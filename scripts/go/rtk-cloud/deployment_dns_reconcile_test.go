package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// The recovery adapters expose only authenticated reads. Any attempted cloud
// mutation fails the test, including during evidence publication and validation.
type dnsReconcileReadOnlyAdapter struct {
	t       *testing.T
	zone    dnsZone
	records map[string]dnsRecordSet
	calls   []string
	err     error
}

func (a *dnsReconcileReadOnlyAdapter) Name() string { return "godaddy" }
func (a *dnsReconcileReadOnlyAdapter) Validate(context.Context, dnsAdapterContext) error {
	return a.err
}
func (a *dnsReconcileReadOnlyAdapter) DiscoverZone(context.Context, dnsAdapterContext) (dnsZone, error) {
	return a.zone, a.err
}
func (a *dnsReconcileReadOnlyAdapter) GetRecordSet(_ context.Context, _ dnsAdapterContext, _ dnsZone, name, kind string) (dnsRecordSet, error) {
	a.calls = append(a.calls, kind+":"+name)
	record := a.records[kind+":"+name]
	record.Values = slices.Clone(record.Values)
	return record, a.err
}
func (a *dnsReconcileReadOnlyAdapter) mutation() error {
	a.t.Error("DNS reconciliation attempted a cloud mutation")
	return errors.New("forbidden cloud mutation")
}
func (a *dnsReconcileReadOnlyAdapter) UpsertRecordSet(context.Context, dnsAdapterContext, dnsZone, dnsRecordSet) error {
	return a.mutation()
}
func (a *dnsReconcileReadOnlyAdapter) DeleteRecordValues(context.Context, dnsAdapterContext, dnsZone, dnsRecordSet) error {
	return a.mutation()
}
func (a *dnsReconcileReadOnlyAdapter) PresentDNS01Challenge(context.Context, dnsAdapterContext, dnsZone, string, string) error {
	return a.mutation()
}
func (a *dnsReconcileReadOnlyAdapter) CleanupDNS01Challenge(context.Context, dnsAdapterContext, dnsZone, string, string) error {
	return a.mutation()
}
func (a *dnsReconcileReadOnlyAdapter) CollectEvidence(context.Context, dnsAdapterContext, dnsZone) error {
	return a.mutation()
}

type dnsReconcileNativeInventory struct {
	t         *testing.T
	clusters  []lkeCluster
	instances []linodeInstance
	calls     []string
	denied    map[string]bool
}

func (n *dnsReconcileNativeInventory) read(ctx context.Context, token, path string, _ map[string]any) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if token != "isolated-dns-reconciliation-token" {
		n.t.Fatal("DNS recovery did not use the selected isolated credential store")
	}
	n.calls = append(n.calls, path)
	endpoint, _, _ := strings.Cut(path, "?")
	if n.denied[endpoint] {
		return nil, errors.New("isolated exact-ID read denied")
	}
	page := func(data any, count int) ([]byte, error) {
		return json.Marshal(map[string]any{"page": 1, "pages": 1, "results": count, "data": data})
	}
	switch endpoint {
	case "/lke/clusters":
		return page(n.clusters, len(n.clusters))
	case "/linode/instances":
		return page(n.instances, len(n.instances))
	}
	if raw, ok := strings.CutPrefix(endpoint, "/linode/instances/"); ok {
		id, _ := strconv.Atoi(raw)
		for _, item := range n.instances {
			if item.ID == id {
				return json.Marshal(item)
			}
		}
		return nil, errRemovalRecoveryNativeNotFound
	}
	if raw, ok := strings.CutPrefix(endpoint, "/lke/clusters/"); ok {
		id, _ := strconv.Atoi(raw)
		for _, item := range n.clusters {
			if item.ID == id {
				return json.Marshal(item)
			}
		}
		return nil, errRemovalRecoveryNativeNotFound
	}
	return nil, fmt.Errorf("unexpected read-only native endpoint %q", endpoint)
}

type dnsReconcileFixture struct {
	cfg     deploymentConfig
	store   secretStore
	native  *dnsReconcileNativeInventory
	dns     *dnsReconcileReadOnlyAdapter
	factory string
	turn    string
	oldTurn []string
}

func newDNSReconcileFixture(t *testing.T, environment string) dnsReconcileFixture {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, environment)
	if err := store.write("operator/env/LINODE_TOKEN", []byte("isolated-dns-reconciliation-token"), true); err != nil {
		t.Fatal(err)
	}
	stack := "video-cloud-" + environment
	cfg := deploymentConfig{
		Environment: environment, Adapter: "lke", DNSAdapter: "godaddy",
		Workspace: t.TempDir(), RuntimeRoot: t.TempDir(),
		Values: map[string]string{"CLOUD_ENV_NAME": environment, "CLOUD_STACK_NAME": stack,
			"CLOUD_DNS_ROOT_DOMAIN": "example.test", "TURN_REPLICAS": "1", "EDGE_REPLICAS": "1", "FACTORY_ENROLL_PUBLIC_ENABLED": "false"},
		DNSValues: map[string]string{"DNS_RECORD_TTL": "600"}, AdapterResolved: map[string]string{"LKE_REGION": "us-sea"},
	}
	native := &dnsReconcileNativeInventory{t: t, denied: map[string]bool{},
		clusters: []lkeCluster{{ID: 123, Label: stack + "-lke", Region: "us-sea"}},
		instances: []linodeInstance{
			{ID: 41, Label: stack + "-edge-haproxy-01", Region: "us-sea", IPv4: []string{"198.51.100.41", "192.168.1.41"}, Tags: []string{"rtk-cloud", stack, "edge-haproxy"}},
			{ID: 42, Label: stack + "-turn01", Region: "us-sea", IPv4: []string{"198.51.100.42"}, Tags: []string{"rtk-cloud", stack, "coturn-vm"}},
		},
	}
	dns := &dnsReconcileReadOnlyAdapter{t: t, zone: dnsZone{Name: "example.test"}, records: map[string]dnsRecordSet{}}
	for name, role := range dnsReconciliationCandidates(cfg) {
		ip := "198.51.100.41"
		if role == "coturn-vm" {
			ip = "198.51.100.42"
		}
		dns.records["A:"+name] = dnsRecordSet{Name: name, Type: "A", Values: []string{ip}, TTL: 600}
	}
	values := deploymentEndpointValues(cfg)
	old := []string{"turn01." + values["VIDEO_CLOUD_DOMAIN"], "turn02." + values["VIDEO_CLOUD_DOMAIN"]}
	for _, name := range old {
		dns.records["A:"+name] = dnsRecordSet{Name: name, Type: "A", Values: []string{"203.0.113.99"}, TTL: 600}
	}
	return dnsReconcileFixture{cfg, store, native, dns, values["FACTORY_ENROLL_DOMAIN"], lkeCoturnDomain(values), old}
}

func reviewDNSReconcileFixture(t *testing.T, f dnsReconcileFixture) deploymentDNSReconciliation {
	t.Helper()
	review, err := reviewDeploymentDNSReconciliation(context.Background(), f.cfg, f.native.read, f.dns)
	if err != nil {
		t.Fatal(err)
	}
	if review.ReviewSHA256 == "" || review.ReviewSHA256 != dnsReconciliationSHA(review) {
		t.Fatal("review does not bind the complete candidate authority")
	}
	return review
}

func TestDeploymentDNSReconcileReviewUsesExactManagedAuthority(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			f := newDNSReconcileFixture(t, environment)
			before := snapshotRemovalRecoveryStore(t, f.store.Root)
			var output bytes.Buffer
			if err := reconcileDeploymentDNSState(context.Background(), f.cfg, dnsReconciliationRequest{}, f.native.read, f.dns, &output); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(output.String(), "Read-only review") || !strings.Contains(output.String(), "review-sha256:") {
				t.Fatal("default command did not clearly return a read-only approval candidate")
			}
			review := reviewDNSReconcileFixture(t, f)
			if after := snapshotRemovalRecoveryStore(t, f.store.Root); !reflect.DeepEqual(before, after) {
				t.Fatal("read-only review changed private environment state")
			}
			foundFactory, foundTurn := false, false
			for _, mapping := range review.Mappings {
				if len(mapping.Targets) != 1 {
					t.Fatal("review did not preserve exact single numeric role authority")
				}
				target := mapping.Targets[0]
				if mapping.Record.Name == f.turn {
					foundTurn = target.ID == 42 && target.Role == "coturn-vm" && target.IPv4 == "198.51.100.42"
				} else if target.ID != 41 || target.Role != "edge-haproxy" || target.IPv4 != "198.51.100.41" {
					t.Fatalf("edge mapping acquired foreign authority: %+v", mapping)
				}
				foundFactory = foundFactory || mapping.Record.Name == f.factory
				if slices.Contains(f.oldTurn, mapping.Record.Name) {
					t.Fatal("an old alien TURN alias entered deletion authority")
				}
			}
			if !foundFactory || !foundTurn {
				t.Fatal("review omitted disabled registered factory remnant or current configured TURN mapping")
			}
			for _, name := range f.oldTurn {
				found := false
				for _, excluded := range review.Excluded {
					found = found || excluded.Name == name && excluded.Type == "A" && slices.Contains(excluded.Values, "203.0.113.99")
				}
				if !found {
					t.Fatalf("old alien alias %q was not visible in preserved exclusions", name)
				}
			}
		})
	}
}

func TestDeploymentDNSReconcileRejectsNameOnlyOrForeignInstances(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"name-only", "foreign-stack-tag", "wrong-role-tag", "foreign-region"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				f := newDNSReconcileFixture(t, environment)
				item := &f.native.instances[0]
				switch mode {
				case "name-only":
					item.Tags = nil
				case "foreign-stack-tag":
					item.Tags = []string{"rtk-cloud", "video-cloud-another", "edge-haproxy"}
				case "wrong-role-tag":
					item.Tags = []string{"rtk-cloud", f.cfg.Values["CLOUD_STACK_NAME"], "coturn-vm"}
				case "foreign-region":
					item.Region = "sg-sin-2"
				}
				review := reviewDNSReconcileFixture(t, f)
				for _, mapping := range review.Mappings {
					for _, target := range mapping.Targets {
						if target.ID == item.ID {
							t.Fatal("name-only or foreign instance became DNS ownership authority")
						}
					}
				}
				if len(review.Mappings) != 1 || review.Mappings[0].Record.Name != f.turn {
					t.Fatal("excluded edge should leave only the independently verified TURN mapping")
				}
			})
		}
	}
}

func TestDeploymentDNSReconcileBlocksOutstandingACME(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"live-TXT", "original-private-challenge"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				f := newDNSReconcileFixture(t, environment)
				if mode == "live-TXT" {
					name, err := dns01RecordName(f.dns.zone.Name, f.factory)
					if err != nil {
						t.Fatal(err)
					}
					f.dns.records["TXT:"+name] = dnsRecordSet{Name: name, Type: "TXT", Values: []string{"private-original-challenge"}}
				} else if err := recordDNSChallenge(dnsReconciliationContext(f.cfg), "godaddy", f.factory, "private-original-challenge"); err != nil {
					t.Fatal(err)
				}
				before := snapshotRemovalRecoveryStore(t, f.store.Root)
				_, err := reviewDeploymentDNSReconciliation(context.Background(), f.cfg, f.native.read, f.dns)
				if err == nil || !strings.Contains(err.Error(), "ACME") || strings.Contains(err.Error(), "private-original-challenge") {
					t.Fatal("unresolved ACME did not block safely", err)
				}
				if after := snapshotRemovalRecoveryStore(t, f.store.Root); !reflect.DeepEqual(before, after) {
					t.Fatal("blocked review discarded original ACME evidence")
				}
			})
		}
	}
}

func publishDNSReconcileFixture(t *testing.T, f dnsReconcileFixture) deploymentDNSReconciliation {
	t.Helper()
	review := reviewDNSReconcileFixture(t, f)
	request := dnsReconciliationRequest{Confirm: f.cfg.Values["CLOUD_STACK_NAME"], ReviewSHA256: review.ReviewSHA256}
	var output bytes.Buffer
	if err := reconcileDeploymentDNSState(context.Background(), f.cfg, request, f.native.read, f.dns, &output); err != nil {
		t.Fatal(err)
	}
	return review
}

func TestDeploymentDNSReconcilePublicationRequiresExactReview(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"wrong-stack", "missing-digest", "wrong-digest", "dns-drift", "instance-ID-drift", "private-metadata-drift", "existing-ownership", "conflicting-receipt", "active-execution"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				f := newDNSReconcileFixture(t, environment)
				review := reviewDNSReconcileFixture(t, f)
				request := dnsReconciliationRequest{Confirm: f.cfg.Values["CLOUD_STACK_NAME"], ReviewSHA256: review.ReviewSHA256}
				switch mode {
				case "wrong-stack":
					request.Confirm = "video-cloud-another"
				case "missing-digest":
					request.ReviewSHA256 = ""
				case "wrong-digest":
					request.ReviewSHA256 = strings.Repeat("f", 64)
				case "dns-drift":
					f.dns.records["A:"+f.factory] = dnsRecordSet{Name: f.factory, Type: "A", Values: []string{"203.0.113.88"}, TTL: 600}
				case "instance-ID-drift":
					f.native.instances[0].ID = 987
				case "private-metadata-drift":
					if err := writeDNSLocalState(dnsReconciliationContext(f.cfg), "godaddy", "challenges.json", []byte("[]")); err != nil {
						t.Fatal(err)
					}
				case "existing-ownership":
					if err := writeDNSOwnership(dnsReconciliationContext(f.cfg), "godaddy", []dnsRecordSet{{Name: f.factory, Type: "A", Values: []string{"203.0.113.7"}, TTL: 600}}); err != nil {
						t.Fatal(err)
					}
				case "conflicting-receipt":
					if err := writePrivateDeploymentState(environment, "deployment/dns/godaddy/reconciliation.json", []byte("{\"schema\":99}\n")); err != nil {
						t.Fatal(err)
					}
				case "active-execution":
					if err := writePrivateDeploymentState(environment, "deployment/.execution-active", []byte("existing execution")); err != nil {
						t.Fatal(err)
					}
				}
				before := snapshotRemovalRecoveryStore(t, f.store.Root)
				var output bytes.Buffer
				if err := reconcileDeploymentDNSState(context.Background(), f.cfg, request, f.native.read, f.dns, &output); err == nil {
					t.Fatal("changed authority or original evidence consumed an earlier review")
				}
				if after := snapshotRemovalRecoveryStore(t, f.store.Root); !reflect.DeepEqual(before, after) {
					t.Fatal("rejected reconciliation changed original private evidence")
				}
			})
		}
	}
}

func TestDeploymentDNSReconcilePublishesPrivateEvidenceAndValidatesExactIDs(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			f := newDNSReconcileFixture(t, environment)
			review := publishDNSReconcileFixture(t, f)
			owned, err := readDNSOwnership(dnsReconciliationContext(f.cfg), "godaddy")
			if err != nil || len(owned.Records) != len(review.Mappings) {
				t.Fatal("publication did not retain exactly the reviewed ownership mappings", err)
			}
			for _, record := range owned.Records {
				if slices.Contains(f.oldTurn, record.Name) {
					t.Fatal("excluded alias was published as owned")
				}
			}
			path, err := dnsLocalStatePath(dnsReconciliationContext(f.cfg), "godaddy", "ownership.json")
			if err != nil {
				t.Fatal(err)
			}
			if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("published ownership was not private", err)
			}
			if info, err := os.Stat(filepath.Dir(path)); err != nil || info.Mode().Perm() != 0700 {
				t.Fatal("published DNS directory was not private", err)
			}
			if err := validateDeploymentDNSReconciliation(context.Background(), f.cfg, f.native.read, f.dns); err != nil {
				t.Fatal("published reviewed authority failed immediate validation", err)
			}
			if !slices.Contains(f.native.calls, "/linode/instances/41") || !slices.Contains(f.native.calls, "/linode/instances/42") {
				t.Fatal("consumer trusted a mutable listing instead of exact numeric instance IDs")
			}
		})
	}
}

func TestDeploymentDNSReconcileConsumerRejectsDriftAndUnknownAuthority(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"numeric-ID-replacement", "tag-drift", "public-IP-drift", "foreign-DNS-value", "exact-ID-read-denied", "unknown-receipt-authority", "receipt-digest-drift", "excluded-turn-ACME", "new-CNAME", "new-AAAA"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				f := newDNSReconcileFixture(t, environment)
				publishDNSReconcileFixture(t, f)
				switch mode {
				case "numeric-ID-replacement":
					f.native.instances[0].ID = 987
				case "tag-drift":
					f.native.instances[0].Tags = nil
				case "public-IP-drift":
					f.native.instances[0].IPv4 = []string{"203.0.113.88"}
				case "foreign-DNS-value":
					f.dns.records["A:"+f.factory] = dnsRecordSet{Name: f.factory, Type: "A", Values: []string{"203.0.113.88"}, TTL: 600}
				case "exact-ID-read-denied":
					f.native.denied["/linode/instances/41"] = true
				case "excluded-turn-ACME":
					name, err := dns01RecordName(f.dns.zone.Name, f.oldTurn[0])
					if err != nil {
						t.Fatal(err)
					}
					f.dns.records["TXT:"+name] = dnsRecordSet{Name: name, Type: "TXT", Values: []string{"new-private-challenge"}}
				case "new-CNAME":
					f.dns.records["CNAME:"+f.factory] = dnsRecordSet{Name: f.factory, Type: "CNAME", Values: []string{"foreign.example.test"}}
				case "new-AAAA":
					f.dns.records["AAAA:"+f.factory] = dnsRecordSet{Name: f.factory, Type: "AAAA", Values: []string{"2001:db8::88"}}
				case "unknown-receipt-authority", "receipt-digest-drift":
					owned, err := readDNSOwnership(dnsReconciliationContext(f.cfg), "godaddy")
					if err != nil {
						t.Fatal(err)
					}
					receipt, err := readDeploymentDNSReconciliation(dnsReconciliationContext(f.cfg), "godaddy", owned)
					if err != nil {
						t.Fatal(err)
					}
					if mode == "unknown-receipt-authority" {
						receipt.Method = "name-only-invented-authority"
						receipt.ReviewSHA256 = dnsReconciliationSHA(receipt)
						owned.ReconciliationSHA256 = receipt.ReviewSHA256
						ownedRaw, err := json.Marshal(owned)
						if err != nil {
							t.Fatal(err)
						}
						if err := writeDNSLocalState(dnsReconciliationContext(f.cfg), "godaddy", "ownership.json", ownedRaw); err != nil {
							t.Fatal(err)
						}
					} else {
						receipt.ReviewSHA256 = strings.Repeat("a", 64)
					}
					raw, err := json.Marshal(receipt)
					if err != nil {
						t.Fatal(err)
					}
					if err := writePrivateDeploymentState(environment, "deployment/dns/godaddy/reconciliation.json", raw); err != nil {
						t.Fatal(err)
					}
				}
				before := snapshotRemovalRecoveryStore(t, f.store.Root)
				if err := validateDeploymentDNSReconciliation(context.Background(), f.cfg, f.native.read, f.dns); err == nil {
					t.Fatal("consumer accepted changed or unverifiable numeric ownership authority")
				}
				if after := snapshotRemovalRecoveryStore(t, f.store.Root); !reflect.DeepEqual(before, after) {
					t.Fatal("consumer altered recovery evidence")
				}
			})
		}
	}
}

func TestDeploymentDNSReconcileIgnoresProcessEndpointOverrides(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			f := newDNSReconcileFixture(t, environment)
			expected := lkeFrontendPublicDomain(deploymentEndpointValues(f.cfg))
			foreign := "alien.example.test"
			t.Setenv("LKE_FRONTEND_DOMAIN", foreign)
			f.dns.records["A:"+foreign] = dnsRecordSet{Name: foreign, Type: "A", Values: []string{"198.51.100.41"}, TTL: 600}
			review := reviewDNSReconcileFixture(t, f)
			foundExpected := false
			for _, mapping := range review.Mappings {
				if mapping.Record.Name == foreign {
					t.Fatal("an arbitrary process endpoint override entered reviewed deletion authority")
				}
				foundExpected = foundExpected || mapping.Record.Name == expected
			}
			if !foundExpected {
				t.Fatal("registered maintained frontend was replaced by a process override")
			}
		})
	}
}

func TestDeploymentDNSReconcileConsumerAbsentRetryUsesExactAuthority(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"complete-404-retry", "same-label-new-ID", "404-DNS-still-present", "403-DNS-empty"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				f := newDNSReconcileFixture(t, environment)
				publishDNSReconcileFixture(t, f)
				if mode != "404-DNS-still-present" {
					for key, record := range f.dns.records {
						if record.Type == "A" && !slices.Contains(f.oldTurn, record.Name) {
							record.Values = nil
							f.dns.records[key] = record
						}
					}
				}
				switch mode {
				case "complete-404-retry", "404-DNS-still-present":
					f.native.instances = []linodeInstance{}
					if mode == "complete-404-retry" {
						f.native.clusters = []lkeCluster{}
					}
				case "same-label-new-ID":
					f.native.instances[0].ID = 987
				case "403-DNS-empty":
					f.native.denied["/linode/instances/41"] = true
				}
				before := snapshotRemovalRecoveryStore(t, f.store.Root)
				err := validateDeploymentDNSReconciliation(context.Background(), f.cfg, f.native.read, f.dns)
				if mode == "complete-404-retry" {
					if err != nil {
						t.Fatal("exact 404 and absent reviewed DNS should permit an idempotent retry", err)
					}
				} else if err == nil {
					t.Fatal("unknown generation, surviving DNS or denied exact-ID read was treated as completed removal")
				}
				if after := snapshotRemovalRecoveryStore(t, f.store.Root); !reflect.DeepEqual(before, after) {
					t.Fatal("retry validation rewrote original reviewed authority")
				}
			})
		}
	}
}

func TestDeploymentDNSReconcileProviderPhaseRejectsGenerationAfterDNSCleanup(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, replacement := range []string{"cluster", "edge"} {
			t.Run(environment+"/"+replacement, func(t *testing.T) {
				f := newDNSReconcileFixture(t, environment)
				review := publishDNSReconcileFixture(t, f)
				if err := validateDeploymentDNSReconciliation(context.Background(), f.cfg, f.native.read, f.dns); err != nil {
					t.Fatal("original reviewed generation failed validation before DNS cleanup", err)
				}
				// DNS cleanup can finish before a later provider phase. Its empty
				// records cannot authorize a replacement born during that interval.
				for _, mapping := range review.Mappings {
					key := "A:" + mapping.Record.Name
					record := f.dns.records[key]
					record.Values = nil
					f.dns.records[key] = record
				}
				if replacement == "cluster" {
					f.native.clusters[0].ID = 987
				} else {
					f.native.instances[0].ID = 987
				}
				before := snapshotRemovalRecoveryStore(t, f.store.Root)
				if err := validateDeploymentDNSReconciliation(context.Background(), f.cfg, f.native.read, f.dns); err == nil {
					t.Fatal("completed DNS cleanup allowed a new numeric provider generation into the destructive phase")
				}
				if after := snapshotRemovalRecoveryStore(t, f.store.Root); !reflect.DeepEqual(before, after) {
					t.Fatal("the failed provider phase fence altered the approved evidence")
				}
			})
		}
	}
}
