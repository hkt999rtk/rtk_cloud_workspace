package main

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

// Narrow OTA updates are also steps in the reviewed persistent staging release.
// Protected-environment qualification and CI image provenance remain release
// gates; each command additionally checks its selected stack and live inputs.
func requireTargetedOTAEnvironment(cfg deploymentConfig) error {
	if cfg.Adapter != "lke" || (cfg.Environment != "dev" && cfg.Environment != "staging") {
		return errors.New("targeted OTA update requires an existing dev or reviewed staging LKE stack")
	}
	return nil
}

// selectedOTAEnvironment derives scoped command inputs from the selected
// deployment configuration. The SecretStore does not contain generated stack.env.
func selectedOTAEnvironment(cfg deploymentConfig) (map[string]string, error) {
	selected := appendMap(cfg.Values, map[string]string{
		"CLOUD_ENV_NAME": cfg.Environment,
		"CLOUD_PROVIDER": "lke",
		"CLOUD_REGION":   cfg.AdapterResolved["LKE_REGION"],
	})
	selected = envroot.Derive(selected)
	if selected["CLOUD_STACK_NAME"] != cfg.Values["CLOUD_STACK_NAME"] || selected["CLOUD_ENV_NAME"] != cfg.Environment {
		return nil, errors.New("resolved OTA runtime does not match the selected deployment environment")
	}
	env := appendMap(selected, deploymentRuntimeEndpoints(selected))
	env = appendMap(env, cfg.DNSValues)
	env = appendMap(env, cfg.AdapterValues)
	env = appendMap(env, cfg.AdapterResolved)
	return env, nil
}

// A scoped service rollout cannot infer its private origin from a live Pod or
// a generated runtime file that may be absent in a managed worktree. Resolve
// the selected bucket and scoped key against read-only provider inventory.
type selectedOTAStorageCredentials struct {
	Access string
	Secret string
}

func bindSelectedOTAStorage(cfg deploymentConfig, store secretStore, env map[string]string) (selectedOTAStorageCredentials, error) {
	target := cfg.Storage.RuntimeMedia
	if target.Bucket == "" || target.Region == "" {
		return selectedOTAStorageCredentials{}, errors.New("selected OTA runtime storage plan is incomplete")
	}
	operator, err := store.readOperator()
	if err != nil {
		return selectedOTAStorageCredentials{}, errors.New("selected OTA object storage binding is unavailable")
	}
	token := strings.TrimSpace(operator["LINODE_TOKEN"])
	selected := selectedOTAStorageCredentials{
		Access: strings.TrimSpace(operator["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"]),
		Secret: strings.TrimSpace(operator["LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY"]),
	}
	scoped := selected.Access != "" || selected.Secret != ""
	if !scoped {
		selected.Access = strings.TrimSpace(operator["LINODE_OBJ_ACCESS_KEY_ID"])
		selected.Secret = strings.TrimSpace(operator["LINODE_OBJ_SECRET_ACCESS_KEY"])
		if operator["LINODE_OBJ_BUCKET"] != target.Bucket || (operator["LINODE_OBJ_REGION"] != "" && operator["LINODE_OBJ_REGION"] != target.Region) {
			return selectedOTAStorageCredentials{}, errors.New("selected OTA legacy object storage binding conflicts with the runtime storage plan")
		}
	}
	if token == "" || selected.Access == "" || selected.Secret == "" {
		return selectedOTAStorageCredentials{}, errors.New("selected OTA object storage credential pair is incomplete")
	}
	checker := defaultDeploymentCredentialChecker()
	bucket, err := checker.resolveStorageBucket(token, target)
	if err != nil {
		return selectedOTAStorageCredentials{}, fmt.Errorf("validate selected OTA storage bucket: %w", err)
	}
	endpoint, err := normalizeLinodeS3Endpoint(bucket.S3Endpoint)
	if err != nil {
		return selectedOTAStorageCredentials{}, errors.New("selected OTA bucket inventory has no HTTPS endpoint")
	}
	if _, err := checker.resolveAuthorizedStorageKey(token, selected.Access, target); err != nil {
		return selectedOTAStorageCredentials{}, fmt.Errorf("validate selected OTA scoped storage key: %w", err)
	}
	if !scoped {
		configured := strings.TrimRight(strings.TrimSpace(operator["LINODE_OBJ_ENDPOINT"]), "/")
		if configured != "" && configured != endpoint {
			return selectedOTAStorageCredentials{}, errors.New("selected OTA legacy object storage endpoint conflicts with bucket inventory")
		}
	}
	parsed, err := url.Parse(endpoint)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return selectedOTAStorageCredentials{}, errors.New("selected OTA object storage binding requires an HTTPS endpoint")
	}
	if receipt, err := readDeploymentStorageReceipt(cfg.RuntimeRoot); err == nil {
		if receipt.Environment != cfg.Environment || receipt.Purpose != target.Purpose || receipt.Bucket != target.Bucket || receipt.Region != target.Region || strings.TrimRight(receipt.Endpoint, "/") != endpoint {
			return selectedOTAStorageCredentials{}, errors.New("selected OTA object storage binding differs from the validated storage receipt")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return selectedOTAStorageCredentials{}, fmt.Errorf("read selected OTA storage receipt: %w", err)
	}
	for key, desired := range map[string]string{
		"VIDEO_CLOUD_BLOB_BUCKET":   target.Bucket,
		"VIDEO_CLOUD_BLOB_REGION":   target.Region,
		"VIDEO_CLOUD_BLOB_PREFIX":   target.Prefix,
		"VIDEO_CLOUD_BLOB_ENDPOINT": endpoint,
	} {
		if current := strings.TrimSpace(env[key]); current != "" && current != desired {
			return selectedOTAStorageCredentials{}, fmt.Errorf("selected OTA %s conflicts with the runtime storage plan", key)
		}
		env[key] = desired
	}
	return selected, nil
}
