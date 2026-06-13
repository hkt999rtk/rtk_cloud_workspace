package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type lkeNamespace struct {
	Key  string
	Name string
}

type lkeWorkload struct {
	Key       string
	Name      string
	EnvKey    string
	Image     string
	Namespace string
	Port      int
	Host      string
}

func runLKEProvision(paths provisionPaths, env map[string]string, opts provisionOptions) error {
	if opts.mode.reset {
		return errors.New("LKE provision reset is not implemented; use remove-all-vm for namespace teardown")
	}
	if opts.mode.preflight {
		if err := lkePreflight(env); err != nil {
			return err
		}
	}
	if opts.mode.plan {
		lkePlan(env)
	}
	if opts.mode.apply {
		if err := lkeApplyBase(env); err != nil {
			return err
		}
		if !opts.mode.deploy {
			fmt.Fprintln(os.Stderr, "[lke-provision] apply complete without deploy; service images were not installed")
		}
	}
	if opts.mode.dns {
		fmt.Fprintln(os.Stderr, "[lke-provision] dns step is delegated to Linode NodeBalancer, Ingress/Gateway, and cert-manager; no DNS records were mutated")
	}
	if opts.mode.deploy {
		if err := lkeDeployWorkloads(env, opts); err != nil {
			return err
		}
	}
	if opts.mode.artifacts {
		dir, err := writeLKEProvisionArtifacts(paths, env)
		if err != nil {
			return err
		}
		fmt.Fprintln(os.Stdout, dir)
	}
	if opts.mode.e2e {
		if err := lkeProvisionE2E(env, opts); err != nil {
			return err
		}
	}
	return nil
}

func lkePreflight(env map[string]string) error {
	if env["CLOUD_PROVIDER"] != "lke" {
		return fmt.Errorf("LKE provision requires CLOUD_PROVIDER=lke, got %s", env["CLOUD_PROVIDER"])
	}
	kubectl := lkeKubectl()
	if _, err := exec.LookPath(kubectl); err != nil && filepath.Base(kubectl) == kubectl {
		return fmt.Errorf("%s is required for LKE provision", kubectl)
	}
	if err := requireLKEKubeContext(); err != nil {
		return err
	}
	if err := runKubectl("version", "--client"); err != nil {
		return err
	}
	return nil
}

func lkePlan(env map[string]string) {
	fmt.Fprintln(os.Stdout, "LKE target:")
	fmt.Fprintf(os.Stdout, "- stack: %s\n", env["CLOUD_STACK_NAME"])
	fmt.Fprintf(os.Stdout, "- region: %s\n", env["CLOUD_REGION"])
	fmt.Fprintln(os.Stdout, "- namespaces:")
	for _, ns := range lkeNamespaces(env) {
		fmt.Fprintf(os.Stdout, "  - %s=%s\n", ns.Key, ns.Name)
	}
	fmt.Fprintln(os.Stdout, "- public HTTP: Linode NodeBalancer -> Ingress/Gateway -> video-cloud/account-manager/admin/frontend")
	fmt.Fprintln(os.Stdout, "- non-HTTP: MQTT/TURN require LoadBalancer/NodeBalancer or a TCP-capable ingress path")
	fmt.Fprintln(os.Stdout, "- secrets: OpenBao with Kubernetes auth / External Secrets; no root, unseal, HSM PIN, or signing key material in Kubernetes manifests")
	fmt.Fprintln(os.Stdout, "- deploy images:")
	for _, workload := range lkeWorkloads(env) {
		status := "TODO"
		if workload.Image != "" {
			status = workload.Image
		}
		fmt.Fprintf(os.Stdout, "  - %s: %s\n", workload.EnvKey, status)
	}
}

func lkeApplyBase(env map[string]string) error {
	for _, ns := range lkeNamespaces(env) {
		manifest := fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %s
  labels:
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
`, ns.Name, env["CLOUD_STACK_NAME"])
		if err := kubectlApply(manifest); err != nil {
			return err
		}
	}
	config := fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: rtk-cloud-stack
  namespace: %s
  labels:
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
data:
  CLOUD_ENV_NAME: %q
  CLOUD_PROVIDER: "lke"
  CLOUD_REGION: %q
  CLOUD_STACK_NAME: %q
  VIDEO_CLOUD_DOMAIN: %q
  VIDEO_CLOUD_CERTISSUER_DOMAIN: %q
  ACCOUNT_MANAGER_DOMAIN: %q
  CLOUD_ADMIN_DOMAIN: %q
  CLOUD_LOGGER_DOMAIN: %q
`, lkeNamespaceName(env, "platform"), env["CLOUD_STACK_NAME"], env["CLOUD_ENV_NAME"], env["CLOUD_REGION"], env["CLOUD_STACK_NAME"], env["VIDEO_CLOUD_DOMAIN"], env["VIDEO_CLOUD_CERTISSUER_DOMAIN"], env["ACCOUNT_MANAGER_DOMAIN"], env["CLOUD_ADMIN_DOMAIN"], env["CLOUD_LOGGER_DOMAIN"])
	return kubectlApply(config)
}

func lkeDeployWorkloads(env map[string]string, opts provisionOptions) error {
	if opts.loggerOnly {
		return errors.New("LKE logger-only deploy is not implemented; configure the Kubernetes log collection pipeline before enabling logger-only deploy")
	}
	workloads := lkeSelectedWorkloads(env, opts)
	missing := []string{}
	for _, workload := range workloads {
		if workload.Image == "" {
			missing = append(missing, workload.EnvKey)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("LKE deploy requires container image environment variables: %s", strings.Join(missing, ", "))
	}
	for _, workload := range workloads {
		if err := kubectlApply(lkeDeploymentManifest(env, workload)); err != nil {
			return err
		}
		if err := kubectlApply(lkeServiceManifest(env, workload)); err != nil {
			return err
		}
	}
	return nil
}

func lkeProvisionE2E(env map[string]string, opts provisionOptions) error {
	for _, workload := range lkeSelectedWorkloads(env, opts) {
		if workload.Image == "" {
			continue
		}
		if err := runKubectl("-n", workload.Namespace, "rollout", "status", "deployment/"+workload.Name, "--timeout", firstNonEmpty(os.Getenv("LKE_ROLLOUT_TIMEOUT"), "5m")); err != nil {
			return err
		}
	}
	return nil
}

func runRemoveAllLKE(envRoot string, env map[string]string, confirmed bool) error {
	stack := firstNonEmpty(env["CLOUD_STACK_NAME"], envFileValue(filepath.Join(envRoot, "env", "stack.env"), "CLOUD_STACK_NAME"), "video-cloud-staging")
	if !confirmed {
		fmt.Fprintf(os.Stderr, `Delete all LKE namespaces for stack "%s"? Type yes to continue: `, stack)
		var answer string
		_, _ = fmt.Fscan(os.Stdin, &answer)
		if answer != "yes" {
			fmt.Fprintln(os.Stderr, "[cloud-remove-all-vm] cancelled")
			return nil
		}
	}
	if err := requireLKEKubeContext(); err != nil {
		return err
	}
	args := []string{"delete", "namespace", "--ignore-not-found"}
	for _, ns := range lkeNamespaces(env) {
		args = append(args, ns.Name)
	}
	if err := runKubectl(args...); err != nil {
		return err
	}
	if err := backupAndRemoveState(envRoot); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[cloud-remove-all-vm] LKE namespace delete requests submitted for stack %s\n", stack)
	return nil
}

func writeLKEProvisionArtifacts(paths provisionPaths, env map[string]string) (string, error) {
	dir := filepath.Join(paths.ArtifactsDir, "lke-provision-"+time.Now().UTC().Format("20060102T150405Z"))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	namespaces := []string{}
	for _, ns := range lkeNamespaces(env) {
		namespaces = append(namespaces, ns.Name)
	}
	workloads := map[string]string{}
	for _, workload := range lkeWorkloads(env) {
		workloads[workload.Name] = workload.Image
	}
	body, err := json.MarshalIndent(map[string]any{
		"provider":   "lke",
		"stack":      env["CLOUD_STACK_NAME"],
		"region":     env["CLOUD_REGION"],
		"namespaces": namespaces,
		"domains": map[string]string{
			"video_cloud":     env["VIDEO_CLOUD_DOMAIN"],
			"certissuer":      env["VIDEO_CLOUD_CERTISSUER_DOMAIN"],
			"account_manager": env["ACCOUNT_MANAGER_DOMAIN"],
			"cloud_admin":     env["CLOUD_ADMIN_DOMAIN"],
			"cloud_logger":    env["CLOUD_LOGGER_DOMAIN"],
		},
		"workload_images": workloads,
	}, "", "  ")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, "summary.json"), append(body, '\n'), 0o644); err != nil {
		return "", err
	}
	return dir, nil
}

func lkeNamespaces(env map[string]string) []lkeNamespace {
	stack := lkeName(firstNonEmpty(env["CLOUD_STACK_NAME"], "video-cloud-staging"))
	values := []lkeNamespace{
		{Key: "platform", Name: firstNonEmpty(os.Getenv("LKE_NAMESPACE_PLATFORM"), stack+"-platform")},
		{Key: "video-cloud", Name: firstNonEmpty(os.Getenv("LKE_NAMESPACE_VIDEO_CLOUD"), stack+"-video-cloud")},
		{Key: "account-manager", Name: firstNonEmpty(os.Getenv("LKE_NAMESPACE_ACCOUNT_MANAGER"), stack+"-account-manager")},
		{Key: "admin", Name: firstNonEmpty(os.Getenv("LKE_NAMESPACE_ADMIN"), stack+"-admin")},
		{Key: "frontend", Name: firstNonEmpty(os.Getenv("LKE_NAMESPACE_FRONTEND"), stack+"-frontend")},
		{Key: "observability", Name: firstNonEmpty(os.Getenv("LKE_NAMESPACE_OBSERVABILITY"), stack+"-observability")},
		{Key: "secrets", Name: firstNonEmpty(os.Getenv("LKE_NAMESPACE_SECRETS"), stack+"-secrets")},
	}
	return values
}

func lkeNamespaceName(env map[string]string, key string) string {
	for _, ns := range lkeNamespaces(env) {
		if ns.Key == key {
			return ns.Name
		}
	}
	return lkeName(firstNonEmpty(env["CLOUD_STACK_NAME"], "video-cloud-staging")) + "-" + key
}

func lkeWorkloads(env map[string]string) []lkeWorkload {
	return []lkeWorkload{
		{Key: "video-cloud", Name: "video-cloud-api", EnvKey: "LKE_VIDEO_CLOUD_IMAGE", Image: os.Getenv("LKE_VIDEO_CLOUD_IMAGE"), Namespace: lkeNamespaceName(env, "video-cloud"), Port: envIntDefault("LKE_VIDEO_CLOUD_PORT", 8080), Host: env["VIDEO_CLOUD_DOMAIN"]},
		{Key: "account-manager", Name: "account-manager", EnvKey: "LKE_ACCOUNT_MANAGER_IMAGE", Image: os.Getenv("LKE_ACCOUNT_MANAGER_IMAGE"), Namespace: lkeNamespaceName(env, "account-manager"), Port: envIntDefault("LKE_ACCOUNT_MANAGER_PORT", 8080), Host: env["ACCOUNT_MANAGER_DOMAIN"]},
		{Key: "cloud-admin", Name: "cloud-admin", EnvKey: "LKE_CLOUD_ADMIN_IMAGE", Image: os.Getenv("LKE_CLOUD_ADMIN_IMAGE"), Namespace: lkeNamespaceName(env, "admin"), Port: envIntDefault("LKE_CLOUD_ADMIN_PORT", 8080), Host: env["CLOUD_ADMIN_DOMAIN"]},
		{Key: "frontend", Name: "frontend", EnvKey: "LKE_FRONTEND_IMAGE", Image: os.Getenv("LKE_FRONTEND_IMAGE"), Namespace: lkeNamespaceName(env, "frontend"), Port: envIntDefault("LKE_FRONTEND_PORT", 8080), Host: firstNonEmpty(os.Getenv("LKE_FRONTEND_DOMAIN"), env["CLOUD_ADMIN_DOMAIN"])},
	}
}

func lkeSelectedWorkloads(env map[string]string, opts provisionOptions) []lkeWorkload {
	workloads := lkeWorkloads(env)
	if !opts.videoOnly {
		return workloads
	}
	selected := []lkeWorkload{}
	for _, workload := range workloads {
		if workload.Key == "video-cloud" {
			selected = append(selected, workload)
		}
	}
	return selected
}

func lkeDeploymentManifest(env map[string]string, workload lkeWorkload) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/name: %s
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: %s
  template:
    metadata:
      labels:
        app.kubernetes.io/name: %s
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
      containers:
        - name: app
          image: %s
          imagePullPolicy: IfNotPresent
          ports:
            - name: http
              containerPort: %d
          env:
            - name: CLOUD_PROVIDER
              value: "lke"
            - name: CLOUD_STACK_NAME
              value: %q
            - name: SERVICE_PUBLIC_HOST
              value: %q
`, workload.Name, workload.Namespace, workload.Name, env["CLOUD_STACK_NAME"], workload.Name, workload.Name, env["CLOUD_STACK_NAME"], workload.Image, workload.Port, env["CLOUD_STACK_NAME"], workload.Host)
}

func lkeServiceManifest(env map[string]string, workload lkeWorkload) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/name: %s
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: %s
  ports:
    - name: http
      port: 80
      targetPort: %d
`, workload.Name, workload.Namespace, workload.Name, env["CLOUD_STACK_NAME"], workload.Name, workload.Port)
}

func kubectlApply(manifest string) error {
	cmd := exec.Command(lkeKubectl(), "apply", "-f", "-")
	cmd.Stdin = strings.NewReader(manifest)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runKubectl(args ...string) error {
	cmd := exec.Command(lkeKubectl(), args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func lkeKubectl() string {
	return firstNonEmpty(os.Getenv("RTK_CLOUD_KUBECTL"), "kubectl")
}

func requireLKEKubeContext() error {
	out, err := exec.Command(lkeKubectl(), "config", "current-context").CombinedOutput()
	context := strings.TrimSpace(string(out))
	if err != nil || context == "" {
		return fmt.Errorf("kubectl current-context is required for LKE operations; set KUBECONFIG or RTK_CLOUD_KUBECTL before running destructive staging commands")
	}
	fmt.Fprintf(os.Stderr, "[lke] kubectl context: %s\n", context)
	return nil
}

func lkeName(value string) string {
	value = strings.ToLower(value)
	var b bytes.Buffer
	lastDash := false
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}

func envIntDefault(key string, fallback int) int {
	if value := os.Getenv(key); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil && parsed > 0 {
			return parsed
		}
	}
	return fallback
}
