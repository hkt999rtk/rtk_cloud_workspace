package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"
)

const deploymentPullDiagnosticLimit = 64 << 10

// Docker may emit registry credentials or signed URLs on stderr. Keep only a
// bounded in-memory prefix for classification and never return or persist it.
type deploymentPullDiagnostic struct{ data []byte }

func (d *deploymentPullDiagnostic) Write(p []byte) (int, error) {
	if remaining := deploymentPullDiagnosticLimit - len(d.data); remaining > 0 {
		d.data = append(d.data, p[:min(remaining, len(p))]...)
	}
	return len(p), nil
}

func classifyDeploymentPullDiagnostic(data []byte) (code string, transient bool) {
	s := strings.ToLower(string(data))
	contains := func(parts ...string) bool {
		for _, part := range parts {
			if strings.Contains(s, part) {
				return true
			}
		}
		return false
	}
	// Permanent errors precede network signatures so a mixed diagnostic is
	// never retried merely because unrelated text contains a timeout.
	switch {
	case contains("unauthorized", "authentication required", "authentication failed", "access denied", "denied: ", "status: 401", "status: 403"):
		return "AUTH", false
	case contains("no space left on device", "disk quota exceeded"):
		return "NO_SPACE", false
	case contains("no matching manifest", "no match for platform", "platform not found"):
		return "PLATFORM", false
	case contains("manifest unknown", "manifest not found", "not found: manifest", "invalid reference format"):
		return "MANIFEST", false
	case contains("too many requests", "rate limit", "status: 429"):
		return "RATE_LIMIT", false
	case contains("i/o timeout", "tls handshake timeout", "client.timeout exceeded", "context deadline exceeded", "network timeout"):
		return "NETWORK_TIMEOUT", true
	case contains("temporary failure in name resolution", "server misbehaving", "connection reset by peer", "connection refused", "network is unreachable", "unexpected eof"):
		return "NETWORK_TRANSIENT", true
	case contains("status: 502", "status: 503", "status: 504", "502 bad gateway", "503 service unavailable", "504 gateway timeout"):
		return "REGISTRY_TRANSIENT", true
	default:
		return "UNKNOWN", false
	}
}

func waitDeploymentPullRetry(ctx context.Context, retry int) error {
	delay := time.Second
	if retry > 1 {
		delay = 2 * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

// Only static classification and ordinal reach the terminal/private log. Raw
// stderr, image references and operator credentials never cross this boundary.
func reportDeploymentPullRetry(ctx context.Context, code string, attempt int) {
	switch code {
	case "NETWORK_TIMEOUT", "NETWORK_TRANSIENT", "REGISTRY_TRANSIENT":
	default:
		return
	}
	if attempt < 1 || attempt > 2 {
		return
	}
	progress, ok := ctx.Value(environmentProgressKey{}).(*environmentProgress)
	if !ok {
		return
	}
	progress.mu.Lock()
	defer progress.mu.Unlock()
	progress.clearTerminalLine()
	line := fmt.Sprintf("[RETRY] deployment.release.pull reason=%s attempt=%d/3", code, attempt+1)
	if progress.out != nil {
		fmt.Fprintln(progress.out, line)
	}
	if progress.log != nil {
		fmt.Fprintln(progress.log, line)
	}
	if progress.terminal {
		progress.writeTerminal()
	}
}

var _ io.Writer = (*deploymentPullDiagnostic)(nil)
