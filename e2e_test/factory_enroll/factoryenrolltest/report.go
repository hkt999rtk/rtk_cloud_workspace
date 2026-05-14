package factoryenrolltest

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

func WriteJSON(path string, result *Result) error {
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o644)
}

func ReadJSON(path string) (*Result, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var result Result
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, err
	}
	return &result, nil
}

func WriteMarkdown(path string, result *Result) error {
	return os.WriteFile(path, []byte(RenderMarkdown(result)), 0o644)
}

func RenderMarkdown(result *Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Factory Enrollment E2E Report\n\n")
	fmt.Fprintf(&b, "- Schema: `%s`\n", result.Schema)
	fmt.Fprintf(&b, "- Run ID: `%s`\n", result.RunID)
	fmt.Fprintf(&b, "- Started: `%s`\n", result.StartedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(&b, "- Ended: `%s`\n", result.EndedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(&b, "- Factory URL: `%s`\n", result.Config.FactoryURL)
	fmt.Fprintf(&b, "- Count: `%d`\n", result.Config.Count)
	fmt.Fprintf(&b, "- Concurrency: `%d`\n", result.Config.Concurrency)
	fmt.Fprintf(&b, "- Batch ID: `%s`\n", result.Config.BatchID)
	fmt.Fprintf(&b, "\n## Summary\n\n")
	status := "PASS"
	if result.Summary.Failures > 0 {
		status = "FAIL"
	}
	fmt.Fprintf(&b, "- Overall result: `%s`\n\n", status)
	fmt.Fprintf(&b, "| Metric | Value |\n| --- | ---: |\n")
	fmt.Fprintf(&b, "| Total devices | %d |\n", result.Summary.Total)
	fmt.Fprintf(&b, "| Successes | %d |\n", result.Summary.Successes)
	fmt.Fprintf(&b, "| Failures | %d |\n", result.Summary.Failures)
	fmt.Fprintf(&b, "| Success rate | %.2f%% |\n", result.Summary.SuccessRate*100)
	fmt.Fprintf(&b, "| p95 latency | %d ms |\n", result.Summary.P95LatencyMS)
	fmt.Fprintf(&b, "| p99 latency | %d ms |\n", result.Summary.P99LatencyMS)
	fmt.Fprintf(&b, "| Duration | %d ms |\n", result.Summary.DurationMillis)
	if len(result.Errors) > 0 {
		fmt.Fprintf(&b, "\n## Error Classes\n\n")
		keys := make([]string, 0, len(result.Errors))
		for key := range result.Errors {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		fmt.Fprintf(&b, "| Class | Count |\n| --- | ---: |\n")
		for _, key := range keys {
			fmt.Fprintf(&b, "| `%s` | %d |\n", key, result.Errors[key])
		}
	}
	fmt.Fprintf(&b, "\n## Device Results\n\n")
	fmt.Fprintf(&b, "| # | Device ID | Result | HTTP | Latency | Evidence |\n")
	fmt.Fprintf(&b, "| ---: | --- | --- | ---: | ---: | --- |\n")
	for _, device := range result.Devices {
		outcome := "PASS"
		evidence := "certificate CN/public key/clientAuth validated"
		if !device.Success {
			outcome = "FAIL"
			evidence = safeCell(device.ErrorClass + ": " + device.Error)
		}
		fmt.Fprintf(&b, "| %d | `%s` | `%s` | %d | %d ms | %s |\n", device.Index, device.DeviceID, outcome, device.StatusCode, device.LatencyMillis, evidence)
	}
	fmt.Fprintf(&b, "\n## Artifacts\n\n")
	fmt.Fprintf(&b, "- JSON result: `factory-enroll-results.json`\n")
	fmt.Fprintf(&b, "- Markdown report: `factory-enroll-report.md`\n")
	fmt.Fprintf(&b, "- Device private keys are not written unless `--write-key-files` is explicitly enabled.\n")
	return b.String()
}

func safeCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) > 160 {
		return value[:157] + "..."
	}
	return value
}
