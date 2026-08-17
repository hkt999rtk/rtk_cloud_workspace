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
	"time"

	"gopkg.in/yaml.v3"
)

var testCaseIDPattern = regexp.MustCompile(`^(SVC|UNIT|INT|E2E|UI|LIVE|LOAD)(-[A-Z0-9]+){2,3}-[0-9]{3}$`)
var playwrightTestPattern = regexp.MustCompile(`(?m)^\s*test\(\s*['"\x60](\[[A-Z0-9-]+\])`)
var playwrightAnyTestPattern = regexp.MustCompile(`(?m)^\s*test\(\s*['"\x60]`)

type testCatalog struct {
	SchemaVersion int                  `yaml:"schema_version"`
	Features      []testCatalogFeature `yaml:"features"`
	Cases         []testCatalogCase    `yaml:"cases"`
}

type testCatalogCase struct {
	ID           string   `yaml:"id"`
	Title        string   `yaml:"title"`
	Layer        string   `yaml:"layer"`
	Feature      string   `yaml:"feature,omitempty"`
	Profile      string   `yaml:"profile,omitempty"`
	Covers       []string `yaml:"covers,omitempty"`
	ChangePaths  []string `yaml:"change_paths,omitempty"`
	Owner        string   `yaml:"owner"`
	Source       string   `yaml:"source"`
	Selector     string   `yaml:"selector"`
	Method       string   `yaml:"method"`
	Runner       string   `yaml:"runner"`
	Targets      []string `yaml:"targets,omitempty"`
	Environments []string `yaml:"environments"`
	Tags         []string `yaml:"tags,omitempty"`
	Evidence     []string `yaml:"evidence,omitempty"`
	Verifies     []string `yaml:"verifies,omitempty"`
	Supports     []string `yaml:"supports,omitempty"`
	Status       string   `yaml:"status"`
}

type testCatalogFeature struct {
	ID            string                   `yaml:"id"`
	Title         string                   `yaml:"title"`
	Owner         string                   `yaml:"owner"`
	Risk          string                   `yaml:"risk"`
	ChangePaths   []string                 `yaml:"change_paths"`
	CommitAnchors []string                 `yaml:"commit_anchors,omitempty"`
	Surfaces      []testCatalogSurface     `yaml:"surfaces"`
	Requirements  []testCatalogRequirement `yaml:"requirements"`
	Status        string                   `yaml:"status"`
	SpecSource    specRequirementSource    `yaml:"-"`
}

type testCatalogSurface struct {
	Kind      string `yaml:"kind"`
	Source    string `yaml:"source"`
	Selector  string `yaml:"selector"`
	Exclusion string `yaml:"exclusion,omitempty"`
	Owner     string `yaml:"owner,omitempty"`
	Expires   string `yaml:"expires,omitempty"`
}

type testCatalogRequirement struct {
	ID              string                `yaml:"id" json:"id"`
	Title           string                `yaml:"title" json:"title"`
	AcceptanceLayer string                `yaml:"acceptance_layer" json:"acceptance_layer"`
	OperationModel  string                `yaml:"operation_model,omitempty" json:"operation_model,omitempty"`
	Gate            string                `yaml:"gate" json:"gate"`
	Environments    []string              `yaml:"environments" json:"environments"`
	Targets         []string              `yaml:"targets,omitempty" json:"targets,omitempty"`
	Evidence        []string              `yaml:"evidence,omitempty" json:"evidence,omitempty"`
	FreshnessHours  int                   `yaml:"freshness_hours,omitempty" json:"freshness_hours,omitempty"`
	Required        *bool                 `yaml:"required,omitempty" json:"required,omitempty"`
	Status          string                `yaml:"status" json:"status"`
	Revision        string                `yaml:"-" json:"revision"`
	SpecSource      specRequirementSource `yaml:"-" json:"spec_source"`
}

var catalogLayers = map[string]string{"service": "SVC-", "unit": "UNIT-", "integration": "INT-", "e2e": "E2E-", "ui": "UI-", "live": "LIVE-", "load": "LOAD-"}
var catalogOwners = map[string]bool{
	"cloud_platform": true, "factory_enroll": true, "home_cloud": true, "provisioning": true,
	"rtk_account_manager": true, "rtk_cloud_admin": true, "rtk_cloud_client": true,
	"rtk_cloud_frontend": true, "rtk_cloud_logger": true, "rtk_video_cloud": true, "video_cloud": true,
}
var catalogRunners = map[string]bool{"test-services": true, "test-coverage": true, "test-e2e": true, "test-ui": true, "test-live": true, "test-feature": true, "test-payment": true}
var catalogTargets = map[string]bool{"desktop": true, "mobile": true, "ios": true, "android": true}
var catalogEnvironments = map[string]bool{"local": true, "ci": true, "staging": true}
var catalogProfiles = map[string]bool{"canary": true, "qualification-1k": true, "capacity": true}
var catalogEvidence = map[string]bool{
	"screenshot": true, "cloud-evidence": true, "junit": true, "json": true,
	"markdown": true, "logs": true, "console": true, "pdf": true,
}
var featureIDPattern = regexp.MustCompile(`^FEAT(-[A-Z0-9]+){2,3}-[0-9]{3}$`)
var requirementIDPattern = regexp.MustCompile(`^REQ(-[A-Z0-9]+){2,4}-[0-9]{3}$`)
var catalogFeatureRisks = map[string]bool{"critical": true, "high": true, "normal": true}
var catalogAcceptanceLayers = map[string]bool{"integration": true, "ui": true, "e2e": true, "live": true}
var catalogRequirementGates = map[string]bool{"pr": true, "main": true, "scheduled": true, "operator-release": true}
var catalogSurfaceKinds = map[string]bool{"api-route": true, "ui-route": true, "sdk-api": true, "operator-workflow": true}

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
	if catalog.SchemaVersion != 4 {
		return testCatalog{}, fmt.Errorf("test catalog schema_version=%d, want 4", catalog.SchemaVersion)
	}
	if mode := featureQualificationMode(); mode != "observe" && mode != "required" {
		return testCatalog{}, fmt.Errorf("FEATURE_QUALIFICATION_MODE must be observe or required, got %q", mode)
	}
	if len(catalog.Features) != 0 {
		return testCatalog{}, errors.New("test catalog schema v4 derives features and requirements from registered specs; remove catalog features")
	}
	var inventory specInventory
	if sourceRunner == "" {
		inventory, err = loadSpecInventory(workspace)
	} else {
		inventory, err = loadAvailableSpecInventory(workspace)
	}
	if err != nil {
		return testCatalog{}, err
	}
	catalog.Features = inventory.Features
	if err := validateFeatureRequirements(workspace, catalog, sourceRunner); err != nil {
		return testCatalog{}, err
	}
	requirements := catalogRequirementIndex(catalog)
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
		if tc.Layer != "unit" && tc.Layer != "service" && len(tc.Verifies) == 0 {
			return testCatalog{}, fmt.Errorf("%s %s product case requires verifies", prefix, tc.ID)
		}
		if sourceRunner == "" || tc.Runner == sourceRunner {
			for _, requirementID := range append(append([]string{}, tc.Verifies...), tc.Supports...) {
				if _, ok := requirements[requirementID]; !ok {
					return testCatalog{}, fmt.Errorf("%s %s references unknown requirement %q", prefix, tc.ID, requirementID)
				}
			}
		}
		if tc.Profile != "" && !catalogProfiles[tc.Profile] {
			return testCatalog{}, fmt.Errorf("%s %s has unsupported profile %q", prefix, tc.ID, tc.Profile)
		}
		if tc.Runner == "test-feature" {
			if tc.Feature == "" || tc.Profile == "" {
				return testCatalog{}, fmt.Errorf("%s %s test-feature case requires feature and profile", prefix, tc.ID)
			}
			if tc.Layer != "e2e" && tc.Layer != "live" && tc.Layer != "load" {
				return testCatalog{}, fmt.Errorf("%s %s test-feature runner requires e2e, live, or load layer", prefix, tc.ID)
			}
		}
		if tc.Layer == "load" && (tc.Feature == "" || tc.Profile == "") {
			return testCatalog{}, fmt.Errorf("%s %s load case requires feature and profile", prefix, tc.ID)
		}
		if tc.Profile == "qualification-1k" {
			if tc.Layer != "load" || len(tc.Covers) == 0 || len(tc.ChangePaths) == 0 {
				return testCatalog{}, fmt.Errorf("%s %s qualification-1k case requires load layer, covers, and change_paths", prefix, tc.ID)
			}
			for _, required := range []string{"json", "markdown", "logs"} {
				if !catalogContainsString(tc.Evidence, required) {
					return testCatalog{}, fmt.Errorf("%s %s qualification-1k case requires %s evidence", prefix, tc.ID, required)
				}
			}
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
			if !catalogSelectorExists(workspace, tc, source) {
				return testCatalog{}, fmt.Errorf("%s %s selector %q not found in %s", prefix, tc.ID, tc.Selector, tc.Source)
			}
		}
		if validateSource {
			for _, changePath := range tc.ChangePaths {
				if err := validateCatalogChangePath(workspace, changePath); err != nil {
					return testCatalog{}, fmt.Errorf("%s %s change_path %q: %w", prefix, tc.ID, changePath, err)
				}
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
	if err := validateCatalogRelationships(catalog); err != nil {
		return testCatalog{}, err
	}
	if sourceRunner == "" || sourceRunner == "test-ui" {
		if err := validatePlaywrightCatalogCoverage(workspace, catalogUI); err != nil {
			return testCatalog{}, err
		}
	}
	return catalog, nil
}

func catalogSelectorExists(workspace string, tc testCatalogCase, source []byte) bool {
	if bytes.Contains(source, []byte(tc.Selector)) {
		return true
	}
	if !strings.Contains(tc.Selector, "#") {
		return false
	}
	sourceParts := strings.Split(filepath.ToSlash(tc.Source), "/")
	if len(sourceParts) < 2 || sourceParts[0] != "repos" {
		return false
	}
	repositoryRoot := filepath.Join(workspace, sourceParts[0], sourceParts[1])
	for _, selector := range strings.Split(tc.Selector, ",") {
		packageName, testName, ok := strings.Cut(selector, "#")
		if !ok || !strings.HasPrefix(packageName, "./") || strings.TrimSpace(testName) == "" {
			return false
		}
		packageDir := filepath.Join(repositoryRoot, filepath.FromSlash(strings.TrimPrefix(packageName, "./")))
		entries, err := os.ReadDir(packageDir)
		if err != nil {
			return false
		}
		found := false
		needle := []byte("func " + testName + "(")
		for _, entry := range entries {
			if entry.IsDir() || !strings.HasSuffix(entry.Name(), "_test.go") {
				continue
			}
			raw, err := os.ReadFile(filepath.Join(packageDir, entry.Name()))
			if err == nil && bytes.Contains(raw, needle) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}

func validateCatalogRelationships(catalog testCatalog) error {
	for i, tc := range catalog.Cases {
		if tc.Status != "active" {
			continue
		}
		for _, coveredID := range tc.Covers {
			covered, ok := catalogCaseByID(catalog.Cases, coveredID)
			if !ok || covered.Status != "active" {
				return fmt.Errorf("cases[%d] %s covers unknown or inactive case %q", i, tc.ID, coveredID)
			}
			if tc.Layer != "load" || covered.Layer != "e2e" {
				return fmt.Errorf("cases[%d] %s covers %s; covers requires load -> e2e", i, tc.ID, coveredID)
			}
			if tc.Feature == "" || tc.Feature != covered.Feature {
				return fmt.Errorf("cases[%d] %s feature %q does not match covered case %s feature %q", i, tc.ID, tc.Feature, coveredID, covered.Feature)
			}
		}
	}
	if featureQualificationMode() == "required" {
		if err := validateRequirementProofMappings(catalog); err != nil {
			return err
		}
	}
	return nil
}

func featureQualificationMode() string {
	mode := strings.TrimSpace(os.Getenv("FEATURE_QUALIFICATION_MODE"))
	if mode == "" {
		return "observe"
	}
	return mode
}

func catalogRequirementIndex(catalog testCatalog) map[string]testCatalogRequirement {
	out := map[string]testCatalogRequirement{}
	for _, feature := range catalog.Features {
		for _, requirement := range feature.Requirements {
			out[requirement.ID] = requirement
		}
	}
	return out
}

func catalogFeatureByRequirement(catalog testCatalog) map[string]testCatalogFeature {
	out := map[string]testCatalogFeature{}
	for _, feature := range catalog.Features {
		for _, requirement := range feature.Requirements {
			out[requirement.ID] = feature
		}
	}
	return out
}

func requirementRequired(requirement testCatalogRequirement) bool {
	return requirement.Required == nil || *requirement.Required
}

func validateFeatureRequirements(workspace string, catalog testCatalog, sourceRunner string) error {
	if len(catalog.Features) == 0 {
		return errors.New("test catalog requires at least one feature")
	}
	featureIDs := map[string]bool{}
	requirementIDs := map[string]bool{}
	for i, feature := range catalog.Features {
		prefix := fmt.Sprintf("features[%d]", i)
		if !featureIDPattern.MatchString(feature.ID) {
			return fmt.Errorf("%s id %q is invalid", prefix, feature.ID)
		}
		if featureIDs[feature.ID] {
			return fmt.Errorf("duplicate feature id %q", feature.ID)
		}
		featureIDs[feature.ID] = true
		if strings.TrimSpace(feature.Title) == "" || !catalogOwners[feature.Owner] || !catalogFeatureRisks[feature.Risk] {
			return fmt.Errorf("%s %s requires title, known owner, and valid risk", prefix, feature.ID)
		}
		if feature.Status != "active" && feature.Status != "planned" && feature.Status != "review_required" && feature.Status != "deprecated" && feature.Status != "retired" {
			return fmt.Errorf("%s %s has unsupported status %q", prefix, feature.ID, feature.Status)
		}
		if feature.Status != "active" {
			continue
		}
		if len(feature.ChangePaths) == 0 || len(feature.Surfaces) == 0 || len(feature.Requirements) == 0 {
			return fmt.Errorf("%s %s requires change_paths, surfaces, and requirements", prefix, feature.ID)
		}
		for _, changePath := range feature.ChangePaths {
			if sourceRunner == "" {
				if err := validateCatalogChangePath(workspace, changePath); err != nil {
					return fmt.Errorf("%s %s change_path %q: %w", prefix, feature.ID, changePath, err)
				}
			}
		}
		for j, surface := range feature.Surfaces {
			surfacePrefix := fmt.Sprintf("%s.surfaces[%d]", prefix, j)
			if !catalogSurfaceKinds[surface.Kind] || strings.TrimSpace(surface.Source) == "" || strings.TrimSpace(surface.Selector) == "" {
				return fmt.Errorf("%s requires known kind, source, and selector", surfacePrefix)
			}
			if surface.Exclusion != "" {
				if !catalogOwners[surface.Owner] || strings.TrimSpace(surface.Expires) == "" {
					return fmt.Errorf("%s exclusion requires owner and expires", surfacePrefix)
				}
				expires, err := time.Parse("2006-01-02", surface.Expires)
				if err != nil {
					return fmt.Errorf("%s exclusion expires must be YYYY-MM-DD", surfacePrefix)
				}
				if expires.Before(time.Now().UTC().Truncate(24 * time.Hour)) {
					return fmt.Errorf("%s exclusion expired on %s", surfacePrefix, surface.Expires)
				}
			}
			if sourceRunner == "" {
				raw, err := os.ReadFile(filepath.Join(workspace, filepath.FromSlash(surface.Source)))
				if err != nil {
					return fmt.Errorf("%s source: %w", surfacePrefix, err)
				}
				if !bytes.Contains(raw, []byte(surface.Selector)) {
					return fmt.Errorf("%s selector %q not found in %s", surfacePrefix, surface.Selector, surface.Source)
				}
			}
		}
		for j, requirement := range feature.Requirements {
			requirementPrefix := fmt.Sprintf("%s.requirements[%d]", prefix, j)
			if !requirementIDPattern.MatchString(requirement.ID) || requirementIDs[requirement.ID] {
				return fmt.Errorf("%s id %q is invalid or reused", requirementPrefix, requirement.ID)
			}
			requirementIDs[requirement.ID] = true
			if strings.TrimSpace(requirement.Title) == "" || !catalogAcceptanceLayers[requirement.AcceptanceLayer] || !catalogRequirementGates[requirement.Gate] {
				return fmt.Errorf("%s %s requires title, acceptance_layer, and gate", requirementPrefix, requirement.ID)
			}
			if requirement.Status != "active" && requirement.Status != "planned" && requirement.Status != "review_required" && requirement.Status != "deprecated" && requirement.Status != "retired" {
				return fmt.Errorf("%s %s has unsupported status %q", requirementPrefix, requirement.ID, requirement.Status)
			}
			if requirement.Status != "active" {
				continue
			}
			if len(requirement.Environments) == 0 {
				return fmt.Errorf("%s %s requires environments", requirementPrefix, requirement.ID)
			}
			for _, environment := range requirement.Environments {
				if !catalogEnvironments[environment] {
					return fmt.Errorf("%s %s has unknown environment %q", requirementPrefix, requirement.ID, environment)
				}
			}
			for _, target := range requirement.Targets {
				if !catalogTargets[target] {
					return fmt.Errorf("%s %s has unknown target %q", requirementPrefix, requirement.ID, target)
				}
			}
			for _, evidence := range requirement.Evidence {
				if !catalogEvidence[evidence] {
					return fmt.Errorf("%s %s has unknown evidence %q", requirementPrefix, requirement.ID, evidence)
				}
			}
			if requirement.Gate == "scheduled" || requirement.Gate == "operator-release" {
				if requirement.AcceptanceLayer != "live" || requirement.FreshnessHours <= 0 {
					return fmt.Errorf("%s %s live gate requires acceptance_layer live and positive freshness_hours", requirementPrefix, requirement.ID)
				}
				expectedFreshness := 168
				if requirement.Gate == "scheduled" && feature.Risk == "critical" {
					expectedFreshness = 36
				}
				if requirement.FreshnessHours != expectedFreshness {
					return fmt.Errorf("%s %s freshness_hours must be %d", requirementPrefix, requirement.ID, expectedFreshness)
				}
			} else if requirement.FreshnessHours != 0 {
				return fmt.Errorf("%s %s deterministic gate must not set freshness_hours", requirementPrefix, requirement.ID)
			}
		}
	}
	return nil
}

func validateRequirementProofMappings(catalog testCatalog) error {
	requirements := catalogRequirementIndex(catalog)
	proofs := map[string][]testCatalogCase{}
	for _, tc := range catalog.Cases {
		if tc.Status != "active" {
			continue
		}
		for _, requirementID := range tc.Verifies {
			proofs[requirementID] = append(proofs[requirementID], tc)
		}
	}
	for requirementID, requirement := range requirements {
		if requirement.Status != "active" || !requirementRequired(requirement) {
			continue
		}
		var valid, gateReachable bool
		for _, tc := range proofs[requirementID] {
			if tc.Layer == "unit" || tc.Layer == "service" {
				continue
			}
			if requirement.AcceptanceLayer == "ui" && tc.Layer != "ui" &&
				!(tc.Layer == "integration" && catalogContainsString(tc.Evidence, "screenshot")) {
				continue
			}
			if requirement.AcceptanceLayer == "live" && !catalogContainsString(tc.Environments, "staging") {
				continue
			}
			if !catalogSlicesOverlap(requirement.Environments, tc.Environments) {
				continue
			}
			if len(requirement.Targets) > 0 && !catalogSlicesOverlap(requirement.Targets, tc.Targets) {
				continue
			}
			valid = true
			if requirement.Gate != "scheduled" || scheduledGateProofCase(tc) {
				gateReachable = true
			}
		}
		if !valid {
			return fmt.Errorf("required requirement %s has no qualifying integration/UI/E2E/live proof case", requirementID)
		}
		if requirement.Gate == "scheduled" && !gateReachable {
			return fmt.Errorf("scheduled requirement %s has no scheduled-executable proof case; capacity and qualification load profiles belong to operator-release", requirementID)
		}
	}
	return nil
}

func scheduledGateProofCase(tc testCatalogCase) bool {
	if !catalogContainsString(tc.Environments, "staging") {
		return false
	}
	switch tc.Runner {
	case "test-feature":
		return tc.Profile == "canary"
	case "test-ui":
		return tc.Layer == "ui"
	case "test-e2e":
		return tc.Owner == "rtk_cloud_client"
	case "test-live":
		return tc.ID == "LIVE-STG-ONBOARD-001" || tc.ID == "LIVE-CA-SCRAPE-001"
	default:
		return false
	}
}

func catalogSlicesOverlap(left, right []string) bool {
	for _, item := range left {
		if catalogContainsString(right, item) {
			return true
		}
	}
	return false
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
	fmt.Fprintln(&b, "> Features and requirements are generated from `tests/spec-sources.yaml` and the registered specs. Test cases are generated from `tests/catalog.yaml`; do not edit this file directly.")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Feature requirements")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Spec source | Feature ID | Feature | Risk | Requirement ID | Requirement | Revision | Layer | Gate | Freshness | Owner | Status |")
	fmt.Fprintln(&b, "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	features := append([]testCatalogFeature(nil), catalog.Features...)
	sort.Slice(features, func(i, j int) bool { return features[i].ID < features[j].ID })
	for _, feature := range features {
		requirements := append([]testCatalogRequirement(nil), feature.Requirements...)
		sort.Slice(requirements, func(i, j int) bool { return requirements[i].ID < requirements[j].ID })
		for _, requirement := range requirements {
			freshness := "—"
			if requirement.FreshnessHours > 0 {
				freshness = fmt.Sprintf("%dh", requirement.FreshnessHours)
			}
			fmt.Fprintf(&b, "| `%s#%s` | `%s` | %s | `%s` | `%s` | %s | `%s` | `%s` | `%s` | %s | `%s` | `%s` |\n",
				requirement.SpecSource.Path, requirement.SpecSource.Section,
				feature.ID, escapeMarkdownCell(feature.Title), feature.Risk, requirement.ID,
				escapeMarkdownCell(requirement.Title), shortRevision(requirement.Revision),
				requirement.AcceptanceLayer, requirement.Gate, freshness, feature.Owner, requirement.Status)
		}
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Test cases")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Test ID | Purpose | Method | Layer | Feature | Profile | Verifies | Supports | Covers | Change Paths | Owner | Targets | Environments | Runner | Evidence | Status |")
	fmt.Fprintln(&b, "| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, tc := range cases {
		fmt.Fprintf(&b, "| `%s` | %s | %s | `%s` | %s | %s | %s | %s | %s | %s | `%s` | %s | %s | `%s` | %s | `%s` |\n",
			tc.ID, escapeMarkdownCell(tc.Title), escapeMarkdownCell(tc.Method), tc.Layer,
			catalogOptionalCode(tc.Feature), catalogOptionalCode(tc.Profile), joinCatalogValues(tc.Verifies),
			joinCatalogValues(tc.Supports), joinCatalogValues(tc.Covers),
			joinCatalogValues(tc.ChangePaths), tc.Owner,
			joinCatalogValues(tc.Targets), joinCatalogValues(tc.Environments), tc.Runner,
			joinCatalogValues(tc.Evidence), tc.Status)
	}
	return []byte(b.String())
}

func catalogOptionalCode(value string) string {
	if value == "" {
		return "—"
	}
	return "`" + value + "`"
}

func catalogCaseByID(cases []testCatalogCase, id string) (testCatalogCase, bool) {
	for _, tc := range cases {
		if tc.ID == id {
			return tc, true
		}
	}
	return testCatalogCase{}, false
}

func validateCatalogChangePath(workspace, pattern string) error {
	if pattern == "" || filepath.IsAbs(pattern) || strings.Contains(pattern, `\`) || strings.Contains(pattern, "..") {
		return errors.New("must be a non-empty workspace-relative glob without backslashes or parent traversal")
	}
	files, err := gitOutput(workspace, "ls-files", "--recurse-submodules")
	if err != nil {
		return fmt.Errorf("list tracked files: %w", err)
	}
	re, err := catalogGlobRegexp(pattern)
	if err != nil {
		return err
	}
	for _, tracked := range strings.Fields(files) {
		if re.MatchString(filepath.ToSlash(tracked)) {
			return nil
		}
	}
	return errors.New("does not match a tracked file")
}

func catalogGlobRegexp(pattern string) (*regexp.Regexp, error) {
	var b strings.Builder
	b.WriteString("^")
	for i := 0; i < len(pattern); {
		switch pattern[i] {
		case '*':
			if i+1 < len(pattern) && pattern[i+1] == '*' {
				b.WriteString(".*")
				i += 2
			} else {
				b.WriteString("[^/]*")
				i++
			}
		case '?':
			b.WriteString("[^/]")
			i++
		default:
			b.WriteString(regexp.QuoteMeta(string(pattern[i])))
			i++
		}
	}
	b.WriteString("$")
	return regexp.Compile(b.String())
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
