package bulkbindvalidation

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

func ReadArtifact(path string) (Artifact, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Artifact{}, err
	}
	var artifact Artifact
	if err := json.Unmarshal(b, &artifact); err != nil {
		return Artifact{}, err
	}
	return artifact, nil
}

func WriteJSON(path string, result Result) error {
	b, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func WriteMarkdown(path string, result Result) error {
	return os.WriteFile(path, []byte(RenderMarkdown(result)), 0o600)
}

func RenderMarkdown(result Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Bulk Device Bind Validation Report\n\n")
	fmt.Fprintf(&b, "- Schema: `%s`\n", result.Schema)
	fmt.Fprintf(&b, "- Started: `%s`\n", result.StartedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(&b, "- Ended: `%s`\n", result.EndedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(&b, "- Overall: `%s`\n", result.Overall)
	fmt.Fprintf(&b, "- Brand: `%s`\n", safeCell(result.Summary.BrandName))
	fmt.Fprintf(&b, "- Brand cloud: `%s`\n", safeCell(result.Summary.BrandCloudID))
	fmt.Fprintf(&b, "\n## Summary\n\n")
	fmt.Fprintf(&b, "| Metric | Value |\n| --- | ---: |\n")
	fmt.Fprintf(&b, "| Total devices | %d |\n", result.Summary.TotalDevices)
	fmt.Fprintf(&b, "| Users | %d |\n", result.Summary.Users)
	fmt.Fprintf(&b, "| Provision requested | %d |\n", result.Summary.ProvisionRequested)
	fmt.Fprintf(&b, "| MQTT-only devices | %d |\n", result.Summary.MQTTOnlyDevices)
	fmt.Fprintf(&b, "| Video-capable devices | %d |\n", result.Summary.VideoDevices)
	fmt.Fprintf(&b, "\n## Checks\n\n")
	fmt.Fprintf(&b, "| Check | Status | Evidence |\n")
	fmt.Fprintf(&b, "| --- | --- | --- |\n")
	for _, check := range result.Checks {
		fmt.Fprintf(&b, "| `%s` | `%s` | %s |\n", check.Name, check.Status, safeCell(check.Evidence))
	}
	fmt.Fprintf(&b, "\n## User Distribution\n\n")
	fmt.Fprintf(&b, "| User | Devices |\n")
	fmt.Fprintf(&b, "| --- | ---: |\n")
	for _, user := range result.UserCounts {
		fmt.Fprintf(&b, "| `%s` | %d |\n", safeCell(user.Email), user.DeviceCount)
	}
	fmt.Fprintf(&b, "\n## Artifacts\n\n")
	fmt.Fprintf(&b, "- JSON result: `bulk-bind-validation-results.json`\n")
	fmt.Fprintf(&b, "- Markdown report: `bulk-bind-validation-report.md`\n")
	fmt.Fprintf(&b, "- Reports contain redacted API-level identifiers only; credential material and local key paths are omitted.\n")
	return b.String()
}

func safeCell(value string) string {
	value = strings.ReplaceAll(value, "|", "\\|")
	value = strings.ReplaceAll(value, "\n", " ")
	if len(value) > 180 {
		return value[:177] + "..."
	}
	return value
}
