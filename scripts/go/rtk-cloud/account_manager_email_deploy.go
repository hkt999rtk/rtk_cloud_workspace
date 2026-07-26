package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

var accountManagerEmailSecretKeys = []string{
	"AUTH_TOKEN_DELIVERY",
	"AUTH_TOKEN_BASE_URL",
	"SMTP_HOST",
	"SMTP_PORT",
	"SMTP_USERNAME",
	"SMTP_PASSWORD",
	"SMTP_FROM",
	"SMTP_FROM_NAME",
	"SMTP_ENCRYPTION",
	"EMAIL_OUTBOX_ENCRYPTION_KEY",
	"EMAIL_OUTBOX_POLL_INTERVAL",
	"EMAIL_OUTBOX_BATCH_SIZE",
	"EMAIL_OUTBOX_MAX_ATTEMPTS",
	"EMAIL_OUTBOX_RETRY_BASE",
	"EMAIL_OUTBOX_RETRY_MAX",
}

// runAccountManagerEmailDeploy is deliberately narrower than provision
// --deploy. It updates an existing Account Manager installation without
// reconciling OpenBao, Postgres, node pools, DNS, or other shared workloads.
func runAccountManagerEmailDeploy(args []string) error {
	fs := flag.NewFlagSet("account-manager-email-deploy", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace root")
	envRootFlag := fs.String("env-root", "", "normalized environment root")
	confirm := fs.String("confirm", "", "exact stack name confirmation")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig for the existing staging cluster")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*envRootFlag) == "" {
		return errors.New("--env-root is required")
	}
	workspace := strings.TrimSpace(*workspaceFlag)
	if workspace == "" {
		var err error
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	root, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	loaded, err := envroot.Load(root, "")
	if err != nil {
		return err
	}
	env := loaded.Values
	stack := strings.TrimSpace(env["CLOUD_STACK_NAME"])
	if stack == "" || strings.TrimSpace(*confirm) != stack {
		return fmt.Errorf("--confirm must equal CLOUD_STACK_NAME %q", stack)
	}
	if strings.TrimSpace(*kubeconfig) != "" {
		if err := os.Setenv("RTK_CLOUD_KUBECONFIG", strings.TrimSpace(*kubeconfig)); err != nil {
			return err
		}
	}
	if err := validateAccountManagerEmailDeployEnv(env); err != nil {
		return err
	}
	if err := waitForKubernetesAPIReady(); err != nil {
		return err
	}
	return deployExistingAccountManagerEmail(env)
}

func validateAccountManagerEmailDeployEnv(env map[string]string) error {
	required := []string{
		"LKE_ACCOUNT_MANAGER_IMAGE", "AUTH_TOKEN_BASE_URL", "SMTP_HOST",
		"SMTP_PORT", "SMTP_USERNAME", "SMTP_PASSWORD", "SMTP_FROM",
	}
	for _, key := range required {
		if strings.TrimSpace(lkeEnvValue(env, key)) == "" {
			return fmt.Errorf("%s is required for Account Manager email deploy", key)
		}
	}
	if strings.ToLower(strings.TrimSpace(firstNonEmpty(os.Getenv("AUTH_TOKEN_DELIVERY"), env["AUTH_TOKEN_DELIVERY"]))) != "smtp" {
		return errors.New("AUTH_TOKEN_DELIVERY must be smtp")
	}
	baseURL, err := url.Parse(strings.TrimSpace(firstNonEmpty(os.Getenv("AUTH_TOKEN_BASE_URL"), env["AUTH_TOKEN_BASE_URL"])))
	if err != nil || baseURL.Scheme != "https" || baseURL.Host == "" || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return errors.New("AUTH_TOKEN_BASE_URL must be a credential-free HTTPS origin")
	}
	if baseURL.Path != "" && baseURL.Path != "/" {
		return errors.New("AUTH_TOKEN_BASE_URL must not contain a path")
	}
	return nil
}

func deployExistingAccountManagerEmail(env map[string]string) error {
	namespace := lkeNamespaceName(env, "account-manager")
	for _, resource := range []string{
		"deployment/account-manager",
		"secret/account-manager-runtime",
		"secret/account-manager-certissuer-client",
	} {
		if _, err := kubectlCombinedOutput(nil, "-n", namespace, "get", resource, "-o", "name"); err != nil {
			return fmt.Errorf("existing Account Manager prerequisite %s is unavailable: %w", resource, err)
		}
	}

	secret, err := kubectlResourceJSON(namespace, "secret", "account-manager-runtime")
	if err != nil {
		return err
	}
	checksum, err := mergeAccountManagerEmailSecret(secret, env)
	if err != nil {
		return err
	}
	if err := kubectlReplaceJSON(secret); err != nil {
		return fmt.Errorf("replace Account Manager runtime secret: %w", err)
	}

	_ = runKubectl("-n", namespace, "delete", "job/account-manager-migrate", "--ignore-not-found=true")
	if err := kubectlApply(lkeAccountManagerMigrationJobManifest(env)); err != nil {
		return err
	}
	if err := runKubectl("-n", namespace, "wait", "--for=condition=complete", "job/account-manager-migrate", "--timeout", firstNonEmpty(os.Getenv("LKE_MIGRATION_JOB_TIMEOUT"), "5m")); err != nil {
		return err
	}

	deployment, err := kubectlResourceJSON(namespace, "deployment", "account-manager")
	if err != nil {
		return err
	}
	if err := updateAccountManagerDeployment(deployment, lkeEnvValue(env, "LKE_ACCOUNT_MANAGER_IMAGE"), checksum); err != nil {
		return err
	}
	if err := kubectlReplaceJSON(deployment); err != nil {
		return fmt.Errorf("replace Account Manager deployment: %w", err)
	}
	if err := kubectlApply(lkeAccountManagerEmailWorkerManifestWithChecksum(env, checksum)); err != nil {
		return err
	}
	timeout := firstNonEmpty(os.Getenv("LKE_WORKLOAD_ROLLOUT_TIMEOUT"), "10m")
	for _, name := range []string{"account-manager", "account-manager-email-worker"} {
		if err := runKubectl("-n", namespace, "rollout", "status", "deployment/"+name, "--timeout", timeout); err != nil {
			return err
		}
	}
	return nil
}

func kubectlResourceJSON(namespace, kind, name string) (map[string]any, error) {
	body, err := kubectlCombinedOutput(nil, "-n", namespace, "get", kind, name, "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("get %s/%s: %w", kind, name, err)
	}
	var resource map[string]any
	if err := json.Unmarshal(body, &resource); err != nil {
		return nil, fmt.Errorf("decode %s/%s: %w", kind, name, err)
	}
	return resource, nil
}

func kubectlReplaceJSON(resource map[string]any) error {
	body, err := json.Marshal(resource)
	if err != nil {
		return err
	}
	out, err := kubectlCombinedOutput(bytes.NewReader(body), "replace", "-f", "-")
	if err != nil {
		return fmt.Errorf("kubectl replace failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func mergeAccountManagerEmailSecret(secret map[string]any, env map[string]string) (string, error) {
	raw, ok := secret["data"].(map[string]any)
	if !ok {
		return "", errors.New("account-manager-runtime Secret has no data")
	}
	if strings.TrimSpace(lkeEnvValue(env, "EMAIL_OUTBOX_ENCRYPTION_KEY")) == "" {
		if encoded, ok := raw["EMAIL_OUTBOX_ENCRYPTION_KEY"].(string); ok && encoded != "" {
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				return "", errors.New("existing EMAIL_OUTBOX_ENCRYPTION_KEY is invalid")
			}
			env["EMAIL_OUTBOX_ENCRYPTION_KEY"] = string(decoded)
		} else {
			var key [32]byte
			if _, err := rand.Read(key[:]); err != nil {
				return "", fmt.Errorf("generate EMAIL_OUTBOX_ENCRYPTION_KEY: %w", err)
			}
			env["EMAIL_OUTBOX_ENCRYPTION_KEY"] = base64.StdEncoding.EncodeToString(key[:])
		}
	}
	defaults := map[string]string{
		"SMTP_FROM_NAME":             "Realtek Connect",
		"SMTP_ENCRYPTION":            "starttls",
		"EMAIL_OUTBOX_POLL_INTERVAL": "5s",
		"EMAIL_OUTBOX_BATCH_SIZE":    "20",
		"EMAIL_OUTBOX_MAX_ATTEMPTS":  "8",
		"EMAIL_OUTBOX_RETRY_BASE":    "30s",
		"EMAIL_OUTBOX_RETRY_MAX":     "30m",
	}
	values := make([]string, 0, len(accountManagerEmailSecretKeys)*2)
	for _, key := range accountManagerEmailSecretKeys {
		value := strings.TrimSpace(firstNonEmpty(os.Getenv(key), env[key], defaults[key]))
		raw[key] = base64.StdEncoding.EncodeToString([]byte(value))
		values = append(values, key, value)
	}
	return lkeConfigChecksum(values...), nil
}

func updateAccountManagerDeployment(deployment map[string]any, image, checksum string) error {
	spec, ok := deployment["spec"].(map[string]any)
	if !ok {
		return errors.New("account-manager Deployment has no spec")
	}
	template, ok := spec["template"].(map[string]any)
	if !ok {
		return errors.New("account-manager Deployment has no pod template")
	}
	metadata, _ := template["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
		template["metadata"] = metadata
	}
	annotations, _ := metadata["annotations"].(map[string]any)
	if annotations == nil {
		annotations = map[string]any{}
		metadata["annotations"] = annotations
	}
	annotations["rtk.realtek.com/runtime-checksum"] = checksum
	podSpec, ok := template["spec"].(map[string]any)
	if !ok {
		return errors.New("account-manager Deployment has no pod spec")
	}
	containers, ok := podSpec["containers"].([]any)
	if !ok {
		return errors.New("account-manager Deployment has no containers")
	}
	for _, item := range containers {
		container, _ := item.(map[string]any)
		if container["name"] == "app" {
			container["image"] = image
			return nil
		}
	}
	return errors.New("account-manager Deployment has no app container")
}
