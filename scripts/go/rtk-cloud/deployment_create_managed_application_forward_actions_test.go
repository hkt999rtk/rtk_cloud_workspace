package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedApplicationForwardActionsFixture(t *testing.T) (*createManagedApplicationPartialRecovery, *deploymentplan.Plan) {
	t.Helper()
	store, e, _, _ := managedApplicationPartialFixture(t)
	_, _, partial, err := loadManagedApplicationPartialSource(store, e)
	if err != nil {
		t.Fatal(err)
	}
	cfg, p, release, runtime, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	final, err := finalizeEnvironmentCreateApplicationDefinitions(context.Background(), cfg, p, release, runtime, service, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	owned := func(body deploymentplan.Object) deploymentplan.Object {
		body = deploymentplan.Clone(body)
		metadata := deploymentplan.Map(body["metadata"])
		labels := deploymentplan.Map(metadata["labels"])
		if labels == nil {
			labels = deploymentplan.Object{}
			metadata["labels"] = labels
		}
		labels["rtk.realtek.com/stack"], labels["app.kubernetes.io/managed-by"] = p.Stack, "rtk-cloud"
		return body
	}
	for index := range partial.Plan.Resources {
		resource := &partial.Plan.Resources[index]
		if index >= 20 {
			resource.Desired = owned(createRecipeObject(t, final, resource.ID))
			if index == 22 || index == 25 {
				c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(resource.Desired)["containers"])[0])
				for _, raw := range certIssuerObjectList(c["volumeMounts"]) {
					m := deploymentplan.Map(raw)
					switch m["mountPath"] {
					case "/run/pki-mqtt-crls/crls.json":
						m["mountPath"] = "/run/pki-mqtt/crls.json"
					case "/run/pki-service-crls/outbound-crls.json":
						m["mountPath"] = environmentCreateServiceTrust + "/outbound-crls.json"
					}
				}
				for _, raw := range certIssuerObjectList(c["env"]) {
					m := deploymentplan.Map(raw)
					switch m["value"] {
					case "/run/pki-mqtt-crls/crls.json":
						m["value"] = "/run/pki-mqtt/crls.json"
					case "/run/pki-service-crls/outbound-crls.json":
						m["value"] = environmentCreateServiceTrust + "/outbound-crls.json"
					}
				}
			}
			if index > 22 {
				resource.Baseline = owned(createRecipeObject(t, initial, resource.ID))
			}
		} else {
			resource.Desired = owned(resource.Desired)
		}
		if resource.Baseline != nil {
			resource.Baseline = owned(resource.Baseline)
		}
		if index >= 22 && index <= 24 {
			// The authentic interrupted predecessor lacked this base field.
			// Start from those old bytes; the future candidate adds it explicitly.
			for _, body := range []deploymentplan.Object{resource.Desired, resource.Baseline} {
				if body == nil {
					continue
				}
				for _, raw := range certIssuerObjectList(environmentPKIPod(body)["containers"]) {
					container := deploymentplan.Map(raw)
					var settings []any
					for _, setting := range certIssuerObjectList(container["env"]) {
						if deploymentplan.Map(setting)["name"] != "VIDEO_CLOUD_CONTROLLER_SERVER_CRL_MANIFEST" {
							settings = append(settings, setting)
						}
					}
					container["env"] = settings
				}
			}
		}
	}
	partial.Journal.PlanSHA256 = partial.Plan.SHA256()
	if err := validateManagedApplicationPartialJournal(partial.Plan, partial.Journal); err != nil {
		t.Fatal("synthetic source lost exact partial progress", err)
	}
	definitions, err := managedApplicationPartialDefinitions(partial)
	if err != nil {
		t.Fatal(err)
	}
	live := []deploymentplan.Object{}
	for index, resource := range partial.Plan.Resources {
		object, uid := resource.Desired, partial.Journal.Owners[resource.ID]
		if index > 22 {
			object, uid = resource.Baseline, resource.UID
		}
		actual := deploymentplan.Clone(object)
		deploymentplan.Map(actual["metadata"])["uid"] = uid
		live = append(live, actual)
	}
	beforePlan, beforeJournal := partial.Plan.SHA256(), deploymentplan.Digest(partial.Journal)
	inputs := partial.Plan.Inputs
	inputs.Operation = "scoped"
	inputs.BindingsSHA256 = environmentCreateStageBindingsSHA("managed-application-owners-forward", definitions)
	candidate, err := deploymentplan.Compile(inputs, definitions, live)
	if err != nil {
		t.Fatal("real planner failed exact retained generation", err)
	}
	if partial.Plan.SHA256() != beforePlan || deploymentplan.Digest(partial.Journal) != beforeJournal {
		t.Fatal("candidate compile rewrote failed proof")
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", store.ConfigRoot)
	return partial, candidate
}

func TestManagedApplicationPartialForwardRealPlannerRetainsCompletedOwnersAndUpdatesFourOriginalWorkloads(t *testing.T) {
	partial, candidate := managedApplicationForwardActionsFixture(t)
	if err := validateManagedApplicationPartialCandidate(partial, candidate); err != nil {
		t.Fatal(err)
	}
	if len(candidate.Resources) != 26 {
		t.Fatal("candidate scope changed")
	}
	for index, resource := range candidate.Resources {
		action, uid := "retain", partial.Journal.Owners[resource.ID]
		if index >= 22 {
			action = "update"
		}
		if index > 22 {
			uid = partial.Plan.Resources[index].UID
		}
		if resource.Action != action || resource.UID != uid || resource.Baseline == nil {
			t.Fatal("real planner lost twenty-two/four split", index, resource.ID, resource.Action)
		}
	}
	beforePlan, beforeJournal := partial.Plan.SHA256(), deploymentplan.Digest(partial.Journal)
	for _, mode := range []string{"original", "completed-write", "changed-original-uid", "extra-environment", "changed-image", "changed-private-key-path", "changed-private-claim", "untouched-baseline", "extra-resource", "dag-delta", "completed-body"} {
		t.Run(mode, func(t *testing.T) {
			var changed deploymentplan.Plan
			if json.Unmarshal(mustProviderRecoveryJSON(candidate), &changed) != nil {
				t.Fatal("fixture cloning failed")
			}
			container := func(index int) deploymentplan.Object {
				return deploymentplan.Map(certIssuerObjectList(environmentPKIPod(changed.Resources[index].Desired)["containers"])[0])
			}
			switch mode {
			case "completed-write":
				changed.Resources[0].Action = "update"
			case "changed-original-uid":
				changed.Resources[22].UID = "88888888-8888-4888-8888-888888888888"
			case "extra-environment":
				c := container(22)
				c["env"] = append(certIssuerObjectList(c["env"]), deploymentplan.Object{"name": "UNREVIEWED", "value": "unqualified"})
			case "changed-image":
				container(22)["image"] = "ghcr.io/unqualified/api@sha256:" + strings.Repeat("f", 64)
			case "changed-private-key-path":
				found := false
				for _, raw := range certIssuerObjectList(container(24)["env"]) {
					m := deploymentplan.Map(raw)
					if m["name"] == "VIDEO_CLOUD_API_TLS_KEY_PATH" {
						m["value"] = "/foreign/private.key"
						found = true
					}
				}
				if !found {
					t.Fatal("maintained API private key path absent")
				}
			case "changed-private-claim":
				found := false
				for _, raw := range certIssuerObjectList(environmentPKIPod(changed.Resources[24].Desired)["volumes"]) {
					claim := deploymentplan.Map(deploymentplan.Map(raw)["persistentVolumeClaim"])
					if claim != nil {
						claim["claimName"] = "foreign-private-state"
						found = true
						break
					}
				}
				if !found {
					t.Fatal("maintained private claim absent")
				}
			case "untouched-baseline":
				deploymentplan.Map(certIssuerObjectList(environmentPKIPod(changed.Resources[23].Baseline)["containers"])[0])["image"] = "ghcr.io/unqualified/bootstrap@sha256:" + strings.Repeat("f", 64)
			case "extra-resource":
				changed.Resources = append(changed.Resources, changed.Resources[0])
			case "dag-delta":
				changed.Resources[22].Dependencies = append(changed.Resources[22].Dependencies, changed.Resources[4].ID)
			case "completed-body":
				container(21)["image"] = "ghcr.io/unqualified/broker@sha256:" + strings.Repeat("f", 64)
			}
			if (validateManagedApplicationPartialCandidate(partial, &changed) == nil) != (mode == "original") {
				t.Fatal("unqualified candidate delta accepted", mode)
			}
		})
	}
	if partial.Plan.SHA256() != beforePlan || deploymentplan.Digest(partial.Journal) != beforeJournal {
		t.Fatal("validation changed immutable original generation")
	}
}
