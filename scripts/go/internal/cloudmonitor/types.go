package cloudmonitor

import (
	"context"
	"time"
)

const SchemaVersion = 1

type Status string

const (
	Pass          Status = "PASS"
	Warn          Status = "WARN"
	Fail          Status = "FAIL"
	Unknown       Status = "UNKNOWN"
	NotApplicable Status = "NOT_APPLICABLE"
	NotRun        Status = "NOT_RUN"
)

type Result struct {
	Service              string     `json:"service"`
	CheckID              string     `json:"check_id"`
	Layer                string     `json:"layer"`
	Required             bool       `json:"required"`
	Status               Status     `json:"status"`
	ObservedStatus       Status     `json:"observed_status,omitempty"`
	ConsecutiveFailures  int        `json:"consecutive_failures,omitempty"`
	ConsecutiveSuccesses int        `json:"consecutive_successes,omitempty"`
	ObservedAt           time.Time  `json:"observed_at"`
	DurationMS           int64      `json:"duration_ms,omitempty"`
	Value                *float64   `json:"value,omitempty"`
	Unit                 string     `json:"unit,omitempty"`
	Threshold            string     `json:"threshold,omitempty"`
	Source               string     `json:"source,omitempty"`
	Reason               string     `json:"reason"`
	Recommendation       string     `json:"recommendation,omitempty"`
	ExpiresAt            *time.Time `json:"expires_at,omitempty"`
	Fingerprint          string     `json:"fingerprint,omitempty"`
}

type Sample struct {
	Service    string    `json:"service"`
	Name       string    `json:"name"`
	Value      float64   `json:"value"`
	Unit       string    `json:"unit"`
	Kind       string    `json:"kind"` // gauge or counter
	ObservedAt time.Time `json:"observed_at"`
	Identity   string    `json:"identity,omitempty"` // stable labels, never credentials
}

type Collection struct {
	Results []Result
	Samples []Sample
}

type Coverage struct {
	Required int     `json:"required"`
	Verified int     `json:"verified"`
	Unknown  int     `json:"unknown"`
	NotRun   int     `json:"not_run"`
	Percent  float64 `json:"percent"`
}

type Snapshot struct {
	SchemaVersion     int       `json:"schema_version"`
	RunID             string    `json:"run_id"`
	Environment       string    `json:"environment"`
	Stack             string    `json:"stack"`
	SourceFingerprint string    `json:"source_fingerprint"`
	Profile           string    `json:"profile"`
	StartedAt         time.Time `json:"started_at"`
	FinishedAt        time.Time `json:"finished_at"`
	Overall           Status    `json:"overall"`
	Coverage          Coverage  `json:"coverage"`
	Results           []Result  `json:"results"`
	Samples           []Sample  `json:"samples"`
	ReportError       string    `json:"report_error,omitempty"`
}

type WorkloadTarget struct {
	ID             string `json:"id"`
	Service        string `json:"service"`
	Namespace      string `json:"namespace"`
	Kind           string `json:"kind"`
	Name           string `json:"name"`
	Required       bool   `json:"required"`
	Enabled        bool   `json:"enabled"`
	DisabledReason string `json:"disabled_reason,omitempty"`
	IntentUnknown  bool   `json:"intent_unknown,omitempty"`
}

type Endpoint struct {
	Service      string `json:"service"`
	URL          string `json:"url"`
	Path         string `json:"path,omitempty"`
	SuccessCodes []int  `json:"success_codes"`
	LivenessOnly bool   `json:"liveness_only"`
}

type MetricTarget struct {
	Job       string `json:"job"`
	Namespace string `json:"namespace"`
	Service   string `json:"service"`
	Port      int    `json:"port"`
	Path      string `json:"path"`
}

type Inventory struct {
	SchemaVersion     int              `json:"schema_version"`
	Environment       string           `json:"environment"`
	Stack             string           `json:"stack"`
	SourceFingerprint string           `json:"source_fingerprint"`
	GeneratedAt       time.Time        `json:"generated_at"`
	Targets           []WorkloadTarget `json:"targets"`
	Endpoints         []Endpoint       `json:"endpoints"`
	MetricTargets     []MetricTarget   `json:"metric_targets"`
	CoverageNotes     []string         `json:"coverage_notes,omitempty"`
}

type Config struct {
	SchemaVersion        int                   `json:"schema_version"`
	Environment          string                `json:"environment"`
	Stack                string                `json:"stack"`
	InventoryFile        string                `json:"inventory_file"`
	CertificateTool      string                `json:"certificate_tool,omitempty"`
	CertificateInventory string                `json:"certificate_inventory,omitempty"`
	Chromium             string                `json:"chromium,omitempty"`
	Timezone             string                `json:"timezone,omitempty"`
	DailyAt              string                `json:"daily_at,omitempty"`
	TimeoutSeconds       int                   `json:"timeout_seconds,omitempty"`
	Concurrency          int                   `json:"concurrency,omitempty"`
	RetentionDays        int                   `json:"retention_days,omitempty"`
	HTTP                 []HTTPCheck           `json:"http,omitempty"`
	TLS                  []TLSCheck            `json:"tls,omitempty"`
	Tokens               []TokenCheck          `json:"tokens,omitempty"`
	Postgres             []PostgresCheck       `json:"postgres,omitempty"`
	PostgresBackups      []PostgresBackupCheck `json:"postgres_backups,omitempty"`
	Redis                []RedisCheck          `json:"redis,omitempty"`
	Metrics              []MetricCheck         `json:"metrics,omitempty"`
	Synthetic            SyntheticConfig       `json:"synthetic,omitempty"`
}

type HTTPCheck struct {
	Name           string  `json:"name"`
	Service        string  `json:"service"`
	URL            string  `json:"url"`
	SuccessCodes   []int   `json:"success_codes"`
	Contains       string  `json:"contains,omitempty"`
	TokenFile      string  `json:"token_file,omitempty"`
	CAFile         string  `json:"ca_file,omitempty"`
	ClientCertFile string  `json:"client_cert_file,omitempty"`
	ClientKeyFile  string  `json:"client_key_file,omitempty"`
	Required       bool    `json:"required"`
	LivenessOnly   bool    `json:"liveness_only,omitempty"`
	MaxLatencyMS   float64 `json:"max_latency_ms,omitempty"`
}

type TLSCheck struct {
	Name                 string   `json:"name"`
	Service              string   `json:"service"`
	Address              string   `json:"address,omitempty"`
	ServerName           string   `json:"server_name,omitempty"`
	CertFile             string   `json:"cert_file,omitempty"`
	CAFile               string   `json:"ca_file,omitempty"`
	ClientCertFile       string   `json:"client_cert_file,omitempty"`
	ClientKeyFile        string   `json:"client_key_file,omitempty"`
	ClientCAFile         string   `json:"client_ca_file,omitempty"`
	ClientCRLFiles       []string `json:"client_crl_files,omitempty"`
	ApplicationURL       string   `json:"application_url,omitempty"`
	ApplicationContains  string   `json:"application_contains,omitempty"`
	ApplicationTokenFile string   `json:"application_token_file,omitempty"`
	CRLFiles             []string `json:"crl_files,omitempty"`
	Required             bool     `json:"required"`
	Role                 string   `json:"role,omitempty"` // leaf, ca, or managed
	WarnDays             float64  `json:"warn_days,omitempty"`
	CriticalDays         float64  `json:"critical_days,omitempty"`
	RenewalFile          string   `json:"renewal_file,omitempty"`
	ExpectedFingerprint  string   `json:"expected_fingerprint,omitempty"`
}

type TokenCheck struct {
	Name            string   `json:"name"`
	Service         string   `json:"service"`
	Kind            string   `json:"kind"` // jwt, opaque
	TokenFile       string   `json:"token_file"`
	KeyFile         string   `json:"key_file,omitempty"`
	Algorithm       string   `json:"algorithm,omitempty"`
	Issuer          string   `json:"issuer,omitempty"`
	Audience        string   `json:"audience,omitempty"`
	RequiredScopes  []string `json:"required_scopes,omitempty"`
	Subject         string   `json:"subject,omitempty"`
	SubjectClaim    string   `json:"subject_claim,omitempty"`
	ValidationURL   string   `json:"validation_url,omitempty"`
	CAFile          string   `json:"ca_file,omitempty"`
	MetadataFile    string   `json:"metadata_file,omitempty"`
	Required        bool     `json:"required"`
	WarnSeconds     int      `json:"warn_seconds,omitempty"`
	CriticalSeconds int      `json:"critical_seconds,omitempty"`
}

type VolumeCheck struct {
	Namespace string `json:"namespace"`
	Pod       string `json:"pod"`
	Container string `json:"container,omitempty"`
	Mount     string `json:"mount"`
}

type PostgresCheck struct {
	Name                  string       `json:"name"`
	DSNFile               string       `json:"dsn_file"`
	Required              bool         `json:"required"`
	Volume                *VolumeCheck `json:"volume,omitempty"`
	MaxProbeLatencyMS     float64      `json:"max_probe_latency_ms,omitempty"`
	MaxConnectionsPercent float64      `json:"max_connections_percent,omitempty"`
}

// PostgresBackupCheck reads the backup controller's redacted status ConfigMap.
// It never starts a backup or a restore drill.
type PostgresBackupCheck struct {
	Name            string  `json:"name"`
	Required        bool    `json:"required"`
	Enabled         bool    `json:"enabled"`
	Namespace       string  `json:"namespace"`
	ClusterID       string  `json:"cluster_id"`
	StatusConfigMap string  `json:"status_config_map"`
	WarnAgeHours    float64 `json:"warn_age_hours,omitempty"`
	FailAgeHours    float64 `json:"fail_age_hours,omitempty"`
}

type RedisCheck struct {
	Name                string       `json:"name"`
	Address             string       `json:"address"`
	Username            string       `json:"username,omitempty"`
	PasswordFile        string       `json:"password_file,omitempty"`
	CAFile              string       `json:"ca_file,omitempty"`
	Required            bool         `json:"required"`
	Role                string       `json:"role"` // cache, shadow, fleet
	DB                  int          `json:"db,omitempty"`
	ContainerLimitBytes float64      `json:"container_limit_bytes,omitempty"`
	Volume              *VolumeCheck `json:"volume,omitempty"`
	MaxProbeLatencyMS   float64      `json:"max_probe_latency_ms,omitempty"`
}

type MetricCheck struct {
	Name             string   `json:"name"`
	Service          string   `json:"service"`
	URL              string   `json:"url"` // Prometheus API base
	CAFile           string   `json:"ca_file,omitempty"`
	TokenFile        string   `json:"token_file,omitempty"`
	Query            string   `json:"query"`
	Unit             string   `json:"unit"`
	Required         bool     `json:"required"`
	WarnAbove        *float64 `json:"warn_above,omitempty"`
	FailAbove        *float64 `json:"fail_above,omitempty"`
	Baseline         float64  `json:"validated_baseline,omitempty"`
	BaselineEvidence string   `json:"baseline_evidence,omitempty"`
	MaxAgeSeconds    int      `json:"max_age_seconds,omitempty"`
}

type SyntheticConfig struct {
	Auth []AuthProbe `json:"auth,omitempty"`
	MQTT []MQTTProbe `json:"mqtt,omitempty"`
	TURN []TURNProbe `json:"turn,omitempty"`
}

type AuthProbe struct {
	Name            string `json:"name"`
	Kind            string `json:"kind"` // account-manager, video-cloud
	LoginURL        string `json:"login_url,omitempty"`
	RefreshURL      string `json:"refresh_url"`
	ValidationURL   string `json:"validation_url"`
	CredentialsFile string `json:"credentials_file"`
	CAFile          string `json:"ca_file,omitempty"`
}

type MQTTProbe struct {
	Name            string `json:"name"`
	Broker          string `json:"broker"`
	Topic           string `json:"topic"`
	CredentialsFile string `json:"credentials_file"`
	CAFile          string `json:"ca_file,omitempty"`
}

type TURNProbe struct {
	Name            string `json:"name"`
	Address         string `json:"address"`
	Network         string `json:"network"`
	Realm           string `json:"realm"`
	PeerAddress     string `json:"peer_address"`
	CredentialsFile string `json:"credentials_file"`
}

type Runtime struct {
	ConfigRoot string
	Workspace  string
	Kubeconfig string
	OutDir     string
}

type CommandRunner interface {
	Run(context.Context, string, []string, []byte, []string) ([]byte, int, error)
}

func Merge(parts ...Collection) Collection {
	var out Collection
	for _, p := range parts {
		out.Results = append(out.Results, p.Results...)
		out.Samples = append(out.Samples, p.Samples...)
	}
	return out
}

func Summarize(results []Result) (Status, Coverage) {
	status := Pass
	var c Coverage
	for _, r := range results {
		if r.Layer == "reference" {
			continue
		}
		switch r.Status {
		case Pass, Warn, Fail, Unknown, NotApplicable, NotRun:
		default:
			r.Status = Unknown
		}
		if r.Required && r.Status != NotApplicable {
			c.Required++
			if r.Status == Unknown {
				c.Unknown++
			} else if r.Status == NotRun {
				c.NotRun++
			} else {
				c.Verified++
			}
		}
		if r.Required && r.Status == Fail {
			status = Fail
		}
		if status != Fail && r.Required && (r.Status == Unknown || r.Status == NotRun) {
			status = Unknown
		}
		if status == Pass && (r.Status == Warn || r.Status == Fail) {
			status = Warn
		}
	}
	if c.Required > 0 {
		c.Percent = 100 * float64(c.Verified) / float64(c.Required)
	} else {
		status = Unknown
	}
	return status, c
}
