package main

import (
	"context"
	"crypto/ed25519"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"

	"golang.org/x/crypto/ssh"
)

func createProviderTestFixture(t *testing.T, environment string) (deploymentConfig, map[string]string) {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, environment)
	t.Setenv("RTK_CREATE_FIXTURE_ENVIRONMENT", environment)
	writeTestLKEAccount(t, environment, "LKE_ACTIVE_SERVICE_LIMIT=100\n")
	key := ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize))
	public, err := ssh.NewPublicKey(key.Public())
	if err != nil {
		t.Fatal(err)
	}
	block, err := ssh.MarshalPrivateKey(key, "selected-operator")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.write("operator/ssh/id_ed25519", pem.EncodeToMemory(block), false); err != nil {
		t.Fatal(err)
	}
	if err := store.write("operator/ssh/id_ed25519.pub", ssh.MarshalAuthorizedKey(public), false); err != nil {
		t.Fatal(err)
	}
	cfg := deploymentConfig{Environment: environment, Adapter: "lke", Values: map[string]string{
		"CLOUD_STACK_NAME": "video-cloud-" + environment, "DEPLOYMENT_ADAPTER": "lke", "DEPLOYMENT_PROTECTION_PROFILE": "standard",
		"NODE_CLASS_GENERAL_EFFECTIVE_COUNT": "1", "NODE_CLASS_BROKER_EFFECTIVE_COUNT": "1", "NODE_CLASS_DATABASE_EFFECTIVE_COUNT": "0", "EDGE_REPLICAS": "1", "TURN_REPLICAS": "1",
	}, AdapterResolved: map[string]string{"LKE_REGION": "us-sea", "LKE_GENERAL_NODE_TYPE": "g6-standard-2", "LKE_BROKER_NODE_TYPE": "g6-standard-2", "LKE_DATABASE_NODE_TYPE": "g6-standard-4"}, Storage: deploymentStoragePlan{
		RuntimeMedia: deploymentStorageTarget{Purpose: "runtime-media", Bucket: "rtk-cloud-" + environment + "-runtime-us-sea", Region: "us-sea", Prefix: "environments/video-cloud-" + environment},
		OTAFirmware:  deploymentStorageTarget{Purpose: "ota-firmware", Bucket: "rtk-cloud-" + environment + "-ota-firmware-us-lax", Region: "us-lax", Prefix: "environments/video-cloud-" + environment},
		OTAMode:      "dedicated",
	}}
	operator := map[string]string{"LINODE_TOKEN": "private-provider-token", "DEPLOYMENT_SSH_PUBLIC_KEY": strings.TrimSpace(string(ssh.MarshalAuthorizedKey(public))), "LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "private-media-key", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "private-media-secret", "LINODE_OTA_OBJ_ACCESS_KEY_ID": "private-ota-key", "LINODE_OTA_OBJ_SECRET_ACCESS_KEY": "private-ota-secret"}
	for key, value := range operator {
		if err := store.write("operator/env/"+key, []byte(value), true); err != nil {
			t.Fatal(err)
		}
	}
	return cfg, operator
}

func createProviderTestAPI(t *testing.T, custom func(http.ResponseWriter, *http.Request) bool) *int {
	t.Helper()
	mutations := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-provider-token" {
			t.Error("selected credential missing")
		}
		if r.Method != http.MethodGet {
			mutations++
		}
		if custom != nil && custom(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/lke/versions":
			fmt.Fprint(w, `{"data":[{"id":"1.9"},{"id":"1.33"},{"id":"1.34"}],"page":1,"pages":1,"results":3}`)
		case r.URL.Path == "/object-storage/endpoints":
			fmt.Fprint(w, `{"data":[{"region":"us-sea","endpoint_type":"E1"},{"region":"us-lax","endpoint_type":"E3"}],"page":1,"pages":1,"results":2}`)
		case r.URL.Path == "/object-storage/keys":
			environment := firstNonEmpty(r.Header.Get("X-Fixture-Environment"), "dev")
			// Each environment test installs its exact requested bucket suffix.
			if value := os.Getenv("RTK_CREATE_FIXTURE_ENVIRONMENT"); value != "" {
				environment = value
			}
			json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": 21, "access_key": "private-media-key", "bucket_access": []any{map[string]string{"bucket_name": "rtk-cloud-" + environment + "-runtime-us-sea", "region": "us-sea", "permissions": "read_write"}}}, map[string]any{"id": 22, "access_key": "private-ota-key", "bucket_access": []any{map[string]string{"bucket_name": "rtk-cloud-" + environment + "-ota-firmware-us-lax", "region": "us-lax", "permissions": "read_write"}}}}, "page": 1, "pages": 1, "results": 2})
		case strings.HasPrefix(r.URL.Path, "/regions/"):
			json.NewEncoder(w).Encode(map[string]any{"id": strings.TrimPrefix(r.URL.Path, "/regions/"), "status": "ok", "capabilities": []string{"Linodes", "Kubernetes", "Object Storage", "Metadata"}})
		case strings.HasPrefix(r.URL.Path, "/linode/types/"):
			json.NewEncoder(w).Encode(map[string]any{"id": strings.TrimPrefix(r.URL.Path, "/linode/types/")})
		case strings.HasPrefix(r.URL.Path, "/images/"):
			json.NewEncoder(w).Encode(map[string]any{"id": strings.TrimPrefix(r.URL.Path, "/images/"), "capabilities": []string{"cloud-init"}})
		case r.Method == http.MethodGet:
			fmt.Fprint(w, `{"data":[],"page":1,"pages":1,"results":0}`)
		default:
			t.Errorf("unexpected mutation %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusBadRequest)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("RTK_CLOUD_TEST_MODE", "1")
	t.Setenv("RTK_CLOUD_LINODE_API_ROOT", server.URL)
	return &mutations
}

func TestFreshCreateProviderPlanFreezesMaintainedInputsAcrossEnvironments(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, operator := createProviderTestFixture(t, environment)
			mutations := createProviderTestAPI(t, nil)
			t.Setenv("LKE_CLUSTER_LABEL", "foreign-cluster")
			t.Setenv("LKE_NODE_COUNT", "99")
			t.Setenv("LKE_COTURN_VM_PUBLIC_IP", "198.51.100.77")
			plan, err := buildEnvironmentCreateProviderPlan(context.Background(), cfg, operator)
			if err != nil {
				t.Fatal(err)
			}
			if len(plan.Resources) != 5 || plan.AdditionalActiveServices != 4 || plan.Resources[0].Name != "video-cloud-"+environment+"-lke" || plan.Resources[0].Body["k8s_version"] != "1.34" {
				t.Fatalf("unfrozen provider plan: %+v", plan)
			}
			encoded, _ := json.Marshal(plan)
			if strings.Contains(string(encoded), operator["LINODE_TOKEN"]) || strings.Contains(string(encoded), "root_pass") || strings.Contains(string(encoded), "foreign-cluster") {
				t.Fatal("private or ambient input entered plan")
			}
			// Evidence round trips through private JSON before consumption.
			var decoded environmentCreateProviderPlan
			if json.Unmarshal(encoded, &decoded) != nil {
				t.Fatal("plan JSON roundtrip failed")
			}
			for _, pool := range createProviderObjectList(decoded.Resources[0].Body["node_pools"]) {
				labels, _ := pool["labels"].(map[string]any)
				class, _ := labels["rtk.io/node-class"].(string)
				if class == "" || pool["label"] != "video-cloud-"+environment+"-"+class {
					t.Fatal("LKE pool display label lacks its environment or changed logical placement")
				}
			}
			decoded.WorkloadAllocations = 7
			if err := qualifyEnvironmentCreateProviders(context.Background(), cfg, decoded, operator); err != nil {
				t.Fatal(err)
			}
			if *mutations != 0 {
				t.Fatal("qualification allocated provider resources")
			}
		})
	}
}

func TestFreshCreateProviderQualificationFailsClosedBeforeAllocation(t *testing.T) {
	for _, mode := range []string{"same-name", "owned-tag", "retained-volume", "denied", "partial-pagination", "changed-pagination", "quota", "missing-e3", "invalid-plan", "bad-ssh", "missing-vm-type"} {
		t.Run(mode, func(t *testing.T) {
			cfg, operator := createProviderTestFixture(t, "dev")
			mutations := createProviderTestAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
				if mode == "missing-vm-type" && r.URL.Path == "/linode/types/g6-nanode-1" {
					w.WriteHeader(http.StatusNotFound)
					return true
				}
				if mode == "denied" && r.URL.Path == "/linode/instances" {
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(w, "PRIVATE_PROVIDER_RESPONSE")
					return true
				}
				if r.URL.Path == "/lke/clusters" {
					switch mode {
					case "same-name":
						fmt.Fprint(w, `{"data":[{"id":777,"label":"video-cloud-dev-lke"}],"page":1,"pages":1,"results":1}`)
						return true
					case "owned-tag":
						fmt.Fprint(w, `{"data":[{"id":777,"label":"renamed","tags":["video-cloud-dev"]}],"page":1,"pages":1,"results":1}`)
						return true
					case "partial-pagination":
						fmt.Fprint(w, `{"data":[],"page":1,"pages":1,"results":1}`)
						return true
					case "changed-pagination":
						if r.URL.Query().Get("page") == "1" {
							fmt.Fprint(w, `{"data":[{"id":101}],"page":1,"pages":2,"results":2}`)
						} else {
							fmt.Fprint(w, `{"data":[{"id":102}],"page":2,"pages":2,"results":3}`)
						}
						return true
					}
				}
				if mode == "retained-volume" && r.URL.Path == "/volumes" {
					fmt.Fprint(w, `{"data":[{"id":777,"label":"retained","tags":["video-cloud-dev"]}],"page":1,"pages":1,"results":1}`)
					return true
				}
				if mode == "missing-e3" && r.URL.Path == "/object-storage/endpoints" {
					fmt.Fprint(w, `{"data":[{"region":"us-sea","endpoint_type":"E1"},{"region":"us-lax","endpoint_type":"E2"}],"page":1,"pages":1,"results":2}`)
					return true
				}
				return false
			})
			if mode == "bad-ssh" {
				operator["DEPLOYMENT_SSH_PUBLIC_KEY"] = "ssh-ed25519 ZmFrZQ=="
			}
			plan, err := buildEnvironmentCreateProviderPlan(context.Background(), cfg, operator)
			if mode != "bad-ssh" {
				if err != nil {
					t.Fatal(err)
				}
				if mode == "quota" {
					writeTestLKEAccount(t, "dev", "LKE_ACTIVE_SERVICE_LIMIT=4\n")
					plan.WorkloadAllocations = 1
				}
				if mode == "invalid-plan" {
					plan.Resources[0].Body["node_pools"] = "not-an-array"
				}
				err = qualifyEnvironmentCreateProviders(context.Background(), cfg, plan, operator)
			}
			if err == nil || strings.Contains(err.Error(), "PRIVATE_PROVIDER_RESPONSE") || *mutations != 0 {
				t.Fatalf("fail-closed mode %s: err=%v mutations=%d", mode, err, *mutations)
			}
		})
	}
}

func TestFreshCreateProviderReturnsImmediateExactReceiptAndNeverRetries(t *testing.T) {
	for _, mode := range []string{"created", "conflict", "invalid-response", "wrong-owner", "wrong-id", "cancelled", "transport-cancel"} {
		t.Run(mode, func(t *testing.T) {
			cfg, operator := createProviderTestFixture(t, "dev")
			calls := 0
			createProviderTestAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
				if r.Method == http.MethodPost && r.URL.Path == "/linode/instances" {
					calls++
					var body map[string]any
					json.NewDecoder(r.Body).Decode(&body)
					if body["root_pass"] != "persisted-private-bootstrap-password-1234" {
						t.Error("explicit persisted authentication not supplied")
					}
					if mode == "conflict" {
						w.WriteHeader(http.StatusBadRequest)
						fmt.Fprint(w, `{"errors":[{"reason":"label already taken PRIVATE"}]}`)
						return true
					}
					if mode == "invalid-response" {
						fmt.Fprint(w, "not-json")
						return true
					}
					if mode == "transport-cancel" {
						<-r.Context().Done()
						return true
					}
					tags := body["tags"]
					if mode == "wrong-owner" {
						tags = []string{"foreign"}
					}
					json.NewEncoder(w).Encode(map[string]any{"id": 123, "label": body["label"], "region": body["region"], "tags": tags, "status": "provisioning", "ipv4": []string{"198.51.100.12", "192.168.1.12"}})
					return true
				}
				if r.URL.Path == "/linode/instances/123" {
					id := 123
					if mode == "wrong-id" {
						id = 124
					}
					json.NewEncoder(w).Encode(map[string]any{"id": id, "label": "video-cloud-dev-edge-haproxy-01", "region": "us-sea", "tags": []string{"rtk-cloud", "video-cloud-dev", "dev", "edge-haproxy"}, "status": "running", "ipv4": []string{"198.51.100.12", "192.168.1.12"}})
					return true
				}
				return false
			})
			plan, err := buildEnvironmentCreateProviderPlan(context.Background(), cfg, operator)
			if err != nil {
				t.Fatal(err)
			}
			store, err := newSecretStore("", cfg.Environment)
			if err != nil {
				t.Fatal(err)
			}
			material, err := prepareEnvironmentCreateProviderMaterial(context.Background(), cfg, strings.Repeat("a", 64), plan.Resources[1], store)
			if err != nil {
				t.Fatal(err)
			}
			material["root_pass"] = "persisted-private-bootstrap-password-1234"
			ctx := context.Background()
			cancel := func() {}
			if mode == "cancelled" || mode == "transport-cancel" {
				var stop context.CancelFunc
				ctx, stop = context.WithCancel(ctx)
				cancel = stop
				if mode == "cancelled" {
					stop()
				}
			}
			defer cancel()
			if mode == "transport-cancel" {
				ctx, cancel = context.WithTimeout(ctx, 10*1000*1000)
				defer cancel()
			}
			receipt, err := executeEnvironmentCreateProviderResource(ctx, cfg, plan.Resources[1], operator, material)
			if mode == "created" || mode == "wrong-id" {
				if err != nil || receipt.NativeID != "123" || receipt.Status != "provisioning" || calls != 1 {
					t.Fatalf("immediate receipt unavailable: %+v %v calls=%d", receipt, err, calls)
				}
				actual, ready, err := verifyEnvironmentCreateProviderResource(context.Background(), cfg, plan.Resources[1], receipt, operator)
				if mode == "created" && (err != nil || !ready || actual.NativeID != "123") || mode == "wrong-id" && err == nil {
					t.Fatalf("native exact ID verification failed: %+v ready=%t err=%v", actual, ready, err)
				}
			} else {
				if err == nil || strings.Contains(err.Error(), "PRIVATE") || calls > 1 {
					t.Fatalf("uncertain allocation retried: receipt=%+v err=%v calls=%d", receipt, err, calls)
				}
				if mode == "wrong-owner" && receipt.NativeID != "123" {
					t.Fatal("known partial allocation ID discarded")
				}
				if mode == "cancelled" && calls != 0 {
					t.Fatal("cancelled request allocated infrastructure")
				}
			}
		})
	}
}

func TestFreshCreateProviderNativeReadErrorsCannotBecomeAbsence(t *testing.T) {
	for _, status := range []int{http.StatusNotFound, http.StatusForbidden, http.StatusServiceUnavailable} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			createProviderTestAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
				w.WriteHeader(status)
				fmt.Fprint(w, "private-response")
				return true
			})
			_, err := environmentCreateProviderList(context.Background(), "private-provider-token", "/lke/clusters")
			var native *environmentCreateProviderHTTPError
			if !errors.As(err, &native) || native.Status != status || strings.Contains(err.Error(), "private-response") {
				t.Fatalf("untyped or leaked native failure: %v", err)
			}
		})
	}
}
