package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const openBaoPublicCRLStage = "service-core-openbao-public-crls"
const openBaoPublicCRLSchema = "rtk-cloud.create-openbao-public-crl/v1"

// Only external observations vary in isolated tests. Source/native parent,
// owner, lease, plan admission and recorded execution always use the real code.
type openBaoPublicCRLObservations struct {
	tool    func(context.Context, deploymentConfig, secretStore, *environmentCreateExecution, *environmentCreatePlan) (failedForwardTool, error)
	custody func(deploymentConfig, secretStore, string, ...*environmentCreateExecution) (*environmentCustodyBinding, error)
	records func(context.Context, deploymentConfig, environmentCreateBootstrapRuntime, []createOpenBaoPublicCRLPurpose) ([]createRootCRLRecord, error)
	command environmentReleaseCommand
}

func openBaoPublicCRLReadAdapters(cfg deploymentConfig, selected []openBaoPublicCRLObservations) (openBaoPublicCRLObservations, error) {
	if len(selected) == 0 {
		return openBaoPublicCRLObservations{selectOpenBaoPublicCRLTool, readInitializedCreateCustodyBinding, readOpenBaoPublicCRLRecords, environmentCIImageCommand(cfg)}, nil
	}
	if len(selected) != 1 || selected[0].tool == nil || selected[0].custody == nil || selected[0].records == nil || selected[0].command == nil {
		return openBaoPublicCRLObservations{}, openBaoPublicCRLBlock("complete-observation-adapters")
	}
	return selected[0], nil
}

// A recorded public-cache Job can still be running when this same producer is
// qualified for retry. AdmitResumable verifies its literal journal, UID and
// desired body; only execution waits for success and authenticates Job/Pod output.
// This wrapper is never passed to execution or completed-producer observation.
type openBaoPublicCRLAdmissionBackend struct {
	deploymentplan.Backend
}

func (b openBaoPublicCRLAdmissionBackend) Wait(ctx context.Context, r deploymentplan.Resource) error {
	if deploymentplan.String(r.Desired["kind"]) == "Job" {
		return ctx.Err()
	}
	return b.Backend.Wait(ctx, r)
}

type createOpenBaoPublicCRLQualification struct {
	Schema, Parent, OwnerSHA, ConfigurationSHA, CheckerSHA string
	SourceJournal                                          environmentCreateJournal
	SourcePlanSHA, SourceChildSHA                          string
	SourceDefinitions                                      []deploymentplan.Definition
	Owners                                                 map[string]string
	CustodySHA                                             string
	Tool                                                   failedForwardTool
	Purposes                                               []createOpenBaoPublicCRLPurpose
	CRLs                                                   []createRootCRLRecord
	Plan                                                   *deploymentplan.Plan
	QualifiedAt, ValidUntil                                time.Time
}

func openBaoPublicCRLBase(parent string) string {
	return filepath.Join("deployment/create", parent, "openbao-public-crl")
}

func readSelectedOpenBaoPublicCRL(store secretStore, e *environmentCreateExecution) (*createOpenBaoPublicCRLQualification, string, error) {
	path, err := store.safePath(filepath.Join(openBaoPublicCRLBase(e.Journal.PlanSHA256), "selected.json"))
	if err != nil {
		return nil, "", err
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, "", nil
	} else if err != nil {
		return nil, "", err
	}
	var selected struct {
		Review string `json:"review_sha256"`
	}
	if readExecutionJSON(path, &selected) != nil || !environmentPreflightDigest.MatchString(selected.Review) {
		return nil, "", openBaoPublicCRLBlock("selected-qualification")
	}
	var q createOpenBaoPublicCRLQualification
	if readExecutionJSON(filepath.Join(store.Root, openBaoPublicCRLBase(e.Journal.PlanSHA256), selected.Review, "qualification.json"), &q) != nil || deploymentplan.Digest(q) != selected.Review {
		return nil, "", openBaoPublicCRLBlock("immutable-qualification")
	}
	return &q, selected.Review, nil
}

// Read the real current signed record directly from the original registry.
// CertIssuer's HTTP listener and its pending provider transport are not used.
func readOpenBaoPublicCRLRecords(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, purposes []createOpenBaoPublicCRLPurpose) ([]createRootCRLRecord, error) {
	var records []createRootCRLRecord
	for _, purpose := range purposes {
		i := purpose.Issuer
		if i.Environment != cfg.Environment || i.Domain != "openbao_tls" || !environmentBootstrapOwnerUUID.MatchString(i.ID) {
			return nil, openBaoPublicCRLBlock("original-issuer")
		}
		query := "SELECT json_build_object('document',document,'status',status,'environment',environment,'domain',domain,'crl',(SELECT json_build_object('issuer_id',issuer_id,'crl_sha256',digest,'crl_number',number::text,'crl_pem',pem,'this_update',this_update,'next_update',next_update) FROM pki_crls WHERE issuer_id='" + i.ID + "' ORDER BY number DESC LIMIT 1)) FROM pki_issuers WHERE id='" + i.ID + "'"
		raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
		var current struct {
			Document    deploymentplan.Object `json:"document"`
			Status      string                `json:"status"`
			Environment string                `json:"environment"`
			Domain      string                `json:"domain"`
			CRL         createRootCRLRecord   `json:"crl"`
		}
		if err != nil || decodeEnvironmentJSON(raw, &current) != nil || current.Status != "active" || current.Environment != cfg.Environment || current.Domain != i.Domain || deploymentplan.Digest(current.Document) != deploymentplan.Digest(i.Document) {
			return nil, openBaoPublicCRLBlock("current-issuer-registry")
		}
		if _, err := validateMaintenanceCRL(cfg.Environment, i, current.CRL, true, time.Now().UTC()); err != nil {
			return nil, openBaoPublicCRLBlock("current-signed-crl")
		}
		records = append(records, current.CRL)
	}
	return records, nil
}

func openBaoPublicCRLSource(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, definitions []deploymentplan.Definition) (string, string, map[string]string, error) {
	owners := map[string]string{}
	source, err := readEnvironmentCreateStageInput(store, e, "managed-complete-issuer")
	if err != nil {
		return "", "", nil, err
	}
	sourceSHA, childSHA := "", ""
	var child deploymentplan.Journal
	if source != nil {
		var exists bool
		child, exists, err = source.ReadExecution(store.Root)
		if err != nil || !exists || child.InFlight != "" || source.ConfigurationSHA256 != deploymentplan.Digest(cfg) {
			return "", "", nil, openBaoPublicCRLBlock("original-source-child")
		}
		if _, exists, err := readEnvironmentCreateStageProgress(store, e, "managed-complete-issuer", source); err != nil || !exists {
			return "", "", nil, openBaoPublicCRLBlock("original-source-native-parent")
		}
		sourceSHA, childSHA = source.SHA256(), deploymentplan.Digest(child)
	}
	for _, role := range []string{"pki-controller", "certissuer"} {
		id := "Deployment/" + e.Journal.Stack + "-video-cloud/" + role
		live, err := b.Read(ctx, id)
		if err != nil || live == nil || deploymentplan.Map(live["metadata"])["deletionTimestamp"] != nil {
			return "", "", nil, openBaoPublicCRLBlock("original-provider-owner")
		}
		uid := deploymentplan.String(deploymentplan.Map(live["metadata"])["uid"])
		if !environmentBootstrapOwnerUUID.MatchString(uid) {
			return "", "", nil, openBaoPublicCRLBlock("original-provider-uid")
		}
		known := false
		if source != nil {
			for _, r := range source.Resources {
				if r.ID == id && r.UID == uid && (child.Owners[id] == "" || child.Owners[id] == uid) && (deploymentplan.Matches(r.Desired, live) || r.Baseline != nil && deploymentplan.Matches(r.Baseline, live)) {
					known = true
				}
			}
		}
		if !known {
			accepted, err := readLatestAcceptedCreateResource(ctx, store, e, b, id)
			if err != nil || accepted == nil || !deploymentplan.Matches(accepted, live) {
				return "", "", nil, openBaoPublicCRLBlock("recorded-provider-history")
			}
		}
		owners[id] = uid
	}
	return sourceSHA, childSHA, owners, e.Check(ctx)
}

func prepareOpenBaoPublicCRLQualification(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, core []deploymentplan.Definition, selectedObservations ...openBaoPublicCRLObservations) (*createOpenBaoPublicCRLQualification, string, error) {
	observations, err := openBaoPublicCRLReadAdapters(cfg, selectedObservations)
	if err != nil {
		return nil, "", err
	}
	e := runtime.Execution
	if e == nil || e.Store != store || parent == nil || e.Check(ctx) != nil || deploymentplan.Digest(cfg) != parent.ConfigurationSHA256 {
		return nil, "", openBaoPublicCRLBlock("original-parent")
	}
	if q, review, err := readSelectedOpenBaoPublicCRL(store, e); err != nil || q != nil {
		if err != nil {
			return nil, "", err
		}
		entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+openBaoPublicCRLStage)
		if entry != nil && entry.State == "completed" {
			return q, review, nil
		}
		checker, err := environmentPreflightCheckerSHA256()
		if err != nil {
			return nil, "", err
		}
		currentRecords, err := observations.records(ctx, cfg, runtime, q.Purposes)
		if err != nil {
			return nil, "", err
		}
		if checker == q.CheckerSHA && time.Now().UTC().Before(q.ValidUntil) && deploymentplan.Digest(currentRecords) == deploymentplan.Digest(q.CRLs) {
			return q, review, nil
		}
		// Requalify the same selected plan/tool; retain the old qualification
		// and every original native Job/session deadline unchanged.
		var next createOpenBaoPublicCRLQualification
		if json.Unmarshal(mustProviderRecoveryJSON(q), &next) != nil {
			return nil, "", openBaoPublicCRLBlock("prior-selected-proof")
		}
		next.QualifiedAt, next.ValidUntil, next.CheckerSHA = time.Now().UTC(), time.Now().UTC().Add(time.Hour), checker
		if json.Unmarshal(mustProviderRecoveryJSON(e.Journal), &next.SourceJournal) != nil {
			return nil, "", openBaoPublicCRLBlock("renewed-source-journal")
		}
		next.CRLs = currentRecords
		nextReview := deploymentplan.Digest(next)
		if err := verifyOpenBaoPublicCRLQualification(ctx, cfg, store, parent, runtime, &next, nextReview, observations); err != nil {
			return nil, "", err
		}
		if err := writeOpenBaoPublicCRLQualification(store, &next); err != nil {
			return nil, "", err
		}
		_, currentReview, err := readSelectedOpenBaoPublicCRL(store, e)
		if err != nil || currentReview != review || e.Check(ctx) != nil {
			return nil, "", openBaoPublicCRLBlock("qualification-pointer-changed")
		}
		if err := store.write(filepath.Join(openBaoPublicCRLBase(next.Parent), "selected.json"), mustProviderRecoveryJSON(map[string]string{"review_sha256": nextReview}), true); err != nil {
			return nil, "", err
		}
		return &next, nextReview, e.Check(ctx)
	}
	binding, err := observations.custody(cfg, store, e.Journal.PlanSHA256, e)
	if err != nil {
		return nil, "", err
	}
	purposes, err := originalOpenBaoPublicCRLPurposes(cfg.Environment, parent.Stack, core)
	if err != nil || purposes[0].Issuer.RootFingerprint != binding.OpenBaoRootSHA256 {
		return nil, "", openBaoPublicCRLBlock("original-custody-root")
	}
	tool, err := observations.tool(ctx, cfg, store, e, parent)
	if err != nil {
		return nil, "", err
	}
	definitions, err := renderEnvironmentCreateOpenBaoPublicCRLDefinitions(ctx, cfg, parent.Bootstrap, service, core, tool.Image)
	if err != nil {
		return nil, "", err
	}
	definitions, err = projectEnvironmentCustodyResources(cfg.Values, cfg.Environment, binding, "", definitions)
	if err != nil {
		return nil, "", err
	}
	backend := environmentCreateKubernetesBackend{environmentKubernetesBackend: runtime.Backend, execution: e, cfg: cfg, store: store}
	sourceSHA, childSHA, owners, err := openBaoPublicCRLSource(ctx, cfg, store, e, backend, core)
	if err != nil {
		return nil, "", err
	}
	records, err := observations.records(ctx, cfg, runtime, purposes)
	if err != nil {
		return nil, "", err
	}
	live, err := runtime.Backend.Inventory(ctx, parent.Stack)
	if err != nil {
		return nil, "", err
	}
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: parent.Stack, Operation: "scoped", ConfigurationSHA256: parent.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: environmentCreateStageBindingsSHA(openBaoPublicCRLStage, definitions)}, definitions, live)
	if err != nil {
		return nil, "", err
	}
	if err := plan.AdmitResumable(ctx, openBaoPublicCRLAdmissionBackend{backend}, store.Root); err != nil {
		return nil, "", err
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return nil, "", err
	}
	var sourceJournal environmentCreateJournal
	if json.Unmarshal(mustProviderRecoveryJSON(e.Journal), &sourceJournal) != nil {
		return nil, "", openBaoPublicCRLBlock("source-journal")
	}
	now := time.Now().UTC()
	q := &createOpenBaoPublicCRLQualification{Schema: openBaoPublicCRLSchema, Parent: e.Journal.PlanSHA256, OwnerSHA: deploymentplan.Digest(e.owner), ConfigurationSHA: parent.ConfigurationSHA256, CheckerSHA: checker, SourceJournal: sourceJournal, SourcePlanSHA: sourceSHA, SourceChildSHA: childSHA, SourceDefinitions: core, Owners: owners, CustodySHA: deploymentplan.Digest(binding), Tool: tool, Purposes: purposes, CRLs: records, Plan: plan, QualifiedAt: now, ValidUntil: now.Add(time.Hour)}
	review := deploymentplan.Digest(q)
	if err := writeOpenBaoPublicCRLQualification(store, q); err != nil {
		return nil, "", err
	}
	if _, err := writeProductArtifact(store, filepath.Join(openBaoPublicCRLBase(q.Parent), "selected.json"), map[string]string{"review_sha256": review}); err != nil {
		return nil, "", err
	}
	return q, review, e.Check(ctx)
}

func verifyOpenBaoPublicCRLQualification(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, runtime environmentCreateBootstrapRuntime, q *createOpenBaoPublicCRLQualification, review string, selectedObservations ...openBaoPublicCRLObservations) error {
	observations, err := openBaoPublicCRLReadAdapters(cfg, selectedObservations)
	if err != nil {
		return err
	}
	e := runtime.Execution
	if e == nil || q == nil || q.Schema != openBaoPublicCRLSchema || q.Plan == nil || q.Parent != e.Journal.PlanSHA256 || q.OwnerSHA != deploymentplan.Digest(e.owner) || deploymentplan.Digest(q) != review || q.ConfigurationSHA != deploymentplan.Digest(cfg) || parent.ConfigurationSHA256 != q.ConfigurationSHA || !partialSessionJournalExtends(q.SourceJournal, e.Journal) || e.Check(ctx) != nil {
		return openBaoPublicCRLBlock("qualified-original-scope")
	}
	completed := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+openBaoPublicCRLStage)
	if completed == nil || completed.State != "completed" {
		checker, err := environmentPreflightCheckerSHA256()
		if err != nil || checker != q.CheckerSHA || q.QualifiedAt.IsZero() || time.Now().UTC().Before(q.QualifiedAt) || !time.Now().UTC().Before(q.ValidUntil) {
			return openBaoPublicCRLBlock("qualified-clock-or-checker")
		}
	}
	backend := environmentCreateKubernetesBackend{environmentKubernetesBackend: runtime.Backend, execution: e, cfg: cfg, store: store}
	sourceSHA, childSHA, owners, err := openBaoPublicCRLSource(ctx, cfg, store, e, backend, q.SourceDefinitions)
	if err != nil || !openBaoPublicCRLSourceSnapshotMatches(q, sourceSHA, childSHA, owners, completed != nil && completed.State == "completed") {
		return openBaoPublicCRLBlock("original-source-or-owners")
	}
	binding, err := observations.custody(cfg, store, q.Parent, e)
	if err != nil || deploymentplan.Digest(binding) != q.CustodySHA {
		return openBaoPublicCRLBlock("original-custody-scope")
	}
	purposes, err := originalOpenBaoPublicCRLPurposes(cfg.Environment, parent.Stack, q.SourceDefinitions)
	if err != nil || deploymentplan.Digest(purposes) != deploymentplan.Digest(q.Purposes) {
		return openBaoPublicCRLBlock("original-public-purposes")
	}
	records, err := observations.records(ctx, cfg, runtime, purposes)
	if err != nil {
		return err
	}
	if completed == nil || completed.State != "completed" {
		if deploymentplan.Digest(records) != deploymentplan.Digest(q.CRLs) {
			return openBaoPublicCRLBlock("frozen-current-crls")
		}
		proofCtx := newEnvironmentReleaseProofCapture(ctx)
		if _, err := verifyEnvironmentPublication(proofCtx, q.Tool.Publication.Repository, q.Tool.Publication.RunID, q.Tool.Image, false, observations.command); err != nil {
			return err
		}
		verified := snapshotVerifiedReleaseProofs(proofCtx, environmentRelease{Images: map[string]environmentReleaseImage{"LKE_VIDEO_CLOUD_IMAGE": q.Tool.Image}})
		if len(verified) != 1 {
			return openBaoPublicCRLBlock("exact-helper-publication")
		}
		verified[0].ObservedAt = q.Tool.Publication.ObservedAt
		if deploymentplan.Digest(verified[0]) != deploymentplan.Digest(q.Tool.Publication) {
			return openBaoPublicCRLBlock("exact-helper-publication")
		}
		caps, err := observations.command(ctx, "docker", "run", "--rm", "--platform", "linux/amd64", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", "/app/serviceidentity-bootstrap", q.Tool.Image.Reference, "--deployment-capabilities")
		canonical, capErr := canonicalFailedForwardCapabilities(caps)
		var advertised struct {
			Schema, Service string
			Features        []string
		}
		if err != nil || capErr != nil || decodeEnvironmentJSON(canonical, &advertised) != nil || !slices.Contains(advertised.Features, openBaoPublicCRLCapability) || deploymentplan.Digest(json.RawMessage(canonical)) != deploymentplan.Digest(q.Tool.Capabilities) {
			return openBaoPublicCRLBlock("exact-helper-capability")
		}
		return q.Plan.AdmitResumable(ctx, openBaoPublicCRLAdmissionBackend{backend}, store.Root)
	}
	observed, err := verifyAcceptedStageAfterRecordedHistory(ctx, store, e, backend, "kubernetes/"+openBaoPublicCRLStage)
	if err != nil || observed.SHA256() != q.Plan.SHA256() {
		return openBaoPublicCRLBlock("completed-native-producer")
	}
	return e.Check(ctx)
}

func executeOpenBaoPublicCRLQualification(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, runtime environmentCreateBootstrapRuntime, q *createOpenBaoPublicCRLQualification, review string, out io.Writer, selectedObservations ...openBaoPublicCRLObservations) error {
	if err := verifyOpenBaoPublicCRLQualification(ctx, cfg, store, parent, runtime, q, review, selectedObservations...); err != nil {
		return err
	}
	entry := createOpenBaoACKCompletionEntry(runtime.Execution.Journal, "kubernetes/"+openBaoPublicCRLStage)
	if entry != nil && entry.State == "completed" {
		return runtime.Execution.Check(ctx)
	}
	bounded, cancel := context.WithDeadline(ctx, q.ValidUntil)
	defer cancel()
	if err := executeCompiledEnvironmentCreateStage(withEnvironmentCreateBootstrapRuntime(bounded, runtime), cfg, store, parent, runtime.Execution, openBaoPublicCRLStage, q.Plan, runtime.Backend, out); err != nil {
		return err
	}
	return verifyOpenBaoPublicCRLQualification(ctx, cfg, store, parent, runtime, q, review, selectedObservations...)
}

func openBaoPublicCRLBlock(boundary string) error {
	return createProviderRecoveryBlock("original OpenBao public CRL producer differs: " + boundary)
}

func openBaoPublicCRLSourceSnapshotMatches(q *createOpenBaoPublicCRLQualification, sourceSHA, childSHA string, owners map[string]string, completed bool) bool {
	return q != nil && (q.SourcePlanSHA == "" || sourceSHA == q.SourcePlanSHA && (completed || childSHA == q.SourceChildSHA)) && deploymentplan.Digest(owners) == deploymentplan.Digest(q.Owners)
}

// The proof includes the whole authenticated journal and native source bodies,
// so it uses the existing bounded immutable qualification writer rather than
// the small public-ceremony record writer. Existing proof bytes never change.
func writeOpenBaoPublicCRLQualification(store secretStore, q *createOpenBaoPublicCRLQualification) error {
	if q == nil || q.Schema != openBaoPublicCRLSchema || !environmentPreflightDigest.MatchString(q.Parent) || q.SourceJournal.PlanSHA256 != q.Parent {
		return openBaoPublicCRLBlock("qualification-shape")
	}
	raw, err := json.Marshal(q)
	if err != nil || len(raw) > environmentPreflightArtifactLimit {
		return openBaoPublicCRLBlock("bounded-qualification")
	}
	relative := filepath.Join(openBaoPublicCRLBase(q.Parent), deploymentplan.Digest(q), "qualification.json")
	path, err := store.safePath(relative)
	if err != nil {
		return err
	}
	if prior, err := readBoundedDerivedArtifact(path); err == nil {
		if !bytes.Equal(prior, raw) {
			return openBaoPublicCRLBlock("immutable-qualification-bytes")
		}
		return nil
	} else if !os.IsNotExist(err) {
		if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
			return openBaoPublicCRLBlock("unsafe-qualification-file")
		}
	}
	if _, err := prepareEnvironmentCreateDirectories(store, filepath.Dir(relative)); err != nil {
		return err
	}
	return writeEnvironmentPreflightImmutableJSON(path, q)
}

// Fresh bootstrap already admitted the exact main API publication and required
// its public producer capability. Legacy interrupted generations instead use
// the explicitly recorded helper publication; neither path finds a latest run.
func selectOpenBaoPublicCRLTool(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, parent *environmentCreatePlan) (failedForwardTool, error) {
	if !openBaoPublicCRLFreshParent(parent, e) {
		return selectFailedForwardToolForPurpose(ctx, store, e, environmentCIImageCommand(cfg), openBaoPublicCRLToolPurpose)
	}
	var empty failedForwardTool
	image := parent.Release.Images["LKE_VIDEO_CLOUD_IMAGE"]
	base := filepath.Join(store.Root, "deployment/create/preflight", e.Journal.PlanSHA256)
	entries, err := os.ReadDir(base)
	if err != nil {
		return empty, openBaoPublicCRLBlock("original-fresh-publication")
	}
	var selected *environmentReleasePublicationProof
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		var a environmentCreateArtifact
		if readExecutionJSON(filepath.Join(base, entry.Name(), "artifact.json"), &a) != nil || deploymentplan.Digest(a) != e.Journal.ArtifactSHA256 {
			continue
		}
		if validateEnvironmentPreflightReleaseProofs(environmentPreflightProof{Environment: cfg.Environment, Stack: parent.Stack, QualifiedAt: a.QualifiedAt, ReleaseProofs: a.ReleaseProofs}, parent.Release) != nil {
			return empty, openBaoPublicCRLBlock("admitted-fresh-publication")
		}
		for _, proof := range a.ReleaseProofs {
			if proof.ImageKey == "LKE_VIDEO_CLOUD_IMAGE" && proof.Reference == image.Reference && proof.SourceRevision == image.SourceRevision {
				if selected != nil {
					return empty, openBaoPublicCRLBlock("ambiguous-fresh-publication")
				}
				copy := proof
				selected = &copy
			}
		}
	}
	if selected == nil {
		return empty, openBaoPublicCRLBlock("missing-fresh-publication")
	}
	run := environmentCIImageCommand(cfg)
	if _, err := verifyEnvironmentPublication(ctx, selected.Repository, selected.RunID, image, false, run); err != nil {
		return empty, err
	}
	raw, err := run(ctx, "docker", "run", "--rm", "--platform", "linux/amd64", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", "/app/serviceidentity-bootstrap", image.Reference, "--deployment-capabilities")
	canonical, capErr := canonicalFailedForwardCapabilities(raw)
	var caps struct {
		Schema, Service string
		Features        []string
	}
	if err != nil || capErr != nil || decodeEnvironmentJSON(canonical, &caps) != nil || !slices.Contains(caps.Features, openBaoPublicCRLCapability) {
		return empty, openBaoPublicCRLBlock("fresh-native-capability")
	}
	return failedForwardTool{Image: image, Publication: *selected, Capabilities: canonical}, e.Check(ctx)
}

func openBaoPublicCRLFreshParent(parent *environmentCreatePlan, e *environmentCreateExecution) bool {
	if parent == nil || e == nil {
		return false
	}
	sha, err := environmentCreatePlanSHA256(mustProviderRecoveryJSON(parent))
	return err == nil && sha == e.Journal.PlanSHA256
}
