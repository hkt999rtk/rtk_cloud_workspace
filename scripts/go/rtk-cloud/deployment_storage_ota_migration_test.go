package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestOTACutoverReconcilesLiveSourceReceiptAndDestination(t *testing.T) {
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", t.TempDir())
	const bucket = "rtk-ota-firmware-dev-us-sea"
	const prefix = "environments/video-cloud-dev"
	const sourceKey = "ota-billable-v1/brand/product/release/firmware.bin"
	destinationKey := prefix + "/" + sourceKey
	sourceRoot := t.TempDir()
	sourceBucket := filepath.Join(sourceRoot, "old-media-bucket")
	if err := os.MkdirAll(filepath.Dir(filepath.Join(sourceBucket, filepath.FromSlash(sourceKey))), 0o700); err != nil {
		t.Fatal(err)
	}
	sourcePath := filepath.Join(sourceBucket, filepath.FromSlash(sourceKey))
	firmware := []byte("verified firmware")
	if err := os.WriteFile(sourcePath, firmware, 0o600); err != nil {
		t.Fatal(err)
	}
	sourceFile := filepath.Join(t.TempDir(), "source.env")
	if err := os.WriteFile(sourceFile, []byte("LINODE_OBJ_BUCKET=old-media-bucket\nLINODE_OBJ_ENDPOINT=file://"+sourceRoot+"\nLINODE_OBJ_REGION=us-sea\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	destinationObjects := map[string][]byte{destinationKey: firmware}
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
		case r.URL.Path == "/v4/object-storage/buckets":
			_, _ = fmt.Fprintf(w, `{"data":[{"label":%q,"region":"us-sea","s3_endpoint":%q}]}`, bucket, server.URL)
		case r.URL.Path == "/"+bucket && r.URL.Query().Get("list-type") == "2":
			_, _ = w.Write([]byte(`<ListBucketResult><IsTruncated>false</IsTruncated>`))
			for key := range destinationObjects {
				if strings.HasPrefix(key, r.URL.Query().Get("prefix")) {
					_, _ = fmt.Fprintf(w, `<Contents><Key>%s</Key></Contents>`, key)
				}
			}
			_, _ = w.Write([]byte(`</ListBucketResult>`))
		case strings.HasPrefix(r.URL.Path, "/"+bucket+"/"):
			key := strings.TrimPrefix(r.URL.Path, "/"+bucket+"/")
			body, found := destinationObjects[key]
			if !found {
				http.NotFound(w, r)
				return
			}
			_, _ = w.Write(body)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := deploymentConfig{Environment: "dev", RuntimeRoot: t.TempDir(), Storage: deploymentStoragePlan{OTAFirmware: deploymentStorageTarget{Bucket: bucket, Region: "us-sea", Prefix: prefix}}}
	checker := deploymentCredentialChecker{client: server.Client(), linodeAPIRoot: server.URL + "/v4"}
	values := map[string]string{"LINODE_TOKEN": "token", "LINODE_OTA_OBJ_ACCESS_KEY_ID": "access", "LINODE_OTA_OBJ_SECRET_ACCESS_KEY": "secret"}
	statePath := mustStorageStatePath(t, cfg, "storage-migration-ota.json")
	if err := checker.validateOTAMigrationCutover(cfg, values, sourceFile); err == nil || !strings.Contains(err.Error(), "migration receipt") {
		t.Fatalf("missing migration receipt accepted: %v", err)
	}
	source := provisionObjectStore{bucket: "old-media-bucket", endpoint: "file://" + sourceRoot, region: "us-sea"}
	destination := provisionObjectStore{bucket: bucket, endpoint: server.URL, region: "us-sea", accessKey: "access", secretKey: "secret"}
	receipt := deploymentStorageMigrationState{
		Environment: "dev", Purpose: "ota-firmware", SourceKeys: map[string]string{sourceKey: destinationKey}, Source: source.bucket, SourceRegion: source.region, SourceEndpoint: source.endpoint,
		Destination: destination.bucket, DestinationRegion: destination.region, DestinationEndpoint: destination.endpoint,
		Prefix: prefix, Objects: map[string]storageObjectProof{destinationKey: otaObjectProof(firmware)},
		ObjectCount: 1, ByteCount: int64(len(firmware)), UpdatedAt: time.Now().UTC().Format(time.RFC3339),
	}
	writeReceipt := func() {
		t.Helper()
		if err := writeStorageState(statePath, receipt); err != nil {
			t.Fatal(err)
		}
	}
	writeReceipt()
	if err := checker.validateOTAMigrationCutover(cfg, values, sourceFile); err != nil {
		t.Fatalf("matching source, receipt and destination rejected: %v", err)
	}
	destinationObjects[destinationKey] = []byte("changed destination")
	if err := checker.validateOTAMigrationCutover(cfg, values, sourceFile); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("changed destination accepted: %v", err)
	}
	destinationObjects[destinationKey] = firmware
	newSource := filepath.Join(sourceBucket, "ota-billable-v1", "brand", "product", "other", "firmware.bin")
	if err := os.MkdirAll(filepath.Dir(newSource), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newSource, []byte("new firmware"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := checker.validateOTAMigrationCutover(cfg, values, sourceFile); err == nil || !strings.Contains(err.Error(), "receipt does not match source") {
		t.Fatalf("new source object accepted without migration: %v", err)
	}
	if err := os.Remove(newSource); err != nil {
		t.Fatal(err)
	}
	destinationObjects[prefix+"/ota-billable-v1/unexpected.bin"] = []byte("unverified")
	if err := checker.validateOTAMigrationCutover(cfg, values, sourceFile); err == nil || !strings.Contains(err.Error(), "unverified object") {
		t.Fatalf("extra destination object accepted: %v", err)
	}
	delete(destinationObjects, prefix+"/ota-billable-v1/unexpected.bin")
	receipt.ByteCount++
	writeReceipt()
	if err := checker.validateOTAMigrationCutover(cfg, values, sourceFile); err == nil || !strings.Contains(err.Error(), "totals") {
		t.Fatalf("incorrect receipt totals accepted: %v", err)
	}
	if err := os.Remove(sourcePath); err != nil {
		t.Fatal(err)
	}
	delete(destinationObjects, destinationKey)
	if err := os.Remove(statePath); err != nil {
		t.Fatal(err)
	}
	if err := checker.migrateOTAFirmwareObjects(cfg, source, destination, statePath); err != nil {
		t.Fatalf("verified empty source migration failed: %v", err)
	}
	body, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	var empty deploymentStorageMigrationState
	if err := json.Unmarshal(body, &empty); err != nil || empty.ObjectCount != 0 || empty.ByteCount != 0 || empty.Objects == nil {
		t.Fatalf("empty source receipt = %+v, error = %v", empty, err)
	}
	if err := checker.validateOTAMigrationCutover(cfg, values, sourceFile); err != nil {
		t.Fatalf("verified empty source cutover blocked: %v", err)
	}
}

func TestOTACutoverRequiresCurrentCoreSourceBucket(t *testing.T) {
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", t.TempDir())
	deployment := []byte(`{"metadata":{"name":"video-cloud-api"},"spec":{"template":{"spec":{"containers":[{"name":"app","env":[{"name":"VIDEO_CLOUD_BLOB_BUCKET","value":"live-media"},{"name":"VIDEO_CLOUD_BLOB_REGION","value":"us-sea"},{"name":"VIDEO_CLOUD_BLOB_ENDPOINT","value":"https://us-sea-1.linodeobjects.com"},{"name":"VIDEO_CLOUD_BLOB_PREFIX","value":"environments/video-cloud-dev"}]}]}}}}`)
	if err := validateLiveOTASourceBucket(deployment, "live-media", "us-sea", "us-sea-1.linodeobjects.com/", "environments/video-cloud-dev"); err != nil {
		t.Fatalf("matching live source rejected: %v", err)
	}
	if err := validateLiveOTASourceBucket(deployment, "wrong-empty-bucket", "us-sea", "https://us-sea-1.linodeobjects.com", "environments/video-cloud-dev"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong empty source accepted: %v", err)
	}
	if err := validateLiveOTASourceBucket(deployment, "live-media", "sg-sin-2", "https://us-sea-1.linodeobjects.com", "environments/video-cloud-dev"); err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("wrong source region accepted: %v", err)
	}
	if err := validateLiveOTASourceBucket(deployment, "live-media", "us-sea", "https://other-account.example.test", "environments/video-cloud-dev"); err == nil || !strings.Contains(err.Error(), "endpoint") {
		t.Fatalf("wrong source endpoint accepted: %v", err)
	}
	if err := validateLiveOTASourceBucket(deployment, "live-media", "us-sea", "https://us-sea-1.linodeobjects.com", "another-prefix"); err == nil || !strings.Contains(err.Error(), "prefix") {
		t.Fatalf("unlisted source prefix accepted: %v", err)
	}
	if err := validateLiveOTASourceBucket(nil, "live-media", "us-sea", "https://us-sea-1.linodeobjects.com", "environments/video-cloud-dev"); err == nil {
		t.Fatal("missing live core source accepted")
	}
}
