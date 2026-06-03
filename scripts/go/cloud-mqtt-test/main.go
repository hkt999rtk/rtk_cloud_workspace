package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

var homeTypes = map[string]bool{
	"light":           true,
	"air_conditioner": true,
	"smart_meter":     true,
}

type userArtifact struct {
	Brandname string           `json:"brandname"`
	Users     []userCredential `json:"users"`
}

type userCredential struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type bindArtifact struct {
	Brandname   string       `json:"brandname"`
	Assignments []assignment `json:"assignments"`
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
}

type deviceResult struct {
	DeviceID       string    `json:"device_id"`
	DeviceType     string    `json:"device_type"`
	AssignedEmail  string    `json:"assigned_email"`
	Commands       int       `json:"commands"`
	SuccessPercent float64   `json:"success_percent"`
	LatencyMS      []float64 `json:"latency_ms"`
	MQTTStatus     string    `json:"mqtt_status"`
	PublishTopic   string    `json:"publish_topic,omitempty"`
	SubscribeTopic string    `json:"subscribe_topic,omitempty"`
	MessageType    string    `json:"message_type,omitempty"`
	PayloadSchema  string    `json:"payload_schema,omitempty"`
	Error          string    `json:"error,omitempty"`
}

type appBootstrapStatus struct {
	Status            string `json:"status"`
	Reason            string `json:"reason,omitempty"`
	UserEmail         string `json:"user_email,omitempty"`
	DeviceID          string `json:"device_id,omitempty"`
	CertificateStatus string `json:"certificate_status,omitempty"`
	Subject           string `json:"subject,omitempty"`
	FingerprintSHA256 string `json:"fingerprint_sha256,omitempty"`
	TokenScope        string `json:"token_scope,omitempty"`
}

func main() {
	var root, envRoot, brandname, outDir, profile, maxUsersRaw, mqttProbeRaw string
	var duration, seed int
	flag.StringVar(&root, "root", "", "workspace root")
	flag.StringVar(&envRoot, "env-root", "", "environment root")
	flag.StringVar(&brandname, "brandname", "", "brand name")
	flag.StringVar(&outDir, "out-dir", "", "output directory")
	flag.StringVar(&profile, "profile", "smoke", "profile")
	flag.IntVar(&duration, "duration-seconds", 120, "duration seconds")
	flag.StringVar(&maxUsersRaw, "max-users", "", "max users")
	flag.IntVar(&seed, "seed", 20260531, "seed")
	flag.StringVar(&mqttProbeRaw, "mqtt-probe", "true", "mqtt probe")
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
	if err := run(root, envRoot, brandname, outDir, profile, duration, maxUsers, seed, mqttProbe); err != nil {
		fatal(err)
	}
}

func fatal(err error) {
	fmt.Fprintf(os.Stderr, "error: %v\n", err)
	os.Exit(2)
}

func run(root, envRoot, brandname, outDir, profile string, duration, maxUsers, seed int, mqttProbe bool) error {
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
	endpoints := map[string]any{
		"account_manager_base_url": "https://" + firstNonEmpty(stackValues["ACCOUNT_MANAGER_DOMAIN"], accountValues["ACCOUNT_MANAGER_LINODE_DOMAIN"], "unknown"),
		"video_cloud_base_url":     "https://" + firstNonEmpty(stackValues["VIDEO_CLOUD_DOMAIN"], "unknown"),
	}
	mqttHost, mqttPort := mqttEndpoint(videoState, loadValues)
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
	certRecords := []certRecord{}
	for _, item := range selectedAssignments {
		record, ok := manifestByID[item.DeviceID]
		if !ok {
			blockers = append(blockers, fmt.Sprintf("device %s missing from manifest", item.DeviceID))
			continue
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
		"env":              map[string]string{"root": envRoot},
		"inputs":           inputs,
		"endpoints":        endpoints,
		"blockers":         blockers,
	}
	if len(blockers) > 0 {
		base["status"] = "BLOCKED"
		base["overall"] = "blocked"
		return writeOutputs(outDir, base)
	}
	appBootstrap := appBootstrapStatus{Status: "BLOCKED", Reason: "no selected assignment"}
	if len(selectedAssignments) > 0 {
		first := selectedAssignments[0]
		appBootstrap = runAppCertificateBootstrap(endpoints["account_manager_base_url"].(string), endpoints["video_cloud_base_url"].(string), usersByEmail[first.AssignedEmail], first.DeviceID)
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
	} else {
		mqttProbeResult = "PASS"
		for _, item := range selectedAssignments {
			row := capCounts[item.DeviceType]
			row["devices"]++
			row["commands"]++
			record := findCert(certRecords, item.DeviceID)
			outcome := runDeviceSampleEnvelope(record, brandname, endpoints["video_cloud_base_url"].(string), mqttHost, mqttPort)
			if outcome.MQTTStatus == "PASS" {
				row["passed"]++
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

	totalCommands := 0
	totalPassed := 0
	for _, row := range perDevice {
		totalCommands += row.Commands
		if row.MQTTStatus == "PASS" {
			totalPassed++
		}
	}
	successRate := 0.0
	if totalCommands > 0 {
		successRate = float64(totalPassed) / float64(totalCommands) * 100.0
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
	result["capability_metrics"] = capMetrics
	result["negative_checks"] = []any{}
	result["mqtt"] = map[string]any{"probe_result": mqttProbeResult, "client_identities_checked": len(certRecords), "client_identity_mode": "device_token", "auth_flow": "device certificate mTLS request_token -> MQTT token credential"}
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

func runDeviceSampleEnvelope(record certRecord, brandname, apiBaseURL, host string, port int) deviceResult {
	start := time.Now()
	result := deviceResult{DeviceID: record.DeviceID, DeviceType: record.DeviceType, Commands: 1, SuccessPercent: 0, MQTTStatus: "FAIL", LatencyMS: []float64{0}}
	cert, err := tls.LoadX509KeyPair(record.CertPath, record.KeyPath)
	if err != nil {
		result.Error = redactedError(err)
		return result
	}
	token, err := requestDeviceToken(apiBaseURL, cert)
	if err != nil {
		result.Error = redactedError(err)
		return result
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", net.JoinHostPort(host, strconv.Itoa(port)), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		result.Error = redactedError(err)
		return result
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	clientID := fmt.Sprintf("rtk-e2e-%s-%d", record.DeviceID, os.Getpid())
	if err := mqttConnect(conn, clientID, record.DeviceID, token); err != nil {
		result.Error = redactedError(err)
		return result
	}
	messageID := fmt.Sprintf("msg-mqtt-e2e-%d-%s", time.Now().Unix(), record.DeviceID)
	topic, payload, err := sampleHomeStatusReport(record.DeviceID, record.DeviceType, brandname, messageID, time.Now().UTC())
	if err != nil {
		result.Error = redactedError(err)
		return result
	}
	result.PublishTopic = topic
	result.SubscribeTopic = topic
	result.MessageType = "status_report"
	result.PayloadSchema = "home_device_message/v1"
	if err := mqttSubscribe(conn, 1, topic); err != nil {
		result.Error = redactedError(err)
		return result
	}
	if err := mqttPublish(conn, topic, payload); err != nil {
		result.Error = redactedError(err)
		return result
	}
	for time.Since(start) < 10*time.Second {
		packetType, body, err := mqttReadPacket(conn)
		if err != nil {
			result.Error = redactedError(err)
			return result
		}
		if packetType>>4 != 3 {
			continue
		}
		topic, message, err := mqttDecodePublish(packetType&0x0f, body)
		if err != nil || topic != result.SubscribeTopic {
			continue
		}
		doc := map[string]any{}
		if err := json.Unmarshal(message, &doc); err != nil {
			continue
		}
		if doc["sample_type"] != "home_device_message" || doc["message_id"] != messageID {
			continue
		}
		result.MQTTStatus = "PASS"
		result.SuccessPercent = 100
		result.LatencyMS = []float64{float64(time.Since(start).Milliseconds())}
		return result
	}
	result.Error = "timed out waiting for sample home-device message loopback"
	result.LatencyMS = []float64{float64(time.Since(start).Milliseconds())}
	return result
}

func requestDeviceToken(apiBaseURL string, cert tls.Certificate) (string, error) {
	apiBaseURL = strings.TrimRight(strings.TrimSpace(apiBaseURL), "/")
	if apiBaseURL == "" || strings.Contains(apiBaseURL, "unknown") {
		return "", errors.New("missing video cloud API base URL for mTLS token bootstrap")
	}
	body := bytes.NewBufferString(`{"scope":"device"}`)
	req, err := http.NewRequest(http.MethodPost, apiBaseURL+"/request_token", body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
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

func runAppCertificateBootstrap(accountBaseURL, videoBaseURL string, user userCredential, deviceID string) appBootstrapStatus {
	status := appBootstrapStatus{Status: "FAIL", UserEmail: user.Email, DeviceID: deviceID}
	if strings.TrimSpace(user.Email) == "" || strings.TrimSpace(user.Password) == "" {
		status.Status = "BLOCKED"
		status.Reason = "selected user is missing login credential"
		return status
	}
	first, err := accountLoginAppCertificate(accountBaseURL, user, "")
	if err != nil {
		status.Reason = redactedError(err)
		return status
	}
	if first.User.ID == "" {
		status.Reason = "login response missing user id"
		return status
	}
	status.CertificateStatus = first.AppCertificate.Status
	login := first
	var keyPEM []byte
	if first.AppCertificate.Status == "csr_required" {
		csrPEM, generatedKeyPEM, err := generateAppCSR("app-user:" + first.User.ID)
		if err != nil {
			status.Reason = redactedError(err)
			return status
		}
		keyPEM = generatedKeyPEM
		login, err = accountLoginAppCertificate(accountBaseURL, user, csrPEM)
		if err != nil {
			status.Reason = redactedError(err)
			return status
		}
		status.CertificateStatus = login.AppCertificate.Status
	}
	status.Subject = login.AppCertificate.Subject
	status.FingerprintSHA256 = login.AppCertificate.FingerprintSHA256
	if login.AppCertificate.CertificatePEM == "" {
		status.Reason = "login response missing app certificate"
		return status
	}
	if len(keyPEM) == 0 {
		status.Status = "BLOCKED"
		status.Reason = "existing app certificate returned but simulation has no matching private key"
		return status
	}
	certPEM := []byte(firstNonEmpty(login.AppCertificate.CertificateChainPEM, login.AppCertificate.CertificatePEM))
	appCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		status.Reason = redactedError(err)
		return status
	}
	token, err := requestAppToken(videoBaseURL, appCert, deviceID)
	if err != nil {
		status.Reason = redactedError(err)
		return status
	}
	status.Status = "PASS"
	status.Reason = ""
	status.TokenScope = token.Scope
	return status
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

func accountLoginAppCertificate(baseURL string, user userCredential, csrPEM string) (accountLoginAppResponse, error) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" || strings.Contains(baseURL, "unknown") {
		return accountLoginAppResponse{}, errors.New("missing account manager base URL")
	}
	payload := map[string]string{"email": user.Email, "password": user.Password}
	if strings.TrimSpace(csrPEM) != "" {
		payload["app_csr_pem"] = csrPEM
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return accountLoginAppResponse{}, err
	}
	req, err := http.NewRequest(http.MethodPost, baseURL+"/v1/auth/login", bytes.NewReader(raw))
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
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", nil, err
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: subject}}, key)
	if err != nil {
		return "", nil, err
	}
	csrPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})
	return string(csrPEM), keyPEM, nil
}

type appTokenResponse struct {
	Scope string `json:"scope"`
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
	return out, nil
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

func mqttConnect(w io.ReadWriter, clientID, username, password string) error {
	flags := byte(2)
	if username != "" {
		flags |= 0x80
	}
	if password != "" {
		flags |= 0x40
	}
	body := append(mqttString("MQTT"), 4, flags, 0, 30)
	body = append(body, mqttString(clientID)...)
	if username != "" {
		body = append(body, mqttString(username)...)
	}
	if password != "" {
		body = append(body, mqttString(password)...)
	}
	if err := mqttWritePacket(w, 0x10, body); err != nil {
		return err
	}
	packetType, response, err := mqttReadPacket(w)
	if err != nil {
		return err
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
	return strings.Join(lines, "\n") + "\n"
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
	sort.Slice(matches, func(i, j int) bool {
		ai, _ := os.Stat(matches[i])
		aj, _ := os.Stat(matches[j])
		return ai.ModTime().After(aj.ModTime())
	})
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

func latestHomeMQTTBindArtifact(pattern, brandLower string) string {
	matches, _ := filepath.Glob(pattern)
	sort.Slice(matches, func(i, j int) bool {
		ai, _ := os.Stat(matches[i])
		aj, _ := os.Stat(matches[j])
		return ai.ModTime().After(aj.ModTime())
	})
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
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
