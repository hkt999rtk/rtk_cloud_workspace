// Package postgresbackup creates encrypted, standalone PostgreSQL physical backups.
// This format is deliberately separate from matched core recovery archives.
package postgresbackup

import (
	"errors"
	"io"
	"math"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"filippo.io/age"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
)

const Version = 1
const Scope = "postgres-physical"
const PostgresMajor = 16

type Source struct {
	Host             string `json:"host"`
	Port             int    `json:"port"`
	User             string `json:"user"`
	PasswordFile     string `json:"password_file"`
	SSLMode          string `json:"ssl_mode"`
	RootCertFile     string `json:"root_cert_file,omitempty"`
	PostgresImage    string `json:"postgres_image"`
	SystemIdentifier string `json:"system_identifier"`
	MaxRate          string `json:"max_rate,omitempty"`
}

type Config struct {
	Version           int             `json:"version"`
	Environment       string          `json:"environment"`
	Stack             string          `json:"stack"`
	ClusterID         string          `json:"cluster_id"`
	Directory         string          `json:"directory"`
	Source            Source          `json:"source"`
	Recipients        []string        `json:"recipients"`
	Remote            recovery.Remote `json:"remote"`
	TimeoutSeconds    int             `json:"timeout_seconds"`
	MaxArchiveBytes   int64           `json:"max_archive_bytes"`
	MaxPlaintextBytes int64           `json:"max_plaintext_bytes"`
	RetentionDays     int             `json:"retention_days"`
	MinimumBackups    int             `json:"minimum_backups"`
}

type Manifest struct {
	Version              int       `json:"version"`
	Scope                string    `json:"scope"`
	ID                   string    `json:"backup_id"`
	Environment          string    `json:"environment"`
	Stack                string    `json:"stack"`
	ClusterID            string    `json:"cluster_id"`
	PostgresMajor        int       `json:"postgres_major"`
	PostgresImage        string    `json:"postgres_image"`
	SystemIdentifier     string    `json:"system_identifier"`
	StartedAt            time.Time `json:"started_at"`
	FinishedAt           time.Time `json:"finished_at"`
	NativeManifestSHA256 string    `json:"native_manifest_sha256"`
}

type CaptureResult struct {
	Manifest Manifest
	File     string
}

func Decode(r io.Reader) (Config, error) {
	var c Config
	if err := recovery.Decode(io.LimitReader(r, 1<<20), &c); err != nil {
		return c, errors.New("invalid PostgreSQL backup configuration")
	}
	return c, c.Validate()
}

var imageDigest = regexp.MustCompile(`^\S+@sha256:[a-f0-9]{64}$`)
var systemIdentifier = regexp.MustCompile(`^[1-9][0-9]{0,19}$`)
var sha256Hex = regexp.MustCompile(`^[a-f0-9]{64}$`)
var rate = regexp.MustCompile(`^[1-9][0-9]*(k|M)?$`)

func (c Config) Validate() error {
	if c.Version != Version || !recovery.Name.MatchString(c.Environment) || !recovery.Name.MatchString(c.Stack) || !recovery.Name.MatchString(c.ClusterID) {
		return errors.New("invalid PostgreSQL backup version or environment identity")
	}
	if !cleanAbsolute(c.Directory) || c.Directory == "/" || c.TimeoutSeconds < 1 || c.MaxArchiveBytes < 1 || c.MaxPlaintextBytes < 1 {
		return errors.New("private absolute directory, timeout and archive/plaintext limits are required")
	}
	if c.MaxPlaintextBytes > math.MaxInt64-c.MaxArchiveBytes-(64<<20) {
		return errors.New("backup limits exceed supported capacity range")
	}
	if c.RetentionDays < 14 || c.MinimumBackups < 14 {
		return errors.New("retain at least 14 days and 14 successful backups")
	}
	if len(c.Recipients) == 0 {
		return errors.New("age encryption recipients required")
	}
	for _, r := range c.Recipients {
		if _, err := age.ParseX25519Recipient(r); err != nil {
			return errors.New("invalid age recipient")
		}
	}
	u, err := url.Parse(c.Remote.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || c.Remote.Region == "" || c.Remote.Bucket == "" || !recovery.SafeRelative(c.Remote.Prefix) || !strings.HasPrefix(c.Remote.Prefix, c.Environment+"/") {
		return errors.New("environment-owned private HTTPS backup destination required")
	}
	s := c.Source
	if s.Host == "" || strings.ContainsAny(s.Host, "\x00\r\n") || strings.ContainsAny(s.User, "\x00\r\n") || s.User == "" || s.Port < 1 || s.Port > 65535 || !cleanAbsolute(s.PasswordFile) || !imageDigest.MatchString(s.PostgresImage) || !systemIdentifier.MatchString(s.SystemIdentifier) {
		return errors.New("invalid PostgreSQL source, credential reference, image or system identifier")
	}
	if s.SSLMode != "verify-full" && s.SSLMode != "require" && s.SSLMode != "disable" {
		return errors.New("PostgreSQL ssl_mode must be verify-full, require or disable")
	}
	if (s.SSLMode == "verify-full" && !cleanAbsolute(s.RootCertFile)) || (s.RootCertFile != "" && !cleanAbsolute(s.RootCertFile)) {
		return errors.New("invalid PostgreSQL root certificate reference")
	}
	if s.MaxRate != "" && !rate.MatchString(s.MaxRate) {
		return errors.New("invalid PostgreSQL backup max_rate")
	}
	return nil
}

func cleanAbsolute(p string) bool { return filepath.IsAbs(p) && filepath.Clean(p) == p }

func (m Manifest) Validate() error {
	if m.Version != Version || m.Scope != Scope || !recovery.Name.MatchString(m.ID) || !recovery.Name.MatchString(m.Environment) || !recovery.Name.MatchString(m.Stack) || !recovery.Name.MatchString(m.ClusterID) || m.PostgresMajor != PostgresMajor || !imageDigest.MatchString(m.PostgresImage) || !systemIdentifier.MatchString(m.SystemIdentifier) || !sha256Hex.MatchString(m.NativeManifestSHA256) || m.StartedAt.IsZero() || m.FinishedAt.Before(m.StartedAt) || m.FinishedAt.IsZero() {
		return errors.New("invalid PostgreSQL physical backup manifest")
	}
	return nil
}

func (c Config) MatchManifest(m Manifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if m.Environment != c.Environment || m.Stack != c.Stack || m.ClusterID != c.ClusterID || m.SystemIdentifier != c.Source.SystemIdentifier || m.PostgresImage != c.Source.PostgresImage {
		return errors.New("PostgreSQL backup source identity differs from reviewed target")
	}
	return nil
}
