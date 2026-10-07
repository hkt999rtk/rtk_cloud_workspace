package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

var errStorageBucketMissing = errors.New("configured storage bucket is missing")

// Only bucket creation is permitted here. Credentials, objects and cutover
// evidence remain unchanged; other preflight probes keep their read-only mode.
func prepareDeploymentMediaBucket(ctx context.Context, cfg deploymentConfig, out io.Writer) error {
	if cfg.Adapter != "lke" || cfg.Storage.RuntimeMedia.Bucket == "" || cfg.Storage.RuntimeMediaCutoverRequired {
		return nil
	}
	values, profile := deploymentCredentialProfileValues(cfg.Environment, defaultDeploymentEnvironmentCredentialFile(cfg.Environment), "")
	if !profile.Passed {
		return errors.New("environment credentials are unavailable for bucket preparation")
	}
	checker := defaultDeploymentCredentialChecker().withCheckContext(ctx)
	created, err := checker.ensurePreflightMediaBucket(cfg, values["LINODE_TOKEN"])
	if err != nil {
		return err
	}
	status := "EXISTS"
	if created {
		status = "CREATED"
	}
	fmt.Fprintf(out, "[PASS] provider.storage.media.bucket %s bucket=%s region=%s\n", status, cfg.Storage.RuntimeMedia.Bucket, cfg.Storage.RuntimeMedia.Region)
	return nil
}

func (c deploymentCredentialChecker) ensurePreflightMediaBucket(cfg deploymentConfig, token string) (bool, error) {
	if strings.TrimSpace(token) == "" {
		return false, errors.New("LINODE_TOKEN is required for bucket preparation")
	}
	// Always use fresh inventory, including the readback after creation.
	c.session = nil
	target := cfg.Storage.RuntimeMedia
	bucket, err := c.resolveStorageBucket(token, target)
	if err == nil {
		_, err = normalizeLinodeS3Endpoint(bucket.S3Endpoint)
		return false, err
	}
	if !errors.Is(err, errStorageBucketMissing) {
		return false, err
	}
	if cfg.Storage.RuntimeMediaCutoverRequired {
		return false, errors.New("bucket creation requires the configured storage cutover lifecycle")
	}
	if err := validateStorageBucketCreation(cfg.Environment, "runtime", target); err != nil {
		return false, err
	}
	if err := recoveryMutationGuard([]string{"deployment", "storage-bootstrap", "--environment", cfg.Environment}); err != nil {
		return false, err
	}
	if err := c.validateStorageRegionCapabilities(token, target.Region); err != nil {
		return false, err
	}
	payload, _ := json.Marshal(map[string]string{"label": target.Bucket, "region": target.Region})
	if _, err := c.linodeAuthorizedRequest(token, http.MethodPost, "/object-storage/buckets", payload); err != nil {
		return false, fmt.Errorf("create configured media bucket: %w", err)
	}
	bucket, err = c.resolveStorageBucket(token, target)
	if err != nil {
		return false, fmt.Errorf("created media bucket readback failed: %w", err)
	}
	if _, err := normalizeLinodeS3Endpoint(bucket.S3Endpoint); err != nil {
		return false, err
	}
	return true, nil
}
