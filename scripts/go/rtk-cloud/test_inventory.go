package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

type unitInventoryLedger struct {
	SchemaVersion int                 `yaml:"schema_version" json:"schema_version"`
	Cases         []unitInventoryCase `yaml:"cases" json:"cases"`
}

type unitInventoryCase struct {
	CanonicalKey string `yaml:"canonical_key" json:"canonical_key"`
	Module       string `yaml:"module" json:"module"`
	Language     string `yaml:"language" json:"language"`
	Source       string `yaml:"source" json:"source"`
	Title        string `yaml:"title" json:"title"`
	Owner        string `yaml:"owner" json:"owner"`
	Status       string `yaml:"status" json:"status"`
	TestID       string `yaml:"test_id,omitempty" json:"test_id,omitempty"`
	RetiredAt    string `yaml:"retired_at,omitempty" json:"retired_at,omitempty"`
	Reason       string `yaml:"reason,omitempty" json:"reason,omitempty"`
}

type nodeUnitResult struct {
	CanonicalKey string  `json:"canonical_key"`
	Module       string  `json:"module"`
	Language     string  `json:"language"`
	Source       string  `json:"source"`
	Title        string  `json:"title"`
	StartedAt    string  `json:"started_at"`
	CompletedAt  string  `json:"completed_at"`
	DurationMS   float64 `json:"duration_ms"`
	Status       string  `json:"status"`
	TestID       string  `json:"test_id,omitempty"`
	Error        string  `json:"error,omitempty"`
}

type nodeReporterEvent struct {
	SchemaVersion int             `json:"schema_version"`
	Event         string          `json:"event"`
	Name          string          `json:"name"`
	File          string          `json:"file"`
	Line          int             `json:"line"`
	Column        int             `json:"column"`
	Nesting       int             `json:"nesting"`
	TestType      string          `json:"test_type"`
	DurationMS    float64         `json:"duration_ms"`
	Skip          json.RawMessage `json:"skip"`
	Todo          json.RawMessage `json:"todo"`
	Error         *struct {
		Message string `json:"message"`
	} `json:"error"`
	Timestamp string `json:"timestamp"`
}

type nodeUnitManifest struct {
	SchemaVersion int              `json:"schema_version"`
	Module        string           `json:"module"`
	Profile       string           `json:"profile"`
	Commit        string           `json:"commit"`
	Tests         []nodeUnitResult `json:"tests"`
}

func runTestInventory(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: test-inventory check|update|render [--from-run RUN_DIR]")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	switch args[0] {
	case "check":
		flags := flag.NewFlagSet("test-inventory check", flag.ContinueOnError)
		fromRun := flags.String("from-run", "", "coverage run directory containing unit manifests")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*fromRun) == "" {
			return errors.New("--from-run is required")
		}
		return checkUnitInventory(workspace, *fromRun, false)
	case "update":
		flags := flag.NewFlagSet("test-inventory update", flag.ContinueOnError)
		fromRun := flags.String("from-run", "", "coverage run directory containing unit manifests")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		if strings.TrimSpace(*fromRun) == "" {
			return errors.New("--from-run is required")
		}
		return updateUnitInventory(workspace, *fromRun)
	case "render":
		flags := flag.NewFlagSet("test-inventory render", flag.ContinueOnError)
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		ledger, err := loadUnitInventory(workspace)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(workspace, "docs", "unit-test-inventory.md"), renderUnitInventory(ledger), 0o644)
	default:
		return fmt.Errorf("unknown test-inventory command %q", args[0])
	}
}

func loadUnitInventory(workspace string) (unitInventoryLedger, error) {
	path := filepath.Join(workspace, "tests", "unit-inventory.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return unitInventoryLedger{}, fmt.Errorf("read unit inventory: %w", err)
	}
	var ledger unitInventoryLedger
	if err := yaml.Unmarshal(raw, &ledger); err != nil {
		return unitInventoryLedger{}, fmt.Errorf("parse unit inventory: %w", err)
	}
	if ledger.SchemaVersion != 1 {
		return unitInventoryLedger{}, fmt.Errorf("unit inventory schema_version must be 1, got %d", ledger.SchemaVersion)
	}
	return ledger, nil
}

func validateUnitInventory(workspace string, ledger unitInventoryLedger, cfg coverageConfig) error {
	moduleByName := map[string]coverageModule{}
	criticalByKey := map[string]string{}
	criticalIDKeys := map[string]string{}
	for _, module := range cfg.Modules {
		if module.Kind != "node" {
			continue
		}
		moduleByName[module.Name] = module
		for _, critical := range module.CriticalCases {
			if previous := criticalIDKeys[critical.TestID]; previous != "" && previous != critical.CanonicalKey {
				return fmt.Errorf("critical Test ID %s is reused", critical.TestID)
			}
			criticalByKey[critical.CanonicalKey] = critical.TestID
			criticalIDKeys[critical.TestID] = critical.CanonicalKey
		}
	}
	keys := map[string]bool{}
	testIDs := map[string]string{}
	for index, item := range ledger.Cases {
		prefix := fmt.Sprintf("unit inventory case %d", index+1)
		module, ok := moduleByName[item.Module]
		if !ok || item.Language != "javascript" {
			return fmt.Errorf("%s has invalid module or language", prefix)
		}
		if item.CanonicalKey != canonicalJSKey(item.Module, item.Source, item.Title) {
			return fmt.Errorf("%s canonical key does not match module/source/title", prefix)
		}
		if keys[item.CanonicalKey] {
			return fmt.Errorf("duplicate unit inventory canonical key %q", item.CanonicalKey)
		}
		keys[item.CanonicalKey] = true
		if item.Owner != module.Owner || !matchesAnyGlob(item.Source, module.SourceTestGlobs) {
			return fmt.Errorf("%s has invalid owner or source", prefix)
		}
		if !exists(filepath.Join(workspace, module.Path, filepath.FromSlash(item.Source))) && item.Status == "active" {
			return fmt.Errorf("%s active source does not exist: %s", prefix, item.Source)
		}
		switch item.Status {
		case "active":
			if item.RetiredAt != "" || item.Reason != "" {
				return fmt.Errorf("%s active case cannot have retirement metadata", prefix)
			}
		case "retired":
			if item.RetiredAt == "" || item.Reason == "" {
				return fmt.Errorf("%s retired case requires retired_at and reason", prefix)
			}
		default:
			return fmt.Errorf("%s has invalid status %q", prefix, item.Status)
		}
		if item.TestID != "" {
			if previous := testIDs[item.TestID]; previous != "" && previous != item.CanonicalKey {
				return fmt.Errorf("permanent Test ID %s is reused", item.TestID)
			}
			testIDs[item.TestID] = item.CanonicalKey
		}
		if expected := criticalByKey[item.CanonicalKey]; expected != item.TestID {
			return fmt.Errorf("%s critical Test ID mapping mismatch: got %q, want %q", prefix, item.TestID, expected)
		}
	}
	for key, testID := range criticalByKey {
		if !keys[key] || testIDs[testID] != key {
			return fmt.Errorf("critical Test ID %s has no active inventory mapping to %s", testID, key)
		}
	}
	return nil
}

func checkUnitInventory(workspace, fromRun string, requireRendered bool) error {
	cfg, err := loadCoverageConfig(workspace)
	if err != nil {
		return err
	}
	ledger, err := loadUnitInventory(workspace)
	if err != nil {
		return err
	}
	if err := validateUnitInventory(workspace, ledger, cfg); err != nil {
		return err
	}
	if strings.TrimSpace(fromRun) != "" {
		manifests, err := loadNodeUnitManifests(fromRun)
		if err != nil {
			return err
		}
		if err := compareUnitInventory(ledger, manifests); err != nil {
			return err
		}
	}
	if requireRendered {
		path := filepath.Join(workspace, "docs", "unit-test-inventory.md")
		current, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read rendered unit inventory: %w", err)
		}
		expected := renderUnitInventory(ledger)
		if string(current) != string(expected) {
			return errors.New("docs/unit-test-inventory.md is stale; run rtk-cloud test-inventory render")
		}
	}
	fmt.Printf("unit inventory valid: %d cases\n", len(ledger.Cases))
	return nil
}

func updateUnitInventory(workspace, fromRun string) error {
	cfg, err := loadCoverageConfig(workspace)
	if err != nil {
		return err
	}
	ledger, err := loadUnitInventory(workspace)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		ledger.SchemaVersion = 1
	}
	manifests, err := loadNodeUnitManifests(fromRun)
	if err != nil {
		return err
	}
	existing := map[string]bool{}
	for _, item := range ledger.Cases {
		existing[item.CanonicalKey] = true
	}
	moduleByName := map[string]coverageModule{}
	for _, module := range cfg.Modules {
		moduleByName[module.Name] = module
	}
	added := 0
	for _, manifest := range manifests {
		module := moduleByName[manifest.Module]
		for _, test := range manifest.Tests {
			if existing[test.CanonicalKey] {
				continue
			}
			ledger.Cases = append(ledger.Cases, unitInventoryCase{
				CanonicalKey: test.CanonicalKey,
				Module:       test.Module,
				Language:     "javascript",
				Source:       test.Source,
				Title:        test.Title,
				Owner:        module.Owner,
				Status:       "active",
				TestID:       test.TestID,
			})
			existing[test.CanonicalKey] = true
			added++
		}
	}
	sort.Slice(ledger.Cases, func(i, j int) bool { return ledger.Cases[i].CanonicalKey < ledger.Cases[j].CanonicalKey })
	if err := validateUnitInventory(workspace, ledger, cfg); err != nil {
		return err
	}
	raw, err := yaml.Marshal(ledger)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(workspace, "tests", "unit-inventory.yaml"), raw, 0o644); err != nil {
		return err
	}
	fmt.Printf("unit inventory updated: %d new cases, %d total; missing cases were preserved\n", added, len(ledger.Cases))
	return nil
}

func loadNodeUnitManifests(root string) ([]nodeUnitManifest, error) {
	root, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	var manifests []nodeUnitManifest
	err = filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || entry.Name() != "unit-manifest.json" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var probe struct {
			Module string `json:"module"`
			Tests  []struct {
				Language string `json:"language"`
			} `json:"tests"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			return fmt.Errorf("parse %s: %w", path, err)
		}
		if len(probe.Tests) == 0 || probe.Tests[0].Language != "javascript" {
			return nil
		}
		var manifest nodeUnitManifest
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return err
		}
		manifests = append(manifests, manifest)
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(manifests) == 0 {
		return nil, errors.New("no JavaScript unit manifests found")
	}
	sort.Slice(manifests, func(i, j int) bool { return manifests[i].Module < manifests[j].Module })
	return manifests, nil
}

func compareUnitInventory(ledger unitInventoryLedger, manifests []nodeUnitManifest) error {
	activeByModule := map[string]map[string]bool{}
	for _, item := range ledger.Cases {
		if item.Status != "active" {
			continue
		}
		if activeByModule[item.Module] == nil {
			activeByModule[item.Module] = map[string]bool{}
		}
		activeByModule[item.Module][item.CanonicalKey] = true
	}
	for _, manifest := range manifests {
		actual := map[string]nodeUnitResult{}
		for _, test := range manifest.Tests {
			if _, duplicate := actual[test.CanonicalKey]; duplicate {
				return fmt.Errorf("%s emitted duplicate test %s", manifest.Module, test.CanonicalKey)
			}
			actual[test.CanonicalKey] = test
			if !activeByModule[manifest.Module][test.CanonicalKey] {
				return fmt.Errorf("%s emitted unregistered test %s", manifest.Module, test.CanonicalKey)
			}
		}
		for key := range activeByModule[manifest.Module] {
			if _, ok := actual[key]; !ok {
				return fmt.Errorf("%s active inventory case is missing from run: %s", manifest.Module, key)
			}
		}
	}
	return nil
}

func compareUnitInventoryMustPass(workspace string, cfg coverageConfig, manifests []nodeUnitManifest) error {
	ledger, err := loadUnitInventory(workspace)
	if err != nil {
		return err
	}
	if err := validateUnitInventory(workspace, ledger, cfg); err != nil {
		return err
	}
	if err := compareUnitInventory(ledger, manifests); err != nil {
		return err
	}
	moduleByName := map[string]coverageModule{}
	for _, module := range cfg.Modules {
		moduleByName[module.Name] = module
	}
	for _, manifest := range manifests {
		if err := validateCriticalNodeTests(moduleByName[manifest.Module], manifest.Tests); err != nil {
			return err
		}
	}
	return nil
}

func canonicalJSKey(module, source, title string) string {
	return fmt.Sprintf("js://%s/%s#%s", module, filepath.ToSlash(source), url.PathEscape(title))
}

func matchesAnyGlob(path string, globs []string) bool {
	for _, pattern := range globs {
		if matched, _ := filepath.Match(filepath.FromSlash(pattern), filepath.FromSlash(path)); matched {
			return true
		}
	}
	return false
}

func rewriteNodeSource(module coverageModule, runtimePath string) (string, error) {
	runtimePath = filepath.ToSlash(runtimePath)
	source := runtimePath
	for _, rewrite := range module.SourceRewrites {
		from := filepath.ToSlash(rewrite.FromPrefix)
		if !strings.HasPrefix(source, from) {
			continue
		}
		source = filepath.ToSlash(rewrite.ToPrefix) + strings.TrimPrefix(source, from)
		if rewrite.Extension != "" {
			source = strings.TrimSuffix(source, filepath.Ext(source)) + rewrite.Extension
		}
		break
	}
	if !matchesAnyGlob(source, module.SourceTestGlobs) {
		return "", fmt.Errorf("runtime test %s cannot be mapped to a configured source test", runtimePath)
	}
	return source, nil
}

func parseNodeTestEvents(path, moduleDir string, module coverageModule) ([]nodeUnitResult, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	resolvedModuleDir, err := filepath.EvalSymlinks(moduleDir)
	if err != nil {
		return nil, err
	}
	started := map[string]string{}
	results := map[string]nodeUnitResult{}
	titleSources := map[string]bool{}
	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		var event nodeReporterEvent
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, fmt.Errorf("parse Node TestStream event: %w", err)
		}
		if event.SchemaVersion != 1 || event.Name == "" || event.File == "" || event.Timestamp == "" {
			return nil, errors.New("malformed Node TestStream event")
		}
		eventFile, err := filepath.EvalSymlinks(event.File)
		if err != nil {
			return nil, err
		}
		runtimeRel, err := filepath.Rel(resolvedModuleDir, eventFile)
		if err != nil || strings.HasPrefix(runtimeRel, "..") {
			return nil, fmt.Errorf("Node test source is outside module: %s", event.File)
		}
		runtimeRel = filepath.ToSlash(runtimeRel)
		eventKey := runtimeRel + "\x00" + strconv.Itoa(event.Line) + "\x00" + event.Name
		if event.Event == "start" {
			started[eventKey] = event.Timestamp
			continue
		}
		if event.Event != "pass" && event.Event != "fail" {
			return nil, fmt.Errorf("unsupported Node TestStream event %q", event.Event)
		}
		if event.TestType != "test" {
			continue
		}
		startedAt := started[eventKey]
		if startedAt == "" {
			return nil, fmt.Errorf("Node test completed without start event: %s", event.Name)
		}
		source, err := rewriteNodeSource(module, runtimeRel)
		if err != nil {
			return nil, err
		}
		if !exists(filepath.Join(moduleDir, filepath.FromSlash(source))) {
			return nil, fmt.Errorf("mapped Node source does not exist: %s", source)
		}
		titleKey := source + "\x00" + event.Name
		if titleSources[titleKey] {
			return nil, fmt.Errorf("duplicate Node test title %q in %s", event.Name, source)
		}
		titleSources[titleKey] = true
		status := strings.ToUpper(event.Event)
		if rawTruthy(event.Skip) {
			status = "SKIP"
		} else if rawTruthy(event.Todo) {
			status = "TODO"
		}
		canonical := canonicalJSKey(module.Name, source, event.Name)
		item := nodeUnitResult{
			CanonicalKey: canonical,
			Module:       module.Name,
			Language:     "javascript",
			Source:       source,
			Title:        event.Name,
			StartedAt:    startedAt,
			CompletedAt:  event.Timestamp,
			DurationMS:   event.DurationMS,
			Status:       status,
		}
		if event.Error != nil {
			item.Error = event.Error.Message
		}
		results[canonical] = item
		delete(started, eventKey)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(started) > 0 {
		return nil, errors.New("one or more Node tests emitted no completion event")
	}
	criticalByKey := map[string]string{}
	for _, critical := range module.CriticalCases {
		criticalByKey[critical.CanonicalKey] = critical.TestID
	}
	units := make([]nodeUnitResult, 0, len(results))
	for _, item := range results {
		item.TestID = criticalByKey[item.CanonicalKey]
		units = append(units, item)
	}
	sort.Slice(units, func(i, j int) bool { return units[i].CanonicalKey < units[j].CanonicalKey })
	if len(units) == 0 {
		return nil, errors.New("Node TestStream produced no individual tests")
	}
	return units, nil
}

func rawTruthy(raw json.RawMessage) bool {
	value := strings.TrimSpace(string(raw))
	return value != "" && value != "null" && value != "false" && value != `""`
}

func validateCriticalNodeTests(module coverageModule, units []nodeUnitResult) error {
	byKey := map[string]nodeUnitResult{}
	for _, unit := range units {
		byKey[unit.CanonicalKey] = unit
	}
	for _, critical := range module.CriticalCases {
		unit, ok := byKey[critical.CanonicalKey]
		if !ok {
			return fmt.Errorf("critical test %s did not execute", critical.TestID)
		}
		if unit.TestID != critical.TestID || unit.Status != "PASS" {
			return fmt.Errorf("critical test %s status is %s", critical.TestID, unit.Status)
		}
	}
	return nil
}

func renderNodeJUnit(module string, units []nodeUnitResult) []byte {
	failures, skipped, duration := 0, 0, float64(0)
	for _, unit := range units {
		duration += unit.DurationMS
		if unit.Status == "FAIL" {
			failures++
		}
		if unit.Status == "SKIP" || unit.Status == "TODO" {
			skipped++
		}
	}
	var out strings.Builder
	fmt.Fprintf(&out, "<?xml version=\"1.0\" encoding=\"UTF-8\"?>\n<testsuite name=\"%s\" tests=\"%d\" failures=\"%d\" skipped=\"%d\" time=\"%.3f\">\n",
		html.EscapeString(module), len(units), failures, skipped, float64(duration)/1000)
	for _, unit := range units {
		fmt.Fprintf(&out, "  <testcase classname=\"%s\" name=\"%s\" file=\"%s\" time=\"%.3f\">",
			html.EscapeString(module), html.EscapeString(unit.Title), html.EscapeString(unit.Source), unit.DurationMS/1000)
		switch unit.Status {
		case "FAIL":
			fmt.Fprintf(&out, "<failure message=\"%s\"/>", html.EscapeString(unit.Error))
		case "SKIP", "TODO":
			fmt.Fprintf(&out, "<skipped message=\"%s\"/>", strings.ToLower(unit.Status))
		}
		out.WriteString("</testcase>\n")
	}
	out.WriteString("</testsuite>\n")
	return []byte(out.String())
}

func renderUnitInventory(ledger unitInventoryLedger) []byte {
	var out strings.Builder
	out.WriteString("# Unit Test Inventory\n\n")
	out.WriteString("Generated from `tests/unit-inventory.yaml`. Do not edit this file directly.\n\n")
	out.WriteString("| Canonical key | Module | Source | Title | Owner | Status | Test ID |\n")
	out.WriteString("|---|---|---|---|---|---|---|\n")
	for _, item := range ledger.Cases {
		fmt.Fprintf(&out, "| `%s` | %s | `%s` | %s | %s | %s | %s |\n",
			strings.ReplaceAll(item.CanonicalKey, "|", "\\|"),
			item.Module,
			item.Source,
			strings.ReplaceAll(item.Title, "|", "\\|"),
			item.Owner,
			item.Status,
			item.TestID,
		)
	}
	return []byte(out.String())
}

func writeCrossLanguageUnitInventory(outDir string, cases []coverageCaseResult) error {
	type aggregateItem struct {
		Module       string `json:"module"`
		Language     string `json:"language"`
		CanonicalKey string `json:"canonical_key"`
		Source       string `json:"source"`
		Title        string `json:"title"`
		Status       string `json:"status"`
		TestID       string `json:"test_id,omitempty"`
	}
	var items []aggregateItem
	for _, result := range cases {
		if result.UnitManifestPath == "" {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(outDir, filepath.FromSlash(result.UnitManifestPath)))
		if err != nil {
			return err
		}
		if result.Kind == "node" {
			var manifest nodeUnitManifest
			if err := json.Unmarshal(raw, &manifest); err != nil {
				return err
			}
			for _, unit := range manifest.Tests {
				items = append(items, aggregateItem{
					Module: unit.Module, Language: unit.Language, CanonicalKey: unit.CanonicalKey,
					Source: unit.Source, Title: unit.Title, Status: unit.Status, TestID: unit.TestID,
				})
			}
			continue
		}
		var manifest struct {
			Module string         `json:"module"`
			Tests  []goUnitResult `json:"tests"`
		}
		if err := json.Unmarshal(raw, &manifest); err != nil {
			return err
		}
		for _, unit := range manifest.Tests {
			title := unit.Test
			if unit.Subtest != "" {
				title += "/" + unit.Subtest
			}
			items = append(items, aggregateItem{
				Module: manifest.Module, Language: "go", CanonicalKey: unit.CanonicalKey,
				Source: unit.Source, Title: title, Status: unit.Status, TestID: unit.TestID,
			})
		}
	}
	sort.Slice(items, func(i, j int) bool { return items[i].CanonicalKey < items[j].CanonicalKey })
	return writeJSON(filepath.Join(outDir, "unit-inventory.json"), map[string]any{
		"schema_version": 1,
		"generated_at":   time.Now().UTC().Format(time.RFC3339),
		"tests":          items,
	})
}
