package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

type lkeNamespace struct {
	Key  string
	Name string
}

type lkeImageArtifact struct {
	Key          string `json:"key"`
	Name         string `json:"name"`
	EnvKey       string `json:"env_key"`
	Image        string `json:"image"`
	Digest       string `json:"digest,omitempty"`
	SourceRepo   string `json:"source_repo,omitempty"`
	SourcePath   string `json:"source_path,omitempty"`
	SourceCommit string `json:"source_commit,omitempty"`
	Tag          string `json:"tag,omitempty"`
}

type lkeServiceImageSource struct {
	Key      string
	Name     string
	EnvKey   string
	RepoName string
	RepoPath string
}

var errLKEMissingCluster = errors.New("no matching LKE cluster found")
var errLKEImageNotFound = errors.New("LKE image not found")
var lkeRuntimeSecretCache = map[string]string{}
var lkeRuntimeSecretStateDir string
var inspectLKEImage = func(image string) error {
	return runExternal("docker", "buildx", "imagetools", "inspect", image)
}

func runLKEBuildImages(args []string) error {
	fs := flag.NewFlagSet("lke-build-images", flag.ContinueOnError)
	workspace := fs.String("workspace", ".", "workspace root")
	envRoot := fs.String("env-root", "cloud_env/staging/runtime", "normalized environment runtime root")
	registryFlag := fs.String("registry", "", "container image registry/repository prefix, for example ghcr.io/org/repo/lke")
	tagFlag := fs.String("tag", "", "container image tag")
	workloadsFlag := fs.String("workloads", "postgres", "comma-separated workload keys to build, or all")
	imageFlag := fs.String("image", "", "exact image to build; valid only when --workloads selects one workload")
	out := fs.String("out", "", "write image manifest JSON to this path")
	if err := fs.Parse(args); err != nil {
		return err
	}
	workspaceAbs, err := filepath.Abs(*workspace)
	if err != nil {
		return err
	}
	oldwd, err := os.Getwd()
	if err != nil {
		return err
	}
	if err := os.Chdir(workspaceAbs); err != nil {
		return err
	}
	defer func() { _ = os.Chdir(oldwd) }()
	oldWorkspaceEnv, hadWorkspaceEnv := os.LookupEnv("RTK_CLOUD_WORKSPACE")
	_ = os.Setenv("RTK_CLOUD_WORKSPACE", workspaceAbs)
	defer func() {
		if hadWorkspaceEnv {
			_ = os.Setenv("RTK_CLOUD_WORKSPACE", oldWorkspaceEnv)
			return
		}
		_ = os.Unsetenv("RTK_CLOUD_WORKSPACE")
	}()

	resolvedEnvRoot := *envRoot
	if !filepath.IsAbs(resolvedEnvRoot) {
		resolvedEnvRoot = filepath.Join(workspaceAbs, resolvedEnvRoot)
	}
	if filepath.Base(resolvedEnvRoot) != "lke" {
		candidate := filepath.Join(resolvedEnvRoot, "lke")
		if _, err := os.Stat(candidate); err == nil {
			resolvedEnvRoot = candidate
		}
	}
	env, err := envroot.Load(resolvedEnvRoot, "")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			env = envroot.Environment{Values: defaultProvisionEnvValues()}
		} else {
			return err
		}
	}
	env.Values["CLOUD_PROVIDER"] = "lke"

	allWorkloads := lkeImageWorkloads(env.Values, provisionOptions{})
	selectedWorkloads, err := selectLKEBuildImageWorkloads(allWorkloads, *workloadsFlag)
	if err != nil {
		return err
	}
	exactImage := strings.TrimSpace(*imageFlag)
	if exactImage != "" && len(selectedWorkloads) != 1 {
		return errors.New("lke-build-images --image requires exactly one selected workload")
	}
	registry := strings.TrimRight(firstNonEmpty(*registryFlag, os.Getenv("LKE_IMAGE_REGISTRY")), "/")
	if registry == "" && exactImage == "" {
		return errors.New("lke-build-images requires --registry or LKE_IMAGE_REGISTRY unless --image is set for one workload")
	}
	tag := firstNonEmpty(*tagFlag, os.Getenv("LKE_IMAGE_TAG"), shortGitCommit(workspaceAbs), lkeName(firstNonEmpty(env.Values["CLOUD_STACK_NAME"], "video-cloud-staging")))
	artifacts := []lkeImageArtifact{}
	for _, workload := range selectedWorkloads {
		image := exactImage
		if image == "" {
			image = registry + "/" + workload.Name + ":" + tag
		}
		if err := buildLKEImage(workload, image); err != nil {
			return err
		}
		artifacts = append(artifacts, lkeImageArtifact{
			Key:    workload.Key,
			Name:   workload.Name,
			EnvKey: workload.EnvKey,
			Image:  image,
			Tag:    tag,
		})
	}
	manifest := map[string]any{
		"schema":        "rtk-cloud-workspace.lke-image-artifacts/v1",
		"generated_at":  time.Now().UTC().Format(time.RFC3339),
		"provider":      "lke",
		"stack":         env.Values["CLOUD_STACK_NAME"],
		"source_commit": strings.TrimSpace(firstNonEmpty(shortGitCommit(workspaceAbs), "unknown")),
		"registry":      registry,
		"tag":           tag,
		"images":        artifacts,
		"env":           lkeImageArtifactEnv(artifacts),
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if *out == "" {
		_, err = os.Stdout.Write(body)
		return err
	}
	outPath := *out
	if !filepath.IsAbs(outPath) {
		outPath = filepath.Join(workspaceAbs, outPath)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(outPath, body, 0o644)
}

func selectLKEBuildImageWorkloads(workloads []lkeWorkload, raw string) ([]lkeWorkload, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = "postgres"
	}
	if raw == "all" {
		return append([]lkeWorkload(nil), workloads...), nil
	}
	byKey := map[string]lkeWorkload{}
	for _, workload := range workloads {
		byKey[workload.Key] = workload
	}
	selected := []lkeWorkload{}
	seen := map[string]bool{}
	for _, part := range strings.Split(raw, ",") {
		key := strings.TrimSpace(part)
		if key == "" {
			continue
		}
		if seen[key] {
			continue
		}
		workload, ok := byKey[key]
		if !ok {
			return nil, fmt.Errorf("unknown LKE image workload %q", key)
		}
		selected = append(selected, workload)
		seen[key] = true
	}
	if len(selected) == 0 {
		return nil, errors.New("lke-build-images requires at least one workload")
	}
	return selected, nil
}

func runLKEResolveImages(args []string) error {
	fs := flag.NewFlagSet("lke-resolve-images", flag.ContinueOnError)
	workspace := fs.String("workspace", ".", "workspace root")
	envRoot := fs.String("env-root", "cloud_env/staging/runtime", "normalized environment runtime root")
	registryHost := fs.String("registry-host", "ghcr.io", "container registry host")
	owner := fs.String("owner", "hkt999rtk", "container registry owner or organization")
	out := fs.String("out", "", "write image manifest JSON to this path")
	skipVerify := fs.Bool("skip-verify", false, "skip remote image existence checks")
	if err := fs.Parse(args); err != nil {
		return err
	}
	workspaceAbs, err := filepath.Abs(*workspace)
	if err != nil {
		return err
	}
	env, err := loadLKEImageEnv(workspaceAbs, *envRoot)
	if err != nil {
		return err
	}
	artifacts := []lkeImageArtifact{
		{
			Key:    "postgres",
			Name:   "postgresql",
			EnvKey: "LKE_POSTGRES_IMAGE",
			Image:  firstNonEmpty(os.Getenv("LKE_POSTGRES_IMAGE"), "postgres:16-alpine"),
			Tag:    "16-alpine",
		},
	}
	for _, source := range lkeServiceImageSources() {
		if pinnedImage := firstNonEmpty(os.Getenv(source.EnvKey), env.Values[source.EnvKey]); pinnedImage != "" {
			repoDir := filepath.Join(workspaceAbs, source.RepoPath)
			fullCommit, err := gitOutput(repoDir, "rev-parse", "HEAD")
			if err != nil {
				return fmt.Errorf("resolve %s source commit from %s: %w", source.Key, source.RepoPath, err)
			}
			fullCommit = strings.TrimSpace(fullCommit)
			if err := validatePinnedLKEImage(pinnedImage, *registryHost, *owner, source, fullCommit, *skipVerify); err != nil {
				return err
			}
			artifacts = append(artifacts, lkeImageArtifact{
				Key:          source.Key,
				Name:         source.Name,
				EnvKey:       source.EnvKey,
				Image:        pinnedImage,
				Digest:       strings.TrimSpace(os.Getenv(source.EnvKey + "_DIGEST")),
				SourceRepo:   source.RepoName,
				SourcePath:   source.RepoPath,
				SourceCommit: fullCommit,
			})
			continue
		}
		repoDir := filepath.Join(workspaceAbs, source.RepoPath)
		fullCommit, err := gitOutput(repoDir, "rev-parse", "HEAD")
		if err != nil {
			return fmt.Errorf("resolve %s source commit from %s: %w", source.Key, source.RepoPath, err)
		}
		fullCommit = strings.TrimSpace(fullCommit)
		shortCommit, err := gitOutput(repoDir, "rev-parse", "--short=12", "HEAD")
		if err != nil {
			return fmt.Errorf("resolve %s short commit from %s: %w", source.Key, source.RepoPath, err)
		}
		tag := "sha-" + strings.TrimSpace(shortCommit)
		image := strings.TrimRight(*registryHost, "/") + "/" + strings.Trim(*owner, "/") + "/" + source.RepoName + "/" + source.Name + ":" + tag
		if !*skipVerify {
			if err := inspectLKEImage(image); err != nil {
				return fmt.Errorf("LKE image missing for %s at %s (%s): %s: %w: %v", source.Key, source.RepoPath, fullCommit, image, errLKEImageNotFound, err)
			}
		}
		artifacts = append(artifacts, lkeImageArtifact{
			Key:          source.Key,
			Name:         source.Name,
			EnvKey:       source.EnvKey,
			Image:        image,
			SourceRepo:   source.RepoName,
			SourcePath:   source.RepoPath,
			SourceCommit: fullCommit,
			Tag:          tag,
		})
	}
	manifest := map[string]any{
		"schema":        "rtk-cloud-workspace.lke-image-manifest/v1",
		"generated_at":  time.Now().UTC().Format(time.RFC3339),
		"provider":      "lke",
		"stack":         env.Values["CLOUD_STACK_NAME"],
		"source_commit": strings.TrimSpace(firstNonEmpty(shortGitCommit(workspaceAbs), "unknown")),
		"images":        artifacts,
		"env":           lkeImageArtifactEnv(artifacts),
	}
	body, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	if *out == "" {
		_, err = os.Stdout.Write(body)
		return err
	}
	outPath := *out
	if !filepath.IsAbs(outPath) {
		outPath = filepath.Join(workspaceAbs, outPath)
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	return os.WriteFile(outPath, body, 0o644)
}

func validatePinnedLKEImage(image, registryHost, owner string, source lkeServiceImageSource, fullCommit string, skipVerify bool) error {
	officialRepository := strings.TrimRight(registryHost, "/") + "/" + strings.Trim(owner, "/") + "/" + source.RepoName + "/" + source.Name
	if !strings.HasPrefix(image, officialRepository+":sha-") {
		return nil
	}
	shortCommit := fullCommit
	if len(shortCommit) > 12 {
		shortCommit = shortCommit[:12]
	}
	expected := officialRepository + ":sha-" + shortCommit
	if image != expected {
		return fmt.Errorf("pinned %s image %s does not match source commit %s; use %s or remove the stale override", source.Key, image, fullCommit, expected)
	}
	if !skipVerify {
		if err := inspectLKEImage(image); err != nil {
			return fmt.Errorf("LKE image missing for %s at %s (%s): %s: %w: %v", source.Key, source.RepoPath, fullCommit, image, errLKEImageNotFound, err)
		}
	}
	return nil
}

func loadLKEImageEnv(workspaceAbs, envRoot string) (envroot.Environment, error) {
	resolvedEnvRoot := envRoot
	if !filepath.IsAbs(resolvedEnvRoot) {
		resolvedEnvRoot = filepath.Join(workspaceAbs, resolvedEnvRoot)
	}
	if filepath.Base(resolvedEnvRoot) != "lke" {
		candidate := filepath.Join(resolvedEnvRoot, "lke")
		if _, err := os.Stat(candidate); err == nil {
			resolvedEnvRoot = candidate
		}
	}
	env, err := envroot.Load(resolvedEnvRoot, "")
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			env = envroot.Environment{Values: defaultProvisionEnvValues()}
		} else {
			return envroot.Environment{}, err
		}
	}
	env.Values["CLOUD_PROVIDER"] = "lke"
	return env, nil
}

func lkeImageArtifactEnv(artifacts []lkeImageArtifact) map[string]string {
	out := map[string]string{}
	for _, artifact := range artifacts {
		out[artifact.EnvKey] = artifact.Image
	}
	return out
}

func loadLKEImageManifestDefaults(envRoot string, env map[string]string) error {
	path := filepath.Join(envRoot, "artifacts", "lke-images", "lke-image-manifest.json")
	body, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var manifest struct {
		Env map[string]string `json:"env"`
	}
	if err := json.Unmarshal(body, &manifest); err != nil {
		return fmt.Errorf("read LKE image manifest %s: %w", path, err)
	}
	for _, key := range []string{
		"LKE_POSTGRES_IMAGE",
		"LKE_VIDEO_CLOUD_IMAGE",
		"LKE_ACCOUNT_MANAGER_IMAGE",
		"LKE_CLOUD_ADMIN_IMAGE",
		"LKE_FRONTEND_IMAGE",
		"LKE_CLOUD_LOGGER_IMAGE",
	} {
		if os.Getenv(key) == "" && env[key] == "" && manifest.Env[key] != "" {
			env[key] = manifest.Env[key]
		}
	}
	return nil
}

func lkeEnvValue(env map[string]string, key string) string {
	return firstNonEmpty(os.Getenv(key), env[key])
}

func lkeServiceImageSources() []lkeServiceImageSource {
	return []lkeServiceImageSource{
		{Key: "video-cloud", Name: "video-cloud-api", EnvKey: "LKE_VIDEO_CLOUD_IMAGE", RepoName: "rtk_video_cloud", RepoPath: filepath.Join("repos", "rtk_video_cloud")},
		{Key: "account-manager", Name: "account-manager", EnvKey: "LKE_ACCOUNT_MANAGER_IMAGE", RepoName: "rtk_account_manager", RepoPath: filepath.Join("repos", "rtk_account_manager")},
		{Key: "cloud-admin", Name: "cloud-admin", EnvKey: "LKE_CLOUD_ADMIN_IMAGE", RepoName: "rtk_cloud_admin", RepoPath: filepath.Join("repos", "rtk_cloud_admin")},
		{Key: "frontend", Name: "frontend", EnvKey: "LKE_FRONTEND_IMAGE", RepoName: "rtk_cloud_frontend", RepoPath: filepath.Join("repos", "rtk_cloud_frontend")},
		{Key: "cloud-logger", Name: "rtk-cloud-logger", EnvKey: "LKE_CLOUD_LOGGER_IMAGE", RepoName: "rtk_cloud_logger", RepoPath: filepath.Join("repos", "rtk_cloud_logger")},
	}
}

func shortGitCommit(dir string) string {
	out, err := gitOutput(dir, "rev-parse", "--short=12", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func runLKEProvision(paths provisionPaths, env map[string]string, opts provisionOptions) error {
	return runKubernetesProvision(lkeCloudProvider{}, provisionContext{Paths: paths, Env: env, Opts: opts})
}

func lkePreflight(paths provisionPaths, env map[string]string) error {
	if env["CLOUD_PROVIDER"] != "lke" {
		return fmt.Errorf("LKE provision requires CLOUD_PROVIDER=lke, got %s", env["CLOUD_PROVIDER"])
	}
	kubectl := lkeKubectl()
	if _, err := exec.LookPath(kubectl); err != nil && filepath.Base(kubectl) == kubectl {
		return fmt.Errorf("%s is required for LKE provision", kubectl)
	}
	helm := lkeHelm()
	if _, err := exec.LookPath(helm); err != nil && filepath.Base(helm) == helm {
		return fmt.Errorf("%s is required for LKE OpenBao provision", helm)
	}
	if err := runKubectl("version", "--client"); err != nil {
		return err
	}
	return nil
}

func lkePlan(env map[string]string, opts provisionOptions) {
	fmt.Fprintln(os.Stdout, "LKE target:")
	fmt.Fprintf(os.Stdout, "- stack: %s\n", env["CLOUD_STACK_NAME"])
	fmt.Fprintf(os.Stdout, "- region: %s\n", env["CLOUD_REGION"])
	fmt.Fprintln(os.Stdout, "- namespaces:")
	for _, ns := range lkeNamespaces(env) {
		fmt.Fprintf(os.Stdout, "  - %s=%s\n", ns.Key, ns.Name)
	}
	fmt.Fprintln(os.Stdout, "- public edge: external HAProxy VM :443/:8883 -> LKE node private IP NodePorts")
	fmt.Fprintf(os.Stdout, "- public HTTP :80 is not exposed; TLS issuance uses %s DNS-01\n", env["DNS_ADAPTER"])
	fmt.Fprintln(os.Stdout, "- public MQTT: HAProxy TCP passthrough :8883 -> EMQX/MQTT NodePort")
	fmt.Fprintln(os.Stdout, "- public TURN: external coturn VM data-plane exception, not HAProxy-backed")
	fmt.Fprintf(os.Stdout, "  - coturn_vms: count=%d type=%s image=%s ports=3478/udp,3478/tcp relay_udp=%s-%s\n",
		lkeCoturnVMCount(env),
		firstNonEmpty(os.Getenv("LKE_COTURN_VM_TYPE"), env["LKE_COTURN_VM_TYPE"], "g6-nanode-1"),
		firstNonEmpty(os.Getenv("LKE_COTURN_VM_IMAGE"), env["LKE_COTURN_VM_IMAGE"], "linode/ubuntu24.04"),
		firstNonEmpty(os.Getenv("LKE_COTURN_MIN_PORT"), env["LKE_COTURN_MIN_PORT"], "49152"),
		firstNonEmpty(os.Getenv("LKE_COTURN_MAX_PORT"), env["LKE_COTURN_MAX_PORT"], "65535"),
	)
	for index := 1; index <= lkeCoturnVMCount(env); index++ {
		nodeEnv := lkeCoturnVMEnvForIndex(env, index)
		fmt.Fprintf(os.Stdout, "    - coturn_vm: name=%s label=%s domain=%s\n",
			lkeCoturnVMName(nodeEnv),
			lkeCoturnVMLabel(nodeEnv),
			lkeCoturnDomain(nodeEnv))
	}
	fmt.Fprintf(os.Stdout, "  - turn_registry: domain=%s registrar_node_id=%s\n", lkeTurnRegistryPublicDomain(env), lkeCoturnVMName(env))
	fmt.Fprintln(os.Stdout, "- secrets: OpenBao standalone/AppRole for environment PKI; certissuer delegates device/app signing to OpenBao and Kubernetes Secrets do not carry CA private keys")
	fmt.Fprintln(os.Stdout, "- deploy images:")
	for _, workload := range lkeWorkloads(env) {
		status := "TODO"
		if workload.Image != "" {
			status = workload.Image
		}
		fmt.Fprintf(os.Stdout, "  - %s: %s\n", workload.EnvKey, status)
	}
	lkePrintCapacityPlan(env, opts)
}

func lkeApplyBase(env map[string]string) error {
	for _, ns := range lkeNamespaces(env) {
		manifest, err := renderK8SNamespaceManifest(ns.Name, env["CLOUD_STACK_NAME"])
		if err != nil {
			return err
		}
		if err := kubectlApply(manifest); err != nil {
			return err
		}
	}
	for _, ns := range lkeNamespaces(env) {
		if err := lkeApplyImagePullSecret(env, ns.Name); err != nil {
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
	if err := kubectlApply(config); err != nil {
		return err
	}
	if os.Getenv("RUNTIME_COVERAGE_SHARED_CLUSTER") == "1" {
		fmt.Fprintln(os.Stdout, "[lke] shared runtime-coverage cluster: preserving cluster-scoped metrics-server")
		return nil
	}
	return lkeInstallMetricsServer()
}

func lkeInstallMetricsServer() error {
	version := firstNonEmpty(os.Getenv("LKE_METRICS_SERVER_VERSION"), "v0.8.1")
	if err := runKubectl("apply", "-f", "https://github.com/kubernetes-sigs/metrics-server/releases/download/"+version+"/components.yaml"); err != nil {
		return err
	}
	return runKubectl("-n", "kube-system", "rollout", "status", "deployment/metrics-server", "--timeout", firstNonEmpty(os.Getenv("LKE_METRICS_SERVER_ROLLOUT_TIMEOUT"), "5m"))
}

func lkeApplyImagePullSecret(env map[string]string, namespace string) error {
	username, token := lkeGHCRPullCredentials(env)
	if username == "" || token == "" {
		return nil
	}
	secret, err := newK8SDockerConfigSecretObject(namespace, lkeImagePullSecretName(env), username, token)
	if err != nil {
		return err
	}
	return applyKubernetesObjectJSON(secret)
}

func lkeGHCRPullCredentials(env map[string]string) (string, string) {
	username := firstNonEmpty(os.Getenv("GHCR_PULL_USERNAME"), env["GHCR_PULL_USERNAME"])
	token := firstNonEmpty(os.Getenv("GHCR_PULL_TOKEN"), env["GHCR_PULL_TOKEN"])
	if username != "" && token != "" {
		return username, token
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return username, token
	}
	return firstNonEmpty(username, envFileValue(filepath.Join(home, ".env"), "GHCR_PULL_USERNAME")),
		firstNonEmpty(token, envFileValue(filepath.Join(home, ".env"), "GHCR_PULL_TOKEN"))
}

type lkePublicHTTPSRoute struct {
	Host        string
	Service     string
	Namespace   string
	ServicePort int
	TargetPort  int
	Protocol    string
}

func lkeApplyPublicHTTPS(paths provisionPaths, env map[string]string, opts provisionOptions) error {
	if err := lkeValidatePublicEdge(env); err != nil {
		return err
	}
	routes := lkePublicHTTPSRoutes(env)
	if len(routes) == 0 {
		return errors.New("no public HTTPS routes configured")
	}
	if err := kubectlApply(lkeIngressNamespaceManifest(env)); err != nil {
		return err
	}
	if err := lkeInstallIngressNginx(env); err != nil {
		return err
	}
	hosts := lkePublicHTTPSHosts(routes)
	certPEM, keyPEM, hasTLS, err := lkeExistingPublicHTTPSTLSSecretMaterialCoversHosts(env, hosts)
	if err != nil {
		return err
	}
	if hasTLS {
		fmt.Fprintf(os.Stderr, "[lke-provision] reusing existing public TLS secret %s/%s\n", lkeIngressNamespace(env), lkePublicHTTPSTLSSecretName(env))
		if err := lkeWritePublicHTTPSCertificateCache(paths, env, hosts, certPEM, keyPEM); err != nil {
			return err
		}
	} else {
		cachedCertPEM, cachedKeyPEM, ok, err := lkeLoadPublicHTTPSCertificateCache(paths, env, hosts)
		if err != nil {
			return err
		}
		if ok {
			fmt.Fprintf(os.Stderr, "[lke-provision] restoring cached public TLS certificate %s\n", lkePublicHTTPSCertificateCacheDir(paths, env))
			certPEM, keyPEM = cachedCertPEM, cachedKeyPEM
		} else {
			certPEM, keyPEM, err = lkeIssuePublicHTTPSCertificate(paths, env, opts, hosts)
			if err != nil {
				return err
			}
			if err := lkeWritePublicHTTPSCertificateCache(paths, env, hosts, certPEM, keyPEM); err != nil {
				return err
			}
		}
		if err := kubectlApply(lkePublicHTTPSTLSSecretManifest(env, certPEM, keyPEM)); err != nil {
			return err
		}
	}
	if err := lkeCopyExistingDeviceMTLSAppCASecret(env); err != nil {
		return err
	}
	for _, manifest := range lkePublicHTTPSBridgeServiceManifests(env, routes) {
		if err := kubectlApply(manifest); err != nil {
			return err
		}
	}
	for _, manifest := range lkePublicHTTPSIngressManifests(env, routes) {
		if err := kubectlApply(manifest); err != nil {
			return err
		}
	}
	for _, manifest := range lkePublicHTTPSNetworkPolicyManifests(env, routes) {
		if err := kubectlApply(manifest); err != nil {
			return err
		}
	}
	if err := lkeApplyPublicMQTTNodePort(env); err != nil {
		return err
	}
	edge, err := lkeEnsureExternalHAProxyEdge(paths, env, opts)
	if err != nil {
		return err
	}
	if err := lkeSyncPublicHTTPSDNS(paths, env, opts, hosts, edge.PublicIP); err != nil {
		return err
	}
	coturnVMs, err := lkeEnsureExternalCoturnVMs(paths, env, opts)
	if err != nil {
		return err
	}
	return lkeSyncCoturnVMsDNS(paths, env, opts, coturnVMs)
}

func lkeSyncPublicHTTPSDNS(paths provisionPaths, env map[string]string, opts provisionOptions, hosts []string, ip string) error {
	if len(hosts) == 0 {
		return nil
	}
	ttl := envIntDefault("DNS_RECORD_TTL", 600)
	records := make([]dnsRecordSet, 0, len(hosts))
	for _, host := range hosts {
		records = append(records, dnsRecordSet{Name: host, Type: "A", Values: []string{ip}, TTL: ttl, Purpose: "public-edge"})
	}
	return syncDNSRecords(paths, env, records)
}

func lkeRunHostTasks(hosts []string, concurrency int, task func(string) error) error {
	if len(hosts) == 0 {
		return nil
	}
	if concurrency < 1 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)
	errCh := make(chan error, len(hosts))
	for _, host := range hosts {
		host := host
		go func() {
			sem <- struct{}{}
			err := task(host)
			<-sem
			errCh <- err
		}()
	}
	var firstErr error
	for range hosts {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func lkeIngressNamespace(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_NAMESPACE_INGRESS"), lkeName(firstNonEmpty(env["CLOUD_STACK_NAME"], "video-cloud-staging"))+"-ingress")
}

func lkeIngressNamespaceManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Namespace
metadata:
  name: %s
  labels:
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
`, lkeIngressNamespace(env), env["CLOUD_STACK_NAME"])
}

func lkeInstallIngressNginx(env map[string]string) error {
	ns := lkeIngressNamespace(env)
	args := []string{
		"upgrade", "--install", "ingress-nginx", "ingress-nginx",
		"--repo", "https://kubernetes.github.io/ingress-nginx",
		"--namespace", ns,
		"--create-namespace",
		"--set", "controller.service.type=NodePort",
		"--set", "controller.service.ports.https=443",
		"--set", "controller.service.targetPorts.https=https",
		"--set", "controller.service.nodePorts.https=" + strconv.Itoa(lkeIngressHTTPSNodePort(env)),
		"--set", "controller.service.enableHttp=false",
		"--set", "controller.allowSnippetAnnotations=true",
		"--set", "controller.config.annotations-risk-level=Critical",
		"--set", "controller.ingressClassResource.default=false",
		"--set", "controller.replicaCount=" + lkeIngressReplicas(env),
		"--set", "controller.resources.requests.cpu=" + firstNonEmpty(os.Getenv("LKE_INGRESS_REQUEST_CPU"), env["LKE_INGRESS_REQUEST_CPU"], "500m"),
		"--set", "controller.resources.requests.memory=" + firstNonEmpty(os.Getenv("LKE_INGRESS_REQUEST_MEMORY"), env["LKE_INGRESS_REQUEST_MEMORY"], "512Mi"),
		"--set", "controller.resources.limits.memory=" + firstNonEmpty(os.Getenv("LKE_INGRESS_LIMIT_MEMORY"), env["LKE_INGRESS_LIMIT_MEMORY"], "1Gi"),
		"--set-json", `controller.topologySpreadConstraints=[{"maxSkew":1,"topologyKey":"kubernetes.io/hostname","whenUnsatisfiable":"ScheduleAnyway","labelSelector":{"matchLabels":{"app.kubernetes.io/name":"ingress-nginx","app.kubernetes.io/component":"controller"}}}]`,
	}
	timeout := envDurationDefault("LKE_INGRESS_HELM_TIMEOUT", 5*time.Minute)
	if err := runHelmWithTimeout(timeout, args...); err != nil {
		return err
	}
	return runKubectl("-n", ns, "rollout", "status", "deployment/ingress-nginx-controller", "--timeout", firstNonEmpty(os.Getenv("LKE_INGRESS_ROLLOUT_TIMEOUT"), "5m"))
}

func lkeIngressReplicas(env map[string]string) string {
	raw := strings.TrimSpace(firstNonEmpty(env["INGRESS_EFFECTIVE_REPLICAS"], os.Getenv("LKE_INGRESS_REPLICAS"), env["LKE_INGRESS_REPLICAS"], "1"))
	replicas, err := strconv.Atoi(raw)
	if err != nil || replicas < 1 {
		return "1"
	}
	return strconv.Itoa(replicas)
}

func lkePublicHTTPSRoutes(env map[string]string) []lkePublicHTTPSRoute {
	routes := lkePublicHTTPSBaseRoutes(env)
	if logger := lkeCloudLoggerRoute(env); logger.Host != "" {
		routes = append(routes, logger)
	}
	return routes
}

func lkePublicHTTPSBaseRoutes(env map[string]string) []lkePublicHTTPSRoute {
	videoNS := lkeNamespaceName(env, "video-cloud")
	videoDomain := env["VIDEO_CLOUD_DOMAIN"]
	return []lkePublicHTTPSRoute{
		{Host: videoDomain, Namespace: videoNS, Service: "video-cloud-api", ServicePort: 80, TargetPort: envIntDefault("LKE_VIDEO_CLOUD_PORT", 8080)},
		{Host: firstNonEmpty(os.Getenv("LKE_DEVICE_DOMAIN"), env["VIDEO_CLOUD_DEVICE_DOMAIN"], "device."+videoDomain), Namespace: videoNS, Service: "video-cloud-api", ServicePort: 80, TargetPort: envIntDefault("LKE_VIDEO_CLOUD_PORT", 8080)},
		{Host: env["VIDEO_CLOUD_CERTISSUER_DOMAIN"], Namespace: videoNS, Service: "certissuer", ServicePort: 9443, TargetPort: 9443, Protocol: "HTTPS"},
		{Host: lkeTurnRegistryPublicDomain(env), Namespace: videoNS, Service: "video-cloud-turnregistry", ServicePort: 18190, TargetPort: 18190},
		{Host: env["ACCOUNT_MANAGER_DOMAIN"], Namespace: lkeNamespaceName(env, "account-manager"), Service: "account-manager", ServicePort: 80, TargetPort: envIntDefault("LKE_ACCOUNT_MANAGER_PORT", 8080)},
		{Host: lkePaymentSimulatorPublicDomain(env), Namespace: lkeNamespaceName(env, "account-manager"), Service: "payment-simulator", ServicePort: 80, TargetPort: 8081},
		{Host: env["CLOUD_ADMIN_DOMAIN"], Namespace: lkeNamespaceName(env, "admin"), Service: "cloud-admin", ServicePort: 80, TargetPort: envIntDefault("LKE_CLOUD_ADMIN_PORT", 8080)},
		{Host: firstNonEmpty(os.Getenv("LKE_FRONTEND_DOMAIN"), env["FRONTEND_DOMAIN"], "frontend."+videoDomain), Namespace: lkeNamespaceName(env, "frontend"), Service: "frontend", ServicePort: 80, TargetPort: envIntDefault("LKE_FRONTEND_PORT", 8080)},
	}
}

func lkeCloudLoggerRoute(env map[string]string) lkePublicHTTPSRoute {
	host := env["CLOUD_LOGGER_DOMAIN"]
	if host == "" {
		return lkePublicHTTPSRoute{}
	}
	namespace := lkeNamespaceName(env, "logger")
	service := firstNonEmpty(os.Getenv("LKE_CLOUD_LOGGER_SERVICE"), "cloud-logger")
	out, err := kubectlCombinedOutput(nil, "-n", namespace, "get", "service", service, "-o", "name")
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return lkePublicHTTPSRoute{}
	}
	servicePort := envIntDefault("LKE_CLOUD_LOGGER_SERVICE_PORT", 80)
	targetPortDefault := envIntDefault("LKE_CLOUD_LOGGER_PORT", 18090)
	return lkePublicHTTPSRoute{Host: host, Namespace: namespace, Service: service, ServicePort: servicePort, TargetPort: envIntDefault("LKE_CLOUD_LOGGER_TARGET_PORT", targetPortDefault)}
}

func lkePublicHTTPSBridgeServiceManifests(env map[string]string, routes []lkePublicHTTPSRoute) []string {
	manifests := []string{}
	seen := map[string]bool{}
	for _, route := range routes {
		name := lkePublicHTTPSBridgeServiceName(env, route)
		if seen[name] {
			continue
		}
		seen[name] = true
		manifests = append(manifests, fmt.Sprintf(`apiVersion: v1
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
  type: ExternalName
  externalName: %s.%s.svc.cluster.local
  ports:
    - name: %s
      port: %d
      protocol: TCP
`, name, lkeIngressNamespace(env), name, env["CLOUD_STACK_NAME"], route.Service, route.Namespace, strings.ToLower(firstNonEmpty(route.Protocol, "HTTP")), route.ServicePort))
	}
	return manifests
}

func lkePublicHTTPSBridgeServiceName(env map[string]string, route lkePublicHTTPSRoute) string {
	stackPrefix := lkeName(firstNonEmpty(env["CLOUD_STACK_NAME"], "video-cloud-staging")) + "-"
	namespaceKey := strings.TrimPrefix(route.Namespace, stackPrefix)
	return lkeName("public-" + route.Service + "-" + namespaceKey)
}

func lkePublicHTTPSHosts(routes []lkePublicHTTPSRoute) []string {
	hosts := []string{}
	seen := map[string]bool{}
	for _, route := range routes {
		host := strings.TrimSpace(route.Host)
		if host == "" || seen[host] {
			continue
		}
		seen[host] = true
		hosts = append(hosts, host)
	}
	sort.Strings(hosts)
	return hosts
}

func lkeIssuePublicHTTPSCertificate(paths provisionPaths, env map[string]string, opts provisionOptions, hosts []string) (string, string, error) {
	if len(hosts) == 0 {
		return "", "", errors.New("public HTTPS certificate requires at least one hostname")
	}
	if _, _, _, err := selectedDNSAdapter(paths, env); err != nil {
		return "", "", err
	}
	defer func() {
		if err := cleanupRecordedDNSChallenges(paths, env); err != nil {
			fmt.Fprintf(os.Stderr, "[dns] challenge cleanup warning: %v\n", err)
		}
	}()
	workDir, err := os.MkdirTemp("", "rtk-public-https-*")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(workDir)
	hookBinary := os.Getenv("RTK_CLOUD_DNS_HOOK_BINARY")
	if hookBinary == "" {
		hookBinary, err = os.Executable()
		if err != nil {
			return "", "", err
		}
	}
	authHook := filepath.Join(workDir, "dns-auth.sh")
	cleanupHook := filepath.Join(workDir, "dns-cleanup.sh")
	if err := os.WriteFile(authHook, []byte(certbotDNSHookScript(hookBinary, paths.EnvRoot, paths.OperatorEnv, "present")), 0o700); err != nil {
		return "", "", err
	}
	if err := os.WriteFile(cleanupHook, []byte(certbotDNSHookScript(hookBinary, paths.EnvRoot, paths.OperatorEnv, "cleanup")), 0o700); err != nil {
		return "", "", err
	}
	configDir := filepath.Join(paths.EnvRoot, "state", "acme")
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return "", "", err
	}
	certbot := firstNonEmpty(os.Getenv("RTK_CLOUD_CERTBOT"), "certbot")
	args := []string{
		"certonly",
		"--manual",
		"--preferred-challenges", "dns",
		"--manual-auth-hook", authHook,
		"--manual-cleanup-hook", cleanupHook,
		"--non-interactive",
		"--agree-tos",
		"--config-dir", configDir,
		"--work-dir", filepath.Join(workDir, "work"),
		"--logs-dir", filepath.Join(workDir, "logs"),
	}
	if server := os.Getenv("LKE_PUBLIC_HTTPS_ACME_SERVER"); server != "" {
		args = append(args, "--server", server)
	}
	if email := os.Getenv("LKE_PUBLIC_HTTPS_ISSUE_EMAIL"); email != "" {
		args = append(args, "--email", email)
	} else {
		args = append(args, "--register-unsafely-without-email")
	}
	for _, host := range hosts {
		args = append(args, "-d", host)
	}
	attempts := envIntDefault("LKE_PUBLIC_HTTPS_ACME_ATTEMPTS", 3)
	retryDelay := time.Duration(envIntDefault("LKE_PUBLIC_HTTPS_ACME_RETRY_SECONDS", 10)) * time.Second
	var certbotErr error
	attemptsUsed := 0
	for attempt := 1; attempt <= attempts; attempt++ {
		attemptsUsed = attempt
		var certbotOutput bytes.Buffer
		cmd := exec.Command(certbot, args...)
		cmd.Env = os.Environ()
		cmd.Stdout = io.MultiWriter(os.Stdout, &certbotOutput)
		cmd.Stderr = io.MultiWriter(os.Stderr, &certbotOutput)
		certbotErr = cmd.Run()
		if certbotErr == nil {
			break
		}
		output := certbotOutput.String()
		accountMissing := lkeACMEAccountMissing(output)
		if accountMissing && attempt < attempts {
			fmt.Fprintf(os.Stderr, "[lke] ACME issuance failed, retrying with the registered account attempt %d/%d: %v\n", attempt+1, attempts, certbotErr)
			time.Sleep(retryDelay)
			continue
		}
		break
	}
	if certbotErr != nil {
		return "", "", fmt.Errorf("public HTTPS ACME DNS-01 issuance failed after %d attempt(s): %w", attemptsUsed, certbotErr)
	}
	liveDir := filepath.Join(configDir, "live", hosts[0])
	certPEM, err := os.ReadFile(filepath.Join(liveDir, "fullchain.pem"))
	if err != nil {
		return "", "", err
	}
	keyPEM, err := os.ReadFile(filepath.Join(liveDir, "privkey.pem"))
	if err != nil {
		return "", "", err
	}
	if len(bytes.TrimSpace(certPEM)) == 0 || len(bytes.TrimSpace(keyPEM)) == 0 {
		return "", "", errors.New("public HTTPS ACME DNS-01 issuance produced an empty certificate or key")
	}
	return string(certPEM), string(keyPEM), nil
}

func lkeACMEAccountMissing(output string) bool {
	return strings.Contains(output, "accountDoesNotExist") ||
		(strings.Contains(output, "Unable to validate JWS") && strings.Contains(output, "not found"))
}

func lkeExistingPublicHTTPSTLSSecretCoversHosts(env map[string]string, hosts []string) (bool, error) {
	_, _, ok, err := lkeExistingPublicHTTPSTLSSecretMaterialCoversHosts(env, hosts)
	return ok, err
}

func lkeExistingPublicHTTPSTLSSecretMaterialCoversHosts(env map[string]string, hosts []string) (string, string, bool, error) {
	certOut, err := kubectlCombinedOutput(nil, "-n", lkeIngressNamespace(env), "get", "secret", lkePublicHTTPSTLSSecretName(env), "-o", "jsonpath={.data.tls\\.crt}")
	if err != nil {
		return "", "", false, nil
	}
	keyOut, err := kubectlCombinedOutput(nil, "-n", lkeIngressNamespace(env), "get", "secret", lkePublicHTTPSTLSSecretName(env), "-o", "jsonpath={.data.tls\\.key}")
	if err != nil {
		return "", "", false, nil
	}
	certEncoded := strings.TrimSpace(string(certOut))
	keyEncoded := strings.TrimSpace(string(keyOut))
	if certEncoded == "" || keyEncoded == "" {
		return "", "", false, nil
	}
	certPEM, err := base64.StdEncoding.DecodeString(certEncoded)
	if err != nil {
		return "", "", false, fmt.Errorf("decode existing public TLS secret certificate: %w", err)
	}
	keyPEM, err := base64.StdEncoding.DecodeString(keyEncoded)
	if err != nil {
		return "", "", false, fmt.Errorf("decode existing public TLS secret key: %w", err)
	}
	ok, err := lkeCertificateCoversHosts(certPEM, hosts, lkePublicHTTPSMinValidUntil())
	if err != nil || !ok {
		return "", "", ok, err
	}
	return string(certPEM), string(keyPEM), true, nil
}

func lkePublicHTTPSMinValidUntil() time.Time {
	return time.Now().Add(time.Duration(envIntDefault("LKE_PUBLIC_HTTPS_MIN_VALID_DAYS", 30)) * 24 * time.Hour)
}

func lkeCertificateCoversHosts(certPEM []byte, hosts []string, minValidUntil time.Time) (bool, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return false, nil
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return false, fmt.Errorf("parse existing public TLS certificate: %w", err)
	}
	if cert.NotAfter.Before(minValidUntil) {
		return false, nil
	}
	for _, host := range hosts {
		if host == "" {
			continue
		}
		if err := cert.VerifyHostname(host); err != nil {
			return false, nil
		}
	}
	return true, nil
}

type lkePublicHTTPSCertificateCacheMetadata struct {
	SecretName    string   `json:"secret_name"`
	Namespace     string   `json:"namespace"`
	Hosts         []string `json:"hosts"`
	NotBefore     string   `json:"not_before,omitempty"`
	NotAfter      string   `json:"not_after,omitempty"`
	CachedAt      string   `json:"cached_at"`
	MinValidDays  int      `json:"min_valid_days"`
	Certificate   string   `json:"certificate"`
	PrivateKey    string   `json:"private_key"`
	CacheContract string   `json:"cache_contract"`
}

func lkePublicHTTPSCertificateCacheDir(paths provisionPaths, env map[string]string) string {
	return filepath.Join(paths.EnvRoot, "state", "public-https", lkePublicHTTPSTLSSecretName(env))
}

func lkeLoadPublicHTTPSCertificateCache(paths provisionPaths, env map[string]string, hosts []string) (string, string, bool, error) {
	cacheDir := lkePublicHTTPSCertificateCacheDir(paths, env)
	certPEM, err := os.ReadFile(filepath.Join(cacheDir, "fullchain.pem"))
	if errors.Is(err, os.ErrNotExist) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	keyPEM, err := os.ReadFile(filepath.Join(cacheDir, "privkey.pem"))
	if errors.Is(err, os.ErrNotExist) {
		return "", "", false, nil
	}
	if err != nil {
		return "", "", false, err
	}
	if len(bytes.TrimSpace(certPEM)) == 0 || len(bytes.TrimSpace(keyPEM)) == 0 {
		return "", "", false, nil
	}
	ok, err := lkeCertificateCoversHosts(certPEM, hosts, lkePublicHTTPSMinValidUntil())
	if err != nil || !ok {
		return "", "", ok, err
	}
	return string(certPEM), string(keyPEM), true, nil
}

func lkeWritePublicHTTPSCertificateCache(paths provisionPaths, env map[string]string, hosts []string, certPEM, keyPEM string) error {
	if len(bytes.TrimSpace([]byte(certPEM))) == 0 || len(bytes.TrimSpace([]byte(keyPEM))) == 0 {
		return errors.New("public HTTPS certificate cache requires non-empty certificate and key")
	}
	cacheDir := lkePublicHTTPSCertificateCacheDir(paths, env)
	if err := os.MkdirAll(cacheDir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "fullchain.pem"), []byte(certPEM), 0o600); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "privkey.pem"), []byte(keyPEM), 0o600); err != nil {
		return err
	}
	meta := lkePublicHTTPSCertificateCacheMetadata{
		SecretName:    lkePublicHTTPSTLSSecretName(env),
		Namespace:     lkeIngressNamespace(env),
		Hosts:         append([]string(nil), hosts...),
		CachedAt:      time.Now().UTC().Format(time.RFC3339),
		MinValidDays:  envIntDefault("LKE_PUBLIC_HTTPS_MIN_VALID_DAYS", 30),
		Certificate:   "fullchain.pem",
		PrivateKey:    "privkey.pem",
		CacheContract: "reuse this certificate until it no longer covers all public HTTPS hosts or is within min_valid_days of expiration",
	}
	if cert, err := parseFirstCertificatePEM([]byte(certPEM)); err == nil && cert != nil {
		meta.NotBefore = cert.NotBefore.UTC().Format(time.RFC3339)
		meta.NotAfter = cert.NotAfter.UTC().Format(time.RFC3339)
	}
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(cacheDir, "metadata.json"), append(raw, '\n'), 0o600); err != nil {
		return err
	}
	return nil
}

func parseFirstCertificatePEM(certPEM []byte) (*x509.Certificate, error) {
	block, _ := pem.Decode(certPEM)
	if block == nil {
		return nil, nil
	}
	return x509.ParseCertificate(block.Bytes)
}

func lkePublicHTTPSTLSSecretName(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_PUBLIC_HTTPS_TLS_SECRET"), "video-cloud-staging-public-tls")
}

func lkePublicHTTPSTLSSecretManifest(env map[string]string, certPEM, keyPEM string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/name: %s
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
type: kubernetes.io/tls
data:
  tls.crt: %s
  tls.key: %s
`, lkePublicHTTPSTLSSecretName(env), lkeIngressNamespace(env), lkePublicHTTPSTLSSecretName(env), env["CLOUD_STACK_NAME"], base64.StdEncoding.EncodeToString([]byte(certPEM)), base64.StdEncoding.EncodeToString([]byte(keyPEM)))
}

func lkeDeviceMTLSAppCASecretName(env map[string]string) string {
	return lkeName(firstNonEmpty(env["CLOUD_STACK_NAME"], "video-cloud-staging")) + "-app-client-ca"
}

func lkeDeviceMTLSAppCASecretManifest(env map[string]string, appCACertPEM string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/name: %s
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
type: Opaque
stringData:
  ca.crt: %q
`, lkeDeviceMTLSAppCASecretName(env), lkeIngressNamespace(env), lkeDeviceMTLSAppCASecretName(env), env["CLOUD_STACK_NAME"], appCACertPEM)
}

func lkeCopyExistingDeviceMTLSAppCASecret(env map[string]string) error {
	out, err := kubectlCombinedOutput(nil, "-n", lkeNamespaceName(env, "video-cloud"), "get", "secret", "certissuer-runtime", "-o", "json")
	if err != nil || strings.TrimSpace(string(out)) == "" {
		return nil
	}
	var secret struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(out, &secret); err != nil {
		return fmt.Errorf("decode existing certissuer-runtime secret for device mTLS ingress: %w", err)
	}
	rootCA, err := decodeSecretPEM(secret.Data, "root-ca.crt")
	if err != nil {
		return err
	}
	deviceCA, err := decodeSecretPEM(secret.Data, "device-ca.crt")
	if err != nil {
		return err
	}
	appCA, err := decodeSecretPEM(secret.Data, "app-ca.crt")
	if err != nil {
		return err
	}
	if strings.TrimSpace(rootCA) == "" || strings.TrimSpace(deviceCA) == "" || strings.TrimSpace(appCA) == "" {
		return nil
	}
	return kubectlApply(lkeDeviceMTLSAppCASecretManifest(env, lkeClientCABundle(rootCA, deviceCA, appCA)))
}

func decodeSecretPEM(data map[string]string, key string) (string, error) {
	raw := strings.TrimSpace(data[key])
	if raw == "" {
		return "", nil
	}
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", fmt.Errorf("decode existing %s for device mTLS ingress: %w", key, err)
	}
	return string(decoded), nil
}

func lkeClientCABundle(rootCA string, deviceCA string, appCA string) string {
	parts := []string{}
	for _, cert := range []string{rootCA, deviceCA, appCA} {
		cert = strings.TrimSpace(cert)
		if cert != "" {
			parts = append(parts, cert+"\n")
		}
	}
	return strings.Join(parts, "")
}

func writeLKEDeviceClientCABundle(paths provisionPaths, rootCA string, deviceCA string, appCA string) error {
	if strings.TrimSpace(rootCA) == "" || strings.TrimSpace(deviceCA) == "" || strings.TrimSpace(appCA) == "" {
		return errors.New("root, device, and app CA certificates are required for the device client CA bundle")
	}
	dir := filepath.Join(paths.EnvRoot, "state", "secrets")
	path := filepath.Join(dir, "device-client-ca-bundle.pem")
	if err := writeSensitiveFile(path, lkeClientCABundle(rootCA, deviceCA, appCA)); err != nil {
		return fmt.Errorf("write device client CA bundle: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return fmt.Errorf("secure device client CA bundle directory: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("secure device client CA bundle: %w", err)
	}
	return nil
}

func lkePublicHTTPSIngressManifests(env map[string]string, routes []lkePublicHTTPSRoute) []string {
	httpRoutes := []lkePublicHTTPSRoute{}
	deviceMTLSRoutes := []lkePublicHTTPSRoute{}
	httpsRoutes := []lkePublicHTTPSRoute{}
	for _, route := range routes {
		if strings.EqualFold(route.Protocol, "HTTPS") {
			httpsRoutes = append(httpsRoutes, route)
			continue
		}
		if lkeIsDeviceMTLSRoute(env, route) {
			deviceMTLSRoutes = append(deviceMTLSRoutes, route)
			continue
		}
		httpRoutes = append(httpRoutes, route)
	}
	manifests := []string{}
	if len(httpRoutes) > 0 {
		manifests = append(manifests, lkePublicHTTPSIngressManifest(env, "video-cloud-staging-public", httpRoutes, "", ""))
	}
	if len(deviceMTLSRoutes) > 0 {
		manifests = append(manifests, lkePublicHTTPSIngressManifest(env, "video-cloud-staging-device-mtls", deviceMTLSRoutes, "", lkeDeviceMTLSIngressAnnotations(env)))
	}
	if len(httpsRoutes) > 0 {
		manifests = append(manifests, lkePublicHTTPSIngressManifest(env, "video-cloud-staging-certissuer", httpsRoutes, "HTTPS", ""))
	}
	return manifests
}

func lkeIsDeviceMTLSRoute(env map[string]string, route lkePublicHTTPSRoute) bool {
	videoDomain := env["VIDEO_CLOUD_DOMAIN"]
	deviceHost := firstNonEmpty(os.Getenv("LKE_DEVICE_DOMAIN"), env["VIDEO_CLOUD_DEVICE_DOMAIN"], "device."+videoDomain)
	return route.Host != "" && route.Host == deviceHost && route.Service == "video-cloud-api"
}

func lkeDeviceMTLSIngressAnnotations(env map[string]string) string {
	return fmt.Sprintf(`    nginx.ingress.kubernetes.io/auth-tls-secret: %q
    nginx.ingress.kubernetes.io/auth-tls-verify-client: "on"
    nginx.ingress.kubernetes.io/auth-tls-verify-depth: "2"
    nginx.ingress.kubernetes.io/configuration-snippet: |
      proxy_set_header X-Client-Verify $ssl_client_verify;
      proxy_set_header X-Client-S-DN $ssl_client_s_dn_legacy;
      proxy_set_header X-Client-Cert $ssl_client_escaped_cert;
`, lkeIngressNamespace(env)+"/"+lkeDeviceMTLSAppCASecretName(env))
}

func lkePublicHTTPSIngressManifest(env map[string]string, name string, routes []lkePublicHTTPSRoute, backendProtocol string, extraAnnotations string) string {
	var rules strings.Builder
	for _, route := range routes {
		if route.Host == "" {
			continue
		}
		fmt.Fprintf(&rules, `    - host: %s
      http:
        paths:
          - path: /
            pathType: Prefix
            backend:
              service:
                name: %s
                port:
                  number: %d
`, route.Host, lkePublicHTTPSBridgeServiceName(env, route), route.ServicePort)
	}
	backendAnnotation := ""
	if backendProtocol != "" {
		backendAnnotation = fmt.Sprintf("    nginx.ingress.kubernetes.io/backend-protocol: %q\n", backendProtocol)
	}
	annotations := backendAnnotation + extraAnnotations
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: Ingress
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/name: %s
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
  annotations:
    nginx.ingress.kubernetes.io/ssl-redirect: "false"
    nginx.ingress.kubernetes.io/force-ssl-redirect: "false"
    nginx.ingress.kubernetes.io/proxy-connect-timeout: "60"
    nginx.ingress.kubernetes.io/proxy-read-timeout: "3600"
    nginx.ingress.kubernetes.io/proxy-send-timeout: "3600"
    nginx.ingress.kubernetes.io/proxy-body-size: "20m"
%s
spec:
  ingressClassName: nginx
  tls:
    - secretName: video-cloud-staging-public-tls
      hosts:
%s
  rules:
%s`, name, lkeIngressNamespace(env), name, env["CLOUD_STACK_NAME"], annotations, lkePublicHTTPSTLSHostsYAML(routes), rules.String())
}

func lkePublicHTTPSTLSHostsYAML(routes []lkePublicHTTPSRoute) string {
	var b strings.Builder
	for _, host := range lkePublicHTTPSHosts(routes) {
		fmt.Fprintf(&b, "        - %s\n", host)
	}
	return b.String()
}

func lkePublicHTTPSNetworkPolicyManifests(env map[string]string, routes []lkePublicHTTPSRoute) []string {
	namespaces := uniqueNonEmpty(
		lkeNamespaceName(env, "platform"),
		lkeNamespaceName(env, "video-cloud"),
		lkeNamespaceName(env, "account-manager"),
		lkeNamespaceName(env, "admin"),
		lkeNamespaceName(env, "frontend"),
		lkeNamespaceName(env, "observability"),
		lkeNamespaceName(env, "secrets"),
		lkeNamespaceName(env, "logger"),
	)
	manifests := []string{}
	for _, namespace := range namespaces {
		manifests = append(manifests, lkeDefaultDenyIngressNetworkPolicyManifest(env, namespace))
	}
	byNamespace := map[string][]int{}
	for _, route := range routes {
		byNamespace[route.Namespace] = append(byNamespace[route.Namespace], firstNonZero(route.TargetPort, route.ServicePort))
	}
	for namespace, ports := range byNamespace {
		manifests = append(manifests, lkeAllowPublicIngressNetworkPolicyManifest(env, namespace, ports))
	}
	manifests = append(manifests, lkeAllowPostgresClientsNetworkPolicyManifest(env))
	manifests = append(manifests, lkeAllowOpenBaoClientsNetworkPolicyManifest(env))
	manifests = append(manifests, lkeAllowAccountManagerCertIssuerNetworkPolicyManifest(env))
	manifests = append(manifests, lkeAllowVideoCloudAccountManagerNetworkPolicyManifest(env))
	manifests = append(manifests, lkeAllowAccountManagerPaymentSimulatorNetworkPolicyManifest(env))
	manifests = append(manifests, lkeAllowVideoCloudAPIInternalNetworkPolicyManifest(env))
	manifests = append(manifests, lkeAllowVideoCloudAPITurnRegistryNetworkPolicyManifest(env))
	manifests = append(manifests, lkeAllowVideoCloudMQTTClientsNetworkPolicyManifest(env))
	manifests = append(manifests, lkeAllowEMQXMQTTUsageNetworkPolicyManifest(env))
	manifests = append(manifests, lkeAllowEMQXClusterNetworkPolicyManifest(env))
	manifests = append(manifests, lkeAllowVideoCloudLoggerNetworkPolicyManifest(env))
	manifests = append(manifests, lkeAllowRedisClientsNetworkPolicyManifest(env))
	manifests = append(manifests, lkeAllowPrometheusScrapeNetworkPolicyManifest(env))
	return manifests
}

func lkeAllowAccountManagerPaymentSimulatorNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-account-manager-payment-simulator
  namespace: %s
  labels:
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchExpressions:
      - key: app.kubernetes.io/name
        operator: In
        values: [account-manager, payment-simulator]
  policyTypes: [Ingress]
  ingress:
    - from:
        - podSelector:
            matchExpressions:
              - key: app.kubernetes.io/name
                operator: In
                values: [account-manager, account-manager-payment-worker, payment-simulator]
      ports:
        - { protocol: TCP, port: 8080 }
        - { protocol: TCP, port: 8081 }
`, lkeNamespaceName(env, "account-manager"), env["CLOUD_STACK_NAME"])
}

func firstNonZero(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func lkeEnvBool(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func lkeDefaultDenyIngressNetworkPolicyManifest(env map[string]string, namespace string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: default-deny-ingress
  namespace: %s
  labels:
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector: {}
  policyTypes:
    - Ingress
`, namespace, env["CLOUD_STACK_NAME"])
}

func lkeAllowPublicIngressNetworkPolicyManifest(env map[string]string, namespace string, ports []int) string {
	uniquePorts := []int{}
	seen := map[int]bool{}
	for _, port := range ports {
		if !seen[port] {
			seen[port] = true
			uniquePorts = append(uniquePorts, port)
		}
	}
	sort.Ints(uniquePorts)
	var portRules strings.Builder
	for _, port := range uniquePorts {
		fmt.Fprintf(&portRules, `        - protocol: TCP
          port: %d
`, port)
	}
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-public-ingress
  namespace: %s
  labels:
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector: {}
  policyTypes:
    - Ingress
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: %s
      ports:
%s`, namespace, env["CLOUD_STACK_NAME"], lkeIngressNamespace(env), portRules.String())
}

func lkeAllowPostgresClientsNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-postgres-clients
  namespace: %s
  labels:
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: postgresql
  policyTypes:
    - Ingress
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: %s
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: %s
      ports:
        - protocol: TCP
          port: 5432
`, lkeNamespaceName(env, "platform"), env["CLOUD_STACK_NAME"], lkeNamespaceName(env, "video-cloud"), lkeNamespaceName(env, "account-manager"))
}

func lkeAllowOpenBaoClientsNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-openbao-clients
  namespace: %s
  labels:
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: openbao
  policyTypes:
    - Ingress
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: %s
      ports:
        - protocol: TCP
          port: 8200
        - protocol: TCP
          port: 8201
`, lkeNamespaceName(env, "secrets"), env["CLOUD_STACK_NAME"], lkeNamespaceName(env, "video-cloud"))
}

func lkeAllowAccountManagerCertIssuerNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-account-manager-certissuer
  namespace: %s
  labels:
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: certissuer
  policyTypes:
    - Ingress
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: %s
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: factoryenroll
      ports:
        - protocol: TCP
          port: 9443
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], lkeNamespaceName(env, "account-manager"))
}

func lkeAllowVideoCloudAccountManagerNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-video-cloud-account-manager
  namespace: %s
  labels:
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: account-manager
  policyTypes:
    - Ingress
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: %s
      ports:
        - protocol: TCP
          port: 8080
`, lkeNamespaceName(env, "account-manager"), env["CLOUD_STACK_NAME"], lkeNamespaceName(env, "video-cloud"))
}

func lkeAllowVideoCloudAPIInternalNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-video-cloud-api-internal
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-api
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: video-cloud-api
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: video-cloud-api
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: mqtt
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: %s
          podSelector:
            matchLabels:
              app.kubernetes.io/name: account-manager-outbox-worker
      ports:
        - protocol: TCP
          port: 8080
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], lkeNamespaceName(env, "account-manager"))
}

func lkeAllowVideoCloudAPITurnRegistryNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-video-cloud-api-turnregistry
  namespace: %s
  labels:
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: video-cloud-turnregistry
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: video-cloud-api
      ports:
        - protocol: TCP
          port: 18190
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"])
}

func lkeAllowVideoCloudMQTTClientsNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-video-cloud-mqtt-clients
  namespace: %s
  labels:
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: mqtt
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: video-cloud-api
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: video-cloud-logingester
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: video-cloud-mqttusage
      ports:
        - protocol: TCP
          port: 1883
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"])
}

func lkeAllowEMQXMQTTUsageNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-emqx-mqtt-usage
  namespace: %s
  labels:
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: video-cloud-mqttusage
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: mqtt
      ports:
        - protocol: TCP
          port: 19400
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"])
}

func lkeAllowEMQXClusterNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-emqx-cluster
  namespace: %s
  labels:
    app.kubernetes.io/name: mqtt
    app.kubernetes.io/component: cluster-discovery
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: mqtt
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: mqtt
      ports:
        - protocol: TCP
          port: 4369
        - protocol: TCP
          port: 4370
        - protocol: TCP
          port: 5369
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"])
}

func lkeAllowVideoCloudLoggerNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-video-cloud-logger
  namespace: %s
  labels:
    app.kubernetes.io/name: cloud-logger
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: cloud-logger
  policyTypes:
    - Ingress
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: %s
      ports:
        - protocol: TCP
          port: 18090
`, lkeNamespaceName(env, "logger"), env["CLOUD_STACK_NAME"], lkeNamespaceName(env, "video-cloud"))
}

func lkeAllowCloudLoggerLokiNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-cloud-logger-loki
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-loki
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: video-cloud-loki
  policyTypes:
    - Ingress
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: %s
          podSelector:
            matchLabels:
              app.kubernetes.io/name: cloud-logger
      ports:
        - protocol: TCP
          port: 3100
`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"], lkeNamespaceName(env, "logger"))
}

func lkeAllowLogCollectorLokiNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-log-collector-loki
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-loki
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: video-cloud-loki
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: video-cloud-log-collector
      ports:
        - protocol: TCP
          port: 3100
`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"])
}

func lkeAllowRedisClientsNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-redis-clients
  namespace: %s
  labels:
    app.kubernetes.io/name: redis
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: redis
  policyTypes:
    - Ingress
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: %s
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: %s
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: redis-exporter
      ports:
        - protocol: TCP
          port: 6379
`, lkeNamespaceName(env, "platform"), env["CLOUD_STACK_NAME"], lkeNamespaceName(env, "video-cloud"), lkeNamespaceName(env, "account-manager"))
}

func lkeAllowPrometheusScrapeNetworkPolicyManifest(env map[string]string) string {
	type scrapePolicy struct {
		namespace string
		apps      []string
		ports     []int
	}
	policies := []scrapePolicy{
		{namespace: lkeNamespaceName(env, "platform"), apps: []string{"redis-exporter"}, ports: []int{9121}},
		{namespace: lkeNamespaceName(env, "account-manager"), apps: []string{"account-manager"}, ports: []int{8080}},
		{namespace: lkeNamespaceName(env, "admin"), apps: []string{"cloud-admin"}, ports: []int{8080}},
		{namespace: lkeNamespaceName(env, "frontend"), apps: []string{"frontend"}, ports: []int{8080}},
		{namespace: lkeNamespaceName(env, "video-cloud"), apps: []string{"video-cloud-api", "factoryenroll", "video-cloud-metricsexporter", "video-cloud-turnregistry", "video-cloud-logingester", "video-cloud-mqttusage"}, ports: []int{8080, 18190, 18443, 19200, 19300, 19400}},
	}
	var b strings.Builder
	for idx, policy := range policies {
		if idx > 0 {
			b.WriteString("---\n")
		}
		b.WriteString(lkePrometheusScrapeNetworkPolicyForNamespace(env, policy.namespace, policy.apps, policy.ports))
	}
	return b.String()
}

func lkePrometheusScrapeNetworkPolicyForNamespace(env map[string]string, namespace string, apps []string, ports []int) string {
	var appValues strings.Builder
	for _, app := range apps {
		fmt.Fprintf(&appValues, "          - %s\n", app)
	}
	var portValues strings.Builder
	for _, port := range ports {
		fmt.Fprintf(&portValues, `        - protocol: TCP
          port: %d
`, port)
	}
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-prometheus-scrape
  namespace: %s
  labels:
    app.kubernetes.io/name: prometheus-scrape
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchExpressions:
      - key: app.kubernetes.io/name
        operator: In
        values:
%s  policyTypes:
    - Ingress
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: %s
          podSelector:
            matchLabels:
              app.kubernetes.io/name: video-cloud-prometheus
      ports:
%s`, namespace, env["CLOUD_STACK_NAME"], appValues.String(), lkeNamespaceName(env, "observability"), portValues.String())
}

func lkeWaitForIngressExternalIP(env map[string]string) (string, error) {
	ns := lkeIngressNamespace(env)
	timeout := envDurationDefault("LKE_INGRESS_EXTERNAL_IP_TIMEOUT", 10*time.Minute)
	deadline := time.Now().Add(timeout)
	var last string
	for {
		out, err := kubectlCombinedOutput(nil, "-n", ns, "get", "service", "ingress-nginx-controller", "-o", "jsonpath={.status.loadBalancer.ingress[0].ip}")
		last = strings.TrimSpace(string(out))
		if err == nil && net.ParseIP(last) != nil {
			return last, nil
		}
		if time.Now().After(deadline) {
			return "", fmt.Errorf("ingress-nginx LoadBalancer external IP not ready: %s", last)
		}
		time.Sleep(5 * time.Second)
	}
}

func lkeDeployWorkloads(paths provisionPaths, env map[string]string, opts provisionOptions) error {
	if opts.loggerOnly {
		if err := ensureLKEDeployImages(env, opts); err != nil {
			return err
		}
		return lkeApplyCloudLogger(env, opts)
	}
	if err := ensureLKEDeployImages(env, opts); err != nil {
		return err
	}
	if err := lkeApplyCloudLogger(env, opts); err != nil {
		return err
	}
	if err := lkeApplyRuntimeDependencies(paths, env, opts); err != nil {
		return err
	}
	var certIssuerMaterial *lkeCertIssuerMaterial
	if lkeWorkloadSelected(env, opts, "account-manager") {
		material, err := loadOrCreateLKECertIssuerMaterial(paths, env)
		if err != nil {
			return err
		}
		certIssuerMaterial = &material
	}
	selectedWorkloads := lkeSelectedWorkloads(env, opts)
	for _, workload := range selectedWorkloads {
		if workload.Key == "cloud-logger" {
			continue
		}
		if err := kubectlApply(lkeDeploymentManifest(env, workload, certIssuerMaterial)); err != nil {
			return err
		}
		if err := kubectlApply(lkeServiceManifest(env, workload)); err != nil {
			return err
		}
	}
	if lkeWorkloadSelected(env, opts, "account-manager") {
		if err := kubectlApply(lkeAccountManagerOutboxWorkerManifest(env)); err != nil {
			return err
		}
		if lkeEmailDeliveryEnabled(env) {
			if err := kubectlApply(lkeAccountManagerEmailWorkerManifest(env)); err != nil {
				return err
			}
		} else if err := runKubectl("-n", lkeNamespaceName(env, "account-manager"), "delete", "deployment/account-manager-email-worker", "--ignore-not-found=true"); err != nil {
			return err
		}
		if err := kubectlApply(lkePaymentSimulatorServiceManifest(env)); err != nil {
			return err
		}
		if err := kubectlApply(lkePaymentSimulatorDeploymentManifest(env)); err != nil {
			return err
		}
		if err := kubectlApply(lkeAccountManagerPaymentWorkerManifest(env)); err != nil {
			return err
		}
	}
	if err := lkeWaitForRollouts(k8sRolloutTargetsFromEnv(selectedWorkloads)); err != nil {
		return err
	}
	if lkeWorkloadSelected(env, opts, "account-manager") && lkeEmailDeliveryEnabled(env) {
		if err := runKubectl("-n", lkeNamespaceName(env, "account-manager"), "rollout", "status", "deployment/account-manager-email-worker", "--timeout", firstNonEmpty(os.Getenv("LKE_WORKLOAD_ROLLOUT_TIMEOUT"), "10m")); err != nil {
			return err
		}
	}
	if lkeWorkloadSelected(env, opts, "account-manager") {
		if err := runKubectl("-n", lkeNamespaceName(env, "account-manager"), "rollout", "status", "deployment/account-manager-outbox-worker", "--timeout", firstNonEmpty(os.Getenv("LKE_WORKLOAD_ROLLOUT_TIMEOUT"), "10m")); err != nil {
			return err
		}
		for _, deployment := range []string{"payment-simulator", "account-manager-payment-worker"} {
			if err := runKubectl("-n", lkeNamespaceName(env, "account-manager"), "rollout", "status", "deployment/"+deployment, "--timeout", firstNonEmpty(os.Getenv("LKE_WORKLOAD_ROLLOUT_TIMEOUT"), "10m")); err != nil {
				return err
			}
		}
	}
	if lkeWorkloadSelected(env, opts, "video-cloud") {
		return lkeRestartVideoCloudLogIngester(env)
	}
	return nil
}

func lkeRestartVideoCloudLogIngester(env map[string]string) error {
	namespace := lkeNamespaceName(env, "video-cloud")
	if err := runKubectl("-n", namespace, "rollout", "restart", "deployment/video-cloud-logingester"); err != nil {
		return err
	}
	return runKubectl("-n", namespace, "rollout", "status", "deployment/video-cloud-logingester", "--timeout", firstNonEmpty(os.Getenv("LKE_ROLLOUT_TIMEOUT"), "5m"))
}

func ensureLKEDeployImages(env map[string]string, opts provisionOptions) error {
	return validateLKEDeployInputs(env, opts)
}

func validateLKEDeployInputs(env map[string]string, opts provisionOptions) error {
	missingWorkloads := lkeMissingDeployImageWorkloads(env, opts)
	missing := []string{}
	for _, workload := range missingWorkloads {
		missing = append(missing, workload.EnvKey)
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return fmt.Errorf("LKE deploy requires container image environment variables; generate them with lke-resolve-images: %s", strings.Join(missing, ", "))
	}
	if lkeWorkloadSelected(env, opts, "video-cloud") {
		if err := validateRuntimeCoverageVideoCloudAPIBaseURL(env); err != nil {
			return err
		}
	}
	if lkeWorkloadSelected(env, opts, "video-cloud") && lkeClipDirectUploadEnabled(env) {
		missingBlob := []string{}
		for _, key := range []string{"VIDEO_CLOUD_BLOB_ENDPOINT", "VIDEO_CLOUD_BLOB_REGION", "VIDEO_CLOUD_BLOB_BUCKET"} {
			if strings.TrimSpace(env[key]) == "" {
				missingBlob = append(missingBlob, key)
			}
		}
		if len(missingBlob) > 0 {
			return fmt.Errorf("LKE video-cloud deploy requires blob configuration when direct clip upload is enabled: %s", strings.Join(missingBlob, ", "))
		}
		missingCredentials := []string{}
		for _, key := range []string{"LINODE_OBJ_ACCESS_KEY_ID", "LINODE_OBJ_SECRET_ACCESS_KEY"} {
			if lkeObjectStorageCredential(env, key) == "" {
				missingCredentials = append(missingCredentials, key)
			}
		}
		if len(missingCredentials) > 0 {
			return fmt.Errorf("LKE video-cloud deploy requires object-storage credentials when direct clip upload is enabled: %s", strings.Join(missingCredentials, ", "))
		}
	}
	return nil
}

func lkeVideoCloudAPIBaseURL(env map[string]string) string {
	if value := strings.TrimSpace(env["VIDEO_CLOUD_API_BASE_URL"]); value != "" {
		return value
	}
	if domain := strings.TrimSpace(env["VIDEO_CLOUD_DOMAIN"]); domain != "" {
		return "https://" + domain
	}
	return "http://video-cloud-api." + lkeNamespaceName(env, "video-cloud") + ".svc.cluster.local:8080"
}

func validateRuntimeCoverageVideoCloudAPIBaseURL(env map[string]string) error {
	stack := strings.TrimSpace(env["CLOUD_RUNTIME_COVERAGE_STACK"])
	if stack == "" {
		return nil
	}
	if strings.TrimSpace(env["CLOUD_STACK_NAME"]) != stack {
		return fmt.Errorf("runtime coverage API base URL stack marker %q does not match CLOUD_STACK_NAME %q", stack, strings.TrimSpace(env["CLOUD_STACK_NAME"]))
	}
	raw := strings.TrimSpace(env["VIDEO_CLOUD_API_BASE_URL"])
	if raw == "" {
		return errors.New("runtime coverage requires VIDEO_CLOUD_API_BASE_URL")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse runtime coverage VIDEO_CLOUD_API_BASE_URL: %w", err)
	}
	expectedHost := "video." + stack + ".invalid"
	if parsed.Scheme != "https" ||
		parsed.Hostname() != expectedHost ||
		parsed.Port() != "18443" ||
		parsed.User != nil ||
		(parsed.Path != "" && parsed.Path != "/") ||
		parsed.RawQuery != "" ||
		parsed.Fragment != "" {
		return fmt.Errorf("runtime coverage VIDEO_CLOUD_API_BASE_URL must be https://%s:18443", expectedHost)
	}
	return nil
}

func lkeMissingDeployImageWorkloads(env map[string]string, opts provisionOptions) []lkeWorkload {
	return k8sMissingDeployImageWorkloads(env, opts)
}

func lkeMissingBuildImageWorkloads(env map[string]string, opts provisionOptions) []lkeWorkload {
	return k8sMissingBuildImageWorkloads(env, opts)
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
	case "postgres":
		return generatedPostgresDockerfile()
	case "video-cloud":
		return generatedVideoCloudDockerfile(filepath.Join(workspace, "repos", "rtk_video_cloud"))
	case "account-manager":
		return generatedAccountManagerDockerfile(filepath.Join(workspace, "repos", "rtk_account_manager"))
	case "cloud-admin":
		return generatedGoServiceDockerfile(filepath.Join(workspace, "repos", "rtk_cloud_admin"), "./cmd/server", "rtk-cloud-admin")
	case "frontend":
		return filepath.Join(workspace, "repos", "rtk_cloud_frontend"), filepath.Join(workspace, "repos", "rtk_cloud_frontend", "Dockerfile"), func() {}, nil
	case "cloud-logger":
		return filepath.Join(workspace, "repos", "rtk_cloud_logger"), filepath.Join(workspace, "repos", "rtk_cloud_logger", "Dockerfile"), func() {}, nil
	default:
		return "", "", func() {}, fmt.Errorf("no LKE image build context for workload %s", workload.Key)
	}
}

func generatedPostgresDockerfile() (string, string, func(), error) {
	dir, err := os.MkdirTemp("", "rtk-lke-postgres-*")
	if err != nil {
		return "", "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	dockerfile := filepath.Join(dir, "Dockerfile")
	body := `FROM postgres:16-alpine
LABEL org.opencontainers.image.title="rtk-cloud-lke-postgresql"
LABEL org.opencontainers.image.description="PostgreSQL runtime image for the RTK Cloud LKE staging bridge"
`
	if err := os.WriteFile(dockerfile, []byte(body), 0o644); err != nil {
		cleanup()
		return "", "", func() {}, err
	}
	return dir, dockerfile, cleanup, nil
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
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/clipverifier ./cmd/clipverifier
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/clipuploadpreflight ./cmd/clipuploadpreflight
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/clipreconcile ./cmd/clipreconcile

FROM debian:bookworm-slim
WORKDIR /app
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd -r -u 10001 app \
    && chown app:app /app
COPY --from=builder /out/api /app/api
COPY --from=builder /out/certissuer /app/certissuer
COPY --from=builder /out/factoryenroll /app/factoryenroll
COPY --from=builder /out/cleaner /app/cleaner
COPY --from=builder /out/statistics /app/statistics
COPY --from=builder /out/metricsexporter /app/metricsexporter
COPY --from=builder /out/turnregistry /app/turnregistry
COPY --from=builder /out/logingester /app/logingester
COPY --from=builder /out/mqttusage /app/mqttusage
COPY --from=builder /out/clipverifier /app/clipverifier
COPY --from=builder /out/clipuploadpreflight /app/clipuploadpreflight
COPY --from=builder /out/clipreconcile /app/clipreconcile
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
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/rtk-account-manager-user-cache ./cmd/user-cache
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/rtk-account-manager-email-worker ./cmd/email-worker
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/rtk-account-manager-email-outbox-admin ./cmd/email-outbox-admin
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o /out/rtk-account-manager-outbox-worker ./cmd/outbox-worker

FROM debian:bookworm-slim
WORKDIR /app
RUN apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates \
    && rm -rf /var/lib/apt/lists/* \
    && useradd -r -u 10001 app \
    && chown app:app /app
COPY --from=builder /out/rtk-account-manager /app/rtk-account-manager
COPY --from=builder /out/rtk-account-manager-migrate /app/rtk-account-manager-migrate
COPY --from=builder /out/rtk-account-manager-user-cache /app/rtk-account-manager-user-cache
COPY --from=builder /out/rtk-account-manager-email-worker /app/rtk-account-manager-email-worker
COPY --from=builder /out/rtk-account-manager-email-outbox-admin /app/rtk-account-manager-email-outbox-admin
COPY --from=builder /out/rtk-account-manager-outbox-worker /app/rtk-account-manager-outbox-worker
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
			fmt.Fprintln(os.Stderr, "[cloud-remove-k8s] cancelled")
			return nil
		}
	}
	if err := ensureLKEKubeAccess(provisionPaths{EnvRoot: envRoot}, env, false); err != nil {
		if errors.Is(err, errLKEMissingCluster) {
			fmt.Fprintf(os.Stderr, "[cloud-remove-k8s] no LKE cluster found for stack %s\n", stack)
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
	if err := backupAndRemoveLKEState(envRoot); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "[cloud-remove-k8s] LKE namespace delete requests submitted for stack %s\n", stack)
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
		{Key: "logger", Name: firstNonEmpty(os.Getenv("LKE_NAMESPACE_CLOUD_LOGGER"), stack+"-logger")},
		{Key: "ingress", Name: lkeIngressNamespace(env)},
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
	return k8sWorkloads(env)
}

func lkeVideoCloudAuxiliaryServices() []lkeVideoCloudAuxiliaryService {
	return k8sAuxiliaryWorkloads()
}

func lkeImageWorkloads(env map[string]string, opts provisionOptions) []lkeWorkload {
	return k8sImageWorkloads(env, opts)
}

func lkeSelectedWorkloads(env map[string]string, opts provisionOptions) []lkeWorkload {
	return k8sSelectedWorkloads(env, opts)
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
	if err := lkeApplyPostgresStatefulSet(env); err != nil {
		return err
	}
	if lkePostgresUsesPVC(env) {
		if err := lkeWaitForPostgresPVC(env); err != nil {
			return err
		}
	}
	if err := runKubectl("-n", lkeNamespaceName(env, "platform"), "rollout", "status", "statefulset/postgresql", "--timeout", firstNonEmpty(os.Getenv("LKE_POSTGRES_ROLLOUT_TIMEOUT"), "5m")); err != nil {
		return err
	}
	if err := lkeApplyRedisRuntime(env); err != nil {
		return err
	}
	var material lkeCertIssuerMaterial
	materialReady := false
	if lkeWorkloadSelected(env, opts, "video-cloud") {
		var err error
		material, err = loadOrCreateLKECertIssuerMaterial(paths, env)
		if err != nil {
			return err
		}
		openBaoTLS, err := loadOrCreateLKEOpenBaoTLSMaterial(paths, env)
		if err != nil {
			return err
		}
		openBao, err := lkeEnsureOpenBao(paths, env, openBaoTLS)
		if err != nil {
			return err
		}
		material.RootCACert = openBao.RootCACert
		material.DeviceCACert = openBao.DeviceCACert
		material.AppCACert = openBao.AppCACert
		if err := writeLKEDeviceClientCABundle(paths, material.RootCACert, material.DeviceCACert, material.AppCACert); err != nil {
			return err
		}
		materialReady = true
		if err := writeLKEVideoCloudRuntimeEnv(paths, env); err != nil {
			return err
		}
		if err := kubectlApply(lkeVideoCloudRuntimeSecretManifest(env)); err != nil {
			return err
		}
		if err := kubectlDeleteSecret(lkeNamespaceName(env, "video-cloud"), "certissuer-runtime"); err != nil {
			return err
		}
		if err := kubectlApply(lkeCertIssuerRuntimeSecretManifest(env, material)); err != nil {
			return err
		}
		if err := kubectlApply(lkeDeviceMTLSAppCASecretManifest(env, lkeClientCABundle(material.RootCACert, material.DeviceCACert, material.AppCACert))); err != nil {
			return err
		}
		if err := kubectlApply(lkeCertIssuerOpenBaoAuthSecretManifest(env, openBao)); err != nil {
			return err
		}
		if err := kubectlApply(lkeCertIssuerServiceManifest(env)); err != nil {
			return err
		}
		if err := kubectlApply(lkeCertIssuerDeploymentManifest(env, material, openBao)); err != nil {
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
		if err := kubectlApply(lkeFactoryEnrollDeploymentManifest(env, material)); err != nil {
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
		if err := kubectlApply(lkeMQTTHeadlessServiceManifest(env)); err != nil {
			return err
		}
		if err := kubectlApply(lkeAllowEMQXClusterNetworkPolicyManifest(env)); err != nil {
			return err
		}
		// API and worker MQTT clients start before public HTTPS/ACME setup.
		// Apply their ingress grant here so a later public-edge failure cannot
		// leave the internal MQTT data plane disconnected.
		if err := kubectlApply(lkeAllowVideoCloudMQTTClientsNetworkPolicyManifest(env)); err != nil {
			return err
		}
		if err := kubectlApply(lkeAllowVideoCloudAPIInternalNetworkPolicyManifest(env)); err != nil {
			return err
		}
		if lkeEnvBool("LKE_PUBLIC_MQTT_LOADBALANCER") {
			if err := lkeApplyPublicMQTTNodePort(env); err != nil {
				return err
			}
		}
		if err := runKubectl("-n", lkeNamespaceName(env, "video-cloud"), "delete", "deployment/mqtt", "--ignore-not-found=true"); err != nil {
			return err
		}
		if err := kubectlApply(lkeMQTTStatefulSetManifest(env)); err != nil {
			return err
		}
		if err := runKubectl("-n", lkeNamespaceName(env, "video-cloud"), "rollout", "status", "statefulset/mqtt", "--timeout", firstNonEmpty(os.Getenv("LKE_MQTT_ROLLOUT_TIMEOUT"), "5m")); err != nil {
			return err
		}
		if err := lkeEnsureEMQXCluster(env); err != nil {
			return err
		}
		if err := lkeRemoveK8sCoturnRuntime(env); err != nil {
			return err
		}
		if err := lkeApplyVideoCloudAuxiliaryServices(env, opts); err != nil {
			return err
		}
		if err := lkeConfigureEMQXBilling(paths, env); err != nil {
			return err
		}
	}
	if !lkeWorkloadSelected(env, opts, "account-manager") {
		return nil
	}
	if !materialReady {
		var err error
		material, err = loadOrCreateLKECertIssuerMaterial(paths, env)
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
	if err := kubectlApply(lkeAllowVideoCloudAPIInternalNetworkPolicyManifest(env)); err != nil {
		return err
	}
	_ = runKubectl("-n", lkeNamespaceName(env, "account-manager"), "delete", "job", "account-manager-migrate", "--ignore-not-found")
	if err := kubectlApply(lkeAccountManagerMigrationJobManifest(env)); err != nil {
		return err
	}
	return runKubectl("-n", lkeNamespaceName(env, "account-manager"), "wait", "--for=condition=complete", "job/account-manager-migrate", "--timeout", firstNonEmpty(os.Getenv("LKE_MIGRATION_JOB_TIMEOUT"), "5m"))
}

func lkeApplyRedisRuntime(env map[string]string) error {
	for _, manifest := range []string{
		lkeRedisDeploymentManifest(env),
		lkeRedisServiceManifest(env),
		lkeRedisExporterDeploymentManifest(env),
		lkeRedisExporterServiceManifest(env),
		lkeAllowRedisClientsNetworkPolicyManifest(env),
		lkeAllowPrometheusScrapeNetworkPolicyManifest(env),
	} {
		if err := kubectlApply(manifest); err != nil {
			return err
		}
	}
	if err := runKubectl("-n", lkeNamespaceName(env, "platform"), "rollout", "status", "deployment/redis", "--timeout", firstNonEmpty(os.Getenv("LKE_REDIS_ROLLOUT_TIMEOUT"), "5m")); err != nil {
		return err
	}
	return runKubectl("-n", lkeNamespaceName(env, "platform"), "rollout", "status", "deployment/redis-exporter", "--timeout", firstNonEmpty(os.Getenv("LKE_REDIS_EXPORTER_ROLLOUT_TIMEOUT"), "5m"))
}

func lkeApplyCloudLogger(env map[string]string, opts provisionOptions) error {
	if !lkeWorkloadSelected(env, opts, "cloud-logger") {
		return nil
	}
	for _, manifest := range []string{
		lkeLokiConfigManifest(env),
		lkeLokiDeploymentManifest(env),
		lkeLokiServiceManifest(env),
		lkeAllowCloudLoggerLokiNetworkPolicyManifest(env),
		lkeLogCollectorServiceAccountManifest(env),
		lkeLogCollectorClusterRoleManifest(env),
		lkeLogCollectorClusterRoleBindingManifest(env),
		lkeLogCollectorConfigManifest(env),
		lkeLogCollectorDaemonSetManifest(env),
		lkeAllowLogCollectorLokiNetworkPolicyManifest(env),
	} {
		if err := kubectlApply(manifest); err != nil {
			return err
		}
	}
	if err := runKubectl("-n", lkeNamespaceName(env, "observability"), "rollout", "status", "deployment/video-cloud-loki", "--timeout", firstNonEmpty(os.Getenv("LKE_LOKI_ROLLOUT_TIMEOUT"), "5m")); err != nil {
		return err
	}
	if err := runKubectl("-n", lkeNamespaceName(env, "observability"), "rollout", "status", "daemonset/video-cloud-log-collector", "--timeout", firstNonEmpty(os.Getenv("LKE_LOG_COLLECTOR_ROLLOUT_TIMEOUT"), "5m")); err != nil {
		return err
	}
	if err := kubectlApply(lkeCloudLoggerRuntimeSecretManifest(env)); err != nil {
		return err
	}
	if err := kubectlApply(lkeCloudLoggerDeploymentManifest(env)); err != nil {
		return err
	}
	if err := kubectlApply(lkeCloudLoggerServiceManifest(env)); err != nil {
		return err
	}
	if err := kubectlApply(lkeAllowVideoCloudLoggerNetworkPolicyManifest(env)); err != nil {
		return err
	}
	return runKubectl("-n", lkeNamespaceName(env, "logger"), "rollout", "status", "deployment/cloud-logger", "--timeout", firstNonEmpty(os.Getenv("LKE_CLOUD_LOGGER_ROLLOUT_TIMEOUT"), "3m"))
}

func lkeRemoveK8sCoturnRuntime(env map[string]string) error {
	namespace := lkeNamespaceName(env, "video-cloud")
	for _, resource := range []string{
		"deployment/coturn",
		"service/coturn",
		"configmap/coturn-config",
		"secret/coturn-runtime",
	} {
		if err := runKubectl("-n", namespace, "delete", resource, "--ignore-not-found=true"); err != nil {
			return err
		}
	}
	return nil
}

func lkeApplyVideoCloudAuxiliaryServices(env map[string]string, opts provisionOptions) error {
	if err := kubectlApply(lkeVideoCloudWorkersSecretManifest(env)); err != nil {
		return err
	}
	rollouts := []lkeRolloutTarget{}
	for _, service := range lkeVideoCloudAuxiliaryServices() {
		if err := kubectlApply(lkeVideoCloudAuxiliaryDeploymentManifest(env, service)); err != nil {
			return err
		}
		if service.Port > 0 {
			if err := kubectlApply(lkeVideoCloudAuxiliaryServiceManifest(env, service)); err != nil {
				return err
			}
		}
		rollouts = append(rollouts, lkeRolloutTarget{
			Namespace: lkeNamespaceName(env, "video-cloud"),
			Resource:  "deployment/" + service.Name,
			Timeout:   firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_WORKER_ROLLOUT_TIMEOUT"), "5m"),
		})
	}
	if err := lkeWaitForRollouts(rollouts); err != nil {
		return err
	}
	if err := kubectlApply(lkeVideoCloudPrometheusConfigManifest(env, opts)); err != nil {
		return err
	}
	if err := kubectlApply(lkeVideoCloudPrometheusDeploymentManifest(env, opts)); err != nil {
		return err
	}
	if err := kubectlApply(lkeVideoCloudPrometheusServiceManifest(env)); err != nil {
		return err
	}
	if err := runKubectl("-n", lkeNamespaceName(env, "observability"), "rollout", "status", "deployment/video-cloud-prometheus", "--timeout", firstNonEmpty(os.Getenv("LKE_PROMETHEUS_ROLLOUT_TIMEOUT"), "5m")); err != nil {
		return err
	}
	if !lkeWorkloadSelected(env, opts, "cloud-admin") {
		return nil
	}
	return lkeApplyGrafana(env)
}

func lkeConfigureEMQXBilling(paths provisionPaths, env map[string]string) error {
	workspace := strings.TrimSpace(paths.Workspace)
	if workspace == "" {
		var err error
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	scriptPath := filepath.Join(workspace, "scripts", "configure-emqx-billing.sh")
	script, err := os.ReadFile(scriptPath)
	if errors.Is(err, os.ErrNotExist) {
		if sourceWorkspace, sourceErr := workspaceRoot(); sourceErr == nil && sourceWorkspace != workspace {
			scriptPath = filepath.Join(sourceWorkspace, "scripts", "configure-emqx-billing.sh")
			script, err = os.ReadFile(scriptPath)
		}
	}
	if err != nil {
		return fmt.Errorf("read EMQX billing configurator %s: %w", scriptPath, err)
	}
	if err := kubectlApply(lkeAllowEMQXBillingConfigureNetworkPolicyManifest(env)); err != nil {
		return err
	}
	if err := kubectlApply(lkeAllowEMQXMQTTUsageNetworkPolicyManifest(env)); err != nil {
		return err
	}
	if err := kubectlApply(lkeEMQXBillingConfigMapManifest(env, string(script))); err != nil {
		return err
	}
	namespace := lkeNamespaceName(env, "video-cloud")
	if err := runKubectl("-n", namespace, "delete", "job/emqx-billing-configure", "--ignore-not-found=true", "--wait=true"); err != nil {
		return err
	}
	if err := kubectlApply(lkeEMQXBillingJobManifest(env)); err != nil {
		return err
	}
	if err := runKubectl("-n", namespace, "wait", "--for=condition=complete", "job/emqx-billing-configure", "--timeout", firstNonEmpty(os.Getenv("LKE_EMQX_BILLING_CONFIGURE_TIMEOUT"), "5m")); err != nil {
		_ = runKubectl("-n", namespace, "logs", "job/emqx-billing-configure")
		return fmt.Errorf("configure EMQX billing rules: %w", err)
	}
	if err := runKubectl("-n", namespace, "logs", "job/emqx-billing-configure"); err != nil {
		return err
	}
	if err := runKubectl("-n", namespace, "delete", "job/emqx-billing-configure", "--wait=true"); err != nil {
		return err
	}
	return runKubectl("-n", namespace, "delete", "configmap/emqx-billing-configure", "--ignore-not-found=true")
}

func lkeEMQXBillingConfigMapManifest(env map[string]string, script string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: emqx-billing-configure
  namespace: %s
  labels:
    app.kubernetes.io/name: emqx-billing-configure
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
data:
  configure-emqx-billing.sh: |
%s
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], indentManifest(script, 4))
}

func lkeEMQXBillingJobManifest(env map[string]string) string {
	callbackURL := fmt.Sprintf("http://video-cloud-mqttusage.%s.svc.cluster.local:19400", lkeNamespaceName(env, "video-cloud"))
	return fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata:
  name: emqx-billing-configure
  namespace: %s
  labels:
    app.kubernetes.io/name: emqx-billing-configure
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  backoffLimit: 3
  template:
    metadata:
      labels:
        app.kubernetes.io/name: emqx-billing-configure
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
      restartPolicy: Never
      containers:
        - name: configure
          image: alpine:3.20
          command: ["/bin/sh", "-c"]
          args:
            - |
              set -eu
              apk add --no-cache bash curl jq >/dev/null
              login_response="$(curl --fail --silent --show-error --max-time 10 --retry 12 --retry-all-errors --retry-delay 2 \
                -H 'content-type: application/json' \
                -d "{\"username\":\"admin\",\"password\":\"${EMQX_DASHBOARD_PASSWORD}\"}" \
                http://mqtt:18083/api/v5/login)"
              EMQX_API_TOKEN="$(printf '%%s' "$login_response" | jq -er '.token')"
              export EMQX_API_TOKEN
              exec /scripts/configure-emqx-billing.sh
          env:
            - name: EMQX_API_URL
              value: "http://mqtt:18083/api/v5"
            - name: EMQX_DASHBOARD_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: mqtt-runtime
                  key: EMQX_DASHBOARD_PASSWORD
            - name: VIDEO_CLOUD_MQTT_USAGE_INGEST_TOKEN
              valueFrom:
                secretKeyRef:
                  name: video-cloud-workers-runtime
                  key: VIDEO_CLOUD_MQTT_USAGE_INGEST_TOKEN
            - name: VIDEO_CLOUD_MQTT_USAGE_CALLBACK_URL
              value: %q
          volumeMounts:
            - name: scripts
              mountPath: /scripts
              readOnly: true
      volumes:
        - name: scripts
          configMap:
            name: emqx-billing-configure
            defaultMode: 0555
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], callbackURL)
}

func lkeAllowEMQXBillingConfigureNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-emqx-billing-configure
  namespace: %s
  labels:
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: mqtt
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: emqx-billing-configure
      ports:
        - protocol: TCP
          port: 18083
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"])
}

func lkeApplyGrafana(env map[string]string) error {
	manifests := []string{
		lkeGrafanaAdminSecretManifest(env),
		lkeGrafanaDatasourcesConfigManifest(env),
		lkeGrafanaDashboardProvidersConfigManifest(env),
		lkeGrafanaDashboardsConfigManifest(env),
	}
	if lkeGrafanaPersistenceEnabled(env) {
		manifests = append(manifests, lkeGrafanaPVCManifest(env))
	}
	manifests = append(manifests,
		lkeGrafanaDeploymentManifest(env),
		lkeGrafanaServiceManifest(env),
		lkeAllowCloudAdminGrafanaNetworkPolicyManifest(env),
		lkeAllowGrafanaPrometheusNetworkPolicyManifest(env),
		lkeAllowPrometheusGrafanaNetworkPolicyManifest(env),
	)
	for _, manifest := range manifests {
		if err := kubectlApply(manifest); err != nil {
			return err
		}
	}
	return runKubectl("-n", lkeNamespaceName(env, "observability"), "rollout", "status", "deployment/video-cloud-grafana", "--timeout", firstNonEmpty(os.Getenv("LKE_GRAFANA_ROLLOUT_TIMEOUT"), "5m"))
}

func lkeWaitForRollouts(targets []lkeRolloutTarget) error {
	if len(targets) == 0 {
		return nil
	}
	errCh := make(chan error, len(targets))
	for _, target := range targets {
		target := target
		go func() {
			errCh <- runKubectl("-n", target.Namespace, "rollout", "status", target.Resource, "--timeout", target.Timeout)
		}()
	}
	var firstErr error
	for range targets {
		if err := <-errCh; err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func writeLKECompatibilityArtifacts(paths provisionPaths, env map[string]string) error {
	if err := os.MkdirAll(filepath.Join(paths.EnvRoot, "env"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(paths.EnvRoot, "state"), 0o755); err != nil {
		return err
	}
	rawStack := map[string]string{
		"CLOUD_ENV_NAME":        firstNonEmpty(env["CLOUD_ENV_NAME"], "staging"),
		"CLOUD_PROVIDER":        "lke",
		"CLOUD_REGION":          firstNonEmpty(env["CLOUD_REGION"], "us-sea"),
		"CLOUD_DNS_ROOT_DOMAIN": firstNonEmpty(env["CLOUD_DNS_ROOT_DOMAIN"], "realtekconnect.com"),
	}
	for key, value := range env {
		if value != "" && isSafeLKEOperatorStackOverride(key) {
			rawStack[key] = value
		}
	}
	stackBody := renderStackEnv(rawStack, env)
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

func isSafeLKEOperatorStackOverride(key string) bool {
	safeRuntimeKeys := map[string]bool{
		"CLOUD_RUNTIME_COVERAGE_STACK":           true,
		"VIDEO_CLOUD_API_BASE_URL":               true,
		"VIDEO_CLOUD_BLOB_ENDPOINT":              true,
		"VIDEO_CLOUD_BLOB_REGION":                true,
		"VIDEO_CLOUD_BLOB_BUCKET":                true,
		"VIDEO_CLOUD_BLOB_PREFIX":                true,
		"VIDEO_CLOUD_BLOB_FORCE_PATH_STYLE":      true,
		"VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED": true,
		"VIDEO_CLOUD_CLIP_VERIFIER_ADDR":         true,
		"VIDEO_CLOUD_CLIP_UPLOAD_URL_TTL":        true,
		"VIDEO_CLOUD_CLIP_UPLOAD_SESSION_TTL":    true,
		"VIDEO_CLOUD_CLIP_UPLOAD_MAX_BYTES":      true,
		"VIDEO_CLOUD_CLIP_THUMBNAIL_MAX_BYTES":   true,
		"VIDEO_CLOUD_CLIP_VERIFY_POLL_INTERVAL":  true,
		"VIDEO_CLOUD_CLIP_VERIFY_SWEEP_INTERVAL": true,
		"VIDEO_CLOUD_WEBRTC_TURN_URLS":           true,
	}
	if safeRuntimeKeys[key] {
		return true
	}
	if !strings.HasPrefix(key, "LKE_") {
		return false
	}
	secretMarkers := []string{"TOKEN", "SECRET", "PASSWORD", "KEY", "DSN", "CREDENTIAL"}
	upper := strings.ToUpper(key)
	for _, marker := range secretMarkers {
		if strings.Contains(upper, marker) {
			return false
		}
	}
	return true
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
				"role":       "statefulset/mqtt",
			},
			"coturn": map[string]any{
				"public_host": lkeCoturnDomain(env),
				"role":        "coturn-vm",
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
	return k8sWorkloadSelected(env, opts, key)
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
	if lkePostgresUsesPVC(env) {
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
        volumeMode: Filesystem
        resources:
          requests:
            storage: %s
`, firstNonEmpty(os.Getenv("LKE_POSTGRES_STORAGE"), env["LKE_POSTGRES_STORAGE"], "20Gi"))
	}
	placement := lkePostgresPlacementManifest(env)
	requestCPU := firstNonEmpty(os.Getenv("LKE_POSTGRES_REQUEST_CPU"), env["LKE_POSTGRES_REQUEST_CPU"], "1")
	requestMemory := firstNonEmpty(os.Getenv("LKE_POSTGRES_REQUEST_MEMORY"), env["LKE_POSTGRES_REQUEST_MEMORY"], "2Gi")
	limitMemory := firstNonEmpty(os.Getenv("LKE_POSTGRES_LIMIT_MEMORY"), env["LKE_POSTGRES_LIMIT_MEMORY"], "4Gi")
	maxConnections := firstNonEmpty(os.Getenv("LKE_POSTGRES_MAX_CONNECTIONS"), env["LKE_POSTGRES_MAX_CONNECTIONS"], "800")
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
%s
      containers:
        - name: postgres
          image: %s
          args:
            - "-c"
            - "max_connections=%s"
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
          resources:
            requests:
              cpu: %q
              memory: %q
            limits:
              memory: %q
          volumeMounts:
            - name: data
              mountPath: /var/lib/postgresql/data
            - name: initdb
              mountPath: /docker-entrypoint-initdb.d
%s%s`, lkeNamespaceName(env, "platform"), env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], placement, lkePostgresImage(), maxConnections, requestCPU, requestMemory, limitMemory, storage, volumeClaims)
}

func lkePostgresPlacementManifest(env map[string]string) string {
	if firstNonEmpty(os.Getenv("LKE_POSTGRES_NODE_POOL_ID"), env["LKE_POSTGRES_NODE_POOL_ID"]) == "" {
		return ""
	}
	return `      nodeSelector:
        rtk.io/node-class: "database"
      tolerations:
        - key: "rtk.io/node-class"
          operator: "Equal"
          value: "database"
          effect: "NoSchedule"
`
}

func lkeApplyPostgresStatefulSet(env map[string]string) error {
	manifest := lkePostgresStatefulSetManifest(env)
	err := kubectlApply(manifest)
	if err == nil {
		return lkeReplaceStalePostgresPod(env)
	}
	if !strings.Contains(manifest, "volumeClaimTemplates:") || !isStatefulSetImmutableUpdateError(err) {
		return err
	}
	namespace := lkeNamespaceName(env, "platform")
	if deleteErr := runKubectl("-n", namespace, "delete", "statefulset/postgresql", "--cascade=orphan", "--ignore-not-found=true"); deleteErr != nil {
		return deleteErr
	}
	if err := kubectlApply(manifest); err != nil {
		return err
	}
	return lkeReplaceStalePostgresPod(env)
}

func lkeReplaceStalePostgresPod(env map[string]string) error {
	namespace := lkeNamespaceName(env, "platform")
	out, err := kubectlCombinedOutput(nil, "-n", namespace, "get", "statefulset/postgresql", "-o", "jsonpath={.status.currentRevision} {.status.updateRevision}")
	if err != nil {
		return err
	}
	fields := strings.Fields(string(out))
	if len(fields) < 2 || fields[0] == "" || fields[1] == "" || fields[0] == fields[1] {
		return nil
	}
	fmt.Fprintf(os.Stderr, "[lke] replacing stale postgresql-0 pod revision %s -> %s\n", fields[0], fields[1])
	return runKubectl("-n", namespace, "delete", "pod/postgresql-0", "--ignore-not-found=true")
}

func isStatefulSetImmutableUpdateError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	return strings.Contains(msg, "StatefulSet") && strings.Contains(msg, "Forbidden") && strings.Contains(msg, "updates to statefulset spec")
}

func lkePostgresUsesPVC(env map[string]string) bool {
	mode := strings.ToLower(strings.TrimSpace(firstNonEmpty(os.Getenv("LKE_POSTGRES_STORAGE_MODE"), env["LKE_POSTGRES_STORAGE_MODE"], "pvc")))
	return mode != "emptydir" && mode != "ephemeral"
}

func lkeWaitForPostgresPVC(env map[string]string) error {
	return runKubectl("-n", lkeNamespaceName(env, "platform"), "wait", "--for=jsonpath={.status.phase}=Bound", "pvc/data-postgresql-0", "--timeout", firstNonEmpty(os.Getenv("LKE_POSTGRES_PVC_TIMEOUT"), "5m"))
}

func lkePostgresImage() string {
	return firstNonEmpty(os.Getenv("LKE_POSTGRES_IMAGE"), "postgres:16-alpine")
}

func lkeRedisImage() string {
	return firstNonEmpty(os.Getenv("LKE_REDIS_IMAGE"), "valkey/valkey:8-alpine")
}

func lkeRedisExporterImage() string {
	return firstNonEmpty(os.Getenv("LKE_REDIS_EXPORTER_IMAGE"), "oliver006/redis_exporter:v1.74.0")
}

func lkeRedisServiceHost(env map[string]string) string {
	return "redis." + lkeNamespaceName(env, "platform") + ".svc.cluster.local"
}

func lkeRedisDeploymentManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: redis
  namespace: %s
  labels:
    app.kubernetes.io/name: redis
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: redis
  template:
    metadata:
      labels:
        app.kubernetes.io/name: redis
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
      containers:
        - name: redis
          image: %s
          imagePullPolicy: IfNotPresent
          ports:
            - name: redis
              containerPort: 6379
          resources:
            requests:
              cpu: %q
              memory: %q
            limits:
              memory: %q
          volumeMounts:
            - name: data
              mountPath: /data
      volumes:
        - name: data
          emptyDir: {}
`, lkeNamespaceName(env, "platform"), env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], lkeRedisImage(), firstNonEmpty(os.Getenv("LKE_REDIS_REQUEST_CPU"), env["LKE_REDIS_REQUEST_CPU"], "100m"), firstNonEmpty(os.Getenv("LKE_REDIS_REQUEST_MEMORY"), env["LKE_REDIS_REQUEST_MEMORY"], "128Mi"), firstNonEmpty(os.Getenv("LKE_REDIS_LIMIT_MEMORY"), env["LKE_REDIS_LIMIT_MEMORY"], "512Mi"))
}

func lkeRedisServiceManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: redis
  namespace: %s
  labels:
    app.kubernetes.io/name: redis
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: redis
  ports:
    - name: redis
      port: 6379
      targetPort: 6379
`, lkeNamespaceName(env, "platform"), env["CLOUD_STACK_NAME"])
}

func lkeRedisExporterDeploymentManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: redis-exporter
  namespace: %s
  labels:
    app.kubernetes.io/name: redis-exporter
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: redis-exporter
  template:
    metadata:
      labels:
        app.kubernetes.io/name: redis-exporter
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
      containers:
        - name: redis-exporter
          image: %s
          imagePullPolicy: IfNotPresent
          ports:
            - name: metrics
              containerPort: 9121
          env:
            - name: REDIS_ADDR
              value: %q
          resources:
            requests:
              cpu: %q
              memory: %q
            limits:
              memory: %q
`, lkeNamespaceName(env, "platform"), env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], lkeRedisExporterImage(), "redis://"+lkeRedisServiceHost(env)+":6379", firstNonEmpty(os.Getenv("LKE_REDIS_EXPORTER_REQUEST_CPU"), env["LKE_REDIS_EXPORTER_REQUEST_CPU"], "50m"), firstNonEmpty(os.Getenv("LKE_REDIS_EXPORTER_REQUEST_MEMORY"), env["LKE_REDIS_EXPORTER_REQUEST_MEMORY"], "64Mi"), firstNonEmpty(os.Getenv("LKE_REDIS_EXPORTER_LIMIT_MEMORY"), env["LKE_REDIS_EXPORTER_LIMIT_MEMORY"], "256Mi"))
}

func lkeRedisExporterServiceManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: redis-exporter
  namespace: %s
  labels:
    app.kubernetes.io/name: redis-exporter
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: redis-exporter
  ports:
    - name: metrics
      port: 9121
      targetPort: 9121
`, lkeNamespaceName(env, "platform"), env["CLOUD_STACK_NAME"])
}

type lkeCertIssuerMaterial struct {
	ServerCert   string
	ServerKey    string
	ServiceCA    string
	ClientCert   string
	ClientKey    string
	FactoryCert  string
	FactoryKey   string
	RootCACert   string
	DeviceCACert string
	AppCACert    string
}

type lkeMQTTMaterial struct {
	ServerCert string
	ServerKey  string
}

type lkeOpenBaoTLSMaterial struct {
	CACert     string
	ServerCert string
	ServerKey  string
}

type lkeOpenBaoBootstrapResult struct {
	RoleID       string
	SecretID     string
	TLSCACert    string
	RootCACert   string
	DeviceCACert string
	AppCACert    string
}

type lkeOpenBaoStatus struct {
	Initialized bool `json:"initialized"`
	Sealed      bool `json:"sealed"`
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
	return lkeCertIssuerMaterial{
		ServerCert:  serverCertPEM,
		ServerKey:   serverKeyPEM,
		ServiceCA:   serviceCACertPEM,
		ClientCert:  clientCertPEM,
		ClientKey:   clientKeyPEM,
		FactoryCert: factoryCertPEM,
		FactoryKey:  factoryKeyPEM,
	}, nil
}

func loadOrCreateLKECertIssuerMaterial(paths provisionPaths, env map[string]string) (lkeCertIssuerMaterial, error) {
	stateDir := filepath.Join(paths.EnvRoot, "state", "certissuer")
	files := map[string]*string{
		"server.crt":     nil,
		"server.key":     nil,
		"service-ca.crt": nil,
		"client.crt":     nil,
		"client.key":     nil,
		"factory.crt":    nil,
		"factory.key":    nil,
	}
	exists := false
	for name := range files {
		if fileExists(filepath.Join(stateDir, name)) {
			exists = true
			break
		}
	}
	if exists {
		for _, name := range []string{"server.key", "client.key", "factory.key"} {
			keyPath := filepath.Join(stateDir, name)
			if !fileExists(keyPath) {
				return lkeCertIssuerMaterial{}, fmt.Errorf("certissuer TLS state is incomplete under %s", stateDir)
			}
			isEd25519, err := lkePEMPrivateKeyIsEd25519(keyPath)
			if err != nil {
				return lkeCertIssuerMaterial{}, err
			}
			if !isEd25519 {
				if err := os.RemoveAll(stateDir); err != nil {
					return lkeCertIssuerMaterial{}, err
				}
				exists = false
				break
			}
		}
	}
	if exists {
		readPublic := func(name string) (string, error) {
			body, err := os.ReadFile(filepath.Join(stateDir, name))
			if err != nil {
				return "", err
			}
			return string(body), nil
		}
		readPrivate := func(name, label string) (string, error) {
			return readSensitiveFile(filepath.Join(stateDir, name), label)
		}
		serverCert, err := readPublic("server.crt")
		if err != nil {
			return lkeCertIssuerMaterial{}, err
		}
		serverKey, err := readPrivate("server.key", "certissuer server private key")
		if err != nil {
			return lkeCertIssuerMaterial{}, err
		}
		serviceCA, err := readPublic("service-ca.crt")
		if err != nil {
			return lkeCertIssuerMaterial{}, err
		}
		clientCert, err := readPublic("client.crt")
		if err != nil {
			return lkeCertIssuerMaterial{}, err
		}
		clientKey, err := readPrivate("client.key", "certissuer account-manager client private key")
		if err != nil {
			return lkeCertIssuerMaterial{}, err
		}
		factoryCert, err := readPublic("factory.crt")
		if err != nil {
			return lkeCertIssuerMaterial{}, err
		}
		factoryKey, err := readPrivate("factory.key", "certissuer factory client private key")
		if err != nil {
			return lkeCertIssuerMaterial{}, err
		}
		return lkeCertIssuerMaterial{
			ServerCert:  serverCert,
			ServerKey:   serverKey,
			ServiceCA:   serviceCA,
			ClientCert:  clientCert,
			ClientKey:   clientKey,
			FactoryCert: factoryCert,
			FactoryKey:  factoryKey,
		}, nil
	}
	material, err := newLKECertIssuerMaterial(env)
	if err != nil {
		return lkeCertIssuerMaterial{}, err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return lkeCertIssuerMaterial{}, err
	}
	writes := map[string]string{
		"server.crt":     material.ServerCert,
		"service-ca.crt": material.ServiceCA,
		"client.crt":     material.ClientCert,
		"factory.crt":    material.FactoryCert,
	}
	for name, value := range writes {
		if err := os.WriteFile(filepath.Join(stateDir, name), []byte(value), 0o600); err != nil {
			return lkeCertIssuerMaterial{}, err
		}
	}
	privateWrites := map[string]string{
		"server.key":  material.ServerKey,
		"client.key":  material.ClientKey,
		"factory.key": material.FactoryKey,
	}
	for name, value := range privateWrites {
		if err := writeSensitiveFile(filepath.Join(stateDir, name), value); err != nil {
			return lkeCertIssuerMaterial{}, err
		}
	}
	return material, nil
}

func newLKEOpenBaoTLSMaterial(env map[string]string) (lkeOpenBaoTLSMaterial, error) {
	caCert, caKey, caCertPEM, _, err := newLKECertificateAuthority("rtk-lke-openbao-tls-ca")
	if err != nil {
		return lkeOpenBaoTLSMaterial{}, err
	}
	serverCert, serverKey, err := newLKESignedCertificate(caCert, caKey, "openbao", lkeOpenBaoDNSNames(env), nil, []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth})
	if err != nil {
		return lkeOpenBaoTLSMaterial{}, err
	}
	return lkeOpenBaoTLSMaterial{CACert: caCertPEM, ServerCert: serverCert, ServerKey: serverKey}, nil
}

func loadOrCreateLKEOpenBaoTLSMaterial(paths provisionPaths, env map[string]string) (lkeOpenBaoTLSMaterial, error) {
	stateDir := firstNonEmpty(os.Getenv("RTK_CLOUD_OPENBAO_STATE_DIR"), filepath.Join(paths.EnvRoot, "state", "openbao"))
	caPath := filepath.Join(stateDir, "tls-ca.crt")
	certPath := filepath.Join(stateDir, "tls.crt")
	keyPath := filepath.Join(stateDir, "tls.key")
	if fileExists(caPath) || fileExists(certPath) || fileExists(keyPath) {
		if !fileExists(caPath) || !fileExists(certPath) || !fileExists(keyPath) {
			return lkeOpenBaoTLSMaterial{}, fmt.Errorf("OpenBao TLS state is incomplete under %s", stateDir)
		}
		isEd25519, err := lkePEMPrivateKeyIsEd25519(keyPath)
		if err != nil {
			return lkeOpenBaoTLSMaterial{}, err
		}
		if !isEd25519 {
			for _, path := range []string{caPath, certPath, keyPath} {
				if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
					return lkeOpenBaoTLSMaterial{}, err
				}
			}
			return loadOrCreateLKEOpenBaoTLSMaterial(paths, env)
		}
		ca, err := os.ReadFile(caPath)
		if err != nil {
			return lkeOpenBaoTLSMaterial{}, err
		}
		cert, err := os.ReadFile(certPath)
		if err != nil {
			return lkeOpenBaoTLSMaterial{}, err
		}
		key, err := readSensitiveFile(keyPath, "OpenBao TLS private key")
		if err != nil {
			return lkeOpenBaoTLSMaterial{}, err
		}
		return lkeOpenBaoTLSMaterial{CACert: string(ca), ServerCert: string(cert), ServerKey: key}, nil
	}
	material, err := newLKEOpenBaoTLSMaterial(env)
	if err != nil {
		return lkeOpenBaoTLSMaterial{}, err
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return lkeOpenBaoTLSMaterial{}, err
	}
	if err := os.WriteFile(caPath, []byte(material.CACert), 0o600); err != nil {
		return lkeOpenBaoTLSMaterial{}, err
	}
	if err := os.WriteFile(certPath, []byte(material.ServerCert), 0o600); err != nil {
		return lkeOpenBaoTLSMaterial{}, err
	}
	if err := writeSensitiveFile(keyPath, material.ServerKey); err != nil {
		return lkeOpenBaoTLSMaterial{}, err
	}
	return material, nil
}

func lkePEMPrivateKeyIsEd25519(path string) (bool, error) {
	body, err := readSensitiveFile(path, "private key")
	if err != nil {
		return false, err
	}
	block, rest := pem.Decode([]byte(body))
	if block == nil || len(strings.TrimSpace(string(rest))) != 0 {
		return false, fmt.Errorf("invalid PEM private key at %s", path)
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		_, ok := key.(ed25519.PrivateKey)
		return ok, nil
	}
	if _, err := x509.ParseECPrivateKey(block.Bytes); err == nil {
		return false, nil
	}
	if _, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return false, nil
	}
	return false, fmt.Errorf("unsupported PEM private key at %s", path)
}

func newLKECertificatePrivateKey() (crypto.Signer, string, error) {
	_, edKey, err := ed25519.GenerateKey(rand.Reader)
	if err == nil {
		keyDER, err := x509.MarshalPKCS8PrivateKey(edKey)
		if err != nil {
			return nil, "", err
		}
		return edKey, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})), nil
	}

	p256Key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, "", err
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(p256Key)
	if err != nil {
		return nil, "", err
	}
	return p256Key, string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})), nil
}

func newLKECertificateAuthority(commonName string) (*x509.Certificate, crypto.Signer, string, string, error) {
	key, keyPEM, err := newLKECertificatePrivateKey()
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
	der, err := x509.CreateCertificate(rand.Reader, template, template, key.Public(), key)
	if err != nil {
		return nil, nil, "", "", err
	}
	return template, key,
		string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		keyPEM,
		nil
}

func newLKESignedCertificate(caCert *x509.Certificate, caKey crypto.Signer, commonName string, dnsNames []string, ipAddresses []net.IP, usages []x509.ExtKeyUsage) (string, string, error) {
	key, keyPEM, err := newLKECertificatePrivateKey()
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
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  usages,
		DNSNames:     dnsNames,
		IPAddresses:  ipAddresses,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, caCert, key.Public(), caKey)
	if err != nil {
		return "", "", err
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})),
		keyPEM,
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

func lkeOpenBaoDNSNames(env map[string]string) []string {
	namespace := lkeNamespaceName(env, "secrets")
	return []string{
		"openbao",
		"openbao." + namespace,
		"openbao." + namespace + ".svc",
		"openbao." + namespace + ".svc.cluster.local",
		"openbao-0.openbao-internal",
		"openbao-0.openbao-internal." + namespace,
		"openbao-0.openbao-internal." + namespace + ".svc",
		"openbao-0.openbao-internal." + namespace + ".svc.cluster.local",
	}
}

func lkeEnsureOpenBao(paths provisionPaths, env map[string]string, tls lkeOpenBaoTLSMaterial) (lkeOpenBaoBootstrapResult, error) {
	tlsUnchanged := lkeOpenBaoTLSSecretUnchanged(env, tls)
	if err := kubectlApply(lkeOpenBaoTLSSecretManifest(env, tls)); err != nil {
		return lkeOpenBaoBootstrapResult{}, err
	}
	valuesPath, cleanup, err := writeLKEOpenBaoHelmValues(env)
	if err != nil {
		return lkeOpenBaoBootstrapResult{}, err
	}
	defer cleanup()
	if err := runHelm("repo", "add", "openbao", "https://openbao.github.io/openbao-helm", "--force-update"); err != nil {
		return lkeOpenBaoBootstrapResult{}, err
	}
	namespace := lkeNamespaceName(env, "secrets")
	chartVersion := lkeOpenBaoChartVersion()
	templateArgs := []string{"template", "openbao", "openbao/openbao", "--version", chartVersion, "--namespace", namespace, "-f", valuesPath}
	if os.Getenv("LKE_OPENBAO_HELM_REPO_UPDATE") == "1" {
		if err := runHelm("repo", "update", "openbao"); err != nil {
			return lkeOpenBaoBootstrapResult{}, err
		}
	}
	if err := runHelmQuiet(templateArgs...); err != nil {
		if os.Getenv("LKE_OPENBAO_HELM_REPO_UPDATE") != "1" {
			if updateErr := runHelm("repo", "update", "openbao"); updateErr != nil {
				return lkeOpenBaoBootstrapResult{}, updateErr
			}
			err = runHelmQuiet(templateArgs...)
		}
		if err != nil {
			return lkeOpenBaoBootstrapResult{}, err
		}
	}
	if err := runHelm("upgrade", "--install", "openbao", "openbao/openbao", "--version", chartVersion, "--namespace", namespace, "-f", valuesPath, "--wait", "--timeout", firstNonEmpty(os.Getenv("LKE_OPENBAO_HELM_TIMEOUT"), "10m")); err != nil {
		return lkeOpenBaoBootstrapResult{}, err
	}
	if os.Getenv("LKE_OPENBAO_RESTART_AFTER_HELM") == "0" || tlsUnchanged {
		if err := runKubectl("-n", namespace, "wait", "--for=jsonpath={.status.phase}=Running", "pod/openbao-0", "--timeout", firstNonEmpty(os.Getenv("LKE_OPENBAO_ROLLOUT_TIMEOUT"), "10m")); err != nil {
			return lkeOpenBaoBootstrapResult{}, err
		}
	} else {
		if err := lkeRestartOpenBaoPod(env); err != nil {
			return lkeOpenBaoBootstrapResult{}, err
		}
	}
	result, err := lkeBootstrapOpenBao(paths, env)
	if err != nil && lkeIsOpenBaoTLSMismatch(err) {
		if restartErr := lkeRestartOpenBaoPod(env); restartErr != nil {
			return lkeOpenBaoBootstrapResult{}, restartErr
		}
		result, err = lkeBootstrapOpenBao(paths, env)
	}
	if err != nil {
		return lkeOpenBaoBootstrapResult{}, err
	}
	result.TLSCACert = tls.CACert
	return result, nil
}

func lkeOpenBaoTLSSecretUnchanged(env map[string]string, tls lkeOpenBaoTLSMaterial) bool {
	namespace := lkeNamespaceName(env, "secrets")
	out, err := kubectlCombinedOutput(nil, "-n", namespace, "get", "secret", "openbao-tls", "-o", "json")
	if err != nil {
		return false
	}
	var parsed struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return false
	}
	return parsed.Data["ca.crt"] == base64.StdEncoding.EncodeToString([]byte(tls.CACert)) &&
		parsed.Data["tls.crt"] == base64.StdEncoding.EncodeToString([]byte(tls.ServerCert)) &&
		parsed.Data["tls.key"] == base64.StdEncoding.EncodeToString([]byte(tls.ServerKey))
}

func lkeOpenBaoChartVersion() string {
	return firstNonEmpty(os.Getenv("LKE_OPENBAO_CHART_VERSION"), "0.28.3")
}

func lkeIsOpenBaoTLSMismatch(err error) bool {
	msg := err.Error()
	return strings.Contains(msg, "certificate signed by unknown authority") || strings.Contains(msg, "ECDSA verification failure")
}

func lkeRestartOpenBaoPod(env map[string]string) error {
	namespace := lkeNamespaceName(env, "secrets")
	if err := runKubectl("-n", namespace, "delete", "pod/openbao-0", "--ignore-not-found=true", "--wait=true", "--timeout", "2m"); err != nil {
		return err
	}
	deadline := time.Now().Add(lkeOpenBaoWaitTimeout())
	var lastOut []byte
	var lastErr error
	for time.Now().Before(deadline) {
		lastOut, lastErr = kubectlCombinedOutput(nil, "-n", namespace, "get", "pod/openbao-0", "-o", "jsonpath={.status.phase}")
		if lastErr == nil && strings.TrimSpace(string(lastOut)) == "Running" {
			fmt.Fprintln(os.Stdout, "pod/openbao-0 condition met")
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("wait for restarted OpenBao pod: %w: %s", lastErr, strings.TrimSpace(string(lastOut)))
}

func lkeOpenBaoWaitTimeout() time.Duration {
	timeout, err := time.ParseDuration(firstNonEmpty(os.Getenv("LKE_OPENBAO_ROLLOUT_TIMEOUT"), "10m"))
	if err != nil {
		return 10 * time.Minute
	}
	return timeout
}

func writeLKEOpenBaoHelmValues(env map[string]string) (string, func(), error) {
	dir, err := os.MkdirTemp("", "rtk-lke-openbao-values-*")
	if err != nil {
		return "", func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(dir) }
	path := filepath.Join(dir, "values.yaml")
	authDelegatorEnabled := os.Getenv("RUNTIME_COVERAGE_SHARED_CLUSTER") != "1"
	persistentStorageEnabled := os.Getenv("RUNTIME_COVERAGE_SHARED_CLUSTER") != "1"
	ephemeralVolumes := ""
	ephemeralMounts := ""
	if !persistentStorageEnabled {
		ephemeralVolumes = `    - name: runtime-openbao-data
      emptyDir: {}
    - name: runtime-openbao-audit
      emptyDir: {}
`
		ephemeralMounts = `    - name: runtime-openbao-data
      mountPath: /openbao/data
    - name: runtime-openbao-audit
      mountPath: /openbao/audit
`
	}
	body := fmt.Sprintf(`global:
  tlsDisable: false
injector:
  enabled: false
server:
  enabled: true
  authDelegator:
    enabled: %t
  standalone:
    enabled: true
    config: |
      ui = true

      listener "tcp" {
        tls_disable = 0
        tls_cert_file = "/openbao/tls/tls.crt"
        tls_key_file = "/openbao/tls/tls.key"
        address = "[::]:8200"
        cluster_address = "[::]:8201"
      }

      storage "file" {
        path = "/openbao/data"
      }

      audit "file" "file" {
        options = {
          file_path = "/openbao/audit/openbao-audit.log"
        }
      }
  ha:
    enabled: false
  service:
    type: ClusterIP
  dataStorage:
    enabled: %t
    size: %s
  auditStorage:
    enabled: %t
    size: %s
  volumes:
    - name: openbao-tls
      secret:
        secretName: openbao-tls
%s
  volumeMounts:
    - name: openbao-tls
      mountPath: /openbao/tls
      readOnly: true
%s
`, authDelegatorEnabled,
		persistentStorageEnabled, firstNonEmpty(os.Getenv("LKE_OPENBAO_DATA_STORAGE"), "10Gi"),
		persistentStorageEnabled, firstNonEmpty(os.Getenv("LKE_OPENBAO_AUDIT_STORAGE"), "5Gi"),
		ephemeralVolumes, ephemeralMounts)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		cleanup()
		return "", func() {}, err
	}
	return path, cleanup, nil
}

func lkeOpenBaoTLSSecretManifest(env map[string]string, material lkeOpenBaoTLSMaterial) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: openbao-tls
  namespace: %s
  labels:
    app.kubernetes.io/name: openbao
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
type: kubernetes.io/tls
stringData:
  ca.crt: %q
  tls.crt: %q
  tls.key: %q
`, lkeNamespaceName(env, "secrets"), env["CLOUD_STACK_NAME"], material.CACert, material.ServerCert, material.ServerKey)
}

func lkeBootstrapOpenBao(paths provisionPaths, env map[string]string) (lkeOpenBaoBootstrapResult, error) {
	status, err := lkeOpenBaoStatusValue(env)
	if err != nil {
		return lkeOpenBaoBootstrapResult{}, err
	}
	stateDir := firstNonEmpty(os.Getenv("RTK_CLOUD_OPENBAO_STATE_DIR"), filepath.Join(paths.EnvRoot, "state", "openbao"))
	if !status.Initialized {
		if err := os.MkdirAll(stateDir, 0o700); err != nil {
			return lkeOpenBaoBootstrapResult{}, err
		}
		out, err := openBaoExecOutput(env, "operator", "init", "-key-shares=1", "-key-threshold=1", "-format=json")
		if err != nil {
			return lkeOpenBaoBootstrapResult{}, err
		}
		var initOut struct {
			UnsealKeysB64 []string `json:"unseal_keys_b64"`
			RootToken     string   `json:"root_token"`
		}
		if err := json.Unmarshal([]byte(out), &initOut); err != nil {
			return lkeOpenBaoBootstrapResult{}, fmt.Errorf("decode OpenBao init output: %w", err)
		}
		if len(initOut.UnsealKeysB64) == 0 || strings.TrimSpace(initOut.RootToken) == "" {
			return lkeOpenBaoBootstrapResult{}, errors.New("OpenBao init output missing unseal key or root token")
		}
		if err := writeSensitiveFile(filepath.Join(stateDir, "unseal-key"), initOut.UnsealKeysB64[0]); err != nil {
			return lkeOpenBaoBootstrapResult{}, err
		}
		if err := writeSensitiveFile(filepath.Join(stateDir, "root-token"), initOut.RootToken); err != nil {
			return lkeOpenBaoBootstrapResult{}, err
		}
		status.Initialized = true
		status.Sealed = true
	}
	unsealKey, err := readSensitiveFile(filepath.Join(stateDir, "unseal-key"), "OpenBao unseal key")
	if err != nil {
		return lkeOpenBaoBootstrapResult{}, err
	}
	rootToken, err := readSensitiveFile(filepath.Join(stateDir, "root-token"), "OpenBao root token")
	if err != nil {
		return lkeOpenBaoBootstrapResult{}, err
	}
	if status.Sealed {
		if _, err := openBaoSensitiveScript(env, unsealKey, `bao operator unseal "$OPENBAO_SECRET_INPUT" >/dev/null`); err != nil {
			return lkeOpenBaoBootstrapResult{}, err
		}
	}
	out, err := openBaoSensitiveScript(env, rootToken, lkeOpenBaoBootstrapScript(env))
	if err != nil {
		return lkeOpenBaoBootstrapResult{}, err
	}
	result, err := parseLKEOpenBaoBootstrapOutput(out)
	if err != nil {
		return lkeOpenBaoBootstrapResult{}, err
	}
	if result.RoleID == "" || result.SecretID == "" || result.RootCACert == "" || result.DeviceCACert == "" || result.AppCACert == "" {
		return lkeOpenBaoBootstrapResult{}, errors.New("OpenBao bootstrap output missing role_id, secret_id, or CA certificate")
	}
	return result, nil
}

func lkeOpenBaoStatusValue(env map[string]string) (lkeOpenBaoStatus, error) {
	namespace := lkeNamespaceName(env, "secrets")
	argv := []string{"-n", namespace, "exec", "openbao-0", "--", "env", "BAO_ADDR=" + lkeOpenBaoAddr(env), "BAO_CACERT=/openbao/tls/ca.crt", "bao", "status", "-format=json"}
	out, statusErr := kubectlCombinedOutput(nil, argv...)
	var status lkeOpenBaoStatus
	statusJSON := extractJSONObject(string(out))
	if err := json.Unmarshal([]byte(statusJSON), &status); err != nil {
		if statusErr != nil {
			return lkeOpenBaoStatus{}, fmt.Errorf("kubectl exec openbao status -format=json: %w: %s", statusErr, strings.TrimSpace(string(out)))
		}
		return lkeOpenBaoStatus{}, fmt.Errorf("decode OpenBao status: %w", err)
	}
	return status, nil
}

func extractJSONObject(out string) string {
	start := strings.Index(out, "{")
	end := strings.LastIndex(out, "}")
	if start >= 0 && end >= start {
		return out[start : end+1]
	}
	return out
}

func lkeOpenBaoBootstrapScript(env map[string]string) string {
	rootCN := firstNonEmpty(os.Getenv("LKE_OPENBAO_ROOT_CN"), lkeName(firstNonEmpty(env["CLOUD_STACK_NAME"], "video-cloud-staging"))+"-root-ca")
	deviceCN := firstNonEmpty(os.Getenv("LKE_OPENBAO_DEVICE_INTERMEDIATE_CN"), lkeName(firstNonEmpty(env["CLOUD_STACK_NAME"], "video-cloud-staging"))+"-device-ca")
	appCN := firstNonEmpty(os.Getenv("LKE_OPENBAO_APP_INTERMEDIATE_CN"), lkeName(firstNonEmpty(env["CLOUD_STACK_NAME"], "video-cloud-staging"))+"-app-ca")
	return fmt.Sprintf(`
export BAO_TOKEN="$OPENBAO_SECRET_INPUT"
ensure_pki() {
  path="$1"
  max_ttl="$2"
  if ! bao secrets enable -path="$path" pki >/tmp/openbao-enable.err 2>&1; then
    if ! grep -q "path is already in use" /tmp/openbao-enable.err; then
      cat /tmp/openbao-enable.err >&2
      exit 1
    fi
  fi
  bao secrets tune -max-lease-ttl="$max_ttl" "$path" >/dev/null
}
has_ca() {
  bao read -field=certificate "$1/cert/ca" >/dev/null 2>&1
}
ensure_pki pki/root 87600h
ensure_pki pki/device 26280h
ensure_pki pki/app 26280h
if ! has_ca pki/root; then
  bao write -field=certificate pki/root/root/generate/internal common_name=%s ttl=87600h >/tmp/openbao-root-ca.crt
fi
if ! has_ca pki/device; then
  bao write -field=csr pki/device/intermediate/generate/internal common_name=%s ttl=26280h >/tmp/openbao-device.csr
  bao write -field=certificate pki/root/root/sign-intermediate csr=@/tmp/openbao-device.csr common_name=%s ttl=26280h >/tmp/openbao-device.crt
  bao write pki/device/intermediate/set-signed certificate=@/tmp/openbao-device.crt >/dev/null
fi
if ! has_ca pki/app; then
  bao write -field=csr pki/app/intermediate/generate/internal common_name=%s ttl=26280h >/tmp/openbao-app.csr
  bao write -field=certificate pki/root/root/sign-intermediate csr=@/tmp/openbao-app.csr common_name=%s ttl=26280h >/tmp/openbao-app.crt
  bao write pki/app/intermediate/set-signed certificate=@/tmp/openbao-app.crt >/dev/null
fi
	bao write pki/device/roles/factory-device - >/dev/null <<'JSON'
{"allow_any_name":true,"enforce_hostnames":false,"cn_validations":["disabled"],"server_flag":false,"client_flag":true,"key_type":"any","key_usage":["DigitalSignature"],"ext_key_usage":["ClientAuth"],"ttl":"8760h","max_ttl":"26280h"}
JSON
	bao write pki/device/roles/gateway-server \
	  allow_any_name=true enforce_hostnames=false server_flag=true client_flag=false \
	  key_type=any key_usage=DigitalSignature ext_key_usage=ServerAuth \
	  ttl=8760h max_ttl=26280h >/dev/null
	bao write pki/app/roles/app-user \
	  allow_any_name=true enforce_hostnames=false server_flag=false client_flag=true \
	  key_type=any key_usage=DigitalSignature ext_key_usage=ClientAuth \
	  ttl=8760h max_ttl=26280h >/dev/null
if ! bao auth enable approle >/tmp/openbao-auth.err 2>&1; then
  if ! grep -q "path is already in use" /tmp/openbao-auth.err; then
    cat /tmp/openbao-auth.err >&2
    exit 1
  fi
fi
cat >/tmp/video-cloud-certissuer-policy.hcl <<'POLICY'
path "pki/device/sign/factory-device" { capabilities = ["update"] }
path "pki/device/sign/gateway-server" { capabilities = ["update"] }
path "pki/app/sign/app-user" { capabilities = ["update"] }
path "pki/device/cert/ca" { capabilities = ["read"] }
path "pki/app/cert/ca" { capabilities = ["read"] }
path "pki/root/cert/ca" { capabilities = ["read"] }
path "pki/device/ca_chain" { capabilities = ["read"] }
path "pki/app/ca_chain" { capabilities = ["read"] }
POLICY
bao policy write video-cloud-certissuer /tmp/video-cloud-certissuer-policy.hcl >/dev/null
bao write auth/approle/role/video-cloud-certissuer \
  token_policies=video-cloud-certissuer token_ttl=1h token_max_ttl=24h \
  secret_id_ttl=0 secret_id_num_uses=0 >/dev/null
role_id="$(bao read -field=role_id auth/approle/role/video-cloud-certissuer/role-id)"
secret_id="$(bao write -f -field=secret_id auth/approle/role/video-cloud-certissuer/secret-id)"
root_ca="$(bao read -field=certificate pki/root/cert/ca | base64 | tr -d '\n')"
device_ca="$(bao read -field=certificate pki/device/cert/ca | base64 | tr -d '\n')"
app_ca="$(bao read -field=certificate pki/app/cert/ca | base64 | tr -d '\n')"
printf 'ROLE_ID=%%s\n' "$role_id"
printf 'SECRET_ID=%%s\n' "$secret_id"
printf 'ROOT_CA_CERT_B64=%%s\n' "$root_ca"
printf 'DEVICE_CA_CERT_B64=%%s\n' "$device_ca"
printf 'APP_CA_CERT_B64=%%s\n' "$app_ca"
`, strconv.Quote(rootCN), strconv.Quote(deviceCN), strconv.Quote(deviceCN), strconv.Quote(appCN), strconv.Quote(appCN))
}

func parseLKEOpenBaoBootstrapOutput(out string) (lkeOpenBaoBootstrapResult, error) {
	values := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		values[strings.TrimSpace(key)] = strings.TrimSpace(value)
	}
	deviceCA, err := base64.StdEncoding.DecodeString(values["DEVICE_CA_CERT_B64"])
	if err != nil {
		return lkeOpenBaoBootstrapResult{}, fmt.Errorf("decode OpenBao device CA certificate: %w", err)
	}
	rootCA, err := base64.StdEncoding.DecodeString(values["ROOT_CA_CERT_B64"])
	if err != nil {
		return lkeOpenBaoBootstrapResult{}, fmt.Errorf("decode OpenBao root CA certificate: %w", err)
	}
	appCA, err := base64.StdEncoding.DecodeString(values["APP_CA_CERT_B64"])
	if err != nil {
		return lkeOpenBaoBootstrapResult{}, fmt.Errorf("decode OpenBao app CA certificate: %w", err)
	}
	return lkeOpenBaoBootstrapResult{
		RoleID:       values["ROLE_ID"],
		SecretID:     values["SECRET_ID"],
		RootCACert:   string(rootCA),
		DeviceCACert: string(deviceCA),
		AppCACert:    string(appCA),
	}, nil
}

func openBaoExecOutput(env map[string]string, args ...string) (string, error) {
	namespace := lkeNamespaceName(env, "secrets")
	argv := []string{"-n", namespace, "exec", "openbao-0", "--", "env", "BAO_ADDR=" + lkeOpenBaoAddr(env), "BAO_CACERT=/openbao/tls/ca.crt", "bao"}
	argv = append(argv, args...)
	out, err := kubectlCombinedOutput(nil, argv...)
	if err != nil {
		return "", fmt.Errorf("kubectl exec openbao %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func openBaoSensitiveScript(env map[string]string, secret, script string) (string, error) {
	namespace := lkeNamespaceName(env, "secrets")
	prologue := fmt.Sprintf("set -euo pipefail\nOPENBAO_SECRET_INPUT=%s\nexport BAO_ADDR=%s\nexport BAO_CACERT=/openbao/tls/ca.crt\n", shellSingleQuote(secret), strconv.Quote(lkeOpenBaoAddr(env)))
	out, err := kubectlCombinedOutput(strings.NewReader(prologue+script+"\n"), "-n", namespace, "exec", "-i", "openbao-0", "--", "sh", "-s")
	if err != nil {
		return "", fmt.Errorf("kubectl exec openbao bootstrap failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func lkeOpenBaoAddr(env map[string]string) string {
	return "https://openbao." + lkeNamespaceName(env, "secrets") + ".svc.cluster.local:8200"
}

func writeSensitiveFile(path, value string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(strings.TrimSpace(value)+"\n"), 0o600)
}

func readSensitiveFile(path, label string) (string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("%s is required at %s; existing OpenBao instances cannot be managed without local operator state", label, path)
		}
		return "", err
	}
	value := strings.TrimSpace(string(body))
	if value == "" {
		return "", fmt.Errorf("%s at %s is empty", label, path)
	}
	return value, nil
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func runHelm(args ...string) error {
	cmd := exec.Command(lkeHelm(), lkeHelmArgs(args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func runHelmWithTimeout(timeout time.Duration, args ...string) error {
	if timeout <= 0 {
		return runHelm(args...)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, lkeHelm(), lkeHelmArgs(args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("helm command timed out after %s: %s", timeout, strings.Join(args, " "))
	}
	return err
}

func runHelmQuiet(args ...string) error {
	cmd := exec.Command(lkeHelm(), lkeHelmArgs(args...)...)
	cmd.Stdout = io.Discard
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func lkeHelm() string {
	return firstNonEmpty(os.Getenv("RTK_CLOUD_HELM"), "helm")
}

func lkeHelmArgs(args ...string) []string {
	if kubeconfig := os.Getenv("RTK_CLOUD_LKE_KUBECONFIG"); kubeconfig != "" {
		return append([]string{"--kubeconfig", kubeconfig}, args...)
	}
	return args
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
  root-ca.crt: %q
  device-ca.crt: %q
  app-ca.crt: %q
  ca.crt: %q
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], lkeRuntimeSecretValue("postgres"), material.ServerCert, material.ServerKey, material.ServiceCA, material.RootCACert, material.DeviceCACert, material.AppCACert, lkeClientCABundle(material.RootCACert, material.DeviceCACert, material.AppCACert))
}

func lkeCertIssuerOpenBaoAuthSecretManifest(env map[string]string, openBao lkeOpenBaoBootstrapResult) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: certissuer-openbao-auth
  namespace: %s
  labels:
    app.kubernetes.io/name: certissuer
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
type: Opaque
stringData:
  role_id: %q
  secret_id: %q
  ca.crt: %q
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], openBao.RoleID, openBao.SecretID, openBao.TLSCACert)
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
  FACTORY_ENROLL_PRODUCTION_JWT_SECRET: %q
  FACTORY_ENROLL_PRODUCTION_JWT_AUDIENCE: %q
  POSTGRES_PASSWORD: %q
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], lkeFactoryEnrollAuthKey(env), lkeFactoryProductionJWTSecret(env), lkeFactoryProductionJWTAudience(env), lkeRuntimeSecretValue("postgres"))
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
  VIDEO_CLOUD_AUTH_SECRET: %q
  VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN: %q
  VIDEO_CLOUD_LOGGER_TOKEN: %q
  VIDEO_CLOUD_BILLING_USAGE_LOGGER_TOKEN: %q
  VIDEO_CLOUD_TURN_SHARED_SECRET: %q
  VIDEO_CLOUD_MQTT_BROKER_AUTH_KEY: %q
  VIDEO_CLOUD_MQTT_SERVER_PASSWORD: %q
  AWS_ACCESS_KEY_ID: %q
  AWS_SECRET_ACCESS_KEY: %q
  clip-private-key.pem: %q
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], lkeRuntimeSecretValue("postgres"), lkeRuntimeSecretValue("video-auth"), lkeInternalAuthToken(), lkeRuntimeSecretValue("cloud-logger-ingest-token"), lkeRuntimeSecretValue("cloud-logger-billing-usage-token"), lkeRuntimeSecretValue("turn-shared"), lkeRuntimeSecretValue("mqtt-broker-auth"), lkeRuntimeSecretValue("mqtt-server-password"), lkeObjectStorageCredential(env, "LINODE_OBJ_ACCESS_KEY_ID"), lkeObjectStorageCredential(env, "LINODE_OBJ_SECRET_ACCESS_KEY"), lkeClipPrivateKeyPEM())
}

func lkeClipPrivateKeyPEM() string {
	seed := sha256.Sum256([]byte(lkeRuntimeSecretValue("clip-private-key-seed")))
	n := elliptic.P256().Params().N
	d := new(big.Int).SetBytes(seed[:])
	d.Mod(d, new(big.Int).Sub(n, big.NewInt(1)))
	d.Add(d, big.NewInt(1))
	key := &ecdsa.PrivateKey{PublicKey: ecdsa.PublicKey{Curve: elliptic.P256()}, D: d}
	key.PublicKey.X, key.PublicKey.Y = key.PublicKey.Curve.ScalarBaseMult(d.Bytes())
	encoded, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		panic("marshal clip private key: " + err.Error())
	}
	return string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: encoded}))
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
  cert.pem: %q
  key.pem: %q
  cacert.pem: %q
  EMQX_HTTP_AUTHENTICATION: %q
  EMQX_DASHBOARD_PASSWORD: %q
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], material.ServerCert, material.ServerKey, material.ServerCert, material.ServerKey, material.ServerCert, lkeEMQXHTTPAuthentication(env), lkeRuntimeSecretValue("emqx-dashboard-password"))
}

func lkeEMQXHTTPAuthentication(env map[string]string) string {
	return fmt.Sprintf(`[{mechanism=password_based,backend=http,enable=true,method=post,url="http://video-cloud-api.%s.svc.cluster.local/v1/internal/mqtt/authenticate",headers={"content-type"="application/json","authorization"="Bearer %s"},body={listener="${listener}",username="${username}",password="${password}",clientid="${clientid}"},connect_timeout="5s",request_timeout="5s",pool_size=32}]`, lkeNamespaceName(env, "video-cloud"), lkeRuntimeSecretValue("mqtt-broker-auth"))
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
  broker: emqx
  base.hocon: |
%s
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], indentManifest(lkeEMQXTenantBaseHOCON(env), 4))
}

func lkeEMQXTenantBaseHOCON(env map[string]string) string {
	if !lkeMQTTTenantNamespaceEnabled(env) {
		return "rewrite = []\n"
	}
	return `authorization {
  no_match = deny
  deny_action = ignore
  cache.enable = false
  sources = []
}
rewrite = [
  {
    action = "all"
    source_topic = "#"
    re = "^(?!(?:_bc/|\\$share/[^/]+/_bc/))(.+)$"
    dest_topic = "_bc/${username}/$1"
  },
  {
    action = "all"
    source_topic = "$vc/#"
    re = "^(\\$vc/.*)$"
    dest_topic = "_bc/${username}/$1"
  }
]
`
}

func lkeMQTTTenantNamespaceEnabled(env map[string]string) bool {
	// Staging now validates every external MQTT connection through the tenant
	// namespace path. An explicit false remains available only for local
	// compatibility environments.
	return strings.EqualFold(strings.TrimSpace(firstNonEmpty(os.Getenv("LKE_MQTT_TENANT_NAMESPACE_ENABLED"), env["LKE_MQTT_TENANT_NAMESPACE_ENABLED"], "true")), "true")
}

func indentManifest(value string, spaces int) string {
	prefix := strings.Repeat(" ", spaces)
	lines := strings.Split(strings.TrimSuffix(value, "\n"), "\n")
	for idx := range lines {
		lines[idx] = prefix + lines[idx]
	}
	return strings.Join(lines, "\n")
}

func lkeEMQXListenerAuthEnvManifest(env map[string]string) string {
	if !lkeMQTTTenantNamespaceEnabled(env) {
		return ""
	}
	return `            - name: EMQX_LISTENERS__TCP__DEFAULT__AUTHENTICATION
              valueFrom:
                secretKeyRef:
                  name: mqtt-runtime
                  key: EMQX_HTTP_AUTHENTICATION
            - name: EMQX_LISTENERS__SSL__DEFAULT__AUTHENTICATION
              valueFrom:
                secretKeyRef:
                  name: mqtt-runtime
                  key: EMQX_HTTP_AUTHENTICATION
`
}

func lkeMQTTDeploymentManifest(env map[string]string) string {
	return lkeMQTTStatefulSetManifest(env)
}

func lkeMQTTStatefulSetManifest(env map[string]string) string {
	placement := lkeMQTTPlacementManifest(env)
	authEnabled := strconv.FormatBool(lkeMQTTTenantNamespaceEnabled(env))
	authEnv := lkeEMQXListenerAuthEnvManifest(env)
	return fmt.Sprintf(`apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: mqtt
  namespace: %s
  labels:
    app.kubernetes.io/name: mqtt
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  serviceName: mqtt-headless
  replicas: %d
  podManagementPolicy: Parallel
  updateStrategy:
    type: RollingUpdate
  selector:
    matchLabels:
      app.kubernetes.io/name: mqtt
  template:
    metadata:
      annotations:
        rtk.realtek.com/mqtt-config-checksum: %q
      labels:
        app.kubernetes.io/name: mqtt
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
%s
      containers:
        - name: mqtt
          image: %s
          imagePullPolicy: IfNotPresent
          env:
            - name: POD_NAME
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
            - name: POD_IP
              valueFrom:
                fieldRef:
                  fieldPath: status.podIP
            - name: EMQX_NODE__NAME
              value: "emqx@$(POD_NAME).mqtt-headless.%s.svc.cluster.local"
            - name: EMQX_NODE__COOKIE
              value: "%s"
            - name: EMQX_CLUSTER__DISCOVERY_STRATEGY
              value: "static"
            - name: EMQX_CLUSTER__STATIC__SEEDS
              value: "%s"
            - name: EMQX_LISTENERS__TCP__DEFAULT__BIND
              value: "0.0.0.0:1883"
            - name: EMQX_LISTENERS__TCP__DEFAULT__ENABLE_AUTHN
              value: %q
            - name: EMQX_LISTENERS__TCP__DEFAULT__ACCEPTORS
              value: "%s"
            - name: EMQX_LISTENERS__TCP__DEFAULT__TCP_OPTIONS__BACKLOG
              value: "%s"
            - name: EMQX_LISTENERS__SSL__DEFAULT__BIND
              value: "0.0.0.0:8883"
            - name: EMQX_LISTENERS__SSL__DEFAULT__ENABLE_AUTHN
              value: %q
            - name: EMQX_LISTENERS__SSL__DEFAULT__ACCEPTORS
              value: "%s"
            - name: EMQX_LISTENERS__SSL__DEFAULT__TCP_OPTIONS__BACKLOG
              value: "%s"
            - name: EMQX_LISTENERS__SSL__DEFAULT__SSL_OPTIONS__CERTFILE
              value: /opt/emqx/etc/certs/tls.crt
            - name: EMQX_LISTENERS__SSL__DEFAULT__SSL_OPTIONS__KEYFILE
              value: /opt/emqx/etc/certs/tls.key
            - name: EMQX_FORCE_SHUTDOWN__MAX_MAILBOX_SIZE
              value: "%s"
            - name: EMQX_FORCE_SHUTDOWN__MAX_HEAP_SIZE
              value: "%s"
            - name: EMQX_DASHBOARD__DEFAULT_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: mqtt-runtime
                  key: EMQX_DASHBOARD_PASSWORD
%s
%s
          ports:
            - name: mqtt
              containerPort: 1883
            - name: mqtts
              containerPort: 8883
            - name: dashboard
              containerPort: 18083
          volumeMounts:
            - name: mqtt-runtime
              mountPath: /opt/emqx/etc/certs
              readOnly: true
            - name: mqtt-config
              mountPath: /opt/emqx/etc/base.hocon
              subPath: base.hocon
              readOnly: true
      volumes:
        - name: mqtt-runtime
          secret:
            secretName: mqtt-runtime
        - name: mqtt-config
          configMap:
            name: mqtt-config
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], lkeMQTTReplicas(env), lkeConfigChecksum(lkeEMQXTenantBaseHOCON(env), lkeEMQXHTTPAuthentication(env)), env["CLOUD_STACK_NAME"], placement, firstNonEmpty(os.Getenv("LKE_MQTT_IMAGE"), "emqx/emqx:5.8.7"), lkeNamespaceName(env, "video-cloud"), lkeEMQXNodeCookie(env), lkeEMQXStaticSeeds(env), authEnabled, lkeEMQXListenerAcceptors(env), lkeEMQXListenerBacklog(env), authEnabled, lkeEMQXListenerAcceptors(env), lkeEMQXListenerBacklog(env), firstNonEmpty(os.Getenv("LKE_EMQX_FORCE_SHUTDOWN_MAX_MAILBOX_SIZE"), env["LKE_EMQX_FORCE_SHUTDOWN_MAX_MAILBOX_SIZE"], "131072"), firstNonEmpty(os.Getenv("LKE_EMQX_FORCE_SHUTDOWN_MAX_HEAP_SIZE"), env["LKE_EMQX_FORCE_SHUTDOWN_MAX_HEAP_SIZE"], "512MB"), authEnv, lkeContainerResourcesManifest(env, "mqtt"))
}

func lkeMQTTReplicas(env map[string]string) int {
	raw := strings.TrimSpace(firstNonEmpty(env["MQTT_EFFECTIVE_REPLICAS"], os.Getenv("LKE_MQTT_REPLICAS"), env["LKE_MQTT_REPLICAS"], "1"))
	replicas, err := strconv.Atoi(raw)
	if err != nil || replicas < 1 {
		return 4
	}
	return replicas
}

func lkeTargetConnects(env map[string]string) int {
	raw := strings.TrimSpace(firstNonEmpty(os.Getenv("LKE_TARGET_CONNECTS"), env["LKE_TARGET_CONNECTS"], os.Getenv("HOME100K_DEVICES"), env["HOME100K_DEVICES"]))
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func lkeMQTTConnectionsPerPod(env map[string]string) int {
	raw := strings.TrimSpace(firstNonEmpty(os.Getenv("LKE_MQTT_CONNECTIONS_PER_POD"), env["LKE_MQTT_CONNECTIONS_PER_POD"], "20000"))
	value, err := strconv.Atoi(raw)
	if err != nil || value <= 0 {
		return 20000
	}
	return value
}

func lkeEMQXNodeCookie(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_EMQX_NODE_COOKIE"), env["LKE_EMQX_NODE_COOKIE"], "rtk-home100k-emqx-cookie")
}

func lkeEMQXStaticSeeds(env map[string]string) string {
	replicas := lkeMQTTReplicas(env)
	namespace := lkeNamespaceName(env, "video-cloud")
	seeds := make([]string, 0, replicas)
	for idx := 0; idx < replicas; idx++ {
		seeds = append(seeds, fmt.Sprintf("emqx@mqtt-%d.mqtt-headless.%s.svc.cluster.local", idx, namespace))
	}
	return "[" + strings.Join(seeds, ",") + "]"
}

func lkeEMQXListenerAcceptors(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_EMQX_LISTENER_ACCEPTORS"), env["LKE_EMQX_LISTENER_ACCEPTORS"], "128")
}

func lkeEMQXListenerBacklog(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_EMQX_LISTENER_BACKLOG"), env["LKE_EMQX_LISTENER_BACKLOG"], "8192")
}

type lkeMQTTPod struct {
	Name               string
	NodeName           string
	KubernetesNodeName string
}

func lkeEnsureEMQXCluster(env map[string]string) error {
	namespace := lkeNamespaceName(env, "video-cloud")
	var lastErr error
	for attempt := 1; attempt <= 12; attempt++ {
		lastErr = nil
		pods, err := lkeMQTTPods(env)
		if err != nil {
			lastErr = err
			time.Sleep(5 * time.Second)
			continue
		}
		if len(pods) <= 1 {
			return nil
		}
		status, err := runKubectlOutput("-n", namespace, "exec", pods[0].Name, "--", "emqx", "ctl", "cluster", "status")
		if err != nil {
			lastErr = fmt.Errorf("verify EMQX cluster status: %w", err)
			time.Sleep(5 * time.Second)
			continue
		}
		for _, pod := range pods {
			if !strings.Contains(status, pod.NodeName) {
				lastErr = fmt.Errorf("EMQX cluster status missing %s: %s", pod.NodeName, strings.TrimSpace(status))
				break
			}
		}
		if lastErr != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		status, err = runKubectlOutput("-n", namespace, "exec", pods[0].Name, "--", "emqx", "ctl", "cluster", "status")
		if err != nil {
			lastErr = fmt.Errorf("verify EMQX cluster status: %w", err)
			time.Sleep(5 * time.Second)
			continue
		}
		running, stopped := lkeEMQXClusterStatusNodes(status)
		for _, node := range stopped {
			out, err := runKubectlOutput("-n", namespace, "exec", pods[0].Name, "--", "emqx", "ctl", "cluster", "force-leave", node)
			lowerOut := strings.ToLower(out)
			if err != nil || strings.Contains(lowerOut, "failed") || strings.Contains(lowerOut, "node_down") {
				lastErr = fmt.Errorf("force-leave stopped EMQX node %s: %w: %s", node, err, strings.TrimSpace(out))
				break
			}
		}
		if lastErr != nil {
			time.Sleep(5 * time.Second)
			continue
		}
		if len(stopped) > 0 {
			status, err = runKubectlOutput("-n", namespace, "exec", pods[0].Name, "--", "emqx", "ctl", "cluster", "status")
			if err != nil {
				lastErr = fmt.Errorf("verify EMQX cluster status after force-leave: %w", err)
				time.Sleep(5 * time.Second)
				continue
			}
			running, stopped = lkeEMQXClusterStatusNodes(status)
		}
		if running < len(pods) || len(stopped) > 0 {
			lastErr = fmt.Errorf("EMQX cluster status has running=%d/%d stopped=%d: %s", running, len(pods), len(stopped), strings.TrimSpace(status))
			time.Sleep(5 * time.Second)
			continue
		}
		fmt.Print(status)
		return nil
	}
	return lastErr
}

func lkeMQTTPods(env map[string]string) ([]lkeMQTTPod, error) {
	namespace := lkeNamespaceName(env, "video-cloud")
	out, err := kubectlCombinedOutput(nil, "-n", namespace, "get", "pods", "-l", "app.kubernetes.io/name=mqtt", "-o", "json")
	if err != nil {
		return nil, fmt.Errorf("list MQTT pods: %w: %s", err, strings.TrimSpace(string(out)))
	}
	var parsed struct {
		Items []struct {
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
			Spec struct {
				NodeName string `json:"nodeName"`
			} `json:"spec"`
			Status struct {
				Phase string `json:"phase"`
			} `json:"status"`
		} `json:"items"`
	}
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("parse MQTT pods: %w", err)
	}
	pods := []lkeMQTTPod{}
	for _, item := range parsed.Items {
		if item.Metadata.Name == "" || item.Status.Phase != "Running" {
			continue
		}
		pods = append(pods, lkeMQTTPod{
			Name:               item.Metadata.Name,
			NodeName:           fmt.Sprintf("emqx@%s.mqtt-headless.%s.svc.cluster.local", item.Metadata.Name, namespace),
			KubernetesNodeName: item.Spec.NodeName,
		})
	}
	sort.Slice(pods, func(i, j int) bool {
		return pods[i].Name < pods[j].Name
	})
	return pods, nil
}

func lkeEMQXClusterStatusNodes(status string) (int, []string) {
	runningSection := status
	if idx := strings.Index(status, "running_nodes"); idx >= 0 {
		runningSection = status[idx:]
	}
	if idx := strings.Index(runningSection, "stopped_nodes"); idx >= 0 {
		runningSection = runningSection[:idx]
	}
	running := strings.Count(runningSection, "emqx@")
	stoppedSection := ""
	if idx := strings.Index(status, "stopped_nodes"); idx >= 0 {
		stoppedSection = status[idx:]
	}
	stopped := []string{}
	for _, field := range strings.FieldsFunc(stoppedSection, func(r rune) bool {
		return r == '[' || r == ']' || r == ',' || r == '\'' || r == '"' || r == ' ' || r == '\n' || r == '\t' || r == '}'
	}) {
		if strings.HasPrefix(field, "emqx@") {
			stopped = append(stopped, field)
		}
	}
	return running, stopped
}

func lkeMQTTPlacementManifest(env map[string]string) string {
	var b strings.Builder
	fmt.Fprintf(&b, `      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: kubernetes.io/hostname
          whenUnsatisfiable: DoNotSchedule
          labelSelector:
            matchLabels:
              app.kubernetes.io/name: mqtt
      affinity:
        podAntiAffinity:
          requiredDuringSchedulingIgnoredDuringExecution:
            - topologyKey: kubernetes.io/hostname
              labelSelector:
                matchLabels:
                  app.kubernetes.io/name: mqtt
`)
	fmt.Fprint(&b, `      nodeSelector:
        rtk.io/node-class: "broker"
`)
	return b.String()
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
    - name: dashboard
      port: 18083
      targetPort: 18083
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"])
}

func lkeMQTTHeadlessServiceManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: mqtt-headless
  namespace: %s
  labels:
    app.kubernetes.io/name: mqtt
    app.kubernetes.io/component: cluster-discovery
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  clusterIP: None
  publishNotReadyAddresses: true
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

func lkeApplyPublicMQTTNodePort(env map[string]string) error {
	manifests, err := lkeMQTTPublicServiceManifests(env)
	if err != nil {
		return err
	}
	for _, manifest := range manifests {
		if err := kubectlApply(manifest); err != nil {
			return err
		}
	}
	return kubectlApply(lkeAllowPublicMQTTLoadTestNetworkPolicyManifest(env))
}

func lkeMQTTPublicServiceManifests(env map[string]string) ([]string, error) {
	count, err := lkePublicMQTTNodePortCount(env)
	if err != nil {
		return nil, err
	}
	manifests := make([]string, 0, count)
	for idx := 0; idx < count; idx++ {
		manifests = append(manifests, lkeMQTTPublicServiceManifest(env, idx))
	}
	return manifests, nil
}

func lkePublicMQTTNodePortCount(env map[string]string) (int, error) {
	raw := strings.TrimSpace(firstNonEmpty(os.Getenv("LKE_PUBLIC_MQTT_LOADBALANCER_COUNT"), env["LKE_PUBLIC_MQTT_LOADBALANCER_COUNT"], "1"))
	count, err := strconv.Atoi(raw)
	if err != nil || count < 1 {
		return 1, nil
	}
	if count > 1 {
		return 0, errors.New("LKE_PUBLIC_MQTT_LOADBALANCER_COUNT>1 is not supported by external HAProxy edge v1")
	}
	return count, nil
}

func lkeMQTTPublicServiceName(index int) string {
	if index <= 0 {
		return "mqtt-public"
	}
	return fmt.Sprintf("mqtt-public-%02d", index)
}

func lkeMQTTPublicServiceManifest(env map[string]string, index int) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/name: mqtt
    app.kubernetes.io/component: public-mqtt
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  type: NodePort
  externalTrafficPolicy: Local
  selector:
    app.kubernetes.io/name: mqtt
  ports:
    - name: mqtts
      port: 8883
      targetPort: 8883
      nodePort: %d
`, lkeMQTTPublicServiceName(index), lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], lkeMQTTPublicNodePort(env))
}

func lkeAllowPublicMQTTLoadTestNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-public-mqtt-loadtest
  namespace: %s
  labels:
    app.kubernetes.io/name: mqtt
    app.kubernetes.io/component: public-mqtt
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: mqtt
  policyTypes:
    - Ingress
  ingress:
    - ports:
        - protocol: TCP
          port: 8883
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
  VIDEO_CLOUD_LOGGER_TOKEN: %q
  VIDEO_CLOUD_BILLING_USAGE_LOGGER_TOKEN: %q
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], lkeRuntimeSecretValue("postgres"), lkeRuntimeSecretValue("turn-registry-node-auth"), lkeRuntimeSecretValue("mqtt-usage-ingest"), lkeRuntimeSecretValue("cloud-logger-ingest-token"), lkeRuntimeSecretValue("cloud-logger-billing-usage-token"))
}

func lkeCloudLoggerRuntimeSecretManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: cloud-logger-runtime
  namespace: %s
  labels:
    app.kubernetes.io/name: cloud-logger
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
type: Opaque
stringData:
  RTK_CLOUD_LOGGER_TOKEN: %q
  RTK_CLOUD_LOGGER_BILLING_USAGE_TOKEN: %q
`, lkeNamespaceName(env, "logger"), env["CLOUD_STACK_NAME"], lkeRuntimeSecretValue("cloud-logger-ingest-token"), lkeRuntimeSecretValue("cloud-logger-billing-usage-token"))
}

func lkeCloudLoggerDeploymentManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: cloud-logger
  namespace: %s
  labels:
    app.kubernetes.io/name: cloud-logger
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: cloud-logger
  template:
    metadata:
      labels:
        app.kubernetes.io/name: cloud-logger
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
      imagePullSecrets:
        - name: %s
      containers:
        - name: app
          image: %s
          imagePullPolicy: IfNotPresent
          ports:
            - name: http
              containerPort: 18090
          env:
            - name: RTK_CLOUD_LOGGER_TOKEN
              valueFrom:
                secretKeyRef:
                  name: cloud-logger-runtime
                  key: RTK_CLOUD_LOGGER_TOKEN
            - name: RTK_CLOUD_LOGGER_BILLING_USAGE_TOKEN
              valueFrom:
                secretKeyRef:
                  name: cloud-logger-runtime
                  key: RTK_CLOUD_LOGGER_BILLING_USAGE_TOKEN
            - name: RTK_CLOUD_LOGGER_LOKI_URL
              value: %q
%s`, lkeNamespaceName(env, "logger"), env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], lkeImagePullSecretName(env), lkeCloudLoggerImage(env), lkeCloudLoggerLokiURL(env), lkeContainerResourcesManifest(env, "cloud-logger"))
}

func lkeCloudLoggerServiceManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: cloud-logger
  namespace: %s
  labels:
    app.kubernetes.io/name: cloud-logger
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: cloud-logger
  ports:
    - name: http
      port: 80
      targetPort: 18090
`, lkeNamespaceName(env, "logger"), env["CLOUD_STACK_NAME"])
}

func lkeVideoCloudAuxiliaryDeploymentManifest(env map[string]string, service lkeVideoCloudAuxiliaryService) string {
	replicas := 1
	if service.Name == "video-cloud-clipverifier" {
		if lkeClipDirectUploadEnabled(env) {
			replicas = envIntDefault("LKE_VIDEO_CLOUD_CLIPVERIFIER_REPLICAS", 4)
		} else {
			replicas = 0
		}
	}
	ports := ""
	if service.Port > 0 {
		ports = fmt.Sprintf(`          ports:
            - name: %s
              containerPort: %d
`, firstNonEmpty(service.PortName, "http"), service.Port)
	}
	logIngesterEnv := ""
	if service.Name == "video-cloud-logingester" {
		logIngesterEnv = fmt.Sprintf(`            - name: VIDEO_CLOUD_MQTT_LOG_HANDLER_CONCURRENCY
              value: %q
            - name: VIDEO_CLOUD_LOG_INGESTER_WORKER_COUNT
              value: %q
`, firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_LOG_INGESTER_MQTT_HANDLER_CONCURRENCY"), env["LKE_VIDEO_CLOUD_LOG_INGESTER_MQTT_HANDLER_CONCURRENCY"], "1"), firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_LOG_INGESTER_WORKER_COUNT"), env["LKE_VIDEO_CLOUD_LOG_INGESTER_WORKER_COUNT"], "1"))
	}
	clipVerifierEnv := ""
	if service.Name == "video-cloud-clipverifier" {
		clipVerifierEnv = fmt.Sprintf(`            - name: VIDEO_CLOUD_AUTH_SECRET
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: VIDEO_CLOUD_AUTH_SECRET
            - name: VIDEO_CLOUD_API_BASE_URL
              value: %q
`, lkeVideoCloudAPIBaseURL(env))
	}
	mqttUsageEnv := ""
	if service.Name == "video-cloud-mqttusage" {
		mqttUsageEnv = `            - name: VIDEO_CLOUD_MQTT_USAGE_LOG_INTERVAL
              value: "5s"
            - name: VIDEO_CLOUD_MQTT_USAGE_PERSIST_INTERVAL
              value: "5s"
`
	}
	body := fmt.Sprintf(`apiVersion: apps/v1
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
  replicas: %d
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
%s
      containers:
        - name: app
          image: %s
          imagePullPolicy: IfNotPresent
          command: ["/app/%s"]
%s
%s          volumeMounts:
            - name: logger-spool
              mountPath: /var/lib/video_cloud/logger-spool
          env:
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
            - name: VIDEO_CLOUD_LOGGER_ENDPOINT
              value: %q
            - name: VIDEO_CLOUD_LOGGER_TOKEN
              valueFrom:
                secretKeyRef:
                  name: video-cloud-workers-runtime
                  key: VIDEO_CLOUD_LOGGER_TOKEN
            - name: VIDEO_CLOUD_BILLING_USAGE_LOGGER_TOKEN
              valueFrom:
                secretKeyRef:
                  name: video-cloud-workers-runtime
                  key: VIDEO_CLOUD_BILLING_USAGE_LOGGER_TOKEN
            - name: VIDEO_CLOUD_LOGGER_SPOOL_DIR
              value: "/var/lib/video_cloud/logger-spool"
            - name: VIDEO_CLOUD_LOGGER_SPOOL_MAX_BYTES
              value: %q
            - name: VIDEO_CLOUD_DB_MAX_OPEN_CONNS
              value: %q
            - name: VIDEO_CLOUD_DB_MAX_IDLE_CONNS
              value: %q
            - name: VIDEO_CLOUD_DB_CONN_MAX_LIFETIME
              value: %q
            - name: VIDEO_CLOUD_MQTT_ADDR
              value: %q
            - name: VIDEO_CLOUD_MQTT_USERNAME
              value: "video-cloud-server"
            - name: VIDEO_CLOUD_MQTT_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: VIDEO_CLOUD_MQTT_SERVER_PASSWORD
            - name: VIDEO_CLOUD_MQTT_SERVER_USERNAME
              value: "video-cloud-server"
            - name: VIDEO_CLOUD_MQTT_SERVER_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: VIDEO_CLOUD_MQTT_SERVER_PASSWORD
            - name: VIDEO_CLOUD_MQTT_BROKER_AUTH_KEY
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: VIDEO_CLOUD_MQTT_BROKER_AUTH_KEY
            - name: VIDEO_CLOUD_MQTT_TENANT_NAMESPACE_ENABLED
              value: %q
            - name: VIDEO_CLOUD_MQTT_TENANT_PREFIX
              value: "_bc"
            - name: VIDEO_CLOUD_MQTT_CLIENT_ID
              value: %q
            - name: VIDEO_CLOUD_MQTT_TOPIC_ROOT
              value: "devices"
            - name: VIDEO_CLOUD_MQTT_CLEAN_SESSION
              value: %q
%s            - name: VIDEO_CLOUD_METRICS_EXPORTER_ADDR
              value: "0.0.0.0:19200"
            - name: VIDEO_CLOUD_TURN_REGISTRY_ADDR
              value: "0.0.0.0:18190"
            - name: VIDEO_CLOUD_LOG_INGESTER_ADDR
              value: "0.0.0.0:19300"
            - name: VIDEO_CLOUD_MQTT_USAGE_ADDR
              value: "0.0.0.0:19400"
            - name: VIDEO_CLOUD_MQTT_BROKER_NODE
              value: %q
%s            - name: VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY
              valueFrom:
                secretKeyRef:
                  name: video-cloud-workers-runtime
                  key: VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY
            - name: VIDEO_CLOUD_MQTT_USAGE_INGEST_TOKEN
              valueFrom:
                secretKeyRef:
                  name: video-cloud-workers-runtime
                  key: VIDEO_CLOUD_MQTT_USAGE_INGEST_TOKEN
      volumes:
        - name: logger-spool
          emptyDir: {}
`, service.Name, lkeNamespaceName(env, "video-cloud"), service.Name, env["CLOUD_STACK_NAME"], replicas, service.Name, service.Name, env["CLOUD_STACK_NAME"], lkeDeploymentImagePullSecretsManifest(env), lkeVideoCloudImage(env), service.Binary, lkeContainerResourcesManifest(env, service.Name), ports, firstNonEmpty(os.Getenv("VIDEO_CLOUD_LOG_LEVEL"), "info"), lkeNamespaceName(env, "platform"), lkeCloudLoggerEndpoint(env), firstNonEmpty(os.Getenv("VIDEO_CLOUD_LOGGER_SPOOL_MAX_BYTES"), "104857600"), lkeVideoCloudWorkerDBMaxOpenConns(env), lkeVideoCloudWorkerDBMaxIdleConns(env), lkeVideoCloudDBConnMaxLifetime(env), lkeMQTTInternalAddr(env), strconv.FormatBool(lkeMQTTTenantNamespaceEnabled(env)), service.Name, lkeVideoCloudAuxiliaryMQTTCleanSession(service), logIngesterEnv+clipVerifierEnv, service.Name, mqttUsageEnv)
	body = strings.Replace(body, "      volumes:\n", lkeBlobEnvironmentManifest(env, "video-cloud-runtime")+"      volumes:\n", 1)
	body = strings.Replace(body, "    metadata:\n      labels:", fmt.Sprintf("    metadata:\n      annotations:\n        rtk.realtek.com/runtime-checksum: %q\n      labels:", lkeVideoCloudRuntimeChecksum(env)), 1)
	return body
}

func lkeVideoCloudAuxiliaryMQTTCleanSession(service lkeVideoCloudAuxiliaryService) string {
	if service.Name == "video-cloud-logingester" {
		return firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_LOGINGESTER_MQTT_CLEAN_SESSION"), "false")
	}
	return "true"
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

func lkePrometheusTargets(env map[string]string, opts provisionOptions) []lkePrometheusTarget {
	targets := k8sPrometheusTargets(env, opts)
	if lkeClipDirectUploadEnabled(env) {
		return targets
	}
	filtered := make([]lkePrometheusTarget, 0, len(targets))
	for _, target := range targets {
		if target.Service != "video-cloud-clipverifier" {
			filtered = append(filtered, target)
		}
	}
	return filtered
}

func lkeVideoCloudPrometheusConfigManifest(env map[string]string, opts provisionOptions) string {
	var scrape strings.Builder
	for _, target := range lkePrometheusTargets(env, opts) {
		fmt.Fprintf(&scrape, `      - job_name: %s
        metrics_path: %s
        static_configs:
          - targets: ["%s.%s.svc.cluster.local:%d"]
`, target.Job, target.Path, target.Service, target.Namespace, target.Port)
	}
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
%s`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"], scrape.String())
}

func lkeVideoCloudPrometheusDeploymentManifest(env map[string]string, opts provisionOptions) string {
	checksum := lkeConfigChecksum(lkeVideoCloudPrometheusConfigManifest(env, opts))
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
      annotations:
        rtk.realtek.com/config-checksum: %q
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
`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"], checksum, env["CLOUD_STACK_NAME"], firstNonEmpty(os.Getenv("LKE_PROMETHEUS_IMAGE"), "prom/prometheus:v2.53.1"), firstNonEmpty(os.Getenv("LKE_PROMETHEUS_RETENTION"), "24h"))
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

func lkeLokiConfigManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: video-cloud-loki-config
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-loki
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
data:
  config.yaml: |
    auth_enabled: false
    server:
      http_listen_port: 3100
    common:
      path_prefix: /loki
      storage:
        filesystem:
          chunks_directory: /loki/chunks
          rules_directory: /loki/rules
      replication_factor: 1
      ring:
        kvstore:
          store: inmemory
    schema_config:
      configs:
        - from: 2024-01-01
          store: tsdb
          object_store: filesystem
          schema: v13
          index:
            prefix: index_
            period: 24h
    limits_config:
      allow_structured_metadata: false
      retention_period: 24h
`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"])
}

func lkeLokiDeploymentManifest(env map[string]string) string {
	checksum := lkeConfigChecksum(lkeLokiConfigManifest(env))
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: video-cloud-loki
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-loki
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: video-cloud-loki
  template:
    metadata:
      annotations:
        rtk.realtek.com/config-checksum: %q
      labels:
        app.kubernetes.io/name: video-cloud-loki
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
      containers:
        - name: loki
          image: %s
          imagePullPolicy: IfNotPresent
          args:
            - -config.file=/etc/loki/config.yaml
          ports:
            - name: http
              containerPort: 3100
          volumeMounts:
            - name: config
              mountPath: /etc/loki
            - name: data
              mountPath: /loki
%s      volumes:
        - name: config
          configMap:
            name: video-cloud-loki-config
        - name: data
          emptyDir: {}
`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"], checksum, env["CLOUD_STACK_NAME"], firstNonEmpty(os.Getenv("LKE_LOKI_IMAGE"), env["LKE_LOKI_IMAGE"], "grafana/loki:3.5.1"), lkeContainerResourcesManifest(env, "loki"))
}

func lkeLokiServiceManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: video-cloud-loki
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-loki
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: video-cloud-loki
  ports:
    - name: http
      port: 3100
      targetPort: 3100
`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"])
}

func lkeLogCollectorServiceAccountManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: ServiceAccount
metadata:
  name: video-cloud-log-collector
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-log-collector
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"])
}

func lkeLogCollectorClusterRoleManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRole
metadata:
  name: %s-video-cloud-log-collector
  labels:
    app.kubernetes.io/name: video-cloud-log-collector
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
rules:
  - apiGroups: [""]
    resources: ["nodes", "nodes/proxy", "services", "endpoints", "pods"]
    verbs: ["get", "list", "watch"]
`, env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"])
}

func lkeLogCollectorClusterRoleBindingManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: rbac.authorization.k8s.io/v1
kind: ClusterRoleBinding
metadata:
  name: %s-video-cloud-log-collector
  labels:
    app.kubernetes.io/name: video-cloud-log-collector
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
roleRef:
  apiGroup: rbac.authorization.k8s.io
  kind: ClusterRole
  name: %s-video-cloud-log-collector
subjects:
  - kind: ServiceAccount
    name: video-cloud-log-collector
    namespace: %s
`, env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], lkeNamespaceName(env, "observability"))
}

func lkeLogCollectorConfigManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: video-cloud-log-collector-config
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-log-collector
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
data:
  config.yaml: |
    server:
      http_listen_port: 3101
      grpc_listen_port: 0
    positions:
      filename: /run/promtail/positions.yaml
    clients:
      - url: http://video-cloud-loki.%s.svc.cluster.local:3100/loki/api/v1/push
    scrape_configs:
      - job_name: kubernetes-pod-files
        pipeline_stages:
          - cri: {}
        static_configs:
          - targets:
              - localhost
            labels:
              job: kubernetes-pod-files
              __path__: /var/log/pods/**/*.log
`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"], lkeNamespaceName(env, "observability"))
}

func lkeLogCollectorDaemonSetManifest(env map[string]string) string {
	checksum := lkeConfigChecksum(lkeLogCollectorConfigManifest(env))
	return fmt.Sprintf(`apiVersion: apps/v1
kind: DaemonSet
metadata:
  name: video-cloud-log-collector
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-log-collector
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  selector:
    matchLabels:
      app.kubernetes.io/name: video-cloud-log-collector
  template:
    metadata:
      annotations:
        rtk.realtek.com/config-checksum: %q
      labels:
        app.kubernetes.io/name: video-cloud-log-collector
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
      serviceAccountName: video-cloud-log-collector
      tolerations:
        - operator: Exists
      containers:
        - name: promtail
          image: %s
          imagePullPolicy: IfNotPresent
          args:
            - -config.file=/etc/promtail/config.yaml
          ports:
            - name: http
              containerPort: 3101
          volumeMounts:
            - name: config
              mountPath: /etc/promtail
            - name: run
              mountPath: /run/promtail
            - name: pod-logs
              mountPath: /var/log/pods
              readOnly: true
%s      volumes:
        - name: config
          configMap:
            name: video-cloud-log-collector-config
        - name: run
          emptyDir: {}
        - name: pod-logs
          hostPath:
            path: /var/log/pods
`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"], checksum, env["CLOUD_STACK_NAME"], firstNonEmpty(os.Getenv("LKE_LOG_COLLECTOR_IMAGE"), env["LKE_LOG_COLLECTOR_IMAGE"], "grafana/promtail:3.5.1"), lkeContainerResourcesManifest(env, "log-collector"))
}

func lkeGrafanaAdminSecretManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: video-cloud-grafana-admin
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-grafana
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
type: Opaque
stringData:
  admin-password: %q
`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"], lkeRuntimeSecretValue("grafana-admin-password"))
}

func lkeGrafanaDatasourcesConfigManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: video-cloud-grafana-datasources
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-grafana
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
data:
  datasources.yaml: |
    apiVersion: 1
    datasources:
      - name: Prometheus
        type: prometheus
        access: proxy
        isDefault: true
        url: http://video-cloud-prometheus.%s.svc.cluster.local:9090
        editable: false
`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"], lkeNamespaceName(env, "observability"))
}

func lkeGrafanaDashboardProvidersConfigManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: video-cloud-grafana-dashboard-providers
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-grafana
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
data:
  dashboards.yaml: |
    apiVersion: 1
    providers:
      - name: rtk-lke-staging
        orgId: 1
        folder: RTK Cloud
        type: file
        disableDeletion: true
        updateIntervalSeconds: 30
        options:
          path: /etc/grafana/provisioned-dashboards
`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"])
}

func lkeGrafanaDashboardsConfigManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: ConfigMap
metadata:
  name: video-cloud-grafana-dashboards
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-grafana
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
data:
  rtk-lke-staging-overview.json: |
    {
      "uid": "rtk-lke-staging",
      "title": "RTK LKE Staging Overview",
      "schemaVersion": 39,
      "version": 1,
      "refresh": "30s",
      "time": { "from": "now-1h", "to": "now" },
      "templating": {
        "list": [
          { "name": "job", "type": "query", "datasource": { "type": "prometheus", "uid": "Prometheus" }, "query": "label_values(up, job)", "refresh": 1 },
          { "name": "service", "type": "query", "datasource": { "type": "prometheus", "uid": "Prometheus" }, "query": "label_values(http_requests_total, service)", "refresh": 1 },
          { "name": "path", "type": "query", "datasource": { "type": "prometheus", "uid": "Prometheus" }, "query": "label_values(http_requests_total, path)", "refresh": 1 }
        ]
      },
      "panels": [
        { "type": "row", "title": "Platform Stability Overview", "gridPos": { "x": 0, "y": 0, "w": 24, "h": 1 } },
        { "type": "stat", "title": "Targets Down", "gridPos": { "x": 0, "y": 1, "w": 6, "h": 4 }, "targets": [{ "expr": "sum(up == bool 0)" }] },
        { "type": "stat", "title": "Targets Up", "gridPos": { "x": 6, "y": 1, "w": 6, "h": 4 }, "targets": [{ "expr": "sum(up == bool 1)" }] },
        { "type": "stat", "title": "Metrics Snapshot Age", "gridPos": { "x": 12, "y": 1, "w": 6, "h": 4 }, "targets": [{ "expr": "time() - video_cloud_exporter_last_collect_timestamp_seconds" }] },
        { "type": "timeseries", "title": "Scrape Duration", "gridPos": { "x": 18, "y": 1, "w": 6, "h": 4 }, "targets": [{ "expr": "scrape_duration_seconds" }] },
        { "type": "row", "title": "Per-Platform MQTT", "gridPos": { "x": 0, "y": 5, "w": 24, "h": 1 } },
        { "type": "timeseries", "title": "MQTT Publish Rate", "description": "Dashboard-only MQTT counter. Billing uses mqtt_usage_windows.", "gridPos": { "x": 0, "y": 6, "w": 8, "h": 6 }, "targets": [{ "expr": "sum by(brand_cloud_id) (rate(mqtt_brand_publish_total[$__rate_interval]))" }] },
        { "type": "timeseries", "title": "MQTT Delivery Rate", "description": "Dashboard-only MQTT counter. Billing uses mqtt_usage_windows.", "gridPos": { "x": 8, "y": 6, "w": 8, "h": 6 }, "targets": [{ "expr": "sum by(brand_cloud_id) (rate(mqtt_brand_delivery_total[$__rate_interval]))" }] },
        { "type": "table", "title": "Top MQTT Brand Clouds 24h", "description": "Dashboard-only MQTT counter. Billing uses mqtt_usage_windows.", "gridPos": { "x": 16, "y": 6, "w": 8, "h": 6 }, "targets": [{ "expr": "topk(10, sum by(brand_cloud_id) (increase(mqtt_brand_publish_total[24h])))" }] },
        { "type": "row", "title": "Device Fleet", "gridPos": { "x": 0, "y": 12, "w": 24, "h": 1 } },
        { "type": "stat", "title": "Online Devices", "gridPos": { "x": 0, "y": 13, "w": 6, "h": 4 }, "targets": [{ "expr": "video_cloud_devices_online" }] },
        { "type": "stat", "title": "Offline Devices", "gridPos": { "x": 6, "y": 13, "w": 6, "h": 4 }, "targets": [{ "expr": "video_cloud_devices_offline" }] },
        { "type": "timeseries", "title": "Device State Trend", "gridPos": { "x": 12, "y": 13, "w": 12, "h": 4 }, "targets": [{ "expr": "video_cloud_devices_online" }, { "expr": "video_cloud_devices_offline" }, { "expr": "video_cloud_devices_connected" }, { "expr": "video_cloud_devices_attached" }, { "expr": "video_cloud_devices_activated" }] },
        { "type": "row", "title": "API Health", "gridPos": { "x": 0, "y": 17, "w": 24, "h": 1 } },
        { "type": "timeseries", "title": "Request Rate", "gridPos": { "x": 0, "y": 18, "w": 6, "h": 5 }, "targets": [{ "expr": "sum by(service) (rate(http_requests_total[$__rate_interval]))" }] },
        { "type": "timeseries", "title": "5xx Rate", "gridPos": { "x": 6, "y": 18, "w": 6, "h": 5 }, "targets": [{ "expr": "sum by(service) (rate(http_status_group_total{status=\"5xx\"}[$__rate_interval]))" }] },
        { "type": "timeseries", "title": "Average Latency", "gridPos": { "x": 12, "y": 18, "w": 6, "h": 5 }, "targets": [{ "expr": "sum by(service) (rate(http_request_duration_seconds_sum[$__rate_interval])) / clamp_min(sum by(service) (rate(http_request_duration_seconds_count[$__rate_interval])), 1)" }] },
        { "type": "table", "title": "Top Error Routes", "gridPos": { "x": 18, "y": 18, "w": 6, "h": 5 }, "targets": [{ "expr": "topk(10, sum by(service, method, path, status) (rate(http_status_group_total{status=~\"4xx|5xx\"}[$__rate_interval])))" }] },
        { "type": "row", "title": "Runtime Pipeline", "gridPos": { "x": 0, "y": 23, "w": 24, "h": 1 } },
        { "type": "timeseries", "title": "Log Queue Depth", "gridPos": { "x": 0, "y": 24, "w": 6, "h": 5 }, "targets": [{ "expr": "device_log_queue_depth" }] },
        { "type": "timeseries", "title": "Log Drops and Failures", "gridPos": { "x": 6, "y": 24, "w": 6, "h": 5 }, "targets": [{ "expr": "rate(device_log_dropped_total[$__rate_interval])" }, { "expr": "rate(device_log_write_failures_total[$__rate_interval])" }, { "expr": "rate(device_log_sequence_gap_total[$__rate_interval])" }] },
        { "type": "timeseries", "title": "Cross-Service Backlog", "gridPos": { "x": 12, "y": 24, "w": 6, "h": 5 }, "targets": [{ "expr": "crossservice_bus_consumer_pending_messages" }] },
        { "type": "timeseries", "title": "Dead Letters", "gridPos": { "x": 18, "y": 24, "w": 6, "h": 5 }, "targets": [{ "expr": "sum by(worker, error_code, retryable) (rate(crossservice_worker_dead_letters_total[$__rate_interval]))" }] },
        { "type": "row", "title": "Video Runtime / TURN", "gridPos": { "x": 0, "y": 29, "w": 24, "h": 1 } },
        { "type": "stat", "title": "Active TURN Nodes", "gridPos": { "x": 0, "y": 30, "w": 6, "h": 4 }, "targets": [{ "expr": "turn_registry_active_nodes" }] },
        { "type": "stat", "title": "Expired TURN Nodes", "gridPos": { "x": 6, "y": 30, "w": 6, "h": 4 }, "targets": [{ "expr": "turn_registry_expired_nodes" }] },
        { "type": "timeseries", "title": "TURN Heartbeats", "gridPos": { "x": 12, "y": 30, "w": 6, "h": 4 }, "targets": [{ "expr": "rate(turn_registry_heartbeat_total[$__rate_interval])" }] },
        { "type": "timeseries", "title": "TURN Failures", "gridPos": { "x": 18, "y": 30, "w": 6, "h": 4 }, "targets": [{ "expr": "rate(turn_registry_register_failures_total[$__rate_interval])" }, { "expr": "rate(turn_registry_heartbeat_failures_total[$__rate_interval])" }] },
        { "type": "row", "title": "Capacity", "gridPos": { "x": 0, "y": 34, "w": 24, "h": 1 } },
        { "type": "gauge", "title": "Blob Capacity Utilization", "gridPos": { "x": 0, "y": 35, "w": 8, "h": 5 }, "targets": [{ "expr": "video_cloud_blob_capacity_utilization_percent" }] },
        { "type": "stat", "title": "Blob Consumed Bytes", "gridPos": { "x": 8, "y": 35, "w": 8, "h": 5 }, "targets": [{ "expr": "video_cloud_blob_capacity_consumed_bytes" }] },
        { "type": "stat", "title": "Clips Total", "gridPos": { "x": 16, "y": 35, "w": 8, "h": 5 }, "targets": [{ "expr": "video_cloud_clips_total" }] }
      ]
    }
`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"])
}

func lkeGrafanaPVCManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: video-cloud-grafana-data
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-grafana
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  accessModes: ["ReadWriteOnce"]
  resources:
    requests:
      storage: %s
`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"], firstNonEmpty(os.Getenv("LKE_GRAFANA_STORAGE"), env["LKE_GRAFANA_STORAGE"], "5Gi"))
}

func lkeGrafanaPersistenceEnabled(env map[string]string) bool {
	raw := strings.ToLower(strings.TrimSpace(firstNonEmpty(os.Getenv("LKE_GRAFANA_PERSISTENCE"), env["LKE_GRAFANA_PERSISTENCE"], "false")))
	return raw == "1" || raw == "true" || raw == "yes" || raw == "on"
}

func lkeGrafanaDataVolumeManifest(env map[string]string) string {
	if lkeGrafanaPersistenceEnabled(env) {
		return `        - name: data
          persistentVolumeClaim:
            claimName: video-cloud-grafana-data
`
	}
	return `        - name: data
          emptyDir: {}
`
}

func lkeGrafanaDeploymentManifest(env map[string]string) string {
	checksum := lkeConfigChecksum(lkeRuntimeSecretValue("grafana-admin-password"), lkeGrafanaDatasourceURL(env))
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: video-cloud-grafana
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-grafana
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app.kubernetes.io/name: video-cloud-grafana
  template:
    metadata:
      annotations:
        rtk.realtek.com/runtime-checksum: %q
      labels:
        app.kubernetes.io/name: video-cloud-grafana
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
      securityContext:
        fsGroup: 472
        fsGroupChangePolicy: OnRootMismatch
      containers:
        - name: grafana
          image: %s
          imagePullPolicy: IfNotPresent
          ports:
            - name: http
              containerPort: 3000
          env:
            - name: GF_SECURITY_ADMIN_USER
              value: "admin"
            - name: GF_SECURITY_ADMIN_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: video-cloud-grafana-admin
                  key: admin-password
            - name: GF_SECURITY_ALLOW_EMBEDDING
              value: "true"
            - name: GF_AUTH_ANONYMOUS_ENABLED
              value: "false"
            - name: GF_AUTH_PROXY_ENABLED
              value: "true"
            - name: GF_AUTH_PROXY_HEADER_NAME
              value: "X-WEBAUTH-USER"
            - name: GF_AUTH_PROXY_AUTO_SIGN_UP
              value: "true"
            - name: GF_AUTH_PROXY_HEADERS
              value: "Email:X-WEBAUTH-EMAIL Role:X-WEBAUTH-ROLE"
            - name: GF_SERVER_ROOT_URL
              value: "%%(protocol)s://%%(domain)s/api/admin/grafana/"
            - name: GF_SERVER_SERVE_FROM_SUB_PATH
              value: "true"
            - name: GF_METRICS_ENABLED
              value: "true"
          volumeMounts:
            - name: data
              mountPath: /var/lib/grafana
            - name: datasources
              mountPath: /etc/grafana/provisioning/datasources
              readOnly: true
            - name: dashboard-providers
              mountPath: /etc/grafana/provisioning/dashboards
              readOnly: true
            - name: dashboards
              mountPath: /etc/grafana/provisioned-dashboards
              readOnly: true
      volumes:
%s
        - name: datasources
          configMap:
            name: video-cloud-grafana-datasources
        - name: dashboard-providers
          configMap:
            name: video-cloud-grafana-dashboard-providers
        - name: dashboards
          configMap:
            name: video-cloud-grafana-dashboards
`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"], checksum, env["CLOUD_STACK_NAME"], firstNonEmpty(os.Getenv("LKE_GRAFANA_IMAGE"), env["LKE_GRAFANA_IMAGE"], "grafana/grafana:13.0.2"), lkeGrafanaDataVolumeManifest(env))
}

func lkeGrafanaDatasourceURL(env map[string]string) string {
	return "http://video-cloud-prometheus." + lkeNamespaceName(env, "observability") + ".svc.cluster.local:9090"
}

func lkeGrafanaServiceManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: video-cloud-grafana
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-grafana
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: video-cloud-grafana
  ports:
    - name: http
      port: 3000
      targetPort: 3000
`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"])
}

func lkeAllowCloudAdminGrafanaNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-cloud-admin-grafana
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-grafana
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: video-cloud-grafana
  policyTypes:
    - Ingress
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: %s
          podSelector:
            matchLabels:
              app.kubernetes.io/name: cloud-admin
      ports:
        - protocol: TCP
          port: 3000
`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"], lkeNamespaceName(env, "admin"))
}

func lkeAllowGrafanaPrometheusNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-grafana-prometheus
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-grafana
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: video-cloud-grafana
  policyTypes:
    - Egress
  egress:
    - to:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: video-cloud-prometheus
      ports:
        - protocol: TCP
          port: 9090
`, lkeNamespaceName(env, "observability"), env["CLOUD_STACK_NAME"])
}

func lkeAllowPrometheusGrafanaNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-prometheus-grafana
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-prometheus
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: video-cloud-grafana
  policyTypes:
    - Ingress
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: video-cloud-prometheus
      ports:
        - protocol: TCP
          port: 3000
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

func lkeCertIssuerDeploymentManifest(env map[string]string, material lkeCertIssuerMaterial, openBao lkeOpenBaoBootstrapResult) string {
	checksum := lkeConfigChecksum(
		material.ServerCert,
		material.ServiceCA,
		material.RootCACert,
		material.DeviceCACert,
		material.AppCACert,
		openBao.RoleID,
		openBao.SecretID,
		openBao.TLSCACert,
		lkeRuntimeSecretValue("postgres"),
	)
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
      annotations:
        rtk.realtek.com/runtime-checksum: %q
      labels:
        app.kubernetes.io/name: certissuer
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
      imagePullSecrets:
        - name: %s
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
            - name: CERT_ISSUER_APP_CA_CERT_PATH
              value: /etc/video-cloud/certissuer/app-ca.crt
            - name: CERT_ISSUER_SIGNER_PROVIDER
              value: openbao
            - name: CERT_ISSUER_OPENBAO_PKI_MOUNT
              value: pki/device
            - name: CERT_ISSUER_OPENBAO_PKI_ROLE
              value: factory-device
            - name: CERT_ISSUER_OPENBAO_GATEWAY_ROLE
              value: gateway-server
            - name: CERT_ISSUER_APP_SIGNER_PROVIDER
              value: openbao
            - name: CERT_ISSUER_APP_OPENBAO_PKI_MOUNT
              value: pki/app
            - name: CERT_ISSUER_APP_OPENBAO_PKI_ROLE
              value: app-user
            - name: OPENBAO_ADDR
              value: %q
            - name: OPENBAO_CACERT
              value: /etc/video-cloud/openbao/ca.crt
            - name: OPENBAO_AUTH_METHOD
              value: approle
            - name: OPENBAO_ROLE_ID_FILE
              value: /etc/video-cloud/openbao/role_id
            - name: OPENBAO_SECRET_ID_FILE
              value: /etc/video-cloud/openbao/secret_id
            - name: CERT_ISSUER_DB_DSN
              value: "postgres://postgres:$(POSTGRES_PASSWORD)@postgresql.%s.svc.cluster.local:5432/video_cloud?sslmode=disable"
          volumeMounts:
            - name: certissuer-runtime
              mountPath: /etc/video-cloud/certissuer
              readOnly: true
            - name: certissuer-openbao-auth
              mountPath: /etc/video-cloud/openbao
              readOnly: true
      volumes:
        - name: certissuer-runtime
          secret:
            secretName: certissuer-runtime
        - name: certissuer-openbao-auth
          secret:
            secretName: certissuer-openbao-auth
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], checksum, env["CLOUD_STACK_NAME"], lkeImagePullSecretName(env), lkeVideoCloudImage(env), lkeOpenBaoAddr(env), lkeNamespaceName(env, "platform"))
}

func lkeFactoryEnrollDeploymentManifest(env map[string]string, material lkeCertIssuerMaterial) string {
	checksum := lkeConfigChecksum(
		lkeRuntimeSecretValue("postgres"),
		lkeRuntimeSecretValue("factory-enroll-auth"),
		lkeFactoryProductionJWTSecret(env),
		lkeFactoryProductionJWTAudience(env),
		lkeCertIssuerBaseURL(env),
		material.FactoryCert,
		material.ServiceCA,
	)
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
      annotations:
        rtk.realtek.com/runtime-checksum: %q
      labels:
        app.kubernetes.io/name: factoryenroll
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
      imagePullSecrets:
        - name: %s
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
            - name: FACTORY_ENROLL_PRODUCTION_JWT_SECRET
              valueFrom:
                secretKeyRef:
                  name: factoryenroll-runtime
                  key: FACTORY_ENROLL_PRODUCTION_JWT_SECRET
            - name: FACTORY_ENROLL_PRODUCTION_JWT_AUDIENCE
              valueFrom:
                secretKeyRef:
                  name: factoryenroll-runtime
                  key: FACTORY_ENROLL_PRODUCTION_JWT_AUDIENCE
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
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], checksum, env["CLOUD_STACK_NAME"], lkeImagePullSecretName(env), lkeVideoCloudImage(env), lkeCertIssuerBaseURL(env), lkeNamespaceName(env, "platform"))
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

func lkeCloudLoggerImage(env map[string]string) string {
	for _, workload := range lkeWorkloads(env) {
		if workload.Key == "cloud-logger" {
			return workload.Image
		}
	}
	return ""
}

func lkeCloudLoggerEndpoint(env map[string]string) string {
	return firstNonEmpty(
		os.Getenv("CLOUD_LOGGER_ENDPOINT"),
		env["CLOUD_LOGGER_ENDPOINT"],
		"http://cloud-logger."+lkeNamespaceName(env, "logger")+".svc.cluster.local",
	)
}

func lkeCloudLoggerLokiURL(env map[string]string) string {
	return strings.TrimRight(firstNonEmpty(
		os.Getenv("RTK_CLOUD_LOGGER_LOKI_URL"),
		env["RTK_CLOUD_LOGGER_LOKI_URL"],
		"http://video-cloud-loki."+lkeNamespaceName(env, "observability")+".svc.cluster.local:3100",
	), "/")
}

func lkeImagePullSecretName(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_IMAGE_PULL_SECRET_NAME"), env["LKE_IMAGE_PULL_SECRET_NAME"], "ghcr-pull")
}

func lkeCertIssuerBaseURL(env map[string]string) string {
	return "https://certissuer." + lkeNamespaceName(env, "video-cloud") + ".svc.cluster.local:9443"
}

func lkeFactoryEnrollAuthKey(env map[string]string) string {
	return firstNonEmpty(os.Getenv("FACTORY_ENROLL_AUTH_KEY"), lkeRuntimeSecretValue("factory-enroll-auth"))
}

func lkeFactoryProductionJWTSecret(env map[string]string) string {
	return firstNonEmpty(os.Getenv("FACTORY_PRODUCTION_JWT_SECRET"), env["FACTORY_PRODUCTION_JWT_SECRET"], lkeRuntimeSecretValue("factory-production-jwt"))
}

func lkeFactoryProductionJWTAudience(env map[string]string) string {
	return firstNonEmpty(os.Getenv("FACTORY_PRODUCTION_JWT_AUDIENCE"), env["FACTORY_PRODUCTION_JWT_AUDIENCE"], "factory-enroll")
}

func lkeInternalAuthToken() string {
	return lkeRuntimeSecretValue("internal-auth")
}

func lkeObjectStorageCredential(env map[string]string, name string) string {
	return strings.TrimSpace(firstNonEmpty(os.Getenv(name), env[name]))
}

func lkeAccountManagerSecretManifest(env map[string]string) string {
	accountEnv := firstNonEmpty(os.Getenv("ACCOUNT_MANAGER_ENV"), env["ACCOUNT_MANAGER_ENV"], "staging")
	smtpHost := lkeEnvValue(env, "SMTP_HOST")
	authDelivery := strings.ToLower(strings.TrimSpace(firstNonEmpty(
		os.Getenv("AUTH_TOKEN_DELIVERY"),
		env["AUTH_TOKEN_DELIVERY"],
	)))
	if authDelivery == "" {
		if smtpHost != "" {
			authDelivery = "smtp"
		} else {
			authDelivery = "log"
		}
	}
	authBaseURL := firstNonEmpty(os.Getenv("AUTH_TOKEN_BASE_URL"), env["AUTH_TOKEN_BASE_URL"])
	if authBaseURL == "" && strings.TrimSpace(env["FRONTEND_DOMAIN"]) != "" {
		authBaseURL = "https://" + strings.TrimSpace(env["FRONTEND_DOMAIN"])
	}
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
  FACTORY_PRODUCTION_JWT_SECRET: %q
  FACTORY_PRODUCTION_JWT_AUDIENCE: %q
  ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL: %q
  ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD: %q
  ACCOUNT_MANAGER_USER_CACHE_ENABLED: "true"
  ACCOUNT_MANAGER_USER_CACHE_ADDR: %q
  ACCOUNT_MANAGER_USER_CACHE_PREFIX: "account_manager:user"
  ACCOUNT_MANAGER_ENV: %q
  ACCOUNT_MANAGER_LOG_LEVEL: %q
  AUTH_TOKEN_DELIVERY: %q
  AUTH_TOKEN_BASE_URL: %q
  SMTP_HOST: %q
  SMTP_PORT: %q
  SMTP_USERNAME: %q
  SMTP_PASSWORD: %q
  SMTP_FROM: %q
  SMTP_FROM_NAME: %q
  SMTP_ENCRYPTION: %q
  SENDMAIL_HTTP_BASE_URL: %q
  SENDMAIL_HTTP_BEARER_TOKEN: %q
  SENDMAIL_HTTP_TIMEOUT: %q
  EMAIL_OUTBOX_ENCRYPTION_KEY: %q
  EMAIL_OUTBOX_POLL_INTERVAL: %q
  EMAIL_OUTBOX_BATCH_SIZE: %q
  EMAIL_OUTBOX_MAX_ATTEMPTS: %q
  EMAIL_OUTBOX_RETRY_BASE: %q
  EMAIL_OUTBOX_RETRY_MAX: %q
  CROSS_SERVICE_BROKER: "direct_http"
  VIDEO_CLOUD_LIFECYCLE_BASE_URL: %q
  VIDEO_CLOUD_LIFECYCLE_TOKEN: %q
  VIDEO_CLOUD_LIFECYCLE_TIMEOUT: %q
  APP_CERT_ISSUER_BASE_URL: %q
  APP_CERT_ISSUER_CLIENT_CERT: "/etc/rtk-account-manager/certissuer/client.crt"
  APP_CERT_ISSUER_CLIENT_KEY: "/etc/rtk-account-manager/certissuer/client.key"
  APP_CERT_ISSUER_CA_FILE: "/etc/rtk-account-manager/certissuer/ca.crt"
  PAYMENT_SIMULATOR_ENABLED: "true"
  PAYMENT_SIMULATOR_RUN_ID: %q
  PAYMENT_SIMULATOR_BASE_URL: %q
  PAYMENT_SIMULATOR_PUBLIC_BASE_URL: %q
  PAYMENT_SIMULATOR_CALLBACK_URL: %q
  PAYMENT_SIMULATOR_SHARED_SECRET: %q
  PAYMENT_SIMULATOR_SETUP_CALLBACK_SECRET: %q
  PAYMENT_SIMULATOR_SCENARIO: %q
  PAYMENT_SIMULATOR_RETENTION: %q
  PAYMENT_REFERENCE_ENCRYPTION_KEY: %q
  PAYMENT_WORKER_ENABLED: "true"
`, lkeNamespaceName(env, "account-manager"), env["CLOUD_STACK_NAME"], lkeAccountManagerDatabaseURL(env), lkeRuntimeSecretValue("jwt-access"), lkeRuntimeSecretValue("jwt-refresh"), lkeInternalAuthToken(), lkeFactoryProductionJWTSecret(env), lkeFactoryProductionJWTAudience(env), lkePlatformAdminEmail(env), lkeRuntimeSecretValue("platform-admin"), lkeRedisServiceHost(env)+":6379", accountEnv, firstNonEmpty(os.Getenv("ACCOUNT_MANAGER_LOG_LEVEL"), "info"), authDelivery, authBaseURL, smtpHost, firstNonEmpty(lkeEnvValue(env, "SMTP_PORT"), "587"), lkeEnvValue(env, "SMTP_USERNAME"), lkeEnvValue(env, "SMTP_PASSWORD"), lkeEnvValue(env, "SMTP_FROM"), firstNonEmpty(lkeEnvValue(env, "SMTP_FROM_NAME"), "Realtek Connect"), firstNonEmpty(lkeEnvValue(env, "SMTP_ENCRYPTION"), "starttls"), lkeEnvValue(env, "SENDMAIL_HTTP_BASE_URL"), lkeEnvValue(env, "SENDMAIL_HTTP_BEARER_TOKEN"), firstNonEmpty(lkeEnvValue(env, "SENDMAIL_HTTP_TIMEOUT"), "15s"), lkeEmailOutboxEncryptionKey(env), firstNonEmpty(lkeEnvValue(env, "EMAIL_OUTBOX_POLL_INTERVAL"), "5s"), firstNonEmpty(lkeEnvValue(env, "EMAIL_OUTBOX_BATCH_SIZE"), "20"), firstNonEmpty(lkeEnvValue(env, "EMAIL_OUTBOX_MAX_ATTEMPTS"), "8"), firstNonEmpty(lkeEnvValue(env, "EMAIL_OUTBOX_RETRY_BASE"), "30s"), firstNonEmpty(lkeEnvValue(env, "EMAIL_OUTBOX_RETRY_MAX"), "30m"), lkeVideoCloudLifecycleInternalURL(env), lkeInternalAuthToken(), firstNonEmpty(lkeEnvValue(env, "VIDEO_CLOUD_LIFECYCLE_TIMEOUT"), "10s"), lkeCertIssuerBaseURL(env), lkePaymentSimulatorRunID(env), lkePaymentSimulatorInternalURL(env), "https://"+lkePaymentSimulatorPublicDomain(env), lkeAccountManagerInternalURL(env)+"/v1/internal/payment-simulator/setup-callback", lkeRuntimeSecretValue("payment-simulator-shared"), lkeRuntimeSecretValue("payment-simulator-callback"), firstNonEmpty(lkeEnvValue(env, "PAYMENT_SIMULATOR_SCENARIO"), "success"), firstNonEmpty(lkeEnvValue(env, "PAYMENT_SIMULATOR_RETENTION"), "168h"), lkePaymentReferenceEncryptionKey(env))
}

func lkePaymentSimulatorRunID(env map[string]string) string {
	return firstNonEmpty(os.Getenv("PAYMENT_SIMULATOR_RUN_ID"), env["PAYMENT_SIMULATOR_RUN_ID"], env["CLOUD_STACK_NAME"])
}

func lkePaymentSimulatorPublicDomain(env map[string]string) string {
	return firstNonEmpty(os.Getenv("PAYMENT_SIMULATOR_DOMAIN"), env["PAYMENT_SIMULATOR_DOMAIN"], "payment-simulator."+env["VIDEO_CLOUD_DOMAIN"])
}

func lkePaymentSimulatorInternalURL(env map[string]string) string {
	return "http://payment-simulator." + lkeNamespaceName(env, "account-manager") + ".svc.cluster.local:80"
}

func lkePaymentReferenceEncryptionKey(env map[string]string) string {
	if value := strings.TrimSpace(firstNonEmpty(os.Getenv("PAYMENT_REFERENCE_ENCRYPTION_KEY"), env["PAYMENT_REFERENCE_ENCRYPTION_KEY"])); value != "" {
		return value
	}
	seed := sha256.Sum256([]byte(lkeRuntimeSecretValue("payment-reference-encryption")))
	return base64.StdEncoding.EncodeToString(seed[:])
}

func lkeAccountManagerImage(env map[string]string) string {
	for _, workload := range lkeWorkloads(env) {
		if workload.Key == "account-manager" {
			return workload.Image
		}
	}
	return ""
}

func lkePaymentSimulatorServiceManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: payment-simulator
  namespace: %s
  labels:
    app.kubernetes.io/name: payment-simulator
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  selector:
    app.kubernetes.io/name: payment-simulator
  ports:
    - { name: http, port: 80, targetPort: 8081 }
`, lkeNamespaceName(env, "account-manager"), env["CLOUD_STACK_NAME"])
}

func lkePaymentSimulatorDeploymentManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: payment-simulator
  namespace: %s
  labels:
    app.kubernetes.io/name: payment-simulator
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  selector:
    matchLabels: { app.kubernetes.io/name: payment-simulator }
  template:
    metadata:
      labels:
        app.kubernetes.io/name: payment-simulator
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
      annotations:
        rtk.realtek.com/runtime-checksum: %q
    spec:
      imagePullSecrets:
        - name: %s
      containers:
        - name: simulator
          image: %s
          imagePullPolicy: IfNotPresent
          command: ["/app/rtk-account-manager-payment-simulator"]
          envFrom:
            - secretRef: { name: account-manager-runtime }
          env:
            - { name: PORT, value: "8081" }
          ports:
            - { name: http, containerPort: 8081 }
          readinessProbe:
            httpGet: { path: /internal/v1/health, port: http }
            initialDelaySeconds: 2
            periodSeconds: 5
          resources:
            requests: { cpu: 25m, memory: 64Mi }
            limits: { cpu: 250m, memory: 256Mi }
`, lkeNamespaceName(env, "account-manager"), env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], lkeConfigChecksum(lkePaymentSimulatorRunID(env), lkePaymentSimulatorInternalURL(env), lkePaymentSimulatorPublicDomain(env), lkeRuntimeSecretValue("payment-simulator-shared"), lkeRuntimeSecretValue("payment-simulator-callback")), lkeImagePullSecretName(env), lkeAccountManagerImage(env))
}

func lkeAccountManagerPaymentWorkerManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: account-manager-payment-worker
  namespace: %s
  labels:
    app.kubernetes.io/name: account-manager-payment-worker
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  selector:
    matchLabels: { app.kubernetes.io/name: account-manager-payment-worker }
  template:
    metadata:
      labels:
        app.kubernetes.io/name: account-manager-payment-worker
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
      annotations:
        rtk.realtek.com/runtime-checksum: %q
    spec:
      imagePullSecrets:
        - name: %s
      containers:
        - name: worker
          image: %s
          imagePullPolicy: IfNotPresent
          command: ["/app/rtk-account-manager-payment-worker"]
          envFrom:
            - secretRef: { name: account-manager-runtime }
          resources:
            requests: { cpu: 25m, memory: 64Mi }
            limits: { cpu: 250m, memory: 256Mi }
`, lkeNamespaceName(env, "account-manager"), env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], lkeConfigChecksum(lkePaymentSimulatorRunID(env), lkePaymentSimulatorInternalURL(env), lkePaymentReferenceEncryptionKey(env), lkeRuntimeSecretValue("payment-simulator-shared")), lkeImagePullSecretName(env), lkeAccountManagerImage(env))
}

func lkeVideoCloudLifecycleInternalURL(env map[string]string) string {
	return "http://video-cloud-api." + lkeNamespaceName(env, "video-cloud") + ".svc.cluster.local:80"
}

func lkeEmailOutboxEncryptionKey(env map[string]string) string {
	if value := strings.TrimSpace(firstNonEmpty(os.Getenv("EMAIL_OUTBOX_ENCRYPTION_KEY"), env["EMAIL_OUTBOX_ENCRYPTION_KEY"])); value != "" {
		return value
	}
	seed := sha256.Sum256([]byte(lkeRuntimeSecretValue("email-outbox-encryption")))
	return base64.StdEncoding.EncodeToString(seed[:])
}

func lkeEmailDeliveryEnabled(env map[string]string) bool {
	delivery := strings.ToLower(strings.TrimSpace(firstNonEmpty(os.Getenv("AUTH_TOKEN_DELIVERY"), env["AUTH_TOKEN_DELIVERY"])))
	if delivery == "" {
		return strings.TrimSpace(lkeEnvValue(env, "SMTP_HOST")) != ""
	}
	return delivery == "smtp" || delivery == "sendmail_http"
}

func lkeAccountManagerEmailWorkerManifest(env map[string]string) string {
	checksum := lkeConfigChecksum(
		lkeAccountManagerDatabaseURL(env),
		lkeEnvValue(env, "AUTH_TOKEN_DELIVERY"),
		lkeEnvValue(env, "AUTH_TOKEN_BASE_URL"),
		lkeEnvValue(env, "SMTP_HOST"),
		lkeEnvValue(env, "SMTP_PORT"),
		lkeEnvValue(env, "SMTP_USERNAME"),
		lkeEnvValue(env, "SMTP_PASSWORD"),
		lkeEnvValue(env, "SMTP_FROM"),
		lkeEnvValue(env, "SMTP_FROM_NAME"),
		lkeEnvValue(env, "SMTP_ENCRYPTION"),
		lkeEnvValue(env, "SENDMAIL_HTTP_BASE_URL"),
		lkeEnvValue(env, "SENDMAIL_HTTP_BEARER_TOKEN"),
		lkeEnvValue(env, "SENDMAIL_HTTP_TIMEOUT"),
		lkeEmailOutboxEncryptionKey(env),
	)
	return lkeAccountManagerEmailWorkerManifestWithChecksum(env, checksum)
}

func lkeAccountManagerEmailWorkerManifestWithChecksum(env map[string]string, checksum string) string {
	image := ""
	for _, workload := range lkeWorkloads(env) {
		if workload.Key == "account-manager" {
			image = workload.Image
			break
		}
	}
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: account-manager-email-worker
  namespace: %s
  labels:
    app.kubernetes.io/name: account-manager-email-worker
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  strategy:
    type: RollingUpdate
  selector:
    matchLabels:
      app.kubernetes.io/name: account-manager-email-worker
  template:
    metadata:
      labels:
        app.kubernetes.io/name: account-manager-email-worker
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
      annotations:
        rtk.realtek.com/runtime-checksum: %q
    spec:
      imagePullSecrets:
        - name: %s
      containers:
        - name: worker
          image: %s
          imagePullPolicy: IfNotPresent
          command: ["/app/rtk-account-manager-email-worker"]
          envFrom:
            - secretRef:
                name: account-manager-runtime
          resources:
            requests:
              cpu: 25m
              memory: 64Mi
            limits:
              cpu: 250m
              memory: 256Mi
`, lkeNamespaceName(env, "account-manager"), env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], checksum, lkeImagePullSecretName(env), image)
}

func lkeAccountManagerOutboxWorkerManifest(env map[string]string) string {
	checksum := lkeConfigChecksum(
		lkeAccountManagerDatabaseURL(env),
		lkeVideoCloudLifecycleInternalURL(env),
		lkeInternalAuthToken(),
		firstNonEmpty(lkeEnvValue(env, "VIDEO_CLOUD_LIFECYCLE_TIMEOUT"), "10s"),
	)
	image := ""
	for _, workload := range lkeWorkloads(env) {
		if workload.Key == "account-manager" {
			image = workload.Image
			break
		}
	}
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: account-manager-outbox-worker
  namespace: %s
  labels:
    app.kubernetes.io/name: account-manager-outbox-worker
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  strategy:
    type: RollingUpdate
  selector:
    matchLabels:
      app.kubernetes.io/name: account-manager-outbox-worker
  template:
    metadata:
      labels:
        app.kubernetes.io/name: account-manager-outbox-worker
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
      annotations:
        rtk.realtek.com/runtime-checksum: %q
    spec:
      imagePullSecrets:
        - name: %s
      containers:
        - name: worker
          image: %s
          imagePullPolicy: IfNotPresent
          command: ["/app/rtk-account-manager-outbox-worker"]
          envFrom:
            - secretRef:
                name: account-manager-runtime
          resources:
            requests:
              cpu: 25m
              memory: 64Mi
            limits:
              cpu: 250m
              memory: 256Mi
`, lkeNamespaceName(env, "account-manager"), env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], checksum, lkeImagePullSecretName(env), image)
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
      imagePullSecrets:
        - name: %s
      containers:
        - name: migrate
          image: %s
          imagePullPolicy: IfNotPresent
          command: ["/app/rtk-account-manager-migrate"]
          envFrom:
            - secretRef:
                name: account-manager-runtime
`, lkeNamespaceName(env, "account-manager"), env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], lkeImagePullSecretName(env), image)
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
	body := fmt.Sprintf("ACCOUNT_MANAGER_INTERNAL_AUTH_TOKEN=%s\nFACTORY_PRODUCTION_JWT_SECRET=%s\nFACTORY_PRODUCTION_JWT_AUDIENCE=%s\n", lkeInternalAuthToken(), lkeFactoryProductionJWTSecret(env), lkeFactoryProductionJWTAudience(env))
	return os.WriteFile(path, []byte(body), 0o600)
}

func writeLKEVideoCloudRuntimeEnv(paths provisionPaths, env map[string]string) error {
	path := filepath.Join(paths.EnvRoot, "services", "video-cloud", "video-cloud.env")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	body := fmt.Sprintf("FACTORY_ENROLL_AUTH_KEY=%s\nFACTORY_ENROLL_PRODUCTION_JWT_SECRET=%s\nFACTORY_ENROLL_PRODUCTION_JWT_AUDIENCE=%s\nVIDEO_CLOUD_AUTH_SECRET=%s\nVIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN=%s\n", lkeFactoryEnrollAuthKey(env), lkeFactoryProductionJWTSecret(env), lkeFactoryProductionJWTAudience(env), lkeRuntimeSecretValue("video-auth"), lkeInternalAuthToken())
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
	if lkeRuntimeSecretStateDir != "" {
		path := filepath.Join(lkeRuntimeSecretStateDir, lkeSecretFileName(name))
		if value, err := readSensitiveFile(path, "LKE runtime secret "+name); err == nil {
			lkeRuntimeSecretCache[name] = value
			return value
		}
		value := randomSecret()
		if err := writeSensitiveFile(path, value); err == nil {
			lkeRuntimeSecretCache[name] = value
			return value
		}
	}
	value := randomSecret()
	lkeRuntimeSecretCache[name] = value
	return value
}

func lkeSecretFileName(name string) string {
	replacer := strings.NewReplacer("/", "-", "\\", "-", "..", "-")
	return replacer.Replace(strings.TrimSpace(name))
}

func randomSecret() string {
	var buf [24]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return base64.RawURLEncoding.EncodeToString(buf[:])
}

func lkeConfigChecksum(values ...string) string {
	h := sha256.New()
	for _, value := range values {
		_, _ = h.Write([]byte(value))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func lkeDeploymentManifest(env map[string]string, workload lkeWorkload, certIssuerMaterial *lkeCertIssuerMaterial) string {
	envFrom := ""
	extraEnv := ""
	templateAnnotations := ""
	topologySpread := lkeTopologySpreadManifest(workload.Name)
	probes := lkeDeploymentProbeManifest(workload.Name)
	imagePullSecrets := lkeDeploymentImagePullSecretsManifest(env)
	replicas := lkeWorkloadReplicas(env, workload)
	strategy := lkeDeploymentStrategyManifest(workload)
	volumeMounts := ""
	volumes := ""
	if workload.Key == "account-manager" {
		checksumValues := []string{
			lkeAccountManagerDatabaseURL(env),
			lkeRuntimeSecretValue("jwt-access"),
			lkeRuntimeSecretValue("jwt-refresh"),
			lkeInternalAuthToken(),
			lkeFactoryProductionJWTSecret(env),
			lkeFactoryProductionJWTAudience(env),
			lkePlatformAdminEmail(env),
			lkeRuntimeSecretValue("platform-admin"),
			lkeCertIssuerBaseURL(env),
			lkeEnvValue(env, "AUTH_TOKEN_DELIVERY"),
			lkeEnvValue(env, "AUTH_TOKEN_BASE_URL"),
			lkeEnvValue(env, "SMTP_HOST"),
			lkeEnvValue(env, "SMTP_PORT"),
			lkeEnvValue(env, "SMTP_USERNAME"),
			lkeEnvValue(env, "SMTP_PASSWORD"),
			lkeEnvValue(env, "SMTP_FROM"),
			lkeEnvValue(env, "SMTP_FROM_NAME"),
			lkeEnvValue(env, "SMTP_ENCRYPTION"),
			lkeEnvValue(env, "SENDMAIL_HTTP_BASE_URL"),
			lkeEnvValue(env, "SENDMAIL_HTTP_BEARER_TOKEN"),
			lkeEnvValue(env, "SENDMAIL_HTTP_TIMEOUT"),
			lkeEmailOutboxEncryptionKey(env),
		}
		if certIssuerMaterial != nil {
			checksumValues = append(checksumValues, certIssuerMaterial.ClientCert, certIssuerMaterial.ServiceCA)
		}
		templateAnnotations = fmt.Sprintf(`      annotations:
        rtk.realtek.com/runtime-checksum: %q
`, lkeConfigChecksum(checksumValues...))
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
		templateAnnotations = fmt.Sprintf(`      annotations:
        rtk.realtek.com/runtime-checksum: %q
`, lkeVideoCloudRuntimeChecksum(env))
		mqttHandlerConcurrency := firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_MQTT_HANDLER_CONCURRENCY"), env["LKE_VIDEO_CLOUD_MQTT_HANDLER_CONCURRENCY"], "64")
		mqttShadowHandlerConcurrency := firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_MQTT_SHADOW_HANDLER_CONCURRENCY"), env["LKE_VIDEO_CLOUD_MQTT_SHADOW_HANDLER_CONCURRENCY"], "64")
		mqttShadowQueueSize := firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_MQTT_SHADOW_QUEUE_SIZE"), env["LKE_VIDEO_CLOUD_MQTT_SHADOW_QUEUE_SIZE"], "8192")
		mqttMessageHandlerConcurrency := firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_MQTT_MESSAGE_HANDLER_CONCURRENCY"), env["LKE_VIDEO_CLOUD_MQTT_MESSAGE_HANDLER_CONCURRENCY"], "128")
		mqttMessageQueueSize := firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_MQTT_MESSAGE_QUEUE_SIZE"), env["LKE_VIDEO_CLOUD_MQTT_MESSAGE_QUEUE_SIZE"], "16384")
		mqttLogHandlerConcurrency := firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_MQTT_LOG_HANDLER_CONCURRENCY"), env["LKE_VIDEO_CLOUD_MQTT_LOG_HANDLER_CONCURRENCY"], "32")
		mqttLogQueueSize := firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_MQTT_LOG_QUEUE_SIZE"), env["LKE_VIDEO_CLOUD_MQTT_LOG_QUEUE_SIZE"], "8192")
		mqttOutboundConnections := firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_MQTT_OUTBOUND_CONNECTIONS"), env["LKE_VIDEO_CLOUD_MQTT_OUTBOUND_CONNECTIONS"], "16")
		mqttOutboundQueueSize := firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_MQTT_OUTBOUND_QUEUE_SIZE"), env["LKE_VIDEO_CLOUD_MQTT_OUTBOUND_QUEUE_SIZE"], "8192")
		mqttOutboundWriteTimeout := firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_MQTT_OUTBOUND_WRITE_TIMEOUT"), env["LKE_VIDEO_CLOUD_MQTT_OUTBOUND_WRITE_TIMEOUT"], "10s")
		shadowCacheTTL := firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_SHADOW_CACHE_TTL"), env["LKE_VIDEO_CLOUD_SHADOW_CACHE_TTL"], "24h")
		webrtcSignalingStoreTTLGrace := firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_WEBRTC_SIGNALING_STORE_TTL_GRACE"), env["LKE_VIDEO_CLOUD_WEBRTC_SIGNALING_STORE_TTL_GRACE"], "30s")
		volumeMounts = `          volumeMounts:
            - name: clip-crypto
              mountPath: /etc/video_cloud/clip-crypto
              readOnly: true
`
		volumes = `      volumes:
        - name: clip-crypto
          secret:
            secretName: video-cloud-runtime
            items:
              - key: clip-private-key.pem
                path: clip-private-key.pem
`
		extraEnv = fmt.Sprintf(`            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: POSTGRES_PASSWORD
            - name: VIDEO_CLOUD_AUTH_SECRET
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: VIDEO_CLOUD_AUTH_SECRET
            - name: VIDEO_CLOUD_API_ADDR
              value: ":8080"
            - name: VIDEO_CLOUD_API_BASE_URL
              value: %q
            - name: VIDEO_CLOUD_DB_DSN
              value: "postgres://postgres:$(POSTGRES_PASSWORD)@postgresql.%s.svc.cluster.local:5432/video_cloud?sslmode=disable"
            - name: VIDEO_CLOUD_DB_MAX_OPEN_CONNS
              value: %q
            - name: VIDEO_CLOUD_DB_MAX_IDLE_CONNS
              value: %q
            - name: VIDEO_CLOUD_DB_CONN_MAX_LIFETIME
              value: %q
            - name: VIDEO_CLOUD_DB_ENSURE_SCHEMA
              value: %q
            - name: VIDEO_CLOUD_CLIP_PRIVATE_KEY_PATH
              value: "/etc/video_cloud/clip-crypto/clip-private-key.pem"
            - name: VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN
            - name: VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_URL
              value: %q
            - name: VIDEO_CLOUD_LOGGER_ENDPOINT
              value: %q
            - name: VIDEO_CLOUD_LOGGER_TOKEN
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: VIDEO_CLOUD_LOGGER_TOKEN
            - name: VIDEO_CLOUD_LOGGER_SPOOL_DIR
              value: "/var/lib/video_cloud/logger-spool"
            - name: VIDEO_CLOUD_LOGGER_SPOOL_MAX_BYTES
              value: %q
            - name: VIDEO_CLOUD_AUTH_TRUSTED_CLIENT_CERT_HEADERS
              value: "true"
            - name: VIDEO_CLOUD_MQTT_ENABLED
              value: "true"
            - name: VIDEO_CLOUD_MQTT_ADDR
              value: %q
            - name: VIDEO_CLOUD_MQTT_USERNAME
              value: "video-cloud-server"
            - name: VIDEO_CLOUD_MQTT_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: VIDEO_CLOUD_MQTT_SERVER_PASSWORD
            - name: VIDEO_CLOUD_MQTT_SERVER_USERNAME
              value: "video-cloud-server"
            - name: VIDEO_CLOUD_MQTT_SERVER_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: VIDEO_CLOUD_MQTT_SERVER_PASSWORD
            - name: VIDEO_CLOUD_MQTT_BROKER_AUTH_KEY
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: VIDEO_CLOUD_MQTT_BROKER_AUTH_KEY
            - name: VIDEO_CLOUD_MQTT_TENANT_NAMESPACE_ENABLED
              value: %q
            - name: VIDEO_CLOUD_MQTT_TENANT_PREFIX
              value: "_bc"
            - name: POD_NAME
              valueFrom:
                fieldRef:
                  fieldPath: metadata.name
            - name: POD_IP
              valueFrom:
                fieldRef:
                  fieldPath: status.podIP
            - name: VIDEO_CLOUD_MQTT_CLIENT_ID
              value: "video-cloud-api-$(POD_NAME)"
            - name: VIDEO_CLOUD_MQTT_TOPIC_ROOT
              value: "devices"
            - name: VIDEO_CLOUD_MQTT_HANDLER_CONCURRENCY
              value: %q
            - name: VIDEO_CLOUD_MQTT_SHADOW_HANDLER_CONCURRENCY
              value: %q
            - name: VIDEO_CLOUD_MQTT_SHADOW_QUEUE_SIZE
              value: %q
            - name: VIDEO_CLOUD_MQTT_MESSAGE_HANDLER_CONCURRENCY
              value: %q
            - name: VIDEO_CLOUD_MQTT_MESSAGE_QUEUE_SIZE
              value: %q
            - name: VIDEO_CLOUD_MQTT_LOG_HANDLER_CONCURRENCY
              value: %q
            - name: VIDEO_CLOUD_MQTT_LOG_QUEUE_SIZE
              value: %q
            - name: VIDEO_CLOUD_MQTT_OUTBOUND_CONNECTIONS
              value: %q
            - name: VIDEO_CLOUD_MQTT_OUTBOUND_QUEUE_SIZE
              value: %q
            - name: VIDEO_CLOUD_MQTT_OUTBOUND_WRITE_TIMEOUT
              value: %q
            - name: VIDEO_CLOUD_SHADOW_CACHE_ENABLED
              value: "true"
            - name: VIDEO_CLOUD_SHADOW_CACHE_ADDR
              value: "redis.%s.svc.cluster.local:6379"
            - name: VIDEO_CLOUD_SHADOW_CACHE_TTL
              value: %q
            - name: VIDEO_CLOUD_SHADOW_CACHE_WRITE_BEHIND_ENABLED
              value: %q
            - name: VIDEO_CLOUD_SHADOW_CACHE_FLUSH_INTERVAL
              value: %q
            - name: VIDEO_CLOUD_SHADOW_CACHE_FLUSH_BATCH_SIZE
              value: %q
            - name: VIDEO_CLOUD_SHADOW_CACHE_BUFFER_MAX_DOCS
              value: %q
            - name: VIDEO_CLOUD_SHADOW_CACHE_RECOVERY_INTERVAL
              value: %q
            - name: VIDEO_CLOUD_WEBRTC_STUN_URLS
              value: %q
            - name: VIDEO_CLOUD_WEBRTC_TURN_URLS
              value: %q
            - name: VIDEO_CLOUD_TURN_REALM
              value: %q
            - name: VIDEO_CLOUD_TURN_SHARED_SECRET
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: VIDEO_CLOUD_TURN_SHARED_SECRET
            - name: VIDEO_CLOUD_TURN_CREDENTIAL_TTL
              value: %q
            - name: VIDEO_CLOUD_WEBRTC_ICE_POLICY
              value: %q
            - name: VIDEO_CLOUD_TURN_REGISTRY_ADDR
              value: "http://video-cloud-turnregistry.%s.svc.cluster.local:18190"
            - name: VIDEO_CLOUD_TURN_REGISTRY_CLIENT_NODE_ID
              value: "video-cloud-api"
            - name: VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY
              valueFrom:
                secretKeyRef:
                  name: video-cloud-workers-runtime
                  key: VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY
            - name: VIDEO_CLOUD_WEBRTC_SIGNALING_STORE_ENABLED
              value: "true"
            - name: VIDEO_CLOUD_WEBRTC_SIGNALING_STORE_ADDR
              value: "redis.%s.svc.cluster.local:6379"
            - name: VIDEO_CLOUD_WEBRTC_SIGNALING_STORE_PREFIX
              value: "video_cloud:webrtc"
            - name: VIDEO_CLOUD_WEBRTC_SIGNALING_STORE_TTL_GRACE
              value: %q
`,
			lkeVideoCloudAPIBaseURL(env),
			lkeNamespaceName(env, "platform"),
			lkeVideoCloudAPIDBMaxOpenConns(env),
			lkeVideoCloudAPIDBMaxIdleConns(env),
			lkeVideoCloudDBConnMaxLifetime(env),
			lkeVideoCloudDBEnsureSchema(env),
			lkeAccountManagerInternalURL(env),
			lkeCloudLoggerEndpoint(env),
			firstNonEmpty(os.Getenv("VIDEO_CLOUD_LOGGER_SPOOL_MAX_BYTES"), "104857600"),
			lkeMQTTInternalAddr(env),
			strconv.FormatBool(lkeMQTTTenantNamespaceEnabled(env)),
			mqttHandlerConcurrency,
			mqttShadowHandlerConcurrency,
			mqttShadowQueueSize,
			mqttMessageHandlerConcurrency,
			mqttMessageQueueSize,
			mqttLogHandlerConcurrency,
			mqttLogQueueSize,
			mqttOutboundConnections,
			mqttOutboundQueueSize,
			mqttOutboundWriteTimeout,
			lkeNamespaceName(env, "platform"),
			shadowCacheTTL,
			lkeVideoCloudShadowCacheWriteBehindEnabled(env),
			lkeVideoCloudShadowCacheFlushInterval(env),
			lkeVideoCloudShadowCacheFlushBatchSize(env),
			lkeVideoCloudShadowCacheBufferMaxDocs(env),
			lkeVideoCloudShadowCacheRecoveryInterval(env),
			lkeCoturnSTUNURLs(env),
			lkeCoturnTURNURLs(env),
			lkeCoturnRealm(env),
			lkeCoturnCredentialTTL(env),
			lkeWebRTCICEPolicy(env),
			lkeNamespaceName(env, "video-cloud"),
			lkeNamespaceName(env, "platform"),
			webrtcSignalingStoreTTLGrace,
		)
		extraEnv += lkeBlobEnvironmentManifest(env, "video-cloud-runtime")
		volumeMounts = `          volumeMounts:
            - name: logger-spool
              mountPath: /var/lib/video_cloud/logger-spool
            - name: clip-crypto
              mountPath: /etc/video_cloud/clip-crypto
              readOnly: true
`
		volumes = `      volumes:
        - name: logger-spool
          emptyDir: {}
        - name: clip-crypto
          secret:
            secretName: video-cloud-runtime
            items:
              - key: clip-private-key.pem
                path: clip-private-key.pem
`
	}
	if workload.Key == "cloud-admin" {
		extraEnv = fmt.Sprintf(`            - name: ACCOUNT_MANAGER_BASE_URL
              value: %q
            - name: CLOUD_ADMIN_GRAFANA_BASE_URL
              value: %q
            - name: CLOUD_ADMIN_GRAFANA_DASHBOARD_PATH
              value: %q
`, lkeAccountManagerInternalURL(env), lkeGrafanaInternalURL(env), lkeGrafanaDashboardPath(env))
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
  replicas: %s
%s
  selector:
    matchLabels:
      app.kubernetes.io/name: %s
  template:
    metadata:
%s
      labels:
        app.kubernetes.io/name: %s
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
%s
%s
      containers:
        - name: app
          image: %s
          imagePullPolicy: IfNotPresent
%s
          ports:
            - name: http
              containerPort: %d
%s
          env:
            - name: CLOUD_PROVIDER
              value: "lke"
            - name: CLOUD_STACK_NAME
              value: %q
            - name: SERVICE_PUBLIC_HOST
              value: %q
%s%s%s%s`, workload.Name, workload.Namespace, workload.Name, env["CLOUD_STACK_NAME"], replicas, strategy, workload.Name, templateAnnotations, workload.Name, env["CLOUD_STACK_NAME"], imagePullSecrets, topologySpread, workload.Image, lkeContainerResourcesManifest(env, workload.Name), workload.Port, probes, env["CLOUD_STACK_NAME"], workload.Host, extraEnv, envFrom, volumeMounts, volumes)
}

func lkeBlobEnvironmentManifest(env map[string]string, secretName string) string {
	return fmt.Sprintf(`            - name: VIDEO_CLOUD_BLOB_ENDPOINT
              value: %q
            - name: VIDEO_CLOUD_BLOB_REGION
              value: %q
            - name: VIDEO_CLOUD_BLOB_BUCKET
              value: %q
            - name: VIDEO_CLOUD_BLOB_PREFIX
              value: %q
            - name: VIDEO_CLOUD_BLOB_FORCE_PATH_STYLE
              value: %q
            - name: VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED
              value: %q
            - name: VIDEO_CLOUD_CLIP_VERIFIER_ADDR
              value: %q
            - name: VIDEO_CLOUD_CLIP_UPLOAD_URL_TTL
              value: %q
            - name: VIDEO_CLOUD_CLIP_UPLOAD_SESSION_TTL
              value: %q
            - name: VIDEO_CLOUD_CLIP_UPLOAD_MAX_BYTES
              value: %q
            - name: VIDEO_CLOUD_CLIP_THUMBNAIL_MAX_BYTES
              value: %q
            - name: VIDEO_CLOUD_CLIP_VERIFY_POLL_INTERVAL
              value: %q
            - name: VIDEO_CLOUD_CLIP_VERIFY_SWEEP_INTERVAL
              value: %q
            - name: AWS_ACCESS_KEY_ID
              valueFrom:
                secretKeyRef:
                  name: %s
                  key: AWS_ACCESS_KEY_ID
            - name: AWS_SECRET_ACCESS_KEY
              valueFrom:
                secretKeyRef:
                  name: %s
                  key: AWS_SECRET_ACCESS_KEY
`, env["VIDEO_CLOUD_BLOB_ENDPOINT"], env["VIDEO_CLOUD_BLOB_REGION"], env["VIDEO_CLOUD_BLOB_BUCKET"], env["VIDEO_CLOUD_BLOB_PREFIX"], firstNonEmpty(env["VIDEO_CLOUD_BLOB_FORCE_PATH_STYLE"], "false"), strconv.FormatBool(lkeClipDirectUploadEnabled(env)), firstNonEmpty(env["VIDEO_CLOUD_CLIP_VERIFIER_ADDR"], "0.0.0.0:19500"), firstNonEmpty(env["VIDEO_CLOUD_CLIP_UPLOAD_URL_TTL"], "10m"), firstNonEmpty(env["VIDEO_CLOUD_CLIP_UPLOAD_SESSION_TTL"], "30m"), firstNonEmpty(env["VIDEO_CLOUD_CLIP_UPLOAD_MAX_BYTES"], "268435456"), firstNonEmpty(env["VIDEO_CLOUD_CLIP_THUMBNAIL_MAX_BYTES"], "5242880"), firstNonEmpty(env["VIDEO_CLOUD_CLIP_VERIFY_POLL_INTERVAL"], "500ms"), firstNonEmpty(env["VIDEO_CLOUD_CLIP_VERIFY_SWEEP_INTERVAL"], "1m"), secretName, secretName)
}

func lkeClipDirectUploadEnabled(env map[string]string) bool {
	value := strings.TrimSpace(env["VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED"])
	if value == "" {
		return true
	}
	enabled, err := strconv.ParseBool(value)
	return err != nil || enabled
}

func lkeVideoCloudRuntimeChecksum(env map[string]string) string {
	return lkeConfigChecksum(
		lkeRuntimeSecretValue("postgres"),
		lkeRuntimeSecretValue("video-auth"),
		lkeRuntimeSecretValue("mqtt-broker-auth"),
		lkeRuntimeSecretValue("mqtt-server-password"),
		lkeClipPrivateKeyPEM(),
		lkeVideoCloudAPIBaseURL(env),
		env["VIDEO_CLOUD_BLOB_ENDPOINT"],
		env["VIDEO_CLOUD_BLOB_REGION"],
		env["VIDEO_CLOUD_BLOB_BUCKET"],
		env["VIDEO_CLOUD_BLOB_PREFIX"],
		lkeObjectStorageCredential(env, "LINODE_OBJ_ACCESS_KEY_ID"),
		lkeObjectStorageCredential(env, "LINODE_OBJ_SECRET_ACCESS_KEY"),
		strconv.FormatBool(lkeMQTTTenantNamespaceEnabled(env)),
	)
}

func lkeDeploymentImagePullSecretsManifest(env map[string]string) string {
	return fmt.Sprintf(`      imagePullSecrets:
        - name: %s
`, lkeImagePullSecretName(env))
}

func lkeWorkloadReplicas(env map[string]string, workload lkeWorkload) string {
	if spec, ok := capacityWorkloadSpecForName(workload.Name); ok {
		if effective := strings.TrimSpace(env[spec.Prefix+"_EFFECTIVE_REPLICAS"]); effective != "" {
			return effective
		}
	}
	switch workload.Key {
	case "account-manager":
		return firstNonEmpty(os.Getenv("LKE_ACCOUNT_MANAGER_REPLICAS"), env["LKE_ACCOUNT_MANAGER_REPLICAS"], "1")
	case "video-cloud":
		return firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_REPLICAS"), env["LKE_VIDEO_CLOUD_REPLICAS"], "2")
	}
	return "1"
}

func lkeDeploymentStrategyManifest(workload lkeWorkload) string {
	if workload.Key != "video-cloud" {
		return ""
	}
	return `  strategy:
    type: RollingUpdate
    rollingUpdate:
      maxSurge: 0
      maxUnavailable: 1
`
}

func lkeDeploymentProbeManifest(name string) string {
	if name != "video-cloud-api" {
		return ""
	}
	return `          readinessProbe:
            httpGet:
              path: /healthz
              port: http
            initialDelaySeconds: 3
            periodSeconds: 5
            timeoutSeconds: 2
            failureThreshold: 3
          livenessProbe:
            httpGet:
              path: /healthz
              port: http
            initialDelaySeconds: 30
            periodSeconds: 10
            timeoutSeconds: 2
            failureThreshold: 6
`
}

func lkeTopologySpreadManifest(name string) string {
	switch name {
	case "account-manager", "video-cloud-api":
		return fmt.Sprintf(`      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: kubernetes.io/hostname
          whenUnsatisfiable: DoNotSchedule
          labelSelector:
            matchLabels:
              app.kubernetes.io/name: %s
`, name)
	default:
		return ""
	}
}

func lkeContainerResourcesManifest(env map[string]string, name string) string {
	profile, ok := lkeContainerResourceProfile(env, name)
	if !ok {
		return ""
	}
	// Empty request values are valid for an unset capacity profile, but an
	// empty Kubernetes quantity is not. Omit the optional resources block until
	// the operator supplies a complete request profile.
	if strings.TrimSpace(profile.requestCPU) == "" || strings.TrimSpace(profile.requestMemory) == "" {
		return ""
	}
	return fmt.Sprintf(`          resources:
            requests:
              cpu: %q
              memory: %q
            limits:
              memory: %q
`, profile.requestCPU, profile.requestMemory, profile.limitMemory)
}

type lkeResourceProfile struct {
	requestCPU    string
	requestMemory string
	limitMemory   string
}

func lkeContainerResourceProfile(env map[string]string, name string) (lkeResourceProfile, bool) {
	limits := map[string]string{
		"account-manager": "1Gi", "cloud-admin": "512Mi", "cloud-logger": "2Gi", "frontend": "512Mi",
		"mqtt": "1536Mi", "video-cloud-api": "1536Mi", "video-cloud-clipverifier": "1Gi", "video-cloud-logingester": "1Gi", "video-cloud-mqttusage": "1Gi",
		"loki": "2Gi",
	}
	spec, ok := capacityWorkloadSpecForName(name)
	if !ok {
		return lkeResourceProfile{}, false
	}
	profile := lkeResourceProfile{
		requestCPU:    firstNonEmpty(os.Getenv(spec.Prefix+"_REQUEST_CPU"), env[spec.Prefix+"_REQUEST_CPU"]),
		requestMemory: firstNonEmpty(os.Getenv(spec.Prefix+"_REQUEST_MEMORY"), env[spec.Prefix+"_REQUEST_MEMORY"]),
		limitMemory:   firstNonEmpty(os.Getenv(spec.Prefix+"_LIMIT_MEMORY"), env[spec.Prefix+"_LIMIT_MEMORY"], limits[name], env[spec.Prefix+"_REQUEST_MEMORY"]),
	}
	return profile, true
}

func lkeVideoCloudAPIDBMaxOpenConns(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_API_DB_MAX_OPEN_CONNS"), env["LKE_VIDEO_CLOUD_API_DB_MAX_OPEN_CONNS"], "40")
}

func lkeVideoCloudAPIDBMaxIdleConns(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_API_DB_MAX_IDLE_CONNS"), env["LKE_VIDEO_CLOUD_API_DB_MAX_IDLE_CONNS"], "20")
}

func lkeVideoCloudMQTTHandlerConcurrency(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_MQTT_HANDLER_CONCURRENCY"), env["LKE_VIDEO_CLOUD_MQTT_HANDLER_CONCURRENCY"], "64")
}

func lkeVideoCloudShadowCacheWriteBehindEnabled(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_SHADOW_CACHE_WRITE_BEHIND_ENABLED"), env["LKE_VIDEO_CLOUD_SHADOW_CACHE_WRITE_BEHIND_ENABLED"], "true")
}

func lkeVideoCloudShadowCacheFlushInterval(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_SHADOW_CACHE_FLUSH_INTERVAL"), env["LKE_VIDEO_CLOUD_SHADOW_CACHE_FLUSH_INTERVAL"], "1s")
}

func lkeVideoCloudShadowCacheFlushBatchSize(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_SHADOW_CACHE_FLUSH_BATCH_SIZE"), env["LKE_VIDEO_CLOUD_SHADOW_CACHE_FLUSH_BATCH_SIZE"], "500")
}

func lkeVideoCloudShadowCacheBufferMaxDocs(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_SHADOW_CACHE_BUFFER_MAX_DOCS"), env["LKE_VIDEO_CLOUD_SHADOW_CACHE_BUFFER_MAX_DOCS"], "10000")
}

func lkeVideoCloudShadowCacheRecoveryInterval(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_SHADOW_CACHE_RECOVERY_INTERVAL"), env["LKE_VIDEO_CLOUD_SHADOW_CACHE_RECOVERY_INTERVAL"], "5s")
}

func lkeVideoCloudWorkerDBMaxOpenConns(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_WORKER_DB_MAX_OPEN_CONNS"), env["LKE_VIDEO_CLOUD_WORKER_DB_MAX_OPEN_CONNS"], "4")
}

func lkeVideoCloudWorkerDBMaxIdleConns(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_WORKER_DB_MAX_IDLE_CONNS"), env["LKE_VIDEO_CLOUD_WORKER_DB_MAX_IDLE_CONNS"], "2")
}

func lkeVideoCloudDBConnMaxLifetime(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_DB_CONN_MAX_LIFETIME"), env["LKE_VIDEO_CLOUD_DB_CONN_MAX_LIFETIME"], "5m")
}

func lkeVideoCloudDBEnsureSchema(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_VIDEO_CLOUD_DB_ENSURE_SCHEMA"), env["LKE_VIDEO_CLOUD_DB_ENSURE_SCHEMA"], "false")
}

func lkeWebRTCICEPolicy(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_WEBRTC_ICE_POLICY"), os.Getenv("VIDEO_CLOUD_WEBRTC_ICE_POLICY"), env["LKE_WEBRTC_ICE_POLICY"], env["VIDEO_CLOUD_WEBRTC_ICE_POLICY"], "relay")
}

func lkeAccountManagerInternalURL(env map[string]string) string {
	return "http://account-manager." + lkeNamespaceName(env, "account-manager") + ".svc.cluster.local:80"
}

func lkeGrafanaInternalURL(env map[string]string) string {
	return "http://video-cloud-grafana." + lkeNamespaceName(env, "observability") + ".svc.cluster.local:3000"
}

func lkeGrafanaDashboardPath(env map[string]string) string {
	return firstNonEmpty(os.Getenv("CLOUD_ADMIN_GRAFANA_DASHBOARD_PATH"), env["CLOUD_ADMIN_GRAFANA_DASHBOARD_PATH"], "/d/rtk-lke-staging/rtk-lke-staging-overview")
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

func ensureLKEKubeAccess(paths provisionPaths, env map[string]string, allowCreate bool) error {
	expiredKubeconfig := false
	if kubeconfig := firstNonEmpty(os.Getenv("RTK_CLOUD_LKE_KUBECONFIG"), os.Getenv("LKE_KUBECONFIG")); kubeconfig != "" {
		if _, statErr := os.Stat(kubeconfig); statErr != nil {
			return statErr
		}
		if k8sKubeconfigReady(kubeconfig) {
			_ = os.Setenv("RTK_CLOUD_LKE_KUBECONFIG", kubeconfig)
			fmt.Fprintf(os.Stderr, "[lke] kubeconfig: %s\n", kubeconfig)
			return nil
		}
		expiredKubeconfig = true
		fmt.Fprintf(os.Stderr, "[lke] kubeconfig is unavailable or expired, refreshing: %s\n", kubeconfig)
	}
	stateKubeconfig := filepath.Join(paths.EnvRoot, "state", "kubeconfig.yaml")
	if _, statErr := os.Stat(stateKubeconfig); statErr == nil {
		if k8sKubeconfigReady(stateKubeconfig) {
			_ = os.Setenv("RTK_CLOUD_LKE_KUBECONFIG", stateKubeconfig)
			fmt.Fprintf(os.Stderr, "[lke] kubeconfig: %s\n", stateKubeconfig)
			return nil
		}
		expiredKubeconfig = true
		fmt.Fprintf(os.Stderr, "[lke] kubeconfig is unavailable or expired, refreshing: %s\n", stateKubeconfig)
	}
	out, err := exec.Command(lkeKubectl(), "config", "current-context").CombinedOutput()
	context := strings.TrimSpace(string(out))
	ready := exec.Command(lkeKubectl(), "--request-timeout=5s", "get", "--raw=/readyz").Run() == nil
	if !expiredKubeconfig && err == nil && context != "" && ready {
		fmt.Fprintf(os.Stderr, "[lke] kubectl context: %s\n", context)
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
		if !isLinodeNotFoundError(err) {
			return err
		}
		fmt.Fprintf(os.Stderr, "[lke] cluster %s is missing; rediscovering LKE cluster\n", clusterID)
		discovered, discoverErr := discoverLKECluster(token, paths, env, allowCreate)
		if discoverErr != nil {
			return discoverErr
		}
		clusterID = strconv.Itoa(discovered.ID)
		if err := writeLKEState(paths, discovered); err != nil {
			return err
		}
		kubeconfig, err = fetchLKEKubeconfig(token, clusterID)
		if err != nil {
			return err
		}
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
		envFileValue(filepath.Join(paths.EnvRoot, "adapters", "lke", "state.env"), "LKE_CLUSTER_ID"),
		envFileValue(filepath.Join(paths.EnvRoot, "env", "stack.env"), "LKE_CLUSTER_ID"),
	)
}

type lkeCluster struct {
	ID         int    `json:"id"`
	Label      string `json:"label"`
	Region     string `json:"region"`
	K8sVersion string `json:"k8s_version"`
}

type lkeNodePool struct {
	ID         int                   `json:"id"`
	Type       string                `json:"type"`
	Count      int                   `json:"count"`
	Label      string                `json:"label"`
	Labels     map[string]string     `json:"labels"`
	Taints     []lkeNodePoolTaint    `json:"taints"`
	Autoscaler lkeNodePoolAutoscaler `json:"autoscaler"`
}

type lkeNodePoolAutoscaler struct {
	Enabled bool `json:"enabled"`
	Min     int  `json:"min"`
	Max     int  `json:"max"`
}

type lkeNodePoolTaint struct {
	Key    string `json:"key"`
	Value  string `json:"value"`
	Effect string `json:"effect"`
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
	nodeCount, err := lkeNodeCount(env)
	if err != nil {
		return lkeCluster{}, err
	}
	nodePools := []map[string]any{
		{"type": nodeType, "count": nodeCount, "labels": map[string]string{"rtk.io/node-class": "broker"}},
	}
	if lkePostgresDedicatedNodePoolEnabled(env) {
		nodePools = append(nodePools, lkePostgresNodePoolPayload(env))
	}
	payload, err := json.Marshal(map[string]any{
		"label":       lkeClusterLabel(env),
		"region":      firstNonEmpty(env["CLOUD_REGION"], "us-sea"),
		"k8s_version": version,
		"tags":        []string{"rtk-cloud", env["CLOUD_STACK_NAME"], "staging"},
		"node_pools":  nodePools,
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

func ensureLKENodePool(paths provisionPaths, env map[string]string) error {
	token := resolveLinodeToken(paths.EnvRoot)
	if token == "" {
		fmt.Fprintln(os.Stderr, "[lke] skipping node pool reconcile: LINODE_TOKEN is not available")
		return nil
	}
	clusterID := lkeClusterID(paths, env)
	if clusterID == "" {
		cluster, err := discoverLKECluster(token, paths, env, false)
		if err != nil {
			return err
		}
		clusterID = strconv.Itoa(cluster.ID)
	}
	desiredCount, err := lkeNodeCount(env)
	if err != nil {
		return err
	}
	desiredType := firstNonEmpty(os.Getenv("LKE_NODE_TYPE"), env["LKE_NODE_TYPE"], "g6-standard-2")
	pools, err := listLKENodePools(token, clusterID)
	if err != nil {
		if !isLinodeNotFoundError(err) {
			return err
		}
		fmt.Fprintf(os.Stderr, "[lke] cluster %s is missing; refreshing LKE state\n", clusterID)
		cluster, recoverErr := recoverStaleLKECluster(token, paths, env)
		if recoverErr != nil {
			return recoverErr
		}
		clusterID = strconv.Itoa(cluster.ID)
		pools, err = listLKENodePools(token, clusterID)
		if err != nil {
			return err
		}
	}
	if len(pools) == 0 {
		return fmt.Errorf("LKE cluster %s has no node pools", clusterID)
	}
	if err := ensureLKEGeneralNodePool(env, token, clusterID, pools); err != nil {
		return err
	}
	pools, err = listLKENodePools(token, clusterID)
	if err != nil {
		return err
	}
	pool := pools[0]
	for _, candidate := range pools {
		if candidate.Type == desiredType && candidate.Labels["rtk.io/node-class"] != "general" && candidate.Labels["rtk.io/node-class"] != "database" {
			pool = candidate
			break
		}
	}
	if pool.Count == desiredCount && !lkeNodePoolAutoscalerNeedsReconcile(pool, desiredCount) && pool.Labels["rtk.io/node-class"] == "broker" {
		fmt.Fprintf(os.Stderr, "[lke] node pool %d already at count=%d\n", pool.ID, desiredCount)
		return ensureLKEPostgresNodePool(paths, env, token, clusterID, pools)
	}
	payloadMap := map[string]any{"count": desiredCount, "labels": map[string]string{"rtk.io/node-class": "broker"}}
	if lkeNodePoolAutoscalerNeedsReconcile(pool, desiredCount) {
		payloadMap["autoscaler"] = map[string]any{
			"enabled": false,
			"min":     desiredCount,
			"max":     desiredCount,
		}
	}
	payload, err := json.Marshal(payloadMap)
	if err != nil {
		return err
	}
	if _, err := linodeRequestRaw(token, "PUT", fmt.Sprintf("/lke/clusters/%s/pools/%d", clusterID, pool.ID), string(payload)); err != nil {
		return err
	}
	if pool.Count == desiredCount {
		fmt.Fprintf(os.Stderr, "[lke] reconciled node pool %d autoscaler min/max to count=%d\n", pool.ID, desiredCount)
	} else {
		fmt.Fprintf(os.Stderr, "[lke] resized node pool %d from count=%d to count=%d\n", pool.ID, pool.Count, desiredCount)
	}
	pools, err = listLKENodePools(token, clusterID)
	if err != nil {
		return err
	}
	return ensureLKEPostgresNodePool(paths, env, token, clusterID, pools)
}

func ensureLKEGeneralNodePool(env map[string]string, token, clusterID string, pools []lkeNodePool) error {
	rawCount := firstNonEmpty(env["LKE_GENERAL_NODE_COUNT"], "0")
	desiredCount, err := strconv.Atoi(rawCount)
	if err != nil || desiredCount <= 0 {
		return nil
	}
	desiredType := firstNonEmpty(env["LKE_GENERAL_NODE_TYPE"], env["LKE_NODE_TYPE"], "g6-standard-2")
	var pool *lkeNodePool
	for i := range pools {
		if pools[i].Labels["rtk.io/node-class"] == "general" {
			pool = &pools[i]
			break
		}
	}
	payloadMap := map[string]any{
		"type": desiredType, "count": desiredCount, "label": "general",
		"labels": map[string]string{"rtk.io/node-class": "general"},
	}
	if pool == nil || pool.Type != desiredType {
		payload, marshalErr := json.Marshal(payloadMap)
		if marshalErr != nil {
			return marshalErr
		}
		out, requestErr := linodeRequestRaw(token, "POST", fmt.Sprintf("/lke/clusters/%s/pools", clusterID), string(payload))
		if requestErr != nil {
			return requestErr
		}
		var created lkeNodePool
		if err := json.Unmarshal(out, &created); err != nil {
			return err
		}
		if created.ID == 0 {
			return errors.New("LKE general node pool create response did not include pool id")
		}
		fmt.Fprintf(os.Stderr, "[lke] created general node pool %d type=%s count=%d\n", created.ID, desiredType, desiredCount)
		return nil
	}
	if pool.Count == desiredCount && pool.Labels["rtk.io/node-class"] == "general" && !lkeNodePoolAutoscalerNeedsReconcile(*pool, desiredCount) {
		return nil
	}
	update := map[string]any{"count": desiredCount, "labels": map[string]string{"rtk.io/node-class": "general"}}
	if lkeNodePoolAutoscalerNeedsReconcile(*pool, desiredCount) {
		update["autoscaler"] = map[string]any{"enabled": false, "min": desiredCount, "max": desiredCount}
	}
	payload, err := json.Marshal(update)
	if err != nil {
		return err
	}
	_, err = linodeRequestRaw(token, "PUT", fmt.Sprintf("/lke/clusters/%s/pools/%d", clusterID, pool.ID), string(payload))
	return err
}

func lkeNodePoolAutoscalerNeedsReconcile(pool lkeNodePool, desiredCount int) bool {
	if desiredCount <= 0 {
		return false
	}
	if pool.Autoscaler.Enabled {
		return true
	}
	if pool.Autoscaler.Min == 0 && pool.Autoscaler.Max == 0 {
		return false
	}
	return pool.Autoscaler.Min != desiredCount || pool.Autoscaler.Max != desiredCount
}

func ensureLKEPostgresNodePool(paths provisionPaths, env map[string]string, token, clusterID string, pools []lkeNodePool) error {
	if !lkePostgresDedicatedNodePoolEnabled(env) {
		return nil
	}
	desiredCount := lkePostgresNodePoolCount(env)
	desiredType := lkePostgresNodePoolType(env)
	desiredIDRaw := firstNonEmpty(os.Getenv("LKE_POSTGRES_NODE_POOL_ID"), env["LKE_POSTGRES_NODE_POOL_ID"])
	var pool *lkeNodePool
	var postgresPool *lkeNodePool
	for i := range pools {
		if desiredIDRaw != "" && strconv.Itoa(pools[i].ID) == desiredIDRaw {
			pool = &pools[i]
			break
		}
		if lkeNodePoolHasPostgresPlacement(pools[i]) && postgresPool == nil {
			postgresPool = &pools[i]
		}
	}
	if pool == nil && postgresPool != nil {
		if desiredIDRaw != "" {
			fmt.Fprintf(os.Stderr, "[lke] postgres node pool id %s not found in cluster %s; using existing postgres pool %d\n", desiredIDRaw, clusterID, postgresPool.ID)
		}
		pool = postgresPool
	}
	payloadMap := lkePostgresNodePoolPayload(env)
	if pool == nil {
		created, err := createLKEPostgresNodePool(token, clusterID, payloadMap)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "[lke] created postgres node pool %d type=%s count=%d\n", created.ID, desiredType, desiredCount)
		lkeSetPostgresNodePoolEnv(env, created.ID, desiredType, desiredCount)
		return lkePersistStackEnvValues(paths.EnvRoot, map[string]string{
			"LKE_POSTGRES_NODE_POOL_ID": strconv.Itoa(created.ID),
			"LKE_POSTGRES_NODE_TYPE":    desiredType,
			"LKE_POSTGRES_NODE_COUNT":   strconv.Itoa(desiredCount),
		})
	}
	if pool.Type != desiredType {
		created, err := createLKEPostgresNodePool(token, clusterID, payloadMap)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "[lke] created replacement postgres node pool %d type=%s count=%d for immutable type change from pool %d type=%s\n", created.ID, desiredType, desiredCount, pool.ID, pool.Type)
		lkeSetPostgresNodePoolEnv(env, created.ID, desiredType, desiredCount)
		return lkePersistStackEnvValues(paths.EnvRoot, map[string]string{
			"LKE_POSTGRES_NODE_POOL_ID": strconv.Itoa(created.ID),
			"LKE_POSTGRES_NODE_TYPE":    desiredType,
			"LKE_POSTGRES_NODE_COUNT":   strconv.Itoa(desiredCount),
		})
	}
	if pool.Count != desiredCount || !lkeNodePoolHasPostgresPlacement(*pool) {
		payload, err := json.Marshal(payloadMap)
		if err != nil {
			return err
		}
		if _, err := linodeRequestRaw(token, "PUT", fmt.Sprintf("/lke/clusters/%s/pools/%d", clusterID, pool.ID), string(payload)); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "[lke] updated postgres node pool %d type=%s count=%d\n", pool.ID, desiredType, desiredCount)
	}
	if err := pruneLKEExtraPostgresNodePools(token, clusterID, pools, pool.ID); err != nil {
		return err
	}
	lkeSetPostgresNodePoolEnv(env, pool.ID, desiredType, desiredCount)
	return lkePersistStackEnvValues(paths.EnvRoot, map[string]string{
		"LKE_POSTGRES_NODE_POOL_ID": strconv.Itoa(pool.ID),
		"LKE_POSTGRES_NODE_TYPE":    desiredType,
		"LKE_POSTGRES_NODE_COUNT":   strconv.Itoa(desiredCount),
	})
}

func pruneLKEExtraPostgresNodePools(token, clusterID string, pools []lkeNodePool, keepID int) error {
	for _, pool := range pools {
		if pool.ID == keepID || !lkeNodePoolHasPostgresPlacement(pool) {
			continue
		}
		if _, err := linodeRequestRaw(token, "DELETE", fmt.Sprintf("/lke/clusters/%s/pools/%d", clusterID, pool.ID), ""); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "[lke] deleted duplicate postgres node pool %d\n", pool.ID)
	}
	return nil
}

func createLKEPostgresNodePool(token, clusterID string, payloadMap map[string]any) (lkeNodePool, error) {
	payload, err := json.Marshal(payloadMap)
	if err != nil {
		return lkeNodePool{}, err
	}
	out, err := linodeRequestRaw(token, "POST", fmt.Sprintf("/lke/clusters/%s/pools", clusterID), string(payload))
	if err != nil {
		return lkeNodePool{}, err
	}
	var created lkeNodePool
	if err := json.Unmarshal(out, &created); err != nil {
		return lkeNodePool{}, err
	}
	if created.ID == 0 {
		return lkeNodePool{}, errors.New("LKE postgres node pool create response did not include pool id")
	}
	return created, nil
}

func lkeSetPostgresNodePoolEnv(env map[string]string, poolID int, nodeType string, count int) {
	env["LKE_POSTGRES_NODE_POOL_ID"] = strconv.Itoa(poolID)
	env["LKE_POSTGRES_NODE_TYPE"] = nodeType
	env["LKE_POSTGRES_NODE_COUNT"] = strconv.Itoa(count)
}

func lkePostgresDedicatedNodePoolEnabled(env map[string]string) bool {
	if firstNonEmpty(os.Getenv("LKE_POSTGRES_NODE_POOL_ID"), env["LKE_POSTGRES_NODE_POOL_ID"]) != "" {
		return true
	}
	raw := strings.TrimSpace(firstNonEmpty(os.Getenv("LKE_POSTGRES_DEDICATED_NODE_POOL"), env["LKE_POSTGRES_DEDICATED_NODE_POOL"]))
	if raw != "" {
		return strings.EqualFold(raw, "true") || raw == "1" || strings.EqualFold(raw, "yes")
	}
	target, err := strconv.Atoi(firstNonEmpty(os.Getenv("LKE_TARGET_CONNECTS"), env["LKE_TARGET_CONNECTS"], "0"))
	return err == nil && target >= 100000
}

func lkePostgresNodePoolType(env map[string]string) string {
	return firstNonEmpty(os.Getenv("LKE_POSTGRES_NODE_TYPE"), env["LKE_POSTGRES_NODE_TYPE"], "g6-standard-8")
}

func lkePostgresNodePoolCount(env map[string]string) int {
	raw := strings.TrimSpace(firstNonEmpty(os.Getenv("LKE_POSTGRES_NODE_COUNT"), env["LKE_POSTGRES_NODE_COUNT"], "1"))
	count, err := strconv.Atoi(raw)
	if err != nil || count <= 0 {
		return 1
	}
	return count
}

func lkePostgresNodePoolPayload(env map[string]string) map[string]any {
	return map[string]any{
		"type":  lkePostgresNodePoolType(env),
		"count": lkePostgresNodePoolCount(env),
		"label": "postgres",
		"labels": map[string]string{
			"rtk.io/node-class": "database",
		},
		"taints": []map[string]string{
			{
				"key":    "rtk.io/node-class",
				"value":  "database",
				"effect": "NoSchedule",
			},
		},
	}
}

func lkeNodePoolHasPostgresPlacement(pool lkeNodePool) bool {
	if pool.Label != "postgres" {
		return false
	}
	if pool.Labels["rtk.io/node-class"] != "database" {
		return false
	}
	for _, taint := range pool.Taints {
		if taint.Key == "rtk.io/node-class" && taint.Value == "database" && taint.Effect == "NoSchedule" {
			return true
		}
	}
	return false
}

func lkePersistStackEnvValues(envRoot string, updates map[string]string) error {
	statePath := filepath.Join(envRoot, "adapters", "lke", "state.env")
	raw, err := readEnvFile(statePath)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if raw == nil {
		raw = map[string]string{}
	}
	for key, value := range updates {
		raw[key] = value
	}
	if err := os.MkdirAll(filepath.Dir(statePath), 0o700); err != nil {
		return err
	}
	return writeSortedEnv(statePath, raw, 0o600)
}

func listLKENodePools(token string, clusterID string) ([]lkeNodePool, error) {
	out, err := linodeRequestRaw(token, "GET", "/lke/clusters/"+clusterID+"/pools", "")
	if err != nil {
		return nil, err
	}
	var listed struct {
		Data []lkeNodePool `json:"data"`
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		return nil, err
	}
	return listed.Data, nil
}

func recoverStaleLKECluster(token string, paths provisionPaths, env map[string]string) (lkeCluster, error) {
	cluster, err := discoverLKECluster(token, paths, env, true)
	if err != nil {
		return lkeCluster{}, err
	}
	if err := writeLKEState(paths, cluster); err != nil {
		return lkeCluster{}, err
	}
	kubeconfig, err := fetchLKEKubeconfig(token, strconv.Itoa(cluster.ID))
	if err != nil {
		return lkeCluster{}, err
	}
	stateKubeconfig := filepath.Join(paths.EnvRoot, "state", "kubeconfig.yaml")
	if err := os.MkdirAll(filepath.Dir(stateKubeconfig), 0o755); err != nil {
		return lkeCluster{}, err
	}
	if err := os.WriteFile(stateKubeconfig, kubeconfig, 0o600); err != nil {
		return lkeCluster{}, err
	}
	_ = os.Setenv("RTK_CLOUD_LKE_KUBECONFIG", stateKubeconfig)
	fmt.Fprintf(os.Stderr, "[lke] wrote kubeconfig: %s\n", stateKubeconfig)
	return cluster, nil
}

func lkeNodeCount(env map[string]string) (int, error) {
	raw := strings.TrimSpace(firstNonEmpty(os.Getenv("LKE_NODE_COUNT"), env["LKE_NODE_COUNT"], "5"))
	if strings.EqualFold(raw, "auto") {
		nodeCount, err := lkeRecommendedNodeCount(env)
		if err != nil {
			return 0, err
		}
		if nodeCount <= 0 {
			return 0, errors.New("LKE_NODE_COUNT auto produced no nodes")
		}
		return nodeCount, nil
	}
	nodeCount, err := strconv.Atoi(raw)
	if err != nil || nodeCount <= 0 {
		return 0, errors.New("LKE_NODE_COUNT must be a positive integer")
	}
	return nodeCount, nil
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
	statePath := filepath.Join(paths.EnvRoot, "adapters", "lke", "state.env")
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

func isLinodeNotFoundError(err error) bool {
	return err != nil && strings.Contains(err.Error(), "returned error: 404")
}

func linodeRequestRaw(token, method, path, data string) ([]byte, error) {
	args := []string{"--fail-with-body", "-sS", "-X", method, "https://api.linode.com/v4" + path, "-H", "Authorization: Bearer " + token, "-H", "Content-Type: application/json"}
	if data != "" {
		args = append(args, "-d", data)
	}
	cmd := exec.Command("curl", args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		errText := strings.TrimSpace(stderr.String())
		bodyText := strings.TrimSpace(string(out))
		if errText != "" {
			if bodyText != "" {
				return nil, fmt.Errorf("Linode API %s %s failed: %w: %s: %s", method, path, err, errText, bodyText)
			}
			return nil, fmt.Errorf("Linode API %s %s failed: %w: %s", method, path, err, errText)
		}
		if bodyText != "" {
			return nil, fmt.Errorf("Linode API %s %s failed: %w: %s", method, path, err, bodyText)
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
