package billingbackup

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	logger "github.com/hkt999rtk/rtk_cloud_logger"
	"github.com/hkt999rtk/rtk_cloud_logger/billingarchive"
)

func (e Engine) ReadRange(ctx context.Context, prefix string, from, through uint64) ([]billingarchive.ExportRecord, []billingarchive.RecordBinding, billingarchive.Manifest, error) {
	var m billingarchive.Manifest
	if err := e.Config.Validate(); err != nil {
		return nil, nil, m, err
	}
	if e.Objects == nil || len(e.Identities) == 0 {
		return nil, nil, m, errors.New("isolated archive reader dependencies required")
	}
	if from == 0 || through < from || through-from >= 1000 {
		return nil, nil, m, errors.New("range must contain 1..1000 consecutive receipts")
	}
	m, raw, err := e.readManifest(ctx, prefix)
	if err != nil {
		return nil, nil, m, err
	}
	if m.FromSequence > from || m.ThroughSequence < through {
		return nil, nil, m, errors.New("requested range outside archive")
	}
	r, err := e.Objects.Read(ctx, prefix+"/complete.json")
	if err != nil {
		return nil, nil, m, err
	}
	var c billingarchive.SignedCompletion
	err = billingarchive.StrictDecode(r, 32768, &c)
	r.Close()
	if err != nil {
		return nil, nil, m, err
	}
	keys, err := e.Config.PublicKeys()
	if err != nil {
		return nil, nil, m, err
	}
	if _, err = verifyCompletionNow(c, m, raw, keys); err != nil {
		return nil, nil, m, err
	}
	if err = privateDirectory(e.Config.Directory); err != nil {
		return nil, nil, m, err
	}
	if err = checkScratch(e.Config.Directory, m, true); err != nil {
		return nil, nil, m, err
	}
	work, err := os.MkdirTemp(e.Config.Directory, "range-")
	if err != nil {
		return nil, nil, m, err
	}
	defer os.RemoveAll(work)
	file, err := os.OpenFile(filepath.Join(work, "events.ndjson"), os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return nil, nil, m, err
	}
	defer file.Close()
	for _, part := range m.Parts {
		if part.Kind != "events" {
			continue
		}
		r, err := e.Objects.Read(ctx, Prefix(m, part.Kind)+"/"+part.Name)
		if err != nil {
			return nil, nil, m, err
		}
		err = billingarchive.DecodePart(ctx, file, r, e.Identities, part)
		r.Close()
		if err != nil {
			return nil, nil, m, err
		}
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, nil, m, err
	}
	count, maxTime, digest, err := validateEvents(ctx, file, m)
	if err != nil {
		return nil, nil, m, err
	}
	if count != m.RecordCount || !maxTime.Equal(m.MaxReceivedAt) || digest != m.RecordsBindingsSHA256 {
		return nil, nil, m, errors.New("archive raw coverage mismatch")
	}
	if _, err = file.Seek(0, io.SeekStart); err != nil {
		return nil, nil, m, err
	}
	records := make([]billingarchive.ExportRecord, 0, through-from+1)
	refs := make([]billingarchive.RecordBinding, 0, through-from+1)
	scan := bufio.NewScanner(file)
	scan.Buffer(make([]byte, 64<<10), 2<<20)
	for scan.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, nil, m, err
		}
		var export billingarchive.ExportRecord
		if err = billingarchive.StrictDecode(strings.NewReader(scan.Text()), 2<<20, &export); err != nil {
			return nil, nil, m, err
		}
		if export.Sequence < from || export.Sequence > through {
			continue
		}
		record, err := logger.ValidateBillingExportRecord(export)
		if err != nil {
			return nil, nil, m, err
		}
		usage, err := json.Marshal(record.Event.Fields["usage_event"])
		if err != nil {
			return nil, nil, m, err
		}
		ref, err := billingarchive.CanonicalUsageBinding(export.Sequence, export.ContentSHA256, usage)
		if err != nil {
			return nil, nil, m, err
		}
		records = append(records, export)
		refs = append(refs, ref)
	}
	if err = scan.Err(); err != nil {
		return nil, nil, m, err
	}
	if uint64(len(records)) != through-from+1 {
		return nil, nil, m, errors.New("requested archive range incomplete")
	}
	return records, refs, m, nil
}

type retirementPlan struct {
	OperationID           string                          `json:"operation_id"`
	PolicyID              string                          `json:"policy_id"`
	PolicyVersion         int64                           `json:"policy_version"`
	ClearanceID           string                          `json:"clearance_id"`
	Environment           string                          `json:"environment"`
	StoreID               string                          `json:"store_id"`
	FromSequence          uint64                          `json:"from_sequence,string"`
	ThroughSequence       uint64                          `json:"through_sequence,string"`
	SetID                 string                          `json:"set_id"`
	PlanSHA256            string                          `json:"plan_sha256"`
	ArchiveManifestSHA256 string                          `json:"archive_manifest_sha256"`
	ArchiveCompletion     billingarchive.SignedCompletion `json:"archive_completion"`
	RangeProof            billingarchive.SignedCompletion `json:"range_proof"`
	Records               []billingarchive.RecordBinding  `json:"records"`
}

func planDigest(p retirementPlan) string {
	core := struct {
		OperationID           string `json:"operation_id"`
		PolicyID              string `json:"policy_id"`
		PolicyVersion         int64  `json:"policy_version"`
		ClearanceID           string `json:"clearance_id"`
		Environment           string `json:"environment"`
		StoreID               string `json:"store_id"`
		FromSequence          uint64 `json:"from_sequence,string"`
		ThroughSequence       uint64 `json:"through_sequence,string"`
		SetID                 string `json:"set_id"`
		ArchiveManifestSHA256 string `json:"archive_manifest_sha256"`
		RecordsBindingsSHA256 string `json:"records_bindings_sha256"`
	}{p.OperationID, p.PolicyID, p.PolicyVersion, p.ClearanceID, p.Environment, p.StoreID, p.FromSequence, p.ThroughSequence, p.SetID, p.ArchiveManifestSHA256, billingarchive.BindingsDigest(p.Records)}
	b, _ := json.Marshal(core)
	return billingarchive.Digest(append([]byte("rtk-billing-raw-retirement-plan-v1\x00"), b...))
}

func RetireRange(ctx context.Context, e Engine, local, authority Client, prefix string, from, through uint64) error {
	if !e.Config.AutoRetirement {
		return errors.New("retirement disabled in approved controller configuration")
	}
	// Never allocate another intent while a previously written intent has an
	// ambiguous result. Its exact ID survives the request, process and host retry.
	if resumed, err := resumePending(ctx, e, local, authority); err != nil || resumed != 0 {
		return err
	}
	records, refs, m, err := e.ReadRange(ctx, prefix, from, through)
	if err != nil {
		return err
	}
	_, manifestBytes, err := e.readManifest(ctx, prefix)
	if err != nil {
		return err
	}
	r, err := e.Objects.Read(ctx, prefix+"/complete.json")
	if err != nil {
		return err
	}
	var complete billingarchive.SignedCompletion
	err = billingarchive.StrictDecode(r, 32768, &complete)
	r.Close()
	if err != nil {
		return err
	}
	var maxTime time.Time
	for _, r := range records {
		if r.ReceivedAt.After(maxTime) {
			maxTime = r.ReceivedAt
		}
	}
	if maxTime.After(time.Now().UTC().Add(-time.Duration(e.Config.HotDays()) * 24 * time.Hour)) {
		return errors.New("receipt has not reached minimum hot age")
	}
	rangeProof, err := billingarchive.SignRangeProof(billingarchive.RangeProof{Version: 1, Purpose: "billing-raw-reconciliation", VerifierKeyID: e.Config.VerifierKeyID, Environment: m.Environment, Stack: m.Stack, StoreID: m.StoreID, SetID: m.SetID, ManifestSHA256: billingarchive.Digest(manifestBytes), HighWater: m.HighWater, FromSequence: from, ThroughSequence: through, RecordCount: uint64(len(records)), MaxReceivedAt: maxTime, RecordsBindingsSHA256: billingarchive.BindingsDigest(refs), VerifiedAt: time.Now().UTC(), PolicyVersion: strconv.Itoa(e.Config.PolicyVersion), VerifierVersion: "rtk-cloud-billing-verifier-v1"}, e.Signer)
	if err != nil {
		return err
	}
	idBytes := make([]byte, 16)
	if _, err = rand.Read(idBytes); err != nil {
		return err
	}
	p := retirementPlan{OperationID: "retire-" + hex.EncodeToString(idBytes), PolicyID: e.Config.PolicyID, PolicyVersion: int64(e.Config.PolicyVersion), ClearanceID: e.Config.ClearanceID, Environment: m.Environment, StoreID: m.StoreID, FromSequence: from, ThroughSequence: through, SetID: m.SetID, ArchiveManifestSHA256: billingarchive.Digest(manifestBytes), ArchiveCompletion: complete, RangeProof: rangeProof, Records: refs}
	p.PlanSHA256 = planDigest(p)
	if err = atomicJSON(filepath.Join(e.Config.Directory, p.OperationID+".plan.json"), p); err != nil {
		return err
	}
	return submitSavedPlan(ctx, e, local, authority, p)
}

func ResumeRetirement(ctx context.Context, cfg Config, local, authority Client, id string, abort bool) error {
	if !billingarchive.SafeID(id) {
		return errors.New("invalid retirement operation ID")
	}
	var state struct {
		retirementPlan
		Status string `json:"status"`
	}
	if err := authority.Request(ctx, http.MethodGet, "/v1/internal/billing/raw-retention/operations/"+id, nil, &state); err != nil {
		return err
	}
	if state.OperationID != id || state.PlanSHA256 != planDigest(state.retirementPlan) || state.Environment != cfg.Environment || state.StoreID != cfg.StoreID {
		return errors.New("retirement authority plan mismatch")
	}
	if state.Status == "COMPLETED" || state.Status == "ABORTED" {
		return nil
	}
	var result json.RawMessage
	plan := map[string]any{"operation_id": id, "environment": state.Environment, "store_id": state.StoreID, "from_sequence": strconv.FormatUint(state.FromSequence, 10), "through_sequence": strconv.FormatUint(state.ThroughSequence, 10), "set_id": state.SetID, "plan_sha256": state.PlanSHA256}
	if abort || state.Status == "ABORT_REQUESTED" {
		if err := authority.Request(ctx, http.MethodPost, "/v1/internal/billing/raw-retention/operations/"+id+"/abort", map[string]string{}, &result); err != nil {
			return err
		}
		if err := local.Request(ctx, http.MethodPost, "/v1/internal/billing-lifecycle/retire/abort", plan, &result); err != nil {
			return err
		}
	} else {
		if state.Status != "ACTIVE" {
			return errors.New("retirement authority is not active")
		}
		if err := local.Request(ctx, http.MethodPost, "/v1/internal/billing-lifecycle/retire/plan", plan, &result); err != nil {
			return err
		}
		if err := local.Request(ctx, http.MethodPost, "/v1/internal/billing-lifecycle/retire/apply", map[string]string{"operation_id": id}, &result); err != nil {
			return err
		}
	}
	return authority.Request(ctx, http.MethodPost, "/v1/internal/billing/raw-retention/operations/"+id+"/resolve", map[string]string{}, &result)
}
