package main

import (
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

// isolatedFreshProvider is a stateful loopback-only Linode/S3 substitute.
// It owns no deployment journal and fails closed on unknown operations.
type isolatedFreshProvider struct {
	server     *httptest.Server
	s3Server   *httptest.Server
	mu         sync.Mutex
	token      string
	kubeconfig []byte
	targets    map[string]deploymentStorageTarget
	secrets    map[string]string
	keys       []map[string]any
	resources  map[string][]map[string]any
	objects    map[string][]byte
	calls      []string
	nextID     int
}

type isolatedProviderResponse struct {
	http.ResponseWriter
	status int
}

func (w *isolatedProviderResponse) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *isolatedProviderResponse) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

// safeRequestLog is test-only diagnostics: never include credentials, headers,
// query strings or request/response bodies. Call only on this synthetic server.
func (f *isolatedFreshProvider) safeRequestLog() []byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return []byte(strings.Join(f.calls, "\n") + "\n")
}

func newIsolatedFreshProvider(t *testing.T, cfg deploymentConfig, operator map[string]string, kubeconfig []byte) *isolatedFreshProvider {
	t.Helper()
	if cfg.Environment != "dev" || operator["LINODE_TOKEN"] != "isolated-provider-token" || len(kubeconfig) == 0 {
		t.Fatal("isolated provider requires synthetic dev credentials and kubeconfig")
	}
	f := &isolatedFreshProvider{token: operator["LINODE_TOKEN"], kubeconfig: append([]byte(nil), kubeconfig...), targets: map[string]deploymentStorageTarget{}, secrets: map[string]string{}, resources: map[string][]map[string]any{}, objects: map[string][]byte{}, nextID: 100}
	for i, target := range []deploymentStorageTarget{cfg.Storage.RuntimeMedia, cfg.Storage.OTAFirmware} {
		access, secret := environmentCreateStorageCredentials(operator, target.Purpose)
		if target.Bucket == "" || target.Region == "" || access == "" || secret == "" || f.targets[target.Bucket].Bucket != "" {
			t.Fatal("isolated provider requires two distinct scoped storage credentials")
		}
		f.targets[target.Bucket] = target
		f.secrets[access] = secret
		f.keys = append(f.keys, map[string]any{"id": i + 1, "access_key": access, "bucket_access": []any{map[string]string{"bucket_name": target.Bucket, "region": target.Region, "permissions": "read_write"}}})
	}
	f.server = httptest.NewServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.server.Close)
	// S3 uses a real loopback TLS server; the default httptest certificate is
	// the same explicit test-only trust anchor used by the ACME fixture.
	f.s3Server = httptest.NewTLSServer(http.HandlerFunc(f.serveHTTP))
	t.Cleanup(f.s3Server.Close)
	t.Setenv("RTK_CLOUD_TEST_MODE", "1")
	t.Setenv("RTK_CLOUD_LINODE_API_ROOT", f.server.URL)
	return f
}

func (f *isolatedFreshProvider) serveHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	response := &isolatedProviderResponse{ResponseWriter: w}
	w = response
	defer func() {
		status := response.status
		if status == 0 {
			status = http.StatusOK
		}
		path := r.URL.EscapedPath()
		if len(path) > 512 {
			path = "<oversized>"
		}
		f.calls = append(f.calls, fmt.Sprintf("%s %s %d", r.Method, path, status))
	}()
	for bucket, target := range f.targets {
		if r.URL.Path == "/"+bucket || strings.HasPrefix(r.URL.Path, "/"+bucket+"/") {
			f.serveS3(w, r, target)
			return
		}
	}
	if r.Header.Get("Authorization") != "Bearer "+f.token {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	emit := func(v any) { _ = json.NewEncoder(w).Encode(v) }
	list := func(values any, n int) { emit(map[string]any{"data": values, "page": 1, "pages": 1, "results": n}) }
	path := r.URL.Path
	if r.Method == http.MethodGet {
		switch {
		case path == "/lke/versions":
			list([]any{map[string]string{"id": "1.34"}}, 1)
			return
		case path == "/object-storage/endpoints":
			seen := map[string]bool{}
			var endpoints []any
			for _, target := range f.targets {
				for _, kind := range []string{"E1", "E3"} {
					key := target.Region + "/" + kind
					if !seen[key] {
						endpoints = append(endpoints, map[string]string{"region": target.Region, "endpoint_type": kind})
						seen[key] = true
					}
				}
			}
			list(endpoints, len(endpoints))
			return
		case path == "/object-storage/keys":
			list(f.keys, len(f.keys))
			return
		case strings.HasPrefix(path, "/regions/"):
			emit(map[string]any{"id": strings.TrimPrefix(path, "/regions/"), "status": "ok", "capabilities": []string{"Linodes", "Kubernetes", "Object Storage", "Metadata"}})
			return
		case strings.HasPrefix(path, "/linode/types/"):
			emit(map[string]string{"id": strings.TrimPrefix(path, "/linode/types/")})
			return
		case strings.HasPrefix(path, "/images/"):
			emit(map[string]any{"id": strings.TrimPrefix(path, "/images/"), "capabilities": []string{"cloud-init"}})
			return
		case strings.HasSuffix(path, "/kubeconfig") && strings.HasPrefix(path, "/lke/clusters/"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/lke/clusters/"), "/kubeconfig")
			if f.find("/lke/clusters", id) == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			emit(map[string]string{"kubeconfig": base64.StdEncoding.EncodeToString(f.kubeconfig)})
			return
		case strings.HasSuffix(path, "/pools") && strings.HasPrefix(path, "/lke/clusters/"):
			id := strings.TrimSuffix(strings.TrimPrefix(path, "/lke/clusters/"), "/pools")
			if f.find("/lke/clusters", id) == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			values := f.resources["/lke/pools/"+id]
			if values == nil {
				values = []map[string]any{}
			}
			list(values, len(values))
			return
		case strings.HasSuffix(path, "/access") && strings.HasPrefix(path, "/object-storage/buckets/"):
			if f.findBucket(strings.TrimSuffix(path, "/access")) == nil {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			emit(map[string]string{"acl": "private", "acl_xml": `<AccessControlPolicy><Owner><ID>isolated-owner</ID></Owner><AccessControlList></AccessControlList></AccessControlPolicy>`})
			return
		}
		for _, collection := range []string{"/lke/clusters", "/linode/instances", "/networking/firewalls", "/vpcs", "/object-storage/buckets", "/volumes", "/nodebalancers"} {
			if path == collection {
				values := f.resources[collection]
				if values == nil {
					values = []map[string]any{}
				}
				list(values, len(values))
				return
			}
			if strings.HasPrefix(path, collection+"/") {
				var item map[string]any
				if collection == "/object-storage/buckets" {
					item = f.findBucket(path)
				} else {
					item = f.find(collection, strings.TrimPrefix(path, collection+"/"))
				}
				if item == nil {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				emit(item)
				return
			}
		}
	}
	if r.Method == http.MethodPost && (path == "/lke/clusters" || path == "/linode/instances" || path == "/object-storage/buckets") {
		var body map[string]any
		if json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body) != nil || body["label"] == nil || body["region"] == nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		for _, item := range f.resources[path] {
			if item["label"] == body["label"] {
				w.WriteHeader(http.StatusConflict)
				return
			}
		}
		f.nextID++
		item := map[string]any{"id": f.nextID, "label": body["label"], "region": body["region"], "tags": body["tags"], "status": "running"}
		if path == "/lke/clusters" {
			version, ok := body["k8s_version"].(string)
			pools, poolsOK := body["node_pools"].([]any)
			if !ok || !validEnvironmentCreateKubernetesVersion(version) || !poolsOK || len(pools) == 0 {
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			item["k8s_version"] = version
			poolPath := "/lke/pools/" + strconv.Itoa(f.nextID)
			for _, raw := range pools {
				pool, ok := raw.(map[string]any)
				count, countOK := pool["count"].(float64)
				labels, labelsOK := pool["labels"].(map[string]any)
				if !ok || !countOK || count < 1 || count > 100 || count != float64(int(count)) || !labelsOK || labels["rtk.io/node-class"] == "" || pool["label"] == "" {
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				f.nextID++
				syntheticPool := map[string]any{"id": f.nextID, "label": pool["label"], "labels": labels}
				var nodes []any
				for i := 0; i < int(count); i++ {
					f.nextID++
					nodes = append(nodes, map[string]any{"instance_id": f.nextID})
					f.resources["/linode/instances"] = append(f.resources["/linode/instances"], map[string]any{"id": f.nextID, "label": fmt.Sprintf("isolated-worker-%d", f.nextID), "ipv4": []string{fmt.Sprintf("198.51.100.%d", f.nextID%200+1)}, "region": body["region"], "status": "running", "isolated_worker": true})
				}
				syntheticPool["nodes"] = nodes
				f.resources[poolPath] = append(f.resources[poolPath], syntheticPool)
			}
		}
		if path == "/linode/instances" {
			item["ipv4"] = []string{"198.51.100." + strconv.Itoa(f.nextID%200+1), "10.0.0." + strconv.Itoa(f.nextID%200+1)}
		}
		if path == "/object-storage/buckets" {
			item["s3_endpoint"], item["endpoint_type"] = f.s3Server.URL, body["endpoint_type"]
		}
		f.resources[path] = append(f.resources[path], item)
		emit(item)
		return
	}
	if r.Method == http.MethodPut && strings.HasPrefix(path, "/linode/instances/") {
		item := f.find("/linode/instances", strings.TrimPrefix(path, "/linode/instances/"))
		var body map[string]string
		if item == nil || item["isolated_worker"] != true || json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body) != nil || len(body) != 1 || body["label"] == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		item["label"] = body["label"]
		emit(item)
		return
	}
	w.WriteHeader(http.StatusNotFound)
}

func (f *isolatedFreshProvider) find(collection, id string) map[string]any {
	for _, item := range f.resources[collection] {
		if fmt.Sprint(item["id"]) == id {
			return item
		}
	}
	return nil
}

func (f *isolatedFreshProvider) findBucket(path string) map[string]any {
	for _, item := range f.resources["/object-storage/buckets"] {
		if path == "/object-storage/buckets/"+fmt.Sprint(item["region"])+"/"+fmt.Sprint(item["label"]) {
			return item
		}
	}
	return nil
}

func (f *isolatedFreshProvider) serveS3(w http.ResponseWriter, r *http.Request, target deploymentStorageTarget) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	parts := strings.Split(r.Header.Get("Authorization"), ", ")
	if len(parts) != 3 {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	credential := strings.TrimPrefix(parts[0], "AWS4-HMAC-SHA256 Credential=")
	scope := strings.SplitN(credential, "/", 2)
	if len(scope) != 2 || f.secrets[scope[0]] == "" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	access, secret := scope[0], f.secrets[scope[0]]
	allowed := false
	for _, key := range f.keys {
		if key["access_key"] == access {
			grant := key["bucket_access"].([]any)[0].(map[string]string)
			allowed = grant["bucket_name"] == target.Bucket && grant["region"] == target.Region
		}
	}
	date := r.Header.Get("X-Amz-Date")
	if !allowed || len(date) != 16 || scope[1] != date[:8]+"/"+target.Region+"/s3/aws4_request" {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	headers := "host:" + r.Host + "\nx-amz-content-sha256:" + r.Header.Get("X-Amz-Content-Sha256") + "\nx-amz-date:" + date + "\n"
	signed := "host;x-amz-content-sha256;x-amz-date"
	canonical := strings.Join([]string{r.Method, r.URL.EscapedPath(), r.URL.RawQuery, headers, signed, provisionHexSHA256(body)}, "\n")
	message := strings.Join([]string{"AWS4-HMAC-SHA256", date, scope[1], provisionHexSHA256([]byte(canonical))}, "\n")
	signature := hex.EncodeToString(provisionHMACSHA256(provisionSigningKey(secret, date[:8], target.Region), []byte(message)))
	if parts[1] != "SignedHeaders="+signed || parts[2] != "Signature="+signature {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	if r.URL.Query().Has("policy") {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	if r.URL.Query().Get("list-type") == "2" {
		_, _ = io.WriteString(w, `<ListBucketResult/>`)
		return
	}
	prefix := "/" + target.Bucket + "/" + strings.Trim(target.Prefix, "/") + "/"
	relative := strings.TrimPrefix(r.URL.Path, prefix)
	parts = strings.Split(relative, "/")
	validPreflight := len(parts) == 2 && parts[0] == "__rtk_cloud_validation__" && parts[1] != ""
	validCreate := len(parts) == 3 && parts[0] == "__rtk_cloud_create_validation__" && environmentPreflightDigest.MatchString(parts[1]) && parts[2] == target.Purpose
	if !strings.HasPrefix(r.URL.Path, prefix) || (!validPreflight && !validCreate) {
		w.WriteHeader(http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodPut:
		f.objects[r.URL.Path] = append([]byte(nil), body...)
	case http.MethodGet:
		object, ok := f.objects[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(object)
	case http.MethodDelete:
		delete(f.objects, r.URL.Path)
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func TestIsolatedFreshProviderRunsNativeCreateCanaryAndPreservesIntent(t *testing.T) {
	cfg, operator := createProviderTestFixture(t, "dev")
	operator["LINODE_TOKEN"] = "isolated-provider-token"
	backend := newIsolatedFreshProvider(t, cfg, operator, []byte("isolated-kubeconfig"))
	stateCfg, _, operatorSHA, artifact := createStateFixture(t, "dev")
	execution, err := startEnvironmentCreateExecution(t.Context(), stateCfg, artifact, operatorSHA, time.Now().UTC())
	if err != nil {
		t.Fatal("synthetic create owner", err)
	}
	t.Cleanup(func() { _ = execution.Close() })
	target := cfg.Storage.RuntimeMedia
	access, secret := environmentCreateStorageCredentials(operator, target.Purpose)
	s3 := provisionObjectStore{bucket: target.Bucket, region: target.Region, endpoint: backend.s3Server.URL, accessKey: access, secretKey: secret}
	checker := defaultDeploymentCredentialChecker().withCheckContext(t.Context())
	checker.client = backend.s3Server.Client()
	object := strings.Trim(target.Prefix, "/") + "/__rtk_cloud_create_validation__/" + execution.Journal.PlanSHA256 + "/" + target.Purpose
	for _, invalid := range []string{
		strings.Trim(target.Prefix, "/") + "/__rtk_cloud_create_validation__/not-a-plan/" + target.Purpose,
		strings.Trim(target.Prefix, "/") + "/__rtk_cloud_create_validation__/" + execution.Journal.PlanSHA256 + "/ota-firmware",
	} {
		_, err := provisionSignedObjectRequestWithClient(checker.client, s3, http.MethodGet, invalid, nil, nil)
		var forbidden *provisionObjectStorageHTTPError
		if !errors.As(err, &forbidden) || forbidden.StatusCode != http.StatusForbidden {
			t.Fatal("wrong plan or purpose was not rejected", err)
		}
	}
	occupied := []byte("isolated-existing-object")
	if _, err := provisionSignedObjectRequestWithClient(checker.client, s3, http.MethodPut, object, nil, occupied); err != nil {
		t.Fatal("seed synthetic occupied object", err)
	}
	before := len(execution.Journal.Entries)
	if err := runEnvironmentCreateStorageCanary(t.Context(), checker, s3, target, execution); err == nil || len(execution.Journal.Entries) != before {
		t.Fatal("occupied object must be refused before recording intent", err)
	}
	got, err := provisionSignedObjectRequestWithClient(checker.client, s3, http.MethodGet, object, nil, nil)
	if err != nil || string(got) != string(occupied) {
		t.Fatal("occupied object changed", err)
	}
	if _, err := provisionSignedObjectRequestWithClient(checker.client, s3, http.MethodDelete, object, nil, nil); err != nil {
		t.Fatal("clear only synthetic occupied object", err)
	}
	if err := runEnvironmentCreateStorageCanary(t.Context(), checker, s3, target, execution); err != nil {
		t.Fatal("native signed create canary", err)
	}
	entry := createOpenBaoACKCompletionEntry(execution.Journal, "storage-canary-"+target.Purpose)
	if entry == nil || entry.State != "completed" || len(entry.Receipt) == 0 || len(backend.objects) != 0 {
		t.Fatal("completed canary lacks native receipt or verified cleanup")
	}
	if err := runEnvironmentCreateStorageCanary(t.Context(), checker, s3, target, execution); err == nil || len(execution.Journal.Entries) != before+1 || len(backend.objects) != 0 {
		t.Fatal("completed canary replay must preserve original intent and never rewrite object", err)
	}
}

func TestIsolatedFreshProviderUsesNativeAllocationAndSignedStorage(t *testing.T) {
	cfg, operator := createProviderTestFixture(t, "dev")
	operator["LINODE_TOKEN"] = "isolated-provider-token"
	ca := base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))
	kubeconfig := []byte("apiVersion: v1\nkind: Config\ncurrent-context: isolated\nclusters:\n- name: isolated\n  cluster:\n    server: https://127.0.0.1:6443\n    certificate-authority-data: " + ca + "\ncontexts:\n- name: isolated\n  context:\n    cluster: isolated\n    user: isolated\nusers:\n- name: isolated\n  user:\n    token: isolated-kubernetes-token\n")
	backend := newIsolatedFreshProvider(t, cfg, operator, kubeconfig)
	plan, err := buildEnvironmentCreateProviderPlan(t.Context(), cfg, operator)
	if err != nil {
		t.Fatal("native provider plan", err)
	}
	if err := qualifyEnvironmentCreateProviders(t.Context(), cfg, plan, operator); err != nil {
		t.Fatal("native provider qualification", err)
	}
	for _, resource := range plan.Resources {
		if resource.Kind != "bucket" && resource.Kind != "lke" {
			continue
		}
		receipt, err := executeEnvironmentCreateProviderResource(t.Context(), cfg, resource, operator, nil)
		if err != nil {
			t.Fatal("native bucket allocation", err)
		}
		if _, ready, err := verifyEnvironmentCreateProviderResource(t.Context(), cfg, resource, receipt, operator); err != nil || !ready {
			t.Fatal("native provider readback", err)
		}
		if resource.Kind == "lke" {
			actual, err := fetchEnvironmentCreateKubeconfig(t.Context(), cfg, receipt, operator)
			if err != nil || string(actual) != string(kubeconfig) {
				t.Fatal("original isolated Kubernetes identity", err)
			}
			metadataRaw, err := environmentCreateProviderRequest(t.Context(), operator["LINODE_TOKEN"], http.MethodGet, resource.Path+"/"+receipt.NativeID, nil)
			var metadata lkeCluster
			if err != nil || json.Unmarshal(metadataRaw, &metadata) != nil || strconv.Itoa(metadata.ID) != receipt.NativeID || metadata.Label != receipt.Name || metadata.Region != receipt.Region || metadata.K8sVersion != resource.Body["k8s_version"] {
				t.Fatal("isolated original cluster metadata differs from allocation", err)
			}
			pools, err := environmentCreateProviderList(t.Context(), operator["LINODE_TOKEN"], resource.Path+"/"+receipt.NativeID+"/pools")
			if err != nil || len(pools) == 0 {
				t.Fatal("isolated cluster has no allocated node pool inventory", err)
			}
			var pool struct {
				ID    int `json:"id"`
				Nodes []struct {
					ID int `json:"instance_id"`
				} `json:"nodes"`
			}
			if json.Unmarshal(pools[0], &pool) != nil || pool.ID <= 0 || len(pool.Nodes) == 0 || pool.Nodes[0].ID <= 0 {
				t.Fatal("isolated pool omitted exact node identities")
			}
			workerPath := "/linode/instances/" + strconv.Itoa(pool.Nodes[0].ID)
			if _, err := environmentCreateProviderRequest(t.Context(), operator["LINODE_TOKEN"], http.MethodPut, workerPath, map[string]string{"label": "isolated-renamed-worker"}); err != nil {
				t.Fatal("isolated worker label update", err)
			}
			readback, err := environmentCreateProviderRequest(t.Context(), operator["LINODE_TOKEN"], http.MethodGet, workerPath, nil)
			var worker struct {
				ID    int    `json:"id"`
				Label string `json:"label"`
			}
			if err != nil || json.Unmarshal(readback, &worker) != nil || worker.ID != pool.Nodes[0].ID || worker.Label != "isolated-renamed-worker" {
				t.Fatal("isolated worker identity changed on label readback", err)
			}
		}
	}
	checker := defaultDeploymentCredentialChecker().withCheckContext(t.Context())
	checker.client = backend.s3Server.Client()
	for _, target := range []deploymentStorageTarget{cfg.Storage.RuntimeMedia, cfg.Storage.OTAFirmware} {
		access, secret := environmentCreateStorageCredentials(operator, target.Purpose)
		store := provisionObjectStore{bucket: target.Bucket, region: target.Region, endpoint: backend.s3Server.URL, accessKey: access, secretKey: secret}
		if err := checker.verifyCreateStorageAccess(operator["LINODE_TOKEN"], store, target.Prefix); err != nil {
			t.Fatal("native signed storage qualification", err)
		}
		if err := checker.validateStorageReadWriteCanary(store, target.Prefix); err != nil {
			t.Fatal("native signed storage canary", err)
		}
	}
	mediaAccess, mediaSecret := environmentCreateStorageCredentials(operator, cfg.Storage.RuntimeMedia.Purpose)
	for _, denied := range []provisionObjectStore{
		{bucket: cfg.Storage.RuntimeMedia.Bucket, region: cfg.Storage.RuntimeMedia.Region, endpoint: backend.s3Server.URL, accessKey: mediaAccess, secretKey: "incorrect-synthetic-secret"},
		{bucket: cfg.Storage.OTAFirmware.Bucket, region: cfg.Storage.OTAFirmware.Region, endpoint: backend.s3Server.URL, accessKey: mediaAccess, secretKey: mediaSecret},
	} {
		if err := checker.validateStorageReadWriteCanary(denied, cfg.Storage.RuntimeMedia.Prefix); err == nil {
			t.Fatal("isolated S3 accepted an invalid signature or another bucket's key")
		}
	}
	if len(backend.objects) != 0 {
		t.Fatal("signed storage canary was not cleaned up")
	}
	log := string(backend.safeRequestLog())
	if !strings.Contains(log, " 200\n") || !strings.Contains(log, " 401\n") || strings.Contains(log, mediaAccess) || strings.Contains(log, mediaSecret) || strings.Contains(log, "?") {
		t.Fatal("synthetic request log lost status or exposed credentials/query")
	}
}

func TestIsolatedFreshProviderVMRetainsHostIdentityAcrossRetry(t *testing.T) {
	cfg, operator := createProviderTestFixture(t, "dev")
	operator["LINODE_TOKEN"] = "isolated-provider-token"
	newIsolatedFreshProvider(t, cfg, operator, []byte("isolated-kubeconfig"))
	plan, err := buildEnvironmentCreateProviderPlan(t.Context(), cfg, operator)
	if err != nil {
		t.Fatal("native provider plan", err)
	}
	// The explicit handle points only to a fresh t.TempDir; never resolve a
	// real environment store for synthetic provider material.
	store := makeIsolatedTestSecretStore(t, "dev")
	count := 0
	for _, resource := range plan.Resources {
		if resource.Kind != "instance" {
			continue
		}
		material, err := prepareEnvironmentCreateProviderMaterial(t.Context(), cfg, strings.Repeat("a", 64), resource, store)
		if err != nil {
			t.Fatal("prepare isolated host identity", err)
		}
		retry, err := prepareEnvironmentCreateProviderMaterial(t.Context(), cfg, strings.Repeat("a", 64), resource, store)
		if err != nil || material["user_data"] != retry["user_data"] || material["host_public_key"] != retry["host_public_key"] {
			t.Fatal("isolated host identity changed on retry", err)
		}
		receipt, err := executeEnvironmentCreateProviderResource(t.Context(), cfg, resource, operator, material)
		if err != nil {
			t.Fatal("native VM allocation", err)
		}
		if _, ready, err := verifyEnvironmentCreateProviderResource(t.Context(), cfg, resource, receipt, operator); err != nil || !ready {
			t.Fatal("native VM readback", err)
		}
		count++
	}
	if count != 2 {
		t.Fatalf("expected two isolated VMs, got %d", count)
	}
}
