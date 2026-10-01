package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

// This scoped update adds trusted public manifest keys to the live OTA
// Service without reapplying its registration Deployment or network policies.
func runDeploymentOTAManifestTrust(args []string) error {
	return runDeploymentOTAManifestTrustWithCredentials(args, func(environment string) (func(), error) {
		_, restore, err := configureProvisionSecretStore(environment)
		return restore, err
	})
}

func runDeploymentOTAManifestTrustWithCredentials(args []string, credentials func(string) (func(), error)) error {
	fs := flag.NewFlagSet("deployment ota-manifest-trust", flag.ContinueOnError)
	environment := fs.String("environment", "", "selected environment")
	workspace := fs.String("workspace", "", "workspace root")
	confirm := fs.String("confirm", "", "selected stack name for mutation")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *environment == "" {
		return errors.New("--environment is required and positional arguments are not accepted")
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, "")
	if err != nil {
		return err
	}
	if err := requireTargetedOTAEnvironment(cfg); err != nil {
		return err
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	env, err := selectedOTAEnvironment(cfg)
	if err != nil {
		return err
	}
	if env["CLOUD_STACK_NAME"] != cfg.Values["CLOUD_STACK_NAME"] || env["CLOUD_ENV_NAME"] != cfg.Environment {
		return errors.New("resolved OTA runtime does not match the selected environment")
	}
	if err := loadLKEImageManifestDefaults(store.Root, env); err != nil {
		return err
	}
	if *confirm == "" {
		fmt.Fprintf(os.Stdout, "OTA manifest trust update plan: environment=%s stack=%s workload=%s (public keys only)\n", cfg.Environment, env["CLOUD_STACK_NAME"], otaServiceWorkloadName)
		fmt.Fprintln(os.Stdout, "Apply requires the active device mTLS edge, a ready independent OTA Service, additive trust keys, and --confirm STACK.")
		return nil
	}
	if *confirm != env["CLOUD_STACK_NAME"] {
		return errors.New("--confirm must match the selected stack")
	}
	restore, err := credentials(cfg.Environment)
	if err != nil {
		return err
	}
	defer restore()
	if !lkeOTAServiceRegistrationEnabled(env) || !lkeOTAServiceEdgeEnabled(env) ||
		lkeOTARegistrarRegistrationEnabled(env) || lkeOTACoreCutoverEnabled(env) || !lkeOTAEntitlementsRequired(env) {
		return errors.New("OTA manifest trust update requires the independent service and edge active, old registrar and core cutover off, and strict Product entitlements")
	}
	if err := validateDeploymentOTATrustedManifestKeys(env["VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON"]); err != nil {
		return err
	}
	image := lkeVideoCloudImage(env)
	if !strings.Contains(image, "@sha256:") {
		return errors.New("OTA manifest trust update requires an immutable pinned image")
	}
	if err := lkeRequireStoppedOTARegistrar(env); err != nil {
		return err
	}
	if err := lkeRequireReadyOTAServiceEndpoint(env); err != nil {
		return err
	}
	if err := lkeRequireActiveOTADeviceEdgeRoute(env); err != nil {
		return err
	}
	namespace := lkeNamespaceName(env, "video-cloud")
	deployment, err := kubectlResourceJSON(namespace, "deployment", otaServiceWorkloadName)
	if err != nil {
		return err
	}
	patch, already, err := otaManifestTrustPatch(deployment, image, env["VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON"])
	if err != nil {
		return err
	}
	if !already {
		if err := runKubectl("-n", namespace, "patch", "deployment", otaServiceWorkloadName, "--type=json", "-p", patch); err != nil {
			return fmt.Errorf("patch OTA public manifest trust: %w", err)
		}
		if err := runKubectl("-n", namespace, "rollout", "status", "deployment/"+otaServiceWorkloadName, "--timeout", "5m"); err != nil {
			return err
		}
	}
	updated, err := kubectlResourceJSON(namespace, "deployment", otaServiceWorkloadName)
	if err != nil {
		return err
	}
	_, complete, err := otaManifestTrustPatch(updated, image, env["VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON"])
	if err != nil || !complete {
		return fmt.Errorf("OTA Service public trust read-back is incomplete: %v", err)
	}
	if err := lkeRequireReadyOTAServiceEndpoint(env); err != nil {
		return err
	}
	return lkeRequireActiveOTADeviceEdgeRoute(env)
}

// The patch tests resourceVersion and the exact old value before replacing one
// environment entry. It never rewrites the full Pod template or removes keys.
func otaManifestTrustPatch(deployment map[string]any, image, desired string) (string, bool, error) {
	metadata, _ := deployment["metadata"].(map[string]any)
	version, _ := metadata["resourceVersion"].(string)
	if version == "" {
		return "", false, errors.New("OTA Service Deployment lacks resourceVersion")
	}
	spec, _ := deployment["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	containerSpec, _ := template["spec"].(map[string]any)
	containers, _ := containerSpec["containers"].([]any)
	if len(containers) != 1 {
		return "", false, errors.New("OTA Service Deployment container layout changed")
	}
	container, _ := containers[0].(map[string]any)
	if container["name"] != "otaservice" || container["image"] != image {
		return "", false, errors.New("OTA Service Deployment name or pinned image changed")
	}
	environment, _ := container["env"].([]any)
	index := -1
	current := ""
	for i, item := range environment {
		entry, _ := item.(map[string]any)
		if entry["name"] == "VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON" {
			if index != -1 {
				return "", false, errors.New("OTA Service Deployment has duplicate manifest trust entries")
			}
			index = i
			current, _ = entry["value"].(string)
		}
	}
	if index < 0 || current == "" {
		return "", false, errors.New("OTA Service Deployment lacks inline public manifest trust")
	}
	var before, after map[string]string
	if err := json.Unmarshal([]byte(current), &before); err != nil || len(before) == 0 {
		return "", false, errors.New("live OTA manifest trust is invalid")
	}
	if err := json.Unmarshal([]byte(desired), &after); err != nil || len(after) == 0 {
		return "", false, errors.New("desired OTA manifest trust is invalid")
	}
	for id, value := range before {
		if after[id] != value {
			return "", false, fmt.Errorf("OTA manifest trust update would remove or change existing key %q", id)
		}
	}
	if current == desired {
		return "", true, nil
	}
	path := fmt.Sprintf("/spec/template/spec/containers/0/env/%d/value", index)
	operations := []map[string]any{
		{"op": "test", "path": "/metadata/resourceVersion", "value": version},
		{"op": "test", "path": "/spec/template/spec/containers/0/name", "value": "otaservice"},
		{"op": "test", "path": "/spec/template/spec/containers/0/image", "value": image},
		{"op": "test", "path": path, "value": current},
		{"op": "replace", "path": path, "value": desired},
	}
	encoded, err := json.Marshal(operations)
	if err != nil {
		return "", false, err
	}
	return string(encoded), false, nil
}
