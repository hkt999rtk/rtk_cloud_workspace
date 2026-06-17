package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestValidateDeviceBindAllowsMissingClaimIDWhenProvisionIdentifiersExist(t *testing.T) {
	root := t.TempDir()
	bindPath := filepath.Join(root, "bind.json")
	outDir := filepath.Join(root, "out")
	data := `{
  "brandname":"RTK",
  "brand_cloud_id":"brand-1",
  "assignments":[
    {
      "assignment_index":0,
      "assigned_email":"rtk+001@users.local",
      "device_id":"load-device-0001",
      "device_type":"camera",
      "category":"ip_camera",
      "service_options":["mqtt","video_streaming","video_storage"],
      "claim_id":"",
      "account_device_id":"account-device-1",
      "operation_id":"operation-1",
      "status":"provision_requested"
    },
    {
      "assignment_index":1,
      "assigned_email":"rtk+002@users.local",
      "device_id":"load-device-0002",
      "device_type":"light",
      "category":"mqtt_device",
      "service_options":["mqtt"],
      "claim_id":"",
      "account_device_id":"account-device-2",
      "operation_id":"operation-2",
      "status":"provision_requested"
    }
  ]
}`
	if err := os.WriteFile(bindPath, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runValidateDeviceBind([]string{
		"--bind-artifact", bindPath,
		"--out-dir", outDir,
		"--expected-count", "2",
		"--expected-devices-per-user", "1",
	})
	if err != nil {
		t.Fatalf("runValidateDeviceBind() error = %v", err)
	}
}

func TestValidateDeviceBindAllowsAlreadyBoundDevicesWithoutOperationID(t *testing.T) {
	root := t.TempDir()
	bindPath := filepath.Join(root, "bind.json")
	outDir := filepath.Join(root, "out")
	data := `{
  "brandname":"RTK",
  "brand_cloud_id":"brand-1",
  "assignments":[
    {
      "assignment_index":0,
      "assigned_email":"rtk+001@users.local",
      "device_id":"load-device-0001",
      "device_type":"camera",
      "category":"ip_camera",
      "service_options":["mqtt","video_streaming","video_storage"],
      "claim_id":"",
      "account_device_id":"account-device-1",
      "operation_id":"",
      "status":"already_bound"
    }
  ]
}`
	if err := os.WriteFile(bindPath, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runValidateDeviceBind([]string{
		"--bind-artifact", bindPath,
		"--out-dir", outDir,
		"--expected-count", "1",
		"--expected-devices-per-user", "1",
	})
	if err != nil {
		t.Fatalf("runValidateDeviceBind() error = %v", err)
	}
}

func TestAccountIndexDevicesByVideoCloudDevidPaginatesThroughServerCappedPages(t *testing.T) {
	requests := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r.URL.RawQuery)
		limit := r.URL.Query().Get("limit")
		offset := r.URL.Query().Get("offset")
		if limit != "200" {
			t.Fatalf("limit = %q, want 200", limit)
		}
		devices := []map[string]any{}
		switch offset {
		case "0":
			devices = makeIndexedDevices(1, 200)
		case "200":
			devices = makeIndexedDevices(201, 200)
		case "400":
			devices = makeIndexedDevices(401, 3)
		default:
			t.Fatalf("unexpected offset %q", offset)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"devices": devices})
	}))
	defer server.Close()

	index, count, err := accountIndexDevicesByVideoCloudDevid(accountManagerContext{BaseURL: server.URL}, "token", "brand-1")
	if err != nil {
		t.Fatal(err)
	}
	if count != 403 || len(index) != 403 {
		t.Fatalf("count=%d len(index)=%d, want 403", count, len(index))
	}
	if index["load-device-0001"] == nil || index["load-device-0201"] == nil || index["load-device-0403"] == nil {
		t.Fatalf("index did not include devices from every page: keys=%d", len(index))
	}
	if len(requests) != 3 {
		t.Fatalf("requests=%v, want 3 pages", requests)
	}
}

func TestAccountIndexDevicesByVideoCloudDevidRefreshesBrandCloudUserTokenOnUnauthorized(t *testing.T) {
	oldAccessToken := testJWT(time.Now().Add(time.Hour))
	newAccessToken := testJWT(time.Now().Add(2 * time.Hour))
	refreshCount := 0
	indexAttempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/brand-clouds/rtk-test/auth/refresh":
			refreshCount++
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": map[string]string{"access_token": newAccessToken, "refresh_token": testJWT(time.Now().Add(24 * time.Hour))}})
		case "/v1/orgs/brand-1/devices":
			indexAttempts++
			switch r.Header.Get("authorization") {
			case "Bearer " + oldAccessToken:
				http.Error(w, `{"code":"invalid_token","message":"Invalid bearer token"}`, http.StatusUnauthorized)
			case "Bearer " + newAccessToken:
				_ = json.NewEncoder(w).Encode(map[string]any{"devices": makeIndexedDevices(1, 1)})
			default:
				t.Fatalf("authorization header = %q", r.Header.Get("authorization"))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	user := &brandCloudUserSession{
		Email: "rtk+001@users.local",
		Session: accountPlatformSession{
			AccessToken:  oldAccessToken,
			RefreshToken: testJWT(time.Now().Add(time.Hour)),
		},
	}
	index, count, err := accountIndexDevicesByVideoCloudDevidWithBrandCloudUserRetry(accountManagerContext{BaseURL: server.URL}, "rtk-test", "brand-1", user, func(string, ...any) {})
	if err != nil {
		t.Fatalf("accountIndexDevicesByVideoCloudDevidWithBrandCloudUserRetry() error = %v", err)
	}
	if refreshCount != 1 || indexAttempts != 2 || user.Session.AccessToken != newAccessToken {
		t.Fatalf("refreshCount=%d indexAttempts=%d session=%+v", refreshCount, indexAttempts, user.Session)
	}
	if count != 1 || index["load-device-0001"] == nil {
		t.Fatalf("count=%d index=%v", count, index)
	}
}

func makeIndexedDevices(start, count int) []map[string]any {
	devices := make([]map[string]any, count)
	for i := 0; i < count; i++ {
		id := start + i
		devices[i] = map[string]any{
			"id":          "account-device-" + strconv.Itoa(id),
			"disabled_at": nil,
			"metadata": map[string]any{
				"video_cloud_devid": fmt.Sprintf("load-device-%04d", id),
			},
		}
	}
	return devices
}

func TestValidateDeviceBindWaitsForProvisionedState(t *testing.T) {
	root := t.TempDir()
	envRoot := filepath.Join(root, "env")
	if err := os.MkdirAll(filepath.Join(envRoot, "services", "account-manager"), 0o755); err != nil {
		t.Fatal(err)
	}
	usersPath := filepath.Join(root, "users.json")
	if err := os.WriteFile(usersPath, []byte(`{"users":[{"email":"rtk+001@users.local","password":"pass"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	bindPath := filepath.Join(root, "bind.json")
	outDir := filepath.Join(root, "out")

	loginSeen := false
	provisioningSeen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/brand-clouds/rtk-test/auth/login":
			loginSeen = true
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": map[string]string{"access_token": "user-token"}})
		case "/v1/orgs/brand-1/devices/account-device-1/provisioning":
			provisioningSeen = true
			if r.Header.Get("authorization") != "Bearer user-token" {
				t.Fatalf("authorization header = %q", r.Header.Get("authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"operation": map[string]string{"status": "succeeded"},
				"readiness": map[string]any{
					"state":         "transport_pending",
					"product_state": "activated",
					"sources": map[string]any{
						"provisioning_operation_status": "succeeded",
						"video_cloud_activation_status": "activated",
					},
				},
				"video_metadata": map[string]string{"video_cloud_devid": "load-device-0001"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	if err := os.WriteFile(filepath.Join(envRoot, "services", "account-manager", "account-manager.env"), []byte("ACCOUNT_MANAGER_BASE_URL="+server.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	data := `{
  "brandname":"RTK",
  "brand_cloud_id":"brand-1",
  "tenant_slug":"rtk-test",
  "inputs":{"users_file":"` + usersPath + `"},
  "assignments":[
    {
      "assignment_index":0,
      "assigned_email":"rtk+001@users.local",
      "device_id":"load-device-0001",
      "device_type":"light",
      "category":"mqtt_device",
      "service_options":["mqtt"],
      "account_device_id":"account-device-1",
      "operation_id":"operation-1",
      "status":"provision_requested"
    }
  ]
}`
	if err := os.WriteFile(bindPath, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}

	err := runValidateDeviceBind([]string{
		"--workspace", root,
		"--env-root", envRoot,
		"--bind-artifact", bindPath,
		"--out-dir", outDir,
		"--expected-count", "1",
		"--expected-devices-per-user", "1",
		"--wait-provisioned-timeout", "1s",
		"--wait-provisioned-poll", "1ms",
	})
	if err != nil {
		t.Fatalf("runValidateDeviceBind() error = %v", err)
	}
	if !loginSeen || !provisioningSeen {
		t.Fatalf("loginSeen=%v provisioningSeen=%v", loginSeen, provisioningSeen)
	}
	var result struct {
		Provisioning bindProvisionWaitResult `json:"provisioning"`
	}
	body, err := os.ReadFile(filepath.Join(outDir, "bulk-device-bind-validation-results.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(body, &result); err != nil {
		t.Fatal(err)
	}
	if result.Provisioning.Ready != 1 || result.Provisioning.Pending != 0 || len(result.Provisioning.Failures) != 0 {
		t.Fatalf("unexpected provisioning result: %+v", result.Provisioning)
	}
}
