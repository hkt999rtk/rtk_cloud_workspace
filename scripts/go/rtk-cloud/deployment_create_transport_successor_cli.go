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

func runEnvironmentCreateInitialTransportSuccessor(args []string) error {
	return runEnvironmentCreateInitialTransportSuccessorContext(context.Background(), args, os.Stdout, os.Stderr)
}

type initialTransportSuccessorArtifact struct {
	Schema      string                        `json:"schema"`
	Plan        initialTransportSuccessorPlan `json:"plan"`
	QualifiedAt time.Time                     `json:"qualified_at"`
	ValidUntil  time.Time                     `json:"valid_until"`
}

type initialTransportSuccessorProof struct {
	Schema       string `json:"schema"`
	Parent       string `json:"parent_plan_sha256"`
	Review       string `json:"review_sha256"`
	CandidateSHA string `json:"candidate_sha256"`
	Journal      string `json:"journal"`
	Completed    bool   `json:"completed"`
}

// Recover the original continuation only from a fully verified successor.
// The source journal is immutable and predates the transport-only writes.
func initialSuccessorContinuationEntry(store secretStore, e *environmentCreateExecution) (environmentCreateJournalEntry, bool, error) {
	var empty environmentCreateJournalEntry
	if e == nil || e.Store.Root != store.Root {
		return empty, false, createProviderRecoveryBlock("original successor execution is unavailable")
	}
	_, complete, err := readCompletedInitialTransportSuccessor(store, e.Journal.PlanSHA256)
	if err != nil || !complete {
		return empty, false, err
	}
	var proof initialTransportSuccessorProof
	pointer := filepath.Join(store.Root, "deployment", "create", "material", e.Journal.PlanSHA256, "initial-transport-successor.json")
	var source, current environmentCreateJournal
	if readExecutionJSON(pointer, &proof) != nil || proof.Journal != e.owner.Journal ||
		readExecutionJSON(filepath.Join(store.Root, "deployment", "create", "transport-successor", proof.Review, "source-journal.json"), &source) != nil ||
		readExecutionJSON(filepath.Join(store.Root, proof.Journal), &current) != nil ||
		deploymentplan.Digest(current) != deploymentplan.Digest(e.Journal) || source.OwnerToken != e.Journal.OwnerToken {
		return empty, false, createProviderRecoveryBlock("original successor continuation evidence differs from the active execution")
	}
	entry, ok := initialTransportSuccessorCheckpoint(source)
	if !ok {
		return empty, false, createProviderRecoveryBlock("original successor continuation checkpoint is inconsistent")
	}
	for _, active := range current.Entries {
		if active.Key == entry.Key {
			return entry, active.State == "pending" && active.IntentSHA256 == entry.IntentSHA256, nil
		}
	}
	return empty, false, createProviderRecoveryBlock("original successor continuation is missing from the active journal")
}

func initialSuccessorAllowsPendingRole(store secretStore, e *environmentCreateExecution, key string, intent deploymentplan.Object) bool {
	if e == nil || e.Store.Root != store.Root || e.Check(context.Background()) != nil {
		return false
	}
	candidate, ok, err := readCompletedInitialTransportSuccessor(store, e.Journal.PlanSHA256)
	if err != nil || !ok {
		return false
	}
	var proof initialTransportSuccessorProof
	pointer := filepath.Join(store.Root, "deployment", "create", "material", e.Journal.PlanSHA256, "initial-transport-successor.json")
	if readExecutionJSON(pointer, &proof) != nil || proof.Journal != e.owner.Journal || proof.Parent != e.Journal.PlanSHA256 || proof.CandidateSHA != deploymentplan.Digest(candidate) || !environmentPreflightDigest.MatchString(proof.Review) {
		return false
	}
	var source environmentCreateJournal
	if readExecutionJSON(filepath.Join(store.Root, "deployment", "create", "transport-successor", proof.Review, "source-journal.json"), &source) != nil {
		return false
	}
	if _, ok := initialTransportSuccessorCheckpoint(source); !ok || len(source.Entries) == 0 {
		return false
	}
	role := source.Entries[len(source.Entries)-1]
	return role.Key == key && role.IntentSHA256 == deploymentplan.Digest(intent) && source.OwnerToken == e.Journal.OwnerToken
}

func readCompletedInitialTransportSuccessor(store secretStore, parent string) (initialTransportSuccessorCandidate, bool, error) {
	var empty initialTransportSuccessorCandidate
	bad := func() (initialTransportSuccessorCandidate, bool, error) {
		return empty, false, createProviderRecoveryBlock("completed temporary transport successor evidence is missing or inconsistent")
	}
	if !environmentPreflightDigest.MatchString(parent) {
		return bad()
	}
	pointer, err := store.safePath(filepath.Join("deployment", "create", "material", parent, "initial-transport-successor.json"))
	if err != nil {
		return bad()
	}
	if _, err := os.Lstat(pointer); errors.Is(err, os.ErrNotExist) {
		return empty, false, nil
	} else if err != nil {
		return bad()
	}
	var proof initialTransportSuccessorProof
	if readExecutionJSON(pointer, &proof) != nil || proof.Parent != parent || !environmentPreflightDigest.MatchString(proof.Review) || filepath.Clean(proof.Journal) != proof.Journal || !strings.HasPrefix(filepath.ToSlash(proof.Journal), "deployment/create/executions/"+parent+"/execution-") || filepath.Base(proof.Journal) != "journal.json" {
		return bad()
	}
	journalPath, err := store.safePath(proof.Journal)
	if err != nil {
		return bad()
	}
	base := filepath.Join(store.Root, "deployment", "create", "transport-successor", proof.Review)
	var artifact initialTransportSuccessorArtifact
	var source, current environmentCreateJournal
	var completed initialTransportSuccessorProof
	if readExecutionJSON(filepath.Join(base, "qualification.json"), &artifact) != nil || readExecutionJSON(filepath.Join(base, "source-journal.json"), &source) != nil || readExecutionJSON(filepath.Join(base, "completed.json"), &completed) != nil || readExecutionJSON(journalPath, &current) != nil || deploymentplan.Digest(completed) != deploymentplan.Digest(proof) || validateInitialTransportSuccessorProof(artifact, proof, source, current) != nil {
		return bad()
	}
	candidate, err := readInitialTransportSuccessorCandidate(store, parent, artifact.QualifiedAt)
	if err != nil || deploymentplan.Digest(candidate) != proof.CandidateSHA {
		return bad()
	}
	return candidate, true, nil
}

func completedInitialTransportSuccessorOriginalResources(store secretStore, parent string) ([]activeTransportRenewalResource, error) {
	_, completed, err := readCompletedInitialTransportSuccessor(store, parent)
	if err != nil || !completed {
		return nil, err
	}
	var proof initialTransportSuccessorProof
	var artifact initialTransportSuccessorArtifact
	pointer := filepath.Join(store.Root, "deployment", "create", "material", parent, "initial-transport-successor.json")
	if readExecutionJSON(pointer, &proof) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment", "create", "transport-successor", proof.Review, "qualification.json"), &artifact) != nil || deploymentplan.Digest(artifact) != proof.Review {
		return nil, createProviderRecoveryBlock("completed temporary transport successor resources are unavailable")
	}
	return artifact.Plan.Resources, nil
}

func completedInitialTransportSuccessorResources(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend) ([]activeTransportRenewalResource, error) {
	if e == nil || b == nil || e.Store != store || e.Check(ctx) != nil {
		return nil, createProviderRecoveryBlock("original execution owner is unavailable")
	}
	parent := e.Journal.PlanSHA256
	base, err := completedInitialTransportSuccessorOriginalResources(store, parent)
	if err != nil || len(base) == 0 {
		return base, err
	}
	var proof initialTransportSuccessorProof
	pointer := filepath.Join(store.Root, "deployment", "create", "material", parent, "initial-transport-successor.json")
	if readExecutionJSON(pointer, &proof) != nil {
		return nil, createProviderRecoveryBlock("completed temporary transport successor pointer is unavailable")
	}
	return completedRecordedTransportSuccessors(ctx, store, e, b, proof, base)
}

func initialTransportAuthorityPath(store secretStore, parent, role string) (string, string, error) {
	if !environmentPreflightDigest.MatchString(parent) || (role != "openbao" && role != "management") {
		return "", "", createProviderRecoveryBlock("initial transport authority requires the original generation and role")
	}
	relative := filepath.Join("deployment", "create", "material", parent, role+"-transport", "ca.crt")
	candidate, completed, err := readCompletedInitialTransportSuccessor(store, parent)
	if err != nil {
		return "", "", err
	}
	if completed {
		var proof initialTransportSuccessorProof
		pointer := filepath.Join(store.Root, "deployment", "create", "material", parent, "initial-transport-successor.json")
		if readExecutionJSON(pointer, &proof) != nil || proof.Parent != parent || !proof.Completed || !environmentPreflightDigest.MatchString(proof.Review) || proof.CandidateSHA != deploymentplan.Digest(candidate) {
			return "", "", createProviderRecoveryBlock("completed temporary transport successor pointer is unavailable")
		}
		relative = filepath.Join("deployment", "create", "transport-successor", proof.Review, role+"-transport", "ca.crt")
	}
	path, err := store.safePath(relative)
	if err != nil {
		return "", "", err
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	if err == nil && completed && string(raw) != candidate.Transports[role].CACert {
		return "", "", createProviderRecoveryBlock("completed temporary transport authority differs from its retained successor")
	}
	return path, string(raw), err
}

func initialTransportCustodySnapshotPath(store secretStore, parent, authorityPath string) (string, error) {
	original := filepath.Join(store.Root, "deployment", "create", "material", parent, "openbao-transport", "ca.crt")
	relative := filepath.Join("deployment", "create", parent, "custody", "transport.json")
	if authorityPath != original {
		var proof initialTransportSuccessorProof
		pointer := filepath.Join(store.Root, "deployment", "create", "material", parent, "initial-transport-successor.json")
		if readExecutionJSON(pointer, &proof) != nil || proof.Parent != parent || !proof.Completed || !environmentPreflightDigest.MatchString(proof.Review) || authorityPath != filepath.Join(store.Root, "deployment", "create", "transport-successor", proof.Review, "openbao-transport", "ca.crt") {
			return "", createProviderRecoveryBlock("custody snapshot does not match its completed temporary transport authority")
		}
		relative = filepath.Join("deployment", "create", "transport-successor", proof.Review, "custody", "transport.json")
	}
	return store.safePath(relative)
}

func initialTransportSuccessorIntent(proof initialTransportSuccessorProof) map[string]string {
	return map[string]string{"review_sha256": proof.Review, "candidate_sha256": proof.CandidateSHA}
}

func initialTransportSuccessorResourceIntent(resource activeTransportRenewalResource) map[string]string {
	return map[string]string{"resource": resource.ID, "uid": resource.UID, "before_sha256": deploymentplan.Digest(resource.Before), "desired_sha256": deploymentplan.Digest(resource.Desired)}
}

func validateInitialTransportSuccessorProof(a initialTransportSuccessorArtifact, proof initialTransportSuccessorProof, source, current environmentCreateJournal) error {
	bad := func() error {
		return createProviderRecoveryBlock("initial transport successor completion lacks its exact recorded native outcomes")
	}
	if proof.Schema != "rtk-cloud.initial-transport-successor-proof/v1" || !proof.Completed || proof.Parent != a.Plan.Parent || proof.CandidateSHA != a.Plan.CandidateSHA || current.Environment != source.Environment || current.PlanSHA256 != source.PlanSHA256 || current.OwnerToken != source.OwnerToken || len(current.Entries) < len(source.Entries)+1 || len(a.Plan.Resources) == 0 {
		return bad()
	}
	for i, before := range source.Entries {
		after := current.Entries[i]
		if deploymentplan.Digest(before) == deploymentplan.Digest(after) {
			continue
		}
		if before.State != "pending" || len(before.Receipt) != 0 || before.Key != after.Key || before.Phase != after.Phase || before.IntentSHA256 != after.IntentSHA256 || !before.StartedAt.Equal(after.StartedAt) || len(after.Receipt) == 0 || (after.State != "allocated" && after.State != "completed") || (after.State == "allocated") != after.CompletedAt.IsZero() {
			return bad()
		}
	}
	key := "initial-transport-successor-" + proof.Review
	entries := map[string]environmentCreateJournalEntry{}
	for _, entry := range current.Entries[len(source.Entries):] {
		if _, exists := entries[entry.Key]; exists {
			return bad()
		}
		entries[entry.Key] = entry
	}
	parent, ok := entries[key]
	if !ok || parent.Phase != "recovery" || parent.State != "completed" || parent.StartedAt.IsZero() || parent.CompletedAt.Before(parent.StartedAt) || parent.IntentSHA256 != deploymentplan.Digest(initialTransportSuccessorIntent(proof)) || deploymentplan.Digest(jsonRawObject(parent.Receipt)) != deploymentplan.Digest(jsonRawObject(mustProviderRecoveryJSON(proof))) || validateInitialTransportSuccessorArtifact(a, proof.Review, source, parent.StartedAt) != nil {
		return bad()
	}
	seen := map[string]bool{}
	for _, resource := range a.Plan.Resources {
		partKey := key + "-" + strings.ReplaceAll(resource.ID, "/", "-")
		part, exists := entries[partKey]
		intent := initialTransportSuccessorResourceIntent(resource)
		if !exists || seen[partKey] || resource.ID == "" || resource.UID == "" || part.Phase != "recovery" || part.State != "completed" || part.StartedAt.Before(parent.StartedAt) || part.CompletedAt.Before(part.StartedAt) || part.CompletedAt.After(parent.CompletedAt) || part.IntentSHA256 != deploymentplan.Digest(intent) || deploymentplan.Digest(jsonRawObject(part.Receipt)) != deploymentplan.Digest(intent) {
			return bad()
		}
		seen[partKey] = true
	}
	return nil
}

func validateInitialTransportSuccessorArtifact(a initialTransportSuccessorArtifact, review string, source environmentCreateJournal, now time.Time) error {
	if a.Schema != "rtk-cloud.initial-transport-successor-artifact/v1" || a.Plan.Schema != "rtk-cloud.initial-transport-successor-plan/v1" || !environmentPreflightDigest.MatchString(review) || deploymentplan.Digest(a) != review || !environmentPreflightDigest.MatchString(a.Plan.Parent) || a.Plan.Parent != source.PlanSHA256 || a.Plan.JournalSHA != deploymentplan.Digest(source) || !environmentPreflightDigest.MatchString(a.Plan.CandidateSHA) || a.QualifiedAt.IsZero() || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || now.Before(a.QualifiedAt) || !now.Before(a.ValidUntil) {
		return createProviderRecoveryBlock("initial transport successor qualification is expired or differs from its original journal")
	}
	return nil
}

// Preparing generates only temporary successor material in the selected
// private store. Qualification verifies that exact material and dry-runs the
// native Kubernetes writes; neither phase changes live resources.
func runEnvironmentCreateInitialTransportSuccessorContext(parentContext context.Context, args []string, out, diagnostics io.Writer) (resultErr error) {
	fs := flag.NewFlagSet("recover-create-initial-transports", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	env := fs.String("environment", "", "selected environment")
	parent := fs.String("parent-plan-sha256", "", "original create generation")
	workspaceOpt := fs.String("workspace", "", "selected workspace")
	prepare := fs.Bool("prepare", false, "prepare temporary successor material")
	qualify := fs.Bool("qualify", false, "qualify the prepared successor without live writes")
	execute := fs.Bool("execute", false, "execute the exact qualified successor")
	review := fs.String("review-sha256", "", "exact successor qualification")
	confirm := fs.String("confirm", "", "exact selected stack")
	timeout := fs.Duration("timeout", 15*time.Minute, "bounded qualification timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	phases := 0
	for _, selected := range []bool{*prepare, *qualify, *execute} {
		if selected {
			phases++
		}
	}
	if fs.NArg() != 0 || *env != "dev" || !environmentPreflightDigest.MatchString(*parent) || phases != 1 || *timeout <= 0 || *timeout > 30*time.Minute || *execute && !environmentPreflightDigest.MatchString(*review) || !*execute && (*review != "" || *confirm != "") {
		return createProviderRecoveryBlock("select dev, the exact original generation, and exactly one successor preparation or qualification phase")
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
	if err := recoveryMutationGuard(append([]string{"deployment", "recover-create-initial-transports"}, args...)); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(parentContext, *timeout)
	defer cancel()
	e, original, err := loadActiveTransportExecution(cfg, store, *parent)
	if err != nil {
		return err
	}
	defer e.Close()
	if deploymentplan.Digest(operator) != original.Plan.Plan.OperatorSHA256 {
		return createProviderRecoveryBlock("operator inputs differ from the original generation")
	}
	source := e.Journal
	var executionArtifact initialTransportSuccessorArtifact
	if *execute {
		base := filepath.Join(store.Root, "deployment", "create", "transport-successor", *review)
		if *confirm != cfg.Values["CLOUD_STACK_NAME"] || readExecutionJSON(filepath.Join(base, "qualification.json"), &executionArtifact) != nil || readExecutionJSON(filepath.Join(base, "source-journal.json"), &source) != nil || deploymentplan.Digest(executionArtifact) != *review || executionArtifact.Plan.Parent != *parent {
			return createProviderRecoveryBlock("exact qualified recovery and selected stack are required")
		}
	}
	entry, ok := initialTransportSuccessorCheckpoint(source)
	if !ok {
		return createProviderRecoveryBlock("original interrupted issuer enrollment checkpoint is unavailable")
	}
	if _, err := verifyIssuerCoreContinuationAdmission(store, e, entry); err != nil {
		return err
	}
	if *prepare {
		if err := e.Check(ctx); err != nil {
			return err
		}
		candidate, err := prepareInitialTransportSuccessor(store, *parent, time.Now().UTC())
		if err != nil {
			return err
		}
		if err := e.Check(ctx); err != nil {
			return err
		}
		fmt.Fprintf(out, "PASS environment=%s scope=initial-temporary-transport-preparation candidate-sha256=%s\n", *env, deploymentplan.Digest(candidate))
		return nil
	}
	candidate, err := readInitialTransportSuccessorCandidate(store, *parent, time.Now().UTC())
	if err != nil {
		return err
	}
	kube, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || environmentPreflightBytesSHA256(kube) != original.Plan.KubeconfigSHA256 {
		return errors.New("selected kubeconfig differs from the original generation")
	}
	b, err := newEnvironmentKubernetesBackendForConfig(string(kube), cfg)
	if err != nil {
		return err
	}
	if *execute {
		checker, err := environmentPreflightCheckerSHA256()
		if err != nil || executionArtifact.Plan.KubeconfigSHA != environmentPreflightBytesSHA256(kube) || executionArtifact.Plan.OwnerSHA != deploymentplan.Digest(e.owner) || executionArtifact.Plan.ConfigurationSHA != deploymentplan.Digest(cfg) || executionArtifact.Plan.CandidateSHA != deploymentplan.Digest(candidate) {
			return createProviderRecoveryBlock("qualified successor no longer matches its environment, checker, owner or candidate")
		}
		admitted := time.Now().UTC()
		started := false
		expectedIntent := initialTransportSuccessorIntent(initialTransportSuccessorProof{Review: *review, CandidateSHA: executionArtifact.Plan.CandidateSHA})
		for _, journalEntry := range e.Journal.Entries {
			if journalEntry.Key == "initial-transport-successor-"+*review {
				if started || journalEntry.Phase != "recovery" || journalEntry.IntentSHA256 != deploymentplan.Digest(expectedIntent) {
					return createProviderRecoveryBlock("original transport recovery admission differs")
				}
				started = true
				admitted = journalEntry.StartedAt
			}
		}
		if checker != executionArtifact.Plan.CheckerSHA && !started {
			return createProviderRecoveryBlock("qualified successor checker changed before recovery admission")
		}
		if checker != executionArtifact.Plan.CheckerSHA {
			fmt.Fprintln(out, "[RUNNING] deployment.transport-recovery.resume: reconcile the admitted original write intents with the updated checker")
		}
		if err := validateInitialTransportSuccessorArtifact(executionArtifact, *review, source, admitted); err != nil {
			return err
		}
		pod, set, err := originalCreateOpenBaoFoundationIdentity(ctx, cfg, store, e, b, "kubernetes/foundation")
		if err != nil || pod != executionArtifact.Plan.PodUID || set != executionArtifact.Plan.StatefulSetUID {
			return createProviderRecoveryBlock("original OpenBao Pod or StatefulSet identity changed")
		}
		oldRaw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment", "create", "material", *parent, "openbao-transport", "identities.json"), true)
		var old environmentCreateTransport
		if err != nil || json.Unmarshal(oldRaw, &old) != nil || verifyActiveTransportMounted(ctx, cfg, b, old, candidate.Transports["openbao"], true) != nil {
			return createProviderRecoveryBlock("original OpenBao temporary TLS projection cannot be reconciled")
		}
		leasePlan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: *env, Stack: cfg.Values["CLOUD_STACK_NAME"], Operation: "scoped", BindingsSHA256: *review}}
		lease, err := b.AcquireExecution(ctx, leasePlan)
		if err != nil {
			return err
		}
		defer func() {
			cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if err := lease.Release(cleanup); err != nil {
				resultErr = errors.Join(resultErr, err)
			}
		}()
		if err := e.RecordKubernetesLease(lease.Owner()); err != nil {
			return err
		}
		b.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
		if err != nil {
			return err
		}
		check := func() error { return lease.Check(ctx) }
		reload := func() error {
			leaf := candidate.Transports["openbao"].Identities["openbao"]
			script := "set -eu; for n in $(seq 1 90); do cert=$(sha256sum /openbao/transport/tls.crt | cut -d' ' -f1); ca=$(sha256sum /openbao/transport/ca.crt | cut -d' ' -f1); key=$(sha256sum /openbao/transport/tls.key | cut -d' ' -f1); if [ \"$cert\" = \"" + environmentPreflightBytesSHA256([]byte(leaf.Certificate)) + "\" ] && [ \"$ca\" = \"" + environmentPreflightBytesSHA256([]byte(candidate.Transports["openbao"].CACert)) + "\" ] && [ \"$key\" = \"" + environmentPreflightBytesSHA256([]byte(leaf.Key)) + "\" ]; then kill -HUP 1; exit 0; fi; sleep 2; done; exit 1"
			if _, err := environmentCreateOpenBaoExec(ctx, cfg, b, nil, script); err != nil {
				return err
			}
			_, _, err := verifyInitializedOpenBaoIdentity(ctx, cfg, store, e, b)
			return err
		}
		verify := func() error {
			actualPod, actualSet, err := originalCreateOpenBaoFoundationIdentity(ctx, cfg, store, e, b, "kubernetes/foundation")
			if err != nil || actualPod != pod || actualSet != set {
				return createProviderRecoveryBlock("original OpenBao owner changed during transport recovery")
			}
			for _, resource := range executionArtifact.Plan.Resources {
				actual, err := b.Read(ctx, resource.ID)
				if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != resource.UID || !deploymentplan.Matches(resource.Desired, actual) {
					return createProviderRecoveryBlock("temporary transport resource readback differs after rollout: " + resource.ID)
				}
			}
			return nil
		}
		for _, resource := range executionArtifact.Plan.Resources {
			if initialTransportBootstrapTrustHelper(resource) {
				fmt.Fprintf(out, "[RUNNING] %s bootstrap readiness remains subject to complete deployment and health verification\n", resource.ID)
			}
		}
		if err := executeInitialTransportSuccessor(ctx, cfg, store, e, b, executionArtifact, source, candidate, check, reload, verify); err != nil {
			return err
		}
		fmt.Fprintf(out, "PASS environment=%s scope=initial-temporary-transport-recovery review-sha256=%s\n", *env, *review)
		return nil
	}
	plan, err := qualifyInitialTransportSuccessor(ctx, cfg, store, e, b, candidate)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	artifact := initialTransportSuccessorArtifact{Schema: "rtk-cloud.initial-transport-successor-artifact/v1", Plan: plan, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
	sha := deploymentplan.Digest(artifact)
	if err := validateInitialTransportSuccessorArtifact(artifact, sha, e.Journal, now); err != nil {
		return err
	}
	base := filepath.Join("deployment", "create", "transport-successor", sha)
	if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(base, "source-journal.json"), mustProviderRecoveryJSON(e.Journal)); err != nil {
		return err
	}
	if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(base, "qualification.json"), mustProviderRecoveryJSON(artifact)); err != nil {
		return err
	}
	fmt.Fprintf(out, "GO environment=%s scope=initial-temporary-transport-recovery review-sha256=%s resources=%d\n", *env, sha, len(plan.Resources))
	return nil
}
