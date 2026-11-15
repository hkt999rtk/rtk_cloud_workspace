package main

import (
	"context"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Model API-owned fields in real native responses, not renderer settings.
// The captured baseline must retain them across recorded template changes.
type workloadNativeDefaultsBackend struct{ freshStageBackend }

func (b workloadNativeDefaultsBackend) Write(ctx context.Context, action string, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	actual, err := b.freshStageBackend.Write(ctx, action, body, dry)
	if err != nil || actual == nil {
		return actual, err
	}
	if actual["kind"] == "Namespace" {
		m := deploymentplan.Map(actual["metadata"])
		labels := deploymentplan.Map(m["labels"])
		if labels == nil {
			labels = deploymentplan.Object{}
			m["labels"] = labels
		}
		labels["kubernetes.io/metadata.name"] = m["name"]
		actual["spec"] = deploymentplan.Object{"finalizers": []any{"kubernetes"}}
	}
	if actual["kind"] == "Deployment" {
		spec := deploymentplan.Map(actual["spec"])
		spec["progressDeadlineSeconds"], spec["revisionHistoryLimit"] = float64(600), float64(10)
	}
	if pod := environmentPKIPod(actual); pod != nil {
		defaults := deploymentplan.Object{"dnsPolicy": "ClusterFirst", "restartPolicy": "Always", "schedulerName": "default-scheduler", "terminationGracePeriodSeconds": float64(30)}
		for key, value := range defaults {
			if _, declared := pod[key]; !declared {
				pod[key] = value
			}
		}
		if initializers, ok := pod["initContainers"].([]any); ok && len(initializers) == 0 {
			delete(pod, "initContainers")
		}
		for _, raw := range certIssuerObjectList(pod["containers"]) {
			c := deploymentplan.Map(raw)
			c["imagePullPolicy"], c["terminationMessagePath"], c["terminationMessagePolicy"] = "IfNotPresent", "/dev/termination-log", "File"
		}
	}
	if actual["kind"] == "Service" {
		spec := deploymentplan.Map(actual["spec"])
		spec["clusterIP"], spec["clusterIPs"], spec["ipFamilies"], spec["ipFamilyPolicy"], spec["sessionAffinity"] = "10.0.0.25", []any{"10.0.0.25"}, []any{"IPv4"}, "SingleStack", "None"
	}
	if !dry {
		b.objects[deploymentplan.ID(actual)] = deploymentplan.Clone(actual)
	}
	return actual, nil
}
