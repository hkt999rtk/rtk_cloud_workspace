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
	"strings"
	"time"
)

type deploymentStorageMigrationState struct {
	Environment string                        `json:"environment"`
	Source      string                        `json:"source_bucket"`
	Destination string                        `json:"destination_bucket"`
	Prefix      string                        `json:"destination_prefix"`
	Objects     map[string]storageObjectProof `json:"objects"`
	ObjectCount int                           `json:"object_count"`
	ByteCount   int64                         `json:"byte_count"`
	UpdatedAt   string                        `json:"updated_at"`
}

type storageObjectProof struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

var errStorageEndpointUnassigned = errors.New("Object Storage endpoint is not assigned to this account")

func runDeploymentStorageLifecycle(action string, cfg deploymentConfig, environmentFile, sourceFile string, keyID int) error {
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
		plan.ReleaseArtifacts.Endpoint = artifactEndpoint
		body, _ := json.MarshalIndent(map[string]any{"environment": cfg.Environment, "compute_region": cfg.AdapterResolved["LKE_REGION"], "storage": plan, "endpoint_status": endpointStatus, "credential_source": "environment SecretStore"}, "", "  ")
		fmt.Println(string(body))
		return nil
	case "storage-bootstrap":
		return checker.bootstrapRuntimeStorage(cfg, values, environmentFile)
	case "storage-migrate":
		if sourceFile == "" {
			return errors.New("--source-env-file is required for storage-migrate")
		}
		return checker.migrateRuntimeStorage(cfg, values, sourceFile)
	case "storage-cutover":
		if check := checker.checkResolvedObjectStorage(cfg, values); !check.Passed {
			return errors.New(check.Detail)
		}
		return checker.cutoverRuntimeStorage(cfg, values)
	case "storage-retire":
		if keyID <= 0 {
			return errors.New("--key-id is required for storage-retire")
		}
		return checker.retireStorageKey(cfg, values, keyID)
	default:
		return fmt.Errorf("unsupported storage action %s", action)
	}
}

func (c deploymentCredentialChecker) cutoverRuntimeStorage(cfg deploymentConfig, values map[string]string) error {
	if err := materializeDeploymentRuntime(cfg); err != nil {
		return err
	}
	receipt, err := readDeploymentStorageReceipt(cfg.RuntimeRoot)
	if err != nil {
		return err
	}
	values["LINODE_OBJ_BUCKET"], values["LINODE_OBJ_REGION"], values["LINODE_OBJ_ENDPOINT"] = cfg.Storage.RuntimeMedia.Bucket, cfg.Storage.RuntimeMedia.Region, receipt.Endpoint
	restore := installDeploymentChildCredentialEnvironment(values)
	defer restore()
	store := provisionObjectStore{bucket: cfg.Storage.RuntimeMedia.Bucket, endpoint: receipt.Endpoint, region: cfg.Storage.RuntimeMedia.Region, accessKey: values["LINODE_OBJ_ACCESS_KEY_ID"], secretKey: values["LINODE_OBJ_SECRET_ACCESS_KEY"]}
	if err := c.validateClipStorageSmoke(store, cfg.Storage.RuntimeMedia.Prefix); err != nil {
		return err
	}
	secretStore, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	kubeconfig := secretStore.KubeconfigPath()
	if _, err := os.Stat(kubeconfig); err != nil {
		return errors.New("staging kubeconfig is required to cut over and roll workloads")
	}
	oldKubeconfig, hadKubeconfig := os.LookupEnv("RTK_CLOUD_LKE_KUBECONFIG")
	_ = os.Setenv("RTK_CLOUD_LKE_KUBECONFIG", kubeconfig)
	defer func() {
		if hadKubeconfig {
			_ = os.Setenv("RTK_CLOUD_LKE_KUBECONFIG", oldKubeconfig)
		} else {
			_ = os.Unsetenv("RTK_CLOUD_LKE_KUBECONFIG")
		}
	}()
	stack, err := readEnvFile(filepath.Join(cfg.RuntimeRoot, "env", "stack.env"))
	if err != nil {
		return err
	}
	if err := kubectlApply(lkeVideoCloudRuntimeSecretManifest(stack)); err != nil {
		return err
	}
	namespace := lkeNamespaceName(stack, "video-cloud")
	for _, deployment := range []string{"video-cloud-api", "video-cloud-clipverifier"} {
		if err := runKubectl("-n", namespace, "rollout", "restart", "deployment/"+deployment); err != nil {
			return err
		}
		if err := runKubectl("-n", namespace, "rollout", "status", "deployment/"+deployment, "--timeout", firstNonEmpty(os.Getenv("LKE_ROLLOUT_TIMEOUT"), "5m")); err != nil {
			return err
		}
	}
	state := map[string]any{"environment": cfg.Environment, "bucket": cfg.Storage.RuntimeMedia.Bucket, "cutover_at": time.Now().UTC().Format(time.RFC3339), "rollback_credentials_retained": true, "workloads_rolled": []string{"video-cloud-api", "video-cloud-clipverifier"}, "clip_storage_smoke": "pass"}
	return writeStorageState(filepath.Join(cfg.RuntimeRoot, "state", "storage-cutover.json"), state)
}

func (c deploymentCredentialChecker) validateClipStorageSmoke(store provisionObjectStore, prefix string) error {
	body := []byte("rtk-cloud-clip-storage-smoke")
	key := strings.Trim(prefix, "/") + "/clips/__rtk_cloud_cutover_smoke__/" + fmt.Sprintf("%d", time.Now().UTC().UnixNano())
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
	return c.bootstrapArtifactStorage(cfg, values)
}

func (c deploymentCredentialChecker) bootstrapArtifactStorage(cfg deploymentConfig, values map[string]string) error {
	if c.checkResolvedArtifactStorage(cfg, values).Passed {
		return nil
	}
	target := cfg.Storage.ReleaseArtifacts
	bucket, err := c.resolveStorageBucket(values["LINODE_TOKEN"], target)
	if err != nil {
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
	environmentFile := defaultDeploymentEnvironmentCredentialFile(cfg.Environment)
	if err := ensureCredentialProfile(environmentFile); err != nil {
		return err
	}
	replacements := map[string]string{
		"LINODE_TOKEN":                          values["LINODE_TOKEN"],
		"GHCR_PULL_USERNAME":                    values["GHCR_PULL_USERNAME"],
		"GHCR_PULL_TOKEN":                       values["GHCR_PULL_TOKEN"],
		"GODADDY_KEY":                           values["GODADDY_KEY"],
		"GODADDY_SECRET":                        values["GODADDY_SECRET"],
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
	if check := c.checkResolvedObjectStorage(cfg, destinationValues); !check.Passed {
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
	bucket, err := c.resolveStorageBucket(token, cfg.Storage.RuntimeMedia)
	if err != nil {
		return err
	}
	endpoint, err := normalizeLinodeS3Endpoint(bucket.S3Endpoint)
	if err != nil {
		return err
	}
	destination := provisionObjectStore{bucket: cfg.Storage.RuntimeMedia.Bucket, endpoint: endpoint, region: cfg.Storage.RuntimeMedia.Region, accessKey: firstNonEmpty(destinationValues["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"], destinationValues["LINODE_OBJ_ACCESS_KEY_ID"]), secretKey: firstNonEmpty(destinationValues["LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY"], destinationValues["LINODE_OBJ_SECRET_ACCESS_KEY"])}
	statePath := filepath.Join(cfg.RuntimeRoot, "state", "storage-migration.json")
	state := deploymentStorageMigrationState{Environment: cfg.Environment, Source: source.bucket, Destination: destination.bucket, Prefix: cfg.Storage.RuntimeMedia.Prefix, Objects: map[string]storageObjectProof{}}
	if body, readErr := os.ReadFile(statePath); readErr == nil {
		_ = json.Unmarshal(body, &state)
		if state.Objects == nil {
			state.Objects = map[string]storageObjectProof{}
		}
	}
	for _, namespace := range []string{"clips/", "brands/", "firmware/"} {
		entries, listErr := provisionListObjects(source, namespace)
		if listErr != nil {
			return listErr
		}
		for _, entry := range entries {
			destinationKey, allowed := storageMigrationDestinationKey(cfg.Storage.RuntimeMedia.Prefix, entry.Key)
			if !allowed {
				continue
			}
			if _, done := state.Objects[destinationKey]; done {
				continue
			}
			data, readErr := provisionReadObject(source, entry.Key)
			if readErr != nil {
				return readErr
			}
			if _, writeErr := provisionSignedObjectRequestWithClient(c.client, destination, http.MethodPut, destinationKey, nil, data); writeErr != nil {
				return writeErr
			}
			written, verifyErr := provisionSignedObjectRequestWithClient(c.client, destination, http.MethodGet, destinationKey, nil, nil)
			if verifyErr != nil {
				return verifyErr
			}
			sourceSum, destinationSum := sha256.Sum256(data), sha256.Sum256(written)
			if sourceSum != destinationSum {
				return fmt.Errorf("checksum mismatch for %s", entry.Key)
			}
			state.Objects[destinationKey] = storageObjectProof{SHA256: hex.EncodeToString(sourceSum[:]), Bytes: int64(len(data))}
			state.ObjectCount++
			state.ByteCount += int64(len(data))
			state.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
			if err := writeStorageState(statePath, state); err != nil {
				return err
			}
		}
	}
	return nil
}

func storageMigrationDestinationKey(environmentPrefix, sourceKey string) (string, bool) {
	allowed := false
	for _, namespace := range []string{"clips/", "brands/", "firmware/"} {
		if strings.HasPrefix(sourceKey, namespace) {
			allowed = true
			break
		}
	}
	if !allowed {
		return "", false
	}
	return strings.Trim(environmentPrefix, "/") + "/" + strings.TrimLeft(sourceKey, "/"), true
}

func (c deploymentCredentialChecker) retireStorageKey(cfg deploymentConfig, values map[string]string, keyID int) error {
	cutoverPath := filepath.Join(cfg.RuntimeRoot, "state", "storage-cutover.json")
	if _, err := os.Stat(cutoverPath); err != nil {
		return errors.New("recorded storage cutover state is required before retirement")
	}
	consumerInventory := filepath.Join(cfg.RuntimeRoot, "state", "storage-consumers.json")
	body, err := os.ReadFile(consumerInventory)
	if err != nil || !bytes.Contains(body, []byte(`"generic_key_in_use": false`)) {
		return errors.New("consumer inventory must confirm generic_key_in_use is false")
	}
	_, err = c.linodeAuthorizedRequest(values["LINODE_TOKEN"], http.MethodDelete, fmt.Sprintf("/object-storage/keys/%d", keyID), nil)
	return err
}

func writeStorageState(path string, value any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(body, '\n'), 0o600)
}

type linodeStorageBucket struct {
	Label      string `json:"label"`
	Region     string `json:"region"`
	Cluster    string `json:"cluster"`
	S3Endpoint string `json:"s3_endpoint"`
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
	body, err := c.linodeAuthorizedRequest(token, http.MethodGet, "/object-storage/buckets?page_size=500", nil)
	if err != nil {
		return linodeStorageBucket{}, fmt.Errorf("bucket inventory request failed: %w", err)
	}
	var inventory struct {
		Data []linodeStorageBucket `json:"data"`
	}
	if json.Unmarshal(body, &inventory) != nil {
		return linodeStorageBucket{}, errors.New("bucket inventory returned invalid JSON")
	}
	for _, bucket := range inventory.Data {
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
	body, err := c.linodeAuthorizedRequest(token, http.MethodGet, "/object-storage/keys?page_size=500", nil)
	if err != nil {
		return linodeStorageKey{}, fmt.Errorf("access-key inventory request failed: %w", err)
	}
	var inventory struct {
		Data []linodeStorageKey `json:"data"`
	}
	if json.Unmarshal(body, &inventory) != nil {
		return linodeStorageKey{}, errors.New("access-key inventory returned invalid JSON")
	}
	for _, key := range inventory.Data {
		if key.AccessKey != access {
			continue
		}
		for _, grant := range key.BucketAccess {
			if grant.BucketName == target.Bucket && grant.Region == target.Region && grant.Permissions == "read_write" {
				return key, nil
			}
		}
		return linodeStorageKey{}, fmt.Errorf("configured access key is not limited read_write for bucket %s in region %s", target.Bucket, target.Region)
	}
	return linodeStorageKey{}, errors.New("configured access-key ID was not found in Linode inventory")
}

func (c deploymentCredentialChecker) validateStorageReadWriteCanary(store provisionObjectStore, prefix string) error {
	query := url.Values{"list-type": {"2"}, "max-keys": {"1"}, "prefix": {strings.Trim(prefix, "/") + "/"}}
	if _, err := provisionSignedObjectRequestWithClient(c.client, store, http.MethodGet, "", query, nil); err != nil {
		return fmt.Errorf("signed list failed: %w", err)
	}
	body := []byte("rtk-cloud-storage-canary")
	key := strings.Trim(prefix, "/") + "/__rtk_cloud_validation__/" + fmt.Sprintf("%d-%s", time.Now().UTC().UnixNano(), hex.EncodeToString(sha256.New().Sum(nil))[:8])
	if _, err := provisionSignedObjectRequestWithClient(c.client, store, http.MethodPut, key, nil, body); err != nil {
		return fmt.Errorf("write canary failed: %w", err)
	}
	cleaned := false
	defer func() {
		if !cleaned {
			_, _ = provisionSignedObjectRequestWithClient(c.client, store, http.MethodDelete, key, nil, nil)
		}
	}()
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
		time.Sleep(5 * time.Second)
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
