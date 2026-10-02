package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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
	return listStorageSourceObjects(source, destinationPrefix, []string{"ota-billable-v1/"})
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

func ensureLiveOTASourceBucket(namespace, sourceFile, destinationPrefix string) error {
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
	return validateLiveOTASourceBucket(body, source.bucket, source.region, source.endpoint, destinationPrefix)
}

func normalizedOTASourceEndpoint(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if strings.HasPrefix(raw, "file://") {
		return raw, nil
	}
	return normalizeLinodeS3Endpoint(raw)
}

func validateLiveOTASourceBucket(body []byte, sourceBucket, sourceRegion, sourceEndpoint, destinationPrefix string) error {
	wantEndpoint, err := normalizedOTASourceEndpoint(sourceEndpoint)
	if err != nil {
		return fmt.Errorf("invalid OTA source endpoint: %w", err)
	}
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
	bucketFound, regionFound, endpointFound, prefixFound := false, false, false, false
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
			case "VIDEO_CLOUD_BLOB_ENDPOINT":
				endpointFound = true
				liveEndpoint, err := normalizedOTASourceEndpoint(variable.Value)
				if err != nil || liveEndpoint != wantEndpoint {
					return fmt.Errorf("OTA source endpoint %s does not match live core API endpoint %s", wantEndpoint, variable.Value)
				}
			case "VIDEO_CLOUD_BLOB_PREFIX":
				prefixFound = true
				livePrefix := strings.Trim(variable.Value, "/")
				if livePrefix != "" && livePrefix != strings.Trim(destinationPrefix, "/") {
					return fmt.Errorf("OTA source prefix %s does not match the inventoried destination prefix %s", livePrefix, destinationPrefix)
				}
			}
		}
	}
	if !bucketFound || !regionFound || !endpointFound || !prefixFound {
		return errors.New("OTA cutover cannot verify the live core API bucket, region, endpoint, and prefix")
	}
	return nil
}

func (c deploymentCredentialChecker) migrateOTAFirmwareObjects(cfg deploymentConfig, source, destination provisionObjectStore, statePath string) error {
	return c.migrateStorageObjects(cfg.Environment, source, destination, cfg.Storage.OTAFirmware.Prefix, statePath, "ota-firmware", []string{"ota-billable-v1/"})
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
	return c.validateStorageMigrationCutover(cfg.Environment, source, destination, target.Prefix, filepath.Join(cfg.RuntimeRoot, "state", "storage-migration-ota.json"), "ota-firmware", []string{"ota-billable-v1/"})
}

func (c deploymentCredentialChecker) validateMediaMigrationCutover(cfg deploymentConfig, values map[string]string, sourceFile string) error {
	if sourceFile == "" {
		return errors.New("--source-env-file is required to verify media migration before cutover")
	}
	sourceValues, check := deploymentCredentialValues(sourceFile)
	if !check.Passed {
		return errors.New(check.Detail)
	}
	source, err := provisionObjectStoreFromEnv(sourceValues)
	if err != nil {
		return err
	}
	target := cfg.Storage.RuntimeMedia
	bucket, err := c.resolveStorageBucket(values["LINODE_TOKEN"], target)
	if err != nil {
		return err
	}
	endpoint, err := normalizeLinodeS3Endpoint(bucket.S3Endpoint)
	if err != nil {
		return err
	}
	destination := provisionObjectStore{bucket: target.Bucket, endpoint: endpoint, region: target.Region,
		accessKey: firstNonEmpty(values["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"], values["LINODE_OBJ_ACCESS_KEY_ID"]),
		secretKey: firstNonEmpty(values["LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY"], values["LINODE_OBJ_SECRET_ACCESS_KEY"])}
	return c.validateStorageMigrationCutover(cfg.Environment, source, destination, target.Prefix, filepath.Join(cfg.RuntimeRoot, "state", "storage-migration.json"), "media", []string{"clips/", "brands/", "snapshots/", "clip-index/", "ota/", "firmware/"})
}

func (c deploymentCredentialChecker) validateStorageMigrationCutover(environment string, source, destination provisionObjectStore, prefix, statePath, purpose string, namespaces []string) error {
	body, err := os.ReadFile(statePath)
	if err != nil {
		return fmt.Errorf("cutover requires a completed migration receipt, including verified empty inventory: %w", err)
	}
	var receipt deploymentStorageMigrationState
	if err := json.Unmarshal(body, &receipt); err != nil {
		return fmt.Errorf("decode migration receipt: %w", err)
	}
	if receipt.Environment != environment || receipt.Purpose != purpose || receipt.Source != source.bucket || receipt.SourceRegion != source.region || receipt.SourceEndpoint != source.endpoint ||
		receipt.SourcePrefix != source.prefix || receipt.SourcePrefixExplicit != source.prefixSet || receipt.Destination != destination.bucket || receipt.DestinationRegion != destination.region || receipt.DestinationEndpoint != destination.endpoint ||
		receipt.Prefix != strings.Trim(prefix, "/") || receipt.Objects == nil || receipt.SourceKeys == nil || receipt.UpdatedAt == "" {
		return errors.New("migration receipt does not match current source and destination; rerun storage-migrate")
	}
	for _, store := range []provisionObjectStore{source, destination} {
		if err := validateStorageCopyBucket(c.client, store); err != nil {
			return err
		}
	}
	objects, err := listStorageSourceObjects(source, prefix, namespaces)
	if err != nil {
		return err
	}
	verified := map[string]storageObjectProof{}
	for _, object := range objects {
		if receipt.SourceKeys[object.SourceKey] != object.DestinationKey {
			return fmt.Errorf("migration receipt does not match source mapping %s", object.SourceKey)
		}
		snapshot, err := readStorageObject(c.client, source, object.SourceKey)
		if err != nil {
			return fmt.Errorf("read source object %s: %w", object.SourceKey, err)
		}
		proof := snapshot.proof()
		verified[object.DestinationKey] = proof
		if receipt.Objects[object.DestinationKey] != proof {
			return fmt.Errorf("migration receipt does not match source object %s", object.SourceKey)
		}
		written, err := readStorageObject(c.client, destination, object.DestinationKey)
		if err != nil {
			return fmt.Errorf("read destination object %s: %w", object.DestinationKey, err)
		}
		if err := verifyStorageObject(snapshot, written, object.DestinationKey); err != nil {
			return err
		}
	}
	count, bytes := otaProofTotals(verified)
	if len(receipt.SourceKeys) != len(objects) || len(receipt.Objects) != count || receipt.ObjectCount != count || receipt.ByteCount != bytes {
		return errors.New("migration receipt inventory or totals differ from current source bucket")
	}
	var destinationCount int
	for _, namespace := range namespaces {
		entries, err := provisionListObjects(destination, storageJoinPrefix(prefix, namespace))
		if err != nil {
			return fmt.Errorf("inventory destination bucket: %w", err)
		}
		for _, entry := range entries {
			if _, found := verified[entry.Key]; !found {
				return fmt.Errorf("destination bucket contains unverified object %s", entry.Key)
			}
			destinationCount++
		}
	}
	if destinationCount != count {
		return errors.New("destination inventory differs from verified source inventory")
	}
	return nil
}
