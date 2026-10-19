package deploymentplan

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// ReadExecution returns only the literal original plan and a known write
// prefix. It does not infer a successful write from the current resource.
func (p *Plan) ReadExecution(privateRoot string) (Journal, bool, error) {
	e, err := p.resumableEvidence(privateRoot)
	if err != nil || e == nil {
		return Journal{}, false, err
	}
	return e.journal, true, nil
}

// AdmitResumable is the read-only qualification counterpart of execution. It
// verifies every recorded owner and admits only the original unstarted writes.
func (p *Plan) AdmitResumable(ctx context.Context, backend Backend, privateRoot string) error {
	prior, err := p.resumableEvidence(privateRoot)
	if err != nil {
		return err
	}
	if prior == nil {
		return p.Admit(ctx, backend)
	}
	if err := p.verifyRecorded(ctx, backend, prior.journal); err != nil {
		return err
	}
	return p.recordedTarget(prior.journal).Admit(ctx, backend)
}

func (p *Plan) resumableEvidence(privateRoot string) (*retainedExecutionEvidence, error) {
	if p == nil || p.Version != Version || len(p.Resources) == 0 || !filepath.IsAbs(privateRoot) {
		return nil, existingExecutionEvidenceBlocker()
	}
	directory := filepath.Join(privateRoot, "deployment", "rollback", p.SHA256())
	for dir := directory; ; dir = filepath.Dir(dir) {
		info, err := os.Lstat(dir)
		if err == nil && (!info.IsDir() || info.Mode().Perm() != 0700) || err != nil && !errors.Is(err, os.ErrNotExist) {
			return nil, existingExecutionEvidenceBlocker()
		}
		if dir == privateRoot {
			break
		}
	}
	_, pe := os.Lstat(filepath.Join(directory, "plan.json"))
	_, je := os.Lstat(filepath.Join(directory, "journal.json"))
	if errors.Is(pe, os.ErrNotExist) && errors.Is(je, os.ErrNotExist) {
		return nil, nil
	}
	planRaw, err := readPrivateExecutionEvidence(filepath.Join(directory, "plan.json"))
	want, encodeErr := json.Marshal(p)
	if err != nil || encodeErr != nil || !bytes.Equal(planRaw, want) {
		return nil, existingExecutionEvidenceBlocker()
	}
	journalRaw, err := readPrivateExecutionEvidence(filepath.Join(directory, "journal.json"))
	var j Journal
	if err != nil || json.Unmarshal(journalRaw, &j) != nil {
		return nil, existingExecutionEvidenceBlocker()
	}
	canonical, err := json.Marshal(j)
	if err != nil || !bytes.Equal(journalRaw, canonical) || j.PlanSHA256 != p.SHA256() || j.InFlight != "" || j.Owners == nil || len(j.Completed) > len(p.Resources) || len(j.Owners) < len(j.Completed) || len(j.Owners) > min(len(p.Resources), len(j.Completed)+1) || j.AcceptanceStarted && len(j.Completed) != len(p.Resources) || j.Accepted && (!j.AcceptanceStarted || j.Failed != "") {
		return nil, existingExecutionEvidenceBlocker()
	}
	for index, resource := range p.Resources {
		owner, recorded := j.Owners[resource.ID]
		if index < len(j.Completed) && (j.Completed[index] != resource.ID || !recorded) || recorded && (index > len(j.Completed) || owner == "" || resource.UID != "" && owner != resource.UID) || index == len(j.Completed) && recorded && resource.Action == "retain" {
			return nil, existingExecutionEvidenceBlocker()
		}
	}
	// Unknown owner IDs cannot be smuggled into an otherwise valid prefix.
	known := 0
	for _, resource := range p.Resources {
		if _, ok := j.Owners[resource.ID]; ok {
			known++
		}
	}
	if known != len(j.Owners) {
		return nil, existingExecutionEvidenceBlocker()
	}
	return &retainedExecutionEvidence{planRaw: planRaw, journalRaw: journalRaw, journal: j}, nil
}

func (p *Plan) recordedTarget(j Journal) *Plan {
	copy := *p
	copy.Resources = nil
	for _, r := range p.Resources {
		if owner := j.Owners[r.ID]; owner != "" {
			continue // verified separately; do not admit another create/update/delete
		}
		copy.Resources = append(copy.Resources, r)
	}
	return &copy
}

func recordedResourceMatches(r Resource, owner string, current Object) error {
	if r.Action == "retire" {
		if current == nil {
			return nil
		}
	} else if current != nil && owner != "" && String(Map(current["metadata"])["uid"]) == owner && (r.UID == "" || r.UID == owner) && Matches(r.Desired, current) {
		return nil
	}
	return blocked("deployment.drift", r.ID, "a recorded write owner or desired body changed", "preserve the original execution journal; never adopt or recreate the owner")
}

func (p *Plan) verifyRecorded(ctx context.Context, backend Backend, j Journal) error {
	for index, r := range p.Resources {
		owner := j.Owners[r.ID]
		if owner == "" {
			continue
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		current, err := backend.Read(ctx, r.ID)
		if err != nil {
			return err
		}
		knownRetirement := r.Action == "retire" && index == len(j.Completed)
		if knownRetirement {
			if err := recordedRetirementResponseMatches(r, owner, current); err != nil {
				return err
			}
		} else if err := recordedResourceMatches(r, owner, current); err != nil {
			return err
		}
		if deferredWorkloadReadiness(r) {
			_, end := p.workloadReadinessBlock(index)
			if len(j.Completed) < end {
				continue // known owner/body is strict; its peers are not all written yet
			}
			if err := waitRecordedWorkloadReadiness(ctx, backend, r, owner, current, nil); err != nil {
				return err
			}
		} else if err := backend.Wait(ctx, r); err != nil {
			return err
		}
		if knownRetirement {
			current, err := backend.Read(ctx, r.ID)
			if err != nil {
				return err
			}
			if current != nil {
				return blocked("deployment.retirement", r.ID, "recorded retirement has not become absent", "wait for the original deleting owner; never issue another delete or adopt a replacement")
			}
		}
	}
	return nil
}

// A successful DELETE response is journaled before its absence wait. Resuming
// that exact next owner only waits and verifies; it never repeats the DELETE.
func recordedRetirementResponseMatches(r Resource, owner string, current Object) error {
	bad := func() error {
		return blocked("deployment.drift", r.ID, "recorded retirement owner or terminating body changed", "preserve the original successful response; never repeat the delete or adopt a replacement")
	}
	if r.Action != "retire" || owner == "" || r.UID != owner {
		return bad()
	}
	if current == nil {
		return nil
	}
	meta := Map(current["metadata"])
	if String(meta["uid"]) != owner {
		return bad()
	}
	if at, err := time.Parse(time.RFC3339Nano, String(meta["deletionTimestamp"])); err != nil || at.IsZero() {
		return bad()
	}
	// The foreground delete adds only these API deletion markers. All other
	// metadata and the complete original body remain the frozen baseline.
	observed := Clone(current)
	baseline := Clone(r.Baseline)
	for _, object := range []Object{observed, baseline} {
		m := Map(object["metadata"])
		delete(m, "deletionTimestamp")
		delete(m, "deletionGracePeriodSeconds")
		if finalizers, ok := m["finalizers"].([]any); ok {
			remaining := make([]any, 0, len(finalizers))
			for _, finalizer := range finalizers {
				if finalizer != "foregroundDeletion" {
					remaining = append(remaining, finalizer)
				}
			}
			if len(remaining) == 0 {
				delete(m, "finalizers")
			} else {
				m["finalizers"] = remaining
			}
		}
	}
	copy := r
	copy.Baseline = baseline
	if _, err := copy.Body(observed); err != nil {
		return bad()
	}
	return nil
}
