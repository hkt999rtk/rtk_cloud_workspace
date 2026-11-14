package deploymentplan

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"time"
)

// VerifyFullyWrittenFailedSuccessor verifies a separately authenticated and
// narrowly qualified accepted retain successor of the original failed child.
// The caller must independently bind and validate the allowed desired-body
// transition before calling; this function does not authorize that transition.
// It never rewrites either journal or performs a Kubernetes write. The parent
// and original child lease fence the observations and receipt callback.
func (p *Plan) VerifyFullyWrittenFailedSuccessor(ctx context.Context, backend Backend, root, expectedPlan, expectedJournal, expectedFailed string, successor *Plan, accept func(context.Context, *Plan) error, persist func(context.Context, FailedForwardEvidence, ExecutionLease) error) (proof FailedForwardEvidence, resultErr error) {
	bad := errors.New("failed child successor requires exact original and accepted successor journals, owners, parent and lease")
	if p == nil || successor == nil || backend == nil || accept == nil || persist == nil || expectedPlan == "" || expectedPlan != p.SHA256() || expectedJournal == "" || expectedFailed == "" || len(p.Resources) == 0 || len(p.Resources) != len(successor.Resources) || p.Environment != successor.Environment || p.Stack != successor.Stack || p.Operation != successor.Operation || p.ConfigurationSHA256 != successor.ConfigurationSHA256 || p.ReleaseSHA256 != successor.ReleaseSHA256 {
		return proof, bad
	}
	parent, ok := backend.(ParentExecutionBackend)
	if !ok {
		return proof, bad
	}
	leasing, ok := backend.(LeasingBackend)
	if !ok {
		return proof, bad
	}
	original, err := p.resumableEvidence(root)
	if err != nil || original == nil {
		return proof, bad
	}
	old := original.journal
	if failedForwardBytesDigest(original.journalRaw) != expectedJournal || old.Failed != expectedFailed || old.InFlight != "" || old.Accepted || old.AcceptanceStarted || len(old.Completed) != len(p.Resources) || len(old.Owners) != len(p.Resources) {
		return proof, bad
	}
	newer, err := successor.resumableEvidence(root)
	if err != nil || newer == nil {
		return proof, bad
	}
	next := newer.journal
	if !next.Accepted || !next.AcceptanceStarted || next.Failed != "" || next.InFlight != "" || len(next.Completed) != len(successor.Resources) || len(next.Owners) != len(successor.Resources) {
		return proof, bad
	}
	found := false
	for i, r := range p.Resources {
		s := successor.Resources[i]
		uid := old.Owners[r.ID]
		if r.ID == expectedFailed {
			found = true
		}
		if old.Completed[i] != r.ID || next.Completed[i] != r.ID || s.ID != r.ID || s.Phase != r.Phase || !slices.Equal(s.Dependencies, r.Dependencies) || r.Action == "retire" || s.Action != "retain" || uid == "" || next.Owners[r.ID] != uid || s.UID != uid || (r.UID != "" && r.UID != uid) {
			return proof, bad
		}
	}
	if !found || parent.CheckParentExecution(ctx, p, root) != nil {
		return proof, bad
	}
	lease, err := leasing.AcquireExecution(ctx, p)
	if err != nil {
		return proof, err
	}
	if lease == nil {
		return proof, bad
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resultErr = errors.Join(resultErr, lease.Release(releaseCtx))
	}()
	owner := lease.Owner()
	fenced := func() bool {
		return owner.ResourceID != "" && owner.UID != "" && owner.Holder != "" && lease.Check(ctx) == nil && lease.Owner() == owner && parent.CheckParentExecution(ctx, p, root) == nil
	}
	if !fenced() {
		return proof, bad
	}
	if err = parent.RecordParentExecutionLease(ctx, p, owner); err != nil {
		return proof, err
	}
	if err = successor.verifyRecorded(ctx, backend, next); err != nil {
		return proof, err
	}
	if !fenced() {
		return proof, bad
	}
	if err = accept(ctx, successor); err != nil {
		return proof, err
	}
	verifyEvidence := func() bool {
		fresh, e := p.resumableEvidence(root)
		if e != nil || fresh == nil || !bytes.Equal(fresh.planRaw, original.planRaw) || !bytes.Equal(fresh.journalRaw, original.journalRaw) {
			return false
		}
		fresh, e = successor.resumableEvidence(root)
		if e != nil || fresh == nil || !bytes.Equal(fresh.planRaw, newer.planRaw) || !bytes.Equal(fresh.journalRaw, newer.journalRaw) {
			return false
		}
		return fenced()
	}
	if !verifyEvidence() {
		return proof, bad
	}
	// Re-read owners and desired bodies immediately before recording the alias.
	for _, r := range successor.Resources {
		current, e := backend.Read(ctx, r.ID)
		if e != nil || recordedResourceMatches(r, next.Owners[r.ID], current) != nil {
			return proof, bad
		}
	}
	if !verifyEvidence() {
		return proof, bad
	}
	proof = FailedForwardEvidence{PlanSHA256: p.SHA256(), JournalSHA256: expectedJournal, FailedResource: expectedFailed, Lease: owner}
	if err = persist(ctx, proof, lease); err != nil {
		return FailedForwardEvidence{}, err
	}
	if !verifyEvidence() {
		return FailedForwardEvidence{}, bad
	}
	return proof, nil
}
