package main

import (
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	mrand "math/rand"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

var homeTypes = map[string]bool{
	"light":           true,
	"air_conditioner": true,
	"smart_meter":     true,
}

type userArtifact struct {
	Brandname    string           `json:"brandname"`
	BrandCloudID string           `json:"brand_cloud_id"`
	TenantSlug   string           `json:"tenant_slug"`
	Users        []userCredential `json:"users"`
}

type userCredential struct {
	Email          string                `json:"email"`
	Password       string                `json:"password"`
	AppCredentials appCertificateKeys    `json:"app_credentials"`
	AppCertificate appCertificateSummary `json:"app_certificate"`
}

type appCertificateKeys struct {
	PrivateKeyPEM string `json:"private_key_pem"`
	CSRPem        string `json:"csr_pem"`
}

type appCertificateSummary struct {
	Subject             string `json:"subject"`
	CertificatePEM      string `json:"certificate_pem"`
	CertificateChainPEM string `json:"certificate_chain_pem"`
	FingerprintSHA256   string `json:"fingerprint_sha256"`
}

type bindArtifact struct {
	Brandname    string       `json:"brandname"`
	BrandCloudID string       `json:"brand_cloud_id"`
	TenantSlug   string       `json:"tenant_slug"`
	Assignments  []assignment `json:"assignments"`
}

type assignment struct {
	AssignedEmail  string   `json:"assigned_email"`
	DeviceID       string   `json:"device_id"`
	DeviceType     string   `json:"device_type"`
	ServiceOptions []string `json:"service_options"`
}

type manifestRecord struct {
	DeviceID             string `json:"device_id"`
	DeviceType           string `json:"device_type"`
	CertificatePath      string `json:"certificate_path"`
	CertificateChainPath string `json:"certificate_chain_path"`
	KeyPath              string `json:"key_path"`
}

type certRecord struct {
	DeviceID   string `json:"device_id"`
	DeviceType string `json:"device_type"`
	CertPath   string `json:"cert_path"`
	KeyPath    string `json:"key_path"`
	ChainPath  string `json:"chain_path"`
	CertPEM    string `json:"-"`
	KeyPEM     string `json:"-"`
	ChainPEM   string `json:"-"`
}

type home100KCredentialBundle struct {
	Devices map[string]home100KCredentialDevice
	Source  string
}

type home100KCredentialDevice struct {
	DeviceID   string
	DeviceType string
	CertPEM    string
	KeyPEM     string
	ChainPEM   string
	BundlePEM  string
}

type deviceResult struct {
	DeviceID                string      `json:"device_id"`
	DeviceType              string      `json:"device_type"`
	AssignedEmail           string      `json:"assigned_email"`
	Commands                int         `json:"commands"`
	SuccessPercent          float64     `json:"success_percent"`
	LatencyMS               []float64   `json:"latency_ms"`
	MQTTStatus              string      `json:"mqtt_status"`
	PublishTopic            string      `json:"publish_topic,omitempty"`
	SubscribeTopic          string      `json:"subscribe_topic,omitempty"`
	MessageType             string      `json:"message_type,omitempty"`
	PayloadSchema           string      `json:"payload_schema,omitempty"`
	TelemetryStatus         string      `json:"telemetry_status,omitempty"`
	TelemetryPublishActor   string      `json:"telemetry_publish_actor,omitempty"`
	TelemetrySubscribeActor string      `json:"telemetry_subscribe_actor,omitempty"`
	TelemetryTopic          string      `json:"telemetry_topic,omitempty"`
	CommandStatus           string      `json:"command_status,omitempty"`
	CommandPublishActor     string      `json:"command_publish_actor,omitempty"`
	CommandSubscribeActor   string      `json:"command_subscribe_actor,omitempty"`
	CommandTopic            string      `json:"command_topic,omitempty"`
	AckTopic                string      `json:"ack_topic,omitempty"`
	RuntimeLogStreamID      string      `json:"runtime_log_stream_id,omitempty"`
	RuntimeLogExpectations  []logExpect `json:"runtime_log_expectations,omitempty"`
	TraceChain              []traceStep `json:"trace_chain,omitempty"`
	Error                   string      `json:"error,omitempty"`
}

type logExpect struct {
	Seq     int    `json:"seq"`
	Source  string `json:"source"`
	Message string `json:"message"`
}

type traceStep struct {
	Step      int    `json:"step"`
	Timestamp string `json:"timestamp,omitempty"`
	Phase     string `json:"phase"`
	Actor     string `json:"actor"`
	Action    string `json:"action"`
	Topic     string `json:"topic,omitempty"`
	Status    string `json:"status"`
	Data      string `json:"data,omitempty"`
	Detail    string `json:"detail,omitempty"`
}

type mqttIOTotals struct {
	ConnectAttempts        int64                       `json:"connect_attempts"`
	ConnectSuccesses       int64                       `json:"connect_successes"`
	ConnectFailures        int64                       `json:"connect_failures"`
	SubscribeSuccesses     int64                       `json:"subscribe_successes"`
	ActiveConnections      int64                       `json:"active_connections,omitempty"`
	ActiveSubscriptions    int64                       `json:"active_subscriptions,omitempty"`
	PublishSuccesses       int64                       `json:"publish_successes"`
	PublishFailures        int64                       `json:"publish_failures"`
	MessagesReceived       int64                       `json:"messages_received"`
	DeltaReceived          int64                       `json:"delta_received"`
	ReportedEvents         int64                       `json:"reported_events"`
	AppLoginAttempts       int64                       `json:"app_login_attempts"`
	AppLoginSuccesses      int64                       `json:"app_login_successes"`
	AppLoginFailures       int64                       `json:"app_login_failures"`
	AppDesiredWrites       int64                       `json:"app_desired_writes"`
	AppReceivedAcks        int64                       `json:"app_received_acks"`
	TotalBytesSent         int64                       `json:"total_bytes_sent"`
	TotalBytesReceived     int64                       `json:"total_bytes_received"`
	AuthViolations         int64                       `json:"auth_violations"`
	HTTPRequests           int64                       `json:"http_requests"`
	HTTPSuccesses          int64                       `json:"http_successes"`
	HTTPFailures           int64                       `json:"http_failures"`
	TotalHTTPBytesSent     int64                       `json:"total_http_bytes_sent"`
	TotalHTTPBytesReceived int64                       `json:"total_http_bytes_received"`
	FailureReasons         map[string]int64            `json:"failure_reasons,omitempty"`
	FailureDetails         map[string]map[string]int64 `json:"failure_details,omitempty"`
}

type appBootstrapStatus struct {
	Status            string                `json:"status"`
	Reason            string                `json:"reason,omitempty"`
	UserEmail         string                `json:"user_email,omitempty"`
	DeviceID          string                `json:"device_id,omitempty"`
	Attempts          []appBootstrapAttempt `json:"attempts,omitempty"`
	CertificateStatus string                `json:"certificate_status,omitempty"`
	Subject           string                `json:"subject,omitempty"`
	FingerprintSHA256 string                `json:"fingerprint_sha256,omitempty"`
	TokenScope        string                `json:"token_scope,omitempty"`
	AccessToken       string                `json:"-"`
	CertificateSource string                `json:"certificate_source,omitempty"`
}

type appBootstrapAttempt struct {
	UserEmail string `json:"user_email,omitempty"`
	DeviceID  string `json:"device_id,omitempty"`
	Status    string `json:"status"`
	Reason    string `json:"reason,omitempty"`
}

type appBootstrapMaterial struct {
	Status      appBootstrapStatus
	Certificate tls.Certificate
}

type mqttActorProbe struct {
	DeviceID         string
	DeviceType       string
	Brandname        string
	RunID            string
	DeviceToken      string
	AppToken         string
	Dial             func() (io.ReadWriteCloser, error)
	Timeout          time.Duration
	KeepAliveSeconds uint16
	Now              func() time.Time
}

func main() {
	var root, envRoot, brandname, outDir, profile, maxUsersRaw, mqttProbeRaw, traceDetail, runID string
	var rampUp, telemetryInterval, stateInterval, commandRate, loadModel string
	var stageNamesRaw, stageTargetsRaw, stageDurationsRaw string
	var duration, seed, shardIndex, shardCount, concurrency, maxConnectedDevices int
	flag.StringVar(&root, "root", "", "workspace root")
	flag.StringVar(&envRoot, "env-root", "", "environment root")
	flag.StringVar(&brandname, "brandname", "", "brand name")
	flag.StringVar(&outDir, "out-dir", "", "output directory")
	flag.StringVar(&profile, "profile", "smoke", "profile")
	flag.StringVar(&runID, "run-id", os.Getenv("HOME100K_RUN_ID"), "run id for log correlation")
	flag.IntVar(&duration, "duration-seconds", 120, "duration seconds")
	flag.StringVar(&maxUsersRaw, "max-users", "", "max users")
	flag.IntVar(&seed, "seed", 20260531, "seed")
	flag.StringVar(&mqttProbeRaw, "mqtt-probe", "true", "mqtt probe")
	flag.StringVar(&traceDetail, "trace-detail", "summary", "console trace detail: none, summary, full")
	flag.IntVar(&shardIndex, "shard-index", 0, "load-test shard index")
	flag.IntVar(&shardCount, "shard-count", 1, "load-test shard count")
	flag.StringVar(&rampUp, "ramp-up", "", "load-test ramp-up duration")
	flag.StringVar(&telemetryInterval, "telemetry-interval", "", "load-test telemetry interval")
	flag.StringVar(&stateInterval, "state-interval", "", "load-test state interval")
	flag.StringVar(&commandRate, "command-rate-per-device-per-day", "", "load-test command rate per device per day")
	flag.StringVar(&loadModel, "load-model", "", "load model: actor-separated-probe or home-100k-sustained")
	flag.StringVar(&stageNamesRaw, "stage-names", "", "comma-separated staged sustained load stage names")
	flag.StringVar(&stageTargetsRaw, "stage-connected-devices", "", "comma-separated staged sustained per-shard connected device targets")
	flag.StringVar(&stageDurationsRaw, "stage-durations-seconds", "", "comma-separated staged sustained stage durations in seconds")
	flag.IntVar(&concurrency, "concurrency", 25, "load-test MQTT probe concurrency")
	flag.IntVar(&maxConnectedDevices, "max-connected-devices", 0, "load-test max connected devices in this shard")
	flag.Parse()

	maxUsers := 0
	if maxUsersRaw != "" {
		parsed, err := strconv.Atoi(maxUsersRaw)
		if err != nil {
			fatal(err)
		}
		maxUsers = parsed
	}
	mqttProbe := mqttProbeRaw == "true"
	opts := loadOptions{
		ShardIndex:                  shardIndex,
		ShardCount:                  shardCount,
		RampUp:                      rampUp,
		TelemetryInterval:           telemetryInterval,
		StateInterval:               stateInterval,
		CommandRatePerDevicePerDay:  commandRate,
		LoadModel:                   loadModel,
		StageNames:                  stageNamesRaw,
		StageConnectedDevices:       stageTargetsRaw,
		StageDurationsSeconds:       stageDurationsRaw,
		Concurrency:                 concurrency,
		MaxConnectedDevicesPerShard: maxConnectedDevices,
		RunID:                       runID,
	}
	if err := run(root, envRoot, brandname, outDir, profile, duration, maxUsers, seed, mqttProbe, traceDetail, opts); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(2)
}

type loadOptions struct {
	ShardIndex                  int    `json:"shard_index"`
	ShardCount                  int    `json:"shard_count"`
	RunID                       string `json:"run_id,omitempty"`
	RampUp                      string `json:"ramp_up"`
	TelemetryInterval           string `json:"telemetry_interval"`
	StateInterval               string `json:"state_interval"`
	CommandRatePerDevicePerDay  string `json:"command_rate_per_device_per_day"`
	LoadModel                   string `json:"load_model"`
	StageNames                  string `json:"stage_names,omitempty"`
	StageConnectedDevices       string `json:"stage_connected_devices,omitempty"`
	StageDurationsSeconds       string `json:"stage_durations_seconds,omitempty"`
	Concurrency                 int    `json:"concurrency"`
	MaxConnectedDevicesPerShard int    `json:"max_connected_devices_per_shard"`
}

func (opts loadOptions) validateLoadModel() error {
	switch strings.TrimSpace(opts.LoadModel) {
	case "", "actor-separated-probe", "home-100k-sustained":
		return nil
	default:
		return errors.New("--load-model must be actor-separated-probe or home-100k-sustained")
	}
}

func run(root, envRoot, brandname, outDir, profile string, duration, maxUsers, seed int, mqttProbe bool, traceDetail string, opts loadOptions) error {
	opts.RunID = sanitizeCorrelationID(opts.RunID)
	traceDetail = strings.ToLower(strings.TrimSpace(traceDetail))
	if traceDetail == "" {
		traceDetail = "summary"
	}
	if traceDetail != "none" && traceDetail != "summary" && traceDetail != "full" {
		return errors.New("--trace-detail must be none, summary, or full")
	}
	if profile != "smoke" && profile != "real-case" && profile != "baseline-10k" {
		return errors.New("--profile must be smoke, real-case, or baseline-10k")
	}
	if opts.ShardCount <= 0 || opts.ShardIndex < 0 || opts.ShardIndex >= opts.ShardCount {
		return errors.New("--shard-count must be positive and --shard-index must be within range")
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 25
	}
	if err := opts.validateLoadModel(); err != nil {
		return err
	}
	if profile == "baseline-10k" {
		opts = baseline10KDefaults(opts)
	}
	if strings.TrimSpace(opts.LoadModel) == "" {
		opts.LoadModel = "actor-separated-probe"
	}
	brandLower := strings.ToLower(brandname)
	artifactsDir := filepath.Join(envRoot, "artifacts")
	testDevicesDir := filepath.Join(envRoot, "devices", "test_device")
	stackEnv := filepath.Join(envRoot, "env", "stack.env")
	accountEnv := firstExisting(
		filepath.Join(envRoot, "services", "account-manager", "account-manager.env"),
		filepath.Join(envRoot, "services", "account-manager", "account-manager-public-staging.env"),
	)
	videoEnv := firstExisting(
		filepath.Join(envRoot, "services", "video-cloud", "video-cloud.env"),
		filepath.Join(envRoot, "services", "video-cloud", "video-cloud-staging.env"),
	)
	videoState := videoStatePath(envRoot, stackEnv)

	blockers := []string{}
	required := map[string]string{
		"stack_env":       stackEnv,
		"account_manager": accountEnv,
		"video_env":       videoEnv,
		"video_state":     videoState,
		"device_manifest": filepath.Join(testDevicesDir, "manifests", "devices.json"),
		"device_ids":      filepath.Join(testDevicesDir, "manifests", "device_ids.txt"),
		"loadtest_env":    filepath.Join(testDevicesDir, "loadtest.env"),
	}
	for name, path := range required {
		if !readable(path) {
			blockers = append(blockers, fmt.Sprintf("missing %s: %s", name, path))
		}
	}

	usersPath := latest(filepath.Join(artifactsDir, "users", brandLower+"-users-*.json"))
	bindPath := latestHomeMQTTBindArtifact(filepath.Join(artifactsDir, "device-bind", brandLower+"-device-bind-*.json"), brandLower)
	if usersPath == "" {
		blockers = append(blockers, fmt.Sprintf("missing latest users artifact for brand %s", brandname))
	}
	if bindPath == "" {
		blockers = append(blockers, fmt.Sprintf("missing latest device-bind artifact for brand %s", brandname))
	}
	if usersPath != "" {
		if info, err := os.Stat(usersPath); err == nil && info.Mode().Perm()&0o077 != 0 {
			blockers = append(blockers, fmt.Sprintf("users artifact must not be group/world readable: %s", usersPath))
		}
	}

	inputs := map[string]any{
		"users_artifact":       valueOr(usersPath, "missing"),
		"device_bind_artifact": valueOr(bindPath, "missing"),
		"device_manifest":      required["device_manifest"],
		"env_key_counts": map[string]int{
			"stack":           len(envKeys(stackEnv)),
			"account_manager": len(envKeys(accountEnv)),
			"video_cloud":     len(envKeys(videoEnv)),
		},
	}
	stackValues := envValues(stackEnv)
	accountValues := envValues(accountEnv)
	loadValues := envValues(filepath.Join(testDevicesDir, "loadtest.env"))
	videoEndpoints := resolveVideoCloudEndpoints(envRoot, stackValues)
	accountBaseURL := strings.TrimRight(firstNonEmpty(os.Getenv("ACCOUNT_MANAGER_BASE_URL"), "https://"+firstNonEmpty(stackValues["ACCOUNT_MANAGER_DOMAIN"], accountValues["ACCOUNT_MANAGER_DOMAIN"], "unknown")), "/")
	endpoints := map[string]any{
		"account_manager_base_url":       accountBaseURL,
		"video_cloud_base_url":           videoEndpoints.PublicBaseURL,
		"video_cloud_mtls_base_url":      videoEndpoints.MTLSBaseURL,
		"video_cloud_token_base_url":     videoEndpoints.TokenBootstrapBaseURL,
		"video_cloud_token_endpoint_src": videoEndpoints.TokenBootstrapSource,
	}
	mqttHost, mqttPort := mqttEndpoint(videoState, loadValues)
	if override := strings.TrimSpace(os.Getenv("VIDEO_CLOUD_MQTT_ADDR")); override != "" {
		if host, port, ok := splitHostPortInt(override); ok {
			mqttHost = host
			mqttPort = port
		}
	}
	endpoints["mqtt_host"] = mqttHost
	endpoints["mqtt_port"] = mqttPort

	users := userArtifact{}
	if usersPath != "" {
		if err := readJSON(usersPath, &users); err != nil {
			blockers = append(blockers, "invalid users artifact: "+redactedError(err))
		} else if strings.ToLower(users.Brandname) != brandLower {
			blockers = append(blockers, "users artifact brand mismatch: "+usersPath)
		}
	}
	bind := bindArtifact{}
	if bindPath != "" {
		if err := readJSON(bindPath, &bind); err != nil {
			blockers = append(blockers, "invalid device-bind artifact: "+redactedError(err))
		} else if strings.ToLower(bind.Brandname) != brandLower {
			blockers = append(blockers, "device-bind artifact brand mismatch: "+bindPath)
		}
	}
	if strings.TrimSpace(users.TenantSlug) == "" {
		users.TenantSlug = strings.TrimSpace(bind.TenantSlug)
	}
	manifest := []manifestRecord{}
	if readable(required["device_manifest"]) {
		if err := readJSON(required["device_manifest"], &manifest); err != nil {
			blockers = append(blockers, "invalid device manifest: "+redactedError(err))
		}
	}

	userEmails := map[string]bool{}
	usersByEmail := map[string]userCredential{}
	for _, u := range users.Users {
		if u.Email != "" {
			userEmails[u.Email] = true
			usersByEmail[u.Email] = u
		}
	}
	manifestByID := map[string]manifestRecord{}
	for _, item := range manifest {
		manifestByID[item.DeviceID] = item
	}
	selectedByUser := map[string][]assignment{}
	for _, item := range bind.Assignments {
		if !homeTypes[item.DeviceType] || !contains(item.ServiceOptions, "mqtt") || !userEmails[item.AssignedEmail] {
			continue
		}
		selectedByUser[item.AssignedEmail] = append(selectedByUser[item.AssignedEmail], item)
	}
	if len(selectedByUser) == 0 {
		blockers = append(blockers, "no bound home MQTT devices for users in latest artifacts")
	}
	for _, kind := range []string{"light", "air_conditioner", "smart_meter"} {
		found := false
		for _, rows := range selectedByUser {
			for _, row := range rows {
				if row.DeviceType == kind {
					found = true
				}
			}
		}
		if !found {
			blockers = append(blockers, "missing bound "+kind+" device in latest device-bind artifact")
		}
	}

	selectedUsers := sortedKeys(selectedByUser)
	if maxUsers > 0 && len(selectedUsers) > maxUsers {
		selectedUsers = selectedUsers[:maxUsers]
	}
	selectedAssignments := []assignment{}
	for _, email := range selectedUsers {
		selectedAssignments = append(selectedAssignments, selectedByUser[email]...)
	}
	totalEligibleAssignments := len(selectedAssignments)
	selectedAssignments = shardAssignments(selectedAssignments, opts.ShardIndex, opts.ShardCount)
	if opts.MaxConnectedDevicesPerShard > 0 && len(selectedAssignments) > opts.MaxConnectedDevicesPerShard {
		selectedAssignments = selectedAssignments[:opts.MaxConnectedDevicesPerShard]
	}
	if len(selectedAssignments) == 0 && totalEligibleAssignments > 0 {
		blockers = append(blockers, fmt.Sprintf("shard %d/%d has no selected MQTT devices", opts.ShardIndex, opts.ShardCount))
	}
	credentialBundle, err := loadHome100KCredentialBundle(envRoot)
	if err != nil {
		blockers = append(blockers, "invalid home-100k credential bundle: "+redactedError(err))
	}
	certRecords := []certRecord{}
	for _, item := range selectedAssignments {
		record, ok := manifestByID[item.DeviceID]
		if !ok {
			blockers = append(blockers, fmt.Sprintf("device %s missing from manifest", item.DeviceID))
			continue
		}
		if credentialBundle != nil {
			if bundled, ok := credentialBundle.Devices[item.DeviceID]; ok {
				certRecords = append(certRecords, certRecord{
					DeviceID:   item.DeviceID,
					DeviceType: item.DeviceType,
					CertPEM:    bundled.CertPEM,
					KeyPEM:     bundled.KeyPEM,
					ChainPEM:   bundled.ChainPEM,
				})
				continue
			}
		}
		certRel := firstNonEmpty(record.CertificatePath, filepath.Join("devices", item.DeviceType, item.DeviceID, "device.cert.pem"))
		keyRel := firstNonEmpty(record.KeyPath, filepath.Join("devices", item.DeviceType, item.DeviceID, "device.key.pem"))
		chainRel := firstNonEmpty(record.CertificateChainPath, filepath.Join("devices", item.DeviceType, item.DeviceID, "device.chain.pem"))
		paths := map[string]string{
			"cert":  filepath.Join(testDevicesDir, certRel),
			"key":   filepath.Join(testDevicesDir, keyRel),
			"chain": filepath.Join(testDevicesDir, chainRel),
		}
		for label, path := range paths {
			if !readable(path) {
				blockers = append(blockers, fmt.Sprintf("device %s missing %s file", item.DeviceID, label))
			}
		}
		certRecords = append(certRecords, certRecord{DeviceID: item.DeviceID, DeviceType: item.DeviceType, CertPath: paths["cert"], KeyPath: paths["key"], ChainPath: paths["chain"]})
	}

	base := map[string]any{
		"generated_at":     nowISO(),
		"status":           "PASS",
		"overall":          "pass",
		"brandname":        brandname,
		"profile":          profile,
		"duration_seconds": duration,
		"seed":             seed,
		"load": map[string]any{
			"profile":                     profile,
			"total_eligible_devices":      totalEligibleAssignments,
			"selected_shard_devices":      len(selectedAssignments),
			"shard_index":                 opts.ShardIndex,
			"shard_count":                 opts.ShardCount,
			"ramp_up":                     opts.RampUp,
			"telemetry_interval":          opts.TelemetryInterval,
			"state_interval":              opts.StateInterval,
			"command_rate_per_device_day": opts.CommandRatePerDevicePerDay,
			"load_model":                  opts.LoadModel,
			"stage_names":                 opts.StageNames,
			"stage_connected_devices":     opts.StageConnectedDevices,
			"stage_durations_seconds":     opts.StageDurationsSeconds,
			"concurrency":                 opts.Concurrency,
			"max_connected_devices":       opts.MaxConnectedDevicesPerShard,
		},
		"env":          map[string]string{"root": envRoot},
		"trace_detail": traceDetail,
		"inputs":       inputs,
		"endpoints":    endpoints,
		"blockers":     blockers,
	}
	if len(blockers) > 0 {
		base["status"] = "BLOCKED"
		base["overall"] = "blocked"
		return writeOutputs(outDir, base)
	}
	appBootstrap := appBootstrapStatus{Status: "BLOCKED", Reason: "no selected assignment"}
	appMaterial := appBootstrapMaterial{Status: appBootstrap}
	if len(selectedAssignments) > 0 {
		appMaterial = prepareAppCertificateBootstrapForAssignments(endpoints["account_manager_base_url"].(string), endpoints["video_cloud_token_base_url"].(string), users.TenantSlug, usersByEmail, selectedAssignments, 10)
		appBootstrap = appMaterial.Status
		if appBootstrap.Status == "FAIL" {
			base["status"] = "FAIL"
			base["overall"] = "fail"
		} else if appBootstrap.Status == "BLOCKED" {
			base["status"] = "BLOCKED"
			base["overall"] = "blocked"
			base["blockers"] = append(blockers, "app certificate bootstrap: "+appBootstrap.Reason)
		}
	}

	perDevice := []deviceResult{}
	latencies := []float64{}
	totalCommands := 0
	totalPassed := 0
	successRate := 0.0
	ioTotals := mqttIOTotals{}
	resultModel := "actor_separated_iot"
	resultNotes := []string{}
	capCounts := map[string]map[string]int{}
	for kind := range homeTypes {
		capCounts[kind] = map[string]int{"devices": 0, "commands": 0, "passed": 0}
	}
	mqttProbeResult := "NOT_RUN"
	if !mqttProbe {
		base["status"] = "BLOCKED"
		base["overall"] = "blocked"
		base["blockers"] = []string{"--no-mqtt-probe skips live MQTT E2E"}
	} else if mqttHost == "" || mqttHost == "unknown" || mqttPort == 0 {
		base["status"] = "BLOCKED"
		base["overall"] = "blocked"
		base["blockers"] = []string{"missing MQTT endpoint"}
		mqttProbeResult = "BLOCKED: missing MQTT endpoint"
	} else if appMaterial.Status.Status != "PASS" && opts.LoadModel != "home-100k-sustained" {
		mqttProbeResult = appMaterial.Status.Status + ": app MQTT actor unavailable"
	} else {
		mqttProbeResult = "PASS"
		if opts.LoadModel == "home-100k-sustained" {
			if appMaterial.Status.Status != "PASS" {
				mqttProbeResult = "FAIL: app MQTT actor unavailable; device sustained path still executed"
				resultNotes = append(resultNotes, "app bootstrap unavailable; device sustained connect/subscribe/telemetry path still executed")
			}
			var sustained sustainedLoadResult
			var staged []sustainedStageResult
			if stages, err := parseSustainedStages(opts); err != nil {
				sustained = sustainedLoadResult{Status: "FAIL", Notes: []string{err.Error()}}
			} else if len(stages) > 0 {
				sustained, staged = runStagedSustainedHome100KLoad(selectedAssignments, certRecords, brandname, opts.RunID, endpoints["video_cloud_token_base_url"].(string), mqttHost, mqttPort, appMaterial.Certificate, seed, opts, stages)
			} else {
				sustained = runSustainedHome100KLoad(selectedAssignments, certRecords, brandname, opts.RunID, endpoints["video_cloud_token_base_url"].(string), mqttHost, mqttPort, appMaterial.Certificate, duration, seed, opts)
			}
			attachAppBootstrapTotals(&sustained.Totals, appBootstrap)
			for _, item := range selectedAssignments {
				row := capCounts[item.DeviceType]
				row["devices"]++
			}
			totalCommands = sustained.CommandsAttempted
			totalPassed = sustained.CommandsPassed
			successRate = sustained.SuccessRate()
			ioTotals = sustained.Totals
			resultModel = "home_100k_sustained"
			resultNotes = append(resultNotes, sustained.Notes...)
			if sustained.Status != "PASS" {
				mqttProbeResult = "FAIL"
				base["status"] = "FAIL"
				base["overall"] = "fail"
			}
			if len(staged) > 0 {
				base["stage_results"] = sustainedStageResultsJSON(staged, appBootstrap)
			}
		} else {
			outcomes := runSelectedDeviceProbes(selectedAssignments, certRecords, brandname, opts.RunID, endpoints["video_cloud_token_base_url"].(string), mqttHost, mqttPort, appMaterial.Certificate, opts.Concurrency)
			for _, item := range selectedAssignments {
				row := capCounts[item.DeviceType]
				row["devices"]++
				row["commands"]++
				outcome := outcomes[item.DeviceID]
				row["commands"] += outcome.Commands - 1
				if outcome.MQTTStatus == "PASS" {
					row["passed"] += outcome.Commands
				} else {
					mqttProbeResult = "FAIL"
				}
				outcome.AssignedEmail = item.AssignedEmail
				perDevice = append(perDevice, outcome)
				if len(outcome.LatencyMS) > 0 {
					latencies = append(latencies, outcome.LatencyMS[0])
				}
			}
		}
	}

	if opts.LoadModel != "home-100k-sustained" {
		for _, row := range perDevice {
			totalCommands += row.Commands
			if row.MQTTStatus == "PASS" {
				totalPassed += row.Commands
			}
		}
		if totalCommands > 0 {
			successRate = float64(totalPassed) / float64(totalCommands) * 100.0
		}
		ioTotals = aggregateMQTTIOTotals(perDevice, appBootstrap, totalCommands, totalPassed)
	}
	capMetrics := []map[string]any{}
	for _, kind := range []string{"light", "air_conditioner", "smart_meter"} {
		row := capCounts[kind]
		pct := 0.0
		if row["commands"] > 0 {
			pct = float64(row["passed"]) / float64(row["commands"]) * 100.0
		}
		capMetrics = append(capMetrics, map[string]any{"capability": kind, "devices": row["devices"], "commands": row["commands"], "success_percent": pct})
	}
	result := cloneMap(base)
	result["users"] = userSummaries(selectedUsers, selectedByUser)
	result["devices"] = perDevice
	result["mtls_files"] = mtlsSummaries(certRecords)
	result["metrics"] = map[string]any{
		"users_selected":             len(selectedUsers),
		"devices_selected":           len(selectedAssignments),
		"commands_attempted":         totalCommands,
		"commands_passed":            totalPassed,
		"success_rate_percent":       successRate,
		"command_latency_p95_ms":     percentile(latencies, 95),
		"command_latency_p99_ms":     percentile(latencies, 99),
		"telemetry_freshness_max_ms": maxLatency(perDevice, "smart_meter"),
	}
	attachMQTTIOTotals(result, ioTotals)
	result["capability_metrics"] = capMetrics
	result["negative_checks"] = []any{}
	result["mqtt"] = map[string]any{
		"probe_result":              mqttProbeResult,
		"probe_model":               resultModel,
		"client_identities_checked": len(certRecords),
		"client_identity_mode":      "app_token_and_device_token",
		"telemetry_receiver":        "app_observer",
		"command_receiver":          "device_client",
		"auth_flow":                 "device/app certificate mTLS request_token -> MQTT token credential",
	}
	if len(resultNotes) > 0 {
		result["notes"] = resultNotes
	}
	result["app_certificate_bootstrap"] = appBootstrap
	result["out_of_scope"] = []string{"webrtc", "relay", "storage", "clip", "snapshot"}
	if result["overall"] != "blocked" && successRate < 95 {
		result["status"] = "FAIL"
		result["overall"] = "fail"
	}
	if result["overall"] != "blocked" && mqttProbe && mqttProbeResult != "PASS" {
		result["status"] = "FAIL"
		result["overall"] = "fail"
	}
	return writeOutputs(outDir, result)
}

func runDeviceActorSeparatedEnvelope(record certRecord, brandname, runID, apiBaseURL, host string, port int, appCert tls.Certificate) deviceResult {
	cert, err := loadLeafFirstX509KeyPairForRecord(record)
	if err != nil {
		return failedActorResult(record.DeviceID, record.DeviceType, redactedError(err))
	}
	deviceToken, err := requestDeviceToken(apiBaseURL, cert, record.DeviceID)
	if err != nil {
		return failedActorResult(record.DeviceID, record.DeviceType, redactedError(err))
	}
	appToken, err := requestAppToken(apiBaseURL, appCert, record.DeviceID)
	if err != nil {
		return failedActorResult(record.DeviceID, record.DeviceType, redactedError(err))
	}
	if strings.TrimSpace(appToken.AccessToken) == "" {
		return failedActorResult(record.DeviceID, record.DeviceType, "app request_token response missing access_token")
	}
	result := runActorSeparatedProbe(mqttActorProbe{
		DeviceID:    record.DeviceID,
		DeviceType:  record.DeviceType,
		Brandname:   brandname,
		RunID:       runID,
		DeviceToken: deviceToken,
		AppToken:    appToken.AccessToken,
		Dial: func() (io.ReadWriteCloser, error) {
			return tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", net.JoinHostPort(host, strconv.Itoa(port)), &tls.Config{InsecureSkipVerify: true})
		},
		Timeout: 10 * time.Second,
		Now:     time.Now,
	})
	prefix := []traceStep{
		{Step: 1, Timestamp: nowISO(), Phase: "app_token", Actor: "app_actor", Action: "request_token", Status: "PASS", Detail: "scope=app"},
		{Step: 2, Timestamp: nowISO(), Phase: "device_token", Actor: "device_client", Action: "request_token", Status: "PASS", Detail: "scope=device"},
	}
	result.TraceChain = renumberTrace(append(prefix, result.TraceChain...))
	return result
}

type sustainedLoadResult struct {
	Status            string
	Totals            mqttIOTotals
	CommandsAttempted int
	CommandsPassed    int
	Notes             []string
}

func (r sustainedLoadResult) SuccessRate() float64 {
	if r.CommandsAttempted <= 0 {
		return 0
	}
	return float64(r.CommandsPassed) / float64(r.CommandsAttempted) * 100
}

type sustainedDeviceSession struct {
	Assignment assignment
	Record     certRecord
	Conn       io.ReadWriteCloser
}

type sustainedEvent struct {
	Offset time.Duration
	Kind   string
	Index  int
}

type sustainedStage struct {
	Name            string
	ConnectedTarget int
	DurationSeconds int
}

type sustainedStageResult struct {
	Name              string
	ConnectedTarget   int
	ActiveConnections int
	Status            string
	Totals            mqttIOTotals
	CommandsAttempted int
	CommandsPassed    int
	Notes             []string
	Diagnostics       sustainedStageDiagnostics
}

type sustainedStageDiagnostics struct {
	StageStartedAt       string  `json:"stage_started_at,omitempty"`
	StageDeadlineAt      string  `json:"stage_deadline_at,omitempty"`
	ConnectStartedAt     string  `json:"connect_started_at,omitempty"`
	ConnectDeadlineAt    string  `json:"connect_deadline_at,omitempty"`
	ConnectFinishedAt    string  `json:"connect_finished_at,omitempty"`
	ActionStartedAt      string  `json:"action_started_at,omitempty"`
	StageDurationSeconds int     `json:"stage_duration_seconds,omitempty"`
	ConnectWindowSeconds float64 `json:"connect_window_seconds,omitempty"`
	ActionWindowSeconds  float64 `json:"action_window_seconds,omitempty"`
	ConnectedBefore      int     `json:"connected_before"`
	ConnectedAfter       int     `json:"connected_after"`
	ConnectedTarget      int     `json:"connected_target"`
	NewAssignments       int     `json:"new_assignments"`
	ConnectAttempts      int64   `json:"connect_attempts"`
	ConnectSuccesses     int64   `json:"connect_successes"`
	ConnectFailures      int64   `json:"connect_failures"`
	SubscribeSuccesses   int64   `json:"subscribe_successes"`
	CommandsScheduled    int     `json:"commands_scheduled"`
	CommandsAttempted    int     `json:"commands_attempted"`
	CommandsPassed       int     `json:"commands_passed"`
	SkipReason           string  `json:"skip_reason,omitempty"`
}

func parseSustainedStages(opts loadOptions) ([]sustainedStage, error) {
	if strings.TrimSpace(opts.StageNames) == "" && strings.TrimSpace(opts.StageConnectedDevices) == "" && strings.TrimSpace(opts.StageDurationsSeconds) == "" {
		return nil, nil
	}
	names := splitCSV(opts.StageNames)
	targets, err := parseCSVInts(opts.StageConnectedDevices)
	if err != nil {
		return nil, fmt.Errorf("--stage-connected-devices: %w", err)
	}
	durations, err := parseCSVInts(opts.StageDurationsSeconds)
	if err != nil {
		return nil, fmt.Errorf("--stage-durations-seconds: %w", err)
	}
	if len(names) == 0 || len(targets) == 0 || len(durations) == 0 || len(names) != len(targets) || len(names) != len(durations) {
		return nil, errors.New("--stage-names, --stage-connected-devices, and --stage-durations-seconds must have the same non-zero length")
	}
	stages := make([]sustainedStage, 0, len(names))
	lastTarget := 0
	for idx := range names {
		if targets[idx] <= 0 {
			return nil, fmt.Errorf("stage %s connected target must be positive", names[idx])
		}
		if targets[idx] < lastTarget {
			return nil, fmt.Errorf("stage %s connected target must not decrease", names[idx])
		}
		if durations[idx] <= 0 {
			return nil, fmt.Errorf("stage %s duration must be positive", names[idx])
		}
		stages = append(stages, sustainedStage{Name: names[idx], ConnectedTarget: targets[idx], DurationSeconds: durations[idx]})
		lastTarget = targets[idx]
	}
	return stages, nil
}

func splitCSV(raw string) []string {
	parts := strings.Split(raw, ",")
	out := []string{}
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part != "" {
			out = append(out, part)
		}
	}
	return out
}

func parseCSVInts(raw string) ([]int, error) {
	parts := splitCSV(raw)
	out := make([]int, 0, len(parts))
	for _, part := range parts {
		value, err := strconv.Atoi(part)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, nil
}

func runSustainedHome100KLoad(assignments []assignment, certs []certRecord, brandname, runID, apiBaseURL, host string, port int, appCert tls.Certificate, durationSeconds int, seed int, opts loadOptions) sustainedLoadResult {
	result := sustainedLoadResult{Status: "PASS"}
	window := time.Duration(durationSeconds) * time.Second
	if window <= 0 {
		result.Status = "FAIL"
		result.Notes = append(result.Notes, "invalid sustained load duration")
		return result
	}
	certByID := map[string]certRecord{}
	for _, cert := range certs {
		certByID[cert.DeviceID] = cert
	}
	start := time.Now()
	deadline := start.Add(window)
	sessions := connectSustainedDevicesUntil(assignments, certByID, brandname, runID, apiBaseURL, host, port, opts.Concurrency, deadline, &result.Totals)
	defer func() {
		for _, session := range sessions {
			_ = session.Conn.Close()
		}
	}()
	if len(sessions) == 0 {
		result.Status = "FAIL"
		result.Notes = append(result.Notes, "no sustained device MQTT sessions connected")
		return result
	}

	events := sustainedEvents(sessions, opts, seed, window)
	for _, event := range events {
		if !time.Now().Before(deadline) {
			result.Status = "FAIL"
			result.Notes = append(result.Notes, "sustained load deadline reached before all scheduled events completed")
			break
		}
		if event.Offset > 0 {
			waitUntil := start.Add(event.Offset)
			if delay := time.Until(waitUntil); delay > 0 {
				sleepUntilDeadline(delay, deadline)
			}
		}
		if !time.Now().Before(deadline) {
			result.Status = "FAIL"
			result.Notes = append(result.Notes, "sustained load deadline reached before all scheduled events completed")
			break
		}
		session := sessions[event.Index%len(sessions)]
		switch event.Kind {
		case "telemetry":
			if publishSustainedTelemetry(session, brandname, runID, &result.Totals) != nil {
				result.Status = "FAIL"
			}
		case "command":
			if time.Until(deadline) < 25*time.Second {
				result.Status = "FAIL"
				result.Notes = append(result.Notes, "skipped desired write because remaining stage time was below command budget")
				continue
			}
			result.CommandsAttempted++
			if runSustainedShadowCommand(session, brandname, runID, apiBaseURL, host, port, appCert, &result.Totals) == nil {
				result.CommandsPassed++
			} else {
				result.Status = "FAIL"
			}
		}
	}
	if result.CommandsAttempted == 0 {
		result.Status = "FAIL"
		result.Notes = append(result.Notes, "sustained user command schedule produced zero desired writes")
	}
	return result
}

func runStagedSustainedHome100KLoad(assignments []assignment, certs []certRecord, brandname, runID, apiBaseURL, host string, port int, appCert tls.Certificate, seed int, opts loadOptions, stages []sustainedStage) (sustainedLoadResult, []sustainedStageResult) {
	overall := sustainedLoadResult{Status: "PASS"}
	certByID := map[string]certRecord{}
	for _, cert := range certs {
		certByID[cert.DeviceID] = cert
	}
	sessions := []sustainedDeviceSession{}
	defer func() {
		for _, session := range sessions {
			_ = session.Conn.Close()
		}
	}()
	results := make([]sustainedStageResult, 0, len(stages))
	for idx, stage := range stages {
		stageResult := sustainedStageResult{Name: stage.Name, ConnectedTarget: stage.ConnectedTarget, Status: "PASS"}
		stageWindow := time.Duration(stage.DurationSeconds) * time.Second
		stageStart := time.Now()
		stageDeadline := stageStart.Add(stageWindow)
		stageResult.Diagnostics = sustainedStageDiagnostics{
			StageStartedAt:       stageStart.UTC().Format(time.RFC3339Nano),
			StageDeadlineAt:      stageDeadline.UTC().Format(time.RFC3339Nano),
			StageDurationSeconds: stage.DurationSeconds,
			ConnectedBefore:      len(sessions),
			ConnectedTarget:      stage.ConnectedTarget,
		}
		if stage.ConnectedTarget > len(assignments) {
			stageResult.Status = "FAIL"
			stageResult.Notes = append(stageResult.Notes, fmt.Sprintf("stage target %d exceeds selected assignments %d", stage.ConnectedTarget, len(assignments)))
			stageResult.Diagnostics.SkipReason = "target_exceeds_selected_assignments"
			overall.Status = "FAIL"
			results = append(results, stageResult)
			break
		}
		if stage.ConnectedTarget > len(sessions) {
			newAssignments := assignments[len(sessions):stage.ConnectedTarget]
			connectDeadline := stagedConnectDeadline(stageStart, stageDeadline)
			connectStarted := time.Now()
			before := stageResult.Totals
			stageResult.Diagnostics.ConnectStartedAt = connectStarted.UTC().Format(time.RFC3339Nano)
			stageResult.Diagnostics.ConnectDeadlineAt = connectDeadline.UTC().Format(time.RFC3339Nano)
			stageResult.Diagnostics.ConnectWindowSeconds = connectDeadline.Sub(connectStarted).Seconds()
			stageResult.Diagnostics.NewAssignments = len(newAssignments)
			newSessions := connectSustainedDevicesUntil(newAssignments, certByID, brandname, runID, apiBaseURL, host, port, opts.Concurrency, connectDeadline, &stageResult.Totals)
			stageResult.Diagnostics.ConnectFinishedAt = time.Now().UTC().Format(time.RFC3339Nano)
			stageResult.Diagnostics.ConnectAttempts = stageResult.Totals.ConnectAttempts - before.ConnectAttempts
			stageResult.Diagnostics.ConnectSuccesses = stageResult.Totals.ConnectSuccesses - before.ConnectSuccesses
			stageResult.Diagnostics.ConnectFailures = stageResult.Totals.ConnectFailures - before.ConnectFailures
			stageResult.Diagnostics.SubscribeSuccesses = stageResult.Totals.SubscribeSuccesses - before.SubscribeSuccesses
			sessions = append(sessions, newSessions...)
			if len(sessions) < stage.ConnectedTarget {
				stageResult.Status = "FAIL"
				stageResult.Notes = append(stageResult.Notes, fmt.Sprintf("stage %s did not reach connected target before action window: active=%d target=%d", stage.Name, len(sessions), stage.ConnectedTarget))
				stageResult.Diagnostics.SkipReason = "device_connect_target_missed"
				recordSyntheticFailure(&stageResult.Totals, "device_connect_target_missed", fmt.Sprintf("active=%d target=%d", len(sessions), stage.ConnectedTarget))
			}
		}
		stageResult.ActiveConnections = len(sessions)
		stageResult.Diagnostics.ConnectedAfter = len(sessions)
		stageResult.Totals.ActiveConnections = int64(len(sessions))
		stageResult.Totals.ActiveSubscriptions = int64(len(sessions))
		if len(sessions) == 0 {
			stageResult.Status = "FAIL"
			stageResult.Notes = append(stageResult.Notes, "no sustained device MQTT sessions connected")
			stageResult.Diagnostics.SkipReason = "no_sustained_device_mqtt_sessions_connected"
			overall.Status = "FAIL"
			results = append(results, stageResult)
			break
		}
		if len(sessions) < stage.ConnectedTarget {
			stageResult.Notes = append(stageResult.Notes, fmt.Sprintf("stage %s skipped shadow action because connected target was not reached", stage.Name))
			stageResult.Diagnostics.SkipReason = firstNonEmptyString(stageResult.Diagnostics.SkipReason, "connected_target_not_reached")
			overall.Status = "FAIL"
			overall.Totals = addMQTTIOTotals(overall.Totals, stageResult.Totals)
			overall.Notes = append(overall.Notes, prefixedNotes(stage.Name, stageResult.Notes)...)
			results = append(results, stageResult)
			continue
		}
		actionStart := time.Now()
		actionWindow := time.Until(stageDeadline)
		stageResult.Diagnostics.ActionStartedAt = actionStart.UTC().Format(time.RFC3339Nano)
		stageResult.Diagnostics.ActionWindowSeconds = actionWindow.Seconds()
		if actionWindow <= 0 {
			stageResult.Status = "FAIL"
			stageResult.Notes = append(stageResult.Notes, fmt.Sprintf("stage %s deadline reached before action window", stage.Name))
			stageResult.Diagnostics.SkipReason = "deadline_reached_before_action_window"
			overall.Status = "FAIL"
			results = append(results, stageResult)
			continue
		}
		stageOpts := opts
		stageOpts.MaxConnectedDevicesPerShard = stage.ConnectedTarget
		events := sustainedEvents(sessions, stageOpts, seed+idx, actionWindow)
		for _, event := range events {
			if event.Kind == "command" {
				stageResult.Diagnostics.CommandsScheduled++
			}
		}
		for _, event := range events {
			if !time.Now().Before(stageDeadline) {
				stageResult.Status = "FAIL"
				stageResult.Notes = append(stageResult.Notes, fmt.Sprintf("stage %s deadline reached before all scheduled events completed", stage.Name))
				break
			}
			if event.Offset > 0 {
				waitUntil := actionStart.Add(event.Offset)
				if delay := time.Until(waitUntil); delay > 0 {
					sleepUntilDeadline(delay, stageDeadline)
				}
			}
			if !time.Now().Before(stageDeadline) {
				stageResult.Status = "FAIL"
				stageResult.Notes = append(stageResult.Notes, fmt.Sprintf("stage %s deadline reached before all scheduled events completed", stage.Name))
				break
			}
			session := sessions[event.Index%len(sessions)]
			switch event.Kind {
			case "telemetry":
				if publishSustainedTelemetry(session, brandname, runID, &stageResult.Totals) != nil {
					stageResult.Status = "FAIL"
				}
			case "command":
				if time.Until(stageDeadline) < 15*time.Second {
					stageResult.Status = "FAIL"
					stageResult.Notes = append(stageResult.Notes, fmt.Sprintf("stage %s skipped desired write because remaining time was below command budget", stage.Name))
					stageResult.Diagnostics.SkipReason = "remaining_time_below_command_budget"
					continue
				}
				stageResult.CommandsAttempted++
				if runSustainedShadowCommand(session, brandname, runID, apiBaseURL, host, port, appCert, &stageResult.Totals) == nil {
					stageResult.CommandsPassed++
				} else {
					stageResult.Status = "FAIL"
				}
			}
		}
		if stageResult.CommandsAttempted == 0 {
			stageResult.Status = "FAIL"
			stageResult.Notes = append(stageResult.Notes, "sustained user command schedule produced zero desired writes")
			stageResult.Diagnostics.SkipReason = firstNonEmptyString(stageResult.Diagnostics.SkipReason, "zero_desired_writes_scheduled_or_attempted")
		}
		stageResult.Diagnostics.CommandsAttempted = stageResult.CommandsAttempted
		stageResult.Diagnostics.CommandsPassed = stageResult.CommandsPassed
		if stageResult.Status != "PASS" {
			overall.Status = "FAIL"
		}
		overall.Totals = addMQTTIOTotals(overall.Totals, stageResult.Totals)
		overall.CommandsAttempted += stageResult.CommandsAttempted
		overall.CommandsPassed += stageResult.CommandsPassed
		overall.Notes = append(overall.Notes, prefixedNotes(stage.Name, stageResult.Notes)...)
		results = append(results, stageResult)
	}
	return overall, results
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func stagedConnectDeadline(stageStart time.Time, stageDeadline time.Time) time.Time {
	if stageStart.IsZero() || stageDeadline.IsZero() || !stageDeadline.After(stageStart) {
		return stageDeadline
	}
	window := stageDeadline.Sub(stageStart)
	actionReserve := window / 2
	if actionReserve < 30*time.Second {
		actionReserve = 30 * time.Second
	}
	if actionReserve > 90*time.Second {
		actionReserve = 90 * time.Second
	}
	if actionReserve >= window {
		return stageDeadline
	}
	return stageDeadline.Add(-actionReserve)
}

func connectSustainedDevices(assignments []assignment, certByID map[string]certRecord, brandname, runID, apiBaseURL, host string, port int, concurrency int, totals *mqttIOTotals) []sustainedDeviceSession {
	return connectSustainedDevicesUntil(assignments, certByID, brandname, runID, apiBaseURL, host, port, concurrency, time.Time{}, totals)
}

func connectSustainedDevicesUntil(assignments []assignment, certByID map[string]certRecord, brandname, runID, apiBaseURL, host string, port int, concurrency int, deadline time.Time, totals *mqttIOTotals) []sustainedDeviceSession {
	if concurrency <= 0 {
		concurrency = 25
	}
	type job struct {
		Index      int
		Assignment assignment
	}
	jobs := make(chan job)
	results := make(chan sustainedDeviceSession, len(assignments))
	var mu sync.Mutex
	var wg sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				if deadlineReached(deadline) {
					return
				}
				record := certByID[item.Assignment.DeviceID]
				mu.Lock()
				totals.ConnectAttempts++
				mu.Unlock()
				conn, err := connectSustainedDevice(record, brandname, runID, apiBaseURL, host, port)
				if err != nil {
					mu.Lock()
					totals.ConnectFailures++
					recordFailure(totals, "device_mqtt_connect_failed", err)
					mu.Unlock()
					continue
				}
				deltaTopic := "$vc/devices/" + record.DeviceID + "/shadow/update/delta"
				if err := mqttSubscribe(conn, uint16((item.Index%60000)+1), deltaTopic); err != nil {
					_ = conn.Close()
					mu.Lock()
					totals.ConnectFailures++
					recordFailure(totals, "device_delta_subscribe_failed", err)
					mu.Unlock()
					continue
				}
				clearConnDeadline(conn)
				mu.Lock()
				totals.ConnectSuccesses++
				totals.SubscribeSuccesses++
				mu.Unlock()
				results <- sustainedDeviceSession{Assignment: item.Assignment, Record: record, Conn: conn}
			}
		}()
	}
	go func() {
		defer func() {
			close(jobs)
			wg.Wait()
			close(results)
		}()
		for idx, assignment := range assignments {
			if deadlineReached(deadline) {
				break
			}
			if deadline.IsZero() {
				jobs <- job{Index: idx, Assignment: assignment}
				continue
			}
			wait := time.Until(deadline)
			if wait <= 0 {
				break
			}
			timer := time.NewTimer(wait)
			select {
			case jobs <- job{Index: idx, Assignment: assignment}:
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			case <-timer.C:
				return
			}
		}
	}()
	sessions := []sustainedDeviceSession{}
	for session := range results {
		sessions = append(sessions, session)
	}
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].Assignment.DeviceID < sessions[j].Assignment.DeviceID
	})
	return sessions
}

func deadlineReached(deadline time.Time) bool {
	return !deadline.IsZero() && !time.Now().Before(deadline)
}

func sleepUntilDeadline(delay time.Duration, deadline time.Time) {
	if delay <= 0 {
		return
	}
	if !deadline.IsZero() {
		if remaining := time.Until(deadline); remaining <= 0 {
			return
		} else if delay > remaining {
			delay = remaining
		}
	}
	time.Sleep(delay)
}

func connectSustainedDevice(record certRecord, brandname, runID, apiBaseURL, host string, port int) (io.ReadWriteCloser, error) {
	cert, err := loadLeafFirstX509KeyPairForRecord(record)
	if err != nil {
		return nil, err
	}
	deviceToken, err := requestDeviceToken(apiBaseURL, cert, record.DeviceID)
	if err != nil {
		return nil, fmt.Errorf("device request_token: %w", err)
	}
	return connectMQTTActor(mqttActorProbe{
		DeviceID:    record.DeviceID,
		DeviceType:  record.DeviceType,
		Brandname:   brandname,
		RunID:       runID,
		DeviceToken: deviceToken,
		Dial: func() (io.ReadWriteCloser, error) {
			conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", net.JoinHostPort(host, strconv.Itoa(port)), &tls.Config{InsecureSkipVerify: true})
			if err != nil {
				return nil, fmt.Errorf("mqtt tls dial: %w", err)
			}
			return conn, nil
		},
		Timeout:          10 * time.Second,
		KeepAliveSeconds: sustainedMQTTKeepAliveSeconds,
		Now:              time.Now,
	}, "device", record.DeviceID, deviceToken)
}

func sustainedEvents(sessions []sustainedDeviceSession, opts loadOptions, seed int, window time.Duration) []sustainedEvent {
	telemetryInterval := parseDurationDefault(opts.TelemetryInterval, window)
	events := []sustainedEvent{}
	for idx, session := range sessions {
		for _, offset := range telemetrySchedule(session.Record.DeviceID, seed, telemetryInterval, window) {
			events = append(events, sustainedEvent{Offset: offset, Kind: "telemetry", Index: idx})
		}
	}
	rate, _ := strconv.ParseFloat(strings.TrimSpace(opts.CommandRatePerDevicePerDay), 64)
	for idx, offset := range userCommandSchedule(len(sessions), rate, window, int64(seed)+int64(len(sessions))*7919) {
		events = append(events, sustainedEvent{Offset: offset, Kind: "command", Index: idx % len(sessions)})
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].Offset == events[j].Offset {
			return events[i].Kind < events[j].Kind
		}
		return events[i].Offset < events[j].Offset
	})
	return events
}

func publishSustainedTelemetry(session sustainedDeviceSession, brandname string, runID string, totals *mqttIOTotals) error {
	messageID := fmt.Sprintf("msg-home100k-%s-%s-%d", probeCorrelationID(runID, time.Now()), session.Record.DeviceID, time.Now().UnixNano())
	topic, payload, err := sampleHomeStatusReport(session.Record.DeviceID, session.Record.DeviceType, brandname, messageID, time.Now().UTC())
	if err != nil {
		return err
	}
	if err := mqttPublish(session.Conn, topic, payload); err != nil {
		totals.PublishFailures++
		recordFailure(totals, "device_telemetry_publish_failed", err)
		return err
	}
	totals.PublishSuccesses++
	totals.TotalBytesSent += int64(len(topic) + len(payload))
	return nil
}

func runSustainedShadowCommand(session sustainedDeviceSession, brandname, runID, apiBaseURL, host string, port int, appCert tls.Certificate, totals *mqttIOTotals) error {
	appToken, err := requestAppToken(apiBaseURL, appCert, session.Record.DeviceID)
	if err != nil {
		totals.HTTPFailures++
		recordFailure(totals, "app_token_request_failed", err)
		return err
	}
	appConn, err := connectMQTTActor(mqttActorProbe{
		DeviceID:   session.Record.DeviceID,
		DeviceType: session.Record.DeviceType,
		Brandname:  brandname,
		RunID:      runID,
		AppToken:   appToken.AccessToken,
		Dial: func() (io.ReadWriteCloser, error) {
			conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", net.JoinHostPort(host, strconv.Itoa(port)), &tls.Config{InsecureSkipVerify: true})
			if err != nil {
				return nil, fmt.Errorf("mqtt tls dial: %w", err)
			}
			return conn, nil
		},
		Timeout:          10 * time.Second,
		KeepAliveSeconds: sustainedMQTTKeepAliveSeconds,
		Now:              time.Now,
	}, "app-controller", appMQTTUsername(session.Record.DeviceID), appToken.AccessToken)
	if err != nil {
		totals.HTTPFailures++
		recordFailure(totals, "app_mqtt_connect_failed", err)
		return err
	}
	defer appConn.Close()
	clearConnDeadline(appConn)

	shadowUpdateTopic := "$vc/devices/" + session.Record.DeviceID + "/shadow/update"
	documentsTopic := shadowUpdateTopic + "/documents"
	deltaTopic := shadowUpdateTopic + "/delta"
	if err := mqttSubscribe(appConn, 1, documentsTopic); err != nil {
		totals.HTTPFailures++
		recordFailure(totals, "app_shadow_documents_subscribe_failed", err)
		return err
	}
	commandID := fmt.Sprintf("cmd-home100k-%s-%s-%d", probeCorrelationID(runID, time.Now()), session.Record.DeviceID, time.Now().UnixNano())
	desiredState := shadowStateWithLoadTestMarker(desiredStateForCapability(session.Record.DeviceType), probeCorrelationID(runID, time.Now()), commandID)
	reportedState := shadowStateWithLoadTestMarker(reportedStateForCapability(session.Record.DeviceType), probeCorrelationID(runID, time.Now()), commandID)
	desiredPayload, err := json.Marshal(map[string]any{
		"state":       map[string]any{"desired": desiredState},
		"clientToken": commandID,
	})
	if err != nil {
		totals.HTTPFailures++
		recordFailure(totals, "desired_payload_encode_failed", err)
		return err
	}
	if err := mqttPublish(appConn, shadowUpdateTopic, desiredPayload); err != nil {
		totals.HTTPFailures++
		recordFailure(totals, "app_desired_publish_failed", err)
		return err
	}
	recorder := newRuntimeLogRecorder(session.Record.DeviceID, runID, time.Now)
	if err := recorder.Record(appConn, "shadow_desired", "app_controller", "publish", shadowUpdateTopic, map[string]any{"direction": "app_to_device", "command_id": commandID}); err != nil {
		totals.HTTPFailures++
		recordFailure(totals, "app_desired_runtime_log_failed", err)
		return err
	}
	totals.AppDesiredWrites++
	totals.HTTPRequests++
	totals.TotalHTTPBytesSent += int64(len(shadowUpdateTopic) + len(desiredPayload))
	if _, err := waitForMQTTPublishWithDeadline(session.Conn, deltaTopic, 10*time.Second, func(doc map[string]any) bool {
		return doc["clientToken"] == commandID
	}); err != nil {
		totals.HTTPFailures++
		recordFailure(totals, "device_delta_wait_failed", err)
		return err
	}
	totals.MessagesReceived++
	totals.DeltaReceived++
	if err := recorder.Record(session.Conn, "shadow_delta", "device_client", "receive", deltaTopic, map[string]any{"direction": "app_to_device", "command_id": commandID}); err != nil {
		totals.HTTPFailures++
		recordFailure(totals, "device_delta_runtime_log_failed", err)
		return err
	}
	reportedPayload, err := json.Marshal(map[string]any{
		"state":       map[string]any{"reported": reportedState},
		"clientToken": "reported-" + commandID,
	})
	if err != nil {
		totals.HTTPFailures++
		recordFailure(totals, "reported_payload_encode_failed", err)
		return err
	}
	if err := mqttPublish(session.Conn, shadowUpdateTopic, reportedPayload); err != nil {
		totals.PublishFailures++
		recordFailure(totals, "device_reported_publish_failed", err)
		return err
	}
	totals.PublishSuccesses++
	totals.ReportedEvents++
	totals.TotalBytesSent += int64(len(shadowUpdateTopic) + len(reportedPayload))
	if err := recorder.Record(session.Conn, "shadow_reported", "device_client", "publish", shadowUpdateTopic, map[string]any{"direction": "device_to_app", "command_id": commandID}); err != nil {
		totals.PublishFailures++
		recordFailure(totals, "device_reported_runtime_log_failed", err)
		return err
	}
	if _, err := waitForMQTTPublishWithDeadline(appConn, documentsTopic, 10*time.Second, func(doc map[string]any) bool {
		return doc["clientToken"] == "reported-"+commandID && shadowDocumentsDeltaCleared(doc)
	}); err != nil {
		totals.HTTPFailures++
		recordFailure(totals, "app_delta_clear_wait_failed", err)
		return err
	}
	if err := recorder.Record(appConn, "shadow_reported", "app_observer", "receive", documentsTopic, map[string]any{"direction": "device_to_app", "command_id": commandID}); err != nil {
		totals.HTTPFailures++
		recordFailure(totals, "app_reported_runtime_log_failed", err)
		return err
	}
	totals.AppReceivedAcks++
	totals.HTTPSuccesses++
	return nil
}

func parseDurationDefault(raw string, fallback time.Duration) time.Duration {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "off", "none", "disabled", "false":
		return 0
	}
	if duration, err := time.ParseDuration(strings.TrimSpace(raw)); err == nil && duration > 0 {
		return duration
	}
	if fallback > 0 {
		return fallback
	}
	return time.Minute
}

func waitForMQTTPublishWithDeadline(conn io.Reader, topic string, timeout time.Duration, match func(map[string]any) bool) (map[string]any, error) {
	if setter, ok := conn.(interface{ SetReadDeadline(time.Time) error }); ok {
		_ = setter.SetReadDeadline(time.Now().Add(timeout))
		defer setter.SetReadDeadline(time.Time{})
	}
	return waitForMQTTPublish(conn, topic, timeout, match)
}

func clearConnDeadline(conn io.ReadWriter) {
	if setter, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		_ = setter.SetDeadline(time.Time{})
	}
}

func loadLeafFirstX509KeyPair(certPath, chainPath, keyPath string) (tls.Certificate, error) {
	if strings.TrimSpace(certPath) != "" {
		cert, err := tls.LoadX509KeyPair(certPath, keyPath)
		if err == nil {
			return cert, nil
		}
		if strings.TrimSpace(chainPath) == "" || chainPath == certPath {
			return tls.Certificate{}, err
		}
	}
	return tls.LoadX509KeyPair(chainPath, keyPath)
}

func loadLeafFirstX509KeyPairForRecord(record certRecord) (tls.Certificate, error) {
	if strings.TrimSpace(record.CertPEM) != "" || strings.TrimSpace(record.ChainPEM) != "" || strings.TrimSpace(record.KeyPEM) != "" {
		certPEM := strings.TrimSpace(record.CertPEM)
		if certPEM == "" {
			certPEM = strings.TrimSpace(record.ChainPEM)
		}
		if certPEM != "" && strings.TrimSpace(record.KeyPEM) != "" {
			cert, err := tls.X509KeyPair([]byte(certPEM), []byte(strings.TrimSpace(record.KeyPEM)))
			if err == nil {
				return cert, nil
			}
			if strings.TrimSpace(record.ChainPEM) == "" || strings.TrimSpace(record.ChainPEM) == certPEM {
				return tls.Certificate{}, err
			}
		}
		return tls.X509KeyPair([]byte(strings.TrimSpace(record.ChainPEM)), []byte(strings.TrimSpace(record.KeyPEM)))
	}
	return loadLeafFirstX509KeyPair(record.CertPath, record.ChainPath, record.KeyPath)
}

func loadHome100KCredentialBundle(envRoot string) (*home100KCredentialBundle, error) {
	matches, err := filepath.Glob(filepath.Join(envRoot, "loadtests", "home-100k", "credentials", "*.sqlite.gz"))
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil
	}
	sort.Strings(matches)
	source := matches[0]
	sqlitePath, cleanup, err := gunzipToTempFile(source)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	db, err := sql.Open("sqlite", sqlitePath)
	if err != nil {
		return nil, err
	}
	defer db.Close()
	rows, err := db.Query(`select device_id, device_type, cert_pem, key_pem, chain_pem, bundle_pem from devices`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	bundle := &home100KCredentialBundle{Devices: map[string]home100KCredentialDevice{}, Source: source}
	for rows.Next() {
		var device home100KCredentialDevice
		var certPEM, keyPEM, chainPEM, bundlePEM sql.NullString
		if err := rows.Scan(&device.DeviceID, &device.DeviceType, &certPEM, &keyPEM, &chainPEM, &bundlePEM); err != nil {
			return nil, err
		}
		device.CertPEM = certPEM.String
		device.KeyPEM = keyPEM.String
		device.ChainPEM = chainPEM.String
		device.BundlePEM = bundlePEM.String
		if strings.TrimSpace(device.DeviceID) != "" {
			bundle.Devices[device.DeviceID] = device
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return bundle, nil
}

func gunzipToTempFile(path string) (string, func(), error) {
	in, err := os.Open(path)
	if err != nil {
		return "", func() {}, err
	}
	defer in.Close()
	gz, err := gzip.NewReader(in)
	if err != nil {
		return "", func() {}, err
	}
	defer gz.Close()
	tmp, err := os.CreateTemp("", "home-100k-credentials-*.sqlite")
	if err != nil {
		return "", func() {}, err
	}
	tmpPath := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpPath) }
	if _, err := io.Copy(tmp, gz); err != nil {
		_ = tmp.Close()
		cleanup()
		return "", func() {}, err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return tmpPath, cleanup, nil
}

func baseline10KDefaults(opts loadOptions) loadOptions {
	if opts.RampUp == "" {
		opts.RampUp = "10m"
	}
	if opts.TelemetryInterval == "" {
		opts.TelemetryInterval = "5m"
	}
	if opts.StateInterval == "" {
		opts.StateInterval = "1h"
	}
	if opts.CommandRatePerDevicePerDay == "" {
		opts.CommandRatePerDevicePerDay = "1"
	}
	if opts.Concurrency <= 0 {
		opts.Concurrency = 250
	}
	return opts
}

func telemetrySchedule(deviceID string, seed int, interval time.Duration, window time.Duration) []time.Duration {
	if strings.TrimSpace(deviceID) == "" || interval <= 0 || window <= 0 {
		return nil
	}
	hash := sha256.Sum256([]byte(fmt.Sprintf("%d:%s", seed, deviceID)))
	phaseNanos := int64(binary.BigEndian.Uint64(hash[:8]) % uint64(interval.Nanoseconds()))
	offsets := []time.Duration{}
	for offset := time.Duration(phaseNanos); offset < window; offset += interval {
		offsets = append(offsets, offset)
	}
	return offsets
}

func userCommandSchedule(deviceCount int, commandsPerDevicePerDay float64, window time.Duration, seed int64) []time.Duration {
	if deviceCount <= 0 || commandsPerDevicePerDay <= 0 || window <= 0 {
		return nil
	}
	lambdaPerSecond := float64(deviceCount) * commandsPerDevicePerDay / 86400.0
	if lambdaPerSecond <= 0 {
		return nil
	}
	rng := mrand.New(mrand.NewSource(seed))
	offsets := []time.Duration{}
	elapsedSeconds := 0.0
	windowSeconds := window.Seconds()
	for elapsedSeconds < windowSeconds {
		elapsedSeconds += rng.ExpFloat64() / lambdaPerSecond
		if elapsedSeconds >= windowSeconds {
			break
		}
		offsets = append(offsets, time.Duration(elapsedSeconds*float64(time.Second)))
	}
	return offsets
}

func shardAssignments(items []assignment, shardIndex, shardCount int) []assignment {
	if shardCount <= 1 {
		return append([]assignment(nil), items...)
	}
	out := []assignment{}
	for idx, item := range items {
		if idx%shardCount == shardIndex {
			out = append(out, item)
		}
	}
	return out
}

func runSelectedDeviceProbes(assignments []assignment, certs []certRecord, brandname, runID, apiBaseURL, host string, port int, appCert tls.Certificate, concurrency int) map[string]deviceResult {
	if concurrency <= 0 {
		concurrency = 25
	}
	if concurrency > len(assignments) && len(assignments) > 0 {
		concurrency = len(assignments)
	}
	type job struct {
		Assignment assignment
		Cert       certRecord
	}
	jobs := make(chan job)
	results := make(chan deviceResult, len(assignments))
	var wg sync.WaitGroup
	for worker := 0; worker < concurrency; worker++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for item := range jobs {
				results <- runDeviceActorSeparatedEnvelope(item.Cert, brandname, runID, apiBaseURL, host, port, appCert)
			}
		}()
	}
	for _, item := range assignments {
		jobs <- job{Assignment: item, Cert: findCert(certs, item.DeviceID)}
	}
	close(jobs)
	wg.Wait()
	close(results)
	out := map[string]deviceResult{}
	for row := range results {
		out[row.DeviceID] = row
	}
	return out
}

func failedActorResult(deviceID, deviceType, reason string) deviceResult {
	return deviceResult{DeviceID: deviceID, DeviceType: deviceType, Commands: 2, SuccessPercent: 0, MQTTStatus: "FAIL", TelemetryStatus: "FAIL", CommandStatus: "FAIL", LatencyMS: []float64{0}, Error: reason}
}

func runActorSeparatedProbe(probe mqttActorProbe) deviceResult {
	if probe.Timeout <= 0 {
		probe.Timeout = 10 * time.Second
	}
	if probe.Now == nil {
		probe.Now = time.Now
	}
	start := time.Now()
	upTopic := "devices/" + probe.DeviceID + "/up/messages"
	downTopic := "devices/" + probe.DeviceID + "/down/commands"
	shadowUpdateTopic := "$vc/devices/" + probe.DeviceID + "/shadow/update"
	shadowAcceptedTopic := shadowUpdateTopic + "/accepted"
	shadowDocumentsTopic := shadowUpdateTopic + "/documents"
	shadowDeltaTopic := shadowUpdateTopic + "/delta"
	correlationID := probeCorrelationID(probe.RunID, probe.Now())
	logStreamID := fmt.Sprintf("mqtt-e2e-%s-%s", correlationID, probe.DeviceID)
	result := deviceResult{
		DeviceID:                probe.DeviceID,
		DeviceType:              probe.DeviceType,
		Commands:                2,
		SuccessPercent:          0,
		MQTTStatus:              "FAIL",
		TelemetryStatus:         "FAIL",
		CommandStatus:           "FAIL",
		LatencyMS:               []float64{0, 0},
		PublishTopic:            upTopic,
		SubscribeTopic:          upTopic,
		MessageType:             "status_report",
		PayloadSchema:           "home_device_message/v1",
		TelemetryPublishActor:   "device_client",
		TelemetrySubscribeActor: "app_observer",
		TelemetryTopic:          upTopic,
		CommandPublishActor:     "app_controller",
		CommandSubscribeActor:   "device_client",
		CommandTopic:            shadowUpdateTopic,
		AckTopic:                upTopic,
		RuntimeLogStreamID:      logStreamID,
	}
	recorder := runtimeLogRecorder{deviceID: probe.DeviceID, streamID: logStreamID, now: probe.Now}
	recordRuntimeLog := func(conn io.ReadWriter, phase, actor, action, topic string, attrs map[string]any) error {
		expect, err := recorder.RecordWithExpectation(conn, phase, actor, action, topic, attrs)
		if err != nil {
			return err
		}
		result.RuntimeLogExpectations = append(result.RuntimeLogExpectations, expect)
		return nil
	}
	appObserver, err := connectMQTTActor(probe, "app-observer", appMQTTUsername(probe.DeviceID), probe.AppToken)
	if err != nil {
		result.Error = "app MQTT actor unauthorized or unavailable: " + redactedError(err)
		result.TraceChain = appendTrace(result.TraceChain, "mqtt_connect", "app_observer", "mqtt_connect", "", "FAIL", "")
		return result
	}
	result.TraceChain = appendTrace(result.TraceChain, "mqtt_connect", "app_observer", "mqtt_connect", "", "PASS", "")
	defer appObserver.Close()
	device, err := connectMQTTActor(probe, "device", probe.DeviceID, probe.DeviceToken)
	if err != nil {
		result.Error = "device MQTT actor unauthorized or unavailable: " + redactedError(err)
		result.TraceChain = appendTrace(result.TraceChain, "mqtt_connect", "device_client", "mqtt_connect", "", "FAIL", "")
		return result
	}
	result.TraceChain = appendTrace(result.TraceChain, "mqtt_connect", "device_client", "mqtt_connect", "", "PASS", "")
	defer device.Close()
	appController, err := connectMQTTActor(probe, "app-controller", appMQTTUsername(probe.DeviceID), probe.AppToken)
	if err != nil {
		result.Error = "app MQTT actor unauthorized or unavailable: " + redactedError(err)
		result.TraceChain = appendTrace(result.TraceChain, "mqtt_connect", "app_controller", "mqtt_connect", "", "FAIL", "")
		return result
	}
	result.TraceChain = appendTrace(result.TraceChain, "mqtt_connect", "app_controller", "mqtt_connect", "", "PASS", "")
	defer appController.Close()

	if err := mqttSubscribe(appObserver, 1, upTopic); err != nil {
		result.Error = "app observer subscribe failed: " + redactedError(err)
		result.TraceChain = appendTrace(result.TraceChain, "telemetry", "app_observer", "subscribe", upTopic, "FAIL", "")
		return result
	}
	result.TraceChain = appendTrace(result.TraceChain, "telemetry", "app_observer", "subscribe", upTopic, "PASS", "")
	if err := mqttSubscribe(device, 1, downTopic); err != nil {
		result.Error = "device command subscribe failed: " + redactedError(err)
		result.TraceChain = appendTrace(result.TraceChain, "command", "device_client", "subscribe", downTopic, "FAIL", "")
		return result
	}
	result.TraceChain = appendTrace(result.TraceChain, "command", "device_client", "subscribe", downTopic, "PASS", "")
	if err := mqttSubscribe(appObserver, 2, shadowAcceptedTopic); err != nil {
		result.Error = "app shadow accepted subscribe failed: " + redactedError(err)
		result.TraceChain = appendTrace(result.TraceChain, "shadow_desired", "app_observer", "subscribe", shadowAcceptedTopic, "FAIL", "")
		return result
	}
	result.TraceChain = appendTrace(result.TraceChain, "shadow_desired", "app_observer", "subscribe", shadowAcceptedTopic, "PASS", "")
	if err := mqttSubscribe(appObserver, 3, shadowDocumentsTopic); err != nil {
		result.Error = "app shadow documents subscribe failed: " + redactedError(err)
		result.TraceChain = appendTrace(result.TraceChain, "shadow_reported", "app_observer", "subscribe", shadowDocumentsTopic, "FAIL", "")
		return result
	}
	result.TraceChain = appendTrace(result.TraceChain, "shadow_reported", "app_observer", "subscribe", shadowDocumentsTopic, "PASS", "")
	if err := mqttSubscribe(device, 4, shadowDeltaTopic); err != nil {
		result.Error = "device shadow delta subscribe failed: " + redactedError(err)
		result.TraceChain = appendTrace(result.TraceChain, "shadow_delta", "device_client", "subscribe", shadowDeltaTopic, "FAIL", "")
		return result
	}
	result.TraceChain = appendTrace(result.TraceChain, "shadow_delta", "device_client", "subscribe", shadowDeltaTopic, "PASS", "")

	messageID := fmt.Sprintf("msg-mqtt-e2e-%s-%s", correlationID, probe.DeviceID)
	_, telemetryPayload, err := sampleHomeStatusReport(probe.DeviceID, probe.DeviceType, probe.Brandname, messageID, probe.Now().UTC())
	if err != nil {
		result.Error = redactedError(err)
		return result
	}
	if err := mqttPublish(device, upTopic, telemetryPayload); err != nil {
		result.Error = "device telemetry publish failed: " + redactedError(err)
		result.TraceChain = appendTrace(result.TraceChain, "telemetry", "device_client", "publish", upTopic, "FAIL", "")
		return result
	}
	telemetryData := traceDataSummaryFromPayload(telemetryPayload, "device_to_app")
	result.TraceChain = appendTraceData(result.TraceChain, "telemetry", "device_client", "publish", upTopic, "PASS", telemetryData, "")
	if err := recordRuntimeLog(device, "telemetry", "device_client", "publish", upTopic, map[string]any{"direction": "device_to_app", "message_id": messageID}); err != nil {
		result.Error = "device telemetry runtime log publish failed: " + redactedError(err)
		return result
	}
	telemetryDoc, err := waitForMQTTPublish(appObserver, upTopic, probe.Timeout, func(doc map[string]any) bool {
		return doc["sample_type"] == "home_device_message" && doc["message_id"] == messageID
	})
	if err != nil {
		result.Error = "app observer did not receive device telemetry: " + redactedError(err)
		result.LatencyMS = []float64{float64(time.Since(start).Milliseconds()), 0}
		result.TraceChain = appendTrace(result.TraceChain, "telemetry", "app_observer", "receive", upTopic, "FAIL", "")
		return result
	}
	result.TraceChain = appendTraceData(result.TraceChain, "telemetry", "app_observer", "receive", upTopic, "PASS", traceDataSummary(telemetryDoc), "")
	if err := recordRuntimeLog(appObserver, "telemetry", "app_observer", "receive", upTopic, map[string]any{"direction": "device_to_app", "message_id": messageID}); err != nil {
		result.Error = "app telemetry runtime log publish failed: " + redactedError(err)
		return result
	}
	result.TelemetryStatus = "PASS"
	telemetryLatency := float64(time.Since(start).Milliseconds())

	commandID := fmt.Sprintf("cmd-mqtt-e2e-%s-%s", correlationID, probe.DeviceID)
	legacyCommandPayload, err := sampleHomeCommand(probe.DeviceID, probe.DeviceType, commandID, probe.Now().UTC())
	if err != nil {
		result.Error = redactedError(err)
		return result
	}
	desiredState := shadowStateWithLoadTestMarker(desiredStateForCapability(probe.DeviceType), correlationID, commandID)
	reportedState := shadowStateWithLoadTestMarker(reportedStateForCapability(probe.DeviceType), correlationID, commandID)
	commandPayload, err := json.Marshal(map[string]any{
		"state":       map[string]any{"desired": desiredState},
		"clientToken": commandID,
	})
	if err != nil {
		result.Error = redactedError(err)
		return result
	}
	commandStart := time.Now()
	if err := mqttPublish(appController, shadowUpdateTopic, commandPayload); err != nil {
		result.Error = "app shadow desired publish failed: " + redactedError(err)
		result.TraceChain = appendTrace(result.TraceChain, "shadow_desired", "app_controller", "publish", shadowUpdateTopic, "FAIL", "")
		return result
	}
	commandData := traceDataSummaryFromPayload(legacyCommandPayload, "app_to_device")
	result.TraceChain = appendTraceData(result.TraceChain, "shadow_desired", "app_controller", "publish", shadowUpdateTopic, "PASS", commandData, "")
	if err := recordRuntimeLog(appController, "shadow_desired", "app_controller", "publish", shadowUpdateTopic, map[string]any{"direction": "app_to_device", "command_id": commandID}); err != nil {
		result.Error = "app command runtime log publish failed: " + redactedError(err)
		return result
	}
	if _, err := waitForMQTTPublish(appObserver, shadowAcceptedTopic, probe.Timeout, func(doc map[string]any) bool {
		return doc["clientToken"] == commandID
	}); err != nil {
		result.Error = "app observer did not receive shadow desired accepted: " + redactedError(err)
		result.LatencyMS = []float64{telemetryLatency, float64(time.Since(commandStart).Milliseconds())}
		result.TraceChain = appendTrace(result.TraceChain, "shadow_desired", "app_observer", "receive", shadowAcceptedTopic, "FAIL", "")
		return result
	}
	result.TraceChain = appendTrace(result.TraceChain, "shadow_desired", "app_observer", "receive", shadowAcceptedTopic, "PASS", "")
	deltaDoc, err := waitForMQTTPublish(device, shadowDeltaTopic, probe.Timeout, func(doc map[string]any) bool {
		return doc["clientToken"] == commandID
	})
	if err != nil {
		result.Error = "device did not receive shadow delta: " + redactedError(err)
		result.LatencyMS = []float64{telemetryLatency, float64(time.Since(commandStart).Milliseconds())}
		result.TraceChain = appendTrace(result.TraceChain, "shadow_delta", "device_client", "receive", shadowDeltaTopic, "FAIL", "")
		return result
	}
	result.TraceChain = appendTraceData(result.TraceChain, "shadow_delta", "device_client", "receive", shadowDeltaTopic, "PASS", traceDataSummary(deltaDoc), "")
	if err := recordRuntimeLog(device, "shadow_delta", "device_client", "receive", shadowDeltaTopic, map[string]any{"direction": "app_to_device", "command_id": commandID}); err != nil {
		result.Error = "device command runtime log publish failed: " + redactedError(err)
		return result
	}
	ackPayload, err := json.Marshal(map[string]any{
		"state":       map[string]any{"reported": reportedState},
		"clientToken": "reported-" + commandID,
	})
	if err != nil {
		result.Error = redactedError(err)
		return result
	}
	if err := mqttPublish(device, shadowUpdateTopic, ackPayload); err != nil {
		result.Error = "device shadow reported publish failed: " + redactedError(err)
		result.TraceChain = appendTrace(result.TraceChain, "shadow_reported", "device_client", "publish", shadowUpdateTopic, "FAIL", "")
		return result
	}
	ackData := traceDataSummaryFromPayload(ackPayload, "device_to_app")
	result.TraceChain = appendTraceData(result.TraceChain, "shadow_reported", "device_client", "publish", shadowUpdateTopic, "PASS", ackData, "")
	if err := recordRuntimeLog(device, "shadow_reported", "device_client", "publish", shadowUpdateTopic, map[string]any{"direction": "device_to_app", "command_id": commandID}); err != nil {
		result.Error = "device command ack runtime log publish failed: " + redactedError(err)
		return result
	}
	ackDoc, err := waitForMQTTPublish(appObserver, shadowDocumentsTopic, probe.Timeout, func(doc map[string]any) bool {
		return doc["clientToken"] == "reported-"+commandID && shadowDocumentsDeltaCleared(doc)
	})
	if err != nil {
		result.Error = "app observer did not receive shadow reported documents: " + redactedError(err)
		result.LatencyMS = []float64{telemetryLatency, float64(time.Since(commandStart).Milliseconds())}
		result.TraceChain = appendTrace(result.TraceChain, "shadow_reported", "app_observer", "receive", shadowDocumentsTopic, "FAIL", "")
		return result
	}
	result.TraceChain = appendTraceData(result.TraceChain, "shadow_reported", "app_observer", "receive", shadowDocumentsTopic, "PASS", traceDataSummary(ackDoc), "")
	if err := recordRuntimeLog(appObserver, "shadow_reported", "app_observer", "receive", shadowDocumentsTopic, map[string]any{"direction": "device_to_app", "command_id": commandID}); err != nil {
		result.Error = "app command ack runtime log publish failed: " + redactedError(err)
		return result
	}
	result.CommandStatus = "PASS"
	result.MQTTStatus = "PASS"
	result.SuccessPercent = 100
	result.LatencyMS = []float64{telemetryLatency, float64(time.Since(commandStart).Milliseconds())}
	return result
}

const sustainedMQTTKeepAliveSeconds uint16 = 900

func connectMQTTActor(probe mqttActorProbe, actor, username, password string) (io.ReadWriteCloser, error) {
	if probe.Dial == nil {
		return nil, errors.New("missing MQTT dialer")
	}
	conn, err := probe.Dial()
	if err != nil {
		return nil, fmt.Errorf("mqtt dial: %w", err)
	}
	if setter, ok := conn.(interface{ SetDeadline(time.Time) error }); ok {
		_ = setter.SetDeadline(time.Now().Add(probe.Timeout))
	}
	clientID := fmt.Sprintf("rtk-e2e-%s-%s-%s-%d", probeCorrelationID(probe.RunID, probe.Now()), probe.DeviceID, actor, os.Getpid())
	keepAliveSeconds := probe.KeepAliveSeconds
	if keepAliveSeconds == 0 {
		keepAliveSeconds = 30
	}
	if err := mqttConnect(conn, clientID, username, password, keepAliveSeconds); err != nil {
		_ = conn.Close()
		return nil, err
	}
	return conn, nil
}

func probeCorrelationID(runID string, now time.Time) string {
	runID = sanitizeCorrelationID(runID)
	if runID != "" {
		return runID
	}
	if now.IsZero() {
		now = time.Now()
	}
	return strconv.FormatInt(now.Unix(), 10)
}

func sanitizeCorrelationID(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func waitForMQTTPublish(conn io.Reader, topic string, timeout time.Duration, match func(map[string]any) bool) (map[string]any, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		packetType, body, err := mqttReadPacket(conn)
		if err != nil {
			return nil, err
		}
		if packetType>>4 != 3 {
			continue
		}
		gotTopic, message, err := mqttDecodePublish(packetType&0x0f, body)
		if err != nil || gotTopic != topic {
			continue
		}
		doc := map[string]any{}
		if err := json.Unmarshal(message, &doc); err != nil {
			continue
		}
		if match(doc) {
			return doc, nil
		}
	}
	return nil, errors.New("timed out waiting for MQTT publish")
}

func appMQTTUsername(deviceID string) string {
	return "app-user:" + deviceID
}

func appendTrace(chain []traceStep, phase, actor, action, topic, status, detail string) []traceStep {
	return appendTraceData(chain, phase, actor, action, topic, status, "", detail)
}

func appendTraceData(chain []traceStep, phase, actor, action, topic, status, data, detail string) []traceStep {
	step := traceStep{
		Step:      len(chain) + 1,
		Timestamp: nowISO(),
		Phase:     phase,
		Actor:     actor,
		Action:    action,
		Topic:     topic,
		Status:    status,
		Data:      traceDetail(data),
		Detail:    traceDetail(detail),
	}
	return append(chain, step)
}

func renumberTrace(chain []traceStep) []traceStep {
	for i := range chain {
		chain[i].Step = i + 1
		if chain[i].Timestamp == "" {
			chain[i].Timestamp = nowISO()
		}
		chain[i].Data = traceDetail(chain[i].Data)
		chain[i].Detail = traceDetail(chain[i].Detail)
	}
	return chain
}

func aggregateMQTTIOTotals(rows []deviceResult, appBootstrap appBootstrapStatus, commandsAttempted int, commandsPassed int) mqttIOTotals {
	var totals mqttIOTotals
	for _, row := range rows {
		for _, step := range row.TraceChain {
			statusPass := strings.EqualFold(step.Status, "PASS")
			statusFail := strings.EqualFold(step.Status, "FAIL")
			switch step.Actor {
			case "device_client":
				switch step.Action {
				case "mqtt_connect":
					totals.ConnectAttempts++
					if statusPass {
						totals.ConnectSuccesses++
					} else if statusFail {
						totals.ConnectFailures++
					}
				case "subscribe":
					if statusPass {
						totals.SubscribeSuccesses++
					}
				case "publish":
					if statusPass {
						totals.PublishSuccesses++
						totals.TotalBytesSent += tracePayloadBytes(step)
						if step.Phase == "command_ack" {
							totals.ReportedEvents++
						}
					} else if statusFail {
						totals.PublishFailures++
					}
				case "receive":
					if statusPass {
						totals.MessagesReceived++
						totals.TotalBytesReceived += tracePayloadBytes(step)
						if step.Phase == "shadow_delta" {
							totals.DeltaReceived++
						}
					}
				}
				if step.Phase == "shadow_reported" && step.Action == "publish" && statusPass {
					totals.ReportedEvents++
				}
			case "app_controller":
				if step.Action == "publish" {
					if statusPass {
						totals.AppDesiredWrites++
						totals.TotalHTTPBytesSent += tracePayloadBytes(step)
					} else if statusFail {
						totals.HTTPFailures++
					}
				}
			case "app_observer":
				if step.Action == "receive" && statusPass {
					if step.Phase == "shadow_reported" || step.Phase == "command_ack" {
						totals.AppReceivedAcks++
					}
					totals.TotalHTTPBytesReceived += tracePayloadBytes(step)
				}
			}
		}
	}
	if strings.EqualFold(appBootstrap.Status, "PASS") || strings.EqualFold(appBootstrap.Status, "FAIL") {
		totals.AppLoginAttempts = 1
		if strings.EqualFold(appBootstrap.Status, "PASS") {
			totals.AppLoginSuccesses = 1
		} else {
			totals.AppLoginFailures = 1
		}
	}
	totals.HTTPRequests = totals.AppDesiredWrites
	totals.HTTPSuccesses = totals.AppReceivedAcks
	if totals.HTTPFailures == 0 && totals.AppDesiredWrites > totals.AppReceivedAcks {
		totals.HTTPFailures = totals.AppDesiredWrites - totals.AppReceivedAcks
	}
	if strings.EqualFold(appBootstrap.Status, "FAIL") {
		totals.AuthViolations = 1
	}
	return totals
}

func tracePayloadBytes(step traceStep) int64 {
	size := len(step.Topic) + len(step.Data)
	if size <= 0 {
		return 0
	}
	return int64(size)
}

func attachMQTTIOTotals(result map[string]any, totals mqttIOTotals) {
	result["connect_attempts"] = totals.ConnectAttempts
	result["connect_successes"] = totals.ConnectSuccesses
	result["connect_failures"] = totals.ConnectFailures
	result["subscribe_successes"] = totals.SubscribeSuccesses
	result["active_connections"] = totals.ActiveConnections
	result["active_subscriptions"] = totals.ActiveSubscriptions
	result["publish_successes"] = totals.PublishSuccesses
	result["publish_failures"] = totals.PublishFailures
	result["messages_received"] = totals.MessagesReceived
	result["reported_events"] = totals.ReportedEvents
	result["total_bytes_sent"] = totals.TotalBytesSent
	result["total_bytes_received"] = totals.TotalBytesReceived
	result["auth_violations"] = totals.AuthViolations
	result["http_requests"] = totals.HTTPRequests
	result["http_successes"] = totals.HTTPSuccesses
	result["http_failures"] = totals.HTTPFailures
	result["total_http_bytes_sent"] = totals.TotalHTTPBytesSent
	result["total_http_bytes_received"] = totals.TotalHTTPBytesReceived
	if len(totals.FailureReasons) > 0 {
		result["failure_reasons"] = totals.FailureReasons
	}
	if len(totals.FailureDetails) > 0 {
		result["failure_details"] = totals.FailureDetails
	}
	result["device_mqtt_totals"] = map[string]any{
		"connect_attempts":     totals.ConnectAttempts,
		"connect_success":      totals.ConnectSuccesses,
		"connect_fail":         totals.ConnectFailures,
		"subscribes":           totals.SubscribeSuccesses,
		"active_connections":   totals.ActiveConnections,
		"active_subscriptions": totals.ActiveSubscriptions,
		"publishes":            totals.PublishSuccesses + totals.PublishFailures,
		"received_messages":    totals.MessagesReceived,
		"delta_received":       totals.DeltaReceived,
		"reported_publishes":   totals.ReportedEvents,
		"rejected_publishes":   totals.PublishFailures,
		"bytes_sent":           totals.TotalBytesSent,
		"bytes_received":       totals.TotalBytesReceived,
	}
	result["app_user_totals"] = map[string]any{
		"login_attempts":        totals.AppLoginAttempts,
		"login_success":         totals.AppLoginSuccesses,
		"login_fail":            totals.AppLoginFailures,
		"list_devices_requests": 0,
		"read_shadow_requests":  0,
		"desired_writes":        totals.AppDesiredWrites,
		"received_acks":         totals.AppReceivedAcks,
		"bytes_sent":            totals.TotalHTTPBytesSent,
		"bytes_received":        totals.TotalHTTPBytesReceived,
	}
}

func addMQTTIOTotals(a mqttIOTotals, b mqttIOTotals) mqttIOTotals {
	a.ConnectAttempts += b.ConnectAttempts
	a.ConnectSuccesses += b.ConnectSuccesses
	a.ConnectFailures += b.ConnectFailures
	a.SubscribeSuccesses += b.SubscribeSuccesses
	a.ActiveConnections = b.ActiveConnections
	a.ActiveSubscriptions = b.ActiveSubscriptions
	a.PublishSuccesses += b.PublishSuccesses
	a.PublishFailures += b.PublishFailures
	a.MessagesReceived += b.MessagesReceived
	a.DeltaReceived += b.DeltaReceived
	a.ReportedEvents += b.ReportedEvents
	a.AppLoginAttempts += b.AppLoginAttempts
	a.AppLoginSuccesses += b.AppLoginSuccesses
	a.AppLoginFailures += b.AppLoginFailures
	a.AppDesiredWrites += b.AppDesiredWrites
	a.AppReceivedAcks += b.AppReceivedAcks
	a.TotalBytesSent += b.TotalBytesSent
	a.TotalBytesReceived += b.TotalBytesReceived
	a.AuthViolations += b.AuthViolations
	a.HTTPRequests += b.HTTPRequests
	a.HTTPSuccesses += b.HTTPSuccesses
	a.HTTPFailures += b.HTTPFailures
	a.TotalHTTPBytesSent += b.TotalHTTPBytesSent
	a.TotalHTTPBytesReceived += b.TotalHTTPBytesReceived
	if len(b.FailureReasons) > 0 {
		if a.FailureReasons == nil {
			a.FailureReasons = map[string]int64{}
		}
		for reason, count := range b.FailureReasons {
			a.FailureReasons[reason] += count
		}
	}
	if len(b.FailureDetails) > 0 {
		if a.FailureDetails == nil {
			a.FailureDetails = map[string]map[string]int64{}
		}
		for reason, details := range b.FailureDetails {
			if a.FailureDetails[reason] == nil {
				a.FailureDetails[reason] = map[string]int64{}
			}
			for detail, count := range details {
				a.FailureDetails[reason][detail] += count
			}
		}
	}
	return a
}

func prefixedNotes(prefix string, notes []string) []string {
	out := make([]string, 0, len(notes))
	for _, note := range notes {
		out = append(out, prefix+": "+note)
	}
	return out
}

func sustainedStageResultsJSON(stages []sustainedStageResult, appBootstrap appBootstrapStatus) []map[string]any {
	out := make([]map[string]any, 0, len(stages))
	for idx, stage := range stages {
		totals := stage.Totals
		if idx == 0 {
			attachAppBootstrapTotals(&totals, appBootstrap)
		}
		row := map[string]any{
			"name":                      stage.Name,
			"connected_devices":         stage.ConnectedTarget,
			"active_connections":        stage.ActiveConnections,
			"status":                    stage.Status,
			"commands_attempted":        stage.CommandsAttempted,
			"commands_passed":           stage.CommandsPassed,
			"success_rate_percent":      percentInt(stage.CommandsPassed, stage.CommandsAttempted),
			"notes":                     stage.Notes,
			"stage_diagnostics":         stage.Diagnostics,
			"device_mqtt_totals":        map[string]any{},
			"app_user_totals":           map[string]any{},
			"failure_reasons":           totals.FailureReasons,
			"failure_details":           totals.FailureDetails,
			"connect_attempts":          totals.ConnectAttempts,
			"connect_successes":         totals.ConnectSuccesses,
			"connect_failures":          totals.ConnectFailures,
			"subscribe_successes":       totals.SubscribeSuccesses,
			"active_subscriptions":      totals.ActiveSubscriptions,
			"publish_successes":         totals.PublishSuccesses,
			"publish_failures":          totals.PublishFailures,
			"messages_received":         totals.MessagesReceived,
			"reported_events":           totals.ReportedEvents,
			"total_bytes_sent":          totals.TotalBytesSent,
			"total_bytes_received":      totals.TotalBytesReceived,
			"http_requests":             totals.HTTPRequests,
			"http_successes":            totals.HTTPSuccesses,
			"http_failures":             totals.HTTPFailures,
			"total_http_bytes_sent":     totals.TotalHTTPBytesSent,
			"total_http_bytes_received": totals.TotalHTTPBytesReceived,
		}
		attachMQTTIOTotals(row, totals)
		out = append(out, row)
	}
	return out
}

func percentInt(numerator int, denominator int) float64 {
	if denominator <= 0 {
		return 0
	}
	return float64(numerator) / float64(denominator) * 100
}

func attachAppBootstrapTotals(totals *mqttIOTotals, appBootstrap appBootstrapStatus) {
	if strings.EqualFold(appBootstrap.Status, "PASS") || strings.EqualFold(appBootstrap.Status, "FAIL") {
		totals.AppLoginAttempts = 1
		if strings.EqualFold(appBootstrap.Status, "PASS") {
			totals.AppLoginSuccesses = 1
		} else {
			totals.AppLoginFailures = 1
		}
	}
}

func recordFailureReason(totals *mqttIOTotals, reason string) {
	if totals == nil || strings.TrimSpace(reason) == "" {
		return
	}
	if totals.FailureReasons == nil {
		totals.FailureReasons = map[string]int64{}
	}
	totals.FailureReasons[reason]++
}

func recordFailure(totals *mqttIOTotals, reason string, err error) {
	recordFailureReason(totals, reason)
	if totals == nil || err == nil {
		return
	}
	detail := normalizeFailureDetail(redactedError(err))
	if detail == "" {
		return
	}
	if totals.FailureDetails == nil {
		totals.FailureDetails = map[string]map[string]int64{}
	}
	if totals.FailureDetails[reason] == nil {
		totals.FailureDetails[reason] = map[string]int64{}
	}
	totals.FailureDetails[reason][detail]++
}

func recordSyntheticFailure(totals *mqttIOTotals, reason string, detail string) {
	recordFailureReason(totals, reason)
	if totals == nil {
		return
	}
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return
	}
	if totals.FailureDetails == nil {
		totals.FailureDetails = map[string]map[string]int64{}
	}
	if totals.FailureDetails[reason] == nil {
		totals.FailureDetails[reason] = map[string]int64{}
	}
	totals.FailureDetails[reason][detail]++
}

func normalizeFailureDetail(detail string) string {
	detail = strings.TrimSpace(detail)
	lower := strings.ToLower(detail)
	switch {
	case detail == "":
		return ""
	case strings.Contains(lower, "mqtt connack read:"):
		return "mqtt connack read failed"
	case strings.Contains(lower, "mqtt connect write:"):
		return "mqtt connect write failed"
	case strings.Contains(lower, "mqtt tls dial:"):
		if strings.Contains(lower, "i/o timeout") {
			return "mqtt tls dial timeout"
		}
		return "mqtt tls dial failed"
	case strings.Contains(lower, "mqtt dial:"):
		return "mqtt dial failed"
	case strings.Contains(lower, "device request_token:"):
		if strings.Contains(lower, "context deadline exceeded") {
			return "device request_token context deadline exceeded"
		}
		if strings.Contains(lower, "i/o timeout") {
			return "device request_token i/o timeout"
		}
		return "device request_token failed"
	case strings.Contains(lower, "write: broken pipe"):
		return "mqtt write broken pipe"
	case strings.Contains(lower, "read: connection reset by peer") || strings.Contains(lower, "write: connection reset by peer"):
		return "mqtt connection reset by peer"
	case strings.Contains(lower, "use of closed network connection"):
		return "mqtt closed network connection"
	case strings.Contains(lower, "i/o timeout"):
		if strings.Contains(lower, "request_token") {
			return "request_token i/o timeout"
		}
		return "network i/o timeout"
	case strings.Contains(lower, "context deadline exceeded"):
		if strings.Contains(lower, "request_token") {
			return "request_token context deadline exceeded"
		}
		return "context deadline exceeded"
	case strings.Contains(lower, "unexpected eof") || lower == "eof":
		if strings.Contains(lower, "request_token") {
			return "request_token EOF"
		}
		return "network EOF"
	default:
		return detail
	}
}

func traceDataSummary(doc map[string]any) string {
	parts := []string{}
	for _, key := range []string{"direction", "sample_type", "message_type", "message_id", "command_id", "device_id", "capability"} {
		value := strings.TrimSpace(fmt.Sprint(doc[key]))
		if value == "" || value == "<nil>" {
			continue
		}
		parts = append(parts, key+"="+value)
	}
	if payload, ok := doc["payload"].(map[string]any); ok {
		for _, key := range []string{"action", "status"} {
			value := strings.TrimSpace(fmt.Sprint(payload[key]))
			if value == "" || value == "<nil>" {
				continue
			}
			parts = append(parts, "payload."+key+"="+value)
		}
		if state, ok := payload["state"].(map[string]any); ok {
			for _, section := range []string{"desired", "reported"} {
				values, ok := state[section].(map[string]any)
				if !ok {
					continue
				}
				for _, key := range []string{"power", "mode", "target_temperature_c", "fan", "reading", "telemetry_report_requested"} {
					value := strings.TrimSpace(fmt.Sprint(values[key]))
					if value == "" || value == "<nil>" {
						continue
					}
					parts = append(parts, section+"."+key+"="+value)
				}
			}
		}
	}
	if state, ok := doc["state"].(map[string]any); ok {
		for _, section := range []string{"desired", "reported", "delta"} {
			values, ok := state[section].(map[string]any)
			if !ok {
				if section == "delta" {
					values = state
				} else {
					continue
				}
			}
			for _, key := range []string{"power", "mode", "target_temperature_c", "fan", "reading", "telemetry_report_requested"} {
				value := strings.TrimSpace(fmt.Sprint(values[key]))
				if value == "" || value == "<nil>" {
					continue
				}
				parts = append(parts, section+"."+key+"="+value)
			}
			if section == "delta" {
				break
			}
		}
	}
	return strings.Join(parts, " ")
}

func shadowDocumentsDeltaCleared(doc map[string]any) bool {
	current, _ := doc["current"].(map[string]any)
	state, _ := current["state"].(map[string]any)
	delta, ok := state["delta"].(map[string]any)
	return ok && len(delta) == 0
}

func traceDataSummaryFromPayload(payload []byte, direction string) string {
	doc := map[string]any{}
	if err := json.Unmarshal(payload, &doc); err != nil {
		return ""
	}
	doc["direction"] = direction
	return traceDataSummary(doc)
}

func traceDetail(detail string) string {
	if detail == "" {
		return ""
	}
	return redactedErrorString(detail)
}

func requestDeviceToken(apiBaseURL string, cert tls.Certificate, deviceID string) (string, error) {
	apiBaseURL = strings.TrimRight(strings.TrimSpace(apiBaseURL), "/")
	if apiBaseURL == "" || strings.Contains(apiBaseURL, "unknown") {
		return "", errors.New("missing video cloud API base URL for mTLS token bootstrap")
	}
	raw, err := json.Marshal(map[string]string{"scope": "device", "devid": deviceID, "service": "mqtt"})
	if err != nil {
		return "", err
	}
	body := bytes.NewBuffer(raw)
	req, err := http.NewRequest(http.MethodPost, apiBaseURL+"/request_token", body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if isHTTPBaseURL(apiBaseURL) {
		if err := setTrustedClientCertHeaders(req, cert); err != nil {
			return "", err
		}
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}, InsecureSkipVerify: true},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("request_token failed with HTTP %d", resp.StatusCode)
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(payload, &token); err != nil {
		return "", err
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		return "", errors.New("request_token response missing access_token")
	}
	return token.AccessToken, nil
}

func runAppCertificateBootstrap(accountBaseURL, videoBaseURL, tenantSlug string, user userCredential, deviceID string) appBootstrapStatus {
	return prepareAppCertificateBootstrap(accountBaseURL, videoBaseURL, tenantSlug, user, deviceID).Status
}

func prepareAppCertificateBootstrapForAssignments(accountBaseURL, videoBaseURL, tenantSlug string, usersByEmail map[string]userCredential, assignments []assignment, maxCandidates int) appBootstrapMaterial {
	candidates := appBootstrapCandidates(assignments, maxCandidates)
	if len(candidates) == 0 {
		return appBootstrapMaterial{Status: appBootstrapStatus{Status: "BLOCKED", Reason: "no app bootstrap candidates"}}
	}
	attempts := make([]appBootstrapAttempt, 0, len(candidates))
	var last appBootstrapMaterial
	for _, candidate := range candidates {
		user, ok := usersByEmail[candidate.AssignedEmail]
		if !ok {
			attempts = append(attempts, appBootstrapAttempt{
				UserEmail: candidate.AssignedEmail,
				DeviceID:  candidate.DeviceID,
				Status:    "BLOCKED",
				Reason:    "selected assignment user missing from users artifact",
			})
			continue
		}
		material := prepareAppCertificateBootstrap(accountBaseURL, videoBaseURL, tenantSlug, user, candidate.DeviceID)
		material.Status.Attempts = append([]appBootstrapAttempt{}, attempts...)
		material.Status.Attempts = append(material.Status.Attempts, appBootstrapAttempt{
			UserEmail: user.Email,
			DeviceID:  candidate.DeviceID,
			Status:    material.Status.Status,
			Reason:    material.Status.Reason,
		})
		if material.Status.Status == "PASS" {
			return material
		}
		attempts = material.Status.Attempts
		last = material
	}
	if last.Status.Status == "" {
		return appBootstrapMaterial{Status: appBootstrapStatus{Status: "BLOCKED", Reason: "no valid app bootstrap candidates", Attempts: attempts}}
	}
	last.Status.Attempts = attempts
	last.Status.Reason = "all app bootstrap candidates failed; last: " + strings.TrimSpace(last.Status.Reason)
	return last
}

func appBootstrapCandidates(assignments []assignment, maxCandidates int) []assignment {
	if maxCandidates <= 0 {
		maxCandidates = 1
	}
	candidates := make([]assignment, 0, maxCandidates)
	seen := map[string]bool{}
	for _, item := range assignments {
		key := item.AssignedEmail + "\x00" + item.DeviceID
		if seen[key] {
			continue
		}
		seen[key] = true
		candidates = append(candidates, item)
		if len(candidates) >= maxCandidates {
			break
		}
	}
	return candidates
}

func prepareAppCertificateBootstrap(accountBaseURL, videoBaseURL, tenantSlug string, user userCredential, deviceID string) appBootstrapMaterial {
	status := appBootstrapStatus{Status: "FAIL", UserEmail: user.Email, DeviceID: deviceID}
	material := appBootstrapMaterial{Status: status}
	if strings.TrimSpace(tenantSlug) == "" {
		status.Status = "BLOCKED"
		status.Reason = "users artifact missing tenant_slug"
		material.Status = status
		return material
	}
	if strings.TrimSpace(user.Email) == "" || strings.TrimSpace(user.Password) == "" {
		status.Status = "BLOCKED"
		status.Reason = "selected user is missing login credential"
		material.Status = status
		return material
	}
	first, err := accountLoginAppCertificate(accountBaseURL, tenantSlug, user, "")
	if err != nil {
		status.Reason = redactedError(err)
		material.Status = status
		return material
	}
	if first.User.ID == "" {
		status.Reason = "login response missing user id"
		material.Status = status
		return material
	}
	status.CertificateStatus = first.AppCertificate.Status
	login := first
	var keyPEM []byte
	switch first.AppCertificate.Status {
	case "csr_required":
		csrPEM, generatedKeyPEM, err := generateAppCSR("app-user:" + first.User.ID)
		if err != nil {
			status.Reason = redactedError(err)
			material.Status = status
			return material
		}
		keyPEM = generatedKeyPEM
		login, err = accountLoginAppCertificate(accountBaseURL, tenantSlug, user, csrPEM)
		if err != nil {
			status.Reason = redactedError(err)
			material.Status = status
			return material
		}
		status.CertificateStatus = login.AppCertificate.Status
	case "issued":
		if !hasLocalAppCredentials(user.AppCredentials) {
			status.Status = "BLOCKED"
			status.Reason = "users artifact lacks local app credentials for issued app certificate"
			material.Status = status
			return material
		}
		keyPEM = []byte(strings.TrimSpace(user.AppCredentials.PrivateKeyPEM))
	}
	status.Subject = login.AppCertificate.Subject
	status.FingerprintSHA256 = login.AppCertificate.FingerprintSHA256
	if len(keyPEM) == 0 {
		status.Status = "BLOCKED"
		status.Reason = "existing app certificate returned but simulation has no matching private key"
		material.Status = status
		return material
	}
	certPEMText, certSource := firstCertificatePEM(
		"artifact_cert", user.AppCertificate.CertificatePEM,
		"artifact_chain", user.AppCertificate.CertificateChainPEM,
		"login_cert", login.AppCertificate.CertificatePEM,
		"login_chain", login.AppCertificate.CertificateChainPEM,
	)
	status.CertificateSource = certSource
	if certPEMText == "" {
		status.Reason = "app certificate material missing valid PEM"
		material.Status = status
		return material
	}
	certPEM := []byte(certPEMText)
	appCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		status.Reason = redactedError(err)
		material.Status = status
		return material
	}
	token, err := requestAppToken(videoBaseURL, appCert, deviceID)
	if err != nil {
		status.Reason = redactedError(err)
		material.Status = status
		return material
	}
	if strings.TrimSpace(token.AccessToken) == "" {
		status.Reason = "app request_token response missing access_token"
		material.Status = status
		return material
	}
	status.Status = "PASS"
	status.Reason = ""
	status.TokenScope = token.Scope
	status.AccessToken = token.AccessToken
	material.Status = status
	material.Certificate = appCert
	return material
}

func hasLocalAppCredentials(credentials appCertificateKeys) bool {
	privateKey := strings.TrimSpace(credentials.PrivateKeyPEM)
	csr := strings.TrimSpace(credentials.CSRPem)
	return strings.HasPrefix(privateKey, "-----BEGIN ") &&
		strings.Contains(privateKey, "PRIVATE KEY-----") &&
		strings.HasPrefix(csr, "-----BEGIN CERTIFICATE REQUEST-----")
}

type accountLoginAppResponse struct {
	User struct {
		ID string `json:"id"`
	} `json:"user"`
	AppCertificate struct {
		Status              string `json:"status"`
		Subject             string `json:"subject"`
		CertificatePEM      string `json:"certificate_pem"`
		CertificateChainPEM string `json:"certificate_chain_pem"`
		FingerprintSHA256   string `json:"fingerprint_sha256"`
	} `json:"app_certificate"`
}

func accountLoginAppCertificate(baseURL, tenantSlug string, user userCredential, csrPEM string) (accountLoginAppResponse, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" || strings.Contains(baseURL, "unknown") {
		return accountLoginAppResponse{}, errors.New("missing account manager base URL")
	}
	tenantSlug = strings.TrimSpace(tenantSlug)
	if tenantSlug == "" {
		return accountLoginAppResponse{}, errors.New("missing tenant_slug")
	}
	payload := map[string]string{"email": user.Email, "password": user.Password}
	if strings.TrimSpace(csrPEM) != "" {
		payload["app_csr_pem"] = csrPEM
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return accountLoginAppResponse{}, err
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/brand-clouds/"+url.PathEscape(tenantSlug)+"/auth/login", bytes.NewReader(raw))
	if err != nil {
		return accountLoginAppResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return accountLoginAppResponse{}, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return accountLoginAppResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return accountLoginAppResponse{}, fmt.Errorf("account login status=%d", resp.StatusCode)
	}
	var out accountLoginAppResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return accountLoginAppResponse{}, err
	}
	return out, nil
}

func generateAppCSR(subject string) (string, []byte, error) {
	_, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return "", nil, err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: subject}}, key)
	if err != nil {
		return "", nil, err
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return "", nil, err
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	return string(csrPEM), keyPEM, nil
}

type appTokenResponse struct {
	Scope       string `json:"scope"`
	AccessToken string `json:"access_token"`
}

func requestAppToken(apiBaseURL string, cert tls.Certificate, deviceID string) (appTokenResponse, error) {
	apiBaseURL = strings.TrimRight(strings.TrimSpace(apiBaseURL), "/")
	if apiBaseURL == "" || strings.Contains(apiBaseURL, "unknown") {
		return appTokenResponse{}, errors.New("missing video cloud API base URL for app token bootstrap")
	}
	raw, err := json.Marshal(map[string]string{"scope": "app", "devid": deviceID})
	if err != nil {
		return appTokenResponse{}, err
	}
	req, err := http.NewRequest(http.MethodPost, apiBaseURL+"/request_token", bytes.NewReader(raw))
	if err != nil {
		return appTokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if isHTTPBaseURL(apiBaseURL) {
		if err := setTrustedClientCertHeaders(req, cert); err != nil {
			return appTokenResponse{}, err
		}
	}
	client := &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}, InsecureSkipVerify: true},
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return appTokenResponse{}, err
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return appTokenResponse{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return appTokenResponse{}, fmt.Errorf("app request_token status=%d", resp.StatusCode)
	}
	var out appTokenResponse
	if err := json.Unmarshal(payload, &out); err != nil {
		return appTokenResponse{}, err
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return appTokenResponse{}, errors.New("app request_token response missing access_token")
	}
	return out, nil
}

func isHTTPBaseURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && strings.EqualFold(parsed.Scheme, "http")
}

func setTrustedClientCertHeaders(req *http.Request, cert tls.Certificate) error {
	if len(cert.Certificate) == 0 {
		return errors.New("missing client certificate for trusted header token request")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return err
	}
	cn := strings.TrimSpace(leaf.Subject.CommonName)
	if cn == "" {
		return errors.New("client certificate missing common name for trusted header token request")
	}
	subject := "/CN=" + cn
	org := "VideoCloud"
	if len(leaf.Subject.Organization) > 0 && strings.TrimSpace(leaf.Subject.Organization[0]) != "" {
		org = strings.TrimSpace(leaf.Subject.Organization[0])
	}
	subject += "/O=" + org
	req.Header.Set("X-Client-Verify", "SUCCESS")
	req.Header.Set("X-Client-S-DN", subject)
	return nil
}

func sampleHomeStatusReport(deviceID, capability, brandname, messageID string, occurredAt time.Time) (string, []byte, error) {
	topic := "devices/" + deviceID + "/up/messages"
	body := map[string]any{
		"sample_type":    "home_device_message",
		"schema_version": 1,
		"message_type":   "status_report",
		"message_id":     messageID,
		"correlation_id": nil,
		"command_id":     nil,
		"device_id":      deviceID,
		"capability":     capability,
		"occurred_at":    occurredAt.UTC().Format(time.RFC3339),
		"payload": map[string]any{
			"brand":       brandname,
			"transport":   "mqtt",
			"status":      "online",
			"probe":       "home-mqtt-loadtest",
			"reported_at": occurredAt.UTC().Format(time.RFC3339),
		},
	}
	payload, err := json.Marshal(body)
	return topic, payload, err
}

func sampleHomeCommand(deviceID, capability, commandID string, occurredAt time.Time) ([]byte, error) {
	body := map[string]any{
		"sample_type":    "home_device_message",
		"schema_version": 1,
		"message_type":   "command",
		"message_id":     "msg-" + commandID,
		"correlation_id": commandID,
		"command_id":     commandID,
		"device_id":      deviceID,
		"capability":     capability,
		"occurred_at":    occurredAt.UTC().Format(time.RFC3339),
		"payload": map[string]any{
			"action":      commandActionForCapability(capability),
			"clientToken": commandID,
			"state": map[string]any{
				"desired": desiredStateForCapability(capability),
			},
		},
	}
	return json.Marshal(body)
}

func sampleHomeCommandResult(deviceID, capability, commandID string, occurredAt time.Time) ([]byte, error) {
	body := map[string]any{
		"sample_type":    "home_device_message",
		"schema_version": 1,
		"message_type":   "command_result",
		"message_id":     "msg-result-" + commandID,
		"correlation_id": commandID,
		"command_id":     commandID,
		"device_id":      deviceID,
		"capability":     capability,
		"occurred_at":    occurredAt.UTC().Format(time.RFC3339),
		"payload": map[string]any{
			"clientToken": commandID,
			"status":      "accepted",
			"state": map[string]any{
				"reported": reportedStateForCapability(capability),
			},
		},
	}
	return json.Marshal(body)
}

func commandActionForCapability(capability string) string {
	switch strings.TrimSpace(strings.ToLower(capability)) {
	case "light", "smart_light":
		return "set_power"
	case "air_conditioner", "ac", "hvac":
		return "set_hvac"
	case "smart_meter", "meter":
		return "read_meter"
	default:
		return "probe_command"
	}
}

func desiredStateForCapability(capability string) map[string]any {
	switch strings.TrimSpace(strings.ToLower(capability)) {
	case "light", "smart_light":
		return map[string]any{"power": true}
	case "air_conditioner", "ac", "hvac":
		return map[string]any{"mode": "cool", "target_temperature_c": 24, "fan": "auto"}
	case "smart_meter", "meter":
		return map[string]any{"reading": "instantaneous"}
	default:
		return map[string]any{"command": "probe"}
	}
}

func reportedStateForCapability(capability string) map[string]any {
	switch strings.TrimSpace(strings.ToLower(capability)) {
	case "smart_meter", "meter":
		return map[string]any{"reading": "instantaneous", "telemetry_report_requested": true}
	default:
		return desiredStateForCapability(capability)
	}
}

func shadowStateWithLoadTestMarker(base map[string]any, runID, commandID string) map[string]any {
	state := make(map[string]any, len(base)+2)
	for key, value := range base {
		state[key] = value
	}
	state["_loadtest_run_id"] = runID
	state["_loadtest_command_id"] = commandID
	return state
}

func mqttConnect(w io.ReadWriter, clientID, username, password string, keepAliveSeconds uint16) error {
	flags := byte(2)
	if username != "" {
		flags |= 0x80
	}
	if password != "" {
		flags |= 0x40
	}
	body := append(mqttString("MQTT"), 4, flags, byte(keepAliveSeconds>>8), byte(keepAliveSeconds))
	body = append(body, mqttString(clientID)...)
	if username != "" {
		body = append(body, mqttString(username)...)
	}
	if password != "" {
		body = append(body, mqttString(password)...)
	}
	if err := mqttWritePacket(w, 0x10, body); err != nil {
		return fmt.Errorf("mqtt connect write: %w", err)
	}
	packetType, response, err := mqttReadPacket(w)
	if err != nil {
		return fmt.Errorf("mqtt connack read: %w", err)
	}
	if packetType != 0x20 || len(response) < 2 || response[1] != 0 {
		return errors.New("mqtt connack failed")
	}
	return nil
}

func mqttSubscribe(w io.ReadWriter, packetID uint16, topic string) error {
	body := []byte{byte(packetID >> 8), byte(packetID)}
	body = append(body, mqttString(topic)...)
	body = append(body, 0)
	if err := mqttWritePacket(w, 0x82, body); err != nil {
		return err
	}
	packetType, response, err := mqttReadPacket(w)
	if err != nil {
		return err
	}
	if packetType != 0x90 || len(response) < 3 || response[2] == 0x80 {
		return errors.New("mqtt suback failed")
	}
	return nil
}

func mqttPublish(w io.ReadWriter, topic string, payload []byte) error {
	body := append(mqttString(topic), payload...)
	return mqttWritePacket(w, 0x30, body)
}

type runtimeLogRecorder struct {
	deviceID string
	streamID string
	seq      int
	now      func() time.Time
}

func newRuntimeLogRecorder(deviceID string, runID string, now func() time.Time) *runtimeLogRecorder {
	if now == nil {
		now = time.Now
	}
	correlationID := probeCorrelationID(runID, now())
	return &runtimeLogRecorder{
		deviceID: deviceID,
		streamID: fmt.Sprintf("mqtt-e2e-%s-%s", correlationID, deviceID),
		now:      now,
	}
}

func (r *runtimeLogRecorder) Record(conn io.ReadWriter, phase, actor, action, topic string, attrs map[string]any) error {
	_, err := r.RecordWithExpectation(conn, phase, actor, action, topic, attrs)
	return err
}

func (r *runtimeLogRecorder) RecordWithExpectation(conn io.ReadWriter, phase, actor, action, topic string, attrs map[string]any) (logExpect, error) {
	if r == nil {
		return logExpect{}, errors.New("missing runtime log recorder")
	}
	r.seq++
	message := fmt.Sprintf("mqtt_e2e %s %s %s", phase, actor, action)
	if attrs == nil {
		attrs = map[string]any{}
	}
	attrs["phase"] = phase
	attrs["actor"] = actor
	attrs["action"] = action
	attrs["topic"] = topic
	at := time.Now().UTC()
	if r.now != nil {
		at = r.now().UTC()
	}
	if err := mqttPublishRuntimeLog(conn, r.deviceID, r.streamID, r.seq, actor, message, attrs, at); err != nil {
		return logExpect{}, err
	}
	return logExpect{Seq: r.seq, Source: actor, Message: message}, nil
}

func mqttPublishRuntimeLog(w io.ReadWriter, deviceID, streamID string, seq int, source, message string, attrs map[string]any, at time.Time) error {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	payload, err := json.Marshal(map[string]any{
		"devid":     deviceID,
		"stream_id": streamID,
		"seq":       seq,
		"ts":        at.UTC().Format(time.RFC3339Nano),
		"level":     "info",
		"source":    source,
		"message":   message,
		"attrs":     attrs,
	})
	if err != nil {
		return err
	}
	return mqttPublish(w, "devices/"+deviceID+"/logs", payload)
}

func mqttWritePacket(w io.Writer, packetType byte, body []byte) error {
	packet := []byte{packetType}
	packet = append(packet, mqttRemainingLength(len(body))...)
	packet = append(packet, body...)
	_, err := w.Write(packet)
	return err
}

func mqttReadPacket(r io.Reader) (byte, []byte, error) {
	first := []byte{0}
	if _, err := io.ReadFull(r, first); err != nil {
		return 0, nil, err
	}
	multiplier := 1
	remaining := 0
	for {
		digit := []byte{0}
		if _, err := io.ReadFull(r, digit); err != nil {
			return 0, nil, err
		}
		remaining += int(digit[0]&127) * multiplier
		if digit[0]&128 == 0 {
			break
		}
		multiplier *= 128
		if multiplier > 128*128*128 {
			return 0, nil, errors.New("malformed mqtt remaining length")
		}
	}
	body := make([]byte, remaining)
	_, err := io.ReadFull(r, body)
	return first[0], body, err
}

func mqttDecodePublish(flags byte, body []byte) (string, []byte, error) {
	if len(body) < 2 {
		return "", nil, errors.New("publish body too short")
	}
	topicLen := int(binary.BigEndian.Uint16(body[:2]))
	topicEnd := 2 + topicLen
	if len(body) < topicEnd {
		return "", nil, errors.New("publish topic truncated")
	}
	pos := topicEnd
	qos := (flags >> 1) & 0x03
	if qos > 0 {
		pos += 2
	}
	return string(body[2:topicEnd]), body[pos:], nil
}

func mqttString(value string) []byte {
	raw := []byte(value)
	out := []byte{byte(len(raw) >> 8), byte(len(raw))}
	return append(out, raw...)
}

func mqttRemainingLength(length int) []byte {
	out := []byte{}
	for {
		digit := byte(length % 128)
		length /= 128
		if length > 0 {
			digit |= 0x80
		}
		out = append(out, digit)
		if length == 0 {
			return out
		}
	}
}

func writeOutputs(outDir string, result map[string]any) error {
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return err
	}
	resultsFile := filepath.Join(outDir, "results.json")
	reportFile := filepath.Join(outDir, "TEST_REPORT.md")
	result["results_file"] = resultsFile
	result["report_file"] = reportFile
	report := renderReport(result)
	payload, _ := json.MarshalIndent(result, "", "  ")
	if err := os.WriteFile(resultsFile, append(payload, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(reportFile, []byte(report), 0o644); err != nil {
		return err
	}
	if err := emitCentralLoggerEvent(result["env"].(map[string]string)["root"], result); err != nil {
		fmt.Fprintf(os.Stderr, "[home-mqtt-loadtest] central logger emit skipped: %s\n", redactedError(err))
	}
	fmt.Fprint(os.Stderr, renderConsole(result))
	summary, _ := json.Marshal(map[string]any{"action": "home-mqtt-loadtest", "overall": result["overall"], "status": result["status"], "results_file": resultsFile, "report_file": reportFile})
	fmt.Println(string(summary))
	if result["overall"] == "pass" {
		return nil
	}
	os.Exit(1)
	return nil
}

func emitCentralLoggerEvent(envRoot string, result map[string]any) error {
	loggerEnvPath := filepath.Join(envRoot, "services", "cloud-logger", "logger.env")
	if !readable(loggerEnvPath) {
		return nil
	}
	values := envValues(loggerEnvPath)
	endpoint := loggerIngestURL(values["CLOUD_LOGGER_ENDPOINT"])
	token := values["CLOUD_LOGGER_INGEST_TOKEN"]
	if endpoint == "" || token == "" {
		return nil
	}

	generatedAt := asString(result["generated_at"])
	ts, err := time.Parse(time.RFC3339, generatedAt)
	if err != nil {
		ts = time.Now().UTC()
	}
	brandname := asString(result["brandname"])
	overall := asString(result["overall"])
	status := asString(result["status"])
	eventID := mqttLoggerEventID(generatedAt, brandname, asString(result["results_file"]))
	fields := map[string]any{
		"brandname":        brandname,
		"profile":          result["profile"],
		"duration_seconds": result["duration_seconds"],
		"status":           status,
		"overall":          overall,
		"metrics":          result["metrics"],
		"mqtt":             result["mqtt"],
		"results_file":     result["results_file"],
		"report_file":      result["report_file"],
	}
	request := map[string]any{
		"events": []map[string]any{{
			"event_id":     eventID,
			"ts":           ts.UTC().Format(time.RFC3339Nano),
			"level":        loggerLevel(overall),
			"msg":          "home mqtt loadtest " + overall,
			"service":      "workspace-mqtt-test",
			"env":          envNameFromRoot(envRoot),
			"version":      "workspace",
			"host":         "operator",
			"unit":         "stg.sh mqtt",
			"source":       "workspace",
			"trace_id":     eventID,
			"request_id":   eventID,
			"operation_id": "home-mqtt-loadtest",
			"component":    "cloud-mqtt-test",
			"fields":       fields,
		}},
	}
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	client := http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("logger ingest status=%d", resp.StatusCode)
	}
	return nil
}

func loggerIngestURL(endpoint string) string {
	endpoint = strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if endpoint == "" || strings.HasSuffix(endpoint, "/v1/logs/ingest") {
		return endpoint
	}
	return endpoint + "/v1/logs/ingest"
}

func mqttLoggerEventID(generatedAt, brandname, resultsFile string) string {
	sum := sha256.Sum256([]byte(generatedAt + "\x00" + brandname + "\x00" + resultsFile))
	return "home-mqtt-loadtest-" + hex.EncodeToString(sum[:12])
}

func loggerLevel(overall string) string {
	if overall == "pass" {
		return "info"
	}
	return "warn"
}

func envNameFromRoot(envRoot string) string {
	envName := filepath.Base(filepath.Dir(envRoot))
	if envName == "." || envName == string(filepath.Separator) || envName == "" {
		return "staging"
	}
	return envName
}

func renderConsole(result map[string]any) string {
	lines := []string{
		"Home MQTT Load-Test Report",
		"==========================",
		fmt.Sprintf("Status: %s | Overall: %s", result["status"], result["overall"]),
		fmt.Sprintf("Brand: %s | Profile: %s | Duration: %vs", result["brandname"], result["profile"], result["duration_seconds"]),
		fmt.Sprintf("Env: %s", result["env"].(map[string]string)["root"]),
		"",
		"Artifacts:",
		fmt.Sprintf("  results: %s", result["results_file"]),
		fmt.Sprintf("  report:  %s", result["report_file"]),
		"",
	}
	if result["overall"] == "blocked" {
		lines = append(lines, "Blockers:")
		for _, blocker := range asStringSlice(result["blockers"]) {
			lines = append(lines, "  - "+blocker)
		}
		lines = append(lines, "")
		return strings.Join(lines, "\n") + "\n"
	}
	traceDetail := asString(result["trace_detail"])
	if traceDetail == "" {
		traceDetail = "summary"
	}
	if traceDetail != "none" {
		if devices, ok := result["devices"].([]deviceResult); ok && len(devices) > 0 {
			lines = append(lines, "Runtime MQTT Trace:")
			for _, row := range devices {
				for _, step := range row.TraceChain {
					if !consoleTraceStepVisible(step, traceDetail) {
						continue
					}
					topic := step.Topic
					if topic == "" {
						topic = "-"
					}
					data := step.Data
					if data == "" {
						data = step.Detail
					}
					if data == "" {
						data = "-"
					}
					lines = append(lines, fmt.Sprintf("  [%s] %s step=%02d %s %s topic=%s status=%s data=%s",
						step.Timestamp,
						row.DeviceID,
						step.Step,
						step.Actor,
						step.Action,
						topic,
						step.Status,
						data,
					))
				}
			}
			lines = append(lines, "")
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

func consoleTraceStepVisible(step traceStep, detail string) bool {
	if detail == "full" {
		return true
	}
	return step.Action == "publish" || step.Action == "receive" || step.Status == "FAIL"
}

func renderReport(result map[string]any) string {
	lines := []string{
		"# Home MQTT Load-Test Report",
		"",
		fmt.Sprintf("- Status: %s", result["status"]),
		fmt.Sprintf("- Overall: %s", result["overall"]),
		fmt.Sprintf("- Generated: %s", result["generated_at"]),
		fmt.Sprintf("- Env root: `%s`", result["env"].(map[string]string)["root"]),
		fmt.Sprintf("- Brand: `%s`", result["brandname"]),
		fmt.Sprintf("- Profile: `%s`", result["profile"]),
		fmt.Sprintf("- Duration seconds: %v", result["duration_seconds"]),
		fmt.Sprintf("- Seed: %v", result["seed"]),
		"",
	}
	if result["overall"] == "blocked" {
		lines = append(lines, "## Blockers", "")
		for _, blocker := range asStringSlice(result["blockers"]) {
			lines = append(lines, "- "+blocker)
		}
		lines = append(lines, "")
		return strings.Join(lines, "\n") + "\n"
	}
	if mqtt, ok := result["mqtt"].(map[string]any); ok {
		lines = append(lines,
			"## MQTT Actor-Separated E2E",
			"",
			fmt.Sprintf("- Probe model: `%s`", asString(mqtt["probe_model"])),
			fmt.Sprintf("- Client identity mode: `%s`", asString(mqtt["client_identity_mode"])),
			fmt.Sprintf("- Telemetry receiver: `%s`", asString(mqtt["telemetry_receiver"])),
			fmt.Sprintf("- Command receiver: `%s`", asString(mqtt["command_receiver"])),
			"",
		)
	}
	if devices, ok := result["devices"].([]deviceResult); ok && len(devices) > 0 {
		lines = append(lines,
			"## Per Device MQTT E2E",
			"",
			"| Device | Type | Telemetry | Command | Up topic | Down topic |",
			"| --- | --- | --- | --- | --- | --- |",
		)
		for _, row := range devices {
			lines = append(lines, fmt.Sprintf("| `%s` | `%s` | `%s -> %s: %s` | `%s -> %s: %s` | `%s` | `%s` |",
				row.DeviceID,
				row.DeviceType,
				row.TelemetryPublishActor,
				row.TelemetrySubscribeActor,
				row.TelemetryStatus,
				row.CommandPublishActor,
				row.CommandSubscribeActor,
				row.CommandStatus,
				row.TelemetryTopic,
				row.CommandTopic,
			))
		}
		lines = append(lines, "")
		lines = append(lines,
			"## MQTT E2E Trace Chain",
			"",
			"| Device | Step | Timestamp | Phase | Actor | Action | Topic | Status | Data | Detail |",
			"| --- | ---: | --- | --- | --- | --- | --- | --- | --- | --- |",
		)
		for _, row := range devices {
			for _, step := range row.TraceChain {
				lines = append(lines, fmt.Sprintf("| `%s` | %d | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` | `%s` |",
					row.DeviceID,
					step.Step,
					step.Timestamp,
					step.Phase,
					step.Actor,
					step.Action,
					step.Topic,
					step.Status,
					step.Data,
					step.Detail,
				))
			}
		}
		lines = append(lines, "")
	}
	return strings.Join(lines, "\n") + "\n"
}

func firstExisting(paths ...string) string {
	for _, path := range paths {
		if _, err := os.Stat(path); err == nil {
			return path
		}
	}
	if len(paths) == 0 {
		return ""
	}
	return paths[0]
}

func readable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func latest(pattern string) string {
	matches, _ := filepath.Glob(pattern)
	sortArtifactPathsNewestFirst(matches)
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func videoStatePath(envRoot, stackEnv string) string {
	stackValues := envValues(stackEnv)
	candidates := []string{}
	if stack := strings.TrimSpace(stackValues["CLOUD_STACK_NAME"]); stack != "" {
		candidates = append(candidates, filepath.Join(envRoot, "state", stack+".state.json"))
	}
	candidates = append(candidates,
		filepath.Join(envRoot, "state", "video-cloud.state.json"),
		filepath.Join(envRoot, "state", "video-cloud-staging.state.json"),
	)
	return firstExisting(candidates...)
}

func videoCloudMTLSBaseURL(envRoot string, stackValues map[string]string, fallback string) string {
	host := firstNonEmpty(
		stackValues["VIDEO_CLOUD_MTLS_DOMAIN"],
		stackValues["VIDEO_CLOUD_DEVICE_CLIENT_DOMAIN"],
		stackValues["VIDEO_CLOUD_DEVICE_DOMAIN"],
		topologyDeployValue(firstExisting(
			filepath.Join(envRoot, "topology", "video-cloud.yaml"),
			filepath.Join(envRoot, "topology", "video-cloud-staging.yaml"),
		), "device_client_domain"),
	)
	if host == "" {
		if publicHost := strings.TrimSpace(stackValues["VIDEO_CLOUD_DOMAIN"]); publicHost != "" {
			host = "device." + publicHost
		}
	}
	if host == "" {
		return fallback
	}
	return "https://" + strings.TrimRight(strings.TrimSpace(host), "/")
}

type videoCloudEndpointSet struct {
	PublicBaseURL         string
	MTLSBaseURL           string
	TokenBootstrapBaseURL string
	TokenBootstrapSource  string
}

func resolveVideoCloudEndpoints(envRoot string, stackValues map[string]string) videoCloudEndpointSet {
	defaultPublicBaseURL := trimBaseURL(firstNonEmpty(
		stackValues["VIDEO_CLOUD_PUBLIC_BASE_URL"],
		stackValues["VIDEO_CLOUD_BASE_URL"],
		"https://"+firstNonEmpty(stackValues["VIDEO_CLOUD_DOMAIN"], "unknown"),
	))
	publicBaseURL := trimBaseURL(firstNonEmpty(
		os.Getenv("VIDEO_CLOUD_PUBLIC_BASE_URL"),
		os.Getenv("VIDEO_CLOUD_BASE_URL"),
		defaultPublicBaseURL,
	))
	defaultMTLSBaseURL := videoCloudMTLSBaseURL(envRoot, stackValues, defaultPublicBaseURL)
	mtlsBaseURL := trimBaseURL(firstNonEmpty(
		os.Getenv("VIDEO_CLOUD_MTLS_BASE_URL"),
		os.Getenv("VIDEO_CLOUD_DEVICE_CLIENT_BASE_URL"),
		stackValues["VIDEO_CLOUD_MTLS_BASE_URL"],
		stackValues["VIDEO_CLOUD_DEVICE_CLIENT_BASE_URL"],
		defaultMTLSBaseURL,
	))
	tokenBaseURL := trimBaseURL(firstNonEmpty(
		os.Getenv("VIDEO_CLOUD_TOKEN_BASE_URL"),
		stackValues["VIDEO_CLOUD_TOKEN_BASE_URL"],
	))
	tokenSource := "VIDEO_CLOUD_MTLS_BASE_URL"
	if strings.TrimSpace(os.Getenv("VIDEO_CLOUD_TOKEN_BASE_URL")) != "" {
		tokenSource = "VIDEO_CLOUD_TOKEN_BASE_URL"
	} else if strings.TrimSpace(stackValues["VIDEO_CLOUD_TOKEN_BASE_URL"]) != "" {
		tokenSource = "env_root:VIDEO_CLOUD_TOKEN_BASE_URL"
	} else {
		tokenBaseURL = mtlsBaseURL
	}
	return videoCloudEndpointSet{
		PublicBaseURL:         publicBaseURL,
		MTLSBaseURL:           mtlsBaseURL,
		TokenBootstrapBaseURL: tokenBaseURL,
		TokenBootstrapSource:  tokenSource,
	}
}

func trimBaseURL(raw string) string {
	return strings.TrimRight(strings.TrimSpace(raw), "/")
}

func topologyDeployValue(path, key string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	inDeploy := false
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if !strings.HasPrefix(line, " ") && strings.HasSuffix(trimmed, ":") {
			inDeploy = trimmed == "deploy:"
			continue
		}
		if !inDeploy {
			continue
		}
		name, value, ok := strings.Cut(trimmed, ":")
		if !ok || strings.TrimSpace(name) != key {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"'`)
	}
	return ""
}

func latestHomeMQTTBindArtifact(pattern, brandLower string) string {
	matches, _ := filepath.Glob(pattern)
	sortArtifactPathsNewestFirst(matches)
	for _, path := range matches {
		bind := bindArtifact{}
		if err := readJSON(path, &bind); err != nil {
			continue
		}
		if strings.ToLower(bind.Brandname) != brandLower {
			continue
		}
		found := map[string]bool{}
		for _, item := range bind.Assignments {
			if homeTypes[item.DeviceType] && contains(item.ServiceOptions, "mqtt") {
				found[item.DeviceType] = true
			}
		}
		if found["light"] && found["air_conditioner"] && found["smart_meter"] {
			return path
		}
	}
	if len(matches) == 0 {
		return ""
	}
	return matches[0]
}

func sortArtifactPathsNewestFirst(paths []string) {
	sort.Slice(paths, func(i, j int) bool {
		ti := artifactFilenameTimestamp(paths[i])
		tj := artifactFilenameTimestamp(paths[j])
		if ti != "" || tj != "" {
			if ti != tj {
				return ti > tj
			}
			return filepath.Base(paths[i]) > filepath.Base(paths[j])
		}
		ai, _ := os.Stat(paths[i])
		aj, _ := os.Stat(paths[j])
		if ai == nil || aj == nil {
			return paths[i] < paths[j]
		}
		if !ai.ModTime().Equal(aj.ModTime()) {
			return ai.ModTime().After(aj.ModTime())
		}
		return filepath.Base(paths[i]) > filepath.Base(paths[j])
	})
}

func artifactFilenameTimestamp(path string) string {
	base := filepath.Base(path)
	for i := 0; i+16 <= len(base); i++ {
		candidate := base[i : i+16]
		if candidate[8] != 'T' || candidate[15] != 'Z' {
			continue
		}
		ok := true
		for idx, ch := range candidate {
			if idx == 8 || idx == 15 {
				continue
			}
			if ch < '0' || ch > '9' {
				ok = false
				break
			}
		}
		if ok {
			return candidate
		}
	}
	return ""
}

func readJSON(path string, dest any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, dest)
}

func envValues(path string) map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") || !strings.Contains(line, "=") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		out[parts[0]] = strings.Trim(strings.TrimSpace(parts[1]), `"'`)
	}
	return out
}

func envKeys(path string) []string {
	values := envValues(path)
	return sortedKeysString(values)
}

func mqttEndpoint(videoState string, loadValues map[string]string) (string, int) {
	if host := strings.TrimSpace(os.Getenv("RTK_CLOUD_MQTT_TEST_MQTT_HOST")); host != "" {
		return host, envIntDefault("RTK_CLOUD_MQTT_TEST_MQTT_PORT", 8883)
	}
	host := firstNonEmpty(loadValues["MQTT_HOST"], "unknown")
	portRaw := firstNonEmpty(loadValues["MQTT_TLS_PORT"], loadValues["MQTT_PORT"], "8883")
	if host == "unknown" {
		state := map[string]any{}
		if err := readJSON(videoState, &state); err == nil {
			if instances, ok := state["instances"].(map[string]any); ok {
				if mqtt, ok := instances["mqtt"].(map[string]any); ok {
					host = firstNonEmpty(asString(mqtt["public_ipv4"]), asString(mqtt["private_ip"]), "unknown")
				}
			}
		}
	}
	port, _ := strconv.Atoi(portRaw)
	return host, port
}

func envIntDefault(key string, fallback int) int {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			return parsed
		}
	}
	return fallback
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func firstCertificatePEM(labelValuePairs ...string) (string, string) {
	for i := 0; i+1 < len(labelValuePairs); i += 2 {
		label := labelValuePairs[i]
		value := labelValuePairs[i+1]
		trimmed := strings.TrimSpace(value)
		if strings.Contains(trimmed, "-----BEGIN CERTIFICATE-----") {
			return trimmed, label
		}
	}
	return "", ""
}

func valueOr(value, fallback string) string {
	if value == "" {
		return fallback
	}
	return value
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func sortedKeys[T any](m map[string]T) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func sortedKeysString(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func redactedError(err error) string {
	return redactedErrorString(err.Error())
}

func redactedErrorString(message string) string {
	lower := strings.ToLower(message)
	if strings.Contains(lower, "request_token") &&
		(strings.Contains(lower, "http ") ||
			strings.Contains(lower, "status=") ||
			strings.Contains(lower, "failed with") ||
			strings.Contains(lower, "missing access_token") ||
			strings.Contains(lower, "i/o timeout") ||
			strings.Contains(lower, "context deadline exceeded") ||
			strings.Contains(lower, "connection refused") ||
			strings.Contains(lower, "connection reset by peer") ||
			strings.Contains(lower, "unexpected eof") ||
			strings.HasSuffix(lower, "eof")) {
		return message
	}
	for _, word := range []string{"password", "token", "secret", "private", "bearer", "device.key", "-----begin"} {
		if strings.Contains(lower, word) {
			return "redacted sensitive error"
		}
	}
	return message
}

func cloneMap(in map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range in {
		out[key] = value
	}
	return out
}

func userSummaries(users []string, selected map[string][]assignment) []map[string]any {
	out := []map[string]any{}
	for _, email := range users {
		out = append(out, map[string]any{"email": email, "assigned_devices": len(selected[email])})
	}
	return out
}

func mtlsSummaries(records []certRecord) []map[string]any {
	out := []map[string]any{}
	for _, record := range records {
		out = append(out, map[string]any{"device_id": record.DeviceID, "device_type": record.DeviceType, "cert": "present", "key": "present", "chain": "present"})
	}
	return out
}

func findCert(records []certRecord, deviceID string) certRecord {
	for _, record := range records {
		if record.DeviceID == deviceID {
			return record
		}
	}
	return certRecord{DeviceID: deviceID}
}

func percentile(values []float64, pct float64) float64 {
	if len(values) == 0 {
		return 0
	}
	sort.Float64s(values)
	rank := (float64(len(values)) - 1) * pct / 100.0
	low := int(math.Floor(rank))
	high := int(math.Min(float64(low+1), float64(len(values)-1)))
	if low == high {
		return values[low]
	}
	return values[low] + (values[high]-values[low])*(rank-float64(low))
}

func maxLatency(rows []deviceResult, kind string) float64 {
	max := 0.0
	for _, row := range rows {
		if row.DeviceType == kind && len(row.LatencyMS) > 0 && row.LatencyMS[0] > max {
			max = row.LatencyMS[0]
		}
	}
	return max
}

func nowISO() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func splitHostPortInt(value string) (string, int, bool) {
	host, portRaw, err := net.SplitHostPort(value)
	if err != nil && strings.Count(value, ":") == 1 {
		parts := strings.SplitN(value, ":", 2)
		host, portRaw, err = parts[0], parts[1], nil
	}
	if err != nil {
		return "", 0, false
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil || port <= 0 {
		return "", 0, false
	}
	return host, port, true
}

func asString(value any) string {
	if value == nil {
		return ""
	}
	return fmt.Sprint(value)
}

func asStringSlice(value any) []string {
	switch typed := value.(type) {
	case []string:
		return typed
	case []any:
		out := make([]string, 0, len(typed))
		for _, item := range typed {
			out = append(out, fmt.Sprint(item))
		}
		return out
	default:
		return nil
	}
}
