package cloudmonitor

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestConfigurationBoundaries(t *testing.T) {
	dir := t.TempDir()
	inv := Inventory{SchemaVersion: 1, Environment: "dev", Stack: "video-cloud-dev", SourceFingerprint: "fixture", Targets: []WorkloadTarget{{ID: "app", Name: "app", Service: "app", Namespace: "video-cloud-dev-video-cloud", Kind: "deployment", Enabled: true, Required: true}}}
	raw, _ := json.Marshal(inv)
	os.WriteFile(filepath.Join(dir, "inventory.json"), raw, 0600)
	volume := &VolumeCheck{Namespace: "video-cloud-dev-platform", Pod: "postgresql-0", Mount: "/data"}
	base := Config{SchemaVersion: 1, Environment: "dev", Stack: "video-cloud-dev", InventoryFile: "inventory.json", HTTP: []HTTPCheck{{Name: "health", Service: "app", URL: "https://example.test/health", SuccessCodes: []int{200}, Required: true}}, TLS: []TLSCheck{{Name: "tls", Service: "app", Address: "example.test:443", Role: "leaf", WarnDays: 30, CriticalDays: 7, Required: true}}, Tokens: []TokenCheck{{Name: "token", Service: "app", Kind: "jwt", TokenFile: "monitor/token", Algorithm: "RS256", ValidationURL: "https://example.test/me", Required: true}}, Postgres: []PostgresCheck{{Name: "postgres", DSNFile: "monitor/pg", Volume: volume, Required: true}}, Redis: []RedisCheck{{Name: "redis", Address: "localhost:6379", Role: "shadow", Volume: volume, Required: true}}, Metrics: []MetricCheck{{Name: "qps", Service: "app", URL: "https://example.test/prom", Query: "sum(rate(requests[2m]))", Baseline: 100, BaselineEvidence: "dated-load-report"}}, Synthetic: SyntheticConfig{Auth: []AuthProbe{{Name: "am", Kind: "account-manager", CredentialsFile: "monitor/auth", LoginURL: "https://example.test/v1/auth/login", RefreshURL: "https://example.test/v1/auth/refresh", ValidationURL: "https://example.test/v1/me"}}, MQTT: []MQTTProbe{{Name: "mqtt", Broker: "ssl://example.test:8883", Topic: "monitor", CredentialsFile: "monitor/mqtt"}}, TURN: []TURNProbe{{Name: "turn", Address: "example.test:3478", Network: "udp", PeerAddress: "peer.test:3478", CredentialsFile: "monitor/turn"}}}}
	cases := []struct {
		name   string
		change func(*Config)
	}{
		{"timeout", func(c *Config) { c.TimeoutSeconds = 121 }}, {"concurrency", func(c *Config) { c.Concurrency = 33 }}, {"retention", func(c *Config) { c.RetentionDays = -1 }}, {"timezone", func(c *Config) { c.Timezone = "invalid-zone" }}, {"report-clock", func(c *Config) { c.DailyAt = "25:00" }},
		{"embedded-secret", func(c *Config) { c.HTTP[0].URL = "https://user:secret@example.test" }}, {"duplicate", func(c *Config) { c.HTTP = append(c.HTTP, c.HTTP[0]) }}, {"no-http-codes", func(c *Config) { c.HTTP[0].SuccessCodes = nil }}, {"missing-tls-source", func(c *Config) { c.TLS[0].Address = "" }}, {"cert-threshold", func(c *Config) { c.TLS[0].CriticalDays = 40 }},
		{"token-kind", func(c *Config) { c.Tokens[0].Kind = "unknown" }}, {"untrusted-jwt", func(c *Config) { c.Tokens[0].Algorithm = ""; c.Tokens[0].ValidationURL = "" }}, {"token-window", func(c *Config) { c.Tokens[0].WarnSeconds = 60; c.Tokens[0].CriticalSeconds = 90 }},
		{"pg-reference", func(c *Config) { c.Postgres[0].DSNFile = "" }}, {"cross-env-volume", func(c *Config) { c.Postgres[0].Volume.Namespace = "video-cloud-prod-platform" }}, {"relative-mount", func(c *Config) { c.Postgres[0].Volume.Mount = "data" }}, {"redis-role", func(c *Config) { c.Redis[0].Role = "ordinary" }},
		{"no-metric-query", func(c *Config) { c.Metrics[0].Query = "" }}, {"unsupported-baseline", func(c *Config) { c.Metrics[0].BaselineEvidence = "" }}, {"auth-kind", func(c *Config) { c.Synthetic.Auth[0].Kind = "operator" }}, {"mqtt-wildcard", func(c *Config) { c.Synthetic.MQTT[0].Topic = "monitor/#" }}, {"turn-transport", func(c *Config) { c.Synthetic.TURN[0].Network = "sctp" }},
	}
	original, _ := json.Marshal(base)
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, original, 0600)
	if _, _, e := LoadConfig(path, "dev"); e != nil {
		t.Fatal(e)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var c Config
			json.Unmarshal(original, &c)
			tc.change(&c)
			raw, _ := json.Marshal(c)
			os.WriteFile(path, raw, 0600)
			if _, _, e := LoadConfig(path, "dev"); e == nil {
				t.Fatal("unsafe configuration accepted")
			}
		})
	}
}

func TestResolveRuntimeSelectsOneEnvironment(t *testing.T) {
	base := t.TempDir()
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", base)
	rt, e := ResolveRuntime("dev", "", base, "out")
	if e != nil || rt.ConfigRoot != filepath.Join(base, "dev") || rt.Kubeconfig != filepath.Join(base, "dev", "kube", "kubeconfig.yaml") {
		t.Fatal(rt, e)
	}
	if _, e = ResolveRuntime("../prod", base, base, "out"); e == nil {
		t.Fatal("invalidenvironment")
	}
}

func TestRunnerHelper(t *testing.T) {
	if os.Getenv("CLOUD_MONITOR_TEST_HELPER") != "1" {
		return
	}
	data, _ := io.ReadAll(os.Stdin)
	os.Stdout.Write(data)
	os.Stderr.WriteString("private-error-must-never-appear")
	os.Exit(3)
}
func TestRunnerPreservesLiteralInputSuppressesStderrAndBounds(t *testing.T) {
	raw := []byte("literal $(echo secret) `echo secret`\n")
	stdout, code, e := (OSRunner{}).Run(context.Background(), os.Args[0], []string{"-test.run=^TestRunnerHelper$"}, raw, []string{"CLOUD_MONITOR_TEST_HELPER=1"})
	if code != 3 || e == nil || !bytes.Equal(stdout, raw) {
		t.Fatal("runner didnotpreserveliteral/suppressstderr", code, e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, e = (OSRunner{}).Run(ctx, os.Args[0], nil, nil, nil); e == nil {
		t.Fatal("cancelignored")
	}
	var out boundedOutput
	if n, e := out.Write([]byte("small")); n != 5 || e != nil {
		t.Fatal(n, e)
	}
	if _, e := out.Write(make([]byte, 32<<20)); e == nil {
		t.Fatal("unboundedcommandoutput")
	}
}

func TestCredentialScopePermissionsAndSymlinks(t *testing.T) {
	root := t.TempDir()
	rt := Runtime{ConfigRoot: root}
	p := filepath.Join(root, "secret")
	if e := os.WriteFile(p, []byte("sensitive-value"), 0600); e != nil {
		t.Fatal(e)
	}
	if b, e := ReadCredential(rt, "secret"); e != nil || string(b) != "sensitive-value" {
		t.Fatal("private credential should be available")
	}
	os.Chmod(p, 0644)
	if _, e := ReadCredential(rt, "secret"); e == nil {
		t.Fatal("publicly readable secret accepted")
	}
	if _, e := ReadPublicFile(rt, "secret"); e != nil {
		t.Fatal(e)
	}
	outside := filepath.Join(t.TempDir(), "other")
	os.WriteFile(outside, []byte("outside"), 0600)
	os.Symlink(outside, filepath.Join(root, "link"))
	if _, e := ReadCredential(rt, "link"); e == nil {
		t.Fatal("escaping symlink accepted")
	}
	if _, e := ReadCredential(rt, "../other"); e == nil {
		t.Fatal("escaping path accepted")
	}
}

func TestConfigStrictAndEnvironmentIdentity(t *testing.T) {
	dir := t.TempDir()
	inv := Inventory{SchemaVersion: 1, Environment: "dev", Stack: "video-cloud-dev", SourceFingerprint: "source", Targets: []WorkloadTarget{{ID: "db", Name: "postgres", Service: "db", Namespace: "video-cloud-dev-platform", Kind: "statefulset", Enabled: true, Required: true}}}
	raw, _ := json.Marshal(inv)
	os.WriteFile(filepath.Join(dir, "inventory.json"), raw, 0600)
	cfg := Config{SchemaVersion: 1, Environment: "dev", Stack: "video-cloud-dev", InventoryFile: "inventory.json"}
	raw, _ = json.Marshal(cfg)
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, raw, 0600)
	if c, _, e := LoadConfig(path, "dev"); e != nil || c.TimeoutSeconds != 5 {
		t.Fatal(c, e)
	}
	if _, _, e := LoadConfig(path, "prod"); e == nil {
		t.Fatal("cross environment accepted")
	}
	raw = append(raw[:len(raw)-1], []byte(",\"unknown_property\":true}")...)
	os.WriteFile(path, raw, 0600)
	if _, _, e := LoadConfig(path, "dev"); e == nil {
		t.Fatal("unknown field accepted")
	}
}

func TestSummaryCannotGreenMissingRequired(t *testing.T) {
	if status, c := Summarize([]Result{{Status: Status("UNRECOGNIZED"), Required: true}}); status != Unknown || c.Unknown != 1 {
		t.Fatal("unknown schema status becamegreen", status, c)
	}
	for _, missing := range []Status{Unknown, NotRun} {
		for _, order := range [][]Result{{{Status: Pass, Required: true}, {Status: missing, Required: true}}, {{Status: missing, Required: true}, {Status: Pass, Required: true}}} {
			s, c := Summarize(order)
			if s != Unknown || c.Percent != 50 {
				t.Fatalf("%s %#v", s, c)
			}
		}
	}
	s, c := Summarize([]Result{{Status: Fail, Required: true}, {Status: Unknown, Required: true}, {Status: NotApplicable, Required: true}})
	if s != Fail || c.Required != 2 {
		t.Fatal(s, c)
	}
}

func TestWriterLockRejectsSecondWriterAndReleases(t *testing.T) {
	out := t.TempDir()
	first, e := AcquireWriterLock(out, "dev")
	if e != nil {
		t.Fatal(e)
	}
	if _, e = AcquireWriterLock(out, "dev"); e == nil {
		t.Fatal("second writer accepted")
	}
	first.Close()
	second, e := AcquireWriterLock(out, "dev")
	if e != nil {
		t.Fatal(e)
	}
	second.Close()
	if _, e = AcquireWriterLock(out, "../escape"); e == nil {
		t.Fatal("invalid environment accepted")
	}
}

func TestSyntheticSkippedMaintainsCoverage(t *testing.T) {
	s, c := Summarize(syntheticSkipped(time.Now()).Results)
	if s != Unknown || c.NotRun != 1 {
		t.Fatal(s, c)
	}
}
