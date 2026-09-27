package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type otaSourceObject struct {
	SourceKey      string
	DestinationKey string
}

// The old bucket may contain objects written before or after its environment
// prefix was introduced. Both spellings map to the same dedicated-bucket key.
func listOTASourceObjects(source provisionObjectStore, destinationPrefix string) ([]otaSourceObject, error) {
	if strings.HasPrefix(source.endpoint, "file://") {
		root, err := provisionFileObjectRoot(source)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(filepath.Join(root, source.bucket))
		if err != nil {
			return nil, fmt.Errorf("inspect OTA source bucket: %w", err)
		}
		if !info.IsDir() {
			return nil, errors.New("OTA source bucket is not a directory")
		}
	}
	keys := map[string]string{}
	for _, prefix := range []string{"ota-billable-v1/", strings.Trim(destinationPrefix, "/") + "/ota-billable-v1/"} {
		entries, err := provisionListObjects(source, prefix)
		if err != nil {
			return nil, fmt.Errorf("inventory OTA source bucket: %w", err)
		}
		for _, entry := range entries {
			destinationKey, allowed := storageMigrationDestinationKeyForPurpose(destinationPrefix, entry.Key, []string{"ota-billable-v1/"})
			if allowed {
				keys[entry.Key] = destinationKey
			}
		}
	}
	sourceKeys := make([]string, 0, len(keys))
	for key := range keys {
		sourceKeys = append(sourceKeys, key)
	}
	sort.Strings(sourceKeys)
	objects := make([]otaSourceObject, 0, len(sourceKeys))
	for _, key := range sourceKeys {
		objects = append(objects, otaSourceObject{SourceKey: key, DestinationKey: keys[key]})
	}
	return objects, nil
}

func otaObjectProof(data []byte) storageObjectProof {
	sum := sha256.Sum256(data)
	return storageObjectProof{SHA256: hex.EncodeToString(sum[:]), Bytes: int64(len(data))}
}

func otaProofTotals(objects map[string]storageObjectProof) (int, int64) {
	var bytes int64
	for _, proof := range objects {
		bytes += proof.Bytes
	}
	return len(objects), bytes
}

func ensureLiveOTASourceBucket(namespace, sourceFile string) error {
	sourceValues, check := deploymentCredentialValues(sourceFile)
	if !check.Passed {
		return errors.New(check.Detail)
	}
	source, err := provisionObjectStoreFromEnv(sourceValues)
	if err != nil {
		return fmt.Errorf("OTA source storage: %w", err)
	}
	body, err := kubectlCombinedOutput(nil, "-n", namespace, "get", "deployment", "video-cloud-api", "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return fmt.Errorf("inspect live core OTA source bucket: %w", err)
	}
	return validateLiveOTASourceBucket(body, source.bucket, source.region)
}

func validateLiveOTASourceBucket(body []byte, sourceBucket, sourceRegion string) error {
	var deployment struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Env []struct {
							Name  string `json:"name"`
							Value string `json:"value"`
						} `json:"env"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(body, &deployment); err != nil || deployment.Metadata.Name != "video-cloud-api" {
		return errors.New("OTA cutover cannot verify the live core API source bucket")
	}
	bucketFound, regionFound := false, false
	for _, container := range deployment.Spec.Template.Spec.Containers {
		for _, variable := range container.Env {
			switch variable.Name {
			case "VIDEO_CLOUD_BLOB_BUCKET":
				bucketFound = true
				if variable.Value == "" || variable.Value != sourceBucket {
					return fmt.Errorf("OTA source bucket %s does not match live core API bucket %s", sourceBucket, variable.Value)
				}
			case "VIDEO_CLOUD_BLOB_REGION":
				regionFound = true
				if variable.Value == "" || variable.Value != sourceRegion {
					return fmt.Errorf("OTA source region %s does not match live core API region %s", sourceRegion, variable.Value)
				}
			}
		}
	}
	if !bucketFound || !regionFound {
		return errors.New("OTA cutover cannot verify the live core API bucket and region")
	}
	return nil
}

func (c deploymentCredentialChecker) migrateOTAFirmwareObjects(cfg deploymentConfig, source, destination provisionObjectStore, statePath string) error {
	state := deploymentStorageMigrationState{
		Environment: cfg.Environment, Source: source.bucket, SourceRegion: source.region, SourceEndpoint: source.endpoint,
		Destination: destination.bucket, DestinationRegion: destination.region, DestinationEndpoint: destination.endpoint,
		Prefix: cfg.Storage.OTAFirmware.Prefix, Objects: map[string]storageObjectProof{},
	}
	if body, err := os.ReadFile(statePath); err == nil {
		if err := json.Unmarshal(body, &state); err != nil {
			return fmt.Errorf("decode existing OTA migration receipt: %w", err)
		}
		if state.Environment != cfg.Environment || state.Source != source.bucket || state.Destination != destination.bucket || state.Prefix != cfg.Storage.OTAFirmware.Prefix ||
			(state.SourceRegion != "" && state.SourceRegion != source.region) || (state.SourceEndpoint != "" && state.SourceEndpoint != source.endpoint) ||
			(state.DestinationRegion != "" && state.DestinationRegion != destination.region) || (state.DestinationEndpoint != "" && state.DestinationEndpoint != destination.endpoint) {
			return errors.New("existing OTA migration receipt targets different storage")
		}
		if state.Objects == nil {
			return errors.New("existing OTA migration receipt has no object inventory")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read existing OTA migration receipt: %w", err)
	}
	objects, err := listOTASourceObjects(source, cfg.Storage.OTAFirmware.Prefix)
	if err != nil {
		return err
	}
	verified := map[string]storageObjectProof{}
	for _, object := range objects {
		data, err := provisionReadObject(source, object.SourceKey)
		if err != nil {
			return fmt.Errorf("read OTA source object %s: %w", object.SourceKey, err)
		}
		proof := otaObjectProof(data)
		if previous, found := verified[object.DestinationKey]; found {
			if previous != proof {
				return fmt.Errorf("OTA source objects map to %s with different content", object.DestinationKey)
			}
			continue
		}
		verified[object.DestinationKey] = proof
		if recorded, found := state.Objects[object.DestinationKey]; found && recorded != proof {
			return fmt.Errorf("OTA source object %s differs from migration receipt", object.SourceKey)
		}
		if _, found := state.Objects[object.DestinationKey]; !found {
			entries, err := provisionListObjects(destination, object.DestinationKey)
			if err != nil {
				return err
			}
			exists := false
			for _, entry := range entries {
				if entry.Key == object.DestinationKey {
					exists = true
					break
				}
			}
			if !exists {
				if _, err := provisionSignedObjectRequestWithClient(c.client, destination, http.MethodPut, object.DestinationKey, nil, data); err != nil {
					return fmt.Errorf("copy OTA object %s: %w", object.SourceKey, err)
				}
			}
		}
		written, err := provisionReadObject(destination, object.DestinationKey)
		if err != nil {
			return fmt.Errorf("read OTA destination object %s: %w", object.DestinationKey, err)
		}
		if otaObjectProof(written) != proof {
			return fmt.Errorf("checksum mismatch for %s", object.SourceKey)
		}
		state.Objects[object.DestinationKey] = proof
		state.ObjectCount, state.ByteCount = otaProofTotals(state.Objects)
		state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := writeStorageState(statePath, state); err != nil {
			return err
		}
	}
	if len(state.Objects) != len(verified) {
		return errors.New("OTA migration receipt contains objects absent from the current source bucket")
	}
	// A completed receipt is required even when the source namespace is empty.
	state.SourceRegion, state.SourceEndpoint = source.region, source.endpoint
	state.DestinationRegion, state.DestinationEndpoint = destination.region, destination.endpoint
	state.ObjectCount, state.ByteCount = otaProofTotals(verified)
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return writeStorageState(statePath, state)
}

func (c deploymentCredentialChecker) validateOTAMigrationCutover(cfg deploymentConfig, values map[string]string, sourceFile string) error {
	sourceValues, check := deploymentCredentialValues(sourceFile)
	if !check.Passed {
		return errors.New(check.Detail)
	}
	source, err := provisionObjectStoreFromEnv(sourceValues)
	if err != nil {
		return fmt.Errorf("OTA source storage: %w", err)
	}
	target := cfg.Storage.OTAFirmware
	bucket, err := c.resolveStorageBucket(values["LINODE_TOKEN"], target)
	if err != nil {
		return err
	}
	endpoint, err := normalizeLinodeS3Endpoint(bucket.S3Endpoint)
	if err != nil {
		return err
	}
	destination := provisionObjectStore{bucket: target.Bucket, endpoint: endpoint, region: target.Region, accessKey: values["LINODE_OTA_OBJ_ACCESS_KEY_ID"], secretKey: values["LINODE_OTA_OBJ_SECRET_ACCESS_KEY"]}
	body, err := os.ReadFile(filepath.Join(cfg.RuntimeRoot, "state", "storage-migration-ota.json"))
	if err != nil {
		return fmt.Errorf("OTA cutover requires a completed migration receipt, including verified empty inventory: %w", err)
	}
	var receipt deploymentStorageMigrationState
	if err := json.Unmarshal(body, &receipt); err != nil {
		return fmt.Errorf("decode OTA migration receipt: %w", err)
	}
	if receipt.Environment != cfg.Environment || receipt.Source != source.bucket || receipt.SourceRegion != source.region || receipt.SourceEndpoint != source.endpoint ||
		receipt.Destination != destination.bucket || receipt.DestinationRegion != destination.region || receipt.DestinationEndpoint != destination.endpoint ||
		receipt.Prefix != target.Prefix || receipt.Objects == nil || receipt.UpdatedAt == "" {
		return errors.New("OTA migration receipt does not match current source and destination; rerun storage-migrate")
	}
	objects, err := listOTASourceObjects(source, target.Prefix)
	if err != nil {
		return err
	}
	verified := map[string]storageObjectProof{}
	for _, object := range objects {
		data, err := provisionReadObject(source, object.SourceKey)
		if err != nil {
			return fmt.Errorf("read OTA source object %s: %w", object.SourceKey, err)
		}
		proof := otaObjectProof(data)
		if previous, found := verified[object.DestinationKey]; found {
			if previous != proof {
				return fmt.Errorf("OTA source objects map to %s with different content", object.DestinationKey)
			}
			continue
		}
		verified[object.DestinationKey] = proof
		if receipt.Objects[object.DestinationKey] != proof {
			return fmt.Errorf("OTA migration receipt does not match source object %s", object.SourceKey)
		}
		data, err = provisionReadObject(destination, object.DestinationKey)
		if err != nil {
			return fmt.Errorf("read OTA destination object %s: %w", object.DestinationKey, err)
		}
		if otaObjectProof(data) != proof {
			return fmt.Errorf("OTA destination checksum mismatch for %s", object.DestinationKey)
		}
	}
	count, bytes := otaProofTotals(verified)
	if len(receipt.Objects) != count || receipt.ObjectCount != count || receipt.ByteCount != bytes {
		return errors.New("OTA migration receipt inventory or totals differ from current source bucket")
	}
	destinationEntries, err := provisionListObjects(destination, strings.Trim(target.Prefix, "/")+"/ota-billable-v1/")
	if err != nil {
		return fmt.Errorf("inventory OTA destination bucket: %w", err)
	}
	for _, entry := range destinationEntries {
		if _, found := verified[entry.Key]; !found {
			return fmt.Errorf("OTA destination bucket contains unverified object %s", entry.Key)
		}
	}
	if len(destinationEntries) != count {
		return errors.New("OTA destination bucket inventory differs from the verified source inventory")
	}
	return nil
}
