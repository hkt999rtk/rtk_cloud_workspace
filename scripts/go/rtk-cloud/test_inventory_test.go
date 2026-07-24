package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseNodeTestEventsTracksStatusesAndSourceRewrite(t *testing.T) {
	moduleDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(moduleDir, "test"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(moduleDir, "test", "package.test.ts"), []byte("// source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtimeFile := filepath.Join(moduleDir, "dist", "test", "package.test.js")
	if err := os.MkdirAll(filepath.Dir(runtimeFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runtimeFile, []byte("// build output\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	events := strings.Join([]string{
		`{"schema_version":1,"event":"start","name":"passes","file":"` + runtimeFile + `","line":1,"timestamp":"2026-07-24T00:00:00Z"}`,
		`{"schema_version":1,"event":"pass","name":"passes","file":"` + runtimeFile + `","line":1,"test_type":"test","duration_ms":12.9,"timestamp":"2026-07-24T00:00:01Z"}`,
		`{"schema_version":1,"event":"start","name":"skips","file":"` + runtimeFile + `","line":2,"timestamp":"2026-07-24T00:00:01Z"}`,
		`{"schema_version":1,"event":"pass","name":"skips","file":"` + runtimeFile + `","line":2,"test_type":"test","skip":true,"timestamp":"2026-07-24T00:00:02Z"}`,
		`{"schema_version":1,"event":"start","name":"todo","file":"` + runtimeFile + `","line":3,"timestamp":"2026-07-24T00:00:02Z"}`,
		`{"schema_version":1,"event":"pass","name":"todo","file":"` + runtimeFile + `","line":3,"test_type":"test","todo":"later","timestamp":"2026-07-24T00:00:03Z"}`,
		`{"schema_version":1,"event":"start","name":"fails","file":"` + runtimeFile + `","line":4,"timestamp":"2026-07-24T00:00:03Z"}`,
		`{"schema_version":1,"event":"fail","name":"fails","file":"` + runtimeFile + `","line":4,"test_type":"test","error":{"message":"boom"},"timestamp":"2026-07-24T00:00:04Z"}`,
	}, "\n") + "\n"
	eventPath := filepath.Join(t.TempDir(), "events.jsonl")
	if err := os.WriteFile(eventPath, []byte(events), 0o644); err != nil {
		t.Fatal(err)
	}
	module := coverageModule{
		Name: "client", SourceTestGlobs: []string{"test/*.test.ts"},
		SourceRewrites: []coverageSourceRewrite{{FromPrefix: "dist/test/", ToPrefix: "test/", Extension: ".ts"}},
	}
	units, err := parseNodeTestEvents(eventPath, moduleDir, module)
	if err != nil {
		t.Fatal(err)
	}
	statuses := map[string]string{}
	for _, unit := range units {
		statuses[unit.Title] = unit.Status
		if unit.Source != "test/package.test.ts" || !strings.HasPrefix(unit.CanonicalKey, "js://client/test/package.test.ts#") {
			t.Fatalf("unexpected mapped unit: %#v", unit)
		}
	}
	for title, want := range map[string]string{"passes": "PASS", "skips": "SKIP", "todo": "TODO", "fails": "FAIL"} {
		if statuses[title] != want {
			t.Fatalf("%s status = %q, want %q", title, statuses[title], want)
		}
	}
}

func TestParseNodeTestEventsRejectsMalformedIncompleteAndDuplicateEvents(t *testing.T) {
	moduleDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(moduleDir, "case.test.mjs"), []byte("// source\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runtimeFile := filepath.Join(moduleDir, "case.test.mjs")
	module := coverageModule{Name: "module", SourceTestGlobs: []string{"*.test.mjs"}}
	tests := map[string]string{
		"malformed":  "{bad json}\n",
		"incomplete": `{"schema_version":1,"event":"start","name":"case","file":"` + runtimeFile + `","line":1,"timestamp":"2026-07-24T00:00:00Z"}` + "\n",
		"duplicate": strings.Join([]string{
			`{"schema_version":1,"event":"start","name":"case","file":"` + runtimeFile + `","line":1,"timestamp":"2026-07-24T00:00:00Z"}`,
			`{"schema_version":1,"event":"pass","name":"case","file":"` + runtimeFile + `","line":1,"test_type":"test","timestamp":"2026-07-24T00:00:01Z"}`,
			`{"schema_version":1,"event":"start","name":"case","file":"` + runtimeFile + `","line":2,"timestamp":"2026-07-24T00:00:02Z"}`,
			`{"schema_version":1,"event":"pass","name":"case","file":"` + runtimeFile + `","line":2,"test_type":"test","timestamp":"2026-07-24T00:00:03Z"}`,
		}, "\n") + "\n",
	}
	for name, body := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "events.jsonl")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatal(err)
			}
			if _, err := parseNodeTestEvents(path, moduleDir, module); err == nil {
				t.Fatal("invalid events unexpectedly passed")
			}
		})
	}
}

func TestCompareUnitInventoryRejectsNewAndMissingCasesButAllowsRetiredCases(t *testing.T) {
	registered := unitInventoryCase{
		CanonicalKey: "js://module/test/case.test.ts#registered",
		Module:       "module", Language: "javascript", Source: "test/case.test.ts",
		Title: "registered", Owner: "owner", Status: "active",
	}
	retired := unitInventoryCase{
		CanonicalKey: "js://module/test/case.test.ts#retired",
		Module:       "module", Language: "javascript", Source: "test/case.test.ts",
		Title: "retired", Owner: "owner", Status: "retired", RetiredAt: "2026-07-24", Reason: "obsolete",
	}
	ledger := unitInventoryLedger{SchemaVersion: 1, Cases: []unitInventoryCase{registered, retired}}
	passing := nodeUnitManifest{Module: "module", Tests: []nodeUnitResult{{CanonicalKey: registered.CanonicalKey}}}
	if err := compareUnitInventory(ledger, []nodeUnitManifest{passing}); err != nil {
		t.Fatal(err)
	}
	if err := compareUnitInventory(ledger, []nodeUnitManifest{{Module: "module"}}); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("missing active case error = %v", err)
	}
	newCase := nodeUnitManifest{Module: "module", Tests: []nodeUnitResult{{CanonicalKey: "js://module/test/case.test.ts#new"}}}
	if err := compareUnitInventory(ledger, []nodeUnitManifest{newCase}); err == nil || !strings.Contains(err.Error(), "unregistered") {
		t.Fatalf("new case error = %v", err)
	}
}

func TestValidateCriticalNodeTestsRequiresPass(t *testing.T) {
	key := "js://module/test/case.test.ts#critical"
	module := coverageModule{CriticalCases: []coverageCriticalCase{{TestID: "UNIT-X-CASE-001", CanonicalKey: key}}}
	for _, status := range []string{"FAIL", "SKIP", "TODO"} {
		if err := validateCriticalNodeTests(module, []nodeUnitResult{{CanonicalKey: key, TestID: "UNIT-X-CASE-001", Status: status}}); err == nil {
			t.Fatalf("critical status %s unexpectedly passed", status)
		}
	}
	if err := validateCriticalNodeTests(module, []nodeUnitResult{{CanonicalKey: key, TestID: "UNIT-X-CASE-001", Status: "PASS"}}); err != nil {
		t.Fatal(err)
	}
}
