package home100k

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

type ShadowHTTPClient struct {
	BaseURL    string
	HTTPClient *http.Client
}

func NewShadowHTTPClient(baseURL string, client *http.Client) *ShadowHTTPClient {
	if client == nil {
		client = http.DefaultClient
	}
	return &ShadowHTTPClient{
		BaseURL:    strings.TrimRight(baseURL, "/"),
		HTTPClient: client,
	}
}

func (c *ShadowHTTPClient) Get(ctx context.Context, deviceID string, token string, clientToken string) (ShadowDocument, error) {
	target := fmt.Sprintf("%s/api/devices/%s/shadow", c.BaseURL, url.PathEscape(deviceID))
	if strings.TrimSpace(clientToken) != "" {
		target += "?clientToken=" + url.QueryEscape(clientToken)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return ShadowDocument{}, err
	}
	setBearer(req, token)
	return c.doShadow(req)
}

func (c *ShadowHTTPClient) UpdateDesired(ctx context.Context, deviceID string, token string, desired map[string]any, clientToken string, version int64) (ShadowDocument, error) {
	return c.update(ctx, deviceID, token, map[string]any{"desired": desired}, clientToken, version)
}

func (c *ShadowHTTPClient) UpdateReported(ctx context.Context, deviceID string, token string, reported map[string]any, clientToken string, version int64) (ShadowDocument, error) {
	return c.update(ctx, deviceID, token, map[string]any{"reported": reported}, clientToken, version)
}

func (c *ShadowHTTPClient) update(ctx context.Context, deviceID string, token string, state map[string]any, clientToken string, version int64) (ShadowDocument, error) {
	body := map[string]any{"state": state}
	if strings.TrimSpace(clientToken) != "" {
		body["clientToken"] = clientToken
	}
	if version > 0 {
		body["version"] = version
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return ShadowDocument{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, fmt.Sprintf("%s/api/devices/%s/shadow", c.BaseURL, url.PathEscape(deviceID)), bytes.NewReader(raw))
	if err != nil {
		return ShadowDocument{}, err
	}
	setBearer(req, token)
	req.Header.Set("Content-Type", "application/json")
	return c.doShadow(req)
}

func (c *ShadowHTTPClient) doShadow(req *http.Request) (ShadowDocument, error) {
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return ShadowDocument{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return ShadowDocument{}, fmt.Errorf("shadow HTTP %s %s failed: HTTP %d", req.Method, req.URL.Path, resp.StatusCode)
	}
	var body struct {
		State struct {
			Desired  map[string]any `json:"desired"`
			Reported map[string]any `json:"reported"`
			Delta    map[string]any `json:"delta"`
		} `json:"state"`
		Version     int64  `json:"version"`
		ClientToken string `json:"clientToken"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		return ShadowDocument{}, err
	}
	return ShadowDocument{
		Desired:     nonNilMap(body.State.Desired),
		Reported:    nonNilMap(body.State.Reported),
		Delta:       nonNilMap(body.State.Delta),
		Version:     body.Version,
		ClientToken: body.ClientToken,
	}, nil
}

func setBearer(req *http.Request, token string) {
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(token))
	req.Header.Set("Accept", "application/json")
}

func nonNilMap(value map[string]any) map[string]any {
	if value == nil {
		return map[string]any{}
	}
	return value
}
