package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"regexp"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

var cloudAdminCommitImagePattern = regexp.MustCompile(
	`^ghcr\.io/hkt999rtk/rtk_cloud_admin/cloud-admin:sha-[0-9a-f]{12,40}$`,
)
var cloudAdminCurrentImagePattern = regexp.MustCompile(
	`^ghcr\.io/hkt999rtk/rtk_cloud_admin/cloud-admin(?::sha-[0-9a-f]{12,40}|@sha256:[0-9a-f]{64})$`,
)

// runCloudAdminImageDeploy updates only the existing Cloud Admin deployment.
// It does not reconcile shared infrastructure, secrets, DNS, or other workloads.
func runCloudAdminImageDeploy(args []string) error {
	fs := flag.NewFlagSet("cloud-admin-image-deploy", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace root")
	envRootFlag := fs.String("env-root", "", "normalized environment root")
	confirm := fs.String("confirm", "", "exact stack name confirmation")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig for the existing staging cluster")
	expectedOldImage := fs.String("expected-old-image", "", "exact currently deployed Cloud Admin sha tag or digest image")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*envRootFlag) == "" {
		return errors.New("--env-root is required")
	}
	if !cloudAdminCurrentImagePattern.MatchString(strings.TrimSpace(*expectedOldImage)) {
		return errors.New("--expected-old-image must be the exact current Cloud Admin sha or digest image")
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
	store, err := newSecretStore("", env["CLOUD_ENV_NAME"])
	if err != nil {
		return err
	}
	operator, err := store.readOperator()
	if err != nil {
		return fmt.Errorf("read selected Cloud Admin operator configuration: %w", err)
	}
	image := strings.TrimSpace(operator["LKE_CLOUD_ADMIN_IMAGE"])
	if !cloudAdminCommitImagePattern.MatchString(image) {
		return errors.New("selected operator LKE_CLOUD_ADMIN_IMAGE must be the exact Cloud Admin sha image")
	}
	if processImage := strings.TrimSpace(os.Getenv("LKE_CLOUD_ADMIN_IMAGE")); processImage != "" && processImage != image {
		return errors.New("process LKE_CLOUD_ADMIN_IMAGE differs from the selected operator pin")
	}
	if strings.TrimSpace(*kubeconfig) != "" {
		if err := os.Setenv("RTK_CLOUD_KUBECONFIG", strings.TrimSpace(*kubeconfig)); err != nil {
			return err
		}
	}
	if err := waitForKubernetesAPIReady(); err != nil {
		return err
	}

	namespace := lkeNamespaceName(env, "admin")
	if _, err := kubectlCombinedOutput(nil, "-n", namespace, "get", "deployment/cloud-admin", "-o", "name"); err != nil {
		return fmt.Errorf("existing Cloud Admin deployment is unavailable: %w", err)
	}
	deployment, err := kubectlResourceJSON(namespace, "deployment", "cloud-admin")
	if err != nil {
		return err
	}
	already, err := updateCloudAdminDeploymentImage(deployment, strings.TrimSpace(*expectedOldImage), image)
	if err != nil {
		return err
	}
	if !already {
		if err := kubectlReplaceJSON(deployment); err != nil {
			return fmt.Errorf("replace Cloud Admin deployment: %w", err)
		}
	}
	return runKubectl(
		"-n", namespace, "rollout", "status", "deployment/cloud-admin",
		"--timeout", firstNonEmpty(os.Getenv("LKE_WORKLOAD_ROLLOUT_TIMEOUT"), "10m"),
	)
}

func updateCloudAdminDeploymentImage(deployment map[string]any, expectedOldImage, image string) (bool, error) {
	metadata, ok := deployment["metadata"].(map[string]any)
	if !ok {
		return false, errors.New("Cloud Admin deployment has no resourceVersion")
	}
	resourceVersion, ok := metadata["resourceVersion"].(string)
	if !ok || strings.TrimSpace(resourceVersion) == "" {
		return false, errors.New("Cloud Admin deployment has no resourceVersion")
	}
	spec, ok := deployment["spec"].(map[string]any)
	if !ok {
		return false, errors.New("deployment has no spec")
	}
	if replicas, ok := spec["replicas"].(float64); !ok || replicas != 1 {
		return false, errors.New("Cloud Admin deployment must have exactly one replica")
	}
	template, ok := spec["template"].(map[string]any)
	if !ok {
		return false, errors.New("deployment has no pod template")
	}
	podSpec, ok := template["spec"].(map[string]any)
	if !ok {
		return false, errors.New("deployment has no pod spec")
	}
	containers, ok := podSpec["containers"].([]any)
	if !ok {
		return false, errors.New("deployment has no containers")
	}
	for _, item := range containers {
		container, _ := item.(map[string]any)
		if container["name"] == "app" {
			if container["image"] != expectedOldImage {
				return false, errors.New("Cloud Admin deployment image differs from --expected-old-image")
			}
			if image == expectedOldImage {
				return true, nil
			}
			container["image"] = image
			return false, nil
		}
	}
	return false, errors.New("Cloud Admin deployment has no app container")
}
