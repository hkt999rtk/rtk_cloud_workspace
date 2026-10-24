package main

import (
	"encoding/json"
	"errors"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Runtime validity belongs to credentials used by current or planned consumers.
// Historical exports keep their original bytes and keypair checks; retaining a
// zero-replica rollout revision does not keep its old listener credential active.
func liveRuntimeSecretReferences(store secretStore) (map[string]bool, error) {
	raw, err := secretCheckKubectl([]*deploymentCheckRuntime{store.checkRuntime}, false, "--kubeconfig", store.KubeconfigPath(), "get", "pods,deployments,statefulsets,daemonsets,replicasets,replicationcontrollers,jobs,cronjobs,ingresses,serviceaccounts", "--all-namespaces", "-o", "json")
	if err != nil {
		return nil, errors.New("current credential consumer inventory is unavailable")
	}
	if _, err = decodeDeploymentCheckInventory(raw, "credential consumers"); err != nil {
		return nil, err
	}
	var list struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if json.Unmarshal(raw, &list) != nil {
		return nil, errors.New("current credential consumer inventory is invalid")
	}
	for _, object := range list.Items {
		namespace := deploymentplan.String(deploymentplan.Map(object["metadata"])["namespace"])
		if namespace == "" || (namespace == "video-cloud-"+store.Environment || strings.HasPrefix(namespace, "video-cloud-"+store.Environment+"-")) && !slicesContainsRuntimeConsumer(deploymentplan.String(object["kind"])) {
			return nil, errors.New("current credential consumer inventory has an unknown kind or namespace")
		}
	}
	if store.checkRuntime != nil && store.checkRuntime.preflightPlan != nil {
		for _, r := range store.checkRuntime.preflightPlan.Resources {
			if r.Desired != nil {
				list.Items = append(list.Items, r.Desired)
			} else if r.Baseline != nil {
				list.Items = append(list.Items, r.Baseline)
			}
		}
	}
	return runtimeSecretReferenceIndex("video-cloud-"+store.Environment, list.Items)
}

func runtimeSecretReferenceIndex(stack string, objects []deploymentplan.Object) (map[string]bool, error) {
	refs := map[string]bool{}
	deployments, podOwners := map[string]string{}, map[string]bool{}
	selected := func(ns string) bool { return ns == stack || strings.HasPrefix(ns, stack+"-") }
	for _, o := range objects {
		m := deploymentplan.Map(o["metadata"])
		ns := deploymentplan.String(m["namespace"])
		if ns == "" && slicesContainsRuntimeConsumer(deploymentplan.String(o["kind"])) {
			return nil, errors.New("current credential consumer has no explicit namespace")
		}
		if !selected(ns) {
			continue
		}
		if o["kind"] == "Deployment" && m["deletionTimestamp"] == nil {
			uid := deploymentplan.String(m["uid"])
			if uid != "" {
				deployments[deploymentplan.ID(o)] = uid
			}
		}
		if o["kind"] == "Pod" {
			for _, value := range certIssuerObjectList(m["ownerReferences"]) {
				uid := deploymentplan.String(deploymentplan.Map(value)["uid"])
				if uid != "" {
					podOwners[ns+"/"+uid] = true
				}
			}
		}
	}
	for _, o := range objects {
		m := deploymentplan.Map(o["metadata"])
		ns := deploymentplan.String(m["namespace"])
		if !selected(ns) {
			continue
		}
		var source any
		spec := deploymentplan.Map(o["spec"])
		switch o["kind"] {
		case "Pod":
			source = spec
		case "Deployment", "StatefulSet", "DaemonSet", "ReplicationController", "Job":
			source = deploymentplan.Map(spec["template"])["spec"]
		case "ReplicaSet":
			if historicalRuntimeReplicaSet(o, deployments, podOwners) {
				continue
			}
			source = deploymentplan.Map(spec["template"])["spec"]
		case "CronJob":
			source = deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(spec["jobTemplate"])["spec"])["template"])["spec"]
		case "Ingress":
			source = spec["tls"]
			if source == nil {
				continue
			}
		case "ServiceAccount":
			source = o["imagePullSecrets"]
			if source == nil {
				continue
			}
			source = deploymentplan.Object{"imagePullSecrets": source}
		default:
			continue // Nonconsumer objects in the qualified target.
		}
		podSpec, isMap := source.(map[string]any)
		if source == nil || isMap && len(podSpec) == 0 {
			return nil, errors.New("current credential consumer has no observable pod or TLS specification")
		}
		if err := collectRuntimeSecretReferences(source, ns, refs); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

func slicesContainsRuntimeConsumer(kind string) bool {
	return kind == "Pod" || kind == "Deployment" || kind == "StatefulSet" || kind == "DaemonSet" || kind == "ReplicationController" || kind == "ReplicaSet" || kind == "Job" || kind == "CronJob" || kind == "Ingress" || kind == "ServiceAccount"
}

func historicalRuntimeReplicaSet(o deploymentplan.Object, deployments map[string]string, podOwners map[string]bool) bool {
	m, spec, status := deploymentplan.Map(o["metadata"]), deploymentplan.Map(o["spec"]), deploymentplan.Map(o["status"])
	ns, uid := deploymentplan.String(m["namespace"]), deploymentplan.String(m["uid"])
	if uid == "" || podOwners[ns+"/"+uid] || spec["replicas"] != float64(0) || status["replicas"] != float64(0) {
		return false
	}
	for _, key := range []string{"readyReplicas", "availableReplicas", "fullyLabeledReplicas"} {
		if value, exists := status[key]; exists && value != float64(0) {
			return false
		}
	}
	generation, genOK := m["generation"].(float64)
	observed, observedOK := status["observedGeneration"].(float64)
	if !genOK || !observedOK || observed < generation {
		return false
	}
	var owner deploymentplan.Object
	for _, value := range certIssuerObjectList(m["ownerReferences"]) {
		candidate := deploymentplan.Map(value)
		if candidate["controller"] == true {
			if owner != nil {
				return false
			}
			owner = candidate
		}
	}
	return owner != nil && owner["kind"] == "Deployment" && deploymentplan.String(owner["uid"]) != "" && deployments["Deployment/"+ns+"/"+deploymentplan.String(owner["name"])] == owner["uid"]
}

func collectRuntimeSecretReferences(value any, namespace string, refs map[string]bool) error {
	bad := func() error {
		return errors.New("credential reference is malformed or uses an unsupported external Secret source")
	}
	add := func(name any) error {
		text, ok := name.(string)
		if !ok || text == "" || strings.ContainsAny(text, "/\r\n\x00") {
			return bad()
		}
		refs["Secret/"+namespace+"/"+text] = true
		return nil
	}
	switch v := value.(type) {
	case map[string]any:
		for key, child := range v {
			switch key {
			case "secretName":
				if err := add(child); err != nil {
					return err
				}
			case "secret", "secretKeyRef", "secretRef", "nodePublishSecretRef":
				m, ok := child.(map[string]any)
				if !ok {
					return bad()
				}
				if n := deploymentplan.String(m["namespace"]); n != "" && n != namespace {
					return bad()
				}
				name := m["name"]
				if name == nil {
					name = m["secretName"]
				}
				if err := add(name); err != nil {
					return err
				}
			case "imagePullSecrets":
				items, ok := child.([]any)
				if !ok {
					return bad()
				}
				for _, item := range items {
					m, ok := item.(map[string]any)
					if !ok {
						return bad()
					}
					if err := add(m["name"]); err != nil {
						return err
					}
				}
			case "secretProviderClass":
				return bad()
			}
			if err := collectRuntimeSecretReferences(child, namespace, refs); err != nil {
				return err
			}
		}
	case []any:
		for _, child := range v {
			if err := collectRuntimeSecretReferences(child, namespace, refs); err != nil {
				return err
			}
		}
	}
	return nil
}
