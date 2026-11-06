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
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

// Custom module fixtures and narrower package selections retain the serial runner.
func useWorkspaceCoverageShards(moduleDir string, module coverageModule) bool {
	if os.Getenv("RTK_COVERAGE_SERIAL") == "1" || module.Name != "workspace-tooling" || filepath.ToSlash(filepath.Clean(module.Path)) != "scripts/go" || !slices.Equal(module.Packages, []string{"./..."}) {
		return false
	}
	name, err := goModuleName(filepath.Join(moduleDir, "go.mod"))
	return err == nil && name == "rtk-cloud-workspace/scripts/go"
}

// All children use one checkout and the original instrumentation/skip options.
// Discovery includes newly added tests/packages; hints never define membership.
func runGoCoverageShards(ctx context.Context, moduleDir, logPath, eventsPath, profilePath string, env map[string]string, commonArgs []string) error {
	discoveryLog := filepath.Join(filepath.Dir(profilePath), "discovery.log")
	discoveryEvents := filepath.Join(filepath.Dir(profilePath), "discovery-events.json")
	planPath := filepath.Join(filepath.Dir(profilePath), "shard-plan.json")
	for _, path := range []string{profilePath, eventsPath, logPath, discoveryLog, discoveryEvents, planPath} {
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
	names := []string{"group-1", "group-2", "group-3"}
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
	// Listing warms the instrumented build before parallel execution. Without
	// -cover, unit discovery builds a different binary and all three workers
	// compile the large covered package at once. Bound cold-build parallelism.
	discoveryEnv := map[string]string{}
	for key, value := range env {
		discoveryEnv[key] = value
	}
	for _, key := range []string{"TMPDIR", "XDG_CONFIG_HOME"} {
		dir := filepath.Join(scratch, "discovery", key)
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		discoveryEnv[key] = dir
	}
	discoveryArgs := append(slices.Clone(commonArgs), "-cover", "-p=1", "-list=.", "./...")
	if err := runGoCoverageShardCommand(ctx, moduleDir, discoveryLog, discoveryEvents, discoveryEnv, discoveryArgs); err != nil {
		_ = joinGoCoverageShardLogs([]string{discoveryLog}, logPath)
		return fmt.Errorf("discover coverage tests: %w", err)
	}
	tests, err := discoverGoCoverageTests(discoveryEvents, commonArgs)
	if err != nil {
		return err
	}
	groups := balanceGoCoverageTests(tests, workspaceCoverageTimingHints)
	plan, err := json.MarshalIndent(groups, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(planPath, append(plan, '\n'), 0600); err != nil {
		return err
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
			var patterns []string
			for _, test := range groups[i].Tests {
				patterns = append(patterns, regexp.QuoteMeta(test.Name))
			}
			sort.Strings(patterns)
			patterns = slices.Compact(patterns)
			args = append(args, "-run=^("+strings.Join(patterns, "|")+")$", "./...")
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
	if err := mergeGoCoverageShardEvents(events, eventsPath, groups); err != nil {
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

func mergeGoCoverageShardEvents(paths []string, output string, groups []goCoverageShard) error {
	if len(paths) != 3 || len(groups) != 3 {
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
		expected := map[string]bool{}
		for _, test := range groups[i].Tests {
			expected[test.Package+"\x00"+test.Name] = true
		}
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
			if !expected[event.Package+"\x00"+top] {
				return fmt.Errorf("unexpected test in coverage shard %d: %s %s", i, event.Package, event.Test)
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
		for key := range expected {
			if !completed[key] {
				return fmt.Errorf("coverage shard %d lacks discovered test completion: %s", i, key)
			}
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

// Timings only influence scheduling. Inventory always comes from Go's compiled
// test list, including examples and fuzz seeds run by a normal go test command.
type goCoverageTest struct {
	Package string `json:"package"`
	Name    string `json:"name"`
}

type goCoverageShard struct {
	EstimatedSeconds float64          `json:"estimated_seconds"`
	Tests            []goCoverageTest `json:"tests"`
}

func discoverGoCoverageTests(path string, args []string) ([]goCoverageTest, error) {
	var skip *regexp.Regexp
	for _, arg := range args {
		if strings.HasPrefix(arg, "-skip=") {
			var err error
			skip, err = regexp.Compile(strings.TrimPrefix(arg, "-skip="))
			if err != nil {
				return nil, err
			}
		}
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	scanner.Buffer(make([]byte, 64*1024), 4*1024*1024)
	var tests []goCoverageTest
	seen := map[goCoverageTest]bool{}
	packages := map[string]bool{}
	for scanner.Scan() {
		var event struct {
			goTestEvent
			Output string `json:"Output"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return nil, err
		}
		if event.Action == "fail" {
			return nil, fmt.Errorf("coverage discovery failed: %s", event.Package)
		}
		if event.Test == "" && (event.Action == "pass" || event.Action == "skip") {
			packages[event.Package] = true
		}
		name := strings.TrimSpace(event.Output)
		if event.Action != "output" || event.Test != "" || len(strings.Fields(name)) != 1 || !(strings.HasPrefix(name, "Test") || strings.HasPrefix(name, "Example") || strings.HasPrefix(name, "Fuzz")) {
			continue
		}
		if skip != nil && skip.MatchString(name) {
			continue
		}
		test := goCoverageTest{event.Package, name}
		if seen[test] {
			return nil, fmt.Errorf("duplicate discovered Go test: %s %s", test.Package, test.Name)
		}
		seen[test] = true
		tests = append(tests, test)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(packages) == 0 {
		return nil, errors.New("coverage discovery has no completed packages")
	}
	for _, test := range tests {
		if !packages[test.Package] {
			return nil, fmt.Errorf("incomplete coverage discovery for %s", test.Package)
		}
	}
	return tests, nil
}

// Longest-first packing keeps three bounded workers. Identically named tests
// in different packages travel together because -run applies to every package.
// Unknown tests get one second, so cold CI distributes the ordinary remainder
// without requiring a previous run or introducing an allowlist.
func balanceGoCoverageTests(tests []goCoverageTest, hints map[string]float64) []goCoverageShard {
	byName := map[string][]goCoverageTest{}
	weights := map[string]float64{}
	for _, test := range tests {
		byName[test.Name] = append(byName[test.Name], test)
		weight := hints[test.Name]
		if weight <= 0 {
			weight = 1
		}
		weights[test.Name] += weight
	}
	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Slice(names, func(i, j int) bool {
		if weights[names[i]] == weights[names[j]] {
			return names[i] < names[j]
		}
		return weights[names[i]] > weights[names[j]]
	})
	groups := make([]goCoverageShard, 3)
	for _, name := range names {
		i := 0
		for j := 1; j < len(groups); j++ {
			if groups[j].EstimatedSeconds < groups[i].EstimatedSeconds {
				i = j
			}
		}
		groups[i].Tests = append(groups[i].Tests, byName[name]...)
		groups[i].EstimatedSeconds += weights[name]
	}
	for i := range groups {
		sort.Slice(groups[i].Tests, func(a, b int) bool {
			x, y := groups[i].Tests[a], groups[i].Tests[b]
			if x.Package == y.Package {
				return x.Name < y.Name
			}
			return x.Package < y.Package
		})
	}
	return groups
}
