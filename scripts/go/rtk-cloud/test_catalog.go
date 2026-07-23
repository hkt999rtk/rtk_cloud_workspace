package main

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

var testCaseIDPattern = regexp.MustCompile(`^(SVC|E2E|UI|LIVE|LOAD)-[A-Z0-9]+-[A-Z0-9]+-[0-9]{3}$`)
var playwrightTestPattern = regexp.MustCompile(`(?m)^\s*test\(\s*['"\x60](\[[A-Z0-9-]+\])`)
var playwrightAnyTestPattern = regexp.MustCompile(`(?m)^\s*test\(\s*['"\x60]`)

type testCatalog struct {
	SchemaVersion int               `yaml:"schema_version"`
	Cases         []testCatalogCase `yaml:"cases"`
}

type testCatalogCase struct {
	ID           string   `yaml:"id"`
	Title        string   `yaml:"title"`
	Layer        string   `yaml:"layer"`
	Owner        string   `yaml:"owner"`
	Source       string   `yaml:"source"`
	Selector     string   `yaml:"selector"`
	Method       string   `yaml:"method"`
	Runner       string   `yaml:"runner"`
	Targets      []string `yaml:"targets,omitempty"`
	Environments []string `yaml:"environments"`
	Tags         []string `yaml:"tags,omitempty"`
	Evidence     []string `yaml:"evidence,omitempty"`
	Status       string   `yaml:"status"`
}

var catalogLayers = map[string]string{"service": "SVC-", "e2e": "E2E-", "ui": "UI-", "live": "LIVE-", "load": "LOAD-"}
var catalogOwners = map[string]bool{
	"cloud_platform": true, "factory_enroll": true, "home_cloud": true, "provisioning": true,
	"rtk_account_manager": true, "rtk_cloud_admin": true, "rtk_cloud_client": true,
	"rtk_cloud_frontend": true, "rtk_cloud_logger": true, "rtk_video_cloud": true, "video_cloud": true,
}
var catalogRunners = map[string]bool{"test-services": true, "test-e2e": true, "test-ui": true, "test-live": true}
var catalogTargets = map[string]bool{"desktop": true, "mobile": true, "ios": true, "android": true}
var catalogEnvironments = map[string]bool{"local": true, "ci": true, "staging": true}
var catalogEvidence = map[string]bool{
	"screenshot": true, "cloud-evidence": true, "junit": true, "json": true,
	"markdown": true, "logs": true, "console": true,
}

func runTestCatalog(args []string) error {
	action := "check"
	if len(args) > 0 {
		action = args[0]
	}
	if len(args) > 1 {
		return errors.New("usage: test-catalog [check|render]")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	switch action {
	case "check":
		return checkTestCatalog(workspace, true)
	case "render":
		catalog, err := loadAndValidateTestCatalog(workspace)
		if err != nil {
			return err
		}
		path := filepath.Join(workspace, "docs", "test-catalog.md")
		return os.WriteFile(path, renderTestCatalog(catalog), 0o644)
	default:
		return fmt.Errorf("unknown test-catalog action %q; use check or render", action)
	}
}

func checkTestCatalog(workspace string, checkRendered bool) error {
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		return err
	}
	if !checkRendered {
		return nil
	}
	path := filepath.Join(workspace, "docs", "test-catalog.md")
	actual, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read generated test catalog %s: %w", path, err)
	}
	expected := renderTestCatalog(catalog)
	if !bytes.Equal(actual, expected) {
		return errors.New("docs/test-catalog.md is stale; run rtk-cloud test-catalog render")
	}
	fmt.Fprintf(os.Stdout, "test catalog valid: %d cases\n", len(catalog.Cases))
	return nil
}

func loadAndValidateTestCatalog(workspace string) (testCatalog, error) {
	return loadAndValidateTestCatalogForRunner(workspace, "")
}

func loadAndValidateTestCatalogForRunner(workspace, sourceRunner string) (testCatalog, error) {
	path := filepath.Join(workspace, "tests", "catalog.yaml")
	raw, err := os.ReadFile(path)
	if err != nil {
		return testCatalog{}, fmt.Errorf("read test catalog: %w", err)
	}
	var catalog testCatalog
	if err := yaml.Unmarshal(raw, &catalog); err != nil {
		return testCatalog{}, fmt.Errorf("parse test catalog: %w", err)
	}
	if catalog.SchemaVersion != 1 {
		return testCatalog{}, fmt.Errorf("test catalog schema_version=%d, want 1", catalog.SchemaVersion)
	}
	seen := map[string]bool{}
	catalogUI := map[string]bool{}
	for i, tc := range catalog.Cases {
		prefix := fmt.Sprintf("cases[%d]", i)
		if !testCaseIDPattern.MatchString(tc.ID) {
			return testCatalog{}, fmt.Errorf("%s id %q must match %s", prefix, tc.ID, testCaseIDPattern)
		}
		if seen[tc.ID] {
			return testCatalog{}, fmt.Errorf("duplicate test id %q", tc.ID)
		}
		seen[tc.ID] = true
		if strings.TrimSpace(tc.Title) == "" || strings.TrimSpace(tc.Owner) == "" || strings.TrimSpace(tc.Source) == "" || strings.TrimSpace(tc.Selector) == "" || strings.TrimSpace(tc.Method) == "" || strings.TrimSpace(tc.Runner) == "" {
			return testCatalog{}, fmt.Errorf("%s %s requires title, owner, source, selector, method, and runner", prefix, tc.ID)
		}
		idPrefix, ok := catalogLayers[tc.Layer]
		if !ok || !strings.HasPrefix(tc.ID, idPrefix) {
			return testCatalog{}, fmt.Errorf("%s %s has invalid layer %q for its ID", prefix, tc.ID, tc.Layer)
		}
		if !catalogOwners[tc.Owner] {
			return testCatalog{}, fmt.Errorf("%s %s has unknown owner %q", prefix, tc.ID, tc.Owner)
		}
		if !catalogRunners[tc.Runner] {
			return testCatalog{}, fmt.Errorf("%s %s has unknown runner %q", prefix, tc.ID, tc.Runner)
		}
		for _, target := range tc.Targets {
			if !catalogTargets[target] {
				return testCatalog{}, fmt.Errorf("%s %s has unknown target %q", prefix, tc.ID, target)
			}
		}
		for _, environment := range tc.Environments {
			if !catalogEnvironments[environment] {
				return testCatalog{}, fmt.Errorf("%s %s has unknown environment %q", prefix, tc.ID, environment)
			}
		}
		for _, evidence := range tc.Evidence {
			if !catalogEvidence[evidence] {
				return testCatalog{}, fmt.Errorf("%s %s has unknown evidence type %q", prefix, tc.ID, evidence)
			}
		}
		if tc.Status != "active" && tc.Status != "retired" {
			return testCatalog{}, fmt.Errorf("%s %s has unsupported status %q", prefix, tc.ID, tc.Status)
		}
		if tc.Status == "retired" {
			continue
		}
		if len(tc.Environments) == 0 {
			return testCatalog{}, fmt.Errorf("%s %s requires at least one environment", prefix, tc.ID)
		}
		var source []byte
		validateSource := sourceRunner == "" || tc.Runner == sourceRunner
		if validateSource {
			sourcePath := filepath.Join(workspace, filepath.FromSlash(tc.Source))
			source, err = os.ReadFile(sourcePath)
			if err != nil {
				return testCatalog{}, fmt.Errorf("%s %s source %s: %w", prefix, tc.ID, tc.Source, err)
			}
			if !bytes.Contains(source, []byte(tc.Selector)) {
				return testCatalog{}, fmt.Errorf("%s %s selector %q not found in %s", prefix, tc.ID, tc.Selector, tc.Source)
			}
		}
		if tc.Layer == "ui" {
			if !strings.HasPrefix(tc.ID, "UI-") {
				return testCatalog{}, fmt.Errorf("%s %s layer ui requires UI- id", prefix, tc.ID)
			}
			if !catalogContainsString(tc.Evidence, "screenshot") {
				return testCatalog{}, fmt.Errorf("%s %s UI case requires screenshot evidence", prefix, tc.ID)
			}
			if !catalogContainsString(tc.Targets, "desktop") && !catalogContainsString(tc.Targets, "mobile") {
				return testCatalog{}, fmt.Errorf("%s %s UI case requires desktop or mobile target", prefix, tc.ID)
			}
			if validateSource && !bytes.Contains(source, []byte("["+tc.ID+"]")) {
				return testCatalog{}, fmt.Errorf("%s %s source title is missing [%s]", prefix, tc.ID, tc.ID)
			}
			catalogUI[tc.ID] = true
		}
	}
	if sourceRunner == "" || sourceRunner == "test-ui" {
		if err := validatePlaywrightCatalogCoverage(workspace, catalogUI); err != nil {
			return testCatalog{}, err
		}
	}
	return catalog, nil
}

func validatePlaywrightCatalogCoverage(workspace string, catalogUI map[string]bool) error {
	files, err := filepath.Glob(filepath.Join(workspace, "repos", "rtk_cloud_admin", "web", "e2e", "*.spec.mjs"))
	if err != nil {
		return err
	}
	found := map[string]bool{}
	for _, path := range files {
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		matches := playwrightTestPattern.FindAllSubmatch(raw, -1)
		testCalls := len(playwrightAnyTestPattern.FindAll(raw, -1))
		if len(matches) != testCalls {
			return fmt.Errorf("%s has %d test() calls but %d Test ID prefixes", filepath.ToSlash(path), testCalls, len(matches))
		}
		for _, match := range matches {
			id := strings.Trim(string(match[1]), "[]")
			if !catalogUI[id] {
				return fmt.Errorf("Playwright test %s in %s is not in tests/catalog.yaml", id, filepath.ToSlash(path))
			}
			found[id] = true
		}
	}
	for id := range catalogUI {
		if !found[id] {
			return fmt.Errorf("catalog UI test %s is not present in Playwright sources", id)
		}
	}
	return nil
}

func renderTestCatalog(catalog testCatalog) []byte {
	cases := append([]testCatalogCase(nil), catalog.Cases...)
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	var b strings.Builder
	fmt.Fprintln(&b, "# Test Catalog")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "> Generated from `tests/catalog.yaml`; do not edit this file directly.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Test ID | Purpose | Method | Layer | Owner | Targets | Environments | Runner | Evidence | Status |")
	fmt.Fprintln(&b, "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, tc := range cases {
		fmt.Fprintf(&b, "| `%s` | %s | %s | `%s` | `%s` | %s | %s | `%s` | %s | `%s` |\n",
			tc.ID, escapeMarkdownCell(tc.Title), escapeMarkdownCell(tc.Method), tc.Layer, tc.Owner,
			joinCatalogValues(tc.Targets), joinCatalogValues(tc.Environments), tc.Runner,
			joinCatalogValues(tc.Evidence), tc.Status)
	}
	return []byte(b.String())
}

func joinCatalogValues(values []string) string {
	if len(values) == 0 {
		return "—"
	}
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, "`"+value+"`")
	}
	return strings.Join(quoted, ", ")
}

func escapeMarkdownCell(value string) string {
	return strings.ReplaceAll(value, "|", "\\|")
}

func catalogContainsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func expectedUITestIDs(workspace, target, environment string, smokeOnly bool) ([]string, error) {
	catalog, err := loadAndValidateTestCatalogForRunner(workspace, "test-ui")
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, tc := range catalog.Cases {
		if tc.Status != "active" || tc.Layer != "ui" ||
			!catalogContainsString(tc.Targets, target) || !catalogContainsString(tc.Environments, environment) {
			continue
		}
		if smokeOnly && !catalogContainsString(tc.Tags, "smoke") {
			continue
		}
		ids = append(ids, tc.ID)
	}
	sort.Strings(ids)
	return ids, nil
}
