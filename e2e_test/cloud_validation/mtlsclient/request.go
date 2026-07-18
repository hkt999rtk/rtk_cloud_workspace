package mtlsclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

const maxResponseBytes = 1 << 20

// Request sends a JSON request with a PEM client identity and writes the
// successful response to a mode-0600 file. Credential contents and response
// bodies are intentionally never written to stdout or stderr.
func Request(ctx context.Context, url, certPath, keyPath, caPath, requestPath, outputPath string, timeout time.Duration) error {
	identity, err := tls.LoadX509KeyPair(certPath, keyPath)
	if err != nil {
		return fmt.Errorf("load client identity: %w", err)
	}
	tlsConfig := &tls.Config{Certificates: []tls.Certificate{identity}, MinVersion: tls.VersionTLS12}
	if caPath != "" {
		caPEM, err := os.ReadFile(caPath)
		if err != nil {
			return fmt.Errorf("read server CA: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(caPEM) {
			return fmt.Errorf("server CA contains no certificates")
		}
		tlsConfig.RootCAs = roots
	}
	body, err := os.ReadFile(requestPath)
	if err != nil {
		return fmt.Errorf("read request body: %w", err)
	}
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	client := &http.Client{
		Timeout:   timeout,
		Transport: &http.Transport{TLSClientConfig: tlsConfig},
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
		return fmt.Errorf("mTLS request returned HTTP %d", resp.StatusCode)
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
