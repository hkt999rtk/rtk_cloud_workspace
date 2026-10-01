package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

// This update deliberately avoids the baseline Video Cloud deployment: the
// existing PKI-managed API and log ingester must retain their identities.
func runDeploymentOTAServiceRollout(args []string) error {
	return runDeploymentOTAServiceRolloutWithCredentials(args, func(environment string) (func(), error) {
		_, restore, err := configureProvisionSecretStore(environment)
		return restore, err
	})
}

func runDeploymentOTAServiceRolloutWithCredentials(args []string, credentials func(string) (func(), error)) error {
	fs := flag.NewFlagSet("deployment ota-service-rollout", flag.ContinueOnError)
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
	// The selected configuration supplies runtime values; the canonical
	// SecretStore supplies image pins and the validated object origin binding.
	env, err := selectedOTAEnvironment(cfg)
	if err != nil {
		return err
	}
	if env["CLOUD_STACK_NAME"] != cfg.Values["CLOUD_STACK_NAME"] || env["CLOUD_ENV_NAME"] != cfg.Environment {
		return errors.New("resolved OTA runtime does not match the selected deployment environment")
	}
	if err := loadLKEImageManifestDefaults(store.Root, env); err != nil {
		return err
	}
	if *confirm == "" {
		fmt.Fprintf(os.Stdout, "OTA service rollout plan: environment=%s stack=%s workload=%s (registration only)\n", cfg.Environment, env["CLOUD_STACK_NAME"], otaServiceWorkloadName)
		fmt.Fprintln(os.Stdout, "Apply requires the old OTA registrar stopped, strict Product entitlement, service identity, runtime Secrets, private registration listener, and --confirm STACK.")
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
	storageCredentials, err := bindSelectedOTAStorage(cfg, store, env)
	if err != nil {
		return err
	}
	if !lkeOTAServiceRegistrationEnabled(env) {
		return errors.New("LKE_OTA_SERVICE_REGISTRATION_ENABLED must be true in the selected environment")
	}
	if lkeOTARegistrarRegistrationEnabled(env) {
		return errors.New("LKE_OTA_REGISTRAR_REGISTRATION_ENABLED must be false before independent OTA rollout")
	}
	if lkeOTAServiceEdgeEnabled(env) || lkeOTACoreCutoverEnabled(env) {
		return errors.New("registration rollout requires OTA edge and core cutover flags disabled")
	}
	if image := lkeVideoCloudImage(env); !strings.Contains(image, "@sha256:") {
		return errors.New("OTA service rollout requires an immutable Video Cloud image digest")
	}
	var trustedKeys map[string]string
	if err := json.Unmarshal([]byte(env["VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON"]), &trustedKeys); err != nil || len(trustedKeys) == 0 {
		return errors.New("OTA service rollout requires at least one configured firmware manifest trust key")
	}
	if err := validateDeploymentOTATrustedManifestKeys(env["VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON"]); err != nil {
		return err
	}
	if err := lkeRequireExistingServiceRegistrationEndpoint(env); err != nil {
		return err
	}
	if err := lkeRequireOTAServiceInputs(env); err != nil {
		return err
	}
	if err := lkeRequireOTAServiceSelectedStorageCredentials(env, storageCredentials); err != nil {
		return err
	}
	if err := lkeRequireStoppedOTARegistrar(env); err != nil {
		return err
	}
	for _, manifest := range []string{
		lkeAllowServiceRegistrationNetworkPolicyManifest(env),
		lkeAllowAccountManagerHandoffBillingNetworkPolicyManifest(env),
		lkeAllowVideoCloudAPIOTAGatewayNetworkPolicyManifest(env),
		lkeAllowOTABillingNetworkPolicyManifest(env),
		lkeOTAServiceServiceManifest(env),
		lkeOTAServiceDeploymentManifest(env),
	} {
		if err := kubectlApply(manifest); err != nil {
			return err
		}
	}
	return runKubectl("-n", lkeNamespaceName(env, "video-cloud"), "rollout", "status", "deployment/"+otaServiceWorkloadName, "--timeout", "5m")
}

func lkeRequireOTAServiceSelectedStorageCredentials(env map[string]string, selected selectedOTAStorageCredentials) error {
	secret, err := kubectlResourceJSON(lkeNamespaceName(env, "video-cloud"), "secret", "video-cloud-runtime")
	if err != nil {
		return errors.New("OTA service runtime storage Secret is unavailable")
	}
	actualAccess, accessErr := kubernetesSecretBytes(secret, "AWS_ACCESS_KEY_ID")
	actualSecret, secretErr := kubernetesSecretBytes(secret, "AWS_SECRET_ACCESS_KEY")
	if accessErr != nil || secretErr != nil || strings.TrimSpace(string(actualAccess)) != selected.Access || strings.TrimSpace(string(actualSecret)) != selected.Secret {
		return errors.New("OTA service runtime storage credentials do not match the selected media grant")
	}
	return nil
}

func lkeRequireStoppedOTARegistrar(env map[string]string) error {
	body, err := kubectlCombinedOutput(nil, "-n", lkeNamespaceName(env, "video-cloud"), "get", "deployment", "video-cloud-otaregistrar", "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return fmt.Errorf("inspect existing OTA registrar: %w", err)
	}
	var state struct {
		Spec struct {
			Replicas *int `json:"replicas"`
		} `json:"spec"`
		Status struct {
			Replicas int `json:"replicas"`
			Ready    int `json:"readyReplicas"`
		} `json:"status"`
	}
	if len(strings.TrimSpace(string(body))) != 0 {
		if err := json.Unmarshal(body, &state); err != nil {
			return fmt.Errorf("decode existing OTA registrar: %w", err)
		}
		if state.Spec.Replicas == nil || *state.Spec.Replicas != 0 || state.Status.Replicas != 0 || state.Status.Ready != 0 {
			return errors.New("old OTA registrar must be scaled to zero and fully stopped before independent service rollout")
		}
	}
	pods, err := kubectlCombinedOutput(nil, "-n", lkeNamespaceName(env, "video-cloud"), "get", "pods", "-l", "app.kubernetes.io/name=video-cloud-otaregistrar", "-o", "name")
	if err != nil {
		return fmt.Errorf("inspect existing OTA registrar Pods: %w", err)
	}
	if strings.TrimSpace(string(pods)) != "" {
		return errors.New("old OTA registrar Pod is still terminating; wait before independent service rollout")
	}
	return nil
}
