package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type mqttTraceResults struct {
	Status      string            `json:"status"`
	Overall     string            `json:"overall"`
	GeneratedAt string            `json:"generated_at"`
	Brandname   string            `json:"brandname"`
	Profile     string            `json:"profile"`
	MQTT        mqttTraceSummary  `json:"mqtt"`
	Metrics     map[string]any    `json:"metrics"`
	Devices     []mqttTraceDevice `json:"devices"`
	ReportFile  string            `json:"report_file"`
	ResultsFile string            `json:"results_file"`
}

type mqttTraceSummary struct {
	ProbeResult        string `json:"probe_result"`
	ProbeModel         string `json:"probe_model"`
	ClientIdentityMode string `json:"client_identity_mode"`
	TelemetryReceiver  string `json:"telemetry_receiver"`
	CommandReceiver    string `json:"command_receiver"`
}

type mqttTraceDevice struct {
	DeviceID   string          `json:"device_id"`
	DeviceType string          `json:"device_type"`
	MQTTStatus string          `json:"mqtt_status"`
	TraceChain []mqttTraceStep `json:"trace_chain"`
}

type mqttTraceStep struct {
	Step      int    `json:"step"`
	Timestamp string `json:"timestamp"`
	Phase     string `json:"phase"`
	Actor     string `json:"actor"`
	Action    string `json:"action"`
	Topic     string `json:"topic"`
	Status    string `json:"status"`
	Data      string `json:"data"`
	Detail    string `json:"detail"`
}

func runMQTTTraceReport(args []string) error {
	fs := flag.NewFlagSet("mqtt-trace-report", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	envRoot := fs.String("env-root", "", "environment root")
	brandname := fs.String("brandname", "", "brand name filter for latest artifact")
	resultsFile := fs.String("results-file", "", "home MQTT results.json")
	outFile := fs.String("out-file", "", "output markdown path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *resultsFile == "" {
		if *envRoot == "" {
			return errors.New("--env-root is required when --results-file is not set")
		}
		workspace, err := workspaceRoot()
		if err != nil {
			return err
		}
		resolvedEnv, err := resolveEnvRoot(workspace, *envRoot)
		if err != nil {
			return err
		}
		latest, err := latestMQTTTraceResultsFile(resolvedEnv, *brandname)
		if err != nil {
			return err
		}
		*resultsFile = latest
	}
	results, err := readMQTTTraceResults(*resultsFile)
	if err != nil {
		return err
	}
	report := renderMQTTTraceReport(results, *resultsFile)
	if *outFile == "" {
		*outFile = filepath.Join(filepath.Dir(*resultsFile), "E2E_TRACE_CHAIN_REPORT.md")
	}
	if err := os.WriteFile(*outFile, []byte(report), 0o644); err != nil {
		return err
	}
	summary, _ := json.Marshal(map[string]any{
		"action":       "mqtt-trace-report",
		"brandname":    results.Brandname,
		"overall":      results.Overall,
		"results_file": *resultsFile,
		"report_file":  *outFile,
	})
	fmt.Println(string(summary))
	return nil
}

func readMQTTTraceResults(path string) (mqttTraceResults, error) {
	var results mqttTraceResults
	raw, err := os.ReadFile(path)
	if err != nil {
		return results, err
	}
	if err := json.Unmarshal(raw, &results); err != nil {
		return results, err
	}
	if len(results.Devices) == 0 {
		return results, errors.New("results file has no MQTT devices")
	}
	return results, nil
}

func latestMQTTTraceResultsFile(envRoot, brandname string) (string, error) {
	pattern := filepath.Join(envRoot, "artifacts", "home-mqtt-loadtest", "*", "results.json")
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return "", err
	}
	brandLower := strings.ToLower(strings.TrimSpace(brandname))
	candidates := []string{}
	for _, path := range matches {
		if brandLower == "" {
			candidates = append(candidates, path)
			continue
		}
		results, err := readMQTTTraceResultsAllowEmpty(path)
		if err != nil {
			continue
		}
		if strings.ToLower(results.Brandname) == brandLower {
			candidates = append(candidates, path)
		}
	}
	sort.Slice(candidates, func(i, j int) bool {
		ai, _ := os.Stat(candidates[i])
		aj, _ := os.Stat(candidates[j])
		return ai.ModTime().After(aj.ModTime())
	})
	if len(candidates) == 0 {
		if brandLower != "" {
			return "", fmt.Errorf("missing home MQTT results artifact for brand %s", brandname)
		}
		return "", errors.New("missing home MQTT results artifact")
	}
	return candidates[0], nil
}

func readMQTTTraceResultsAllowEmpty(path string) (mqttTraceResults, error) {
	var results mqttTraceResults
	raw, err := os.ReadFile(path)
	if err != nil {
		return results, err
	}
	if err := json.Unmarshal(raw, &results); err != nil {
		return results, err
	}
	return results, nil
}

func renderMQTTTraceReport(results mqttTraceResults, source string) string {
	var b strings.Builder
	fmt.Fprintln(&b, "# Home MQTT E2E Trace Chain Report")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "- Source results: `%s`\n", source)
	fmt.Fprintf(&b, "- Status: `%s`\n", results.Status)
	fmt.Fprintf(&b, "- Overall: `%s`\n", results.Overall)
	fmt.Fprintf(&b, "- Generated: `%s`\n", results.GeneratedAt)
	fmt.Fprintf(&b, "- Brand: `%s`\n", results.Brandname)
	fmt.Fprintf(&b, "- Profile: `%s`\n", results.Profile)
	fmt.Fprintf(&b, "- Probe model: `%s`\n", results.MQTT.ProbeModel)
	fmt.Fprintf(&b, "- Client identity mode: `%s`\n", results.MQTT.ClientIdentityMode)
	fmt.Fprintf(&b, "- Telemetry receiver: `%s`\n", results.MQTT.TelemetryReceiver)
	fmt.Fprintf(&b, "- Command receiver: `%s`\n", results.MQTT.CommandReceiver)
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Summary")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Device | Type | MQTT | Trace steps |")
	fmt.Fprintln(&b, "| --- | --- | --- | ---: |")
	for _, device := range results.Devices {
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %d |\n", device.DeviceID, device.DeviceType, device.MQTTStatus, len(device.TraceChain))
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Trace Chain")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Device | Step | Timestamp | Phase | Actor | Action | Topic | Status | Data | Detail |")
	fmt.Fprintln(&b, "| --- | ---: | --- | --- | --- | --- | --- | --- | --- | --- |")
	for _, device := range results.Devices {
		for _, step := range device.TraceChain {
			fmt.Fprintf(&b, "| `%s` | %d | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` |\n",
				device.DeviceID,
				step.Step,
				step.Timestamp,
				step.Phase,
				step.Actor,
				step.Action,
				step.Topic,
				step.Status,
				safeMQTTTraceDetail(step.Data),
				safeMQTTTraceDetail(step.Detail),
			)
		}
	}
	return b.String()
}

func safeMQTTTraceDetail(detail string) string {
	lower := strings.ToLower(detail)
	for _, word := range []string{"access_token", "refresh_token", "password", "private", "bearer", "-----begin", "secret"} {
		if strings.Contains(lower, word) {
			return "redacted sensitive detail"
		}
	}
	return detail
}
