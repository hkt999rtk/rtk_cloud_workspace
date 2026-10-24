package main

import (
	"context"
	"encoding/json"
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

const createRootCRLInstalledSchema = "rtk-cloud.create-service-root-crl-installed/v1"

type createRootCRLInstalledPlan struct {
	Schema              string            `json:"schema"`
	Parent              string            `json:"parent_plan_sha256"`
	OriginalReview      string            `json:"original_review_sha256"`
	OriginalArtifactSHA string            `json:"original_artifact_sha256"`
	SourceJournalSHA    string            `json:"source_journal_sha256"`
	CheckerSHA          string            `json:"checker_sha256"`
	OwnerSHA            string            `json:"owner_sha256"`
	ConfigurationSHA    string            `json:"configuration_sha256"`
	KubeconfigSHA       string            `json:"kubeconfig_sha256"`
	Evidence            map[string]string `json:"evidence_sha256"`
}
type createRootCRLInstalledArtifact struct {
	Schema        string                       `json:"schema"`
	Plan          createRootCRLInstalledPlan   `json:"plan"`
	Qualification deploymentplan.Qualification `json:"qualification"`
	QualifiedAt   time.Time                    `json:"qualified_at"`
	ValidUntil    time.Time                    `json:"valid_until"`
}
type createRootCRLInstalledCompletion struct {
	Schema                string    `json:"schema"`
	Review                string    `json:"review_sha256"`
	OriginalReview        string    `json:"original_review_sha256"`
	FinalEntriesSHA       string    `json:"final_entries_sha256"`
	ProofSHA              string    `json:"original_proof_sha256"`
	EvidenceSHA           string    `json:"observed_evidence_sha256"`
	StartedAt             time.Time `json:"started_at"`
	CompletedAt           time.Time `json:"completed_at"`
	RuntimeReloadRequired bool      `json:"runtime_reload_required"`
}
type createRootCRLInstalledPointer struct {
	Review         string `json:"review_sha256"`
	OriginalReview string `json:"original_review_sha256"`
	CompletionSHA  string `json:"completion_sha256"`
}

var createRootCRLInstalledGates = []string{"deployment.root-crl-installed.original-admission", "deployment.root-crl-installed.current-authority", "deployment.root-crl-installed.original-owners", "deployment.root-crl-installed.signed-disk-state", "deployment.root-crl-installed.inputs", "deployment.root-crl-installed.execution-lease"}

func rootCRLInstalledBase(old, review string) string {
	return filepath.Join("deployment/create/root-crl-recovery", old, "installed-resume", review)
}

// Only the original admitted publication and Secret may be completed. Remaining
// native disk steps must retain their original exact intents and receipt shapes.
func rootCRLInstalledSourceValid(current environmentCreateJournal, original createRootCRLRecoveryArtifact, source environmentCreateJournal) bool {
	if !rootCRLJournalTailValid(current, original, source, false) || len(current.Entries) < len(source.Entries)+4 || len(current.Entries) > len(source.Entries)+5 {
		return false
	}
	tail := current.Entries[len(source.Entries):]
	if tail[0].State == "completed" {
		return len(tail) == 5 && rootCRLJournalTailValid(current, original, source, true)
	}
	return tail[0].State == "pending" && tail[1].State == "completed" && tail[2].State == "completed"
}

// Retries can advance only the two originally pinned disk receipts and the
// original top completion. Other journal changes or foreign lease owners fail.
func rootCRLInstalledProgressValid(source, current environmentCreateJournal, original createRootCRLRecoveryArtifact, originalSource environmentCreateJournal, review string) bool {
	if !rootCRLInstalledSourceValid(source, original, originalSource) || len(current.Entries) < len(source.Entries) || len(current.Entries) > len(originalSource.Entries)+5 || !rootCRLJournalTailValid(current, original, originalSource, false) {
		return false
	}
	for i, entry := range source.Entries {
		actual := current.Entries[i]
		if entry.Key != actual.Key || entry.Phase != actual.Phase || entry.IntentSHA256 != actual.IntentSHA256 || !entry.StartedAt.Equal(actual.StartedAt) || entry.State == "completed" && activeTransportJournalDigest(entry) != activeTransportJournalDigest(actual) {
			return false
		}
	}
	if len(current.KubernetesLeases) < len(source.KubernetesLeases) {
		return false
	}
	for i, lease := range source.KubernetesLeases {
		if lease != current.KubernetesLeases[i] {
			return false
		}
	}
	leasePlan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: source.Environment, Stack: source.Stack, Operation: "scoped", BindingsSHA256: review}}
	for _, lease := range current.KubernetesLeases[len(source.KubernetesLeases):] {
		if lease.ResourceID != "Lease/"+source.Stack+"-video-cloud/rtk-deployment-execution" || lease.UID == "" || !strings.HasPrefix(lease.Holder, leasePlan.SHA256()+":") {
			return false
		}
	}
	normalized := current
	normalized.Entries, normalized.KubernetesLeases = source.Entries, source.KubernetesLeases
	return deploymentplan.Digest(normalized) == deploymentplan.Digest(source)
}

func validateRootCRLInstalledState(raw []byte, root environmentCreateRegisteredIssuer, signed createRootCRLRecord) error {
	var state struct {
		Fingerprint string              `json:"issuer_fingerprint"`
		CRL         createRootCRLRecord `json:"crl"`
		Terminal    string              `json:"terminal_issuer_id"`
		Retained    json.RawMessage     `json:"retained_crl"`
	}
	if json.Unmarshal(raw, &state) != nil || state.Fingerprint != root.RootFingerprint || state.Terminal != "" || len(state.Retained) != 0 || deploymentplan.Digest(state.CRL) != deploymentplan.Digest(signed) {
		return rootCRLRecoveryError()
	}
	if _, err := parseCreateRootCRL(root, state.CRL, true, time.Now().UTC()); err != nil {
		return err
	}
	return nil
}

func observeCreateRootCRLInstalled(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, original createRootCRLRecoveryArtifact, generation environmentCreatePlan) (map[string]string, error) {
	plan := original.Plan
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, plan.Candidate, "signed/revocations.pem"), true)
	if err != nil {
		return nil, err
	}
	signed, err := validateCreateRootCRLSigned(plan.Root, plan.Snapshot, plan.Request, raw, time.Now().UTC())
	if err != nil || deploymentplan.Digest(signed) != deploymentplan.Digest(plan.Signed) {
		return nil, rootCRLRecoveryError()
	}
	material, err := createRootCRLMaterial(store, plan.Parent, plan.Root)
	if err != nil {
		return nil, err
	}
	for path, hash := range material {
		if plan.Material[path] != hash {
			return nil, rootCRLRecoveryError()
		}
	}
	registry := createFoundationRecoveryPlan{Plan: generation, Materials: map[string]string{}}
	if err := verifyManagedTransportNativeRegistry(ctx, cfg, store, e, b, &registry); err != nil {
		return nil, err
	}
	for key, hash := range plan.Material {
		if strings.HasPrefix(key, "evidence/") && registry.Materials[strings.TrimPrefix(key, "evidence/")] != hash {
			return nil, rootCRLRecoveryError()
		}
	}
	current, native, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, plan.Root.ID)
	if err != nil || deploymentplan.Digest(current.Prior) != deploymentplan.Digest(plan.Signed) {
		return nil, rootCRLRecoveryError()
	}
	current.Prior = plan.Snapshot.Prior
	if deploymentplan.Digest(current) != deploymentplan.Digest(plan.Snapshot) {
		return nil, rootCRLRecoveryError()
	}
	secret, err := b.Read(ctx, plan.Secret.ID)
	if err != nil || secret == nil || deploymentplan.Map(secret["metadata"])["uid"] != plan.Secret.UID || !deploymentplan.Matches(plan.Secret.Desired, secret) {
		return nil, rootCRLRecoveryError()
	}
	evidence := map[string]string{"signed-file": environmentPreflightBytesSHA256(raw), "native-publication": environmentPreflightBytesSHA256(native), "original-secret": deploymentplan.Digest(plan.Secret.Desired), "original-secret-owner": deploymentplan.Digest(plan.Secret.UID), "root-custody": deploymentplan.Digest(material), "native-registry": deploymentplan.Digest(registry.Materials)}
	if len(plan.Targets) != 2 {
		return nil, rootCRLRecoveryError()
	}
	for _, target := range plan.Targets {
		if err := verifyCreateRootCRLTargetOwners(ctx, cfg, b, target); err != nil {
			return nil, err
		}
		if len(target.Paths) != 2 {
			return nil, rootCRLRecoveryError()
		}
		for _, path := range target.Paths {
			raw, err := b.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud", "exec", "pod/"+target.PodName, "--container", target.Container, "--", "/bin/sh", "-ec", `test -f "$1" && test ! -L "$1" && test "$(stat -c %a "$1")" = 600 && test "$(stat -c %u "$1")" = 10001 && cat "$1"`, "root-crl-installed-read", path)
			if err != nil {
				return nil, rootCRLRecoveryError()
			}
			if err := validateRootCRLInstalledState(raw, plan.Root, plan.Signed); err != nil {
				return nil, err
			}
			evidence[target.ID+":"+path] = environmentPreflightBytesSHA256(raw)
		}
		evidence[target.ID+":owner"] = deploymentplan.Digest(target)
	}
	return evidence, e.Check(ctx)
}

func loadCreateRootCRLInstalledArtifact(store secretStore, old, review string) (createRootCRLInstalledArtifact, environmentCreateJournal, error) {
	var a createRootCRLInstalledArtifact
	var source environmentCreateJournal
	base := filepath.Join(store.Root, rootCRLInstalledBase(old, review))
	if !environmentPreflightDigest.MatchString(old) || !environmentPreflightDigest.MatchString(review) || readExecutionJSON(filepath.Join(base, "qualification.json"), &a) != nil || readExecutionJSON(filepath.Join(base, "source-journal.json"), &source) != nil || a.Schema != createRootCRLInstalledSchema+"/artifact" || a.Plan.Schema != createRootCRLInstalledSchema || a.Plan.OriginalReview != old || deploymentplan.Digest(a.Plan) != review || deploymentplan.Digest(source) != a.Plan.SourceJournalSHA || a.Plan.Parent != source.PlanSHA256 || a.Qualification.PlanSHA256 != review || a.Qualification.Decision != "GO" || len(a.Qualification.Gates) != len(createRootCRLInstalledGates) || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.QualifiedAt.After(time.Now()) {
		return a, source, rootCRLRecoveryError()
	}
	for i, id := range createRootCRLInstalledGates {
		g := a.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != review {
			return a, source, rootCRLRecoveryError()
		}
	}
	return a, source, nil
}

func runEnvironmentCreateRootCRLInstalled(args []string) error {
	return runEnvironmentCreateRootCRLInstalledContext(context.Background(), args, os.Stdout, os.Stderr)
}
func runEnvironmentCreateRootCRLInstalledContext(parentContext context.Context, args []string, out, diagnostics io.Writer) error {
	fs := flag.NewFlagSet("recover-create-service-root-crl-installed", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	env := fs.String("environment", "", "selected dev")
	parent := fs.String("parent-plan-sha256", "", "original generation")
	workspaceOpt := fs.String("workspace", "", "original logical workspace")
	old := fs.String("original-review-sha256", "", "original admitted Root CRL review")
	review := fs.String("review-sha256", "", "fresh installed-state review")
	execute := fs.Bool("execute", false, "record genuinely observed native disk installation")
	confirm := fs.String("confirm", "", "exact stack")
	timeout := fs.Duration("timeout", 15*time.Minute, "bounded timeout")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *env != "dev" || !environmentPreflightDigest.MatchString(*parent) || !environmentPreflightDigest.MatchString(*old) || *timeout <= 0 || *timeout > 30*time.Minute || !*execute && *review != "" {
		return rootCRLRecoveryError()
	}
	if recoveryMutationGuard(append([]string{"deployment", "recover-create-service-root-crl-installed"}, args...)) != nil {
		return rootCRLRecoveryError()
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
	e, generation, err := loadActiveTransportExecution(cfg, store, *parent)
	if err != nil {
		return err
	}
	defer e.Close()
	if deploymentplan.Digest(operator) != generation.Plan.Plan.OperatorSHA256 {
		return rootCRLRecoveryError()
	}
	kube, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || environmentPreflightBytesSHA256(kube) != generation.Plan.KubeconfigSHA256 {
		return rootCRLRecoveryError()
	}
	b, err := newEnvironmentKubernetesBackend(string(kube), *env)
	if err != nil {
		return err
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return err
	}
	original, originalSource, err := loadCreateRootCRLArtifact(store, e, *old)
	if err != nil {
		return err
	}
	if original.Plan.ConfigurationSHA != deploymentplan.Digest(cfg) || original.Plan.KubeconfigSHA != environmentPreflightBytesSHA256(kube) || original.Plan.Image != generation.Plan.Plan.Bootstrap.Image || !*execute && !rootCRLInstalledSourceValid(e.Journal, original, originalSource) {
		return rootCRLRecoveryError()
	}
	if !*execute {
		if _, err := os.Lstat(filepath.Join(store.Root, "deployment/create/material", *parent, "service-root-crl-renewal.json")); err == nil {
			return createProviderRecoveryBlock("original Root CRL proof already exists; consume its completed receipt without replay")
		} else if !os.IsNotExist(err) {
			return err
		}
		evidence, err := observeCreateRootCRLInstalled(ctx, cfg, store, e, b, original, generation.Plan.Plan)
		if err != nil {
			return err
		}
		plan := createRootCRLInstalledPlan{Schema: createRootCRLInstalledSchema, Parent: *parent, OriginalReview: *old, OriginalArtifactSHA: deploymentplan.Digest(original), SourceJournalSHA: deploymentplan.Digest(e.Journal), CheckerSHA: checker, OwnerSHA: deploymentplan.Digest(e.owner), ConfigurationSHA: deploymentplan.Digest(cfg), KubeconfigSHA: environmentPreflightBytesSHA256(kube), Evidence: evidence}
		sha := deploymentplan.Digest(plan)
		if err := checkCreateRootCRLLeaseAvailable(ctx, e, b, rootCRLLeasePlan(cfg, sha)); err != nil {
			return err
		}
		now := time.Now().UTC()
		a := createRootCRLInstalledArtifact{Schema: createRootCRLInstalledSchema + "/artifact", Plan: plan, Qualification: deploymentplan.Qualification{PlanSHA256: sha, Decision: "GO"}, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
		for _, id := range createRootCRLInstalledGates {
			a.Qualification.Gates = append(a.Qualification.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: sha})
		}
		base := rootCRLInstalledBase(*old, sha)
		if err := store.write(filepath.Join(base, "source-journal.json"), mustProviderRecoveryJSON(e.Journal), false); err != nil {
			return err
		}
		if err := store.write(filepath.Join(base, "qualification.json"), mustProviderRecoveryJSON(a), false); err != nil {
			return err
		}
		fmt.Fprintf(out, "GO environment=dev scope=original-service-root-crl-installed original-review-sha256=%s review-sha256=%s runtime-reload-required=true\n", *old, sha)
		return nil
	}
	a, source, err := loadCreateRootCRLInstalledArtifact(store, *old, *review)
	if err != nil {
		return err
	}
	if *confirm != cfg.Values["CLOUD_STACK_NAME"] || a.Plan.CheckerSHA != checker || a.Plan.OwnerSHA != deploymentplan.Digest(e.owner) || a.Plan.ConfigurationSHA != deploymentplan.Digest(cfg) || a.Plan.KubeconfigSHA != environmentPreflightBytesSHA256(kube) || a.Plan.OriginalArtifactSHA != deploymentplan.Digest(original) || source.OwnerToken != e.Journal.OwnerToken || !rootCRLInstalledProgressValid(source, e.Journal, original, originalSource, *review) || !time.Now().Before(a.ValidUntil) {
		return rootCRLRecoveryError()
	}
	return executeCreateRootCRLInstalled(ctx, cfg, store, e, b, original, originalSource, a, generation.Plan.Plan, out)
}

// No native import, Secret replacement, disk installer or runtime ACK occurs.
// These receipts describe the existing native signed state observed under lease.
func executeCreateRootCRLInstalled(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, original createRootCRLRecoveryArtifact, originalSource environmentCreateJournal, a createRootCRLInstalledArtifact, generation environmentCreatePlan, out io.Writer) (resultErr error) {
	review, old := deploymentplan.Digest(a.Plan), a.Plan.OriginalReview
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
	if err := e.RecordKubernetesLease(lease.Owner()); err != nil {
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
	evidence, err := observeCreateRootCRLInstalled(ctx, cfg, store, e, b, original, generation)
	if err != nil || deploymentplan.Digest(evidence) != deploymentplan.Digest(a.Plan.Evidence) {
		return rootCRLRecoveryError()
	}
	key := "service-root-crl-renewal-" + old
	for _, target := range original.Plan.Targets {
		if err := guard(); err != nil {
			return err
		}
		if err := verifyCreateRootCRLTargetOwners(ctx, cfg, b, target); err != nil {
			return err
		}
		if err := verifyCreateRootCRLTargetState(ctx, cfg, b, original.Plan, target); err != nil {
			return err
		}
		stateKey := key + "/state-" + target.Container
		if err := rootCRLStepBegin(e, stateKey, map[string]any{"target": target, "native_crl_sha256": original.Plan.Signed.Digest}); err != nil {
			return err
		}
		if err := rootCRLStepComplete(e, stateKey, map[string]any{"resource_id": target.ID, "uid": target.UID, "crl_sha256": original.Plan.Signed.Digest, "paths": target.Paths, "installed_on_disk": true, "runtime_reload_required": true}); err != nil {
			return err
		}
	}
	proof := createRootCRLRecoveryProof{Schema: createRootCRLRecoverySchema, Parent: original.Plan.Parent, Review: old, File: filepath.ToSlash(filepath.Join(original.Plan.Candidate, "signed/revocations.pem")), Digest: original.Plan.Signed.Digest, Completed: true}
	if err := guard(); err != nil {
		return err
	}
	if err := rootCRLStepComplete(e, key, proof); err != nil {
		return err
	}
	end := len(originalSource.Entries) + 5
	if !rootCRLJournalTailValid(e.Journal, original, originalSource, true) || len(e.Journal.Entries) != end {
		return rootCRLRecoveryError()
	}
	completion := createRootCRLInstalledCompletion{Schema: createRootCRLInstalledSchema + "/completion", Review: review, OriginalReview: old, FinalEntriesSHA: deploymentplan.Digest(e.Journal.Entries[:end]), ProofSHA: activeTransportJournalDigest(proof), EvidenceSHA: deploymentplan.Digest(evidence), StartedAt: started, CompletedAt: time.Now().UTC(), RuntimeReloadRequired: true}
	base := rootCRLInstalledBase(old, review)
	completionPath := filepath.Join(base, "completed.json")
	if retainedRaw, err := store.read(completionPath); err == nil {
		var retained createRootCRLInstalledCompletion
		if json.Unmarshal([]byte(retainedRaw), &retained) != nil || retained.Schema != completion.Schema || retained.Review != review || retained.OriginalReview != old || retained.FinalEntriesSHA != completion.FinalEntriesSHA || retained.ProofSHA != completion.ProofSHA || retained.EvidenceSHA != completion.EvidenceSHA || !retained.RuntimeReloadRequired || retained.StartedAt.Before(a.QualifiedAt) || !retained.StartedAt.Before(a.ValidUntil) || retained.CompletedAt.Before(retained.StartedAt) {
			return rootCRLRecoveryError()
		}
		completion = retained
	} else if !os.IsNotExist(err) {
		return err
	} else if err := store.write(completionPath, mustProviderRecoveryJSON(completion), false); err != nil {
		return err
	}
	pointer := createRootCRLInstalledPointer{Review: review, OriginalReview: old, CompletionSHA: deploymentplan.Digest(completion)}
	if err := writeRootCRLInstalledImmutable(store, filepath.Join("deployment/create/root-crl-recovery", old, "installed-resume/completed.json"), pointer); err != nil {
		return err
	}
	if err := writeRootCRLInstalledImmutable(store, filepath.Join("deployment/create/material", original.Plan.Parent, "service-root-crl-renewal.json"), proof); err != nil {
		return err
	}
	fmt.Fprintf(out, "completed original-service-root-crl-installed review-sha256=%s original-review-sha256=%s runtime-reload-required=true\n", review, old)
	return nil
}

func verifyCreateRootCRLInstalledCompletion(store secretStore, e *environmentCreateExecution, original createRootCRLRecoveryArtifact, originalSource environmentCreateJournal, proof createRootCRLRecoveryProof) error {
	path := filepath.Join(store.Root, "deployment/create/root-crl-recovery", proof.Review, "installed-resume/completed.json")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	var pointer createRootCRLInstalledPointer
	if readExecutionJSON(path, &pointer) != nil || pointer.OriginalReview != proof.Review {
		return rootCRLRecoveryError()
	}
	a, source, err := loadCreateRootCRLInstalledArtifact(store, proof.Review, pointer.Review)
	if err != nil || a.Plan.OriginalArtifactSHA != deploymentplan.Digest(original) || a.Plan.Parent != proof.Parent || a.Plan.OwnerSHA != deploymentplan.Digest(e.owner) || source.OwnerToken != e.Journal.OwnerToken || !rootCRLInstalledSourceValid(source, original, originalSource) {
		return rootCRLRecoveryError()
	}
	var completed createRootCRLInstalledCompletion
	if readExecutionJSON(filepath.Join(store.Root, rootCRLInstalledBase(proof.Review, pointer.Review), "completed.json"), &completed) != nil || completed.Schema != createRootCRLInstalledSchema+"/completion" || completed.Review != pointer.Review || completed.OriginalReview != proof.Review || deploymentplan.Digest(completed) != pointer.CompletionSHA || completed.ProofSHA != activeTransportJournalDigest(proof) || completed.EvidenceSHA != deploymentplan.Digest(a.Plan.Evidence) || !completed.RuntimeReloadRequired || completed.StartedAt.Before(a.QualifiedAt) || !completed.StartedAt.Before(a.ValidUntil) || completed.CompletedAt.Before(completed.StartedAt) {
		return rootCRLRecoveryError()
	}
	end := len(originalSource.Entries) + 5
	if len(e.Journal.Entries) < end || !environmentPreflightDigest.MatchString(completed.FinalEntriesSHA) || completed.FinalEntriesSHA != deploymentplan.Digest(e.Journal.Entries[:end]) || !rootCRLJournalTailValid(e.Journal, original, originalSource, true) {
		return rootCRLRecoveryError()
	}
	for i, entry := range source.Entries {
		actual := e.Journal.Entries[i]
		if entry.State == "completed" && activeTransportJournalDigest(entry) != activeTransportJournalDigest(actual) || entry.Key != actual.Key || entry.Phase != actual.Phase || entry.IntentSHA256 != actual.IntentSHA256 || !entry.StartedAt.Equal(actual.StartedAt) || entry.State == "allocated" && activeTransportJournalDigest(json.RawMessage(entry.Receipt)) != activeTransportJournalDigest(json.RawMessage(actual.Receipt)) {
			return rootCRLRecoveryError()
		}
	}
	return nil
}

func writeRootCRLInstalledImmutable(store secretStore, path string, value any) error {
	if old, err := store.read(path); err == nil {
		if activeTransportJournalDigest(json.RawMessage(old)) != activeTransportJournalDigest(value) {
			return rootCRLRecoveryError()
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return store.write(path, mustProviderRecoveryJSON(value), false)
}
