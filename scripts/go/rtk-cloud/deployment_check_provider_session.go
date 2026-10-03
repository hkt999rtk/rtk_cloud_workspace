package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const deploymentCheckHTTPTimeout = 15 * time.Second
const deploymentCheckCleanupTimeout = 30 * time.Second

type deploymentInventoryEntry struct {
	ready    chan struct{}
	raw      []byte
	err      error
	code     string
	attempts int
}

type deploymentProviderSession struct {
	mu        sync.Mutex
	inventory map[string]*deploymentInventoryEntry
}

type deploymentProviderTrace struct {
	mu       sync.Mutex
	attempts int
	code     string
	reused   bool
}

func (t *deploymentProviderTrace) record(attempt int, code string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if attempt > t.attempts {
		t.attempts = attempt
	}
	t.code = code
}

func (c deploymentCredentialChecker) checkContext() context.Context {
	if c.ctx != nil {
		return c.ctx
	}
	return context.Background()
}

func (c deploymentCredentialChecker) withCheckContext(ctx context.Context) deploymentCredentialChecker {
	c.ctx = ctx
	client := c.client
	if client == nil {
		client = http.DefaultClient
	}
	next := *client
	base := next.Transport
	if previous, ok := base.(*deploymentCheckTransport); ok {
		base = previous.base
	}
	if base == nil {
		base = http.DefaultTransport
	}
	// The transport bounds each attempt and the body read. The caller's context
	// bounds the whole check, including a single retry and cleanup.
	next.Timeout = 0
	next.Transport = &deploymentCheckTransport{ctx: ctx, base: base, trace: c.trace}
	c.client = &next
	return c
}

type deploymentCheckTransport struct {
	ctx   context.Context
	base  http.RoundTripper
	trace *deploymentProviderTrace
}

type deploymentCheckBody struct {
	io.ReadCloser
	cancel  context.CancelFunc
	trace   *deploymentProviderTrace
	attempt int
}

func (b *deploymentCheckBody) Close() error { defer b.cancel(); return b.ReadCloser.Close() }
func (b *deploymentCheckBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if err != nil && err != io.EOF {
		b.trace.record(b.attempt, deploymentRequestCode(err))
	}
	return n, err
}

func (t *deploymentCheckTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	parent := t.ctx
	trace := t.trace
	if parent == nil {
		parent = req.Context()
	}
	// An explicitly detached cleanup request carries its own bounded context.
	if req.Context().Value(deploymentCleanupContextKey{}) != nil {
		parent = req.Context()
		trace = nil
	}
	safeRetry := req.Method == http.MethodGet || req.Method == http.MethodHead
	for attempt := 1; attempt <= 2; attempt++ {
		if err := parent.Err(); err != nil {
			trace.record(attempt, deploymentRequestCode(err))
			return nil, err
		}
		attemptCtx, cancel := context.WithTimeout(parent, deploymentCheckHTTPTimeout)
		stop := context.AfterFunc(req.Context(), cancel)
		next := req.Clone(attemptCtx)
		response, err := t.base.RoundTrip(next)
		code, transient := "", false
		if err != nil {
			code = deploymentRequestCode(err)
			var network net.Error
			transient = errors.Is(err, io.EOF) || (errors.As(err, &network) && (network.Timeout() || network.Temporary()))
		} else if response.StatusCode >= 400 {
			code = deploymentHTTPCode(response.StatusCode)
			transient = response.StatusCode == 429 || response.StatusCode == 502 || response.StatusCode == 503 || response.StatusCode == 504
		}
		trace.record(attempt, code)
		if safeRetry && transient && attempt == 1 && parent.Err() == nil {
			delay := 200 * time.Millisecond
			if response != nil {
				delay = deploymentRetryDelay(response.Header.Get("Retry-After"), time.Now())
				_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
				_ = response.Body.Close()
			}
			stop()
			cancel()
			timer := time.NewTimer(delay)
			select {
			case <-parent.Done():
				timer.Stop()
				return nil, parent.Err()
			case <-timer.C:
			}
			continue
		}
		if err != nil {
			stop()
			cancel()
			return nil, err
		}
		response.Body = &deploymentCheckBody{ReadCloser: response.Body, cancel: func() { stop(); cancel() }, trace: trace, attempt: attempt}
		return response, nil
	}
	panic("unreachable")
}

type deploymentCleanupContextKey struct{}

func deploymentCleanupContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(context.Background(), deploymentCheckCleanupTimeout)
	return context.WithValue(ctx, deploymentCleanupContextKey{}, true), cancel
}

func deploymentHTTPCode(status int) string {
	switch status {
	case 401:
		return "AUTH_REJECTED"
	case 403:
		return "ACCESS_DENIED"
	case 404:
		return "NOT_FOUND"
	case 429:
		return "RATE_LIMITED"
	}
	if status >= 500 {
		return "PROVIDER_UNAVAILABLE"
	}
	return "HTTP_ERROR"
}
func deploymentRequestCode(err error) string {
	if errors.Is(err, context.Canceled) {
		return "CANCELED"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "TIMEOUT"
	}
	var dns *net.DNSError
	if errors.As(err, &dns) {
		if dns.IsTimeout {
			return "TIMEOUT"
		}
		return "DNS_LOOKUP_FAILED"
	}
	var invalid x509.CertificateInvalidError
	var unknown x509.UnknownAuthorityError
	var hostname x509.HostnameError
	if errors.As(err, &invalid) || errors.As(err, &unknown) || errors.As(err, &hostname) {
		return "TLS_FAILED"
	}
	var network net.Error
	if errors.As(err, &network) && network.Timeout() {
		return "TIMEOUT"
	}
	return "CONNECTION_FAILED"
}
func sanitizedDeploymentRequestError(err error) error {
	// Raw url.Error and helper output may contain credentials or signed URLs.
	return fmt.Errorf("request failed (%s)", deploymentRequestCode(err))
}

func (c deploymentCredentialChecker) cachedStorageInventory(resource string, result any, fetch func() ([]byte, error)) error {
	if c.session == nil {
		raw, err := fetch()
		if err != nil {
			return err
		}
		return json.Unmarshal(raw, result)
	}
	s := c.session
	s.mu.Lock()
	entry, exists := s.inventory[resource]
	if !exists {
		entry = &deploymentInventoryEntry{ready: make(chan struct{})}
		s.inventory[resource] = entry
	}
	s.mu.Unlock()
	if exists {
		if c.trace != nil {
			c.trace.mu.Lock()
			c.trace.reused = true
			c.trace.mu.Unlock()
		}
		select {
		case <-entry.ready:
		case <-c.checkContext().Done():
			return c.checkContext().Err()
		}
	} else {
		entry.raw, entry.err = fetch()
		if c.trace != nil {
			c.trace.mu.Lock()
			entry.code, entry.attempts = c.trace.code, c.trace.attempts
			c.trace.mu.Unlock()
		}
		close(entry.ready)
	}
	if entry.err != nil {
		if exists {
			c.trace.record(entry.attempts, entry.code)
		}
		return entry.err
	}
	return json.Unmarshal(entry.raw, result)
}

// Honor server backoff while leaving the overall caller deadline authoritative.
func deploymentRetryDelay(raw string, now time.Time) time.Duration {
	raw = strings.TrimSpace(raw)
	if seconds, err := strconv.ParseInt(raw, 10, 64); err == nil && seconds >= 0 {
		if seconds > 86400 {
			seconds = 86400
		}
		return time.Duration(seconds) * time.Second
	}
	if date, err := http.ParseTime(raw); err == nil && date.After(now) {
		return min(date.Sub(now), 24*time.Hour)
	}
	return 200 * time.Millisecond
}
