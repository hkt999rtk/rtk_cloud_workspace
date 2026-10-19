package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const environmentOTAObservationPath = "deployment/storage/ota-render-observation.json"

// Public allocated-provider metadata only. This is neither a canary validation
// receipt nor storage cutover evidence. Its stable projection belongs to the
// same input digest as the selected maintained storage target and credentials.
type environmentOTAStorageObservation struct {
	Version      int    `json:"schema_version"`
	Environment  string `json:"environment"`
	Provider     string `json:"provider"`
	Bucket       string `json:"bucket"`
	Region       string `json:"region"`
	Prefix       string `json:"prefix"`
	Endpoint     string `json:"endpoint"`
	EndpointType string `json:"endpoint_type"`
}

type environmentOTAStorageRecord struct {
	Observation environmentOTAStorageObservation `json:"observation"`
	SHA256      string                           `json:"sha256"`
	ObservedAt  string                           `json:"observed_at"`
}

func otaStartupBlocker(id, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.configuration.ota-startup", Resource: id, Cause: cause, Next: "restore the selected maintained dedicated OTA storage intent and original credential/registration references; derive runtime endpoints from current selected allocation and Services, then repeat full preflight; do not initialize another identity or require the future OTA workload to be ready"}
}

func validOTAHTTPSURL(raw string) bool {
	u, err := url.Parse(strings.TrimSpace(raw))
	return err == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

func validateOTAStorageObservation(cfg deploymentConfig, observation environmentOTAStorageObservation) error {
	if cfg.Storage.OTAMode != "dedicated" || observation.Version != 1 || observation.Environment != cfg.Environment || observation.Provider != "linode" || observation.Bucket != cfg.Storage.OTAFirmware.Bucket || observation.Region != cfg.Storage.OTAFirmware.Region || observation.Prefix != cfg.Storage.OTAFirmware.Prefix || !validOTAHTTPSURL(observation.Endpoint) || (cfg.Values["VIDEO_CLOUD_OTA_CDN_BASE_URL"] == "" && observation.EndpointType != "E3") {
		return otaStartupBlocker("", "current OTA allocation observation does not match the selected dedicated storage target")
	}
	return nil
}

func readOTAStorageObservation(cfg deploymentConfig, store secretStore) (environmentOTAStorageObservation, error) {
	var record environmentOTAStorageRecord
	path, err := privateDeploymentStatePath(store.Environment, environmentOTAObservationPath)
	if err != nil {
		return record.Observation, otaStartupBlocker("", "canonical OTA allocation observation path is unsafe")
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	if errors.Is(err, os.ErrNotExist) {
		return record.Observation, nil
	}
	// The bounded reader intentionally sanitizes filesystem errors; distinguish
	// an absent original observation without choosing another source directory.
	if err != nil {
		if _, original := os.Lstat(path); errors.Is(original, os.ErrNotExist) {
			return record.Observation, nil
		}
		return record.Observation, otaStartupBlocker("", "canonical OTA allocation observation is not a bounded private regular file")
	}
	if decodeEnvironmentJSON(raw, &record) != nil || record.SHA256 != deploymentplan.Digest(record.Observation) {
		return record.Observation, otaStartupBlocker("", "canonical OTA allocation observation is malformed or its projection hash differs")
	}
	if _, err := time.Parse(time.RFC3339, record.ObservedAt); err != nil {
		return record.Observation, otaStartupBlocker("", "canonical OTA allocation observation has no genuine observation time")
	}
	return record.Observation, validateOTAStorageObservation(cfg, record.Observation)
}

func observeOTAStorageForRender(ctx context.Context, cfg deploymentConfig, store secretStore, operator map[string]string, checker deploymentCredentialChecker) (environmentOTAStorageObservation, error) {
	var observation environmentOTAStorageObservation
	if cfg.Storage.OTAMode != "dedicated" || cfg.Storage.OTAFirmware.Bucket == "" || cfg.Storage.OTAFirmware.Region == "" || operator["LINODE_TOKEN"] == "" || operator["LINODE_OTA_OBJ_ACCESS_KEY_ID"] == "" || operator["LINODE_OTA_OBJ_SECRET_ACCESS_KEY"] == "" {
		return observation, otaStartupBlocker("", "dedicated OTA storage intent or original canonical provider/storage credentials are unavailable")
	}
	checker = checker.withCheckContext(ctx)
	checker.readOnly, checker.out = true, io.Discard
	bucket, err := checker.resolveStorageBucket(operator["LINODE_TOKEN"], cfg.Storage.OTAFirmware)
	if err != nil {
		return observation, otaStartupBlocker("", "current existing OTA bucket metadata cannot be read from the selected provider")
	}
	endpoint, err := normalizeLinodeS3Endpoint(bucket.S3Endpoint)
	if err != nil || !validOTAHTTPSURL(endpoint) || (cfg.Values["VIDEO_CLOUD_OTA_CDN_BASE_URL"] == "" && bucket.EndpointType != "E3") {
		return observation, otaStartupBlocker("", "current existing OTA bucket has no qualified private HTTPS E3 endpoint")
	}
	observation = environmentOTAStorageObservation{Version: 1, Environment: cfg.Environment, Provider: "linode", Bucket: bucket.Label, Region: firstNonEmpty(bucket.Region, bucket.Cluster), Prefix: cfg.Storage.OTAFirmware.Prefix, Endpoint: endpoint, EndpointType: bucket.EndpointType}
	if err := validateOTAStorageObservation(cfg, observation); err != nil {
		return observation, err
	}
	if err := ctx.Err(); err != nil {
		return observation, otaStartupBlocker("", "current OTA allocation observation was interrupted")
	}
	if previous, err := readOTAStorageObservation(cfg, store); err != nil {
		return observation, err
	} else if previous == observation {
		return observation, nil // A fresh GET proved the stable projection; preserve its original record.
	}
	record := environmentOTAStorageRecord{Observation: observation, SHA256: deploymentplan.Digest(observation), ObservedAt: time.Now().UTC().Format(time.RFC3339)}
	raw, err := json.Marshal(record)
	if err != nil {
		return observation, otaStartupBlocker("", "current OTA allocation observation could not be encoded")
	}
	if err := writePrivateDeploymentState(store.Environment, environmentOTAObservationPath, append(raw, '\n')); err != nil {
		return observation, otaStartupBlocker("", "current OTA allocation observation could not be recorded in the canonical private store")
	}
	return observation, nil
}

func projectOTAStorageRenderInputs(cfg deploymentConfig, observation environmentOTAStorageObservation, values map[string]string) error {
	if cfg.Values["OTA_SERVICE_REGISTRATION_ENABLED"] != "true" {
		return nil
	}
	if err := validateOTAStorageObservation(cfg, observation); err != nil {
		return err
	}
	values["VIDEO_CLOUD_OTA_STORAGE_MODE"] = cfg.Storage.OTAMode
	values["VIDEO_CLOUD_OTA_BLOB_BUCKET"] = cfg.Storage.OTAFirmware.Bucket
	values["VIDEO_CLOUD_OTA_BLOB_REGION"] = cfg.Storage.OTAFirmware.Region
	values["VIDEO_CLOUD_OTA_BLOB_PREFIX"] = cfg.Storage.OTAFirmware.Prefix
	values["VIDEO_CLOUD_OTA_BLOB_ENDPOINT"] = observation.Endpoint
	values["VIDEO_CLOUD_OTA_BLOB_ENDPOINT_TYPE"] = observation.EndpointType
	return nil
}

func checkOTAStorageAccess(ctx context.Context, cfg deploymentConfig, store secretStore, operator map[string]string, checker deploymentCredentialChecker) error {
	observation, err := readOTAStorageObservation(cfg, store)
	if err != nil {
		return err
	}
	if err := validateOTAStorageObservation(cfg, observation); err != nil {
		return err
	}
	checker = checker.withCheckContext(ctx)
	checker.readOnly, checker.out = true, io.Discard
	target := provisionObjectStore{bucket: observation.Bucket, region: observation.Region, endpoint: observation.Endpoint, accessKey: operator["LINODE_OTA_OBJ_ACCESS_KEY_ID"], secretKey: operator["LINODE_OTA_OBJ_SECRET_ACCESS_KEY"]}
	if target.accessKey == "" || target.secretKey == "" {
		return otaStartupBlocker("", "original canonical OTA storage credentials are unavailable")
	}
	if err := checker.validateStoragePrivacy(target); err != nil {
		return otaStartupBlocker("", "original dedicated OTA bucket ACL/policy cannot be qualified as private using read-only access")
	}
	if check := checker.checkStorageReadOnly(target, cfg.Storage.OTAFirmware.Prefix, "OTA storage startup input"); !check.Passed {
		return otaStartupBlocker("", "original dedicated OTA bucket/prefix cannot be read with its selected canonical credentials")
	}
	return nil
}
