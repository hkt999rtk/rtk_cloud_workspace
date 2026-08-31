package main

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type lkeCapacityRunSummary struct {
	Schema               string                     `json:"schema"`
	GeneratedAt          string                     `json:"generated_at"`
	RunID                string                     `json:"run_id"`
	RunDir               string                     `json:"run_dir"`
	ReportFile           string                     `json:"report_file"`
	ResultsFile          string                     `json:"results_file"`
	ServerEvidenceFile   string                     `json:"server_evidence_file"`
	EnvRoot              string                     `json:"env_root,omitempty"`
	Requested            lkeCapacityRequested       `json:"requested"`
	Outcome              lkeCapacityOutcome         `json:"outcome"`
	Gates                map[string]string          `json:"gates"`
	Counters             map[string]int             `json:"counters"`
	ResourceSummary      lkeCapacityResourceSummary `json:"resource_summary"`
	Bottleneck           string                     `json:"bottleneck_classification"`
	CapacityCoefficients map[string]float64         `json:"capacity_coefficients,omitempty"`
	FormulaInputs        map[string]any             `json:"formula_inputs"`
	NextDecisionGuidance string                     `json:"next_decision_guidance"`
}

type lkeCapacityRequested struct {
	TargetDevices             int    `json:"target_devices"`
	Users                     int    `json:"users"`
	DevicesPerUser            int    `json:"devices_per_user"`
	LoadGeneratorVMs          int    `json:"load_generator_vms"`
	LoadGeneratorDevicesPerVM int    `json:"load_generator_devices_per_vm"`
	MQTTReplicas              int    `json:"mqtt_replicas"`
	NodeCount                 int    `json:"node_count"`
	NodeType                  string `json:"node_type"`
}

type lkeCapacityOutcome struct {
	Status                 string `json:"status"`
	Result                 string `json:"result"`
	Complete               bool   `json:"complete"`
	Success                bool   `json:"success"`
	LoadGeneratorSaturated bool   `json:"load_generator_saturated"`
	ServerEvidenceComplete bool   `json:"server_evidence_complete"`
	ServerCorrelation      string `json:"server_correlation"`
	RuntimeLogCorrelation  string `json:"runtime_log_correlation"`
}

type lkeCapacityResourceSummary struct {
	LoadGenerator map[string]lkeCapacityResourceStats `json:"load_generator_vms,omitempty"`
	K8sNodes      map[string]lkeCapacityResourceStats `json:"k8s_nodes,omitempty"`
	Pods          map[string]lkeCapacityPodStats      `json:"pods,omitempty"`
	PodStatuses   []lkeCapacityPodStatus              `json:"pod_statuses,omitempty"`
	HAProxy       map[string]int                      `json:"haproxy,omitempty"`
}

type lkeCapacityResourceStats struct {
	Samples int     `json:"samples"`
	CPUP95  float64 `json:"cpu_p95_percent,omitempty"`
	CPUMax  float64 `json:"cpu_max_percent,omitempty"`
	MemP95  float64 `json:"memory_p95_percent,omitempty"`
	MemMax  float64 `json:"memory_max_percent,omitempty"`
}

type lkeCapacityPodStats struct {
	Namespace        string  `json:"namespace"`
	Samples          int     `json:"samples"`
	CPUP95Milli      float64 `json:"cpu_p95_millicores"`
	CPUMaxMilli      float64 `json:"cpu_max_millicores"`
	MemoryP95Mi      float64 `json:"memory_p95_mi"`
	MemoryMaxMi      float64 `json:"memory_max_mi"`
	WorkloadCategory string  `json:"workload_category"`
}

type lkeCapacityPodStatus struct {
	Namespace                string `json:"namespace"`
	Name                     string `json:"name"`
	WorkloadCategory         string `json:"workload_category"`
	RestartCount             int    `json:"restart_count"`
	LastTerminatedReason     string `json:"last_terminated_reason,omitempty"`
	LastTerminatedExitCode   int    `json:"last_terminated_exit_code,omitempty"`
	LastTerminatedFinishedAt string `json:"last_terminated_finished_at,omitempty"`
	OOMKilled                bool   `json:"oom_killed,omitempty"`
}

func runLKECapacityRunSummary(args []string) error {
	fs := flag.NewFlagSet("lke-capacity-run-summary", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	runDir := fs.String("run-dir", "", "home-100k report directory containing results.json")
	envRoot := fs.String("env-root", "", "environment root")
	out := fs.String("out", "", "summary JSON path; default: <run-dir>/capacity-run-summary.json")
	targetDevices := fs.Int("target-devices", 0, "target devices override")
	mqttReplicas := fs.Int("mqtt-pods", 0, "MQTT replica count used for the run")
	nodeCount := fs.Int("node-count", 0, "LKE node count used for the run")
	nodeType := fs.String("node-type", "", "LKE node type used for the run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*runDir) == "" {
		return errors.New("--run-dir is required")
	}
	absRunDir, err := filepath.Abs(*runDir)
	if err != nil {
		return err
	}
	if *out == "" {
		*out = filepath.Join(absRunDir, "capacity-run-summary.json")
	}
	summary, err := buildLKECapacityRunSummary(absRunDir, strings.TrimSpace(*envRoot), *targetDevices, *mqttReplicas, *nodeCount, strings.TrimSpace(*nodeType))
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil {
		return err
	}
	if err := writeJSON(*out, summary); err != nil {
		return err
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(summary)
}

func buildLKECapacityRunSummary(runDir, envRoot string, targetDevices, mqttReplicas, nodeCount int, nodeType string) (lkeCapacityRunSummary, error) {
	resultsPath := filepath.Join(runDir, "results.json")
	results, err := readJSONMap(resultsPath)
	if err != nil {
		return lkeCapacityRunSummary{}, err
	}
	conditions := mapValue(mapValue(results, "plan"), "conditions")
	if targetDevices <= 0 {
		targetDevices = lkeCapacityIntValue(conditions["devices"])
	}
	users := lkeCapacityIntValue(conditions["users"])
	devicesPerUser := lkeCapacityIntValue(conditions["devices_per_user"])
	loadGeneratorVMs := lkeCapacityIntValue(conditions["vm_count"])
	loadGeneratorDevicesPerVM := lkeCapacityIntValue(conditions["load_generator_devices_per_vm"])
	if envRoot == "" {
		envRoot = stringValue(conditions["env_root"])
	}
	if mqttReplicas <= 0 {
		mqttReplicas = envIntFromFile(envRoot, "LKE_MQTT_REPLICAS")
	}
	if nodeCount <= 0 {
		nodeCount = envIntFromFile(envRoot, "LKE_NODE_COUNT")
	}
	if nodeType == "" {
		nodeType = envValueFromFile(envRoot, "LKE_NODE_TYPE")
	}
	runID := firstNonEmpty(stringValue(results["run_id"]), filepath.Base(runDir))
	serverEvidenceFile := firstNonEmpty(stringValue(results["server_evidence_file"]), filepath.Join(runDir, "server-evidence.json"))
	serverEvidence := mapValue(results, "server_evidence")
	sources := mapValue(serverEvidence, "sources")
	counters := lkeCapacityCounters(results, sources)
	resourceSummary := lkeCapacityResources(runDir, envRoot, sources)
	outcome := lkeCapacityOutcome{
		Status:                 stringValue(results["status"]),
		Result:                 stringValue(results["result"]),
		LoadGeneratorSaturated: boolValue(mapValue(results, "load_generator_health")["saturated"]),
		ServerEvidenceComplete: boolValue(serverEvidence["complete"]),
		ServerCorrelation:      stringValue(mapValue(results, "server_correlation")["status"]),
		RuntimeLogCorrelation:  stringValue(mapValue(results, "runtime_log_correlation")["status"]),
	}
	outcome.Complete = strings.EqualFold(outcome.Status, "COMPLETE")
	outcome.Success = strings.EqualFold(outcome.Result, "SUCCESS")
	gates := map[string]string{
		"status":                  outcome.Status,
		"result":                  outcome.Result,
		"server_correlation":      outcome.ServerCorrelation,
		"runtime_log_correlation": outcome.RuntimeLogCorrelation,
	}
	coefficients := map[string]float64{}
	if outcome.Complete && outcome.Success && !outcome.LoadGeneratorSaturated && targetDevices > 0 {
		if loadGeneratorVMs > 0 {
			coefficients["safe_devices_per_load_generator_vm"] = float64(targetDevices) / float64(loadGeneratorVMs)
		}
		if mqttReplicas > 0 {
			coefficients["safe_devices_per_mqtt_pod"] = float64(targetDevices) / float64(mqttReplicas)
		}
		if nodeCount > 0 {
			coefficients["safe_devices_per_node"] = float64(targetDevices) / float64(nodeCount)
		}
	}
	if len(coefficients) == 0 {
		coefficients = nil
	}
	summary := lkeCapacityRunSummary{
		Schema:             "rtk-cloud-workspace.lke-capacity-run-summary/v1",
		GeneratedAt:        time.Now().UTC().Format(time.RFC3339),
		RunID:              runID,
		RunDir:             runDir,
		ReportFile:         firstNonEmpty(stringValue(results["report_file"]), filepath.Join(runDir, "test_report.md")),
		ResultsFile:        resultsPath,
		ServerEvidenceFile: serverEvidenceFile,
		EnvRoot:            envRoot,
		Requested: lkeCapacityRequested{
			TargetDevices: targetDevices, Users: users, DevicesPerUser: devicesPerUser,
			LoadGeneratorVMs: loadGeneratorVMs, LoadGeneratorDevicesPerVM: loadGeneratorDevicesPerVM,
			MQTTReplicas: mqttReplicas, NodeCount: nodeCount, NodeType: nodeType,
		},
		Outcome:              outcome,
		Gates:                gates,
		Counters:             counters,
		ResourceSummary:      resourceSummary,
		Bottleneck:           classifyLKECapacityBottleneck(outcome, counters, resourceSummary),
		CapacityCoefficients: coefficients,
		FormulaInputs: map[string]any{
			"users":              "ceil(devices / devices_per_user)",
			"load_generator_vms": "ceil(devices / measured_safe_devices_per_generator_vm)",
			"required_mqtt_pods": "ceil(devices / measured_safe_devices_per_mqtt_pod)",
			"required_nodes":     "max(cpu_nodes, memory_nodes, required_mqtt_pods, spread_min)",
		},
	}
	summary.NextDecisionGuidance = lkeCapacityNextDecision(summary)
	return summary, nil
}

func lkeCapacityCounters(results map[string]any, sources map[string]any) map[string]int {
	counters := map[string]int{}
	copyCounter := func(name string, value any) {
		counters[name] = lkeCapacityIntValue(value)
	}
	for _, entry := range []struct {
		source string
		keys   []string
	}{
		{"video_cloud_api", []string{"video_cloud_api.request_token.total", "video_cloud_api.request_token.status_200", "video_cloud_api.request_token.status_500", "video_cloud_api.request_token.gt1s", "video_cloud_api.request_token.gt5s", "video_cloud_api.request_token.gt10s"}},
		{"emqx", []string{"mqtt.total_connect_attempts", "mqtt.total_connect_success", "device_mqtt.connect_attempts", "device_mqtt.connect_success"}},
		{"iot_device_shadow", []string{"app_user.desired_writes", "app_user.received_acks", "device_mqtt.delta_received", "device_mqtt.reported_publishes"}},
	} {
		sourceCounters := mapValue(mapValue(sources, entry.source), "counters")
		for _, key := range entry.keys {
			if value, ok := sourceCounters[key]; ok {
				copyCounter(key, value)
			}
		}
	}
	deviceTotals := mapValue(results, "device_mqtt_totals")
	if nested := mapValue(deviceTotals, "total"); len(nested) > 0 {
		deviceTotals = nested
	}
	for _, key := range []string{"connect_attempts", "connect_success", "connect_fail", "subscribes", "delta_received", "reported_publishes"} {
		if value, ok := deviceTotals[key]; ok {
			copyCounter("client.device_mqtt."+key, value)
		}
	}
	appTotals := mapValue(results, "app_user_totals")
	if nested := mapValue(appTotals, "total"); len(nested) > 0 {
		appTotals = nested
	}
	for _, key := range []string{"login_attempts", "login_success", "login_fail", "desired_writes", "received_acks"} {
		if value, ok := appTotals[key]; ok {
			copyCounter("client.app_user."+key, value)
		}
	}
	for _, stage := range mapSlice(results["stage_results"]) {
		for detail, count := range mapValue(mapValue(stage, "failure_details"), "runner_failed") {
			if strings.Contains(strings.ToLower(detail), "timed_out_after") || strings.Contains(strings.ToLower(detail), "timed out after") {
				counters["runner.timeout_failures"] += lkeCapacityIntValue(count)
			}
		}
	}
	return counters
}

func lkeCapacityResources(runDir, envRoot string, sources map[string]any) lkeCapacityResourceSummary {
	hostPodResources := mapValue(sources, "host_pod_resources")
	return lkeCapacityResourceSummary{
		LoadGenerator: parseLoadVMResourceTSV(filepath.Join(runDir, "resource-samples", "load-vms.tsv")),
		K8sNodes:      parseK8sNodeResourceTSV(filepath.Join(runDir, "resource-samples", "k8s-nodes.tsv")),
		Pods:          parsePodResourceSamples(mapSlice(hostPodResources["samples"])),
		PodStatuses:   collectLKECapacityPodStatuses(envRoot),
		HAProxy:       intMap(mapValue(mapValue(sources, "edge_haproxy"), "counters")),
	}
}

func parseLoadVMResourceTSV(path string) map[string]lkeCapacityResourceStats {
	rows := readTSV(path)
	byLabel := map[string][]float64{}
	memByLabel := map[string][]float64{}
	for _, row := range rows {
		if row["status"] != "ok" {
			continue
		}
		label := row["label"]
		if label == "" {
			continue
		}
		byLabel[label] = append(byLabel[label], lkeCapacityFloatValue(row["cpu_pct"]))
		total := lkeCapacityFloatValue(row["mem_total_mb"])
		if total > 0 {
			memByLabel[label] = append(memByLabel[label], lkeCapacityFloatValue(row["mem_used_mb"])*100.0/total)
		}
	}
	out := map[string]lkeCapacityResourceStats{}
	for label, samples := range byLabel {
		out[label] = lkeCapacityResourceStats{Samples: len(samples), CPUP95: percentile(samples, 95), CPUMax: maxFloat(samples), MemP95: percentile(memByLabel[label], 95), MemMax: maxFloat(memByLabel[label])}
	}
	return out
}

func parseK8sNodeResourceTSV(path string) map[string]lkeCapacityResourceStats {
	rows := readTSV(path)
	cpuByNode := map[string][]float64{}
	memByNode := map[string][]float64{}
	for _, row := range rows {
		if row["status"] != "ok" {
			continue
		}
		name := row["name"]
		if name == "" {
			continue
		}
		cpuByNode[name] = append(cpuByNode[name], lkeCapacityFloatValue(row["cpu_pct"]))
		memByNode[name] = append(memByNode[name], lkeCapacityFloatValue(row["mem_pct"]))
	}
	out := map[string]lkeCapacityResourceStats{}
	for name, samples := range cpuByNode {
		out[name] = lkeCapacityResourceStats{Samples: len(samples), CPUP95: percentile(samples, 95), CPUMax: maxFloat(samples), MemP95: percentile(memByNode[name], 95), MemMax: maxFloat(memByNode[name])}
	}
	return out
}

func parsePodResourceSamples(samples []map[string]any) map[string]lkeCapacityPodStats {
	type aggregate struct {
		namespace string
		category  string
		cpu       []float64
		memoryMi  []float64
	}
	byPod := map[string]*aggregate{}
	order := []string{}
	add := func(key string, namespace string, category string, cpu float64, memoryMi float64) {
		if _, ok := byPod[key]; !ok {
			byPod[key] = &aggregate{namespace: namespace, category: category}
			order = append(order, key)
		}
		byPod[key].cpu = append(byPod[key].cpu, cpu)
		byPod[key].memoryMi = append(byPod[key].memoryMi, memoryMi)
	}
	for _, sample := range samples {
		pod := stringValue(sample["pod"])
		namespace := stringValue(sample["namespace"])
		if pod == "" {
			continue
		}
		category := capacityPodCategory(pod)
		if category == "" {
			continue
		}
		key := namespace + "/" + pod
		add(key, namespace, category, lkeCapacityFloatValue(sample["cpu_millicores"]), lkeCapacityFloatValue(sample["memory_bytes"])/1024.0/1024.0)
	}
	out := map[string]lkeCapacityPodStats{}
	for _, key := range order {
		item := byPod[key]
		out[key] = lkeCapacityPodStats{
			Namespace:        item.namespace,
			Samples:          len(item.cpu),
			CPUP95Milli:      percentile(item.cpu, 95),
			CPUMaxMilli:      maxFloat(item.cpu),
			MemoryP95Mi:      percentile(item.memoryMi, 95),
			MemoryMaxMi:      maxFloat(item.memoryMi),
			WorkloadCategory: item.category,
		}
	}
	return out
}

func capacityPodCategory(pod string) string {
	for _, item := range []struct{ prefix, category string }{
		{"mqtt-", "mqtt"},
		{"video-cloud-api-", "video-cloud-api"},
		{"postgresql-", "postgres"},
		{"account-manager-", "account-manager"},
		{"ingress-nginx-controller-", "ingress"},
		{"cloud-logger-", "cloud-logger"},
	} {
		if strings.HasPrefix(pod, item.prefix) {
			return item.category
		}
	}
	return ""
}

func collectLKECapacityPodStatuses(envRoot string) []lkeCapacityPodStatus {
	if strings.TrimSpace(envRoot) == "" {
		return nil
	}
	kubeconfig := filepath.Join(envRoot, "state", "kubeconfig.yaml")
	if _, err := os.Stat(kubeconfig); err != nil {
		return nil
	}
	out, err := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "--request-timeout=10s", "get", "pods", "-A", "-o", "json").Output()
	if err != nil {
		return nil
	}
	var pods struct {
		Items []struct {
			Metadata struct {
				Name      string `json:"name"`
				Namespace string `json:"namespace"`
			} `json:"metadata"`
			Status struct {
				ContainerStatuses []struct {
					RestartCount int `json:"restartCount"`
					LastState    struct {
						Terminated struct {
							Reason     string `json:"reason"`
							ExitCode   int    `json:"exitCode"`
							FinishedAt string `json:"finishedAt"`
						} `json:"terminated"`
					} `json:"lastState"`
				} `json:"containerStatuses"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &pods); err != nil {
		return nil
	}
	statuses := []lkeCapacityPodStatus{}
	for _, item := range pods.Items {
		category := capacityPodCategory(item.Metadata.Name)
		if category == "" {
			continue
		}
		status := lkeCapacityPodStatus{
			Namespace:        item.Metadata.Namespace,
			Name:             item.Metadata.Name,
			WorkloadCategory: category,
		}
		for _, container := range item.Status.ContainerStatuses {
			status.RestartCount += container.RestartCount
			if container.LastState.Terminated.Reason != "" {
				status.LastTerminatedReason = container.LastState.Terminated.Reason
				status.LastTerminatedExitCode = container.LastState.Terminated.ExitCode
				status.LastTerminatedFinishedAt = container.LastState.Terminated.FinishedAt
				if container.LastState.Terminated.Reason == "OOMKilled" {
					status.OOMKilled = true
				}
			}
		}
		if status.RestartCount > 0 || status.LastTerminatedReason != "" {
			statuses = append(statuses, status)
		}
	}
	sort.Slice(statuses, func(i, j int) bool {
		if statuses[i].WorkloadCategory != statuses[j].WorkloadCategory {
			return statuses[i].WorkloadCategory < statuses[j].WorkloadCategory
		}
		return statuses[i].Name < statuses[j].Name
	})
	return statuses
}

func readTSV(path string) []map[string]string {
	file, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer file.Close()
	reader := csv.NewReader(bufio.NewReader(file))
	reader.Comma = '\t'
	reader.FieldsPerRecord = -1
	header, err := reader.Read()
	if err != nil {
		return nil
	}
	rows := []map[string]string{}
	for {
		record, err := reader.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			continue
		}
		row := map[string]string{}
		for i, key := range header {
			if i < len(record) {
				row[key] = record[i]
			}
		}
		rows = append(rows, row)
	}
	return rows
}

func classifyLKECapacityBottleneck(outcome lkeCapacityOutcome, counters map[string]int, resources lkeCapacityResourceSummary) string {
	if outcome.Success {
		return "none"
	}
	if resourceAnyPodOOMKilled(resources.PodStatuses, "mqtt") {
		return "mqtt_pod_oom"
	}
	if resourceAnyPodOOMKilled(resources.PodStatuses, "cloud-logger") {
		return "cloud_logger_oom"
	}
	if outcome.LoadGeneratorSaturated || resourceAnyCPUAbove(resources.LoadGenerator, 90) {
		return "load_generator"
	}
	if counters["runner.timeout_failures"] > 0 {
		return "runner_timeout"
	}
	if counters["client.device_mqtt.connect_fail"] > 0 || counters["mqtt.total_connect_attempts"] > counters["mqtt.total_connect_success"] {
		return "mqtt_emqx"
	}
	if counters["client.app_user.desired_writes"] > counters["client.app_user.received_acks"] || counters["app_user.desired_writes"] > counters["app_user.received_acks"] {
		return "api_postgres_shadow"
	}
	if counters["video_cloud_api.request_token.status_500"] > 0 || counters["video_cloud_api.request_token.gt5s"] > 0 {
		return "api_request_token"
	}
	if !outcome.ServerEvidenceComplete || outcome.ServerCorrelation == "incomplete" || outcome.RuntimeLogCorrelation == "incomplete" {
		return "evidence_logging"
	}
	return "unknown"
}

func lkeCapacityNextDecision(summary lkeCapacityRunSummary) string {
	if summary.Outcome.Success {
		if summary.Requested.TargetDevices >= 100000 {
			return "100K passed; run a reduction binary-search by lowering MQTT replicas and/or node count to find the minimum PASS configuration."
		}
		return "PASS anchor; advance toward the next planned target or bisect upward toward 100K."
	}
	switch summary.Bottleneck {
	case "load_generator":
		return "Increase load-generator VM count or lower devices per generator before changing server capacity."
	case "mqtt_emqx":
		return "Increase MQTT replicas and node spread, then rerun the same target."
	case "mqtt_pod_oom":
		return "Increase MQTT pod memory request/limit and keep one MQTT pod per node, then rerun the same target before changing target size."
	case "api_postgres_shadow", "api_request_token":
		return "Tune API/Postgres/shadow resources before increasing MQTT replicas."
	case "runner_timeout":
		return "Increase live runner timeout grace and rerun the same target before changing server capacity."
	case "evidence_logging":
		return "Fix logger/server-evidence collection before using this run for capacity coefficients."
	case "cloud_logger_oom":
		return "Increase cloud-logger memory or switch to a durable logger backend, then rerun the same target; do not use this run as a safe capacity coefficient."
	default:
		return "Classify failure from shard logs and server evidence before changing the sizing model."
	}
}

func resourceAnyPodOOMKilled(statuses []lkeCapacityPodStatus, category string) bool {
	for _, status := range statuses {
		if status.WorkloadCategory == category && status.OOMKilled {
			return true
		}
	}
	return false
}

func resourceAnyCPUAbove(resources map[string]lkeCapacityResourceStats, threshold float64) bool {
	for _, item := range resources {
		if item.CPUP95 >= threshold || item.CPUMax >= threshold {
			return true
		}
	}
	return false
}

func percentile(values []float64, pct float64) float64 {
	clean := []float64{}
	for _, value := range values {
		if !math.IsNaN(value) {
			clean = append(clean, value)
		}
	}
	if len(clean) == 0 {
		return 0
	}
	sort.Float64s(clean)
	idx := int(math.Ceil((pct/100.0)*float64(len(clean)))) - 1
	if idx < 0 {
		idx = 0
	}
	if idx >= len(clean) {
		idx = len(clean) - 1
	}
	return math.Round(clean[idx]*10) / 10
}

func maxFloat(values []float64) float64 {
	max := 0.0
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return math.Round(max*10) / 10
}

func lkeCapacityFloatValue(value any) float64 {
	switch v := value.(type) {
	case string:
		out, _ := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return out
	case float64:
		return v
	case int:
		return float64(v)
	case json.Number:
		out, _ := v.Float64()
		return out
	default:
		return 0
	}
}

func lkeCapacityIntValue(value any) int {
	switch v := value.(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		out, _ := v.Int64()
		return int(out)
	case string:
		out, _ := strconv.Atoi(strings.TrimSpace(v))
		return out
	default:
		return 0
	}
}

func intMap(values map[string]any) map[string]int {
	out := map[string]int{}
	for key, value := range values {
		out[key] = lkeCapacityIntValue(value)
	}
	return out
}

func mapValue(values map[string]any, key string) map[string]any {
	if values == nil {
		return map[string]any{}
	}
	if nested, ok := values[key].(map[string]any); ok {
		return nested
	}
	return map[string]any{}
}

func mapSlice(value any) []map[string]any {
	raw, _ := value.([]any)
	out := []map[string]any{}
	for _, item := range raw {
		if obj, ok := item.(map[string]any); ok {
			out = append(out, obj)
		}
	}
	return out
}

func boolValue(value any) bool {
	v, _ := value.(bool)
	return v
}

func envValueFromFile(envRoot, key string) string {
	if envRoot == "" {
		return ""
	}
	values, _ := readEnvFile(filepath.Join(envRoot, "env", "stack.env"))
	return strings.TrimSpace(values[key])
}

func envIntFromFile(envRoot, key string) int {
	raw := envValueFromFile(envRoot, key)
	value, _ := strconv.Atoi(raw)
	return value
}
