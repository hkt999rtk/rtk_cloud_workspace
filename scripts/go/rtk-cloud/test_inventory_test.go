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

func TestRenderNodeJUnitAndUnitInventory(t *testing.T) {
	units := []nodeUnitResult{
		{
			CanonicalKey: "js://module/test/case.test.ts#passes",
			Module:       "module",
			Language:     "javascript",
			Source:       "test/case.test.ts",
			Title:        "passes",
			DurationMS:   0.625,
			Status:       "PASS",
			TestID:       "UNIT-X-CASE-001",
		},
		{
			CanonicalKey: "js://module/test/case.test.ts#fails",
			Module:       "module",
			Language:     "javascript",
			Source:       "test/case.test.ts",
			Title:        "fails",
			DurationMS:   1.5,
			Status:       "FAIL",
			Error:        "unsafe <detail>",
		},
		{
			CanonicalKey: "js://module/test/case.test.ts#skips",
			Module:       "module",
			Language:     "javascript",
			Source:       "test/case.test.ts",
			Title:        "skips",
			Status:       "SKIP",
		},
		{
			CanonicalKey: "js://module/test/case.test.ts#todo",
			Module:       "module",
			Language:     "javascript",
			Source:       "test/case.test.ts",
			Title:        "todo",
			Status:       "TODO",
		},
	}
	junit := string(renderNodeJUnit("module", units))
	for _, expected := range []string{
		`tests="4" failures="1" skipped="2"`,
		`time="0.001"`,
		`<failure message="unsafe &lt;detail&gt;"/>`,
		`<skipped message="skip"/>`,
		`<skipped message="todo"/>`,
	} {
		if !strings.Contains(junit, expected) {
			t.Fatalf("JUnit missing %q:\n%s", expected, junit)
		}
	}

	ledger := unitInventoryLedger{SchemaVersion: 1, Cases: []unitInventoryCase{
		{
			CanonicalKey: units[0].CanonicalKey,
			Module:       "module",
			Language:     "javascript",
			Source:       units[0].Source,
			Title:        "passes | safely",
			Owner:        "owner",
			Status:       "active",
			TestID:       units[0].TestID,
		},
	}}
	rendered := string(renderUnitInventory(ledger))
	if !strings.Contains(rendered, "Generated from `tests/unit-inventory.yaml`") ||
		!strings.Contains(rendered, "passes \\| safely") ||
		!strings.Contains(rendered, "UNIT-X-CASE-001") {
		t.Fatalf("unexpected rendered inventory:\n%s", rendered)
	}
}

func TestRunTestInventoryRejectsInvalidArguments(t *testing.T) {
	for name, args := range map[string][]string{
		"missing command": nil,
		"unknown command": {"unknown"},
		"missing run":     {"check"},
		"missing update":  {"update"},
		"invalid render":  {"render", "--unknown"},
	} {
		t.Run(name, func(t *testing.T) {
			if err := runTestInventory(args); err == nil {
				t.Fatal("invalid inventory arguments unexpectedly passed")
			}
		})
	}
}

func TestValidateUnitInventoryRejectsInvalidLedgerEntries(t *testing.T) {
	workspace := t.TempDir()
	module := coverageModule{
		Name:            "module",
		Kind:            "node",
		Path:            "module",
		Owner:           "owner",
		SourceTestGlobs: []string{"test/*.test.js"},
	}
	source := "test/case.test.js"
	if err := os.MkdirAll(filepath.Join(workspace, module.Path, "test"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, module.Path, source), []byte("// test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	valid := unitInventoryCase{
		CanonicalKey: canonicalJSKey(module.Name, source, "case"),
		Module:       module.Name,
		Language:     "javascript",
		Source:       source,
		Title:        "case",
		Owner:        module.Owner,
		Status:       "active",
	}
	cfg := coverageConfig{Modules: []coverageModule{module}}
	clone := func(item unitInventoryCase) unitInventoryCase { return item }
	tests := map[string]unitInventoryLedger{}

	item := clone(valid)
	item.Language = "go"
	tests["language"] = unitInventoryLedger{Cases: []unitInventoryCase{item}}
	item = clone(valid)
	item.CanonicalKey += "-wrong"
	tests["canonical"] = unitInventoryLedger{Cases: []unitInventoryCase{item}}
	tests["duplicate"] = unitInventoryLedger{Cases: []unitInventoryCase{valid, valid}}
	item = clone(valid)
	item.Owner = "wrong"
	tests["owner"] = unitInventoryLedger{Cases: []unitInventoryCase{item}}
	item = clone(valid)
	item.Source = "test/missing.test.js"
	item.CanonicalKey = canonicalJSKey(item.Module, item.Source, item.Title)
	tests["missing source"] = unitInventoryLedger{Cases: []unitInventoryCase{item}}
	item = clone(valid)
	item.RetiredAt = "2026-07-24"
	item.Reason = "not retired"
	tests["active retirement"] = unitInventoryLedger{Cases: []unitInventoryCase{item}}
	item = clone(valid)
	item.Status = "retired"
	tests["retired metadata"] = unitInventoryLedger{Cases: []unitInventoryCase{item}}
	item = clone(valid)
	item.Status = "unknown"
	tests["status"] = unitInventoryLedger{Cases: []unitInventoryCase{item}}
	item = clone(valid)
	item.TestID = "UNIT-X-CASE-001"
	tests["unexpected permanent id"] = unitInventoryLedger{Cases: []unitInventoryCase{item}}

	for name, ledger := range tests {
		t.Run(name, func(t *testing.T) {
			if err := validateUnitInventory(workspace, ledger, cfg); err == nil {
				t.Fatal("invalid ledger unexpectedly passed")
			}
		})
	}

	criticalModule := module
	criticalModule.CriticalCases = []coverageCriticalCase{{
		TestID:       "UNIT-X-CASE-001",
		CanonicalKey: valid.CanonicalKey,
	}}
	if err := validateUnitInventory(workspace, unitInventoryLedger{}, coverageConfig{
		Modules: []coverageModule{criticalModule},
	}); err == nil {
		t.Fatal("missing critical mapping unexpectedly passed")
	}
}

func TestUpdateAndCheckUnitInventoryAgainstRunManifests(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	tempWorkspace := t.TempDir()
	entries, err := os.ReadDir(workspace)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "tests" || entry.Name() == "docs" || entry.Name() == ".artifacts" {
			continue
		}
		if err := os.Symlink(filepath.Join(workspace, entry.Name()), filepath.Join(tempWorkspace, entry.Name())); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(tempWorkspace, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tempWorkspace, "docs"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"catalog.yaml", "coverage.yaml", "unit-inventory.yaml"} {
		raw, err := os.ReadFile(filepath.Join(workspace, "tests", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(tempWorkspace, "tests", name), raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	renderedInventory, err := os.ReadFile(filepath.Join(workspace, "docs", "unit-test-inventory.md"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tempWorkspace, "docs", "unit-test-inventory.md"), renderedInventory, 0o644); err != nil {
		t.Fatal(err)
	}

	ledger, err := loadUnitInventory(tempWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	originalCount := len(ledger.Cases)
	testsByModule := map[string][]nodeUnitResult{}
	for _, item := range ledger.Cases {
		if item.Status != "active" {
			continue
		}
		testsByModule[item.Module] = append(testsByModule[item.Module], nodeUnitResult{
			CanonicalKey: item.CanonicalKey,
			Module:       item.Module,
			Language:     item.Language,
			Source:       item.Source,
			Title:        item.Title,
			Status:       "PASS",
			TestID:       item.TestID,
		})
	}
	runDir := filepath.Join(t.TempDir(), "coverage")
	writeManifests := func() {
		t.Helper()
		for module, units := range testsByModule {
			path := filepath.Join(runDir, "modules", module, "unit-manifest.json")
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := writeJSON(path, nodeUnitManifest{
				SchemaVersion: 1,
				Module:        module,
				Profile:       "unit",
				Commit:        "0123456789012345678901234567890123456789",
				Tests:         units,
			}); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeManifests()
	if err := runTestInventoryAtWorkspace(tempWorkspace, []string{"check", "--from-run", runDir}); err != nil {
		t.Fatal(err)
	}
	if err := runTestInventory([]string{"check", "--from-run", runDir}); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadCoverageConfig(tempWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	manifests, err := loadNodeUnitManifests(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := compareUnitInventoryMustPass(tempWorkspace, cfg, manifests); err != nil {
		t.Fatal(err)
	}

	const newTitle = "inventory update preserves newly observed case"
	base := ledger.Cases[0]
	testsByModule[base.Module] = append(testsByModule[base.Module], nodeUnitResult{
		CanonicalKey: canonicalJSKey(base.Module, base.Source, newTitle),
		Module:       base.Module,
		Language:     "javascript",
		Source:       base.Source,
		Title:        newTitle,
		Status:       "PASS",
	})
	writeManifests()

	if err := runTestInventoryAtWorkspace(tempWorkspace, []string{"update", "--from-run", runDir}); err != nil {
		t.Fatal(err)
	}
	if err := updateUnitInventory(tempWorkspace, runDir); err != nil {
		t.Fatal(err)
	}
	updated, err := loadUnitInventory(tempWorkspace)
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Cases) != originalCount+1 {
		t.Fatalf("updated inventory has %d cases, want %d", len(updated.Cases), originalCount+1)
	}
	if err := runTestInventoryAtWorkspace(tempWorkspace, []string{"render"}); err != nil {
		t.Fatal(err)
	}
	if err := checkUnitInventory(tempWorkspace, runDir, false); err != nil {
		t.Fatal(err)
	}
}
