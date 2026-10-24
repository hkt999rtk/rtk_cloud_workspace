package main

import (
	"bytes"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type providerStorageExecutionContract struct {
	cfg                  deploymentConfig
	store                secretStore
	e                    *environmentCreateExecution
	plan                 environmentCreatePlan
	operator             map[string]string
	originals            map[string][]byte
	mu                   sync.Mutex
	keys                 []map[string]any
	responses            map[string][]byte
	objects              map[string][]byte
	calls                []string
	posts, puts, deletes int
	mode                 string
	injected             bool
}

func newProviderStorageExecutionContract(t *testing.T, mode string, prepares ...func(*deploymentConfig, *environmentCreatePlan) []environmentReleasePublicationProof) *providerStorageExecutionContract {
	t.Helper()
	cfg, _, _, operator, bootstrap := createDependencyContractFixture(t)
	cfg.Values["PKI_REQUIRE_USER_MFA"] = "false"
	cfg.Storage = deploymentStoragePlan{RuntimeMedia: deploymentStorageTarget{Purpose: "runtime-media", Bucket: "video-cloud-dev-bucket-runtime-media", Region: "us-sea", Prefix: "dev"}, OTAFirmware: deploymentStorageTarget{Purpose: "ota-firmware", Bucket: "video-cloud-dev-bucket-ota-firmware", Region: "us-sea", Prefix: "dev"}, OTAMode: "dedicated"}
	for k, v := range map[string]string{"LINODE_TOKEN": "isolated-storage-provider-token", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL": "operator@isolated.test", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD": "isolated-original-admin", "PKI_OPERATOR_SIGNER_REF": "isolated-original-offline-signer"} {
		operator[k] = v
	}
	if len(prepares) > 1 {
		t.Fatal("one optional original provider publication preparation required")
	}
	var prepare func(*deploymentConfig, *environmentCreatePlan) []environmentReleasePublicationProof
	if len(prepares) == 1 {
		prepare = prepares[0]
	}
	cfg, store, e, _, plan := providerExecutionContractFixture(t, providerExecutionContractInputs{cfg: cfg, operator: operator, bootstrap: bootstrap, prepare: prepare})
	// The general store helper writes an empty inventory marker. This fixture
	// represents the provider-only phase before inventory/runtime initialization.
	entries, err := os.ReadDir(filepath.Join(store.Root, "runtime"))
	if err != nil || len(entries) != 0 {
		t.Fatal("provider-only fixture unexpectedly contains initialized runtime", err)
	}
	if err := os.Remove(filepath.Join(store.Root, "inventory.json")); err != nil {
		t.Fatal(err)
	}
	f := &providerStorageExecutionContract{cfg: cfg, store: store, e: e, plan: plan, operator: operator, originals: map[string][]byte{}, responses: map[string][]byte{}, objects: map[string][]byte{}, mode: mode}
	for _, purpose := range []string{"MEDIA", "OTA"} {
		for _, suffix := range []string{"ACCESS_KEY_ID", "SECRET_ACCESS_KEY"} {
			name := "LINODE_" + purpose + "_OBJ_" + suffix
			f.originals[name] = []byte(operator[name])
		}
	}
	kube := map[string]any{"apiVersion": "v1", "kind": "Config", "current-context": "original", "contexts": []any{map[string]any{"name": "original", "context": map[string]any{"cluster": "original", "user": "original"}}}, "clusters": []any{map[string]any{"name": "original", "cluster": map[string]any{"server": "https://isolated.invalid:6443", "certificate-authority-data": base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}}}, "users": []any{map[string]any{"name": "original", "user": map[string]any{"token": "isolated-original-kubernetes-token"}}}}
	raw, err := yaml.Marshal(kube)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.write("kube/kubeconfig.yaml", raw, false); err != nil {
		t.Fatal(err)
	}
	for i := range e.Journal.Entries {
		if e.Journal.Entries[i].Key == "provider-kube-access" {
			var receipt map[string]string
			if json.Unmarshal(e.Journal.Entries[i].Receipt, &receipt) != nil {
				t.Fatal("original kube receipt")
			}
			receipt["kubeconfig_sha256"] = environmentPreflightBytesSHA256(raw)
			e.Journal.Entries[i].Receipt = mustProviderRecoveryJSON(receipt)
		}
	}
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	account, err := deploymentLKEStatePath("dev", "account.env")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(store.Root, account)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.write(rel, []byte("LKE_ACTIVE_SERVICE_LIMIT=unlimited\n"), false); err != nil {
		t.Fatal(err)
	}
	f.installNative(t)
	for i, target := range []deploymentStorageTarget{cfg.Storage.RuntimeMedia, cfg.Storage.OTAFirmware} {
		prefix := []string{"LINODE_MEDIA_OBJ_", "LINODE_OTA_OBJ_"}[i]
		f.keys = append(f.keys, map[string]any{"id": i + 1, "access_key": operator[prefix+"ACCESS_KEY_ID"], "bucket_access": []map[string]string{{"bucket_name": target.Bucket, "region": target.Region, "permissions": "read_write"}}})
	}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.calls = append(f.calls, r.Method+" "+r.URL.Path)
		emit := func(v any) { w.Header().Set("Content-Type", "application/json"); _ = json.NewEncoder(w).Encode(v) }
		if strings.HasPrefix(r.URL.Path, "/video-cloud-dev-bucket-") {
			f.s3(t, w, r)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+operator["LINODE_TOKEN"] {
			t.Error("native provider authentication changed")
			w.WriteHeader(401)
			return
		}
		if r.URL.Path == "/object-storage/keys" {
			if r.Method == http.MethodGet {
				emit(map[string]any{"data": f.keys, "page": 1, "pages": 1, "results": len(f.keys)})
				return
			}
			if r.Method != http.MethodPost {
				t.Error("unexpected provider key mutation", r.Method)
				w.WriteHeader(405)
				return
			}
			var request struct {
				Label  string              `json:"label"`
				Grants []map[string]string `json:"bucket_access"`
			}
			body, _ := io.ReadAll(r.Body)
			if json.Unmarshal(body, &request) != nil || len(request.Grants) != 1 {
				t.Error("invalid native limited-key intent")
				w.WriteHeader(400)
				return
			}
			purpose := "media"
			target := cfg.Storage.RuntimeMedia
			if request.Grants[0]["bucket_name"] == cfg.Storage.OTAFirmware.Bucket {
				purpose = "ota"
				target = cfg.Storage.OTAFirmware
			}
			label := "rtk-cloud-dev-new-" + purpose + "-" + e.Journal.PlanSHA256[:16]
			if request.Label != label || request.Grants[0]["region"] != target.Region || request.Grants[0]["permissions"] != "read_write" || request.Grants[0]["bucket_name"] != target.Bucket {
				t.Error("key request escaped selected bucket")
				w.WriteHeader(400)
				return
			}
			intent, _ := os.ReadFile(filepath.Join(store.Root, "deployment/create/recovery-storage-keys", e.Journal.PlanSHA256, purpose, "create-intent.json"))
			if !bytes.Equal(intent, body) {
				t.Error("native key write preceded durable exact intent")
				w.WriteHeader(400)
				return
			}
			if _, exists := f.responses[purpose]; exists {
				t.Error("duplicate native key allocation")
				w.WriteHeader(409)
				return
			}
			f.posts++
			created := map[string]any{"id": 100 + f.posts, "label": label, "access_key": "isolated-new-" + purpose + "-access", "secret_key": "isolated-new-" + purpose + "-secret"}
			f.responses[purpose] = mustProviderRecoveryJSON(created)
			grants := request.Grants
			if mode == "wrong-native-grant" {
				grants = append(grants, map[string]string{"bucket_name": "foreign", "region": target.Region, "permissions": "read_write"})
			}
			f.keys = append(f.keys, map[string]any{"id": created["id"], "access_key": created["access_key"], "bucket_access": grants})
			if mode == "lost-native-response" && !f.injected {
				f.injected = true
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				}
				return
			}
			w.Write(f.responses[purpose])
			return
		}
		items := []map[string]any{}
		for i, resource := range plan.Provider.Resources {
			id := 101 + i
			item := map[string]any{"id": id, "label": resource.Name, "region": resource.Region, "status": "running", "ipv4": []string{"203.0.113." + strconv.Itoa(10+i), "192.168.1." + strconv.Itoa(10+i)}}
			exact := resource.Path + "/" + strconv.Itoa(id)
			if resource.Kind == "bucket" {
				item["s3_endpoint"] = server.URL
				exact = resource.Path + "/" + resource.Region + "/" + resource.Name
			}
			if r.URL.Path == exact {
				if mode == "changed-provider" {
					item["label"] = "foreign"
				}
				emit(item)
				return
			}
			if r.URL.Path == resource.Path {
				items = append(items, item)
			}
		}
		switch r.URL.Path {
		case "/lke/clusters", "/linode/instances", "/object-storage/buckets", "/networking/firewalls", "/vpcs", "/volumes", "/nodebalancers":
			emit(map[string]any{"data": items, "page": 1, "pages": 1, "results": len(items)})
		default:
			t.Error("unexpected provider path", r.Method, r.URL.Path)
			w.WriteHeader(404)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("RTK_CLOUD_LINODE_API_ROOT", server.URL)
	return f
}

func (f *providerStorageExecutionContract) installNative(t *testing.T) {
	t.Helper()
	dir := t.TempDir()
	features := []string{"deployment-bootstrap-finalize-v1", "first-trust-v1", "create-service-enrollment-v1", "registry-only-issuance-v1", "fresh-bootstrap-trust-v1", "mqtt-bundle-bootstrap-v1", "owned-server-export-v1", "managed-callback-export-v1", "managed-test-lab-callback-v1", "service-registration-bootstrap-ack-v1"}
	capabilities := map[string]any{}
	for _, name := range []string{"rtk-account-manager", "pkimanagement", "certissuer", "api", "logingester", "pkibroker", "serviceidentity-bootstrap", "emqxpkihost", "mqttfoundation", "shadowworker", "otaservice", "videostorage", "webrtcservice", "cloudlogger"} {
		service := name
		if name == "rtk-account-manager" {
			service = "account-manager"
		}
		capabilities[name] = map[string]any{"schema": "rtk-cloud.deployment-capabilities/v1", "service": service, "features": features}
	}
	if err := os.WriteFile(filepath.Join(dir, "capabilities.json"), mustProviderRecoveryJSON(capabilities), 0600); err != nil {
		t.Fatal(err)
	}
	script := `#!/usr/bin/env python3
import json,os,pathlib,sys
args=sys.argv[1:]
if pathlib.Path(sys.argv[0]).name=='kubectl':
 assert args[-4:]==['get','namespaces','-o','json']
 print(json.dumps({'apiVersion':'v1','kind':'NamespaceList','items':[{'kind':'Namespace','metadata':{'name':'kube-system'}}]}))
else:
 assert args[0:2]==['run','--rm'] and '--network' in args and args[args.index('--network')+1]=='none' and '--read-only' in args
 binary=args[args.index('--entrypoint')+1]
 assert any('@sha256:' in v for v in args)
 if binary!='/bin/sh':
  assert args[-1]=='--deployment-capabilities'
  print(json.dumps(json.loads(pathlib.Path(os.environ['RTK_STORAGE_KEY_CAPABILITIES']).read_text())[pathlib.Path(binary).name]))
`
	for _, name := range []string{"docker", "kubectl"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("RTK_STORAGE_KEY_CAPABILITIES", filepath.Join(dir, "capabilities.json"))
	t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(dir, "kubectl"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func (f *providerStorageExecutionContract) s3(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	body, _ := io.ReadAll(r.Body)
	parts := strings.Split(r.Header.Get("Authorization"), ", ")
	if len(parts) != 3 {
		t.Error("S3 request is unsigned")
		w.WriteHeader(401)
		return
	}
	credential := strings.TrimPrefix(parts[0], "AWS4-HMAC-SHA256 Credential=")
	scope := strings.SplitN(credential, "/", 2)
	if len(scope) != 2 {
		t.Error("invalid S3 credential scope")
		w.WriteHeader(401)
		return
	}
	purpose := "media"
	bucket := f.cfg.Storage.RuntimeMedia.Bucket
	if strings.HasPrefix(r.URL.Path, "/"+f.cfg.Storage.OTAFirmware.Bucket) {
		purpose = "ota"
		bucket = f.cfg.Storage.OTAFirmware.Bucket
	}
	if scope[0] != "isolated-new-"+purpose+"-access" {
		t.Error("S3 canary did not use exact new limited key")
		w.WriteHeader(401)
		return
	}
	amzDate := r.Header.Get("X-Amz-Date")
	if len(amzDate) != 16 || scope[1] != amzDate[:8]+"/us-sea/s3/aws4_request" {
		t.Error("wrong S3 signing scope")
		w.WriteHeader(401)
		return
	}
	headers := "host:" + r.Host + "\nx-amz-content-sha256:" + r.Header.Get("X-Amz-Content-Sha256") + "\nx-amz-date:" + amzDate + "\n"
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonical := strings.Join([]string{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, headers, signedHeaders, provisionHexSHA256(body)}, "\n")
	sign := strings.Join([]string{"AWS4-HMAC-SHA256", amzDate, scope[1], provisionHexSHA256([]byte(canonical))}, "\n")
	signature := hex.EncodeToString(provisionHMACSHA256(provisionSigningKey("isolated-new-"+purpose+"-secret", amzDate[:8], "us-sea"), []byte(sign)))
	if parts[1] != "SignedHeaders="+signedHeaders || parts[2] != "Signature="+signature {
		t.Error("S3 signature does not authenticate the original request/body")
		w.WriteHeader(401)
		return
	}
	switch {
	case r.URL.Query().Has("acl"):
		io.WriteString(w, privateStorageACLForTest)
	case r.URL.Query().Has("policy"):
		io.WriteString(w, `{"Statement":[]}`)
	case r.URL.Query().Get("list-type") == "2":
		io.WriteString(w, `<ListBucketResult/>`)
	default:
		if !strings.HasPrefix(r.URL.Path, "/"+bucket+"/dev/__rtk_cloud_validation__/") {
			t.Error("canary escaped its scoped prefix")
			w.WriteHeader(400)
			return
		}
		switch r.Method {
		case http.MethodPut:
			f.puts++
			f.objects[r.URL.Path] = append([]byte(nil), body...)
		case http.MethodGet:
			if f.mode == "canary-read-interruption" && !f.injected {
				f.injected = true
				io.WriteString(w, "unreadable-original-response")
				return
			}
			w.Write(f.objects[r.URL.Path])
		case http.MethodDelete:
			f.deletes++
			delete(f.objects, r.URL.Path)
			if (f.mode == "concurrent-drift" || f.mode == "partial-promotion") && purpose == "ota" && !f.injected {
				f.injected = true
				if err := os.WriteFile(filepath.Join(f.store.Root, "operator/env/LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY"), []byte("concurrent-foreign-value"), 0600); err != nil {
					t.Error(err)
				}
			}
		default:
			t.Error("unexpected S3 operation", r.Method)
			w.WriteHeader(405)
		}
	}
}

func TestProviderStorageKeyExecutionContractPreservesOriginalsAndReconciles(t *testing.T) {
	for _, mode := range []string{"complete", "canary-read-interruption", "lost-native-response", "wrong-native-grant", "concurrent-drift", "partial-promotion", "changed-provider"} {
		t.Run(mode, func(t *testing.T) {
			f := newProviderStorageExecutionContract(t, mode)
			original := f.e.Journal.PlanSHA256
			journal := readTestFile(t, f.e.JournalPath)
			owner := readTestFile(t, filepath.Join(f.store.Root, "deployment/.execution-active/owner.json"))
			err := establishProviderRecoveryStorageKeys(t.Context(), f.cfg, f.store, f.operator, f.e, f.plan, original)
			if mode == "complete" {
				if err != nil {
					t.Fatal("complete storage establishment rejected", err)
				}
			} else if err == nil {
				t.Fatal("injected interruption ignored", mode)
			}
			f.mu.Lock()
			firstPosts := f.posts
			firstPuts := f.puts
			f.mu.Unlock()
			if mode != "complete" {
				for name, want := range f.originals {
					raw, _ := os.ReadFile(filepath.Join(f.store.Root, "operator/env", name))
					if (mode == "concurrent-drift" || mode == "partial-promotion") && strings.HasSuffix(name, "MEDIA_OBJ_SECRET_ACCESS_KEY") {
						if string(raw) != "concurrent-foreign-value" {
							t.Fatal("concurrent value overwritten")
						}
						continue
					}
					if !bytes.Equal(raw, want) {
						t.Fatal("unvalidated promotion", name)
					}
				}
			}
			switch mode {
			case "changed-provider":
				if firstPosts != 0 || firstPuts != 0 {
					t.Fatal("changed allocation reached storage mutation")
				}
			case "lost-native-response", "wrong-native-grant":
				if firstPosts != 1 || firstPuts != 0 {
					t.Fatal("unresolved key response reached S3")
				}
				err = establishProviderRecoveryStorageKeys(t.Context(), f.cfg, f.store, f.operator, f.e, f.plan, original)
				if err == nil {
					t.Fatal("unresolved native allocation became retry permission")
				}
				f.mu.Lock()
				posts := f.posts
				f.mu.Unlock()
				if posts != 1 {
					t.Fatal("uncertain intent allocated a duplicate")
				}
			default:
				if mode == "concurrent-drift" || mode == "partial-promotion" {
					if err := os.WriteFile(filepath.Join(f.store.Root, "operator/env/LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY"), f.originals["LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY"], 0600); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "partial-promotion" {
					if err := writeProviderStorageKeyValue(f.store, "LINODE_MEDIA_OBJ_ACCESS_KEY_ID", f.originals["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"], []byte("isolated-new-media-access\n")); err != nil {
						t.Fatal(err)
					}
				}
				if err := establishProviderRecoveryStorageKeys(t.Context(), f.cfg, f.store, f.operator, f.e, f.plan, original); err != nil {
					t.Fatal("exact retained key response could not resume", err)
				}
				f.mu.Lock()
				posts, objects := f.posts, len(f.objects)
				f.mu.Unlock()
				if posts != 2 || objects != 0 {
					t.Fatal("resume repeated allocation or left canary", posts, objects)
				}
				for _, purpose := range []string{"media", "ota"} {
					for _, suffix := range []string{"ACCESS_KEY_ID", "SECRET_ACCESS_KEY"} {
						name := "LINODE_" + strings.ToUpper(purpose) + "_OBJ_" + suffix
						got, _ := os.ReadFile(filepath.Join(f.store.Root, "operator/env", name))
						value := "access"
						if suffix == "SECRET_ACCESS_KEY" {
							value = "secret"
						}
						if string(got) != "isolated-new-"+purpose+"-"+value+"\n" {
							t.Fatal("wrong promoted credential", name)
						}
						archive, _ := os.ReadFile(filepath.Join(f.store.Root, "deployment/create/recovery-storage-keys", original, purpose, "original", name))
						if !bytes.Equal(archive, f.originals[name]) {
							t.Fatal("original credential changed", name)
						}
					}
				}
				var completed struct {
					Environment string           `json:"environment"`
					ValidatedAt time.Time        `json:"validated_at"`
					CompletedAt time.Time        `json:"completed_at"`
					Original    string           `json:"original_plan_sha256"`
					Revoked     bool             `json:"old_keys_revoked"`
					Keys        []map[string]any `json:"keys"`
				}
				if createOpenBaoACKCompletionRead(f.store, filepath.Join("deployment/create/recovery-storage-keys", original, "completed.json"), &completed) != nil || completed.Environment != "dev" || completed.ValidatedAt.IsZero() || completed.CompletedAt.Before(completed.ValidatedAt) || completed.Original != original || completed.Revoked || len(completed.Keys) != 2 {
					t.Fatal("completion lost original key retention evidence")
				}
			}
			if readTestFile(t, f.e.JournalPath) != journal || readTestFile(t, filepath.Join(f.store.Root, "deployment/.execution-active/owner.json")) != owner {
				t.Fatal("storage establishment rewrote original deployment evidence")
			}
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, call := range f.calls {
				if strings.HasPrefix(call, "DELETE /object-storage/keys") {
					t.Fatal("original native key revoked")
				}
			}
		})
	}
}
