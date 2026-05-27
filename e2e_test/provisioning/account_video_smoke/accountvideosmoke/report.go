package accountvideosmoke

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

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
	fmt.Fprintf(&b, "# Account + Video Provisioning Smoke Report\n\n")
	fmt.Fprintf(&b, "- Schema: `%s`\n", result.Schema)
	fmt.Fprintf(&b, "- Run ID: `%s`\n", result.RunID)
	fmt.Fprintf(&b, "- Started: `%s`\n", result.StartedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(&b, "- Ended: `%s`\n", result.EndedAt.Format("2006-01-02T15:04:05Z07:00"))
	fmt.Fprintf(&b, "- Overall: `%s`\n", result.Overall)
	fmt.Fprintf(&b, "- Account Manager: `%s`\n", result.Config.AccountManagerBaseURL)
	fmt.Fprintf(&b, "- Video Cloud: `%s`\n", result.Config.VideoCloudBaseURL)
	fmt.Fprintf(&b, "\n## Steps\n\n")
	fmt.Fprintf(&b, "| Step | Status | Evidence |\n")
	fmt.Fprintf(&b, "| --- | --- | --- |\n")
	for _, step := range result.Steps {
		evidence := step.Evidence
		if evidence == "" {
			evidence = step.Reason
		}
		if step.StatusCode != 0 {
			evidence = fmt.Sprintf("HTTP %d: %s", step.StatusCode, evidence)
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | %s |\n", step.Name, step.Status, safeCell(Redact(evidence)))
	}
	fmt.Fprintf(&b, "\n## Artifacts\n\n")
	fmt.Fprintf(&b, "- JSON result: `account-video-smoke-results.json`\n")
	fmt.Fprintf(&b, "- Markdown report: `account-video-smoke-report.md`\n")
	fmt.Fprintf(&b, "- Reports must not contain private keys, raw bearer tokens, raw Claim Tokens, HMAC secrets, or unredacted service credentials.\n")
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
