package main

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPrePRCommandIsRegistered(t *testing.T) {
	if _, ok := commands["pre-pr"]; !ok {
		t.Fatal("pre-pr command is not registered")
	}
}

func TestParsePrePRSelection(t *testing.T) {
	raw := strings.Join([]string{
		"policy=true",
		`go_modules=["account-manager","cloud-admin-backend"]`,
		`node_modules=["cloud-admin-web"]`,
		"account_manager_postgres=true",
		"billing_postgres=false",
		"video_cloud_postgres_emqx=false",
	}, "\n")
	got, err := parsePrePRSelection(raw)
	if err != nil {
		t.Fatal(err)
	}
	want := prePRSelection{
		Policy: true, GoModules: []string{"account-manager", "cloud-admin-backend"}, NodeModules: []string{"cloud-admin-web"}, AccountManagerPostgres: true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("selection = %#v, want %#v", got, want)
	}
}

func TestParsePrePRSelectionRejectsMalformedOutput(t *testing.T) {
	raw := "policy=true\ngo_modules=not-json\nnode_modules=[]\naccount_manager_postgres=false\nbilling_postgres=false\nvideo_cloud_postgres_emqx=false\n"
	if _, err := parsePrePRSelection(raw); err == nil || !strings.Contains(err.Error(), "go_modules") {
		t.Fatalf("error = %v, want go_modules parse failure", err)
	}
}

func TestParsePrePRSelectionRejectsMissingAndInvalidFields(t *testing.T) {
	valid := map[string]string{
		"policy":                    "true",
		"go_modules":                "[]",
		"node_modules":              "[]",
		"account_manager_postgres":  "false",
		"billing_postgres":          "false",
		"video_cloud_postgres_emqx": "false",
	}
	for _, key := range []string{"policy", "account_manager_postgres", "billing_postgres", "video_cloud_postgres_emqx"} {
		t.Run("missing "+key, func(t *testing.T) {
			fields := clonePrePRFields(valid)
			delete(fields, key)
			if _, err := parsePrePRSelection(renderPrePRFields(fields)); err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("error = %v, want missing %s", err, key)
			}
		})
	}
	t.Run("invalid boolean", func(t *testing.T) {
		fields := clonePrePRFields(valid)
		fields["policy"] = "sometimes"
		if _, err := parsePrePRSelection(renderPrePRFields(fields)); err == nil || !strings.Contains(err.Error(), "policy") {
			t.Fatalf("error = %v, want invalid policy boolean", err)
		}
	})
	t.Run("invalid node modules", func(t *testing.T) {
		fields := clonePrePRFields(valid)
		fields["node_modules"] = "not-json"
		if _, err := parsePrePRSelection(renderPrePRFields(fields)); err == nil || !strings.Contains(err.Error(), "node_modules") {
			t.Fatalf("error = %v, want node_modules parse failure", err)
		}
	})
}

func TestPrePRIntegrationChecks(t *testing.T) {
	selection := prePRSelection{AccountManagerPostgres: true, VideoCloudPostgresEMQX: true}
	want := []string{"Account Manager PostgreSQL", "Video Cloud PostgreSQL/EMQX"}
	if got := prePRIntegrationChecks(selection); !reflect.DeepEqual(got, want) {
		t.Fatalf("checks = %v, want %v", got, want)
	}
	if got := prePRIntegrationChecks(prePRSelection{}); !reflect.DeepEqual(got, []string{"none"}) {
		t.Fatalf("empty checks = %v", got)
	}
}

func TestPrintPrePRPlanMakesLocalBoundaryExplicit(t *testing.T) {
	selection := prePRSelection{Policy: true, NodeModules: []string{"cloud-admin-web"}, BillingPostgres: true}
	var output bytes.Buffer
	printPrePRPlan(&output, "origin/main", "HEAD", selection, true, true)
	for _, want := range []string{
		"Workspace policy matrix: true",
		"Cloud Admin desktop/mobile E2E: true",
		"CI-only integration checks: Billing PostgreSQL/virtual payment",
		"Shared staging: never",
	} {
		if !strings.Contains(output.String(), want) {
			t.Fatalf("plan missing %q:\n%s", want, output.String())
		}
	}
}

func TestRunPrePRDryRunUsesCommittedSelection(t *testing.T) {
	workspace := newPrePRTestWorkspace(t)
	t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
	if err := runPrePR([]string{"--base", "HEAD", "--head", "HEAD", "--dry-run"}); err != nil {
		t.Fatal(err)
	}
}

func TestRunPrePRRejectsDirtyWorkspace(t *testing.T) {
	workspace := newPrePRTestWorkspace(t)
	t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
	if err := os.WriteFile(filepath.Join(workspace, "dirty.txt"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runPrePR([]string{"--base", "HEAD", "--head", "HEAD", "--dry-run"})
	if err == nil || !strings.Contains(err.Error(), "committed changes only") {
		t.Fatalf("error = %v, want dirty-workspace rejection", err)
	}
}

func TestRunPrePRRejectsInvalidArgumentsAndRefs(t *testing.T) {
	if err := runPrePR([]string{"--unknown"}); err == nil {
		t.Fatal("unknown flag should fail")
	}
	if err := runPrePR([]string{"--base", ""}); err == nil || !strings.Contains(err.Error(), "non-empty") {
		t.Fatalf("empty base error = %v", err)
	}
	workspace := newPrePRTestWorkspace(t)
	t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
	if err := runPrePR([]string{"--base", "missing-ref", "--dry-run"}); err == nil || !strings.Contains(err.Error(), "resolve --base") {
		t.Fatalf("invalid ref error = %v", err)
	}
}

func TestRunPrePRReportsSelectorFailure(t *testing.T) {
	workspace := newPrePRTestWorkspaceWithScript(t, "#!/usr/bin/env bash\necho selector-failed >&2\nexit 42\n")
	t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
	err := runPrePR([]string{"--base", "HEAD", "--head", "HEAD", "--dry-run"})
	if err == nil || !strings.Contains(err.Error(), "selector-failed") {
		t.Fatalf("error = %v, want selector output", err)
	}
}

func TestRunPrePRExecutesSelectedLocalChecks(t *testing.T) {
	script := `#!/usr/bin/env bash
cat <<'EOF'
policy=true
go_modules=["cloud-admin-backend"]
node_modules=["cloud-admin-web"]
account_manager_postgres=false
billing_postgres=false
video_cloud_postgres_emqx=false
EOF
`
	workspace := newPrePRTestWorkspaceWithScript(t, script)
	t.Setenv("RTK_CLOUD_WORKSPACE", workspace)
	originalCmd := prePRRunCmd
	originalMatrix := prePRRunMatrix
	originalCoverage := prePRRunCoverage
	originalInventory := prePRRunInventory
	originalUI := prePRRunUI
	t.Cleanup(func() {
		prePRRunCmd = originalCmd
		prePRRunMatrix = originalMatrix
		prePRRunCoverage = originalCoverage
		prePRRunInventory = originalInventory
		prePRRunUI = originalUI
	})
	calls := []string{}
	prePRRunCmd = func(_ string, name string, args ...string) error {
		calls = append(calls, name+" "+strings.Join(args, " "))
		return nil
	}
	prePRRunMatrix = func(args []string) error {
		calls = append(calls, "matrix "+strings.Join(args, " "))
		return nil
	}
	prePRRunCoverage = func(args []string) error {
		calls = append(calls, "coverage "+strings.Join(args, " "))
		return nil
	}
	prePRRunInventory = func(args []string) error {
		calls = append(calls, "inventory "+strings.Join(args, " "))
		return nil
	}
	prePRRunUI = func(args []string) error {
		calls = append(calls, "ui "+strings.Join(args, " "))
		return nil
	}
	if err := runPrePR([]string{"--base", "HEAD", "--head", "HEAD", "--run-id", "test-pre-pr"}); err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(calls, "\n")
	for _, want := range []string{
		"git diff --check HEAD...HEAD",
		"matrix ",
		"coverage --profile unit --module cloud-admin-backend --base-ref HEAD --head-ref HEAD --run-id test-pre-pr-go",
		"coverage --profile unit --module cloud-admin-web --run-id test-pre-pr-node --install",
		"inventory check --from-run",
		"ui --desktop --mobile --full --run-id test-pre-pr-ui --install",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("calls missing %q:\n%s", want, joined)
		}
	}
}

func TestRunPrePRReportsGitStatusFailure(t *testing.T) {
	t.Setenv("RTK_CLOUD_WORKSPACE", filepath.Join(t.TempDir(), "missing"))
	err := runPrePR([]string{"--dry-run"})
	if err == nil || !strings.Contains(err.Error(), "inspect workspace status") {
		t.Fatalf("error = %v, want git status failure", err)
	}
}

func newPrePRTestWorkspace(t *testing.T) string {
	t.Helper()
	script := `#!/usr/bin/env bash
set -e
cat <<'EOF'
policy=true
go_modules=["workspace-tooling"]
node_modules=[]
account_manager_postgres=false
billing_postgres=false
video_cloud_postgres_emqx=false
EOF
`
	return newPrePRTestWorkspaceWithScript(t, script)
}

func newPrePRTestWorkspaceWithScript(t *testing.T, script string) string {
	t.Helper()
	workspace := t.TempDir()
	scriptDir := filepath.Join(workspace, "scripts", "ci")
	if err := os.MkdirAll(scriptDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(scriptDir, "select-coverage-jobs.sh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"init"},
		{"config", "user.email", "pre-pr-test@example.invalid"},
		{"config", "user.name", "Pre PR Test"},
		{"add", "."},
		{"commit", "-m", "initial"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v: %s", strings.Join(args, " "), err, output)
		}
	}
	return workspace
}

func clonePrePRFields(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func renderPrePRFields(fields map[string]string) string {
	order := []string{"policy", "go_modules", "node_modules", "account_manager_postgres", "billing_postgres", "video_cloud_postgres_emqx"}
	lines := []string{}
	for _, key := range order {
		if value, ok := fields[key]; ok {
			lines = append(lines, key+"="+value)
		}
	}
	return strings.Join(lines, "\n")
}
