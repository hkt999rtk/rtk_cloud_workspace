package main

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedCoreInterruptedFixture(t *testing.T) *createManagedCorePartialRecovery {
	t.Helper()
	core, candidate := managedCorePartialFixture(t)
	_, _, _, prior := managedCorePendingArtifactFixture(t)
	core.PriorAdmission = prior
	journal := deploymentplan.Journal{PlanSHA256: candidate.SHA256(), Failed: candidate.Resources[14].ID, Owners: map[string]string{}}
	for index, resource := range candidate.Resources {
		if index < 15 {
			journal.Owners[resource.ID] = resource.UID
		}
		if index < 14 {
			journal.Completed = append(journal.Completed, resource.ID)
		}
	}
	core.PriorForward = &createManagedCoreInterruptedForward{Review: strings.Repeat("8", 64), SourceSHA: prior.SourceSHA, ArtifactSHA: strings.Repeat("7", 64), Plan: candidate, Journal: journal}
	return core
}

func TestManagedCoreInterruptedCurrentPlanPreservesActualCompletedControllerAndFailedCertIssuer(t *testing.T) {
	original := managedCoreInterruptedFixture(t)
	before := deploymentplan.Digest(original.Plan)
	plan, j, err := managedCorePartialCurrentPlan(original)
	if err != nil || plan.SHA256() != original.PriorForward.Plan.SHA256() || len(j.Completed) != 14 || len(j.Owners) != 15 || mqttFoundationEnv(mqttFoundationPrimary(plan.Resources[13].Desired), "PKI_FIXED_DEVICE_ROOT_TRUST") != "true" || deploymentplan.Digest(original.Plan) != before {
		t.Fatal("genuine14/15 source did not retain original failed13 authority", err)
	}
	for _, mode := range []string{"accepted", "acceptance-started", "inflight", "wrong-failure", "missing-controller", "wrong-order", "missing-written", "untouched-owner", "new-owner", "old-controller", "extra-image", "extra-env", "extra-private-path", "dag", "forced-account-write", "original-journal-changed", "review", "source", "artifact", "missing-prior"} {
		t.Run(mode, func(t *testing.T) {
			var core createManagedCorePartialRecovery
			json.Unmarshal(mustProviderRecoveryJSON(original), &core)
			latest := core.PriorForward
			switch mode {
			case "accepted":
				latest.Journal.Accepted = true
			case "acceptance-started":
				latest.Journal.AcceptanceStarted = true
			case "inflight":
				latest.Journal.InFlight = latest.Plan.Resources[14].ID
			case "wrong-failure":
				latest.Journal.Failed = latest.Plan.Resources[13].ID
			case "missing-controller":
				latest.Journal.Completed = latest.Journal.Completed[:13]
			case "wrong-order":
				slices.Reverse(latest.Journal.Completed)
			case "missing-written":
				delete(latest.Journal.Owners, latest.Plan.Resources[14].ID)
			case "untouched-owner":
				latest.Journal.Owners[latest.Plan.Resources[15].ID] = latest.Plan.Resources[15].UID
			case "new-owner":
				latest.Journal.Owners[latest.Plan.Resources[13].ID] = "22222222-2222-4222-8222-222222222222"
			case "old-controller":
				fields, _ := environmentPKINamedFields(mqttFoundationPrimary(latest.Plan.Resources[13].Desired)["env"])
				fields["PKI_FIXED_DEVICE_ROOT_TRUST"]["value"] = "false"
			case "extra-image":
				mqttFoundationPrimary(latest.Plan.Resources[14].Desired)["image"] = "unqualified-image"
			case "extra-env":
				c := mqttFoundationPrimary(latest.Plan.Resources[14].Desired)
				c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"UNQUALIFIED": "true"}))
			case "extra-private-path":
				fields, _ := environmentPKINamedFields(mqttFoundationPrimary(latest.Plan.Resources[14].Desired)["env"])
				fields["PRIVATE_KEY_PATH"]["value"] = "/replacement/key"
			case "dag":
				latest.Plan.Resources[14].Dependencies = nil
			case "forced-account-write":
				latest.Plan.Resources[15].Action = "update"
			case "original-journal-changed":
				core.Journal.Accepted = true
			case "review":
				latest.Review = core.OriginalRepairReview
			case "source":
				latest.SourceSHA = strings.Repeat("0", 64)
			case "artifact":
				latest.ArtifactSHA = ""
			case "missing-prior":
				core.PriorAdmission = nil
			}
			latest.Journal.PlanSHA256 = latest.Plan.SHA256()
			if _, _, err := managedCorePartialCurrentPlan(&core); err == nil {
				t.Fatal("unqualified interrupted source accepted", mode)
			}
		})
	}
}

func managedCoreInterruptedJournalFixture(t *testing.T) (environmentCreateJournal, environmentCreateJournal, *createManagedCorePendingAdmission) {
	t.Helper()
	_, source, _, prior := managedCorePendingArtifactFixture(t)
	current := source
	current.Entries = slices.Clone(source.Entries)
	current.KubernetesLeases = slices.Clone(source.KubernetesLeases)
	old := &current.Entries[len(current.Entries)-1]
	old.State = "completed"
	old.CompletedAt = old.StartedAt.Add(time.Hour)
	old.Receipt = mustProviderRecoveryJSON(map[string]string{"audit_sha256": prior.AuditSHA, "qualified_recovery_sha256": prior.Review})
	review := strings.Repeat("8", 64)
	at := old.CompletedAt.Add(time.Minute)
	current.Entries = append(current.Entries, environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + review, Phase: "recovery", State: "completed", StartedAt: at, CompletedAt: at.Add(time.Second), IntentSHA256: strings.Repeat("9", 64), Receipt: mustProviderRecoveryJSON(map[string]string{"audit_sha256": prior.AuditSHA, "qualified_recovery_sha256": review})}, environmentCreateJournalEntry{Key: "kubernetes/managed-complete-issuer-forward-" + review, Phase: "managed-complete-issuer-forward-" + review, State: "pending", StartedAt: at.Add(2 * time.Second), IntentSHA256: strings.Repeat("6", 64)})
	current.KubernetesLeases = append(current.KubernetesLeases, deploymentplan.LeaseOwner{ResourceID: "Lease/video-cloud-dev-video-cloud/rtk-deployment-execution", UID: "33333333-3333-4333-8333-333333333333", Holder: strings.Repeat("6", 64) + ":actual-child"})
	return source, current, prior
}

func TestManagedCoreInterruptedSelectorAndSourceMembershipAreBoundedToActualTwoEntryOneLeaseSuffix(t *testing.T) {
	source, current, prior := managedCoreInterruptedJournalFixture(t)
	if managedCoreInterruptedForwardReview(current) != strings.Repeat("8", 64) || managedCorePartialReview(current) != strings.Repeat("a", 64) || !managedCoreInterruptedSourceMembership(source, current, prior) {
		t.Fatal("exact bounded interrupted source rejected")
	}
	for _, mode := range []string{"old-receipt", "old-intent", "old-start", "old-owner", "old-lease", "extra-entry", "extra-lease", "missing-lease", "forward-completed", "forward-inflight-receipt", "wrong-phase", "wrong-review", "fresh-admission-pending", "original-core-completed"} {
		t.Run(mode, func(t *testing.T) {
			var changed environmentCreateJournal
			json.Unmarshal(mustProviderRecoveryJSON(current), &changed)
			old := len(source.Entries) - 1
			last := len(changed.Entries) - 1
			switch mode {
			case "old-receipt":
				changed.Entries[old].Receipt = mustProviderRecoveryJSON(map[string]string{"audit_sha256": strings.Repeat("0", 64), "qualified_recovery_sha256": prior.Review})
			case "old-intent":
				changed.Entries[old].IntentSHA256 = strings.Repeat("0", 64)
			case "old-start":
				changed.Entries[old].StartedAt = changed.Entries[old].StartedAt.Add(time.Second)
			case "old-owner":
				changed.OwnerToken = "replacement"
			case "old-lease":
				changed.KubernetesLeases = append([]deploymentplan.LeaseOwner{{UID: "replacement"}}, changed.KubernetesLeases...)
			case "extra-entry":
				changed.Entries = append(changed.Entries, changed.Entries[last])
			case "extra-lease":
				changed.KubernetesLeases = append(changed.KubernetesLeases, changed.KubernetesLeases[0])
			case "missing-lease":
				changed.KubernetesLeases = nil
			case "forward-completed":
				changed.Entries[last].State = "completed"
			case "forward-inflight-receipt":
				changed.Entries[last].Receipt = mustProviderRecoveryJSON(map[string]string{"fake": "owner"})
			case "wrong-phase":
				changed.Entries[last].Phase = "bootstrap"
			case "wrong-review":
				changed.Entries[last].Key = "kubernetes/managed-complete-issuer-forward-" + strings.Repeat("0", 64)
			case "fresh-admission-pending":
				changed.Entries[last-1].State = "pending"
			case "original-core-completed":
				changed.Entries[old-1].State = "completed"
			}
			selected := managedCoreInterruptedForwardReview(changed) != ""
			membership := managedCoreInterruptedSourceMembership(source, changed, prior)
			if selected && membership {
				t.Fatal("unqualified interrupted suffix accepted", mode)
			}
		})
	}
}

func TestManagedCoreInterruptedAcceptedAliasNormalizationRequiresBothRealFiveFieldReceipts(t *testing.T) {
	core := managedCoreInterruptedFixture(t)
	_, source, _ := managedCoreInterruptedJournalFixture(t)
	core.SourceSHA = deploymentplan.Digest(source)
	stage := "kubernetes/managed-complete-issuer-forward-" + strings.Repeat("2", 64)
	actual := strings.Repeat("3", 64)
	current := source
	current.Entries = slices.Clone(source.Entries)
	for index, entry := range current.Entries {
		plan, journal := "", ""
		if entry.Key == "kubernetes/managed-complete-issuer" {
			plan, journal = core.Plan.SHA256(), deploymentplan.Digest(core.Journal)
		}
		if entry.Key == "kubernetes/managed-complete-issuer-forward-"+core.PriorForward.Review {
			plan, journal = core.PriorForward.Plan.SHA256(), deploymentplan.Digest(core.PriorForward.Journal)
		}
		if plan == "" {
			continue
		}
		entry.State = "completed"
		entry.CompletedAt = entry.StartedAt.Add(time.Hour)
		entry.Receipt = mustProviderRecoveryJSON(map[string]string{"original_plan_sha256": plan, "original_failed_journal_sha256": journal, "actual_qualified_forward_stage": stage, "actual_qualified_forward_plan_sha256": actual, "original_repair_review_sha256": core.OriginalRepairReview})
		current.Entries[index] = entry
	}
	if !managedCorePartialAliasPrefixMatches(source, current, core, stage, actual) {
		t.Fatal("both genuine observed aliases rejected")
	}
	for _, mode := range []string{"missing-original", "missing-prior", "actual-plan", "original-plan", "failed-journal", "old-review", "extra-field", "started", "metadata"} {
		t.Run(mode, func(t *testing.T) {
			var changed environmentCreateJournal
			json.Unmarshal(mustProviderRecoveryJSON(current), &changed)
			index := len(changed.Entries) - 1
			var receipt map[string]string
			json.Unmarshal(changed.Entries[index].Receipt, &receipt)
			switch mode {
			case "missing-original":
				changed.Entries[len(changed.Entries)-4] = source.Entries[len(source.Entries)-4]
			case "missing-prior":
				changed.Entries[index] = source.Entries[index]
			case "actual-plan":
				receipt["actual_qualified_forward_plan_sha256"] = strings.Repeat("0", 64)
			case "original-plan":
				receipt["original_plan_sha256"] = strings.Repeat("0", 64)
			case "failed-journal":
				receipt["original_failed_journal_sha256"] = strings.Repeat("0", 64)
			case "old-review":
				receipt["original_repair_review_sha256"] = strings.Repeat("0", 64)
			case "extra-field":
				receipt["fake"] = "accepted"
			case "started":
				changed.Entries[index].StartedAt = changed.Entries[index].StartedAt.Add(time.Second)
			case "metadata":
				changed.OwnerToken = "replacement"
			}
			if mode == "actual-plan" || mode == "original-plan" || mode == "failed-journal" || mode == "old-review" || mode == "extra-field" {
				changed.Entries[index].Receipt = mustProviderRecoveryJSON(receipt)
			}
			if managedCorePartialAliasPrefixMatches(source, changed, core, stage, actual) {
				t.Fatal("unqualified alias closure accepted", mode)
			}
		})
	}
}
