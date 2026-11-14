package deploymentplan

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
)

// This records a separately verified pre-API rejection, never a native write
// success. Original bytes and the caller's exact evidence remain immutable.
type RejectedCreateReconciliation struct {
	ResourceID          string `json:"resource_id"`
	SourceJournalSHA256 string `json:"source_journal_sha256"`
	EvidenceSHA256      string `json:"evidence_sha256"`
}

func rejectedCreateDigest(raw []byte) string {
	h := sha256.Sum256(raw)
	return hex.EncodeToString(h[:])
}

func validRejectedCreateDigest(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == 32
}

// ReconcileRejectedCreate is only a local journal transition under a held
// original parent. A scope-specific verifier must prove the exact historical
// rejection and current unstarted state. Native writes are never issued here.
func (p *Plan) ReconcileRejectedCreate(ctx context.Context, backend Backend, privateRoot, sourceSHA, resourceID string, evidence Object, verify func(context.Context) error) error {
	bad := func() error {
		return blocked("deployment.recovery.rejected-create", resourceID, "exact pre-send rejection, original write prefix and independent no-write checks required", "preserve the original journal and investigate the native request; absence alone cannot authorize replay")
	}
	parent, ok := backend.(ParentExecutionBackend)
	if p == nil || p.Operation != "scoped" || !filepath.IsAbs(privateRoot) || !validRejectedCreateDigest(sourceSHA) || !ok || verify == nil || len(evidence) == 0 {
		return bad()
	}
	if err := parent.CheckParentExecution(ctx, p, privateRoot); err != nil {
		return err
	}
	dir := filepath.Join(privateRoot, "deployment", "rollback", p.SHA256())
	for d := dir; ; d = filepath.Dir(d) {
		if err := privateDirectory(d); err != nil {
			return err
		}
		if d == privateRoot {
			break
		}
	}
	active := filepath.Join(dir, ".active")
	if os.Mkdir(active, 0700) != nil {
		return bad()
	}
	defer os.Remove(active)
	lock, err := LockExecutionOwner(active, true)
	if err != nil {
		return err
	}
	defer lock.Close()
	defer os.Remove(filepath.Join(active, "running.lock"))
	planRaw, err := readPrivateExecutionEvidence(filepath.Join(dir, "plan.json"))
	want, encodeErr := json.Marshal(p)
	if err != nil || encodeErr != nil || !bytes.Equal(planRaw, want) {
		return bad()
	}
	raw, err := readPrivateExecutionEvidence(filepath.Join(dir, "journal.json"))
	var j Journal
	if err != nil || rejectedCreateDigest(raw) != sourceSHA || json.Unmarshal(raw, &j) != nil {
		return bad()
	}
	canonical, err := json.Marshal(j)
	if err != nil || !bytes.Equal(raw, canonical) || j.PlanSHA256 != p.SHA256() || j.Accepted || j.AcceptanceStarted || j.InFlight != resourceID || j.Failed != resourceID || len(j.Completed) >= len(p.Resources) || len(j.Owners) != len(j.Completed) {
		return bad()
	}
	target := p.Resources[len(j.Completed)]
	if target.ID != resourceID || target.Action != "create" || target.UID != "" || target.Desired["kind"] != "Job" {
		return bad()
	}
	for i, r := range p.Resources {
		if i < len(j.Completed) {
			if j.Completed[i] != r.ID || j.Owners[r.ID] == "" {
				return bad()
			}
			current, err := backend.Read(ctx, r.ID)
			if err != nil {
				return err
			}
			if err := recordedResourceMatches(r, j.Owners[r.ID], current); err != nil {
				return err
			}
		} else if j.Owners[r.ID] != "" {
			return bad()
		}
	}
	if err := validateRejectedCreates(dir, p, j); err != nil {
		return err
	}
	if err := verify(ctx); err != nil {
		return err
	}
	if err := parent.CheckParentExecution(ctx, p, privateRoot); err != nil {
		return err
	}
	fresh, err := readPrivateExecutionEvidence(filepath.Join(dir, "journal.json"))
	currentPlan, encodeErr := json.Marshal(p)
	if err != nil || encodeErr != nil || !bytes.Equal(currentPlan, want) || !bytes.Equal(raw, fresh) || ctx.Err() != nil {
		return bad()
	}
	archive := filepath.Join(dir, "rejected-creates")
	if err := privateDirectory(archive); err != nil {
		return err
	}
	archive = filepath.Join(archive, sourceSHA)
	if err := privateDirectory(archive); err != nil {
		return err
	}
	if err := writeExclusiveJSON(filepath.Join(archive, "journal.json"), json.RawMessage(raw)); err != nil {
		return err
	}
	if err := writeExclusiveJSON(filepath.Join(archive, "evidence.json"), evidence); err != nil {
		return err
	}
	j.RejectedCreates = append(j.RejectedCreates, RejectedCreateReconciliation{resourceID, sourceSHA, Digest(evidence)})
	j.InFlight = ""
	return saveJournal(dir, j)
}

func validateRejectedCreates(dir string, p *Plan, j Journal) error {
	for i, receipt := range j.RejectedCreates {
		bad := func() error { return existingExecutionEvidenceBlocker() }
		if !validRejectedCreateDigest(receipt.SourceJournalSHA256) || !validRejectedCreateDigest(receipt.EvidenceSHA256) {
			return bad()
		}
		archive := filepath.Join(dir, "rejected-creates", receipt.SourceJournalSHA256)
		for _, d := range []string{filepath.Dir(archive), archive} {
			info, err := os.Lstat(d)
			if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
				return bad()
			}
		}
		raw, err := readPrivateExecutionEvidence(filepath.Join(archive, "journal.json"))
		var source Journal
		if err != nil || rejectedCreateDigest(raw) != receipt.SourceJournalSHA256 || json.Unmarshal(raw, &source) != nil || source.PlanSHA256 != p.SHA256() || source.Accepted || source.AcceptanceStarted || source.InFlight != receipt.ResourceID || source.Failed != receipt.ResourceID || source.Owners[receipt.ResourceID] != "" || len(source.RejectedCreates) != i || i > 0 && Digest(source.RejectedCreates) != Digest(j.RejectedCreates[:i]) || len(source.Completed) > len(j.Completed) {
			return bad()
		}
		for index, id := range source.Completed {
			if j.Completed[index] != id || j.Owners[id] != source.Owners[id] {
				return bad()
			}
		}
		raw, err = readPrivateExecutionEvidence(filepath.Join(archive, "evidence.json"))
		if err != nil || rejectedCreateDigest(raw) != receipt.EvidenceSHA256 {
			return bad()
		}
	}
	return nil
}
