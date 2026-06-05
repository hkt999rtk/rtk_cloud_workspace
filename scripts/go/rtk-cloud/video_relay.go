package main

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const videoRelayProbeModel = "webrtc_rtp_relay"

type videoRelayUsersArtifact struct {
	Brandname string           `json:"brandname"`
	Users     []videoRelayUser `json:"users"`
}

type videoRelayUser struct {
	Email          string                   `json:"email"`
	AppCredentials videoRelayAppCredentials `json:"app_credentials"`
	AppCertificate videoRelayAppCertificate `json:"app_certificate"`
}

type videoRelayAppCredentials struct {
	PrivateKeyPEM string `json:"private_key_pem"`
	CSRPem        string `json:"csr_pem"`
}

type videoRelayAppCertificate struct {
	CertificatePEM      string `json:"certificate_pem"`
	CertificateChainPEM string `json:"certificate_chain_pem"`
	Subject             string `json:"subject"`
	FingerprintSHA256   string `json:"fingerprint_sha256"`
}

type videoRelayBindArtifact struct {
	Brandname   string           `json:"brandname"`
	Inputs      bindInputs       `json:"inputs"`
	Assignments []bindAssignment `json:"assignments"`
}

type videoRelayDeviceManifest struct {
	DeviceID             string   `json:"device_id"`
	DeviceType           string   `json:"device_type"`
	ServiceOptions       []string `json:"service_options"`
	CertificatePath      string   `json:"certificate_path"`
	CertificateChainPath string   `json:"certificate_chain_path"`
	KeyPath              string   `json:"key_path"`
}

type videoRelaySelectedDevice struct {
	DeviceID       string
	DeviceType     string
	AssignedEmail  string
	ServiceOptions []string
	CertPath       string
	KeyPath        string
	ChainPath      string
	User           videoRelayUser
}

type videoRelayRunnerConfig struct {
	Workspace          string
	APIURL             string
	OutDir             string
	Profile            string
	DurationSeconds    int
	DeviceIDs          []string
	DeviceTokenMapFile string
	AppTokenMapFile    string
}

type videoRelayTokenMapFiles struct {
	Device string
	App    string
}

type videoRelayResult struct {
	Schema      string                   `json:"schema"`
	GeneratedAt string                   `json:"generated_at"`
	Status      string                   `json:"status"`
	Overall     string                   `json:"overall"`
	Brandname   string                   `json:"brandname"`
	Profile     string                   `json:"profile"`
	ProbeModel  string                   `json:"probe_model"`
	Artifacts   map[string]string        `json:"artifacts,omitempty"`
	Devices     []videoRelayDeviceResult `json:"devices"`
	Error       string                   `json:"error,omitempty"`
}

type videoRelayDeviceResult struct {
	DeviceID              string `json:"device_id"`
	AssignedEmail         string `json:"assigned_email,omitempty"`
	WebSocketOwnerStatus  string `json:"websocket_owner_status"`
	WebRTCCreateStatus    string `json:"webrtc_create_status"`
	WebRTCAnswerStatus    string `json:"webrtc_answer_status"`
	ICEConnectedStatus    string `json:"ice_connected_status"`
	RTPReceiveStatus      string `json:"rtp_receive_status"`
	CloseStatus           string `json:"close_status"`
	SessionIDPresent      bool   `json:"session_id_present,omitempty"`
	ICEServerCount        int    `json:"ice_server_count,omitempty"`
	ICEConnectedLatencyMS int64  `json:"ice_connected_latency_ms,omitempty"`
	RTPCodec              string `json:"rtp_codec,omitempty"`
	RTPNALTypes           string `json:"rtp_nal_types,omitempty"`
	RTPPacketsReceived    int    `json:"rtp_packets_received,omitempty"`
	RTPBytesReceived      int    `json:"rtp_bytes_received,omitempty"`
	Error                 string `json:"error,omitempty"`
}

func runVideoRelayTest(args []string) error {
	fs := flag.NewFlagSet("video-relay-test", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	envRootFlag := fs.String("env-root", "", "environment root")
	brandname := fs.String("brandname", "", "brand name")
	outDir := fs.String("out-dir", "", "output directory")
	profile := fs.String("profile", "smoke", "profile")
	duration := fs.Int("duration-seconds", 120, "duration seconds")
	maxDevices := fs.Int("max-devices", 3, "maximum selected video devices")
	traceDetail := fs.String("trace-detail", "summary", "console trace detail: none or summary")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required")
	}
	if *brandname == "" {
		return errors.New("--brandname is required")
	}
	if *profile != "smoke" {
		return errors.New("--profile must be smoke")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	if *outDir == "" {
		*outDir = filepath.Join(envRoot, "artifacts", "video-relay-test", time.Now().UTC().Format("20060102T150405Z"))
	}
	result, exitErr := executeVideoRelayTest(workspace, envRoot, *brandname, *outDir, *profile, *duration, *maxDevices, *traceDetail)
	if result.Status == "PASS" {
		return nil
	}
	if exitErr != nil {
		return exitErr
	}
	return exitCode(1)
}

func executeVideoRelayTest(workspace, envRoot, brandname, outDir, profile string, durationSeconds, maxDevices int, traceDetail string) (videoRelayResult, error) {
	_ = os.MkdirAll(outDir, 0o755)
	result := videoRelayResult{
		Schema:      "rtk-cloud-workspace.video-relay-test/v1",
		GeneratedAt: time.Now().UTC().Format(time.RFC3339),
		Status:      "PASS",
		Overall:     "pass",
		Brandname:   brandname,
		Profile:     profile,
		ProbeModel:  videoRelayProbeModel,
		Artifacts:   map[string]string{},
	}
	brandSlug := strings.ToLower(strings.TrimSpace(brandname))
	usersPath := latestMatchingFile(filepath.Join(envRoot, "artifacts", "users"), brandSlug+"-users-*.json")
	bindPath := latestMatchingFile(filepath.Join(envRoot, "artifacts", "device-bind"), brandSlug+"-device-bind-*.json")
	if usersPath == "" || bindPath == "" {
		return writeVideoRelayBlocked(outDir, result, "missing latest users or device-bind artifact")
	}
	result.Artifacts["users_artifact"] = usersPath
	result.Artifacts["device_bind_artifact"] = bindPath

	usersArtifact, err := readVideoRelayUsersArtifact(usersPath)
	if err != nil {
		return writeVideoRelayBlocked(outDir, result, "invalid users artifact: "+sanitizeVideoRelayText(err.Error()))
	}
	bind, err := readVideoRelayBindArtifact(bindPath)
	if err != nil {
		return writeVideoRelayBlocked(outDir, result, "invalid device-bind artifact: "+sanitizeVideoRelayText(err.Error()))
	}
	devicesDir := bind.Inputs.DevicesDir
	if devicesDir == "" {
		devicesDir = filepath.Join(envRoot, "devices", "test_device")
	}
	manifest, err := readVideoRelayManifest(filepath.Join(devicesDir, "manifests", "devices.json"))
	if err != nil {
		return writeVideoRelayBlocked(outDir, result, "invalid device manifest: "+sanitizeVideoRelayText(err.Error()))
	}
	usersByEmail := map[string]videoRelayUser{}
	for _, user := range usersArtifact.Users {
		usersByEmail[strings.ToLower(strings.TrimSpace(user.Email))] = user
	}
	selected, blockers := selectVideoRelayDevices(bind, usersByEmail, manifest, maxDevices)
	if len(blockers) > 0 {
		return writeVideoRelayBlocked(outDir, result, strings.Join(blockers, "; "))
	}

	stackEnv := videoRelayEnvValues(filepath.Join(envRoot, "env", "stack.env"))
	apiURL := "https://" + firstNonEmpty(stackEnv["VIDEO_CLOUD_DOMAIN"], "video-cloud-staging.realtekconnect.com")
	mtlsURL := videoCloudMTLSBaseURLForRelay(envRoot, stackEnv, apiURL)
	deviceTokens := map[string]string{}
	appTokens := map[string]string{}
	for _, device := range selected {
		cert, err := loadRelayDeviceCertificate(devicesDir, device)
		if err != nil {
			return writeVideoRelayBlocked(outDir, result, fmt.Sprintf("device %s certificate material missing: %s", device.DeviceID, sanitizeVideoRelayText(err.Error())))
		}
		deviceToken, err := requestVideoRelayDeviceToken(mtlsURL, cert)
		if err != nil {
			return writeVideoRelayFailed(outDir, result, "device request_token failed: "+sanitizeVideoRelayText(err.Error()))
		}
		appCert, err := loadRelayAppCertificate(device.User)
		if err != nil {
			return writeVideoRelayBlocked(outDir, result, fmt.Sprintf("users artifact lacks matching local app credentials for %s", device.AssignedEmail))
		}
		appToken, err := requestVideoRelayAppToken(mtlsURL, appCert, device.DeviceID)
		if err != nil {
			return writeVideoRelayFailed(outDir, result, "app request_token failed: "+sanitizeVideoRelayText(err.Error()))
		}
		deviceTokens[device.DeviceID] = deviceToken
		appTokens[device.DeviceID] = appToken.AccessToken
	}

	deviceIDs := make([]string, 0, len(selected))
	for _, device := range selected {
		deviceIDs = append(deviceIDs, device.DeviceID)
	}
	tokenFiles, cleanupTokenFiles, err := writeVideoRelayTokenMapFiles(deviceTokens, appTokens)
	if err != nil {
		return writeVideoRelayBlocked(outDir, result, sanitizeVideoRelayText(err.Error()))
	}
	defer cleanupTokenFiles()
	cfg := videoRelayRunnerConfig{
		Workspace:          workspace,
		APIURL:             apiURL,
		OutDir:             outDir,
		Profile:            profile,
		DurationSeconds:    durationSeconds,
		DeviceIDs:          deviceIDs,
		DeviceTokenMapFile: tokenFiles.Device,
		AppTokenMapFile:    tokenFiles.App,
	}
	args, display, err := buildVideoRelayRunnerArgs(cfg)
	if err != nil {
		return writeVideoRelayBlocked(outDir, result, sanitizeVideoRelayText(err.Error()))
	}
	if traceDetail != "none" {
		fmt.Fprintf(os.Stdout, "Video Relay Runtime Trace\n")
		fmt.Fprintf(os.Stdout, "  probe_model=%s devices=%s runner=%s\n", videoRelayProbeModel, strings.Join(deviceIDs, ","), display)
	}
	goCmd, err := exec.LookPath("go")
	if err != nil {
		return writeVideoRelayBlocked(outDir, result, "go is required")
	}
	cmd := exec.Command(goCmd, args...)
	cmd.Dir = filepath.Join(workspace, "e2e_test")
	cmd.Env = withEnv(os.Environ(), map[string]string{"GOWORK": "off"})
	var stderr bytes.Buffer
	cmd.Stdout = os.Stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return writeVideoRelayFailed(outDir, result, "load runner failed: "+sanitizeVideoRelayText(stderr.String()+" "+err.Error()))
	}

	loadResultsPath := filepath.Join(outDir, "load-results.json")
	result.Artifacts["load_results"] = loadResultsPath
	result.Artifacts["load_report"] = filepath.Join(outDir, "load-report.md")
	result.Devices = summarizeVideoRelayLoadResults(loadResultsPath, selected)
	for _, device := range result.Devices {
		if device.WebSocketOwnerStatus != "PASS" || device.WebRTCCreateStatus != "PASS" || device.WebRTCAnswerStatus != "PASS" ||
			device.ICEConnectedStatus != "PASS" || device.RTPReceiveStatus != "PASS" || device.CloseStatus != "PASS" ||
			device.RTPPacketsReceived <= 0 || device.RTPBytesReceived <= 0 {
			result.Status = "FAIL"
			result.Overall = "fail"
			break
		}
	}
	return writeVideoRelayFinal(outDir, result)
}

func readVideoRelayUsersArtifact(path string) (videoRelayUsersArtifact, error) {
	var artifact videoRelayUsersArtifact
	raw, err := os.ReadFile(path)
	if err != nil {
		return artifact, err
	}
	if err := json.Unmarshal(raw, &artifact); err != nil {
		return artifact, err
	}
	return artifact, nil
}

func readVideoRelayBindArtifact(path string) (videoRelayBindArtifact, error) {
	var artifact videoRelayBindArtifact
	raw, err := os.ReadFile(path)
	if err != nil {
		return artifact, err
	}
	return artifact, json.Unmarshal(raw, &artifact)
}

func readVideoRelayManifest(path string) (map[string]videoRelayDeviceManifest, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var rows []videoRelayDeviceManifest
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, err
	}
	out := map[string]videoRelayDeviceManifest{}
	for _, row := range rows {
		out[row.DeviceID] = row
	}
	return out, nil
}

func selectVideoRelayDevices(bind videoRelayBindArtifact, users map[string]videoRelayUser, manifest map[string]videoRelayDeviceManifest, maxDevices int) ([]videoRelaySelectedDevice, []string) {
	selected := []videoRelaySelectedDevice{}
	blockers := []string{}
	for _, assignment := range bind.Assignments {
		if assignment.DeviceType != "camera" || !contains(assignment.ServiceOptions, "video_streaming") {
			continue
		}
		email := strings.ToLower(strings.TrimSpace(assignment.AssignedEmail))
		user, ok := users[email]
		if !ok || !videoRelayHasLocalAppCredentials(user) {
			blockers = append(blockers, "users artifact lacks matching local app credentials for "+assignment.AssignedEmail)
			continue
		}
		manifestRecord, ok := manifest[assignment.DeviceID]
		if !ok {
			blockers = append(blockers, "device "+assignment.DeviceID+" missing from manifest")
			continue
		}
		selected = append(selected, videoRelaySelectedDevice{
			DeviceID: assignment.DeviceID, DeviceType: assignment.DeviceType, AssignedEmail: assignment.AssignedEmail,
			ServiceOptions: assignment.ServiceOptions, CertPath: manifestRecord.CertificatePath, KeyPath: manifestRecord.KeyPath,
			ChainPath: manifestRecord.CertificateChainPath, User: user,
		})
		if maxDevices > 0 && len(selected) >= maxDevices {
			break
		}
	}
	if len(selected) == 0 && len(blockers) == 0 {
		blockers = append(blockers, "no bound camera devices with video_streaming service option")
	}
	return selected, blockers
}

func videoRelayHasLocalAppCredentials(user videoRelayUser) bool {
	return strings.Contains(user.AppCredentials.PrivateKeyPEM, "PRIVATE KEY-----") &&
		strings.HasPrefix(strings.TrimSpace(user.AppCredentials.CSRPem), "-----BEGIN CERTIFICATE REQUEST-----") &&
		strings.HasPrefix(strings.TrimSpace(firstNonEmpty(user.AppCertificate.CertificateChainPEM, user.AppCertificate.CertificatePEM)), "-----BEGIN CERTIFICATE-----")
}

func buildVideoRelayRunnerArgs(cfg videoRelayRunnerConfig) ([]string, string, error) {
	if len(cfg.DeviceIDs) == 0 {
		return nil, "", errors.New("at least one device id is required")
	}
	if cfg.DeviceTokenMapFile == "" || cfg.AppTokenMapFile == "" {
		return nil, "", errors.New("device and app token map files are required")
	}
	args := []string{"run", "./video_cloud/load/cmd/rtk-video-loadtest", "run",
		"--profile", cfg.Profile,
		"--actors", "device,viewer",
		"--device-online-mode", "websocket",
		"--device-route-set", "off",
		"--webrtc-media-set", "h264",
		"--webrtc-media-duration", "20s",
		"--device-ids", strings.Join(cfg.DeviceIDs, ","),
		"--virtual-devices", strconv.Itoa(len(cfg.DeviceIDs)),
		"--virtual-viewers", strconv.Itoa(len(cfg.DeviceIDs)),
		"--iterations", "1",
		"--duration", strconv.Itoa(cfg.DurationSeconds) + "s",
		"--api-url", cfg.APIURL,
		"--device-token-map-file", cfg.DeviceTokenMapFile,
		"--app-token-map-file", cfg.AppTokenMapFile,
		"--output", filepath.Join(cfg.OutDir, "load-results.json"),
		"--report-output", filepath.Join(cfg.OutDir, "load-report.md"),
		"--min-success-rate", "1",
		"--max-open-webrtc-sessions", "0",
		"--require-coverage-matrix",
	}
	display := sanitizeVideoRelayText(strings.Join(args, " "))
	return args, display, nil
}

func writeVideoRelayTokenMapFiles(deviceTokens, appTokens map[string]string) (videoRelayTokenMapFiles, func(), error) {
	devicePath, err := writeVideoRelayTokenMapFile("rtk-video-relay-device-tokens-*.json", deviceTokens)
	if err != nil {
		return videoRelayTokenMapFiles{}, func() {}, err
	}
	appPath, err := writeVideoRelayTokenMapFile("rtk-video-relay-app-tokens-*.json", appTokens)
	if err != nil {
		_ = os.Remove(devicePath)
		return videoRelayTokenMapFiles{}, func() {}, err
	}
	files := videoRelayTokenMapFiles{Device: devicePath, App: appPath}
	cleanup := func() {
		_ = os.Remove(files.Device)
		_ = os.Remove(files.App)
	}
	return files, cleanup, nil
}

func writeVideoRelayTokenMapFile(pattern string, tokens map[string]string) (string, error) {
	raw, err := json.Marshal(tokens)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", err
	}
	path := f.Name()
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		_ = os.Remove(path)
		return "", err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = os.Remove(path)
		return "", err
	}
	return path, nil
}

func summarizeVideoRelayLoadResults(path string, selected []videoRelaySelectedDevice) []videoRelayDeviceResult {
	raw, err := os.ReadFile(path)
	if err != nil {
		return []videoRelayDeviceResult{}
	}
	var parsed struct {
		Operations []struct {
			Name        string `json:"name"`
			DeviceID    string `json:"device_id"`
			Success     bool   `json:"success"`
			Evidence    string `json:"evidence"`
			LatencyMS   int64  `json:"latency_ms"`
			ErrorClass  string `json:"error_class"`
			ErrorDetail string `json:"error_detail"`
		} `json:"operations"`
	}
	_ = json.Unmarshal(raw, &parsed)
	byDevice := map[string]*videoRelayDeviceResult{}
	for _, device := range selected {
		byDevice[device.DeviceID] = &videoRelayDeviceResult{DeviceID: device.DeviceID, AssignedEmail: device.AssignedEmail}
	}
	for _, op := range parsed.Operations {
		row := byDevice[op.DeviceID]
		if row == nil {
			continue
		}
		status := "FAIL"
		if op.Success {
			status = "PASS"
		}
		switch op.Name {
		case "device_websocket_owner":
			row.WebSocketOwnerStatus = status
		case "webrtc_media_answer":
			if row.WebRTCCreateStatus == "" {
				row.WebRTCCreateStatus = status
				row.SessionIDPresent = op.Success
				row.ICEServerCount = max(row.ICEServerCount, parseEvidenceInt(op.Evidence, "ice_servers"))
			} else {
				row.WebRTCAnswerStatus = status
			}
		case "webrtc_media_ice_connected":
			row.ICEConnectedStatus = status
			row.ICEConnectedLatencyMS = op.LatencyMS
		case "webrtc_media_receive":
			row.RTPReceiveStatus = status
			packets, bytes := parseRTPRelayEvidence(op.Evidence)
			row.RTPPacketsReceived = packets
			row.RTPBytesReceived = bytes
			row.RTPCodec = parseEvidenceString(op.Evidence, "codec")
			row.RTPNALTypes = parseEvidenceString(op.Evidence, "nal_types")
		case "webrtc_media_close":
			row.CloseStatus = status
		}
		if !op.Success && row.Error == "" {
			row.Error = sanitizeVideoRelayText(firstNonEmpty(op.ErrorClass+": "+op.ErrorDetail, op.ErrorDetail))
		}
	}
	out := make([]videoRelayDeviceResult, 0, len(selected))
	for _, device := range selected {
		row := byDevice[device.DeviceID]
		fillMissingVideoRelayStatuses(row)
		out = append(out, *row)
	}
	return out
}

func fillMissingVideoRelayStatuses(row *videoRelayDeviceResult) {
	if row.WebSocketOwnerStatus == "" {
		row.WebSocketOwnerStatus = "FAIL"
	}
	if row.WebRTCCreateStatus == "" {
		row.WebRTCCreateStatus = "FAIL"
	}
	if row.WebRTCAnswerStatus == "" {
		row.WebRTCAnswerStatus = "FAIL"
	}
	if row.ICEConnectedStatus == "" {
		row.ICEConnectedStatus = "FAIL"
	}
	if row.RTPReceiveStatus == "" {
		row.RTPReceiveStatus = "FAIL"
	}
	if row.CloseStatus == "" {
		row.CloseStatus = "FAIL"
	}
}

func parseRTPRelayEvidence(evidence string) (int, int) {
	packets := parseEvidenceInt(evidence, "packets")
	bytes := parseEvidenceInt(evidence, "bytes")
	return packets, bytes
}

func parseEvidenceInt(evidence, key string) int {
	re := regexp.MustCompile(regexp.QuoteMeta(key) + `=([0-9]+)`)
	m := re.FindStringSubmatch(evidence)
	if len(m) != 2 {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	return n
}

func parseEvidenceString(evidence, key string) string {
	re := regexp.MustCompile(regexp.QuoteMeta(key) + `=([^ ]+)`)
	m := re.FindStringSubmatch(evidence)
	if len(m) != 2 {
		return ""
	}
	return m[1]
}

func renderVideoRelayReport(result videoRelayResult) string {
	var b strings.Builder
	fmt.Fprintln(&b, "Video Relay Test Report")
	fmt.Fprintln(&b, "=======================")
	fmt.Fprintf(&b, "Status: %s | Overall: %s\n", result.Status, result.Overall)
	fmt.Fprintf(&b, "Brand: %s | Profile: %s | Probe: %s\n\n", result.Brandname, result.Profile, result.ProbeModel)
	if result.Error != "" {
		fmt.Fprintf(&b, "Error: %s\n\n", sanitizeVideoRelayText(result.Error))
	}
	fmt.Fprintln(&b, "Devices:")
	for _, device := range result.Devices {
		fmt.Fprintf(&b, "- %s websocket=%s create=%s answer=%s ice=%s close=%s codec=%s nal_types=%s ICE servers=%d ICE connected=%dms RTP packets=%d RTP bytes=%d",
			device.DeviceID, device.WebSocketOwnerStatus, device.WebRTCCreateStatus, device.WebRTCAnswerStatus,
			device.ICEConnectedStatus, device.CloseStatus, device.RTPCodec, device.RTPNALTypes, device.ICEServerCount,
			device.ICEConnectedLatencyMS, device.RTPPacketsReceived, device.RTPBytesReceived)
		if device.Error != "" {
			fmt.Fprintf(&b, " error=%s", sanitizeVideoRelayText(device.Error))
		}
		fmt.Fprintln(&b)
	}
	if len(result.Artifacts) > 0 {
		fmt.Fprintln(&b, "\nArtifacts:")
		for _, key := range sortedMapKeys(result.Artifacts) {
			fmt.Fprintf(&b, "- %s: `%s`\n", key, result.Artifacts[key])
		}
	}
	return sanitizeVideoRelayText(b.String())
}

func writeVideoRelayBlocked(outDir string, result videoRelayResult, reason string) (videoRelayResult, error) {
	result.Status = "BLOCKED"
	result.Overall = "blocked"
	result.Error = sanitizeVideoRelayText(reason)
	return writeVideoRelayFinal(outDir, result)
}

func writeVideoRelayFailed(outDir string, result videoRelayResult, reason string) (videoRelayResult, error) {
	result.Status = "FAIL"
	result.Overall = "fail"
	result.Error = sanitizeVideoRelayText(reason)
	return writeVideoRelayFinal(outDir, result)
}

func writeVideoRelayFinal(outDir string, result videoRelayResult) (videoRelayResult, error) {
	resultsFile := filepath.Join(outDir, "results.json")
	reportFile := filepath.Join(outDir, "TEST_REPORT.md")
	if result.Artifacts == nil {
		result.Artifacts = map[string]string{}
	}
	result.Artifacts["results"] = resultsFile
	result.Artifacts["report"] = reportFile
	if err := writeJSON(resultsFile, result); err != nil {
		return result, err
	}
	if err := os.WriteFile(reportFile, []byte(renderVideoRelayReport(result)), 0o644); err != nil {
		return result, err
	}
	fmt.Print(renderVideoRelayConsole(result))
	return result, nil
}

func renderVideoRelayConsole(result videoRelayResult) string {
	var b strings.Builder
	fmt.Fprintln(&b, "Video Relay Test Report")
	fmt.Fprintln(&b, "=======================")
	fmt.Fprintf(&b, "Status: %s | Overall: %s\n", result.Status, result.Overall)
	fmt.Fprintf(&b, "Brand: %s | Profile: %s\n", result.Brandname, result.Profile)
	for _, device := range result.Devices {
		fmt.Fprintf(&b, "  %s websocket=%s create=%s answer=%s ice=%s rtp=%s close=%s codec=%s nal_types=%s packets=%d bytes=%d\n",
			device.DeviceID, device.WebSocketOwnerStatus, device.WebRTCCreateStatus, device.WebRTCAnswerStatus,
			device.ICEConnectedStatus, device.RTPReceiveStatus, device.CloseStatus, device.RTPCodec, device.RTPNALTypes, device.RTPPacketsReceived, device.RTPBytesReceived)
	}
	if result.Error != "" {
		fmt.Fprintf(&b, "Error: %s\n", sanitizeVideoRelayText(result.Error))
	}
	fmt.Fprintf(&b, "\n{\"action\":\"video-relay-test\",\"overall\":\"%s\",\"status\":\"%s\",\"report_file\":\"%s\",\"results_file\":\"%s\"}\n",
		result.Overall, result.Status, result.Artifacts["report"], result.Artifacts["results"])
	return sanitizeVideoRelayText(b.String())
}

func sanitizeVideoRelayText(text string) string {
	if text == "" {
		return ""
	}
	patterns := []string{
		`(?i)bearer\s+[A-Za-z0-9._~+/=-]+`,
		`(?i)(access_token|refresh_token|device-token-map-json|app-token-map-json|credential|private_key_pem|password|secret)[^ \n\r]*[^\n\r]*`,
		`-----BEGIN [^-]+-----[\s\S]*?-----END [^-]+-----`,
	}
	out := text
	for _, pattern := range patterns {
		out = regexp.MustCompile(pattern).ReplaceAllString(out, "redacted sensitive value")
	}
	return out
}

func loadRelayDeviceCertificate(devicesDir string, device videoRelaySelectedDevice) (tls.Certificate, error) {
	certPath := filepath.Join(devicesDir, firstNonEmpty(device.ChainPath, device.CertPath))
	keyPath := filepath.Join(devicesDir, device.KeyPath)
	if filepath.IsAbs(firstNonEmpty(device.ChainPath, device.CertPath)) {
		certPath = firstNonEmpty(device.ChainPath, device.CertPath)
	}
	if filepath.IsAbs(device.KeyPath) {
		keyPath = device.KeyPath
	}
	return tls.LoadX509KeyPair(certPath, keyPath)
}

func loadRelayAppCertificate(user videoRelayUser) (tls.Certificate, error) {
	certPEM := firstNonEmpty(user.AppCertificate.CertificateChainPEM, user.AppCertificate.CertificatePEM)
	if strings.TrimSpace(certPEM) == "" || strings.TrimSpace(user.AppCredentials.PrivateKeyPEM) == "" {
		return tls.Certificate{}, errors.New("missing app certificate or key")
	}
	return tls.X509KeyPair([]byte(certPEM), []byte(user.AppCredentials.PrivateKeyPEM))
}

type videoRelayTokenResponse struct {
	Scope       string `json:"scope"`
	AccessToken string `json:"access_token"`
}

func requestVideoRelayDeviceToken(apiBaseURL string, cert tls.Certificate) (string, error) {
	resp, err := requestVideoRelayToken(apiBaseURL, cert, map[string]string{"scope": "device"})
	return resp.AccessToken, err
}

func requestVideoRelayAppToken(apiBaseURL string, cert tls.Certificate, deviceID string) (videoRelayTokenResponse, error) {
	return requestVideoRelayToken(apiBaseURL, cert, map[string]string{"scope": "app", "devid": deviceID})
}

func requestVideoRelayToken(apiBaseURL string, cert tls.Certificate, payload map[string]string) (videoRelayTokenResponse, error) {
	apiBaseURL = strings.TrimRight(strings.TrimSpace(apiBaseURL), "/")
	if apiBaseURL == "" {
		return videoRelayTokenResponse{}, errors.New("missing video cloud API URL")
	}
	raw, _ := json.Marshal(payload)
	req, err := http.NewRequest(http.MethodPost, apiBaseURL+"/request_token", bytes.NewReader(raw))
	if err != nil {
		return videoRelayTokenResponse{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := http.Client{Timeout: 10 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{Certificates: []tls.Certificate{cert}, InsecureSkipVerify: true}}}
	httpResp, err := client.Do(req)
	if err != nil {
		return videoRelayTokenResponse{}, err
	}
	defer httpResp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(httpResp.Body, 1<<20))
	if httpResp.StatusCode < 200 || httpResp.StatusCode >= 300 {
		return videoRelayTokenResponse{}, fmt.Errorf("request_token status=%d", httpResp.StatusCode)
	}
	var out videoRelayTokenResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return out, err
	}
	if strings.TrimSpace(out.AccessToken) == "" {
		return out, errors.New("request_token response missing access_token")
	}
	return out, nil
}

func videoCloudMTLSBaseURLForRelay(envRoot string, stackValues map[string]string, fallback string) string {
	host := firstNonEmpty(
		stackValues["VIDEO_CLOUD_MTLS_DOMAIN"],
		stackValues["VIDEO_CLOUD_DEVICE_CLIENT_DOMAIN"],
		videoRelayTopologyDeployValue(firstExistingPath(
			filepath.Join(envRoot, "topology", "video-cloud.yaml"),
			filepath.Join(envRoot, "topology", "video-cloud-staging.yaml"),
		), "device_client_domain"),
	)
	if host != "" {
		return "https://" + strings.TrimRight(strings.TrimSpace(host), "/")
	}
	return fallback
}

func videoRelayTopologyDeployValue(path, key string) string {
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

func videoRelayEnvValues(path string) map[string]string {
	values, err := readEnvFile(path)
	if err != nil {
		return map[string]string{}
	}
	return values
}

func sortedMapKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
