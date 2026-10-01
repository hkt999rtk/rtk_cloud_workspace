package main

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSelectedOTAEnvironmentUsesTrackedStagingWithoutGeneratedRuntime(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	const trust = `{"staging-ota":"AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA="}`
	writeTestFile(t, filepath.Join(workspace, "cloud_env/staging/overrides/architecture.env"), "VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON="+trust+"\n")
	cfg, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cfg.RuntimeRoot, "env", "stack.env")); !os.IsNotExist(err) {
		t.Fatalf("fixture unexpectedly has generated stack.env: %v", err)
	}
	env, err := selectedOTAEnvironment(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]string{
		"CLOUD_ENV_NAME":   "staging",
		"CLOUD_STACK_NAME": cfg.Values["CLOUD_STACK_NAME"],
		"VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON": trust,
		"DNS_RECORD_TTL": cfg.DNSValues["DNS_RECORD_TTL"],
		"LKE_REGION":     cfg.AdapterResolved["LKE_REGION"],
	} {
		if env[key] != want {
			t.Fatalf("selected OTA %s = %q, want %q", key, env[key], want)
		}
	}
}

func TestBindSelectedOTAStorageRejectsConflictingCanonicalBinding(t *testing.T) {
	cfg := deploymentConfig{
		Environment: "staging", RuntimeRoot: t.TempDir(),
		Storage: deploymentStoragePlan{RuntimeMedia: deploymentStorageTarget{
			Purpose: "runtime-media", Bucket: "approved-staging-bucket", Region: "sg-sin-2", Prefix: "environments/video-cloud-staging",
		}},
	}
	store, err := newSecretStore(t.TempDir(), "staging")
	if err != nil {
		t.Fatal(err)
	}
	operatorDir := filepath.Join(store.Root, "operator", "env")
	if err := os.MkdirAll(operatorDir, 0o700); err != nil {
		t.Fatal(err)
	}
	writeOperator := func(key, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(operatorDir, key), []byte(value+"\n"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	writeOperator("LINODE_OBJ_BUCKET", cfg.Storage.RuntimeMedia.Bucket)
	writeOperator("LINODE_OBJ_ENDPOINT", "https://objects.example.test")
	writeOperator("LINODE_TOKEN", "test-token")
	writeOperator("LINODE_MEDIA_OBJ_ACCESS_KEY_ID", "media-access")
	writeOperator("LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY", "media-secret")
	bucketRegion := cfg.Storage.RuntimeMedia.Region
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer test-token" {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/object-storage/buckets":
			_, _ = fmt.Fprintf(w, `{"data":[{"label":%q,"region":%q,"s3_endpoint":"https://objects.example.test"}]}`, cfg.Storage.RuntimeMedia.Bucket, bucketRegion)
		case "/object-storage/keys":
			_, _ = fmt.Fprintf(w, `{"data":[{"access_key":"media-access","bucket_access":[{"bucket_name":%q,"region":%q,"permissions":"read_write"}]},{"access_key":"legacy-access","bucket_access":[{"bucket_name":%q,"region":%q,"permissions":"read_write"}]}]}`, cfg.Storage.RuntimeMedia.Bucket, cfg.Storage.RuntimeMedia.Region, cfg.Storage.RuntimeMedia.Bucket, cfg.Storage.RuntimeMedia.Region)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("RTK_CLOUD_LINODE_API_ROOT", server.URL)
	env := map[string]string{}
	access, err := bindSelectedOTAStorage(cfg, store, env)
	if err != nil {
		t.Fatal(err)
	}
	if access.Access != "media-access" || access.Secret != "media-secret" {
		t.Fatal("selected OTA storage credentials differ from validated media grant")
	}
	incomplete := cfg
	incomplete.Storage.RuntimeMedia.Bucket = ""
	if _, err := bindSelectedOTAStorage(incomplete, store, map[string]string{}); err == nil || !strings.Contains(err.Error(), "plan is incomplete") {
		t.Fatalf("incomplete selected storage plan was accepted: %v", err)
	}
	missingStore, err := newSecretStore(t.TempDir(), "staging")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := bindSelectedOTAStorage(cfg, missingStore, map[string]string{}); err == nil || !strings.Contains(err.Error(), "binding is unavailable") {
		t.Fatalf("missing canonical operator binding was accepted: %v", err)
	}
	for key, want := range map[string]string{
		"VIDEO_CLOUD_BLOB_BUCKET":   cfg.Storage.RuntimeMedia.Bucket,
		"VIDEO_CLOUD_BLOB_REGION":   cfg.Storage.RuntimeMedia.Region,
		"VIDEO_CLOUD_BLOB_PREFIX":   cfg.Storage.RuntimeMedia.Prefix,
		"VIDEO_CLOUD_BLOB_ENDPOINT": "https://objects.example.test",
	} {
		if env[key] != want {
			t.Fatalf("bound OTA %s = %q, want %q", key, env[key], want)
		}
	}
	// With a scoped media key, the legacy bucket and endpoint may belong to
	// release-artifact storage and must never become the OTA private origin.
	writeOperator("LINODE_OBJ_BUCKET", "unrelated-artifact-bucket")
	writeOperator("LINODE_OBJ_ENDPOINT", "https://artifact.example.test")
	if _, err := bindSelectedOTAStorage(cfg, store, map[string]string{}); err != nil {
		t.Fatalf("scoped media binding incorrectly used legacy artifact storage: %v", err)
	}
	for _, tc := range []struct{ name, key, value, want string }{
		{"scope", "LINODE_MEDIA_OBJ_ACCESS_KEY_ID", "unrelated-access", "not found"},
		{"token", "LINODE_TOKEN", "", "credential pair is incomplete"},
		{"missing media secret", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY", "", "credential pair is incomplete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			writeOperator(tc.key, tc.value)
			defer func() {
				if tc.key == "LINODE_TOKEN" {
					writeOperator(tc.key, "test-token")
				} else if tc.key == "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY" {
					writeOperator(tc.key, "media-secret")
				} else {
					writeOperator(tc.key, "media-access")
				}
			}()
			fresh := map[string]string{}
			if _, err := bindSelectedOTAStorage(cfg, store, fresh); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("conflicting %s was accepted: %v", tc.name, err)
			}
			if len(fresh) != 0 {
				t.Fatalf("invalid binding partially updated runtime: %v", fresh)
			}
		})
	}
	writeOperator("LINODE_MEDIA_OBJ_ACCESS_KEY_ID", "")
	writeOperator("LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY", "")
	writeOperator("LINODE_OBJ_ACCESS_KEY_ID", "legacy-access")
	writeOperator("LINODE_OBJ_SECRET_ACCESS_KEY", "legacy-secret")
	if _, err := bindSelectedOTAStorage(cfg, store, map[string]string{}); err == nil || !strings.Contains(err.Error(), "legacy object storage binding conflicts") {
		t.Fatalf("unrelated legacy bucket was accepted without scoped media key: %v", err)
	}
	writeOperator("LINODE_OBJ_BUCKET", cfg.Storage.RuntimeMedia.Bucket)
	writeOperator("LINODE_OBJ_ENDPOINT", "https://objects.example.test")
	legacy, err := bindSelectedOTAStorage(cfg, store, map[string]string{})
	if err != nil || legacy.Access != "legacy-access" || legacy.Secret != "legacy-secret" {
		t.Fatalf("matching complete legacy binding was rejected: %v", err)
	}
	writeOperator("LINODE_OBJ_ENDPOINT", "https://artifact.example.test")
	if _, err := bindSelectedOTAStorage(cfg, store, map[string]string{}); err == nil || !strings.Contains(err.Error(), "endpoint conflicts") {
		t.Fatalf("legacy endpoint different from inventory was accepted: %v", err)
	}
	writeOperator("LINODE_MEDIA_OBJ_ACCESS_KEY_ID", "media-access")
	writeOperator("LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY", "media-secret")
	bucketRegion = "us-sea"
	if _, err := bindSelectedOTAStorage(cfg, store, map[string]string{}); err == nil || !strings.Contains(err.Error(), "storage bucket") {
		t.Fatalf("provider bucket in a different region was accepted: %v", err)
	}
	bucketRegion = cfg.Storage.RuntimeMedia.Region
	if _, err := bindSelectedOTAStorage(cfg, store, map[string]string{"VIDEO_CLOUD_BLOB_BUCKET": "unrelated-bucket"}); err == nil || !strings.Contains(err.Error(), "VIDEO_CLOUD_BLOB_BUCKET conflicts") {
		t.Fatalf("runtime blob bucket different from selected plan was accepted: %v", err)
	}
	receipt := deploymentStorageReceipt{
		Environment: cfg.Environment, Purpose: cfg.Storage.RuntimeMedia.Purpose,
		Bucket: cfg.Storage.RuntimeMedia.Bucket, Region: cfg.Storage.RuntimeMedia.Region,
		Endpoint: "https://other.example.test",
	}
	if err := writeDeploymentStorageReceipt(cfg.RuntimeRoot, receipt); err != nil {
		t.Fatal(err)
	}
	if _, err := bindSelectedOTAStorage(cfg, store, map[string]string{}); err == nil || !strings.Contains(err.Error(), "validated storage receipt") {
		t.Fatalf("conflicting validated receipt was accepted: %v", err)
	}
	if err := os.WriteFile(filepath.Join(cfg.RuntimeRoot, "state", "storage-preflight.json"), []byte("{invalid"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := bindSelectedOTAStorage(cfg, store, map[string]string{}); err == nil || !strings.Contains(err.Error(), "read selected OTA storage receipt") {
		t.Fatalf("malformed validated receipt was accepted: %v", err)
	}
}
