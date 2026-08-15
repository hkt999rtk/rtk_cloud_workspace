package main

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestDeploymentCredentialProfilePrecedenceAndScopedMapping(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	dir := t.TempDir()
	shared := filepath.Join(dir, "shared.env")
	environment := filepath.Join(dir, "staging.env")
	if err := os.WriteFile(shared, []byte("LINODE_TOKEN=shared\nGHCR_PULL_USERNAME=shared-user\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(environment, []byte("GHCR_PULL_USERNAME=environment-user\nLINODE_MEDIA_OBJ_ACCESS_KEY_ID=media-access\nLINODE_MEDIA_OBJ_SECRET_ACCESS_KEY=media-secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GHCR_PULL_USERNAME", "process-user")
	values, check := deploymentCredentialProfileValues("staging", environment, shared)
	if !check.Passed {
		t.Fatal(check.Detail)
	}
	if values["LINODE_TOKEN"] != "shared" || values["GHCR_PULL_USERNAME"] != "process-user" {
		t.Fatalf("precedence values = %#v", values)
	}
	if values["LINODE_OBJ_ACCESS_KEY_ID"] != "media-access" || values["LINODE_OBJ_SECRET_ACCESS_KEY"] != "media-secret" {
		t.Fatal("scoped media credentials were not mapped to the legacy child interface")
	}
}

func TestDeploymentCredentialProfilesNeverFallbackToHomeEnv(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.WriteFile(filepath.Join(home, ".env"), []byte("LINODE_TOKEN=legacy-must-not-load\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	environmentFile := filepath.Join(home, ".config", "rtk-cloud", "environments", "staging.env")
	if err := os.MkdirAll(filepath.Dir(environmentFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(environmentFile, []byte("LINODE_MEDIA_OBJ_ACCESS_KEY_ID=media\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	values, check := deploymentCredentialProfileValues("staging", environmentFile, filepath.Join(home, ".config", "rtk-cloud", "shared.env"))
	if check.Passed || values != nil || !strings.Contains(check.Detail, "shared.env") {
		t.Fatalf("values=%v check=%#v", values, check)
	}
}

func TestNormalizeLinodeS3EndpointRequiresHTTPS(t *testing.T) {
	if got, err := normalizeLinodeS3Endpoint("sg-sin-1.linodeobjects.com"); err != nil || got != "https://sg-sin-1.linodeobjects.com" {
		t.Fatalf("endpoint = %q, err = %v", got, err)
	}
	if _, err := normalizeLinodeS3Endpoint("http://insecure.example"); err == nil {
		t.Fatal("insecure endpoint unexpectedly accepted")
	}
}

func TestResolveStorageEndpointSkipsUnavailableEndpointTypes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"region":"sg-sin-2","endpoint_type":"E3","s3_endpoint":null},{"region":"sg-sin-2","endpoint_type":"E1","s3_endpoint":"sg-sin-1.linodeobjects.com"}]}`))
	}))
	defer server.Close()
	checker := deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL}
	endpoint, err := checker.resolveStorageEndpoint("token", "sg-sin-2")
	if err != nil || endpoint != "https://sg-sin-1.linodeobjects.com" {
		t.Fatalf("endpoint = %q, err = %v", endpoint, err)
	}
}

func TestResolvedObjectStorageValidationWritesRedactedReceipt(t *testing.T) {
	var mu sync.Mutex
	objects := map[string][]byte{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v4/regions/sg-sin-2":
			_, _ = w.Write([]byte(`{"id":"sg-sin-2","status":"ok","capabilities":["Kubernetes","Object Storage"]}`))
		case "/v4/object-storage/buckets":
			_, _ = fmt.Fprintf(w, `{"data":[{"label":"rtk-video-staging-sg","region":"sg-sin-2","s3_endpoint":%q}]}`, server.URL)
		case "/v4/object-storage/keys":
			_, _ = w.Write([]byte(`{"data":[{"id":42,"access_key":"media-access-secret-name","bucket_access":[{"bucket_name":"rtk-video-staging-sg","region":"sg-sin-2","permissions":"read_write"}]}]}`))
		default:
			if r.URL.Path == "/rtk-video-staging-sg" && r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`))
				return
			}
			prefix := "/rtk-video-staging-sg/"
			if !strings.HasPrefix(r.URL.Path, prefix) {
				http.NotFound(w, r)
				return
			}
			key := strings.TrimPrefix(r.URL.Path, prefix)
			mu.Lock()
			defer mu.Unlock()
			switch r.Method {
			case http.MethodPut:
				body := new(bytes.Buffer)
				_, _ = body.ReadFrom(r.Body)
				objects[key] = body.Bytes()
			case http.MethodGet:
				data, ok := objects[key]
				if !ok {
					http.NotFound(w, r)
					return
				}
				_, _ = w.Write(data)
			case http.MethodDelete:
				delete(objects, key)
			default:
				http.Error(w, "method", http.StatusMethodNotAllowed)
			}
		}
	}))
	defer server.Close()
	cfg := deploymentConfig{Environment: "staging", RuntimeRoot: t.TempDir(), Storage: deploymentStoragePlan{RuntimeMedia: deploymentStorageTarget{Purpose: "runtime-media", Policy: "colocated", Bucket: "rtk-video-staging-sg", Prefix: "environments/video-cloud-staging", Region: "sg-sin-2"}}}
	checker := deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL + "/v4"}
	check := checker.checkResolvedObjectStorage(cfg, map[string]string{"LINODE_TOKEN": "token", "LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "media-access-secret-name", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "never-write-this"})
	if !check.Passed {
		t.Fatal(check.Detail)
	}
	body, err := os.ReadFile(filepath.Join(cfg.RuntimeRoot, "state", "storage-preflight.json"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(body)
	for _, secret := range []string{"never-write-this", "media-access-secret-name"} {
		if strings.Contains(text, secret) {
			t.Fatalf("receipt leaked %q: %s", secret, text)
		}
	}
	if !strings.Contains(text, `"region": "sg-sin-2"`) || !strings.Contains(text, `"key_id": 42`) {
		t.Fatalf("receipt missing evidence: %s", text)
	}
	mu.Lock()
	remaining := len(objects)
	mu.Unlock()
	if remaining != 0 {
		t.Fatalf("canary cleanup left %d objects", remaining)
	}
}

func TestResolvedObjectStorageRejectsWrongRegionBeforeS3(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v4/regions/sg-sin-2":
			_, _ = w.Write([]byte(`{"id":"sg-sin-2","status":"ok","capabilities":["Kubernetes","Object Storage"]}`))
		case "/v4/object-storage/buckets":
			_, _ = fmt.Fprintf(w, `{"data":[{"label":"rtk-video-staging-sg","region":"us-sea","s3_endpoint":%q}]}`, server.URL)
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	cfg := deploymentConfig{Storage: deploymentStoragePlan{RuntimeMedia: deploymentStorageTarget{Purpose: "runtime-media", Policy: "colocated", Bucket: "rtk-video-staging-sg", Region: "sg-sin-2"}}}
	check := (deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL + "/v4"}).checkResolvedObjectStorage(cfg, map[string]string{"LINODE_TOKEN": "token"})
	if check.Passed || !strings.Contains(check.Detail, "colocated policy requires sg-sin-2") {
		t.Fatalf("check = %#v", check)
	}
}

func TestStorageCanaryCleansUpAfterReadMismatch(t *testing.T) {
	deleted := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/bucket" {
			_, _ = w.Write([]byte(`<ListBucketResult/>`))
			return
		}
		switch r.Method {
		case http.MethodPut:
			w.WriteHeader(http.StatusOK)
		case http.MethodGet:
			_, _ = w.Write([]byte("corrupt"))
		case http.MethodDelete:
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()
	checker := deploymentCredentialChecker{client: server.Client()}
	err := checker.validateStorageReadWriteCanary(provisionObjectStore{bucket: "bucket", endpoint: server.URL, region: "test", accessKey: "access", secretKey: "secret"}, "environment/test")
	if err == nil || !strings.Contains(err.Error(), "content mismatch") {
		t.Fatalf("error = %v", err)
	}
	if !deleted {
		t.Fatal("failed canary was not cleaned up")
	}
}

func TestStorageMigrationFiltersApplicationNamespaces(t *testing.T) {
	for _, source := range []string{"clips/a.mp4", "brands/logo.png", "firmware/device.bin"} {
		got, ok := storageMigrationDestinationKey("environments/video-cloud-staging", source)
		if !ok || got != "environments/video-cloud-staging/"+source {
			t.Fatalf("source %q => %q, %v", source, got, ok)
		}
	}
	for _, source := range []string{"releases/client.tar.gz", "artifacts/build.zip", "clip/not-plural"} {
		if got, ok := storageMigrationDestinationKey("environments/video-cloud-staging", source); ok {
			t.Fatalf("excluded source %q mapped to %q", source, got)
		}
	}
}

func TestResolvedObjectStorageRejectsLegacySeattleTuple(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v4/regions/sg-sin-2":
			_, _ = w.Write([]byte(`{"id":"sg-sin-2","status":"ok","capabilities":["Kubernetes","Object Storage"]}`))
		case "/v4/object-storage/buckets":
			_, _ = fmt.Fprintf(w, `{"data":[{"label":"rtk-video-staging-sg","region":"sg-sin-2","s3_endpoint":%q}]}`, server.URL)
		default:
			t.Fatalf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	cfg := deploymentConfig{Storage: deploymentStoragePlan{RuntimeMedia: deploymentStorageTarget{Purpose: "runtime-media", Policy: "colocated", Bucket: "rtk-video-staging-sg", Region: "sg-sin-2"}}}
	values := map[string]string{"LINODE_TOKEN": "token", "LINODE_OBJ_BUCKET": "rtk-video-old", "LINODE_OBJ_REGION": "us-sea", "LINODE_OBJ_ACCESS_KEY_ID": "legacy", "LINODE_OBJ_SECRET_ACCESS_KEY": "secret"}
	check := (deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL + "/v4"}).checkResolvedObjectStorage(cfg, values)
	if check.Passed || !strings.Contains(check.Detail, "legacy bucket") {
		t.Fatalf("check = %#v", check)
	}
}
