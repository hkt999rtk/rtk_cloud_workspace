package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const createWorkloadImageForwardSchema = "rtk-cloud.create-workload-image-forward/v1"
const createWorkloadImageForwardStage = "workload-image-forward"

type createWorkloadImageForward struct {
	Schema, Parent, OriginalPlan, OriginalJournal string
	Images                                        map[string]environmentReleaseImage
	Plan                                          *deploymentplan.Plan
}
type createWorkloadImageForwardReceipt struct {
	Schema          string `json:"schema"`
	OriginalPlan    string `json:"original_failed_plan_sha256"`
	OriginalJournal string `json:"original_failed_journal_sha256"`
	ForwardStage    string `json:"actual_qualified_forward_stage"`
	ForwardPlan     string `json:"actual_qualified_forward_plan_sha256"`
	Qualification   string `json:"qualification_sha256"`
}

func workloadImageRole(stack, id, name string) string {
	if id == "Deployment/"+stack+"-video-cloud/pki-controller" && (name == "controller" || name == "pki-controller") {
		return "LKE_PKI_CONTROLLER_IMAGE"
	}
	if id == "Deployment/"+stack+"-account-manager/account-manager" && name == "pkimanagement" {
		return "LKE_PKI_MANAGEMENT_IMAGE"
	}
	return ""
}

// Only the two explicitly selected runtime packages can change. Known writes
// become same-owner retains; unstarted resources keep their original actions.
func buildCreateWorkloadImageForward(original *deploymentplan.Plan, j deploymentplan.Journal, images map[string]environmentReleaseImage, observed map[string]deploymentplan.Object, stage string) (*deploymentplan.Plan, error) {
	bad := func() (*deploymentplan.Plan, error) {
		return nil, createProviderRecoveryBlock("workload image continuation differs from its exact known original writes")
	}
	if original == nil || j.PlanSHA256 != original.SHA256() || j.InFlight != "" || j.Accepted || j.AcceptanceStarted || j.Failed == "" || len(j.Completed) >= len(original.Resources) || len(j.Owners) != len(j.Completed) || len(images) == 0 || !secretEnvironmentPattern.MatchString(stage) {
		return bad()
	}
	for role, image := range images {
		if (role != "LKE_PKI_CONTROLLER_IMAGE" && role != "LKE_PKI_MANAGEMENT_IMAGE") || !immutableEnvironmentImage.MatchString(image.Reference) || !strings.HasPrefix(image.Reference, "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@") {
			return bad()
		}
	}
	next := *original
	next.Resources = make([]deploymentplan.Resource, len(original.Resources))
	changed := map[string]bool{}
	for i, old := range original.Resources {
		r := old
		r.Desired = deploymentplan.Clone(old.Desired)
		current := observed[old.ID]
		if i < len(j.Completed) {
			uid := j.Owners[old.ID]
			if j.Completed[i] != old.ID || !environmentBootstrapOwnerUUID.MatchString(uid) || old.UID != "" && old.UID != uid || current == nil || deploymentplan.Map(current["metadata"])["uid"] != uid || !deploymentplan.Matches(old.Desired, current) {
				return bad()
			}
			r.UID = uid
			r.Baseline = deploymentplan.Projection(current)
			r.Action = "retain"
		} else if old.Action == "create" {
			if current != nil {
				return bad()
			}
		} else if old.UID == "" || current == nil || deploymentplan.Map(current["metadata"])["uid"] != old.UID || !deploymentplan.Matches(old.Baseline, current) {
			return bad()
		}
		for _, raw := range certIssuerObjectList(environmentPKIPod(r.Desired)["containers"]) {
			container := deploymentplan.Map(raw)
			role := workloadImageRole(original.Stack, old.ID, deploymentplan.String(container["name"]))
			image, ok := images[role]
			if !ok || container["image"] == image.Reference {
				continue
			}
			if i >= len(j.Completed) || old.Phase != "workload" || changed[role] {
				return bad()
			}
			container["image"] = image.Reference
			r.Action = "update"
			changed[role] = true
		}
		next.Resources[i] = r
	}
	if len(changed) != len(images) {
		return bad()
	}
	// The original release remains history; these exact selected role references
	// are an explicit new executable input, without selecting a latest version.
	next.ReleaseSHA256 = deploymentplan.Digest(struct {
		Original string
		Images   map[string]environmentReleaseImage
	}{original.ReleaseSHA256, images})
	next.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, environmentCreateDefinitions(&next))
	return &next, nil
}
func workloadImageForwardPath(e *environmentCreateExecution) string {
	return filepath.Join(e.Directory, "workload-image-forward.json")
}
func readOriginalCreateWorkloadImageForward(store secretStore, e *environmentCreateExecution) (*createWorkloadImageForward, error) {
	path := workloadImageForwardPath(e)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var q createWorkloadImageForward
	if readExecutionJSON(path, &q) != nil || q.Schema != createWorkloadImageForwardSchema || q.Parent != e.Journal.PlanSHA256 || q.Plan == nil {
		return nil, createProviderRecoveryBlock("original workload image continuation artifact is invalid")
	}
	original, err := readEnvironmentCreateStageInput(store, e, "workload")
	if err != nil || original == nil || q.OriginalPlan != original.SHA256() {
		return nil, createProviderRecoveryBlock("original workload image continuation lost its source")
	}
	j, exists, err := original.ReadExecution(store.Root)
	if err != nil || !exists || q.OriginalJournal != deploymentplan.Digest(j) {
		return nil, createProviderRecoveryBlock("original workload image continuation source journal changed")
	}
	observed := map[string]deploymentplan.Object{}
	for _, r := range q.Plan.Resources {
		if r.Baseline != nil {
			observed[r.ID] = r.Baseline
		}
	}
	expected, err := buildCreateWorkloadImageForward(original, j, q.Images, observed, createWorkloadImageForwardStage)
	if err != nil || expected.SHA256() != q.Plan.SHA256() {
		return nil, createProviderRecoveryBlock("workload image continuation changes fields outside its selected packages")
	}
	return &q, nil
}
func workloadImageForwardReceiptMatches(raw json.RawMessage) bool {
	var r createWorkloadImageForwardReceipt
	return json.Unmarshal(raw, &r) == nil && r.Schema == createWorkloadImageForwardSchema
}
func validateWorkloadImageForwardReceipt(store secretStore, e *environmentCreateExecution, p *deploymentplan.Plan, j deploymentplan.Journal, raw json.RawMessage) error {
	var r createWorkloadImageForwardReceipt
	q, err := readCreateWorkloadImageForward(store, e)
	if err != nil || q == nil || decodeEnvironmentJSON(raw, &r) != nil || r.OriginalPlan != p.SHA256() || r.OriginalJournal != deploymentplan.Digest(j) || r.ForwardStage != "kubernetes/"+createWorkloadImageForwardStage || r.ForwardPlan != q.Plan.SHA256() || r.Qualification != deploymentplan.Digest(q) {
		return createProviderRecoveryBlock("original workload completion differs from its actual image successor")
	}
	accepted, _, err := acceptedCreateStage(store, e, r.ForwardStage)
	if err != nil || accepted.SHA256() != q.Plan.SHA256() {
		return createProviderRecoveryBlock("original workload image successor has not actually passed acceptance")
	}
	return nil
}
func completeCreateWorkloadImageForward(ctx context.Context, store secretStore, e *environmentCreateExecution, q *createWorkloadImageForward) error {
	p, err := readEnvironmentCreateStageInput(store, e, "workload")
	if err != nil || p == nil {
		return createProviderRecoveryBlock("original workload input unavailable")
	}
	j, exists, err := p.ReadExecution(store.Root)
	if err != nil || !exists {
		return createProviderRecoveryBlock("original workload journal unavailable")
	}
	r := createWorkloadImageForwardReceipt{Schema: createWorkloadImageForwardSchema, OriginalPlan: q.OriginalPlan, OriginalJournal: q.OriginalJournal, ForwardStage: "kubernetes/" + createWorkloadImageForwardStage, ForwardPlan: q.Plan.SHA256(), Qualification: deploymentplan.Digest(q)}
	raw := mustProviderRecoveryJSON(r)
	if err = validateWorkloadImageForwardReceipt(store, e, p, j, raw); err != nil {
		return err
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload")
	if entry == nil {
		return createProviderRecoveryBlock("original workload intent unavailable")
	}
	if entry.State == "completed" {
		if !samePersistedJSONReceipt(entry.Receipt, raw) {
			return createProviderRecoveryBlock("original workload successor receipt changed")
		}
		return e.Check(ctx)
	}
	if entry.State == "pending" {
		if err = e.RecordReceipt(entry.Key, raw); err != nil {
			return err
		}
	} else if entry.State != "allocated" || !samePersistedJSONReceipt(entry.Receipt, raw) {
		return createProviderRecoveryBlock("original workload successor receipt differs")
	}
	return e.CompleteEntry(entry.Key, time.Now().UTC())
}

// Deployment plans use the existing 32 MiB private artifact contract, rather
// than the 256 KiB public-ceremony metadata writer. Retries preserve exact bytes.
func writeCreateWorkloadImageArtifact(store secretStore, relative string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil || len(raw) == 0 || len(raw) > environmentPreflightArtifactLimit {
		return createProviderRecoveryBlock("private workload image artifact exceeds the complete deployment plan bound")
	}
	path, err := store.safePath(relative)
	if err != nil {
		return err
	}
	if err = ensurePrivateDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	if err = validateEnvironmentPreflightDirectories(store, filepath.Dir(path)); err != nil {
		return err
	}
	if _, err = os.Lstat(path); err == nil {
		var previous json.RawMessage
		if err = readExecutionJSON(path, &previous); err != nil {
			return err
		}
		var compact bytes.Buffer
		if json.Compact(&compact, previous) != nil || !bytes.Equal(compact.Bytes(), raw) {
			return createProviderRecoveryBlock("immutable workload image continuation artifact changed")
		}
		return nil
	} else if !os.IsNotExist(err) {
		return err
	}
	return writeEnvironmentPreflightImmutableJSON(path, value)
}

// Late image updates use the completed direct-fresh lifecycle and the same
// ordinary children. They grant no bootstrap, cleanup or replacement authority.
func completedFreshRuntimeImageSource(ctx context.Context, store secretStore, e *environmentCreateExecution) error {
	if e == nil || e.Store != store || !e.Journal.CompletedAt.IsZero() || e.Check(ctx) != nil {
		return createProviderRecoveryBlock("runtime image successor requires its open original create owner")
	}
	if _, err := readFreshProductProgram(store, e); err != nil {
		return err
	}
	final := createOpenBaoACKCompletionEntry(e.Journal, "service-session-finalize")
	if final == nil || final.State != "completed" || final.CompletedAt.IsZero() {
		return createProviderRecoveryBlock("runtime image successor precedes original native finalization")
	}
	for _, stage := range []string{"fresh-product-steady", "fresh-product-producer-retire"} {
		p, j, err := acceptedCreateStage(store, e, "kubernetes/"+stage)
		if err != nil {
			return err
		}
		checked, found, err := readEnvironmentCreateStageProgress(store, e, stage, p)
		if err != nil || !found || deploymentplan.Digest(checked) != deploymentplan.Digest(j) {
			return createProviderRecoveryBlock("runtime image successor lacks original accepted cleanup evidence")
		}
	}
	return nil
}

// The historical artifact and alias remain immutable. A different authoritative
// environment selection derives one image-only child from genuine recorded
// same-UID bodies; native reads can verify those bodies, never supply history.
func selectedCreateWorkloadImageForward(ctx context.Context, store secretStore, e *environmentCreateExecution, backend deploymentplan.Backend, reviewed ...map[string]environmentReleaseImage) (*createWorkloadImageForward, string, error) {
	q, err := readCreateWorkloadImageForward(store, e)
	if err != nil {
		return nil, "", err
	}
	var images map[string]environmentReleaseImage
	if len(reviewed) > 1 {
		return nil, "", createProviderRecoveryBlock("runtime image review has ambiguous selections")
	}
	if len(reviewed) == 1 {
		images = reviewed[0] // explicit updater preview, after its exact publication/capability checks
	} else {
		if e.versionState == nil || e.versionState.cache == nil || len(e.versionState.cache.RuntimeImages) == 0 {
			return q, createWorkloadImageForwardStage, nil
		}
		images = e.versionState.cache.RuntimeImages
	}
	if q != nil && deploymentplan.Digest(q.Images) == deploymentplan.Digest(images) {
		return q, createWorkloadImageForwardStage, nil
	}
	bad := func() (*createWorkloadImageForward, string, error) {
		return nil, "", createProviderRecoveryBlock("runtime image successor differs from its exact selected roles and recorded original owners")
	}
	if backend == nil || completedFreshRuntimeImageSource(ctx, store, e) != nil {
		return bad()
	}
	for role, image := range images {
		if (role != "LKE_PKI_CONTROLLER_IMAGE" && role != "LKE_PKI_MANAGEMENT_IMAGE") || !immutableEnvironmentImage.MatchString(image.Reference) || !strings.HasPrefix(image.Reference, "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@") {
			return bad()
		}
	}
	original, journal, err := acceptedCreateStage(store, e, "kubernetes/workload")
	if err != nil {
		return nil, "", err
	}
	sourceKey := "kubernetes/workload"
	if q != nil {
		sourceKey = "kubernetes/" + createWorkloadImageForwardStage
	}
	checked, found, err := readEnvironmentCreateStageProgress(store, e, strings.TrimPrefix(sourceKey, "kubernetes/"), original)
	if err != nil || !found || deploymentplan.Digest(checked) != deploymentplan.Digest(journal) {
		return bad()
	}
	stage := createWorkloadImageForwardStage + "-" + deploymentplan.Digest(images)
	var base []activeTransportRenewalResource
	for _, r := range original.Resources {
		for _, raw := range certIssuerObjectList(environmentPKIPod(r.Desired)["containers"]) {
			role := workloadImageRole(original.Stack, r.ID, deploymentplan.String(deploymentplan.Map(raw)["name"]))
			if _, selected := images[role]; selected {
				base = append(base, sessionRecordedOwnerBase(r.ID, journal.Owners[r.ID], r.Desired))
				break
			}
		}
	}
	pending, err := authenticatedRecordedPendingWrites(ctx, store, e, backend)
	if err != nil {
		return nil, "", err
	}
	latest, err := completedRecordedOwnerSuccessorsAfterHistoryBefore(ctx, store, e, backend, sourceKey, base, pending, "kubernetes/"+stage)
	if err != nil {
		return nil, "", err
	}
	saved, err := readEnvironmentCreateStageInput(store, e, stage)
	if err != nil {
		return nil, "", err
	}
	var definitions []deploymentplan.Definition
	var live []deploymentplan.Object
	present := map[string]bool{}
	for _, owner := range latest {
		desired := deploymentplan.Clone(owner.Desired)
		changed := false
		for _, raw := range certIssuerObjectList(environmentPKIPod(desired)["containers"]) {
			container := deploymentplan.Map(raw)
			role := workloadImageRole(original.Stack, owner.ID, deploymentplan.String(container["name"]))
			image, selected := images[role]
			if !selected {
				continue
			}
			if present[role] {
				return bad()
			}
			present[role] = true
			if container["image"] != image.Reference {
				container["image"], changed = image.Reference, true
			}
		}
		if !changed {
			current, err := backend.Read(ctx, owner.ID)
			if err != nil || current == nil || deploymentplan.Map(current["metadata"])["uid"] != owner.UID || !deploymentplan.Matches(owner.Desired, current) {
				return bad()
			}
			continue
		}
		deploymentplan.Map(desired["metadata"])["uid"] = owner.UID
		definitions = append(definitions, deploymentplan.Definition{Object: desired, Phase: "workload"})
		var current deploymentplan.Object
		if saved == nil {
			current, err = backend.Read(ctx, owner.ID)
			if err != nil {
				return nil, "", err
			}
		} else {
			for _, r := range saved.Resources {
				if r.ID == owner.ID && r.UID == owner.UID {
					current = deploymentplan.Clone(r.Baseline)
					deploymentplan.Map(current["metadata"])["uid"] = owner.UID
				}
			}
		}
		if current == nil || deploymentplan.Map(current["metadata"])["uid"] != owner.UID || !deploymentplan.Matches(owner.Desired, current) {
			return bad()
		}
		live = append(live, current)
	}
	if len(present) != len(images) || len(definitions) == 0 {
		return bad()
	}
	inputs := original.Inputs
	inputs.ReleaseSHA256 = deploymentplan.Digest(struct {
		Original string
		Images   map[string]environmentReleaseImage
	}{original.ReleaseSHA256, images})
	inputs.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, definitions)
	inputs.CreateParentSHA256 = e.Journal.PlanSHA256
	plan, err := deploymentplan.Compile(inputs, definitions, live)
	if err != nil {
		return nil, "", err
	}
	if saved != nil && saved.SHA256() != plan.SHA256() {
		return bad()
	}
	return &createWorkloadImageForward{Schema: createWorkloadImageForwardSchema, Parent: e.Journal.PlanSHA256, OriginalPlan: original.SHA256(), OriginalJournal: deploymentplan.Digest(journal), Images: images, Plan: plan}, stage, e.Check(ctx)
}

// Classify the genuine pre-rollout role body without selecting it as the new
// executable release. Bindings carry role names, not this read-only image map.
func createRuntimeImageObservationRelease(ctx context.Context, store secretStore, e *environmentCreateExecution, backend deploymentplan.Backend, release environmentRelease, live []deploymentplan.Object) (environmentRelease, error) {
	if e == nil || e.versionState == nil || e.versionState.cache == nil || len(e.versionState.cache.RuntimeImages) == 0 {
		return release, nil
	}
	q, stage, err := selectedCreateWorkloadImageForward(ctx, store, e, backend)
	if err != nil || q == nil || stage == createWorkloadImageForwardStage {
		return release, err
	}
	next := release
	next.Images = make(map[string]environmentReleaseImage, len(release.Images))
	for role, image := range release.Images {
		next.Images[role] = image
	}
	for _, r := range q.Plan.Resources {
		var current deploymentplan.Object
		for _, object := range live {
			if deploymentplan.ID(object) == r.ID {
				current = object
			}
		}
		if current == nil || deploymentplan.Map(current["metadata"])["uid"] != r.UID {
			return release, createProviderRecoveryBlock("runtime image observation changed an original owner")
		}
		if deploymentplan.Matches(r.Desired, current) {
			continue
		}
		if !deploymentplan.Matches(r.Baseline, current) {
			return release, createProviderRecoveryBlock("runtime image observation differs from its recorded source body")
		}
		for _, raw := range certIssuerObjectList(environmentPKIPod(r.Baseline)["containers"]) {
			c := deploymentplan.Map(raw)
			role := workloadImageRole(q.Plan.Stack, r.ID, deploymentplan.String(c["name"]))
			if role != "" {
				next.Images[role] = environmentReleaseImage{Reference: deploymentplan.String(c["image"])}
			}
		}
	}
	return next, nil
}
