package accountvideosmoke

import "time"

const ResultSchema = "rtk-account-video-provisioning-smoke/v1"

type Status string

const (
	StatusPass    Status = "PASS"
	StatusFail    Status = "FAIL"
	StatusSkip    Status = "SKIP"
	StatusBlocked Status = "BLOCKED"
)

type Config struct {
	RunID                    string        `json:"run_id"`
	AccountUsersDir          string        `json:"account_users_dir,omitempty"`
	DeviceCertsetDir         string        `json:"device_certset_dir,omitempty"`
	AccountManagerBaseURL    string        `json:"account_manager_base_url,omitempty"`
	VideoCloudBaseURL        string        `json:"video_cloud_base_url,omitempty"`
	VideoCloudDeviceBaseURL  string        `json:"video_cloud_device_base_url,omitempty"`
	ArtifactDir              string        `json:"artifact_dir"`
	DeviceID                 string        `json:"device_id,omitempty"`
	DeviceName               string        `json:"device_name,omitempty"`
	ClaimToken               string        `json:"-"`
	StrictBlocked            bool          `json:"strict_blocked"`
	Timeout                  time.Duration `json:"timeout"`
	ProvisioningPollInterval time.Duration `json:"provisioning_poll_interval"`
	ProvisioningPollAttempts int           `json:"provisioning_poll_attempts"`
}

type Result struct {
	Schema    string            `json:"schema"`
	RunID     string            `json:"run_id"`
	StartedAt time.Time         `json:"started_at"`
	EndedAt   time.Time         `json:"ended_at"`
	Overall   Status            `json:"overall"`
	Config    Config            `json:"config"`
	Steps     []StepResult      `json:"steps"`
	Artifacts map[string]string `json:"artifacts,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type StepResult struct {
	Name       string `json:"name"`
	Status     Status `json:"status"`
	Reason     string `json:"reason"`
	StatusCode int    `json:"status_code,omitempty"`
	Evidence   string `json:"evidence,omitempty"`
}

func (r Result) HasStep(name string, status Status) bool {
	for _, step := range r.Steps {
		if step.Name == name && step.Status == status {
			return true
		}
	}
	return false
}

type AccountFixture struct {
	Email          string `json:"email"`
	Password       string `json:"password"`
	UserID         string `json:"user_id,omitempty"`
	OrganizationID string `json:"organization_id,omitempty"`
}

type DeviceCertset struct {
	RootDir         string
	DeviceDir       string
	DeviceID        string
	DeviceKeyPath   string
	DeviceCertPath  string
	DeviceChainPath string
	DeviceCAPath    string
}

func (d DeviceCertset) Summary() string {
	out := "devid=" + d.DeviceID
	if d.DeviceDir != "" {
		out += " device_dir=" + d.DeviceDir
	}
	if d.DeviceKeyPath != "" {
		out += " key_path=" + d.DeviceKeyPath
	}
	if d.DeviceChainPath != "" {
		out += " chain_path=" + d.DeviceChainPath
	}
	if d.DeviceCAPath != "" {
		out += " ca_path=" + d.DeviceCAPath
	}
	return out
}
