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

func managedApplicationThirdInterruptedFixture(t *testing.T) (secretStore, *environmentCreateExecution, *createManagedApplicationPartialRecovery, *createManagedApplicationInterruptedForward) {
	t.Helper()
	store, e, original, previous := managedApplicationLatestInterruptedFixture(t)
	previous.LeaseSHA = deploymentplan.Digest(deploymentplan.Object(nil))
	source := e.Journal
	source.Entries = slices.Clone(e.Journal.Entries)
	source.KubernetesLeases = slices.Clone(e.Journal.KubernetesLeases)
	defs, err := managedApplicationPartialDefinitionsWithRepairs(original, true, true)
	if err != nil {
		t.Fatal(err)
	}
	live := []deploymentplan.Object{}
	for index, resource := range previous.Plan.Resources {
		body, uid := resource.Desired, previous.Journal.Owners[resource.ID]
		if index > 23 {
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
	a := createFoundationRecoveryArtifact{Plan: createFoundationRecoveryPlan{Schema: "rtk-cloud.create-managed-application-app-role-recovery/v1", ParentSHA: source.PlanSHA256, ParentJournalSHA: deploymentplan.Digest(source), Materials: map[string]string{
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
	journal := previous.Journal
	journal.PlanSHA256 = actual.SHA256()
	journal.Owners = map[string]string{}
	journal.Completed = slices.Clone(previous.Journal.Completed)
	for id, uid := range previous.Journal.Owners {
		journal.Owners[id] = uid
	}
	forward := &createManagedApplicationInterruptedForward{Previous: previous, Review: review, SourceSHA: deploymentplan.Digest(source), ArtifactSHA: deploymentplan.Digest(a), Plan: &actual, Journal: journal}
	intent, err := environmentCreateKubernetesStageIntent(&actual)
	if err != nil {
		t.Fatal(err)
	}
	hash, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		t.Fatal(err)
	}
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + review, Phase: "recovery", State: "completed", CompletedAt: time.Now(), IntentSHA256: deploymentplan.Digest(map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": a.Plan.ParentJournalSHA}), Receipt: mustProviderRecoveryJSON(map[string]string{"qualified_recovery_sha256": review})}, environmentCreateJournalEntry{Key: "kubernetes/" + stage, Phase: stage, State: "pending", StartedAt: time.Now(), IntentSHA256: hash})
	e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, deploymentplan.LeaseOwner{ResourceID: "Lease/" + source.Stack + "-video-cloud/rtk-deployment-execution", UID: "77777777-7777-4777-8777-777777777777", Holder: actual.SHA256() + ":owned"})
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

func TestManagedApplicationThirdInterruptedRequiresExactAppRoleAdmissionAndObservedLeaseAbsence(t *testing.T) {
	for _, mode := range []string{"original", "old-prefix", "admission", "intent", "invented-release", "extra-lease", "changed-old-alias"} {
		t.Run(mode, func(t *testing.T) {
			store, e, partial, expected := managedApplicationThirdInterruptedFixture(t)
			count := len(e.Journal.Entries)
			switch mode {
			case "old-prefix":
				e.Journal.Entries[0].IntentSHA256 = strings.Repeat("0", 64)
			case "admission":
				e.Journal.Entries[count-2].IntentSHA256 = strings.Repeat("0", 64)
			case "intent":
				e.Journal.Entries[count-1].IntentSHA256 = strings.Repeat("0", 64)
			case "invented-release":
				release := e.Journal.Entries[count-4]
				release.Key = "managed-application-interrupted-lease-release-" + expected.Review
				e.Journal.Entries = append(e.Journal.Entries[:count-1], release, e.Journal.Entries[count-1])
			case "extra-lease":
				e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1])
			case "changed-old-alias":
				e.Journal.Entries[count-3].State = "completed"
			}
			source, _, loaded, err := loadManagedApplicationInterruptedSource(store, e)
			if (err == nil) != (mode == "original") {
				t.Fatal("unqualified third source accepted", mode, err)
			}
			if mode == "original" {
				if loaded.Previous == nil || loaded.Previous.Previous == nil || loaded.Previous.Previous.Previous != nil || loaded.Previous.LeaseSHA != deploymentplan.Digest(deploymentplan.Object(nil)) || loaded.Plan.Resources[22].Action != "retain" || len(source.Journal.Entries) != count-2 {
					t.Fatal("exact admitted history/progress/absence lost")
				}
				partial.Interrupted = loaded
				if _, _, err := managedApplicationPartialCurrentPlan(partial); err != nil {
					t.Fatal(err)
				}
				keys := []string{"kubernetes/managed-application-owners", "kubernetes/managed-application-owners-forward-" + loaded.Review, "kubernetes/managed-application-owners-forward-" + loaded.Previous.Review, "kubernetes/managed-application-owners-forward-" + loaded.Previous.Previous.Review}
				for _, key := range keys {
					if !managedApplicationPendingForwardAlias(e.Journal, key) {
						t.Fatal("genuine pending alias lost", key)
					}
				}
				if managedApplicationPendingForwardAlias(e.Journal, "kubernetes/managed-application-owners-forward-foreign") {
					t.Fatal("foreign alias allowed")
				}
			}
		})
	}
}

func TestManagedApplicationThirdInterruptedRejectsScopeRegressionAndFutureIncomingProjection(t *testing.T) {
	_, _, partial, forward := managedApplicationThirdInterruptedFixture(t)
	for _, mode := range []string{"original", "replay-main-api", "missing-completion", "blank-failed", "wrong-failed", "unwritten-owner", "missing-previous", "fourth-previous", "private-path", "future-incoming"} {
		t.Run(mode, func(t *testing.T) {
			var changed createManagedApplicationInterruptedForward
			if json.Unmarshal(mustProviderRecoveryJSON(forward), &changed) != nil {
				t.Fatal("fixture clone failed")
			}
			switch mode {
			case "replay-main-api":
				changed.Plan.Resources[22].Action = "update"
			case "missing-completion":
				changed.Journal.Completed = changed.Journal.Completed[:22]
			case "blank-failed":
				changed.Journal.Failed = ""
			case "wrong-failed":
				changed.Journal.Failed = changed.Plan.Resources[24].ID
			case "unwritten-owner":
				changed.Journal.Owners[changed.Plan.Resources[24].ID] = changed.Plan.Resources[24].UID
			case "missing-previous":
				changed.Previous = nil
			case "fourth-previous":
				changed.Previous.Previous.Previous = forward.Previous.Previous
			case "private-path":
				environmentPKIPod(changed.Plan.Resources[23].Desired)["volumes"] = []any{deploymentplan.Object{"name": "pki-state", "persistentVolumeClaim": deploymentplan.Object{"claimName": "foreign"}}}
			case "future-incoming":
				for _, raw := range certIssuerObjectList(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(changed.Plan.Resources[23].Desired)["containers"])[0])["env"]) {
					field := deploymentplan.Map(raw)
					if field["name"] == "VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST" {
						field["value"] = "/run/pki-app-manifest/bundles.json"
					}
				}
			}
			changed.Journal.PlanSHA256 = changed.Plan.SHA256()
			if (validateManagedApplicationInterruptedForward(partial, &changed) == nil) != (mode == "original") {
				t.Fatal("unqualified third source recipe/progress accepted", mode)
			}
		})
	}
}
