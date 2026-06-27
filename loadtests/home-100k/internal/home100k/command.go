package home100k

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

var commandRunner = func(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

var ansibleRetryDelay = 5 * time.Second

var commandRunnerWithTimeout = func(timeout time.Duration, name string, args ...string) error {
	if timeout <= 0 {
		return commandRunner(name, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("%s timed out after %s", name, timeout)
		}
		return err
	}
	return nil
}

var commandOutputRunner = func(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

var commandOutputRunnerWithTimeout = func(timeout time.Duration, name string, args ...string) (string, error) {
	if timeout <= 0 {
		return commandOutputRunner(name, args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	out, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		if cmd.Process != nil {
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return string(out), fmt.Errorf("%s timed out after %s", name, timeout)
	}
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
	case "token-only":
		return executeTokenOnly(args[1:], stdout, stderr)
	case "seed-token-projections":
		return executeSeedTokenProjections(args[1:], stdout, stderr)
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
	vmLabelPrefix := addVMLabelPrefixFlag(fs)
	stageWarmUp, stageSteady, stageCoolDown := addStageDurationFlags(fs)
	deviceCount, userCount, devicesPerUser, vmCount, loadGeneratorDevicesPerVM := addSizingFlags(fs)
	runnerNofile, sessionModel, readModel := addRuntimeConditionFlags(fs)
	functionalThreshold, targetThreshold, eventThreshold, aggregateTolerancePercent, aggregateMinTolerance := addGateThresholdFlags(fs)
	ephemeral := fs.Bool("ephemeral-vms", false, "require ephemeral VM lifecycle")
	if err := fs.Parse(args); err != nil {
		return PlanOptions{}, false, err
	}
	opts := PlanOptions{
		EnvRoot:   *envRoot,
		Brandname: *brandname,
		Region:    *region,
	}
	applyVMLabelPrefixFlag(&opts, vmLabelPrefix)
	applyStageDurationFlags(&opts, stageWarmUp, stageSteady, stageCoolDown)
	applySizingFlags(&opts, deviceCount, userCount, devicesPerUser, vmCount, loadGeneratorDevicesPerVM)
	applyRuntimeConditionFlags(&opts, runnerNofile, sessionModel, readModel)
	applyGateThresholdFlags(&opts, functionalThreshold, targetThreshold, eventThreshold, aggregateTolerancePercent, aggregateMinTolerance)
	return opts, *ephemeral, nil
}

func printUsage(w io.Writer) {
	fmt.Fprintln(w, `usage: home-100k <plan|run|token-only|seed-token-projections|provision-vms|sync|run-stages|collect|collect-server-evidence|aggregate|list-vms|destroy-vms|runner-daemon> --env-root PATH --brandname NAME --region LINODE_REGION [--ephemeral-vms]`)
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
	vmLabelPrefix := addVMLabelPrefixFlag(fs)
	stageWarmUp, stageSteady, stageCoolDown := addStageDurationFlags(fs)
	deviceCount, userCount, devicesPerUser, vmCount, loadGeneratorDevicesPerVM := addSizingFlags(fs)
	runnerNofile, sessionModel, readModel := addRuntimeConditionFlags(fs)
	functionalThreshold, targetThreshold, eventThreshold, aggregateTolerancePercent, aggregateMinTolerance := addGateThresholdFlags(fs)
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
	applyVMLabelPrefixFlag(&opts, vmLabelPrefix)
	applyStageDurationFlags(&opts, stageWarmUp, stageSteady, stageCoolDown)
	applySizingFlags(&opts, deviceCount, userCount, devicesPerUser, vmCount, loadGeneratorDevicesPerVM)
	applyRuntimeConditionFlags(&opts, runnerNofile, sessionModel, readModel)
	applyGateThresholdFlags(&opts, functionalThreshold, targetThreshold, eventThreshold, aggregateTolerancePercent, aggregateMinTolerance)
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

func addSizingFlags(fs *flag.FlagSet) (*int, *int, *int, *int, *int) {
	deviceCount := fs.Int("devices", 0, "total simulated device count")
	userCount := fs.Int("users", 0, "total simulated app user count")
	devicesPerUser := fs.Int("devices-per-user", 0, "target devices per app user when --users is omitted")
	vmCount := fs.Int("vm-count", 0, "number of mixed Linode generator VMs")
	loadGeneratorDevicesPerVM := fs.Int("load-generator-devices-per-vm", 0, "maximum simulated devices per load-generator VM")
	return deviceCount, userCount, devicesPerUser, vmCount, loadGeneratorDevicesPerVM
}

func addVMLabelPrefixFlag(fs *flag.FlagSet) *string {
	return fs.String("vm-label-prefix", DefaultVMLabelPrefix, "load-generator VM label prefix")
}

func applyVMLabelPrefixFlag(opts *PlanOptions, vmLabelPrefix *string) {
	opts.VMLabelPrefix = *vmLabelPrefix
}

func addRuntimeConditionFlags(fs *flag.FlagSet) (*int, *string, *string) {
	runnerNofile := fs.Int("runner-nofile-limit", DefaultRunnerNofile, "remote runner nofile limit for MQTT sockets")
	sessionModel := fs.String("device-session-model", DefaultDeviceSession, "device MQTT session model")
	readModel := fs.String("runner-read-model", DefaultRunnerReadModel, "runner MQTT read model")
	return runnerNofile, sessionModel, readModel
}

func addGateThresholdFlags(fs *flag.FlagSet) (*float64, *float64, *float64, *float64, *int64) {
	functionalThreshold := fs.Float64("functional-success-threshold-percent", DefaultFunctionalSuccessThresholdPercent, "functional pass threshold for MQTT connect, ACK, delta, and convergence rates")
	targetThreshold := fs.Float64("client-target-completeness-percent", DefaultClientTargetCompletenessPercent, "target coverage threshold for active devices/subscriptions and desired-write attempts")
	eventThreshold := fs.Float64("exact-event-correlation-percent", DefaultExactEventCorrelationPercent, "runtime event correlation threshold for command stream/sequence evidence")
	aggregateTolerancePercent := fs.Float64("aggregate-correlation-tolerance-percent", DefaultAggregateCorrelationTolerancePercent, "aggregate server/client counter tolerance percentage")
	aggregateMinTolerance := fs.Int64("aggregate-correlation-min-tolerance", DefaultAggregateCorrelationMinTolerance, "minimum aggregate server/client counter tolerance")
	return functionalThreshold, targetThreshold, eventThreshold, aggregateTolerancePercent, aggregateMinTolerance
}

func applyStageDurationFlags(opts *PlanOptions, stageWarmUp *string, stageSteady *string, stageCoolDown *string) {
	opts.StageWarmUp = *stageWarmUp
	opts.StageSteady = *stageSteady
	opts.StageCoolDown = *stageCoolDown
}

func applySizingFlags(opts *PlanOptions, deviceCount *int, userCount *int, devicesPerUser *int, vmCount *int, loadGeneratorDevicesPerVM *int) {
	opts.DeviceCount = *deviceCount
	opts.UserCount = *userCount
	opts.DevicesPerUser = *devicesPerUser
	opts.VMCount = *vmCount
	opts.LoadGeneratorDevicesPerVM = *loadGeneratorDevicesPerVM
}

func applyRuntimeConditionFlags(opts *PlanOptions, runnerNofile *int, sessionModel *string, readModel *string) {
	opts.RunnerNofile = *runnerNofile
	opts.SessionModel = *sessionModel
	opts.RunnerReadModel = *readModel
}

func applyGateThresholdFlags(opts *PlanOptions, functionalThreshold *float64, targetThreshold *float64, eventThreshold *float64, aggregateTolerancePercent *float64, aggregateMinTolerance *int64) {
	opts.FunctionalSuccessThresholdPercent = *functionalThreshold
	opts.ClientTargetCompletenessPercent = *targetThreshold
	opts.ExactEventCorrelationPercent = *eventThreshold
	opts.AggregateCorrelationTolerancePercent = *aggregateTolerancePercent
	opts.AggregateCorrelationMinTolerance = *aggregateMinTolerance
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
	runID                    string
	outDir                   string
	serverEvidenceFile       string
	live                     bool
	runnerMode               string
	vmStateFile              string
	remoteWorkspace          string
	remoteEnvRoot            string
	remoteOutRoot            string
	sshUser                  string
	sshKey                   string
	coordinatorDelayMS       int
	mqttAddr                 string
	videoCloudBaseURL        string
	videoCloudPublicURL      string
	videoCloudTokenURL       string
	accountManagerURL        string
	generatorHostsOverrideIP string
	credentialBundleFormat   string
	runnerNofileLimit        int
	mqttConcurrency          int
	commandConcurrency       int
	shadowCommandTimeout     string
	liveRunnerTimeoutGrace   string
}

type shardRunFlagValues struct {
	runID                  string
	outDir                 string
	role                   string
	shardIndex             int
	shardManifest          string
	honorStageDurations    bool
	runnerMode             string
	rtkCloudBinary         string
	workspace              string
	mqttConcurrency        int
	commandConcurrency     int
	shadowCommandTimeout   string
	liveRunnerTimeoutGrace string
}

const DefaultLiveMQTTConcurrency = 1000
const DefaultLiveCommandConcurrency = 100
const DefaultShadowCommandTimeout = "30s"
const DefaultLiveRunnerTimeoutGrace = "10m"

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
	reused := []LinodeVM{}
	existingByLabel := map[string]LinodeVM{}
	if existing, err := client.ListVMs(context.Background(), nil); err == nil {
		for _, vm := range existing {
			if strings.TrimSpace(vm.Label) != "" {
				existingByLabel[vm.Label] = vm
			}
		}
	} else {
		fmt.Fprintf(stderr, "warning: unable to list existing Linode VMs for reuse/conflict checks: %v\n", err)
	}
	for _, action := range actions {
		if vm, ok := existingByLabel[action.Label]; ok {
			if !vmReusableForRun(vm, runID) {
				fmt.Fprintf(stderr, "existing Linode VM label %s belongs to a different run or is missing required tags; expected tags home-100k, %s, load-generator\n", vm.Label, runID)
				return 1
			}
			if shouldBootLinodeVM(vm.Status) {
				if err := client.BootVM(context.Background(), vm.ID); err != nil {
					fmt.Fprintln(stderr, err)
					return 1
				}
				vm.Status = "booting"
			}
			reused = append(reused, vm)
			created = append(created, vm)
			continue
		}
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
			"reused":  reused,
		}); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	return writeJSONTo(stdout, stderr, map[string]any{
		"dry_run":       false,
		"run_id":        runID,
		"created":       created,
		"reused":        reused,
		"vm_state_file": vmStateFile,
	})
}

func vmReusableForRun(vm LinodeVM, runID string) bool {
	return stringSliceContains(vm.Tags, "home-100k") &&
		stringSliceContains(vm.Tags, runID) &&
		stringSliceContains(vm.Tags, "load-generator")
}

func shouldBootLinodeVM(status string) bool {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "running", "booting", "provisioning", "rebooting", "migrating":
		return false
	default:
		return true
	}
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
		if err := validatePlanDataCoverage(plan.Conditions.EnvRoot, plan); err != nil {
			writePreflightFailure(values.outDir, err)
			fmt.Fprintln(stderr, err)
			return 1
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
		if usesFormalRunner(values.runnerMode) && strings.TrimSpace(values.mqttAddr) == "" {
			fmt.Fprintln(stderr, "public MQTT endpoint is required for run-stages --live --runner-mode live; set --mqtt-addr or HOME100K_MQTT_ADDR so remote VMs do not fall back to kubectl port-forward")
			return 2
		}
		if err := validatePlanDataCoverage(plan.Conditions.EnvRoot, plan); err != nil {
			writePreflightFailure(values.outDir, err)
			fmt.Fprintln(stderr, err)
			return 1
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
	if usesFormalRunner(values.runnerMode) {
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

func usesFormalRunner(mode string) bool {
	return strings.EqualFold(mode, "live") || strings.EqualFold(mode, "formal")
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
	stageNames := make([]string, 0, len(plan.Stages))
	stageTargets := make([]string, 0, len(plan.Stages))
	stageDurations := make([]string, 0, len(plan.Stages))
	stageCommandRates := []string{}
	stageMinCommands := []string{}
	maxTarget := 0
	for _, stage := range plan.Stages {
		durationSeconds, err := stageWindowSeconds(stage)
		if err != nil {
			return fmt.Errorf("stage %s duration: %w", stage.Name, err)
		}
		maxConnected := shardConnectedDevices(stage.ConnectedDevices, plan.Conditions.Devices, assignment)
		if maxConnected > maxTarget {
			maxTarget = maxConnected
		}
		stageNames = append(stageNames, stage.Name)
		stageTargets = append(stageTargets, strconv.Itoa(maxConnected))
		stageDurations = append(stageDurations, strconv.Itoa(durationSeconds))
		stageCommandRates = append(stageCommandRates, commandRatePerDeviceDay(maxConnected, durationSeconds, plan.Conditions.DevicesPerUser))
		stageMinCommands = append(stageMinCommands, strconv.Itoa(ceilDiv(maxConnected, plan.Conditions.DevicesPerUser)))
	}
	totalDuration := 0
	for _, raw := range stageDurations {
		seconds, _ := strconv.Atoi(raw)
		totalDuration += seconds
	}
	stageOut := filepath.Join(outDir, "mqtt-test", "target")
	args := []string{
		"mqtt-test",
		"--env-root", plan.Conditions.EnvRoot,
		"--brandname", plan.Conditions.Brandname,
		"--profile", "baseline-10k",
		"--duration-seconds", strconv.Itoa(totalDuration),
		"--out-dir", stageOut,
		"--mqtt-probe",
		"--run-id", runID,
		"--shard-index", strconv.Itoa(assignment.Index),
		"--shard-count", strconv.Itoa(deviceShardCount),
		"--ramp-up", plan.Stages[0].WarmUp,
		"--telemetry-interval", "off",
		"--state-interval", plan.Stages[0].SteadyState,
		"--command-rate-per-device-per-day", maxCommandRatePerDeviceDay(stageCommandRates),
		"--load-model", "home-100k-sustained",
		"--stage-names", strings.Join(stageNames, ","),
		"--stage-connected-devices", strings.Join(stageTargets, ","),
		"--stage-durations-seconds", strings.Join(stageDurations, ","),
		"--stage-min-commands", strings.Join(stageMinCommands, ","),
		"--device-traffic-profile", firstNonEmpty(plan.ScenarioProfile, DefaultScenarioProfile),
		"--concurrency", strconv.Itoa(liveMQTTConcurrency(maxTarget, values.mqttConcurrency)),
		"--command-concurrency", strconv.Itoa(liveCommandConcurrency(maxTarget, values.commandConcurrency)),
		"--shadow-command-timeout", firstNonEmpty(values.shadowCommandTimeout, DefaultShadowCommandTimeout),
		"--max-connected-devices", strconv.Itoa(maxTarget),
	}
	if strings.TrimSpace(values.workspace) != "" {
		args = append([]string{args[0], "--workspace", values.workspace}, args[1:]...)
	}
	runTimeout, err := liveRunnerCommandTimeout(totalDuration, values.liveRunnerTimeoutGrace)
	if err != nil {
		return err
	}
	runErr := commandRunnerWithTimeout(runTimeout, rtkCloud, args...)
	stageResults, err := loadLiveMQTTShardResults(filepath.Join(stageOut, "results.json"), plan.Stages, stageTargets)
	if err != nil && len(stageResults) == 0 {
		stageResults = fallbackFailedLiveStageResults(plan.Stages, stageTargets, liveShardErrorText(runErr, err))
	}
	resultFile := filepath.Join(outDir, "results.json")
	reportFile := filepath.Join(outDir, "TEST_REPORT.md")
	status := "completed"
	partial := false
	errorText := ""
	if err != nil || runErr != nil {
		status = "failed"
		partial = err != nil
		if err != nil && runErr != nil {
			errorText = fmt.Sprintf("live target mqtt-test failed: %v; target live result: %v", runErr, err)
		} else if err != nil {
			errorText = fmt.Sprintf("target live result: %v", err)
		} else {
			errorText = fmt.Sprintf("live target mqtt-test failed: %v", runErr)
		}
	}
	if err := writeJSONFile(resultFile, map[string]any{
		"run_id":                runID,
		"role":                  values.role,
		"shard_index":           values.shardIndex,
		"runner_mode":           "live",
		"status":                status,
		"partial":               partial,
		"error":                 errorText,
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
	if err != nil {
		if runErr != nil {
			return fmt.Errorf("live target mqtt-test failed: %w; target live result: %v", runErr, err)
		}
		return fmt.Errorf("target live result: %w", err)
	}
	if runErr != nil {
		return fmt.Errorf("live target mqtt-test failed: %w", runErr)
	}
	return nil
}

func liveRunnerCommandTimeout(totalDurationSeconds int, graceRaw string) (time.Duration, error) {
	if totalDurationSeconds < 0 {
		return 0, fmt.Errorf("total duration must be non-negative")
	}
	base := time.Duration(totalDurationSeconds) * time.Second
	grace := base / 4
	minGrace, err := time.ParseDuration(DefaultLiveRunnerTimeoutGrace)
	if err != nil {
		return 0, err
	}
	if grace < minGrace {
		grace = minGrace
	}
	if raw := strings.TrimSpace(graceRaw); raw != "" {
		parsed, err := time.ParseDuration(raw)
		if err != nil {
			return 0, fmt.Errorf("invalid live runner timeout grace %q: %w", raw, err)
		}
		if parsed < 0 {
			return 0, fmt.Errorf("live runner timeout grace must be non-negative")
		}
		grace = parsed
	}
	return base + grace, nil
}

func fallbackFailedLiveStageResults(stages []Stage, stageTargets []string, detail string) []StageResult {
	results := make([]StageResult, 0, len(stages))
	for idx, stage := range stages {
		reasons := map[string]int64{"runner_failed": 1}
		details := map[string]map[string]int64{}
		if normalized := normalizeLiveShardFailureDetail(detail); normalized != "" {
			details["runner_failed"] = map[string]int64{normalized: 1}
		}
		results = append(results, StageResult{
			Name:             stage.Name,
			ConnectedDevices: stage.ConnectedDevices,
			DeviceMQTTTotals: DeviceMQTTTotals{
				ActiveConnections:   int64(parseStageTarget(stageTargets, idx)),
				ActiveSubscriptions: int64(parseStageTarget(stageTargets, idx)),
			},
			FailureReasons: reasons,
			FailureDetails: details,
		})
	}
	return results
}

func normalizeLiveShardFailureDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return ""
	}
	detail = strings.ToLower(detail)
	replacer := strings.NewReplacer("\n", " ", "\r", " ", "\t", " ")
	detail = replacer.Replace(detail)
	fields := strings.Fields(detail)
	if len(fields) == 0 {
		return ""
	}
	detail = strings.Join(fields, "_")
	if len(detail) > 160 {
		detail = detail[:160]
	}
	return detail
}

func liveShardErrorText(runErr error, resultErr error) string {
	switch {
	case runErr != nil && resultErr != nil:
		return fmt.Sprintf("live target mqtt-test failed: %v; target live result: %v", runErr, resultErr)
	case runErr != nil:
		return fmt.Sprintf("live target mqtt-test failed: %v", runErr)
	case resultErr != nil:
		return fmt.Sprintf("target live result: %v", resultErr)
	default:
		return ""
	}
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

func shardConnectedDevices(stageConnected int, totalDevices int, assignment VMAssignment) int {
	deviceCount := 0
	for _, shard := range assignment.TaskShards {
		if shard.Role == "device-mqtt" {
			deviceCount += shard.Count
		}
	}
	if deviceCount <= 0 {
		deviceCount = DefaultDevicesPerVM
	}
	if totalDevices <= 0 {
		totalDevices = DefaultDeviceCount
	}
	if deviceCount > totalDevices {
		deviceCount = totalDevices
	}
	target := stageConnected * deviceCount / totalDevices
	if target <= 0 && stageConnected > 0 {
		target = 1
	}
	return target
}

func commandRatePerDeviceDay(maxConnected int, durationSeconds int, devicesPerUser int) string {
	if maxConnected <= 0 || durationSeconds <= 0 {
		return "1.00"
	}
	if devicesPerUser <= 0 {
		devicesPerUser = DefaultDevicesPerUser
	}
	expectedUserWrites := maxConnected / devicesPerUser
	if expectedUserWrites <= 0 {
		expectedUserWrites = 1
	}
	rate := float64(expectedUserWrites) / float64(maxConnected) * 86400.0 / float64(durationSeconds) * 1.25
	if rate < 1 {
		rate = 1
	}
	return fmt.Sprintf("%.2f", rate)
}

func liveMQTTConcurrency(maxTarget int, configured int) int {
	if configured <= 0 {
		configured = DefaultLiveMQTTConcurrency
	}
	if maxTarget > 0 && configured > maxTarget {
		return maxTarget
	}
	return configured
}

func liveCommandConcurrency(maxTarget int, configured int) int {
	if configured <= 0 {
		configured = DefaultLiveCommandConcurrency
	}
	if maxTarget > 0 && configured > maxTarget {
		return maxTarget
	}
	return configured
}

func maxCommandRatePerDeviceDay(values []string) string {
	maxRate := 1.0
	for _, raw := range values {
		rate, err := strconv.ParseFloat(raw, 64)
		if err == nil && rate > maxRate {
			maxRate = rate
		}
	}
	return fmt.Sprintf("%.2f", maxRate)
}

type rawLiveMQTTResult struct {
	Name    string `json:"name"`
	Overall string `json:"overall"`
	Status  string `json:"status"`
	Metrics struct {
		DevicesSelected   int `json:"devices_selected"`
		CommandsAttempted int `json:"commands_attempted"`
		CommandsPassed    int `json:"commands_passed"`
	} `json:"metrics"`
	ConnectedDevices           int                         `json:"connected_devices"`
	ActiveConnections          int                         `json:"active_connections"`
	CommandsAttempted          int                         `json:"commands_attempted"`
	CommandsPassed             int                         `json:"commands_passed"`
	ConnectAttempts            int64                       `json:"connect_attempts"`
	ConnectSuccesses           int64                       `json:"connect_successes"`
	ConnectFailures            int64                       `json:"connect_failures"`
	DeviceTokenAttempts        int64                       `json:"device_token_attempts"`
	DeviceTokenSuccesses       int64                       `json:"device_token_successes"`
	DeviceTokenFailures        int64                       `json:"device_token_failures"`
	DeviceMQTTDialAttempts     int64                       `json:"device_mqtt_dial_attempts"`
	DeviceMQTTDialSuccesses    int64                       `json:"device_mqtt_dial_successes"`
	DeviceMQTTDialFailures     int64                       `json:"device_mqtt_dial_failures"`
	DeviceMQTTConnackAttempts  int64                       `json:"device_mqtt_connack_attempts"`
	DeviceMQTTConnackSuccesses int64                       `json:"device_mqtt_connack_successes"`
	DeviceMQTTConnackFailures  int64                       `json:"device_mqtt_connack_failures"`
	DeviceSubscribeAttempts    int64                       `json:"device_subscribe_attempts"`
	DeviceSubscribeFailures    int64                       `json:"device_subscribe_failures"`
	SubscribeSuccesses         int64                       `json:"subscribe_successes"`
	ActiveSubscriptions        int64                       `json:"active_subscriptions"`
	PublishSuccesses           int64                       `json:"publish_successes"`
	PublishFailures            int64                       `json:"publish_failures"`
	MessagesReceived           int64                       `json:"messages_received"`
	ReportedEvents             int64                       `json:"reported_events"`
	TotalBytesSent             int64                       `json:"total_bytes_sent"`
	TotalBytesReceived         int64                       `json:"total_bytes_received"`
	AuthViolations             int64                       `json:"auth_violations"`
	HTTPRequests               int64                       `json:"http_requests"`
	HTTPSuccesses              int64                       `json:"http_successes"`
	HTTPFailures               int64                       `json:"http_failures"`
	AppTokenAttempts           int64                       `json:"app_token_attempts"`
	AppTokenSuccesses          int64                       `json:"app_token_successes"`
	AppTokenFailures           int64                       `json:"app_token_failures"`
	AppMQTTDialAttempts        int64                       `json:"app_mqtt_dial_attempts"`
	AppMQTTDialSuccesses       int64                       `json:"app_mqtt_dial_successes"`
	AppMQTTDialFailures        int64                       `json:"app_mqtt_dial_failures"`
	AppMQTTConnackAttempts     int64                       `json:"app_mqtt_connack_attempts"`
	AppMQTTConnackSuccesses    int64                       `json:"app_mqtt_connack_successes"`
	AppMQTTConnackFailures     int64                       `json:"app_mqtt_connack_failures"`
	TotalHTTPBytesSent         int64                       `json:"total_http_bytes_sent"`
	TotalHTTPBytesReceived     int64                       `json:"total_http_bytes_received"`
	RejectedUpdates            int64                       `json:"rejected_updates"`
	DeviceMQTTTotals           DeviceMQTTTotals            `json:"device_mqtt_totals"`
	AppUserTotals              AppUserTotals               `json:"app_user_totals"`
	FailureReasons             map[string]int64            `json:"failure_reasons"`
	FailureDetails             map[string]map[string]int64 `json:"failure_details"`
	FailureEvents              []FailureEvent              `json:"failure_events"`
	CommandEvents              []CommandEvent              `json:"command_events"`
	DeviceTypeTotals           map[string]DeviceTypeTotals `json:"device_type_totals"`
	UserActionTotals           map[string]int64            `json:"user_action_totals"`
	UsageWindowTotals          map[string]int64            `json:"usage_window_totals"`
	StageDiagnostics           map[string]any              `json:"stage_diagnostics"`
}

func loadLiveMQTTShardResults(path string, stages []Stage, stageTargets []string) ([]StageResult, error) {
	var root struct {
		StageResults []rawLiveMQTTResult `json:"stage_results"`
	}
	if err := readJSON(path, &root); err != nil {
		return nil, err
	}
	if len(root.StageResults) == 0 {
		if len(stages) == 0 {
			return nil, fmt.Errorf("no stages configured")
		}
		result, err := loadLiveMQTTStageResult(path, stages[0], parseStageTarget(stageTargets, 0))
		if err != nil {
			return nil, err
		}
		return []StageResult{result}, nil
	}
	results := make([]StageResult, 0, len(root.StageResults))
	for idx, raw := range root.StageResults {
		if idx >= len(stages) {
			break
		}
		results = append(results, convertLiveMQTTStageResult(raw, stages[idx], parseStageTarget(stageTargets, idx)))
	}
	if len(root.StageResults) != len(stages) {
		return results, fmt.Errorf("stage_results len = %d, want %d", len(root.StageResults), len(stages))
	}
	return results, nil
}

func parseStageTarget(values []string, idx int) int {
	if idx < 0 || idx >= len(values) {
		return 0
	}
	value, _ := strconv.Atoi(values[idx])
	return value
}

func loadLiveMQTTStageResult(path string, stage Stage, maxConnected int) (StageResult, error) {
	var raw rawLiveMQTTResult
	if err := readJSON(path, &raw); err != nil {
		return StageResult{}, err
	}
	return convertLiveMQTTStageResult(raw, stage, maxConnected), nil
}

func convertLiveMQTTStageResult(raw rawLiveMQTTResult, stage Stage, maxConnected int) StageResult {
	commandsAttempted := raw.Metrics.CommandsAttempted
	if commandsAttempted == 0 {
		commandsAttempted = raw.CommandsAttempted
	}
	commandsPassed := raw.Metrics.CommandsPassed
	if commandsPassed == 0 {
		commandsPassed = raw.CommandsPassed
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
	activeConnections := nonZeroInt64(raw.DeviceMQTTTotals.ActiveConnections, int64(raw.ActiveConnections))
	activeSubscriptions := nonZeroInt64(raw.DeviceMQTTTotals.ActiveSubscriptions, raw.ActiveSubscriptions)
	if activeConnections == 0 {
		activeConnections = connectSuccess
	}
	if activeSubscriptions == 0 {
		activeSubscriptions = subscribes
	}
	httpRequests := nonZeroInt64(raw.AppUserTotals.DesiredWrites, raw.HTTPRequests)
	httpSuccesses := nonZeroInt64(raw.AppUserTotals.ReceivedAcks, raw.HTTPSuccesses)
	return StageResult{
		Name:                  stage.Name,
		ConnectedDevices:      stage.ConnectedDevices,
		ShardConnectedDevices: maxConnected,
		DeviceMQTTTotals: DeviceMQTTTotals{
			ConnectAttempts:     connectAttempts,
			ConnectSuccess:      connectSuccess,
			ConnectFail:         connectFail,
			TokenAttempts:       nonZeroInt64(raw.DeviceMQTTTotals.TokenAttempts, raw.DeviceTokenAttempts),
			TokenSuccess:        nonZeroInt64(raw.DeviceMQTTTotals.TokenSuccess, raw.DeviceTokenSuccesses),
			TokenFail:           nonZeroInt64(raw.DeviceMQTTTotals.TokenFail, raw.DeviceTokenFailures),
			MQTTDialAttempts:    nonZeroInt64(raw.DeviceMQTTTotals.MQTTDialAttempts, raw.DeviceMQTTDialAttempts),
			MQTTDialSuccess:     nonZeroInt64(raw.DeviceMQTTTotals.MQTTDialSuccess, raw.DeviceMQTTDialSuccesses),
			MQTTDialFail:        nonZeroInt64(raw.DeviceMQTTTotals.MQTTDialFail, raw.DeviceMQTTDialFailures),
			MQTTConnackAttempts: nonZeroInt64(raw.DeviceMQTTTotals.MQTTConnackAttempts, raw.DeviceMQTTConnackAttempts),
			MQTTConnackSuccess:  nonZeroInt64(raw.DeviceMQTTTotals.MQTTConnackSuccess, raw.DeviceMQTTConnackSuccesses),
			MQTTConnackFail:     nonZeroInt64(raw.DeviceMQTTTotals.MQTTConnackFail, raw.DeviceMQTTConnackFailures),
			SubscribeAttempts:   nonZeroInt64(raw.DeviceMQTTTotals.SubscribeAttempts, raw.DeviceSubscribeAttempts),
			SubscribeFail:       nonZeroInt64(raw.DeviceMQTTTotals.SubscribeFail, raw.DeviceSubscribeFailures),
			Subscribes:          subscribes,
			ActiveConnections:   activeConnections,
			ActiveSubscriptions: activeSubscriptions,
			Publishes:           publishes,
			ReceivedMessages:    receivedMessages,
			DeltaReceived:       deltaReceived,
			ReportedPublishes:   reportedPublishes,
			RejectedPublishes:   rejectedPublishes,
			BytesSent:           nonZeroInt64(raw.DeviceMQTTTotals.BytesSent, raw.TotalBytesSent),
			BytesReceived:       nonZeroInt64(raw.DeviceMQTTTotals.BytesReceived, raw.TotalBytesReceived),
		},
		AppUserTotals: AppUserTotals{
			LoginAttempts:       raw.AppUserTotals.LoginAttempts,
			LoginSuccess:        raw.AppUserTotals.LoginSuccess,
			LoginFail:           raw.AppUserTotals.LoginFail,
			TokenAttempts:       nonZeroInt64(raw.AppUserTotals.TokenAttempts, raw.AppTokenAttempts),
			TokenSuccess:        nonZeroInt64(raw.AppUserTotals.TokenSuccess, raw.AppTokenSuccesses),
			TokenFail:           nonZeroInt64(raw.AppUserTotals.TokenFail, raw.AppTokenFailures),
			MQTTDialAttempts:    nonZeroInt64(raw.AppUserTotals.MQTTDialAttempts, raw.AppMQTTDialAttempts),
			MQTTDialSuccess:     nonZeroInt64(raw.AppUserTotals.MQTTDialSuccess, raw.AppMQTTDialSuccesses),
			MQTTDialFail:        nonZeroInt64(raw.AppUserTotals.MQTTDialFail, raw.AppMQTTDialFailures),
			MQTTConnackAttempts: nonZeroInt64(raw.AppUserTotals.MQTTConnackAttempts, raw.AppMQTTConnackAttempts),
			MQTTConnackSuccess:  nonZeroInt64(raw.AppUserTotals.MQTTConnackSuccess, raw.AppMQTTConnackSuccesses),
			MQTTConnackFail:     nonZeroInt64(raw.AppUserTotals.MQTTConnackFail, raw.AppMQTTConnackFailures),
			ListDevicesRequests: raw.AppUserTotals.ListDevicesRequests,
			ReadShadowRequests:  raw.AppUserTotals.ReadShadowRequests,
			DesiredWrites:       httpRequests,
			ReceivedAcks:        httpSuccesses,
			BytesSent:           nonZeroInt64(raw.AppUserTotals.BytesSent, raw.TotalHTTPBytesSent),
			BytesReceived:       nonZeroInt64(raw.AppUserTotals.BytesReceived, raw.TotalHTTPBytesReceived),
		},
		MQTTConnectSuccessRatePercent:  connectSuccessPercent(DeviceMQTTTotals{ConnectAttempts: connectAttempts, ConnectSuccess: connectSuccess}),
		DesiredReportedConvergenceRate: percent(commandsPassed, commandsAttempted),
		OfflineDesiredConvergenceRate:  100,
		DeltaClearSuccessRatePercent:   percent(commandsPassed, commandsAttempted),
		RejectedUpdateCount:            int(raw.RejectedUpdates),
		AuthorizationViolationCount:    int(raw.AuthViolations),
		ClientTokenCorrelationCount:    int(httpSuccesses),
		FailureReasons:                 raw.FailureReasons,
		FailureDetails:                 raw.FailureDetails,
		FailureEvents:                  raw.FailureEvents,
		CommandEvents:                  raw.CommandEvents,
		DeviceTypeTotals:               raw.DeviceTypeTotals,
		UserActionTotals:               raw.UserActionTotals,
		UsageWindowTotals:              raw.UsageWindowTotals,
		StageDiagnostics:               liveStageDiagnostics(raw.StageDiagnostics),
	}
}

func liveStageDiagnostics(raw map[string]any) []map[string]any {
	if len(raw) == 0 {
		return nil
	}
	return []map[string]any{raw}
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
	outDir, err := localWorkflowOutDir(values)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
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
	plan, values, code := buildWorkflowPlan("home-100k collect-server-evidence", args, stderr)
	if code != 0 {
		return code
	}
	runID := normalizedRunID(values.runID)
	if values.live {
		outDir := strings.TrimSpace(values.outDir)
		if outDir != "" {
			resolved, err := localWorkflowOutDir(values)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			outDir = resolved
		}
		evidence := collectLiveServerEvidence(plan.Conditions.EnvRoot, runID, outDir)
		outputPath := strings.TrimSpace(values.serverEvidenceFile)
		if outputPath == "" && outDir != "" {
			outputPath = filepath.Join(outDir, "server-evidence.json")
		}
		if outputPath != "" {
			resolved, err := localWorkflowArtifactPath(outputPath)
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			outputPath = resolved
			if err := writeJSONFile(outputPath, evidence); err != nil {
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
	outDir, err := localWorkflowOutDir(values)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	result, err := AggregateCollectedRun(AggregateOptions{
		PlanOptions: opts,
		RunID:       values.runID,
		OutDir:      outDir,
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
	tags := []string{"home-100k", runID, "load-generator"}
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
		fmt.Fprintln(stderr, err)
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
	vmLabelPrefix := addVMLabelPrefixFlag(fs)
	stageWarmUp, stageSteady, stageCoolDown := addStageDurationFlags(fs)
	deviceCount, userCount, devicesPerUser, vmCount, loadGeneratorDevicesPerVM := addSizingFlags(fs)
	functionalThreshold, targetThreshold, eventThreshold, aggregateTolerancePercent, aggregateMinTolerance := addGateThresholdFlags(fs)
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
	applyVMLabelPrefixFlag(&opts, vmLabelPrefix)
	applyStageDurationFlags(&opts, stageWarmUp, stageSteady, stageCoolDown)
	applySizingFlags(&opts, deviceCount, userCount, devicesPerUser, vmCount, loadGeneratorDevicesPerVM)
	applyGateThresholdFlags(&opts, functionalThreshold, targetThreshold, eventThreshold, aggregateTolerancePercent, aggregateMinTolerance)
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
	vmLabelPrefix := addVMLabelPrefixFlag(fs)
	stageWarmUp, stageSteady, stageCoolDown := addStageDurationFlags(fs)
	deviceCount, userCount, devicesPerUser, vmCount, loadGeneratorDevicesPerVM := addSizingFlags(fs)
	functionalThreshold, targetThreshold, eventThreshold, aggregateTolerancePercent, aggregateMinTolerance := addGateThresholdFlags(fs)
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
	applyVMLabelPrefixFlag(&opts, vmLabelPrefix)
	applyStageDurationFlags(&opts, stageWarmUp, stageSteady, stageCoolDown)
	applySizingFlags(&opts, deviceCount, userCount, devicesPerUser, vmCount, loadGeneratorDevicesPerVM)
	applyGateThresholdFlags(&opts, functionalThreshold, targetThreshold, eventThreshold, aggregateTolerancePercent, aggregateMinTolerance)
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
	vmLabelPrefix := addVMLabelPrefixFlag(fs)
	stageWarmUp, stageSteady, stageCoolDown := addStageDurationFlags(fs)
	deviceCount, userCount, devicesPerUser, vmCount, loadGeneratorDevicesPerVM := addSizingFlags(fs)
	functionalThreshold, targetThreshold, eventThreshold, aggregateTolerancePercent, aggregateMinTolerance := addGateThresholdFlags(fs)
	runID := fs.String("run-id", "", "run id for VM tags")
	live := fs.Bool("live", false, "query Linode VMs")
	linodeToken := fs.String("linode-token", os.Getenv("LINODE_TOKEN"), "Linode API token")
	linodeEndpoint := fs.String("linode-endpoint", "", "Linode API endpoint")
	if err := fs.Parse(args); err != nil {
		return PlanOptions{}, listVMFlagValues{}, err
	}
	opts := PlanOptions{EnvRoot: *envRoot, Brandname: *brandname, Region: *region}
	applyVMLabelPrefixFlag(&opts, vmLabelPrefix)
	applyStageDurationFlags(&opts, stageWarmUp, stageSteady, stageCoolDown)
	applySizingFlags(&opts, deviceCount, userCount, devicesPerUser, vmCount, loadGeneratorDevicesPerVM)
	applyGateThresholdFlags(&opts, functionalThreshold, targetThreshold, eventThreshold, aggregateTolerancePercent, aggregateMinTolerance)
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
	vmLabelPrefix := addVMLabelPrefixFlag(fs)
	stageWarmUp, stageSteady, stageCoolDown := addStageDurationFlags(fs)
	deviceCount, userCount, devicesPerUser, vmCount, loadGeneratorDevicesPerVM := addSizingFlags(fs)
	runnerNofile, sessionModel, readModel := addRuntimeConditionFlags(fs)
	functionalThreshold, targetThreshold, eventThreshold, aggregateTolerancePercent, aggregateMinTolerance := addGateThresholdFlags(fs)
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
	mqttConcurrency := fs.Int("mqtt-concurrency", DefaultLiveMQTTConcurrency, "per-shard MQTT connect worker concurrency for live runner")
	commandConcurrency := fs.Int("command-concurrency", DefaultLiveCommandConcurrency, "per-shard sustained shadow command concurrency for live runner")
	shadowCommandTimeout := fs.String("shadow-command-timeout", DefaultShadowCommandTimeout, "per-phase sustained shadow command timeout")
	liveRunnerTimeoutGrace := fs.String("live-runner-timeout-grace", "", "extra timeout after the configured live MQTT duration before killing the shard runner")
	mqttAddr := fs.String("mqtt-addr", "", "public MQTT host:port for remote load-generator VMs")
	videoCloudBaseURL := fs.String("video-cloud-base-url", "", "legacy alias for --video-cloud-public-base-url")
	videoCloudPublicURL := fs.String("video-cloud-public-base-url", "", "Video Cloud public API base URL for remote load-generator VMs")
	videoCloudTokenURL := fs.String("video-cloud-token-base-url", "", "Video Cloud mTLS token bootstrap base URL for remote load-generator VMs")
	accountManagerURL := fs.String("account-manager-base-url", "", "Account Manager base URL for remote load-generator VMs")
	generatorHostsOverrideIP := fs.String("generator-hosts-override-ip", "", "optional IPv4 address to map staging HTTPS hostnames to on load-generator VMs")
	credentialBundleFormat := fs.String("credential-bundle-format", "sqlite-gzip", "credential bundle format: sqlite-gzip")
	if err := fs.Parse(args); err != nil {
		return PlanOptions{}, workflowFlagValues{}, err
	}
	if strings.TrimSpace(*credentialBundleFormat) != "sqlite-gzip" {
		return PlanOptions{}, workflowFlagValues{}, fmt.Errorf("unsupported --credential-bundle-format %q; only sqlite-gzip is supported", *credentialBundleFormat)
	}
	opts := PlanOptions{EnvRoot: *envRoot, Brandname: *brandname, Region: *region}
	applyVMLabelPrefixFlag(&opts, vmLabelPrefix)
	applyStageDurationFlags(&opts, stageWarmUp, stageSteady, stageCoolDown)
	applySizingFlags(&opts, deviceCount, userCount, devicesPerUser, vmCount, loadGeneratorDevicesPerVM)
	applyRuntimeConditionFlags(&opts, runnerNofile, sessionModel, readModel)
	applyGateThresholdFlags(&opts, functionalThreshold, targetThreshold, eventThreshold, aggregateTolerancePercent, aggregateMinTolerance)
	return opts, workflowFlagValues{
		runID:                    *runID,
		outDir:                   *outDir,
		serverEvidenceFile:       *serverEvidenceFile,
		live:                     *live,
		runnerMode:               *runnerMode,
		vmStateFile:              *vmStateFile,
		remoteWorkspace:          *remoteWorkspace,
		remoteEnvRoot:            *remoteEnvRoot,
		remoteOutRoot:            *remoteOutRoot,
		sshUser:                  *sshUser,
		sshKey:                   *sshKey,
		coordinatorDelayMS:       *coordinatorDelayMS,
		mqttAddr:                 *mqttAddr,
		videoCloudBaseURL:        *videoCloudBaseURL,
		videoCloudPublicURL:      *videoCloudPublicURL,
		videoCloudTokenURL:       *videoCloudTokenURL,
		accountManagerURL:        *accountManagerURL,
		generatorHostsOverrideIP: strings.TrimSpace(*generatorHostsOverrideIP),
		credentialBundleFormat:   strings.TrimSpace(*credentialBundleFormat),
		runnerNofileLimit:        *runnerNofile,
		mqttConcurrency:          *mqttConcurrency,
		commandConcurrency:       *commandConcurrency,
		shadowCommandTimeout:     strings.TrimSpace(*shadowCommandTimeout),
		liveRunnerTimeoutGrace:   strings.TrimSpace(*liveRunnerTimeoutGrace),
	}, nil
}

func parseShardRunFlags(name string, args []string, stderr io.Writer) (PlanOptions, shardRunFlagValues, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	envRoot := fs.String("env-root", "", "staging/LKE env-root")
	brandname := fs.String("brandname", "", "brand name")
	region := fs.String("region", "", "Linode region for load-generator VMs")
	vmLabelPrefix := addVMLabelPrefixFlag(fs)
	stageWarmUp, stageSteady, stageCoolDown := addStageDurationFlags(fs)
	deviceCount, userCount, devicesPerUser, vmCount, loadGeneratorDevicesPerVM := addSizingFlags(fs)
	functionalThreshold, targetThreshold, eventThreshold, aggregateTolerancePercent, aggregateMinTolerance := addGateThresholdFlags(fs)
	runID := fs.String("run-id", "", "run id for artifact correlation")
	outDir := fs.String("out-dir", "", "artifact output directory")
	role := fs.String("role", "", "shard role")
	shardIndex := fs.Int("shard-index", 0, "shard index")
	shardManifest := fs.String("shard-manifest", "", "shard manifest JSON path")
	honorStageDurations := fs.Bool("honor-stage-durations", false, "sleep through configured stage warm-up, steady, and cool-down windows")
	runnerMode := fs.String("runner-mode", "sample", "runner mode: sample or live")
	rtkCloudBinary := fs.String("rtk-cloud-binary", "rtk-cloud", "rtk-cloud binary for live MQTT/API runner")
	workspace := fs.String("workspace", "", "workspace path for live MQTT/API runner")
	mqttConcurrency := fs.Int("mqtt-concurrency", DefaultLiveMQTTConcurrency, "per-shard MQTT connect worker concurrency for live runner")
	commandConcurrency := fs.Int("command-concurrency", DefaultLiveCommandConcurrency, "per-shard sustained shadow command concurrency for live runner")
	shadowCommandTimeout := fs.String("shadow-command-timeout", DefaultShadowCommandTimeout, "per-phase sustained shadow command timeout")
	liveRunnerTimeoutGrace := fs.String("live-runner-timeout-grace", "", "extra timeout after the configured live MQTT duration before killing the shard runner")
	if err := fs.Parse(args); err != nil {
		return PlanOptions{}, shardRunFlagValues{}, err
	}
	if strings.TrimSpace(*role) == "" {
		return PlanOptions{}, shardRunFlagValues{}, fmt.Errorf("--role is required")
	}
	opts := PlanOptions{EnvRoot: *envRoot, Brandname: *brandname, Region: *region}
	applyVMLabelPrefixFlag(&opts, vmLabelPrefix)
	applyStageDurationFlags(&opts, stageWarmUp, stageSteady, stageCoolDown)
	applySizingFlags(&opts, deviceCount, userCount, devicesPerUser, vmCount, loadGeneratorDevicesPerVM)
	applyGateThresholdFlags(&opts, functionalThreshold, targetThreshold, eventThreshold, aggregateTolerancePercent, aggregateMinTolerance)
	return opts, shardRunFlagValues{
		runID:                  *runID,
		outDir:                 *outDir,
		role:                   *role,
		shardIndex:             *shardIndex,
		shardManifest:          *shardManifest,
		honorStageDurations:    *honorStageDurations,
		runnerMode:             *runnerMode,
		rtkCloudBinary:         *rtkCloudBinary,
		workspace:              *workspace,
		mqttConcurrency:        *mqttConcurrency,
		commandConcurrency:     *commandConcurrency,
		shadowCommandTimeout:   strings.TrimSpace(*shadowCommandTimeout),
		liveRunnerTimeoutGrace: strings.TrimSpace(*liveRunnerTimeoutGrace),
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

func collectLiveServerEvidence(envRoot string, runID string, outDir string) ServerEvidence {
	sources := map[string]EvidenceSource{}
	notes := []string{}
	windowStart := evidenceWindowStart(outDir)
	windowMode := "fallback_since_30m"
	logsSinceArg := "--since=30m"
	if windowStart != "" {
		windowMode = "run_scoped_since_time"
		logsSinceArg = "--since-time=" + windowStart
	}
	for _, probe := range serverEvidenceProbes(envRoot, runID, logsSinceArg) {
		timeout := probe.timeout
		if timeout <= 0 {
			timeout = defaultServerEvidenceProbeTimeout
		}
		out, err := commandOutputRunnerWithTimeout(timeout, probe.command, probe.args...)
		source, note := evidenceSourceFromProbeResult(probe, runID, out, err)
		sources[probe.source] = mergeEvidenceSource(sources[probe.source], source)
		if note != "" {
			notes = append(notes, note)
		}
	}
	shadowSource, streamSource, runtimeNote := collectCentralLoggerRuntimeLogEvidence(envRoot, runID, windowStart)
	if shadowSource.Available {
		sources["iot_device_shadow"] = shadowSource
	}
	if streamSource.Available {
		sources["iot_device_shadow_streams"] = streamSource
	}
	if runtimeNote != "" {
		notes = append(notes, runtimeNote)
	}
	loggerSource, loggerNote := collectCentralLoggerEvidence(envRoot, runID)
	sources["central_logger"] = loggerSource
	if loggerNote != "" {
		notes = append(notes, loggerNote)
	}
	normalizeEvidenceSourceCatalogMetadata(sources)
	evidence := ServerEvidence{
		RunID:               runID,
		EvidenceWindowStart: windowStart,
		EvidenceWindowMode:  windowMode,
		Complete:            allEvidenceSourcesAvailable(sources),
		Sources:             sources,
		Notes:               notes,
	}
	applyServerEvidenceBaselineDeltas(&evidence, runID, outDir, &notes)
	evidence.Notes = notes
	return evidence
}

func evidenceSourceFromProbeResult(probe serverEvidenceProbe, runID string, out string, err error) (EvidenceSource, string) {
	counters := parseEvidenceCounters(probe.source, runID, out)
	samples := parseEvidenceSamples(probe.source, out)
	if err == nil {
		return EvidenceSource{Available: true, Detail: probe.detail, Counters: counters, Samples: samples}, ""
	}
	note := fmt.Sprintf("%s evidence probe failed: %s", probe.source, err.Error())
	if len(counters) > 0 || len(samples) > 0 {
		detail := strings.TrimSpace(probe.detail + "; probe exited non-zero after producing parseable evidence: " + err.Error())
		return EvidenceSource{Available: true, Detail: detail, Counters: counters, Samples: samples}, note
	}
	detail := strings.TrimSpace(err.Error() + " " + redactEvidenceOutput(out))
	return EvidenceSource{Available: false, Detail: detail}, note
}

func normalizeEvidenceSourceCatalogMetadata(sources map[string]EvidenceSource) {
	if sources == nil {
		return
	}
	for key, catalogSource := range evidenceSourceCatalog(false) {
		source, ok := sources[key]
		if !ok {
			catalogSource.Detail = "probe not configured"
			sources[key] = catalogSource
			continue
		}
		source.Optional = source.Optional || catalogSource.Optional
		sources[key] = source
	}
}

func collectCentralLoggerEvidence(envRoot string, runID string) (EvidenceSource, string) {
	if skipCentralLoggerEvidence() {
		return EvidenceSource{Available: false, Optional: true, Detail: "central logger evidence skipped by HOME100K_SKIP_CENTRAL_LOGGER"}, "central_logger evidence probe skipped by HOME100K_SKIP_CENTRAL_LOGGER"
	}
	values := centralLoggerEnvValues(envRoot)
	endpoint, token := centralLoggerEndpointAndToken(values)
	if endpoint == "" || token == "" {
		return EvidenceSource{Available: false, Optional: true, Detail: "central logger endpoint or token missing"}, "central_logger evidence probe skipped: endpoint/token missing"
	}
	counters := map[string]int64{}
	queries := []struct {
		label string
		key   string
		value string
	}{
		{label: "trace_id", key: "trace_id", value: runID},
		{label: "request_id", key: "request_id", value: runID},
		{label: "operation_id", key: "operation_id", value: runID},
		{label: "home_mqtt_operation", key: "operation_id", value: "home-mqtt-loadtest"},
	}
	for _, query := range queries {
		count, err := queryCentralLoggerCount(endpoint, token, query.key, query.value)
		if err != nil {
			return EvidenceSource{Available: false, Optional: true, Detail: "central logger query failed: " + redact(err.Error())}, "central_logger evidence probe failed: " + err.Error()
		}
		counters["central_logger."+query.label+".events"] = int64(count)
	}
	return EvidenceSource{
		Available: true,
		Optional:  true,
		Detail:    "central logger /v1/logs queried by run_id trace_id/request_id/operation_id and home-mqtt-loadtest operation_id",
		Counters:  counters,
	}, ""
}

func centralLoggerEnvValues(envRoot string) map[string]string {
	candidates := []string{}
	explicitOverride := false
	if override := strings.TrimSpace(os.Getenv("HOME100K_CLOUD_LOGGER_ENV")); override != "" {
		candidates = append(candidates, override)
		explicitOverride = true
	}
	candidates = append(candidates,
		filepath.Join(envRoot, "services", "cloud-logger", "logger.env"),
		filepath.Join(envRoot, "state", "cloud-logger.env"),
	)
	if parent := filepath.Dir(strings.TrimRight(envRoot, string(os.PathSeparator))); parent != "." && parent != "" {
		candidates = append(candidates,
			filepath.Join(parent, "linode", "services", "cloud-logger", "logger.env"),
			filepath.Join(parent, "linode", "state", "cloud-logger.env"),
		)
	}
	values := map[string]string{}
	for _, path := range candidates {
		if fileReadable(path) {
			values = parseEnvFile(path)
			break
		}
	}
	stackValues := parseEnvFile(filepath.Join(envRoot, "env", "stack.env"))
	if values["CLOUD_LOGGER_DOMAIN"] == "" {
		values["CLOUD_LOGGER_DOMAIN"] = stackValues["CLOUD_LOGGER_DOMAIN"]
	}
	if values["CLOUD_LOGGER_ENDPOINT"] == "" && values["CLOUD_LOGGER_DOMAIN"] != "" {
		values["CLOUD_LOGGER_ENDPOINT"] = "https://" + values["CLOUD_LOGGER_DOMAIN"]
	}
	if !explicitOverride {
		if token := readTrimmedFile(filepath.Join(envRoot, "state", "secrets", "cloud-logger-ingest-token")); token != "" {
			values["CLOUD_LOGGER_INGEST_TOKEN"] = token
		}
	}
	return values
}

func centralLoggerEndpointAndToken(values map[string]string) (string, string) {
	endpoint := strings.TrimRight(strings.TrimSpace(firstNonEmpty(
		os.Getenv("HOME100K_CLOUD_LOGGER_ENDPOINT"),
		os.Getenv("CLOUD_LOGGER_ENDPOINT"),
		values["CLOUD_LOGGER_ENDPOINT"],
	)), "/")
	token := strings.TrimSpace(firstNonEmpty(
		os.Getenv("HOME100K_CLOUD_LOGGER_INGEST_TOKEN"),
		os.Getenv("CLOUD_LOGGER_INGEST_TOKEN"),
		values["CLOUD_LOGGER_INGEST_TOKEN"],
	))
	return endpoint, token
}

func readTrimmedFile(path string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(raw))
}

func evidenceWindowStart(outDir string) string {
	outDir = strings.TrimSpace(outDir)
	if outDir == "" {
		return ""
	}
	coordination := loadStartCoordination(filepath.Join(outDir, "start-coordination.json"))
	var earliest time.Time
	for _, vm := range coordination.VMs {
		for _, raw := range []string{vm.StartSignalReceivedAt, vm.StageStartedAt, vm.FirstConnectAt} {
			parsed, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(raw))
			if err != nil {
				continue
			}
			if earliest.IsZero() || parsed.Before(earliest) {
				earliest = parsed
			}
		}
	}
	if earliest.IsZero() {
		return ""
	}
	return earliest.Add(-5 * time.Second).UTC().Format(time.RFC3339Nano)
}

func queryCentralLoggerCount(endpoint string, token string, key string, value string) (int, error) {
	base, err := url.Parse(endpoint + "/v1/logs")
	if err != nil {
		return 0, err
	}
	values := base.Query()
	values.Set(key, value)
	base.RawQuery = values.Encode()
	req, err := http.NewRequest(http.MethodGet, base.String(), nil)
	if err != nil {
		return 0, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	var decoded struct {
		Events []json.RawMessage `json:"events"`
	}
	if err := doCentralLoggerJSON(req, 10*time.Second, &decoded); err != nil {
		return 0, err
	}
	return len(decoded.Events), nil
}

type centralLoggerRuntimeEvent struct {
	EventID   string         `json:"event_id"`
	Time      time.Time      `json:"ts"`
	Message   string         `json:"msg"`
	Source    string         `json:"source"`
	Component string         `json:"component"`
	Fields    map[string]any `json:"fields"`
}

func collectCentralLoggerRuntimeLogEvidence(envRoot string, runID string, windowStart string) (EvidenceSource, EvidenceSource, string) {
	if skipCentralLoggerEvidence() {
		return EvidenceSource{}, EvidenceSource{}, "central_logger runtime evidence probe skipped by HOME100K_SKIP_CENTRAL_LOGGER"
	}
	values := centralLoggerEnvValues(envRoot)
	endpoint, token := centralLoggerEndpointAndToken(values)
	if endpoint == "" || token == "" {
		return EvidenceSource{}, EvidenceSource{}, ""
	}
	since := time.Now().UTC().Add(-30 * time.Minute)
	if strings.TrimSpace(windowStart) != "" {
		parsed, err := time.Parse(time.RFC3339Nano, windowStart)
		if err != nil {
			return EvidenceSource{}, EvidenceSource{}, "central_logger runtime evidence skipped: invalid evidence window start " + redact(err.Error())
		}
		since = parsed.UTC()
	}
	until := time.Now().UTC()
	if until.Before(since) {
		until = since.Add(30 * time.Minute)
	}
	events, err := queryCentralLoggerRuntimeEventsWindowed(endpoint, token, since, until, 0)
	if err != nil {
		return EvidenceSource{}, EvidenceSource{}, "central_logger runtime evidence probe failed: " + err.Error()
	}
	shadowCounters, streamCounters := centralLoggerRuntimeCounters(runID, events)
	if len(shadowCounters) == 0 && len(streamCounters) == 0 {
		return EvidenceSource{}, EvidenceSource{}, "central_logger runtime evidence probe found no matching runtime log events for run_id " + runID
	}
	detail := "IoT Device Shadow runtime evidence parsed from central logger device_runtime_log events for run_id " + runID
	return EvidenceSource{Available: true, Detail: detail, Counters: shadowCounters},
		EvidenceSource{Available: true, Detail: detail, Counters: streamCounters},
		"central_logger runtime evidence used for iot_device_shadow and iot_device_shadow_streams"
}

func skipCentralLoggerEvidence() bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv("HOME100K_SKIP_CENTRAL_LOGGER")))
	return raw == "1" || raw == "true" || raw == "yes"
}

func queryCentralLoggerRuntimeEventsWindowed(endpoint string, token string, since time.Time, until time.Time, depth int) ([]centralLoggerRuntimeEvent, error) {
	events, err := queryCentralLoggerRuntimeEvents(endpoint, token, since, until)
	if err != nil {
		if depth >= 20 || until.Sub(since) <= time.Second {
			return nil, err
		}
		mid := since.Add(until.Sub(since) / 2)
		left, leftErr := queryCentralLoggerRuntimeEventsWindowed(endpoint, token, since, mid, depth+1)
		if leftErr != nil {
			return nil, leftErr
		}
		right, rightErr := queryCentralLoggerRuntimeEventsWindowed(endpoint, token, mid, until, depth+1)
		if rightErr != nil {
			return nil, rightErr
		}
		return dedupeCentralLoggerRuntimeEvents(append(left, right...)), nil
	}
	if len(events) < 1000 || depth >= 20 || until.Sub(since) <= time.Second {
		return dedupeCentralLoggerRuntimeEvents(events), nil
	}
	mid := since.Add(until.Sub(since) / 2)
	left, err := queryCentralLoggerRuntimeEventsWindowed(endpoint, token, since, mid, depth+1)
	if err != nil {
		return nil, err
	}
	right, err := queryCentralLoggerRuntimeEventsWindowed(endpoint, token, mid, until, depth+1)
	if err != nil {
		return nil, err
	}
	return dedupeCentralLoggerRuntimeEvents(append(left, right...)), nil
}

func queryCentralLoggerRuntimeEvents(endpoint string, token string, since time.Time, until time.Time) ([]centralLoggerRuntimeEvent, error) {
	base, err := url.Parse(endpoint + "/v1/logs")
	if err != nil {
		return nil, err
	}
	values := base.Query()
	values.Set("component", "device_runtime_log")
	values.Set("source", "device-runtime")
	values.Set("limit", "1000")
	values.Set("order", "asc")
	values.Set("since", since.UTC().Format(time.RFC3339Nano))
	values.Set("until", until.UTC().Format(time.RFC3339Nano))
	base.RawQuery = values.Encode()
	req, err := http.NewRequest(http.MethodGet, base.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	var decoded struct {
		Events []centralLoggerRuntimeEvent `json:"events"`
	}
	if err := doCentralLoggerJSON(req, 15*time.Second, &decoded); err != nil {
		return nil, err
	}
	return decoded.Events, nil
}

func doCentralLoggerJSON(req *http.Request, timeout time.Duration, out any) error {
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		clone := req.Clone(req.Context())
		client := http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				DisableKeepAlives: true,
				ForceAttemptHTTP2: false,
			},
		}
		resp, err := client.Do(clone)
		if err != nil {
			lastErr = err
		} else {
			body, readErr := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			if resp.StatusCode < 200 || resp.StatusCode >= 300 {
				return fmt.Errorf("logger query status=%d", resp.StatusCode)
			}
			if readErr != nil {
				lastErr = readErr
			} else if err := json.Unmarshal(body, out); err != nil {
				lastErr = err
			} else {
				return nil
			}
		}
		time.Sleep(time.Duration(attempt+1) * 250 * time.Millisecond)
	}
	return lastErr
}

func dedupeCentralLoggerRuntimeEvents(events []centralLoggerRuntimeEvent) []centralLoggerRuntimeEvent {
	if len(events) == 0 {
		return nil
	}
	seen := map[string]struct{}{}
	out := make([]centralLoggerRuntimeEvent, 0, len(events))
	for _, event := range events {
		key := strings.TrimSpace(event.EventID)
		if key == "" {
			key = event.Time.UTC().Format(time.RFC3339Nano) + "\x00" + event.Message + "\x00" + centralLoggerEventFieldString(event, "stream_id") + "\x00" + centralLoggerEventFieldString(event, "seq")
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, event)
	}
	return out
}

func centralLoggerRuntimeCounters(runID string, events []centralLoggerRuntimeEvent) (map[string]int64, map[string]int64) {
	prefix := "mqtt-e2e-" + sanitizeEvidenceRunID(runID) + "-"
	shadowCounters := map[string]int64{}
	streamEntries := map[string]int64{}
	streamSeqEntries := map[string]map[string]int64{}
	for _, event := range dedupeCentralLoggerRuntimeEvents(events) {
		if event.Component != "" && event.Component != "device_runtime_log" {
			continue
		}
		if event.Source != "" && event.Source != "device-runtime" {
			continue
		}
		streamID := centralLoggerEventFieldString(event, "stream_id")
		if !strings.HasPrefix(streamID, prefix) {
			continue
		}
		source := centralLoggerEventFieldString(event, "source")
		switch {
		case source == "app_controller" && event.Message == "mqtt_e2e shadow_desired app_controller publish":
			shadowCounters["app_user.desired_writes"]++
		case source == "device_client" && event.Message == "mqtt_e2e shadow_delta device_client receive":
			shadowCounters["device_mqtt.delta_received"]++
		case source == "device_client" && event.Message == "mqtt_e2e shadow_reported device_client publish":
			shadowCounters["device_mqtt.reported_publishes"]++
		case source == "app_observer" && event.Message == "mqtt_e2e shadow_reported app_observer receive":
			shadowCounters["app_user.received_acks"]++
		}
		streamEntries[streamID]++
		seq := centralLoggerEventFieldString(event, "seq")
		if seq == "" {
			continue
		}
		if streamSeqEntries[streamID] == nil {
			streamSeqEntries[streamID] = map[string]int64{}
		}
		streamSeqEntries[streamID][seq]++
	}
	streamCounters := map[string]int64{}
	if len(streamEntries) > 0 {
		streamCounters["runtime_log_streams.total"] = int64(len(streamEntries))
	}
	for streamID, entries := range streamEntries {
		streamCounters["runtime_log_stream."+streamID+".entries"] = entries
		for seq, count := range streamSeqEntries[streamID] {
			streamCounters["runtime_log_stream."+streamID+".seq."+seq] = count
		}
	}
	if len(shadowCounters) == 0 {
		shadowCounters = nil
	}
	if len(streamCounters) == 0 {
		streamCounters = nil
	}
	return shadowCounters, streamCounters
}

func centralLoggerEventFieldString(event centralLoggerRuntimeEvent, key string) string {
	if event.Fields == nil {
		return ""
	}
	value, ok := event.Fields[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	case json.Number:
		return typed.String()
	default:
		return strings.TrimSpace(fmt.Sprint(typed))
	}
}

func fileReadable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func parseEvidenceCounters(source string, runID string, out string) map[string]int64 {
	counters := map[string]int64{}
	if source == "emqx_listener_stats" && (strings.Contains(out, "tcp:default") || strings.Contains(out, "ssl:default")) {
		counters["emqx.broker.identity"] = 1
	}
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
		if source == "emqx_listener_stats" {
			parseEMQXListenerCounterLine(counters, line)
		}
		if source == "video_cloud_api" {
			parseVideoCloudAPILogCounterLine(counters, line)
		}
		if source == "ingress_nginx" {
			parseIngressRequestTokenCounterLine(counters, line)
		}
		if source == "postgres" {
			parsePostgresCounterLine(counters, line)
		}
		if source == "redis_valkey" {
			parseRedisInfoCounterLine(counters, line)
		}
	}
	if len(counters) == 0 {
		return nil
	}
	return counters
}

func parseRedisInfoCounterLine(counters map[string]int64, line string) {
	line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
	if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "pod:") {
		return
	}
	key, value, ok := strings.Cut(line, ":")
	if !ok {
		return
	}
	key = strings.TrimSpace(key)
	value = strings.TrimSpace(value)
	switch key {
	case "total_commands_processed", "keyspace_hits", "keyspace_misses", "connected_clients", "used_memory", "used_memory_peak", "expired_keys", "evicted_keys":
		if parsed, err := strconv.ParseInt(value, 10, 64); err == nil {
			counters["redis_valkey."+key] += parsed
		}
	default:
		if strings.HasPrefix(key, "db") {
			for _, part := range strings.Split(value, ",") {
				name, raw, ok := strings.Cut(part, "=")
				if !ok {
					continue
				}
				if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
					counters["redis_valkey.keyspace."+key+"."+name] += parsed
				}
			}
			return
		}
		if strings.HasPrefix(key, "cmdstat_") {
			command := strings.TrimPrefix(key, "cmdstat_")
			for _, part := range strings.Split(value, ",") {
				name, raw, ok := strings.Cut(part, "=")
				if !ok || name != "calls" {
					continue
				}
				if parsed, err := strconv.ParseInt(raw, 10, 64); err == nil {
					counters["redis_valkey.command."+command+".calls"] += parsed
				}
			}
		}
	}
}

func applyServerEvidenceBaselineDeltas(evidence *ServerEvidence, runID string, outDir string, notes *[]string) {
	if evidence == nil || strings.TrimSpace(outDir) == "" {
		return
	}
	baselinePath := filepath.Join(outDir, "server-evidence-baseline.json")
	if !fileReadable(baselinePath) {
		return
	}
	baseline, err := loadServerEvidence(baselinePath, runID)
	if err != nil {
		if notes != nil {
			*notes = append(*notes, "server evidence baseline ignored: "+err.Error())
		}
		return
	}
	applyEMQXMetricDelta(evidence, baseline)
	applySourceCounterBaselineDelta(evidence, baseline, "ingress_nginx")
	applySourceCounterBaselineDelta(evidence, baseline, "postgres")
	applySourceCounterBaselineDelta(evidence, baseline, "redis_valkey")
	applySourceCounterBaselineDelta(evidence, baseline, "video_cloud_api")
	recomputeVideoCloudAPITopLevelCounters(evidence)
}

func applyEMQXMetricDelta(evidence *ServerEvidence, baseline ServerEvidence) {
	if evidence == nil || evidence.Sources == nil {
		return
	}
	source := evidence.Sources["emqx"]
	if source.Counters == nil {
		source.Counters = map[string]int64{}
	}
	deltaConnected := source.Counters["emqx.metric.client.connected"] - evidenceCounter(baseline, "emqx", "emqx.metric.client.connected")
	if deltaConnected > 0 {
		source.Counters["mqtt.total_connect_success"] = deltaConnected
		source.Counters["device_mqtt.connect_success"] = deltaConnected
	}
	deltaConnectReceived := source.Counters["emqx.metric.packets.connect.received"] - evidenceCounter(baseline, "emqx", "emqx.metric.packets.connect.received")
	if deltaConnectReceived > 0 {
		source.Counters["mqtt.total_connect_attempts"] = deltaConnectReceived
		source.Counters["device_mqtt.connect_attempts"] = deltaConnectReceived
	}
	evidence.Sources["emqx"] = source
}

func applySourceCounterBaselineDelta(evidence *ServerEvidence, baseline ServerEvidence, sourceName string) {
	if evidence == nil || evidence.Sources == nil || strings.TrimSpace(sourceName) == "" {
		return
	}
	source, ok := evidence.Sources[sourceName]
	if !ok || source.Counters == nil {
		return
	}
	baselineSource, ok := baseline.Sources[sourceName]
	if !ok || baselineSource.Counters == nil {
		return
	}
	for counter, value := range source.Counters {
		base, ok := baselineSource.Counters[counter]
		if !ok {
			continue
		}
		delta := value - base
		if delta < 0 {
			delta = 0
		}
		source.Counters[counter] = delta
	}
	evidence.Sources[sourceName] = source
}

func recomputeVideoCloudAPITopLevelCounters(evidence *ServerEvidence) {
	if evidence == nil || evidence.Sources == nil {
		return
	}
	source, ok := evidence.Sources["video_cloud_api"]
	if !ok || source.Counters == nil {
		return
	}
	fields := []string{"total", "status_200", "status_500", "gt1s", "gt5s", "gt10s"}
	sums := map[string]int64{}
	foundPodCounters := false
	for counter, value := range source.Counters {
		if !strings.HasPrefix(counter, "video_cloud_api.request_token.pod_") {
			continue
		}
		for _, field := range fields {
			if strings.HasSuffix(counter, "."+field) {
				sums[field] += value
				foundPodCounters = true
				break
			}
		}
	}
	if !foundPodCounters {
		return
	}
	for _, field := range fields {
		source.Counters["video_cloud_api.request_token."+field] = sums[field]
	}
	evidence.Sources["video_cloud_api"] = source
}

func parsePostgresCounterLine(counters map[string]int64, line string) {
	lower := strings.ToLower(line)
	if strings.Contains(lower, "fatal:") {
		counters["postgres.fatal"]++
	}
	if strings.Contains(lower, "too many clients already") {
		counters["postgres.too_many_clients"]++
	}
	if strings.Contains(lower, "canceling statement due to user request") {
		counters["postgres.statement_canceled"]++
	}
}

func parseEMQXListenerCounterLine(counters map[string]int64, line string) {
	fields := strings.Fields(line)
	if len(fields) != 3 {
		return
	}
	listener := strings.ReplaceAll(fields[0], ":", "_")
	key := strings.TrimSuffix(fields[1], ":")
	value, err := strconv.ParseInt(fields[2], 10, 64)
	if err != nil {
		return
	}
	switch key {
	case "acceptors", "current_conn", "max_conns":
		counters["emqx."+listener+"."+key] += value
	case "ssl_closed", "tcp_closed", "discarded":
		counters["emqx."+listener+".shutdown_"+key] += value
	}
}

func parseVideoCloudAPILogCounterLine(counters map[string]int64, line string) {
	jsonStart := strings.Index(line, "{")
	if jsonStart > 0 {
		line = line[jsonStart:]
	}
	var entry struct {
		Message         string          `json:"msg"`
		LegacyMessage   string          `json:"message"`
		Path            string          `json:"path"`
		Status          int             `json:"status"`
		DurationMS      float64         `json:"duration_ms"`
		SubscriberRole  string          `json:"subscriber_role"`
		TopicClass      string          `json:"topic_class"`
		QueueLen        int64           `json:"queue_len"`
		QueueCap        int64           `json:"queue_cap"`
		Duration        json.RawMessage `json:"duration"`
		HandlerDuration json.RawMessage `json:"handler_duration"`
		TotalDuration   json.RawMessage `json:"total_duration"`
	}
	if err := json.Unmarshal([]byte(line), &entry); err != nil {
		return
	}
	message := firstNonEmpty(entry.Message, entry.LegacyMessage)
	switch message {
	case "mqtt inbound handler queue pressure":
		role := videoCloudMQTTRoleKey(entry.SubscriberRole)
		counters[fmt.Sprintf("video_cloud_api.mqtt.%s.queue_pressure", role)]++
		if entry.QueueCap > 0 && entry.QueueLen >= entry.QueueCap {
			counters[fmt.Sprintf("video_cloud_api.mqtt.%s.queue_full", role)]++
		}
		if entry.QueueLen > counters[fmt.Sprintf("video_cloud_api.mqtt.%s.max_queue_len", role)] {
			counters[fmt.Sprintf("video_cloud_api.mqtt.%s.max_queue_len", role)] = entry.QueueLen
		}
		if entry.QueueCap > counters[fmt.Sprintf("video_cloud_api.mqtt.%s.max_queue_cap", role)] {
			counters[fmt.Sprintf("video_cloud_api.mqtt.%s.max_queue_cap", role)] = entry.QueueCap
		}
		if topicClass := videoCloudMQTTRoleKey(entry.TopicClass); topicClass != "unknown" {
			counters[fmt.Sprintf("video_cloud_api.mqtt.%s.%s.queue_pressure", role, topicClass)]++
		}
	case "mqtt inbound handler slow":
		role := videoCloudMQTTRoleKey(entry.SubscriberRole)
		counters[fmt.Sprintf("video_cloud_api.mqtt.%s.handler_slow", role)]++
		handlerMS := parseVideoCloudAPIDurationMS(entry.HandlerDuration)
		totalMS := parseVideoCloudAPIDurationMS(entry.TotalDuration)
		if handlerMS > counters[fmt.Sprintf("video_cloud_api.mqtt.%s.max_handler_ms", role)] {
			counters[fmt.Sprintf("video_cloud_api.mqtt.%s.max_handler_ms", role)] = handlerMS
		}
		if totalMS > counters[fmt.Sprintf("video_cloud_api.mqtt.%s.max_total_ms", role)] {
			counters[fmt.Sprintf("video_cloud_api.mqtt.%s.max_total_ms", role)] = totalMS
		}
	case "mqtt shadow request slow":
		counters["video_cloud_api.mqtt.shadow_request_slow"]++
		durationMS := parseVideoCloudAPIDurationMS(entry.Duration)
		if durationMS > counters["video_cloud_api.mqtt.shadow_request.max_ms"] {
			counters["video_cloud_api.mqtt.shadow_request.max_ms"] = durationMS
		}
	}
	if entry.Path != "/request_token" {
		return
	}
	counters["video_cloud_api.request_token.total"]++
	counters[fmt.Sprintf("video_cloud_api.request_token.status_%d", entry.Status)]++
	duration := int64(entry.DurationMS)
	if duration > counters["video_cloud_api.request_token.max_ms"] {
		counters["video_cloud_api.request_token.max_ms"] = duration
	}
	if entry.DurationMS > 1000 {
		counters["video_cloud_api.request_token.gt1s"]++
	}
	if entry.DurationMS > 5000 {
		counters["video_cloud_api.request_token.gt5s"]++
	}
	if entry.DurationMS > 10000 {
		counters["video_cloud_api.request_token.gt10s"]++
	}
}

func videoCloudMQTTRoleKey(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.ReplaceAll(value, "-", "_")
	if value == "" {
		return "unknown"
	}
	return value
}

func parseVideoCloudAPIDurationMS(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var seconds float64
	if err := json.Unmarshal(raw, &seconds); err == nil {
		if seconds > 1000000 {
			return int64(seconds / 1000000)
		}
		return int64(seconds * 1000)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil || strings.TrimSpace(text) == "" {
		return 0
	}
	duration, err := time.ParseDuration(text)
	if err == nil {
		return duration.Milliseconds()
	}
	parsed, err := strconv.ParseFloat(strings.TrimSuffix(strings.TrimSpace(text), "s"), 64)
	if err != nil {
		return 0
	}
	return int64(parsed * 1000)
}

var ingressAccessLogPattern = regexp.MustCompile(`\[[0-9]{2}/[A-Za-z]+/[0-9]{4}:[0-9]{2}:[0-9]{2}:[0-9]{2} \+0000\] "([A-Z]+) ([^ ]+) [^"]+" ([0-9]{3}) [0-9]+ "[^"]*" "[^"]*" [0-9]+ ([0-9.]+) \[[^\]]*\] \[\] [^ ]+ [^ ]+ ([0-9.]+|-|,) ([0-9]{3}|-)`)

func parseIngressRequestTokenCounterLine(counters map[string]int64, line string) {
	matches := ingressAccessLogPattern.FindStringSubmatch(line)
	if len(matches) != 7 {
		return
	}
	if matches[1] != http.MethodPost || matches[2] != "/request_token" {
		return
	}
	status, err := strconv.Atoi(matches[3])
	if err != nil {
		return
	}
	requestTime, err := strconv.ParseFloat(matches[4], 64)
	if err != nil {
		return
	}
	counters["ingress_nginx.request_token.total"]++
	counters[fmt.Sprintf("ingress_nginx.request_token.status_%d", status)]++
	ms := int64(requestTime * 1000)
	if ms > counters["ingress_nginx.request_token.max_ms"] {
		counters["ingress_nginx.request_token.max_ms"] = ms
	}
	if requestTime > 1 {
		counters["ingress_nginx.request_token.gt1s"]++
	}
	if requestTime > 5 {
		counters["ingress_nginx.request_token.gt5s"]++
	}
	if requestTime > 10 {
		counters["ingress_nginx.request_token.gt10s"]++
	}
	if upstreamStatus, err := strconv.Atoi(matches[6]); err == nil {
		counters[fmt.Sprintf("ingress_nginx.request_token.upstream_%d", upstreamStatus)]++
	}
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

func parseEvidenceSamples(source string, out string) []EvidenceResourceSample {
	if source != "host_pod_resources" {
		return nil
	}
	samples := []EvidenceResourceSample{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		if strings.EqualFold(fields[0], "NAMESPACE") {
			continue
		}
		cpu, ok := parseCPUMillicores(fields[2])
		if !ok {
			continue
		}
		memory, ok := parseMemoryBytes(fields[3])
		if !ok {
			continue
		}
		samples = append(samples, EvidenceResourceSample{
			Kind:        "k8s_pod_top",
			Namespace:   fields[0],
			Pod:         fields[1],
			CPUCoreMil:  cpu,
			MemoryBytes: memory,
		})
	}
	return samples
}

func parseCPUMillicores(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	switch {
	case strings.HasSuffix(value, "m"):
		parsed, err := strconv.ParseInt(strings.TrimSuffix(value, "m"), 10, 64)
		return parsed, err == nil
	case strings.HasSuffix(value, "u"):
		parsed, err := strconv.ParseInt(strings.TrimSuffix(value, "u"), 10, 64)
		if err != nil {
			return 0, false
		}
		return (parsed + 999) / 1000, true
	case strings.HasSuffix(value, "n"):
		parsed, err := strconv.ParseInt(strings.TrimSuffix(value, "n"), 10, 64)
		if err != nil {
			return 0, false
		}
		return (parsed + 999999) / 1000000, true
	default:
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return 0, false
		}
		return int64(parsed * 1000), true
	}
}

func parseMemoryBytes(value string) (int64, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	units := []struct {
		suffix string
		scale  int64
	}{
		{"Ki", 1024},
		{"Mi", 1024 * 1024},
		{"Gi", 1024 * 1024 * 1024},
		{"Ti", 1024 * 1024 * 1024 * 1024},
		{"K", 1000},
		{"M", 1000 * 1000},
		{"G", 1000 * 1000 * 1000},
		{"T", 1000 * 1000 * 1000 * 1000},
	}
	for _, unit := range units {
		if strings.HasSuffix(value, unit.suffix) {
			parsed, err := strconv.ParseFloat(strings.TrimSuffix(value, unit.suffix), 64)
			if err != nil {
				return 0, false
			}
			return int64(parsed * float64(unit.scale)), true
		}
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	return parsed, err == nil
}

func mergeEvidenceSource(current EvidenceSource, next EvidenceSource) EvidenceSource {
	if current.Detail == "" {
		return next
	}
	available := current.Available && next.Available
	if evidenceSourceHasData(current) && !next.Available && !evidenceSourceHasData(next) {
		available = true
	}
	if evidenceSourceHasData(next) && !current.Available && !evidenceSourceHasData(current) {
		available = true
	}
	merged := EvidenceSource{
		Available: available,
		Optional:  current.Optional || next.Optional,
		Detail:    current.Detail + "; " + next.Detail,
		Counters:  map[string]int64{},
		Samples:   append(append([]EvidenceResourceSample{}, current.Samples...), next.Samples...),
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
	if len(merged.Samples) == 0 {
		merged.Samples = nil
	}
	return merged
}

func evidenceSourceHasData(source EvidenceSource) bool {
	return len(source.Counters) > 0 || len(source.Samples) > 0
}

type serverEvidenceProbe struct {
	source  string
	command string
	args    []string
	detail  string
	timeout time.Duration
}

const (
	defaultServerEvidenceProbeTimeout = 60 * time.Second
	serverEvidenceLogTailLines        = "120000"
)

func serverEvidenceProbes(envRoot string, runID string, logsSinceArg string) []serverEvidenceProbe {
	if strings.TrimSpace(logsSinceArg) == "" {
		logsSinceArg = "--since=30m"
	}
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
		kubectlLogsProbe("emqx", "video-cloud-staging-video-cloud", "app.kubernetes.io/name=mqtt", logsSinceArg, "MQTT broker logs and client churn evidence captured for run_id "+runID),
		emqxBrokerMetricsProbe(runID),
		emqxListenerStatsProbe(runID),
		edgeHAProxyProbe(envRoot, runID),
		videoCloudAPIRequestTokenCounterProbe(runID, logsSinceArg),
		videoCloudAPIMetricsProbe(runID),
		kubectlLogsProbe("video_cloud_api", "video-cloud-staging-video-cloud", "app.kubernetes.io/name=video-cloud-api", logsSinceArg, "Video Cloud API logs captured for run_id "+runID),
		postgresCounterProbe("postgres", runID, shadowStoreCounterSQL(runID), "PostgreSQL device shadow convergence counters parsed for run_id "+runID),
		kubectlLogsProbe("postgres", "video-cloud-staging-platform", "app.kubernetes.io/name=postgresql", logsSinceArg, "PostgreSQL logs captured"),
		redisInfoProbe(runID),
		kubectlLogsProbe("redis_valkey", "video-cloud-staging-platform", "app.kubernetes.io/name=redis", logsSinceArg, "Redis/Valkey logs captured when enabled"),
		kubectlLogsProbe("ingress_nginx", "video-cloud-staging-ingress", "app.kubernetes.io/name=ingress-nginx", logsSinceArg, "Ingress/nginx logs captured for run_id "+runID),
	}
}

func postgresCounterProbe(source string, runID string, sql string, detail string) serverEvidenceProbe {
	script := fmt.Sprintf(
		`set -euo pipefail; kubectl -n video-cloud-staging-platform exec postgresql-0 -- psql -U postgres -d video_cloud -At -F '	' -c %s`,
		shellQuote(sql),
	)
	return serverEvidenceProbe{source: source, command: "bash", args: []string{"-lc", script}, detail: detail}
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

func redisInfoProbe(runID string) serverEvidenceProbe {
	script := `set -euo pipefail
pods="$(kubectl -n video-cloud-staging-platform get pods --selector app.kubernetes.io/name=redis -o jsonpath='{range .items[?(@.status.phase=="Running")]}{.metadata.name}{"\n"}{end}')"
test -n "$pods"
for pod in $pods; do
  echo "pod:$pod"
  kubectl -n video-cloud-staging-platform exec "$pod" -- redis-cli INFO stats clients memory keyspace commandstats
done`
	return serverEvidenceProbe{
		source:  "redis_valkey",
		command: "bash",
		args:    []string{"-lc", script},
		detail:  "Redis/Valkey INFO counters captured for run_id " + runID,
	}
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
func kubectlLogsProbe(source string, namespace string, selector string, logsSinceArg string, detail string) serverEvidenceProbe {
	script := fmt.Sprintf(
		`set -euo pipefail; pods="$(kubectl -n %s get pods --selector %s -o name)"; test -n "$pods"; timeout 20s kubectl -n %s logs %s --selector %s --tail=%s || true`,
		shellQuote(namespace),
		shellQuote(selector),
		shellQuote(namespace),
		shellQuote(logsSinceArg),
		shellQuote(selector),
		serverEvidenceLogTailLines,
	)
	return serverEvidenceProbe{
		source:  source,
		command: "bash",
		args:    []string{"-lc", script},
		detail:  detail,
	}
}

func emqxListenerStatsProbe(runID string) serverEvidenceProbe {
	script := `set -euo pipefail
pods="$(kubectl -n video-cloud-staging-video-cloud get pods --selector app.kubernetes.io/name=mqtt -o jsonpath='{range .items[?(@.status.phase=="Running")]}{.metadata.name}{"\n"}{end}')"
test -n "$pods"
for pod in $pods; do
  safe_pod="$(printf '%s' "$pod" | tr -c 'A-Za-z0-9_' '_')"
  listener_out="$(kubectl -n video-cloud-staging-video-cloud exec "$pod" -- emqx ctl listeners 2>&1 || true)"
  if [ -z "$listener_out" ]; then
    continue
  fi
  printf '%s\n' "$listener_out" | awk -v pod="$safe_pod" '
    /^[a-z]+:default/ {listener=$1}
    /^[[:space:]]+(acceptors|current_conn|max_conns)[[:space:]]*:/ {
      key=$1; value=$3; safe_listener=listener; gsub(":", "_", safe_listener)
      print listener, key, value; print "emqx.pod_" pod "." safe_listener "." key, value
    }
    /^[[:space:]]+shutdown_count[[:space:]]*:/ {
      safe_listener=listener; gsub(":", "_", safe_listener)
      if ($0 ~ /ssl_closed/) {s=$0; sub(/^.*ssl_closed,/, "", s); sub(/[^0-9].*$/, "", s); if (s != "") {print listener, "ssl_closed", s; print "emqx.pod_" pod "." safe_listener ".shutdown_ssl_closed", s}}
      if ($0 ~ /tcp_closed/) {s=$0; sub(/^.*tcp_closed,/, "", s); sub(/[^0-9].*$/, "", s); if (s != "") {print listener, "tcp_closed", s; print "emqx.pod_" pod "." safe_listener ".shutdown_tcp_closed", s}}
      if ($0 ~ /discarded/) {s=$0; sub(/^.*discarded,/, "", s); sub(/[^0-9].*$/, "", s); if (s != "") {print listener, "discarded", s; print "emqx.pod_" pod "." safe_listener ".shutdown_discarded", s}}
    }
  '
done`
	return serverEvidenceProbe{
		source:  "emqx_listener_stats",
		command: "bash",
		args:    []string{"-lc", script},
		detail:  "EMQX listener acceptor, current connection, max connection, and shutdown counters captured for run_id " + runID,
	}
}

func emqxBrokerMetricsProbe(runID string) serverEvidenceProbe {
	script := `set -euo pipefail
pods="$(kubectl -n video-cloud-staging-video-cloud get pods --selector app.kubernetes.io/name=mqtt -o jsonpath='{range .items[?(@.status.phase=="Running")]}{.metadata.name}{"\n"}{end}')"
test -n "$pods"
for pod in $pods; do
  safe_pod="$(printf '%s' "$pod" | tr -c 'A-Za-z0-9_' '_')"
  metrics="$(kubectl -n video-cloud-staging-video-cloud exec "$pod" -- emqx ctl broker metrics 2>/dev/null || true)"
  test -n "$metrics"
  printf '%s\n' "$metrics" | awk -F ':' -v pod="$safe_pod" '
    {
      key=$1; value=$2
      gsub(/[[:space:]]/, "", key)
      gsub(/[[:space:]]/, "", value)
    }
    key ~ /^(client\.connected|client\.connack|packets\.connect\.received|packets\.connack\.sent|packets\.pingreq\.received|packets\.pingresp\.sent)$/ && value ~ /^[0-9]+$/ {
      print "emqx.metric." key, value
      print "emqx.pod_" pod ".metric." key, value
    }
  '
done`
	return serverEvidenceProbe{
		source:  "emqx",
		command: "bash",
		args:    []string{"-lc", script},
		detail:  "EMQX broker metrics captured for run_id " + runID + "; run-scoped connect counters require server-evidence-baseline.json delta",
	}
}

func edgeHAProxyProbe(envRoot string, runID string) serverEvidenceProbe {
	artifact := filepath.Join(envRoot, "artifacts", "edge-haproxy", "edge-vms.json")
	script := strings.ReplaceAll(`set -euo pipefail
artifact=__EDGE_HAPROXY_ARTIFACT__
test -r "$artifact"
ip="$(jq -r '.edge_vms[0].public_ip // ""' "$artifact")"
user="$(jq -r '.ssh_access.user // "root"' "$artifact")"
key="$(jq -r '.ssh_access.key_path // ""' "$artifact")"
test -n "$ip"
test -n "$key"
ssh -i "$key" -o IdentitiesOnly=yes -o BatchMode=yes -o ConnectTimeout=15 -o StrictHostKeyChecking=accept-new -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR "$user@$ip" 'bash -s' <<'REMOTE'
set -euo pipefail
pids="$(pgrep -x haproxy || true)"
printf 'edge_haproxy.ssh_ok 1\n'
printf 'edge_haproxy.vm_count 1\n'
printf 'edge_haproxy.process.count %s\n' "$(printf '%s\n' "$pids" | awk 'NF{c++} END{print c+0}')"
fd_total=0
rss_total=0
for pid in $pids; do
  if [ -d "/proc/$pid/fd" ]; then
    fd="$(find "/proc/$pid/fd" -maxdepth 1 -type l 2>/dev/null | wc -l | tr -d ' ')"
    fd_total=$((fd_total + fd))
  fi
  if [ -r "/proc/$pid/status" ]; then
    rss="$(awk '/^VmRSS:/ {print $2}' "/proc/$pid/status")"
    rss_total=$((rss_total + ${rss:-0}))
  fi
done
printf 'edge_haproxy.process.fd_count %s\n' "$fd_total"
printf 'edge_haproxy.process.rss_kb %s\n' "$rss_total"
ss -Htan state established '( sport = :443 or dport = :443 )' 2>/dev/null | awk 'END{print "edge_haproxy.tcp.established_443", NR+0}'
ss -Htan state established '( sport = :8883 or dport = :8883 )' 2>/dev/null | awk 'END{print "edge_haproxy.tcp.established_8883", NR+0}'
limit="$(systemctl show haproxy -p LimitNOFILE --value 2>/dev/null || true)"
case "$limit" in ''|*[!0-9]*) ;; *) printf 'edge_haproxy.systemd.limit_nofile %s\n' "$limit" ;; esac
REMOTE
`, "__EDGE_HAPROXY_ARTIFACT__", shellQuote(artifact))
	return serverEvidenceProbe{
		source:  "edge_haproxy",
		command: "bash",
		args:    []string{"-lc", script},
		detail:  "External HAProxy edge VM process, socket, and systemd limit evidence captured for run_id " + runID,
	}
}

func videoCloudAPIRequestTokenCounterProbe(runID string, logsSinceArg string) serverEvidenceProbe {
	script := `set -euo pipefail
pods="$(kubectl -n video-cloud-staging-video-cloud get pods --selector app.kubernetes.io/name=video-cloud-api -o jsonpath='{range .items[?(@.status.phase=="Running")]}{.metadata.name}{"\n"}{end}')"
test -n "$pods"
bounded_logs() {
  pod="$1"
  tmp="$(mktemp)"
  kubectl -n video-cloud-staging-video-cloud logs ` + shellQuote(logsSinceArg) + ` "$pod" --tail=5000 --request-timeout=30s >"$tmp" 2>&1 &
  pid="$!"
  for _ in $(seq 1 30); do
    if ! kill -0 "$pid" 2>/dev/null; then
      wait "$pid" 2>/dev/null || true
      cat "$tmp"
      rm -f "$tmp"
      return 0
    fi
    sleep 1
  done
  kill "$pid" 2>/dev/null || true
  wait "$pid" 2>/dev/null || true
  cat "$tmp"
  rm -f "$tmp"
}
for pod in $pods; do
  safe_pod="$(printf '%s' "$pod" | tr -c 'A-Za-z0-9_' '_')"
  { timeout 20s kubectl -n video-cloud-staging-video-cloud logs ` + shellQuote(logsSinceArg) + ` "$pod" --tail=` + serverEvidenceLogTailLines + ` || true; } \
    | jq -sr --arg pod "$safe_pod" '
        [.[] | select(.path == "/request_token")] as $rt
        | [
            "video_cloud_api.request_token.total \($rt | length)",
            "video_cloud_api.request_token.status_200 \(($rt | map(select(.status == 200)) | length))",
            "video_cloud_api.request_token.status_500 \(($rt | map(select(.status == 500)) | length))",
            "video_cloud_api.request_token.gt1s \(($rt | map(select(.duration_ms > 1000)) | length))",
            "video_cloud_api.request_token.gt5s \(($rt | map(select(.duration_ms > 5000)) | length))",
            "video_cloud_api.request_token.gt10s \(($rt | map(select(.duration_ms > 10000)) | length))",
            "video_cloud_api.request_token.pod_\($pod).total \($rt | length)",
            "video_cloud_api.request_token.pod_\($pod).status_200 \(($rt | map(select(.status == 200)) | length))",
            "video_cloud_api.request_token.pod_\($pod).status_500 \(($rt | map(select(.status == 500)) | length))",
            "video_cloud_api.request_token.pod_\($pod).gt1s \(($rt | map(select(.duration_ms > 1000)) | length))",
            "video_cloud_api.request_token.pod_\($pod).gt5s \(($rt | map(select(.duration_ms > 5000)) | length))",
            "video_cloud_api.request_token.pod_\($pod).gt10s \(($rt | map(select(.duration_ms > 10000)) | length))"
          ]
        | .[]'
done
`
	return serverEvidenceProbe{
		source:  "video_cloud_api",
		command: "bash",
		args:    []string{"-lc", script},
		detail:  "Video Cloud API /request_token counters parsed from logs for run_id " + runID,
	}
}

func videoCloudAPIMetricsProbe(runID string) serverEvidenceProbe {
	script := `set -euo pipefail
pods="$(kubectl -n video-cloud-staging-video-cloud get pods --selector app.kubernetes.io/name=video-cloud-api -o jsonpath='{range .items[?(@.status.phase=="Running")]}{.metadata.name}{"\n"}{end}')"
test -n "$pods"
for pod in $pods; do
  safe_pod="$(printf '%s' "$pod" | tr -c 'A-Za-z0-9_' '_')"
  metrics="$(kubectl -n video-cloud-staging-video-cloud exec "$pod" -- sh -c 'wget -qO- http://127.0.0.1:8080/metrics/prometheus 2>/dev/null || curl -fsS http://127.0.0.1:8080/metrics/prometheus' 2>/dev/null || true)"
  test -n "$metrics"
  printf '%s\n' "$metrics" | awk -v pod="$safe_pod" '
    /^request_token_step_duration_seconds_/ || /^pkcs11_sign_(wait|duration)_seconds_/ {
      name=$1; value=$2
      gsub(/\{.*\}/, "", name)
      gsub(/[^A-Za-z0-9_]/, "_", name)
      if (value ~ /^[0-9.]+$/) {
        if (name ~ /_count$/) {
          printf "video_cloud_api.metrics.%s %.0f\n", name, value
          printf "video_cloud_api.metrics.pod_%s.%s %.0f\n", pod, name, value
        } else if (name ~ /_max$/) {
          printf "video_cloud_api.metrics.%s_ms %.0f\n", name, value * 1000
          printf "video_cloud_api.metrics.pod_%s.%s_ms %.0f\n", pod, name, value * 1000
        }
      }
    }
  '
done
`
	return serverEvidenceProbe{
		source:  "video_cloud_api",
		command: "bash",
		args:    []string{"-lc", script},
		detail:  "Video Cloud API /metrics/prometheus request_token and PKCS#11 timing metrics captured for run_id " + runID,
	}
}

func syncRemoteVMs(vms []LinodeVM, plan Plan, values workflowFlagValues) error {
	binaries, err := buildRemoteRunnerBinaries(values)
	if err != nil {
		return err
	}
	manifestBase, err := localWorkflowOutDir(values)
	if err != nil {
		return err
	}
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
		"--exclude", "*",
	}
}

type remoteRunnerBinaries struct {
	Home100K      string
	RTKCloud      string
	CloudMQTTTest string
}

func writeAnsibleInputs(vms []LinodeVM, plan Plan, values workflowFlagValues, binaries remoteRunnerBinaries) error {
	base, err := localWorkflowOutDir(values)
	if err != nil {
		return err
	}
	if err := writeCommonEnvArchive(filepath.Join(base, "env-common", "env-common.tar.gz"), plan); err != nil {
		return fmt.Errorf("write common env archive: %w", err)
	}
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
	return writeAnsibleInventoryAndVars(vms, plan, values, binaries, true)
}

func writeAnsibleInputsForExistingManifests(vms []LinodeVM, plan Plan, values workflowFlagValues) error {
	base, err := localWorkflowOutDir(values)
	if err != nil {
		return err
	}
	return writeAnsibleInventoryAndVars(vms, plan, values, remoteRunnerBinaries{
		Home100K:      filepath.Join(base, "bin", "home-100k-linux-amd64"),
		RTKCloud:      filepath.Join(base, "bin", "rtk-cloud-linux-amd64"),
		CloudMQTTTest: filepath.Join(base, "bin", "cloud-mqtt-test-linux-amd64"),
	}, false)
}

func writeEnvRsyncFilter(path string, envRoot string, assignment VMAssignment) error {
	lines := []string{
		"+ /env/***",
		"+ /services/***",
		"+ /devices/",
		"+ /devices/test_device/",
		"+ /devices/test_device/loadtest.env",
		"+ /devices/test_device/summary.json",
	}
	lines = append(lines, "- *")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.Join(deduplicateLines(lines), "\n")+"\n"), 0o644)
}

type deviceManifestRow struct {
	AssignmentIndex int
	AssignedEmail   string
	DeviceID        string
	DeviceType      string
	ServiceOptions  []string
}

type shardBindAssignment struct {
	AssignmentIndex int      `json:"assignment_index"`
	AssignedEmail   string   `json:"assigned_email"`
	DeviceID        string   `json:"device_id"`
	DeviceType      string   `json:"device_type"`
	ServiceOptions  []string `json:"service_options"`
}

func writeEnvArchive(path string, plan Plan, assignment VMAssignment) error {
	envRoot := plan.Conditions.EnvRoot
	bundle, err := writeShardCredentialBundle(filepath.Join(filepath.Dir(filepath.Dir(path)), "credential-bundles"), envRoot, plan, assignment)
	if err != nil {
		return err
	}
	extraFiles := []archiveExtraFile{
		{Path: bundle.CompressedPath, Name: filepath.ToSlash(filepath.Join("loadtests", "home-100k", "credentials", assignment.Label+".sqlite.gz"))},
		{Path: bundle.ManifestPath, Name: filepath.ToSlash(filepath.Join("loadtests", "home-100k", "credentials", assignment.Label+".manifest.json"))},
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := createTarGzFromRelPaths(tmpPath, envRoot, nil, extraFiles); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

func writeCommonEnvArchive(path string, plan Plan) error {
	envRoot := plan.Conditions.EnvRoot
	relPaths := []string{
		"env",
		"state/lke.env",
		"state/lke-kubeconfig.yaml",
		"state/video-cloud-staging.state.json",
		"services",
		"devices/test_device/loadtest.env",
		"devices/test_device/summary.json",
	}
	if stackState := stackStateRelPath(envRoot); stackState != "" {
		relPaths = append(relPaths, stackState)
	}
	extraFiles := []archiveExtraFile{}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmpPath := path + ".tmp"
	if err := createTarGzFromRelPaths(tmpPath, envRoot, deduplicateLines(relPaths), extraFiles); err != nil {
		_ = os.Remove(tmpPath)
		return err
	}
	return os.Rename(tmpPath, path)
}

type planDataCoverage struct {
	UsersPath        string         `json:"users_path"`
	DeviceBindPath   string         `json:"device_bind_path"`
	UsersAvailable   int            `json:"users_available"`
	EligibleUsers    int            `json:"eligible_users"`
	DevicesAvailable int            `json:"devices_available"`
	DeviceMix        map[string]int `json:"device_mix"`
}

func validatePlanDataCoverage(envRoot string, plan Plan) error {
	coverage, err := inspectPlanDataCoverage(envRoot, plan)
	if err != nil {
		return err
	}
	problems := []string{}
	if coverage.UsersAvailable < plan.Conditions.Users {
		problems = append(problems, fmt.Sprintf("users available=%d required=%d", coverage.UsersAvailable, plan.Conditions.Users))
	}
	if coverage.EligibleUsers < plan.Conditions.Users {
		problems = append(problems, fmt.Sprintf("eligible users available=%d required=%d", coverage.EligibleUsers, plan.Conditions.Users))
	}
	if coverage.DevicesAvailable < plan.Conditions.Devices {
		problems = append(problems, fmt.Sprintf("eligible devices available=%d required=%d", coverage.DevicesAvailable, plan.Conditions.Devices))
	}
	for _, deviceType := range sortedMapKeys(plan.DeviceMix) {
		required := plan.DeviceMix[deviceType]
		available := coverage.DeviceMix[deviceType]
		if available < required {
			problems = append(problems, fmt.Sprintf("%s available=%d required=%d", deviceType, available, required))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("home-100k data preflight failed: %s (users=%s device_bind=%s)", strings.Join(problems, "; "), coverage.UsersPath, coverage.DeviceBindPath)
	}
	return nil
}

func inspectPlanDataCoverage(envRoot string, plan Plan) (planDataCoverage, error) {
	dbPath := homeTestDataDBPath(envRoot, plan.Conditions.Brandname)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return planDataCoverage{}, err
	}
	defer db.Close()
	coverage := planDataCoverage{
		UsersPath:      dbPath,
		DeviceBindPath: dbPath,
		DeviceMix:      map[string]int{},
	}
	if err := db.QueryRow(`select count(*) from users where brandname = ?`, plan.Conditions.Brandname).Scan(&coverage.UsersAvailable); err != nil {
		return planDataCoverage{}, fmt.Errorf("read SQLite users coverage: %w", err)
	}
	eligibleUsers := map[string]bool{}
	rows, err := db.Query(`select b.assigned_email, b.device_type, b.service_options_json from device_bindings b join users u on u.brandname = b.brandname and u.email = b.assigned_email where b.brandname = ? order by b.assignment_index, b.device_id`, plan.Conditions.Brandname)
	if err != nil {
		return planDataCoverage{}, err
	}
	defer rows.Close()
	for rows.Next() {
		var email, deviceType, serviceOptionsJSON string
		if err := rows.Scan(&email, &deviceType, &serviceOptionsJSON); err != nil {
			return planDataCoverage{}, err
		}
		serviceOptions := []string{}
		_ = json.Unmarshal([]byte(serviceOptionsJSON), &serviceOptions)
		if !homeDeviceType(deviceType) || !stringSliceContains(serviceOptions, "mqtt") {
			continue
		}
		coverage.DevicesAvailable++
		coverage.DeviceMix[deviceType]++
		if email != "" {
			eligibleUsers[email] = true
		}
	}
	if err := rows.Err(); err != nil {
		return planDataCoverage{}, err
	}
	coverage.EligibleUsers = len(eligibleUsers)
	return coverage, nil
}

func writePreflightFailure(outDir string, err error) {
	if strings.TrimSpace(outDir) == "" {
		return
	}
	_ = writeJSONFile(filepath.Join(outDir, "preflight.json"), map[string]any{
		"status": "failed",
		"error":  err.Error(),
	})
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

func homeDeviceType(value string) bool {
	switch value {
	case "light", "switch", "smart_plug", "air_conditioner", "environment_sensor", "security_sensor", "smart_meter", "camera_status", "door_lock", "appliance", "gateway":
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

type archiveExtraFile struct {
	Path string
	Name string
}

func createTarGzFromRelPaths(path string, root string, relPaths []string, extraFiles []archiveExtraFile) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	gz := gzip.NewWriter(file)
	gz.Header.ModTime = time.Unix(0, 0)
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
	for _, extra := range extraFiles {
		if err := addExtraPathToTar(tw, extra.Path, extra.Name, added); err != nil {
			return err
		}
	}
	return nil
}

func addExtraPathToTar(tw *tar.Writer, path string, name string, added map[string]bool) error {
	name = filepath.ToSlash(filepath.Clean(name))
	if name == "." || strings.HasPrefix(name, "../") || name == ".." || filepath.IsAbs(name) {
		return fmt.Errorf("invalid archive extra path: %s", name)
	}
	if added[name] {
		return nil
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("archive extra path is not a regular file: %s", path)
	}
	header, err := tar.FileInfoHeader(info, "")
	if err != nil {
		return err
	}
	header.Name = name
	normalizeArchiveHeader(header)
	if err := tw.WriteHeader(header); err != nil {
		return err
	}
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	if _, err := io.Copy(tw, file); err != nil {
		return err
	}
	added[name] = true
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
	normalizeArchiveHeader(header)
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

func normalizeArchiveHeader(header *tar.Header) {
	header.ModTime = time.Unix(0, 0)
	header.AccessTime = time.Unix(0, 0)
	header.ChangeTime = time.Unix(0, 0)
	header.Uid = 0
	header.Gid = 0
	header.Uname = ""
	header.Gname = ""
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

func writeAnsibleInventoryAndVars(vms []LinodeVM, plan Plan, values workflowFlagValues, binaries remoteRunnerBinaries, prepareArtifacts bool) error {
	base, err := localWorkflowOutDir(values)
	if err != nil {
		return err
	}
	localOutDir := base
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
	localArtifactStore, err := filepath.Abs(filepath.Join(base, "artifact-store"))
	if err != nil {
		return err
	}
	fanoutPrivateKey, err := filepath.Abs(filepath.Join(base, "ansible", "fanout_ed25519"))
	if err != nil {
		return err
	}
	if prepareArtifacts {
		localArtifactStore, err = prepareLocalArtifactStore(base, vms, binaries)
		if err != nil {
			return err
		}
		fanoutPrivateKey, err = ensureFanoutKey(base)
		if err != nil {
			return err
		}
	}
	fanoutPublicKey := fanoutPrivateKey + ".pub"
	ansibleDir := filepath.Join(base, "ansible")
	if err := os.MkdirAll(ansibleDir, 0o755); err != nil {
		return err
	}
	hosts := map[string]any{}
	orchestraHosts := map[string]any{}
	for _, vm := range vms {
		if strings.TrimSpace(vm.PublicIPv4) == "" {
			return fmt.Errorf("VM %s has no public IPv4 for ansible inventory", vm.Label)
		}
		assignment, ok := findAssignmentByLabel(plan, vm.Label)
		if !ok {
			return fmt.Errorf("VM label %s does not match any plan assignment", vm.Label)
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
			"role":                         assignment.Role,
			"shard_index":                  assignment.Index,
			"vm_label":                     vm.Label,
			"local_shard_manifest":         localShardManifest,
			"local_env_rsync_filter":       localEnvRsyncFilter,
			"local_env_archive":            localEnvArchive,
			"artifact_env_archive":         filepath.Base(localEnvArchive),
			"artifact_shard_manifest":      filepath.Base(localShardManifest),
			"remote_shard_manifest":        strings.TrimRight(values.remoteWorkspace, "/") + "/loadtests/home-100k/shard-manifests/current.json",
			"remote_out_dir":               strings.TrimRight(firstNonEmpty(values.remoteOutRoot, "/var/lib/home-100k"), "/") + "/" + normalizedRunID(values.runID) + "/" + vm.Label,
		}
	}
	if len(vms) == 0 {
		return errors.New("no VMs available for ansible inventory")
	}
	orchestraHosts[vms[0].Label] = hosts[vms[0].Label]
	inventory := map[string]any{
		"all": map[string]any{
			"children": map[string]any{
				"home_100k": map[string]any{
					"hosts": hosts,
				},
				"home_100k_orchestra": map[string]any{
					"hosts": orchestraHosts,
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
		"run_id":                        normalizedRunID(values.runID),
		"local_out_dir":                 localOutDir,
		"local_runner":                  localRunner,
		"local_rtk_cloud":               localRTKCloud,
		"local_cloud_mqtt_test":         localCloudMQTTTest,
		"local_artifact_store":          localArtifactStore,
		"fanout_private_key":            fanoutPrivateKey,
		"fanout_public_key":             fanoutPublicKey,
		"local_env_root":                strings.TrimRight(localEnvRoot, "/"),
		"remote_workspace":              strings.TrimRight(values.remoteWorkspace, "/"),
		"remote_env_root":               strings.TrimRight(values.remoteEnvRoot, "/"),
		"remote_out_root":               strings.TrimRight(firstNonEmpty(values.remoteOutRoot, "/var/lib/home-100k"), "/"),
		"brandname":                     plan.Conditions.Brandname,
		"region":                        plan.Conditions.Region,
		"vm_label_prefix":               plan.Conditions.VMLabelPrefix,
		"device_count":                  plan.Conditions.Devices,
		"user_count":                    plan.Conditions.Users,
		"devices_per_user":              plan.Conditions.DevicesPerUser,
		"load_generator_devices_per_vm": plan.Conditions.LoadGeneratorDevicesPerVM,
		"target_ramp_up_time":           stageWarmUp,
		"measurement_window":            stageSteady,
		"post_run_collection":           stageCoolDown,
		"runner_mode":                   firstNonEmpty(values.runnerMode, "sample"),
		"runner_nofile_limit":           values.runnerNofileLimit,
		"mqtt_concurrency":              values.mqttConcurrency,
		"command_concurrency":           liveCommandConcurrency(plan.Conditions.DeviceGeneratorLimit, values.commandConcurrency),
		"shadow_command_timeout":        firstNonEmpty(values.shadowCommandTimeout, DefaultShadowCommandTimeout),
		"live_runner_timeout_grace":     firstNonEmpty(values.liveRunnerTimeoutGrace, ""),
		"mqtt_addr":                     strings.TrimSpace(values.mqttAddr),
		"video_cloud_public_url":        strings.TrimSpace(firstNonEmpty(values.videoCloudPublicURL, values.videoCloudBaseURL)),
		"video_cloud_token_url":         strings.TrimSpace(values.videoCloudTokenURL),
		"account_manager_url":           strings.TrimSpace(values.accountManagerURL),
		"generator_hosts_override_ip":   strings.TrimSpace(values.generatorHostsOverrideIP),
		"credential_bundle_format":      firstNonEmpty(values.credentialBundleFormat, "sqlite-gzip"),
	}
	return writeJSONFile(filepath.Join(ansibleDir, "extra-vars.json"), extraVars)
}

func prepareLocalArtifactStore(base string, vms []LinodeVM, binaries remoteRunnerBinaries) (string, error) {
	store := filepath.Join(base, "artifact-store")
	paths := []string{
		filepath.Join(store, "bin"),
		filepath.Join(store, "common"),
		filepath.Join(store, "env-archives"),
		filepath.Join(store, "shard-manifests"),
	}
	for _, path := range paths {
		if err := os.MkdirAll(path, 0o755); err != nil {
			return "", err
		}
	}
	links := []struct {
		src  string
		dest string
	}{
		{binaries.Home100K, filepath.Join(store, "bin", "home-100k")},
		{binaries.RTKCloud, filepath.Join(store, "bin", "rtk-cloud")},
		{binaries.CloudMQTTTest, filepath.Join(store, "bin", "cloud-mqtt-test")},
		{filepath.Join(base, "env-common", "env-common.tar.gz"), filepath.Join(store, "common", "env-common.tar.gz")},
	}
	for _, vm := range vms {
		links = append(links,
			struct {
				src  string
				dest string
			}{filepath.Join(base, "env-archives", vm.Label+".tar.gz"), filepath.Join(store, "env-archives", vm.Label+".tar.gz")},
			struct {
				src  string
				dest string
			}{filepath.Join(base, "shard-manifests", vm.Label+".json"), filepath.Join(store, "shard-manifests", vm.Label+".json")},
		)
	}
	for _, item := range links {
		if err := linkOrCopyFile(item.src, item.dest); err != nil {
			return "", err
		}
	}
	if err := writeArtifactStoreManifest(store); err != nil {
		return "", err
	}
	return filepath.Abs(store)
}

func writeArtifactStoreManifest(store string) error {
	type artifactFile struct {
		Path   string `json:"path"`
		Size   int64  `json:"size"`
		SHA256 string `json:"sha256"`
	}
	files := []artifactFile{}
	if err := filepath.WalkDir(store, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(store, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if rel == "manifest.json" {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		sum, err := sha256File(path)
		if err != nil {
			return err
		}
		files = append(files, artifactFile{Path: rel, Size: info.Size(), SHA256: sum})
		return nil
	}); err != nil {
		return err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	digestInput := strings.Builder{}
	for _, file := range files {
		digestInput.WriteString(file.Path)
		digestInput.WriteByte('\x00')
		digestInput.WriteString(strconv.FormatInt(file.Size, 10))
		digestInput.WriteByte('\x00')
		digestInput.WriteString(file.SHA256)
		digestInput.WriteByte('\n')
	}
	digest := sha256.Sum256([]byte(digestInput.String()))
	return writeJSONFile(filepath.Join(store, "manifest.json"), map[string]any{
		"schema":       "home-100k-artifact-store/v1",
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"digest":       hex.EncodeToString(digest[:]),
		"files":        files,
	})
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func linkOrCopyFile(src, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	_ = os.Remove(dest)
	if err := os.Link(src, dest); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

func ensureFanoutKey(base string) (string, error) {
	keyPath, err := filepath.Abs(filepath.Join(base, "ansible", "fanout_ed25519"))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(keyPath); err == nil {
		return keyPath, nil
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o700); err != nil {
		return "", err
	}
	if err := commandRunner("ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", keyPath); err != nil {
		return "", fmt.Errorf("generate per-run fanout ssh key: %w", err)
	}
	_ = os.Chmod(keyPath, 0o600)
	return keyPath, nil
}

func runAnsiblePlaybook(values workflowFlagValues, playbook string) error {
	workspace, err := localWorkspaceRoot()
	if err != nil {
		return err
	}
	base, err := localWorkflowOutDir(values)
	if err != nil {
		return err
	}
	ansibleDir := filepath.Join(workspace, "loadtests", "home-100k", "ansible")
	ansibleConfig := filepath.Join(ansibleDir, "ansible.cfg")
	args := []string{
		"ANSIBLE_CONFIG=" + ansibleConfig,
		"ansible-playbook",
		"--forks", "20",
		"-i", filepath.Join(base, "ansible", "inventory.json"),
		filepath.Join(ansibleDir, playbook),
		"--extra-vars", "@" + filepath.Join(base, "ansible", "extra-vars.json"),
	}
	return commandRunner("env", args...)
}

func runAnsiblePlaybookWithRetry(values workflowFlagValues, playbook string, attempts int) error {
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		if err := runAnsiblePlaybook(values, playbook); err != nil {
			lastErr = err
			if attempt < attempts {
				fmt.Fprintf(os.Stderr, "warning: ansible playbook %s failed on attempt %d/%d: %v; retrying\n", playbook, attempt, attempts, err)
				if ansibleRetryDelay > 0 {
					time.Sleep(ansibleRetryDelay)
				}
				continue
			}
			break
		}
		return nil
	}
	return lastErr
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

func localWorkflowOutDir(values workflowFlagValues) (string, error) {
	base := workflowOutDir(values)
	if filepath.IsAbs(base) {
		return base, nil
	}
	return localWorkflowArtifactPath(base)
}

func localWorkflowArtifactPath(path string) (string, error) {
	if filepath.IsAbs(path) {
		return path, nil
	}
	workspace, err := localWorkspaceRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(workspace, path), nil
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
	workspace, err := localWorkspaceRoot()
	if err != nil {
		return remoteRunnerBinaries{}, err
	}
	base, err := localWorkflowOutDir(values)
	if err != nil {
		return remoteRunnerBinaries{}, err
	}
	binDir := filepath.Join(base, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return remoteRunnerBinaries{}, err
	}
	home100K := filepath.Join(binDir, "home-100k-linux-amd64")
	cmd := fmt.Sprintf("cd %s && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o %s ./loadtests/home-100k/cmd/home-100k", shellQuote(workspace), shellQuote(home100K))
	if err := commandRunner("bash", "-lc", cmd); err != nil {
		return remoteRunnerBinaries{}, fmt.Errorf("build linux home-100k runner binary: %w", err)
	}
	rtkCloud := filepath.Join(binDir, "rtk-cloud-linux-amd64")
	cmd = fmt.Sprintf("cd %s && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o %s ./scripts/go/rtk-cloud", shellQuote(workspace), shellQuote(rtkCloud))
	if err := commandRunner("bash", "-lc", cmd); err != nil {
		return remoteRunnerBinaries{}, fmt.Errorf("build linux rtk-cloud runner binary: %w", err)
	}
	cloudMQTTTest := filepath.Join(binDir, "cloud-mqtt-test-linux-amd64")
	cmd = fmt.Sprintf("cd %s && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o %s ./scripts/go/cloud-mqtt-test", shellQuote(workspace), shellQuote(cloudMQTTTest))
	if err := commandRunner("bash", "-lc", cmd); err != nil {
		return remoteRunnerBinaries{}, fmt.Errorf("build linux cloud-mqtt-test runner binary: %w", err)
	}
	return remoteRunnerBinaries{Home100K: home100K, RTKCloud: rtkCloud, CloudMQTTTest: cloudMQTTTest}, nil
}

func localWorkspaceRoot() (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if pathIsFile(filepath.Join(wd, "go.work")) && pathIsDir(filepath.Join(wd, "loadtests", "home-100k")) && pathIsDir(filepath.Join(wd, "scripts", "go")) {
			return wd, nil
		}
		parent := filepath.Dir(wd)
		if parent == wd {
			return "", fmt.Errorf("workspace root not found from %s", wd)
		}
		wd = parent
	}
}

func pathIsFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func pathIsDir(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.IsDir()
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
	if err := runAnsiblePlaybookWithRetry(values, "start-runner.yml", 3); err != nil {
		return err
	}
	coordination, err := runHostCoordinator(vms, plan, runID, values)
	if err != nil {
		return err
	}
	outDir, err := localWorkflowOutDir(values)
	if err != nil {
		return err
	}
	return writeJSONFile(filepath.Join(outDir, "start-coordination.json"), coordination)
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
