package main

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net"
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

type lkeVideoCloudAuxiliaryService struct {
	Name        string
	Binary      string
	Port        int
	PortName    string
	MetricsPath string
}

var errLKEMissingCluster = errors.New("no matching LKE cluster found")
var lkeRuntimeSecretCache = map[string]string{}

func runLKEProvision(paths provisionPaths, env map[string]string, opts provisionOptions) error {
	if opts.mode.reset {
		return errors.New("LKE provision reset is not implemented; use remove-all-vm for namespace teardown")
	}
	if opts.mode.apply || opts.mode.deploy || opts.mode.artifacts || opts.mode.e2e {
		if err := writeLKECompatibilityArtifacts(paths, env); err != nil {
			return err
		}
	}
	if opts.mode.preflight {
		if err := lkePreflight(paths, env); err != nil {
			return err
		}
	}
	if opts.mode.plan {
		lkePlan(env)
	}
	if opts.mode.deploy {
		if err := ensureLKEDeployImages(env, opts); err != nil {
			return err
		}
	}
	if opts.mode.apply || opts.mode.deploy || opts.mode.e2e {
		if err := ensureLKEKubeAccess(paths, env, opts.mode.apply); err != nil {
			return err
		}
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
		if err := lkeDeployWorkloads(paths, env, opts); err != nil {
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

func lkePreflight(paths provisionPaths, env map[string]string) error {
	if env["CLOUD_PROVIDER"] != "lke" {
		return fmt.Errorf("LKE provision requires CLOUD_PROVIDER=lke, got %s", env["CLOUD_PROVIDER"])
	}
	kubectl := lkeKubectl()
	if _, err := exec.LookPath(kubectl); err != nil && filepath.Base(kubectl) == kubectl {
		return fmt.Errorf("%s is required for LKE provision", kubectl)
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

func lkeDeployWorkloads(paths provisionPaths, env map[string]string, opts provisionOptions) error {
	if opts.loggerOnly {
		return errors.New("LKE logger-only deploy is not implemented; configure the Kubernetes log collection pipeline before enabling logger-only deploy")
	}
	if err := ensureLKEDeployImages(env, opts); err != nil {
		return err
	}
	if err := lkeApplyRuntimeDependencies(paths, env, opts); err != nil {
		return err
	}
	for _, workload := range lkeSelectedWorkloads(env, opts) {
		if err := kubectlApply(lkeDeploymentManifest(env, workload)); err != nil {
			return err
		}
		if err := kubectlApply(lkeServiceManifest(env, workload)); err != nil {
			return err
		}
	}
	return nil
}

func ensureLKEDeployImages(env map[string]string, opts provisionOptions) error {
	missing := lkeMissingImageWorkloads(env, opts)
	if len(missing) == 0 {
		return nil
	}
	registry := strings.TrimRight(os.Getenv("LKE_IMAGE_REGISTRY"), "/")
	if registry == "" {
		return validateLKEDeployInputs(env, opts)
	}
	tag := firstNonEmpty(os.Getenv("LKE_IMAGE_TAG"), lkeName(firstNonEmpty(env["CLOUD_STACK_NAME"], "video-cloud-staging")))
	for _, workload := range missing {
		image := registry + "/" + workload.Name + ":" + tag
		if err := buildLKEImage(workload, image); err != nil {
			return err
		}
		_ = os.Setenv(workload.EnvKey, image)
	}
	return nil
}

func validateLKEDeployInputs(env map[string]string, opts provisionOptions) error {
	missingWorkloads := lkeMissingImageWorkloads(env, opts)
	missing := []string{}
	for _, workload := range missingWorkloads {
		missing = append(missing, workload.EnvKey)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("LKE deploy requires container image environment variables or LKE_IMAGE_REGISTRY for auto build/push: %s", strings.Join(missing, ", "))
	}
	return nil
}

func lkeMissingImageWorkloads(env map[string]string, opts provisionOptions) []lkeWorkload {
	missing := []lkeWorkload{}
	for _, workload := range lkeSelectedWorkloads(env, opts) {
		if firstNonEmpty(os.Getenv(workload.EnvKey), workload.Image) == "" {
			missing = append(missing, workload)
		}
	}
	return missing
}

func buildLKEImage(workload lkeWorkload, image string) error {
	contextDir, dockerfile, cleanup, err := lkeImageBuildContext(workload)
	if err != nil {
		return err
	}
	defer cleanup()
	args := []string{"buildx", "build", "--platform", "linux/amd64", "--push", "-t", image, "-f", dockerfile, contextDir}
	fmt.Fprintf(os.Stderr, "[lke] building image %s\n", image)
	return runExternal("docker", args...)
}

func lkeImageBuildContext(workload lkeWorkload) (contextDir, dockerfile string, cleanup func(), err error) {
	workspace, err := workspaceRoot()
	if err != nil {
		return "", "", func() {}, err
	}
	switch workload.Key {
	case "video-cloud":
		return generatedVideoCloudDockerfile(filepath.Join(workspace, "repos", "rtk_video_cloud"))
	case "account-manager":
		return generatedAccountManagerDockerfile(filepath.Join(workspace, "repos", "rtk_account_manager"))
	case "cloud-admin":
		return generatedGoServiceDockerfile(filepath.Join(workspace, "repos", "rtk_cloud_admin"), "./cmd/server", "rtk-cloud-admin")
	case "frontend":
		return filepath.Join(workspace, "repos", "rtk_cloud_frontend"), filepath.Join(workspace, "repos", "rtk_cloud_frontend", "Dockerfile"), func() {}, nil
	default:
		return "", "", func() {}, fmt.Errorf("no LKE image build context for workload %s", workload.Key)
	}
}

func generatedGoServiceDockerfile(contextDir, packagePath, binaryName string) (string, string, func(), error) {
	dir, err := os.MkdirTemp("", "rtk-lke-dockerfile-*")
	if err != nil {
		return "", "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	dockerfile := filepath.Join(dir, "Dockerfile")
	goVersion := goModMajorMinor(filepath.Join(contextDir, "go.mod"))
	body := fmt.Sprintf(`FROM golang:%s-bookworm AS builder
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/app %s

FROM debian:bookworm-slim
WORKDIR /app
RUN useradd -r -u 10001 app && chown app:app /app
COPY --from=builder /out/app /app/%s
USER app
EXPOSE 8080
ENTRYPOINT ["/app/%s"]
`, goVersion, packagePath, binaryName, binaryName)
	if err := os.WriteFile(dockerfile, []byte(body), 0o644); err != nil {
		cleanup()
		return "", "", func() {}, err
	}
	return contextDir, dockerfile, cleanup, nil
}

func generatedVideoCloudDockerfile(contextDir string) (string, string, func(), error) {
	dir, err := os.MkdirTemp("", "rtk-lke-dockerfile-*")
	if err != nil {
		return "", "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	dockerfile := filepath.Join(dir, "Dockerfile")
	goVersion := goModMajorMinor(filepath.Join(contextDir, "go.mod"))
	body := fmt.Sprintf(`FROM golang:%s-bookworm AS builder
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/api ./cmd/api
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/certissuer ./cmd/certissuer
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/factoryenroll ./cmd/factoryenroll
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/cleaner ./cmd/cleaner
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/statistics ./cmd/statistics
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/metricsexporter ./cmd/metricsexporter
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/turnregistry ./cmd/turnregistry
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/logingester ./cmd/logingester
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/mqttusage ./cmd/mqttusage

FROM debian:bookworm-slim
WORKDIR /app
RUN useradd -r -u 10001 app && chown app:app /app
COPY --from=builder /out/api /app/api
COPY --from=builder /out/certissuer /app/certissuer
COPY --from=builder /out/factoryenroll /app/factoryenroll
COPY --from=builder /out/cleaner /app/cleaner
COPY --from=builder /out/statistics /app/statistics
COPY --from=builder /out/metricsexporter /app/metricsexporter
COPY --from=builder /out/turnregistry /app/turnregistry
COPY --from=builder /out/logingester /app/logingester
COPY --from=builder /out/mqttusage /app/mqttusage
USER app
EXPOSE 8080
ENTRYPOINT ["/app/api"]
`, goVersion)
	if err := os.WriteFile(dockerfile, []byte(body), 0o644); err != nil {
		cleanup()
		return "", "", func() {}, err
	}
	return contextDir, dockerfile, cleanup, nil
}

func generatedAccountManagerDockerfile(contextDir string) (string, string, func(), error) {
	dir, err := os.MkdirTemp("", "rtk-lke-dockerfile-*")
	if err != nil {
		return "", "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	dockerfile := filepath.Join(dir, "Dockerfile")
	goVersion := goModMajorMinor(filepath.Join(contextDir, "go.mod"))
	body := fmt.Sprintf(`FROM golang:%s-bookworm AS builder
WORKDIR /src
COPY . .
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/rtk-account-manager ./cmd/server
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/rtk-account-manager-migrate ./cmd/migrate

FROM debian:bookworm-slim
WORKDIR /app
RUN useradd -r -u 10001 app && chown app:app /app
COPY --from=builder /out/rtk-account-manager /app/rtk-account-manager
COPY --from=builder /out/rtk-account-manager-migrate /app/rtk-account-manager-migrate
COPY --from=builder /src/migrations /app/migrations
USER app
EXPOSE 8080
ENTRYPOINT ["/app/rtk-account-manager"]
`, goVersion)
	if err := os.WriteFile(dockerfile, []byte(body), 0o644); err != nil {
		cleanup()
		return "", "", func() {}, err
	}
	return contextDir, dockerfile, cleanup, nil
}

func goModMajorMinor(path string) string {
	body, err := os.ReadFile(path)
	if err != nil {
		return "1.24"
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || fields[0] != "go" {
			continue
		}
		parts := strings.Split(fields[1], ".")
		if len(parts) >= 2 {
			return parts[0] + "." + parts[1]
		}
		return fields[1]
	}
	return "1.24"
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
	if err := ensureLKEKubeAccess(provisionPaths{EnvRoot: envRoot}, env, false); err != nil {
		if errors.Is(err, errLKEMissingCluster) {
			fmt.Fprintf(os.Stderr, "[cloud-remove-all-vm] no LKE cluster found for stack %s\n", stack)
			return nil
		}
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

func lkeVideoCloudAuxiliaryServices() []lkeVideoCloudAuxiliaryService {
	return []lkeVideoCloudAuxiliaryService{
		{Name: "video-cloud-cleaner", Binary: "cleaner"},
		{Name: "video-cloud-statistics", Binary: "statistics"},
		{Name: "video-cloud-metricsexporter", Binary: "metricsexporter", Port: 19200, PortName: "http", MetricsPath: "/metrics/prometheus"},
		{Name: "video-cloud-turnregistry", Binary: "turnregistry", Port: 18190, PortName: "http", MetricsPath: "/metrics/prometheus"},
		{Name: "video-cloud-logingester", Binary: "logingester", Port: 19300, PortName: "http", MetricsPath: "/metrics/prometheus"},
		{Name: "video-cloud-mqttusage", Binary: "mqttusage", Port: 19400, PortName: "http", MetricsPath: "/metrics/prometheus"},
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

func lkeApplyRuntimeDependencies(paths provisionPaths, env map[string]string, opts provisionOptions) error {
	if err := kubectlApply(lkePostgresSecretManifest(env)); err != nil {
		return err
	}
	if err := kubectlApply(lkePostgresInitManifest(env)); err != nil {
		return err
	}
	if err := kubectlApply(lkePostgresServiceManifest(env)); err != nil {
		return err
	}
	if err := kubectlApply(lkePostgresStatefulSetManifest(env)); err != nil {
		return err
	}
	if err := runKubectl("-n", lkeNamespaceName(env, "platform"), "rollout", "status", "statefulset/postgresql", "--timeout", firstNonEmpty(os.Getenv("LKE_POSTGRES_ROLLOUT_TIMEOUT"), "5m")); err != nil {
		return err
	}
	var material lkeCertIssuerMaterial
	materialReady := false
	if lkeWorkloadSelected(env, opts, "video-cloud") {
		var err error
		material, err = newLKECertIssuerMaterial(env)
		if err != nil {
			return err
		}
		materialReady = true
		if err := writeLKEVideoCloudRuntimeEnv(paths, env); err != nil {
			return err
		}
		if err := kubectlApply(lkeVideoCloudRuntimeSecretManifest(env)); err != nil {
			return err
		}
		if err := kubectlApply(lkeCertIssuerRuntimeSecretManifest(env, material)); err != nil {
			return err
		}
		if err := kubectlApply(lkeCertIssuerServiceManifest(env)); err != nil {
			return err
		}
		if err := kubectlApply(lkeCertIssuerDeploymentManifest(env)); err != nil {
			return err
		}
		if err := runKubectl("-n", lkeNamespaceName(env, "video-cloud"), "rollout", "status", "deployment/certissuer", "--timeout", firstNonEmpty(os.Getenv("LKE_CERTISSUER_ROLLOUT_TIMEOUT"), "5m")); err != nil {
			return err
		}
		if err := kubectlApply(lkeFactoryEnrollRuntimeSecretManifest(env)); err != nil {
			return err
		}
		if err := kubectlApply(lkeFactoryEnrollCertIssuerClientSecretManifest(env, material)); err != nil {
			return err
		}
		if err := kubectlApply(lkeFactoryEnrollServiceManifest(env)); err != nil {
			return err
		}
		if err := kubectlApply(lkeFactoryEnrollDeploymentManifest(env)); err != nil {
			return err
		}
		if err := runKubectl("-n", lkeNamespaceName(env, "video-cloud"), "rollout", "status", "deployment/factoryenroll", "--timeout", firstNonEmpty(os.Getenv("LKE_FACTORYENROLL_ROLLOUT_TIMEOUT"), "5m")); err != nil {
			return err
		}
		mqttMaterial, err := newLKEMQTTMaterial()
		if err != nil {
			return err
		}
		if err := kubectlApply(lkeMQTTRuntimeSecretManifest(env, mqttMaterial)); err != nil {
			return err
		}
		if err := kubectlApply(lkeMQTTConfigManifest(env)); err != nil {
			return err
		}
		if err := kubectlApply(lkeMQTTServiceManifest(env)); err != nil {
			return err
		}
		if err := kubectlApply(lkeMQTTDeploymentManifest(env)); err != nil {
			return err
		}
		if err := runKubectl("-n", lkeNamespaceName(env, "video-cloud"), "rollout", "status", "deployment/mqtt", "--timeout", firstNonEmpty(os.Getenv("LKE_MQTT_ROLLOUT_TIMEOUT"), "5m")); err != nil {
			return err
		}
		if err := lkeApplyCoturnRuntime(env); err != nil {
			return err
		}
		if err := lkeApplyVideoCloudAuxiliaryServices(env); err != nil {
			return err
		}
	}
	if !lkeWorkloadSelected(env, opts, "account-manager") {
		return nil
	}
	if !materialReady {
		var err error
		material, err = newLKECertIssuerMaterial(env)
		if err != nil {
			return err
		}
	}
	if err := kubectlApply(lkeAccountManagerCertIssuerClientSecretManifest(env, material)); err != nil {
		return err
	}
	if err := writeLKEAccountManagerRuntimeEnv(paths, env); err != nil {
		return err
	}
	if err := writeLKEPlatformAdminEnv(paths, env); err != nil {
		return err
	}
	if err := kubectlApply(lkeAccountManagerSecretManifest(env)); err != nil {
		return err
	}
	_ = runKubectl("-n", lkeNamespaceName(env, "account-manager"), "delete", "job", "account-manager-migrate", "--ignore-not-found")
	if err := kubectlApply(lkeAccountManagerMigrationJobManifest(env)); err != nil {
		return err
	}
	return runKubectl("-n", lkeNamespaceName(env, "account-manager"), "wait", "--for=condition=complete", "job/account-manager-migrate", "--timeout", firstNonEmpty(os.Getenv("LKE_MIGRATION_JOB_TIMEOUT"), "5m"))
}

func lkeApplyCoturnRuntime(env map[string]string) error {
	if err := kubectlApply(lkeCoturnRuntimeSecretManifest(env)); err != nil {
		return err
	}
	if err := kubectlApply(lkeCoturnConfigManifest(env)); err != nil {
		return err
	}
	if err := kubectlApply(lkeCoturnDeploymentManifest(env)); err != nil {
		return err
	}
	if err := kubectlApply(lkeCoturnServiceManifest(env)); err != nil {
		return err
	}
	return runKubectl("-n", lkeNamespaceName(env, "video-cloud"), "rollout", "status", "deployment/coturn", "--timeout", firstNonEmpty(os.Getenv("LKE_COTURN_ROLLOUT_TIMEOUT"), "5m"))
}

func lkeApplyVideoCloudAuxiliaryServices(env map[string]string) error {
	if err := kubectlApply(lkeVideoCloudWorkersSecretManifest(env)); err != nil {
		return err
	}
	for _, service := range lkeVideoCloudAuxiliaryServices() {
		if err := kubectlApply(lkeVideoCloudAuxiliaryDeploymentManifest(env, service)); err != nil {
			return err
		}
		if service.Port > 0 {
			if err := kubectlApply(lkeVideoCloudAuxiliaryServiceManifest(env, service)); err != nil {
				return err
			}
		}
		if err := runKubectl("-n", lkeNamespaceName(env, "video-cloud"), "rollout", "status", "deployment/"+service.Name, "--timeout", firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_WORKER_ROLLOUT_TIMEOUT"), "5m")); err != nil {
			return err
		}
	}
	if err := kubectlApply(lkeVideoCloudPrometheusConfigManifest(env)); err != nil {
		return err
	}
	if err := kubectlApply(lkeVideoCloudPrometheusDeploymentManifest(env)); err != nil {
		return err
	}
	if err := kubectlApply(lkeVideoCloudPrometheusServiceManifest(env)); err != nil {
		return err
	}
	return runKubectl("-n", lkeNamespaceName(env, "observability"), "rollout", "status", "deployment/video-cloud-prometheus", "--timeout", firstNonEmpty(os.Getenv("LKE_PROMETHEUS_ROLLOUT_TIMEOUT"), "5m"))
}

func writeLKECompatibilityArtifacts(paths provisionPaths, env map[string]string) error {
	if err := os.MkdirAll(filepath.Join(paths.EnvRoot, "env"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(paths.EnvRoot, "state"), 0o755); err != nil {
		return err
	}
	stackBody := renderStackEnv(map[string]string{
		"CLOUD_ENV_NAME":        firstNonEmpty(env["CLOUD_ENV_NAME"], "staging"),
		"CLOUD_PROVIDER":        "lke",
		"CLOUD_REGION":          firstNonEmpty(env["CLOUD_REGION"], "us-sea"),
		"CLOUD_DNS_ROOT_DOMAIN": firstNonEmpty(env["CLOUD_DNS_ROOT_DOMAIN"], "realtekconnect.com"),
	}, env)
	if err := os.WriteFile(filepath.Join(paths.EnvRoot, "env", "stack.env"), []byte(stackBody), 0o644); err != nil {
		return err
	}
	stateBody, err := json.MarshalIndent(lkeCompatibilityVideoState(env), "", "  ")
	if err != nil {
		return err
	}
	stateBody = append(stateBody, '\n')
	for _, path := range uniqueNonEmpty(
		filepath.Join(paths.EnvRoot, "state", "video-cloud.state.json"),
		filepath.Join(paths.EnvRoot, "state", firstNonEmpty(env["CLOUD_STACK_NAME"], "video-cloud-staging")+".state.json"),
	) {
		if err := os.WriteFile(path, stateBody, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func lkeCompatibilityVideoState(env map[string]string) map[string]any {
	stack := firstNonEmpty(env["CLOUD_STACK_NAME"], "video-cloud-staging")
	videoNS := lkeNamespaceName(env, "video-cloud")
	accountNS := lkeNamespaceName(env, "account-manager")
	adminNS := lkeNamespaceName(env, "admin")
	frontendNS := lkeNamespaceName(env, "frontend")
	platformNS := lkeNamespaceName(env, "platform")
	serviceHost := func(service, namespace string) string {
		return service + "." + namespace + ".svc.cluster.local"
	}
	return map[string]any{
		"provider": "lke",
		"stack":    stack,
		"region":   env["CLOUD_REGION"],
		"instances": map[string]any{
			"edge": map[string]any{
				"public_ipv4": env["VIDEO_CLOUD_DOMAIN"],
				"role":        "ingress",
			},
			"api": map[string]any{
				"private_ip": serviceHost("video-cloud-api", videoNS),
				"role":       "deployment/video-cloud-api",
			},
			"infra": map[string]any{
				"private_ip": serviceHost("postgresql", platformNS),
				"role":       "statefulset/postgresql",
			},
			"mqtt": map[string]any{
				"private_ip": serviceHost("mqtt", videoNS),
				"role":       "deployment/mqtt",
			},
			"coturn": map[string]any{
				"private_ip": serviceHost("coturn", videoNS),
				"role":       "deployment/coturn",
			},
			"account-manager": map[string]any{
				"private_ip": serviceHost("account-manager", accountNS),
				"role":       "deployment/account-manager",
			},
			"cloud-admin": map[string]any{
				"private_ip": serviceHost("cloud-admin", adminNS),
				"role":       "deployment/cloud-admin",
			},
			"frontend": map[string]any{
				"private_ip": serviceHost("frontend", frontendNS),
				"role":       "deployment/frontend",
			},
		},
	}
}

func lkeWorkloadSelected(env map[string]string, opts provisionOptions, key string) bool {
	for _, workload := range lkeSelectedWorkloads(env, opts) {
		if workload.Key == key {
			return true
		}
	}
	return false
}

func lkePostgresSecretManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: postgresql-runtime
  namespace: %s
  labels:
    app.kubernetes.io/name: postgresql
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
type: Opaque
stringData:
  POSTGRES_PASSWORD: %q
`, lkeNamespaceName(env, "platform"), env["CLOUD_STACK_NAME"], lkeRuntimeSecretValue("postgres"))
}

func lkePostgresInitManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: postgresql-initdb
  namespace: %s
  labels:
    app.kubernetes.io/name: postgresql
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
data:
  001-create-databases.sql: |
    CREATE DATABASE rtk_account_manager;
    CREATE DATABASE video_cloud;
`, lkeNamespaceName(env, "platform"), env["CLOUD_STACK_NAME"])
}

func lkePostgresServiceManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: postgresql
  namespace: %s
  labels:
    app.kubernetes.io/name: postgresql
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: postgresql
  ports:
    - name: postgres
      port: 5432
      targetPort: 5432
`, lkeNamespaceName(env, "platform"), env["CLOUD_STACK_NAME"])
}

func lkePostgresStatefulSetManifest(env map[string]string) string {
	storage := `      volumes:
        - name: initdb
          configMap:
            name: postgresql-initdb
        - name: data
          emptyDir: {}
`
	volumeClaims := ""
	if strings.EqualFold(strings.TrimSpace(os.Getenv("LKE_POSTGRES_STORAGE_MODE")), "pvc") {
		storage = `      volumes:
        - name: initdb
          configMap:
            name: postgresql-initdb
`
		volumeClaims = fmt.Sprintf(`  volumeClaimTemplates:
    - metadata:
        name: data
      spec:
        accessModes: ["ReadWriteOnce"]
        resources:
          requests:
            storage: %s
`, firstNonEmpty(os.Getenv("LKE_POSTGRES_STORAGE"), "10Gi"))
	}
	return fmt.Sprintf(`apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: postgresql
  namespace: %s
  labels:
    app.kubernetes.io/name: postgresql
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  serviceName: postgresql
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: postgresql
  template:
    metadata:
      labels:
        app.kubernetes.io/name: postgresql
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
      containers:
        - name: postgres
          image: postgres:16-alpine
          ports:
            - name: postgres
              containerPort: 5432
          env:
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: postgresql-runtime
                  key: POSTGRES_PASSWORD
            - name: PGDATA
              value: /var/lib/postgresql/data/pgdata
          volumeMounts:
            - name: data
              mountPath: /var/lib/postgresql/data
            - name: initdb
              mountPath: /docker-entrypoint-initdb.d
%s%s`, lkeNamespaceName(env, "platform"), env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], storage, volumeClaims)
}

type lkeCertIssuerMaterial struct {
	ServerCert   string
	ServerKey    string
	ServiceCA    string
	ClientCert   string
	ClientKey    string
	FactoryCert  string
	FactoryKey   string
	DeviceCACert string
	DeviceCAKey  string
	AppCACert    string
	AppCAKey     string
}

type lkeMQTTMaterial struct {
	ServerCert string
	ServerKey  string
}

func newLKEMQTTMaterial() (lkeMQTTMaterial, error) {
	caCert, caKey, _, _, err := newLKECertificateAuthority("rtk-lke-mqtt-ca")
	if err != nil {
		return lkeMQTTMaterial{}, err
	}
	serverCert, serverKey, err := newLKESignedCertificate(caCert, caKey, "mqtt", []string{"mqtt"}, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	if err != nil {
		return lkeMQTTMaterial{}, err
	}
	return lkeMQTTMaterial{ServerCert: serverCert, ServerKey: serverKey}, nil
}

func newLKECertIssuerMaterial(env map[string]string) (lkeCertIssuerMaterial, error) {
	serviceCACert, serviceCAKey, serviceCACertPEM, _, err := newLKECertificateAuthority("rtk-lke-certissuer-service-ca")
	if err != nil {
		return lkeCertIssuerMaterial{}, err
	}
	deviceCACert, deviceCAKey, deviceCACertPEM, deviceCAKeyPEM, err := newLKECertificateAuthority("rtk-lke-device-ca")
	if err != nil {
		return lkeCertIssuerMaterial{}, err
	}
	appCACert, appCAKey, appCACertPEM, appCAKeyPEM, err := newLKECertificateAuthority("rtk-lke-app-ca")
	if err != nil {
		return lkeCertIssuerMaterial{}, err
	}
	serverDNS := lkeCertIssuerDNSNames(env)
	serverCertPEM, serverKeyPEM, err := newLKESignedCertificate(serviceCACert, serviceCAKey, "certissuer", serverDNS, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	if err != nil {
		return lkeCertIssuerMaterial{}, err
	}
	clientCertPEM, clientKeyPEM, err := newLKESignedCertificate(serviceCACert, serviceCAKey, "account-manager", nil, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	if err != nil {
		return lkeCertIssuerMaterial{}, err
	}
	factoryCertPEM, factoryKeyPEM, err := newLKESignedCertificate(serviceCACert, serviceCAKey, "factoryenroll", nil, nil, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	if err != nil {
		return lkeCertIssuerMaterial{}, err
	}
	_ = deviceCACert
	_ = deviceCAKey
	_ = appCACert
	_ = appCAKey
	return lkeCertIssuerMaterial{
		ServerCert:   serverCertPEM,
		ServerKey:    serverKeyPEM,
		ServiceCA:    serviceCACertPEM,
		ClientCert:   clientCertPEM,
		ClientKey:    clientKeyPEM,
		FactoryCert:  factoryCertPEM,
		FactoryKey:   factoryKeyPEM,
		DeviceCACert: deviceCACertPEM,
		DeviceCAKey:  deviceCAKeyPEM,
		AppCACert:    appCACertPEM,
		AppCAKey:     appCAKeyPEM,
	}, nil
}

func newLKECertificateAuthority(commonName string) (*x509.Certificate, *ecdsa.PrivateKey, string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, nil, "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, nil, "", "", err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(10, 0, 0),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return nil, nil, "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return nil, nil, "", "", err
	}
	return template, key,
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})),
		nil
}

func newLKESignedCertificate(caCert *x509.Certificate, caKey *ecdsa.PrivateKey, commonName string, dnsNames []string, ipAddresses []net.IP, usages []x509.ExtKeyUsage) (string, string, error) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return "", "", err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return "", "", err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.AddDate(2, 0, 0),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  usages,
		DNSNames:     dnsNames,
		IPAddresses:  ipAddresses,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, &key.PublicKey, caKey)
	if err != nil {
		return "", "", err
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})),
		nil
}

func lkeCertIssuerDNSNames(env map[string]string) []string {
	namespace := lkeNamespaceName(env, "video-cloud")
	return []string{
		"certissuer",
		"certissuer." + namespace,
		"certissuer." + namespace + ".svc",
		"certissuer." + namespace + ".svc.cluster.local",
	}
}

func lkeCertIssuerRuntimeSecretManifest(env map[string]string, material lkeCertIssuerMaterial) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: certissuer-runtime
  namespace: %s
  labels:
    app.kubernetes.io/name: certissuer
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
type: Opaque
stringData:
  POSTGRES_PASSWORD: %q
  tls.crt: %q
  tls.key: %q
  client-ca.crt: %q
  device-ca.crt: %q
  device-ca.key: %q
  app-ca.crt: %q
  app-ca.key: %q
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], lkeRuntimeSecretValue("postgres"), material.ServerCert, material.ServerKey, material.ServiceCA, material.DeviceCACert, material.DeviceCAKey, material.AppCACert, material.AppCAKey)
}

func lkeAccountManagerCertIssuerClientSecretManifest(env map[string]string, material lkeCertIssuerMaterial) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: account-manager-certissuer-client
  namespace: %s
  labels:
    app.kubernetes.io/name: account-manager
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
type: Opaque
stringData:
  client.crt: %q
  client.key: %q
  ca.crt: %q
`, lkeNamespaceName(env, "account-manager"), env["CLOUD_STACK_NAME"], material.ClientCert, material.ClientKey, material.ServiceCA)
}

func lkeFactoryEnrollRuntimeSecretManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: factoryenroll-runtime
  namespace: %s
  labels:
    app.kubernetes.io/name: factoryenroll
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
type: Opaque
stringData:
  FACTORY_ENROLL_AUTH_KEY: %q
  POSTGRES_PASSWORD: %q
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], lkeFactoryEnrollAuthKey(env), lkeRuntimeSecretValue("postgres"))
}

func lkeVideoCloudRuntimeSecretManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: video-cloud-runtime
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-api
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
type: Opaque
stringData:
  POSTGRES_PASSWORD: %q
  VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN: %q
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], lkeRuntimeSecretValue("postgres"), lkeInternalAuthToken())
}

func lkeMQTTRuntimeSecretManifest(env map[string]string, material lkeMQTTMaterial) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: mqtt-runtime
  namespace: %s
  labels:
    app.kubernetes.io/name: mqtt
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
type: Opaque
stringData:
  tls.crt: %q
  tls.key: %q
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], material.ServerCert, material.ServerKey)
}

func lkeMQTTConfigManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: mqtt-config
  namespace: %s
  labels:
    app.kubernetes.io/name: mqtt
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
data:
  mosquitto.conf: |
    listener 1883 0.0.0.0
    allow_anonymous true

    listener 8883 0.0.0.0
    allow_anonymous true
    persistence false
    log_dest stdout
    certfile /mosquitto/certs/tls.crt
    keyfile /mosquitto/certs/tls.key
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"])
}

func lkeMQTTDeploymentManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: mqtt
  namespace: %s
  labels:
    app.kubernetes.io/name: mqtt
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: mqtt
  template:
    metadata:
      labels:
        app.kubernetes.io/name: mqtt
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
      containers:
        - name: mqtt
          image: %s
          imagePullPolicy: IfNotPresent
          args: ["mosquitto", "-c", "/mosquitto/config/mosquitto.conf"]
          ports:
            - name: mqtt
              containerPort: 1883
            - name: mqtts
              containerPort: 8883
          volumeMounts:
            - name: mqtt-config
              mountPath: /mosquitto/config
              readOnly: true
            - name: mqtt-runtime
              mountPath: /mosquitto/certs
              readOnly: true
      volumes:
        - name: mqtt-config
          configMap:
            name: mqtt-config
        - name: mqtt-runtime
          secret:
            secretName: mqtt-runtime
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], firstNonEmpty(os.Getenv("LKE_MQTT_IMAGE"), "eclipse-mosquitto:2"))
}

func lkeMQTTServiceManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: mqtt
  namespace: %s
  labels:
    app.kubernetes.io/name: mqtt
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: mqtt
  ports:
    - name: mqtt
      port: 1883
      targetPort: 1883
    - name: mqtts
      port: 8883
      targetPort: 8883
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"])
}

func lkeVideoCloudWorkersSecretManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: video-cloud-workers-runtime
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-workers
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
type: Opaque
stringData:
  POSTGRES_PASSWORD: %q
  VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY: %q
  VIDEO_CLOUD_MQTT_USAGE_INGEST_TOKEN: %q
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], lkeRuntimeSecretValue("postgres"), lkeRuntimeSecretValue("turn-registry-node-auth"), lkeRuntimeSecretValue("mqtt-usage-ingest"))
}

func lkeCoturnRuntimeSecretManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: coturn-runtime
  namespace: %s
  labels:
    app.kubernetes.io/name: coturn
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
type: Opaque
stringData:
  VIDEO_CLOUD_TURN_SHARED_SECRET: %q
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], lkeRuntimeSecretValue("turn-shared"))
}

func lkeCoturnConfigManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: coturn-config
  namespace: %s
  labels:
    app.kubernetes.io/name: coturn
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
data:
  turnserver.conf: |
    use-auth-secret
    static-auth-secret=$(VIDEO_CLOUD_TURN_SHARED_SECRET)
    realm=%s
    listening-port=3478
    fingerprint
    min-port=%s
    max-port=%s
    no-loopback-peers
    no-multicast-peers
    log-file=stdout
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], firstNonEmpty(os.Getenv("LKE_COTURN_REALM"), "video_cloud"), firstNonEmpty(os.Getenv("LKE_COTURN_MIN_PORT"), "49152"), firstNonEmpty(os.Getenv("LKE_COTURN_MAX_PORT"), "49200"))
}

func lkeCoturnDeploymentManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: coturn
  namespace: %s
  labels:
    app.kubernetes.io/name: coturn
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: coturn
  template:
    metadata:
      labels:
        app.kubernetes.io/name: coturn
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
      initContainers:
        - name: render-config
          image: busybox:1.36
          command: ["/bin/sh", "-c"]
          args:
            - |
              printf '%%s\n' \
                use-auth-secret \
                "static-auth-secret=${VIDEO_CLOUD_TURN_SHARED_SECRET}" \
                realm=%s \
                listening-port=3478 \
                fingerprint \
                min-port=%s \
                max-port=%s \
                no-loopback-peers \
                no-multicast-peers \
                log-file=stdout \
                > /tmp/coturn/turnserver.conf
          env:
            - name: VIDEO_CLOUD_TURN_SHARED_SECRET
              valueFrom:
                secretKeyRef:
                  name: coturn-runtime
                  key: VIDEO_CLOUD_TURN_SHARED_SECRET
          volumeMounts:
            - name: coturn-runtime-config
              mountPath: /tmp/coturn
      containers:
        - name: coturn
          image: %s
          imagePullPolicy: IfNotPresent
          command: ["/usr/bin/turnserver", "-c", "/tmp/coturn/turnserver.conf"]
          ports:
            - name: turn-udp
              containerPort: 3478
              protocol: UDP
            - name: turn-tcp
              containerPort: 3478
              protocol: TCP
          volumeMounts:
            - name: coturn-runtime-config
              mountPath: /tmp/coturn
              readOnly: true
      volumes:
        - name: coturn-runtime-config
          emptyDir: {}
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], firstNonEmpty(os.Getenv("LKE_COTURN_REALM"), "video_cloud"), firstNonEmpty(os.Getenv("LKE_COTURN_MIN_PORT"), "49152"), firstNonEmpty(os.Getenv("LKE_COTURN_MAX_PORT"), "49200"), firstNonEmpty(os.Getenv("LKE_COTURN_IMAGE"), "coturn/coturn:4.6.2"))
}

func lkeCoturnServiceManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: coturn
  namespace: %s
  labels:
    app.kubernetes.io/name: coturn
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  type: %s
  selector:
    app.kubernetes.io/name: coturn
  ports:
    - name: turn-udp
      port: 3478
      targetPort: 3478
      protocol: UDP
    - name: turn-tcp
      port: 3478
      targetPort: 3478
      protocol: TCP
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], firstNonEmpty(os.Getenv("LKE_COTURN_SERVICE_TYPE"), "ClusterIP"))
}

func lkeVideoCloudAuxiliaryDeploymentManifest(env map[string]string, service lkeVideoCloudAuxiliaryService) string {
	ports := ""
	if service.Port > 0 {
		ports = fmt.Sprintf(`          ports:
            - name: %s
              containerPort: %d
`, firstNonEmpty(service.PortName, "http"), service.Port)
	}
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
          command: ["/app/%s"]
%s          env:
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: video-cloud-workers-runtime
                  key: POSTGRES_PASSWORD
            - name: VIDEO_CLOUD_ENV
              value: "staging"
            - name: VIDEO_CLOUD_LOG_LEVEL
              value: %q
            - name: VIDEO_CLOUD_DB_DSN
              value: "postgres://postgres:$(POSTGRES_PASSWORD)@postgresql.%s.svc.cluster.local:5432/video_cloud?sslmode=disable"
            - name: VIDEO_CLOUD_LOG_DB_DSN
              value: "postgres://postgres:$(POSTGRES_PASSWORD)@postgresql.%s.svc.cluster.local:5432/video_cloud?sslmode=disable"
            - name: VIDEO_CLOUD_MQTT_ADDR
              value: %q
            - name: VIDEO_CLOUD_MQTT_CLIENT_ID
              value: %q
            - name: VIDEO_CLOUD_MQTT_TOPIC_ROOT
              value: "devices"
            - name: VIDEO_CLOUD_METRICS_EXPORTER_ADDR
              value: "0.0.0.0:19200"
            - name: VIDEO_CLOUD_TURN_REGISTRY_ADDR
              value: "0.0.0.0:18190"
            - name: VIDEO_CLOUD_LOG_INGESTER_ADDR
              value: "0.0.0.0:19300"
            - name: VIDEO_CLOUD_MQTT_USAGE_ADDR
              value: "0.0.0.0:19400"
            - name: VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY
              valueFrom:
                secretKeyRef:
                  name: video-cloud-workers-runtime
                  key: VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY
            - name: VIDEO_CLOUD_MQTT_USAGE_INGEST_TOKEN
              valueFrom:
                secretKeyRef:
                  name: video-cloud-workers-runtime
                  key: VIDEO_CLOUD_MQTT_USAGE_INGEST_TOKEN
`, service.Name, lkeNamespaceName(env, "video-cloud"), service.Name, env["CLOUD_STACK_NAME"], service.Name, service.Name, env["CLOUD_STACK_NAME"], lkeVideoCloudImage(env), service.Binary, ports, firstNonEmpty(os.Getenv("VIDEO_CLOUD_LOG_LEVEL"), "info"), lkeNamespaceName(env, "platform"), lkeNamespaceName(env, "platform"), lkeMQTTInternalAddr(env), service.Name)
}

func lkeVideoCloudAuxiliaryServiceManifest(env map[string]string, service lkeVideoCloudAuxiliaryService) string {
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
    - name: %s
      port: %d
      targetPort: %d
`, service.Name, lkeNamespaceName(env, "video-cloud"), service.Name, env["CLOUD_STACK_NAME"], service.Name, firstNonEmpty(service.PortName, "http"), service.Port, service.Port)
}

func lkeVideoCloudPrometheusConfigManifest(env map[string]string) string {
	videoNS := lkeNamespaceName(env, "video-cloud")
	return fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: video-cloud-prometheus-config
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-prometheus
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
data:
  prometheus.yml: |
    global:
      scrape_interval: 15s
      evaluation_interval: 15s

    scrape_configs:
      - job_name: video-cloud-api
        metrics_path: /metrics/prometheus
        static_configs:
          - targets: ["video-cloud-api.%s.svc.cluster.local:80"]
      - job_name: video-cloud-turnregistry
        metrics_path: /metrics/prometheus
        static_configs:
          - targets: ["video-cloud-turnregistry.%s.svc.cluster.local:18190"]
      - job_name: video-cloud-metrics-exporter
        metrics_path: /metrics/prometheus
        static_configs:
          - targets: ["video-cloud-metricsexporter.%s.svc.cluster.local:19200"]
      - job_name: video-cloud-logingester
        metrics_path: /metrics/prometheus
        static_configs:
          - targets: ["video-cloud-logingester.%s.svc.cluster.local:19300"]
      - job_name: video-cloud-mqttusage
        metrics_path: /metrics/prometheus
        static_configs:
          - targets: ["video-cloud-mqttusage.%s.svc.cluster.local:19400"]
      - job_name: video-cloud-factoryenroll
        metrics_path: /metrics/prometheus
        static_configs:
          - targets: ["factoryenroll.%s.svc.cluster.local:80"]
`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"], videoNS, videoNS, videoNS, videoNS, videoNS, videoNS)
}

func lkeVideoCloudPrometheusDeploymentManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: video-cloud-prometheus
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-prometheus
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: video-cloud-prometheus
  template:
    metadata:
      labels:
        app.kubernetes.io/name: video-cloud-prometheus
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
      containers:
        - name: prometheus
          image: %s
          imagePullPolicy: IfNotPresent
          args:
            - --config.file=/etc/prometheus/prometheus.yml
            - --storage.tsdb.path=/prometheus
            - --storage.tsdb.retention.time=%s
            - --web.listen-address=0.0.0.0:9090
          ports:
            - name: http
              containerPort: 9090
          volumeMounts:
            - name: config
              mountPath: /etc/prometheus
              readOnly: true
            - name: data
              mountPath: /prometheus
      volumes:
        - name: config
          configMap:
            name: video-cloud-prometheus-config
        - name: data
          emptyDir: {}
`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], firstNonEmpty(os.Getenv("LKE_PROMETHEUS_IMAGE"), "prom/prometheus:v2.53.1"), firstNonEmpty(os.Getenv("LKE_PROMETHEUS_RETENTION"), "24h"))
}

func lkeVideoCloudPrometheusServiceManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: video-cloud-prometheus
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-prometheus
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: video-cloud-prometheus
  ports:
    - name: http
      port: 9090
      targetPort: 9090
`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"])
}

func lkeMQTTInternalAddr(env map[string]string) string {
	return "mqtt." + lkeNamespaceName(env, "video-cloud") + ".svc.cluster.local:1883"
}

func lkeFactoryEnrollCertIssuerClientSecretManifest(env map[string]string, material lkeCertIssuerMaterial) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: factoryenroll-certissuer-client
  namespace: %s
  labels:
    app.kubernetes.io/name: factoryenroll
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
type: Opaque
stringData:
  client.crt: %q
  client.key: %q
  ca.crt: %q
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], material.FactoryCert, material.FactoryKey, material.ServiceCA)
}

func lkeCertIssuerDeploymentManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: certissuer
  namespace: %s
  labels:
    app.kubernetes.io/name: certissuer
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: certissuer
  template:
    metadata:
      labels:
        app.kubernetes.io/name: certissuer
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
      containers:
        - name: certissuer
          image: %s
          imagePullPolicy: IfNotPresent
          command: ["/app/certissuer"]
          ports:
            - name: https
              containerPort: 9443
          env:
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: certissuer-runtime
                  key: POSTGRES_PASSWORD
            - name: CERT_ISSUER_LISTEN_ADDR
              value: ":9443"
            - name: CERT_ISSUER_SERVER_CERT
              value: /etc/video-cloud/certissuer/tls.crt
            - name: CERT_ISSUER_SERVER_KEY
              value: /etc/video-cloud/certissuer/tls.key
            - name: CERT_ISSUER_CLIENT_CA
              value: /etc/video-cloud/certissuer/client-ca.crt
            - name: CERT_ISSUER_CA_CERT_PATH
              value: /etc/video-cloud/certissuer/device-ca.crt
            - name: CERT_ISSUER_CA_KEY_PATH
              value: /etc/video-cloud/certissuer/device-ca.key
            - name: CERT_ISSUER_APP_CA_CERT_PATH
              value: /etc/video-cloud/certissuer/app-ca.crt
            - name: CERT_ISSUER_APP_CA_KEY_PATH
              value: /etc/video-cloud/certissuer/app-ca.key
            - name: CERT_ISSUER_DB_DSN
              value: "postgres://postgres:$(POSTGRES_PASSWORD)@postgresql.%s.svc.cluster.local:5432/video_cloud?sslmode=disable"
          volumeMounts:
            - name: certissuer-runtime
              mountPath: /etc/video-cloud/certissuer
              readOnly: true
      volumes:
        - name: certissuer-runtime
          secret:
            secretName: certissuer-runtime
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], lkeVideoCloudImage(env), lkeNamespaceName(env, "platform"))
}

func lkeFactoryEnrollDeploymentManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: factoryenroll
  namespace: %s
  labels:
    app.kubernetes.io/name: factoryenroll
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: factoryenroll
  template:
    metadata:
      labels:
        app.kubernetes.io/name: factoryenroll
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
      containers:
        - name: factoryenroll
          image: %s
          imagePullPolicy: IfNotPresent
          command: ["/app/factoryenroll"]
          ports:
            - name: http
              containerPort: 18443
          env:
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: factoryenroll-runtime
                  key: POSTGRES_PASSWORD
            - name: FACTORY_ENROLL_AUTH_KEY
              valueFrom:
                secretKeyRef:
                  name: factoryenroll-runtime
                  key: FACTORY_ENROLL_AUTH_KEY
            - name: FACTORY_ENROLL_ADDR
              value: ":18443"
            - name: FACTORY_ENROLL_CERT_ISSUER_URL
              value: %q
            - name: FACTORY_ENROLL_CERT_ISSUER_CLIENT_CERT
              value: /etc/video-cloud/factoryenroll/client.crt
            - name: FACTORY_ENROLL_CERT_ISSUER_CLIENT_KEY
              value: /etc/video-cloud/factoryenroll/client.key
            - name: FACTORY_ENROLL_CERT_ISSUER_CA
              value: /etc/video-cloud/factoryenroll/ca.crt
            - name: VIDEO_CLOUD_DB_DSN
              value: "postgres://postgres:$(POSTGRES_PASSWORD)@postgresql.%s.svc.cluster.local:5432/video_cloud?sslmode=disable"
          volumeMounts:
            - name: factoryenroll-certissuer-client
              mountPath: /etc/video-cloud/factoryenroll
              readOnly: true
      volumes:
        - name: factoryenroll-certissuer-client
          secret:
            secretName: factoryenroll-certissuer-client
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], lkeVideoCloudImage(env), lkeCertIssuerBaseURL(env), lkeNamespaceName(env, "platform"))
}

func lkeCertIssuerServiceManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: certissuer
  namespace: %s
  labels:
    app.kubernetes.io/name: certissuer
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: certissuer
  ports:
    - name: https
      port: 9443
      targetPort: 9443
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"])
}

func lkeFactoryEnrollServiceManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: factoryenroll
  namespace: %s
  labels:
    app.kubernetes.io/name: factoryenroll
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: factoryenroll
  ports:
    - name: http
      port: 80
      targetPort: 18443
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"])
}

func lkeVideoCloudImage(env map[string]string) string {
	for _, workload := range lkeWorkloads(env) {
		if workload.Key == "video-cloud" {
			return workload.Image
		}
	}
	return ""
}

func lkeCertIssuerBaseURL(env map[string]string) string {
	return "https://certissuer." + lkeNamespaceName(env, "video-cloud") + ".svc.cluster.local:9443"
}

func lkeFactoryEnrollAuthKey(env map[string]string) string {
	return firstNonEmpty(os.Getenv("FACTORY_ENROLL_AUTH_KEY"), lkeRuntimeSecretValue("factory-enroll-auth"))
}

func lkeInternalAuthToken() string {
	return lkeRuntimeSecretValue("internal-auth")
}

func lkeAccountManagerSecretManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: account-manager-runtime
  namespace: %s
  labels:
    app.kubernetes.io/name: account-manager
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
type: Opaque
stringData:
  DATABASE_URL: %q
  JWT_ACCESS_SECRET: %q
  JWT_REFRESH_SECRET: %q
  ACCOUNT_MANAGER_INTERNAL_AUTH_TOKEN: %q
  ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL: %q
  ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD: %q
  ACCOUNT_MANAGER_ENV: "staging"
  ACCOUNT_MANAGER_LOG_LEVEL: %q
  AUTH_TOKEN_DELIVERY: "log"
  CROSS_SERVICE_BROKER: "log"
  APP_CERT_ISSUER_BASE_URL: %q
  APP_CERT_ISSUER_CLIENT_CERT: "/etc/rtk-account-manager/certissuer/client.crt"
  APP_CERT_ISSUER_CLIENT_KEY: "/etc/rtk-account-manager/certissuer/client.key"
  APP_CERT_ISSUER_CA_FILE: "/etc/rtk-account-manager/certissuer/ca.crt"
`, lkeNamespaceName(env, "account-manager"), env["CLOUD_STACK_NAME"], lkeAccountManagerDatabaseURL(env), lkeRuntimeSecretValue("jwt-access"), lkeRuntimeSecretValue("jwt-refresh"), lkeInternalAuthToken(), lkePlatformAdminEmail(env), lkeRuntimeSecretValue("platform-admin"), firstNonEmpty(os.Getenv("ACCOUNT_MANAGER_LOG_LEVEL"), "info"), lkeCertIssuerBaseURL(env))
}

func lkeAccountManagerMigrationJobManifest(env map[string]string) string {
	image := ""
	for _, workload := range lkeWorkloads(env) {
		if workload.Key == "account-manager" {
			image = workload.Image
			break
		}
	}
	return fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata:
  name: account-manager-migrate
  namespace: %s
  labels:
    app.kubernetes.io/name: account-manager-migrate
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  backoffLimit: 6
  template:
    metadata:
      labels:
        app.kubernetes.io/name: account-manager-migrate
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
      restartPolicy: OnFailure
      containers:
        - name: migrate
          image: %s
          imagePullPolicy: IfNotPresent
          command: ["/app/rtk-account-manager-migrate"]
          envFrom:
            - secretRef:
                name: account-manager-runtime
`, lkeNamespaceName(env, "account-manager"), env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], image)
}

func lkeAccountManagerDatabaseURL(env map[string]string) string {
	return fmt.Sprintf("postgres://postgres:%s@postgresql.%s.svc.cluster.local:5432/rtk_account_manager?sslmode=disable", lkeRuntimeSecretValue("postgres"), lkeNamespaceName(env, "platform"))
}

func writeLKEPlatformAdminEnv(paths provisionPaths, env map[string]string) error {
	path := filepath.Join(paths.EnvRoot, "services", "account-manager", "account-manager-platform-admin.env")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body := fmt.Sprintf("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL=%s\nACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD=%s\n", lkePlatformAdminEmail(env), lkeRuntimeSecretValue("platform-admin"))
	return os.WriteFile(path, []byte(body), 0o600)
}

func writeLKEAccountManagerRuntimeEnv(paths provisionPaths, env map[string]string) error {
	path := filepath.Join(paths.EnvRoot, "services", "account-manager", "account-manager.env")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body := fmt.Sprintf("ACCOUNT_MANAGER_INTERNAL_AUTH_TOKEN=%s\n", lkeInternalAuthToken())
	return os.WriteFile(path, []byte(body), 0o600)
}

func writeLKEVideoCloudRuntimeEnv(paths provisionPaths, env map[string]string) error {
	path := filepath.Join(paths.EnvRoot, "services", "video-cloud", "video-cloud.env")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body := fmt.Sprintf("FACTORY_ENROLL_AUTH_KEY=%s\nVIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN=%s\n", lkeFactoryEnrollAuthKey(env), lkeInternalAuthToken())
	return os.WriteFile(path, []byte(body), 0o600)
}

func lkePlatformAdminEmail(env map[string]string) string {
	return firstNonEmpty(os.Getenv("ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"), "platform-admin@"+lkeName(firstNonEmpty(env["CLOUD_STACK_NAME"], "video-cloud-staging"))+".local")
}

func lkeRuntimeSecretValue(name string) string {
	if value := os.Getenv("LKE_" + strings.ToUpper(strings.ReplaceAll(name, "-", "_"))); value != "" {
		return value
	}
	if seed := os.Getenv("LKE_RUNTIME_SECRET_SEED"); seed != "" {
		return seed + "-" + name
	}
	if value := lkeRuntimeSecretCache[name]; value != "" {
		return value
	}
	value := randomSecret()
	lkeRuntimeSecretCache[name] = value
	return value
}

func randomSecret() string {
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return base64.RawURLEncoding.EncodeToString(buf[:])
}

func lkeDeploymentManifest(env map[string]string, workload lkeWorkload) string {
	envFrom := ""
	extraEnv := ""
	volumeMounts := ""
	volumes := ""
	if workload.Key == "account-manager" {
		envFrom = `          envFrom:
            - secretRef:
                name: account-manager-runtime
`
		volumeMounts = `          volumeMounts:
            - name: account-manager-certissuer-client
              mountPath: /etc/rtk-account-manager/certissuer
              readOnly: true
`
		volumes = `      volumes:
        - name: account-manager-certissuer-client
          secret:
            secretName: account-manager-certissuer-client
`
	}
	if workload.Key == "video-cloud" {
		extraEnv = fmt.Sprintf(`            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: POSTGRES_PASSWORD
            - name: VIDEO_CLOUD_API_ADDR
              value: ":8080"
            - name: VIDEO_CLOUD_DB_DSN
              value: "postgres://postgres:$(POSTGRES_PASSWORD)@postgresql.%s.svc.cluster.local:5432/video_cloud?sslmode=disable"
            - name: VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN
            - name: VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_URL
              value: %q
            - name: VIDEO_CLOUD_AUTH_TRUSTED_CLIENT_CERT_HEADERS
              value: "true"
`, lkeNamespaceName(env, "platform"), lkeAccountManagerInternalURL(env))
	}
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
%s%s%s%s`, workload.Name, workload.Namespace, workload.Name, env["CLOUD_STACK_NAME"], workload.Name, workload.Name, env["CLOUD_STACK_NAME"], workload.Image, workload.Port, env["CLOUD_STACK_NAME"], workload.Host, extraEnv, envFrom, volumeMounts, volumes)
}

func lkeAccountManagerInternalURL(env map[string]string) string {
	return "http://account-manager." + lkeNamespaceName(env, "account-manager") + ".svc.cluster.local:80"
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
	cmd := exec.Command(lkeKubectl(), lkeKubectlArgs("apply", "-f", "-")...)
	cmd.Stdin = strings.NewReader(manifest)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runKubectl(args ...string) error {
	cmd := exec.Command(lkeKubectl(), lkeKubectlArgs(args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func lkeKubectl() string {
	return firstNonEmpty(os.Getenv("RTK_CLOUD_KUBECTL"), "kubectl")
}

func lkeKubectlArgs(args ...string) []string {
	if kubeconfig := os.Getenv("RTK_CLOUD_LKE_KUBECONFIG"); kubeconfig != "" {
		return append([]string{"--kubeconfig", kubeconfig}, args...)
	}
	return args
}

func ensureLKEKubeAccess(paths provisionPaths, env map[string]string, allowCreate bool) error {
	out, err := exec.Command(lkeKubectl(), "config", "current-context").CombinedOutput()
	context := strings.TrimSpace(string(out))
	if err == nil && context != "" {
		fmt.Fprintf(os.Stderr, "[lke] kubectl context: %s\n", context)
		return nil
	}
	if kubeconfig := firstNonEmpty(os.Getenv("RTK_CLOUD_LKE_KUBECONFIG"), os.Getenv("LKE_KUBECONFIG")); kubeconfig != "" {
		if _, statErr := os.Stat(kubeconfig); statErr != nil {
			return statErr
		}
		_ = os.Setenv("RTK_CLOUD_LKE_KUBECONFIG", kubeconfig)
		return nil
	}
	stateKubeconfig := filepath.Join(paths.EnvRoot, "state", "lke-kubeconfig.yaml")
	if _, statErr := os.Stat(stateKubeconfig); statErr == nil {
		_ = os.Setenv("RTK_CLOUD_LKE_KUBECONFIG", stateKubeconfig)
		return nil
	}
	clusterID := lkeClusterID(paths, env)
	token := resolveLinodeToken(paths.EnvRoot)
	if clusterID == "" {
		if token == "" {
			return fmt.Errorf("kubectl current-context is required for LKE operations; set KUBECONFIG, RTK_CLOUD_LKE_KUBECONFIG, LKE_CLUSTER_ID, or LINODE_TOKEN before running destructive staging commands")
		}
		discovered, err := discoverLKECluster(token, paths, env, allowCreate)
		if err != nil {
			return err
		}
		clusterID = strconv.Itoa(discovered.ID)
		if err := writeLKEState(paths, discovered); err != nil {
			return err
		}
	}
	if token == "" {
		return errors.New("LINODE_TOKEN is required to fetch LKE kubeconfig")
	}
	kubeconfig, err := fetchLKEKubeconfig(token, clusterID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(stateKubeconfig), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(stateKubeconfig, kubeconfig, 0o600); err != nil {
		return err
	}
	_ = os.Setenv("RTK_CLOUD_LKE_KUBECONFIG", stateKubeconfig)
	fmt.Fprintf(os.Stderr, "[lke] wrote kubeconfig: %s\n", stateKubeconfig)
	return nil
}

func lkeClusterID(paths provisionPaths, env map[string]string) string {
	return firstNonEmpty(
		os.Getenv("LKE_CLUSTER_ID"),
		env["LKE_CLUSTER_ID"],
		envFileValue(filepath.Join(paths.EnvRoot, "state", "lke.env"), "LKE_CLUSTER_ID"),
		envFileValue(filepath.Join(paths.EnvRoot, "env", "stack.env"), "LKE_CLUSTER_ID"),
	)
}

type lkeCluster struct {
	ID         int    `json:"id"`
	Label      string `json:"label"`
	Region     string `json:"region"`
	K8sVersion string `json:"k8s_version"`
}

func lkeClusterLabel(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_CLUSTER_LABEL"), env["LKE_CLUSTER_LABEL"], env["CLOUD_STACK_NAME"]+"-lke")
}

func discoverLKECluster(token string, paths provisionPaths, env map[string]string, allowCreate bool) (lkeCluster, error) {
	label := lkeClusterLabel(env)
	if label == "" {
		return lkeCluster{}, errors.New("LKE_CLUSTER_LABEL or CLOUD_STACK_NAME is required to discover an LKE cluster")
	}
	out, err := linodeRequestRaw(token, "GET", "/lke/clusters?page_size=500", "")
	if err != nil {
		return lkeCluster{}, err
	}
	var listed struct {
		Data []lkeCluster `json:"data"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		return lkeCluster{}, err
	}
	for _, cluster := range listed.Data {
		if cluster.Label == label {
			return cluster, nil
		}
	}
	if !allowCreate {
		return lkeCluster{}, fmt.Errorf("%w: %s", errLKEMissingCluster, label)
	}
	return createLKECluster(token, env)
}

func createLKECluster(token string, env map[string]string) (lkeCluster, error) {
	version := firstNonEmpty(os.Getenv("LKE_K8S_VERSION"), env["LKE_K8S_VERSION"])
	if version == "" {
		selected, err := latestLKEVersion(token)
		if err != nil {
			return lkeCluster{}, err
		}
		version = selected
	}
	nodeType := firstNonEmpty(os.Getenv("LKE_NODE_TYPE"), env["LKE_NODE_TYPE"], "g6-standard-2")
	nodeCount, err := strconv.Atoi(firstNonEmpty(os.Getenv("LKE_NODE_COUNT"), env["LKE_NODE_COUNT"], "3"))
	if err != nil || nodeCount <= 0 {
		return lkeCluster{}, errors.New("LKE_NODE_COUNT must be a positive integer")
	}
	payload, err := json.Marshal(map[string]any{
		"label":       lkeClusterLabel(env),
		"region":      firstNonEmpty(env["CLOUD_REGION"], "us-sea"),
		"k8s_version": version,
		"tags":        []string{"rtk-cloud", env["CLOUD_STACK_NAME"], "staging"},
		"node_pools": []map[string]any{
			{"type": nodeType, "count": nodeCount},
		},
	})
	if err != nil {
		return lkeCluster{}, err
	}
	out, err := linodeRequestRaw(token, "POST", "/lke/clusters", string(payload))
	if err != nil {
		return lkeCluster{}, err
	}
	var created lkeCluster
	if err := json.Unmarshal(out, &created); err != nil {
		return lkeCluster{}, err
	}
	if created.ID == 0 {
		return lkeCluster{}, errors.New("LKE create response did not include cluster id")
	}
	if created.Label == "" {
		created.Label = lkeClusterLabel(env)
	}
	if created.Region == "" {
		created.Region = env["CLOUD_REGION"]
	}
	if created.K8sVersion == "" {
		created.K8sVersion = version
	}
	return created, nil
}

func latestLKEVersion(token string) (string, error) {
	out, err := linodeRequestRaw(token, "GET", "/lke/versions", "")
	if err != nil {
		return "", err
	}
	var listed struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		return "", err
	}
	if len(listed.Data) == 0 || listed.Data[0].ID == "" {
		return "", errors.New("LKE versions response did not include any versions")
	}
	return listed.Data[0].ID, nil
}

func writeLKEState(paths provisionPaths, cluster lkeCluster) error {
	statePath := filepath.Join(paths.EnvRoot, "state", "lke.env")
	if err := os.MkdirAll(filepath.Dir(statePath), 0o755); err != nil {
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "LKE_CLUSTER_ID=%d\n", cluster.ID)
	fmt.Fprintf(&b, "LKE_CLUSTER_LABEL=%s\n", cluster.Label)
	if cluster.Region != "" {
		fmt.Fprintf(&b, "LKE_CLUSTER_REGION=%s\n", cluster.Region)
	}
	if cluster.K8sVersion != "" {
		fmt.Fprintf(&b, "LKE_CLUSTER_VERSION=%s\n", cluster.K8sVersion)
	}
	return os.WriteFile(statePath, []byte(b.String()), 0o600)
}

func fetchLKEKubeconfig(token, clusterID string) ([]byte, error) {
	path := "/lke/clusters/" + clusterID + "/kubeconfig"
	var out []byte
	var err error
	attempts := envIntDefault("LKE_KUBECONFIG_RETRY_ATTEMPTS", 30)
	delay := envDurationDefault("LKE_KUBECONFIG_RETRY_DELAY", 10*time.Second)
	for attempt := 1; attempt <= attempts; attempt++ {
		out, err = linodeRequestRaw(token, "GET", path, "")
		if err == nil {
			break
		}
		if attempt == attempts || !isTransientLKEKubeconfigError(err) {
			return nil, err
		}
		fmt.Fprintf(os.Stderr, "[lke] kubeconfig not available yet, retrying attempt %d/%d\n", attempt+1, attempts)
		if delay > 0 {
			time.Sleep(delay)
		}
	}
	var parsed struct {
		Kubeconfig string `json:"kubeconfig"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, err
	}
	if parsed.Kubeconfig == "" {
		return nil, errors.New("LKE kubeconfig response did not include kubeconfig")
	}
	decoded, err := base64.StdEncoding.DecodeString(parsed.Kubeconfig)
	if err != nil {
		return nil, err
	}
	return decoded, nil
}

func isTransientLKEKubeconfigError(err error) bool {
	message := err.Error()
	return strings.Contains(message, "503") || strings.Contains(message, "not yet available")
}

func linodeRequestRaw(token, method, path, data string) ([]byte, error) {
	args := []string{"-fsS", "-X", method, "https://api.linode.com/v4" + path, "-H", "Authorization: Bearer " + token, "-H", "Content-Type: application/json"}
	if data != "" {
		args = append(args, "-d", data)
	}
	cmd := exec.Command("curl", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		errText := strings.TrimSpace(stderr.String())
		if errText != "" {
			return nil, fmt.Errorf("Linode API %s %s failed: %w: %s", method, path, err, errText)
		}
		return nil, fmt.Errorf("Linode API %s %s failed: %w", method, path, err)
	}
	return out, nil
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

func envDurationDefault(key string, fallback time.Duration) time.Duration {
	if value := os.Getenv(key); value != "" {
		if parsed, err := time.ParseDuration(value); err == nil && parsed >= 0 {
			return parsed
		}
	}
	return fallback
}
