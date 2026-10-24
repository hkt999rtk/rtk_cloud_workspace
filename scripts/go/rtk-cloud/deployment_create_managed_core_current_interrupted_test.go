package main

import (
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedCoreCurrentCertIssuerFixture(t *testing.T) *createManagedCorePartialRecovery {
	t.Helper()
	_, _, _, r, _, candidate, _, _ := managedCoreAuthAdmissionFixture(t, true)
	core := r.ManagedCorePartial
	j := deploymentplan.Journal{PlanSHA256: candidate.SHA256(), Owners: map[string]string{}, Failed: candidate.Resources[14].ID}
	for index, resource := range candidate.Resources {
		if index < 14 {
			j.Completed = append(j.Completed, resource.ID)
		}
		if index < 15 {
			j.Owners[resource.ID] = resource.UID
		}
	}
	core.PriorForward = &createManagedCoreInterruptedForward{Review: strings.Repeat("7", 64), SourceSHA: core.PriorAdmission.SourceSHA, ArtifactSHA: strings.Repeat("8", 64), Plan: candidate, Journal: j}
	core.SourceSHA = strings.Repeat("6", 64)
	if _, _, err := managedCorePartialCurrentPlan(core); err != nil {
		t.Fatal(err)
	}
	return core
}

func TestManagedCoreCurrentCertIssuerBoundaryPreservesRealProgress(t *testing.T) {
	for _, index := range []int{0, 12, 13, 14, 15} {
		for _, mode := range []string{"current", "api-defaults", "old-controller", "old-certissuer", "future-account", "replacement", "terminating", "recipe-drift", "fake-completion", "fake-acceptance", "missing-forward", "changed-original"} {
			t.Run(string(rune('A'+index))+"/"+mode, func(t *testing.T) {
				core := managedCoreCurrentCertIssuerFixture(t)
				plan, journal, err := managedCorePartialCurrentPlan(core)
				if err != nil {
					t.Fatal(err)
				}
				resource := plan.Resources[index]
				body, uid := resource.Desired, journal.Owners[resource.ID]
				if index > 14 {
					body, uid = resource.Baseline, resource.UID
				}
				actual := deploymentplan.Clone(body)
				deploymentplan.Map(actual["metadata"])["uid"] = uid
				pass := true
				switch mode {
				case "api-defaults":
					deploymentplan.Map(actual["metadata"])["resourceVersion"] = "57"
				case "old-controller":
					if index == 13 {
						actual = deploymentplan.Clone(core.Plan.Resources[13].Desired)
						deploymentplan.Map(actual["metadata"])["uid"] = uid
						pass = false
					}
				case "old-certissuer":
					if index == 14 {
						actual = deploymentplan.Clone(core.Plan.Resources[14].Baseline)
						deploymentplan.Map(actual["metadata"])["uid"] = uid
						pass = false
					}
				case "future-account":
					if index == 15 {
						actual = deploymentplan.Clone(resource.Desired)
						deploymentplan.Map(actual["metadata"])["uid"] = uid
						deploymentplan.Map(actual["metadata"])["labels"] = deploymentplan.Object{"foreign": "future"}
						pass = false
					}
				case "replacement":
					deploymentplan.Map(actual["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
					pass = false
				case "terminating":
					deploymentplan.Map(actual["metadata"])["deletionTimestamp"] = time.Now().Format(time.RFC3339)
					pass = false
				case "recipe-drift":
					if index >= 13 {
						mqttFoundationPrimary(actual)["image"] = "foreign"
					} else {
						actual["kind"] = "foreign"
					}
					pass = false
				case "fake-completion":
					core.PriorForward.Journal.Completed = append(core.PriorForward.Journal.Completed, plan.Resources[14].ID)
					pass = false
				case "fake-acceptance":
					core.PriorForward.Journal.Accepted = true
					pass = false
				case "missing-forward":
					core.PriorForward = nil
					pass = index < 13 || index == 15
				case "changed-original":
					core.Journal.Failed = "foreign"
					pass = false
				}
				before := deploymentplan.Digest(core)
				if (validateManagedCoreCurrentOwner(core, index, actual) == nil) != pass {
					t.Fatal("wrong genuine current boundary", index, mode)
				}
				if deploymentplan.Digest(core) != before {
					t.Fatal("current observer fabricated original progress")
				}
			})
		}
	}
}

func TestManagedCorePolicyCarrierRequiresGenuinelyCompletedController(t *testing.T) {
	for _, mode := range []string{"current-controller", "old-false-recipe", "certissuer-cannot-be-carrier", "replacement", "not-completed", "fake-ready-boundary", "no-current-forward"} {
		t.Run(mode, func(t *testing.T) {
			core := managedCoreCurrentCertIssuerFixture(t)
			actual := deploymentplan.Clone(core.PriorForward.Plan.Resources[13].Desired)
			deploymentplan.Map(actual["metadata"])["uid"] = core.PriorForward.Journal.Owners[core.PriorForward.Plan.Resources[13].ID]
			switch mode {
			case "old-false-recipe":
				actual = deploymentplan.Clone(core.Plan.Resources[13].Desired)
				deploymentplan.Map(actual["metadata"])["uid"] = core.Journal.Owners[core.Plan.Resources[13].ID]
			case "certissuer-cannot-be-carrier":
				actual = deploymentplan.Clone(core.PriorForward.Plan.Resources[14].Desired)
			case "replacement":
				deploymentplan.Map(actual["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "not-completed":
				core.PriorForward.Journal.Completed = core.PriorForward.Journal.Completed[:13]
			case "fake-ready-boundary":
				core.PriorForward.Journal.Failed = core.PriorForward.Plan.Resources[13].ID
			case "no-current-forward":
				core.PriorForward = nil
			}
			before := deploymentplan.Digest(core)
			resource, err := managedCoreReadyControllerResource(core, actual)
			if (err == nil) != (mode == "current-controller") {
				t.Fatal("wrong native carrier authority", mode, err)
			}
			if err == nil && (resource.Action != "retain" || resource.UID != core.Journal.Owners[resource.ID] || resource.ID != core.Plan.Resources[13].ID) {
				t.Fatal("carrier lost exact original UID")
			}
			if deploymentplan.Digest(core) != before {
				t.Fatal("carrier verifier changed original source")
			}
		})
	}
}
