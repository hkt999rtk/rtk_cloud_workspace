package main

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedApplicationLatestInterruptedFixture(t *testing.T) (secretStore, *environmentCreateExecution, *createManagedApplicationPartialRecovery, *createManagedApplicationInterruptedForward) {
	t.Helper()
	store, e, original, previous := managedApplicationInterruptedFixture(t)
	original.Interrupted = previous
	previous.LeaseSHA = strings.Repeat("e", 64)
	e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1].UID = "99999999-9999-4999-8999-999999999999"
	source := e.Journal
	source.Entries = slices.Clone(e.Journal.Entries)
	source.KubernetesLeases = slices.Clone(e.Journal.KubernetesLeases)
	defs, err := managedApplicationPartialDefinitionsWithServiceCRL(original, true)
	if err != nil {
		t.Fatal(err)
	}
	live := []deploymentplan.Object{}
	for index, resource := range previous.Plan.Resources {
		body, uid := resource.Desired, previous.Journal.Owners[resource.ID]
		if index > 22 {
			body, uid = resource.Baseline, resource.UID
		}
		actual := deploymentplan.Clone(body)
		deploymentplan.Map(actual["metadata"])["uid"] = uid
		live = append(live, actual)
	}
	inputs := previous.Plan.Inputs
	inputs.BindingsSHA256 = environmentCreateStageBindingsSHA("managed-application-owners-forward", defs)
	preview, err := deploymentplan.Compile(inputs, defs, live)
	if err != nil {
		t.Fatal(err)
	}
	a := createFoundationRecoveryArtifact{Plan: createFoundationRecoveryPlan{Schema: "rtk-cloud.create-managed-application-interrupted-recovery/v1", ParentSHA: source.PlanSHA256, ParentJournalSHA: deploymentplan.Digest(source), Materials: map[string]string{
		"accepted-service-core-managed-application-plan": original.Plan.SHA256(), "accepted-service-core-managed-application-journal": deploymentplan.Digest(original.Journal), "accepted-service-core-managed-application-source": original.SourceSHA, "accepted-service-core-managed-application-admission": original.ArtifactSHA,
		"accepted-service-core-managed-application-interrupted-plan": previous.Plan.SHA256(), "accepted-service-core-managed-application-interrupted-journal": deploymentplan.Digest(previous.Journal), "accepted-service-core-managed-application-interrupted-source": previous.SourceSHA, "accepted-service-core-managed-application-interrupted-admission": previous.ArtifactSHA,
		managedApplicationInterruptedLeaseMaterial: previous.LeaseSHA, "accepted-service-core-managed-application-forward-plan": preview.SHA256()}}}
	review := deploymentplan.Digest(a.Plan)
	a.Qualification = deploymentplan.Qualification{Decision: "GO", PlanSHA256: review}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: review})
	}
	stage := "managed-application-owners-forward-" + review
	actual := *preview
	actual.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, defs)
	journal := deploymentplan.Journal{PlanSHA256: actual.SHA256(), Owners: map[string]string{}, Completed: append(slices.Clone(previous.Journal.Completed), actual.Resources[22].ID), Failed: actual.Resources[23].ID}
	for id, uid := range previous.Journal.Owners {
		journal.Owners[id] = uid
	}
	journal.Owners[actual.Resources[23].ID] = actual.Resources[23].UID
	forward := &createManagedApplicationInterruptedForward{Previous: previous, Review: review, SourceSHA: deploymentplan.Digest(source), ArtifactSHA: deploymentplan.Digest(a), Plan: &actual, Journal: journal}
	owner := source.KubernetesLeases[len(source.KubernetesLeases)-1]
	releaseIntent := managedApplicationInterruptedLeaseIntent{Review: review, SourceReview: previous.Review, SourceSHA: previous.SourceSHA, PlanSHA: previous.Plan.SHA256(), Lease: owner, LeaseSHA: previous.LeaseSHA}
	releaseReceipt := managedApplicationInterruptedLeaseReceipt{Lease: owner, LeaseSHA: previous.LeaseSHA, Absent: true}
	intent, err := environmentCreateKubernetesStageIntent(&actual)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		t.Fatal(err)
	}
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + review, Phase: "recovery", State: "completed", CompletedAt: time.Now(), IntentSHA256: deploymentplan.Digest(map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": a.Plan.ParentJournalSHA}), Receipt: mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": review})}, environmentCreateJournalEntry{Key: "managed-application-interrupted-lease-release-" + review, Phase: "managed-application-interrupted-lease-release", State: "completed", CompletedAt: time.Now(), IntentSHA256: activeTransportJournalDigest(releaseIntent), Receipt: mustProviderRecoveryJSON(releaseReceipt)}, environmentCreateJournalEntry{Key: "kubernetes/" + stage, Phase: stage, State: "pending", StartedAt: time.Now(), IntentSHA256: hash})
	e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, deploymentplan.LeaseOwner{ResourceID: owner.ResourceID, UID: "88888888-8888-4888-8888-888888888888", Holder: actual.SHA256() + ":owned"})
	write := func(path string, value any) {
		if err := store.write(path, mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	dir := filepath.Join("deployment/create/openbao-recovery", review)
	write(filepath.Join(dir, "qualification.json"), a)
	write(filepath.Join(dir, "source-journal.json"), source)
	write(filepath.Join(dir, "openbao-root-trust-plan.json"), preview)
	write(filepath.Join("deployment/rollback", actual.SHA256(), "plan.json"), actual)
	write(filepath.Join("deployment/rollback", actual.SHA256(), "journal.json"), journal)
	original.Interrupted = forward
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", store.ConfigRoot)
	return store, e, original, forward
}

func TestManagedApplicationLatestInterruptedAuthenticatesBothOriginalForwardsAndRecordedLeaseRelease(t *testing.T) {
	for _, mode := range []string{"original", "old-source", "latest-admission", "release-intent", "release-uid", "release-native-hash", "release-not-absent", "release-missing", "added-lease", "last-intent", "old-alias-changed"} {
		t.Run(mode, func(t *testing.T) {
			store, e, original, expected := managedApplicationLatestInterruptedFixture(t)
			count := len(e.Journal.Entries)
			switch mode {
			case "old-source":
				e.Journal.Entries[0].IntentSHA256 = strings.Repeat("0", 64)
			case "latest-admission":
				e.Journal.Entries[count-3].IntentSHA256 = strings.Repeat("0", 64)
			case "release-intent":
				e.Journal.Entries[count-2].IntentSHA256 = strings.Repeat("0", 64)
			case "release-uid", "release-native-hash", "release-not-absent":
				var receipt managedApplicationInterruptedLeaseReceipt
				if json.Unmarshal(e.Journal.Entries[count-2].Receipt, &receipt) != nil {
					t.Fatal("fixture receipt missing")
				}
				if mode == "release-uid" {
					receipt.Lease.UID = "77777777-7777-4777-8777-777777777777"
				} else if mode == "release-native-hash" {
					receipt.LeaseSHA = strings.Repeat("0", 64)
				} else {
					receipt.Absent = false
				}
				e.Journal.Entries[count-2].Receipt = mustProviderRecoveryJSON(receipt)
			case "release-missing":
				e.Journal.Entries = append(e.Journal.Entries[:count-2], e.Journal.Entries[count-1])
			case "added-lease":
				e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1])
			case "last-intent":
				e.Journal.Entries[count-1].IntentSHA256 = strings.Repeat("0", 64)
			case "old-alias-changed":
				e.Journal.Entries[count-4].State = "completed"
			}
			source, _, loaded, err := loadManagedApplicationInterruptedSource(store, e)
			if (err == nil) != (mode == "original") {
				t.Fatal("unqualified bounded continuation accepted", mode, err)
			}
			if mode == "original" {
				if loaded.Previous == nil || loaded.Previous.LeaseSHA != expected.Previous.LeaseSHA || len(loaded.Journal.Completed) != 23 || deploymentplan.Digest(source.Journal) != expected.SourceSHA {
					t.Fatal("genuine main API progress or previous lease history lost")
				}
				original.Interrupted = loaded
				plan, j, err := managedApplicationPartialCurrentPlan(original)
				if err != nil || plan.SHA256() != loaded.Plan.SHA256() || j.Failed != plan.Resources[23].ID {
					t.Fatal("latest current owner boundary not selected", err)
				}
			}
		})
	}
}

func TestManagedApplicationLatestInterruptedRejectsChangedProgressAndFutureAppFlagInHistoricalSource(t *testing.T) {
	_, _, original, forward := managedApplicationLatestInterruptedFixture(t)
	for _, mode := range []string{"original", "changed-main-api-owner", "missing-completion", "extra-completion", "blank-failed", "wrong-failed", "unwritten-owner", "missing-previous", "too-many-previous", "future-app-flag", "private-claim", "prior-failed-fabricated"} {
		t.Run(mode, func(t *testing.T) {
			var changed createManagedApplicationInterruptedForward
			if json.Unmarshal(mustProviderRecoveryJSON(forward), &changed) != nil {
				t.Fatal("fixture clone failed")
			}
			switch mode {
			case "changed-main-api-owner":
				changed.Journal.Owners[changed.Plan.Resources[22].ID] = "77777777-7777-4777-8777-777777777777"
			case "missing-completion":
				changed.Journal.Completed = changed.Journal.Completed[:22]
			case "extra-completion":
				changed.Journal.Completed = append(changed.Journal.Completed, changed.Plan.Resources[23].ID)
			case "blank-failed":
				changed.Journal.Failed = ""
			case "wrong-failed":
				changed.Journal.Failed = changed.Plan.Resources[24].ID
			case "unwritten-owner":
				changed.Journal.Owners[changed.Plan.Resources[24].ID] = changed.Plan.Resources[24].UID
			case "missing-previous":
				changed.Previous = nil
			case "too-many-previous":
				changed.Previous.Previous = forward.Previous
			case "future-app-flag":
				for _, raw := range certIssuerObjectList(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(changed.Plan.Resources[23].Desired)["containers"])[0])["env"]) {
					setting := deploymentplan.Map(raw)
					if setting["name"] == "VIDEO_CLOUD_AUTH_PRODUCT_PKI_REQUIRE_CRLS" {
						setting["value"] = "false"
					}
				}
			case "private-claim":
				environmentPKIPod(changed.Plan.Resources[23].Desired)["volumes"] = []any{deploymentplan.Object{"name": "pki-state", "persistentVolumeClaim": deploymentplan.Object{"claimName": "foreign"}}}
			case "prior-failed-fabricated":
				changed.Previous.Journal.Failed = changed.Previous.Plan.Resources[22].ID
			}
			changed.Journal.PlanSHA256 = changed.Plan.SHA256()
			if (validateManagedApplicationInterruptedForward(original, &changed) == nil) != (mode == "original") {
				t.Fatal("unqualified latest progress/historical recipe accepted", mode)
			}
		})
	}
}
