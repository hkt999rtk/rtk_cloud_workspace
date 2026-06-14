package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
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
	"bind-devices":                {run: runBindDevices},
	"check-certificates":          {run: runCheckCertificates},
	"collect-evidence":            {run: runCollectEvidence},
	"contracts-check":             {run: runContractsCheck},
	"create-brandname-cloud":      {run: runCreateBrandnameCloud},
	"create-users":                {run: runCreateUsers},
	"deploy":                      {run: runDeploy},
	"docs-check":                  {run: runDocsCheck},
	"generate-load-devices":       {run: runGenerateLoadDevices},
	"list-brandname-clouds":       {run: runListBrandnameClouds},
	"logs-check":                  {run: runLogsCheck},
	"lke-build-images":            {run: runLKEBuildImages},
	"migrate-env":                 {run: runMigrateEnv},
	"mqtt-loadtest":               {run: runMQTTLoadTest},
	"mqtt-test":                   {run: runMQTTTest},
	"mqtt-trace-report":           {run: runMQTTTraceReport},
	"platform-admin-token":        {run: runPlatformAdminToken},
	"provision":                   {run: runProvision},
	"provision-k8s":               {run: runProvisionK8s},
	"refresh-user-tokens":         {run: runRefreshUserTokens},
	"remove-k8s":                  {run: runRemoveK8s},
	"secrets-check":               {run: runSecretsCheck},
	"staging-e2e-data-setup":      {run: runStagingE2EDataSetup},
	"staging-e2e-mqtt-log-verify": {run: runStagingE2EMQTTLogVerify},
	"staging-e2e-test":            {run: runStagingE2ETest},
	"status-all":                  {run: runStatusAll},
	"sync-env":                    {run: runSyncEnv},
	"sync-all":                    {run: runSyncAll},
	"test-matrix":                 {run: runTestMatrix},
	"unprovision-devices":         {run: runUnprovisionDevices},
	"validate-device-bind":        {run: runValidateDeviceBind},
	"video-relay-test":            {run: runVideoRelayTest},
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
	profile := fs.String("profile", "smoke", "profile")
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
	concurrency := fs.Int("concurrency", 25, "load-test MQTT probe concurrency")
	maxConnectedDevices := fs.Int("max-connected-devices", 0, "load-test max connected devices in this shard")
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
		childEnv["RTK_CLOUD_MQTT_TEST_VIDEO_BASE_URL"] = videoURL
		childEnv["RTK_CLOUD_MQTT_TEST_ACCOUNT_BASE_URL"] = accountURL
	}
	goCmd, err := exec.LookPath("go")
	if err != nil {
		return errors.New("go is required")
	}
	cmd := exec.Command(goCmd, "run", "./cloud-mqtt-test",
		"--root", workspace,
		"--env-root", resolvedEnv,
		"--brandname", *brandname,
		"--out-dir", *outDir,
		"--profile", *profile,
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
		"--concurrency", strconv.Itoa(*concurrency),
		"--max-connected-devices", strconv.Itoa(*maxConnectedDevices),
	)
	cmd.Dir = filepath.Join(workspace, "scripts", "go")
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
		raw["CLOUD_PROVIDER"] = "linode"
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
	topology := firstExistingPath(filepath.Join(root, "topology", "video-cloud.yaml"), filepath.Join(root, "topology", "video-cloud-staging.yaml"))
	if c, err := syncTopology(topology, raw, derived, check); err != nil {
		return changed, err
	} else if c {
		changed = true
	}
	envUpdates := []struct {
		path string
		keys map[string]string
	}{
		{firstExistingPath(filepath.Join(root, "services", "video-cloud", "video-cloud.env"), filepath.Join(root, "services", "video-cloud", "video-cloud-staging.env")), map[string]string{}},
		{firstExistingPath(filepath.Join(root, "services", "account-manager", "account-manager.env"), filepath.Join(root, "services", "account-manager", "account-manager-public-staging.env")), map[string]string{
			"ACCOUNT_MANAGER_DOMAIN": derived["ACCOUNT_MANAGER_DOMAIN"],
		}},
		{firstExistingPath(filepath.Join(root, "services", "cloud-admin", "admin.env"), filepath.Join(root, "services", "cloud-admin", "admin-staging.env")), map[string]string{
			"CLOUD_ADMIN_DOMAIN": derived["CLOUD_ADMIN_DOMAIN"],
		}},
		{filepath.Join(root, "services", "cloud-logger", "logger.env"), map[string]string{
			"CLOUD_LOGGER_DOMAIN": derived["CLOUD_LOGGER_DOMAIN"],
		}},
	}
	for _, item := range envUpdates {
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
	if token := os.Getenv("LINODE_TOKEN"); token != "" {
		return token
	}
	if envRoot != "" {
		if token := envFileValue(filepath.Join(envRoot, "env", "operator.env"), "LINODE_TOKEN"); token != "" {
			return token
		}
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if token := envFileValue(filepath.Join(home, ".env"), "LINODE_TOKEN"); token != "" {
			return token
		}
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
		"docs/deployment-secrets-governance.md",
		"docs/linode-ci-runners.md",
		"docs/examples/secrets-manifest.example.json",
		"docs/testing.md",
		"docs/LOAD_TEST_REPORT.md",
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
		"scripts/README.zh-TW.md",
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
		"repos/rtk_account_manager/docs/SPEC.md",
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
		".secrets",
		".secrets.backup",
		".secrets/staging/linode/admin/env/admin.env",
		"cloud_env/staging/linode/env/operator.env",
		"cloud_env/staging/linode/keys/root-ca.key.pem",
	} {
		if err := exec.Command("git", "-C", workspace, "check-ignore", "-q", path).Run(); err == nil {
			check.pass(path + " is ignored")
		} else {
			check.fail(path + " is not ignored")
		}
	}

	fmt.Fprintln(os.Stdout)
	fmt.Fprintln(os.Stdout, "== tracked workspace secret scan ==")
	workspacePaths := []string{".gitignore", "README.md", "docs", "scripts", "e2e_test"}
	checkGitGrepNoMatchFiltered(check, workspace, "private key block", `-----BEGIN ([A-Z0-9 ]+ )?PRIVATE KEY-----`, workspacePaths, func(line string) bool {
		return strings.Contains(line, "_test.go:") && strings.Contains(line, `"-----BEGIN PRIVATE KEY-----"`)
	})
	for _, scan := range []struct {
		label   string
		pattern string
	}{
		{"bearer token literal", `Bearer[[:space:]]+[A-Za-z0-9._~+/-]{24,}`},
		{"JWT-like token", `eyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{20,}`},
		{"hard-coded password assignment", `(^|[^A-Za-z0-9_])(PASSWORD|PASS|TOKEN|SECRET|PRIVATE_KEY)[A-Za-z0-9_]*=[^[:space:]<>$][^[:space:]]{7,}`},
	} {
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
	fmt.Fprintln(os.Stdout, "== workspace Go validation ==")
	if err := runCmd(workspace, "git", "diff", "--check"); err != nil {
		return err
	}
	if err := runCmd(workspace, "go", "test", "./scripts/go/..."); err != nil {
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
	{"air_conditioner", "RTC-AC-SIM", "air_conditioner", []string{"mqtt"}, []string{"mqtt", "power", "target_temperature", "mode", "fan", "state_report", "command_result"}},
	{"smart_meter", "RTC-METER-SIM", "smart_meter", []string{"mqtt"}, []string{"mqtt", "status_report", "telemetry_report", "power_watts", "energy_kwh", "voltage", "current"}},
}

type generatedDevice struct {
	DeviceID             string   `json:"device_id"`
	DeviceType           string   `json:"device_type"`
	MQTTCapability       string   `json:"mqtt_capability"`
	ServiceOptions       []string `json:"service_options"`
	Model                string   `json:"model"`
	DisplayName          string   `json:"display_name"`
	FirmwareVersion      string   `json:"firmware_version"`
	Capabilities         []string `json:"capabilities"`
	CertificateProfile   string   `json:"certificate_profile"`
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
	outDir := fs.String("out-dir", "", "output directory")
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	factoryURL := fs.String("factory-url", os.Getenv("FACTORY_ENROLL_URL"), "factory enroll URL")
	factoryAuthKey := fs.String("factory-auth-key", os.Getenv("FACTORY_ENROLL_AUTH_KEY"), "factory enroll auth key")
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
	concurrency := fs.Int("concurrency", envInt("CLOUD_CREATE_DEVICES_CONCURRENCY", 16), "device generation concurrency")
	force := fs.Bool("force", false, "force")
	if err := fs.Parse(args); err != nil {
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
	if *runID == "" {
		*runID = time.Now().UTC().Format("20060102T150405Z")
	}
	if *batchID == "" {
		*batchID = *runID
	}
	if *outDir == "" {
		*outDir = filepath.Join(envRoot, "devices", "test_device")
	}
	if !*generateOnly {
		videoEnv := firstExistingPath(filepath.Join(envRoot, "services", "video-cloud", "video-cloud.env"), filepath.Join(envRoot, "services", "video-cloud", "video-cloud-staging.env"))
		if *factoryURL == "" {
			*factoryURL = envFileValue(videoEnv, "FACTORY_ENROLL_URL")
		}
		if *factoryAuthKey == "" {
			*factoryAuthKey = envFileValue(videoEnv, "FACTORY_ENROLL_AUTH_KEY")
		}
		if *factoryURL == "" {
			return errors.New("factory enrollment URL missing; set FACTORY_ENROLL_URL in video-cloud env or pass --factory-url")
		}
		if *factoryAuthKey == "" {
			return errors.New("factory enrollment auth key missing; set FACTORY_ENROLL_AUTH_KEY in video-cloud env or pass --factory-auth-key")
		}
		*factoryURL = strings.TrimRight(*factoryURL, "/")
	}
	if exists(*outDir) {
		if !*force {
			return fmt.Errorf("%s already exists; use --force to replace it", *outDir)
		}
		if err := os.RemoveAll(*outDir); err != nil {
			return err
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
				GenerateOnly:   *generateOnly,
				CAKey:          caKey,
				CACert:         caCert,
				DeviceDays:     *deviceValidDays,
				FactoryURL:     *factoryURL,
				FactoryAuthKey: *factoryAuthKey,
				FactoryID:      *factoryID,
				LineID:         *lineID,
				StationID:      *stationID,
				FixtureID:      *fixtureID,
				OperatorID:     *operatorID,
				BatchID:        *batchID,
				SerialPrefix:   *serialPrefix,
				RunID:          *runID,
				Timeout:        time.Duration(*timeoutSeconds) * time.Second,
				ResultsPath:    enrollResultsPath,
			}})
			index++
		}
	}
	logLoad("device generation concurrency=%d", *concurrency)
	deviceResults, err := boundedParallelMap(len(tasks), *concurrency, func(i int) (deviceResult, error) {
		device, ok, err := writeLoadDevice(tasks[i].input)
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
	if err := writeLoadDeviceReadme(*outDir, *count, *mix, mode, *factoryURL, *caValidDays, *deviceValidDays); err != nil {
		return err
	}
	if enrollFailed > 0 {
		logLoad("complete with failures: requested=%d succeeded=%d failed=%d results=%s", *count, enrollSucceeded, enrollFailed, enrollResultsPath)
		fmt.Fprintf(os.Stdout, "output=%s\nsummary=%s\nenroll_results=%s\nopenssl_log=%s\n", *outDir, filepath.Join(*outDir, "summary.json"), enrollResultsPath, opensslLog)
		return exitCode(1)
	}
	logLoad("complete: requested=%d succeeded=%d failed=%d", *count, enrollSucceeded, enrollFailed)
	fmt.Fprintf(os.Stdout, "output=%s\nsummary=%s\nenroll_results=%s\nloadtest_env=%s\nopenssl_log=%s\n", *outDir, filepath.Join(*outDir, "summary.json"), enrollResultsPath, filepath.Join(*outDir, "loadtest.env"), opensslLog)
	return nil
}

type accountManagerContext struct {
	EnvRoot          string
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
	created, status, err := accountCreateBrandCloud(ctx, token, *brandname)
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
	count := fs.Int("count", 10, "count")
	role := fs.String("role", "member", "role")
	rotatePassword := fs.Bool("rotate-password", false, "rotate password")
	concurrency := fs.Int("concurrency", envInt("CLOUD_CREATE_USERS_CONCURRENCY", 16), "user creation concurrency")
	dryRun := fs.Bool("dry-run", false, "dry run")
	skipBootstrap := fs.Bool("skip-bootstrap", false, "skip bootstrap")
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
	if *count <= 0 {
		return errors.New("--count must be greater than zero")
	}
	if *role != "owner" && *role != "admin" && *role != "member" {
		return errors.New("--role must be owner, admin, or member")
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
	planned := plannedUsers(*brandname, slug, *role, *count)
	if *dryRun {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"action": "dry_run", "brand_cloud": brandCloud, "role": *role, "users": planned})
	}
	existingAppCredentials := loadExistingUserAppCredentials(ctx.EnvRoot, slug)
	type createUserResult struct {
		user     map[string]any
		created  bool
		assigned bool
	}
	var sessionMu sync.Mutex
	var logMu sync.Mutex
	safeLog := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logCreateUsers(format, args...)
	}
	safeAccountCreateUser := func(email, displayName, password string) (accountCreateUserResult, error) {
		sessionMu.Lock()
		defer sessionMu.Unlock()
		return accountCreateUser(ctx, &session, safeLog, brandCloudID, email, displayName, password, *role, *rotatePassword)
	}
	safeRevokeAppCertificate := func(brandCloudUserID string) error {
		sessionMu.Lock()
		defer sessionMu.Unlock()
		return accountRevokeBrandCloudUserAppCertificate(ctx, &session, safeLog, brandCloudID, brandCloudUserID)
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
		safeLog("ensuring brand user: email=%s role=%s", email, *role)
		createResult, err := safeAccountCreateUser(email, displayName, password)
		if err != nil {
			return createUserResult{}, err
		}
		result := createUserResult{}
		if createResult.Action == "created" {
			result.created = true
		} else {
			if !*rotatePassword {
				return createUserResult{}, fmt.Errorf("brand user already exists and password was not rotated: email=%s; use the previous credentials artifact or rerun with --rotate-password", email)
			}
			result.assigned = true
		}
		if createResult.BrandCloudUserID == "" {
			return createUserResult{}, fmt.Errorf("brand user create response missing brand_cloud_user.id for %s", email)
		}
		appSubject := "app-brand-cloud-user:" + createResult.BrandCloudUserID
		safeLog("bootstrapping app certificate: email=%s", email)
		appCredentials, appCertificate, userSession, err := accountEnsureUserAppCertificate(ctx, tenantSlug, email, password, appSubject, existingAppCredentials[email], func() error {
			return safeRevokeAppCertificate(createResult.BrandCloudUserID)
		})
		if err != nil {
			return createUserResult{}, err
		}
		result.user = map[string]any{
			"email":           email,
			"display_name":    displayName,
			"role":            *role,
			"password":        password,
			"action":          createResult.Action,
			"app_credentials": appCredentials,
			"app_certificate": appCertificate,
			"tokens":          userSession,
		}
		return result, nil
	})
	if err != nil {
		return err
	}
	users := []map[string]any{}
	created := 0
	assigned := 0
	for _, result := range results {
		if result.created {
			created++
		}
		if result.assigned {
			assigned++
		}
		users = append(users, result.user)
	}
	artifactDir := filepath.Join(ctx.EnvRoot, "artifacts", "users")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return err
	}
	credentialsFile := uniqueUserCredentialsFile(artifactDir, slug)
	if err := writeJSON(credentialsFile, map[string]any{"brandname": *brandname, "brand_cloud_id": brandCloudID, "tenant_slug": tenantSlug, "role": *role, "users": users}); err != nil {
		return err
	}
	_ = os.Chmod(credentialsFile, 0o600)
	logCreateUsers("credentials written: %s", credentialsFile)
	return json.NewEncoder(os.Stdout).Encode(map[string]any{
		"action":           "created",
		"brandname":        *brandname,
		"brand_cloud_id":   brandCloudID,
		"tenant_slug":      tenantSlug,
		"role":             *role,
		"count":            *count,
		"created":          created,
		"assigned":         assigned,
		"app_certificates": *count,
		"credentials_file": credentialsFile,
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
	UsersFile         string    `json:"users_file"`
	DeviceBindFile    string    `json:"device_bind_file"`
	BindValidationDir string    `json:"bind_validation_dir"`
	Steps             []e2eStep `json:"steps"`
}

func runStagingE2EDataSetup(args []string) error {
	fs := flag.NewFlagSet("staging-e2e-data-setup", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	planMode := fs.Bool("plan", false, "plan")
	brandname := fs.String("brandname", "RTK", "brand name")
	userCount := fs.Int("user-count", 10, "user count")
	deviceCount := fs.Int("device-count", 100, "device count")
	deviceMix := fs.String("device-mix", "camera=40,light=25,air_conditioner=20,smart_meter=15", "device mix")
	devicePrefix := fs.String("device-prefix", "load-device", "device prefix")
	userConcurrency := fs.Int("user-concurrency", envInt("CLOUD_STAGING_E2E_USER_CONCURRENCY", 16), "user creation concurrency")
	deviceConcurrency := fs.Int("device-concurrency", envInt("CLOUD_STAGING_E2E_DEVICE_CONCURRENCY", 16), "device generation concurrency")
	bindConcurrency := fs.Int("bind-concurrency", envInt("CLOUD_STAGING_E2E_BIND_CONCURRENCY", 16), "device bind concurrency")
	outDir := fs.String("out-dir", "", "out dir")
	quiet := fs.Bool("quiet", false, "suppress periodic progress output")
	resume := fs.Bool("resume", false, "reuse completed artifacts for matching steps")
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
	if *deviceCount <= 0 {
		return errors.New("--device-count must be a positive integer")
	}
	if *userConcurrency <= 0 || *deviceConcurrency <= 0 || *bindConcurrency <= 0 {
		return errors.New("--user-concurrency, --device-concurrency, and --bind-concurrency must be positive integers")
	}
	if *fromStep != "" && e2eStepIndex(*fromStep) < 0 {
		return fmt.Errorf("--from-step must be one of: %s", strings.Join(e2eStepOrder(), ", "))
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
		"generate-devices": firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_GENERATE_DEVICES_SCRIPT"), selfCommandPath("generate-load-devices")),
		"bind-devices":     firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_BIND_DEVICES_SCRIPT"), selfCommandPath("bind-devices")),
		"validate-bind":    firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_VALIDATE_BIND_SCRIPT"), selfCommandPath("validate-device-bind")),
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
	runStep := func(name string, argv ...string) error {
		step, err := runE2EStepWithOptions(name, filepath.Join(logsDir, name+".log"), e2eStepOptions{Quiet: *quiet}, argv...)
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
	usersFile := *usersFileFlag
	if usersFile == "" {
		usersFile = latestMatchingFile(filepath.Join(envRoot, "artifacts", "users"), slug+"-users-*.json")
	}
	if shouldRunStep("create_users") && !(*resume && usersArtifactCount(usersFile) == *userCount) {
		args := []string{"--workspace", workspace, "--env-root", envRoot, "--brandname", *brandname, "--count", strconv.Itoa(*userCount), "--rotate-password", "--concurrency", strconv.Itoa(*userConcurrency)}
		if boolishEnv("CLOUD_STAGING_E2E_SKIP_BOOTSTRAP") {
			args = append(args, "--skip-bootstrap")
		}
		if err := runStep("create_users", commandWithArgs(scripts["create-users"], args...)...); err != nil {
			return err
		}
		usersFile = latestMatchingFile(filepath.Join(envRoot, "artifacts", "users"), slug+"-users-*.json")
	} else {
		reason := "--from-step"
		if shouldRunStep("create_users") {
			reason = fmt.Sprintf("--resume users artifact count=%d", usersArtifactCount(usersFile))
		}
		skipStep("create_users", reason)
	}
	devicesDir := filepath.Join(envRoot, "devices", "test_device")
	if shouldRunStep("create_devices") && !(*resume && deviceManifestCount(devicesDir) == *deviceCount) {
		if err := runStep("create_devices", commandWithArgs(scripts["generate-devices"], "--workspace", workspace, "--env-root", envRoot, "--count", strconv.Itoa(*deviceCount), "--mix", *deviceMix, "--prefix", *devicePrefix, "--force", "--concurrency", strconv.Itoa(*deviceConcurrency))...); err != nil {
			return err
		}
	} else {
		reason := "--from-step"
		if shouldRunStep("create_devices") {
			reason = fmt.Sprintf("--resume device manifest count=%d", deviceManifestCount(devicesDir))
		}
		skipStep("create_devices", reason)
	}
	if usersFile == "" {
		return fmt.Errorf("no users artifact found for brand slug %s", slug)
	}
	bindFile := *bindArtifactFlag
	if bindFile == "" {
		bindFile = latestMatchingFile(filepath.Join(envRoot, "artifacts", "device-bind"), slug+"-device-bind-*.json")
	}
	if shouldRunStep("bind_devices") && !(*resume && bindArtifactCount(bindFile) == *deviceCount) {
		args := []string{"--workspace", workspace, "--env-root", envRoot, "--brandname", *brandname, "--users-file", usersFile, "--devices-dir", devicesDir, "--count", strconv.Itoa(*deviceCount), "--concurrency", strconv.Itoa(*bindConcurrency)}
		if boolishEnv("CLOUD_STAGING_E2E_SKIP_BOOTSTRAP") {
			args = append(args, "--skip-bootstrap")
		}
		if err := runStep("bind_devices", commandWithArgs(scripts["bind-devices"], args...)...); err != nil {
			return err
		}
		bindFile = latestMatchingFile(filepath.Join(envRoot, "artifacts", "device-bind"), slug+"-device-bind-*.json")
	} else {
		reason := "--from-step"
		if shouldRunStep("bind_devices") {
			reason = fmt.Sprintf("--resume bind artifact count=%d", bindArtifactCount(bindFile))
		}
		skipStep("bind_devices", reason)
	}
	if bindFile == "" {
		return fmt.Errorf("no device-bind artifact found for brand slug %s", slug)
	}
	expectedPerUser := (*deviceCount + *userCount - 1) / *userCount
	bindValidationDir := filepath.Join(*outDir, "bind-validation")
	if shouldRunStep("validate_bind") {
		if err := runStep("validate_bind", commandWithArgs(scripts["validate-bind"], "--workspace", workspace, "--env-root", envRoot, "--bind-artifact", bindFile, "--out-dir", bindValidationDir, "--expected-count", strconv.Itoa(*deviceCount), "--expected-devices-per-user", strconv.Itoa(expectedPerUser), "--wait-provisioned-timeout", firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_BIND_PROVISION_TIMEOUT"), "10m"), "--wait-provisioned-poll", firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_BIND_PROVISION_POLL"), "10s"), "--wait-provisioned-concurrency", strconv.Itoa(*bindConcurrency))...); err != nil {
			return err
		}
	} else {
		skipStep("validate_bind", "--from-step")
	}
	overall := "pass"
	for _, step := range steps {
		if step.Status != "PASS" && step.Status != "SKIP" {
			overall = "fail"
		}
	}
	summaryFile := filepath.Join(*outDir, "summary.json")
	summary := e2eDataSetupSummary{
		Overall:           overall,
		SummaryFile:       summaryFile,
		UsersFile:         usersFile,
		DeviceBindFile:    bindFile,
		BindValidationDir: bindValidationDir,
		Steps:             steps,
	}
	if err := writeJSON(summaryFile, map[string]any{
		"overall":             summary.Overall,
		"generated_at":        time.Now().UTC().Format(time.RFC3339),
		"env_root":            envRoot,
		"brandname":           *brandname,
		"summary_file":        summary.SummaryFile,
		"users_file":          summary.UsersFile,
		"device_bind_file":    summary.DeviceBindFile,
		"bind_validation_dir": summary.BindValidationDir,
		"artifacts":           map[string]any{"users_file": summary.UsersFile, "device_bind_file": summary.DeviceBindFile, "bind_validation_dir": summary.BindValidationDir},
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
	fmt.Fprintf(os.Stdout, "  - create brand cloud with %s\n", scripts["create-brand"])
	fmt.Fprintf(os.Stdout, "  - create users with %s\n", scripts["create-users"])
	fmt.Fprintf(os.Stdout, "  - generate/factory-enroll devices with %s\n", scripts["generate-devices"])
	fmt.Fprintf(os.Stdout, "  - bind/provision devices with %s\n", scripts["bind-devices"])
	fmt.Fprintf(os.Stdout, "  - validate bind artifact with %s\n", scripts["validate-bind"])
}

func e2eStepOrder() []string {
	return []string{"create_brand", "create_users", "create_devices", "bind_devices", "validate_bind"}
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
	yes := fs.Bool("yes", false, "confirm")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if !*yes {
		return errors.New("--yes is required")
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
	if os.Getenv("CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET") != "1" {
		fmt.Fprintf(os.Stderr, "[cloud-remove-k8s] non-destructive reset for %s; set CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET=1 to delete namespaces\n", stack)
		return nil
	}
	kubeconfig, err := ensureK8SKubeconfig(workspace, envRoot, stack)
	if err != nil {
		return err
	}
	for _, ns := range k8sStagingNamespaces(stack) {
		if err := runK8SKubectl(kubeconfig, "delete", "namespace", ns, "--ignore-not-found=true"); err != nil {
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
	}
}

func ensureK8SKubeconfig(workspace, envRoot, stack string) (string, error) {
	if path := firstNonEmpty(os.Getenv("CLOUD_STAGING_K8S_KUBECONFIG"), os.Getenv("KUBECONFIG")); path != "" {
		return path, nil
	}
	out := filepath.Join(workspace, ".artifacts", "kube", stack+"-lke.kubeconfig")
	if info, err := os.Stat(out); err == nil && !info.IsDir() {
		return out, nil
	}
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
		if err := runK8SKubectl(kubeconfig, "-n", namespace, "rollout", "status", name, timeoutArg); err != nil {
			return err
		}
	}
	return nil
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
	mqttPort := firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_MQTT_PORT"), "18883")
	forwards := []struct {
		ns      string
		service string
		port    string
		local   string
	}{
		{stack + "-account-manager", "account-manager", "http", accountPort},
		{stack + "-video-cloud", "video-cloud-api", "http", videoPort},
		{stack + "-video-cloud", "factoryenroll", "http", factoryPort},
		{stack + "-video-cloud", "mqtt", "mqtts", mqttPort},
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
	for _, fwd := range forwards {
		servicePort, err := k8sServicePort(kubeconfig, fwd.ns, fwd.service, fwd.port)
		if err != nil {
			cleanup()
			return nil, nil, err
		}
		cmd := exec.Command("kubectl", "-n", fwd.ns, "port-forward", "svc/"+fwd.service, fwd.local+":"+strconv.Itoa(servicePort))
		cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
		cmd.Stdout = os.Stderr
		cmd.Stderr = os.Stderr
		if err := cmd.Start(); err != nil {
			cleanup()
			return nil, nil, err
		}
		cmds = append(cmds, cmd)
		if err := waitTCP("127.0.0.1:"+fwd.local, 15*time.Second); err != nil {
			cleanup()
			return nil, nil, err
		}
	}
	env := []string{
		"ACCOUNT_MANAGER_BASE_URL=http://127.0.0.1:" + accountPort,
		"VIDEO_CLOUD_BASE_URL=http://127.0.0.1:" + videoPort,
		"FACTORY_ENROLL_URL=http://127.0.0.1:" + factoryPort,
		"VIDEO_CLOUD_MQTT_ADDR=127.0.0.1:" + mqttPort,
		"VIDEO_CLOUD_LOAD_MQTT_SET=broker",
		"CLOUD_STAGING_E2E_SKIP_BOOTSTRAP=1",
		"CLOUD_STAGING_E2E_ENDPOINT_SOURCE=k8s-service",
	}
	if secretEnv, err := readK8SSecretEnv(kubeconfig, stack+"-account-manager", "account-manager-runtime", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD", "ACCOUNT_MANAGER_INTERNAL_AUTH_TOKEN"); err == nil {
		env = append(env, secretEnv...)
	} else {
		cleanup()
		return nil, nil, err
	}
	if secretEnv, err := readK8SSecretEnv(kubeconfig, stack+"-video-cloud", "video-cloud-runtime", "VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN"); err == nil {
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

func readK8SSecretEnv(kubeconfig, namespace, secret string, keys ...string) ([]string, error) {
	cmd := exec.Command("kubectl", "-n", namespace, "get", "secret", secret, "-o", "json")
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
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
	deviceCount := fs.Int("device-count", 100, "device count")
	deviceMix := fs.String("device-mix", "camera=40,light=25,air_conditioner=20,smart_meter=15", "device mix")
	devicePrefix := fs.String("device-prefix", "load-device", "device prefix")
	userConcurrency := fs.Int("user-concurrency", envInt("CLOUD_STAGING_E2E_USER_CONCURRENCY", 16), "user creation concurrency")
	deviceConcurrency := fs.Int("device-concurrency", envInt("CLOUD_STAGING_E2E_DEVICE_CONCURRENCY", 16), "device generation concurrency")
	bindConcurrency := fs.Int("bind-concurrency", envInt("CLOUD_STAGING_E2E_BIND_CONCURRENCY", 16), "device bind concurrency")
	outDir := fs.String("out-dir", "", "out dir")
	skipMQTTProbe := fs.Bool("skip-mqtt-probe", false, "skip mqtt probe")
	quiet := fs.Bool("quiet", false, "suppress periodic progress output")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required")
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
	scripts := map[string]string{
		"remove-k8s":      firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_REMOVE_K8S_SCRIPT"), selfCommandPath("remove-k8s")),
		"provision-k8s":   firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_PROVISION_K8S_SCRIPT"), selfCommandPath("provision-k8s")),
		"setup-data":      firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_DATA_SETUP_SCRIPT"), filepath.Join(workspace, "scripts", "setup-staging-e2e-data.sh")),
		"mqtt-test":       firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_MQTT_TEST_SCRIPT"), selfCommandPath("mqtt-test")),
		"mqtt-log-verify": firstNonEmpty(os.Getenv("CLOUD_STAGING_E2E_MQTT_LOG_VERIFY_SCRIPT"), selfCommandPath("staging-e2e-mqtt-log-verify")),
	}
	if !*runMode {
		printE2EPlan(workspace, envRoot, stackName, *brandname, *userCount, *deviceCount, *deviceMix, *userConcurrency, *deviceConcurrency, *bindConcurrency, *skipRemove, scripts)
		return nil
	}
	if *confirm != stackName {
		return fmt.Errorf("--confirm %s does not match CLOUD_STACK_NAME=%s", *confirm, stackName)
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
	if !*skipRemove {
		if err := runStep("reset_k8s", append(commandWithArgs(scripts["remove-k8s"], "--workspace", workspace, "--env-root", envRoot), "--yes")...); err != nil {
			return err
		}
	}
	k8sProvisionArgs := []string{"--workspace", workspace, "--env-root", envRoot, "--confirm", stackName}
	if err := runStep("provision_k8s", commandWithArgs(scripts["provision-k8s"], k8sProvisionArgs...)...); err != nil {
		return err
	}
	portForwardEnv, cleanup, err := startK8SE2EPortForwards(workspace, envRoot)
	if err != nil {
		return err
	}
	defer cleanup()
	childEnv = append(childEnv, portForwardEnv...)
	dataSetupDir := filepath.Join(*outDir, "data-setup")
	dataSetupArgs := []string{"--workspace", workspace, "--env-root", envRoot, "--brandname", *brandname, "--user-count", strconv.Itoa(*userCount), "--device-count", strconv.Itoa(*deviceCount), "--device-mix", *deviceMix, "--device-prefix", *devicePrefix, "--user-concurrency", strconv.Itoa(*userConcurrency), "--device-concurrency", strconv.Itoa(*deviceConcurrency), "--bind-concurrency", strconv.Itoa(*bindConcurrency), "--out-dir", dataSetupDir}
	if *quiet {
		dataSetupArgs = append(dataSetupArgs, "--quiet")
	}
	dataSetupStep, err := runE2EStepWithOptions("setup_brand_devices", filepath.Join(logsDir, "setup_brand_devices.log"), e2eStepOptions{Quiet: *quiet, Env: childEnv}, commandWithArgs(scripts["setup-data"], dataSetupArgs...)...)
	if err != nil {
		steps = append(steps, dataSetupStep)
		return err
	}
	dataSummary, err := readE2EDataSetupSummary(filepath.Join(dataSetupDir, "summary.json"))
	if err != nil {
		steps = append(steps, dataSetupStep)
		return err
	}
	steps = append(steps, dataSetupStep)
	usersFile := dataSummary.UsersFile
	bindFile := dataSummary.DeviceBindFile
	dataSetupSummaryFile := dataSummary.SummaryFile
	bindValidationDir := dataSummary.BindValidationDir
	if usersFile == "" {
		return errors.New("data setup summary did not include users_file")
	}
	if bindFile == "" {
		return errors.New("data setup summary did not include device_bind_file")
	}
	if dataSetupSummaryFile == "" {
		dataSetupSummaryFile = filepath.Join(dataSetupDir, "summary.json")
	}
	if bindValidationDir == "" {
		return errors.New("data setup summary did not include bind_validation_dir")
	}
	mqttArgs := []string{"--env-root", envRoot, "--brandname", *brandname, "--profile", "smoke", "--out-dir", filepath.Join(*outDir, "home-mqtt")}
	if *skipMQTTProbe {
		mqttArgs = append(mqttArgs, "--no-mqtt-probe")
	} else {
		mqttArgs = append(mqttArgs, "--mqtt-probe")
	}
	step, err := runE2EStepWithOptions("cloud_mqtt_test", filepath.Join(logsDir, "cloud_mqtt_test.log"), e2eStepOptions{Quiet: *quiet, Env: childEnv}, commandWithArgs(scripts["mqtt-test"], mqttArgs...)...)
	steps = append(steps, step)
	if err != nil {
		return err
	}
	mqttLogVerifyDir := filepath.Join(*outDir, "mqtt-log-verify")
	mqttLogVerifySummaryFile := filepath.Join(mqttLogVerifyDir, "summary.json")
	mqttLogVerifyArgs := []string{"--workspace", workspace, "--env-root", envRoot, "--mqtt-results", filepath.Join(*outDir, "home-mqtt", "results.json"), "--out-dir", mqttLogVerifyDir}
	step, err = runE2EStepWithOptions("verify_mqtt_logs", filepath.Join(logsDir, "verify_mqtt_logs.log"), e2eStepOptions{Quiet: *quiet, Env: childEnv}, commandWithArgs(scripts["mqtt-log-verify"], mqttLogVerifyArgs...)...)
	steps = append(steps, step)
	if err != nil {
		return err
	}
	overall := "pass"
	for _, step := range steps {
		if step.Status != "PASS" {
			overall = "fail"
		}
	}
	summaryFile := filepath.Join(*outDir, "summary.json")
	reportFile := filepath.Join(*outDir, "TEST_REPORT.md")
	summary := map[string]any{
		"overall":      overall,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"env_root":     envRoot,
		"stack":        stackName,
		"target":       "k8s",
		"brandname":    *brandname,
		"artifacts":    map[string]any{"users_file": usersFile, "device_bind_file": bindFile, "bind_validation_dir": bindValidationDir, "data_setup_summary_file": dataSetupSummaryFile, "mqtt_log_verify_summary_file": mqttLogVerifySummaryFile, "report_file": reportFile},
		"steps":        steps,
	}
	if err := writeJSON(summaryFile, summary); err != nil {
		return err
	}
	if err := os.WriteFile(reportFile, []byte(renderE2EReport(overall, envRoot, stackName, *brandname, usersFile, bindFile, bindValidationDir, dataSetupSummaryFile, filepath.Join(*outDir, "home-mqtt"), mqttLogVerifySummaryFile, steps)), 0o644); err != nil {
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
	values := make([]string, 0, len(expectations))
	for _, item := range expectations {
		values = append(values, fmt.Sprintf("(%s,%s,%d,%s,%s)", sqlLiteral(item.DeviceID), sqlLiteral(item.StreamID), item.Seq, sqlLiteral(item.Source), sqlLiteral(item.Message)))
	}
	sql := `
WITH expected(device_id, stream_id, seq, source, message) AS (
	VALUES ` + strings.Join(values, ",") + `
)
SELECT e.device_id, e.stream_id, e.seq, e.source, e.message
FROM expected e
LEFT JOIN device_runtime_logs l
  ON l.device_id = e.device_id
 AND l.stream_id = e.stream_id
 AND l.seq = e.seq
 AND l.source = e.source
 AND l.message = e.message
WHERE l.id IS NULL
ORDER BY e.device_id, e.stream_id, e.seq`
	cmd := exec.Command("kubectl", "-n", stack+"-platform", "exec", "postgresql-0", "--", "psql", "-U", "postgres", "-d", "video_cloud", "-At", "-F", "\t", "-c", sql)
	cmd.Env = append(os.Environ(), "KUBECONFIG="+kubeconfig)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("query MQTT runtime logs: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, err
	}
	out = bytes.TrimSpace(out)
	if len(out) == 0 {
		return nil, nil
	}
	missing := []mqttLogExpectation{}
	for _, line := range strings.Split(string(out), "\n") {
		parts := strings.Split(line, "\t")
		if len(parts) != 5 {
			return nil, fmt.Errorf("unexpected MQTT log verification row: %q", line)
		}
		seq, err := strconv.Atoi(parts[2])
		if err != nil {
			return nil, err
		}
		missing = append(missing, mqttLogExpectation{DeviceID: parts[0], StreamID: parts[1], Seq: seq, Source: parts[3], Message: parts[4]})
	}
	return missing, nil
}

func sqlLiteral(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "''") + "'"
}

func printE2EPlan(workspace, envRoot, stack, brandname string, userCount, deviceCount int, deviceMix string, userConcurrency, deviceConcurrency, bindConcurrency int, skipRemove bool, scripts map[string]string) {
	fmt.Fprintln(os.Stdout, "cloud-staging-e2e-test plan")
	fmt.Fprintf(os.Stdout, "workspace: %s\n", workspace)
	fmt.Fprintf(os.Stdout, "env_root: %s\n", envRoot)
	fmt.Fprintf(os.Stdout, "stack: %s\n", stack)
	fmt.Fprintln(os.Stdout, "target: k8s")
	fmt.Fprintf(os.Stdout, "brandname: %s\n", brandname)
	fmt.Fprintf(os.Stdout, "user_count: %d\n", userCount)
	fmt.Fprintf(os.Stdout, "device_count: %d\n", deviceCount)
	fmt.Fprintf(os.Stdout, "device_mix: %s\n", deviceMix)
	fmt.Fprintf(os.Stdout, "user_concurrency: %d\n", userConcurrency)
	fmt.Fprintf(os.Stdout, "device_concurrency: %d\n", deviceConcurrency)
	fmt.Fprintf(os.Stdout, "bind_concurrency: %d\n", bindConcurrency)
	fmt.Fprintf(os.Stdout, "skip_remove: %v\n", skipRemove)
	fmt.Fprintln(os.Stdout, "steps:")
	if !skipRemove {
		fmt.Fprintf(os.Stdout, "  - reset K8s staging with %s\n", scripts["remove-k8s"])
	}
	fmt.Fprintf(os.Stdout, "  - provision K8s staging with %s\n", scripts["provision-k8s"])
	fmt.Fprintf(os.Stdout, "  - setup brand/users/devices with %s\n", scripts["setup-data"])
	fmt.Fprintf(os.Stdout, "  - run live home MQTT E2E with %s\n", scripts["mqtt-test"])
	fmt.Fprintf(os.Stdout, "  - verify persisted MQTT runtime logs with %s\n", scripts["mqtt-log-verify"])
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
	lastPrinted := ""
	for {
		select {
		case err := <-done:
			return err
		case <-ticker.C:
			line := latestLogLine(logPath)
			if line == "" || line == lastPrinted {
				continue
			}
			lastPrinted = line
			elapsed := time.Since(start)
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
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		return ""
	}
	const maxTail = int64(64 * 1024)
	offset := int64(0)
	if st.Size() > maxTail {
		offset = st.Size() - maxTail
	}
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return ""
	}
	buf, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	buf = bytes.TrimSpace(buf)
	if len(buf) == 0 {
		return ""
	}
	if idx := bytes.LastIndexByte(buf, '\n'); idx >= 0 {
		buf = bytes.TrimSpace(buf[idx+1:])
	}
	line := strings.TrimSpace(string(buf))
	if line == "" {
		return ""
	}
	return redactProgressLogLine(line)
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

func latestMatchingFile(dir, pattern string) string {
	matches, _ := filepath.Glob(filepath.Join(dir, pattern))
	sort.Strings(matches)
	if len(matches) == 0 {
		return ""
	}
	return matches[len(matches)-1]
}

func renderE2EReport(overall, envRoot, stack, brandname, usersFile, bindFile, bindValidationDir, dataSetupSummaryFile, mqttDir, mqttLogVerifySummaryFile string, steps []e2eStep) string {
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
	fmt.Fprintf(&b, "- Users artifact: `%s`\n", usersFile)
	fmt.Fprintf(&b, "- Device bind artifact: `%s`\n", bindFile)
	fmt.Fprintf(&b, "- Bind validation: `%s`\n", bindValidationDir)
	fmt.Fprintf(&b, "- Data setup summary: `%s`\n", dataSetupSummaryFile)
	fmt.Fprintf(&b, "- Home MQTT report: `%s`\n", filepath.Join(mqttDir, "TEST_REPORT.md"))
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
	Action           string
	BrandCloudUserID string
}

func accountCreateUser(ctx accountManagerContext, session *accountPlatformSession, logf func(string, ...any), brandCloudID, email, displayName, password, role string, rotate bool) (accountCreateUserResult, error) {
	payload, _ := json.Marshal(map[string]any{"email": email, "password": password, "display_name": displayName, "role": role, "rotate_password": rotate})
	body, status, err := curlJSONStatusWithPlatformRetry(ctx, session, logf, "brand user create", func(platformToken string) ([]byte, int, error) {
		return curlJSONStatus(fmt.Sprintf("%s/v1/admin/brand-clouds/%s/users", ctx.BaseURL, brandCloudID), platformToken, payload)
	})
	if err != nil {
		return accountCreateUserResult{}, err
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
	brandCloudUser, _ := parsed["brand_cloud_user"].(map[string]any)
	return accountCreateUserResult{Action: action, BrandCloudUserID: stringValue(brandCloudUser["id"])}, nil
}

func accountRevokeBrandCloudUserAppCertificate(ctx accountManagerContext, session *accountPlatformSession, logf func(string, ...any), brandCloudID, brandCloudUserID string) error {
	body, status, err := curlJSONStatusWithPlatformRetry(ctx, session, logf, "brand user app certificate revoke", func(platformToken string) ([]byte, int, error) {
		endpoint := fmt.Sprintf("%s/v1/admin/brand-clouds/%s/users/%s/app-certificate/revoke", ctx.BaseURL, url.PathEscape(brandCloudID), url.PathEscape(brandCloudUserID))
		return curlJSONStatus(endpoint, platformToken, []byte("{}"))
	})
	if err != nil {
		return err
	}
	if status != http.StatusOK {
		return fmt.Errorf("brand user app certificate revoke failed: brand_cloud_user=%s HTTP %d%s", brandCloudUserID, status, errorBodySuffix(body))
	}
	return nil
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

func accountEnsureUserAppCertificate(ctx accountManagerContext, tenantSlug, email, password, subject string, existingAppCredentials map[string]any, recoverMissingLocalCredentials func() error) (map[string]any, map[string]any, accountPlatformSession, error) {
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
	if strings.TrimSpace(subject) == "" {
		return nil, nil, accountPlatformSession{}, fmt.Errorf("app certificate subject is required for %s", email)
	}
	privateKeyPEM, csrPEM, err := generateAppCertificateCSR(subject)
	if err != nil {
		return nil, nil, accountPlatformSession{}, err
	}
	issued, err := accountLoginUserFull(ctx, tenantSlug, email, password, csrPEM)
	if err != nil && strings.Contains(err.Error(), "app_certificate_csr_invalid") && strings.HasPrefix(subject, "app-brand-cloud-user:") && initial.User.ID != "" {
		legacySubject := "app-user:" + initial.User.ID
		logCreateUsers("retrying app certificate with legacy subject: email=%s", email)
		subject = legacySubject
		privateKeyPEM, csrPEM, err = generateAppCertificateCSR(subject)
		if err != nil {
			return nil, nil, accountPlatformSession{}, err
		}
		issued, err = accountLoginUserFull(ctx, tenantSlug, email, password, csrPEM)
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
		"private_key_pem": privateKeyPEM,
		"csr_pem":         csrPEM,
	}, accountAppCertificateMap(issued.AppCertificate), accountPlatformSession{AccessToken: issued.Tokens.AccessToken, RefreshToken: issued.Tokens.RefreshToken}, nil
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

func accountLoginUserFull(ctx accountManagerContext, tenantSlug, email, password, csrPEM string) (accountUserLoginResponse, error) {
	payload := map[string]string{"email": email, "password": password}
	if strings.TrimSpace(csrPEM) != "" {
		payload["app_csr_pem"] = csrPEM
	}
	raw, _ := json.Marshal(payload)
	loginURL := fmt.Sprintf("%s/v1/brand-clouds/%s/auth/login", ctx.BaseURL, url.PathEscape(tenantSlug))
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

func generateAppCertificateCSR(subject string) (string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: subject},
	}, key)
	if err != nil {
		return "", "", err
	}
	privateKeyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	csrPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))
	return privateKeyPEM, csrPEM, nil
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
	users := make([]map[string]any, 0, count)
	for i := 1; i <= count; i++ {
		suffix := fmt.Sprintf("%03d", i)
		users = append(users, map[string]any{
			"email":        fmt.Sprintf("%s+%s@users.local", slug, suffix),
			"display_name": fmt.Sprintf("%s User %s", brandname, suffix),
			"role":         role,
		})
	}
	return users
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
	ctx := accountManagerContext{
		EnvRoot:          envRoot,
		BaseURL:          baseURL,
		AdminEmail:       firstNonEmpty(os.Getenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"), envFileValue(platformEnv, "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL")),
		AdminPassword:    firstNonEmpty(os.Getenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"), envFileValue(platformEnv, "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD")),
		PlatformAdminEnv: platformEnv,
	}
	if firstNonEmpty(os.Getenv("CLOUD_PROVIDER"), stackEnv["CLOUD_PROVIDER"]) == "lke" && os.Getenv("ACCOUNT_MANAGER_BASE_URL") == "" {
		forwardURL, cleanup, err := lkeAccountManagerPortForward(envRoot, map[string]string{
			"CLOUD_STACK_NAME": firstNonEmpty(stackEnv["CLOUD_STACK_NAME"], "video-cloud-staging"),
		})
		if err != nil {
			return accountManagerContext{}, err
		}
		ctx.BaseURL = forwardURL
		ctx.cleanup = cleanup
	}
	return ctx, nil
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

func accountCreateBrandCloud(ctx accountManagerContext, token, brandname string) (map[string]any, int, error) {
	payload, _ := json.Marshal(map[string]any{"name": brandname, "metadata": map[string]string{"brandname": brandname}})
	body, status, err := curlJSONStatus(ctx.BaseURL+"/v1/admin/brand-clouds", token, payload)
	if err != nil {
		return nil, status, err
	}
	var parsed map[string]any
	_ = json.Unmarshal(body, &parsed)
	return parsed, status, nil
}

func curlJSONStatus(url, bearer string, payload []byte) ([]byte, int, error) {
	tmp, err := os.CreateTemp("", "rtk-curl-json-*")
	if err != nil {
		return nil, 0, err
	}
	tmp.Close()
	defer os.Remove(tmp.Name())
	connectTimeout := firstNonEmpty(os.Getenv("RTK_CLOUD_CURL_CONNECT_TIMEOUT"), "10")
	maxTime := firstNonEmpty(os.Getenv("RTK_CLOUD_CURL_MAX_TIME"), "60")
	args := []string{"-sS", "--connect-timeout", connectTimeout, "--max-time", maxTime, "-o", tmp.Name(), "-w", "%{http_code}"}
	var payloadPath string
	if payload != nil {
		payloadFile, err := os.CreateTemp("", "rtk-curl-payload-*")
		if err != nil {
			return nil, 0, err
		}
		payloadPath = payloadFile.Name()
		if _, err := payloadFile.Write(payload); err != nil {
			payloadFile.Close()
			os.Remove(payloadPath)
			return nil, 0, err
		}
		payloadFile.Close()
		defer os.Remove(payloadPath)
		args = append(args, "-H", "content-type: application/json")
		if bearer != "" {
			args = append(args, "-H", "authorization: Bearer "+bearer)
		}
		args = append(args, "--data-binary", "@"+payloadPath)
	} else if bearer != "" {
		args = append(args, "-H", "authorization: Bearer "+bearer)
	}
	args = append(args, url)
	retries := envInt("RTK_CLOUD_CURL_RETRIES", 3)
	if retries < 1 {
		retries = 1
	}
	var statusBytes []byte
	var lastErr error
	for attempt := 1; attempt <= retries; attempt++ {
		var stderr bytes.Buffer
		cmd := exec.Command("curl", args...)
		cmd.Stderr = &stderr
		statusBytes, err = cmd.Output()
		if err == nil {
			lastErr = nil
			break
		}
		errText := strings.TrimSpace(stderr.String())
		if errText != "" {
			lastErr = fmt.Errorf("curl failed for %s: %w: %s", url, err, errText)
		} else {
			lastErr = fmt.Errorf("curl failed for %s: %w", url, err)
		}
		if attempt < retries {
			time.Sleep(time.Duration(250*attempt*attempt) * time.Millisecond)
		}
	}
	if lastErr != nil {
		return nil, 0, lastErr
	}
	body, err := os.ReadFile(tmp.Name())
	if err != nil {
		return nil, 0, err
	}
	status, _ := strconv.Atoi(strings.TrimSpace(string(statusBytes)))
	return body, status, nil
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
	GenerateOnly   bool
	CAKey          *ecdsa.PrivateKey
	CACert         []byte
	DeviceDays     int
	FactoryURL     string
	FactoryAuthKey string
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
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return generatedDevice{}, false, err
	}
	keyPath := filepath.Join(deviceDir, "device.key.pem")
	if err := writeECPrivateKey(keyPath, key); err != nil {
		return generatedDevice{}, false, err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject:  pkix.Name{Country: []string{"TW"}, Organization: []string{"Realtek Connect Plus Simulation"}, OrganizationalUnit: []string{in.Type.Name}, CommonName: deviceID},
		DNSNames: []string{deviceID + ".simulated.realtek-connect.local"},
		URIs:     mustParseURIs("urn:realtek-connect:simulated-device:" + deviceID),
	}, key)
	if err != nil {
		return generatedDevice{}, false, err
	}
	csrPath := filepath.Join(deviceDir, "device.csr.pem")
	if err := writePEM(csrPath, "CERTIFICATE REQUEST", csrDER, 0o644); err != nil {
		return generatedDevice{}, false, err
	}
	certPath := filepath.Join(deviceDir, "device.cert.pem")
	chainPath := filepath.Join(deviceDir, "device.chain.pem")
	profile := "factory-enrolled-device-mtls-client"
	warning := "Factory-enrolled staging load-test credential. Keep private key material out of source control."
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
		ok, err := factoryEnrollDevice(in, deviceID, display, csrPath, certPath, chainPath)
		if err != nil || !ok {
			return generatedDevice{}, ok, err
		}
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
		MQTTCapability:       in.Type.Capability,
		ServiceOptions:       in.Type.ServiceOptions,
		Model:                in.Type.Model,
		DisplayName:          display,
		FirmwareVersion:      "0.0.0-loadtest",
		Capabilities:         in.Type.Capabilities,
		CertificateProfile:   profile,
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

func factoryEnrollDevice(in loadDeviceInput, deviceID, display, csrPath, certPath, chainPath string) (bool, error) {
	requestID := fmt.Sprintf("%s-%s", in.RunID, deviceID)
	serial := fmt.Sprintf("%s-%s-%04d", in.SerialPrefix, in.RunID, in.Index)
	deviceDir := filepath.Dir(csrPath)
	logLoad("enroll start: index=%03d device=%s type=%s service_options=%s", in.Index, deviceID, in.Type.Name, strings.Join(in.Type.ServiceOptions, ","))
	csrPEM, err := os.ReadFile(csrPath)
	if err != nil {
		return false, err
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
		return false, err
	}
	requestPath := filepath.Join(deviceDir, "factory-enroll-request.json")
	if err := os.WriteFile(requestPath, bodyBytes, 0o644); err != nil {
		return false, err
	}
	timestamp := time.Now().UTC().Format(time.RFC3339)
	signature := signFactoryRequest(in.FactoryAuthKey, "POST", "/v1/factory/enroll", timestamp, requestID, bodyBytes)
	client := &http.Client{Timeout: in.Timeout}
	req, err := http.NewRequest(http.MethodPost, in.FactoryURL+"/v1/factory/enroll", bytes.NewReader(bodyBytes))
	if err != nil {
		return false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Video-Cloud-Request-ID", requestID)
	req.Header.Set("X-Video-Cloud-Timestamp", timestamp)
	req.Header.Set("X-Video-Cloud-Signature", signature)
	resp, err := client.Do(req)
	if err != nil {
		recordEnrollResult(in.ResultsPath, "failed", in.Index, deviceID, in.Type.Name, in.Type.ServiceOptions, "000", requestID, serial, err.Error())
		return false, nil
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		errText := fmt.Sprintf("factory enrollment HTTP %d", resp.StatusCode)
		recordEnrollResult(in.ResultsPath, "failed", in.Index, deviceID, in.Type.Name, in.Type.ServiceOptions, strconv.Itoa(resp.StatusCode), requestID, serial, errText)
		logLoad("enroll failed: index=%03d device=%s type=%s status=%d error=%s", in.Index, deviceID, in.Type.Name, resp.StatusCode, errText)
		return false, nil
	}
	var parsed struct {
		CertificatePEM      string `json:"certificate_pem"`
		CertificateChainPEM string `json:"certificate_chain_pem"`
		SerialNumber        string `json:"serial_number"`
	}
	if err := json.Unmarshal(respBytes, &parsed); err != nil {
		return false, err
	}
	if parsed.CertificatePEM == "" || parsed.CertificateChainPEM == "" {
		errText := "factory enrollment response missing certificate_pem or certificate_chain_pem"
		recordEnrollResult(in.ResultsPath, "failed", in.Index, deviceID, in.Type.Name, in.Type.ServiceOptions, strconv.Itoa(resp.StatusCode), requestID, serial, errText)
		return false, nil
	}
	if err := os.WriteFile(certPath, []byte(parsed.CertificatePEM), 0o644); err != nil {
		return false, err
	}
	if err := os.WriteFile(chainPath, []byte(parsed.CertificateChainPEM), 0o644); err != nil {
		return false, err
	}
	var redacted map[string]any
	_ = json.Unmarshal(respBytes, &redacted)
	delete(redacted, "certificate_pem")
	delete(redacted, "certificate_chain_pem")
	if err := writeJSON(filepath.Join(deviceDir, "factory-enroll-response.redacted.json"), redacted); err != nil {
		return false, err
	}
	if parsed.SerialNumber == "" {
		parsed.SerialNumber = serial
	}
	logLoad("enroll ok: index=%03d device=%s type=%s status=%d serial=%s", in.Index, deviceID, in.Type.Name, resp.StatusCode, parsed.SerialNumber)
	recordEnrollResult(in.ResultsPath, "ok", in.Index, deviceID, in.Type.Name, in.Type.ServiceOptions, strconv.Itoa(resp.StatusCode), requestID, parsed.SerialNumber, "")
	return true, nil
}

func allocateDeviceMix(count int, raw string) (map[string]int, error) {
	weights := map[string]int{"camera": 0, "light": 0, "air_conditioner": 0, "smart_meter": 0}
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

func signDeviceCert(deviceID string, key, caKey *ecdsa.PrivateKey, caPEM []byte, days int) ([]byte, error) {
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
	return x509.CreateCertificate(rand.Reader, tpl, caCert, &key.PublicKey, caKey)
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
- Device key type: EC P-256
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
	case "air_conditioner":
		return fmt.Sprintf("Air Conditioner Simulator %03d", ordinal)
	case "smart_meter":
		return fmt.Sprintf("Smart Meter Simulator %03d", ordinal)
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
	if *bindPath == "" {
		return errors.New("--bind-artifact is required")
	}
	if *outDir == "" {
		return errors.New("--out-dir is required")
	}
	if *waitProvisionedConcurrency <= 0 {
		return errors.New("--wait-provisioned-concurrency must be greater than zero")
	}
	data, err := os.ReadFile(*bindPath)
	if err != nil {
		return err
	}
	var artifact bindArtifact
	if err := json.Unmarshal(data, &artifact); err != nil {
		return err
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
	if artifact.Inputs.UsersFile == "" {
		return bindProvisionWaitResult{}, errors.New("bind artifact missing inputs.users_file; cannot poll brand-cloud provisioning state")
	}
	users, err := readUsersFile(artifact.Inputs.UsersFile)
	if err != nil {
		return bindProvisionWaitResult{}, fmt.Errorf("read bind users file: %w", err)
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
	for _, userSession := range userSessions {
		if _, err := brandCloudUserAccessToken(ctx, artifact.TenantSlug, userSession, func(string, ...any) {}); err != nil {
			return bindProvisionWaitResult{}, err
		}
	}
	defer func() {
		_, _ = updateUsersArtifactTokens(artifact.Inputs.UsersFile, userSessions)
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
		if ready == len(selected) || len(failures) > 0 {
			return result, nil
		}
		if time.Now().After(deadline) {
			result.Failures = bindTimeoutFailures(result.LastStates)
			return result, nil
		}
		time.Sleep(poll)
	}
}

func categorizeBindValidationFailure(failure string) string {
	lower := strings.ToLower(failure)
	switch {
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
	return snapshot.ProvisioningHTTPError != "" || snapshot.ReadinessState == "activation_failed" || snapshot.ProductState == "failed" || snapshot.OperationStatus == "failed" || snapshot.ActivationStatus == "failed"
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
	if *bindPath == "" {
		*bindPath = latestMatchingFile(filepath.Join(envRoot, "artifacts", "device-bind"), slug+"-device-bind-*.json")
		if *bindPath == "" {
			return fmt.Errorf("--bind-artifact was not provided and no bind artifact matched")
		}
	}
	bindAbs, _ := filepath.Abs(*bindPath)
	var artifact bindArtifact
	if data, err := os.ReadFile(bindAbs); err != nil {
		return err
	} else if err := json.Unmarshal(data, &artifact); err != nil {
		return err
	}
	if artifact.Brandname != *brandname {
		return fmt.Errorf("--bind-artifact brandname %s does not match --brandname %s", artifact.Brandname, *brandname)
	}
	if artifact.BrandCloudID == "" {
		return errors.New("--bind-artifact missing brand_cloud_id")
	}
	usersFile := artifact.Inputs.UsersFile
	if usersFile == "" {
		return errors.New("--bind-artifact missing inputs.users_file")
	}
	usersAbs, _ := filepath.Abs(usersFile)
	users, err := readUsersFile(usersAbs)
	if err != nil {
		return err
	}
	if *count == 0 {
		*count = len(artifact.Assignments)
	}
	if *count <= 0 || *count > len(artifact.Assignments) {
		return fmt.Errorf("--count %d exceeds bind assignment count %d", *count, len(artifact.Assignments))
	}
	plan := artifact.Assignments[:*count]
	if *dryRun {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"action": "dry_run", "brandname": *brandname, "brand_cloud_id": artifact.BrandCloudID, "count": *count, "bind_artifact": bindAbs, "users_file": usersAbs, "assignments": plan})
	}
	ctx, err := accountManagerContextFromFlags(*workspaceFlag, *envRootFlag)
	if err != nil {
		return err
	}
	logUnprovision("workspace=%s", workspace)
	logUnprovision("env_root=%s", envRoot)
	logUnprovision("bind_artifact=%s", bindAbs)
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
	if err := writeJSON(outFile, map[string]any{"schema": "rtk-cloud-workspace.bulk-device-unprovision/v1", "generated_at": time.Now().UTC().Format(time.RFC3339), "brandname": *brandname, "brand_cloud_id": artifact.BrandCloudID, "count": *count, "inputs": map[string]string{"bind_artifact": bindAbs, "users_file": usersAbs}, "assignments": results}); err != nil {
		return err
	}
	_ = os.Chmod(outFile, 0o600)
	logUnprovision("unprovision artifact written: %s", outFile)
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"action": "unprovisioned", "brandname": *brandname, "brand_cloud_id": artifact.BrandCloudID, "count": *count, "unprovisioned": len(results), "artifact_file": outFile})
}

type bindDeviceManifest struct {
	DeviceID       string   `json:"device_id"`
	DeviceType     string   `json:"device_type"`
	DisplayName    string   `json:"display_name"`
	ServiceOptions []string `json:"service_options"`
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
	concurrency := fs.Int("concurrency", envInt("CLOUD_BIND_DEVICES_CONCURRENCY", 16), "device binding concurrency")
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
	if *usersPath == "" {
		*usersPath = latestMatchingFile(filepath.Join(envRoot, "artifacts", "users"), slug+"-users-*.json")
		if *usersPath == "" {
			return fmt.Errorf("--users-file was not provided and no users artifact matched")
		}
	}
	usersAbs, _ := filepath.Abs(*usersPath)
	devicesAbs, _ := filepath.Abs(*devicesDir)
	users, usersList, err := readUsersList(usersAbs)
	if err != nil {
		return err
	}
	devices, err := readDeviceManifest(filepath.Join(devicesAbs, "manifests", "devices.json"))
	if err != nil {
		return err
	}
	if *count == 0 {
		*count = len(devices)
	}
	if *count <= 0 || *count > len(devices) {
		return fmt.Errorf("--count %d exceeds device manifest count %d", *count, len(devices))
	}
	assignments := buildBindAssignments(devices[:*count], usersList)
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
	var sessionMu sync.Mutex
	var logMu sync.Mutex
	safeLog := func(format string, args ...any) {
		logMu.Lock()
		defer logMu.Unlock()
		logBind(format, args...)
	}
	safeCreateClaimToken := func(assignment bindAssignment) (map[string]any, error) {
		return createClaimToken(ctx, &session, &sessionMu, safeLog, brandCloudID, assignment, runID, *claimTTL)
	}
	existingDeviceIndex := map[string]map[string]any{}
	if len(assignments) > 0 {
		indexUserSession := userSessions[assignments[0].AssignedEmail]
		indexToken, err := brandCloudUserAccessToken(ctx, tenantSlug, indexUserSession, safeLog)
		if err != nil {
			return err
		}
		safeLog("indexing existing account devices: brand_cloud_id=%s", brandCloudID)
		index, indexed, err := accountIndexDevicesByVideoCloudDevid(ctx, indexToken, brandCloudID)
		if err != nil {
			return err
		}
		existingDeviceIndex = index
		safeLog("indexed existing account devices: active_video_cloud_devices=%d", indexed)
	}
	var progressMu sync.Mutex
	done := 0
	skipped := 0
	createdClaims := 0
	resolvedClaims := 0
	provisionStarted := 0
	progress := func(skippedDelta, createdDelta, resolvedDelta, provisionDelta int) {
		progressMu.Lock()
		defer progressMu.Unlock()
		done++
		skipped += skippedDelta
		createdClaims += createdDelta
		resolvedClaims += resolvedDelta
		provisionStarted += provisionDelta
		safeLog("bind progress: done=%d/%d created_claims=%d resolved_claims=%d provision_started=%d skipped=%d", done, len(assignments), createdClaims, resolvedClaims, provisionStarted, skipped)
	}
	safeLog("device bind concurrency=%d", *concurrency)
	results, err := boundedParallelMap(len(assignments), *concurrency, func(i int) (bindAssignment, error) {
		assignment := assignments[i]
		safeLog("binding device %d/%d: device=%s user=%s services=%s", i+1, len(assignments), assignment.DeviceID, assignment.AssignedEmail, strings.Join(assignment.ServiceOptions, ","))
		existingDevice, exists := existingDeviceIndex[assignment.DeviceID]
		if exists {
			assignment.AccountDeviceID = stringValue(existingDevice["id"])
			assignment.Status = "already_bound"
			safeLog("device already bound; skipping claim: device=%s account_device=%s", assignment.DeviceID, assignment.AccountDeviceID)
			progress(1, 0, 0, 0)
			return assignment, nil
		}
		safeLog("creating claim token: device=%s", assignment.DeviceID)
		claim, err := safeCreateClaimToken(assignment)
		if err != nil {
			return bindAssignment{}, err
		}
		claimToken := stringValue(claim["claim_token"])
		assignment.ClaimID = stringValue(firstPresent(claim, "claim_id", "id"))
		userSession := userSessions[assignment.AssignedEmail]
		if userSession == nil {
			return bindAssignment{}, fmt.Errorf("missing assigned user session: %s", assignment.AssignedEmail)
		}
		safeLog("resolving claim: device=%s user=%s", assignment.DeviceID, assignment.AssignedEmail)
		resolve, err := resolveClaimWithBrandCloudUserRetry(ctx, tenantSlug, brandCloudID, assignment, claimToken, userSession, safeLog)
		if err != nil {
			return bindAssignment{}, err
		}
		if dev, ok := resolve["device"].(map[string]any); ok {
			assignment.AccountDeviceID = stringValue(dev["id"])
		}
		prov, _ := resolve["provision_input"].(map[string]any)
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
		progress(0, 1, 1, 1)
		return assignment, nil
	})
	if err != nil {
		return err
	}
	artifactDir := filepath.Join(envRoot, "artifacts", "device-bind")
	if err := os.MkdirAll(artifactDir, 0o755); err != nil {
		return err
	}
	artifactFile := filepath.Join(artifactDir, fmt.Sprintf("%s-device-bind-%s.json", slug, runID))
	if err := writeJSON(artifactFile, map[string]any{"schema": "rtk-cloud-workspace.bulk-device-bind/v1", "generated_at": time.Now().UTC().Format(time.RFC3339), "brandname": *brandname, "brand_cloud_id": brandCloudID, "tenant_slug": tenantSlug, "count": *count, "inputs": map[string]string{"users_file": usersAbs, "devices_dir": devicesAbs}, "assignments": results}); err != nil {
		return err
	}
	_ = os.Chmod(artifactFile, 0o600)
	if updated, err := updateUsersArtifactTokens(usersAbs, userSessions); err != nil {
		return fmt.Errorf("update users artifact tokens: %w", err)
	} else if updated > 0 {
		logBind("updated users artifact tokens: count=%d file=%s", updated, usersAbs)
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"action": "bound", "brandname": *brandname, "brand_cloud_id": brandCloudID, "count": *count, "created_claims": provisionStarted, "resolved_claims": provisionStarted, "provision_started": provisionStarted, "already_bound": skipped, "artifact_file": artifactFile})
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
	out := make([]bindAssignment, len(devices))
	offset := 0
	for _, typ := range []string{"camera", "light", "air_conditioner", "smart_meter"} {
		indexes := []int{}
		for i, device := range devices {
			if device.DeviceType == typ {
				indexes = append(indexes, i)
			}
		}
		for j, deviceIndex := range indexes {
			device := devices[deviceIndex]
			userIndex := (offset + j) % len(users)
			category := "mqtt_device"
			if contains(device.ServiceOptions, "video_streaming") || contains(device.ServiceOptions, "video_storage") {
				category = "ip_camera"
			}
			out[deviceIndex] = bindAssignment{AssignmentIndex: deviceIndex, AssignedEmail: users[userIndex].Email, DeviceID: device.DeviceID, DeviceType: device.DeviceType, Category: category, ServiceOptions: device.ServiceOptions}
		}
		if len(indexes) > 0 {
			offset = (offset + len(indexes)) % len(users)
		}
	}
	return out
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

func accountFindDeviceByVideoCloudDevid(ctx accountManagerContext, token, brandCloudID, videoCloudDevid string) (map[string]any, bool, error) {
	index, _, err := accountIndexDevicesByVideoCloudDevid(ctx, token, brandCloudID)
	if err != nil {
		return nil, false, err
	}
	device, ok := index[videoCloudDevid]
	return device, ok, nil
}

func accountIndexDevicesByVideoCloudDevid(ctx accountManagerContext, token, brandCloudID string) (map[string]map[string]any, int, error) {
	const limit = 200
	index := map[string]map[string]any{}
	for offset := 0; ; offset += limit {
		body, status, err := curlJSONStatus(fmt.Sprintf("%s/v1/orgs/%s/devices?limit=%d&offset=%d", ctx.BaseURL, brandCloudID, limit, offset), token, nil)
		if err != nil {
			return nil, 0, err
		}
		if status != 200 {
			return nil, 0, fmt.Errorf("device index failed: HTTP %d%s", status, errorBodySuffix(body))
		}
		var parsed map[string]any
		if err := json.Unmarshal(body, &parsed); err != nil {
			return nil, 0, err
		}
		devices := anySlice(parsed["devices"])
		for _, item := range devices {
			device, _ := item.(map[string]any)
			metadata, _ := device["metadata"].(map[string]any)
			videoCloudDevid := stringValue(metadata["video_cloud_devid"])
			if videoCloudDevid != "" && device["disabled_at"] == nil {
				if _, exists := index[videoCloudDevid]; !exists {
					index[videoCloudDevid] = device
				}
			}
		}
		if len(devices) < limit {
			return index, len(index), nil
		}
	}
}

func createClaimToken(ctx accountManagerContext, session *accountPlatformSession, sessionMu *sync.Mutex, logf func(string, ...any), brandCloudID string, assignment bindAssignment, runID string, ttlHours int) (map[string]any, error) {
	expires := time.Now().UTC().Add(time.Duration(ttlHours) * time.Hour).Format(time.RFC3339)
	payload, _ := json.Marshal(map[string]any{
		"organization_id":   brandCloudID,
		"category":          assignment.Category,
		"video_cloud_devid": assignment.DeviceID,
		"activity_id":       "bulk-bind-" + runID + "-" + assignment.DeviceID,
		"clip_public_key":   "bulk-bind-placeholder-public-key",
		"expires_at":        expires,
		"service_options":   assignment.ServiceOptions,
		"metadata":          map[string]any{"source": "cloud-bind-devices", "device_type": assignment.DeviceType, "service_options": assignment.ServiceOptions},
	})
	body, status, err := curlJSONStatusWithPlatformRetryLocked(ctx, session, sessionMu, logf, "claim token create", func(platformToken string) ([]byte, int, error) {
		return curlJSONStatus(ctx.BaseURL+"/v1/admin/device-claim-tokens", platformToken, payload)
	})
	if err != nil {
		return nil, err
	}
	if status != 200 && status != 201 {
		return nil, fmt.Errorf("claim token create failed: device=%s HTTP %d%s", assignment.DeviceID, status, errorBodySuffix(body))
	}
	var parsed map[string]any
	return parsed, json.Unmarshal(body, &parsed)
}

func resolveClaim(ctx accountManagerContext, token, brandCloudID string, assignment bindAssignment, claimToken string) (map[string]any, error) {
	payload, _ := json.Marshal(map[string]string{"claim_token": claimToken, "device_name": firstNonEmpty(assignment.DeviceID, assignment.DeviceID)})
	body, status, err := curlJSONStatus(fmt.Sprintf("%s/v1/orgs/%s/devices/claim/resolve", ctx.BaseURL, brandCloudID), token, payload)
	if err != nil {
		return nil, err
	}
	if status != 200 && status != 201 {
		return nil, fmt.Errorf("claim resolve failed: device=%s HTTP %d%s", assignment.DeviceID, status, errorBodySuffix(body))
	}
	var parsed map[string]any
	return parsed, json.Unmarshal(body, &parsed)
}

func resolveClaimWithBrandCloudUserRetry(ctx accountManagerContext, tenantSlug, brandCloudID string, assignment bindAssignment, claimToken string, user *brandCloudUserSession, logf func(string, ...any)) (map[string]any, error) {
	payload, _ := json.Marshal(map[string]string{"claim_token": claimToken, "device_name": firstNonEmpty(assignment.DeviceID, assignment.DeviceID)})
	body, status, err := curlJSONStatusWithBrandCloudUserRetryLocked(ctx, tenantSlug, user, logf, "claim resolve", func(token string) ([]byte, int, error) {
		return curlJSONStatus(fmt.Sprintf("%s/v1/orgs/%s/devices/claim/resolve", ctx.BaseURL, brandCloudID), token, payload)
	})
	if err != nil {
		return nil, err
	}
	if status != 200 && status != 201 {
		return nil, fmt.Errorf("claim resolve failed: device=%s HTTP %d%s", assignment.DeviceID, status, errorBodySuffix(body))
	}
	var parsed map[string]any
	return parsed, json.Unmarshal(body, &parsed)
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
		return nil, fmt.Errorf("unprovision failed: device=%s account_device=%s HTTP %d", assignment.DeviceID, assignment.AccountDeviceID, status)
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

func loginBrandCloudUserSession(ctx accountManagerContext, tenantSlug, email, password string) (accountPlatformSession, error) {
	payload, _ := json.Marshal(map[string]string{"email": email, "password": password})
	loginURL := fmt.Sprintf("%s/v1/brand-clouds/%s/auth/login", ctx.BaseURL, url.PathEscape(tenantSlug))
	body, status, err := curlJSONStatus(loginURL, "", payload)
	if err != nil {
		return accountPlatformSession{}, err
	}
	if status != 200 {
		return accountPlatformSession{}, fmt.Errorf("brand-cloud login failed: email=%s tenant_slug=%s HTTP %d%s", email, tenantSlug, status, accountAPIErrorSuffix(body))
	}
	session, err := parsePlatformSession(body)
	if err != nil {
		return accountPlatformSession{}, err
	}
	if session.AccessToken == "" {
		return accountPlatformSession{}, fmt.Errorf("brand-cloud login response did not include an access token: %s", email)
	}
	return session, nil
}

func accountRefreshBrandCloudUserSession(ctx accountManagerContext, tenantSlug, email, refreshToken string, logf func(string, ...any)) (accountPlatformSession, error) {
	if strings.TrimSpace(refreshToken) == "" {
		return accountPlatformSession{}, errors.New("brand-cloud user refresh token is empty")
	}
	logf("refreshing brand-cloud user token: email=%s url=%s/v1/brand-clouds/%s/auth/refresh", email, ctx.BaseURL, tenantSlug)
	payload, _ := json.Marshal(map[string]string{"refresh_token": refreshToken})
	refreshURL := fmt.Sprintf("%s/v1/brand-clouds/%s/auth/refresh", ctx.BaseURL, url.PathEscape(tenantSlug))
	body, status, err := curlJSONStatus(refreshURL, "", payload)
	if err != nil {
		return accountPlatformSession{}, err
	}
	if status != 200 {
		return accountPlatformSession{}, fmt.Errorf("brand-cloud user token refresh failed: email=%s HTTP %d%s", email, status, accountAPIErrorSuffix(body))
	}
	session, err := parsePlatformSession(body)
	if err != nil {
		return accountPlatformSession{}, err
	}
	if session.AccessToken == "" || session.RefreshToken == "" {
		return accountPlatformSession{}, fmt.Errorf("brand-cloud user refresh response did not include access and refresh tokens: %s", email)
	}
	logf("brand-cloud user token refresh ok: email=%s", email)
	return session, nil
}

func brandCloudUserAccessToken(ctx accountManagerContext, tenantSlug string, user *brandCloudUserSession, logf func(string, ...any)) (string, error) {
	if user == nil {
		return "", errors.New("brand-cloud user session is nil")
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
		logf("brand-cloud user token refresh failed; falling back to login: email=%s error=%v", user.Email, err)
	}
	logf("logging in brand-cloud user: email=%s", user.Email)
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
		return nil, 0, errors.New("brand-cloud user session is nil")
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
	logf("%s got HTTP 401; refreshing brand-cloud user token before retry: email=%s", operation, user.Email)
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
	if filepath.Base(envRoot) == "staging" {
		return filepath.Join(envRoot, "linode"), nil
	}
	if info, err := os.Stat(filepath.Join(envRoot, "linode")); err == nil && info.IsDir() {
		if _, servicesErr := os.Stat(filepath.Join(envRoot, "services")); os.IsNotExist(servicesErr) {
			if _, envErr := os.Stat(filepath.Join(envRoot, "env")); os.IsNotExist(envErr) {
				return filepath.Join(envRoot, "linode"), nil
			}
		}
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
