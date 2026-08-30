package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestStagingCommandAndArgumentBoundaries(t *testing.T) {
	for _, name := range []string{
		"CLOUD_STAGING_E2E_REMOVE_K8S_SCRIPT", "CLOUD_STAGING_E2E_REMOVE_SCRIPT",
		"CLOUD_STAGING_E2E_PROVISION_SCRIPT", "CLOUD_STAGING_E2E_PROVISION_K8S_SCRIPT",
	} {
		t.Setenv(name, "")
	}
	ctx := stagingRuntimeContext{
		workspace: "/workspace", envRoot: "/env/lke", provider: "lke", stackName: "video-cloud-staging",
	}
	command, args := stagingResetCommand(ctx, true)
	if command == "" || !contains(args, "--purge-storage") || !contains(args, "--yes") {
		t.Fatalf("reset command = %q %#v", command, args)
	}
	command, args = stagingProvisionCommand(ctx)
	if !strings.Contains(displayCommand(command), "provision") || !contains(args, "--deploy") || !contains(args, "--confirm") {
		t.Fatalf("provision command = %q %#v", command, args)
	}
	if err := runStagingPhaseCommand(nil, nil); err == nil {
		t.Fatal("empty staging command unexpectedly passed")
	}

	e2eArgs := stagingE2ETestArgs(stagingE2EArgs{
		workspace: "/workspace", envRoot: "/env/lke", stackName: "stack", confirmOverride: "confirmed",
		run: true, plan: true, brandname: "RTK", userCount: 2, userEmailPrefix: "run-123", userEmailDomain: "users.invalid", deviceCount: 4,
		deviceMix: "camera=50,light=50", devicePrefix: "test", userConcurrency: 2,
		deviceConcurrency: 4, bindConcurrency: 2, outDir: "/out", steps: "data,mqtt",
		skipMQTTProbe: true, skipRemove: true, purgeStorage: true, skipProvision: true,
		quiet: true, resume: true,
	})
	for _, expected := range []string{
		"--run", "confirmed", "--plan", "--out-dir", "/out", "--steps", "data,mqtt",
		"--user-email-prefix", "run-123", "--user-email-domain", "users.invalid",
		"--skip-mqtt-probe", "--skip-remove", "--purge-storage", "--skip-provision", "--quiet",
	} {
		if !contains(e2eArgs, expected) {
			t.Fatalf("E2E args missing %q: %#v", expected, e2eArgs)
		}
	}
}

func TestStagingArtifactAndLogBoundaries(t *testing.T) {
	root := t.TempDir()
	envPath := filepath.Join(root, "images.env")
	if err := writeLKEImageEnvFile(envPath, map[string]string{"B": "two words", "A": "one"}); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(envPath)
	if err != nil || !strings.HasPrefix(string(body), "export A=") || !strings.Contains(string(body), "export B='two words'") {
		t.Fatalf("image env = %q, error = %v", body, err)
	}
	copyPath := filepath.Join(root, "nested", "copy.env")
	if err := copyFileWithMode(envPath, copyPath, 0o600); err != nil {
		t.Fatal(err)
	}
	if copied, err := os.ReadFile(copyPath); err != nil || !reflect.DeepEqual(copied, body) {
		t.Fatalf("copied = %q, error = %v", copied, err)
	}

	logPath := filepath.Join(root, "run.log")
	if err := os.WriteFile(logPath, []byte("\nfirst\naccess_token=secret\nuser creation progress: done=4/10 created=4\nlast\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := latestLogLine(logPath); got != "last" {
		t.Fatalf("latest log line = %q", got)
	}
	if got := latestProgressLogLine(logPath, time.Second); !strings.Contains(got, "done=4/10") {
		t.Fatalf("latest progress line = %q", got)
	}
	lines := latestLogLines(logPath, 2)
	if len(lines) != 2 || lines[1] != "last" {
		t.Fatalf("latest log lines = %#v", lines)
	}
	if latestLogLines(logPath, 0) != nil || latestLogLine(filepath.Join(root, "missing")) != "" {
		t.Fatal("missing/zero log boundary mismatch")
	}
	t.Setenv("CLOUD_STAGING_E2E_FAILURE_TAIL_LINES", "12")
	if e2eFailureTailLines() != 12 {
		t.Fatal("failure tail override not honored")
	}
	t.Setenv("CLOUD_STAGING_E2E_FAILURE_TAIL_LINES", "bad")
	if e2eFailureTailLines() != 40 {
		t.Fatal("invalid failure tail did not use default")
	}
}

func TestWorkspaceFileSelectionAndConversionBoundaries(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"001.json", "002.json", "003.txt"} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if got := latestMatchingFile(root, "*.json"); !strings.HasSuffix(got, "002.json") {
		t.Fatalf("latest file = %q", got)
	}
	if got := latestMatchingFileWhere(root, "*.json", func(path string) bool {
		return strings.HasSuffix(path, "001.json")
	}); !strings.HasSuffix(got, "001.json") {
		t.Fatalf("latest matching file = %q", got)
	}
	if latestMatchingFileWhere(root, "*.missing", func(string) bool { return true }) != "" {
		t.Fatal("missing match unexpectedly returned a path")
	}

	slug := "brand"
	first := uniqueUserCredentialsFile(root, slug)
	if err := os.WriteFile(first, []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	second := uniqueUserCredentialsFile(root, slug)
	if second == first || !strings.HasSuffix(second, "-02.json") {
		t.Fatalf("unique credentials paths = %q, %q", first, second)
	}
	if asFloat(2) != 2 || asFloat(2.5) != 2.5 || asFloat(json.Number("3.5")) != 3.5 || asFloat("4.5") != 4.5 {
		t.Fatal("numeric conversion mismatch")
	}
	if len(loadDeviceTypeNames()) == 0 {
		t.Fatal("device type inventory is empty")
	}
	for _, test := range []struct {
		body string
		want string
	}{
		{"", ""},
		{`{"error":"denied","message":"not allowed"}`, ": denied (not allowed)"},
		{`{"code":"bad"}`, ": bad"},
		{`{"message":"bad request"}`, ": bad request"},
		{"plain failure", ": plain failure"},
	} {
		if got := errorBodySuffix([]byte(test.body)); got != test.want {
			t.Fatalf("errorBodySuffix(%q) = %q, want %q", test.body, got, test.want)
		}
	}
}

func TestWorkspaceGovernanceFileHelpers(t *testing.T) {
	root := t.TempDir()
	sourceDir := filepath.Join(root, "source")
	if err := os.MkdirAll(filepath.Join(sourceDir, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "nested", "one.txt"), []byte("safe content"), 0o600); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "destination")
	if err := copyPath(sourceDir, destination); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(filepath.Join(destination, "nested", "one.txt")); err != nil || string(body) != "safe content" {
		t.Fatalf("copied directory body = %q, error = %v", body, err)
	}
	singleCopy := filepath.Join(root, "single", "copy.txt")
	if err := copyPath(filepath.Join(sourceDir, "nested", "one.txt"), singleCopy); err != nil {
		t.Fatal(err)
	}

	tsvPath := filepath.Join(root, "reports", "inventory.tsv")
	if err := writeTSV(tsvPath, [][]string{{"id", "title"}, {"one", "tab\tremoved"}}); err != nil {
		t.Fatal(err)
	}
	if body, err := os.ReadFile(tsvPath); err != nil || strings.Contains(string(body), "tab\tremoved") {
		t.Fatalf("TSV = %q, error = %v", body, err)
	}

	check := newCheck()
	check.requireFile(root, "source/nested/one.txt")
	check.requireDir(root, "source/nested")
	checkFileNoMatch(check, filepath.Join(sourceDir, "nested", "one.txt"), "credential", "secret")
	if check.failures != 0 {
		t.Fatalf("safe governance checks failed: %d", check.failures)
	}
	checkFileNoMatch(check, filepath.Join(sourceDir, "nested", "one.txt"), "content", "safe")
	checkFileNoMatch(check, filepath.Join(root, "missing"), "missing", "value")
	checkFileNoMatch(check, filepath.Join(sourceDir, "nested", "one.txt"), "invalid", "[")
	if check.failures != 3 {
		t.Fatalf("governance failure count = %d", check.failures)
	}
	if !anyFileContains(root, []string{"source/nested/one.txt"}, "safe") ||
		anyFileContains(root, []string{"source/nested/one.txt"}, "absent") ||
		readText(filepath.Join(root, "missing")) != "" {
		t.Fatal("file content helper mismatch")
	}

	gitRoot := filepath.Join(root, "git")
	if err := os.MkdirAll(gitRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := runCmd(gitRoot, "git", "init", "-q"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(gitRoot, ".gitmodules"), []byte(`
[submodule "z"]
	path = repos/z
	url = https://example.test/z.git
[submodule "a"]
	path = repos/a
	url = https://example.test/a.git
`), 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := submodulePaths(gitRoot)
	if err != nil || !reflect.DeepEqual(paths, []string{"repos/a", "repos/z"}) {
		t.Fatalf("submodule paths = %#v, error = %v", paths, err)
	}
}
