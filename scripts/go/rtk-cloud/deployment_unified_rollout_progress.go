package main

import (
	"context"
	"errors"
	"fmt"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Kubernetes wait for Complete alone does not stop for a terminal failed Job.
// Observe failure while preserving that original wait and its exact deadline.
func (b environmentKubernetesBackend) waitJobTerminal(ctx context.Context, resource deploymentplan.Resource, args []string) error {
	watch, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { _, err := b.command(watch, nil, args...); done <- err }()
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-ctx.Done():
			cancel()
			<-done
			return ctx.Err()
		case <-ticker.C:
			object, err := b.Read(watch, resource.ID)
			if err != nil || object == nil {
				continue
			}
			if resource.UID != "" && deploymentplan.Map(object["metadata"])["uid"] != resource.UID {
				cancel()
				<-done
				return &deploymentplan.Blocker{ID: "deployment.rollout.job-owner", Resource: resource.ID, Cause: "Job owner changed during readiness wait", Next: "inspect original owners and execution journal before recovery"}
			}
			for _, v := range certIssuerObjectList(deploymentplan.Map(object["status"])["conditions"]) {
				condition := deploymentplan.Map(v)
				if (condition["type"] == "Failed" || condition["type"] == "FailureTarget") && condition["status"] == "True" {
					cancel()
					<-done
					return &deploymentplan.Blocker{ID: "deployment.rollout.job-failed", Resource: resource.ID, Cause: "Job reached a native terminal failure; readiness cannot succeed", Next: "inspect this original Job and its private completed-resource journal; qualify a distinct forward repair without replaying unknown writes"}
				}
			}
		}
	}
}

// Observations run only while the original wait runs. They neither restart the
// rollout nor change its deadline or outcome, and join before final output.
func (b environmentKubernetesBackend) waitWithRolloutProgress(ctx context.Context, resource deploymentplan.Resource, wait func() error) error {
	_, reporting := ctx.Value(environmentProgressKey{}).(*environmentProgress)
	kind := deploymentplan.String(resource.Desired["kind"])
	if !reporting || resource.Action == "retire" || kind != "Deployment" && kind != "StatefulSet" && kind != "DaemonSet" {
		return wait()
	}
	observations, cancel := context.WithCancel(ctx)
	stopped := make(chan struct{})
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			object, err := b.Read(observations, resource.ID)
			if err == nil && object != nil && (resource.UID == "" || deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"]) == resource.UID) {
				status, spec := deploymentplan.Map(object["status"]), deploymentplan.Map(object["spec"])
				desired, ready, updated := 1, progressReplicaCount(status["readyReplicas"]), progressReplicaCount(status["updatedReplicas"])
				if count, present := spec["replicas"]; present {
					desired = progressReplicaCount(count)
				}
				if kind == "DaemonSet" {
					desired, ready, updated = progressReplicaCount(status["desiredNumberScheduled"]), progressReplicaCount(status["numberReady"]), progressReplicaCount(status["updatedNumberScheduled"])
				}
				environmentDeploymentRolloutProgress(ctx, resource.ID, ready, updated, desired, int(b.RolloutTimeout/time.Second))
			}
			select {
			case <-observations.Done():
				return
			case <-ticker.C:
			}
		}
	}()
	defer func() { cancel(); <-stopped }()
	return wait()
}

func progressReplicaCount(value any) int {
	if value == nil {
		return 0
	}
	count, ok := value.(float64)
	if !ok || count < 0 || count > 1000000 || count != float64(int(count)) {
		return -1
	}
	return int(count)
}

func environmentRolloutFailure(ctx context.Context, resource deploymentplan.Resource, err error, waitWindow time.Duration) error {
	if err == nil {
		return nil
	}
	var existing *deploymentplan.Blocker
	if errors.As(err, &existing) {
		return err
	}
	if ctx.Err() != nil {
		err = ctx.Err()
	}
	id, cause := "deployment.rollout", "resource readiness could not be verified"
	switch {
	case errors.Is(err, context.Canceled):
		id, cause = "deployment.rollout.cancelled", "deployment was cancelled while waiting for this resource"
	case errors.Is(err, context.DeadlineExceeded):
		id, cause = "deployment.rollout.deadline", "the execution or command deadline expired while waiting for this resource"
	default:
		var failure *deploymentRuntimeError
		if errors.As(err, &failure) && failure.code == "KUBE_TIMEOUT" {
			id, cause = "deployment.rollout.timeout", fmt.Sprintf("resource readiness wait timed out (configured rollout limit %ds)", int64(waitWindow/time.Second))
		}
	}
	blocker := &deploymentplan.Blocker{ID: id, Resource: resource.ID, Cause: cause, Next: "inspect this resource's Pod scheduling, startup and readiness; review the per-resource rollout wait separately from the overall --timeout, then repeat full preflight against the partial deployment"}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return errors.Join(blocker, err)
	}
	return blocker
}
