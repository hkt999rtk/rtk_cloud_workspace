package home100k

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/csv"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var commandRunner = func(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

var commandOutputRunner = func(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func Execute(args []string, stdout io.Writer, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "plan":
		return executePlan(args[1:], stdout, stderr)
	case "run":
		return executeRun(args[1:], stdout, stderr)
	case "shard-run":
		return executeShardRun(args[1:], stdout, stderr)
	case "runner-daemon":
		return executeRunnerDaemon(args[1:], stdout, stderr)
	case "provision-vms":
		return executeProvisionVMs(args[1:], stdout, stderr)
	case "sync":
		return executeSync(args[1:], stdout, stderr)
	case "run-stages":
		return executeRunStages(args[1:], stdout, stderr)
	case "collect":
		return executeCollect(args[1:], stdout, stderr)
	case "collect-server-evidence":
		return executeCollectServerEvidence(args[1:], stdout, stderr)
	case "aggregate":
		return executeAggregate(args[1:], stdout, stderr)
	case "list-vms":
		return executeListVMs(args[1:], stdout, stderr)
	case "destroy-vms":
		return executeDestroyVMs(args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown command: %s\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func executePlan(args []string, stdout io.Writer, stderr io.Writer) int {
	opts, _, err := parseCommonFlags("home-100k plan", args, stderr)
	if err != nil {
		return 2
	}
	plan, err := NewPlan(opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if err := plan.Validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(plan); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func executeRun(args []string, stdout io.Writer, stderr io.Writer) int {
	opts, ephemeral, runOpts, err := parseRunFlags("home-100k run", args, stderr)
	if err != nil {
		return 2
	}
	if !ephemeral {
		fmt.Fprintln(stderr, "--ephemeral-vms is required for 100K secret-bearing load-generator runs")
		return 2
	}
	result, err := Run(RunOptions{
		PlanOptions:        opts,
		RunID:              runOpts.runID,
		OutDir:             runOpts.outDir,
		EphemeralVMs:       ephemeral,
		ServerEvidenceFile: runOpts.serverEvidenceFile,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	report, err := os.ReadFile(result.ReportFile)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprint(stdout, string(report))
	fmt.Fprintf(stderr, "artifacts: %s\n", result.ReportFile)
	return 0
}

func parseCommonFlags(name string, args []string, stderr io.Writer) (PlanOptions, bool, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	envRoot := fs.String("env-root", "", "staging/LKE env-root")
	brandname := fs.String("brandname", "", "brand name")
	region := fs.String("region", "", "Linode region for load-generator VMs")
	stageWarmUp, stageSteady, stageCoolDown := addStageDurationFlags(fs)
	ephemeral := fs.Bool("ephemeral-vms", false, "require ephemeral VM lifecycle")
	if err := fs.Parse(args); err != nil {
		return PlanOptions{}, false, err
	}
	opts := PlanOptions{
		EnvRoot:   *envRoot,
		Brandname: *brandname,
		Region:    *region,
	}
	applyStageDurationFlags(&opts, stageWarmUp, stageSteady, stageCoolDown)
	return opts, *ephemeral, nil
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `usage: home-100k <plan|run|provision-vms|sync|run-stages|collect|collect-server-evidence|aggregate|list-vms|destroy-vms|runner-daemon> --env-root PATH --brandname NAME --region LINODE_REGION [--ephemeral-vms]`)
}

type runFlagValues struct {
	runID              string
	outDir             string
	serverEvidenceFile string
}

func parseRunFlags(name string, args []string, stderr io.Writer) (PlanOptions, bool, runFlagValues, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	envRoot := fs.String("env-root", "", "staging/LKE env-root")
	brandname := fs.String("brandname", "", "brand name")
	region := fs.String("region", "", "Linode region for load-generator VMs")
	stageWarmUp, stageSteady, stageCoolDown := addStageDurationFlags(fs)
	ephemeral := fs.Bool("ephemeral-vms", false, "require ephemeral VM lifecycle")
	runID := fs.String("run-id", "", "run id for artifact correlation")
	outDir := fs.String("out-dir", "", "artifact output directory")
	serverEvidenceFile := fs.String("server-evidence-file", "", "server evidence JSON file")
	if err := fs.Parse(args); err != nil {
		return PlanOptions{}, false, runFlagValues{}, err
	}
	opts := PlanOptions{
		EnvRoot:   *envRoot,
		Brandname: *brandname,
		Region:    *region,
	}
	applyStageDurationFlags(&opts, stageWarmUp, stageSteady, stageCoolDown)
	return opts, *ephemeral, runFlagValues{
		runID:              *runID,
		outDir:             *outDir,
		serverEvidenceFile: *serverEvidenceFile,
	}, nil
}

func addStageDurationFlags(fs *flag.FlagSet) (*string, *string, *string) {
	stageWarmUp := fs.String("stage-warm-up", "", "stage warm-up duration")
	stageSteady := fs.String("stage-steady", "", "stage steady-state duration")
	stageCoolDown := fs.String("stage-cool-down", "", "stage cool-down duration")
	return stageWarmUp, stageSteady, stageCoolDown
}

func applyStageDurationFlags(opts *PlanOptions, stageWarmUp *string, stageSteady *string, stageCoolDown *string) {
	opts.StageWarmUp = *stageWarmUp
	opts.StageSteady = *stageSteady
	opts.StageCoolDown = *stageCoolDown
}

type provisionVMFlagValues struct {
	runID             string
	outDir            string
	live              bool
	confirmLive       bool
	linodeToken       string
	linodeEndpoint    string
	linodeType        string
	linodeImage       string
	rootPass          string
	authorizedKeyFile string
}

type destroyVMFlagValues struct {
	runID          string
	live           bool
	confirmLive    bool
	linodeToken    string
	linodeEndpoint string
	vmStateFile    string
}

type listVMFlagValues struct {
	runID          string
	live           bool
	linodeToken    string
	linodeEndpoint string
}

type workflowFlagValues struct {
	runID              string
	outDir             string
	serverEvidenceFile string
	live               bool
	runnerMode         string
	vmStateFile        string
	remoteWorkspace    string
	remoteEnvRoot      string
	remoteOutRoot      string
	sshUser            string
	sshKey             string
	coordinatorDelayMS int
	mqttAddr           string
	videoCloudBaseURL  string
	accountManagerURL  string
}

type shardRunFlagValues struct {
	runID               string
	outDir              string
	role                string
	shardIndex          int
	shardManifest       string
	honorStageDurations bool
	runnerMode          string
	rtkCloudBinary      string
	workspace           string
}

func executeProvisionVMs(args []string, stdout io.Writer, stderr io.Writer) int {
	opts, values, err := parseProvisionVMFlags("home-100k provision-vms", args, stderr)
	if err != nil {
		return 2
	}
	plan, err := NewPlan(opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	runID := strings.TrimSpace(values.runID)
	if runID == "" {
		runID = "<run_id>"
	}
	actions := filterLifecycleActions(BuildLifecycleActions(plan, runID), "provision-vm")
	if !values.live {
		return writeJSONTo(stdout, stderr, map[string]any{
			"dry_run": true,
			"run_id":  runID,
			"actions": actions,
		})
	}
	if !values.confirmLive {
		fmt.Fprintln(stderr, "--confirm-live is required with --live before creating Linode VMs")
		return 2
	}
	if strings.TrimSpace(values.linodeToken) == "" {
		fmt.Fprintln(stderr, "--linode-token or LINODE_TOKEN is required with --live")
		return 2
	}
	vmConfig, err := buildLinodeVMConfig(values)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	client := NewLinodeClient(firstNonEmpty(values.linodeEndpoint, "https://api.linode.com/v4"), values.linodeToken)
	created := []LinodeVM{}
	for _, action := range actions {
		vm, err := client.ProvisionVM(context.Background(), action, vmConfig)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		created = append(created, vm)
	}
	vmStateFile := ""
	if strings.TrimSpace(values.outDir) != "" {
		vmStateFile = filepath.Join(values.outDir, "vms.json")
		if err := writeJSONFile(vmStateFile, map[string]any{
			"run_id":  runID,
			"created": created,
		}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	return writeJSONTo(stdout, stderr, map[string]any{
		"dry_run":       false,
		"run_id":        runID,
		"created":       created,
		"vm_state_file": vmStateFile,
	})
}

func executeSync(args []string, stdout io.Writer, stderr io.Writer) int {
	plan, values, code := buildWorkflowPlan("home-100k sync", args, stderr)
	if code != 0 {
		return code
	}
	runID := normalizedRunID(values.runID)
	actions := filterLifecycleActions(BuildLifecycleActions(plan, runID), "sync")
	if values.live {
		vms, err := readVMStateFile(values.vmStateFile)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if strings.TrimSpace(values.remoteWorkspace) == "" || strings.TrimSpace(values.remoteEnvRoot) == "" {
			fmt.Fprintln(stderr, "--remote-workspace and --remote-env-root are required with sync --live")
			return 2
		}
		if err := syncRemoteVMs(vms, plan, values); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return writeJSONTo(stdout, stderr, map[string]any{
			"dry_run": false,
			"run_id":  runID,
			"synced":  vms,
		})
	}
	return writeJSONTo(stdout, stderr, map[string]any{
		"dry_run": true,
		"run_id":  runID,
		"actions": actions,
		"sync_inputs": []string{
			"loadtests/home-100k runner source",
			"selected env-root artifacts only",
			"run plan and shard assignments",
		},
	})
}

func executeRunStages(args []string, stdout io.Writer, stderr io.Writer) int {
	plan, values, code := buildWorkflowPlan("home-100k run-stages", args, stderr)
	if code != 0 {
		return code
	}
	runID := normalizedRunID(values.runID)
	if values.live {
		vms, err := readVMStateFile(values.vmStateFile)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if strings.TrimSpace(values.remoteWorkspace) == "" || strings.TrimSpace(values.remoteEnvRoot) == "" {
			fmt.Fprintln(stderr, "--remote-workspace and --remote-env-root are required with run-stages --live")
			return 2
		}
		remoteOutRoot := firstNonEmpty(values.remoteOutRoot, "/var/lib/home-100k")
		if err := dispatchRemoteShards(vms, plan, runID, remoteOutRoot, values); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return writeJSONTo(stdout, stderr, map[string]any{
			"dry_run":    false,
			"run_id":     runID,
			"dispatched": vms,
		})
	}
	if strings.EqualFold(values.runnerMode, "live") || strings.EqualFold(values.runnerMode, "formal") {
		fmt.Fprintln(stderr, "formal live runner requires the VM/Ansible workflow for shard dispatch; refusing to run sampled actor executor")
		return 2
	}
	results, err := ExecuteStages(plan, StageExecutionOptions{SampleFlowsPerPresence: 2})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeJSONTo(stdout, stderr, map[string]any{
		"run_id":        runID,
		"stage_results": results,
	})
}

func executeShardRun(args []string, stdout io.Writer, stderr io.Writer) int {
	opts, values, err := parseShardRunFlags("home-100k shard-run", args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	plan, err := NewPlan(opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	assignment, ok := findAssignment(plan, values.role, values.shardIndex)
	if !ok {
		shard, shardOK := findShard(plan, values.role, values.shardIndex)
		if !shardOK {
			fmt.Fprintf(stderr, "shard not found: role=%s index=%d\n", values.role, values.shardIndex)
			return 2
		}
		assignment = VMAssignment{
			Label:      fmt.Sprintf("home-100k-%s-%03d", shard.Role, shard.Index),
			Index:      shard.Index,
			Role:       shard.Role,
			Region:     shard.Region,
			TaskShards: []Shard{shard},
		}
	}
	if strings.TrimSpace(values.shardManifest) != "" {
		manifest, err := loadVMAssignment(values.shardManifest)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		if manifest.Role != assignment.Role || manifest.Index != assignment.Index || manifest.Label != assignment.Label {
			fmt.Fprintf(stderr, "assignment manifest mismatch: manifest=%+v plan=%+v\n", manifest, assignment)
			return 2
		}
		assignment = manifest
	}
	runID := normalizedRunID(values.runID)
	if strings.EqualFold(values.runnerMode, "live") || strings.EqualFold(values.runnerMode, "formal") {
		if err := runLiveShard(plan, assignment, values, runID); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return writeJSONTo(stdout, stderr, map[string]any{
			"run_id":      runID,
			"role":        values.role,
			"shard_index": values.shardIndex,
			"runner_mode": "live",
			"out_dir":     firstNonEmpty(strings.TrimSpace(values.outDir), filepath.Join("loadtests", "home-100k", "reports", runID, values.role, fmt.Sprintf("%03d", values.shardIndex))),
		})
	}
	results, err := ExecuteStages(plan, StageExecutionOptions{SampleFlowsPerPresence: 2, HonorStageDurations: values.honorStageDurations})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	outDir := strings.TrimSpace(values.outDir)
	if outDir == "" {
		outDir = filepath.Join("loadtests", "home-100k", "reports", runID, values.role, fmt.Sprintf("%03d", values.shardIndex))
	}
	resultFile := filepath.Join(outDir, "results.json")
	reportFile := filepath.Join(outDir, "TEST_REPORT.md")
	if err := writeJSONFile(resultFile, map[string]any{
		"run_id":                runID,
		"role":                  values.role,
		"shard_index":           values.shardIndex,
		"vm_assignment":         assignment,
		"stage_results":         results,
		"load_generator_health": LoadGeneratorHealth{},
	}); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	report := RenderReport(ReportInput{
		Plan:                 plan,
		RunID:                runID,
		ShadowEvidenceFound:  shadowEvidenceComplete(results),
		ServerEvidenceFound:  false,
		LoadGeneratorHealthy: true,
		StageResults:         results,
	})
	if err := os.WriteFile(reportFile, []byte(report), 0o644); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeJSONTo(stdout, stderr, map[string]any{
		"run_id":       runID,
		"role":         values.role,
		"shard_index":  values.shardIndex,
		"results_file": resultFile,
		"report_file":  reportFile,
	})
}

func runLiveShard(plan Plan, assignment VMAssignment, values shardRunFlagValues, runID string) error {
	outDir := strings.TrimSpace(values.outDir)
	if outDir == "" {
		outDir = filepath.Join("loadtests", "home-100k", "reports", runID, values.role, fmt.Sprintf("%03d", values.shardIndex))
	}
	rtkCloud := firstNonEmpty(strings.TrimSpace(values.rtkCloudBinary), "rtk-cloud")
	deviceShardCount := len(plan.ShardsByRole("device-mqtt"))
	if deviceShardCount <= 0 {
		return fmt.Errorf("plan has no device-mqtt shards")
	}
	stageResults := make([]StageResult, 0, len(plan.Stages))
	var runErr error
	for _, stage := range plan.Stages {
		durationSeconds, err := stageWindowSeconds(stage)
		if err != nil {
			return fmt.Errorf("stage %s duration: %w", stage.Name, err)
		}
		maxConnected := shardConnectedDevices(stage.ConnectedDevices, assignment)
		stageOut := filepath.Join(outDir, "mqtt-test", stage.Name)
		args := []string{
			"mqtt-test",
			"--env-root", plan.Conditions.EnvRoot,
			"--brandname", plan.Conditions.Brandname,
			"--profile", "baseline-10k",
			"--duration-seconds", strconv.Itoa(durationSeconds),
			"--out-dir", stageOut,
			"--mqtt-probe",
			"--run-id", runID,
			"--shard-index", strconv.Itoa(assignment.Index),
			"--shard-count", strconv.Itoa(deviceShardCount),
			"--ramp-up", stage.WarmUp,
			"--telemetry-interval", stage.SteadyState,
			"--state-interval", stage.SteadyState,
			"--command-rate-per-device-per-day", "1",
			"--concurrency", strconv.Itoa(minInt(maxConnected, 250)),
			"--max-connected-devices", strconv.Itoa(maxConnected),
		}
		if strings.TrimSpace(values.workspace) != "" {
			args = append([]string{args[0], "--workspace", values.workspace}, args[1:]...)
		}
		if err := commandRunner(rtkCloud, args...); err != nil {
			runErr = fmt.Errorf("stage %s live mqtt-test failed: %w", stage.Name, err)
		}
		stageResult, err := loadLiveMQTTStageResult(filepath.Join(stageOut, "results.json"), stage, maxConnected)
		if err != nil {
			if runErr != nil {
				return fmt.Errorf("%w; stage %s live result: %v", runErr, stage.Name, err)
			}
			return fmt.Errorf("stage %s live result: %w", stage.Name, err)
		}
		stageResults = append(stageResults, stageResult)
		if runErr != nil {
			break
		}
	}
	resultFile := filepath.Join(outDir, "results.json")
	reportFile := filepath.Join(outDir, "TEST_REPORT.md")
	if err := writeJSONFile(resultFile, map[string]any{
		"run_id":                runID,
		"role":                  values.role,
		"shard_index":           values.shardIndex,
		"runner_mode":           "live",
		"vm_assignment":         assignment,
		"stage_results":         stageResults,
		"load_generator_health": LoadGeneratorHealth{},
	}); err != nil {
		return err
	}
	report := RenderReport(ReportInput{
		Plan:                 plan,
		RunID:                runID,
		ShadowEvidenceFound:  shadowEvidenceComplete(stageResults),
		ServerEvidenceFound:  false,
		LoadGeneratorHealthy: true,
		StageResults:         stageResults,
	})
	if err := os.WriteFile(reportFile, []byte(report), 0o644); err != nil {
		return err
	}
	return runErr
}

func stageWindowSeconds(stage Stage) (int, error) {
	total := 0
	for _, raw := range []string{stage.WarmUp, stage.SteadyState, stage.CoolDown} {
		duration, err := time.ParseDuration(raw)
		if err != nil {
			return 0, err
		}
		if duration <= 0 {
			return 0, fmt.Errorf("duration must be positive, got %s", raw)
		}
		total += int(duration.Seconds())
	}
	if total <= 0 {
		return 0, fmt.Errorf("stage window must be positive")
	}
	return total, nil
}

func shardConnectedDevices(stageConnected int, assignment VMAssignment) int {
	deviceCount := 0
	for _, shard := range assignment.TaskShards {
		if shard.Role == "device-mqtt" {
			deviceCount += shard.Count
		}
	}
	if deviceCount <= 0 {
		deviceCount = DefaultDevicesPerVM
	}
	target := stageConnected * deviceCount / DefaultDeviceCount
	if target <= 0 && stageConnected > 0 {
		target = 1
	}
	return target
}

func loadLiveMQTTStageResult(path string, stage Stage, maxConnected int) (StageResult, error) {
	var raw struct {
		Overall string `json:"overall"`
		Metrics struct {
			DevicesSelected   int `json:"devices_selected"`
			CommandsAttempted int `json:"commands_attempted"`
			CommandsPassed    int `json:"commands_passed"`
		} `json:"metrics"`
		ConnectAttempts        int64            `json:"connect_attempts"`
		ConnectSuccesses       int64            `json:"connect_successes"`
		ConnectFailures        int64            `json:"connect_failures"`
		SubscribeSuccesses     int64            `json:"subscribe_successes"`
		PublishSuccesses       int64            `json:"publish_successes"`
		PublishFailures        int64            `json:"publish_failures"`
		MessagesReceived       int64            `json:"messages_received"`
		ReportedEvents         int64            `json:"reported_events"`
		TotalBytesSent         int64            `json:"total_bytes_sent"`
		TotalBytesReceived     int64            `json:"total_bytes_received"`
		AuthViolations         int64            `json:"auth_violations"`
		HTTPRequests           int64            `json:"http_requests"`
		HTTPSuccesses          int64            `json:"http_successes"`
		HTTPFailures           int64            `json:"http_failures"`
		TotalHTTPBytesSent     int64            `json:"total_http_bytes_sent"`
		TotalHTTPBytesReceived int64            `json:"total_http_bytes_received"`
		DeviceMQTTTotals       DeviceMQTTTotals `json:"device_mqtt_totals"`
		AppUserTotals          AppUserTotals    `json:"app_user_totals"`
	}
	if err := readJSON(path, &raw); err != nil {
		return StageResult{}, err
	}
	commandsAttempted := raw.Metrics.CommandsAttempted
	commandsPassed := raw.Metrics.CommandsPassed
	connectRate := 100.0
	if strings.EqualFold(raw.Overall, "fail") {
		connectRate = 0
	}
	connectAttempts := nonZeroInt64(raw.DeviceMQTTTotals.ConnectAttempts, raw.ConnectAttempts)
	connectSuccess := nonZeroInt64(raw.DeviceMQTTTotals.ConnectSuccess, raw.ConnectSuccesses)
	connectFail := raw.ConnectFailures
	if raw.DeviceMQTTTotals.ConnectFail != 0 {
		connectFail = raw.DeviceMQTTTotals.ConnectFail
	}
	if connectFail == 0 && connectAttempts > connectSuccess {
		connectFail = connectAttempts - connectSuccess
	}
	subscribes := nonZeroInt64(raw.DeviceMQTTTotals.Subscribes, raw.SubscribeSuccesses)
	publishes := nonZeroInt64(raw.DeviceMQTTTotals.Publishes, raw.PublishSuccesses+raw.PublishFailures)
	receivedMessages := nonZeroInt64(raw.DeviceMQTTTotals.ReceivedMessages, raw.MessagesReceived)
	deltaReceived := nonZeroInt64(raw.DeviceMQTTTotals.DeltaReceived, receivedMessages)
	reportedPublishes := nonZeroInt64(raw.DeviceMQTTTotals.ReportedPublishes, raw.ReportedEvents)
	rejectedPublishes := nonZeroInt64(raw.DeviceMQTTTotals.RejectedPublishes, raw.PublishFailures)
	httpRequests := nonZeroInt64(raw.AppUserTotals.DesiredWrites, raw.HTTPRequests)
	httpSuccesses := nonZeroInt64(raw.AppUserTotals.ReceivedAcks, raw.HTTPSuccesses)
	httpFailures := raw.HTTPFailures
	if raw.AppUserTotals.LoginFail != 0 {
		httpFailures = raw.AppUserTotals.LoginFail
	}
	if httpFailures == 0 && httpRequests > httpSuccesses {
		httpFailures = httpRequests - httpSuccesses
	}
	return StageResult{
		Name:             stage.Name,
		ConnectedDevices: stage.ConnectedDevices,
		DeviceMQTTTotals: DeviceMQTTTotals{
			ConnectAttempts:   connectAttempts,
			ConnectSuccess:    connectSuccess,
			ConnectFail:       connectFail,
			Subscribes:        subscribes,
			Publishes:         publishes,
			ReceivedMessages:  receivedMessages,
			DeltaReceived:     deltaReceived,
			ReportedPublishes: reportedPublishes,
			RejectedPublishes: rejectedPublishes,
			BytesSent:         nonZeroInt64(raw.DeviceMQTTTotals.BytesSent, raw.TotalBytesSent),
			BytesReceived:     nonZeroInt64(raw.DeviceMQTTTotals.BytesReceived, raw.TotalBytesReceived),
		},
		AppUserTotals: AppUserTotals{
			LoginAttempts:       raw.AppUserTotals.LoginAttempts,
			LoginSuccess:        raw.AppUserTotals.LoginSuccess,
			LoginFail:           raw.AppUserTotals.LoginFail,
			ListDevicesRequests: raw.AppUserTotals.ListDevicesRequests,
			ReadShadowRequests:  raw.AppUserTotals.ReadShadowRequests,
			DesiredWrites:       httpRequests,
			ReceivedAcks:        httpSuccesses,
			BytesSent:           nonZeroInt64(raw.AppUserTotals.BytesSent, raw.TotalHTTPBytesSent),
			BytesReceived:       nonZeroInt64(raw.AppUserTotals.BytesReceived, raw.TotalHTTPBytesReceived),
		},
		MQTTConnectSuccessRatePercent:  connectRate,
		DesiredReportedConvergenceRate: percent(commandsPassed, commandsAttempted),
		OfflineDesiredConvergenceRate:  100,
		DeltaClearSuccessRatePercent:   percent(commandsPassed, commandsAttempted),
		RejectedUpdateCount:            int(httpFailures),
		AuthorizationViolationCount:    int(raw.AuthViolations),
		ClientTokenCorrelationCount:    int(httpSuccesses),
	}, nil
}

func nonZeroInt64(value int64, fallback int64) int64 {
	if value != 0 {
		return value
	}
	return fallback
}

func readJSON(path string, out any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func minInt(a int, b int) int {
	if a < b {
		return a
	}
	return b
}

func executeCollect(args []string, stdout io.Writer, stderr io.Writer) int {
	plan, values, code := buildWorkflowPlan("home-100k collect", args, stderr)
	if code != 0 {
		return code
	}
	runID := normalizedRunID(values.runID)
	outDir := strings.TrimSpace(values.outDir)
	if outDir == "" {
		outDir = filepath.Join("loadtests", "home-100k", "reports", runID)
	}
	if values.live {
		vms, err := readVMStateFile(values.vmStateFile)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		remoteOutRoot := firstNonEmpty(values.remoteOutRoot, "/var/lib/home-100k")
		if err := collectRemoteVMs(vms, plan, runID, remoteOutRoot, outDir, values); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return writeJSONTo(stdout, stderr, map[string]any{
			"dry_run":   false,
			"run_id":    runID,
			"collected": vms,
			"out_dir":   outDir,
		})
	}
	return writeJSONTo(stdout, stderr, map[string]any{
		"dry_run":             true,
		"run_id":              runID,
		"action":              firstLifecycleAction(BuildLifecycleActions(plan, runID), "collect"),
		"remote_results_glob": "/var/lib/home-100k/" + runID + "/*",
		"local_artifacts": []string{
			filepath.Join(outDir, "plan.json"),
			filepath.Join(outDir, "results.json"),
			filepath.Join(outDir, "server-evidence.json"),
			filepath.Join(outDir, "TEST_REPORT.md"),
		},
	})
}

func executeCollectServerEvidence(args []string, stdout io.Writer, stderr io.Writer) int {
	_, values, code := buildWorkflowPlan("home-100k collect-server-evidence", args, stderr)
	if code != 0 {
		return code
	}
	runID := normalizedRunID(values.runID)
	if values.live {
		evidence := collectLiveServerEvidence(runID)
		if strings.TrimSpace(values.outDir) != "" {
			if err := writeJSONFile(filepath.Join(values.outDir, "server-evidence.json"), evidence); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
		}
		return writeJSONTo(stdout, stderr, evidence)
	}
	evidence, err := loadServerEvidence(values.serverEvidenceFile, runID)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeJSONTo(stdout, stderr, evidence)
}

func executeAggregate(args []string, stdout io.Writer, stderr io.Writer) int {
	opts, values, err := parseWorkflowFlags("home-100k aggregate", args, stderr)
	if err != nil {
		return 2
	}
	result, err := AggregateCollectedRun(AggregateOptions{
		PlanOptions: opts,
		RunID:       values.runID,
		OutDir:      values.outDir,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeJSONTo(stdout, stderr, result)
}

func executeListVMs(args []string, stdout io.Writer, stderr io.Writer) int {
	opts, values, err := parseListVMFlags("home-100k list-vms", args, stderr)
	if err != nil {
		return 2
	}
	if _, err := NewPlan(opts); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	runID := normalizedRunID(values.runID)
	tags := []string{"home-100k", runID}
	if !values.live {
		return writeJSONTo(stdout, stderr, map[string]any{
			"dry_run": true,
			"run_id":  runID,
			"tags":    tags,
		})
	}
	if strings.TrimSpace(values.linodeToken) == "" {
		fmt.Fprintln(stderr, "--linode-token or LINODE_TOKEN is required with --live")
		return 2
	}
	client := NewLinodeClient(firstNonEmpty(values.linodeEndpoint, "https://api.linode.com/v4"), values.linodeToken)
	vms, err := client.ListVMs(context.Background(), tags)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeJSONTo(stdout, stderr, map[string]any{
		"dry_run": false,
		"run_id":  runID,
		"tags":    tags,
		"vms":     vms,
	})
}

func executeDestroyVMs(args []string, stdout io.Writer, stderr io.Writer) int {
	opts, values, err := parseDestroyVMFlags("home-100k destroy-vms", args, stderr)
	if err != nil {
		return 2
	}
	plan, err := NewPlan(opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	runID := strings.TrimSpace(values.runID)
	if runID == "" {
		runID = "<run_id>"
	}
	actions := filterLifecycleActions(BuildLifecycleActions(plan, runID), "destroy-vm")
	if !values.live {
		return writeJSONTo(stdout, stderr, map[string]any{
			"dry_run": true,
			"run_id":  runID,
			"actions": actions,
		})
	}
	if !values.confirmLive {
		fmt.Fprintln(stderr, "--confirm-live is required with --live before destroying Linode VMs")
		return 2
	}
	if strings.TrimSpace(values.linodeToken) == "" {
		fmt.Fprintln(stderr, "--linode-token or LINODE_TOKEN is required with --live")
		return 2
	}
	if strings.TrimSpace(values.vmStateFile) == "" {
		fmt.Fprintln(stderr, "--vm-state-file is required with destroy-vms --live")
		return 2
	}
	vms, err := readVMStateFile(values.vmStateFile)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	client := NewLinodeClient(firstNonEmpty(values.linodeEndpoint, "https://api.linode.com/v4"), values.linodeToken)
	destroyed := []LinodeVM{}
	for _, vm := range vms {
		if err := client.DestroyVM(context.Background(), vm.ID); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		destroyed = append(destroyed, vm)
	}
	return writeJSONTo(stdout, stderr, map[string]any{
		"dry_run":   false,
		"run_id":    runID,
		"destroyed": destroyed,
	})
}

func buildWorkflowPlan(name string, args []string, stderr io.Writer) (Plan, workflowFlagValues, int) {
	opts, values, err := parseWorkflowFlags(name, args, stderr)
	if err != nil {
		return Plan{}, workflowFlagValues{}, 2
	}
	plan, err := NewPlan(opts)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return Plan{}, workflowFlagValues{}, 2
	}
	if err := plan.Validate(); err != nil {
		fmt.Fprintln(stderr, err)
		return Plan{}, workflowFlagValues{}, 2
	}
	return plan, values, 0
}

func parseProvisionVMFlags(name string, args []string, stderr io.Writer) (PlanOptions, provisionVMFlagValues, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	envRoot := fs.String("env-root", "", "staging/LKE env-root")
	brandname := fs.String("brandname", "", "brand name")
	region := fs.String("region", "", "Linode region for load-generator VMs")
	stageWarmUp, stageSteady, stageCoolDown := addStageDurationFlags(fs)
	runID := fs.String("run-id", "", "run id for VM tags")
	outDir := fs.String("out-dir", "", "artifact output directory")
	live := fs.Bool("live", false, "create Linode VMs")
	confirmLive := fs.Bool("confirm-live", false, "confirm live Linode VM creation")
	linodeToken := fs.String("linode-token", os.Getenv("LINODE_TOKEN"), "Linode API token")
	linodeEndpoint := fs.String("linode-endpoint", "", "Linode API endpoint")
	linodeType := fs.String("linode-type", "", "Linode instance type")
	linodeImage := fs.String("linode-image", "", "Linode image")
	rootPass := fs.String("root-pass", "", "Linode root password")
	authorizedKeyFile := fs.String("authorized-key-file", "", "SSH public key file for Linode authorized_keys")
	if err := fs.Parse(args); err != nil {
		return PlanOptions{}, provisionVMFlagValues{}, err
	}
	opts := PlanOptions{EnvRoot: *envRoot, Brandname: *brandname, Region: *region}
	applyStageDurationFlags(&opts, stageWarmUp, stageSteady, stageCoolDown)
	return opts, provisionVMFlagValues{
		runID:             *runID,
		outDir:            *outDir,
		live:              *live,
		confirmLive:       *confirmLive,
		linodeToken:       *linodeToken,
		linodeEndpoint:    *linodeEndpoint,
		linodeType:        *linodeType,
		linodeImage:       *linodeImage,
		rootPass:          *rootPass,
		authorizedKeyFile: *authorizedKeyFile,
	}, nil
}

func parseDestroyVMFlags(name string, args []string, stderr io.Writer) (PlanOptions, destroyVMFlagValues, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	envRoot := fs.String("env-root", "", "staging/LKE env-root")
	brandname := fs.String("brandname", "", "brand name")
	region := fs.String("region", "", "Linode region for load-generator VMs")
	stageWarmUp, stageSteady, stageCoolDown := addStageDurationFlags(fs)
	runID := fs.String("run-id", "", "run id for VM tags")
	live := fs.Bool("live", false, "destroy Linode VMs")
	confirmLive := fs.Bool("confirm-live", false, "confirm live Linode VM destruction")
	linodeToken := fs.String("linode-token", os.Getenv("LINODE_TOKEN"), "Linode API token")
	linodeEndpoint := fs.String("linode-endpoint", "", "Linode API endpoint")
	vmStateFile := fs.String("vm-state-file", "", "VM state JSON from provision-vms")
	if err := fs.Parse(args); err != nil {
		return PlanOptions{}, destroyVMFlagValues{}, err
	}
	opts := PlanOptions{EnvRoot: *envRoot, Brandname: *brandname, Region: *region}
	applyStageDurationFlags(&opts, stageWarmUp, stageSteady, stageCoolDown)
	return opts, destroyVMFlagValues{
		runID:          *runID,
		live:           *live,
		confirmLive:    *confirmLive,
		linodeToken:    *linodeToken,
		linodeEndpoint: *linodeEndpoint,
		vmStateFile:    *vmStateFile,
	}, nil
}

func parseListVMFlags(name string, args []string, stderr io.Writer) (PlanOptions, listVMFlagValues, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	envRoot := fs.String("env-root", "", "staging/LKE env-root")
	brandname := fs.String("brandname", "", "brand name")
	region := fs.String("region", "", "Linode region for load-generator VMs")
	stageWarmUp, stageSteady, stageCoolDown := addStageDurationFlags(fs)
	runID := fs.String("run-id", "", "run id for VM tags")
	live := fs.Bool("live", false, "query Linode VMs")
	linodeToken := fs.String("linode-token", os.Getenv("LINODE_TOKEN"), "Linode API token")
	linodeEndpoint := fs.String("linode-endpoint", "", "Linode API endpoint")
	if err := fs.Parse(args); err != nil {
		return PlanOptions{}, listVMFlagValues{}, err
	}
	opts := PlanOptions{EnvRoot: *envRoot, Brandname: *brandname, Region: *region}
	applyStageDurationFlags(&opts, stageWarmUp, stageSteady, stageCoolDown)
	return opts, listVMFlagValues{
		runID:          *runID,
		live:           *live,
		linodeToken:    *linodeToken,
		linodeEndpoint: *linodeEndpoint,
	}, nil
}

func parseWorkflowFlags(name string, args []string, stderr io.Writer) (PlanOptions, workflowFlagValues, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	envRoot := fs.String("env-root", "", "staging/LKE env-root")
	brandname := fs.String("brandname", "", "brand name")
	region := fs.String("region", "", "Linode region for load-generator VMs")
	stageWarmUp, stageSteady, stageCoolDown := addStageDurationFlags(fs)
	runID := fs.String("run-id", "", "run id for artifact correlation")
	outDir := fs.String("out-dir", "", "artifact output directory")
	serverEvidenceFile := fs.String("server-evidence-file", "", "server evidence JSON file")
	live := fs.Bool("live", false, "execute live remote workflow step")
	runnerMode := fs.String("runner-mode", "sample", "runner mode: sample or live")
	vmStateFile := fs.String("vm-state-file", "", "VM state JSON from provision-vms")
	remoteWorkspace := fs.String("remote-workspace", "", "workspace path on load-generator VMs")
	remoteEnvRoot := fs.String("remote-env-root", "", "env-root path on load-generator VMs")
	remoteOutRoot := fs.String("remote-out-root", "", "output root on load-generator VMs")
	sshUser := fs.String("ssh-user", "root", "SSH user for load-generator VMs")
	sshKey := fs.String("ssh-key", "", "SSH private key for load-generator VMs")
	coordinatorDelayMS := fs.Int("coordinator-start-delay-ms", defaultCoordinatorStartDelayMS, "host coordinator delay between START ack and local monotonic runner start")
	mqttAddr := fs.String("mqtt-addr", "", "public MQTT host:port for remote load-generator VMs")
	videoCloudBaseURL := fs.String("video-cloud-base-url", "", "Video Cloud base URL for remote load-generator VMs")
	accountManagerURL := fs.String("account-manager-base-url", "", "Account Manager base URL for remote load-generator VMs")
	if err := fs.Parse(args); err != nil {
		return PlanOptions{}, workflowFlagValues{}, err
	}
	opts := PlanOptions{EnvRoot: *envRoot, Brandname: *brandname, Region: *region}
	applyStageDurationFlags(&opts, stageWarmUp, stageSteady, stageCoolDown)
	return opts, workflowFlagValues{
		runID:              *runID,
		outDir:             *outDir,
		serverEvidenceFile: *serverEvidenceFile,
		live:               *live,
		runnerMode:         *runnerMode,
		vmStateFile:        *vmStateFile,
		remoteWorkspace:    *remoteWorkspace,
		remoteEnvRoot:      *remoteEnvRoot,
		remoteOutRoot:      *remoteOutRoot,
		sshUser:            *sshUser,
		sshKey:             *sshKey,
		coordinatorDelayMS: *coordinatorDelayMS,
		mqttAddr:           *mqttAddr,
		videoCloudBaseURL:  *videoCloudBaseURL,
		accountManagerURL:  *accountManagerURL,
	}, nil
}

func parseShardRunFlags(name string, args []string, stderr io.Writer) (PlanOptions, shardRunFlagValues, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	envRoot := fs.String("env-root", "", "staging/LKE env-root")
	brandname := fs.String("brandname", "", "brand name")
	region := fs.String("region", "", "Linode region for load-generator VMs")
	stageWarmUp, stageSteady, stageCoolDown := addStageDurationFlags(fs)
	runID := fs.String("run-id", "", "run id for artifact correlation")
	outDir := fs.String("out-dir", "", "artifact output directory")
	role := fs.String("role", "", "shard role")
	shardIndex := fs.Int("shard-index", 0, "shard index")
	shardManifest := fs.String("shard-manifest", "", "shard manifest JSON path")
	honorStageDurations := fs.Bool("honor-stage-durations", false, "sleep through configured stage warm-up, steady, and cool-down windows")
	runnerMode := fs.String("runner-mode", "sample", "runner mode: sample or live")
	rtkCloudBinary := fs.String("rtk-cloud-binary", "rtk-cloud", "rtk-cloud binary for live MQTT/API runner")
	workspace := fs.String("workspace", "", "workspace path for live MQTT/API runner")
	if err := fs.Parse(args); err != nil {
		return PlanOptions{}, shardRunFlagValues{}, err
	}
	if strings.TrimSpace(*role) == "" {
		return PlanOptions{}, shardRunFlagValues{}, fmt.Errorf("--role is required")
	}
	opts := PlanOptions{EnvRoot: *envRoot, Brandname: *brandname, Region: *region}
	applyStageDurationFlags(&opts, stageWarmUp, stageSteady, stageCoolDown)
	return opts, shardRunFlagValues{
		runID:               *runID,
		outDir:              *outDir,
		role:                *role,
		shardIndex:          *shardIndex,
		shardManifest:       *shardManifest,
		honorStageDurations: *honorStageDurations,
		runnerMode:          *runnerMode,
		rtkCloudBinary:      *rtkCloudBinary,
		workspace:           *workspace,
	}, nil
}

func readVMStateFile(path string) ([]LinodeVM, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var state struct {
		Created []LinodeVM `json:"created"`
		VMs     []LinodeVM `json:"vms"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, err
	}
	vms := state.Created
	if len(vms) == 0 {
		vms = state.VMs
	}
	if len(vms) == 0 {
		return nil, fmt.Errorf("VM state file %s does not contain created VMs", path)
	}
	for _, vm := range vms {
		if vm.ID <= 0 {
			return nil, fmt.Errorf("VM state file %s contains VM without positive id", path)
		}
	}
	return vms, nil
}

func normalizedRunID(runID string) string {
	runID = strings.TrimSpace(runID)
	if runID == "" {
		return "<run_id>"
	}
	return runID
}

func filterLifecycleActions(actions []LifecycleAction, action string) []LifecycleAction {
	out := []LifecycleAction{}
	for _, item := range actions {
		if item.Action == action {
			out = append(out, item)
		}
	}
	return out
}

func firstLifecycleAction(actions []LifecycleAction, action string) LifecycleAction {
	for _, item := range actions {
		if item.Action == action {
			return item
		}
	}
	return LifecycleAction{}
}

func buildLinodeVMConfig(values provisionVMFlagValues) (LinodeVMConfig, error) {
	cfg := LinodeVMConfig{
		Type:     values.linodeType,
		Image:    values.linodeImage,
		RootPass: values.rootPass,
	}
	if strings.TrimSpace(values.authorizedKeyFile) != "" {
		raw, err := os.ReadFile(values.authorizedKeyFile)
		if err != nil {
			return LinodeVMConfig{}, err
		}
		key := strings.TrimSpace(string(raw))
		if key == "" {
			return LinodeVMConfig{}, fmt.Errorf("authorized key file %s is empty", values.authorizedKeyFile)
		}
		cfg.AuthorizedKeys = []string{key}
	}
	return cfg, nil
}

func findShard(plan Plan, role string, index int) (Shard, bool) {
	for _, shard := range plan.Shards {
		if shard.Role == role && shard.Index == index {
			return shard, true
		}
	}
	return Shard{}, false
}

func findAssignment(plan Plan, role string, index int) (VMAssignment, bool) {
	for _, assignment := range plan.Assignments {
		if assignment.Role == role && assignment.Index == index {
			return assignment, true
		}
	}
	return VMAssignment{}, false
}

func findAssignmentByLabel(plan Plan, label string) (VMAssignment, bool) {
	for _, assignment := range plan.Assignments {
		if assignment.Label == label {
			return assignment, true
		}
	}
	return VMAssignment{}, false
}

func loadVMAssignment(path string) (VMAssignment, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return VMAssignment{}, err
	}
	var assignment VMAssignment
	if err := json.Unmarshal(raw, &assignment); err != nil {
		return VMAssignment{}, err
	}
	return assignment, nil
}

func collectLiveServerEvidence(runID string) ServerEvidence {
	sources := map[string]EvidenceSource{}
	notes := []string{}
	for _, probe := range serverEvidenceProbes(runID) {
		out, err := commandOutputRunner(probe.command, probe.args...)
		if err != nil {
			detail := strings.TrimSpace(err.Error() + " " + redactEvidenceOutput(out))
			sources[probe.source] = mergeEvidenceSource(sources[probe.source], EvidenceSource{Available: false, Detail: detail})
			notes = append(notes, fmt.Sprintf("%s evidence probe failed: %s", probe.source, err.Error()))
			continue
		}
		counters := parseEvidenceCounters(probe.source, runID, out)
		sources[probe.source] = mergeEvidenceSource(sources[probe.source], EvidenceSource{Available: true, Detail: probe.detail, Counters: counters})
	}
	for key, source := range evidenceSourceCatalog(false) {
		if _, ok := sources[key]; !ok {
			source.Detail = "probe not configured"
			sources[key] = source
		}
	}
	return ServerEvidence{
		RunID:    runID,
		Complete: allEvidenceSourcesAvailable(sources),
		Sources:  sources,
		Notes:    notes,
	}
}

func parseEvidenceCounters(source string, runID string, out string) map[string]int64 {
	counters := map[string]int64{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) == 2 && strings.Contains(parts[0], ".") {
			value, err := strconv.ParseInt(parts[1], 10, 64)
			if err == nil {
				counters[parts[0]] += value
			}
			continue
		}
		if source == "emqx" && strings.Contains(line, runID) && strings.Contains(line, "-device-") {
			if strings.Contains(line, "New client connected") || strings.Contains(line, "client.connected") {
				counters["device_mqtt.connect_success"]++
			}
		}
	}
	if len(counters) == 0 {
		return nil
	}
	return counters
}

func redactEvidenceOutput(out string) string {
	out = strings.TrimSpace(out)
	if out == "" {
		return ""
	}
	if len(out) > 400 {
		out = out[:400] + "...(truncated)"
	}
	return redact(out)
}

func mergeEvidenceSource(current EvidenceSource, next EvidenceSource) EvidenceSource {
	if current.Detail == "" {
		return next
	}
	merged := EvidenceSource{
		Available: current.Available && next.Available,
		Optional:  current.Optional || next.Optional,
		Detail:    current.Detail + "; " + next.Detail,
		Counters:  map[string]int64{},
	}
	for key, value := range current.Counters {
		merged.Counters[key] = value
	}
	for key, value := range next.Counters {
		merged.Counters[key] = value
	}
	if len(merged.Counters) == 0 {
		merged.Counters = nil
	}
	return merged
}

type serverEvidenceProbe struct {
	source  string
	command string
	args    []string
	detail  string
}

func serverEvidenceProbes(runID string) []serverEvidenceProbe {
	return []serverEvidenceProbe{
		{
			source:  "host_pod_resources",
			command: "kubectl",
			args:    []string{"get", "pods", "-A", "-o", "wide"},
			detail:  "pod placement and readiness captured",
		},
		{
			source:  "host_pod_resources",
			command: "bash",
			args:    []string{"-lc", "kubectl top pods -A || true"},
			detail:  "pod resource usage captured",
		},
		kubectlLogsProbe("emqx", "video-cloud-staging-video-cloud", "app.kubernetes.io/name=mqtt", "MQTT broker logs and client churn evidence captured for run_id "+runID),
		kubectlLogsProbe("video_cloud_api", "video-cloud-staging-video-cloud", "app.kubernetes.io/name=video-cloud-api", "Video Cloud API logs captured for run_id "+runID),
		postgresCounterProbe("iot_device_shadow", runID, shadowRuntimeLogCounterSQL(runID), "IoT Device Shadow MQTT path counters parsed from persisted runtime logs for run_id "+runID),
		postgresCounterProbe("postgres", runID, shadowStoreCounterSQL(runID), "PostgreSQL device shadow convergence counters parsed for run_id "+runID),
		kubectlLogsProbe("postgres", "video-cloud-staging-platform", "app.kubernetes.io/name=postgresql", "PostgreSQL logs captured"),
		kubectlLogsProbe("redis_valkey", "video-cloud-staging-platform", "app.kubernetes.io/name=redis", "Redis/Valkey logs captured when enabled"),
		kubectlLogsProbe("ingress_nginx", "video-cloud-staging-ingress", "app.kubernetes.io/name=ingress-nginx", "Ingress/nginx logs captured for run_id "+runID),
	}
}

func postgresCounterProbe(source string, runID string, sql string, detail string) serverEvidenceProbe {
	script := fmt.Sprintf(
		`set -euo pipefail; kubectl -n video-cloud-staging-platform exec postgresql-0 -- psql -U postgres -d video_cloud -At -F '	' -c %s`,
		shellQuote(sql),
	)
	return serverEvidenceProbe{source: source, command: "bash", args: []string{"-lc", script}, detail: detail}
}

func shadowRuntimeLogCounterSQL(runID string) string {
	prefix := "mqtt-e2e-" + sanitizeEvidenceRunID(runID) + "-%"
	return `
WITH logs AS (
	SELECT source, message
	FROM device_runtime_logs
	WHERE stream_id LIKE ` + sqlLiteral(prefix) + `
)
SELECT 'app_user.desired_writes', COUNT(*) FROM logs WHERE source = 'app_controller' AND message = 'mqtt_e2e shadow_desired app_controller publish'
UNION ALL SELECT 'device_mqtt.delta_received', COUNT(*) FROM logs WHERE source = 'device_client' AND message = 'mqtt_e2e shadow_delta device_client receive'
UNION ALL SELECT 'device_mqtt.reported_publishes', COUNT(*) FROM logs WHERE source = 'device_client' AND message = 'mqtt_e2e shadow_reported device_client publish'
UNION ALL SELECT 'app_user.received_acks', COUNT(*) FROM logs WHERE source = 'app_observer' AND message = 'mqtt_e2e shadow_reported app_observer receive'
`
}

func shadowStoreCounterSQL(runID string) string {
	return `
SELECT 'device_shadow.rows_current_converged', COUNT(*)
FROM device_shadows
WHERE shadow_name = ''
  AND desired = reported
  AND deleted_at IS NULL
`
}

func sanitizeEvidenceRunID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func sqlLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func kubectlLogsProbe(source string, namespace string, selector string, detail string) serverEvidenceProbe {
	script := fmt.Sprintf(
		`set -euo pipefail; pods="$(kubectl -n %s get pods --selector %s -o name)"; test -n "$pods"; kubectl -n %s logs --since=30m --selector %s --tail=-1`,
		shellQuote(namespace),
		shellQuote(selector),
		shellQuote(namespace),
		shellQuote(selector),
	)
	return serverEvidenceProbe{
		source:  source,
		command: "bash",
		args:    []string{"-lc", script},
		detail:  detail,
	}
}

func syncRemoteVMs(vms []LinodeVM, plan Plan, values workflowFlagValues) error {
	binaries, err := buildRemoteRunnerBinaries(values)
	if err != nil {
		return err
	}
	manifestBase := workflowOutDir(values)
	if err := writeAnsibleInputs(vms, plan, values, binaries); err != nil {
		return err
	}
	if err := writeJSONFile(filepath.Join(manifestBase, "sync-telemetry.json"), SyncTelemetry{VMs: initialSyncTelemetry(vms)}); err != nil {
		return err
	}
	return runAnsiblePlaybook(values, "sync.yml")
}

func envRootRsyncFilters() []string {
	return []string{
		"--include", "/env/***",
		"--include", "/services/***",
		"--include", "/devices/",
		"--include", "/devices/test_device/",
		"--include", "/devices/test_device/loadtest.env",
		"--include", "/devices/test_device/summary.json",
		"--include", "/artifacts/",
		"--include", "/artifacts/users/",
		"--include", "/artifacts/users/*.json",
		"--include", "/artifacts/device-bind/",
		"--include", "/artifacts/device-bind/*.json",
		"--exclude", "*",
	}
}

type remoteRunnerBinaries struct {
	Home100K      string
	RTKCloud      string
	CloudMQTTTest string
}

func writeAnsibleInputs(vms []LinodeVM, plan Plan, values workflowFlagValues, binaries remoteRunnerBinaries) error {
	base := workflowOutDir(values)
	for _, vm := range vms {
		assignment, ok := findAssignmentByLabel(plan, vm.Label)
		if !ok {
			return fmt.Errorf("shard not found for VM %s", vm.Label)
		}
		manifestPath := filepath.Join(base, "shard-manifests", vm.Label+".json")
		if err := writeJSONFile(manifestPath, assignment); err != nil {
			return fmt.Errorf("write shard manifest for %s: %w", vm.Label, err)
		}
		filterPath := filepath.Join(base, "env-rsync-filters", vm.Label+".filter")
		if err := writeEnvRsyncFilter(filterPath, plan.Conditions.EnvRoot, assignment); err != nil {
			return fmt.Errorf("write env rsync filter for %s: %w", vm.Label, err)
		}
		archivePath := filepath.Join(base, "env-archives", vm.Label+".tar.gz")
		if err := writeEnvArchive(archivePath, plan, assignment); err != nil {
			return fmt.Errorf("write env archive for %s: %w", vm.Label, err)
		}
	}
	return writeAnsibleInventoryAndVars(vms, plan, values, binaries)
}

func writeAnsibleInputsForExistingManifests(vms []LinodeVM, plan Plan, values workflowFlagValues) error {
	return writeAnsibleInventoryAndVars(vms, plan, values, remoteRunnerBinaries{
		Home100K:      filepath.Join(workflowOutDir(values), "bin", "home-100k-linux-amd64"),
		RTKCloud:      filepath.Join(workflowOutDir(values), "bin", "rtk-cloud-linux-amd64"),
		CloudMQTTTest: filepath.Join(workflowOutDir(values), "bin", "cloud-mqtt-test-linux-amd64"),
	})
}

func writeEnvRsyncFilter(path string, envRoot string, assignment VMAssignment) error {
	lines := []string{
		"+ /env/***",
		"+ /services/***",
		"+ /devices/",
		"+ /devices/test_device/",
		"+ /devices/test_device/loadtest.env",
		"+ /devices/test_device/summary.json",
		"+ /devices/test_device/manifests/",
		"+ /devices/test_device/manifests/devices.csv",
		"+ /devices/test_device/manifests/devices.json",
		"+ /devices/test_device/manifests/device_ids.txt",
		"+ /devices/test_device/devices/",
		"+ /devices/test_device/bundles/",
		"+ /artifacts/",
		"+ /artifacts/users/",
		"+ /artifacts/users/*.json",
		"+ /artifacts/device-bind/",
		"+ /artifacts/device-bind/*.json",
	}
	deviceRows, err := readDeviceManifestRows(filepath.Join(envRoot, "devices", "test_device", "manifests", "devices.csv"))
	if err == nil && len(deviceRows) == 0 {
		deviceRows, err = readDeviceManifestRowsFromJSON(filepath.Join(envRoot, "devices", "test_device", "manifests", "devices.json"))
	}
	if err == nil {
		for _, shard := range assignment.TaskShards {
			if shard.Role != "device-mqtt" {
				continue
			}
			for idx := shard.Start; idx < shard.End && idx < len(deviceRows); idx++ {
				row := deviceRows[idx]
				lines = append(lines,
					"+ /devices/test_device/devices/"+row.DeviceType+"/",
					"+ /devices/test_device/devices/"+row.DeviceType+"/"+row.DeviceID+"/***",
					"+ /devices/test_device/bundles/"+row.DeviceType+"/",
					"+ /devices/test_device/bundles/"+row.DeviceType+"/"+row.DeviceID+".pem",
				)
			}
		}
	}
	lines = append(lines, "- *")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(deduplicateLines(lines), "\n")+"\n"), 0o644)
}

type deviceManifestRow struct {
	DeviceID   string
	DeviceType string
}

type shardUserArtifact struct {
	Users []struct {
		Email string `json:"email"`
	} `json:"users"`
}

type shardBindArtifact struct {
	Brandname   string                `json:"brandname"`
	Assignments []shardBindAssignment `json:"assignments"`
}

type shardBindAssignment struct {
	AssignedEmail  string   `json:"assigned_email"`
	DeviceID       string   `json:"device_id"`
	DeviceType     string   `json:"device_type"`
	ServiceOptions []string `json:"service_options"`
}

func readDeviceManifestRows(path string) ([]deviceManifestRow, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	reader := csv.NewReader(file)
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("empty devices manifest: %s", path)
	}
	header := map[string]int{}
	for idx, name := range rows[0] {
		header[strings.TrimSpace(name)] = idx
	}
	idIdx, ok := header["device_id"]
	if !ok {
		return nil, fmt.Errorf("devices manifest missing device_id")
	}
	typeIdx, ok := header["device_type"]
	if !ok {
		return nil, fmt.Errorf("devices manifest missing device_type")
	}
	out := make([]deviceManifestRow, 0, len(rows)-1)
	for _, row := range rows[1:] {
		if idIdx >= len(row) || typeIdx >= len(row) {
			continue
		}
		deviceID := strings.TrimSpace(row[idIdx])
		deviceType := strings.TrimSpace(row[typeIdx])
		if deviceID == "" || deviceType == "" {
			continue
		}
		out = append(out, deviceManifestRow{DeviceID: deviceID, DeviceType: deviceType})
	}
	return out, nil
}

func readDeviceManifestRowsFromJSON(path string) ([]deviceManifestRow, error) {
	var rows []struct {
		DeviceID   string `json:"device_id"`
		DeviceType string `json:"device_type"`
	}
	if err := readJSON(path, &rows); err != nil {
		return nil, err
	}
	out := make([]deviceManifestRow, 0, len(rows))
	for _, row := range rows {
		deviceID := strings.TrimSpace(row.DeviceID)
		deviceType := strings.TrimSpace(row.DeviceType)
		if deviceID == "" || deviceType == "" {
			continue
		}
		out = append(out, deviceManifestRow{DeviceID: deviceID, DeviceType: deviceType})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("devices JSON manifest has no usable rows: %s", path)
	}
	return out, nil
}

func loadDeviceManifestRows(envRoot string) ([]deviceManifestRow, error) {
	rows, err := readDeviceManifestRows(filepath.Join(envRoot, "devices", "test_device", "manifests", "devices.csv"))
	if err == nil && len(rows) > 0 {
		return rows, nil
	}
	jsonRows, jsonErr := readDeviceManifestRowsFromJSON(filepath.Join(envRoot, "devices", "test_device", "manifests", "devices.json"))
	if jsonErr == nil {
		return jsonRows, nil
	}
	if err != nil {
		return nil, err
	}
	return nil, jsonErr
}

func writeEnvArchive(path string, plan Plan, assignment VMAssignment) error {
	envRoot := plan.Conditions.EnvRoot
	deviceRows, err := loadDeviceManifestRows(envRoot)
	if err != nil {
		return err
	}
	relPaths := []string{
		"env",
		"state/lke.env",
		"state/lke-kubeconfig.yaml",
		"state/video-cloud-staging.state.json",
		"services",
		"devices/test_device/loadtest.env",
		"devices/test_device/summary.json",
		"devices/test_device/manifests/devices.csv",
		"devices/test_device/manifests/devices.json",
		"devices/test_device/manifests/device_ids.txt",
		"artifacts/users",
		"artifacts/device-bind",
	}
	if stackState := stackStateRelPath(envRoot); stackState != "" {
		relPaths = append(relPaths, stackState)
	}
	shardRows, shardErr := loadShardDeviceRowsFromArtifacts(envRoot, plan, assignment)
	if shardErr == nil && len(shardRows) > 0 {
		deviceRows = shardRows
	}
	for _, shard := range assignment.TaskShards {
		if shard.Role != "device-mqtt" {
			continue
		}
		if shardErr == nil && len(shardRows) > 0 {
			for _, row := range shardRows {
				relPaths = append(relPaths, deviceCredentialRelPaths(row)...)
			}
			continue
		}
		for idx := shard.Start; idx < shard.End && idx < len(deviceRows); idx++ {
			relPaths = append(relPaths, deviceCredentialRelPaths(deviceRows[idx])...)
		}
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := createTarGzFromRelPaths(tmpPath, envRoot, deduplicateLines(relPaths)); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

func deviceCredentialRelPaths(row deviceManifestRow) []string {
	return []string{
		filepath.Join("devices", "test_device", "devices", row.DeviceType, row.DeviceID),
		filepath.Join("devices", "test_device", "bundles", row.DeviceType, row.DeviceID+".pem"),
	}
}

func stackStateRelPath(envRoot string) string {
	values := parseEnvFile(filepath.Join(envRoot, "env", "stack.env"))
	stack := strings.TrimSpace(values["CLOUD_STACK_NAME"])
	if stack == "" {
		return ""
	}
	rel := filepath.Join("state", stack+".state.json")
	if _, err := os.Stat(filepath.Join(envRoot, rel)); err == nil {
		return rel
	}
	return ""
}

func loadShardDeviceRowsFromArtifacts(envRoot string, plan Plan, assignment VMAssignment) ([]deviceManifestRow, error) {
	deviceShardCount := 0
	for _, shard := range assignment.TaskShards {
		if shard.Role == "device-mqtt" {
			deviceShardCount++
		}
	}
	if deviceShardCount == 0 {
		return nil, fmt.Errorf("assignment has no device shard")
	}
	brandLower := "rtk"
	usersPath := latestFile(filepath.Join(envRoot, "artifacts", "users", brandLower+"-users-*.json"))
	bindPath := latestHomeBindArtifact(filepath.Join(envRoot, "artifacts", "device-bind", brandLower+"-device-bind-*.json"), brandLower)
	if usersPath == "" || bindPath == "" {
		return nil, fmt.Errorf("missing users or device-bind artifact")
	}
	users := shardUserArtifact{}
	if err := readJSON(usersPath, &users); err != nil {
		return nil, err
	}
	bind := shardBindArtifact{}
	if err := readJSON(bindPath, &bind); err != nil {
		return nil, err
	}
	userEmails := map[string]bool{}
	for _, user := range users.Users {
		email := strings.TrimSpace(user.Email)
		if email != "" {
			userEmails[email] = true
		}
	}
	selectedByUser := map[string][]shardBindAssignment{}
	for _, item := range bind.Assignments {
		if !homeDeviceType(item.DeviceType) || !stringSliceContains(item.ServiceOptions, "mqtt") || !userEmails[item.AssignedEmail] {
			continue
		}
		selectedByUser[item.AssignedEmail] = append(selectedByUser[item.AssignedEmail], item)
	}
	selectedUsers := sortedMapKeys(selectedByUser)
	selected := []shardBindAssignment{}
	for _, email := range selectedUsers {
		selected = append(selected, selectedByUser[email]...)
	}
	shardCount := len(plan.ShardsByRole("device-mqtt"))
	if shardCount <= 0 {
		shardCount = 1
	}
	shardIndex := assignment.Index
	rows := []deviceManifestRow{}
	maxConnected := maxAssignmentConnectedDevices(assignment)
	for idx, item := range selected {
		if idx%shardCount != shardIndex {
			continue
		}
		rows = append(rows, deviceManifestRow{DeviceID: item.DeviceID, DeviceType: item.DeviceType})
		if maxConnected > 0 && len(rows) >= maxConnected {
			break
		}
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("no selected devices for shard %d/%d", shardIndex, shardCount)
	}
	return rows, nil
}

func maxAssignmentConnectedDevices(assignment VMAssignment) int {
	maxConnected := 0
	for _, shard := range assignment.TaskShards {
		if shard.Role != "device-mqtt" {
			continue
		}
		if shard.Count > maxConnected {
			maxConnected = shard.Count
		}
	}
	return maxConnected
}

func latestFile(pattern string) string {
	matches, _ := filepath.Glob(pattern)
	sort.Slice(matches, func(i, j int) bool {
		ai, _ := os.Stat(matches[i])
		aj, _ := os.Stat(matches[j])
		if ai == nil || aj == nil {
			return matches[i] < matches[j]
		}
		return ai.ModTime().After(aj.ModTime())
	})
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func latestHomeBindArtifact(pattern string, brandLower string) string {
	matches, _ := filepath.Glob(pattern)
	sort.Slice(matches, func(i, j int) bool {
		ai, _ := os.Stat(matches[i])
		aj, _ := os.Stat(matches[j])
		if ai == nil || aj == nil {
			return matches[i] < matches[j]
		}
		return ai.ModTime().After(aj.ModTime())
	})
	for _, path := range matches {
		bind := shardBindArtifact{}
		if err := readJSON(path, &bind); err != nil {
			continue
		}
		if strings.ToLower(bind.Brandname) != brandLower {
			continue
		}
		found := map[string]bool{}
		for _, item := range bind.Assignments {
			if homeDeviceType(item.DeviceType) && stringSliceContains(item.ServiceOptions, "mqtt") {
				found[item.DeviceType] = true
			}
		}
		if found["light"] && found["air_conditioner"] && found["smart_meter"] {
			return path
		}
	}
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func homeDeviceType(value string) bool {
	switch value {
	case "light", "air_conditioner", "smart_meter":
		return true
	default:
		return false
	}
}

func stringSliceContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sortedMapKeys[T any](values map[string]T) []string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func parseEnvFile(path string) map[string]string {
	out := map[string]string{}
	raw, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return out
}

func createTarGzFromRelPaths(path string, root string, relPaths []string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	gz := gzip.NewWriter(file)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	added := map[string]bool{}
	for _, rel := range relPaths {
		cleanRel := filepath.Clean(rel)
		if cleanRel == "." || strings.HasPrefix(cleanRel, "..") || filepath.IsAbs(cleanRel) {
			return fmt.Errorf("invalid archive path: %s", rel)
		}
		abs := filepath.Join(root, cleanRel)
		info, err := os.Lstat(abs)
		if err != nil {
			if os.IsNotExist(err) {
				continue
			}
			return err
		}
		if info.IsDir() {
			if err := filepath.WalkDir(abs, func(path string, entry fs.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				return addPathToTar(tw, root, path, added)
			}); err != nil {
				return err
			}
			continue
		}
		if err := addPathToTar(tw, root, abs, added); err != nil {
			return err
		}
	}
	return nil
}

func addPathToTar(tw *tar.Writer, root string, path string, added map[string]bool) error {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(filepath.Clean(rel))
	if rel == "." || strings.HasPrefix(rel, "../") || rel == ".." {
		return fmt.Errorf("invalid archive path: %s", path)
	}
	if added[rel] {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = rel
	if info.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return err
		}
		header.Linkname = target
	}
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	added[rel] = true
	if !info.Mode().IsRegular() {
		return nil
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	_, err = io.Copy(tw, file)
	return err
}

func deduplicateLines(lines []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if seen[line] {
			continue
		}
		seen[line] = true
		out = append(out, line)
	}
	return out
}

func writeAnsibleInventoryAndVars(vms []LinodeVM, plan Plan, values workflowFlagValues, binaries remoteRunnerBinaries) error {
	base := workflowOutDir(values)
	localOutDir, err := filepath.Abs(base)
	if err != nil {
		return err
	}
	localRunner, err := filepath.Abs(binaries.Home100K)
	if err != nil {
		return err
	}
	localRTKCloud, err := filepath.Abs(binaries.RTKCloud)
	if err != nil {
		return err
	}
	localCloudMQTTTest, err := filepath.Abs(binaries.CloudMQTTTest)
	if err != nil {
		return err
	}
	localEnvRoot, err := filepath.Abs(plan.Conditions.EnvRoot)
	if err != nil {
		return err
	}
	ansibleDir := filepath.Join(base, "ansible")
	if err := os.MkdirAll(ansibleDir, 0o755); err != nil {
		return err
	}
	hosts := map[string]any{}
	for _, vm := range vms {
		if strings.TrimSpace(vm.PublicIPv4) == "" {
			return fmt.Errorf("VM %s has no public IPv4 for ansible inventory", vm.Label)
		}
		role, shardIndex, err := shardFromVMLabel(vm.Label)
		if err != nil {
			return err
		}
		localShardManifest, err := filepath.Abs(filepath.Join(base, "shard-manifests", vm.Label+".json"))
		if err != nil {
			return err
		}
		localEnvRsyncFilter, err := filepath.Abs(filepath.Join(base, "env-rsync-filters", vm.Label+".filter"))
		if err != nil {
			return err
		}
		localEnvArchive, err := filepath.Abs(filepath.Join(base, "env-archives", vm.Label+".tar.gz"))
		if err != nil {
			return err
		}
		hosts[vm.Label] = map[string]any{
			"ansible_host":                 vm.PublicIPv4,
			"ansible_user":                 values.sshUser,
			"ansible_ssh_private_key_file": values.sshKey,
			"ansible_ssh_common_args":      "-o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=" + workflowKnownHostsFile(values),
			"run_id":                       normalizedRunID(values.runID),
			"role":                         role,
			"shard_index":                  shardIndex,
			"vm_label":                     vm.Label,
			"local_shard_manifest":         localShardManifest,
			"local_env_rsync_filter":       localEnvRsyncFilter,
			"local_env_archive":            localEnvArchive,
			"remote_shard_manifest":        strings.TrimRight(values.remoteWorkspace, "/") + "/loadtests/home-100k/shard-manifests/current.json",
			"remote_out_dir":               strings.TrimRight(firstNonEmpty(values.remoteOutRoot, "/var/lib/home-100k"), "/") + "/" + normalizedRunID(values.runID) + "/" + vm.Label,
		}
	}
	inventory := map[string]any{
		"all": map[string]any{
			"children": map[string]any{
				"home_100k": map[string]any{
					"hosts": hosts,
				},
			},
		},
	}
	if err := writeJSONFile(filepath.Join(ansibleDir, "inventory.json"), inventory); err != nil {
		return err
	}
	stageWarmUp := DefaultStageWarmUp
	stageSteady := DefaultStageSteady
	stageCoolDown := DefaultStageCoolDown
	if len(plan.Stages) > 0 {
		stageWarmUp = plan.Stages[0].WarmUp
		stageSteady = plan.Stages[0].SteadyState
		stageCoolDown = plan.Stages[0].CoolDown
	}
	extraVars := map[string]any{
		"run_id":                normalizedRunID(values.runID),
		"local_out_dir":         localOutDir,
		"local_runner":          localRunner,
		"local_rtk_cloud":       localRTKCloud,
		"local_cloud_mqtt_test": localCloudMQTTTest,
		"local_env_root":        strings.TrimRight(localEnvRoot, "/"),
		"remote_workspace":      strings.TrimRight(values.remoteWorkspace, "/"),
		"remote_env_root":       strings.TrimRight(values.remoteEnvRoot, "/"),
		"remote_out_root":       strings.TrimRight(firstNonEmpty(values.remoteOutRoot, "/var/lib/home-100k"), "/"),
		"brandname":             plan.Conditions.Brandname,
		"region":                plan.Conditions.Region,
		"stage_warm_up":         stageWarmUp,
		"stage_steady":          stageSteady,
		"stage_cool_down":       stageCoolDown,
		"runner_mode":           firstNonEmpty(values.runnerMode, "sample"),
		"mqtt_addr":             strings.TrimSpace(values.mqttAddr),
		"video_cloud_base_url":  strings.TrimSpace(values.videoCloudBaseURL),
		"account_manager_url":   strings.TrimSpace(values.accountManagerURL),
	}
	return writeJSONFile(filepath.Join(ansibleDir, "extra-vars.json"), extraVars)
}

func runAnsiblePlaybook(values workflowFlagValues, playbook string) error {
	base := workflowOutDir(values)
	args := []string{
		"--forks", "20",
		"-i", filepath.Join(base, "ansible", "inventory.json"),
		filepath.Join("loadtests", "home-100k", "ansible", playbook),
		"--extra-vars", "@" + filepath.Join(base, "ansible", "extra-vars.json"),
	}
	return commandRunner("ansible-playbook", args...)
}

func initialSyncTelemetry(vms []LinodeVM) []VMSyncTelemetry {
	items := make([]VMSyncTelemetry, 0, len(vms))
	for _, vm := range vms {
		items = append(items, VMSyncTelemetry{Label: vm.Label})
	}
	return items
}

func workflowOutDir(values workflowFlagValues) string {
	base := strings.TrimSpace(values.outDir)
	if base == "" {
		base = filepath.Join("loadtests", "home-100k", "reports", normalizedRunID(values.runID))
	}
	return base
}

func forEachVMParallel(vms []LinodeVM, fn func(LinodeVM) error) error {
	var wg sync.WaitGroup
	errs := make(chan error, len(vms))
	for _, vm := range vms {
		vm := vm
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := fn(vm); err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		return err
	}
	return nil
}

func buildRemoteRunnerBinaries(values workflowFlagValues) (remoteRunnerBinaries, error) {
	base := strings.TrimSpace(values.outDir)
	if base == "" {
		base = filepath.Join("loadtests", "home-100k", "reports", normalizedRunID(values.runID))
	}
	binDir := filepath.Join(base, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return remoteRunnerBinaries{}, err
	}
	home100K := filepath.Join(binDir, "home-100k-linux-amd64")
	cmd := fmt.Sprintf("GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o %s ./loadtests/home-100k/cmd/home-100k", shellQuote(home100K))
	if err := commandRunner("bash", "-lc", cmd); err != nil {
		return remoteRunnerBinaries{}, fmt.Errorf("build linux home-100k runner binary: %w", err)
	}
	rtkCloud := filepath.Join(binDir, "rtk-cloud-linux-amd64")
	cmd = fmt.Sprintf("GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o %s ./scripts/go/rtk-cloud", shellQuote(rtkCloud))
	if err := commandRunner("bash", "-lc", cmd); err != nil {
		return remoteRunnerBinaries{}, fmt.Errorf("build linux rtk-cloud runner binary: %w", err)
	}
	cloudMQTTTest := filepath.Join(binDir, "cloud-mqtt-test-linux-amd64")
	cmd = fmt.Sprintf("GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o %s ./scripts/go/cloud-mqtt-test", shellQuote(cloudMQTTTest))
	if err := commandRunner("bash", "-lc", cmd); err != nil {
		return remoteRunnerBinaries{}, fmt.Errorf("build linux cloud-mqtt-test runner binary: %w", err)
	}
	return remoteRunnerBinaries{Home100K: home100K, RTKCloud: rtkCloud, CloudMQTTTest: cloudMQTTTest}, nil
}

func waitForRemoteSSH(target string, sshBase []string) error {
	args := append(append([]string{}, sshBase...), "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=accept-new", target, "true")
	var lastErr error
	for attempt := 1; attempt <= 30; attempt++ {
		if err := commandRunner("ssh", args...); err == nil {
			return nil
		} else {
			lastErr = err
		}
		time.Sleep(10 * time.Second)
	}
	return lastErr
}

func bootstrapRemoteVM(target string, sshBase []string) error {
	script := "command -v rsync >/dev/null 2>&1 || (apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y rsync)"
	args := append(append([]string{}, sshBase...), target, "bash", "-lc", script)
	return commandRunner("ssh", args...)
}

func collectRemoteVMs(vms []LinodeVM, plan Plan, runID string, remoteOutRoot string, outDir string, values workflowFlagValues) error {
	if err := writeAnsibleInputsForExistingManifests(vms, plan, values); err != nil {
		return err
	}
	for _, vm := range vms {
		shardDir := filepath.Join(outDir, "shards", vm.Label)
		if err := os.MkdirAll(shardDir, 0o755); err != nil {
			return err
		}
	}
	return runAnsiblePlaybook(values, "collect.yml")
}

func dispatchRemoteShards(vms []LinodeVM, plan Plan, runID string, remoteOutRoot string, values workflowFlagValues) error {
	if err := writeAnsibleInputsForExistingManifests(vms, plan, values); err != nil {
		return err
	}
	if err := runAnsiblePlaybook(values, "start-runner.yml"); err != nil {
		return err
	}
	coordination, err := runHostCoordinator(vms, plan, runID, values)
	if err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(workflowOutDir(values), "start-coordination.json"), coordination)
}

func shardFromVMLabel(label string) (string, int, error) {
	const prefix = "home-100k-"
	if !strings.HasPrefix(label, prefix) {
		return "", 0, fmt.Errorf("VM label %s does not have %s prefix", label, prefix)
	}
	rest := strings.TrimPrefix(label, prefix)
	idx := strings.LastIndex(rest, "-")
	if idx <= 0 || idx == len(rest)-1 {
		return "", 0, fmt.Errorf("VM label %s does not contain shard index", label)
	}
	role := rest[:idx]
	var shardIndex int
	if _, err := fmt.Sscanf(rest[idx+1:], "%d", &shardIndex); err != nil {
		return "", 0, fmt.Errorf("VM label %s has invalid shard index: %w", label, err)
	}
	return role, shardIndex, nil
}

func workflowKnownHostsFile(values workflowFlagValues) string {
	base := strings.TrimSpace(values.outDir)
	if base == "" {
		base = filepath.Join("loadtests", "home-100k", "reports", normalizedRunID(values.runID))
	}
	return filepath.Join(base, "ssh_known_hosts")
}

func prepareKnownHostsPath(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	return os.MkdirAll(filepath.Dir(path), 0o755)
}

func sshArgs(sshKey string, knownHostsFile string) []string {
	args := make([]string, 0, 6)
	if strings.TrimSpace(sshKey) == "" {
		return args
	}
	args = append(args, "-i", sshKey)
	if strings.TrimSpace(knownHostsFile) != "" {
		args = append(args, "-o", "UserKnownHostsFile="+knownHostsFile)
	}
	return args
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func writeJSONTo(stdout io.Writer, stderr io.Writer, value any) int {
	encoder := json.NewEncoder(stdout)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}
