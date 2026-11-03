package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var workspaceCoverageLongTests = []string{
	"TestPRHistoricalLateRootCRLsCompleteDurableCheckpoints",
	"TestPRProductPostCleanupCorrectionCompletesDurableCheckpoint",
}

// Custom module fixtures and narrower package selections retain the serial
// runner. Only the maintained workspace module has these two lifecycle tests.
func useWorkspaceCoverageShards(moduleDir string, module coverageModule) bool {
	if os.Getenv("RTK_COVERAGE_SERIAL") == "1" || module.Name != "workspace-tooling" || filepath.ToSlash(filepath.Clean(module.Path)) != "scripts/go" || !slices.Equal(module.Packages, []string{"./..."}) {
		return false
	}
	name, err := goModuleName(filepath.Join(moduleDir, "go.mod"))
	return err == nil && name == "rtk-cloud-workspace/scripts/go"
}

// All children use one checkout and the original instrumentation/skip options.
// The remainder keeps ./... so newly added tests and packages run automatically.
func runGoCoverageShards(ctx context.Context, moduleDir, logPath, eventsPath, profilePath string, env map[string]string, commonArgs []string) error {
	for _, path := range []string{profilePath, eventsPath, logPath} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	shardRoot := filepath.Join(filepath.Dir(profilePath), "shards")
	if err := os.RemoveAll(shardRoot); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := exec.LookPath("pgrep"); err != nil {
		return fmt.Errorf("coverage shard cancellation requires pgrep: %w", err)
	}
	scratch, err := os.MkdirTemp("", "rtk-coverage-shards-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(scratch) // Children are joined before this scope returns.
	names := []string{"historical-late-root-crls", "postcleanup-correction", "remainder"}
	logs, events, profiles := make([]string, 3), make([]string, 3), make([]string, 3)
	childEnvs := make([]map[string]string, 3)
	for i, name := range names {
		dir := filepath.Join(shardRoot, name)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		privateDir := filepath.Join(scratch, name)
		for _, sub := range []string{"tmp", "config"} {
			if err := os.MkdirAll(filepath.Join(privateDir, sub), 0700); err != nil {
				return err
			}
		}
		logs[i], events[i], profiles[i] = filepath.Join(dir, "coverage.log"), filepath.Join(dir, "test-events.json"), filepath.Join(dir, "coverage.out")
		childEnvs[i] = map[string]string{}
		for key, value := range env {
			childEnvs[i][key] = value
		}
		childEnvs[i]["TMPDIR"], childEnvs[i]["XDG_CONFIG_HOME"] = filepath.Join(privateDir, "tmp"), filepath.Join(privateDir, "config")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make([]error, 3)
	var joined sync.WaitGroup
	for i := range names {
		joined.Add(1)
		go func(i int) {
			defer joined.Done()
			args := slices.Clone(commonArgs)
			if i < 2 {
				args = append(args, "-run=^"+workspaceCoverageLongTests[i]+"$", "./rtk-cloud")
			} else {
				for j, arg := range args {
					if strings.HasPrefix(arg, "-skip=") {
						args[j] = arg + "|^(" + strings.Join(workspaceCoverageLongTests, "|") + ")$"
					}
				}
				// Callers always supply the maintained skip expression. Keep the helper
				// safe for direct invocation as well.
				hasSkip := false
				for _, arg := range args {
					hasSkip = hasSkip || strings.HasPrefix(arg, "-skip=")
				}
				if !hasSkip {
					args = append(args, "-skip=^("+strings.Join(workspaceCoverageLongTests, "|")+")$")
				}
				args = append(args, "./...")
			}
			args = append(args, "-coverprofile="+profiles[i])
			errs[i] = runGoCoverageShardCommand(ctx, moduleDir, logs[i], events[i], childEnvs[i], args)
			if errs[i] != nil {
				cancel()
			}
		}(i)
	}
	joined.Wait()
	// No shared output is opened until every child has joined.
	if err := joinGoCoverageShardLogs(logs, logPath); err != nil {
		return err
	}
	var commandErrors []error
	for i, err := range errs {
		if err != nil {
			commandErrors = append(commandErrors, fmt.Errorf("coverage shard %s: %w", names[i], err))
		}
	}
	if len(commandErrors) > 0 {
		return errors.Join(commandErrors...)
	}
	if err := mergeGoCoverageShardEvents(events, eventsPath); err != nil {
		return err
	}
	if err := mergeGoCoverageShardProfiles(profiles, profilePath); err != nil {
		return err
	}
	return nil
}

func runGoCoverageShardCommand(ctx context.Context, dir, logPath, eventsPath string, env map[string]string, args []string) error {
	pgrep, err := exec.LookPath("pgrep")
	if err != nil {
		return fmt.Errorf("coverage shard cancellation requires pgrep: %w", err)
	}
	log, err := os.OpenFile(logPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer log.Close()
	events, err := os.OpenFile(eventsPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	defer events.Close()
	fmt.Fprintf(log, "$ go %s\n", strings.Join(args, " "))
	fmt.Fprintf(log, "# TMPDIR=%s XDG_CONFIG_HOME=%s\n", env["TMPDIR"], env["XDG_CONFIG_HOME"])
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	cmd.Env = withEnv(os.Environ(), env)
	// Observers and nested test runners may establish their own process groups.
	// Stop and terminate the owned descendants as well as the original group.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return killGoCoverageProcessTree(cmd.Process.Pid, pgrep) }
	cmd.WaitDelay = time.Second
	cmd.Stdout = io.MultiWriter(log, events)
	cmd.Stderr = log
	fmt.Printf("== coverage shard: %s ==\n", filepath.Base(filepath.Dir(logPath)))
	return cmd.Run()
}

// Freeze parents before discovery so they cannot fork new children between
// enumeration and termination. Every stopped PID is killed on every error path.
// pgrep is scoped to one owned parent at a time; unrelated processes are never
// selected by command name or included in a system-wide process listing.
func killGoCoverageProcessTree(root int, pgrep string) (result error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	var stopped []int
	defer func() {
		for i := len(stopped) - 1; i >= 0; i-- {
			if err := syscall.Kill(stopped[i], syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
				result = errors.Join(result, err)
			}
		}
		if err := syscall.Kill(-root, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
			result = errors.Join(result, err)
		}
	}()
	if err := syscall.Kill(-root, syscall.SIGSTOP); err != nil {
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
	var discover func(int) error
	discover = func(pid int) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := syscall.Kill(pid, syscall.SIGSTOP); err != nil {
			if errors.Is(err, syscall.ESRCH) {
				return nil
			}
			return err
		}
		stopped = append(stopped, pid)
		probe := exec.CommandContext(ctx, pgrep, "-P", strconv.Itoa(pid))
		probe.WaitDelay = 100 * time.Millisecond
		output, err := probe.Output()
		var exit *exec.ExitError
		if err != nil && !(errors.As(err, &exit) && exit.ExitCode() == 1) {
			return fmt.Errorf("discover coverage child processes: %w", err)
		}
		for _, field := range strings.Fields(string(output)) {
			child, err := strconv.Atoi(field)
			if err != nil || child <= 1 || child == pid {
				return fmt.Errorf("invalid coverage child PID %q", field)
			}
			if slices.Contains(stopped, child) {
				return fmt.Errorf("repeated coverage child PID %d", child)
			}
			if err := discover(child); err != nil {
				return err
			}
		}
		return nil
	}
	return discover(root)
}

func joinGoCoverageShardLogs(paths []string, output string) error {
	if err := os.MkdirAll(filepath.Dir(output), 0755); err != nil {
		return err
	}
	file, err := os.Create(output)
	if err != nil {
		return err
	}
	defer file.Close()
	for _, path := range paths {
		fmt.Fprintf(file, "== shard %s ==\n", filepath.Base(filepath.Dir(path)))
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if _, err := file.Write(raw); err != nil {
			return err
		}
	}
	return file.Close()
}

func mergeGoCoverageShardProfiles(paths []string, output string) error {
	type block struct {
		location          string
		statements, count int
	}
	mode := ""
	blocks := map[string]block{}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read coverage shard profile: %w", err)
		}
		scanner := bufio.NewScanner(strings.NewReader(string(raw)))
		if !scanner.Scan() {
			return fmt.Errorf("coverage shard profile %s is empty", path)
		}
		header := scanner.Text()
		shardMode := strings.TrimPrefix(header, "mode: ")
		if header == shardMode || (shardMode != "set" && shardMode != "count" && shardMode != "atomic") {
			return fmt.Errorf("invalid coverage shard mode %q", header)
		}
		if mode != "" && mode != shardMode {
			return fmt.Errorf("coverage shard mode mismatch: %s and %s", mode, shardMode)
		}
		mode = shardMode
		for scanner.Scan() {
			line := scanner.Text()
			match := goCoverageLinePattern.FindStringSubmatch(line)
			if match == nil {
				return fmt.Errorf("invalid coverage shard profile line %q", line)
			}
			statements, err := strconv.Atoi(match[6])
			if err != nil {
				return err
			}
			count, err := strconv.Atoi(match[7])
			if err != nil {
				return err
			}
			location := strings.Fields(line)[0]
			current, exists := blocks[location]
			if exists && current.statements != statements {
				return fmt.Errorf("inconsistent statement count for Go coverage block %s", location)
			}
			if mode == "set" {
				count = max(count, current.count)
			} else {
				if count > int(^uint(0)>>1)-current.count {
					return fmt.Errorf("coverage counter overflow for %s", location)
				}
				count += current.count
			}
			blocks[location] = block{location, statements, count}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
	}
	if mode == "" || len(blocks) == 0 {
		return errors.New("coverage shard profiles contain no blocks")
	}
	keys := make([]string, 0, len(blocks))
	for key := range blocks {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var merged strings.Builder
	fmt.Fprintf(&merged, "mode: %s\n", mode)
	for _, key := range keys {
		b := blocks[key]
		fmt.Fprintf(&merged, "%s %d %d\n", b.location, b.statements, b.count)
	}
	return os.WriteFile(output, []byte(merged.String()), 0600)
}

func mergeGoCoverageShardEvents(paths []string, output string) error {
	if len(paths) != 3 {
		return errors.New("workspace coverage requires all three shard event streams")
	}
	owner := map[string]int{}
	var merged strings.Builder
	for i, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read coverage shard events: %w", err)
		}
		scanner := bufio.NewScanner(strings.NewReader(string(raw)))
		scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
		started, completed := map[string]bool{}, map[string]bool{}
		packagePassed := map[string]bool{}
		expectedPassed := false
		expectedPackage := ""
		for scanner.Scan() {
			var event goTestEvent
			if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
				return fmt.Errorf("invalid coverage shard event: %w", err)
			}
			if event.Package == "" || event.Action == "" {
				return errors.New("coverage shard event lacks package or action")
			}
			if event.Action == "fail" {
				return fmt.Errorf("coverage shard failed: %s %s", event.Package, event.Test)
			}
			if event.Test == "" {
				if event.Action == "pass" || event.Action == "skip" {
					packagePassed[event.Package] = true
				}
				continue
			}
			key := event.Package + "\x00" + event.Test
			if prior, ok := owner[key]; ok && prior != i {
				return fmt.Errorf("duplicate Go test across coverage shards: %s %s", event.Package, event.Test)
			}
			owner[key] = i
			top := strings.SplitN(event.Test, "/", 2)[0]
			if i < 2 && (top != workspaceCoverageLongTests[i] || !strings.HasSuffix(event.Package, "/rtk-cloud")) {
				return fmt.Errorf("unexpected test in lifecycle coverage shard: %s %s", event.Package, event.Test)
			}
			if i == 2 && slices.Contains(workspaceCoverageLongTests, top) {
				return fmt.Errorf("lifecycle test also ran in remainder: %s", event.Test)
			}
			switch event.Action {
			case "run":
				if started[key] {
					return fmt.Errorf("duplicate Go test run: %s", event.Test)
				}
				started[key] = true
			case "pass", "skip":
				if !started[key] || completed[key] {
					return fmt.Errorf("coverage test has no unique run/completion: %s", event.Test)
				}
				completed[key] = true
				if i < 2 && event.Test == workspaceCoverageLongTests[i] && event.Action == "pass" {
					expectedPassed = true
					expectedPackage = event.Package
				}
			}
		}
		if err := scanner.Err(); err != nil {
			return err
		}
		if len(packagePassed) == 0 {
			return fmt.Errorf("coverage shard %d has no completed package events", i)
		}
		for key := range started {
			pkg, _, _ := strings.Cut(key, "\x00")
			if !completed[key] || !packagePassed[pkg] {
				return fmt.Errorf("incomplete coverage shard test %s", key)
			}
		}
		if i < 2 && (!expectedPassed || !packagePassed[expectedPackage]) {
			return fmt.Errorf("coverage shard %d lacks expected lifecycle test pass", i)
		}
		if len(raw) == 0 {
			return fmt.Errorf("coverage shard %d has no events", i)
		}
		merged.Write(raw)
		if raw[len(raw)-1] != '\n' {
			merged.WriteByte('\n')
		}
	}
	return os.WriteFile(output, []byte(merged.String()), 0600)
}
