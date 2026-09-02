package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/runner"
)

type commandSpec struct {
	run func([]string) error
}

var commands = map[string]commandSpec{
	"backup":                           {run: runBackup},
	"restore":                          {run: runRestore},
	"bind-devices":                     {run: runBindDevices},
	"account-manager-email-deploy":     {run: runAccountManagerEmailDeploy},
	"activate-load-owner":              {run: runActivateLoadOwner},
	"check-certificates":               {run: runCheckCertificates},
	"certissuer-openbao-sync":          {run: runCertIssuerOpenBaoSync},
	"cloud-admin-image-deploy":         {run: runCloudAdminImageDeploy},
	"collect-evidence":                 {run: runCollectEvidence},
	"contracts-check":                  {run: runContractsCheck},
	"create-brandname-cloud":           {run: runCreateBrandnameCloud},
	"create-users":                     {run: runCreateUsers},
	"deploy":                           {run: runDeploy},
	"deployment":                       {run: runDeployment},
	"destroy-environment-resources":    {run: runDestroyEnvironmentResources},
	"dns-hook":                         {run: runDNSHook},
	"destroy-linode-staging-resources": {run: runDestroyLinodeStagingResources},
	"docs-check":                       {run: runDocsCheck},
	"generate-load-devices":            {run: runGenerateLoadDevices},
	"list-brandname-clouds":            {run: runListBrandnameClouds},
	"logs-check":                       {run: runLogsCheck},
	"lke-build-images":                 {run: runLKEBuildImages},
	"lke-capacity-run-summary":         {run: runLKECapacityRunSummary},
	"lke-resolve-images":               {run: runLKEResolveImages},
	"migrate-env":                      {run: runMigrateEnv},
	"mqtt-loadtest":                    {run: runMQTTLoadTest},
	"mqtt-test":                        {run: runMQTTTest},
	"mqtt-trace-report":                {run: runMQTTTraceReport},
	"platform-admin-token":             {run: runPlatformAdminToken},
	"pre-pr":                           {run: runPrePR},
	"provisioning-lifecycle-evidence":  {run: runProvisioningLifecycleEvidence},
	"video-cloud-admin-token":          {run: runVideoCloudAdminToken},
	"cloud-logger-token":               {run: runCloudLoggerToken},
	"provision":                        {run: runProvision},
	"provision-k8s":                    {run: runProvisionK8s},
	"refresh-user-tokens":              {run: runRefreshUserTokens},
	"refresh-runtime-client-ca":        {run: runRefreshRuntimeClientCA},
	"remove-k8s":                       {run: runRemoveK8s},
	"run-staging-e2e":                  {run: runStagingE2E},
	"secrets":                          {run: runSecrets},
	"secrets-check":                    {run: runSecretsCheck},
	"environment-acceptance":           {run: runEnvironmentAcceptance},
	"staging-acceptance":               {run: runStagingAcceptance},
	"staging-e2e-billing-verify":       {run: runStagingE2EBillingVerify},
	"environment-e2e-billing-verify":   {run: runStagingE2EBillingVerify},
	"staging-e2e-data-setup":           {run: runStagingE2EDataSetup},
	"environment-e2e-data-setup":       {run: runStagingE2EDataSetup},
	"staging-e2e-mqtt-log-verify":      {run: runStagingE2EMQTTLogVerify},
	"environment-e2e-mqtt-log-verify":  {run: runStagingE2EMQTTLogVerify},
	"staging-e2e-test":                 {run: runStagingE2ETest},
	"staging-provision":                {run: runStagingProvision},
	"staging-reset-k8s":                {run: runStagingResetK8s},
	"status-all":                       {run: runStatusAll},
	"sync-env":                         {run: runSyncEnv},
	"sync-all":                         {run: runSyncAll},
	"test-data":                        {run: runTestData},
	"test-e2e":                         {run: runTestE2E},
	"test-feature":                     {run: runTestFeature},
	"test-feature-coverage":            {run: runTestFeatureCoverage},
	"test-factory-live":                {run: runTestFactoryLive},
	"test-live":                        {run: runTestLive},
	"test-matrix":                      {run: runTestMatrix},
	"test-multicloud":                  {run: runTestMulticloud},
	"test-payment":                     {run: runTestPayment},
	"test-platform-live":               {run: runPlatformLiveEvidence},
	"test-services":                    {run: runTestServices},
	"test-catalog":                     {run: runTestCatalog},
	"test-coverage":                    {run: runTestCoverage},
	"test-coverage-aggregate":          {run: runTestCoverageAggregate},
	"test-inventory":                   {run: runTestInventory},
	"test-spec-inventory":              {run: runTestSpecInventory},
	"test-spec-impact":                 {run: runTestSpecImpact},
	"test-ui":                          {run: runTestUI},
	"unprovision-devices":              {run: runUnprovisionDevices},
	"validate-device-bind":             {run: runValidateDeviceBind},
	"video-loadtest-tokens":            {run: runVideoLoadtestTokens},
	"video-relay-test":                 {run: runVideoRelayTest},
}

var ciRunnerCommands = map[string]commandSpec{
	"archive-artifacts": {run: runCIRunnersArchiveArtifacts},
	"list":              {run: runCIRunnersList},
	"power":             {run: runCIRunnersPower},
	"provision":         {run: runCIRunnersProvision},
	"run-session":       {run: runCIRunnersRunSession},
	"wait-online":       {run: runCIRunnersWaitOnline},
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		var code exitCode
		if errors.As(err, &code) {
			os.Exit(int(code))
		}
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

type exitCode int

func (e exitCode) Error() string {
	return fmt.Sprintf("exit status %d", int(e))
}

func run(args []string) error {
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printUsage()
		return nil
	}
	args = normalizeLegacyPathArgs(args)
	if err := recoveryMutationGuard(args); err != nil {
		return err
	}
	var err error
	args, err = normalizeEnvironmentArgs(args)
	if err != nil {
		return err
	}
	cmdName := args[0]
	if cmdName == "ci-runners" {
		if len(args) < 2 || args[1] == "-h" || args[1] == "--help" {
			printCIRunnerUsage()
			return nil
		}
		spec, ok := ciRunnerCommands[args[1]]
		if !ok {
			return fmt.Errorf("unknown ci-runners command: %s", args[1])
		}
		if spec.run != nil {
			return spec.run(args[2:])
		}
		return errors.New("internal error: command has no native implementation")
	}
	spec, ok := commands[cmdName]
	if !ok {
		return fmt.Errorf("unknown command: %s", cmdName)
	}
	if spec.run != nil {
		return spec.run(args[1:])
	}
	return errors.New("internal error: command has no native implementation")
}

func normalizeEnvironmentArgs(args []string) ([]string, error) {
	if len(args) == 0 || args[0] == "deployment" || args[0] == "secrets" || args[0] == "backup" || args[0] == "restore" || args[0] == "test-feature-coverage" {
		return args, nil
	}
	var environment, workspace string
	hasEnvRoot := false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--environment", "--workspace", "--env-root":
			if i+1 >= len(args) {
				return nil, fmt.Errorf("%s requires a value", args[i])
			}
			if args[i] == "--environment" {
				environment = args[i+1]
			} else if args[i] == "--workspace" {
				workspace = args[i+1]
			} else {
				hasEnvRoot = true
			}
			i++
		}
	}
	if environment == "" {
		return args, nil
	}
	if hasEnvRoot {
		if args[0] == "test-feature" {
			return args, nil
		}
		return nil, errors.New("--environment and --env-root cannot be used together")
	}
	if filepath.Base(environment) != environment || environment == "." || environment == ".." {
		return nil, fmt.Errorf("invalid environment name %q", environment)
	}
	if workspace == "" {
		workspace = "."
	}
	envRoot := filepath.Join(workspace, "cloud_env", environment, "runtime")
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--environment" {
			out = append(out, "--env-root", envRoot)
			i++
			continue
		}
		out = append(out, args[i])
	}
	return out, nil
}

func normalizeLegacyPathArgs(args []string) []string {
	pathFlags := map[string]bool{
		"--env-root":      true,
		"--secrets-root":  true,
		"--operator-env":  true,
		"--workspace":     true,
		"--out-dir":       true,
		"--users-file":    true,
		"--devices-dir":   true,
		"--bind-artifact": true,
		"--ssh-key":       true,
		"--public-key":    true,
		"--state-dir":     true,
		"--repo-root":     true,
		"--artifacts-dir": true,
		"--output-dir":    true,
		"--config":        true,
		"--config-root":   true,
	}
	out := append([]string(nil), args...)
	cwd, err := os.Getwd()
	if err != nil {
		return out
	}
	for i := 0; i < len(out); i++ {
		arg := out[i]
		if pathFlags[arg] && i+1 < len(out) {
			out[i+1] = absIfRelative(cwd, out[i+1])
			i++
			continue
		}
		if name, value, ok := strings.Cut(arg, "="); ok && pathFlags[name] {
			out[i] = name + "=" + absIfRelative(cwd, value)
		}
	}
	return out
}

func absIfRelative(cwd, value string) string {
	if value == "" || strings.HasPrefix(value, "-") || filepath.IsAbs(value) {
		return value
	}
	return filepath.Clean(filepath.Join(cwd, value))
}

func runMQTTTest(args []string) error {
	fs := flag.NewFlagSet("mqtt-test", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	envRoot := fs.String("env-root", "", "environment root")
	workspaceFlag := fs.String("workspace", "", "workspace")
	brandname := fs.String("brandname", "", "brand name")
	outDir := fs.String("out-dir", "", "output directory")
	testDataDB := fs.String("test-data-db", "", "explicit SQLite test-data database containing load-test credentials")
	profile := fs.String("profile", "smoke", "profile")
	runID := fs.String("run-id", os.Getenv("HOME100K_RUN_ID"), "run id for log correlation")
	duration := fs.Int("duration-seconds", 120, "duration seconds")
	maxUsers := fs.String("max-users", "", "max users")
	seed := fs.Int("seed", 20260531, "seed")
	traceDetail := fs.String("trace-detail", "summary", "console trace detail: none, summary, full")
	shardIndex := fs.Int("shard-index", 0, "load-test shard index")
	shardCount := fs.Int("shard-count", 1, "load-test shard count")
	rampUp := fs.String("ramp-up", "", "load-test ramp-up duration")
	telemetryInterval := fs.String("telemetry-interval", "", "load-test telemetry interval")
	stateInterval := fs.String("state-interval", "", "load-test state interval")
	commandRate := fs.String("command-rate-per-device-per-day", "", "load-test command rate per device per day")
	commandConcurrency := fs.Int("command-concurrency", 0, "load-test sustained shadow command concurrency")
	shadowCommandTimeout := fs.String("shadow-command-timeout", "", "per-phase sustained shadow command timeout")
	deviceTokenRequestTimeout := fs.String("device-token-request-timeout", "", "per-attempt device /request_token timeout")
	deviceTokenRequestRetries := fs.Int("device-token-request-retries", 0, "device /request_token retries after the first attempt")
	runtimeLogs := fs.Bool("runtime-logs", true, "publish MQTT runtime logs during sustained shadow commands")
	loadModel := fs.String("load-model", "", "load model passed through to cloud-mqtt-test")
	stageNames := fs.String("stage-names", "", "comma-separated staged sustained load stage names")
	stageConnectedDevices := fs.String("stage-connected-devices", "", "comma-separated staged sustained per-shard connected device targets")
	stageDurationsSeconds := fs.String("stage-durations-seconds", "", "comma-separated staged sustained stage durations in seconds")
	stageMinCommands := fs.String("stage-min-commands", "", "comma-separated staged sustained minimum command events")
	deviceTrafficProfile := fs.String("device-traffic-profile", "", "device traffic profile passed through to cloud-mqtt-test")
	stageUsageWindows := fs.String("stage-usage-windows", "", "comma-separated staged sustained usage windows")
	concurrency := fs.Int("concurrency", 25, "load-test MQTT probe concurrency")
	maxConnectedDevices := fs.Int("max-connected-devices", 0, "load-test max connected devices in this shard")
	otaCampaignID := fs.String("ota-campaign-id", "", "required OTA campaign id")
	otaTargetVersion := fs.String("ota-target-version", "", "required OTA target version")
	otaCurrentVersion := fs.String("ota-current-version", "", "required initial device firmware version")
	otaHardwareRevision := fs.String("ota-hardware-revision", "", "required device hardware revision")
	otaAntiRollbackCounter := fs.Int("ota-anti-rollback-counter", 0, "device anti-rollback counter")
	otaPollInterval := fs.String("ota-poll-interval", "5s", "OTA assignment poll interval")
	otaUpgradeTimeout := fs.String("ota-upgrade-timeout", "30m", "per-device OTA deadline")
	otaHTTPConcurrency := fs.Int("ota-http-concurrency", 250, "maximum concurrent OTA HTTP requests")
	otaDownloadConcurrency := fs.Int("ota-download-concurrency", 64, "maximum concurrent artifact streams")
	otaInstallDelay := fs.String("ota-install-delay", "2s", "simulated installation delay")
	otaRebootDelay := fs.String("ota-reboot-delay", "2s", "simulated reboot delay")
	otaVerifyDelay := fs.String("ota-verify-delay", "1s", "simulated post-reboot verification delay")
	otaStageJitterPercent := fs.Float64("ota-stage-jitter-percent", 20, "deterministic OTA timing jitter percentage")
	otaDownloadFailurePercent := fs.Float64("ota-download-failure-percent", 0, "deterministic download failure percentage")
	otaVerifyFailurePercent := fs.Float64("ota-verify-failure-percent", 0, "deterministic verification failure percentage")
	otaInstallFailurePercent := fs.Float64("ota-install-failure-percent", 0, "deterministic installation failure percentage")
	otaRebootFailurePercent := fs.Float64("ota-reboot-failure-percent", 0, "deterministic reboot failure percentage")
	otaTimeoutPercent := fs.Float64("ota-timeout-percent", 0, "deterministic OTA timeout percentage")
	mqttProbe := true
	fs.BoolFunc("mqtt-probe", "run mqtt probe", func(string) error { mqttProbe = true; return nil })
	fs.BoolFunc("no-mqtt-probe", "skip mqtt probe", func(string) error { mqttProbe = false; return nil })
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRoot == "" {
		return errors.New("--env-root is required")
	}
	if *brandname == "" {
		return errors.New("--brandname is required")
	}
	if *profile != "smoke" && *profile != "real-case" && *profile != "baseline-10k" {
		return errors.New("--profile must be smoke, real-case, or baseline-10k")
	}
	if *shardCount <= 0 || *shardIndex < 0 || *shardIndex >= *shardCount {
		return errors.New("--shard-count must be positive and --shard-index must be within range")
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	resolvedEnv, err := resolveEnvRoot(workspace, *envRoot)
	if err != nil {
		return err
	}
	if *maxUsers == "" && *profile == "smoke" {
		*maxUsers = "1"
	}
	if *outDir == "" {
		*outDir = filepath.Join(resolvedEnv, "artifacts", "home-mqtt-loadtest", time.Now().UTC().Format("20060102T150405Z"))
	}
	childEnv := map[string]string{"GOWORK": "off"}
	stackEnv, _ := readEnvFile(filepath.Join(resolvedEnv, "env", "stack.env"))
	if firstNonEmpty(os.Getenv("CLOUD_PROVIDER"), stackEnv["CLOUD_PROVIDER"]) == "lke" {
		accountBaseURL := firstNonEmpty(os.Getenv("ACCOUNT_MANAGER_BASE_URL"), stackEnv["ACCOUNT_MANAGER_BASE_URL"])
		videoBaseURL := firstNonEmpty(os.Getenv("VIDEO_CLOUD_BASE_URL"), os.Getenv("VIDEO_CLOUD_PUBLIC_BASE_URL"), stackEnv["VIDEO_CLOUD_BASE_URL"], stackEnv["VIDEO_CLOUD_PUBLIC_BASE_URL"])
		videoTokenBaseURL := firstNonEmpty(os.Getenv("VIDEO_CLOUD_TOKEN_BASE_URL"), stackEnv["VIDEO_CLOUD_TOKEN_BASE_URL"])
		mqttAddr := firstNonEmpty(os.Getenv("VIDEO_CLOUD_MQTT_ADDR"), stackEnv["VIDEO_CLOUD_MQTT_ADDR"])
		if accountBaseURL != "" {
			childEnv["ACCOUNT_MANAGER_BASE_URL"] = accountBaseURL
		}
		if videoBaseURL != "" {
			childEnv["VIDEO_CLOUD_BASE_URL"] = videoBaseURL
			childEnv["VIDEO_CLOUD_PUBLIC_BASE_URL"] = videoBaseURL
		}
		if videoTokenBaseURL != "" {
			childEnv["VIDEO_CLOUD_TOKEN_BASE_URL"] = videoTokenBaseURL
		}
		if mqttAddr != "" {
			childEnv["VIDEO_CLOUD_MQTT_ADDR"] = mqttAddr
		}
		if accountBaseURL == "" || videoBaseURL == "" || mqttAddr == "" {
			if strings.TrimSpace(os.Getenv("CLOUD_STAGING_E2E_K8S_PORT_FORWARD")) == "0" {
				return errors.New("external endpoints are required when CLOUD_STAGING_E2E_K8S_PORT_FORWARD=0: set ACCOUNT_MANAGER_BASE_URL, VIDEO_CLOUD_BASE_URL or VIDEO_CLOUD_PUBLIC_BASE_URL, and VIDEO_CLOUD_MQTT_ADDR")
			}
			stack := firstNonEmpty(stackEnv["CLOUD_STACK_NAME"], "video-cloud-staging")
			env := map[string]string{"CLOUD_STACK_NAME": stack}
			mqttPort, mqttCleanup, err := lkeTCPServicePortForward(resolvedEnv, env, "video-cloud", "mqtt", 8883, "mqtt")
			if err != nil {
				return err
			}
			defer mqttCleanup()
			videoURL, videoCleanup, err := lkeVideoCloudAPIPortForward(resolvedEnv, env)
			if err != nil {
				return err
			}
			defer videoCleanup()
			accountURL, accountCleanup, err := lkeAccountManagerPortForward(resolvedEnv, env)
			if err != nil {
				return err
			}
			defer accountCleanup()
			childEnv["RTK_CLOUD_MQTT_TEST_MQTT_HOST"] = "127.0.0.1"
			childEnv["RTK_CLOUD_MQTT_TEST_MQTT_PORT"] = strconv.Itoa(mqttPort)
			childEnv["VIDEO_CLOUD_BASE_URL"] = videoURL
			childEnv["VIDEO_CLOUD_TOKEN_BASE_URL"] = videoURL
			childEnv["ACCOUNT_MANAGER_BASE_URL"] = accountURL
		}
	}
	childArgs := []string{
		"--root", workspace,
		"--env-root", resolvedEnv,
		"--brandname", *brandname,
		"--out-dir", *outDir,
		"--test-data-db", *testDataDB,
		"--profile", *profile,
		"--run-id", *runID,
		"--duration-seconds", strconv.Itoa(*duration),
		"--max-users", *maxUsers,
		"--seed", strconv.Itoa(*seed),
		"--mqtt-probe", strconv.FormatBool(mqttProbe),
		"--trace-detail", *traceDetail,
		"--shard-index", strconv.Itoa(*shardIndex),
		"--shard-count", strconv.Itoa(*shardCount),
		"--ramp-up", *rampUp,
		"--telemetry-interval", *telemetryInterval,
		"--state-interval", *stateInterval,
		"--command-rate-per-device-per-day", *commandRate,
		"--command-concurrency", strconv.Itoa(*commandConcurrency),
		"--shadow-command-timeout", *shadowCommandTimeout,
		"--device-token-request-timeout", *deviceTokenRequestTimeout,
		"--device-token-request-retries", strconv.Itoa(*deviceTokenRequestRetries),
		"--runtime-logs=" + strconv.FormatBool(*runtimeLogs),
		"--load-model", *loadModel,
		"--stage-names", *stageNames,
		"--stage-connected-devices", *stageConnectedDevices,
		"--stage-durations-seconds", *stageDurationsSeconds,
		"--stage-min-commands", *stageMinCommands,
		"--device-traffic-profile", *deviceTrafficProfile,
		"--concurrency", strconv.Itoa(*concurrency),
		"--max-connected-devices", strconv.Itoa(*maxConnectedDevices),
	}
	if strings.TrimSpace(*loadModel) == "ota-device-simulator" {
		childArgs = append(childArgs,
			"--ota-campaign-id", *otaCampaignID,
			"--ota-target-version", *otaTargetVersion,
			"--ota-current-version", *otaCurrentVersion,
			"--ota-hardware-revision", *otaHardwareRevision,
			"--ota-anti-rollback-counter", strconv.Itoa(*otaAntiRollbackCounter),
			"--ota-poll-interval", *otaPollInterval,
			"--ota-upgrade-timeout", *otaUpgradeTimeout,
			"--ota-http-concurrency", strconv.Itoa(*otaHTTPConcurrency),
			"--ota-download-concurrency", strconv.Itoa(*otaDownloadConcurrency),
			"--ota-install-delay", *otaInstallDelay,
			"--ota-reboot-delay", *otaRebootDelay,
			"--ota-verify-delay", *otaVerifyDelay,
			"--ota-stage-jitter-percent", strconv.FormatFloat(*otaStageJitterPercent, 'f', -1, 64),
			"--ota-download-failure-percent", strconv.FormatFloat(*otaDownloadFailurePercent, 'f', -1, 64),
			"--ota-verify-failure-percent", strconv.FormatFloat(*otaVerifyFailurePercent, 'f', -1, 64),
			"--ota-install-failure-percent", strconv.FormatFloat(*otaInstallFailurePercent, 'f', -1, 64),
			"--ota-reboot-failure-percent", strconv.FormatFloat(*otaRebootFailurePercent, 'f', -1, 64),
			"--ota-timeout-percent", strconv.FormatFloat(*otaTimeoutPercent, 'f', -1, 64),
		)
	}
	if strings.TrimSpace(*stageUsageWindows) != "" {
		childArgs = append(childArgs, "--stage-usage-windows", *stageUsageWindows)
	}
	childScript := strings.TrimSpace(os.Getenv("CLOUD_STAGING_E2E_MQTT_TEST_SCRIPT"))
	var cmd *exec.Cmd
	if childScript != "" {
		cmd = exec.Command(childScript, childArgs...)
	} else {
		goCmd, err := exec.LookPath("go")
		if err != nil {
			return errors.New("go is required")
		}
		cmd = exec.Command(goCmd, append([]string{"run", "./cloud-mqtt-test"}, childArgs...)...)
		cmd.Dir = filepath.Join(workspace, "scripts", "go")
	}
	cmd.Env = withEnv(os.Environ(), childEnv)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

type certCheckResult struct {
	Target    string `json:"target"`
	Domain    string `json:"domain"`
	Source    string `json:"source"`
	Status    string `json:"status"`
	DaysLeft  any    `json:"days_left"`
	ExpiresAt string `json:"expires_at"`
	Issuer    string `json:"issuer"`
	Detail    string `json:"detail"`
}

func runCheckCertificates(args []string) error {
	fs := flag.NewFlagSet("check-certificates", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	dnsRoot := fs.String("dns-root-domain", "realtekconnect.com", "dns root domain")
	minValidDays := fs.Int("min-valid-days", 7, "minimum valid days")
	jsonOut := fs.Bool("json", false, "json output")
	skipLive := fs.Bool("skip-live", false, "skip live")
	_ = skipLive
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required")
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	accountEnv := firstExistingPath(filepath.Join(envRoot, "services", "account-manager", "account-manager.env"), filepath.Join(envRoot, "services", "account-manager", "account-manager-public-staging.env"))
	adminEnv := firstExistingPath(filepath.Join(envRoot, "services", "cloud-admin", "admin.env"), filepath.Join(envRoot, "services", "cloud-admin", "admin-staging.env"))
	stackEnv, _ := readEnvFile(filepath.Join(envRoot, "env", "stack.env"))
	loggerEnv, _ := readEnvFile(filepath.Join(envRoot, "services", "cloud-logger", "logger.env"))
	loggerState, _ := readEnvFile(filepath.Join(envRoot, "state", "cloud-logger.env"))
	videoDomain := firstNonEmpty(stackEnv["VIDEO_CLOUD_DOMAIN"], "video-cloud-staging."+*dnsRoot)
	certIssuerDomain := firstNonEmpty(stackEnv["VIDEO_CLOUD_CERTISSUER_DOMAIN"], "certissuer."+videoDomain)
	accountDomain := firstNonEmpty(stackEnv["ACCOUNT_MANAGER_DOMAIN"], envFileValue(accountEnv, "ACCOUNT_MANAGER_DOMAIN"))
	adminDomain := firstNonEmpty(stackEnv["CLOUD_ADMIN_DOMAIN"], envFileValue(adminEnv, "CLOUD_ADMIN_DOMAIN"))
	loggerDomain := firstNonEmpty(stackEnv["CLOUD_LOGGER_DOMAIN"], loggerEnv["CLOUD_LOGGER_DOMAIN"], loggerState["CLOUD_LOGGER_DOMAIN"], "logger."+videoDomain)
	targets := []struct {
		name   string
		domain string
		dir    string
	}{
		{"video-cloud", videoDomain, filepath.Join(envRoot, "certificates", videoDomain)},
		{"video-cloud-certissuer", certIssuerDomain, filepath.Join(envRoot, "certificates", videoDomain)},
		{"account-manager", accountDomain, filepath.Join(envRoot, "certificates", accountDomain)},
		{"cloud-admin", adminDomain, filepath.Join(envRoot, "certificates", adminDomain)},
		{"cloud-logger", loggerDomain, filepath.Join(envRoot, "certificates", loggerDomain)},
	}
	results := []certCheckResult{}
	overall := "pass"
	for _, target := range targets {
		result := checkCertTarget(target.name, target.domain, filepath.Join(target.dir, "fullchain.pem"), *minValidDays)
		if result.Status != "pass" {
			overall = "fail"
		}
		results = append(results, result)
	}
	payload := map[string]any{"status": overall, "results": results}
	if *jsonOut {
		if err := json.NewEncoder(os.Stdout).Encode(payload); err != nil {
			return err
		}
		if overall != "pass" {
			return exitCode(1)
		}
		return nil
	}
	fmt.Fprintf(os.Stdout, "cloud_certificates status=%s min_valid_days=%d env_root=%s\n", overall, *minValidDays, envRoot)
	for _, result := range results {
		fmt.Fprintf(os.Stdout, "%s\t%s\t%s\t%v\t%s\n", result.Target, result.Domain, result.Status, result.DaysLeft, result.Detail)
	}
	if overall != "pass" {
		return exitCode(1)
	}
	return nil
}

func runMigrateEnv(args []string) error {
	fs := flag.NewFlagSet("migrate-env", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	envRootFlag := fs.String("env-root", "", "environment root")
	fs.String("workspace", "", "workspace")
	fs.Bool("force", false, "retired")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required")
	}
	return errors.New("migrate-env is retired with the staging VM toolkit; use sync-env plus the K8s staging service discovery flow")
}

func runSyncEnv(args []string) error {
	fs := flag.NewFlagSet("sync-env", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	check := fs.Bool("check", false, "check only")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required")
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	resolvedEnvRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	changed, err := syncEnvRoot(resolvedEnvRoot, *check)
	if err != nil {
		return err
	}
	if *check && changed {
		return errors.New("environment metadata is not synchronized; run sync-env --env-root " + resolvedEnvRoot)
	}
	if changed {
		fmt.Fprintf(os.Stdout, "synced=%s\n", resolvedEnvRoot)
	} else {
		fmt.Fprintf(os.Stdout, "synced=%s unchanged\n", resolvedEnvRoot)
	}
	return nil
}

func syncEnvRoot(root string, check bool) (bool, error) {
	stackPath := filepath.Join(root, "env", "stack.env")
	raw, err := readEnvFile(stackPath)
	if err != nil {
		return false, err
	}
	if raw["CLOUD_ENV_NAME"] == "" {
		raw["CLOUD_ENV_NAME"] = filepath.Base(filepath.Dir(root))
	}
	if raw["CLOUD_PROVIDER"] == "" {
		raw["CLOUD_PROVIDER"] = "lke"
	}
	if raw["CLOUD_REGION"] == "" {
		raw["CLOUD_REGION"] = "us-sea"
	}
	if raw["CLOUD_DNS_ROOT_DOMAIN"] == "" {
		raw["CLOUD_DNS_ROOT_DOMAIN"] = "realtekconnect.com"
	}
	derived := envroot.Derive(raw)
	changed := false
	if c, err := syncTextFile(stackPath, renderStackEnv(raw, derived), check); err != nil {
		return changed, err
	} else if c {
		changed = true
	}
	if raw["CLOUD_PROVIDER"] == "linode" {
		topology := firstExistingPath(filepath.Join(root, "topology", "video-cloud.yaml"), filepath.Join(root, "topology", "video-cloud-staging.yaml"))
		if c, err := syncTopology(topology, raw, derived, check); err != nil {
			return changed, err
		} else if c {
			changed = true
		}
	}
	envUpdates := []struct {
		path   string
		keys   map[string]string
		create bool
	}{
		{path: firstExistingPath(filepath.Join(root, "services", "video-cloud", "video-cloud.env"), filepath.Join(root, "services", "video-cloud", "video-cloud-staging.env")), keys: map[string]string{}},
		{path: firstExistingPath(filepath.Join(root, "services", "account-manager", "account-manager.env"), filepath.Join(root, "services", "account-manager", "account-manager-public-staging.env")), keys: map[string]string{
			"ACCOUNT_MANAGER_DOMAIN": derived["ACCOUNT_MANAGER_DOMAIN"],
		}},
		{path: firstExistingPath(filepath.Join(root, "services", "cloud-admin", "admin.env"), filepath.Join(root, "services", "cloud-admin", "admin-staging.env")), keys: map[string]string{
			"CLOUD_ADMIN_DOMAIN": derived["CLOUD_ADMIN_DOMAIN"],
		}},
		{path: filepath.Join(root, "services", "cloud-logger", "logger.env"), create: raw["CLOUD_PROVIDER"] == "lke", keys: map[string]string{
			"CLOUD_LOGGER_DOMAIN":   derived["CLOUD_LOGGER_DOMAIN"],
			"CLOUD_LOGGER_ENDPOINT": "https://" + derived["CLOUD_LOGGER_DOMAIN"],
		}},
	}
	for _, item := range envUpdates {
		if _, statErr := os.Stat(item.path); os.IsNotExist(statErr) && !item.create {
			continue
		}
		c, err := syncEnvFile(item.path, item.keys, raw, derived, check)
		if err != nil {
			return changed, err
		}
		if c {
			changed = true
		}
	}
	return changed, nil
}

func renderStackEnv(raw, derived map[string]string) string {
	rootKeys := []string{"CLOUD_ENV_NAME", "CLOUD_PROVIDER", "CLOUD_REGION", "CLOUD_DNS_ROOT_DOMAIN"}
	generatedKeys := envroot.GeneratedKeys()
	known := map[string]bool{}
	var b strings.Builder
	for _, key := range rootKeys {
		known[key] = true
		fmt.Fprintf(&b, "%s=%s\n", key, firstNonEmpty(raw[key], derived[key]))
	}
	b.WriteString("\n# Generated by rtk-cloud sync-env. Do not edit manually.\n")
	for _, key := range generatedKeys {
		known[key] = true
		if derived[key] != "" {
			fmt.Fprintf(&b, "%s=%s\n", key, derived[key])
		}
	}
	extraKeys := make([]string, 0, len(raw))
	for key := range raw {
		if !known[key] && !isRetiredStagingRuntimeEnvKey(key) {
			extraKeys = append(extraKeys, key)
		}
	}
	sort.Strings(extraKeys)
	if len(extraKeys) > 0 {
		b.WriteString("\n# Local overrides and operator metadata.\n")
		for _, key := range extraKeys {
			fmt.Fprintf(&b, "%s=%s\n", key, raw[key])
		}
	}
	return b.String()
}

func syncTopology(path string, raw, derived map[string]string, check bool) (bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return false, err
	}
	text := string(data)
	for _, key := range envroot.GeneratedKeys() {
		if old := raw[key]; old != "" && derived[key] != "" {
			text = strings.ReplaceAll(text, old, derived[key])
		}
	}
	text = replaceDerivedText(text, raw, derived)
	if old := raw["CLOUD_REGION"]; old != "" && derived["CLOUD_REGION"] != "" {
		text = replaceYAMLTopScalar(text, "region", derived["CLOUD_REGION"])
	}
	if derived["CLOUD_STACK_NAME"] != "" {
		text = replaceYAMLTopScalar(text, "stack", derived["CLOUD_STACK_NAME"])
	}
	return syncTextFile(path, text, check)
}

func replaceYAMLTopScalar(text, key, value string) string {
	lines := strings.Split(text, "\n")
	prefix := key + ":"
	for i, line := range lines {
		if strings.HasPrefix(line, prefix) {
			lines[i] = key + ": " + value
			return strings.Join(lines, "\n")
		}
	}
	return key + ": " + value + "\n" + text
}

func syncEnvFile(path string, updates map[string]string, raw, derived map[string]string, check bool) (bool, error) {
	values, err := readEnvFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if values == nil {
		values = map[string]string{}
	}
	for key, value := range values {
		if isRetiredStagingRuntimeEnvKey(key) {
			delete(values, key)
			continue
		}
		values[key] = replaceDerivedText(value, raw, derived)
	}
	for key, value := range updates {
		values[key] = value
	}
	return syncTextFile(path, renderEnvMap(values), check)
}

func replaceDerivedText(text string, raw, derived map[string]string) string {
	for _, key := range envroot.GeneratedKeys() {
		old := raw[key]
		newValue := derived[key]
		if old != "" && newValue != "" {
			text = strings.ReplaceAll(text, old, newValue)
		}
	}
	replacements := []struct {
		pattern string
		value   string
	}{
		{`video-cloud-stg-[0-9]{4}[a-z0-9]*`, derived["CLOUD_STACK_NAME"]},
		{`video-cloud-staging`, derived["CLOUD_STACK_NAME"]},
	}
	for _, item := range replacements {
		if item.value == "" {
			continue
		}
		text = regexp.MustCompile(item.pattern).ReplaceAllString(text, item.value)
	}
	return text
}

func isRetiredStagingRuntimeEnvKey(key string) bool {
	retiredExact := map[string]bool{
		"VIDEO_CLOUD_" + "LABEL_PREFIX": true,
		"VIDEO_CLOUD_" + "VPC_LABEL":    true,
		"VIDEO_CLOUD_" + "SUBNET_LABEL": true,
	}
	if retiredExact[key] {
		return true
	}
	for _, prefix := range []string{"ACCOUNT_MANAGER_" + "LINODE_", "ADMIN_" + "LINODE_", "CLOUD_LOGGER_" + "LINODE_"} {
		if strings.HasPrefix(key, prefix) {
			return true
		}
	}
	return false
}

func renderEnvMap(values map[string]string) string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, key := range keys {
		fmt.Fprintf(&b, "%s=%s\n", key, values[key])
	}
	return b.String()
}

func syncTextFile(path, want string, check bool) (bool, error) {
	current, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return false, err
	}
	if string(current) == want {
		return false, nil
	}
	if check {
		return true, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return false, err
	}
	return true, os.WriteFile(path, []byte(want), 0o644)
}

type linodeList[T any] struct {
	Data []T `json:"data"`
}

func linodeGetList[T any](token, path string) ([]T, error) {
	out, err := exec.Command("curl", "-fsS", "-X", "GET", "https://api.linode.com/v4"+path, "-H", "Authorization: Bearer "+token, "-H", "Content-Type: application/json").Output()
	if err != nil {
		return nil, err
	}
	var parsed linodeList[T]
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, err
	}
	return parsed.Data, nil
}

func resolveLinodeToken(envRoot string) string {
	if rtkCloudTestMode() {
		if token := strings.TrimSpace(os.Getenv("LINODE_TOKEN")); token != "" {
			return token
		}
	}
	environment := firstNonEmpty(envFileValue(filepath.Join(envRoot, "env", "stack.env"), "CLOUD_ENV_NAME"), "staging")
	values, check := deploymentCredentialValues(defaultDeploymentEnvironmentCredentialFile(environment))
	if check.Passed {
		return values["LINODE_TOKEN"]
	}
	return ""
}

type ioDiscard struct{}

func (ioDiscard) Write(p []byte) (int, error) { return len(p), nil }

func runStatusAll(args []string) error {
	fs := flag.NewFlagSet("status-all", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "workspace:")
	if err := runCmd(workspace, "git", "status", "--short", "--branch"); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout)
	submodules, err := submodulePaths(workspace)
	if err != nil {
		return err
	}
	for _, path := range submodules {
		abs := filepath.Join(workspace, path)
		if !exists(abs) {
			continue
		}
		fmt.Fprintln(os.Stdout)
		fmt.Fprintf(os.Stdout, "[%s] %s\n", filepath.Base(path), path)
		if err := runCmd(abs, "git", "status", "--short", "--branch"); err != nil {
			return err
		}
		if err := runCmd(abs, "git", "log", "-1", "--oneline", "--decorate"); err != nil {
			return err
		}
	}
	return nil
}

func runSyncAll(args []string) error {
	fs := flag.NewFlagSet("sync-all", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	steps := [][]string{
		{"git", "fetch", "origin"},
		{"git", "submodule", "sync", "--recursive"},
		{"git", "submodule", "update", "--init", "--recursive"},
	}
	for _, step := range steps {
		if err := runCmd(workspace, step[0], step[1:]...); err != nil {
			return err
		}
	}
	submodules, err := submodulePaths(workspace)
	if err != nil {
		return err
	}
	for _, path := range submodules {
		abs := filepath.Join(workspace, path)
		if exists(abs) {
			if err := runCmd(abs, "git", "fetch", "--all", "--prune"); err != nil {
				return err
			}
		}
	}
	fmt.Fprintln(os.Stdout, "Fetched workspace and submodule remotes. Pinned commits were not changed.")
	return nil
}

func runDocsCheck(args []string) error {
	fs := flag.NewFlagSet("docs-check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	check := newCheck()
	fmt.Fprintln(os.Stdout, "== workspace documentation entries ==")
	for _, path := range []string{
		"README.md",
		"docs/README.md",
		"docs/architecture.md",
		"docs/contracts-submodule-governance.md",
		"docs/documentation-governance.md",
		"docs/deployment-operations.md",
		"docs/backup-restore.md",
		"docs/examples/backup-config.example.json",
		"docs/deployment-secrets-governance.md",
		"docs/examples/operator.env.example",
		"docs/linode-ci-runners.md",
		"docs/examples/secrets-manifest.example.json",
		"docs/testing.md",
		"docs/testing-operations.md",
		"docs/load_test_report.md",
		"e2e_test/README.md",
		"e2e_test/go.mod",
		"e2e_test/fixtures/README.md",
		"e2e_test/factory_enroll/README.md",
		"e2e_test/factory_enroll/cmd/rtk-factory-enroll-test/main.go",
		"e2e_test/provisioning/account_video_smoke/README.md",
		"e2e_test/provisioning/account_video_smoke/cmd/rtk-account-video-smoke/main.go",
		"e2e_test/provisioning/bulk_bind_validation/README.md",
		"e2e_test/provisioning/bulk_bind_validation/cmd/rtk-bulk-bind-validate/main.go",
		"e2e_test/admin_bff/README.md",
		"e2e_test/video_cloud/load/cmd/rtk-video-loadtest/main.go",
		"docs/adr/README.md",
		"docs/product-level-evidence.md",
		"docs/cross-service-broker-packaging.md",
		"repos/rtk_cloud_contracts_doc/README.md",
		"scripts/README.md",
		"scripts/go/go.mod",
		"scripts/go/rtk-cloud/main.go",
		"scripts/go/rtk-cloud/internal/envroot/envroot.go",
		"scripts/go/rtk-cloud/internal/runner/runner.go",
		"scripts/go/linode-object-storage/main.go",
		"scripts/go/cloud-mqtt-test/main.go",
		"tests/helpers/factory_enroll_mock.go",
		"tests/staging-bind-devices.test.sh",
		"tests/staging-bind-validation.test.sh",
	} {
		check.requireFile(workspace, path)
	}
	if anyFileContains(workspace, []string{"README.md", "docs/architecture.md"}, "repos/rtk_mqtt") {
		check.fail("workspace README or architecture still references repos/rtk_mqtt")
	} else {
		check.pass("removed repos/rtk_mqtt workspace references")
	}

	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "== submodule registry ==")
	submodules, err := submodulePaths(workspace)
	if err != nil {
		return err
	}
	readme := readText(filepath.Join(workspace, "README.md"))
	for _, path := range submodules {
		check.requireDir(workspace, path)
		if strings.Contains(readme, "`"+path+"`") || strings.Contains(readme, path) {
			check.pass("README documents " + path)
		} else {
			check.fail("README does not document " + path)
		}
	}

	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "== service documentation entry points ==")
	for _, path := range []string{
		"repos/rtk_cloud_client/docs/README.md",
		"repos/rtk_video_cloud/docs/architecture.md",
		"repos/rtk_account_manager/docs/spec.md",
		"repos/rtk_cloud_frontend/README.md",
		"repos/rtk_cloud_admin/README.md",
	} {
		check.requireFile(workspace, path)
	}

	fmt.Fprintln(os.Stdout)
	checkContractsPolicy(check, workspace, collectContractsCommits(workspace))
	if check.failures == 0 {
		fmt.Fprintln(os.Stdout)
		fmt.Fprintln(os.Stdout, "Documentation checks passed.")
		return nil
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintf(os.Stderr, "Documentation checks failed: %d\n", check.failures)
	return exitCode(1)
}

func runSecretsCheck(args []string) error {
	fs := flag.NewFlagSet("secrets-check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	check := newCheck()
	fmt.Fprintln(os.Stdout, "== ignore rules ==")
	for _, path := range []string{
		".secrets/placeholder",
		".secrets.backup",
		".secrets/staging/linode/admin/env/admin.env",
		"cloud_env/staging/runtime/state/secrets/postgres",
		"cloud_env/staging/runtime/artifacts/test-data/rtk-test-data.sqlite",
	} {
		if err := exec.Command("git", "-C", workspace, "check-ignore", "-q", path).Run(); err == nil {
			check.pass(path + " is ignored")
		} else {
			check.fail(path + " is not ignored")
		}
	}

	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "== prohibited legacy copies ==")
	for _, path := range []string{
		filepath.Join(workspace, "cloud_env", "staging", "runtime", "state", "kubeconfig.yaml"),
		filepath.Join(workspace, "cloud_env", "staging", "runtime", "state", "secrets"),
		filepath.Join(workspace, "cloud_env", "staging", "runtime", "state", "openbao"),
	} {
		if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
			check.pass(path + " is absent")
		} else if err != nil {
			check.fail(path + " cannot be inspected")
		} else {
			check.fail(path + " is a prohibited legacy secret copy")
		}
	}

	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "== tracked workspace secret scan ==")
	workspacePaths := []string{".gitignore", "README.md", "docs", "scripts", "e2e_test"}
	checkGitGrepNoMatchFiltered(check, workspace, "private key block", `-----BEGIN ([A-Z0-9 ]+ )?PRIVATE KEY-----`, workspacePaths, func(line string) bool {
		return strings.Contains(line, "_test.go:") ||
			(strings.Contains(line, "video_relay.go:") && strings.Contains(line, "strings.ReplaceAll"))
	})
	for _, scan := range []struct {
		label   string
		pattern string
	}{
		{"bearer token literal", `Bearer[[:space:]]+[A-Za-z0-9._~+/-]{24,}`},
		{"JWT-like token", `eyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}`},
		{"hard-coded password assignment", `(^|[^A-Za-z0-9_])(PASSWORD|PASS|TOKEN|SECRET|PRIVATE_KEY)[A-Za-z0-9_]*=[^[:space:]<>$][^[:space:]]{7,}`},
	} {
		if scan.label == "hard-coded password assignment" {
			checkGitGrepNoMatchFiltered(check, workspace, scan.label, scan.pattern, workspacePaths, func(line string) bool {
				return strings.Contains(line, "_test.go:")
			})
			continue
		}
		checkGitGrepNoMatch(check, workspace, scan.label, scan.pattern, workspacePaths)
	}

	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "== manifest example ==")
	manifest := filepath.Join(workspace, "docs/examples/secrets-manifest.example.json")
	checkFileNoMatch(check, manifest, "private key block", `-----BEGIN ([A-Z0-9 ]+ )?PRIVATE KEY-----`)
	checkFileNoMatch(check, manifest, "JWT-like token", `eyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}`)
	checkFileNoMatch(check, manifest, "production staging reference", `"environment"[[:space:]]*:[[:space:]]*"production"|video-cloud-staging|staging-token|factory-linode-certset|example.invalid`)
	if check.failures > 0 {
		fmt.Fprintf(os.Stderr, "Secrets checks failed: %d\n", check.failures)
		return exitCode(1)
	}
	fmt.Fprintln(os.Stdout, "Secrets checks passed.")
	return nil
}

func runTestMatrix(args []string) error {
	fs := flag.NewFlagSet("test-matrix", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "== workspace status ==")
	if err := runCmd(workspace, "git", "status", "--short", "--branch"); err != nil {
		return err
	}
	if err := runCmd(workspace, "git", "submodule", "status", "--recursive"); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "== workspace baseline validation ==")
	if err := runCmd(workspace, "git", "diff", "--check"); err != nil {
		return err
	}
	if err := runCmd(workspace, "go", "test", "./scripts/go/..."); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "== test catalog ==")
	if err := checkTestCatalog(workspace, true); err != nil {
		return err
	}
	if cfg, err := loadCoverageConfig(workspace); err != nil {
		return err
	} else {
		fmt.Fprintf(os.Stdout, "coverage policy valid: %d modules, %.1f%% differential minimum\n", len(cfg.Modules), cfg.Differential.MinimumStatementPercent)
	}
	if err := checkUnitInventory(workspace, "", true); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "== repository status checks ==")
	submodules, err := submodulePaths(workspace)
	if err != nil {
		return err
	}
	for _, repo := range submodules {
		abs := filepath.Join(workspace, repo)
		if !exists(abs) {
			fmt.Fprintf(os.Stdout, "SKIP: %s is missing\n", repo)
			continue
		}
		fmt.Fprintf(os.Stdout, "-- %s\n", repo)
		if err := runCmd(abs, "git", "status", "--short", "--branch"); err != nil {
			return err
		}
	}
	return nil
}

type serviceTestSpec struct {
	name string
	dir  string
	cmd  []string
}

var managedServiceRepos = []string{
	"rtk_account_manager",
	"rtk_billing",
	"rtk_cloud_admin",
	"rtk_cloud_client",
	"rtk_cloud_frontend",
	"rtk_cloud_logger",
	"rtk_video_cloud",
}

func runTestServices(args []string) error {
	fs := flag.NewFlagSet("test-services", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	repoFilter := fs.String("repo", "", "comma-separated repository names; default runs all local service and SDK tests")
	changedSince := fs.String("changed-since", "", "run repositories affected between this git ref and --head-ref")
	headRef := fs.String("head-ref", "HEAD", "head git ref used with --changed-since")
	install := fs.Bool("install", false, "install npm dependencies before JavaScript service tests")
	qualificationOutputDir := fs.String("qualification-output-dir", "", "write explicit integration requirement evidence to this directory")
	qualificationRunID := fs.String("qualification-run-id", "", "stable run ID for integration requirement evidence")
	qualificationCases := fs.String("qualification-cases", "", "comma-separated qualification Test IDs; default runs every qualification case")
	qualificationOnly := fs.Bool("qualification-only", false, "run only targeted integration qualification cases")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*repoFilter) != "" && strings.TrimSpace(*changedSince) != "" {
		return errors.New("test-services accepts either --repo or --changed-since, not both")
	}
	if *qualificationOnly && strings.TrimSpace(*qualificationOutputDir) == "" {
		return errors.New("--qualification-only requires --qualification-output-dir")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	selected := map[string]bool{}
	for _, name := range strings.Split(*repoFilter, ",") {
		if name = strings.TrimSpace(name); name != "" {
			if !slices.Contains(managedServiceRepos, name) {
				return fmt.Errorf("unknown managed service repository %q", name)
			}
			selected[name] = true
		}
	}
	if strings.TrimSpace(*changedSince) != "" {
		changed, err := gitOutput(workspace, "diff", "--name-only", strings.TrimSpace(*changedSince)+"..."+strings.TrimSpace(*headRef))
		if err != nil {
			return fmt.Errorf("select changed service repositories: %w", err)
		}
		repos := selectChangedServiceRepos(strings.Fields(changed))
		if len(repos) == 0 {
			fmt.Fprintln(os.Stdout, "No service repository tests selected by the workspace diff.")
			return nil
		}
		fmt.Fprintf(os.Stdout, "Selected service repositories: %s\n", strings.Join(repos, ","))
		for _, repo := range repos {
			selected[repo] = true
		}
	}
	shouldRun := func(name string) bool {
		if len(selected) == 0 || selected[name] {
			return true
		}
		for repo := range selected {
			if strings.HasPrefix(name, repo+"/") {
				return true
			}
		}
		return false
	}

	specs := []serviceTestSpec{
		{name: "rtk_account_manager", dir: filepath.Join(workspace, "repos", "rtk_account_manager"), cmd: []string{"go", "test", "./..."}},
		{name: "rtk_billing", dir: filepath.Join(workspace, "repos", "rtk_billing"), cmd: []string{"go", "test", "./..."}},
		{name: "rtk_cloud_admin", dir: filepath.Join(workspace, "repos", "rtk_cloud_admin"), cmd: []string{"go", "test", "./..."}},
		{name: "rtk_cloud_admin/web", dir: filepath.Join(workspace, "repos", "rtk_cloud_admin", "web"), cmd: []string{"npm", "test"}},
		{name: "rtk_cloud_client/golang", dir: filepath.Join(workspace, "repos", "rtk_cloud_client", "packages", "golang"), cmd: []string{"go", "test", "./..."}},
		{name: "rtk_cloud_frontend", dir: filepath.Join(workspace, "repos", "rtk_cloud_frontend"), cmd: []string{"go", "test", "./..."}},
		{name: "rtk_cloud_logger", dir: filepath.Join(workspace, "repos", "rtk_cloud_logger"), cmd: []string{"go", "test", "./..."}},
		{name: "rtk_video_cloud", dir: filepath.Join(workspace, "repos", "rtk_video_cloud"), cmd: []string{"go", "test", "./..."}},
		{name: "rtk_video_cloud/godaddy-dns", dir: filepath.Join(workspace, "repos", "rtk_video_cloud", "tools", "godaddy-dns"), cmd: []string{"go", "test", "./..."}},
		{name: "rtk_cloud_client/javascript", dir: filepath.Join(workspace, "repos", "rtk_cloud_client", "packages", "javascript"), cmd: []string{"npm", "test"}},
		{name: "rtk_cloud_client/tools", dir: filepath.Join(workspace, "repos", "rtk_cloud_client"), cmd: []string{"python3", "-m", "unittest", "discover", "-s", "tools/tests"}},
		{name: "rtk_video_cloud/tools", dir: filepath.Join(workspace, "repos", "rtk_video_cloud"), cmd: []string{"python3", "-m", "unittest", "discover", "-s", "tools/tests"}},
	}
	for _, spec := range specs {
		if *qualificationOnly {
			break
		}
		if !shouldRun(spec.name) {
			continue
		}
		if !exists(spec.dir) {
			fmt.Fprintf(os.Stdout, "SKIP: %s is missing\n", spec.name)
			continue
		}
		fmt.Fprintf(os.Stdout, "== service tests: %s ==\n", spec.name)
		if spec.cmd[0] == "go" {
			if err := runCmdWithEnv(spec.dir, map[string]string{"GOWORK": "off"}, spec.cmd[0], spec.cmd[1:]...); err != nil {
				return err
			}
			continue
		}
		if *install && spec.cmd[0] == "npm" {
			fmt.Fprintf(os.Stdout, "== install test dependencies: %s ==\n", spec.name)
			npmCache := filepath.Join(workspace, ".artifacts", "npm-cache")
			if err := os.MkdirAll(npmCache, 0o755); err != nil {
				return fmt.Errorf("create isolated npm cache: %w", err)
			}
			if err := runCmdWithEnv(spec.dir, map[string]string{"NPM_CONFIG_CACHE": npmCache}, "npm", "ci"); err != nil {
				return err
			}
		}
		if err := runCmd(spec.dir, spec.cmd[0], spec.cmd[1:]...); err != nil {
			return err
		}
	}
	if strings.TrimSpace(*qualificationOutputDir) != "" {
		for _, requiredRepo := range []string{"rtk_account_manager", "rtk_billing", "rtk_cloud_admin", "rtk_video_cloud"} {
			if len(selected) > 0 && !selected[requiredRepo] {
				return fmt.Errorf("--qualification-output-dir requires %s to be selected", requiredRepo)
			}
		}
		qualificationSpecs, err := selectQualificationSpecs(*qualificationCases, authorizationQualificationSpecs)
		if err != nil {
			return err
		}
		if *install {
			if err := installQualificationNPMDependencies(workspace, qualificationSpecs); err != nil {
				return err
			}
		}
		if err := runAuthorizationQualificationWithSpecs(workspace, *qualificationOutputDir, *qualificationRunID, qualificationSpecs); err != nil {
			return err
		}
	}
	return nil
}

func qualificationNPMInstallDirs(specs []authorizationQualificationSpec) ([]string, error) {
	dirs := map[string]bool{}
	for _, spec := range specs {
		targets, err := authorizationQualificationTargets(spec)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", spec.TestID, err)
		}
		for _, target := range targets {
			commands := append(slices.Clone(target.SetupCommands), target.Command)
			for _, command := range commands {
				if len(command) == 0 || command[0] != "npm" {
					continue
				}
				dir := filepath.Join("repos", spec.Repository, target.WorkingDir)
				for index := 1; index < len(command); index++ {
					if command[index] == "--prefix" && index+1 < len(command) {
						dir = filepath.Join(dir, command[index+1])
						break
					}
					if prefix, ok := strings.CutPrefix(command[index], "--prefix="); ok {
						dir = filepath.Join(dir, prefix)
						break
					}
				}
				dirs[dir] = true
			}
		}
	}
	result := make([]string, 0, len(dirs))
	for dir := range dirs {
		result = append(result, dir)
	}
	slices.Sort(result)
	return result, nil
}

var qualificationNPMCI = runCmdWithEnv

func installQualificationNPMDependencies(workspace string, specs []authorizationQualificationSpec) error {
	dirs, err := qualificationNPMInstallDirs(specs)
	if err != nil {
		return err
	}
	if len(dirs) == 0 {
		return nil
	}
	npmCache := filepath.Join(workspace, ".artifacts", "npm-cache")
	if err := os.MkdirAll(npmCache, 0o755); err != nil {
		return fmt.Errorf("create isolated npm cache: %w", err)
	}
	for _, relativeDir := range dirs {
		fmt.Fprintf(os.Stdout, "== install qualification dependencies: %s ==\n", relativeDir)
		if err := qualificationNPMCI(filepath.Join(workspace, relativeDir), map[string]string{"NPM_CONFIG_CACHE": npmCache}, "npm", "ci"); err != nil {
			return err
		}
	}
	return nil
}

func selectQualificationSpecs(raw string, available []authorizationQualificationSpec) ([]authorizationQualificationSpec, error) {
	if strings.TrimSpace(raw) == "" {
		return available, nil
	}
	requested := map[string]bool{}
	for _, testID := range strings.Split(raw, ",") {
		if testID = strings.TrimSpace(testID); testID != "" {
			requested[testID] = true
		}
	}
	selected := make([]authorizationQualificationSpec, 0, len(requested))
	for _, spec := range available {
		if requested[spec.TestID] {
			selected = append(selected, spec)
			delete(requested, spec.TestID)
		}
	}
	if len(requested) == 0 {
		return selected, nil
	}
	unknown := make([]string, 0, len(requested))
	for testID := range requested {
		unknown = append(unknown, testID)
	}
	sort.Strings(unknown)
	return nil, fmt.Errorf("unknown qualification cases: %s", strings.Join(unknown, ", "))
}

func runTestE2E(args []string) error {
	fs := flag.NewFlagSet("test-e2e", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	scripts := fs.Bool("scripts", false, "also run root staging script contract tests")
	runID := fs.String("run-id", "", "stable evidence run ID")
	outputDir := fs.String("output-dir", "", "evidence output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	if *runID == "" {
		*runID = time.Now().UTC().Format("20060102T150405Z") + "-e2e"
	}
	if *outputDir == "" {
		*outputDir = filepath.Join(workspace, ".artifacts", "test-runs", *runID, "e2e")
	}
	started := time.Now().UTC()

	fmt.Fprintln(os.Stdout, "== E2E Go packages ==")
	if err := runCmdWithEnv(filepath.Join(workspace, "e2e_test"), map[string]string{"GOWORK": "off"}, "go", "test", "./..."); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "== MQTT E2E harness ==")
	if err := runCmdWithEnv(filepath.Join(workspace, "scripts", "go"), map[string]string{"GOWORK": "off"}, "go", "test", "./cloud-mqtt-test"); err != nil {
		return err
	}
	if *scripts {
		fmt.Fprintln(os.Stdout, "== workspace staging script contract tests ==")
		tests, err := filepath.Glob(filepath.Join(workspace, "tests", "*.test.sh"))
		if err != nil {
			return err
		}
		sort.Strings(tests)
		for _, test := range tests {
			if filepath.Base(test) == "no-deprecated-staging-wrappers.test.sh" {
				fmt.Fprintln(os.Stdout, "SKIP: no-deprecated-staging-wrappers.test.sh is a repository governance gate, not an E2E test")
				continue
			}
			if err := runCmd(workspace, "bash", test); err != nil {
				return err
			}
		}
	}
	fmt.Fprintln(os.Stdout, "== E2E report-tool tests ==")
	if err := runCmd(workspace, "python3", "-m", "unittest", "discover", "-s", "e2e_test/video_cloud/load/tools/tests"); err != nil {
		return err
	}
	return writeDeterministicE2EEvidence(workspace, *outputDir, *runID, started, time.Now().UTC(), *scripts)
}

func writeDeterministicE2EEvidence(workspace, outputDir, runID string, started, completed time.Time, scripts bool) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return err
	}
	commit, err := gitOutput(workspace, "rev-parse", "HEAD")
	if err != nil {
		return err
	}
	summary := map[string]any{
		"schema_version": "rtk-cloud-deterministic-e2e-result/v2",
		"run_id":         runID, "status": "PASS", "assessment": "deterministic E2E harness suites passed",
		"started_at": started.Format(time.RFC3339), "completed_at": completed.Format(time.RFC3339),
		"workspace_commit": strings.TrimSpace(commit), "scripts_included": scripts,
	}
	resultsPath := filepath.Join(outputDir, "results.json")
	if err := writeJSON(resultsPath, summary); err != nil {
		return err
	}
	raw, err := os.ReadFile(resultsPath)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(raw)
	specCommit, err := currentCanonicalSpecCommit(workspace)
	if err != nil {
		return err
	}
	manifest := featureEvidenceManifestV2{
		SchemaVersion: featureEvidenceSchemaV3, RunID: runID, GeneratedAt: completed.Format(time.RFC3339), SpecCommit: specCommit,
		Cases: []featureCaseEvidenceV2{{
			TestID: "SVC-WS-E2E-001", Status: "PASS", Assessment: "supporting deterministic harness evidence",
			Environment: "ci", StartedAt: started.Format(time.RFC3339), CompletedAt: completed.Format(time.RFC3339), WorkspaceCommit: strings.TrimSpace(commit),
			Commits: map[string]string{"workspace": strings.TrimSpace(commit)}, Requirements: []featureRequirementAssertion{},
		}},
	}
	if err := writeJSON(filepath.Join(outputDir, "evidence-manifest.json"), manifest); err != nil {
		return err
	}
	junit := fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><testsuite name="workspace-deterministic-e2e" tests="1" failures="0" time="%.3f"><testcase classname="workspace" name="SVC-WS-E2E-001" time="%.3f"><system-out>results.json sha256=%x</system-out></testcase></testsuite>`+"\n",
		completed.Sub(started).Seconds(), completed.Sub(started).Seconds(), sum)
	if err := os.WriteFile(filepath.Join(outputDir, "junit.xml"), []byte(junit), 0o644); err != nil {
		return err
	}
	report := fmt.Sprintf("# Deterministic E2E\n\n- Status: **PASS**\n- Test ID: `SVC-WS-E2E-001` (supporting evidence only)\n- Run ID: `%s`\n- Workspace commit: `%s`\n- Results SHA-256: `%x`\n", runID, strings.TrimSpace(commit), sum)
	return os.WriteFile(filepath.Join(outputDir, "test_report.md"), []byte(report), 0o644)
}

func runTestUI(args []string) error {
	fs := flag.NewFlagSet("test-ui", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	install := fs.Bool("install", false, "install npm dependencies and the Chromium browser before testing")
	full := fs.Bool("full", false, "run the full browser suite instead of the smoke suite")
	staging := fs.Bool("staging", false, "run read-only headless tests against E2E_BASE_URL")
	desktop := fs.Bool("desktop", false, "run the desktop Chromium project")
	mobile := fs.Bool("mobile", false, "run the mobile viewport project")
	runID := fs.String("run-id", "", "stable test run ID; defaults to GitHub run identity or a UTC timestamp")
	outputDir := fs.String("output-dir", "", "evidence output root below .artifacts/test-runs; defaults to the run ID UI directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *runID == "" {
		if githubRunID, githubAttempt := strings.TrimSpace(os.Getenv("GITHUB_RUN_ID")), strings.TrimSpace(os.Getenv("GITHUB_RUN_ATTEMPT")); githubRunID != "" && githubAttempt != "" {
			*runID = "gh-" + githubRunID + "-" + githubAttempt
		} else {
			*runID = time.Now().UTC().Format("20060102T150405Z")
		}
	}
	if ok, _ := regexp.MatchString(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,127}$`, *runID); !ok {
		return errors.New("--run-id must contain only letters, digits, dot, underscore, and hyphen")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	uiOutputRoot, err := resolveUIEvidenceOutputRoot(workspace, *runID, *outputDir)
	if err != nil {
		return err
	}
	webRoot := filepath.Join(workspace, "repos", "rtk_cloud_admin", "web")
	if !exists(webRoot) {
		return errors.New("rtk_cloud_admin/web is missing")
	}
	if *install {
		fmt.Fprintln(os.Stdout, "== UI dependencies ==")
		if err := runCmd(webRoot, "npm", "ci"); err != nil {
			return err
		}
		if err := installPlaywright(webRoot, runtime.GOOS); err != nil {
			return err
		}
	}
	if !exists(filepath.Join(webRoot, "node_modules", ".bin", "playwright")) {
		return errors.New("Playwright is not installed; rerun test-ui with --install")
	}

	fmt.Fprintln(os.Stdout, "== UI build ==")
	if err := runCmd(webRoot, "npm", "run", "build"); err != nil {
		return err
	}
	if *staging {
		for _, name := range []string{"E2E_BASE_URL", "E2E_PLATFORM_SESSION_ID", "E2E_CUSTOMER_SESSION_ID", "E2E_EVIDENCE_SAFE"} {
			if strings.TrimSpace(os.Getenv(name)) == "" {
				return fmt.Errorf("%s is required for test-ui --staging", name)
			}
		}
		if os.Getenv("E2E_EVIDENCE_SAFE") != "1" {
			return errors.New("test-ui --staging requires E2E_EVIDENCE_SAFE=1 to confirm dedicated test data is safe for artifact upload")
		}
		fmt.Fprintln(os.Stdout, "== headless UI E2E: deployed staging backend ==")
		targets := []string{}
		if *desktop {
			targets = append(targets, "desktop")
		}
		if *mobile {
			targets = append(targets, "mobile")
		}
		if len(targets) == 0 {
			targets = []string{"desktop"}
		}
		for _, target := range targets {
			project := "staging"
			if target == "mobile" {
				project = "staging-mobile"
			}
			expected, err := expectedUITestIDs(workspace, target, "staging", !*full)
			if err != nil {
				return err
			}
			env, err := uiEvidenceEnv(workspace, webRoot, uiOutputRoot, *runID, target, "staging", expected)
			if err != nil {
				return err
			}
			env["E2E_UI_TARGET"] = target
			if err := os.RemoveAll(env["E2E_TEST_RUN_DIR"]); err != nil {
				return fmt.Errorf("reset UI artifact directory: %w", err)
			}
			runErr := runCmdWithEnv(webRoot, env, "npx", "playwright", "test", "--project="+project)
			if evidenceErr := validateUIEvidenceRun(env["E2E_TEST_RUN_DIR"], expected); evidenceErr != nil {
				return evidenceErr
			}
			if evidenceErr := writeNormalizedUIEvidence(workspace, env["E2E_TEST_RUN_DIR"]); evidenceErr != nil {
				return evidenceErr
			}
			if runErr != nil {
				return runErr
			}
		}
		return nil
	}

	fmt.Fprintln(os.Stdout, "== UI fixture generation ==")
	if err := runCmd(webRoot, "npm", "run", "e2e:generate-fixture"); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "== headless UI E2E: browser -> Go BFF -> fixture upstreams ==")
	targets := []string{}
	if *desktop {
		targets = append(targets, "chromium")
	}
	if *mobile {
		targets = append(targets, "mobile")
	}
	if len(targets) == 0 {
		targets = []string{"chromium", "mobile"}
	}
	for _, target := range targets {
		evidenceTarget := target
		if target == "chromium" {
			evidenceTarget = "desktop"
		}
		fmt.Fprintf(os.Stdout, "-- UI target: %s (run %s)\n", evidenceTarget, *runID)
		playwrightArgs := []string{"playwright", "test", "--project=" + target}
		if !*full {
			playwrightArgs = append(playwrightArgs, "--grep", "@smoke")
		} else {
			// Full qualification cases share a mutable fixture backend (for
			// example provider lifecycle state), so parallel workers can
			// invalidate another case's navigation or expected state.
			// Visual snapshots run in a separate invocation so lifecycle tests
			// cannot change the fixture state they compare against.
			playwrightArgs = append(playwrightArgs, "--workers=1", "--grep-invert", "@visual")
		}
		expected, err := expectedUITestIDs(workspace, evidenceTarget, "local", !*full)
		if err != nil {
			return err
		}
		env, err := uiEvidenceEnv(workspace, webRoot, uiOutputRoot, *runID, evidenceTarget, "local", expected)
		if err != nil {
			return err
		}
		if err := os.RemoveAll(env["E2E_TEST_RUN_DIR"]); err != nil {
			return fmt.Errorf("reset UI artifact directory: %w", err)
		}
		if *full {
			env["E2E_EXPECTED_TEST_IDS"] = ""
		}
		runErr := runCmdWithEnv(webRoot, env, "npx", playwrightArgs...)
		if *full {
			type uiPhase struct {
				name string
				grep string
				env  map[string]string
			}
			phases := []uiPhase{
				{name: "visual", grep: "@visual", env: map[string]string{}},
			}
			if evidenceTarget == "desktop" {
				phases = append(phases,
					uiPhase{name: "unavailable", grep: "UI-CA-(SOURCE-003|SOURCE-004|REPORT-001|CHIPSET-003|CLOUD-003|DASH-002)", env: map[string]string{"E2E_SCENARIO_MODE": "unavailable", "E2E_PROMETHEUS_MODE": "unavailable"}},
					uiPhase{name: "empty", grep: "UI-CA-(SOURCE-001|DASH-003)", env: map[string]string{"E2E_SCENARIO_MODE": "empty", "E2E_PROMETHEUS_MODE": "empty"}},
					uiPhase{name: "stale", grep: "UI-CA-(SOURCE-002|DASH-003)", env: map[string]string{"E2E_SCENARIO_MODE": "stale", "E2E_PROMETHEUS_MODE": "stale"}},
					uiPhase{name: "expired", grep: "UI-CA-REPORT-002", env: map[string]string{"E2E_RESULT_EXPIRED": "true"}},
					uiPhase{name: "partial-failure", grep: "UI-CA-BATCH-001", env: map[string]string{"E2E_SCENARIO_MODE": "partial_failure"}},
					uiPhase{name: "slow", grep: "UI-CA-BATCH-002", env: map[string]string{"E2E_SCENARIO_MODE": "slow"}},
					uiPhase{name: "member-assign-failure", grep: "UI-CA-CLOUD-006", env: map[string]string{"E2E_FAIL_ACTION": "member-assign"}},
				)
			}
			for _, phase := range phases {
				fmt.Fprintf(os.Stdout, "-- UI phase: %s\n", phase.name)
				phaseEnv := make(map[string]string, len(env)+len(phase.env)+1)
				for key, value := range env {
					phaseEnv[key] = value
				}
				for key, value := range phase.env {
					phaseEnv[key] = value
				}
				phaseEnv["E2E_TEST_PHASE"] = phase.name
				if err := runCmdWithEnv(webRoot, phaseEnv, "npx", "playwright", "test", "--project="+target, "--workers=1", "--grep", phase.grep); err != nil {
					fmt.Fprintf(os.Stderr, "UI phase %s reported a test failure: %v\n", phase.name, err)
					runErr = err
				}
			}
		}
		if err := validateUIEvidenceRun(env["E2E_TEST_RUN_DIR"], expected); err != nil {
			return err
		}
		if err := writeNormalizedUIEvidence(workspace, env["E2E_TEST_RUN_DIR"]); err != nil {
			return err
		}
		if runErr != nil {
			return runErr
		}
	}
	return nil
}

func playwrightInstallArguments(goos string) []string {
	// Visual qualification snapshots depend on the same system font and browser
	// libraries in every Linux runner. Installing Chromium alone can render a
	// stable but different image from the dedicated UI job, which installs
	// Playwright's OS dependencies as well.
	if goos == "linux" {
		return []string{"playwright", "install", "--with-deps", "chromium"}
	}
	return []string{"playwright", "install", "chromium"}
}

var playwrightInstallCommand = runCmd
var playwrightInstallSleep = time.Sleep

func installPlaywright(webRoot, goos string) error {
	attempts := 1
	if goos == "linux" {
		// Multiple repository-scoped runners can share one host. Their apt-based
		// setup steps may briefly contend for the global dpkg/apt locks.
		attempts = 5
	}
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		err = playwrightInstallCommand(webRoot, "npx", playwrightInstallArguments(goos)...)
		if err == nil {
			return nil
		}
		if attempt < attempts {
			fmt.Fprintf(os.Stderr, "UI browser dependency install attempt %d/%d failed; retrying in 15s: %v\n", attempt, attempts, err)
			playwrightInstallSleep(15 * time.Second)
		}
	}
	return fmt.Errorf("UI browser dependency install failed after %d attempt(s): %w", attempts, err)
}

func resolveUIEvidenceOutputRoot(workspace, runID, raw string) (string, error) {
	artifactRoot := filepath.Join(workspace, ".artifacts", "test-runs")
	outputRoot := strings.TrimSpace(raw)
	if outputRoot == "" {
		outputRoot = filepath.Join(artifactRoot, runID, "ui")
	} else if !filepath.IsAbs(outputRoot) {
		outputRoot = filepath.Join(workspace, outputRoot)
	}
	absArtifactRoot, err := filepath.Abs(artifactRoot)
	if err != nil {
		return "", err
	}
	absOutputRoot, err := filepath.Abs(outputRoot)
	if err != nil {
		return "", err
	}
	relative, err := filepath.Rel(absArtifactRoot, absOutputRoot)
	if err != nil || relative == "." || relative == "" || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", errors.New("--output-dir must identify a child of the workspace .artifacts/test-runs directory")
	}
	return absOutputRoot, nil
}

func uiEvidenceEnv(workspace, webRoot, outputRoot, runID, target, environment string, expected []string) (map[string]string, error) {
	workspaceCommit, err := gitOutput(workspace, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	submoduleRoot := filepath.Dir(webRoot)
	submoduleCommit, err := gitOutput(submoduleRoot, "rev-parse", "HEAD")
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"E2E_UI_TARGET":         target,
		"E2E_TEST_RUN_ID":       runID,
		"E2E_TEST_TARGET":       target,
		"E2E_TEST_ENVIRONMENT":  environment,
		"E2E_TEST_RUN_DIR":      filepath.Join(outputRoot, target),
		"E2E_EXPECTED_TEST_IDS": strings.Join(expected, ","),
		"E2E_WORKSPACE_COMMIT":  strings.TrimSpace(workspaceCommit),
		"E2E_SUBMODULE_COMMIT":  strings.TrimSpace(submoduleCommit),
	}, nil
}

func validateUIEvidenceRun(runDir string, expected []string) error {
	raw, err := os.ReadFile(filepath.Join(runDir, "evidence-manifest.json"))
	if err != nil {
		return fmt.Errorf("read UI evidence manifest: %w", err)
	}
	var manifest struct {
		Cases []struct {
			TestID           string `json:"test_id"`
			Assessment       string `json:"assessment"`
			ScreenshotPath   string `json:"screenshot_path"`
			ScreenshotSHA256 string `json:"screenshot_sha256"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &manifest); err != nil {
		return fmt.Errorf("parse UI evidence manifest: %w", err)
	}
	byID := make(map[string]struct {
		assessment string
		screenshot string
		checksum   string
	}, len(manifest.Cases))
	for _, item := range manifest.Cases {
		byID[item.TestID] = struct {
			assessment string
			screenshot string
			checksum   string
		}{item.Assessment, item.ScreenshotPath, item.ScreenshotSHA256}
	}
	var failures []string
	for _, id := range expected {
		item, ok := byID[id]
		if !ok {
			failures = append(failures, id+" has no result")
			continue
		}
		if item.assessment != "PASS" {
			failures = append(failures, id+" assessment is "+item.assessment)
			continue
		}
		screenshot := filepath.Join(runDir, filepath.FromSlash(item.screenshot))
		content, err := os.ReadFile(screenshot)
		if err != nil {
			failures = append(failures, id+" screenshot is missing")
			continue
		}
		sum := fmt.Sprintf("%x", sha256.Sum256(content))
		if item.checksum == "" || sum != item.checksum {
			failures = append(failures, id+" screenshot checksum is invalid")
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("UI evidence validation failed: %s", strings.Join(failures, "; "))
	}
	fmt.Fprintf(os.Stdout, "UI evidence valid: %d required cases in %s\n", len(expected), runDir)
	return nil
}

func writeNormalizedUIEvidence(workspace, runDir string) error {
	catalog, err := loadAndValidateTestCatalogForRunner(workspace, "test-ui")
	if err != nil {
		return err
	}
	sourcePath := filepath.Join(runDir, "evidence-manifest.json")
	raw, err := os.ReadFile(sourcePath)
	if err != nil {
		return err
	}
	manifest, err := adaptLegacyFeatureEvidence(workspace, raw, sourcePath, catalog)
	if err != nil {
		return err
	}
	var sourceManifest struct {
		Cases []struct {
			TestID           string `json:"test_id"`
			ScreenshotPath   string `json:"screenshot_path"`
			ScreenshotSHA256 string `json:"screenshot_sha256"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &sourceManifest); err != nil {
		return fmt.Errorf("parse UI evidence paths: %w", err)
	}
	screenshots := map[string]featureCoverageEvidenceFile{}
	for _, item := range sourceManifest.Cases {
		if item.ScreenshotPath != "" && item.ScreenshotSHA256 != "" {
			screenshots[item.TestID] = featureCoverageEvidenceFile{
				Path: item.ScreenshotPath, SHA256: item.ScreenshotSHA256, Type: "screenshot",
			}
		}
	}
	inventory, err := loadAvailableSpecInventory(workspace)
	if err != nil {
		return err
	}
	requirements := catalogRequirementIndex(catalog)
	featuresByRequirement := catalogFeatureByRequirement(catalog)
	cases := map[string]testCatalogCase{}
	for _, tc := range catalog.Cases {
		cases[tc.ID] = tc
	}
	commonEvidence := make([]featureCoverageEvidenceFile, 0, 2)
	for _, item := range []struct {
		path, evidenceType string
	}{
		{"evidence-manifest.json", "json"},
		{"junit.xml", "junit"},
	} {
		sha, hashErr := fileSHA256(filepath.Join(runDir, item.path))
		if hashErr != nil {
			return fmt.Errorf("hash UI %s evidence: %w", item.evidenceType, hashErr)
		}
		commonEvidence = append(commonEvidence, featureCoverageEvidenceFile{Path: item.path, SHA256: sha, Type: item.evidenceType})
	}
	for caseIndex := range manifest.Cases {
		item := &manifest.Cases[caseIndex]
		tc, ok := cases[item.TestID]
		if !ok {
			return fmt.Errorf("UI evidence contains unknown test case %s", item.TestID)
		}
		if item.Commits == nil {
			item.Commits = map[string]string{}
		}
		// The normalized UI assertions are revision-bound to the canonical
		// contracts checkout used to build this manifest.
		item.Commits["contracts"] = manifest.SpecCommit
		for _, requirementID := range tc.Verifies {
			feature, exists := featuresByRequirement[requirementID]
			if !exists {
				return fmt.Errorf("UI case %s requirement %s has no feature", tc.ID, requirementID)
			}
			commits, commitErr := currentFeatureCommits(workspace, feature)
			if commitErr != nil {
				return commitErr
			}
			for anchor, commit := range commits {
				item.Commits[anchor] = commit
			}
		}
		refs := append([]featureCoverageEvidenceFile(nil), commonEvidence...)
		if screenshot, exists := screenshots[item.TestID]; exists {
			refs = append(refs, screenshot)
		}
		status := strings.ToUpper(item.Status)
		item.Requirements = nil
		for _, requirementID := range tc.Verifies {
			requirement, exists := requirements[requirementID]
			if !exists {
				return fmt.Errorf("UI case %s verifies unknown requirement %s", tc.ID, requirementID)
			}
			item.Requirements = append(item.Requirements, featureRequirementAssertion{
				RequirementID: requirementID,
				Revision:      requirement.Revision,
				SpecSource:    requirement.SpecSource,
				Status:        status,
				Assessment:    "Playwright observable behavior and evidence contract assessed for this requirement",
				Assertions: map[string]string{
					"observable_ui_behavior": status,
					"evidence_contract":      status,
				},
				Evidence: refs,
			})
		}
		item.Workflows = uiWorkflowAssertions(inventory, tc, status)
	}
	if err := validateFeatureEvidenceManifestV2(manifest, catalog, inventory); err != nil {
		return err
	}
	return writeJSON(filepath.Join(runDir, "feature-evidence.json"), manifest)
}

func uiWorkflowAssertions(inventory specInventory, tc testCatalogCase, status string) []featureWorkflowAssertion {
	var assertions []featureWorkflowAssertion
	for _, workflow := range inventory.Workflows {
		bound := false
		for _, requirementID := range workflow.RequirementIDs {
			if catalogContainsString(tc.Verifies, requirementID) {
				bound = true
				break
			}
		}
		if !bound {
			continue
		}
		assertion := featureWorkflowAssertion{WorkflowID: workflow.ID, Revision: workflow.Revision, Status: status}
		for _, step := range workflow.Steps {
			assertion.Steps = append(assertion.Steps, featureWorkflowStepAssertion{
				StepID: step.ID, OperationRef: step.OperationRef, Status: status,
				Assertions: map[string]string{"observable_step_behavior": status},
			})
		}
		assertions = append(assertions, assertion)
	}
	sort.Slice(assertions, func(i, j int) bool { return assertions[i].WorkflowID < assertions[j].WorkflowID })
	return assertions
}

func runTestLive(args []string) error {
	args = ensureTestLiveMode(args)
	runID := commandFlagValue(args, "--run-id")
	args = removeFlagValue(args, "--run-id")
	if runID == "" {
		runID = firstNonEmpty(os.Getenv("RUNTIME_COVERAGE_RUN_ID"), time.Now().UTC().Format("20060102T150405Z")+"-live")
	}
	workspace := commandFlagValue(args, "--workspace")
	if workspace == "" {
		var err error
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	if hasFlag(args, "--run") && commandFlagValue(args, "--out-dir") == "" {
		args = append(args, "--out-dir", filepath.Join(workspace, ".artifacts", "test-runs", runID, "live"))
	}
	started := time.Now().UTC()
	if err := runStagingE2ETest(args); err != nil {
		return err
	}
	if !hasFlag(args, "--run") {
		return nil
	}
	outDir := commandFlagValue(args, "--out-dir")
	if err := writeLiveOnboardingWorkflowEvidence(outDir); err != nil {
		return err
	}
	for _, testID := range []string{"LIVE-STG-ONBOARD-001", "E2E-PROV-ACCOUNT-001", "E2E-PROV-BULK-001"} {
		if err := writeCaseFeatureEvidence(
			workspace, outDir, testID, runID,
			"staging", "", started, time.Now().UTC(),
		); err != nil {
			return err
		}
	}
	return nil
}

func commandFlagValue(args []string, name string) string {
	for i, arg := range args {
		if arg == name && i+1 < len(args) {
			return args[i+1]
		}
		if key, value, ok := strings.Cut(arg, "="); ok && key == name {
			return value
		}
	}
	return ""
}

func removeFlagValue(args []string, name string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == name {
			if i+1 < len(args) {
				i++
			}
			continue
		}
		if key, _, ok := strings.Cut(args[i], "="); ok && key == name {
			continue
		}
		out = append(out, args[i])
	}
	return out
}

func ensureTestLiveMode(args []string) []string {
	if hasFlag(args, "--plan") || hasFlag(args, "--run") {
		return args
	}
	return append(append([]string(nil), args...), "--plan")
}

type loadDeviceType struct {
	Name           string
	Model          string
	Capability     string
	ServiceOptions []string
	Capabilities   []string
}

var loadDeviceTypes = []loadDeviceType{
	{"camera", "RTC-CAM-PRO2-SIM", "camera", []string{"mqtt", "video_streaming", "video_storage"}, []string{"camera_event", "status_report", "snapshot", "websocket_owner", "webrtc", "recording_clip", "mqtt_legacy_snapshot"}},
	{"light", "RTC-LIGHT-SIM", "light", []string{"mqtt"}, []string{"mqtt", "power", "brightness", "color_temperature", "state_report", "command_result"}},
	{"switch", "RTC-SWITCH-SIM", "switch", []string{"mqtt"}, []string{"mqtt", "power", "state_report", "command_result"}},
	{"smart_plug", "RTC-PLUG-SIM", "smart_plug", []string{"mqtt"}, []string{"mqtt", "power", "energy_watts", "state_report", "command_result"}},
	{"air_conditioner", "RTC-AC-SIM", "air_conditioner", []string{"mqtt"}, []string{"mqtt", "power", "target_temperature", "mode", "fan", "state_report", "command_result"}},
	{"environment_sensor", "RTC-ENV-SENSOR-SIM", "environment_sensor", []string{"mqtt"}, []string{"mqtt", "temperature_c", "humidity_percent", "telemetry_report", "state_report"}},
	{"security_sensor", "RTC-SECURITY-SENSOR-SIM", "security_sensor", []string{"mqtt"}, []string{"mqtt", "open_closed", "motion", "event_report", "state_report"}},
	{"smart_meter", "RTC-METER-SIM", "smart_meter", []string{"mqtt"}, []string{"mqtt", "status_report", "telemetry_report", "power_watts", "energy_kwh", "voltage", "current"}},
	{"camera_status", "RTC-CAM-STATUS-SIM", "camera_status", []string{"mqtt"}, []string{"mqtt", "status_report", "motion_event", "privacy_mode", "command_result"}},
	{"door_lock", "RTC-LOCK-SIM", "door_lock", []string{"mqtt"}, []string{"mqtt", "locked", "battery_percent", "state_report", "command_result"}},
	{"appliance", "RTC-APPLIANCE-SIM", "appliance", []string{"mqtt"}, []string{"mqtt", "run_state", "mode", "remaining_minutes", "state_report", "command_result"}},
	{"gateway", "RTC-GATEWAY-SIM", "gateway", []string{"mqtt"}, []string{"mqtt", "child_device_count", "network_status", "batch_state_report", "command_result"}},
}

type generatedDevice struct {
	DeviceID             string   `json:"device_id"`
	DeviceType           string   `json:"device_type"`
	DeviceItemProfileID  string   `json:"device_item_profile_id,omitempty"`
	MQTTCapability       string   `json:"mqtt_capability"`
	ServiceOptions       []string `json:"service_options"`
	Model                string   `json:"model"`
	DisplayName          string   `json:"display_name"`
	FirmwareVersion      string   `json:"firmware_version"`
	Capabilities         []string `json:"capabilities"`
	CertificateProfile   string   `json:"certificate_profile"`
	KeyAlgorithm         string   `json:"key_algorithm"`
	CertificatePath      string   `json:"certificate_path"`
	CertificateChainPath string   `json:"certificate_chain_path"`
	KeyPath              string   `json:"key_path"`
	CSRPath              string   `json:"csr_path"`
	BundlePath           string   `json:"bundle_path"`
	Production           bool     `json:"production"`
	Warning              string   `json:"warning"`
}

func runGenerateLoadDevices(args []string) error {
	fs := flag.NewFlagSet("generate-load-devices", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	count := fs.Int("count", 100, "device count")
	mix := fs.String("mix", "camera=40,light=25,air_conditioner=20,smart_meter=15", "device mix")
	prefix := fs.String("prefix", "load-device", "device prefix")
	brandname := fs.String("brandname", "RTK", "brand name")
	outDir := fs.String("out-dir", "", "output directory")
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	factoryURL := fs.String("factory-url", os.Getenv("FACTORY_ENROLL_URL"), "factory enroll URL")
	factoryAuthKey := fs.String("factory-auth-key", os.Getenv("FACTORY_ENROLL_AUTH_KEY"), "factory enroll auth key")
	factoryProductionJWT := strings.TrimSpace(os.Getenv("FACTORY_ENROLL_PRODUCTION_JWT"))
	factoryID := fs.String("factory-id", firstNonEmpty(os.Getenv("FACTORY_ENROLL_FACTORY_ID"), "staging-loadtest"), "factory id")
	lineID := fs.String("line-id", firstNonEmpty(os.Getenv("FACTORY_ENROLL_LINE_ID"), "loadtest-line"), "line id")
	stationID := fs.String("station-id", firstNonEmpty(os.Getenv("FACTORY_ENROLL_STATION_ID"), "loadtest-station"), "station id")
	fixtureID := fs.String("fixture-id", firstNonEmpty(os.Getenv("FACTORY_ENROLL_FIXTURE_ID"), "loadtest-fixture"), "fixture id")
	operatorID := fs.String("operator-id", firstNonEmpty(os.Getenv("FACTORY_ENROLL_OPERATOR_ID"), "loadtest-operator"), "operator id")
	batchID := fs.String("batch-id", os.Getenv("FACTORY_ENROLL_BATCH_ID"), "batch id")
	serialPrefix := fs.String("serial-prefix", firstNonEmpty(os.Getenv("FACTORY_ENROLL_SERIAL_PREFIX"), "LOAD"), "serial prefix")
	runID := fs.String("run-id", os.Getenv("FACTORY_ENROLL_RUN_ID"), "run id")
	timeoutSeconds := fs.Int("enroll-timeout", envInt("FACTORY_ENROLL_TIMEOUT", 30), "enroll timeout seconds")
	generateOnly := fs.Bool("generate-only", false, "generate only")
	caValidDays := fs.Int("ca-valid-days", 365, "CA validity days")
	deviceValidDays := fs.Int("device-valid-days", 180, "device validity days")
	concurrency := fs.Int("concurrency", envInt("CLOUD_CREATE_DEVICES_CONCURRENCY", 64), "device generation concurrency")
	force := fs.Bool("force", false, "force")
	if err := fs.Parse(args); err != nil {
		return err
	}
	factoryProductionJWTByType, err := envJSONTextMap("FACTORY_ENROLL_PRODUCTION_JWT_BY_DEVICE_TYPE")
	if err != nil {
		return err
	}
	factoryBatchIDByType, err := envJSONTextMap("FACTORY_ENROLL_BATCH_ID_BY_DEVICE_TYPE")
	if err != nil {
		return err
	}
	factoryProfileIDByType, err := envJSONTextMap("FACTORY_ENROLL_DEVICE_ITEM_PROFILE_ID_BY_DEVICE_TYPE")
	if err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging")
	}
	if *count <= 0 {
		return errors.New("--count must be a positive integer")
	}
	if *caValidDays <= 0 || *deviceValidDays <= 0 || *timeoutSeconds <= 0 {
		return errors.New("validity days and enroll timeout must be positive integers")
	}
	if *concurrency <= 0 {
		return errors.New("--concurrency must be greater than zero")
	}
	if ok, _ := regexp.MatchString(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`, *prefix); !ok {
		return errors.New("--prefix contains unsupported characters")
	}
	workspace := *workspaceFlag
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	stackValues, _ := readEnvFile(filepath.Join(envRoot, "env", "stack.env"))
	deviceKeyAlgorithms, err := deploymentCertificateAlgorithms("CERTIFICATE_DEVICE_CSR_KEY_ALGORITHMS", stackValues["CERTIFICATE_DEVICE_CSR_KEY_ALGORITHMS"])
	if err != nil {
		return fmt.Errorf("load staging certificate policy: %w", err)
	}
	if *runID == "" {
		*runID = time.Now().UTC().Format("20060102T150405Z")
	}
	if *batchID == "" {
		*batchID = *runID
	}
	if *outDir == "" {
		*outDir = filepath.Join(envRoot, "devices", "test_device")
	}
	*brandname = strings.TrimSpace(*brandname)
	if *brandname == "" {
		return errors.New("--brandname is required")
	}
	if !*generateOnly {
		videoEnv := firstExistingPath(filepath.Join(envRoot, "services", "video-cloud", "video-cloud.env"), filepath.Join(envRoot, "services", "video-cloud", "video-cloud-staging.env"))
		if *factoryURL == "" {
			*factoryURL = envFileValue(videoEnv, "FACTORY_ENROLL_URL")
		}
		if factoryProductionJWT == "" && len(factoryProductionJWTByType) == 0 && *factoryAuthKey == "" {
			*factoryAuthKey = envFileValue(videoEnv, "FACTORY_ENROLL_AUTH_KEY")
		}
		if *factoryURL == "" {
			return errors.New("factory enrollment URL missing; set FACTORY_ENROLL_URL in video-cloud env or pass --factory-url")
		}
		if factoryProductionJWT == "" && len(factoryProductionJWTByType) == 0 && *factoryAuthKey == "" {
			return errors.New("factory enrollment credential missing; set ephemeral FACTORY_ENROLL_PRODUCTION_JWT or FACTORY_ENROLL_AUTH_KEY")
		}
		*factoryURL = normalizeFactoryEnrollURLs(*factoryURL)
	}
	if exists(*outDir) {
		if !*force {
			return fmt.Errorf("%s already exists; use --force to replace it", *outDir)
		}
		if err := os.RemoveAll(*outDir); err != nil {
			return fmt.Errorf("remove existing output directory for --force: %w", err)
		}
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	*outDir, _ = filepath.Abs(*outDir)
	opensslLog := filepath.Join(*outDir, "openssl.log")
	if err := os.WriteFile(opensslLog, nil, 0o644); err != nil {
		return err
	}
	mode := "factory_enroll"
	if *generateOnly {
		mode = "generate_only"
	}
	logLoad("start load-test device generation: count=%d mix=%s mode=%s%s", *count, *mix, mode, factoryLogSuffix(mode, *factoryURL))
	logLoad("workspace=%s", workspace)
	logLoad("output=%s", *outDir)
	logLoad("run_id=%s batch_id=%s", *runID, *batchID)
	alloc, err := allocateDeviceMix(*count, *mix)
	if err != nil {
		return err
	}
	if !*generateOnly && factoryProductionJWT == "" && len(factoryProductionJWTByType) > 0 {
		for _, deviceType := range loadDeviceTypes {
			if alloc[deviceType.Name] == 0 {
				continue
			}
			if factoryProductionJWTByType[deviceType.Name] == "" {
				return fmt.Errorf("FACTORY_ENROLL_PRODUCTION_JWT_BY_DEVICE_TYPE is missing %s", deviceType.Name)
			}
			if factoryBatchIDByType[deviceType.Name] == "" {
				return fmt.Errorf("FACTORY_ENROLL_BATCH_ID_BY_DEVICE_TYPE is missing %s", deviceType.Name)
			}
			if factoryProfileIDByType[deviceType.Name] == "" {
				return fmt.Errorf("FACTORY_ENROLL_DEVICE_ITEM_PROFILE_ID_BY_DEVICE_TYPE is missing %s", deviceType.Name)
			}
		}
	}
	var caKey *ecdsa.PrivateKey
	var caCert []byte
	if *generateOnly {
		logLoad("generating simulation device CA")
		caKey, caCert, err = writeGeneratedCA(*outDir, *caValidDays)
		if err != nil {
			return err
		}
	}
	manifestsDir := filepath.Join(*outDir, "manifests")
	if err := os.MkdirAll(manifestsDir, 0o755); err != nil {
		return err
	}
	csvPath := filepath.Join(manifestsDir, "devices.csv")
	deviceIDsPath := filepath.Join(manifestsDir, "device_ids.txt")
	enrollResultsPath := filepath.Join(manifestsDir, "factory-enroll-results.jsonl")
	if err := os.WriteFile(csvPath, []byte("device_id,device_type,mqtt_capability,service_options,model,certificate_path,key_path,bundle_path\n"), 0o644); err != nil {
		return err
	}
	_ = os.WriteFile(deviceIDsPath, nil, 0o644)
	_ = os.WriteFile(enrollResultsPath, nil, 0o644)
	type deviceTask struct {
		input loadDeviceInput
	}
	type deviceResult struct {
		device generatedDevice
		ok     bool
	}
	tasks := []deviceTask{}
	index := 1
	for _, dt := range loadDeviceTypes {
		n := alloc[dt.Name]
		if n == 0 {
			continue
		}
		logLoad("generating devices: type=%s count=%d", dt.Name, n)
		for ordinal := 1; ordinal <= n; ordinal++ {
			tasks = append(tasks, deviceTask{input: loadDeviceInput{
				Index:          index,
				Ordinal:        ordinal,
				Type:           dt,
				Prefix:         *prefix,
				OutDir:         *outDir,
				EnvRoot:        envRoot,
				Brandname:      *brandname,
				GenerateOnly:   *generateOnly,
				CAKey:          caKey,
				CACert:         caCert,
				DeviceDays:     *deviceValidDays,
				FactoryURL:     *factoryURL,
				FactoryAuthKey: *factoryAuthKey,
				ProductionJWT:  firstNonEmpty(factoryProductionJWTByType[dt.Name], factoryProductionJWT),
				ProductID:      factoryProfileIDByType[dt.Name],
				FactoryID:      *factoryID,
				LineID:         *lineID,
				StationID:      *stationID,
				FixtureID:      *fixtureID,
				OperatorID:     *operatorID,
				BatchID:        firstNonEmpty(factoryBatchIDByType[dt.Name], *batchID),
				SerialPrefix:   *serialPrefix,
				RunID:          *runID,
				Timeout:        time.Duration(*timeoutSeconds) * time.Second,
				ResultsPath:    enrollResultsPath,
				KeyAlgorithms:  deviceKeyAlgorithms,
			}})
			index++
		}
	}
	logLoad("device generation concurrency=%d", *concurrency)
	var progressMu sync.Mutex
	progressDone := 0
	progressGenerated := 0
	progressFailed := 0
	progress := func(ok bool) {
		progressMu.Lock()
		defer progressMu.Unlock()
		progressDone++
		if ok {
			progressGenerated++
		} else {
			progressFailed++
		}
		if shouldLogCountedProgress(progressDone, len(tasks)) {
			logLoad("device generation progress: done=%d/%d generated=%d failed=%d", progressDone, len(tasks), progressGenerated, progressFailed)
		}
	}
	deviceResults, err := boundedParallelMap(len(tasks), *concurrency, func(i int) (deviceResult, error) {
		device, ok, err := writeLoadDevice(tasks[i].input)
		if err == nil {
			progress(ok)
		}
		return deviceResult{device: device, ok: ok}, err
	})
	if err != nil {
		return err
	}
	devices := []generatedDevice{}
	deviceIDs := []string{}
	enrollSucceeded := 0
	enrollFailed := 0
	for _, result := range deviceResults {
		if ok := result.ok; ok {
			enrollSucceeded++
			devices = append(devices, result.device)
			deviceIDs = append(deviceIDs, result.device.DeviceID)
			appendCSV(csvPath, result.device)
			appendLine(deviceIDsPath, result.device.DeviceID)
		} else {
			enrollFailed++
		}
	}
	if err := writeJSON(filepath.Join(manifestsDir, "devices.json"), devices); err != nil {
		return err
	}
	profile := "mixed"
	if alloc["camera"] == *count {
		profile = "camera"
	} else if alloc["camera"] == 0 {
		profile = "iot"
	}
	iotMix := fmt.Sprintf("light=%d,air_conditioner=%d,smart_meter=%d", alloc["light"], alloc["air_conditioner"], alloc["smart_meter"])
	loadtestEnv := fmt.Sprintf(`# Source this file before e2e_test/video_cloud/load/scripts/run_video_loadtest.sh.
# It contains no bearer tokens; provide VIDEO_CLOUD_LOAD_*_TOKEN separately.
export VIDEO_CLOUD_LOAD_DEVICE_PREFIX='%s'
export VIDEO_CLOUD_LOAD_VIRTUAL_DEVICES=%d
export VIDEO_CLOUD_LOAD_DEVICE_IDS='%s'
export VIDEO_CLOUD_LOAD_MQTT_DEVICE_PROFILE='%s'
export VIDEO_CLOUD_LOAD_MQTT_IOT_MIX='%s'
export VIDEO_CLOUD_LOAD_DEVICE_MANIFEST='%s'
export VIDEO_CLOUD_LOAD_DEVICE_CERT_ROOT='%s'
`, shellQuote(*prefix), *count, strings.Join(deviceIDs, ","), shellQuote(profile), shellQuote(iotMix), shellQuote(filepath.Join(*outDir, "manifests", "devices.json")), shellQuote(*outDir))
	if err := os.WriteFile(filepath.Join(*outDir, "loadtest.env"), []byte(loadtestEnv), 0o644); err != nil {
		return err
	}
	summary := map[string]any{
		"generated_at":  time.Now().UTC().Format(time.RFC3339),
		"count":         *count,
		"prefix":        *prefix,
		"requested_mix": *mix,
		"allocated":     alloc,
		"enrollment":    map[string]any{"mode": mode, "factory_url": *factoryURL, "succeeded": enrollSucceeded, "failed": enrollFailed, "results": "manifests/factory-enroll-results.jsonl"},
		"paths":         map[string]any{"output_dir": *outDir, "loadtest_env": "loadtest.env", "device_ids": "manifests/device_ids.txt", "devices_csv": "manifests/devices.csv", "devices_json": "manifests/devices.json", "ca_cert": "ca/sim-device-ca.cert.pem"},
	}
	if err := writeJSON(filepath.Join(*outDir, "summary.json"), summary); err != nil {
		return err
	}
	store, err := openTestDataStore(envRoot, *brandname)
	if err != nil {
		return err
	}
	credentials := map[string]testDataDeviceCredential{}
	for _, device := range devices {
		credentials[device.DeviceID] = testDataCredentialFromOutputDir(*outDir, device)
	}
	if err := store.ReplaceDevices(*brandname, *runID, devices, credentials); err != nil {
		_ = store.Close()
		return err
	}
	_ = store.Close()
	if err := cleanupGeneratedDeviceSmallFiles(*outDir); err != nil {
		return err
	}
	if err := writeLoadDeviceReadme(*outDir, *count, *mix, mode, *factoryURL, *caValidDays, *deviceValidDays); err != nil {
		return err
	}
	if enrollFailed > 0 {
		logLoad("complete with failures: requested=%d succeeded=%d failed=%d results=%s", *count, enrollSucceeded, enrollFailed, enrollResultsPath)
		fmt.Fprintf(os.Stdout, "output=%s\nsummary=%s\nenroll_results=%s\nopenssl_log=%s\n", *outDir, filepath.Join(*outDir, "summary.json"), enrollResultsPath, opensslLog)
		return exitCode(1)
	}
	logLoad("complete: requested=%d succeeded=%d failed=%d", *count, enrollSucceeded, enrollFailed)
	fmt.Fprintf(os.Stdout, "output=%s\nsummary=%s\ntest_data_db=%s\nloadtest_env=%s\nopenssl_log=%s\n", *outDir, filepath.Join(*outDir, "summary.json"), testDataDBPath(envRoot, *brandname), filepath.Join(*outDir, "loadtest.env"), opensslLog)
	return nil
}

type accountManagerContext struct {
	EnvRoot          string
	StackValues      map[string]string
	BaseURL          string
	AdminEmail       string
	AdminPassword    string
	Host             string
	SSHUser          string
	SSHKey           string
	PlatformAdminEnv string
	cleanup          func()
}

func (ctx accountManagerContext) Close() {
	if ctx.cleanup != nil {
		ctx.cleanup()
	}
}

func runListBrandnameClouds(args []string) error {
	fs := flag.NewFlagSet("list-brandname-clouds", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	fs.StringVar(envRootFlag, "secrets-root", "", "deprecated env root")
	brandname := fs.String("brandname", "", "brand name")
	limit := fs.Int("limit", 200, "limit")
	jsonOutput := fs.Bool("json", false, "json output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging")
	}
	if *limit <= 0 {
		return errors.New("--limit must be a positive integer")
	}
	ctx, err := accountManagerContextFromFlags(*workspaceFlag, *envRootFlag)
	if err != nil {
		return err
	}
	logBrandList("start staging brand cloud list")
	logBrandList("workspace=%s", mustWorkspace(*workspaceFlag))
	logBrandList("env_root=%s", ctx.EnvRoot)
	logBrandList("loading Account Manager staging env/state")
	token, err := accountLogin(ctx, logBrandList)
	if err != nil {
		return err
	}
	payload, err := accountListBrandClouds(ctx, token, *limit)
	if err != nil {
		return err
	}
	if *brandname != "" {
		filtered := []any{}
		for _, item := range anySlice(payload["brand_clouds"]) {
			obj, _ := item.(map[string]any)
			metadata, _ := obj["metadata"].(map[string]any)
			if obj["name"] == *brandname || metadata["brandname"] == *brandname {
				filtered = append(filtered, item)
			}
		}
		payload["brand_clouds"] = filtered
		pagination, _ := payload["pagination"].(map[string]any)
		if pagination == nil {
			pagination = map[string]any{}
			payload["pagination"] = pagination
		}
		pagination["filtered_total"] = len(filtered)
	}
	if *jsonOutput {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(payload)
	}
	clouds := anySlice(payload["brand_clouds"])
	total := len(clouds)
	if pagination, ok := payload["pagination"].(map[string]any); ok {
		if v, ok := pagination["total"]; ok {
			total = int(asFloat(v))
		}
	}
	if *brandname != "" {
		fmt.Fprintf(os.Stdout, "brand_clouds=%d api_total=%d filter=%s\n", len(clouds), total, *brandname)
	} else {
		fmt.Fprintf(os.Stdout, "brand_clouds=%d api_total=%d\n", len(clouds), total)
	}
	fmt.Fprintf(os.Stdout, "%-36s  %-24s  %-10s  %-12s  %-5s  %-16s  %-24s  %s\n", "id", "name", "status", "tier", "quota", "metadata.brandname", "created_at", "metadata")
	for _, item := range clouds {
		obj, _ := item.(map[string]any)
		metadata, _ := obj["metadata"].(map[string]any)
		metaJSON, _ := json.Marshal(metadata)
		fmt.Fprintf(os.Stdout, "%-36s  %-24s  %-10s  %-12s  %-5s  %-16s  %-24s  %s\n",
			stringValue(obj["id"]), stringValue(obj["name"]), stringValue(obj["status"]), stringValue(obj["tier"]),
			fmt.Sprintf("%.0f", asFloat(obj["evaluation_device_quota"])), stringValue(metadata["brandname"]), stringValue(obj["created_at"]), string(metaJSON))
	}
	logBrandList("complete: listed brand clouds")
	return nil
}

func runCreateBrandnameCloud(args []string) error {
	fs := flag.NewFlagSet("create-brandname-cloud", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	fs.StringVar(envRootFlag, "secrets-root", "", "deprecated env root")
	brandname := fs.String("brandname", "", "brand name")
	skipBootstrap := fs.Bool("skip-bootstrap", false, "skip bootstrap")
	if err := fs.Parse(args); err != nil {
		return err
	}
	*brandname = strings.TrimSpace(*brandname)
	if *brandname == "" {
		return errors.New("--brandname is required")
	}
	if strings.ContainsFunc(*brandname, func(r rune) bool { return r < 32 || r == 127 }) {
		return errors.New("--brandname must not contain control characters")
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging")
	}
	ctx, err := accountManagerContextFromFlags(*workspaceFlag, *envRootFlag)
	if err != nil {
		return err
	}
	logBrandCreate("start staging brand cloud create: brandname=%s", *brandname)
	logBrandCreate("workspace=%s", mustWorkspace(*workspaceFlag))
	logBrandCreate("loading Account Manager staging env/state")
	if !*skipBootstrap {
		if err := accountBootstrap(ctx); err != nil {
			return err
		}
	}
	token, err := accountLogin(ctx, logBrandCreate)
	if err != nil {
		return err
	}
	ownerUserID, err := accountCurrentUserID(ctx, token)
	if err != nil {
		return err
	}
	list, err := accountListBrandClouds(ctx, token, 200)
	if err != nil {
		return err
	}
	for _, item := range anySlice(list["brand_clouds"]) {
		obj, _ := item.(map[string]any)
		if obj["name"] == *brandname {
			logBrandCreate("brand cloud already exists: id=%s", stringValue(obj["id"]))
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"action": "exists", "brand_cloud": obj})
		}
	}
	created, status, err := accountCreateBrandCloud(ctx, token, ownerUserID, *brandname)
	if err != nil {
		return err
	}
	if status == 201 {
		logBrandCreate("brand cloud created via API")
		if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"action": "created", "brand_cloud": created["brand_cloud"]}); err != nil {
			return err
		}
		logBrandCreate("complete: brand cloud created")
		return nil
	}
	if status != 500 {
		return fmt.Errorf("brand cloud create failed: HTTP %d", status)
	}
	logBrandCreate("API create returned HTTP 500; falling back to direct PostgreSQL upsert")
	fallback, err := accountPostgresFallback(ctx, *brandname)
	if err != nil {
		return err
	}
	var fallbackObj map[string]any
	if err := json.Unmarshal([]byte(fallback), &fallbackObj); err != nil {
		return err
	}
	verify, err := accountListBrandClouds(ctx, token, 200)
	if err != nil {
		return err
	}
	id := ""
	if bc, ok := fallbackObj["brand_cloud"].(map[string]any); ok {
		id = stringValue(bc["id"])
	}
	found := false
	for _, item := range anySlice(verify["brand_clouds"]) {
		obj, _ := item.(map[string]any)
		if stringValue(obj["id"]) == id {
			found = true
		}
	}
	if !found {
		return fmt.Errorf("post-create brand cloud verification did not find %s", id)
	}
	fmt.Fprintln(os.Stdout, strings.TrimSpace(fallback))
	logBrandCreate("complete: brand cloud created through fallback")
	return nil
}

func runCreateUsers(args []string) error {
	fs := flag.NewFlagSet("create-users", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	brandname := fs.String("brandname", "", "brand name")
	userEmailPrefix := fs.String("user-email-prefix", "", "optional run-scoped email prefix")
	userEmailDomain := fs.String("user-email-domain", "users.local", "test-only email domain")
	count := fs.Int("count", 10, "count")
	role := fs.String("role", "member", "role")
	rotatePassword := fs.Bool("rotate-password", false, "rotate password")
	reuseLocalUsers := fs.Bool("reuse-local-users", true, "reuse complete local user artifacts")
	noReuseLocalUsers := fs.Bool("no-reuse-local-users", false, "do not reuse complete local user artifacts")
	concurrency := fs.Int("concurrency", envInt("CLOUD_CREATE_USERS_CONCURRENCY", 64), "user creation concurrency")
	dryRun := fs.Bool("dry-run", false, "dry run")
	skipBootstrap := fs.Bool("skip-bootstrap", false, "skip bootstrap")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *noReuseLocalUsers {
		*reuseLocalUsers = false
	}
	*brandname = strings.TrimSpace(*brandname)
	if *brandname == "" {
		return errors.New("--brandname is required")
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging")
	}
	if *count <= 0 {
		return errors.New("--count must be greater than zero")
	}
	if *role != "owner" && *role != "admin" && *role != "member" {
		return errors.New("--role must be owner, admin, or member")
	}
	if *role == "owner" {
		return errors.New("bulk owner creation is disabled; use load-owner-activation so the owner completes email verification")
	}
	if *concurrency <= 0 {
		return errors.New("--concurrency must be greater than zero")
	}
	ctx, err := accountManagerContextFromFlags(*workspaceFlag, *envRootFlag)
	if err != nil {
		return err
	}
	logCreateUsers("workspace=%s", mustWorkspace(*workspaceFlag))
	logCreateUsers("env_root=%s", ctx.EnvRoot)
	logCreateUsers("loading Account Manager env/state")
	if !*skipBootstrap {
		if err := accountBootstrap(ctx); err != nil {
			return err
		}
	}
	session, err := accountLoginSession(ctx, logCreateUsers)
	if err != nil {
		return err
	}
	brandCloud, err := accountFindBrandCloud(ctx, session.AccessToken, *brandname)
	if err != nil {
		return err
	}
	brandCloudID := stringValue(brandCloud["id"])
	tenantSlug := stringValue(brandCloud["tenant_slug"])
	if tenantSlug == "" {
		return fmt.Errorf("brand cloud response missing tenant_slug for %s", *brandname)
	}
	logCreateUsers("brand cloud found: id=%s", brandCloudID)
	slug := brandSlug(*brandname)
	planned := plannedUsersWithPrefixAndDomain(*brandname, slug, *role, *count, *userEmailPrefix, *userEmailDomain)
	if *dryRun {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"action": "dry_run", "brand_cloud": brandCloud, "role": *role, "users": planned})
	}
	existingAppCredentials := loadExistingUserAppCredentials(ctx.EnvRoot, slug)
	reusableLocalUsers := map[string]map[string]any{}
	if *reuseLocalUsers && !*rotatePassword {
		reusableLocalUsers = loadReusableLocalUsers(ctx.EnvRoot, slug)
	}
	type createUserResult struct {
		user     map[string]any
		created  bool
		assigned bool
		reused   bool
	}
	var sessionMu sync.Mutex
	var logMu sync.Mutex
	safeLog := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logCreateUsers(format, args...)
	}
	safeAccountCreateUser := func(email, displayName, password string) (accountCreateUserResult, error) {
		return accountCreateUserWithSessionLock(ctx, &session, &sessionMu, safeLog, brandCloudID, email, displayName, password, *role, *rotatePassword)
	}
	var progressMu sync.Mutex
	progressDone := 0
	progressCreated := 0
	progressAssigned := 0
	progress := func(created, assigned int) {
		progressMu.Lock()
		defer progressMu.Unlock()
		progressDone++
		progressCreated += created
		progressAssigned += assigned
		if shouldLogCountedProgress(progressDone, len(planned)) {
			safeLog("user creation progress: done=%d/%d created=%d assigned=%d app_certificates=%d", progressDone, len(planned), progressCreated, progressAssigned, progressDone)
		}
	}
	safeLog("user creation concurrency=%d", *concurrency)
	results, err := boundedParallelMap(len(planned), *concurrency, func(i int) (createUserResult, error) {
		plan := planned[i]
		email := plan["email"].(string)
		displayName := plan["display_name"].(string)
		password, err := randomPassword()
		if err != nil {
			return createUserResult{}, err
		}
		safeLog("ensuring global user membership: email=%s role=%s", email, *role)
		if reusable := reusableLocalUsers[email]; reusable != nil {
			safeLog("reusing local user artifact: email=%s", email)
			reusable["action"] = "reused"
			return createUserResult{user: reusable, reused: true}, nil
		}
		createResult, err := safeAccountCreateUser(email, displayName, password)
		if err != nil {
			return createUserResult{}, err
		}
		result := createUserResult{}
		if createResult.Action == "created" {
			result.created = true
		} else {
			if !*rotatePassword {
				return createUserResult{}, fmt.Errorf("global user already exists and password was not rotated: email=%s; use the previous credentials artifact or rerun with --rotate-password", email)
			}
			result.assigned = true
		}
		if createResult.UserID == "" {
			return createUserResult{}, fmt.Errorf("global user create response missing user.id for %s", email)
		}
		appSubject := "app-user:" + createResult.UserID
		safeLog("bootstrapping app certificate: email=%s", email)
		// Always inspect the current server-side certificate before generating a
		// CSR. Account Manager reuses a valid certificate when a caller supplies
		// another CSR, so the direct-CSR fast path can pair that certificate with
		// a newly generated, unrelated private key on repeat staging runs.
		appCredentials, appCertificate, userSession, err := accountEnsureUserAppCertificate(ctx, tenantSlug, email, password, appSubject, false, existingAppCredentials[email], nil)
		if err != nil {
			return createUserResult{}, err
		}
		effectiveRole := firstNonEmpty(createResult.Role, *role)
		result.user = map[string]any{
			"email":           email,
			"display_name":    displayName,
			"role":            effectiveRole,
			"id":              createResult.UserID,
			"user_id":         createResult.UserID,
			"password":        password,
			"action":          createResult.Action,
			"app_credentials": appCredentials,
			"app_certificate": appCertificate,
			"tokens":          userSession,
		}
		createdDelta := 0
		if result.created {
			createdDelta = 1
		}
		assignedDelta := 0
		if result.assigned {
			assignedDelta = 1
		}
		progress(createdDelta, assignedDelta)
		return result, nil
	})
	if err != nil {
		return err
	}
	users := []map[string]any{}
	created := 0
	assigned := 0
	reused := 0
	for _, result := range results {
		if result.created {
			created++
		}
		if result.assigned {
			assigned++
		}
		if result.reused {
			reused++
		}
		users = append(users, result.user)
	}
	store, err := openTestDataStore(ctx.EnvRoot, *brandname)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.ReplaceUsers(*brandname, brandCloudID, tenantSlug, *role, users); err != nil {
		return err
	}
	logCreateUsers("credentials written to SQLite: %s", store.Path)
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"action":           "created",
		"brandname":        *brandname,
		"brand_cloud_id":   brandCloudID,
		"tenant_slug":      tenantSlug,
		"role":             *role,
		"count":            *count,
		"created":          created,
		"assigned":         assigned,
		"reused":           reused,
		"app_certificates": *count,
		"test_data_db":     store.Path,
	})
}

type e2eStep struct {
	Name            string `json:"name"`
	Status          string `json:"status"`
	ExitCode        int    `json:"exit_code"`
	DurationSeconds int64  `json:"duration_seconds"`
	LogFile         string `json:"log_file"`
}

type e2eDataSetupSummary struct {
	Overall           string    `json:"overall"`
	SummaryFile       string    `json:"summary_file"`
	UsersFile         string    `json:"users_file,omitempty"`
	DeviceBindFile    string    `json:"device_bind_file,omitempty"`
	TestDataDB        string    `json:"test_data_db"`
	BindValidationDir string    `json:"bind_validation_dir"`
	Steps             []e2eStep `json:"steps"`
}

type stagingE2EMultiBrandConfig struct {
	Workspace         string
	EnvRoot           string
	BrandPlanFile     string
	DeviceMix         string
	DevicePrefix      string
	UserConcurrency   int
	DeviceConcurrency int
	BindConcurrency   int
	OutDir            string
	Quiet             bool
	Resume            bool
	NoResume          bool
	FromStep          string
	PlanMode          bool
	Scripts           map[string]string
	RunID             string
	LoadTarget        string
	EmailOwners       bool
	OperatorEnvFile   string
}

func runStagingE2EMultiBrandDataSetup(cfg stagingE2EMultiBrandConfig) error {
	started := time.Now().UTC()
	plan, err := loadLoadTestBrandPlan(cfg.BrandPlanFile)
	if err != nil {
		return err
	}
	if cfg.EmailOwners {
		var operator map[string]string
		var readErr error
		if rtkCloudTestMode() && strings.TrimSpace(cfg.OperatorEnvFile) != "" {
			operator, readErr = readEnvFile(cfg.OperatorEnvFile)
		} else {
			stackEnv, _ := readEnvFile(filepath.Join(cfg.EnvRoot, "env", "stack.env"))
			store, storeErr := newSecretStore("", firstNonEmpty(stackEnv["CLOUD_ENV_NAME"], "staging"))
			if storeErr != nil {
				return storeErr
			}
			operator, readErr = store.readOperator()
		}
		if readErr != nil {
			return fmt.Errorf("read canonical operator settings: %w", readErr)
		}
		mailbox := firstNonEmpty(os.Getenv("IMAP_EMAIL_ADDR"), operator["IMAP_EMAIL_ADDR"])
		plan, err = resolveLoadTestBrandPlan(plan, cfg.LoadTarget, cfg.RunID, mailbox)
		if err != nil {
			return err
		}
		cfg.RunID = plan.RunID
	}
	if cfg.PlanMode {
		fmt.Fprintf(os.Stdout, "multi_brand_plan: %s\n", cfg.BrandPlanFile)
		fmt.Fprintf(os.Stdout, "total_devices: %d\nnormal_users: %d\ndeveloper_users: %d\ndevices_per_user: %d\n", plan.TotalDevices, plan.normalUserCount(), plan.developerUserCount(), plan.DevicesPerUser)
		for _, brand := range plan.Brands {
			deviceMix := cfg.DeviceMix
			if len(brand.DeviceMix) > 0 {
				deviceMix = deviceMixString(brand.DeviceMix)
			}
			fmt.Fprintf(os.Stdout, "- brandname: %s normal_users=%d devices=%d owner=%d admin=%d device_mix=%s\n", brand.Brandname, brand.NormalUsers, brand.Devices, brand.DeveloperUsers["owner"], brand.DeveloperUsers["admin"], deviceMix)
		}
		return nil
	}
	if cfg.NoResume {
		cfg.Resume = false
	}
	if cfg.OutDir == "" {
		cfg.OutDir = filepath.Join(cfg.EnvRoot, "artifacts", "staging-e2e-data", time.Now().UTC().Format("20060102T150405Z"))
	}
	if err := os.MkdirAll(cfg.OutDir, 0o755); err != nil {
		return err
	}
	if cfg.EmailOwners {
		resolvedPath := filepath.Join(cfg.OutDir, "resolved-brand-plan.json")
		if err := writeJSON(resolvedPath, plan); err != nil {
			return err
		}
		cfg.BrandPlanFile = resolvedPath
	}
	logsDir := filepath.Join(cfg.OutDir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return err
	}
	steps := []e2eStep{}
	runStep := func(name string, argv ...string) error {
		step, err := runE2EStepWithOptions(name, filepath.Join(logsDir, name+".log"), e2eStepOptions{Quiet: cfg.Quiet}, argv...)
		steps = append(steps, step)
		return err
	}
	activatedOwners := 0
	for _, brand := range plan.Brands {
		brandSlug := brandSlug(brand.Brandname)
		if cfg.EmailOwners {
			steps = append(steps, e2eStep{Name: brandSlug + "_create_brand", Status: "SKIP", ExitCode: 0})
		} else {
			if err := runStep(brandSlug+"_create_brand", commandWithArgs(cfg.Scripts["create-brand"], "--workspace", cfg.Workspace, "--env-root", cfg.EnvRoot, "--brandname", brand.Brandname)...); err != nil {
				return err
			}
		}
		if cfg.EmailOwners {
			if brand.DeveloperUsers["owner"] != 1 {
				return fmt.Errorf("%s must define exactly one owner for email activation", brand.Brandname)
			}
			evidencePath := filepath.Join(cfg.OutDir, "owner-activation", strings.ToLower(brand.BrandKey)+".json")
			args := []string{
				"--workspace", cfg.Workspace, "--env-root", cfg.EnvRoot,
				"--brandname", brand.Brandname, "--email", brand.OwnerEmail,
				"--display-name", brand.OwnerName, "--run-id", cfg.RunID,
				"--evidence-path", evidencePath,
			}
			if rtkCloudTestMode() && strings.TrimSpace(cfg.OperatorEnvFile) != "" {
				args = append(args, "--operator-env-file", cfg.OperatorEnvFile)
			}
			if cfg.Resume {
				args = append(args, "--resume")
			}
			activationCommand := firstNonEmpty(cfg.Scripts["activate-owner"], selfCommandPath("activate-load-owner"))
			if err := runStep(brandSlug+"_activate_owner", commandWithArgs(activationCommand, args...)...); err != nil {
				return err
			}
			activatedOwners++
		}
		for _, role := range []string{"owner", "admin"} {
			if cfg.EmailOwners && role == "owner" {
				continue
			}
			count := brand.DeveloperUsers[role]
			if count <= 0 {
				continue
			}
			args := []string{"--workspace", cfg.Workspace, "--env-root", cfg.EnvRoot, "--brandname", brand.Brandname, "--count", strconv.Itoa(count), "--role", role, "--rotate-password", "--concurrency", strconv.Itoa(cfg.UserConcurrency)}
			if cfg.NoResume {
				args = append(args, "--no-reuse-local-users")
			}
			if err := runStep(brandSlug+"_create_"+role+"_users", commandWithArgs(cfg.Scripts["create-users"], args...)...); err != nil {
				return err
			}
		}
		deviceMix := cfg.DeviceMix
		if len(brand.DeviceMix) > 0 {
			deviceMix = deviceMixString(brand.DeviceMix)
		}
		devicePrefix := cfg.DevicePrefix + "-" + brandSlug
		if cfg.EmailOwners {
			devicePrefix, err = loadEmailDevicePrefix(cfg.RunID, brand.BrandKey)
			if err != nil {
				return err
			}
		}
		args := []string{"--workspace", cfg.Workspace, "--env-root", cfg.EnvRoot, "--brandname", brand.Brandname, "--user-count", strconv.Itoa(brand.NormalUsers), "--device-count", strconv.Itoa(brand.Devices), "--device-mix", deviceMix, "--device-prefix", devicePrefix, "--user-concurrency", strconv.Itoa(cfg.UserConcurrency), "--device-concurrency", strconv.Itoa(cfg.DeviceConcurrency), "--bind-concurrency", strconv.Itoa(cfg.BindConcurrency)}
		if cfg.EmailOwners {
			args = append(args, "--user-role", "member", "--user-email-prefix", brand.MemberPrefix, "--user-email-domain", "users.invalid")
		}
		if cfg.Resume {
			args = append(args, "--resume")
		} else {
			args = append(args, "--no-resume")
		}
		if cfg.FromStep != "" {
			args = append(args, "--from-step", cfg.FromStep)
		}
		args = append(args, "--out-dir", filepath.Join(cfg.OutDir, brandSlug))
		if cfg.Quiet {
			args = append(args, "--quiet")
		}
		setupCommand := firstNonEmpty(cfg.Scripts["setup-brand"], selfCommandPath("staging-e2e-data-setup"))
		if err := runStep(brandSlug+"_member_devices_bind_validate", commandWithArgs(setupCommand, args...)...); err != nil {
			return err
		}
	}
	overall := "pass"
	for _, step := range steps {
		if step.Status != "PASS" && step.Status != "SKIP" && step.Status != "RETRY" {
			overall = "fail"
		}
	}
	summaryFile := filepath.Join(cfg.OutDir, "summary.json")
	summary := map[string]any{
		"overall":           overall,
		"generated_at":      time.Now().UTC().Format(time.RFC3339),
		"env_root":          cfg.EnvRoot,
		"brand_plan_file":   cfg.BrandPlanFile,
		"brand_count":       len(plan.Brands),
		"normal_users":      plan.normalUserCount(),
		"developer_users":   plan.developerUserCount(),
		"activated_owners":  activatedOwners,
		"synthetic_members": plan.normalUserCount(),
		"devices":           plan.TotalDevices,
		"devices_per_user":  plan.DevicesPerUser,
		"summary_file":      summaryFile,
		"steps":             steps,
	}
	if err := writeJSON(summaryFile, summary); err != nil {
		return err
	}
	if err := json.NewEncoder(os.Stdout).Encode(summary); err != nil {
		return err
	}
	if overall != "pass" {
		return exitCode(1)
	}
	if cfg.RunID != "" && len(cfg.Scripts) == 0 {
		if err := writeBulkProvisioningWorkflowEvidence(cfg.OutDir, plan); err != nil {
			return err
		}
		if err := writeCaseFeatureEvidence(
			cfg.Workspace, cfg.OutDir, "E2E-PROV-BULK-001", cfg.RunID,
			"staging", "", started, time.Now().UTC(),
		); err != nil {
			return err
		}
	}
	if cfg.EmailOwners {
		if err := writeCaseFeatureEvidence(
			cfg.Workspace, cfg.OutDir, "E2E-LOAD-ACCOUNT-001", cfg.RunID,
			"staging", "", started, time.Now().UTC(),
		); err != nil {
			return err
		}
	}
	return nil
}

func writeBulkProvisioningWorkflowEvidence(outputDir string, plan loadTestBrandPlan) error {
	type provisioningResult struct {
		Overall      string `json:"overall"`
		Provisioning struct {
			Checked int `json:"checked"`
			Ready   int `json:"ready"`
			Pending int `json:"pending"`
			Failed  int `json:"failed"`
		} `json:"provisioning"`
	}
	totalChecked := 0
	for _, brand := range plan.Brands {
		path := filepath.Join(outputDir, brandSlug(brand.Brandname), "bind-validation", "bulk-device-bind-validation-results.json")
		var result provisioningResult
		if err := readJSONFile(path, &result); err != nil {
			return fmt.Errorf("read %s bulk provisioning evidence: %w", brand.Brandname, err)
		}
		if strings.ToLower(result.Overall) != "pass" || result.Provisioning.Checked != brand.Devices ||
			result.Provisioning.Ready != brand.Devices || result.Provisioning.Pending != 0 || result.Provisioning.Failed != 0 {
			return fmt.Errorf("%s bulk provisioning evidence is incomplete: checked=%d ready=%d pending=%d failed=%d expected=%d",
				brand.Brandname, result.Provisioning.Checked, result.Provisioning.Ready,
				result.Provisioning.Pending, result.Provisioning.Failed, brand.Devices)
		}
		totalChecked += result.Provisioning.Checked
	}
	if totalChecked != plan.TotalDevices {
		return fmt.Errorf("bulk provisioning evidence covers %d devices, want %d", totalChecked, plan.TotalDevices)
	}
	return writeJSON(filepath.Join(outputDir, "bulk-provisioning-workflow.json"), map[string]any{
		"schema_version": "rtk-live-workflow-evidence/v1",
		"workflow": map[string]any{
			"workflow_id": "WF-PROV-BULK-001",
			"steps": map[string]string{
				"provision_registry_device": "PASS",
				"wait_for_provisioning":     "PASS",
			},
			"assertions": map[string]map[string]string{
				"provision_registry_device": {
					"all_devices_have_provision_operation_id": "PASS",
					"per_device_identity_preserved":           "PASS",
				},
				"wait_for_provisioning": {
					"all_devices_reached_ready":  "PASS",
					"no_pending_or_failed_items": "PASS",
				},
			},
		},
	})
}

func loadEmailDevicePrefix(runID, brandKey string) (string, error) {
	runID = strings.ToLower(strings.TrimSpace(runID))
	brandKey = strings.ToLower(strings.TrimSpace(brandKey))
	if !loadRunIDPattern.MatchString(runID) || !loadBrandKeyPattern.MatchString(brandKey) {
		return "", errors.New("run-scoped device prefix requires a safe run ID and Brand key B<nn>")
	}
	prefix := "load-" + runID + "-" + brandKey
	// generate-load-devices appends "-0001"; OpenBao rejects a DNS label
	// longer than 63 bytes even when hostname enforcement is disabled.
	if len(prefix)+len("-0001") > 63 {
		return "", fmt.Errorf("run-scoped device prefix exceeds the OpenBao 63-byte label limit: %s", prefix)
	}
	return prefix, nil
}

func deviceMixString(mix map[string]int) string {
	keys := make([]string, 0, len(mix))
	for key := range mix {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, fmt.Sprintf("%s=%d", key, mix[key]))
	}
	return strings.Join(parts, ",")
}

func runStagingE2EDataSetup(args []string) error {
	fs := flag.NewFlagSet("staging-e2e-data-setup", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	planMode := fs.Bool("plan", false, "plan")
	brandname := fs.String("brandname", "RTK", "brand name")
	brandPlanFile := fs.String("brand-plan", "", "multi-brand load-test plan JSON")
	loadRunID := fs.String("load-run-id", "", "run ID for run-scoped Brand Cloud/account names")
	loadTarget := fs.String("load-target", "", "load target: 1K, 50K, 100K, or CANARY")
	emailActivateOwners := fs.Bool("email-activate-owners", false, "activate one formal owner per Brand through Send Mail and local IMAP")
	operatorEnvFile := fs.String("operator-env-file", defaultDeploymentSharedCredentialFile(), "operator credential profile containing IMAP settings")
	userCount := fs.Int("user-count", 10, "user count")
	deviceCount := fs.Int("device-count", 100, "device count")
	deviceMix := fs.String("device-mix", "camera=40,light=25,air_conditioner=20,smart_meter=15", "device mix")
	devicePrefix := fs.String("device-prefix", "load-device", "device prefix")
	userEmailPrefix := fs.String("user-email-prefix", "", "optional run-scoped user email prefix")
	userEmailDomain := fs.String("user-email-domain", "users.local", "test-only user email domain")
	userRole := fs.String("user-role", firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_USER_ROLE"), "admin"), "role for run-scoped staging users: owner, admin, or member")
	userConcurrency := fs.Int("user-concurrency", envInt("CLOUD_STAGING_E2E_USER_CONCURRENCY", 64), "user creation concurrency")
	deviceConcurrency := fs.Int("device-concurrency", envInt("CLOUD_STAGING_E2E_DEVICE_CONCURRENCY", 64), "device generation concurrency")
	bindConcurrency := fs.Int("bind-concurrency", envInt("CLOUD_STAGING_E2E_BIND_CONCURRENCY", 64), "device bind concurrency")
	outDir := fs.String("out-dir", "", "out dir")
	quiet := fs.Bool("quiet", false, "suppress periodic progress output")
	resume := fs.Bool("resume", true, "reuse completed artifacts for matching steps")
	noResume := fs.Bool("no-resume", false, "recreate artifacts even when matching completed artifacts exist")
	fromStep := fs.String("from-step", "", "start from step: create_brand, create_users, create_devices, bind_devices, or validate_bind")
	usersFileFlag := fs.String("users-file", "", "existing users artifact for bind/validate resume")
	bindArtifactFlag := fs.String("bind-artifact", "", "existing bind artifact for validate resume")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required")
	}
	if *userCount <= 0 {
		return errors.New("--user-count must be a positive integer")
	}
	if *userRole != "owner" && *userRole != "admin" && *userRole != "member" {
		return errors.New("--user-role must be owner, admin, or member")
	}
	if *deviceCount <= 0 {
		return errors.New("--device-count must be a positive integer")
	}
	if *userConcurrency <= 0 || *deviceConcurrency <= 0 || *bindConcurrency <= 0 {
		return errors.New("--user-concurrency, --device-concurrency, and --bind-concurrency must be positive integers")
	}
	if *fromStep != "" && e2eStepIndex(*fromStep) < 0 {
		return fmt.Errorf("--from-step must be one of: %s", strings.Join(e2eStepOrder(), ", "))
	}
	if *noResume {
		*resume = false
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	scripts := map[string]string{
		"create-brand":     firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_CREATE_BRAND_SCRIPT"), selfCommandPath("create-brandname-cloud")),
		"create-users":     firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_CREATE_USERS_SCRIPT"), selfCommandPath("create-users")),
		"activate-owner":   firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_ACTIVATE_OWNER_SCRIPT"), selfCommandPath("activate-load-owner")),
		"setup-brand":      firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_SETUP_BRAND_SCRIPT"), selfCommandPath("staging-e2e-data-setup")),
		"generate-devices": firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_GENERATE_DEVICES_SCRIPT"), selfCommandPath("generate-load-devices")),
		"bind-devices":     firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_BIND_DEVICES_SCRIPT"), selfCommandPath("bind-devices")),
		"validate-bind":    firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_VALIDATE_BIND_SCRIPT"), selfCommandPath("validate-device-bind")),
	}
	if strings.TrimSpace(*brandPlanFile) != "" {
		return runStagingE2EMultiBrandDataSetup(stagingE2EMultiBrandConfig{
			Workspace:         workspace,
			EnvRoot:           envRoot,
			BrandPlanFile:     *brandPlanFile,
			DeviceMix:         *deviceMix,
			DevicePrefix:      *devicePrefix,
			UserConcurrency:   *userConcurrency,
			DeviceConcurrency: *deviceConcurrency,
			BindConcurrency:   *bindConcurrency,
			OutDir:            *outDir,
			Quiet:             *quiet,
			Resume:            *resume,
			NoResume:          *noResume,
			FromStep:          *fromStep,
			PlanMode:          *planMode,
			Scripts:           scripts,
			RunID:             *loadRunID,
			LoadTarget:        *loadTarget,
			EmailOwners:       *emailActivateOwners,
			OperatorEnvFile:   *operatorEnvFile,
		})
	}
	if *planMode {
		printE2EDataSetupPlan(workspace, envRoot, *brandname, *userCount, *deviceCount, *deviceMix, *userConcurrency, *deviceConcurrency, *bindConcurrency, scripts)
		return nil
	}
	if *outDir == "" {
		*outDir = filepath.Join(envRoot, "artifacts", "staging-e2e-data", time.Now().UTC().Format("20060102T150405Z"))
	}
	logsDir := filepath.Join(*outDir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return err
	}
	steps := []e2eStep{}
	childEnv, cleanup, err := startK8SE2EDataSetupPortForwardsIfNeeded(workspace, envRoot)
	if err != nil {
		return err
	}
	defer cleanup()
	runStep := func(name string, argv ...string) error {
		step, err := runE2EStepWithOptions(name, filepath.Join(logsDir, name+".log"), e2eStepOptions{Quiet: *quiet, Env: childEnv}, argv...)
		steps = append(steps, step)
		return err
	}
	runStepWithEnv := func(name string, env []string, argv ...string) error {
		step, err := runE2EStepWithOptions(name, filepath.Join(logsDir, name+".log"), e2eStepOptions{Quiet: *quiet, Env: env}, argv...)
		steps = append(steps, step)
		return err
	}
	skipStep := func(name, reason string) {
		fmt.Fprintf(os.Stderr, "[cloud-staging-e2e] skip: %s reason=%q\n", name, reason)
		steps = append(steps, e2eStep{Name: name, Status: "SKIP", ExitCode: 0, DurationSeconds: 0, LogFile: ""})
	}
	shouldRunStep := func(name string) bool {
		return shouldRunE2EStep(name, *fromStep)
	}
	if shouldRunStep("create_brand") {
		args := []string{"--workspace", workspace, "--env-root", envRoot, "--brandname", *brandname}
		if boolishEnv("CLOUD_STAGING_E2E_SKIP_BOOTSTRAP") {
			args = append(args, "--skip-bootstrap")
		}
		if err := runStep("create_brand", commandWithArgs(scripts["create-brand"], args...)...); err != nil {
			return err
		}
	} else {
		skipStep("create_brand", "--from-step")
	}
	slug := brandSlug(*brandname)
	_ = slug
	testDataDB := testDataDBPath(envRoot, *brandname)
	usersFile := *usersFileFlag
	if usersFile == "" {
		usersFile = testDataDB
	}
	coverage := testDataCoverageFor(envRoot, *brandname)
	if shouldRunStep("create_users") && !(*resume && coverage.Users == *userCount) {
		if !*resume {
			store, err := openTestDataStore(envRoot, *brandname)
			if err != nil {
				return err
			}
			if err := store.ClearUsers(*brandname); err != nil {
				_ = store.Close()
				return fmt.Errorf("clear stale local users for %s: %w", *brandname, err)
			}
			if err := store.Close(); err != nil {
				return err
			}
		}
		args := []string{"--workspace", workspace, "--env-root", envRoot, "--brandname", *brandname, "--count", strconv.Itoa(*userCount), "--role", *userRole, "--rotate-password", "--concurrency", strconv.Itoa(*userConcurrency)}
		if strings.TrimSpace(*userEmailPrefix) != "" {
			args = append(args, "--user-email-prefix", *userEmailPrefix)
		}
		args = append(args, "--user-email-domain", *userEmailDomain)
		if !*resume {
			args = append(args, "--no-reuse-local-users")
		}
		if boolishEnv("CLOUD_STAGING_E2E_SKIP_BOOTSTRAP") {
			args = append(args, "--skip-bootstrap")
		}
		if err := runStep("create_users", commandWithArgs(scripts["create-users"], args...)...); err != nil {
			return err
		}
		usersFile = testDataDB
		coverage = testDataCoverageFor(envRoot, *brandname)
	} else {
		reason := "--from-step"
		if shouldRunStep("create_users") {
			reason = fmt.Sprintf("--resume SQLite users count=%d", coverage.Users)
		}
		skipStep("create_users", reason)
	}
	deviceSetupRequired := shouldRunStep("create_devices") && !(*resume && testDataDeviceMatchesSetup(envRoot, *brandname, *deviceCount, *deviceMix))
	managedFactoryGenerator := strings.TrimSpace(os.Getenv("CLOUD_STAGING_E2E_GENERATE_DEVICES_SCRIPT")) == ""
	providedProductionJWT := strings.TrimSpace(os.Getenv("FACTORY_ENROLL_PRODUCTION_JWT"))
	factoryEnv := append([]string(nil), childEnv...)
	if deviceSetupRequired && managedFactoryGenerator {
		factoryRunID := firstNonEmpty(os.Getenv("RUNTIME_COVERAGE_RUN_ID"), *loadRunID, time.Now().UTC().Format("20060102T150405Z")+"-factory")
		var credentialEnv []string
		var step e2eStep
		var err error
		if providedProductionJWT != "" {
			credentialEnv, step, err = useProvidedFactoryProductionCredential(logsDir, factoryRunID, providedProductionJWT)
		} else {
			credentialEnv, step, err = prepareFactoryProductionBundleStep(workspace, envRoot, *outDir, logsDir, *brandname, factoryRunID, *deviceCount, *deviceMix, time.Now().UTC(), prepareFactoryProductionCredentials)
		}
		steps = append(steps, step)
		if err != nil {
			return err
		}
		factoryEnv = append(factoryEnv, credentialEnv...)
	} else {
		reason := "device generation not selected or resumed"
		if deviceSetupRequired && !managedFactoryGenerator {
			reason = "external device generator owns its factory credential contract"
		}
		skipStep("prepare_factory_production", reason)
	}
	if deviceSetupRequired {
		if err := runStepWithEnv("create_devices", factoryEnv, commandWithArgs(scripts["generate-devices"], "--workspace", workspace, "--env-root", envRoot, "--brandname", *brandname, "--count", strconv.Itoa(*deviceCount), "--mix", *deviceMix, "--prefix", *devicePrefix, "--force", "--concurrency", strconv.Itoa(*deviceConcurrency))...); err != nil {
			return err
		}
		coverage = testDataCoverageFor(envRoot, *brandname)
	} else {
		reason := "--from-step"
		if shouldRunStep("create_devices") {
			reason = fmt.Sprintf("--resume SQLite device count=%d mix=%s", coverage.Devices, *deviceMix)
		}
		skipStep("create_devices", reason)
	}
	if usersFile == "" {
		return fmt.Errorf("no users test-data DB found for brand %s", *brandname)
	}
	bindFile := *bindArtifactFlag
	if bindFile == "" {
		bindFile = testDataDB
	}
	bindSkippedForResume := false
	runBindStep := func() error {
		args := []string{"--workspace", workspace, "--env-root", envRoot, "--brandname", *brandname, "--count", strconv.Itoa(*deviceCount), "--concurrency", strconv.Itoa(*bindConcurrency)}
		if boolishEnv("CLOUD_STAGING_E2E_SKIP_BOOTSTRAP") {
			args = append(args, "--skip-bootstrap")
		}
		if err := runStepWithEnv("bind_devices", factoryEnv, commandWithArgs(scripts["bind-devices"], args...)...); err != nil {
			return err
		}
		bindFile = testDataDB
		coverage = testDataCoverageFor(envRoot, *brandname)
		return nil
	}
	if shouldRunStep("bind_devices") && !(*resume && testDataBindMatchesSetup(envRoot, *brandname, *userCount, *deviceCount, *deviceMix)) {
		if err := runBindStep(); err != nil {
			return err
		}
	} else {
		reason := "--from-step"
		if shouldRunStep("bind_devices") {
			reason = fmt.Sprintf("--resume SQLite bindings=%d users=%d mix=%s", coverage.Bindings, coverage.Users, *deviceMix)
			bindSkippedForResume = true
		}
		skipStep("bind_devices", reason)
	}
	if bindFile == "" {
		return fmt.Errorf("no device-bind test-data DB found for brand %s", *brandname)
	}
	expectedPerUser := (*deviceCount + *userCount - 1) / *userCount
	bindValidationDir := filepath.Join(*outDir, "bind-validation")
	if shouldRunStep("validate_bind") {
		runValidateStep := func() error {
			return runStep("validate_bind", commandWithArgs(scripts["validate-bind"], "--workspace", workspace, "--env-root", envRoot, "--brandname", *brandname, "--out-dir", bindValidationDir, "--expected-count", strconv.Itoa(*deviceCount), "--expected-devices-per-user", strconv.Itoa(expectedPerUser), "--wait-provisioned-timeout", firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_BIND_PROVISION_TIMEOUT"), "10m"), "--wait-provisioned-poll", firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_BIND_PROVISION_POLL"), "10s"), "--wait-provisioned-concurrency", strconv.Itoa(*bindConcurrency))...)
		}
		if err := runValidateStep(); err != nil {
			if bindSkippedForResume && shouldRunStep("bind_devices") && validationFailureCategoryCount(bindValidationDir, "already_bound_not_ready") > 0 {
				fmt.Fprintf(os.Stderr, "[cloud-staging-e2e] repair: validate_bind failure_category=%q; rerunning bind_devices\n", "already_bound_not_ready")
				if len(steps) > 0 && steps[len(steps)-1].Name == "validate_bind" {
					steps[len(steps)-1].Status = "RETRY"
					steps[len(steps)-1].ExitCode = 0
				}
				if err := runBindStep(); err != nil {
					return err
				}
				if bindFile == "" {
					return fmt.Errorf("no device-bind artifact found after bind repair for brand slug %s", slug)
				}
				if err := runValidateStep(); err != nil {
					return err
				}
			} else {
				return err
			}
		}
	} else {
		skipStep("validate_bind", "--from-step")
	}
	overall := "pass"
	for _, step := range steps {
		if step.Status != "PASS" && step.Status != "SKIP" && step.Status != "RETRY" {
			overall = "fail"
		}
	}
	summaryFile := filepath.Join(*outDir, "summary.json")
	summary := e2eDataSetupSummary{
		Overall:           overall,
		SummaryFile:       summaryFile,
		TestDataDB:        testDataDB,
		BindValidationDir: bindValidationDir,
		Steps:             steps,
	}
	if err := writeJSON(summaryFile, map[string]any{
		"overall":             summary.Overall,
		"generated_at":        time.Now().UTC().Format(time.RFC3339),
		"env_root":            envRoot,
		"brandname":           *brandname,
		"summary_file":        summary.SummaryFile,
		"test_data_db":        summary.TestDataDB,
		"bind_validation_dir": summary.BindValidationDir,
		"artifacts":           map[string]any{"test_data_db": summary.TestDataDB, "bind_validation_dir": summary.BindValidationDir},
		"steps":               summary.Steps,
	}); err != nil {
		return err
	}
	if containsSensitiveReportTerms(readText(summaryFile)) {
		return errors.New("sanitized data setup summary contains sensitive terms")
	}
	if err := json.NewEncoder(os.Stdout).Encode(summary); err != nil {
		return err
	}
	if overall != "pass" {
		return exitCode(1)
	}
	return nil
}

func startK8SE2EDataSetupPortForwardsIfNeeded(workspace, envRoot string) ([]string, func(), error) {
	stackEnv, _ := readEnvFile(filepath.Join(envRoot, "env", "stack.env"))
	if firstNonEmpty(os.Getenv("CLOUD_PROVIDER"), stackEnv["CLOUD_PROVIDER"]) != "lke" {
		return nil, func() {}, nil
	}
	if os.Getenv("FACTORY_ENROLL_URL") != "" && os.Getenv("ACCOUNT_MANAGER_BASE_URL") != "" && os.Getenv("VIDEO_CLOUD_BASE_URL") != "" {
		return nil, func() {}, nil
	}
	return startK8SE2EPortForwardsForServices(workspace, envRoot, false)
}

func printE2EDataSetupPlan(workspace, envRoot, brandname string, userCount, deviceCount int, deviceMix string, userConcurrency, deviceConcurrency, bindConcurrency int, scripts map[string]string) {
	fmt.Fprintln(os.Stdout, "cloud-staging-e2e-data-setup plan")
	fmt.Fprintf(os.Stdout, "workspace: %s\n", workspace)
	fmt.Fprintf(os.Stdout, "env_root: %s\n", envRoot)
	fmt.Fprintf(os.Stdout, "brandname: %s\n", brandname)
	fmt.Fprintf(os.Stdout, "user_count: %d\n", userCount)
	fmt.Fprintf(os.Stdout, "device_count: %d\n", deviceCount)
	fmt.Fprintf(os.Stdout, "device_mix: %s\n", deviceMix)
	fmt.Fprintf(os.Stdout, "user_concurrency: %d\n", userConcurrency)
	fmt.Fprintf(os.Stdout, "device_concurrency: %d\n", deviceConcurrency)
	fmt.Fprintf(os.Stdout, "bind_concurrency: %d\n", bindConcurrency)
	fmt.Fprintln(os.Stdout, "steps:")
	fmt.Fprintf(os.Stdout, "  - create brand cloud with %s\n", displayCommand(scripts["create-brand"]))
	fmt.Fprintf(os.Stdout, "  - create users with %s\n", displayCommand(scripts["create-users"]))
	fmt.Fprintf(os.Stdout, "  - generate/factory-enroll devices with %s\n", displayCommand(scripts["generate-devices"]))
	fmt.Fprintf(os.Stdout, "  - bind/provision devices with %s\n", displayCommand(scripts["bind-devices"]))
	fmt.Fprintf(os.Stdout, "  - validate SQLite bind data with %s\n", displayCommand(scripts["validate-bind"]))
	fmt.Fprintf(os.Stdout, "test_data_db: %s\n", testDataDBPath(envRoot, brandname))
}

func e2eStepOrder() []string {
	return []string{"create_brand", "create_users", "prepare_factory_production", "create_devices", "bind_devices", "validate_bind"}
}

func e2eStepIndex(name string) int {
	for i, step := range e2eStepOrder() {
		if step == name {
			return i
		}
	}
	return -1
}

func shouldRunE2EStep(name, fromStep string) bool {
	if fromStep == "" {
		return true
	}
	stepIndex := e2eStepIndex(name)
	fromIndex := e2eStepIndex(fromStep)
	return stepIndex >= 0 && fromIndex >= 0 && stepIndex >= fromIndex
}

func usersArtifactCount(path string) int {
	if path == "" {
		return 0
	}
	var parsed struct {
		Users []json.RawMessage `json:"users"`
	}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &parsed); err == nil {
			return len(parsed.Users)
		}
	}
	return 0
}

func deviceManifestCount(devicesDir string) int {
	if devicesDir == "" {
		return 0
	}
	var devices []json.RawMessage
	path := filepath.Join(devicesDir, "manifests", "devices.json")
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &devices); err == nil {
			return len(devices)
		}
	}
	return 0
}

func deviceManifestMatchesSetup(devicesDir string, expectedCount int, expectedMix string) bool {
	if devicesDir == "" || expectedCount <= 0 {
		return false
	}
	devices, err := readDeviceManifest(filepath.Join(devicesDir, "manifests", "devices.json"))
	if err != nil || len(devices) != expectedCount {
		return false
	}
	expected, err := allocateDeviceMix(expectedCount, expectedMix)
	if err != nil {
		return false
	}
	actual := map[string]int{}
	for _, device := range devices {
		actual[device.DeviceType]++
	}
	for deviceType, want := range expected {
		if actual[deviceType] != want {
			return false
		}
	}
	return true
}

func bindArtifactCount(path string) int {
	if path == "" {
		return 0
	}
	var artifact struct {
		Assignments []json.RawMessage `json:"assignments"`
	}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &artifact); err == nil {
			return len(artifact.Assignments)
		}
	}
	return 0
}

func bindArtifactMatchesSetup(path, usersPath, devicesDir string, expectedUsers, expectedDevices int, expectedMix string) bool {
	if path == "" || usersPath == "" || devicesDir == "" || expectedUsers <= 0 || expectedDevices <= 0 {
		return false
	}
	var artifact struct {
		Assignments []struct {
			AssignedEmail  string   `json:"assigned_email"`
			DeviceID       string   `json:"device_id"`
			DeviceType     string   `json:"device_type"`
			ServiceOptions []string `json:"service_options"`
			OperationID    string   `json:"operation_id"`
			Status         string   `json:"status"`
		} `json:"assignments"`
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &artifact) != nil || len(artifact.Assignments) != expectedDevices {
		return false
	}
	if usersArtifactCount(usersPath) != expectedUsers {
		return false
	}
	devices, err := readDeviceManifest(filepath.Join(devicesDir, "manifests", "devices.json"))
	if err != nil || len(devices) != expectedDevices {
		return false
	}
	expectedDeviceIDs := map[string]bool{}
	for _, device := range devices {
		expectedDeviceIDs[device.DeviceID] = true
	}
	expected, err := allocateDeviceMix(expectedDevices, expectedMix)
	if err != nil {
		return false
	}
	actual := map[string]int{}
	assignedUsers := map[string]bool{}
	hasProvisionEvidence := false
	for _, assignment := range artifact.Assignments {
		if !expectedDeviceIDs[assignment.DeviceID] {
			return false
		}
		actual[assignment.DeviceType]++
		email := strings.TrimSpace(assignment.AssignedEmail)
		if email != "" {
			assignedUsers[email] = true
		}
		if strings.TrimSpace(assignment.OperationID) != "" || strings.TrimSpace(assignment.Status) != "already_bound" {
			hasProvisionEvidence = true
		}
	}
	if !hasProvisionEvidence {
		return false
	}
	for deviceType, want := range expected {
		if actual[deviceType] != want {
			return false
		}
	}
	return len(assignedUsers) == expectedUsers
}

func bindArtifactAssignedUserCount(path string) int {
	if path == "" {
		return 0
	}
	var artifact struct {
		Assignments []struct {
			AssignedEmail string `json:"assigned_email"`
		} `json:"assignments"`
	}
	raw, err := os.ReadFile(path)
	if err != nil || json.Unmarshal(raw, &artifact) != nil {
		return 0
	}
	assigned := map[string]bool{}
	for _, item := range artifact.Assignments {
		email := strings.TrimSpace(item.AssignedEmail)
		if email != "" {
			assigned[email] = true
		}
	}
	return len(assigned)
}

func validationFailureCategoryCount(outDir, category string) int {
	if outDir == "" || category == "" {
		return 0
	}
	var result struct {
		FailureCategories map[string]int `json:"failure_categories"`
	}
	path := filepath.Join(outDir, "bulk-device-bind-validation-results.json")
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &result); err == nil {
			return result.FailureCategories[category]
		}
	}
	return 0
}

func readE2EDataSetupSummary(path string) (e2eDataSetupSummary, error) {
	var summary e2eDataSetupSummary
	body, err := os.ReadFile(path)
	if err != nil {
		return summary, err
	}
	if err := json.Unmarshal(body, &summary); err != nil {
		return summary, err
	}
	if summary.UsersFile == "" {
		summary.UsersFile = stringFromJSONPath(body, "artifacts", "users_file")
	}
	if summary.DeviceBindFile == "" {
		summary.DeviceBindFile = stringFromJSONPath(body, "artifacts", "device_bind_file")
	}
	if summary.TestDataDB == "" {
		summary.TestDataDB = stringFromJSONPath(body, "artifacts", "test_data_db")
	}
	if summary.BindValidationDir == "" {
		summary.BindValidationDir = stringFromJSONPath(body, "artifacts", "bind_validation_dir")
	}
	return summary, nil
}

func stringFromJSONPath(body []byte, keys ...string) string {
	var value any
	if err := json.Unmarshal(body, &value); err != nil {
		return ""
	}
	for _, key := range keys {
		obj, ok := value.(map[string]any)
		if !ok {
			return ""
		}
		value = obj[key]
	}
	if s, ok := value.(string); ok {
		return s
	}
	return ""
}

func runRemoveK8s(args []string) error {
	fs := flag.NewFlagSet("remove-k8s", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	plan := fs.Bool("plan", false, "print cleanup plan without deleting resources")
	yes := fs.Bool("yes", false, "confirm")
	purgeStorage := fs.Bool("purge-storage", false, "delete staging PVC/PV storage before removing namespaces")
	if err := fs.Parse(args); err != nil {
		return err
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	stack := firstNonEmpty(envFileValue(filepath.Join(envRoot, "env", "stack.env"), "CLOUD_STACK_NAME"), "video-cloud-staging")
	destructive := os.Getenv("CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET") == "1"
	if *plan {
		mode := "non-destructive reset no-op"
		if destructive {
			mode = "destructive namespace cleanup"
		}
		fmt.Fprintf(os.Stdout, "cloud-remove-k8s plan\n")
		fmt.Fprintf(os.Stdout, "workspace: %s\n", workspace)
		fmt.Fprintf(os.Stdout, "env_root: %s\n", envRoot)
		fmt.Fprintf(os.Stdout, "stack: %s\n", stack)
		fmt.Fprintf(os.Stdout, "mode: %s\n", mode)
		fmt.Fprintf(os.Stdout, "purge_storage: %t\n", *purgeStorage)
		fmt.Fprintf(os.Stdout, "namespaces:\n")
		for _, ns := range k8sStagingNamespaces(stack) {
			if *purgeStorage && destructive {
				fmt.Fprintf(os.Stdout, "  - delete workloads, PVCs, then namespace %s\n", ns)
				continue
			}
			if destructive {
				fmt.Fprintf(os.Stdout, "  - reset namespace resources in %s\n", ns)
			} else {
				fmt.Fprintf(os.Stdout, "  - would skip %s unless CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET=1\n", ns)
			}
		}
		return nil
	}
	if !*yes {
		return errors.New("--yes is required")
	}
	if !destructive {
		fmt.Fprintf(os.Stderr, "[cloud-remove-k8s] non-destructive reset for %s; set CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET=1 to delete namespaces\n", stack)
		return nil
	}
	if err := validateStagingEmailDeliveryBeforeReset(envRoot); err != nil {
		return err
	}
	kubeconfig, err := ensureK8SKubeconfig(workspace, envRoot, stack)
	if err != nil {
		return err
	}
	if err := cachePublicHTTPSSecretBeforeK8SRemoval(envRoot, kubeconfig); err != nil {
		fmt.Fprintf(os.Stderr, "[cloud-remove-k8s] warning: public TLS certificate cache skipped: %v\n", err)
	}
	for _, ns := range k8sStagingNamespaces(stack) {
		if *purgeStorage {
			if err := resetK8SNamespaceResources(kubeconfig, ns); err != nil {
				return err
			}
			if err := runK8SKubectl(kubeconfig, "-n", ns, "delete", "pvc", "--all", "--ignore-not-found=true"); err != nil {
				return err
			}
			if err := runK8SKubectl(kubeconfig, "delete", "namespace", ns, "--ignore-not-found=true"); err != nil {
				return err
			}
			continue
		}
		if err := resetK8SNamespaceResources(kubeconfig, ns); err != nil {
			return err
		}
	}
	return nil
}

func cachePublicHTTPSSecretBeforeK8SRemoval(envRoot, kubeconfig string) error {
	loaded, err := envroot.Load(envRoot, "")
	if err != nil {
		return err
	}
	env := loaded.Values
	if firstNonEmpty(env["CLOUD_PROVIDER"], "lke") != "lke" {
		return nil
	}
	if strings.TrimSpace(kubeconfig) != "" {
		old := os.Getenv("RTK_CLOUD_LKE_KUBECONFIG")
		if err := os.Setenv("RTK_CLOUD_LKE_KUBECONFIG", kubeconfig); err != nil {
			return err
		}
		defer func() {
			if old == "" {
				_ = os.Unsetenv("RTK_CLOUD_LKE_KUBECONFIG")
			} else {
				_ = os.Setenv("RTK_CLOUD_LKE_KUBECONFIG", old)
			}
		}()
	}
	hosts := lkePublicHTTPSHosts(lkePublicHTTPSBaseRoutes(env))
	certPEM, keyPEM, ok, err := lkeExistingPublicHTTPSTLSSecretMaterialCoversHosts(env, hosts)
	if err != nil || !ok {
		return err
	}
	if err := lkeWritePublicHTTPSCertificateCache(provisionPaths{EnvRoot: envRoot}, env, hosts, certPEM, keyPEM); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[cloud-remove-k8s] cached public TLS certificate: %s\n", lkePublicHTTPSCertificateCacheDir(provisionPaths{EnvRoot: envRoot}, env))
	return nil
}

func resetK8SNamespaceResources(kubeconfig, ns string) error {
	if err := runK8SKubectl(kubeconfig, "get", "namespace", ns); err != nil {
		return nil
	}
	resourceGroups := []string{
		"deployment,statefulset,daemonset,job,cronjob",
		"service,ingress,networkpolicy",
		"configmap,secret,serviceaccount,role,rolebinding",
		"horizontalpodautoscaler,poddisruptionbudget",
	}
	for _, group := range resourceGroups {
		if err := runK8SKubectl(kubeconfig, "-n", ns, "delete", group, "--all", "--ignore-not-found=true"); err != nil {
			return err
		}
	}
	return nil
}

func runProvisionK8s(args []string) error {
	fs := flag.NewFlagSet("provision-k8s", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	confirm := fs.String("confirm", "", "confirm stack name")
	timeout := fs.Duration("timeout", envDurationDefault("CLOUD_STAGING_E2E_K8S_ROLLOUT_TIMEOUT", 5*time.Minute), "rollout timeout")
	if err := fs.Parse(args); err != nil {
		return err
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	stack := firstNonEmpty(envFileValue(filepath.Join(envRoot, "env", "stack.env"), "CLOUD_STACK_NAME"), "video-cloud-staging")
	if *confirm != stack {
		return fmt.Errorf("--confirm %s does not match CLOUD_STACK_NAME=%s", *confirm, stack)
	}
	kubeconfig, err := ensureK8SKubeconfig(workspace, envRoot, stack)
	if err != nil {
		return err
	}
	if err := runK8SKubectl(kubeconfig, "get", "nodes"); err != nil {
		return err
	}
	for _, ns := range k8sStagingNamespaces(stack) {
		if err := runK8SKubectl(kubeconfig, "get", "namespace", ns); err != nil {
			return err
		}
		rolloutTimeout := "--timeout=" + timeout.String()
		if err := rolloutK8SKind(kubeconfig, ns, "deployment", rolloutTimeout); err != nil {
			return err
		}
		if err := rolloutK8SKind(kubeconfig, ns, "statefulset", rolloutTimeout); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stderr, "[cloud-provision-k8s] rollout ready stack=%s kubeconfig=%s\n", stack, kubeconfig)
	return nil
}

func k8sStagingNamespaces(stack string) []string {
	return []string{
		stack + "-platform",
		stack + "-account-manager",
		stack + "-admin",
		stack + "-frontend",
		stack + "-observability",
		stack + "-video-cloud",
		stack + "-ingress",
		stack + "-secrets",
		stack + "-logger",
	}
}

func ensureK8SKubeconfig(workspace, envRoot, stack string) (string, error) {
	if path := firstNonEmpty(os.Getenv("CLOUD_STAGING_K8S_KUBECONFIG"), os.Getenv("KUBECONFIG")); path != "" {
		return path, nil
	}
	envRootKubeconfig := filepath.Join(envRoot, "state", "kubeconfig.yaml")
	if info, err := os.Stat(envRootKubeconfig); err == nil && !info.IsDir() {
		return envRootKubeconfig, nil
	}
	out := filepath.Join(workspace, ".artifacts", "kube", stack+"-lke.kubeconfig")
	if info, err := os.Stat(out); err == nil && !info.IsDir() {
		return out, nil
	}
	return downloadK8SKubeconfig(workspace, stack)
}

func downloadK8SKubeconfig(workspace, stack string) (string, error) {
	out := filepath.Join(workspace, ".artifacts", "kube", stack+"-lke.kubeconfig")
	token := strings.TrimSpace(os.Getenv("LINODE_TOKEN"))
	if token == "" {
		return "", errors.New("LINODE_TOKEN, KUBECONFIG, or CLOUD_STAGING_K8S_KUBECONFIG is required for K8s staging")
	}
	clusterID := strings.TrimSpace(os.Getenv("CLOUD_STAGING_LKE_CLUSTER_ID"))
	if clusterID == "" {
		id, err := findLinodeLKEClusterID(token, firstNonEmpty(os.Getenv("CLOUD_STAGING_LKE_CLUSTER_LABEL"), stack+"-lke"))
		if err != nil {
			return "", err
		}
		clusterID = id
	}
	req, err := http.NewRequest(http.MethodGet, "https://api.linode.com/v4/lke/clusters/"+clusterID+"/kubeconfig", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("Linode kubeconfig request failed: HTTP %d %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var parsed struct {
		Kubeconfig string `json:"kubeconfig"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	decoded, err := base64.StdEncoding.DecodeString(parsed.Kubeconfig)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return "", err
	}
	if err := os.WriteFile(out, decoded, 0o600); err != nil {
		return "", err
	}
	return out, nil
}

func findLinodeLKEClusterID(token, label string) (string, error) {
	req, err := http.NewRequest(http.MethodGet, "https://api.linode.com/v4/lke/clusters?page_size=100", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Linode LKE list failed: HTTP %d", resp.StatusCode)
	}
	var parsed struct {
		Data []struct {
			ID    int    `json:"id"`
			Label string `json:"label"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", err
	}
	for _, cluster := range parsed.Data {
		if cluster.Label == label {
			return strconv.Itoa(cluster.ID), nil
		}
	}
	return "", fmt.Errorf("Linode LKE cluster not found: %s", label)
}

func runK8SKubectl(kubeconfig string, args ...string) error {
	cmd := exec.Command("kubectl", args...)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func rolloutK8SKind(kubeconfig, namespace, kind, timeoutArg string) error {
	cmd := exec.Command("kubectl", "-n", namespace, "get", kind, "-o", "name")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return fmt.Errorf("kubectl get %s/%s: %s", namespace, kind, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return err
	}
	for _, name := range strings.Fields(string(out)) {
		if kind == "statefulset" {
			strategy, err := k8sStatefulSetUpdateStrategy(kubeconfig, namespace, name)
			if err != nil {
				return err
			}
			if strategy == "OnDelete" {
				if err := waitK8SOnDeleteStatefulSetReady(kubeconfig, namespace, name, timeoutArg); err != nil {
					return err
				}
				continue
			}
		}
		if err := runK8SKubectl(kubeconfig, "-n", namespace, "rollout", "status", name, timeoutArg); err != nil {
			return err
		}
	}
	return nil
}

func k8sStatefulSetUpdateStrategy(kubeconfig, namespace, name string) (string, error) {
	cmd := exec.Command("kubectl", "-n", namespace, "get", name, "-o", "jsonpath={.spec.updateStrategy.type}")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("kubectl get %s/%s update strategy: %s", namespace, name, strings.TrimSpace(string(out)))
	}
	return strings.TrimSpace(string(out)), nil
}

func waitK8SOnDeleteStatefulSetReady(kubeconfig, namespace, name, timeoutArg string) error {
	timeoutText := strings.TrimPrefix(timeoutArg, "--timeout=")
	timeout, err := time.ParseDuration(timeoutText)
	if err != nil || timeout <= 0 {
		return fmt.Errorf("invalid rollout timeout %q", timeoutArg)
	}
	poll := envDurationDefault("RTK_CLOUD_K8S_ROLLOUT_POLL", 2*time.Second)
	deadline := time.Now().Add(timeout)
	var last string
	for {
		cmd := exec.Command("kubectl", "-n", namespace, "get", name, "-o", "json")
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
		out, commandErr := cmd.CombinedOutput()
		if commandErr != nil {
			last = strings.TrimSpace(string(out))
		} else {
			var state struct {
				Metadata struct {
					Generation int64 `json:"generation"`
				} `json:"metadata"`
				Spec struct {
					Replicas *int32 `json:"replicas"`
				} `json:"spec"`
				Status struct {
					ObservedGeneration int64  `json:"observedGeneration"`
					ReadyReplicas      int32  `json:"readyReplicas"`
					CurrentReplicas    int32  `json:"currentReplicas"`
					CurrentRevision    string `json:"currentRevision"`
					UpdateRevision     string `json:"updateRevision"`
				} `json:"status"`
			}
			if unmarshalErr := json.Unmarshal(out, &state); unmarshalErr != nil {
				last = unmarshalErr.Error()
			} else {
				desired := int32(1)
				if state.Spec.Replicas != nil {
					desired = *state.Spec.Replicas
				}
				revisionReady := state.Status.UpdateRevision == "" || state.Status.CurrentRevision == state.Status.UpdateRevision
				if state.Status.ObservedGeneration >= state.Metadata.Generation && state.Status.ReadyReplicas == desired && state.Status.CurrentReplicas == desired && revisionReady {
					fmt.Fprintf(os.Stderr, "statefulset %q OnDelete readiness verified\n", strings.TrimPrefix(name, "statefulset/"))
					return nil
				}
				last = fmt.Sprintf("generation=%d/%d ready=%d current=%d desired=%d revision=%s/%s", state.Status.ObservedGeneration, state.Metadata.Generation, state.Status.ReadyReplicas, state.Status.CurrentReplicas, desired, state.Status.CurrentRevision, state.Status.UpdateRevision)
			}
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("statefulset %s/%s OnDelete readiness timed out after %s: %s", namespace, name, timeout, last)
		}
		time.Sleep(poll)
	}
}

func k8sServicePort(kubeconfig, namespace, service, portName string) (int, error) {
	cmd := exec.Command("kubectl", "-n", namespace, "get", "svc", service, "-o", "json")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	out, err := cmd.Output()
	if err != nil {
		return 0, err
	}
	var parsed struct {
		Spec struct {
			Ports []struct {
				Name string `json:"name"`
				Port int    `json:"port"`
			} `json:"ports"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return 0, err
	}
	if wanted, err := strconv.Atoi(portName); err == nil {
		for _, port := range parsed.Spec.Ports {
			if port.Port == wanted {
				return port.Port, nil
			}
		}
	}
	for _, port := range parsed.Spec.Ports {
		if port.Name == portName {
			return port.Port, nil
		}
	}
	return 0, fmt.Errorf("k8s service %s/%s missing port %s", namespace, service, portName)
}

func startK8SE2EPortForwards(workspace, envRoot string) ([]string, func(), error) {
	return startK8SE2EPortForwardsForServices(workspace, envRoot, true)
}

func startK8SE2EPortForwardsForServices(workspace, envRoot string, includeMQTT bool) ([]string, func(), error) {
	portForward := strings.ToLower(strings.TrimSpace(os.Getenv("CLOUD_STAGING_E2E_K8S_PORT_FORWARD")))
	if portForward == "0" || portForward == "false" || portForward == "off" {
		return nil, func() {}, nil
	}
	stack := firstNonEmpty(envFileValue(filepath.Join(envRoot, "env", "stack.env"), "CLOUD_STACK_NAME"), "video-cloud-staging")
	kubeconfig, err := ensureK8SKubeconfig(workspace, envRoot, stack)
	if err != nil {
		return nil, nil, err
	}
	accountPort := firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_ACCOUNT_MANAGER_PORT"), "18081")
	videoPort := firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_VIDEO_CLOUD_PORT"), "18080")
	factoryPort := firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_FACTORY_ENROLL_PORT"), "18443")
	factoryPorts := splitCSV(firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_FACTORY_ENROLL_PORTS"), factoryPort))
	if len(factoryPorts) == 0 {
		factoryPorts = []string{factoryPort}
	}
	mqttPort := firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_MQTT_PORT"), "18883")
	loggerPort := firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_LOGGER_PORT"), "18090")
	type portForwardSpec struct {
		ns          string
		service     string
		port        string
		local       string
		servicePort int
	}
	forwards := []portForwardSpec{
		{ns: stack + "-account-manager", service: "account-manager", port: "http", local: accountPort},
		{ns: stack + "-video-cloud", service: "video-cloud-api", port: "http", local: videoPort},
	}
	factoryURLs := make([]string, 0, len(factoryPorts))
	for _, port := range factoryPorts {
		forwards = append(forwards, portForwardSpec{ns: stack + "-video-cloud", service: "factoryenroll", port: "http", local: port})
		factoryURLs = append(factoryURLs, "http://127.0.0.1:"+port)
	}
	if includeMQTT {
		forwards = append(forwards, portForwardSpec{ns: stack + "-video-cloud", service: "mqtt", port: "mqtts", local: mqttPort})
		forwards = append(forwards, portForwardSpec{ns: stack + "-logger", service: "cloud-logger", port: "http", local: loggerPort})
	}
	cmds := []*exec.Cmd{}
	cleanup := func() {
		for _, cmd := range cmds {
			if cmd.Process != nil {
				_ = cmd.Process.Kill()
				_, _ = cmd.Process.Wait()
			}
		}
	}
	for i := range forwards {
		fwd := forwards[i]
		servicePort, err := k8sServicePort(kubeconfig, fwd.ns, fwd.service, fwd.port)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		forwards[i].servicePort = servicePort
	}
	for _, fwd := range forwards {
		cmd := exec.Command("kubectl", "-n", fwd.ns, "port-forward", "svc/"+fwd.service, fwd.local+":"+strconv.Itoa(fwd.servicePort))
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		stderr, err := cmd.StderrPipe()
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		label := fwd.ns + "/" + fwd.service
		fmt.Fprintf(os.Stderr, "[cloud-staging-e2e] port-forward start: %s 127.0.0.1:%s -> %d\n", label, fwd.local, fwd.servicePort)
		if err := cmd.Start(); err != nil {
			cleanup()
			return nil, nil, err
		}
		go streamK8SPortForwardOutput(label, stdout)
		go streamK8SPortForwardOutput(label, stderr)
		cmds = append(cmds, cmd)
	}
	for _, fwd := range forwards {
		if err := waitTCP("127.0.0.1:"+fwd.local, 15*time.Second); err != nil {
			cleanup()
			return nil, nil, err
		}
		fmt.Fprintf(os.Stderr, "[cloud-staging-e2e] port-forward ready: %s/%s 127.0.0.1:%s -> %d\n", fwd.ns, fwd.service, fwd.local, fwd.servicePort)
	}
	env := []string{
		"ACCOUNT_MANAGER_BASE_URL=http://127.0.0.1:" + accountPort,
		"VIDEO_CLOUD_BASE_URL=http://127.0.0.1:" + videoPort,
		"VIDEO_CLOUD_TOKEN_BASE_URL=" + firstNonEmpty(
			os.Getenv("CLOUD_STAGING_E2E_VIDEO_CLOUD_TOKEN_BASE_URL_OVERRIDE"),
			envFileValue(filepath.Join(envRoot, "env", "stack.env"), "VIDEO_CLOUD_TOKEN_BASE_URL"),
			k8sE2ETokenBaseURL(videoPort),
		),
		"FACTORY_ENROLL_URL=" + strings.Join(factoryURLs, ","),
		"VIDEO_CLOUD_LOAD_MQTT_SET=broker",
		"CLOUD_STAGING_E2E_SKIP_BOOTSTRAP=1",
		"CLOUD_STAGING_E2E_ENDPOINT_SOURCE=k8s-service",
	}
	if includeMQTT {
		env = append(env, "VIDEO_CLOUD_MQTT_ADDR=127.0.0.1:"+mqttPort)
		env = append(env, "VIDEO_CLOUD_LOGGER_ENDPOINT=http://127.0.0.1:"+loggerPort)
	}
	if secretEnv, err := readK8SSecretEnv(kubeconfig, stack+"-account-manager", "account-manager-runtime", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD", "ACCOUNT_MANAGER_INTERNAL_AUTH_TOKEN"); err == nil {
		env = append(env, secretEnv...)
	} else {
		cleanup()
		return nil, nil, err
	}
	if secretEnv, err := readK8SSecretEnv(kubeconfig, stack+"-video-cloud", "video-cloud-runtime", "VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN", "VIDEO_CLOUD_LOGGER_TOKEN"); err == nil {
		env = append(env, secretEnv...)
	} else {
		cleanup()
		return nil, nil, err
	}
	if secretEnv, err := readK8SSecretEnv(kubeconfig, stack+"-video-cloud", "factoryenroll-runtime", "FACTORY_ENROLL_AUTH_KEY"); err == nil {
		env = append(env, secretEnv...)
	} else {
		cleanup()
		return nil, nil, err
	}
	return env, cleanup, nil
}

func k8sE2ETokenBaseURL(videoPort string) string {
	return firstNonEmpty(
		os.Getenv("CLOUD_STAGING_E2E_VIDEO_CLOUD_TOKEN_BASE_URL_OVERRIDE"),
		"http://127.0.0.1:"+videoPort,
	)
}

func streamK8SPortForwardOutput(label string, r io.Reader) {
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !shouldLogK8SPortForwardLine(line) {
			continue
		}
		fmt.Fprintf(os.Stderr, "[cloud-staging-e2e] port-forward %s: %s\n", label, line)
	}
}

func shouldLogK8SPortForwardLine(line string) bool {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	if strings.HasPrefix(line, "Forwarding from ") {
		return false
	}
	if strings.HasPrefix(line, "Handling connection for ") {
		return false
	}
	return true
}

func readK8SSecretEnv(kubeconfig, namespace, secret string, keys ...string) ([]string, error) {
	cmd := exec.Command(lkeKubectl(), "-n", namespace, "get", "secret", secret, "-o", "json")
	cmd.Env = os.Environ()
	if strings.TrimSpace(kubeconfig) != "" {
		cmd.Env = append(cmd.Env, "KUBECONFIG="+kubeconfig)
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, err
	}
	env := []string{}
	for _, key := range keys {
		raw := strings.TrimSpace(parsed.Data[key])
		if raw == "" {
			return nil, fmt.Errorf("k8s secret %s/%s missing %s", namespace, secret, key)
		}
		decoded, err := base64.StdEncoding.DecodeString(raw)
		if err != nil {
			return nil, err
		}
		env = append(env, key+"="+string(decoded))
	}
	return env, nil
}

func waitTCP(addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s: %w", addr, lastErr)
}

type stagingRuntimeContext struct {
	workspace string
	stackFile string
	envRoot   string
	provider  string
	stackName string
}

func resolveStagingRuntimeContext(workspaceFlag, stackFileFlag, envRootFlag string) (stagingRuntimeContext, error) {
	ctx := stagingRuntimeContext{}
	workspace := workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return ctx, err
		}
	}
	ctx.workspace = workspace
	stackFile := stackFileFlag
	provider := firstNonEmpty(os.Getenv("CLOUD_PROVIDER"), os.Getenv("RTK_CLOUD_STAGING_PROVIDER"))
	if stackFile == "" {
		stackFile = filepath.Join(workspace, "cloud_env", "staging", "runtime", "env", "stack.env")
	}
	if !filepath.IsAbs(stackFile) {
		stackFile = filepath.Join(workspace, stackFile)
	}
	ctx.stackFile = filepath.Clean(stackFile)
	if provider == "" {
		provider = envFileValue(ctx.stackFile, "CLOUD_PROVIDER")
	}
	provider = firstNonEmpty(provider, "lke")
	if provider == "linode" {
		return ctx, fmt.Errorf("%w: CLOUD_PROVIDER=linode used the retired VM runtime; use CLOUD_PROVIDER=lke or another Kubernetes provider", errVMRuntimeRetired)
	}
	if !isKubernetesProviderName(provider) {
		return ctx, fmt.Errorf("unsupported CLOUD_PROVIDER=%s; staging E2E currently supports Kubernetes providers only", provider)
	}
	ctx.provider = provider
	if err := os.Setenv("CLOUD_PROVIDER", provider); err != nil {
		return ctx, err
	}
	if os.Getenv("CLOUD_DNS_ROOT_DOMAIN") == "" {
		if value := envFileValue(ctx.stackFile, "CLOUD_DNS_ROOT_DOMAIN"); value != "" {
			if err := os.Setenv("CLOUD_DNS_ROOT_DOMAIN", value); err != nil {
				return ctx, err
			}
		}
	}
	envRoot := envRootFlag
	if envRoot == "" {
		envRoot = filepath.Join(filepath.Dir(ctx.stackFile), "..")
	}
	if !filepath.IsAbs(envRoot) {
		envRoot = filepath.Join(workspace, envRoot)
	}
	envRoot = filepath.Clean(envRoot)
	envRoot, err = resolveEnvRoot(workspace, envRoot)
	if err != nil {
		return ctx, err
	}
	ctx.envRoot = envRoot
	ctx.stackName = firstNonEmpty(os.Getenv("RTK_CLOUD_STAGING_STACK_NAME"), envFileValue(filepath.Join(envRoot, "env", "stack.env"), "CLOUD_STACK_NAME"), envFileValue(ctx.stackFile, "CLOUD_STACK_NAME"), "video-cloud-staging")
	return ctx, nil
}

func stagingResetCommand(ctx stagingRuntimeContext, purgeStorage bool) (string, []string) {
	script := firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_REMOVE_K8S_SCRIPT"), selfCommandPath("remove-k8s"))
	if ctx.provider == "lke" {
		if v := os.Getenv("CLOUD_STAGING_E2E_REMOVE_SCRIPT"); v != "" {
			script = v
		}
	}
	args := []string{"--workspace", ctx.workspace, "--env-root", ctx.envRoot, "--yes"}
	if purgeStorage {
		args = append(args, "--purge-storage")
	}
	return script, args
}

func stagingProvisionCommand(ctx stagingRuntimeContext) (string, []string) {
	lkeProvisionScript := os.Getenv("CLOUD_STAGING_E2E_PROVISION_SCRIPT")
	script := firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_PROVISION_K8S_SCRIPT"), selfCommandPath("provision-k8s"))
	args := []string{"--workspace", ctx.workspace, "--env-root", ctx.envRoot, "--confirm", ctx.stackName}
	if ctx.provider == "lke" {
		if lkeProvisionScript != "" {
			return lkeProvisionScript, []string{"--workspace", ctx.workspace, "--env-root", ctx.envRoot, "--all", "--confirm", ctx.stackName}
		}
		if os.Getenv("CLOUD_STAGING_E2E_PROVISION_K8S_SCRIPT") == "" {
			return selfCommandPath("provision"), []string{"--workspace", ctx.workspace, "--env-root", ctx.envRoot, "--preflight", "--plan", "--apply", "--deploy", "--dns", "--artifacts", "--confirm", ctx.stackName}
		}
	}
	return script, args
}

func runStagingPhaseCommand(argv []string, extraEnv []string) error {
	if len(argv) == 0 {
		return errors.New("empty staging phase command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if len(extraEnv) > 0 {
		cmd.Env = append(os.Environ(), extraEnv...)
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runStagingResetK8s(args []string) error {
	fs := flag.NewFlagSet("staging-reset-k8s", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace root")
	stackFileFlag := fs.String("stack-file", os.Getenv("RTK_CLOUD_STACK_FILE"), "stack.env path")
	envRootFlag := fs.String("env-root", os.Getenv("RTK_CLOUD_STAGING_ENV_ROOT"), "staging environment root")
	confirm := fs.String("confirm", "", "stack name confirmation")
	planMode := fs.Bool("plan", false, "print reset plan")
	purgeStorage := fs.Bool("purge-storage", false, "also delete staging PVC/PV/provider storage")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, err := resolveStagingRuntimeContext(*workspaceFlag, *stackFileFlag, *envRootFlag)
	if err != nil {
		return err
	}
	script, commandArgs := stagingResetCommand(ctx, *purgeStorage)
	if *planMode {
		fmt.Fprintln(os.Stdout, "cloud-staging-reset-k8s plan")
		fmt.Fprintf(os.Stdout, "workspace: %s\n", ctx.workspace)
		fmt.Fprintf(os.Stdout, "env_root: %s\n", ctx.envRoot)
		fmt.Fprintf(os.Stdout, "stack: %s\n", ctx.stackName)
		fmt.Fprintln(os.Stdout, "phase: reset")
		fmt.Fprintf(os.Stdout, "purge_storage: %v\n", *purgeStorage)
		if *purgeStorage {
			fmt.Fprintln(os.Stdout, "storage: purge PV/PVC/provider volumes")
		} else {
			fmt.Fprintln(os.Stdout, "storage: preserve PV/PVC/provider volumes")
		}
		fmt.Fprintf(os.Stdout, "command: %s\n", displayCommand(script))
		return nil
	}
	if *confirm != ctx.stackName {
		if *confirm == "" {
			return fmt.Errorf("--confirm %s is required before resetting staging K8s", ctx.stackName)
		}
		return fmt.Errorf("--confirm must be %s, got %s", ctx.stackName, *confirm)
	}
	if err := runStagingPhaseCommand(commandWithArgs(script, commandArgs...), []string{"CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET=1"}); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"overall": "pass", "phase": "reset", "stack": ctx.stackName, "purge_storage": *purgeStorage})
}

func runStagingProvision(args []string) error {
	fs := flag.NewFlagSet("staging-provision", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace root")
	stackFileFlag := fs.String("stack-file", os.Getenv("RTK_CLOUD_STACK_FILE"), "stack.env path")
	envRootFlag := fs.String("env-root", os.Getenv("RTK_CLOUD_STAGING_ENV_ROOT"), "staging environment root")
	confirm := fs.String("confirm", "", "stack name confirmation")
	planMode := fs.Bool("plan", false, "print provision plan")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, err := resolveStagingRuntimeContext(*workspaceFlag, *stackFileFlag, *envRootFlag)
	if err != nil {
		return err
	}
	script, commandArgs := stagingProvisionCommand(ctx)
	if *planMode {
		fmt.Fprintln(os.Stdout, "cloud-staging-provision plan")
		fmt.Fprintf(os.Stdout, "workspace: %s\n", ctx.workspace)
		fmt.Fprintf(os.Stdout, "env_root: %s\n", ctx.envRoot)
		fmt.Fprintf(os.Stdout, "stack: %s\n", ctx.stackName)
		fmt.Fprintln(os.Stdout, "phase: provision")
		if ctx.provider == "lke" {
			missing := missingLKEImageEnvKeys()
			if len(missing) > 0 {
				if env, source := stackLKEImageEnv(ctx.envRoot); lkeImageEnvHasKeys(env, missing) {
					fmt.Fprintf(os.Stdout, "image_resolve: stack env (%s)\n", source)
				} else if env, source := existingLKEImageEnv(ctx.envRoot); lkeImageEnvHasKeys(env, missing) {
					fmt.Fprintf(os.Stdout, "image_resolve: existing artifact (%s)\n", source)
				} else {
					fmt.Fprintf(os.Stdout, "image_resolve: automatic (%s)\n", strings.Join(missing, ","))
				}
			} else {
				fmt.Fprintln(os.Stdout, "image_resolve: skipped (all LKE image env vars provided)")
			}
		}
		fmt.Fprintf(os.Stdout, "provision K8s staging with %s\n", displayCommand(script))
		return nil
	}
	if *confirm != ctx.stackName {
		if *confirm == "" {
			return fmt.Errorf("--confirm %s is required before provisioning staging", ctx.stackName)
		}
		return fmt.Errorf("--confirm must be %s, got %s", ctx.stackName, *confirm)
	}
	if ctx.provider == "lke" {
		cfg, err := resolveDeploymentConfig(ctx.workspace, "staging", "")
		if err != nil {
			return fmt.Errorf("resolve staging credential requirements: %w", err)
		}
		if err := validateDeploymentCredentials(cfg, defaultDeploymentEnvironmentCredentialFile(cfg.Environment)); err != nil {
			return err
		}
		if err := resolveLKEImagesIfNeeded(ctx.workspace, ctx.envRoot); err != nil {
			return err
		}
	}
	if err := runStagingPhaseCommand(commandWithArgs(script, commandArgs...), nil); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"overall": "pass", "phase": "provision", "stack": ctx.stackName})
}

func runStagingAcceptance(args []string) error {
	return runEnvironmentAcceptance(args)
}

func runEnvironmentAcceptance(args []string) error {
	fs := flag.NewFlagSet("environment-acceptance", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace root")
	stackFileFlag := fs.String("stack-file", os.Getenv("RTK_CLOUD_STACK_FILE"), "stack.env path")
	envRootFlag := fs.String("env-root", os.Getenv("RTK_CLOUD_STAGING_ENV_ROOT"), "environment runtime root")
	confirm := fs.String("confirm", "", "stack name confirmation")
	planMode := fs.Bool("plan", false, "print acceptance plan")
	outDir := fs.String("out-dir", "", "override report output directory")
	brandname := fs.String("brandname", "RTK", "brand cloud name")
	userCount := fs.Int("user-count", 10, "user count")
	deviceCount := fs.Int("device-count", 100, "device count")
	deviceMix := fs.String("device-mix", "camera=40,light=25,air_conditioner=20,smart_meter=15", "device mix")
	devicePrefix := fs.String("device-prefix", "load-device", "device prefix")
	userConcurrency := fs.Int("user-concurrency", envInt("CLOUD_STAGING_E2E_USER_CONCURRENCY", 64), "user creation concurrency")
	deviceConcurrency := fs.Int("device-concurrency", envInt("CLOUD_STAGING_E2E_DEVICE_CONCURRENCY", 64), "device generation concurrency")
	bindConcurrency := fs.Int("bind-concurrency", envInt("CLOUD_STAGING_E2E_BIND_CONCURRENCY", 64), "device bind concurrency")
	skipMQTTProbe := fs.Bool("skip-mqtt-probe", false, "run MQTT test without live broker probe")
	quiet := fs.Bool("quiet", false, "suppress periodic progress lines")
	resume := fs.Bool("resume", true, "reuse completed data setup artifacts")
	noResume := fs.Bool("no-resume", false, "recreate users/devices/bind artifacts")
	steps := fs.String("steps", "all", "comma-separated steps: reset,provision,data,mqtt,runtime-logs,billing-log,billing-db,lifecycle (or all)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *noResume {
		*resume = false
	}
	if _, err := parseE2ESteps(*steps, true, true); err != nil {
		return err
	}
	ctx, err := resolveStagingRuntimeContext(*workspaceFlag, *stackFileFlag, *envRootFlag)
	if err != nil {
		return err
	}
	if !*planMode && *confirm != ctx.stackName {
		if *confirm == "" {
			return fmt.Errorf("--confirm %s is required before running environment acceptance", ctx.stackName)
		}
		return fmt.Errorf("--confirm must be %s, got %s", ctx.stackName, *confirm)
	}
	return runStagingE2ETest(stagingE2ETestArgs(stagingE2EArgs{
		workspace: ctx.workspace, envRoot: ctx.envRoot, stackName: ctx.stackName, run: !*planMode, plan: *planMode,
		brandname: *brandname, userCount: *userCount, deviceCount: *deviceCount, deviceMix: *deviceMix, devicePrefix: *devicePrefix,
		userConcurrency: *userConcurrency, deviceConcurrency: *deviceConcurrency, bindConcurrency: *bindConcurrency,
		outDir: *outDir, skipMQTTProbe: *skipMQTTProbe, skipRemove: true, skipProvision: true, quiet: *quiet, resume: *resume,
		confirmOverride: *confirm,
		steps:           *steps,
	}))
}

func runStagingE2ETest(args []string) error {
	fs := flag.NewFlagSet("staging-e2e-test", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	runMode := fs.Bool("run", false, "run")
	planMode := fs.Bool("plan", false, "plan")
	confirm := fs.String("confirm", "", "confirm")
	skipRemove := fs.Bool("skip-remove", false, "skip remove")
	brandname := fs.String("brandname", "RTK", "brand name")
	userCount := fs.Int("user-count", 10, "user count")
	userEmailPrefix := fs.String("user-email-prefix", "", "optional run-scoped user email prefix")
	userEmailDomain := fs.String("user-email-domain", "users.local", "test-only user email domain")
	deviceCount := fs.Int("device-count", 100, "device count")
	deviceMix := fs.String("device-mix", "camera=40,light=25,air_conditioner=20,smart_meter=15", "device mix")
	devicePrefix := fs.String("device-prefix", "load-device", "device prefix")
	userConcurrency := fs.Int("user-concurrency", envInt("CLOUD_STAGING_E2E_USER_CONCURRENCY", 64), "user creation concurrency")
	deviceConcurrency := fs.Int("device-concurrency", envInt("CLOUD_STAGING_E2E_DEVICE_CONCURRENCY", 64), "device generation concurrency")
	bindConcurrency := fs.Int("bind-concurrency", envInt("CLOUD_STAGING_E2E_BIND_CONCURRENCY", 64), "device bind concurrency")
	outDir := fs.String("out-dir", "", "out dir")
	skipMQTTProbe := fs.Bool("skip-mqtt-probe", false, "skip mqtt probe")
	quiet := fs.Bool("quiet", false, "suppress periodic progress output")
	purgeStorage := fs.Bool("purge-storage", false, "also delete staging PVC/PV/provider storage during reset")
	resume := fs.Bool("resume", true, "reuse completed data setup artifacts")
	noResume := fs.Bool("no-resume", false, "recreate data setup artifacts")
	skipProvision := fs.Bool("skip-provision", false, "skip K8s provision and run only acceptance checks")
	selectedStepsFlag := fs.String("steps", "all", "comma-separated steps: reset,provision,data,mqtt,runtime-logs,billing-log,billing-db,lifecycle (or all)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required")
	}
	if *noResume {
		*resume = false
	}
	if _, err := parseE2ESteps(*selectedStepsFlag, *skipRemove, *skipProvision); err != nil {
		return err
	}
	selection, selectionErr := parseE2ESteps(*selectedStepsFlag, *skipRemove, *skipProvision)
	if selectionErr != nil {
		return selectionErr
	}
	if !*skipRemove && !hasFlag(args, "--resume") {
		*resume = false
	}
	if *userCount <= 0 {
		return errors.New("--user-count must be a positive integer")
	}
	if *deviceCount <= 0 {
		return errors.New("--device-count must be a positive integer")
	}
	if *userConcurrency <= 0 || *deviceConcurrency <= 0 || *bindConcurrency <= 0 {
		return errors.New("--user-concurrency, --device-concurrency, and --bind-concurrency must be positive integers")
	}
	_ = planMode
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	stackName := envFileValue(filepath.Join(envRoot, "env", "stack.env"), "CLOUD_STACK_NAME")
	if stackName == "" {
		stackName = "video-cloud-staging"
	}
	provider := firstNonEmpty(os.Getenv("CLOUD_PROVIDER"), os.Getenv("RTK_CLOUD_STAGING_PROVIDER"), envFileValue(filepath.Join(envRoot, "env", "stack.env"), "CLOUD_PROVIDER"))
	provider = firstNonEmpty(provider, "lke")
	if provider == "linode" {
		return fmt.Errorf("%w: CLOUD_PROVIDER=linode used the retired VM runtime; use CLOUD_PROVIDER=lke or another Kubernetes provider", errVMRuntimeRetired)
	}
	if !isKubernetesProviderName(provider) {
		return fmt.Errorf("unsupported CLOUD_PROVIDER=%s; staging E2E currently supports Kubernetes providers only", provider)
	}
	lkeRemoveScript := os.Getenv("CLOUD_STAGING_E2E_REMOVE_SCRIPT")
	lkeProvisionScript := os.Getenv("CLOUD_STAGING_E2E_PROVISION_SCRIPT")
	useLKEProvision := provider == "lke" && os.Getenv("CLOUD_STAGING_E2E_PROVISION_K8S_SCRIPT") == "" && lkeProvisionScript == ""
	useLegacyLKEProvision := provider == "lke" && lkeProvisionScript != ""
	scripts := map[string]string{
		"remove-k8s":       firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_REMOVE_K8S_SCRIPT"), selfCommandPath("remove-k8s")),
		"provision-k8s":    firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_PROVISION_K8S_SCRIPT"), selfCommandPath("provision-k8s")),
		"setup-data":       firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_DATA_SETUP_SCRIPT"), selfCommandPath("environment-e2e-data-setup")),
		"mqtt-test":        firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_MQTT_TEST_SCRIPT"), selfCommandPath("mqtt-test")),
		"mqtt-log-verify":  firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_MQTT_LOG_VERIFY_SCRIPT"), selfCommandPath("environment-e2e-mqtt-log-verify")),
		"billing-verify":   firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_BILLING_VERIFY_SCRIPT"), selfCommandPath("environment-e2e-billing-verify")),
		"lifecycle-verify": selfCommandPath("provisioning-lifecycle-evidence"),
	}
	if provider == "lke" {
		if lkeRemoveScript != "" {
			scripts["remove-k8s"] = lkeRemoveScript
		}
		if lkeProvisionScript != "" {
			scripts["provision-k8s"] = lkeProvisionScript
		}
	}
	if useLKEProvision {
		scripts["provision-k8s"] = selfCommandPath("provision")
	}
	if !*runMode {
		phase := "full"
		if *skipRemove && *skipProvision {
			phase = "acceptance"
		}
		printE2EPlan(workspace, envRoot, stackName, phase, *brandname, *userCount, *deviceCount, *deviceMix, *userConcurrency, *deviceConcurrency, *bindConcurrency, *skipRemove, *skipProvision, *selectedStepsFlag, scripts)
		return nil
	}
	if *confirm != stackName {
		return fmt.Errorf("--confirm %s does not match CLOUD_STACK_NAME=%s", *confirm, stackName)
	}
	if useLKEProvision && selection.Provision {
		if err := materializeStagingE2EDeploymentConfig(workspace, envRoot); err != nil {
			return err
		}
	}
	if selection.Reset {
		if err := validateStagingEmailDeliveryBeforeReset(envRoot); err != nil {
			return fmt.Errorf("staging reset blocked before deleting workloads: %w", err)
		}
	}
	if *outDir == "" {
		*outDir = filepath.Join(envRoot, "artifacts", "staging-e2e", time.Now().UTC().Format("20060102T150405Z"))
	}
	logsDir := filepath.Join(*outDir, "logs")
	if err := os.MkdirAll(logsDir, 0o755); err != nil {
		return err
	}
	steps := []e2eStep{}
	runStep := func(name string, argv ...string) error {
		step, err := runE2EStepWithOptions(name, filepath.Join(logsDir, name+".log"), e2eStepOptions{Quiet: *quiet}, argv...)
		steps = append(steps, step)
		return err
	}
	childEnv := []string{}
	if selection.Reset {
		resetArgs := append(commandWithArgs(scripts["remove-k8s"], "--workspace", workspace, "--env-root", envRoot), "--yes")
		if *purgeStorage {
			resetArgs = append(resetArgs, "--purge-storage")
		}
		step, err := runE2EStepWithOptions("reset_k8s", filepath.Join(logsDir, "reset_k8s.log"), e2eStepOptions{Quiet: *quiet, Env: []string{"CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET=1"}}, resetArgs...)
		steps = append(steps, step)
		if err != nil {
			return err
		}
	}
	if useLKEProvision && selection.Provision {
		// remove-k8s may normalize the runtime environment. Re-materialize the
		// tracked deployment config before invoking the lower-level provisioner.
		if err := materializeStagingE2EDeploymentConfig(workspace, envRoot); err != nil {
			return err
		}
	}
	k8sProvisionArgs := []string{"--workspace", workspace, "--env-root", envRoot, "--confirm", stackName}
	if useLKEProvision {
		k8sProvisionArgs = []string{"--workspace", workspace, "--env-root", envRoot, "--preflight", "--plan", "--apply", "--deploy", "--dns", "--artifacts", "--confirm", stackName}
	} else if useLegacyLKEProvision {
		k8sProvisionArgs = []string{"--workspace", workspace, "--env-root", envRoot, "--all", "--confirm", stackName}
	}
	if selection.Provision {
		if err := runStep("provision_k8s", commandWithArgs(scripts["provision-k8s"], k8sProvisionArgs...)...); err != nil {
			return err
		}
	}
	portForwardEnv, cleanup, err := startK8SE2EPortForwardsForServices(workspace, envRoot, selection.MQTT || selection.RuntimeLogs || selection.BillingLog)
	if err != nil {
		return err
	}
	defer cleanup()
	childEnv = append(childEnv, portForwardEnv...)
	dataSetupDir := filepath.Join(*outDir, "data-setup")
	testDataDB := testDataDBPath(envRoot, *brandname)
	dataSetupSummaryFile := filepath.Join(dataSetupDir, "summary.json")
	bindValidationDir := filepath.Join(dataSetupDir, "bind-validation")
	if selection.Data {
		dataSetupArgs := []string{"--workspace", workspace, "--env-root", envRoot, "--brandname", *brandname, "--user-count", strconv.Itoa(*userCount), "--device-count", strconv.Itoa(*deviceCount), "--device-mix", *deviceMix, "--device-prefix", *devicePrefix, "--user-concurrency", strconv.Itoa(*userConcurrency), "--device-concurrency", strconv.Itoa(*deviceConcurrency), "--bind-concurrency", strconv.Itoa(*bindConcurrency), "--out-dir", dataSetupDir}
		if strings.TrimSpace(*userEmailPrefix) != "" {
			dataSetupArgs = append(dataSetupArgs, "--user-email-prefix", *userEmailPrefix)
		}
		if strings.TrimSpace(*userEmailDomain) != "" {
			dataSetupArgs = append(dataSetupArgs, "--user-email-domain", *userEmailDomain)
		}
		if *quiet {
			dataSetupArgs = append(dataSetupArgs, "--quiet")
		}
		if !*resume {
			dataSetupArgs = append(dataSetupArgs, "--no-resume")
		}
		dataSetupStep, dataErr := runE2EStepWithOptions("setup_brand_devices", filepath.Join(logsDir, "setup_brand_devices.log"), e2eStepOptions{Quiet: *quiet, Env: childEnv}, commandWithArgs(scripts["setup-data"], dataSetupArgs...)...)
		steps = append(steps, dataSetupStep)
		if dataErr != nil {
			return dataErr
		}
		dataSummary, dataErr := readE2EDataSetupSummary(filepath.Join(dataSetupDir, "summary.json"))
		if dataErr != nil {
			return dataErr
		}
		testDataDB = firstNonEmpty(dataSummary.TestDataDB, testDataDB)
		dataSetupSummaryFile = firstNonEmpty(dataSummary.SummaryFile, dataSetupSummaryFile)
		bindValidationDir = firstNonEmpty(dataSummary.BindValidationDir, bindValidationDir)
	}
	if selection.MQTT {
		mqttArgs := []string{"--env-root", envRoot, "--brandname", *brandname, "--profile", "smoke", "--test-data-db", testDataDB, "--out-dir", filepath.Join(*outDir, "home-mqtt")}
		if *skipMQTTProbe {
			mqttArgs = append(mqttArgs, "--no-mqtt-probe")
		} else {
			mqttArgs = append(mqttArgs, "--mqtt-probe")
		}
		step, mqttErr := runE2EStepWithOptions("cloud_mqtt_test", filepath.Join(logsDir, "cloud_mqtt_test.log"), e2eStepOptions{Quiet: *quiet, Env: childEnv}, commandWithArgs(scripts["mqtt-test"], mqttArgs...)...)
		steps = append(steps, step)
		if mqttErr != nil {
			return mqttErr
		}
	}
	mqttResultsFile := filepath.Join(*outDir, "home-mqtt", "results.json")
	mqttLogVerifySummaryFile := filepath.Join(*outDir, "mqtt-log-verify", "summary.json")
	if selection.RuntimeLogs {
		mqttLogVerifyArgs := []string{"--workspace", workspace, "--env-root", envRoot, "--mqtt-results", mqttResultsFile, "--out-dir", filepath.Dir(mqttLogVerifySummaryFile)}
		step, logErr := runE2EStepWithOptions("verify_mqtt_logs", filepath.Join(logsDir, "verify_mqtt_logs.log"), e2eStepOptions{Quiet: *quiet, Env: childEnv}, commandWithArgs(scripts["mqtt-log-verify"], mqttLogVerifyArgs...)...)
		steps = append(steps, step)
		if logErr != nil {
			return logErr
		}
	}
	if selection.BillingLog || selection.BillingDB {
		if _, err := os.Stat(testDataDB); err != nil {
			return fmt.Errorf("billing step requires test data database %s: %w", testDataDB, err)
		}
		billingChecks := []string{}
		if selection.BillingLog {
			billingChecks = append(billingChecks, "log")
		}
		if selection.BillingDB {
			billingChecks = append(billingChecks, "db")
		}
		billingArgs := []string{"--workspace", workspace, "--env-root", envRoot, "--stack", stackName, "--test-data-db", testDataDB, "--out-dir", filepath.Join(*outDir, "billing-verify"), "--checks", strings.Join(billingChecks, ",")}
		if _, err := os.Stat(mqttResultsFile); err == nil {
			billingArgs = append(billingArgs, "--mqtt-results", mqttResultsFile)
		}
		step, billingErr := runE2EStepWithOptions("verify_billing", filepath.Join(logsDir, "verify_billing.log"), e2eStepOptions{Quiet: *quiet, Env: childEnv}, commandWithArgs(scripts["billing-verify"], billingArgs...)...)
		steps = append(steps, step)
		if billingErr != nil {
			return billingErr
		}
	}
	if selection.Lifecycle {
		lifecycleEnv := append([]string(nil), childEnv...)
		if strings.TrimSpace(os.Getenv("VIDEO_CLOUD_LOAD_ADMIN_TOKEN")) == "" && provider == "lke" {
			token, tokenErr := videoCloudAdminTokenValue(workspace, envRoot, 30*time.Minute)
			if tokenErr != nil {
				return fmt.Errorf("issue short-lived video-cloud admin token for lifecycle acceptance: %w", tokenErr)
			}
			lifecycleEnv = append(lifecycleEnv, "VIDEO_CLOUD_LOAD_ADMIN_TOKEN="+token)
		}
		lifecycleArgs := []string{
			"--workspace", workspace,
			"--env-root", envRoot,
			"--brandname", *brandname,
			"--run-id", firstNonEmpty(os.Getenv("RUNTIME_COVERAGE_RUN_ID"), filepath.Base(*outDir)),
			"--out-dir", filepath.Join(*outDir, "provisioning-lifecycle"),
		}
		step, lifecycleErr := runE2EStepWithOptions("verify_provisioning_lifecycle", filepath.Join(logsDir, "verify_provisioning_lifecycle.log"), e2eStepOptions{Quiet: *quiet, Env: lifecycleEnv}, commandWithArgs(scripts["lifecycle-verify"], lifecycleArgs...)...)
		steps = append(steps, step)
		if lifecycleErr != nil {
			return lifecycleErr
		}
	}
	overall := "pass"
	for _, step := range steps {
		if step.Status != "PASS" {
			overall = "fail"
		}
	}
	summaryFile := filepath.Join(*outDir, "summary.json")
	reportFile := filepath.Join(*outDir, "test_report.md")
	summary := map[string]any{
		"overall":      overall,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"env_root":     envRoot,
		"stack":        stackName,
		"target":       "k8s",
		"brandname":    *brandname,
		"artifacts":    map[string]any{"test_data_db": testDataDB, "bind_validation_dir": bindValidationDir, "data_setup_summary_file": dataSetupSummaryFile, "mqtt_log_verify_summary_file": mqttLogVerifySummaryFile, "billing_verify_summary_file": filepath.Join(*outDir, "billing-verify", "summary.json"), "provisioning_lifecycle_results_file": filepath.Join(*outDir, "provisioning-lifecycle", "results.json"), "report_file": reportFile},
		"steps":        steps,
	}
	if err := writeJSON(summaryFile, summary); err != nil {
		return err
	}
	if err := os.WriteFile(reportFile, []byte(renderE2EReport(overall, envRoot, stackName, *brandname, testDataDB, bindValidationDir, dataSetupSummaryFile, filepath.Join(*outDir, "home-mqtt"), mqttLogVerifySummaryFile, steps)), 0o644); err != nil {
		return err
	}
	if containsSensitiveReportTerms(readText(summaryFile)) || containsSensitiveReportTerms(readText(reportFile)) {
		return errors.New("sanitized report contains sensitive terms")
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"overall": overall, "summary_file": summaryFile, "report_file": reportFile}); err != nil {
		return err
	}
	if overall != "pass" {
		return exitCode(1)
	}
	return nil
}

func runStagingE2E(args []string) error {
	fs := flag.NewFlagSet("run-staging-e2e", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace root")
	stackFileFlag := fs.String("stack-file", os.Getenv("RTK_CLOUD_STACK_FILE"), "stack.env path")
	envRootFlag := fs.String("env-root", os.Getenv("RTK_CLOUD_STAGING_ENV_ROOT"), "staging environment root")
	confirm := fs.String("confirm", "", "stack name confirmation")
	planMode := fs.Bool("plan", false, "print the underlying E2E plan")
	outDir := fs.String("out-dir", "", "override report output directory")
	brandname := fs.String("brandname", "RTK", "brand cloud name")
	userCount := fs.Int("user-count", 10, "user count")
	userEmailPrefix := fs.String("user-email-prefix", "", "optional run-scoped user email prefix")
	userEmailDomain := fs.String("user-email-domain", "users.local", "test-only user email domain")
	deviceCount := fs.Int("device-count", 100, "device count")
	deviceMix := fs.String("device-mix", "camera=40,light=25,air_conditioner=20,smart_meter=15", "device mix")
	devicePrefix := fs.String("device-prefix", "load-device", "device prefix")
	userConcurrency := fs.Int("user-concurrency", envInt("CLOUD_STAGING_E2E_USER_CONCURRENCY", 64), "user creation concurrency")
	deviceConcurrency := fs.Int("device-concurrency", envInt("CLOUD_STAGING_E2E_DEVICE_CONCURRENCY", 64), "device generation concurrency")
	bindConcurrency := fs.Int("bind-concurrency", envInt("CLOUD_STAGING_E2E_BIND_CONCURRENCY", 64), "device bind concurrency")
	skipMQTTProbe := fs.Bool("skip-mqtt-probe", false, "run MQTT test without live broker probe")
	skipRemove := fs.Bool("skip-remove", false, "skip K8s reset and keep existing cluster state")
	purgeStorage := fs.Bool("purge-storage", false, "also delete staging PVC/PV/provider storage during reset")
	quiet := fs.Bool("quiet", false, "suppress periodic progress lines")
	resume := fs.Bool("resume", true, "reuse completed data setup artifacts")
	noResume := fs.Bool("no-resume", false, "recreate users/devices/bind artifacts")
	steps := fs.String("steps", "all", "comma-separated steps: reset,provision,data,mqtt,runtime-logs,billing-log,billing-db,lifecycle (or all)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *noResume {
		*resume = false
	}
	if _, err := parseE2ESteps(*steps, *skipRemove, false); err != nil {
		return err
	}
	if !*skipRemove && !hasFlag(args, "--resume") {
		*resume = false
	}
	ctx, err := resolveStagingRuntimeContext(*workspaceFlag, *stackFileFlag, *envRootFlag)
	if err != nil {
		return err
	}

	if *planMode {
		if ctx.provider == "lke" {
			missing := missingLKEImageEnvKeys()
			if len(missing) > 0 {
				if env, source := stackLKEImageEnv(ctx.envRoot); lkeImageEnvHasKeys(env, missing) {
					fmt.Fprintf(os.Stderr, "[cloud-staging-e2e] plan: LKE image env will be loaded from %s\n", source)
				} else if env, source := existingLKEImageEnv(ctx.envRoot); lkeImageEnvHasKeys(env, missing) {
					fmt.Fprintf(os.Stderr, "[cloud-staging-e2e] plan: LKE image env will be loaded from %s\n", source)
				} else {
					fmt.Fprintf(os.Stderr, "[cloud-staging-e2e] plan: LKE image resolve will run before provision because these env vars are missing: %s\n", strings.Join(missing, ","))
				}
			}
		}
		return runStagingE2ETest(stagingE2ETestArgs(stagingE2EArgs{
			workspace: ctx.workspace, envRoot: ctx.envRoot, stackName: ctx.stackName, run: false, plan: true,
			brandname: *brandname, userCount: *userCount, userEmailPrefix: *userEmailPrefix, userEmailDomain: *userEmailDomain, deviceCount: *deviceCount, deviceMix: *deviceMix, devicePrefix: *devicePrefix,
			userConcurrency: *userConcurrency, deviceConcurrency: *deviceConcurrency, bindConcurrency: *bindConcurrency,
			outDir: *outDir, skipMQTTProbe: *skipMQTTProbe, skipRemove: *skipRemove, purgeStorage: *purgeStorage, quiet: *quiet, resume: *resume,
			steps: *steps,
		}))
	}
	if *confirm != ctx.stackName {
		if *confirm == "" {
			return fmt.Errorf("--confirm %s is required before deleting and redeploying staging", ctx.stackName)
		}
		return fmt.Errorf("--confirm must be %s, got %s", ctx.stackName, *confirm)
	}
	if ctx.provider == "lke" {
		if err := resolveLKEImagesIfNeeded(ctx.workspace, ctx.envRoot); err != nil {
			return err
		}
	}
	runOutDir := *outDir
	if runOutDir == "" {
		runOutDir = filepath.Join(ctx.envRoot, "artifacts", "staging-e2e", time.Now().UTC().Format("20060102T150405Z"))
	}
	err = runStagingE2ETest(stagingE2ETestArgs(stagingE2EArgs{
		workspace: ctx.workspace, envRoot: ctx.envRoot, stackName: ctx.stackName, run: true, plan: false,
		brandname: *brandname, userCount: *userCount, userEmailPrefix: *userEmailPrefix, userEmailDomain: *userEmailDomain, deviceCount: *deviceCount, deviceMix: *deviceMix, devicePrefix: *devicePrefix,
		userConcurrency: *userConcurrency, deviceConcurrency: *deviceConcurrency, bindConcurrency: *bindConcurrency,
		outDir: runOutDir, skipMQTTProbe: *skipMQTTProbe, skipRemove: *skipRemove, purgeStorage: *purgeStorage, quiet: *quiet, resume: *resume,
		steps: *steps,
	}))
	if reportErr := writeStagingInstallReport(ctx.provider, filepath.Join(runOutDir, "summary.json"), filepath.Join(runOutDir, "test_report.md"), runOutDir); reportErr != nil && err == nil {
		err = reportErr
	}
	if err == nil {
		printStagingFinalReportPaths(runOutDir)
	}
	return err
}

type stagingE2EArgs struct {
	workspace         string
	envRoot           string
	stackName         string
	run               bool
	plan              bool
	brandname         string
	userCount         int
	userEmailPrefix   string
	userEmailDomain   string
	deviceCount       int
	deviceMix         string
	devicePrefix      string
	userConcurrency   int
	deviceConcurrency int
	bindConcurrency   int
	outDir            string
	skipMQTTProbe     bool
	skipRemove        bool
	purgeStorage      bool
	skipProvision     bool
	quiet             bool
	resume            bool
	confirmOverride   string
	steps             string
}

func stagingE2ETestArgs(cfg stagingE2EArgs) []string {
	out := []string{"--workspace", cfg.workspace, "--env-root", cfg.envRoot}
	if cfg.run {
		confirm := firstNonEmpty(cfg.confirmOverride, cfg.stackName)
		out = append(out, "--run", "--confirm", confirm)
	}
	if cfg.plan {
		out = append(out, "--plan")
	}
	out = append(out,
		"--brandname", cfg.brandname,
		"--user-count", strconv.Itoa(cfg.userCount),
		"--device-count", strconv.Itoa(cfg.deviceCount),
		"--device-mix", cfg.deviceMix,
		"--device-prefix", cfg.devicePrefix,
		"--user-concurrency", strconv.Itoa(cfg.userConcurrency),
		"--device-concurrency", strconv.Itoa(cfg.deviceConcurrency),
		"--bind-concurrency", strconv.Itoa(cfg.bindConcurrency),
	)
	if strings.TrimSpace(cfg.userEmailPrefix) != "" {
		out = append(out, "--user-email-prefix", cfg.userEmailPrefix)
	}
	if strings.TrimSpace(cfg.userEmailDomain) != "" {
		out = append(out, "--user-email-domain", cfg.userEmailDomain)
	}
	if cfg.outDir != "" {
		out = append(out, "--out-dir", cfg.outDir)
	}
	if strings.TrimSpace(cfg.steps) != "" && strings.TrimSpace(cfg.steps) != "all" {
		out = append(out, "--steps", cfg.steps)
	}
	if cfg.skipMQTTProbe {
		out = append(out, "--skip-mqtt-probe")
	}
	if cfg.skipRemove {
		out = append(out, "--skip-remove")
	}
	if cfg.purgeStorage {
		out = append(out, "--purge-storage")
	}
	if cfg.skipProvision {
		out = append(out, "--skip-provision")
	}
	if cfg.quiet {
		out = append(out, "--quiet")
	}
	if !cfg.resume {
		out = append(out, "--no-resume")
	}
	return out
}

func missingLKEImageEnvKeys() []string {
	keys := []string{
		"LKE_VIDEO_CLOUD_IMAGE",
		"LKE_ACCOUNT_MANAGER_IMAGE",
		"LKE_CLOUD_ADMIN_IMAGE",
		"LKE_FRONTEND_IMAGE",
		"LKE_CLOUD_LOGGER_IMAGE",
	}
	missing := []string{}
	for _, key := range keys {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			missing = append(missing, key)
		}
	}
	return missing
}

func resolveLKEImagesIfNeeded(workspace, envRoot string) error {
	missing := missingLKEImageEnvKeys()
	if len(missing) == 0 {
		return nil
	}
	if stackEnv, source := stackLKEImageEnv(envRoot); lkeImageEnvHasKeys(stackEnv, missing) {
		for _, key := range missing {
			if err := os.Setenv(key, stackEnv[key]); err != nil {
				return err
			}
		}
		fmt.Fprintf(os.Stderr, "[cloud-staging-e2e] use: lke_image_env source=%s keys=%s\n", source, strings.Join(missing, ","))
		return nil
	}
	if env, source := existingLKEImageEnv(envRoot); lkeImageEnvHasKeys(env, missing) {
		if err := validateExistingLKEImageEnvAgainstStack(envRoot, env, source, missing); err != nil {
			return err
		}
		for _, key := range missing {
			if err := os.Setenv(key, env[key]); err != nil {
				return err
			}
		}
		fmt.Fprintf(os.Stderr, "[cloud-staging-e2e] use: lke_image_env source=%s keys=%s\n", source, strings.Join(missing, ","))
		return nil
	}
	ts := time.Now().UTC().Format("20060102T150405Z")
	imageDir := filepath.Join(envRoot, "artifacts", "lke-images", ts)
	latestDir := filepath.Join(envRoot, "artifacts", "lke-images")
	manifestFile := filepath.Join(imageDir, "lke-image-manifest.json")
	envFile := filepath.Join(imageDir, "lke-image-env.sh")
	if err := os.MkdirAll(imageDir, 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(latestDir, 0o755); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[cloud-staging-e2e] start: lke_resolve_images missing=%s out=%s\n", strings.Join(missing, ","), manifestFile)
	if err := runLKEResolveImages([]string{"--workspace", workspace, "--env-root", envRoot, "--out", manifestFile}); err != nil {
		return err
	}
	env, err := readLKEImageManifestEnv(manifestFile)
	if err != nil {
		return err
	}
	if err := writeLKEImageEnvFile(envFile, env); err != nil {
		return err
	}
	for key, value := range env {
		if os.Getenv(key) == "" {
			if err := os.Setenv(key, value); err != nil {
				return err
			}
		}
	}
	if err := copyFileWithMode(manifestFile, filepath.Join(latestDir, "lke-image-manifest.json"), 0o644); err != nil {
		return err
	}
	if err := copyFileWithMode(envFile, filepath.Join(latestDir, "lke-image-env.sh"), 0o644); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[cloud-staging-e2e] pass: lke_resolve_images env=%s\n", envFile)
	return nil
}

func stackLKEImageEnv(envRoot string) (map[string]string, string) {
	source := filepath.Join(envRoot, "env", "stack.env")
	env, err := readEnvFile(source)
	if err != nil {
		return nil, ""
	}
	return env, source
}

func existingLKEImageEnv(envRoot string) (map[string]string, string) {
	latestDir := filepath.Join(envRoot, "artifacts", "lke-images")
	manifestFile := filepath.Join(latestDir, "lke-image-manifest.json")
	if env, err := readLKEImageManifestEnv(manifestFile); err == nil {
		return env, manifestFile
	}
	envFile := filepath.Join(latestDir, "lke-image-env.sh")
	if env, err := readEnvFile(envFile); err == nil {
		return env, envFile
	}
	return nil, ""
}

func validateExistingLKEImageEnvAgainstStack(envRoot string, imageEnv map[string]string, source string, keys []string) error {
	stackEnv, err := readEnvFile(filepath.Join(envRoot, "env", "stack.env"))
	if err != nil {
		return nil
	}
	for _, key := range keys {
		stackValue := strings.TrimSpace(stackEnv[key])
		imageValue := strings.TrimSpace(imageEnv[key])
		if stackValue == "" || imageValue == "" || stackValue == imageValue {
			continue
		}
		return fmt.Errorf("LKE image artifact mismatch for %s: env/stack.env=%q but %s=%q; refresh artifacts/lke-images/lke-image-manifest.json or remove the stale artifact before provisioning", key, stackValue, source, imageValue)
	}
	return nil
}

func lkeImageEnvHasKeys(env map[string]string, keys []string) bool {
	if len(env) == 0 {
		return false
	}
	for _, key := range keys {
		if strings.TrimSpace(env[key]) == "" {
			return false
		}
	}
	return true
}

func readLKEImageManifestEnv(path string) (map[string]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var parsed struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("read LKE image manifest %s: %w", path, err)
	}
	return parsed.Env, nil
}

func writeLKEImageEnvFile(path string, env map[string]string) error {
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	var buf bytes.Buffer
	for _, key := range keys {
		fmt.Fprintf(&buf, "export %s=%s\n", key, shellQuoteArg(env[key]))
	}
	return os.WriteFile(path, buf.Bytes(), 0o644)
}

func copyFileWithMode(src, dst string, mode os.FileMode) error {
	body, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, body, mode)
}

func writeStagingInstallReport(provider, summaryFile, e2eReportFile, reportDir string) error {
	if summaryFile == "" || !exists(summaryFile) {
		return nil
	}
	body, err := os.ReadFile(summaryFile)
	if err != nil {
		return err
	}
	var summary struct {
		Overall     string            `json:"overall"`
		GeneratedAt string            `json:"generated_at"`
		EnvRoot     string            `json:"env_root"`
		Stack       string            `json:"stack"`
		Brandname   string            `json:"brandname"`
		Artifacts   map[string]string `json:"artifacts"`
		Steps       []e2eStep         `json:"steps"`
	}
	if err := json.Unmarshal(body, &summary); err != nil {
		return err
	}
	totalSeconds := int64(0)
	for _, step := range summary.Steps {
		totalSeconds += step.DurationSeconds
	}
	if e2eReportFile == "" {
		e2eReportFile = summary.Artifacts["report_file"]
	}
	var buf bytes.Buffer
	fmt.Fprintln(&buf, "# Staging Installation Report")
	fmt.Fprintln(&buf)
	fmt.Fprintf(&buf, "- Overall: %s\n", firstNonEmpty(summary.Overall, "unknown"))
	fmt.Fprintf(&buf, "- Provider: %s\n", provider)
	if summary.Stack != "" {
		fmt.Fprintf(&buf, "- Stack: %s\n", summary.Stack)
	}
	if summary.Brandname != "" {
		fmt.Fprintf(&buf, "- Brand: %s\n", summary.Brandname)
	}
	if summary.GeneratedAt != "" {
		fmt.Fprintf(&buf, "- Generated: %s\n", summary.GeneratedAt)
	}
	if summary.EnvRoot != "" {
		fmt.Fprintf(&buf, "- Env root: `%s`\n", summary.EnvRoot)
	}
	fmt.Fprintf(&buf, "- Total duration seconds: %d\n\n", totalSeconds)
	fmt.Fprintln(&buf, "## Segment Durations")
	fmt.Fprintln(&buf)
	fmt.Fprintln(&buf, "| Segment | Status | Duration seconds | Log |")
	fmt.Fprintln(&buf, "| --- | --- | ---: | --- |")
	for _, step := range summary.Steps {
		fmt.Fprintf(&buf, "| %s | %s | %d |", step.Name, step.Status, step.DurationSeconds)
		if step.LogFile != "" {
			fmt.Fprintf(&buf, " `%s`", step.LogFile)
		}
		fmt.Fprintln(&buf, " |")
	}
	fmt.Fprintln(&buf)
	fmt.Fprintln(&buf, "## Artifacts")
	fmt.Fprintln(&buf)
	fmt.Fprintf(&buf, "- Summary: `%s`\n", summaryFile)
	if e2eReportFile != "" {
		fmt.Fprintf(&buf, "- E2E report: `%s`\n", e2eReportFile)
	}
	if value := summary.Artifacts["data_setup_summary_file"]; value != "" {
		fmt.Fprintf(&buf, "- Data setup summary: `%s`\n", value)
	}
	fmt.Fprintf(&buf, "- Logs: `%s`\n", filepath.Join(reportDir, "logs"))
	if value := summary.Artifacts["bind_validation_dir"]; value != "" {
		fmt.Fprintf(&buf, "- Bind validation: `%s`\n", value)
	}
	fmt.Fprintf(&buf, "- MQTT report: `%s`\n", filepath.Join(reportDir, "home-mqtt", "test_report.md"))
	return os.WriteFile(filepath.Join(reportDir, "INSTALL_REPORT.md"), buf.Bytes(), 0o644)
}

func printStagingFinalReportPaths(reportDir string) {
	summaryFile := filepath.Join(reportDir, "summary.json")
	reportFile := filepath.Join(reportDir, "test_report.md")
	installReportFile := filepath.Join(reportDir, "INSTALL_REPORT.md")
	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "Final report paths:")
	if exists(summaryFile) {
		fmt.Fprintf(os.Stdout, "summary_file=%s\n", summaryFile)
	}
	if exists(reportFile) {
		fmt.Fprintf(os.Stdout, "report_file=%s\n", reportFile)
	}
	if exists(installReportFile) {
		fmt.Fprintf(os.Stdout, "install_report_file=%s\n", installReportFile)
	}
	fmt.Fprintf(os.Stdout, "logs_dir=%s\n", filepath.Join(reportDir, "logs"))
	if exists(summaryFile) {
		var summary struct {
			Artifacts map[string]string `json:"artifacts"`
		}
		if body, err := os.ReadFile(summaryFile); err == nil && json.Unmarshal(body, &summary) == nil {
			if value := summary.Artifacts["data_setup_summary_file"]; value != "" {
				fmt.Fprintf(os.Stdout, "data_setup_summary_file=%s\n", value)
			}
			if value := summary.Artifacts["bind_validation_dir"]; value != "" {
				fmt.Fprintf(os.Stdout, "bind_validation_dir=%s\n", value)
			}
		}
	}
	fmt.Fprintf(os.Stdout, "mqtt_report_file=%s\n", filepath.Join(reportDir, "home-mqtt", "test_report.md"))
}

type mqttLogVerifyResults struct {
	Overall string `json:"overall"`
	Devices []struct {
		DeviceID               string `json:"device_id"`
		MQTTStatus             string `json:"mqtt_status"`
		RuntimeLogStreamID     string `json:"runtime_log_stream_id"`
		RuntimeLogExpectations []struct {
			Seq     int    `json:"seq"`
			Source  string `json:"source"`
			Message string `json:"message"`
		} `json:"runtime_log_expectations"`
	} `json:"devices"`
}

type mqttLogExpectation struct {
	DeviceID string `json:"device_id"`
	StreamID string `json:"stream_id"`
	Seq      int    `json:"seq"`
	Source   string `json:"source"`
	Message  string `json:"message"`
}

func runStagingE2EMQTTLogVerify(args []string) error {
	fs := flag.NewFlagSet("staging-e2e-mqtt-log-verify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	target := fs.String("target", "k8s", "staging target")
	mqttResults := fs.String("mqtt-results", "", "cloud MQTT test results.json")
	outDir := fs.String("out-dir", "", "output directory")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *mqttResults == "" {
		return errors.New("--mqtt-results is required")
	}
	if *outDir == "" {
		return errors.New("--out-dir is required")
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	parsed := mqttLogVerifyResults{}
	rawResults, err := os.ReadFile(*mqttResults)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(rawResults, &parsed); err != nil {
		return err
	}
	if strings.ToLower(strings.TrimSpace(parsed.Overall)) != "pass" {
		return fmt.Errorf("MQTT test did not pass: %s", parsed.Overall)
	}
	expectations := []mqttLogExpectation{}
	for _, device := range parsed.Devices {
		if device.MQTTStatus != "" && device.MQTTStatus != "PASS" {
			continue
		}
		if strings.TrimSpace(device.DeviceID) == "" || strings.TrimSpace(device.RuntimeLogStreamID) == "" {
			continue
		}
		for _, item := range device.RuntimeLogExpectations {
			if item.Seq <= 0 || strings.TrimSpace(item.Source) == "" || strings.TrimSpace(item.Message) == "" {
				continue
			}
			expectations = append(expectations, mqttLogExpectation{DeviceID: device.DeviceID, StreamID: device.RuntimeLogStreamID, Seq: item.Seq, Source: item.Source, Message: item.Message})
		}
	}
	if len(expectations) == 0 {
		return errors.New("MQTT test results did not include runtime log expectations")
	}
	if strings.ToLower(strings.TrimSpace(*target)) != "k8s" {
		return fmt.Errorf("MQTT log verification requires k8s target, got %s", *target)
	}
	stack := firstNonEmpty(envFileValue(filepath.Join(envRoot, "env", "stack.env"), "CLOUD_STACK_NAME"), "video-cloud-staging")
	kubeconfig, err := ensureK8SKubeconfig(workspace, envRoot, stack)
	if err != nil {
		return err
	}
	verifyTimeout := envDurationDefault("CLOUD_STAGING_E2E_MQTT_LOG_VERIFY_TIMEOUT", 60*time.Second)
	missing, err := waitForK8SMQTTRuntimeLogs(kubeconfig, stack, expectations, verifyTimeout)
	if err != nil {
		return err
	}
	checkedDevices := map[string]bool{}
	for _, item := range expectations {
		checkedDevices[item.DeviceID] = true
	}
	overall := "pass"
	if len(missing) > 0 {
		overall = "fail"
	}
	summaryFile := filepath.Join(*outDir, "summary.json")
	summary := map[string]any{
		"overall":         overall,
		"generated_at":    time.Now().UTC().Format(time.RFC3339),
		"target":          "k8s",
		"mqtt_results":    *mqttResults,
		"timeout_seconds": int(verifyTimeout.Seconds()),
		"checked_devices": len(checkedDevices),
		"checked_logs":    len(expectations),
		"missing_logs":    missing,
	}
	if err := writeJSON(summaryFile, summary); err != nil {
		return err
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"overall": overall, "summary_file": summaryFile}); err != nil {
		return err
	}
	if overall != "pass" {
		return fmt.Errorf("missing %d persisted MQTT runtime logs", len(missing))
	}
	return nil
}

func waitForK8SMQTTRuntimeLogs(kubeconfig, stack string, expectations []mqttLogExpectation, timeout time.Duration) ([]mqttLogExpectation, error) {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var lastMissing []mqttLogExpectation
	for {
		missing, err := queryMissingK8SMQTTRuntimeLogs(kubeconfig, stack, expectations)
		if err != nil {
			return nil, err
		}
		if len(missing) == 0 || time.Now().After(deadline) {
			return missing, nil
		}
		lastMissing = missing
		time.Sleep(2 * time.Second)
		if time.Now().After(deadline) {
			return lastMissing, nil
		}
	}
}

func queryMissingK8SMQTTRuntimeLogs(kubeconfig, stack string, expectations []mqttLogExpectation) ([]mqttLogExpectation, error) {
	if len(expectations) == 0 {
		return nil, nil
	}
	_ = kubeconfig
	_ = stack
	endpoint := strings.TrimRight(firstNonEmpty(os.Getenv("VIDEO_CLOUD_LOGGER_ENDPOINT"), os.Getenv("CLOUD_LOGGER_ENDPOINT")), "/")
	if endpoint == "" {
		return expectations, fmt.Errorf("logger endpoint is required to verify MQTT runtime logs")
	}
	token := firstNonEmpty(os.Getenv("VIDEO_CLOUD_LOGGER_TOKEN"), os.Getenv("CLOUD_LOGGER_INGEST_TOKEN"))
	client := &http.Client{Timeout: 10 * time.Second}
	found := map[string]struct{}{}
	expectedByDevice := map[string][]mqttLogExpectation{}
	deviceIDs := []string{}
	for _, expected := range expectations {
		if _, ok := expectedByDevice[expected.DeviceID]; !ok {
			deviceIDs = append(deviceIDs, expected.DeviceID)
		}
		expectedByDevice[expected.DeviceID] = append(expectedByDevice[expected.DeviceID], expected)
	}
	for _, deviceID := range deviceIDs {
		values := url.Values{}
		// Loki keeps device_id, component, and source out of labels to avoid
		// unbounded cardinality. Restrict the label selector to the video-cloud
		// service and dedicated ingester unit before those fields are post-filtered.
		// Otherwise busy clusters can push expected records outside Loki's
		// candidate limit.
		values.Set("service", "video_cloud")
		values.Set("unit", "video_cloud-logingester.service")
		values.Set("device_id", deviceID)
		values.Set("component", "device_runtime_log")
		values.Set("source", "device-runtime")
		values.Set("limit", "1000")
		req, err := http.NewRequest(http.MethodGet, endpoint+"/v1/logs?"+values.Encode(), nil)
		if err != nil {
			return nil, err
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		resp, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		var parsed struct {
			Events []struct {
				Message string         `json:"msg"`
				Fields  map[string]any `json:"fields"`
			} `json:"events"`
		}
		decodeErr := json.NewDecoder(resp.Body).Decode(&parsed)
		_ = resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, fmt.Errorf("logger query status %d", resp.StatusCode)
		}
		if decodeErr != nil {
			return nil, decodeErr
		}
		for _, event := range parsed.Events {
			streamID, _ := event.Fields["stream_id"].(string)
			source, _ := event.Fields["source"].(string)
			seq := intFromJSONNumber(event.Fields["seq"])
			for _, expected := range expectedByDevice[deviceID] {
				if streamID == expected.StreamID && source == expected.Source && seq == expected.Seq && event.Message == expected.Message {
					found[expected.key()] = struct{}{}
				}
			}
		}
	}
	missing := []mqttLogExpectation{}
	for _, expected := range expectations {
		if _, ok := found[expected.key()]; !ok {
			missing = append(missing, expected)
		}
	}
	return missing, nil
}

func (e mqttLogExpectation) key() string {
	return strings.Join([]string{e.DeviceID, e.StreamID, strconv.Itoa(e.Seq), e.Source, e.Message}, "\x00")
}

func intFromJSONNumber(value any) int {
	switch v := value.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case uint64:
		return int(v)
	default:
		return 0
	}
}

func sqlLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func printE2EPlan(workspace, envRoot, stack, phase, brandname string, userCount, deviceCount int, deviceMix string, userConcurrency, deviceConcurrency, bindConcurrency int, skipRemove, skipProvision bool, selectedSteps string, scripts map[string]string) {
	fmt.Fprintln(os.Stdout, "cloud-environment-e2e-test plan")
	fmt.Fprintf(os.Stdout, "workspace: %s\n", workspace)
	fmt.Fprintf(os.Stdout, "env_root: %s\n", envRoot)
	fmt.Fprintf(os.Stdout, "stack: %s\n", stack)
	fmt.Fprintln(os.Stdout, "target: k8s")
	fmt.Fprintf(os.Stdout, "phase: %s\n", phase)
	fmt.Fprintf(os.Stdout, "brandname: %s\n", brandname)
	fmt.Fprintf(os.Stdout, "user_count: %d\n", userCount)
	fmt.Fprintf(os.Stdout, "device_count: %d\n", deviceCount)
	fmt.Fprintf(os.Stdout, "device_mix: %s\n", deviceMix)
	fmt.Fprintf(os.Stdout, "user_concurrency: %d\n", userConcurrency)
	fmt.Fprintf(os.Stdout, "device_concurrency: %d\n", deviceConcurrency)
	fmt.Fprintf(os.Stdout, "bind_concurrency: %d\n", bindConcurrency)
	fmt.Fprintf(os.Stdout, "skip_remove: %v\n", skipRemove)
	fmt.Fprintf(os.Stdout, "skip_provision: %v\n", skipProvision)
	fmt.Fprintf(os.Stdout, "steps: %s\n", selectedSteps)
	fmt.Fprintln(os.Stdout, "steps:")
	selection, _ := parseE2ESteps(selectedSteps, skipRemove, skipProvision)
	if selection.Reset {
		fmt.Fprintf(os.Stdout, "  - reset environment K8s with %s\n", displayCommand(scripts["remove-k8s"]))
	}
	if selection.Provision {
		fmt.Fprintf(os.Stdout, "  - provision environment K8s with %s\n", displayCommand(scripts["provision-k8s"]))
	}
	if selection.Data {
		fmt.Fprintf(os.Stdout, "  - setup brand/users/devices with %s\n", displayCommand(scripts["setup-data"]))
	}
	if selection.MQTT {
		fmt.Fprintf(os.Stdout, "  - run live home MQTT E2E with %s\n", displayCommand(scripts["mqtt-test"]))
	}
	if selection.RuntimeLogs {
		fmt.Fprintf(os.Stdout, "  - verify persisted MQTT runtime logs with %s\n", displayCommand(scripts["mqtt-log-verify"]))
	}
	if selection.BillingLog || selection.BillingDB {
		fmt.Fprintf(os.Stdout, "  - verify billing usage log/ledger with %s\n", displayCommand(scripts["billing-verify"]))
	}
}

type e2eStepOptions struct {
	Quiet bool
	Env   []string
}

func runE2EStep(name, logPath string, argv ...string) (e2eStep, error) {
	return runE2EStepWithOptions(name, logPath, e2eStepOptions{}, argv...)
}

func runE2EStepWithOptions(name, logPath string, options e2eStepOptions, argv ...string) (e2eStep, error) {
	start := time.Now()
	fmt.Fprintf(os.Stderr, "[cloud-staging-e2e] start: %s log=%s\n", name, logPath)
	if len(argv) == 0 {
		durationSeconds := int64(time.Since(start).Seconds())
		fmt.Fprintf(os.Stderr, "[cloud-staging-e2e] fail: %s duration_seconds=%d elapsed=%s\n", name, durationSeconds, formatDurationSeconds(durationSeconds))
		return e2eStep{Name: name, Status: "FAIL", ExitCode: 1, DurationSeconds: durationSeconds, LogFile: logPath}, errors.New("empty e2e command")
	}
	cmd := exec.Command(argv[0], argv[1:]...)
	if len(options.Env) > 0 {
		cmd.Env = append(os.Environ(), options.Env...)
	}
	logFile, err := os.Create(logPath)
	if err != nil {
		return e2eStep{}, err
	}
	defer logFile.Close()
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	rc := 0
	err = runE2ECommandWithProgress(cmd, name, logPath, start, options.Quiet)
	if err != nil {
		rc = 1
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			rc = exitErr.ExitCode()
		}
	}
	status := "PASS"
	if rc != 0 {
		status = "FAIL"
	}
	durationSeconds := int64(time.Since(start).Seconds())
	step := e2eStep{Name: name, Status: status, ExitCode: rc, DurationSeconds: durationSeconds, LogFile: logPath}
	if rc != 0 {
		fmt.Fprintf(os.Stderr, "[cloud-staging-e2e] fail: %s duration_seconds=%d elapsed=%s (see %s)\n", name, durationSeconds, formatDurationSeconds(durationSeconds), logPath)
		for _, line := range latestLogLines(logPath, e2eFailureTailLines()) {
			fmt.Fprintf(os.Stderr, "[cloud-staging-e2e] fail-log: %s %s\n", name, line)
		}
		return step, err
	}
	fmt.Fprintf(os.Stderr, "[cloud-staging-e2e] pass: %s duration_seconds=%d elapsed=%s\n", name, durationSeconds, formatDurationSeconds(durationSeconds))
	return step, nil
}

func runE2ECommandWithProgress(cmd *exec.Cmd, name, logPath string, start time.Time, quiet bool) error {
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() {
		done <- cmd.Wait()
	}()
	if quiet {
		return <-done
	}

	interval := e2eProgressInterval()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case err := <-done:
			return err
		case <-ticker.C:
			elapsed := time.Since(start)
			line := latestProgressLogLine(logPath, elapsed)
			if line == "" {
				continue
			}
			fmt.Fprintf(os.Stderr, "[cloud-staging-e2e] progress: %s elapsed=%s%s latest=%q log=%s\n", name, formatDurationSeconds(int64(elapsed.Seconds())), e2eProgressMetrics(line, elapsed), line, logPath)
		}
	}
}

func e2eFailureTailLines() int {
	const defaultLines = 40
	raw := strings.TrimSpace(os.Getenv("CLOUD_STAGING_E2E_FAILURE_TAIL_LINES"))
	if raw == "" {
		return defaultLines
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return defaultLines
	}
	return n
}

func e2eProgressInterval() time.Duration {
	const defaultInterval = 30 * time.Second
	raw := strings.TrimSpace(os.Getenv("CLOUD_STAGING_E2E_PROGRESS_INTERVAL"))
	if raw == "" {
		return defaultInterval
	}
	interval, err := time.ParseDuration(raw)
	if err != nil || interval <= 0 {
		return defaultInterval
	}
	return interval
}

func latestLogLine(path string) string {
	line, _ := latestLogLineFromTail(path, false, 0)
	return line
}

func latestProgressLogLine(path string, elapsed time.Duration) string {
	line, _ := latestLogLineFromTail(path, true, elapsed)
	return line
}

func latestLogLineFromTail(path string, preferProgress bool, elapsed time.Duration) (string, bool) {
	f, err := os.Open(path)
	if err != nil {
		return "", false
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return "", false
	}
	const maxTail = int64(64 * 1024)
	offset := int64(0)
	if st.Size() > maxTail {
		offset = st.Size() - maxTail
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return "", false
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return "", false
	}
	buf = bytes.TrimSpace(buf)
	if len(buf) == 0 {
		return "", false
	}
	lines := bytes.Split(buf, []byte{'\n'})
	latest := ""
	latestProgress := ""
	for _, raw := range lines {
		line := redactProgressLogLine(strings.TrimSpace(string(raw)))
		if line == "" {
			continue
		}
		latest = line
		if preferProgress && e2eProgressMetrics(line, elapsed) != "" {
			latestProgress = line
		}
	}
	if latestProgress != "" {
		return latestProgress, true
	}
	return latest, false
}

func latestLogLines(path string, count int) []string {
	if count <= 0 {
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	ring := make([]string, count)
	written := 0
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		ring[written%count] = redactProgressLogLine(line)
		written++
	}
	if written == 0 {
		return nil
	}
	n := written
	if n > count {
		n = count
	}
	out := make([]string, 0, n)
	start := written - n
	for i := 0; i < n; i++ {
		out = append(out, ring[(start+i)%count])
	}
	return out
}

func e2eProgressMetrics(line string, elapsed time.Duration) string {
	if elapsed <= 0 {
		return ""
	}
	re := regexp.MustCompile(`(?:done|completed|processed)=([0-9]+)/([0-9]+)`)
	m := re.FindStringSubmatch(line)
	if len(m) != 3 {
		return ""
	}
	done, errDone := strconv.Atoi(m[1])
	total, errTotal := strconv.Atoi(m[2])
	if errDone != nil || errTotal != nil || done <= 0 || total <= 0 || done > total {
		return ""
	}
	rateElapsed := elapsed
	if lineElapsed, ok := progressLineElapsed(line); ok && lineElapsed > 0 {
		rateElapsed = lineElapsed
	}
	rate := float64(done) / rateElapsed.Seconds()
	remaining := total - done
	eta := int64(0)
	if rate > 0 {
		eta = int64(float64(remaining) / rate)
	}
	return fmt.Sprintf(" done=%d/%d rate=%.2f/s eta=%s", done, total, rate, formatDurationSeconds(eta))
}

func shouldLogCountedProgress(done, total int) bool {
	if done <= 0 || total <= 0 {
		return false
	}
	if done == 1 || done == total {
		return true
	}
	interval := 1
	switch {
	case total > 10000:
		interval = 100
	case total > 1000:
		interval = 10
	}
	return done%interval == 0
}

func progressLineElapsed(line string) (time.Duration, bool) {
	re := regexp.MustCompile(`elapsed=([0-9]{2}):([0-9]{2}):([0-9]{2})`)
	m := re.FindStringSubmatch(line)
	if len(m) != 4 {
		return 0, false
	}
	hours, _ := strconv.Atoi(m[1])
	minutes, _ := strconv.Atoi(m[2])
	seconds, _ := strconv.Atoi(m[3])
	return time.Duration(hours)*time.Hour + time.Duration(minutes)*time.Minute + time.Duration(seconds)*time.Second, true
}

func redactProgressLogLine(line string) string {
	lower := strings.ToLower(line)
	for _, marker := range []string{"token", "password", "secret", "private key", "-----begin", "bearer "} {
		if strings.Contains(lower, marker) {
			return "[redacted sensitive log line]"
		}
	}
	return line
}

func formatDurationSeconds(seconds int64) string {
	if seconds < 0 {
		seconds = 0
	}
	hours := seconds / 3600
	minutes := (seconds % 3600) / 60
	secs := seconds % 60
	return fmt.Sprintf("%02d:%02d:%02d", hours, minutes, secs)
}

func boundedParallelMap[T any](count, concurrency int, fn func(int) (T, error)) ([]T, error) {
	if count < 0 {
		return nil, errors.New("parallel map count must not be negative")
	}
	if concurrency <= 0 {
		return nil, errors.New("parallel map concurrency must be greater than zero")
	}
	results := make([]T, count)
	if count == 0 {
		return results, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	jobs := make(chan int)
	errs := make(chan error, 1)
	var wg sync.WaitGroup
	workerCount := concurrency
	if workerCount > count {
		workerCount = count
	}
	for worker := 0; worker < workerCount; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-ctx.Done():
					return
				case i, ok := <-jobs:
					if !ok {
						return
					}
					result, err := fn(i)
					if err != nil {
						select {
						case errs <- err:
							cancel()
						default:
						}
						return
					}
					results[i] = result
				}
			}
		}()
	}
sendLoop:
	for i := 0; i < count; i++ {
		select {
		case <-ctx.Done():
			break sendLoop
		case jobs <- i:
		}
	}
	close(jobs)
	wg.Wait()
	select {
	case err := <-errs:
		return nil, err
	default:
	}
	return results, nil
}

func commandWithArgs(command string, args ...string) []string {
	out := strings.Split(command, "\x00")
	return append(out, args...)
}

func selfCommandPath(command string) string {
	exe, err := os.Executable()
	if err != nil {
		return "rtk-cloud"
	}
	return exe + "\x00" + command
}

func displayCommand(command string) string {
	return strings.Join(strings.Split(command, "\x00"), " ")
}

func latestMatchingFile(dir, pattern string) string {
	matches, _ := filepath.Glob(filepath.Join(dir, pattern))
	sort.Strings(matches)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

func latestMatchingFileWhere(dir, pattern string, match func(string) bool) string {
	matches, _ := filepath.Glob(filepath.Join(dir, pattern))
	sort.Strings(matches)
	for i := len(matches) - 1; i >= 0; i-- {
		if match(matches[i]) {
			return matches[i]
		}
	}
	return ""
}

func renderE2EReport(overall, envRoot, stack, brandname, testDataDB, bindValidationDir, dataSetupSummaryFile, mqttDir, mqttLogVerifySummaryFile string, steps []e2eStep) string {
	var b strings.Builder
	fmt.Fprintln(&b, "# Staging E2E Test Report")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "- Overall: %s\n", overall)
	fmt.Fprintf(&b, "- Generated: %s\n", time.Now().UTC().Format(time.RFC3339))
	fmt.Fprintf(&b, "- Env root: `%s`\n", envRoot)
	fmt.Fprintf(&b, "- Stack: `%s`\n", stack)
	fmt.Fprintf(&b, "- Brand: `%s`\n\n", brandname)
	fmt.Fprintln(&b, "## Steps")
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "| Step | Status | Duration seconds | Log |")
	fmt.Fprintln(&b, "| --- | --- | ---: | --- |")
	for _, step := range steps {
		fmt.Fprintf(&b, "| %s | %s | %d | `%s` |\n", step.Name, step.Status, step.DurationSeconds, step.LogFile)
	}
	fmt.Fprintln(&b)
	fmt.Fprintln(&b, "## Artifacts")
	fmt.Fprintln(&b)
	fmt.Fprintf(&b, "- Test data DB: `%s`\n", testDataDB)
	fmt.Fprintf(&b, "- Bind validation: `%s`\n", bindValidationDir)
	fmt.Fprintf(&b, "- Data setup summary: `%s`\n", dataSetupSummaryFile)
	fmt.Fprintf(&b, "- Home MQTT report: `%s`\n", filepath.Join(mqttDir, "test_report.md"))
	fmt.Fprintf(&b, "- Home MQTT results: `%s`\n", filepath.Join(mqttDir, "results.json"))
	fmt.Fprintf(&b, "- MQTT log verification summary: `%s`\n", mqttLogVerifySummaryFile)
	return b.String()
}

func containsSensitiveReportTerms(text string) bool {
	re := regexp.MustCompile(`(?i)password|bearer|raw-token|-----BEGIN|PRIVATE KEY|JWT_ACCESS_SECRET|VIDEO_CLOUD_AUTH_SECRET`)
	return re.MatchString(text)
}

func runCIRunnersList(args []string) error {
	fs := flag.NewFlagSet("ci-runners list", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	seen := map[string]bool{}
	for _, spec := range runner.Specs() {
		if seen[spec.Repo] {
			continue
		}
		seen[spec.Repo] = true
		fmt.Fprintf(os.Stdout, "== %s ==\n", spec.Repo)
		out, err := ghAPI("repos/" + spec.Repo + "/actions/runners")
		if err != nil {
			return err
		}
		var parsed struct {
			Runners []struct {
				Name   string `json:"name"`
				Status string `json:"status"`
				Busy   bool   `json:"busy"`
				Labels []struct {
					Name string `json:"name"`
				} `json:"labels"`
			} `json:"runners"`
		}
		if err := json.Unmarshal(out, &parsed); err != nil {
			return err
		}
		for _, r := range parsed.Runners {
			labels := []string{}
			for _, label := range r.Labels {
				labels = append(labels, label.Name)
			}
			row := map[string]any{"name": r.Name, "status": r.Status, "busy": r.Busy, "labels": labels}
			data, _ := json.Marshal(row)
			fmt.Fprintln(os.Stdout, string(data))
		}
		fmt.Fprintln(os.Stdout)
	}
	return nil
}

func runCIRunnersPower(args []string) error {
	fs := flag.NewFlagSet("ci-runners power", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New("usage: rtk-cloud ci-runners power start|stop|status")
	}
	action := fs.Arg(0)
	if action != "start" && action != "stop" && action != "status" {
		return errors.New("usage: rtk-cloud ci-runners power start|stop|status")
	}
	if os.Getenv("LINODE_TOKEN") == "" {
		return errors.New("LINODE_TOKEN is required")
	}
	type linodeVM struct {
		ID     int      `json:"id"`
		Label  string   `json:"label"`
		Status string   `json:"status"`
		IPv4   []string `json:"ipv4"`
	}
	vms, err := linodeGetList[linodeVM](os.Getenv("LINODE_TOKEN"), "/linode/instances?page_size=500")
	if err != nil {
		return err
	}
	byLabel := map[string]linodeVM{}
	for _, vm := range vms {
		byLabel[vm.Label] = vm
	}
	seenHosts := map[string]bool{}
	for _, spec := range runner.Specs() {
		if seenHosts[spec.HostLabel] {
			continue
		}
		seenHosts[spec.HostLabel] = true
		vm, ok := byLabel[spec.HostLabel]
		if !ok {
			fmt.Fprintf(os.Stdout, "%s\t%s\tmissing\n", spec.HostLabel, spec.Repo)
			continue
		}
		ipv4 := ""
		if len(vm.IPv4) > 0 {
			ipv4 = vm.IPv4[0]
		}
		switch action {
		case "start":
			if vm.Status == "running" {
				fmt.Fprintf(os.Stdout, "%s\talready-running\t%s\n", spec.HostLabel, ipv4)
			} else {
				if _, err := curlLinode("POST", fmt.Sprintf("/linode/instances/%d/boot", vm.ID), "{}"); err != nil {
					return err
				}
				fmt.Fprintf(os.Stdout, "%s\tboot-requested\t%s\n", spec.HostLabel, ipv4)
			}
		case "stop":
			if vm.Status == "offline" {
				fmt.Fprintf(os.Stdout, "%s\talready-offline\t%s\n", spec.HostLabel, ipv4)
			} else {
				if _, err := curlLinode("POST", fmt.Sprintf("/linode/instances/%d/shutdown", vm.ID), "{}"); err != nil {
					return err
				}
				fmt.Fprintf(os.Stdout, "%s\tshutdown-requested\t%s\n", spec.HostLabel, ipv4)
			}
		case "status":
			fmt.Fprintf(os.Stdout, "%s\t%s\t%s\n", spec.HostLabel, vm.Status, ipv4)
		}
	}
	return nil
}

func runCIRunnersWaitOnline(args []string) error {
	fs := flag.NewFlagSet("ci-runners wait-online", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		return err
	}
	timeout := time.Duration(envInt("CI_RUNNER_ONLINE_TIMEOUT_SECONDS", 900)) * time.Second
	sleep := time.Duration(envInt("CI_RUNNER_ONLINE_POLL_SECONDS", 15)) * time.Second
	deadline := time.Now().Add(timeout)
	for {
		missing := 0
		for _, spec := range runner.Specs() {
			status, _ := githubRunnerStatus(spec.Repo, spec.RunnerName)
			if status == "online" {
				fmt.Fprintf(os.Stdout, "online: %s (%s)\n", spec.RunnerName, spec.CustomLabel)
			} else {
				if status == "" {
					status = "missing"
				}
				fmt.Fprintf(os.Stdout, "waiting: %s (%s), current=%s\n", spec.RunnerName, spec.CustomLabel, status)
				missing++
			}
		}
		if missing == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return errors.New("timed out waiting for CI runners to become online")
		}
		time.Sleep(sleep)
	}
}

func githubRunnerStatus(repo, name string) (string, error) {
	out, err := ghAPI("repos/" + repo + "/actions/runners")
	if err != nil {
		return "", err
	}
	var parsed struct {
		Runners []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"runners"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return "", err
	}
	for _, r := range parsed.Runners {
		if r.Name == name {
			return r.Status, nil
		}
	}
	return "", nil
}

func ghAPI(path string) ([]byte, error) {
	cmd := exec.Command("gh", "api", path)
	cmd.Stderr = os.Stderr
	return cmd.Output()
}

func accountFindBrandCloud(ctx accountManagerContext, token, brandname string) (map[string]any, error) {
	logCreateUsers("checking brand cloud: name=%s", brandname)
	list, err := accountListBrandClouds(ctx, token, 200)
	if err != nil {
		return nil, err
	}
	for _, item := range anySlice(list["brand_clouds"]) {
		obj, _ := item.(map[string]any)
		metadata, _ := obj["metadata"].(map[string]any)
		if obj["name"] == brandname || metadata["brandname"] == brandname {
			return obj, nil
		}
	}
	return nil, fmt.Errorf("brand cloud not found: %s", brandname)
}

type accountCreateUserResult struct {
	Action string
	UserID string
	Role   string
}

func accountCreateUser(ctx accountManagerContext, session *accountPlatformSession, logf func(string, ...any), brandCloudID, email, displayName, password, role string, rotate bool) (accountCreateUserResult, error) {
	return accountCreateUserWithSessionLock(ctx, session, nil, logf, brandCloudID, email, displayName, password, role, rotate)
}

func accountCreateUserWithSessionLock(ctx accountManagerContext, session *accountPlatformSession, sessionMu *sync.Mutex, logf func(string, ...any), brandCloudID, email, displayName, password, role string, rotate bool) (accountCreateUserResult, error) {
	request := func(requestedRole string) ([]byte, int, error) {
		payload, _ := json.Marshal(map[string]any{"email": email, "password": password, "display_name": displayName, "role": requestedRole, "rotate_password": rotate, "activation_mode": "immediate"})
		return curlJSONStatusWithPlatformRetryLocked(ctx, session, sessionMu, logf, "brand user create", func(platformToken string) ([]byte, int, error) {
			return curlJSONStatus(fmt.Sprintf("%s/v1/admin/brand-clouds/%s/users", ctx.BaseURL, brandCloudID), platformToken, payload)
		})
	}
	body, status, err := request(role)
	if err != nil {
		return accountCreateUserResult{}, err
	}
	effectiveRole := role
	if status == http.StatusInternalServerError && role != "owner" {
		existingRole, lookupErr := accountBrandCloudUserRole(ctx, session, sessionMu, logf, brandCloudID, email)
		if lookupErr == nil && roleRank(existingRole) > roleRank(role) {
			return accountCreateUserResult{}, fmt.Errorf("refusing to replace higher existing membership role: email=%s requested=%s existing=%s; use --user-email-prefix for run-scoped load-test accounts", email, role, existingRole)
		}
	}
	if status != 200 && status != 201 {
		return accountCreateUserResult{}, fmt.Errorf("brand user create failed: email=%s HTTP %d", email, status)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return accountCreateUserResult{}, err
	}
	action := stringValue(parsed["action"])
	if action == "" {
		action = "assigned"
	}
	user, _ := parsed["user"].(map[string]any)
	return accountCreateUserResult{Action: action, UserID: stringValue(user["id"]), Role: effectiveRole}, nil
}

func accountBrandCloudUserRole(ctx accountManagerContext, session *accountPlatformSession, sessionMu *sync.Mutex, logf func(string, ...any), brandCloudID, email string) (string, error) {
	endpoint := fmt.Sprintf("%s/v1/admin/brand-clouds/%s/users?limit=1&q=%s", ctx.BaseURL, url.PathEscape(brandCloudID), url.QueryEscape(strings.ToLower(strings.TrimSpace(email))))
	body, status, err := curlJSONStatusWithPlatformRetryLocked(ctx, session, sessionMu, logf, "brand user lookup", func(platformToken string) ([]byte, int, error) {
		return curlJSONStatus(endpoint, platformToken, nil)
	})
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("brand user lookup failed: HTTP %d", status)
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	for _, item := range anySlice(parsed["users"]) {
		user, _ := item.(map[string]any)
		if strings.EqualFold(stringValue(user["email"]), email) {
			return stringValue(user["role"]), nil
		}
	}
	return "", nil
}

func roleRank(role string) int {
	switch strings.ToLower(strings.TrimSpace(role)) {
	case "owner":
		return 3
	case "admin":
		return 2
	case "member":
		return 1
	default:
		return 0
	}
}

type accountUserLoginResponse struct {
	User struct {
		ID    string `json:"id"`
		Email string `json:"email"`
	} `json:"user"`
	Tokens struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	} `json:"tokens"`
	AppCertificate accountAppCertificate `json:"app_certificate"`
}

type accountAppCertificate struct {
	Status              string `json:"status"`
	Subject             string `json:"subject,omitempty"`
	CertificatePEM      string `json:"certificate_pem,omitempty"`
	CertificateChainPEM string `json:"certificate_chain_pem,omitempty"`
	FingerprintSHA256   string `json:"fingerprint_sha256,omitempty"`
	SerialNumber        string `json:"serial_number,omitempty"`
	IssuerRequestID     string `json:"issuer_request_id,omitempty"`
	NotBefore           string `json:"not_before,omitempty"`
	NotAfter            string `json:"not_after,omitempty"`
}

var appCertificateRetrySleep = time.Sleep

func accountEnsureUserAppCertificate(ctx accountManagerContext, tenantSlug, email, password, subject string, bootstrapWithCSR bool, existingAppCredentials map[string]any, recoverMissingLocalCredentials func() error) (map[string]any, map[string]any, accountPlatformSession, error) {
	if strings.TrimSpace(subject) == "" {
		return nil, nil, accountPlatformSession{}, fmt.Errorf("app certificate subject is required for %s", email)
	}
	keyAlgorithms, err := appCertificateKeyAlgorithms(ctx)
	if err != nil {
		return nil, nil, accountPlatformSession{}, err
	}
	if bootstrapWithCSR {
		return accountIssueUserAppCertificate(ctx, tenantSlug, email, password, subject, "", keyAlgorithms)
	}
	initial, err := accountLoginUserFull(ctx, tenantSlug, email, password, "")
	if err != nil {
		return nil, nil, accountPlatformSession{}, err
	}
	switch initial.AppCertificate.Status {
	case "issued":
		if !hasLocalAppCredentials(existingAppCredentials) || !appCredentialsMatchCertificate(existingAppCredentials, initial.AppCertificate) {
			if recoverMissingLocalCredentials == nil {
				return nil, nil, accountPlatformSession{}, fmt.Errorf("app certificate already exists for %s but no matching local app private key was found in previous users artifacts; use the artifact that originally bootstrapped this user or revoke/rotate the app certificate before generating a new key", email)
			}
			logCreateUsers("revoking stale app certificate without matching local private key: email=%s", email)
			if err := recoverMissingLocalCredentials(); err != nil {
				return nil, nil, accountPlatformSession{}, err
			}
			initial, err = accountLoginUserFull(ctx, tenantSlug, email, password, "")
			if err != nil {
				return nil, nil, accountPlatformSession{}, err
			}
			if initial.AppCertificate.Status != "csr_required" {
				return nil, nil, accountPlatformSession{}, fmt.Errorf("app certificate recovery for %s did not return csr_required: status=%s", email, initial.AppCertificate.Status)
			}
			break
		}
		return existingAppCredentials, accountAppCertificateMap(initial.AppCertificate), accountPlatformSession{AccessToken: initial.Tokens.AccessToken, RefreshToken: initial.Tokens.RefreshToken}, nil
	case "csr_required":
	default:
		return nil, nil, accountPlatformSession{}, fmt.Errorf("login response included unexpected app certificate status for %s: %s", email, initial.AppCertificate.Status)
	}
	if initial.User.ID == "" {
		return nil, nil, accountPlatformSession{}, fmt.Errorf("login response did not include a user id for app certificate bootstrap: %s", email)
	}
	return accountIssueUserAppCertificate(ctx, tenantSlug, email, password, subject, initial.User.ID, keyAlgorithms)
}

func accountIssueUserAppCertificate(ctx accountManagerContext, tenantSlug, email, password, subject, _ string, keyAlgorithms []string) (map[string]any, map[string]any, accountPlatformSession, error) {
	if len(keyAlgorithms) == 0 {
		return nil, nil, accountPlatformSession{}, errors.New("app certificate key algorithm policy is empty")
	}
	var privateKeyPEM, csrPEM, keyAlgorithm string
	var issued accountUserLoginResponse
	var err error
	retryBudget := envInt("CLOUD_CREATE_USERS_APP_CERT_RETRIES", 12)
	for algorithmIndex, algorithm := range keyAlgorithms {
		keyAlgorithm = algorithm
		privateKeyPEM, csrPEM, err = generateAppCertificateCSRWithAlgorithm(subject, keyAlgorithm)
		if err != nil {
			return nil, nil, accountPlatformSession{}, err
		}
		issued, err = accountLoginUserFull(ctx, tenantSlug, email, password, csrPEM)
		for attempt := 1; shouldRetrySameAppCertificateSubject(err, subject) && attempt <= retryBudget; attempt++ {
			logCreateUsers("retrying app certificate after transient error: email=%s algorithm=%s attempt=%d", email, keyAlgorithm, attempt)
			appCertificateRetrySleep(time.Duration(2*attempt) * time.Second)
			issued, err = accountLoginUserFull(ctx, tenantSlug, email, password, csrPEM)
		}
		if err == nil || algorithmIndex == len(keyAlgorithms)-1 || !shouldFallbackAppCertificateAlgorithm(err, keyAlgorithm) {
			break
		}
		logCreateUsers("retrying app certificate with fallback key algorithm: email=%s algorithm=%s", email, keyAlgorithms[algorithmIndex+1])
	}
	if err != nil {
		return nil, nil, accountPlatformSession{}, err
	}
	if issued.AppCertificate.Status != "issued" {
		return nil, nil, accountPlatformSession{}, fmt.Errorf("app certificate was not issued for %s: status=%s", email, issued.AppCertificate.Status)
	}
	if strings.TrimSpace(issued.AppCertificate.CertificatePEM) == "" || strings.TrimSpace(issued.AppCertificate.FingerprintSHA256) == "" {
		return nil, nil, accountPlatformSession{}, fmt.Errorf("app certificate response missing certificate material for %s", email)
	}
	return map[string]any{
		"subject":         subject,
		"key_algorithm":   keyAlgorithm,
		"private_key_pem": privateKeyPEM,
		"csr_pem":         csrPEM,
	}, accountAppCertificateMap(issued.AppCertificate), accountPlatformSession{AccessToken: issued.Tokens.AccessToken, RefreshToken: issued.Tokens.RefreshToken}, nil
}

func shouldRetrySameAppCertificateSubject(err error, subject string) bool {
	if err == nil || !strings.HasPrefix(subject, "app-user:") {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "HTTP 500") && strings.Contains(msg, "internal_error")
}

func shouldFallbackAppCertificateAlgorithm(err error, algorithm string) bool {
	if err == nil || strings.TrimSpace(algorithm) == "" {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "HTTP 500") && strings.Contains(msg, "internal_error")
}

func appCertificateKeyAlgorithms(ctx accountManagerContext) ([]string, error) {
	if legacy := strings.TrimSpace(os.Getenv("RTK_CLOUD_APP_CERT_KEY_ALGORITHM")); legacy != "" {
		algorithm, err := deploymentCertificateAlgorithm("RTK_CLOUD_APP_CERT_KEY_ALGORITHM", legacy)
		if err != nil {
			return nil, err
		}
		logCreateUsers("warning: RTK_CLOUD_APP_CERT_KEY_ALGORITHM is deprecated; configure CERTIFICATE_APP_CSR_KEY_ALGORITHMS in the deployment environment")
		return []string{algorithm}, nil
	}
	values := ctx.StackValues
	if values == nil && ctx.EnvRoot != "" {
		values, _ = readEnvFile(filepath.Join(ctx.EnvRoot, "env", "stack.env"))
	}
	return deploymentCertificateAlgorithms("CERTIFICATE_APP_CSR_KEY_ALGORITHMS", values["CERTIFICATE_APP_CSR_KEY_ALGORITHMS"])
}

func loadExistingUserAppCredentials(envRoot, slug string) map[string]map[string]any {
	out := map[string]map[string]any{}
	dir := filepath.Join(envRoot, "artifacts", "users")
	matches, _ := filepath.Glob(filepath.Join(dir, slug+"-users-*.json"))
	sort.Strings(matches)
	for i := len(matches) - 1; i >= 0; i-- {
		raw, err := os.ReadFile(matches[i])
		if err != nil {
			continue
		}
		var artifact struct {
			Users []struct {
				Email          string         `json:"email"`
				AppCredentials map[string]any `json:"app_credentials"`
			} `json:"users"`
		}
		if err := json.Unmarshal(raw, &artifact); err != nil {
			continue
		}
		for _, user := range artifact.Users {
			email := strings.ToLower(strings.TrimSpace(user.Email))
			if email == "" || out[email] != nil || !hasLocalAppCredentials(user.AppCredentials) {
				continue
			}
			out[email] = user.AppCredentials
		}
	}
	return out
}

func loadReusableLocalUsers(envRoot, slug string) map[string]map[string]any {
	out := map[string]map[string]any{}
	dir := filepath.Join(envRoot, "artifacts", "users")
	matches, _ := filepath.Glob(filepath.Join(dir, slug+"-users-*.json"))
	sort.Strings(matches)
	for i := len(matches) - 1; i >= 0; i-- {
		raw, err := os.ReadFile(matches[i])
		if err != nil {
			continue
		}
		var artifact struct {
			Users []map[string]any `json:"users"`
		}
		if err := json.Unmarshal(raw, &artifact); err != nil {
			continue
		}
		for _, user := range artifact.Users {
			email := strings.ToLower(strings.TrimSpace(stringValue(user["email"])))
			if email == "" || out[email] != nil || !hasReusableLocalUser(user) {
				continue
			}
			out[email] = cloneJSONMap(user)
		}
	}
	return out
}

func hasReusableLocalUser(user map[string]any) bool {
	if strings.TrimSpace(stringValue(user["email"])) == "" || strings.TrimSpace(stringValue(user["password"])) == "" {
		return false
	}
	appCredentials, _ := user["app_credentials"].(map[string]any)
	if !hasLocalAppCredentials(appCredentials) {
		return false
	}
	appCertificate, _ := user["app_certificate"].(map[string]any)
	if strings.ToLower(strings.TrimSpace(stringValue(appCertificate["status"]))) != "issued" {
		return false
	}
	if strings.TrimSpace(firstNonEmpty(stringValue(appCertificate["certificate_pem"]), stringValue(appCertificate["certificate_chain_pem"]))) == "" {
		return false
	}
	return strings.TrimSpace(stringValue(appCertificate["fingerprint_sha256"])) != ""
}

func cloneJSONMap(in map[string]any) map[string]any {
	raw, err := json.Marshal(in)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func uniqueUserCredentialsFile(artifactDir, slug string) string {
	base := fmt.Sprintf("%s-users-%s", slug, time.Now().UTC().Format("20060102T150405Z"))
	path := filepath.Join(artifactDir, base+".json")
	if !exists(path) {
		return path
	}
	for i := 2; ; i++ {
		path = filepath.Join(artifactDir, fmt.Sprintf("%s-%02d.json", base, i))
		if !exists(path) {
			return path
		}
	}
}

func hasLocalAppCredentials(credentials map[string]any) bool {
	if credentials == nil {
		return false
	}
	privateKey := strings.TrimSpace(stringValue(credentials["private_key_pem"]))
	csr := strings.TrimSpace(stringValue(credentials["csr_pem"]))
	return strings.HasPrefix(privateKey, "-----BEGIN ") &&
		strings.Contains(privateKey, "PRIVATE KEY-----") &&
		strings.HasPrefix(csr, "-----BEGIN CERTIFICATE REQUEST-----")
}

func appCredentialsMatchCertificate(credentials map[string]any, certificate accountAppCertificate) bool {
	if !hasLocalAppCredentials(credentials) {
		return false
	}
	certPEM := strings.TrimSpace(firstNonEmpty(certificate.CertificateChainPEM, certificate.CertificatePEM))
	if certPEM == "" {
		return false
	}
	privateKey := strings.TrimSpace(stringValue(credentials["private_key_pem"]))
	_, err := tls.X509KeyPair([]byte(certPEM), []byte(privateKey))
	return err == nil
}

func accountLoginUserFull(ctx accountManagerContext, _ string, email, password, csrPEM string) (accountUserLoginResponse, error) {
	payload := map[string]string{"email": email, "password": password}
	if strings.TrimSpace(csrPEM) != "" {
		payload["app_csr_pem"] = csrPEM
	}
	raw, _ := json.Marshal(payload)
	loginURL := ctx.BaseURL + "/v1/auth/login"
	body, status, err := curlJSONStatus(loginURL, "", raw)
	if err != nil {
		return accountUserLoginResponse{}, err
	}
	if status != 200 {
		return accountUserLoginResponse{}, fmt.Errorf("login failed during app certificate bootstrap: email=%s HTTP %d%s", email, status, accountAPIErrorSuffix(body))
	}
	var parsed accountUserLoginResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return accountUserLoginResponse{}, err
	}
	if parsed.Tokens.AccessToken == "" {
		return accountUserLoginResponse{}, fmt.Errorf("login response did not include an access token: %s", email)
	}
	return parsed, nil
}

func accountAPIErrorSuffix(body []byte) string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return fmt.Sprintf(": %s", truncateForLog(string(body), 240))
	}
	parts := []string{}
	if nested, ok := parsed["error"].(map[string]any); ok {
		parsed = nested
	}
	for _, key := range []string{"code", "error", "message", "detail"} {
		value := strings.TrimSpace(stringValue(parsed[key]))
		if value != "" {
			parts = append(parts, fmt.Sprintf("%s=%s", key, truncateForLog(value, 160)))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return ": " + strings.Join(parts, " ")
}

func accountAPIErrorCode(body []byte) string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	if nested, ok := parsed["error"].(map[string]any); ok {
		return strings.TrimSpace(stringValue(firstPresent(nested, "code", "error")))
	}
	return strings.TrimSpace(stringValue(firstPresent(parsed, "code", "error")))
}

func truncateForLog(value string, maxLen int) string {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\n", " "))
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	if maxLen <= 3 {
		return value[:maxLen]
	}
	return value[:maxLen-3] + "..."
}

func generateAppCertificateCSRWithAlgorithm(subject, algorithm string) (string, string, error) {
	key, keyPEM, err := newCertificatePrivateKey(algorithm)
	if err != nil {
		return "", "", err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: subject},
	}, key)
	if err != nil {
		return "", "", err
	}
	csrPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))
	return keyPEM, csrPEM, nil
}

func newCertificatePrivateKey(algorithm string) (crypto.Signer, string, error) {
	switch strings.ToLower(strings.TrimSpace(algorithm)) {
	case "ed25519":
		_, key, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, "", err
		}
		der, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			return nil, "", err
		}
		return key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), nil
	case "p256":
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, "", err
		}
		der, err := x509.MarshalECPrivateKey(key)
		if err != nil {
			return nil, "", err
		}
		return key, string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: der})), nil
	default:
		return nil, "", fmt.Errorf("unsupported certificate key algorithm: %s", algorithm)
	}
}

func accountAppCertificateMap(cert accountAppCertificate) map[string]any {
	return map[string]any{
		"status":                cert.Status,
		"subject":               cert.Subject,
		"certificate_pem":       cert.CertificatePEM,
		"certificate_chain_pem": cert.CertificateChainPEM,
		"fingerprint_sha256":    cert.FingerprintSHA256,
		"serial_number":         cert.SerialNumber,
		"issuer_request_id":     cert.IssuerRequestID,
		"not_before":            cert.NotBefore,
		"not_after":             cert.NotAfter,
	}
}

func plannedUsers(brandname, slug, role string, count int) []map[string]any {
	return plannedUsersWithPrefix(brandname, slug, role, count, "")
}

func plannedUsersWithPrefix(brandname, slug, role string, count int, emailPrefix string) []map[string]any {
	return plannedUsersWithPrefixAndDomain(brandname, slug, role, count, emailPrefix, "users.local")
}

func plannedUsersWithPrefixAndDomain(brandname, slug, role string, count int, emailPrefix, emailDomain string) []map[string]any {
	emailPrefix = brandSlug(firstNonEmpty(strings.TrimSpace(emailPrefix), slug))
	emailDomain = strings.ToLower(strings.TrimSpace(emailDomain))
	if emailDomain == "" {
		emailDomain = "users.local"
	}
	users := make([]map[string]any, 0, count)
	for i := 1; i <= count; i++ {
		suffix := fmt.Sprintf("%03d", i)
		email := fmt.Sprintf("%s+%s@%s", emailPrefix, suffix, emailDomain)
		if role != "member" {
			email = fmt.Sprintf("%s+%s-%s@%s", emailPrefix, role, suffix, emailDomain)
		}
		users = append(users, map[string]any{
			"email":        email,
			"display_name": fmt.Sprintf("%s User %s", brandname, suffix),
			"role":         role,
		})
	}
	return users
}

func loadOwnerAdminBaseURL(stackEnv map[string]string) (string, error) {
	environment := strings.ToLower(strings.TrimSpace(stackEnv["CLOUD_ENV_NAME"]))
	stack := strings.ToLower(strings.TrimSpace(stackEnv["CLOUD_STACK_NAME"]))
	dnsRoot := strings.ToLower(strings.Trim(strings.TrimSpace(stackEnv["CLOUD_DNS_ROOT_DOMAIN"]), "."))
	if environment != "staging" || !strings.Contains(stack, "staging") || stack == "" || dnsRoot == "" {
		return "", errors.New("load-owner activation requires a staging stack and DNS root")
	}
	expectedDomain := "admin." + stack + "." + dnsRoot
	domain := strings.ToLower(strings.TrimSpace(firstNonEmpty(stackEnv["CLOUD_ADMIN_DOMAIN"], expectedDomain)))
	adminBaseURL := "https://" + domain
	parsed, err := url.Parse(adminBaseURL)
	if err != nil || parsed.Scheme != "https" || parsed.User != nil || parsed.Port() != "" ||
		parsed.Path != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		!strings.EqualFold(parsed.Hostname(), expectedDomain) {
		return "", errors.New("staging Cloud Admin HTTPS origin is unavailable or does not match the runtime stack")
	}
	return adminBaseURL, nil
}

func runActivateLoadOwner(args []string) error {
	fs := flag.NewFlagSet("activate-load-owner", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	brandname := fs.String("brandname", "", "resolved run-scoped Brand Cloud name")
	email := fs.String("email", "", "owner plus-alias recipient")
	displayName := fs.String("display-name", "", "owner display name")
	runID := fs.String("run-id", "", "load run ID")
	evidencePath := fs.String("evidence-path", "", "redacted evidence output")
	operatorEnvFile := fs.String("operator-env-file", defaultDeploymentSharedCredentialFile(), "operator credential profile containing IMAP settings")
	resume := fs.Bool("resume", false, "reuse only a matching verified owner artifact from this run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if hasFlag(args, "--operator-env-file") && !rtkCloudTestMode() {
		return errors.New("--operator-env-file is retired; store IMAP settings in the environment SecretStore operator/env directory")
	}
	for name, value := range map[string]string{
		"--brandname": *brandname, "--email": *email, "--display-name": *displayName, "--run-id": *runID,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s is required", name)
		}
	}
	if !regexp.MustCompile(`^[a-z0-9-]{8,64}$`).MatchString(*runID) {
		return errors.New("--run-id must use lowercase letters, digits, and hyphens")
	}
	workspace := *workspaceFlag
	if workspace == "" {
		var err error
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	ctx, err := accountManagerContextFromFlags(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	defer ctx.Close()
	if *evidencePath == "" {
		*evidencePath = filepath.Join(ctx.EnvRoot, "artifacts", "load-owner-activation", *runID, brandSlug(*brandname)+".json")
	}
	if *resume {
		reused, reuseErr := reuseVerifiedLoadOwner(ctx, *brandname, *email, *runID, *evidencePath)
		if reuseErr != nil {
			return reuseErr
		}
		if reused {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{
				"status": "PASS", "action": "resumed", "run_id": *runID, "brandname": *brandname,
				"activated_owners": 1, "evidence_path": *evidencePath,
			})
		}
	}
	var operator map[string]string
	if rtkCloudTestMode() && strings.TrimSpace(*operatorEnvFile) != "" {
		operator, err = readEnvFile(*operatorEnvFile)
	} else {
		environment := firstNonEmpty(ctx.StackValues["CLOUD_ENV_NAME"], "staging")
		store, storeErr := newSecretStore("", environment)
		if storeErr != nil {
			return storeErr
		}
		operator, err = store.readOperator()
	}
	if err != nil {
		return fmt.Errorf("read canonical operator settings: %w", err)
	}
	childEnv := os.Environ()
	for _, key := range []string{"IMAP_SERVER", "IMAP_EMAIL_ADDR", "IMAP_EMAIL_PASSWORD", "IMAP_EMAIL_PORT", "IMAP_EMAIL_SECURITY", "IMAP_EMAIL_FOLDER"} {
		value := operator[key]
		if rtkCloudTestMode() {
			value = firstNonEmpty(os.Getenv(key), value)
		}
		if value == "" {
			return fmt.Errorf("missing operator IMAP setting: %s", key)
		}
		childEnv = append(childEnv, key+"="+value)
	}
	connectHost := operator["IMAP_CONNECT_HOST"]
	if rtkCloudTestMode() {
		connectHost = firstNonEmpty(os.Getenv("IMAP_CONNECT_HOST"), connectHost)
	}
	connectHost, err = resolveIMAPConnectHost(connectHost, firstNonEmpty(os.Getenv("IMAP_SERVER"), operator["IMAP_SERVER"]), net.LookupHost)
	if err != nil {
		return err
	}
	if connectHost != "" {
		childEnv = append(childEnv, "IMAP_CONNECT_HOST="+connectHost)
	}
	stackEnv, _ := readEnvFile(filepath.Join(ctx.EnvRoot, "env", "stack.env"))
	adminBaseURL, err := loadOwnerAdminBaseURL(stackEnv)
	if err != nil {
		return err
	}
	helper := filepath.Join(workspace, "repos", "rtk_account_manager", "scripts", "email_signup_imap.py")
	imapEnv := append(childEnv,
		"EMAIL_E2E_SIGNUP_EMAIL="+strings.TrimSpace(*email),
		"EMAIL_E2E_EXPECTED_FROM=no-reply@realtekconnect.com",
		"EMAIL_E2E_EXPECTED_SUBJECT=Verify your Realtek Connect account",
		"EMAIL_E2E_EXPECTED_PATH=/signup/verify",
		"AUTH_TOKEN_BASE_URL="+adminBaseURL,
	)
	snapshot, err := runIMAPJSON(helper, imapEnv, "snapshot")
	if err != nil {
		return err
	}
	uidStart := int(asFloat(snapshot["uid_next"]))
	if uidStart < 1 {
		return errors.New("IMAP snapshot did not return a valid UIDNEXT")
	}
	ownerEmail := strings.ToLower(strings.TrimSpace(*email))
	payload, _ := json.Marshal(map[string]string{"email": ownerEmail})
	body, status, err := curlJSONStatus(ctx.BaseURL+"/v1/auth/signup", "", payload)
	if err != nil {
		return err
	}
	if status != http.StatusAccepted {
		return fmt.Errorf("public owner signup failed: HTTP %d%s", status, accountAPIErrorSuffix(body))
	}
	var created map[string]any
	if err := json.Unmarshal(body, &created); err != nil {
		return fmt.Errorf("decode public owner signup response: %w", err)
	}
	globalUser := objectValue(created["user"])
	brandCloud := objectValue(created["brand_cloud"])
	userID := stringValue(globalUser["id"])
	brandCloudID := stringValue(brandCloud["id"])
	tenantSlug := stringValue(brandCloud["tenant_slug"])
	if userID == "" || brandCloudID == "" || tenantSlug == "" ||
		!strings.EqualFold(stringValue(globalUser["email"]), ownerEmail) ||
		stringValue(brandCloud["name"]) != ownerEmail {
		return errors.New("public signup response is missing the exact pending user and default owned cloud")
	}
	delivered, err := runIMAPJSON(helper, imapEnv, "wait", "--uid-start", strconv.Itoa(uidStart), "--timeout", firstNonEmpty(os.Getenv("LOAD_OWNER_IMAP_TIMEOUT"), "180"))
	if err != nil {
		return err
	}
	activationURL := stringValue(delivered["url"])
	imapUID := int(asFloat(delivered["uid"]))
	if activationURL == "" || imapUID < 1 {
		return errors.New("IMAP delivery did not contain a valid activation URL and UID")
	}
	password, err := randomPassword()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*evidencePath), 0o700); err != nil {
		return err
	}
	browserEnv := append(imapEnv,
		"LOAD_OWNER_ACTIVATION_URL="+activationURL,
		"LOAD_OWNER_PASSWORD="+password,
		"LOAD_OWNER_EMAIL="+strings.ToLower(strings.TrimSpace(*email)),
		"LOAD_OWNER_DISPLAY_NAME="+strings.TrimSpace(*displayName),
		"LOAD_OWNER_BRAND_NAME="+ownerEmail,
		"LOAD_OWNER_TENANT_SLUG="+tenantSlug,
		"LOAD_OWNER_ADMIN_BASE_URL="+adminBaseURL,
		"LOAD_OWNER_EVIDENCE_PATH="+*evidencePath,
		"LOAD_OWNER_RUN_ID="+*runID,
		"LOAD_OWNER_IMAP_UID="+strconv.Itoa(imapUID),
	)
	cmd := exec.Command("npm", "run", "e2e:load-owner-activation-live")
	cmd.Dir = filepath.Join(workspace, "repos", "rtk_cloud_admin", "web")
	cmd.Env = browserEnv
	if output, runErr := cmd.CombinedOutput(); runErr != nil {
		detail := strings.ReplaceAll(string(output), activationURL, "<redacted-activation-url>")
		detail = strings.ReplaceAll(detail, password, "<redacted-password>")
		return fmt.Errorf("owner browser activation failed: %s", truncateForLog(detail, 500))
	}
	verifiedLogin, err := accountLoginUserFull(ctx, tenantSlug, ownerEmail, password, "")
	if err != nil {
		return fmt.Errorf("login activated public owner: %w", err)
	}
	var renamed map[string]any
	status, err = (multicloudLiveHTTPClient{
		baseURL: strings.TrimRight(ctx.BaseURL, "/"),
		client:  &http.Client{Timeout: 30 * time.Second},
	}).json(context.Background(), http.MethodPatch, "/v1/developer/brand-clouds/"+url.PathEscape(brandCloudID), verifiedLogin.Tokens.AccessToken,
		map[string]any{"name": strings.TrimSpace(*brandname), "description": "Email-activated load-test owner cloud"}, *runID+"-rename-owner-cloud", &renamed)
	if err != nil || status != http.StatusOK || stringValue(objectValue(renamed["brand_cloud"])["id"]) != brandCloudID {
		return apiStatusError("rename email-activated owner cloud", status, err)
	}
	if err := rewriteLoadOwnerEvidenceBrandName(*evidencePath, ownerEmail, strings.TrimSpace(*brandname)); err != nil {
		return err
	}
	appCertificateSubject := "app-user:" + userID
	appKeyAlgorithms, err := appCertificateKeyAlgorithms(ctx)
	if err != nil {
		return err
	}
	appCredentials, appCertificate, ownerSession, err := accountIssueUserAppCertificate(
		ctx,
		tenantSlug,
		strings.ToLower(strings.TrimSpace(*email)),
		password,
		appCertificateSubject,
		userID,
		appKeyAlgorithms,
	)
	if err != nil {
		return err
	}
	user := map[string]any{
		"id": userID, "user_id": userID, "email": strings.ToLower(strings.TrimSpace(*email)), "display_name": strings.TrimSpace(*displayName),
		"role": "owner", "password": password, "access_token": ownerSession.AccessToken, "refresh_token": ownerSession.RefreshToken,
		"app_private_key_pem": stringValue(appCredentials["private_key_pem"]),
		"app_csr_pem":         stringValue(appCredentials["csr_pem"]),
		"app_certificate":     appCertificate,
	}
	store, err := openTestDataStore(ctx.EnvRoot, *brandname)
	if err != nil {
		return err
	}
	defer store.Close()
	if err := store.ReplaceUsers(*brandname, brandCloudID, tenantSlug, "owner", []map[string]any{user}); err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"status": "PASS", "run_id": *runID, "brandname": *brandname, "tenant_slug": tenantSlug,
		"activated_owners": 1, "imap_uid": imapUID, "evidence_path": *evidencePath,
	})
}

func rewriteLoadOwnerEvidenceBrandName(path, initialName, finalName string) error {
	var evidence map[string]any
	if err := readJSONFile(path, &evidence); err != nil {
		return fmt.Errorf("read owner activation evidence before rename: %w", err)
	}
	if stringValue(evidence["schema"]) != "rtk.load-owner-activation.evidence.v1" ||
		stringValue(evidence["status"]) != "PASS" ||
		stringValue(evidence["brand_name"]) != initialName {
		return errors.New("owner activation evidence does not match the public signup cloud")
	}
	evidence["brand_name"] = finalName
	return writeJSON(path, evidence)
}

func reuseVerifiedLoadOwner(ctx accountManagerContext, brandname, email, runID, evidencePath string) (bool, error) {
	raw, err := os.ReadFile(evidencePath)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	var evidence struct {
		Status         string `json:"status"`
		RunID          string `json:"run_id"`
		BrandName      string `json:"brand_name"`
		RecipientAlias string `json:"recipient_alias"`
		TenantSlug     string `json:"tenant_slug"`
	}
	if json.Unmarshal(raw, &evidence) != nil ||
		evidence.Status != "PASS" ||
		evidence.RunID != runID ||
		evidence.BrandName != brandname ||
		!strings.EqualFold(evidence.RecipientAlias, email) ||
		evidence.TenantSlug == "" {
		return false, errors.New("resume owner evidence does not match this run-scoped brand plan")
	}
	store, err := openTestDataStore(ctx.EnvRoot, brandname)
	if err != nil {
		return false, err
	}
	defer store.Close()
	var storedEmail, password string
	err = store.DB.QueryRow(`
		SELECT email, password
		FROM users
		WHERE brandname = ? AND role = 'owner'
	`, brandname).Scan(&storedEmail, &password)
	if errors.Is(err, sql.ErrNoRows) {
		return false, errors.New("resume evidence exists but the matching owner credential is absent")
	}
	if err != nil {
		return false, err
	}
	if !strings.EqualFold(storedEmail, email) || password == "" {
		return false, errors.New("resume owner credential does not match this run-scoped recipient")
	}
	login, err := accountLoginUserFull(ctx, evidence.TenantSlug, storedEmail, password, "")
	if err != nil {
		return false, errors.New("resume owner is not verified or cannot log in")
	}
	return login.User.ID != "", nil
}

func runIMAPJSON(helper string, env []string, args ...string) (map[string]any, error) {
	cmd := exec.Command("python3", append([]string{helper}, args...)...)
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("IMAP %s failed: %s", firstNonEmpty(firstString(args), "operation"), truncateForLog(stderr.String(), 300))
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, errors.New("IMAP helper returned invalid JSON")
	}
	return parsed, nil
}

func firstString(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func userHomeDir() string {
	value, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return value
}

func brandSlug(value string) string {
	value = strings.ToLower(value)
	var b strings.Builder
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	slug := strings.Trim(b.String(), "-")
	if slug == "" {
		return "brand"
	}
	return slug
}

func randomPassword() (string, error) {
	buf := make([]byte, 24)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf), nil
}

func logCreateUsers(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[cloud-create-users %s +%03ds] %s\n", time.Now().Format("15:04:05"), 0, fmt.Sprintf(format, args...))
}

func accountManagerContextFromFlags(workspaceFlag, envRootFlag string) (accountManagerContext, error) {
	workspace := workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return accountManagerContext{}, err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, envRootFlag)
	if err != nil {
		return accountManagerContext{}, err
	}
	accountEnv := firstExistingPath(filepath.Join(envRoot, "services", "account-manager", "account-manager.env"), filepath.Join(envRoot, "services", "account-manager", "account-manager-public-staging.env"))
	platformEnv := filepath.Join(envRoot, "services", "account-manager", "account-manager-platform-admin.env")
	stackEnv, _ := readEnvFile(filepath.Join(envRoot, "env", "stack.env"))
	domain := firstNonEmpty(envFileValue(accountEnv, "ACCOUNT_MANAGER_DOMAIN"), stackEnv["ACCOUNT_MANAGER_DOMAIN"], "account-manager.video-cloud-staging.realtekconnect.com")
	baseURL := strings.TrimRight(firstNonEmpty(os.Getenv("ACCOUNT_MANAGER_BASE_URL"), envFileValue(accountEnv, "ACCOUNT_MANAGER_BASE_URL")), "/")
	if baseURL == "" {
		baseURL = "https://" + domain
	}
	explicitAdminEmail := strings.TrimSpace(os.Getenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"))
	explicitAdminPassword := strings.TrimSpace(os.Getenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"))
	ctx := accountManagerContext{
		EnvRoot:          envRoot,
		StackValues:      stackEnv,
		BaseURL:          baseURL,
		AdminEmail:       firstNonEmpty(os.Getenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"), envFileValue(platformEnv, "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL")),
		AdminPassword:    firstNonEmpty(os.Getenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"), envFileValue(platformEnv, "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD")),
		PlatformAdminEnv: platformEnv,
	}
	if firstNonEmpty(os.Getenv("CLOUD_PROVIDER"), stackEnv["CLOUD_PROVIDER"]) == "lke" {
		stack := firstNonEmpty(stackEnv["CLOUD_STACK_NAME"], "video-cloud-staging")
		refreshRuntimeCredentials := os.Getenv("ACCOUNT_MANAGER_BASE_URL") == "" || lkeAccountManagerURLMatchesDomain(ctx.BaseURL, domain)
		if os.Getenv("ACCOUNT_MANAGER_BASE_URL") == "" {
			forwardURL, cleanup, err := lkeAccountManagerPortForward(envRoot, map[string]string{
				"CLOUD_STACK_NAME": stack,
			})
			if err != nil {
				return accountManagerContext{}, err
			}
			ctx.BaseURL = forwardURL
			ctx.cleanup = cleanup
		}
		if refreshRuntimeCredentials && (explicitAdminEmail == "" || explicitAdminPassword == "") {
			kubeconfig, err := lkeRuntimeKubeconfig(workspace, envRoot, stack)
			if err != nil {
				ctx.Close()
				return accountManagerContext{}, err
			}
			secretEnv, err := readK8SSecretEnv(
				kubeconfig,
				stack+"-account-manager",
				"account-manager-runtime",
				"ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL",
				"ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD",
			)
			if err != nil {
				ctx.Close()
				return accountManagerContext{}, fmt.Errorf("read Account Manager platform-admin credentials from LKE runtime secret: %w", err)
			}
			for _, item := range secretEnv {
				key, value, ok := strings.Cut(item, "=")
				if !ok {
					continue
				}
				switch key {
				case "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL":
					if explicitAdminEmail == "" {
						ctx.AdminEmail = value
					}
				case "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD":
					if explicitAdminPassword == "" {
						ctx.AdminPassword = value
					}
				}
			}
		}
	}
	return ctx, nil
}

func lkeAccountManagerURLMatchesDomain(baseURL, domain string) bool {
	parsed, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil {
		return false
	}
	return strings.EqualFold(parsed.Hostname(), strings.TrimSpace(domain))
}

func lkeRuntimeSecretValueFromFlags(workspaceFlag, envRootFlag, namespaceSuffix, secretName, key string) (string, error) {
	workspace := workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return "", err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, envRootFlag)
	if err != nil {
		return "", err
	}
	stackEnv, _ := readEnvFile(filepath.Join(envRoot, "env", "stack.env"))
	if firstNonEmpty(os.Getenv("CLOUD_PROVIDER"), stackEnv["CLOUD_PROVIDER"]) != "lke" {
		return "", errors.New("LKE runtime credentials require CLOUD_PROVIDER=lke")
	}
	stack := firstNonEmpty(stackEnv["CLOUD_STACK_NAME"], "video-cloud-staging")
	kubeconfig, err := lkeRuntimeKubeconfig(workspace, envRoot, stack)
	if err != nil {
		return "", err
	}
	items, err := readK8SSecretEnv(kubeconfig, stack+namespaceSuffix, secretName, key)
	if err != nil {
		return "", err
	}
	_, value, ok := strings.Cut(items[0], "=")
	if !ok || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("k8s secret %s/%s missing %s", stack+namespaceSuffix, secretName, key)
	}
	return value, nil
}

func lkeRuntimeKubeconfig(workspace, envRoot, stack string) (string, error) {
	candidates := []string{
		firstNonEmpty(os.Getenv("RTK_CLOUD_LKE_KUBECONFIG"), os.Getenv("LKE_KUBECONFIG"), os.Getenv("KUBECONFIG")),
		filepath.Join(envRoot, "state", "kubeconfig.yaml"),
		filepath.Join(workspace, ".artifacts", "kube", stack+"-lke.kubeconfig"),
	}
	for _, path := range candidates {
		if strings.TrimSpace(path) != "" && k8sKubeconfigReady(path) {
			return path, nil
		}
	}
	if strings.TrimSpace(os.Getenv("LINODE_TOKEN")) != "" {
		return downloadK8SKubeconfig(workspace, stack)
	}
	cmd := exec.Command(lkeKubectl(), "--request-timeout=5s", "get", "--raw=/readyz")
	if cmd.Run() == nil {
		return "", nil
	}
	return "", errors.New("current LKE kubeconfig is unavailable or expired; LINODE_TOKEN is required to refresh it")
}

func k8sKubeconfigReady(path string) bool {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return false
	}
	cmd := exec.Command(lkeKubectl(), "--kubeconfig", path, "--request-timeout=5s", "get", "--raw=/readyz")
	return cmd.Run() == nil
}

func runVideoCloudAdminToken(args []string) error {
	fs := flag.NewFlagSet("video-cloud-admin-token", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	ttl := fs.Duration("ttl", 30*time.Minute, "short-lived token TTL")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *ttl <= 0 || *ttl > time.Hour {
		return errors.New("--ttl must be greater than zero and no more than 1h")
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	token, err := videoCloudAdminTokenValue(workspace, *envRootFlag, *ttl)
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, token)
	return nil
}

func videoCloudAdminTokenValue(workspace, envRoot string, ttl time.Duration) (string, error) {
	secret, err := lkeRuntimeSecretValueFromFlags(workspace, envRoot, "-video-cloud", "video-cloud-runtime", "VIDEO_CLOUD_AUTH_SECRET")
	if err != nil {
		return "", err
	}
	cmd := exec.Command("go", "run", "./cmd/admin-token", "--ttl", ttl.String())
	cmd.Dir = filepath.Join(workspace, "repos", "rtk_video_cloud")
	cmd.Env = append(os.Environ(), "GOWORK=off", "VIDEO_CLOUD_AUTH_SECRET="+secret)
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return "", err
	}
	token := strings.TrimSpace(stdout.String())
	if token == "" {
		return "", errors.New("video-cloud admin token command returned an empty token")
	}
	return token, nil
}

func runCloudLoggerToken(args []string) error {
	fs := flag.NewFlagSet("cloud-logger-token", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	value, err := lkeRuntimeSecretValueFromFlags(*workspaceFlag, *envRootFlag, "-video-cloud", "video-cloud-runtime", "VIDEO_CLOUD_LOGGER_TOKEN")
	if err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, value)
	return nil
}

func runPlatformAdminToken(args []string) error {
	fs := flag.NewFlagSet("platform-admin-token", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	fs.StringVar(envRootFlag, "secrets-root", "", "deprecated env root")
	baseURL := fs.String("base-url", "", "Account Manager base URL override")
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, err := accountManagerContextFromFlags(*workspaceFlag, *envRootFlag)
	if err != nil {
		return err
	}
	if *baseURL != "" {
		ctx.BaseURL = strings.TrimRight(*baseURL, "/")
	}
	return writePlatformAdminToken(os.Stdout, ctx)
}

func writePlatformAdminToken(w io.Writer, ctx accountManagerContext) error {
	token, err := accountLogin(ctx, func(string, ...any) {})
	if err != nil {
		return err
	}
	fmt.Fprintln(w, token)
	return nil
}

type accountPlatformSession struct {
	AccessToken  string `json:"access_token,omitempty"`
	RefreshToken string `json:"refresh_token,omitempty"`
}

func accountLogin(ctx accountManagerContext, logf func(string, ...any)) (string, error) {
	session, err := accountLoginSession(ctx, logf)
	if err != nil {
		return "", err
	}
	return session.AccessToken, nil
}

func accountLoginSession(ctx accountManagerContext, logf func(string, ...any)) (accountPlatformSession, error) {
	if ctx.AdminEmail == "" || ctx.AdminPassword == "" {
		return accountPlatformSession{}, errors.New("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL and PASSWORD are required")
	}
	logf("logging in platform admin: username=%s url=%s/v1/auth/login", ctx.AdminEmail, ctx.BaseURL)
	payload, _ := json.Marshal(map[string]string{"email": ctx.AdminEmail, "password": ctx.AdminPassword})
	body, status, err := curlJSONStatus(ctx.BaseURL+"/v1/auth/login", "", payload)
	if err != nil {
		return accountPlatformSession{}, err
	}
	if status != 200 {
		return accountPlatformSession{}, fmt.Errorf("platform admin login failed: HTTP %d", status)
	}
	session, err := parsePlatformSession(body)
	if err != nil {
		return accountPlatformSession{}, err
	}
	if session.AccessToken == "" {
		return accountPlatformSession{}, errors.New("platform admin login response did not include an access token")
	}
	logf("platform admin login ok")
	return session, nil
}

func accountRefreshSession(ctx accountManagerContext, refreshToken string, logf func(string, ...any)) (accountPlatformSession, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return accountPlatformSession{}, errors.New("platform admin refresh token is empty")
	}
	logf("refreshing platform admin token: url=%s/v1/auth/refresh", ctx.BaseURL)
	payload, _ := json.Marshal(map[string]string{"refresh_token": refreshToken})
	body, status, err := curlJSONStatus(ctx.BaseURL+"/v1/auth/refresh", "", payload)
	if err != nil {
		return accountPlatformSession{}, err
	}
	if status != 200 {
		return accountPlatformSession{}, fmt.Errorf("platform admin token refresh failed: HTTP %d%s", status, accountAPIErrorSuffix(body))
	}
	session, err := parsePlatformSession(body)
	if err != nil {
		return accountPlatformSession{}, err
	}
	if session.AccessToken == "" || session.RefreshToken == "" {
		return accountPlatformSession{}, errors.New("platform admin refresh response did not include access and refresh tokens")
	}
	logf("platform admin token refresh ok")
	return session, nil
}

func parsePlatformSession(body []byte) (accountPlatformSession, error) {
	var parsed struct {
		Tokens struct {
			AccessToken  string `json:"access_token"`
			RefreshToken string `json:"refresh_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return accountPlatformSession{}, err
	}
	return accountPlatformSession{
		AccessToken:  parsed.Tokens.AccessToken,
		RefreshToken: parsed.Tokens.RefreshToken,
	}, nil
}

func accountListBrandClouds(ctx accountManagerContext, token string, limit int) (map[string]any, error) {
	body, status, err := curlJSONStatus(fmt.Sprintf("%s/v1/admin/brand-clouds?limit=%d", ctx.BaseURL, limit), token, nil)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("brand cloud list failed: HTTP %d", status)
	}
	var parsed map[string]any
	return parsed, json.Unmarshal(body, &parsed)
}

func accountCurrentUserID(ctx accountManagerContext, token string) (string, error) {
	body, status, err := curlJSONStatus(ctx.BaseURL+"/v1/me", token, nil)
	if err != nil {
		return "", err
	}
	if status != http.StatusOK {
		return "", fmt.Errorf("current user lookup failed: HTTP %d%s", status, accountAPIErrorSuffix(body))
	}
	var parsed struct {
		User struct {
			ID string `json:"id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", fmt.Errorf("parse current user response: %w", err)
	}
	userID := strings.TrimSpace(parsed.User.ID)
	if userID == "" {
		return "", errors.New("current user response did not include a global user ID")
	}
	return userID, nil
}

func accountCreateBrandCloud(ctx accountManagerContext, token, ownerUserID, brandname string) (map[string]any, int, error) {
	payload, _ := json.Marshal(map[string]any{"name": brandname, "owner_user_id": ownerUserID, "metadata": map[string]string{"brandname": brandname}})
	body, status, err := curlJSONStatus(ctx.BaseURL+"/v1/admin/brand-clouds", token, payload)
	if err != nil {
		return nil, status, err
	}
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	return parsed, status, nil
}

var rtkJSONHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			timeout := time.Duration(envInt("RTK_CLOUD_CURL_CONNECT_TIMEOUT", 10)) * time.Second
			dialer := net.Dialer{Timeout: timeout, KeepAlive: 30 * time.Second}
			return dialer.DialContext(ctx, network, address)
		},
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   256,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	},
}

func curlJSONStatus(url, bearer string, payload []byte) ([]byte, int, error) {
	retries := envInt("RTK_CLOUD_CURL_RETRIES", 3)
	if retries < 1 {
		retries = 1
	}
	maxTime := time.Duration(envInt("RTK_CLOUD_CURL_MAX_TIME", 60)) * time.Second
	method := http.MethodGet
	if payload != nil {
		method = http.MethodPost
	}
	var lastErr error
	for attempt := 1; attempt <= retries; attempt++ {
		reqCtx, cancel := context.WithTimeout(context.Background(), maxTime)
		var bodyReader io.Reader
		if payload != nil {
			bodyReader = bytes.NewReader(payload)
		}
		req, err := http.NewRequestWithContext(reqCtx, method, url, bodyReader)
		if err != nil {
			cancel()
			return nil, 0, err
		}
		if payload != nil {
			req.Header.Set("content-type", "application/json")
		}
		if bearer != "" {
			req.Header.Set("authorization", "Bearer "+bearer)
		}
		resp, err := rtkJSONHTTPClient.Do(req)
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			closeErr := resp.Body.Close()
			cancel()
			if readErr != nil {
				return nil, resp.StatusCode, readErr
			}
			if closeErr != nil {
				return nil, resp.StatusCode, closeErr
			}
			return body, resp.StatusCode, nil
		}
		cancel()
		lastErr = fmt.Errorf("HTTP request failed for %s: %w", url, err)
		if attempt < retries {
			time.Sleep(time.Duration(250*attempt*attempt) * time.Millisecond)
		}
	}
	return nil, 0, lastErr
}

func curlJSONStatusWithPlatformRetry(ctx accountManagerContext, session *accountPlatformSession, logf func(string, ...any), operation string, call func(string) ([]byte, int, error)) ([]byte, int, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if err := ensurePlatformSessionFresh(ctx, session, logf); err != nil {
		return nil, 0, err
	}
	body, status, err := call(session.AccessToken)
	if err != nil || status != http.StatusUnauthorized {
		return body, status, err
	}
	logf("%s got HTTP 401; refreshing platform admin token before retry", operation)
	if err := refreshOrLoginPlatformSession(ctx, session, logf); err != nil {
		return body, status, err
	}
	return call(session.AccessToken)
}

func curlJSONStatusWithPlatformRetryLocked(ctx accountManagerContext, session *accountPlatformSession, sessionMu *sync.Mutex, logf func(string, ...any), operation string, call func(string) ([]byte, int, error)) ([]byte, int, error) {
	if sessionMu == nil {
		return curlJSONStatusWithPlatformRetry(ctx, session, logf, operation, call)
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	sessionMu.Lock()
	if err := ensurePlatformSessionFresh(ctx, session, logf); err != nil {
		sessionMu.Unlock()
		return nil, 0, err
	}
	platformToken := session.AccessToken
	sessionMu.Unlock()
	body, status, err := call(platformToken)
	if err != nil || status != http.StatusUnauthorized {
		return body, status, err
	}
	logf("%s got HTTP 401; refreshing platform admin token before retry", operation)
	sessionMu.Lock()
	if err := refreshOrLoginPlatformSession(ctx, session, logf); err != nil {
		sessionMu.Unlock()
		return body, status, err
	}
	platformToken = session.AccessToken
	sessionMu.Unlock()
	return call(platformToken)
}

func ensurePlatformSessionFresh(ctx accountManagerContext, session *accountPlatformSession, logf func(string, ...any)) error {
	const refreshWindow = 2 * time.Minute
	if expiresAt, ok := jwtExpiresAt(session.AccessToken); session.AccessToken != "" && (!ok || time.Until(expiresAt) > refreshWindow) {
		return nil
	}
	return refreshOrLoginPlatformSession(ctx, session, logf)
}

func refreshOrLoginPlatformSession(ctx accountManagerContext, session *accountPlatformSession, logf func(string, ...any)) error {
	if expiresAt, ok := jwtExpiresAt(session.RefreshToken); session.RefreshToken != "" && (!ok || time.Now().Before(expiresAt)) {
		refreshed, err := accountRefreshSession(ctx, session.RefreshToken, logf)
		if err == nil {
			*session = refreshed
			return nil
		}
		logf("platform admin token refresh failed; falling back to login: %v", err)
	}
	loggedIn, err := accountLoginSession(ctx, logf)
	if err != nil {
		return err
	}
	*session = loggedIn
	return nil
}

func jwtExpiresAt(token string) (time.Time, bool) {
	parts := strings.Split(token, ".")
	if len(parts) < 2 {
		return time.Time{}, false
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, false
	}
	var claims struct {
		Exp float64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil || claims.Exp <= 0 {
		return time.Time{}, false
	}
	return time.Unix(int64(claims.Exp), 0), true
}

func accountBootstrap(ctx accountManagerContext) error {
	if strings.HasPrefix(ctx.BaseURL, "http://127.0.0.1:") || strings.HasPrefix(ctx.BaseURL, "http://localhost:") {
		logBrandCreate("platform-admin bootstrap handled by LKE runtime secret; skipping VM SSH bootstrap")
		return nil
	}
	if strings.TrimSpace(ctx.AdminEmail) == "" || strings.TrimSpace(ctx.AdminPassword) == "" {
		return errors.New("Account Manager K8s bootstrap credentials are required; provide ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL and ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD from the K8s runtime secret or run through staging-e2e-test port-forward setup")
	}
	logBrandCreate("using Account Manager K8s bootstrap credentials from env/runtime secret")
	return nil
}

func accountPostgresFallback(ctx accountManagerContext, brandname string) (string, error) {
	return "", fmt.Errorf("Account Manager PostgreSQL fallback is retired for K8s staging; fix the create-brandname-cloud API failure instead of using a VM database fallback for brandname=%s", brandname)
}

func logBrandList(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[cloud-brand-cloud-list %s +%03ds] %s\n", time.Now().Format("15:04:05"), 0, fmt.Sprintf(format, args...))
}

func logBrandCreate(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[cloud-brand-cloud %s +%03ds] %s\n", time.Now().Format("15:04:05"), 0, fmt.Sprintf(format, args...))
}

func mustWorkspace(flagValue string) string {
	if flagValue != "" {
		abs, _ := filepath.Abs(flagValue)
		return abs
	}
	workspace, _ := workspaceRoot()
	return workspace
}

func anySlice(value any) []any {
	if items, ok := value.([]any); ok {
		return items
	}
	return nil
}

func stringValue(value any) string {
	if value == nil {
		return ""
	}
	if s, ok := value.(string); ok {
		return s
	}
	return fmt.Sprint(value)
}

func videoCloudStatePath(envRoot string) string {
	stack := stackNameFromEnvRoot(envRoot)
	if stack == "" {
		stack = "video-cloud-staging"
	}
	return filepath.Join(envRoot, "state", stack+".state.json")
}

func stackNameFromEnvRoot(envRoot string) string {
	if stack := envFileValue(filepath.Join(envRoot, "env", "stack.env"), "CLOUD_STACK_NAME"); stack != "" {
		return stack
	}
	data, err := os.ReadFile(filepath.Join(envRoot, "topology", "video-cloud-staging.yaml"))
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "stack:") {
			return strings.Trim(strings.TrimSpace(strings.TrimPrefix(line, "stack:")), `"'`)
		}
	}
	return ""
}

func curlLinode(method, path, data string) ([]byte, error) {
	return curlLinodeWithStderr(method, path, data, os.Stderr)
}

func curlLinodeQuiet(method, path, data string) ([]byte, error) {
	return curlLinodeWithStderr(method, path, data, io.Discard)
}

func curlLinodeWithStderr(method, path, data string, stderr io.Writer) ([]byte, error) {
	args := []string{"-fsS", "-X", method, "https://api.linode.com/v4" + path, "-H", "Authorization: Bearer " + os.Getenv("LINODE_TOKEN"), "-H", "Content-Type: application/json"}
	if data != "" {
		args = append(args, "--data-binary", data)
	}
	cmd := exec.Command("curl", args...)
	cmd.Stderr = stderr
	out, err := cmd.Output()
	if err != nil {
		return out, fmt.Errorf("Linode API %s %s failed: %w", method, path, err)
	}
	return out, nil
}

func splitCSV(value string) []string {
	out := []string{}
	for _, part := range strings.Split(value, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func asFloat(value any) float64 {
	switch v := value.(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case json.Number:
		f, _ := v.Float64()
		return f
	default:
		f, _ := strconv.ParseFloat(fmt.Sprint(value), 64)
		return f
	}
}

type loadDeviceInput struct {
	Index          int
	Ordinal        int
	Type           loadDeviceType
	Prefix         string
	OutDir         string
	EnvRoot        string
	Brandname      string
	GenerateOnly   bool
	CAKey          *ecdsa.PrivateKey
	CACert         []byte
	DeviceDays     int
	FactoryURL     string
	FactoryAuthKey string
	ProductionJWT  string
	ProductID      string
	FactoryID      string
	LineID         string
	StationID      string
	FixtureID      string
	OperatorID     string
	BatchID        string
	SerialPrefix   string
	RunID          string
	Timeout        time.Duration
	ResultsPath    string
	KeyAlgorithms  []string
}

type factoryEnrollOutcome struct {
	OK         bool
	Retryable  bool
	HTTPStatus string
	ErrorText  string
	Serial     string
	RequestID  string
}

func writeLoadDevice(in loadDeviceInput) (generatedDevice, bool, error) {
	deviceID := fmt.Sprintf("%s-%04d", in.Prefix, in.Index)
	display := loadDisplayName(in.Type.Name, in.Ordinal)
	deviceDir := filepath.Join(in.OutDir, "devices", in.Type.Name, deviceID)
	bundleDir := filepath.Join(in.OutDir, "bundles", in.Type.Name)
	if err := os.MkdirAll(deviceDir, 0o755); err != nil {
		return generatedDevice{}, false, err
	}
	if err := os.MkdirAll(bundleDir, 0o755); err != nil {
		return generatedDevice{}, false, err
	}
	keyPath := filepath.Join(deviceDir, "device.key.pem")
	csrPath := filepath.Join(deviceDir, "device.csr.pem")
	certPath := filepath.Join(deviceDir, "device.cert.pem")
	chainPath := filepath.Join(deviceDir, "device.chain.pem")
	profile := "factory-enrolled-device-mtls-client"
	warning := "Factory-enrolled staging load-test credential. Keep private key material out of source control."
	if len(in.KeyAlgorithms) == 0 {
		return generatedDevice{}, false, errors.New("device certificate key algorithm policy is empty")
	}
	keyAlgorithm := in.KeyAlgorithms[0]
	if device, serial, requestID, ok := reusableLocalLoadDevice(in, deviceID, display, deviceDir, keyPath, csrPath, certPath, chainPath, filepath.Join(bundleDir, deviceID+".pem")); ok {
		logLoad("reusing local device artifact: index=%03d device=%s type=%s service_options=%s", in.Index, deviceID, in.Type.Name, strings.Join(in.Type.ServiceOptions, ","))
		if !in.GenerateOnly {
			recordEnrollResult(in.ResultsPath, "reused", in.Index, deviceID, in.Type.Name, in.Type.ServiceOptions, "local", requestID, serial, "")
		}
		return device, true, nil
	}
	if device, serial, requestID, ok := reusableSQLiteLoadDevice(in, deviceID, display, deviceDir, keyPath, csrPath, certPath, chainPath, filepath.Join(bundleDir, deviceID+".pem")); ok {
		logLoad("reusing SQLite device artifact: index=%03d device=%s type=%s service_options=%s", in.Index, deviceID, in.Type.Name, strings.Join(in.Type.ServiceOptions, ","))
		if !in.GenerateOnly {
			recordEnrollResult(in.ResultsPath, "reused", in.Index, deviceID, in.Type.Name, in.Type.ServiceOptions, "sqlite", requestID, serial, "")
		}
		return device, true, nil
	}
	key, err := writeDeviceKeyAndCSR(keyPath, csrPath, deviceID, in.Type.Name, keyAlgorithm)
	if err != nil {
		return generatedDevice{}, false, err
	}
	if in.GenerateOnly {
		logLoad("generate-only: index=%03d device=%s type=%s service_options=%s", in.Index, deviceID, in.Type.Name, strings.Join(in.Type.ServiceOptions, ","))
		certDER, err := signDeviceCert(deviceID, key, in.CAKey, in.CACert, in.DeviceDays)
		if err != nil {
			return generatedDevice{}, false, err
		}
		if err := writePEM(certPath, "CERTIFICATE", certDER, 0o644); err != nil {
			return generatedDevice{}, false, err
		}
		if err := os.WriteFile(chainPath, in.CACert, 0o644); err != nil {
			return generatedDevice{}, false, err
		}
		profile = "simulation-device-mtls-client"
		warning = "Simulation-only generated credential. Do not use as a production or customer device identity."
	} else {
		var outcome factoryEnrollOutcome
		enrollAttempts := envInt("CLOUD_LOAD_DEVICES_FACTORY_ENROLL_RETRIES", 1)
		if enrollAttempts < 1 {
			enrollAttempts = 1
		}
		retryDelay := envDurationDefault("CLOUD_LOAD_DEVICES_FACTORY_ENROLL_RETRY_DELAY", time.Second)
	enrollAlgorithms:
		for i, algorithm := range in.KeyAlgorithms {
			keyAlgorithm = algorithm
			if i > 0 {
				logLoad("retrying factory enrollment with fallback key algorithm: index=%03d device=%s algorithm=%s", in.Index, deviceID, keyAlgorithm)
				if _, err := writeDeviceKeyAndCSR(keyPath, csrPath, deviceID, in.Type.Name, keyAlgorithm); err != nil {
					return generatedDevice{}, false, err
				}
			}
			for attempt := 1; attempt <= enrollAttempts; attempt++ {
				outcome, err = factoryEnrollDevice(in, deviceID, display, csrPath, certPath, chainPath, keyAlgorithm)
				if err != nil {
					return generatedDevice{}, false, err
				}
				if outcome.OK {
					break enrollAlgorithms
				}
				if !outcome.Retryable {
					break enrollAlgorithms
				}
				if attempt < enrollAttempts {
					logLoad("retrying transient factory enrollment failure: index=%03d device=%s algorithm=%s attempt=%d/%d status=%s", in.Index, deviceID, keyAlgorithm, attempt+1, enrollAttempts, outcome.HTTPStatus)
					if retryDelay > 0 {
						time.Sleep(retryDelay)
					}
				}
			}
			if i == 1 {
				break
			}
		}
		if !outcome.OK {
			recordEnrollResult(in.ResultsPath, "failed", in.Index, deviceID, in.Type.Name, in.Type.ServiceOptions, outcome.HTTPStatus, outcome.RequestID, fmt.Sprintf("%s-%s-%04d", in.SerialPrefix, in.RunID, in.Index), outcome.ErrorText)
			logLoad("enroll failed: index=%03d device=%s type=%s status=%s error=%s", in.Index, deviceID, in.Type.Name, outcome.HTTPStatus, outcome.ErrorText)
			return generatedDevice{}, false, nil
		}
		recordEnrollResult(in.ResultsPath, "ok", in.Index, deviceID, in.Type.Name, in.Type.ServiceOptions, outcome.HTTPStatus, outcome.RequestID, outcome.Serial, "")
	}
	bundlePath := filepath.Join(bundleDir, deviceID+".pem")
	certBytes, err := os.ReadFile(certPath)
	if err != nil {
		return generatedDevice{}, false, err
	}
	keyBytes, err := os.ReadFile(keyPath)
	if err != nil {
		return generatedDevice{}, false, err
	}
	if err := os.WriteFile(bundlePath, append(certBytes, keyBytes...), 0o600); err != nil {
		return generatedDevice{}, false, err
	}
	device := generatedDevice{
		DeviceID:             deviceID,
		DeviceType:           in.Type.Name,
		DeviceItemProfileID:  in.ProductID,
		MQTTCapability:       in.Type.Capability,
		ServiceOptions:       in.Type.ServiceOptions,
		Model:                in.Type.Model,
		DisplayName:          display,
		FirmwareVersion:      "0.0.0-loadtest",
		Capabilities:         in.Type.Capabilities,
		CertificateProfile:   profile,
		KeyAlgorithm:         keyAlgorithm,
		CertificatePath:      relSlash(in.OutDir, certPath),
		CertificateChainPath: relSlash(in.OutDir, chainPath),
		KeyPath:              relSlash(in.OutDir, keyPath),
		CSRPath:              relSlash(in.OutDir, csrPath),
		BundlePath:           relSlash(in.OutDir, bundlePath),
		Production:           false,
		Warning:              warning,
	}
	if err := writeJSON(filepath.Join(deviceDir, "metadata.json"), device); err != nil {
		return generatedDevice{}, false, err
	}
	return device, true, nil
}

func reusableLocalLoadDevice(in loadDeviceInput, deviceID, display, deviceDir, keyPath, csrPath, certPath, chainPath, bundlePath string) (generatedDevice, string, string, bool) {
	metadataPath := filepath.Join(deviceDir, "metadata.json")
	raw, err := os.ReadFile(metadataPath)
	if err != nil {
		return generatedDevice{}, "", "", false
	}
	var device generatedDevice
	if err := json.Unmarshal(raw, &device); err != nil {
		return generatedDevice{}, "", "", false
	}
	expectedProfile := "factory-enrolled-device-mtls-client"
	if in.GenerateOnly {
		expectedProfile = "simulation-device-mtls-client"
	}
	if device.DeviceID != deviceID ||
		device.DeviceType != in.Type.Name ||
		device.DeviceItemProfileID != in.ProductID ||
		device.MQTTCapability != in.Type.Capability ||
		device.Model != in.Type.Model ||
		device.DisplayName != display ||
		device.CertificateProfile != expectedProfile ||
		device.Production ||
		!stringSlicesEqual(device.ServiceOptions, in.Type.ServiceOptions) ||
		!stringSlicesEqual(device.Capabilities, in.Type.Capabilities) {
		return generatedDevice{}, "", "", false
	}
	if device.CertificatePath != relSlash(in.OutDir, certPath) ||
		device.CertificateChainPath != relSlash(in.OutDir, chainPath) ||
		device.KeyPath != relSlash(in.OutDir, keyPath) ||
		device.CSRPath != relSlash(in.OutDir, csrPath) ||
		device.BundlePath != relSlash(in.OutDir, bundlePath) {
		return generatedDevice{}, "", "", false
	}
	if !fileHasPEMBlock(keyPath, "PRIVATE KEY") ||
		!fileHasPEMBlock(csrPath, "CERTIFICATE REQUEST") ||
		!fileHasPEMBlock(certPath, "CERTIFICATE") ||
		!fileHasPEMBlock(chainPath, "CERTIFICATE") ||
		!fileHasPEMBlock(bundlePath, "CERTIFICATE") ||
		!fileHasPEMBlock(bundlePath, "PRIVATE KEY") {
		return generatedDevice{}, "", "", false
	}
	if in.GenerateOnly {
		return device, "", "", true
	}
	var response struct {
		RequestID    string `json:"request_id"`
		SerialNumber string `json:"serial_number"`
	}
	responseRaw, err := os.ReadFile(filepath.Join(deviceDir, "factory-enroll-response.redacted.json"))
	if err != nil {
		return generatedDevice{}, "", "", false
	}
	if err := json.Unmarshal(responseRaw, &response); err != nil {
		return generatedDevice{}, "", "", false
	}
	if strings.TrimSpace(response.SerialNumber) == "" {
		return generatedDevice{}, "", "", false
	}
	if strings.TrimSpace(response.RequestID) == "" {
		var request struct {
			RequestID string `json:"request_id"`
		}
		requestRaw, err := os.ReadFile(filepath.Join(deviceDir, "factory-enroll-request.json"))
		if err != nil {
			return generatedDevice{}, "", "", false
		}
		if err := json.Unmarshal(requestRaw, &request); err != nil || strings.TrimSpace(request.RequestID) == "" {
			return generatedDevice{}, "", "", false
		}
		response.RequestID = request.RequestID
	}
	return device, response.SerialNumber, response.RequestID, true
}

func reusableSQLiteLoadDevice(in loadDeviceInput, deviceID, display, deviceDir, keyPath, csrPath, certPath, chainPath, bundlePath string) (generatedDevice, string, string, bool) {
	if strings.TrimSpace(in.EnvRoot) == "" || strings.TrimSpace(in.Brandname) == "" {
		return generatedDevice{}, "", "", false
	}
	store, err := openTestDataStore(in.EnvRoot, in.Brandname)
	if err != nil {
		return generatedDevice{}, "", "", false
	}
	defer store.Close()
	cred, err := store.ReadDeviceCredential(in.Brandname, deviceID)
	if err != nil {
		return generatedDevice{}, "", "", false
	}
	if strings.TrimSpace(cred.MetadataJSON) == "" {
		return generatedDevice{}, "", "", false
	}
	if err := os.MkdirAll(deviceDir, 0o755); err != nil {
		return generatedDevice{}, "", "", false
	}
	if err := os.MkdirAll(filepath.Dir(bundlePath), 0o755); err != nil {
		return generatedDevice{}, "", "", false
	}
	if err := os.WriteFile(keyPath, []byte(cred.KeyPEM), 0o600); err != nil {
		return generatedDevice{}, "", "", false
	}
	if err := os.WriteFile(csrPath, []byte(cred.CSRPEM), 0o644); err != nil {
		return generatedDevice{}, "", "", false
	}
	if err := os.WriteFile(certPath, []byte(cred.CertPEM), 0o644); err != nil {
		return generatedDevice{}, "", "", false
	}
	if err := os.WriteFile(chainPath, []byte(cred.ChainPEM), 0o644); err != nil {
		return generatedDevice{}, "", "", false
	}
	bundle := cred.BundlePEM
	if strings.TrimSpace(bundle) == "" {
		bundle = cred.CertPEM + cred.KeyPEM
	}
	if err := os.WriteFile(bundlePath, []byte(bundle), 0o600); err != nil {
		return generatedDevice{}, "", "", false
	}
	if strings.TrimSpace(cred.FactoryEnrollRequestJSON) != "" {
		_ = os.WriteFile(filepath.Join(deviceDir, "factory-enroll-request.json"), []byte(cred.FactoryEnrollRequestJSON), 0o600)
	}
	if strings.TrimSpace(cred.FactoryEnrollResponseRedactedJSON) != "" {
		_ = os.WriteFile(filepath.Join(deviceDir, "factory-enroll-response.redacted.json"), []byte(cred.FactoryEnrollResponseRedactedJSON), 0o600)
	}
	if err := os.WriteFile(filepath.Join(deviceDir, "metadata.json"), []byte(cred.MetadataJSON), 0o600); err != nil {
		return generatedDevice{}, "", "", false
	}
	return reusableLocalLoadDevice(in, deviceID, display, deviceDir, keyPath, csrPath, certPath, chainPath, bundlePath)
}

func stringSlicesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func fileHasPEMBlock(path, blockType string) bool {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return false
	}
	for {
		block, rest := pem.Decode(data)
		if block == nil {
			return false
		}
		if block.Type == blockType || strings.HasSuffix(block.Type, " "+blockType) {
			return true
		}
		data = rest
	}
}

func writeDeviceKeyAndCSR(keyPath, csrPath, deviceID, deviceType, algorithm string) (crypto.Signer, error) {
	key, keyPEM, err := newCertificatePrivateKey(algorithm)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(keyPath), 0o755); err != nil {
		return nil, err
	}
	if err := os.WriteFile(keyPath, []byte(keyPEM), 0o600); err != nil {
		return nil, err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{Country: []string{"TW"}, Organization: []string{"Realtek Connect Plus Simulation"}, OrganizationalUnit: []string{deviceType}, CommonName: deviceID},
	}, key)
	if err != nil {
		return nil, err
	}
	if err := writePEM(csrPath, "CERTIFICATE REQUEST", csrDER, 0o644); err != nil {
		return nil, err
	}
	return key, nil
}

func factoryEnrollDevice(in loadDeviceInput, deviceID, display, csrPath, certPath, chainPath, keyAlgorithm string) (factoryEnrollOutcome, error) {
	factoryURL := factoryEnrollURLForDevice(in.FactoryURL, in.Index)
	if factoryURL == "" {
		return factoryEnrollOutcome{}, errors.New("factory enrollment URL missing")
	}
	requestID := fmt.Sprintf("%s-%s", in.RunID, deviceID)
	if strings.EqualFold(strings.TrimSpace(keyAlgorithm), "p256") {
		requestID += "-p256"
	}
	serial := fmt.Sprintf("%s-%s-%04d", in.SerialPrefix, in.RunID, in.Index)
	deviceDir := filepath.Dir(csrPath)
	logLoad("enroll start: index=%03d device=%s type=%s service_options=%s", in.Index, deviceID, in.Type.Name, strings.Join(in.Type.ServiceOptions, ","))
	csrPEM, err := os.ReadFile(csrPath)
	if err != nil {
		return factoryEnrollOutcome{}, err
	}
	body := map[string]any{
		"request_id":      requestID,
		"devid":           deviceID,
		"csr_pem":         string(csrPEM),
		"serial_number":   serial,
		"factory_id":      in.FactoryID,
		"line_id":         in.LineID,
		"station_id":      in.StationID,
		"fixture_id":      in.FixtureID,
		"operator_id":     in.OperatorID,
		"batch_id":        in.BatchID,
		"service_options": in.Type.ServiceOptions,
		"metadata": map[string]any{
			"source":          "cloud-generate-load-devices",
			"run_id":          in.RunID,
			"device_type":     in.Type.Name,
			"model":           in.Type.Model,
			"display_name":    display,
			"mqtt_capability": in.Type.Capability,
			"capabilities":    in.Type.Capabilities,
			"service_options": in.Type.ServiceOptions,
		},
	}
	bodyBytes, err := json.MarshalIndent(body, "", "  ")
	if err != nil {
		return factoryEnrollOutcome{}, err
	}
	requestPath := filepath.Join(deviceDir, "factory-enroll-request.json")
	if err := os.WriteFile(requestPath, bodyBytes, 0o644); err != nil {
		return factoryEnrollOutcome{}, err
	}
	client := &http.Client{Timeout: in.Timeout}
	req, err := http.NewRequest(http.MethodPost, factoryURL+"/v1/factory/enroll", bytes.NewReader(bodyBytes))
	if err != nil {
		return factoryEnrollOutcome{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Video-Cloud-Request-ID", requestID)
	if strings.TrimSpace(in.ProductionJWT) != "" {
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(in.ProductionJWT))
	} else {
		timestamp := time.Now().UTC().Format(time.RFC3339)
		signature := signFactoryRequest(in.FactoryAuthKey, "POST", "/v1/factory/enroll", timestamp, requestID, bodyBytes)
		req.Header.Set("X-Video-Cloud-Timestamp", timestamp)
		req.Header.Set("X-Video-Cloud-Signature", signature)
	}
	resp, err := client.Do(req)
	if err != nil {
		return factoryEnrollOutcome{Retryable: true, HTTPStatus: "000", ErrorText: err.Error(), Serial: serial, RequestID: requestID}, nil
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		errText := fmt.Sprintf("factory enrollment HTTP %d", resp.StatusCode)
		return factoryEnrollOutcome{Retryable: resp.StatusCode >= 500, HTTPStatus: strconv.Itoa(resp.StatusCode), ErrorText: errText, Serial: serial, RequestID: requestID}, nil
	}
	var parsed struct {
		CertificatePEM      string `json:"certificate_pem"`
		CertificateChainPEM string `json:"certificate_chain_pem"`
		SerialNumber        string `json:"serial_number"`
	}
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return factoryEnrollOutcome{}, err
	}
	if parsed.CertificatePEM == "" || parsed.CertificateChainPEM == "" {
		errText := "factory enrollment response missing certificate_pem or certificate_chain_pem"
		return factoryEnrollOutcome{HTTPStatus: strconv.Itoa(resp.StatusCode), ErrorText: errText, Serial: serial, RequestID: requestID}, nil
	}
	if err := os.WriteFile(certPath, []byte(parsed.CertificatePEM), 0o644); err != nil {
		return factoryEnrollOutcome{}, err
	}
	if err := os.WriteFile(chainPath, []byte(parsed.CertificateChainPEM), 0o644); err != nil {
		return factoryEnrollOutcome{}, err
	}
	var redacted map[string]any
	_ = json.Unmarshal(respBytes, &redacted)
	delete(redacted, "certificate_pem")
	delete(redacted, "certificate_chain_pem")
	if err := writeJSON(filepath.Join(deviceDir, "factory-enroll-response.redacted.json"), redacted); err != nil {
		return factoryEnrollOutcome{}, err
	}
	if parsed.SerialNumber == "" {
		parsed.SerialNumber = serial
	}
	logLoad("enroll ok: index=%03d device=%s type=%s status=%d serial=%s", in.Index, deviceID, in.Type.Name, resp.StatusCode, parsed.SerialNumber)
	return factoryEnrollOutcome{OK: true, HTTPStatus: strconv.Itoa(resp.StatusCode), Serial: parsed.SerialNumber, RequestID: requestID}, nil
}

func normalizeFactoryEnrollURLs(raw string) string {
	parts := splitCSV(raw)
	normalized := make([]string, 0, len(parts))
	for _, part := range parts {
		value := strings.TrimRight(strings.TrimSpace(part), "/")
		if value != "" {
			normalized = append(normalized, value)
		}
	}
	return strings.Join(normalized, ",")
}

func factoryEnrollURLForDevice(raw string, index int) string {
	parts := splitCSV(normalizeFactoryEnrollURLs(raw))
	if len(parts) == 0 {
		return ""
	}
	if index <= 0 {
		return parts[0]
	}
	return parts[(index-1)%len(parts)]
}

func allocateDeviceMix(count int, raw string) (map[string]int, error) {
	weights := map[string]int{}
	for _, dt := range loadDeviceTypes {
		weights[dt.Name] = 0
	}
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		name, value, ok := strings.Cut(item, "=")
		if !ok {
			return nil, fmt.Errorf("invalid --mix item: %s", item)
		}
		if _, exists := weights[name]; !exists {
			return nil, fmt.Errorf("unsupported device type in --mix: %s", name)
		}
		n, err := strconv.Atoi(value)
		if err != nil || n < 0 {
			return nil, fmt.Errorf("invalid weight for %s: %s", name, value)
		}
		weights[name] = n
	}
	total := 0
	for _, value := range weights {
		total += value
	}
	if total == 0 {
		return nil, errors.New("--mix must include at least one positive weight")
	}
	alloc := map[string]int{}
	remainders := map[string]int{}
	allocated := 0
	for _, dt := range loadDeviceTypes {
		base := count * weights[dt.Name] / total
		rem := count * weights[dt.Name] % total
		alloc[dt.Name] = base
		remainders[dt.Name] = rem
		allocated += base
	}
	for leftover := count - allocated; leftover > 0; leftover-- {
		selected := ""
		best := -1
		for _, dt := range loadDeviceTypes {
			if weights[dt.Name] > 0 && remainders[dt.Name] > best {
				selected = dt.Name
				best = remainders[dt.Name]
			}
		}
		alloc[selected]++
		remainders[selected] = -1
	}
	return alloc, nil
}

func writeGeneratedCA(outDir string, days int) (*ecdsa.PrivateKey, []byte, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	caDir := filepath.Join(outDir, "ca")
	if err := os.MkdirAll(caDir, 0o755); err != nil {
		return nil, nil, err
	}
	if err := writeECPrivateKey(filepath.Join(caDir, "sim-device-ca.key.pem"), key); err != nil {
		return nil, nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{Country: []string{"TW"}, Organization: []string{"Realtek Connect Plus Simulation"}, OrganizationalUnit: []string{"Load Test Device Factory"}, CommonName: "Realtek Connect Plus Simulation Device CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Duration(days) * 24 * time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		MaxPathLen:            0,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		SubjectKeyId:          []byte{1, 2, 3, 4},
	}
	der, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &key.PublicKey, key)
	if err != nil {
		return nil, nil, err
	}
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if err := os.WriteFile(filepath.Join(caDir, "sim-device-ca.cert.pem"), pemBytes, 0o644); err != nil {
		return nil, nil, err
	}
	return key, pemBytes, nil
}

func signDeviceCert(deviceID string, key crypto.Signer, caKey *ecdsa.PrivateKey, caPEM []byte, days int) ([]byte, error) {
	block, _ := pem.Decode(caPEM)
	if block == nil {
		return nil, errors.New("invalid CA certificate")
	}
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, err
	}
	tpl := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{Country: []string{"TW"}, Organization: []string{"Realtek Connect Plus Simulation"}, OrganizationalUnit: []string{"Load Test Device"}, CommonName: deviceID},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Duration(days) * 24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
		DNSNames:     []string{deviceID + ".simulated.realtek-connect.local"},
		URIs:         mustParseURIs("urn:realtek-connect:simulated-device:" + deviceID),
	}
	return x509.CreateCertificate(rand.Reader, tpl, caCert, key.Public(), caKey)
}

func signFactoryRequest(key, method, path, timestamp, requestID string, body []byte) string {
	hash := sha256.Sum256(body)
	canonical := strings.Join([]string{strings.ToUpper(strings.TrimSpace(method)), strings.TrimSpace(path), strings.TrimSpace(timestamp), strings.TrimSpace(requestID), hex.EncodeToString(hash[:])}, "\n")
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(canonical))
	return "v1=" + hex.EncodeToString(mac.Sum(nil))
}

var enrollResultMu sync.Mutex

func recordEnrollResult(path, status string, index int, deviceID, deviceType string, serviceOptions []string, httpStatus, requestID, serial, errText string) {
	entry := map[string]any{"status": status, "index": index, "device_id": deviceID, "device_type": deviceType, "service_options": serviceOptions, "http_status": httpStatus, "request_id": requestID, "serial_number": serial, "error": errText}
	data, _ := json.Marshal(entry)
	enrollResultMu.Lock()
	defer enrollResultMu.Unlock()
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer fh.Close()
	_, _ = fh.Write(append(data, '\n'))
}

func appendCSV(path string, device generatedDevice) {
	line := fmt.Sprintf("%s,%s,%s,%s,%s,%s,%s,%s\n", device.DeviceID, device.DeviceType, device.MQTTCapability, strings.Join(device.ServiceOptions, ";"), device.Model, device.CertificatePath, device.KeyPath, device.BundlePath)
	appendLine(path, strings.TrimSuffix(line, "\n"))
}

func appendLine(path, line string) {
	fh, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer fh.Close()
	_, _ = fmt.Fprintln(fh, line)
}

func writeLoadDeviceReadme(outDir string, count int, mix, mode, factoryURL string, caDays, deviceDays int) error {
	description := "factory enrollment"
	source := "issued by " + factoryURL + "/v1/factory/enroll"
	if mode == "generate_only" {
		description = "offline generate-only"
		source = "locally signed by the simulation CA"
	}
	content := fmt.Sprintf(`# Staging Load-Test Device Factory Output

This directory contains staging load-test device identities generated for factory/provisioning flow rehearsal.

- Device count: %d
- Requested mix: %s
- Mode: %s
- Device key type: Ed25519, with P-256 fallback when the staging signer rejects Ed25519
- Device certificate profile: clientAuth
- Credential source: %s
- CA validity days: %d
- Device validity days: %d
`, count, mix, description, source, caDays, deviceDays)
	return os.WriteFile(filepath.Join(outDir, "README.md"), []byte(content), 0o644)
}

func loadDisplayName(deviceType string, ordinal int) string {
	switch deviceType {
	case "camera":
		return fmt.Sprintf("PRO2 Camera Simulator %03d", ordinal)
	case "light":
		return fmt.Sprintf("Light Simulator %03d", ordinal)
	case "switch":
		return fmt.Sprintf("Switch Simulator %03d", ordinal)
	case "smart_plug":
		return fmt.Sprintf("Smart Plug Simulator %03d", ordinal)
	case "air_conditioner":
		return fmt.Sprintf("Air Conditioner Simulator %03d", ordinal)
	case "environment_sensor":
		return fmt.Sprintf("Environment Sensor Simulator %03d", ordinal)
	case "security_sensor":
		return fmt.Sprintf("Security Sensor Simulator %03d", ordinal)
	case "smart_meter":
		return fmt.Sprintf("Smart Meter Simulator %03d", ordinal)
	case "camera_status":
		return fmt.Sprintf("Camera Status Simulator %03d", ordinal)
	case "door_lock":
		return fmt.Sprintf("Door Lock Simulator %03d", ordinal)
	case "appliance":
		return fmt.Sprintf("Appliance Simulator %03d", ordinal)
	case "gateway":
		return fmt.Sprintf("Gateway Simulator %03d", ordinal)
	default:
		return fmt.Sprintf("Device Simulator %03d", ordinal)
	}
}

func writeECPrivateKey(path string, key *ecdsa.PrivateKey) error {
	der, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return err
	}
	return writePEM(path, "EC PRIVATE KEY", der, 0o600)
}

func writePEM(path, blockType string, der []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: blockType, Bytes: der}), mode)
}

func relSlash(root, path string) string {
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

func shellQuote(value string) string {
	return strings.ReplaceAll(value, `'`, `'\''`)
}

func factoryLogSuffix(mode, url string) string {
	if mode == "factory_enroll" {
		return " factory_url=" + url
	}
	return ""
}

func logLoad(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[cloud-load-devices %s +%03ds] %s\n", time.Now().Format("15:04:05"), 0, fmt.Sprintf(format, args...))
}

func envInt(key string, fallback int) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return fallback
}

func envJSONTextMap(key string) (map[string]string, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return map[string]string{}, nil
	}
	values := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, fmt.Errorf("%s must be a JSON object with string values: %w", key, err)
	}
	for name, value := range values {
		if strings.TrimSpace(name) == "" || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%s contains an empty device type or value", key)
		}
		values[name] = strings.TrimSpace(value)
	}
	return values, nil
}

func boolishEnv(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func mustParseURIs(values ...string) []*url.URL {
	out := make([]*url.URL, 0, len(values))
	for _, value := range values {
		parsed, err := url.Parse(value)
		if err == nil {
			out = append(out, parsed)
		}
	}
	return out
}

type checkState struct {
	failures int
}

func newCheck() *checkState {
	return &checkState{}
}

func (c *checkState) fail(message string) {
	fmt.Fprintln(os.Stderr, "FAIL: "+message)
	c.failures++
}

func (c *checkState) pass(message string) {
	fmt.Fprintln(os.Stdout, "OK: "+message)
}

func (c *checkState) requireFile(workspace, path string) {
	if info, err := os.Stat(filepath.Join(workspace, path)); err == nil && !info.IsDir() {
		c.pass("found " + path)
	} else {
		c.fail("missing " + path)
	}
}

func (c *checkState) requireDir(workspace, path string) {
	if info, err := os.Stat(filepath.Join(workspace, path)); err == nil && info.IsDir() {
		c.pass("found " + path)
	} else {
		c.fail("missing " + path)
	}
}

func checkGitGrepNoMatch(check *checkState, workspace, label, pattern string, paths []string) {
	checkGitGrepNoMatchFiltered(check, workspace, label, pattern, paths, nil)
}

func checkGitGrepNoMatchFiltered(check *checkState, workspace, label, pattern string, paths []string, allow func(string) bool) {
	args := append([]string{"-C", workspace, "grep", "-n", "-E", "-e", pattern, "--"}, paths...)
	out, err := exec.Command("git", args...).CombinedOutput()
	if err == nil {
		blocking := []string{}
		for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
			if line == "" {
				continue
			}
			if allow != nil && allow(line) {
				continue
			}
			blocking = append(blocking, line)
		}
		if len(blocking) == 0 {
			check.pass("no " + label + " in tracked workspace files")
			return
		}
		fmt.Fprintf(os.Stderr, "Potential %s found:\n%s\n", label, strings.Join(blocking, "\n"))
		check.fail(label + " present in tracked workspace files")
		return
	}
	var exitErr *exec.ExitError
	if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
		check.pass("no " + label + " in tracked workspace files")
		return
	}
	check.fail(label + " scan failed")
}

func checkFileNoMatch(check *checkState, path, label, pattern string) {
	data, err := os.ReadFile(path)
	if err != nil {
		check.fail("missing " + path)
		return
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		check.fail("invalid pattern for " + label)
		return
	}
	if match := re.Find(data); len(match) > 0 {
		fmt.Fprintf(os.Stderr, "Potential %s found in %s\n", label, path)
		check.fail(label + " present in " + path)
		return
	}
	check.pass("no " + label + " in " + path)
}

func anyFileContains(workspace string, paths []string, needle string) bool {
	for _, path := range paths {
		if strings.Contains(readText(filepath.Join(workspace, path)), needle) {
			return true
		}
	}
	return false
}

func readText(path string) string {
	data, _ := os.ReadFile(path)
	return string(data)
}

func submodulePaths(workspace string) ([]string, error) {
	out, err := gitOutput(workspace, "config", "--file", ".gitmodules", "--get-regexp", `^submodule\..*\.path$`)
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 {
			return nil, nil
		}
		return nil, err
	}
	seen := map[string]bool{}
	paths := []string{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		path := fields[1]
		if !seen[path] {
			seen[path] = true
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func gitOutput(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	return stdout.String(), err
}

func runCmd(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func copyPath(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if info.IsDir() {
		return filepath.WalkDir(src, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(src, path)
			if err != nil {
				return err
			}
			target := filepath.Join(dst, rel)
			if entry.IsDir() {
				return os.MkdirAll(target, 0o755)
			}
			return copyFile(path, target)
		})
	}
	return copyFile(src, dst)
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o600)
}

func writeTSV(path string, rows [][]string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	for _, row := range rows {
		for i, col := range row {
			if i > 0 {
				b.WriteByte('\t')
			}
			b.WriteString(strings.ReplaceAll(col, "\t", " "))
		}
		b.WriteByte('\n')
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func checkCertTarget(target, domain, certPath string, minValidDays int) certCheckResult {
	result := certCheckResult{Target: target, Domain: domain, Source: certPath, Status: "pass", DaysLeft: "n/a"}
	data, err := os.ReadFile(certPath)
	if err != nil {
		result.Status = "fail"
		result.Detail = "missing certificate"
		return result
	}
	block, _ := pem.Decode(data)
	if block == nil {
		result.Status = "fail"
		result.Detail = "invalid PEM certificate"
		return result
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		result.Status = "fail"
		result.Detail = "invalid certificate"
		return result
	}
	result.ExpiresAt = cert.NotAfter.UTC().Format(time.RFC3339)
	result.Issuer = cert.Issuer.String()
	daysLeft := int(time.Until(cert.NotAfter).Hours() / 24)
	result.DaysLeft = daysLeft
	if err := cert.VerifyHostname(domain); err != nil {
		result.Status = "fail"
		result.Detail = "hostname mismatch"
		return result
	}
	if daysLeft < minValidDays {
		result.Status = "fail"
		result.Detail = fmt.Sprintf("expires within %d days", minValidDays)
		return result
	}
	result.Detail = "ok"
	return result
}

type bindArtifact struct {
	Brandname    string           `json:"brandname"`
	BrandCloudID string           `json:"brand_cloud_id"`
	TenantSlug   string           `json:"tenant_slug"`
	Count        int              `json:"count"`
	Inputs       bindInputs       `json:"inputs"`
	Assignments  []bindAssignment `json:"assignments"`
}

type bindInputs struct {
	UsersFile  string `json:"users_file"`
	DevicesDir string `json:"devices_dir"`
}

type bindAssignment struct {
	AssignmentIndex int      `json:"assignment_index"`
	AssignedEmail   string   `json:"assigned_email"`
	DeviceID        string   `json:"device_id"`
	DeviceType      string   `json:"device_type"`
	Category        string   `json:"category"`
	ServiceOptions  []string `json:"service_options"`
	ProductID       string   `json:"product_id,omitempty"`
	ClaimID         string   `json:"claim_id"`
	AccountDeviceID string   `json:"account_device_id"`
	OperationID     string   `json:"operation_id"`
	Status          string   `json:"status"`
}

func runValidateDeviceBind(args []string) error {
	fs := flag.NewFlagSet("validate-device-bind", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	brandname := fs.String("brandname", "RTK", "brand name")
	bindPath := fs.String("bind-artifact", "", "bind artifact")
	outDir := fs.String("out-dir", "", "output directory")
	expectedCount := fs.Int("expected-count", 0, "expected count")
	expectedDevicesPerUser := fs.Int("expected-devices-per-user", 0, "expected devices per user")
	waitProvisionedTimeout := fs.Duration("wait-provisioned-timeout", 0, "wait for Account Manager provisioning state to reach activated/online readiness")
	waitProvisionedPoll := fs.Duration("wait-provisioned-poll", 10*time.Second, "provisioning state poll interval")
	waitProvisionedConcurrency := fs.Int("wait-provisioned-concurrency", envInt("CLOUD_STAGING_E2E_BIND_PROVISION_CONCURRENCY", 16), "provisioning state poll concurrency")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *outDir == "" {
		return errors.New("--out-dir is required")
	}
	if *waitProvisionedConcurrency <= 0 {
		return errors.New("--wait-provisioned-concurrency must be greater than zero")
	}
	var artifact bindArtifact
	if *bindPath != "" {
		data, err := os.ReadFile(*bindPath)
		if err != nil {
			return err
		}
		if err := json.Unmarshal(data, &artifact); err != nil {
			return err
		}
	} else {
		workspace := *workspaceFlag
		if workspace == "" {
			var err error
			workspace, err = workspaceRoot()
			if err != nil {
				return err
			}
		}
		envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
		if err != nil {
			return err
		}
		artifact, err = readBindArtifactFromTestData(envRoot, *brandname)
		if err != nil {
			return err
		}
	}
	result := map[string]any{
		"overall": "pass",
		"summary": map[string]any{
			"total_devices": len(artifact.Assignments),
		},
		"user_counts": map[string]int{},
		"failures":    []string{},
	}
	userCounts := result["user_counts"].(map[string]int)
	failures := []string{}
	failureCategories := map[string]int{}
	addFailure := func(category, message string) {
		failureCategories[category]++
		failures = append(failures, message)
	}
	if *expectedCount > 0 && len(artifact.Assignments) != *expectedCount {
		addFailure("count_mismatch", fmt.Sprintf("expected %d devices, got %d", *expectedCount, len(artifact.Assignments)))
	}
	mqttOnly := 0
	videoDevices := 0
	for _, assignment := range artifact.Assignments {
		userCounts[assignment.AssignedEmail]++
		hasMQTT := contains(assignment.ServiceOptions, "mqtt")
		hasVideo := contains(assignment.ServiceOptions, "video_streaming") || contains(assignment.ServiceOptions, "video_storage")
		if assignment.Category == "mqtt_device" {
			mqttOnly++
			if hasVideo {
				addFailure("service_option_mismatch", fmt.Sprintf("mqtt-only device %s has video service option", assignment.DeviceID))
			}
			if !hasMQTT {
				addFailure("service_option_mismatch", fmt.Sprintf("mqtt-only device %s is missing mqtt service option", assignment.DeviceID))
			}
		}
		if assignment.Category == "ip_camera" {
			videoDevices++
			if !hasVideo {
				addFailure("service_option_mismatch", fmt.Sprintf("camera device %s is missing video service option", assignment.DeviceID))
			}
		}
		if assignment.AccountDeviceID == "" || (assignment.OperationID == "" && assignment.Status != "already_bound") {
			addFailure("missing_bind_identifier", fmt.Sprintf("device %s missing bind identifiers", assignment.DeviceID))
		}
	}
	if *waitProvisionedTimeout > 0 && len(failures) == 0 {
		waitResult, err := waitBindProvisioned(*workspaceFlag, *envRootFlag, artifact, *waitProvisionedTimeout, *waitProvisionedPoll, *waitProvisionedConcurrency)
		if err != nil {
			failures = append(failures, err.Error())
		} else {
			result["provisioning"] = waitResult
			for _, failure := range waitResult.Failures {
				addFailure(categorizeBindValidationFailure(failure), failure)
			}
		}
	}
	if *expectedDevicesPerUser > 0 {
		for email, count := range userCounts {
			if count != *expectedDevicesPerUser {
				addFailure("user_device_count_mismatch", fmt.Sprintf("user %s expected %d devices, got %d", email, *expectedDevicesPerUser, count))
			}
		}
	}
	if len(failures) > 0 {
		result["overall"] = "fail"
	}
	result["failures"] = failures
	result["failure_categories"] = failureCategories
	result["summary"].(map[string]any)["mqtt_only_devices"] = mqttOnly
	result["summary"].(map[string]any)["video_devices"] = videoDevices
	result["summary"].(map[string]any)["failure_categories"] = failureCategories
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		return err
	}
	resultsFile := filepath.Join(*outDir, "bulk-device-bind-validation-results.json")
	reportFile := filepath.Join(*outDir, "bulk-device-bind-validation-report.md")
	if err := writeJSON(resultsFile, result); err != nil {
		return err
	}
	if err := os.WriteFile(reportFile, []byte(renderBindReport(artifact, result)), 0o644); err != nil {
		return err
	}
	stdout := map[string]any{
		"action":             "validated",
		"overall":            result["overall"],
		"total_devices":      len(artifact.Assignments),
		"failure_categories": failureCategories,
		"results_file":       resultsFile,
		"report_file":        reportFile,
	}
	if err := json.NewEncoder(os.Stdout).Encode(stdout); err != nil {
		return err
	}
	if result["overall"] != "pass" {
		return exitCode(1)
	}
	return nil
}

type bindProvisionWaitResult struct {
	Checked     int                                      `json:"checked"`
	Ready       int                                      `json:"ready"`
	Pending     int                                      `json:"pending"`
	Failed      int                                      `json:"failed"`
	Attempts    int                                      `json:"attempts"`
	ElapsedMS   int64                                    `json:"elapsed_ms"`
	LastStates  map[string]bindProvisioningStateSnapshot `json:"last_states"`
	Failures    []string                                 `json:"failures"`
	CompletedAt string                                   `json:"completed_at"`
}

type bindProvisioningStateSnapshot struct {
	DeviceID              string `json:"device_id"`
	AccountDeviceID       string `json:"account_device_id"`
	AssignedEmail         string `json:"assigned_email"`
	BindStatus            string `json:"bind_status,omitempty"`
	ReadinessState        string `json:"readiness_state,omitempty"`
	ProductState          string `json:"product_state,omitempty"`
	OperationStatus       string `json:"operation_status,omitempty"`
	ActivationStatus      string `json:"activation_status,omitempty"`
	FailureCode           string `json:"failure_code,omitempty"`
	FailureMessage        string `json:"failure_message,omitempty"`
	ProvisioningHTTPError string `json:"provisioning_http_error,omitempty"`
}

func waitBindProvisioned(workspaceFlag, envRootFlag string, artifact bindArtifact, timeout, poll time.Duration, concurrency int) (bindProvisionWaitResult, error) {
	if timeout <= 0 {
		return bindProvisionWaitResult{}, nil
	}
	if poll <= 0 {
		poll = 10 * time.Second
	}
	if concurrency <= 0 {
		concurrency = 16
	}
	ctx, err := accountManagerContextFromFlags(workspaceFlag, envRootFlag)
	if err != nil {
		return bindProvisionWaitResult{}, err
	}
	if artifact.TenantSlug == "" {
		token, err := accountLogin(ctx, func(string, ...any) {})
		if err != nil {
			return bindProvisionWaitResult{}, fmt.Errorf("bind artifact missing tenant_slug and platform login failed: %w", err)
		}
		brandCloud, err := accountFindBrandCloud(ctx, token, artifact.Brandname)
		if err != nil {
			return bindProvisionWaitResult{}, fmt.Errorf("bind artifact missing tenant_slug and brand cloud lookup failed: %w", err)
		}
		artifact.TenantSlug = stringValue(brandCloud["tenant_slug"])
		if artifact.TenantSlug == "" {
			return bindProvisionWaitResult{}, errors.New("bind artifact missing tenant_slug and brand cloud lookup did not return tenant_slug")
		}
	}
	users, updateUsers, err := bindProvisionUsers(workspaceFlag, envRootFlag, artifact)
	if err != nil {
		return bindProvisionWaitResult{}, err
	}
	userSessions := map[string]*brandCloudUserSession{}
	selected := []bindAssignment{}
	for _, assignment := range artifact.Assignments {
		if assignment.AccountDeviceID == "" || assignment.AssignedEmail == "" {
			continue
		}
		selected = append(selected, assignment)
		if userSessions[assignment.AssignedEmail] == nil {
			user := users[assignment.AssignedEmail]
			if user.Password == "" {
				return bindProvisionWaitResult{}, fmt.Errorf("users artifact missing password for %s", assignment.AssignedEmail)
			}
			userSessions[assignment.AssignedEmail] = &brandCloudUserSession{Email: assignment.AssignedEmail, Password: user.Password, Session: user.Tokens}
		}
	}
	if err := prepareBindProvisionUserSessions(ctx, artifact.TenantSlug, userSessions, concurrency, func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, "[validate-device-bind] "+format+"\n", args...)
	}); err != nil {
		return bindProvisionWaitResult{}, err
	}
	defer func() {
		_ = updateUsers(userSessions)
	}()
	result := bindProvisionWaitResult{Checked: len(selected), LastStates: map[string]bindProvisioningStateSnapshot{}}
	started := time.Now()
	deadline := started.Add(timeout)
	attempts := 0
	lastPollDuration := time.Duration(0)
	for {
		if attempts > 0 && lastPollDuration > 0 && time.Until(deadline) < lastPollDuration {
			failures := bindTimeoutFailures(result.LastStates)
			result.Failures = failures
			result.Pending = len(failures)
			result.CompletedAt = time.Now().UTC().Format(time.RFC3339)
			return result, nil
		}
		attempts++
		attemptStarted := time.Now()
		type pollResult struct {
			snapshot bindProvisioningStateSnapshot
			failure  string
		}
		var pollProgressMu sync.Mutex
		pollCompleted := 0
		lastProgressLog := time.Now()
		logPollProgress := func(force bool) {
			pollProgressMu.Lock()
			defer pollProgressMu.Unlock()
			pollCompleted++
			if !force && pollCompleted < len(selected) && pollCompleted%100 != 0 && time.Since(lastProgressLog) < 10*time.Second {
				return
			}
			lastProgressLog = time.Now()
			fmt.Fprintf(os.Stderr, "[validate-device-bind] provisioning poll progress: attempt=%d done=%d/%d elapsed=%s total_elapsed=%s\n", attempts, pollCompleted, len(selected), formatDurationSeconds(int64(time.Since(attemptStarted).Seconds())), formatDurationSeconds(int64(time.Since(started).Seconds())))
		}
		pollResults, err := boundedParallelMap(len(selected), concurrency, func(i int) (pollResult, error) {
			assignment := selected[i]
			userSession := userSessions[assignment.AssignedEmail]
			token, err := brandCloudUserAccessToken(ctx, artifact.TenantSlug, userSession, func(string, ...any) {})
			if err != nil {
				snapshot := bindProvisioningStateSnapshot{
					DeviceID:              assignment.DeviceID,
					AccountDeviceID:       assignment.AccountDeviceID,
					AssignedEmail:         assignment.AssignedEmail,
					BindStatus:            assignment.Status,
					ProvisioningHTTPError: err.Error(),
				}
				logPollProgress(false)
				return pollResult{snapshot: snapshot, failure: fmt.Sprintf("device %s provisioning token failed: %s", assignment.DeviceID, err)}, nil
			}
			snapshot, err := fetchBindProvisioningState(ctx, token, artifact.BrandCloudID, assignment)
			if err != nil {
				snapshot = bindProvisioningStateSnapshot{
					DeviceID:              assignment.DeviceID,
					AccountDeviceID:       assignment.AccountDeviceID,
					AssignedEmail:         assignment.AssignedEmail,
					BindStatus:            assignment.Status,
					ProvisioningHTTPError: err.Error(),
				}
			}
			logPollProgress(false)
			return pollResult{snapshot: snapshot}, nil
		})
		if err != nil {
			return result, err
		}
		ready := 0
		pending := 0
		failed := 0
		failures := []string{}
		for _, pollResult := range pollResults {
			snapshot := pollResult.snapshot
			result.LastStates[snapshot.DeviceID] = snapshot
			if pollResult.failure != "" {
				failed++
				failures = append(failures, pollResult.failure)
				continue
			}
			switch {
			case snapshotReady(snapshot):
				ready++
			case snapshotFailed(snapshot):
				failed++
				failures = append(failures, fmt.Sprintf("device %s provisioning failed: readiness=%s product=%s operation=%s activation=%s error=%s", snapshot.DeviceID, snapshot.ReadinessState, snapshot.ProductState, snapshot.OperationStatus, snapshot.ActivationStatus, firstNonEmpty(snapshot.FailureCode, snapshot.ProvisioningHTTPError)))
			default:
				pending++
			}
		}
		result.Ready = ready
		result.Pending = pending
		result.Failed = failed
		result.Attempts = attempts
		result.ElapsedMS = time.Since(started).Milliseconds()
		result.Failures = failures
		result.CompletedAt = time.Now().UTC().Format(time.RFC3339)
		lastPollDuration = time.Since(attemptStarted)
		fmt.Fprintf(os.Stderr, "[validate-device-bind] provisioning poll: attempt=%d checked=%d ready=%d pending=%d failed=%d elapsed=%s total_elapsed=%s\n", attempts, len(selected), ready, pending, failed, formatDurationSeconds(int64(time.Since(attemptStarted).Seconds())), formatDurationSeconds(int64(time.Since(started).Seconds())))
		if ready == len(selected) || hasNonRetryableBindProvisionFailures(failures) {
			return result, nil
		}
		if time.Now().After(deadline) {
			result.Failures = bindTimeoutFailures(result.LastStates)
			return result, nil
		}
		time.Sleep(poll)
	}
}

func bindProvisionUsers(workspaceFlag, envRootFlag string, artifact bindArtifact) (map[string]userCredential, func(map[string]*brandCloudUserSession) error, error) {
	if artifact.Inputs.UsersFile != "" {
		users, err := readUsersFile(artifact.Inputs.UsersFile)
		if err != nil {
			return nil, nil, fmt.Errorf("read bind users file: %w", err)
		}
		return users, func(sessions map[string]*brandCloudUserSession) error {
			_, err := updateUsersArtifactTokens(artifact.Inputs.UsersFile, sessions)
			return err
		}, nil
	}
	brandname := strings.TrimSpace(artifact.Brandname)
	if brandname == "" {
		return nil, nil, errors.New("bind artifact missing brandname; cannot read SQLite test-data users")
	}
	envRoot, err := resolveEnvRootFromCommandFlags(workspaceFlag, envRootFlag)
	if err != nil {
		return nil, nil, fmt.Errorf("bind artifact missing inputs.users_file; SQLite fallback requires --env-root: %w", err)
	}
	users, _, err := readUsersListFromTestData(envRoot, brandname)
	if err != nil {
		return nil, nil, fmt.Errorf("read SQLite test-data users: %w", err)
	}
	return users, func(sessions map[string]*brandCloudUserSession) error {
		store, err := openTestDataStore(envRoot, brandname)
		if err != nil {
			return err
		}
		defer store.Close()
		_, err = store.UpdateUserTokens(brandname, sessions)
		return err
	}, nil
}

func categorizeBindValidationFailure(failure string) string {
	lower := strings.ToLower(failure)
	switch {
	case isRetryableProvisioningPollError(lower):
		return "provisioning_transport"
	case strings.Contains(lower, "not ready") || strings.Contains(lower, "timeout"):
		if strings.Contains(lower, "bind_status=already_bound") {
			return "already_bound_not_ready"
		}
		return "provisioning_timeout"
	case strings.Contains(lower, "token"):
		return "token"
	case strings.Contains(lower, "http"):
		return "provisioning_http"
	case strings.Contains(lower, "activation") || strings.Contains(lower, "provisioning failed"):
		return "provisioning_failed"
	default:
		return "provisioning"
	}
}

func hasNonRetryableBindProvisionFailures(failures []string) bool {
	for _, failure := range failures {
		if !isRetryableProvisioningPollError(failure) {
			return true
		}
	}
	return false
}

func isRetryableProvisioningPollError(errText string) bool {
	lower := strings.ToLower(errText)
	for _, marker := range []string{
		"connection reset by peer",
		"connect: connection refused",
		"unexpected eof",
		"broken pipe",
		"error creating error stream",
		"lost connection to pod",
		"error upgrading connection",
		"i/o timeout",
		"context deadline exceeded",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func bindTimeoutFailures(states map[string]bindProvisioningStateSnapshot) []string {
	failures := []string{}
	for _, snapshot := range states {
		if !snapshotReady(snapshot) {
			failures = append(failures, fmt.Sprintf("device %s provisioning not ready before timeout: bind_status=%s readiness=%s product=%s operation=%s activation=%s error=%s", snapshot.DeviceID, snapshot.BindStatus, snapshot.ReadinessState, snapshot.ProductState, snapshot.OperationStatus, snapshot.ActivationStatus, snapshot.ProvisioningHTTPError))
		}
	}
	sort.Strings(failures)
	return failures
}

func prepareBindProvisionUserSessions(ctx accountManagerContext, tenantSlug string, userSessions map[string]*brandCloudUserSession, concurrency int, logf func(string, ...any)) error {
	if len(userSessions) == 0 {
		return nil
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if concurrency <= 0 {
		concurrency = 1
	}
	emails := make([]string, 0, len(userSessions))
	for email := range userSessions {
		emails = append(emails, email)
	}
	sort.Strings(emails)
	workerCount := concurrency
	if workerCount > len(emails) {
		workerCount = len(emails)
	}
	var logMu sync.Mutex
	safeLog := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logf(format, args...)
	}
	safeLog("preparing bind validation user tokens: users=%d concurrency=%d", len(emails), workerCount)

	var progressMu sync.Mutex
	done := 0
	lastProgressLog := time.Now()
	logProgress := func(force bool) {
		progressMu.Lock()
		defer progressMu.Unlock()
		done++
		if !force && done < len(emails) && done%100 != 0 && time.Since(lastProgressLog) < 10*time.Second {
			return
		}
		lastProgressLog = time.Now()
		safeLog("bind validation user token progress: done=%d/%d", done, len(emails))
	}
	_, err := boundedParallelMap(len(emails), workerCount, func(i int) (struct{}, error) {
		email := emails[i]
		if _, err := brandCloudUserAccessToken(ctx, tenantSlug, userSessions[email], safeLog); err != nil {
			logProgress(false)
			return struct{}{}, err
		}
		logProgress(false)
		return struct{}{}, nil
	})
	return err
}

func fetchBindProvisioningState(ctx accountManagerContext, bearer, brandCloudID string, assignment bindAssignment) (bindProvisioningStateSnapshot, error) {
	endpoint := fmt.Sprintf("%s/v1/orgs/%s/devices/%s/provisioning", ctx.BaseURL, url.PathEscape(brandCloudID), url.PathEscape(assignment.AccountDeviceID))
	body, status, err := curlJSONStatus(endpoint, bearer, nil)
	if err != nil {
		return bindProvisioningStateSnapshot{}, err
	}
	if status != 200 {
		return bindProvisioningStateSnapshot{}, fmt.Errorf("provisioning state HTTP %d%s", status, errorBodySuffix(body))
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return bindProvisioningStateSnapshot{}, err
	}
	readiness, _ := parsed["readiness"].(map[string]any)
	sources, _ := readiness["sources"].(map[string]any)
	operation, _ := parsed["operation"].(map[string]any)
	failure, _ := readiness["failure"].(map[string]any)
	return bindProvisioningStateSnapshot{
		DeviceID:         assignment.DeviceID,
		AccountDeviceID:  assignment.AccountDeviceID,
		AssignedEmail:    assignment.AssignedEmail,
		BindStatus:       assignment.Status,
		ReadinessState:   stringValue(readiness["state"]),
		ProductState:     stringValue(readiness["product_state"]),
		OperationStatus:  firstNonEmpty(stringValue(operation["status"]), stringValue(sources["provisioning_operation_status"])),
		ActivationStatus: stringValue(sources["video_cloud_activation_status"]),
		FailureCode:      stringValue(failure["error_code"]),
		FailureMessage:   stringValue(failure["error_message"]),
	}, nil
}

func snapshotReady(snapshot bindProvisioningStateSnapshot) bool {
	return snapshot.ActivationStatus == "activated" || snapshot.ProductState == "activated" || snapshot.ProductState == "online" || snapshot.ReadinessState == "ready" || snapshot.ReadinessState == "transport_pending"
}

func snapshotFailed(snapshot bindProvisioningStateSnapshot) bool {
	return (snapshot.ProvisioningHTTPError != "" && !isRetryableProvisioningPollError(snapshot.ProvisioningHTTPError)) || snapshot.ReadinessState == "activation_failed" || snapshot.ProductState == "failed" || snapshot.OperationStatus == "failed" || snapshot.ActivationStatus == "failed"
}

func renderBindReport(artifact bindArtifact, result map[string]any) string {
	summary := result["summary"].(map[string]any)
	var b strings.Builder
	fmt.Fprintf(&b, `# Bulk Device Bind Validation Report

- brandname: %s
- brand_cloud_id: %s
- overall: %s
- total_devices: %v
- MQTT-only devices: %v
- Video-capable devices: %v
`, artifact.Brandname, artifact.BrandCloudID, result["overall"], summary["total_devices"], summary["mqtt_only_devices"], summary["video_devices"])
	if categories, ok := result["failure_categories"].(map[string]int); ok && len(categories) > 0 {
		fmt.Fprintf(&b, "\n## Failure Categories\n\n")
		keys := make([]string, 0, len(categories))
		for key := range categories {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(&b, "- %s: %d\n", key, categories[key])
		}
	}
	if failures, ok := result["failures"].([]string); ok && len(failures) > 0 {
		fmt.Fprintf(&b, "\n## Failure Samples\n\n")
		limit := len(failures)
		if limit > 20 {
			limit = 20
		}
		for i := 0; i < limit; i++ {
			fmt.Fprintf(&b, "- %s\n", failures[i])
		}
		if len(failures) > limit {
			fmt.Fprintf(&b, "- ... %d more\n", len(failures)-limit)
		}
	}
	return b.String()
}

func runUnprovisionDevices(args []string) error {
	fs := flag.NewFlagSet("unprovision-devices", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	brandname := fs.String("brandname", "", "brand name")
	bindPath := fs.String("bind-artifact", "", "bind artifact")
	count := fs.Int("count", 0, "count")
	dryRun := fs.Bool("dry-run", false, "dry run")
	if err := fs.Parse(args); err != nil {
		return err
	}
	*brandname = strings.TrimSpace(*brandname)
	if *brandname == "" {
		return errors.New("--brandname is required")
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging")
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	slug := brandSlug(*brandname)
	var artifact bindArtifact
	bindAbs := ""
	if *bindPath != "" {
		bindAbs, _ = filepath.Abs(*bindPath)
		if data, err := os.ReadFile(bindAbs); err != nil {
			return err
		} else if err := json.Unmarshal(data, &artifact); err != nil {
			return err
		}
	} else {
		artifact, err = readBindArtifactFromTestData(envRoot, *brandname)
		if err != nil {
			return err
		}
	}
	if artifact.Brandname != *brandname {
		return fmt.Errorf("--bind-artifact brandname %s does not match --brandname %s", artifact.Brandname, *brandname)
	}
	if artifact.BrandCloudID == "" {
		return errors.New("test-data bindings missing brand_cloud_id")
	}
	usersFile := artifact.Inputs.UsersFile
	usersAbs := ""
	var users map[string]userCredential
	if usersFile != "" {
		usersAbs, _ = filepath.Abs(usersFile)
		users, err = readUsersFile(usersAbs)
		if err != nil {
			return err
		}
	} else {
		users, _, err = readUsersListFromTestData(envRoot, *brandname)
		if err != nil {
			return err
		}
	}
	if *count == 0 {
		*count = len(artifact.Assignments)
	}
	if *count <= 0 || *count > len(artifact.Assignments) {
		return fmt.Errorf("--count %d exceeds bind assignment count %d", *count, len(artifact.Assignments))
	}
	plan := artifact.Assignments[:*count]
	if *dryRun {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"action": "dry_run", "brandname": *brandname, "brand_cloud_id": artifact.BrandCloudID, "count": *count, "bind_artifact": bindAbs, "users_file": usersAbs, "test_data_db": testDataDBPath(envRoot, *brandname), "assignments": plan})
	}
	ctx, err := accountManagerContextFromFlags(*workspaceFlag, *envRootFlag)
	if err != nil {
		return err
	}
	logUnprovision("workspace=%s", workspace)
	logUnprovision("env_root=%s", envRoot)
	logUnprovision("test_data_db=%s", testDataDBPath(envRoot, *brandname))
	if err := preflightUnprovision(ctx, artifact.BrandCloudID, plan[0], users); err != nil {
		return err
	}
	tokens := map[string]string{}
	for _, assignment := range plan {
		if tokens[assignment.AssignedEmail] != "" {
			continue
		}
		user := users[assignment.AssignedEmail]
		if user.Password == "" {
			return fmt.Errorf("users_file missing password for assigned user: %s", assignment.AssignedEmail)
		}
		logUnprovision("logging in assigned user: email=%s", assignment.AssignedEmail)
		token, err := loginAccountUser(ctx, assignment.AssignedEmail, user.Password)
		if err != nil {
			return err
		}
		tokens[assignment.AssignedEmail] = token
	}
	results := []map[string]any{}
	for _, assignment := range plan {
		logUnprovision("unprovisioning device: device=%s account_device=%s user=%s", assignment.DeviceID, assignment.AccountDeviceID, assignment.AssignedEmail)
		result, err := unprovisionOne(ctx, artifact.BrandCloudID, assignment, tokens[assignment.AssignedEmail])
		if err != nil {
			return err
		}
		results = append(results, result)
	}
	artifactDir := filepath.Join(envRoot, "artifacts", "device-unprovision")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return err
	}
	outFile := filepath.Join(artifactDir, fmt.Sprintf("%s-device-unprovision-%s.json", slug, time.Now().UTC().Format("20060102T150405Z")))
	if err := writeJSON(outFile, map[string]any{"schema": "rtk-cloud-workspace.bulk-device-unprovision/v1", "generated_at": time.Now().UTC().Format(time.RFC3339), "brandname": *brandname, "brand_cloud_id": artifact.BrandCloudID, "count": *count, "inputs": map[string]string{"bind_artifact": bindAbs, "users_file": usersAbs, "test_data_db": testDataDBPath(envRoot, *brandname)}, "assignments": results}); err != nil {
		return err
	}
	_ = os.Chmod(outFile, 0o600)
	logUnprovision("unprovision artifact written: %s", outFile)
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"action": "unprovisioned", "brandname": *brandname, "brand_cloud_id": artifact.BrandCloudID, "count": *count, "unprovisioned": len(results), "artifact_file": outFile})
}

type bindDeviceManifest struct {
	DeviceID            string   `json:"device_id"`
	DeviceType          string   `json:"device_type"`
	DeviceItemProfileID string   `json:"device_item_profile_id,omitempty"`
	DisplayName         string   `json:"display_name"`
	ServiceOptions      []string `json:"service_options"`
}

func runBindDevices(args []string) error {
	fs := flag.NewFlagSet("bind-devices", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	brandname := fs.String("brandname", "", "brand name")
	usersPath := fs.String("users-file", "", "users file")
	devicesDir := fs.String("devices-dir", "", "devices dir")
	count := fs.Int("count", 0, "count")
	claimTTL := fs.Int("claim-ttl-hours", 24, "claim TTL hours")
	concurrency := fs.Int("concurrency", envInt("CLOUD_BIND_DEVICES_CONCURRENCY", 64), "device binding concurrency")
	dryRun := fs.Bool("dry-run", false, "dry run")
	skipBootstrap := fs.Bool("skip-bootstrap", false, "skip bootstrap")
	skipDirectProvisionBridge := fs.Bool("skip-direct-provision-bridge", false, "skip staging direct provisioning bridge")
	if err := fs.Parse(args); err != nil {
		return err
	}
	*brandname = strings.TrimSpace(*brandname)
	if *brandname == "" {
		return errors.New("--brandname is required")
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required; pass the environment directory explicitly, for example --env-root cloud_env/staging")
	}
	if *claimTTL <= 0 {
		return errors.New("--claim-ttl-hours must be greater than zero")
	}
	if *concurrency <= 0 {
		return errors.New("--concurrency must be greater than zero")
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	slug := brandSlug(*brandname)
	if *devicesDir == "" {
		*devicesDir = filepath.Join(envRoot, "devices", "test_device")
	}
	usersAbs := ""
	if *usersPath != "" {
		usersAbs, _ = filepath.Abs(*usersPath)
	}
	devicesAbs, _ := filepath.Abs(*devicesDir)
	var users map[string]userCredential
	var usersList []userCredential
	if usersAbs != "" {
		users, usersList, err = readUsersList(usersAbs)
	} else {
		users, usersList, err = readUsersListFromTestData(envRoot, *brandname)
	}
	if err != nil {
		return err
	}
	if len(usersList) == 0 {
		return fmt.Errorf("no users found for brand %s in SQLite test data or --users-file", *brandname)
	}
	var devices []bindDeviceManifest
	if *devicesDir != "" && exists(filepath.Join(devicesAbs, "manifests", "devices.json")) {
		devices, err = readDeviceManifest(filepath.Join(devicesAbs, "manifests", "devices.json"))
	} else {
		devices, err = readDeviceManifestFromTestData(envRoot, *brandname)
	}
	if err != nil {
		return err
	}
	if *count == 0 {
		*count = len(devices)
	}
	if *count <= 0 || *count > len(devices) {
		return fmt.Errorf("--count %d exceeds device manifest count %d", *count, len(devices))
	}
	productProfiles, err := envJSONTextMap("FACTORY_ENROLL_DEVICE_ITEM_PROFILE_ID_BY_DEVICE_TYPE")
	if err != nil {
		return err
	}
	assignments := buildBindAssignmentsWithProductProfiles(devices[:*count], usersList, productProfiles, strings.TrimSpace(os.Getenv("FACTORY_ENROLL_DEVICE_ITEM_PROFILE_ID")))
	if *dryRun {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"action": "dry_run", "brandname": *brandname, "count": *count, "users_file": usersAbs, "devices_dir": devicesAbs, "assignments": assignments})
	}
	ctx, err := accountManagerContextFromFlags(*workspaceFlag, *envRootFlag)
	if err != nil {
		return err
	}
	if !*skipBootstrap {
		if err := accountBootstrap(ctx); err != nil {
			return err
		}
	}
	provisionBridge, err := stagingProvisionBridgeFromEnvRoot(ctx, *skipDirectProvisionBridge)
	if err != nil {
		return err
	}
	if provisionBridge.Enabled {
		logBind("staging direct provisioning bridge enabled: video_base_url=%s account_base_url=%s", provisionBridge.VideoBaseURL, provisionBridge.AccountBaseURL)
	}
	session, err := accountLoginSession(ctx, logBind)
	if err != nil {
		return err
	}
	brandCloud, err := accountFindBrandCloudForLog(ctx, session.AccessToken, *brandname, logBind)
	if err != nil {
		return err
	}
	brandCloudID := stringValue(brandCloud["id"])
	tenantSlug := stringValue(brandCloud["tenant_slug"])
	if tenantSlug == "" {
		return fmt.Errorf("brand cloud response missing tenant_slug for %s", *brandname)
	}
	assignedEmails := []string{}
	seenAssignedEmails := map[string]bool{}
	for _, assignment := range assignments {
		if seenAssignedEmails[assignment.AssignedEmail] {
			continue
		}
		seenAssignedEmails[assignment.AssignedEmail] = true
		assignedEmails = append(assignedEmails, assignment.AssignedEmail)
	}
	loginConcurrency := *concurrency
	if loginConcurrency > 16 {
		loginConcurrency = 16
	}
	logBind("preparing assigned user sessions: count=%d concurrency=%d", len(assignedEmails), loginConcurrency)
	userTokenResults, err := boundedParallelMap(len(assignedEmails), loginConcurrency, func(i int) (struct {
		email    string
		password string
		session  accountPlatformSession
	}, error) {
		email := assignedEmails[i]
		user := users[email]
		userSession := user.Tokens
		var err error
		if userSession.AccessToken != "" || userSession.RefreshToken != "" {
			session := &brandCloudUserSession{Email: email, Password: user.Password, Session: userSession}
			if _, err = brandCloudUserAccessToken(ctx, tenantSlug, session, logBind); err == nil {
				userSession = session.Session
			}
		} else {
			logBind("logging in assigned user: email=%s", email)
			userSession, err = loginBrandCloudUserSession(ctx, tenantSlug, email, user.Password)
		}
		return struct {
			email    string
			password string
			session  accountPlatformSession
		}{email: email, password: user.Password, session: userSession}, err
	})
	if err != nil {
		return err
	}
	userSessions := map[string]*brandCloudUserSession{}
	for _, result := range userTokenResults {
		session := result.session
		userSessions[result.email] = &brandCloudUserSession{Email: result.email, Password: result.password, Session: session}
	}
	runID := time.Now().UTC().Format("20060102T150405Z")
	store, err := openTestDataStore(envRoot, *brandname)
	if err != nil {
		return err
	}
	defer store.Close()
	var sessionMu sync.Mutex
	var logMu sync.Mutex
	var checkpointMu sync.Mutex
	safeLog := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logBind(format, args...)
	}
	checkpointBinding := func(assignment bindAssignment) error {
		checkpointMu.Lock()
		defer checkpointMu.Unlock()
		return store.UpsertBinding(*brandname, brandCloudID, tenantSlug, runID, assignment)
	}
	claimEvidenceCount, err := bindClaimEvidenceCount(len(assignments))
	if err != nil {
		return err
	}
	bulkResults := map[string]accountBulkBindDeviceResult{}
	claimBind := func(items []bindAssignment) (map[string]accountBulkBindDeviceResult, accountBulkBindSummary, error) {
		return accountBindDevicesViaClaimResolve(ctx, &session, &sessionMu, safeLog, brandCloudID, tenantSlug, items, userSessions, runID, *concurrency)
	}
	bulkBind := func(items []bindAssignment) (map[string]accountBulkBindDeviceResult, accountBulkBindSummary, error) {
		return accountBulkBindDevicesInChunks(ctx, &session, &sessionMu, safeLog, brandCloudID, items, bindDevicesBulkChunkSize())
	}
	bulkResults, claimSummary, bulkSummary, err := bindAssignmentsForQualification(assignments, claimEvidenceCount, claimBind, bulkBind)
	if err != nil {
		artifactDir := filepath.Join(envRoot, "artifacts", "device-bind")
		_ = os.MkdirAll(artifactDir, 0o755)
		failedPath := filepath.Join(artifactDir, fmt.Sprintf("%s-device-bind-failed-%s.json", slug, runID))
		_ = writeJSON(failedPath, map[string]any{"schema": "rtk-cloud-workspace.bulk-device-bind-failed/v1", "brandname": *brandname, "brand_cloud_id": brandCloudID, "run_id": runID, "error": err.Error(), "results": bulkResults})
		return err
	}
	if claimSummary != nil {
		safeLog("claim evidence bind complete: requested=%d created=%d failed=%d", claimSummary.Requested, claimSummary.Created, claimSummary.Failed)
	}
	if bulkSummary != nil {
		safeLog("bulk bind complete: requested=%d created=%d existing=%d failed=%d chunks=%d", bulkSummary.Requested, bulkSummary.Created, bulkSummary.Existing, bulkSummary.Failed, bulkSummary.Chunks)
	}
	var progressMu sync.Mutex
	done := 0
	skipped := 0
	createdDevices := 0
	provisionStarted := 0
	progress := func(skippedDelta, createdDelta, provisionDelta int) {
		progressMu.Lock()
		defer progressMu.Unlock()
		done++
		skipped += skippedDelta
		createdDevices += createdDelta
		provisionStarted += provisionDelta
		safeLog("bind progress: done=%d/%d bulk_created=%d provision_started=%d skipped=%d", done, len(assignments), createdDevices, provisionStarted, skipped)
	}
	safeLog("device bind concurrency=%d", *concurrency)
	provisionBound := func(assignment bindAssignment, bulkResult accountBulkBindDeviceResult, userSession *brandCloudUserSession) (bindAssignment, error) {
		if bulkResult.AccountDeviceID == "" {
			return bindAssignment{}, fmt.Errorf("bulk bind result missing account_device_id: device=%s", assignment.DeviceID)
		}
		assignment.ClaimID = bulkResult.ClaimID
		assignment.AccountDeviceID = bulkResult.AccountDeviceID
		prov := bulkResult.ProvisionInput
		if len(prov) == 0 {
			prov = provisionInputForAssignment(assignment)
		}
		opID := fmt.Sprintf("bulk-bind-%s-%s", runID, assignment.DeviceID)
		safeLog("starting provision: device=%s account_device=%s", assignment.DeviceID, assignment.AccountDeviceID)
		if err := startProvisionWithBrandCloudUserRetry(ctx, tenantSlug, brandCloudID, assignment, opID, prov, userSession, safeLog); err != nil {
			return bindAssignment{}, err
		}
		assignment.OperationID = opID
		assignment.Status = "provision_requested"
		if provisionBridge.Enabled {
			safeLog("completing staging direct provisioning bridge: device=%s account_device=%s", assignment.DeviceID, assignment.AccountDeviceID)
			if err := completeStagingProvisionBridge(provisionBridge, brandCloudID, assignment, opID, prov); err != nil {
				return bindAssignment{}, err
			}
			assignment.Status = "provisioned"
		}
		return assignment, nil
	}
	recreateAfterUnprovision := func(assignment bindAssignment, userSession *brandCloudUserSession) (bindAssignment, error) {
		results, _, err := accountRegisterDevicesDirect(ctx, brandCloudID, tenantSlug, []bindAssignment{assignment}, map[string]*brandCloudUserSession{assignment.AssignedEmail: userSession}, safeLog, 1)
		if err != nil {
			return bindAssignment{}, err
		}
		result := results[assignment.DeviceID]
		if result.Status == "failed" {
			return bindAssignment{}, fmt.Errorf("bulk bind recreate failed: device=%s code=%s message=%s", assignment.DeviceID, result.ErrorCode, result.ErrorMessage)
		}
		return provisionBound(assignment, result, userSession)
	}
	results, err := boundedParallelMap(len(assignments), *concurrency, func(i int) (bindAssignment, error) {
		assignment := assignments[i]
		safeLog("binding device %d/%d: device=%s user=%s services=%s", i+1, len(assignments), assignment.DeviceID, assignment.AssignedEmail, strings.Join(assignment.ServiceOptions, ","))
		userSession := userSessions[assignment.AssignedEmail]
		if userSession == nil {
			return bindAssignment{}, fmt.Errorf("missing assigned user session: %s", assignment.AssignedEmail)
		}
		bulkResult, exists := bulkResults[assignment.DeviceID]
		if !exists {
			return bindAssignment{}, fmt.Errorf("missing bulk bind result: device=%s", assignment.DeviceID)
		}
		if bulkResult.Status == "failed" {
			return bindAssignment{}, fmt.Errorf("bulk bind item failed: device=%s code=%s message=%s", assignment.DeviceID, bulkResult.ErrorCode, bulkResult.ErrorMessage)
		}
		if bulkResult.Status == "existing" {
			existingDevice := bulkResult.Device
			assignment.AccountDeviceID = stringValue(existingDevice["id"])
			if err := validateExistingBoundDeviceCompatible(existingDevice, assignment); err != nil {
				safeLog("device already bound but incompatible with current assignment; unprovisioning before fresh bind: device=%s account_device=%s reason=%s", assignment.DeviceID, assignment.AccountDeviceID, err)
				token, err := brandCloudUserAccessToken(ctx, tenantSlug, userSession, safeLog)
				if err != nil {
					return bindAssignment{}, err
				}
				if _, err := unprovisionOne(ctx, brandCloudID, assignment, token); err != nil {
					return bindAssignment{}, err
				}
				assignment.AccountDeviceID = ""
				assignment.Status = ""
				rebound, err := recreateAfterUnprovision(assignment, userSession)
				if err != nil {
					return bindAssignment{}, err
				}
				if err := checkpointBinding(rebound); err != nil {
					return bindAssignment{}, err
				}
				progress(0, 1, 1)
				return rebound, nil
			}
			assignment.Status = "already_bound"
			repaired, provisioned, err := repairExistingBoundDeviceProvisioning(ctx, tenantSlug, brandCloudID, assignment, existingDevice, runID, provisionBridge, userSession, safeLog)
			if err != nil {
				return bindAssignment{}, err
			}
			if err := checkpointBinding(repaired); err != nil {
				return bindAssignment{}, err
			}
			if provisioned {
				progress(0, 0, 1)
			} else {
				progress(1, 0, 0)
			}
			return repaired, nil
		}
		assignment, err := provisionBound(assignment, bulkResult, userSession)
		if err != nil {
			return bindAssignment{}, err
		}
		if err := checkpointBinding(assignment); err != nil {
			return bindAssignment{}, err
		}
		progress(0, 1, 1)
		return assignment, nil
	})
	if err != nil {
		return err
	}
	if err := store.ReplaceBindings(*brandname, brandCloudID, tenantSlug, runID, results); err != nil {
		return err
	}
	if updated, err := store.UpdateUserTokens(*brandname, userSessions); err != nil {
		return fmt.Errorf("update users tokens in test data DB: %w", err)
	} else if updated > 0 {
		logBind("updated users test-data tokens: count=%d db=%s", updated, store.Path)
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"action": "bound", "brandname": *brandname, "brand_cloud_id": brandCloudID, "count": *count, "created_devices": createdDevices, "provision_started": provisionStarted, "already_bound": skipped, "test_data_db": store.Path})
}

func runRefreshUserTokens(args []string) error {
	fs := flag.NewFlagSet("refresh-user-tokens", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	usersFileFlag := fs.String("users-file", "", "users artifact")
	brandname := fs.String("brandname", "", "brand name")
	concurrency := fs.Int("concurrency", envInt("CLOUD_REFRESH_USER_TOKENS_CONCURRENCY", 16), "refresh concurrency")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *usersFileFlag == "" {
		return errors.New("--users-file is required")
	}
	if *concurrency <= 0 {
		return errors.New("--concurrency must be greater than zero")
	}
	ctx, err := accountManagerContextFromFlags(*workspaceFlag, *envRootFlag)
	if err != nil {
		return err
	}
	usersAbs, _ := filepath.Abs(*usersFileFlag)
	var artifact struct {
		Brandname  string           `json:"brandname"`
		TenantSlug string           `json:"tenant_slug"`
		Users      []userCredential `json:"users"`
	}
	raw, err := os.ReadFile(usersAbs)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, &artifact); err != nil {
		return err
	}
	if *brandname != "" && artifact.Brandname != "" && *brandname != artifact.Brandname {
		return fmt.Errorf("--brandname %s does not match users artifact brandname %s", *brandname, artifact.Brandname)
	}
	tenantSlug := artifact.TenantSlug
	if tenantSlug == "" {
		effectiveBrandname := firstNonEmpty(*brandname, artifact.Brandname)
		if effectiveBrandname == "" {
			return errors.New("users artifact missing tenant_slug; pass --brandname to look it up")
		}
		session, err := accountLoginSession(ctx, func(string, ...any) {})
		if err != nil {
			return err
		}
		brandCloud, err := accountFindBrandCloud(ctx, session.AccessToken, effectiveBrandname)
		if err != nil {
			return err
		}
		tenantSlug = stringValue(brandCloud["tenant_slug"])
		if tenantSlug == "" {
			return fmt.Errorf("brand cloud lookup did not return tenant_slug for %s", effectiveBrandname)
		}
	}
	type tokenRefreshResult struct {
		email     string
		password  string
		session   accountPlatformSession
		refreshed bool
		loggedIn  bool
	}
	results, err := boundedParallelMap(len(artifact.Users), *concurrency, func(i int) (tokenRefreshResult, error) {
		user := artifact.Users[i]
		if user.Email == "" {
			return tokenRefreshResult{}, errors.New("users artifact contains user without email")
		}
		if user.Password == "" {
			return tokenRefreshResult{}, fmt.Errorf("users artifact missing password for %s", user.Email)
		}
		before := user.Tokens
		session := &brandCloudUserSession{Email: user.Email, Password: user.Password, Session: user.Tokens}
		if _, err := brandCloudUserAccessToken(ctx, tenantSlug, session, func(string, ...any) {}); err != nil {
			return tokenRefreshResult{}, err
		}
		return tokenRefreshResult{
			email:     user.Email,
			password:  user.Password,
			session:   session.Session,
			refreshed: before.RefreshToken != "" && before.AccessToken != session.Session.AccessToken,
			loggedIn:  before.AccessToken == "" && before.RefreshToken == "",
		}, nil
	})
	if err != nil {
		return err
	}
	sessions := map[string]*brandCloudUserSession{}
	refreshed := 0
	loggedIn := 0
	for _, result := range results {
		sessions[result.email] = &brandCloudUserSession{Email: result.email, Password: result.password, Session: result.session}
		if result.refreshed {
			refreshed++
		}
		if result.loggedIn {
			loggedIn++
		}
	}
	updated, err := updateUsersArtifactTokens(usersAbs, sessions)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"action":      "refreshed_user_tokens",
		"users_file":  usersAbs,
		"tenant_slug": tenantSlug,
		"count":       len(artifact.Users),
		"updated":     updated,
		"refreshed":   refreshed,
		"logged_in":   loggedIn,
	})
}

func readUsersList(path string) (map[string]userCredential, []userCredential, error) {
	var parsed struct {
		Users []userCredential `json:"users"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, nil, err
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, nil, err
	}
	byEmail := map[string]userCredential{}
	for _, user := range parsed.Users {
		byEmail[user.Email] = user
	}
	return byEmail, parsed.Users, nil
}

func readDeviceManifest(path string) ([]bindDeviceManifest, error) {
	var devices []bindDeviceManifest
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return devices, json.Unmarshal(data, &devices)
}

func buildBindAssignments(devices []bindDeviceManifest, users []userCredential) []bindAssignment {
	profiles, _ := envJSONTextMap("FACTORY_ENROLL_DEVICE_ITEM_PROFILE_ID_BY_DEVICE_TYPE")
	return buildBindAssignmentsWithProductProfiles(devices, users, profiles, strings.TrimSpace(os.Getenv("FACTORY_ENROLL_DEVICE_ITEM_PROFILE_ID")))
}

func buildBindAssignmentsWithProductProfiles(devices []bindDeviceManifest, users []userCredential, profiles map[string]string, defaultProfile string) []bindAssignment {
	out := make([]bindAssignment, len(devices))
	offset := 0
	for _, typ := range loadDeviceTypes {
		indexes := []int{}
		for i, device := range devices {
			if device.DeviceType == typ.Name {
				indexes = append(indexes, i)
			}
		}
		for j, deviceIndex := range indexes {
			device := devices[deviceIndex]
			userIndex := (offset + j) % len(users)
			category := loadDeviceCategory(loadDeviceType{Name: device.DeviceType, ServiceOptions: device.ServiceOptions})
			out[deviceIndex] = bindAssignment{AssignmentIndex: deviceIndex, AssignedEmail: users[userIndex].Email, DeviceID: device.DeviceID, DeviceType: device.DeviceType, Category: category, ServiceOptions: device.ServiceOptions, ProductID: firstNonEmpty(device.DeviceItemProfileID, profiles[device.DeviceType], defaultProfile)}
		}
		if len(indexes) > 0 {
			offset = (offset + len(indexes)) % len(users)
		}
	}
	return out
}

func loadDeviceCategory(deviceType loadDeviceType) string {
	if contains(deviceType.ServiceOptions, "video_streaming") || contains(deviceType.ServiceOptions, "video_storage") {
		return "ip_camera"
	}
	return "mqtt_device"
}

func loadDeviceTypeNames() []string {
	names := make([]string, 0, len(loadDeviceTypes))
	for _, typ := range loadDeviceTypes {
		names = append(names, typ.Name)
	}
	return names
}

func accountFindBrandCloudForLog(ctx accountManagerContext, token, brandname string, logf func(string, ...any)) (map[string]any, error) {
	logf("checking brand cloud: name=%s", brandname)
	list, err := accountListBrandClouds(ctx, token, 200)
	if err != nil {
		return nil, err
	}
	for _, item := range anySlice(list["brand_clouds"]) {
		obj, _ := item.(map[string]any)
		metadata, _ := obj["metadata"].(map[string]any)
		if obj["name"] == brandname || metadata["brandname"] == brandname {
			logf("brand cloud found: id=%s", stringValue(obj["id"]))
			return obj, nil
		}
	}
	return nil, fmt.Errorf("brand cloud not found: %s", brandname)
}

type accountBulkBindSummary struct {
	Requested int
	Created   int
	Existing  int
	Failed    int
	Chunks    int
}

type accountBulkBindDeviceResult struct {
	VideoCloudDevid string
	Status          string
	ClaimID         string
	AccountDeviceID string
	Device          map[string]any
	ProvisionInput  map[string]any
	ErrorCode       string
	ErrorMessage    string
}

func bindClaimEvidenceCount(total int) (int, error) {
	if total <= 0 {
		return 0, errors.New("claim qualification requires at least one device")
	}
	raw := strings.TrimSpace(os.Getenv("CLOUD_BIND_DEVICES_CLAIM_EVIDENCE_COUNT"))
	if raw == "" || raw == "0" {
		return total, nil
	}
	count, err := strconv.Atoi(raw)
	if err != nil || count < 0 {
		return 0, fmt.Errorf("CLOUD_BIND_DEVICES_CLAIM_EVIDENCE_COUNT must be a non-negative integer")
	}
	if count != total {
		return 0, fmt.Errorf("claim evidence count %d must cover all %d devices; Account Manager has no admin bulk registry route", count, total)
	}
	return count, nil
}

type accountAssignmentBinder func([]bindAssignment) (map[string]accountBulkBindDeviceResult, accountBulkBindSummary, error)

func bindAssignmentsForQualification(assignments []bindAssignment, claimCount int, claimBind, bulkBind accountAssignmentBinder) (map[string]accountBulkBindDeviceResult, *accountBulkBindSummary, *accountBulkBindSummary, error) {
	results := map[string]accountBulkBindDeviceResult{}
	merge := func(items map[string]accountBulkBindDeviceResult) {
		for deviceID, result := range items {
			results[deviceID] = result
		}
	}
	var claimSummary, bulkSummary *accountBulkBindSummary
	bulkAssignments := assignments
	if claimCount > 0 {
		claimResults, summary, err := claimBind(assignments[:claimCount])
		merge(claimResults)
		if err != nil {
			return results, nil, nil, fmt.Errorf("run-scoped claim evidence bind failed: %w", err)
		}
		claimSummary = &summary
		bulkAssignments = assignments[claimCount:]
	}
	if len(bulkAssignments) > 0 {
		bulkResults, summary, err := bulkBind(bulkAssignments)
		merge(bulkResults)
		if err != nil {
			return results, claimSummary, nil, fmt.Errorf("admin bulk bind failed: %w", err)
		}
		bulkSummary = &summary
	}
	return results, claimSummary, bulkSummary, nil
}

func bindDevicesBulkChunkSize() int {
	size := envInt("CLOUD_BIND_DEVICES_BULK_CHUNK_SIZE", 1000)
	if size <= 0 {
		return 1000
	}
	if size > 5000 {
		return 5000
	}
	return size
}

func accountRegisterDevicesDirect(ctx accountManagerContext, brandCloudID, tenantSlug string, assignments []bindAssignment, userSessions map[string]*brandCloudUserSession, logf func(string, ...any), concurrency int) (map[string]accountBulkBindDeviceResult, accountBulkBindSummary, error) {
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > 16 {
		concurrency = 16
	}
	results := map[string]accountBulkBindDeviceResult{}
	var resultsMu sync.Mutex
	created := 0
	existing := 0
	failed := 0
	_, err := boundedParallelMap(len(assignments), concurrency, func(i int) (struct{}, error) {
		assignment := assignments[i]
		userSession := userSessions[assignment.AssignedEmail]
		if userSession == nil {
			return struct{}{}, fmt.Errorf("missing assigned user session: email=%s device=%s", assignment.AssignedEmail, assignment.DeviceID)
		}
		metadata := map[string]any{
			"video_cloud_devid":           assignment.DeviceID,
			"video_cloud_activity_id":     "bulk-bind-" + assignment.DeviceID,
			"video_cloud_clip_public_key": "bulk-bind-placeholder-public-key",
			"device_type":                 assignment.DeviceType,
			"service_options":             assignment.ServiceOptions,
		}
		payload, err := json.Marshal(map[string]any{
			"name": assignment.DeviceID, "category": assignment.Category,
			"serial_number": assignment.DeviceID, "metadata": metadata,
		})
		if err != nil {
			return struct{}{}, err
		}
		endpoint := fmt.Sprintf("%s/v1/orgs/%s/devices", ctx.BaseURL, url.PathEscape(brandCloudID))
		body, status, err := curlJSONStatusWithBrandCloudUserRetryLocked(ctx, tenantSlug, userSession, logf, "create registry device", func(userToken string) ([]byte, int, error) {
			return curlJSONStatus(endpoint, userToken, payload)
		})
		if err != nil {
			return struct{}{}, err
		}
		var result accountBulkBindDeviceResult
		switch status {
		case http.StatusCreated:
			var parsed struct {
				Device map[string]any `json:"device"`
			}
			if err := json.Unmarshal(body, &parsed); err != nil {
				return struct{}{}, err
			}
			result = accountBulkBindDeviceResult{
				VideoCloudDevid: assignment.DeviceID, Status: "created",
				AccountDeviceID: stringValue(parsed.Device["id"]), Device: parsed.Device,
				ProvisionInput: provisionInputForAssignment(assignment),
			}
			if result.AccountDeviceID == "" {
				return struct{}{}, fmt.Errorf("create device response missing device.id: device=%s", assignment.DeviceID)
			}
		case http.StatusConflict:
			userToken, tokenErr := brandCloudUserAccessToken(ctx, tenantSlug, userSession, logf)
			if tokenErr != nil {
				return struct{}{}, tokenErr
			}
			result, err = accountFindExistingClaimedDevice(ctx, brandCloudID, userToken, assignment)
			if err != nil {
				return struct{}{}, err
			}
		default:
			return struct{}{}, fmt.Errorf("create registry device failed: device=%s HTTP %d%s", assignment.DeviceID, status, errorBodySuffix(body))
		}
		resultsMu.Lock()
		results[assignment.DeviceID] = result
		if result.Status == "created" {
			created++
		} else {
			existing++
		}
		resultsMu.Unlock()
		return struct{}{}, nil
	})
	if err != nil {
		failed = len(assignments) - len(results)
	}
	summary := accountBulkBindSummary{Requested: len(assignments), Created: created, Existing: existing, Failed: failed, Chunks: 1}
	logf("bulk registry bind summary: requested=%d created=%d existing=%d failed=%d", summary.Requested, summary.Created, summary.Existing, summary.Failed)
	return results, summary, err
}

func accountBulkBindDevicesInChunks(ctx accountManagerContext, session *accountPlatformSession, sessionMu *sync.Mutex, logf func(string, ...any), brandCloudID string, assignments []bindAssignment, chunkSize int) (map[string]accountBulkBindDeviceResult, accountBulkBindSummary, error) {
	if chunkSize <= 0 {
		chunkSize = 1000
	}
	results := map[string]accountBulkBindDeviceResult{}
	summary := accountBulkBindSummary{}
	for start := 0; start < len(assignments); start += chunkSize {
		end := start + chunkSize
		if end > len(assignments) {
			end = len(assignments)
		}
		chunk := assignments[start:end]
		chunkResults, chunkSummary, err := accountBulkBindDevicesWithPlatformRetry(ctx, session, sessionMu, logf, brandCloudID, chunk)
		summary.Chunks++
		summary.Requested += chunkSummary.Requested
		summary.Created += chunkSummary.Created
		summary.Existing += chunkSummary.Existing
		summary.Failed += chunkSummary.Failed
		for key, value := range chunkResults {
			results[key] = value
		}
		logf("bulk bind chunk summary: chunk=%d requested=%d created=%d existing=%d failed=%d", summary.Chunks, chunkSummary.Requested, chunkSummary.Created, chunkSummary.Existing, chunkSummary.Failed)
		if err != nil {
			return results, summary, err
		}
		if chunkSummary.Failed > 0 {
			return results, summary, fmt.Errorf("bulk bind chunk %d returned %d failed items", summary.Chunks, chunkSummary.Failed)
		}
	}
	return results, summary, nil
}

func accountBulkBindDevicesWithPlatformRetry(ctx accountManagerContext, session *accountPlatformSession, sessionMu *sync.Mutex, logf func(string, ...any), brandCloudID string, assignments []bindAssignment) (map[string]accountBulkBindDeviceResult, accountBulkBindSummary, error) {
	var parsed accountBulkBindSummary
	var results map[string]accountBulkBindDeviceResult
	body, status, err := curlJSONStatusWithPlatformRetryLocked(ctx, session, sessionMu, logf, "bulk device bind", func(platformToken string) ([]byte, int, error) {
		raw, marshalErr := json.Marshal(map[string]any{"items": bulkBindRequestItems(assignments)})
		if marshalErr != nil {
			return nil, 0, marshalErr
		}
		return curlJSONStatus(fmt.Sprintf("%s/v1/admin/brand-clouds/%s/device-bind-jobs", ctx.BaseURL, brandCloudID), platformToken, raw)
	})
	if err != nil {
		return nil, parsed, err
	}
	if status != 200 {
		return nil, parsed, fmt.Errorf("bulk device bind failed: HTTP %d%s", status, errorBodySuffix(body))
	}
	results, parsed, err = parseAccountBulkBindResponse(body)
	return results, parsed, err
}

func accountBulkBindDevices(ctx accountManagerContext, token, brandCloudID string, assignments []bindAssignment) (map[string]accountBulkBindDeviceResult, accountBulkBindSummary, error) {
	raw, err := json.Marshal(map[string]any{"items": bulkBindRequestItems(assignments)})
	if err != nil {
		return nil, accountBulkBindSummary{}, err
	}
	body, status, err := curlJSONStatus(fmt.Sprintf("%s/v1/admin/brand-clouds/%s/device-bind-jobs", ctx.BaseURL, brandCloudID), token, raw)
	if err != nil {
		return nil, accountBulkBindSummary{}, err
	}
	if status != 200 {
		return nil, accountBulkBindSummary{}, fmt.Errorf("bulk device bind failed: HTTP %d%s", status, errorBodySuffix(body))
	}
	return parseAccountBulkBindResponse(body)
}

func accountBindDevicesViaClaimResolve(ctx accountManagerContext, session *accountPlatformSession, sessionMu *sync.Mutex, logf func(string, ...any), brandCloudID, tenantSlug string, assignments []bindAssignment, userSessions map[string]*brandCloudUserSession, runID string, concurrency int) (map[string]accountBulkBindDeviceResult, accountBulkBindSummary, error) {
	if concurrency <= 0 {
		concurrency = 1
	}
	if concurrency > 16 {
		concurrency = 16
	}
	results := map[string]accountBulkBindDeviceResult{}
	var resultsMu sync.Mutex
	var existingMu sync.Mutex
	existingByOrg := map[string]map[string]accountBulkBindDeviceResult{}
	var progressMu sync.Mutex
	done := 0
	failed := 0
	expiresAt := time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339Nano)
	_, err := boundedParallelMap(len(assignments), concurrency, func(i int) (struct{}, error) {
		assignment := assignments[i]
		userSession := userSessions[assignment.AssignedEmail]
		if userSession == nil {
			return struct{}{}, fmt.Errorf("missing assigned user session: email=%s device=%s", assignment.AssignedEmail, assignment.DeviceID)
		}
		claimToken := fmt.Sprintf("loadtest-%s-%s", runID, assignment.DeviceID)
		activityID := fmt.Sprintf("bulk-bind-%s-%s", runID, assignment.DeviceID)
		claimRequest := map[string]any{
			"organization_id":   brandCloudID,
			"claim_token":       claimToken,
			"category":          assignment.Category,
			"video_cloud_devid": assignment.DeviceID,
			"activity_id":       activityID,
			"clip_public_key":   "bulk-bind-placeholder-public-key",
			"service_options":   assignment.ServiceOptions,
			"expires_at":        expiresAt,
			"metadata": map[string]any{
				"source":      "rtk-cloud bind-devices claim fallback",
				"run_id":      runID,
				"device_type": assignment.DeviceType,
			},
		}
		if assignment.ProductID != "" {
			claimRequest["device_item_profile_id"] = assignment.ProductID
		}
		createPayload, err := json.Marshal(claimRequest)
		if err != nil {
			return struct{}{}, err
		}
		body, status, err := curlJSONStatusWithPlatformRetryLocked(ctx, session, sessionMu, logf, "claim token create", func(platformToken string) ([]byte, int, error) {
			return curlJSONStatus(ctx.BaseURL+"/v1/admin/device-claim-tokens", platformToken, createPayload)
		})
		if err != nil {
			return struct{}{}, err
		}
		if status != http.StatusCreated {
			return struct{}{}, fmt.Errorf("claim token create failed: device=%s HTTP %d%s", assignment.DeviceID, status, errorBodySuffix(body))
		}
		userToken, err := brandCloudUserAccessToken(ctx, tenantSlug, userSession, logf)
		if err != nil {
			return struct{}{}, err
		}
		resolvePayload, err := json.Marshal(map[string]any{
			"claim_token": claimToken,
			"device_name": assignment.DeviceID,
		})
		if err != nil {
			return struct{}{}, err
		}
		body, status, err = curlJSONStatus(fmt.Sprintf("%s/v1/orgs/%s/devices/claim/resolve", ctx.BaseURL, url.PathEscape(brandCloudID)), userToken, resolvePayload)
		if err != nil {
			return struct{}{}, err
		}
		if status != http.StatusCreated {
			if status == http.StatusConflict && accountClaimResolveAlreadyClaimed(body) {
				existingMu.Lock()
				byDevice, loaded := existingByOrg[brandCloudID]
				if !loaded {
					byDevice, err = accountListExistingClaimedDevices(ctx, brandCloudID, userToken, assignment)
					if err == nil {
						existingByOrg[brandCloudID] = byDevice
					}
				}
				result, found := byDevice[assignment.DeviceID]
				existingMu.Unlock()
				var lookupErr error
				if !found {
					lookupErr = fmt.Errorf("existing claimed device not found by metadata.video_cloud_devid")
				}
				if lookupErr != nil {
					return struct{}{}, fmt.Errorf("claim resolve already claimed but existing device lookup failed: device=%s: %w", assignment.DeviceID, lookupErr)
				}
				result.VideoCloudDevid = assignment.DeviceID
				result.ProvisionInput = provisionInputForAssignment(assignment)
				resultsMu.Lock()
				results[assignment.DeviceID] = result
				resultsMu.Unlock()
				progressMu.Lock()
				done++
				if done%100 == 0 || done == len(assignments) {
					logf("claim resolve fallback progress: done=%d/%d failed=%d", done, len(assignments), failed)
				}
				progressMu.Unlock()
				return struct{}{}, nil
			}
			return struct{}{}, fmt.Errorf("claim resolve failed: device=%s HTTP %d%s", assignment.DeviceID, status, errorBodySuffix(body))
		}
		result, err := parseAccountClaimResolveBindResult(body, assignment)
		if err != nil {
			return struct{}{}, err
		}
		resultsMu.Lock()
		results[assignment.DeviceID] = result
		resultsMu.Unlock()
		progressMu.Lock()
		done++
		if done%100 == 0 || done == len(assignments) {
			logf("claim resolve fallback progress: done=%d/%d failed=%d", done, len(assignments), failed)
		}
		progressMu.Unlock()
		return struct{}{}, nil
	})
	if err != nil {
		progressMu.Lock()
		failed++
		progressMu.Unlock()
		return results, accountBulkBindSummary{Requested: len(assignments), Created: len(results), Failed: len(assignments) - len(results), Chunks: 1}, err
	}
	return results, accountBulkBindSummary{Requested: len(assignments), Created: len(results), Failed: 0, Chunks: 1}, nil
}

func accountClaimResolveAlreadyClaimed(body []byte) bool {
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return strings.Contains(strings.ToLower(string(body)), "already claimed")
	}
	if stringValue(parsed["code"]) == "already_claimed" {
		return true
	}
	if errorObject, ok := parsed["error"].(map[string]any); ok {
		return stringValue(errorObject["code"]) == "already_claimed"
	}
	if stringValue(parsed["error"]) == "already_claimed" {
		return true
	}
	return strings.Contains(strings.ToLower(string(body)), "already claimed")
}

func accountFindExistingClaimedDevice(ctx accountManagerContext, brandCloudID, userToken string, assignment bindAssignment) (accountBulkBindDeviceResult, error) {
	devices, err := accountListExistingClaimedDevices(ctx, brandCloudID, userToken, assignment)
	if err != nil {
		return accountBulkBindDeviceResult{}, err
	}
	if result, ok := devices[assignment.DeviceID]; ok {
		return result, nil
	}
	return accountBulkBindDeviceResult{}, fmt.Errorf("existing claimed device not found by metadata.video_cloud_devid")
}

func accountListExistingClaimedDevices(ctx accountManagerContext, brandCloudID, userToken string, assignment bindAssignment) (map[string]accountBulkBindDeviceResult, error) {
	devices := map[string]accountBulkBindDeviceResult{}
	const limit = 200
	for offset := 0; ; offset += limit {
		endpoint := fmt.Sprintf("%s/v1/orgs/%s/devices?limit=%d&offset=%d", ctx.BaseURL, url.PathEscape(brandCloudID), limit, offset)
		body, status, err := curlJSONStatus(endpoint, userToken, nil)
		if err != nil {
			return nil, err
		}
		if status != http.StatusOK {
			return nil, fmt.Errorf("list devices failed: HTTP %d%s", status, errorBodySuffix(body))
		}
		var parsed struct {
			Devices    []map[string]any `json:"devices"`
			Pagination struct {
				Total int `json:"total"`
			} `json:"pagination"`
		}
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, err
		}
		for _, device := range parsed.Devices {
			metadata, _ := device["metadata"].(map[string]any)
			deviceID := stringValue(metadata["video_cloud_devid"])
			if deviceID == "" {
				continue
			}
			accountDeviceID := stringValue(device["id"])
			if accountDeviceID == "" {
				return nil, fmt.Errorf("existing device missing id: device=%s", deviceID)
			}
			devices[deviceID] = accountBulkBindDeviceResult{
				VideoCloudDevid: deviceID,
				Status:          "existing",
				AccountDeviceID: accountDeviceID,
				Device:          device,
				ProvisionInput:  provisionInputForAssignment(assignment),
			}
		}
		if len(parsed.Devices) == 0 || offset+len(parsed.Devices) >= parsed.Pagination.Total {
			break
		}
	}
	return devices, nil
}

func parseAccountClaimResolveBindResult(body []byte, assignment bindAssignment) (accountBulkBindDeviceResult, error) {
	var parsed struct {
		ClaimID        string         `json:"claim_id"`
		Device         map[string]any `json:"device"`
		ProvisionInput map[string]any `json:"provision_input"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return accountBulkBindDeviceResult{}, err
	}
	accountDeviceID := stringValue(parsed.Device["id"])
	if accountDeviceID == "" {
		return accountBulkBindDeviceResult{}, fmt.Errorf("claim resolve response missing device.id: device=%s", assignment.DeviceID)
	}
	if strings.TrimSpace(parsed.ClaimID) == "" {
		return accountBulkBindDeviceResult{}, fmt.Errorf("claim resolve response missing claim_id: device=%s", assignment.DeviceID)
	}
	if parsed.ProvisionInput == nil {
		parsed.ProvisionInput = provisionInputForAssignment(assignment)
	}
	return accountBulkBindDeviceResult{
		VideoCloudDevid: assignment.DeviceID,
		Status:          "created",
		ClaimID:         parsed.ClaimID,
		AccountDeviceID: accountDeviceID,
		Device:          parsed.Device,
		ProvisionInput:  parsed.ProvisionInput,
	}, nil
}

func bulkBindRequestItems(assignments []bindAssignment) []map[string]any {
	items := make([]map[string]any, 0, len(assignments))
	for _, assignment := range assignments {
		items = append(items, map[string]any{
			"device_name":       assignment.DeviceID,
			"category":          assignment.Category,
			"video_cloud_devid": assignment.DeviceID,
			"activity_id":       "bulk-bind-" + assignment.DeviceID,
			"clip_public_key":   "bulk-bind-placeholder-public-key",
			"service_options":   assignment.ServiceOptions,
		})
	}
	return items
}

func parseAccountBulkBindResponse(body []byte) (map[string]accountBulkBindDeviceResult, accountBulkBindSummary, error) {
	var parsed struct {
		Job struct {
			Requested int    `json:"requested"`
			Created   int    `json:"created"`
			Existing  int    `json:"existing"`
			Failed    int    `json:"failed"`
			Status    string `json:"status"`
		} `json:"job"`
		Results []struct {
			VideoCloudDevid string         `json:"video_cloud_devid"`
			Status          string         `json:"status"`
			AccountDeviceID string         `json:"account_device_id"`
			Device          map[string]any `json:"device"`
			ProvisionInput  map[string]any `json:"provision_input"`
			Error           *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, accountBulkBindSummary{}, err
	}
	summary := accountBulkBindSummary{
		Requested: parsed.Job.Requested,
		Created:   parsed.Job.Created,
		Existing:  parsed.Job.Existing,
		Failed:    parsed.Job.Failed,
	}
	results := map[string]accountBulkBindDeviceResult{}
	for _, item := range parsed.Results {
		result := accountBulkBindDeviceResult{
			VideoCloudDevid: item.VideoCloudDevid,
			Status:          item.Status,
			AccountDeviceID: item.AccountDeviceID,
			Device:          item.Device,
			ProvisionInput:  item.ProvisionInput,
		}
		if result.AccountDeviceID == "" {
			result.AccountDeviceID = stringValue(item.Device["id"])
		}
		if item.Error != nil {
			result.ErrorCode = item.Error.Code
			result.ErrorMessage = item.Error.Message
		}
		results[item.VideoCloudDevid] = result
	}
	return results, summary, nil
}

func provisionInputForAssignment(assignment bindAssignment) map[string]any {
	return map[string]any{
		"video_cloud_devid": assignment.DeviceID,
		"activity_id":       "bulk-bind-" + assignment.DeviceID,
		"clip_public_key":   "bulk-bind-placeholder-public-key",
		"service_options":   assignment.ServiceOptions,
	}
}

func repairExistingBoundDeviceProvisioning(ctx accountManagerContext, tenantSlug, brandCloudID string, assignment bindAssignment, existingDevice map[string]any, runID string, bridge stagingProvisionBridge, user *brandCloudUserSession, logf func(string, ...any)) (bindAssignment, bool, error) {
	token, err := brandCloudUserAccessToken(ctx, tenantSlug, user, logf)
	if err != nil {
		return bindAssignment{}, false, err
	}
	snapshot, err := fetchBindProvisioningState(ctx, token, brandCloudID, assignment)
	if err != nil {
		return bindAssignment{}, false, fmt.Errorf("check existing bound provisioning state failed: device=%s account_device=%s: %w", assignment.DeviceID, assignment.AccountDeviceID, err)
	}
	if snapshotReady(snapshot) {
		logf("device already bound and provisioned; skipping claim: device=%s account_device=%s readiness=%s product=%s activation=%s", assignment.DeviceID, assignment.AccountDeviceID, snapshot.ReadinessState, snapshot.ProductState, snapshot.ActivationStatus)
		return assignment, false, nil
	}

	prov, opID := provisionInputFromExistingBoundDevice(existingDevice, assignment, runID)
	logf("device already bound but not provisioned; repairing provision: device=%s account_device=%s readiness=%s product=%s operation=%s activation=%s", assignment.DeviceID, assignment.AccountDeviceID, snapshot.ReadinessState, snapshot.ProductState, snapshot.OperationStatus, snapshot.ActivationStatus)
	if err := startProvisionWithBrandCloudUserRetry(ctx, tenantSlug, brandCloudID, assignment, opID, prov, user, logf); err != nil {
		return bindAssignment{}, false, err
	}
	assignment.OperationID = opID
	assignment.Status = "provision_requested"
	if bridge.Enabled {
		logf("completing staging direct provisioning bridge for existing bound device: device=%s account_device=%s", assignment.DeviceID, assignment.AccountDeviceID)
		if err := completeStagingProvisionBridge(bridge, brandCloudID, assignment, opID, prov); err != nil {
			return bindAssignment{}, false, err
		}
		assignment.Status = "provisioned"
	}
	return assignment, true, nil
}

func validateExistingBoundDeviceCompatible(existingDevice map[string]any, assignment bindAssignment) error {
	metadata, _ := existingDevice["metadata"].(map[string]any)
	if got := stringValue(metadata["video_cloud_devid"]); got != "" && got != assignment.DeviceID {
		return fmt.Errorf("video_cloud_devid mismatch: existing=%s assignment=%s", got, assignment.DeviceID)
	}
	if got := stringValue(existingDevice["category"]); got != "" && got != assignment.Category {
		return fmt.Errorf("category mismatch: existing=%s assignment=%s", got, assignment.Category)
	}
	if got := stringValue(metadata["device_type"]); got != "" && got != assignment.DeviceType {
		return fmt.Errorf("device_type mismatch: existing=%s assignment=%s", got, assignment.DeviceType)
	}
	if existingOptions := stringSliceValue(metadata["service_options"]); len(existingOptions) > 0 && !sameStringSet(existingOptions, assignment.ServiceOptions) {
		return fmt.Errorf("service_options mismatch: existing=%s assignment=%s", strings.Join(sortedStrings(existingOptions), ","), strings.Join(sortedStrings(assignment.ServiceOptions), ","))
	}
	return nil
}

func sameStringSet(a, b []string) bool {
	a = sortedStrings(a)
	b = sortedStrings(b)
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func sortedStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func provisionInputFromExistingBoundDevice(existingDevice map[string]any, assignment bindAssignment, runID string) (map[string]any, string) {
	metadata, _ := existingDevice["metadata"].(map[string]any)
	videoCloudDevid := firstNonEmpty(stringValue(metadata["video_cloud_devid"]), assignment.DeviceID)
	activityID := firstNonEmpty(stringValue(metadata["video_cloud_activity_id"]), "bulk-bind-"+runID+"-"+assignment.DeviceID)
	clipPublicKey := firstNonEmpty(stringValue(metadata["video_cloud_clip_public_key"]), "bulk-bind-placeholder-public-key")
	serviceOptions := assignment.ServiceOptions
	if fromMetadata := stringSliceValue(metadata["service_options"]); len(fromMetadata) > 0 {
		serviceOptions = fromMetadata
	}
	return map[string]any{
		"video_cloud_devid": videoCloudDevid,
		"activity_id":       activityID,
		"clip_public_key":   clipPublicKey,
		"service_options":   serviceOptions,
	}, activityID
}

func stringSliceValue(value any) []string {
	switch items := value.(type) {
	case []string:
		return append([]string(nil), items...)
	case []any:
		out := make([]string, 0, len(items))
		for _, item := range items {
			if s := strings.TrimSpace(stringValue(item)); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func errorBodySuffix(body []byte) string {
	if len(bytes.TrimSpace(body)) == 0 {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err == nil {
		code := stringValue(firstPresent(parsed, "error", "code"))
		message := stringValue(parsed["message"])
		switch {
		case code != "" && message != "":
			return fmt.Sprintf(": %s (%s)", code, message)
		case code != "":
			return ": " + code
		case message != "":
			return ": " + message
		}
	}
	return ": " + strings.TrimSpace(string(body))
}

type stagingProvisionBridge struct {
	Enabled        bool
	AccountBaseURL string
	AccountToken   string
	VideoBaseURL   string
	VideoToken     string
	cleanup        func()
}

func (bridge stagingProvisionBridge) Close() {
	if bridge.cleanup != nil {
		bridge.cleanup()
	}
}

func stagingProvisionBridgeFromEnvRoot(ctx accountManagerContext, skip bool) (stagingProvisionBridge, error) {
	if skip || stagingDirectProvisionBridgeDisabled(os.Getenv("CLOUD_STAGING_DIRECT_PROVISION_BRIDGE")) {
		return stagingProvisionBridge{}, nil
	}
	accountEnv := firstExistingPath(filepath.Join(ctx.EnvRoot, "services", "account-manager", "account-manager.env"), filepath.Join(ctx.EnvRoot, "services", "account-manager", "account-manager-public-staging.env"))
	videoEnv := filepath.Join(ctx.EnvRoot, "services", "video-cloud", "video-cloud-staging.env")
	adminEnv := filepath.Join(ctx.EnvRoot, "services", "cloud-admin", "admin-staging.env")
	stackEnv := filepath.Join(ctx.EnvRoot, "env", "stack.env")

	accountToken := firstNonEmpty(os.Getenv("ACCOUNT_MANAGER_INTERNAL_AUTH_TOKEN"), envFileValue(accountEnv, "ACCOUNT_MANAGER_INTERNAL_AUTH_TOKEN"))
	videoToken := firstNonEmpty(os.Getenv("VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN"), envFileValue(videoEnv, "VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN"), accountToken)
	videoBaseURL := strings.TrimRight(firstNonEmpty(os.Getenv("VIDEO_CLOUD_BASE_URL"), envFileValue(adminEnv, "VIDEO_CLOUD_BASE_URL"), envFileValue(videoEnv, "VIDEO_CLOUD_PUBLIC_API_BASE_URL")), "/")
	if videoBaseURL == "" {
		if domain := envFileValue(stackEnv, "VIDEO_CLOUD_DOMAIN"); domain != "" {
			videoBaseURL = "https://" + domain
		}
	}
	var cleanup func()
	if firstNonEmpty(os.Getenv("CLOUD_PROVIDER"), envFileValue(stackEnv, "CLOUD_PROVIDER")) == "lke" && os.Getenv("VIDEO_CLOUD_BASE_URL") == "" {
		forwardURL, forwardCleanup, err := lkeVideoCloudAPIPortForward(ctx.EnvRoot, map[string]string{
			"CLOUD_STACK_NAME": firstNonEmpty(envFileValue(stackEnv, "CLOUD_STACK_NAME"), "video-cloud-staging"),
		})
		if err != nil {
			return stagingProvisionBridge{}, err
		}
		videoBaseURL = forwardURL
		cleanup = forwardCleanup
	}
	bridge := stagingProvisionBridge{
		Enabled:        true,
		AccountBaseURL: strings.TrimRight(ctx.BaseURL, "/"),
		AccountToken:   accountToken,
		VideoBaseURL:   videoBaseURL,
		VideoToken:     videoToken,
		cleanup:        cleanup,
	}
	missing := []string{}
	if bridge.AccountBaseURL == "" {
		missing = append(missing, "ACCOUNT_MANAGER_BASE_URL")
	}
	if bridge.AccountToken == "" {
		missing = append(missing, "ACCOUNT_MANAGER_INTERNAL_AUTH_TOKEN")
	}
	if bridge.VideoBaseURL == "" {
		missing = append(missing, "VIDEO_CLOUD_BASE_URL")
	}
	if bridge.VideoToken == "" {
		missing = append(missing, "VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN")
	}
	if len(missing) > 0 {
		return stagingProvisionBridge{}, fmt.Errorf("staging direct provisioning bridge missing %s; pass --skip-direct-provision-bridge only when another provisioning transport is active", strings.Join(missing, ", "))
	}
	return bridge, nil
}

func stagingDirectProvisionBridgeDisabled(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "", "1", "true", "yes", "on":
		return false
	case "0", "false", "no", "off":
		return true
	default:
		return false
	}
}

func completeStagingProvisionBridge(bridge stagingProvisionBridge, brandCloudID string, assignment bindAssignment, operationID string, provisionInput map[string]any) error {
	activityID := stringValue(firstPresent(provisionInput, "activity_id"))
	clipPublicKey := stringValue(firstPresent(provisionInput, "clip_public_key"))
	videoCloudDevid := firstNonEmpty(stringValue(firstPresent(provisionInput, "video_cloud_devid")), assignment.DeviceID)
	activatedAt := time.Now().UTC().Format(time.RFC3339)

	videoPayload, _ := json.Marshal(map[string]any{
		"devid":             videoCloudDevid,
		"clip_public_key":   clipPublicKey,
		"activityid":        activityID,
		"org_id":            brandCloudID,
		"account_device_id": assignment.AccountDeviceID,
		"device_type":       firstNonEmpty(assignment.DeviceType, assignment.Category),
		"model":             assignment.Category,
	})
	videoURL := fmt.Sprintf("%s/v1/internal/account-manager/devices/%s/activate", bridge.VideoBaseURL, url.PathEscape(videoCloudDevid))
	body, status, err := curlJSONStatus(videoURL, bridge.VideoToken, videoPayload)
	if err != nil {
		return fmt.Errorf("video direct activation failed: device=%s account_device=%s: %w", assignment.DeviceID, assignment.AccountDeviceID, err)
	}
	if status == http.StatusConflict && isVideoCloudIdentityMismatch(body) {
		if err := completeStagingVideoUnprovisionBridge(bridge, videoCloudDevid); err != nil {
			return fmt.Errorf("video direct activation failed: device=%s account_device=%s HTTP %d%s; video unprovision before retry failed: %w", assignment.DeviceID, assignment.AccountDeviceID, status, errorBodySuffix(body), err)
		}
		body, status, err = curlJSONStatus(videoURL, bridge.VideoToken, videoPayload)
		if err != nil {
			return fmt.Errorf("video direct activation retry failed after unprovision: device=%s account_device=%s: %w", assignment.DeviceID, assignment.AccountDeviceID, err)
		}
	}
	if status != http.StatusOK {
		return fmt.Errorf("video direct activation failed: device=%s account_device=%s HTTP %d%s", assignment.DeviceID, assignment.AccountDeviceID, status, errorBodySuffix(body))
	}

	accountPayload, _ := json.Marshal(map[string]any{
		"operation_id":      operationID,
		"org_id":            brandCloudID,
		"account_device_id": assignment.AccountDeviceID,
		"video_cloud_devid": videoCloudDevid,
		"activity_id":       activityID,
		"activated_at":      activatedAt,
	})
	accountURL := bridge.AccountBaseURL + "/v1/internal/device-provisioning-results"
	body, status, err = curlJSONStatus(accountURL, bridge.AccountToken, accountPayload)
	if err != nil {
		return fmt.Errorf("account direct provisioning result failed: device=%s account_device=%s: %w", assignment.DeviceID, assignment.AccountDeviceID, err)
	}
	if status != http.StatusOK {
		return fmt.Errorf("account direct provisioning result failed: device=%s account_device=%s HTTP %d%s", assignment.DeviceID, assignment.AccountDeviceID, status, errorBodySuffix(body))
	}
	return nil
}

func completeStagingVideoUnprovisionBridge(bridge stagingProvisionBridge, videoCloudDevid string) error {
	payload, _ := json.Marshal(map[string]any{"devid": videoCloudDevid})
	videoURL := fmt.Sprintf("%s/v1/internal/account-manager/devices/%s/unprovision", bridge.VideoBaseURL, url.PathEscape(videoCloudDevid))
	body, status, err := curlJSONStatus(videoURL, bridge.VideoToken, payload)
	if err != nil {
		return err
	}
	if status >= 200 && status < 300 {
		return nil
	}
	return fmt.Errorf("HTTP %d%s", status, errorBodySuffix(body))
}

func isVideoCloudIdentityMismatch(body []byte) bool {
	text := strings.ToLower(string(body))
	return strings.Contains(text, "different account identity") || strings.Contains(text, strings.ToLower("reactivate camera with different account identity"))
}

func startProvision(ctx accountManagerContext, token, brandCloudID string, assignment bindAssignment, operationID string, provisionInput map[string]any) error {
	serviceOptions := assignment.ServiceOptions
	if items, ok := provisionInput["service_options"].([]any); ok {
		serviceOptions = []string{}
		for _, item := range items {
			serviceOptions = append(serviceOptions, stringValue(item))
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"video_cloud_devid": stringValue(firstPresent(provisionInput, "video_cloud_devid")),
		"activity_id":       stringValue(firstPresent(provisionInput, "activity_id")),
		"clip_public_key":   stringValue(firstPresent(provisionInput, "clip_public_key")),
		"operation_id":      operationID,
		"service_options":   serviceOptions,
	})
	_, status, err := curlJSONStatus(fmt.Sprintf("%s/v1/orgs/%s/devices/%s/provision", ctx.BaseURL, brandCloudID, assignment.AccountDeviceID), token, payload)
	if err != nil {
		return err
	}
	if status != 200 && status != 201 && status != 202 {
		return fmt.Errorf("provision start failed: device=%s account_device=%s HTTP %d", assignment.DeviceID, assignment.AccountDeviceID, status)
	}
	return nil
}

func startProvisionWithBrandCloudUserRetry(ctx accountManagerContext, tenantSlug, brandCloudID string, assignment bindAssignment, operationID string, provisionInput map[string]any, user *brandCloudUserSession, logf func(string, ...any)) error {
	serviceOptions := assignment.ServiceOptions
	if items, ok := provisionInput["service_options"].([]any); ok {
		serviceOptions = []string{}
		for _, item := range items {
			serviceOptions = append(serviceOptions, stringValue(item))
		}
	}
	payload, _ := json.Marshal(map[string]any{
		"video_cloud_devid": stringValue(firstPresent(provisionInput, "video_cloud_devid")),
		"activity_id":       stringValue(firstPresent(provisionInput, "activity_id")),
		"clip_public_key":   stringValue(firstPresent(provisionInput, "clip_public_key")),
		"operation_id":      operationID,
		"service_options":   serviceOptions,
	})
	body, status, err := curlJSONStatusWithBrandCloudUserRetryLocked(ctx, tenantSlug, user, logf, "provision start", func(token string) ([]byte, int, error) {
		return curlJSONStatus(fmt.Sprintf("%s/v1/orgs/%s/devices/%s/provision", ctx.BaseURL, brandCloudID, assignment.AccountDeviceID), token, payload)
	})
	if err != nil {
		return err
	}
	if status != 200 && status != 201 && status != 202 {
		return fmt.Errorf("provision start failed: device=%s account_device=%s HTTP %d%s", assignment.DeviceID, assignment.AccountDeviceID, status, errorBodySuffix(body))
	}
	return nil
}

func firstPresent(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := values[key]; ok && value != nil {
			return value
		}
	}
	return nil
}

func logBind(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[cloud-bind-devices %s +%03ds] %s\n", time.Now().Format("15:04:05"), 0, fmt.Sprintf(format, args...))
}

type userCredential struct {
	UserID   string                 `json:"user_id"`
	Email    string                 `json:"email"`
	Password string                 `json:"password"`
	Tokens   accountPlatformSession `json:"tokens,omitempty"`
}

func updateUsersArtifactTokens(path string, sessions map[string]*brandCloudUserSession) (int, error) {
	if len(sessions) == 0 {
		return 0, nil
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	var artifact map[string]any
	if err := json.Unmarshal(raw, &artifact); err != nil {
		return 0, err
	}
	users, ok := artifact["users"].([]any)
	if !ok {
		return 0, errors.New("users artifact missing users array")
	}
	updated := 0
	for _, entry := range users {
		user, ok := entry.(map[string]any)
		if !ok {
			continue
		}
		email := stringValue(user["email"])
		session := sessions[email]
		if session == nil || (session.Session.AccessToken == "" && session.Session.RefreshToken == "") {
			continue
		}
		user["tokens"] = session.Session
		updated++
	}
	if updated == 0 {
		return 0, nil
	}
	if err := writeJSON(path, artifact); err != nil {
		return 0, err
	}
	_ = os.Chmod(path, 0o600)
	return updated, nil
}

func readUsersFile(path string) (map[string]userCredential, error) {
	var parsed struct {
		Users []userCredential `json:"users"`
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &parsed); err != nil {
		return nil, err
	}
	out := map[string]userCredential{}
	for _, user := range parsed.Users {
		out[user.Email] = user
	}
	return out, nil
}

func preflightUnprovision(ctx accountManagerContext, brandCloudID string, assignment bindAssignment, users map[string]userCredential) error {
	user := users[assignment.AssignedEmail]
	logUnprovision("checking Account Manager unprovision API route: email=%s", assignment.AssignedEmail)
	token, err := loginAccountUser(ctx, assignment.AssignedEmail, user.Password)
	if err != nil {
		return err
	}
	payload, _ := json.Marshal(map[string]string{"reason": "route_preflight"})
	body, status, err := curlJSONStatus(fmt.Sprintf("%s/v1/orgs/%s/devices/00000000-0000-0000-0000-000000000000/unprovision", ctx.BaseURL, brandCloudID), token, payload)
	if err != nil {
		return err
	}
	if status == 404 {
		if accountAPIErrorCode(body) == "not_found" {
			logUnprovision("Account Manager unprovision API route is available")
			return nil
		}
		return fmt.Errorf("Account Manager unprovision API route is not deployed at %s; deploy an Account Manager build with /v1/orgs/:orgId/devices/:deviceId/unprovision before running this script", ctx.BaseURL)
	}
	if status == 400 || status == 409 {
		logUnprovision("Account Manager unprovision API route is available")
		return nil
	}
	if status == 403 {
		return fmt.Errorf("assigned user lacks device.unprovision permission in brand cloud: email=%s brand_cloud_id=%s", assignment.AssignedEmail, brandCloudID)
	}
	return fmt.Errorf("unexpected Account Manager unprovision API preflight status: HTTP %d", status)
}

func unprovisionOne(ctx accountManagerContext, brandCloudID string, assignment bindAssignment, token string) (map[string]any, error) {
	payload, _ := json.Marshal(map[string]string{"reason": "user_resale_factory_ready"})
	body, status, err := curlJSONStatus(fmt.Sprintf("%s/v1/orgs/%s/devices/%s/unprovision", ctx.BaseURL, brandCloudID, assignment.AccountDeviceID), token, payload)
	if err != nil {
		return nil, err
	}
	if status != 200 {
		return nil, fmt.Errorf("unprovision failed: device=%s account_device=%s HTTP %d%s", assignment.DeviceID, assignment.AccountDeviceID, status, errorBodySuffix(body))
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, err
	}
	unprov, _ := parsed["unprovision"].(map[string]any)
	return map[string]any{
		"assignment_index":   assignment.AssignmentIndex,
		"assigned_email":     assignment.AssignedEmail,
		"device_id":          assignment.DeviceID,
		"device_type":        assignment.DeviceType,
		"category":           assignment.Category,
		"service_options":    assignment.ServiceOptions,
		"claim_id":           assignment.ClaimID,
		"account_device_id":  assignment.AccountDeviceID,
		"response_device_id": stringValue(unprov["device_id"]),
		"organization_id":    stringValue(unprov["organization_id"]),
		"video_cloud_devid":  stringValue(unprov["video_cloud_devid"]),
		"status":             "unprovisioned",
		"unprovisioned_at":   stringValue(unprov["unprovisioned_at"]),
	}, nil
}

func loginAccountUser(ctx accountManagerContext, email, password string) (string, error) {
	payload, _ := json.Marshal(map[string]string{"email": email, "password": password})
	body, status, err := curlJSONStatus(ctx.BaseURL+"/v1/auth/login", "", payload)
	if err != nil {
		return "", err
	}
	if status != 200 {
		return "", fmt.Errorf("login failed: email=%s HTTP %d", email, status)
	}
	var parsed struct {
		Tokens struct {
			AccessToken string `json:"access_token"`
		} `json:"tokens"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return "", err
	}
	if parsed.Tokens.AccessToken == "" {
		return "", fmt.Errorf("login response did not include an access token: %s", email)
	}
	return parsed.Tokens.AccessToken, nil
}

type brandCloudUserSession struct {
	Email    string
	Password string
	Session  accountPlatformSession
	Mu       sync.Mutex
}

func loginBrandCloudUser(ctx accountManagerContext, tenantSlug, email, password string) (string, error) {
	session, err := loginBrandCloudUserSession(ctx, tenantSlug, email, password)
	if err != nil {
		return "", err
	}
	return session.AccessToken, nil
}

func loginBrandCloudUserSession(ctx accountManagerContext, _ string, email, password string) (accountPlatformSession, error) {
	payload, _ := json.Marshal(map[string]string{"email": email, "password": password})
	loginURL := ctx.BaseURL + "/v1/auth/login"
	body, status, err := curlJSONStatus(loginURL, "", payload)
	if err != nil {
		return accountPlatformSession{}, err
	}
	if status != 200 {
		return accountPlatformSession{}, fmt.Errorf("global user login failed: email=%s HTTP %d%s", email, status, accountAPIErrorSuffix(body))
	}
	session, err := parsePlatformSession(body)
	if err != nil {
		return accountPlatformSession{}, err
	}
	if session.AccessToken == "" {
		return accountPlatformSession{}, fmt.Errorf("global login response did not include an access token: %s", email)
	}
	return session, nil
}

func accountRefreshBrandCloudUserSession(ctx accountManagerContext, _ string, email, refreshToken string, logf func(string, ...any)) (accountPlatformSession, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return accountPlatformSession{}, errors.New("global user refresh token is empty")
	}
	logf("refreshing global user token: email=%s url=%s/v1/auth/refresh", email, ctx.BaseURL)
	payload, _ := json.Marshal(map[string]string{"refresh_token": refreshToken})
	refreshURL := ctx.BaseURL + "/v1/auth/refresh"
	body, status, err := curlJSONStatus(refreshURL, "", payload)
	if err != nil {
		return accountPlatformSession{}, err
	}
	if status != 200 {
		return accountPlatformSession{}, fmt.Errorf("global user token refresh failed: email=%s HTTP %d%s", email, status, accountAPIErrorSuffix(body))
	}
	session, err := parsePlatformSession(body)
	if err != nil {
		return accountPlatformSession{}, err
	}
	if session.AccessToken == "" || session.RefreshToken == "" {
		return accountPlatformSession{}, fmt.Errorf("global user refresh response did not include access and refresh tokens: %s", email)
	}
	logf("global user token refresh ok: email=%s", email)
	return session, nil
}

func brandCloudUserAccessToken(ctx accountManagerContext, tenantSlug string, user *brandCloudUserSession, logf func(string, ...any)) (string, error) {
	if user == nil {
		return "", errors.New("global user session is nil")
	}
	user.Mu.Lock()
	defer user.Mu.Unlock()
	if err := ensureBrandCloudUserSessionFresh(ctx, tenantSlug, user, logf); err != nil {
		return "", err
	}
	return user.Session.AccessToken, nil
}

func ensureBrandCloudUserSessionFresh(ctx accountManagerContext, tenantSlug string, user *brandCloudUserSession, logf func(string, ...any)) error {
	const refreshWindow = 2 * time.Minute
	if expiresAt, ok := jwtExpiresAt(user.Session.AccessToken); user.Session.AccessToken != "" && (!ok || time.Until(expiresAt) > refreshWindow) {
		return nil
	}
	return refreshOrLoginBrandCloudUserSession(ctx, tenantSlug, user, logf)
}

func refreshOrLoginBrandCloudUserSession(ctx accountManagerContext, tenantSlug string, user *brandCloudUserSession, logf func(string, ...any)) error {
	if expiresAt, ok := jwtExpiresAt(user.Session.RefreshToken); user.Session.RefreshToken != "" && (!ok || time.Now().Before(expiresAt)) {
		refreshed, err := accountRefreshBrandCloudUserSession(ctx, tenantSlug, user.Email, user.Session.RefreshToken, logf)
		if err == nil {
			user.Session = refreshed
			return nil
		}
		logf("global user token refresh failed; falling back to login: email=%s error=%v", user.Email, err)
	}
	logf("logging in global user: email=%s", user.Email)
	loggedIn, err := loginBrandCloudUserSession(ctx, tenantSlug, user.Email, user.Password)
	if err != nil {
		return err
	}
	user.Session = loggedIn
	return nil
}

func curlJSONStatusWithBrandCloudUserRetryLocked(ctx accountManagerContext, tenantSlug string, user *brandCloudUserSession, logf func(string, ...any), operation string, call func(string) ([]byte, int, error)) ([]byte, int, error) {
	if logf == nil {
		logf = func(string, ...any) {}
	}
	if user == nil {
		return nil, 0, errors.New("global user session is nil")
	}
	user.Mu.Lock()
	if err := ensureBrandCloudUserSessionFresh(ctx, tenantSlug, user, logf); err != nil {
		user.Mu.Unlock()
		return nil, 0, err
	}
	userToken := user.Session.AccessToken
	user.Mu.Unlock()
	body, status, err := call(userToken)
	if err != nil || status != http.StatusUnauthorized {
		return body, status, err
	}
	logf("%s got HTTP 401; refreshing global user token before retry: email=%s", operation, user.Email)
	user.Mu.Lock()
	if err := refreshOrLoginBrandCloudUserSession(ctx, tenantSlug, user, logf); err != nil {
		user.Mu.Unlock()
		return body, status, err
	}
	userToken = user.Session.AccessToken
	user.Mu.Unlock()
	return call(userToken)
}

func logUnprovision(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "[cloud-unprovision-devices %s +%03ds] %s\n", time.Now().Format("15:04:05"), 0, fmt.Sprintf(format, args...))
}

func writeJSON(path string, value any) error {
	fh, err := os.Create(path)
	if err != nil {
		return err
	}
	defer fh.Close()
	enc := json.NewEncoder(fh)
	enc.SetIndent("", "  ")
	return enc.Encode(value)
}

func contains(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}

func firstExistingPath(paths ...string) string {
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	if len(paths) == 0 {
		return ""
	}
	return paths[len(paths)-1]
}

func envFileValue(path, key string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if ok && name == key {
			return strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	return ""
}

func printUsage() {
	names := make([]string, 0, len(commands))
	for name := range commands {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Fprintln(os.Stderr, "Usage: rtk-cloud <command> [args]")
	fmt.Fprintln(os.Stderr, "Environment-aware commands accept --environment NAME; --env-root is reserved for tests and internal orchestration.")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Commands:")
	for _, name := range names {
		fmt.Fprintf(os.Stderr, "  %s\n", name)
	}
	fmt.Fprintln(os.Stderr, "  ci-runners <command>")
}

func printCIRunnerUsage() {
	names := make([]string, 0, len(ciRunnerCommands))
	for name := range ciRunnerCommands {
		names = append(names, name)
	}
	sort.Strings(names)
	fmt.Fprintln(os.Stderr, "Usage: rtk-cloud ci-runners <command> [args]")
	fmt.Fprintln(os.Stderr)
	fmt.Fprintln(os.Stderr, "Commands:")
	for _, name := range names {
		fmt.Fprintf(os.Stderr, "  %s\n", name)
	}
}

func workspaceRoot() (string, error) {
	if v := os.Getenv("RTK_CLOUD_WORKSPACE"); v != "" {
		return filepath.Abs(v)
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if exists(filepath.Join(dir, ".git")) && exists(filepath.Join(dir, "scripts")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
	return "", errors.New("could not locate workspace root")
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func resolveEnvRoot(workspace, envRoot string) (string, error) {
	if envRoot == "" {
		return "", errors.New("--env-root is required")
	}
	if !filepath.IsAbs(envRoot) {
		envRoot = filepath.Join(workspace, envRoot)
	}
	envRoot = filepath.Clean(envRoot)
	if base := filepath.Base(envRoot); base == "lke" || base == "linode" {
		return "", fmt.Errorf("legacy provider env-root %q is not supported; use cloud_env/<environment>/runtime", envRoot)
	}
	if filepath.Base(envRoot) == "staging" || (exists(filepath.Join(envRoot, "environment.env")) && exists(filepath.Join(envRoot, "deployment.env"))) {
		return filepath.Join(envRoot, "runtime"), nil
	}
	return envRoot, nil
}

func hasFlag(args []string, name string) bool {
	for _, arg := range args {
		if arg == name || strings.HasPrefix(arg, name+"=") {
			return true
		}
	}
	return false
}

func withEnv(base []string, overrides map[string]string) []string {
	values := map[string]string{}
	order := []string{}
	for _, item := range base {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = value
	}
	for key, value := range overrides {
		if _, exists := values[key]; !exists {
			order = append(order, key)
		}
		values[key] = value
	}
	out := make([]string, 0, len(order))
	for _, key := range order {
		out = append(out, key+"="+values[key])
	}
	return out
}
