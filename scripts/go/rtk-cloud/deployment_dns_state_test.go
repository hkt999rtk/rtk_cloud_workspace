package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/route53/types"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestDNSPrivateStateUsesOneEnvironmentAuthority(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, environment)
			root := t.TempDir()
			ctx := dnsContext(provisionPaths{EnvRoot: root}, map[string]string{"CLOUD_ENV_NAME": environment, "CLOUD_DNS_ROOT_DOMAIN": "example.test"})
			records := []dnsRecordSet{{Name: "api.example.test", Type: "A", Values: []string{"192.0.2.1"}, TTL: 600}}
			if err := writeDNSOwnership(ctx, "godaddy", records); err != nil {
				t.Fatal(err)
			}
			if err := recordDNSChallenge(ctx, "godaddy", "api.example.test", "original-challenge"); err != nil {
				t.Fatal(err)
			}
			if err := writeDNSProviderState(ctx, "godaddy", map[string]any{"adapter": "godaddy", "zone_name": "example.test"}); err != nil {
				t.Fatal(err)
			}
			if err := writeDNSLocalState(ctx, "godaddy", "dns.env", []byte("DNS_ADAPTER=godaddy\nDNS_ROOT_DOMAIN=example.test\n")); err != nil {
				t.Fatal(err)
			}
			cfg := deploymentConfig{Environment: environment, DNSAdapter: "godaddy", RuntimeRoot: root, Values: ctx.Values}
			before, err := environmentDNSStateDigest(cfg)
			if err != nil {
				t.Fatal(err)
			}
			// A checkout switch preserves authority and the approved input digest.
			cfg.RuntimeRoot = t.TempDir()
			after, err := environmentDNSStateDigest(cfg)
			if err != nil || before != after {
				t.Fatal("checkout changed state authority", err)
			}
			if err := recordDNSChallenge(ctx, "godaddy", "api.example.test", "changed-challenge"); err != nil {
				t.Fatal(err)
			}
			after, err = environmentDNSStateDigest(cfg)
			if err != nil || before == after {
				t.Fatal("state drift did not invalidate the digest", err)
			}
			for _, name := range []string{"ownership.json", "challenges.json", "state.json", "dns.env"} {
				path, err := dnsLocalStatePath(ctx, "godaddy", name)
				if err != nil || path != filepath.Join(store.Root, "deployment", "dns", "godaddy", name) {
					t.Fatal(path, err)
				}
				info, err := os.Lstat(path)
				if err != nil || info.Mode().Perm() != 0600 {
					t.Fatal("file permissions", err)
				}
				info, err = os.Lstat(filepath.Dir(path))
				if err != nil || info.Mode().Perm() != 0700 {
					t.Fatal("directory permissions", err)
				}
			}
			if entries, _ := os.ReadDir(root); len(entries) != 0 {
				t.Fatal("deployment wrote configuration root")
			}
			if err := os.Remove(filepath.Join(store.Root, "deployment", "dns", "godaddy", "ownership.json")); err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, filepath.Join(root, "dns", "godaddy", "ownership.json"), "legacy")
			if _, err := readDNSOwnership(ctx, "godaddy"); err == nil {
				t.Fatal("legacy fallback accepted")
			}
		})
	}
}

func TestDNSImportVerifiesOriginalRecordsAndPreservesSources(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "foreign-environment", "foreign-zone", "drift", "denied", "malformed", "conflict", "execution-active"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				store := makeIsolatedTestSecretStore(t, environment)
				for key, value := range map[string]string{"GODADDY_KEY": "isolated-key", "GODADDY_SECRET": "isolated-secret"} {
					if err := store.write("operator/env/"+key, []byte(value), true); err != nil {
						t.Fatal(err)
					}
				}
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodGet {
						t.Error("import mutated DNS", r.Method)
						w.WriteHeader(500)
						return
					}
					if mode == "denied" {
						w.WriteHeader(403)
						return
					}
					values := []map[string]any{}
					if strings.Contains(r.URL.Path, "/records/A/") {
						value := "192.0.2.1"
						if mode == "drift" {
							value = "192.0.2.2"
						}
						values = append(values, map[string]any{"data": value, "ttl": 600})
					} else if strings.Contains(r.URL.Path, "_acme-challenge") {
						values = append(values, map[string]any{"data": "original-challenge", "ttl": 600})
					}
					_ = json.NewEncoder(w).Encode(values)
				}))
				defer server.Close()
				t.Setenv("RTK_CLOUD_GODADDY_API_ROOT", server.URL)
				source := t.TempDir()
				stack := "video-cloud-" + environment
				zone := "example.test"
				config := "CLOUD_ENV_NAME=" + environment + "\nCLOUD_STACK_NAME=" + stack + "\nCLOUD_DNS_ROOT_DOMAIN=" + zone + "\n"
				if mode == "foreign-environment" {
					config = strings.Replace(config, "CLOUD_ENV_NAME="+environment, "CLOUD_ENV_NAME=other", 1)
				}
				writeTestFile(t, filepath.Join(source, "env", "stack.env"), config)
				if mode == "foreign-zone" {
					zone = "other.test"
				}
				owned, _ := json.Marshal(map[string]any{"adapter": "godaddy", "root_domain": zone, "records": []dnsRecordSet{{Name: "api.example.test", Type: "A", Values: []string{"192.0.2.1"}, TTL: 600}}, "updated_at": "2026-10-01T00:00:00Z"})
				if mode == "malformed" {
					owned = []byte("{")
				}
				originals := map[string][]byte{"ownership.json": owned, "challenges.json": []byte(`[{"domain":"api.example.test","value":"original-challenge"}]`), "state.json": []byte(`{"adapter":"godaddy","zone_name":"example.test"}`), "dns.env": []byte("DNS_ADAPTER=godaddy\nDNS_ROOT_DOMAIN=example.test\nDNS_UPDATED_AT=2026-10-01T00:00:00Z\n")}
				for name, raw := range originals {
					path := filepath.Join(source, "dns", "godaddy", name)
					if name == "dns.env" {
						path = filepath.Join(source, "state", name)
					}
					if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "conflict" {
					if err := writePrivateDeploymentState(environment, "deployment/dns/godaddy/ownership.json", []byte("concurrent")); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "execution-active" {
					if err := writePrivateDeploymentState(environment, "deployment/.execution-active", []byte("live-owner")); err != nil {
						t.Fatal(err)
					}
				}
				cfg := deploymentConfig{Environment: environment, DNSAdapter: "godaddy", Values: map[string]string{"CLOUD_ENV_NAME": environment, "CLOUD_STACK_NAME": stack, "CLOUD_DNS_ROOT_DOMAIN": "example.test"}, DNSValues: map[string]string{"DNS_RECORD_TTL": "600"}}
				count, err := importDeploymentDNSState(context.Background(), cfg, source, &goDaddyDNSAdapter{client: server.Client(), apiRoot: server.URL})
				if mode != "valid" {
					if err == nil {
						t.Fatal("unsafe import passed")
					}
					if strings.Contains(err.Error(), "isolated-secret") || strings.Contains(err.Error(), "original-challenge") {
						t.Fatal("private content exposed")
					}
					if mode != "conflict" && mode != "execution-active" {
						if _, statErr := os.Lstat(filepath.Join(store.Root, "deployment", "dns")); !os.IsNotExist(statErr) {
							t.Fatal("failed verification published destination", statErr)
						}
					}
					return
				}
				if err != nil || count != 4 {
					t.Fatal(count, err)
				}
				if _, err := importDeploymentDNSState(context.Background(), cfg, source, &goDaddyDNSAdapter{client: server.Client(), apiRoot: server.URL}); err != nil {
					t.Fatal("idempotent retry", err)
				}
				for name, raw := range originals {
					path := filepath.Join(source, "dns", "godaddy", name)
					if name == "dns.env" {
						path = filepath.Join(source, "state", name)
					}
					current, _ := os.ReadFile(path)
					archive, _ := os.ReadFile(filepath.Join(store.Root, "migration-backup", "dns-state", deploymentplan.Digest(originals), name))
					if !bytes.Equal(current, raw) || !bytes.Equal(archive, raw) {
						t.Fatal("original source bytes changed", name)
					}
				}
				cfg.RuntimeRoot = source
				if _, err := environmentDNSStateDigest(cfg); err != nil {
					t.Fatal("normal consumer cannot read imported state", err)
				}
			})
		}
	}
}

func TestDNSCredentialsRequireExplicitEnvironment(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "staging")
	for key := range map[string]bool{"GODADDY_KEY": true, "GODADDY_SECRET": true} {
		if err := store.write("operator/env/"+key, []byte("must-not-fallback"), true); err != nil {
			t.Fatal(err)
		}
	}
	if key, secret := (&goDaddyDNSAdapter{}).credentials(dnsAdapterContext{}); key != "" || secret != "" {
		t.Fatal("missing environment selected staging credentials")
	}
}

func TestDNSPrivateStateRejectsPermissionsAndForeignBindings(t *testing.T) {
	for _, mode := range []string{"public-file", "public-directory", "symlink", "foreign-owner", "foreign-config", "invalid-challenge", "unknown-adapter"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			root := t.TempDir()
			ctx := dnsContext(provisionPaths{EnvRoot: root}, map[string]string{"CLOUD_ENV_NAME": "dev", "CLOUD_DNS_ROOT_DOMAIN": "example.test"})
			if err := writeDNSOwnership(ctx, "godaddy", []dnsRecordSet{}); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(store.Root, "deployment", "dns", "godaddy", "ownership.json")
			switch mode {
			case "public-file":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "public-directory":
				if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				target := filepath.Join(t.TempDir(), "foreign")
				if err := os.WriteFile(target, []byte("must-not-read"), 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "foreign-owner":
				raw, _ := os.ReadFile(path)
				if err := os.WriteFile(path, bytes.ReplaceAll(raw, []byte(`"environment": "dev"`), []byte(`"environment": "staging"`)), 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign-config":
				writeTestFile(t, filepath.Join(root, "env", "stack.env"), "CLOUD_ENV_NAME=staging\n")
			case "invalid-challenge":
				if err := writeDNSLocalState(ctx, "godaddy", "challenges.json", []byte(`[{"domain":"api.other.test","value":"private-value"}]`)); err != nil {
					t.Fatal(err)
				}
			case "unknown-adapter":
				if err := validateDNSLocalState(ctx, "../staging"); err == nil {
					t.Fatal("adapter path traversal passed")
				}
				return
			}
			if err := validateDNSLocalState(ctx, "godaddy"); err == nil {
				t.Fatal("unsafe state passed")
			}
		})
	}
}

func TestDNSRoute53ImportRequiresOriginalHostedZone(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, zoneID := range []string{"original-zone", "replacement-zone"} {
			t.Run(environment+"/"+zoneID, func(t *testing.T) {
				makeIsolatedTestSecretStore(t, environment)
				source := t.TempDir()
				stack := "video-cloud-" + environment
				writeTestFile(t, filepath.Join(source, "env", "stack.env"), "CLOUD_ENV_NAME="+environment+"\nCLOUD_STACK_NAME="+stack+"\nCLOUD_DNS_ROOT_DOMAIN=example.test\n")
				path := filepath.Join(source, "dns", "route53", "state.json")
				if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(`{"adapter":"route53","zone_name":"example.test","hosted_zone_id":"original-zone"}`), 0600); err != nil {
					t.Fatal(err)
				}
				fake := &fakeRoute53API{zones: []types.HostedZone{publicZone("example.test.", zoneID, false)}}
				cfg := deploymentConfig{Environment: environment, DNSAdapter: "route53", Values: map[string]string{"CLOUD_ENV_NAME": environment, "CLOUD_STACK_NAME": stack, "CLOUD_DNS_ROOT_DOMAIN": "example.test"}, DNSValues: map[string]string{"DNS_RECORD_TTL": "600"}}
				count, err := importDeploymentDNSState(context.Background(), cfg, source, &route53DNSAdapter{client: fake})
				if zoneID == "original-zone" {
					if err != nil || count != 1 {
						t.Fatal(count, err)
					}
				} else if err == nil {
					t.Fatal("changed hosted-zone ID passed")
				}
				if len(fake.changes) != 0 {
					t.Fatal("import changed Route53 records")
				}
			})
		}
	}
}

func TestNewResolvedDNSConfigurationDoesNotRequireStateImport(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			makeIsolatedTestSecretStore(t, environment)
			workspace := writeDeploymentFixture(t, environment, "lke")
			cfg, err := resolveDeploymentConfig(workspace, environment, "")
			if err != nil {
				t.Fatal(err)
			}
			if err := materializeDeploymentRuntime(cfg); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(cfg.RuntimeRoot, "resolved", "dns.env")); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(filepath.Join(cfg.RuntimeRoot, "state", "dns.env")); !os.IsNotExist(err) {
				t.Fatal("configuration overwrote operational metadata", err)
			}
			if _, err := environmentDNSStateDigest(cfg); err != nil {
				t.Fatal("new configuration requires legacy state import", err)
			}
		})
	}
}
