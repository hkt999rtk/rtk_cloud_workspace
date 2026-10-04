package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
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
	if values["LINODE_TOKEN"] != "shared" || values["GHCR_PULL_USERNAME"] != "environment-user" {
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
	if !check.Passed || values["LINODE_TOKEN"] != "" || values["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"] != "media" {
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

func TestOTAProvisionBucketChecksLiveInventoryBeforeDeployment(t *testing.T) {
	bucketType := "E3"
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveMigrationBucketInspection(w, r) {
			return
		}
		if r.Method == http.MethodGet && r.URL.Query().Has("tagging") {
			_, _ = w.Write([]byte(`<Tagging><TagSet/></Tagging>`))
			return
		}
		if r.Method != http.MethodGet || r.URL.Path != "/v4/object-storage/buckets" {
			http.NotFound(w, r)
			return
		}
		_, _ = fmt.Fprintf(w, `{"data":[{"label":"rtk-ota-firmware-staging-sg-sin-2","region":"sg-sin-2","endpoint_type":%q,"s3_endpoint":%q}]}`, bucketType, server.URL)
	}))
	defer server.Close()
	checker := deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL + "/v4"}
	env := map[string]string{
		"VIDEO_CLOUD_OTA_BLOB_BUCKET":        "rtk-ota-firmware-staging-sg-sin-2",
		"VIDEO_CLOUD_OTA_BLOB_REGION":        "sg-sin-2",
		"VIDEO_CLOUD_OTA_BLOB_ENDPOINT":      server.URL,
		"VIDEO_CLOUD_OTA_BLOB_ENDPOINT_TYPE": "E3",
	}
	if err := checker.validateOTAProvisionBucket("token", env); err != nil {
		t.Fatal(err)
	}
	bucketType = "E1"
	if err := checker.validateOTAProvisionBucket("token", env); err == nil || !strings.Contains(err.Error(), "requires an E3 bucket") {
		t.Fatalf("live E1 bucket was accepted despite an E3 receipt: %v", err)
	}
	bucketType = "E2"
	if err := checker.validateOTAProvisionBucket("token", env); err == nil || !strings.Contains(err.Error(), "requires an E3 bucket") {
		t.Fatalf("live E2 bucket was accepted despite an E3-only policy: %v", err)
	}
	bucketType = "E3"
	env["VIDEO_CLOUD_OTA_BLOB_ENDPOINT"] = "https://stale.example.test"
	if err := checker.validateOTAProvisionBucket("token", env); err == nil || !strings.Contains(err.Error(), "no longer matches") {
		t.Fatalf("stale OTA endpoint receipt was accepted: %v", err)
	}
}

func TestOTABootstrapDoesNotIssueDuplicateKeyWhenExistingE3KeyCannotAccessBucket(t *testing.T) {
	const bucketName = "rtk-ota-firmware-dev-us-lax"
	issued := 0
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveMigrationBucketInspection(w, r) {
			return
		}
		if r.Method == http.MethodGet && r.URL.Query().Has("tagging") {
			_, _ = w.Write([]byte(`<Tagging><TagSet/></Tagging>`))
			return
		}
		switch {
		case r.URL.Path == "/v4/object-storage/buckets" && r.Method == http.MethodGet:
			_, _ = fmt.Fprintf(w, `{"data":[{"label":%q,"region":"us-lax","endpoint_type":"E3","s3_endpoint":%q}]}`, bucketName, server.URL)
		case r.URL.Path == "/v4/object-storage/keys" && r.Method == http.MethodGet:
			_, _ = fmt.Fprintf(w, `{"data":[{"id":42,"access_key":"existing-ota-key","bucket_access":[{"bucket_name":%q,"region":"us-lax","permissions":"read_write"}]}]}`, bucketName)
		case r.URL.Path == "/v4/object-storage/keys" && r.Method == http.MethodPost:
			issued++
			w.WriteHeader(http.StatusCreated)
		case r.URL.Path == "/"+bucketName && r.Method == http.MethodGet:
			w.WriteHeader(http.StatusForbidden)
		default:
			t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	checker := deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL + "/v4"}
	cfg := deploymentConfig{Environment: "dev", Storage: deploymentStoragePlan{OTAMode: "dedicated", OTAFirmware: deploymentStorageTarget{Bucket: bucketName, Region: "us-lax", Prefix: "environments/video-cloud-dev"}}}
	values := map[string]string{"LINODE_TOKEN": "test-token", "LINODE_OTA_OBJ_ACCESS_KEY_ID": "existing-ota-key", "LINODE_OTA_OBJ_SECRET_ACCESS_KEY": "test-secret"}
	err := checker.bootstrapOTAStorage(cfg, values, filepath.Join(t.TempDir(), "env"))
	if err == nil || !strings.Contains(err.Error(), "already belongs to the E3 bucket") || issued != 0 {
		t.Fatalf("failed E3 validation issued another key: error=%v, key_posts=%d", err, issued)
	}
}

func TestResolveStorageEndpointSkipsUnavailableEndpointTypes(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveMigrationBucketInspection(w, r) {
			return
		}
		if r.Method == http.MethodGet && r.URL.Query().Has("tagging") {
			_, _ = w.Write([]byte(`<Tagging><TagSet/></Tagging>`))
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"region":"sg-sin-2","endpoint_type":"E3","s3_endpoint":null},{"region":"sg-sin-2","endpoint_type":"E1","s3_endpoint":"sg-sin-1.linodeobjects.com"}]}`))
	}))
	defer server.Close()
	checker := deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL}
	endpoint, err := checker.resolveStorageEndpoint("token", "sg-sin-2")
	if err != nil || endpoint != "https://sg-sin-1.linodeobjects.com" {
		t.Fatalf("endpoint = %q, err = %v", endpoint, err)
	}
}

func TestResolveOTAMetricsEndpointTypeRequiresAvailableE3(t *testing.T) {
	inventory := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/v4/object-storage/endpoints" {
			t.Errorf("unexpected endpoint discovery request: %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(inventory))
	}))
	defer server.Close()
	checker := deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL + "/v4"}
	for _, tc := range []struct{ name, body, wantError string }{
		{"unassigned E3", `{"data":[{"region":"us-lax","endpoint_type":"E2","s3_endpoint":"us-lax-1.linodeobjects.com"},{"region":"us-lax","endpoint_type":"E3","s3_endpoint":null}]}`, ""},
		{"assigned E3", `{"data":[{"region":"us-lax","endpoint_type":"E3","s3_endpoint":"us-lax-4.linodeobjects.com"}]}`, ""},
		{"E2 only", `{"data":[{"region":"us-lax","endpoint_type":"E2","s3_endpoint":"us-lax-1.linodeobjects.com"}]}`, "no available E3"},
		{"E3 in another region", `{"data":[{"region":"us-sea","endpoint_type":"E3","s3_endpoint":null}]}`, "no available E3"},
		{"missing region", `{"data":[]}`, "no available E3"},
		{"invalid inventory", `{`, "invalid JSON"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			inventory = tc.body
			got, err := checker.resolveOTAMetricsEndpointType("token", "us-lax")
			if tc.wantError == "" {
				if err != nil || got != "E3" {
					t.Fatalf("available E3 type = %q, %v", got, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), tc.wantError) || got != "" {
				t.Fatalf("unavailable E3 accepted: type=%q error=%v", got, err)
			}
		})
	}
}

func TestOTABootstrapRequiresCreatedBucketE3AssignedEndpoint(t *testing.T) {
	for _, tc := range []struct{ name, bucketType, endpoint, want string }{
		{"wrong endpoint type", "E2", "https://assigned.example.test", "requires an E3 bucket"},
		{"missing assigned endpoint", "E3", "", "omitted s3_endpoint"},
		{"invalid assigned endpoint", "E3", "http://insecure.example.test", "invalid s3_endpoint"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const bucketName = "rtk-cloud-dev-ota-firmware-us-sea"
			created, endpointReads, keyPosts, s3Requests := 0, 0, 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/v4/regions/us-sea":
					fmt.Fprint(w, `{"id":"us-sea","status":"ok","capabilities":["Kubernetes","Object Storage"]}`)
				case r.Method == http.MethodGet && r.URL.Path == "/v4/object-storage/endpoints":
					endpointReads++
					// A different assigned type must never fill in the created
					// E3 bucket's missing endpoint.
					fmt.Fprint(w, `{"data":[{"region":"us-sea","endpoint_type":"E3","s3_endpoint":null},{"region":"us-sea","endpoint_type":"E1","s3_endpoint":"assigned.example.test"}]}`)
				case r.Method == http.MethodGet && r.URL.Path == "/v4/object-storage/buckets":
					fmt.Fprint(w, `{"data":[]}`)
				case r.Method == http.MethodPost && r.URL.Path == "/v4/object-storage/buckets":
					created++
					var request map[string]string
					if err := json.NewDecoder(r.Body).Decode(&request); err != nil || request["endpoint_type"] != "E3" || request["label"] != bucketName || request["region"] != "us-sea" {
						t.Errorf("incorrect E3 creation request: %#v, %v", request, err)
					}
					fmt.Fprintf(w, `{"label":%q,"region":"us-sea","endpoint_type":%q,"s3_endpoint":%q}`, bucketName, tc.bucketType, tc.endpoint)
				case r.Method == http.MethodPost && r.URL.Path == "/v4/object-storage/keys":
					keyPosts++
					http.Error(w, "key issuance must not occur", http.StatusBadRequest)
				default:
					s3Requests++
					t.Errorf("unexpected request after invalid bucket response: %s %s", r.Method, r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			checker := deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL + "/v4"}
			cfg := deploymentConfig{Environment: "dev", RuntimeRoot: t.TempDir(), Storage: deploymentStoragePlan{OTAMode: "dedicated", OTAFirmware: deploymentStorageTarget{Purpose: "ota-firmware", Bucket: bucketName, Region: "us-sea", Prefix: "environments/video-cloud-dev"}}}
			profile := filepath.Join(t.TempDir(), "candidate.env")
			err := checker.bootstrapOTAStorage(cfg, map[string]string{"LINODE_TOKEN": "test-token"}, profile)
			if err == nil || !strings.Contains(err.Error(), tc.want) || created != 1 || endpointReads != 1 || keyPosts != 0 || s3Requests != 0 {
				t.Fatalf("invalid created bucket was accepted or used: error=%v creates=%d endpoint_reads=%d key_posts=%d s3_requests=%d", err, created, endpointReads, keyPosts, s3Requests)
			}
			for _, path := range []string{profile, filepath.Join(cfg.RuntimeRoot, "state", "storage-preflight-ota.json"), filepath.Join(cfg.RuntimeRoot, "state", "storage-cutover-ota.json")} {
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatalf("invalid created bucket left credentials or receipt: %s: %v", path, err)
				}
			}
		})
	}
}

func TestResolvedObjectStorageValidationWritesRedactedReceipt(t *testing.T) {
	var mu sync.Mutex
	objects := map[string][]byte{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveMigrationBucketInspection(w, r) {
			return
		}
		if r.Method == http.MethodGet && r.URL.Query().Has("tagging") {
			_, _ = w.Write([]byte(`<Tagging><TagSet/></Tagging>`))
			return
		}
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
		if serveMigrationBucketInspection(w, r) {
			return
		}
		if r.Method == http.MethodGet && r.URL.Query().Has("tagging") {
			_, _ = w.Write([]byte(`<Tagging><TagSet/></Tagging>`))
			return
		}
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
		if serveMigrationBucketInspection(w, r) {
			return
		}
		if r.Method == http.MethodGet && r.URL.Query().Has("tagging") {
			_, _ = w.Write([]byte(`<Tagging><TagSet/></Tagging>`))
			return
		}
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
	for _, source := range []string{"clips/a.mp4", "brands/logo.png", "snapshots/a.jpg", "clip-index/a.json", "ota/legacy/firmware.bin", "firmware/device.bin"} {
		got, ok := storageMigrationDestinationKey("environments/video-cloud-staging", source)
		if !ok || got != "environments/video-cloud-staging/"+source {
			t.Fatalf("source %q => %q, %v", source, got, ok)
		}
	}
	if got, ok := storageMigrationDestinationKey("environments/video-cloud-staging", "environments/video-cloud-staging/clips/a.mp4"); !ok || got != "environments/video-cloud-staging/clips/a.mp4" {
		t.Fatalf("already-prefixed key mapped to %q, %v", got, ok)
	}
	if got, ok := storageMigrationDestinationKeyForPurpose("environments/video-cloud-dev", "ota-billable-v1/brand/product/release/firmware.bin", []string{"ota-billable-v1/"}); !ok || got != "environments/video-cloud-dev/ota-billable-v1/brand/product/release/firmware.bin" {
		t.Fatalf("billable OTA key mapped to %q, %v", got, ok)
	}
	if got, ok := storageMigrationDestinationKeyForPurpose("environments/video-cloud-dev", "ota/legacy/firmware.bin", []string{"ota-billable-v1/"}); ok {
		t.Fatalf("historical OTA key entered billable bucket: %q", got)
	}
	for _, source := range []string{"releases/client.tar.gz", "artifacts/build.zip", "clip/not-plural"} {
		if got, ok := storageMigrationDestinationKey("environments/video-cloud-staging", source); ok {
			t.Fatalf("excluded source %q mapped to %q", source, got)
		}
	}
}

func TestDeploymentStorageActivationRequiresMatchingCutover(t *testing.T) {
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", t.TempDir())
	cfg := deploymentConfig{Environment: "dev", RuntimeRoot: t.TempDir(), Storage: deploymentStoragePlan{RuntimeMedia: deploymentStorageTarget{Bucket: "rtk-cloud-dev-runtime-us-sea", Region: "us-sea", Prefix: "environments/video-cloud-dev"}, RuntimeMediaCutoverRequired: true}}
	if err := validateDeploymentStorageActivation(cfg); err == nil {
		t.Fatal("deployment activated before media cutover")
	}
	if err := writeStorageState(filepath.Join(cfg.RuntimeRoot, "state", "storage-cutover.json"), map[string]any{"environment": "dev", "bucket": "wrong-bucket", "cutover_at": "2026-09-27T00:00:00Z", "rollback_credentials_retained": true}); err != nil {
		t.Fatal(err)
	}
	if err := validateDeploymentStorageActivation(cfg); err == nil {
		t.Fatal("deployment accepted mismatched cutover")
	}
	proof := []byte(`{"verified":true}`)
	if err := os.WriteFile(storageCutoverMigrationPath(cfg, "media"), proof, 0600); err != nil {
		t.Fatal(err)
	}
	hash := fmt.Sprintf("%x", sha256.Sum256(proof))
	store, _ := newSecretStore("", "dev")
	if err := saveStorageCutoverJournal(store, storageCutoverJournal{Environment: "dev", Purpose: "media", Status: "complete", ID: "cutover-id", MigrationSHA256: hash}, false); err != nil {
		t.Fatal(err)
	}
	if err := writeStorageState(filepath.Join(cfg.RuntimeRoot, "state", "storage-cutover.json"), map[string]any{"environment": "dev", "bucket": cfg.Storage.RuntimeMedia.Bucket, "region": "us-sea", "prefix": cfg.Storage.RuntimeMedia.Prefix, "cutover_id": "cutover-id", "migration_receipt_sha256": hash, "cutover_at": "2026-09-27T00:00:00Z", "rollback_credentials_retained": true}); err != nil {
		t.Fatal(err)
	}
	if err := validateDeploymentStorageActivation(cfg); err != nil {
		t.Fatal(err)
	}
	cfg.Storage.RuntimeMedia.Prefix = "environments/another-stack"
	if err := validateDeploymentStorageActivation(cfg); err == nil {
		t.Fatal("changed prefix accepted with stale receipt")
	}
	cfg.Storage.RuntimeMedia.Prefix = "environments/video-cloud-dev"
	if err := os.WriteFile(storageCutoverMigrationPath(cfg, "media"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := validateDeploymentStorageActivation(cfg); err == nil {
		t.Fatal("changed migration proof accepted")
	}
}

func TestResolvedObjectStorageRejectsLegacySeattleTuple(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveMigrationBucketInspection(w, r) {
			return
		}
		if r.Method == http.MethodGet && r.URL.Query().Has("tagging") {
			_, _ = w.Write([]byte(`<Tagging><TagSet/></Tagging>`))
			return
		}
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
	environmentFile := filepath.Join(home, ".config", "rtk_cloud", "staging", "operator", "env")
	if err := os.MkdirAll(environmentFile, 0o700); err != nil {
		t.Fatal(err)
	}
	for name, contents := range map[string]string{
		"LINODE_TOKEN": "token", "GHCR_PULL_USERNAME": "user", "GHCR_PULL_TOKEN": "ghcr",
		"GODADDY_KEY": "key", "GODADDY_SECRET": "secret",
	} {
		path := filepath.Join(environmentFile, name)
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
		if serveMigrationBucketInspection(w, r) {
			return
		}
		if r.Method == http.MethodGet && r.URL.Query().Has("tagging") {
			_, _ = w.Write([]byte(`<Tagging><TagSet/></Tagging>`))
			return
		}
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
			mu.Lock()
			defer mu.Unlock()
			_, _ = w.Write([]byte(`<ListBucketResult><IsTruncated>false</IsTruncated>`))
			for physical, data := range objects {
				key, found := strings.CutPrefix(physical, parts[0]+"/")
				if found && strings.HasPrefix(key, r.URL.Query().Get("prefix")) {
					_, _ = fmt.Fprintf(w, `<Contents><Key>%s</Key><Size>%d</Size></Contents>`, key, len(data))
				}
			}
			_, _ = w.Write([]byte(`</ListBucketResult>`))
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
	prepared, profileCheck := deploymentCredentialProfileValues("staging", environmentFile, "")
	if !profileCheck.Passed || prepared["LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID"] != "" {
		t.Fatalf("media bootstrap changed artifact credentials: %s", profileCheck.Detail)
	}
	if err := runDeploymentStorageLifecyclePurpose("storage-bootstrap", cfg, environmentFile, "", 0, "artifacts"); err != nil {
		t.Fatal(err)
	}
	if err := validateDeploymentCredentials(deploymentConfig{Environment: "staging"}, environmentFile); err != nil {
		t.Fatal(err)
	}
	if err := validateAndBootstrapDeploymentCredentials(cfg, environmentFile); err != nil {
		t.Fatal(err)
	}
	if err := validateAndGrantDeploymentObjectStorageAccess(cfg, environmentFile); err != nil {
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
	values, check := deploymentCredentialProfileValues("staging", environmentFile, "")
	if !check.Passed {
		t.Fatal(check.Detail)
	}
	checker := deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL + "/v4"}
	if check := checker.checkObjectStorageWithOptions(cfg, values, deploymentCredentialCheckOptions{}); !check.Passed {
		t.Fatal(check.Detail)
	}
	credentialCfg := cfg
	credentialCfg.Values = map[string]string{"VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED": "true"}
	if err := checker.checkWithOptions(credentialCfg, environmentFile, deploymentCredentialCheckOptions{}); err != nil {
		t.Fatal(err)
	}
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
	if err := runDeploymentStorageLifecycle("storage-retire", cfg, environmentFile, "", 101); err == nil {
		t.Fatal("retirement accepted incomplete, unbound consumer evidence")
	}
	if deletedKeyID != "" {
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
	store, err := newSecretStore("", "staging")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(store.KubeconfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.KubeconfigPath(), []byte("apiVersion: v1\n"), 0o600); err != nil {
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
	if err := os.WriteFile(filepath.Join(cutoverCfg.RuntimeRoot, "state", "storage-migration.json"), stateBody, 0o600); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(t.TempDir(), "media-candidate.env")
	if err := stageDeploymentStorageProfile("staging", environmentFile, candidate, true); err != nil {
		t.Fatal(err)
	}
	fakeKubectl(t)
	mockRoot := installStorageCutoverMock(t)
	consumer := storageCutoverFixture("Deployment", "video-cloud-api")
	storageCutoverMap(consumer["metadata"])["namespace"] = "video-cloud-staging-video-cloud"
	for _, item := range storageCutoverGet(consumer, "/spec/template/spec/containers/0/env").([]any) {
		env := storageCutoverMap(item)
		switch env["name"] {
		case "VIDEO_CLOUD_BLOB_BUCKET":
			env["value"] = "source-bucket"
		case "VIDEO_CLOUD_BLOB_ENDPOINT":
			env["value"] = "file://" + sourceRoot
		case "VIDEO_CLOUD_BLOB_PREFIX":
			env["value"] = ""
		case "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY":
			delete(env, "valueFrom")
			env["value"] = "source-credential"
		}
	}
	storageCutoverMockWrite(t, mockRoot, consumer)
	if err := runDeploymentStorageLifecycle("storage-cutover", cutoverCfg, candidate, sourceFile, 0); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cutoverCfg.RuntimeRoot, "state", "storage-cutover.json")); err != nil {
		t.Fatal(err)
	}
}

func TestCutoverOTAStorageRejectsWhitespaceCDNBeforeStorageChecks(t *testing.T) {
	cfg := deploymentConfig{Values: map[string]string{"VIDEO_CLOUD_OTA_CDN_BASE_URL": " \t"}}
	if err := (deploymentCredentialChecker{}).cutoverOTAStorage(cfg, nil, "", ""); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("OTA storage cutover accepted whitespace-only CDN URL: %v", err)
	}
}

func TestDedicatedOTAStorageLifecycle(t *testing.T) {
	clearDeploymentCredentialEnvironment(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	profile := filepath.Join(home, ".config", "rtk_cloud", "dev", "operator", "env")
	if err := os.MkdirAll(profile, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(profile, "LINODE_TOKEN"), []byte("token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	const bucketName = "rtk-cloud-dev-ota-firmware-us-sea"
	var mu sync.Mutex
	created, keyIssued, requestedMetricsEndpoint := false, false, false
	keyIssueCount := 0
	bucketType := "E3"
	objects := map[string][]byte{}
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if serveMigrationBucketInspection(w, r) {
			return
		}
		if r.Method == http.MethodGet && r.URL.Query().Has("tagging") {
			_, _ = w.Write([]byte(`<Tagging><TagSet/></Tagging>`))
			return
		}
		switch {
		case r.URL.Path == "/v4/regions/us-sea":
			_, _ = w.Write([]byte(`{"id":"us-sea","status":"ok","capabilities":["Kubernetes","Object Storage"]}`))
			return
		case r.URL.Path == "/v4/object-storage/endpoints":
			if created {
				_, _ = fmt.Fprintf(w, `{"data":[{"region":"us-sea","endpoint_type":"E3","s3_endpoint":%q}]}`, server.URL)
			} else {
				_, _ = w.Write([]byte(`{"data":[{"region":"us-sea","endpoint_type":"E3","s3_endpoint":null}]}`))
			}
			return
		case r.URL.Path == "/v4/object-storage/buckets" && r.Method == http.MethodGet:
			if created {
				_, _ = fmt.Fprintf(w, `{"data":[{"label":%q,"region":"us-sea","endpoint_type":%q,"s3_endpoint":%q}]}`, bucketName, bucketType, server.URL)
			} else {
				_, _ = w.Write([]byte(`{"data":[]}`))
			}
			return
		case r.URL.Path == "/v4/object-storage/buckets" && r.Method == http.MethodPost:
			var request map[string]string
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Errorf("decode OTA bucket creation: %v", err)
			}
			requestedMetricsEndpoint = request["endpoint_type"] == "E3"
			created = true
			_, _ = fmt.Fprintf(w, `{"label":%q,"region":"us-sea","endpoint_type":%q,"s3_endpoint":%q}`, bucketName, bucketType, server.URL)
			return
		case r.URL.Path == "/v4/object-storage/keys" && r.Method == http.MethodGet:
			if keyIssued {
				_, _ = fmt.Fprintf(w, `{"data":[{"id":303,"access_key":"ota-access","bucket_access":[{"bucket_name":%q,"region":"us-sea","permissions":"read_write"}]}]}`, bucketName)
			} else {
				_, _ = w.Write([]byte(`{"data":[]}`))
			}
			return
		case r.URL.Path == "/v4/object-storage/keys" && r.Method == http.MethodPost:
			keyIssued = true
			keyIssueCount++
			_, _ = w.Write([]byte(`{"access_key":"ota-access","secret_key":"ota-secret"}`))
			return
		}
		if !strings.HasPrefix(r.URL.Path, "/"+bucketName) {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("list-type") == "2" {
			mu.Lock()
			defer mu.Unlock()
			_, _ = w.Write([]byte(`<ListBucketResult><IsTruncated>false</IsTruncated>`))
			for key := range objects {
				if strings.HasPrefix(key, r.URL.Query().Get("prefix")) {
					_, _ = fmt.Fprintf(w, `<Contents><Key>%s</Key></Contents>`, key)
				}
			}
			_, _ = w.Write([]byte(`</ListBucketResult>`))
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/"+bucketName+"/")
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			body, _ := io.ReadAll(r.Body)
			objects[key] = body
		case http.MethodGet:
			body, ok := objects[key]
			if !ok {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(body)
		case http.MethodDelete:
			delete(objects, key)
		default:
			http.Error(w, "method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	t.Setenv("RTK_CLOUD_LINODE_API_ROOT", server.URL+"/v4")
	cfg := deploymentConfig{Environment: "dev", RuntimeRoot: t.TempDir(), Values: map[string]string{"VIDEO_CLOUD_OTA_CDN_BASE_URL": "https://firmware.example.test"}, AdapterResolved: map[string]string{"LKE_REGION": "us-sea"}, Storage: deploymentStoragePlan{RuntimeMedia: deploymentStorageTarget{Purpose: "runtime-media", Bucket: "rtk-video-media-dev-us-sea", Region: "us-sea"}, OTAMode: "dedicated", OTAFirmware: deploymentStorageTarget{Purpose: "ota-firmware", Policy: "colocated", Bucket: bucketName, Prefix: "environments/video-cloud-dev", Region: "us-sea"}, ReleaseArtifacts: deploymentStorageTarget{Purpose: "release-artifacts", Bucket: "rtk-release-shared-us-sea", Region: "us-sea"}}}
	plan := captureStdout(t, func() {
		if err := runDeploymentStorageLifecyclePurpose("storage-plan", cfg, profile, "", 0, "ota"); err != nil {
			t.Fatal(err)
		}
	})
	if !strings.Contains(plan, "available E3") || strings.Contains(plan, "assigned E3;") {
		t.Fatalf("first-bucket plan misreported unassigned E3 availability: %s", plan)
	}
	if err := runDeploymentStorageLifecyclePurpose("storage-bootstrap", cfg, profile, "", 0, "ota"); err != nil {
		t.Fatal(err)
	}
	if !created || !keyIssued || !requestedMetricsEndpoint {
		t.Fatal("OTA E3 bucket or limited key was not created")
	}
	values, check := deploymentCredentialProfileValues("dev", profile, "")
	if !check.Passed || values["LINODE_OTA_OBJ_ACCESS_KEY_ID"] != "ota-access" {
		t.Fatalf("OTA credential profile not updated: %s", check.Detail)
	}
	checker := deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL + "/v4"}
	if check := checker.checkResolvedOTAStorage(cfg, values); !check.Passed {
		t.Fatal(check.Detail)
	}
	readOnlyChecker := checker
	readOnlyChecker.readOnly = true
	if check := readOnlyChecker.checkResolvedOTAStorage(cfg, values); !check.Passed {
		t.Fatalf("OTA read-only storage qualification failed: %s", check.Detail)
	}
	if _, err := os.Stat(filepath.Join(cfg.RuntimeRoot, "state", "storage-preflight-ota.json")); err != nil {
		t.Fatal(err)
	}

	sourceRoot := t.TempDir()
	key := "ota-billable-v1/brand/product/release/firmware.bin"
	path := filepath.Join(sourceRoot, "source-bucket", filepath.FromSlash(key))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("billable-firmware"), 0o600); err != nil {
		t.Fatal(err)
	}
	sourceFile := filepath.Join(t.TempDir(), "source.env")
	if err := os.WriteFile(sourceFile, []byte("LINODE_OBJ_BUCKET=source-bucket\nLINODE_OBJ_ENDPOINT=file://"+sourceRoot+"\nLINODE_OBJ_REGION=us-sea\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := runDeploymentStorageLifecyclePurpose("storage-migrate", cfg, profile, sourceFile, 0, "ota"); err != nil {
		t.Fatal(err)
	}
	destinationKey := "environments/video-cloud-dev/" + key
	mu.Lock()
	copied := string(objects[destinationKey])
	mu.Unlock()
	if copied != "billable-firmware" {
		t.Fatalf("billable OTA object copied as %q", copied)
	}
	var migration deploymentStorageMigrationState
	body, err := os.ReadFile(filepath.Join(cfg.RuntimeRoot, "state", "storage-migration-ota.json"))
	if err != nil || json.Unmarshal(body, &migration) != nil || migration.ObjectCount != 1 {
		t.Fatalf("OTA migration receipt = %#v, error = %v", migration, err)
	}
	mu.Lock()
	objects[destinationKey] = []byte("corrupt-firmware")
	mu.Unlock()
	if err := checker.migrateStoragePurpose(cfg, values, sourceFile, "ota"); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("receipt-listed OTA destination was not rechecked: %v", err)
	}
	mu.Lock()
	objects[destinationKey] = []byte("billable-firmware")
	mu.Unlock()
	if err := os.WriteFile(path, []byte("different-firmware"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checker.migrateStoragePurpose(cfg, values, sourceFile, "ota"); err == nil || !strings.Contains(err.Error(), "differs from migration receipt") {
		t.Fatalf("receipt-listed OTA source was not rechecked: %v", err)
	}
	if err := os.Remove(filepath.Join(cfg.RuntimeRoot, "state", "storage-migration-ota.json")); err != nil {
		t.Fatal(err)
	}
	if err := checker.migrateStoragePurpose(cfg, values, sourceFile, "ota"); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("existing OTA firmware conflict was not rejected: %v", err)
	}
	mu.Lock()
	unchanged := string(objects[destinationKey])
	mu.Unlock()
	if unchanged != "billable-firmware" {
		t.Fatalf("conflicting OTA migration overwrote destination: %q", unchanged)
	}
	if err := os.WriteFile(path, []byte("billable-firmware"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checker.migrateStoragePurpose(cfg, values, sourceFile, "ota"); err != nil {
		t.Fatalf("restore OTA migration receipt: %v", err)
	}

	workspace := writeDeploymentFixture(t, "dev", "lke")
	cutoverCfg, err := resolveDeploymentConfig(workspace, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	cutoverCfg.Storage = cfg.Storage
	if err := os.MkdirAll(filepath.Join(cutoverCfg.RuntimeRoot, "state"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := writeStorageState(filepath.Join(cutoverCfg.RuntimeRoot, "state", "storage-preflight-ota.json"), deploymentStorageReceipt{Environment: "dev", Purpose: "ota-firmware", Bucket: bucketName, Region: "us-sea", Endpoint: server.URL, EndpointType: "E3"}); err != nil {
		t.Fatal(err)
	}
	store, err := newSecretStore("", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(store.KubeconfigPath()), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.KubeconfigPath(), []byte("apiVersion: v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	kubectlLog := fakeKubectl(t)
	coreSourceDeployment := fmt.Sprintf(`{"metadata":{"name":"video-cloud-api"},"spec":{"template":{"spec":{"containers":[{"name":"app","env":[{"name":"VIDEO_CLOUD_BLOB_BUCKET","value":"source-bucket"},{"name":"VIDEO_CLOUD_BLOB_REGION","value":"us-sea"},{"name":"VIDEO_CLOUD_BLOB_ENDPOINT","value":%q},{"name":"VIDEO_CLOUD_BLOB_PREFIX","value":"environments/video-cloud-dev"}]}]}}}}`, "file://"+sourceRoot)
	t.Setenv("FAKE_WEBRTC_CORE_DEPLOYMENT_JSON", coreSourceDeployment)
	bucketType = "E1"
	if err := runDeploymentStorageLifecyclePurpose("storage-bootstrap", cfg, profile, "", 0, "ota"); err == nil || !strings.Contains(err.Error(), "requires an E3 bucket") || keyIssueCount != 1 {
		t.Fatalf("E1 OTA bootstrap rotated credentials or passed: error=%v, key issues=%d", err, keyIssueCount)
	}
	if err := runDeploymentStorageLifecyclePurpose("storage-cutover", cutoverCfg, profile, sourceFile, 0, "ota"); err == nil || !strings.Contains(err.Error(), "requires an E3 bucket") {
		t.Fatalf("E1 OTA bucket was allowed for direct-download cutover: %v", err)
	}
	bucketType = "E3"
	if err := runDeploymentStorageLifecyclePurpose("storage-cutover", cutoverCfg, profile, sourceFile, 0, "ota"); err == nil || !strings.Contains(err.Error(), "registration is disabled") {
		t.Fatalf("disabled OTA service was cut over: %v", err)
	}
	cutoverCfg.Values["LKE_OTA_SERVICE_REGISTRATION_ENABLED"] = "true"
	cutoverCfg.Values["VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED"] = "true"
	cutoverCfg.Values["LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED"] = "true"
	cutoverCfg.Values["LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] = "true"
	t.Setenv("FAKE_OTA_VIDEO_RUNTIME_SECRET_JSON", otaTestSecretJSON(t, map[string]string{
		"POSTGRES_PASSWORD": "test", "VIDEO_CLOUD_AUTH_SECRET": "test", "VIDEO_CLOUD_OTA_BFF_TOKEN": "test", "VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN": "test",
	}))
	t.Setenv("FAKE_OTA_WORKERS_RUNTIME_SECRET_JSON", otaTestSecretJSON(t, map[string]string{"VIDEO_CLOUD_BILLING_USAGE_TOKEN": "test"}))
	setFakeLKEPlatformIdentitySecrets(t, cutoverCfg.Values)
	if err := runDeploymentStorageLifecyclePurpose("storage-cutover", cutoverCfg, profile, sourceFile, 0, "ota"); err == nil || !strings.Contains(err.Error(), "metrics qualification receipt") {
		t.Fatalf("OTA direct cutover accepted missing metrics export evidence: %v", err)
	}
	archiveRelative := filepath.Join("artifacts", "ota-metrics", "dev-qualification.json")
	metricsNow := time.Now().UTC().Truncate(time.Minute)
	endpointHost := strings.TrimPrefix(server.URL, "http://")
	archive := otaMetricsMatrixFixture(t, bucketName+"."+endpointHost, endpointHost, metricsNow.Add(-5*time.Minute), 1, 32)
	if err := os.MkdirAll(filepath.Dir(filepath.Join(cutoverCfg.RuntimeRoot, archiveRelative)), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cutoverCfg.RuntimeRoot, archiveRelative), archive, 0o600); err != nil {
		t.Fatal(err)
	}
	archiveHash := sha256.Sum256(archive)
	writeOTAMetricsReceipt(t, cutoverCfg.RuntimeRoot, attachOTAMetricsProbeFixture(t, cutoverCfg.RuntimeRoot, otaMetricsQualification{
		Source: "akamai_cloud_pulse", Environment: "dev", Bucket: bucketName, BucketHostname: bucketName + "." + endpointHost, Region: "us-sea", Endpoint: server.URL,
		WindowStart: metricsNow.Add(-10 * time.Minute).Format(time.RFC3339), WindowEnd: metricsNow.Add(-2 * time.Minute).Format(time.RFC3339),
		ExportedAt: metricsNow.Add(-time.Minute).Format(time.RFC3339), RecordedBy: "test-operator",
		ExportFile: archiveRelative, ExportSHA256: hex.EncodeToString(archiveHash[:]),
		GETMetric: "obj_requests_get", GETRequests: 1, DownloadedBytesMetric: "obj_bytes_downloaded", DownloadedBytes: 32,
	}))
	if err := runDeploymentStorageLifecyclePurpose("storage-cutover", cutoverCfg, profile, sourceFile, 0, "ota"); err == nil || !strings.Contains(err.Error(), "migration receipt") {
		t.Fatalf("OTA cutover accepted missing migration inventory: %v", err)
	}
	migrationBody, err := os.ReadFile(filepath.Join(cfg.RuntimeRoot, "state", "storage-migration-ota.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cutoverCfg.RuntimeRoot, "state", "storage-migration-ota.json"), migrationBody, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(kubectlLog)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	t.Setenv("FAKE_WEBRTC_CORE_DEPLOYMENT_JSON", strings.Replace(coreSourceDeployment, "source-bucket", "wrong-empty-bucket", 1))
	if err := runDeploymentStorageLifecyclePurpose("storage-cutover", cutoverCfg, profile, sourceFile, 0, "ota"); err == nil || !strings.Contains(err.Error(), "does not match live core API bucket") {
		t.Fatalf("cutover accepted a different source bucket: %v", err)
	}
	after, err := os.ReadFile(kubectlLog)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if strings.Contains(string(after[len(before):]), "apply -f") {
		t.Fatal("OTA cutover mutated Kubernetes before verifying its live source bucket")
	}
	t.Setenv("FAKE_WEBRTC_CORE_DEPLOYMENT_JSON", coreSourceDeployment)
	candidate := filepath.Join(t.TempDir(), "ota-candidate.env")
	if err := stageDeploymentStorageProfile("dev", profile, candidate, true); err != nil {
		t.Fatal(err)
	}
	mockRoot := installStorageCutoverMock(t)
	var coreSource map[string]any
	if err := json.Unmarshal([]byte(coreSourceDeployment), &coreSource); err != nil {
		t.Fatal(err)
	}
	coreSource["apiVersion"], coreSource["kind"] = "apps/v1", "Deployment"
	metadata := storageCutoverMap(coreSource["metadata"])
	metadata["namespace"], metadata["uid"], metadata["resourceVersion"] = "video-cloud-dev-video-cloud", "core-source-uid", "1"
	storageCutoverMockWrite(t, mockRoot, coreSource)
	t.Setenv("FAKE_OTA_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"video-cloud-otaservice"},"ports":[{"port":18084,"targetPort":"http"}]}}`)
	if err := runDeploymentStorageLifecyclePurpose("storage-cutover", cutoverCfg, candidate, sourceFile, 0, "ota"); err == nil || !strings.Contains(err.Error(), "no ready registered endpoint") {
		t.Fatalf("OTA cutover accepted a Service without ready endpoints: %v", err)
	}
	if _, err := os.Stat(filepath.Join(cutoverCfg.RuntimeRoot, "state", "storage-cutover-ota.json")); !os.IsNotExist(err) {
		t.Fatalf("OTA cutover wrote receipt before Service readiness: %v", err)
	}
	journalPath, err := store.safePath(filepath.Join("migration-backup", "storage-cutover-ota.json"))
	if err != nil {
		t.Fatal(err)
	}
	journalBody, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	var journal storageCutoverJournal
	if json.Unmarshal(journalBody, &journal) != nil || journal.Status != "rolled-back" {
		t.Fatalf("failed OTA rollout did not restore its changes: %s", journal.Status)
	}
	kinds := map[string]bool{}
	for _, mutation := range journal.Mutations {
		kinds[mutation.Kind] = true
	}
	if !kinds["NetworkPolicy"] || !kinds["Service"] {
		t.Fatal("OTA cutover omitted private network policy or Service")
	}
	// A fresh reviewed attempt archives the completed rollback journal.
	if err := os.Rename(journalPath, journalPath+".rolled-back"); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_OTA_ENDPOINTSLICES_JSON", `{"items":[{"ports":[{"port":18084}],"endpoints":[{"addresses":["10.0.0.5"],"conditions":{"ready":true}}]}]}`)
	if err := runDeploymentStorageLifecyclePurpose("storage-cutover", cutoverCfg, candidate, sourceFile, 0, "ota"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cutoverCfg.RuntimeRoot, "state", "storage-cutover-ota.json")); err != nil {
		t.Fatal(err)
	}
}
