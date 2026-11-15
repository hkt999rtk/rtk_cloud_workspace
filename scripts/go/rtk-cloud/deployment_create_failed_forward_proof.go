package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const failedForwardProofSchema = "rtk-cloud.failed-stage-forward/v1"
const failedForwardRequiredCapability = "initial-public-crl-custody-v2"

// A separate qualification preserves the admitted original operation. This
// data contract binds the specific child, candidate and tool evidence; callers
// must independently authenticate the original admission, capture and verify
// publication via the normal CI verifier, execute the capability probe in the
// exact tool image, and re-render/admit the candidate before producing a GO.
type createFailedForwardProof struct {
	// Runtime-only selection; the original qualification hash is unchanged.
	initializer             *createInitializerForwardProof
	Schema                  string                             `json:"schema"`
	Environment             string                             `json:"environment"`
	ParentOwnerSHA256       string                             `json:"parent_owner_sha256"`
	OriginalAdmissionSHA256 string                             `json:"original_admission_sha256"`
	SourceJournal           environmentCreateJournal           `json:"source_journal"`
	FailedStage             createFailedStageQualification     `json:"failed_stage"`
	CandidateSHA256         string                             `json:"candidate_sha256"`
	RetainCandidateSHA256   string                             `json:"retain_candidate_sha256"`
	ToolImage               environmentReleaseImage            `json:"tool_image"`
	ToolPublication         environmentReleasePublicationProof `json:"tool_publication"`
	Capabilities            json.RawMessage                    `json:"capabilities"`
	CapabilitiesSHA256      string                             `json:"capabilities_sha256"`
	QualifiedAt             time.Time                          `json:"qualified_at"`
	ValidUntil              time.Time                          `json:"valid_until"`
}

// Canonicalize before binding or comparing capabilities: json.RawMessage is
// compacted when persisted, while native JSON output normally ends in a newline.
// Reject unknown fields, extra JSON values and invalid capability content.
func canonicalFailedForwardCapabilities(raw []byte) (json.RawMessage, error) {
	bad := func() (json.RawMessage, error) {
		return nil, createProviderRecoveryBlock("failed-stage capability content is invalid")
	}
	if len(raw) == 0 || len(raw) > 16<<10 {
		return bad()
	}
	var caps struct {
		Schema   string   `json:"schema"`
		Service  string   `json:"service"`
		Features []string `json:"features"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&caps) != nil || decoder.Decode(new(any)) != io.EOF || caps.Schema != "rtk-cloud.deployment-capabilities/v1" || caps.Service != "serviceidentity-bootstrap" || !slices.Contains(caps.Features, failedForwardRequiredCapability) {
		return bad()
	}
	canonical, err := json.Marshal(caps)
	if err != nil {
		return bad()
	}
	return canonical, nil
}

// validateCreateFailedForwardProofBindings verifies content and provenance
// bindings, not external CI success or that capability bytes were executed.
// Source arguments must themselves originate from authenticated live checks.
func validateCreateFailedForwardProofBindings(q createFailedForwardProof, admitted issuerEnrollmentContinuationProof, admissionSHA string, stage createFailedStageQualification, candidate, retained *deploymentplan.Plan, now time.Time) error {
	bad := func(category string) error {
		return createProviderRecoveryBlock("failed-stage repair proof rejected: " + category)
	}
	admitted.QualifiedAt, admitted.ValidUntil = time.Time{}, time.Time{}
	if q.Schema != failedForwardProofSchema || !secretEnvironmentPattern.MatchString(q.Environment) || !environmentPreflightDigest.MatchString(admissionSHA) || deploymentplan.Digest(admitted) != admissionSHA || q.OriginalAdmissionSHA256 != admissionSHA || q.ParentOwnerSHA256 != admitted.ParentOwner || !environmentPreflightDigest.MatchString(q.ParentOwnerSHA256) || stage.ParentSHA256 != admitted.Parent || deploymentplan.Digest(q.FailedStage) != deploymentplan.Digest(stage) {
		return bad("original admission")
	}
	if !environmentPreflightDigest.MatchString(stage.ParentSHA256) || !environmentPreflightDigest.MatchString(stage.ParentJournalSHA256) || !environmentPreflightDigest.MatchString(stage.ParentIntentSHA256) || !secretEnvironmentPattern.MatchString(stage.Stage) || !environmentPreflightDigest.MatchString(stage.Child.PlanSHA256) || !environmentPreflightDigest.MatchString(stage.Child.JournalSHA256) || stage.Child.FailedResource == "" || stage.Child.Lease != (deploymentplan.LeaseOwner{}) {
		return bad("original stage identity")
	}
	if deploymentplan.Digest(q.SourceJournal) != stage.ParentJournalSHA256 || q.SourceJournal.PlanSHA256 != stage.ParentSHA256 || q.SourceJournal.Environment != q.Environment || q.SourceJournal.Stack == "" || !q.SourceJournal.CompletedAt.IsZero() {
		return bad("original parent journal")
	}
	sourceEntry := createOpenBaoACKCompletionEntry(q.SourceJournal, "kubernetes/"+stage.Stage)
	if sourceEntry == nil || sourceEntry.State != "pending" || sourceEntry.IntentSHA256 != stage.ParentIntentSHA256 || len(sourceEntry.Receipt) != 0 || !sourceEntry.CompletedAt.IsZero() {
		return bad("original parent stage")
	}
	if q.QualifiedAt.IsZero() || q.QualifiedAt.After(now) || !q.ValidUntil.Equal(q.QualifiedAt.Add(time.Hour)) || !now.Before(q.ValidUntil) {
		return bad("qualification validity")
	}
	if candidate == nil || candidate.Environment != q.Environment || candidate.Stack != q.SourceJournal.Stack || candidate.Operation != "scoped" || len(candidate.Resources) == 0 || q.CandidateSHA256 != candidate.SHA256() {
		return bad("repair plan binding")
	}
	if retained == nil || retained.Environment != q.Environment || retained.Stack != candidate.Stack || retained.Operation != "scoped" || len(retained.Resources) == 0 || q.RetainCandidateSHA256 != retained.SHA256() {
		return bad("retained plan binding")
	}
	for _, resource := range retained.Resources {
		if resource.Action != "retain" || resource.UID == "" {
			return bad("retained owner binding")
		}
	}
	image, pub := q.ToolImage, q.ToolPublication
	if !immutableEnvironmentImage.MatchString(image.Reference) || !deploymentUpgradeSHA.MatchString(image.SourceRevision) || image.RetainedPlanSHA256 != "" || pub.ImageKey != "LKE_VIDEO_CLOUD_IMAGE" || pub.Reference != image.Reference || pub.SourceRevision != image.SourceRevision || pub.Backup || pub.Workflow != "release.yml" || pub.ArtifactName != "image-manifest" || pub.RunID <= 0 || pub.RunAttempt <= 0 || pub.ArtifactID <= 0 || !strings.HasPrefix(image.Reference, "ghcr.io/"+pub.Repository+"/") || !environmentPreflightDigest.MatchString(pub.RunSHA256) || !environmentPreflightDigest.MatchString(pub.InventorySHA256) || !environmentPreflightDigest.MatchString(pub.ArchiveSHA256) || pub.ManifestSHA256 != environmentPreflightBytesSHA256(pub.Manifest) || verifyPublicationJSON(pub.Manifest, pub.Repository, pub.RunID, pub.RunAttempt, image, false) != nil {
		return bad("formal image publication")
	}
	if pub.ObservedAt.IsZero() || pub.ObservedAt.After(q.QualifiedAt) {
		return bad("formal publication observation time")
	}
	if image.OTARegistration != nil {
		registration, err := publishedOTARegistration(pub.Manifest, image)
		if err != nil || registration == nil || deploymentplan.Digest(registration) != deploymentplan.Digest(image.OTARegistration) {
			return bad("formal image registration descriptor")
		}
	}
	canonical, err := canonicalFailedForwardCapabilities(q.Capabilities)
	if err != nil {
		return bad("capability content")
	}
	if q.CapabilitiesSHA256 != environmentPreflightBytesSHA256(canonical) {
		return bad("capability digest")
	}
	jobs := 0
	for _, resource := range candidate.Resources {
		if resource.Desired["kind"] != "Job" {
			continue
		}
		pod := environmentPKIPod(resource.Desired)
		if pod == nil {
			return bad("repair job pod")
		}
		containers := certIssuerObjectList(pod["containers"])
		if len(containers) != 1 {
			return bad("repair job containers")
		}
		container := deploymentplan.Map(containers[0])
		if container["image"] != image.Reference {
			return bad("repair job image")
		}
		command := certIssuerObjectList(container["command"])
		if len(command) < 5 || command[0] != "/app/serviceidentity-bootstrap" || command[1] != "initialize-public-crl" || (len(command)-2)%3 != 0 {
			return bad("repair job command")
		}
		jobs++
	}
	if jobs == 0 {
		return bad("repair job missing")
	}
	return nil
}

// verifyCreateFailedForwardLiveEvidence re-observes CI, runs the capability
// command from the exact immutable image, and dry-runs a re-rendered candidate.
// The caller must pass an already authenticated admission and the result of a
// fresh original-parent stage qualification, plus a renderer that independently
// derives definitions from the authenticated manifests, owners and issuers.
type verifiedCreateFailedForward struct{ proofSHA string }

func verifyCreateFailedForwardLiveEvidence(ctx context.Context, q createFailedForwardProof, admitted issuerEnrollmentContinuationProof, admissionSHA string, stage createFailedStageQualification, now time.Time, privateRoot string, render func(context.Context) (*deploymentplan.Plan, *deploymentplan.Plan, error), backend deploymentplan.Backend, run environmentReleaseCommand) (*verifiedCreateFailedForward, error) {
	bad := func(category string) error {
		return createProviderRecoveryBlock("failed-stage live evidence rejected: " + category)
	}
	if render == nil || backend == nil || run == nil {
		return nil, bad("verification adapters")
	}
	proofCtx := newEnvironmentReleaseProofCapture(ctx)
	if _, err := verifyEnvironmentPublication(proofCtx, q.ToolPublication.Repository, q.ToolPublication.RunID, q.ToolImage, false, run); err != nil {
		return nil, bad("formal publication verification")
	}
	release := environmentRelease{Images: map[string]environmentReleaseImage{"LKE_VIDEO_CLOUD_IMAGE": q.ToolImage}}
	observed := snapshotVerifiedReleaseProofs(proofCtx, release)
	if len(observed) != 1 {
		return nil, bad("formal publication evidence count")
	}
	before, after := q.ToolPublication, observed[0]
	before.ObservedAt, after.ObservedAt = time.Time{}, time.Time{}
	if deploymentplan.Digest(before) != deploymentplan.Digest(after) {
		return nil, bad("formal publication evidence changed")
	}
	if _, err := run(ctx, "docker", "pull", "--platform", "linux/amd64", q.ToolImage.Reference); err != nil {
		return nil, bad("exact tool image pull")
	}
	caps, err := run(ctx, "docker", "run", "--rm", "--platform", "linux/amd64", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--entrypoint", "/app/serviceidentity-bootstrap", q.ToolImage.Reference, "--deployment-capabilities")
	canonical, canonicalErr := canonicalFailedForwardCapabilities(caps)
	bound, boundErr := canonicalFailedForwardCapabilities(q.Capabilities)
	if err != nil || canonicalErr != nil || boundErr != nil || !bytes.Equal(canonical, bound) {
		return nil, bad("exact tool capability output")
	}
	candidate, retained, err := render(ctx)
	if err != nil {
		return nil, err
	}
	if err = validateCreateFailedForwardProofBindings(q, admitted, admissionSHA, stage, candidate, retained, now); err != nil {
		return nil, err
	}
	if err = candidate.AdmitFailedForwardResumable(ctx, backend, privateRoot); err != nil {
		return nil, err
	}
	if err = retained.AdmitFailedForwardResumable(ctx, backend, privateRoot); err != nil {
		return nil, err
	}
	return &verifiedCreateFailedForward{proofSHA: deploymentplan.Digest(q)}, nil
}
