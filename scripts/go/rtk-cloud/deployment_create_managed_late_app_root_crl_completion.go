package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"time"
)

const managedLateAppCompletionSchema = "rtk-cloud.create-app-root-crl-completion-recovery/v1"

var managedLateAppCompletionGates = []string{"deployment.app-root-crl.completion-original-authority", "deployment.app-root-crl.completion-recorded-import", "deployment.app-root-crl.completion-current-native-two", "deployment.app-root-crl.completion-current-consumers", "deployment.app-root-crl.completion-preserved-identities", "deployment.app-root-crl.completion-original-owned-lease"}

type managedLateAppCompletionPlan struct {
	Schema, Parent, OriginalReview, OriginalArtifactSHA, SourceJournalSHA, OwnerSHA, ConfigurationSHA, KubeconfigSHA, CheckerSHA string
	NativeEvidence                                                                                                               map[string]string
	Lease                                                                                                                        deploymentplan.LeaseOwner
	LeaseObject                                                                                                                  deploymentplan.Object
	QualifiedAt                                                                                                                  time.Time
}
type managedLateAppCompletionArtifact struct {
	Schema                  string
	Plan                    managedLateAppCompletionPlan
	Qualification           deploymentplan.Qualification
	QualifiedAt, ValidUntil time.Time
}
type managedLateAppCompletionExecution struct {
	Schema, Review, OriginalReview, SourceSHA, FinalSHA, EvidenceSHA string
	StartedAt, CompletedAt                                           time.Time
}

func managedLateAppCompletionBase(old, review string) string {
	return filepath.Join(appRootCRLBase(old), "completion-only", review)
}
func runEnvironmentCreateAppRootCRLCompletion(args []string) error {
	return runEnvironmentCreateAppRootCRLCompletionContext(context.Background(), args, os.Stdout, os.Stderr)
}
func runEnvironmentCreateAppRootCRLCompletionContext(parentCtx context.Context, args []string, out, diagnostics io.Writer) error {
	fs := flag.NewFlagSet("recover-create-app-root-crl-completion", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	env := fs.String("environment", "", "selected dev")
	parent := fs.String("parent-plan-sha256", "", "original parent")
	old := fs.String("original-review-sha256", "", "original admitted App review")
	workspaceOpt := fs.String("workspace", "", "selected workspace")
	execute := fs.Bool("execute", false, "complete the original App proof only")
	review := fs.String("review-sha256", "", "fresh completion-only review")
	confirm := fs.String("confirm", "", "original stack")
	timeout := fs.Duration("timeout", 30*time.Minute, "bounded readonly verification")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *env != "dev" || !environmentPreflightDigest.MatchString(*parent) || !environmentPreflightDigest.MatchString(*old) || *timeout <= 0 || *timeout > 30*time.Minute || !*execute && *review != "" {
		return appRootCRLError("completion-arguments")
	}
	if err := recoveryMutationGuard(append([]string{"deployment", "recover-create-app-root-crl-completion"}, args...)); err != nil {
		return err
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	if *workspaceOpt != "" {
		workspace = *workspaceOpt
	}
	cfg, err := resolveDeploymentConfig(workspace, *env, "")
	if err != nil {
		return err
	}
	store, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parentCtx, *timeout)
	defer cancel()
	profile := make([]bool, 13)
	for i := range profile {
		profile[i] = true
	}
	e, generation, _, _, err := loadInitializedCreateAudit(cfg, store, *parent, false, false, false, false, false, profile...)
	if err != nil {
		return err
	}
	defer e.Close()
	if deploymentplan.Digest(operator) != generation.Plan.Plan.OperatorSHA256 {
		return appRootCRLError("completion-original-operator")
	}
	kube, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || environmentPreflightBytesSHA256(kube) != generation.Plan.KubeconfigSHA256 {
		return appRootCRLError("completion-original-kubeconfig")
	}
	b, err := newEnvironmentCreateCRLBackend(string(kube), *env, cfg.Values)
	if err != nil {
		return err
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return err
	}
	original, source, err := loadManagedLateAppRootCRLArtifact(store, e, *old)
	if err != nil {
		return err
	}
	if !managedLateAppCompletionSourceValid(original, source, e.Journal) || original.Plan.ConfigurationSHA != deploymentplan.Digest(cfg) || original.Plan.KubeconfigSHA != environmentPreflightBytesSHA256(kube) || managedLateInstallationClockAbsent(store, *parent) != nil {
		return appRootCRLError("completion-exact-current-source")
	}
	if !*execute {
		if _, err := os.Lstat(filepath.Join(store.Root, appRootCRLPointer(*parent))); !os.IsNotExist(err) {
			return appRootCRLError("completion-already-published-or-unknown-pointer")
		}
		evidence, err := observeManagedLateAppCompletion(ctx, cfg, store, e, b, operator, original, source)
		if err != nil {
			return err
		}
		lease, actual, err := observeManagedLateAppCompletionLease(ctx, e, b, original, source)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		plan := managedLateAppCompletionPlan{Schema: managedLateAppCompletionSchema, Parent: *parent, OriginalReview: *old, OriginalArtifactSHA: deploymentplan.Digest(original), SourceJournalSHA: deploymentplan.Digest(e.Journal), OwnerSHA: deploymentplan.Digest(e.owner), ConfigurationSHA: deploymentplan.Digest(cfg), KubeconfigSHA: environmentPreflightBytesSHA256(kube), CheckerSHA: checker, NativeEvidence: evidence, Lease: lease, LeaseObject: actual, QualifiedAt: now}
		sha := deploymentplan.Digest(plan)
		a := managedLateAppCompletionArtifact{Schema: managedLateAppCompletionSchema + "/artifact", Plan: plan, Qualification: deploymentplan.Qualification{PlanSHA256: sha, Decision: "GO"}, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
		for _, id := range managedLateAppCompletionGates {
			a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: sha})
		}
		if !managedLateAppCompletionMembership(original, source, e.Journal, e.Journal, evidence) {
			return appRootCRLError("completion-original-recorded-evidence")
		}
		for name, value := range map[string]any{"qualification.json": a, "source-owner.json": e.owner, "source-journal.json": e.Journal} {
			if err := writeMQTTRootCRLImmutable(store, filepath.Join(managedLateAppCompletionBase(*old, sha), name), value); err != nil {
				return err
			}
		}
		if err := e.Check(ctx); err != nil {
			return err
		}
		fmt.Fprintf(out, "GO environment=dev scope=original-app-root-crl-completion original-review-sha256=%s review-sha256=%s native-writes=false original-lease-retained=true\n", *old, sha)
		return nil
	}
	a, captured, err := loadManagedLateAppCompletionArtifact(store, *old, *review, original, source, e.owner)
	if err != nil {
		return err
	}
	if *confirm != cfg.Values["CLOUD_STACK_NAME"] || a.Plan.CheckerSHA != checker || a.Plan.ConfigurationSHA != deploymentplan.Digest(cfg) || a.Plan.KubeconfigSHA != environmentPreflightBytesSHA256(kube) || !time.Now().UTC().Before(a.ValidUntil) || !managedLateAppCompletionMembership(original, source, captured, e.Journal, a.Plan.NativeEvidence) {
		return appRootCRLError("completion-fresh-qualified-inputs")
	}
	return executeManagedLateAppCompletion(ctx, cfg, store, e, b, operator, original, source, a, captured, out)
}
func observeManagedLateAppCompletionLease(ctx context.Context, e *environmentCreateExecution, b environmentKubernetesBackend, a createAppRootCRLArtifact, source environmentCreateJournal) (deploymentplan.LeaseOwner, deploymentplan.Object, error) {
	var empty deploymentplan.LeaseOwner
	if e == nil || e.Check(ctx) != nil || len(e.Journal.KubernetesLeases) != len(source.KubernetesLeases)+1 {
		return empty, nil, appRootCRLError("completion-exact-original-lease-history")
	}
	owner := e.Journal.KubernetesLeases[len(source.KubernetesLeases)]
	actual, err := b.Read(ctx, owner.ResourceID)
	if err != nil || actual == nil || !managedLateLeaseMatches(owner, source.Stack, rootCRLLeasePlan(a.Plan.Configuration, deploymentplan.Digest(a.Plan)).SHA256()) || validateManagedApplicationInterruptedLease(rootCRLLeasePlan(a.Plan.Configuration, deploymentplan.Digest(a.Plan)), owner, actual) != nil {
		return empty, nil, appRootCRLError("completion-original-live-owned-lease")
	}
	return owner, actual, nil
}
func observeManagedLateAppCompletion(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, a createAppRootCRLArtifact, source environmentCreateJournal) (map[string]string, error) {
	if !managedLateAppCompletionSourceValid(a, source, e.Journal) || managedLateInstallationClockAbsent(store, a.Plan.Parent) != nil {
		return nil, appRootCRLError("completion-original-source")
	}
	if _, _, err := observeManagedLateAppCompletionLease(ctx, e, b, a, source); err != nil {
		return nil, err
	}
	if err := verifyManagedLateAppRootCRLInputs(ctx, cfg, store, e, b, a.Plan, true); err != nil {
		return nil, err
	}
	binding, recorded, err := resolveInitialOperatorBinding(ctx, cfg, store, e, b, operator)
	if err != nil || !recorded || deploymentplan.Digest(binding) != deploymentplan.Digest(a.Plan.Operator) {
		return nil, appRootCRLError("completion-original-human-principal")
	}
	snapshot, _, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, a.Plan.Root.ID)
	if err != nil || deploymentplan.Digest(snapshot.Prior) != deploymentplan.Digest(a.Plan.Signed) {
		return nil, appRootCRLError("completion-current-native-two")
	}
	snapshot.Prior = a.Plan.Snapshot.Prior
	if deploymentplan.Digest(snapshot) != deploymentplan.Digest(a.Plan.Snapshot) {
		return nil, appRootCRLError("completion-current-complete-revocation-union")
	}
	evidence := map[string]string{}
	for _, target := range a.Plan.Consumers {
		actual, err := observeManagedLateAppRootCRLConsumer(ctx, cfg, store, e, b, a, target)
		if err != nil {
			return nil, err
		}
		for k, v := range actual {
			if previous := evidence[k]; previous != "" && previous != v {
				return nil, appRootCRLError("completion-two-consumer-conflict")
			}
			evidence[k] = v
		}
	}
	if !managedLateAppCompletionEvidenceValid(a, evidence) || !managedLateAppCompletionMembership(a, source, e.Journal, e.Journal, evidence) {
		return nil, appRootCRLError("completion-native-evidence-differs-from-recorded-proof")
	}
	return evidence, e.Check(ctx)
}
func loadManagedLateAppCompletionArtifact(store secretStore, old, review string, original createAppRootCRLArtifact, source environmentCreateJournal, owner environmentCreateOwner) (managedLateAppCompletionArtifact, environmentCreateJournal, error) {
	var a managedLateAppCompletionArtifact
	var captured environmentCreateJournal
	var capturedOwner environmentCreateOwner
	base := managedLateAppCompletionBase(old, review)
	if !environmentPreflightDigest.MatchString(old) || !environmentPreflightDigest.MatchString(review) || createOpenBaoACKCompletionRead(store, filepath.Join(base, "qualification.json"), &a) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(base, "source-journal.json"), &captured) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(base, "source-owner.json"), &capturedOwner) != nil {
		return a, captured, appRootCRLError("completion-canonical-artifact")
	}
	if deploymentplan.Digest(a.Plan) != review || a.Plan.OriginalReview != old || !managedLateAppCompletionArtifactValid(a, original, source, captured, owner) || deploymentplan.Digest(capturedOwner) != deploymentplan.Digest(owner) {
		return a, captured, appRootCRLError("completion-authenticated-current-source")
	}
	return a, captured, nil
}
func managedLateAppCompletionArtifactValid(a managedLateAppCompletionArtifact, original createAppRootCRLArtifact, source, captured environmentCreateJournal, owner environmentCreateOwner) bool {
	p := a.Plan
	sha := deploymentplan.Digest(p)
	if a.Schema != managedLateAppCompletionSchema+"/artifact" || p.Schema != managedLateAppCompletionSchema || p.OriginalReview != deploymentplan.Digest(original.Plan) || p.OriginalArtifactSHA != deploymentplan.Digest(original) || p.Parent != original.Plan.Parent || p.ConfigurationSHA != original.Plan.ConfigurationSHA || p.KubeconfigSHA != original.Plan.KubeconfigSHA || !environmentPreflightDigest.MatchString(p.CheckerSHA) || p.SourceJournalSHA != deploymentplan.Digest(captured) || p.OwnerSHA != deploymentplan.Digest(owner) || owner.PlanSHA256 != captured.PlanSHA256 || owner.OwnerToken != captured.OwnerToken || owner.Environment != captured.Environment || owner.Stack != captured.Stack || owner.Schema != environmentCreateOwnerSchema || a.QualifiedAt.IsZero() || !a.QualifiedAt.Equal(p.QualifiedAt) || a.QualifiedAt.After(time.Now().UTC()) || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.Qualification.Decision != "GO" || a.Qualification.PlanSHA256 != sha || len(a.Qualification.Gates) != 6 || !managedLateAppCompletionSourceValid(original, source, captured) || !managedLateAppCompletionMembership(original, source, captured, captured, p.NativeEvidence) {
		return false
	}
	if p.Lease != captured.KubernetesLeases[len(source.KubernetesLeases)] || p.LeaseObject == nil || validateManagedApplicationInterruptedLease(rootCRLLeasePlan(original.Plan.Configuration, p.OriginalReview), p.Lease, p.LeaseObject) != nil {
		return false
	}
	for i, id := range managedLateAppCompletionGates {
		g := a.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != sha {
			return false
		}
	}
	return true
}
func executeManagedLateAppCompletion(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, original createAppRootCRLArtifact, source environmentCreateJournal, a managedLateAppCompletionArtifact, captured environmentCreateJournal, out io.Writer) error {
	review := deploymentplan.Digest(a.Plan)
	base := managedLateAppCompletionBase(a.Plan.OriginalReview, review)
	started := time.Now().UTC()
	guard := func() error {
		if e.Check(ctx) != nil || managedLateInstallationClockAbsent(store, a.Plan.Parent) != nil {
			return appRootCRLError("completion-original-fence-and-no-clock")
		}
		owner, _, err := observeManagedLateAppCompletionLease(ctx, e, b, original, source)
		if err != nil || owner != a.Plan.Lease {
			return appRootCRLError("completion-original-lease-changed")
		}
		return nil
	}
	if err := guard(); err != nil {
		return err
	}
	evidence, err := observeManagedLateAppCompletion(ctx, cfg, store, e, b, operator, original, source)
	if err != nil {
		return err
	}
	if deploymentplan.Digest(evidence) != deploymentplan.Digest(a.Plan.NativeEvidence) || !managedLateAppCompletionMembership(original, source, captured, e.Journal, evidence) {
		return appRootCRLError("completion-current-source-or-evidence-changed")
	}
	// Fresh authority admits only the following local proof writes. Re-read
	// the named immutable artifact after all potentially expensive native reads.
	fresh, freshSource, freshErr := loadManagedLateAppCompletionArtifact(store, a.Plan.OriginalReview, review, original, source, e.owner)
	if freshErr != nil || deploymentplan.Digest(fresh) != deploymentplan.Digest(a) || deploymentplan.Digest(freshSource) != deploymentplan.Digest(captured) || !time.Now().UTC().Before(fresh.ValidUntil) || guard() != nil {
		return appRootCRLError("completion-immediate-fresh-sixGO")
	}
	key := "app-root-crl-renewal-" + a.Plan.OriginalReview
	for _, target := range original.Plan.Consumers {
		if err := guard(); err != nil {
			return err
		}
		stage := key + "/native-runtime-" + deploymentplan.String(deploymentplan.Map(target.Desired["metadata"])["name"])
		if err := rootCRLStepBegin(e, stage, appRootCRLRuntimeIntent(original.Plan, target)); err != nil {
			return err
		}
		if err := rootCRLStepComplete(e, stage, managedLateAppCompletionEvidenceForTarget(original, evidence, target)); err != nil {
			return err
		}
		if !managedLateAppCompletionMembership(original, source, captured, e.Journal, evidence) {
			return appRootCRLError("completion-monotonic-consumer-proof")
		}
	}
	if err := guard(); err != nil {
		return err
	}
	if err := rootCRLStepComplete(e, key, appRootCRLReceipt(original, evidence)); err != nil {
		return err
	}
	if !managedLateAppCompletionMembership(original, source, captured, e.Journal, evidence) || !managedLateAppRootCRLProgressValid(original, source, e.Journal, true) {
		return appRootCRLError("completion-real-original-four-tail")
	}
	proof := managedLateAppCompletionExecution{Schema: managedLateAppCompletionSchema + "/completion", Review: review, OriginalReview: a.Plan.OriginalReview, SourceSHA: a.Plan.SourceJournalSHA, FinalSHA: deploymentplan.Digest(e.Journal), EvidenceSHA: deploymentplan.Digest(evidence), StartedAt: started, CompletedAt: time.Now().UTC()}
	path := filepath.Join(base, "completed.json")
	if _, statErr := os.Lstat(filepath.Join(store.Root, path)); statErr == nil {
		var saved managedLateAppCompletionExecution
		if createOpenBaoACKCompletionRead(store, path, &saved) != nil || saved.Schema != proof.Schema || saved.Review != proof.Review || saved.OriginalReview != proof.OriginalReview || saved.SourceSHA != proof.SourceSHA || saved.FinalSHA != proof.FinalSHA || saved.EvidenceSHA != proof.EvidenceSHA || saved.StartedAt.Before(a.QualifiedAt) || !saved.StartedAt.Before(a.ValidUntil) || saved.CompletedAt.Before(saved.StartedAt) {
			return appRootCRLError("completion-existing-sidecar-drift")
		}
	} else if !os.IsNotExist(statErr) {
		return statErr
	} else if err := writeMQTTRootCRLImmutable(store, path, proof); err != nil {
		return err
	}
	completion := createAppRootCRLCompletion{Schema: createAppRootCRLSchema + "/completion", Parent: original.Plan.Parent, Review: a.Plan.OriginalReview, SourceSHA: deploymentplan.Digest(source), FinalSHA: deploymentplan.Digest(e.Journal), EvidenceSHA: deploymentplan.Digest(evidence), SourceEntryCount: len(source.Entries), FinalEntryCount: len(e.Journal.Entries), Completed: true, NativeEvidence: evidence, CompletionReview: review}
	// A genuine existing mirror is immutable even when only its pointer is missing.
	mirror := filepath.Join(appRootCRLBase(a.Plan.OriginalReview), "completed.json")
	if _, statErr := os.Lstat(filepath.Join(store.Root, mirror)); statErr == nil {
		var retained createAppRootCRLCompletion
		if createOpenBaoACKCompletionRead(store, mirror, &retained) != nil || retained.Parent != completion.Parent || retained.Review != completion.Review || retained.FinalSHA != completion.FinalSHA || retained.EvidenceSHA != completion.EvidenceSHA || validateManagedLateAppCompletionProof(store, original, source, retained, e.Journal, e.owner) != nil {
			return appRootCRLError("completion-existing-mirror-drift")
		}
		completion = retained
	} else if !os.IsNotExist(statErr) {
		return statErr
	}
	if err := guard(); err != nil {
		return err
	}
	for name, value := range map[string]any{"final-journal.json": e.Journal, "completed.json": completion} {
		if err := writeMQTTRootCRLImmutable(store, filepath.Join(appRootCRLBase(a.Plan.OriginalReview), name), value); err != nil {
			return err
		}
	}
	if err := guard(); err != nil {
		return err
	}
	if err := writeMQTTRootCRLImmutable(store, appRootCRLPointer(original.Plan.Parent), completion); err != nil {
		return err
	}
	if err := validateManagedLateAppRootCRLCompletion(store, original, source, e.Journal); err != nil {
		return err
	}
	fmt.Fprintf(out, "PASS environment=dev scope=original-app-root-crl-completion original-review-sha256=%s review-sha256=%s native-writes=false original-lease-retained=true\n", a.Plan.OriginalReview, review)
	return nil
}
func validateManagedLateAppCompletionProof(store secretStore, original createAppRootCRLArtifact, source environmentCreateJournal, completion createAppRootCRLCompletion, final environmentCreateJournal, owner environmentCreateOwner) error {
	if !environmentPreflightDigest.MatchString(completion.CompletionReview) {
		return appRootCRLError("completion-sidecar-reference")
	}
	a, captured, err := loadManagedLateAppCompletionArtifact(store, completion.Review, completion.CompletionReview, original, source, owner)
	if err != nil {
		return err
	}
	if deploymentplan.Digest(completion.NativeEvidence) != deploymentplan.Digest(a.Plan.NativeEvidence) {
		return appRootCRLError("completion-qualified-native-evidence")
	}
	var proof managedLateAppCompletionExecution
	if createOpenBaoACKCompletionRead(store, filepath.Join(managedLateAppCompletionBase(completion.Review, completion.CompletionReview), "completed.json"), &proof) != nil || proof.Schema != managedLateAppCompletionSchema+"/completion" || proof.Review != completion.CompletionReview || proof.OriginalReview != completion.Review || proof.SourceSHA != a.Plan.SourceJournalSHA || proof.FinalSHA != deploymentplan.Digest(final) || proof.EvidenceSHA != deploymentplan.Digest(completion.NativeEvidence) || proof.StartedAt.Before(a.QualifiedAt) || !proof.StartedAt.Before(a.ValidUntil) || proof.CompletedAt.Before(proof.StartedAt) || proof.CompletedAt.After(time.Now().UTC()) || !managedLateAppCompletionMembership(original, source, captured, final, completion.NativeEvidence) {
		return appRootCRLError("completion-authentic-final-sidecar")
	}
	for i := len(source.Entries); i < len(final.Entries); i++ {
		if i == len(source.Entries)+1 {
			continue
		}
		if i < len(captured.Entries) && deploymentplan.Digest(captured.Entries[i]) == deploymentplan.Digest(final.Entries[i]) {
			continue
		}
		entry := final.Entries[i]
		if entry.CompletedAt.Before(proof.StartedAt) || entry.CompletedAt.After(proof.CompletedAt) || i >= len(captured.Entries) && entry.StartedAt.Before(proof.StartedAt) {
			return appRootCRLError("completion-fresh-local-closure-times")
		}
	}
	return nil
}
