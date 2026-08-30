package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
)

func TestGoDaddyDNSAdapterCleanupPreservesOtherTXTValues(t *testing.T) {
	values := []string{"existing-value", "remove-value"}
	var putValues []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			payload := []map[string]any{}
			for _, value := range values {
				payload = append(payload, map[string]any{"data": value, "ttl": 600})
			}
			_ = json.NewEncoder(w).Encode(payload)
		case http.MethodPut:
			var payload []struct {
				Data string `json:"data"`
			}
			_ = json.NewDecoder(r.Body).Decode(&payload)
			for _, record := range payload {
				putValues = append(putValues, record.Data)
			}
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected method %s", r.Method)
		}
	}))
	defer server.Close()
	t.Setenv("RTK_CLOUD_GODADDY_API_ROOT", server.URL)
	t.Setenv("GODADDY_KEY", "key")
	t.Setenv("GODADDY_SECRET", "secret")
	adapter := &goDaddyDNSAdapter{client: server.Client()}
	ctx := dnsAdapterContext{RootDomain: "example.test", Values: map[string]string{"GODADDY_ENV": "prod", "DNS_RECORD_TTL": "600"}}
	zone := dnsZone{Name: "example.test"}
	if err := adapter.CleanupDNS01Challenge(context.Background(), ctx, zone, "api.example.test", "remove-value"); err != nil {
		t.Fatal(err)
	}
	if len(putValues) != 1 || putValues[0] != "existing-value" {
		t.Fatalf("cleanup wrote %#v", putValues)
	}
}

func TestGoDaddyDNSAdapterCredentialsUseEnvironmentStore(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GODADDY_KEY", "")
	t.Setenv("GODADDY_SECRET", "")
	store, err := newSecretStore("", "staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ensureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := store.write("operator/env/GODADDY_KEY", []byte("profile-key\n"), true); err != nil {
		t.Fatal(err)
	}
	if err := store.write("operator/env/GODADDY_SECRET", []byte("profile-secret\n"), true); err != nil {
		t.Fatal(err)
	}

	adapter := &goDaddyDNSAdapter{}
	ctx := dnsAdapterContext{
		OperatorEnv: filepath.Join(t.TempDir(), "missing-operator.env"),
		Values:      map[string]string{"DNS_RECORD_TTL": "600"},
	}
	key, secret := adapter.credentials(ctx)
	if key != "profile-key" || secret != "profile-secret" {
		t.Fatalf("credentials did not use shared profile")
	}
	if err := adapter.Validate(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
}

func TestGoDaddyDNSAdapterCredentialsNeverReadHomeEnv(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GODADDY_KEY", "")
	t.Setenv("GODADDY_SECRET", "")
	writeTestFile(t, filepath.Join(home, ".env"), "GODADDY_KEY=legacy-key\nGODADDY_SECRET=legacy-secret\n")
	key, secret := (&goDaddyDNSAdapter{}).credentials(dnsAdapterContext{})
	if key != "" || secret != "" {
		t.Fatalf("legacy ~/.env must be ignored, got (%q, %q)", key, secret)
	}
}

func TestGoDaddyDNSAdapterIgnoresProcessAndLegacyOperatorFiles(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("GODADDY_KEY", "process-key")
	t.Setenv("GODADDY_SECRET", "process-secret")
	store, err := newSecretStore("", "staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.ensureLayout(); err != nil {
		t.Fatal(err)
	}
	if err := store.write("operator/env/GODADDY_KEY", []byte("profile-key\n"), true); err != nil {
		t.Fatal(err)
	}
	if err := store.write("operator/env/GODADDY_SECRET", []byte("profile-secret\n"), true); err != nil {
		t.Fatal(err)
	}
	operatorEnv := filepath.Join(t.TempDir(), "operator.env")
	writeTestFile(t, operatorEnv, "GODADDY_KEY=operator-key\nGODADDY_SECRET=operator-secret\n")

	adapter := &goDaddyDNSAdapter{}
	key, secret := adapter.credentials(dnsAdapterContext{OperatorEnv: operatorEnv})
	if key != "profile-key" || secret != "profile-secret" {
		t.Fatalf("canonical environment store must be the only source")
	}
}

type fakeRoute53API struct {
	zones   []types.HostedZone
	records []types.ResourceRecordSet
	changes []types.Change
}

func (f *fakeRoute53API) ListHostedZonesByName(context.Context, *route53.ListHostedZonesByNameInput, ...func(*route53.Options)) (*route53.ListHostedZonesByNameOutput, error) {
	return &route53.ListHostedZonesByNameOutput{HostedZones: f.zones}, nil
}
func (f *fakeRoute53API) ListResourceRecordSets(context.Context, *route53.ListResourceRecordSetsInput, ...func(*route53.Options)) (*route53.ListResourceRecordSetsOutput, error) {
	return &route53.ListResourceRecordSetsOutput{ResourceRecordSets: f.records}, nil
}
func (f *fakeRoute53API) ChangeResourceRecordSets(_ context.Context, input *route53.ChangeResourceRecordSetsInput, _ ...func(*route53.Options)) (*route53.ChangeResourceRecordSetsOutput, error) {
	f.changes = append(f.changes, input.ChangeBatch.Changes...)
	now := time.Now()
	return &route53.ChangeResourceRecordSetsOutput{ChangeInfo: &types.ChangeInfo{Id: aws.String("change-1"), Status: types.ChangeStatusPending, SubmittedAt: &now}}, nil
}
func (*fakeRoute53API) GetChange(context.Context, *route53.GetChangeInput, ...func(*route53.Options)) (*route53.GetChangeOutput, error) {
	now := time.Now()
	return &route53.GetChangeOutput{ChangeInfo: &types.ChangeInfo{Id: aws.String("change-1"), Status: types.ChangeStatusInsync, SubmittedAt: &now}}, nil
}

func publicZone(name, id string, private bool) types.HostedZone {
	return types.HostedZone{Name: aws.String(name), Id: aws.String(id), CallerReference: aws.String("test"), Config: &types.HostedZoneConfig{PrivateZone: private}}
}

func TestRoute53DiscoverZoneRequiresUniquePublicMatch(t *testing.T) {
	for name, zones := range map[string][]types.HostedZone{
		"unique":   {publicZone("example.test.", "z1", false), publicZone("example.test.", "private", true)},
		"none":     {publicZone("example.test.", "private", true)},
		"multiple": {publicZone("example.test.", "z1", false), publicZone("example.test.", "z2", false)},
	} {
		t.Run(name, func(t *testing.T) {
			adapter := &route53DNSAdapter{client: &fakeRoute53API{zones: zones}}
			zone, err := adapter.DiscoverZone(context.Background(), dnsAdapterContext{RootDomain: "example.test"})
			if name == "unique" {
				if err != nil || zone.ID != "z1" {
					t.Fatalf("zone=%#v err=%v", zone, err)
				}
			} else if err == nil {
				t.Fatalf("expected discovery failure, zone=%#v", zone)
			}
		})
	}
}

func TestRoute53DNS01UsesQuotedMultiValueRecordSet(t *testing.T) {
	fake := &fakeRoute53API{
		zones:   []types.HostedZone{publicZone("example.test.", "z1", false)},
		records: []types.ResourceRecordSet{{Name: aws.String("_acme-challenge.api.example.test."), Type: types.RRTypeTxt, TTL: aws.Int64(600), ResourceRecords: []types.ResourceRecord{{Value: aws.String(`"existing"`)}}}},
	}
	adapter := &route53DNSAdapter{client: fake}
	ctx := dnsAdapterContext{RootDomain: "example.test", Values: map[string]string{"DNS_RECORD_TTL": "600", "DNS_PROPAGATION_TIMEOUT_SECONDS": "1", "DNS_PROPAGATION_INTERVAL_SECONDS": "1"}}
	if err := adapter.PresentDNS01Challenge(context.Background(), ctx, dnsZone{Name: "example.test", ID: "z1"}, "api.example.test", "new-value"); err != nil {
		t.Fatal(err)
	}
	if len(fake.changes) != 1 || fake.changes[0].Action != types.ChangeActionUpsert {
		t.Fatalf("changes=%#v", fake.changes)
	}
	rr := fake.changes[0].ResourceRecordSet
	got := []string{aws.ToString(rr.ResourceRecords[0].Value), aws.ToString(rr.ResourceRecords[1].Value)}
	if strings.Join(got, ",") != `"existing","new-value"` {
		t.Fatalf("TXT values=%#v", got)
	}
}

func TestRoute53CleanupRemovesOnlyRequestedTXTValuesAndCollectsEvidence(t *testing.T) {
	zone := dnsZone{Name: "example.test", ID: "z1"}
	ctx := dnsAdapterContext{
		RootDomain: "example.test", RuntimeRoot: t.TempDir(),
		Values: map[string]string{"DNS_RECORD_TTL": "600", "DNS_PROPAGATION_TIMEOUT_SECONDS": "1", "DNS_PROPAGATION_INTERVAL_SECONDS": "1"},
	}
	fake := &fakeRoute53API{records: []types.ResourceRecordSet{{
		Name: aws.String("_acme-challenge.api.example.test."), Type: types.RRTypeTxt, TTL: aws.Int64(600),
		ResourceRecords: []types.ResourceRecord{{Value: aws.String(`"keep"`)}, {Value: aws.String(`"remove"`)}},
	}}}
	adapter := &route53DNSAdapter{client: fake}
	if err := adapter.CleanupDNS01Challenge(context.Background(), ctx, zone, "api.example.test", "remove"); err != nil {
		t.Fatal(err)
	}
	if len(fake.changes) != 1 || fake.changes[0].Action != types.ChangeActionUpsert {
		t.Fatalf("changes = %#v", fake.changes)
	}
	values := fake.changes[0].ResourceRecordSet.ResourceRecords
	if len(values) != 1 || aws.ToString(values[0].Value) != `"keep"` {
		t.Fatalf("remaining values = %#v", values)
	}

	fake.records[0].ResourceRecords = []types.ResourceRecord{{Value: aws.String(`"remove"`)}}
	if err := adapter.DeleteRecordValues(context.Background(), ctx, zone, dnsRecordSet{
		Name: "_acme-challenge.api.example.test", Type: "TXT", Values: []string{"remove"}, TTL: 600,
	}); err != nil {
		t.Fatal(err)
	}
	if len(fake.changes) != 2 || fake.changes[1].Action != types.ChangeActionDelete {
		t.Fatalf("changes = %#v", fake.changes)
	}
	if err := adapter.CollectEvidence(context.Background(), ctx, zone); err != nil {
		t.Fatal(err)
	}
	state, err := os.ReadFile(filepath.Join(ctx.RuntimeRoot, "dns", "route53", "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(state), `"hosted_zone_id": "z1"`) {
		t.Fatalf("provider state = %s", state)
	}
}

func TestDeploymentDNSPlanIsIdenticalAcrossDNSAdapters(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	writeTestFile(t, filepath.Join(workspace, "cloud_deploy", "dns_adapters", "route53", "defaults.env"), "DNS_RECORD_TTL=600\nDNS_PROPAGATION_TIMEOUT_SECONDS=900\nDNS_PROPAGATION_INTERVAL_SECONDS=10\nROUTE53_CONTROL_PLANE_REGION=us-east-1\n")
	writeTestFile(t, filepath.Join(workspace, "cloud_deploy", "dns_adapters", "route53", "schema.env"), "DNS_ADAPTER_NAME=route53\n")
	godaddy, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(workspace, "cloud_env", "staging", "deployment.env"), "DEPLOYMENT_ARCHITECTURE=kubernetes\nDEPLOYMENT_ADAPTER=lke\nDNS_ADAPTER=route53\n")
	route, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	left, _ := json.Marshal(buildGenericDNSPlan(godaddy))
	right, _ := json.Marshal(buildGenericDNSPlan(route))
	if string(left) != string(right) {
		t.Fatalf("DNS plans differ:\n%s\n%s", left, right)
	}
}

func TestCertbotDNSHookContainsNoVendorImplementation(t *testing.T) {
	script := certbotDNSHookScript("/usr/local/bin/rtk-cloud", "/runtime", "/operator.env", "present")
	for _, forbidden := range []string{"GODADDY_", "api.godaddy.com", "route53", "curl"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("hook contains %s:\n%s", forbidden, script)
		}
	}
	if !strings.Contains(script, "dns-hook 'present'") {
		t.Fatalf("hook does not call shared command:\n%s", script)
	}
}

func TestResolvedDNSPlanContainsNoCredentialsOrProviderIDs(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := materializeDeploymentRuntime(cfg); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(cfg.RuntimeRoot, "resolved", "dns-plan.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"GODADDY_KEY", "GODADDY_SECRET", "AWS_ACCESS_KEY_ID", "hosted_zone_id", "change_id"} {
		if strings.Contains(string(body), forbidden) {
			t.Fatalf("resolved DNS plan contains %s", forbidden)
		}
	}
}

func TestRemoveOwnedDNSRecordsStopsOnDrift(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_ = json.NewEncoder(w).Encode([]map[string]any{{"data": "198.51.100.99", "ttl": 600}})
			return
		}
		t.Fatalf("drifted record must not be mutated: %s", r.Method)
	}))
	defer server.Close()
	t.Setenv("RTK_CLOUD_GODADDY_API_ROOT", server.URL)
	store := makeIsolatedTestSecretStore(t, "staging")
	if err := store.write("operator/env/GODADDY_KEY", []byte("key\n"), true); err != nil {
		t.Fatal(err)
	}
	if err := store.write("operator/env/GODADDY_SECRET", []byte("secret\n"), true); err != nil {
		t.Fatal(err)
	}
	runtimeRoot := t.TempDir()
	dir := filepath.Join(runtimeRoot, "dns", "godaddy")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"records": []dnsRecordSet{{Name: "api.example.test", Type: "A", Values: []string{"198.51.100.10"}, TTL: 600}}})
	if err := os.WriteFile(filepath.Join(dir, "ownership.json"), body, 0o600); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"DNS_ADAPTER": "godaddy", "CLOUD_DNS_ROOT_DOMAIN": "example.test", "DNS_RECORD_TTL": "600", "GODADDY_ENV": "prod"}
	err := removeOwnedDNSRecords(provisionPaths{EnvRoot: runtimeRoot}, env)
	if err == nil || !strings.Contains(err.Error(), "DNS drift prevents removal") {
		t.Fatalf("got %v", err)
	}
}

func TestRoute53LiveSmoke(t *testing.T) {
	root := strings.TrimSpace(os.Getenv("RTK_CLOUD_ROUTE53_LIVE_ROOT_DOMAIN"))
	if root == "" {
		t.Skip("set RTK_CLOUD_ROUTE53_LIVE_ROOT_DOMAIN to an approved disposable public hosted zone")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	adapterCtx := dnsAdapterContext{RootDomain: root, Values: map[string]string{
		"ROUTE53_CONTROL_PLANE_REGION": "us-east-1", "DNS_RECORD_TTL": "60",
		"DNS_PROPAGATION_TIMEOUT_SECONDS": "300", "DNS_PROPAGATION_INTERVAL_SECONDS": "5",
	}}
	adapter := &route53DNSAdapter{}
	if err := adapter.Validate(ctx, adapterCtx); err != nil {
		t.Fatal(err)
	}
	zone, err := adapter.DiscoverZone(ctx, adapterCtx)
	if err != nil {
		t.Fatal(err)
	}
	record := dnsRecordSet{Name: "_rtk-dns-adapter-smoke." + root, Type: "TXT", Values: []string{"rtk-dns-adapter-smoke"}, TTL: 60, Purpose: "live-smoke"}
	if err := adapter.UpsertRecordSet(ctx, adapterCtx, zone, record); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Minute)
		defer cleanupCancel()
		if err := adapter.DeleteRecordValues(cleanupCtx, adapterCtx, zone, record); err != nil {
			t.Errorf("Route53 smoke cleanup: %v", err)
		}
	})
	got, err := adapter.GetRecordSet(ctx, adapterCtx, zone, record.Name, record.Type)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(got.Values, ",") != "rtk-dns-adapter-smoke" {
		t.Fatalf("record values=%v", got.Values)
	}
}
