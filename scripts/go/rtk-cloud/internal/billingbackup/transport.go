package billingbackup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Client struct {
	BaseURL string
	Token   string
	HTTP    *http.Client
}

// HTTPError conveys only an authoritative status, never an endpoint, token or
// untrusted response body. A transport timeout is deliberately not a 404.
type HTTPError struct{ Status int }

func (e *HTTPError) Error() string {
	return fmt.Sprintf("lifecycle request rejected (HTTP %d); authoritative status required", e.Status)
}

func (c Client) Request(ctx context.Context, method, path string, body, result any) error {
	u, err := url.Parse(c.BaseURL)
	relative, parseErr := url.ParseRequestURI(path)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || parseErr != nil || relative.Host != "" || relative.User != nil || relative.Fragment != "" || !strings.HasPrefix(relative.Path, "/v1/internal/") || strings.Contains(relative.Path, "..") {
		return errors.New("invalid lifecycle endpoint")
	}
	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || !(u.Hostname() == "localhost" || strings.HasSuffix(u.Hostname(), ".svc.cluster.local") || ip != nil && ip.IsLoopback()) {
			return errors.New("unqualified plaintext lifecycle endpoint")
		}
	}
	if len(c.Token) < 32 || strings.ContainsAny(c.Token, " \t\r\n") {
		return errors.New("dedicated lifecycle credential required")
	}
	u.Path = relative.Path
	u.RawQuery = relative.RawQuery
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return err
		}
		if len(raw) > 4<<20 {
			return errors.New("lifecycle request exceeds bound")
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), reader)
	if err != nil {
		return errors.New("invalid lifecycle request")
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	h := http.Client{Timeout: 45 * time.Second}
	if c.HTTP != nil {
		h = *c.HTTP
		if h.Timeout == 0 {
			h.Timeout = 45 * time.Second
		}
	}
	h.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := h.Do(req)
	if err != nil {
		return errors.New("lifecycle endpoint unavailable")
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &HTTPError{Status: resp.StatusCode}
	}
	if !strings.Contains(strings.ToLower(resp.Header.Get("Cache-Control")), "no-store") {
		return errors.New("cacheable lifecycle evidence rejected")
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
	if err != nil || len(raw) > 8<<20 {
		return errors.New("lifecycle response exceeds bound")
	}
	if result == nil {
		return nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err = dec.Decode(result); err != nil {
		return errors.New("invalid lifecycle response")
	}
	if dec.Decode(new(any)) != io.EOF {
		return errors.New("trailing lifecycle response")
	}
	return nil
}
