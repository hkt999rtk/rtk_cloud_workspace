package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hkt999rtk/rtk_cloud_workspace/e2e_test/factory_enroll/factoryenrolltest"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return usage()
	}
	switch args[0] {
	case "run":
		return runEnroll(args[1:])
	case "report":
		return runReport(args[1:])
	default:
		return usage()
	}
}

func usage() error {
	return fmt.Errorf("usage: rtk-factory-enroll-test <run|report> [flags]")
}

func runEnroll(args []string) error {
	cfg := factoryenrolltest.DefaultConfigFromEnv()
	var output, reportOutput string
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.StringVar(&cfg.FactoryURL, "factory-url", cfg.FactoryURL, "factory enrollment base URL")
	fs.StringVar(&cfg.AuthKey, "auth-key", cfg.AuthKey, "factory enrollment HMAC auth key")
	fs.IntVar(&cfg.Count, "count", cfg.Count, "number of generated devices to enroll")
	fs.IntVar(&cfg.Concurrency, "concurrency", cfg.Concurrency, "parallel enrollment workers")
	fs.StringVar(&cfg.RunID, "run-id", cfg.RunID, "test run id")
	fs.StringVar(&cfg.FactoryID, "factory-id", cfg.FactoryID, "factory metadata")
	fs.StringVar(&cfg.LineID, "line-id", cfg.LineID, "line metadata")
	fs.StringVar(&cfg.StationID, "station-id", cfg.StationID, "station metadata")
	fs.StringVar(&cfg.FixtureID, "fixture-id", cfg.FixtureID, "fixture metadata")
	fs.StringVar(&cfg.OperatorID, "operator-id", cfg.OperatorID, "operator metadata")
	fs.StringVar(&cfg.BatchID, "batch-id", cfg.BatchID, "batch metadata; defaults from run id")
	fs.StringVar(&cfg.SerialPrefix, "serial-prefix", cfg.SerialPrefix, "serial number prefix")
	fs.DurationVar(&cfg.Timeout, "timeout", cfg.Timeout, "per-device enrollment timeout")
	fs.StringVar(&cfg.ArtifactDir, "artifact-dir", cfg.ArtifactDir, "artifact directory")
	fs.BoolVar(&cfg.WriteKeyFiles, "write-key-files", cfg.WriteKeyFiles, "write generated private keys and CSRs to artifact-dir/device-material")
	fs.StringVar(&output, "output", "", "JSON output path")
	fs.StringVar(&reportOutput, "report-output", "", "Markdown report output path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if cfg.ArtifactDir == "" {
		cfg.ArtifactDir = filepath.Join(".artifacts", "e2e_test", "factory_enroll", cfg.RunID)
	}
	if output == "" {
		output = filepath.Join(cfg.ArtifactDir, "factory-enroll-results.json")
	}
	if reportOutput == "" {
		reportOutput = filepath.Join(cfg.ArtifactDir, "factory-enroll-report.md")
	}
	if err := os.MkdirAll(cfg.ArtifactDir, 0o755); err != nil {
		return err
	}
	ctx := context.Background()
	result, err := factoryenrolltest.NewRunner(nil).Run(ctx, cfg)
	if err != nil {
		return err
	}
	if err := factoryenrolltest.WriteJSON(output, result); err != nil {
		return err
	}
	if err := factoryenrolltest.WriteMarkdown(reportOutput, result); err != nil {
		return err
	}
	fmt.Printf("factory enrollment test complete: total=%d success=%d failure=%d output=%s report=%s\n", result.Summary.Total, result.Summary.Successes, result.Summary.Failures, output, reportOutput)
	if result.Summary.Failures > 0 {
		return fmt.Errorf("factory enrollment test failed: %d/%d failed", result.Summary.Failures, result.Summary.Total)
	}
	return nil
}

func runReport(args []string) error {
	var input, output string
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.StringVar(&input, "input", "factory-enroll-results.json", "JSON result input path")
	fs.StringVar(&output, "output", "factory-enroll-report.md", "Markdown report output path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	result, err := factoryenrolltest.ReadJSON(input)
	if err != nil {
		return err
	}
	if result.EndedAt.IsZero() {
		result.EndedAt = time.Now().UTC()
	}
	return factoryenrolltest.WriteMarkdown(output, result)
}
