package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
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
	prePRRunCmd       = runCmd
	prePRRunMatrix    = runTestMatrix
	prePRRunCoverage  = runTestCoverage
	prePRRunInventory = runTestInventory
	prePRRunUI        = runTestUI
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
		if _, err := gitOutput(workspace, "rev-parse", "--verify", strings.TrimSpace(ref)+"^{commit}"); err != nil {
			return fmt.Errorf("resolve --%s ref %q (run git fetch origin main first if needed): %w", label, ref, err)
		}
	}
	selection, err := selectPrePRChecks(workspace, strings.TrimSpace(*baseRef), strings.TrimSpace(*headRef))
	if err != nil {
		return err
	}
	if *runID == "" {
		*runID = "local-pre-pr-" + time.Now().UTC().Format("20060102T150405Z")
	}
	printPrePRPlan(os.Stdout, strings.TrimSpace(*baseRef), strings.TrimSpace(*headRef), selection, *runMatrix, *runUI)
	if *dryRun {
		return nil
	}

	fmt.Fprintln(os.Stdout, "\n== diff check ==")
	if err := prePRRunCmd(workspace, "git", "diff", "--check", strings.TrimSpace(*baseRef)+"..."+strings.TrimSpace(*headRef)); err != nil {
		return err
	}
	if selection.Policy && *runMatrix {
		fmt.Fprintln(os.Stdout, "\n== workspace policy matrix ==")
		if err := prePRRunMatrix(nil); err != nil {
			return err
		}
	}
	if len(selection.GoModules) > 0 {
		fmt.Fprintln(os.Stdout, "\n== affected Go coverage ==")
		if err := prePRRunCoverage([]string{
			"--profile", "unit",
			"--module", strings.Join(selection.GoModules, ","),
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

func prePRIntegrationChecks(selection prePRSelection) []string {
	checks := []string{}
	if selection.AccountManagerPostgres {
		checks = append(checks, "Account Manager PostgreSQL")
	}
	if selection.BillingPostgres {
		checks = append(checks, "Billing PostgreSQL/virtual payment")
	}
	if selection.VideoCloudPostgresEMQX {
		checks = append(checks, "Video Cloud PostgreSQL/EMQX")
	}
	if len(checks) == 0 {
		return []string{"none"}
	}
	return checks
}
