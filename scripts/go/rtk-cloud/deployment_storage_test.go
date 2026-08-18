package main

import (
	"bytes"
	"encoding/json"
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

func TestDeploymentStorageLifecycleHappyPaths(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	sharedFile := filepath.Join(home, ".config", "rtk-cloud", "shared.env")
	environmentFile := filepath.Join(home, ".config", "rtk-cloud", "environments", "staging.env")
	for path, contents := range map[string]string{
		sharedFile:      "LINODE_TOKEN=token\nGHCR_PULL_USERNAME=user\nGHCR_PULL_TOKEN=ghcr\nGODADDY_KEY=key\nGODADDY_SECRET=secret\n",
		environmentFile: "",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var mu sync.Mutex
	objects := map[string][]byte{}
	deletedKeyID := ""
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/v4/regions/"):
			region := strings.TrimPrefix(r.URL.Path, "/v4/regions/")
			_, _ = fmt.Fprintf(w, `{"id":%q,"status":"ok","capabilities":["Kubernetes","Object Storage"]}`, region)
			return
		case r.URL.Path == "/v4/object-storage/endpoints":
			_, _ = fmt.Fprintf(w, `{"data":[{"region":"sg-sin-2","s3_endpoint":%q},{"region":"us-sea","s3_endpoint":%q}]}`, server.URL, server.URL)
			return
		case r.URL.Path == "/v4/object-storage/buckets" && r.Method == http.MethodGet:
			_, _ = fmt.Fprintf(w, `{"data":[{"label":"rtk-video-staging-sg","region":"sg-sin-2","s3_endpoint":%q},{"label":"rtk-cloud-client-artifacts","region":"us-sea","s3_endpoint":%q}]}`, server.URL, server.URL)
			return
		case r.URL.Path == "/v4/object-storage/keys" && r.Method == http.MethodGet:
			_, _ = w.Write([]byte(`{"data":[{"id":101,"access_key":"media-access","bucket_access":[{"bucket_name":"rtk-video-staging-sg","region":"sg-sin-2","permissions":"read_write"}]},{"id":202,"access_key":"artifact-access","bucket_access":[{"bucket_name":"rtk-cloud-client-artifacts","region":"us-sea","permissions":"read_write"}]}]}`))
			return
		case r.URL.Path == "/v4/object-storage/keys" && r.Method == http.MethodPost:
			body := new(bytes.Buffer)
			_, _ = body.ReadFrom(r.Body)
			if strings.Contains(body.String(), "rtk-cloud-client-artifacts") {
				_, _ = w.Write([]byte(`{"access_key":"artifact-access","secret_key":"artifact-secret"}`))
			} else {
				_, _ = w.Write([]byte(`{"access_key":"media-access","secret_key":"media-secret"}`))
			}
			return
		case strings.HasPrefix(r.URL.Path, "/v4/object-storage/keys/") && r.Method == http.MethodDelete:
			deletedKeyID = strings.TrimPrefix(r.URL.Path, "/v4/object-storage/keys/")
			w.WriteHeader(http.StatusNoContent)
			return
		}

		parts := strings.SplitN(strings.TrimPrefix(r.URL.Path, "/"), "/", 2)
		if len(parts) == 0 || (parts[0] != "rtk-video-staging-sg" && parts[0] != "rtk-cloud-client-artifacts") {
			http.NotFound(w, r)
			return
		}
		if len(parts) == 1 {
			_, _ = w.Write([]byte(`<ListBucketResult><IsTruncated>false</IsTruncated></ListBucketResult>`))
			return
		}
		key := parts[0] + "/" + parts[1]
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
	}))
	defer server.Close()
	t.Setenv("RTK_CLOUD_LINODE_API_ROOT", server.URL+"/v4")

	cfg := deploymentConfig{
		Environment:     "staging",
		RuntimeRoot:     t.TempDir(),
		AdapterResolved: map[string]string{"LKE_REGION": "sg-sin-2"},
		Storage: deploymentStoragePlan{
			RuntimeMedia:     deploymentStorageTarget{Purpose: "runtime-media", Policy: "colocated", Bucket: "rtk-video-staging-sg", Prefix: "environments/video-cloud-staging", Region: "sg-sin-2"},
			ReleaseArtifacts: deploymentStorageTarget{Purpose: "release-artifacts", Policy: "shared-cross-region", Bucket: "rtk-cloud-client-artifacts", Prefix: "releases", Region: "us-sea"},
		},
	}
	if err := runDeploymentStorageLifecycle("storage-plan", cfg, environmentFile, "", 0); err != nil {
		t.Fatal(err)
	}
	if err := runDeploymentStorageLifecycle("storage-bootstrap", cfg, environmentFile, "", 0); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		action, source string
		keyID          int
	}{
		{action: "storage-migrate"},
		{action: "storage-retire"},
		{action: "storage-unknown"},
	} {
		if err := runDeploymentStorageLifecycle(tc.action, cfg, environmentFile, tc.source, tc.keyID); err == nil {
			t.Fatalf("%s unexpectedly succeeded", tc.action)
		}
	}
	values, check := deploymentCredentialProfileValues("staging", environmentFile, sharedFile)
	if !check.Passed {
		t.Fatal(check.Detail)
	}
	checker := deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL + "/v4"}
	if check := checker.checkResolvedObjectStorage(cfg, values); !check.Passed {
		t.Fatal(check.Detail)
	}
	if check := checker.checkResolvedArtifactStorage(cfg, values); !check.Passed {
		t.Fatal(check.Detail)
	}
	if err := checker.validateClipStorageSmoke(provisionObjectStore{bucket: cfg.Storage.RuntimeMedia.Bucket, endpoint: server.URL, region: cfg.Storage.RuntimeMedia.Region, accessKey: values["LINODE_OBJ_ACCESS_KEY_ID"], secretKey: values["LINODE_OBJ_SECRET_ACCESS_KEY"]}, cfg.Storage.RuntimeMedia.Prefix); err != nil {
		t.Fatal(err)
	}

	sourceRoot := t.TempDir()
	for key, contents := range map[string]string{"clips/a.mp4": "clip", "brands/logo.png": "brand", "firmware/device.bin": "firmware"} {
		path := filepath.Join(sourceRoot, "source-bucket", filepath.FromSlash(key))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	sourceFile := filepath.Join(t.TempDir(), "source.env")
	sourceProfile := "LINODE_OBJ_BUCKET=source-bucket\nLINODE_OBJ_ENDPOINT=file://" + sourceRoot + "\nLINODE_OBJ_REGION=us-sea\n"
	if err := os.WriteFile(sourceFile, []byte(sourceProfile), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runDeploymentStorageLifecycle("storage-migrate", cfg, environmentFile, sourceFile, 0); err != nil {
		t.Fatal(err)
	}
	if err := checker.migrateRuntimeStorage(cfg, values, sourceFile); err != nil {
		t.Fatal(err)
	}
	var migration deploymentStorageMigrationState
	stateBody, err := os.ReadFile(filepath.Join(cfg.RuntimeRoot, "state", "storage-migration.json"))
	if err != nil || json.Unmarshal(stateBody, &migration) != nil || migration.ObjectCount != 3 {
		t.Fatalf("migration state = %#v, err = %v", migration, err)
	}

	if err := writeStorageState(filepath.Join(cfg.RuntimeRoot, "state", "storage-cutover.json"), map[string]bool{"complete": true}); err != nil {
		t.Fatal(err)
	}
	if err := writeStorageState(filepath.Join(cfg.RuntimeRoot, "state", "storage-consumers.json"), map[string]bool{"generic_key_in_use": false}); err != nil {
		t.Fatal(err)
	}
	if err := runDeploymentStorageLifecycle("storage-retire", cfg, environmentFile, "", 101); err != nil {
		t.Fatal(err)
	}
	if deletedKeyID != "101" {
		t.Fatalf("deleted key ID = %q", deletedKeyID)
	}

	workspace := writeDeploymentFixture(t, "staging", "lke")
	cutoverCfg, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	cutoverCfg.Storage = cfg.Storage
	if err := os.MkdirAll(filepath.Join(cutoverCfg.RuntimeRoot, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cutoverCfg.RuntimeRoot, "state", "kubeconfig.yaml"), []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := writeDeploymentStorageReceipt(cutoverCfg.RuntimeRoot, deploymentStorageReceipt{
		Purpose:  cutoverCfg.Storage.RuntimeMedia.Purpose,
		Bucket:   cutoverCfg.Storage.RuntimeMedia.Bucket,
		Region:   cutoverCfg.Storage.RuntimeMedia.Region,
		Endpoint: server.URL,
	}); err != nil {
		t.Fatal(err)
	}
	fakeKubectl(t)
	if err := runDeploymentStorageLifecycle("storage-cutover", cutoverCfg, environmentFile, "", 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cutoverCfg.RuntimeRoot, "state", "storage-cutover.json")); err != nil {
		t.Fatal(err)
	}
}
