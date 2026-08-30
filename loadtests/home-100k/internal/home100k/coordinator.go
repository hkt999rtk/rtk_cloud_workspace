package home100k

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const defaultRunnerDaemonListen = ":18080"
const defaultCoordinatorStartDelayMS = 3000

var runHostCoordinator = coordinateRemoteRunnerStart

type runnerDaemonFlagValues struct {
	shardRunFlagValues
	listen string
}

type startCommand struct {
	RunID   string `json:"run_id"`
	StageID string `json:"stage_id,omitempty"`
	Seq     int    `json:"sequence"`
	DelayMS int    `json:"delay_ms"`
}

type runnerDaemonState struct {
	mu         sync.Mutex
	plan       Plan
	assignment VMAssignment
	values     shardRunFlagValues
	runID      string
	outDir     string
	telemetry  VMStartTelemetry
	started    bool
	done       chan struct{}
}

func executeRunnerDaemon(args []string, stdout io.Writer, stderr io.Writer) int {
	opts, values, err := parseRunnerDaemonFlags("home-100k runner-daemon", args, stderr)
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
		fmt.Fprintf(stderr, "assignment not found: role=%s index=%d\n", values.role, values.shardIndex)
		return 2
	}
	if strings.TrimSpace(values.shardManifest) != "" {
		manifest, err := loadVMAssignment(values.shardManifest)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		assignment = manifest
	}
	runID := normalizedRunID(values.runID)
	if err := runRunnerDaemon(plan, assignment, values.shardRunFlagValues, runID, values.listen); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func parseRunnerDaemonFlags(name string, args []string, stderr io.Writer) (PlanOptions, runnerDaemonFlagValues, error) {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(stderr)
	envRoot := fs.String("env-root", "", "staging/LKE env-root")
	brandname := fs.String("brandname", "", "brand name")
	scenarioProfile := fs.String("scenario-profile", "", "scenario profile")
	otaProfile := addOTAProfileFlags(fs)
	region := fs.String("region", "", "Linode region for load-generator VMs")
	vmLabelPrefix := addVMLabelPrefixFlag(fs)
	stageWarmUp, stageSteady, stageCoolDown := addStageDurationFlags(fs)
	deviceCount, userCount, devicesPerUser, vmCount, loadGeneratorDevicesPerVM, videoGeneratorVMCount, videoGeneratorLabelPrefix := addSizingFlags(fs)
	runID := fs.String("run-id", "", "run id for artifact correlation")
	outDir := fs.String("out-dir", "", "artifact output directory")
	role := fs.String("role", "", "shard role")
	shardIndex := fs.Int("shard-index", 0, "shard index")
	shardManifest := fs.String("shard-manifest", "", "shard manifest JSON path")
	runnerMode := fs.String("runner-mode", "live", "runner mode: sample or live")
	rtkCloudBinary := fs.String("rtk-cloud-binary", "rtk-cloud", "rtk-cloud binary for live MQTT/API runner")
	workspace := fs.String("workspace", "", "workspace path for live MQTT/API runner")
	mqttConcurrency := fs.Int("mqtt-concurrency", DefaultLiveMQTTConcurrency, "per-shard MQTT connect worker concurrency for live runner")
	commandConcurrency := fs.Int("command-concurrency", DefaultLiveCommandConcurrency, "per-shard sustained shadow command concurrency for live runner")
	shadowCommandTimeout := fs.String("shadow-command-timeout", DefaultShadowCommandTimeout, "per-phase sustained shadow command timeout")
	deviceTokenRequestTimeout := fs.String("device-token-request-timeout", DefaultDeviceTokenRequestTimeout, "per-attempt device /request_token timeout for live MQTT bootstrap")
	deviceTokenRequestRetries := fs.Int("device-token-request-retries", 0, "bounded retry count after the first device /request_token attempt")
	runtimeLogs := fs.Bool("runtime-logs", true, "publish MQTT runtime logs during sustained shadow commands")
	liveRunnerTimeoutGrace := fs.String("live-runner-timeout-grace", "", "extra timeout after the configured live MQTT duration before killing the shard runner")
	listen := fs.String("listen", defaultRunnerDaemonListen, "runner daemon listen address")
	if err := fs.Parse(args); err != nil {
		return PlanOptions{}, runnerDaemonFlagValues{}, err
	}
	if strings.TrimSpace(*role) == "" {
		return PlanOptions{}, runnerDaemonFlagValues{}, fmt.Errorf("--role is required")
	}
	opts := PlanOptions{EnvRoot: *envRoot, Brandname: *brandname, ScenarioProfile: *scenarioProfile, Region: *region}
	applyVMLabelPrefixFlag(&opts, vmLabelPrefix)
	applyStageDurationFlags(&opts, stageWarmUp, stageSteady, stageCoolDown)
	applySizingFlags(&opts, deviceCount, userCount, devicesPerUser, vmCount, loadGeneratorDevicesPerVM, videoGeneratorVMCount, videoGeneratorLabelPrefix)
	opts.DeviceTokenRequestTimeout = strings.TrimSpace(*deviceTokenRequestTimeout)
	opts.DeviceTokenRequestRetries = *deviceTokenRequestRetries
	applyOTAProfileFlags(&opts, otaProfile)
	return opts, runnerDaemonFlagValues{
		shardRunFlagValues: shardRunFlagValues{
			runID:                     *runID,
			outDir:                    *outDir,
			role:                      *role,
			shardIndex:                *shardIndex,
			shardManifest:             *shardManifest,
			honorStageDurations:       true,
			runnerMode:                *runnerMode,
			rtkCloudBinary:            *rtkCloudBinary,
			workspace:                 *workspace,
			mqttConcurrency:           *mqttConcurrency,
			commandConcurrency:        *commandConcurrency,
			shadowCommandTimeout:      strings.TrimSpace(*shadowCommandTimeout),
			deviceTokenRequestTimeout: strings.TrimSpace(*deviceTokenRequestTimeout),
			deviceTokenRequestRetries: *deviceTokenRequestRetries,
			runtimeLogs:               *runtimeLogs,
			liveRunnerTimeoutGrace:    strings.TrimSpace(*liveRunnerTimeoutGrace),
		},
		listen: *listen,
	}, nil
}

func runRunnerDaemon(plan Plan, assignment VMAssignment, values shardRunFlagValues, runID string, listen string) error {
	outDir := strings.TrimSpace(values.outDir)
	if outDir == "" {
		outDir = filepath.Join("loadtests", "home-100k", "reports", runID, assignment.Role, fmt.Sprintf("%03d", assignment.Index))
	}
	state := &runnerDaemonState{
		plan:       plan,
		assignment: assignment,
		values:     values,
		runID:      runID,
		outDir:     outDir,
		done:       make(chan struct{}),
		telemetry: VMStartTelemetry{
			Label:   assignment.Label,
			RunID:   runID,
			ReadyAt: time.Now().UTC().Format(time.RFC3339Nano),
			Status:  "READY_WAIT",
		},
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	if err := state.writeTelemetry(); err != nil {
		return err
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/ready", state.handleReady)
	mux.HandleFunc("/start", state.handleStart)
	mux.HandleFunc("/status", state.handleStatus)
	server := &http.Server{Addr: firstNonEmpty(strings.TrimSpace(listen), defaultRunnerDaemonListen), Handler: mux}
	err := server.ListenAndServe()
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

func (s *runnerDaemonState) handleReady(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = json.NewEncoder(w).Encode(s.telemetry)
}

func (s *runnerDaemonState) handleStatus(w http.ResponseWriter, _ *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_ = json.NewEncoder(w).Encode(s.telemetry)
}

func (s *runnerDaemonState) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var cmd startCommand
	if err := json.NewDecoder(r.Body).Decode(&cmd); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(cmd.RunID) != s.runID {
		http.Error(w, "run_id mismatch", http.StatusConflict)
		return
	}
	if cmd.DelayMS <= 0 {
		cmd.DelayMS = defaultCoordinatorStartDelayMS
	}
	s.mu.Lock()
	if s.started {
		current := s.telemetry
		s.mu.Unlock()
		_ = json.NewEncoder(w).Encode(current)
		return
	}
	s.started = true
	s.telemetry.StartSignalReceivedAt = time.Now().UTC().Format(time.RFC3339Nano)
	s.telemetry.Status = "START_RECEIVED"
	_ = s.writeTelemetryLocked()
	s.mu.Unlock()

	go s.runAfterDelay(time.Duration(cmd.DelayMS) * time.Millisecond)
	_ = json.NewEncoder(w).Encode(map[string]any{"accepted": true, "delay_ms": cmd.DelayMS})
}

func (s *runnerDaemonState) runAfterDelay(delay time.Duration) {
	time.Sleep(delay)
	s.mu.Lock()
	s.telemetry.StageStartedAt = time.Now().UTC().Format(time.RFC3339Nano)
	s.telemetry.FirstConnectAt = s.telemetry.StageStartedAt
	s.telemetry.Status = "running"
	_ = s.writeTelemetryLocked()
	s.mu.Unlock()

	err := runLiveShard(s.plan, s.assignment, s.values, s.runID)

	s.mu.Lock()
	s.telemetry.StageCompletedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if err != nil {
		s.telemetry.Status = "failed"
		s.telemetry.Error = err.Error()
	} else {
		s.telemetry.Status = "completed"
	}
	_ = s.writeTelemetryLocked()
	s.mu.Unlock()
	close(s.done)
}

func (s *runnerDaemonState) writeTelemetry() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.writeTelemetryLocked()
}

func (s *runnerDaemonState) writeTelemetryLocked() error {
	return writeJSONFile(filepath.Join(s.outDir, "coordination.json"), s.telemetry)
}

func coordinateRemoteRunnerStart(vms []LinodeVM, plan Plan, runID string, values workflowFlagValues) (StartCoordination, error) {
	client := &http.Client{Timeout: 5 * time.Second}
	deadline := time.Now().Add(5 * time.Minute)
	ready := map[string]VMStartTelemetry{}
	for time.Now().Before(deadline) {
		for _, vm := range vms {
			if _, ok := ready[vm.Label]; ok {
				continue
			}
			telemetry, err := getRunnerTelemetryWithSSHFallback(client, vm, "/ready", values)
			if err == nil && telemetry.Status == "READY_WAIT" && strings.TrimSpace(telemetry.RunID) == runID {
				telemetry.IP = vm.PublicIPv4
				ready[vm.Label] = telemetry
			}
		}
		if len(ready) == len(vms) {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if len(ready) != len(vms) {
		return StartCoordination{}, fmt.Errorf("RUNNER_READY_BARRIER_FAILED: runner ready barrier failed: %d/%d ready", len(ready), len(vms))
	}

	delayMS := values.coordinatorDelayMS
	if delayMS <= 0 {
		delayMS = defaultCoordinatorStartDelayMS
	}
	for idx, vm := range vms {
		if err := postRunnerStartWithSSHFallback(client, vm, startCommand{RunID: runID, StageID: "all", Seq: idx + 1, DelayMS: delayMS}, values); err != nil {
			return StartCoordination{}, err
		}
	}

	completed := map[string]VMStartTelemetry{}
	waitDeadline := time.Now().Add(planTotalDuration(plan) + 20*time.Minute)
	for time.Now().Before(waitDeadline) {
		for _, vm := range vms {
			if _, ok := completed[vm.Label]; ok {
				continue
			}
			telemetry, err := getRunnerTelemetry(client, vm, "/status")
			if err != nil {
				continue
			}
			telemetry.IP = vm.PublicIPv4
			if strings.TrimSpace(telemetry.RunID) == runID && (telemetry.Status == "completed" || telemetry.Status == "failed") {
				completed[vm.Label] = telemetry
			}
		}
		if len(completed) == len(vms) {
			break
		}
		time.Sleep(5 * time.Second)
	}
	if len(completed) != len(vms) {
		return StartCoordination{}, fmt.Errorf("runner completion barrier failed: %d/%d completed", len(completed), len(vms))
	}

	items := make([]VMStartTelemetry, 0, len(vms))
	for _, vm := range vms {
		items = append(items, completed[vm.Label])
	}
	sort.Slice(items, func(i, j int) bool { return items[i].Label < items[j].Label })
	return StartCoordination{
		Mode:         "host-coordinator",
		ReadyBarrier: fmt.Sprintf("%d/%d", len(ready), len(vms)),
		StartDelayMS: delayMS,
		MaxSkewMS:    computeMaxStartSkewMS(items),
		VMs:          items,
	}, nil
}

func getRunnerTelemetryWithSSHFallback(client *http.Client, vm LinodeVM, path string, values workflowFlagValues) (VMStartTelemetry, error) {
	telemetry, err := getRunnerTelemetry(client, vm, path)
	if err == nil || strings.TrimSpace(values.sshKey) == "" {
		return telemetry, err
	}
	out, sshErr := runnerSSHHTTP(values, vm, "GET", path, "")
	if sshErr != nil {
		return VMStartTelemetry{}, fmt.Errorf("%s public control request failed: %v; ssh fallback failed: %w", vm.Label, err, sshErr)
	}
	if decodeErr := json.Unmarshal([]byte(out), &telemetry); decodeErr != nil {
		return VMStartTelemetry{}, decodeErr
	}
	return telemetry, nil
}

func postRunnerStartWithSSHFallback(client *http.Client, vm LinodeVM, cmd startCommand, values workflowFlagValues) error {
	err := postRunnerStart(client, vm, cmd)
	if err == nil || strings.TrimSpace(values.sshKey) == "" {
		return err
	}
	body, marshalErr := json.Marshal(cmd)
	if marshalErr != nil {
		return marshalErr
	}
	if _, sshErr := runnerSSHHTTP(values, vm, "POST", "/start", string(body)); sshErr != nil {
		return fmt.Errorf("%s public control request failed: %v; ssh fallback failed: %w", vm.Label, err, sshErr)
	}
	return nil
}

func runnerSSHHTTP(values workflowFlagValues, vm LinodeVM, method, path, body string) (string, error) {
	user := firstNonEmpty(values.sshUser, "root")
	args := []string{"-n", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-o", "StrictHostKeyChecking=accept-new", "-i", values.sshKey, user + "@" + vm.PublicIPv4, "curl", "-fsS", "-X", method, "http://127.0.0.1:18080" + path}
	if method == "POST" {
		args = append(args, "-H", "Content-Type: application/json", "--data-raw", body)
	}
	return commandOutputRunnerWithTimeout(15*time.Second, "ssh", args...)
}

func getRunnerTelemetry(client *http.Client, vm LinodeVM, path string) (VMStartTelemetry, error) {
	resp, err := client.Get("http://" + vm.PublicIPv4 + ":18080" + path)
	if err != nil {
		return VMStartTelemetry{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return VMStartTelemetry{}, fmt.Errorf("%s %s: %s", vm.Label, path, resp.Status)
	}
	var telemetry VMStartTelemetry
	if err := json.NewDecoder(resp.Body).Decode(&telemetry); err != nil {
		return VMStartTelemetry{}, err
	}
	return telemetry, nil
}

func postRunnerStart(client *http.Client, vm LinodeVM, cmd startCommand) error {
	body, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	resp, err := client.Post("http://"+vm.PublicIPv4+":18080/start", "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("%s start: %s", vm.Label, resp.Status)
	}
	return nil
}

func planTotalDuration(plan Plan) time.Duration {
	total := time.Duration(0)
	for _, stage := range plan.Stages {
		for _, raw := range []string{stage.WarmUp, stage.SteadyState, stage.CoolDown} {
			d, err := time.ParseDuration(raw)
			if err == nil {
				total += d
			}
		}
	}
	return total
}

func computeMaxStartSkewMS(items []VMStartTelemetry) int64 {
	var minTime, maxTime time.Time
	for _, item := range items {
		raw := firstNonEmpty(item.StageStartedAt, item.FirstConnectAt)
		if strings.TrimSpace(raw) == "" {
			continue
		}
		ts, err := time.Parse(time.RFC3339Nano, raw)
		if err != nil {
			continue
		}
		if minTime.IsZero() || ts.Before(minTime) {
			minTime = ts
		}
		if maxTime.IsZero() || ts.After(maxTime) {
			maxTime = ts
		}
	}
	if minTime.IsZero() || maxTime.IsZero() {
		return 0
	}
	return maxTime.Sub(minTime).Milliseconds()
}
