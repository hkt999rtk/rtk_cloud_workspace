package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const mqttRootCRLCompletionRecoverySchema = "rtk-cloud.create-mqtt-root-crl-completion-recovery/v1"

var mqttRootCRLCompletionRecoveryGates = []string{"deployment.mqtt-root-crl.completion-original-authority", "deployment.mqtt-root-crl.completion-recorded-import", "deployment.mqtt-root-crl.completion-current-native-two", "deployment.mqtt-root-crl.completion-current-consumers", "deployment.mqtt-root-crl.completion-preserved-identities", "deployment.mqtt-root-crl.completion-execution-lease"}

type mqttRootCRLCompletionRecoveryPlan struct {
	Schema, Parent, OriginalReview, OriginalArtifactSHA                     string
	SourceJournalSHA, OwnerSHA, ConfigurationSHA, KubeconfigSHA, CheckerSHA string
	NativeEvidence                                                          map[string]string
	QualifiedAt                                                             time.Time
	ExistingCompletionSHA                                                   string `json:",omitempty"`
}
type mqttRootCRLCompletionRecoveryArtifact struct {
	Schema                  string
	Plan                    mqttRootCRLCompletionRecoveryPlan
	Qualification           deploymentplan.Qualification
	QualifiedAt, ValidUntil time.Time
}
type mqttRootCRLCompletionRecoveryExecution struct {
	Schema, Review, OriginalReview, SourceJournalSHA, FinalJournalSHA, NativeEvidenceSHA string
	Lease                                                                                deploymentplan.LeaseOwner
	StartedAt, CompletedAt                                                               time.Time
}

func mqttRootCRLCompletionRecoveryBase(old, review string) string {
	return filepath.Join(mqttRootCRLBase(old), "completion-only", review)
}

// Selection itself grants no rights; the profile's strong helper reads the old
// immutable qualification and validates every exact native child/owner/lease.
func mqttRootCRLCompletionCheckpointReview(j environmentCreateJournal) string {
	if len(j.Entries) < 4 || !j.CompletedAt.IsZero() {
		return ""
	}
	tail := j.Entries[len(j.Entries)-4:]
	review := strings.TrimPrefix(tail[0].Key, "mqtt-root-crl-renewal-")
	if !environmentPreflightDigest.MatchString(review) || tail[0].Key != "mqtt-root-crl-renewal-"+review {
		return ""
	}
	for i, suffix := range []string{"", "/native-import", "/native-runtime-video-cloud-api", "/native-runtime-video-cloud-logingester"} {
		entry := tail[i]
		if entry.Key != tail[0].Key+suffix || entry.Phase != "bootstrap" || i > 0 && (entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0) {
			return ""
		}
	}
	return review
}
func mqttRootCRLCompletionNativeCheckpoint(store secretStore, e *environmentCreateExecution) bool {
	if e == nil {
		return false
	}
	review := mqttRootCRLCompletionCheckpointReview(e.Journal)
	if review == "" {
		return false
	}
	var saved createMQTTRootCRLArtifact
	if createOpenBaoACKCompletionRead(store, filepath.Join(mqttRootCRLBase(review), "qualification.json"), &saved) != nil {
		return false
	}
	a, source, err := loadCreateMQTTRootCRLArtifact(saved.Plan.Configuration, store, e, review)
	return err == nil && mqttRootCRLCompletionSourceValid(a, source, e.Journal)
}

func runEnvironmentCreateMQTTRootCRLCompletion(args []string) error {
	return runEnvironmentCreateMQTTRootCRLCompletionContext(context.Background(), args, os.Stdout, os.Stderr)
}
func runEnvironmentCreateMQTTRootCRLCompletionContext(parentContext context.Context, args []string, out, diagnostics io.Writer) error {
	fs := flag.NewFlagSet("recover-create-mqtt-root-crl-completion", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	env := fs.String("environment", "", "selected dev")
	parent := fs.String("parent-plan-sha256", "", "original generation")
	old := fs.String("original-review-sha256", "", "original admitted MQTT review")
	workspaceOpt := fs.String("workspace", "", "original logical workspace")
	execute := fs.Bool("execute", false, "complete exact observed original MQTT receipt only")
	review := fs.String("review-sha256", "", "fresh completion-only review")
	confirm := fs.String("confirm", "", "original stack")
	timeout := fs.Duration("timeout", 15*time.Minute, "complete bounded observation")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *env != "dev" || !environmentPreflightDigest.MatchString(*parent) || !environmentPreflightDigest.MatchString(*old) || *timeout <= 0 || *timeout > 30*time.Minute || !*execute && *review != "" {
		return mqttRootCRLError("completion-arguments")
	}
	if err := recoveryMutationGuard(append([]string{"deployment", "recover-create-mqtt-root-crl-completion"}, args...)); err != nil {
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
	ctx, cancel := context.WithTimeout(parentContext, *timeout)
	defer cancel()
	profile := make([]bool, 12)
	for i := range profile {
		profile[i] = true
	}
	e, generation, _, _, err := loadInitializedCreateAudit(cfg, store, *parent, false, false, false, false, false, profile...)
	if err != nil {
		return err
	}
	defer e.Close()
	if deploymentplan.Digest(operator) != generation.Plan.Plan.OperatorSHA256 {
		return mqttRootCRLError("completion-original-operator")
	}
	kube, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || environmentPreflightBytesSHA256(kube) != generation.Plan.KubeconfigSHA256 {
		return mqttRootCRLError("completion-canonical-kubeconfig")
	}
	b, err := newEnvironmentCreateCRLBackend(string(kube), *env, cfg.Values)
	if err != nil {
		return err
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return err
	}
	original, originalSource, err := loadCreateMQTTRootCRLArtifact(cfg, store, e, *old)
	if err != nil {
		return err
	}
	if !mqttRootCRLCompletionSourceValid(original, originalSource, e.Journal) || original.Plan.ConfigurationSHA != deploymentplan.Digest(cfg) || original.Plan.KubeconfigSHA != environmentPreflightBytesSHA256(kube) {
		return mqttRootCRLError("completion-exact-original-progress")
	}
	if !*execute {
		if completed, _, err := readCreateMQTTRootCRLRecovery(store, *parent); err != nil {
			return err
		} else if completed != nil {
			return mqttRootCRLError("completion-already-published")
		}
		existing, err := readMQTTRootCRLCompletionMirror(store, e, original, originalSource)
		if err != nil {
			return err
		}
		evidence, err := observeMQTTRootCRLCompletion(ctx, cfg, store, e, b, original, originalSource)
		if err != nil {
			return err
		}
		now := time.Now().UTC()
		plan := mqttRootCRLCompletionRecoveryPlan{Schema: mqttRootCRLCompletionRecoverySchema, Parent: *parent, OriginalReview: *old, OriginalArtifactSHA: deploymentplan.Digest(original), SourceJournalSHA: deploymentplan.Digest(e.Journal), OwnerSHA: deploymentplan.Digest(e.owner), ConfigurationSHA: deploymentplan.Digest(cfg), KubeconfigSHA: environmentPreflightBytesSHA256(kube), CheckerSHA: checker, NativeEvidence: evidence, QualifiedAt: now}
		if existing != nil {
			plan.ExistingCompletionSHA = deploymentplan.Digest(existing)
		}
		sha := deploymentplan.Digest(plan)
		if err := checkCreateRootCRLLeaseAvailable(ctx, e, b, rootCRLLeasePlan(cfg, sha)); err != nil {
			return err
		}
		a := mqttRootCRLCompletionRecoveryArtifact{Schema: mqttRootCRLCompletionRecoverySchema + "/artifact", Plan: plan, Qualification: deploymentplan.Qualification{PlanSHA256: sha, Decision: "GO"}, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
		for _, id := range mqttRootCRLCompletionRecoveryGates {
			a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: sha})
		}
		base := mqttRootCRLCompletionRecoveryBase(*old, sha)
		for name, value := range map[string]any{"qualification.json": a, "source-journal.json": e.Journal, "source-owner.json": e.owner} {
			if err := writeMQTTRootCRLImmutable(store, filepath.Join(base, name), value); err != nil {
				return err
			}
		}
		if err := e.Check(ctx); err != nil {
			return err
		}
		fmt.Fprintf(out, "GO environment=dev scope=original-mqtt-root-crl-completion original-review-sha256=%s review-sha256=%s native-writes=false\n", *old, sha)
		return nil
	}
	a, captured, err := loadMQTTRootCRLCompletionRecovery(store, *old, *review, original, originalSource, e.owner)
	if err != nil {
		return err
	}
	if *confirm != cfg.Values["CLOUD_STACK_NAME"] || a.Plan.CheckerSHA != checker || a.Plan.ConfigurationSHA != deploymentplan.Digest(cfg) || a.Plan.KubeconfigSHA != environmentPreflightBytesSHA256(kube) || !time.Now().Before(a.ValidUntil) || !mqttRootCRLCompletionMembership(original, originalSource, captured, e.Journal) {
		return mqttRootCRLError("completion-fresh-qualified-inputs")
	}
	return executeMQTTRootCRLCompletion(ctx, cfg, store, e, b, original, originalSource, a, captured, out)
}

func observeMQTTRootCRLCompletion(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, a createMQTTRootCRLArtifact, source environmentCreateJournal) (map[string]string, error) {
	recorded, err := mqttRootCRLCompletionRecordedEvidence(a, source, e.Journal)
	if err != nil {
		return nil, err
	}
	if err := verifyMQTTRootCRLInputs(ctx, cfg, store, e, b, a.Plan, true); err != nil {
		return nil, err
	}
	for _, target := range a.Plan.Consumers {
		actual, err := observeMQTTRootCRLConsumer(ctx, cfg, store, e, b, a, target)
		if err != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(mqttRootCRLEvidenceForTarget(recorded, target)) {
			return nil, mqttRootCRLError("completion-exact-current-native-evidence-" + target.ID)
		}
	}
	return recorded, e.Check(ctx)
}

func loadMQTTRootCRLCompletionRecovery(store secretStore, old, review string, original createMQTTRootCRLArtifact, originalSource environmentCreateJournal, owner environmentCreateOwner) (mqttRootCRLCompletionRecoveryArtifact, environmentCreateJournal, error) {
	var a mqttRootCRLCompletionRecoveryArtifact
	var source environmentCreateJournal
	var capturedOwner environmentCreateOwner
	base := mqttRootCRLCompletionRecoveryBase(old, review)
	if !environmentPreflightDigest.MatchString(old) || !environmentPreflightDigest.MatchString(review) || createOpenBaoACKCompletionRead(store, filepath.Join(base, "qualification.json"), &a) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(base, "source-journal.json"), &source) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(base, "source-owner.json"), &capturedOwner) != nil {
		return a, source, mqttRootCRLError("completion-canonical-artifact")
	}
	if a.Schema != mqttRootCRLCompletionRecoverySchema+"/artifact" || a.Plan.Schema != mqttRootCRLCompletionRecoverySchema || deploymentplan.Digest(a.Plan) != review || a.Plan.OriginalReview != old || a.Plan.OriginalArtifactSHA != deploymentplan.Digest(original) || a.Plan.Parent != original.Plan.Parent || a.Plan.ConfigurationSHA != original.Plan.ConfigurationSHA || a.Plan.KubeconfigSHA != original.Plan.KubeconfigSHA || !environmentPreflightDigest.MatchString(a.Plan.CheckerSHA) || a.Plan.ExistingCompletionSHA != "" && !environmentPreflightDigest.MatchString(a.Plan.ExistingCompletionSHA) || a.Plan.SourceJournalSHA != deploymentplan.Digest(source) || a.Plan.OwnerSHA != deploymentplan.Digest(owner) || deploymentplan.Digest(capturedOwner) != deploymentplan.Digest(owner) || owner.Schema != environmentCreateOwnerSchema || owner.PlanSHA256 != source.PlanSHA256 || owner.OwnerToken != source.OwnerToken || owner.Environment != source.Environment || owner.Stack != source.Stack || a.Qualification.Decision != "GO" || a.Qualification.PlanSHA256 != review || len(a.Qualification.Gates) != len(mqttRootCRLCompletionRecoveryGates) || a.QualifiedAt.IsZero() || !a.QualifiedAt.Equal(a.Plan.QualifiedAt) || a.QualifiedAt.After(time.Now().UTC()) || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || !mqttRootCRLCompletionSourceValid(original, originalSource, source) {
		return a, source, mqttRootCRLError("completion-exact-qualified-source")
	}
	for i, id := range mqttRootCRLCompletionRecoveryGates {
		g := a.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != review {
			return a, source, mqttRootCRLError("completion-required-gates")
		}
	}
	evidence, err := mqttRootCRLCompletionRecordedEvidence(original, originalSource, source)
	if err != nil || deploymentplan.Digest(evidence) != deploymentplan.Digest(a.Plan.NativeEvidence) {
		return a, source, mqttRootCRLError("completion-preserved-child-evidence")
	}
	return a, source, nil
}

// The fresh distributed lease is immutable sidecar evidence only. Parent
// entries/lease history keep the exact original four-step MQTT shape for TLS.
func executeMQTTRootCRLCompletion(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, original createMQTTRootCRLArtifact, originalSource environmentCreateJournal, a mqttRootCRLCompletionRecoveryArtifact, captured environmentCreateJournal, out io.Writer) (resultErr error) {
	review := deploymentplan.Digest(a.Plan)
	base := mqttRootCRLCompletionRecoveryBase(a.Plan.OriginalReview, review)
	started := time.Now().UTC()
	lease, err := b.AcquireExecution(ctx, rootCRLLeasePlan(cfg, review))
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resultErr = errors.Join(resultErr, lease.Release(cleanup))
	}()
	// Attempts remain immutable if an earlier completion was interrupted.
	leaseFile := filepath.Join(base, "execution-leases", deploymentplan.Digest(lease.Owner()), "lease.json")
	if err := writeMQTTRootCRLImmutable(store, leaseFile, lease.Owner()); err != nil {
		return err
	}
	guard := func() error {
		if err := e.Check(ctx); err != nil {
			return err
		}
		return lease.Check(ctx)
	}
	if err := guard(); err != nil {
		return err
	}
	evidence, err := observeMQTTRootCRLCompletion(ctx, cfg, store, e, b, original, originalSource)
	if err != nil || deploymentplan.Digest(evidence) != deploymentplan.Digest(a.Plan.NativeEvidence) {
		if err != nil {
			return err
		}
		return mqttRootCRLError("completion-current-evidence-changed")
	}
	if err := guard(); err != nil {
		return err
	}
	if !mqttRootCRLCompletionMembership(original, originalSource, captured, e.Journal) {
		return mqttRootCRLError("completion-current-prefix-changed")
	}
	receipt := createMQTTRootCRLReceipt{Schema: createMQTTRootCRLSchema, Parent: original.Plan.Parent, Review: a.Plan.OriginalReview, File: filepath.ToSlash(filepath.Join(original.Plan.Candidate, "signed/revocations.pem")), Digest: original.Plan.Signed.Digest, NativeEvidenceSHA: deploymentplan.Digest(evidence), Completed: true}
	if err := rootCRLStepComplete(e, "mqtt-root-crl-renewal-"+a.Plan.OriginalReview, receipt); err != nil {
		return err
	}
	if err := guard(); err != nil {
		return err
	}
	if !mqttRootCRLCompletionMembership(original, originalSource, captured, e.Journal) {
		return mqttRootCRLError("completion-monotonic-original-receipt")
	}
	completed := mqttRootCRLCompletionRecoveryExecution{Schema: mqttRootCRLCompletionRecoverySchema + "/completion", Review: review, OriginalReview: a.Plan.OriginalReview, SourceJournalSHA: a.Plan.SourceJournalSHA, FinalJournalSHA: deploymentplan.Digest(e.Journal), NativeEvidenceSHA: deploymentplan.Digest(evidence), Lease: lease.Owner(), StartedAt: started, CompletedAt: time.Now().UTC()}
	// A new fresh qualification may publish a missing pointer after a completed
	// top; it never changes that original completion timestamp or child receipts.
	completionPath := filepath.Join(base, "completed.json")
	if path, err := store.safePath(completionPath); err != nil {
		return err
	} else if _, err := os.Lstat(path); err == nil {
		var retained mqttRootCRLCompletionRecoveryExecution
		if createOpenBaoACKCompletionRead(store, completionPath, &retained) != nil || retained.Schema != completed.Schema || retained.Review != completed.Review || retained.OriginalReview != completed.OriginalReview || retained.SourceJournalSHA != completed.SourceJournalSHA || retained.FinalJournalSHA != completed.FinalJournalSHA || retained.NativeEvidenceSHA != completed.NativeEvidenceSHA || retained.StartedAt.Before(a.QualifiedAt) || !retained.StartedAt.Before(a.ValidUntil) || retained.CompletedAt.Before(retained.StartedAt) {
			return mqttRootCRLError("completion-existing-sidecar-drift")
		}
	} else if !os.IsNotExist(err) {
		return err
	} else if err := writeMQTTRootCRLImmutable(store, completionPath, completed); err != nil {
		return err
	}
	existing, err := readMQTTRootCRLCompletionMirror(store, e, original, originalSource)
	if err != nil {
		return err
	}
	if existing != nil {
		if a.Plan.ExistingCompletionSHA != "" && deploymentplan.Digest(existing) != a.Plan.ExistingCompletionSHA {
			return mqttRootCRLError("completion-existing-mirror-changed")
		}
		if err := guard(); err != nil {
			return err
		}
		if err := writeMQTTRootCRLImmutable(store, filepath.Join("deployment/create/material", original.Plan.Parent, "mqtt-root-crl-renewal.json"), existing); err != nil {
			return err
		}
		fmt.Fprintf(out, "PASS environment=dev scope=original-mqtt-root-crl-completion original-review-sha256=%s pointer-restored=true\n", a.Plan.OriginalReview)
		return nil
	}
	if a.Plan.ExistingCompletionSHA != "" {
		return mqttRootCRLError("completion-admitted-mirror-missing")
	}
	if err := guard(); err != nil {
		return err
	}
	if err := publishCreateMQTTRootCRLCompletion(ctx, store, e, original, originalSource, evidence, out, review); err != nil {
		return err
	}
	return nil
}

// Historical consumption checks the fresh checker/source/lease authority without
// requiring it to equal the old import checker's hash or today's binary hash.
func validateMQTTRootCRLCompletionRecoveryProof(store secretStore, original createMQTTRootCRLArtifact, originalSource environmentCreateJournal, completion createMQTTRootCRLCompletion, final environmentCreateJournal, owner environmentCreateOwner) error {
	if !environmentPreflightDigest.MatchString(completion.CompletionReview) || completion.Review != deploymentplan.Digest(original.Plan) {
		return mqttRootCRLError("completion-only-sidecar-reference")
	}
	a, captured, err := loadMQTTRootCRLCompletionRecovery(store, completion.Review, completion.CompletionReview, original, originalSource, owner)
	if err != nil {
		return err
	}
	base := mqttRootCRLCompletionRecoveryBase(completion.Review, completion.CompletionReview)
	var proof mqttRootCRLCompletionRecoveryExecution
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, "completed.json"), &proof) != nil || proof.Schema != mqttRootCRLCompletionRecoverySchema+"/completion" || proof.Review != completion.CompletionReview || proof.OriginalReview != completion.Review || proof.SourceJournalSHA != a.Plan.SourceJournalSHA || proof.FinalJournalSHA != deploymentplan.Digest(final) || proof.NativeEvidenceSHA != deploymentplan.Digest(completion.NativeEvidence) || proof.StartedAt.Before(a.QualifiedAt) || !proof.StartedAt.Before(a.ValidUntil) || proof.CompletedAt.Before(proof.StartedAt) || proof.CompletedAt.After(time.Now().UTC()) || !mqttRootCRLCompletionMembership(original, originalSource, captured, final) || !environmentPreflightDigest.MatchString(a.Plan.CheckerSHA) {
		return mqttRootCRLError("completion-only-authenticated-final-proof")
	}
	plan := rootCRLLeasePlan(original.Plan.Configuration, completion.CompletionReview)
	if proof.Lease.ResourceID != "Lease/"+originalSource.Stack+"-video-cloud/rtk-deployment-execution" || !environmentBootstrapOwnerUUID.MatchString(proof.Lease.UID) || !strings.HasPrefix(proof.Lease.Holder, plan.SHA256()+":") {
		return mqttRootCRLError("completion-only-reviewed-lease")
	}
	var lease deploymentplan.LeaseOwner
	path := filepath.Join(base, "execution-leases", deploymentplan.Digest(proof.Lease), "lease.json")
	if createOpenBaoACKCompletionRead(store, path, &lease) != nil || lease != proof.Lease {
		return mqttRootCRLError("completion-only-immutable-lease-evidence")
	}
	top := final.Entries[len(originalSource.Entries)]
	capturedTop := captured.Entries[len(originalSource.Entries)]
	if capturedTop.State != "completed" && (top.CompletedAt.Before(proof.StartedAt) || top.CompletedAt.After(proof.CompletedAt)) {
		return mqttRootCRLError("completion-only-original-top-time")
	}
	return nil
}

// Only this completion-only adapter may recover a genuine mirror without its
// pointer. The ordinary global reader keeps rejecting malformed pointers.
func readMQTTRootCRLCompletionMirror(store secretStore, e *environmentCreateExecution, original createMQTTRootCRLArtifact, source environmentCreateJournal) (*createMQTTRootCRLCompletion, error) {
	review := deploymentplan.Digest(original.Plan)
	base := mqttRootCRLBase(review)
	relative := filepath.Join(base, "completed.json")
	path, err := store.safePath(relative)
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var completion createMQTTRootCRLCompletion
	var final environmentCreateJournal
	if createOpenBaoACKCompletionRead(store, relative, &completion) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(base, "final-journal.json"), &final) != nil || completion.Schema != createMQTTRootCRLSchema+"/completion" || completion.Parent != original.Plan.Parent || completion.Review != review || !completion.Completed || completion.SourceEntryCount != len(source.Entries) || completion.FinalEntryCount != len(source.Entries)+4 || completion.FinalEntryCount != len(final.Entries) || completion.FinalJournalSHA != deploymentplan.Digest(final) || completion.FinalEntriesSHA != deploymentplan.Digest(final.Entries) || deploymentplan.Digest(final) != deploymentplan.Digest(e.Journal) || !mqttRootCRLCompletionSourceValid(original, source, final) {
		return nil, mqttRootCRLError("completion-only-original-mirror-drift")
	}
	evidence, err := mqttRootCRLCompletionRecordedEvidence(original, source, final)
	if err != nil || deploymentplan.Digest(evidence) != deploymentplan.Digest(completion.NativeEvidence) || completion.Receipt.NativeEvidenceSHA != deploymentplan.Digest(evidence) || completion.Receipt.Review != review || completion.Receipt.Digest != original.Plan.Signed.Digest || !completion.Receipt.Completed {
		return nil, mqttRootCRLError("completion-only-original-mirror-evidence")
	}
	if completion.CompletionReview != "" {
		if err := validateMQTTRootCRLCompletionRecoveryProof(store, original, source, completion, final, e.owner); err != nil {
			return nil, err
		}
	}
	return &completion, nil
}
