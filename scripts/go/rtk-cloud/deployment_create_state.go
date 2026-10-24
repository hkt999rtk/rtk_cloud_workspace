package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const environmentCreateArtifactSchema = "rtk-cloud.create-artifact/v1"
const environmentCreateJournalSchema = "rtk-cloud.create-journal/v1"
const environmentCreateOwnerSchema = "rtk-cloud.create-owner/v1"

var environmentCreateRequiredGates = []string{"deployment.create.target", "deployment.create.release", "deployment.create.provider", "deployment.create.bootstrap", "deployment.create.network", "deployment.create.inputs"}

// Create qualification freezes provider/bootstrap intent independently of the
// later Kubernetes plan. Only the shared active qualification generation grants
// admission; the create pointer alone never authorizes an execution.
type environmentCreateArtifact struct {
	Schema              string                               `json:"schema"`
	Generation          string                               `json:"generation"`
	Environment         string                               `json:"environment"`
	Stack               string                               `json:"stack"`
	PlanSHA256          string                               `json:"plan_sha256"`
	ConfigurationSHA256 string                               `json:"configuration_sha256"`
	OperatorSHA256      string                               `json:"operator_sha256"`
	CheckerSHA256       string                               `json:"checker_sha256"`
	QualifiedAt         time.Time                            `json:"qualified_at"`
	ValidUntil          time.Time                            `json:"valid_until"`
	Plan                json.RawMessage                      `json:"plan"`
	Qualification       deploymentplan.Qualification         `json:"qualification"`
	ReleaseProofs       []environmentReleasePublicationProof `json:"release_proofs"`
	Directory           string                               `json:"-"`
}

func environmentCreateStateBlocker(id, cause string) error {
	return &deploymentplan.Blocker{ID: id, Cause: cause, Next: "preserve the selected environment's original create qualification and journal; verify exact inputs and native outcomes before separately qualified recovery; never overwrite partial execution or steal its owner"}
}

func canonicalEnvironmentCreateJSON(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 || len(raw) > environmentPreflightArtifactLimit {
		return nil, environmentCreateStateBlocker("deployment.create.preflight.evidence", "bounded create intent is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value map[string]any
	if decoder.Decode(&value) != nil || len(value) == 0 || decoder.Decode(new(any)) != io.EOF {
		return nil, environmentCreateStateBlocker("deployment.create.preflight.evidence", "complete create intent must be one JSON object")
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return nil, environmentCreateStateBlocker("deployment.create.preflight.evidence", "create intent cannot be encoded")
	}
	return canonical, nil
}

func environmentCreatePlanSHA256(raw json.RawMessage) (string, error) {
	canonical, err := canonicalEnvironmentCreateJSON(raw)
	if err != nil {
		return "", err
	}
	return environmentPreflightBytesSHA256(canonical), nil
}

func prepareEnvironmentCreateDirectories(store secretStore, relative string) (string, error) {
	if store.Root != filepath.Join(store.ConfigRoot, store.Environment) || !filepath.IsAbs(store.Root) || !secretEnvironmentPattern.MatchString(store.Environment) || !strings.HasPrefix(relative, "deployment/create/") || filepath.Clean(relative) != relative {
		return "", environmentCreateStateBlocker("deployment.create.state", "canonical selected create state path is required")
	}
	for _, root := range []string{store.ConfigRoot, store.Root} {
		if err := os.Mkdir(root, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", environmentCreateStateBlocker("deployment.create.state", "private create state directory cannot be prepared")
		}
		if err := validateEnvironmentPreflightDirectory(root); err != nil {
			return "", err
		}
	}
	current := store.Root
	for _, part := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		if err := os.Mkdir(current, 0700); err != nil && !errors.Is(err, os.ErrExist) {
			return "", environmentCreateStateBlocker("deployment.create.state", "private create state directory cannot be prepared")
		}
		if err := validateEnvironmentPreflightDirectory(current); err != nil {
			return "", err
		}
	}
	return current, nil
}

func validateEnvironmentCreateArtifact(artifact environmentCreateArtifact, cfg deploymentConfig, operatorSHA string, now time.Time) error {
	sha, err := environmentCreatePlanSHA256(artifact.Plan)
	checker, checkerErr := environmentPreflightCheckerSHA256()
	validity, validityErr := environmentPreflightValidity(cfg.Values)
	if err != nil || checkerErr != nil || validityErr != nil || artifact.Schema != environmentCreateArtifactSchema || artifact.Environment != cfg.Environment || artifact.Stack != cfg.Values["CLOUD_STACK_NAME"] || artifact.Stack != "video-cloud-"+cfg.Environment || !environmentPreflightDigest.MatchString(artifact.Generation) || artifact.PlanSHA256 != sha || artifact.ConfigurationSHA256 != deploymentplan.Digest(cfg) || !environmentPreflightDigest.MatchString(operatorSHA) || artifact.OperatorSHA256 != operatorSHA || artifact.CheckerSHA256 != checker || artifact.Qualification.PlanSHA256 != sha || artifact.Qualification.Decision != "GO" {
		return environmentCreateStateBlocker("deployment.create.preflight.evidence", "create qualification does not match the selected plan, checker, maintained configuration and operator inputs")
	}
	if err := environmentPreflightProofTimingError(environmentPreflightProof{QualifiedAt: artifact.QualifiedAt, ValidUntil: artifact.ValidUntil}, now); err != nil {
		return err
	}
	if !artifact.ValidUntil.Equal(artifact.QualifiedAt.Add(time.Duration(validity) * time.Second)) {
		return environmentCreateStateBlocker("deployment.create.preflight.evidence", "create qualification expiry does not match the maintained evidence validity")
	}
	seen, required := map[string]bool{}, map[string]bool{}
	for _, gate := range artifact.Qualification.Gates {
		if gate.ID == "" || seen[gate.ID] || gate.Required && (gate.Status != "PASS" || !environmentPreflightDigest.MatchString(gate.EvidenceSHA256)) {
			return environmentCreateStateBlocker("deployment.create.preflight.evidence", "a required create qualification gate is missing, incomplete or unsuccessful")
		}
		seen[gate.ID] = true
		if gate.Required {
			required[gate.ID] = true
		}
	}
	for _, id := range environmentCreateRequiredGates {
		if !required[id] {
			return environmentCreateStateBlocker("deployment.create.preflight.evidence", "successful required create qualification gates are unavailable")
		}
	}
	var plan environmentCreatePlan
	proof := environmentPreflightProof{Environment: artifact.Environment, Stack: artifact.Stack, QualifiedAt: artifact.QualifiedAt, ReleaseProofs: artifact.ReleaseProofs}
	if decodeEnvironmentJSON(artifact.Plan, &plan) != nil || validateEnvironmentPreflightReleaseProofs(proof, plan.Release) != nil {
		return environmentCreateStateBlocker("deployment.create.preflight.evidence", "complete exact-source publication evidence for the frozen create release is unavailable or changed")
	}
	return nil
}

func publishEnvironmentCreateArtifact(ctx context.Context, cfg deploymentConfig, generation string, plan json.RawMessage, operatorSHA string, qualification deploymentplan.Qualification, releaseProofs []environmentReleasePublicationProof, qualifiedAt time.Time) (*environmentCreateArtifact, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	canonical, err := canonicalEnvironmentCreateJSON(plan)
	if err != nil {
		return nil, err
	}
	sha := environmentPreflightBytesSHA256(canonical)
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return nil, err
	}
	validity, err := environmentPreflightValidity(cfg.Values)
	if err != nil {
		return nil, err
	}
	artifact := environmentCreateArtifact{Schema: environmentCreateArtifactSchema, Generation: generation, Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], PlanSHA256: sha, ConfigurationSHA256: deploymentplan.Digest(cfg), OperatorSHA256: operatorSHA, CheckerSHA256: checker, QualifiedAt: qualifiedAt.UTC(), ValidUntil: qualifiedAt.UTC().Add(time.Duration(validity) * time.Second), Plan: canonical, Qualification: qualification, ReleaseProofs: append([]environmentReleasePublicationProof(nil), releaseProofs...)}
	if err := validateEnvironmentCreateArtifact(artifact, cfg, operatorSHA, time.Now().UTC()); err != nil {
		return nil, err
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return nil, err
	}
	base, err := prepareEnvironmentCreateDirectories(store, "deployment/create/preflight")
	if err != nil {
		return nil, err
	}
	parent, err := prepareEnvironmentCreateDirectories(store, filepath.Join("deployment", "create", "preflight", sha))
	if err != nil {
		return nil, err
	}
	attempt, err := os.MkdirTemp(parent, "qualification-")
	if err != nil {
		return nil, environmentCreateStateBlocker("deployment.create.preflight.evidence", "immutable create qualification directory cannot be prepared")
	}
	if err := writeEnvironmentPreflightImmutableJSON(filepath.Join(attempt, "artifact.json"), artifact); err != nil {
		return nil, err
	}
	for _, path := range []string{attempt, parent, base} {
		if err := syncEnvironmentPreflightDirectory(path); err != nil {
			return nil, err
		}
	}
	relative, _ := filepath.Rel(base, attempt)
	if !environmentPreflightArtifactName.MatchString(filepath.ToSlash(relative)) {
		return nil, environmentCreateStateBlocker("deployment.create.preflight.evidence", "create qualification path cannot be verified")
	}
	lock, err := lockEnvironmentPreflightPublication(store)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	active, err := readEnvironmentPreflightActive(store)
	if err != nil || active.Decision != "NO-GO" || active.Generation != generation {
		return nil, environmentCreateStateBlocker("deployment.create.preflight.evidence", "another create or upgrade preflight replaced this qualification generation")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := validateEnvironmentCreateArtifact(artifact, cfg, operatorSHA, time.Now().UTC()); err != nil {
		return nil, err
	}
	active = environmentPreflightActive{Schema: environmentPreflightActiveSchema, Generation: generation, Environment: cfg.Environment, Decision: "GO", PlanSHA256: sha, Artifact: filepath.ToSlash(relative), ProofSHA256: deploymentplan.Digest(artifact)}
	if err := writeEnvironmentCreateActive(store, active); err != nil {
		return nil, err
	}
	if err := writeEnvironmentPreflightActive(store, active); err != nil {
		return nil, errors.Join(err, revokeEnvironmentPreflightProofLocked(store, generation))
	}
	if err := ctx.Err(); err != nil {
		return nil, errors.Join(err, revokeEnvironmentPreflightProofLocked(store, generation))
	}
	artifact.Directory = attempt
	return &artifact, nil
}

func writeEnvironmentCreateActive(store secretStore, active environmentPreflightActive) error {
	base, err := prepareEnvironmentCreateDirectories(store, "deployment/create/preflight")
	if err != nil {
		return err
	}
	raw, err := json.Marshal(active)
	if err != nil || store.write("deployment/create/preflight/active.json", raw, true) != nil {
		return environmentCreateStateBlocker("deployment.create.preflight.evidence", "create qualification pointer cannot be committed atomically")
	}
	return syncEnvironmentPreflightDirectory(base)
}

func loadEnvironmentCreateArtifact(cfg deploymentConfig, operatorSHA, requestedSHA string, now time.Time) (*environmentCreateArtifact, error) {
	store, err := newSecretStore("", cfg.Environment)
	if err != nil || !environmentPreflightDigest.MatchString(requestedSHA) {
		return nil, environmentCreateStateBlocker("deployment.create.preflight.evidence", "exact selected create plan SHA-256 is required")
	}
	active, err := readEnvironmentPreflightActive(store)
	if err != nil || active.Decision != "GO" || active.PlanSHA256 != requestedSHA {
		return nil, environmentCreateStateBlocker("deployment.create.preflight.evidence", "there is no active matching complete create qualification")
	}
	base := filepath.Join(store.Root, "deployment", "create", "preflight")
	if err := validateEnvironmentPreflightDirectories(store, base); err != nil {
		return nil, err
	}
	var createActive environmentPreflightActive
	if err := readEnvironmentPreflightJSON(filepath.Join(base, "active.json"), &createActive); err != nil || deploymentplan.Digest(createActive) != deploymentplan.Digest(active) {
		return nil, environmentCreateStateBlock("create authorization differs from the shared active qualification generation")
	}
	directory := filepath.Join(base, filepath.FromSlash(active.Artifact))
	if err := validateEnvironmentPreflightDirectories(store, directory); err != nil {
		return nil, err
	}
	var artifact environmentCreateArtifact
	if err := readEnvironmentPreflightJSON(filepath.Join(directory, "artifact.json"), &artifact); err != nil {
		return nil, err
	}
	if artifact.Generation != active.Generation || deploymentplan.Digest(artifact) != active.ProofSHA256 || artifact.PlanSHA256 != requestedSHA {
		return nil, environmentCreateStateBlock("immutable create qualification does not match its active pointer")
	}
	if err := validateEnvironmentCreateArtifact(artifact, cfg, operatorSHA, now); err != nil {
		return nil, err
	}
	current, err := readEnvironmentPreflightActive(store)
	if err != nil || deploymentplan.Digest(current) != deploymentplan.Digest(active) {
		return nil, environmentCreateStateBlock("create qualification was invalidated while being loaded")
	}
	artifact.Directory = directory
	return &artifact, nil
}

func environmentCreateStateBlock(cause string) error {
	return environmentCreateStateBlocker("deployment.create.preflight.evidence", cause)
}

// loadEnvironmentCreateAdmittedGeneration resolves the immutable qualification
// that actually admitted an execution. A parent can be either an original
// create qualification or a separately qualified provider recovery. The live
// preflight pointer and qualification expiry are deliberately irrelevant here:
// this is provenance for an already admitted journal, never permission to run
// a new deployment.
func loadEnvironmentCreateAdmittedGeneration(store secretStore, parent, artifactSHA string) (environmentCreatePlan, string, error) {
	var plan environmentCreatePlan
	if !environmentPreflightDigest.MatchString(parent) || !environmentPreflightDigest.MatchString(artifactSHA) {
		return plan, "", environmentCreateStateBlock("admitted create parent requires exact plan and artifact digests")
	}
	recoveryPath := filepath.Join(store.Root, "deployment/create/recovery", parent, "qualification.json")
	if _, err := os.Lstat(recoveryPath); err == nil {
		var artifact environmentCreateProviderRecoveryArtifact
		if readExecutionJSON(recoveryPath, &artifact) != nil || artifact.Schema != "rtk-cloud.provider-create-recovery-artifact/v1" || deploymentplan.Digest(artifact.Plan) != parent || deploymentplan.Digest(artifact) != artifactSHA || artifact.Qualification.PlanSHA256 != parent || artifact.Qualification.Decision != "GO" || !environmentCreateQualificationGatesValid(artifact.Qualification) {
			return plan, "", environmentCreateStateBlock("admitted provider recovery qualification is unavailable or changed")
		}
		return artifact.Plan.Plan, artifact.Plan.Plan.ConfigurationSHA256, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return plan, "", environmentCreateStateBlock("admitted provider recovery qualification cannot be inspected")
	}
	base := filepath.Join(store.Root, "deployment/create/preflight", parent)
	if validateEnvironmentPreflightDirectories(store, base) != nil {
		return plan, "", environmentCreateStateBlock("admitted original create qualification directory is unavailable")
	}
	entries, err := os.ReadDir(base)
	if err != nil {
		return plan, "", environmentCreateStateBlock("admitted original create qualification history cannot be inspected")
	}
	matches := 0
	configurationSHA := ""
	for _, entry := range entries {
		relative := filepath.ToSlash(filepath.Join(parent, entry.Name()))
		if !entry.IsDir() || !environmentPreflightArtifactName.MatchString(relative) {
			continue
		}
		directory := filepath.Join(base, entry.Name())
		if validateEnvironmentPreflightDirectories(store, directory) != nil {
			return plan, "", environmentCreateStateBlock("admitted original create qualification history contains an unsafe entry")
		}
		var artifact environmentCreateArtifact
		if readExecutionJSON(filepath.Join(directory, "artifact.json"), &artifact) != nil {
			return plan, "", environmentCreateStateBlock("admitted original create qualification history contains unreadable evidence")
		}
		if deploymentplan.Digest(artifact) != artifactSHA {
			continue
		}
		var candidate environmentCreatePlan
		sha, shaErr := environmentCreatePlanSHA256(artifact.Plan)
		if shaErr != nil || decodeEnvironmentJSON(artifact.Plan, &candidate) != nil || artifact.Schema != environmentCreateArtifactSchema || artifact.PlanSHA256 != parent || sha != parent || artifact.Environment != store.Environment || artifact.Stack != candidate.Stack || artifact.ConfigurationSHA256 != candidate.ConfigurationSHA256 || artifact.Qualification.PlanSHA256 != parent || artifact.Qualification.Decision != "GO" || !environmentCreateQualificationGatesValid(artifact.Qualification) {
			return plan, "", environmentCreateStateBlock("admitted original create qualification is incomplete or changed")
		}
		matches++
		plan, configurationSHA = candidate, artifact.ConfigurationSHA256
	}
	if matches != 1 {
		return plan, "", environmentCreateStateBlock("exactly one immutable admitted original create qualification is required")
	}
	return plan, configurationSHA, nil
}

func environmentCreateQualificationGatesValid(q deploymentplan.Qualification) bool {
	if len(q.Gates) != len(environmentCreateRequiredGates) {
		return false
	}
	for i, id := range environmentCreateRequiredGates {
		gate := q.Gates[i]
		if gate.ID != id || !gate.Required || gate.Status != "PASS" || !environmentPreflightDigest.MatchString(gate.EvidenceSHA256) {
			return false
		}
	}
	return true
}

// environmentCreateGenerationMatchesAdmitted accepts the immutable admitted
// generation and the one maintained legacy repair that can legitimately evolve
// it: removing the refused bare OpenBao search-path alias. The recovery that
// performs this change separately authenticates the original pending request,
// its corrected request and the accepted forward. No other plan drift is
// allowed at later CRL recovery boundaries.
func environmentCreateGenerationMatchesAdmitted(admitted, current environmentCreatePlan) bool {
	if deploymentplan.Digest(admitted) == deploymentplan.Digest(current) {
		return true
	}
	raw, err := json.Marshal(admitted)
	if err != nil {
		return false
	}
	var expected environmentCreatePlan
	if decodeEnvironmentJSON(raw, &expected) != nil {
		return false
	}
	found := false
	for i := range expected.Bootstrap.Domains {
		if expected.Bootstrap.Domains[i].Domain != "openbao_tls" {
			continue
		}
		names, err := correctedOpenBaoDNSPolicy(expected.Bootstrap.Domains[i].ServerDNSNames)
		if err != nil {
			return false
		}
		expected.Bootstrap.Domains[i].ServerDNSNames = names
		found = true
	}
	return found && deploymentplan.Digest(expected) == deploymentplan.Digest(current)
}

type environmentCreateJournalEntry struct {
	Key          string          `json:"key"`
	Phase        string          `json:"phase"`
	State        string          `json:"state"`
	IntentSHA256 string          `json:"intent_sha256"`
	Receipt      json.RawMessage `json:"receipt,omitempty"`
	StartedAt    time.Time       `json:"started_at"`
	CompletedAt  time.Time       `json:"completed_at,omitempty"`
}

type environmentCreateJournal struct {
	Schema                     string                          `json:"schema"`
	Environment                string                          `json:"environment"`
	Stack                      string                          `json:"stack"`
	PlanSHA256                 string                          `json:"plan_sha256"`
	ArtifactSHA256             string                          `json:"artifact_sha256"`
	OwnerToken                 string                          `json:"owner_token"`
	AdmittedAt                 time.Time                       `json:"admitted_at"`
	Entries                    []environmentCreateJournalEntry `json:"entries"`
	KubernetesLeases           []deploymentplan.LeaseOwner     `json:"kubernetes_leases,omitempty"`
	CompletedAt                time.Time                       `json:"completed_at,omitempty"`
	OriginalJournal            string                          `json:"original_journal,omitempty"`
	OriginalJournalSHA256      string                          `json:"original_journal_sha256,omitempty"`
	ProviderMaterialPlanSHA256 string                          `json:"provider_material_plan_sha256,omitempty"`
}

type environmentCreateOwner struct {
	Schema      string    `json:"schema"`
	Environment string    `json:"environment"`
	Stack       string    `json:"stack"`
	PlanSHA256  string    `json:"plan_sha256"`
	OwnerToken  string    `json:"owner_token"`
	Journal     string    `json:"journal"`
	StartedAt   time.Time `json:"started_at"`
}

type environmentCreateExecution struct {
	Store           secretStore
	Directory       string
	JournalPath     string
	Journal         environmentCreateJournal
	owner           environmentCreateOwner
	ownerInfo       os.FileInfo
	running         *os.File
	runningInfo     os.FileInfo
	lateSourceReuse *managedLateSourceReuse
	latePureSource  *managedLatePureSourceView
	volumeExecution *environmentCreateExecution // pure historical volume provenance; never a write executor
}

func startEnvironmentCreateExecution(ctx context.Context, cfg deploymentConfig, artifact *environmentCreateArtifact, operatorSHA string, admittedAt time.Time) (*environmentCreateExecution, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if artifact == nil {
		return nil, environmentCreateStateBlock("complete create qualification is required for admission")
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return nil, err
	}
	lock, err := lockEnvironmentPreflightPublication(store)
	if err != nil {
		return nil, err
	}
	defer lock.Close()
	current, err := loadEnvironmentCreateArtifact(cfg, operatorSHA, artifact.PlanSHA256, admittedAt)
	if err != nil || deploymentplan.Digest(current) != deploymentplan.Digest(artifact) {
		if err != nil {
			return nil, err
		}
		return nil, environmentCreateStateBlock("create admission does not match the original active qualification")
	}
	if err := checkLocalExecutionFence(store.Root); err != nil {
		return nil, err
	}
	parent, err := prepareEnvironmentCreateDirectories(store, filepath.Join("deployment", "create", "executions", artifact.PlanSHA256))
	if err != nil {
		return nil, err
	}
	prior, err := os.ReadDir(parent)
	if err != nil || len(prior) != 0 {
		return nil, environmentCreateStateBlocker("deployment.create.execution", "this create plan already has execution evidence or its history cannot be inspected")
	}
	fence := filepath.Join(store.Root, "deployment", ".execution-active")
	if err := os.Mkdir(fence, 0700); err != nil {
		return nil, environmentCreateStateBlocker("deployment.create.execution", "another controller or interrupted operation owns environment execution")
	}
	e := &environmentCreateExecution{Store: store}
	var succeeded bool
	defer func() {
		if !succeeded {
			_ = e.Close() // The exact fence and any partial evidence remain.
		}
	}()
	e.ownerInfo, err = os.Lstat(fence)
	if err != nil {
		return nil, environmentCreateStateBlocker("deployment.create.execution", "new create execution owner cannot be inspected")
	}
	e.running, err = deploymentplan.LockExecutionOwner(fence, true)
	if err != nil {
		return nil, err
	}
	e.runningInfo, err = e.running.Stat()
	if err != nil {
		return nil, err
	}
	e.Directory, err = os.MkdirTemp(parent, "execution-")
	if err != nil {
		return nil, environmentCreateStateBlocker("deployment.create.execution", "private create execution journal cannot be prepared")
	}
	var nonce [32]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	ownerToken := fmt.Sprintf("%x", nonce[:])
	e.JournalPath = filepath.Join(e.Directory, "journal.json")
	journalRelative, _ := filepath.Rel(store.Root, e.JournalPath)
	e.owner = environmentCreateOwner{Schema: environmentCreateOwnerSchema, Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], PlanSHA256: artifact.PlanSHA256, OwnerToken: ownerToken, Journal: filepath.ToSlash(journalRelative), StartedAt: admittedAt.UTC()}
	e.Journal = environmentCreateJournal{Schema: environmentCreateJournalSchema, Environment: cfg.Environment, Stack: e.owner.Stack, PlanSHA256: artifact.PlanSHA256, ArtifactSHA256: deploymentplan.Digest(artifact), OwnerToken: ownerToken, AdmittedAt: admittedAt.UTC(), Entries: []environmentCreateJournalEntry{}}
	if err := writeEnvironmentPreflightImmutableJSON(filepath.Join(fence, "owner.json"), e.owner); err != nil {
		return nil, err
	}
	if err := e.save(); err != nil {
		return nil, err
	}
	for _, directory := range []string{fence, e.Directory, parent, filepath.Dir(fence)} {
		if err := syncEnvironmentPreflightDirectory(directory); err != nil {
			return nil, err
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	succeeded = true
	return e, nil
}

func (e *environmentCreateExecution) Check(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if e == nil || e.running == nil || e.Store.Environment != e.owner.Environment || e.Store.Root != filepath.Join(e.Store.ConfigRoot, e.owner.Environment) || e.Journal.Environment != e.owner.Environment || e.Journal.Stack != e.owner.Stack || e.Journal.PlanSHA256 != e.owner.PlanSHA256 || e.Journal.OwnerToken != e.owner.OwnerToken {
		return environmentCreateStateBlocker("deployment.create.execution", "original create execution owner is not held")
	}
	fence := filepath.Join(e.Store.Root, "deployment", ".execution-active")
	ownerPath, pathErr := privateDeploymentStatePath(e.Store.Environment, filepath.Join("deployment", ".execution-active", "owner.json"))
	journalRelative, relErr := filepath.Rel(e.Store.Root, e.JournalPath)
	journalPath, journalErr := privateDeploymentStatePath(e.Store.Environment, journalRelative)
	if pathErr != nil || relErr != nil || journalErr != nil || ownerPath != filepath.Join(fence, "owner.json") || journalPath != e.JournalPath || filepath.ToSlash(journalRelative) != e.owner.Journal || e.JournalPath != filepath.Join(e.Directory, "journal.json") {
		return environmentCreateStateBlocker("deployment.create.execution", "original create owner and journal paths are not canonical private state")
	}
	info, err := os.Lstat(fence)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !os.SameFile(info, e.ownerInfo) {
		return environmentCreateStateBlocker("deployment.create.execution", "original create execution fence changed or cannot be verified")
	}
	info, err = os.Lstat(filepath.Join(fence, "running.lock"))
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || !os.SameFile(info, e.runningInfo) {
		return environmentCreateStateBlocker("deployment.create.execution", "original create process lock changed or cannot be verified")
	}
	if _, err := e.running.Stat(); err != nil {
		return environmentCreateStateBlocker("deployment.create.execution", "original create process lock is no longer held")
	}
	var owner environmentCreateOwner
	var journal environmentCreateJournal
	if readEnvironmentPreflightJSON(filepath.Join(fence, "owner.json"), &owner) != nil || deploymentplan.Digest(owner) != deploymentplan.Digest(e.owner) || readEnvironmentPreflightJSON(e.JournalPath, &journal) != nil || deploymentplan.Digest(journal) != deploymentplan.Digest(e.Journal) {
		return environmentCreateStateBlocker("deployment.create.execution", "original create owner or durable journal changed or cannot be verified")
	}
	return nil
}

func (e *environmentCreateExecution) save() error {
	invalidateManagedLateSourceReuse(e)
	raw, err := json.Marshal(e.Journal)
	if err != nil || len(raw) > environmentPreflightArtifactLimit {
		return environmentCreateStateBlocker("deployment.create.execution", "bounded create journal cannot be encoded")
	}
	relative, err := filepath.Rel(e.Store.Root, e.JournalPath)
	if err != nil || !strings.HasPrefix(filepath.ToSlash(relative), "deployment/create/executions/") || e.Store.write(relative, raw, true) != nil {
		return environmentCreateStateBlocker("deployment.create.execution", "create journal cannot be committed before mutation or polling")
	}
	return syncEnvironmentPreflightDirectory(e.Directory)
}

func (e *environmentCreateExecution) BeginEntry(key, phase string, intent json.RawMessage, now time.Time) error {
	if err := e.Check(context.Background()); err != nil {
		return err
	}
	if key == "" || phase == "" || len(key) > 256 || len(phase) > 128 || strings.ContainsAny(key+phase, "\r\n\x00") || !e.Journal.CompletedAt.IsZero() || now.Before(e.Journal.AdmittedAt) {
		return environmentCreateStateBlocker("deployment.create.execution", "exact create stage intent and active admission are required")
	}
	sha, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		return err
	}
	for _, entry := range e.Journal.Entries {
		if entry.Key == key {
			return environmentCreateStateBlocker("deployment.create.execution", "this create stage already has mutation intent; automatic replay is prohibited")
		}
	}
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: key, Phase: phase, State: "pending", IntentSHA256: sha, StartedAt: now.UTC()})
	return e.save()
}

func (e *environmentCreateExecution) RecordReceipt(key string, receipt json.RawMessage) error {
	if err := e.Check(context.Background()); err != nil {
		return err
	}
	canonical, err := canonicalEnvironmentCreateJSON(receipt)
	if err != nil {
		return err
	}
	for index, entry := range e.Journal.Entries {
		if entry.Key != key {
			continue
		}
		if entry.State == "allocated" && bytes.Equal(entry.Receipt, canonical) {
			return nil
		}
		if entry.State != "pending" || len(entry.Receipt) != 0 {
			return environmentCreateStateBlocker("deployment.create.execution", "create native receipt already exists; never replace the recorded outcome")
		}
		e.Journal.Entries[index].Receipt, e.Journal.Entries[index].State = canonical, "allocated"
		return e.save()
	}
	return environmentCreateStateBlocker("deployment.create.execution", "native receipt has no preceding durable create intent")
}

func (e *environmentCreateExecution) CompleteEntry(key string, now time.Time) error {
	if err := e.Check(context.Background()); err != nil {
		return err
	}
	for index, entry := range e.Journal.Entries {
		if entry.Key == key {
			if entry.State != "allocated" || len(entry.Receipt) == 0 || now.Before(entry.StartedAt) {
				return environmentCreateStateBlocker("deployment.create.execution", "create stage completion lacks a verified recorded native outcome")
			}
			e.Journal.Entries[index].State, e.Journal.Entries[index].CompletedAt = "completed", now.UTC()
			return e.save()
		}
	}
	return environmentCreateStateBlocker("deployment.create.execution", "create stage completion has no original intent")
}

func (e *environmentCreateExecution) RecordKubernetesLease(lease deploymentplan.LeaseOwner) error {
	if err := e.Check(context.Background()); err != nil {
		return err
	}
	if lease.ResourceID != "Lease/"+e.Journal.Stack+"-video-cloud/rtk-deployment-execution" || lease.UID == "" || lease.Holder == "" {
		return environmentCreateStateBlocker("deployment.create.execution", "exact environment Kubernetes execution lease receipt is required")
	}
	for _, previous := range e.Journal.KubernetesLeases {
		if previous == lease {
			return nil
		}
		if previous.UID == lease.UID {
			return environmentCreateStateBlocker("deployment.create.execution", "original Kubernetes execution lease owner changed")
		}
	}
	e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, lease)
	return e.save()
}

func (e *environmentCreateExecution) CompleteAndRelease(now time.Time) error {
	return e.CompleteAndReleaseContext(context.Background(), now)
}

func (e *environmentCreateExecution) CompleteAndReleaseContext(ctx context.Context, now time.Time) error {
	if err := e.Check(ctx); err != nil {
		return err
	}
	completed := e.Journal.CompletedAt
	if completed.IsZero() {
		completed = now.UTC()
	}
	if len(e.Journal.Entries) == 0 || completed.Before(e.Journal.AdmittedAt) || completed.After(now) {
		return environmentCreateStateBlocker("deployment.create.execution", "complete verified create stages are required before releasing ownership")
	}
	for _, entry := range e.Journal.Entries {
		if entry.State != "completed" || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || len(entry.Receipt) == 0 || completed.Before(entry.CompletedAt) {
			return environmentCreateStateBlocker("deployment.create.execution", "pending or uncertain create outcomes retain environment ownership")
		}
	}
	if e.Journal.CompletedAt.IsZero() {
		e.Journal.CompletedAt = completed
		if err := e.save(); err != nil {
			return err
		}
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	fence := filepath.Join(e.Store.Root, "deployment", ".execution-active")
	archive := filepath.Join(e.Directory, "owner")
	if _, err := os.Lstat(archive); !errors.Is(err, os.ErrNotExist) {
		return environmentCreateStateBlocker("deployment.create.execution", "create owner archive already exists or cannot be inspected")
	}
	if err := os.Rename(fence, archive); err != nil {
		return environmentCreateStateBlocker("deployment.create.execution", "original create owner cannot be archived after verified completion")
	}
	if err := syncEnvironmentPreflightDirectory(filepath.Dir(fence)); err != nil {
		return err
	}
	if err := syncEnvironmentPreflightDirectory(e.Directory); err != nil {
		return err
	}
	return e.Close()
}

func (e *environmentCreateExecution) Close() error {
	if e == nil || e.running == nil {
		return nil
	}
	err := e.running.Close()
	e.running = nil
	return err
}
