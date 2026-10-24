package main

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedTransportAcceptedForwardFixture(t *testing.T) (secretStore, *environmentCreateExecution, *createManagedTransportPartialRecovery) {
	t.Helper()
	store, e, partial := managedTransportWrittenOpenBaoFixture(t)
	source := e.Journal
	source.Entries = slices.Clone(e.Journal.Entries)
	source.KubernetesLeases = slices.Clone(e.Journal.KubernetesLeases)
	defs := []deploymentplan.Definition{}
	live := []deploymentplan.Object{}
	for _, resource := range partial.PriorForward.Plan.Resources {
		actual := deploymentplan.Clone(resource.Desired)
		deploymentplan.Map(actual["metadata"])["uid"] = resource.UID
		live = append(live, actual)
		body := deploymentplan.Clone(resource.Desired)
		if strings.HasPrefix(resource.ID, "StatefulSet/") {
			environmentPKIPod(body)["imagePullSecrets"] = []any{deploymentplan.Object{"name": "ghcr-pull"}}
		}
		defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: body, Dependencies: resource.Dependencies})
	}
	inputs := partial.Original.Inputs
	inputs.BindingsSHA256 = strings.Repeat("b", 64)
	preview, err := deploymentplan.Compile(inputs, defs, live)
	if err != nil {
		t.Fatal(err)
	}
	a := createFoundationRecoveryArtifact{Plan: createFoundationRecoveryPlan{Schema: "rtk-cloud.create-managed-transports-partial-recovery/v1", ParentSHA: source.PlanSHA256, ParentJournalSHA: deploymentplan.Digest(source), Materials: map[string]string{"accepted-service-core-managed-partial-original-plan": partial.Original.SHA256(), "accepted-service-core-managed-partial-forward-plan": preview.SHA256()}}}
	review := deploymentplan.Digest(a.Plan)
	a.Qualification = deploymentplan.Qualification{Decision: "GO", PlanSHA256: review}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: review})
	}
	plan := *preview
	plan.BindingsSHA256 = strings.Repeat("c", 64)
	journal := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Accepted: true, AcceptanceStarted: true, Owners: map[string]string{}}
	for _, resource := range plan.Resources {
		journal.Completed = append(journal.Completed, resource.ID)
		journal.Owners[resource.ID] = resource.UID
	}
	intent, err := environmentCreateKubernetesStageIntent(&plan)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		t.Fatal(err)
	}
	pod := "33333333-3333-4333-8333-333333333333"
	revision := "openbao-original-managed-revision"
	initialized := mustProviderRecoveryJSON(map[string]any{"root_token": "original-root-token", "unseal_keys_b64": []string{"original-retained-share"}})
	unsealIntent := deploymentplan.Object{"pod_uid": pod, "controller_uid": journal.Owners["StatefulSet/"+plan.Stack+"-secrets/openbao"], "revision": revision, "original_init_sha256": environmentPreflightBytesSHA256(initialized), "unseal_sha256": environmentPreflightBytesSHA256([]byte("original-retained-share"))}
	unseal := environmentCreateJournalEntry{Key: "openbao-managed-unseal-" + pod, Phase: "openbao-unseal", State: "completed", CompletedAt: time.Now(), IntentSHA256: activeTransportJournalDigest(unsealIntent), Receipt: mustProviderRecoveryJSON(map[string]any{"pod_uid": pod, "revision": revision, "initialized": true, "sealed": false})}
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + review, State: "completed", CompletedAt: time.Now(), IntentSHA256: deploymentplan.Digest(map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": a.Plan.ParentJournalSHA}), Receipt: mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": review})}, environmentCreateJournalEntry{Key: "kubernetes/transport-managed-hosts-forward-" + review, State: "completed", CompletedAt: time.Now(), IntentSHA256: hash, Receipt: mustProviderRecoveryJSON(map[string]any{"plan_sha256": plan.SHA256(), "owners": journal.Owners})}, unseal)
	e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, deploymentplan.LeaseOwner{ResourceID: "Lease/" + plan.Stack + "-video-cloud/rtk-deployment-execution", UID: "genuine-third-child-lease", Holder: plan.SHA256() + ":native-owned"})
	write := func(path string, value any) {
		if err := store.write(path, mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	path := filepath.Join("deployment/create/openbao-recovery", review)
	write(filepath.Join(path, "qualification.json"), a)
	write(filepath.Join(path, "source-journal.json"), source)
	write(filepath.Join(path, "openbao-root-trust-plan.json"), preview)
	write(filepath.Join("deployment/rollback", plan.SHA256(), "plan.json"), &plan)
	write(filepath.Join("deployment/rollback", plan.SHA256(), "journal.json"), journal)
	if err := store.write("openbao/init.json", initialized, false); err != nil {
		t.Fatal(err)
	}
	partial.AcceptedForward = &createManagedTransportAcceptedForward{Review: review, Plan: &plan, Journal: journal, UnsealKey: unseal.Key, UnsealSHA: activeTransportJournalDigest(unseal)}
	return store, e, partial
}

func TestManagedTransportPartialAcceptedForwardRequiresExactAdmissionAcceptanceAndNativeUnseal(t *testing.T) {
	for _, mode := range []string{"original", "changed-admission", "changed-source-prefix", "changed-metadata", "wrong-unseal-intent", "wrong-unseal-pod", "unseal-still-sealed", "unseal-pending", "missing-unseal", "extra-stage", "extra-lease", "receipt-owner-changed"} {
		t.Run(mode, func(t *testing.T) {
			store, e, _ := managedTransportAcceptedForwardFixture(t)
			switch mode {
			case "changed-admission":
				e.Journal.Entries[len(e.Journal.Entries)-3].IntentSHA256 = strings.Repeat("f", 64)
			case "changed-source-prefix":
				e.Journal.Entries[0].IntentSHA256 = strings.Repeat("f", 64)
			case "changed-metadata":
				e.Journal.OriginalJournalSHA256 = strings.Repeat("f", 64)
			case "wrong-unseal-intent":
				e.Journal.Entries[len(e.Journal.Entries)-1].IntentSHA256 = strings.Repeat("f", 64)
			case "wrong-unseal-pod":
				e.Journal.Entries[len(e.Journal.Entries)-1].Receipt = mustProviderRecoveryJSON(map[string]any{"pod_uid": "44444444-4444-4444-8444-444444444444", "revision": "openbao-original-managed-revision", "initialized": true, "sealed": false})
			case "unseal-still-sealed":
				e.Journal.Entries[len(e.Journal.Entries)-1].Receipt = mustProviderRecoveryJSON(map[string]any{"pod_uid": "33333333-3333-4333-8333-333333333333", "revision": "openbao-original-managed-revision", "initialized": true, "sealed": true})
			case "unseal-pending":
				e.Journal.Entries[len(e.Journal.Entries)-1].State = "pending"
			case "missing-unseal":
				e.Journal.Entries = e.Journal.Entries[:len(e.Journal.Entries)-1]
			case "extra-stage":
				e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "unrelated", State: "pending", IntentSHA256: strings.Repeat("f", 64)})
			case "extra-lease":
				e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1])
			case "receipt-owner-changed":
				e.Journal.Entries[len(e.Journal.Entries)-2].Receipt = mustProviderRecoveryJSON(map[string]any{"plan_sha256": "invalid", "owners": map[string]string{}})
			}
			base, prior, accepted, err := loadManagedTransportAcceptedForward(store, e)
			if (err == nil) != (mode == "original") {
				t.Fatal("unqualified accepted or unsealed state accepted", mode, err)
			}
			if mode == "original" {
				if base == nil || !isManagedTransportBaseCheckpoint(base.Journal) || prior == nil || prior.Previous == nil || accepted == nil || !accepted.Journal.Accepted || !environmentPreflightDigest.MatchString(accepted.UnsealSHA) {
					t.Fatal("real accepted/unseal outcomes lost")
				}
				profile, flag := createRecoveryCheckpointProfile(e.Journal)
				if profile != "managed-transports-accepted-openbao-audit" || flag != "--after-application-enrollment-trust" {
					t.Fatal("wrong accepted observer selector", profile, flag)
				}
			}
		})
	}
}

func TestManagedTransportPartialAcceptedForwardRejectsInventedAcceptanceOrDroppedOwner(t *testing.T) {
	for _, mode := range []string{"original", "not-accepted", "acceptance-not-started", "in-flight", "failed", "missing-owner", "missing-completion", "wrong-owner", "old-host-create"} {
		t.Run(mode, func(t *testing.T) {
			_, _, partial := managedTransportAcceptedForwardFixture(t)
			plan, journal := partial.AcceptedForward.Plan, partial.AcceptedForward.Journal
			switch mode {
			case "not-accepted":
				journal.Accepted = false
			case "acceptance-not-started":
				journal.AcceptanceStarted = false
			case "in-flight":
				journal.InFlight = plan.Resources[0].ID
			case "failed":
				journal.Failed = plan.Resources[0].ID
			case "missing-owner":
				delete(journal.Owners, plan.Resources[0].ID)
			case "missing-completion":
				journal.Completed = journal.Completed[:6]
			case "wrong-owner":
				journal.Owners[plan.Resources[0].ID] = "replacement"
			case "old-host-create":
				plan.Resources[0].Action = "create"
				journal.PlanSHA256 = plan.SHA256()
			}
			if err := validateManagedTransportAcceptedForwardJournal(plan, journal); (err == nil) != (mode == "original") {
				t.Fatal("invented accepted child", mode, err)
			}
		})
	}
}

func TestManagedTransportPartialAcceptedAuditCandidateCannotReplayHostsOrChangeOriginalIdentity(t *testing.T) {
	for _, mode := range []string{"original", "wrong-audit-path", "unredacted-audit", "changed-mqtt", "changed-controller", "changed-openbao-template", "extra-object"} {
		t.Run(mode, func(t *testing.T) {
			_, _, partial := managedTransportAcceptedForwardFixture(t)
			defs := []deploymentplan.Definition{}
			live := []deploymentplan.Object{}
			for _, resource := range partial.AcceptedForward.Plan.Resources {
				actual := deploymentplan.Clone(resource.Desired)
				deploymentplan.Map(actual["metadata"])["uid"] = resource.UID
				live = append(live, actual)
				body := deploymentplan.Clone(resource.Desired)
				if strings.HasSuffix(resource.ID, "/openbao-file") {
					block := managedTransportOriginalAuditBlock
					if mode == "wrong-audit-path" {
						block = strings.ReplaceAll(block, "/openbao/audit/audit.jsonl", "/foreign/audit.log")
					}
					if mode == "unredacted-audit" {
						block = strings.ReplaceAll(block, "false", "true")
					}
					deploymentplan.Map(body["data"])["file.hcl"] = deploymentplan.String(deploymentplan.Map(body["data"])["file.hcl"]) + block
				}
				if mode == "changed-mqtt" && strings.HasSuffix(resource.ID, "/mqtt-pki") || mode == "changed-controller" && strings.HasSuffix(resource.ID, "/pki-controller") || mode == "changed-openbao-template" && strings.HasSuffix(resource.ID, "/openbao") {
					deploymentplan.Map(body["spec"])["replicas"] = float64(9)
				}
				defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: body, Dependencies: resource.Dependencies})
			}
			if mode == "extra-object" {
				defs = append(defs, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "foreign", "namespace": partial.Original.Stack + "-secrets"}}})
			}
			err := validateManagedTransportPartialForwardDefinitions(partial, defs)
			if (err == nil) != (mode == "original") {
				t.Fatal("accepted observer changed a host or original audit contract", mode, err)
			}
			if mode == "original" {
				candidate, err := deploymentplan.Compile(partial.Original.Inputs, defs, live)
				if err != nil {
					t.Fatal(err)
				}
				if err := validateManagedTransportPartialForwardActions(partial, candidate); err != nil {
					t.Fatal("valid audit-only candidate rejected", err)
				}
				for _, resource := range candidate.Resources {
					if strings.HasSuffix(resource.ID, "/openbao-file") {
						if resource.Action != "update" {
							t.Fatal("audit configuration not updated")
						}
					} else if resource.Action != "retain" {
						t.Fatal("accepted host replayed")
					}
				}
			}
		})
	}
}

func TestManagedTransportPartialAcceptedObserverRejectsShortJournalsWithoutPanic(t *testing.T) {
	for length := 0; length < 8; length++ {
		journal := environmentCreateJournal{Entries: make([]environmentCreateJournalEntry, length)}
		if managedTransportAcceptedForwardReview(journal) != "" {
			t.Fatal("short journal selected accepted observer", length)
		}
		if isManagedTransportPartialCheckpoint(journal) {
			t.Fatal("short journal selected partial recovery", length)
		}
	}
}

func TestManagedTransportPartialAcceptedFoundationRequiresExactOriginalWrittenAcceptedChain(t *testing.T) {
	for _, mode := range []string{"original", "wrong-owner", "wrong-original-baseline", "wrong-accepted-baseline", "wrong-accepted-owner"} {
		t.Run(mode, func(t *testing.T) {
			_, _, partial := managedTransportAcceptedForwardFixture(t)
			id := "StatefulSet/" + partial.Original.Stack + "-secrets/openbao"
			var written deploymentplan.Resource
			for _, resource := range partial.PriorForward.Plan.Resources {
				if resource.ID == id {
					written = resource
				}
			}
			owner, expected := written.UID, deploymentplan.Clone(written.Baseline)
			switch mode {
			case "wrong-owner":
				owner = "replacement"
			case "wrong-original-baseline":
				deploymentplan.Map(expected["spec"])["replicas"] = float64(10)
			case "wrong-accepted-baseline", "wrong-accepted-owner":
				for index := range partial.AcceptedForward.Plan.Resources {
					resource := &partial.AcceptedForward.Plan.Resources[index]
					if resource.ID == id {
						if mode == "wrong-accepted-baseline" {
							deploymentplan.Map(resource.Baseline["spec"])["replicas"] = float64(10)
						} else {
							resource.UID = "replacement"
						}
					}
				}
			}
			actual, changed, err := managedTransportPartialFoundationOverride(partial, id, owner, expected)
			if (err == nil) != (mode == "original") || changed != (mode == "original") {
				t.Fatal("unqualified accepted foundation override", mode, changed, err)
			}
			if mode == "original" {
				for _, resource := range partial.AcceptedForward.Plan.Resources {
					if resource.ID == id && deploymentplan.Digest(actual) != deploymentplan.Digest(resource.Desired) {
						t.Fatal("genuine accepted pull reference lost")
					}
				}
			}
		})
	}
}
