package billingbackup

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hkt999rtk/rtk_cloud_logger/billingarchive"
)

type singleManifestLister struct {
	*memoryStore
	prefix string
}

func (s singleManifestLister) ListManifests(context.Context, string) ([]string, error) {
	return []string{s.prefix}, nil
}

func runOneControllerCycle(t *testing.T, e Engine, store ManifestLister, local, authority Client) ControllerStatus {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	finished := make(chan error, 1)
	started := time.Now().UTC()
	go func() { finished <- Run(ctx, e, store, local, authority, time.Hour) }()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	var status ControllerStatus
	for {
		select {
		case err := <-finished:
			t.Fatalf("controller stopped before reporting cycle: %v", err)
		case <-deadline.C:
			t.Fatal("controller did not durably report its first cycle")
		case <-tick.C:
			status = ControllerStatus{}
			if readPrivateJSON(filepath.Join(e.Config.Directory, "controller-status.json"), &status) != nil || status.UpdatedAt.Before(started) {
				continue
			}
			cancel()
			if err := <-finished; !errors.Is(err, context.Canceled) {
				t.Fatalf("controller failed shutdown: %v", err)
			}
			return status
		}
	}
}

func TestRunnerFreshVerifiedProtectionPointAndEncodedAuthorityScope(t *testing.T) {
	e, prefix, objects := fixture(t)
	verified, err := e.Verify(context.Background(), prefix)
	if err != nil {
		t.Fatal(err)
	}
	e.Config.AutoRetirement = true
	e.Config.PolicyID = "policy"
	e.Config.PolicyVersion = 1
	e.Config.ClearanceID = "clearance"
	var queries, registered, applies atomic.Int32
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if r.Method != http.MethodGet || r.URL.Path != "/v1/internal/billing/raw-retention/operations" || r.URL.Query().Get("environment") != e.Config.Environment || r.URL.Query().Get("store_id") != e.Config.StoreID {
			t.Error("authority query lost decoded path/scope", r.URL)
			w.WriteHeader(400)
			return
		}
		queries.Add(1)
		json.NewEncoder(w).Encode(map[string]any{"operations": []any{}})
	}))
	defer authority.Close()
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		switch r.URL.Path {
		case "/v1/internal/billing-lifecycle/status":
			json.NewEncoder(w).Encode(map[string]string{"environment": e.Config.Environment, "store_id": e.Config.StoreID, "retired_through": "0"})
		case "/v1/internal/billing-lifecycle/verify":
			registered.Add(1)
			json.NewEncoder(w).Encode(map[string]bool{"verified": true})
		default:
			if strings.Contains(r.URL.Path, "/retire/") {
				applies.Add(1)
			}
			json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		}
	}))
	defer local.Close()
	status := runOneControllerCycle(t, e, singleManifestLister{objects, prefix}, Client{BaseURL: local.URL, Token: strings.Repeat("l", 32)}, Client{BaseURL: authority.URL, Token: strings.Repeat("a", 32)})
	if status.ProtectionOverdue || !status.VerifiedHorizon.Equal(verified.Manifest.CreatedAt) || status.ProtectedHighWater != 3 || queries.Load() != 1 || registered.Load() != 1 || applies.Load() != 0 || !strings.Contains(status.RetirementBlocked, "minimum hot age") {
		t.Fatal("fresh protected horizon or age gate incorrect", status, queries.Load(), registered.Load(), applies.Load())
	}
}

func TestRunnerRejectsFutureSignedHorizonsWithoutSilentlyResettingProtectionAge(t *testing.T) {
	for _, mode := range []string{"writer", "verifier", "receipt-after-verification"} {
		t.Run(mode, func(t *testing.T) {
			e, prefix, objects := fixture(t)
			m, _, err := e.readManifest(context.Background(), prefix)
			if err != nil {
				t.Fatal(err)
			}
			at := time.Now().UTC().Add(2 * time.Minute)
			if mode == "writer" {
				m.CreatedAt = at
				prefix = Prefix(m, "snapshot")
			}
			if mode == "receipt-after-verification" {
				m.MaxReceivedAt = at
				at = time.Now().UTC()
			}
			raw, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			payload := billingarchive.PayloadFor(m, raw, e.Config.VerifierKeyID, "billing-raw-v1", "test-verifier", at.Add(time.Second))
			completion, err := billingarchive.SignCompletion(payload, e.Signer)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(completion)
			objects.mu.Lock()
			objects.data[prefix+"/manifest.json"] = raw
			objects.data[prefix+"/complete.json"] = encoded
			objects.mu.Unlock()
			var registrations atomic.Int32
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				if strings.HasSuffix(r.URL.Path, "/verify") {
					registrations.Add(1)
				}
				json.NewEncoder(w).Encode(map[string]bool{"ok": true})
			}))
			defer local.Close()
			status := runOneControllerCycle(t, e, singleManifestLister{objects, prefix}, Client{BaseURL: local.URL, Token: strings.Repeat("l", 32)}, Client{})
			if !status.ProtectionOverdue || !status.VerifiedHorizon.IsZero() || status.ProtectedHighWater != 0 || len(status.Errors) == 0 || registrations.Load() != 0 {
				t.Fatal("future signed timestamps manufactured a protection point", status, registrations.Load())
			}
		})
	}
}

func TestRunnerAuthorityMissingOrForeignLoggerFloorNeverAppliesRetirement(t *testing.T) {
	for _, mode := range []string{"authority-404", "authority-503", "floor-environment", "floor-store", "floor-missing"} {
		t.Run(mode, func(t *testing.T) {
			e, prefix, objects := fixture(t)
			if _, err := e.Verify(context.Background(), prefix); err != nil {
				t.Fatal(err)
			}
			e.Config.AutoRetirement = true
			e.Config.PolicyID = "policy"
			e.Config.PolicyVersion = 1
			e.Config.ClearanceID = "clearance"
			var applies, authorityMutations atomic.Int32
			authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				if r.Method != http.MethodGet {
					authorityMutations.Add(1)
				}
				if mode == "authority-404" {
					w.WriteHeader(404)
					return
				}
				if mode == "authority-503" {
					w.WriteHeader(503)
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"operations": []any{}})
			}))
			defer authority.Close()
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				if strings.Contains(r.URL.Path, "/retire/") {
					applies.Add(1)
				}
				if strings.HasSuffix(r.URL.Path, "/status") {
					floor := map[string]string{"environment": e.Config.Environment, "store_id": e.Config.StoreID, "retired_through": "0"}
					switch mode {
					case "floor-environment":
						floor["environment"] = "prod"
					case "floor-store":
						floor["store_id"] = strings.Repeat("f", 32)
					case "floor-missing":
						delete(floor, "retired_through")
					}
					json.NewEncoder(w).Encode(floor)
					return
				}
				json.NewEncoder(w).Encode(map[string]bool{"ok": true})
			}))
			defer local.Close()
			status := runOneControllerCycle(t, e, singleManifestLister{objects, prefix}, Client{BaseURL: local.URL, Token: strings.Repeat("l", 32)}, Client{BaseURL: authority.URL, Token: strings.Repeat("a", 32)})
			if status.RetirementBlocked == "" || applies.Load() != 0 || authorityMutations.Load() != 0 {
				t.Fatal("incomplete authority/floor produced retirement", status, applies.Load(), authorityMutations.Load())
			}
			if strings.HasPrefix(mode, "floor-") && !strings.Contains(status.RetirementBlocked, "qualified retirement frontier") {
				t.Fatal("did not bind Logger floor to actual environment/StoreID", status)
			}
		})
	}
}

func minimalControllerPlan(e Engine, id string) retirementPlan {
	p := retirementPlan{OperationID: id, PolicyID: "policy", PolicyVersion: 1, ClearanceID: "clearance", Environment: e.Config.Environment, StoreID: e.Config.StoreID, FromSequence: 1, ThroughSequence: 1, SetID: "set", ArchiveManifestSHA256: strings.Repeat("a", 64), Records: []billingarchive.RecordBinding{{Sequence: 1, LoggerContentSHA256: strings.Repeat("b", 64), UsageID: "usage", EventSHA256: strings.Repeat("c", 64)}}}
	p.PlanSHA256 = planDigest(p)
	return p
}

func TestAuthorityDiscoveryNeverWritesAnUnqualifiedOperationFilename(t *testing.T) {
	e, _, _ := fixture(t)
	p := minimalControllerPlan(e, "../escape")
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		json.NewEncoder(w).Encode(map[string]any{"operations": []any{struct {
			retirementPlan
			Status string `json:"status"`
		}{p, "ACTIVE"}}})
	}))
	defer authority.Close()
	err := resumeAuthorityPending(context.Background(), e, Client{}, Client{BaseURL: authority.URL, Token: strings.Repeat("a", 32)})
	if err == nil {
		t.Fatal("unsafe operation ID accepted")
	}
	if _, err := os.Stat(filepath.Join(e.Config.Directory, "..", "escape.plan.json")); !os.IsNotExist(err) {
		t.Fatal("authority response escaped controller journal directory before validation", err)
	}
}

func TestPendingIntentDoesNotTrustCorruptTerminalJournalExistence(t *testing.T) {
	e, _, _ := fixture(t)
	p := minimalControllerPlan(e, "retire-unresolved")
	if err := atomicJSON(filepath.Join(e.Config.Directory, p.OperationID+".plan.json"), p); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(e.Config.Directory, p.OperationID+".terminal.json"), []byte(`{}`), 0600); err != nil {
		t.Fatal(err)
	}
	if n, err := resumePending(context.Background(), e, Client{}, Client{}); err == nil || n != 0 {
		t.Fatal("corrupt terminal journal silently skipped unresolved operation", n, err)
	}
}

func TestResumeRetirementBindsConfirmedEnvironmentAndStoreBeforeAnyMutation(t *testing.T) {
	for _, mode := range []string{"environment", "store"} {
		t.Run(mode, func(t *testing.T) {
			e, _, _ := fixture(t)
			p := minimalControllerPlan(e, "retire-foreign")
			if mode == "environment" {
				p.Environment = "prod"
			} else {
				p.StoreID = strings.Repeat("f", 32)
			}
			p.PlanSHA256 = planDigest(p)
			var writes atomic.Int32
			authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Cache-Control", "no-store")
				if r.Method != http.MethodGet {
					writes.Add(1)
				}
				json.NewEncoder(w).Encode(struct {
					retirementPlan
					Status string `json:"status"`
				}{p, "ACTIVE"})
			}))
			defer authority.Close()
			local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				writes.Add(1)
				w.Header().Set("Cache-Control", "no-store")
				json.NewEncoder(w).Encode(map[string]bool{"ok": true})
			}))
			defer local.Close()
			for _, abort := range []bool{false, true} {
				if err := ResumeRetirement(context.Background(), e.Config, Client{BaseURL: local.URL, Token: strings.Repeat("l", 32)}, Client{BaseURL: authority.URL, Token: strings.Repeat("a", 32)}, p.OperationID, abort); err == nil {
					t.Fatal("foreign confirmed scope accepted")
				}
			}
			if writes.Load() != 0 {
				t.Fatal("foreign scope reached a participant or authority mutation", writes.Load())
			}
		})
	}
}

type readOnlyFixtureObjects struct{ *memoryStore }

func (readOnlyFixtureObjects) PutImmutable(context.Context, string, []byte) error {
	return errors.New("read-only recovery credential cannot publish")
}

func TestReaderOnlyRehydrationAndRecoveryStageNeverRequireOrPublishVerifierSignature(t *testing.T) {
	e, prefix, objects := fixture(t)
	if _, err := e.Verify(context.Background(), prefix); err != nil {
		t.Fatal(err)
	}
	e.Signer = nil
	e.Objects = readOnlyFixtureObjects{objects}
	before := objects.writes
	records, bindings, manifest, err := e.ReadRange(context.Background(), prefix, 2, 3)
	if err != nil || len(records) != 2 || len(bindings) != 2 || records[0].Sequence != 2 || manifest.StoreID != e.Config.StoreID {
		t.Fatal("read-only archive rehydration depended on signing/publication", err)
	}
	destination := privateTestDir(t)
	staged, err := e.StageRestore(context.Background(), prefix, destination)
	if err != nil || !staged.State.Fenced || filepath.Dir(staged.Directory) != destination || objects.writes != before {
		t.Fatal("offline recovery depended on verifier write authority or unfenced live restore", staged, err)
	}
}

func TestControllerEnvironmentAndSourceRegistryDoNotConflateSigningRegion(t *testing.T) {
	e, prefix, _ := fixture(t)
	if e.Config.Remote.Region == e.Config.Remote.SigningRegion || e.Config.Validate() != nil {
		t.Fatal("actual Linode region wrongly reused signing scope")
	}
	e.Config.Remote.Bucket = "rtk-cloud-dev-billing-backup-us-east-1"
	if e.Config.Validate() == nil {
		t.Fatal("signing region was accepted as actual backup bucket placement")
	}
	e.Config.Remote.Bucket = "rtk-cloud-dev-billing-backup-us-sea"
	e.Config.SourceEventEnvironments = []string{"staging"}
	if _, _, err := e.readManifest(context.Background(), prefix); err == nil {
		t.Fatal("unapproved immutable source mapping was accepted")
	}
	e.Config.SourceEventEnvironments = nil
	e.Config.Environment = "prod"
	if _, _, err := e.readManifest(context.Background(), prefix); err == nil {
		t.Fatal("archive environment silently retargeted")
	}
}

func TestAuthorityPendingDiscoveryResumesExactIntentAndPersistsFullTerminalReceipt(t *testing.T) {
	e, _, _ := fixture(t)
	p := minimalControllerPlan(e, "retire-discovered")
	status := "ACTIVE"
	var applies, creates atomic.Int32
	authority := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		op := map[string]any{}
		raw, _ := json.Marshal(p)
		_ = json.Unmarshal(raw, &op)
		op["status"] = status
		if status == "COMPLETED" {
			op["terminal_receipt"] = map[string]any{"operation_id": p.OperationID, "environment": p.Environment, "store_id": p.StoreID, "from_sequence": "1", "through_sequence": "1", "set_id": p.SetID, "plan_sha256": p.PlanSHA256, "status": "completed", "completed_at": time.Now().UTC(), "terminal_sha256": strings.Repeat("d", 64)}
			op["consumer_proofs"] = []any{map[string]string{"consumer_id": "consumer", "proof_sha256": strings.Repeat("e", 64)}}
		}
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/internal/billing/raw-retention/operations":
			json.NewEncoder(w).Encode(map[string]any{"operations": []any{op}})
		case r.Method == http.MethodGet:
			json.NewEncoder(w).Encode(op)
		case strings.HasSuffix(r.URL.Path, "/resolve"):
			status = "COMPLETED"
			json.NewEncoder(w).Encode(map[string]bool{"resolved": true})
		default:
			creates.Add(1)
			w.WriteHeader(409)
		}
	}))
	defer authority.Close()
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		if strings.HasSuffix(r.URL.Path, "/apply") {
			applies.Add(1)
		}
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
	}))
	defer local.Close()
	err := resumeAuthorityPending(context.Background(), e, Client{BaseURL: local.URL, Token: strings.Repeat("l", 32)}, Client{BaseURL: authority.URL, Token: strings.Repeat("a", 32)})
	if err != nil || applies.Load() != 1 || creates.Load() != 0 {
		t.Fatal("discovered fence allocated another intent", err, applies.Load(), creates.Load())
	}
	var terminal map[string]json.RawMessage
	if err := readPrivateJSON(filepath.Join(e.Config.Directory, p.OperationID+".terminal.json"), &terminal); err != nil || len(terminal["terminal_receipt"]) == 0 || len(terminal["consumer_proofs"]) == 0 {
		t.Fatal("terminal journal discarded exact participant receipt", terminal, err)
	}
	if n, err := resumePending(context.Background(), e, Client{}, Client{}); err != nil || n != 0 {
		t.Fatal("completed discovered intent was replayed", n, err)
	}
}
