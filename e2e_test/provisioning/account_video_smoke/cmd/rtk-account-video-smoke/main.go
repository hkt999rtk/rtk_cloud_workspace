package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/hkt999rtk/rtk_cloud_workspace/e2e_test/provisioning/account_video_smoke/accountvideosmoke"
)

func main() {
	var cfg accountvideosmoke.Config
	var planOnly bool
	flag.StringVar(&cfg.RunID, "run-id", env("ACCOUNT_VIDEO_SMOKE_RUN_ID", time.Now().UTC().Format("20060102T150405Z")), "run id")
	flag.StringVar(&cfg.AccountUsersDir, "account-users-dir", os.Getenv("E2E_ACCOUNT_USERS_DIR"), "account-manager test users fixture directory")
	flag.StringVar(&cfg.DeviceCertsetDir, "device-certset-dir", os.Getenv("E2E_DEVICE_CERTSET_DIR"), "factory-enrolled device certset directory")
	flag.StringVar(&cfg.AccountManagerBaseURL, "account-manager-url", os.Getenv("ACCOUNT_MANAGER_BASE_URL"), "Account Manager base URL")
	flag.StringVar(&cfg.VideoCloudBaseURL, "video-cloud-url", os.Getenv("VIDEO_CLOUD_BASE_URL"), "Video Cloud base URL")
	flag.StringVar(&cfg.VideoCloudDeviceBaseURL, "video-cloud-device-url", os.Getenv("VIDEO_CLOUD_DEVICE_BASE_URL"), "Video Cloud device-facing mTLS base URL")
	flag.StringVar(&cfg.ArtifactDir, "artifact-dir", os.Getenv("ACCOUNT_VIDEO_SMOKE_ARTIFACT_DIR"), "artifact output directory")
	flag.StringVar(&cfg.DeviceID, "device-id", os.Getenv("E2E_DEVICE_ID"), "optional factory-enrolled device id")
	flag.StringVar(&cfg.DeviceName, "device-name", env("E2E_DEVICE_NAME", "Factory Enrolled Device"), "device name for claim resolve")
	flag.StringVar(&cfg.ClaimToken, "claim-token", os.Getenv("E2E_CLAIM_TOKEN"), "optional raw Claim Token; redacted from reports")
	flag.BoolVar(&cfg.StrictBlocked, "strict-blocked", envBool("ACCOUNT_VIDEO_SMOKE_STRICT_BLOCKED"), "exit non-zero on BLOCKED")
	flag.BoolVar(&planOnly, "plan-only", false, "only validate required inputs and write a BLOCKED/PASS plan")
	flag.DurationVar(&cfg.Timeout, "timeout", 30*time.Second, "HTTP timeout")
	flag.DurationVar(&cfg.ProvisioningPollInterval, "poll-interval", 2*time.Second, "provisioning poll interval")
	flag.IntVar(&cfg.ProvisioningPollAttempts, "poll-attempts", 6, "provisioning poll attempts")
	flag.Parse()

	if cfg.ArtifactDir == "" {
		cfg.ArtifactDir = filepath.Join(".artifacts", "e2e_test", "provisioning", "account_video_smoke", cfg.RunID)
	}

	var result accountvideosmoke.Result
	var err error
	if planOnly {
		result = accountvideosmoke.Plan(cfg)
	} else {
		result, err = accountvideosmoke.Run(context.Background(), cfg)
	}
	if writeErr := accountvideosmoke.WriteArtifacts(result, cfg.ArtifactDir); writeErr != nil && err == nil {
		err = writeErr
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "account-video smoke error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("account-video smoke %s; run_id=%s artifacts=%s\n", result.Overall, result.RunID, cfg.ArtifactDir)
	switch result.Overall {
	case accountvideosmoke.StatusFail:
		os.Exit(1)
	case accountvideosmoke.StatusBlocked:
		if cfg.StrictBlocked {
			os.Exit(1)
		}
	}
}

func env(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envBool(key string) bool {
	switch os.Getenv(key) {
	case "1", "true", "TRUE", "yes", "YES":
		return true
	default:
		return false
	}
}
