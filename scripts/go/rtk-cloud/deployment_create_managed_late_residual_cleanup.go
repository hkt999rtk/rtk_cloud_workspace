package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const managedLateResidualCleanupSchema = "rtk-cloud.create-managed-late-residual-copy-cleanup/v1"
const managedLateResidualCleanupLimit = 60 * time.Minute

type createManagedLateResidualCleanup struct {
	Approval                                 *createManagedLateResidualCleanupApproval
	PostNativeSHA, OldPostNativeAuthoritySHA string
	CredentialMode, CredentialSHA            string
	CredentialExpires                        time.Time
	NativeEvidence                           map[string]string
}

type createManagedLateResidualCleanupAuthority struct {
	OriginalSourceSHA                                                                           string `json:"original_source_sha256,omitempty"`
	PriorAuthoritySHA                                                                           string `json:"prior_authority_sha256,omitempty"`
	CompletedSHA                                                                                string `json:"completed_sha256,omitempty"`
	Schema, ParentSHA, SourceSHA, OwnerSHA, Review, CheckerSHA, ArtifactSHA, QualifiedBytesSHA  string
	DescriptorSHA, ResidualSHA, ProposalSHA, ApprovalSHA, OldPostNativeAuthoritySHA, PendingSHA string
	SessionID, Fingerprint                                                                      string
	StartedAt, Deadline, OverallDeadline, CredentialExpires                                     time.Time
}

func managedLateResidualCleanupPolicySHA() string {
	return deploymentplan.Digest(deploymentplan.Object{"schema": managedLateResidualCleanupSchema, "phase": managedLateResidualCleanupLimit.String(), "caller": managedLateOverallLimit.String(), "first_admission_reserve": managedLateFinalizerLimit.String(), "current_credential_reserve": managedLateExpiryReserve.String(), "admission": "explicit-approved-proposal-paired-exclusive-witness-first", "writes": "only-approved-24-historical-secret-uids-and-original-finalizer-receipt"})
}

func managedLateResidualCleanupApprovalValid(a *createManagedLateResidualCleanupApproval) bool {
	if a == nil || a.ApprovedAt.IsZero() || a.InventorySHA != deploymentplan.Digest(a.Objects) || a.CleanupLimit != managedLateResidualCleanupLimit || a.OverallLimit != managedLateOverallLimit || len(a.Objects) != 24 {
		return false
	}
	for _, sha := range []string{a.ProposalSHA, a.ApprovalSHA, a.ParentSHA, a.SourceSHA, a.OwnerSHA} {
		if !environmentPreflightDigest.MatchString(sha) {
			return false
		}
	}
	seen, uids := map[string]bool{}, map[string]bool{}
	for _, object := range a.Objects {
		if !environmentBootstrapOwnerUUID.MatchString(object.UID) || !environmentBootstrapOwnerUUID.MatchString(object.SessionID) || object.Namespace == "" || object.ResourceID != "Secret/"+object.Namespace+"/service-bootstrap-"+object.SessionID || seen[object.ResourceID] || uids[object.UID] {
			return false
		}
		seen[object.ResourceID], uids[object.UID] = true, true
	}
	return true
}

func managedLateResidualCleanupBaseValid(r *createManagedLateRemaining) bool {
	if r == nil || r.ResidualCleanup == nil || !managedLatePostNativeCleanupAuthorityMatches(r, r.PostNativeAuthority) {
		return false
	}
	p := r.ResidualCleanup
	return managedLateResidualCleanupApprovalValid(p.Approval) && p.Approval.ParentSHA == r.ParentSHA && p.Approval.SourceSHA == r.PostNativeCleanup.SourceSHA && p.Approval.OwnerSHA == r.OwnerSHA && p.PostNativeSHA == deploymentplan.Digest(r.PostNativeCleanup) && p.OldPostNativeAuthoritySHA == deploymentplan.Digest(r.PostNativeAuthority) && r.PostNativeCleanup.Native.FinalizedAt.Before(r.LeafNotAfter) && r.PostNativeCleanup.Native.FinalizedAt.Before(r.SessionExpires)
}

func managedLateResidualCleanupDeadline(start, overall, credential time.Time) time.Time {
	deadline := start.Add(managedLateResidualCleanupLimit)
	cutoffs := []time.Time{overall}
	if !credential.IsZero() {
		cutoffs = append(cutoffs, credential.Add(-managedLateExpiryReserve))
	}
	for _, cutoff := range cutoffs {
		if cutoff.Before(deadline) {
			deadline = cutoff
		}
	}
	return deadline
}

func managedLateResidualCleanupAuthorityMatches(r *createManagedLateRemaining, a *createManagedLateResidualCleanupAuthority) bool {
	if !managedLateResidualCleanupBaseValid(r) || a == nil {
		return false
	}
	p := r.ResidualCleanup
	for _, sha := range []string{a.ParentSHA, a.SourceSHA, a.OwnerSHA, a.Review, a.CheckerSHA, a.ArtifactSHA, a.QualifiedBytesSHA, a.DescriptorSHA, a.ResidualSHA, a.ProposalSHA, a.ApprovalSHA, a.OldPostNativeAuthoritySHA, a.PendingSHA, a.Fingerprint} {
		if !environmentPreflightDigest.MatchString(sha) {
			return false
		}
	}
	return a.Schema == managedLateResidualCleanupSchema && a.ParentSHA == r.ParentSHA && a.SourceSHA == p.Approval.SourceSHA && a.OwnerSHA == r.OwnerSHA && a.DescriptorSHA == managedLateRemainingDigest(r) && a.ResidualSHA == deploymentplan.Digest(p) && a.ProposalSHA == p.Approval.ProposalSHA && a.ApprovalSHA == p.Approval.ApprovalSHA && a.OldPostNativeAuthoritySHA == deploymentplan.Digest(r.PostNativeAuthority) && a.PendingSHA == r.TerminalFinalizerCompletion.PendingSHA && a.SessionID == r.SessionID && a.Fingerprint == r.Fingerprint && !a.StartedAt.IsZero() && !a.StartedAt.Before(r.PostNativeCleanup.Native.FinalizedAt) && a.CredentialExpires.Equal(p.CredentialExpires) && managedLateResidualCredentialValid(p) && a.OverallDeadline.After(a.StartedAt) && !a.OverallDeadline.After(a.StartedAt.Add(managedLateOverallLimit)) && a.Deadline.Equal(managedLateResidualCleanupDeadline(a.StartedAt, a.OverallDeadline, a.CredentialExpires)) && !a.Deadline.Before(a.StartedAt.Add(managedLateFinalizerLimit)) && managedLateResidualCleanupEvidenceValid(p.NativeEvidence) && p.NativeEvidence["current-credential"] == managedLateResidualCredentialSHA(p)
}

func newManagedLateResidualCleanupAuthority(r *createManagedLateRemaining, a createFoundationRecoveryArtifact, overall, now time.Time, bytesSHA string) (*createManagedLateResidualCleanupAuthority, error) {
	bad := func() (*createManagedLateResidualCleanupAuthority, error) {
		return nil, createOpenBaoACKBlock("residual-cleanup-fresh-authority")
	}
	if !managedLateResidualCleanupBaseValid(r) || r.ResidualCleanup.Approval.ApprovedAt.After(now) || a.Schema != "rtk-cloud.create-openbao-recovery-artifact/v1" || a.Plan.Schema != managedLateExpiredRenewalSchema || deploymentplan.Digest(a.Plan) != r.Review || a.Plan.ParentSHA != r.ParentSHA || a.Plan.ParentJournalSHA != r.ResidualCleanup.Approval.SourceSHA || a.Plan.ParentOwnerSHA != r.OwnerSHA || a.Plan.Materials[managedLateRemainingMaterial+"residual-cleanup"] != deploymentplan.Digest(r.ResidualCleanup) || a.Plan.Materials[managedLateRemainingMaterial+"residual-policy"] != managedLateResidualCleanupPolicySHA() || a.QualifiedAt.IsZero() || a.QualifiedAt.After(now) || !now.Before(a.ValidUntil) || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.Qualification.Decision != "GO" || a.Qualification.PlanSHA256 != r.Review || len(a.Qualification.Gates) != 6 {
		return bad()
	}
	for i, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		g := a.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != r.Review {
			return bad()
		}
	}
	p := r.ResidualCleanup
	result := &createManagedLateResidualCleanupAuthority{Schema: managedLateResidualCleanupSchema, ParentSHA: r.ParentSHA, SourceSHA: p.Approval.SourceSHA, OwnerSHA: r.OwnerSHA, Review: r.Review, CheckerSHA: a.Plan.CheckerSHA, ArtifactSHA: deploymentplan.Digest(a), QualifiedBytesSHA: bytesSHA, DescriptorSHA: managedLateRemainingDigest(r), ResidualSHA: deploymentplan.Digest(p), ProposalSHA: p.Approval.ProposalSHA, ApprovalSHA: p.Approval.ApprovalSHA, OldPostNativeAuthoritySHA: deploymentplan.Digest(r.PostNativeAuthority), PendingSHA: r.TerminalFinalizerCompletion.PendingSHA, SessionID: r.SessionID, Fingerprint: r.Fingerprint, StartedAt: now, OverallDeadline: overall, CredentialExpires: p.CredentialExpires, Deadline: managedLateResidualCleanupDeadline(now, overall, p.CredentialExpires)}
	if !managedLateResidualCleanupAuthorityMatches(r, result) {
		return bad()
	}
	return result, nil
}

func managedLateResidualCleanupPath(parent string, witness bool) string {
	name := "managed-late-residual-copy-cleanup-grant-authority.json"
	if witness {
		name = "managed-late-residual-copy-cleanup-grant-admission.json"
	}
	return filepath.Join("deployment/create", parent, "bootstrap/service", name)
}

func readManagedLateResidualCleanupPair(store secretStore, parent string) ([]byte, error) {
	return readManagedLateResidualPairAt(store, parent, false)
}

func readManagedLateResidualPairAt(store secretStore, parent string, remaining bool) ([]byte, error) {
	bad := func() ([]byte, error) { return nil, createOpenBaoACKBlock("residual-cleanup-admission-pair") }
	if !environmentPreflightDigest.MatchString(parent) {
		return bad()
	}
	paths := []string{managedLateResidualCleanupPath(parent, false), managedLateResidualCleanupPath(parent, true)}
	if remaining {
		paths = []string{managedLateResidualRemainingPath(parent, false), managedLateResidualRemainingPath(parent, true)}
	}
	absent := 0
	for i, relative := range paths {
		path, err := privateDeploymentStatePath(store.Environment, relative)
		if err != nil || path != filepath.Join(store.Root, relative) || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
			return bad()
		}
		paths[i] = path
		if _, err = os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			absent++
		} else if err != nil {
			return bad()
		}
	}
	if absent == 2 {
		return nil, nil
	}
	if absent != 0 {
		return bad()
	}
	a, err := readBoundedEnvironmentFile(paths[0], true)
	b, other := readBoundedEnvironmentFile(paths[1], true)
	if err != nil || other != nil || len(a) == 0 || !bytes.Equal(a, b) {
		return bad()
	}
	return a, nil
}

func writeManagedLateResidualCleanupPair(store secretStore, parent string, raw []byte) error {
	return writeManagedLateResidualPairAt(store, parent, raw, false)
}

func writeManagedLateResidualPairAt(store secretStore, parent string, raw []byte, remaining bool) error {
	prior, err := readManagedLateResidualPairAt(store, parent, remaining)
	if err != nil || len(prior) != 0 || len(raw) == 0 {
		return createOpenBaoACKBlock("residual-cleanup-admission-pair-exists")
	}
	for _, witness := range []bool{true, false} {
		relative := managedLateResidualCleanupPath(parent, witness)
		if remaining {
			relative = managedLateResidualRemainingPath(parent, witness)
		}
		path, err := privateDeploymentStatePath(store.Environment, relative)
		if err != nil || writeExclusivePrivateDeploymentJSON(path, raw) != nil || syncEnvironmentPreflightDirectory(filepath.Dir(path)) != nil {
			return createOpenBaoACKBlock("residual-cleanup-admission-pair-write")
		}
	}
	got, err := readManagedLateResidualPairAt(store, parent, remaining)
	if err != nil || !bytes.Equal(got, raw) {
		return createOpenBaoACKBlock("residual-cleanup-admission-pair-readback")
	}
	return nil
}

func readManagedLateResidualCleanupAuthority(store secretStore, r *createManagedLateRemaining) (*createManagedLateResidualCleanupAuthority, error) {
	bad := func() (*createManagedLateResidualCleanupAuthority, error) {
		return nil, createOpenBaoACKBlock("residual-cleanup-immutable-authority")
	}
	if r == nil || r.ResidualCleanup == nil {
		return bad()
	}
	approval, err := loadManagedLateResidualCleanupApproval(store, r.ParentSHA)
	if err != nil || deploymentplan.Digest(approval) != deploymentplan.Digest(r.ResidualCleanup.Approval) {
		return bad()
	}
	raw, err := readManagedLateResidualCleanupPair(store, r.ParentSHA)
	if err != nil {
		return bad()
	}
	if raw == nil {
		return nil, nil
	}
	var result createManagedLateResidualCleanupAuthority
	if decodeEnvironmentJSON(raw, &result) != nil || !bytes.Equal(raw, mustProviderRecoveryJSON(result)) || !managedLateResidualCleanupAuthorityMatches(r, &result) {
		return bad()
	}
	path := filepath.Join(store.Root, "deployment/create/openbao-recovery", result.Review, "qualification.json")
	qraw, err := readBoundedEnvironmentFile(path, true)
	var a createFoundationRecoveryArtifact
	if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil || environmentPreflightBytesSHA256(qraw) != result.QualifiedBytesSHA || decodeEnvironmentJSON(qraw, &a) != nil || deploymentplan.Digest(a) != result.ArtifactSHA {
		return bad()
	}
	copy := *r
	copy.Review = result.Review
	expected, err := newManagedLateResidualCleanupAuthority(&copy, a, result.OverallDeadline, result.StartedAt, result.QualifiedBytesSHA)
	if err != nil || deploymentplan.Digest(expected) != deploymentplan.Digest(result) {
		return bad()
	}
	return &result, nil
}

func loadManagedLateResidualCleanupDescriptor(store secretStore, r *createManagedLateRemaining) error {
	approval, err := loadManagedLateResidualCleanupApproval(store, r.ParentSHA)
	if err != nil {
		return err
	}
	if approval == nil {
		return nil
	}
	r.ResidualCleanup = &createManagedLateResidualCleanup{Approval: approval, PostNativeSHA: deploymentplan.Digest(r.PostNativeCleanup), OldPostNativeAuthoritySHA: deploymentplan.Digest(r.PostNativeAuthority)}
	if !managedLateResidualCleanupBaseValid(r) {
		return createOpenBaoACKBlock("residual-cleanup-approved-source")
	}
	raw, err := readManagedLateResidualCleanupPair(store, r.ParentSHA)
	if err != nil {
		return err
	}
	if raw == nil {
		return nil
	}
	var header createManagedLateResidualCleanupAuthority
	var archived createManagedLateRemaining
	if decodeEnvironmentJSON(raw, &header) != nil || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", header.Review, managedLateRemainingFilename), &archived) != nil || !managedLateResidualCleanupSourceMatches(r.ResidualCleanup, archived.ResidualCleanup) {
		return createOpenBaoACKBlock("residual-cleanup-archived-source")
	}
	r.ResidualCleanup = archived.ResidualCleanup
	r.ResidualAuthority, err = readManagedLateResidualCleanupAuthority(store, r)
	if err != nil {
		return err
	}
	return loadManagedLateResidualRemainingDescriptor(store, r)
}

func managedLateResidualCleanupSourceMatches(a, b *createManagedLateResidualCleanup) bool {
	if a == nil || b == nil {
		return false
	}
	x, y := *a, *b
	x.NativeEvidence, y.NativeEvidence = nil, nil
	x.CredentialExpires, y.CredentialExpires = time.Time{}, time.Time{}
	x.CredentialMode, y.CredentialMode = "", ""
	x.CredentialSHA, y.CredentialSHA = "", ""
	return deploymentplan.Digest(x) == deploymentplan.Digest(y)
}

// This is only a routing envelope. The source loader must authenticate the
// existing paired authority and every approved UID receipt before it is used.
func managedLateResidualCleanupSelected(j environmentCreateJournal) bool {
	if len(j.Entries) <= 400 || len(j.Entries) > 424 || !j.CompletedAt.IsZero() {
		return false
	}
	prefix := j
	prefix.Entries = prefix.Entries[:400]
	if !managedLatePostNativeCleanupSelected(prefix) {
		return false
	}
	now := time.Now().UTC()
	seen := map[string]bool{}
	for _, entry := range j.Entries[400:] {
		if len(entry.Key) <= len("bootstrap-copy-retire-") || entry.Key[:len("bootstrap-copy-retire-")] != "bootstrap-copy-retire-" || seen[entry.Key] || entry.Phase != "bootstrap" || entry.State != "completed" || entry.StartedAt.IsZero() || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(now) || len(entry.Receipt) == 0 {
			return false
		}
		seen[entry.Key] = true
	}
	return true
}

func managedLateResidualCleanupProgressValid(j environmentCreateJournal, remaining *createManagedLateRemaining) bool {
	if remaining != nil && remaining.ResidualRemaining != nil {
		return managedLateResidualRemainingProgressValid(j, remaining)
	}
	if remaining == nil || !managedLateResidualCleanupBaseValid(remaining) || len(j.Entries) < 400 || len(j.Entries) > 424 || !j.CompletedAt.IsZero() {
		return false
	}
	prefix := j
	prefix.Entries = prefix.Entries[:400]
	if !managedLatePostNativeCleanupSelected(prefix) || deploymentplan.Digest(prefix) != remaining.ResidualCleanup.Approval.SourceSHA {
		return false
	}
	if len(j.Entries) == 400 {
		return true
	}
	if !managedLateResidualCleanupSelected(j) || !managedLateResidualCleanupAuthorityMatches(remaining, remaining.ResidualAuthority) {
		return false
	}
	allowed := map[string]createManagedLateResidualCleanupObject{}
	for _, object := range remaining.ResidualCleanup.Approval.Objects {
		allowed["bootstrap-copy-retire-"+object.Namespace+"-"+object.SessionID] = object
	}
	seen := map[string]bool{}
	for _, entry := range j.Entries[400:] {
		object, ok := allowed[entry.Key]
		var receipt struct {
			ID     string `json:"resource_id"`
			UID    string `json:"uid"`
			Absent bool   `json:"absent"`
		}
		intent := deploymentplan.Object{"resource_id": object.ResourceID, "uid": object.UID, "session_id": object.SessionID}
		if !ok || seen[entry.Key] || entry.Phase != "bootstrap" || entry.State != "completed" || entry.StartedAt.IsZero() || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || entry.IntentSHA256 != activeTransportJournalDigest(intent) || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt.ID != object.ResourceID || receipt.UID != object.UID || !receipt.Absent || entry.StartedAt.Before(remaining.ResidualAuthority.StartedAt) || entry.CompletedAt.After(remaining.ResidualAuthority.Deadline) {
			return false
		}
		seen[entry.Key] = true
	}
	return true
}

// Pure historical readers may see the routing envelope without a prepared
// runtime. Authenticate its recorded authority chain before using that scope.
func verifyManagedLateRecordedResidualCleanupProgress(store secretStore, e *environmentCreateExecution) error {
	bad := func() error { return createOpenBaoACKBlock("residual-cleanup-recorded-progress") }
	if e == nil || e.Store != store || !managedLateResidualCleanupSelected(e.Journal) {
		return bad()
	}
	raw, err := readManagedLateResidualCleanupPair(store, e.Journal.PlanSHA256)
	var header createManagedLateResidualCleanupAuthority
	var r createManagedLateRemaining
	if err != nil || len(raw) == 0 || decodeEnvironmentJSON(raw, &header) != nil || header.ParentSHA != e.Journal.PlanSHA256 || !environmentPreflightDigest.MatchString(header.Review) || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", header.Review, managedLateRemainingFilename), &r) != nil || r.Review != header.Review || r.ParentSHA != e.Journal.PlanSHA256 || r.OwnerSHA != deploymentplan.Digest(e.owner) || r.ResidualCleanup == nil {
		return bad()
	}
	if r.Authority, err = readManagedLateRemainingAuthority(store, &r); err != nil {
		return err
	}
	if r.CleanupAuthority, err = readManagedLateInstalledCleanupAuthority(store, &r); err != nil {
		return err
	}
	if r.FinalizationAuthority, err = readManagedLateFinalizationAuthority(store, &r); err != nil {
		return err
	}
	if r.TerminalAuthority, err = readManagedLateTerminalFinalizerAuthority(store, &r); err != nil {
		return err
	}
	if r.PostNativeAuthority, err = readManagedLatePostNativeCleanupAuthority(store, &r); err != nil {
		return err
	}
	if r.ResidualAuthority, err = readManagedLateResidualCleanupAuthority(store, &r); err != nil || r.ResidualAuthority == nil || deploymentplan.Digest(r.ResidualAuthority) != deploymentplan.Digest(header) {
		return bad()
	}
	if err = loadManagedLateResidualRemainingDescriptor(store, &r); err != nil {
		return err
	}
	if !managedLateResidualCleanupProgressValid(e.Journal, &r) {
		return bad()
	}
	return nil
}

func verifyManagedLateResidualCleanupSource(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery) error {
	if err := verifyManagedLatePostNativeCleanupSource(ctx, cfg, store, e, r); err != nil {
		return err
	}
	remaining := r.ManagedLateRemaining
	if !managedLateResidualCleanupBaseValid(remaining) {
		return createOpenBaoACKBlock("residual-cleanup-approved-source")
	}
	approval, err := loadManagedLateResidualCleanupApproval(store, remaining.ParentSHA)
	if err != nil || deploymentplan.Digest(approval) != deploymentplan.Digest(remaining.ResidualCleanup.Approval) {
		return createOpenBaoACKBlock("residual-cleanup-operator-approval")
	}
	if !managedLateResidualCleanupProgressValid(e.Journal, remaining) {
		return createOpenBaoACKBlock("residual-cleanup-exact-object-tail")
	}
	return nil
}

func managedLateResidualCopyAllowed(r *createServiceSessionRecovery, s environmentCreateServiceCeremony, namespace string) bool {
	if r != nil && r.ManagedLateRemaining != nil && r.ManagedLateRemaining.ResidualRemaining != nil {
		return managedLateResidualRemainingCopyAllowed(r.ManagedLateRemaining, s, namespace)
	}
	if r == nil || r.ManagedLateRemaining == nil || r.ManagedLateRemaining.ResidualCleanup == nil || !managedLateResidualCleanupApprovalValid(r.ManagedLateRemaining.ResidualCleanup.Approval) {
		return false
	}
	for _, object := range r.ManagedLateRemaining.ResidualCleanup.Approval.Objects {
		if object.ResourceID == "Secret/"+namespace+"/"+s.BootstrapCredentialSecret && object.SessionID == s.BootstrapSessionID && object.Namespace == namespace && object.UID == s.CredentialUIDs[namespace] {
			return true
		}
	}
	return false
}

func verifyManagedLateResidualCleanupAuthorityCurrent(store secretStore, e *environmentCreateExecution, r *createManagedLateRemaining) error {
	if r != nil && r.ResidualRemaining != nil {
		return verifyManagedLateResidualRemainingAuthorityCurrent(store, e, r)
	}
	if e == nil || r == nil || r.ResidualCleanup == nil {
		return createOpenBaoACKBlock("residual-cleanup-authority-source")
	}
	a, err := readManagedLateResidualCleanupAuthority(store, r)
	if err == nil && a == nil && r.ResidualAuthority == nil && len(e.Journal.Entries) == 400 {
		return nil
	}
	if err != nil || a == nil || deploymentplan.Digest(a) != deploymentplan.Digest(r.ResidualAuthority) || !time.Now().Before(a.Deadline) {
		return createOpenBaoACKBlock("residual-cleanup-current-authority")
	}
	return nil
}

func verifyManagedLateResidualCleanupRuntime(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime) error {
	if runtime.ServiceSessionRecovery != nil && runtime.ServiceSessionRecovery.ManagedLateRemaining != nil && runtime.ServiceSessionRecovery.ManagedLateRemaining.ResidualRemaining != nil {
		return verifyManagedLateResidualRemainingRuntime(ctx, cfg, store, runtime)
	}
	r := runtime.ServiceSessionRecovery
	if err := verifyManagedLateResidualCleanupSource(ctx, cfg, store, runtime.Execution, r); err != nil {
		return err
	}
	a, err := readManagedLateResidualCleanupAuthority(store, r.ManagedLateRemaining)
	budget, budgetErr := managedLateActiveBudget(ctx)
	if err != nil || a == nil || budgetErr != nil || budget.ResidualAuthority == nil || deploymentplan.Digest(a) != deploymentplan.Digest(r.ManagedLateRemaining.ResidualAuthority) || deploymentplan.Digest(a) != deploymentplan.Digest(budget.ResidualAuthority) || runtime.ExecutionSHA256 != r.ManagedLateRemaining.ParentSHA || runtime.Backend.Store != store {
		return createOpenBaoACKBlock("residual-cleanup-runtime-authority")
	}
	return nil
}

func admitManagedLateResidualCleanup(ctx context.Context, store secretStore, e *environmentCreateExecution, a createFoundationRecoveryArtifact, bytesSHA string, r *createManagedLateRemaining, overall time.Time) (*createManagedLateRemainingAuthority, error) {
	if e == nil || e.Check(ctx) != nil || store != e.Store || !managedLateResidualCleanupBaseValid(r) || r.OwnerSHA != deploymentplan.Digest(e.owner) {
		return nil, createOpenBaoACKBlock("residual-cleanup-admission-source")
	}
	prior, err := readManagedLateResidualCleanupAuthority(store, r)
	if err != nil {
		return nil, err
	}
	if prior != nil {
		r.ResidualAuthority = prior
		if !managedLateResidualCleanupProgressValid(e.Journal, r) {
			return nil, createOpenBaoACKBlock("residual-cleanup-admission-source")
		}
		if !time.Now().Before(prior.Deadline) || overall.Before(prior.Deadline) {
			return nil, createOpenBaoACKBlock("residual-cleanup-fixed-budget")
		}
		return r.Authority, nil
	}
	if len(e.Journal.Entries) != 400 || !managedLateResidualCleanupProgressValid(e.Journal, r) {
		return nil, createOpenBaoACKBlock("residual-cleanup-admission-source")
	}
	phase, err := newManagedLateResidualCleanupAuthority(r, a, overall, time.Now().UTC(), bytesSHA)
	if err != nil {
		return nil, err
	}
	invalidateManagedLateSourceReuse(e)
	if err := writeManagedLateResidualCleanupPair(store, r.ParentSHA, mustProviderRecoveryJSON(phase)); err != nil {
		return nil, err
	}
	r.ResidualAuthority = phase
	return r.Authority, e.Check(ctx)
}

func beginManagedLateResidualCleanupBudget(ctx context.Context, materials map[string]string, r *createManagedLateRemaining) (context.Context, context.CancelFunc, error) {
	if ctx == nil || ctx.Err() != nil || ctx.Value(managedLateBudgetKey{}) != nil || r == nil || !managedLateResidualCleanupAuthorityMatches(r, r.ResidualAuthority) || !time.Now().Before(r.ResidualAuthority.Deadline) {
		return nil, nil, createOpenBaoACKBlock("residual-cleanup-fixed-budget")
	}
	if cutoff, ok := ctx.Deadline(); !ok || cutoff.Before(r.ResidualAuthority.Deadline) {
		return nil, nil, createOpenBaoACKBlock("residual-cleanup-caller-budget")
	}
	for key, value := range managedLateBudgetMaterials() {
		if materials[key] != value {
			return nil, nil, createOpenBaoACKBlock("late-qualified-budget")
		}
	}
	if materials[managedLateRemainingMaterial+"residual-policy"] != managedLateResidualCleanupPolicySHA() {
		return nil, nil, createOpenBaoACKBlock("residual-cleanup-qualified-policy")
	}
	overall, stop := context.WithDeadline(ctx, r.ResidualAuthority.OverallDeadline)
	budget := managedLateRuntimeBudget{StartedAt: r.FinalizationAuthority.StartedAt, Deadline: r.ResidualAuthority.Deadline, Overall: overall, Limit: managedLateResidualCleanupLimit, ResidualAuthority: r.ResidualAuthority}
	phase, cancel := context.WithDeadline(context.WithValue(overall, managedLateBudgetKey{}, budget), budget.Deadline)
	return phase, func() { cancel(); stop() }, nil
}

func managedLateResidualCredentialValid(p *createManagedLateResidualCleanup) bool {
	if p == nil || !environmentPreflightDigest.MatchString(p.CredentialSHA) {
		return false
	}
	switch p.CredentialMode {
	case "non-expiring-kubernetes-token":
		return p.CredentialExpires.IsZero()
	case "expiring-kubernetes-token", "kubernetes-client-certificate":
		return !p.CredentialExpires.IsZero()
	default:
		return false
	}
}

func managedLateResidualCleanupEvidenceValid(evidence map[string]string) bool {
	if len(evidence) != 15 || !environmentPreflightDigest.MatchString(evidence["current-credential"]) {
		return false
	}
	copy := map[string]string{}
	for key, value := range evidence {
		if key != "current-credential" {
			copy[key] = value
		}
	}
	return managedLateTerminalFinalizerEvidenceValid(copy)
}

func managedLateResidualRetirementsComplete(j environmentCreateJournal, a *createManagedLateResidualCleanupApproval, start, deadline time.Time) bool {
	if !managedLateResidualCleanupApprovalValid(a) || len(j.Entries) < 424 {
		return false
	}
	for _, object := range a.Objects {
		count := 0
		key := "bootstrap-copy-retire-" + object.Namespace + "-" + object.SessionID
		for index, entry := range j.Entries {
			if entry.Key != key {
				continue
			}
			count++
			var receipt struct {
				ID     string `json:"resource_id"`
				UID    string `json:"uid"`
				Absent bool   `json:"absent"`
			}
			intent := deploymentplan.Object{"resource_id": object.ResourceID, "uid": object.UID, "session_id": object.SessionID}
			if index < 400 || entry.Phase != "bootstrap" || entry.State != "completed" || entry.StartedAt.Before(start) || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(deadline) || entry.IntentSHA256 != activeTransportJournalDigest(intent) || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt.ID != object.ResourceID || receipt.UID != object.UID || !receipt.Absent {
				return false
			}
		}
		if count != 1 {
			return false
		}
	}
	return true
}
