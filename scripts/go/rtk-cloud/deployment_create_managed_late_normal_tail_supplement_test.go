package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func normalTailSupplementFixture(t *testing.T) (secretStore, *environmentCreateExecution, *createServiceSessionRecovery, createFoundationRecoveryArtifact, managedLateNormalTailSupplementProposal) {
	t.Helper()
	return normalTailSupplementFixtureAt(t, 100*time.Minute)
}
func normalTailSupplementFixtureAt(t *testing.T, age time.Duration) (secretStore, *environmentCreateExecution, *createServiceSessionRecovery, createFoundationRecoveryArtifact, managedLateNormalTailSupplementProposal) {
	t.Helper()
	_, _, _, store, e, _, _ := createCompletedIngressFixture(t)
	sample, _ := redisDurableProjectionFixture()
	source := e.Journal
	source.Entries = sample.Entries
	for i := range source.Entries {
		source.Entries[i].StartedAt = source.Entries[i].StartedAt.Add(-(age - 100*time.Minute))
		source.Entries[i].CompletedAt = source.Entries[i].CompletedAt.Add(-(age - 100*time.Minute))
	}
	custody := "retire-pki-bootstrap-custody-" + source.PlanSHA256[:12]
	source.Entries[429].Key = custody
	source.Entries[430].Key = "kubernetes/" + custody
	source.KubernetesLeases = nil
	for i := range 149 {
		source.KubernetesLeases = append(source.KubernetesLeases, deploymentplan.LeaseOwner{UID: deploymentplan.Digest(i), Holder: "isolated-original"})
	}
	if !managedLateNormalTailControllerSelected(source) {
		t.Fatal("isolated captured434 fixture not original-parent bound")
	}
	e.Journal = source
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	started := now.Add(-age)
	n, old, _, _ := normalTailAuthorityFixture(t)
	n.SourceSHA = deploymentplan.Digest(managedLateNormalTailOriginalSource(source))
	n.CurrentSHA = n.SourceSHA
	n.OwnerSHA = deploymentplan.Digest(e.owner)
	n.Approval.ParentSHA = source.PlanSHA256
	n.Approval.ApprovedAt = started.Add(-time.Minute)
	proof := []byte(`{"isolated":"immutable-completed396-and-all24"}`)
	n.CompletedProofSHA = environmentPreflightBytesSHA256(proof)
	old.Plan.ParentSHA = source.PlanSHA256
	old.Plan.ParentOwnerSHA = n.OwnerSHA
	old.Plan.ParentJournalSHA = n.SourceSHA
	old.Plan.Materials[managedLateNormalTailMaterial+"source"] = managedLateNormalTailDigest(n)
	n.Review = deploymentplan.Digest(old.Plan)
	old.Qualification.PlanSHA256 = n.Review
	for i := range old.Qualification.Gates {
		old.Qualification.Gates[i].EvidenceSHA256 = n.Review
	}
	old.QualifiedAt = started.Add(2 * time.Minute)
	old.ValidUntil = old.QualifiedAt.Add(15 * time.Minute)
	oldBytes := mustProviderRecoveryJSON(old)
	phase, err := newManagedLateNormalTailAuthority(n, old, environmentPreflightBytesSHA256(oldBytes), started, started.Add(3*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	oldBase := filepath.Join("deployment/create/openbao-recovery", n.Review)
	for name, value := range map[string]any{"qualification.json": old, managedLateNormalTailFilename: n} {
		if err := store.write(filepath.Join(oldBase, name), mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensurePrivateDirectory(filepath.Dir(filepath.Join(store.Root, managedLateNormalTailPath(source.PlanSHA256, false)))); err != nil {
		t.Fatal(err)
	}
	if err := writeManagedLateNormalTailPair(store, source.PlanSHA256, mustProviderRecoveryJSON(phase)); err != nil {
		t.Fatal(err)
	}
	if err := store.write(managedLateTerminalFinalizerCompletedPath(source.PlanSHA256), proof, false); err != nil {
		t.Fatal(err)
	}
	rawSource := mustProviderRecoveryJSON(source)
	no := false
	recorded := now.Add(-time.Second)
	if age > 180*time.Minute {
		recorded = now.Add(-(age - 100*time.Minute))
	}
	p := managedLateNormalTailSupplementProposal{Schema: "rtk-cloud.remaining-normal-tail-timeout-supplement-proposal/v1", Environment: "dev", Parent: source.PlanSHA256, SourcePath: managedLateNormalTailSupplementSourcePath, SourceRawSHA: environmentPreflightBytesSHA256(rawSource), OwnerPath: managedLateNormalTailSupplementOwnerPath, OwnerSHA: n.OwnerSHA, SourceEntries: 434, SourceLeases: 149, OriginalAuthoritySHA: deploymentplan.Digest(phase), OriginalAuthorityRawSHA: environmentPreflightBytesSHA256(mustProviderRecoveryJSON(phase)), OriginalReview: n.Review, OriginalQualifiedBytesSHA: phase.QualifiedBytesSHA, CompletedProofSHA: n.CompletedProofSHA, PreparedCheckerSHA: strings.Repeat("6", 64), OriginalStartedAt: phase.StartedAt, OriginalDeadline: phase.Deadline, ExtendedDeadline: phase.StartedAt.Add(managedLateNormalTailSupplementLimit), TotalMinutes: 180, FirstReserveMinutes: 15, CredentialReserveMinutes: 5, ExecutionAuthority: &no, Status: "proposal-awaiting-explicit-approval", RecordedAt: recorded, Allowed: managedLateNormalTailSupplementAllowed(), Forbidden: managedLateNormalTailSupplementForbidden(), Completion: managedLateNormalTailSupplementCompletion()}
	pRaw := mustProviderRecoveryJSON(p)
	approval := managedLateNormalTailSupplementApprovalRecord{Schema: "rtk-cloud.remaining-normal-tail-timeout-supplement-operator-approval/v1", Environment: "dev", Parent: source.PlanSHA256, ProposalSHA: environmentPreflightBytesSHA256(pRaw), SourceRawSHA: p.SourceRawSHA, OriginalAuthorityRawSHA: p.OriginalAuthorityRawSHA, TotalMinutes: 180, Approved: true, ExecutionAuthority: &no, RecordedAt: recorded.Add(500 * time.Millisecond), HumanMessage: "yes"}
	for path, raw := range map[string][]byte{managedLateNormalTailSupplementProposalPath: pRaw, managedLateNormalTailSupplementApprovalPath: mustProviderRecoveryJSON(approval), managedLateNormalTailSupplementSourcePath: rawSource, managedLateNormalTailSupplementOwnerPath: mustProviderRecoveryJSON(e.owner)} {
		if err := store.write(path, raw, false); err != nil {
			t.Fatal(err)
		}
	}
	loaded, err := loadManagedLateNormalTailSupplementApproval(store, source.PlanSHA256)
	if err != nil {
		t.Fatal("valid explicit supplement metadata refused", err)
	}
	n.CurrentSHA = deploymentplan.Digest(source)
	n.Authority = phase
	s := &createManagedLateNormalTailSupplement{Approval: loaded, CredentialSHA: managedLateNormalTailCredentialSHA(n), CredentialExpires: n.CredentialExpires}
	n.Supplement = s
	r := &createServiceSessionRecovery{normalTail: n, normalSupplement: s}
	a := old
	a.Plan.Materials = map[string]string{}
	for k, v := range old.Plan.Materials {
		a.Plan.Materials[k] = v
	}
	a.Plan.ParentJournalSHA = deploymentplan.Digest(source)
	a.Plan.Materials[managedLateNormalTailMaterial+"source"] = managedLateNormalTailDigest(n)
	a.Plan.Materials[managedLateNormalTailSupplementMaterial+"intent"] = managedLateNormalTailSupplementIntent(r)
	a.Plan.Materials[managedLateNormalTailSupplementMaterial+"policy"] = managedLateNormalTailSupplementPolicySHA()
	a.QualifiedAt = now.Add(-time.Minute)
	a.ValidUntil = a.QualifiedAt.Add(15 * time.Minute)
	a.Qualification.Gates = slices.Clone(old.Qualification.Gates)
	resealNormalTailSupplementFixture(r, &a)
	return store, e, r, a, p
}
func resealNormalTailSupplementFixture(r *createServiceSessionRecovery, a *createFoundationRecoveryArtifact) {
	a.Plan.Materials[managedLateNormalTailMaterial+"source"] = managedLateNormalTailDigest(r.normalTail)
	a.Plan.Materials[managedLateNormalTailSupplementMaterial+"intent"] = managedLateNormalTailSupplementIntent(r)
	a.Qualification.PlanSHA256 = deploymentplan.Digest(a.Plan)
	r.normalTail.Review = a.Qualification.PlanSHA256
	for i := range a.Qualification.Gates {
		a.Qualification.Gates[i].EvidenceSHA256 = a.Qualification.PlanSHA256
	}
}
func TestManagedLateNormalTailSupplementAnchorsTotal180AndPreservesOriginalPair(t *testing.T) {
	store, e, r, a, p := normalTailSupplementFixture(t)
	before, _ := readManagedLateNormalTailPair(store, e.Journal.PlanSHA256)
	now := time.Now().UTC()
	caller := now.Add(180 * time.Minute)
	qraw := mustProviderRecoveryJSON(a)
	phase, err := newManagedLateNormalTailSupplementAuthority(r, a, environmentPreflightBytesSHA256(qraw), caller, now)
	if err != nil || !phase.Deadline.Equal(p.OriginalStartedAt.Add(180*time.Minute)) || !phase.OriginalDeadline.Equal(p.OriginalStartedAt.Add(90*time.Minute)) {
		t.Fatal("new epoch replaced original fixed anchor", err)
	}
	if !phase.OriginalDeadline.Before(now) {
		t.Fatal("fixture did not exercise historical expired90")
	}
	if !managedLateNormalTailSupplementPauseMatches(phase, phase.Deadline) {
		t.Fatal("typed supplemental pause unavailable")
	}
	bounded, cancel, err := managedLateNormalTailSupplementCallerContext(context.Background(), store, e, 180*time.Minute, true)
	if err != nil {
		t.Fatal("approved exact434 caller rejected", err)
	}
	defer cancel()
	end, _ := bounded.Deadline()
	if !end.Equal(p.ExtendedDeadline) {
		t.Fatal("caller reset supplement anchor")
	}
	base := filepath.Join("deployment/create/openbao-recovery", phase.Review)
	for name, value := range map[string]any{"qualification.json": a, managedLateNormalTailFilename: r.normalTail, managedLateNormalTailSupplementFilename: r.normalSupplement} {
		if err := store.write(filepath.Join(base, name), mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeManagedLateNormalTailSupplementPair(store, e.Journal.PlanSHA256, mustProviderRecoveryJSON(phase)); err != nil {
		t.Fatal(err)
	}
	got, err := readManagedLateNormalTailSupplementAuthority(store, e.Journal.PlanSHA256)
	if err != nil || deploymentplan.Digest(got) != deploymentplan.Digest(phase) {
		t.Fatal("genuine paired supplement could not be restored", err)
	}
	if writeManagedLateNormalTailSupplementPair(store, e.Journal.PlanSHA256, mustProviderRecoveryJSON(phase)) == nil {
		t.Fatal("supplement clock could be rewritten")
	}
	after, _ := readManagedLateNormalTailPair(store, e.Journal.PlanSHA256)
	if !bytes.Equal(before, after) {
		t.Fatal("original90 receipt changed")
	}
}
func TestManagedLateNormalTailSupplementFreshAuthorityRefusesDriftAndKeepsCallerCaps(t *testing.T) {
	_, _, r, a, _ := normalTailSupplementFixture(t)
	now := time.Now().UTC()
	sha := strings.Repeat("9", 64)
	for _, mode := range []string{"approval-missing", "source", "owner", "old-authority", "raw-empty", "failed-gate", "expired-go", "credential-mode", "credential-evidence", "future-approval", "extended-origin", "self-consistent-origin-reset", "short-caller"} {
		t.Run(mode, func(t *testing.T) {
			n := *r.normalTail
			n.NativeEvidence = map[string]string{}
			for k, v := range r.normalTail.NativeEvidence {
				n.NativeEvidence[k] = v
			}
			s := *r.normalSupplement
			p := *s.Approval
			s.Approval = &p
			q := a
			q.Plan.Materials = map[string]string{}
			for k, v := range a.Plan.Materials {
				q.Plan.Materials[k] = v
			}
			q.Qualification.Gates = slices.Clone(a.Qualification.Gates)
			x := &createServiceSessionRecovery{normalTail: &n, normalSupplement: &s}
			raw := sha
			caller := now.Add(3 * time.Hour)
			switch mode {
			case "approval-missing":
				s.Approval = nil
			case "source":
				q.Plan.ParentJournalSHA = strings.Repeat("f", 64)
			case "owner":
				q.Plan.ParentOwnerSHA = strings.Repeat("f", 64)
			case "old-authority":
				p.OriginalAuthoritySHA = strings.Repeat("f", 64)
			case "raw-empty":
				raw = ""
			case "failed-gate":
				q.Qualification.Gates[0].Status = "BLOCKED"
			case "expired-go":
				q.ValidUntil = now
			case "credential-mode":
				n.CredentialMode = "unqualified"
			case "credential-evidence":
				n.NativeEvidence["current-credential"] = strings.Repeat("f", 64)
			case "future-approval":
				p.ApprovedAt = now.Add(time.Minute)
			case "extended-origin":
				p.ExtendedDeadline = now.Add(180 * time.Minute)
			case "self-consistent-origin-reset":
				p.OriginalStartedAt = now
				p.OriginalDeadline = now.Add(90 * time.Minute)
				p.ExtendedDeadline = now.Add(180 * time.Minute)
			case "short-caller":
				caller = now.Add(14 * time.Minute)
			}
			resealNormalTailSupplementFixture(x, &q)
			if _, err := newManagedLateNormalTailSupplementAuthority(x, q, raw, caller, now); err == nil {
				t.Fatal("unqualified supplement accepted")
			}
		})
	}
	r.normalTail.CredentialMode = "kubernetes-client-certificate"
	r.normalTail.CredentialExpires = now.Add(40 * time.Minute)
	r.normalTail.NativeEvidence["current-credential"] = managedLateNormalTailCredentialSHA(r.normalTail)
	r.normalSupplement.CredentialSHA = managedLateNormalTailCredentialSHA(r.normalTail)
	r.normalSupplement.CredentialExpires = r.normalTail.CredentialExpires
	resealNormalTailSupplementFixture(r, &a)
	got, err := newManagedLateNormalTailSupplementAuthority(r, a, sha, now.Add(3*time.Hour), now)
	if err != nil || !got.Deadline.Equal(r.normalTail.CredentialExpires.Add(-5*time.Minute)) {
		t.Fatal("finite current credential did not retain reserve", err)
	}
	a.Plan.CheckerSHA = strings.Repeat("8", 64)
	resealNormalTailSupplementFixture(r, &a)
	if _, err := newManagedLateNormalTailSupplementAuthority(r, a, sha, now.Add(3*time.Hour), now); err != nil {
		t.Fatal("fresh scoped repair required another approval", err)
	}
}
func TestManagedLateNormalTailSupplementApprovalAndPairFailClosed(t *testing.T) {
	for _, mode := range []string{"missing-approval", "changed-proposal", "changed-source", "changed-owner", "old-pair", "witness-only", "authority-only", "mismatch", "public", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			store, e, _, _, _ := normalTailSupplementFixture(t)
			parent := e.Journal.PlanSHA256
			switch mode {
			case "missing-approval":
				os.Remove(filepath.Join(store.Root, managedLateNormalTailSupplementApprovalPath))
			case "changed-proposal":
				store.write(managedLateNormalTailSupplementProposalPath, []byte(`{}`), true)
			case "changed-source":
				store.write(managedLateNormalTailSupplementSourcePath, []byte(`{}`), true)
			case "changed-owner":
				store.write(managedLateNormalTailSupplementOwnerPath, []byte(`{}`), true)
			case "old-pair":
				store.write(managedLateNormalTailPath(parent, false), []byte(`{}`), true)
			case "witness-only":
				store.write(managedLateNormalTailSupplementPath(parent, true), []byte(`{}`), false)
			case "authority-only":
				store.write(managedLateNormalTailSupplementPath(parent, false), []byte(`{}`), false)
			case "mismatch":
				store.write(managedLateNormalTailSupplementPath(parent, true), []byte(`{}`), false)
				store.write(managedLateNormalTailSupplementPath(parent, false), []byte(`{"foreign":true}`), false)
			case "public":
				os.Chmod(filepath.Join(store.Root, managedLateNormalTailSupplementApprovalPath), 0644)
			case "symlink":
				path := filepath.Join(store.Root, managedLateNormalTailSupplementApprovalPath)
				raw, _ := os.ReadFile(path)
				target := filepath.Join(t.TempDir(), "target")
				os.WriteFile(target, raw, 0600)
				os.Remove(path)
				os.Symlink(target, path)
			}
			if strings.Contains(mode, "only") || mode == "mismatch" {
				if _, err := readManagedLateNormalTailSupplementPair(store, parent); err == nil {
					t.Fatal("partial pair could regenerate")
				}
			} else if _, err := loadManagedLateNormalTailSupplementApproval(store, parent); err == nil {
				t.Fatal("approval/source drift accepted")
			}
		})
	}
}

func TestManagedLateNormalTailSupplementAdmissionChecksActualCheckerAndKeepsNarrowerRetry(t *testing.T) {
	store, e, r, a, p := normalTailSupplementFixture(t)
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		t.Fatal(err)
	}
	a.Plan.CheckerSHA = checker
	resealNormalTailSupplementFixture(r, &a)
	base := filepath.Join("deployment/create/openbao-recovery", a.Qualification.PlanSHA256)
	for name, value := range map[string]any{"qualification.json": a, managedLateNormalTailFilename: r.normalTail, managedLateNormalTailSupplementFilename: r.normalSupplement} {
		if err := store.write(filepath.Join(base, name), mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	old, _ := readManagedLateNormalTailPair(store, e.Journal.PlanSHA256)
	rawSHA := environmentPreflightBytesSHA256(mustProviderRecoveryJSON(a))
	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Minute)
	defer cancel()
	admitted, closeAdmit, err := admitManagedLateNormalTailSupplement(ctx, store, e, r, a, rawSHA)
	if err != nil {
		t.Fatal("fresh actualchecker/approvedsource could not admit", err)
	}
	defer closeAdmit()
	end, _ := admitted.Deadline()
	if !end.Equal(p.ExtendedDeadline) {
		t.Fatal("admission restarted anchor")
	}
	pair, _ := readManagedLateNormalTailSupplementPair(store, e.Journal.PlanSHA256)
	short, shortCancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer shortCancel()
	retry, retryCancel, err := admitManagedLateNormalTailSupplement(short, store, e, r, a, rawSHA)
	if err != nil {
		t.Fatal("existing grant was subjected to another first15m admission", err)
	}
	defer retryCancel()
	shortened, _ := retry.Deadline()
	shortEnd, _ := short.Deadline()
	if !shortened.Equal(shortEnd) {
		t.Fatal("retry enlarged a narrower caller")
	}
	unchanged, _ := readManagedLateNormalTailSupplementPair(store, e.Journal.PlanSHA256)
	if !bytes.Equal(pair, unchanged) {
		t.Fatal("retry reset paired authority")
	}
	a.Plan.CheckerSHA = strings.Repeat("f", 64)
	resealNormalTailSupplementFixture(r, &a)
	if _, _, err := admitManagedLateNormalTailSupplement(ctx, store, e, r, a, rawSHA); err == nil {
		t.Fatal("stale executable checker was admitted")
	}
	retained, _ := readManagedLateNormalTailPair(store, e.Journal.PlanSHA256)
	if !bytes.Equal(old, retained) {
		t.Fatal("original90 grant changed")
	}
}
