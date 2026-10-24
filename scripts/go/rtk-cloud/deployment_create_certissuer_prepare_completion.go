package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const createCertIssuerPrepareCompletionSchema = "rtk-cloud.create-certissuer-provider-prepare-completion/v1"

type createCertIssuerPrepareCompletion struct {
	Schema             string                               `json:"schema"`
	Environment        string                               `json:"environment"`
	Stack              string                               `json:"stack"`
	Parent             string                               `json:"parent_plan_sha256"`
	Review             string                               `json:"review_sha256"`
	OriginalReleaseSHA string                               `json:"original_release_sha256"`
	SourceSHA          string                               `json:"source_journal_sha256"`
	ArtifactSHA        string                               `json:"qualification_sha256"`
	OwnerSHA           string                               `json:"source_owner_sha256"`
	AcceptedPlanSHA    string                               `json:"accepted_plan_sha256"`
	AcceptedJournalSHA string                               `json:"accepted_journal_sha256"`
	Recovery           createCertIssuerPrepareImageRecovery `json:"recovery"`
	SourceEntryCount   int                                  `json:"source_entry_count"`
	FinalEntryCount    int                                  `json:"final_entry_count"`
	FinalEntriesSHA    string                               `json:"final_entries_sha256"`
	FinalJournalSHA    string                               `json:"final_journal_sha256"`
	CompletedAt        time.Time                            `json:"completed_at"`
}

func createCertIssuerPrepareCompletionPath(parent string) string {
	return filepath.Join("deployment/create/material", parent, "certissuer-provider-prepare.json")
}
func createCertIssuerPrepareCompletionBase(review string) string {
	return filepath.Join("deployment/create/certissuer-provider-prepare", review)
}
func certIssuerPrepareCompletionBlock(stage string) error {
	return managedCorePartialBlock("completed-certissuer-prepare-" + stage)
}

// Only the exact completed producer is an overlay. An incomplete pointer/mirror
// never falls back to the original image and cannot promote a selected image.
func readCreateCertIssuerPrepareCompletion(store secretStore, parent string) (*createCertIssuerPrepareCompletion, error) {
	if !environmentPreflightDigest.MatchString(parent) {
		return nil, certIssuerPrepareCompletionBlock("parent")
	}
	path, err := store.safePath(createCertIssuerPrepareCompletionPath(parent))
	if err != nil {
		return nil, err
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		for dir := filepath.Dir(path); strings.HasPrefix(dir, store.Root); dir = filepath.Dir(dir) {
			if _, err := os.Lstat(dir); err == nil && validateEnvironmentPreflightDirectory(dir) != nil || err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, certIssuerPrepareCompletionBlock("private-ancestors")
			}
			if dir == store.Root {
				break
			}
		}
		// Resolve only this actual active generation's journal. No repair-directory
		// search or alternate owner can substitute for the canonical parent.
		ownerPath, err := store.safePath("deployment/.execution-active/owner.json")
		if err != nil {
			return nil, err
		}
		if _, err := os.Lstat(ownerPath); err == nil {
			var owner environmentCreateOwner
			var current environmentCreateJournal
			if createOpenBaoACKCompletionRead(store, "deployment/.execution-active/owner.json", &owner) != nil {
				return nil, certIssuerPrepareCompletionBlock("active-owner")
			}
			if owner.PlanSHA256 == parent {
				if createOpenBaoACKCompletionRead(store, owner.Journal, &current) != nil {
					return nil, certIssuerPrepareCompletionBlock("active-journal")
				}
				for _, entry := range current.Entries {
					prefix := "kubernetes/managed-complete-issuer-forward-"
					review := strings.TrimPrefix(entry.Key, prefix)
					if entry.State != "completed" || entry.Key != prefix+review || !environmentPreflightDigest.MatchString(review) {
						continue
					}
					for _, name := range []string{"completed.json", "final-journal.json"} {
						mirror, err := store.safePath(filepath.Join(createCertIssuerPrepareCompletionBase(review), name))
						if err != nil {
							return nil, err
						}
						if _, err := os.Lstat(mirror); err == nil || !errors.Is(err, os.ErrNotExist) {
							return nil, certIssuerPrepareCompletionBlock("half-published-mirror")
						}
					}
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return nil, certIssuerPrepareCompletionBlock("active-owner")
		}
		return nil, nil
	} else if err != nil {
		return nil, certIssuerPrepareCompletionBlock("private-pointer")
	}
	var proof, mirror createCertIssuerPrepareCompletion
	if createOpenBaoACKCompletionRead(store, createCertIssuerPrepareCompletionPath(parent), &proof) != nil || proof.Schema != createCertIssuerPrepareCompletionSchema || proof.Parent != parent || !environmentPreflightDigest.MatchString(proof.Review) || createOpenBaoACKCompletionRead(store, filepath.Join(createCertIssuerPrepareCompletionBase(proof.Review), "completed.json"), &mirror) != nil || deploymentplan.Digest(proof) != deploymentplan.Digest(mirror) {
		return nil, certIssuerPrepareCompletionBlock("pointer-mirror")
	}
	return &proof, nil
}

// The actual accepted snapshot must remain an unchanged prefix. Later native
// session stages may append receipts/leases; they cannot change any old owner,
// intent, completion or alias receipt. Only final parent completion is monotonic.
func certIssuerPrepareCompletionMembership(final, current environmentCreateJournal, proof *createCertIssuerPrepareCompletion) bool {
	if proof == nil || proof.FinalEntryCount != len(final.Entries) || !environmentPreflightDigest.MatchString(proof.FinalEntriesSHA) || proof.FinalEntriesSHA != deploymentplan.Digest(final.Entries) || proof.FinalJournalSHA != deploymentplan.Digest(final) || len(current.Entries) < len(final.Entries) || len(current.KubernetesLeases) < len(final.KubernetesLeases) {
		return false
	}
	prefix := current
	prefix.Entries = current.Entries[:len(final.Entries)]
	prefix.KubernetesLeases = current.KubernetesLeases[:len(final.KubernetesLeases)]
	if final.CompletedAt.IsZero() {
		if !current.CompletedAt.IsZero() && current.CompletedAt.Before(proof.CompletedAt) {
			return false
		}
		prefix.CompletedAt = final.CompletedAt
	}
	return deploymentplan.Digest(prefix) == deploymentplan.Digest(final)
}

func validateCertIssuerPrepareAcceptedPlan(core *createManagedCorePartialRecovery, plan *deploymentplan.Plan, j deploymentplan.Journal) error {
	if core == nil || core.PriorForward == nil || validateManagedCoreCertIssuerPrepare(core.CertIssuerPrepare) != nil || validateManagedCorePartialCandidate(core, plan) != nil || !createOpenBaoACKCompletedChild(plan, j) || !j.AcceptanceStarted || len(j.Completed) != 16 || len(j.Owners) != 16 {
		return certIssuerPrepareCompletionBlock("genuine-accepted-sixteen")
	}
	for index, resource := range plan.Resources {
		if j.Completed[index] != resource.ID || j.Owners[resource.ID] != resource.UID || resource.UID != core.Plan.Resources[index].UID {
			return certIssuerPrepareCompletionBlock("original-owner-" + resource.ID)
		}
	}
	return nil
}

func validateCreateCertIssuerPrepareCompletion(ctx context.Context, cfg deploymentConfig, store secretStore, parent string, original environmentRelease, proof *createCertIssuerPrepareCompletion, run environmentReleaseCommand) error {
	bad := func(stage string) error { return certIssuerPrepareCompletionBlock(stage) }
	if ctx == nil || ctx.Err() != nil || run == nil || cfg.Environment != "dev" || store.Environment != cfg.Environment || proof == nil || proof.Schema != createCertIssuerPrepareCompletionSchema || proof.Environment != cfg.Environment || proof.Stack != "video-cloud-"+cfg.Environment || original.Environment != cfg.Environment || original.Stack != proof.Stack || proof.Parent != parent || proof.CompletedAt.IsZero() || validateManagedCoreCertIssuerPrepare(&proof.Recovery) != nil {
		return bad("selected-generation")
	}
	for _, hash := range []string{parent, proof.Review, proof.OriginalReleaseSHA, proof.SourceSHA, proof.ArtifactSHA, proof.OwnerSHA, proof.AcceptedPlanSHA, proof.AcceptedJournalSHA, proof.FinalEntriesSHA, proof.FinalJournalSHA} {
		if !environmentPreflightDigest.MatchString(hash) {
			return bad("digests")
		}
	}
	base := filepath.Join("deployment/create/openbao-recovery", proof.Review)
	var source, final, current environmentCreateJournal
	var owner environmentCreateOwner
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, "source-owner.json"), &owner) != nil || owner.Schema != environmentCreateOwnerSchema || owner.Environment != cfg.Environment || owner.Stack != proof.Stack || owner.PlanSHA256 != parent || owner.OwnerToken == "" || deploymentplan.Digest(owner) != proof.OwnerSHA || !strings.HasPrefix(owner.Journal, filepath.Join("deployment/create/executions", parent)+string(filepath.Separator)+"execution-") || filepath.Base(owner.Journal) != "journal.json" || createOpenBaoACKCompletionRead(store, filepath.Join(base, "source-journal.json"), &source) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(createCertIssuerPrepareCompletionBase(proof.Review), "final-journal.json"), &final) != nil || createOpenBaoACKCompletionRead(store, owner.Journal, &current) != nil || proof.SourceSHA != deploymentplan.Digest(source) || proof.SourceEntryCount != len(source.Entries) || proof.FinalEntryCount != len(source.Entries)+2 || proof.FinalJournalSHA != deploymentplan.Digest(final) || !certIssuerPrepareCompletionMembership(final, current, proof) {
		return bad("immutable-source-and-final-membership")
	}
	ownerPath := "deployment/.execution-active/owner.json"
	if !current.CompletedAt.IsZero() {
		ownerPath = filepath.Join(filepath.Dir(owner.Journal), "owner/owner.json")
	}
	var actualOwner environmentCreateOwner
	if createOpenBaoACKCompletionRead(store, ownerPath, &actualOwner) != nil || deploymentplan.Digest(actualOwner) != proof.OwnerSHA {
		return bad("original-current-owner")
	}
	e := &environmentCreateExecution{Store: store, Journal: final, owner: owner}
	a, captured, err := loadAdmittedCreateRecovery(store, e, proof.Review)
	if err != nil || a.Plan.Schema != managedCorePartialSchema || a.Plan.ParentOwnerSHA != proof.OwnerSHA || a.Plan.ParentJournalSHA != proof.SourceSHA || deploymentplan.Digest(a) != proof.ArtifactSHA || deploymentplan.Digest(captured) != deploymentplan.Digest(source) || deploymentplan.Digest(a.Plan.Plan.Release) != proof.OriginalReleaseSHA || !managedCorePartialNativeCheckpoint(store, e) {
		return bad("six-go-accepted-forward-and-two-aliases")
	}
	pure := *e
	pure.Journal = source
	_, _, core, err := loadManagedCorePartialSource(store, &pure)
	if err != nil || core.PriorForward == nil {
		return bad("actual-failed-certissuer-source")
	}
	core.CertIssuerPrepare, err = loadManagedCoreQualifiedCertIssuerPrepare(store, proof.Review, a.Plan.Materials)
	if err != nil || deploymentplan.Digest(core.CertIssuerPrepare) != deploymentplan.Digest(proof.Recovery) {
		return bad("archived-role-selection")
	}
	plan, j, err := managedTransportPostAcceptedStage(store, e, "kubernetes/managed-complete-issuer-forward-"+proof.Review)
	if err != nil || plan.SHA256() != proof.AcceptedPlanSHA || deploymentplan.Digest(j) != proof.AcceptedJournalSHA || validateCertIssuerPrepareAcceptedPlan(core, plan, j) != nil || certIssuerPrepareSnapshotCompletionTime(final) != proof.CompletedAt {
		return bad("accepted-original-uid-body")
	}
	capturedRelease := a.Plan.Plan.Release
	if err := validateCertIssuerPrepareOriginalRelease(original, capturedRelease, proof); err != nil {
		return err
	}
	image, selection, manifest, err := loadCreateCertIssuerPrepareRepairImageWithCommand(ctx, cfg, store, parent, run)
	if err != nil || deploymentplan.Digest(image) != deploymentplan.Digest(proof.Recovery.Image) || selection != proof.Recovery.SelectionSHA || manifest != proof.Recovery.ManifestSHA {
		return bad("current-formal-publication")
	}
	return nil
}

func effectiveCreateCertIssuerPrepareReleaseWithCommand(ctx context.Context, cfg deploymentConfig, store secretStore, parent string, original environmentRelease, run environmentReleaseCommand) (environmentRelease, error) {
	proof, err := readCreateCertIssuerPrepareCompletion(store, parent)
	if err != nil {
		return environmentRelease{}, err
	}
	if proof == nil {
		return original, nil
	}
	if err := validateCreateCertIssuerPrepareCompletion(ctx, cfg, store, parent, original, proof, run); err != nil {
		return environmentRelease{}, err
	}
	return certIssuerPrepareReleaseOverlay(original, proof.Recovery.Image), nil
}

func publishCreateCertIssuerPrepareCompletion(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime) error {
	return publishCreateCertIssuerPrepareCompletionWithCommand(ctx, cfg, runtime, environmentCIImageCommand(cfg))
}

func publishCreateCertIssuerPrepareCompletionWithCommand(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, run environmentReleaseCommand) error {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if ctx == nil || r == nil || e == nil || cfg.Environment != e.Store.Environment || cfg.Values["CLOUD_STACK_NAME"] != e.Journal.Stack || runtime.Backend.Store.Root != e.Store.Root || r.ManagedCorePartial == nil || r.ManagedCorePartial.Action != managedCoreRestoreOnly || r.ManagedCorePartial.PriorForward == nil || e.Check(ctx) != nil || !managedCorePartialNativeCheckpoint(e.Store, e) {
		return certIssuerPrepareCompletionBlock("exclusive-real-accepted-owner")
	}
	core := r.ManagedCorePartial
	a, source, err := loadAdmittedCreateRecovery(e.Store, e, core.ForwardReview)
	if err != nil {
		return err
	}
	plan, j, err := managedTransportPostAcceptedStage(e.Store, e, "kubernetes/managed-complete-issuer-forward-"+core.ForwardReview)
	if err != nil || validateCertIssuerPrepareAcceptedPlan(core, plan, j) != nil {
		return certIssuerPrepareCompletionBlock("accepted-forward")
	}
	for _, resource := range plan.Resources {
		if err := e.Check(ctx); err != nil {
			return err
		}
		actual, err := runtime.Backend.Read(ctx, resource.ID)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != j.Owners[resource.ID] || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(resource.Desired, actual) {
			return certIssuerPrepareCompletionBlock("current-native-owner-" + resource.ID)
		}
		ready := resource
		ready.Action = "retain"
		bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = runtime.Backend.Wait(bounded, ready)
		cancel()
		if err != nil {
			return certIssuerPrepareCompletionBlock("current-native-ready-" + resource.ID)
		}
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	proof := createCertIssuerPrepareCompletion{Schema: createCertIssuerPrepareCompletionSchema, Environment: cfg.Environment, Stack: e.Journal.Stack, Parent: e.Journal.PlanSHA256, Review: core.ForwardReview, OriginalReleaseSHA: deploymentplan.Digest(a.Plan.Plan.Release), SourceSHA: deploymentplan.Digest(source), ArtifactSHA: deploymentplan.Digest(a), OwnerSHA: deploymentplan.Digest(e.owner), AcceptedPlanSHA: plan.SHA256(), AcceptedJournalSHA: deploymentplan.Digest(j), Recovery: *core.CertIssuerPrepare, SourceEntryCount: len(source.Entries), FinalEntryCount: len(e.Journal.Entries), FinalEntriesSHA: deploymentplan.Digest(e.Journal.Entries), FinalJournalSHA: deploymentplan.Digest(e.Journal), CompletedAt: certIssuerPrepareSnapshotCompletionTime(e.Journal)}
	base := createCertIssuerPrepareCompletionBase(proof.Review)
	for _, directory := range []string{base, filepath.Dir(createCertIssuerPrepareCompletionPath(proof.Parent))} {
		if _, err := prepareEnvironmentCreateDirectories(e.Store, directory); err != nil {
			return err
		}
	}
	// Immutable actual snapshot first; failure can only retry these exact bytes.
	if err := writeEnvironmentCreateDerivedPrivateFile(e.Store, filepath.Join(base, "final-journal.json"), mustProviderRecoveryJSON(e.Journal)); err != nil {
		return err
	}
	if err := validateCreateCertIssuerPrepareCompletion(ctx, cfg, e.Store, proof.Parent, a.Plan.Plan.Release, &proof, run); err != nil {
		return err
	}
	raw := mustProviderRecoveryJSON(proof)
	if err := writeEnvironmentCreateDerivedPrivateFile(e.Store, filepath.Join(base, "completed.json"), raw); err != nil {
		return err
	}
	return writeEnvironmentCreateDerivedPrivateFile(e.Store, createCertIssuerPrepareCompletionPath(proof.Parent), raw)
}

func certIssuerPrepareSnapshotCompletionTime(j environmentCreateJournal) time.Time {
	var at time.Time
	for _, entry := range j.Entries {
		if entry.State == "completed" && entry.CompletedAt.After(at) {
			at = entry.CompletedAt
		}
	}
	return at
}

func validateCertIssuerPrepareOriginalRelease(original, captured environmentRelease, proof *createCertIssuerPrepareCompletion) error {
	if proof == nil || proof.OriginalReleaseSHA != deploymentplan.Digest(captured) || original.Schema != captured.Schema || original.Environment != captured.Environment || original.Stack != captured.Stack || original.WorkspaceRevision != captured.WorkspaceRevision || original.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference == "" || deploymentplan.Digest(original.Images["LKE_VIDEO_CLOUD_IMAGE"]) != deploymentplan.Digest(captured.Images["LKE_VIDEO_CLOUD_IMAGE"]) {
		return certIssuerPrepareCompletionBlock("original-business-release")
	}
	for role, image := range captured.Images {
		if role == "LKE_OPENBAO_PKI_IMAGE" || role == "LKE_CERTISSUER_IMAGE" {
			continue
		}
		if deploymentplan.Digest(original.Images[role]) != deploymentplan.Digest(image) {
			return certIssuerPrepareCompletionBlock("original-release-role-" + role)
		}
	}
	for role := range original.Images {
		if _, ok := captured.Images[role]; !ok && role != "LKE_CERTISSUER_IMAGE" && role != "LKE_OPENBAO_PKI_IMAGE" {
			return certIssuerPrepareCompletionBlock("foreign-release-role-" + role)
		}
	}
	if selected, ok := original.Images["LKE_CERTISSUER_IMAGE"]; ok && deploymentplan.Digest(selected) != deploymentplan.Digest(proof.Recovery.Image) && deploymentplan.Digest(selected) != deploymentplan.Digest(captured.Images["LKE_CERTISSUER_IMAGE"]) {
		return certIssuerPrepareCompletionBlock("foreign-certissuer-overlay")
	}
	return nil
}

func certIssuerPrepareReleaseOverlay(original environmentRelease, image environmentReleaseImage) environmentRelease {
	effective := original
	effective.Images = make(map[string]environmentReleaseImage, len(original.Images)+1)
	for role, value := range original.Images {
		effective.Images[role] = value
	}
	effective.Images["LKE_CERTISSUER_IMAGE"] = image
	return effective
}

// Current native owner/image is checked separately from the historical complete
// body: a later qualified same-key session may change its literal SID only.
func validateCreateCertIssuerPrepareInventory(store secretStore, parent string, live []deploymentplan.Object) error {
	proof, err := readCreateCertIssuerPrepareCompletion(store, parent)
	if err != nil {
		return err
	}
	if proof == nil {
		return nil
	}
	var plan deploymentplan.Plan
	var journal deploymentplan.Journal
	if !environmentPreflightDigest.MatchString(proof.AcceptedPlanSHA) || !environmentPreflightDigest.MatchString(proof.AcceptedJournalSHA) || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/rollback", proof.AcceptedPlanSHA, "plan.json"), &plan) != nil || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/rollback", proof.AcceptedPlanSHA, "journal.json"), &journal) != nil || plan.SHA256() != proof.AcceptedPlanSHA || deploymentplan.Digest(journal) != proof.AcceptedJournalSHA || !createOpenBaoACKCompletedChild(&plan, journal) || !journal.AcceptanceStarted || len(plan.Resources) != 16 {
		return certIssuerPrepareCompletionBlock("inventory-accepted-authority")
	}
	id := "Deployment/" + proof.Stack + "-video-cloud/certissuer"
	uid := ""
	foundOriginal := 0
	for _, resource := range plan.Resources {
		if resource.ID != id {
			continue
		}
		containers, err := environmentPKINamedFields(environmentPKIPod(resource.Desired)["containers"])
		if err != nil || len(containers) != 1 || containers["certissuer"] == nil || containers["certissuer"]["image"] != proof.Recovery.Image.Reference || journal.Owners[id] != resource.UID || !environmentBootstrapOwnerUUID.MatchString(resource.UID) {
			return certIssuerPrepareCompletionBlock("inventory-original-certissuer")
		}
		uid = resource.UID
		foundOriginal++
	}
	if foundOriginal != 1 {
		return certIssuerPrepareCompletionBlock("inventory-original-certissuer")
	}
	found := 0
	for _, object := range live {
		if deploymentplan.ID(object) != id {
			continue
		}
		found++
		metadata := deploymentplan.Map(object["metadata"])
		containers, err := environmentPKINamedFields(environmentPKIPod(object)["containers"])
		if metadata["uid"] != uid || metadata["deletionTimestamp"] != nil || err != nil || len(containers) != 1 || containers["certissuer"] == nil || containers["certissuer"]["image"] != proof.Recovery.Image.Reference {
			return certIssuerPrepareCompletionBlock("inventory-current-certissuer")
		}
	}
	if found != 1 {
		return certIssuerPrepareCompletionBlock("inventory-one-current-certissuer")
	}
	return nil
}
