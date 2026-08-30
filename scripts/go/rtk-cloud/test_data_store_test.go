package main

import (
	"database/sql"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestTestDataStoreWritesUsersDevicesAndBindings(t *testing.T) {
	envRoot := t.TempDir()
	store, err := openTestDataStore(envRoot, "RTK")
	if err != nil {
		t.Fatalf("openTestDataStore() error = %v", err)
	}
	defer store.Close()

	users := []map[string]any{
		{"id": "global-user-1", "user_id": "global-user-1", "email": "rtk+001@users.local", "password": "pw1", "tokens": map[string]any{"access_token": "old"}},
		{"id": "global-user-2", "user_id": "global-user-2", "email": "rtk+002@users.local", "password": "pw2"},
	}
	if err := store.ReplaceUsers("RTK", "org-rtk", "rtk", "member", users); err != nil {
		t.Fatalf("ReplaceUsers() error = %v", err)
	}
	if got := testDataUsersCount(store.DB, "RTK"); got != 2 {
		t.Fatalf("users count = %d, want 2", got)
	}
	byEmail, list, err := store.ReadUsersList("RTK")
	if err != nil {
		t.Fatalf("ReadUsersList() error = %v", err)
	}
	if byEmail["rtk+001@users.local"].Password != "pw1" || len(list) != 2 {
		t.Fatalf("users = %+v list=%+v", byEmail, list)
	}
	if byEmail["rtk+001@users.local"].UserID != "global-user-1" {
		t.Fatalf("global user id = %q, want global-user-1", byEmail["rtk+001@users.local"].UserID)
	}

	devices := []generatedDevice{{
		DeviceID:             "load-device-0001",
		DeviceType:           "camera",
		MQTTCapability:       "camera",
		ServiceOptions:       []string{"mqtt", "video_streaming"},
		Model:                "RTC-CAM-PRO2-SIM",
		DisplayName:          "Camera 1",
		FirmwareVersion:      "0.0.0-loadtest",
		Capabilities:         []string{"camera_event"},
		CertificateProfile:   "factory-enrolled-device-mtls-client",
		KeyAlgorithm:         "ed25519",
		CertificatePath:      "sqlite://device_credentials/load-device-0001/cert_pem",
		CertificateChainPath: "sqlite://device_credentials/load-device-0001/chain_pem",
		KeyPath:              "sqlite://device_credentials/load-device-0001/key_pem",
		BundlePath:           "sqlite://device_credentials/load-device-0001/bundle_pem",
	}}
	materials := map[string]testDataDeviceCredential{
		"load-device-0001": {
			DeviceID:     "load-device-0001",
			CertPEM:      "CERT",
			KeyPEM:       "KEY",
			ChainPEM:     "CHAIN",
			BundlePEM:    "CERTKEY",
			MetadataJSON: mustJSON(t, devices[0]),
		},
	}
	if err := store.ReplaceDevices("RTK", "run-1", devices, materials); err != nil {
		t.Fatalf("ReplaceDevices() error = %v", err)
	}
	loadedDevices, err := store.ReadDeviceManifest("RTK")
	if err != nil {
		t.Fatalf("ReadDeviceManifest() error = %v", err)
	}
	if len(loadedDevices) != 1 || loadedDevices[0].DeviceID != "load-device-0001" || !reflect.DeepEqual(loadedDevices[0].ServiceOptions, []string{"mqtt", "video_streaming"}) {
		t.Fatalf("loaded devices = %+v", loadedDevices)
	}

	assignments := []bindAssignment{{
		AssignmentIndex: 0,
		AssignedEmail:   "rtk+001@users.local",
		DeviceID:        "load-device-0001",
		DeviceType:      "camera",
		Category:        "ip_camera",
		ServiceOptions:  []string{"mqtt", "video_streaming"},
		AccountDeviceID: "account-device-1",
		OperationID:     "op-1",
		Status:          "provision_requested",
	}}
	if err := store.ReplaceBindings("RTK", "org-rtk", "rtk", "run-1", assignments); err != nil {
		t.Fatalf("ReplaceBindings() error = %v", err)
	}
	loadedAssignments, err := store.ReadBindAssignments("RTK")
	if err != nil {
		t.Fatalf("ReadBindAssignments() error = %v", err)
	}
	if !reflect.DeepEqual(loadedAssignments, assignments) {
		t.Fatalf("assignments = %+v, want %+v", loadedAssignments, assignments)
	}

	info, err := os.Stat(testDataDBPath(envRoot, "RTK"))
	if err != nil {
		t.Fatalf("stat db: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("db mode = %o, want 0600", info.Mode().Perm())
	}
}

func TestReadUsersListFallsBackToPrivilegedStagingUsersWithoutMembers(t *testing.T) {
	store, err := openTestDataStore(t.TempDir(), "RTK")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if err := store.ReplaceUsers("RTK", "org-rtk", "rtk", "admin", []map[string]any{{"email": "admin@example.test", "password": "pw"}}); err != nil {
		t.Fatal(err)
	}
	_, users, err := store.ReadUsersList("RTK")
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Email != "admin@example.test" {
		t.Fatalf("privileged fallback users = %+v", users)
	}
	coverage, err := store.Coverage("RTK")
	if err != nil {
		t.Fatal(err)
	}
	if coverage.Users != 1 {
		t.Fatalf("privileged fallback coverage users = %d, want 1", coverage.Users)
	}
	if err := store.ReplaceUsers("RTK", "org-rtk", "rtk", "member", []map[string]any{{"email": "member@example.test", "password": "pw"}}); err != nil {
		t.Fatal(err)
	}
	_, users, err = store.ReadUsersList("RTK")
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Email != "member@example.test" {
		t.Fatalf("member-preferred users = %+v", users)
	}
	coverage, err = store.Coverage("RTK")
	if err != nil {
		t.Fatal(err)
	}
	if coverage.Users != 1 {
		t.Fatalf("member-preferred coverage users = %d, want 1", coverage.Users)
	}
}

func TestTestDataStoreClearUsersRemovesEveryRoleForBrandOnly(t *testing.T) {
	store, err := openTestDataStore(t.TempDir(), "RTK")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	for _, tc := range []struct {
		brand string
		role  string
		email string
	}{
		{brand: "RTK", role: "member", email: "member@users.local"},
		{brand: "RTK", role: "admin", email: "admin@users.local"},
		{brand: "Other", role: "member", email: "other@users.local"},
	} {
		if err := store.ReplaceUsers(tc.brand, "brand-id", "tenant", tc.role, []map[string]any{{"email": tc.email, "role": tc.role}}); err != nil {
			t.Fatal(err)
		}
	}

	if err := store.ClearUsers("RTK"); err != nil {
		t.Fatal(err)
	}
	if got := testDataUsersCount(store.DB, "RTK"); got != 0 {
		t.Fatalf("expected RTK users to be cleared, got %d", got)
	}
	if got := testDataUsersCount(store.DB, "Other"); got != 1 {
		t.Fatalf("expected other brand users to remain, got %d", got)
	}
}

func TestTestDataStoreUpsertsBindingCheckpoint(t *testing.T) {
	envRoot := t.TempDir()
	store, err := openTestDataStore(envRoot, "RTK")
	if err != nil {
		t.Fatalf("openTestDataStore() error = %v", err)
	}
	defer store.Close()

	devices := []generatedDevice{{
		DeviceID:       "load-device-0001",
		DeviceType:     "light",
		DisplayName:    "Light 1",
		ServiceOptions: []string{"mqtt"},
	}}
	if err := store.ReplaceDevices("RTK", "run-1", devices, nil); err != nil {
		t.Fatalf("ReplaceDevices() error = %v", err)
	}

	first := bindAssignment{
		AssignmentIndex: 0,
		AssignedEmail:   "rtk+001@users.local",
		DeviceID:        "load-device-0001",
		DeviceType:      "light",
		Category:        "mqtt_device",
		ServiceOptions:  []string{"mqtt"},
		AccountDeviceID: "account-device-1",
		OperationID:     "op-1",
		Status:          "provision_requested",
	}
	if err := store.UpsertBinding("RTK", "org-rtk", "rtk", "run-1", first); err != nil {
		t.Fatalf("UpsertBinding(first) error = %v", err)
	}
	updated := first
	updated.OperationID = "op-2"
	updated.Status = "provisioned"
	if err := store.UpsertBinding("RTK", "org-rtk", "rtk", "run-2", updated); err != nil {
		t.Fatalf("UpsertBinding(updated) error = %v", err)
	}

	assignments, err := store.ReadBindAssignments("RTK")
	if err != nil {
		t.Fatalf("ReadBindAssignments() error = %v", err)
	}
	if len(assignments) != 1 {
		t.Fatalf("assignments count = %d, want 1", len(assignments))
	}
	if assignments[0].OperationID != "op-2" || assignments[0].Status != "provisioned" {
		t.Fatalf("assignment not updated: %+v", assignments[0])
	}
	matches, err := store.BindingsMatchDevices("RTK")
	if err != nil {
		t.Fatalf("BindingsMatchDevices() error = %v", err)
	}
	if !matches {
		t.Fatal("binding should match current device set")
	}
	stale := updated
	stale.DeviceID = "old-device-0001"
	if err := store.UpsertBinding("RTK", "org-rtk", "rtk", "run-2", stale); err != nil {
		t.Fatalf("UpsertBinding(stale) error = %v", err)
	}
	matches, err = store.BindingsMatchDevices("RTK")
	if err != nil {
		t.Fatalf("BindingsMatchDevices(stale) error = %v", err)
	}
	if matches {
		t.Fatal("stale binding must not match current device set")
	}
}

func TestImportLegacyLatestAndCleanupPlan(t *testing.T) {
	envRoot := t.TempDir()
	usersDir := filepath.Join(envRoot, "artifacts", "users")
	bindDir := filepath.Join(envRoot, "artifacts", "device-bind")
	deviceDir := filepath.Join(envRoot, "devices", "test_device", "devices", "camera", "load-device-0001")
	bundleDir := filepath.Join(envRoot, "devices", "test_device", "bundles", "camera")
	manifestDir := filepath.Join(envRoot, "devices", "test_device", "manifests")
	for _, dir := range []string{usersDir, bindDir, deviceDir, bundleDir, manifestDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(usersDir, "rtk-users-20200101T000000Z.json"), `{"brandname":"RTK","brand_cloud_id":"old","tenant_slug":"rtk","role":"member","users":[]}`)
	writeTestFile(t, filepath.Join(usersDir, "rtk-users-20200102T000000Z.json"), `{"brandname":"RTK","brand_cloud_id":"org-rtk","tenant_slug":"rtk","role":"member","users":[{"email":"rtk+001@users.local","password":"pw"}]}`)
	writeTestFile(t, filepath.Join(manifestDir, "devices.json"), `[{"device_id":"load-device-0001","device_type":"camera","display_name":"Camera 1","service_options":["mqtt","video_streaming"]}]`)
	writeTestFile(t, filepath.Join(deviceDir, "metadata.json"), `{"device_id":"load-device-0001","device_type":"camera","display_name":"Camera 1","service_options":["mqtt","video_streaming"],"mqtt_capability":"camera","model":"RTC-CAM-PRO2-SIM"}`)
	writeTestFile(t, filepath.Join(deviceDir, "device.cert.pem"), "CERT")
	writeTestFile(t, filepath.Join(deviceDir, "device.key.pem"), "KEY")
	writeTestFile(t, filepath.Join(deviceDir, "device.chain.pem"), "CHAIN")
	writeTestFile(t, filepath.Join(bundleDir, "load-device-0001.pem"), "CERTKEY")
	writeTestFile(t, filepath.Join(bindDir, "rtk-device-bind-new.json"), `{"brandname":"RTK","brand_cloud_id":"org-rtk","tenant_slug":"rtk","count":1,"assignments":[{"assignment_index":0,"assigned_email":"rtk+001@users.local","device_id":"load-device-0001","device_type":"camera","category":"ip_camera","service_options":["mqtt","video_streaming"],"account_device_id":"account-device-1","operation_id":"op-1","status":"provision_requested"}]}`)

	summary, err := importLegacyTestData(envRoot, "RTK", true)
	if err != nil {
		t.Fatalf("importLegacyTestData() error = %v", err)
	}
	if summary.Users != 1 || summary.Devices != 1 || summary.Bindings != 1 || summary.MissingDeviceKeys != 0 {
		t.Fatalf("summary = %+v", summary)
	}
	store, err := openTestDataStore(envRoot, "RTK")
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if got := testDataUsersCount(store.DB, "RTK"); got != 1 {
		t.Fatalf("users count after import = %d", got)
	}

	plan, err := legacyCleanupPlan(envRoot, "RTK")
	if err != nil {
		t.Fatalf("legacyCleanupPlan() error = %v", err)
	}
	if len(plan.Files) == 0 {
		t.Fatal("cleanup plan should include legacy files")
	}
	if testDataContainsString(plan.Files, filepath.Join(envRoot, "reports", "keep.json")) {
		t.Fatal("cleanup plan must not include reports")
	}
	if !testDataContainsString(plan.Files, filepath.Join(bindDir, "rtk-device-bind-new.json")) || !testDataContainsString(plan.Files, filepath.Join(deviceDir, "device.key.pem")) {
		t.Fatalf("cleanup files = %+v", plan.Files)
	}
}

func TestRunTestDataImportInspectAndCleanupDryRun(t *testing.T) {
	envRoot := t.TempDir()
	seedLegacyTestData(t, envRoot)

	importOut := runRTKCloudForTest(t, "test-data", "import-legacy", "--env-root", envRoot, "--brandname", "RTK", "--latest-only")
	if !strings.Contains(importOut, `"users":1`) || !strings.Contains(importOut, `"devices":1`) || !strings.Contains(importOut, `"bindings":1`) {
		t.Fatalf("import output = %s", importOut)
	}
	inspectOut := runRTKCloudForTest(t, "test-data", "inspect", "--env-root", envRoot, "--brandname", "RTK")
	if !strings.Contains(inspectOut, `"schema":"rtk-cloud-workspace-test-data/v1"`) || !strings.Contains(inspectOut, `"users":1`) {
		t.Fatalf("inspect output = %s", inspectOut)
	}
	cleanupOut := runRTKCloudForTest(t, "test-data", "cleanup-legacy", "--env-root", envRoot, "--brandname", "RTK", "--dry-run", "--confirm", "RTK")
	if !strings.Contains(cleanupOut, `"action":"dry_run"`) || !strings.Contains(cleanupOut, `device.key.pem`) {
		t.Fatalf("cleanup output = %s", cleanupOut)
	}
	if _, err := os.Stat(filepath.Join(envRoot, "devices", "test_device", "devices", "camera", "load-device-0001", "device.key.pem")); err != nil {
		t.Fatalf("dry-run must not delete legacy file: %v", err)
	}
}

func testDataUsersCount(db *sql.DB, brand string) int {
	var count int
	_ = db.QueryRow(`select count(*) from users where brandname = ?`, brand).Scan(&count)
	return count
}

func seedLegacyTestData(t *testing.T, envRoot string) {
	t.Helper()
	usersDir := filepath.Join(envRoot, "artifacts", "users")
	bindDir := filepath.Join(envRoot, "artifacts", "device-bind")
	deviceDir := filepath.Join(envRoot, "devices", "test_device", "devices", "camera", "load-device-0001")
	bundleDir := filepath.Join(envRoot, "devices", "test_device", "bundles", "camera")
	manifestDir := filepath.Join(envRoot, "devices", "test_device", "manifests")
	for _, dir := range []string{usersDir, bindDir, deviceDir, bundleDir, manifestDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeTestFile(t, filepath.Join(usersDir, "rtk-users-20200102T000000Z.json"), `{"brandname":"RTK","brand_cloud_id":"org-rtk","tenant_slug":"rtk","role":"member","users":[{"email":"rtk+001@users.local","password":"pw"}]}`)
	writeTestFile(t, filepath.Join(manifestDir, "devices.json"), `[{"device_id":"load-device-0001","device_type":"camera","display_name":"Camera 1","service_options":["mqtt","video_streaming"]}]`)
	writeTestFile(t, filepath.Join(deviceDir, "metadata.json"), `{"device_id":"load-device-0001","device_type":"camera","display_name":"Camera 1","service_options":["mqtt","video_streaming"],"mqtt_capability":"camera","model":"RTC-CAM-PRO2-SIM"}`)
	writeTestFile(t, filepath.Join(deviceDir, "device.cert.pem"), "CERT")
	writeTestFile(t, filepath.Join(deviceDir, "device.key.pem"), "KEY")
	writeTestFile(t, filepath.Join(deviceDir, "device.chain.pem"), "CHAIN")
	writeTestFile(t, filepath.Join(bundleDir, "load-device-0001.pem"), "CERTKEY")
	writeTestFile(t, filepath.Join(bindDir, "rtk-device-bind-20200102T000000Z.json"), `{"brandname":"RTK","brand_cloud_id":"org-rtk","tenant_slug":"rtk","count":1,"assignments":[{"assignment_index":0,"assigned_email":"rtk+001@users.local","device_id":"load-device-0001","device_type":"camera","category":"ip_camera","service_options":["mqtt","video_streaming"],"account_device_id":"account-device-1","operation_id":"op-1","status":"provision_requested"}]}`)
}

func runRTKCloudForTest(t *testing.T, args ...string) string {
	t.Helper()
	if len(args) == 0 {
		t.Fatal("missing command")
	}
	spec, ok := commands[args[0]]
	if !ok {
		t.Fatalf("command %q not registered", args[0])
	}
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := spec.run(args[1:])
	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)
	_ = r.Close()
	if runErr != nil {
		t.Fatalf("%s failed: %v\nstdout=%s", strings.Join(args, " "), runErr, string(out))
	}
	return string(out)
}

func mustJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func testDataContainsString(items []string, want string) bool {
	for _, item := range items {
		if item == want {
			return true
		}
	}
	return false
}
