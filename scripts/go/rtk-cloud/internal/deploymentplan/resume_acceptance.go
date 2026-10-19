package deploymentplan

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// ResumeAppliedPodAcceptance reconciles the known, fully applied single-Pod
// child of a held parent. It never calls Write, Admit, or resource execution.
// Qualification must separately authenticate the original parent intent and
// exact journal bytes; neither partial writes nor uncertain acceptance qualify.
func (p *Plan) ResumeAppliedPodAcceptance(ctx context.Context, backend Backend, expectedDigest, journalSHA, privateRoot string, fence func(context.Context, *Plan) error, accept func(context.Context, *Plan, map[string]string) error) (result Journal, resultErr error) {
	bad := func() error {
		return blocked("deployment.recovery.acceptance", "", "exact fully applied scoped Pod acceptance evidence required", "preserve the original parent, plan, owners and failed journal; qualify the actual target before continuation")
	}
	if p == nil || p.Version != Version || p.Operation != "scoped" || p.SHA256() != expectedDigest || len(p.Resources) != 1 || p.Resources[0].Action != "create" || p.Resources[0].UID != "" || p.Resources[0].Baseline != nil || p.Resources[0].Desired["kind"] != "Pod" || len(journalSHA) != 64 || !filepath.IsAbs(privateRoot) || fence == nil || accept == nil {
		return result, bad()
	}
	parent, ok := backend.(ParentExecutionBackend)
	leasing, leaseOK := backend.(LeasingBackend)
	if !ok || !leaseOK || parent.CheckParentExecution(ctx, p, privateRoot) != nil {
		return result, bad()
	}
	directory := filepath.Join(privateRoot, "deployment", "rollback", expectedDigest)
	for _, path := range []string{privateRoot, filepath.Join(privateRoot, "deployment"), filepath.Join(privateRoot, "deployment", "rollback"), directory} {
		if err := privateDirectory(path); err != nil {
			return result, err
		}
	}
	lock := filepath.Join(directory, ".active")
	if err := os.Mkdir(lock, 0700); err != nil {
		return result, bad()
	}
	defer os.Remove(lock)
	running, err := LockExecutionOwner(lock, true)
	if err != nil {
		return result, err
	}
	defer running.Close()
	defer os.Remove(filepath.Join(lock, "running.lock"))
	planRaw, err := readPrivateExecutionEvidence(filepath.Join(directory, "plan.json"))
	if err != nil {
		return result, err
	}
	var recorded Plan
	if json.Unmarshal(planRaw, &recorded) != nil || recorded.SHA256() != expectedDigest {
		return result, bad()
	}
	raw, err := readPrivateExecutionEvidence(filepath.Join(directory, "journal.json"))
	if err != nil {
		return result, err
	}
	h := sha256.Sum256(raw)
	if hex.EncodeToString(h[:]) != journalSHA || json.Unmarshal(raw, &result) != nil || result.PlanSHA256 != expectedDigest || result.Accepted || !result.AcceptanceStarted || result.Failed != "deployment.post-deploy.workloads" || result.InFlight != "" || len(result.Completed) != 1 || result.Completed[0] != p.Resources[0].ID || len(result.Owners) != 1 || result.Owners[p.Resources[0].ID] == "" {
		return Journal{}, bad()
	}
	lease, err := leasing.AcquireExecution(ctx, p)
	if err != nil || lease == nil {
		return Journal{}, bad()
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resultErr = errors.Join(resultErr, lease.Release(cleanup))
	}()
	check := func() error {
		if ctx.Err() != nil || p.SHA256() != expectedDigest || parent.CheckParentExecution(ctx, p, privateRoot) != nil || fence(ctx, p) != nil || lease.Check(ctx) != nil {
			return bad()
		}
		current, err := readPrivateExecutionEvidence(filepath.Join(directory, "journal.json"))
		if err != nil || string(current) != string(raw) {
			return bad()
		}
		return nil
	}
	verify := func() error {
		if err := check(); err != nil {
			return err
		}
		resource := p.Resources[0]
		if err := backend.Wait(ctx, resource); err != nil {
			return err
		}
		current, err := backend.Read(ctx, resource.ID)
		if err != nil || current == nil || String(Map(current["metadata"])["uid"]) != result.Owners[resource.ID] || !Matches(resource.Desired, current) {
			return bad()
		}
		if err := p.Verify(ctx, backend); err != nil {
			return err
		}
		return check()
	}
	if err := verify(); err != nil {
		return Journal{}, err
	}
	attemptRoot := filepath.Join(directory, "acceptance-recovery")
	if err := privateDirectory(attemptRoot); err != nil {
		return Journal{}, err
	}
	attempt, err := os.MkdirTemp(attemptRoot, "attempt-")
	if err != nil {
		return Journal{}, err
	}
	if err := writeExclusiveJSON(filepath.Join(attempt, "original.json"), struct {
		PlanSHA, JournalSHA string
		Journal             json.RawMessage
		Lease               LeaseOwner
		StartedAt           time.Time
	}{expectedDigest, journalSHA, json.RawMessage(raw), lease.Owner(), time.Now().UTC()}); err != nil {
		return Journal{}, err
	}
	owners := map[string]string{p.Resources[0].ID: result.Owners[p.Resources[0].ID]}
	if err := accept(ctx, p, owners); err != nil {
		return Journal{}, err
	}
	if err := verify(); err != nil {
		return Journal{}, err
	}
	result.Accepted, result.Failed = true, ""
	if err := writeExclusiveJSON(filepath.Join(attempt, "completed.json"), struct {
		PlanSHA, OriginalJournalSHA, AcceptedJournalSHA string
		Lease                                           LeaseOwner
		CompletedAt                                     time.Time
	}{expectedDigest, journalSHA, Digest(result), lease.Owner(), time.Now().UTC()}); err != nil {
		return Journal{}, err
	}
	if err := saveJournal(directory, result); err != nil {
		return Journal{}, err
	}
	return result, nil
}

// The completed attempt precedes Accepted=true publication. An accepted child
// without this immutable recovery evidence cannot close its pending parent.
func VerifyAppliedPodAcceptanceReceipt(privateRoot, planSHA string, journal Journal) error {
	bad := func() error { return errors.New("original completed Pod acceptance attempt required") }
	if journal.PlanSHA256 != planSHA || !journal.Accepted || !journal.AcceptanceStarted || journal.Failed != "" || journal.InFlight != "" {
		return bad()
	}
	directory := filepath.Join(privateRoot, "deployment", "rollback", planSHA, "acceptance-recovery")
	info, err := os.Lstat(directory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return bad()
	}
	entries, err := os.ReadDir(directory)
	if err != nil || len(entries) > 128 {
		return bad()
	}
	for _, entry := range entries {
		if !entry.IsDir() || len(entry.Name()) < 8 || entry.Name()[:8] != "attempt-" {
			return bad()
		}
		path := filepath.Join(directory, entry.Name())
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			return bad()
		}
		raw, err := readPrivateExecutionEvidence(filepath.Join(path, "completed.json"))
		if err != nil {
			continue
		}
		var completed struct {
			PlanSHA, OriginalJournalSHA, AcceptedJournalSHA string
			Lease                                           LeaseOwner
			CompletedAt                                     time.Time
		}
		if json.Unmarshal(raw, &completed) != nil || completed.PlanSHA != planSHA || completed.AcceptedJournalSHA != Digest(journal) || completed.CompletedAt.IsZero() || completed.CompletedAt.After(time.Now().UTC()) || completed.Lease.UID == "" {
			continue
		}
		originalRaw, err := readPrivateExecutionEvidence(filepath.Join(path, "original.json"))
		if err != nil {
			continue
		}
		var original struct {
			PlanSHA, JournalSHA string
			Journal             json.RawMessage
			Lease               LeaseOwner
			StartedAt           time.Time
		}
		var before Journal
		if json.Unmarshal(originalRaw, &original) != nil || original.PlanSHA != planSHA || original.JournalSHA != completed.OriginalJournalSHA || original.Lease != completed.Lease || original.StartedAt.IsZero() || completed.CompletedAt.Before(original.StartedAt) || json.Unmarshal(original.Journal, &before) != nil {
			continue
		}
		h := sha256.Sum256(original.Journal)
		if hex.EncodeToString(h[:]) != original.JournalSHA || before.Accepted || before.Failed != "deployment.post-deploy.workloads" {
			continue
		}
		before.Accepted, before.Failed = true, ""
		if Digest(before) == Digest(journal) {
			return nil
		}
	}
	return bad()
}
