package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const createOpenBaoACKCompletionSchema = "rtk-cloud.create-openbao-host-ack-recovery/v1"

type createOpenBaoACKCompletion struct {
	Schema             string                   `json:"schema"`
	Environment        string                   `json:"environment"`
	Stack              string                   `json:"stack"`
	Parent             string                   `json:"parent_plan_sha256"`
	Review             string                   `json:"review_sha256"`
	OriginalReleaseSHA string                   `json:"original_release_sha256"`
	Recovery           createOpenBaoACKRecovery `json:"recovery"`
	Native             deploymentplan.Object    `json:"native_proof"`
	CompletedAt        time.Time                `json:"completed_at"`
}

func createOpenBaoACKCompletionPath(parent string) string {
	return filepath.Join("deployment/create/material", parent, "openbao-host-ack.json")
}

func createOpenBaoACKCompletionRead(store secretStore, relative string, target any) error {
	path, err := privateDeploymentStatePath(store.Environment, relative)
	if err != nil || path != filepath.Join(store.Root, relative) || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
		return createOpenBaoACKBlock("completed-private-evidence")
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil || decodeEnvironmentJSON(raw, target) != nil {
		return createOpenBaoACKBlock("completed-private-evidence")
	}
	return nil
}

// Only an absent pointer means no overlay. A partially published mirror or a
// changed completed pointer cannot silently fall back to the original image.
func readCreateOpenBaoACKCompletion(store secretStore, parent string) (*createOpenBaoACKCompletion, error) {
	if !environmentPreflightDigest.MatchString(parent) {
		return nil, createOpenBaoACKBlock("completed-parent")
	}
	path, err := privateDeploymentStatePath(store.Environment, createOpenBaoACKCompletionPath(parent))
	if err != nil || path != filepath.Join(store.Root, createOpenBaoACKCompletionPath(parent)) {
		return nil, createOpenBaoACKBlock("completed-private-evidence")
	}
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		// Check existing ancestors even when this generation has no pointer.
		for dir := filepath.Dir(path); strings.HasPrefix(dir, store.Root); dir = filepath.Dir(dir) {
			if _, err := os.Lstat(dir); err == nil && validateEnvironmentPreflightDirectory(dir) != nil || err != nil && !errors.Is(err, os.ErrNotExist) {
				return nil, createOpenBaoACKBlock("completed-private-evidence")
			}
			if dir == store.Root {
				break
			}
		}
		return nil, nil
	} else if err != nil {
		return nil, createOpenBaoACKBlock("completed-private-evidence")
	}
	var proof, mirror createOpenBaoACKCompletion
	if createOpenBaoACKCompletionRead(store, createOpenBaoACKCompletionPath(parent), &proof) != nil || proof.Schema != createOpenBaoACKCompletionSchema || proof.Parent != parent || !environmentPreflightDigest.MatchString(proof.Review) || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-host-ack", proof.Review, "completed.json"), &mirror) != nil || activeTransportJournalDigest(proof) != activeTransportJournalDigest(mirror) {
		return nil, createOpenBaoACKBlock("completed-pointer-mirror")
	}
	return &proof, nil
}

func effectiveCreateOpenBaoACKRelease(ctx context.Context, cfg deploymentConfig, store secretStore, parent string, original environmentRelease, commands ...*environmentCreateExecution) (environmentRelease, error) {
	return effectiveCreateOpenBaoACKReleaseWithCommand(ctx, cfg, store, parent, original, environmentCIImageCommand(cfg), commands...)
}

func effectiveCreateOpenBaoACKReleaseWithCommand(ctx context.Context, cfg deploymentConfig, store secretStore, parent string, original environmentRelease, run environmentReleaseCommand, commands ...*environmentCreateExecution) (environmentRelease, error) {
	if len(commands) > 1 {
		return environmentRelease{}, managedLateSourceReuseBlock()
	}
	if len(commands) == 1 {
		e := commands[0]
		if e == nil || e.Store != store || e.Journal.PlanSHA256 != parent || e.Check(ctx) != nil {
			return environmentRelease{}, managedLateSourceReuseBlock()
		}
		if managedLateSourceReuseActive(e) {
			if err := checkManagedLateSourceReuse(e); err != nil {
				return environmentRelease{}, err
			}
		}
	}
	proof, err := readCreateOpenBaoACKCompletion(store, parent)
	if err != nil {
		return environmentRelease{}, err
	}
	if proof == nil {
		return effectiveCreateCertIssuerPrepareReleaseWithCommand(ctx, cfg, store, parent, original, run)
	}
	if err := validateCreateOpenBaoACKCompletion(ctx, cfg, store, parent, original, proof, run, commands...); err != nil {
		return environmentRelease{}, err
	}
	effective := original
	effective.Images = make(map[string]environmentReleaseImage, len(original.Images))
	for role, image := range original.Images {
		effective.Images[role] = image
	}
	effective.Images["LKE_OPENBAO_PKI_IMAGE"] = proof.Recovery.Image
	return effectiveCreateCertIssuerPrepareReleaseWithCommand(ctx, cfg, store, parent, effective, run)
}

// Existing-environment targets obtain the generation only from the original
// immutable native App routing allocation, never from a search for repair files.
func effectiveCreateOpenBaoACKReleaseForInventory(ctx context.Context, cfg deploymentConfig, store secretStore, original environmentRelease, live []deploymentplan.Object) (environmentRelease, error) {
	return effectiveCreateOpenBaoACKReleaseForInventoryWithCommand(ctx, cfg, store, original, live, environmentCIImageCommand(cfg))
}

func effectiveCreateOpenBaoACKReleaseForInventoryWithCommand(ctx context.Context, cfg deploymentConfig, store secretStore, original environmentRelease, live []deploymentplan.Object, run environmentReleaseCommand) (environmentRelease, error) {
	bad := func() (environmentRelease, error) {
		return environmentRelease{}, createOpenBaoACKBlock("completed-native-generation")
	}
	var declaration deploymentplan.Object
	stack := cfg.Values["CLOUD_STACK_NAME"]
	for _, object := range live {
		if deploymentplan.ID(object) == "ConfigMap/"+stack+"-video-cloud/"+environmentAppRoutingDeclaration {
			if declaration != nil {
				return bad()
			}
			declaration = object
		}
	}
	if declaration == nil {
		return original, nil
	}
	var private environmentAppRoutingBinding
	if createOpenBaoACKCompletionRead(store, "pki/app/routing.json", &private) != nil {
		return bad()
	}
	bindings := environmentIdentityBindings{Environment: cfg.Environment, Stack: stack}
	if observeEnvironmentAppRouting(&bindings, declaration, live) != nil || bindings.AppRouting == nil || *bindings.AppRouting != private {
		return bad()
	}
	proof, err := readCreateOpenBaoACKCompletion(store, private.CreatePlanSHA256)
	if err != nil {
		return environmentRelease{}, err
	}
	if proof == nil {
		return original, nil
	}
	appOwners, baoOwners := 0, 0
	for _, object := range live {
		if deploymentplan.ID(object) == "Deployment/"+stack+"-video-cloud/video-cloud-api-app-pki" {
			if deploymentplan.Map(object["metadata"])["uid"] != private.DeploymentUID {
				return bad()
			}
			appOwners++
		}
		if deploymentplan.ID(object) == proof.Recovery.Original.ID {
			if deploymentplan.Map(object["metadata"])["uid"] != proof.Recovery.Original.UID {
				return bad()
			}
			pod := environmentPKIPod(object)
			containers, err := environmentPKINamedFields(pod["containers"])
			init, initErr := environmentPKINamedFields(pod["initContainers"])
			if err != nil || initErr != nil || containers["openbao-pki"]["image"] != proof.Recovery.Image.Reference || init["openbao-pki-install"]["image"] != proof.Recovery.Image.Reference {
				return bad()
			}
			baoOwners++
		}
	}
	if appOwners != 1 || baoOwners != 1 {
		return bad()
	}
	if err := validateCreateCertIssuerPrepareInventory(store, private.CreatePlanSHA256, live); err != nil {
		return environmentRelease{}, err
	}
	return effectiveCreateOpenBaoACKReleaseWithCommand(ctx, cfg, store, private.CreatePlanSHA256, original, run)
}

func publishCreateOpenBaoACKCompletion(ctx context.Context, cfg deploymentConfig, store secretStore, parent, review string, original environmentRelease, ack *createOpenBaoACKRecovery, nativeProof deploymentplan.Object) error {
	return publishCreateOpenBaoACKCompletionWithCommand(ctx, cfg, store, parent, review, original, ack, nativeProof, environmentCIImageCommand(cfg))
}

func publishCreateOpenBaoACKCompletionWithCommand(ctx context.Context, cfg deploymentConfig, store secretStore, parent, review string, original environmentRelease, ack *createOpenBaoACKRecovery, nativeProof deploymentplan.Object, run environmentReleaseCommand) error {
	if ack == nil {
		return createOpenBaoACKBlock("completed-native-repair")
	}
	var sourceOwner environmentCreateOwner
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, "source-owner.json"), &sourceOwner) != nil {
		return createOpenBaoACKBlock("completed-source-owner")
	}
	var current environmentCreateJournal
	if createOpenBaoACKCompletionRead(store, sourceOwner.Journal, &current) != nil {
		return createOpenBaoACKBlock("completed-parent-journal")
	}
	entry := createOpenBaoACKCompletionEntry(current, "openbao-host-ack-verified-"+review)
	if entry == nil {
		return createOpenBaoACKBlock("completed-native-receipt")
	}
	proof := createOpenBaoACKCompletion{Schema: createOpenBaoACKCompletionSchema, Environment: cfg.Environment, Stack: original.Stack, Parent: parent, Review: review, OriginalReleaseSHA: deploymentplan.Digest(original), Recovery: *ack, Native: deploymentplan.Clone(nativeProof), CompletedAt: entry.CompletedAt}
	if err := validateCreateOpenBaoACKCompletion(ctx, cfg, store, parent, original, &proof, run); err != nil {
		return err
	}
	// The immutable mirror precedes the active pointer. An interrupted publish
	// can retry only these exact authenticated bytes.
	raw := mustProviderRecoveryJSON(proof)
	for _, directory := range []string{filepath.Join("deployment/create/openbao-host-ack", review), filepath.Dir(createOpenBaoACKCompletionPath(parent))} {
		if _, err := prepareEnvironmentCreateDirectories(store, directory); err != nil {
			return err
		}
	}
	if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/openbao-host-ack", review, "completed.json"), raw); err != nil {
		return err
	}
	return writeEnvironmentCreateDerivedPrivateFile(store, createOpenBaoACKCompletionPath(parent), raw)
}

func validateCreateOpenBaoACKCompletion(ctx context.Context, cfg deploymentConfig, store secretStore, parent string, original environmentRelease, proof *createOpenBaoACKCompletion, run environmentReleaseCommand, commands ...*environmentCreateExecution) error {
	return validateCreateOpenBaoACKCompletionSource(ctx, cfg, store, parent, original, proof, run, false, commands...)
}

// terminalCompleted is used only by the completed-bootstrap handoff after the
// durable finalizer and every cleanup receipt have been authenticated. The
// original ACK source/mirror, accepted child and native proof still pass below;
// its obsolete pre-finalization ancestry is not reconstructed again.
func validateCreateOpenBaoACKCompletionSource(ctx context.Context, cfg deploymentConfig, store secretStore, parent string, original environmentRelease, proof *createOpenBaoACKCompletion, run environmentReleaseCommand, terminalCompleted bool, commands ...*environmentCreateExecution) error {
	bad := func() error { return createOpenBaoACKBlock("completed-authenticated-chain") }
	if ctx == nil || ctx.Err() != nil || proof == nil || cfg.Environment != "dev" || cfg.Environment != store.Environment || proof.Schema != createOpenBaoACKCompletionSchema || proof.Environment != cfg.Environment || proof.Stack != "video-cloud-"+cfg.Environment || original.Environment != cfg.Environment || original.Stack != proof.Stack || proof.Parent != parent || !environmentPreflightDigest.MatchString(parent) || !environmentPreflightDigest.MatchString(proof.Review) || proof.OriginalReleaseSHA != deploymentplan.Digest(original) || original.Images["LKE_OPENBAO_PKI_IMAGE"].Reference == "" || proof.CompletedAt.IsZero() {
		return bad()
	}
	ack := &proof.Recovery
	defs, err := createOpenBaoACKDefinitions(ack)
	if err != nil || ack.Candidate == nil || len(ack.Candidate.Resources) != 1 || ack.Candidate.Environment != cfg.Environment || ack.Candidate.Stack != proof.Stack || ack.Candidate.ReleaseSHA256 != proof.OriginalReleaseSHA || ack.Candidate.Resources[0].Action != "update" || ack.Candidate.Resources[0].UID != ack.Original.UID || ack.Candidate.Resources[0].ID != ack.Original.ID || deploymentplan.Digest(ack.Candidate.Resources[0].Desired) != deploymentplan.Digest(defs[0].Object) || !deploymentplan.Matches(ack.Original.Desired, ack.Candidate.Resources[0].Baseline) {
		return bad()
	}
	base := filepath.Join("deployment/create/openbao-recovery", proof.Review)
	var captured createOpenBaoACKRecovery
	var owner environmentCreateOwner
	var before, current environmentCreateJournal
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, "openbao-host-ack-source.json"), &captured) != nil || deploymentplan.Digest(captured) != deploymentplan.Digest(*ack) || createOpenBaoACKCompletionRead(store, filepath.Join(base, "source-owner.json"), &owner) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(base, "source-journal.json"), &before) != nil || owner.Schema != environmentCreateOwnerSchema || owner.Environment != cfg.Environment || owner.Stack != proof.Stack || owner.PlanSHA256 != parent || owner.OwnerToken == "" || !strings.HasPrefix(owner.Journal, filepath.Join("deployment/create/executions", parent)+string(filepath.Separator)+"execution-") || filepath.Base(owner.Journal) != "journal.json" || createOpenBaoACKCompletionRead(store, owner.Journal, &current) != nil || !createOpenBaoACKCompletionMembership(before, current, owner) {
		return bad()
	}
	var actualOwner environmentCreateOwner
	ownerPath := "deployment/.execution-active/owner.json"
	if !current.CompletedAt.IsZero() {
		ownerPath = filepath.Join(filepath.Dir(owner.Journal), "owner/owner.json")
	}
	if createOpenBaoACKCompletionRead(store, ownerPath, &actualOwner) != nil || deploymentplan.Digest(actualOwner) != deploymentplan.Digest(owner) {
		return bad()
	}
	e := &environmentCreateExecution{Store: store, Journal: current, owner: owner}
	volumeSource := *e // authenticated canonical snapshot, solely for pure allocation proofs
	e.volumeExecution = &volumeSource
	if len(commands) > 1 {
		return bad()
	}
	if len(commands) == 1 {
		command := commands[0]
		if command == nil || command.Store != store || deploymentplan.Digest(command.owner) != deploymentplan.Digest(owner) || deploymentplan.Digest(command.Journal) != deploymentplan.Digest(current) {
			return bad()
		}
		e.lateSourceReuse = command.lateSourceReuse
		e.volumeExecution = command
	}
	a, source, err := loadAdmittedCreateRecovery(store, e, proof.Review)
	if err != nil || a.Plan.Schema != "rtk-cloud.create-managed-application-trust-openbao-ack-recovery/v1" || a.Plan.ParentOwnerSHA != deploymentplan.Digest(owner) || deploymentplan.Digest(source) != deploymentplan.Digest(before) || deploymentplan.Digest(a.Plan.Plan.Release) != proof.OriginalReleaseSHA {
		return bad()
	}
	for key, digest := range map[string]string{
		"selection": ack.SelectionSHA, "manifest": ack.ManifestSHA, "image": deploymentplan.Digest(ack.Image), "original-release": proof.OriginalReleaseSHA,
		"original-plan": ack.OriginalPlanSHA, "original-journal": ack.OriginalJournalSHA, "candidate-plan": ack.Candidate.SHA256(), "source-pod": deploymentplan.Digest(ack.SourcePodUID), "source-process": deploymentplan.Digest(ack.SourceProcess), "source": deploymentplan.Digest(*ack),
	} {
		if a.Plan.Materials["accepted-service-core-openbao-ack-"+key] != digest {
			return bad()
		}
	}
	var originalPlan deploymentplan.Plan
	var originalJournal deploymentplan.Journal
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/rollback", ack.OriginalPlanSHA, "plan.json"), &originalPlan) != nil || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/rollback", ack.OriginalPlanSHA, "journal.json"), &originalJournal) != nil || originalPlan.SHA256() != ack.OriginalPlanSHA || deploymentplan.Digest(originalJournal) != ack.OriginalJournalSHA || !createOpenBaoACKCompletedChild(&originalPlan, originalJournal) {
		return bad()
	}
	found := 0
	for _, resource := range originalPlan.Resources {
		if resource.ID == ack.Original.ID && deploymentplan.Digest(resource) == deploymentplan.Digest(ack.Original) && originalJournal.Owners[resource.ID] == ack.Original.UID {
			found++
		}
	}
	child, childEntry, err := readCreateOpenBaoACKCompletedStage(store, current, "openbao-host-ack-forward-"+proof.Review)
	if err != nil || found != 1 || childEntry.CompletedAt.After(proof.CompletedAt) {
		return bad()
	}
	normalized := *child
	normalized.BindingsSHA256 = ack.Candidate.BindingsSHA256
	if normalized.SHA256() != ack.Candidate.SHA256() || validateCreateOpenBaoACKNativeProof(cfg, parent, proof.Review, ack, child, proof.Native) != nil {
		return bad()
	}
	verified := createOpenBaoACKCompletionEntry(current, "openbao-host-ack-verified-"+proof.Review)
	if verified == nil || verified.Phase != "openbao-host-ack-verification" || verified.State != "completed" || !verified.CompletedAt.Equal(proof.CompletedAt) || verified.IntentSHA256 != activeTransportJournalDigest(proof.Native) || activeTransportJournalDigest(jsonRawObject(verified.Receipt)) != activeTransportJournalDigest(proof.Native) {
		return bad()
	}
	// Bind completion to the actually accepted current application owners, rather
	// than a prospective candidate or any older failed application forward.
	apps, appsEntry, err := readCreateOpenBaoACKCompletedStage(store, current, "managed-application-owners-forward-"+proof.Review)
	var preview deploymentplan.Plan
	if err != nil || len(apps.Resources) != 26 || appsEntry.CompletedAt.After(proof.CompletedAt) || createOpenBaoACKCompletionRead(store, filepath.Join(base, "openbao-root-trust-plan.json"), &preview) != nil || preview.SHA256() != a.Plan.Materials["accepted-service-core-managed-application-forward-plan"] {
		return bad()
	}
	normalizedApps := *apps
	normalizedApps.BindingsSHA256 = preview.BindingsSHA256
	if normalizedApps.SHA256() != preview.SHA256() {
		return bad()
	}
	image, selection, manifest, err := loadCreateOpenBaoACKRepairImageWithCommand(ctx, cfg, store, parent, run)
	if err != nil || selection != ack.SelectionSHA || manifest != ack.ManifestSHA || deploymentplan.Digest(image) != deploymentplan.Digest(ack.Image) {
		return bad()
	}
	if proof.Native["late_review_sha256"] != nil && !terminalCompleted {
		return validateCreateManagedLateOpenBaoACKCompletion(ctx, cfg, store, e, proof)
	}
	return nil
}

func createOpenBaoACKCompletionEntry(j environmentCreateJournal, key string) *environmentCreateJournalEntry {
	var result *environmentCreateJournalEntry
	for i := range j.Entries {
		if j.Entries[i].Key == key {
			if result != nil {
				return nil
			}
			result = &j.Entries[i]
		}
	}
	return result
}

func createOpenBaoACKCompletionMembership(before, current environmentCreateJournal, owner environmentCreateOwner) bool {
	identity := func(j environmentCreateJournal) environmentCreateJournal {
		j.Entries = nil
		j.KubernetesLeases = nil
		j.CompletedAt = time.Time{}
		return j
	}
	if before.Schema != environmentCreateJournalSchema || before.Environment != owner.Environment || before.Stack != owner.Stack || before.PlanSHA256 != owner.PlanSHA256 || before.OwnerToken != owner.OwnerToken || deploymentplan.Digest(identity(before)) != deploymentplan.Digest(identity(current)) || len(current.Entries) < len(before.Entries) || len(current.KubernetesLeases) < len(before.KubernetesLeases) || deploymentplan.Digest(current.KubernetesLeases[:len(before.KubernetesLeases)]) != deploymentplan.Digest(before.KubernetesLeases) {
		return false
	}
	seen := map[string]bool{}
	for _, entry := range current.Entries {
		if entry.Key == "" || seen[entry.Key] {
			return false
		}
		seen[entry.Key] = true
		if !current.CompletedAt.IsZero() && (entry.State != "completed" || entry.CompletedAt.IsZero() || entry.CompletedAt.After(current.CompletedAt)) {
			return false
		}
	}
	for i, old := range before.Entries {
		entry := current.Entries[i]
		if old.Key != entry.Key || old.Phase != entry.Phase || old.IntentSHA256 != entry.IntentSHA256 || !old.StartedAt.Equal(entry.StartedAt) || old.State == "completed" && (entry.State != "completed" || !old.CompletedAt.Equal(entry.CompletedAt) || !bytes.Equal(old.Receipt, entry.Receipt)) || old.State != "completed" && (old.State != "pending" || entry.State != "pending" && entry.State != "completed") {
			return false
		}
	}
	return true
}

func createOpenBaoACKCompletedChild(plan *deploymentplan.Plan, journal deploymentplan.Journal) bool {
	if plan == nil || journal.PlanSHA256 != plan.SHA256() || !journal.Accepted || journal.Failed != "" || journal.InFlight != "" || len(journal.Completed) != len(plan.Resources) || len(journal.Owners) != len(plan.Resources) {
		return false
	}
	seen := map[string]bool{}
	for _, resource := range plan.Resources {
		if seen[resource.ID] || !slices.Contains(journal.Completed, resource.ID) || !environmentBootstrapOwnerUUID.MatchString(journal.Owners[resource.ID]) || resource.UID != "" && journal.Owners[resource.ID] != resource.UID {
			return false
		}
		seen[resource.ID] = true
	}
	return true
}

func readCreateOpenBaoACKCompletedStage(store secretStore, parent environmentCreateJournal, stage string) (*deploymentplan.Plan, *environmentCreateJournalEntry, error) {
	entry := createOpenBaoACKCompletionEntry(parent, "kubernetes/"+stage)
	var receipt struct {
		Plan   string            `json:"plan_sha256"`
		Owners map[string]string `json:"owners"`
	}
	if entry == nil || entry.Phase != stage || entry.State != "completed" || entry.CompletedAt.IsZero() || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || !environmentPreflightDigest.MatchString(receipt.Plan) {
		return nil, nil, createOpenBaoACKBlock("completed-child-stage")
	}
	var plan deploymentplan.Plan
	var journal deploymentplan.Journal
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/rollback", receipt.Plan, "plan.json"), &plan) != nil || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/rollback", receipt.Plan, "journal.json"), &journal) != nil || !createOpenBaoACKCompletedChild(&plan, journal) || deploymentplan.Digest(receipt.Owners) != deploymentplan.Digest(journal.Owners) {
		return nil, nil, createOpenBaoACKBlock("completed-child-stage")
	}
	intent, err := environmentCreateKubernetesStageIntent(&plan)
	if err != nil || entry.IntentSHA256 != activeTransportJournalDigest(jsonRawObject(intent)) {
		return nil, nil, createOpenBaoACKBlock("completed-child-intent")
	}
	return &plan, entry, nil
}

func jsonRawObject(raw []byte) deploymentplan.Object {
	var object deploymentplan.Object
	if decodeEnvironmentJSON(raw, &object) != nil {
		return nil
	}
	return object
}

func validateCreateOpenBaoACKNativeProof(cfg deploymentConfig, parent, review string, ack *createOpenBaoACKRecovery, child *deploymentplan.Plan, proof deploymentplan.Object) error {
	bad := func() error { return createOpenBaoACKBlock("completed-genuine-nine-ack") }
	if proof["parent"] != parent || proof["review"] != review || proof["statefulset_uid"] != ack.Original.UID || proof["child_plan_sha256"] != child.SHA256() || !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(proof["pod_uid"])) || proof["pod_uid"] == ack.SourcePodUID || !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(proof["session_id"])) {
		return bad()
	}
	for _, field := range []string{"process_sha256", "clients_sha256", "servers_sha256", "volume_sha256", "session_sha256", "original_init_sha256", "audit_sha256"} {
		if !environmentPreflightDigest.MatchString(deploymentplan.String(proof[field])) {
			return bad()
		}
	}
	for key, value := range proof {
		if strings.HasSuffix(key, "_sha256") && !environmentPreflightDigest.MatchString(deploymentplan.String(value)) {
			return bad()
		}
	}
	wanted, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil || len(wanted) != 9 {
		return bad()
	}
	for _, field := range []string{"subjects", "installed_subjects"} {
		var subjects []string
		if decodeEnvironmentJSON(mustProviderRecoveryJSON(proof[field]), &subjects) != nil || len(subjects) != 9 {
			return bad()
		}
		slices.Sort(subjects)
		if !slices.Equal(subjects, wanted) {
			return bad()
		}
	}
	return nil
}
