package main

import (
	"bytes"
	"context"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const managedLateResidualRemainingSchema = "rtk-cloud.create-managed-late-residual-copy-cleanup-remaining/v1"

type createManagedLateResidualRemaining struct {
	Approval             *createManagedLateResidualRemainingApproval
	OriginalAuthoritySHA string
	NativeEvidence       map[string]string
}

func managedLateResidualRemainingPolicySHA() string {
	return deploymentplan.Digest(deploymentplan.Object{"schema": managedLateResidualRemainingSchema, "cleanup": managedLateResidualCleanupLimit.String(), "overall": managedLateOverallLimit.String(), "scope": "approved-remaining21-secret-uids-and-original396-receipt", "admission": "explicit-remaining-proposal-paired-witness-first", "old3": "unchanged-original-window", "first-reserve": managedLateFinalizerLimit.String()})
}
func managedLateResidualRemainingApprovalValid(a *createManagedLateResidualRemainingApproval) bool {
	if a == nil || len(a.Objects) != 21 || len(a.Completed) != 3 || a.CleanupLimit != managedLateResidualCleanupLimit || a.OverallLimit != managedLateOverallLimit || a.ApprovedAt.IsZero() || a.InventorySHA != deploymentplan.Digest(a.Objects) {
		return false
	}
	for _, sha := range []string{a.ProposalSHA, a.ApprovalSHA, a.ParentSHA, a.SourceSHA, a.SourceRawSHA, a.OriginalSourceSHA, a.OwnerSHA, a.OriginalAuthoritySHA, a.OldAuthorityRawSHA, a.CompletedSHA} {
		if !environmentPreflightDigest.MatchString(sha) {
			return false
		}
	}
	seen, uids := map[string]bool{}, map[string]bool{}
	for _, o := range append(append([]createManagedLateResidualCleanupObject(nil), a.Completed...), a.Objects...) {
		if o.ResourceID != "Secret/"+o.Namespace+"/service-bootstrap-"+o.SessionID || o.Namespace == "" || !environmentBootstrapOwnerUUID.MatchString(o.SessionID) || !environmentBootstrapOwnerUUID.MatchString(o.UID) || seen[o.ResourceID] || uids[o.UID] {
			return false
		}
		seen[o.ResourceID], uids[o.UID] = true, true
	}
	return true
}
func managedLateResidualRemainingBaseValid(r *createManagedLateRemaining) bool {
	if r == nil || r.ResidualRemaining == nil || !managedLateResidualCleanupBaseValid(r) || !managedLateResidualCleanupAuthorityMatches(r, r.ResidualAuthority) {
		return false
	}
	p, a := r.ResidualRemaining, r.ResidualRemaining.Approval
	if !managedLateResidualRemainingApprovalValid(a) || p.OriginalAuthoritySHA != deploymentplan.Digest(r.ResidualAuthority) || a.OriginalAuthoritySHA != p.OriginalAuthoritySHA || a.OriginalSourceSHA != r.ResidualCleanup.Approval.SourceSHA || a.ParentSHA != r.ParentSHA || a.OwnerSHA != r.OwnerSHA {
		return false
	}
	old := map[string]createManagedLateResidualCleanupObject{}
	for _, o := range r.ResidualCleanup.Approval.Objects {
		old[o.ResourceID] = o
	}
	for _, o := range append(append([]createManagedLateResidualCleanupObject(nil), a.Completed...), a.Objects...) {
		if deploymentplan.Digest(old[o.ResourceID]) != deploymentplan.Digest(o) {
			return false
		}
	}
	return true
}
func managedLateResidualEffectiveAuthority(r *createManagedLateRemaining) *createManagedLateResidualCleanupAuthority {
	if r != nil && r.ResidualRemaining != nil {
		return r.ResidualRemainingAuthority
	}
	if r != nil {
		return r.ResidualAuthority
	}
	return nil
}
func managedLateResidualRemainingAuthorityMatches(r *createManagedLateRemaining, a *createManagedLateResidualCleanupAuthority) bool {
	if !managedLateResidualRemainingBaseValid(r) || a == nil {
		return false
	}
	p := r.ResidualRemaining
	approval := p.Approval
	for _, sha := range []string{a.ParentSHA, a.SourceSHA, a.OwnerSHA, a.Review, a.CheckerSHA, a.ArtifactSHA, a.QualifiedBytesSHA, a.DescriptorSHA, a.ResidualSHA, a.ProposalSHA, a.ApprovalSHA, a.PriorAuthoritySHA, a.OriginalSourceSHA, a.CompletedSHA, a.PendingSHA, a.Fingerprint} {
		if !environmentPreflightDigest.MatchString(sha) {
			return false
		}
	}
	return a.Schema == managedLateResidualRemainingSchema && a.ParentSHA == r.ParentSHA && a.SourceSHA == approval.SourceSHA && a.OwnerSHA == r.OwnerSHA && a.DescriptorSHA == managedLateRemainingDigest(r) && a.ResidualSHA == deploymentplan.Digest(p) && a.ProposalSHA == approval.ProposalSHA && a.ApprovalSHA == approval.ApprovalSHA && a.PriorAuthoritySHA == deploymentplan.Digest(r.ResidualAuthority) && a.OriginalSourceSHA == approval.OriginalSourceSHA && a.CompletedSHA == approval.CompletedSHA && a.OldPostNativeAuthoritySHA == deploymentplan.Digest(r.PostNativeAuthority) && a.PendingSHA == r.TerminalFinalizerCompletion.PendingSHA && a.SessionID == r.SessionID && a.Fingerprint == r.Fingerprint && !a.StartedAt.IsZero() && !a.StartedAt.Before(approval.ApprovedAt) && !a.OverallDeadline.After(a.StartedAt.Add(managedLateOverallLimit)) && a.CredentialExpires.Equal(r.ResidualCleanup.CredentialExpires) && a.Deadline.Equal(managedLateResidualCleanupDeadline(a.StartedAt, a.OverallDeadline, a.CredentialExpires)) && !a.Deadline.Before(a.StartedAt.Add(managedLateFinalizerLimit)) && managedLateResidualCleanupEvidenceValid(p.NativeEvidence) && p.NativeEvidence["current-credential"] == managedLateResidualCredentialSHA(r.ResidualCleanup)
}
func newManagedLateResidualRemainingAuthority(r *createManagedLateRemaining, a createFoundationRecoveryArtifact, overall, now time.Time, rawSHA string) (*createManagedLateResidualCleanupAuthority, error) {
	bad := func() (*createManagedLateResidualCleanupAuthority, error) {
		return nil, createOpenBaoACKBlock("residual-remaining-fresh-authority")
	}
	if !managedLateResidualRemainingBaseValid(r) || a.Schema != "rtk-cloud.create-openbao-recovery-artifact/v1" || a.Plan.Schema != managedLateExpiredRenewalSchema || deploymentplan.Digest(a.Plan) != r.Review || a.Plan.ParentSHA != r.ParentSHA || a.Plan.ParentJournalSHA != r.ResidualRemaining.Approval.SourceSHA || a.Plan.ParentOwnerSHA != r.OwnerSHA || a.Plan.Materials[managedLateRemainingMaterial+"residual-remaining"] != deploymentplan.Digest(r.ResidualRemaining) || a.Plan.Materials[managedLateRemainingMaterial+"residual-remaining-policy"] != managedLateResidualRemainingPolicySHA() || a.QualifiedAt.IsZero() || a.QualifiedAt.After(now) || !now.Before(a.ValidUntil) || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.Qualification.Decision != "GO" || a.Qualification.PlanSHA256 != r.Review || len(a.Qualification.Gates) != 6 {
		return bad()
	}
	for i, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		g := a.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != r.Review {
			return bad()
		}
	}
	p := r.ResidualRemaining
	ap := p.Approval
	result := &createManagedLateResidualCleanupAuthority{Schema: managedLateResidualRemainingSchema, ParentSHA: r.ParentSHA, SourceSHA: ap.SourceSHA, OwnerSHA: r.OwnerSHA, Review: r.Review, CheckerSHA: a.Plan.CheckerSHA, ArtifactSHA: deploymentplan.Digest(a), QualifiedBytesSHA: rawSHA, DescriptorSHA: managedLateRemainingDigest(r), ResidualSHA: deploymentplan.Digest(p), ProposalSHA: ap.ProposalSHA, ApprovalSHA: ap.ApprovalSHA, PriorAuthoritySHA: deploymentplan.Digest(r.ResidualAuthority), OriginalSourceSHA: ap.OriginalSourceSHA, CompletedSHA: ap.CompletedSHA, OldPostNativeAuthoritySHA: deploymentplan.Digest(r.PostNativeAuthority), PendingSHA: r.TerminalFinalizerCompletion.PendingSHA, SessionID: r.SessionID, Fingerprint: r.Fingerprint, StartedAt: now, OverallDeadline: overall, CredentialExpires: r.ResidualCleanup.CredentialExpires, Deadline: managedLateResidualCleanupDeadline(now, overall, r.ResidualCleanup.CredentialExpires)}
	if !managedLateResidualRemainingAuthorityMatches(r, result) {
		return bad()
	}
	return result, nil
}
func managedLateResidualRemainingPath(parent string, witness bool) string {
	name := "managed-late-residual-copy-cleanup-remaining-grant-authority.json"
	if witness {
		name = "managed-late-residual-copy-cleanup-remaining-grant-admission.json"
	}
	return filepath.Join("deployment/create", parent, "bootstrap/service", name)
}
func readManagedLateResidualRemainingPair(store secretStore, parent string) ([]byte, error) {
	return readManagedLateResidualPairAt(store, parent, true)
}
func writeManagedLateResidualRemainingPair(store secretStore, parent string, raw []byte) error {
	return writeManagedLateResidualPairAt(store, parent, raw, true)
}
func readManagedLateResidualRemainingAuthority(store secretStore, r *createManagedLateRemaining) (*createManagedLateResidualCleanupAuthority, error) {
	bad := func() (*createManagedLateResidualCleanupAuthority, error) {
		return nil, createOpenBaoACKBlock("residual-remaining-immutable-authority")
	}
	if r == nil || r.ResidualRemaining == nil {
		return bad()
	}
	approval, err := loadManagedLateResidualRemainingApproval(store, r.ParentSHA)
	if err != nil || deploymentplan.Digest(approval) != deploymentplan.Digest(r.ResidualRemaining.Approval) {
		return bad()
	}
	raw, err := readManagedLateResidualRemainingPair(store, r.ParentSHA)
	if err != nil {
		return bad()
	}
	if raw == nil {
		return nil, nil
	}
	var result createManagedLateResidualCleanupAuthority
	if decodeEnvironmentJSON(raw, &result) != nil || !bytes.Equal(raw, mustProviderRecoveryJSON(result)) || !managedLateResidualRemainingAuthorityMatches(r, &result) {
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
	expected, err := newManagedLateResidualRemainingAuthority(&copy, a, result.OverallDeadline, result.StartedAt, result.QualifiedBytesSHA)
	if err != nil || deploymentplan.Digest(expected) != deploymentplan.Digest(result) {
		return bad()
	}
	return &result, nil
}
func managedLateResidualRemainingSourceMatches(a, b *createManagedLateResidualRemaining) bool {
	if a == nil || b == nil {
		return false
	}
	x, y := *a, *b
	x.NativeEvidence, y.NativeEvidence = nil, nil
	return deploymentplan.Digest(x) == deploymentplan.Digest(y)
}
func loadManagedLateResidualRemainingDescriptor(store secretStore, r *createManagedLateRemaining) error {
	approval, err := loadManagedLateResidualRemainingApproval(store, r.ParentSHA)
	if err != nil {
		return err
	}
	if approval == nil {
		return nil
	}
	r.ResidualRemaining = &createManagedLateResidualRemaining{Approval: approval, OriginalAuthoritySHA: deploymentplan.Digest(r.ResidualAuthority)}
	if !managedLateResidualRemainingBaseValid(r) {
		return createOpenBaoACKBlock("residual-remaining-approved-source")
	}
	raw, err := readManagedLateResidualRemainingPair(store, r.ParentSHA)
	if err != nil {
		return err
	}
	if raw == nil {
		return nil
	}
	var header createManagedLateResidualCleanupAuthority
	var archived createManagedLateRemaining
	if decodeEnvironmentJSON(raw, &header) != nil || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", header.Review, managedLateRemainingFilename), &archived) != nil || !managedLateResidualRemainingSourceMatches(r.ResidualRemaining, archived.ResidualRemaining) {
		return createOpenBaoACKBlock("residual-remaining-archived-source")
	}
	r.ResidualRemaining = archived.ResidualRemaining
	r.ResidualRemainingAuthority, err = readManagedLateResidualRemainingAuthority(store, r)
	return err
}
func managedLateResidualRemainingObjectsValid(entries []environmentCreateJournalEntry, objects []createManagedLateResidualCleanupObject, start, deadline time.Time) bool {
	allowed := map[string]createManagedLateResidualCleanupObject{}
	for _, o := range objects {
		allowed["bootstrap-copy-retire-"+o.Namespace+"-"+o.SessionID] = o
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		o, ok := allowed[entry.Key]
		var receipt struct {
			ID     string `json:"resource_id"`
			UID    string `json:"uid"`
			Absent bool   `json:"absent"`
		}
		intent := deploymentplan.Object{"resource_id": o.ResourceID, "uid": o.UID, "session_id": o.SessionID}
		if !ok || seen[entry.Key] || entry.Phase != "bootstrap" || entry.State != "completed" || entry.StartedAt.IsZero() || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(time.Now().UTC()) || entry.IntentSHA256 != activeTransportJournalDigest(intent) || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt.ID != o.ResourceID || receipt.UID != o.UID || !receipt.Absent || entry.StartedAt.Before(start) || entry.CompletedAt.After(deadline) {
			return false
		}
		seen[entry.Key] = true
	}
	return true
}
func managedLateResidualRemainingProgressValid(j environmentCreateJournal, r *createManagedLateRemaining) bool {
	if !managedLateResidualRemainingBaseValid(r) || len(j.Entries) < 403 || len(j.Entries) > 424 || !j.CompletedAt.IsZero() {
		return false
	}
	a := r.ResidualRemaining.Approval
	prefix := j
	prefix.Entries = prefix.Entries[:403]
	if deploymentplan.Digest(prefix) != a.SourceSHA || deploymentplan.Digest(j.Entries[400:403]) != a.CompletedSHA || !managedLateResidualRemainingObjectsValid(j.Entries[400:403], a.Completed, r.ResidualAuthority.StartedAt, r.ResidualAuthority.Deadline) {
		return false
	}
	if len(j.Entries) == 403 {
		return true
	}
	if !managedLateResidualRemainingAuthorityMatches(r, r.ResidualRemainingAuthority) {
		return false
	}
	return managedLateResidualRemainingObjectsValid(j.Entries[403:], a.Objects, r.ResidualRemainingAuthority.StartedAt, r.ResidualRemainingAuthority.Deadline)
}
func managedLateResidualRemainingRetirementsComplete(j environmentCreateJournal, r *createManagedLateRemaining) bool {
	if len(j.Entries) < 424 {
		return false
	}
	copy := j
	copy.CompletedAt = time.Time{}
	if r == nil || r.FinalizationAuthority == nil || len(copy.KubernetesLeases) < r.FinalizationAuthority.SourceLeases {
		return false
	}
	copy.KubernetesLeases = copy.KubernetesLeases[:r.FinalizationAuthority.SourceLeases]
	copy.Entries = append([]environmentCreateJournalEntry(nil), j.Entries[:424]...)
	copy.Entries[395].State = "pending"
	copy.Entries[395].CompletedAt = time.Time{}
	copy.Entries[395].Receipt = nil
	return managedLateResidualRemainingProgressValid(copy, r) && len(copy.Entries[403:]) == len(r.ResidualRemaining.Approval.Objects)
}
func managedLateResidualRemainingCopyAllowed(r *createManagedLateRemaining, s environmentCreateServiceCeremony, ns string) bool {
	if !managedLateResidualRemainingBaseValid(r) {
		return false
	}
	for _, o := range r.ResidualRemaining.Approval.Objects {
		if o.ResourceID == "Secret/"+ns+"/"+s.BootstrapCredentialSecret && o.SessionID == s.BootstrapSessionID && o.Namespace == ns && o.UID == s.CredentialUIDs[ns] {
			return true
		}
	}
	return false
}
func verifyManagedLateResidualRemainingAuthorityCurrent(store secretStore, e *environmentCreateExecution, r *createManagedLateRemaining) error {
	a, err := readManagedLateResidualRemainingAuthority(store, r)
	if err == nil && a == nil && r.ResidualRemainingAuthority == nil && len(e.Journal.Entries) == 403 {
		return nil
	}
	if err != nil || a == nil || deploymentplan.Digest(a) != deploymentplan.Digest(r.ResidualRemainingAuthority) || !time.Now().Before(a.Deadline) {
		return createOpenBaoACKBlock("residual-remaining-current-authority")
	}
	return nil
}
func verifyManagedLateResidualRemainingRuntime(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime) error {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if err := verifyManagedLateResidualCleanupSource(ctx, cfg, store, e, r); err != nil {
		return err
	}
	remaining := r.ManagedLateRemaining
	a, err := readManagedLateResidualRemainingAuthority(store, remaining)
	budget, bErr := managedLateActiveBudget(ctx)
	if err != nil || a == nil || bErr != nil || deploymentplan.Digest(a) != deploymentplan.Digest(remaining.ResidualRemainingAuthority) || deploymentplan.Digest(a) != deploymentplan.Digest(budget.ResidualAuthority) || runtime.ExecutionSHA256 != remaining.ParentSHA || runtime.Backend.Store != store {
		return createOpenBaoACKBlock("residual-remaining-runtime-authority")
	}
	return nil
}
func admitManagedLateResidualRemaining(ctx context.Context, store secretStore, e *environmentCreateExecution, a createFoundationRecoveryArtifact, rawSHA string, r *createManagedLateRemaining, overall time.Time) (*createManagedLateRemainingAuthority, error) {
	if e == nil || e.Check(ctx) != nil || store != e.Store || !managedLateResidualRemainingBaseValid(r) || r.OwnerSHA != deploymentplan.Digest(e.owner) {
		return nil, createOpenBaoACKBlock("residual-remaining-admission-source")
	}
	prior, err := readManagedLateResidualRemainingAuthority(store, r)
	if err != nil {
		return nil, err
	}
	if prior != nil {
		r.ResidualRemainingAuthority = prior
		if !managedLateResidualRemainingProgressValid(e.Journal, r) || !time.Now().Before(prior.Deadline) || overall.Before(prior.Deadline) {
			return nil, createOpenBaoACKBlock("residual-remaining-fixed-budget")
		}
		return r.Authority, nil
	}
	if len(e.Journal.Entries) != 403 || !managedLateResidualRemainingProgressValid(e.Journal, r) {
		return nil, createOpenBaoACKBlock("residual-remaining-admission-source")
	}
	phase, err := newManagedLateResidualRemainingAuthority(r, a, overall, time.Now().UTC(), rawSHA)
	if err != nil {
		return nil, err
	}
	invalidateManagedLateSourceReuse(e)
	if err := writeManagedLateResidualRemainingPair(store, r.ParentSHA, mustProviderRecoveryJSON(phase)); err != nil {
		return nil, err
	}
	r.ResidualRemainingAuthority = phase
	return r.Authority, e.Check(ctx)
}
func beginManagedLateResidualRemainingBudget(ctx context.Context, materials map[string]string, r *createManagedLateRemaining) (context.Context, context.CancelFunc, error) {
	if ctx == nil || ctx.Err() != nil || ctx.Value(managedLateBudgetKey{}) != nil || !managedLateResidualRemainingAuthorityMatches(r, r.ResidualRemainingAuthority) || !time.Now().Before(r.ResidualRemainingAuthority.Deadline) {
		return nil, nil, createOpenBaoACKBlock("residual-remaining-fixed-budget")
	}
	if d, ok := ctx.Deadline(); !ok || d.Before(r.ResidualRemainingAuthority.Deadline) {
		return nil, nil, createOpenBaoACKBlock("residual-remaining-caller-budget")
	}
	for k, v := range managedLateBudgetMaterials() {
		if materials[k] != v {
			return nil, nil, createOpenBaoACKBlock("late-qualified-budget")
		}
	}
	if materials[managedLateRemainingMaterial+"residual-remaining-policy"] != managedLateResidualRemainingPolicySHA() {
		return nil, nil, createOpenBaoACKBlock("residual-remaining-policy")
	}
	a := r.ResidualRemainingAuthority
	overall, stop := context.WithDeadline(ctx, a.OverallDeadline)
	budget := managedLateRuntimeBudget{StartedAt: r.FinalizationAuthority.StartedAt, Deadline: a.Deadline, Overall: overall, Limit: managedLateResidualCleanupLimit, ResidualAuthority: a}
	phase, cancel := context.WithDeadline(context.WithValue(overall, managedLateBudgetKey{}, budget), a.Deadline)
	return phase, func() { cancel(); stop() }, nil
}

func managedLateResidualCompletedBudgetMatches(proof createManagedLateTerminalFinalizerCompleted, a *createManagedLateResidualCleanupAuthority) bool {
	if a == nil {
		return false
	}
	if a.Schema == managedLateResidualRemainingSchema {
		return proof.ResidualRemainingReview == a.Review && proof.ResidualRemainingAuthoritySHA == deploymentplan.Digest(a)
	}
	return proof.ResidualReview == a.Review && proof.ResidualAuthoritySHA == deploymentplan.Digest(a)
}
