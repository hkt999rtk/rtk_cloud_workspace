package cloudvalidation

import "time"

type Status string

const (
	StatusPass    Status = "PASS"
	StatusFail    Status = "FAIL"
	StatusSkip    Status = "SKIP"
	StatusBlocked Status = "BLOCKED"
)

type ScenarioManifest struct {
	SchemaVersion int        `yaml:"schema_version" json:"schema_version"`
	Scenarios     []Scenario `yaml:"scenarios" json:"scenarios"`
}

type Scenario struct {
	ID                    string   `yaml:"id" json:"id"`
	TestID                string   `yaml:"test_id" json:"test_id"`
	RequiredCapabilities  []string `yaml:"required_capabilities" json:"required_capabilities"`
	DeviceProfile         string   `yaml:"device_profile" json:"device_profile"`
	AppAction             string   `yaml:"app_action" json:"app_action"`
	ExpectedSDKResult     string   `yaml:"expected_sdk_result" json:"expected_sdk_result"`
	ExpectedCloudEvidence []string `yaml:"expected_cloud_evidence" json:"expected_cloud_evidence"`
	Timeout               string   `yaml:"timeout" json:"timeout"`
	Cleanup               string   `yaml:"cleanup" json:"cleanup"`
}

type Config struct {
	Environment       string
	Platform          string
	Mode              string
	RunID             string
	OutDir            string
	ScenarioFiles     []string
	PlanOnly          bool
	AccountManagerURL string
	VideoCloudURL     string
	DeviceURL         string
	MQTTAddr          string
	BrandCloudSlug    string
	CABundle          string
	RuntimeBundle     string
	SDKCommit         string
	ServerVersion     string
	ArtifactPath      string
	ArtifactChecksum  string
	ReadinessCommand  string
	SetupCommand      string
	VirtualCommand    string
	PlatformCommand   string
	EvidenceCommand   string
	CleanupCommand    string
	ReadyFile         string
	PlatformResult    string
	CloudEvidenceFile string
	ResourceManifest  string
	ReadyTimeout      time.Duration
	RunTimeout        time.Duration
}

type PlatformResult struct {
	SchemaVersion int              `json:"schema_version"`
	RunID         string           `json:"run_id"`
	Platform      string           `json:"platform"`
	SDKCommit     string           `json:"sdk_commit"`
	ServerVersion string           `json:"server_version"`
	Status        Status           `json:"status"`
	Results       []ScenarioResult `json:"results"`
}

type ScenarioResult struct {
	TestID        string   `json:"test_id,omitempty"`
	ScenarioID    string   `json:"scenario_id"`
	Status        Status   `json:"status"`
	ReasonCode    string   `json:"reason_code,omitempty"`
	Reason        string   `json:"reason,omitempty"`
	DurationMS    int64    `json:"duration_ms"`
	SDKErrorCode  string   `json:"sdk_error_code,omitempty"`
	CorrelationID string   `json:"correlation_id"`
	Evidence      []string `json:"evidence,omitempty"`
}

type RunReport struct {
	SchemaVersion    int               `json:"schema_version"`
	RunID            string            `json:"run_id"`
	Environment      string            `json:"environment"`
	Platform         string            `json:"platform"`
	Mode             string            `json:"mode"`
	Status           Status            `json:"status"`
	StartedAt        time.Time         `json:"started_at"`
	CompletedAt      time.Time         `json:"completed_at"`
	SDKCommit        string            `json:"sdk_commit"`
	ContractsCommit  string            `json:"contracts_commit"`
	ServerVersion    string            `json:"server_version"`
	BrandCloudSlug   string            `json:"brand_cloud_slug"`
	ArtifactChecksum string            `json:"artifact_checksum,omitempty"`
	Host             map[string]string `json:"host"`
	Toolchains       map[string]string `json:"toolchains"`
	Artifacts        map[string]string `json:"artifacts"`
	ResourceManifest string            `json:"resource_manifest,omitempty"`
	ResourceCount    int               `json:"resource_count"`
	StatusCounts     map[Status]int    `json:"status_counts"`
	Steps            []StepResult      `json:"steps"`
	PlatformResult   *PlatformResult   `json:"platform_result,omitempty"`
	Scenarios        []Scenario        `json:"scenarios"`
}

type StepResult struct {
	Name       string `json:"name"`
	Status     Status `json:"status"`
	ReasonCode string `json:"reason_code,omitempty"`
	Reason     string `json:"reason"`
	Evidence   string `json:"evidence,omitempty"`
}
