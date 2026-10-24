package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func readProviderStorageKeyFile(path string) ([]byte, error) {
	if _, err := os.Lstat(path); err != nil {
		return nil, err
	}
	return readBoundedEnvironmentFile(path, true)
}

func writeProviderStorageKeyValue(store secretStore, name string, previous, next []byte) error {
	path, err := store.safePath(filepath.Join("operator", "env", name))
	if err != nil {
		return err
	}
	current, err := readBoundedEnvironmentFile(path, true)
	if err != nil {
		return err
	}
	if string(current) != string(previous) && string(current) != string(next) {
		return createProviderRecoveryBlock("storage credential changed before promotion")
	}
	if err := store.write(filepath.Join("operator", "env", name), next, true); err != nil {
		return err
	}
	actual, err := readBoundedEnvironmentFile(path, true)
	if err != nil || string(actual) != string(next) {
		return createProviderRecoveryBlock("storage credential promotion readback failed")
	}
	return nil
}

// This is an explicitly confirmed new-generation preparation, never an
// automatic response to missing state or an existing-generation rotation.
// Every POST has a durable intent. An uncertain response must be reconciled
// manually; rerunning cannot allocate a second key. Original keys stay active.
func establishProviderRecoveryStorageKeys(ctx context.Context, cfg deploymentConfig, store secretStore, operator map[string]string, old *environmentCreateExecution, plan environmentCreatePlan, original string) error {
	receipts, kubeSHA, err := validateProviderOnlyCreateJournal(old.Journal, plan)
	if err != nil {
		return err
	}
	p := environmentCreateProviderRecoveryPlan{Plan: plan, Receipts: receipts, KubeconfigSHA256: kubeSHA}
	if err := verifyProviderRecoveryBaseline(ctx, cfg, store, p, operator); err != nil {
		return err
	}
	if err := qualifyProviderRecoveryBootstrap(ctx, cfg, plan.Bootstrap, store, operator); err != nil {
		return err
	}
	base := filepath.Join("deployment", "create", "recovery-storage-keys", original)
	if _, err := prepareEnvironmentCreateDirectories(store, base); err != nil {
		return err
	}
	checker := defaultDeploymentCredentialChecker().withCheckContext(ctx)
	checker.out = io.Discard
	type replacement struct {
		name           string
		previous, next []byte
	}
	var replacements []replacement
	var evidence []map[string]any
	for _, item := range []struct {
		purpose, prefix string
		target          deploymentStorageTarget
	}{
		{"media", "LINODE_MEDIA_OBJ_", cfg.Storage.RuntimeMedia},
		{"ota", "LINODE_OTA_OBJ_", cfg.Storage.OTAFirmware},
	} {
		fmt.Printf("[RUNNING] deployment.recovery.storage.keys.%s\n", item.purpose)
		dir := filepath.Join(base, item.purpose)
		if _, err := prepareEnvironmentCreateDirectories(store, filepath.Join(dir, "original")); err != nil {
			return err
		}
		originalValues := map[string]string{}
		for _, suffix := range []string{"ACCESS_KEY_ID", "SECRET_ACCESS_KEY"} {
			name := item.prefix + suffix
			activePath, err := store.safePath(filepath.Join("operator", "env", name))
			if err != nil {
				return err
			}
			active, err := readBoundedEnvironmentFile(activePath, true)
			if err != nil {
				return err
			}
			archive := filepath.Join(dir, "original", name)
			archivePath, err := store.safePath(archive)
			if err != nil {
				return err
			}
			preserved, err := readProviderStorageKeyFile(archivePath)
			if errors.Is(err, os.ErrNotExist) {
				if err := store.write(archive, active, false); err != nil {
					return err
				}
				preserved = active
			} else if err != nil {
				return err
			}
			originalValues[name] = strings.TrimSpace(string(preserved))
			replacements = append(replacements, replacement{name: name, previous: preserved})
		}
		previousKey, err := checker.resolveAuthorizedStorageKey(operator["LINODE_TOKEN"], originalValues[item.prefix+"ACCESS_KEY_ID"], item.target)
		if err != nil {
			return createProviderRecoveryBlock("original storage key identity cannot be verified; no key was revoked")
		}
		bucket, err := checker.resolveStorageBucket(operator["LINODE_TOKEN"], item.target)
		if err != nil {
			return err
		}
		endpoint, err := normalizeLinodeS3Endpoint(bucket.S3Endpoint)
		if err != nil {
			return err
		}
		label := "rtk-cloud-dev-new-" + item.purpose + "-" + original[:16]
		request, err := json.Marshal(map[string]any{"label": label, "bucket_access": []map[string]string{{"bucket_name": item.target.Bucket, "region": item.target.Region, "permissions": "read_write"}}})
		if err != nil {
			return err
		}
		responsePath, err := store.safePath(filepath.Join(dir, "native-key-response.json"))
		if err != nil {
			return err
		}
		response, err := readProviderStorageKeyFile(responsePath)
		if errors.Is(err, os.ErrNotExist) {
			intent := filepath.Join(dir, "create-intent.json")
			intentPath, err := store.safePath(intent)
			if err != nil {
				return err
			}
			if _, err := os.Lstat(intentPath); !errors.Is(err, os.ErrNotExist) {
				return createProviderRecoveryBlock("storage key creation has an uncertain prior intent; reconcile the exact native label before retrying")
			}
			if err := old.Check(ctx); err != nil {
				return err
			}
			if err := store.write(intent, request, false); err != nil {
				return err
			}
			response, err = checker.linodeAuthorizedRequest(operator["LINODE_TOKEN"], http.MethodPost, "/object-storage/keys", request)
			if err != nil {
				return createProviderRecoveryBlock("storage key request did not yield durable credentials; preserve its intent and reconcile before retrying")
			}
			// Persist the one-time secret before parsing or performing any further API request.
			if err := store.write(filepath.Join(dir, "native-key-response.json"), response, false); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		var created struct {
			ID     int    `json:"id"`
			Label  string `json:"label"`
			Access string `json:"access_key"`
			Secret string `json:"secret_key"`
		}
		if json.Unmarshal(response, &created) != nil || created.ID <= 0 || created.ID == previousKey.ID || created.Label != label || created.Access == "" || created.Secret == "" || created.Access == previousKey.AccessKey {
			return createProviderRecoveryBlock("preserved new storage key response does not match its exact generation intent")
		}
		key, err := checker.resolveAuthorizedStorageKey(operator["LINODE_TOKEN"], created.Access, item.target)
		if err != nil || key.ID != created.ID {
			return createProviderRecoveryBlock("new key does not have the exact single-bucket read_write grant")
		}
		if err := checker.validateNewStorageKey(provisionObjectStore{bucket: item.target.Bucket, region: item.target.Region, endpoint: endpoint, accessKey: created.Access, secretKey: created.Secret}, item.target.Prefix); err != nil {
			return createProviderRecoveryBlock("new storage key private ACL/policy or read/write/delete validation failed; preserved credentials allow revalidation")
		}
		replacements[len(replacements)-2].next = []byte(created.Access + "\n")
		replacements[len(replacements)-1].next = []byte(created.Secret + "\n")
		evidence = append(evidence, map[string]any{"purpose": item.purpose, "bucket": item.target.Bucket, "region": item.target.Region, "original_key_id": previousKey.ID, "new_key_id": created.ID, "single_bucket_read_write": true, "private_acl_policy_verified": true, "list_write_read_delete_verified": true, "original_key_retained": true})
		fmt.Printf("[PASS] deployment.recovery.storage.keys.%s\n", item.purpose)
	}
	// All four originals were preserved byte-for-byte before any promotion.
	// Partial file promotion is resumable only against these exact old/new bytes.
	for _, r := range replacements {
		path, err := store.safePath(filepath.Join("operator", "env", r.name))
		if err != nil {
			return err
		}
		current, err := readBoundedEnvironmentFile(path, true)
		if err != nil {
			return err
		}
		if string(current) != string(r.previous) && string(current) != string(r.next) {
			return createProviderRecoveryBlock("active storage credential changed concurrently; preserved originals will not be overwritten")
		}
	}
	if err := old.Check(ctx); err != nil {
		return err
	}
	manifest := map[string]any{"environment": "dev", "original_plan_sha256": original, "validated_at": time.Now().UTC(), "keys": evidence, "old_keys_revoked": false}
	if err := store.write(filepath.Join(base, "verified-promotion-intent.json"), mustProviderRecoveryJSON(manifest), true); err != nil {
		return err
	}
	for _, r := range replacements {
		if err := writeProviderStorageKeyValue(store, r.name, r.previous, r.next); err != nil {
			return err
		}
	}
	manifest["completed_at"] = time.Now().UTC()
	if err := store.write(filepath.Join(base, "completed.json"), mustProviderRecoveryJSON(manifest), true); err != nil {
		return err
	}
	fmt.Println("[PASS] deployment.recovery.storage.keys.promoted original-keys-retained=true")
	return nil
}
