package cloudmonitor

import (
	"strings"
	"testing"
	"time"
)

func TestEmptyAdaptersCannotHideDatabasesAndTokens(t *testing.T) {
	inv := Inventory{Targets: []WorkloadTarget{{Service: "postgres", Enabled: true, Required: true}, {Service: "redis", Enabled: true, Required: true}, {Service: "fleet-valkey", Enabled: true, Required: true}, {Service: "video-cloud", Enabled: true, Required: true}}}
	col := ConfigurationCoverage(Config{}, inv, time.Now())
	status, c := Summarize(col.Results)
	if status != Unknown || c.Unknown < 5 {
		t.Fatal(status, c)
	}
	inv.Targets[0].Enabled = false
	col = ConfigurationCoverage(Config{}, inv, time.Now())
	for _, r := range col.Results {
		if r.Service == "postgres" {
			t.Fatal("disabled expectation checked")
		}
	}
}

func TestStoredCertificateCannotSatisfyActiveIdentityCoverage(t *testing.T) {
	inv := Inventory{Targets: []WorkloadTarget{{Service: "mqtt", Required: true, Enabled: true}}}
	cfg := Config{TLS: []TLSCheck{{Name: "stored", Service: "mqtt", CertFile: "saved.crt", Required: true}}}
	missing := func(c Config) bool {
		for _, r := range ConfigurationCoverage(c, inv, time.Now()).Results {
			if r.CheckID == "coverage/active-identity" {
				return true
			}
		}
		return false
	}
	if !missing(cfg) {
		t.Fatal("stored certificate claimed active identity")
	}
	cfg.TLS[0].Address = "broker.example.test:8883"
	if missing(cfg) {
		t.Fatal("live TLS configuration not counted")
	}
}

func TestCoverageRequiresEvidenceForEachApplication(t *testing.T) {
	inv := Inventory{Endpoints: []Endpoint{{Service: "video-cloud"}, {Service: "billing"}}}
	cfg := Config{
		HTTP:    []HTTPCheck{{Service: "video-cloud", Required: true, Contains: "ready"}},
		Metrics: []MetricCheck{{Service: "video-cloud", Required: true}},
	}
	gaps := map[string]Status{}
	for _, r := range ConfigurationCoverage(cfg, inv, time.Now()).Results {
		gaps[r.Service+"/"+r.CheckID] = r.Status
	}
	if len(gaps) != 2 || gaps["billing/coverage/readiness"] != Unknown || gaps["billing/coverage/performance"] != Unknown {
		t.Fatalf("another application's evidence hid billing coverage: %v", gaps)
	}
	cfg.HTTP = append(cfg.HTTP, HTTPCheck{Service: "billing", Required: true, Contains: "ready"})
	cfg.Metrics = append(cfg.Metrics, MetricCheck{Service: "billing", Required: false})
	results := ConfigurationCoverage(cfg, inv, time.Now()).Results
	if len(results) != 1 || results[0].CheckID != "coverage/performance" {
		t.Fatalf("optional metric claimed required coverage: %v", results)
	}
	cfg.Metrics[1].Required = true
	if results := ConfigurationCoverage(cfg, inv, time.Now()).Results; len(results) != 0 {
		t.Fatalf("configured evidence still reported missing: %v", results)
	}
}

func TestExternalCoverageRequiresMatchedEvidence(t *testing.T) {
	now := time.Now()
	inv := Inventory{Targets: []WorkloadTarget{
		{ID: "edge", Service: "public-edge", Kind: "external", Name: "haproxy", Enabled: true, Required: true},
		{ID: "gateway", Service: "partner-gateway", Kind: "external", Name: "gateway.example.test", Enabled: true, Required: true},
		{ID: "turn-one", Service: "turn", Kind: "external", Name: "turn01.example.test", Enabled: true, Required: true},
		{ID: "turn-two", Service: "turn", Kind: "external", Name: "turn02.example.test", Enabled: true, Required: true},
	}}
	base := Config{Metrics: []MetricCheck{{Name: "required-metric", Service: "public-edge", URL: "https://metrics.example.test", Query: "up", Required: true}}}
	wantAll := map[string]bool{
		"coverage/external-http/haproxy":              true,
		"coverage/external-http/gateway.example.test": true,
		"coverage/external-turn/turn01.example.test":  true,
		"coverage/external-turn/turn02.example.test":  true,
	}
	if got := externalCoverageGapIDs(base, inv, now); !sameGapIDs(got, wantAll) {
		t.Fatalf("missing external evidence gaps = %v, want %v", got, wantAll)
	}

	optionalOrWrong := base
	optionalOrWrong.HTTP = []HTTPCheck{
		{Name: "edge-optional", Service: "public-edge", URL: "https://edge.example.test", SuccessCodes: []int{200}, Required: false, Contains: "ready"},
		{Name: "edge-liveness-only", Service: "public-edge", URL: "https://edge.example.test", SuccessCodes: []int{200}, Required: true, LivenessOnly: true, Contains: "ready"},
		{Name: "wrong-service", Service: "other", URL: "https://other.example.test", SuccessCodes: []int{200}, Required: true, Contains: "ready"},
	}
	optionalOrWrong.Synthetic.TURN = []TURNProbe{{Name: "wrong-host", Address: "turn03.example.test:3478", Network: "udp", PeerAddress: "peer.example.test:3478", CredentialsFile: "monitor/turn"}}
	if got := externalCoverageGapIDs(optionalOrWrong, inv, now); !sameGapIDs(got, wantAll) {
		t.Fatalf("optional, liveness-only, or mismatched evidence cleared gaps: %v", got)
	}

	configured := base
	configured.HTTP = []HTTPCheck{
		{Name: "edge-semantic", Service: "public-edge", URL: "https://edge.example.test", SuccessCodes: []int{200}, Required: true, Contains: "ready"},
		{Name: "gateway-semantic", Service: "partner-gateway", URL: "https://gateway.example.test", SuccessCodes: []int{200}, Required: true, Contains: "ready"},
	}
	configured.Synthetic.TURN = []TURNProbe{{Name: "turn-one", Address: "TURN01.EXAMPLE.TEST.:3478", Network: "udp", PeerAddress: "peer.example.test:3478", CredentialsFile: "monitor/turn"}}
	wantSecondTURN := map[string]bool{"coverage/external-turn/turn02.example.test": true}
	if got := externalCoverageGapIDs(configured, inv, now); !sameGapIDs(got, wantSecondTURN) {
		t.Fatalf("one TURN probe must not cover another host: %v", got)
	}
	configured.Synthetic.TURN = append(configured.Synthetic.TURN, TURNProbe{Name: "turn-two", Address: "turn02.example.test:3478", Network: "udp", PeerAddress: "peer.example.test:3478", CredentialsFile: "monitor/turn"})
	if got := externalCoverageGapIDs(configured, inv, now); len(got) != 0 {
		t.Fatalf("configured matching external evidence still reported gaps: %v", got)
	}

	actual := []Result{
		{Service: "public-edge", CheckID: "k8s/edge", Required: true, Status: NotApplicable},
		{Service: "partner-gateway", CheckID: "k8s/gateway", Required: true, Status: NotApplicable},
		{Service: "turn", CheckID: "k8s/turn-one", Required: true, Status: NotApplicable},
		{Service: "turn", CheckID: "k8s/turn-two", Required: true, Status: NotApplicable},
		{Service: "public-edge", CheckID: "http/edge-semantic", Required: true, Status: Pass},
		{Service: "partner-gateway", CheckID: "http/gateway-semantic", Required: true, Status: Pass},
		{Service: "turn", CheckID: "synthetic/turn/turn-one", Required: true, Status: Pass},
		{Service: "turn", CheckID: "synthetic/turn/turn-two", Required: true, Status: Pass},
	}
	allPassing := append([]Result{}, actual...)
	allPassing = append(allPassing, ConfigurationCoverage(configured, inv, now).Results...)
	if status, coverage := Summarize(allPassing); status != Pass || coverage.Unknown != 0 || coverage.NotRun != 0 {
		t.Fatalf("configured external probes must allow PASS: %s %#v", status, coverage)
	}
	for _, probeStatus := range []Status{Fail, NotRun} {
		results := append([]Result{}, actual...)
		results[len(results)-1].Status = probeStatus
		results = append(results, ConfigurationCoverage(configured, inv, now).Results...)
		status, _ := Summarize(results)
		want := Unknown
		if probeStatus == Fail {
			want = Fail
		}
		if status != want {
			t.Fatalf("required external TURN probe %s produced %s, want %s", probeStatus, status, want)
		}
	}
}

func externalCoverageGapIDs(cfg Config, inv Inventory, now time.Time) map[string]bool {
	gaps := map[string]bool{}
	for _, result := range ConfigurationCoverage(cfg, inv, now).Results {
		if strings.HasPrefix(result.CheckID, "coverage/external-") {
			gaps[result.CheckID] = true
		}
	}
	return gaps
}

func sameGapIDs(got, want map[string]bool) bool {
	if len(got) != len(want) {
		return false
	}
	for id := range want {
		if !got[id] {
			return false
		}
	}
	return true
}
