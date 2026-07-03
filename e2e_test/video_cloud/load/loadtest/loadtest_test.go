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
		case "/api/request_webrtc/ice":
			writeTestICEPreflight(w)
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

func TestSummarizeUnhandledWebSocketTextFrameRedactsBody(t *testing.T) {
	body := []byte(`{"event":"webrtc_offer","data":{"session_id":"s1","offer":{"type":"offer","sdp":"SECRET-SDP"},"ice_servers":[{"username":"turn-user","credential":"turn-pass"}]}}`)

	got := summarizeUnhandledWebSocketTextFrame(body)

	for _, want := range []string{"event=webrtc_offer", "data_keys=ice_servers,offer,session_id"} {
		if !strings.Contains(got, want) {
			t.Fatalf("summary missing %q: %s", want, got)
		}
	}
	for _, leaked := range []string{"SECRET-SDP", "turn-user", "turn-pass"} {
		if strings.Contains(got, leaked) {
			t.Fatalf("summary leaked %q: %s", leaked, got)
		}
	}
}

func TestReadWebSocketFrameAssemblesFragmentedTextMessage(t *testing.T) {
	var stream bytes.Buffer
	writeServerFrameFragment(t, &stream, false, 1, []byte(`{"event":"webrtc_`))
	writeServerFrameFragment(t, &stream, true, 0, []byte(`offer","data":{"session_id":"s1"}}`))

	payload, opcode, err := readWebSocketFrame(&stream)
	if err != nil {
		t.Fatalf("readWebSocketFrame() error = %v", err)
	}
	if opcode != 1 {
		t.Fatalf("opcode = %d, want text", opcode)
	}
	if got := string(payload); got != `{"event":"webrtc_offer","data":{"session_id":"s1"}}` {
		t.Fatalf("payload = %q", got)
	}
}

func writeServerFrameFragment(t *testing.T, w io.Writer, fin bool, opcode byte, payload []byte) {
	t.Helper()
	first := opcode & 0x0f
	if fin {
		first |= 0x80
	}
	header := []byte{first}
	switch {
	case len(payload) < 126:
		header = append(header, byte(len(payload)))
	case len(payload) <= 0xffff:
		header = append(header, 126, byte(len(payload)>>8), byte(len(payload)))
	default:
		t.Fatalf("test payload too large: %d", len(payload))
	}
	if _, err := w.Write(header); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write(payload); err != nil {
		t.Fatal(err)
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

func TestRunnerWebSocketOwnersStartConcurrently(t *testing.T) {
	var mu sync.Mutex
	active := 0
	maxActive := 0
	wsRequests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ws/device":
			mu.Lock()
			active++
			if active > maxActive {
				maxActive = active
			}
			wsRequests++
			mu.Unlock()

			time.Sleep(100 * time.Millisecond)
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("response writer does not support hijack")
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			mu.Lock()
			active--
			mu.Unlock()
			_, _ = fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: test\r\n\r\n")
			go func() {
				<-r.Context().Done()
				_ = conn.Close()
			}()
		case "/camera_event", "/get_statistics":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	deviceIDs := []string{"device-0", "device-1", "device-2", "device-3"}
	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:           "safe-staging",
		APIURL:            server.URL,
		DeviceToken:       "device-token",
		RunID:             "run-ws-owner-concurrent",
		InstanceID:        "instance-ws-owner-concurrent",
		Actors:            "device",
		DeviceOnlineMode:  DeviceOnlineModeWebSocket,
		DeviceIDs:         deviceIDs,
		Duration:          10 * time.Millisecond,
		VirtualDevices:    len(deviceIDs),
		Iterations:        1,
		DeviceConcurrency: len(deviceIDs),
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.Summary.Failures != 0 {
		t.Fatalf("unexpected failures: %#v", result.Operations)
	}
	mu.Lock()
	defer mu.Unlock()
	if wsRequests != len(deviceIDs) {
		t.Fatalf("websocket owner requests = %d, want %d", wsRequests, len(deviceIDs))
	}
	if maxActive < 2 {
		t.Fatalf("max concurrent websocket owner dials = %d, want at least 2", maxActive)
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

func TestWebSocketOwnerKeepaliveInitialDelayIsDeterministicJitter(t *testing.T) {
	oldInterval := webSocketOwnerKeepaliveInterval
	webSocketOwnerKeepaliveInterval = time.Second
	defer func() { webSocketOwnerKeepaliveInterval = oldInterval }()

	first := webSocketOwnerKeepaliveInitialDelay("device-1")
	second := webSocketOwnerKeepaliveInitialDelay("device-1")
	other := webSocketOwnerKeepaliveInitialDelay("device-2")
	if first != second {
		t.Fatalf("jitter is not deterministic: %s != %s", first, second)
	}
	if first < 0 || first >= webSocketOwnerKeepaliveInterval {
		t.Fatalf("jitter %s outside interval %s", first, webSocketOwnerKeepaliveInterval)
	}
	if first == other {
		t.Fatalf("expected different devices to spread keepalive phase, both got %s", first)
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
		case "/api/request_webrtc/ice":
			writeTestICEPreflight(w)
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
	if answerer.CodecMimeType() != webrtc.MimeTypeH264 {
		t.Fatalf("answerer codec = %q, want H.264", answerer.CodecMimeType())
	}
	if answer := answerer.AnswerPayload(); !strings.Contains(answer["sdp"], "H264") {
		t.Fatalf("answer SDP does not negotiate H.264:\n%s", answer["sdp"])
	}
	if err := viewer.SetRemoteAnswer(answerer.AnswerPayload()); err != nil {
		t.Fatalf("SetRemoteAnswer: %v", err)
	}

	go func() {
		_, _ = answerer.SendH264RTP(ctx, 200*time.Millisecond)
	}()
	stats, err := viewer.WaitForMedia(ctx, 3, 3*time.Second)
	if err != nil {
		t.Fatalf("WaitForMedia: %v", err)
	}
	if stats.PacketsReceived < 3 || stats.BytesReceived == 0 {
		t.Fatalf("media stats = %#v, want RTP packets and bytes", stats)
	}
	if stats.FirstH264AccessUnitMS < stats.TimeToFirstRTPMS || !h264AccessUnitEvidenceReady(mapFromStrings(stats.NALTypes)) {
		t.Fatalf("media stats = %#v, want first H.264 access unit timing and SPS/PPS/IDR evidence", stats)
	}
	if stats.SelectedLocalCandidateType == "" || stats.SelectedRemoteCandidateType == "" {
		t.Fatalf("media stats missing selected candidate pair: %#v", stats)
	}
}

func TestPionWebRTCMediaSessionsCanForceRelayICEPolicy(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	viewer, err := NewPionMediaOfferSessionForSetWithICEPolicy(ctx, WebRTCMediaSetH264, WebRTCICEPolicyRelay, 2*time.Second)
	if err != nil {
		t.Fatalf("NewPionMediaOfferSessionForSetWithICEPolicy: %v", err)
	}
	defer viewer.Close()
	if got := viewer.ICETransportPolicy(); got != webrtc.ICETransportPolicyRelay {
		t.Fatalf("viewer ICE policy = %s, want relay", got)
	}

	answerer, err := NewPionMediaAnswerSessionWithICEServersForSetAndPolicy(ctx, viewer.OfferPayload(), nil, WebRTCMediaSetH264, WebRTCICEPolicyRelay, 2*time.Second)
	if err != nil {
		t.Fatalf("NewPionMediaAnswerSessionWithICEServersForSetAndPolicy: %v", err)
	}
	defer answerer.Close()
	if got := answerer.ICETransportPolicy(); got != webrtc.ICETransportPolicyRelay {
		t.Fatalf("answerer ICE policy = %s, want relay", got)
	}
}

func TestRelayCandidateTypeEvidenceFallsBackToInference(t *testing.T) {
	if got := candidateTypeEvidence("", webrtc.ICETransportPolicyRelay); got != "relay_inferred" {
		t.Fatalf("relay policy candidate evidence = %q, want relay_inferred", got)
	}
	if got := candidateTypeEvidence("relay", webrtc.ICETransportPolicyRelay); got != "relay" {
		t.Fatalf("explicit relay candidate evidence = %q, want relay", got)
	}
	if got := candidateTypeEvidence("", webrtc.ICETransportPolicyAll); got != "" {
		t.Fatalf("all policy candidate evidence = %q, want empty", got)
	}
}

func TestParseH264SenderEvidencePreservesCandidateTypes(t *testing.T) {
	evidence := H264RTPEvidence{
		Packets:                     10,
		Bytes:                       20,
		DurationMS:                  30,
		Loops:                       1,
		Frames:                      2,
		NALTypes:                    map[string]bool{"idr": true},
		Packetizations:              map[string]bool{"single-nal": true},
		ReceiveMS:                   40,
		TimeToFirstMS:               5,
		ICEMS:                       6,
		SelectedLocalCandidateType:  "relay_inferred",
		SelectedRemoteCandidateType: "relay_inferred",
		ExpectedSHA256:              "abc123",
	}
	parsed := parseH264SenderEvidence(h264SenderEvidence(evidence))
	if parsed.SelectedLocalCandidateType != "relay_inferred" || parsed.SelectedRemoteCandidateType != "relay_inferred" {
		t.Fatalf("parsed candidate types = %q/%q, want relay_inferred/relay_inferred", parsed.SelectedLocalCandidateType, parsed.SelectedRemoteCandidateType)
	}
}

func TestH264AnnexBSamplePacketizesIntoValidRTPPayloads(t *testing.T) {
	sample, err := h264AnnexBSample(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	packets, evidence, err := packetizeH264AnnexBForRTP(sample, 1200)
	if err != nil {
		t.Fatal(err)
	}
	if len(packets) == 0 || evidence.Packets == 0 || evidence.Bytes == 0 {
		t.Fatalf("packets=%d evidence=%#v, want RTP payloads", len(packets), evidence)
	}
	for _, want := range []string{"sps", "pps", "idr"} {
		if !evidence.HasNALType(want) {
			t.Fatalf("H.264 evidence missing %s: %#v", want, evidence)
		}
	}
	if !strings.Contains(evidence.String(), "codec=h264") || !strings.Contains(evidence.String(), "nal_types=") {
		t.Fatalf("evidence string missing H.264 payload details: %s", evidence.String())
	}
	if err := validateH264RTPPayloads(packets); err != nil {
		t.Fatalf("invalid H.264 RTP payloads: %v", err)
	}
}

func TestH264MediaPlanLoopsTwoSecondFixtureForTwentySeconds(t *testing.T) {
	plan, err := buildH264MediaPlan(20 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Duration != 20*time.Second || plan.Loops != 10 || plan.FrameRate != 30 {
		t.Fatalf("plan = %#v, want 20s/10 loops/30fps", plan)
	}
	if plan.Frames != 600 {
		t.Fatalf("frames = %d, want 600", plan.Frames)
	}
	if len(plan.Packets) == 0 {
		t.Fatal("plan should include RTP packets")
	}
	if err := validateH264RTPSequence(plan.Packets); err != nil {
		t.Fatalf("invalid RTP sequence/timestamps: %v", err)
	}
	if !strings.Contains(plan.Evidence.String(), "duration_ms=20000") || !strings.Contains(plan.Evidence.String(), "loops=10") {
		t.Fatalf("evidence missing 20s loop details: %s", plan.Evidence.String())
	}
}

func TestOpusMediaPlanLoopsTwoSecondFixtureForTwentySeconds(t *testing.T) {
	plan, err := buildOpusMediaPlan(20 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Duration != 20*time.Second || plan.Loops != 10 || plan.SampleRate != 48000 || plan.Channels != 1 {
		t.Fatalf("plan = %#v, want 20s/10 loops/48kHz mono", plan)
	}
	if plan.Frames != 1000 {
		t.Fatalf("frames = %d, want 1000", plan.Frames)
	}
	if len(plan.Packets) != plan.Frames {
		t.Fatalf("packets = %d, want one RTP packet per Opus frame", len(plan.Packets))
	}
	if plan.Evidence.ExpectedSHA256 == "" || plan.Evidence.Bytes == 0 {
		t.Fatalf("evidence = %#v, want expected audio payload hash and bytes", plan.Evidence)
	}
	if !strings.Contains(plan.Evidence.String(), "codec=opus") || !strings.Contains(plan.Evidence.String(), "sample_rate=48000") {
		t.Fatalf("evidence missing Opus details: %s", plan.Evidence.String())
	}
}

func TestPionWebRTCMediaAVLoopbackReceivesVideoAndAudioRTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	viewer, err := NewPionMediaOfferSessionForSet(ctx, WebRTCMediaSetAV, 2*time.Second)
	if err != nil {
		t.Fatalf("NewPionMediaOfferSessionForSet: %v", err)
	}
	defer viewer.Close()
	if offer := viewer.OfferPayload(); !strings.Contains(offer["sdp"], "m=video") || !strings.Contains(offer["sdp"], "m=audio") || !strings.Contains(offer["sdp"], "recvonly") {
		t.Fatalf("AV offer does not advertise recvonly audio+video:\n%s", offer["sdp"])
	}

	answerer, err := NewPionMediaAnswerSessionForSet(ctx, viewer.OfferPayload(), WebRTCMediaSetAV, 2*time.Second)
	if err != nil {
		t.Fatalf("NewPionMediaAnswerSessionForSet: %v", err)
	}
	defer answerer.Close()
	if answer := answerer.AnswerPayload(); !strings.Contains(answer["sdp"], "H264") || !strings.Contains(answer["sdp"], "opus") {
		t.Fatalf("answer SDP does not negotiate H.264 + Opus:\n%s", answer["sdp"])
	}
	if err := viewer.SetRemoteAnswer(answerer.AnswerPayload()); err != nil {
		t.Fatalf("SetRemoteAnswer: %v", err)
	}

	go func() {
		_, _ = answerer.SendAVRTP(ctx, 200*time.Millisecond)
	}()
	stats, err := viewer.WaitForMedia(ctx, 3, 3*time.Second)
	if err != nil {
		t.Fatalf("WaitForMedia: %v", err)
	}
	if stats.H264Bytes == 0 || stats.OpusBytes == 0 || stats.OpusPackets == 0 {
		t.Fatalf("media stats = %#v, want both H.264 and Opus RTP evidence", stats)
	}
}

func TestRunnerWebRTCMediaRTPRecordsCoverage(t *testing.T) {
	var answerersMu sync.Mutex
	answerers := make([]*PionMediaAnswerSession, 0)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/request_webrtc/ice":
			writeTestICEPreflight(w)
		case "/api/request_webrtc":
			if r.Method == http.MethodGet {
				answerersMu.Lock()
				answerer := answerers[len(answerers)-1]
				answerersMu.Unlock()
				_ = json.NewEncoder(w).Encode(map[string]any{
					"status":     "ok",
					"session_id": "media-session-1",
					"answer":     answerer.AnswerPayload(),
				})
				return
			}
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
				_, _ = answerer.SendH264RTP(context.Background(), 200*time.Millisecond)
			}()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":     "ok",
				"session_id": "media-session-1",
				"ice_servers": []map[string]any{{
					"urls":       []string{"turn:turn.example.test:3478?transport=udp"},
					"username":   "turn-user",
					"credential": "turn-secret",
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
		Profile:             ProfileSmoke,
		APIURL:              server.URL,
		Actors:              ActorViewer,
		AccountToken:        "account-token",
		WebRTCMediaSet:      WebRTCMediaSetH264,
		WebRTCMediaDuration: 200 * time.Millisecond,
		RunID:               "run-media",
		InstanceID:          "instance-media",
		DevicePrefix:        "load-device",
		Duration:            time.Nanosecond,
		VirtualDevices:      1,
		VirtualViewers:      1,
		Iterations:          1,
		HTTPTimeout:         3 * time.Second,
		Thresholds:          Thresholds{MinSuccessRate: 1, RequireCoverageMatrix: true},
		DeviceOnlineMode:    DeviceOnlineModeNone,
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
	for _, op := range result.Operations {
		if strings.Contains(op.Evidence, "turn-user") || strings.Contains(op.Evidence, "turn-secret") {
			t.Fatalf("operation evidence leaked TURN credentials: %#v", op)
		}
	}
}

func TestRunnerDeviceRouteSetOffSkipsDeviceHTTP(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/camera_event" {
			called = true
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:          ProfileSmoke,
		APIURL:           server.URL,
		Actors:           ActorDevice,
		DeviceRouteSet:   DeviceRouteSetOff,
		DeviceToken:      "device-token",
		RunID:            "run-device-off",
		InstanceID:       "instance-device-off",
		DevicePrefix:     "load-device",
		Duration:         time.Nanosecond,
		VirtualDevices:   1,
		Iterations:       1,
		DeviceOnlineMode: DeviceOnlineModeNone,
		Thresholds:       Thresholds{MinSuccessRate: 1, RequireCoverageMatrix: true},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if called {
		t.Fatal("camera_event should not be called when device route set is off")
	}
	if result.Summary.Failures != 0 {
		t.Fatalf("failures = %d operations=%#v", result.Summary.Failures, result.Operations)
	}
	if result.CoverageMatrix["device_http"].Status != CoverageStatusNotRun {
		t.Fatalf("device_http coverage = %#v, want NOT_RUN", result.CoverageMatrix["device_http"])
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

	ops, _, cleanup := NewRunner(server.Client()).answerWebRTCMediaOffer(context.Background(), Config{
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

func TestDeviceWebSocketListenerHandlesNextFrameWhileWebRTCAnswerRuns(t *testing.T) {
	viewer, err := NewPionMediaOfferSession(context.Background(), 2*time.Second)
	if err != nil {
		t.Fatalf("NewPionMediaOfferSession: %v", err)
	}
	defer viewer.Close()

	answerStarted := make(chan struct{})
	releaseAnswer := make(chan struct{})
	defer close(releaseAnswer)

	reader, writer := net.Pipe()
	defer reader.Close()
	defer writer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := NewRunner(nil)
	runner.mediaCoord = newWebRTCMediaCoordinator()
	runner.webRTCMediaOfferHandler = func(context.Context, Config, string, webRTCMediaOfferMessage) ([]Operation, time.Time, func()) {
		close(answerStarted)
		<-releaseAnswer
		return []Operation{{Actor: ActorDevice, Name: "webrtc_media_answer", DeviceID: "load-device-0", Success: true}}, time.Now(), func() {}
	}
	records := make(chan Operation, 8)
	done := make(chan error, 1)
	go func() {
		done <- runner.listenDeviceTransportMessages(ctx, Config{
			APIURL:              "https://example.test",
			DeviceToken:         "device-token",
			HTTPTimeout:         5 * time.Second,
			WebRTCMediaSet:      WebRTCMediaSetH264,
			WebRTCMediaDuration: 10 * time.Millisecond,
			RunID:               "run-async-ws",
		}, "load-device-0", reader, func(op Operation) {
			records <- op
		})
	}()

	offerPayload, err := json.Marshal(map[string]any{
		"event": "webrtc_offer",
		"data": map[string]any{
			"session_id": "session-async",
			"offer":      viewer.OfferPayload(),
		},
	})
	if err != nil {
		t.Fatalf("marshal offer: %v", err)
	}
	if err := writeWebSocketFrame(writer, 1, offerPayload); err != nil {
		t.Fatalf("write offer frame: %v", err)
	}
	select {
	case <-answerStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("answer request did not start")
	}

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- writeWebSocketFrame(writer, 1, []byte(`{"event":"after_offer"}`))
	}()
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case err := <-writeDone:
			if err != nil {
				t.Fatalf("write second frame: %v", err)
			}
		case op := <-records:
			if op.Name == "device_websocket_text_unhandled" && strings.Contains(op.Evidence, "event=after_offer") {
				cancel()
				return
			}
		case <-deadline:
			t.Fatal("listener did not process the next websocket frame while WebRTC answer was still running")
		case err := <-done:
			t.Fatalf("listener exited before processing next frame: %v", err)
		}
	}
}

func TestDeviceWebSocketListenerIgnoresDuplicateWebRTCOffers(t *testing.T) {
	viewer, err := NewPionMediaOfferSession(context.Background(), 2*time.Second)
	if err != nil {
		t.Fatalf("NewPionMediaOfferSession: %v", err)
	}
	defer viewer.Close()

	reader, writer := net.Pipe()
	defer reader.Close()
	defer writer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := NewRunner(nil)
	runner.mediaCoord = newWebRTCMediaCoordinator()
	answerCalls := 0
	answerDone := make(chan struct{}, 1)
	runner.webRTCMediaOfferHandler = func(context.Context, Config, string, webRTCMediaOfferMessage) ([]Operation, time.Time, func()) {
		answerCalls++
		answerDone <- struct{}{}
		return []Operation{{Actor: ActorDevice, Name: "webrtc_media_answer", DeviceID: "load-device-0", Success: true}}, time.Now(), func() {}
	}
	records := make(chan Operation, 8)
	done := make(chan error, 1)
	go func() {
		done <- runner.listenDeviceTransportMessages(ctx, Config{
			APIURL:              "https://example.test",
			DeviceToken:         "device-token",
			HTTPTimeout:         5 * time.Second,
			WebRTCMediaSet:      WebRTCMediaSetH264,
			WebRTCMediaDuration: 10 * time.Millisecond,
			RunID:               "run-duplicate-offer",
		}, "load-device-0", reader, func(op Operation) {
			records <- op
		})
	}()

	offerPayload, err := json.Marshal(map[string]any{
		"event": "webrtc_offer",
		"data": map[string]any{
			"session_id": "session-duplicate",
			"offer":      viewer.OfferPayload(),
		},
	})
	if err != nil {
		t.Fatalf("marshal offer: %v", err)
	}
	if err := writeWebSocketFrame(writer, 1, offerPayload); err != nil {
		t.Fatalf("write first offer frame: %v", err)
	}
	select {
	case <-answerDone:
	case <-time.After(5 * time.Second):
		t.Fatal("answer handler did not run for first offer")
	}
	if err := writeWebSocketFrame(writer, 1, offerPayload); err != nil {
		t.Fatalf("write duplicate offer frame: %v", err)
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case op := <-records:
			if op.Name == "webrtc_media_offer_duplicate" {
				if !op.Success || !strings.Contains(op.Evidence, "session_id=session-duplicate") {
					t.Fatalf("duplicate op = %#v", op)
				}
				if answerCalls != 1 {
					t.Fatalf("answer calls = %d, want 1", answerCalls)
				}
				cancel()
				return
			}
		case <-deadline:
			t.Fatal("listener did not record duplicate WebRTC offer")
		case err := <-done:
			t.Fatalf("listener exited before duplicate evidence: %v", err)
		}
	}
}

func TestDeviceWebSocketListenerHandlesNextFrameWhileRecordingUploadRuns(t *testing.T) {
	uploadStarted := make(chan struct{})
	releaseUpload := make(chan struct{})
	defer close(releaseUpload)

	reader, writer := net.Pipe()
	defer reader.Close()
	defer writer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := NewRunner(nil)
	runner.recordingClipUploadHandler = func(context.Context, Config, string) Operation {
		close(uploadStarted)
		<-releaseUpload
		return Operation{Actor: ActorDevice, Name: "clip_upload", DeviceID: "load-device-0", Success: true}
	}
	records := make(chan Operation, 8)
	done := make(chan error, 1)
	go func() {
		done <- runner.listenDeviceTransportMessages(ctx, Config{
			APIURL:  "https://example.test",
			ClipSet: ClipSetRecordingFunctional,
			RunID:   "run-async-recording",
		}, "load-device-0", reader, func(op Operation) {
			records <- op
		})
	}()

	if err := writeWebSocketFrame(writer, 1, []byte(`{"event":"start_recording","data":{"actionid":"action-1","eventid":"event-1"}}`)); err != nil {
		t.Fatalf("write recording frame: %v", err)
	}
	select {
	case <-uploadStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("recording upload did not start")
	}

	writeDone := make(chan error, 1)
	go func() {
		writeDone <- writeWebSocketFrame(writer, 1, []byte(`{"event":"after_recording"}`))
	}()
	deadline := time.After(300 * time.Millisecond)
	sawReceive := false
	for {
		select {
		case err := <-writeDone:
			if err != nil {
				t.Fatalf("write second frame: %v", err)
			}
		case op := <-records:
			if op.Name == "recording_command_receive" && op.Success {
				sawReceive = true
			}
			if sawReceive && op.Name == "device_websocket_text_unhandled" && strings.Contains(op.Evidence, "event=after_recording") {
				cancel()
				return
			}
		case <-deadline:
			t.Fatal("listener did not process the next websocket frame while recording upload was still running")
		case err := <-done:
			t.Fatalf("listener exited before processing next frame: %v", err)
		}
	}
}

func TestDeviceWebSocketListenerReportsWebRTCOfferQueueFull(t *testing.T) {
	viewer, err := NewPionMediaOfferSession(context.Background(), 2*time.Second)
	if err != nil {
		t.Fatalf("NewPionMediaOfferSession: %v", err)
	}
	defer viewer.Close()

	answerStarted := make(chan struct{})
	releaseAnswer := make(chan struct{})
	defer close(releaseAnswer)

	reader, writer := net.Pipe()
	defer reader.Close()
	defer writer.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := NewRunner(nil)
	runner.mediaCoord = newWebRTCMediaCoordinator()
	runner.webRTCMediaOfferHandler = func(context.Context, Config, string, webRTCMediaOfferMessage) ([]Operation, time.Time, func()) {
		select {
		case <-answerStarted:
		default:
			close(answerStarted)
		}
		<-releaseAnswer
		return []Operation{{Actor: ActorDevice, Name: "webrtc_media_answer", DeviceID: "load-device-0", Success: true}}, time.Now(), func() {}
	}
	records := make(chan Operation, 64)
	done := make(chan error, 1)
	go func() {
		done <- runner.listenDeviceTransportMessages(ctx, Config{
			APIURL:              "https://example.test",
			DeviceToken:         "device-token",
			HTTPTimeout:         5 * time.Second,
			WebRTCMediaSet:      WebRTCMediaSetH264,
			WebRTCMediaDuration: 10 * time.Millisecond,
			RunID:               "run-queue-full",
			ViewerConcurrency:   1,
		}, "load-device-0", reader, func(op Operation) {
			records <- op
		})
	}()
	select {
	case <-answerStarted:
	default:
	}

	for i := 0; i < 18; i++ {
		offerPayload, err := json.Marshal(map[string]any{
			"event": "webrtc_offer",
			"data": map[string]any{
				"session_id": fmt.Sprintf("session-full-%02d", i),
				"offer":      viewer.OfferPayload(),
			},
		})
		if err != nil {
			t.Fatalf("marshal offer: %v", err)
		}
		if err := writeWebSocketFrame(writer, 1, offerPayload); err != nil {
			t.Fatalf("write offer frame %d: %v", i, err)
		}
		if i == 0 {
			select {
			case <-answerStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("answer worker did not start")
			}
		}
	}

	deadline := time.After(2 * time.Second)
	for {
		select {
		case op := <-records:
			if op.Name == "webrtc_media_answer" && !op.Success && op.ErrorDetail == "webrtc offer queue full" {
				if op.ErrorClass != ClassWebRTCSetup || !strings.Contains(op.Evidence, "queue_depth=16") {
					t.Fatalf("queue full op = %#v, want setup class and queue depth evidence", op)
				}
				cancel()
				return
			}
		case <-deadline:
			t.Fatal("listener did not report full WebRTC offer queue")
		case err := <-done:
			t.Fatalf("listener exited before queue full evidence: %v", err)
		}
	}
}

func TestRunnerWebRTCMediaUsesPerDeviceAppTokens(t *testing.T) {
	seen := map[string]string{}
	expiries := map[string]float64{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/request_webrtc/ice" {
			writeTestICEPreflight(w)
			return
		}
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
		expiry, _ := body["expiry"].(float64)
		expiries[devid] = expiry
		http.Error(w, `{"status":"fail","reason":"stop after auth capture"}`, http.StatusBadRequest)
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:             ProfileSmoke,
		APIURL:              server.URL,
		Actors:              ActorViewer,
		AccountToken:        "fallback-app-token",
		AppTokens:           map[string]string{"load-device-0": "app-token-0", "load-device-1": "app-token-1"},
		WebRTCMediaSet:      WebRTCMediaSetH264,
		WebRTCMediaDuration: 200 * time.Millisecond,
		RunID:               "run-media-token-map",
		InstanceID:          "instance-media-token-map",
		DevicePrefix:        "load-device",
		Duration:            time.Nanosecond,
		VirtualDevices:      2,
		VirtualViewers:      2,
		Iterations:          1,
		HTTPTimeout:         time.Second,
		DeviceOnlineMode:    DeviceOnlineModeNone,
		Thresholds:          Thresholds{MinSuccessRate: 0},
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
	if expiries["load-device-0"] < 300 || expiries["load-device-1"] < 300 {
		t.Fatalf("request_webrtc expiry = %#v, want at least 300 seconds for relay AV media", expiries)
	}
}

func operationByName(ops []Operation, name string) Operation {
	for _, op := range ops {
		if op.Name == name {
			return op
		}
	}
	return Operation{}
}

func writeTestICEPreflight(w http.ResponseWriter) {
	_ = json.NewEncoder(w).Encode(map[string]any{
		"status":     "ok",
		"mode":       "webrtc",
		"ice_policy": "all",
		"ice_servers": []map[string]any{{
			"urls": []string{"stun:stun.example.test:3478"},
		}},
	})
}

func TestRunnerWebRTCMediaAppOfferReceivesSyntheticRTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	var sessionsMu sync.Mutex
	sessions := map[string]*PionMediaAnswerSession{}
	defer func() {
		sessionsMu.Lock()
		defer sessionsMu.Unlock()
		for _, session := range sessions {
			session.Close()
		}
	}()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/request_webrtc/ice":
			writeTestICEPreflight(w)
		case "/api/request_webrtc":
			if r.Method == http.MethodGet {
				sessionID := r.URL.Query().Get("session_id")
				sessionsMu.Lock()
				answerSession := sessions[sessionID]
				sessionsMu.Unlock()
				if answerSession == nil {
					t.Fatalf("unknown answer session id: %s", sessionID)
				}
				go func() {
					_, _ = answerSession.SendH264RTP(ctx, 200*time.Millisecond)
				}()
				_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "session_id": sessionID, "answer": answerSession.AnswerPayload()})
				return
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request_webrtc: %v", err)
			}
			offer := mapStringAnyToStringMap(body["offer"].(map[string]any))
			sessionID := fmt.Sprintf("app-offer-session-%d", len(sessions)+1)
			answerSession, err := NewPionMediaAnswerSession(ctx, offer, 2*time.Second)
			if err != nil {
				t.Fatalf("NewPionMediaAnswerSession: %v", err)
			}
			sessionsMu.Lock()
			sessions[sessionID] = answerSession
			sessionsMu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "mode": "webrtc", "devid": "load-device-0", "session_id": sessionID, "ice_servers": []map[string]any{}})
		case "/api/request_webrtc/close":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "closed": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(ctx, Config{
		Profile:             ProfileSmoke,
		APIURL:              server.URL,
		Actors:              ActorViewer,
		AccountToken:        "app-token",
		WebRTCMediaSet:      WebRTCMediaSetH264,
		WebRTCMediaDuration: 200 * time.Millisecond,
		RunID:               "run-media-app-offer",
		InstanceID:          "instance-media-app-offer",
		DevicePrefix:        "load-device",
		Duration:            time.Nanosecond,
		VirtualDevices:      1,
		VirtualViewers:      1,
		Iterations:          1,
		HTTPTimeout:         5 * time.Second,
		DeviceOnlineMode:    DeviceOnlineModeNone,
		Thresholds:          Thresholds{MinSuccessRate: 1, RequireCoverageMatrix: true},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.CoverageMatrix["webrtc_media"].Status != CoverageStatusPass {
		t.Fatalf("webrtc_media coverage = %#v", result.CoverageMatrix["webrtc_media"])
	}
	if result.WebRTCMedia.PacketsReceived == 0 || result.WebRTCMedia.BytesReceived == 0 {
		t.Fatalf("WebRTCMedia metrics = %#v, want RTP evidence", result.WebRTCMedia)
	}
	receive := operationByName(result.Operations, "webrtc_media_receive")
	if !strings.Contains(receive.Evidence, "packets=") || !strings.Contains(receive.Evidence, "startup_app_request_to_first_h264_access_unit_ms=") || !strings.Contains(receive.Evidence, "selected_local_candidate_type=") {
		t.Fatalf("receive evidence missing viewer-side media/startup validation: %#v", receive)
	}
}

func TestRunnerWebRTCMediaAppOfferUsesDeviceWebSocketActor(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	answerAuth := make(chan string, 1)
	var mu sync.Mutex
	var wsConn net.Conn
	answers := map[string]map[string]string{}
	defer func() {
		mu.Lock()
		defer mu.Unlock()
		if wsConn != nil {
			_ = wsConn.Close()
		}
	}()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/request_webrtc/ice":
			writeTestICEPreflight(w)
		case "/ws/device":
			hijacker, ok := w.(http.Hijacker)
			if !ok {
				t.Fatal("response writer does not support hijack")
			}
			conn, _, err := hijacker.Hijack()
			if err != nil {
				t.Fatalf("hijack: %v", err)
			}
			mu.Lock()
			wsConn = conn
			mu.Unlock()
			_, _ = fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: test\r\n\r\n")
		case "/api/request_webrtc":
			sessionID := "app-offer-ws-session-1"
			if r.Method == http.MethodGet {
				deadline := time.Now().Add(3 * time.Second)
				for {
					mu.Lock()
					answer := answers[sessionID]
					mu.Unlock()
					if answer != nil {
						_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "session_id": sessionID, "answer": answer})
						return
					}
					if time.Now().After(deadline) {
						http.Error(w, "answer timeout", http.StatusGatewayTimeout)
						return
					}
					time.Sleep(10 * time.Millisecond)
				}
			}
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request_webrtc: %v", err)
			}
			offer := mapStringAnyToStringMap(body["offer"].(map[string]any))
			mu.Lock()
			conn := wsConn
			mu.Unlock()
			if conn == nil {
				t.Fatal("websocket owner not connected before request_webrtc")
			}
			payload, _ := json.Marshal(map[string]any{
				"event":      "webrtc_offer",
				"session_id": sessionID,
				"offer":      offer,
				"ice_servers": []map[string]any{{
					"urls": []string{"stun:stun.example.test:3478"},
				}},
				"data": map[string]any{
					"session_id": sessionID,
					"offer":      offer,
					"ice_servers": []map[string]any{{
						"urls": []string{"stun:stun.example.test:3478"},
					}},
				},
			})
			if err := writeWebSocketFrame(conn, 1, payload); err != nil {
				t.Fatalf("write websocket offer: %v", err)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":      "ok",
				"mode":        "webrtc",
				"devid":       "load-device-0",
				"session_id":  sessionID,
				"ice_servers": []map[string]any{{"urls": []string{"stun:stun.example.test:3478"}}},
			})
		case "/api/request_webrtc/answer":
			answerAuth <- r.Header.Get("Authorization")
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode request_webrtc answer: %v", err)
			}
			sessionID, _ := body["session_id"].(string)
			answer := mapStringAnyToStringMap(body["answer"].(map[string]any))
			mu.Lock()
			answers[sessionID] = answer
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "session_id": sessionID})
		case "/api/request_webrtc/close":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "closed": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(ctx, Config{
		Profile:             ProfileSmoke,
		APIURL:              server.URL,
		Actors:              ActorDevice + "," + ActorViewer,
		AccountToken:        "app-token",
		DeviceToken:         "device-token",
		WebRTCMediaSet:      WebRTCMediaSetH264,
		WebRTCMediaDuration: 200 * time.Millisecond,
		RunID:               "run-media-app-offer-ws",
		InstanceID:          "instance-media-app-offer-ws",
		DevicePrefix:        "load-device",
		Duration:            time.Nanosecond,
		VirtualDevices:      1,
		VirtualViewers:      1,
		Iterations:          1,
		HTTPTimeout:         5 * time.Second,
		DeviceOnlineMode:    DeviceOnlineModeWebSocket,
		DeviceRouteSet:      DeviceRouteSetOff,
		Thresholds:          Thresholds{MinSuccessRate: 1, RequireCoverageMatrix: true},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if got := <-answerAuth; got != "Bearer device-token" {
		t.Fatalf("answer Authorization = %q, want device token", got)
	}
	if op := operationByName(result.Operations, "webrtc_media_offer_receive"); !op.Success || op.Actor != ActorDevice {
		t.Fatalf("offer receive op = %#v, want successful device actor", op)
	}
	var answer Operation
	for _, op := range result.Operations {
		if op.Name == "webrtc_media_answer" && op.Actor == ActorDevice {
			answer = op
			break
		}
	}
	if !answer.Success || answer.Actor != ActorDevice {
		t.Fatalf("answer op = %#v, want successful device actor", answer)
	}
	receive := operationByName(result.Operations, "webrtc_media_receive")
	if !receive.Success || !strings.Contains(receive.Evidence, "startup_app_request_to_first_h264_access_unit_ms=") || !strings.Contains(receive.Evidence, "packets=") {
		t.Fatalf("receive op = %#v, want viewer-side H.264 media evidence", receive)
	}
}

func TestWebRTCRelayRoleDefaultsAndValidation(t *testing.T) {
	cfg := DefaultConfigFromEnv()
	if cfg.WebRTCRelayRole != WebRTCRelayRoleBoth {
		t.Fatalf("default WebRTCRelayRole = %q, want %q", cfg.WebRTCRelayRole, WebRTCRelayRoleBoth)
	}

	cfg = Config{Profile: ProfileSmoke, APIURL: "https://example.test", Actors: ActorAll, WebRTCMediaSet: WebRTCMediaSetAV, WebRTCRelayRole: "kiosk"}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "unsupported webrtc relay role") {
		t.Fatalf("Validate error = %v, want unsupported webrtc relay role", err)
	}
}

func TestWebRTCICEPolicyEnvAndValidation(t *testing.T) {
	t.Setenv("VIDEO_CLOUD_LOAD_WEBRTC_ICE_POLICY", WebRTCICEPolicyRelay)
	cfg := DefaultConfigFromEnv()
	if cfg.WebRTCICEPolicy != WebRTCICEPolicyRelay {
		t.Fatalf("WebRTCICEPolicy = %q, want %q", cfg.WebRTCICEPolicy, WebRTCICEPolicyRelay)
	}

	cfg = Config{Profile: ProfileSmoke, APIURL: "https://example.test", Actors: ActorAll, WebRTCMediaSet: WebRTCMediaSetH264, WebRTCICEPolicy: "direct"}
	err := cfg.Validate()
	if err == nil || !strings.Contains(err.Error(), "unsupported webrtc ICE policy") {
		t.Fatalf("Validate error = %v, want unsupported webrtc ICE policy", err)
	}
}

func TestApplyWebRTCRelayRoleSelectsMediaActors(t *testing.T) {
	for _, tc := range []struct {
		role   string
		app    bool
		device bool
		viewer bool
	}{
		{role: WebRTCRelayRoleBoth, app: false, device: true, viewer: true},
		{role: WebRTCRelayRoleAppOnly, app: false, device: false, viewer: true},
		{role: WebRTCRelayRoleDeviceOnly, app: false, device: true, viewer: false},
	} {
		t.Run(tc.role, func(t *testing.T) {
			enabled := map[string]bool{ActorApp: true, ActorDevice: true, ActorViewer: true}
			cfg := Config{WebRTCMediaSet: WebRTCMediaSetAV, WebRTCRelayRole: tc.role}
			got := cfg.ApplyWebRTCRelayRole(enabled)
			if got[ActorApp] != tc.app || got[ActorDevice] != tc.device || got[ActorViewer] != tc.viewer {
				t.Fatalf("enabled actors = %#v, want app=%t device=%t viewer=%t", got, tc.app, tc.device, tc.viewer)
			}
		})
	}
}

func TestBuildCoverageMatrixPassesDeviceOnlyWebRTCMediaSend(t *testing.T) {
	cfg := Config{WebRTCMediaSet: WebRTCMediaSetAV, WebRTCRelayRole: WebRTCRelayRoleDeviceOnly}
	matrix := BuildCoverageMatrix(cfg, []Operation{
		{Actor: ActorDevice, Name: "device_websocket_owner", DeviceID: "device-1", Success: true},
		{Actor: ActorDevice, Name: "webrtc_media_offer_receive", DeviceID: "device-1", Success: true},
		{Actor: ActorDevice, Name: "webrtc_media_answer", DeviceID: "device-1", Success: true},
		{Actor: ActorDevice, Name: "webrtc_media_ice_connected", DeviceID: "device-1", Success: true},
		{Actor: ActorDevice, Name: "webrtc_media_first_rtp", DeviceID: "device-1", Success: true},
		{Actor: ActorDevice, Name: "webrtc_media_send", DeviceID: "device-1", Success: true},
	})
	if got := matrix["webrtc_media"]; got.Status != CoverageStatusPass {
		t.Fatalf("webrtc_media coverage = %#v, want PASS", got)
	}
}

func TestRunnerDeviceOnlyWebRTCRelayWaitsForDuration(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ws/device" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		hijacker, ok := w.(http.Hijacker)
		if !ok {
			t.Fatal("response writer does not support hijack")
		}
		conn, _, err := hijacker.Hijack()
		if err != nil {
			t.Fatalf("hijack: %v", err)
		}
		defer conn.Close()
		_, _ = fmt.Fprintf(conn, "HTTP/1.1 101 Switching Protocols\r\nUpgrade: websocket\r\nConnection: Upgrade\r\nSec-WebSocket-Accept: test\r\n\r\n")
		_, _ = io.Copy(io.Discard, conn)
	}))
	defer server.Close()

	duration := 50 * time.Millisecond
	start := time.Now()
	result, err := NewRunner(server.Client()).Run(context.Background(), Config{
		Profile:             ProfileSmoke,
		APIURL:              server.URL,
		Actors:              ActorAll,
		AccountToken:        "app-token",
		DeviceToken:         "device-token",
		WebRTCMediaSet:      WebRTCMediaSetAV,
		WebRTCRelayRole:     WebRTCRelayRoleDeviceOnly,
		RunID:               "run-device-only-wait",
		InstanceID:          "instance-device-only-wait",
		DevicePrefix:        "load-device",
		Duration:            duration,
		VirtualDevices:      1,
		VirtualViewers:      1,
		Iterations:          1,
		HTTPTimeout:         time.Second,
		DeviceOnlineMode:    DeviceOnlineModeWebSocket,
		DeviceRouteSet:      DeviceRouteSetOff,
		DeviceRatePerSecond: 1,
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if elapsed := time.Since(start); elapsed < duration {
		t.Fatalf("device-only run elapsed %s, want at least %s", elapsed, duration)
	}
	if result.Actors[ActorViewer].Operations != 0 {
		t.Fatalf("viewer actor unexpectedly ran: %#v", result.Actors)
	}
}

func TestRunnerWebRTCMediaFailsWhenDeviceAnswerMissing(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/request_webrtc/ice":
			writeTestICEPreflight(w)
		case "/api/request_webrtc":
			if r.Method == http.MethodGet {
				http.Error(w, "answer timeout", http.StatusGatewayTimeout)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"status":      "ok",
				"mode":        "webrtc",
				"devid":       "load-device-0",
				"session_id":  "answer-missing-session",
				"ice_servers": []map[string]any{},
			})
		case "/api/request_webrtc/close":
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	result, err := NewRunner(server.Client()).Run(ctx, Config{
		Profile:             ProfileSmoke,
		APIURL:              server.URL,
		Actors:              ActorViewer,
		AccountToken:        "app-token",
		WebRTCMediaSet:      WebRTCMediaSetH264,
		WebRTCMediaDuration: 200 * time.Millisecond,
		RunID:               "run-media-answer-missing",
		InstanceID:          "instance-media-answer-missing",
		DevicePrefix:        "load-device",
		Duration:            time.Nanosecond,
		VirtualDevices:      1,
		VirtualViewers:      1,
		Iterations:          1,
		HTTPTimeout:         5 * time.Second,
		DeviceOnlineMode:    DeviceOnlineModeNone,
		Thresholds:          Thresholds{MinSuccessRate: 1, RequireCoverageMatrix: true},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	answer := operationByName(result.Operations, "webrtc_media_answer")
	if answer.Success || answer.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("answer op = %#v, want answer wait timeout failure", answer)
	}
	if result.CoverageMatrix["webrtc_media"].Status != CoverageStatusFail {
		t.Fatalf("webrtc_media coverage = %#v, want FAIL", result.CoverageMatrix["webrtc_media"])
	}
}

func TestPostWebRTCMediaRequestRetriesDeviceNotOnline(t *testing.T) {
	var mu sync.Mutex
	attempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/request_webrtc" {
			http.NotFound(w, r)
			return
		}
		mu.Lock()
		attempts++
		attempt := attempts
		mu.Unlock()
		if attempt < 3 {
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "fail", "reason": "device not online"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok", "session_id": "session-1"})
	}))
	defer server.Close()

	op := NewRunner(server.Client()).postWebRTCMediaRequestWithOnlineRetry(context.Background(), Config{
		APIURL:      server.URL,
		HTTPTimeout: time.Second,
	}, "load-device-0", "viewer-0", map[string]any{"devid": "load-device-0"}, "app-token")
	if !op.Success {
		t.Fatalf("operation = %#v, want retry success", op)
	}
	if !strings.Contains(op.Evidence, "request_webrtc_attempts=3") {
		t.Fatalf("evidence = %q, want retry attempt count", op.Evidence)
	}
	mu.Lock()
	defer mu.Unlock()
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

func TestAVReceiverCompareFailsWithoutAudioPayloadStats(t *testing.T) {
	videoPlan, err := buildH264MediaPlan(200 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	audioPlan, err := buildOpusMediaPlan(200 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	closeOp := Operation{
		Success: true,
		Evidence: fmt.Sprintf(`{"status":"ok","media":{"packets_received":%d,"bytes_received":%d,"h264_packets":%d,"h264_sha256":%q,"h264_bytes":%d,"nal_types":["idr","sps"]}}`,
			len(videoPlan.Packets), videoPlan.Evidence.Bytes, len(videoPlan.Packets), videoPlan.Evidence.ExpectedSHA256, videoPlan.Evidence.ReceiverBytes),
	}
	op := avReceiverCompareOperation("load-device-0", "viewer-0", 10, AVRTPEvidence{Video: videoPlan.Evidence, Audio: audioPlan.Evidence}, closeOp)
	if op.Success || op.ErrorDetail != "receiver Opus payload hash mismatch" {
		t.Fatalf("receive op = %#v, want audio payload mismatch failure", op)
	}
	if !strings.Contains(op.Evidence, "video_receiver_bitstream_match=true") || !strings.Contains(op.Evidence, "audio_payload_match=false") {
		t.Fatalf("receive evidence should show video matched but audio failed: %s", op.Evidence)
	}
}

func TestH264ReceiverCompareAcceptsCompleteNALTypesWhenHashDiffers(t *testing.T) {
	videoPlan, err := buildH264MediaPlan(200 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	closeOp := Operation{
		Success: true,
		Evidence: fmt.Sprintf(`{"status":"ok","media":{"packets_received":%d,"bytes_received":%d,"h264_packets":%d,"h264_sha256":"different","h264_bytes":%d,"nal_types":["idr","non-idr","pps","sps"]}}`,
			len(videoPlan.Packets), videoPlan.Evidence.Bytes, len(videoPlan.Packets), videoPlan.Evidence.Bytes),
	}
	op := h264ReceiverCompareOperation("load-device-0", "viewer-0", 10, videoPlan.Evidence, closeOp)
	if !op.Success {
		t.Fatalf("receive op = %#v, want success with complete H.264 RTP evidence", op)
	}
	if !strings.Contains(op.Evidence, "receiver_bitstream_match=false") {
		t.Fatalf("receive evidence should retain hash mismatch detail: %s", op.Evidence)
	}
}

func TestH264ReceiverCompareAcceptsAccessUnitEvidenceWithoutHash(t *testing.T) {
	videoPlan, err := buildH264MediaPlan(200 * time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	closeOp := Operation{
		Success: true,
		Evidence: fmt.Sprintf(`{"status":"ok","media":{"packets_received":%d,"bytes_received":%d,"h264_packets":%d,"h264_bytes":%d,"nal_types":["idr","non-idr","pps","sps"]}}`,
			len(videoPlan.Packets), videoPlan.Evidence.Bytes, len(videoPlan.Packets), videoPlan.Evidence.Bytes),
	}
	op := h264ReceiverCompareOperation("load-device-0", "viewer-0", 10, videoPlan.Evidence, closeOp)
	if !op.Success {
		t.Fatalf("receive op = %#v, want success with complete H.264 access-unit evidence", op)
	}
	if !strings.Contains(op.Evidence, "receiver_bitstream_match=false") {
		t.Fatalf("receive evidence should retain missing hash as non-match detail: %s", op.Evidence)
	}
}

func TestWebRTCMediaDrainDelayAllowsInFlightRelayPackets(t *testing.T) {
	if got := webRTCMediaDrainDelay(20 * time.Second); got != 5*time.Second {
		t.Fatalf("20s media drain delay = %s, want 5s", got)
	}
	if got := webRTCMediaDrainDelay(60 * time.Second); got != 5*time.Second {
		t.Fatalf("60s media drain delay = %s, want 5s", got)
	}
	if got := webRTCMediaDrainDelay(200 * time.Millisecond); got != 20*time.Millisecond {
		t.Fatalf("short media drain delay = %s, want 20ms", got)
	}
	if got := webRTCMediaDrainDelay(0); got != 0 {
		t.Fatalf("disabled media drain delay = %s, want 0", got)
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

func TestBuildResultAllowsThresholdToleratedPartialWebRTCMedia(t *testing.T) {
	started := time.Date(2026, 5, 9, 5, 33, 0, 0, time.UTC)
	ops := make([]Operation, 0)
	for i := 0; i < 10; i++ {
		deviceID := fmt.Sprintf("load-device-%d", i)
		viewerID := fmt.Sprintf("viewer-%d", i)
		ops = append(ops,
			Operation{Actor: ActorViewer, Name: "webrtc_media_offer", DeviceID: deviceID, ViewerID: viewerID, Success: true},
			Operation{Actor: ActorViewer, Name: "webrtc_media_answer", DeviceID: deviceID, ViewerID: viewerID, Success: i < 9, ErrorClass: ClassHTTP, ErrorDetail: "http 400: device not online"},
		)
		if i < 9 {
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
	if result.CoverageMatrix["webrtc_media"].Status != CoverageStatusPass {
		t.Fatalf("webrtc_media coverage = %#v, want PASS for threshold-tolerated partial media success", result.CoverageMatrix["webrtc_media"])
	}
	if !result.Thresholds.Passed {
		t.Fatalf("threshold gate failed for threshold-tolerated partial media success: %#v", result.Thresholds)
	}
	md := RenderMarkdown(result)
	for _, want := range []string{"| webrtc_media | PASS |", "RTP media received for 9/10 attempted devices"} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestBuildResultFailsWhenWebRTCMediaSuccessRateBelowThreshold(t *testing.T) {
	started := time.Date(2026, 5, 9, 5, 33, 0, 0, time.UTC)
	ops := make([]Operation, 0)
	for i := 0; i < 10; i++ {
		deviceID := fmt.Sprintf("load-device-%d", i)
		viewerID := fmt.Sprintf("viewer-%d", i)
		ops = append(ops,
			Operation{Actor: ActorViewer, Name: "webrtc_media_offer", DeviceID: deviceID, ViewerID: viewerID, Success: true},
			Operation{Actor: ActorViewer, Name: "webrtc_media_answer", DeviceID: deviceID, ViewerID: viewerID, Success: i < 9, ErrorClass: ClassHTTP, ErrorDetail: "http 400: device not online"},
		)
		if i < 9 {
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
			MinSuccessRate:            0.95,
			MinWebRTCMediaSuccessRate: 0.995,
			RequireCoverageMatrix:     true,
		},
	}, started, started.Add(10*time.Minute), ops)
	if result.CoverageMatrix["webrtc_media"].Status != CoverageStatusPass {
		t.Fatalf("webrtc_media coverage = %#v, want PASS for exercised media family", result.CoverageMatrix["webrtc_media"])
	}
	if result.Thresholds.Passed {
		t.Fatalf("threshold gate passed for media success below threshold: %#v", result.Thresholds)
	}
	if got := strings.Join(result.Thresholds.Failures, "\n"); !strings.Contains(got, "WebRTC media success rate 90.00% is below threshold 99.50% (9/10)") {
		t.Fatalf("threshold failures missing media success rate:\n%s", got)
	}
}

func TestBuildResultCountsWebRTCMediaCloseAsClosedSession(t *testing.T) {
	started := time.Date(2026, 5, 9, 5, 33, 0, 0, time.UTC)
	result := BuildResult(Config{}, started, started.Add(time.Second), []Operation{
		{Actor: ActorViewer, Name: "request_webrtc_create", DeviceID: "device-1", ViewerID: "viewer-1", Success: true},
		{Actor: ActorViewer, Name: "webrtc_media_close", DeviceID: "device-1", ViewerID: "viewer-1", Success: true},
	})

	if result.WebRTC.Close.Successes != 1 {
		t.Fatalf("webrtc close successes = %d, want 1", result.WebRTC.Close.Successes)
	}
	if result.WebRTC.OpenSessions != 0 {
		t.Fatalf("open sessions = %d, want 0", result.WebRTC.OpenSessions)
	}
}

func TestCompleteServerOfferWebRTCMediaClosesSessionOnSetupFailure(t *testing.T) {
	closeCalled := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/request_webrtc/close":
			closeCalled = true
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	response := map[string]any{"session_id": "session-setup-fail"}
	ops := NewRunner(server.Client()).completeServerOfferWebRTCMedia(context.Background(), Config{
		APIURL:         server.URL,
		AccountToken:   "account-token",
		WebRTCMediaSet: WebRTCMediaSetH264,
		HTTPTimeout:    time.Second,
	}, "device-1", "viewer-1", response, map[string]string{"type": "offer", "sdp": "invalid"}, "account-token", videoStartupClock{})

	if !closeCalled {
		t.Fatal("close endpoint was not called after media setup failure")
	}
	if len(ops) != 2 {
		t.Fatalf("ops len = %d, want setup failure and close: %#v", len(ops), ops)
	}
	if ops[0].Name != "webrtc_media_ice_connected" || ops[0].Success {
		t.Fatalf("first op = %#v, want failed setup/ICE operation", ops[0])
	}
	if ops[1].Name != "webrtc_media_close" || !ops[1].Success {
		t.Fatalf("close op = %#v, want successful webrtc_media_close", ops[1])
	}
}

func TestBuildResultSummarizesVideoStartupLatency(t *testing.T) {
	started := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	ops := []Operation{
		{
			Actor:    ActorViewer,
			Name:     "webrtc_media_receive",
			DeviceID: "dev-1",
			ViewerID: "viewer-1",
			Success:  true,
			Evidence: "session_id=sess-1 ice_policy=relay selected_local_candidate_type=relay selected_remote_candidate_type=relay packets=30 bytes=3000 receiver_packets=30 receiver_bytes=3000 receive_ms=240 startup_api_create_ms=40 startup_offer_delivery_ms=20 startup_device_answer_ms=30 startup_remote_answer_set_ms=120 startup_ice_connected_since_session_start_ms=150 startup_ice_check_ms=30 startup_first_rtp_after_ice_ms=10 startup_first_h264_access_unit_after_rtp_ms=5 startup_app_request_to_first_rtp_ms=180 startup_app_request_to_first_h264_access_unit_ms=185",
		},
		{
			Actor:    ActorViewer,
			Name:     "webrtc_media_receive",
			DeviceID: "dev-2",
			ViewerID: "viewer-2",
			Success:  true,
			Evidence: "session_id=sess-2 ice_policy=relay selected_local_candidate_type=relay selected_remote_candidate_type=relay packets=40 bytes=4000 receiver_packets=40 receiver_bytes=4000 receive_ms=260 startup_api_create_ms=50 startup_offer_delivery_ms=25 startup_device_answer_ms=35 startup_remote_answer_set_ms=140 startup_ice_connected_since_session_start_ms=175 startup_ice_check_ms=35 startup_first_rtp_after_ice_ms=12 startup_first_h264_access_unit_after_rtp_ms=7 startup_app_request_to_first_rtp_ms=210 startup_app_request_to_first_h264_access_unit_ms=217",
		},
	}

	result := BuildResult(Config{RunID: "run-startup", WebRTCMediaSet: WebRTCMediaSetH264, WebRTCICEPolicy: WebRTCICEPolicyRelay}, started, started.Add(time.Second), ops)

	if len(result.VideoStartupLatency) != 2 {
		t.Fatalf("startup samples = %d, want 2: %#v", len(result.VideoStartupLatency), result.VideoStartupLatency)
	}
	if got := result.VideoStartupLatency[0].SessionID; got != "sess-1" {
		t.Fatalf("first session_id = %q, want sess-1", got)
	}
	if got := result.VideoStartupLatency[0].AppRequestToFirstH264AccessUnitMS; got != 185 {
		t.Fatalf("first app->h264 AU = %d, want 185", got)
	}
	if result.WebRTCMedia.AppRequestToFirstH264AccessUnitP95MS != 217 {
		t.Fatalf("h264 AU p95 = %d, want 217", result.WebRTCMedia.AppRequestToFirstH264AccessUnitP95MS)
	}
	if result.WebRTCMedia.AppRequestToFirstH264AccessUnitP99MS != 217 {
		t.Fatalf("h264 AU p99 = %d, want 217", result.WebRTCMedia.AppRequestToFirstH264AccessUnitP99MS)
	}
	if result.WebRTCMedia.VideoStartupLatency.Samples != 2 || result.WebRTCMedia.VideoStartupLatency.H264AccessUnitSamples != 2 {
		t.Fatalf("startup sample counts = %d/%d, want 2/2", result.WebRTCMedia.VideoStartupLatency.Samples, result.WebRTCMedia.VideoStartupLatency.H264AccessUnitSamples)
	}
	if result.WebRTCMedia.BreakdownP95.DeviceAnswerMS != 35 {
		t.Fatalf("device answer p95 = %d, want 35", result.WebRTCMedia.BreakdownP95.DeviceAnswerMS)
	}
	if result.WebRTCMedia.BreakdownP95.RemoteAnswerSetMS != 140 {
		t.Fatalf("remote answer set p95 = %d, want 140", result.WebRTCMedia.BreakdownP95.RemoteAnswerSetMS)
	}
	if result.WebRTCMedia.BreakdownP95.ICECheckMS != 35 {
		t.Fatalf("ICE check p95 = %d, want 35", result.WebRTCMedia.BreakdownP95.ICECheckMS)
	}
	if result.WebRTCMedia.BreakdownP95.ICEConnectedSinceSessionStartMS != 175 {
		t.Fatalf("ICE connected since session start p95 = %d, want 175", result.WebRTCMedia.BreakdownP95.ICEConnectedSinceSessionStartMS)
	}

	md := RenderMarkdown(result)
	for _, want := range []string{
		"## Video Startup Latency",
		"H.264 access unit samples: 2",
		"| App request -> first RTP | 180 ms | 210 ms | 210 ms |",
		"| App request -> first H.264 access unit | 185 ms | 217 ms | 217 ms |",
		"| Device answer | 35 ms |",
		"| Remote answer set | 140 ms |",
		"| ICE check | 35 ms |",
		"| ICE connected since session start | 175 ms |",
	} {
		if !strings.Contains(md, want) {
			t.Fatalf("markdown missing %q:\n%s", want, md)
		}
	}
}

func TestBuildResultIgnoresMissingH264StartupEvidence(t *testing.T) {
	started := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	ops := []Operation{
		{
			Actor:    ActorViewer,
			Name:     "webrtc_media_receive",
			DeviceID: "dev-1",
			ViewerID: "viewer-1",
			Success:  true,
			Evidence: "session_id=sess-missing ice_policy=relay selected_local_candidate_type=relay selected_remote_candidate_type=relay packets=30 bytes=3000 receiver_packets=30 receiver_bytes=3000 receive_ms=240 startup_api_create_ms=40 startup_device_answer_ms=30 startup_ice_connected_since_session_start_ms=80",
		},
	}

	result := BuildResult(Config{RunID: "run-startup-zero", WebRTCMediaSet: WebRTCMediaSetH264, WebRTCICEPolicy: WebRTCICEPolicyRelay}, started, started.Add(time.Second), ops)

	if len(result.VideoStartupLatency) != 0 {
		t.Fatalf("startup samples = %d, want 0: %#v", len(result.VideoStartupLatency), result.VideoStartupLatency)
	}
	summary := result.WebRTCMedia.VideoStartupLatency
	if summary.Samples != 0 || summary.H264AccessUnitSamples != 0 {
		t.Fatalf("startup sample counts = %d/%d, want 0/0", summary.Samples, summary.H264AccessUnitSamples)
	}
}

func TestVideoStartupDeviceOwnerAddsRTPAfterICEToICEConnect(t *testing.T) {
	started := time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)
	startup := videoStartupClock{
		RunID:               "run-1",
		SessionID:           "sess-1",
		DeviceID:            "dev-1",
		ViewerID:            "viewer-1",
		ICEPolicy:           WebRTCICEPolicyRelay,
		AppRequestStartedAt: started.Add(100 * time.Millisecond),
		AppRequestOffsetMS:  100,
	}
	closeEvidence := `{"media":{"packets_received":10,"bytes_received":1000,"h264_packets":10,"h264_bytes":1000,"h264_sha256":"abc","nal_types":["sps","pps","idr","non-idr"]}}`
	evidence := startup.EvidenceForDeviceOwner("packets=10 bytes=1000 ice_ms=1200 remote_description_set_ms=900 ttfb_ms=35 selected_local_candidate_type=relay selected_remote_candidate_type=relay", closeEvidence)

	if got := evidenceInt64(evidence, "startup_app_request_to_first_rtp_ms"); got != 1335 {
		t.Fatalf("app request -> first RTP = %d, want 1335", got)
	}
	if got := evidenceInt64(evidence, "startup_app_request_to_first_h264_access_unit_ms"); got != 1335 {
		t.Fatalf("app request -> first H264 AU = %d, want 1335", got)
	}
	if got := evidenceInt64(evidence, "startup_first_rtp_after_ice_ms"); got != 35 {
		t.Fatalf("first RTP after ICE = %d, want 35", got)
	}
	if got := evidenceInt64(evidence, "startup_ice_connected_since_session_start_ms"); got != 1200 {
		t.Fatalf("ICE connected since session start = %d, want 1200", got)
	}
	if got := evidenceInt64(evidence, "startup_remote_answer_set_ms"); got != 900 {
		t.Fatalf("remote answer set = %d, want 900", got)
	}
	if got := evidenceInt64(evidence, "startup_ice_check_ms"); got != 300 {
		t.Fatalf("ICE check = %d, want 300", got)
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

func TestRequestJSONRawKeepsSecretsOutOfEvidence(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"status": "ok",
			"ice_servers": []map[string]any{{
				"urls":       []string{"turn:turn.example.test:3478?transport=udp"},
				"username":   "turn-user",
				"credential": "turn-secret",
			}},
		})
	}))
	defer srv.Close()

	runner := NewRunner(srv.Client())
	cfg := Config{APIURL: srv.URL, HTTPTimeout: time.Second}
	op, raw := runner.getRaw(context.Background(), cfg, ActorViewer, "webrtc_ice_preflight", "device-1", "viewer-1", "/ice", "app-token")
	if !op.Success {
		t.Fatalf("getRaw success = false: class=%s detail=%s", op.ErrorClass, op.ErrorDetail)
	}
	if strings.Contains(op.Evidence, "turn-user") || strings.Contains(op.Evidence, "turn-secret") {
		t.Fatalf("evidence leaked TURN credentials: %s", op.Evidence)
	}
	if !strings.Contains(string(raw), "turn-user") || !strings.Contains(string(raw), "turn-secret") {
		t.Fatalf("raw response did not preserve TURN credentials: %s", string(raw))
	}
}
