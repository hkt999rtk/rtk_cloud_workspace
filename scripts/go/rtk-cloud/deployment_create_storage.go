package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const environmentCreateStorageAllocationPath = "deployment/storage/create-allocation.json"

type environmentCreateStorageBucketAllocation struct {
	Key          string    `json:"key"`
	NativeID     string    `json:"native_id"`
	Bucket       string    `json:"bucket"`
	Region       string    `json:"region"`
	Prefix       string    `json:"prefix"`
	Endpoint     string    `json:"endpoint"`
	EndpointType string    `json:"endpoint_type,omitempty"`
	LimitedKeyID int       `json:"limited_key_id"`
	AccessSuffix string    `json:"access_suffix"`
	ValidatedAt  time.Time `json:"validated_at"`
}

type environmentCreateStorageAllocation struct {
	Schema      string                                     `json:"schema"`
	Environment string                                     `json:"environment"`
	Stack       string                                     `json:"stack"`
	PlanSHA256  string                                     `json:"plan_sha256"`
	Journal     string                                     `json:"journal"`
	Buckets     []environmentCreateStorageBucketAllocation `json:"buckets"`
}

type environmentCreateStorageActivationProof struct {
	AllocationSHA256 string                                     `json:"allocation_sha256"`
	AllocationRef    string                                     `json:"allocation_ref"`
	Buckets          []environmentCreateStorageBucketAllocation `json:"buckets"`
}

func qualifyEnvironmentCreateStorage(ctx context.Context, cfg deploymentConfig, operator map[string]string) error {
	if cfg.Storage.OTAMode != "dedicated" || cfg.Storage.RuntimeMedia.Bucket == cfg.Storage.OTAFirmware.Bucket {
		return createProviderBlocker("fresh runtime/OTA activation requires the maintained separate private OTA E3 bucket policy")
	}
	keys, err := environmentCreateProviderList(ctx, operator["LINODE_TOKEN"], "/object-storage/keys")
	if err != nil {
		return createProviderBlocker("complete selected storage access-key inventory cannot be read")
	}
	for _, target := range []deploymentStorageTarget{cfg.Storage.RuntimeMedia, cfg.Storage.OTAFirmware} {
		access, secret := environmentCreateStorageCredentials(operator, target.Purpose)
		if access == "" || secret == "" || strings.ContainsAny(access+secret, "\r\n") {
			return createProviderBlocker("canonical separate media/OTA storage credentials are unavailable or invalid")
		}
		found := false
		for _, raw := range keys {
			var key linodeStorageKey
			if json.Unmarshal(raw, &key) != nil || key.ID <= 0 {
				return createProviderBlocker("storage access-key inventory contains an invalid native ID")
			}
			if key.AccessKey != access {
				continue
			}
			if found || len(key.BucketAccess) != 1 {
				return createProviderBlocker("selected storage key ownership is duplicated or is not limited to one exact bucket")
			}
			grant := key.BucketAccess[0]
			if grant.BucketName != target.Bucket || grant.Region != target.Region || grant.Permissions != "read_write" {
				return createProviderBlocker("selected existing storage key does not have the exact maintained bucket/region read_write grant")
			}
			found = true
		}
		if !found {
			return createProviderBlocker("the original configured storage access key is absent; fresh creation cannot rotate or issue a substitute")
		}
	}
	return ctx.Err()
}

func environmentCreateStorageCredentials(operator map[string]string, purpose string) (string, string) {
	if purpose == "ota-firmware" {
		return strings.TrimSpace(operator["LINODE_OTA_OBJ_ACCESS_KEY_ID"]), strings.TrimSpace(operator["LINODE_OTA_OBJ_SECRET_ACCESS_KEY"])
	}
	return strings.TrimSpace(operator["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"]), strings.TrimSpace(operator["LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY"])
}

func activateEnvironmentCreateStorage(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, receipts map[string]environmentCreateProviderReceipt, store secretStore, operator map[string]string, execution *environmentCreateExecution) error {
	checker := defaultDeploymentCredentialChecker().withCheckContext(ctx)
	checker.out = io.Discard
	return activateEnvironmentCreateStorageWithChecker(ctx, cfg, plan, receipts, store, operator, execution, checker)
}

func activateEnvironmentCreateStorageWithChecker(ctx context.Context, cfg deploymentConfig, plan *environmentCreatePlan, receipts map[string]environmentCreateProviderReceipt, store secretStore, operator map[string]string, execution *environmentCreateExecution, checker deploymentCredentialChecker) error {
	if plan == nil || execution == nil || execution.Store.Root != store.Root || execution.Journal.PlanSHA256 == "" || store.Environment != cfg.Environment {
		return createProviderBlocker("original selected create plan and journal are required for fresh storage activation")
	}
	if err := qualifyEnvironmentCreateStorage(ctx, cfg, operator); err != nil {
		return err
	}
	intent, _ := json.Marshal(deploymentplan.Object{"root_plan_sha256": execution.Journal.PlanSHA256, "storage": cfg.Storage, "policy": "private-acl-no-public-grants", "canary": "signed-list-put-get-delete"})
	if err := execution.BeginEntry("activate-storage", "storage", intent, time.Now().UTC()); err != nil {
		return err
	}
	relative, err := filepath.Rel(store.Root, execution.JournalPath)
	if err != nil {
		return err
	}
	allocation := environmentCreateStorageAllocation{Schema: "rtk-cloud.create-storage-allocation/v1", Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], PlanSHA256: execution.Journal.PlanSHA256, Journal: filepath.ToSlash(relative)}
	for _, target := range []deploymentStorageTarget{cfg.Storage.RuntimeMedia, cfg.Storage.OTAFirmware} {
		key := "bucket-" + target.Purpose
		var resource environmentCreateProviderResource
		for _, item := range plan.Provider.Resources {
			if item.Key == key {
				resource = item
				break
			}
		}
		if resource.Kind != "bucket" || resource.Name != target.Bucket || resource.Region != target.Region {
			return createProviderBlocker("fresh storage target differs from the exact reviewed provider request")
		}
		if err := verifyEnvironmentCreateStorageJournalEntry(execution.Journal, key, receipts[key]); err != nil {
			return err
		}
		actual, ready, err := verifyEnvironmentCreateProviderResource(ctx, cfg, resource, receipts[key], operator)
		if err != nil || !ready {
			return createProviderBlocker("the exact originally allocated storage bucket cannot be read back")
		}
		access, secret := environmentCreateStorageCredentials(operator, target.Purpose)
		s3 := provisionObjectStore{bucket: target.Bucket, region: target.Region, endpoint: actual.S3Endpoint, accessKey: access, secretKey: secret}
		// Private ACL and absence of public policy grants are proven before the
		// deployment canary. No ACL/policy mutation or credential rotation occurs.
		if err := checker.validateStoragePrivacy(s3); err != nil {
			return createProviderBlocker("the newly allocated bucket cannot be verified private; public ACL or policy defaults cannot be accepted")
		}
		metadata, err := checker.resolveStorageBucket(operator["LINODE_TOKEN"], target)
		if err != nil {
			return err
		}
		endpoint, err := normalizeLinodeS3Endpoint(metadata.S3Endpoint)
		if err != nil || endpoint != actual.S3Endpoint {
			return createProviderBlocker("storage endpoint changed during activation")
		}
		observation := environmentOTAStorageObservation{Version: 1, Environment: cfg.Environment, Provider: "linode", Bucket: target.Bucket, Region: target.Region, Prefix: target.Prefix, Endpoint: endpoint, EndpointType: metadata.EndpointType}
		observationPath := environmentMediaObservationPath
		if target.Purpose == "ota-firmware" {
			observationPath = environmentOTAObservationPath
			err = validateOTAStorageObservation(cfg, observation)
		} else {
			err = validateMediaStorageObservation(cfg, observation)
		}
		if err != nil {
			return err
		}
		if err := retainEnvironmentCreateStorageObservation(store, observationPath, observation, time.Now().UTC()); err != nil {
			return err
		}
		if err := runEnvironmentCreateStorageCanary(ctx, checker, s3, target, execution); err != nil {
			return createProviderBlocker("fresh storage signed canary failed or its exact cleanup could not be verified")
		}
		limited, err := checker.resolveAuthorizedStorageKey(operator["LINODE_TOKEN"], access, target)
		if err != nil {
			return createProviderBlocker("selected storage key ownership changed during actual fresh activation")
		}
		item := environmentCreateStorageBucketAllocation{Key: key, NativeID: actual.NativeID, Bucket: target.Bucket, Region: target.Region, Prefix: target.Prefix, Endpoint: actual.S3Endpoint, EndpointType: metadata.EndpointType, LimitedKeyID: limited.ID, AccessSuffix: redactAccessKey(access), ValidatedAt: time.Now().UTC()}
		allocation.Buckets = append(allocation.Buckets, item)
	}
	body, _ := json.Marshal(allocation)
	if err := writeEnvironmentCreateDerivedPrivateFile(store, environmentCreateStorageAllocationPath, body); err != nil {
		return err
	}
	proof, _ := json.Marshal(environmentCreateStorageActivationProof{environmentPreflightBytesSHA256(body), environmentCreateStorageAllocationPath, allocation.Buckets})
	if err := execution.RecordReceipt("activate-storage", proof); err != nil {
		return err
	}
	return execution.CompleteEntry("activate-storage", time.Now().UTC())
}

// An observation is public provider metadata, not a generation identity or a
// canary receipt. A fresh native GET can verify an unchanged projection while
// preserving the original bytes and observation time.
func retainEnvironmentCreateStorageObservation(store secretStore, relative string, observed environmentOTAStorageObservation, now time.Time) error {
	path, err := store.safePath(relative)
	if err != nil {
		return err
	}
	raw, err := readProviderStorageKeyFile(path)
	if err == nil {
		var original environmentOTAStorageRecord
		if decodeEnvironmentJSON(raw, &original) != nil || original.SHA256 != deploymentplan.Digest(original.Observation) || original.Observation != observed {
			return createProviderBlocker("recorded storage metadata differs from actual provider allocation")
		}
		at, err := time.Parse(time.RFC3339, original.ObservedAt)
		if err != nil || at.After(now) {
			return createProviderBlocker("original storage observation time is invalid")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	record := environmentOTAStorageRecord{Observation: observed, SHA256: deploymentplan.Digest(observed), ObservedAt: now.Format(time.RFC3339)}
	return writeEnvironmentCreateDerivedPrivateFile(store, relative, mustProviderRecoveryJSON(record))
}

func runEnvironmentCreateStorageCanary(ctx context.Context, checker deploymentCredentialChecker, s3 provisionObjectStore, target deploymentStorageTarget, execution *environmentCreateExecution) (result error) {
	object := strings.Trim(target.Prefix, "/") + "/__rtk_cloud_create_validation__/" + execution.Journal.PlanSHA256 + "/" + target.Purpose
	body := []byte("rtk-cloud-create-storage-canary")
	if _, err := provisionSignedObjectRequestWithClient(checker.client, s3, http.MethodGet, "", url.Values{"list-type": {"2"}, "max-keys": {"1"}, "prefix": {target.Prefix + "/"}}, nil); err != nil {
		return err
	}
	_, err := provisionSignedObjectRequestWithClient(checker.client, s3, http.MethodGet, object, nil, nil)
	var absent *provisionObjectStorageHTTPError
	if !errors.As(err, &absent) || absent.StatusCode != http.StatusNotFound {
		return createProviderBlocker("reserved exact fresh canary object is occupied or absence cannot be verified")
	}
	key := "storage-canary-" + target.Purpose
	intent, _ := json.Marshal(deploymentplan.Object{"bucket": target.Bucket, "region": target.Region, "object": object, "body_sha256": environmentPreflightBytesSHA256(body)})
	if err := execution.BeginEntry(key, "storage", intent, time.Now().UTC()); err != nil {
		return err
	}
	cleaned := false
	defer func() {
		if cleaned {
			return
		}
		cleanupCtx, cancel := deploymentCleanupContext()
		defer cancel()
		cleanup := checker.withCheckContext(cleanupCtx)
		_, deleteErr := provisionSignedObjectRequestWithClient(cleanup.client, s3, http.MethodDelete, object, nil, nil)
		_, readErr := provisionSignedObjectRequestWithClient(cleanup.client, s3, http.MethodGet, object, nil, nil)
		var missing *provisionObjectStorageHTTPError
		if deleteErr != nil || !errors.As(readErr, &missing) || missing.StatusCode != http.StatusNotFound {
			result = errors.Join(result, createProviderBlocker("exact fresh canary cleanup cannot be verified; original private object intent is retained"))
		}
	}()
	if _, err := provisionSignedObjectRequestWithClient(checker.client, s3, http.MethodPut, object, nil, body); err != nil {
		return err
	}
	actual, err := provisionSignedObjectRequestWithClient(checker.client, s3, http.MethodGet, object, nil, nil)
	if err != nil || !bytes.Equal(actual, body) {
		return createProviderBlocker("fresh canary readback differs from its exact reserved object body")
	}
	if _, err := provisionSignedObjectRequestWithClient(checker.client, s3, http.MethodDelete, object, nil, nil); err != nil {
		return err
	}
	_, err = provisionSignedObjectRequestWithClient(checker.client, s3, http.MethodGet, object, nil, nil)
	absent = nil
	if !errors.As(err, &absent) || absent.StatusCode != http.StatusNotFound {
		return createProviderBlocker("deleted exact fresh canary still exists or absence cannot be verified")
	}
	cleaned = true
	proof, _ := json.Marshal(deploymentplan.Object{"bucket": target.Bucket, "region": target.Region, "object": object, "body_sha256": environmentPreflightBytesSHA256(body), "deleted_verified_http": 404})
	if err := execution.RecordReceipt(key, proof); err != nil {
		return err
	}
	return execution.CompleteEntry(key, time.Now().UTC())
}

func verifyEnvironmentCreateStorageJournalEntry(journal environmentCreateJournal, key string, receipt environmentCreateProviderReceipt) error {
	for _, entry := range journal.Entries {
		if entry.Key != key {
			continue
		}
		var original environmentCreateProviderReceipt
		if entry.Phase != "provider" || entry.State != "completed" || entry.CompletedAt.IsZero() || decodeEnvironmentJSON(entry.Receipt, &original) != nil || original.Key != receipt.Key || original.Kind != "bucket" || original.NativeID != receipt.NativeID || original.Name != receipt.Name || original.Region != receipt.Region || original.S3Endpoint != receipt.S3Endpoint {
			return createProviderBlocker("bucket activation lacks its exact original completed provider allocation receipt")
		}
		return nil
	}
	return createProviderBlocker("bucket activation has no original provider allocation entry")
}

// This receipt proves genuine fresh allocation and capability, never migration
// or cutover of prior data. Consumers also require the original root journal.
func validateEnvironmentCreateStorageAllocation(cfg deploymentConfig) error {
	path, err := privateDeploymentStatePath(cfg.Environment, environmentCreateStorageAllocationPath)
	if err != nil {
		return err
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	var allocation environmentCreateStorageAllocation
	if err != nil || decodeEnvironmentJSON(raw, &allocation) != nil || allocation.Schema != "rtk-cloud.create-storage-allocation/v1" || allocation.Environment != cfg.Environment || allocation.Stack != cfg.Values["CLOUD_STACK_NAME"] || !environmentPreflightDigest.MatchString(allocation.PlanSHA256) || len(allocation.Buckets) != 2 || !strings.HasPrefix(allocation.Journal, "deployment/create/executions/"+allocation.PlanSHA256+"/") || filepath.Clean(allocation.Journal) != allocation.Journal {
		return createProviderBlocker("original fresh allocation proof is unavailable or not bound to the selected environment")
	}
	journalPath, err := privateDeploymentStatePath(cfg.Environment, allocation.Journal)
	if err != nil {
		return err
	}
	journalRaw, err := readBoundedEnvironmentFile(journalPath, true)
	var journal environmentCreateJournal
	if err != nil || decodeEnvironmentJSON(journalRaw, &journal) != nil || journal.Schema != environmentCreateJournalSchema || journal.AdmittedAt.IsZero() || !environmentPreflightDigest.MatchString(journal.OwnerToken) || journal.Environment != cfg.Environment || journal.Stack != allocation.Stack || journal.PlanSHA256 != allocation.PlanSHA256 {
		return createProviderBlocker("fresh storage allocation does not match the original root creation journal")
	}
	if journal.CompletedAt.IsZero() {
		ownerPath, err := privateDeploymentStatePath(cfg.Environment, "deployment/.execution-active/owner.json")
		if err != nil {
			return err
		}
		ownerRaw, err := readBoundedEnvironmentFile(ownerPath, true)
		var owner environmentCreateOwner
		if err != nil || decodeEnvironmentJSON(ownerRaw, &owner) != nil || owner.Schema != environmentCreateOwnerSchema || owner.Environment != cfg.Environment || owner.PlanSHA256 != allocation.PlanSHA256 || owner.OwnerToken != journal.OwnerToken || owner.Journal != allocation.Journal {
			return createProviderBlocker("unfinished creation storage origin does not have its original active execution owner")
		}
	}
	for index, target := range []deploymentStorageTarget{cfg.Storage.RuntimeMedia, cfg.Storage.OTAFirmware} {
		item := allocation.Buckets[index]
		if item.Key != "bucket-"+target.Purpose || item.NativeID != target.Region+"/"+target.Bucket || item.Bucket != target.Bucket || item.Region != target.Region || item.Prefix != target.Prefix || item.LimitedKeyID <= 0 || !validOTAHTTPSURL(item.Endpoint) || item.ValidatedAt.Before(journal.AdmittedAt) || target.Purpose == "ota-firmware" && item.EndpointType != "E3" {
			return createProviderBlocker("fresh allocation metadata does not match the exact maintained bucket, key or E3 target")
		}
		if err := verifyEnvironmentCreateStorageJournalEntry(journal, item.Key, environmentCreateProviderReceipt{Key: item.Key, Kind: "bucket", NativeID: item.NativeID, Name: item.Bucket, Region: item.Region, S3Endpoint: item.Endpoint}); err != nil {
			return err
		}
	}
	for _, entry := range journal.Entries {
		if entry.Key == "activate-storage" {
			var proof environmentCreateStorageActivationProof
			if entry.State != "completed" || entry.CompletedAt.IsZero() || decodeEnvironmentJSON(entry.Receipt, &proof) != nil || proof.AllocationSHA256 != environmentPreflightBytesSHA256(raw) || proof.AllocationRef != environmentCreateStorageAllocationPath || deploymentplan.Digest(proof.Buckets) != deploymentplan.Digest(allocation.Buckets) {
				return createProviderBlocker("fresh storage activation lacks original completed exact receipt evidence")
			}
			return nil
		}
	}
	return createProviderBlocker("fresh storage activation completion has not been recorded")
}

func validateEnvironmentCreateStorageOrigin(environment, purpose string, target deploymentStorageTarget) error {
	path, err := privateDeploymentStatePath(environment, environmentCreateStorageAllocationPath)
	if err != nil {
		return err
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	var allocation environmentCreateStorageAllocation
	if err != nil || decodeEnvironmentJSON(raw, &allocation) != nil || len(allocation.Buckets) != 2 {
		return createProviderBlocker("original selected creation storage origin cannot be read")
	}
	if purpose != "media" && purpose != "ota" {
		return createProviderBlocker("an explicit supported storage lifecycle purpose is required")
	}
	cfg := deploymentConfig{Environment: environment, Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + environment}}
	for index, item := range allocation.Buckets {
		stored := deploymentStorageTarget{Purpose: strings.TrimPrefix(item.Key, "bucket-"), Bucket: item.Bucket, Region: item.Region, Prefix: item.Prefix}
		if index == 0 {
			cfg.Storage.RuntimeMedia = stored
		} else {
			cfg.Storage.OTAFirmware = stored
			cfg.Storage.OTAMode = "dedicated"
		}
	}
	if err := validateEnvironmentCreateStorageAllocation(cfg); err != nil {
		return err
	}
	index := 0
	if purpose == "ota" {
		index = 1
	}
	item := allocation.Buckets[index]
	if item.Bucket != target.Bucket || item.Region != target.Region || item.Prefix != target.Prefix {
		return createProviderBlocker("original creation allocation differs from the selected storage target; complete its explicit migration/cutover lifecycle")
	}
	store, err := newSecretStore("", environment)
	if err != nil {
		return err
	}
	operator, err := store.readOperator()
	if err != nil || operator["LINODE_TOKEN"] == "" {
		return createProviderBlocker("selected provider credentials cannot verify the original current storage allocation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	resource := environmentCreateProviderResource{Key: item.Key, Kind: "bucket", Path: "/object-storage/buckets", Name: item.Bucket, Region: item.Region, Body: map[string]any{}}
	if purpose == "ota" {
		resource.Body["endpoint_type"] = "E3"
	}
	actual, ready, err := verifyEnvironmentCreateProviderResource(ctx, cfg, resource, environmentCreateProviderReceipt{Key: item.Key, Kind: "bucket", NativeID: item.NativeID, Name: item.Bucket, Region: item.Region, S3Endpoint: item.Endpoint}, operator)
	if err != nil || !ready || actual.S3Endpoint != item.Endpoint {
		return createProviderBlocker("original selected storage allocation cannot be verified from current exact provider metadata")
	}
	return nil
}

func environmentCreateStorageAllocationAbsent(cfg deploymentConfig) error {
	path, err := privateDeploymentStatePath(cfg.Environment, environmentCreateStorageAllocationPath)
	if err != nil {
		return err
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		return createProviderBlocker("original fresh storage allocation evidence exists or cannot be inspected; a new creation cannot overwrite it")
	}
	return nil
}
