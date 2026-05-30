package bulkbindvalidation

import "time"

const (
	ArtifactSchema = "rtk-cloud-workspace.bulk-device-bind/v1"
	ResultSchema   = "rtk-cloud-workspace.bulk-bind-validation/v1"

	StatusPass = "pass"
	StatusFail = "fail"
)

type Config struct {
	BindArtifactPath       string `json:"bind_artifact_path"`
	ExpectedCount          int    `json:"expected_count"`
	ExpectedDevicesPerUser int    `json:"expected_devices_per_user"`
}

type Artifact struct {
	Schema       string       `json:"schema"`
	GeneratedAt  string       `json:"generated_at"`
	BrandName    string       `json:"brandname"`
	BrandCloudID string       `json:"brand_cloud_id"`
	Count        int          `json:"count"`
	Assignments  []Assignment `json:"assignments"`
}

type Assignment struct {
	AssignmentIndex int      `json:"assignment_index"`
	AssignedEmail   string   `json:"assigned_email"`
	DeviceID        string   `json:"device_id"`
	DeviceType      string   `json:"device_type"`
	Category        string   `json:"category"`
	ServiceOptions  []string `json:"service_options"`
	ClaimID         string   `json:"claim_id"`
	AccountDeviceID string   `json:"account_device_id"`
	OperationID     string   `json:"operation_id"`
	Status          string   `json:"status"`
}

type Result struct {
	Schema     string      `json:"schema"`
	StartedAt  time.Time   `json:"started_at"`
	EndedAt    time.Time   `json:"ended_at"`
	Overall    string      `json:"overall"`
	Config     Config      `json:"config"`
	Summary    Summary     `json:"summary"`
	UserCounts []UserCount `json:"user_counts"`
	Checks     []Check     `json:"checks"`
	Artifacts  Artifacts   `json:"artifacts,omitempty"`
}

type Summary struct {
	BrandName          string `json:"brandname"`
	BrandCloudID       string `json:"brand_cloud_id"`
	TotalDevices       int    `json:"total_devices"`
	Users              int    `json:"users"`
	ProvisionRequested int    `json:"provision_requested"`
	MQTTOnlyDevices    int    `json:"mqtt_only_devices"`
	VideoDevices       int    `json:"video_devices"`
}

type UserCount struct {
	Email       string `json:"email"`
	DeviceCount int    `json:"device_count"`
}

type Check struct {
	Name     string `json:"name"`
	Status   string `json:"status"`
	Evidence string `json:"evidence"`
}

type Artifacts struct {
	JSONReport     string `json:"json_report,omitempty"`
	MarkdownReport string `json:"markdown_report,omitempty"`
}
