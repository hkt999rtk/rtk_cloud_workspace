package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

const (
	mqttLoadDefaultUsers       = 2500
	mqttLoadDefaultDevices     = 10000
	mqttLoadDefaultMix         = "light=3334,air_conditioner=3333,smart_meter=3333"
	mqttLoadDefaultProfile     = "baseline-10k"
	mqttLoadDefaultRampUp      = "10m"
	mqttLoadDefaultDuration    = "30m"
	mqttLoadDefaultTelemetry   = "5m"
	mqttLoadDefaultState       = "1h"
	mqttLoadDefaultCommandRate = "1"
)

func runMQTTLoadTest(args []string) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printMQTTLoadTestUsage()
		return nil
	}
	switch args[0] {
	case "prepare":
		return runMQTTLoadTestPrepare(args[1:])
	case "run":
		return runMQTTLoadTestRun(args[1:])
	case "aggregate":
		return runMQTTLoadTestAggregate(args[1:])
	default:
		return fmt.Errorf("unknown mqtt-loadtest subcommand: %s", args[0])
	}
}

func printMQTTLoadTestUsage() {
	fmt.Fprintln(os.Stdout, `usage: rtk-cloud mqtt-loadtest <prepare|run|aggregate> [options]

Two-phase MQTT-only load test helper for Linode staging.

Subcommands:
  prepare    create/validate users, MQTT-only devices, bind artifact, and bind validation
  run        run one local shard or dispatch shards to manually supplied SSH hosts
  aggregate  merge shard results into one report`)
}

func runMQTTLoadTestPrepare(args []string) error {
	fs := flag.NewFlagSet("mqtt-loadtest prepare", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	brandname := fs.String("brandname", "RTK", "brand name")
	userCount := fs.Int("user-count", mqttLoadDefaultUsers, "user count")
	deviceCount := fs.Int("device-count", mqttLoadDefaultDevices, "device count")
	deviceMix := fs.String("device-mix", mqttLoadDefaultMix, "MQTT-only device mix")
	devicePrefix := fs.String("device-prefix", "load-device", "device prefix")
	runMode := fs.Bool("run", false, "execute commands")
	planMode := fs.Bool("plan", false, "print commands only")
	outDir := fs.String("out-dir", "", "report output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	_ = planMode
	if *envRootFlag == "" {
		return errors.New("--env-root is required")
	}
	if *userCount <= 0 || *deviceCount <= 0 {
		return errors.New("--user-count and --device-count must be positive")
	}
	workspace, envRoot, err := resolveWorkspaceAndEnv(*workspaceFlag, *envRootFlag)
	if err != nil {
		return err
	}
	if *outDir == "" {
		*outDir = filepath.Join(envRoot, "artifacts", "mqtt-loadtest-prepare", time.Now().UTC().Format("20060102T150405Z"))
	}
	slug := brandSlug(*brandname)
	devicesDir := filepath.Join(envRoot, "devices", "test_device")
	commands := [][]string{
		commandWithArgs(selfCommandPath("create-users"), "--workspace", workspace, "--env-root", envRoot, "--brandname", *brandname, "--count", strconv.Itoa(*userCount)),
		commandWithArgs(selfCommandPath("generate-load-devices"), "--workspace", workspace, "--env-root", envRoot, "--count", strconv.Itoa(*deviceCount), "--mix", *deviceMix, "--prefix", *devicePrefix, "--force"),
	}
	if !*runMode {
		fmt.Fprintln(os.Stdout, "mqtt-loadtest prepare plan")
		fmt.Fprintf(os.Stdout, "workspace: %s\n", workspace)
		fmt.Fprintf(os.Stdout, "env_root: %s\n", envRoot)
		fmt.Fprintf(os.Stdout, "brandname: %s\n", *brandname)
		fmt.Fprintf(os.Stdout, "user_count: %d\n", *userCount)
		fmt.Fprintf(os.Stdout, "device_count: %d\n", *deviceCount)
		fmt.Fprintf(os.Stdout, "device_mix: %s\n", *deviceMix)
		for _, argv := range [][]string{
			planSelfCommand("create-users", "--workspace", workspace, "--env-root", envRoot, "--brandname", *brandname, "--count", strconv.Itoa(*userCount)),
			planSelfCommand("generate-load-devices", "--workspace", workspace, "--env-root", envRoot, "--count", strconv.Itoa(*deviceCount), "--mix", *deviceMix, "--prefix", *devicePrefix, "--force"),
		} {
			fmt.Fprintf(os.Stdout, "  - %s\n", shellQuoteArgs(argv))
		}
		fmt.Fprintf(os.Stdout, "  - bind-devices uses latest %s-users-*.json after create-users\n", slug)
		fmt.Fprintf(os.Stdout, "  - validate-device-bind uses latest %s-device-bind-*.json after bind-devices\n", slug)
		return nil
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	steps := []e2eStep{}
	for _, row := range []struct {
		name string
		argv []string
	}{
		{"create_users", commands[0]},
		{"create_devices", commands[1]},
	} {
		step, err := runE2EStep(row.name, filepath.Join(*outDir, row.name+".log"), row.argv...)
		steps = append(steps, step)
		if err != nil {
			return err
		}
	}
	usersFile := latestMatchingFile(filepath.Join(envRoot, "artifacts", "users"), slug+"-users-*.json")
	if usersFile == "" {
		return fmt.Errorf("no users artifact found for brand slug %s", slug)
	}
	bindArgs := commandWithArgs(selfCommandPath("bind-devices"), "--workspace", workspace, "--env-root", envRoot, "--brandname", *brandname, "--users-file", usersFile, "--devices-dir", devicesDir, "--count", strconv.Itoa(*deviceCount))
	step, err := runE2EStep("bind_devices", filepath.Join(*outDir, "bind_devices.log"), bindArgs...)
	steps = append(steps, step)
	if err != nil {
		return err
	}
	bindFile := latestMatchingFile(filepath.Join(envRoot, "artifacts", "device-bind"), slug+"-device-bind-*.json")
	if bindFile == "" {
		return fmt.Errorf("no device-bind artifact found for brand slug %s", slug)
	}
	expectedPerUser := (*deviceCount + *userCount - 1) / *userCount
	validateArgs := commandWithArgs(selfCommandPath("validate-device-bind"), "--workspace", workspace, "--env-root", envRoot, "--bind-artifact", bindFile, "--out-dir", filepath.Join(*outDir, "bind-validation"), "--expected-count", strconv.Itoa(*deviceCount), "--expected-devices-per-user", strconv.Itoa(expectedPerUser), "--wait-provisioned-timeout", firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_BIND_PROVISION_TIMEOUT"), "10m"), "--wait-provisioned-poll", firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_BIND_PROVISION_POLL"), "10s"))
	step, err = runE2EStep("validate_bind", filepath.Join(*outDir, "validate_bind.log"), validateArgs...)
	steps = append(steps, step)
	if err != nil {
		return err
	}
	summary := map[string]any{
		"overall":          "pass",
		"generated_at":     time.Now().UTC().Format(time.RFC3339),
		"env_root":         envRoot,
		"brandname":        *brandname,
		"user_count":       *userCount,
		"device_count":     *deviceCount,
		"device_mix":       *deviceMix,
		"users_file":       usersFile,
		"device_bind_file": bindFile,
		"steps":            steps,
	}
	summaryFile := filepath.Join(*outDir, "summary.json")
	if err := writeJSON(summaryFile, summary); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"overall": "pass", "summary_file": summaryFile})
}

func runMQTTLoadTestRun(args []string) error {
	fs := flag.NewFlagSet("mqtt-loadtest run", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	brandname := fs.String("brandname", "RTK", "brand name")
	outDir := fs.String("out-dir", "", "output directory")
	hostsFile := fs.String("hosts-file", "", "optional SSH hosts file")
	remoteWorkspace := fs.String("remote-workspace", "", "workspace path on remote load-generator hosts")
	remoteEnvRoot := fs.String("remote-env-root", "", "env-root path on remote load-generator hosts")
	remoteOutRoot := fs.String("remote-out-root", "/tmp/rtk-mqtt-loadtest", "output root on remote load-generator hosts")
	syncRemote := fs.Bool("sync-remote", false, "copy scripts/go and env-root to remote hosts before running")
	planOnly := fs.Bool("plan", false, "print run plan")
	profile := fs.String("profile", mqttLoadDefaultProfile, "load profile")
	shardIndex := fs.Int("shard-index", 0, "local shard index")
	shardCount := fs.Int("shard-count", 1, "total shard count")
	rampUp := fs.String("ramp-up", mqttLoadDefaultRampUp, "ramp-up duration")
	duration := fs.String("duration", mqttLoadDefaultDuration, "steady run duration")
	telemetry := fs.String("telemetry-interval", mqttLoadDefaultTelemetry, "device telemetry interval")
	state := fs.String("state-interval", mqttLoadDefaultState, "state interval")
	commandRate := fs.String("command-rate-per-device-per-day", mqttLoadDefaultCommandRate, "command rate per device per day")
	concurrency := fs.Int("concurrency", 250, "local probe concurrency")
	maxConnected := fs.Int("max-connected-devices", 0, "optional max connected devices per shard")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required")
	}
	if *shardCount <= 0 || *shardIndex < 0 || *shardIndex >= *shardCount {
		return errors.New("--shard-count must be positive and --shard-index must be within range")
	}
	workspace, envRoot, err := resolveWorkspaceAndEnv(*workspaceFlag, *envRootFlag)
	if err != nil {
		return err
	}
	if *outDir == "" {
		*outDir = filepath.Join(envRoot, "artifacts", "mqtt-loadtest", time.Now().UTC().Format("20060102T150405Z"))
	}
	hosts := []string{}
	if *hostsFile != "" {
		hosts, err = readHostLines(*hostsFile)
		if err != nil {
			return err
		}
		if len(hosts) == 0 {
			return errors.New("--hosts-file did not contain any hosts")
		}
	}
	if len(hosts) > 0 {
		*shardCount = len(hosts)
	}
	if *remoteWorkspace == "" {
		*remoteWorkspace = workspace
	}
	if *remoteEnvRoot == "" {
		*remoteEnvRoot = envRoot
	}
	if *planOnly {
		fmt.Fprintln(os.Stdout, "mqtt-loadtest run plan")
		fmt.Fprintf(os.Stdout, "workspace: %s\n", workspace)
		fmt.Fprintf(os.Stdout, "env_root: %s\n", envRoot)
		fmt.Fprintf(os.Stdout, "brandname: %s\n", *brandname)
		fmt.Fprintf(os.Stdout, "profile: %s\n", *profile)
		fmt.Fprintf(os.Stdout, "shards: %d\n", *shardCount)
		if len(hosts) > 0 {
			for idx, host := range hosts {
				fmt.Fprintf(os.Stdout, "  - shard %d host %s remote_workspace=%s remote_env_root=%s\n", idx, host, *remoteWorkspace, *remoteEnvRoot)
			}
		} else {
			fmt.Fprintf(os.Stdout, "  - local shard %d/%d\n", *shardIndex, *shardCount)
		}
		return nil
	}
	if len(hosts) > 0 {
		durationSeconds, err := durationStringSeconds(*duration)
		if err != nil {
			return err
		}
		return runRemoteMQTTLoadShards(remoteMQTTLoadInput{
			Hosts:           hosts,
			Workspace:       *remoteWorkspace,
			EnvRoot:         *remoteEnvRoot,
			LocalOutDir:     *outDir,
			RemoteOutRoot:   *remoteOutRoot,
			Brandname:       *brandname,
			Profile:         *profile,
			RampUp:          *rampUp,
			Duration:        *duration,
			Telemetry:       *telemetry,
			State:           *state,
			CommandRate:     *commandRate,
			Concurrency:     *concurrency,
			MaxConnected:    *maxConnected,
			DurationSeconds: durationSeconds,
			SyncRemote:      *syncRemote,
			LocalWorkspace:  workspace,
			LocalEnvRoot:    envRoot,
		})
	}
	durationSeconds, err := durationStringSeconds(*duration)
	if err != nil {
		return err
	}
	shardOut := filepath.Join(*outDir, "shards", fmt.Sprintf("%03d", *shardIndex))
	argv := commandWithArgs(selfCommandPath("mqtt-test"),
		"--workspace", workspace,
		"--env-root", envRoot,
		"--brandname", *brandname,
		"--profile", *profile,
		"--duration-seconds", strconv.Itoa(durationSeconds),
		"--out-dir", shardOut,
		"--mqtt-probe",
		"--shard-index", strconv.Itoa(*shardIndex),
		"--shard-count", strconv.Itoa(*shardCount),
		"--ramp-up", *rampUp,
		"--telemetry-interval", *telemetry,
		"--state-interval", *state,
		"--command-rate-per-device-per-day", *commandRate,
		"--concurrency", strconv.Itoa(*concurrency),
	)
	if *maxConnected > 0 {
		argv = append(argv, "--max-connected-devices", strconv.Itoa(*maxConnected))
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

type remoteMQTTLoadInput struct {
	Hosts           []string
	Workspace       string
	EnvRoot         string
	LocalOutDir     string
	RemoteOutRoot   string
	Brandname       string
	Profile         string
	RampUp          string
	Duration        string
	Telemetry       string
	State           string
	CommandRate     string
	Concurrency     int
	MaxConnected    int
	DurationSeconds int
	SyncRemote      bool
	LocalWorkspace  string
	LocalEnvRoot    string
}

func runRemoteMQTTLoadShards(input remoteMQTTLoadInput) error {
	if len(input.Hosts) == 0 {
		return errors.New("no remote hosts provided")
	}
	if input.Workspace == "" || input.EnvRoot == "" {
		return errors.New("remote workspace and env-root are required")
	}
	if input.RemoteOutRoot == "" {
		input.RemoteOutRoot = "/tmp/rtk-mqtt-loadtest"
	}
	localShards := filepath.Join(input.LocalOutDir, "shards")
	if err := os.MkdirAll(localShards, 0o755); err != nil {
		return err
	}
	runID := time.Now().UTC().Format("20060102T150405Z")
	for idx, host := range input.Hosts {
		if input.SyncRemote {
			fmt.Fprintf(os.Stderr, "[mqtt-loadtest] sync remote inputs: host=%s\n", host)
			if err := syncRemoteMQTTLoadInputs(host, input.LocalWorkspace, input.Workspace, input.LocalEnvRoot, input.EnvRoot); err != nil {
				return fmt.Errorf("sync remote inputs to %s: %w", host, err)
			}
		}
		remoteOut := filepath.Join(input.RemoteOutRoot, runID, "shards", fmt.Sprintf("%03d", idx))
		fmt.Fprintf(os.Stderr, "[mqtt-loadtest] remote shard %d/%d start: host=%s\n", idx, len(input.Hosts), host)
		if err := runSSH(host, buildRemoteMQTTShardCommand(input, idx, len(input.Hosts), remoteOut)); err != nil {
			return fmt.Errorf("remote shard %d on %s failed: %w", idx, host, err)
		}
		localShardDir := filepath.Join(localShards, fmt.Sprintf("%03d", idx))
		if err := os.MkdirAll(localShardDir, 0o755); err != nil {
			return err
		}
		if err := runSCP(host+":"+shellQuoteArg(filepath.Join(remoteOut, "results.json")), filepath.Join(localShardDir, "results.json")); err != nil {
			return fmt.Errorf("copy shard %d results from %s: %w", idx, host, err)
		}
		_ = runSCP(host+":"+shellQuoteArg(filepath.Join(remoteOut, "TEST_REPORT.md")), filepath.Join(localShardDir, "TEST_REPORT.md"))
	}
	return runMQTTLoadTestAggregate([]string{"--input-dir", localShards, "--out-dir", filepath.Join(input.LocalOutDir, "aggregate")})
}

func buildRemoteMQTTShardCommand(input remoteMQTTLoadInput, shardIndex, shardCount int, remoteOut string) string {
	argv := []string{
		"cd", filepath.Join(input.Workspace, "scripts", "go"), "&&",
		"GOWORK=off", "go", "run", "./rtk-cloud", "--", "mqtt-test",
		"--workspace", input.Workspace,
		"--env-root", input.EnvRoot,
		"--brandname", input.Brandname,
		"--profile", input.Profile,
		"--duration-seconds", strconv.Itoa(input.DurationSeconds),
		"--out-dir", remoteOut,
		"--mqtt-probe",
		"--shard-index", strconv.Itoa(shardIndex),
		"--shard-count", strconv.Itoa(shardCount),
		"--ramp-up", input.RampUp,
		"--telemetry-interval", input.Telemetry,
		"--state-interval", input.State,
		"--command-rate-per-device-per-day", input.CommandRate,
		"--concurrency", strconv.Itoa(input.Concurrency),
	}
	if input.MaxConnected > 0 {
		argv = append(argv, "--max-connected-devices", strconv.Itoa(input.MaxConnected))
	}
	return strings.Join(shellQuoteArgsList(argv), " ")
}

func runSSH(host, command string) error {
	cmd := exec.Command("ssh", host, command)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func runSCP(from, to string) error {
	cmd := exec.Command("scp", from, to)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func syncRemoteMQTTLoadInputs(host, localWorkspace, remoteWorkspace, localEnvRoot, remoteEnvRoot string) error {
	if localWorkspace == "" || remoteWorkspace == "" || localEnvRoot == "" || remoteEnvRoot == "" {
		return errors.New("local and remote workspace/env-root paths are required for sync")
	}
	if err := tarToRemote(host, localWorkspace, []string{"scripts/go"}, remoteWorkspace); err != nil {
		return err
	}
	return tarToRemote(host, localEnvRoot, []string{"."}, remoteEnvRoot)
}

func tarToRemote(host, localRoot string, includes []string, remoteRoot string) error {
	args := append([]string{"-C", localRoot, "-czf", "-"}, includes...)
	tarCmd := exec.Command("tar", args...)
	sshCmd := exec.Command("ssh", host, "mkdir -p "+shellQuoteArg(remoteRoot)+" && tar -C "+shellQuoteArg(remoteRoot)+" -xzf -")
	reader, writer, err := os.Pipe()
	if err != nil {
		return err
	}
	defer reader.Close()
	tarCmd.Stdout = writer
	tarCmd.Stderr = os.Stderr
	sshCmd.Stdin = reader
	sshCmd.Stdout = os.Stdout
	sshCmd.Stderr = os.Stderr
	if err := tarCmd.Start(); err != nil {
		_ = writer.Close()
		return err
	}
	if err := sshCmd.Start(); err != nil {
		_ = writer.Close()
		_ = tarCmd.Wait()
		return err
	}
	tarErr := tarCmd.Wait()
	_ = writer.Close()
	sshErr := sshCmd.Wait()
	if tarErr != nil {
		return tarErr
	}
	return sshErr
}

func runMQTTLoadTestAggregate(args []string) error {
	fs := flag.NewFlagSet("mqtt-loadtest aggregate", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	inputDir := fs.String("input-dir", "", "directory containing shard subdirectories")
	outDir := fs.String("out-dir", "", "aggregate output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *inputDir == "" {
		return errors.New("--input-dir is required")
	}
	if *outDir == "" {
		*outDir = filepath.Join(*inputDir, "aggregate")
	}
	matches, err := filepath.Glob(filepath.Join(*inputDir, "*", "results.json"))
	if err != nil {
		return err
	}
	sort.Strings(matches)
	if len(matches) == 0 {
		return fmt.Errorf("no shard results found under %s", *inputDir)
	}
	shards := []map[string]any{}
	for _, path := range matches {
		row := map[string]any{}
		if err := readJSONFile(path, &row); err != nil {
			return fmt.Errorf("read shard %s: %w", path, err)
		}
		shards = append(shards, row)
	}
	result := aggregateMQTTShardResults(shards)
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	resultsFile := filepath.Join(*outDir, "results.json")
	reportFile := filepath.Join(*outDir, "TEST_REPORT.md")
	result["results_file"] = resultsFile
	result["report_file"] = reportFile
	if err := writeJSON(resultsFile, result); err != nil {
		return err
	}
	if err := os.WriteFile(reportFile, []byte(renderMQTTAggregateReport(result)), 0o644); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"overall": result["overall"], "results_file": resultsFile, "report_file": reportFile})
}

func aggregateMQTTShardResults(shards []map[string]any) map[string]any {
	totalDevices := 0
	totalCommands := 0
	totalPassed := 0
	latencies := []float64{}
	overall := "pass"
	for _, shard := range shards {
		if stringFromAny(shard["overall"]) != "pass" {
			overall = "fail"
		}
		metrics, _ := shard["metrics"].(map[string]any)
		totalDevices += intFromAny(metrics["devices_selected"])
		totalCommands += intFromAny(metrics["commands_attempted"])
		totalPassed += intFromAny(metrics["commands_passed"])
		if devices, ok := shard["devices"].([]any); ok {
			for _, raw := range devices {
				row, _ := raw.(map[string]any)
				if values, ok := row["latency_ms"].([]any); ok {
					for _, value := range values {
						if f, ok := floatFromAny(value); ok {
							latencies = append(latencies, f)
						}
					}
				}
			}
		}
	}
	successRate := 0.0
	if totalCommands > 0 {
		successRate = float64(totalPassed) / float64(totalCommands) * 100
	}
	if successRate < 95 {
		overall = "fail"
	}
	return map[string]any{
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"status":       strings.ToUpper(overall),
		"overall":      overall,
		"shard_count":  len(shards),
		"metrics": map[string]any{
			"devices_selected":       totalDevices,
			"commands_attempted":     totalCommands,
			"commands_passed":        totalPassed,
			"success_rate_percent":   successRate,
			"command_latency_p95_ms": mqttLoadPercentile(latencies, 95),
			"command_latency_p99_ms": mqttLoadPercentile(latencies, 99),
		},
	}
}

func renderMQTTAggregateReport(result map[string]any) string {
	metrics, _ := result["metrics"].(map[string]any)
	return strings.Join([]string{
		"# MQTT Load-Test Aggregate Report",
		"",
		fmt.Sprintf("- Status: %s", result["status"]),
		fmt.Sprintf("- Overall: %s", result["overall"]),
		fmt.Sprintf("- Generated: %s", result["generated_at"]),
		fmt.Sprintf("- Shards: %v", result["shard_count"]),
		"",
		"## Metrics",
		"",
		fmt.Sprintf("- Devices selected: %v", metrics["devices_selected"]),
		fmt.Sprintf("- Commands attempted: %v", metrics["commands_attempted"]),
		fmt.Sprintf("- Commands passed: %v", metrics["commands_passed"]),
		fmt.Sprintf("- Success rate percent: %.2f", floatValue(metrics["success_rate_percent"])),
		fmt.Sprintf("- Command latency p95 ms: %.2f", floatValue(metrics["command_latency_p95_ms"])),
		fmt.Sprintf("- Command latency p99 ms: %.2f", floatValue(metrics["command_latency_p99_ms"])),
		"",
	}, "\n")
}

func resolveWorkspaceAndEnv(workspaceFlag, envRootFlag string) (string, string, error) {
	workspace := workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return "", "", err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, envRootFlag)
	if err != nil {
		return "", "", err
	}
	return workspace, envRoot, nil
}

func readHostLines(path string) ([]string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	hosts := []string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		hosts = append(hosts, line)
	}
	return hosts, nil
}

func readJSONFile(path string, out any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func stringFromAny(value any) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(value))
}

func durationStringSeconds(value string) (int, error) {
	d, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("invalid duration %q: %w", value, err)
	}
	if d <= 0 {
		return 0, errors.New("duration must be positive")
	}
	return int(math.Ceil(d.Seconds())), nil
}

func shellQuoteArgs(args []string) string {
	return strings.Join(shellQuoteArgsList(args), " ")
}

func planSelfCommand(command string, args ...string) []string {
	out := []string{"go", "run", "./scripts/go/rtk-cloud", "--", command}
	return append(out, args...)
}

func shellQuoteArgsList(args []string) []string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, shellQuoteArg(arg))
	}
	return parts
}

func shellQuoteArg(arg string) string {
	if arg == "" {
		return "''"
	}
	if strings.ContainsAny(arg, " \t\n'\"$`\\") {
		return "'" + strings.ReplaceAll(arg, "'", "'\\''") + "'"
	}
	return arg
}

func intFromAny(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case float64:
		return int(v)
	case json.Number:
		i, _ := v.Int64()
		return int(i)
	default:
		return 0
	}
}

func floatValue(value any) float64 {
	f, _ := floatFromAny(value)
	return f
}

func floatFromAny(value any) (float64, bool) {
	switch v := value.(type) {
	case float64:
		return v, true
	case int:
		return float64(v), true
	case json.Number:
		f, err := v.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func mqttLoadPercentile(values []float64, pct float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	index := int(math.Ceil((pct/100)*float64(len(sorted)))) - 1
	if index < 0 {
		index = 0
	}
	if index >= len(sorted) {
		index = len(sorted) - 1
	}
	return sorted[index]
}
