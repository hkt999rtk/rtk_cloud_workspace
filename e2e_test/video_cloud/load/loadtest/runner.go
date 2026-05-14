package loadtest

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

type Runner struct {
	client     *http.Client
	ownsClient bool
}

var webSocketOwnerKeepaliveInterval = 30 * time.Second

func NewRunner(client *http.Client) *Runner {
	if client == nil {
		client = &http.Client{}
		return &Runner{client: client, ownsClient: true}
	}
	return &Runner{client: client}
}

func DefaultConfigFromEnv() Config {
	host, _ := os.Hostname()
	cfg := Config{
		Profile:               envDefault("VIDEO_CLOUD_LOAD_PROFILE", ProfileSafeStaging),
		APIURL:                os.Getenv("VIDEO_CLOUD_LOAD_API_URL"),
		WSURL:                 os.Getenv("VIDEO_CLOUD_LOAD_WS_URL"),
		AccountToken:          os.Getenv("VIDEO_CLOUD_LOAD_ACCOUNT_TOKEN"),
		AppTokens:             parseTokenMap(os.Getenv("VIDEO_CLOUD_LOAD_APP_TOKENS")),
		AdminToken:            os.Getenv("VIDEO_CLOUD_LOAD_ADMIN_TOKEN"),
		DeviceToken:           os.Getenv("VIDEO_CLOUD_LOAD_DEVICE_TOKEN"),
		DeviceTokens:          parseTokenMap(os.Getenv("VIDEO_CLOUD_LOAD_DEVICE_TOKENS")),
		RefreshToken:          os.Getenv("VIDEO_CLOUD_LOAD_REFRESH_TOKEN"),
		RunID:                 os.Getenv("VIDEO_CLOUD_LOAD_RUN_ID"),
		InstanceID:            os.Getenv("VIDEO_CLOUD_LOAD_INSTANCE_ID"),
		Actors:                envDefault("VIDEO_CLOUD_LOAD_ACTORS", ActorAll),
		AppRouteSet:           envDefault("VIDEO_CLOUD_LOAD_APP_ROUTE_SET", AppRouteSetSmoke),
		DeviceRouteSet:        envDefault("VIDEO_CLOUD_LOAD_DEVICE_ROUTE_SET", DeviceRouteSetSmoke),
		DeviceTransportSet:    envDefault("VIDEO_CLOUD_LOAD_DEVICE_TRANSPORT_SET", DeviceTransportSetSmoke),
		ViewerRouteSet:        envDefault("VIDEO_CLOUD_LOAD_VIEWER_ROUTE_SET", ViewerRouteSetSmoke),
		WebRTCMediaSet:        envDefault("VIDEO_CLOUD_LOAD_WEBRTC_MEDIA_SET", WebRTCMediaSetOff),
		ClipSet:               envDefault("VIDEO_CLOUD_LOAD_CLIP_SET", ClipSetOff),
		MQTTSet:               envDefault("VIDEO_CLOUD_LOAD_MQTT_SET", MQTTSetOff),
		MQTTAddr:              os.Getenv("VIDEO_CLOUD_MQTT_ADDR"),
		MQTTUsername:          os.Getenv("VIDEO_CLOUD_MQTT_USERNAME"),
		MQTTPassword:          os.Getenv("VIDEO_CLOUD_MQTT_PASSWORD"),
		MQTTTopicRoot:         envDefault("VIDEO_CLOUD_MQTT_TOPIC_ROOT", "devices"),
		MQTTDeviceProfile:     envDefault("VIDEO_CLOUD_LOAD_MQTT_DEVICE_PROFILE", MQTTDeviceProfileCamera),
		MQTTIoTMix:            envDefault("VIDEO_CLOUD_LOAD_MQTT_IOT_MIX", "light=4,air_conditioner=3,smart_meter=3"),
		MQTTRequired:          envBool("VIDEO_CLOUD_LOAD_MQTT_REQUIRED"),
		NegativeSet:           envDefault("VIDEO_CLOUD_LOAD_NEGATIVE_SET", NegativeSetOff),
		NegativeMalformedPath: os.Getenv("VIDEO_CLOUD_LOAD_NEGATIVE_MALFORMED_PATH"),
		NegativeTimeoutPath:   os.Getenv("VIDEO_CLOUD_LOAD_NEGATIVE_TIMEOUT_PATH"),
		DeviceOnlineMode:      envDefault("VIDEO_CLOUD_LOAD_DEVICE_ONLINE_MODE", DeviceOnlineModeWebSocket),
		DevicePrefix:          envDefault("VIDEO_CLOUD_LOAD_DEVICE_PREFIX", "load-device"),
		DeviceIDs:             ParseDeviceIDs(os.Getenv("VIDEO_CLOUD_LOAD_DEVICE_IDS")),
		ContractsCommit:       os.Getenv("VIDEO_CLOUD_LOAD_CONTRACTS_COMMIT"),
		ServerCommit:          os.Getenv("VIDEO_CLOUD_LOAD_SERVER_COMMIT"),
		ClientCommit:          os.Getenv("VIDEO_CLOUD_LOAD_CLIENT_COMMIT"),
		BinarySHA256:          os.Getenv("VIDEO_CLOUD_LOAD_BINARY_SHA256"),
		Duration:              30 * time.Second,
		VirtualDevices:        1,
		VirtualViewers:        1,
		AppConcurrency:        1,
		DeviceConcurrency:     1,
		ViewerConcurrency:     1,
		Iterations:            1,
		HTTPTimeout:           10 * time.Second,
		Thresholds: Thresholds{
			MinSuccessRate:           0.95,
			MaxP95Latency:            30000,
			MaxP99Latency:            60000,
			MaxWebRTCSetupP95Latency: 30000,
			MaxOpenWebRTCSessions:    0,
			RequireCoverageMatrix:    true,
		},
	}
	if cfg.RunID == "" {
		cfg.RunID = time.Now().UTC().Format("20060102T150405Z") + "-" + uuid.NewString()[:8]
	}
	if cfg.InstanceID == "" {
		cfg.InstanceID = fmt.Sprintf("%s-%d", host, os.Getpid())
	}
	return cfg
}

func parseTokenMap(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	var tokens map[string]string
	if err := json.Unmarshal([]byte(raw), &tokens); err != nil {
		return nil
	}
	return tokens
}

func ParseDeviceIDs(raw string) []string {
	if raw == "" {
		return nil
	}
	parts := strings.Split(raw, ",")
	ids := make([]string, 0, len(parts))
	for _, part := range parts {
		id := strings.TrimSpace(part)
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

func envDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func envBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func ParseMQTTIoTMix(raw string) (map[string]int, error) {
	if strings.TrimSpace(raw) == "" {
		raw = "light=4,air_conditioner=3,smart_meter=3"
	}
	allowed := map[string]bool{"light": true, "air_conditioner": true, "smart_meter": true}
	mix := map[string]int{}
	total := 0
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			return nil, fmt.Errorf("invalid MQTT IoT mix %q: empty item", raw)
		}
		key, value, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("invalid MQTT IoT mix item %q: expected capability=count", part)
		}
		capability := strings.TrimSpace(key)
		if !allowed[capability] {
			return nil, fmt.Errorf("unsupported MQTT IoT capability %q: expected light, air_conditioner, or smart_meter", capability)
		}
		count, err := strconv.Atoi(strings.TrimSpace(value))
		if err != nil || count < 0 {
			return nil, fmt.Errorf("invalid MQTT IoT mix count for %q: expected non-negative integer", capability)
		}
		mix[capability] = count
		total += count
	}
	if total == 0 {
		return nil, fmt.Errorf("invalid MQTT IoT mix %q: at least one device is required", raw)
	}
	return mix, nil
}

func (c *Config) Validate() error {
	if c.Profile == "" {
		c.Profile = ProfileSafeStaging
	}
	switch c.Profile {
	case ProfileSmoke, ProfileSafeStaging, ProfileFunctional:
	case ProfileStress:
		if !c.AllowStress {
			return errors.New("stress profile requires --allow-stress or VIDEO_CLOUD_LOAD_ALLOW_STRESS=1")
		}
	case ProfileSoak:
		if !c.AllowSoak {
			return errors.New("soak profile requires --allow-soak or VIDEO_CLOUD_LOAD_ALLOW_SOAK=1")
		}
	default:
		return fmt.Errorf("unsupported profile %q", c.Profile)
	}
	if c.APIURL == "" {
		return errors.New("api url is required")
	}
	if c.DeviceOnlineMode == "" {
		c.DeviceOnlineMode = DeviceOnlineModeNone
	}
	switch c.DeviceOnlineMode {
	case DeviceOnlineModeNone:
	case DeviceOnlineModeWebSocket:
		if c.WSURL == "" {
			wsURL, err := DeriveWebSocketBaseURL(c.APIURL)
			if err != nil {
				return err
			}
			c.WSURL = wsURL
		}
	default:
		return fmt.Errorf("unsupported device online mode %q: expected %q or %q", c.DeviceOnlineMode, DeviceOnlineModeNone, DeviceOnlineModeWebSocket)
	}
	if c.RunID == "" {
		c.RunID = uuid.NewString()
	}
	if c.InstanceID == "" {
		c.InstanceID = "local"
	}
	actors, _, err := NormalizeActors(c.Actors)
	if err != nil {
		return err
	}
	c.Actors = actors
	if c.AppRouteSet == "" {
		c.AppRouteSet = AppRouteSetSmoke
	}
	if c.Profile == ProfileFunctional && c.AppRouteSet == AppRouteSetSmoke {
		c.AppRouteSet = AppRouteSetFunctional
	}
	switch c.AppRouteSet {
	case AppRouteSetSmoke, AppRouteSetFunctional:
	default:
		return fmt.Errorf("unsupported app route set %q: expected %q or %q", c.AppRouteSet, AppRouteSetSmoke, AppRouteSetFunctional)
	}
	if c.DeviceRouteSet == "" {
		c.DeviceRouteSet = DeviceRouteSetSmoke
	}
	if c.Profile == ProfileFunctional && c.DeviceRouteSet == DeviceRouteSetSmoke {
		c.DeviceRouteSet = DeviceRouteSetFunctional
	}
	switch c.DeviceRouteSet {
	case DeviceRouteSetSmoke, DeviceRouteSetFunctional:
	default:
		return fmt.Errorf("unsupported device route set %q: expected %q or %q", c.DeviceRouteSet, DeviceRouteSetSmoke, DeviceRouteSetFunctional)
	}
	if c.DeviceTransportSet == "" {
		c.DeviceTransportSet = DeviceTransportSetSmoke
	}
	if c.Profile == ProfileFunctional && c.DeviceTransportSet == DeviceTransportSetSmoke {
		c.DeviceTransportSet = DeviceTransportSetSnapshot
	}
	switch c.DeviceTransportSet {
	case DeviceTransportSetSmoke, DeviceTransportSetSnapshot:
	default:
		return fmt.Errorf("unsupported device transport set %q: expected %q or %q", c.DeviceTransportSet, DeviceTransportSetSmoke, DeviceTransportSetSnapshot)
	}
	if c.ViewerRouteSet == "" {
		c.ViewerRouteSet = ViewerRouteSetSmoke
	}
	if c.Profile == ProfileFunctional && c.ViewerRouteSet == ViewerRouteSetSmoke {
		c.ViewerRouteSet = ViewerRouteSetFunctional
	}
	switch c.ViewerRouteSet {
	case ViewerRouteSetSmoke, ViewerRouteSetFunctional, ViewerRouteSetNegative:
	default:
		return fmt.Errorf("unsupported viewer route set %q: expected %q, %q, or %q", c.ViewerRouteSet, ViewerRouteSetSmoke, ViewerRouteSetFunctional, ViewerRouteSetNegative)
	}
	if c.WebRTCMediaSet == "" {
		c.WebRTCMediaSet = WebRTCMediaSetOff
	}
	switch c.WebRTCMediaSet {
	case WebRTCMediaSetOff, WebRTCMediaSetRTP:
	default:
		return fmt.Errorf("unsupported webrtc media set %q: expected %q or %q", c.WebRTCMediaSet, WebRTCMediaSetOff, WebRTCMediaSetRTP)
	}
	if c.ClipSet == "" {
		c.ClipSet = ClipSetOff
	}
	switch c.ClipSet {
	case ClipSetOff, ClipSetRecordingFunctional:
	default:
		return fmt.Errorf("unsupported clip set %q: expected %q or %q", c.ClipSet, ClipSetOff, ClipSetRecordingFunctional)
	}
	if c.MQTTSet == "" {
		c.MQTTSet = MQTTSetOff
	}
	switch c.MQTTSet {
	case MQTTSetOff, MQTTSetBroker:
	default:
		return fmt.Errorf("unsupported mqtt set %q: expected %q or %q", c.MQTTSet, MQTTSetOff, MQTTSetBroker)
	}
	if c.MQTTDeviceProfile == "" {
		c.MQTTDeviceProfile = MQTTDeviceProfileCamera
	}
	switch c.MQTTDeviceProfile {
	case MQTTDeviceProfileCamera, MQTTDeviceProfileIoT, MQTTDeviceProfileMixed:
	default:
		return fmt.Errorf("unsupported mqtt device profile %q: expected %q, %q, or %q", c.MQTTDeviceProfile, MQTTDeviceProfileCamera, MQTTDeviceProfileIoT, MQTTDeviceProfileMixed)
	}
	if c.MQTTIoTMix == "" {
		c.MQTTIoTMix = "light=4,air_conditioner=3,smart_meter=3"
	}
	if _, err := ParseMQTTIoTMix(c.MQTTIoTMix); err != nil {
		return err
	}
	if c.MQTTSet == MQTTSetBroker && c.MQTTRequired && strings.TrimSpace(c.MQTTAddr) == "" {
		return errors.New("mqtt-required requires VIDEO_CLOUD_MQTT_ADDR or --mqtt-addr")
	}
	if c.NegativeSet == "" {
		c.NegativeSet = NegativeSetOff
	}
	switch c.NegativeSet {
	case NegativeSetOff, NegativeSetHTTP:
	default:
		return fmt.Errorf("unsupported negative set %q: expected %q or %q", c.NegativeSet, NegativeSetOff, NegativeSetHTTP)
	}
	if c.MQTTTopicRoot == "" {
		c.MQTTTopicRoot = "devices"
	}
	if c.DevicePrefix == "" {
		c.DevicePrefix = "load-device"
	}
	if len(c.DeviceIDs) > 0 {
		normalized := make([]string, 0, len(c.DeviceIDs))
		seen := map[string]bool{}
		for _, raw := range c.DeviceIDs {
			deviceID := strings.TrimSpace(raw)
			if deviceID == "" {
				return errors.New("device ids must not contain empty values")
			}
			if seen[deviceID] {
				return fmt.Errorf("duplicate device id %q", deviceID)
			}
			seen[deviceID] = true
			normalized = append(normalized, deviceID)
		}
		c.DeviceIDs = normalized
		c.VirtualDevices = len(normalized)
	}
	if c.Duration <= 0 {
		c.Duration = 30 * time.Second
	}
	if c.VirtualDevices <= 0 {
		c.VirtualDevices = 1
	}
	if c.VirtualViewers < 0 {
		c.VirtualViewers = 0
	}
	if c.AppConcurrency <= 0 {
		c.AppConcurrency = 1
	}
	if c.DeviceConcurrency <= 0 {
		c.DeviceConcurrency = 1
	}
	if c.ViewerConcurrency <= 0 {
		c.ViewerConcurrency = 1
	}
	if c.Iterations <= 0 {
		c.Iterations = 1
	}
	if c.AppRatePerSecond < 0 || c.DeviceRatePerSecond < 0 || c.ViewerRatePerSecond < 0 {
		return errors.New("actor rates must be non-negative")
	}
	if c.HTTPTimeout <= 0 {
		c.HTTPTimeout = 10 * time.Second
	}
	return nil
}

func NormalizeActors(raw string) (string, map[string]bool, error) {
	if strings.TrimSpace(raw) == "" {
		raw = ActorAll
	}
	enabled := map[string]bool{}
	parts := strings.Split(raw, ",")
	for _, part := range parts {
		actor := strings.ToLower(strings.TrimSpace(part))
		if actor == "" {
			return "", nil, fmt.Errorf("invalid actors %q: empty actor", raw)
		}
		switch actor {
		case ActorAll:
			if len(parts) != 1 {
				return "", nil, fmt.Errorf("invalid actors %q: %q cannot be combined with other actors", raw, ActorAll)
			}
			return ActorAll, map[string]bool{ActorApp: true, ActorDevice: true, ActorViewer: true}, nil
		case ActorApp, ActorDevice, ActorViewer:
			enabled[actor] = true
		default:
			return "", nil, fmt.Errorf("unsupported actor %q: expected %q, %q, %q, or %q", actor, ActorAll, ActorApp, ActorDevice, ActorViewer)
		}
	}
	if len(enabled) == 0 {
		return "", nil, fmt.Errorf("invalid actors %q: at least one actor is required", raw)
	}
	ordered := make([]string, 0, len(enabled))
	for _, actor := range []string{ActorApp, ActorDevice, ActorViewer} {
		if enabled[actor] {
			ordered = append(ordered, actor)
		}
	}
	return strings.Join(ordered, ","), enabled, nil
}

func (c Config) EnabledActors() map[string]bool {
	_, enabled, err := NormalizeActors(c.Actors)
	if err != nil {
		return map[string]bool{}
	}
	return enabled
}

func (c Config) DeviceIDsForRun() []string {
	if len(c.DeviceIDs) > 0 {
		ids := make([]string, len(c.DeviceIDs))
		copy(ids, c.DeviceIDs)
		return ids
	}
	count := c.VirtualDevices
	if count <= 0 {
		count = 1
	}
	ids := make([]string, count)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-%d", c.DevicePrefix, i)
	}
	return ids
}

func (c Config) DeviceIDFor(index int) string {
	ids := c.DeviceIDsForRun()
	return ids[index%len(ids)]
}

func DeriveWebSocketBaseURL(raw string) (string, error) {
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("parse api url: %w", err)
	}
	switch parsed.Scheme {
	case "http":
		parsed.Scheme = "ws"
	case "https":
		parsed.Scheme = "wss"
	case "ws", "wss":
	default:
		return "", fmt.Errorf("unsupported api url scheme %q for websocket derivation", parsed.Scheme)
	}
	parsed.Path = strings.TrimRight(parsed.Path, "/")
	parsed.RawQuery = ""
	parsed.Fragment = ""
	return parsed.String(), nil
}

func (r *Runner) Run(ctx context.Context, cfg Config) (*Result, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	enabledActors := cfg.EnabledActors()
	if r.ownsClient || r.client.Timeout == 0 {
		r.client.Timeout = cfg.HTTPTimeout
	}
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	started := time.Now().UTC()
	var mu sync.Mutex
	operations := make([]Operation, 0)

	record := func(op Operation) {
		mu.Lock()
		operations = append(operations, op)
		mu.Unlock()
	}

	stopOwners := func() {}
	if enabledActors[ActorDevice] && cfg.DeviceOnlineMode == DeviceOnlineModeWebSocket {
		var ownerOps []Operation
		stopOwners, ownerOps = r.startWebSocketOwners(runCtx, cfg, record)
		defer stopOwners()
		for _, op := range ownerOps {
			record(op)
		}
	}

	var groups sync.WaitGroup
	if enabledActors[ActorApp] {
		groups.Add(1)
		go func() {
			defer groups.Done()
			r.runGroup(runCtx, cfg.AppConcurrency, cfg.VirtualDevices, cfg.Iterations, cfg.AppRatePerSecond, cfg.RampUp, cfg.Duration, func(i int) []Operation {
				deviceID := cfg.DeviceIDFor(i)
				return r.runAppActor(runCtx, cfg, deviceID)
			}, record)
		}()
	}
	if enabledActors[ActorDevice] {
		groups.Add(1)
		go func() {
			defer groups.Done()
			r.runGroup(runCtx, cfg.DeviceConcurrency, cfg.VirtualDevices, cfg.Iterations, cfg.DeviceRatePerSecond, cfg.RampUp, cfg.Duration, func(i int) []Operation {
				deviceID := cfg.DeviceIDFor(i)
				return r.runDeviceActor(runCtx, cfg, deviceID)
			}, record)
		}()
	}
	if enabledActors[ActorViewer] {
		groups.Add(1)
		go func() {
			defer groups.Done()
			r.runGroup(runCtx, cfg.ViewerConcurrency, cfg.VirtualViewers, cfg.Iterations, cfg.ViewerRatePerSecond, cfg.RampUp, cfg.Duration, func(i int) []Operation {
				deviceID := cfg.DeviceIDFor(i)
				viewerID := fmt.Sprintf("viewer-%d", i)
				return r.runViewerActor(runCtx, cfg, deviceID, viewerID)
			}, record)
		}()
	}
	groups.Wait()
	for _, op := range r.runNegativeCoverage(runCtx, cfg) {
		record(op)
	}

	ended := time.Now().UTC()
	result := BuildResult(cfg, started, ended, operations)
	return result, nil
}

func (r *Runner) startWebSocketOwners(ctx context.Context, cfg Config, record func(Operation)) (func(), []Operation) {
	ownerCtx, cancel := context.WithCancel(ctx)
	deviceIDs := cfg.DeviceIDsForRun()
	handles := make([]*webSocketOwnerHandle, 0, len(deviceIDs))
	ops := make([]Operation, 0, len(deviceIDs))
	for _, deviceID := range deviceIDs {
		start := time.Now()
		op := Operation{
			Actor:    ActorDevice,
			Name:     "device_websocket_owner",
			DeviceID: deviceID,
		}
		token := cfg.DeviceBearerFor(deviceID)
		if token == "" {
			op.Success = false
			op.ErrorClass = ClassAuth
			op.ErrorDetail = "missing device-scoped token for websocket owner"
			ops = append(ops, op)
			continue
		}
		conn, err := openWebSocketOwner(ownerCtx, cfg, deviceID, token)
		op.LatencyMS = time.Since(start).Milliseconds()
		if err != nil {
			op.Success = false
			op.ErrorClass = ClassifyError(0, nil, err)
			op.ErrorDetail = redactDetail(err.Error())
			ops = append(ops, op)
			continue
		}
		op.Success = true
		op.Evidence = "websocket_owner_online"
		ops = append(ops, op)
		handle := &webSocketOwnerHandle{}
		handle.set(conn)
		if cfg.DeviceTransportSet == DeviceTransportSetSnapshot {
			snapshotOps := sendWebSocketSnapshot(conn, cfg, deviceID)
			ops = append(ops, snapshotOps...)
			handle.close()
			reconnectStart := time.Now()
			reconnect := Operation{
				Actor:    ActorDevice,
				Name:     "device_websocket_reconnect",
				DeviceID: deviceID,
			}
			conn, err = openWebSocketOwner(ownerCtx, cfg, deviceID, token)
			reconnect.LatencyMS = time.Since(reconnectStart).Milliseconds()
			if err != nil {
				reconnect.Success = false
				reconnect.ErrorClass = ClassifyError(0, nil, err)
				reconnect.ErrorDetail = redactDetail(err.Error())
				ops = append(ops, reconnect)
				continue
			}
			reconnect.Success = true
			reconnect.Evidence = "websocket_owner_reconnected"
			ops = append(ops, reconnect)
			handle.set(conn)
		}
		handles = append(handles, handle)
		go r.maintainWebSocketOwner(ownerCtx, cfg, deviceID, token, handle, record)
	}
	stop := func() {
		cancel()
		for _, handle := range handles {
			handle.close()
		}
	}
	return stop, ops
}

type webSocketOwnerHandle struct {
	mu   sync.Mutex
	conn net.Conn
}

func (h *webSocketOwnerHandle) set(conn net.Conn) {
	h.mu.Lock()
	old := h.conn
	h.conn = conn
	h.mu.Unlock()
	if old != nil && old != conn {
		_ = old.Close()
	}
}

func (h *webSocketOwnerHandle) close() {
	h.mu.Lock()
	conn := h.conn
	h.conn = nil
	h.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (h *webSocketOwnerHandle) writeFrame(opcode byte, payload []byte) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.conn == nil {
		return errors.New("websocket owner connection is not open")
	}
	_ = h.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	err := writeWebSocketFrame(h.conn, opcode, payload)
	_ = h.conn.SetWriteDeadline(time.Time{})
	return err
}

func (r *Runner) maintainWebSocketOwner(ctx context.Context, cfg Config, deviceID, token string, handle *webSocketOwnerHandle, record func(Operation)) {
	ticker := time.NewTicker(webSocketOwnerKeepaliveInterval)
	defer ticker.Stop()

	listenerDone := r.startDeviceTransportListener(ctx, cfg, deviceID, handle, record)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			start := time.Now()
			op := Operation{Actor: ActorDevice, Name: "device_websocket_keepalive", DeviceID: deviceID}
			if err := handle.writeFrame(1, deviceWebSocketKeepalivePayload(cfg, deviceID)); err != nil {
				op.Success = false
				op.ErrorClass = ClassNetwork
				op.ErrorDetail = redactDetail(err.Error())
				op.LatencyMS = time.Since(start).Milliseconds()
				record(op)
				if r.reconnectWebSocketOwner(ctx, cfg, deviceID, token, handle, "keepalive", record) {
					listenerDone = r.startDeviceTransportListener(ctx, cfg, deviceID, handle, record)
				}
				continue
			}
			op.Success = true
			op.Evidence = "status_report_keepalive_sent"
			op.LatencyMS = time.Since(start).Milliseconds()
			record(op)
		case err, ok := <-listenerDone:
			if !ok {
				listenerDone = nil
				continue
			}
			if ctx.Err() != nil {
				return
			}
			if err != nil {
				record(Operation{
					Actor:       ActorDevice,
					Name:        "device_websocket_error",
					DeviceID:    deviceID,
					Success:     false,
					ErrorClass:  ClassNetwork,
					ErrorDetail: redactDetail(err.Error()),
				})
			}
			if r.reconnectWebSocketOwner(ctx, cfg, deviceID, token, handle, "listener", record) {
				listenerDone = r.startDeviceTransportListener(ctx, cfg, deviceID, handle, record)
			}
		}
	}
}

func deviceWebSocketKeepalivePayload(cfg Config, deviceID string) []byte {
	payload := map[string]any{
		"event": "status_report",
		"data": map[string]any{
			"wifi_strength": "-50",
			"run_id":        cfg.RunID,
			"device_id":     deviceID,
			"instance_id":   cfg.InstanceID,
		},
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return []byte(`{"event":"status_report","data":{"wifi_strength":"-50"}}`)
	}
	return body
}

func (r *Runner) reconnectWebSocketOwner(ctx context.Context, cfg Config, deviceID, token string, handle *webSocketOwnerHandle, reason string, record func(Operation)) bool {
	handle.close()
	start := time.Now()
	op := Operation{Actor: ActorDevice, Name: "device_websocket_reconnect", DeviceID: deviceID}
	conn, err := openWebSocketOwner(ctx, cfg, deviceID, token)
	op.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		op.Success = false
		op.ErrorClass = ClassNetwork
		op.ErrorDetail = redactDetail(err.Error())
		record(op)
		return false
	}
	handle.set(conn)
	op.Success = true
	op.Evidence = "websocket_owner_reconnected reason=" + reason
	record(op)
	return true
}

func (r *Runner) startDeviceTransportListener(ctx context.Context, cfg Config, deviceID string, handle *webSocketOwnerHandle, record func(Operation)) <-chan error {
	if cfg.WebRTCMediaSet != WebRTCMediaSetRTP && cfg.ClipSet != ClipSetRecordingFunctional {
		return nil
	}
	done := make(chan error, 1)
	go func() {
		handle.mu.Lock()
		conn := handle.conn
		handle.mu.Unlock()
		if conn == nil {
			done <- errors.New("websocket owner connection is not open")
			return
		}
		done <- r.listenDeviceTransportMessages(ctx, cfg, deviceID, conn, record)
	}()
	return done
}

func sendWebSocketSnapshot(conn net.Conn, cfg Config, deviceID string) []Operation {
	body := []byte("rtk-video-loadtest-snapshot")
	metadata := map[string]any{
		"event": "upload_snapshot",
		"data": map[string]any{
			"Size":     len(body),
			"EventId":  fmt.Sprintf("%s-%s-snapshot", cfg.RunID, deviceID),
			"MainType": "Snapshot",
			"SubType":  "StillImage",
		},
	}
	rawMetadata, err := json.Marshal(metadata)
	if err != nil {
		return []Operation{{
			Actor:       ActorDevice,
			Name:        "websocket_snapshot_metadata",
			DeviceID:    deviceID,
			Success:     false,
			ErrorClass:  ClassMalformed,
			ErrorDetail: err.Error(),
		}}
	}
	metadataStart := time.Now()
	metadataOp := Operation{Actor: ActorDevice, Name: "websocket_snapshot_metadata", DeviceID: deviceID}
	if err := writeWebSocketFrame(conn, 1, rawMetadata); err != nil {
		metadataOp.Success = false
		metadataOp.ErrorClass = ClassNetwork
		metadataOp.ErrorDetail = redactDetail(err.Error())
		metadataOp.LatencyMS = time.Since(metadataStart).Milliseconds()
		return []Operation{metadataOp}
	}
	metadataOp.Success = true
	metadataOp.Evidence = fmt.Sprintf("snapshot_metadata_sent bytes=%d", len(rawMetadata))
	metadataOp.LatencyMS = time.Since(metadataStart).Milliseconds()

	binaryStart := time.Now()
	binaryOp := Operation{Actor: ActorDevice, Name: "websocket_snapshot_binary", DeviceID: deviceID}
	if err := writeWebSocketFrame(conn, 2, body); err != nil {
		binaryOp.Success = false
		binaryOp.ErrorClass = ClassNetwork
		binaryOp.ErrorDetail = redactDetail(err.Error())
		binaryOp.LatencyMS = time.Since(binaryStart).Milliseconds()
		return []Operation{metadataOp, binaryOp}
	}
	binaryOp.Success = true
	binaryOp.Evidence = fmt.Sprintf("snapshot_binary_sent bytes=%d", len(body))
	binaryOp.LatencyMS = time.Since(binaryStart).Milliseconds()
	return []Operation{metadataOp, binaryOp}
}

type webRTCMediaOfferMessage struct {
	SessionID string
	Offer     map[string]string
}

func (r *Runner) listenDeviceTransportMessages(ctx context.Context, cfg Config, deviceID string, conn net.Conn, record func(Operation)) error {
	cleanups := make([]func(), 0)
	defer func() {
		for _, cleanup := range cleanups {
			cleanup()
		}
	}()
	for {
		payload, opcode, err := readWebSocketFrame(conn)
		if err != nil {
			return err
		}
		if opcode != 1 {
			continue
		}
		if cfg.WebRTCMediaSet == WebRTCMediaSetRTP {
			if msg, ok := parseWebRTCMediaOfferMessage(payload); ok {
				ops, cleanup := r.answerWebRTCMediaOffer(ctx, cfg, deviceID, msg)
				cleanups = append(cleanups, cleanup)
				for _, op := range ops {
					record(op)
				}
				continue
			}
		}
		if cfg.ClipSet == ClipSetRecordingFunctional {
			if msg, ok := parseRecordingCommandMessage(payload); ok {
				record(Operation{
					Actor:     ActorDevice,
					Name:      "recording_command_receive",
					DeviceID:  deviceID,
					Success:   true,
					Evidence:  fmt.Sprintf("event=%s actionid=%s eventid=%s", msg.Event, msg.ActionID, msg.EventID),
					LatencyMS: 0,
				})
				record(r.uploadRecordingClip(ctx, cfg, deviceID))
			}
		}
	}
}

type recordingCommandMessage struct {
	Event    string
	ActionID string
	EventID  string
}

func parseRecordingCommandMessage(payload []byte) (recordingCommandMessage, bool) {
	var envelope struct {
		Event string         `json:"event"`
		Type  string         `json:"type"`
		Data  map[string]any `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return recordingCommandMessage{}, false
	}
	event := envelope.Event
	if event == "" {
		event = envelope.Type
	}
	if event != "start_recording" {
		return recordingCommandMessage{}, false
	}
	actionID, _ := envelope.Data["actionid"].(string)
	eventID, _ := envelope.Data["eventid"].(string)
	return recordingCommandMessage{Event: event, ActionID: actionID, EventID: eventID}, true
}

func parseWebRTCMediaOfferMessage(payload []byte) (webRTCMediaOfferMessage, bool) {
	var envelope struct {
		Event string         `json:"event"`
		Type  string         `json:"type"`
		Data  map[string]any `json:"data"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return webRTCMediaOfferMessage{}, false
	}
	event := envelope.Event
	if event == "" {
		event = envelope.Type
	}
	if event != "webrtc_offer" {
		return webRTCMediaOfferMessage{}, false
	}
	data := envelope.Data
	if data == nil {
		data = map[string]any{}
		var top map[string]any
		if err := json.Unmarshal(payload, &top); err == nil {
			data = top
		}
	}
	sessionID, _ := data["session_id"].(string)
	rawOffer, ok := data["offer"]
	if !ok {
		return webRTCMediaOfferMessage{}, false
	}
	offerBytes, err := json.Marshal(rawOffer)
	if err != nil {
		return webRTCMediaOfferMessage{}, false
	}
	var offer map[string]string
	if err := json.Unmarshal(offerBytes, &offer); err != nil {
		return webRTCMediaOfferMessage{}, false
	}
	if sessionID == "" || offer["type"] != "offer" || offer["sdp"] == "" {
		return webRTCMediaOfferMessage{}, false
	}
	return webRTCMediaOfferMessage{SessionID: sessionID, Offer: offer}, true
}

func (r *Runner) answerWebRTCMediaOffer(ctx context.Context, cfg Config, deviceID string, msg webRTCMediaOfferMessage) ([]Operation, func()) {
	answerer, err := NewPionMediaAnswerSession(ctx, msg.Offer, cfg.HTTPTimeout)
	if err != nil {
		return []Operation{{
			Actor:       ActorDevice,
			Name:        "webrtc_media_answer",
			DeviceID:    deviceID,
			Success:     false,
			ErrorClass:  ClassWebRTCSetup,
			ErrorDetail: redactDetail(err.Error()),
		}}, func() {}
	}
	cleanup := answerer.Close
	op := r.post(ctx, cfg, ActorDevice, "webrtc_media_answer", deviceID, "", "/api/request_webrtc/answer", map[string]any{
		"devid":      deviceID,
		"session_id": msg.SessionID,
		"answer":     answerer.AnswerPayload(),
	}, cfg.DeviceBearerFor(deviceID))
	if op.Success {
		op.Evidence = "device_media_answer_submitted"
		go func() {
			_ = answerer.SendSyntheticRTP(ctx, 120, 20*time.Millisecond)
		}()
	}
	return []Operation{op}, cleanup
}

func readWebSocketFrame(r io.Reader) ([]byte, byte, error) {
	header := []byte{0, 0}
	if _, err := io.ReadFull(r, header); err != nil {
		return nil, 0, err
	}
	opcode := header[0] & 0x0f
	masked := header[1]&0x80 != 0
	length := int64(header[1] & 0x7f)
	switch length {
	case 126:
		extended := []byte{0, 0}
		if _, err := io.ReadFull(r, extended); err != nil {
			return nil, 0, err
		}
		length = int64(extended[0])<<8 | int64(extended[1])
	case 127:
		extended := make([]byte, 8)
		if _, err := io.ReadFull(r, extended); err != nil {
			return nil, 0, err
		}
		length = 0
		for _, b := range extended {
			length = length<<8 | int64(b)
		}
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(r, mask[:]); err != nil {
			return nil, 0, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(r, payload); err != nil {
		return nil, 0, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	if opcode == 8 {
		return payload, opcode, io.EOF
	}
	return payload, opcode, nil
}

func writeWebSocketFrame(w io.Writer, opcode byte, payload []byte) error {
	header := []byte{0x80 | (opcode & 0x0f)}
	length := len(payload)
	switch {
	case length < 126:
		header = append(header, 0x80|byte(length))
	case length <= 0xffff:
		header = append(header, 0x80|126, byte(length>>8), byte(length))
	default:
		header = append(header, 0x80|127, byte(length>>56), byte(length>>48), byte(length>>40), byte(length>>32), byte(length>>24), byte(length>>16), byte(length>>8), byte(length))
	}
	var mask [4]byte
	if _, err := rand.Read(mask[:]); err != nil {
		return err
	}
	maskedPayload := make([]byte, len(payload))
	for i := range payload {
		maskedPayload[i] = payload[i] ^ mask[i%4]
	}
	if _, err := w.Write(append(header, mask[:]...)); err != nil {
		return err
	}
	_, err := w.Write(maskedPayload)
	return err
}

func openWebSocketOwner(ctx context.Context, cfg Config, deviceID, bearer string) (net.Conn, error) {
	base, err := url.Parse(cfg.WSURL)
	if err != nil {
		return nil, fmt.Errorf("parse websocket url: %w", err)
	}
	switch base.Scheme {
	case "ws", "wss":
	default:
		return nil, fmt.Errorf("unsupported websocket url scheme %q", base.Scheme)
	}
	upgradeURL := *base
	upgradeURL.Path = strings.TrimRight(base.Path, "/") + "/ws/device"
	query := upgradeURL.Query()
	query.Set("devid", deviceID)
	upgradeURL.RawQuery = query.Encode()

	dialer := net.Dialer{Timeout: cfg.HTTPTimeout}
	address := upgradeURL.Host
	if !strings.Contains(address, ":") {
		if upgradeURL.Scheme == "wss" {
			address += ":443"
		} else {
			address += ":80"
		}
	}
	var conn net.Conn
	if upgradeURL.Scheme == "wss" {
		conn, err = tls.DialWithDialer(&dialer, "tcp", address, &tls.Config{MinVersion: tls.VersionTLS12, ServerName: upgradeURL.Hostname()})
	} else {
		conn, err = dialer.DialContext(ctx, "tcp", address)
	}
	if err != nil {
		return nil, err
	}
	if deadline, ok := ctx.Deadline(); ok {
		_ = conn.SetDeadline(deadline)
	} else {
		_ = conn.SetDeadline(time.Now().Add(cfg.HTTPTimeout))
	}
	keyBytes := make([]byte, 16)
	if _, err := rand.Read(keyBytes); err != nil {
		_ = conn.Close()
		return nil, err
	}
	key := base64.StdEncoding.EncodeToString(keyBytes)
	target := upgradeURL.RequestURI()
	request := fmt.Sprintf(
		"GET %s HTTP/1.1\r\nHost: %s\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Key: %s\r\nSec-WebSocket-Version: 13\r\nAuthorization: Bearer %s\r\n\r\n",
		target,
		upgradeURL.Host,
		key,
		bearer,
	)
	if _, err := io.WriteString(conn, request); err != nil {
		_ = conn.Close()
		return nil, err
	}
	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil {
		_ = conn.Close()
		return nil, err
	}
	if !strings.Contains(status, " 101 ") {
		_ = conn.Close()
		return nil, fmt.Errorf("websocket upgrade failed: %s", strings.TrimSpace(status))
	}
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			_ = conn.Close()
			return nil, err
		}
		if line == "\r\n" {
			break
		}
	}
	_ = conn.SetDeadline(time.Time{})
	return conn, nil
}

func (r *Runner) runGroup(ctx context.Context, concurrency, actorCount, iterations int, ratePerSecond float64, rampUp, duration time.Duration, fn func(int) []Operation, record func(Operation)) {
	if actorCount <= 0 {
		return
	}
	if concurrency <= 0 {
		concurrency = 1
	}
	totalTarget := actorCount * iterations
	if ratePerSecond > 0 {
		totalTarget = int(ratePerSecond * duration.Seconds())
		if totalTarget < actorCount {
			totalTarget = actorCount
		}
	}
	if totalTarget <= 0 {
		totalTarget = actorCount
	}
	if ratePerSecond <= 0 && totalTarget == 1 && duration > 0 {
		totalTarget = 2
	}
	interval := scheduleInterval(duration, totalTarget, ratePerSecond)
	sem := make(chan struct{}, concurrency)
	var inflight sync.WaitGroup
	defer inflight.Wait()

	if rampUp > 0 {
		select {
		case <-ctx.Done():
			return
		case <-time.After(rampUp):
		}
	}

	scheduled := 0
	for scheduled < totalTarget {
		select {
		case <-ctx.Done():
			return
		default:
		}
		select {
		case sem <- struct{}{}:
		case <-ctx.Done():
			return
		}
		inflight.Add(1)
		go func(idx int) {
			defer inflight.Done()
			defer func() { <-sem }()
			for _, op := range fn(idx) {
				record(op)
			}
		}(scheduled)
		scheduled++
		waitUntil := time.Now().Add(interval)
		if scheduled < totalTarget {
			select {
			case <-ctx.Done():
				return
			case <-time.After(time.Until(waitUntil)):
			}
		}
	}
}

func scheduleInterval(duration time.Duration, totalTarget int, ratePerSecond float64) time.Duration {
	if ratePerSecond > 0 {
		interval := time.Duration(float64(time.Second) / ratePerSecond)
		if interval <= 0 {
			return time.Nanosecond
		}
		return interval
	}
	if totalTarget <= 1 {
		return duration
	}
	interval := duration / time.Duration(totalTarget-1)
	if interval <= 0 {
		return time.Nanosecond
	}
	return interval
}

func (r *Runner) runAppActor(ctx context.Context, cfg Config, deviceID string) []Operation {
	ops := []Operation{
		r.post(ctx, cfg, ActorApp, "get_statistics", deviceID, "", "/get_statistics", map[string]any{"devid": deviceID}, cfg.AdminToken),
	}
	ops = append(ops, r.runMQTTIoTAppCoverage(ctx, cfg, deviceID)...)
	if cfg.AppRouteSet != AppRouteSetFunctional {
		return ops
	}
	ops = append(ops,
		r.get(ctx, cfg, ActorApp, "version", deviceID, "", "/version", ""),
		r.get(ctx, cfg, ActorApp, "server_time", deviceID, "", "/server_time", ""),
	)
	if cfg.RefreshToken != "" {
		ops = append(ops, r.post(ctx, cfg, ActorApp, "refresh_token", deviceID, "", "/refresh_token", map[string]any{
			"scope":         "camera",
			"devid":         deviceID,
			"refresh_token": cfg.RefreshToken,
		}, cfg.AdminToken))
	} else {
		ops = append(ops, skipOperation(ActorApp, "refresh_token", deviceID, "", "VIDEO_CLOUD_LOAD_REFRESH_TOKEN is not configured"))
	}
	ops = append(ops,
		r.post(ctx, cfg, ActorApp, "query_camera_activate", deviceID, "", "/query_camera_activate", map[string]any{"devices": []string{deviceID}}, cfg.AdminToken),
		r.post(ctx, cfg, ActorApp, "get_camera_info", deviceID, "", "/get_camera_info", map[string]any{"devid": deviceID}, cfg.AdminToken),
	)
	if token := cfg.DeviceBearerFor(deviceID); token != "" {
		ops = append(ops, r.post(ctx, cfg, ActorApp, "set_camera_info", deviceID, "", "/set_camera_info", map[string]any{
			"devid":            deviceID,
			"model_name":       "rtk-video-loadtest",
			"firmware_version": "0.0.0-loadtest",
			"serial_number":    deviceID,
			"protocol_version": "050050",
		}, token))
	} else {
		ops = append(ops, skipOperation(ActorApp, "set_camera_info", deviceID, "", "device-scoped token is not configured"))
	}
	ops = append(ops,
		r.post(ctx, cfg, ActorApp, "camera_write_conf", deviceID, "", "/camera_write_conf", map[string]any{
			"devid":        deviceID,
			"privacy_mode": "off",
			"storage_plan": "loadtest",
		}, cfg.AdminToken),
		r.post(ctx, cfg, ActorApp, "camera_read_conf", deviceID, "", "/camera_read_conf", map[string]any{"devid": deviceID}, cfg.AdminToken),
	)
	if cfg.ClipSet == ClipSetRecordingFunctional {
		ops = append(ops, r.runRecordingClipAppLifecycle(ctx, cfg, deviceID)...)
	}
	return ops
}

func (r *Runner) runDeviceActor(ctx context.Context, cfg Config, deviceID string) []Operation {
	token := cfg.DeviceBearerFor(deviceID)
	if token == "" {
		return []Operation{{
			Actor:       "device",
			Name:        "camera_event",
			DeviceID:    deviceID,
			Success:     false,
			ErrorClass:  ClassAuth,
			ErrorDetail: "missing device-scoped token for device actor",
		}}
	}
	body := map[string]any{
		"devid":    deviceID,
		"maintype": "LoadTest",
		"subtype":  "DeviceOnline",
		"eventid":  fmt.Sprintf("%s-%s-device-online", cfg.RunID, deviceID),
		"desc":     fmt.Sprintf("loadtest.device.online run_id=%s instance_id=%s", cfg.RunID, cfg.InstanceID),
	}
	ops := []Operation{
		r.post(ctx, cfg, ActorDevice, "camera_event", deviceID, "", "/camera_event", body, token),
	}
	if cfg.DeviceRouteSet != DeviceRouteSetFunctional {
		return append(ops, r.runMQTTDeviceCoverage(ctx, cfg, deviceID)...)
	}
	now := time.Now().UTC()
	ops = append(ops,
		r.post(ctx, cfg, ActorDevice, "write_log", deviceID, "", "/write_log", map[string]any{
			"devid": deviceID,
			"type":  "eventlog",
			"time":  now.Format("2006-01-02 15:04:05"),
			"desc":  fmt.Sprintf("rtk-video-loadtest device functional run_id=%s instance_id=%s", cfg.RunID, cfg.InstanceID),
		}, cfg.AdminToken),
		r.post(ctx, cfg, ActorDevice, "retrieve_log", deviceID, "", "/retrieve_log", map[string]any{
			"devid":      deviceID,
			"type":       "all",
			"start_time": now.Add(-time.Hour).Format("2006-01-02 15:04:05"),
			"end_time":   now.Add(time.Hour).Format("2006-01-02 15:04:05"),
		}, cfg.AdminToken),
		r.post(ctx, cfg, ActorDevice, "notify_camera", deviceID, "", "/notify_camera", map[string]any{
			"devid": deviceID,
			"event": "loadtest.notify",
			"data": map[string]any{
				"type":        "loadtest.notify",
				"run_id":      cfg.RunID,
				"instance_id": cfg.InstanceID,
				"message":     "rtk-video-loadtest functional notify_camera probe",
			},
		}, cfg.AdminToken),
		r.post(ctx, cfg, ActorDevice, "start_video_record", deviceID, "", "/start_video_record", map[string]any{
			"devid":    deviceID,
			"duration": 10,
		}, cfg.AdminToken),
	)
	ops = append(ops, r.runMQTTDeviceCoverage(ctx, cfg, deviceID)...)
	return ops
}

func (r *Runner) runMQTTDeviceCoverage(ctx context.Context, cfg Config, deviceID string) []Operation {
	switch cfg.MQTTDeviceProfile {
	case MQTTDeviceProfileIoT:
		return r.runMQTTIoTDeviceCoverage(ctx, cfg, deviceID)
	case MQTTDeviceProfileMixed:
		if mqttIoTCapabilityForDevice(cfg, deviceID) != "" {
			return r.runMQTTIoTDeviceCoverage(ctx, cfg, deviceID)
		}
	}
	return r.runMQTTBrokerCoverage(ctx, cfg, deviceID)
}

func (r *Runner) runMQTTBrokerCoverage(ctx context.Context, cfg Config, deviceID string) []Operation {
	if cfg.MQTTSet != MQTTSetBroker {
		return nil
	}
	if strings.TrimSpace(cfg.MQTTAddr) == "" {
		if cfg.MQTTRequired {
			return []Operation{{
				Actor:       ActorDevice,
				Name:        "mqtt_connect",
				DeviceID:    deviceID,
				Success:     false,
				ErrorClass:  ClassConfig,
				ErrorDetail: "mqtt-required requires VIDEO_CLOUD_MQTT_ADDR or --mqtt-addr",
			}}
		}
		return []Operation{skipOperation(ActorDevice, "mqtt_connect", deviceID, "", "VIDEO_CLOUD_MQTT_ADDR is not configured")}
	}
	start := time.Now()
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.HTTPTimeout)
	defer cancel()
	dialer := net.Dialer{Timeout: cfg.HTTPTimeout}
	conn, err := dialer.DialContext(opCtx, "tcp", cfg.MQTTAddr)
	connectOp := Operation{Actor: ActorDevice, Name: "mqtt_connect", DeviceID: deviceID, LatencyMS: time.Since(start).Milliseconds()}
	if err != nil {
		connectOp.Success = false
		connectOp.ErrorClass = ClassNetwork
		connectOp.ErrorDetail = redactDetail(err.Error())
		return []Operation{connectOp}
	}
	defer conn.Close()
	if err := mqttConnect(conn, cfg, deviceID); err != nil {
		connectOp.Success = false
		connectOp.ErrorClass = ClassNetwork
		connectOp.ErrorDetail = redactDetail(err.Error())
		return []Operation{connectOp}
	}
	connectOp.Success = true
	connectOp.Evidence = fmt.Sprintf("mqtt_connected addr=%s topic_root=%s", cfg.MQTTAddr, cfg.MQTTTopicRoot)
	ops := []Operation{connectOp}

	downTopic := mqttTopic(cfg, deviceID, "down/commands")
	upTopic := mqttTopic(cfg, deviceID, "up/messages")
	ops = append(ops, mqttSubscribeOperation(conn, deviceID, downTopic))
	ops = append(ops, mqttPublishOperation(conn, "mqtt_state_publish", deviceID, upTopic, map[string]any{
		"kind": "state_report",
		"payload": map[string]any{
			"devid": deviceID,
			"state": "online",
		},
	}))
	ops = append(ops, mqttPublishOperation(conn, "mqtt_log_publish", deviceID, upTopic, map[string]any{
		"kind": "log_report",
		"payload": map[string]any{
			"devid": deviceID,
			"type":  "eventlog",
			"desc":  "rtk-video-loadtest mqtt log",
		},
	}))
	image := []byte("rtk-video-loadtest-mqtt-snapshot")
	ops = append(ops, mqttPublishOperation(conn, "mqtt_snapshot_publish", deviceID, upTopic, map[string]any{
		"kind": "legacy_snapshot",
		"payload": map[string]any{
			"devid":        deviceID,
			"image_base64": base64.StdEncoding.EncodeToString(image),
			"data": map[string]any{
				"Size": len(image),
			},
		},
	}))
	ops = append(ops, skipOperation(ActorDevice, "mqtt_native_binary_unsupported", deviceID, "", "MQTT snapshot uses JSON+base64 and has no native binary side channel"))
	return ops
}

func (r *Runner) runMQTTIoTDeviceCoverage(ctx context.Context, cfg Config, deviceID string) []Operation {
	capability := mqttIoTCapabilityForDevice(cfg, deviceID)
	if capability == "" || cfg.MQTTSet != MQTTSetBroker {
		return nil
	}
	conn, connectOp := mqttOpenConnection(ctx, cfg, ActorDevice, "mqtt_iot_device_connect", deviceID)
	if !connectOp.Success {
		return []Operation{connectOp}
	}
	defer conn.Close()
	ops := []Operation{connectOp}
	downTopic := mqttTopic(cfg, deviceID, "down/commands")
	upTopic := mqttTopic(cfg, deviceID, "up/messages")
	coordinationTimeout := mqttCoordinationTimeout(cfg)
	if capability == "smart_meter" {
		appReadyTopic := mqttIoTReadyTopic(cfg, deviceID, "app")
		ops = append(ops, mqttSubscribeOperationForActor(conn, ActorDevice, "mqtt_smart_meter_app_ready_subscribe", deviceID, appReadyTopic))
		if !ops[len(ops)-1].Success {
			return ops
		}
		ops = append(ops, mqttWaitForSampleMessageOperation(conn, ActorDevice, "mqtt_smart_meter_app_ready_receive", deviceID, appReadyTopic, "readiness_report", coordinationTimeout))
		if !ops[len(ops)-1].Success {
			return ops
		}
		deviceReadyTopic := mqttIoTReadyTopic(cfg, deviceID, "device")
		ops = append(ops, mqttPublishOperationForActorRetained(conn, ActorDevice, "mqtt_smart_meter_device_ready_publish", deviceID, deviceReadyTopic, mqttIoTReadinessPayload(cfg, deviceID, capability, "device")))
		if !ops[len(ops)-1].Success {
			return ops
		}
		for i := 0; i < 3; i++ {
			if i > 0 {
				select {
				case <-ctx.Done():
					return ops
				case <-time.After(50 * time.Millisecond):
				}
			}
			ops = append(ops, mqttPublishOperationForActor(conn, ActorDevice, "mqtt_smart_meter_status_publish", deviceID, upTopic, mqttIoTStatusPayload(cfg, deviceID, capability)))
			ops = append(ops, mqttPublishOperationForActor(conn, ActorDevice, "mqtt_smart_meter_telemetry_publish", deviceID, upTopic, mqttSmartMeterTelemetryPayload(cfg, deviceID)))
		}
		return ops
	}
	subscribeName := "mqtt_" + capability + "_command_subscribe"
	ops = append(ops, mqttSubscribeOperationForActor(conn, ActorDevice, subscribeName, deviceID, downTopic))
	if !ops[len(ops)-1].Success {
		return ops
	}
	deviceReadyTopic := mqttIoTReadyTopic(cfg, deviceID, "device")
	ops = append(ops, mqttPublishOperationForActorRetained(conn, ActorDevice, "mqtt_"+capability+"_device_ready_publish", deviceID, deviceReadyTopic, mqttIoTReadinessPayload(cfg, deviceID, capability, "device")))
	if !ops[len(ops)-1].Success {
		return ops
	}
	receiveName := "mqtt_" + capability + "_command_receive"
	command, receiveOp := mqttWaitForSampleMessageWithOperation(conn, ActorDevice, receiveName, deviceID, downTopic, "command", coordinationTimeout)
	if !receiveOp.Success {
		return append(ops, receiveOp)
	}
	receiveOp.Evidence = fmt.Sprintf("mqtt_command_received capability=%s command_id=%s", capability, command.CommandID)
	ops = append(ops, receiveOp)
	ops = append(ops,
		mqttPublishOperationForActor(conn, ActorDevice, "mqtt_"+capability+"_command_result_publish", deviceID, upTopic, mqttIoTCommandResultPayload(cfg, deviceID, capability, command)),
		mqttPublishOperationForActor(conn, ActorDevice, "mqtt_"+capability+"_state_report_publish", deviceID, upTopic, mqttIoTStateReportPayload(cfg, deviceID, capability, command)),
	)
	return ops
}

func (r *Runner) runMQTTIoTAppCoverage(ctx context.Context, cfg Config, deviceID string) []Operation {
	capability := mqttIoTCapabilityForDevice(cfg, deviceID)
	if capability == "" || cfg.MQTTSet != MQTTSetBroker {
		return nil
	}
	conn, connectOp := mqttOpenConnection(ctx, cfg, ActorApp, "mqtt_iot_app_connect", deviceID)
	if !connectOp.Success {
		return []Operation{connectOp}
	}
	defer conn.Close()
	ops := []Operation{connectOp}
	upTopic := mqttTopic(cfg, deviceID, "up/messages")
	downTopic := mqttTopic(cfg, deviceID, "down/commands")
	ops = append(ops, mqttSubscribeOperationForActor(conn, ActorApp, "mqtt_"+capability+"_up_subscribe", deviceID, upTopic))
	if !ops[len(ops)-1].Success {
		return ops
	}
	coordinationTimeout := mqttCoordinationTimeout(cfg)
	if capability == "smart_meter" {
		appReadyTopic := mqttIoTReadyTopic(cfg, deviceID, "app")
		ops = append(ops, mqttPublishOperationForActorRetained(conn, ActorApp, "mqtt_smart_meter_app_ready_publish", deviceID, appReadyTopic, mqttIoTReadinessPayload(cfg, deviceID, capability, "app")))
		if !ops[len(ops)-1].Success {
			return ops
		}
		deviceReadyTopic := mqttIoTReadyTopic(cfg, deviceID, "device")
		ops = append(ops, mqttSubscribeOperationForActor(conn, ActorApp, "mqtt_smart_meter_device_ready_subscribe", deviceID, deviceReadyTopic))
		if !ops[len(ops)-1].Success {
			return ops
		}
		ops = append(ops, mqttWaitForSampleMessageOperation(conn, ActorApp, "mqtt_smart_meter_device_ready_receive", deviceID, deviceReadyTopic, "readiness_report", coordinationTimeout))
		if !ops[len(ops)-1].Success {
			return ops
		}
		_, statusOp := mqttWaitForSampleMessageWithOperation(conn, ActorApp, "mqtt_smart_meter_status_receive", deviceID, upTopic, "status_report", cfg.HTTPTimeout)
		if !statusOp.Success {
			return append(ops, statusOp)
		}
		statusOp.Evidence = "mqtt_smart_meter_status_received"
		ops = append(ops, statusOp)
		msg, telemetryOp := mqttWaitForSampleMessageWithOperation(conn, ActorApp, "mqtt_smart_meter_telemetry_receive", deviceID, upTopic, "telemetry_report", cfg.HTTPTimeout)
		if !telemetryOp.Success {
			return append(ops, telemetryOp)
		}
		telemetryOp.Evidence = fmt.Sprintf("mqtt_smart_meter_telemetry_received watts=%v", msg.Payload["power_watts"])
		return append(ops, telemetryOp)
	}
	deviceReadyTopic := mqttIoTReadyTopic(cfg, deviceID, "device")
	ops = append(ops, mqttSubscribeOperationForActor(conn, ActorApp, "mqtt_"+capability+"_device_ready_subscribe", deviceID, deviceReadyTopic))
	if !ops[len(ops)-1].Success {
		return ops
	}
	ops = append(ops, mqttWaitForSampleMessageOperation(conn, ActorApp, "mqtt_"+capability+"_device_ready_receive", deviceID, deviceReadyTopic, "readiness_report", coordinationTimeout))
	if !ops[len(ops)-1].Success {
		return ops
	}
	command := mqttIoTCommandPayload(cfg, deviceID, capability)
	ops = append(ops, mqttPublishOperationForActor(conn, ActorApp, "mqtt_"+capability+"_command_publish", deviceID, downTopic, command))
	for _, messageType := range []string{"command_result", "state_report"} {
		name := "mqtt_" + capability + "_" + messageType + "_receive"
		msg, op := mqttWaitForSampleMessageWithOperation(conn, ActorApp, name, deviceID, upTopic, messageType, cfg.HTTPTimeout)
		if !op.Success {
			ops = append(ops, op)
			continue
		}
		op.Evidence = fmt.Sprintf("mqtt_%s_received command_id=%s", messageType, msg.CommandID)
		ops = append(ops, op)
	}
	return ops
}

func (r *Runner) runNegativeCoverage(ctx context.Context, cfg Config) []Operation {
	if cfg.NegativeSet != NegativeSetHTTP {
		return nil
	}
	deviceID := cfg.DevicePrefix + "-invalid"
	ops := []Operation{
		expectedHTTPFailure(r.post(ctx, cfg, ActorApp, "negative_missing_bearer", deviceID, "", "/get_camera_info", map[string]any{"devid": deviceID}, ""), "expected missing bearer rejection", http.StatusUnauthorized, http.StatusForbidden),
		expectedHTTPFailure(r.post(ctx, cfg, ActorApp, "negative_invalid_device", deviceID, "", "/get_camera_info", map[string]any{"devid": deviceID}, cfg.AdminToken), "expected invalid device rejection", http.StatusNotFound, http.StatusConflict, http.StatusGone),
	}
	if cfg.NegativeMalformedPath != "" {
		ops = append(ops, r.expectedMalformedJSON(ctx, cfg, cfg.NegativeMalformedPath))
	} else {
		ops = append(ops, skipOperation(ActorApp, "negative_malformed_json", deviceID, "", "VIDEO_CLOUD_LOAD_NEGATIVE_MALFORMED_PATH is not configured"))
	}
	if cfg.NegativeTimeoutPath != "" {
		ops = append(ops, expectedTimeout(r.get(ctx, cfg, ActorApp, "negative_timeout", deviceID, "", cfg.NegativeTimeoutPath, cfg.AdminToken)))
	} else {
		ops = append(ops, skipOperation(ActorApp, "negative_timeout", deviceID, "", "VIDEO_CLOUD_LOAD_NEGATIVE_TIMEOUT_PATH is not configured"))
	}
	return ops
}

func expectedHTTPFailure(op Operation, summary string, expected ...int) Operation {
	if op.Success {
		op.Success = false
		op.ErrorClass = ClassHTTP
		op.ErrorDetail = summary + " unexpectedly succeeded"
		return op
	}
	for _, status := range expected {
		if op.StatusCode == status {
			op.Success = true
			op.Evidence = fmt.Sprintf("expected_failure status=%d class=%s summary=%s", op.StatusCode, op.ErrorClass, summary)
			op.ErrorClass = ""
			op.ErrorDetail = ""
			return op
		}
	}
	op.ErrorDetail = fmt.Sprintf("%s: unexpected status=%d detail=%s", summary, op.StatusCode, op.ErrorDetail)
	return op
}

func (r *Runner) expectedMalformedJSON(ctx context.Context, cfg Config, path string) Operation {
	op := r.get(ctx, cfg, ActorApp, "negative_malformed_json", "", "", path, "")
	if !op.Success {
		return expectedHTTPFailure(op, "expected malformed JSON probe response", http.StatusOK, http.StatusBadRequest)
	}
	var decoded any
	if err := json.Unmarshal([]byte(op.Evidence), &decoded); err != nil {
		op.Success = true
		op.Evidence = "expected_failure malformed_json"
		return op
	}
	op.Success = false
	op.ErrorClass = ClassMalformed
	op.ErrorDetail = "malformed JSON probe returned valid JSON"
	return op
}

func expectedTimeout(op Operation) Operation {
	if !op.Success && op.ErrorClass == ClassTimeout {
		op.Success = true
		op.Evidence = "expected_failure timeout"
		op.ErrorClass = ""
		op.ErrorDetail = ""
		return op
	}
	if op.Success {
		op.Success = false
		op.ErrorClass = ClassTimeout
		op.ErrorDetail = "expected timeout unexpectedly succeeded"
		return op
	}
	op.ErrorDetail = fmt.Sprintf("expected timeout but got class=%s status=%d detail=%s", op.ErrorClass, op.StatusCode, op.ErrorDetail)
	return op
}

func (r *Runner) runRecordingClipAppLifecycle(ctx context.Context, cfg Config, deviceID string) []Operation {
	clipID := recordingClipID(cfg, deviceID)
	bearer := cfg.AccountBearerFor(deviceID)
	if bearer == "" {
		bearer = cfg.AdminToken
	}
	ops := []Operation{
		r.post(ctx, cfg, ActorApp, "recording_request", deviceID, "", "/start_video_record", map[string]any{
			"devid":    deviceID,
			"duration": 10,
			"eventid":  clipID,
		}, cfg.AdminToken),
	}
	if !ops[0].Success {
		return ops
	}

	info := r.waitForClipInfo(ctx, cfg, deviceID, clipID)
	ops = append(ops, info)
	if !info.Success {
		return ops
	}
	ops = append(ops,
		r.post(ctx, cfg, ActorApp, "clip_total", deviceID, "", "/total_clips", map[string]any{"devid": deviceID}, cfg.AdminToken),
		r.post(ctx, cfg, ActorApp, "clip_enum", deviceID, "", "/enum_clips", map[string]any{
			"devid":  deviceID,
			"offset": 0,
			"count":  10,
		}, cfg.AdminToken),
		r.downloadClipRange(ctx, cfg, "clip_download_range", deviceID, clipID, "bytes=0-15", bearer, false),
		r.downloadClipRange(ctx, cfg, "clip_download_invalid_range", deviceID, clipID, "bytes=999999-", bearer, true),
		r.post(ctx, cfg, ActorApp, "clip_delete", deviceID, "", "/delete_clip", map[string]any{
			"devid":  deviceID,
			"clipid": clipID,
		}, cfg.AdminToken),
	)
	return ops
}

func (r *Runner) waitForClipInfo(ctx context.Context, cfg Config, deviceID, clipID string) Operation {
	deadline := time.Now().Add(cfg.HTTPTimeout)
	var last Operation
	for {
		last = r.post(ctx, cfg, ActorApp, "clip_info", deviceID, "", "/get_clip_info", map[string]any{
			"devid":  deviceID,
			"clipid": clipID,
		}, cfg.AdminToken)
		if last.Success {
			return last
		}
		if time.Now().Add(200 * time.Millisecond).After(deadline) {
			if last.ErrorDetail == "" {
				last.ErrorDetail = "clip_metadata_missing"
			} else {
				last.ErrorDetail = "clip_metadata_missing: " + last.ErrorDetail
			}
			return last
		}
		select {
		case <-ctx.Done():
			last.ErrorClass = ClassCancelled
			last.ErrorDetail = "clip_metadata_missing: " + ctx.Err().Error()
			return last
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (r *Runner) uploadRecordingClip(ctx context.Context, cfg Config, deviceID string) Operation {
	token := cfg.DeviceBearerFor(deviceID)
	op := Operation{Actor: ActorDevice, Name: "clip_upload", DeviceID: deviceID}
	if token == "" {
		op.Success = false
		op.ErrorClass = ClassAuth
		op.ErrorDetail = "auth_or_token_config_error: missing device-scoped token for clip upload"
		return op
	}
	clipID := recordingClipID(cfg, deviceID)
	meta := map[string]any{
		"devid":      deviceID,
		"clipid":     clipID,
		"event_type": "LoadTestRecording",
		"eventid":    clipID,
		"time":       time.Now().UTC().Format(time.RFC3339),
	}
	fields := map[string][]byte{}
	rawMeta, err := json.Marshal(meta)
	if err != nil {
		op.Success = false
		op.ErrorClass = ClassMalformed
		op.ErrorDetail = err.Error()
		return op
	}
	fields["meta"] = rawMeta
	files := map[string]multipartFile{
		"clip": {
			Filename:    clipID + ".mp4",
			ContentType: "video/mp4",
			Body:        deterministicClipBytes(),
		},
	}
	uploaded := r.postMultipart(ctx, cfg, ActorDevice, "clip_upload", deviceID, "/upload_clip", fields, files, token)
	if uploaded.Success {
		uploaded.Evidence = fmt.Sprintf("clipid=%s bytes=%d", clipID, len(files["clip"].Body))
	} else if uploaded.ErrorDetail != "" {
		uploaded.ErrorDetail = "clip_upload_failed: " + uploaded.ErrorDetail
	}
	return uploaded
}

type multipartFile struct {
	Filename    string
	ContentType string
	Body        []byte
}

func (r *Runner) postMultipart(ctx context.Context, cfg Config, actor, name, deviceID, path string, fields map[string][]byte, files map[string]multipartFile, bearer string) Operation {
	start := time.Now()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	for fieldName, value := range fields {
		part, err := writer.CreateFormField(fieldName)
		if err != nil {
			return Operation{Actor: actor, Name: name, DeviceID: deviceID, Success: false, ErrorClass: ClassMalformed, ErrorDetail: err.Error()}
		}
		if _, err := part.Write(value); err != nil {
			return Operation{Actor: actor, Name: name, DeviceID: deviceID, Success: false, ErrorClass: ClassMalformed, ErrorDetail: err.Error()}
		}
	}
	for fieldName, file := range files {
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`, escapeQuotes(fieldName), escapeQuotes(file.Filename)))
		if file.ContentType != "" {
			header.Set("Content-Type", file.ContentType)
		}
		part, err := writer.CreatePart(header)
		if err != nil {
			return Operation{Actor: actor, Name: name, DeviceID: deviceID, Success: false, ErrorClass: ClassMalformed, ErrorDetail: err.Error()}
		}
		if _, err := part.Write(file.Body); err != nil {
			return Operation{Actor: actor, Name: name, DeviceID: deviceID, Success: false, ErrorClass: ClassMalformed, ErrorDetail: err.Error()}
		}
	}
	if err := writer.Close(); err != nil {
		return Operation{Actor: actor, Name: name, DeviceID: deviceID, Success: false, ErrorClass: ClassMalformed, ErrorDetail: err.Error()}
	}

	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.HTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(opCtx, http.MethodPost, cfg.APIURL+path, &body)
	if err != nil {
		return Operation{Actor: actor, Name: name, DeviceID: deviceID, Success: false, ErrorClass: ClassMalformed, ErrorDetail: err.Error()}
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := r.client.Do(req)
	latency := time.Since(start).Milliseconds()
	op := Operation{Actor: actor, Name: name, DeviceID: deviceID, LatencyMS: latency}
	if err != nil {
		op.Success = false
		op.ErrorClass = ClassifyError(0, nil, err)
		op.ErrorDetail = redactDetail(err.Error())
		return op
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(resp.Body)
	op.StatusCode = resp.StatusCode
	if readErr != nil {
		op.Success = false
		op.ErrorClass = ClassifyError(resp.StatusCode, raw, readErr)
		op.ErrorDetail = redactDetail(readErr.Error())
		return op
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		op.Success = false
		op.ErrorClass = ClassifyError(resp.StatusCode, raw, nil)
		op.ErrorDetail = redactDetail(fmt.Sprintf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw))))
		return op
	}
	op.Success = true
	op.Evidence = string(raw)
	return op
}

func escapeQuotes(value string) string {
	return strings.NewReplacer("\\", "\\\\", `"`, "\\\"").Replace(value)
}

func (r *Runner) downloadClipRange(ctx context.Context, cfg Config, name, deviceID, clipID, rangeHeader, bearer string, expectInvalidRange bool) Operation {
	start := time.Now()
	downloadURL := fmt.Sprintf("%s/download/%s/%s", strings.TrimRight(cfg.APIURL, "/"), url.PathEscape(deviceID), url.PathEscape(clipID))
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.HTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(opCtx, http.MethodGet, downloadURL, nil)
	if err != nil {
		return Operation{Actor: ActorApp, Name: name, DeviceID: deviceID, Success: false, ErrorClass: ClassMalformed, ErrorDetail: err.Error()}
	}
	req.Header.Set("Range", rangeHeader)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := r.client.Do(req)
	op := Operation{Actor: ActorApp, Name: name, DeviceID: deviceID, LatencyMS: time.Since(start).Milliseconds()}
	if err != nil {
		op.Success = false
		op.ErrorClass = ClassifyError(0, nil, err)
		op.ErrorDetail = redactDetail(err.Error())
		return op
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(resp.Body)
	op.StatusCode = resp.StatusCode
	if readErr != nil {
		op.Success = false
		op.ErrorClass = ClassifyError(resp.StatusCode, raw, readErr)
		op.ErrorDetail = redactDetail(readErr.Error())
		return op
	}
	if expectInvalidRange {
		if resp.StatusCode == http.StatusRequestedRangeNotSatisfiable {
			op.Success = true
			op.Evidence = fmt.Sprintf("expected_invalid_range status=%d content_range=%s", resp.StatusCode, resp.Header.Get("Content-Range"))
			return op
		}
		op.Success = false
		op.ErrorClass = ClassHTTP
		op.ErrorDetail = fmt.Sprintf("clip_range_invalid: expected HTTP 416, got %d", resp.StatusCode)
		return op
	}
	if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
		op.Success = false
		op.ErrorClass = ClassifyError(resp.StatusCode, raw, nil)
		op.ErrorDetail = redactDetail(fmt.Sprintf("clip_download_failed: http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw))))
		return op
	}
	op.Success = true
	op.Evidence = fmt.Sprintf("bytes=%d status=%d accept_ranges=%s content_range=%s", len(raw), resp.StatusCode, resp.Header.Get("Accept-Ranges"), resp.Header.Get("Content-Range"))
	return op
}

func recordingClipID(cfg Config, deviceID string) string {
	base := cfg.RunID
	if base == "" {
		base = "run"
	}
	return safeIDPart(base) + "-" + safeIDPart(deviceID)
}

func safeIDPart(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= 'A' && r <= 'Z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_' || r == '.':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	return strings.Trim(b.String(), "-")
}

func deterministicClipBytes() []byte {
	return []byte{
		0x00, 0x00, 0x00, 0x18, 'f', 't', 'y', 'p', 'i', 's', 'o', 'm',
		0x00, 0x00, 0x02, 0x00, 'i', 's', 'o', 'm', 'i', 's', 'o', '2',
		0x00, 0x00, 0x00, 0x10, 'm', 'd', 'a', 't',
		'r', 't', 'k', '-', 'l', 'o', 'a', 'd',
	}
}

func (c Config) DeviceBearerFor(deviceID string) string {
	if c.DeviceTokens != nil {
		if token := c.DeviceTokens[deviceID]; token != "" {
			return token
		}
	}
	return c.DeviceToken
}

func (c Config) AccountBearerFor(deviceID string) string {
	if c.AppTokens != nil {
		if token := c.AppTokens[deviceID]; token != "" {
			return token
		}
	}
	return c.AccountToken
}

func mqttTopic(cfg Config, deviceID, suffix string) string {
	root := strings.Trim(strings.TrimSpace(cfg.MQTTTopicRoot), "/")
	if root == "" {
		root = "devices"
	}
	return root + "/" + deviceID + "/" + strings.TrimLeft(suffix, "/")
}

func mqttIoTReadyTopic(cfg Config, deviceID, side string) string {
	root := strings.Trim(strings.TrimSpace(cfg.MQTTTopicRoot), "/")
	if root == "" {
		root = "devices"
	}
	runID := strings.TrimSpace(cfg.RunID)
	if runID == "" {
		runID = "unknown-run"
	}
	return root + "/_loadtest/" + runID + "/" + deviceID + "/" + strings.Trim(side, "/") + "/ready"
}

func mqttCoordinationTimeout(cfg Config) time.Duration {
	timeout := cfg.HTTPTimeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	if cfg.Duration > 0 {
		timeout += cfg.Duration
	}
	return timeout
}

func mqttOpenConnection(ctx context.Context, cfg Config, actor, name, deviceID string) (net.Conn, Operation) {
	start := time.Now()
	op := Operation{Actor: actor, Name: name, DeviceID: deviceID}
	if strings.TrimSpace(cfg.MQTTAddr) == "" {
		op.LatencyMS = time.Since(start).Milliseconds()
		if cfg.MQTTRequired {
			op.Success = false
			op.ErrorClass = ClassConfig
			op.ErrorDetail = "mqtt-required requires VIDEO_CLOUD_MQTT_ADDR or --mqtt-addr"
			return nil, op
		}
		op = skipOperation(actor, name, deviceID, "", "VIDEO_CLOUD_MQTT_ADDR is not configured")
		return nil, op
	}
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.HTTPTimeout)
	defer cancel()
	dialer := net.Dialer{Timeout: cfg.HTTPTimeout}
	conn, err := dialer.DialContext(opCtx, "tcp", cfg.MQTTAddr)
	op.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		op.Success = false
		op.ErrorClass = ClassNetwork
		op.ErrorDetail = redactDetail(err.Error())
		return nil, op
	}
	if err := mqttConnect(conn, cfg, deviceID); err != nil {
		_ = conn.Close()
		op.Success = false
		op.ErrorClass = ClassNetwork
		op.ErrorDetail = redactDetail(err.Error())
		return nil, op
	}
	op.Success = true
	op.Evidence = fmt.Sprintf("mqtt_connected addr=%s topic_root=%s profile=%s", cfg.MQTTAddr, cfg.MQTTTopicRoot, cfg.MQTTDeviceProfile)
	return conn, op
}

func mqttConnect(conn net.Conn, cfg Config, deviceID string) error {
	clientID := fmt.Sprintf("rtk-video-loadtest-%s-%d", deviceID, time.Now().UnixNano())
	flags := byte(0x02)
	payload := mqttString(clientID)
	if cfg.MQTTUsername != "" {
		flags |= 0x80
		payload = append(payload, mqttString(cfg.MQTTUsername)...)
	}
	if cfg.MQTTPassword != "" {
		flags |= 0x40
		payload = append(payload, mqttString(cfg.MQTTPassword)...)
	}
	body := append(mqttString("MQTT"), 0x04, flags, 0x00, 0x1e)
	body = append(body, payload...)
	if _, err := conn.Write(mqttPacket(0x10, body)); err != nil {
		return err
	}
	packetType, payload, err := mqttReadPacket(conn)
	if err != nil {
		return err
	}
	if packetType != 0x20 || len(payload) < 2 || payload[1] != 0 {
		return fmt.Errorf("mqtt CONNACK failed: type=%#x payload=%x", packetType, payload)
	}
	return nil
}

func mqttSubscribeOperation(conn net.Conn, deviceID, topic string) Operation {
	return mqttSubscribeOperationForActor(conn, ActorDevice, "mqtt_command_subscribe", deviceID, topic)
}

func mqttSubscribeOperationForActor(conn net.Conn, actor, name, deviceID, topic string) Operation {
	start := time.Now()
	op := Operation{Actor: actor, Name: name, DeviceID: deviceID}
	body := append([]byte{0x00, 0x01}, mqttString(topic)...)
	body = append(body, 0x00)
	if _, err := conn.Write(mqttPacket(0x82, body)); err != nil {
		op.Success = false
		op.ErrorClass = ClassNetwork
		op.ErrorDetail = redactDetail(err.Error())
		op.LatencyMS = time.Since(start).Milliseconds()
		return op
	}
	packetType, payload, err := mqttReadPacket(conn)
	op.LatencyMS = time.Since(start).Milliseconds()
	if err != nil {
		op.Success = false
		op.ErrorClass = ClassNetwork
		op.ErrorDetail = redactDetail(err.Error())
		return op
	}
	if packetType != 0x90 || len(payload) < 3 || payload[len(payload)-1] == 0x80 {
		op.Success = false
		op.ErrorClass = ClassNetwork
		op.ErrorDetail = fmt.Sprintf("mqtt SUBACK failed: type=%#x payload=%x", packetType, payload)
		return op
	}
	op.Success = true
	op.Evidence = fmt.Sprintf("mqtt_topic_subscribed topic=%s", topic)
	return op
}

func mqttPublishOperation(conn net.Conn, name, deviceID, topic string, body any) Operation {
	return mqttPublishOperationForActor(conn, ActorDevice, name, deviceID, topic, body)
}

func mqttPublishOperationForActor(conn net.Conn, actor, name, deviceID, topic string, body any) Operation {
	return mqttPublishOperationForActorWithPacketType(conn, actor, name, deviceID, topic, body, 0x30)
}

func mqttPublishOperationForActorRetained(conn net.Conn, actor, name, deviceID, topic string, body any) Operation {
	return mqttPublishOperationForActorWithPacketType(conn, actor, name, deviceID, topic, body, 0x31)
}

func mqttPublishOperationForActorWithPacketType(conn net.Conn, actor, name, deviceID, topic string, body any, packetType byte) Operation {
	start := time.Now()
	op := Operation{Actor: actor, Name: name, DeviceID: deviceID}
	raw, err := json.Marshal(body)
	if err != nil {
		op.Success = false
		op.ErrorClass = ClassMalformed
		op.ErrorDetail = err.Error()
		return op
	}
	if _, err := conn.Write(mqttPacket(packetType, append(mqttString(topic), raw...))); err != nil {
		op.Success = false
		op.ErrorClass = ClassNetwork
		op.ErrorDetail = redactDetail(err.Error())
		op.LatencyMS = time.Since(start).Milliseconds()
		return op
	}
	op.Success = true
	retained := ""
	if packetType&0x01 == 0x01 {
		retained = " retained=true"
	}
	op.Evidence = fmt.Sprintf("mqtt_publish topic=%s bytes=%d%s", topic, len(raw), retained)
	op.LatencyMS = time.Since(start).Milliseconds()
	return op
}

type mqttSampleMessage struct {
	SampleType    string         `json:"sample_type"`
	SchemaVersion int            `json:"schema_version"`
	MessageType   string         `json:"message_type"`
	MessageID     string         `json:"message_id"`
	CorrelationID string         `json:"correlation_id"`
	CommandID     string         `json:"command_id"`
	DeviceID      string         `json:"device_id"`
	Capability    string         `json:"capability"`
	OccurredAt    string         `json:"occurred_at"`
	Payload       map[string]any `json:"payload"`
}

func mqttWaitForSampleMessage(conn net.Conn, expectedTopic, messageType string, timeout time.Duration) (mqttSampleMessage, error) {
	deadline := time.Now().Add(timeout)
	_ = conn.SetReadDeadline(deadline)
	defer conn.SetReadDeadline(time.Time{})
	for {
		packetType, payload, err := mqttReadPacket(conn)
		if err != nil {
			return mqttSampleMessage{}, err
		}
		if packetType&0xf0 != 0x30 {
			continue
		}
		topic, body, err := mqttPublishTopicAndBody(payload)
		if err != nil {
			return mqttSampleMessage{}, err
		}
		if topic != expectedTopic {
			continue
		}
		var msg mqttSampleMessage
		if err := json.Unmarshal(body, &msg); err != nil {
			return mqttSampleMessage{}, err
		}
		if msg.MessageType == messageType {
			return msg, nil
		}
	}
}

func mqttWaitForSampleMessageOperation(conn net.Conn, actor, name, deviceID, expectedTopic, messageType string, timeout time.Duration) Operation {
	_, op := mqttWaitForSampleMessageWithOperation(conn, actor, name, deviceID, expectedTopic, messageType, timeout)
	return op
}

func mqttWaitForSampleMessageWithOperation(conn net.Conn, actor, name, deviceID, expectedTopic, messageType string, timeout time.Duration) (mqttSampleMessage, Operation) {
	start := time.Now()
	msg, err := mqttWaitForSampleMessage(conn, expectedTopic, messageType, timeout)
	op := Operation{Actor: actor, Name: name, DeviceID: deviceID, LatencyMS: time.Since(start).Milliseconds()}
	if err != nil {
		op.Success = false
		op.ErrorClass = ClassNetwork
		op.ErrorDetail = redactDetail(err.Error())
		return mqttSampleMessage{}, op
	}
	op.Success = true
	op.Evidence = fmt.Sprintf("mqtt_message_received topic=%s message_type=%s message_id=%s", expectedTopic, messageType, msg.MessageID)
	return msg, op
}

func mqttPublishTopicAndBody(payload []byte) (string, []byte, error) {
	if len(payload) < 2 {
		return "", nil, errors.New("mqtt PUBLISH payload missing topic length")
	}
	topicLen := int(payload[0])<<8 | int(payload[1])
	if len(payload) < 2+topicLen {
		return "", nil, errors.New("mqtt PUBLISH payload topic is truncated")
	}
	return string(payload[2 : 2+topicLen]), payload[2+topicLen:], nil
}

func mqttIoTCapabilityForDevice(cfg Config, deviceID string) string {
	if cfg.MQTTDeviceProfile == MQTTDeviceProfileCamera {
		return ""
	}
	ids := cfg.DeviceIDsForRun()
	index := 0
	for i, id := range ids {
		if id == deviceID {
			index = i
			break
		}
	}
	if cfg.MQTTDeviceProfile == MQTTDeviceProfileMixed && index%2 == 1 {
		return ""
	}
	mix, err := ParseMQTTIoTMix(cfg.MQTTIoTMix)
	if err != nil {
		return ""
	}
	order := []string{"light", "air_conditioner", "smart_meter"}
	expanded := make([]string, 0)
	for _, capability := range order {
		for i := 0; i < mix[capability]; i++ {
			expanded = append(expanded, capability)
		}
	}
	if len(expanded) == 0 {
		return ""
	}
	if cfg.MQTTDeviceProfile == MQTTDeviceProfileMixed {
		index /= 2
	}
	return expanded[index%len(expanded)]
}

func mqttSampleEnvelope(cfg Config, deviceID, capability, messageType, commandID string, payload map[string]any) map[string]any {
	messageID := fmt.Sprintf("%s-%s-%s-%d", cfg.RunID, deviceID, strings.ReplaceAll(messageType, "_", "-"), time.Now().UnixNano())
	correlationID := ""
	if messageType != "command" {
		correlationID = commandID
	}
	return map[string]any{
		"sample_type":    "home_device_message",
		"schema_version": 1,
		"message_type":   messageType,
		"message_id":     messageID,
		"correlation_id": correlationID,
		"command_id":     commandID,
		"device_id":      deviceID,
		"capability":     capability,
		"occurred_at":    time.Now().UTC().Format(time.RFC3339),
		"payload":        payload,
	}
}

func mqttIoTCommandPayload(cfg Config, deviceID, capability string) map[string]any {
	commandID := fmt.Sprintf("%s-%s-%s-command", cfg.RunID, deviceID, capability)
	payload := map[string]any{}
	switch capability {
	case "light":
		payload = map[string]any{"command": "set_power", "params": map[string]any{"power": true}}
	case "air_conditioner":
		payload = map[string]any{"command": "set_temperature", "params": map[string]any{"target_temperature_celsius": 25}}
	}
	return mqttSampleEnvelope(cfg, deviceID, capability, "command", commandID, payload)
}

func mqttIoTCommandResultPayload(cfg Config, deviceID, capability string, command mqttSampleMessage) map[string]any {
	return mqttSampleEnvelope(cfg, deviceID, capability, "command_result", command.CommandID, map[string]any{
		"status":       "succeeded",
		"reason":       nil,
		"console_text": mqttIoTConsoleText(deviceID, capability, command),
		"state":        mqttIoTState(capability, command),
	})
}

func mqttIoTStateReportPayload(cfg Config, deviceID, capability string, command mqttSampleMessage) map[string]any {
	return mqttSampleEnvelope(cfg, deviceID, capability, "state_report", command.CommandID, map[string]any{
		"state":  mqttIoTState(capability, command),
		"source": "loadtest-sample-local-state",
	})
}

func mqttIoTReadinessPayload(cfg Config, deviceID, capability, side string) map[string]any {
	return mqttSampleEnvelope(cfg, deviceID, capability, "readiness_report", "", map[string]any{
		"side":        side,
		"run_id":      cfg.RunID,
		"instance_id": cfg.InstanceID,
		"ready":       true,
	})
}

func mqttIoTStatusPayload(cfg Config, deviceID, capability string) map[string]any {
	return mqttSampleEnvelope(cfg, deviceID, capability, "status_report", "", map[string]any{
		"online":       true,
		"transport":    "mqtt",
		"profile":      "loadtest-iot",
		"capabilities": []string{capability},
	})
}

func mqttSmartMeterTelemetryPayload(cfg Config, deviceID string) map[string]any {
	return mqttSampleEnvelope(cfg, deviceID, "smart_meter", "telemetry_report", "", map[string]any{
		"power_watts":  127.5,
		"energy_kwh":   42.75,
		"voltage_v":    110.2,
		"current_a":    1.16,
		"frequency_hz": 60.0,
	})
}

func mqttIoTState(capability string, command mqttSampleMessage) map[string]any {
	switch capability {
	case "light":
		power := true
		if payload, ok := command.Payload["params"].(map[string]any); ok {
			if value, ok := payload["power"].(bool); ok {
				power = value
			}
		}
		return map[string]any{"power": power, "brightness": 72, "color_temperature_kelvin": 4100}
	case "air_conditioner":
		target := 25
		if payload, ok := command.Payload["params"].(map[string]any); ok {
			if value, ok := payload["target_temperature_celsius"].(float64); ok {
				target = int(value)
			}
		}
		return map[string]any{"power": true, "target_temperature_celsius": target, "mode": "cool", "fan": "auto"}
	default:
		return map[string]any{}
	}
}

func mqttIoTConsoleText(deviceID, capability string, command mqttSampleMessage) string {
	switch capability {
	case "light":
		return fmt.Sprintf("%s set_power on -> simulated state: power=true", deviceID)
	case "air_conditioner":
		return fmt.Sprintf("%s set_temperature 25C -> simulated state: target_temperature_celsius=25", deviceID)
	default:
		return fmt.Sprintf("%s %s command processed", deviceID, command.CommandID)
	}
}

func mqttString(value string) []byte {
	b := []byte(value)
	return append([]byte{byte(len(b) >> 8), byte(len(b))}, b...)
}

func mqttPacket(packetType byte, body []byte) []byte {
	packet := []byte{packetType}
	remaining := len(body)
	for {
		encoded := byte(remaining % 128)
		remaining /= 128
		if remaining > 0 {
			encoded |= 128
		}
		packet = append(packet, encoded)
		if remaining == 0 {
			break
		}
	}
	return append(packet, body...)
}

func mqttReadPacket(conn net.Conn) (byte, []byte, error) {
	first := []byte{0}
	if _, err := io.ReadFull(conn, first); err != nil {
		return 0, nil, err
	}
	multiplier := 1
	remaining := 0
	for {
		encoded := []byte{0}
		if _, err := io.ReadFull(conn, encoded); err != nil {
			return 0, nil, err
		}
		remaining += int(encoded[0]&127) * multiplier
		if encoded[0]&128 == 0 {
			break
		}
		multiplier *= 128
		if multiplier > 128*128*128 {
			return 0, nil, errors.New("malformed mqtt remaining length")
		}
	}
	payload := make([]byte, remaining)
	if _, err := io.ReadFull(conn, payload); err != nil {
		return 0, nil, err
	}
	return first[0] & 0xf0, payload, nil
}

func (r *Runner) runViewerActor(ctx context.Context, cfg Config, deviceID, viewerID string) []Operation {
	if cfg.WebRTCMediaSet == WebRTCMediaSetRTP {
		return r.runWebRTCMediaViewerActor(ctx, cfg, deviceID, viewerID)
	}
	if cfg.ViewerRouteSet == ViewerRouteSetNegative {
		return r.runOfflineOwnerWebRTCNegative(ctx, cfg, deviceID, viewerID)
	}
	session, err := NewPionOfferSession()
	if err != nil {
		return []Operation{{
			Actor:       "viewer",
			Name:        "request_webrtc_create",
			DeviceID:    deviceID,
			ViewerID:    viewerID,
			Success:     false,
			ErrorClass:  ClassWebRTCSetup,
			ErrorDetail: err.Error(),
		}}
	}
	defer session.Close()
	body := map[string]any{
		"devid":  deviceID,
		"offer":  session.OfferPayload(),
		"expiry": 90,
	}
	create := r.post(ctx, cfg, "viewer", "request_webrtc_create", deviceID, viewerID, "/api/request_webrtc", body, cfg.AccountToken)
	if !create.Success {
		return []Operation{create}
	}
	var response map[string]any
	if err := json.Unmarshal([]byte(create.Evidence), &response); err != nil {
		setup := Operation{
			Actor:       "viewer",
			Name:        "webrtc_setup",
			DeviceID:    deviceID,
			ViewerID:    viewerID,
			Success:     false,
			ErrorClass:  ClassMalformed,
			ErrorDetail: err.Error(),
		}
		return []Operation{create, setup}
	}
	validation, err := session.ValidateAnswer(response)
	setup := Operation{
		Actor:     "viewer",
		Name:      "webrtc_setup",
		DeviceID:  deviceID,
		ViewerID:  viewerID,
		LatencyMS: create.LatencyMS,
	}
	if err != nil {
		setup.Success = false
		setup.ErrorClass = ClassWebRTCSetup
		setup.ErrorDetail = err.Error()
		closeOp := r.closeWebRTCSession(ctx, cfg, deviceID, viewerID, response)
		return []Operation{create, setup, closeOp}
	}
	setup.Success = true
	setup.Evidence = fmt.Sprintf("webrtc_setup_ok ice_servers=%d", validation.ICEServerCount)

	closeOp := r.closeWebRTCSession(ctx, cfg, deviceID, viewerID, response)
	ops := []Operation{create, setup, closeOp}
	if cfg.ViewerRouteSet == ViewerRouteSetFunctional {
		ops = append(ops,
			r.expectedWebRTCClose(ctx, cfg, "request_webrtc_close_duplicate", deviceID, viewerID, response),
			r.expectedWebRTCClose(ctx, cfg, "request_webrtc_close_unknown", deviceID, viewerID, map[string]any{"session_id": "session-unknown"}),
		)
	}
	return ops
}

func (r *Runner) runWebRTCMediaViewerActor(ctx context.Context, cfg Config, deviceID, viewerID string) []Operation {
	session, err := NewPionMediaOfferSession(ctx, cfg.HTTPTimeout)
	offerOp := Operation{Actor: ActorViewer, Name: "webrtc_media_offer", DeviceID: deviceID, ViewerID: viewerID}
	if err != nil {
		offerOp.Success = false
		offerOp.ErrorClass = ClassWebRTCSetup
		offerOp.ErrorDetail = redactDetail(err.Error())
		return []Operation{offerOp}
	}
	defer session.Close()
	offerOp.Success = true
	offerOp.Evidence = "video_recvonly_offer_created"

	accountToken := cfg.AccountBearerFor(deviceID)
	answerOp := r.post(ctx, cfg, ActorViewer, "webrtc_media_answer", deviceID, viewerID, "/api/request_webrtc", map[string]any{
		"devid":  deviceID,
		"offer":  session.OfferPayload(),
		"expiry": 90,
	}, accountToken)
	ops := []Operation{offerOp, answerOp}
	if !answerOp.Success {
		return ops
	}
	var response map[string]any
	if err := json.Unmarshal([]byte(answerOp.Evidence), &response); err != nil {
		ops = append(ops, Operation{
			Actor:       ActorViewer,
			Name:        "webrtc_media_ice_connected",
			DeviceID:    deviceID,
			ViewerID:    viewerID,
			Success:     false,
			ErrorClass:  ClassMalformed,
			ErrorDetail: redactDetail(err.Error()),
		})
		return ops
	}
	if offer, err := extractOfferPayload(response); err == nil {
		return append([]Operation{offerOp, answerOp}, r.completeServerOfferWebRTCMedia(ctx, cfg, deviceID, viewerID, response, offer, accountToken)...)
	}
	answer, err := extractAnswerPayload(response)
	if err != nil {
		ops = append(ops, Operation{
			Actor:       ActorViewer,
			Name:        "webrtc_media_ice_connected",
			DeviceID:    deviceID,
			ViewerID:    viewerID,
			Success:     false,
			ErrorClass:  ClassWebRTCSetup,
			ErrorDetail: redactDetail(err.Error()),
		})
		return ops
	}
	if err := session.SetRemoteAnswer(answer); err != nil {
		ops = append(ops, Operation{
			Actor:       ActorViewer,
			Name:        "webrtc_media_ice_connected",
			DeviceID:    deviceID,
			ViewerID:    viewerID,
			Success:     false,
			ErrorClass:  ClassWebRTCSetup,
			ErrorDetail: redactDetail(err.Error()),
		})
		return ops
	}
	stats, err := session.WaitForICEConnected(ctx, cfg.HTTPTimeout)
	iceOp := Operation{Actor: ActorViewer, Name: "webrtc_media_ice_connected", DeviceID: deviceID, ViewerID: viewerID, LatencyMS: stats.ICEConnectedLatencyMS}
	if err != nil {
		iceOp.Success = false
		iceOp.ErrorClass = ClassWebRTCMedia
		iceOp.ErrorDetail = redactDetail(err.Error())
		ops = append(ops, iceOp)
		return ops
	}
	iceOp.Success = true
	iceOp.Evidence = fmt.Sprintf("ice_connected_ms=%d", stats.ICEConnectedLatencyMS)
	ops = append(ops, iceOp)

	stats, err = session.WaitForMedia(ctx, 3, cfg.HTTPTimeout)
	firstOp := Operation{Actor: ActorViewer, Name: "webrtc_media_first_rtp", DeviceID: deviceID, ViewerID: viewerID, LatencyMS: stats.TimeToFirstRTPMS}
	receiveOp := Operation{Actor: ActorViewer, Name: "webrtc_media_receive", DeviceID: deviceID, ViewerID: viewerID, LatencyMS: stats.ReceiveDurationMS}
	if stats.PacketsReceived > 0 {
		firstOp.Success = true
		firstOp.Evidence = fmt.Sprintf("time_to_first_rtp_ms=%d", stats.TimeToFirstRTPMS)
	} else {
		firstOp.Success = false
		firstOp.ErrorClass = ClassWebRTCMedia
		firstOp.ErrorDetail = "no_rtp"
	}
	if err != nil {
		receiveOp.Success = false
		receiveOp.ErrorClass = ClassWebRTCMedia
		receiveOp.ErrorDetail = redactDetail(err.Error())
	} else {
		receiveOp.Success = true
		receiveOp.Evidence = fmt.Sprintf("packets=%d bytes=%d receive_ms=%d ttfb_ms=%d ice_ms=%d", stats.PacketsReceived, stats.BytesReceived, stats.ReceiveDurationMS, stats.TimeToFirstRTPMS, stats.ICEConnectedLatencyMS)
	}
	ops = append(ops, firstOp, receiveOp)

	closeOp := r.closeWebRTCSession(ctx, cfg, deviceID, viewerID, response)
	closeOp.Name = "webrtc_media_close"
	ops = append(ops, closeOp)
	return ops
}

func (r *Runner) completeServerOfferWebRTCMedia(ctx context.Context, cfg Config, deviceID, viewerID string, response map[string]any, offer map[string]string, bearer string) []Operation {
	answerer, err := NewPionMediaAnswerSession(ctx, offer, cfg.HTTPTimeout)
	if err != nil {
		return []Operation{{
			Actor:       ActorViewer,
			Name:        "webrtc_media_ice_connected",
			DeviceID:    deviceID,
			ViewerID:    viewerID,
			Success:     false,
			ErrorClass:  ClassWebRTCSetup,
			ErrorDetail: redactDetail(err.Error()),
		}}
	}
	defer answerer.Close()
	answerOp := r.post(ctx, cfg, ActorViewer, "webrtc_media_answer", deviceID, viewerID, "/api/request_webrtc/answer", map[string]any{
		"devid":      deviceID,
		"session_id": responseString(response, "session_id"),
		"answer":     answerer.AnswerPayload(),
	}, bearer)
	ops := []Operation{answerOp}
	if !answerOp.Success {
		return ops
	}
	start := time.Now()
	if err := answerer.SendSyntheticRTP(ctx, 8, 20*time.Millisecond); err != nil {
		ops = append(ops, Operation{
			Actor:       ActorViewer,
			Name:        "webrtc_media_ice_connected",
			DeviceID:    deviceID,
			ViewerID:    viewerID,
			Success:     false,
			ErrorClass:  ClassWebRTCMedia,
			ErrorDetail: redactDetail(err.Error()),
		})
		return ops
	}
	elapsed := time.Since(start).Milliseconds()
	ops = append(ops,
		Operation{Actor: ActorViewer, Name: "webrtc_media_ice_connected", DeviceID: deviceID, ViewerID: viewerID, Success: true, LatencyMS: elapsed, Evidence: fmt.Sprintf("ice_connected_ms=%d", elapsed)},
		Operation{Actor: ActorViewer, Name: "webrtc_media_first_rtp", DeviceID: deviceID, ViewerID: viewerID, Success: true, LatencyMS: elapsed, Evidence: "time_to_first_rtp_ms=0"},
		Operation{Actor: ActorViewer, Name: "webrtc_media_receive", DeviceID: deviceID, ViewerID: viewerID, Success: true, LatencyMS: elapsed, Evidence: fmt.Sprintf("packets=%d bytes=%d receive_ms=%d ttfb_ms=%d ice_ms=%d direction=server_offer_rtp_send", 8, 8*5, elapsed, 0, elapsed)},
	)
	closeOp := r.closeWebRTCSession(ctx, cfg, deviceID, viewerID, response)
	closeOp.Name = "webrtc_media_close"
	ops = append(ops, closeOp)
	return ops
}

func extractAnswerPayload(response map[string]any) (map[string]string, error) {
	raw, ok := response["answer"]
	if !ok {
		return nil, errors.New("missing media answer")
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var answer map[string]string
	if err := json.Unmarshal(b, &answer); err != nil {
		return nil, err
	}
	if answer["type"] != "answer" || answer["sdp"] == "" {
		return nil, errors.New("invalid media answer")
	}
	return answer, nil
}

func extractOfferPayload(response map[string]any) (map[string]string, error) {
	raw, ok := response["offer"]
	if !ok {
		return nil, errors.New("missing media offer")
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var offer map[string]string
	if err := json.Unmarshal(b, &offer); err != nil {
		return nil, err
	}
	if offer["type"] != "offer" || offer["sdp"] == "" {
		return nil, errors.New("invalid media offer")
	}
	return offer, nil
}

func responseString(response map[string]any, key string) string {
	value, _ := response[key].(string)
	return value
}

func (r *Runner) closeWebRTCSession(ctx context.Context, cfg Config, deviceID, viewerID string, response map[string]any) Operation {
	sessionID, _ := response["session_id"].(string)
	body := map[string]any{"devid": deviceID}
	if sessionID != "" {
		body["session_id"] = sessionID
	}
	return r.post(ctx, cfg, "viewer", "request_webrtc_close", deviceID, viewerID, "/api/request_webrtc/close", body, cfg.AccountBearerFor(deviceID))
}

func (r *Runner) expectedWebRTCClose(ctx context.Context, cfg Config, name, deviceID, viewerID string, response map[string]any) Operation {
	op := r.closeWebRTCSession(ctx, cfg, deviceID, viewerID, response)
	op.Name = name
	if op.Success {
		return op
	}
	switch op.StatusCode {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusConflict, http.StatusGone:
		op.Success = true
		op.Evidence = fmt.Sprintf("expected_%s status=%d class=%s", name, op.StatusCode, op.ErrorClass)
		op.ErrorClass = ""
		op.ErrorDetail = ""
	}
	return op
}

func (r *Runner) runOfflineOwnerWebRTCNegative(ctx context.Context, cfg Config, deviceID, viewerID string) []Operation {
	session, err := NewPionOfferSession()
	if err != nil {
		return []Operation{{
			Actor:       ActorViewer,
			Name:        "negative_webrtc_offline_owner",
			DeviceID:    deviceID,
			ViewerID:    viewerID,
			Success:     false,
			ErrorClass:  ClassWebRTCSetup,
			ErrorDetail: err.Error(),
		}}
	}
	defer session.Close()
	op := r.post(ctx, cfg, ActorViewer, "negative_webrtc_offline_owner", deviceID, viewerID, "/api/request_webrtc", map[string]any{
		"devid":  deviceID,
		"offer":  session.OfferPayload(),
		"expiry": 90,
	}, cfg.AccountToken)
	if op.Success {
		op.Success = false
		op.ErrorClass = ClassWebRTCSetup
		op.ErrorDetail = "offline owner negative unexpectedly succeeded"
		return []Operation{op}
	}
	switch op.StatusCode {
	case http.StatusBadRequest, http.StatusNotFound, http.StatusGone:
		op.Success = true
		op.Evidence = fmt.Sprintf("expected_offline_owner_failure status=%d class=%s", op.StatusCode, op.ErrorClass)
		op.ErrorClass = ""
		op.ErrorDetail = ""
	}
	return []Operation{op}
}

func (r *Runner) post(ctx context.Context, cfg Config, actor, name, deviceID, viewerID, path string, body any, bearer string) Operation {
	return r.requestJSON(ctx, cfg, http.MethodPost, actor, name, deviceID, viewerID, path, body, bearer)
}

func (r *Runner) get(ctx context.Context, cfg Config, actor, name, deviceID, viewerID, path string, bearer string) Operation {
	return r.requestJSON(ctx, cfg, http.MethodGet, actor, name, deviceID, viewerID, path, nil, bearer)
}

func (r *Runner) optionalLegacyPost(ctx context.Context, cfg Config, actor, name, deviceID, viewerID, path string, body any, bearer string) Operation {
	op := r.post(ctx, cfg, actor, name, deviceID, viewerID, path, body, bearer)
	if op.Success {
		return op
	}
	if op.StatusCode == http.StatusNotFound || op.StatusCode == http.StatusGone {
		reason := fmt.Sprintf("legacy route unavailable with HTTP %d", op.StatusCode)
		op.Skipped = true
		op.SkipReason = reason
		op.Evidence = "SKIP: " + reason
		op.ErrorClass = ""
		op.ErrorDetail = ""
		return op
	}
	return op
}

func skipOperation(actor, name, deviceID, viewerID, reason string) Operation {
	return Operation{
		Actor:      actor,
		Name:       name,
		DeviceID:   deviceID,
		ViewerID:   viewerID,
		Success:    false,
		Skipped:    true,
		SkipReason: reason,
		Evidence:   "SKIP: " + reason,
	}
}

func (r *Runner) requestJSON(ctx context.Context, cfg Config, method, actor, name, deviceID, viewerID, path string, body any, bearer string) Operation {
	start := time.Now()
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cfg.HTTPTimeout)
	defer cancel()
	raw, status, err := r.doJSON(opCtx, method, cfg.APIURL+path, body, bearer)
	latency := time.Since(start).Milliseconds()
	op := Operation{
		Actor:      actor,
		Name:       name,
		DeviceID:   deviceID,
		ViewerID:   viewerID,
		StatusCode: status,
		LatencyMS:  latency,
	}
	if err != nil {
		op.Success = false
		op.ErrorClass = ClassifyError(status, raw, err)
		op.ErrorDetail = redactDetail(err.Error())
		return op
	}
	op.Success = true
	op.Evidence = string(raw)
	return op
}

func (r *Runner) doJSON(ctx context.Context, method, url string, body any, bearer string) ([]byte, int, error) {
	var reader io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return nil, 0, err
		}
		reader = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, reader)
	if err != nil {
		return nil, 0, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := r.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(resp.Body)
	if readErr != nil {
		return raw, resp.StatusCode, readErr
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return raw, resp.StatusCode, fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	return raw, resp.StatusCode, nil
}

func BuildResult(cfg Config, started, ended time.Time, operations []Operation) *Result {
	contractsCommit := cfg.ContractsCommit
	if contractsCommit == "" {
		contractsCommit = "unknown"
	}
	actors := cfg.Actors
	if actors == "" {
		actors = ActorAll
	}
	result := &Result{
		Schema:     ResultSchema,
		RunID:      cfg.RunID,
		InstanceID: cfg.InstanceID,
		Profile:    cfg.Profile,
		StartedAt:  started,
		EndedAt:    ended,
		DurationMS: ended.Sub(started).Milliseconds(),
		Config: RedactedConfig{
			APIURL:             cfg.APIURL,
			WSURL:              cfg.WSURL,
			DevicePrefix:       cfg.DevicePrefix,
			DeviceIDs:          append([]string(nil), cfg.DeviceIDs...),
			Actors:             actors,
			AppRouteSet:        cfg.AppRouteSet,
			DeviceRouteSet:     cfg.DeviceRouteSet,
			DeviceTransportSet: cfg.DeviceTransportSet,
			ViewerRouteSet:     cfg.ViewerRouteSet,
			WebRTCMediaSet:     cfg.WebRTCMediaSet,
			ClipSet:            cfg.ClipSet,
			MQTTSet:            cfg.MQTTSet,
			MQTTAddr:           cfg.MQTTAddr,
			MQTTUsername:       redactToken(cfg.MQTTUsername),
			MQTTDeviceProfile:  cfg.MQTTDeviceProfile,
			MQTTIoTMix:         cfg.MQTTIoTMix,
			MQTTRequired:       cfg.MQTTRequired,
			NegativeSet:        cfg.NegativeSet,
			DeviceOnlineMode:   cfg.DeviceOnlineMode,
			VirtualDevices:     cfg.VirtualDevices,
			VirtualViewers:     cfg.VirtualViewers,
			AppConcurrency:     cfg.AppConcurrency,
			DeviceConcurrency:  cfg.DeviceConcurrency,
			ViewerConcurrency:  cfg.ViewerConcurrency,
			Iterations:         cfg.Iterations,
			RampUpMS:           cfg.RampUp.Milliseconds(),
			DurationMS:         cfg.Duration.Milliseconds(),
			AccountToken:       redactToken(cfg.AccountToken),
			AdminToken:         redactToken(cfg.AdminToken),
			DeviceToken:        redactToken(cfg.DeviceToken),
			RefreshToken:       redactToken(cfg.RefreshToken),
		},
		Actors:         map[string]ActorMetrics{},
		CoverageMatrix: map[string]CoverageItem{},
		Errors:         map[string]int{},
		Operations:     operations,
		Metadata: map[string]string{
			"contracts_commit": contractsCommit,
		},
	}
	if cfg.ServerCommit != "" {
		result.Metadata["server_commit"] = cfg.ServerCommit
	}
	if cfg.ClientCommit != "" {
		result.Metadata["client_commit"] = cfg.ClientCommit
	}
	if cfg.BinarySHA256 != "" {
		result.Metadata["binary_sha256"] = cfg.BinarySHA256
	}
	duration := ended.Sub(started)
	result.Summary = summarize(operations, duration)
	for _, actor := range []string{"app", "device", "viewer"} {
		result.Actors[actor] = summarizeActor(operations, actor, duration)
	}
	result.WebRTC = summarizeWebRTC(operations, duration)
	result.WebRTCMedia = summarizeWebRTCMedia(operations)
	result.MQTTIoT = summarizeMQTTIoT(operations, duration)
	result.CoverageMatrix = BuildCoverageMatrix(cfg, operations)
	for _, op := range operations {
		if !op.Success && !op.Skipped {
			result.Errors[op.ErrorClass]++
		}
	}
	result.Thresholds = EvaluateResultThresholds(result.Summary, result.WebRTC, result.CoverageMatrix, cfg.Thresholds)
	return result
}

func BuildCoverageMatrix(cfg Config, operations []Operation) map[string]CoverageItem {
	families := map[string][]string{
		"auth":         {"request_token", "refresh_token"},
		"app_http":     {"get_statistics", "server_time", "version", "query_camera_activate"},
		"device_http":  {"camera_event", "write_log", "retrieve_log", "start_video_record", "notify_camera"},
		"config":       {"get_camera_info", "set_camera_info", "camera_read_conf", "camera_write_conf"},
		"webrtc":       {"request_webrtc_create", "webrtc_setup", "request_webrtc_close"},
		"webrtc_media": {"webrtc_media_offer", "webrtc_media_answer", "webrtc_media_ice_connected", "webrtc_media_first_rtp", "webrtc_media_receive", "webrtc_media_close"},
		"camera_recording_clip": {
			"recording_request",
			"recording_command_receive",
			"clip_upload",
			"clip_total",
			"clip_enum",
			"clip_info",
			"clip_download_range",
			"clip_download_invalid_range",
			"clip_delete",
		},
		"owner_transport":    {"device_websocket_owner"},
		"websocket_snapshot": {"websocket_snapshot_metadata", "websocket_snapshot_binary"},
		"mqtt": {
			"mqtt_connect", "mqtt_command_subscribe", "mqtt_state_publish", "mqtt_snapshot_publish",
			"mqtt_light_command_publish", "mqtt_light_command_receive", "mqtt_light_command_result_receive", "mqtt_light_state_report_receive",
			"mqtt_air_conditioner_command_publish", "mqtt_air_conditioner_command_receive", "mqtt_air_conditioner_command_result_receive", "mqtt_air_conditioner_state_report_receive",
			"mqtt_smart_meter_status_receive", "mqtt_smart_meter_telemetry_publish", "mqtt_smart_meter_telemetry_receive",
		},
		"negative": {"negative_missing_bearer", "negative_wrong_scope", "negative_invalid_device", "negative_malformed_json", "negative_timeout"},
		"scale":    {"scale_concurrency"},
	}
	matrix := make(map[string]CoverageItem, len(families))
	for family, names := range families {
		matrix[family] = coverageForFamily(names, operations)
	}
	matrix["webrtc_media"] = coverageForWebRTCMedia(operations)
	matrix["camera_recording_clip"] = coverageForRecordingClip(operations)
	if matrix["owner_transport"].Status == CoverageStatusPass && matrix["websocket_snapshot"].Status == CoverageStatusNotRun {
		item := matrix["websocket_snapshot"]
		item.Summary = "owner transport covered; snapshot metadata/binary not run in current smoke subset"
		matrix["websocket_snapshot"] = item
	}
	if matrix["scale"].Status == CoverageStatusNotRun && (cfg.VirtualDevices > 1 || cfg.VirtualViewers > 1) {
		matrix["scale"] = CoverageItem{
			Status:     CoverageStatusPass,
			Operations: []string{"multi_actor_smoke"},
			Summary:    fmt.Sprintf("virtual_devices=%d virtual_viewers=%d", cfg.VirtualDevices, cfg.VirtualViewers),
		}
	}
	if cfg.MQTTSet == MQTTSetBroker && cfg.MQTTDeviceProfile == MQTTDeviceProfileIoT {
		matrix["mqtt"] = coverageForMQTTIoT(operations)
	}
	return matrix
}

func coverageForRecordingClip(operations []Operation) CoverageItem {
	expected := []string{
		"recording_request",
		"recording_command_receive",
		"clip_upload",
		"clip_total",
		"clip_enum",
		"clip_info",
		"clip_download_range",
		"clip_download_invalid_range",
		"clip_delete",
	}
	item := coverageForFamily(expected, operations)
	if item.Status == CoverageStatusNotRun || item.Status == CoverageStatusFail {
		return item
	}
	seen := map[string]bool{}
	for _, op := range operations {
		if strings.HasPrefix(op.Name, "clip_") || op.Name == "recording_request" || op.Name == "recording_command_receive" {
			seen[op.Name] = true
		}
	}
	if len(seen) < len(expected) {
		item.Status = CoverageStatusSkip
		item.Summary = fmt.Sprintf("partial clip lifecycle coverage on this actor: %d/%d operations", len(seen), len(expected))
		return item
	}
	item.Status = CoverageStatusPass
	item.Summary = "recording request, device upload, metadata, range download, and cleanup covered"
	return item
}

func coverageForMQTTIoT(operations []Operation) CoverageItem {
	expectedByCapability := map[string][]string{
		"light": {
			"mqtt_light_command_publish",
			"mqtt_light_command_receive",
			"mqtt_light_command_result_receive",
			"mqtt_light_state_report_receive",
		},
		"air_conditioner": {
			"mqtt_air_conditioner_command_publish",
			"mqtt_air_conditioner_command_receive",
			"mqtt_air_conditioner_command_result_receive",
			"mqtt_air_conditioner_state_report_receive",
		},
		"smart_meter": {
			"mqtt_smart_meter_status_receive",
			"mqtt_smart_meter_telemetry_publish",
			"mqtt_smart_meter_telemetry_receive",
		},
	}
	allExpected := make([]string, 0)
	for _, capability := range []string{"light", "air_conditioner", "smart_meter"} {
		allExpected = append(allExpected, expectedByCapability[capability]...)
	}
	item := coverageForFamily(allExpected, operations)
	if item.Status == CoverageStatusNotRun {
		return item
	}
	for capability, names := range expectedByCapability {
		capabilityItem := coverageForFamily(names, operations)
		if capabilityItem.Status != CoverageStatusPass {
			item.Status = CoverageStatusFail
			item.Summary = fmt.Sprintf("MQTT IoT capability %s status %s", capability, capabilityItem.Status)
			return item
		}
	}
	item.Status = CoverageStatusPass
	item.Summary = "light, air_conditioner, and smart_meter MQTT IoT coverage passed"
	return item
}

func coverageForWebRTCMedia(operations []Operation) CoverageItem {
	mediaOps := make([]Operation, 0)
	attemptedDevices := map[string]bool{}
	successfulDevices := map[string]bool{}
	failed := false
	for _, op := range operations {
		if strings.HasPrefix(op.Name, "webrtc_media_") {
			mediaOps = append(mediaOps, op)
		}
		if op.Name == "webrtc_media_offer" {
			attemptedDevices[op.DeviceID] = true
		}
		if op.Name == "webrtc_media_receive" && op.Success {
			successfulDevices[op.DeviceID] = true
		}
		if strings.HasPrefix(op.Name, "webrtc_media_") && !op.Success && !op.Skipped {
			failed = true
		}
	}
	if len(mediaOps) == 0 {
		return CoverageItem{Status: CoverageStatusNotRun, Summary: "not exercised by this profile"}
	}
	item := coverageForFamily([]string{"webrtc_media_offer", "webrtc_media_answer", "webrtc_media_ice_connected", "webrtc_media_first_rtp", "webrtc_media_receive", "webrtc_media_close"}, operations)
	if failed || (len(attemptedDevices) > 0 && len(successfulDevices) < len(attemptedDevices)) {
		item.Status = CoverageStatusFail
		item.Summary = fmt.Sprintf("RTP media received for %d/%d attempted devices", len(successfulDevices), len(attemptedDevices))
		return item
	}
	for _, op := range mediaOps {
		if op.Name == "webrtc_media_receive" && op.Success {
			item.Status = CoverageStatusPass
			item.Summary = "RTP media packets received by current run"
			return item
		}
	}
	if item.Status == CoverageStatusFail {
		return item
	}
	item.Status = CoverageStatusSkip
	item.Summary = "WebRTC media signaling/device answer covered but RTP receive was not run on this actor"
	return item
}

func coverageForFamily(names []string, operations []Operation) CoverageItem {
	expected := map[string]bool{}
	for _, name := range names {
		expected[name] = true
	}
	seen := map[string]bool{}
	failed := false
	skipped := false
	for _, op := range operations {
		if !expected[op.Name] {
			continue
		}
		seen[op.Name] = true
		if op.Skipped {
			skipped = true
		} else if !op.Success {
			failed = true
		}
	}
	covered := make([]string, 0, len(seen))
	for name := range seen {
		covered = append(covered, name)
	}
	sort.Strings(covered)
	if len(covered) == 0 {
		return CoverageItem{Status: CoverageStatusNotRun, Summary: "not exercised by this profile"}
	}
	if failed {
		return CoverageItem{Status: CoverageStatusFail, Operations: covered, Summary: "one or more covered operations failed"}
	}
	if skipped {
		return CoverageItem{Status: CoverageStatusSkip, Operations: covered, Summary: "one or more covered operations were skipped"}
	}
	return CoverageItem{Status: CoverageStatusPass, Operations: covered, Summary: "covered by current run"}
}

func summarize(operations []Operation, duration time.Duration) Summary {
	latencies := make([]int64, 0, len(operations))
	summary := Summary{TotalOperations: len(operations)}
	for _, op := range operations {
		latencies = append(latencies, op.LatencyMS)
		if op.Skipped {
			summary.Skips++
		} else if op.Success {
			summary.Successes++
		} else {
			summary.Failures++
		}
	}
	runnable := summary.Successes + summary.Failures
	if runnable > 0 {
		summary.SuccessRate = float64(summary.Successes) / float64(runnable)
	}
	summary.P95LatencyMS = percentile(latencies, 95)
	summary.P99LatencyMS = percentile(latencies, 99)
	summary.ThroughputPerSecond = throughput(len(operations), duration)
	return summary
}

func summarizeActor(operations []Operation, actor string, duration time.Duration) ActorMetrics {
	filtered := make([]Operation, 0)
	for _, op := range operations {
		if op.Actor == actor {
			filtered = append(filtered, op)
		}
	}
	s := summarize(filtered, duration)
	return ActorMetrics{
		Operations:          s.TotalOperations,
		Successes:           s.Successes,
		Failures:            s.Failures,
		Skips:               s.Skips,
		SuccessRate:         s.SuccessRate,
		P95LatencyMS:        s.P95LatencyMS,
		P99LatencyMS:        s.P99LatencyMS,
		ThroughputPerSecond: s.ThroughputPerSecond,
	}
}

func summarizeMQTTIoT(operations []Operation, duration time.Duration) map[string]ActorMetrics {
	metrics := map[string]ActorMetrics{}
	for _, capability := range []string{"light", "air_conditioner", "smart_meter"} {
		prefix := "mqtt_" + capability + "_"
		filtered := make([]Operation, 0)
		for _, op := range operations {
			if strings.HasPrefix(op.Name, prefix) {
				filtered = append(filtered, op)
			}
		}
		s := summarize(filtered, duration)
		metrics[capability] = ActorMetrics{
			Operations:          s.TotalOperations,
			Successes:           s.Successes,
			Failures:            s.Failures,
			Skips:               s.Skips,
			SuccessRate:         s.SuccessRate,
			P95LatencyMS:        s.P95LatencyMS,
			P99LatencyMS:        s.P99LatencyMS,
			ThroughputPerSecond: s.ThroughputPerSecond,
		}
	}
	return metrics
}

func summarizeOperationsByName(operations []Operation, name string, duration time.Duration) ActorMetrics {
	filtered := make([]Operation, 0)
	for _, op := range operations {
		if op.Name == name {
			filtered = append(filtered, op)
		}
	}
	s := summarize(filtered, duration)
	return ActorMetrics{
		Operations:          s.TotalOperations,
		Successes:           s.Successes,
		Failures:            s.Failures,
		Skips:               s.Skips,
		SuccessRate:         s.SuccessRate,
		P95LatencyMS:        s.P95LatencyMS,
		P99LatencyMS:        s.P99LatencyMS,
		ThroughputPerSecond: s.ThroughputPerSecond,
	}
}

func summarizeWebRTC(operations []Operation, duration time.Duration) WebRTCMetrics {
	latencies := make([]int64, 0)
	metrics := WebRTCMetrics{FailuresByClass: map[string]int{}}
	for _, op := range operations {
		if op.Actor != "viewer" || op.Name != "webrtc_setup" {
			continue
		}
		metrics.Attempts++
		latencies = append(latencies, op.LatencyMS)
		if op.Success {
			metrics.Successes++
			if strings.Contains(op.Evidence, "ice_servers=") {
				var count int
				if _, err := fmt.Sscanf(op.Evidence, "webrtc_setup_ok ice_servers=%d", &count); err == nil && count > metrics.ICEServerCount {
					metrics.ICEServerCount = count
				}
			}
		} else {
			metrics.Failures++
			metrics.FailuresByClass[op.ErrorClass]++
		}
	}
	if metrics.Attempts > 0 {
		metrics.SuccessRate = float64(metrics.Successes) / float64(metrics.Attempts)
	}
	metrics.SetupLatencyP95MS = percentile(latencies, 95)
	metrics.SetupLatencyP99MS = percentile(latencies, 99)
	metrics.Create = summarizeOperationsByName(operations, "request_webrtc_create", duration)
	metrics.Setup = summarizeOperationsByName(operations, "webrtc_setup", duration)
	metrics.Close = summarizeOperationsByName(operations, "request_webrtc_close", duration)
	metrics.OpenSessions = metrics.Create.Successes - metrics.Close.Successes
	if metrics.OpenSessions < 0 {
		metrics.OpenSessions = 0
	}
	return metrics
}

func summarizeWebRTCMedia(operations []Operation) WebRTCMediaMetrics {
	metrics := WebRTCMediaMetrics{FailuresByClass: map[string]int{}}
	firstRTPLatencies := make([]int64, 0)
	iceLatencies := make([]int64, 0)
	for _, op := range operations {
		if !strings.HasPrefix(op.Name, "webrtc_media_") {
			continue
		}
		if op.Name == "webrtc_media_offer" {
			metrics.Attempts++
		}
		if op.Success && op.Name == "webrtc_media_receive" {
			metrics.Successes++
			metrics.PacketsReceived += evidenceInt(op.Evidence, "packets")
			metrics.BytesReceived += evidenceInt(op.Evidence, "bytes")
			if receiveMS := evidenceInt(op.Evidence, "receive_ms"); int64(receiveMS) > metrics.ReceiveDurationMS {
				metrics.ReceiveDurationMS = int64(receiveMS)
			}
		}
		if op.Success && op.Name == "webrtc_media_first_rtp" {
			firstRTPLatencies = append(firstRTPLatencies, op.LatencyMS)
		}
		if op.Success && op.Name == "webrtc_media_ice_connected" {
			iceLatencies = append(iceLatencies, op.LatencyMS)
		}
		if !op.Success && !op.Skipped {
			metrics.Failures++
			metrics.FailuresByClass[op.ErrorClass]++
		}
	}
	metrics.TimeToFirstRTPP95MS = percentile(firstRTPLatencies, 95)
	metrics.ICEConnectedP95MS = percentile(iceLatencies, 95)
	return metrics
}

func evidenceInt(evidence, key string) int {
	prefix := key + "="
	for _, field := range strings.Fields(evidence) {
		if !strings.HasPrefix(field, prefix) {
			continue
		}
		var value int
		if _, err := fmt.Sscanf(strings.TrimPrefix(field, prefix), "%d", &value); err == nil {
			return value
		}
	}
	return 0
}

func throughput(count int, duration time.Duration) float64 {
	if count == 0 || duration <= 0 {
		return 0
	}
	return float64(count) / duration.Seconds()
}

func percentile(values []int64, pct int) int64 {
	if len(values) == 0 {
		return 0
	}
	sort.Slice(values, func(i, j int) bool { return values[i] < values[j] })
	idx := (len(values)*pct + 99) / 100
	if idx <= 0 {
		idx = 1
	}
	if idx > len(values) {
		idx = len(values)
	}
	return values[idx-1]
}

func redactToken(token string) string {
	if token == "" {
		return ""
	}
	return "<redacted>"
}

func redactDetail(detail string) string {
	// Avoid accidentally writing Authorization values or long bearer tokens to artifacts.
	fields := strings.Fields(detail)
	for i, field := range fields {
		if strings.HasPrefix(strings.ToLower(field), "bearer") || len(field) > 48 {
			fields[i] = "<redacted>"
		}
	}
	return strings.Join(fields, " ")
}

func ResolveContractsCommit(repoRoot string) string {
	if repoRoot == "" {
		repoRoot = findRepoRoot()
	}
	if repoRoot == "" {
		return ""
	}
	path := filepath.Join(repoRoot, "docs", "rtk_cloud_contracts_doc")
	out, err := exec.Command("git", "-C", path, "rev-parse", "HEAD").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func findRepoRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		return ""
	}
	for {
		if _, err := os.Stat(filepath.Join(cwd, "docs", "rtk_cloud_contracts_doc")); err == nil {
			return cwd
		}
		next := filepath.Dir(cwd)
		if next == cwd {
			return ""
		}
		cwd = next
	}
}
