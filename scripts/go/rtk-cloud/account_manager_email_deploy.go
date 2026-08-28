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
	"path/filepath"
	"strconv"
	"strings"
	"time"

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
	"SENDMAIL_HTTP_BASE_URL",
	"SENDMAIL_HTTP_BEARER_TOKEN",
	"SENDMAIL_HTTP_TIMEOUT",
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
	if strings.TrimSpace(lkeEnvValue(env, "LKE_ACCOUNT_MANAGER_IMAGE")) == "" {
		return errors.New("LKE_ACCOUNT_MANAGER_IMAGE is required for Account Manager email deploy")
	}
	return validateAccountManagerEmailDeliveryEnv(env)
}

func validateAccountManagerEmailDeliveryEnv(env map[string]string) error {
	if strings.TrimSpace(lkeEnvValue(env, "AUTH_TOKEN_BASE_URL")) == "" {
		return errors.New("AUTH_TOKEN_BASE_URL is required for Account Manager email delivery")
	}
	delivery := strings.ToLower(strings.TrimSpace(firstNonEmpty(os.Getenv("AUTH_TOKEN_DELIVERY"), env["AUTH_TOKEN_DELIVERY"])))
	baseURL, err := url.Parse(strings.TrimSpace(firstNonEmpty(os.Getenv("AUTH_TOKEN_BASE_URL"), env["AUTH_TOKEN_BASE_URL"])))
	if err != nil || baseURL.Scheme != "https" || baseURL.Host == "" || baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return errors.New("AUTH_TOKEN_BASE_URL must be a credential-free HTTPS origin")
	}
	if baseURL.Path != "" && baseURL.Path != "/" {
		return errors.New("AUTH_TOKEN_BASE_URL must not contain a path")
	}
	switch delivery {
	case "smtp":
		for _, key := range []string{"SMTP_HOST", "SMTP_PORT", "SMTP_USERNAME", "SMTP_PASSWORD", "SMTP_FROM"} {
			if strings.TrimSpace(lkeEnvValue(env, key)) == "" {
				return fmt.Errorf("%s is required for SMTP delivery", key)
			}
		}
	case "sendmail_http":
		if err := validateSendMailHTTPDeployEnv(env); err != nil {
			return err
		}
	default:
		return errors.New("AUTH_TOKEN_DELIVERY must be smtp or sendmail_http")
	}
	if delivery != "smtp" {
		return nil
	}
	port, err := strconv.Atoi(strings.TrimSpace(lkeEnvValue(env, "SMTP_PORT")))
	if err != nil || port < 1 || port > 65535 {
		return errors.New("SMTP_PORT must be a valid TCP port")
	}
	return nil
}

func validateStagingEmailDeliveryBeforeReset(envRoot string) error {
	stackEnv := filepath.Join(envRoot, "env", "stack.env")
	env := make(map[string]string, len(accountManagerEmailSecretKeys))
	for _, key := range accountManagerEmailSecretKeys {
		env[key] = envroot.FileVar(stackEnv, key)
	}
	if delivery := strings.ToLower(strings.TrimSpace(lkeEnvValue(env, "AUTH_TOKEN_DELIVERY"))); delivery != "sendmail_http" {
		return errors.New("staging reset blocked before deleting workloads: AUTH_TOKEN_DELIVERY must be sendmail_http")
	}
	if err := validateAccountManagerEmailDeliveryEnv(env); err != nil {
		return fmt.Errorf("staging reset blocked before deleting workloads: configure Account Manager email delivery: %w", err)
	}
	return nil
}

func validateSendMailHTTPDeployEnv(env map[string]string) error {
	raw := strings.TrimSpace(lkeEnvValue(env, "SENDMAIL_HTTP_BASE_URL"))
	baseURL, err := url.Parse(raw)
	if err != nil || baseURL.Scheme != "https" || baseURL.Host == "" ||
		baseURL.User != nil || baseURL.RawQuery != "" || baseURL.Fragment != "" {
		return errors.New("SENDMAIL_HTTP_BASE_URL must be a credential-free HTTPS origin")
	}
	if baseURL.Path != "" && baseURL.Path != "/" {
		return errors.New("SENDMAIL_HTTP_BASE_URL must not contain a path")
	}
	const expectedHost = "sm.realtekconnect.com"
	if !strings.EqualFold(baseURL.Hostname(), expectedHost) || baseURL.Port() != "" {
		return fmt.Errorf("SENDMAIL_HTTP_BASE_URL host must equal %s with the default HTTPS port", expectedHost)
	}
	if strings.TrimSpace(lkeEnvValue(env, "SENDMAIL_HTTP_BEARER_TOKEN")) == "" {
		return errors.New("SENDMAIL_HTTP_BEARER_TOKEN is required")
	}
	if timeout := strings.TrimSpace(lkeEnvValue(env, "SENDMAIL_HTTP_TIMEOUT")); timeout != "" {
		value, err := time.ParseDuration(timeout)
		if err != nil || value <= 0 {
			return errors.New("SENDMAIL_HTTP_TIMEOUT must be a positive duration")
		}
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
	if strings.EqualFold(strings.TrimSpace(lkeEnvValue(env, "AUTH_TOKEN_DELIVERY")), "smtp") {
		if err := checkAccountManagerSMTPSubmissionEgress(env); err != nil {
			return err
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

func checkAccountManagerSMTPSubmissionEgress(env map[string]string) error {
	namespace := lkeNamespaceName(env, "account-manager")
	name := "account-manager-smtp-egress-check"
	_ = runKubectl("-n", namespace, "delete", "pod/"+name, "--ignore-not-found=true", "--wait=true")
	if err := kubectlApply(accountManagerSMTPEgressCheckPodManifest(env, name)); err != nil {
		return err
	}
	defer func() {
		_ = runKubectl("-n", namespace, "delete", "pod/"+name, "--ignore-not-found=true", "--wait=true")
	}()
	if err := runKubectl("-n", namespace, "wait", "--for=condition=Ready", "pod/"+name, "--timeout", firstNonEmpty(os.Getenv("LKE_SMTP_EGRESS_CHECK_READY_TIMEOUT"), "90s")); err != nil {
		return errors.New("SMTP egress preflight pod did not become ready")
	}
	out, err := kubectlCombinedOutput(nil,
		"-n", namespace,
		"exec", "pod/"+name, "--",
		"sh", "-c", `nc -z -w 10 "$SMTP_HOST" "$SMTP_PORT"`,
	)
	if err != nil {
		return fmt.Errorf(
			"LKE outbound SMTP submission is blocked on configured port %s; verify Akamai SMTP restrictions before retrying",
			strings.TrimSpace(lkeEnvValue(env, "SMTP_PORT")),
		)
	}
	if len(bytes.TrimSpace(out)) != 0 {
		return errors.New("SMTP egress preflight returned unexpected output")
	}
	return nil
}

func accountManagerSMTPEgressCheckPodManifest(env map[string]string, name string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Pod
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/name: account-manager-smtp-egress-check
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  restartPolicy: Never
  terminationGracePeriodSeconds: 0
  containers:
    - name: check
      image: %s
      imagePullPolicy: IfNotPresent
      command: ["sh", "-c", "sleep 3600"]
      env:
        - name: SMTP_HOST
          value: %q
        - name: SMTP_PORT
          value: %q
      resources:
        requests:
          cpu: 5m
          memory: 8Mi
        limits:
          cpu: 25m
          memory: 32Mi
`, name, lkeNamespaceName(env, "account-manager"), env["CLOUD_STACK_NAME"], firstNonEmpty(os.Getenv("LKE_SMTP_EGRESS_CHECK_IMAGE"), "busybox:1.36"), lkeEnvValue(env, "SMTP_HOST"), lkeEnvValue(env, "SMTP_PORT"))
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
		"SENDMAIL_HTTP_TIMEOUT":      "15s",
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
