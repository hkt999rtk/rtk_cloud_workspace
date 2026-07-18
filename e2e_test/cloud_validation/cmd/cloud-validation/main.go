package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hkt999rtk/rtk_cloud_workspace/e2e_test/cloud_validation/cloudvalidation"
)

func main() {
	var cfg cloudvalidation.Config
	var scenarios string
	flag.StringVar(&cfg.Environment, "environment", env("CLOUD_VALIDATION_ENVIRONMENT", "staging"), "deployed environment name")
	flag.StringVar(&cfg.Platform, "platform", env("CLOUD_VALIDATION_PLATFORM", ""), "ios or android")
	flag.StringVar(&cfg.Mode, "mode", env("CLOUD_VALIDATION_MODE", "source"), "source or package")
	flag.StringVar(&cfg.RunID, "run-id", env("CLOUD_VALIDATION_RUN_ID", ""), "unique run id")
	flag.StringVar(&cfg.OutDir, "out-dir", env("CLOUD_VALIDATION_OUT_DIR", ""), "artifact output directory")
	flag.StringVar(&scenarios, "scenarios", env("CLOUD_VALIDATION_SCENARIOS", ""), "comma-separated scenario manifests")
	flag.BoolVar(&cfg.PlanOnly, "plan-only", false, "validate inputs without live calls")
	flag.StringVar(&cfg.AccountManagerURL, "account-manager-url", os.Getenv("CLOUD_VALIDATION_ACCOUNT_MANAGER_URL"), "Account Manager URL")
	flag.StringVar(&cfg.VideoCloudURL, "video-cloud-url", os.Getenv("CLOUD_VALIDATION_VIDEO_CLOUD_URL"), "Video Cloud URL")
	flag.StringVar(&cfg.DeviceURL, "device-url", os.Getenv("CLOUD_VALIDATION_DEVICE_URL"), "device-facing URL")
	flag.StringVar(&cfg.MQTTAddr, "mqtt-addr", os.Getenv("CLOUD_VALIDATION_MQTT_ADDR"), "MQTT host:port")
	flag.StringVar(&cfg.BrandCloudSlug, "brand-cloud-slug", "", "dedicated SDK E2E Brand Cloud slug")
	flag.StringVar(&cfg.CABundle, "ca-bundle", os.Getenv("CLOUD_VALIDATION_CA_BUNDLE"), "current device/app CA bundle")
	flag.StringVar(&cfg.RuntimeBundle, "runtime-bundle", os.Getenv("CLOUD_VALIDATION_RUNTIME_BUNDLE"), "mode-0600 runtime credential bundle")
	flag.StringVar(&cfg.SDKCommit, "sdk-commit", os.Getenv("CLOUD_VALIDATION_SDK_COMMIT"), "SDK source or artifact commit")
	flag.StringVar(&cfg.ServerVersion, "server-version", os.Getenv("CLOUD_VALIDATION_SERVER_VERSION"), "deployed server version")
	flag.StringVar(&cfg.ArtifactPath, "artifact", os.Getenv("CLOUD_VALIDATION_ARTIFACT"), "release artifact for package mode")
	flag.StringVar(&cfg.ArtifactChecksum, "artifact-sha256", os.Getenv("CLOUD_VALIDATION_ARTIFACT_SHA256"), "release artifact checksum")
	flag.StringVar(&cfg.ReadinessCommand, "readiness-command", os.Getenv("CLOUD_VALIDATION_READINESS_COMMAND"), "deployed Cloud readiness command")
	flag.StringVar(&cfg.SetupCommand, "setup-command", os.Getenv("CLOUD_VALIDATION_SETUP_COMMAND"), "fixture setup command")
	flag.StringVar(&cfg.VirtualCommand, "virtual-command", os.Getenv("CLOUD_VALIDATION_VIRTUAL_DEVICE_COMMAND"), "virtual device command")
	flag.StringVar(&cfg.PlatformCommand, "platform-command", os.Getenv("CLOUD_VALIDATION_PLATFORM_COMMAND"), "platform test command")
	flag.StringVar(&cfg.EvidenceCommand, "evidence-command", os.Getenv("CLOUD_VALIDATION_EVIDENCE_COMMAND"), "Cloud evidence command")
	flag.StringVar(&cfg.CleanupCommand, "cleanup-command", os.Getenv("CLOUD_VALIDATION_CLEANUP_COMMAND"), "cleanup command")
	flag.DurationVar(&cfg.ReadyTimeout, "ready-timeout", 60*time.Second, "virtual device ready timeout")
	flag.DurationVar(&cfg.RunTimeout, "run-timeout", 15*time.Minute, "overall run timeout")
	flag.Parse()

	root, err := workspaceRoot()
	if err != nil {
		fatal(err)
	}
	if cfg.RunID == "" {
		cfg.RunID = "sdk-cloud-" + cfg.Platform + "-" + time.Now().UTC().Format("20060102T150405Z")
	}
	if cfg.OutDir == "" {
		cfg.OutDir = filepath.Join(root, ".artifacts", "e2e_test", "cloud_validation", cfg.RunID)
	}
	if scenarios == "" {
		scenarios = strings.Join([]string{
			filepath.Join(root, "e2e_test", "cloud_validation", "scenarios", "core-smoke.yaml"),
			filepath.Join(root, "e2e_test", "cloud_validation", "scenarios", "shadow-roundtrip.yaml"),
		}, ",")
	}
	for _, value := range strings.Split(scenarios, ",") {
		if strings.TrimSpace(value) != "" {
			cfg.ScenarioFiles = append(cfg.ScenarioFiles, strings.TrimSpace(value))
		}
	}
	if cfg.BrandCloudSlug == "" {
		cfg.BrandCloudSlug = os.Getenv("CLOUD_VALIDATION_" + strings.ToUpper(cfg.Platform) + "_CLOUD_SLUG")
	}
	cfg.ReadyFile = filepath.Join(cfg.OutDir, "virtual-device", "ready.json")
	cfg.PlatformResult = filepath.Join(cfg.OutDir, cfg.Platform, "platform-result.json")
	cfg.CloudEvidenceFile = filepath.Join(cfg.OutDir, "cloud-evidence.json")
	cfg.ResourceManifest = filepath.Join(cfg.OutDir, "resource-manifest.json")
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), cfg.RunTimeout)
	defer cancel()
	report, err := cloudvalidation.Run(ctx, cfg)
	if err != nil {
		fatal(err)
	}
	fmt.Printf("run_id=%s status=%s report=%s\n", report.RunID, report.Status, filepath.Join(cfg.OutDir, "SUMMARY.md"))
	if report.Status == cloudvalidation.StatusFail {
		os.Exit(1)
	}
	if report.Status == cloudvalidation.StatusBlocked && !cfg.PlanOnly {
		os.Exit(2)
	}
	if report.Status == cloudvalidation.StatusSkip && !cfg.PlanOnly {
		os.Exit(3)
	}
}

func workspaceRoot() (string, error) {
	if root := os.Getenv("RTK_CLOUD_WORKSPACE"); root != "" {
		return filepath.Abs(root)
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "e2e_test", "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("workspace root not found")
		}
		dir = parent
	}
}

func env(name, fallback string) string {
	if value := os.Getenv(name); value != "" {
		return value
	}
	return fallback
}

func fatal(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
