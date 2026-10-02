package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

func lkeOTAProducerSealJobManifest(env map[string]string, month, name string, maintenance bool) (string, error) {
	var cron map[string]any
	if err := yaml.Unmarshal([]byte(lkeOTAProducerSealCronJobManifest(env)), &cron); err != nil {
		return "", err
	}
	spec := cron["spec"].(map[string]any)["jobTemplate"].(map[string]any)["spec"].(map[string]any)
	spec["ttlSecondsAfterFinished"] = 604800
	pod := spec["template"].(map[string]any)["spec"].(map[string]any)
	container := pod["containers"].([]any)[0].(map[string]any)
	container["args"] = []string{"--all-brand-clouds", "--month", month, "--first-month", env["LKE_OTA_PRODUCER_SEAL_FIRST_MONTH"]}
	if maintenance {
		spec["activeDeadlineSeconds"] = 600
		container["args"] = []string{"--maintain-identity"}
		// Renewal needs its registry and private trust, not object credentials,
		// inventory bearer or Billing submission credentials.
		var settings []any
		for _, item := range container["env"].([]any) {
			setting := item.(map[string]any)
			key := setting["name"].(string)
			if key == "VIDEO_CLOUD_ENV" || key == "POSTGRES_PASSWORD" || key == "VIDEO_CLOUD_DB_DSN" ||
				(strings.HasPrefix(key, "VIDEO_CLOUD_ACCOUNT_MANAGER_") && key != "VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN") {
				settings = append(settings, setting)
			}
		}
		container["env"] = settings
	}
	metadata := map[string]any{"name": name, "namespace": lkeNamespaceName(env, "video-cloud"), "labels": map[string]string{"app.kubernetes.io/name": otaProducerSealOwner, "app.kubernetes.io/part-of": "rtk-cloud", "rtk.realtek.com/stack": env["CLOUD_STACK_NAME"]}}
	raw, err := yaml.Marshal(map[string]any{"apiVersion": "batch/v1", "kind": "Job", "metadata": metadata, "spec": spec})
	return string(raw), err
}

type otaProducerSealOps struct {
	credentials func(string) (func(), error)
	identity    func(map[string]string, bool) error
	ready       func(map[string]string) error
	apply       func(string) error
}

func runDeploymentOTAProducerSeal(args []string) error {
	return runDeploymentOTAProducerSealWithOps(args, otaProducerSealOps{
		credentials: func(environment string) (func(), error) {
			previous := activeCanonicalSecretStore
			_, restore, err := configureProvisionSecretStore(environment)
			if err != nil {
				return nil, err
			}
			activeCanonicalSecretStore = true
			return func() { activeCanonicalSecretStore = previous; restore() }, nil
		}, identity: lkeRequireOTAProducerSealIdentity, ready: lkeRequireReadyOTAServiceEndpoint, apply: kubectlApply,
	})
}

func runDeploymentOTAProducerSealWithOps(args []string, ops otaProducerSealOps) error {
	fs := flag.NewFlagSet("deployment ota-producer-seal", flag.ContinueOnError)
	environment := fs.String("environment", "", "selected environment")
	workspace := fs.String("workspace", "", "workspace root")
	month := fs.String("month", "", "completed qualified UTC month YYYY-MM")
	maintenance := fs.Bool("maintain-identity", false, "operator verifies/renews this Job's due identity without closing a period")
	confirm := fs.String("confirm", "", "selected stack for a one-time Job")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *environment == "" || (*maintenance && *month != "") {
		return errors.New("--environment and exactly one of --month or --maintain-identity are required")
	}
	if !*maintenance {
		if err := validateOTAProducerSealMonth(*month, time.Now().UTC()); err != nil {
			return err
		}
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, "")
	if err != nil {
		return err
	}
	if cfg.Adapter != "lke" {
		return errors.New("OTA producer Job requires the LKE adapter")
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	env, err := loggerPeriodSourceSelectedEnv(cfg, store)
	if err != nil {
		return err
	}
	if env["CLOUD_STACK_NAME"] != cfg.Values["CLOUD_STACK_NAME"] || env["CLOUD_ENV_NAME"] != cfg.Environment {
		return errors.New("OTA Job inputs differ from selected environment")
	}
	if err := loadLKEImageManifestDefaults(store.Root, env); err != nil {
		return err
	}
	if !*maintenance {
		first := env["LKE_OTA_PRODUCER_SEAL_FIRST_MONTH"]
		parsed, e := time.Parse("2006-01", first)
		if e != nil || parsed.Format("2006-01") != first || *month < first {
			return errors.New("OTA source month precedes the configured first qualified month, or that boundary is missing")
		}
	}
	operation := "source close"
	if *maintenance {
		operation = "identity maintenance"
	}
	if *confirm == "" {
		fmt.Fprintf(os.Stdout, "OTA producer %s plan: environment=%s stack=%s month=%s; one operator-launched Job, no invoice or recurring renewal schedule\n", operation, cfg.Environment, env["CLOUD_STACK_NAME"], *month)
		return nil
	}
	if *confirm != env["CLOUD_STACK_NAME"] {
		return errors.New("--confirm must match the selected stack")
	}
	restore, err := ops.credentials(cfg.Environment)
	if err != nil {
		return err
	}
	defer restore()
	bindings := map[string]string{}
	for key, value := range env {
		if strings.HasPrefix(key, "LKE_") || strings.HasPrefix(key, "VIDEO_CLOUD_") {
			bindings[key] = value
		}
	}
	for _, key := range []string{"LKE_OTA_SERVICE_REGISTRATION_ENABLED", "LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"} {
		bindings[key] = firstNonEmpty(env[key], "false")
	}
	restoreSelected := installAllCredentialEnvironment(bindings)
	defer restoreSelected()
	if !lkeOTAServiceRegistrationEnabled(env) || !lkeAccountManagerServiceRegistrationEnabled(env) {
		return errors.New("OTA monthly Job requires the registered OTA service and private Account Manager listener")
	}
	if !rolloutImagePattern.MatchString(lkeVideoCloudImage(env)) || !strings.Contains(lkeVideoCloudImage(env), "@sha256:") {
		return errors.New("OTA monthly Job requires the pinned environment CI image")
	}
	if err := ops.identity(env, *maintenance); err != nil {
		return err
	}
	if !*maintenance {
		token := lkeOTAProducerSealToken()
		if len(token) < 32 || strings.TrimSpace(token) != token || strings.ContainsAny(token, " \t\r\n") || token == lkeBillingServiceToken() || token == lkeBillingInternalToken() || token == lkeOTAPlatformSealToken() {
			return errors.New("OTA producer close requires a distinct canonical producer token")
		}
		if err := ops.ready(env); err != nil {
			return err
		}
	}
	name := "ota-producer-seal-" + strings.ReplaceAll(*month, "-", "") + "-" + time.Now().UTC().Format("20060102t150405")
	if *maintenance {
		name = "ota-producer-identity-" + time.Now().UTC().Format("20060102t150405")
	}
	manifest, err := lkeOTAProducerSealJobManifest(env, *month, name, *maintenance)
	if err != nil {
		return err
	}
	manifests := []string{lkeOTAProducerIdentityRBACManifest(env), lkeAllowServiceRegistrationNetworkPolicyManifest(env), lkeAllowAccountManagerCertIssuerNetworkPolicyManifest(env)}
	if !*maintenance {
		manifests = append(manifests, lkeOTAProducerSealRuntimeSecretManifest(env), lkeAllowOTABillingNetworkPolicyManifest(env))
	}
	manifests = append(manifests, manifest)
	for _, item := range manifests {
		if err := ops.apply(item); err != nil {
			return err
		}
	}
	fmt.Fprintf(os.Stdout, "OTA producer %s Job started: %s/%s; verify Complete and certificate/source evidence; no invoice issued\n", operation, lkeNamespaceName(env, "video-cloud"), name)
	return nil
}

func validateOTAProducerSealMonth(month string, now time.Time) error {
	start, err := time.Parse("2006-01", month)
	if err != nil || start.Format("2006-01") != month || now.UTC().Before(start.AddDate(0, 1, 0).Add(48*time.Hour)) {
		return errors.New("--month must be YYYY-MM and closed for at least 48 hours in UTC")
	}
	return nil
}
