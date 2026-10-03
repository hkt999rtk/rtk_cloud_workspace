package cloudmonitor

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var environmentPattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:-[a-z0-9]+)*$`)

func LoadConfig(path, environment string) (Config, Inventory, error) {
	var c Config
	if err := decodeFile(path, &c); err != nil {
		return c, Inventory{}, err
	}
	if c.SchemaVersion != SchemaVersion || c.Environment != environment || !environmentPattern.MatchString(environment) || c.Stack != "video-cloud-"+environment {
		return c, Inventory{}, errors.New("configuration schema or environment identity mismatch")
	}
	if c.TimeoutSeconds == 0 {
		c.TimeoutSeconds = 5
	}
	if c.Concurrency == 0 {
		c.Concurrency = 8
	}
	if c.RetentionDays == 0 {
		c.RetentionDays = 30
	}
	if c.Timezone == "" {
		c.Timezone = "Asia/Taipei"
	}
	if c.DailyAt == "" {
		c.DailyAt = "08:00"
	}
	if c.TimeoutSeconds < 1 || c.TimeoutSeconds > 120 || c.Concurrency < 1 || c.Concurrency > 32 || c.RetentionDays < 1 || c.RetentionDays > 365 {
		return c, Inventory{}, errors.New("invalid timeout, concurrency or retention")
	}
	if _, err := time.LoadLocation(c.Timezone); err != nil {
		return c, Inventory{}, errors.New("invalid report timezone")
	}
	if _, err := time.Parse("15:04", c.DailyAt); err != nil {
		return c, Inventory{}, errors.New("daily_at must be HH:MM")
	}
	base := filepath.Dir(path)
	resolve := func(p string) string {
		if p == "" || filepath.IsAbs(p) {
			return p
		}
		return filepath.Join(base, p)
	}
	c.InventoryFile = resolve(c.InventoryFile)
	c.CertificateInventory = resolve(c.CertificateInventory)
	if c.CertificateTool != "" && strings.ContainsAny(c.CertificateTool, "/\\") {
		c.CertificateTool = resolve(c.CertificateTool)
	}
	if c.Chromium != "" && strings.ContainsAny(c.Chromium, "/\\") {
		c.Chromium = resolve(c.Chromium)
	}
	var inv Inventory
	if err := decodeFile(c.InventoryFile, &inv); err != nil {
		return c, inv, err
	}
	if inv.SchemaVersion != SchemaVersion || inv.Environment != environment || inv.Stack != c.Stack || len(inv.Targets) == 0 || inv.SourceFingerprint == "" {
		return c, inv, errors.New("inventory schema, identity or expected targets invalid")
	}
	seen := map[string]bool{}
	for _, t := range inv.Targets {
		if t.ID == "" || seen[t.ID] || t.Name == "" || t.Service == "" {
			return c, inv, errors.New("inventory targets must have unique identities")
		}
		seen[t.ID] = true
		if t.Kind != "external" && !strings.HasPrefix(t.Namespace, c.Stack+"-") {
			return c, inv, errors.New("target outside selected environment")
		}
	}
	checkNames := map[string]bool{}
	unique := func(family, service, name string) error {
		key := family + "/" + service + "/" + name
		if name == "" || strings.ContainsAny(name, "\x00\r\n") || checkNames[key] {
			return errors.New("checks require unique valid identities")
		}
		checkNames[key] = true
		return nil
	}
	for _, h := range c.HTTP {
		if err := unique("http", h.Service, h.Name); err != nil {
			return c, inv, err
		}
		if err := validURL(h.URL); err != nil {
			return c, inv, err
		}
		if h.Name == "" || h.Service == "" || len(h.SuccessCodes) == 0 {
			return c, inv, errors.New("HTTP check requires identity and success_codes")
		}
	}
	for _, t := range c.TLS {
		if err := unique("tls", t.Service, t.Name); err != nil {
			return c, inv, err
		}
		if t.Name == "" || t.Service == "" || (t.Address == "" && t.CertFile == "") {
			return c, inv, errors.New("TLS check requires name/service and address or cert_file")
		}
		if t.WarnDays < 0 || t.CriticalDays < 0 || (t.WarnDays > 0 && t.CriticalDays > t.WarnDays) {
			return c, inv, errors.New("invalid certificate thresholds")
		}
		if t.ApplicationURL != "" {
			if err := validURL(t.ApplicationURL); err != nil {
				return c, inv, err
			}
			if t.ClientCertFile == "" || t.ClientKeyFile == "" || t.ApplicationContains == "" {
				return c, inv, errors.New("mTLS application proof requires client identity and expected response marker")
			}
		}
	}
	for _, t := range c.Tokens {
		if err := unique("token", t.Service, t.Name); err != nil {
			return c, inv, err
		}
		if t.WarnSeconds < 0 || t.CriticalSeconds < 0 || (t.WarnSeconds > 0 && t.CriticalSeconds > t.WarnSeconds) {
			return c, inv, errors.New("invalid token thresholds")
		}
		if t.Name == "" || t.Service == "" || t.TokenFile == "" || (t.Kind != "jwt" && t.Kind != "opaque") {
			return c, inv, errors.New("invalid token check")
		}
		if t.Kind == "jwt" && t.Algorithm == "" && t.ValidationURL == "" {
			return c, inv, errors.New("JWT requires a pinned algorithm/key or online validation")
		}
		if t.ValidationURL != "" {
			if err := validURL(t.ValidationURL); err != nil {
				return c, inv, err
			}
		}
	}
	for _, p := range c.Postgres {
		if err := unique("postgres", p.Name, p.Name); err != nil {
			return c, inv, err
		}
		if p.Name == "" || p.DSNFile == "" {
			return c, inv, errors.New("invalid PostgreSQL check")
		}
		if err := validVolume(p.Volume, c.Stack); err != nil {
			return c, inv, err
		}
	}
	for i := range c.PostgresBackups {
		p := &c.PostgresBackups[i]
		if err := unique("postgres-backup", p.Name, p.Name); err != nil {
			return c, inv, err
		}
		if p.WarnAgeHours == 0 {
			p.WarnAgeHours = 26
		}
		if p.FailAgeHours == 0 {
			p.FailAgeHours = 28
		}
		if p.WarnAgeHours <= 0 || p.FailAgeHours <= p.WarnAgeHours {
			return c, inv, errors.New("invalid PostgreSQL backup freshness thresholds")
		}
		if !strings.HasPrefix(p.Namespace, c.Stack+"-") || p.ClusterID == "" || p.StatusConfigMap == "" || strings.ContainsAny(p.StatusConfigMap, "/\\\x00\r\n ") {
			return c, inv, errors.New("invalid environment-scoped PostgreSQL backup status source")
		}
	}
	for _, r := range c.Redis {
		if err := unique("redis", r.Name, r.Name); err != nil {
			return c, inv, err
		}
		if r.Name == "" || r.Address == "" || (r.Role != "cache" && r.Role != "shadow" && r.Role != "fleet") {
			return c, inv, errors.New("invalid Redis role or address")
		}
		if err := validVolume(r.Volume, c.Stack); err != nil {
			return c, inv, err
		}
	}
	for _, m := range c.Metrics {
		if err := unique("metric", m.Service, m.Name); err != nil {
			return c, inv, err
		}
		if m.MaxAgeSeconds < 0 || m.Baseline < 0 || (m.WarnAbove != nil && m.FailAbove != nil && *m.WarnAbove > *m.FailAbove) {
			return c, inv, errors.New("invalid metric thresholds")
		}
		if m.Name == "" || m.Service == "" || m.Query == "" {
			return c, inv, errors.New("invalid metric check")
		}
		if err := validURL(m.URL); err != nil {
			return c, inv, err
		}
		if m.Baseline > 0 && m.BaselineEvidence == "" {
			return c, inv, errors.New("validated baseline requires evidence")
		}
	}
	for _, a := range c.Synthetic.Auth {
		if err := unique("synthetic-auth", a.Kind, a.Name); err != nil {
			return c, inv, err
		}
		if a.Name == "" || a.CredentialsFile == "" || (a.Kind != "account-manager" && a.Kind != "video-cloud") {
			return c, inv, errors.New("invalid synthetic authentication configuration")
		}
		for _, u := range []string{a.LoginURL, a.RefreshURL, a.ValidationURL} {
			if u != "" {
				if err := validURL(u); err != nil {
					return c, inv, err
				}
			}
		}
	}
	for _, m := range c.Synthetic.MQTT {
		if err := unique("synthetic-mqtt", "mqtt", m.Name); err != nil {
			return c, inv, err
		}
		if m.Name == "" || m.CredentialsFile == "" || m.Topic == "" || strings.ContainsAny(m.Topic, "+#") || m.Broker == "" {
			return c, inv, errors.New("MQTT probe requires exact authorized topic and credentials")
		}
	}
	for _, t := range c.Synthetic.TURN {
		if err := unique("synthetic-turn", "turn", t.Name); err != nil {
			return c, inv, err
		}
		if t.Name == "" || t.CredentialsFile == "" || t.Address == "" || t.PeerAddress == "" || (t.Network != "udp" && t.Network != "tcp") {
			return c, inv, errors.New("invalid TURN probe")
		}
	}
	return c, inv, nil
}

func decodeFile(path string, out any) error {
	f, e := os.Open(path)
	if e != nil {
		return errors.New("configuration file unavailable")
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 8<<20+1))
	if e != nil || len(b) > 8<<20 {
		return errors.New("configuration file too large or unreadable")
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(out) != nil {
		return errors.New("invalid configuration JSON")
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return errors.New("configuration has trailing data")
	}
	return nil
}

func validURL(raw string) error {
	u, e := url.Parse(raw)
	if e != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return errors.New("endpoint must be HTTP(S) without embedded credentials")
	}
	return nil
}

func validVolume(v *VolumeCheck, stack string) error {
	if v == nil {
		return nil
	}
	if !strings.HasPrefix(v.Namespace, stack+"-") || v.Pod == "" || !filepath.IsAbs(v.Mount) || strings.ContainsAny(v.Mount, "\n\r") {
		return errors.New("invalid environment-scoped volume target")
	}
	return nil
}

func ResolveRuntime(environment, configRoot, workspace, outDir string) (Runtime, error) {
	if !environmentPattern.MatchString(environment) {
		return Runtime{}, errors.New("explicit valid environment required")
	}
	if configRoot == "" {
		configRoot = os.Getenv("RTK_CLOUD_CONFIG_ROOT")
	}
	if configRoot == "" {
		h, e := os.UserHomeDir()
		if e != nil {
			return Runtime{}, e
		}
		configRoot = filepath.Join(h, ".config/rtk_cloud")
	}
	root, e := filepath.Abs(configRoot)
	if e != nil {
		return Runtime{}, e
	}
	ws, e := filepath.Abs(workspace)
	if e != nil {
		return Runtime{}, e
	}
	return Runtime{ConfigRoot: filepath.Join(root, environment), Workspace: ws, Kubeconfig: filepath.Join(root, environment, "kube/kubeconfig.yaml"), OutDir: outDir}, nil
}

// Credential references stay within the selected environment and never become command arguments.
func ReadCredential(rt Runtime, reference string) ([]byte, error) {
	return readEnvironmentFile(rt, reference, true)
}

func ReadPublicFile(rt Runtime, reference string) ([]byte, error) {
	return readEnvironmentFile(rt, reference, false)
}

func readEnvironmentFile(rt Runtime, reference string, private bool) ([]byte, error) {
	if reference == "" {
		return nil, errors.New("credential reference missing")
	}
	p := reference
	if !filepath.IsAbs(p) {
		p = filepath.Join(rt.ConfigRoot, p)
	}
	root, e := filepath.EvalSymlinks(rt.ConfigRoot)
	if e != nil {
		return nil, errors.New("selected SecretStore unavailable")
	}
	actual, e := filepath.EvalSymlinks(p)
	if e != nil {
		return nil, errors.New("credential file unavailable")
	}
	rel, e := filepath.Rel(root, actual)
	if e != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return nil, errors.New("credential outside selected environment")
	}
	info, e := os.Stat(actual)
	if e != nil || !info.Mode().IsRegular() {
		return nil, errors.New("credential file unavailable")
	}
	if private && info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("credential file permissions must exclude group and others")
	}
	f, e := os.Open(actual)
	if e != nil {
		return nil, errors.New("credential file unavailable")
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, 1<<20+1))
	if e != nil || len(b) > 1<<20 {
		return nil, errors.New("credential file unreadable or too large")
	}
	return bytes.TrimSpace(b), nil
}

func HTTPClient(caFile, clientCert, key string, rt Runtime, timeout time.Duration) (*http.Client, error) {
	tc := &tls.Config{MinVersion: tls.VersionTLS12}
	if caFile != "" {
		raw, e := ReadPublicFile(rt, caFile)
		if e != nil {
			return nil, e
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(raw) {
			return nil, errors.New("invalid trust bundle")
		}
		tc.RootCAs = pool
	}
	if clientCert != "" || key != "" {
		cr, e := ReadPublicFile(rt, clientCert)
		if e != nil {
			return nil, e
		}
		kr, e := ReadCredential(rt, key)
		if e != nil {
			return nil, e
		}
		pair, e := tls.X509KeyPair(cr, kr)
		if e != nil {
			return nil, errors.New("invalid client identity")
		}
		tc.Certificates = []tls.Certificate{pair}
	}
	return &http.Client{Timeout: timeout, Transport: &http.Transport{TLSClientConfig: tc}, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}, nil
}

type OSRunner struct{}

func (OSRunner) Run(ctx context.Context, name string, args []string, stdin []byte, env []string) ([]byte, int, error) {
	c := exec.CommandContext(ctx, name, args...)
	c.Stdin = bytes.NewReader(stdin)
	c.Env = append(os.Environ(), env...)
	var out boundedOutput
	c.Stdout = &out
	c.Stderr = io.Discard
	e := c.Run()
	if e == nil {
		return out.Bytes(), 0, nil
	}
	var ee *exec.ExitError
	if errors.As(e, &ee) {
		return out.Bytes(), ee.ExitCode(), errors.New("command returned nonzero")
	}
	return out.Bytes(), -1, errors.New("command unavailable or timed out")
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(p []byte) (int, error) {
	if b.Len()+len(p) > 32<<20 {
		return 0, fmt.Errorf("command output exceeds limit")
	}
	return b.Buffer.Write(p)
}
