package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"testing"
	"time"
)

func normalTailExtensionFixture(t *testing.T) (secretStore, *environmentCreateExecution, *createServiceSessionRecovery, createFoundationRecoveryArtifact, managedLateNormalTailExtensionProposal) {
	t.Helper()
	store, e, r, baseq, _ := normalTailSupplementFixtureAt(t, 220*time.Minute)
	now := time.Now().UTC()
	baseAt := r.normalTail.Authority.StartedAt.Add(120 * time.Minute)
	baseq.QualifiedAt = baseAt.Add(-time.Minute)
	baseq.ValidUntil = baseq.QualifiedAt.Add(15 * time.Minute)
	resealNormalTailSupplementFixture(r, &baseq)
	braw := mustProviderRecoveryJSON(baseq)
	ba, err := newManagedLateNormalTailSupplementAuthority(r, baseq, environmentPreflightBytesSHA256(braw), r.normalTail.Authority.StartedAt.Add(180*time.Minute), baseAt)
	if err != nil {
		t.Fatal("historical180 constructor", err)
	}
	base := filepath.Join("deployment/create/openbao-recovery", ba.Review)
	for name, value := range map[string]any{"qualification.json": baseq, managedLateNormalTailFilename: r.normalTail, managedLateNormalTailSupplementFilename: r.normalSupplement} {
		if err := store.write(filepath.Join(base, name), mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeManagedLateNormalTailSupplementPair(store, e.Journal.PlanSHA256, mustProviderRecoveryJSON(ba)); err != nil {
		t.Fatal(err)
	}
	r.normalSupplement.Authority = ba
	if !ba.Deadline.Before(now) {
		t.Fatal("fixture180 must be historical expired")
	}
	no := false
	sraw := mustProviderRecoveryJSON(e.Journal)
	p := managedLateNormalTailExtensionProposal{Schema: "rtk-cloud.remaining-normal-tail-timeout-extension-proposal/v1", Environment: "dev", Parent: e.Journal.PlanSHA256, SourcePath: managedLateNormalTailExtensionSourcePath, SourceRawSHA: environmentPreflightBytesSHA256(sraw), OwnerPath: managedLateNormalTailExtensionOwnerPath, OwnerSHA: deploymentplan.Digest(e.owner), SourceEntries: 434, SourceLeases: 149, OriginalAuthorityRawSHA: ba.OriginalAuthorityRawSHA, BaseAuthoritySHA: deploymentplan.Digest(ba), BaseAuthorityRawSHA: environmentPreflightBytesSHA256(mustProviderRecoveryJSON(ba)), BaseReview: ba.Review, BaseQualifiedBytesSHA: ba.QualifiedBytesSHA, CompletedProofSHA: r.normalSupplement.Approval.CompletedProofSHA, PreparedCheckerSHA: strings.Repeat("7", 64), OriginalStartedAt: ba.OriginalStartedAt, BaseDeadline: ba.Deadline, ExtendedDeadline: ba.OriginalStartedAt.Add(360 * time.Minute), TotalMinutes: 360, FirstReserveMinutes: 15, CredentialReserveMinutes: 5, ExecutionAuthority: &no, Status: "proposal-awaiting-explicit-approval", RecordedAt: now.Add(-time.Second), Allowed: managedLateNormalTailSupplementAllowed(), Forbidden: managedLateNormalTailSupplementForbidden(), Completion: managedLateNormalTailSupplementCompletion()}
	praw := mustProviderRecoveryJSON(p)
	approval := managedLateNormalTailExtensionApprovalRecord{Schema: "rtk-cloud.remaining-normal-tail-timeout-extension-operator-approval/v1", Environment: "dev", Parent: p.Parent, ProposalSHA: environmentPreflightBytesSHA256(praw), SourceRawSHA: p.SourceRawSHA, OriginalAuthorityRawSHA: p.OriginalAuthorityRawSHA, BaseAuthorityRawSHA: p.BaseAuthorityRawSHA, TotalMinutes: 360, Approved: true, ExecutionAuthority: &no, RecordedAt: now.Add(-500 * time.Millisecond), HumanMessage: "approve this distinct TOTAL360 proposal"}
	for path, raw := range map[string][]byte{managedLateNormalTailExtensionProposalPath: praw, managedLateNormalTailExtensionApprovalPath: mustProviderRecoveryJSON(approval), managedLateNormalTailExtensionSourcePath: sraw, managedLateNormalTailExtensionOwnerPath: mustProviderRecoveryJSON(e.owner)} {
		if err := store.write(path, raw, false); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := loadManagedLateNormalTailExtensionApproval(store, p.Parent)
	if err != nil {
		t.Fatal("valid360 approval after expired180", err)
	}
	r.normalSupplement.Extension = &createManagedLateNormalTailExtension{Approval: loaded, CredentialSHA: managedLateNormalTailCredentialSHA(r.normalTail), CredentialExpires: r.normalTail.CredentialExpires}
	a := baseq
	a.Plan.Materials = map[string]string{}
	for k, v := range baseq.Plan.Materials {
		a.Plan.Materials[k] = v
	}
	a.Qualification.Gates = slices.Clone(baseq.Qualification.Gates)
	a.QualifiedAt = now.Add(-time.Minute)
	a.ValidUntil = a.QualifiedAt.Add(15 * time.Minute)
	resealNormalTailExtensionFixture(r, &a)
	return store, e, r, a, p
}
func resealNormalTailExtensionFixture(r *createServiceSessionRecovery, a *createFoundationRecoveryArtifact) {
	a.Plan.Materials[managedLateNormalTailExtensionMaterial+"intent"] = managedLateNormalTailExtensionIntent(r)
	a.Plan.Materials[managedLateNormalTailExtensionMaterial+"policy"] = managedLateNormalTailExtensionPolicySHA()
	resealNormalTailSupplementFixture(r, a)
}
func saveNormalTailExtensionQualification(t *testing.T, store secretStore, r *createServiceSessionRecovery, a createFoundationRecoveryArtifact) {
	t.Helper()
	base := filepath.Join("deployment/create/openbao-recovery", a.Qualification.PlanSHA256)
	for name, value := range map[string]any{"qualification.json": a, managedLateNormalTailFilename: r.normalTail, managedLateNormalTailSupplementFilename: r.normalSupplement, managedLateNormalTailExtensionFilename: r.normalSupplement.Extension} {
		if err := store.write(filepath.Join(base, name), mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
}
func TestManagedLateNormalTailExtensionAnchorsExpired180AndPreservesBothPairs(t *testing.T) {
	store, e, r, a, p := normalTailExtensionFixture(t)
	before90, _ := readManagedLateNormalTailPair(store, p.Parent)
	before180, _ := readManagedLateNormalTailSupplementPair(store, p.Parent)
	now := time.Now().UTC()
	phase, err := newManagedLateNormalTailExtensionAuthority(r, a, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a)), now.Add(360*time.Minute), now)
	if err != nil || !phase.Deadline.Equal(p.OriginalStartedAt.Add(360*time.Minute)) || !phase.BaseDeadline.Equal(p.BaseDeadline) {
		t.Fatal("fixed original anchor changed", err)
	}
	if !managedLateNormalTailExtensionPauseMatches(phase, phase.Deadline) {
		t.Fatal("typed360 authority invalid")
	}
	ctx, cancel, err := managedLateNormalTailSupplementCallerContext(context.Background(), store, e, 360*time.Minute, true)
	if err != nil {
		t.Fatal("explicit360 source caller refused", err)
	}
	defer cancel()
	end, _ := ctx.Deadline()
	if !end.Equal(p.ExtendedDeadline) {
		t.Fatal("caller reset epoch")
	}
	saveNormalTailExtensionQualification(t, store, r, a)
	if err := writeManagedLateNormalTailExtensionPair(store, p.Parent, mustProviderRecoveryJSON(phase)); err != nil {
		t.Fatal(err)
	}
	got, err := readManagedLateNormalTailExtensionAuthority(store, p.Parent)
	if err != nil || deploymentplan.Digest(got) != deploymentplan.Digest(phase) {
		t.Fatal("durable360 restore", err)
	}
	if writeManagedLateNormalTailExtensionPair(store, p.Parent, mustProviderRecoveryJSON(phase)) == nil {
		t.Fatal("new phase reset")
	}
	after90, _ := readManagedLateNormalTailPair(store, p.Parent)
	after180, _ := readManagedLateNormalTailSupplementPair(store, p.Parent)
	if !bytes.Equal(before90, after90) || !bytes.Equal(before180, after180) {
		t.Fatal("old90/180 pair modified")
	}
}
func TestManagedLateNormalTailExtensionQualificationAndClockRefuseDrift(t *testing.T) {
	_, _, r, a, _ := normalTailExtensionFixture(t)
	now := time.Now().UTC()
	for _, mode := range []string{"base", "base-review", "base-source", "original", "origin-reset", "owner", "source", "raw-go", "failed-gate", "expired-go", "future-approval", "credential-evidence", "unknown-mode", "short-caller"} {
		t.Run(mode, func(t *testing.T) {
			n := *r.normalTail
			n.NativeEvidence = map[string]string{}
			for k, v := range r.normalTail.NativeEvidence {
				n.NativeEvidence[k] = v
			}
			s := *r.normalSupplement
			x := *s.Extension
			p := *x.Approval
			x.Approval = &p
			s.Extension = &x
			ba := *s.Authority
			s.Authority = &ba
			q := a
			q.Plan.Materials = map[string]string{}
			for k, v := range a.Plan.Materials {
				q.Plan.Materials[k] = v
			}
			q.Qualification.Gates = slices.Clone(a.Qualification.Gates)
			runtime := &createServiceSessionRecovery{normalTail: &n, normalSupplement: &s}
			raw := strings.Repeat("9", 64)
			caller := now.Add(360 * time.Minute)
			switch mode {
			case "base":
				p.BaseAuthoritySHA = strings.Repeat("f", 64)
			case "base-review":
				p.BaseReview = strings.Repeat("f", 64)
			case "base-source":
				p.SourceSHA = strings.Repeat("f", 64)
				q.Plan.ParentJournalSHA = p.SourceSHA
			case "original":
				p.OriginalAuthoritySHA = strings.Repeat("f", 64)
			case "origin-reset":
				p.OriginalStartedAt = now
				p.BaseDeadline = now.Add(180 * time.Minute)
				p.ExtendedDeadline = now.Add(360 * time.Minute)
			case "owner":
				q.Plan.ParentOwnerSHA = strings.Repeat("f", 64)
			case "source":
				q.Plan.ParentJournalSHA = strings.Repeat("f", 64)
			case "raw-go":
				raw = ""
			case "failed-gate":
				q.Qualification.Gates[0].Status = "BLOCKED"
			case "expired-go":
				q.ValidUntil = now
			case "future-approval":
				p.ApprovedAt = now.Add(time.Minute)
			case "credential-evidence":
				n.NativeEvidence["current-credential"] = strings.Repeat("f", 64)
			case "unknown-mode":
				n.CredentialMode = "unknown"
			case "short-caller":
				caller = now.Add(14 * time.Minute)
			}
			resealNormalTailExtensionFixture(runtime, &q)
			if _, err := newManagedLateNormalTailExtensionAuthority(runtime, q, raw, caller, now); err == nil {
				t.Fatal("changed360 qualification accepted")
			}
		})
	}
	r.normalTail.CredentialMode = "kubernetes-client-certificate"
	r.normalTail.CredentialExpires = now.Add(45 * time.Minute)
	r.normalTail.NativeEvidence["current-credential"] = managedLateNormalTailCredentialSHA(r.normalTail)
	r.normalSupplement.Extension.CredentialSHA = managedLateNormalTailCredentialSHA(r.normalTail)
	r.normalSupplement.Extension.CredentialExpires = r.normalTail.CredentialExpires
	resealNormalTailExtensionFixture(r, &a)
	phase, err := newManagedLateNormalTailExtensionAuthority(r, a, strings.Repeat("9", 64), now.Add(360*time.Minute), now)
	if err != nil || !phase.Deadline.Equal(r.normalTail.CredentialExpires.Add(-5*time.Minute)) {
		t.Fatal("finite credential cap", err)
	}
	a.Plan.CheckerSHA = strings.Repeat("8", 64)
	resealNormalTailExtensionFixture(r, &a)
	if _, err := newManagedLateNormalTailExtensionAuthority(r, a, strings.Repeat("9", 64), now.Add(360*time.Minute), now); err != nil {
		t.Fatal("scoped fresh checker required reapproval", err)
	}
}
func TestManagedLateNormalTailExtensionApprovalAndPairFailClosed(t *testing.T) {
	store, e, _, _, p := normalTailExtensionFixture(t)
	for _, relative := range []string{managedLateNormalTailExtensionApprovalPath, managedLateNormalTailExtensionProposalPath, managedLateNormalTailExtensionSourcePath, managedLateNormalTailExtensionOwnerPath, managedLateNormalTailSupplementPath(p.Parent, false)} {
		path := filepath.Join(store.Root, relative)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.write(relative, []byte(`{}`), true); err != nil {
			t.Fatal(err)
		}
		if _, err := loadManagedLateNormalTailExtensionApproval(store, p.Parent); err == nil {
			t.Fatal("metadata/base drift accepted", relative)
		}
		if err := store.write(relative, raw, true); err != nil {
			t.Fatal(err)
		}
	}
	approvalPath := filepath.Join(store.Root, managedLateNormalTailExtensionApprovalPath)
	raw, _ := os.ReadFile(approvalPath)
	if err := os.Remove(approvalPath); err != nil {
		t.Fatal(err)
	}
	if _, cancel, err := managedLateNormalTailSupplementCallerContext(context.Background(), store, e, 360*time.Minute, true); err == nil {
		cancel()
		t.Fatal("old180 approval authorized360")
	}
	if err := store.write(managedLateNormalTailExtensionApprovalPath, raw, false); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"witness-only", "primary-only", "mismatch", "public", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			primary := filepath.Join(store.Root, managedLateNormalTailExtensionPath(p.Parent, false))
			witness := filepath.Join(store.Root, managedLateNormalTailExtensionPath(p.Parent, true))
			defer os.Remove(primary)
			defer os.Remove(witness)
			if mode != "primary-only" {
				if err := store.write(managedLateNormalTailExtensionPath(p.Parent, true), []byte(`{}`), false); err != nil {
					t.Fatal(err)
				}
			}
			if mode != "witness-only" {
				value := []byte(`{}`)
				if mode == "mismatch" {
					value = []byte(`{"changed":true}`)
				}
				if err := store.write(managedLateNormalTailExtensionPath(p.Parent, false), value, false); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "public" {
				os.Chmod(primary, 0644)
			}
			if mode == "symlink" {
				os.Remove(primary)
				os.Symlink(witness, primary)
			}
			if _, err := readManagedLateNormalTailExtensionPair(store, p.Parent); err == nil {
				t.Fatal("unsafe/orphan pair accepted")
			}
		})
	}
}
func TestManagedLateNormalTailExtensionAdmissionUsesActualCheckerAndFixedNarrowRetry(t *testing.T) {
	store, e, r, a, p := normalTailExtensionFixture(t)
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		t.Fatal(err)
	}
	a.Plan.CheckerSHA = checker
	resealNormalTailExtensionFixture(r, &a)
	saveNormalTailExtensionQualification(t, store, r, a)
	ctx, cancel := context.WithTimeout(context.Background(), 360*time.Minute)
	defer cancel()
	raw := environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a))
	bounded, close, err := admitManagedLateNormalTailExtension(ctx, store, e, r, a, raw)
	if err != nil {
		t.Fatal("fresh checker admission", err)
	}
	defer close()
	end, _ := bounded.Deadline()
	if !end.Equal(p.ExtendedDeadline) {
		t.Fatal("fixed cap changed")
	}
	before, _ := readManagedLateNormalTailExtensionPair(store, p.Parent)
	short, shortCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer shortCancel()
	retry, retryCancel, err := admitManagedLateNormalTailExtension(short, store, e, r, a, raw)
	if err != nil {
		t.Fatal("existing pair requires another first reserve", err)
	}
	defer retryCancel()
	got, _ := retry.Deadline()
	want, _ := short.Deadline()
	if !got.Equal(want) {
		t.Fatal("caller enlarged")
	}
	after, _ := readManagedLateNormalTailExtensionPair(store, p.Parent)
	if !bytes.Equal(before, after) {
		t.Fatal("retry rewrote grant")
	}
	a.Plan.CheckerSHA = strings.Repeat("f", 64)
	resealNormalTailExtensionFixture(r, &a)
	if _, _, err := admitManagedLateNormalTailExtension(ctx, store, e, r, a, raw); err == nil {
		t.Fatal("stale executable admitted")
	}
}
