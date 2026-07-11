package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
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

func TestAccountBulkBindDevicesUsesAdminEndpointAndDoesNotListDevices(t *testing.T) {
	seenBulk := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/admin/brand-clouds/brand-1/device-bind-jobs":
			seenBulk = true
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", r.Method)
			}
			if r.Header.Get("authorization") != "Bearer platform-token" {
				t.Fatalf("authorization = %q", r.Header.Get("authorization"))
			}
			var req map[string]any
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatal(err)
			}
			items := anySlice(req["items"])
			if len(items) != 2 {
				t.Fatalf("items len = %d, want 2", len(items))
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"job": map[string]any{"status": "completed", "requested": 2, "created": 1, "existing": 1, "failed": 0},
				"results": []map[string]any{
					{"video_cloud_devid": "load-device-0001", "status": "existing", "account_device_id": "account-device-1", "provision_input": map[string]any{"video_cloud_devid": "load-device-0001", "service_options": []string{"mqtt"}}},
					{"video_cloud_devid": "load-device-0002", "status": "created", "account_device_id": "account-device-2", "provision_input": map[string]any{"video_cloud_devid": "load-device-0002", "service_options": []string{"mqtt"}}},
				},
			})
		case "/v1/orgs/brand-1/devices":
			t.Fatalf("bulk bind client must not list devices: %s", r.URL.String())
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	results, summary, err := accountBulkBindDevices(accountManagerContext{BaseURL: server.URL}, "platform-token", "brand-1", []bindAssignment{
		{DeviceID: "load-device-0001", DeviceType: "light", Category: "mqtt_device", ServiceOptions: []string{"mqtt"}},
		{DeviceID: "load-device-0002", DeviceType: "light", Category: "mqtt_device", ServiceOptions: []string{"mqtt"}},
	})
	if err != nil {
		t.Fatalf("accountBulkBindDevices() error = %v", err)
	}
	if !seenBulk {
		t.Fatal("bulk endpoint was not called")
	}
	if summary.Requested != 2 || summary.Created != 1 || summary.Existing != 1 || summary.Failed != 0 {
		t.Fatalf("unexpected summary: %+v", summary)
	}
	if results["load-device-0001"].Status != "existing" || results["load-device-0002"].AccountDeviceID != "account-device-2" {
		t.Fatalf("unexpected results: %+v", results)
	}
}

func TestAccountClaimResolveAlreadyClaimed(t *testing.T) {
	if !accountClaimResolveAlreadyClaimed([]byte(`{"code":"already_claimed","message":"Claim token has already been claimed"}`)) {
		t.Fatal("already_claimed response was not recognized")
	}
	if !accountClaimResolveAlreadyClaimed([]byte(`{"error":"already_claimed","message":"Claim token has already been claimed"}`)) {
		t.Fatal("string error already_claimed response was not recognized")
	}
	if !accountClaimResolveAlreadyClaimed([]byte(`{"error":{"code":"already_claimed","message":"Claim token has already been claimed"}}`)) {
		t.Fatal("nested already_claimed response was not recognized")
	}
	if accountClaimResolveAlreadyClaimed([]byte(`{"code":"invalid_claim_token"}`)) {
		t.Fatal("non already_claimed response was recognized")
	}
	if accountClaimResolveAlreadyClaimed([]byte(`not-json`)) {
		t.Fatal("invalid JSON was recognized")
	}
}

func TestAccountFindExistingClaimedDeviceByVideoCloudDevid(t *testing.T) {
	seenOffset := []string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/orgs/brand-1/devices" {
			http.NotFound(w, r)
			return
		}
		if r.Header.Get("authorization") != "Bearer user-token" {
			t.Fatalf("authorization = %q", r.Header.Get("authorization"))
		}
		seenOffset = append(seenOffset, r.URL.Query().Get("offset"))
		switch r.URL.Query().Get("offset") {
		case "0":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"devices": []map[string]any{
					{"id": "account-device-other", "metadata": map[string]any{"video_cloud_devid": "load-device-other"}},
				},
				"pagination": map[string]any{"total": 201},
			})
		case "200":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"devices": []map[string]any{
					{"id": "account-device-1", "metadata": map[string]any{"video_cloud_devid": "load-device-0001"}},
				},
				"pagination": map[string]any{"total": 201},
			})
		default:
			t.Fatalf("unexpected offset %q", r.URL.Query().Get("offset"))
		}
	}))
	defer server.Close()

	result, err := accountFindExistingClaimedDevice(accountManagerContext{BaseURL: server.URL}, "brand-1", "user-token", bindAssignment{
		DeviceID:       "load-device-0001",
		Category:       "mqtt_device",
		ServiceOptions: []string{"mqtt"},
	})
	if err != nil {
		t.Fatalf("accountFindExistingClaimedDevice() error = %v", err)
	}
	if result.Status != "existing" || result.AccountDeviceID != "account-device-1" || result.ProvisionInput["video_cloud_devid"] != "load-device-0001" {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(seenOffset) != 2 || seenOffset[0] != "0" || seenOffset[1] != "200" {
		t.Fatalf("unexpected offsets: %v", seenOffset)
	}
}

func TestBindDeviceBulkChunkSizeIsClamped(t *testing.T) {
	t.Setenv("CLOUD_BIND_DEVICES_BULK_CHUNK_SIZE", "9000")
	if got := bindDevicesBulkChunkSize(); got != 5000 {
		t.Fatalf("chunk size = %d, want 5000", got)
	}
	t.Setenv("CLOUD_BIND_DEVICES_BULK_CHUNK_SIZE", "0")
	if got := bindDevicesBulkChunkSize(); got != 1000 {
		t.Fatalf("chunk size = %d, want default 1000", got)
	}
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

func TestValidateDeviceBindWaitsForProvisionedStateFromSQLiteUsers(t *testing.T) {
	root := t.TempDir()
	envRoot := filepath.Join(root, "env")
	if err := os.MkdirAll(filepath.Join(envRoot, "services", "account-manager"), 0o755); err != nil {
		t.Fatal(err)
	}
	store, err := openTestDataStore(envRoot, "RTK")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceUsers("RTK", "brand-1", "rtk-test", "member", []map[string]any{
		{"email": "rtk+001@users.local", "password": "pass"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.ReplaceBindings("RTK", "brand-1", "rtk-test", "run-1", []bindAssignment{{
		AssignmentIndex: 0,
		AssignedEmail:   "rtk+001@users.local",
		DeviceID:        "load-device-0001",
		DeviceType:      "light",
		Category:        "mqtt_device",
		ServiceOptions:  []string{"mqtt"},
		AccountDeviceID: "account-device-1",
		OperationID:     "operation-1",
		Status:          "provision_requested",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	outDir := filepath.Join(root, "out")

	loginSeen := false
	provisioningSeen := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/brand-clouds/rtk-test/auth/login":
			loginSeen = true
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": map[string]string{"access_token": "user-token", "refresh_token": "refresh-token"}})
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
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	if err := os.WriteFile(filepath.Join(envRoot, "services", "account-manager", "account-manager.env"), []byte("ACCOUNT_MANAGER_BASE_URL="+server.URL+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = runValidateDeviceBind([]string{
		"--workspace", root,
		"--env-root", envRoot,
		"--brandname", "RTK",
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

func TestValidateDeviceBindRetriesProvisioningTransportErrors(t *testing.T) {
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

	provisioningCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/brand-clouds/rtk-test/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": map[string]string{"access_token": "user-token"}})
		case "/v1/orgs/brand-1/devices/account-device-1/provisioning":
			provisioningCalls++
			if provisioningCalls == 1 {
				hijacker, ok := w.(http.Hijacker)
				if !ok {
					t.Fatal("response writer cannot hijack")
				}
				conn, _, err := hijacker.Hijack()
				if err != nil {
					t.Fatalf("hijack: %v", err)
				}
				_ = conn.Close()
				return
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
		"--wait-provisioned-timeout", "2s",
		"--wait-provisioned-poll", "1ms",
		"--wait-provisioned-concurrency", "1",
	})
	if err != nil {
		t.Fatalf("runValidateDeviceBind() error = %v", err)
	}
	if provisioningCalls < 2 {
		t.Fatalf("expected retry after transport error, calls=%d", provisioningCalls)
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
	if result.Provisioning.Ready != 1 || result.Provisioning.Failed != 0 {
		t.Fatalf("unexpected provisioning result: %+v", result.Provisioning)
	}
}
