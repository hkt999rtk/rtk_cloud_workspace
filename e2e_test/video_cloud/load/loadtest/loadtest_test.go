package loadtest

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pion/webrtc/v3"
)

func TestRunnerSimulatesActorsAndClosesWebRTCSessions(t *testing.T) {
	var mu sync.Mutex
	closedSessions := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get_statistics":
			if got := r.Header.Get("Authorization"); got != "Bearer admin-token" {
				t.Fatalf("unexpected admin Authorization header %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/camera_event":
			if got := r.Header.Get("Authorization"); got != "Bearer device-token" {
				t.Fatalf("unexpected device Authorization header %q", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode camera_event: %v", err)
			}
			if body["maintype"] == nil || body["subtype"] == nil || body["eventid"] == nil || body["desc"] == nil {
				t.Fatalf("camera_event body missing legacy event fields: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/api/request_webrtc":
			if got := r.Header.Get("Authorization"); got != "Bearer account-token" {
				t.Fatalf("unexpected account Authorization header %q", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request_webrtc: %v", err)
			}
			answer := answerForOffer(t, body["offer"])
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":     "ok",
				"session_id": "session-1",
				"answer":     answer,
				"ice_servers": []map[string]any{{
					"urls": []string{"stun:stun.example.test:3478"},
				}},
			})
		case "/api/request_webrtc/close":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode close: %v", err)
			}
			sessionID, _ := body["session_id"].(string)
			mu.Lock()
			closedSessions[sessionID] = true
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := Config{
		Profile:           "safe-staging",
		APIURL:            server.URL,
		AccountToken:      "account-token",
		AdminToken:        "admin-token",
		DeviceToken:       "device-token",
		RunID:             "run-1",
		InstanceID:        "instance-1",
		DevicePrefix:      "device",
		ContractsCommit:   "contracts-test",
		Duration:          120 * time.Millisecond,
		VirtualDevices:    2,
		VirtualViewers:    2,
		AppConcurrency:    2,
		DeviceConcurrency: 2,
		ViewerConcurrency: 2,
		Iterations:        1,
		Thresholds: Thresholds{
			MinSuccessRate: 1,
			MaxP95Latency:  1000,
			MaxP99Latency:  1000,
		},
	}
	start := time.Now()
	result, err := NewRunner(server.Client()).Run(context.Background(), cfg)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 100*time.Millisecond {
		t.Fatalf("duration-driven run returned too quickly: %s", elapsed)
	}
	if result.Summary.TotalOperations != 10 {
		t.Fatalf("operations = %d, want 10", result.Summary.TotalOperations)
	}
	if result.Summary.Failures != 0 {
		t.Fatalf("failures = %d, want 0: %#v", result.Summary.Failures, result.Operations)
	}
	if result.WebRTC.Create.Successes != 2 || result.WebRTC.Setup.Successes != 2 || result.WebRTC.Close.Successes != 2 {
		t.Fatalf("unexpected WebRTC lifecycle metrics: %#v", result.WebRTC)
	}
	if result.WebRTC.OpenSessions != 0 {
		t.Fatalf("open sessions = %d, want 0", result.WebRTC.OpenSessions)
	}
	if !closedSessions["session-1"] {
		t.Fatal("expected session-1 to be closed")
	}
	if result.Metadata["contracts_commit"] != "contracts-test" {
		t.Fatalf("contracts commit = %q", result.Metadata["contracts_commit"])
	}
}

func TestRunnerFiltersDeviceActorOnly(t *testing.T) {
	called := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called[r.URL.Path]++
		if r.URL.Path != "/camera_event" {
			t.Fatalf("unexpected path for device-only run: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer device-token" {
			t.Fatalf("unexpected device Authorization header %q", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode camera_event: %v", err)
		}
		if body["maintype"] == nil || body["subtype"] == nil || body["eventid"] == nil || body["desc"] == nil {
			t.Fatalf("camera_event body missing legacy event fields: %#v", body)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:             "safe-staging",
		APIURL:              server.URL,
		Actors:              "device",
		DeviceToken:         "device-token",
		RunID:               "run-device",
		InstanceID:          "instance-device",
		DevicePrefix:        "device",
		Duration:            time.Nanosecond,
		VirtualDevices:      1,
		Iterations:          1,
		DeviceRatePerSecond: 1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Config.Actors != "device" {
		t.Fatalf("actors = %q, want device", result.Config.Actors)
	}
	if result.Actors["device"].Operations == 0 {
		t.Fatalf("device actor did not run: %#v", result.Actors)
	}
	if result.Actors["app"].Operations != 0 || result.Actors["viewer"].Operations != 0 {
		t.Fatalf("non-device actors unexpectedly ran: %#v", result.Actors)
	}
	if called["/camera_event"] == 0 || called["/get_statistics"] != 0 || called["/api/request_webrtc"] != 0 {
		t.Fatalf("unexpected calls: %#v", called)
	}
}

func TestRunnerFiltersAppAndViewerActors(t *testing.T) {
	called := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called[r.URL.Path]++
		switch r.URL.Path {
		case "/get_statistics":
			if got := r.Header.Get("Authorization"); got != "Bearer admin-token" {
				t.Fatalf("unexpected admin Authorization header %q", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/api/request_webrtc":
			if got := r.Header.Get("Authorization"); got != "Bearer account-token" {
				t.Fatalf("unexpected account Authorization header %q", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request_webrtc: %v", err)
			}
			answer := answerForOffer(t, body["offer"])
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":     "ok",
				"session_id": "session-1",
				"answer":     answer,
				"ice_servers": []map[string]any{{
					"urls": []string{"stun:stun.example.test:3478"},
				}},
			})
		case "/api/request_webrtc/close":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			t.Fatalf("unexpected path for app,viewer run: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:             "safe-staging",
		APIURL:              server.URL,
		Actors:              "app,viewer",
		AccountToken:        "account-token",
		AdminToken:          "admin-token",
		RunID:               "run-app-viewer",
		InstanceID:          "instance-app-viewer",
		DevicePrefix:        "device",
		Duration:            time.Nanosecond,
		VirtualDevices:      1,
		VirtualViewers:      1,
		Iterations:          1,
		AppRatePerSecond:    1,
		ViewerRatePerSecond: 1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Config.Actors != "app,viewer" {
		t.Fatalf("actors = %q, want app,viewer", result.Config.Actors)
	}
	if result.Actors["app"].Operations == 0 || result.Actors["viewer"].Operations == 0 {
		t.Fatalf("app/viewer actors did not run: %#v", result.Actors)
	}
	if result.Actors["device"].Operations != 0 {
		t.Fatalf("device actor unexpectedly ran: %#v", result.Actors)
	}
	if called["/camera_event"] != 0 {
		t.Fatalf("device endpoint unexpectedly called: %#v", called)
	}
}

func TestRunnerUsesExplicitDeviceIDsForFocusedRepro(t *testing.T) {
	var mu sync.Mutex
	seen := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get_statistics", "/camera_event":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode %s: %v", r.URL.Path, err)
			}
			devid, _ := body["devid"].(string)
			mu.Lock()
			seen[devid]++
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:           "safe-staging",
		APIURL:            server.URL,
		AdminToken:        "admin-token",
		DeviceToken:       "device-token",
		RunID:             "run-focused",
		InstanceID:        "instance-focused",
		Actors:            "app,device",
		DevicePrefix:      "ignored-prefix",
		DeviceIDs:         []string{"load-device-4"},
		Duration:          20 * time.Millisecond,
		VirtualDevices:    9,
		VirtualViewers:    0,
		AppConcurrency:    1,
		DeviceConcurrency: 1,
		Iterations:        1,
		DeviceOnlineMode:  DeviceOnlineModeNone,
		Thresholds: Thresholds{
			MinSuccessRate: 1,
			MaxP95Latency:  1000,
			MaxP99Latency:  1000,
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Config.VirtualDevices != 1 {
		t.Fatalf("virtual devices = %d, want explicit device id count", result.Config.VirtualDevices)
	}
	if len(result.Config.DeviceIDs) != 1 || result.Config.DeviceIDs[0] != "load-device-4" {
		t.Fatalf("redacted config device ids = %#v", result.Config.DeviceIDs)
	}
	mu.Lock()
	defer mu.Unlock()
	if seen["load-device-4"] == 0 {
		t.Fatalf("load-device-4 calls = 0; seen=%#v", seen)
	}
	for deviceID := range seen {
		if deviceID != "load-device-4" {
			t.Fatalf("unexpected generated device id %q seen=%#v", deviceID, seen)
		}
	}
}

func TestRunnerAppFunctionalRoutesCoverHTTPFamilies(t *testing.T) {
	called := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called[r.URL.Path]++
		switch r.URL.Path {
		case "/version":
			if r.Method != http.MethodGet {
				t.Fatalf("version method = %s, want GET", r.Method)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"version": "test"})
		case "/server_time":
			if r.Method != http.MethodGet {
				t.Fatalf("server_time method = %s, want GET", r.Method)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"server_time": "2026-05-09T00:00:00Z"})
		case "/refresh_token":
			if got := r.Header.Get("Authorization"); got != "Bearer admin-token" {
				t.Fatalf("refresh_token Authorization = %q, want admin bearer", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode refresh_token: %v", err)
			}
			if body["scope"] != "camera" || body["devid"] != "load-device-0" || body["refresh_token"] != "refresh-secret" {
				t.Fatalf("unexpected refresh_token body: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "access_token": "new-token", "refresh_token": "new-refresh"})
		case "/query_camera_activate":
			if got := r.Header.Get("Authorization"); got != "Bearer admin-token" {
				t.Fatalf("query_camera_activate Authorization = %q, want admin bearer", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode query_camera_activate: %v", err)
			}
			devices, ok := body["devices"].([]any)
			if !ok || len(devices) != 1 || devices[0] != "load-device-0" {
				t.Fatalf("unexpected query_camera_activate body: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "devices": []string{"1"}})
		case "/get_camera_info":
			if got := r.Header.Get("Authorization"); got != "Bearer admin-token" {
				t.Fatalf("get_camera_info Authorization = %q, want admin bearer", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "info": map[string]any{"model_name": "loadtest"}})
		case "/set_camera_info":
			if got := r.Header.Get("Authorization"); got != "Bearer device-token" {
				t.Fatalf("set_camera_info Authorization = %q, want device bearer", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/camera_write_conf", "/camera_read_conf":
			if got := r.Header.Get("Authorization"); got != "Bearer admin-token" {
				t.Fatalf("%s Authorization = %q, want admin bearer", r.URL.Path, got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/get_statistics":
			if got := r.Header.Get("Authorization"); got != "Bearer admin-token" {
				t.Fatalf("get_statistics Authorization = %q, want admin bearer", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			t.Fatalf("unexpected path for functional app route set: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:          "safe-staging",
		APIURL:           server.URL,
		Actors:           "app",
		AppRouteSet:      AppRouteSetFunctional,
		AdminToken:       "admin-token",
		DeviceToken:      "device-token",
		RefreshToken:     "refresh-secret",
		RunID:            "run-app-functional",
		InstanceID:       "instance-app-functional",
		DevicePrefix:     "load-device",
		Duration:         time.Nanosecond,
		VirtualDevices:   1,
		Iterations:       1,
		AppRatePerSecond: 1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	opsByName := map[string]Operation{}
	for _, op := range result.Operations {
		opsByName[op.Name] = op
		if strings.Contains(op.Evidence, "admin-token") || strings.Contains(op.Evidence, "device-token") || strings.Contains(op.Evidence, "refresh-secret") {
			t.Fatalf("operation evidence leaked token: %#v", op)
		}
	}
	for _, name := range []string{
		"get_statistics",
		"server_time",
		"version",
		"refresh_token",
		"query_camera_activate",
		"get_camera_info",
		"set_camera_info",
		"camera_write_conf",
		"camera_read_conf",
	} {
		if _, ok := opsByName[name]; !ok {
			t.Fatalf("missing operation %s in %#v", name, result.Operations)
		}
	}
	if result.Summary.Failures != 0 {
		t.Fatalf("functional app routes produced failures: %#v", result.Operations)
	}
	if result.Summary.Skips != 0 {
		t.Fatalf("skips = %d, want 0", result.Summary.Skips)
	}
	if result.CoverageMatrix["app_http"].Status != CoverageStatusPass {
		t.Fatalf("app_http coverage = %#v", result.CoverageMatrix["app_http"])
	}
	if result.CoverageMatrix["auth"].Status != CoverageStatusPass {
		t.Fatalf("auth coverage = %#v", result.CoverageMatrix["auth"])
	}
	if result.CoverageMatrix["config"].Status != CoverageStatusPass {
		t.Fatalf("config coverage = %#v", result.CoverageMatrix["config"])
	}
}

func TestRunnerWebSocketDeviceOnlineEnablesViewerWebRTC(t *testing.T) {
	var mu sync.Mutex
	onlineDevices := map[string]bool{}
	wsRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws/device":
			if got := r.URL.Query().Get("devid"); got != "device-0" {
				t.Fatalf("websocket devid = %q, want device-0", got)
			}
			if token := r.URL.Query().Get("token"); token != "" {
				t.Fatalf("websocket token leaked into query: %q", token)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer device-token" {
				t.Fatalf("websocket Authorization = %q, want bearer device token", got)
			}
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("response writer does not support hijack")
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			mu.Lock()
			onlineDevices["device-0"] = true
			wsRequests++
			mu.Unlock()
			_, _ = fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: test\r\n\r\n")
			go func() {
				<-r.Context().Done()
				_ = conn.Close()
			}()
		case "/camera_event":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/get_statistics":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/api/request_webrtc":
			mu.Lock()
			online := onlineDevices["device-0"]
			mu.Unlock()
			if !online {
				http.Error(w, `{"status":"fail","reason":"device not online"}`, http.StatusBadRequest)
				return
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request_webrtc: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":     "ok",
				"session_id": "session-1",
				"answer":     answerForOffer(t, body["offer"]),
				"ice_servers": []map[string]any{{
					"urls": []string{"stun:stun.example.test:3478"},
				}},
			})
		case "/api/request_webrtc/close":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:          "safe-staging",
		APIURL:           server.URL,
		AccountToken:     "account-token",
		AdminToken:       "admin-token",
		DeviceToken:      "device-token",
		RunID:            "run-ws-owner",
		InstanceID:       "instance-ws-owner",
		DevicePrefix:     "device",
		DeviceOnlineMode: DeviceOnlineModeWebSocket,
		Duration:         50 * time.Millisecond,
		VirtualDevices:   1,
		VirtualViewers:   1,
		Iterations:       1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.WebRTC.Create.Failures != 0 || result.WebRTC.Create.Successes == 0 {
		t.Fatalf("unexpected WebRTC create metrics: %#v operations=%#v", result.WebRTC.Create, result.Operations)
	}
	mu.Lock()
	defer mu.Unlock()
	if wsRequests != 1 {
		t.Fatalf("websocket owner requests = %d, want 1", wsRequests)
	}
}

func TestRunnerWebSocketSnapshotTransportCoverage(t *testing.T) {
	var mu sync.Mutex
	wsRequests := 0
	metadataSeen := false
	binarySeen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws/device" {
			http.NotFound(w, r)
			return
		}
		if got := r.URL.Query().Get("devid"); got != "device-0" {
			t.Fatalf("websocket devid = %q, want device-0", got)
		}
		if token := r.URL.Query().Get("token"); token != "" {
			t.Fatalf("websocket token leaked into query: %q", token)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer device-token" {
			t.Fatalf("websocket Authorization = %q, want bearer device token", got)
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("response writer does not support hijack")
		}
		conn, reader, err := hijacker.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		mu.Lock()
		wsRequests++
		requestIndex := wsRequests
		mu.Unlock()
		_, _ = fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: test\r\n\r\n")
		if requestIndex == 1 {
			opcode, payload, err := readClientWebSocketFrame(reader)
			if err != nil {
				t.Fatalf("read snapshot metadata frame: %v", err)
			}
			if opcode != 1 {
				t.Fatalf("metadata opcode = %d, want text", opcode)
			}
			var metadata map[string]any
			if err := json.Unmarshal(payload, &metadata); err != nil {
				t.Fatalf("decode snapshot metadata: %v payload=%s", err, payload)
			}
			data, _ := metadata["data"].(map[string]any)
			if metadata["event"] != "upload_snapshot" || int(data["Size"].(float64)) == 0 {
				t.Fatalf("unexpected snapshot metadata: %#v", metadata)
			}
			opcode, payload, err = readClientWebSocketFrame(reader)
			if err != nil {
				t.Fatalf("read snapshot binary frame: %v", err)
			}
			if opcode != 2 || len(payload) == 0 {
				t.Fatalf("binary opcode/size = %d/%d, want binary payload", opcode, len(payload))
			}
			mu.Lock()
			metadataSeen = true
			binarySeen = true
			mu.Unlock()
			_ = conn.Close()
			return
		}
		go func() {
			<-r.Context().Done()
			_ = conn.Close()
		}()
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:            "safe-staging",
		APIURL:             server.URL,
		WSURL:              strings.Replace(server.URL, "http://", "ws://", 1),
		Actors:             "device",
		DeviceToken:        "device-token",
		DeviceTransportSet: DeviceTransportSetSnapshot,
		RunID:              "run-ws-snapshot",
		InstanceID:         "instance-ws-snapshot",
		DevicePrefix:       "device",
		DeviceOnlineMode:   DeviceOnlineModeWebSocket,
		Duration:           time.Nanosecond,
		VirtualDevices:     1,
		Iterations:         1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	opsByName := map[string]Operation{}
	for _, op := range result.Operations {
		opsByName[op.Name] = op
		if strings.Contains(op.Evidence, "device-token") {
			t.Fatalf("operation evidence leaked token: %#v", op)
		}
	}
	for _, name := range []string{"device_websocket_owner", "websocket_snapshot_metadata", "websocket_snapshot_binary", "device_websocket_reconnect"} {
		if op := opsByName[name]; !op.Success {
			t.Fatalf("%s = %#v, want success", name, op)
		}
	}
	if result.CoverageMatrix["owner_transport"].Status != CoverageStatusPass {
		t.Fatalf("owner transport coverage = %#v", result.CoverageMatrix["owner_transport"])
	}
	if result.CoverageMatrix["websocket_snapshot"].Status != CoverageStatusPass {
		t.Fatalf("websocket_snapshot coverage = %#v", result.CoverageMatrix["websocket_snapshot"])
	}
	mu.Lock()
	defer mu.Unlock()
	if wsRequests != 2 {
		t.Fatalf("websocket owner requests = %d, want initial connect plus reconnect", wsRequests)
	}
	if !metadataSeen || !binarySeen {
		t.Fatalf("metadataSeen=%v binarySeen=%v", metadataSeen, binarySeen)
	}
}

func TestRunnerWebSocketOwnerSendsKeepaliveAndRecordsLifecycle(t *testing.T) {
	oldInterval := webSocketOwnerKeepaliveInterval
	webSocketOwnerKeepaliveInterval = 5 * time.Millisecond
	defer func() { webSocketOwnerKeepaliveInterval = oldInterval }()

	var mu sync.Mutex
	wsRequests := 0
	statusReports := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws/device":
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("response writer does not support hijack")
			}
			conn, reader, err := hijacker.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			mu.Lock()
			wsRequests++
			mu.Unlock()
			_, _ = fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: test\r\n\r\n")
			go func() {
				defer conn.Close()
				for {
					opcode, payload, err := readClientWebSocketFrame(reader)
					if err != nil {
						return
					}
					if opcode == 1 && strings.Contains(string(payload), `"event":"status_report"`) && strings.Contains(string(payload), `"wifi_strength":"-50"`) {
						mu.Lock()
						statusReports++
						mu.Unlock()
					}
				}
			}()
		case "/camera_event":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:          ProfileSmoke,
		APIURL:           server.URL,
		WSURL:            strings.Replace(server.URL, "http://", "ws://", 1),
		Actors:           ActorDevice,
		DeviceToken:      "device-token",
		RunID:            "run-ws-keepalive",
		InstanceID:       "instance-ws-keepalive",
		DevicePrefix:     "device",
		DeviceOnlineMode: DeviceOnlineModeWebSocket,
		Duration:         35 * time.Millisecond,
		VirtualDevices:   1,
		Iterations:       1,
		HTTPTimeout:      time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	keepaliveOps := 0
	for _, op := range result.Operations {
		if op.Name == "device_websocket_keepalive" && op.Success {
			keepaliveOps++
		}
	}
	if keepaliveOps == 0 {
		t.Fatalf("missing successful device_websocket_keepalive operation: %#v", result.Operations)
	}
	mu.Lock()
	defer mu.Unlock()
	if wsRequests != 1 {
		t.Fatalf("websocket requests = %d, want 1", wsRequests)
	}
	if statusReports == 0 {
		t.Fatal("server did not receive status_report text keepalive")
	}
}

func TestRunnerWebSocketOwnerReconnectsAfterKeepaliveFailure(t *testing.T) {
	oldInterval := webSocketOwnerKeepaliveInterval
	webSocketOwnerKeepaliveInterval = 5 * time.Millisecond
	defer func() { webSocketOwnerKeepaliveInterval = oldInterval }()

	var mu sync.Mutex
	wsRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws/device":
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("response writer does not support hijack")
			}
			conn, reader, err := hijacker.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			mu.Lock()
			wsRequests++
			requestIndex := wsRequests
			mu.Unlock()
			_, _ = fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: test\r\n\r\n")
			go func() {
				defer conn.Close()
				opcode, _, err := readClientWebSocketFrame(reader)
				if err == nil && opcode == 1 && requestIndex == 1 {
					return
				}
				<-r.Context().Done()
			}()
		case "/camera_event":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:          ProfileSmoke,
		APIURL:           server.URL,
		WSURL:            strings.Replace(server.URL, "http://", "ws://", 1),
		Actors:           ActorDevice,
		DeviceToken:      "device-token",
		RunID:            "run-ws-reconnect",
		InstanceID:       "instance-ws-reconnect",
		DevicePrefix:     "device",
		DeviceOnlineMode: DeviceOnlineModeWebSocket,
		Duration:         45 * time.Millisecond,
		VirtualDevices:   1,
		Iterations:       1,
		HTTPTimeout:      time.Second,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	reconnects := 0
	for _, op := range result.Operations {
		if op.Name == "device_websocket_reconnect" && op.Success && strings.Contains(op.Evidence, "reason=keepalive") {
			reconnects++
		}
	}
	if reconnects == 0 {
		t.Fatalf("missing keepalive-triggered reconnect operation: %#v", result.Operations)
	}
	mu.Lock()
	defer mu.Unlock()
	if wsRequests < 2 {
		t.Fatalf("websocket requests = %d, want reconnect", wsRequests)
	}
}

func TestRunnerWebRTCFunctionalCoverageAddsDuplicateAndUnknownClose(t *testing.T) {
	closeCalls := map[string]int{}
	createCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/request_webrtc":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request_webrtc: %v", err)
			}
			createCount++
			sessionID := fmt.Sprintf("session-%d", createCount)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":     "ok",
				"session_id": sessionID,
				"answer":     answerForOffer(t, body["offer"]),
				"ice_servers": []map[string]any{{
					"urls": []string{"stun:stun.example.test:3478"},
				}},
			})
		case "/api/request_webrtc/close":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode close: %v", err)
			}
			sessionID, _ := body["session_id"].(string)
			closeCalls[sessionID]++
			if sessionID == "session-unknown" {
				http.Error(w, `{"status":"fail","reason":"session not found"}`, http.StatusNotFound)
				return
			}
			if closeCalls[sessionID] > 1 {
				http.Error(w, `{"status":"fail","reason":"session already closed"}`, http.StatusConflict)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:             "safe-staging",
		APIURL:              server.URL,
		Actors:              "viewer",
		ViewerRouteSet:      ViewerRouteSetFunctional,
		AccountToken:        "account-token",
		RunID:               "run-webrtc-functional",
		InstanceID:          "instance-webrtc-functional",
		DevicePrefix:        "device",
		DeviceOnlineMode:    DeviceOnlineModeNone,
		Duration:            time.Nanosecond,
		VirtualDevices:      1,
		VirtualViewers:      3,
		ViewerConcurrency:   3,
		Iterations:          1,
		ViewerRatePerSecond: 3,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	opsByName := map[string]int{}
	for _, op := range result.Operations {
		opsByName[op.Name]++
		if strings.Contains(op.Evidence, "account-token") {
			t.Fatalf("operation evidence leaked token: %#v", op)
		}
	}
	if result.WebRTC.Create.Successes != 3 || result.WebRTC.Close.Successes != 3 || result.WebRTC.OpenSessions != 0 {
		t.Fatalf("unexpected WebRTC metrics: %#v operations=%#v", result.WebRTC, result.Operations)
	}
	if opsByName["request_webrtc_close_duplicate"] != 3 || opsByName["request_webrtc_close_unknown"] != 3 {
		t.Fatalf("missing functional close coverage: %#v", opsByName)
	}
	if result.Summary.Failures != 0 {
		t.Fatalf("functional close expected failures should not fail run: %#v", result.Operations)
	}
}

func TestRunnerWebRTCNegativeOfflineOwnerIsExpectedFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/request_webrtc" {
			http.NotFound(w, r)
			return
		}
		http.Error(w, `{"status":"fail","reason":"device not online"}`, http.StatusBadRequest)
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:             "safe-staging",
		APIURL:              server.URL,
		Actors:              "viewer",
		ViewerRouteSet:      ViewerRouteSetNegative,
		AccountToken:        "account-token",
		RunID:               "run-webrtc-negative",
		InstanceID:          "instance-webrtc-negative",
		DevicePrefix:        "device",
		DeviceOnlineMode:    DeviceOnlineModeNone,
		Duration:            time.Nanosecond,
		VirtualDevices:      1,
		VirtualViewers:      1,
		Iterations:          1,
		ViewerRatePerSecond: 1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Operations) != 1 {
		t.Fatalf("operations = %#v, want one negative operation", result.Operations)
	}
	op := result.Operations[0]
	if op.Name != "negative_webrtc_offline_owner" || !op.Success || op.StatusCode != http.StatusBadRequest {
		t.Fatalf("negative offline owner op = %#v, want expected success with HTTP 400 evidence", op)
	}
	if result.Summary.Failures != 0 || result.WebRTC.Attempts != 0 {
		t.Fatalf("negative expected failure polluted normal metrics: summary=%#v webrtc=%#v", result.Summary, result.WebRTC)
	}
}

func TestRunnerMQTTBrokerCoveragePublishesStateLogAndSnapshot(t *testing.T) {
	packets := make(chan byte, 8)
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen mqtt: %v", err)
	}
	defer listener.Close()
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		packetType, _, err := readMQTTPacketForTest(conn)
		if err != nil {
			t.Errorf("read connect: %v", err)
			return
		}
		packets <- packetType
		_, _ = conn.Write([]byte{0x20, 0x02, 0x00, 0x00})
		packetType, _, err = readMQTTPacketForTest(conn)
		if err != nil {
			t.Errorf("read subscribe: %v", err)
			return
		}
		packets <- packetType
		_, _ = conn.Write([]byte{0x90, 0x03, 0x00, 0x01, 0x00})
		for i := 0; i < 3; i++ {
			packetType, payload, err := readMQTTPacketForTest(conn)
			if err != nil {
				t.Errorf("read publish %d: %v", i, err)
				return
			}
			packets <- packetType
			if !bytes.Contains(payload, []byte("load-device-0")) {
				t.Errorf("publish payload missing device id: %s", payload)
			}
		}
	}()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/camera_event" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:             "safe-staging",
		APIURL:              server.URL,
		Actors:              "device",
		DeviceOnlineMode:    DeviceOnlineModeNone,
		DeviceToken:         "device-token",
		MQTTSet:             MQTTSetBroker,
		MQTTAddr:            listener.Addr().String(),
		MQTTTopicRoot:       "devices",
		RunID:               "run-mqtt",
		InstanceID:          "instance-mqtt",
		DevicePrefix:        "load-device",
		Duration:            time.Nanosecond,
		VirtualDevices:      1,
		Iterations:          1,
		DeviceRatePerSecond: 1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	opsByName := map[string]Operation{}
	for _, op := range result.Operations {
		opsByName[op.Name] = op
	}
	for _, name := range []string{"mqtt_connect", "mqtt_command_subscribe", "mqtt_state_publish", "mqtt_log_publish", "mqtt_snapshot_publish"} {
		if op := opsByName[name]; !op.Success {
			t.Fatalf("%s = %#v, want success", name, op)
		}
	}
	if op := opsByName["mqtt_native_binary_unsupported"]; !op.Skipped {
		t.Fatalf("mqtt native binary unsupported op = %#v, want skip evidence", op)
	}
	if result.CoverageMatrix["mqtt"].Status != CoverageStatusPass {
		t.Fatalf("mqtt coverage = %#v, want PASS", result.CoverageMatrix["mqtt"])
	}
	for _, want := range []byte{0x10, 0x80, 0x30, 0x30, 0x30} {
		select {
		case got := <-packets:
			if got&0xf0 != want {
				t.Fatalf("mqtt packet type = %#x, want %#x", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("timeout waiting for mqtt packet %#x", want)
		}
	}
}

func TestRunnerMQTTBrokerCoverageSkipsWhenBrokerMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/camera_event" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:             "safe-staging",
		APIURL:              server.URL,
		Actors:              "device",
		DeviceOnlineMode:    DeviceOnlineModeNone,
		DeviceToken:         "device-token",
		MQTTSet:             MQTTSetBroker,
		RunID:               "run-mqtt-skip",
		InstanceID:          "instance-mqtt-skip",
		DevicePrefix:        "load-device",
		Duration:            time.Nanosecond,
		VirtualDevices:      1,
		Iterations:          1,
		DeviceRatePerSecond: 1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	var mqttConnect Operation
	for _, op := range result.Operations {
		if op.Name == "mqtt_connect" {
			mqttConnect = op
		}
	}
	if !mqttConnect.Skipped {
		t.Fatalf("mqtt_connect = %#v, want clean skip", mqttConnect)
	}
	if result.CoverageMatrix["mqtt"].Status != CoverageStatusSkip {
		t.Fatalf("mqtt coverage = %#v, want SKIP", result.CoverageMatrix["mqtt"])
	}
}

func TestRunnerMQTTRequiredFailsWhenBrokerMissing(t *testing.T) {
	_, err := NewRunner(nil).Run(context.Background(), Config{
		Profile:           "safe-staging",
		APIURL:            "http://video-cloud-cd.local:18080",
		Actors:            "device",
		DeviceToken:       "device-token",
		MQTTSet:           MQTTSetBroker,
		MQTTDeviceProfile: MQTTDeviceProfileIoT,
		MQTTRequired:      true,
		RunID:             "run-mqtt-required",
		InstanceID:        "instance-mqtt-required",
		DevicePrefix:      "load-device",
		Duration:          time.Nanosecond,
		VirtualDevices:    1,
		Iterations:        1,
	})
	if err == nil {
		t.Fatal("Run succeeded, want missing MQTT broker configuration failure")
	}
	if !strings.Contains(err.Error(), "VIDEO_CLOUD_MQTT_ADDR") || !strings.Contains(err.Error(), "mqtt-required") {
		t.Fatalf("error = %q, want mqtt-required broker address detail", err)
	}
}

func TestRunnerRejectsInvalidMQTTIoTMix(t *testing.T) {
	cfg := Config{
		Profile:           "safe-staging",
		APIURL:            "http://video-cloud-cd.local:18080",
		MQTTSet:           MQTTSetBroker,
		MQTTDeviceProfile: MQTTDeviceProfileIoT,
		MQTTIoTMix:        "light=1,thermostat=1",
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("Validate succeeded, want invalid MQTT IoT mix failure")
	}
	if !strings.Contains(err.Error(), "unsupported MQTT IoT capability") {
		t.Fatalf("error = %q, want unsupported capability detail", err)
	}
}

func TestRunnerMQTTIoTCoverageCoordinatesCommandsAndTelemetry(t *testing.T) {
	broker := newFakeMQTTBroker(t)
	defer broker.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/camera_event":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/get_statistics":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	cfg := Config{
		Profile:             "safe-staging",
		APIURL:              server.URL,
		Actors:              "device",
		DeviceOnlineMode:    DeviceOnlineModeNone,
		DeviceToken:         "device-token",
		MQTTSet:             MQTTSetBroker,
		MQTTAddr:            broker.Addr(),
		MQTTTopicRoot:       "devices",
		MQTTDeviceProfile:   MQTTDeviceProfileIoT,
		MQTTIoTMix:          "light=1,air_conditioner=1,smart_meter=1",
		RunID:               "run-mqtt-iot",
		InstanceID:          "instance-mqtt-iot-device",
		DeviceIDs:           []string{"load-light-0", "load-ac-0", "load-meter-0"},
		Duration:            120 * time.Millisecond,
		VirtualDevices:      3,
		Iterations:          1,
		DeviceConcurrency:   3,
		DeviceRatePerSecond: 30,
	}
	deviceResultCh := make(chan *Result, 1)
	deviceErrCh := make(chan error, 1)
	go func() {
		result, err := NewRunner(server.Client()).Run(context.Background(), cfg)
		if err != nil {
			deviceErrCh <- err
			return
		}
		deviceResultCh <- result
	}()
	broker.WaitForSubscribers(t, 2, time.Second)

	appResult, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:           "safe-staging",
		APIURL:            server.URL,
		Actors:            "app",
		AdminToken:        "admin-token",
		MQTTSet:           MQTTSetBroker,
		MQTTAddr:          broker.Addr(),
		MQTTTopicRoot:     "devices",
		MQTTDeviceProfile: MQTTDeviceProfileIoT,
		MQTTIoTMix:        "light=1,air_conditioner=1,smart_meter=1",
		RunID:             "run-mqtt-iot",
		InstanceID:        "instance-mqtt-iot-app",
		DeviceIDs:         []string{"load-light-0", "load-ac-0", "load-meter-0"},
		Duration:          120 * time.Millisecond,
		VirtualDevices:    3,
		Iterations:        1,
		AppConcurrency:    3,
		AppRatePerSecond:  30,
	})
	if err != nil {
		t.Fatalf("Run app: %v", err)
	}
	var deviceResult *Result
	select {
	case err := <-deviceErrCh:
		t.Fatalf("Run device: %v", err)
	case deviceResult = <-deviceResultCh:
	case <-time.After(2 * time.Second):
		t.Fatal("timeout waiting for device MQTT IoT run")
	}
	combinedOps := append(append([]Operation{}, deviceResult.Operations...), appResult.Operations...)
	combined := BuildResult(Config{
		Profile:           "functional",
		RunID:             "combined",
		InstanceID:        "combined",
		MQTTSet:           MQTTSetBroker,
		MQTTDeviceProfile: MQTTDeviceProfileIoT,
		MQTTIoTMix:        "light=1,air_conditioner=1,smart_meter=1",
		Thresholds:        Thresholds{MinSuccessRate: 1, RequireCoverageMatrix: true},
	}, time.Now(), time.Now().Add(time.Second), combinedOps)

	wantOps := []string{
		"mqtt_light_command_publish",
		"mqtt_light_command_receive",
		"mqtt_light_command_result_receive",
		"mqtt_light_state_report_receive",
		"mqtt_air_conditioner_command_publish",
		"mqtt_air_conditioner_command_receive",
		"mqtt_air_conditioner_command_result_receive",
		"mqtt_air_conditioner_state_report_receive",
		"mqtt_smart_meter_telemetry_publish",
		"mqtt_smart_meter_telemetry_receive",
	}
	opsByName := map[string]Operation{}
	for _, op := range combined.Operations {
		opsByName[op.Name] = op
	}
	for _, name := range wantOps {
		if op := opsByName[name]; !op.Success {
			t.Fatalf("%s = %#v, want success", name, op)
		}
	}
	if combined.CoverageMatrix["mqtt"].Status != CoverageStatusPass {
		t.Logf("mqtt ops: %s", mqttOperationSummary(combined.Operations))
		t.Fatalf("mqtt coverage = %#v, want PASS; failed ops: %s", combined.CoverageMatrix["mqtt"], failedOperationSummary(combined.Operations))
	}
	for _, capability := range []string{"light", "air_conditioner", "smart_meter"} {
		if combined.MQTTIoT[capability].Operations == 0 || combined.MQTTIoT[capability].Failures != 0 {
			t.Fatalf("MQTT IoT metrics for %s = %#v, want operations without failures", capability, combined.MQTTIoT[capability])
		}
	}
}

func TestRunnerMQTTIoTCoverageCoordinatesWhenAppStartsBeforeDevice(t *testing.T) {
	broker := newFakeMQTTBroker(t)
	defer broker.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/camera_event":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/get_statistics":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	deviceIDs := []string{"load-light-0", "load-ac-0", "load-meter-0"}
	appResultCh := make(chan *Result, 1)
	appErrCh := make(chan error, 1)
	go func() {
		result, err := NewRunner(server.Client()).Run(context.Background(), Config{
			Profile:           "safe-staging",
			APIURL:            server.URL,
			Actors:            "app",
			AdminToken:        "admin-token",
			MQTTSet:           MQTTSetBroker,
			MQTTAddr:          broker.Addr(),
			MQTTTopicRoot:     "devices",
			MQTTDeviceProfile: MQTTDeviceProfileIoT,
			MQTTIoTMix:        "light=1,air_conditioner=1,smart_meter=1",
			RunID:             "run-mqtt-iot-app-first",
			InstanceID:        "instance-mqtt-iot-app-first",
			DeviceIDs:         deviceIDs,
			Duration:          2 * time.Second,
			VirtualDevices:    len(deviceIDs),
			Iterations:        1,
			AppConcurrency:    len(deviceIDs),
			HTTPTimeout:       200 * time.Millisecond,
		})
		if err != nil {
			appErrCh <- err
			return
		}
		appResultCh <- result
	}()

	time.Sleep(350 * time.Millisecond)

	deviceResult, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:           "safe-staging",
		APIURL:            server.URL,
		Actors:            "device",
		DeviceOnlineMode:  DeviceOnlineModeNone,
		DeviceToken:       "device-token",
		MQTTSet:           MQTTSetBroker,
		MQTTAddr:          broker.Addr(),
		MQTTTopicRoot:     "devices",
		MQTTDeviceProfile: MQTTDeviceProfileIoT,
		MQTTIoTMix:        "light=1,air_conditioner=1,smart_meter=1",
		RunID:             "run-mqtt-iot-app-first",
		InstanceID:        "instance-mqtt-iot-device-after-app",
		DeviceIDs:         deviceIDs,
		Duration:          2 * time.Second,
		VirtualDevices:    len(deviceIDs),
		Iterations:        1,
		DeviceConcurrency: len(deviceIDs),
		HTTPTimeout:       200 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Run device: %v", err)
	}
	var appResult *Result
	select {
	case err := <-appErrCh:
		t.Fatalf("Run app: %v", err)
	case appResult = <-appResultCh:
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for app MQTT IoT run")
	}

	combinedOps := append(append([]Operation{}, deviceResult.Operations...), appResult.Operations...)
	combined := BuildResult(Config{
		Profile:           "functional",
		RunID:             "combined",
		InstanceID:        "combined",
		MQTTSet:           MQTTSetBroker,
		MQTTDeviceProfile: MQTTDeviceProfileIoT,
		MQTTIoTMix:        "light=1,air_conditioner=1,smart_meter=1",
		Thresholds:        Thresholds{MinSuccessRate: 1, RequireCoverageMatrix: true},
	}, time.Now(), time.Now().Add(time.Second), combinedOps)

	if combined.CoverageMatrix["mqtt"].Status != CoverageStatusPass {
		t.Logf("mqtt ops: %s", mqttOperationSummary(combined.Operations))
		t.Fatalf("mqtt coverage = %#v, want PASS; failed ops: %s", combined.CoverageMatrix["mqtt"], failedOperationSummary(combined.Operations))
	}
	for _, capability := range []string{"light", "air_conditioner", "smart_meter"} {
		if combined.MQTTIoT[capability].Operations == 0 || combined.MQTTIoT[capability].Failures != 0 {
			t.Fatalf("MQTT IoT metrics for %s = %#v, want operations without failures", capability, combined.MQTTIoT[capability])
		}
	}
}

func TestRunnerNegativeHTTPCoverageRecordsExpectedFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/get_statistics":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/get_camera_info":
			if r.Header.Get("Authorization") == "" {
				http.Error(w, `{"status":"fail","reason":"missing bearer"}`, http.StatusUnauthorized)
				return
			}
			http.Error(w, `{"status":"fail","reason":"device not found"}`, http.StatusNotFound)
		case "/__loadtest/malformed_json":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{not-json`))
		case "/__loadtest/timeout":
			time.Sleep(50 * time.Millisecond)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "late"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:               "safe-staging",
		APIURL:                server.URL,
		Actors:                "app",
		NegativeSet:           NegativeSetHTTP,
		NegativeMalformedPath: "/__loadtest/malformed_json",
		NegativeTimeoutPath:   "/__loadtest/timeout",
		AdminToken:            "admin-token",
		RunID:                 "run-negative",
		InstanceID:            "instance-negative",
		DevicePrefix:          "load-device",
		DeviceOnlineMode:      DeviceOnlineModeNone,
		Duration:              time.Nanosecond,
		VirtualDevices:        1,
		Iterations:            1,
		AppRatePerSecond:      1,
		HTTPTimeout:           5 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	opsByName := map[string]Operation{}
	for _, op := range result.Operations {
		opsByName[op.Name] = op
		if strings.Contains(op.Evidence, "admin-token") || strings.Contains(op.ErrorDetail, "admin-token") {
			t.Fatalf("negative operation leaked token: %#v", op)
		}
	}
	for _, name := range []string{"negative_missing_bearer", "negative_invalid_device", "negative_malformed_json", "negative_timeout"} {
		if op := opsByName[name]; !op.Success || !strings.Contains(op.Evidence, "expected_failure") {
			t.Fatalf("%s = %#v, want expected failure success", name, op)
		}
	}
	if result.Summary.Failures != 0 {
		t.Fatalf("expected failures should not fail run: %#v", result.Operations)
	}
	if result.CoverageMatrix["negative"].Status != CoverageStatusPass {
		t.Fatalf("negative coverage = %#v", result.CoverageMatrix["negative"])
	}
}

func TestPionWebRTCMediaLoopbackReceivesSyntheticRTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	viewer, err := NewPionMediaOfferSession(ctx, 2*time.Second)
	if err != nil {
		t.Fatalf("NewPionMediaOfferSession: %v", err)
	}
	defer viewer.Close()
	if offer := viewer.OfferPayload(); !strings.Contains(offer["sdp"], "m=video") || !strings.Contains(offer["sdp"], "recvonly") {
		t.Fatalf("media offer does not advertise recvonly video: %#v", offer)
	}

	answerer, err := NewPionMediaAnswerSession(ctx, viewer.OfferPayload(), 2*time.Second)
	if err != nil {
		t.Fatalf("NewPionMediaAnswerSession: %v", err)
	}
	defer answerer.Close()
	if err := viewer.SetRemoteAnswer(answerer.AnswerPayload()); err != nil {
		t.Fatalf("SetRemoteAnswer: %v", err)
	}

	go func() {
		_ = answerer.SendSyntheticRTP(ctx, 6, 20*time.Millisecond)
	}()
	stats, err := viewer.WaitForMedia(ctx, 3, 3*time.Second)
	if err != nil {
		t.Fatalf("WaitForMedia: %v", err)
	}
	if stats.PacketsReceived < 3 || stats.BytesReceived == 0 {
		t.Fatalf("media stats = %#v, want RTP packets and bytes", stats)
	}
}

func TestRunnerWebRTCMediaRTPRecordsCoverage(t *testing.T) {
	var answerersMu sync.Mutex
	answerers := make([]*PionMediaAnswerSession, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/request_webrtc":
			if got := r.Header.Get("Authorization"); got != "Bearer account-token" {
				t.Fatalf("unexpected account Authorization header %q", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request_webrtc: %v", err)
			}
			offer, ok := body["offer"].(map[string]any)
			if !ok {
				t.Fatalf("missing media offer: %#v", body)
			}
			answerer, err := NewPionMediaAnswerSession(r.Context(), mapStringAnyToStringMap(offer), 2*time.Second)
			if err != nil {
				t.Fatalf("answerer: %v", err)
			}
			answerersMu.Lock()
			answerers = append(answerers, answerer)
			answerersMu.Unlock()
			go func() {
				_ = answerer.SendSyntheticRTP(context.Background(), 8, 20*time.Millisecond)
			}()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":     "ok",
				"session_id": "media-session-1",
				"answer":     answerer.AnswerPayload(),
				"ice_servers": []map[string]any{{
					"urls": []string{"stun:stun.example.test:3478"},
				}},
			})
		case "/api/request_webrtc/close":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	defer func() {
		answerersMu.Lock()
		defer answerersMu.Unlock()
		for _, answerer := range answerers {
			answerer.Close()
		}
	}()

	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:          ProfileSmoke,
		APIURL:           server.URL,
		Actors:           ActorViewer,
		AccountToken:     "account-token",
		WebRTCMediaSet:   WebRTCMediaSetRTP,
		RunID:            "run-media",
		InstanceID:       "instance-media",
		DevicePrefix:     "load-device",
		Duration:         time.Nanosecond,
		VirtualDevices:   1,
		VirtualViewers:   1,
		Iterations:       1,
		HTTPTimeout:      3 * time.Second,
		Thresholds:       Thresholds{MinSuccessRate: 1, RequireCoverageMatrix: true},
		DeviceOnlineMode: DeviceOnlineModeNone,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.CoverageMatrix["webrtc_media"].Status != CoverageStatusPass {
		t.Fatalf("webrtc_media coverage = %#v", result.CoverageMatrix["webrtc_media"])
	}
	if result.WebRTCMedia.PacketsReceived == 0 || result.WebRTCMedia.BytesReceived == 0 {
		t.Fatalf("WebRTCMedia metrics = %#v, want packets and bytes", result.WebRTCMedia)
	}
}

func TestDeviceWebRTCMediaAnswererSubmitsAnswer(t *testing.T) {
	viewer, err := NewPionMediaOfferSession(context.Background(), 2*time.Second)
	if err != nil {
		t.Fatalf("NewPionMediaOfferSession: %v", err)
	}
	defer viewer.Close()
	answerCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/request_webrtc/answer" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer device-token" {
			t.Fatalf("Authorization = %q, want device token", got)
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode answer body: %v", err)
		}
		if body["devid"] != "load-device-0" || body["session_id"] != "session-1" || body["answer"] == nil {
			t.Fatalf("unexpected answer body: %#v", body)
		}
		answerCalled = true
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer server.Close()

	ops, cleanup := NewRunner(server.Client()).answerWebRTCMediaOffer(context.Background(), Config{
		APIURL:      server.URL,
		DeviceToken: "device-token",
		HTTPTimeout: 2 * time.Second,
	}, "load-device-0", webRTCMediaOfferMessage{
		SessionID: "session-1",
		Offer:     viewer.OfferPayload(),
	})
	defer cleanup()
	if !answerCalled {
		t.Fatal("expected /api/request_webrtc/answer to be called")
	}
	if len(ops) == 0 || !ops[0].Success || ops[0].Name != "webrtc_media_answer" {
		t.Fatalf("answer ops = %#v, want successful webrtc_media_answer", ops)
	}
}

func TestRunnerWebRTCMediaUsesPerDeviceAppTokens(t *testing.T) {
	seen := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/request_webrtc" {
			http.NotFound(w, r)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request_webrtc: %v", err)
		}
		devid, _ := body["devid"].(string)
		seen[devid] = r.Header.Get("Authorization")
		http.Error(w, `{"status":"fail","reason":"stop after auth capture"}`, http.StatusBadRequest)
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:          ProfileSmoke,
		APIURL:           server.URL,
		Actors:           ActorViewer,
		AccountToken:     "fallback-app-token",
		AppTokens:        map[string]string{"load-device-0": "app-token-0", "load-device-1": "app-token-1"},
		WebRTCMediaSet:   WebRTCMediaSetRTP,
		RunID:            "run-media-token-map",
		InstanceID:       "instance-media-token-map",
		DevicePrefix:     "load-device",
		Duration:         time.Nanosecond,
		VirtualDevices:   2,
		VirtualViewers:   2,
		Iterations:       1,
		HTTPTimeout:      time.Second,
		DeviceOnlineMode: DeviceOnlineModeNone,
		Thresholds:       Thresholds{MinSuccessRate: 0},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Operations) == 0 {
		t.Fatal("expected media operations")
	}
	if seen["load-device-0"] != "Bearer app-token-0" || seen["load-device-1"] != "Bearer app-token-1" {
		t.Fatalf("request_webrtc auth headers = %#v, want per-device app tokens", seen)
	}
}

func TestRunnerWebRTCMediaAnswersServerOfferAndSendsSyntheticRTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	mediaReceived := make(chan WebRTCMediaStats, 1)
	var sessionsMu sync.Mutex
	sessions := map[string]*PionMediaOfferSession{}
	defer func() {
		sessionsMu.Lock()
		defer sessionsMu.Unlock()
		for _, session := range sessions {
			session.Close()
		}
	}()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/request_webrtc":
			sessionID := fmt.Sprintf("server-offer-session-%d", len(sessions)+1)
			offerSession, err := NewPionMediaOfferSession(ctx, 2*time.Second)
			if err != nil {
				t.Fatalf("NewPionMediaOfferSession: %v", err)
			}
			sessionsMu.Lock()
			sessions[sessionID] = offerSession
			sessionsMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":     "ok",
				"mode":       "webrtc",
				"devid":      "load-device-0",
				"session_id": sessionID,
				"offer":      offerSession.OfferPayload(),
			})
		case "/api/request_webrtc/answer":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request_webrtc answer: %v", err)
			}
			sessionID, _ := body["session_id"].(string)
			sessionsMu.Lock()
			offerSession := sessions[sessionID]
			sessionsMu.Unlock()
			if offerSession == nil {
				t.Fatalf("unknown session id: %s", sessionID)
			}
			answer := mapStringAnyToStringMap(body["answer"].(map[string]any))
			if err := offerSession.SetRemoteAnswer(answer); err != nil {
				t.Fatalf("SetRemoteAnswer: %v", err)
			}
			go func() {
				stats, err := offerSession.WaitForMedia(ctx, 3, 5*time.Second)
				if err == nil {
					mediaReceived <- stats
				}
			}()
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "session_id": "server-offer-session-1"})
		case "/api/request_webrtc/close":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(ctx, Config{
		Profile:          ProfileSmoke,
		APIURL:           server.URL,
		Actors:           ActorViewer,
		AccountToken:     "app-token",
		WebRTCMediaSet:   WebRTCMediaSetRTP,
		RunID:            "run-media-server-offer",
		InstanceID:       "instance-media-server-offer",
		DevicePrefix:     "load-device",
		Duration:         time.Nanosecond,
		VirtualDevices:   1,
		VirtualViewers:   1,
		Iterations:       1,
		HTTPTimeout:      5 * time.Second,
		DeviceOnlineMode: DeviceOnlineModeNone,
		Thresholds:       Thresholds{MinSuccessRate: 1, RequireCoverageMatrix: true},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	select {
	case stats := <-mediaReceived:
		if stats.PacketsReceived < 3 {
			t.Fatalf("server media stats = %#v, want RTP packets", stats)
		}
	case <-ctx.Done():
		t.Fatal("server did not receive synthetic RTP")
	}
	if result.CoverageMatrix["webrtc_media"].Status != CoverageStatusPass {
		t.Fatalf("webrtc_media coverage = %#v", result.CoverageMatrix["webrtc_media"])
	}
	if result.WebRTCMedia.PacketsReceived == 0 || result.WebRTCMedia.BytesReceived == 0 {
		t.Fatalf("WebRTCMedia metrics = %#v, want RTP evidence", result.WebRTCMedia)
	}
}

func mapStringAnyToStringMap(values map[string]any) map[string]string {
	out := make(map[string]string, len(values))
	for key, value := range values {
		if s, ok := value.(string); ok {
			out[key] = s
		}
	}
	return out
}

func failedOperationSummary(operations []Operation) string {
	var parts []string
	for _, op := range operations {
		if op.Success || op.Skipped {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s/%s: %s", op.DeviceID, op.Name, op.ErrorDetail))
	}
	return strings.Join(parts, "; ")
}

func mqttOperationSummary(operations []Operation) string {
	var parts []string
	for _, op := range operations {
		if !strings.HasPrefix(op.Name, "mqtt_") {
			continue
		}
		result := "FAIL"
		if op.Success {
			result = "PASS"
		} else if op.Skipped {
			result = "SKIP"
		}
		detail := op.Evidence
		if detail == "" {
			detail = op.ErrorDetail
		}
		parts = append(parts, fmt.Sprintf("%s/%s/%s/%dms/%s", op.DeviceID, op.Name, result, op.LatencyMS, detail))
	}
	return strings.Join(parts, "; ")
}

func readMQTTPacketForTest(r io.Reader) (byte, []byte, error) {
	header := []byte{0}
	if _, err := io.ReadFull(r, header); err != nil {
		return 0, nil, err
	}
	multiplier := 1
	remaining := 0
	for {
		var encoded [1]byte
		if _, err := io.ReadFull(r, encoded[:]); err != nil {
			return 0, nil, err
		}
		remaining += int(encoded[0]&127) * multiplier
		if encoded[0]&128 == 0 {
			break
		}
		multiplier *= 128
	}
	payload := make([]byte, remaining)
	if _, err := io.ReadFull(r, payload); err != nil {
		return 0, nil, err
	}
	return header[0], payload, nil
}

func readClientWebSocketFrame(reader *bufio.ReadWriter) (byte, []byte, error) {
	header := make([]byte, 2)
	if _, err := io.ReadFull(reader, header); err != nil {
		return 0, nil, err
	}
	opcode := header[0] & 0x0f
	masked := header[1]&0x80 != 0
	length := int(header[1] & 0x7f)
	if length == 126 {
		extended := make([]byte, 2)
		if _, err := io.ReadFull(reader, extended); err != nil {
			return 0, nil, err
		}
		length = int(extended[0])<<8 | int(extended[1])
	} else if length == 127 {
		return 0, nil, fmt.Errorf("test helper does not support 64-bit websocket payload lengths")
	}
	var mask [4]byte
	if masked {
		if _, err := io.ReadFull(reader, mask[:]); err != nil {
			return 0, nil, err
		}
	}
	payload := make([]byte, length)
	if _, err := io.ReadFull(reader, payload); err != nil {
		return 0, nil, err
	}
	if masked {
		for i := range payload {
			payload[i] ^= mask[i%4]
		}
	}
	return opcode, payload, nil
}

func TestDeriveWebSocketURLFromAPIURL(t *testing.T) {
	cases := map[string]string{
		"http://video-cloud-cd.local:18080":        "ws://video-cloud-cd.local:18080",
		"https://video-cloud-cd.local:18443/base/": "wss://video-cloud-cd.local:18443/base",
		"ws://video-cloud-cd.local:18080":          "ws://video-cloud-cd.local:18080",
		"wss://video-cloud-cd.local:18443/ws-base": "wss://video-cloud-cd.local:18443/ws-base",
	}
	for input, want := range cases {
		t.Run(input, func(t *testing.T) {
			got, err := DeriveWebSocketBaseURL(input)
			if err != nil {
				t.Fatalf("DeriveWebSocketBaseURL error = %v", err)
			}
			if got != want {
				t.Fatalf("DeriveWebSocketBaseURL = %q, want %q", got, want)
			}
		})
	}
	if _, err := DeriveWebSocketBaseURL("ftp://example.test"); err == nil {
		t.Fatal("unsupported scheme unexpectedly succeeded")
	}
}

func TestValidateRejectsInvalidActors(t *testing.T) {
	cfg := Config{Profile: "safe-staging", APIURL: "https://example.test", Actors: "device,kitchen"}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "unsupported actor") {
		t.Fatalf("Validate error = %v, want unsupported actor", err)
	}
}

func TestValidateProfilesAndFunctionalDefaults(t *testing.T) {
	cfg := Config{Profile: ProfileFunctional, APIURL: "https://example.test", Actors: "all"}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate functional profile: %v", err)
	}
	if cfg.AppRouteSet != AppRouteSetFunctional || cfg.DeviceRouteSet != DeviceRouteSetFunctional || cfg.DeviceTransportSet != DeviceTransportSetSnapshot || cfg.ViewerRouteSet != ViewerRouteSetFunctional {
		t.Fatalf("functional profile did not expand coverage sets: %#v", cfg)
	}

	for _, profile := range []string{ProfileSmoke, ProfileSafeStaging} {
		t.Run(profile, func(t *testing.T) {
			cfg := Config{Profile: profile, APIURL: "https://example.test", Actors: "app"}
			if err := cfg.Validate(); err != nil {
				t.Fatalf("Validate %s: %v", profile, err)
			}
			if cfg.AppRouteSet != AppRouteSetSmoke {
				t.Fatalf("%s app route set = %q, want smoke", profile, cfg.AppRouteSet)
			}
		})
	}

	if err := (&Config{Profile: ProfileStress, APIURL: "https://example.test"}).Validate(); err == nil || !strings.Contains(err.Error(), "allow-stress") {
		t.Fatalf("stress without opt-in error = %v, want allow-stress", err)
	}
	if err := (&Config{Profile: ProfileSoak, APIURL: "https://example.test"}).Validate(); err == nil || !strings.Contains(err.Error(), "allow-soak") {
		t.Fatalf("soak without opt-in error = %v, want allow-soak", err)
	}
}

func answerForOffer(t *testing.T, rawOffer any) map[string]string {
	t.Helper()
	b, err := json.Marshal(rawOffer)
	if err != nil {
		t.Fatalf("marshal offer: %v", err)
	}
	var offer struct {
		Type string `json:"type"`
		SDP  string `json:"sdp"`
	}
	if err := json.Unmarshal(b, &offer); err != nil {
		t.Fatalf("decode offer: %v", err)
	}
	if offer.Type != "offer" || offer.SDP == "" {
		t.Fatalf("invalid offer: %#v", offer)
	}
	peer, err := webrtc.NewPeerConnection(webrtc.Configuration{})
	if err != nil {
		t.Fatalf("peer: %v", err)
	}
	t.Cleanup(func() { _ = peer.Close() })
	if err := peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeOffer, SDP: offer.SDP}); err != nil {
		t.Fatalf("set remote offer: %v", err)
	}
	answer, err := peer.CreateAnswer(nil)
	if err != nil {
		t.Fatalf("create answer: %v", err)
	}
	if err := peer.SetLocalDescription(answer); err != nil {
		t.Fatalf("set local answer: %v", err)
	}
	return map[string]string{"type": "answer", "sdp": answer.SDP}
}

func TestHTTPTimeoutIsAppliedToOwnedClient(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(100 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer server.Close()

	result, err := NewRunner(nil).Run(context.Background(), Config{
		Profile:        "safe-staging",
		APIURL:         server.URL,
		AccountToken:   "account-token",
		AdminToken:     "admin-token",
		DeviceToken:    "device-token",
		RunID:          "run-1",
		InstanceID:     "instance-1",
		DevicePrefix:   "device",
		Duration:       30 * time.Millisecond,
		HTTPTimeout:    5 * time.Millisecond,
		VirtualDevices: 1,
		VirtualViewers: 0,
		Iterations:     1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Errors[ClassTimeout] == 0 {
		t.Fatalf("expected timeout classification, got errors %#v", result.Errors)
	}
}

func TestMissingDeviceCredentialFailsAsAuth(t *testing.T) {
	cameraEventCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/camera_event" {
			cameraEventCalled = true
			t.Fatal("device request should not be sent without device token")
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
	}))
	defer server.Close()
	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:        "safe-staging",
		APIURL:         server.URL,
		AccountToken:   "account-token",
		AdminToken:     "admin-token",
		RunID:          "run-1",
		InstanceID:     "instance-1",
		DevicePrefix:   "device",
		Duration:       20 * time.Millisecond,
		VirtualDevices: 1,
		VirtualViewers: 0,
		Iterations:     1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Errors[ClassAuth] == 0 {
		t.Fatalf("expected auth error for missing device token, got %#v", result.Errors)
	}
	if cameraEventCalled {
		t.Fatal("camera_event should not have been called")
	}
}

func TestRunnerDeviceFunctionalRoutesCoverDeviceHTTP(t *testing.T) {
	called := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called[r.URL.Path]++
		switch r.URL.Path {
		case "/camera_event":
			if got := r.Header.Get("Authorization"); got != "Bearer device-token" {
				t.Fatalf("camera_event Authorization = %q, want device bearer", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode camera_event: %v", err)
			}
			if body["maintype"] == nil || body["subtype"] == nil || body["eventid"] == nil || body["desc"] == nil {
				t.Fatalf("camera_event body missing fields: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/write_log":
			if got := r.Header.Get("Authorization"); got != "Bearer admin-token" {
				t.Fatalf("write_log Authorization = %q, want admin bearer", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode write_log: %v", err)
			}
			if body["type"] != "eventlog" || body["desc"] == "" {
				t.Fatalf("unexpected write_log body: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/retrieve_log":
			if got := r.Header.Get("Authorization"); got != "Bearer admin-token" {
				t.Fatalf("retrieve_log Authorization = %q, want admin bearer", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "log": []any{}})
		case "/notify_camera":
			if got := r.Header.Get("Authorization"); got != "Bearer admin-token" {
				t.Fatalf("notify_camera Authorization = %q, want admin bearer", got)
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode notify_camera: %v", err)
			}
			data, _ := body["data"].(map[string]any)
			if body["devid"] != "load-device-0" || body["event"] != "loadtest.notify" || data["type"] == "" || data["run_id"] == "" {
				t.Fatalf("notify_camera body missing event fields: %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "command": "notify_camera"})
		case "/start_video_record":
			if got := r.Header.Get("Authorization"); got != "Bearer admin-token" {
				t.Fatalf("start_video_record Authorization = %q, want admin bearer", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "command": "start_video_record"})
		default:
			t.Fatalf("unexpected path for functional device route set: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:             "safe-staging",
		APIURL:              server.URL,
		Actors:              "device",
		DeviceRouteSet:      DeviceRouteSetFunctional,
		DeviceOnlineMode:    DeviceOnlineModeNone,
		AdminToken:          "admin-token",
		DeviceToken:         "device-token",
		RunID:               "run-device-functional",
		InstanceID:          "instance-device-functional",
		DevicePrefix:        "load-device",
		Duration:            time.Nanosecond,
		VirtualDevices:      1,
		Iterations:          1,
		DeviceRatePerSecond: 1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	opsByName := map[string]Operation{}
	for _, op := range result.Operations {
		opsByName[op.Name] = op
		if strings.Contains(op.Evidence, "admin-token") || strings.Contains(op.Evidence, "device-token") {
			t.Fatalf("operation evidence leaked token: %#v", op)
		}
	}
	for _, name := range []string{"camera_event", "write_log", "retrieve_log", "notify_camera", "start_video_record"} {
		op, ok := opsByName[name]
		if !ok {
			t.Fatalf("missing operation %s in %#v", name, result.Operations)
		}
		if !op.Success || op.StatusCode != http.StatusOK {
			t.Fatalf("%s = %#v, want successful HTTP operation", name, op)
		}
	}
	if result.CoverageMatrix["device_http"].Status != CoverageStatusPass {
		t.Fatalf("device_http coverage = %#v", result.CoverageMatrix["device_http"])
	}
	if result.CoverageMatrix["owner_transport"].Status != CoverageStatusNotRun {
		t.Fatalf("owner transport coverage = %#v, want separate NOT_RUN", result.CoverageMatrix["owner_transport"])
	}
	if called["/camera_event"] != 1 || called["/write_log"] != 1 || called["/retrieve_log"] != 1 || called["/notify_camera"] != 1 || called["/start_video_record"] != 1 {
		t.Fatalf("unexpected calls: %#v", called)
	}
}

func TestEvaluateThresholds(t *testing.T) {
	eval := EvaluateResultThresholds(Summary{SuccessRate: 0.8, P95LatencyMS: 250, P99LatencyMS: 500}, WebRTCMetrics{
		SetupLatencyP95MS: 750,
		OpenSessions:      1,
	}, nil, Thresholds{
		MinSuccessRate:           0.95,
		MaxP95Latency:            200,
		MaxP99Latency:            400,
		MaxWebRTCSetupP95Latency: 600,
		MaxOpenWebRTCSessions:    0,
		RequireCoverageMatrix:    true,
	})
	if eval.Passed {
		t.Fatal("threshold unexpectedly passed")
	}
	if len(eval.Failures) != 6 {
		t.Fatalf("failures = %d, want 6: %#v", len(eval.Failures), eval.Failures)
	}
}

func TestRenderMarkdownIncludesGateActorsAndWebRTCPhases(t *testing.T) {
	started := time.Date(2026, 5, 8, 1, 2, 3, 0, time.UTC)
	result := BuildResult(Config{
		Profile:         "safe-staging",
		APIURL:          "https://github-runner.local:8443",
		RunID:           "run-1",
		InstanceID:      "instance-1",
		DevicePrefix:    "device",
		ContractsCommit: "contracts-test",
		Thresholds:      Thresholds{MinSuccessRate: 1},
	}, started, started.Add(time.Second), []Operation{
		{Actor: "app", Name: "get_statistics", Success: true, LatencyMS: 10},
		{Actor: "viewer", Name: "request_webrtc_create", Success: true, LatencyMS: 20},
		{Actor: "viewer", Name: "webrtc_setup", Success: false, ErrorClass: ClassWebRTCSetup, LatencyMS: 20},
	})
	md := RenderMarkdown(result)
	for _, want := range []string{"Threshold Gate", "Actor Metrics", "WebRTC Lifecycle Phases", "webrtc_setup", "contracts-test", "Throughput"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestBuildResultAddsCoverageMatrixForSmokeSubset(t *testing.T) {
	started := time.Date(2026, 5, 8, 1, 2, 3, 0, time.UTC)
	result := BuildResult(Config{
		Profile:         "safe-staging",
		APIURL:          "http://video-cloud-cd.local:18080",
		RunID:           "run-coverage",
		InstanceID:      "instance-coverage",
		DevicePrefix:    "load-device",
		ContractsCommit: "contracts-test",
	}, started, started.Add(time.Second), []Operation{
		{Actor: "device", Name: "device_websocket_owner", DeviceID: "load-device-0", Success: true, LatencyMS: 1},
		{Actor: "device", Name: "camera_event", DeviceID: "load-device-0", Success: true, StatusCode: 200, LatencyMS: 10},
		{Actor: "app", Name: "get_statistics", DeviceID: "load-device-0", Success: true, StatusCode: 200, LatencyMS: 10},
		{Actor: "viewer", Name: "request_webrtc_create", DeviceID: "load-device-0", ViewerID: "viewer-0", Success: true, StatusCode: 200, LatencyMS: 10},
		{Actor: "viewer", Name: "webrtc_setup", DeviceID: "load-device-0", ViewerID: "viewer-0", Success: true, LatencyMS: 10},
		{Actor: "viewer", Name: "request_webrtc_close", DeviceID: "load-device-0", ViewerID: "viewer-0", Success: true, StatusCode: 200, LatencyMS: 10},
	})

	if result.CoverageMatrix["webrtc"].Status != CoverageStatusPass {
		t.Fatalf("webrtc coverage = %#v, want PASS", result.CoverageMatrix["webrtc"])
	}
	if result.CoverageMatrix["device_http"].Status != CoverageStatusPass {
		t.Fatalf("device_http coverage = %#v, want PASS", result.CoverageMatrix["device_http"])
	}
	if result.CoverageMatrix["scale"].Status != CoverageStatusNotRun {
		t.Fatalf("scale coverage = %#v, want NOT_RUN", result.CoverageMatrix["scale"])
	}
	if result.CoverageMatrix["mqtt"].Status != CoverageStatusNotRun {
		t.Fatalf("mqtt coverage = %#v, want NOT_RUN", result.CoverageMatrix["mqtt"])
	}
	md := RenderMarkdown(result)
	for _, want := range []string{"Coverage Matrix", "| webrtc | PASS |", "| mqtt | NOT_RUN |", "current smoke subset"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestBuildResultFailsFunctionalGateOnPartialWebRTCMedia(t *testing.T) {
	started := time.Date(2026, 5, 9, 5, 33, 0, 0, time.UTC)
	ops := make([]Operation, 0)
	for i := 0; i < 10; i++ {
		deviceID := fmt.Sprintf("load-device-%d", i)
		viewerID := fmt.Sprintf("viewer-%d", i)
		ops = append(ops,
			Operation{Actor: ActorViewer, Name: "webrtc_media_offer", DeviceID: deviceID, ViewerID: viewerID, Success: true},
			Operation{Actor: ActorViewer, Name: "webrtc_media_answer", DeviceID: deviceID, ViewerID: viewerID, Success: i < 5, ErrorClass: ClassHTTP, ErrorDetail: "http 400: device not online"},
		)
		if i < 5 {
			ops = append(ops,
				Operation{Actor: ActorViewer, Name: "webrtc_media_ice_connected", DeviceID: deviceID, ViewerID: viewerID, Success: true, LatencyMS: 10},
				Operation{Actor: ActorViewer, Name: "webrtc_media_first_rtp", DeviceID: deviceID, ViewerID: viewerID, Success: true, LatencyMS: 10},
				Operation{Actor: ActorViewer, Name: "webrtc_media_receive", DeviceID: deviceID, ViewerID: viewerID, Success: true, Evidence: "packets=8 bytes=40 receive_ms=100"},
				Operation{Actor: ActorViewer, Name: "webrtc_media_close", DeviceID: deviceID, ViewerID: viewerID, Success: true},
			)
		}
	}
	result := BuildResult(Config{
		Profile:         ProfileFunctional,
		APIURL:          "http://video-cloud-cd.local:18080",
		RunID:           "functional-10dev",
		InstanceID:      "app-viewer",
		DevicePrefix:    "load-device",
		WebRTCMediaSet:  WebRTCMediaSetRTP,
		VirtualDevices:  10,
		VirtualViewers:  10,
		ContractsCommit: "contracts-test",
		Thresholds: Thresholds{
			MinSuccessRate:        0.95,
			RequireCoverageMatrix: true,
		},
	}, started, started.Add(10*time.Minute), ops)
	if result.CoverageMatrix["webrtc_media"].Status != CoverageStatusFail {
		t.Fatalf("webrtc_media coverage = %#v, want FAIL for partial media success", result.CoverageMatrix["webrtc_media"])
	}
	if result.Thresholds.Passed {
		t.Fatalf("threshold gate passed for partial media coverage: %#v", result.Thresholds)
	}
	md := RenderMarkdown(result)
	for _, want := range []string{"| webrtc_media | FAIL |", "coverage family webrtc_media status FAIL"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestValidateClipSet(t *testing.T) {
	cfg := Config{Profile: ProfileSmoke, APIURL: "http://example.test", ClipSet: ClipSetRecordingFunctional}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("recording-functional clip set should validate: %v", err)
	}
	if cfg.ClipSet != ClipSetRecordingFunctional {
		t.Fatalf("clip set = %q, want %q", cfg.ClipSet, ClipSetRecordingFunctional)
	}

	cfg = Config{Profile: ProfileSmoke, APIURL: "http://example.test", ClipSet: "full"}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "unsupported clip set") {
		t.Fatalf("Validate error = %v, want unsupported clip set", err)
	}
}

func TestRecordingClipUploadUsesDeviceTokenAndMultipartContract(t *testing.T) {
	var sawMeta bool
	var sawClip bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/upload_clip" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer device-token" {
			t.Fatalf("upload Authorization = %q, want device bearer", got)
		}
		reader, err := r.MultipartReader()
		if err != nil {
			t.Fatalf("MultipartReader: %v", err)
		}
		for {
			part, err := reader.NextPart()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("NextPart: %v", err)
			}
			switch part.FormName() {
			case "meta":
				sawMeta = true
				var meta map[string]any
				if err := json.NewDecoder(part).Decode(&meta); err != nil {
					t.Fatalf("decode meta: %v", err)
				}
				if meta["devid"] != "load-device-0" || meta["clipid"] != "run-clip-load-device-0" {
					t.Fatalf("unexpected meta: %#v", meta)
				}
			case "clip":
				sawClip = true
				if got := part.Header.Get("Content-Type"); got != "video/mp4" {
					t.Fatalf("clip content type = %q, want video/mp4", got)
				}
				if part.FileName() != "run-clip-load-device-0.mp4" {
					t.Fatalf("clip filename = %q", part.FileName())
				}
				body, err := io.ReadAll(part)
				if err != nil {
					t.Fatalf("read clip: %v", err)
				}
				if !bytes.Contains(body, []byte("ftyp")) {
					t.Fatalf("clip fixture does not look like deterministic mp4 bytes: %x", body)
				}
			default:
				t.Fatalf("unexpected multipart part %q", part.FormName())
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "devid": "load-device-0", "clipid": "run-clip-load-device-0"})
	}))
	defer server.Close()

	op := NewRunner(server.Client()).uploadRecordingClip(context.Background(), Config{
		APIURL:      server.URL,
		RunID:       "run-clip",
		DeviceToken: "device-token",
		HTTPTimeout: time.Second,
	}, "load-device-0")

	if !op.Success {
		t.Fatalf("upload op failed: %#v", op)
	}
	if op.Name != "clip_upload" || op.Actor != ActorDevice {
		t.Fatalf("upload op = %#v, want device clip_upload", op)
	}
	if !sawMeta || !sawClip {
		t.Fatalf("multipart sawMeta=%v sawClip=%v", sawMeta, sawClip)
	}
}

func TestRecordingClipAppLifecycleCoversMetadataDownloadAndCleanup(t *testing.T) {
	called := map[string]int{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called[r.URL.Path]++
		switch r.URL.Path {
		case "/start_video_record":
			if got := r.Header.Get("Authorization"); got != "Bearer admin-token" {
				t.Fatalf("start_video_record Authorization = %q, want admin bearer", got)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		case "/get_clip_info":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "devid": "load-device-0", "clipid": "run-clip-load-device-0"})
		case "/total_clips":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "total": 1})
		case "/enum_clips":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "clips": []map[string]any{{"clipid": "run-clip-load-device-0"}}})
		case "/download/load-device-0/run-clip-load-device-0":
			if got := r.Header.Get("Authorization"); got != "Bearer app-token" {
				t.Fatalf("download Authorization = %q, want app bearer", got)
			}
			switch r.Header.Get("Range") {
			case "bytes=0-15":
				w.Header().Set("Accept-Ranges", "bytes")
				w.Header().Set("Content-Range", "bytes 0-15/32")
				w.WriteHeader(http.StatusPartialContent)
				_, _ = w.Write([]byte("0123456789abcdef"))
			case "bytes=999999-":
				w.Header().Set("Content-Range", "bytes */32")
				w.WriteHeader(http.StatusRequestedRangeNotSatisfiable)
				_, _ = w.Write([]byte(`{"status":"fail","reason":"invalid range"}`))
			default:
				t.Fatalf("unexpected Range header %q", r.Header.Get("Range"))
			}
		case "/delete_clip":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	ops := NewRunner(server.Client()).runRecordingClipAppLifecycle(context.Background(), Config{
		APIURL:       server.URL,
		RunID:        "run-clip",
		AdminToken:   "admin-token",
		AccountToken: "app-token",
		HTTPTimeout:  time.Second,
	}, "load-device-0")

	for _, name := range []string{"recording_request", "clip_info", "clip_total", "clip_enum", "clip_download_range", "clip_download_invalid_range", "clip_delete"} {
		if !operationSucceeded(ops, name) {
			t.Fatalf("%s did not succeed: %#v", name, ops)
		}
	}
	if called["/download/load-device-0/run-clip-load-device-0"] != 2 {
		t.Fatalf("download calls = %d, want 2", called["/download/load-device-0/run-clip-load-device-0"])
	}
}

func TestBuildResultAddsCameraRecordingClipCoverage(t *testing.T) {
	started := time.Date(2026, 5, 9, 10, 0, 0, 0, time.UTC)
	result := BuildResult(Config{
		Profile:         ProfileFunctional,
		APIURL:          "http://video-cloud-cd.local:18080",
		RunID:           "clip-coverage",
		ClipSet:         ClipSetRecordingFunctional,
		ContractsCommit: "contracts-test",
	}, started, started.Add(time.Second), []Operation{
		{Actor: ActorApp, Name: "recording_request", DeviceID: "load-device-0", Success: true},
		{Actor: ActorDevice, Name: "recording_command_receive", DeviceID: "load-device-0", Success: true},
		{Actor: ActorDevice, Name: "clip_upload", DeviceID: "load-device-0", Success: true, Evidence: "clipid=clip-1 bytes=32"},
		{Actor: ActorApp, Name: "clip_total", DeviceID: "load-device-0", Success: true},
		{Actor: ActorApp, Name: "clip_enum", DeviceID: "load-device-0", Success: true},
		{Actor: ActorApp, Name: "clip_info", DeviceID: "load-device-0", Success: true},
		{Actor: ActorApp, Name: "clip_download_range", DeviceID: "load-device-0", Success: true, Evidence: "bytes=16"},
		{Actor: ActorApp, Name: "clip_download_invalid_range", DeviceID: "load-device-0", Success: true},
		{Actor: ActorApp, Name: "clip_delete", DeviceID: "load-device-0", Success: true},
	})

	item := result.CoverageMatrix["camera_recording_clip"]
	if item.Status != CoverageStatusPass {
		t.Fatalf("camera_recording_clip coverage = %#v, want PASS", item)
	}
	md := RenderMarkdown(result)
	if !strings.Contains(md, "| camera_recording_clip | PASS |") {
		t.Fatalf("markdown missing camera_recording_clip PASS:\n%s", md)
	}
}

func operationSucceeded(ops []Operation, name string) bool {
	for _, op := range ops {
		if op.Name == name && op.Success {
			return true
		}
	}
	return false
}

type fakeMQTTBroker struct {
	t        *testing.T
	listener net.Listener
	mu       sync.Mutex
	cond     *sync.Cond
	subs     map[string][]*fakeMQTTClient
	retained map[string][]byte
}

type fakeMQTTClient struct {
	conn net.Conn
	mu   sync.Mutex
}

func newFakeMQTTBroker(t *testing.T) *fakeMQTTBroker {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen fake mqtt broker: %v", err)
	}
	b := &fakeMQTTBroker{
		t:        t,
		listener: listener,
		subs:     map[string][]*fakeMQTTClient{},
		retained: map[string][]byte{},
	}
	b.cond = sync.NewCond(&b.mu)
	go b.acceptLoop()
	return b
}

func (b *fakeMQTTBroker) Addr() string {
	return b.listener.Addr().String()
}

func (b *fakeMQTTBroker) Close() {
	_ = b.listener.Close()
}

func (b *fakeMQTTBroker) WaitForSubscribers(t *testing.T, count int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		b.mu.Lock()
		total := 0
		for _, clients := range b.subs {
			total += len(clients)
		}
		b.mu.Unlock()
		if total >= count {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %d MQTT subscribers, got %d", count, total)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func (b *fakeMQTTBroker) acceptLoop() {
	for {
		conn, err := b.listener.Accept()
		if err != nil {
			return
		}
		go b.handleConn(&fakeMQTTClient{conn: conn})
	}
}

func (b *fakeMQTTBroker) handleConn(client *fakeMQTTClient) {
	defer client.conn.Close()
	for {
		packetType, payload, err := readMQTTPacketForTest(client.conn)
		if err != nil {
			return
		}
		switch packetType & 0xf0 {
		case 0x10:
			client.write([]byte{0x20, 0x02, 0x00, 0x00})
		case 0x80:
			topic := mqttTopicFromSubscribePayloadForTest(payload)
			b.mu.Lock()
			b.subs[topic] = append(b.subs[topic], client)
			retained := append([]byte(nil), b.retained[topic]...)
			b.cond.Broadcast()
			b.mu.Unlock()
			client.write([]byte{0x90, 0x03, 0x00, 0x01, 0x00})
			if len(retained) > 0 {
				client.write(mqttPacket(0x31, append(mqttString(topic), retained...)))
			}
		case 0x30:
			topic, body, err := mqttPublishTopicAndBody(payload)
			if err != nil {
				b.t.Errorf("decode fake mqtt publish: %v", err)
				return
			}
			b.publish(topic, body, packetType&0x01 == 0x01)
		}
	}
}

func (c *fakeMQTTClient) write(packet []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, _ = c.conn.Write(packet)
}

func (b *fakeMQTTBroker) publish(topic string, body []byte, retained bool) {
	b.mu.Lock()
	if retained {
		b.retained[topic] = append([]byte(nil), body...)
	}
	clients := append([]*fakeMQTTClient(nil), b.subs[topic]...)
	b.mu.Unlock()
	packetType := byte(0x30)
	if retained {
		packetType = 0x31
	}
	packet := mqttPacket(packetType, append(mqttString(topic), body...))
	for _, client := range clients {
		client.write(packet)
	}
}

func mqttTopicFromSubscribePayloadForTest(payload []byte) string {
	if len(payload) < 4 {
		return ""
	}
	topicLen := int(payload[2])<<8 | int(payload[3])
	if len(payload) < 4+topicLen {
		return ""
	}
	return string(payload[4 : 4+topicLen])
}

func TestClassifyError(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   []byte
		want   string
	}{
		{"auth", 401, []byte(`{"status":"fail"}`), ClassAuth},
		{"timeout", 408, []byte(`{"status":"timeout"}`), ClassTimeout},
		{"conflict", 409, []byte(`{"status":"conflict"}`), ClassConflict},
		{"gone", 410, []byte(`{"status":"gone"}`), ClassGone},
		{"malformed", 400, []byte(`{not-json`), ClassMalformed},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ClassifyError(tc.status, tc.body, nil); got != tc.want {
				t.Fatalf("ClassifyError = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestValidateWebRTCSetup(t *testing.T) {
	offer, cleanup, err := NewPionOffer()
	if err != nil {
		t.Fatalf("NewPionOffer: %v", err)
	}
	defer cleanup()
	answer := answerForOffer(t, offer)
	validation, err := ValidateWebRTCSetup(map[string]any{
		"answer": answer,
		"ice_servers": []map[string]any{{
			"urls": []string{"stun:stun.example.test:3478"},
		}},
	})
	if err != nil {
		t.Fatalf("ValidateWebRTCSetup: %v", err)
	}
	if validation.ICEServerCount != 1 {
		t.Fatalf("ICE server count = %d, want 1", validation.ICEServerCount)
	}
	emptyValidation, err := ValidateWebRTCSetup(map[string]any{
		"answer":      answer,
		"ice_servers": []any{},
	})
	if err != nil {
		t.Fatalf("empty ICE servers should be allowed for signaling-only validation: %v", err)
	}
	if emptyValidation.ICEServerCount != 0 {
		t.Fatalf("empty ICE server count = %d, want 0", emptyValidation.ICEServerCount)
	}
	serverOfferValidation, err := ValidateWebRTCSetup(map[string]any{
		"offer":       offer,
		"ice_servers": []any{},
	})
	if err != nil {
		t.Fatalf("server offer should be allowed for signaling-only validation: %v", err)
	}
	if serverOfferValidation.ICEServerCount != 0 {
		t.Fatalf("server offer ICE server count = %d, want 0", serverOfferValidation.ICEServerCount)
	}
}
