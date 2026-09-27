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
	Environment         string                        `json:"environment"`
	Source              string                        `json:"source_bucket"`
	SourceRegion        string                        `json:"source_region,omitempty"`
	SourceEndpoint      string                        `json:"source_endpoint,omitempty"`
	Destination         string                        `json:"destination_bucket"`
	DestinationRegion   string                        `json:"destination_region,omitempty"`
	DestinationEndpoint string                        `json:"destination_endpoint,omitempty"`
	Prefix              string                        `json:"destination_prefix"`
	Objects             map[string]storageObjectProof `json:"objects"`
	ObjectCount         int                           `json:"object_count"`
	ByteCount           int64                         `json:"byte_count"`
	UpdatedAt           string                        `json:"updated_at"`
}

type storageObjectProof struct {
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
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
		Environment                 string `json:"environment"`
		Bucket                      string `json:"bucket"`
		CutoverAt                   string `json:"cutover_at"`
		RollbackCredentialsRetained bool   `json:"rollback_credentials_retained"`
	}
	if json.Unmarshal(body, &receipt) != nil || receipt.Environment != cfg.Environment || receipt.Bucket != cfg.Storage.RuntimeMedia.Bucket || receipt.CutoverAt == "" || !receipt.RollbackCredentialsRetained {
		return fmt.Errorf("runtime media bucket %s has no matching completed cutover receipt", cfg.Storage.RuntimeMedia.Bucket)
	}
	return nil
}

func runDeploymentStorageLifecycle(action string, cfg deploymentConfig, environmentFile, sourceFile string, keyID int) error {
	return runDeploymentStorageLifecyclePurpose(action, cfg, environmentFile, sourceFile, keyID, "media")
}

func runDeploymentStorageLifecyclePurpose(action string, cfg deploymentConfig, environmentFile, sourceFile string, keyID int, purpose string) error {
	if purpose != "media" && purpose != "ota" {
		return errors.New("--purpose must be media or ota")
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
		if purpose == "ota" {
			if sourceFile == "" {
				return errors.New("--source-env-file is required for OTA storage-cutover")
			}
			return checker.cutoverOTAStorage(cfg, values, sourceFile)
		}
		if check := checker.checkResolvedObjectStorage(cfg, values); !check.Passed {
			return errors.New(check.Detail)
		}
		return checker.cutoverRuntimeStorage(cfg, values)
	case "storage-retire":
		if purpose == "ota" {
			return errors.New("OTA key retirement requires separate consumer verification; use Linode key management after verification")
		}
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
	namespace := lkeNamespaceName(stack, "video-cloud")
	if err := ensureMediaCutoverScope(namespace, cfg.Storage.RuntimeMedia.Bucket); err != nil {
		return err
	}
	if err := kubectlApply(lkeVideoCloudRuntimeSecretManifest(stack)); err != nil {
		return err
	}
	for _, deployment := range []string{"video-cloud-api", "video-cloud-clipverifier"} {
		if err := runKubectl("-n", namespace, "set", "env", "deployment/"+deployment,
			"VIDEO_CLOUD_BLOB_BUCKET="+cfg.Storage.RuntimeMedia.Bucket,
			"VIDEO_CLOUD_BLOB_REGION="+cfg.Storage.RuntimeMedia.Region,
			"VIDEO_CLOUD_BLOB_ENDPOINT="+receipt.Endpoint,
			"VIDEO_CLOUD_BLOB_PREFIX="+cfg.Storage.RuntimeMedia.Prefix); err != nil {
			return err
		}
		if err := runKubectl("-n", namespace, "rollout", "status", "deployment/"+deployment, "--timeout", firstNonEmpty(os.Getenv("LKE_ROLLOUT_TIMEOUT"), "5m")); err != nil {
			return err
		}
	}
	state := map[string]any{"environment": cfg.Environment, "bucket": cfg.Storage.RuntimeMedia.Bucket, "cutover_at": time.Now().UTC().Format(time.RFC3339), "rollback_credentials_retained": true, "workloads_rolled": []string{"video-cloud-api", "video-cloud-clipverifier"}, "clip_storage_smoke": "pass"}
	return writeStorageState(filepath.Join(cfg.RuntimeRoot, "state", "storage-cutover.json"), state)
}

func ensureMediaCutoverScope(namespace, destinationBucket string) error {
	body, err := kubectlCombinedOutput(nil, "-n", namespace, "get", "deployments", "-o", "json")
	if err != nil {
		return fmt.Errorf("inspect media consumers before cutover: %w", err)
	}
	return validateMediaCutoverInventory(body, destinationBucket)
}

func validateMediaCutoverInventory(body []byte, destinationBucket string) error {
	var inventory struct {
		Items []struct {
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
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &inventory); err != nil {
		return fmt.Errorf("decode media consumer inventory: %w", err)
	}
	allowed := keySet("video-cloud-api", "video-cloud-clipverifier")
	for _, deployment := range inventory.Items {
		if allowed[deployment.Metadata.Name] {
			continue
		}
		for _, container := range deployment.Spec.Template.Spec.Containers {
			for _, variable := range container.Env {
				if variable.Name == "VIDEO_CLOUD_BLOB_BUCKET" && variable.Value != "" && variable.Value != destinationBucket {
					return fmt.Errorf("media cutover blocked: active deployment %s still references bucket %s", deployment.Metadata.Name, variable.Value)
				}
			}
		}
	}
	return nil
}

func (c deploymentCredentialChecker) cutoverOTAStorage(cfg deploymentConfig, values map[string]string, sourceFile string) error {
	if check := c.checkResolvedOTAStorage(cfg, values); !check.Passed {
		return errors.New(check.Detail)
	}
	metricsEndpoint := ""
	if strings.TrimSpace(cfg.Values["VIDEO_CLOUD_OTA_CDN_BASE_URL"]) == "" {
		bucket, err := c.resolveStorageBucket(values["LINODE_TOKEN"], cfg.Storage.OTAFirmware)
		if err != nil {
			return err
		}
		if err := validateOTADirectMetricsEndpoint(bucket); err != nil {
			return err
		}
		metricsEndpoint, err = normalizeLinodeS3Endpoint(bucket.S3Endpoint)
		if err != nil {
			return err
		}
	}
	if err := materializeDeploymentRuntime(cfg); err != nil {
		return err
	}
	stack, err := readEnvFile(filepath.Join(cfg.RuntimeRoot, "env", "stack.env"))
	if err != nil {
		return err
	}
	if !lkeOTAServiceRegistrationEnabled(stack) {
		return errors.New("OTA service registration is disabled; dedicated bucket is prepared but no workload can be cut over")
	}
	if metricsEndpoint != "" {
		if err := validateOTAMetricsQualification(cfg.RuntimeRoot, cfg.Environment, cfg.Storage.OTAFirmware.Bucket, cfg.Storage.OTAFirmware.Region, metricsEndpoint, time.Now().UTC()); err != nil {
			return err
		}
	}
	secretStore, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	kubeconfig := secretStore.KubeconfigPath()
	if _, err := os.Stat(kubeconfig); err != nil {
		return errors.New("kubeconfig is required for OTA cutover")
	}
	old, had := os.LookupEnv("RTK_CLOUD_LKE_KUBECONFIG")
	_ = os.Setenv("RTK_CLOUD_LKE_KUBECONFIG", kubeconfig)
	defer func() {
		if had {
			_ = os.Setenv("RTK_CLOUD_LKE_KUBECONFIG", old)
		} else {
			_ = os.Unsetenv("RTK_CLOUD_LKE_KUBECONFIG")
		}
	}()
	if err := ensureLiveOTASourceBucket(lkeNamespaceName(stack, "video-cloud"), sourceFile); err != nil {
		return err
	}
	if err := c.validateOTAMigrationCutover(cfg, values, sourceFile); err != nil {
		return err
	}
	restore := installDeploymentChildCredentialEnvironment(values)
	defer restore()
	if err := lkeRequireOTAServiceInputs(stack); err != nil {
		return err
	}
	if err := kubectlApply(lkeOTAStorageSecretManifest(stack)); err != nil {
		return err
	}
	if err := kubectlApply(lkeOTAServiceDeploymentManifest(stack)); err != nil {
		return err
	}
	if err := runKubectl("-n", lkeNamespaceName(stack, "video-cloud"), "rollout", "status", "deployment/"+otaServiceWorkloadName, "--timeout", firstNonEmpty(os.Getenv("LKE_ROLLOUT_TIMEOUT"), "5m")); err != nil {
		return err
	}
	return writeStorageState(filepath.Join(cfg.RuntimeRoot, "state", "storage-cutover-ota.json"), map[string]any{"environment": cfg.Environment, "bucket": cfg.Storage.OTAFirmware.Bucket, "cutover_at": time.Now().UTC().Format(time.RFC3339), "rollback_credentials_retained": true})
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
	if c.checkResolvedOTAStorage(cfg, values).Passed {
		return nil
	}
	target := cfg.Storage.OTAFirmware
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
	return c.migrateStoragePurpose(cfg, destinationValues, sourceFile, "media")
}

func (c deploymentCredentialChecker) migrateStoragePurpose(cfg deploymentConfig, destinationValues map[string]string, sourceFile, purpose string) error {
	target := cfg.Storage.RuntimeMedia
	check := c.checkResolvedObjectStorage(cfg, destinationValues)
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
	state := deploymentStorageMigrationState{Environment: cfg.Environment, Source: source.bucket, Destination: destination.bucket, Prefix: target.Prefix, Objects: map[string]storageObjectProof{}}
	if body, readErr := os.ReadFile(statePath); readErr == nil {
		_ = json.Unmarshal(body, &state)
		if state.Source != source.bucket || state.Destination != destination.bucket || state.Prefix != target.Prefix {
			return errors.New("existing migration state targets different storage")
		}
		if state.Objects == nil {
			state.Objects = map[string]storageObjectProof{}
		}
	}
	for _, namespace := range namespaces {
		for _, sourcePrefix := range []string{namespace, strings.Trim(target.Prefix, "/") + "/" + namespace} {
			entries, listErr := provisionListObjects(source, sourcePrefix)
			if listErr != nil {
				return listErr
			}
			for _, entry := range entries {
				destinationKey, allowed := storageMigrationDestinationKeyForPurpose(target.Prefix, entry.Key, namespaces)
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
				existing, listErr := provisionListObjects(destination, destinationKey)
				if listErr != nil {
					return listErr
				}
				found := false
				for _, object := range existing {
					if object.Key == destinationKey {
						found = true
						break
					}
				}
				if !found {
					if _, writeErr := provisionSignedObjectRequestWithClient(c.client, destination, http.MethodPut, destinationKey, nil, data); writeErr != nil {
						return writeErr
					}
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
	}
	return nil
}

func storageMigrationDestinationKey(environmentPrefix, sourceKey string) (string, bool) {
	return storageMigrationDestinationKeyForPurpose(environmentPrefix, sourceKey, []string{"clips/", "brands/", "snapshots/", "clip-index/", "ota/", "firmware/"})
}

func storageMigrationDestinationKeyForPurpose(environmentPrefix, sourceKey string, namespaces []string) (string, bool) {
	prefix := strings.Trim(environmentPrefix, "/")
	if strings.HasPrefix(sourceKey, prefix+"/") {
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
	return prefix + "/" + strings.TrimLeft(sourceKey, "/"), true
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
	Label        string `json:"label"`
	Region       string `json:"region"`
	Cluster      string `json:"cluster"`
	S3Endpoint   string `json:"s3_endpoint"`
	EndpointType string `json:"endpoint_type"`
}

func validateOTADirectMetricsEndpoint(bucket linodeStorageBucket) error {
	if bucket.EndpointType != "E2" && bucket.EndpointType != "E3" {
		return fmt.Errorf("OTA direct download cutover requires an E2/E3 bucket with Cloud Pulse GET and downloaded-byte metrics; bucket %s uses endpoint type %q", bucket.Label, bucket.EndpointType)
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
	for _, endpointType := range []string{"E3", "E2"} {
		for _, item := range inventory.Data {
			if item.Region == region && item.EndpointType == endpointType && item.S3Endpoint != "" {
				return endpointType, nil
			}
		}
	}
	return "", fmt.Errorf("region %s has no assigned E2/E3 Object Storage endpoint for billable OTA direct delivery", region)
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
