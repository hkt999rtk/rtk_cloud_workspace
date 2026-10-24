package main

import (
	"fmt"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// An OnDelete controller can already match the maintained template while its
// owned Pods still run an older revision. Select the existing explicit update
// lifecycle before qualification; a retained dependency cannot grant a restart.
// The input is the same captured inventory used by the ordinary compiler.
func qualifyEnvironmentOnDeleteOpenBaoPlan(plan *deploymentplan.Plan, definitions []deploymentplan.Definition, live []deploymentplan.Object) error {
	bad := func(id, cause string) error {
		return &deploymentplan.Blocker{ID: "deployment.compatibility.openbao-pod", Resource: id, Cause: cause, Next: "verify the original OpenBao controller and Pod owners; qualify its explicit controlled update with the existing storage, identity and unseal material"}
	}
	if plan == nil {
		return bad("", "compiled OpenBao target is unavailable")
	}
	retained := map[string]bool{}
	for _, definition := range definitions {
		retained[deploymentplan.ID(definition.Object)] = definition.Retain
	}
	objects := map[string]deploymentplan.Object{}
	duplicates := map[string]bool{}
	for _, object := range live {
		id := deploymentplan.ID(object)
		if objects[id] != nil {
			duplicates[id] = true
		}
		objects[id] = object
	}
	var updates []int
	for i, resource := range plan.Resources {
		if resource.Action != "retain" || !environmentOnDeleteOpenBao(resource) {
			continue
		}
		controller := objects[resource.ID]
		if duplicates[resource.ID] {
			return bad(resource.ID, "captured OpenBao controller owner is ambiguous")
		}
		if _, err := resource.Body(controller); err != nil {
			return bad(resource.ID, "retained OpenBao controller differs from its captured owner and baseline")
		}
		metadata, status := deploymentplan.Map(controller["metadata"]), deploymentplan.Map(controller["status"])
		generation, observed := metadata["generation"].(float64)
		revision := deploymentplan.String(status["updateRevision"])
		if !observed || generation <= 0 || status["observedGeneration"] != generation || revision == "" || envObjectReplicas(resource.Desired) <= 0 {
			return bad(resource.ID, "retained OpenBao controller has no observed target revision or positive replica intent")
		}
		older := false
		for ordinal := 0; ordinal < envObjectReplicas(resource.Desired); ordinal++ {
			id := fmt.Sprintf("Pod/%s/openbao-%d", metadata["namespace"], ordinal)
			pod := objects[id]
			if duplicates[id] || pod == nil || validateOpenBaoPodOwner(controller, pod) != nil {
				return bad(id, "original OpenBao Pod is absent, replaced, ambiguous or terminating")
			}
			actual := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(pod["metadata"])["labels"])["controller-revision-hash"])
			if actual == "" {
				return bad(id, "original OpenBao Pod has no controller revision")
			}
			if actual != revision {
				older = true
				continue
			}
			wanted, current := environmentPKIPod(resource.Desired), deploymentplan.Map(pod["spec"])
			for _, group := range []string{"containers", "initContainers"} {
				if len(certIssuerObjectList(wanted[group])) != len(certIssuerObjectList(current[group])) || wanted[group] != nil && !deploymentplan.Matches(deploymentplan.Object{"kind": "Pod", "spec": deploymentplan.Object{group: wanted[group]}}, deploymentplan.Object{"kind": "Pod", "spec": current}) {
					return bad(id, "current-revision OpenBao Pod differs from its checked container target")
				}
			}
		}
		if older {
			if retained[resource.ID] {
				return bad(resource.ID, "explicitly retained OpenBao dependency needs a separately qualified controlled Pod convergence")
			}
			updates = append(updates, i)
		}
	}
	// Do not leave a partially reclassified plan when any captured owner failed.
	for _, index := range updates {
		plan.Resources[index].Action = "update"
	}
	return nil
}
