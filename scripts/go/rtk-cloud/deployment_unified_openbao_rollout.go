package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func envObjectReplicas(object deploymentplan.Object) int {
	n, _ := deploymentplan.Map(object["spec"])["replicas"].(float64)
	return int(n)
}

func environmentOnDeleteOpenBao(resource deploymentplan.Resource) bool {
	return resource.Desired["kind"] == "StatefulSet" && deploymentplan.Map(resource.Desired["metadata"])["name"] == "openbao" && deploymentplan.Map(deploymentplan.Map(resource.Desired["spec"])["updateStrategy"])["type"] == "OnDelete"
}

func environmentOnDeleteUpdate(resource deploymentplan.Resource) bool {
	return resource.Action == "update" && environmentOnDeleteOpenBao(resource)
}

func validateOpenBaoPodOwner(controller, pod deploymentplan.Object) error {
	meta := deploymentplan.Map(pod["metadata"])
	owner := deploymentplan.Map(controller["metadata"])
	refs := certIssuerObjectList(meta["ownerReferences"])
	if deploymentplan.String(meta["uid"]) == "" || deploymentplan.String(meta["resourceVersion"]) == "" || meta["deletionTimestamp"] != nil || len(refs) != 1 {
		return errors.New("deployment.execution.openbao: Pod ownership is absent, ambiguous or terminating")
	}
	ref := deploymentplan.Map(refs[0])
	if ref["controller"] != true || ref["kind"] != "StatefulSet" || ref["name"] != owner["name"] || ref["uid"] != owner["uid"] || owner["uid"] == nil {
		return errors.New("deployment.execution.openbao: Pod does not belong to the checked StatefulSet UID")
	}
	return nil
}

func (b environmentKubernetesBackend) checkOpenBaoRestartAdmission(ctx context.Context, plan *deploymentplan.Plan) error {
	for _, resource := range plan.Resources {
		if !environmentOnDeleteUpdate(resource) {
			continue
		}
		controller, err := b.Read(ctx, resource.ID)
		if err != nil {
			return err
		}
		if controller == nil || deploymentplan.Map(controller["metadata"])["uid"] != resource.UID {
			return errors.New("deployment.execution.openbao: restart controller owner changed")
		}
		for ordinal := 0; ordinal < envObjectReplicas(resource.Desired); ordinal++ {
			id := fmt.Sprintf("Pod/%s/openbao-%d", deploymentplan.Map(controller["metadata"])["namespace"], ordinal)
			pod, err := b.Read(ctx, id)
			if err != nil {
				return err
			}
			if err := validateOpenBaoPodOwner(controller, pod); err != nil {
				return err
			}
			if _, err := b.retire(ctx, pod, true); err != nil {
				return err
			}
		}
	}
	return nil
}

// OnDelete does not implement rollout status or replace existing Pods. Restart
// only the checked controller's old revision, one ordinal at a time, using
// native UID/resourceVersion delete preconditions. Never delete a PVC or unseal
// with invented credentials. A sealed/unhealthy replacement stops execution.
// Retained targets only verify the original current Pod; they never restart it.
func (b environmentKubernetesBackend) waitOnDeleteOpenBao(ctx context.Context, resource deploymentplan.Resource) error {
	if b.RolloutTimeout <= 0 {
		return &deploymentplan.Blocker{ID: "deployment.configuration.rollout-timeout", Resource: resource.ID, Cause: "a positive shared rollout timeout is required", Next: "set DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS in the common architecture configuration and repeat full preflight"}
	}
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, b.RolloutTimeout)
	defer cancel()
	retained := resource.Action == "retain"
	if retained && envObjectReplicas(resource.Desired) <= 0 {
		return errors.New("deployment.execution.openbao: retained replica intent is absent")
	}
	readController := func() (deploymentplan.Object, error) {
		current, err := b.Read(ctx, resource.ID)
		if err != nil {
			return nil, err
		}
		if current == nil || resource.UID == "" || deploymentplan.Map(current["metadata"])["uid"] != resource.UID || !deploymentplan.Matches(resource.Desired, current) {
			return nil, errors.New("deployment.execution.openbao: StatefulSet drifted before its controlled restart")
		}
		return current, nil
	}
	pause := func() error {
		select {
		case <-ctx.Done():
			if parent.Err() != nil {
				return parent.Err()
			}
			return &deploymentRuntimeError{"KUBE_TIMEOUT", "target OpenBao Pod did not become ready within the configured rollout window"}
		case <-time.After(time.Second):
			return nil
		}
	}
	retainedRevision := ""
	var retainedGeneration float64
	for ordinal := envObjectReplicas(resource.Desired) - 1; ordinal >= 0; ordinal-- {
		deletedUID := ""
		retainedUID := ""
		preparedUID := ""
		for {
			controller, err := readController()
			if err != nil {
				return err
			}
			status, meta := deploymentplan.Map(controller["status"]), deploymentplan.Map(controller["metadata"])
			revision := deploymentplan.String(status["updateRevision"])
			if retained {
				generation, ok := meta["generation"].(float64)
				if !ok || generation <= 0 || status["observedGeneration"] != generation || revision == "" {
					return errors.New("deployment.execution.openbao: retained controller target revision is not observed")
				}
				if retainedRevision != "" && (retainedRevision != revision || retainedGeneration != generation) {
					return errors.New("deployment.execution.openbao: retained controller revision changed while waiting")
				}
				retainedRevision, retainedGeneration = revision, generation
			}
			if revision == "" || status["observedGeneration"] != meta["generation"] {
				if err := pause(); err != nil {
					return err
				}
				continue
			}
			podID := fmt.Sprintf("Pod/%s/openbao-%d", meta["namespace"], ordinal)
			pod, err := b.Read(ctx, podID)
			if err != nil {
				return err
			}
			if pod == nil {
				if retained {
					return errors.New("deployment.execution.openbao: retained original Pod is absent")
				}
				if err := pause(); err != nil {
					return err
				}
				continue
			}
			if deletedUID != "" && deploymentplan.Map(pod["metadata"])["uid"] == deletedUID {
				if err := pause(); err != nil {
					return err
				}
				continue
			}
			if err := validateOpenBaoPodOwner(controller, pod); err != nil {
				return err
			}
			podMeta := deploymentplan.Map(pod["metadata"])
			uid := deploymentplan.String(podMeta["uid"])
			actual := deploymentplan.String(deploymentplan.Map(podMeta["labels"])["controller-revision-hash"])
			if retained {
				if retainedUID != "" && retainedUID != uid {
					return errors.New("deployment.execution.openbao: retained original Pod UID changed while waiting")
				}
				retainedUID = uid
				wanted, actualPod := environmentPKIPod(resource.Desired), deploymentplan.Map(pod["spec"])
				for _, group := range []string{"containers", "initContainers"} {
					if len(certIssuerObjectList(wanted[group])) != len(certIssuerObjectList(actualPod[group])) || wanted[group] != nil && !deploymentplan.Matches(deploymentplan.Object{"kind": "Pod", "spec": deploymentplan.Object{group: wanted[group]}}, deploymentplan.Object{"kind": "Pod", "spec": actualPod}) {
						return errors.New("deployment.execution.openbao: retained Pod differs from the checked container target")
					}
				}
			}
			if actual != revision {
				if retained {
					return errors.New("deployment.execution.openbao: retained Pod has an old or unexpected revision; qualify an explicit update")
				}
				if deletedUID != "" {
					return errors.New("deployment.execution.openbao: replacement Pod has an unexpected revision")
				}
				// Recheck the exact controller immediately before the conditional delete.
				if _, err := readController(); err != nil {
					return err
				}
				if _, err := b.retire(ctx, pod, false); err != nil {
					return err
				}
				deletedUID = uid
			} else if uid != deletedUID {
				if b.OpenBaoReplacementReady != nil && uid != preparedUID && deploymentplan.Map(pod["status"])["phase"] == "Running" {
					if err := b.OpenBaoReplacementReady(ctx, controller, pod); err != nil {
						return err
					}
					preparedUID = uid
				}
				ready := false
				for _, value := range certIssuerObjectList(deploymentplan.Map(pod["status"])["conditions"]) {
					condition := deploymentplan.Map(value)
					if condition["type"] == "Ready" && condition["status"] == "True" {
						ready = true
					}
				}
				if ready && (!retained || deploymentplan.Map(pod["status"])["phase"] == "Running") {
					wanted, actualPod := environmentPKIPod(resource.Desired), deploymentplan.Map(pod["spec"])
					for _, group := range []string{"containers", "initContainers"} {
						if wanted[group] != nil && !deploymentplan.Matches(deploymentplan.Object{"kind": "Pod", "spec": deploymentplan.Object{group: wanted[group]}}, deploymentplan.Object{"kind": "Pod", "spec": actualPod}) {
							return errors.New("deployment.execution.openbao: ready Pod differs from the checked container target")
						}
					}
					break
				}
			}
			if err := pause(); err != nil {
				return err
			}
		}
	}
	return nil
}
