package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type paymentUnitManifest struct {
	SchemaVersion int            `json:"schema_version"`
	Module        string         `json:"module"`
	Profile       string         `json:"profile"`
	Tests         []goUnitResult `json:"tests"`
}

type paymentEvidenceOperation struct {
	CanonicalKey string `json:"canonical_key"`
	Source       string `json:"source,omitempty"`
	StartedAt    string `json:"started_at,omitempty"`
	CompletedAt  string `json:"completed_at,omitempty"`
	DurationMS   int64  `json:"duration_ms"`
	Status       string `json:"status"`
}

type paymentEvidenceCase struct {
	TestID      string                     `json:"test_id"`
	Purpose     string                     `json:"purpose"`
	Method      string                     `json:"method"`
	StartedAt   string                     `json:"started_at,omitempty"`
	CompletedAt string                     `json:"completed_at,omitempty"`
	DurationMS  int64                      `json:"duration_ms"`
	Status      string                     `json:"status"`
	Assessment  string                     `json:"assessment"`
	Operations  []paymentEvidenceOperation `json:"operations"`
}

type paymentEvidenceFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type paymentEvidenceReport struct {
	SchemaVersion   int                   `json:"schema_version"`
	RunID           string                `json:"run_id"`
	Profile         string                `json:"profile"`
	Environment     string                `json:"environment"`
	WorkspaceCommit string                `json:"workspace_commit"`
	ServiceCommit   string                `json:"service_commit"`
	StartedAt       string                `json:"started_at,omitempty"`
	CompletedAt     string                `json:"completed_at,omitempty"`
	DurationMS      int64                 `json:"duration_ms"`
	Status          string                `json:"status"`
	Assessment      string                `json:"assessment"`
	CoverageGate    string                `json:"coverage_gate"`
	Cases           []paymentEvidenceCase `json:"cases"`
	Evidence        []paymentEvidenceFile `json:"evidence,omitempty"`
}

type paymentEvidenceManifest struct {
	SchemaVersion   int                   `json:"schema_version"`
	RunID           string                `json:"run_id"`
	Profile         string                `json:"profile"`
	Environment     string                `json:"environment"`
	Status          string                `json:"status"`
	GeneratedAt     string                `json:"generated_at"`
	WorkspaceCommit string                `json:"workspace_commit"`
	ServiceCommit   string                `json:"service_commit"`
	Cases           []paymentEvidenceCase `json:"cases"`
	Evidence        []paymentEvidenceFile `json:"evidence"`
}

var paymentCoverageRunner = runTestCoverage

func runTestPayment(args []string) error {
	if commandFlagValue(args, "--profile") == "staging-live" {
		return runTestPaymentLive(args)
	}
	fs := flag.NewFlagSet("test-payment", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	profile := fs.String("profile", "fake-e2e", "payment profile; currently fake-e2e")
	runID := fs.String("run-id", "", "artifact run ID; defaults to a UTC timestamp")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *profile != "fake-e2e" {
		return fmt.Errorf("unsupported payment profile %q; use fake-e2e", *profile)
	}
	if *runID == "" {
		*runID = time.Now().UTC().Format("20060102T150405Z")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}

	coverageErr := paymentCoverageRunner([]string{
		"--profile", "pr",
		"--module", "billing-service",
		"--run-id", *runID,
	})
	coverageDir := filepath.Join(workspace, ".artifacts", "test-runs", *runID, "coverage")
	moduleDir := filepath.Join(coverageDir, "modules", "billing-service")
	manifest, err := readPaymentUnitManifest(filepath.Join(moduleDir, "unit-manifest.json"))
	if err != nil {
		if coverageErr != nil {
			return fmt.Errorf("payment coverage failed (%v) and unit evidence is unavailable: %w", coverageErr, err)
		}
		return err
	}
	catalog, err := loadAndValidateTestCatalog(workspace)
	if err != nil {
		return err
	}
	cases := make([]testCatalogCase, 0)
	for _, tc := range catalog.Cases {
		if isFakePaymentCase(tc) {
			cases = append(cases, tc)
		}
	}
	if len(cases) == 0 {
		return errors.New("test-payment has no active catalog cases")
	}
	sort.Slice(cases, func(i, j int) bool { return cases[i].ID < cases[j].ID })
	report, err := aggregatePaymentEvidence(*runID, *profile, cases, manifest.Tests)
	if err != nil {
		return err
	}
	report.Environment = "local-postgresql-fake-provider"
	report.WorkspaceCommit, _ = gitOutput(workspace, "rev-parse", "HEAD")
	report.WorkspaceCommit = strings.TrimSpace(report.WorkspaceCommit)
	report.ServiceCommit, _ = gitOutput(filepath.Join(workspace, "repos", "rtk_billing"), "rev-parse", "HEAD")
	report.ServiceCommit = strings.TrimSpace(report.ServiceCommit)
	report.CoverageGate = "PASS"
	if coverageErr != nil {
		report.CoverageGate = "FAIL"
		report.Status = "FAIL"
		report.Assessment = "Payment case assertions completed, but the required Billing PR coverage gate failed."
	}

	outDir := filepath.Join(workspace, ".artifacts", "test-runs", *runID, "payments", *profile)
	if err := os.MkdirAll(filepath.Join(outDir, "billing-service"), 0o755); err != nil {
		return err
	}
	evidenceSources := []struct {
		source string
		name   string
	}{
		{source: filepath.Join(moduleDir, "coverage.out"), name: "coverage.out"},
		{source: filepath.Join(coverageDir, "logs", "billing-service.log"), name: "coverage.log"},
		{source: filepath.Join(moduleDir, "junit.xml"), name: "junit.xml"},
		{source: filepath.Join(moduleDir, "package-coverage.json"), name: "package-coverage.json"},
		{source: filepath.Join(moduleDir, "test-events.json"), name: "test-events.json"},
		{source: filepath.Join(moduleDir, "unit-manifest.json"), name: "unit-manifest.json"},
	}
	for _, item := range evidenceSources {
		if err := copyFile(item.source, filepath.Join(outDir, "billing-service", item.name)); err != nil {
			return fmt.Errorf("copy payment evidence %s: %w", item.name, err)
		}
	}
	if err := writeJSON(filepath.Join(outDir, "results.json"), report); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "test_report.md"), renderPaymentEvidenceReport(report), 0o644); err != nil {
		return err
	}
	cleanup := map[string]any{
		"schema_version": 1, "run_id": *runID, "status": "PASS",
		"generated_at":      time.Now().UTC().Format(time.RFC3339),
		"resources_created": 0, "resources_deleted": 0, "remaining_resources": []string{},
		"assessment": "The fake-provider profile used only the caller-supplied isolated PostgreSQL database and created no cloud or provider resources.",
	}
	if err := writeJSON(filepath.Join(outDir, "cleanup-report.json"), cleanup); err != nil {
		return err
	}

	redactionIssues := findUnredactedFeatureEvidence(outDir)
	if redactionIssues == nil {
		redactionIssues = []string{}
	}
	redactionStatus := "PASS"
	if len(redactionIssues) > 0 {
		redactionStatus = "FAIL"
		report.Status = "FAIL"
		report.Assessment = "Payment evidence contains credential-like material and is unsafe to upload."
		if err := writeJSON(filepath.Join(outDir, "results.json"), report); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(outDir, "test_report.md"), renderPaymentEvidenceReport(report), 0o644); err != nil {
			return err
		}
	}
	redaction := map[string]any{
		"schema_version": 1, "run_id": *runID, "status": redactionStatus,
		"generated_at": time.Now().UTC().Format(time.RFC3339), "findings": redactionIssues,
		"assessment": map[bool]string{true: "No credential, card, cookie, token, or private-key material was detected.", false: "Credential-like material was detected; artifacts must not be uploaded."}[len(redactionIssues) == 0],
	}
	if err := writeJSON(filepath.Join(outDir, "redaction-report.json"), redaction); err != nil {
		return err
	}

	evidenceNames := []string{
		"results.json", "test_report.md", "cleanup-report.json", "redaction-report.json",
		"billing-service/coverage.out", "billing-service/coverage.log", "billing-service/junit.xml",
		"billing-service/package-coverage.json", "billing-service/test-events.json", "billing-service/unit-manifest.json",
	}
	evidence := make([]paymentEvidenceFile, 0, len(evidenceNames))
	for _, name := range evidenceNames {
		sha, err := fileSHA256(filepath.Join(outDir, filepath.FromSlash(name)))
		if err != nil {
			return err
		}
		evidence = append(evidence, paymentEvidenceFile{Path: name, SHA256: sha})
	}
	manifestOut := paymentEvidenceManifest{
		SchemaVersion: 1, RunID: report.RunID, Profile: report.Profile, Environment: report.Environment,
		Status: report.Status, GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		WorkspaceCommit: report.WorkspaceCommit, ServiceCommit: report.ServiceCommit,
		Cases: report.Cases, Evidence: evidence,
	}
	if err := writeJSON(filepath.Join(outDir, "evidence-manifest.json"), manifestOut); err != nil {
		return err
	}
	fmt.Printf("Payment report: %s\n", filepath.Join(outDir, "test_report.md"))
	if report.Status != "PASS" {
		return exitCode(1)
	}
	return nil
}

func isFakePaymentCase(tc testCatalogCase) bool {
	return tc.Status == "active" && tc.Runner == "test-payment" && tc.Layer != "live"
}

func readPaymentUnitManifest(path string) (paymentUnitManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return paymentUnitManifest{}, fmt.Errorf("read payment unit manifest: %w", err)
	}
	var manifest paymentUnitManifest
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return paymentUnitManifest{}, fmt.Errorf("parse payment unit manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 || manifest.Module != "billing-service" || len(manifest.Tests) == 0 {
		return paymentUnitManifest{}, errors.New("payment unit manifest is incomplete or belongs to another module")
	}
	return manifest, nil
}

func aggregatePaymentEvidence(runID, profile string, cases []testCatalogCase, units []goUnitResult) (paymentEvidenceReport, error) {
	byKey := make(map[string]goUnitResult, len(units))
	for _, unit := range units {
		byKey[unit.CanonicalKey] = unit
	}
	report := paymentEvidenceReport{SchemaVersion: 1, RunID: runID, Profile: profile, Status: "PASS"}
	var earliest, latest time.Time
	for _, tc := range cases {
		result := paymentEvidenceCase{TestID: tc.ID, Purpose: tc.Title, Method: tc.Method, Status: "PASS"}
		selectors := strings.Split(tc.Selector, ",")
		missing := make([]string, 0)
		for _, selector := range selectors {
			canonical, err := paymentSelectorCanonicalKey(selector)
			if err != nil {
				return paymentEvidenceReport{}, fmt.Errorf("%s: %w", tc.ID, err)
			}
			unit, ok := byKey[canonical]
			if !ok {
				missing = append(missing, canonical)
				continue
			}
			operation := paymentEvidenceOperation{
				CanonicalKey: canonical, Source: unit.Source, StartedAt: unit.StartedAt,
				CompletedAt: unit.CompletedAt, DurationMS: unit.DurationMS, Status: unit.Status,
			}
			result.Operations = append(result.Operations, operation)
			result.DurationMS += unit.DurationMS
			start, startErr := time.Parse(time.RFC3339Nano, unit.StartedAt)
			end, endErr := time.Parse(time.RFC3339Nano, unit.CompletedAt)
			if startErr == nil && (result.StartedAt == "" || start.Before(mustParseEvidenceTime(result.StartedAt))) {
				result.StartedAt = start.UTC().Format(time.RFC3339Nano)
			}
			if endErr == nil && (result.CompletedAt == "" || end.After(mustParseEvidenceTime(result.CompletedAt))) {
				result.CompletedAt = end.UTC().Format(time.RFC3339Nano)
			}
			switch unit.Status {
			case "PASS":
			case "FAIL":
				result.Status = "FAIL"
			default:
				if result.Status != "FAIL" {
					result.Status = "INCOMPLETE"
				}
			}
		}
		if len(missing) > 0 {
			result.Status = "INCOMPLETE"
			result.Assessment = "Missing executed operations: " + strings.Join(missing, ", ")
		} else if result.Status == "PASS" {
			result.Assessment = "Every catalog-mapped operation completed with PASS."
		} else if result.Status == "FAIL" {
			result.Assessment = "One or more catalog-mapped operations failed."
		} else {
			result.Assessment = "One or more catalog-mapped operations were skipped or incomplete."
		}
		if result.StartedAt != "" {
			start := mustParseEvidenceTime(result.StartedAt)
			if earliest.IsZero() || start.Before(earliest) {
				earliest = start
			}
		}
		if result.CompletedAt != "" {
			end := mustParseEvidenceTime(result.CompletedAt)
			if latest.IsZero() || end.After(latest) {
				latest = end
			}
		}
		if result.Status == "FAIL" || (result.Status == "INCOMPLETE" && report.Status != "FAIL") {
			report.Status = result.Status
		}
		report.Cases = append(report.Cases, result)
	}
	if !earliest.IsZero() {
		report.StartedAt = earliest.UTC().Format(time.RFC3339Nano)
	}
	if !latest.IsZero() {
		report.CompletedAt = latest.UTC().Format(time.RFC3339Nano)
	}
	if !earliest.IsZero() && !latest.IsZero() {
		report.DurationMS = latest.Sub(earliest).Milliseconds()
	}
	switch report.Status {
	case "PASS":
		report.Assessment = "All fake-provider payment E2E cases passed with complete canonical operation evidence."
	case "FAIL":
		report.Assessment = "One or more fake-provider payment E2E assertions failed."
	default:
		report.Assessment = "Payment E2E evidence is incomplete."
	}
	return report, nil
}

func paymentSelectorCanonicalKey(selector string) (string, error) {
	selector = strings.TrimSpace(selector)
	pkg, testName, ok := strings.Cut(selector, "#")
	pkg = strings.TrimPrefix(strings.TrimSpace(pkg), "./")
	testName = strings.TrimSpace(testName)
	if !ok || pkg == "" || testName == "" || !strings.HasPrefix(testName, "Test") {
		return "", fmt.Errorf("selector %q must use ./package#TestName", selector)
	}
	return "go://billing-service/" + filepath.ToSlash(pkg) + "#" + testName, nil
}

func mustParseEvidenceTime(value string) time.Time {
	parsed, _ := time.Parse(time.RFC3339Nano, value)
	return parsed
}

func renderPaymentEvidenceReport(report paymentEvidenceReport) []byte {
	var out strings.Builder
	out.WriteString("# Payment Test Report\n\n")
	fmt.Fprintf(&out, "- Run ID: `%s`\n", report.RunID)
	fmt.Fprintf(&out, "- Profile: `%s`\n", report.Profile)
	fmt.Fprintf(&out, "- Environment: `%s`\n", report.Environment)
	fmt.Fprintf(&out, "- Started: `%s`\n", report.StartedAt)
	fmt.Fprintf(&out, "- Completed: `%s`\n", report.CompletedAt)
	fmt.Fprintf(&out, "- Duration: `%d ms`\n", report.DurationMS)
	fmt.Fprintf(&out, "- Workspace commit: `%s`\n", report.WorkspaceCommit)
	fmt.Fprintf(&out, "- Billing service commit: `%s`\n", report.ServiceCommit)
	fmt.Fprintf(&out, "- Coverage gate: **%s**\n", report.CoverageGate)
	fmt.Fprintf(&out, "- Overall result: **%s**\n\n", report.Status)
	out.WriteString("| Test ID | Start time (UTC) | End time (UTC) | Duration ms | Purpose | Method | Result | Assessment |\n")
	out.WriteString("| --- | --- | --- | ---: | --- | --- | --- | --- |\n")
	for _, tc := range report.Cases {
		fmt.Fprintf(&out, "| `%s` | `%s` | `%s` | %d | %s | %s | **%s** | %s |\n",
			tc.TestID, tc.StartedAt, tc.CompletedAt, tc.DurationMS,
			escapeMarkdownCell(tc.Purpose), escapeMarkdownCell(tc.Method), tc.Status, escapeMarkdownCell(tc.Assessment))
	}
	out.WriteString("\n## Operation Evidence\n\n")
	for _, tc := range report.Cases {
		fmt.Fprintf(&out, "### `%s`\n\n", tc.TestID)
		for _, operation := range tc.Operations {
			fmt.Fprintf(&out, "- `%s`: **%s**, %d ms (`%s`)\n", operation.CanonicalKey, operation.Status, operation.DurationMS, operation.Source)
		}
	}
	out.WriteString("\n## Assessment\n\n")
	out.WriteString(report.Assessment)
	out.WriteByte('\n')
	return []byte(out.String())
}
