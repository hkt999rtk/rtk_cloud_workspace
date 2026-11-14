package deploymentplan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"
)

// AdmitFailedForwardResumable verifies the original owners and admits only
// unwritten create/retain operations without waiting for workload readiness.
// Recovery may be needed precisely because an existing workload is unready;
// the native executor and final acceptance still wait for readiness.
func (p *Plan) AdmitFailedForwardResumable(ctx context.Context, backend Backend, root string) error {
	bad := errors.New("failed forward qualification requires exact recorded create or retain owners")
	if p == nil || backend == nil {
		return bad
	}
	prior, err := p.resumableEvidence(root)
	if err != nil {
		return err
	}
	for _, resource := range p.Resources {
		if resource.Action != "create" && resource.Action != "retain" {
			return bad
		}
		if prior == nil {
			continue
		}
		owner := prior.journal.Owners[resource.ID]
		if owner == "" {
			continue
		}
		current, readErr := backend.Read(ctx, resource.ID)
		if readErr != nil {
			return readErr
		}
		if err = recordedResourceMatches(resource, owner, current); err != nil {
			return err
		}
	}
	if prior == nil {
		return p.Admit(ctx, backend)
	}
	return p.recordedTarget(prior.journal).Admit(ctx, backend)
}

// FailedForwardEvidence identifies the untouched original failed journal that
// was independently revalidated under its parent fence and native lease.
type FailedForwardEvidence struct {
	PlanSHA256     string     `json:"plan_sha256"`
	JournalSHA256  string     `json:"journal_sha256"`
	FailedResource string     `json:"failed_resource"`
	Lease          LeaseOwner `json:"lease"`
}

func failedForwardBytesDigest(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

// VerifyFullyWrittenFailedForward observes only the exact fully written child.
// The caller must separately qualify any repair before calling and durably
// record this evidence in the parent; it never edits or replaces the source
// plan, failed journal, or child lock and never replays any resource write.
// accept must itself be read-only, repeatable, and verify required acceptance.
func (p *Plan) VerifyFullyWrittenFailedForward(ctx context.Context, backend Backend, root, expectedPlan, expectedJournal, expectedFailed string, accept func(context.Context, *Plan) error, persist func(context.Context, FailedForwardEvidence, ExecutionLease) error) (proof FailedForwardEvidence, resultErr error) {
	bad := errors.New("fully written failed child requires its exact qualified original journal, parent and lease")
	if p == nil || backend == nil || accept == nil || persist == nil || expectedPlan == "" || expectedPlan != p.SHA256() || expectedJournal == "" || expectedFailed == "" {
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
	j := original.journal
	if failedForwardBytesDigest(original.journalRaw) != expectedJournal || j.Failed != expectedFailed || j.InFlight != "" || j.Accepted || j.AcceptanceStarted || len(j.Completed) != len(p.Resources) || len(j.Owners) != len(p.Resources) {
		return proof, bad
	}
	found := false
	for _, r := range p.Resources {
		if r.ID == expectedFailed && j.Owners[r.ID] != "" {
			found = true
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
	if owner.ResourceID == "" || owner.UID == "" || owner.Holder == "" || lease.Check(ctx) != nil || parent.CheckParentExecution(ctx, p, root) != nil {
		return proof, bad
	}
	if err = parent.RecordParentExecutionLease(ctx, p, owner); err != nil {
		return proof, err
	}
	if err = p.verifyRecorded(ctx, backend, j); err != nil {
		return proof, err
	}
	if err = accept(ctx, p); err != nil {
		return proof, err
	}
	fresh, err := p.resumableEvidence(root)
	if err != nil || fresh == nil || !bytes.Equal(fresh.planRaw, original.planRaw) || !bytes.Equal(fresh.journalRaw, original.journalRaw) || lease.Check(ctx) != nil || lease.Owner() != owner || parent.CheckParentExecution(ctx, p, root) != nil {
		return proof, bad
	}
	proof = FailedForwardEvidence{PlanSHA256: p.SHA256(), JournalSHA256: expectedJournal, FailedResource: expectedFailed, Lease: owner}
	if err = persist(ctx, proof, lease); err != nil {
		return FailedForwardEvidence{}, err
	}
	fresh, err = p.resumableEvidence(root)
	if err != nil || fresh == nil || !bytes.Equal(fresh.planRaw, original.planRaw) || !bytes.Equal(fresh.journalRaw, original.journalRaw) || lease.Check(ctx) != nil || lease.Owner() != owner || parent.CheckParentExecution(ctx, p, root) != nil {
		return FailedForwardEvidence{}, bad
	}
	return proof, nil
}

// QualifyFullyWrittenFailedForward only observes original plan, journal and
// owner/body state. It deliberately does not require readiness: the repair is
// meant to remove that blocker. Its evidence must be bound into a separately
// qualified parent continuation; it grants no authority to write resources.
func (p *Plan) QualifyFullyWrittenFailedForward(ctx context.Context, backend Backend, root, expectedPlan, expectedFailed string) (FailedForwardEvidence, error) {
	bad := errors.New("failed child qualification requires its exact fully written original owners")
	if p == nil || backend == nil || expectedPlan == "" || p.SHA256() != expectedPlan || expectedFailed == "" {
		return FailedForwardEvidence{}, bad
	}
	original, err := p.resumableEvidence(root)
	if err != nil || original == nil {
		return FailedForwardEvidence{}, bad
	}
	j := original.journal
	if j.Failed != expectedFailed || j.InFlight != "" || j.Accepted || j.AcceptanceStarted || len(j.Completed) != len(p.Resources) || len(j.Owners) != len(p.Resources) {
		return FailedForwardEvidence{}, bad
	}
	found := false
	for _, r := range p.Resources {
		if r.ID == expectedFailed {
			found = true
		}
		if ctx.Err() != nil {
			return FailedForwardEvidence{}, ctx.Err()
		}
		actual, err := backend.Read(ctx, r.ID)
		if err != nil {
			return FailedForwardEvidence{}, err
		}
		if err = recordedResourceMatches(r, j.Owners[r.ID], actual); err != nil {
			return FailedForwardEvidence{}, err
		}
	}
	fresh, err := p.resumableEvidence(root)
	if !found || err != nil || fresh == nil || !bytes.Equal(fresh.planRaw, original.planRaw) || !bytes.Equal(fresh.journalRaw, original.journalRaw) {
		return FailedForwardEvidence{}, bad
	}
	return FailedForwardEvidence{PlanSHA256: expectedPlan, JournalSHA256: failedForwardBytesDigest(original.journalRaw), FailedResource: expectedFailed}, nil
}
