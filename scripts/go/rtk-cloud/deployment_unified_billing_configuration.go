package main

import (
	"crypto/sha256"
	"encoding/base64"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const (
	environmentBillingRuntimeLegacyProfile = "billing-runtime-v1"
	environmentBillingRuntimeKeyProfile    = "billing-payment-reference-sha256-v2"
)

// Only the historical randomSecret seed shape has a deterministic legacy
// projection. Valid native AES-256 keys keep their exact representation.
func environmentBillingPaymentReferenceKey(value, profile string) (string, error) {
	if profile == "" || profile == environmentBillingRuntimeLegacyProfile {
		return value, nil
	}
	bad := func() (string, error) {
		return "", &deploymentplan.Blocker{ID: "deployment.configuration.billing", Resource: "runtime/payment-reference-encryption", Cause: "Billing payment-reference input is neither a native AES-256 key nor the exact historical opaque seed format", Next: "preserve the original material and qualify its existing-data/key provenance before a versioned correction"}
	}
	if profile != environmentBillingRuntimeKeyProfile {
		return bad()
	}
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil && len(decoded) == 32 {
		return value, nil
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	if err != nil || len(decoded) != 24 || base64.RawURLEncoding.EncodeToString(decoded) != value {
		return bad()
	}
	hash := sha256.Sum256([]byte(value))
	return base64.StdEncoding.EncodeToString(hash[:]), nil
}

// Mirrored payment keys use the same deterministic projection as the maintained
// producer; canonical seed bytes remain the authority and are never rewritten.
func environmentRuntimeBindingValue(id, value string) (string, error) {
	if id == "payment-reference-encryption" {
		return environmentBillingPaymentReferenceKey(value, environmentBillingRuntimeKeyProfile)
	}
	return value, nil
}

// These public settings are selected from maintained intent/operator state;
// raw compatibility helpers must never fall back to unrelated process inputs.
func environmentBillingRenderKeys() []string {
	return []string{"PAYMENT_SIMULATOR_ENABLED", "NEWEBPAY_ENABLED", "PAYPAL_ENABLED", "PAYPAL_ENVIRONMENT", "PAYMENT_SIMULATOR_RUN_ID", "PAYMENT_SIMULATOR_SCENARIO", "PAYMENT_SIMULATOR_DOMAIN", "BILLING_DOMAIN", "NEWEBPAY_MERCHANT_ID"}
}

func environmentBillingPaymentWorkerEnabled(env map[string]string) bool {
	return environmentBillingTestSetting(env, "PAYMENT_SIMULATOR_ENABLED") == "true" || environmentBillingTestSetting(env, "NEWEBPAY_ENABLED") == "true" || lkePayPalEnabled(env)
}

// Use the same provider intent for the API, worker and simulator. Their raw
// historical recipes remain unchanged; only the maintained target is projected.
func applyEnvironmentBillingRuntimeConfiguration(env map[string]string, definitions []deploymentplan.Definition) error {
	return applyEnvironmentBillingRuntimeConfigurationForProfile(env, definitions, environmentBillingRuntimeKeyProfile)
}

func applyEnvironmentBillingRuntimeConfigurationForProfile(env map[string]string, definitions []deploymentplan.Definition, profile string) error {
	secret, err := decodeEnvironmentCreateManifests(lkeBillingSecretManifest(env))
	if err != nil || len(secret) != 1 {
		return &deploymentplan.Blocker{ID: "deployment.configuration.billing", Cause: "selected Billing runtime configuration cannot be rendered", Next: "restore the selected Billing provider inputs"}
	}
	fields := deploymentplan.Map(secret[0].Object["stringData"])
	key, err := environmentBillingPaymentReferenceKey(deploymentplan.String(fields["PAYMENT_REFERENCE_ENCRYPTION_KEY"]), profile)
	if err != nil {
		return err
	}
	fields["PAYMENT_REFERENCE_ENCRYPTION_KEY"] = key
	worker := environmentBillingPaymentWorkerEnabled(env)
	fields["PAYMENT_WORKER_ENABLED"] = "false"
	if worker {
		fields["PAYMENT_WORKER_ENABLED"] = "true"
	}
	hash := deploymentplan.Digest(fields)
	data := deploymentplan.Object{}
	for key, value := range fields {
		data[key] = base64.StdEncoding.EncodeToString([]byte(deploymentplan.String(value)))
	}
	ns := lkeNamespaceName(env, "billing")
	seen := map[string]bool{}
	for _, definition := range definitions {
		o := definition.Object
		meta := deploymentplan.Map(o["metadata"])
		if meta["namespace"] != ns {
			continue
		}
		name := deploymentplan.String(meta["name"])
		if o["kind"] == "Secret" && name == "billing-runtime" {
			if seen["Secret/"+name] {
				return environmentCreateBlocker("deployment.configuration.billing", "Billing runtime owner is ambiguous")
			}
			seen["Secret/"+name] = true
			o["data"] = deploymentplan.Clone(data)
			delete(o, "stringData")
			continue
		}
		if o["kind"] != "Deployment" || name != "billing" && name != "billing-payment-worker" && name != "payment-simulator" {
			continue
		}
		if seen[name] {
			return environmentCreateBlocker("deployment.configuration.billing", "Billing runtime consumer is ambiguous")
		}
		seen[name] = true
		template := deploymentplan.Map(deploymentplan.Map(o["spec"])["template"])
		annotations := deploymentplan.Map(deploymentplan.Map(template["metadata"])["annotations"])
		if annotations == nil {
			annotations = deploymentplan.Object{}
			deploymentplan.Map(template["metadata"])["annotations"] = annotations
		}
		annotations["rtk.realtek.com/runtime-checksum"] = hash
		if name == "billing-payment-worker" {
			replicas := float64(0)
			if worker {
				replicas = 1
			}
			deploymentplan.Map(o["spec"])["replicas"] = replicas
		}
		if name == "payment-simulator" {
			replicas := float64(0)
			if environmentBillingTestSetting(env, "PAYMENT_SIMULATOR_ENABLED") == "true" {
				replicas = 1
			}
			deploymentplan.Map(o["spec"])["replicas"] = replicas
		}
	}
	return nil
}

// A finite correction to frozen desired definitions. It does not select native
// bodies, replace credentials, mutate old plans or alter non-Billing resources.
func environmentBillingRuntimeCorrectionDefinitions(cfg deploymentConfig, store secretStore, release environmentRelease, snapshot environmentDeploymentSnapshot, original []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	return environmentBillingRuntimeCorrectionDefinitionsForProfile(cfg, store, release, snapshot, original, environmentBillingRuntimeKeyProfile)
}

func environmentBillingRuntimeCorrectionDefinitionsForProfile(cfg deploymentConfig, store secretStore, release environmentRelease, snapshot environmentDeploymentSnapshot, original []deploymentplan.Definition, profile string) ([]deploymentplan.Definition, error) {
	selected := map[string]bool{"Secret/" + cfg.Values["CLOUD_STACK_NAME"] + "-billing/billing-runtime": true, "Deployment/" + cfg.Values["CLOUD_STACK_NAME"] + "-billing/billing": true, "Deployment/" + cfg.Values["CLOUD_STACK_NAME"] + "-billing/billing-payment-worker": true, "Deployment/" + cfg.Values["CLOUD_STACK_NAME"] + "-billing/payment-simulator": true}
	var result []deploymentplan.Definition
	for _, d := range original {
		if selected[deploymentplan.ID(d.Object)] {
			d.Object = deploymentplan.Clone(d.Object)
			d.Dependencies = append([]string(nil), d.Dependencies...)
			result = append(result, d)
		}
	}
	err := withEnvironmentRenderValues(store, release, snapshot, environmentConfigurationRenderValues(cfg), func(env map[string]string) error {
		// Reuse the complete producer's credential requirements before publishing
		// any corrected object; enabled external providers cannot acquire fake keys.
		if _, err := renderEnvironmentRuntimeDependencyDefinitionsForBillingProfile(env, snapshot.Secrets, snapshot.Operator, profile); err != nil {
			return err
		}
		return applyEnvironmentBillingRuntimeConfigurationForProfile(env, result, profile)
	})
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, d := range result {
		id := deploymentplan.ID(d.Object)
		if seen[id] {
			return nil, environmentCreateBlocker("deployment.configuration.billing", "Billing correction has duplicate original owner")
		}
		seen[id] = true
	}
	for id := range selected {
		if strings.HasSuffix(id, "/payment-simulator") {
			continue
		}
		if !seen[id] {
			return nil, environmentCreateBlocker("deployment.configuration.billing", "Billing correction lacks its exact original runtime consumer")
		}
	}
	return result, nil
}
