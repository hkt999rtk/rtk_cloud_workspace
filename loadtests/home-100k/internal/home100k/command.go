package home100k

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
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
	fmt.Fprintln(w, `usage: home-100k <plan|run|provision-vms|sync|run-stages|collect|collect-server-evidence|aggregate|list-vms|destroy-vms> --env-root PATH --brandname NAME --region LINODE_REGION [--ephemeral-vms]`)
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
	vmStateFile        string
	remoteWorkspace    string
	remoteEnvRoot      string
	remoteOutRoot      string
	sshUser            string
	sshKey             string
}

type shardRunFlagValues struct {
	runID               string
	outDir              string
	role                string
	shardIndex          int
	shardManifest       string
	honorStageDurations bool
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
	vmStateFile := fs.String("vm-state-file", "", "VM state JSON from provision-vms")
	remoteWorkspace := fs.String("remote-workspace", "", "workspace path on load-generator VMs")
	remoteEnvRoot := fs.String("remote-env-root", "", "env-root path on load-generator VMs")
	remoteOutRoot := fs.String("remote-out-root", "", "output root on load-generator VMs")
	sshUser := fs.String("ssh-user", "root", "SSH user for load-generator VMs")
	sshKey := fs.String("ssh-key", "", "SSH private key for load-generator VMs")
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
		vmStateFile:        *vmStateFile,
		remoteWorkspace:    *remoteWorkspace,
		remoteEnvRoot:      *remoteEnvRoot,
		remoteOutRoot:      *remoteOutRoot,
		sshUser:            *sshUser,
		sshKey:             *sshKey,
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
		err := commandRunner(probe.command, probe.args...)
		if err != nil {
			sources[probe.source] = mergeEvidenceSource(sources[probe.source], EvidenceSource{Available: false, Detail: err.Error()})
			notes = append(notes, fmt.Sprintf("%s evidence probe failed: %s", probe.source, err.Error()))
			continue
		}
		sources[probe.source] = mergeEvidenceSource(sources[probe.source], EvidenceSource{Available: true, Detail: probe.detail})
	}
	for key := range requiredEvidenceSources(false) {
		if _, ok := sources[key]; !ok {
			sources[key] = EvidenceSource{Available: false, Detail: "probe not configured"}
		}
	}
	return ServerEvidence{
		RunID:    runID,
		Complete: allEvidenceSourcesAvailable(sources),
		Sources:  sources,
		Notes:    notes,
	}
}

func mergeEvidenceSource(current EvidenceSource, next EvidenceSource) EvidenceSource {
	if current.Detail == "" {
		return next
	}
	merged := EvidenceSource{
		Available: current.Available && next.Available,
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
			command: "kubectl",
			args:    []string{"top", "pods", "-A"},
			detail:  "pod resource usage captured",
		},
		kubectlLogsProbe("emqx", "video-cloud-staging-video-cloud", "app.kubernetes.io/name=mqtt", "MQTT broker logs and client churn evidence captured for run_id "+runID),
		kubectlLogsProbe("video_cloud_api", "video-cloud-staging-video-cloud", "app.kubernetes.io/name=video-cloud-api", "Video Cloud API logs captured for run_id "+runID),
		kubectlLogsProbe("iot_device_shadow", "video-cloud-staging-video-cloud", "app.kubernetes.io/component=iot-device-shadow", "IoT Device Shadow HTTP path logs captured for run_id "+runID),
		kubectlLogsProbe("postgres", "video-cloud-staging-platform", "app.kubernetes.io/name=postgresql", "PostgreSQL logs captured"),
		kubectlLogsProbe("redis_valkey", "video-cloud-staging-platform", "app.kubernetes.io/name=redis", "Redis/Valkey logs captured when enabled"),
		kubectlLogsProbe("ingress_nginx", "video-cloud-staging-ingress", "app.kubernetes.io/name=ingress-nginx", "Ingress/nginx logs captured for run_id "+runID),
	}
}

func kubectlLogsProbe(source string, namespace string, selector string, detail string) serverEvidenceProbe {
	script := fmt.Sprintf(
		`set -euo pipefail; pods="$(kubectl -n %s get pods --selector %s -o name)"; test -n "$pods"; kubectl -n %s logs --since=30m --selector %s --tail=1000`,
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
	runnerBinary, err := buildRemoteRunnerBinary(values)
	if err != nil {
		return err
	}
	manifestBase := workflowOutDir(values)
	if err := writeAnsibleInputs(vms, plan, values, runnerBinary); err != nil {
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

func writeAnsibleInputs(vms []LinodeVM, plan Plan, values workflowFlagValues, runnerBinary string) error {
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
	}
	return writeAnsibleInventoryAndVars(vms, plan, values, runnerBinary)
}

func writeAnsibleInputsForExistingManifests(vms []LinodeVM, plan Plan, values workflowFlagValues) error {
	return writeAnsibleInventoryAndVars(vms, plan, values, filepath.Join(workflowOutDir(values), "bin", "home-100k-linux-amd64"))
}

func writeAnsibleInventoryAndVars(vms []LinodeVM, plan Plan, values workflowFlagValues, runnerBinary string) error {
	base := workflowOutDir(values)
	localOutDir, err := filepath.Abs(base)
	if err != nil {
		return err
	}
	localRunner, err := filepath.Abs(runnerBinary)
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
		"run_id":           normalizedRunID(values.runID),
		"local_out_dir":    localOutDir,
		"local_runner":     localRunner,
		"local_env_root":   strings.TrimRight(localEnvRoot, "/"),
		"remote_workspace": strings.TrimRight(values.remoteWorkspace, "/"),
		"remote_env_root":  strings.TrimRight(values.remoteEnvRoot, "/"),
		"remote_out_root":  strings.TrimRight(firstNonEmpty(values.remoteOutRoot, "/var/lib/home-100k"), "/"),
		"brandname":        plan.Conditions.Brandname,
		"region":           plan.Conditions.Region,
		"stage_warm_up":    stageWarmUp,
		"stage_steady":     stageSteady,
		"stage_cool_down":  stageCoolDown,
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

func buildRemoteRunnerBinary(values workflowFlagValues) (string, error) {
	base := strings.TrimSpace(values.outDir)
	if base == "" {
		base = filepath.Join("loadtests", "home-100k", "reports", normalizedRunID(values.runID))
	}
	binDir := filepath.Join(base, "bin")
	if err := os.MkdirAll(binDir, 0o755); err != nil {
		return "", err
	}
	out := filepath.Join(binDir, "home-100k-linux-amd64")
	cmd := fmt.Sprintf("GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o %s ./loadtests/home-100k/cmd/home-100k", shellQuote(out))
	if err := commandRunner("bash", "-lc", cmd); err != nil {
		return "", fmt.Errorf("build linux runner binary: %w", err)
	}
	return out, nil
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
	return runAnsiblePlaybook(values, "run-stages.yml")
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
