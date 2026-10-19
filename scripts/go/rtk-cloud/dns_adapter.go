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
	values := deploymentEndpointValues(cfg)
	root := strings.TrimSuffix(values["CLOUD_DNS_ROOT_DOMAIN"], ".")
	ttl, _ := strconv.Atoi(cfg.DNSValues["DNS_RECORD_TTL"])
	records := []dnsRecordSet{}
	for _, host := range lkePublicHTTPSHosts(deploymentPublicHTTPSRoutes(values)) {
		purpose := "public-edge"
		if values["FACTORY_ENROLL_PUBLIC_ENABLED"] == "true" && host == values["FACTORY_ENROLL_DOMAIN"] {
			purpose = "factory-enrollment-mtls"
		}
		records = append(records, dnsRecordSet{Name: host, Type: "A", Values: []string{"runtime:public-edge"}, TTL: ttl, Purpose: purpose})
	}
	for _, host := range lkeCoturnDomains(values) {
		records = append(records, dnsRecordSet{Name: host, Type: "A", Values: []string{"runtime:turn"}, TTL: ttl, Purpose: "turn"})
	}
	return genericDNSPlan{RootDomain: root, Records: records}
}

func deploymentPublicHTTPSRoutes(values map[string]string) []lkePublicHTTPSRoute {
	routes := lkePublicHTTPSBaseRoutes(values)
	if logger := lkeCloudLoggerPlannedRoute(values); logger.Host != "" {
		routes = append(routes, logger)
	}
	return routes
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
	if err := validateDNSLocalState(ctx, adapter.Name()); err != nil {
		return nil, ctx, dnsZone{}, err
	}
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
	if err := writeDNSLocalState(adapterCtx, adapter.Name(), "dns.env", []byte(sortedEnvContents(map[string]string{
		"DNS_ADAPTER": adapter.Name(), "DNS_ROOT_DOMAIN": adapterCtx.RootDomain,
		"DNS_RECORDS_APPLIED": strconv.Itoa(len(records)), "DNS_UPDATED_AT": time.Now().UTC().Format(time.RFC3339),
	}))); err != nil {
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

func validateManagedDNSHostname(name string) error {
	if name == "" || len(name) > 253 {
		return fmt.Errorf("invalid managed DNS hostname %q: total length must be 1 to 253 characters", name)
	}
	for _, label := range strings.Split(name, ".") {
		if len(label) == 0 || len(label) > 63 || !secretEnvironmentPattern.MatchString(label) || strings.HasSuffix(label, "-") {
			return fmt.Errorf("invalid managed DNS hostname %q: use lowercase ASCII labels of 1 to 63 characters with alphanumeric ends", name)
		}
	}
	return nil
}

func writeDNSOwnership(ctx dnsAdapterContext, adapter string, records []dnsRecordSet) error {
	owned, err := readDNSOwnership(ctx, adapter)
	if err != nil {
		return err
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
	body, err := json.MarshalIndent(map[string]any{"adapter": adapter, "root_domain": ctx.RootDomain, "environment": ctx.Values["CLOUD_ENV_NAME"], "records": records, "updated_at": time.Now().UTC().Format(time.RFC3339)}, "", "  ")
	if err != nil {
		return err
	}
	return writeDNSLocalState(ctx, adapter, "ownership.json", append(body, '\n'))
}

func removeOwnedDNSRecords(paths provisionPaths, env map[string]string) error {
	adapterName := firstNonEmpty(env["DNS_ADAPTER"], "godaddy")
	local := dnsContext(paths, env)
	if _, err := readDNSLocalState(local, adapterName, "ownership.json"); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return dnsStateBlocker("original DNS ownership evidence is missing; removal cannot infer ownership")
		}
		return err
	}
	ownership, err := readDNSOwnership(local, adapterName)
	if err != nil {
		return err
	}
	challenges, err := readDNSChallenges(local, adapterName)
	if err != nil {
		return err
	}
	if len(challenges) != 0 {
		return dnsStateBlocker("original ACME challenge cleanup evidence remains; complete original ACME cleanup before DNS removal")
	}
	if len(ownership.Records) == 0 {
		return nil
	}
	adapter, adapterCtx, zone, err := selectedDNSAdapter(paths, env)
	if err != nil {
		return err
	}
	if adapter.Name() == "route53" {
		raw, err := readDNSLocalState(adapterCtx, adapter.Name(), "state.json")
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil {
			var state dnsProviderState
			if decodeEnvironmentJSON(raw, &state) != nil || state.ID != "" && state.ID != zone.ID {
				return dnsStateBlocker("DNS removal hosted-zone identity differs from the original provider state")
			}
		}
	}
	ctx := context.Background()
	// Verify every owned record before the first mutation. A retry may encounter
	// records removed by an earlier partial run, but a replacement is drift.
	for _, owned := range ownership.Records {
		current, err := adapter.GetRecordSet(ctx, adapterCtx, zone, owned.Name, owned.Type)
		if err != nil {
			return err
		}
		if len(current.Values) != 0 && strings.Join(mergeDNSValues(current.Values), "\x00") != strings.Join(mergeDNSValues(owned.Values), "\x00") {
			return fmt.Errorf("DNS drift prevents removal of %s %s", owned.Type, owned.Name)
		}
	}
	for _, owned := range ownership.Records {
		current, err := adapter.GetRecordSet(ctx, adapterCtx, zone, owned.Name, owned.Type)
		if err != nil {
			return err
		}
		if len(current.Values) == 0 {
			fmt.Printf("[ALREADY ABSENT] DNS %s %s\n", owned.Type, owned.Name)
			continue
		}
		if strings.Join(mergeDNSValues(current.Values), "\x00") != strings.Join(mergeDNSValues(owned.Values), "\x00") {
			return fmt.Errorf("DNS drift prevents removal of %s %s", owned.Type, owned.Name)
		}
		if err := adapter.DeleteRecordValues(ctx, adapterCtx, zone, owned); err != nil {
			return err
		}
		remaining, err := adapter.GetRecordSet(ctx, adapterCtx, zone, owned.Name, owned.Type)
		if err != nil {
			return fmt.Errorf("DNS removal verification failed for %s %s: %w", owned.Type, owned.Name, err)
		}
		if len(removeDNSValues(owned.Values, remaining.Values)) != len(owned.Values) {
			return fmt.Errorf("DNS removal verification failed for %s %s: owned values remain", owned.Type, owned.Name)
		}
		fmt.Printf("[DELETED] DNS %s %s\n", owned.Type, owned.Name)
	}
	// Keep original ownership evidence so partial cleanup and later retries can
	// identify exactly the resources this environment had permission to remove.
	return nil
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

type goDaddyDNSAdapter struct {
	client  *http.Client
	key     string
	secret  string
	apiRoot string
}

type goDaddyAPIStatusError struct{ StatusCode int }

func (e *goDaddyAPIStatusError) Error() string {
	return fmt.Sprintf("GoDaddy API request returned HTTP %d", e.StatusCode)
}

func (*goDaddyDNSAdapter) Name() string { return "godaddy" }

func (a *goDaddyDNSAdapter) credentials(ctx dnsAdapterContext) (string, string) {
	if a.key != "" || a.secret != "" {
		return a.key, a.secret
	}
	if rtkCloudTestMode() && os.Getenv("RTK_CLOUD_TEST_ALLOW_PROCESS_CREDENTIALS") == "1" {
		if key, secret := strings.TrimSpace(os.Getenv("GODADDY_KEY")), strings.TrimSpace(os.Getenv("GODADDY_SECRET")); key != "" || secret != "" {
			return key, secret
		}
	}
	environment, err := dnsStateEnvironment(ctx)
	if err != nil {
		return "", ""
	}
	environmentProfile, check := deploymentCredentialValues(defaultDeploymentEnvironmentCredentialFile(environment))
	if !check.Passed {
		return "", ""
	}
	return environmentProfile["GODADDY_KEY"], environmentProfile["GODADDY_SECRET"]
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
	base := a.apiRoot
	if base == "" {
		base = os.Getenv("RTK_CLOUD_GODADDY_API_ROOT")
	}
	if base == "" {
		base = "https://api.godaddy.com"
		if ctx.Values["GODADDY_ENV"] == "ote" {
			base = "https://api.ote-godaddy.com"
		}
	}
	return base + path
}

func validateDNSMutationAccess(ctx context.Context, adapter dnsAdapter, adapterCtx dnsAdapterContext, zone dnsZone, environment string) (err error) {
	environment = strings.ToLower(strings.TrimSpace(environment))
	if environment == "" {
		environment = "deployment"
	}
	name := "_rtk-cloud-credential-preflight-" + environment + "." + zone.Name
	existing, err := adapter.GetRecordSet(ctx, adapterCtx, zone, name, "TXT")
	if err != nil {
		return fmt.Errorf("reserved DNS mutation record could not be read: %w", err)
	}
	if len(existing.Values) != 0 {
		return fmt.Errorf("reserved DNS mutation record already exists: %s", name)
	}
	record := dnsRecordSet{
		Name: name, Type: "TXT", Values: []string{"rtk-cloud-credential-preflight"}, TTL: 600, Purpose: "credential-preflight",
	}
	created := false
	defer func() {
		if !created {
			return
		}
		cleanupCtx, cancel := deploymentCleanupContext()
		defer cancel()
		if cleanupErr := adapter.DeleteRecordValues(cleanupCtx, adapterCtx, zone, record); cleanupErr != nil {
			err = errors.Join(err, fmt.Errorf("reserved DNS mutation record cleanup failed: %w", cleanupErr))
		}
	}()
	// A timed-out PUT may still have reached the provider. Attempt cleanup of
	// our reserved canary even when the mutation response is ambiguous.
	created = true
	if err := adapter.UpsertRecordSet(ctx, adapterCtx, zone, record); err != nil {
		return fmt.Errorf("DNS record mutation permission is required: %w", err)
	}
	got, err := adapter.GetRecordSet(ctx, adapterCtx, zone, name, "TXT")
	if err != nil {
		return fmt.Errorf("DNS mutation verification read failed: %w", err)
	}
	if strings.Join(got.Values, "\x00") != strings.Join(record.Values, "\x00") {
		return errors.New("DNS mutation verification returned unexpected values")
	}
	if err := adapter.DeleteRecordValues(ctx, adapterCtx, zone, record); err != nil {
		return fmt.Errorf("DNS record delete permission is required: %w", err)
	}
	created = false
	got, err = adapter.GetRecordSet(ctx, adapterCtx, zone, name, "TXT")
	if err != nil {
		return fmt.Errorf("DNS mutation cleanup verification failed: %w", err)
	}
	if len(got.Values) != 0 {
		return errors.New("reserved DNS mutation record remained after cleanup")
	}
	return nil
}

func (a *goDaddyDNSAdapter) request(ctx context.Context, adapterCtx dnsAdapterContext, method, path string, body any) (output []byte, resultErr error) {
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
		return nil, errors.New("GoDaddy API request could not be created; check the configured endpoint")
	}
	key, secret := a.credentials(adapterCtx)
	req.Header.Set("Authorization", "sso-key "+key+":"+secret)
	req.Header.Set("Content-Type", "application/json")
	measured := beginDeploymentQuery(ctx, "dns-http")
	defer func() { measured(resultErr) }()
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, sanitizedDeploymentRequestError(err)
	}
	defer resp.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if readErr != nil {
		return nil, sanitizedDeploymentRequestError(readErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &goDaddyAPIStatusError{StatusCode: resp.StatusCode}
	}
	return raw, nil
}

func (a *goDaddyDNSAdapter) GetRecordSet(ctx context.Context, adapterCtx dnsAdapterContext, zone dnsZone, name, recordType string) (dnsRecordSet, error) {
	path := fmt.Sprintf("/v1/domains/%s/records/%s/%s", url.PathEscape(zone.Name), url.PathEscape(recordType), url.PathEscape(dnsRecordName(zone.Name, name)))
	raw, err := a.request(ctx, adapterCtx, http.MethodGet, path, nil)
	if err != nil {
		var status *goDaddyAPIStatusError
		if errors.As(err, &status) && status.StatusCode == http.StatusNotFound {
			return dnsRecordSet{Name: name, Type: recordType}, nil
		}
		return dnsRecordSet{}, err
	}
	var records []struct {
		Data *string `json:"data"`
		TTL  int     `json:"ttl"`
	}
	if json.Unmarshal(raw, &records) != nil || records == nil {
		return dnsRecordSet{}, errors.New("invalid GoDaddy record response")
	}
	out := dnsRecordSet{Name: name, Type: recordType}
	for _, record := range records {
		if record.Data == nil || record.TTL < 0 {
			return dnsRecordSet{}, errors.New("invalid GoDaddy record response")
		}
		out.Values = append(out.Values, *record.Data)
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
	if err != nil || len(existing.Values) == 0 {
		return err
	}
	remaining := removeDNSValues(existing.Values, record.Values)
	if len(remaining) == len(existing.Values) {
		return nil
	}
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
	body, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeDNSLocalState(ctx, adapter, "state.json", append(body, '\n'))
}
