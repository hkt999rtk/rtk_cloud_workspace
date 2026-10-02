package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

var prePRLoadCoverageConfig = loadCoverageConfig

// Collect independent, inexpensive failures before starting any test suite.
// Command output is deliberately not echoed: dependency configuration can contain
// authenticated URLs. The failing check and module identify the repair scope.
func checkPrePRReadiness(workspace string, selection prePRSelection) error {
	var failures []error
	check := func(label, dir string, env []string, name string, args ...string) bool {
		_, err := prePRRunFixtureCommand(dir, env, name, args...)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s failed; resolve it before running tests", label))
		}
		return err == nil
	}
	cfg, err := prePRLoadCoverageConfig(workspace)
	if err != nil {
		failures = append(failures, err)
	} else {
		modules := map[string]coverageModule{}
		for _, module := range cfg.Modules {
			modules[module.Name] = module
		}
		selected := append([]string(nil), selection.GoModules...)
		for name, enabled := range map[string]bool{"account-manager": selection.AccountManagerPostgres, "video-cloud": selection.VideoCloudPostgresEMQX} {
			if enabled && !slices.Contains(selected, name) {
				selected = append(selected, name)
			}
		}
		for _, name := range selected {
			module, ok := modules[name]
			if !ok {
				failures = append(failures, fmt.Errorf("selected Go module %s is not configured", name))
				continue
			}
			dir := filepath.Join(workspace, module.Path)
			env := append(withoutEnvironmentKey(os.Environ(), "GOWORK"), "GOWORK=off")
			args := append([]string{"list", "-mod=readonly", "-deps", "-test"}, module.Packages...)
			check(name+" Go dependencies", dir, env, "go", args...)
			raw, listErr := prePRRunFixtureCommand(dir, nil, "git", "ls-files", "-z", "--", "*.go")
			if listErr != nil {
				failures = append(failures, fmt.Errorf("%s tracked Go source inventory failed", name))
				continue
			}
			// Test fixtures may intentionally contain malformed source.
			files := []string{}
			for _, path := range strings.Split(string(raw), "\x00") {
				if path == "" || strings.Contains("/"+path, "/testdata/") || strings.Contains("/"+path, "/vendor/") {
					continue
				}
				files = append(files, path)
			}
			for len(files) > 0 {
				n := min(len(files), 100)
				formatted, formatErr := prePRRunFixtureCommand(dir, nil, "gofmt", append([]string{"-l"}, files[:n]...)...)
				if formatErr != nil || strings.TrimSpace(string(formatted)) != "" {
					failures = append(failures, fmt.Errorf("%s Go formatting failed; run gofmt on its source and tests", name))
				}
				files = files[n:]
			}
		}
	}
	if len(selection.NodeModules) > 0 {
		check("Node.js", workspace, nil, "node", "--version")
		check("npm", workspace, nil, "npm", "--version")
	}
	if selection.AccountManagerPostgres {
		check("Account Manager report Python", workspace, nil, "python3", "--version")
	}
	if selection.AccountManagerPostgres || selection.VideoCloudPostgresEMQX {
		if check("local Docker daemon", workspace, nil, "docker", "info") {
			images := []string{"postgres:16"}
			if selection.VideoCloudPostgresEMQX {
				images = append(images, "emqx/emqx:latest")
			}
			for _, image := range images {
				if _, err := prePRRunFixtureCommand(workspace, nil, "docker", "image", "inspect", image); err != nil {
					check("local fixture image "+image, workspace, nil, "docker", "pull", image)
				}
			}
		}
	}
	if len(failures) != 0 {
		return fmt.Errorf("pre-PR prerequisites:\n%w", errors.Join(failures...))
	}
	return nil
}

// Validate all output names before policy/baseline or integration execution.
func checkPrePROutputs(workspace, runID string, selection prePRSelection, ui bool) error {
	if strings.TrimSpace(runID) == "" || runID == "." || runID == ".." || strings.ContainsAny(runID, `/\`) {
		return errors.New("pre-pr run ID must be a single non-empty directory name")
	}
	names := []string{}
	if len(selection.GoModules) > 0 {
		names = append(names, runID+"-go")
	}
	if len(selection.NodeModules) > 0 {
		names = append(names, runID+"-node")
	}
	if selection.AccountManagerPostgres {
		names = append(names, runID+"-account-manager-pr")
	}
	if selection.VideoCloudPostgresEMQX {
		names = append(names, runID+"-video-cloud-pr")
	}
	if ui && slices.Contains(selection.NodeModules, "cloud-admin-web") {
		names = append(names, runID+"-ui")
	}
	for _, name := range names {
		path := filepath.Join(workspace, ".artifacts", "test-runs", name)
		if _, err := os.Stat(path); err == nil {
			return fmt.Errorf("pre-pr output already exists: %s; choose a new --run-id", name)
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}

// The database belongs to this invocation; never probe or reset a caller's DB.
func startAccountManagerPRFixture(workspace string) (func(), error) {
	name := fmt.Sprintf("rtk-account-pre-pr-%d", time.Now().UTC().UnixNano())
	remove := func() { _, _ = prePRRunFixtureCommand(workspace, nil, "docker", "rm", "--force", name) }
	if _, err := prePRRunFixtureCommand(workspace, nil, "docker", "run", "--detach", "--name", name,
		"--rm", "--tmpfs", "/var/lib/postgresql/data:rw,size=2g", "--label", "rtk.local-ci=account-manager-pre-pr",
		"--env", "POSTGRES_DB=rtk_account_manager", "--env", "POSTGRES_USER=rtk",
		"--env", "POSTGRES_PASSWORD=local_integration_only", "--publish", "127.0.0.1::5432", "postgres:16"); err != nil {
		remove()
		return nil, errors.New("start owned Account Manager PostgreSQL fixture failed")
	}
	fail := func(message string) (func(), error) { remove(); return nil, errors.New(message) }
	raw, err := prePRRunFixtureCommand(workspace, nil, "docker", "port", name, "5432/tcp")
	if err != nil {
		return fail("discover Account Manager PostgreSQL port failed")
	}
	endpoint := strings.TrimSpace(string(raw))
	if !strings.HasPrefix(endpoint, "127.0.0.1:") || strings.ContainsAny(endpoint, "\r\n /?#") {
		return fail("Account Manager fixture must have one loopback endpoint")
	}
	image, err := prePRRunFixtureCommand(workspace, nil, "docker", "inspect", "--format={{.Image}}", name)
	if err != nil || strings.TrimSpace(string(image)) == "" {
		return fail("inspect Account Manager fixture image identity failed")
	}
	ready := false
	for attempt := 0; attempt < 30; attempt++ {
		if _, err := prePRRunFixtureCommand(workspace, nil, "docker", "exec", name,
			"pg_isready", "-h", "127.0.0.1", "-U", "rtk", "-d", "rtk_account_manager"); err == nil {
			ready = true
			break
		}
		time.Sleep(2 * time.Second)
	}
	if !ready {
		return fail("Account Manager PostgreSQL did not become ready")
	}
	dsn := "postgres://rtk:local_integration_only@" + endpoint + "/rtk_account_manager?sslmode=disable"
	values := map[string]string{"TEST_DATABASE_URL": dsn, "DATABASE_URL": dsn,
		"REPORT_FIXTURE_ID": "postgres16-tmpfs-v1:" + strings.TrimSpace(string(image))}
	old, present := map[string]string{}, map[string]bool{}
	for key, value := range values {
		old[key], present[key] = os.LookupEnv(key)
		_ = os.Setenv(key, value)
	}
	return func() {
		for key := range values {
			if present[key] {
				_ = os.Setenv(key, old[key])
			} else {
				_ = os.Unsetenv(key)
			}
		}
		remove()
	}, nil
}
