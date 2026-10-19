package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedCoreAuthAdmissionFixture(t *testing.T, oldPending bool) (environmentCreateJournal, environmentCreateJournal, environmentCreateOwner, *createServiceSessionRecovery, createFoundationRecoveryArtifact, *deploymentplan.Plan, string, time.Time) {
	t.Helper()
	core, candidate := managedCorePartialFixture(t)
	now := time.Date(2026, 10, 13, 22, 10, 0, 0, time.UTC)
	audit := strings.Repeat("d", 64)
	captured := environmentCreateJournal{Schema: environmentCreateJournalSchema, Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: strings.Repeat("a", 64), OwnerToken: "original-owner", Entries: []environmentCreateJournalEntry{{Key: "openbao-audit-enable", Phase: "openbao-audit", State: "completed", IntentSHA256: strings.Repeat("b", 64), StartedAt: now.Add(-2 * time.Hour), CompletedAt: now.Add(-time.Hour), Receipt: mustProviderRecoveryJSON(map[string]string{"audit_sha256": audit, "qualified_recovery_sha256": strings.Repeat("c", 64)})}, {Key: "kubernetes/managed-complete-issuer", Phase: "managed-complete-issuer", State: "pending", IntentSHA256: strings.Repeat("e", 64), StartedAt: now.Add(-time.Hour)}}}
	if oldPending {
		captured.Entries = append(captured.Entries, environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + strings.Repeat("f", 64), Phase: "recovery", State: "pending", IntentSHA256: strings.Repeat("1", 64), StartedAt: now.Add(-20 * time.Minute)})
	}
	owner := environmentCreateOwner{Schema: environmentCreateOwnerSchema, Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: captured.PlanSHA256, OwnerToken: captured.OwnerToken, Journal: "deployment/create/executions/original/journal.json", StartedAt: now.Add(-time.Hour)}
	core.SourceSHA = deploymentplan.Digest(captured)
	core.ForwardReview = ""
	if oldPending {
		stopped := captured
		stopped.Entries = captured.Entries[:len(captured.Entries)-1]
		core.PriorAdmission = &createManagedCorePendingAdmission{Review: strings.Repeat("f", 64), SourceSHA: core.SourceSHA, StoppedSourceSHA: deploymentplan.Digest(stopped), ArtifactSHA: strings.Repeat("6", 64), CheckerSHA: strings.Repeat("9", 64), CandidateSHA: candidate.SHA256(), AuditSHA: audit, Entry: captured.Entries[len(captured.Entries)-1]}
	}
	checker := strings.Repeat("9", 64)
	a := createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-openbao-recovery-artifact/v1", QualifiedAt: now.Add(-2 * time.Minute), ValidUntil: now.Add(13 * time.Minute), Plan: createFoundationRecoveryPlan{Schema: managedCorePartialSchema, ParentSHA: captured.PlanSHA256, ParentOwnerSHA: deploymentplan.Digest(owner), ParentJournalSHA: core.SourceSHA, CheckerSHA: checker, Materials: map[string]string{
		"accepted-service-core-complete-issuer-partial-action":          deploymentplan.Digest(managedCoreRestoreOnly),
		"accepted-service-core-managed-complete-observation-action":     deploymentplan.Digest(managedCoreRestoreOnly),
		"accepted-service-core-complete-issuer-partial-original-review": core.OriginalRepairReview,
		"accepted-service-core-complete-issuer-partial-source":          deploymentplan.Digest(core),
		"accepted-service-core-complete-issuer-partial-source-journal":  deploymentplan.Digest(core.Journal),
		"accepted-service-core-complete-issuer-partial-source-plan":     core.Plan.SHA256(),
		"accepted-service-core-complete-issuer-partial-forward-plan":    candidate.SHA256(),
	}}}
	if core.PriorAdmission != nil {
		a.Plan.Materials["accepted-service-core-complete-issuer-partial-prior-admission"] = deploymentplan.Digest(core.PriorAdmission)
	}
	review := deploymentplan.Digest(a.Plan)
	a.Qualification = deploymentplan.Qualification{PlanSHA256: review, Decision: "GO"}
	for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: review})
	}
	core.ForwardReview = review
	r := &createServiceSessionRecovery{Review: core.OriginalRepairReview, ManagedCorePartial: core, Candidate: candidate}
	current := captured
	current.Entries = slices.Clone(captured.Entries)
	current.Entries = append(current.Entries, environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + review, Phase: "recovery", State: "pending", StartedAt: now.Add(-time.Minute), IntentSHA256: activeTransportJournalDigest(map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": a.Plan.ParentJournalSHA})})
	return current, captured, owner, r, a, candidate, checker, now
}

func TestManagedCoreAuthenticationOnlyExactFreshAdmissionProgress(t *testing.T) {
	for _, oldPending := range []bool{false, true} {
		for _, mode := range []string{"pending", "completed", "closed-old-admission", "foreign-review", "extra-entry", "prefix-drift", "extra-lease", "bad-owner", "bad-checker", "expired-go", "future-go", "missing-gate", "skipped-gate", "wrong-evidence", "wrong-schema", "wrong-action", "wrong-preview", "entry-intent", "entry-phase", "future-start", "early-start", "pending-receipt", "pending-completion", "unknown-state", "wrong-audit", "wrong-completion-review", "extra-receipt-field", "changed-old-intent", "changed-old-start", "foreign-old-audit", "closed-core-alias"} {
			t.Run(mode+map[bool]string{false: "/344", true: "/345"}[oldPending], func(t *testing.T) {
				current, captured, owner, r, a, preview, checker, now := managedCoreAuthAdmissionFixture(t, oldPending)
				actualOwner := owner
				pass := mode == "pending" || mode == "completed" || mode == "closed-old-admission"
				entry := &current.Entries[len(current.Entries)-1]
				complete := func() {
					entry.State = "completed"
					entry.CompletedAt = now
					entry.Receipt = mustProviderRecoveryJSON(map[string]string{"audit_sha256": strings.Repeat("d", 64), "qualified_recovery_sha256": r.ManagedCorePartial.ForwardReview})
				}
				closeOld := func() {
					if oldPending {
						v := &current.Entries[len(captured.Entries)-1]
						v.State = "completed"
						v.CompletedAt = now
						v.Receipt = mustProviderRecoveryJSON(map[string]string{"audit_sha256": strings.Repeat("d", 64), "qualified_recovery_sha256": strings.Repeat("f", 64)})
					}
				}
				switch mode {
				case "completed":
					complete()
				case "closed-old-admission":
					closeOld()
					complete()
				case "foreign-review":
					entry.Key = "openbao-recovery-admission-" + strings.Repeat("7", 64)
				case "extra-entry":
					current.Entries = append(current.Entries, *entry)
				case "prefix-drift":
					current.Entries[0].Phase = "foreign"
				case "extra-lease":
					current.KubernetesLeases = append(current.KubernetesLeases, deploymentplan.LeaseOwner{UID: "foreign"})
				case "bad-owner":
					actualOwner.OwnerToken = "foreign"
				case "bad-checker":
					checker = strings.Repeat("8", 64)
				case "expired-go":
					now = a.ValidUntil
				case "future-go":
					now = a.QualifiedAt.Add(-time.Second)
				case "missing-gate":
					a.Qualification.Gates = a.Qualification.Gates[:5]
				case "skipped-gate":
					a.Qualification.Gates[3].Status = "SKIPPED"
				case "wrong-evidence":
					a.Qualification.Gates[3].EvidenceSHA256 = strings.Repeat("8", 64)
				case "wrong-schema":
					a.Plan.Schema = "foreign"
				case "wrong-action":
					r.ManagedCorePartial.Action = managedCoreFinalizeComplete
				case "wrong-preview":
					preview.Resources[15].Action = "update"
				case "entry-intent":
					entry.IntentSHA256 = strings.Repeat("0", 64)
				case "entry-phase":
					entry.Phase = "foreign"
				case "future-start":
					entry.StartedAt = now.Add(time.Minute)
				case "early-start":
					entry.StartedAt = a.QualifiedAt.Add(-time.Second)
				case "pending-receipt":
					entry.Receipt = mustProviderRecoveryJSON(map[string]string{"audit_sha256": strings.Repeat("d", 64)})
				case "pending-completion":
					entry.CompletedAt = now
				case "unknown-state":
					entry.State = "allocated"
				case "wrong-audit":
					complete()
					entry.Receipt = mustProviderRecoveryJSON(map[string]string{"audit_sha256": strings.Repeat("8", 64), "qualified_recovery_sha256": r.ManagedCorePartial.ForwardReview})
				case "wrong-completion-review":
					complete()
					entry.Receipt = mustProviderRecoveryJSON(map[string]string{"audit_sha256": strings.Repeat("d", 64), "qualified_recovery_sha256": strings.Repeat("8", 64)})
				case "extra-receipt-field":
					complete()
					entry.Receipt = mustProviderRecoveryJSON(map[string]string{"audit_sha256": strings.Repeat("d", 64), "qualified_recovery_sha256": r.ManagedCorePartial.ForwardReview, "foreign": "claim"})
				case "changed-old-intent":
					if oldPending {
						closeOld()
						current.Entries[len(captured.Entries)-1].IntentSHA256 = strings.Repeat("8", 64)
					} else {
						current.Entries[0].IntentSHA256 = strings.Repeat("8", 64)
					}
				case "changed-old-start":
					if oldPending {
						closeOld()
						current.Entries[len(captured.Entries)-1].StartedAt = now
					} else {
						current.Entries[0].StartedAt = now
					}
				case "foreign-old-audit":
					if oldPending {
						closeOld()
						current.Entries[len(captured.Entries)-1].Receipt = mustProviderRecoveryJSON(map[string]string{"audit_sha256": strings.Repeat("8", 64), "qualified_recovery_sha256": strings.Repeat("f", 64)})
					} else {
						current.Entries[0].Receipt = mustProviderRecoveryJSON(map[string]string{"audit_sha256": strings.Repeat("8", 64)})
					}
				case "closed-core-alias":
					current.Entries[1].State = "completed"
					current.Entries[1].CompletedAt = now
					current.Entries[1].Receipt = mustProviderRecoveryJSON(map[string]string{"foreign": "forward"})
				}
				before := deploymentplan.Digest(current)
				err := validateManagedCoreAuthenticationAdmission(current, captured, actualOwner, owner, r, a, preview, checker, now)
				if (err == nil) != pass {
					t.Fatal("unsafe admission source", mode, err)
				}
				if deploymentplan.Digest(current) != before {
					t.Fatal("auth verifier fabricated journal progress")
				}
			})
		}
	}
}

func managedCoreCurrentAuthAdmissionFixture(t *testing.T) (environmentCreateJournal, environmentCreateJournal, environmentCreateOwner, *createServiceSessionRecovery, createFoundationRecoveryArtifact, *deploymentplan.Plan, string, time.Time) {
	t.Helper()
	_, captured, owner, _, a, _, checker, now := managedCoreAuthAdmissionFixture(t, true)
	core := managedCoreCurrentCertIssuerFixture(t)
	core.PriorAdmission.SourceSHA = deploymentplan.Digest(captured)
	core.PriorForward.SourceSHA = core.PriorAdmission.SourceSHA
	old := &captured.Entries[len(captured.Entries)-1]
	old.State, old.CompletedAt = "completed", now.Add(-10*time.Minute)
	old.Receipt = mustProviderRecoveryJSON(map[string]string{"audit_sha256": core.PriorAdmission.AuditSHA, "qualified_recovery_sha256": core.PriorAdmission.Review})
	captured.Entries = append(captured.Entries,
		environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + core.PriorForward.Review, Phase: "recovery", State: "completed", StartedAt: now.Add(-9 * time.Minute), CompletedAt: now.Add(-8 * time.Minute), IntentSHA256: strings.Repeat("7", 64), Receipt: mustProviderRecoveryJSON(map[string]string{"audit_sha256": core.PriorAdmission.AuditSHA, "qualified_recovery_sha256": core.PriorForward.Review})},
		environmentCreateJournalEntry{Key: "kubernetes/managed-complete-issuer-forward-" + core.PriorForward.Review, Phase: "managed-complete-issuer-forward-" + core.PriorForward.Review, State: "pending", StartedAt: now.Add(-7 * time.Minute), IntentSHA256: strings.Repeat("6", 64)})
	core.SourceSHA = deploymentplan.Digest(captured)
	core.ForwardReview = ""
	pureSource := deploymentplan.Digest(core)
	core.CertIssuerPrepare = &createCertIssuerPrepareImageRecovery{Image: environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("3", 64), SourceRevision: strings.Repeat("4", 40)}, SelectionSHA: strings.Repeat("5", 64), ManifestSHA: strings.Repeat("6", 64)}
	defs, err := managedCorePartialDefinitions(core)
	if err != nil {
		t.Fatal(err)
	}
	preview := *core.PriorForward.Plan
	preview.Resources = slices.Clone(core.PriorForward.Plan.Resources)
	for index := range preview.Resources {
		resource := &preview.Resources[index]
		resource.Action, resource.Baseline, resource.Desired = "retain", deploymentplan.Clone(resource.Desired), defs[index].Object
		if index == 14 {
			resource.Action = "update"
		}
		if index == 15 {
			resource.Baseline = deploymentplan.Clone(core.PriorForward.Plan.Resources[index].Baseline)
		}
	}
	if err := validateManagedCorePartialCandidate(core, &preview); err != nil {
		t.Fatal(err)
	}
	a.Plan.ParentJournalSHA = core.SourceSHA
	a.Plan.Materials["accepted-service-core-complete-issuer-partial-source"] = pureSource
	a.Plan.Materials["accepted-service-core-complete-issuer-partial-prior-admission"] = deploymentplan.Digest(core.PriorAdmission)
	a.Plan.Materials["accepted-service-core-complete-issuer-partial-forward-plan"] = preview.SHA256()
	addManagedCoreCertIssuerPrepareMaterials(a.Plan.Materials, core.CertIssuerPrepare)
	review := deploymentplan.Digest(a.Plan)
	core.ForwardReview = review
	a.Qualification.PlanSHA256 = review
	for index := range a.Qualification.Gates {
		a.Qualification.Gates[index].EvidenceSHA256 = review
	}
	r := &createServiceSessionRecovery{Review: core.OriginalRepairReview, ManagedCorePartial: core, Candidate: &preview}
	current := captured
	current.Entries = slices.Clone(captured.Entries)
	current.Entries = append(current.Entries, environmentCreateJournalEntry{Key: "openbao-recovery-admission-" + review, Phase: "recovery", State: "pending", StartedAt: now.Add(-time.Minute), IntentSHA256: activeTransportJournalDigest(map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": a.Plan.ParentJournalSHA})})
	return current, captured, owner, r, a, &preview, checker, now
}

func TestManagedCoreAuthenticationCurrent347PinsImmutableHistoryAndQualifiedOnlyImage(t *testing.T) {
	for _, mode := range []string{"pending", "completed", "wrong-image", "unbound-image", "unbound-selection", "unbound-manifest", "missing-image", "reopened-old-admission", "changed-old-closure", "changed-failed-forward", "closed-failed-forward", "old-source-sha", "future-source-image", "extra-entry", "extra-lease"} {
		t.Run(mode, func(t *testing.T) {
			current, captured, owner, r, a, preview, checker, now := managedCoreCurrentAuthAdmissionFixture(t)
			core := r.ManagedCorePartial
			pass := mode == "pending" || mode == "completed"
			switch mode {
			case "completed":
				entry := &current.Entries[len(current.Entries)-1]
				entry.State, entry.CompletedAt = "completed", now
				entry.Receipt = mustProviderRecoveryJSON(map[string]string{"audit_sha256": core.PriorAdmission.AuditSHA, "qualified_recovery_sha256": core.ForwardReview})
			case "wrong-image":
				core.CertIssuerPrepare.Image.Reference = "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("0", 64)
			case "unbound-image":
				a.Plan.Materials[managedCoreCertIssuerPrepareMaterialPrefix+"image"] = strings.Repeat("0", 64)
			case "unbound-selection":
				a.Plan.Materials[managedCoreCertIssuerPrepareMaterialPrefix+"selection"] = strings.Repeat("0", 64)
			case "unbound-manifest":
				a.Plan.Materials[managedCoreCertIssuerPrepareMaterialPrefix+"manifest"] = strings.Repeat("0", 64)
			case "missing-image":
				core.CertIssuerPrepare = nil
			case "reopened-old-admission":
				current.Entries[2].State, current.Entries[2].CompletedAt, current.Entries[2].Receipt = "pending", time.Time{}, nil
			case "changed-old-closure":
				current.Entries[2].CompletedAt = now
			case "changed-failed-forward":
				current.Entries[len(captured.Entries)-1].IntentSHA256 = strings.Repeat("0", 64)
			case "closed-failed-forward":
				current.Entries[len(captured.Entries)-1].State = "completed"
			case "old-source-sha":
				core.SourceSHA = core.PriorAdmission.SourceSHA
			case "future-source-image":
				future := *core
				future.ForwardReview = ""
				a.Plan.Materials["accepted-service-core-complete-issuer-partial-source"] = deploymentplan.Digest(future)
			case "extra-entry":
				current.Entries = append(current.Entries, current.Entries[len(current.Entries)-1])
			case "extra-lease":
				current.KubernetesLeases = append(current.KubernetesLeases, deploymentplan.LeaseOwner{UID: "foreign"})
			}
			before := deploymentplan.Digest(current)
			if (validateManagedCoreAuthenticationAdmission(current, captured, owner, owner, r, a, preview, checker, now) == nil) != pass {
				t.Fatal("wrong actual347 auth/progress scope", mode)
			}
			if deploymentplan.Digest(current) != before {
				t.Fatal("authentication changed actual historical progress")
			}
		})
	}
}
