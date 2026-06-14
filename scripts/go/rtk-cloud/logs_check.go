package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const logsCheckRetiredMessage = "staging VM logs-check retired; use MQTT persisted log verification or K8s observability flow"

type logsCheckItem struct {
	Name     string   `json:"name"`
	Target   string   `json:"target"`
	Status   string   `json:"status"`
	Detail   string   `json:"detail,omitempty"`
	Artifact string   `json:"artifact,omitempty"`
	Secrets  []string `json:"secrets,omitempty"`
}

type logsCheckResult struct {
	GeneratedAt string          `json:"generated_at"`
	Status      string          `json:"status"`
	ArtifactDir string          `json:"artifact_dir"`
	Checks      []logsCheckItem `json:"checks"`
}

func runLogsCheck(args []string) error {
	fs := flag.NewFlagSet("logs-check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	envRootFlag := fs.String("env-root", "", "environment root")
	outDirFlag := fs.String("out-dir", "", "output directory")
	jsonOnly := fs.Bool("json", false, "write summary JSON to stdout only")
	fs.String("since", "15m", "retired")
	fs.Int("tail", 300, "retired")
	fs.String("ssh-key", "", "retired")
	fs.Bool("fail-on-secret", true, "retired")
	fs.Bool("skip-traffic", false, "retired")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required")
	}
	outDir := *outDirFlag
	if outDir == "" {
		workspace, err := workspaceRoot()
		if err != nil {
			return err
		}
		envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
		if err != nil {
			return err
		}
		outDir = filepath.Join(envRoot, "artifacts", "logs-check-retired", time.Now().UTC().Format("20060102T150405Z"))
	}
	result := logsCheckResult{
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Status:      "retired",
		ArtifactDir: outDir,
		Checks: []logsCheckItem{{
			Name:   "logs-check-retired",
			Target: "k8s-staging",
			Status: "retired",
			Detail: logsCheckRetiredMessage,
		}},
	}
	if err := writeLogsCheckArtifacts(outDir, result); err != nil {
		return err
	}
	if *jsonOnly {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(result); err != nil {
			return err
		}
		return errors.New(logsCheckRetiredMessage)
	}
	fmt.Fprintf(os.Stderr, "[logs-check] retired: %s\n", logsCheckRetiredMessage)
	fmt.Fprintf(os.Stderr, "[logs-check] report: %s\n", filepath.Join(outDir, "report.md"))
	fmt.Fprintf(os.Stdout, "%s\n", outDir)
	return errors.New(logsCheckRetiredMessage)
}

func scanLogSecrets(body string) []string {
	patterns := []string{
		`(?i)authorization:\s*bearer\s+[^<\s][^\s]+`,
		`(?i)\bpassword\s*=\s*[^<\s][^\s]+`,
		`(?i)\btoken\s*=\s*[^<\s][^\s]+`,
		`(?i)\bsecret\s*=\s*[^<\s][^\s]+`,
		`(?i)BEGIN [A-Z ]*PRIVATE KEY`,
		`(?i)AWS_SECRET_ACCESS_KEY\s*=\s*[^<\s][^\s]+`,
		`(?i)LINODE_TOKEN\s*=\s*[^<\s][^\s]+`,
		`(?i)GODADDY_SECRET\s*=\s*[^<\s][^\s]+`,
	}
	allow := regexp.MustCompile(`(?i)(<redacted>|redacted|\*{3,}|=\s*$|bearer\s+<redacted>)`)
	hits := []string{}
	for _, pattern := range patterns {
		re := regexp.MustCompile(pattern)
		for _, match := range re.FindAllString(body, -1) {
			if allow.MatchString(match) {
				continue
			}
			hits = append(hits, redactLogSecretHit(match))
		}
	}
	sort.Strings(hits)
	return uniqueStrings(hits)
}

func redactLogSecretHit(hit string) string {
	hit = strings.TrimSpace(hit)
	if len(hit) <= 32 {
		return hit
	}
	return hit[:32] + "..."
}

func uniqueStrings(items []string) []string {
	out := []string{}
	for _, item := range items {
		if len(out) == 0 || out[len(out)-1] != item {
			out = append(out, item)
		}
	}
	return out
}

func writeLogsCheckArtifacts(outDir string, result logsCheckResult) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outDir, "summary.json"), result); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "report.md"), []byte(renderLogsCheckReport(result)), 0o644)
}

func renderLogsCheckReport(result logsCheckResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# Logs Check Report\n\n")
	fmt.Fprintf(&b, "- generated_at: %s\n", result.GeneratedAt)
	fmt.Fprintf(&b, "- status: %s\n", result.Status)
	fmt.Fprintf(&b, "- artifact_dir: `%s`\n\n", result.ArtifactDir)
	fmt.Fprintf(&b, "## Checks\n\n")
	fmt.Fprintf(&b, "| Check | Target | Status | Detail | Artifact |\n")
	fmt.Fprintf(&b, "| --- | --- | --- | --- | --- |\n")
	for _, check := range result.Checks {
		detail := check.Detail
		if len(check.Secrets) > 0 {
			detail = strings.TrimSpace(detail + " " + strings.Join(check.Secrets, ", "))
		}
		fmt.Fprintf(&b, "| `%s` | `%s` | `%s` | %s | `%s` |\n", check.Name, check.Target, check.Status, markdownCell(detail), check.Artifact)
	}
	return b.String()
}

func markdownCell(value string) string {
	value = strings.ReplaceAll(value, "\n", " ")
	value = strings.ReplaceAll(value, "|", "\\|")
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return "`" + value + "`"
}

func logShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
