package home100k

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const e2eFixtureVersion = "cloud-admin-e2e.v1"

type E2EFixtureManifest struct {
	Scenario       string `json:"scenario"`
	RunID          string `json:"run_id"`
	GeneratedAt    string `json:"generated_at"`
	FixtureVersion string `json:"fixture_version"`
	BrandCount     int    `json:"brand_count"`
	DeviceCount    int    `json:"device_count"`
	UserCount      int    `json:"user_count"`
}

type E2EBrandCloud struct {
	ID                    string         `json:"id"`
	Name                  string         `json:"name"`
	Role                  string         `json:"role"`
	OrganizationKind      string         `json:"organization_kind"`
	Status                string         `json:"status"`
	Tier                  string         `json:"tier"`
	EvaluationDeviceQuota int            `json:"evaluation_device_quota"`
	Metadata              map[string]any `json:"metadata"`
}

type E2EBrandCloudUser struct {
	ID                        string `json:"id"`
	BrandCloudID              string `json:"brand_cloud_id"`
	Email                     string `json:"email"`
	DisplayName               string `json:"display_name"`
	EmailVerified             bool   `json:"email_verified"`
	SignupPendingVerification bool   `json:"signup_pending_verification"`
	CreatedAt                 string `json:"created_at"`
	UpdatedAt                 string `json:"updated_at"`
	DisabledAt                string `json:"disabled_at,omitempty"`
}

type E2EBrandCloudMember struct {
	OrganizationID   string `json:"organization_id"`
	UserID           string `json:"user_id"`
	BrandCloudUserID string `json:"brand_cloud_user_id"`
	Email            string `json:"email"`
	Role             string `json:"role"`
}

type E2EOperation struct {
	ID                  string `json:"id"`
	DeviceID            string `json:"device_id"`
	DeviceName          string `json:"device_name"`
	OrganizationID      string `json:"organization_id"`
	Organization        string `json:"organization"`
	Type                string `json:"type"`
	State               string `json:"state"`
	UpstreamOperationID string `json:"upstream_operation_id"`
	Message             string `json:"message"`
	UpdatedAt           string `json:"updated_at"`
}

type E2EDevice struct {
	ID             string `json:"id"`
	OrganizationID string `json:"organization_id"`
	Organization   string `json:"organization"`
	Name           string `json:"name"`
	Category       string `json:"category"`
	Model          string `json:"model"`
	SerialNumber   string `json:"serial_number"`
	Status         string `json:"status"`
	Readiness      string `json:"readiness"`
	LastSeenAt     string `json:"last_seen_at"`
}

type E2ESession struct {
	Kind     string `json:"kind"`
	Email    string `json:"email"`
	Password string `json:"password"`
}

type E2EServiceLog struct {
	Service     string `json:"service"`
	Level       string `json:"level"`
	Timestamp   string `json:"timestamp"`
	Message     string `json:"message"`
	TraceID     string `json:"trace_id"`
	RequestID   string `json:"request_id"`
	OperationID string `json:"operation_id"`
}

type E2EFixture struct {
	BrandClouds []E2EBrandCloud       `json:"brand_clouds"`
	Users       []E2EBrandCloudUser   `json:"brand_cloud_users"`
	Members     []E2EBrandCloudMember `json:"members"`
	Devices     []E2EDevice           `json:"devices"`
	Operations  []E2EOperation        `json:"operations"`
	ServiceLogs []E2EServiceLog       `json:"service_logs"`
	Sessions    []E2ESession          `json:"sessions"`
	Prometheus  map[string]any        `json:"prometheus_series"`
	Manifest    E2EFixtureManifest    `json:"manifest"`
}

func GenerateE2EFixture(scenario, runID, outDir string, now time.Time) (E2EFixtureManifest, error) {
	if strings.TrimSpace(scenario) == "" || strings.TrimSpace(runID) == "" || strings.TrimSpace(outDir) == "" {
		return E2EFixtureManifest{}, fmt.Errorf("scenario, run id, and output directory are required")
	}
	plan, err := loadBrandPlan(scenario)
	if err != nil {
		return E2EFixtureManifest{}, err
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return E2EFixtureManifest{}, err
	}
	timestamp := now.UTC().Format(time.RFC3339)
	fixture := E2EFixture{}
	for brandIndex, brand := range plan.Brands {
		brandID := fmt.Sprintf("brand-e2e-%02d", brandIndex+1)
		status := "active"
		if brandIndex == 1 {
			status = "disabled"
		}
		cloud := E2EBrandCloud{
			ID: brandID, Name: brand.Brandname, Role: "platform_admin", OrganizationKind: "brand_cloud",
			Status: status, Tier: "evaluation", EvaluationDeviceQuota: brand.Devices,
			Metadata: map[string]any{"brandname": brand.Brandname, "run_id": runID, "device_count": brand.Devices, "setup_status": "ready"},
		}
		fixture.BrandClouds = append(fixture.BrandClouds, cloud)
		ownerID := fmt.Sprintf("bcu-%02d-owner", brandIndex+1)
		ownerEmail := fmt.Sprintf("owner%02d@%s.example", brandIndex+1, strings.ToLower(strings.ReplaceAll(brand.Brandname, " ", "-")))
		fixture.Users = append(fixture.Users, E2EBrandCloudUser{ID: ownerID, BrandCloudID: brandID, Email: ownerEmail, DisplayName: "E2E Owner", EmailVerified: true, CreatedAt: timestamp, UpdatedAt: timestamp})
		fixture.Members = append(fixture.Members, E2EBrandCloudMember{OrganizationID: brandID, UserID: ownerID, BrandCloudUserID: ownerID, Email: ownerEmail, Role: "owner"})
		for userIndex := 1; userIndex < brand.NormalUsers; userIndex++ {
			userID := fmt.Sprintf("bcu-%02d-user-%02d", brandIndex+1, userIndex)
			email := fmt.Sprintf("user%02d-%02d@e2e.example", brandIndex+1, userIndex)
			pending := brandIndex == 1 && userIndex == 1
			fixture.Users = append(fixture.Users, E2EBrandCloudUser{ID: userID, BrandCloudID: brandID, Email: email, DisplayName: "E2E User", EmailVerified: !pending, SignupPendingVerification: pending, CreatedAt: timestamp, UpdatedAt: timestamp})
		}
	}
	for index := 0; index < plan.TotalDevices; index++ {
		brand := fixture.BrandClouds[index%len(fixture.BrandClouds)]
		fixture.Devices = append(fixture.Devices, E2EDevice{ID: fmt.Sprintf("dev-e2e-%03d", index+1), OrganizationID: brand.ID, Organization: brand.Name, Name: fmt.Sprintf("E2E Camera %03d", index+1), Category: "ip_camera", Model: "E2E-CAM-01", SerialNumber: fmt.Sprintf("E2E-%03d", index+1), Status: "online", Readiness: "activated", LastSeenAt: timestamp})
		fixture.Operations = append(fixture.Operations, E2EOperation{ID: fmt.Sprintf("op-e2e-%03d", index+1), DeviceID: fmt.Sprintf("dev-e2e-%03d", index+1), DeviceName: fmt.Sprintf("E2E Camera %03d", index+1), OrganizationID: brand.ID, Organization: brand.Name, Type: "DeviceProvisionRequested", State: "published", UpstreamOperationID: fmt.Sprintf("upstream-e2e-%03d", index+1), Message: "Waiting for downstream completion.", UpdatedAt: timestamp})
	}
	fixture.Operations = append(fixture.Operations, E2EOperation{ID: "op-e2e-failed", DeviceID: "dev-e2e-001", DeviceName: "E2E Camera 001", OrganizationID: fixture.BrandClouds[0].ID, Organization: fixture.BrandClouds[0].Name, Type: "DeviceDeactivateRequested", State: "failed", UpstreamOperationID: "upstream-e2e-failed", Message: "E2E upstream rejected request.", UpdatedAt: timestamp})
	fixture.ServiceLogs = []E2EServiceLog{{Service: "cloud-admin", Level: "error", Timestamp: timestamp, Message: "E2E operation failed while calling upstream.", TraceID: "trace-e2e-001", RequestID: "request-e2e-001", OperationID: "upstream-e2e-failed"}}
	fixture.Sessions = []E2ESession{{Kind: "platform_admin", Email: "platform.admin@example.com", Password: "e2e-platform-password"}, {Kind: "customer", Email: "customer@example.com", Password: "e2e-customer-password"}}
	fixture.Prometheus = map[string]any{"source_status": "configured", "run_id": runID, "targets_up": 8, "targets_down": 0, "workloads_degraded": 0, "nodes_ready": 3, "services_up": 5}
	fixture.Manifest = E2EFixtureManifest{Scenario: scenario, RunID: runID, GeneratedAt: timestamp, FixtureVersion: e2eFixtureVersion, BrandCount: len(fixture.BrandClouds), DeviceCount: plan.TotalDevices, UserCount: len(fixture.Users)}
	files := map[string]any{
		"brand-clouds.json":        fixture.BrandClouds,
		"brand-cloud-users.json":   fixture.Users,
		"brand-cloud-members.json": fixture.Members,
		"devices.json":             fixture.Devices,
		"operations.json":          fixture.Operations,
		"service-logs.json":        fixture.ServiceLogs,
		"sessions.json":            fixture.Sessions,
		"prometheus-series.json":   fixture.Prometheus,
		"manifest.json":            fixture.Manifest,
	}
	for name, value := range files {
		if err := writeE2EJSON(filepath.Join(outDir, name), value); err != nil {
			return E2EFixtureManifest{}, err
		}
	}
	return fixture.Manifest, nil
}

func writeE2EJSON(path string, value any) error {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return os.WriteFile(path, data, 0o644)
}

func sortedFixtureBrandNames(brands []E2EBrandCloud) []string {
	names := make([]string, 0, len(brands))
	for _, brand := range brands {
		names = append(names, brand.Name)
	}
	sort.Strings(names)
	return names
}
