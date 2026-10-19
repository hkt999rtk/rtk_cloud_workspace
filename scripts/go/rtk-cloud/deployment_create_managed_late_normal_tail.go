package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"time"
)

const managedLateNormalTailSchema = "rtk-cloud.create-managed-late-normal-tail/v1"
const managedLateNormalTailFilename = "managed-late-remaining-normal-tail.json"
const managedLateNormalTailMaterial = "accepted-service-core-late-normal-tail-"

type createManagedLateNormalTail struct {
	Supplement                                                                                                *createManagedLateNormalTailSupplement `json:"-"`
	KubeconfigSHA                                                                                             string                                 // Original raw selected file; CredentialSHA pins the frozen normalized backend bytes.
	CurrentSHA                                                                                                string
	CredentialMode, CredentialSHA                                                                             string
	CredentialExpires                                                                                         time.Time
	Schema, Review, SourceSHA, OwnerSHA, OriginalAuthoritySHA, CompletedProofSHA, PendingSHA, IdentityPlanSHA string
	Approval                                                                                                  *createManagedLateNormalTailApproval
	NativeEvidence                                                                                            map[string]string
	Authority                                                                                                 *createManagedLateNormalTailAuthority `json:"-"`
}
type createManagedLateNormalTailAuthority struct {
	CredentialExpires                                                                                                                                                                                                        time.Time
	Schema, ParentSHA, SourceSHA, OwnerSHA, Review, CheckerSHA, ArtifactSHA, QualifiedBytesSHA, DescriptorSHA, ProposalSHA, ApprovalSHA, OriginalAuthoritySHA, CompletedProofSHA, PendingSHA, IdentityPlanSHA, CredentialSHA string
	StartedAt, AdmittedAt, Deadline                                                                                                                                                                                          time.Time
}

func managedLateNormalTailDigest(n *createManagedLateNormalTail) string {
	if n == nil {
		return ""
	}
	c := *n
	c.Review = ""
	c.Authority = nil
	return deploymentplan.Digest(c)
}
func managedLateNormalTailPolicySHA() string {
	return deploymentplan.Digest(deploymentplan.Object{"schema": managedLateNormalTailSchema, "first-caller": managedLateOverallLimit.String(), "scope": "original-pending-ingress-and-normal-full-deployment-only", "admission": "paired-exclusive-witness-first", "historical": "all-old-clocks-and-completed24-immutable", "identity": "no-seed-export-helper-finalizer-or-cleanup-replay"})
}
func managedLateNormalTailSelected(j environmentCreateJournal) bool {
	if managedLateRedisPrePauseSelected(j) {
		return true
	}
	if len(j.Entries) != 433 && len(j.Entries) != 434 || !environmentPreflightDigest.MatchString(j.PlanSHA256) || !j.CompletedAt.IsZero() {
		return false
	}
	name := "retire-pki-bootstrap-custody-" + j.PlanSHA256[:12]
	keys := []string{"kubernetes/account-manager-registration", name, "kubernetes/" + name, environmentCreateIngressAdmissionEntry, "kubernetes/ingress-admission-identity"}
	for i, entry := range j.Entries[428:433] {
		if entry.Key != keys[i] || entry.StartedAt.IsZero() || !environmentPreflightDigest.MatchString(entry.IntentSHA256) {
			return false
		}
		if i == 3 && entry.State == "pending" {
			if entry.Phase != "ingress-admission" || !entry.CompletedAt.IsZero() || len(entry.Receipt) != 0 {
				return false
			}
		} else if i == 3 && len(j.Entries) == 433 {
			return false
		} else if entry.State != "completed" || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || len(entry.Receipt) == 0 {
			return false
		}
	}
	if len(j.Entries) == 434 {
		e := j.Entries[433]
		if e.Key != "kubernetes/ingress-controller" || e.Phase != "ingress-controller" || e.State != "completed" || e.StartedAt.IsZero() || e.CompletedAt.IsZero() || e.CompletedAt.Before(e.StartedAt) || len(e.Receipt) == 0 || j.Entries[431].State == "completed" && j.Entries[431].CompletedAt.Before(e.CompletedAt) {
			return false
		}
	}
	return true
}
func managedLateNormalTailControllerSelected(j environmentCreateJournal) bool {
	if managedLateRedisPrePauseSelected(j) {
		j = managedLateRedisPrePauseOriginal(j)
	}
	return len(j.Entries) == 434 && managedLateNormalTailSelected(j)
}
func managedLateNormalTailOriginalSource(j environmentCreateJournal) environmentCreateJournal {
	if managedLateRedisPrePauseSelected(j) {
		j = managedLateRedisPrePauseOriginal(j)
	}
	if !managedLateNormalTailSelected(j) {
		return environmentCreateJournal{}
	}
	c := j
	c.Entries = append([]environmentCreateJournalEntry(nil), j.Entries[:433]...)
	if len(j.Entries) == 434 {
		if len(j.KubernetesLeases) == 0 {
			return environmentCreateJournal{}
		}
		c.Entries[431].State = "pending"
		c.Entries[431].Receipt = nil
		c.Entries[431].CompletedAt = time.Time{}
		c.KubernetesLeases = append([]deploymentplan.LeaseOwner(nil), j.KubernetesLeases[:len(j.KubernetesLeases)-1]...)
	}
	return c
}

// Validate recorded native children and the exact original pending operation.
// This pure reader grants no current-fence or native authority.
func validateManagedLateNormalTailStages(store secretStore, e *environmentCreateExecution, r *createManagedLateRemaining) error {
	bad := func() error { return createOpenBaoACKBlock("normal-tail-exact433-stages") }
	if e == nil || r == nil || !managedLateNormalTailSelected(e.Journal) || r.FinalizationAuthority == nil {
		return bad()
	}
	historical, sourceErr := managedLateNormalTailHistoricalSource(store, e)
	if sourceErr != nil {
		return sourceErr
	}
	e = historical
	ns := e.Journal.Stack + "-video-cloud"
	id := "Deployment/" + ns + "/" + r.Ceremony.CustodyReader
	registration, regJournal, err := managedTransportPostAcceptedStage(store, e, e.Journal.Entries[428].Key)
	if err != nil || registration == nil {
		return bad()
	}
	account, secret := false, false
	for _, resource := range registration.Resources {
		if !environmentBootstrapOwnerUUID.MatchString(regJournal.Owners[resource.ID]) {
			return bad()
		}
		switch resource.ID {
		case "Deployment/" + e.Journal.Stack + "-account-manager/account-manager":
			account = resource.Action == "update" && resource.UID == regJournal.Owners[resource.ID]
		case "Secret/" + e.Journal.Stack + "-account-manager/account-manager-service-registration-tls":
			secret = resource.Action == "create" && resource.UID == ""
		default:
			if resource.Action != "retain" || resource.UID != regJournal.Owners[resource.ID] {
				return bad()
			}
		}
	}
	if !account || !secret {
		return bad()
	}
	retired, retireJournal, err := managedTransportPostAcceptedStage(store, e, e.Journal.Entries[430].Key)
	if err != nil || len(retired.Resources) != 1 || retired.Resources[0].ID != id || retired.Resources[0].Action != "retire" || !environmentBootstrapOwnerUUID.MatchString(retired.Resources[0].UID) || retireJournal.Owners[id] != retired.Resources[0].UID {
		return bad()
	}
	var absent struct {
		ID     string `json:"resource_id"`
		UID    string `json:"uid"`
		Absent bool   `json:"absent"`
	}
	outer := e.Journal.Entries[429]
	if decodeEnvironmentJSON(outer.Receipt, &absent) != nil || absent.ID != id || absent.UID != retired.Resources[0].UID || !absent.Absent || outer.IntentSHA256 != activeTransportJournalDigest(deploymentplan.Object{"resource_id": id, "uid": absent.UID}) {
		return bad()
	}
	identity, identityJournal, err := managedTransportPostAcceptedStage(store, e, e.Journal.Entries[432].Key)
	identityID := "Secret/" + e.Journal.Stack + "-ingress/" + environmentCreateIngressAdmissionSecret
	if err != nil || len(identity.Resources) != 1 || identity.Resources[0].ID != identityID || identity.Resources[0].Action != "create" || identity.Resources[0].UID != "" || identity.Resources[0].Baseline != nil || !environmentBootstrapOwnerUUID.MatchString(identityJournal.Owners[identityID]) {
		return bad()
	}
	base := r.FinalizationAuthority.SourceLeases + 2
	if len(e.Journal.KubernetesLeases) != base+3 && !(managedLateNormalTailControllerSelected(e.Journal) && len(e.Journal.KubernetesLeases) == base+4) {
		return bad()
	}
	uids := map[string]bool{}
	for i, p := range []*deploymentplan.Plan{registration, retired, identity} {
		lease := e.Journal.KubernetesLeases[base+i]
		if !managedLateLeaseMatches(lease, e.Journal.Stack, p.SHA256()) || uids[lease.UID] {
			return bad()
		}
		uids[lease.UID] = true
	}
	if managedLateNormalTailControllerSelected(e.Journal) {
		controller, _, controllerErr := managedTransportPostAcceptedStage(store, e, e.Journal.Entries[433].Key)
		if controllerErr != nil || controller == nil || !managedLateLeaseMatches(e.Journal.KubernetesLeases[base+3], e.Journal.Stack, controller.SHA256()) || uids[e.Journal.KubernetesLeases[base+3].UID] {
			return bad()
		}
	}
	return nil
}
func verifyManagedLateNormalTailSource(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery) error {
	if r == nil || r.normalTail == nil || r.completedTail == nil || cfg.Environment != store.Environment {
		return createOpenBaoACKBlock("normal-tail-profile")
	}
	return verifyManagedLateNormalTailFrame(ctx, store, e, r.completedTail, r.normalTail)
}
func verifyManagedLateNormalTailFrame(ctx context.Context, store secretStore, e *environmentCreateExecution, t *createManagedLateCompletedTail, n *createManagedLateNormalTail) error {
	bad := func() error { return createOpenBaoACKBlock("normal-tail-current-source") }
	if e == nil || t == nil || n == nil || n.Approval == nil || e.Check(ctx) != nil || store != e.Store || !managedLateNormalTailSelected(e.Journal) || n.CurrentSHA != deploymentplan.Digest(e.Journal) || n.SourceSHA != deploymentplan.Digest(managedLateNormalTailOriginalSource(e.Journal)) || n.OwnerSHA != deploymentplan.Digest(e.owner) || n.Schema != managedLateNormalTailSchema {
		return bad()
	}
	historical, sourceErr := managedLateNormalTailHistoricalSource(store, e)
	if sourceErr != nil {
		return sourceErr
	}
	approval, err := loadManagedLateNormalTailApproval(store, e.Journal.PlanSHA256)
	if err != nil || deploymentplan.Digest(approval) != deploymentplan.Digest(n.Approval) {
		return bad()
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, managedLateTerminalFinalizerCompletedPath(e.Journal.PlanSHA256)), true)
	if err != nil || environmentPreflightBytesSHA256(raw) != n.CompletedProofSHA || n.CompletedProofSHA != t.ProofSHA || n.OriginalAuthoritySHA != t.AuthoritySHA || n.PendingSHA != deploymentplan.Digest(managedLateNormalTailOriginalSource(e.Journal).Entries[431]) || validateManagedLateTerminalFinalizerCompletion(store, historical, &createManagedLateSessionRecovery{Review: t.LateReview}) != nil {
		return bad()
	}
	if n.Authority != nil {
		a, err := readManagedLateNormalTailCurrentAuthority(store, n)
		effective := &createServiceSessionRecovery{normalTail: n, normalSupplement: n.Supplement}
		if n.Supplement != nil {
			approval, approvalErr := loadManagedLateNormalTailSupplementApproval(store, e.Journal.PlanSHA256)
			if approvalErr != nil || deploymentplan.Digest(approval) != deploymentplan.Digest(n.Supplement.Approval) {
				return bad()
			}
			if x := n.Supplement.Extension; x != nil {
				approval, approvalErr := loadManagedLateNormalTailExtensionApproval(store, e.Journal.PlanSHA256)
				if approvalErr != nil || deploymentplan.Digest(approval) != deploymentplan.Digest(x.Approval) {
					return bad()
				}
				if x.Authority != nil {
					if verifyManagedLateNormalTailExtensionRuntime(store, effective) != nil {
						return bad()
					}
				} else if raw, pairErr := readManagedLateNormalTailExtensionPair(store, e.Journal.PlanSHA256); pairErr != nil || raw != nil {
					return bad()
				}
			} else if n.Supplement.Authority != nil {
				if verifyManagedLateNormalTailSupplementRuntime(store, effective) != nil {
					return bad()
				}
			} else if raw, pairErr := readManagedLateNormalTailSupplementPair(store, e.Journal.PlanSHA256); pairErr != nil || raw != nil {
				return bad()
			}
		}
		if err != nil || a == nil || deploymentplan.Digest(a) != deploymentplan.Digest(n.Authority) || !time.Now().Before(managedLateNormalTailEffectiveDeadline(effective)) {
			return bad()
		}
	}
	return e.Check(ctx)
}
func verifyManagedLateNormalTailRuntime(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime) error {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if r == nil || r.normalTail == nil || r.normalTail.Authority == nil || e == nil || runtime.ExecutionSHA256 != e.Journal.PlanSHA256 || runtime.Backend.Store != store {
		return createOpenBaoACKBlock("normal-tail-admitted-runtime")
	}
	if err := verifyManagedLateNormalTailSource(ctx, cfg, store, e, r); err != nil {
		return err
	}
	if err := verifyManagedLateNormalTailSupplementRuntime(store, r); err != nil {
		return err
	}
	return e.Check(ctx)
}
func managedLateNormalTailPath(parent string, witness bool) string {
	name := "managed-late-normal-tail-authority.json"
	if witness {
		name = "managed-late-normal-tail-admission.json"
	}
	return filepath.Join("deployment/create", parent, "bootstrap/service", name)
}
func readManagedLateNormalTailPair(store secretStore, parent string) ([]byte, error) {
	bad := func() ([]byte, error) { return nil, createOpenBaoACKBlock("normal-tail-authority-pair") }
	if !environmentPreflightDigest.MatchString(parent) {
		return bad()
	}
	paths := []string{managedLateNormalTailPath(parent, false), managedLateNormalTailPath(parent, true)}
	absent := 0
	for i, relative := range paths {
		p, err := privateDeploymentStatePath(store.Environment, relative)
		if err != nil || p != filepath.Join(store.Root, relative) || validateEnvironmentPreflightDirectories(store, filepath.Dir(p)) != nil {
			return bad()
		}
		paths[i] = p
		if _, err := os.Lstat(p); errors.Is(err, os.ErrNotExist) {
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
	a, ae := readBoundedEnvironmentFile(paths[0], true)
	b, be := readBoundedEnvironmentFile(paths[1], true)
	if ae != nil || be != nil || len(a) == 0 || !bytes.Equal(a, b) {
		return bad()
	}
	return a, nil
}
func writeManagedLateNormalTailPair(store secretStore, parent string, raw []byte) error {
	previous, err := readManagedLateNormalTailPair(store, parent)
	if err != nil || len(previous) != 0 || len(raw) == 0 {
		return createOpenBaoACKBlock("normal-tail-authority-exists")
	}
	for _, relative := range []string{managedLateNormalTailPath(parent, true), managedLateNormalTailPath(parent, false)} {
		path, err := privateDeploymentStatePath(store.Environment, relative)
		if err != nil || path != filepath.Join(store.Root, relative) || writeExclusivePrivateDeploymentJSON(path, raw) != nil || syncEnvironmentPreflightDirectory(filepath.Dir(path)) != nil {
			return createOpenBaoACKBlock("normal-tail-exclusive-authority")
		}
	}
	observed, err := readManagedLateNormalTailPair(store, parent)
	if err != nil || !bytes.Equal(raw, observed) {
		return createOpenBaoACKBlock("normal-tail-authority-readback")
	}
	return nil
}
func managedLateNormalTailCredentialSHA(n *createManagedLateNormalTail) string {
	if n == nil {
		return ""
	}
	return managedLateResidualCredentialSHA(&createManagedLateResidualCleanup{CredentialMode: n.CredentialMode, CredentialSHA: n.CredentialSHA, CredentialExpires: n.CredentialExpires})
}

func managedLateNormalTailAuthorityMatches(n *createManagedLateNormalTail, a *createManagedLateNormalTailAuthority) bool {
	if n == nil || n.Approval == nil || n.Approval.FirstCallerLimit != managedLateOverallLimit || a == nil || a.Schema != managedLateNormalTailSchema || a.SourceSHA != n.SourceSHA || a.OwnerSHA != n.OwnerSHA || a.DescriptorSHA != managedLateNormalTailDigest(n) || a.ParentSHA != n.Approval.ParentSHA || a.ProposalSHA != n.Approval.ProposalSHA || a.ApprovalSHA != n.Approval.ApprovalSHA || a.OriginalAuthoritySHA != n.OriginalAuthoritySHA || a.CompletedProofSHA != n.CompletedProofSHA || a.PendingSHA != n.PendingSHA || a.IdentityPlanSHA != n.IdentityPlanSHA || a.StartedAt.IsZero() || a.AdmittedAt.Before(a.StartedAt) || a.StartedAt.Before(n.Approval.ApprovedAt) || !a.Deadline.Equal(a.StartedAt.Add(managedLateOverallLimit)) || !a.AdmittedAt.Before(a.Deadline) || !managedLateResidualCleanupEvidenceValid(n.NativeEvidence) || a.CredentialSHA != n.NativeEvidence["current-credential"] || a.CredentialSHA != managedLateNormalTailCredentialSHA(n) || !a.CredentialExpires.Equal(n.CredentialExpires) || !n.CredentialExpires.IsZero() && n.CredentialExpires.Before(a.Deadline.Add(managedLateExpiryReserve)) {
		return false
	}
	for _, sha := range []string{n.KubeconfigSHA, a.ParentSHA, a.SourceSHA, a.OwnerSHA, a.Review, a.CheckerSHA, a.ArtifactSHA, a.QualifiedBytesSHA, a.DescriptorSHA, a.ProposalSHA, a.ApprovalSHA, a.OriginalAuthoritySHA, a.CompletedProofSHA, a.PendingSHA, a.IdentityPlanSHA, a.CredentialSHA} {
		if !environmentPreflightDigest.MatchString(sha) {
			return false
		}
	}
	return true
}
func newManagedLateNormalTailAuthority(n *createManagedLateNormalTail, a createFoundationRecoveryArtifact, rawSHA string, started, now time.Time) (*createManagedLateNormalTailAuthority, error) {
	bad := func() (*createManagedLateNormalTailAuthority, error) {
		return nil, createOpenBaoACKBlock("normal-tail-fresh-six-go-authority")
	}
	if n == nil || n.Approval == nil || n.Review != deploymentplan.Digest(a.Plan) || a.Plan.Schema != managedLateNormalTailSchema || a.Plan.ParentSHA != n.Approval.ParentSHA || a.Plan.ParentOwnerSHA != n.OwnerSHA || a.Plan.ParentJournalSHA != n.CurrentSHA || !environmentPreflightDigest.MatchString(n.KubeconfigSHA) || a.Plan.KubeconfigSHA != n.KubeconfigSHA || a.Plan.Materials[managedLateNormalTailMaterial+"source"] != managedLateNormalTailDigest(n) || a.Plan.Materials[managedLateNormalTailMaterial+"policy"] != managedLateNormalTailPolicySHA() || a.Schema != "rtk-cloud.create-openbao-recovery-artifact/v1" || a.QualifiedAt.IsZero() || a.QualifiedAt.After(now) || !now.Before(a.ValidUntil) || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.Qualification.Decision != "GO" || a.Qualification.PlanSHA256 != n.Review || len(a.Qualification.Gates) != 6 {
		return bad()
	}
	for i, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		g := a.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != n.Review {
			return bad()
		}
	}
	result := &createManagedLateNormalTailAuthority{Schema: managedLateNormalTailSchema, ParentSHA: n.Approval.ParentSHA, SourceSHA: n.SourceSHA, OwnerSHA: n.OwnerSHA, Review: n.Review, CheckerSHA: a.Plan.CheckerSHA, ArtifactSHA: deploymentplan.Digest(a), QualifiedBytesSHA: rawSHA, DescriptorSHA: managedLateNormalTailDigest(n), ProposalSHA: n.Approval.ProposalSHA, ApprovalSHA: n.Approval.ApprovalSHA, OriginalAuthoritySHA: n.OriginalAuthoritySHA, CompletedProofSHA: n.CompletedProofSHA, PendingSHA: n.PendingSHA, IdentityPlanSHA: n.IdentityPlanSHA, CredentialSHA: n.NativeEvidence["current-credential"], StartedAt: started, AdmittedAt: now, Deadline: started.Add(managedLateOverallLimit), CredentialExpires: n.CredentialExpires}
	if !managedLateNormalTailAuthorityMatches(n, result) || result.Deadline.Sub(now) < managedLateFinalizerLimit {
		return bad()
	}
	return result, nil
}
func readManagedLateNormalTailAuthority(store secretStore, n *createManagedLateNormalTail) (*createManagedLateNormalTailAuthority, error) {
	if n == nil || n.Approval == nil {
		return nil, createOpenBaoACKBlock("normal-tail-authority-source")
	}
	raw, err := readManagedLateNormalTailPair(store, n.Approval.ParentSHA)
	if err != nil || raw == nil {
		return nil, err
	}
	var result createManagedLateNormalTailAuthority
	if decodeEnvironmentJSON(raw, &result) != nil || !bytes.Equal(raw, mustProviderRecoveryJSON(result)) || !managedLateNormalTailAuthorityMatches(n, &result) {
		return nil, createOpenBaoACKBlock("normal-tail-immutable-authority")
	}
	qraw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment/create/openbao-recovery", result.Review, "qualification.json"), true)
	var a createFoundationRecoveryArtifact
	if err != nil || environmentPreflightBytesSHA256(qraw) != result.QualifiedBytesSHA || decodeEnvironmentJSON(qraw, &a) != nil || deploymentplan.Digest(a) != result.ArtifactSHA {
		return nil, createOpenBaoACKBlock("normal-tail-original-go")
	}
	copy := *n
	copy.Review = result.Review
	expected, err := newManagedLateNormalTailAuthority(&copy, a, result.QualifiedBytesSHA, result.StartedAt, result.AdmittedAt)
	if err != nil || deploymentplan.Digest(expected) != deploymentplan.Digest(result) {
		return nil, createOpenBaoACKBlock("normal-tail-original-first-clock")
	}
	return &result, nil
}
func admitManagedLateNormalTail(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, a createFoundationRecoveryArtifact, rawSHA string, started time.Time) (context.Context, context.CancelFunc, error) {
	if err := admitManagedLateCompletedTail(ctx, cfg, store, e, r, a, rawSHA); err != nil {
		return nil, nil, err
	}
	n := r.normalTail
	if n == nil {
		return nil, nil, createOpenBaoACKBlock("normal-tail-profile")
	}
	if r.normalSupplement != nil {
		var bounded context.Context
		var cancel context.CancelFunc
		var err error
		if r.normalSupplement.Extension != nil {
			bounded, cancel, err = admitManagedLateNormalTailExtension(ctx, store, e, r, a, rawSHA)
		} else {
			bounded, cancel, err = admitManagedLateNormalTailSupplement(ctx, store, e, r, a, rawSHA)
		}
		if err != nil {
			return nil, nil, err
		}
		if err := refreshManagedLateNormalTailHistory(bounded, cfg, store, e, r, loadManagedLateCompletedTailSource); err != nil {
			cancel()
			return nil, nil, err
		}
		return bounded, cancel, e.Check(bounded)
	}
	existing, err := readManagedLateNormalTailCurrentAuthority(store, n)
	if err != nil {
		return nil, nil, err
	}
	if existing == nil {
		phase, err := newManagedLateNormalTailAuthority(n, a, rawSHA, started, time.Now().UTC())
		if err != nil {
			return nil, nil, err
		}
		if expiry := r.ManagedLateRemaining.ResidualCleanup.CredentialExpires; !expiry.IsZero() && expiry.Before(phase.Deadline.Add(managedLateExpiryReserve)) {
			return nil, nil, createOpenBaoACKBlock("normal-tail-current-credential-reserve")
		}
		if err := publishManagedLateNormalTailAuthority(ctx, e, phase); err != nil {
			return nil, nil, err
		}
		existing = phase
		fresh, freshErr := loadManagedLateCompletedTailSource(ctx, cfg, store, e)
		if freshErr != nil {
			return nil, nil, freshErr
		}
		r.completedTail.view = fresh.completedTail.view
	}
	if !time.Now().Before(existing.Deadline) {
		return nil, nil, createOpenBaoACKBlock("normal-tail-fixed-first-deadline")
	}
	n.Authority = existing
	bounded, cancel := context.WithDeadline(ctx, existing.Deadline)
	return bounded, cancel, e.Check(bounded)
}

// A newly published pair invalidates the former command-local history scope.
// Derive and authenticate a new scope once; retain only its proved parsing view.
func refreshManagedLateNormalTailHistory(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, load func(context.Context, deploymentConfig, secretStore, *environmentCreateExecution) (*createServiceSessionRecovery, error)) error {
	bad := func() error { return createOpenBaoACKBlock("normal-tail-refreshed-history") }
	if e == nil || r == nil || r.normalTail == nil || r.completedTail == nil || load == nil || store != e.Store || cfg.Environment != store.Environment || r.completedTail.SourceSHA != deploymentplan.Digest(e.Journal) || r.completedTail.OwnerSHA != deploymentplan.Digest(e.owner) || e.Check(ctx) != nil {
		return bad()
	}
	view := r.completedTail.view
	if managedLateSourceReuseActive(e) && view != nil && view.Store == store && deploymentplan.Digest(view.Journal) == r.completedTail.ViewSHA && view.lateSourceReuse == e.lateSourceReuse {
		return checkManagedLateSourceReuse(e)
	}
	prior, invalid := e.lateSourceReuse, !managedLateSourceReuseActive(e)
	fresh, err := load(ctx, cfg, store, e)
	if err != nil {
		invalidateManagedLateSourceReuse(e)
		return err
	}
	if fresh == nil || fresh.completedTail == nil || fresh.completedTail.view == nil {
		invalidateManagedLateSourceReuse(e)
		return bad()
	}
	expected, proved := *r.completedTail, *fresh.completedTail
	expected.NativeEvidence, proved.NativeEvidence = nil, nil
	view = fresh.completedTail.view
	if deploymentplan.Digest(expected) != deploymentplan.Digest(proved) || deploymentplan.Digest(view.Journal) != expected.ViewSHA || view.Store != store || !managedLateSourceReuseActive(e) || view.lateSourceReuse != e.lateSourceReuse || invalid && prior != nil && prior == e.lateSourceReuse {
		invalidateManagedLateSourceReuse(e)
		return bad()
	}
	if err := checkManagedLateSourceReuse(e); err != nil {
		return err
	}
	if err := e.Check(ctx); err != nil {
		invalidateManagedLateSourceReuse(e)
		return err
	}
	r.completedTail.view = view
	return nil
}

// Keep the exact original pending432/accepted433 while other normal stages run.
func managedLateNormalTailContext(ctx context.Context, runtime environmentCreateBootstrapRuntime) (context.Context, error) {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if r == nil || r.normalTail == nil || r.normalTail.Authority == nil || e == nil || e.Check(ctx) != nil || deploymentplan.Digest(e.owner) != r.normalTail.OwnerSHA {
		return nil, createOpenBaoACKBlock("normal-tail-context-source")
	}
	a, err := readManagedLateNormalTailCurrentAuthority(e.Store, r.normalTail)
	if err != nil || a == nil || deploymentplan.Digest(a) != deploymentplan.Digest(r.normalTail.Authority) || !time.Now().Before(managedLateNormalTailEffectiveDeadline(r)) || verifyManagedLateNormalTailSupplementRuntime(e.Store, r) != nil || validateManagedLateTerminalFinalizerCompletion(e.Store, e, r.ManagedLateSession) != nil {
		return nil, createOpenBaoACKBlock("normal-tail-context-deadline")
	}
	bounded, cancel := context.WithDeadline(ctx, managedLateNormalTailEffectiveDeadline(r))
	context.AfterFunc(ctx, cancel)
	if err := e.Check(bounded); err != nil {
		return nil, err
	}
	return bounded, nil
}

// Retry may independently requalify current native evidence, while the first
// authority and its original qualification remain immutable clock authority.
func readManagedLateNormalTailCurrentAuthority(store secretStore, n *createManagedLateNormalTail) (*createManagedLateNormalTailAuthority, error) {
	if n == nil || n.Approval == nil {
		return nil, createOpenBaoACKBlock("normal-tail-authority-source")
	}
	raw, err := readManagedLateNormalTailPair(store, n.Approval.ParentSHA)
	if err != nil || raw == nil {
		return nil, err
	}
	var header createManagedLateNormalTailAuthority
	var original createManagedLateNormalTail
	if decodeEnvironmentJSON(raw, &header) != nil || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", header.Review, managedLateNormalTailFilename), &original) != nil {
		return nil, createOpenBaoACKBlock("normal-tail-first-descriptor")
	}
	old, current := original, *n
	old.NativeEvidence, current.NativeEvidence = nil, nil
	old.CurrentSHA, current.CurrentSHA = "", ""
	if managedLateNormalTailDigest(&old) != managedLateNormalTailDigest(&current) {
		return nil, createOpenBaoACKBlock("normal-tail-unchanged-approved-source")
	}
	return readManagedLateNormalTailAuthority(store, &original)
}

// The caller has already authenticated the fresh sixGO and exact source. This
// local publication ends that immutable phase before any exclusive file write.
func publishManagedLateNormalTailAuthority(ctx context.Context, e *environmentCreateExecution, a *createManagedLateNormalTailAuthority) error {
	if e == nil || a == nil || e.Check(ctx) != nil || a.Schema != managedLateNormalTailSchema || a.ParentSHA != e.Journal.PlanSHA256 || a.OwnerSHA != deploymentplan.Digest(e.owner) || a.SourceSHA != deploymentplan.Digest(e.Journal) {
		return createOpenBaoACKBlock("normal-tail-publication-source")
	}
	invalidateManagedLateSourceReuse(e)
	return writeManagedLateNormalTailPair(e.Store, e.Journal.PlanSHA256, mustProviderRecoveryJSON(a))
}

// This is only a parsing view of already authenticated434 ancestry. Actual
// native reads, owner checks and writes retain the original current executor.
func managedLateNormalTailHistoricalSource(store secretStore, e *environmentCreateExecution) (*environmentCreateExecution, error) {
	if e == nil {
		return nil, createOpenBaoACKBlock("normal-tail-historical-source")
	}
	if !managedLateRedisPrePauseSelected(e.Journal) {
		if len(e.Journal.Entries) > 434 {
			return nil, createOpenBaoACKBlock("normal-tail-historical-source")
		}
		return e, nil
	}
	if e.Store != store {
		return nil, createOpenBaoACKBlock("normal-tail-historical-source")
	}
	if _, _, err := readManagedLateRedisPrePauseSource(store, e); err != nil {
		return nil, err
	}
	view := *e
	view.Journal = managedLateRedisPrePauseOriginal(e.Journal)
	view.volumeExecution = e
	return &view, nil
}
