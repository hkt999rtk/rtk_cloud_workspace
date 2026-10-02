package cloudmonitor

import (
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
