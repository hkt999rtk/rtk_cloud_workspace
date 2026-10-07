// Package recovery implements the portable, encrypted core-backup format.
// Live resource manipulation stays in the deployment adapter, not the archive reader.
package recovery

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"filippo.io/age"
)

const Version = 1

var Name = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,62}$`)

type Artifact struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type Manifest struct {
	Version              int               `json:"version"`
	Scope                string            `json:"scope"`
	ID                   string            `json:"backup_id"`
	Environment          string            `json:"environment"`
	Stack                string            `json:"stack"`
	CreatedAt            time.Time         `json:"created_at"`
	ConfigurationSHA256  string            `json:"configuration_sha256"`
	Components           []Component       `json:"components"`
	Artifacts            []Artifact        `json:"artifacts"`
	ExternalDependencies []string          `json:"external_dependencies"`
	Evidence             map[string]string `json:"evidence"`
}

// Component identifies explicitly approved data. Namespace/name are not inferred
// from a backup when selecting a restore target: the target inventory owns them.
type Component struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"` // postgres, redis, volume, secretstore, k8s-object
	Namespace    string   `json:"namespace,omitempty"`
	Pod          string   `json:"pod,omitempty"`
	Container    string   `json:"container,omitempty"`
	Database     string   `json:"database,omitempty"`
	User         string   `json:"user,omitempty"`
	Prefixes     []string `json:"prefixes,omitempty"`
	ExcludeKeys  []string `json:"exclude_keys,omitempty"`
	PVC          string   `json:"pvc,omitempty"`
	Image        string   `json:"image,omitempty"`
	Purpose      string   `json:"purpose,omitempty"` // openbao-file, sqlite, checkpoint, configuration
	SQLiteFiles  []string `json:"sqlite_files,omitempty"`
	Paths        []string `json:"paths,omitempty"`
	Resource     string   `json:"resource,omitempty"`
	ResourceName string   `json:"resource_name,omitempty"`
}

type Workload struct {
	Namespace string `json:"namespace"`
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Role      string `json:"role"` // application, data (online DB), or offline (file store)
}

type Check struct {
	ID   string   `json:"id"`
	Argv []string `json:"argv"`
}

type Config struct {
	PreflightChecks      []Check     `json:"preflight_checks"`
	StartupChecks        []Check     `json:"startup_checks"`
	ExcludedPVCs         []Exclusion `json:"excluded_pvcs"`
	Version              int         `json:"version"`
	Environment          string      `json:"environment"`
	Stack                string      `json:"stack"`
	Namespaces           []string    `json:"namespaces"`
	LockNamespace        string      `json:"lock_namespace"`
	Directory            string      `json:"directory"`
	Recipients           []string    `json:"recipients"`
	Remote               Remote      `json:"remote"`
	TimeoutSeconds       int         `json:"timeout_seconds"`
	MaxArchiveBytes      int64       `json:"max_archive_bytes"`
	Workloads            []Workload  `json:"workloads"`
	Components           []Component `json:"components"`
	QuiescenceChecks     []Check     `json:"quiescence_checks"`
	RecoveryChecks       []Check     `json:"recovery_checks"`
	HealthChecks         []Check     `json:"health_checks"`
	ExternalDependencies []string    `json:"external_dependencies"`
}

type Exclusion struct {
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	Reason    string `json:"reason"`
}

type Remote struct {
	Endpoint string `json:"endpoint"`
	Region   string `json:"region"`
	Bucket   string `json:"bucket"`
	Prefix   string `json:"prefix"`
}

func Decode(r io.Reader, target any) error {
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return errors.New("invalid recovery JSON (unknown fields or invalid values)")
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return errors.New("trailing recovery JSON")
	}
	return nil
}

func SafeRelative(p string) bool {
	return p != "" && p != "." && !strings.ContainsAny(p, "\\\x00\r\n") && !filepath.IsAbs(p) && filepath.ToSlash(filepath.Clean(p)) == p && p != ".." && !strings.HasPrefix(p, "../")
}

func (c Config) Validate() error {
	if c.Version != Version || !Name.MatchString(c.Environment) || !Name.MatchString(c.Stack) {
		return errors.New("unsupported recovery config version or invalid environment/stack")
	}
	if !filepath.IsAbs(c.Directory) || filepath.Clean(c.Directory) != c.Directory || c.Directory == "/" {
		return errors.New("backup directory must be a dedicated absolute path")
	}
	if c.TimeoutSeconds < 1 || c.MaxArchiveBytes < 1 || len(c.Recipients) == 0 {
		return errors.New("timeout_seconds, max_archive_bytes and recipients are required")
	}
	for _, r := range c.Recipients {
		if _, err := age.ParseX25519Recipient(r); err != nil {
			return errors.New("invalid age X25519 recipient")
		}
	}
	u, err := url.Parse(c.Remote.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || c.Remote.Bucket == "" || c.Remote.Region == "" || !SafeRelative(c.Remote.Prefix) {
		return errors.New("private HTTPS backup destination is required")
	}
	if c.MaxArchiveBytes > 4<<30 {
		return errors.New("v1 archive limit is 4 GiB; use a reviewed larger-backup adapter for larger datasets")
	}
	if !strings.HasPrefix(c.Remote.Prefix, c.Environment+"/") {
		return errors.New("backup prefix must start with environment/")
	}
	ns := map[string]bool{}
	for _, n := range c.Namespaces {
		if !Name.MatchString(n) || ns[n] {
			return errors.New("invalid or duplicate namespace")
		}
		ns[n] = true
	}
	if !ns[c.LockNamespace] {
		return errors.New("lock namespace must be in inventory")
	}
	for _, excluded := range c.ExcludedPVCs {
		if !ns[excluded.Namespace] || !Name.MatchString(excluded.Name) || excluded.Reason == "" {
			return errors.New("invalid PVC exclusion")
		}
	}
	wl := map[string]bool{}
	for _, w := range c.Workloads {
		key := w.Namespace + "/" + w.Kind + "/" + w.Name
		if !ns[w.Namespace] || !Name.MatchString(w.Name) || (w.Kind != "deployment" && w.Kind != "statefulset") || (w.Role != "application" && w.Role != "data" && w.Role != "offline") || wl[key] {
			return errors.New("invalid or duplicate workload")
		}
		wl[key] = true
	}
	if len(wl) == 0 || len(c.Components) == 0 {
		return errors.New("workload and component inventories are required")
	}
	ids, kinds := map[string]bool{}, map[string]bool{}
	datasets := map[string]bool{}
	pgGlobals := map[string]bool{}
	pgData := map[string]bool{}
	openbao := false
	for _, v := range c.Components {
		if !Name.MatchString(v.ID) || ids[v.ID] {
			return errors.New("invalid or duplicate component id")
		}
		ids[v.ID] = true
		kinds[v.Kind] = true
		if v.Kind != "secretstore" && !ns[v.Namespace] {
			return fmt.Errorf("component %s outside namespace inventory", v.ID)
		}
		switch v.Kind {
		case "postgres":
			if !Name.MatchString(v.Pod) || !sqlIdentifier(v.User) || (v.Database != "@globals" && !sqlIdentifier(v.Database)) {
				return errors.New("invalid postgres target")
			}
			if v.Database == "postgres" || strings.HasPrefix(v.Database, "template") {
				return errors.New("system databases cannot be restore targets")
			}
			key := v.Namespace + "/" + v.Pod + "/" + v.Container
			if v.Database == "@globals" {
				pgGlobals[key] = true
			} else {
				pgData[key] = true
			}
			if datasets[key+"/"+v.Database] {
				return errors.New("duplicate PostgreSQL dataset")
			}
			datasets[key+"/"+v.Database] = true
		case "redis":
			if !Name.MatchString(v.Pod) || len(v.Prefixes) == 0 {
				return errors.New("redis requires pod and durable prefixes")
			}
			for _, p := range v.Prefixes {
				if p == "" || strings.ContainsAny(p, "*?[]\\\r\n") || !strings.HasSuffix(p, ":") {
					return errors.New("redis prefixes must be literal namespace prefixes ending in colon")
				}
			}
			for _, key := range v.ExcludeKeys {
				if !validRedisKey(key, v) {
					return errors.New("excluded Redis key must be inside a selected prefix")
				}
			}
		case "volume":
			if !Name.MatchString(v.PVC) || !regexp.MustCompile(`^\S+@sha256:[a-f0-9]{64}$`).MatchString(v.Image) || len(v.ID) > 50 {
				return errors.New("volume requires PVC and digest-pinned helper image")
			}
			if v.Purpose == "openbao-file" {
				openbao = true
			}
			if v.Purpose != "openbao-file" && v.Purpose != "sqlite" && v.Purpose != "checkpoint" && v.Purpose != "configuration" {
				return errors.New("unsupported volume purpose/backend")
			}
			for _, p := range v.SQLiteFiles {
				if !SafeRelative(p) {
					return errors.New("invalid SQLite path")
				}
			}
			if v.Purpose == "sqlite" && len(v.SQLiteFiles) == 0 {
				return errors.New("SQLite volume requires database file inventory")
			}
		case "secretstore":
			if len(v.Paths) == 0 {
				return errors.New("secretstore requires explicit paths")
			}
			for _, p := range v.Paths {
				if !AllowedSecretPath(p) {
					return errors.New("secretstore path outside recoverable runtime material")
				}
			}
		case "k8s-object":
			if (v.Resource != "secret" && v.Resource != "configmap") || !Name.MatchString(v.ResourceName) {
				return errors.New("invalid runtime object")
			}
		default:
			return fmt.Errorf("unsupported component kind %s", v.Kind)
		}
	}
	if !kinds["postgres"] || !kinds["secretstore"] || !openbao {
		return errors.New("core backup requires Postgres, OpenBao file storage and SecretStore")
	}
	for pod := range pgData {
		if !pgGlobals[pod] {
			return errors.New("each PostgreSQL server requires a globals component")
		}
	}
	for _, checks := range [][]Check{c.PreflightChecks, c.StartupChecks, c.QuiescenceChecks, c.RecoveryChecks, c.HealthChecks} {
		if len(checks) == 0 {
			return errors.New("quiescence, recovery and health checks must be configured")
		}
		for _, check := range checks {
			if !Name.MatchString(check.ID) || len(check.Argv) == 0 || check.Argv[0] == "" {
				return errors.New("invalid check")
			}
		}
	}
	if len(c.ExternalDependencies) == 0 {
		return errors.New("core backup must declare excluded external dependencies")
	}
	return nil
}

func AllowedSecretPath(p string) bool {
	return SafeRelative(p) && (p == "runtime" || strings.HasPrefix(p, "runtime/") || p == "pki" || strings.HasPrefix(p, "pki/") || p == "openbao/tls.crt" || p == "openbao/tls.key" || p == "openbao/tls-ca.crt")
}

func sqlIdentifier(s string) bool {
	return regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]{0,62}$`).MatchString(s)
}

func PrivateDirectory(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return errors.New("private directory must be absolute and clean")
	}
	for p := path; p != filepath.Dir(p); p = filepath.Dir(p) {
		i, err := os.Lstat(p)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if i.Mode()&os.ModeSymlink != 0 || !i.IsDir() {
			return errors.New("private directory has symlink or non-directory ancestor")
		}
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return err
	}
	i, err := os.Stat(path)
	if err != nil {
		return err
	}
	if i.Mode().Perm() != 0700 {
		return errors.New("private directory must have mode 0700")
	}
	return nil
}
