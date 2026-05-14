package factoryenrolltest

import "time"

const ResultSchema = "rtk-factory-enroll-test-results/v1"

const enrollPath = "/v1/factory/enroll"

type Config struct {
	FactoryURL    string        `json:"factory_url"`
	AuthKey       string        `json:"-"`
	Count         int           `json:"count"`
	RunID         string        `json:"run_id"`
	FactoryID     string        `json:"factory_id"`
	LineID        string        `json:"line_id"`
	StationID     string        `json:"station_id"`
	FixtureID     string        `json:"fixture_id"`
	OperatorID    string        `json:"operator_id"`
	BatchID       string        `json:"batch_id"`
	Timeout       time.Duration `json:"timeout"`
	Concurrency   int           `json:"concurrency"`
	ArtifactDir   string        `json:"artifact_dir"`
	SerialPrefix  string        `json:"serial_prefix"`
	WriteKeyFiles bool          `json:"write_key_files"`
}

type EnrollRequest struct {
	RequestID    string         `json:"request_id"`
	DeviceID     string         `json:"devid"`
	CSRPem       string         `json:"csr_pem"`
	SerialNumber string         `json:"serial_number,omitempty"`
	FactoryID    string         `json:"factory_id,omitempty"`
	LineID       string         `json:"line_id,omitempty"`
	StationID    string         `json:"station_id,omitempty"`
	FixtureID    string         `json:"fixture_id,omitempty"`
	OperatorID   string         `json:"operator_id,omitempty"`
	BatchID      string         `json:"batch_id,omitempty"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type EnrollResponse struct {
	RequestID           string    `json:"request_id"`
	IssuerRequestID     string    `json:"issuer_request_id"`
	DeviceID            string    `json:"devid"`
	SerialNumber        string    `json:"serial_number"`
	NotBefore           time.Time `json:"not_before"`
	NotAfter            time.Time `json:"not_after"`
	CertificatePEM      string    `json:"certificate_pem"`
	CertificateChainPEM string    `json:"certificate_chain_pem"`
	IssuedAt            time.Time `json:"issued_at"`
}

type Result struct {
	Schema    string            `json:"schema"`
	RunID     string            `json:"run_id"`
	StartedAt time.Time         `json:"started_at"`
	EndedAt   time.Time         `json:"ended_at"`
	Config    ResultConfig      `json:"config"`
	Summary   Summary           `json:"summary"`
	Devices   []DeviceResult    `json:"devices"`
	Errors    map[string]int    `json:"errors,omitempty"`
	Metadata  map[string]string `json:"metadata,omitempty"`
}

type ResultConfig struct {
	FactoryURL  string `json:"factory_url"`
	Count       int    `json:"count"`
	Concurrency int    `json:"concurrency"`
	FactoryID   string `json:"factory_id,omitempty"`
	LineID      string `json:"line_id,omitempty"`
	StationID   string `json:"station_id,omitempty"`
	FixtureID   string `json:"fixture_id,omitempty"`
	BatchID     string `json:"batch_id,omitempty"`
}

type Summary struct {
	Total          int     `json:"total"`
	Successes      int     `json:"successes"`
	Failures       int     `json:"failures"`
	SuccessRate    float64 `json:"success_rate"`
	P95LatencyMS   int64   `json:"p95_latency_ms"`
	P99LatencyMS   int64   `json:"p99_latency_ms"`
	DurationMillis int64   `json:"duration_ms"`
}

type DeviceResult struct {
	Index            int       `json:"index"`
	RequestID        string    `json:"request_id"`
	DeviceID         string    `json:"devid"`
	SerialNumber     string    `json:"serial_number,omitempty"`
	Success          bool      `json:"success"`
	StatusCode       int       `json:"status_code,omitempty"`
	LatencyMillis    int64     `json:"latency_ms"`
	ErrorClass       string    `json:"error_class,omitempty"`
	Error            string    `json:"error,omitempty"`
	CertSubjectCN    string    `json:"cert_subject_cn,omitempty"`
	CertNotBefore    time.Time `json:"cert_not_before,omitempty"`
	CertNotAfter     time.Time `json:"cert_not_after,omitempty"`
	ClientAuthUsable bool      `json:"client_auth_usable,omitempty"`
	CA               bool      `json:"ca,omitempty"`
}
