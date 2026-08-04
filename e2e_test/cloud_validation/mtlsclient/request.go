package mtlsclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const maxResponseBytes = 1 << 20

// HTTPStatusError identifies an authenticated HTTP response that did not have
// a successful status. Callers can use the status without exposing the body.
type HTTPStatusError struct {
	StatusCode int
}

// ProbeBearerGET performs a bearer-authorized GET over the supplied mTLS
// identity. The bearer value is read from a protected JSON response file and
// is never returned or logged.
func ProbeBearerGET(ctx context.Context, url, certPath, keyPath, caPath, tokenPath string, timeout time.Duration) (int, error) {
	client, err := newClient(certPath, keyPath, caPath, timeout)
	if err != nil {
		return 0, err
	}
	raw, err := os.ReadFile(tokenPath)
	if err != nil {
		return 0, fmt.Errorf("read bearer token file: %w", err)
	}
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.Unmarshal(raw, &token); err != nil || token.AccessToken == "" {
		return 0, fmt.Errorf("bearer token file contains no access_token")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, fmt.Errorf("build probe request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+token.AccessToken)
	resp, err := client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("mTLS probe: %w", err)
	}
	defer resp.Body.Close()
	_, err = io.Copy(io.Discard, io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return 0, fmt.Errorf("read probe response: %w", err)
	}
	return resp.StatusCode, nil
}

func newClient(certPath, keyPath, caPath string, timeout time.Duration) (*http.Client, error) {
	identity, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return nil, fmt.Errorf("load client identity: %w", err)
	}
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{identity}, MinVersion: tls.VersionTLS12}
	if caPath != "" {
		caPEM, err := os.ReadFile(caPath)
		if err != nil {
			return nil, fmt.Errorf("read server CA: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(caPEM) {
			return nil, fmt.Errorf("server CA contains no certificates")
		}
		tlsConfig.RootCAs = roots
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	return &http.Client{Timeout: timeout, Transport: &http.Transport{TLSClientConfig: tlsConfig}}, nil
}

func (e *HTTPStatusError) Error() string {
	return fmt.Sprintf("mTLS request returned HTTP %d", e.StatusCode)
}

// Request sends a JSON request with a PEM client identity and writes the
// successful response to a mode-0600 file. Credential contents and response
// bodies are intentionally never written to stdout or stderr.
func Request(ctx context.Context, url, certPath, keyPath, caPath, requestPath, outputPath string, timeout time.Duration) error {
	client, err := newClient(certPath, keyPath, caPath, timeout)
	if err != nil {
		return err
	}
	body, err := os.ReadFile(requestPath)
	if err != nil {
		return fmt.Errorf("read request body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("mTLS request: %w", err)
	}
	defer resp.Body.Close()
	responseBody, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return fmt.Errorf("read response: %w", err)
	}
	if len(responseBody) > maxResponseBytes {
		return fmt.Errorf("response exceeds %d bytes", maxResponseBytes)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		return &HTTPStatusError{StatusCode: resp.StatusCode}
	}
	if err := os.MkdirAll(filepath.Dir(outputPath), 0o700); err != nil {
		return fmt.Errorf("create output directory: %w", err)
	}
	tmp, err := os.CreateTemp(filepath.Dir(outputPath), ".token-response-*")
	if err != nil {
		return fmt.Errorf("create response file: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return fmt.Errorf("protect response file: %w", err)
	}
	if _, err := tmp.Write(responseBody); err != nil {
		tmp.Close()
		return fmt.Errorf("write response file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close response file: %w", err)
	}
	if err := os.Rename(tmpPath, outputPath); err != nil {
		return fmt.Errorf("publish response file: %w", err)
	}
	return nil
}
