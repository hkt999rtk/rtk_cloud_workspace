package main

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedCorePartialFixture(t *testing.T) (*createManagedCorePartialRecovery, *deploymentplan.Plan) {
	t.Helper()
	session := "e0a93344-3c64-4a00-87d5-dc57a4e0d48b"
	ids := managedCorePartialResourceIDs("video-cloud-dev", session)
	definitions, live := []deploymentplan.Definition{}, []deploymentplan.Object{}
	for index, id := range ids {
		parts := strings.Split(id, "/")
		metadata := deploymentplan.Object{"name": parts[2], "labels": deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-dev", "app.kubernetes.io/managed-by": "rtk-cloud"}}
		if parts[1] != "" {
			metadata["namespace"] = parts[1]
		}
		object := deploymentplan.Object{"apiVersion": "v1", "kind": parts[0], "metadata": metadata, "spec": deploymentplan.Object{}}
		if index < 8 {
			object["data"] = deploymentplan.Object{"public": "current-original"}
		}
		phase := "configuration"
		if index >= 13 {
			phase = "workload"
			object["apiVersion"] = "apps/v1"
			image := "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-pki@sha256:" + strings.Repeat("a", 64)
			settings := map[string]string{"PKI_BOOTSTRAP_SESSION_ID": session, "PRIVATE_KEY_PATH": "/run/pki-state/private/client.key", "OTHER_ORIGINAL_SETTING": "unchanged"}
			if index == 13 {
				settings["PKI_FIXED_DEVICE_ROOT_TRUST"] = "false"
				settings["PKI_DEVICE_ROOT_ID"] = "11111111-1111-4111-8111-111111111111"
				settings["PKI_DEVICE_ROOT_SHA256"] = strings.Repeat("b", 64)
			}
			if index == 14 {
				settings["ENROLLMENT_ONLY"] = "false"
				settings["PKI_ENVIRONMENT"] = "dev"
			}
			object["spec"] = deploymentplan.Object{"selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app": parts[2]}}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app": parts[2]}}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": parts[2], "image": image, "env": environmentCreateEnv(settings), "volumeMounts": []any{deploymentplan.Object{"name": "state", "mountPath": "/run/pki-state"}}}}, "volumes": []any{deploymentplan.Object{"name": "state", "persistentVolumeClaim": deploymentplan.Object{"claimName": parts[2] + "-original-state"}}}}}}
			if index == 14 {
				mqttFoundationPrimary(object)["env"] = append(certIssuerObjectList(mqttFoundationPrimary(object)["env"]), deploymentplan.Object{"name": "CERT_ISSUER_DB_DSN", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "pki-issuer-database", "key": "url"}}})
			}
		}
		actual := deploymentplan.Clone(object)
		deploymentplan.Map(actual["metadata"])["uid"] = fmt.Sprintf("%08d-2222-4222-8222-222222222222", index+1)
		if index == 4 {
			deploymentplan.Map(actual["data"])["public"] = "previous-original"
		}
		if index == 13 {
			fields, _ := environmentPKINamedFields(mqttFoundationPrimary(actual)["env"])
			fields["PKI_FIXED_DEVICE_ROOT_TRUST"]["value"] = "true"
		}
		if index == 14 {
			fields, _ := environmentPKINamedFields(mqttFoundationPrimary(actual)["env"])
			fields["ENROLLMENT_ONLY"]["value"] = "true"
		}
		dependencies := []string(nil)
		if index == 14 {
			dependencies = []string{ids[13]}
		}
		if index == 15 {
			dependencies = []string{ids[13], ids[14]}
		}
		definitions = append(definitions, deploymentplan.Definition{Phase: phase, Object: object, Dependencies: dependencies})
		live = append(live, actual)
	}
	inputs := deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: strings.Repeat("c", 64), ReleaseSHA256: strings.Repeat("d", 64), BindingsSHA256: environmentCreateStageBindingsSHA("managed-complete-issuer", definitions)}
	plan, err := deploymentplan.Compile(inputs, definitions, live)
	if err != nil {
		t.Fatal(err)
	}
	journal := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Failed: ids[13], Owners: map[string]string{}}
	for index, resource := range plan.Resources {
		if index < 14 {
			journal.Owners[resource.ID] = resource.UID
		}
		if index < 13 {
			journal.Completed = append(journal.Completed, resource.ID)
		}
	}
	partial := &createManagedCorePartialRecovery{Action: managedCoreRestoreOnly, OriginalRepairReview: strings.Repeat("e", 64), SourceSHA: strings.Repeat("f", 64), ArtifactSHA: strings.Repeat("1", 64), Plan: plan, Journal: journal}
	if err := validateManagedCorePartialJournal(plan, journal); err != nil {
		t.Fatal(err)
	}
	defs, err := managedCorePartialDefinitions(partial)
	if err != nil {
		t.Fatal(err)
	}
	current := []deploymentplan.Object{}
	for index, resource := range plan.Resources {
		body := resource.Desired
		if index > 13 {
			body = resource.Baseline
		}
		actual := deploymentplan.Clone(body)
		deploymentplan.Map(actual["metadata"])["uid"] = resource.UID
		current = append(current, actual)
	}
	nextInputs := plan.Inputs
	nextInputs.BindingsSHA256 = environmentCreateStageBindingsSHA("managed-complete-issuer-forward", defs)
	candidate, err := deploymentplan.Compile(nextInputs, defs, current)
	if err != nil {
		t.Fatal(err)
	}
	return partial, candidate
}

func TestManagedCorePartialCandidateUsesActualPlannerActionsAndOnlyControllerFlagRepair(t *testing.T) {
	partial, candidate := managedCorePartialFixture(t)
	if err := validateManagedCorePartialCandidate(partial, candidate); err != nil {
		t.Fatal(err)
	}
	retained, updated := 0, 0
	for _, resource := range candidate.Resources {
		if resource.Action == "retain" {
			retained++
		}
		if resource.Action == "update" {
			updated++
		}
	}
	if retained != 14 || updated != 2 || candidate.Resources[15].Action != "retain" {
		t.Fatal("byte-identical AccountManager was replayed")
	}
	before, after := partial.Plan.Resources[13].Desired, deploymentplan.Clone(candidate.Resources[13].Desired)
	fields, _ := environmentPKINamedFields(mqttFoundationPrimary(after)["env"])
	if fields["PKI_FIXED_DEVICE_ROOT_TRUST"]["value"] != "true" {
		t.Fatal("fixed Root native setting was not repaired")
	}
	fields["PKI_FIXED_DEVICE_ROOT_TRUST"]["value"] = "false"
	if deploymentplan.Digest(before) != deploymentplan.Digest(after) {
		t.Fatal("controller repair changed unrelated original bytes")
	}
	for _, mode := range []string{"completed-update", "account-update", "replaced-owner", "old-controller-baseline", "untouched-baseline", "image", "key-path", "extra-env", "extra-resource", "dag", "release", "finalization-action"} {
		t.Run(mode, func(t *testing.T) {
			var changed deploymentplan.Plan
			if json.Unmarshal(mustProviderRecoveryJSON(candidate), &changed) != nil {
				t.Fatal("fixture clone failed")
			}
			switch mode {
			case "completed-update":
				changed.Resources[4].Action = "update"
			case "account-update":
				changed.Resources[15].Action = "update"
			case "replaced-owner":
				changed.Resources[13].UID = "99999999-9999-4999-8999-999999999999"
			case "old-controller-baseline":
				changed.Resources[13].Baseline = deploymentplan.Clone(partial.Plan.Resources[13].Baseline)
			case "untouched-baseline":
				changed.Resources[14].Baseline = deploymentplan.Clone(partial.Plan.Resources[14].Desired)
			case "image":
				mqttFoundationPrimary(changed.Resources[13].Desired)["image"] = "foreign@sha256:" + strings.Repeat("9", 64)
			case "key-path":
				fields, _ := environmentPKINamedFields(mqttFoundationPrimary(changed.Resources[13].Desired)["env"])
				fields["PRIVATE_KEY_PATH"]["value"] = "/new/key"
			case "extra-env":
				c := mqttFoundationPrimary(changed.Resources[13].Desired)
				c["env"] = append(certIssuerObjectList(c["env"]), deploymentplan.Object{"name": "UNQUALIFIED", "value": "true"})
			case "extra-resource":
				changed.Resources = append(changed.Resources, changed.Resources[0])
			case "dag":
				changed.Resources[13].Dependencies = []string{changed.Resources[14].ID}
			case "release":
				changed.ReleaseSHA256 = strings.Repeat("0", 64)
			case "finalization-action":
				altered := *partial
				altered.Action = managedCoreFinalizeComplete
				if validateManagedCorePartialCandidate(&altered, &changed) == nil {
					t.Fatal("core-only qualification was promoted to finalization")
				}
				return
			}
			if validateManagedCorePartialCandidate(partial, &changed) == nil {
				t.Fatal("unqualified candidate delta accepted", mode)
			}
		})
	}
}

func TestManagedCorePartialJournalRequiresActualWrittenBoundaryAndImmutableFailure(t *testing.T) {
	partial, _ := managedCorePartialFixture(t)
	for _, mode := range []string{"original", "blank-failed", "foreign-failed", "in-flight", "accepted", "acceptance-started", "missing-completion", "changed-order", "missing-written-owner", "unwritten-owner", "forced-account-update", "wrong-session", "changed-original-fixed-flag"} {
		t.Run(mode, func(t *testing.T) {
			var changed createManagedCorePartialRecovery
			if json.Unmarshal(mustProviderRecoveryJSON(partial), &changed) != nil {
				t.Fatal("fixture clone failed")
			}
			switch mode {
			case "blank-failed":
				changed.Journal.Failed = ""
			case "foreign-failed":
				changed.Journal.Failed = changed.Plan.Resources[14].ID
			case "in-flight":
				changed.Journal.InFlight = changed.Journal.Failed
			case "accepted":
				changed.Journal.Accepted = true
			case "acceptance-started":
				changed.Journal.AcceptanceStarted = true
			case "missing-completion":
				changed.Journal.Completed = changed.Journal.Completed[:12]
			case "changed-order":
				slices.Reverse(changed.Journal.Completed)
			case "missing-written-owner":
				delete(changed.Journal.Owners, changed.Plan.Resources[13].ID)
			case "unwritten-owner":
				changed.Journal.Owners[changed.Plan.Resources[14].ID] = changed.Plan.Resources[14].UID
			case "forced-account-update":
				changed.Plan.Resources[15].Action = "update"
			case "wrong-session":
				fields, _ := environmentPKINamedFields(mqttFoundationPrimary(changed.Plan.Resources[13].Desired)["env"])
				fields["PKI_BOOTSTRAP_SESSION_ID"]["value"] = "foreign"
			case "changed-original-fixed-flag":
				fields, _ := environmentPKINamedFields(mqttFoundationPrimary(changed.Plan.Resources[13].Desired)["env"])
				fields["PKI_FIXED_DEVICE_ROOT_TRUST"]["value"] = "true"
			}
			changed.Journal.PlanSHA256 = changed.Plan.SHA256()
			if (validateManagedCorePartialJournal(changed.Plan, changed.Journal) == nil) != (mode == "original") {
				t.Fatal("unqualified original core journal accepted", mode)
			}
		})
	}
}

func TestManagedCorePartialSourceAllowsOnlyFourGenuineAliasClosures(t *testing.T) {
	now := time.Now().UTC()
	session, review := "e0a93344-3c64-4a00-87d5-dc57a4e0d48b", strings.Repeat("9", 64)
	source := environmentCreateJournal{Environment: "dev", Stack: "video-cloud-dev", OwnerToken: "original", Entries: []environmentCreateJournalEntry{{Key: "original-completed", Phase: "bootstrap", State: "completed", StartedAt: now.Add(-time.Minute), CompletedAt: now, IntentSHA256: strings.Repeat("a", 64), Receipt: mustProviderRecoveryJSON(deploymentplan.Object{"original": "public"})}}, KubernetesLeases: []deploymentplan.LeaseOwner{{ResourceID: "Lease/video-cloud-dev-video-cloud/rtk-deployment-execution", UID: "11111111-1111-4111-8111-111111111111", Holder: "original-owned"}}}
	failed := map[string]string{}
	for index := 0; index < 4; index++ {
		key := "kubernetes/managed-application-owners"
		if index > 0 {
			key += "-forward-" + strings.Repeat(fmt.Sprint(index), 64)
		}
		failed[key] = strings.Repeat(fmt.Sprint(index+4), 64)
		source.Entries = append(source.Entries, environmentCreateJournalEntry{Key: key, Phase: strings.TrimPrefix(key, "kubernetes/"), State: "pending", StartedAt: now, IntentSHA256: strings.Repeat("b", 64)})
	}
	current := source
	current.Entries = slices.Clone(source.Entries)
	current.KubernetesLeases = slices.Clone(source.KubernetesLeases)
	for index := 1; index < len(current.Entries); index++ {
		entry := &current.Entries[index]
		entry.State = "completed"
		entry.CompletedAt = now.Add(time.Second)
		entry.Receipt = mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/managed-application-owners-forward-" + review, "original_failed_plan_sha256": failed[entry.Key], "retained_session_id": session})
	}
	for index := 0; index < 7; index++ {
		current.Entries = append(current.Entries, environmentCreateJournalEntry{Key: fmt.Sprintf("declared-suffix-%d", index)})
	}
	for index := 0; index < 4; index++ {
		current.KubernetesLeases = append(current.KubernetesLeases, deploymentplan.LeaseOwner{UID: fmt.Sprint(index)})
	}
	for _, mode := range []string{"original", "completed-prefix", "alias-stage", "alias-plan", "alias-session", "alias-intent", "alias-started", "still-pending", "extra-alias-field", "foreign-old-pending", "owner", "old-lease", "extra-entry", "extra-lease"} {
		t.Run(mode, func(t *testing.T) {
			var changed environmentCreateJournal
			if json.Unmarshal(mustProviderRecoveryJSON(current), &changed) != nil {
				t.Fatal("clone failed")
			}
			switch mode {
			case "completed-prefix":
				changed.Entries[0].Receipt = mustProviderRecoveryJSON(map[string]string{"replacement": "public"})
			case "alias-stage", "alias-plan", "alias-session", "extra-alias-field":
				var receipt map[string]string
				json.Unmarshal(changed.Entries[1].Receipt, &receipt)
				if mode == "alias-stage" {
					receipt["actual_qualified_forward_stage"] = "foreign"
				}
				if mode == "alias-plan" {
					receipt["original_failed_plan_sha256"] = strings.Repeat("0", 64)
				}
				if mode == "alias-session" {
					receipt["retained_session_id"] = "foreign"
				}
				if mode == "extra-alias-field" {
					receipt["unqualified"] = "true"
				}
				changed.Entries[1].Receipt = mustProviderRecoveryJSON(receipt)
			case "alias-intent":
				changed.Entries[1].IntentSHA256 = strings.Repeat("0", 64)
			case "alias-started":
				changed.Entries[1].StartedAt = now.Add(time.Second)
			case "still-pending":
				changed.Entries[1].State = "pending"
			case "foreign-old-pending":
				changed.Entries[1].Key = "foreign"
			case "owner":
				changed.OwnerToken = "replacement"
			case "old-lease":
				changed.KubernetesLeases[0].UID = "replacement"
			case "extra-entry":
				changed.Entries = append(changed.Entries, changed.Entries[len(changed.Entries)-1])
			case "extra-lease":
				changed.KubernetesLeases = append(changed.KubernetesLeases, changed.KubernetesLeases[len(changed.KubernetesLeases)-1])
			}
			if managedCorePartialSourceMembership(source, changed, failed, session, review) != (mode == "original") {
				t.Fatal("unauthenticated original source change accepted", mode)
			}
		})
	}
}

func TestManagedCorePartialSelectorRejectsUnqualifiedTailAndNeverSelectsAcceptedForward(t *testing.T) {
	now, review := time.Now().UTC(), strings.Repeat("c", 64)
	journal := environmentCreateJournal{}
	for index := 0; index < 8; index++ {
		journal.Entries = append(journal.Entries, environmentCreateJournalEntry{Key: fmt.Sprint(index), Phase: "bootstrap", State: "completed", CompletedAt: now, Receipt: mustProviderRecoveryJSON(map[string]string{"original": "public"}), IntentSHA256: strings.Repeat("d", 64)})
	}
	stages := []string{"recovery", "openbao-host-ack-forward-" + review, "openbao-unseal", "managed-application-owners-forward-" + review, "native-app-routing-profile", "bootstrap", "managed-complete-issuer"}
	keys := []string{"openbao-recovery-admission-" + review, "kubernetes/" + stages[1], "openbao-managed-unseal-11111111-1111-4111-8111-111111111111", "kubernetes/" + stages[3], "kubernetes/" + stages[4], "native-app-routing-profile-proof", "kubernetes/managed-complete-issuer"}
	for index, key := range keys {
		entry := environmentCreateJournalEntry{Key: key, Phase: stages[index], State: "completed", CompletedAt: now, Receipt: mustProviderRecoveryJSON(map[string]string{"actual": "public"}), IntentSHA256: strings.Repeat("e", 64)}
		if index == 6 {
			entry.State = "pending"
			entry.CompletedAt = time.Time{}
			entry.Receipt = nil
		}
		journal.Entries = append(journal.Entries, entry)
	}
	for _, mode := range []string{"original", "empty", "short", "unrelated-pending", "wrong-phase", "bad-unseal-uid", "missing-route", "unknown-tail", "accepted-forward", "core-receipt", "core-completed"} {
		t.Run(mode, func(t *testing.T) {
			var changed environmentCreateJournal
			json.Unmarshal(mustProviderRecoveryJSON(journal), &changed)
			switch mode {
			case "empty":
				changed.Entries = nil
			case "short":
				changed.Entries = changed.Entries[12:]
			case "unrelated-pending":
				changed.Entries[0].State = "pending"
			case "wrong-phase":
				changed.Entries[len(changed.Entries)-7].Phase = "foreign"
			case "bad-unseal-uid":
				changed.Entries[len(changed.Entries)-5].Key = "openbao-managed-unseal-foreign"
			case "missing-route":
				changed.Entries[len(changed.Entries)-3].Key = "foreign"
			case "unknown-tail":
				changed.Entries = append(changed.Entries, environmentCreateJournalEntry{Key: "foreign"})
			case "accepted-forward":
				changed.Entries[len(changed.Entries)-1].Key = "kubernetes/managed-complete-issuer-forward-" + review
			case "core-receipt":
				changed.Entries[len(changed.Entries)-1].Receipt = mustProviderRecoveryJSON(map[string]string{"fabricated": "complete"})
			case "core-completed":
				changed.Entries[len(changed.Entries)-1].State = "completed"
			}
			if (managedCorePartialReview(changed) == review) != (mode == "original") {
				t.Fatal("unqualified late source selected", mode)
			}
		})
	}
}
