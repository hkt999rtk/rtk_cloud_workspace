package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestFreshCreateStorageQualificationUsesOriginalLimitedKeysReadOnly(t *testing.T) {
	for _, mode := range []string{"valid", "missing-secret", "unlimited-key", "wrong-region", "denied", "partial"} {
		t.Run(mode, func(t *testing.T) {
			cfg, operator := createProviderTestFixture(t, "dev")
			mutations := createProviderTestAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/object-storage/keys" || mode == "valid" || mode == "missing-secret" {
					return false
				}
				if mode == "denied" {
					w.WriteHeader(http.StatusForbidden)
					fmt.Fprint(w, "private-provider-body")
					return true
				}
				if mode == "partial" {
					fmt.Fprint(w, `{"data":[],"page":1,"pages":1,"results":2}`)
					return true
				}
				grant := []any{map[string]string{"bucket_name": cfg.Storage.RuntimeMedia.Bucket, "region": "us-sea", "permissions": "read_write"}}
				if mode == "unlimited-key" {
					grant = nil
				}
				if mode == "wrong-region" {
					grant = []any{map[string]string{"bucket_name": cfg.Storage.RuntimeMedia.Bucket, "region": "us-lax", "permissions": "read_write"}}
				}
				json.NewEncoder(w).Encode(map[string]any{"data": []any{map[string]any{"id": 21, "access_key": operator["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"], "bucket_access": grant}}, "page": 1, "pages": 1, "results": 1})
				return true
			})
			if mode == "missing-secret" {
				delete(operator, "LINODE_OTA_OBJ_SECRET_ACCESS_KEY")
			}
			err := qualifyEnvironmentCreateStorage(context.Background(), cfg, operator)
			if (err == nil) != (mode == "valid") || *mutations != 0 || err != nil && strings.Contains(err.Error(), "private-provider-body") {
				t.Fatalf("key qualification violated scope mode=%s err=%v mutations=%d", mode, err, *mutations)
			}
		})
	}
}

func TestFreshCreateStorageActivationRecordsActualCanariesAndCannotFakeCutover(t *testing.T) {
	for _, mode := range []string{"valid", "public-acl", "read-mismatch", "delete-not-absent", "missing-post-receipt"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, operatorSHA, artifact := createStateFixture(t, "dev")
			execution, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operatorSHA, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer execution.Close()
			cfg.Adapter = "lke"
			cfg.Storage = deploymentStoragePlan{OTAMode: "dedicated", RuntimeMedia: deploymentStorageTarget{Purpose: "runtime-media", Bucket: "rtk-cloud-dev-runtime-us-sea", Region: "us-sea", Prefix: "environments/video-cloud-dev"}, OTAFirmware: deploymentStorageTarget{Purpose: "ota-firmware", Bucket: "rtk-cloud-dev-ota-firmware-us-lax", Region: "us-lax", Prefix: "environments/video-cloud-dev"}}
			operator := map[string]string{"LINODE_TOKEN": "private-provider-token", "LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "private-media-key", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "private-media-secret", "LINODE_OTA_OBJ_ACCESS_KEY_ID": "private-ota-key", "LINODE_OTA_OBJ_SECRET_ACCESS_KEY": "private-ota-secret"}
			for key, value := range operator {
				if err := store.write("operator/env/"+key, []byte(value), true); err != nil {
					t.Fatal(err)
				}
			}
			objects := map[string][]byte{}
			puts, deletes := 0, 0
			s3 := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Has("acl") {
					if mode == "public-acl" {
						fmt.Fprint(w, strings.ReplaceAll(privateStorageACLForTest, "<ID>owner</ID></Grantee>", "<URI>http://acs.amazonaws.com/groups/global/AllUsers</URI></Grantee>"))
					} else {
						fmt.Fprint(w, privateStorageACLForTest)
					}
					return
				}
				if r.URL.Query().Has("policy") {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				if r.URL.Query().Has("list-type") {
					fmt.Fprint(w, "<ListBucketResult/>")
					return
				}
				switch r.Method {
				case http.MethodPut:
					puts++
					objects[r.URL.Path], _ = io.ReadAll(r.Body)
				case http.MethodDelete:
					deletes++
					if mode != "delete-not-absent" {
						delete(objects, r.URL.Path)
					}
				case http.MethodGet:
					if body, found := objects[r.URL.Path]; found {
						if mode == "read-mismatch" {
							fmt.Fprint(w, "changed")
						} else {
							w.Write(body)
						}
					} else {
						w.WriteHeader(http.StatusNotFound)
					}
				default:
					t.Errorf("unsupported storage mutation %s", r.Method)
				}
			}))
			defer s3.Close()
			createProviderTestAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path == "/object-storage/buckets" {
					json.NewEncoder(w).Encode(map[string]any{"page": 1, "pages": 1, "results": 2, "data": []map[string]string{
						{"label": cfg.Storage.RuntimeMedia.Bucket, "region": cfg.Storage.RuntimeMedia.Region, "s3_endpoint": s3.URL, "endpoint_type": "E1"},
						{"label": cfg.Storage.OTAFirmware.Bucket, "region": cfg.Storage.OTAFirmware.Region, "s3_endpoint": s3.URL, "endpoint_type": "E3"},
					}})
					return true
				}
				for _, target := range []deploymentStorageTarget{cfg.Storage.RuntimeMedia, cfg.Storage.OTAFirmware} {
					if r.URL.Path == "/object-storage/buckets/"+target.Region+"/"+target.Bucket {
						kind := "E1"
						if target.Purpose == "ota-firmware" {
							kind = "E3"
						}
						json.NewEncoder(w).Encode(map[string]string{"label": target.Bucket, "region": target.Region, "s3_endpoint": s3.URL, "endpoint_type": kind})
						return true
					}
				}
				return false
			})
			plan := &environmentCreatePlan{Environment: "dev", Stack: "video-cloud-dev"}
			receipts := map[string]environmentCreateProviderReceipt{}
			for _, target := range []deploymentStorageTarget{cfg.Storage.RuntimeMedia, cfg.Storage.OTAFirmware} {
				key := "bucket-" + target.Purpose
				body := map[string]any{"label": target.Bucket, "region": target.Region}
				if target.Purpose == "ota-firmware" {
					body["endpoint_type"] = "E3"
				}
				resource := environmentCreateProviderResource{Key: key, Kind: "bucket", Method: http.MethodPost, Path: "/object-storage/buckets", Name: target.Bucket, Region: target.Region, Body: body}
				plan.Provider.Resources = append(plan.Provider.Resources, resource)
				receipt := environmentCreateProviderReceipt{Key: key, Kind: "bucket", NativeID: target.Region + "/" + target.Bucket, Name: target.Bucket, Region: target.Region, S3Endpoint: s3.URL}
				receipts[key] = receipt
				if mode == "missing-post-receipt" {
					continue
				}
				intent, _ := json.Marshal(resource)
				proof, _ := json.Marshal(receipt)
				if err := execution.BeginEntry(key, "provider", intent, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				if err := execution.RecordReceipt(key, proof); err != nil {
					t.Fatal(err)
				}
				if err := execution.CompleteEntry(key, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			checker := defaultDeploymentCredentialChecker()
			checker.client = s3.Client()
			checker.linodeAPIRoot = os.Getenv("RTK_CLOUD_LINODE_API_ROOT")
			checker = checker.withCheckContext(context.Background())
			err = activateEnvironmentCreateStorageWithChecker(context.Background(), cfg, plan, receipts, store, operator, execution, checker)
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				if puts != 2 || deletes != 2 || len(objects) != 0 {
					t.Fatalf("actual exact cleanup missing puts=%d deletes=%d objects=%d", puts, deletes, len(objects))
				}
				if err := validateEnvironmentCreateStorageAllocation(cfg); err != nil {
					t.Fatal("genuine initial allocation not consumable", err)
				}
				if err := validateEnvironmentStorageCutover("dev", "ota", cfg.Storage.OTAFirmware); err != nil {
					t.Fatal("original fresh OTA activation mistaken for missing migration", err)
				}
				cfg.Storage.OTAFirmware.Prefix = "environments/other"
				if err := validateEnvironmentStorageCutover("dev", "ota", cfg.Storage.OTAFirmware); err == nil {
					t.Fatal("old origin authorized a changed allocation")
				}
				allocation := readTestFile(t, filepath.Join(store.Root, environmentCreateStorageAllocationPath))
				if strings.Contains(allocation, "private-media-secret") || strings.Contains(allocation, "private-ota-secret") {
					t.Fatal("storage credential leaked into metadata")
				}
			} else if err == nil {
				t.Fatal("unverified storage activation completed")
			}
			if mode == "public-acl" || mode == "missing-post-receipt" {
				if puts != 0 {
					t.Fatal("unverified ownership/privacy reached object write")
				}
			}
			if mode == "read-mismatch" && len(objects) != 0 {
				t.Fatal("failed readback left its owned exact canary")
			}
			for _, relative := range []string{"deployment/storage/storage-cutover.json", "deployment/storage/storage-cutover-ota.json", "deployment/storage/storage-migration.json", "deployment/storage/storage-migration-ota.json"} {
				if _, err := os.Lstat(filepath.Join(store.Root, relative)); !os.IsNotExist(err) {
					t.Fatal("fresh allocation fabricated older-data lifecycle proof", relative, err)
				}
			}
			_ = deploymentplan.Digest(plan)
		})
	}
}
