package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hkt999rtk/rtk_cloud_workspace/e2e_test/provisioning/bulk_bind_validation/bulkbindvalidation"
)

func main() {
	var cfg bulkbindvalidation.Config
	var outDir string
	flag.StringVar(&cfg.BindArtifactPath, "bind-artifact", "", "bulk bind artifact from cloud-bind-devices.sh")
	flag.StringVar(&outDir, "out-dir", "", "directory for JSON and Markdown reports")
	flag.IntVar(&cfg.ExpectedCount, "expected-count", 100, "expected device assignment count")
	flag.IntVar(&cfg.ExpectedDevicesPerUser, "expected-devices-per-user", 10, "expected devices assigned to each user")
	flag.Parse()

	if cfg.BindArtifactPath == "" {
		fmt.Fprintln(os.Stderr, "error: --bind-artifact is required")
		os.Exit(2)
	}
	if outDir == "" {
		outDir = filepath.Join(".artifacts", "e2e_test", "provisioning", "bulk_bind_validation", time.Now().UTC().Format("20060102T150405Z"))
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "error: create report dir: %v\n", err)
		os.Exit(2)
	}

	artifact, err := bulkbindvalidation.ReadArtifact(cfg.BindArtifactPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: read bind artifact: %v\n", err)
		os.Exit(2)
	}
	result := bulkbindvalidation.Validate(artifact, cfg)
	resultsFile := filepath.Join(outDir, "bulk-bind-validation-results.json")
	reportFile := filepath.Join(outDir, "bulk-bind-validation-report.md")
	result.Artifacts.JSONReport = resultsFile
	result.Artifacts.MarkdownReport = reportFile
	if err := bulkbindvalidation.WriteJSON(resultsFile, result); err != nil {
		fmt.Fprintf(os.Stderr, "error: write JSON report: %v\n", err)
		os.Exit(2)
	}
	if err := bulkbindvalidation.WriteMarkdown(reportFile, result); err != nil {
		fmt.Fprintf(os.Stderr, "error: write Markdown report: %v\n", err)
		os.Exit(2)
	}

	summary := map[string]any{
		"action":        "validated",
		"overall":       result.Overall,
		"total_devices": result.Summary.TotalDevices,
		"users":         result.Summary.Users,
		"results_file":  resultsFile,
		"report_file":   reportFile,
	}
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(summary); err != nil {
		fmt.Fprintf(os.Stderr, "error: write summary: %v\n", err)
		os.Exit(2)
	}
	if result.Overall != bulkbindvalidation.StatusPass {
		os.Exit(1)
	}
}
