package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const managedLateSeedForwardSourceFilename = "managed-late-seed-trust-forward-source.json"
const managedLateSeedForwardPlanFilename = "managed-late-seed-trust-forward-plan.json"
const managedLateSeedForwardMaterial = "accepted-service-core-late-seed-trust-forward-"

func managedLateSeedForwardReview(j environmentCreateJournal) string {
	if len(j.Entries) < 6 || !j.CompletedAt.IsZero() {
		return ""
	}
	entry := j.Entries[len(j.Entries)-1]
	const prefix = "kubernetes/service-session-original-key-renewal-"
	review := strings.TrimPrefix(entry.Key, prefix)
	if entry.Key != prefix+review || !environmentPreflightDigest.MatchString(review) || entry.Phase != strings.TrimPrefix(entry.Key, "kubernetes/") || entry.State != "pending" || !entry.CompletedAt.IsZero() || len(entry.Receipt) != 0 {
		return ""
	}
	return review
}

func managedLateSeedForwardSourceDigest(f *createManagedLateSeedForward) string {
	if f == nil {
		return ""
	}
	copy := *f
	copy.Review = ""
	return deploymentplan.Digest(copy)
}

// The original admission is historical authority, not a renewed GO. The new
// forward qualification separately binds current source/checker and its clock.
func loadManagedLateSeedForwardSource(cfg deploymentConfig, store secretStore, e *environmentCreateExecution) (*createServiceSessionRecovery, *createManagedLateSeedForward, error) {
	bad := func(stage string) (*createServiceSessionRecovery, *createManagedLateSeedForward, error) {
		return nil, nil, environmentCreateBootstrapBlocker("late seed trust forward requires exact terminal admitted seed: " + stage)
	}
	if e == nil || store.Root != e.Store.Root {
		return bad("selected-original-source")
	}
	oldReview := managedLateSeedForwardReview(e.Journal)
	if oldReview == "" {
		return bad("literal-stopped-seed")
	}
	a, source, err := loadAdmittedCreateRecovery(store, e, oldReview)
	if err != nil || a.Plan.Schema != managedLateSessionSchema || len(e.Journal.Entries) != len(source.Entries)+5 || len(e.Journal.KubernetesLeases) != len(source.KubernetesLeases)+2 {
		return bad("original-admitted-five-entry-suffix")
	}
	admission := e.Journal.Entries[len(source.Entries)]
	old := *e
	old.Journal = source
	p := a.Plan
	r, err := loadManagedLateQualifiedSource(cfg, store, &old, &p, oldReview, admission.StartedAt)
	if err != nil {
		return nil, nil, err
	}
	late := r.ManagedLateSession
	if !managedLateAdmissionMatches(a, source, admission, late) {
		return bad("genuine-original-admission-window")
	}
	if _, _, err := managedLateSessionRuntimeProgress(store, e, late); err != nil {
		return nil, nil, err
	}
	entry := e.Journal.Entries[len(e.Journal.Entries)-1]
	lease := e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1]
	failed, journal, err := readManagedLateProgressChild(store, e, entry, &lease)
	if err != nil || !managedLateSamePlan(failed, late.SeedCandidate) {
		return bad("original-failed-child")
	}
	id := "ConfigMap/" + e.Journal.Stack + "-video-cloud/pki-create-openbao-tls-consumer-trust"
	var trust deploymentplan.Resource
	for _, resource := range late.AcceptedCore.Resources {
		if resource.ID == id {
			trust = resource
		}
	}
	if trust.ID != id || trust.UID != late.AcceptedCoreJournal.Owners[id] {
		return bad("original-active-tls-trust-owner")
	}
	f := &createManagedLateSeedForward{Schema: managedLateSeedForwardSchema, OriginalReview: oldReview, SourceSHA: deploymentplan.Digest(e.Journal), SourceOwnerSHA: deploymentplan.Digest(e.owner), OriginalArtifactSHA: deploymentplan.Digest(a), OriginalSource: source, FailedPlan: failed, FailedJournal: journal, FailedPlanSHA: failed.SHA256(), FailedJournalSHA: deploymentplan.Digest(journal), FailedJobUID: journal.Owners[failed.Resources[0].ID], TrustOwner: trust}
	if err := validateManagedLateSeedForwardFailedChild(f); err != nil {
		return nil, nil, err
	}
	r.ManagedLateSeedForward = f
	return r, f, nil
}

func managedLateSeedForwardNativeCheckpoint(store secretStore, e *environmentCreateExecution) bool {
	if e == nil {
		return false
	}
	var original createFoundationRecoveryArtifact
	review := managedLateSeedForwardReview(e.Journal)
	if review == "" || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, "qualification.json"), &original) != nil {
		return false
	}
	cfg := deploymentConfig{Environment: original.Plan.Plan.Environment, Values: map[string]string{"CLOUD_STACK_NAME": original.Plan.Plan.Stack}}
	// Source derivation uses the authenticated original configuration from the
	// TLS repair, whose full content remains bound to the parent generation.
	var tlsArtifact createOpenBaoTLSRootCRLArtifact
	var late createManagedLateSessionRecovery
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, "managed-late-session-source.json"), &late) != nil || late.OpenBaoTLSRepair == nil {
		return false
	}
	tlsArtifact = *late.OpenBaoTLSRepair
	cfg = tlsArtifact.Plan.Configuration
	_, _, err := loadManagedLateSeedForwardSource(cfg, store, e)
	return err == nil
}

func prepareManagedLateSeedForward(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan) (*createServiceSessionRecovery, error) {
	r, f, err := loadManagedLateSeedForwardSource(cfg, store, e)
	if err != nil {
		return nil, err
	}
	original, _, err := loadAdmittedCreateRecovery(store, e, f.OriginalReview)
	if err != nil {
		return nil, err
	}
	for key, value := range original.Plan.Materials {
		if existing, ok := p.Materials[key]; ok && existing != value {
			return nil, environmentCreateBootstrapBlocker("late forward original material changed")
		}
		p.Materials[key] = value
	}
	p.Plan = original.Plan.Plan
	p.Schema = managedLateSeedForwardSchema
	if err := verifyManagedLateSessionCurrentNative(ctx, cfg, store, e, b, r, p, r.ManagedLateSession); err != nil {
		return nil, err
	}
	if err := verifyServiceSessionAuthentication(ctx, cfg, store, e, b, r); err != nil {
		return nil, err
	}
	f.NativeEvidence, err = verifyManagedLateSeedForwardNoMint(ctx, cfg, store, e, b, r, f)
	if err != nil {
		return nil, err
	}
	definitions, err := managedLateSeedForwardDefinitions(f)
	if err != nil {
		return nil, err
	}
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return nil, err
	}
	f.Candidate, err = managedLateCompile(p.Plan, "managed-late-seed-trust-forward", definitions, live)
	if err != nil || validateManagedLateSeedForwardCandidate(f, f.Candidate) != nil {
		return nil, environmentCreateBootstrapBlocker("late forward exact corrected seed candidate unavailable")
	}
	r.Candidate = f.Candidate
	for name, value := range map[string]string{"source": f.SourceSHA, "descriptor": managedLateSeedForwardSourceDigest(f), "original-review": f.OriginalReview, "original-artifact": f.OriginalArtifactSHA, "failed-plan": f.FailedPlanSHA, "failed-journal": f.FailedJournalSHA, "trust-owner": deploymentplan.Digest(f.TrustOwner), "native-no-mint": deploymentplan.Digest(f.NativeEvidence), "candidate": f.Candidate.SHA256(), "fresh-native-phase": deploymentplan.Digest(map[string]string{"schema": managedLateSeedForwardSchema, "duration": "50m", "source": f.SourceSHA, "terminal-failed-plan": f.FailedPlanSHA})} {
		p.Materials[managedLateSeedForwardMaterial+name] = value
	}
	return r, nil
}

func verifyManagedLateSeedForwardCurrent(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan) error {
	f := r.ManagedLateSeedForward
	if f == nil || validateManagedLateSeedForwardCandidate(f, f.Candidate) != nil {
		return environmentCreateBootstrapBlocker("late seed forward exact candidate missing")
	}
	original, _, err := loadAdmittedCreateRecovery(store, e, f.OriginalReview)
	if err != nil {
		return err
	}
	for key, value := range original.Plan.Materials {
		p.Materials[key] = value
	}
	if err := verifyManagedLateSessionCurrentNative(ctx, cfg, store, e, b, r, p, r.ManagedLateSession); err != nil {
		return err
	}
	if err := verifyServiceSessionAuthentication(ctx, cfg, store, e, b, r); err != nil {
		return err
	}
	fresh, err := verifyManagedLateSeedForwardNoMint(ctx, cfg, store, e, b, r, f)
	if err != nil || deploymentplan.Digest(fresh) != deploymentplan.Digest(f.NativeEvidence) {
		return environmentCreateBootstrapBlocker("late seed terminal no-mint evidence changed")
	}
	return f.Candidate.Admit(ctx, b)
}

func managedLateSeedForwardAlias(f *createManagedLateSeedForward, actual *deploymentplan.Plan) map[string]string {
	return map[string]string{"original_plan_sha256": f.FailedPlanSHA, "original_failed_journal_sha256": f.FailedJournalSHA, "actual_qualified_forward_stage": "kubernetes/managed-late-seed-trust-forward-" + f.Review, "actual_qualified_forward_plan_sha256": actual.SHA256(), "original_repair_review_sha256": f.OriginalReview}
}

// Parse only this explicitly declared extra admission/child and its genuine
// alias. The returned slices are parsing inputs; no actual journal is changed.
func managedLateSeedForwardFrame(store secretStore, e *environmentCreateExecution, late *createManagedLateSessionRecovery, source environmentCreateJournal) (*createManagedLateSeedForward, []environmentCreateJournalEntry, []deploymentplan.LeaseOwner, error) {
	bad := func() (*createManagedLateSeedForward, []environmentCreateJournalEntry, []deploymentplan.LeaseOwner, error) {
		return nil, nil, nil, environmentCreateBootstrapBlocker("late seed forward exact admitted frame changed")
	}
	entries, leases := e.Journal.Entries, e.Journal.KubernetesLeases
	if late != nil && late.ExpiredRenewal != nil {
		return nil, entries, leases, nil
	}
	index := len(source.Entries) + 5
	if len(entries) <= index {
		return nil, entries, leases, nil
	}
	fresh := strings.TrimPrefix(entries[index].Key, "openbao-recovery-admission-")
	if !strings.HasPrefix(entries[index].Key, "openbao-recovery-admission-") {
		return nil, entries, leases, nil
	}
	if !environmentPreflightDigest.MatchString(fresh) || entries[index].Key != "openbao-recovery-admission-"+fresh {
		return bad()
	}
	a, captured, err := loadAdmittedCreateRecovery(store, e, fresh)
	var f createManagedLateSeedForward
	if err != nil || a.Plan.Schema != managedLateSeedForwardSchema || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", fresh, managedLateSeedForwardSourceFilename), &f) != nil || f.Review != fresh || f.OriginalReview != late.Review || f.SourceSHA != deploymentplan.Digest(captured) || f.SourceOwnerSHA != a.Plan.ParentOwnerSHA || len(captured.Entries) != index || deploymentplan.Digest(f.OriginalSource) != deploymentplan.Digest(source) || a.Plan.Materials[managedLateSeedForwardMaterial+"descriptor"] != managedLateSeedForwardSourceDigest(&f) || f.Candidate == nil || a.Plan.Materials[managedLateSeedForwardMaterial+"candidate"] != f.Candidate.SHA256() || validateManagedLateSeedForwardFailedChild(&f) != nil || validateManagedLateSeedForwardCandidate(&f, f.Candidate) != nil {
		return bad()
	}
	var oldPlan deploymentplan.Plan
	var oldJournal deploymentplan.Journal
	original, originalSource, originalErr := loadAdmittedCreateRecovery(store, e, late.Review)
	if len(leases) < len(captured.KubernetesLeases) || originalErr != nil || f.OriginalArtifactSHA != deploymentplan.Digest(original) || deploymentplan.Digest(originalSource) != deploymentplan.Digest(f.OriginalSource) || !managedLateSamePlan(f.FailedPlan, late.SeedCandidate) || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/rollback", f.FailedPlanSHA, "plan.json"), &oldPlan) != nil || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/rollback", f.FailedPlanSHA, "journal.json"), &oldJournal) != nil || oldPlan.SHA256() != f.FailedPlanSHA || deploymentplan.Digest(oldJournal) != f.FailedJournalSHA {
		return bad()
	}
	admission := entries[index]
	var admissionReceipt map[string]string
	admissionIntent := map[string]string{"review_sha256": fresh, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": f.SourceSHA}
	if admission.Phase != "recovery" || admission.State != "completed" || admission.StartedAt.IsZero() || admission.CompletedAt.IsZero() || a.QualifiedAt.IsZero() || admission.StartedAt.Before(a.QualifiedAt) || !admission.StartedAt.Before(a.ValidUntil) || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || admission.CompletedAt.Before(admission.StartedAt) || admission.IntentSHA256 != activeTransportJournalDigest(admissionIntent) || decodeEnvironmentJSON(admission.Receipt, &admissionReceipt) != nil || len(admissionReceipt) != 2 || admissionReceipt["qualified_recovery_sha256"] != fresh || !environmentPreflightDigest.MatchString(admissionReceipt["audit_sha256"]) {
		return bad()
	}
	prefix := e.Journal
	prefix.Entries = slices.Clone(entries[:index])
	prefix.KubernetesLeases = slices.Clone(leases[:len(captured.KubernetesLeases)])
	seed := &prefix.Entries[index-1]
	var actual *deploymentplan.Plan
	if len(entries) > index+1 {
		forward := entries[index+1]
		if forward.Key != "kubernetes/managed-late-seed-trust-forward-"+fresh || forward.Phase != "managed-late-seed-trust-forward-"+fresh || len(leases) <= len(captured.KubernetesLeases) {
			return bad()
		}
		plan, journal, readErr := readManagedLateProgressChild(store, e, forward, &leases[len(captured.KubernetesLeases)])
		if readErr != nil || !managedLateLeaseMatches(leases[len(captured.KubernetesLeases)], captured.Stack, plan.SHA256()) || !managedLateSeedForwardActualCandidate(&f, plan) || forward.StartedAt.Before(admission.CompletedAt) {
			return bad()
		}
		if forward.State == "completed" {
			if !createOpenBaoACKCompletedChild(plan, journal) {
				return bad()
			}
			actual = plan
		}
		entries = append(slices.Clone(entries[:index]), entries[index+2:]...)
		leases = append(slices.Clone(leases[:len(captured.KubernetesLeases)]), leases[len(captured.KubernetesLeases)+1:]...)
	} else {
		entries = slices.Clone(entries[:index])
	}
	if seed.State == "completed" {
		unchanged := *seed
		unchanged.State = captured.Entries[index-1].State
		unchanged.CompletedAt = captured.Entries[index-1].CompletedAt
		unchanged.Receipt = captured.Entries[index-1].Receipt
		if deploymentplan.Digest(unchanged) != deploymentplan.Digest(captured.Entries[index-1]) {
			return bad()
		}
		if actual == nil || seed.CompletedAt.Before(e.Journal.Entries[index+1].CompletedAt) || activeTransportJournalDigest(jsonRawObject(seed.Receipt)) != activeTransportJournalDigest(managedLateSeedForwardAlias(&f, actual)) {
			return bad()
		}
		*seed = captured.Entries[index-1]
	}
	if deploymentplan.Digest(prefix) != deploymentplan.Digest(captured) {
		return bad()
	}
	return &f, entries, leases, nil
}

func readManagedLateEffectiveSeed(store secretStore, e *environmentCreateExecution, late *createManagedLateSessionRecovery) (*deploymentplan.Plan, environmentCreateJournalEntry, deploymentplan.Journal, error) {
	key := "kubernetes/service-session-original-key-renewal-" + late.Review
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil {
		return nil, environmentCreateJournalEntry{}, deploymentplan.Journal{}, environmentCreateBootstrapBlocker("late accepted seed entry absent")
	}
	var alias map[string]string
	if json.Unmarshal(entry.Receipt, &alias) == nil && alias["actual_qualified_forward_stage"] != "" {
		_, source, err := loadAdmittedCreateRecovery(store, e, late.Review)
		if err != nil {
			return nil, *entry, deploymentplan.Journal{}, err
		}
		f, _, _, err := managedLateSeedForwardFrame(store, e, late, source)
		if err != nil || f == nil {
			return nil, *entry, deploymentplan.Journal{}, environmentCreateBootstrapBlocker("late effective seed forward authority absent")
		}
		plan, journal, err := managedTransportPostAcceptedStage(store, e, alias["actual_qualified_forward_stage"])
		if err != nil || activeTransportJournalDigest(alias) != activeTransportJournalDigest(managedLateSeedForwardAlias(f, plan)) || !managedLateSeedForwardActualCandidate(f, plan) {
			return nil, *entry, journal, environmentCreateBootstrapBlocker("late effective seed alias differs")
		}
		return plan, *entry, journal, nil
	}
	plan, _, err := readCreateOpenBaoACKCompletedStage(store, e.Journal, strings.TrimPrefix(key, "kubernetes/"))
	var journal deploymentplan.Journal
	if err == nil {
		err = createOpenBaoACKCompletionRead(store, filepath.Join("deployment/rollback", plan.SHA256(), "journal.json"), &journal)
	}
	return plan, *entry, journal, err
}

func executeManagedLateSeedTrustForward(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, apply environmentCreateBootstrapApply) error {
	r := runtime.ServiceSessionRecovery
	f := r.ManagedLateSeedForward
	e := runtime.Execution
	if f == nil || !environmentPreflightDigest.MatchString(f.Review) || e.Check(ctx) != nil {
		return environmentCreateBootstrapBlocker("late seed forward fresh runtime authority missing")
	}
	a, source, err := loadAdmittedCreateRecovery(store, e, f.Review)
	if err != nil || a.Plan.Schema != managedLateSeedForwardSchema || deploymentplan.Digest(source) != f.SourceSHA || a.Plan.Materials[managedLateSeedForwardMaterial+"descriptor"] != managedLateSeedForwardSourceDigest(f) {
		return environmentCreateBootstrapBlocker("late seed forward fresh admission differs")
	}
	if err := verifyServiceSessionAuthentication(ctx, cfg, store, e, runtime.Backend, r); err != nil {
		return err
	}
	fresh, err := verifyManagedLateSeedForwardNoMint(ctx, cfg, store, e, runtime.Backend, r, f)
	if err != nil || deploymentplan.Digest(fresh) != deploymentplan.Digest(f.NativeEvidence) {
		return environmentCreateBootstrapBlocker("late seed no-mint boundary changed before forward")
	}
	definitions, err := managedLateSeedForwardDefinitions(f)
	if err != nil {
		return err
	}
	stage := "managed-late-seed-trust-forward-" + f.Review
	if err := apply(ctx, stage, definitions); err != nil {
		return err
	}
	actual, journal, err := managedTransportPostAcceptedStage(store, e, "kubernetes/"+stage)
	if err != nil || !managedLateSeedForwardActualCandidate(f, actual) || !createOpenBaoACKCompletedChild(actual, journal) {
		return environmentCreateBootstrapBlocker("late corrected seed forward is not genuinely accepted")
	}
	key := "kubernetes/service-session-original-key-renewal-" + f.OriginalReview
	if err := e.RecordReceipt(key, mustProviderRecoveryJSON(managedLateSeedForwardAlias(f, actual))); err != nil {
		return err
	}
	return e.CompleteEntry(key, time.Now().UTC())
}

func managedLateSeedForwardNoMintSourceValid(store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, f *createManagedLateSeedForward) bool {
	if e == nil || r == nil || f == nil || r.ManagedLateSession == nil {
		return false
	}
	if deploymentplan.Digest(e.Journal) == f.SourceSHA {
		_, _, err := managedLateSessionRuntimeProgress(store, e, r.ManagedLateSession)
		return err == nil
	}
	if !environmentPreflightDigest.MatchString(f.Review) {
		return false
	}
	a, source, err := loadAdmittedCreateRecovery(store, e, f.Review)
	if err != nil || a.Plan.Schema != managedLateSeedForwardSchema || a.Plan.ParentJournalSHA != f.SourceSHA || a.Plan.Materials[managedLateSeedForwardMaterial+"descriptor"] != managedLateSeedForwardSourceDigest(f) || deploymentplan.Digest(source) != f.SourceSHA || len(e.Journal.Entries) != len(source.Entries)+1 || len(e.Journal.KubernetesLeases) != len(source.KubernetesLeases) {
		return false
	}
	prefix := e.Journal
	prefix.Entries = prefix.Entries[:len(source.Entries)]
	return deploymentplan.Digest(prefix) == f.SourceSHA
}

func loadManagedLateSeedForwardQualified(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, p *createFoundationRecoveryPlan, review string) (*createServiceSessionRecovery, error) {
	var a createFoundationRecoveryArtifact
	base := filepath.Join("deployment/create/openbao-recovery", review)
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, "qualification.json"), &a) != nil || !managedLateQualifiedArtifactValid(a, e, *p, review, time.Now().UTC(), managedLateSeedForwardSchema) {
		return nil, environmentCreateBootstrapBlocker("late seed forward requires fresh current-checker sixGO")
	}
	r, loaded, err := loadManagedLateSeedForwardSource(cfg, store, e)
	if err != nil {
		return nil, err
	}
	var f createManagedLateSeedForward
	var preview deploymentplan.Plan
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, managedLateSeedForwardSourceFilename), &f) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(base, managedLateSeedForwardPlanFilename), &preview) != nil || f.Review != review || f.Candidate == nil || f.Candidate.SHA256() != preview.SHA256() || a.Plan.Materials[managedLateSeedForwardMaterial+"descriptor"] != managedLateSeedForwardSourceDigest(&f) || a.Plan.Materials[managedLateSeedForwardMaterial+"candidate"] != f.Candidate.SHA256() || validateManagedLateSeedForwardCandidate(&f, f.Candidate) != nil {
		return nil, environmentCreateBootstrapBlocker("late seed forward immutable descriptor/preview changed")
	}
	comparison := f
	comparison.Review = ""
	comparison.Candidate = nil
	comparison.NativeEvidence = nil
	comparison.FailedPodUID = ""
	if managedLateSeedForwardSourceDigest(&comparison) != managedLateSeedForwardSourceDigest(loaded) {
		return nil, environmentCreateBootstrapBlocker("late seed forward original ancestry changed")
	}
	image, selection, err := loadServiceTrustRepairImage(ctx, cfg, store, e.Journal.PlanSHA256)
	if err != nil || deploymentplan.Digest(image) != a.Plan.Materials["accepted-service-core-late-session-image"] || selection != a.Plan.Materials["accepted-service-core-late-session-image-selection"] {
		return nil, environmentCreateBootstrapBlocker("late seed forward original formal image changed")
	}
	*p = a.Plan
	r.ManagedLateSeedForward = &f
	r.Candidate = f.Candidate
	return r, nil
}

func managedLateEffectiveSeedIsForward(store secretStore, e *environmentCreateExecution, late *createManagedLateSessionRecovery, plan *deploymentplan.Plan) bool {
	_, source, err := loadAdmittedCreateRecovery(store, e, late.Review)
	if err != nil {
		return false
	}
	f, _, _, err := managedLateSeedForwardFrame(store, e, late, source)
	return err == nil && f != nil && managedLateSeedForwardActualCandidate(f, plan)
}

// The child engine substitutes only its actual qualified stage binding. Every
// other input/action/UID/recipe field must remain the reviewed one-create plan.
func managedLateSeedForwardActualCandidate(f *createManagedLateSeedForward, actual *deploymentplan.Plan) bool {
	if f == nil || actual == nil || !environmentPreflightDigest.MatchString(f.Review) {
		return false
	}
	defs, err := managedLateSeedForwardDefinitions(f)
	if err != nil || actual.BindingsSHA256 != environmentCreateStageBindingsSHA("managed-late-seed-trust-forward-"+f.Review, defs) {
		return false
	}
	copy := *actual
	copy.BindingsSHA256 = environmentCreateStageBindingsSHA("managed-late-seed-trust-forward", defs)
	return validateManagedLateSeedForwardCandidate(f, &copy) == nil
}
