package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strconv"

	"github.com/hkt999rtk/rtk_cloud_workspace/e2e_test/video_cloud/load/loadtest"
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
		return runLoad(args[1:])
	case "report":
		return runReport(args[1:])
	default:
		return usage()
	}
}

func usage() error {
	return fmt.Errorf("usage: rtk-video-loadtest <run|report> [flags]")
}

func runLoad(args []string) error {
	cfg := loadtest.DefaultConfigFromEnv()
	var output, reportOutput, deviceTokenMapJSON, appTokenMapJSON, deviceIDsCSV string
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	fs.StringVar(&cfg.Profile, "profile", cfg.Profile, "load profile: smoke, functional, safe-staging, stress, soak")
	fs.StringVar(&cfg.APIURL, "api-url", cfg.APIURL, "rtk_video_cloud API base URL")
	fs.StringVar(&cfg.WSURL, "ws-url", cfg.WSURL, "rtk_video_cloud WebSocket base URL; defaults from api-url when device online mode is websocket")
	fs.StringVar(&cfg.AccountToken, "account-token", cfg.AccountToken, "account bearer token")
	fs.StringVar(&cfg.AdminToken, "admin-token", cfg.AdminToken, "admin bearer token")
	fs.StringVar(&cfg.DeviceToken, "device-token", cfg.DeviceToken, "default device/camera-scoped bearer token")
	fs.StringVar(&cfg.RefreshToken, "refresh-token", cfg.RefreshToken, "optional refresh token for functional app route coverage")
	fs.StringVar(&deviceTokenMapJSON, "device-token-map-json", "", "JSON object mapping device id to device/camera-scoped bearer token")
	fs.StringVar(&appTokenMapJSON, "app-token-map-json", "", "JSON object mapping device id to app/viewer-scoped bearer token")
	fs.StringVar(&cfg.RunID, "run-id", cfg.RunID, "shared load run id")
	fs.StringVar(&cfg.InstanceID, "instance-id", cfg.InstanceID, "load runner instance id")
	fs.StringVar(&cfg.Actors, "actors", cfg.Actors, "actor set: all, device, app, viewer, or comma-separated values such as app,viewer")
	fs.StringVar(&cfg.AppRouteSet, "app-route-set", cfg.AppRouteSet, "app route set: smoke or functional")
	fs.StringVar(&cfg.DeviceRouteSet, "device-route-set", cfg.DeviceRouteSet, "device route set: smoke or functional")
	fs.StringVar(&cfg.DeviceTransportSet, "device-transport-set", cfg.DeviceTransportSet, "device transport set: smoke or snapshot")
	fs.StringVar(&cfg.ViewerRouteSet, "viewer-route-set", cfg.ViewerRouteSet, "viewer route set: smoke, functional, or negative")
	fs.StringVar(&cfg.WebRTCMediaSet, "webrtc-media-set", cfg.WebRTCMediaSet, "WebRTC media coverage set: off or rtp")
	fs.StringVar(&cfg.ClipSet, "clip-set", cfg.ClipSet, "camera recording clip coverage set: off or recording-functional")
	fs.StringVar(&cfg.MQTTSet, "mqtt-set", cfg.MQTTSet, "MQTT coverage set: off or broker")
	fs.StringVar(&cfg.MQTTAddr, "mqtt-addr", cfg.MQTTAddr, "MQTT broker host:port")
	fs.StringVar(&cfg.MQTTUsername, "mqtt-username", cfg.MQTTUsername, "MQTT username")
	fs.StringVar(&cfg.MQTTPassword, "mqtt-password", cfg.MQTTPassword, "MQTT password")
	fs.StringVar(&cfg.MQTTTopicRoot, "mqtt-topic-root", cfg.MQTTTopicRoot, "MQTT topic root")
	fs.StringVar(&cfg.MQTTDeviceProfile, "mqtt-device-profile", cfg.MQTTDeviceProfile, "MQTT device profile: camera, iot, or mixed")
	fs.StringVar(&cfg.MQTTIoTMix, "mqtt-iot-mix", cfg.MQTTIoTMix, "MQTT IoT mix, e.g. light=4,air_conditioner=3,smart_meter=3")
	fs.BoolVar(&cfg.MQTTRequired, "mqtt-required", cfg.MQTTRequired, "fail when MQTT broker configuration is missing")
	fs.StringVar(&cfg.NegativeSet, "negative-set", cfg.NegativeSet, "negative coverage set: off or http")
	fs.StringVar(&cfg.NegativeMalformedPath, "negative-malformed-path", cfg.NegativeMalformedPath, "optional endpoint path that returns malformed JSON for negative coverage")
	fs.StringVar(&cfg.NegativeTimeoutPath, "negative-timeout-path", cfg.NegativeTimeoutPath, "optional endpoint path that should exceed http-timeout for negative coverage")
	fs.StringVar(&cfg.DeviceOnlineMode, "device-online-mode", cfg.DeviceOnlineMode, "device online owner mode: none or websocket")
	fs.StringVar(&cfg.DevicePrefix, "device-prefix", cfg.DevicePrefix, "pre-provisioned device id prefix")
	fs.StringVar(&deviceIDsCSV, "device-ids", "", "comma-separated explicit device ids for focused repro; overrides generated device-prefix indexes")
	fs.StringVar(&cfg.ContractsCommit, "contracts-commit", cfg.ContractsCommit, "contracts docs commit for report evidence")
	fs.StringVar(&cfg.ServerCommit, "server-commit", cfg.ServerCommit, "deployed server commit for report evidence")
	fs.StringVar(&cfg.ClientCommit, "client-commit", cfg.ClientCommit, "client repo commit for report evidence")
	fs.StringVar(&cfg.BinarySHA256, "binary-sha256", cfg.BinarySHA256, "runner binary SHA256 for report evidence")
	fs.DurationVar(&cfg.Duration, "duration", cfg.Duration, "run timeout")
	fs.IntVar(&cfg.VirtualDevices, "virtual-devices", cfg.VirtualDevices, "virtual device actors")
	fs.IntVar(&cfg.VirtualViewers, "virtual-viewers", cfg.VirtualViewers, "virtual viewer actors")
	fs.IntVar(&cfg.AppConcurrency, "app-concurrency", cfg.AppConcurrency, "app actor concurrency")
	fs.IntVar(&cfg.DeviceConcurrency, "device-concurrency", cfg.DeviceConcurrency, "device actor concurrency")
	fs.IntVar(&cfg.ViewerConcurrency, "viewer-concurrency", cfg.ViewerConcurrency, "viewer actor concurrency")
	fs.IntVar(&cfg.Iterations, "iterations", cfg.Iterations, "iterations per actor")
	fs.Float64Var(&cfg.AppRatePerSecond, "app-rate", cfg.AppRatePerSecond, "app actor operations per second, 0 spreads iterations across duration")
	fs.Float64Var(&cfg.DeviceRatePerSecond, "device-rate", cfg.DeviceRatePerSecond, "device actor operations per second, 0 spreads iterations across duration")
	fs.Float64Var(&cfg.ViewerRatePerSecond, "viewer-rate", cfg.ViewerRatePerSecond, "viewer actor operations per second, 0 spreads iterations across duration")
	fs.DurationVar(&cfg.RampUp, "ramp-up", cfg.RampUp, "ramp-up duration")
	fs.DurationVar(&cfg.HTTPTimeout, "http-timeout", cfg.HTTPTimeout, "HTTP request timeout")
	fs.BoolVar(&cfg.AllowStress, "allow-stress", envBool("VIDEO_CLOUD_LOAD_ALLOW_STRESS"), "permit stress profile")
	fs.BoolVar(&cfg.AllowSoak, "allow-soak", envBool("VIDEO_CLOUD_LOAD_ALLOW_SOAK"), "permit soak profile")
	fs.Float64Var(&cfg.Thresholds.MinSuccessRate, "min-success-rate", cfg.Thresholds.MinSuccessRate, "minimum success rate, 0 disables")
	fs.Int64Var(&cfg.Thresholds.MaxP95Latency, "max-p95-ms", cfg.Thresholds.MaxP95Latency, "max p95 latency in ms, 0 disables")
	fs.Int64Var(&cfg.Thresholds.MaxP99Latency, "max-p99-ms", cfg.Thresholds.MaxP99Latency, "max p99 latency in ms, 0 disables")
	fs.Int64Var(&cfg.Thresholds.MaxWebRTCSetupP95Latency, "max-webrtc-setup-p95-ms", cfg.Thresholds.MaxWebRTCSetupP95Latency, "max WebRTC setup p95 latency in ms, 0 disables")
	fs.IntVar(&cfg.Thresholds.MaxOpenWebRTCSessions, "max-open-webrtc-sessions", cfg.Thresholds.MaxOpenWebRTCSessions, "max open WebRTC sessions after run")
	fs.BoolVar(&cfg.Thresholds.RequireCoverageMatrix, "require-coverage-matrix", cfg.Thresholds.RequireCoverageMatrix, "require coverage matrix artifact in result")
	fs.StringVar(&output, "output", "load-results.json", "JSON output path")
	fs.StringVar(&reportOutput, "report-output", "load-report.md", "Markdown report output path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if deviceTokenMapJSON != "" {
		if err := json.Unmarshal([]byte(deviceTokenMapJSON), &cfg.DeviceTokens); err != nil {
			return fmt.Errorf("device-token-map-json: %w", err)
		}
	}
	if appTokenMapJSON != "" {
		if err := json.Unmarshal([]byte(appTokenMapJSON), &cfg.AppTokens); err != nil {
			return fmt.Errorf("app-token-map-json: %w", err)
		}
	}
	if deviceIDsCSV != "" {
		cfg.DeviceIDs = loadtest.ParseDeviceIDs(deviceIDsCSV)
	}
	if cfg.ContractsCommit == "" {
		cfg.ContractsCommit = loadtest.ResolveContractsCommit("")
	}
	runner := loadtest.NewRunner(nil)
	result, err := runner.Run(context.Background(), cfg)
	if err != nil {
		return err
	}
	if err := loadtest.WriteJSON(output, result); err != nil {
		return err
	}
	if reportOutput != "" {
		if err := loadtest.WriteMarkdown(reportOutput, result); err != nil {
			return err
		}
	}
	if !result.Thresholds.Passed {
		return fmt.Errorf("threshold gate failed: %v", result.Thresholds.Failures)
	}
	return nil
}

func runReport(args []string) error {
	var input, output string
	thresholds := loadtest.Thresholds{}
	fs := flag.NewFlagSet("report", flag.ContinueOnError)
	fs.StringVar(&input, "input", "load-results.json", "JSON input path")
	fs.StringVar(&output, "output", "load-report.md", "Markdown output path")
	fs.Float64Var(&thresholds.MinSuccessRate, "min-success-rate", 0, "minimum success rate, 0 keeps input gate")
	fs.Int64Var(&thresholds.MaxP95Latency, "max-p95-ms", 0, "max p95 latency in ms")
	fs.Int64Var(&thresholds.MaxP99Latency, "max-p99-ms", 0, "max p99 latency in ms")
	fs.Int64Var(&thresholds.MaxWebRTCSetupP95Latency, "max-webrtc-setup-p95-ms", 0, "max WebRTC setup p95 latency in ms")
	fs.IntVar(&thresholds.MaxOpenWebRTCSessions, "max-open-webrtc-sessions", -1, "max open WebRTC sessions after run, -1 keeps input gate")
	fs.BoolVar(&thresholds.RequireCoverageMatrix, "require-coverage-matrix", false, "require coverage matrix artifact in result")
	if err := fs.Parse(args); err != nil {
		return err
	}
	result, err := loadtest.ReadJSON(input)
	if err != nil {
		return err
	}
	if thresholds.MinSuccessRate > 0 || thresholds.MaxP95Latency > 0 || thresholds.MaxP99Latency > 0 || thresholds.MaxWebRTCSetupP95Latency > 0 || thresholds.MaxOpenWebRTCSessions >= 0 || thresholds.RequireCoverageMatrix {
		result.Thresholds = loadtest.EvaluateResultThresholds(result.Summary, result.WebRTC, result.CoverageMatrix, thresholds)
	}
	if err := loadtest.WriteMarkdown(output, result); err != nil {
		return err
	}
	if !result.Thresholds.Passed {
		return fmt.Errorf("threshold gate failed: %v", result.Thresholds.Failures)
	}
	return nil
}

func envBool(key string) bool {
	value := os.Getenv(key)
	if value == "" {
		return false
	}
	parsed, err := strconv.ParseBool(value)
	return err == nil && parsed
}
