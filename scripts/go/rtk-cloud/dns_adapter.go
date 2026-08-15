package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type dnsRecordSet struct {
	Name    string   `json:"hostname"`
	Type    string   `json:"type"`
	Values  []string `json:"values"`
	TTL     int      `json:"ttl"`
	Purpose string   `json:"purpose"`
}

type dnsZone struct {
	Name string `json:"name"`
	ID   string `json:"-"`
}

type dnsAdapterContext struct {
	RootDomain  string
	RuntimeRoot string
	OperatorEnv string
	Values      map[string]string
}

type dnsAdapter interface {
	Name() string
	Validate(context.Context, dnsAdapterContext) error
	DiscoverZone(context.Context, dnsAdapterContext) (dnsZone, error)
	GetRecordSet(context.Context, dnsAdapterContext, dnsZone, string, string) (dnsRecordSet, error)
	UpsertRecordSet(context.Context, dnsAdapterContext, dnsZone, dnsRecordSet) error
	DeleteRecordValues(context.Context, dnsAdapterContext, dnsZone, dnsRecordSet) error
	PresentDNS01Challenge(context.Context, dnsAdapterContext, dnsZone, string, string) error
	CleanupDNS01Challenge(context.Context, dnsAdapterContext, dnsZone, string, string) error
	CollectEvidence(context.Context, dnsAdapterContext, dnsZone) error
}

type genericDNSPlan struct {
	RootDomain string         `json:"root_domain"`
	Records    []dnsRecordSet `json:"records"`
}

func buildGenericDNSPlan(cfg deploymentConfig) genericDNSPlan {
	root := strings.TrimSuffix(cfg.Values["CLOUD_DNS_ROOT_DOMAIN"], ".")
	stack := cfg.Values["CLOUD_STACK_NAME"]
	base := stack + "." + root
	ttl, _ := strconv.Atoi(cfg.DNSValues["DNS_RECORD_TTL"])
	records := []dnsRecordSet{}
	for _, item := range []struct{ host, target, purpose string }{
		{base, "public-edge", "video-cloud"},
		{"device." + base, "public-edge", "device-mtls"},
		{"certissuer." + base, "public-edge", "certificate-issuer"},
		{"turnregistry." + base, "public-edge", "turn-registry"},
		{"account-manager." + base, "public-edge", "account-manager"},
		{"admin." + base, "public-edge", "cloud-admin"},
		{"frontend." + base, "public-edge", "frontend"},
		{"logger." + base, "public-edge", "cloud-logger"},
		{"turn." + base, "turn", "turn"},
	} {
		records = append(records, dnsRecordSet{Name: item.host, Type: "A", Values: []string{"runtime:" + item.target}, TTL: ttl, Purpose: item.purpose})
	}
	return genericDNSPlan{RootDomain: root, Records: records}
}

func newDNSAdapter(name string) (dnsAdapter, error) {
	switch name {
	case "godaddy":
		return &goDaddyDNSAdapter{client: http.DefaultClient}, nil
	case "route53":
		return &route53DNSAdapter{}, nil
	default:
		return nil, fmt.Errorf("unsupported DNS adapter %q", name)
	}
}

func dnsContext(paths provisionPaths, env map[string]string) dnsAdapterContext {
	values := appendMap(env, nil)
	if values["DNS_RECORD_TTL"] == "" {
		values["DNS_RECORD_TTL"] = "600"
	}
	if values["DNS_PROPAGATION_TIMEOUT_SECONDS"] == "" {
		values["DNS_PROPAGATION_TIMEOUT_SECONDS"] = "900"
	}
	if values["DNS_PROPAGATION_INTERVAL_SECONDS"] == "" {
		values["DNS_PROPAGATION_INTERVAL_SECONDS"] = "10"
	}
	if values["GODADDY_ENV"] == "" {
		values["GODADDY_ENV"] = "prod"
	}
	return dnsAdapterContext{
		RootDomain:  strings.TrimSuffix(env["CLOUD_DNS_ROOT_DOMAIN"], "."),
		RuntimeRoot: paths.EnvRoot,
		OperatorEnv: paths.OperatorEnv,
		Values:      values,
	}
}

func selectedDNSAdapter(paths provisionPaths, env map[string]string) (dnsAdapter, dnsAdapterContext, dnsZone, error) {
	adapter, err := newDNSAdapter(firstNonEmpty(env["DNS_ADAPTER"], "godaddy"))
	if err != nil {
		return nil, dnsAdapterContext{}, dnsZone{}, err
	}
	ctx := dnsContext(paths, env)
	if err := adapter.Validate(context.Background(), ctx); err != nil {
		return nil, ctx, dnsZone{}, err
	}
	zone, err := adapter.DiscoverZone(context.Background(), ctx)
	return adapter, ctx, zone, err
}

func syncDNSRecords(paths provisionPaths, env map[string]string, records []dnsRecordSet) error {
	adapter, adapterCtx, zone, err := selectedDNSAdapter(paths, env)
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, record := range records {
		if err := validateDNSRecord(adapterCtx.RootDomain, record); err != nil {
			return err
		}
		if err := adapter.UpsertRecordSet(ctx, adapterCtx, zone, record); err != nil {
			return fmt.Errorf("DNS %s upsert %s: %w", adapter.Name(), record.Name, err)
		}
	}
	if err := writeDNSOwnership(adapterCtx, adapter.Name(), records); err != nil {
		return err
	}
	if err := adapter.CollectEvidence(ctx, adapterCtx, zone); err != nil {
		return err
	}
	if err := writeSortedEnv(filepath.Join(adapterCtx.RuntimeRoot, "state", "dns.env"), map[string]string{
		"DNS_ADAPTER": adapter.Name(), "DNS_ROOT_DOMAIN": adapterCtx.RootDomain,
		"DNS_RECORDS_APPLIED": strconv.Itoa(len(records)), "DNS_UPDATED_AT": time.Now().UTC().Format(time.RFC3339),
	}, 0o600); err != nil {
		return err
	}
	for _, record := range records {
		if record.Type == "A" && len(record.Values) == 1 {
			if err := waitDNSRecord(record.Name, record.Values[0], adapterCtx.RootDomain, adapterCtx.Values); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateDNSRecord(root string, record dnsRecordSet) error {
	name := strings.TrimSuffix(record.Name, ".")
	if name != root && !strings.HasSuffix(name, "."+root) {
		return fmt.Errorf("DNS record %s is outside root domain %s", name, root)
	}
	switch record.Type {
	case "A", "AAAA", "CNAME", "TXT":
	default:
		return fmt.Errorf("unsupported DNS record type %s", record.Type)
	}
	if record.TTL <= 0 || len(record.Values) == 0 {
		return errors.New("DNS record requires positive TTL and at least one value")
	}
	return nil
}

func writeDNSOwnership(ctx dnsAdapterContext, adapter string, records []dnsRecordSet) error {
	dir := filepath.Join(ctx.RuntimeRoot, "dns", adapter)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	path := filepath.Join(dir, "ownership.json")
	owned := struct {
		Records []dnsRecordSet `json:"records"`
	}{}
	if body, err := os.ReadFile(path); err == nil {
		_ = json.Unmarshal(body, &owned)
	}
	merged := map[string]dnsRecordSet{}
	for _, record := range append(owned.Records, records...) {
		merged[record.Type+"\x00"+record.Name] = record
	}
	records = make([]dnsRecordSet, 0, len(merged))
	for _, record := range merged {
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool {
		if records[i].Name == records[j].Name {
			return records[i].Type < records[j].Type
		}
		return records[i].Name < records[j].Name
	})
	body, err := json.MarshalIndent(map[string]any{"adapter": adapter, "root_domain": ctx.RootDomain, "records": records, "updated_at": time.Now().UTC().Format(time.RFC3339)}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o600)
}

func removeOwnedDNSRecords(paths provisionPaths, env map[string]string) error {
	adapterName := firstNonEmpty(env["DNS_ADAPTER"], "godaddy")
	path := filepath.Join(paths.EnvRoot, "dns", adapterName, "ownership.json")
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var ownership struct {
		Records []dnsRecordSet `json:"records"`
	}
	if err := json.Unmarshal(body, &ownership); err != nil {
		return fmt.Errorf("read DNS ownership: %w", err)
	}
	adapter, adapterCtx, zone, err := selectedDNSAdapter(paths, env)
	if err != nil {
		return err
	}
	ctx := context.Background()
	for _, owned := range ownership.Records {
		current, err := adapter.GetRecordSet(ctx, adapterCtx, zone, owned.Name, owned.Type)
		if err != nil {
			return err
		}
		if strings.Join(mergeDNSValues(current.Values), "\x00") != strings.Join(mergeDNSValues(owned.Values), "\x00") {
			return fmt.Errorf("DNS drift prevents removal of %s %s", owned.Type, owned.Name)
		}
		if err := adapter.DeleteRecordValues(ctx, adapterCtx, zone, owned); err != nil {
			return err
		}
	}
	return os.Remove(path)
}

func waitDNSRecord(domain, value, rootDomain string, values map[string]string) error {
	interval, _ := strconv.Atoi(firstNonEmpty(values["DNS_PROPAGATION_INTERVAL_SECONDS"], "10"))
	maxSeconds, _ := strconv.Atoi(firstNonEmpty(values["DNS_PROPAGATION_TIMEOUT_SECONDS"], "900"))
	nsBytes, _ := exec.Command("dig", "NS", rootDomain, "+short").Output()
	ns := strings.TrimSpace(strings.Split(string(nsBytes), "\n")[0])
	if ns == "" {
		return fmt.Errorf("could not resolve authoritative NS for %s", rootDomain)
	}
	for attempt := 1; attempt <= (maxSeconds+interval-1)/interval; attempt++ {
		if digShort("8.8.8.8", domain) == value && digShort(ns, domain) == value {
			return nil
		}
		time.Sleep(time.Duration(interval) * time.Second)
	}
	return fmt.Errorf("DNS did not converge: %s expected=%s", domain, value)
}

func dnsRecordName(root, fqdn string) string {
	name := strings.TrimSuffix(fqdn, ".")
	if name == root {
		return "@"
	}
	return strings.TrimSuffix(name, "."+root)
}

func mergeDNSValues(existing []string, add ...string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range append(existing, add...) {
		value = strings.TrimSpace(value)
		if value != "" && !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	sort.Strings(out)
	return out
}

func removeDNSValues(existing, remove []string) []string {
	deleted := map[string]bool{}
	for _, value := range remove {
		deleted[value] = true
	}
	out := []string{}
	for _, value := range existing {
		if !deleted[value] {
			out = append(out, value)
		}
	}
	return out
}

type goDaddyDNSAdapter struct{ client *http.Client }

func (*goDaddyDNSAdapter) Name() string { return "godaddy" }

func (a *goDaddyDNSAdapter) credentials(ctx dnsAdapterContext) (string, string) {
	environment := firstNonEmpty(envFileValue(filepath.Join(ctx.RuntimeRoot, "env", "stack.env"), "CLOUD_ENV_NAME"), "staging")
	environmentProfile, _ := readEnvFile(defaultDeploymentEnvironmentCredentialFile(environment))
	sharedProfile, _ := readEnvFile(defaultDeploymentSharedCredentialFile())
	return firstNonEmpty(os.Getenv("GODADDY_KEY"), environmentProfile["GODADDY_KEY"], sharedProfile["GODADDY_KEY"]),
		firstNonEmpty(os.Getenv("GODADDY_SECRET"), environmentProfile["GODADDY_SECRET"], sharedProfile["GODADDY_SECRET"])
}

func (a *goDaddyDNSAdapter) Validate(_ context.Context, ctx dnsAdapterContext) error {
	key, secret := a.credentials(ctx)
	if key == "" || secret == "" {
		return errors.New("GoDaddy DNS credentials missing: GODADDY_KEY and GODADDY_SECRET are required")
	}
	ttl, _ := strconv.Atoi(ctx.Values["DNS_RECORD_TTL"])
	if ttl < 600 {
		return errors.New("GoDaddy DNS record TTL must be at least 600")
	}
	return nil
}

func (*goDaddyDNSAdapter) DiscoverZone(_ context.Context, ctx dnsAdapterContext) (dnsZone, error) {
	if ctx.RootDomain == "" {
		return dnsZone{}, errors.New("CLOUD_DNS_ROOT_DOMAIN is required")
	}
	return dnsZone{Name: ctx.RootDomain}, nil
}

func (a *goDaddyDNSAdapter) endpoint(ctx dnsAdapterContext, path string) string {
	base := os.Getenv("RTK_CLOUD_GODADDY_API_ROOT")
	if base == "" {
		base = "https://api.godaddy.com"
		if ctx.Values["GODADDY_ENV"] == "ote" {
			base = "https://api.ote-godaddy.com"
		}
	}
	return base + path
}

func (a *goDaddyDNSAdapter) request(ctx context.Context, adapterCtx dnsAdapterContext, method, path string, body any) ([]byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, a.endpoint(adapterCtx, path), reader)
	if err != nil {
		return nil, err
	}
	key, secret := a.credentials(adapterCtx)
	req.Header.Set("Authorization", "sso-key "+key+":"+secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("GoDaddy API %s %s returned %s", method, path, resp.Status)
	}
	return raw, nil
}

func (a *goDaddyDNSAdapter) GetRecordSet(ctx context.Context, adapterCtx dnsAdapterContext, zone dnsZone, name, recordType string) (dnsRecordSet, error) {
	path := fmt.Sprintf("/v1/domains/%s/records/%s/%s", url.PathEscape(zone.Name), url.PathEscape(recordType), url.PathEscape(dnsRecordName(zone.Name, name)))
	raw, err := a.request(ctx, adapterCtx, http.MethodGet, path, nil)
	if err != nil {
		return dnsRecordSet{}, err
	}
	var records []struct {
		Data string `json:"data"`
		TTL  int    `json:"ttl"`
	}
	if len(raw) > 0 && json.Unmarshal(raw, &records) != nil {
		return dnsRecordSet{}, errors.New("invalid GoDaddy record response")
	}
	out := dnsRecordSet{Name: name, Type: recordType}
	for _, record := range records {
		out.Values = append(out.Values, record.Data)
		out.TTL = record.TTL
	}
	return out, nil
}

func (a *goDaddyDNSAdapter) UpsertRecordSet(ctx context.Context, adapterCtx dnsAdapterContext, zone dnsZone, record dnsRecordSet) error {
	payload := []map[string]any{}
	for _, value := range record.Values {
		payload = append(payload, map[string]any{"data": value, "ttl": record.TTL})
	}
	path := fmt.Sprintf("/v1/domains/%s/records/%s/%s", url.PathEscape(zone.Name), url.PathEscape(record.Type), url.PathEscape(dnsRecordName(zone.Name, record.Name)))
	_, err := a.request(ctx, adapterCtx, http.MethodPut, path, payload)
	return err
}

func (a *goDaddyDNSAdapter) DeleteRecordValues(ctx context.Context, adapterCtx dnsAdapterContext, zone dnsZone, record dnsRecordSet) error {
	existing, err := a.GetRecordSet(ctx, adapterCtx, zone, record.Name, record.Type)
	if err != nil {
		return err
	}
	remaining := removeDNSValues(existing.Values, record.Values)
	path := fmt.Sprintf("/v1/domains/%s/records/%s/%s", url.PathEscape(zone.Name), url.PathEscape(record.Type), url.PathEscape(dnsRecordName(zone.Name, record.Name)))
	if len(remaining) == 0 {
		_, err = a.request(ctx, adapterCtx, http.MethodDelete, path, nil)
		return err
	}
	existing.Values = remaining
	if existing.TTL == 0 {
		existing.TTL = record.TTL
	}
	return a.UpsertRecordSet(ctx, adapterCtx, zone, existing)
}

func dns01RecordName(root, domain string) (string, error) {
	domain = strings.TrimSuffix(domain, ".")
	if domain != root && !strings.HasSuffix(domain, "."+root) {
		return "", fmt.Errorf("ACME domain %s is outside zone %s", domain, root)
	}
	return "_acme-challenge." + domain, nil
}

func (a *goDaddyDNSAdapter) PresentDNS01Challenge(ctx context.Context, adapterCtx dnsAdapterContext, zone dnsZone, domain, value string) error {
	name, err := dns01RecordName(zone.Name, domain)
	if err != nil {
		return err
	}
	existing, err := a.GetRecordSet(ctx, adapterCtx, zone, name, "TXT")
	if err != nil {
		return err
	}
	ttl, _ := strconv.Atoi(adapterCtx.Values["DNS_RECORD_TTL"])
	return a.UpsertRecordSet(ctx, adapterCtx, zone, dnsRecordSet{Name: name, Type: "TXT", Values: mergeDNSValues(existing.Values, value), TTL: ttl, Purpose: "acme-dns01"})
}

func (a *goDaddyDNSAdapter) CleanupDNS01Challenge(ctx context.Context, adapterCtx dnsAdapterContext, zone dnsZone, domain, value string) error {
	name, err := dns01RecordName(zone.Name, domain)
	if err != nil {
		return err
	}
	ttl, _ := strconv.Atoi(adapterCtx.Values["DNS_RECORD_TTL"])
	return a.DeleteRecordValues(ctx, adapterCtx, zone, dnsRecordSet{Name: name, Type: "TXT", Values: []string{value}, TTL: ttl})
}

func (a *goDaddyDNSAdapter) CollectEvidence(_ context.Context, ctx dnsAdapterContext, zone dnsZone) error {
	return writeDNSProviderState(ctx, a.Name(), map[string]any{"adapter": a.Name(), "zone_name": zone.Name})
}

func writeDNSProviderState(ctx dnsAdapterContext, adapter string, state map[string]any) error {
	dir := filepath.Join(ctx.RuntimeRoot, "dns", adapter)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "state.json"), append(body, '\n'), 0o600)
}
