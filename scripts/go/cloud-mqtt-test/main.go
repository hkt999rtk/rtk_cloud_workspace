package main

import (
	"crypto/tls"
	"encoding/binary"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"net"
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
	Brandname string `json:"brandname"`
	Users     []struct {
		Email string `json:"email"`
	} `json:"users"`
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
	Error          string    `json:"error,omitempty"`
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
	videoState := firstExisting(
		filepath.Join(envRoot, "state", "video-cloud.state.json"),
		filepath.Join(envRoot, "state", "video-cloud-staging.state.json"),
	)

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
	bindPath := latest(filepath.Join(artifactsDir, "device-bind", brandLower+"-device-bind-*.json"))
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
	for _, u := range users.Users {
		if u.Email != "" {
			userEmails[u.Email] = true
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
			outcome := runDeviceShadow(record, brandname, mqttHost, mqttPort)
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
	result["mqtt"] = map[string]any{"probe_result": mqttProbeResult, "client_identities_checked": len(certRecords), "client_identity_mode": "device_id"}
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

func runDeviceShadow(record certRecord, brandname, host string, port int) deviceResult {
	start := time.Now()
	result := deviceResult{DeviceID: record.DeviceID, DeviceType: record.DeviceType, Commands: 1, SuccessPercent: 0, MQTTStatus: "FAIL", LatencyMS: []float64{0}}
	cert, err := tls.LoadX509KeyPair(record.CertPath, record.KeyPath)
	if err != nil {
		result.Error = redactedError(err)
		return result
	}
	conn, err := tls.DialWithDialer(&net.Dialer{Timeout: 10 * time.Second}, "tcp", net.JoinHostPort(host, strconv.Itoa(port)), &tls.Config{Certificates: []tls.Certificate{cert}, InsecureSkipVerify: true})
	if err != nil {
		result.Error = redactedError(err)
		return result
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	clientID := fmt.Sprintf("rtk-e2e-%s-%d", record.DeviceID, os.Getpid())
	if err := mqttConnect(conn, clientID); err != nil {
		result.Error = redactedError(err)
		return result
	}
	token := fmt.Sprintf("mqtt-e2e-%d-%s", time.Now().Unix(), record.DeviceID)
	base := "$vc/devices/" + record.DeviceID + "/shadow/update"
	accepted := base + "/accepted"
	documents := base + "/documents"
	rejected := base + "/rejected"
	if err := mqttSubscribe(conn, 1, accepted); err != nil {
		result.Error = redactedError(err)
		return result
	}
	if err := mqttSubscribe(conn, 2, documents); err != nil {
		result.Error = redactedError(err)
		return result
	}
	if err := mqttSubscribe(conn, 3, rejected); err != nil {
		result.Error = redactedError(err)
		return result
	}
	payload, _ := json.Marshal(map[string]any{"state": map[string]any{"reported": map[string]any{"e2e_probe": map[string]any{"brand": brandname, "device_type": record.DeviceType, "timestamp": nowISO()}}}, "clientToken": token})
	if err := mqttPublish(conn, base, payload); err != nil {
		result.Error = redactedError(err)
		return result
	}
	seenAccepted := false
	seenDocuments := false
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
		if err != nil || (topic != accepted && topic != documents && topic != rejected) {
			continue
		}
		doc := map[string]any{}
		_ = json.Unmarshal(message, &doc)
		if doc["clientToken"] != token {
			continue
		}
		if topic == rejected {
			result.Error = "shadow rejected"
			result.LatencyMS = []float64{float64(time.Since(start).Milliseconds())}
			return result
		}
		if topic == accepted {
			seenAccepted = true
		}
		if topic == documents {
			seenDocuments = true
		}
		if seenAccepted && seenDocuments {
			result.MQTTStatus = "PASS"
			result.SuccessPercent = 100
			result.LatencyMS = []float64{float64(time.Since(start).Milliseconds())}
			return result
		}
	}
	result.Error = "timed out waiting for shadow accepted/documents"
	result.LatencyMS = []float64{float64(time.Since(start).Milliseconds())}
	return result
}

func mqttConnect(w io.ReadWriter, clientID string) error {
	body := append(mqttString("MQTT"), 4, 2, 0, 30)
	body = append(body, mqttString(clientID)...)
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
	fmt.Fprint(os.Stderr, renderConsole(result))
	summary, _ := json.Marshal(map[string]any{"action": "home-mqtt-loadtest", "overall": result["overall"], "status": result["status"], "results_file": resultsFile, "report_file": reportFile})
	fmt.Println(string(summary))
	if result["overall"] == "pass" {
		return nil
	}
	os.Exit(1)
	return nil
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
