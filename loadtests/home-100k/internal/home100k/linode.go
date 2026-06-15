package home100k

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type LinodeClient struct {
	Endpoint   string
	Token      string
	HTTPClient *http.Client
}

type LinodeVMConfig struct {
	Type           string
	Image          string
	RootPass       string
	AuthorizedKeys []string
}

type LinodeVM struct {
	ID         int    `json:"id"`
	Label      string `json:"label"`
	PublicIPv4 string `json:"public_ipv4,omitempty"`
}

func NewLinodeClient(endpoint string, token string) *LinodeClient {
	return &LinodeClient{
		Endpoint:   strings.TrimRight(endpoint, "/"),
		Token:      strings.TrimSpace(token),
		HTTPClient: http.DefaultClient,
	}
}

func (c *LinodeClient) ProvisionVM(ctx context.Context, action LifecycleAction, cfg LinodeVMConfig) (LinodeVM, error) {
	if c == nil || c.HTTPClient == nil {
		return LinodeVM{}, errors.New("linode client is not configured")
	}
	if c.Token == "" {
		return LinodeVM{}, errors.New("Linode token is required")
	}
	payload := map[string]any{
		"region":          action.Region,
		"type":            firstNonEmpty(cfg.Type, "g6-standard-2"),
		"image":           firstNonEmpty(cfg.Image, "linode/ubuntu24.04"),
		"label":           action.Label,
		"tags":            action.Tags,
		"authorized_keys": cfg.AuthorizedKeys,
	}
	if strings.TrimSpace(cfg.RootPass) != "" {
		payload["root_pass"] = cfg.RootPass
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return LinodeVM{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint+"/linode/instances", bytes.NewReader(body))
	if err != nil {
		return LinodeVM{}, err
	}
	c.setHeaders(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return LinodeVM{}, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return LinodeVM{}, fmt.Errorf("Linode provision failed: HTTP %d: %s", resp.StatusCode, readLinodeErrorBody(resp.Body))
	}
	var parsed struct {
		ID    int      `json:"id"`
		Label string   `json:"label"`
		IPv4  []string `json:"ipv4"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return LinodeVM{}, err
	}
	vm := LinodeVM{ID: parsed.ID, Label: parsed.Label}
	if len(parsed.IPv4) > 0 {
		vm.PublicIPv4 = parsed.IPv4[0]
	}
	return vm, nil
}

func readLinodeErrorBody(body io.Reader) string {
	raw, err := io.ReadAll(io.LimitReader(body, 4096))
	if err != nil {
		return "unable to read error body"
	}
	detail := strings.TrimSpace(string(raw))
	if detail == "" {
		return "empty error body"
	}
	return redact(detail)
}

func (c *LinodeClient) DestroyVM(ctx context.Context, id int) error {
	if c == nil || c.HTTPClient == nil {
		return errors.New("linode client is not configured")
	}
	if c.Token == "" {
		return errors.New("Linode token is required")
	}
	if id <= 0 {
		return errors.New("Linode instance id must be positive")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodDelete, fmt.Sprintf("%s/linode/instances/%d", c.Endpoint, id), nil)
	if err != nil {
		return err
	}
	c.setHeaders(req)
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Linode destroy failed: HTTP %d", resp.StatusCode)
	}
	return nil
}

func (c *LinodeClient) ListVMs(ctx context.Context, tags []string) ([]LinodeVM, error) {
	if c == nil || c.HTTPClient == nil {
		return nil, errors.New("linode client is not configured")
	}
	if c.Token == "" {
		return nil, errors.New("Linode token is required")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Endpoint+"/linode/instances", nil)
	if err != nil {
		return nil, err
	}
	c.setHeaders(req)
	if len(tags) > 0 {
		filterParts := make([]map[string]string, 0, len(tags))
		for _, tag := range tags {
			if strings.TrimSpace(tag) != "" {
				filterParts = append(filterParts, map[string]string{"tags": tag})
			}
		}
		filter, err := json.Marshal(map[string]any{"+and": filterParts})
		if err != nil {
			return nil, err
		}
		req.Header.Set("X-Filter", string(filter))
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("Linode list failed: HTTP %d", resp.StatusCode)
	}
	var parsed struct {
		Data []struct {
			ID    int      `json:"id"`
			Label string   `json:"label"`
			IPv4  []string `json:"ipv4"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, err
	}
	vms := make([]LinodeVM, 0, len(parsed.Data))
	for _, item := range parsed.Data {
		vm := LinodeVM{ID: item.ID, Label: item.Label}
		if len(item.IPv4) > 0 {
			vm.PublicIPv4 = item.IPv4[0]
		}
		vms = append(vms, vm)
	}
	return vms, nil
}

func (c *LinodeClient) setHeaders(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
}
