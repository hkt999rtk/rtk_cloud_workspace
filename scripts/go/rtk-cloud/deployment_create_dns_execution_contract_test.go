package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type createDNSExecutionContract struct {
	cfg               deploymentConfig
	store             secretStore
	e                 *environmentCreateExecution
	plan              genericDNSPlan
	operator          map[string]string
	receipts          map[string]environmentCreateProviderReceipt
	mu                sync.Mutex
	records           map[string][]string
	puts              int
	deny              bool
	failReadback      bool
	loseWriteResponse bool
}

// Freeze the maintained DNS target before the original create is published.
// Only the authoritative DNS HTTP protocol is simulated; qualification, native
// publication, private ownership and original execution receipts are real.
func newCreateDNSExecutionContract(t *testing.T) *createDNSExecutionContract {
	t.Helper()
	cfg, store, _, original := createStateFixture(t, "dev")
	values, _, _ := completeEnvironmentRenderFixture("dev")
	cfg.Values = appendMap(cfg.Values, values)
	cfg.Values["VIDEO_CLOUD_DOMAIN"] = "api.dev.example.invalid"
	cfg.Values["TURN_REPLICAS"] = "2"
	cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] = "standard"
	cfg.DNSAdapter = "godaddy"
	cfg.DNSValues = map[string]string{"DNS_RECORD_TTL": "600", "GODADDY_ENV": "prod"}
	f := &createDNSExecutionContract{cfg: cfg, store: store, plan: buildGenericDNSPlan(cfg), operator: map[string]string{"GODADDY_KEY": "isolated-dns-key", "GODADDY_SECRET": "isolated-dns-secret"}, records: map[string][]string{}, receipts: map[string]environmentCreateProviderReceipt{
		"edge":    {Key: "edge", Kind: "instance", NativeID: "100", PublicIP: "198.51.100.10"},
		"turn-01": {Key: "turn-01", Kind: "instance", NativeID: "101", PublicIP: "198.51.100.11"},
		"turn-02": {Key: "turn-02", Kind: "instance", NativeID: "102", PublicIP: "198.51.100.12"},
	}}
	if len(f.plan.Records) < 4 {
		t.Fatal("complete public edge and TURN target required")
	}
	for name, value := range f.operator {
		if err := store.write(filepath.Join("operator/env", name), []byte(value), false); err != nil {
			t.Fatal(err)
		}
	}
	var p environmentCreatePlan
	if err := json.Unmarshal(original.Plan, &p); err != nil {
		t.Fatal(err)
	}
	p.DNS, p.ConfigurationSHA256, p.OperatorSHA256 = f.plan, deploymentplan.Digest(cfg), deploymentplan.Digest(f.operator)
	raw := mustProviderRecoveryJSON(p)
	sha, err := environmentCreatePlanSHA256(raw)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := invalidateEnvironmentPreflightProofWithToken(cfg)
	if err != nil {
		t.Fatal(err)
	}
	q := original.Qualification
	q.PlanSHA256 = sha
	a, err := publishEnvironmentCreateArtifact(t.Context(), cfg, generation, raw, p.OperatorSHA256, q, original.ReleaseProofs, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	f.e, err = startEnvironmentCreateExecution(t.Context(), cfg, a, p.OperatorSHA256, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = f.e.Close() })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if r.Header.Get("Authorization") != "sso-key isolated-dns-key:isolated-dns-secret" {
			t.Error("DNS did not use selected original credentials")
			w.WriteHeader(403)
			return
		}
		prefix := "/v1/domains/" + f.plan.RootDomain + "/records/"
		if !strings.HasPrefix(r.URL.Path, prefix) {
			t.Error("unselected DNS scope", r.URL.Path)
			w.WriteHeader(400)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, prefix)
		if f.deny {
			w.WriteHeader(403)
			return
		}
		switch r.Method {
		case http.MethodGet:
			if f.failReadback && f.puts > 0 {
				f.failReadback = false
				w.WriteHeader(503)
				return
			}
			payload := []map[string]any{}
			for _, value := range f.records[key] {
				payload = append(payload, map[string]any{"data": value, "ttl": 600})
			}
			_ = json.NewEncoder(w).Encode(payload)
		case http.MethodPut:
			var payload []struct {
				Data string `json:"data"`
			}
			if json.NewDecoder(r.Body).Decode(&payload) != nil || len(payload) != 1 {
				t.Error("unexpected DNS publication")
				w.WriteHeader(400)
				return
			}
			f.records[key] = []string{payload[0].Data}
			f.puts++
			if f.loseWriteResponse {
				f.loseWriteResponse = false
				w.WriteHeader(503)
				return
			}
			w.WriteHeader(200)
		default:
			t.Error("unrequested DNS mutation", r.Method)
			w.WriteHeader(405)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("RTK_CLOUD_GODADDY_API_ROOT", server.URL)
	return f
}

func (f *createDNSExecutionContract) key(record dnsRecordSet, kind string) string {
	return kind + "/" + dnsRecordName(f.plan.RootDomain, record.Name)
}

func TestCreateDNSExecutionContractQualificationKeepsAllExistingOwnership(t *testing.T) {
	for _, mode := range []string{"original", "occupied-A", "occupied-AAAA", "occupied-CNAME", "old-owned-occupied", "unresolved-challenge", "changed-intent", "denied", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f := newCreateDNSExecutionContract(t)
			if err := qualifyEnvironmentCreateDNS(t.Context(), f.cfg, f.plan, f.operator); err != nil {
				t.Fatal("complete original DNS target must qualify", err)
			}
			_, local, err := environmentCreateDNSAdapter(f.cfg, f.operator)
			if err != nil {
				t.Fatal(err)
			}
			plan := f.plan
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "occupied-A", "occupied-AAAA", "occupied-CNAME":
				f.records[f.key(plan.Records[0], strings.TrimPrefix(mode, "occupied-"))] = []string{"retained-existing-address"}
			case "old-owned-occupied":
				old := dnsRecordSet{Name: "historical.dev." + plan.RootDomain, Type: "A", Values: []string{"198.51.100.90"}, TTL: 600}
				if err := writeDNSOwnership(local, "godaddy", []dnsRecordSet{old}); err != nil {
					t.Fatal(err)
				}
				f.records[f.key(old, "A")] = old.Values
			case "unresolved-challenge":
				if err := writeDNSChallenges(local, "godaddy", []dnsChallengeState{{Domain: plan.Records[0].Name, Value: "retained-original-challenge"}}); err != nil {
					t.Fatal(err)
				}
			case "changed-intent":
				plan.Records = append([]dnsRecordSet(nil), plan.Records...)
				plan.Records[0].TTL++
			case "denied":
				f.deny = true
			case "cancelled":
				cancel()
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			err = qualifyEnvironmentCreateDNS(ctx, f.cfg, plan, f.operator)
			if (err == nil) != (mode == "original") {
				t.Fatal("unexpected selected DNS qualification", err)
			}
			if f.puts != 0 || !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, f.store.Root)) {
				t.Fatal("qualification changed original DNS or execution state")
			}
		})
	}
}

func TestCreateDNSExecutionContractCompletesAndReconcilesExactNativePublication(t *testing.T) {
	for _, mode := range []string{"completed-retry", "allocated-readback-retry", "unknown-write", "changed-address", "conflicting-CNAME", "changed-intent", "occupied-before-write", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f := newCreateDNSExecutionContract(t)
			if err := qualifyEnvironmentCreateDNS(t.Context(), f.cfg, f.plan, f.operator); err != nil {
				t.Fatal(err)
			}
			ctx := context.WithValue(t.Context(), environmentCreateBootstrapRuntimeKey{}, environmentCreateBootstrapRuntime{Execution: f.e, ExecutionSHA256: f.e.Journal.PlanSHA256})
			f.failReadback = mode == "allocated-readback-retry"
			f.loseWriteResponse = mode == "unknown-write"
			if mode == "occupied-before-write" {
				f.records[f.key(f.plan.Records[0], "AAAA")] = []string{"2001:db8::20"}
			}
			if mode == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err := executeEnvironmentCreateDNS(ctx, f.cfg, f.plan, f.receipts, f.operator, f.e)
			if mode == "occupied-before-write" || mode == "cancelled" {
				if err == nil || f.puts != 0 {
					t.Fatal("unqualified native publication proceeded", err)
				}
				return
			}
			firstKey := "dns/" + f.plan.Records[0].Type + "/" + f.plan.Records[0].Name
			entry := createOpenBaoACKCompletionEntry(f.e.Journal, firstKey)
			if entry == nil {
				t.Fatal("original DNS outcome was not journaled")
			}
			if mode == "allocated-readback-retry" && (err == nil || entry.State != "allocated") {
				t.Fatal("readback interruption lost original allocation", err, entry.State)
			}
			if mode == "unknown-write" && (err == nil || entry.State != "pending") {
				t.Fatal("ambiguous provider response was treated as known", err, entry.State)
			}
			if mode != "allocated-readback-retry" && mode != "unknown-write" && err != nil {
				t.Fatal("initial DNS publication failed", err)
			}
			switch mode {
			case "changed-address":
				f.records[f.key(f.plan.Records[0], "A")] = []string{"198.51.100.99"}
			case "conflicting-CNAME":
				f.records[f.key(f.plan.Records[0], "CNAME")] = []string{"foreign.example.invalid"}
			case "changed-intent":
				f.e.Journal.Entries[len(f.e.Journal.Entries)-1].IntentSHA256 = deploymentplan.Digest("foreign DNS publication")
				if err := f.e.save(); err != nil {
					t.Fatal(err)
				}
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			puts := f.puts
			err = executeEnvironmentCreateDNS(ctx, f.cfg, f.plan, f.receipts, f.operator, f.e)
			success := mode == "completed-retry" || mode == "allocated-readback-retry"
			if (err == nil) != success {
				t.Fatal("unexpected DNS reconciliation outcome", err)
			}
			if !success || mode == "completed-retry" {
				if f.puts != puts || !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, f.store.Root)) {
					t.Fatal("retry changed an original or uncertain DNS outcome")
				}
			}
			if success {
				if f.puts != len(f.plan.Records) {
					t.Fatal("retry republished an existing address", f.puts)
				}
				for _, record := range f.plan.Records {
					if got := createOpenBaoACKCompletionEntry(f.e.Journal, "dns/"+record.Type+"/"+record.Name); got == nil || got.State != "completed" {
						t.Fatal("publication lacks final original receipt", record.Name)
					}
				}
				_, local, _ := environmentCreateDNSAdapter(f.cfg, f.operator)
				owned, err := readDNSOwnership(local, "godaddy")
				if err != nil || len(owned.Records) != len(f.plan.Records) {
					t.Fatal("verified native ownership not retained", err)
				}
			}
		})
	}
}
