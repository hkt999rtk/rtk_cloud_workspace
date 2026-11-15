package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// isolatedScriptInvocation is deliberately test-only. Its processes inherit an
// OS-enforced outbound deny policy, not merely synthetic provider credentials.
type isolatedScriptInvocation struct {
	workspace, environmentRoot, fakeBin string
	store                               secretStore
	execution                           *environmentCreateExecution
	ports                               []int
	env                                 map[string]string
	detached                            bool
	configurationSHA                    string
	goCache, goModCache                 string
	timeout                             time.Duration
}

func isolatedScriptTempPath(path string) bool {
	root, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return false
	}
	actual, err := filepath.EvalSymlinks(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(root, actual)
	return err == nil && rel != "." && rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

func (f *isolatedScriptInvocation) run(t *testing.T, command string, args ...string) (string, error) {
	t.Helper()
	if runtime.GOOS != "darwin" {
		return "", errors.New("isolated script sandbox requires macOS sandbox-exec")
	}
	if f.execution == nil || f.execution.Store.Root != f.store.Root || f.store.Environment != f.execution.Journal.Environment || !isolatedScriptTempPath(f.workspace) || !isolatedScriptTempPath(f.environmentRoot) || !isolatedScriptTempPath(f.store.Root) || !isolatedScriptTempPath(f.store.ConfigRoot) || !isolatedScriptTempPath(f.fakeBin) {
		return "", errors.New("synthetic workspace, environment, store, fake tools and original owner are required")
	}
	cfg, err := resolveDeploymentConfig(f.workspace, f.store.Environment, f.environmentRoot)
	if err != nil || cfg.Environment != f.store.Environment {
		return "", errors.New("isolated selected environment cannot be resolved")
	}
	selectedSHA := deploymentplan.Digest(cfg)
	if !f.detached {
		parent, perr := authenticatedCreateOperatorPlan(f.store, f.execution)
		if perr != nil || parent == nil || parent.ConfigurationSHA256 != selectedSHA {
			return "", errors.New("isolated selected environment differs from the original parent")
		}
		f.configurationSHA = selectedSHA
	} else if f.configurationSHA != selectedSHA {
		return "", errors.New("isolated selected environment changed after parent handoff")
	}
	for k, v := range f.env {
		if !strings.HasPrefix(k, "RTK_") || k == "RTK_CLOUD_CONFIG_ROOT" || k == "RTK_CLOUD_TEST_MODE" || k == "RTK_CLOUD_KUBECTL" || strings.ContainsAny(k, "=\x00") || strings.ContainsRune(v, 0) {
			return "", errors.New("invalid synthetic process environment")
		}
	}
	for _, name := range []string{"go", "kubectl", "gh", "docker", "curl"} {
		p := filepath.Join(f.fakeBin, name)
		info, err := os.Lstat(p)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0100 == 0 {
			return "", fmt.Errorf("isolated %s executable required", name)
		}
	}
	root, err := workspaceRoot()
	if err != nil {
		return "", err
	}
	var script string
	var cli []string
	switch command {
	case "preflight":
		script = filepath.Join(root, "scripts/check-deployment-preflight.sh")
		cli = []string{"--environment", f.store.Environment, "--workspace", f.workspace, "--environment-root", f.environmentRoot}
	case "create":
		script = filepath.Join(root, "scripts/deploy-environment.sh")
		cli = []string{"create", "--environment", f.store.Environment, "--workspace", f.workspace, "--environment-root", f.environmentRoot}
	default:
		return "", errors.New("only original preflight and create wrappers are supported")
	}
	cli = append(cli, args...)
	// No caller may redirect the selected paths after validation.
	for _, a := range args {
		if strings.HasPrefix(a, "--workspace") || strings.HasPrefix(a, "--environment-root") || strings.HasPrefix(a, "--environment") {
			return "", errors.New("selected isolation arguments cannot be overridden")
		}
	}
	profile := "(version 1)(allow default)(deny network-outbound)"
	for _, port := range f.ports {
		if port <= 0 || port > 65535 {
			return "", errors.New("invalid isolated loopback port")
		}
		profile += fmt.Sprintf("(allow network-outbound (remote ip %s))", strconv.Quote(fmt.Sprintf("localhost:%d", port)))
	}
	account, err := user.Current()
	if err != nil || account.HomeDir == "" || !filepath.IsAbs(account.HomeDir) {
		return "", errors.New("cannot identify account home for sandbox deny rules")
	}
	home := account.HomeDir
	for _, path := range []string{filepath.Join(home, ".config/rtk_cloud"), filepath.Join(home, ".aws"), filepath.Join(home, ".ssh"), filepath.Join(home, ".env")} {
		profile += fmt.Sprintf("(deny file-read* file-write* (subpath %s))", strconv.Quote(path))
	}
	// The former parent owner must release its actual native lock before the
	// CLI attempts to reopen the same journal. No synthetic receipt is made.
	if !f.detached {
		if err := f.execution.Close(); err != nil {
			return "", err
		}
		f.detached = true
	}
	fence := filepath.Join(f.store.Root, "deployment/.execution-active")
	lock, err := deploymentplan.LockExecutionOwner(fence, false)
	if err != nil {
		return "", errors.New("original execution lock did not transfer")
	}
	var owner environmentCreateOwner
	var journal environmentCreateJournal
	fenceInfo, fenceErr := os.Lstat(fence)
	lockInfo, lockErr := lock.Stat()
	valid := fenceErr == nil && lockErr == nil && os.SameFile(fenceInfo, f.execution.ownerInfo) && os.SameFile(lockInfo, f.execution.runningInfo) && readEnvironmentPreflightJSON(filepath.Join(fence, "owner.json"), &owner) == nil && deploymentplan.Digest(owner) == deploymentplan.Digest(f.execution.owner) && readEnvironmentPreflightJSON(f.execution.JournalPath, &journal) == nil && journal.Environment == owner.Environment && journal.Stack == owner.Stack && journal.PlanSHA256 == owner.PlanSHA256 && journal.OwnerToken == owner.OwnerToken
	closeErr := lock.Close()
	if !valid || closeErr != nil {
		return "", errors.New("original execution owner or native lock changed during handoff")
	}
	limit := f.timeout
	if limit <= 0 {
		limit = 3 * time.Minute
	}
	ctx, cancel := context.WithTimeout(t.Context(), limit)
	defer cancel()
	argv := append([]string{"-p", profile, "/bin/bash", script}, cli...)
	cmd := exec.CommandContext(ctx, "/usr/bin/sandbox-exec", argv...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = 5 * time.Second
	cache := f.goCache
	if cache == "" {
		cache = os.Getenv("GOCACHE")
	}
	if !filepath.IsAbs(cache) {
		cache = t.TempDir()
	}
	moduleCache := f.goModCache
	if moduleCache == "" {
		moduleCache = filepath.Join(home, "go/pkg/mod")
	}
	if !filepath.IsAbs(moduleCache) {
		return "", errors.New("absolute Go module cache path required")
	}
	cmd.Env = []string{"PATH=" + f.fakeBin + ":/usr/bin:/bin:/usr/sbin:/sbin", "RTK_CLOUD_TEST_MODE=1", "RTK_CLOUD_CONFIG_ROOT=" + f.store.ConfigRoot, "GOPROXY=off", "GOTOOLCHAIN=local", "GOENV=off", "GOMODCACHE=" + moduleCache, "RTK_CLOUD_KUBECTL=" + filepath.Join(f.fakeBin, "kubectl"), "GOCACHE=" + cache, "TMPDIR=" + os.TempDir()}
	// Only explicit synthetic controls are exposed. Host credentials and proxy
	// environment variables are not inherited into the process.
	for k, v := range f.env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	err = cmd.Run()
	return output.String(), err
}
