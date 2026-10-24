package main

import (
	"encoding/base64"
	"sort"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Legacy frontend helpers read these names from the process environment. The
// common render scope installs every key, including absent values, from its
// hashed configuration/operator snapshot before either producer or consumer runs.
func environmentFrontendSDKRenderKeys() []string {
	return []string{"SDK_DOWNLOADS_ENABLED", "SDK_ARTIFACT_BUCKET", "SDK_ARTIFACT_ENDPOINT", "SDK_ARTIFACT_REGION", "SDK_ARTIFACT_ACCESS_KEY_ID", "SDK_ARTIFACT_SECRET_ACCESS_KEY", "SDK_LATEST_OBJECT_KEY", "PRO2_EXAMPLES_PREFIX"}
}

// Called inside the existing frozen render scope. These are mirrors of selected
// canonical credentials, not new credentials or Service PKI identities.
func renderEnvironmentRuntimeDependencyDefinitions(env, secrets, operator map[string]string) ([]deploymentplan.Definition, error) {
	return renderEnvironmentRuntimeDependencyDefinitionsForBillingProfile(env, secrets, operator, environmentBillingRuntimeKeyProfile)
}

func renderEnvironmentRuntimeDependencyDefinitionsForBillingProfile(env, secrets, operator map[string]string, profile string) ([]deploymentplan.Definition, error) {
	bad := func(resource, cause string) error {
		return &deploymentplan.Blocker{ID: "deployment.runtime.dependencies", Resource: resource, Cause: cause, Next: "restore the exact canonical credential or recorded allocation; qualify the shared complete plan without regenerating existing material"}
	}
	if !activeCanonicalSecretStore || activeRuntimeSecretSnapshot == nil || deploymentplan.Digest(activeRuntimeSecretSnapshot) != deploymentplan.Digest(secrets) || env["CLOUD_STACK_NAME"] != "video-cloud-"+env["CLOUD_ENV_NAME"] {
		return nil, bad("", "runtime dependency rendering requires the frozen selected environment snapshot")
	}
	for _, key := range []string{"SUPPORT_TICKETS_ENABLED", "TEST_LAB_ENABLED", "PAYMENT_SIMULATOR_ENABLED", "NEWEBPAY_ENABLED", "PAYPAL_ENABLED", "SDK_DOWNLOADS_ENABLED"} {
		if value := env[key]; value != "" && value != "true" && value != "false" {
			return nil, bad(key, "runtime feature intent must be an explicit boolean")
		}
	}
	required := []string{"postgres", "billing-service-token", "billing-internal-token", "billing-debit-token", "billing-cloud-creation", "billing-handoff", "mqtt-usage-settlement", "payment-reference-encryption", "fleet-read-token", "ota-bff-token", "job-authorization-token", "cloud-logger-ingest-token", "cloud-logger-billing-usage-token", "grafana-admin-password"}
	if env["SUPPORT_TICKETS_ENABLED"] == "true" {
		required = append(required, "zammad-postgres-password", "zammad-redis-password", "zammad-integration-token")
	}
	if environmentBillingTestSetting(env, "PAYMENT_SIMULATOR_ENABLED") == "true" {
		required = append(required, "payment-simulator-shared", "payment-simulator-callback", "payment-simulator-admin-token")
	}
	if environmentBillingTestSetting(env, "NEWEBPAY_ENABLED") == "true" {
		required = append(required, "newebpay-hash-key-seed", "newebpay-hash-iv-seed")
	}
	if env["PAYPAL_ENABLED"] == "true" {
		required = append(required, "paypal-client-id", "paypal-client-secret", "paypal-webhook-id")
	}
	flags, err := lkeBillingLifecycleFlags(env)
	if err != nil {
		return nil, bad("", "runtime lifecycle intent is invalid")
	}
	if flags.Backup {
		required = append(required, "cloud-logger-lifecycle-token", "cloud-logger-lifecycle-read-token", "billing-backup-access-key-id", "billing-backup-secret-access-key")
		if flags.Authority {
			required = append(required, "billing-raw-retention-authority-read")
		}
	}
	var missing []string
	for _, id := range required {
		if strings.TrimSpace(secrets[lkeSecretFileName(id)]) == "" {
			missing = append(missing, "runtime/"+id)
		}
	}
	for _, key := range []string{"GHCR_PULL_USERNAME", "GHCR_PULL_TOKEN"} {
		if strings.TrimSpace(operator[key]) == "" {
			missing = append(missing, "operator/env/"+key)
		}
	}
	for flag, prefix := range map[string]string{"OTA_SERVICE_REGISTRATION_ENABLED": "LINODE_OTA_OBJ_", "VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED": "LINODE_MEDIA_OBJ_"} {
		if env[flag] == "true" {
			for _, suffix := range []string{"ACCESS_KEY_ID", "SECRET_ACCESS_KEY"} {
				if strings.TrimSpace(operator[prefix+suffix]) == "" {
					missing = append(missing, "operator/env/"+prefix+suffix)
				}
			}
		}
	}
	if env["SDK_DOWNLOADS_ENABLED"] == "true" {
		for _, key := range []string{"SDK_ARTIFACT_BUCKET", "SDK_ARTIFACT_ENDPOINT"} {
			if strings.TrimSpace(env[key]) == "" {
				missing = append(missing, "operator/env/"+key)
			}
		}
		for _, key := range []string{"SDK_ARTIFACT_ACCESS_KEY_ID", "SDK_ARTIFACT_SECRET_ACCESS_KEY"} {
			if strings.TrimSpace(operator[key]) == "" {
				missing = append(missing, "operator/env/"+key)
			}
		}
	}
	if len(missing) != 0 {
		sort.Strings(missing)
		return nil, bad(strings.Join(missing, ","), "canonical inputs for the complete runtime dependency set are unavailable")
	}
	if err := lkeValidatePayPalRuntime(env); err != nil {
		return nil, bad("PAYPAL_ENABLED", err.Error())
	}
	manifests := []string{lkeBillingSecretManifest(env), lkeCloudAdminBillingSecretManifest(env), lkeCloudLoggerRuntimeSecretManifest(env), lkeGrafanaAdminSecretManifest(env), lkeCloudLoggerBillingInboxPVCManifest(env), lkeVideoCloudMQTTUsageCheckpointPVCManifest(env)}
	if env["SUPPORT_TICKETS_ENABLED"] == "true" {
		support, err := lkeSupportDependencySecretManifests(env)
		if err != nil {
			return nil, bad("", "canonical support dependency inputs are unavailable")
		}
		manifests = append(manifests, support...)
	}
	definitions, err := decodeEnvironmentCreateManifests(manifests...)
	if err != nil {
		return nil, err
	}
	if err := applyEnvironmentBillingRuntimeConfigurationForProfile(env, definitions, profile); err != nil {
		return nil, err
	}
	if env["SDK_DOWNLOADS_ENABLED"] == "true" {
		prefix := "pro2-examples/" + env["CLOUD_ENV_NAME"] + "/"
		if configured := env["PRO2_EXAMPLES_PREFIX"]; configured != "" && configured != prefix {
			return nil, bad("operator/env/PRO2_EXAMPLES_PREFIX", "SDK examples must use the selected environment prefix")
		}
		definitions = append(definitions, environmentCreateSecret(lkeNamespaceName(env, "frontend"), "frontend-sdk-downloads", map[string]string{
			"SDK_DOWNLOADS_ENABLED": "true", "SDK_ARTIFACT_BUCKET": env["SDK_ARTIFACT_BUCKET"], "SDK_ARTIFACT_ENDPOINT": env["SDK_ARTIFACT_ENDPOINT"],
			"SDK_ARTIFACT_REGION": firstNonEmpty(env["SDK_ARTIFACT_REGION"], "us-sea"), "SDK_LATEST_OBJECT_KEY": firstNonEmpty(env["SDK_LATEST_OBJECT_KEY"], "sdk/latest.json"),
			"SDK_ARTIFACT_ACCESS_KEY_ID": operator["SDK_ARTIFACT_ACCESS_KEY_ID"], "SDK_ARTIFACT_SECRET_ACCESS_KEY": operator["SDK_ARTIFACT_SECRET_ACCESS_KEY"], "PRO2_EXAMPLES_PREFIX": prefix,
		}))
	}
	for _, storage := range []struct{ flag, name, prefix string }{
		{"OTA_SERVICE_REGISTRATION_ENABLED", "ota-object-storage", "LINODE_OTA_OBJ_"},
		{"VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED", environmentVideoStorageObjectStorageSecret, "LINODE_MEDIA_OBJ_"},
	} {
		if env[storage.flag] != "true" {
			continue
		}
		mirror := environmentCreateSecret(lkeNamespaceName(env, "video-cloud"), storage.name, map[string]string{
			"AWS_ACCESS_KEY_ID":     operator[storage.prefix+"ACCESS_KEY_ID"],
			"AWS_SECRET_ACCESS_KEY": operator[storage.prefix+"SECRET_ACCESS_KEY"],
		})
		definitions = append(definitions, mirror)
	}
	for _, suffix := range []string{"admin", "billing", "frontend", "logger"} {
		object, err := newK8SDockerConfigSecretObject(env["CLOUD_STACK_NAME"]+"-"+suffix, lkeImagePullSecretName(env), operator["GHCR_PULL_USERNAME"], operator["GHCR_PULL_TOKEN"])
		if err != nil {
			return nil, bad("", "frozen image-pull credentials cannot be rendered")
		}
		definitions = append(definitions, deploymentplan.Definition{Object: object, Phase: "configuration"})
	}
	return definitions, nil
}

// Test facilities follow selected intent, never an unconditional staging mode.
func environmentBillingTestSetting(env map[string]string, key string) string {
	return firstNonEmpty(env[key], env["TEST_LAB_ENABLED"], "false")
}

func bindEnvironmentRuntimeDependencyOwners(definitions, produced []deploymentplan.Definition, live []deploymentplan.Object, recordedOwners map[string]string) ([]deploymentplan.Definition, error) {
	definitions = append([]deploymentplan.Definition(nil), definitions...)
	selected, current := map[string]bool{}, map[string]deploymentplan.Object{}
	for _, d := range produced {
		selected[deploymentplan.ID(d.Object)] = true
	}
	for _, object := range live {
		id := deploymentplan.ID(object)
		if current[id] != nil {
			return nil, &deploymentplan.Blocker{ID: "deployment.runtime.ownership", Resource: id, Cause: "runtime dependency inventory contains duplicate owners", Next: "repeat complete qualification with an unambiguous selected inventory"}
		}
		current[id] = object
	}
	for i := range definitions {
		d := &definitions[i]
		id := deploymentplan.ID(d.Object)
		if !selected[id] {
			continue
		}
		actual, expectedUID := current[id], recordedOwners[id]
		bad := func(cause string) ([]deploymentplan.Definition, error) {
			return nil, &deploymentplan.Blocker{ID: "deployment.runtime.ownership", Resource: id, Cause: cause, Next: "restore the recorded original dependency or qualify its exact owner and credential lifecycle; do not replace credentials or storage implicitly"}
		}
		if actual == nil {
			if expectedUID != "" {
				return bad("a previously recorded runtime dependency is absent")
			}
			continue // The ordinary reviewed plan may create this new mirror/allocation.
		}
		meta := deploymentplan.Map(actual["metadata"])
		if expectedUID == "" || meta["uid"] != expectedUID || meta["deletionTimestamp"] != nil {
			return bad("existing runtime dependency lacks its exact qualified original owner")
		}
		wanted := deploymentplan.Clone(d.Object)
		if wanted["kind"] == "Secret" {
			data := deploymentplan.Map(wanted["data"])
			if data == nil {
				data = deploymentplan.Object{}
			}
			for key, value := range deploymentplan.Map(wanted["stringData"]) {
				text, ok := value.(string)
				if !ok {
					return bad("canonical runtime dependency has invalid credential encoding")
				}
				data[key] = base64.StdEncoding.EncodeToString([]byte(text))
			}
			wanted["data"] = data
			delete(wanted, "stringData")
			if deploymentplan.Digest(data) != deploymentplan.Digest(deploymentplan.Map(actual["data"])) {
				return bad("existing runtime dependency has a different canonical credential key set or value")
			}
		}
		if !deploymentplan.Matches(wanted, actual) {
			return bad("existing runtime dependency differs from its canonical credentials or maintained allocation")
		}
		deploymentplan.Map(wanted["metadata"])["uid"] = expectedUID
		d.Object, d.Retain = wanted, true
	}
	return definitions, nil
}
