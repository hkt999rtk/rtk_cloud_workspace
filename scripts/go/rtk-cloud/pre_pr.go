package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

type prePRSelection struct {
	Policy                 bool
	GoModules              []string
	NodeModules            []string
	AccountManagerPostgres bool
	BillingPostgres        bool
	VideoCloudPostgresEMQX bool
}

var (
	prePRRunCmd                     = runCmd
	prePRRunMatrix                  = runTestMatrix
	prePRRunCoverage                = runTestCoverage
	prePRRunInventory               = runTestInventory
	prePRRunUI                      = runTestUI
	prePRStartVideoCloudPRFixtures  = startVideoCloudPRFixtures
	prePRRunFixtureCommand          = runPrePRFixtureCommand
	prePRCheckReadiness             = checkPrePRReadiness
	prePRStartAccountManagerFixture = startAccountManagerPRFixture
)

func runPrePR(args []string) error {
	fs := flag.NewFlagSet("pre-pr", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	baseRef := fs.String("base", "origin/main", "base git ref used to select affected checks")
	headRef := fs.String("head", "HEAD", "head git ref used to select affected checks")
	runID := fs.String("run-id", "", "artifact run ID; defaults to a UTC local-pre-pr ID")
	install := fs.Bool("install", true, "install Node and Playwright dependencies when selected")
	runMatrix := fs.Bool("matrix", true, "run the workspace policy matrix when selected")
	runUI := fs.Bool("ui", true, "run full desktop and mobile UI E2E when Cloud Admin web is selected")
	dryRun := fs.Bool("dry-run", false, "print selected local and CI-only checks without running them")
	accountReportEvidence := fs.String("account-manager-report-evidence", "", "reuse a validated completed Account Manager canonical report execution")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*baseRef) == "" || strings.TrimSpace(*headRef) == "" {
		return errors.New("pre-pr requires non-empty --base and --head refs")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	status, err := gitOutput(workspace, "status", "--porcelain", "--untracked-files=normal")
	if err != nil {
		return fmt.Errorf("inspect workspace status: %w", err)
	}
	if strings.TrimSpace(status) != "" {
		return errors.New("pre-pr selects committed changes only; commit the workspace changes before running it")
	}
	for label, ref := range map[string]string{"base": *baseRef, "head": *headRef} {
		resolved, err := gitOutput(workspace, "rev-parse", "--verify", strings.TrimSpace(ref)+"^{commit}")
		if err != nil {
			return fmt.Errorf("resolve --%s ref %q (run git fetch origin main first if needed): %w", label, ref, err)
		}
		if label == "base" {
			*baseRef = strings.TrimSpace(resolved)
		} else {
			*headRef = strings.TrimSpace(resolved)
		}
	}
	resolvedHead, err := gitOutput(workspace, "rev-parse", strings.TrimSpace(*headRef)+"^{commit}")
	if err != nil {
		return err
	}
	checkedOut, err := gitOutput(workspace, "rev-parse", "HEAD")
	if err != nil || strings.TrimSpace(resolvedHead) != strings.TrimSpace(checkedOut) {
		return errors.New("pre-pr --head must resolve to the checked-out commit; checks execute the working tree")
	}
	selection, err := selectPrePRChecks(workspace, strings.TrimSpace(*baseRef), strings.TrimSpace(*headRef))
	if err != nil {
		return err
	}
	if *accountReportEvidence != "" && !selection.AccountManagerPostgres {
		return errors.New("Account Manager evidence was provided but its PR profile is not selected")
	}
	if *accountReportEvidence != "" {
		*accountReportEvidence, err = filepath.Abs(*accountReportEvidence)
		if err != nil {
			return err
		}
	}
	if *runID == "" {
		*runID = "local-pre-pr-" + time.Now().UTC().Format("20060102T150405Z")
	}
	printPrePRPlan(os.Stdout, strings.TrimSpace(*baseRef), strings.TrimSpace(*headRef), selection, *runMatrix, *runUI)
	if *dryRun {
		return nil
	}
	if err := checkPrePROutputs(workspace, *runID, selection, *runUI); err != nil {
		return err
	}
	readiness := selection
	if *accountReportEvidence != "" {
		readiness.AccountManagerPostgres = false // render-only reuse needs no live database
		if !slices.Contains(readiness.GoModules, "account-manager") {
			readiness.GoModules = append(append([]string(nil), readiness.GoModules...), "account-manager")
		}
		if _, err := prePRRunFixtureCommand(workspace, nil, "python3", "--version"); err != nil {
			return errors.New("Account Manager report reuse requires python3")
		}
	}
	if err := prePRCheckReadiness(workspace, readiness); err != nil {
		return err
	}

	fmt.Fprintln(os.Stdout, "\n== diff check ==")
	if err := prePRRunCmd(workspace, "git", "diff", "--check", strings.TrimSpace(*baseRef)+"..."+strings.TrimSpace(*headRef)); err != nil {
		return err
	}
	if selection.Policy && *runMatrix {
		fmt.Fprintln(os.Stdout, "\n== workspace policy matrix ==")
		var matrixArgs []string
		if slices.Contains(selection.GoModules, "workspace-tooling") {
			matrixArgs = []string{"--policy-only"}
		}
		if err := prePRRunMatrix(matrixArgs); err != nil {
			return err
		}
	}
	goModules := append([]string(nil), selection.GoModules...)
	if selection.AccountManagerPostgres {
		fmt.Fprintln(os.Stdout, "\n== local Account Manager PostgreSQL fixture and canonical report ==")
		cleanup := func() {}
		if *accountReportEvidence == "" {
			cleanup, err = prePRStartAccountManagerFixture(workspace)
			if err != nil {
				return err
			}
		}
		coverageArgs := []string{"--profile", "pr", "--module", "account-manager",
			"--base-ref", strings.TrimSpace(*baseRef), "--head-ref", strings.TrimSpace(*headRef),
			"--run-id", *runID + "-account-manager-pr"}
		if *accountReportEvidence != "" {
			coverageArgs = append(coverageArgs, "--account-manager-report-evidence", *accountReportEvidence)
		}
		err = prePRRunCoverage(coverageArgs)
		cleanup()
		if err != nil {
			return err
		}
		goModules = removePrePRModule(goModules, "account-manager")
	}
	if selection.VideoCloudPostgresEMQX {
		fmt.Fprintln(os.Stdout, "\n== local Video Cloud PostgreSQL/EMQX fixtures ==")
		cleanup, err := prePRStartVideoCloudPRFixtures(workspace)
		if err != nil {
			return err
		}
		goModules = removePrePRModule(goModules, "video-cloud")
		fmt.Fprintln(os.Stdout, "\n== Video Cloud PR coverage ==")
		err = prePRRunCoverage([]string{
			"--profile", "pr",
			"--module", "video-cloud",
			"--base-ref", strings.TrimSpace(*baseRef),
			"--head-ref", strings.TrimSpace(*headRef),
			"--run-id", *runID + "-video-cloud-pr",
		})
		cleanup()
		if err != nil {
			return err
		}
	}
	if len(goModules) > 0 {
		fmt.Fprintln(os.Stdout, "\n== affected Go coverage ==")
		if err := prePRRunCoverage([]string{
			"--profile", "unit",
			"--module", strings.Join(goModules, ","),
			"--base-ref", strings.TrimSpace(*baseRef),
			"--head-ref", strings.TrimSpace(*headRef),
			"--run-id", *runID + "-go",
		}); err != nil {
			return err
		}
	}
	if len(selection.NodeModules) > 0 {
		fmt.Fprintln(os.Stdout, "\n== affected JavaScript coverage and inventory ==")
		coverageArgs := []string{
			"--profile", "unit",
			"--module", strings.Join(selection.NodeModules, ","),
			"--run-id", *runID + "-node",
		}
		if *install {
			coverageArgs = append(coverageArgs, "--install")
		}
		if err := prePRRunCoverage(coverageArgs); err != nil {
			return err
		}
		coverageDir := filepath.Join(workspace, ".artifacts", "test-runs", *runID+"-node", "coverage")
		if err := prePRRunInventory([]string{"check", "--from-run", coverageDir}); err != nil {
			return err
		}
	}
	if *runUI && slices.Contains(selection.NodeModules, "cloud-admin-web") {
		fmt.Fprintln(os.Stdout, "\n== Cloud Admin desktop and mobile E2E ==")
		uiArgs := []string{"--desktop", "--mobile", "--full", "--run-id", *runID + "-ui"}
		if *install {
			uiArgs = append(uiArgs, "--install")
		}
		if err := prePRRunUI(uiArgs); err != nil {
			return err
		}
	}
	fmt.Fprintln(os.Stdout, "\nLocal pre-PR checks passed. CI-only integration checks listed above still run on the PR.")
	return nil
}

func selectPrePRChecks(workspace, baseRef, headRef string) (prePRSelection, error) {
	script := filepath.Join(workspace, "scripts", "ci", "select-coverage-jobs.sh")
	cmd := exec.Command("bash", script, baseRef, headRef, "pull_request")
	cmd.Dir = workspace
	cmd.Env = withoutEnvironmentKey(os.Environ(), "GITHUB_OUTPUT")
	raw, err := cmd.CombinedOutput()
	if err != nil {
		return prePRSelection{}, fmt.Errorf("select affected pre-PR checks: %w: %s", err, strings.TrimSpace(string(raw)))
	}
	return parsePrePRSelection(string(raw))
}

func withoutEnvironmentKey(env []string, key string) []string {
	prefix := key + "="
	filtered := make([]string, 0, len(env))
	for _, entry := range env {
		if !strings.HasPrefix(entry, prefix) {
			filtered = append(filtered, entry)
		}
	}
	return filtered
}

func parsePrePRSelection(raw string) (prePRSelection, error) {
	values := map[string]string{}
	for _, line := range strings.Split(raw, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && key != "" {
			values[key] = value
		}
	}
	selection := prePRSelection{}
	var err error
	if selection.Policy, err = parsePrePRBool(values, "policy"); err != nil {
		return prePRSelection{}, err
	}
	if selection.AccountManagerPostgres, err = parsePrePRBool(values, "account_manager_postgres"); err != nil {
		return prePRSelection{}, err
	}
	if selection.BillingPostgres, err = parsePrePRBool(values, "billing_postgres"); err != nil {
		return prePRSelection{}, err
	}
	if selection.VideoCloudPostgresEMQX, err = parsePrePRBool(values, "video_cloud_postgres_emqx"); err != nil {
		return prePRSelection{}, err
	}
	if err := json.Unmarshal([]byte(values["go_modules"]), &selection.GoModules); err != nil {
		return prePRSelection{}, fmt.Errorf("parse pre-PR go_modules: %w", err)
	}
	if err := json.Unmarshal([]byte(values["node_modules"]), &selection.NodeModules); err != nil {
		return prePRSelection{}, fmt.Errorf("parse pre-PR node_modules: %w", err)
	}
	return selection, nil
}

func parsePrePRBool(values map[string]string, key string) (bool, error) {
	value, ok := values[key]
	if !ok {
		return false, fmt.Errorf("pre-PR selector did not emit %s", key)
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("parse pre-PR %s: %w", key, err)
	}
	return parsed, nil
}

func printPrePRPlan(out io.Writer, baseRef, headRef string, selection prePRSelection, runMatrix, runUI bool) {
	fmt.Fprintf(out, "Local pre-PR plan (%s...%s):\n", baseRef, headRef)
	fmt.Fprintf(out, "- Workspace policy matrix: %t\n", selection.Policy && runMatrix)
	fmt.Fprintf(out, "- Go coverage: %s\n", prePRList(selection.GoModules))
	fmt.Fprintf(out, "- Local PostgreSQL canonical Account Manager report: %t\n", selection.AccountManagerPostgres)
	fmt.Fprintf(out, "- Local Video Cloud PostgreSQL/EMQX: %t\n", selection.VideoCloudPostgresEMQX)
	fmt.Fprintf(out, "- JavaScript coverage: %s\n", prePRList(selection.NodeModules))
	fmt.Fprintf(out, "- Cloud Admin desktop/mobile E2E: %t\n", runUI && slices.Contains(selection.NodeModules, "cloud-admin-web"))
	fmt.Fprintf(out, "- CI-only integration checks: %s\n", strings.Join(prePRIntegrationChecks(selection), ", "))
	fmt.Fprintln(out, "- Shared staging: never")
}

func prePRList(values []string) string {
	if len(values) == 0 {
		return "none"
	}
	return strings.Join(values, ", ")
}

func removePrePRModule(values []string, remove string) []string {
	filtered := values[:0]
	for _, value := range values {
		if value != remove {
			filtered = append(filtered, value)
		}
	}
	return filtered
}

// runPrePRFixtureCommand is the narrow command boundary used by the local
// fixture. Keeping it separate makes all lifecycle behaviour testable without
// requiring Docker in the workspace-tooling unit suite.
func runPrePRFixtureCommand(dir string, env []string, name string, args ...string) ([]byte, error) {
	command := exec.Command(name, args...)
	command.Dir = dir
	if env != nil {
		command.Env = env
	}
	return command.CombinedOutput()
}

// startVideoCloudPRFixtures supplies the same isolated dependencies as the
// repository PR profile. The short-lived credentials are local test values and
// are placed in this process only during the Video Cloud PR profile.
func startVideoCloudPRFixtures(workspace string) (func(), error) {
	if _, err := prePRRunFixtureCommand("", nil, "docker", "info"); err != nil {
		return nil, errors.New("Docker is required for local Video Cloud PostgreSQL/EMQX coverage")
	}
	runKey := fmt.Sprintf("pre-pr-%d", time.Now().UTC().UnixNano())
	postgresName, emqxName := "rtk-video-"+runKey+"-pg", "rtk-video-"+runKey+"-emqx"
	cleanup := func() {
		_, _ = prePRRunFixtureCommand("", nil, "docker", "rm", "--force", emqxName)
		_, _ = prePRRunFixtureCommand("", nil, "docker", "rm", "--force", postgresName)
	}
	fail := func(err error) (func(), error) { cleanup(); return nil, err }
	if _, err := prePRRunFixtureCommand("", nil, "docker", "run", "--detach", "--name", postgresName,
		"--rm", "--tmpfs", "/var/lib/postgresql/data:rw,size=2g",
		"--label", "rtk.local-ci=video-cloud-pre-pr", "--env", "POSTGRES_DB=video_cloud_test",
		"--env", "POSTGRES_USER=video_cloud", "--env", "POSTGRES_PASSWORD=local_integration_only",
		"--publish", "127.0.0.1::5432", "postgres:16"); err != nil {
		return fail(fmt.Errorf("start local PostgreSQL fixture: %w", err))
	}
	if _, err := prePRRunFixtureCommand("", nil, "docker", "run", "--detach", "--name", emqxName,
		"--rm",
		"--label", "rtk.local-ci=video-cloud-pre-pr", "--env", "EMQX_NAME=video_cloud_emqx",
		"--env", "EMQX_HOST=127.0.0.1", "--env", "EMQX_LISTENERS__TCP__DEFAULT__ENABLE_AUTHN=false",
		"--env", "EMQX_MQTT__MAX_INFLIGHT=10", "--publish", "127.0.0.1::1883", "emqx/emqx:latest"); err != nil {
		return fail(fmt.Errorf("start local EMQX fixture: %w", err))
	}
	port := func(container, port string) (string, error) {
		raw, err := prePRRunFixtureCommand("", nil, "docker", "port", container, port)
		if err != nil {
			return "", err
		}
		_, selected, err := net.SplitHostPort(strings.TrimSpace(strings.Split(string(raw), "\n")[0]))
		return selected, err
	}
	postgresPort, err := port(postgresName, "5432/tcp")
	if err != nil {
		return fail(fmt.Errorf("discover local PostgreSQL port: %w", err))
	}
	emqxPort, err := port(emqxName, "1883/tcp")
	if err != nil {
		return fail(fmt.Errorf("discover local EMQX port: %w", err))
	}
	ready := func(dir string, env []string, name string, args ...string) bool {
		_, err := prePRRunFixtureCommand(dir, env, name, args...)
		return err == nil
	}
	postgresReady := false
	for attempt := 0; attempt < 30; attempt++ {
		if ready("", nil, "docker", "exec", postgresName, "pg_isready", "-h", "127.0.0.1", "-U", "video_cloud", "-d", "video_cloud_test") {
			postgresReady = true
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !postgresReady {
		return fail(errors.New("local PostgreSQL fixture did not become ready"))
	}
	mqttReady := false
	for attempt := 0; attempt < 30; attempt++ {
		if ready(filepath.Join(workspace, "repos", "rtk_video_cloud"), append(os.Environ(), "GOWORK=off", "VIDEO_CLOUD_MQTT_TEST_ADDR=127.0.0.1:"+emqxPort), "go", "test", "./internal/mqtt", "-run", "^TestBrokerPublishSubscribeIntegration$", "-count=1") {
			mqttReady = true
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !mqttReady {
		return fail(errors.New("local EMQX fixture did not pass MQTT readiness"))
	}
	oldDSN, hadDSN := os.LookupEnv("VIDEO_CLOUD_TEST_DSN")
	oldMQTT, hadMQTT := os.LookupEnv("VIDEO_CLOUD_MQTT_TEST_ADDR")
	os.Setenv("VIDEO_CLOUD_TEST_DSN", "postgres://video_cloud:local_integration_only@127.0.0.1:"+postgresPort+"/video_cloud_test?sslmode=disable")
	os.Setenv("VIDEO_CLOUD_MQTT_TEST_ADDR", "127.0.0.1:"+emqxPort)
	return func() {
		if hadDSN {
			_ = os.Setenv("VIDEO_CLOUD_TEST_DSN", oldDSN)
		} else {
			_ = os.Unsetenv("VIDEO_CLOUD_TEST_DSN")
		}
		if hadMQTT {
			_ = os.Setenv("VIDEO_CLOUD_MQTT_TEST_ADDR", oldMQTT)
		} else {
			_ = os.Unsetenv("VIDEO_CLOUD_MQTT_TEST_ADDR")
		}
		cleanup()
	}, nil
}

func prePRIntegrationChecks(selection prePRSelection) []string {
	checks := []string{}
	if selection.AccountManagerPostgres {
		checks = append(checks, "Account Manager cross-service factory/token chain")
	}
	if selection.BillingPostgres {
		checks = append(checks, "Billing PostgreSQL/virtual payment")
	}
	if len(checks) == 0 {
		return []string{"none"}
	}
	return checks
}
