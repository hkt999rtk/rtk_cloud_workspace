package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/route53"
	"github.com/aws/aws-sdk-go-v2/service/route53/types"
)

func writeRemoveDNSFixture(t *testing.T, environment, adapter string) (provisionPaths, map[string]string, string, []byte) {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, environment)
	for _, key := range []string{"GODADDY_KEY", "GODADDY_SECRET"} {
		if err := store.write("operator/env/"+key, []byte("private-cleanup-credential"), true); err != nil {
			t.Fatal(err)
		}
	}
	paths := provisionPaths{EnvRoot: t.TempDir()}
	env := map[string]string{"CLOUD_ENV_NAME": environment, "DNS_ADAPTER": adapter, "CLOUD_DNS_ROOT_DOMAIN": "example.test", "DNS_RECORD_TTL": "600"}
	ctx := dnsContext(paths, env)
	if err := writeDNSOwnership(ctx, adapter, []dnsRecordSet{
		{Name: "first.example.test", Type: "A", Values: []string{"192.0.2.1"}, TTL: 600},
		{Name: "second.example.test", Type: "A", Values: []string{"192.0.2.2"}, TTL: 600},
	}); err != nil {
		t.Fatal(err)
	}
	path, err := dnsLocalStatePath(ctx, adapter, "ownership.json")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return paths, env, path, raw
}

func TestRemoveOwnedDNSRequiresOriginalProofAndCompletedChallenges(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"missing-proof", "empty-proof", "outstanding-challenge"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				paths, env, ownershipPath, original := writeRemoveDNSFixture(t, environment, "godaddy")
				ctx := dnsContext(paths, env)
				switch mode {
				case "missing-proof":
					if err := os.Remove(ownershipPath); err != nil {
						t.Fatal(err)
					}
				case "empty-proof":
					var owned dnsOwnership
					if err := json.Unmarshal(original, &owned); err != nil {
						t.Fatal(err)
					}
					owned.Records = []dnsRecordSet{}
					original, _ = json.Marshal(owned)
					if err := writeDNSLocalState(ctx, "godaddy", "ownership.json", original); err != nil {
						t.Fatal(err)
					}
				case "outstanding-challenge":
					if err := recordDNSChallenge(ctx, "godaddy", "first.example.test", "original-private-challenge"); err != nil {
						t.Fatal(err)
					}
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					t.Error("unsafe or empty local DNS evidence reached the provider")
					w.WriteHeader(http.StatusInternalServerError)
				}))
				defer server.Close()
				t.Setenv("RTK_CLOUD_GODADDY_API_ROOT", server.URL)
				var err error
				output := captureStdout(t, func() { err = removeOwnedDNSRecords(paths, env) })
				switch mode {
				case "missing-proof":
					if err == nil || !strings.Contains(err.Error(), "original DNS ownership evidence is missing") {
						t.Fatal("missing original proof was accepted", err)
					}
				case "empty-proof":
					if err != nil {
						t.Fatal("valid empty ownership proof was rejected", err)
					}
				case "outstanding-challenge":
					if err == nil || !strings.Contains(err.Error(), "complete original ACME cleanup") || strings.Contains(err.Error(), "original-private-challenge") {
						t.Fatal("outstanding challenge did not block cleanup", err)
					}
					challenges, readErr := readDNSChallenges(ctx, "godaddy")
					if readErr != nil || len(challenges) != 1 || challenges[0].Value != "original-private-challenge" {
						t.Fatal("original ACME cleanup evidence changed", readErr)
					}
				}
				if mode != "missing-proof" {
					current, readErr := os.ReadFile(ownershipPath)
					if readErr != nil || !bytes.Equal(current, original) {
						t.Fatal("original ownership evidence changed", readErr)
					}
				}
				if strings.Contains(output, "original-private-challenge") || strings.Contains(output, "private-cleanup-credential") {
					t.Fatal("cleanup exposed private evidence")
				}
			})
		}
	}
}

func TestRemoveOwnedDNSGoDaddyRecoveryPreservesProof(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"complete", "already-absent", "already-absent-404", "delete-404", "later-drift", "partial-retry", "partial-retry-404", "unconfirmed-delete", "concurrent-foreign", "read-denied"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				paths, env, ownershipPath, original := writeRemoveDNSFixture(t, environment, "godaddy")
				values := map[string][]string{"first": {"192.0.2.1"}, "second": {"192.0.2.2"}}
				if strings.HasPrefix(mode, "already-absent") {
					values["first"] = nil
				} else if mode == "later-drift" {
					values["second"] = []string{"192.0.2.99"}
				}
				failSecond := strings.HasPrefix(mode, "partial-retry")
				deletes := map[string]int{}
				reads := map[string]int{}
				puts := 0
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					name := r.URL.Path[strings.LastIndex(r.URL.Path, "/")+1:]
					switch r.Method {
					case http.MethodGet:
						reads[name]++
						if mode == "read-denied" {
							w.WriteHeader(http.StatusForbidden)
							return
						}
						if mode == "concurrent-foreign" && name == "first" && reads[name] == 3 {
							values[name] = append(values[name], "198.51.100.99")
						}
						if strings.HasSuffix(mode, "-404") && len(values[name]) == 0 {
							w.WriteHeader(http.StatusNotFound)
							return
						}
						payload := []map[string]any{}
						for _, value := range values[name] {
							payload = append(payload, map[string]any{"data": value, "ttl": 600})
						}
						_ = json.NewEncoder(w).Encode(payload)
					case http.MethodDelete:
						deletes[name]++
						if reads["first"] == 0 || reads["second"] == 0 {
							t.Error("mutation preceded validation of all owned records")
						}
						if name == "second" && failSecond {
							w.WriteHeader(http.StatusServiceUnavailable)
							return
						}
						if mode != "unconfirmed-delete" || name != "second" {
							values[name] = nil
						}
						w.WriteHeader(http.StatusOK)
					case http.MethodPut:
						var payload []struct {
							Data string `json:"data"`
						}
						if mode != "concurrent-foreign" || name != "first" || json.NewDecoder(r.Body).Decode(&payload) != nil || len(payload) != 1 || payload[0].Data != "198.51.100.99" {
							t.Error("cleanup changed foreign DNS values")
							w.WriteHeader(http.StatusInternalServerError)
							return
						}
						puts++
						values[name] = []string{payload[0].Data}
						w.WriteHeader(http.StatusOK)
					default:
						t.Errorf("unexpected DNS mutation %s", r.Method)
						w.WriteHeader(http.StatusInternalServerError)
					}
				}))
				defer server.Close()
				t.Setenv("RTK_CLOUD_GODADDY_API_ROOT", server.URL)
				var err error
				output := captureStdout(t, func() { err = removeOwnedDNSRecords(paths, env) })
				switch mode {
				case "read-denied":
					if err == nil || !strings.Contains(err.Error(), "HTTP 403") || len(deletes) != 0 {
						t.Fatalf("denied DNS read passed: err=%v deletes=%v", err, deletes)
					}
				case "later-drift":
					if err == nil || !strings.Contains(err.Error(), "DNS drift prevents removal") || len(deletes) != 0 {
						t.Fatalf("drift mutated DNS: err=%v deletes=%v", err, deletes)
					}
				case "partial-retry", "partial-retry-404":
					if err == nil || len(values["first"]) != 0 || len(values["second"]) == 0 {
						t.Fatalf("expected partial deletion: err=%v deletes=%v", err, deletes)
					}
					failSecond = false
					output += captureStdout(t, func() { err = removeOwnedDNSRecords(paths, env) })
					if err != nil || deletes["first"] != 1 || deletes["second"] != 2 || !strings.Contains(output, "[ALREADY ABSENT] DNS A first.example.test") {
						t.Fatalf("retry failed: err=%v deletes=%v output=%s", err, deletes, output)
					}
				case "unconfirmed-delete":
					if err == nil || !strings.Contains(err.Error(), "owned values remain") || strings.Contains(output, "[DELETED] DNS A second.example.test") {
						t.Fatalf("unconfirmed deletion reported success: err=%v output=%s", err, output)
					}
				case "concurrent-foreign":
					if err != nil || puts != 1 || deletes["first"] != 0 || strings.Join(values["first"], ",") != "198.51.100.99" || len(values["second"]) != 0 {
						t.Fatal("cleanup did not preserve concurrent foreign values", err, deletes)
					}
				default:
					if err != nil || len(values["first"]) != 0 || len(values["second"]) != 0 {
						t.Fatal("cleanup failed", err)
					}
					if strings.HasPrefix(mode, "already-absent") && (deletes["first"] != 0 || !strings.Contains(output, "[ALREADY ABSENT] DNS A first.example.test")) {
						t.Fatal("absent record was mutated", deletes, output)
					}
				}
				current, readErr := os.ReadFile(ownershipPath)
				if readErr != nil || !bytes.Equal(current, original) {
					t.Fatal("original cleanup ownership evidence changed", readErr)
				}
				if strings.Contains(output, "private-cleanup-credential") || err != nil && strings.Contains(err.Error(), "private-cleanup-credential") {
					t.Fatal("cleanup exposed private credentials")
				}
			})
		}
	}
}

type removeRoute53API struct {
	fakeRoute53API
	failSecond bool
	reads      map[string]int
}

func (f *removeRoute53API) ListResourceRecordSets(_ context.Context, input *route53.ListResourceRecordSetsInput, _ ...func(*route53.Options)) (*route53.ListResourceRecordSetsOutput, error) {
	name := aws.ToString(input.StartRecordName)
	f.reads[name]++
	for _, record := range f.records {
		if aws.ToString(record.Name) == name && record.Type == input.StartRecordType {
			return &route53.ListResourceRecordSetsOutput{ResourceRecordSets: []types.ResourceRecordSet{record}}, nil
		}
	}
	return &route53.ListResourceRecordSetsOutput{}, nil
}

func (f *removeRoute53API) ChangeResourceRecordSets(ctx context.Context, input *route53.ChangeResourceRecordSetsInput, options ...func(*route53.Options)) (*route53.ChangeResourceRecordSetsOutput, error) {
	for _, change := range input.ChangeBatch.Changes {
		name := aws.ToString(change.ResourceRecordSet.Name)
		if name == "second.example.test." && f.failSecond {
			return nil, errors.New("isolated DNS delete failure")
		}
		for i, record := range f.records {
			if aws.ToString(record.Name) == name && change.Action == types.ChangeActionDelete {
				f.records = append(f.records[:i], f.records[i+1:]...)
				break
			}
		}
	}
	return f.fakeRoute53API.ChangeResourceRecordSets(ctx, input, options...)
}

func TestRemoveOwnedDNSRoute53PartialRetry(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"partial-retry", "changed-zone"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				paths, env, ownershipPath, original := writeRemoveDNSFixture(t, environment, "route53")
				if err := writeDNSProviderState(dnsContext(paths, env), "route53", map[string]any{"adapter": "route53", "zone_name": "example.test", "hosted_zone_id": "original-zone"}); err != nil {
					t.Fatal(err)
				}
				zoneID := "original-zone"
				if mode == "changed-zone" {
					zoneID = "replacement-zone"
				}
				fake := &removeRoute53API{fakeRoute53API: fakeRoute53API{
					zones: []types.HostedZone{publicZone("example.test.", zoneID, false)},
					records: []types.ResourceRecordSet{
						route53Record(dnsRecordSet{Name: "first.example.test", Type: "A", Values: []string{"192.0.2.1"}, TTL: 600}),
						route53Record(dnsRecordSet{Name: "second.example.test", Type: "A", Values: []string{"192.0.2.2"}, TTL: 600}),
					},
				}, failSecond: true, reads: map[string]int{}}
				previousLoader := loadRoute53Client
				loadRoute53Client = func(context.Context, string) (route53API, error) { return fake, nil }
				t.Cleanup(func() { loadRoute53Client = previousLoader })
				if mode == "changed-zone" {
					if err := removeOwnedDNSRecords(paths, env); err == nil || !strings.Contains(err.Error(), "hosted-zone identity") || len(fake.changes) != 0 || len(fake.reads) != 0 {
						t.Fatal("replacement hosted zone was accessed", err, len(fake.changes))
					}
					return
				}
				if err := removeOwnedDNSRecords(paths, env); err == nil || len(fake.changes) != 1 {
					t.Fatal("expected partial cleanup", err, len(fake.changes))
				}
				fake.failSecond = false
				var err error
				output := captureStdout(t, func() { err = removeOwnedDNSRecords(paths, env) })
				if err != nil || len(fake.changes) != 2 || len(fake.records) != 0 || !strings.Contains(output, "[ALREADY ABSENT] DNS A first.example.test") {
					t.Fatal("Route53 retry failed", err, output)
				}
				current, readErr := os.ReadFile(ownershipPath)
				if readErr != nil || !bytes.Equal(current, original) {
					t.Fatal("Route53 cleanup discarded original evidence", readErr)
				}
			})
		}
	}
}
