package recovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// OpenBaoRaftSource records references and public identity, never token content
// or a CA key. Original seal compatibility is enforced by the snapshot API.
type OpenBaoRaftSource struct {
	URL           string `json:"url"`
	CACertFile    string `json:"ca_cert_file"`
	CAFingerprint string `json:"ca_sha256"`
	TokenFile     string `json:"token_file"`
	ClusterID     string `json:"cluster_id"`
}

func (s OpenBaoRaftSource) Validate() error {
	u, err := url.Parse(s.URL)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || s.ClusterID == "" || strings.ContainsAny(s.ClusterID, "\x00\r\n") || len(s.CAFingerprint) != 64 || strings.Trim(s.CAFingerprint, "0123456789abcdef") != "" || !filepath.IsAbs(s.CACertFile) || !filepath.IsAbs(s.TokenFile) {
		return errors.New("exact HTTPS OpenBao Raft identity and private credential references required")
	}
	return nil
}

type raftBackupClient struct {
	source OpenBaoRaftSource
	client *http.Client
	token  string
}

func newRaftBackupClient(source OpenBaoRaftSource) (*raftBackupClient, error) {
	if err := source.Validate(); err != nil {
		return nil, err
	}
	for _, path := range []string{source.CACertFile, source.TokenFile} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() > 1<<20 {
			return nil, errors.New("OpenBao backup references require bounded private regular files")
		}
	}
	raw, err := os.ReadFile(source.CACertFile)
	if err != nil {
		return nil, errors.New("OpenBao backup trust unavailable")
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, errors.New("exact OpenBao backup trust anchor required")
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !certificate.IsCA {
		return nil, errors.New("OpenBao backup trust anchor invalid")
	}
	digest := sha256.Sum256(certificate.Raw)
	if hex.EncodeToString(digest[:]) != source.CAFingerprint {
		return nil, errors.New("OpenBao backup trust pin differs")
	}
	pool := x509.NewCertPool()
	pool.AddCert(certificate)
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, DisableKeepAlives: true}
	token, err := os.ReadFile(source.TokenFile)
	if err != nil || len(bytes.TrimSpace(token)) == 0 || bytes.ContainsAny(bytes.TrimSpace(token), "\r\n") {
		return nil, errors.New("OpenBao backup authorization unavailable")
	}
	return &raftBackupClient{source, &http.Client{Transport: transport, Timeout: 2 * time.Minute, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("OpenBao backup redirects forbidden") }}, strings.TrimSpace(string(token))}, nil
}
func (c *raftBackupClient) request(ctx context.Context, method, path string, in io.Reader) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.source.URL, "/")+"/v1/"+path, in)
	if err != nil {
		return nil, errors.New("invalid OpenBao backup operation")
	}
	req.Header.Set("X-Vault-Token", c.token)
	if in != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	response, err := c.client.Do(req)
	if err != nil {
		return nil, errors.New("OpenBao backup API unavailable (details suppressed)")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		response.Body.Close()
		return nil, errors.New("OpenBao backup API refused the operation (details suppressed)")
	}
	return response, nil
}
func (c *raftBackupClient) verify(ctx context.Context) error {
	response, err := c.request(ctx, http.MethodGet, "sys/seal-status", nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	var state struct {
		Initialized bool   `json:"initialized"`
		Sealed      bool   `json:"sealed"`
		Type        string `json:"type"`
		Shares      int    `json:"n"`
		Threshold   int    `json:"t"`
		ClusterID   string `json:"cluster_id"`
		Storage     string `json:"storage_type"`
	}
	if json.NewDecoder(io.LimitReader(response.Body, 64<<10)).Decode(&state) != nil || !state.Initialized || state.Sealed || state.Storage != "raft" || state.Type != "shamir" || state.Shares != 5 || state.Threshold != 3 || state.ClusterID != c.source.ClusterID {
		return errors.New("OpenBao backup requires the selected unsealed Raft cluster and 5/3 Shamir custody")
	}
	return nil
}

func (e *Engine) captureOpenBaoRaft(ctx context.Context, component Component, out io.Writer) error {
	if component.OpenBao == nil {
		return errors.New("OpenBao Raft source binding missing")
	}
	client, err := newRaftBackupClient(*component.OpenBao)
	if err != nil {
		return err
	}
	if err := client.verify(ctx); err != nil {
		return err
	}
	response, err := client.request(ctx, http.MethodGet, "sys/storage/raft/snapshot", nil)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	count, err := io.Copy(out, io.LimitReader(response.Body, e.Config.MaxArchiveBytes+1))
	if err != nil || count < 1 || count > e.Config.MaxArchiveBytes {
		return errors.New("OpenBao snapshot capture incomplete or oversized")
	}
	return client.verify(ctx)
}

func (e *Engine) restoreOpenBaoRaft(ctx context.Context, component Component, path string) error {
	if component.OpenBao == nil {
		return errors.New("OpenBao Raft target binding missing")
	}
	client, err := newRaftBackupClient(*component.OpenBao)
	if err != nil {
		return err
	}
	if err := client.verify(ctx); err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		return errors.New("verified OpenBao snapshot unavailable")
	}
	defer f.Close()
	// Never use snapshot-force or reinitialize a seal in ordinary recovery. A
	// distinct isolated migration must explicitly recover the original seal.
	response, err := client.request(ctx, http.MethodPost, "sys/storage/raft/snapshot", f)
	if err != nil {
		return err
	}
	response.Body.Close()
	return nil
}
