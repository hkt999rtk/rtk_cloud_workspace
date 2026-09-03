package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestBindAndUnprovisionDryRunsRenderDeterministicAssignments(t *testing.T) {
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging")
	usersPath := filepath.Join(workspace, "users.json")
	devicesDir := filepath.Join(envRoot, "devices", "test_device")
	manifestPath := filepath.Join(devicesDir, "manifests", "devices.json")
	bindPath := filepath.Join(workspace, "bind.json")
	writeCoverageFile(t, usersPath, `{"users":[{"email":"user1@example.test","password":"pw"},{"email":"user2@example.test","password":"pw"}]}`)
	writeCoverageFile(t, manifestPath, `[
		{"device_id":"device-light","device_type":"light","display_name":"Light","service_options":["mqtt"]},
		{"device_id":"device-camera","device_type":"camera","display_name":"Camera","service_options":["mqtt","video_streaming"]}
	]`)

	var bindErr error
	bindOutput := captureStdout(t, func() {
		bindErr = runBindDevices([]string{
			"--workspace", workspace,
			"--env-root", envRoot,
			"--brandname", "RTK",
			"--users-file", usersPath,
			"--devices-dir", devicesDir,
			"--count", "2",
			"--dry-run",
		})
	})
	if bindErr != nil {
		t.Fatal(bindErr)
	}
	if !strings.Contains(bindOutput, `"action":"dry_run"`) || !strings.Contains(bindOutput, `"category":"ip_camera"`) {
		t.Fatalf("bind output = %s", bindOutput)
	}

	writeCoverageFile(t, bindPath, `{
		"brandname":"RTK",
		"brand_cloud_id":"brand-001",
		"tenant_slug":"rtk",
		"inputs":{"users_file":"`+usersPath+`","devices_dir":"`+devicesDir+`"},
		"assignments":[
			{"assignment_index":0,"assigned_email":"user1@example.test","device_id":"device-light","device_type":"light","category":"mqtt_device","service_options":["mqtt"],"account_device_id":"account-light","status":"provisioned"},
			{"assignment_index":1,"assigned_email":"user2@example.test","device_id":"device-camera","device_type":"camera","category":"ip_camera","service_options":["mqtt","video_streaming"],"account_device_id":"account-camera","status":"provisioned"}
		]
	}`)
	var unprovisionErr error
	unprovisionOutput := captureStdout(t, func() {
		unprovisionErr = runUnprovisionDevices([]string{
			"--workspace", workspace,
			"--env-root", envRoot,
			"--brandname", "RTK",
			"--bind-artifact", bindPath,
			"--count", "2",
			"--dry-run",
		})
	})
	if unprovisionErr != nil {
		t.Fatal(unprovisionErr)
	}
	if !strings.Contains(unprovisionOutput, `"brand_cloud_id":"brand-001"`) || !strings.Contains(unprovisionOutput, `"account_device_id":"account-camera"`) {
		t.Fatalf("unprovision output = %s", unprovisionOutput)
	}
}

func TestUnprovisionCommandPreflightsRouteAndWritesEvidence(t *testing.T) {
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging")
	usersPath := filepath.Join(workspace, "users.json")
	bindPath := filepath.Join(workspace, "bind.json")
	writeCoverageFile(t, usersPath, `{"users":[{"email":"user@example.test","password":"pw"}]}`)
	writeCoverageFile(t, bindPath, `{
		"brandname":"RTK","brand_cloud_id":"brand-001","tenant_slug":"rtk",
		"inputs":{"users_file":"`+usersPath+`"},
		"assignments":[{"assignment_index":0,"assigned_email":"user@example.test","device_id":"device-001","device_type":"light","category":"mqtt_device","service_options":["mqtt"],"account_device_id":"account-001","status":"provisioned"}]
	}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": map[string]string{"access_token": "user-token"}})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/00000000-0000-0000-0000-000000000000/unprovision"):
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"code": "not_found"})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/account-001/unprovision"):
			_ = json.NewEncoder(w).Encode(map[string]any{"unprovision": map[string]string{
				"device_id": "account-001", "organization_id": "brand-001", "video_cloud_devid": "device-001",
				"unprovisioned_at": "2026-07-24T00:00:00Z",
			}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("ACCOUNT_MANAGER_BASE_URL", server.URL)
	writeCoverageFile(t, filepath.Join(envRoot, "services", "account-manager", "account-manager.env"), "ACCOUNT_MANAGER_BASE_URL="+server.URL+"\n")
	writeCoverageFile(t, filepath.Join(envRoot, "env", "stack.env"), "CLOUD_PROVIDER=local\n")

	var commandErr error
	output := captureStdout(t, func() {
		commandErr = runUnprovisionDevices([]string{
			"--workspace", workspace, "--env-root", envRoot, "--brandname", "RTK", "--bind-artifact", bindPath, "--count", "1",
		})
	})
	if commandErr != nil {
		t.Fatal(commandErr)
	}
	if !strings.Contains(output, `"action":"unprovisioned"`) || !strings.Contains(output, `"unprovisioned":1`) {
		t.Fatalf("output = %s", output)
	}
	matches, err := filepath.Glob(filepath.Join(envRoot, "runtime", "artifacts", "device-unprovision", "*.json"))
	if err != nil || len(matches) != 1 {
		t.Fatalf("artifacts = %v, %v", matches, err)
	}
	body, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `"video_cloud_devid": "device-001"`) {
		t.Fatalf("artifact = %s", body)
	}
}

func TestBrandCloudCommandsExerciseLoginListFilterAndCreate(t *testing.T) {
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging")
	writeCoverageFile(t, filepath.Join(envRoot, "env", "stack.env"), "CLOUD_PROVIDER=local\n")
	t.Setenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL", "admin@example.test")
	t.Setenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD", "password")

	listCalls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" && r.Header.Get("Authorization") != "Bearer platform-token" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"user": map[string]string{"id": "platform-user"}, "tokens": map[string]string{"access_token": "platform-token"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/brand-clouds":
			listCalls++
			_ = json.NewEncoder(w).Encode(map[string]any{
				"brand_clouds": []map[string]any{
					{"id": "brand-existing", "name": "Existing", "status": "active", "tier": "evaluation", "evaluation_device_quota": 10, "metadata": map[string]string{"brandname": "Existing"}},
				},
				"pagination": map[string]any{"total": 1},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/brand-clouds":
			var payload map[string]any
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatal(err)
			}
			if payload["owner_user_id"] != "platform-user" {
				t.Fatalf("owner_user_id = %v, want platform-user", payload["owner_user_id"])
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"brand_cloud": map[string]any{"id": "brand-new", "name": "New Brand"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("ACCOUNT_MANAGER_BASE_URL", server.URL)

	var listErr error
	listOutput := captureStdout(t, func() {
		listErr = runListBrandnameClouds([]string{
			"--workspace", workspace, "--env-root", envRoot, "--brandname", "Existing", "--json",
		})
	})
	if listErr != nil {
		t.Fatal(listErr)
	}
	if !strings.Contains(listOutput, `"filtered_total": 1`) || !strings.Contains(listOutput, `"brand-existing"`) {
		t.Fatalf("list output = %s", listOutput)
	}

	var createErr error
	createOutput := captureStdout(t, func() {
		createErr = runCreateBrandnameCloud([]string{
			"--workspace", workspace, "--env-root", envRoot, "--brandname", "New Brand", "--skip-bootstrap",
		})
	})
	if createErr != nil {
		t.Fatal(createErr)
	}
	if !strings.Contains(createOutput, `"action":"created"`) || !strings.Contains(createOutput, `"brand-new"`) {
		t.Fatalf("create output = %s", createOutput)
	}
	if listCalls != 2 {
		t.Fatalf("list calls = %d, want 2", listCalls)
	}
}

func TestCheckCertificatesReportsAllConfiguredTargets(t *testing.T) {
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging")
	writeCoverageFile(t, filepath.Join(envRoot, "env", "stack.env"), strings.Join([]string{
		"VIDEO_CLOUD_DOMAIN=video.example.test",
		"VIDEO_CLOUD_CERTISSUER_DOMAIN=certissuer.video.example.test",
		"ACCOUNT_MANAGER_DOMAIN=account.example.test",
		"CLOUD_ADMIN_DOMAIN=admin.example.test",
	}, "\n"))
	writeCoverageFile(t, filepath.Join(envRoot, "services", "cloud-logger", "logger.env"), "CLOUD_LOGGER_DOMAIN=logger.example.test\n")

	var checkErr error
	output := captureStdout(t, func() {
		checkErr = runCheckCertificates([]string{
			"--workspace", workspace, "--env-root", envRoot, "--json", "--skip-live",
		})
	})
	if checkErr == nil {
		t.Fatal("missing certificates unexpectedly passed")
	}
	for _, target := range []string{"video-cloud", "video-cloud-certissuer", "account-manager", "cloud-admin", "cloud-logger"} {
		if !strings.Contains(output, `"target":"`+target+`"`) {
			t.Fatalf("output missing %s: %s", target, output)
		}
	}
}

func TestUIEvidenceValidationRequiresPassingChecksummedScreenshots(t *testing.T) {
	runDir := t.TempDir()
	screenshot := []byte("stable screenshot evidence")
	if err := os.MkdirAll(filepath.Join(runDir, "evidence"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(runDir, "evidence", "UI-CA-TEST-001.png"), screenshot, 0o600); err != nil {
		t.Fatal(err)
	}
	checksum := fmt.Sprintf("%x", sha256.Sum256(screenshot))
	writeCoverageFile(t, filepath.Join(runDir, "evidence-manifest.json"), `{
		"cases":[{"test_id":"UI-CA-TEST-001","assessment":"PASS","screenshot_path":"evidence/UI-CA-TEST-001.png","screenshot_sha256":"`+checksum+`"}]
	}`)
	if err := validateUIEvidenceRun(runDir, []string{"UI-CA-TEST-001"}); err != nil {
		t.Fatal(err)
	}
	if err := validateUIEvidenceRun(runDir, []string{"UI-CA-MISSING-002"}); err == nil || !strings.Contains(err.Error(), "has no result") {
		t.Fatalf("missing evidence error = %v", err)
	}
	writeCoverageFile(t, filepath.Join(runDir, "evidence-manifest.json"), `{
		"cases":[{"test_id":"UI-CA-TEST-001","assessment":"FAIL","screenshot_path":"evidence/UI-CA-TEST-001.png","screenshot_sha256":"bad"}]
	}`)
	if err := validateUIEvidenceRun(runDir, []string{"UI-CA-TEST-001"}); err == nil || !strings.Contains(err.Error(), "assessment is FAIL") {
		t.Fatalf("failed assessment error = %v", err)
	}
}

func TestStagingMultiBrandPlanAndArtifactResumeHelpers(t *testing.T) {
	root := t.TempDir()
	planPath := filepath.Join(root, "brand-plan.json")
	writeCoverageFile(t, planPath, `{
		"total_devices":3,
		"devices_per_user":1,
		"brands":[
			{"brandname":"Brand B","devices":1,"normal_users":1,"developer_users":{"owner":1},"device_mix":{"smart_meter":1}},
			{"brandname":"Brand A","devices":2,"normal_users":2,"developer_users":{"admin":1},"device_mix":{"light":2}}
		]
	}`)
	var planErr error
	output := captureStdout(t, func() {
		planErr = runStagingE2EMultiBrandDataSetup(stagingE2EMultiBrandConfig{
			Workspace: root, EnvRoot: root, BrandPlanFile: planPath, DeviceMix: "light=3", PlanMode: true,
		})
	})
	if planErr != nil {
		t.Fatal(planErr)
	}
	if !strings.Contains(output, "total_devices: 3") || !strings.Contains(output, "device_mix=light=2") {
		t.Fatalf("plan output = %s", output)
	}
	if got := deviceMixString(map[string]int{"smart_meter": 1, "light": 2}); got != "light=2,smart_meter=1" {
		t.Fatalf("deviceMixString() = %q", got)
	}
	if e2eStepIndex("bind_devices") != 4 || e2eStepIndex("prepare_factory_production") != 2 || e2eStepIndex("missing") != -1 || shouldRunE2EStep("create_users", "bind_devices") {
		t.Fatal("unexpected E2E step ordering")
	}

	usersPath := filepath.Join(root, "users.json")
	devicesDir := filepath.Join(root, "devices")
	bindPath := filepath.Join(root, "bind.json")
	writeCoverageFile(t, usersPath, `{"users":[{"email":"a@example.test"},{"email":"b@example.test"}]}`)
	writeCoverageFile(t, filepath.Join(devicesDir, "manifests", "devices.json"), `[
		{"device_id":"d1","device_type":"light","service_options":["mqtt"]},
		{"device_id":"d2","device_type":"smart_meter","service_options":["mqtt"]}
	]`)
	writeCoverageFile(t, bindPath, `{"assignments":[
		{"assigned_email":"a@example.test","device_id":"d1","device_type":"light","service_options":["mqtt"],"operation_id":"op1","status":"provisioned"},
		{"assigned_email":"b@example.test","device_id":"d2","device_type":"smart_meter","service_options":["mqtt"],"operation_id":"op2","status":"provisioned"}
	]}`)
	if usersArtifactCount(usersPath) != 2 || deviceManifestCount(devicesDir) != 2 || bindArtifactCount(bindPath) != 2 {
		t.Fatal("artifact counts did not match fixtures")
	}
	if !deviceManifestMatchesSetup(devicesDir, 2, "light=1,smart_meter=1") {
		t.Fatal("device manifest did not match setup")
	}
	if !bindArtifactMatchesSetup(bindPath, usersPath, devicesDir, 2, 2, "light=1,smart_meter=1") {
		t.Fatal("bind artifact did not match setup")
	}
	if bindArtifactAssignedUserCount(bindPath) != 2 {
		t.Fatal("assigned user count did not match")
	}
}

func TestCommandValidationCoversOperationalFailureBoundaries(t *testing.T) {
	if err := runTestUI([]string{"--run-id", "bad run id"}); err == nil {
		t.Fatal("invalid UI run id unexpectedly passed")
	}
	if err := runCIRunnersProvision(nil); err == nil || !strings.Contains(err.Error(), "LINODE_TOKEN") {
		t.Fatalf("CI provision error = %v", err)
	}
	if err := runCIRunnersArchiveArtifacts(nil); err == nil || !strings.Contains(err.Error(), "--repo and --run-id") {
		t.Fatalf("CI archive error = %v", err)
	}
	if err := runCIRunnersRunSession([]string{"--shutdown-policy", "sometimes"}); err == nil {
		t.Fatal("invalid runner shutdown policy unexpectedly passed")
	}
	if err := runMigrateEnv([]string{"--env-root", "staging"}); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("migrate error = %v", err)
	}
	assertPerDeviceFactoryCredentialMapsValidateBeforeGeneration(t)
}

func assertPerDeviceFactoryCredentialMapsValidateBeforeGeneration(t *testing.T) {
	keys := []string{
		"FACTORY_ENROLL_PRODUCTION_JWT_BY_DEVICE_TYPE",
		"FACTORY_ENROLL_BATCH_ID_BY_DEVICE_TYPE",
		"FACTORY_ENROLL_DEVICE_ITEM_PROFILE_ID_BY_DEVICE_TYPE",
	}
	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			for _, candidate := range keys {
				t.Setenv(candidate, "")
			}
			t.Setenv(key, "not-json")
			if err := runGenerateLoadDevices(nil); err == nil || !strings.Contains(err.Error(), key+" must be a JSON object") {
				t.Fatalf("error = %v", err)
			}
		})
	}

	t.Setenv("FACTORY_TEST_MAP", `{" camera ":" product-1 "}`)
	values, err := envJSONTextMap("FACTORY_TEST_MAP")
	if err != nil || values[" camera "] != "product-1" {
		t.Fatalf("values=%v err=%v", values, err)
	}
	for _, raw := range []string{`{"":"value"}`, `{"camera":""}`} {
		t.Setenv("FACTORY_TEST_MAP", raw)
		if _, err := envJSONTextMap("FACTORY_TEST_MAP"); err == nil || !strings.Contains(err.Error(), "empty device type or value") {
			t.Fatalf("raw=%s error=%v", raw, err)
		}
	}
}

func TestRefreshUserTokensLogsInUsersAndPersistsSessions(t *testing.T) {
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging")
	writeCoverageFile(t, filepath.Join(envRoot, "env", "stack.env"), "CLOUD_PROVIDER=local\n")
	usersPath := filepath.Join(workspace, "users.json")
	writeCoverageFile(t, usersPath, `{
		"brandname":"RTK","tenant_slug":"rtk",
		"users":[
			{"email":"one@example.test","password":"pw1"},
			{"email":"two@example.test","password":"pw2"}
		]
	}`)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/auth/login" {
			http.NotFound(w, r)
			return
		}
		var request map[string]string
		_ = json.NewDecoder(r.Body).Decode(&request)
		_ = json.NewEncoder(w).Encode(map[string]any{"tokens": map[string]string{
			"access_token": "access-" + request["email"], "refresh_token": "refresh-" + request["email"],
		}})
	}))
	defer server.Close()
	t.Setenv("ACCOUNT_MANAGER_BASE_URL", server.URL)

	var refreshErr error
	output := captureStdout(t, func() {
		refreshErr = runRefreshUserTokens([]string{
			"--workspace", workspace, "--env-root", envRoot, "--users-file", usersPath, "--brandname", "RTK", "--concurrency", "2",
		})
	})
	if refreshErr != nil {
		t.Fatal(refreshErr)
	}
	if !strings.Contains(output, `"logged_in":2`) || !strings.Contains(output, `"updated":2`) {
		t.Fatalf("output = %s", output)
	}
	updated, err := os.ReadFile(usersPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updated), "access-one@example.test") || !strings.Contains(string(updated), "refresh-two@example.test") {
		t.Fatalf("updated users = %s", updated)
	}
}

func TestClaimResolveFallbackCreatesIndependentDeviceResults(t *testing.T) {
	var created []string
	var createdMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/device-claim-tokens":
			var request map[string]any
			_ = json.NewDecoder(r.Body).Decode(&request)
			if request["organization_id"] != "brand-001" {
				t.Fatalf("organization_id = %v, want brand-001", request["organization_id"])
			}
			if request["device_item_profile_id"] != "product-001" {
				t.Fatalf("device_item_profile_id = %v, want product-001", request["device_item_profile_id"])
			}
			deviceID := request["video_cloud_devid"].(string)
			createdMu.Lock()
			created = append(created, deviceID)
			createdMu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"claim_token": "raw-" + deviceID})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/orgs/brand-001/devices/claim/resolve":
			var request map[string]string
			_ = json.NewDecoder(r.Body).Decode(&request)
			deviceID := strings.TrimPrefix(request["claim_token"], "loadtest-run-001-")
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"claim_id": "claim-" + deviceID,
				"device":   map[string]string{"id": "account-" + deviceID},
				"provision_input": map[string]any{
					"video_cloud_devid": deviceID,
				},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	assignments := []bindAssignment{
		{AssignedEmail: "one@example.test", DeviceID: "device-1", DeviceType: "light", Category: "mqtt_device", ServiceOptions: []string{"mqtt"}, ProductID: "product-001"},
		{AssignedEmail: "two@example.test", DeviceID: "device-2", DeviceType: "camera", Category: "ip_camera", ServiceOptions: []string{"mqtt", "video_streaming"}, ProductID: "product-001"},
	}
	userSessions := map[string]*brandCloudUserSession{
		"one@example.test": {Email: "one@example.test", Session: accountPlatformSession{AccessToken: "user-one"}},
		"two@example.test": {Email: "two@example.test", Session: accountPlatformSession{AccessToken: "user-two"}},
	}
	session := &accountPlatformSession{AccessToken: "platform-token"}
	results, summary, err := accountBindDevicesViaClaimResolve(
		accountManagerContext{BaseURL: server.URL}, session, &sync.Mutex{}, func(string, ...any) {},
		"brand-001", "rtk", assignments, userSessions, "run-001", 32,
	)
	if err != nil {
		t.Fatal(err)
	}
	createdMu.Lock()
	createdCount := len(created)
	createdMu.Unlock()
	if summary.Created != 2 || summary.Failed != 0 || len(results) != 2 || createdCount != 2 {
		t.Fatalf("summary=%#v results=%#v created=%v", summary, results, created)
	}
	if results["device-1"].AccountDeviceID != "account-device-1" || results["device-2"].Status != "created" {
		t.Fatalf("results = %#v", results)
	}
}

func TestRunBindDevicesQualifiesEveryAssignmentThroughClaimResolve(t *testing.T) {
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging")
	usersPath := filepath.Join(workspace, "users.json")
	devicesDir := filepath.Join(envRoot, "devices", "test_device")
	writeCoverageFile(t, filepath.Join(envRoot, "env", "stack.env"), "CLOUD_PROVIDER=local\n")
	writeCoverageFile(t, usersPath, `{"brandname":"RTK","users":[{"email":"one@example.test","password":"pw-one"},{"email":"two@example.test","password":"pw-two"}]}`)
	writeCoverageFile(t, filepath.Join(devicesDir, "manifests", "devices.json"), `[
		{"device_id":"device-claim","device_type":"camera","service_options":["mqtt","video_streaming"]},
		{"device_id":"device-bulk","device_type":"camera","service_options":["mqtt","video_streaming"]}
	]`)

	var claimCreates, claimResolves, registryCreates, provisions int
	var requestsMu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/v1/auth/login":
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": map[string]string{"access_token": "platform-token", "refresh_token": "platform-refresh"}})
		case r.Method == http.MethodGet && r.URL.Path == "/v1/admin/brand-clouds":
			_ = json.NewEncoder(w).Encode(map[string]any{"brand_clouds": []map[string]any{{"id": "brand-001", "name": "RTK", "tenant_slug": "rtk"}}})
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/v1/auth/login"):
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": map[string]string{"access_token": "user-token", "refresh_token": "user-refresh"}})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/device-claim-tokens":
			requestsMu.Lock()
			claimCreates++
			requestsMu.Unlock()
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"claim_token": "created"})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/orgs/brand-001/devices/claim/resolve":
			requestsMu.Lock()
			claimResolves++
			requestsMu.Unlock()
			var request map[string]string
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Fatal(err)
			}
			deviceID := "device-claim"
			if strings.HasSuffix(request["claim_token"], "device-bulk") {
				deviceID = "device-bulk"
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"claim_id": "claim-" + deviceID, "device": map[string]string{"id": "account-" + deviceID},
				"provision_input": map[string]any{"video_cloud_devid": deviceID, "service_options": []string{"mqtt", "video_streaming"}},
			})
		case r.Method == http.MethodPost && r.URL.Path == "/v1/admin/brand-clouds/brand-001/device-bind-jobs":
			registryCreates++
			t.Fatal("nonexistent admin bulk registry route was called")
		case r.Method == http.MethodPost && r.URL.Path == "/v1/orgs/brand-001/devices":
			t.Fatal("synthetic member was used for registry creation")
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/provision"):
			requestsMu.Lock()
			provisions++
			requestsMu.Unlock()
			w.WriteHeader(http.StatusAccepted)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("ACCOUNT_MANAGER_BASE_URL", server.URL)
	t.Setenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL", "admin@example.test")
	t.Setenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD", "admin-password")
	t.Setenv("CLOUD_BIND_DEVICES_CLAIM_EVIDENCE_COUNT", "2")

	if err := runBindDevices([]string{
		"--workspace", workspace, "--env-root", envRoot, "--brandname", "RTK",
		"--users-file", usersPath, "--devices-dir", devicesDir, "--count", "2", "--concurrency", "2",
		"--skip-bootstrap", "--skip-direct-provision-bridge",
	}); err != nil {
		t.Fatal(err)
	}
	requestsMu.Lock()
	counts := []int{claimCreates, claimResolves, registryCreates, provisions}
	requestsMu.Unlock()
	if counts[0] != 2 || counts[1] != 2 || counts[2] != 0 || counts[3] != 2 {
		t.Fatalf("claim-create/resolve registry-create provision counts=%v", counts)
	}
	artifact, err := readBindArtifactFromTestData(filepath.Join(envRoot, "runtime"), "RTK")
	if err != nil {
		t.Fatal(err)
	}
	if len(artifact.Assignments) != 2 || artifact.Assignments[0].ClaimID == "" || artifact.Assignments[1].ClaimID == "" {
		t.Fatalf("assignments=%+v", artifact.Assignments)
	}
}

func TestStagingMQTTLogVerifyCorrelatesPersistedRuntimeEvidence(t *testing.T) {
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging")
	writeCoverageFile(t, filepath.Join(envRoot, "env", "stack.env"), "CLOUD_STACK_NAME=video-cloud-staging\n")
	kubeconfig := filepath.Join(workspace, "kubeconfig.yaml")
	writeCoverageFile(t, kubeconfig, "apiVersion: v1\n")
	t.Setenv("KUBECONFIG", kubeconfig)
	resultsPath := filepath.Join(workspace, "mqtt-results.json")
	writeCoverageFile(t, resultsPath, `{
		"overall":"pass",
		"devices":[{
			"device_id":"device-001","mqtt_status":"PASS","runtime_log_stream_id":"stream-001",
			"runtime_log_expectations":[{"seq":1,"source":"device-runtime","message":"connected"}]
		}]
	}`)
	logger := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("device_id") != "device-001" {
			t.Fatalf("query = %s", r.URL.RawQuery)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"events": []map[string]any{{
			"msg": "connected", "fields": map[string]any{"stream_id": "stream-001", "source": "device-runtime", "seq": 1},
		}}})
	}))
	defer logger.Close()
	t.Setenv("VIDEO_CLOUD_LOGGER_ENDPOINT", logger.URL)
	t.Setenv("VIDEO_CLOUD_LOGGER_TOKEN", "logger-token")
	outDir := filepath.Join(workspace, "log-evidence")

	var verifyErr error
	output := captureStdout(t, func() {
		verifyErr = runStagingE2EMQTTLogVerify([]string{
			"--workspace", workspace, "--env-root", envRoot, "--mqtt-results", resultsPath, "--out-dir", outDir,
		})
	})
	if verifyErr != nil {
		t.Fatal(verifyErr)
	}
	if !strings.Contains(output, `"overall":"pass"`) {
		t.Fatalf("output = %s", output)
	}
	summary, err := os.ReadFile(filepath.Join(outDir, "summary.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(summary), `"checked_logs": 1`) || !strings.Contains(string(summary), `"missing_logs": []`) {
		t.Fatalf("summary = %s", summary)
	}
}

func TestUICommandRunsDesktopSmokeAndValidatesGeneratedEvidence(t *testing.T) {
	binDir := t.TempDir()
	writeExecutable(t, filepath.Join(binDir, "npm"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(binDir, "npx"), `#!/bin/sh
/usr/bin/python3 - <<'PY'
import datetime, hashlib, json, os
root = os.environ["E2E_TEST_RUN_DIR"]
evidence = os.path.join(root, "evidence")
os.makedirs(evidence, exist_ok=True)
cases = []
for test_id in filter(None, os.environ.get("E2E_EXPECTED_TEST_IDS", "").split(",")):
    name = test_id + ".png"
    data = ("evidence:" + test_id).encode()
    path = os.path.join(evidence, name)
    with open(path, "wb") as handle:
        handle.write(data)
    cases.append({
        "test_id": test_id,
        "assessment": "PASS",
        "screenshot_path": "evidence/" + name,
        "screenshot_sha256": hashlib.sha256(data).hexdigest(),
    })
with open(os.path.join(root, "evidence-manifest.json"), "w") as handle:
    now = datetime.datetime.now(datetime.timezone.utc).isoformat().replace("+00:00", "Z")
    for case in cases:
        case["workspace_commit"] = os.environ["E2E_WORKSPACE_COMMIT"]
        case["submodule_commit"] = os.environ["E2E_SUBMODULE_COMMIT"]
        case["generated_at"] = now
    json.dump({
        "schema_version": 1,
        "run_id": os.environ["E2E_TEST_RUN_ID"],
        "target": os.environ["E2E_TEST_TARGET"],
        "environment": os.environ["E2E_TEST_ENVIRONMENT"],
        "workspace_commit": os.environ["E2E_WORKSPACE_COMMIT"],
        "submodule_commit": os.environ["E2E_SUBMODULE_COMMIT"],
        "generated_at": now,
        "cases": cases,
    }, handle)
with open(os.path.join(root, "junit.xml"), "w") as handle:
    handle.write('<testsuite name="ui" tests="%d" failures="0"></testsuite>\n' % len(cases))
PY
`)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	playwrightPath := filepath.Join(workspace, "repos", "rtk_cloud_admin", "web", "node_modules", ".bin", "playwright")
	if !exists(playwrightPath) {
		if err := os.MkdirAll(filepath.Dir(playwrightPath), 0o755); err != nil {
			t.Fatal(err)
		}
		writeExecutable(t, playwrightPath, "#!/bin/sh\nexit 0\n")
		t.Cleanup(func() { _ = os.Remove(playwrightPath) })
	}
	runID := "coverage-ui-desktop"
	if err := runTestUI([]string{"--desktop", "--run-id", runID}); err != nil {
		t.Fatal(err)
	}
	runDir := filepath.Join(workspace, ".artifacts", "test-runs", runID)
	t.Cleanup(func() { _ = os.RemoveAll(runDir) })
	if _, err := os.Stat(filepath.Join(runDir, "ui", "desktop", "evidence-manifest.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(runDir, "ui", "desktop", "feature-evidence.json")); err != nil {
		t.Fatal(err)
	}
}

func TestTestCommandsDispatchAllSelectedSuitesThroughConfiguredTools(t *testing.T) {
	binDir := t.TempDir()
	for _, name := range []string{"go", "python3"} {
		writeExecutable(t, filepath.Join(binDir, name), "#!/bin/sh\nexit 0\n")
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if err := runTestE2E(nil); err != nil {
		t.Fatal(err)
	}
	if err := runTestServices([]string{"--repo", "rtk_account_manager"}); err != nil {
		t.Fatal(err)
	}
}

func TestStatusAllInspectsWorkspaceAndEveryInitializedSubmodule(t *testing.T) {
	output := captureStdout(t, func() {
		if err := runStatusAll(nil); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(output, "workspace:") || !strings.Contains(output, "[rtk_video_cloud]") {
		t.Fatalf("status output missing repositories:\n%s", output)
	}
}

func TestPrintE2EPlanRendersEverySelectedQualificationPhase(t *testing.T) {
	output := captureStdout(t, func() {
		printE2EPlan(
			"/workspace", "/env", "stack", "all", "RTK", 2, 4, "light=4", 2, 2, 2,
			false, false, "all", map[string]string{
				"remove-k8s": "remove", "provision-k8s": "provision", "setup-data": "data",
				"mqtt-test": "mqtt", "mqtt-log-verify": "logs", "billing-verify": "billing",
			},
		)
	})
	for _, want := range []string{"reset environment K8s", "provision environment K8s", "setup brand/users/devices", "live home MQTT", "persisted MQTT runtime logs", "billing usage log/ledger"} {
		if !strings.Contains(output, want) {
			t.Fatalf("plan missing %q:\n%s", want, output)
		}
	}
}

func TestCIRunnerProvisionCreatesUniqueHostsAndAttachesFirewalls(t *testing.T) {
	binDir := t.TempDir()
	logPath := filepath.Join(binDir, "curl.log")
	writeExecutable(t, filepath.Join(binDir, "curl"), `#!/bin/sh
printf '%s\n' "$*" >> "$COVERAGE_CURL_LOG"
case "$*" in
  *"/linode/instances"*) printf '%s\n' '{"id":101,"ipv4":["192.0.2.10"]}' ;;
  *"/networking/firewalls/"*"/devices"*) printf '%s\n' '{}' ;;
  *"/networking/firewalls"*) printf '%s\n' '{"id":202}' ;;
  *) printf '%s\n' '{}' ;;
esac
`)
	keyPath := filepath.Join(t.TempDir(), "runner.pub")
	writeCoverageFile(t, keyPath, "ssh-ed25519 AAAATEST runner@example.test\n")
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("COVERAGE_CURL_LOG", logPath)
	t.Setenv("LINODE_TOKEN", "linode-test-token")
	t.Setenv("CI_RUNNER_PUBLIC_KEY_PATH", keyPath)
	t.Setenv("CI_RUNNER_ALLOWED_SSH_CIDRS", "198.51.100.0/24")
	output := captureStdout(t, func() {
		if err := provisionCIRunnerHosts(); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(output, "created\t192.0.2.10") {
		t.Fatalf("output = %s", output)
	}
	calls, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(calls), "/linode/instances") || !strings.Contains(string(calls), "/networking/firewalls/202/devices") {
		t.Fatalf("curl calls = %s", calls)
	}
	if strings.Contains(string(calls), "root_pass") && strings.Contains(output, "root_pass") {
		t.Fatal("generated root password leaked to command output")
	}
}

func TestGovernanceAndProvisioningHelpersClassifyRealBoundaries(t *testing.T) {
	root := t.TempDir()
	writeCoverageFile(t, filepath.Join(root, "env", "stack.env"), "CLOUD_STACK_NAME=qualification-stack\n")
	writeCoverageFile(t, filepath.Join(root, "docs", "one.md"), "needle\n")
	check := newCheck()
	check.requireFile(root, "docs/one.md")
	check.requireDir(root, "docs")
	check.requireFile(root, "docs/missing.md")
	check.requireDir(root, "missing")
	if check.failures != 2 {
		t.Fatalf("check failures = %d", check.failures)
	}
	if !anyFileContains(root, []string{"docs/one.md"}, "needle") || anyFileContains(root, []string{"docs/one.md"}, "absent") {
		t.Fatal("anyFileContains returned an unexpected result")
	}
	if stackNameFromEnvRoot(root) != "qualification-stack" || !strings.HasSuffix(videoCloudStatePath(root), "qualification-stack.state.json") {
		t.Fatal("stack state path was not derived from stack.env")
	}
	if sqlLiteral("brand'o") != "'brand''o'" {
		t.Fatal("SQL literal was not escaped")
	}
	if categorizeBindValidationFailure("connection reset by peer") != "provisioning_transport" ||
		categorizeBindValidationFailure("bind_status=already_bound timeout") != "already_bound_not_ready" ||
		categorizeBindValidationFailure("token expired") != "token" ||
		categorizeBindValidationFailure("HTTP 500") != "provisioning_http" {
		t.Fatal("bind failure classification changed")
	}
	if hasNonRetryableBindProvisionFailures([]string{"unexpected EOF"}) || !hasNonRetryableBindProvisionFailures([]string{"activation failed"}) {
		t.Fatal("retryable provisioning failures were misclassified")
	}
	failures := bindTimeoutFailures(map[string]bindProvisioningStateSnapshot{
		"ready":   {DeviceID: "ready", ReadinessState: "ready"},
		"pending": {DeviceID: "pending", BindStatus: "provision_requested"},
	})
	if len(failures) != 1 || !strings.Contains(failures[0], "device pending") {
		t.Fatalf("timeout failures = %v", failures)
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}

func writeCoverageFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}
