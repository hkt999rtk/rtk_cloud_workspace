package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAccountManagerCoverageConsumesOneCanonicalExecution(t *testing.T) {
	for _, outcome := range []string{"pass", "report-drift", "test-failure"} {
		t.Run(outcome, func(t *testing.T) {
			workspace := t.TempDir()
			dir := filepath.Join(workspace, "account")
			files := map[string]string{
				"go.mod":                               "module example.com/account\n\ngo 1.25.0\n",
				"internal/example/value.go":            "package example\nfunc Value() int { return 1 }\n",
				"internal/example/value_test.go":       "package example\nimport \"testing\"\nfunc TestValue(t *testing.T) { if Value()!=1 { t.Fatal(\"value\") } }\n",
				"docs/test_report.md":                  "canonical\n",
				"scripts/validate-report-candidate.sh": "#!/bin/sh\nexit 0\n",
				"scripts/test-report.sh": `#!/bin/sh
set -eu
printf 'execution\n' >> executions.txt
go test -p=1 -json ./... -coverpkg=./internal/... -coverprofile="$REPORT_DIR/coverage.out" -covermode=atomic > "$REPORT_DIR/test-events.json"
for artifact in execution-evidence.json gofmt.txt build.txt coverage.txt coverage.html test-cases.md correctness-gates.md; do
  printf 'fixture\n' > "$REPORT_DIR/$artifact"
done
printf 'canonical\n' > "$REPORT_FILE"
`,
			}
			if outcome == "report-drift" {
				files["docs/test_report.md"] = "old report\n"
			}
			if outcome == "test-failure" {
				files["scripts/test-report.sh"] += "exit 1\n"
			}
			for path, body := range files {
				target := filepath.Join(dir, path)
				if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, []byte(body), 0644); err != nil {
					t.Fatal(err)
				}
			}
			t.Setenv("TEST_DATABASE_URL", "fixture-only")
			module := coverageModule{Name: "account-manager", Kind: "go", Path: "account", Packages: []string{"./..."}, CoverPackages: []string{"./internal/..."}, PRRequiredEnv: []string{"TEST_DATABASE_URL"}, PRRequiredTests: []string{"TestValue"}, MinimumStatementPercent: 1, PRMinimumStatement: 1, TargetStatementPercent: 100}
			out := t.TempDir()
			if err := os.MkdirAll(filepath.Join(out, "logs"), 0755); err != nil {
				t.Fatal(err)
			}
			result := runCoverageModuleProfile(workspace, out, coverageConfig{}, module, "", "HEAD", "pr", false)
			if outcome == "pass" && result.Status != "PASS" {
				t.Fatalf("result=%+v", result)
			}
			if outcome != "pass" && result.Status != "FAIL" {
				t.Fatalf("result=%+v", result)
			}
			if outcome == "report-drift" && !strings.Contains(result.Assessment, "canonical report differs") {
				t.Fatal(result.Assessment)
			}
			raw, err := os.ReadFile(filepath.Join(dir, "executions.txt"))
			if err != nil || string(raw) != "execution\n" {
				t.Fatalf("execution count: %s %v", raw, err)
			}
			if result.ProfileSHA == "" || result.UnitManifestPath == "" || result.TestEventsPath == "" {
				t.Fatal("completed execution diagnostics missing")
			}
			if outcome != "test-failure" {
				found := false
				for _, evidence := range result.Evidence {
					if strings.HasSuffix(evidence.Path, "execution-evidence.json") {
						found = true
					}
				}
				if !found {
					t.Fatal("reusable report packet was not recorded")
				}
			}
		})
	}
}

func TestCoverageRejectsReportReuseOutsideAccountManagerPR(t *testing.T) {
	for _, args := range [][]string{
		{"--account-manager-report-evidence", "saved"},
		{"--account-manager-report-evidence", "saved", "--profile", "pr", "--module", "video-cloud"},
	} {
		if err := runTestCoverage(args); err == nil || !strings.Contains(err.Error(), "requires --profile pr --module account-manager") {
			t.Fatalf("error=%v", err)
		}
	}
}
