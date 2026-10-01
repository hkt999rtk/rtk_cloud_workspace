package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
)

// ota-core-cutover updates only the selected core API after the independent OTA
// service and authenticated device edge have been qualified.
func runDeploymentOTACoreCutover(args []string) error {
	return runDeploymentOTACoreCutoverWithOps(args, otaCoreCutoverOps{
		credentials: func(environment string) (func(), error) {
			_, restore, err := configureProvisionSecretStore(environment)
			return restore, err
		},
		readOnlyCredentials: func(environment string) (func(), error) {
			_, restore, err := configureReadOnlySecretStore(environment)
			return restore, err
		},
		stoppedRegistrar: lkeRequireStoppedOTARegistrar,
		readyService:     lkeRequireReadyOTAServiceEndpoint,
		activeEdge:       lkeRequireActiveOTADeviceEdgeRoute,
		get:              kubectlResourceJSON,
		pods:             otaCoreCutoverPods,
		patch: func(namespace, patch string) error {
			return runKubectl("-n", namespace, "patch", "deployment", "video-cloud-api", "--type=json", "-p", patch)
		},
		rollout: func(namespace string) error {
			return runKubectl("-n", namespace, "rollout", "status", "deployment/video-cloud-api", "--timeout", "5m")
		},
		persist: func(store secretStore) error {
			return store.write("operator/env/LKE_OTA_CORE_CUTOVER_ENABLED", []byte("true"), true)
		},
	})
}

type otaCoreCutoverOps struct {
	credentials         func(string) (func(), error)
	readOnlyCredentials func(string) (func(), error)
	stoppedRegistrar    func(map[string]string) error
	readyService        func(map[string]string) error
	activeEdge          func(map[string]string) error
	get                 func(string, string, string) (map[string]any, error)
	pods                func(string) ([]map[string]any, error)
	patch               func(string, string) error
	rollout             func(string) error
	persist             func(secretStore) error
}

func runDeploymentOTACoreCutoverWithOps(args []string, ops otaCoreCutoverOps) error {
	fs := flag.NewFlagSet("deployment ota-core-cutover", flag.ContinueOnError)
	environment := fs.String("environment", "", "selected environment")
	workspace := fs.String("workspace", "", "workspace root")
	confirm := fs.String("confirm", "", "selected stack name for mutation")
	readOnly := fs.Bool("read-only", false, "verify the live core cutover without changes")
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
	env, err := selectedOTAEnvironment(cfg)
	if err != nil {
		return err
	}
	if env["CLOUD_STACK_NAME"] != cfg.Values["CLOUD_STACK_NAME"] || env["CLOUD_ENV_NAME"] != cfg.Environment {
		return errors.New("resolved OTA core runtime does not match the selected environment")
	}
	operatorValues, err := store.readOperator()
	if err != nil {
		return err
	}
	env = appendMap(env, operatorValues)
	if env["CLOUD_STACK_NAME"] != cfg.Values["CLOUD_STACK_NAME"] || env["CLOUD_ENV_NAME"] != cfg.Environment {
		return errors.New("operator OTA core settings do not match the selected environment")
	}
	if err := loadLKEImageManifestDefaults(store.Root, env); err != nil {
		return err
	}
	image := strings.TrimSpace(operatorValues["LKE_VIDEO_CLOUD_IMAGE"])
	if *confirm == "" && !*readOnly {
		fmt.Fprintf(os.Stdout, "OTA core cutover plan: environment=%s stack=%s core=video-cloud-api image=%s\n", cfg.Environment, env["CLOUD_STACK_NAME"], image)
		fmt.Fprintln(os.Stdout, "Apply requires the qualified device mTLS edge, ready independent OTA service, strict Product entitlements, and --confirm STACK.")
		return nil
	}
	if (*readOnly && *confirm != "") || (!*readOnly && *confirm != env["CLOUD_STACK_NAME"]) {
		return errors.New("--confirm must match the selected stack and cannot accompany --read-only")
	}
	if operatorValues["LKE_OTA_SERVICE_REGISTRATION_ENABLED"] != "true" ||
		operatorValues["LKE_OTA_SERVICE_EDGE_ENABLED"] != "true" ||
		operatorValues["LKE_OTA_REGISTRAR_REGISTRATION_ENABLED"] != "false" ||
		operatorValues["VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED"] != "true" {
		return errors.New("OTA core cutover requires independent service and edge enabled, old registrar off, and strict Product entitlements")
	}
	if flag := operatorValues["LKE_OTA_CORE_CUTOVER_ENABLED"]; flag != "false" && flag != "true" {
		return errors.New("selected operator environment must explicitly record the current OTA core cutover flag")
	}
	if !rolloutImagePattern.MatchString(image) {
		return errors.New("OTA core cutover requires a pinned immutable Video Cloud image")
	}
	credentials := ops.credentials
	if *readOnly && ops.readOnlyCredentials != nil {
		credentials = ops.readOnlyCredentials
	}
	restore, err := credentials(cfg.Environment)
	if err != nil {
		return err
	}
	defer restore()
	if err := ops.stoppedRegistrar(env); err != nil {
		return err
	}
	if err := ops.readyService(env); err != nil {
		return err
	}
	if err := ops.activeEdge(env); err != nil {
		return err
	}
	namespace := lkeNamespaceName(env, "video-cloud")
	deployment, err := ops.get(namespace, "deployment", "video-cloud-api")
	if err != nil {
		return err
	}
	upstream := "http://" + otaServiceWorkloadName + "." + namespace + ".svc.cluster.local:18084"
	patch, ready, err := otaCoreCutoverPatch(deployment, image, upstream)
	if err != nil {
		return err
	}
	if *readOnly {
		if !ready || operatorValues["LKE_OTA_CORE_CUTOVER_ENABLED"] != "true" {
			return errors.New("selected core API and operator configuration have not both cut over to the pinned independent OTA service")
		}
		pods, err := ops.pods(namespace)
		if err != nil {
			return err
		}
		if err := otaCoreCutoverCurrentReady(deployment, pods, image, upstream); err != nil {
			return err
		}
		fmt.Fprintf(os.Stdout, "PASS %s core OTA cutover uses the pinned image and independent service\n", cfg.Environment)
		return nil
	}
	if !ready {
		if err := ops.patch(namespace, patch); err != nil {
			return fmt.Errorf("apply OTA core cutover: %w", err)
		}
	}
	if err := ops.rollout(namespace); err != nil {
		return err
	}
	deployment, err = ops.get(namespace, "deployment", "video-cloud-api")
	if err != nil {
		return err
	}
	if _, ready, err := otaCoreCutoverPatch(deployment, image, upstream); err != nil || !ready {
		return fmt.Errorf("OTA core cutover read-back is incomplete: %v", err)
	}
	pods, err := ops.pods(namespace)
	if err != nil {
		return err
	}
	if err := otaCoreCutoverCurrentReady(deployment, pods, image, upstream); err != nil {
		return err
	}
	if err := ops.readyService(env); err != nil {
		return err
	}
	if err := ops.activeEdge(env); err != nil {
		return err
	}
	if operatorValues["LKE_OTA_CORE_CUTOVER_ENABLED"] != "true" {
		if err := ops.persist(store); err != nil {
			return fmt.Errorf("persist OTA core cutover after live read-back: %w", err)
		}
	}
	return nil
}

// otaCoreCutoverPatch preserves every unrelated container and setting. A
// resourceVersion test makes concurrent edits fail rather than replacing them.
func otaCoreCutoverPatch(deployment map[string]any, image, upstream string) (string, bool, error) {
	meta, _ := deployment["metadata"].(map[string]any)
	version, _ := meta["resourceVersion"].(string)
	if meta["name"] != "video-cloud-api" || version == "" {
		return "", false, errors.New("core API Deployment identity or resourceVersion changed")
	}
	spec, _ := deployment["spec"].(map[string]any)
	template, _ := spec["template"].(map[string]any)
	pod, _ := template["spec"].(map[string]any)
	containers, _ := pod["containers"].([]any)
	appIndex := -1
	for i, raw := range containers {
		container, _ := raw.(map[string]any)
		if container["name"] == "app" {
			if appIndex >= 0 {
				return "", false, errors.New("core API has duplicate app containers")
			}
			appIndex = i
		}
	}
	if appIndex < 0 {
		return "", false, errors.New("core API app container is missing")
	}
	app, _ := containers[appIndex].(map[string]any)
	currentImage, _ := app["image"].(string)
	if !strings.Contains(currentImage, "@sha256:") || !strings.Contains(image, "@sha256:") {
		return "", false, errors.New("core API image must be immutable")
	}
	env, _ := app["env"].([]any)
	if len(env) == 0 {
		return "", false, errors.New("core API environment is missing")
	}
	indices := map[string]int{}
	values := map[string]string{}
	for i, raw := range env {
		entry, _ := raw.(map[string]any)
		name, _ := entry["name"].(string)
		if name != "VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED" && name != "VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED" && name != "VIDEO_CLOUD_OTA_UPSTREAM_URL" {
			continue
		}
		if _, exists := indices[name]; exists {
			return "", false, fmt.Errorf("core API has duplicate %s", name)
		}
		value, ok := entry["value"].(string)
		if !ok {
			return "", false, fmt.Errorf("core API %s is not a literal value", name)
		}
		indices[name], values[name] = i, value
	}
	if values["VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED"] != "true" {
		return "", false, errors.New("core API must enforce Product OTA entitlements")
	}
	cutover := values["VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED"]
	if cutover != "" && cutover != "false" && cutover != "true" {
		return "", false, errors.New("core API has an unexpected OTA cutover state")
	}
	currentUpstream := values["VIDEO_CLOUD_OTA_UPSTREAM_URL"]
	if currentUpstream != "" && currentUpstream != upstream {
		return "", false, errors.New("core API OTA upstream differs from the selected service")
	}
	if cutover == "true" && currentUpstream != upstream {
		return "", false, errors.New("core API cutover lacks the selected OTA upstream")
	}
	if currentImage == image && cutover == "true" && currentUpstream == upstream {
		return "", true, nil
	}
	base := fmt.Sprintf("/spec/template/spec/containers/%d", appIndex)
	operations := []map[string]any{
		{"op": "test", "path": "/metadata/resourceVersion", "value": version},
		{"op": "test", "path": base + "/name", "value": "app"},
		{"op": "test", "path": base + "/image", "value": currentImage},
	}
	if currentImage != image {
		operations = append(operations, map[string]any{"op": "replace", "path": base + "/image", "value": image})
	}
	if index, exists := indices["VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED"]; exists {
		operations = append(operations, map[string]any{"op": "replace", "path": fmt.Sprintf("%s/env/%d/value", base, index), "value": "true"})
	} else {
		operations = append(operations, map[string]any{"op": "add", "path": base + "/env/-", "value": map[string]string{"name": "VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED", "value": "true"}})
	}
	if _, exists := indices["VIDEO_CLOUD_OTA_UPSTREAM_URL"]; !exists {
		operations = append(operations, map[string]any{"op": "add", "path": base + "/env/-", "value": map[string]string{"name": "VIDEO_CLOUD_OTA_UPSTREAM_URL", "value": upstream}})
	}
	raw, err := json.Marshal(operations)
	return string(raw), false, err
}
