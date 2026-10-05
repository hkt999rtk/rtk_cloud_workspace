package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
)

type deploymentHealthResource struct {
	Kind     string `json:"kind"`
	Metadata struct {
		Name              string `json:"name"`
		Namespace         string `json:"namespace"`
		Generation        int64  `json:"generation"`
		DeletionTimestamp string `json:"deletionTimestamp"`
		OwnerReferences   []struct {
			Kind string `json:"kind"`
			Name string `json:"name"`
		} `json:"ownerReferences"`
	} `json:"metadata"`
	Spec struct {
		Replicas       *int32 `json:"replicas"`
		UpdateStrategy struct {
			Type string `json:"type"`
		} `json:"updateStrategy"`
		Containers     []deploymentHealthImage `json:"containers"`
		InitContainers []deploymentHealthImage `json:"initContainers"`
		Template       struct {
			Spec struct {
				Containers     []deploymentHealthImage `json:"containers"`
				InitContainers []deploymentHealthImage `json:"initContainers"`
			} `json:"spec"`
		} `json:"template"`
	} `json:"spec"`
	Status struct {
		ObservedGeneration     int64  `json:"observedGeneration"`
		Replicas               int32  `json:"replicas"`
		ReadyReplicas          int32  `json:"readyReplicas"`
		UpdatedReplicas        int32  `json:"updatedReplicas"`
		AvailableReplicas      int32  `json:"availableReplicas"`
		UnavailableReplicas    int32  `json:"unavailableReplicas"`
		CurrentRevision        string `json:"currentRevision"`
		UpdateRevision         string `json:"updateRevision"`
		DesiredNumberScheduled int32  `json:"desiredNumberScheduled"`
		UpdatedNumberScheduled int32  `json:"updatedNumberScheduled"`
		NumberReady            int32  `json:"numberReady"`
		NumberUnavailable      int32  `json:"numberUnavailable"`
		Phase                  string `json:"phase"`
		Conditions             []struct {
			Type   string `json:"type"`
			Status string `json:"status"`
		} `json:"conditions"`
		InitContainerStatuses []deploymentHealthContainer `json:"initContainerStatuses"`
		ContainerStatuses     []deploymentHealthContainer `json:"containerStatuses"`
	} `json:"status"`
}

type deploymentHealthImage struct {
	Name  string `json:"name"`
	Image string `json:"image"`
}

type deploymentHealthContainer struct {
	Name  string `json:"name"`
	Ready bool   `json:"ready"`
	State struct {
		Waiting *struct {
			Reason string `json:"reason"`
		} `json:"waiting"`
		Terminated *struct {
			ExitCode int `json:"exitCode"`
		} `json:"terminated"`
	} `json:"state"`
}

func deploymentHealthNamespace(environment, namespace string) bool {
	stack := "video-cloud-" + environment
	return namespace == stack || strings.HasPrefix(namespace, stack+"-")
}

// Read status, never logs, exec commands, Secret values or failure messages.
// Completed/failed acceptance Jobs are not long-running application workloads.
func verifyDeploymentWorkloadHealth(store secretStore) error {
	raw, err := secretCheckKubectl([]*deploymentCheckRuntime{store.checkRuntime}, false,
		"--kubeconfig", store.KubeconfigPath(), "get", "deployments,statefulsets,daemonsets,pods", "--all-namespaces", "-o", "json")
	if err != nil {
		return secretCheckFailure(err, "cannot inspect environment workload health")
	}
	return validateDeploymentWorkloadHealth(store.Environment, raw)
}

func validateDeploymentWorkloadHealth(environment string, raw []byte) error {
	var inventory struct {
		Items []deploymentHealthResource `json:"items"`
	}
	if json.Unmarshal(raw, &inventory) != nil || inventory.Items == nil {
		return &deploymentRuntimeError{"KUBE_INVALID_RESPONSE", "workload inventory is invalid"}
	}
	controllers, pods := 0, 0
	var failures []string
	for _, resource := range inventory.Items {
		if !deploymentHealthNamespace(environment, resource.Metadata.Namespace) {
			continue
		}
		name := resource.Metadata.Namespace + "/" + resource.Metadata.Name
		if resource.Metadata.Name == "" {
			return errors.New("environment workload has no resource name")
		}
		desired := int32(1)
		if resource.Spec.Replicas != nil {
			desired = *resource.Spec.Replicas
		}
		current := resource.Status.ObservedGeneration >= resource.Metadata.Generation
		switch resource.Kind {
		case "Deployment":
			controllers++
			if desired == 0 {
				continue
			}
			if !current || resource.Status.UpdatedReplicas != desired || resource.Status.AvailableReplicas != desired || resource.Status.ReadyReplicas != desired || resource.Status.UnavailableReplicas != 0 {
				failures = append(failures, name+" deployment rollout is not complete and Ready")
			}
		case "StatefulSet":
			controllers++
			if desired == 0 {
				continue
			}
			updated := resource.Status.UpdatedReplicas == desired && resource.Status.CurrentRevision != "" && resource.Status.CurrentRevision == resource.Status.UpdateRevision
			if resource.Spec.UpdateStrategy.Type == "OnDelete" {
				// OnDelete does not maintain updatedReplicas. Require the live
				// owned Pods to serve the declared images, plus their health below.
				updated = deploymentHealthOnDeleteImages(resource, inventory.Items, desired)
			}
			if !current || resource.Status.ReadyReplicas != desired || !updated {
				failures = append(failures, name+" stateful rollout is not complete and Ready")
			}
		case "DaemonSet":
			controllers++
			if !current || resource.Status.NumberReady != resource.Status.DesiredNumberScheduled || resource.Status.UpdatedNumberScheduled != resource.Status.DesiredNumberScheduled || resource.Status.NumberUnavailable != 0 {
				failures = append(failures, name+" daemon rollout is not complete and Ready")
			}
		case "Pod":
			job := false
			for _, owner := range resource.Metadata.OwnerReferences {
				if owner.Kind == "Job" {
					job = true
				}
			}
			if job || resource.Metadata.DeletionTimestamp != "" {
				continue
			}
			pods++
			ready := false
			for _, condition := range resource.Status.Conditions {
				if condition.Type == "Ready" && condition.Status == "True" {
					ready = true
				}
			}
			if resource.Status.Phase != "Running" || !ready || len(resource.Status.ContainerStatuses) == 0 {
				failures = append(failures, name+" pod is not Running and Ready")
			}
			for _, container := range resource.Status.InitContainerStatuses {
				if container.State.Waiting != nil || (container.State.Terminated != nil && container.State.Terminated.ExitCode != 0) {
					failures = append(failures, name+" init container has not completed successfully")
				}
			}
			for _, container := range resource.Status.ContainerStatuses {
				if !container.Ready || container.State.Waiting != nil || container.State.Terminated != nil {
					failures = append(failures, name+" container "+container.Name+" is not running and Ready")
				}
			}
		default:
			return errors.New("workload inventory contains an unexpected resource kind")
		}
	}
	if controllers == 0 || pods == 0 {
		return &deploymentRuntimeError{"KUBE_INVENTORY_EMPTY", "selected environment has no complete controller and Pod inventory"}
	}
	if len(failures) > 0 {
		sort.Strings(failures)
		return fmt.Errorf("environment workload health failed: %s", strings.Join(uniqueNonEmpty(failures...), "; "))
	}
	return nil
}

func deploymentHealthOnDeleteImages(controller deploymentHealthResource, inventory []deploymentHealthResource, desired int32) bool {
	expected := map[string]string{}
	for _, c := range controller.Spec.Template.Spec.Containers {
		expected["app/"+c.Name] = c.Image
	}
	for _, c := range controller.Spec.Template.Spec.InitContainers {
		expected["init/"+c.Name] = c.Image
	}
	if len(controller.Spec.Template.Spec.Containers) == 0 {
		return false
	}
	observed := int32(0)
	for _, pod := range inventory {
		if pod.Kind != "Pod" || pod.Metadata.Namespace != controller.Metadata.Namespace || pod.Metadata.DeletionTimestamp != "" {
			continue
		}
		owned := false
		for _, owner := range pod.Metadata.OwnerReferences {
			if owner.Kind == "StatefulSet" && owner.Name == controller.Metadata.Name {
				owned = true
			}
		}
		if !owned {
			continue
		}
		actual := map[string]string{}
		for _, c := range pod.Spec.Containers {
			actual["app/"+c.Name] = c.Image
		}
		for _, c := range pod.Spec.InitContainers {
			actual["init/"+c.Name] = c.Image
		}
		for key, image := range expected {
			if image == "" || actual[key] != image {
				return false
			}
		}
		observed++
	}
	return observed == desired
}
