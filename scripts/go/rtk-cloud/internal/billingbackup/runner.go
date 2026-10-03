package billingbackup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/hkt999rtk/rtk_cloud_logger/billingarchive"
)

type ManifestLister interface {
	ObjectStore
	ListManifests(context.Context, string) ([]string, error)
}

// A verification receipt alone does not prove Logger accepted it. This journal
// is written only after confirmed registration; an ambiguous POST is retried.
type archiveRegistration struct {
	Prefix        string                          `json:"prefix"`
	ManifestBytes []byte                          `json:"manifest_bytes"`
	Completion    billingarchive.SignedCompletion `json:"completion"`
}

func registeredArchive(e Engine, prefix string) (Verified, bool, error) {
	var out Verified
	id := filepath.Base(prefix)
	if !billingarchive.SafeID(id) {
		return out, false, errors.New("invalid registered archive identity")
	}
	file := filepath.Join(e.Config.Directory, id+".registered.json")
	if _, err := os.Lstat(file); os.IsNotExist(err) {
		return out, false, nil
	} else if err != nil {
		return out, false, err
	}
	var saved archiveRegistration
	if err := readPrivateJSONLimit(file, &saved, 2*billingarchive.MaxManifestBytes); err != nil {
		return out, false, err
	}
	if saved.Prefix != prefix {
		return out, false, errors.New("registered archive prefix mismatch")
	}
	m, err := e.validateManifest(prefix, saved.ManifestBytes)
	if err != nil {
		return out, false, err
	}
	keys, _ := e.Config.PublicKeys()
	if _, err = verifyCompletionNow(saved.Completion, m, saved.ManifestBytes, keys); err != nil {
		return out, false, err
	}
	return Verified{m, saved.Completion, prefix}, true, nil
}

func recordProtection(status *ControllerStatus, m billingarchive.Manifest) {
	if m.CreatedAt.After(status.VerifiedHorizon) {
		status.VerifiedHorizon = m.CreatedAt
		status.ProtectedHighWater = m.HighWater
	}
}

// submitSavedPlan reads the authoritative decision first. Only an authenticated
// 404 permits re-submission; a timeout never releases an ACTIVE fence. Financial
// policy rotation cannot rewrite the saved immutable intent.
func submitSavedPlan(ctx context.Context, e Engine, local, authority Client, p retirementPlan) error {
	if p.Environment != e.Config.Environment || p.StoreID != e.Config.StoreID || p.PlanSHA256 != planDigest(p) || !billingarchive.SafeID(p.OperationID) {
		return errors.New("saved retirement intent scope or digest mismatch")
	}
	var state struct {
		retirementPlan
		Status string `json:"status"`
	}
	path := "/v1/internal/billing/raw-retention/operations/" + p.OperationID
	err := authority.Request(ctx, http.MethodGet, path, nil, &state)
	if err != nil {
		var rejected *HTTPError
		if !errors.As(err, &rejected) || rejected.Status != http.StatusNotFound {
			return err
		}
		keys, _ := e.Config.PublicKeys()
		// Archive completion does not carry CreatedAt. The independently saved
		// verification receipt is the authoritative local manifest location.
		var verified Verified
		if err = readPrivateJSON(filepath.Join(e.Config.Directory, p.SetID+".verified.json"), &verified); err != nil {
			return err
		}
		if verified.Manifest.SetID != p.SetID || verified.Manifest.StoreID != p.StoreID || verified.Manifest.Environment != p.Environment {
			return errors.New("saved archive receipt scope mismatch")
		}
		records, refs, m, err := e.ReadRange(ctx, verified.Prefix, p.FromSequence, p.ThroughSequence)
		if err != nil {
			return err
		}
		if billingarchive.BindingsDigest(refs) != billingarchive.BindingsDigest(p.Records) {
			return errors.New("saved retirement bindings changed")
		}
		_, raw, err := e.readManifest(ctx, verified.Prefix)
		if err != nil || billingarchive.Digest(raw) != p.ArchiveManifestSHA256 {
			return errors.New("saved retirement manifest changed")
		}
		if _, err = billingarchive.VerifyCompletion(p.ArchiveCompletion, m, raw, keys); err != nil {
			return err
		}
		proofPayload, err := billingarchive.VerifyRangeProof(p.RangeProof, keys)
		if err != nil {
			return err
		}
		proofPayload.VerifiedAt = time.Now().UTC()
		proofPayload.PolicyVersion = strconv.FormatInt(p.PolicyVersion, 10)
		proofPayload.VerifierKeyID = e.Config.VerifierKeyID
		proofPayload.VerifierVersion = "rtk-cloud-billing-verifier-v1"
		var maxTime time.Time
		for _, record := range records {
			if record.ReceivedAt.After(maxTime) {
				maxTime = record.ReceivedAt
			}
		}
		if proofPayload.Environment != p.Environment || proofPayload.StoreID != p.StoreID || proofPayload.SetID != p.SetID || proofPayload.FromSequence != p.FromSequence || proofPayload.ThroughSequence != p.ThroughSequence || proofPayload.ManifestSHA256 != p.ArchiveManifestSHA256 || proofPayload.RecordsBindingsSHA256 != billingarchive.BindingsDigest(refs) || !proofPayload.MaxReceivedAt.Equal(maxTime) {
			return errors.New("saved range proof does not bind retirement intent")
		}
		if proofPayload.MaxReceivedAt.After(time.Now().UTC().Add(-90*24*time.Hour)) || proofPayload.HighWater != m.HighWater {
			return errors.New("saved retirement age or horizon mismatch")
		}
		p.RangeProof, err = billingarchive.SignRangeProof(proofPayload, e.Signer)
		if err != nil {
			return err
		}
		var result json.RawMessage
		if err = authority.Request(ctx, http.MethodPost, "/v1/internal/billing/raw-retention/operations", p, &result); err != nil {
			return err
		}
	} else if state.OperationID != p.OperationID || state.PlanSHA256 != p.PlanSHA256 {
		return errors.New("authoritative retirement decision mismatch")
	}
	if err = ResumeRetirement(ctx, e.Config, local, authority, p.OperationID, false); err != nil {
		return err
	}
	var terminal json.RawMessage
	if err = authority.Request(ctx, http.MethodGet, path, nil, &terminal); err != nil {
		return err
	}
	if err = json.Unmarshal(terminal, &state); err != nil {
		return errors.New("invalid authoritative terminal response")
	}
	if state.OperationID != p.OperationID || state.PlanSHA256 != p.PlanSHA256 || (state.Status != "COMPLETED" && state.Status != "ABORTED") {
		return errors.New("retirement result remains unresolved")
	}
	return atomicJSON(filepath.Join(e.Config.Directory, p.OperationID+".terminal.json"), terminal)
}

func readPrivateJSON(file string, result any) error {
	return readPrivateJSONLimit(file, result, 4<<20)
}

func readPrivateJSONLimit(file string, result any, limit int64) error {
	i, err := os.Lstat(file)
	if err != nil || !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 {
		return errors.New("private controller journal unavailable")
	}
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	return billingarchive.StrictDecode(f, limit, result)
}

func resumePending(ctx context.Context, e Engine, local, authority Client) (int, error) {
	files, err := os.ReadDir(e.Config.Directory)
	if err != nil {
		return 0, err
	}
	var n int
	for _, file := range files {
		if !strings.HasSuffix(file.Name(), ".plan.json") {
			continue
		}
		id := strings.TrimSuffix(file.Name(), ".plan.json")
		if !billingarchive.SafeID(id) {
			return n, errors.New("invalid controller journal identity")
		}
		var p retirementPlan
		if err = readPrivateJSON(filepath.Join(e.Config.Directory, file.Name()), &p); err != nil {
			return n, err
		}
		if p.OperationID != id || p.Environment != e.Config.Environment || p.StoreID != e.Config.StoreID || p.PlanSHA256 != planDigest(p) {
			return n, errors.New("controller journal identity mismatch")
		}
		terminalPath := filepath.Join(e.Config.Directory, id+".terminal.json")
		if _, err := os.Lstat(terminalPath); err == nil {
			var raw json.RawMessage
			if err := readPrivateJSON(terminalPath, &raw); err != nil {
				return n, err
			}
			var terminal struct {
				retirementPlan
				Status string `json:"status"`
			}
			if json.Unmarshal(raw, &terminal) != nil || terminal.OperationID != id || terminal.Environment != p.Environment || terminal.StoreID != p.StoreID || terminal.PlanSHA256 != p.PlanSHA256 || planDigest(terminal.retirementPlan) != p.PlanSHA256 || (terminal.Status != "COMPLETED" && terminal.Status != "ABORTED") {
				return n, errors.New("controller terminal journal is invalid or mismatched")
			}
			continue
		} else if !os.IsNotExist(err) {
			return n, err
		}
		if err = submitSavedPlan(ctx, e, local, authority, p); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

func resumeAuthorityPending(ctx context.Context, e Engine, local, authority Client) error {
	query := url.Values{"environment": {e.Config.Environment}, "store_id": {e.Config.StoreID}}
	var listed struct {
		Operations []struct {
			retirementPlan
			Status string `json:"status"`
		} `json:"operations"`
	}
	if err := authority.Request(ctx, http.MethodGet, "/v1/internal/billing/raw-retention/operations?"+query.Encode(), nil, &listed); err != nil {
		return err
	}
	if len(listed.Operations) > 1000 {
		return errors.New("authority operation listing exceeds bound")
	}
	for _, op := range listed.Operations {
		if !billingarchive.SafeID(op.OperationID) || op.Environment != e.Config.Environment || op.StoreID != e.Config.StoreID || op.PlanSHA256 != planDigest(op.retirementPlan) {
			return errors.New("authority pending decision scope mismatch")
		}
		if op.Status == "COMPLETED" || op.Status == "ABORTED" {
			continue
		}
		if op.Status != "ACTIVE" && op.Status != "ABORT_REQUESTED" {
			return errors.New("unknown authority fence state")
		}
		if err := atomicJSON(filepath.Join(e.Config.Directory, op.OperationID+".plan.json"), op.retirementPlan); err != nil {
			// A matching stable intent may have a refreshed proof in the authority;
			// preserve its original local bytes instead of rewriting the journal.
			var saved retirementPlan
			if readPrivateJSON(filepath.Join(e.Config.Directory, op.OperationID+".plan.json"), &saved) != nil || saved.PlanSHA256 != op.PlanSHA256 {
				return err
			}
		}
		if err := submitSavedPlan(ctx, e, local, authority, op.retirementPlan); err != nil {
			return err
		}
	}
	return nil
}

// AbortSavedIntent can also cancel a durably journaled request that Billing has
// not accepted. Billing serializes that permanent before-acceptance tombstone
// against an in-flight creation; no Logger terminal/fence is invented locally.
func AbortSavedIntent(ctx context.Context, cfg Config, local, authority Client, id string) error {
	if !billingarchive.SafeID(id) {
		return errors.New("invalid retirement operation ID")
	}
	var p retirementPlan
	if err := readPrivateJSON(filepath.Join(cfg.Directory, id+".plan.json"), &p); err != nil {
		return err
	}
	if p.OperationID != id || p.Environment != cfg.Environment || p.StoreID != cfg.StoreID || p.PlanSHA256 != planDigest(p) {
		return errors.New("saved cancellation scope mismatch")
	}
	var result json.RawMessage
	if err := authority.Request(ctx, http.MethodPost, "/v1/internal/billing/raw-retention/operations/"+id+"/abort", p, &result); err != nil {
		return err
	}
	if err := ResumeRetirement(ctx, cfg, local, authority, id, true); err != nil {
		return err
	}
	var state struct {
		retirementPlan
		Status         string `json:"status"`
		DecisionOrigin string `json:"decision_origin,omitempty"`
	}
	var terminal json.RawMessage
	if err := authority.Request(ctx, http.MethodGet, "/v1/internal/billing/raw-retention/operations/"+id, nil, &terminal); err != nil {
		return err
	}
	if err := json.Unmarshal(terminal, &state); err != nil {
		return errors.New("invalid authoritative cancellation response")
	}
	if state.PlanSHA256 != p.PlanSHA256 || state.OperationID != id || (state.Status != "ABORTED" && state.Status != "COMPLETED") {
		return errors.New("cancellation remains unresolved")
	}
	return atomicJSON(filepath.Join(cfg.Directory, id+".terminal.json"), terminal)
}

type ControllerStatus struct {
	Environment        string    `json:"environment"`
	StoreID            string    `json:"store_id"`
	UpdatedAt          time.Time `json:"updated_at"`
	LastCapture        time.Time `json:"last_capture"`
	VerifiedHorizon    time.Time `json:"verified_horizon"`
	ProtectedHighWater uint64    `json:"protected_high_water,string"`
	LastCompaction     time.Time `json:"last_compaction"`
	ProtectionOverdue  bool      `json:"protection_overdue"`
	RetirementBlocked  string    `json:"retirement_blocked,omitempty"`
	Errors             []string  `json:"errors,omitempty"`
}

// Run is installed explicitly on the isolated recovery host. It never installs
// a scheduler or enables an environment. Its durable status is consumed by the
// operator monitor; errors are retried without skipping a retirement prefix.
func Run(ctx context.Context, e Engine, objects ManifestLister, local, authority Client, interval time.Duration) error {
	if interval <= 0 || interval > 12*time.Hour {
		return errors.New("capture interval must be at most twelve hours")
	}
	if err := e.Config.Validate(); err != nil {
		return err
	}
	if err := privateDirectory(e.Config.Directory); err != nil {
		return err
	}
	lock, err := os.OpenFile(filepath.Join(e.Config.Directory, "controller.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		return errors.New("controller already running")
	}
	defer syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
	status := ControllerStatus{Environment: e.Config.Environment, StoreID: e.Config.StoreID}
	// Do not trust a previous process's health, but retain cadence timestamps.
	var saved ControllerStatus
	if err := readPrivateJSON(filepath.Join(e.Config.Directory, "controller-status.json"), &saved); err == nil && saved.Environment == status.Environment && saved.StoreID == status.StoreID {
		status.LastCapture, status.LastCompaction = saved.LastCapture, saved.LastCompaction
	}
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		status.Errors = nil
		status.RetirementBlocked = ""
		if time.Since(status.LastCapture) >= interval {
			var result json.RawMessage
			if err := local.Request(ctx, http.MethodPost, "/v1/internal/billing-lifecycle/capture", map[string]int{}, &result); err != nil {
				status.Errors = append(status.Errors, "capture: "+err.Error())
			} else {
				status.LastCapture = time.Now().UTC()
			}
		}
		if e.Config.AutoRetirement {
			if _, err := resumePending(ctx, e, local, authority); err != nil {
				status.RetirementBlocked = err.Error()
			} else if err := resumeAuthorityPending(ctx, e, local, authority); err != nil {
				status.RetirementBlocked = err.Error()
			}
		}
		prefixes, err := objects.ListManifests(ctx, "billing-inbox-snapshots/"+e.Config.Stack+"/"+e.Config.StoreID+"/")
		var archives []Verified
		if err != nil {
			status.Errors = append(status.Errors, "catalog: "+err.Error())
		}
		// Newest manifests first for protection; retirement below is oldest first.
		sort.Sort(sort.Reverse(sort.StringSlice(prefixes)))
		for _, prefix := range prefixes {
			// Revalidate the private durable acknowledgement and signature locally.
			// Historical sets need no S3 GET or Logger mutation on minute ticks or
			// process restart. Retirement still independently reads remote bytes.
			registered, found, err := registeredArchive(e, prefix)
			if err != nil {
				status.Errors = append(status.Errors, "registration journal: "+err.Error())
				continue
			}
			if found {
				recordProtection(&status, registered.Manifest)
				archives = append(archives, registered)
				continue
			}
			m, raw, err := e.readManifest(ctx, prefix)
			if err != nil {
				status.Errors = append(status.Errors, "manifest: "+err.Error())
				continue
			}
			var complete billingarchive.SignedCompletion
			r, err := objects.Read(ctx, prefix+"/complete.json")
			if err == nil {
				err = billingarchive.StrictDecode(r, 32768, &complete)
				r.Close()
			} else if errors.Is(err, ErrObjectMissing) {
				var verified Verified
				verified, err = e.Verify(ctx, prefix)
				complete = verified.Completion
			}
			if err != nil {
				status.Errors = append(status.Errors, "verification: "+err.Error())
				continue
			}
			keys, _ := e.Config.PublicKeys()
			if _, err = verifyCompletionNow(complete, m, raw, keys); err != nil {
				status.Errors = append(status.Errors, "receipt: "+err.Error())
				continue
			}
			var result json.RawMessage
			if err = local.Request(ctx, http.MethodPost, "/v1/internal/billing-lifecycle/verify", map[string]any{"set_id": m.SetID, "completion": complete}, &result); err != nil {
				status.Errors = append(status.Errors, "catalog registration: "+err.Error())
				continue
			}
			if err = atomicJSON(filepath.Join(e.Config.Directory, m.SetID+".verified.json"), Verified{m, complete, prefix}); err != nil {
				status.Errors = append(status.Errors, "receipt journal: "+err.Error())
				continue
			}
			if err = atomicJSON(filepath.Join(e.Config.Directory, m.SetID+".registered.json"), archiveRegistration{prefix, raw, complete}); err != nil {
				status.Errors = append(status.Errors, "registration journal: "+err.Error())
				continue
			}
			recordProtection(&status, m)
			archives = append(archives, Verified{m, complete, prefix})
		}
		status.ProtectionOverdue = status.VerifiedHorizon.IsZero() || time.Since(status.VerifiedHorizon) > 24*time.Hour
		if e.Config.AutoRetirement && status.RetirementBlocked == "" {
			sort.Slice(archives, func(i, j int) bool { return archives[i].Manifest.CreatedAt.Before(archives[j].Manifest.CreatedAt) })
			var floor struct {
				Environment    string  `json:"environment"`
				StoreID        string  `json:"store_id"`
				RetiredThrough *uint64 `json:"retired_through,string"`
			}
			if err = local.Request(ctx, http.MethodGet, "/v1/internal/billing-lifecycle/status", nil, &floor); err != nil {
				status.RetirementBlocked = err.Error()
			} else if floor.RetiredThrough == nil || floor.StoreID != e.Config.StoreID || floor.Environment != e.Config.Environment {
				status.RetirementBlocked = "Logger status lacks the qualified retirement frontier"
			} else {
				for _, archive := range archives {
					from := *floor.RetiredThrough + 1
					if from == 0 || from < archive.Manifest.FromSequence || from > archive.Manifest.ThroughSequence {
						continue
					}
					through := from + 999
					if through < from || through > archive.Manifest.ThroughSequence {
						through = archive.Manifest.ThroughSequence
					}
					records, _, _, readErr := e.ReadRange(ctx, archive.Prefix, from, through)
					if readErr != nil {
						status.RetirementBlocked = readErr.Error()
						break
					}
					through = eligiblePrefix(records, time.Now().UTC().Add(-time.Duration(e.Config.HotDays())*24*time.Hour))
					if through < from {
						status.RetirementBlocked = "first receipt has not reached the approved minimum hot age"
						break
					}
					if err = RetireRange(ctx, e, local, authority, archive.Prefix, from, through); err != nil {
						status.RetirementBlocked = err.Error()
					}
					break // never skip a failed or ambiguous prefix
				}
			}
		}
		if e.Config.AutoCompaction && time.Since(status.LastCompaction) >= interval {
			var result json.RawMessage
			if err = local.Request(ctx, http.MethodPost, "/v1/internal/billing-lifecycle/compact", map[string]int{}, &result); err != nil {
				status.Errors = append(status.Errors, "compaction deferred: "+err.Error())
			} else {
				status.LastCompaction = time.Now().UTC()
			}
		}
		status.UpdatedAt = time.Now().UTC()
		if err = replaceStatus(filepath.Join(e.Config.Directory, "controller-status.json"), status); err != nil {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func eligiblePrefix(records []billingarchive.ExportRecord, cutoff time.Time) uint64 {
	var through uint64
	for _, record := range records {
		if record.ReceivedAt.IsZero() || record.ReceivedAt.After(cutoff) {
			break
		}
		through = record.Sequence
	}
	return through
}

func replaceStatus(file string, value any) error {
	b, err := json.Marshal(value)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(file), ".status-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(b)
	}
	if err == nil {
		err = f.Sync()
	}
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), file); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(file))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
