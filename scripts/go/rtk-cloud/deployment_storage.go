package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type deploymentStorageMigrationState struct {
	Environment          string                        `json:"environment"`
	Source               string                        `json:"source_bucket"`
	SourceRegion         string                        `json:"source_region,omitempty"`
	SourceEndpoint       string                        `json:"source_endpoint,omitempty"`
	SourcePrefix         string                        `json:"source_prefix,omitempty"`
	SourcePrefixExplicit bool                          `json:"source_prefix_explicit,omitempty"`
	Purpose              string                        `json:"purpose,omitempty"`
	SourceKeys           map[string]string             `json:"source_keys"`
	Destination          string                        `json:"destination_bucket"`
	DestinationRegion    string                        `json:"destination_region,omitempty"`
	DestinationEndpoint  string                        `json:"destination_endpoint,omitempty"`
	Prefix               string                        `json:"destination_prefix"`
	Objects              map[string]storageObjectProof `json:"objects"`
	ObjectCount          int                           `json:"object_count"`
	ByteCount            int64                         `json:"byte_count"`
	UpdatedAt            string                        `json:"updated_at"`
}

type storageObjectProof struct {
	SHA256           string `json:"sha256"`
	Bytes            int64  `json:"bytes"`
	AttributesSHA256 string `json:"attributes_sha256,omitempty"`
}

var errStorageEndpointUnassigned = errors.New("Object Storage endpoint is not assigned to this account")

func validateDeploymentStorageActivation(cfg deploymentConfig) error {
	if !cfg.Storage.RuntimeMediaCutoverRequired {
		return nil
	}
	body, err := os.ReadFile(filepath.Join(cfg.RuntimeRoot, "state", "storage-cutover.json"))
	if err != nil {
		return fmt.Errorf("runtime media bucket %s is prepared but not cut over; storage-cutover receipt is required before deployment", cfg.Storage.RuntimeMedia.Bucket)
	}
	var receipt struct {
		Environment                 string    `json:"environment"`
		Bucket                      string    `json:"bucket"`
		Region                      string    `json:"region"`
		Prefix                      string    `json:"prefix"`
		CutoverID                   string    `json:"cutover_id"`
		MigrationSHA256             string    `json:"migration_receipt_sha256"`
		CutoverAt                   time.Time `json:"cutover_at"`
		RollbackCredentialsRetained bool      `json:"rollback_credentials_retained"`
	}
	if json.Unmarshal(body, &receipt) != nil || receipt.Environment != cfg.Environment || receipt.Bucket != cfg.Storage.RuntimeMedia.Bucket || receipt.Region != cfg.Storage.RuntimeMedia.Region || receipt.Prefix != cfg.Storage.RuntimeMedia.Prefix || receipt.CutoverAt.IsZero() || receipt.CutoverAt.After(time.Now().UTC()) || receipt.CutoverID == "" || receipt.MigrationSHA256 == "" || !receipt.RollbackCredentialsRetained {
		return fmt.Errorf("runtime media bucket %s has no matching completed cutover receipt", cfg.Storage.RuntimeMedia.Bucket)
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	raw, err := store.read(storageCutoverJournalName("media"))
	if err != nil {
		return errors.New("completed private storage cutover journal is required before deployment")
	}
	var journal storageCutoverJournal
	if json.Unmarshal([]byte(raw), &journal) != nil || journal.Environment != cfg.Environment || journal.Purpose != "media" || journal.Status != "complete" || journal.ID != receipt.CutoverID || journal.MigrationSHA256 != receipt.MigrationSHA256 {
		return errors.New("storage cutover receipt does not match its completed private journal")
	}
	proof, err := os.ReadFile(storageCutoverMigrationPath(cfg, "media"))
	if err != nil || fmt.Sprintf("%x", sha256.Sum256(proof)) != receipt.MigrationSHA256 {
		return errors.New("storage migration proof changed after cutover; reconcile it before deployment")
	}
	return nil
}

func runDeploymentStorageLifecycle(action string, cfg deploymentConfig, environmentFile, sourceFile string, keyID int) error {
	return runDeploymentStorageLifecyclePurpose(action, cfg, environmentFile, sourceFile, keyID, "media")
}

func runDeploymentStorageLifecyclePurpose(action string, cfg deploymentConfig, environmentFile, sourceFile string, keyID int, purpose string) error {
	if purpose != "media" && purpose != "ota" && purpose != "artifacts" {
		return errors.New("--purpose must be media, ota, or artifacts")
	}
	if purpose == "ota" && cfg.Storage.OTAMode != "dedicated" {
		return errors.New("OTA storage is not configured as dedicated")
	}
	checker := defaultDeploymentCredentialChecker()
	values, check := deploymentCredentialProfileValues(cfg.Environment, environmentFile, defaultDeploymentSharedCredentialFile())
	if !check.Passed {
		return errors.New(check.Detail)
	}
	if strings.Contains(check.Detail, "WARNING:") {
		fmt.Fprintln(os.Stderr, check.Detail)
	}
	switch action {
	case "storage-plan":
		plan := cfg.Storage
		token := strings.TrimSpace(values["LINODE_TOKEN"])
		if token == "" {
			return errors.New("LINODE_TOKEN is required to discover Object Storage endpoints")
		}
		endpointStatus := map[string]string{}
		mediaEndpoint, err := checker.resolveStorageEndpoint(token, plan.RuntimeMedia.Region)
		if err != nil && !errors.Is(err, errStorageEndpointUnassigned) {
			return err
		}
		if errors.Is(err, errStorageEndpointUnassigned) {
			endpointStatus["runtime_media"] = "unassigned; storage-bootstrap will create the bucket and use its API-reported endpoint"
		} else {
			endpointStatus["runtime_media"] = "assigned"
		}
		artifactEndpoint, err := checker.resolveStorageEndpoint(token, plan.ReleaseArtifacts.Region)
		if err != nil && !errors.Is(err, errStorageEndpointUnassigned) {
			return err
		}
		if errors.Is(err, errStorageEndpointUnassigned) {
			endpointStatus["release_artifacts"] = "unassigned"
		} else {
			endpointStatus["release_artifacts"] = "assigned"
		}
		plan.RuntimeMedia.Endpoint = mediaEndpoint
		if plan.OTAMode == "dedicated" {
			bucket, bucketErr := checker.resolveStorageBucket(token, plan.OTAFirmware)
			switch {
			case bucketErr == nil:
				plan.OTAFirmware.Endpoint, err = normalizeLinodeS3Endpoint(bucket.S3Endpoint)
				if err != nil {
					return err
				}
				endpointStatus["ota_firmware"] = "existing " + bucket.EndpointType + " bucket; eligible for direct-delivery metric export"
				if metricErr := validateOTADirectMetricsEndpoint(bucket); metricErr != nil {
					endpointStatus["ota_firmware"] = metricErr.Error()
				}
			case strings.Contains(bucketErr.Error(), "was not found"):
				endpointType, metricErr := checker.resolveOTAMetricsEndpointType(token, plan.OTAFirmware.Region)
				if metricErr != nil {
					endpointStatus["ota_firmware"] = metricErr.Error()
				} else {
					endpointStatus["ota_firmware"] = "assigned " + endpointType + "; storage-bootstrap --purpose ota will create the bucket"
				}
			default:
				return bucketErr
			}
		}
		plan.ReleaseArtifacts.Endpoint = artifactEndpoint
		body, _ := json.MarshalIndent(map[string]any{"environment": cfg.Environment, "compute_region": cfg.AdapterResolved["LKE_REGION"], "storage": plan, "endpoint_status": endpointStatus, "credential_source": "environment SecretStore"}, "", "  ")
		fmt.Println(string(body))
		return nil
	case "storage-bootstrap":
		if purpose == "artifacts" {
			return checker.bootstrapArtifactStorage(cfg, values, environmentFile)
		}
		if purpose == "ota" {
			return checker.bootstrapOTAStorage(cfg, values, environmentFile)
		}
		return checker.bootstrapRuntimeStorage(cfg, values, environmentFile)
	case "storage-migrate":
		if sourceFile == "" {
			return errors.New("--source-env-file is required for storage-migrate")
		}
		return checker.migrateStoragePurpose(cfg, values, sourceFile, purpose)
	case "storage-cutover":
		if purpose == "artifacts" {
			return errors.New("artifact cutover requires updating and verifying every release, CI, SDK, backup, and download consumer; runtime cutover cannot activate shared artifacts")
		}
		if purpose == "ota" {
			if sourceFile == "" {
				return errors.New("--source-env-file is required for OTA storage-cutover")
			}
			return checker.cutoverOTAStorage(cfg, values, sourceFile, environmentFile)
		}
		if check := checker.checkResolvedObjectStorage(cfg, values); !check.Passed {
			return errors.New(check.Detail)
		}
		return checker.cutoverRuntimeStorage(cfg, values, sourceFile, environmentFile)
	case "storage-rollback":
		if purpose == "artifacts" {
			return errors.New("artifact rollback requires all external consumer bindings and destination writes to be reconciled")
		}
		if err := rollbackStorageCutover(cfg, purpose); err != nil {
			return err
		}
		return rollbackStorageCandidate(cfg, environmentFile, purpose)
	case "storage-retire":
		if purpose != "media" {
			return errors.New("OTA and artifact key retirement require separate consumer verification; use Linode key management after verification")
		}
		if keyID <= 0 {
			return errors.New("--key-id is required for storage-retire")
		}
		return checker.retireStorageKey(cfg, values, keyID)
	default:
		return fmt.Errorf("unsupported storage action %s", action)
	}
}

func (c deploymentCredentialChecker) validateClipStorageSmoke(store provisionObjectStore, prefix string) error {
	body := []byte("rtk-cloud-clip-storage-smoke")
	key := strings.Trim(prefix, "/") + "/__rtk_cloud_validation__/cutover-" + fmt.Sprintf("%d", time.Now().UTC().UnixNano())
	if _, err := provisionSignedObjectRequestWithClient(c.client, store, http.MethodPut, key, nil, body); err != nil {
		return fmt.Errorf("clip upload smoke failed: %w", err)
	}
	cleaned := false
	defer func() {
		if !cleaned {
			_, _ = provisionSignedObjectRequestWithClient(c.client, store, http.MethodDelete, key, nil, nil)
		}
	}()
	read, err := provisionSignedObjectRequestWithClient(c.client, store, http.MethodGet, key, nil, nil)
	if err != nil {
		return fmt.Errorf("clip read smoke failed: %w", err)
	}
	if !bytes.Equal(read, body) {
		return errors.New("clip read smoke content mismatch")
	}
	if _, err := provisionSignedObjectRequestWithClient(c.client, store, http.MethodDelete, key, nil, nil); err != nil {
		return fmt.Errorf("clip delete smoke failed: %w", err)
	}
	cleaned = true
	return nil
}

func (c deploymentCredentialChecker) bootstrapRuntimeStorage(cfg deploymentConfig, values map[string]string, environmentFile string) error {
	mediaReady := c.checkResolvedObjectStorage(cfg, values).Passed
	token := strings.TrimSpace(values["LINODE_TOKEN"])
	if token == "" {
		return errors.New("LINODE_TOKEN is required")
	}
	if !mediaReady {
		target := cfg.Storage.RuntimeMedia
		if err := c.validateStorageRegionCapabilities(token, target.Region); err != nil {
			return err
		}
		bucket, err := c.resolveStorageBucket(token, target)
		if err != nil && strings.Contains(err.Error(), "was not found") {
			payload, _ := json.Marshal(map[string]string{"label": target.Bucket, "region": target.Region})
			body, createErr := c.linodeAuthorizedRequest(token, http.MethodPost, "/object-storage/buckets", payload)
			if createErr != nil {
				return fmt.Errorf("create destination bucket: %w", createErr)
			}
			if json.Unmarshal(body, &bucket) != nil {
				return errors.New("bucket create response returned invalid JSON")
			}
		} else if err != nil {
			return err
		}
		endpoint, err := normalizeLinodeS3Endpoint(bucket.S3Endpoint)
		if err != nil {
			endpoint, err = c.resolveStorageEndpoint(token, target.Region)
		}
		if err != nil {
			return err
		}
		access, secret, err := c.createLimitedObjectStorageKey(cfg, values, provisionObjectStore{bucket: target.Bucket, region: target.Region, endpoint: endpoint})
		if err != nil {
			return err
		}
		store := provisionObjectStore{bucket: target.Bucket, region: target.Region, endpoint: endpoint, accessKey: access, secretKey: secret}
		if err := c.validateNewStorageKey(store, target.Prefix); err != nil {
			return fmt.Errorf("new media key validation failed: %w", err)
		}
		if err := ensureCredentialProfile(environmentFile); err != nil {
			return err
		}
		if err := updateDeploymentCredentialEnvFile(environmentFile, map[string]string{"LINODE_MEDIA_OBJ_ACCESS_KEY_ID": access, "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": secret}); err != nil {
			return err
		}
		values["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"] = access
		values["LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY"] = secret
		if check := c.checkResolvedObjectStorage(cfg, values); !check.Passed {
			return errors.New(check.Detail)
		}
	}
	return nil
}

func (c deploymentCredentialChecker) bootstrapOTAStorage(cfg deploymentConfig, values map[string]string, environmentFile string) error {
	token := strings.TrimSpace(values["LINODE_TOKEN"])
	if token == "" {
		return errors.New("LINODE_TOKEN is required")
	}
	if existing, err := c.resolveStorageBucket(token, cfg.Storage.OTAFirmware); err == nil {
		if err := validateOTADirectMetricsEndpoint(existing); err != nil {
			return err
		}
	} else if !strings.Contains(err.Error(), "was not found") {
		return err
	}
	validation := c.checkResolvedOTAStorage(cfg, values)
	if validation.Passed {
		return nil
	}
	target := cfg.Storage.OTAFirmware
	if access := strings.TrimSpace(values["LINODE_OTA_OBJ_ACCESS_KEY_ID"]); access != "" {
		_, keyErr := c.resolveAuthorizedStorageKey(token, access, target)
		switch {
		case keyErr == nil:
			return fmt.Errorf("configured OTA key already belongs to the E3 bucket; repair storage access before retrying bootstrap: %s", validation.Detail)
		case errors.Is(keyErr, errStorageKeyWrongTarget), errors.Is(keyErr, errStorageKeyMissing):
			// The configured key belongs to the old bucket or was retired.
		default:
			return fmt.Errorf("verify configured OTA key before issuing another: %w", keyErr)
		}
	}
	if err := c.validateStorageRegionCapabilities(token, target.Region); err != nil {
		return err
	}
	bucket, err := c.resolveStorageBucket(token, target)
	if err != nil && strings.Contains(err.Error(), "was not found") {
		creation := map[string]string{"label": target.Bucket, "region": target.Region}
		endpointType, typeErr := c.resolveOTAMetricsEndpointType(token, target.Region)
		if typeErr != nil {
			return typeErr
		}
		creation["endpoint_type"] = endpointType
		payload, _ := json.Marshal(creation)
		body, createErr := c.linodeAuthorizedRequest(token, http.MethodPost, "/object-storage/buckets", payload)
		if createErr != nil {
			return fmt.Errorf("create OTA bucket: %w", createErr)
		}
		if json.Unmarshal(body, &bucket) != nil {
			return errors.New("OTA bucket create response returned invalid JSON")
		}
	} else if err != nil {
		return err
	}
	if err := validateOTADirectMetricsEndpoint(bucket); err != nil {
		return err
	}
	endpoint, err := normalizeLinodeS3Endpoint(bucket.S3Endpoint)
	if err != nil {
		endpoint, err = c.resolveStorageEndpoint(token, target.Region)
	}
	if err != nil {
		return err
	}
	access, secret, err := c.createLimitedObjectStorageKey(cfg, values, provisionObjectStore{bucket: target.Bucket, region: target.Region, endpoint: endpoint})
	if err != nil {
		return err
	}
	store := provisionObjectStore{bucket: target.Bucket, region: target.Region, endpoint: endpoint, accessKey: access, secretKey: secret}
	if err := c.validateNewStorageKey(store, target.Prefix); err != nil {
		return fmt.Errorf("new OTA key validation failed: %w", err)
	}
	if err := ensureCredentialProfile(environmentFile); err != nil {
		return err
	}
	if err := updateDeploymentCredentialEnvFile(environmentFile, map[string]string{"LINODE_OTA_OBJ_ACCESS_KEY_ID": access, "LINODE_OTA_OBJ_SECRET_ACCESS_KEY": secret}); err != nil {
		return err
	}
	values["LINODE_OTA_OBJ_ACCESS_KEY_ID"], values["LINODE_OTA_OBJ_SECRET_ACCESS_KEY"] = access, secret
	if check := c.checkResolvedOTAStorage(cfg, values); !check.Passed {
		return errors.New(check.Detail)
	}
	return nil
}

func (c deploymentCredentialChecker) checkResolvedOTAStorage(cfg deploymentConfig, values map[string]string) deploymentCredentialCheck {
	target := cfg.Storage.OTAFirmware
	name := "Linode OTA firmware storage"
	if cfg.Storage.OTAMode != "dedicated" {
		return deploymentCredentialCheck{Name: name, Detail: "dedicated OTA storage is not configured"}
	}
	token := strings.TrimSpace(values["LINODE_TOKEN"])
	if token == "" {
		return deploymentCredentialCheck{Name: name, Detail: "LINODE_TOKEN is required"}
	}
	bucket, err := c.resolveStorageBucket(token, target)
	if err != nil {
		return deploymentCredentialCheck{Name: name, Detail: err.Error()}
	}
	if err := validateOTADirectMetricsEndpoint(bucket); err != nil {
		return deploymentCredentialCheck{Name: name, Detail: err.Error()}
	}
	endpoint, err := normalizeLinodeS3Endpoint(bucket.S3Endpoint)
	if err != nil {
		return deploymentCredentialCheck{Name: name, Detail: err.Error()}
	}
	access, secret := strings.TrimSpace(values["LINODE_OTA_OBJ_ACCESS_KEY_ID"]), strings.TrimSpace(values["LINODE_OTA_OBJ_SECRET_ACCESS_KEY"])
	if access == "" || secret == "" {
		return deploymentCredentialCheck{Name: name, Detail: "LINODE_OTA_OBJ_ACCESS_KEY_ID and LINODE_OTA_OBJ_SECRET_ACCESS_KEY are required"}
	}
	key, err := c.resolveAuthorizedStorageKey(token, access, target)
	if err != nil {
		return deploymentCredentialCheck{Name: name, Detail: err.Error()}
	}
	store := provisionObjectStore{bucket: target.Bucket, endpoint: endpoint, accessKey: access, secretKey: secret, region: target.Region}
	if c.readOnly {
		return c.checkStorageReadOnly(store, target.Prefix, name)
	}
	if err := c.validateStorageReadWriteCanary(store, target.Prefix); err != nil {
		return deploymentCredentialCheck{Name: name, Detail: err.Error()}
	}
	receipt := deploymentStorageReceipt{Environment: cfg.Environment, Purpose: target.Purpose, Bucket: target.Bucket, Region: target.Region, Endpoint: endpoint, EndpointType: bucket.EndpointType, KeyID: key.ID, AccessSuffix: redactAccessKey(access), ValidatedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := writeStorageState(filepath.Join(cfg.RuntimeRoot, "state", "storage-preflight-ota.json"), receipt); err != nil {
		return deploymentCredentialCheck{Name: name, Detail: err.Error()}
	}
	return deploymentCredentialCheck{Name: name, Passed: true, Detail: "inventory, limited key, and write/read/delete canary verified"}
}

func (c deploymentCredentialChecker) bootstrapArtifactStorage(cfg deploymentConfig, values map[string]string, environmentFile string) error {
	if c.checkResolvedArtifactStorage(cfg, values).Passed {
		return nil
	}
	target := cfg.Storage.ReleaseArtifacts
	bucket, err := c.resolveStorageBucket(values["LINODE_TOKEN"], target)
	if err != nil && strings.Contains(err.Error(), "was not found") {
		if err := c.validateStorageRegionCapabilities(values["LINODE_TOKEN"], target.Region); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{"label": target.Bucket, "region": target.Region})
		body, createErr := c.linodeAuthorizedRequest(values["LINODE_TOKEN"], http.MethodPost, "/object-storage/buckets", payload)
		if createErr != nil {
			return fmt.Errorf("create artifact destination bucket: %w", createErr)
		}
		if json.Unmarshal(body, &bucket) != nil {
			return errors.New("artifact bucket create response returned invalid JSON")
		}
	} else if err != nil {
		return err
	}
	endpoint, err := normalizeLinodeS3Endpoint(bucket.S3Endpoint)
	if err != nil {
		return err
	}
	access, secret, err := c.createLimitedObjectStorageKey(cfg, values, provisionObjectStore{bucket: target.Bucket, region: target.Region, endpoint: endpoint})
	if err != nil {
		return err
	}
	store := provisionObjectStore{bucket: target.Bucket, region: target.Region, endpoint: endpoint, accessKey: access, secretKey: secret}
	if err := c.validateNewStorageKey(store, target.Prefix); err != nil {
		return fmt.Errorf("new artifact key validation failed: %w", err)
	}
	if err := ensureCredentialProfile(environmentFile); err != nil {
		return err
	}
	replacements := map[string]string{
		"LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID":     access,
		"LINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY": secret,
	}
	return updateDeploymentCredentialEnvFile(environmentFile, replacements)
}

func ensureCredentialProfile(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("environment credential profile path is empty")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return os.WriteFile(path, nil, 0o600)
	} else {
		return err
	}
}

func (c deploymentCredentialChecker) migrateRuntimeStorage(cfg deploymentConfig, destinationValues map[string]string, sourceFile string) error {
	return c.migrateStoragePurpose(cfg, destinationValues, sourceFile, "media")
}

func (c deploymentCredentialChecker) migrateStoragePurpose(cfg deploymentConfig, destinationValues map[string]string, sourceFile, purpose string) error {
	target := cfg.Storage.RuntimeMedia
	var check deploymentCredentialCheck
	access := firstNonEmpty(destinationValues["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"], destinationValues["LINODE_OBJ_ACCESS_KEY_ID"])
	secret := firstNonEmpty(destinationValues["LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY"], destinationValues["LINODE_OBJ_SECRET_ACCESS_KEY"])
	stateName := "storage-migration.json"
	namespaces := []string{"clips/", "brands/", "snapshots/", "clip-index/", "ota/", "firmware/"}
	if purpose == "ota" {
		target = cfg.Storage.OTAFirmware
		check = c.checkResolvedOTAStorage(cfg, destinationValues)
		access, secret = destinationValues["LINODE_OTA_OBJ_ACCESS_KEY_ID"], destinationValues["LINODE_OTA_OBJ_SECRET_ACCESS_KEY"]
		stateName = "storage-migration-ota.json"
		namespaces = []string{"ota-billable-v1/"}
	} else if purpose == "artifacts" {
		target = cfg.Storage.ReleaseArtifacts
		check = c.checkResolvedArtifactStorage(cfg, destinationValues)
		access, secret = destinationValues["LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID"], destinationValues["LINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY"]
		stateName = "storage-migration-artifacts.json"
		namespaces = []string{""}
	} else {
		check = c.checkResolvedObjectStorage(cfg, destinationValues)
	}
	if !check.Passed {
		return errors.New(check.Detail)
	}
	sourceValues, check := deploymentCredentialValues(sourceFile)
	if !check.Passed {
		return errors.New(check.Detail)
	}
	source, err := provisionObjectStoreFromEnv(sourceValues)
	if err != nil {
		return fmt.Errorf("source storage: %w", err)
	}
	token := destinationValues["LINODE_TOKEN"]
	bucket, err := c.resolveStorageBucket(token, target)
	if err != nil {
		return err
	}
	endpoint, err := normalizeLinodeS3Endpoint(bucket.S3Endpoint)
	if err != nil {
		return err
	}
	destination := provisionObjectStore{bucket: target.Bucket, endpoint: endpoint, region: target.Region, accessKey: access, secretKey: secret}
	statePath := filepath.Join(cfg.RuntimeRoot, "state", stateName)
	if purpose == "ota" {
		return c.migrateOTAFirmwareObjects(cfg, source, destination, statePath)
	}
	if purpose == "artifacts" {
		if source.prefix != "" {
			return errors.New("artifact migration requires the complete source bucket; LINODE_OBJ_PREFIX must be empty")
		}
		// releases/, ci/, SDK and other consumers already own their namespaces.
		// Preserve every physical key instead of prepending the release prefix.
		return c.migrateStorageObjects(cfg.Environment, source, destination, "", statePath, "release-artifacts", namespaces)
	}
	return c.migrateStorageObjects(cfg.Environment, source, destination, target.Prefix, statePath, "media", namespaces)
}

func (c deploymentCredentialChecker) migrateStorageObjects(environment string, source, destination provisionObjectStore, prefix, statePath, purpose string, namespaces []string) error {
	for _, store := range []provisionObjectStore{source, destination} {
		if err := validateStorageCopyBucket(c.client, store); err != nil {
			return err
		}
	}
	want := deploymentStorageMigrationState{
		Environment: environment, Purpose: purpose, Source: source.bucket, SourceRegion: source.region, SourceEndpoint: source.endpoint,
		SourcePrefix: source.prefix, SourcePrefixExplicit: source.prefixSet,
		Destination: destination.bucket, DestinationRegion: destination.region, DestinationEndpoint: destination.endpoint,
		Prefix: strings.Trim(prefix, "/"), Objects: map[string]storageObjectProof{}, SourceKeys: map[string]string{},
	}
	state := want
	if body, err := os.ReadFile(statePath); err == nil {
		if err := json.Unmarshal(body, &state); err != nil {
			return fmt.Errorf("decode existing migration receipt: %w", err)
		}
		if state.Environment != want.Environment || state.Purpose != want.Purpose || state.Source != want.Source || state.SourceRegion != want.SourceRegion || state.SourceEndpoint != want.SourceEndpoint ||
			state.SourcePrefix != want.SourcePrefix || state.SourcePrefixExplicit != want.SourcePrefixExplicit || state.Destination != want.Destination || state.DestinationRegion != want.DestinationRegion ||
			state.DestinationEndpoint != want.DestinationEndpoint || state.Prefix != want.Prefix || state.Objects == nil || state.SourceKeys == nil {
			return errors.New("existing migration receipt targets different storage or predates verified mapping; use a separate reviewed migration receipt")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	objects, err := listStorageSourceObjects(source, prefix, namespaces)
	if err != nil {
		return err
	}
	currentKeys := map[string]string{}
	for _, object := range objects {
		currentKeys[object.SourceKey] = object.DestinationKey
	}
	for key, destinationKey := range state.SourceKeys {
		if currentKeys[key] != destinationKey {
			return fmt.Errorf("source mapping for %s differs from migration receipt", key)
		}
	}
	for _, object := range objects {
		snapshot, err := readStorageObject(c.client, source, object.SourceKey)
		if err != nil {
			return fmt.Errorf("read source object %s: %w", object.SourceKey, err)
		}
		proof := snapshot.proof()
		if recorded, found := state.Objects[object.DestinationKey]; found && recorded != proof {
			return fmt.Errorf("source object %s differs from migration receipt", object.SourceKey)
		}
		if err := copyStorageObject(c.client, snapshot, destination, object.DestinationKey); err != nil {
			return fmt.Errorf("copy %s: %w", object.SourceKey, err)
		}
		// Re-read the source before recording proof so changes during the copy
		// cannot become a successfully resumable receipt.
		current, err := readStorageObject(c.client, source, object.SourceKey)
		if err != nil {
			return err
		}
		if current.proof() != proof {
			return fmt.Errorf("source object %s changed during migration", object.SourceKey)
		}
		state.Objects[object.DestinationKey] = proof
		state.SourceKeys[object.SourceKey] = object.DestinationKey
		state.ObjectCount, state.ByteCount = otaProofTotals(state.Objects)
		state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
		if err := writeStorageState(statePath, state); err != nil {
			return err
		}
	}
	state.ObjectCount, state.ByteCount = otaProofTotals(state.Objects)
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return writeStorageState(statePath, state)
}

// An explicit LINODE_OBJ_PREFIX selects only that source namespace. Profiles
// without it retain the documented root/target-prefix compatibility lookup.
func listStorageSourceObjects(source provisionObjectStore, destinationPrefix string, namespaces []string) ([]otaSourceObject, error) {
	if strings.HasPrefix(source.endpoint, "file://") {
		root, err := provisionFileObjectRoot(source)
		if err != nil {
			return nil, err
		}
		info, err := os.Stat(filepath.Join(root, source.bucket))
		if err != nil {
			return nil, fmt.Errorf("inspect source bucket: %w", err)
		}
		if !info.IsDir() {
			return nil, errors.New("source bucket is not a directory")
		}
	}
	prefixes := []string{"", strings.Trim(destinationPrefix, "/")}
	if source.prefixSet {
		prefixes = []string{source.prefix}
	}
	keys, destinations := map[string]string{}, map[string]string{}
	for _, prefix := range prefixes {
		for _, namespace := range namespaces {
			entries, err := provisionListObjects(source, storageJoinPrefix(prefix, namespace))
			if err != nil {
				return nil, err
			}
			for _, entry := range entries {
				logical := entry.Key
				if prefix != "" {
					logical = strings.TrimPrefix(entry.Key, prefix+"/")
				}
				destinationKey, allowed := storageMigrationDestinationKeyForPurpose(destinationPrefix, logical, namespaces)
				if !allowed {
					return nil, fmt.Errorf("source inventory returned unexpected key %s", entry.Key)
				}
				if previous, found := destinations[destinationKey]; found && previous != entry.Key {
					return nil, fmt.Errorf("source keys %s and %s map to the same destination %s", previous, entry.Key, destinationKey)
				}
				keys[entry.Key], destinations[destinationKey] = destinationKey, entry.Key
			}
		}
	}
	sortedKeys := make([]string, 0, len(keys))
	for key := range keys {
		sortedKeys = append(sortedKeys, key)
	}
	sort.Strings(sortedKeys)
	objects := make([]otaSourceObject, 0, len(keys))
	for _, key := range sortedKeys {
		objects = append(objects, otaSourceObject{SourceKey: key, DestinationKey: keys[key]})
	}
	return objects, nil
}

func storageJoinPrefix(prefix, key string) string {
	if prefix = strings.Trim(prefix, "/"); prefix != "" {
		return prefix + "/" + strings.TrimLeft(key, "/")
	}
	return key
}

func storageMigrationDestinationKey(environmentPrefix, sourceKey string) (string, bool) {
	return storageMigrationDestinationKeyForPurpose(environmentPrefix, sourceKey, []string{"clips/", "brands/", "snapshots/", "clip-index/", "ota/", "firmware/"})
}

func storageMigrationDestinationKeyForPurpose(environmentPrefix, sourceKey string, namespaces []string) (string, bool) {
	prefix := strings.Trim(environmentPrefix, "/")
	if prefix != "" && strings.HasPrefix(sourceKey, prefix+"/") {
		sourceKey = strings.TrimPrefix(sourceKey, prefix+"/")
	}
	allowed := false
	for _, namespace := range namespaces {
		if strings.HasPrefix(sourceKey, namespace) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", false
	}
	return storageJoinPrefix(prefix, sourceKey), true
}

func writeStorageState(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	file, err := os.CreateTemp(filepath.Dir(path), ".storage-state-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(append(body, '\n')); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return os.Rename(file.Name(), path)
}

type linodeStorageBucket struct {
	Label        string `json:"label"`
	Hostname     string `json:"hostname"`
	Region       string `json:"region"`
	Cluster      string `json:"cluster"`
	S3Endpoint   string `json:"s3_endpoint"`
	EndpointType string `json:"endpoint_type"`
}

func validateOTADirectMetricsEndpoint(bucket linodeStorageBucket) error {
	if bucket.EndpointType != "E3" {
		return fmt.Errorf("OTA direct download cutover requires an E3 bucket with Cloud Pulse GET and downloaded-byte metrics; bucket %s uses endpoint type %q", bucket.Label, bucket.EndpointType)
	}
	return nil
}

func (c deploymentCredentialChecker) validateOTAProvisionBucket(token string, env map[string]string) error {
	bucket, err := c.resolveStorageBucket(token, deploymentStorageTarget{
		Purpose: "ota-firmware", Bucket: env["VIDEO_CLOUD_OTA_BLOB_BUCKET"], Region: env["VIDEO_CLOUD_OTA_BLOB_REGION"],
	})
	if err != nil {
		return err
	}
	if err := validateOTADirectMetricsEndpoint(bucket); err != nil {
		return err
	}
	endpoint, err := normalizeLinodeS3Endpoint(bucket.S3Endpoint)
	if err != nil {
		return err
	}
	if env["VIDEO_CLOUD_OTA_BLOB_ENDPOINT_TYPE"] != bucket.EndpointType || strings.TrimRight(env["VIDEO_CLOUD_OTA_BLOB_ENDPOINT"], "/") != endpoint {
		return fmt.Errorf("OTA bucket inventory no longer matches the validated runtime receipt for %s", bucket.Label)
	}
	return nil
}

type linodeStorageKey struct {
	ID           int    `json:"id"`
	AccessKey    string `json:"access_key"`
	BucketAccess []struct {
		BucketName  string `json:"bucket_name"`
		Region      string `json:"region"`
		Permissions string `json:"permissions"`
	} `json:"bucket_access"`
}

type deploymentStorageReceipt struct {
	Environment  string `json:"environment"`
	Purpose      string `json:"purpose"`
	Bucket       string `json:"bucket"`
	Region       string `json:"region"`
	Endpoint     string `json:"endpoint"`
	EndpointType string `json:"endpoint_type,omitempty"`
	KeyID        int    `json:"key_id"`
	AccessSuffix string `json:"access_key_suffix"`
	ValidatedAt  string `json:"validated_at"`
}

func (c deploymentCredentialChecker) checkResolvedObjectStorage(cfg deploymentConfig, values map[string]string) deploymentCredentialCheck {
	target := cfg.Storage.RuntimeMedia
	token := strings.TrimSpace(values["LINODE_TOKEN"])
	if token == "" {
		return deploymentCredentialCheck{Name: "Linode runtime-media storage", Detail: "LINODE_TOKEN is required"}
	}
	if err := c.validateStorageRegionCapabilities(token, target.Region); err != nil {
		return deploymentCredentialCheck{Name: "Linode runtime-media storage", Detail: err.Error()}
	}
	bucket, err := c.resolveStorageBucket(token, target)
	if err != nil {
		return deploymentCredentialCheck{Name: "Linode runtime-media storage", Detail: err.Error()}
	}
	endpoint, err := normalizeLinodeS3Endpoint(bucket.S3Endpoint)
	if err != nil {
		return deploymentCredentialCheck{Name: "Linode runtime-media storage", Detail: err.Error()}
	}
	scopedAccess := strings.TrimSpace(values["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"])
	if scopedAccess == "" {
		if configured := strings.TrimSpace(values["LINODE_OBJ_BUCKET"]); configured != "" && configured != target.Bucket {
			return deploymentCredentialCheck{Name: "Linode runtime-media storage", Detail: fmt.Sprintf("legacy bucket %s conflicts with runtime storage plan bucket %s", configured, target.Bucket)}
		}
		if configured := strings.TrimSpace(values["LINODE_OBJ_REGION"]); configured != "" && configured != target.Region {
			return deploymentCredentialCheck{Name: "Linode runtime-media storage", Detail: fmt.Sprintf("legacy region %s conflicts with runtime storage plan region %s", configured, target.Region)}
		}
		if configured := strings.TrimRight(strings.TrimSpace(values["LINODE_OBJ_ENDPOINT"]), "/"); configured != "" && configured != endpoint {
			return deploymentCredentialCheck{Name: "Linode runtime-media storage", Detail: fmt.Sprintf("legacy endpoint %s does not match Linode inventory endpoint %s", configured, endpoint)}
		}
	}
	access := firstNonEmpty(scopedAccess, values["LINODE_OBJ_ACCESS_KEY_ID"])
	secret := firstNonEmpty(values["LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY"], values["LINODE_OBJ_SECRET_ACCESS_KEY"])
	if access == "" || secret == "" {
		return deploymentCredentialCheck{Name: "Linode runtime-media storage", Detail: "LINODE_MEDIA_OBJ_ACCESS_KEY_ID and LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY are required"}
	}
	key, err := c.resolveAuthorizedStorageKey(token, access, target)
	if err != nil {
		return deploymentCredentialCheck{Name: "Linode runtime-media storage", Detail: err.Error()}
	}
	store := provisionObjectStore{bucket: target.Bucket, endpoint: endpoint, accessKey: access, secretKey: secret, region: target.Region}
	if c.readOnly {
		return c.checkStorageReadOnly(store, target.Prefix, "Linode runtime-media storage")
	}
	if err := c.validateStorageReadWriteCanary(store, target.Prefix); err != nil {
		return deploymentCredentialCheck{Name: "Linode runtime-media storage", Detail: err.Error()}
	}
	receipt := deploymentStorageReceipt{Environment: cfg.Environment, Purpose: target.Purpose, Bucket: target.Bucket, Region: target.Region, Endpoint: endpoint, KeyID: key.ID, AccessSuffix: redactAccessKey(access), ValidatedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := writeDeploymentStorageReceipt(cfg.RuntimeRoot, receipt); err != nil {
		return deploymentCredentialCheck{Name: "Linode runtime-media storage", Detail: "validation passed but receipt could not be written"}
	}
	return deploymentCredentialCheck{Name: "Linode runtime-media storage", Passed: true, Detail: "inventory region/endpoint, limited key scope, signed list, and write/read/delete canary verified"}
}

func (c deploymentCredentialChecker) checkResolvedArtifactStorage(cfg deploymentConfig, values map[string]string) deploymentCredentialCheck {
	target := cfg.Storage.ReleaseArtifacts
	token := strings.TrimSpace(values["LINODE_TOKEN"])
	if token == "" {
		return deploymentCredentialCheck{Name: "Linode release-artifact storage", Detail: "LINODE_TOKEN is required"}
	}
	bucket, err := c.resolveStorageBucket(token, target)
	if err != nil {
		return deploymentCredentialCheck{Name: "Linode release-artifact storage", Detail: err.Error()}
	}
	endpoint, err := normalizeLinodeS3Endpoint(bucket.S3Endpoint)
	if err != nil {
		return deploymentCredentialCheck{Name: "Linode release-artifact storage", Detail: err.Error()}
	}
	scopedAccess := strings.TrimSpace(values["LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID"])
	if scopedAccess == "" {
		if configured := strings.TrimSpace(values["LINODE_OBJ_BUCKET"]); configured != "" && configured != target.Bucket {
			return deploymentCredentialCheck{Name: "Linode release-artifact storage", Detail: fmt.Sprintf("legacy bucket %s conflicts with release-artifact bucket %s", configured, target.Bucket)}
		}
		if configured := strings.TrimSpace(values["LINODE_OBJ_REGION"]); configured != "" && configured != target.Region {
			return deploymentCredentialCheck{Name: "Linode release-artifact storage", Detail: fmt.Sprintf("legacy region %s conflicts with release-artifact region %s", configured, target.Region)}
		}
		if configured := strings.TrimRight(strings.TrimSpace(values["LINODE_OBJ_ENDPOINT"]), "/"); configured != "" && configured != endpoint {
			return deploymentCredentialCheck{Name: "Linode release-artifact storage", Detail: fmt.Sprintf("legacy endpoint %s does not match release-artifact inventory endpoint %s", configured, endpoint)}
		}
	}
	access := firstNonEmpty(scopedAccess, values["LINODE_OBJ_ACCESS_KEY_ID"])
	secret := firstNonEmpty(values["LINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY"], values["LINODE_OBJ_SECRET_ACCESS_KEY"])
	if access == "" || secret == "" {
		return deploymentCredentialCheck{Name: "Linode release-artifact storage", Detail: "LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID and LINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY are required"}
	}
	key, err := c.resolveAuthorizedStorageKey(token, access, target)
	if err != nil {
		return deploymentCredentialCheck{Name: "Linode release-artifact storage", Detail: err.Error()}
	}
	store := provisionObjectStore{bucket: target.Bucket, endpoint: endpoint, accessKey: access, secretKey: secret, region: target.Region}
	if c.readOnly {
		return c.checkStorageReadOnly(store, target.Prefix, "Linode release-artifact storage")
	}
	if err := c.validateStorageReadWriteCanary(store, target.Prefix); err != nil {
		return deploymentCredentialCheck{Name: "Linode release-artifact storage", Detail: err.Error()}
	}
	receipt := deploymentStorageReceipt{Environment: cfg.Environment, Purpose: target.Purpose, Bucket: target.Bucket, Region: target.Region, Endpoint: endpoint, KeyID: key.ID, AccessSuffix: redactAccessKey(access), ValidatedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := writeStorageState(filepath.Join(cfg.RuntimeRoot, "state", "storage-preflight-release-artifacts.json"), receipt); err != nil {
		return deploymentCredentialCheck{Name: "Linode release-artifact storage", Detail: "validation passed but receipt could not be written"}
	}
	return deploymentCredentialCheck{Name: "Linode release-artifact storage", Passed: true, Detail: "shared policy, inventory region/endpoint, limited key scope, signed list, and write/read/delete canary verified"}
}

func (c deploymentCredentialChecker) validateStorageRegionCapabilities(token, region string) error {
	body, err := c.linodeAuthorizedRequest(token, http.MethodGet, "/regions/"+url.PathEscape(region), nil)
	if err != nil {
		return fmt.Errorf("region capability lookup failed: %w", err)
	}
	var result struct {
		ID           string   `json:"id"`
		Status       string   `json:"status"`
		Capabilities []string `json:"capabilities"`
	}
	if json.Unmarshal(body, &result) != nil || result.ID != region {
		return errors.New("region capability lookup returned invalid data")
	}
	capabilities := map[string]bool{}
	for _, capability := range result.Capabilities {
		capabilities[strings.ToLower(capability)] = true
	}
	if result.Status != "ok" || !capabilities["kubernetes"] || !capabilities["object storage"] {
		return fmt.Errorf("region %s must be available and support Kubernetes and Object Storage", region)
	}
	return nil
}

func (c deploymentCredentialChecker) resolveStorageBucket(token string, target deploymentStorageTarget) (linodeStorageBucket, error) {
	var inventory []linodeStorageBucket
	if err := c.storageAccountInventory(token, "buckets", &inventory); err != nil {
		return linodeStorageBucket{}, fmt.Errorf("bucket inventory request failed: %w", err)
	}
	for _, bucket := range inventory {
		if bucket.Label != target.Bucket {
			continue
		}
		actualRegion := firstNonEmpty(bucket.Region, bucket.Cluster)
		if actualRegion != target.Region {
			return linodeStorageBucket{}, fmt.Errorf("bucket %s is in %s; %s policy requires %s", target.Bucket, actualRegion, target.Policy, target.Region)
		}
		if strings.TrimSpace(bucket.S3Endpoint) == "" {
			return linodeStorageBucket{}, errors.New("bucket inventory omitted s3_endpoint")
		}
		return bucket, nil
	}
	return linodeStorageBucket{}, fmt.Errorf("bucket %s was not found in Linode inventory", target.Bucket)
}

func (c deploymentCredentialChecker) resolveAuthorizedStorageKey(token, access string, target deploymentStorageTarget) (linodeStorageKey, error) {
	var inventory []linodeStorageKey
	if err := c.storageAccountInventory(token, "keys", &inventory); err != nil {
		return linodeStorageKey{}, fmt.Errorf("access-key inventory request failed: %w", err)
	}
	for _, key := range inventory {
		if key.AccessKey != access {
			continue
		}
		if len(key.BucketAccess) != 1 {
			return linodeStorageKey{}, fmt.Errorf("%w: key must grant exactly one bucket", errStorageKeyWrongTarget)
		}
		for _, grant := range key.BucketAccess {
			if grant.BucketName == target.Bucket && grant.Region == target.Region && grant.Permissions == "read_write" {
				return key, nil
			}
		}
		return linodeStorageKey{}, fmt.Errorf("%w for bucket %s in region %s", errStorageKeyWrongTarget, target.Bucket, target.Region)
	}
	return linodeStorageKey{}, errStorageKeyMissing
}

var (
	errStorageKeyWrongTarget = errors.New("configured access key is not limited read_write")
	errStorageKeyMissing     = errors.New("configured access-key ID was not found in Linode inventory")
)

func (c deploymentCredentialChecker) validateStorageReadWriteCanary(store provisionObjectStore, prefix string) (err error) {
	query := url.Values{"list-type": {"2"}, "max-keys": {"1"}, "prefix": {strings.Trim(prefix, "/") + "/"}}
	if _, err := provisionSignedObjectRequestWithClient(c.client, store, http.MethodGet, "", query, nil); err != nil {
		return fmt.Errorf("signed list failed: %w", err)
	}
	body := []byte("rtk-cloud-storage-canary")
	key := strings.Trim(prefix, "/") + "/__rtk_cloud_validation__/" + fmt.Sprintf("%d-%s", time.Now().UTC().UnixNano(), hex.EncodeToString(sha256.New().Sum(nil))[:8])
	cleaned := false
	defer func() {
		if !cleaned {
			cleanupCtx, cancel := deploymentCleanupContext()
			defer cancel()
			cleanup := c
			cleanup.trace = nil
			cleanup = cleanup.withCheckContext(cleanupCtx)
			if _, cleanupErr := provisionSignedObjectRequestWithClient(cleanup.client, store, http.MethodDelete, key, nil, nil); cleanupErr != nil {
				err = errors.Join(err, fmt.Errorf("canary cleanup failed: %w", cleanupErr))
			}
		}
	}()
	if _, err := provisionSignedObjectRequestWithClient(c.client, store, http.MethodPut, key, nil, body); err != nil {
		return fmt.Errorf("write canary failed: %w", err)
	}
	read, err := provisionSignedObjectRequestWithClient(c.client, store, http.MethodGet, key, nil, nil)
	if err != nil {
		return fmt.Errorf("read canary failed: %w", err)
	}
	if string(read) != string(body) {
		return errors.New("read canary content mismatch")
	}
	if _, err := provisionSignedObjectRequestWithClient(c.client, store, http.MethodDelete, key, nil, nil); err != nil {
		return fmt.Errorf("delete canary failed: %w", err)
	}
	cleaned = true
	return nil
}

func (c deploymentCredentialChecker) validateNewStorageKey(store provisionObjectStore, prefix string) error {
	const attempts = 6
	var err error
	for attempt := 1; attempt <= attempts; attempt++ {
		err = c.validateStorageReadWriteCanary(store, prefix)
		if err == nil {
			return nil
		}
		var httpErr *provisionObjectStorageHTTPError
		if !errors.As(err, &httpErr) || httpErr.StatusCode != http.StatusForbidden || attempt == attempts {
			return err
		}
		timer := time.NewTimer(5 * time.Second)
		select {
		case <-c.checkContext().Done():
			timer.Stop()
			return c.checkContext().Err()
		case <-timer.C:
		}
	}
	return err
}

func (c deploymentCredentialChecker) linodeAuthorizedRequest(token, method, path string, body []byte) ([]byte, error) {
	req, err := http.NewRequestWithContext(context.Background(), method, strings.TrimRight(c.linodeAPIRoot, "/")+path, strings.NewReader(string(body)))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	return c.request(req)
}

func (c deploymentCredentialChecker) resolveStorageEndpoint(token, region string) (string, error) {
	body, err := c.linodeAuthorizedRequest(token, http.MethodGet, "/object-storage/endpoints?page_size=500", nil)
	if err != nil {
		return "", fmt.Errorf("Object Storage endpoint discovery failed: %w", err)
	}
	var inventory struct {
		Data []struct {
			Region     string `json:"region"`
			S3Endpoint string `json:"s3_endpoint"`
			Endpoint   string `json:"endpoint"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &inventory) != nil {
		return "", errors.New("Object Storage endpoint inventory returned invalid JSON")
	}
	foundRegion := false
	for _, item := range inventory.Data {
		if item.Region == region {
			foundRegion = true
			if endpoint := firstNonEmpty(item.S3Endpoint, item.Endpoint); endpoint != "" {
				return normalizeLinodeS3Endpoint(endpoint)
			}
		}
	}
	if foundRegion {
		return "", fmt.Errorf("%w for region %s", errStorageEndpointUnassigned, region)
	}
	return "", fmt.Errorf("Linode API reported no Object Storage endpoint for region %s", region)
}

func (c deploymentCredentialChecker) resolveOTAMetricsEndpointType(token, region string) (string, error) {
	body, err := c.linodeAuthorizedRequest(token, http.MethodGet, "/object-storage/endpoints?page_size=500", nil)
	if err != nil {
		return "", fmt.Errorf("OTA Object Storage endpoint discovery failed: %w", err)
	}
	var inventory struct {
		Data []struct {
			Region       string `json:"region"`
			EndpointType string `json:"endpoint_type"`
			S3Endpoint   string `json:"s3_endpoint"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &inventory); err != nil {
		return "", errors.New("OTA Object Storage endpoint inventory returned invalid JSON")
	}
	for _, item := range inventory.Data {
		if item.Region == region && item.EndpointType == "E3" && item.S3Endpoint != "" {
			return "E3", nil
		}
	}
	return "", fmt.Errorf("region %s has no assigned E3 Object Storage endpoint for billable OTA direct delivery", region)
}

func normalizeLinodeS3Endpoint(raw string) (string, error) {
	raw = strings.TrimRight(strings.TrimSpace(raw), "/")
	if raw == "" {
		return "", errors.New("bucket inventory omitted s3_endpoint")
	}
	if !strings.Contains(raw, "://") {
		raw = "https://" + raw
	}
	u, err := url.Parse(raw)
	loopbackTestEndpoint := u.Scheme == "http" && (strings.HasPrefix(u.Host, "127.0.0.1:") || strings.HasPrefix(u.Host, "localhost:"))
	if err != nil || u.Host == "" || (u.Scheme != "https" && !loopbackTestEndpoint) {
		return "", errors.New("bucket inventory returned an invalid s3_endpoint")
	}
	return u.String(), nil
}

func redactAccessKey(value string) string {
	if len(value) <= 6 {
		return "***"
	}
	return "..." + value[len(value)-6:]
}

func writeDeploymentStorageReceipt(runtimeRoot string, receipt deploymentStorageReceipt) error {
	path := filepath.Join(runtimeRoot, "state", "storage-preflight.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o600)
}

func readDeploymentStorageReceipt(runtimeRoot string) (deploymentStorageReceipt, error) {
	var receipt deploymentStorageReceipt
	body, err := os.ReadFile(filepath.Join(runtimeRoot, "state", "storage-preflight.json"))
	if err != nil {
		return receipt, err
	}
	if err := json.Unmarshal(body, &receipt); err != nil {
		return receipt, err
	}
	return receipt, nil
}
