package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestProviderStorageRecoveryReadsCompleteCleanupReceiptAndRequiresLive404(t *testing.T) {
	for _, mode := range []string{"absent", "occupied", "denied", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			cfg, operator := createProviderTestFixture(t, "dev")
			target := cfg.Storage.RuntimeMedia
			object := target.Prefix + "/__rtk_cloud_create_validation__/original/runtime-media"
			reads, writes := 0, 0
			s3 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet {
					writes++
					w.WriteHeader(405)
					return
				}
				reads++
				if r.URL.Path != "/"+target.Bucket+"/"+object {
					t.Errorf("wrong canary readback path %s", r.URL.Path)
				}
				status := http.StatusNotFound
				if mode == "occupied" {
					status = http.StatusOK
				}
				if mode == "denied" {
					status = http.StatusForbidden
				}
				w.WriteHeader(status)
			}))
			defer s3.Close()
			createProviderTestAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/object-storage/buckets" {
					return false
				}
				json.NewEncoder(w).Encode(map[string]any{"page": 1, "pages": 1, "results": 1, "data": []map[string]string{{"label": target.Bucket, "region": target.Region, "s3_endpoint": s3.URL, "endpoint_type": "E1"}}})
				return true
			})
			proof := mustProviderRecoveryJSON(deploymentplan.Object{"bucket": target.Bucket, "region": target.Region, "object": object, "body_sha256": strings.Repeat("a", 64), "deleted_verified_http": 404})
			if mode == "malformed" {
				proof = []byte(`{"object":"wrong","unexpected":true}`)
			}
			j := environmentCreateJournal{Entries: make([]environmentCreateJournalEntry, 8)}
			j.Entries[7] = environmentCreateJournalEntry{Key: "storage-canary-runtime-media", Receipt: proof}
			checker := defaultDeploymentCredentialChecker()
			checker.client = s3.Client()
			checker.linodeAPIRoot = os.Getenv("RTK_CLOUD_LINODE_API_ROOT")
			err := verifyProviderRecoveryCanaryAbsenceWithChecker(context.Background(), cfg, j, operator, checker)
			if (err == nil) != (mode == "absent") {
				t.Fatalf("unexpected live cleanup qualification: %v", err)
			}
			if writes != 0 || mode != "malformed" && reads != 1 || mode == "malformed" && reads != 0 {
				t.Fatal("cleanup qualification mutated objects or failed to read its complete receipt")
			}
		})
	}
}

func TestCreateStorageObservationRetainsOriginalTimeAndRejectsAllocationDrift(t *testing.T) {
	for _, mode := range []string{"same", "changed-endpoint", "changed-type", "invalid-hash", "future-time"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			store := secretStore{ConfigRoot: root, Environment: "dev", Root: filepath.Join(root, "dev")}
			now := time.Now().UTC()
			o := environmentOTAStorageObservation{Version: 1, Environment: "dev", Provider: "linode", Bucket: "media", Region: "us-sea", Prefix: "dev", Endpoint: "https://us-sea-1.linodeobjects.com", EndpointType: "E1"}
			record := environmentOTAStorageRecord{Observation: o, SHA256: deploymentplan.Digest(o), ObservedAt: now.Add(-48 * time.Hour).Format(time.RFC3339)}
			if mode == "invalid-hash" {
				record.SHA256 = strings.Repeat("f", 64)
			}
			if mode == "future-time" {
				record.ObservedAt = now.Add(time.Hour).Format(time.RFC3339)
			}
			bytes, _ := json.MarshalIndent(record, "", "  ")
			if err := store.write(environmentMediaObservationPath, bytes, false); err != nil {
				t.Fatal(err)
			}
			if mode == "changed-endpoint" {
				o.Endpoint = "https://other.linodeobjects.com"
			}
			if mode == "changed-type" {
				o.EndpointType = "E3"
			}
			err := retainEnvironmentCreateStorageObservation(store, environmentMediaObservationPath, o, now)
			if (err == nil) != (mode == "same") {
				t.Fatalf("unexpected metadata retention result: %v", err)
			}
			actual, err := os.ReadFile(filepath.Join(store.Root, environmentMediaObservationPath))
			if err != nil || string(actual) != string(bytes) {
				t.Fatal("retained observation bytes/time changed")
			}
		})
	}
}

func TestProviderStorageRecoveryRejectsUnknownWritesAndFalseCleanup(t *testing.T) {
	for _, mode := range []string{"valid", "pending-canary", "wrong-object", "false-cleanup", "unknown-stage", "lease", "storage-complete", "bad-intent"} {
		t.Run(mode, func(t *testing.T) {
			j, p := providerRecoveryJournalFixture()
			originalAdmission := j.AdmittedAt
			j.OriginalJournal = "deployment/create/executions/original/journal.json"
			j.AdmittedAt = time.Now().UTC().Add(-time.Second)
			cfg := deploymentConfig{Storage: deploymentStoragePlan{RuntimeMedia: deploymentStorageTarget{Purpose: "runtime-media", Bucket: p.Provider.Resources[3].Name, Region: "us-sea", Prefix: "environments/video-cloud-dev"}}}
			target := cfg.Storage.RuntimeMedia
			object := target.Prefix + "/__rtk_cloud_create_validation__/" + j.PlanSHA256 + "/" + target.Purpose
			bodySHA := environmentPreflightBytesSHA256([]byte("rtk-cloud-create-storage-canary"))
			intent, _ := json.Marshal(deploymentplan.Object{"bucket": target.Bucket, "region": target.Region, "object": object, "body_sha256": bodySHA})
			intentSHA, _ := environmentCreatePlanSHA256(intent)
			proof := deploymentplan.Object{"bucket": target.Bucket, "region": target.Region, "object": object, "body_sha256": bodySHA, "deleted_verified_http": 404}
			if mode == "wrong-object" {
				proof["object"] = "unrelated-object"
			}
			if mode == "false-cleanup" {
				proof["deleted_verified_http"] = 200
			}
			entry := environmentCreateJournalEntry{Key: "storage-canary-runtime-media", Phase: "storage", State: "completed", IntentSHA256: intentSHA, Receipt: mustProviderRecoveryJSON(proof), StartedAt: j.AdmittedAt, CompletedAt: time.Now().UTC()}
			if mode == "pending-canary" {
				entry.State = "pending"
			}
			if mode == "unknown-stage" {
				entry.Key = "kubernetes/foundation"
			}
			if mode == "bad-intent" {
				entry.IntentSHA256 = strings.Repeat("f", 64)
			}
			if mode == "lease" {
				j.KubernetesLeases = []deploymentplan.LeaseOwner{{UID: "actual-lease"}}
			}
			if mode == "storage-complete" {
				j.Entries[6].State = "completed"
			}
			j.Entries = append(j.Entries, entry)
			_, _, err := validateProviderStorageRecoveryJournal(j, p, cfg, originalAdmission)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("unexpected recovery admission: %v", err)
			}
		})
	}
}
