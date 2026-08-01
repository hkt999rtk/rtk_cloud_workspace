package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
)

type deploymentConfig struct {
	Workspace       string
	Environment     string
	EnvironmentRoot string
	RuntimeRoot     string
	Architecture    string
	Adapter         string
	DNSAdapter      string
	Values          map[string]string
	AdapterValues   map[string]string
	AdapterResolved map[string]string
	DNSValues       map[string]string
	Capacity        sharedCapacityPlan
}

type deploymentOperations struct {
	plan        func(deploymentConfig) error
	prepareTest func(deploymentConfig) error
	provision   func(deploymentConfig) error
	acceptance  func(deploymentConfig) error
	cleanup     func(deploymentConfig) error
	normalize   func(deploymentConfig) error
}

var deploymentIntegerKeys = map[string]bool{
	"CAPACITY_TARGET_CONNECTIONS": true, "CAPACITY_CONNECTIONS_PER_MQTT_POD": true,
	"CAPACITY_ACTIVE_DEVICES": true, "CAPACITY_ACTIVE_DEVICES_PER_API_POD": true,
	"CAPACITY_SYSTEM_RESERVED_CPU_MILLI": true, "CAPACITY_SYSTEM_RESERVED_MEMORY_MIB": true,
	"NODE_CLASS_GENERAL_MIN_COUNT": true, "NODE_CLASS_BROKER_MIN_COUNT": true,
	"NODE_CLASS_DATABASE_MIN_COUNT": true,
	"NODE_CLASS_GENERAL_MIN_VCPU":   true, "NODE_CLASS_GENERAL_MIN_MEMORY_GIB": true,
	"NODE_CLASS_BROKER_MIN_VCPU": true, "NODE_CLASS_BROKER_MIN_MEMORY_GIB": true,
	"NODE_CLASS_DATABASE_MIN_VCPU": true, "NODE_CLASS_DATABASE_MIN_MEMORY_GIB": true,
	"EDGE_REPLICAS":        true,
	"EDGE_MAX_CONNECTIONS": true, "TURN_REPLICAS": true,
	"TURN_MIN_PORT": true, "TURN_MAX_PORT": true,
}

var deploymentArchitectureKeys = architectureKeySet()

func architectureKeySet() map[string]bool {
	out := keySet(
		"DEPLOYMENT_RUNTIME", "NODE_CLASS_LABEL_KEY", "DEFAULT_WORKLOAD_NODE_CLASS", "POD_SPREAD_TOPOLOGY_KEY",
		"CAPACITY_TARGET_CONNECTIONS", "CAPACITY_CONNECTIONS_PER_MQTT_POD",
		"CAPACITY_ACTIVE_DEVICES", "CAPACITY_ACTIVE_DEVICES_PER_API_POD", "CAPACITY_SYSTEM_RESERVED_CPU_MILLI", "CAPACITY_SYSTEM_RESERVED_MEMORY_MIB",
		"NODE_CLASS_GENERAL_MIN_COUNT", "NODE_CLASS_BROKER_MIN_COUNT", "NODE_CLASS_DATABASE_MIN_COUNT",
		"NODE_CLASS_GENERAL_MIN_VCPU", "NODE_CLASS_GENERAL_MIN_MEMORY_GIB",
		"NODE_CLASS_BROKER_MIN_VCPU", "NODE_CLASS_BROKER_MIN_MEMORY_GIB",
		"NODE_CLASS_DATABASE_MIN_VCPU", "NODE_CLASS_DATABASE_MIN_MEMORY_GIB",
		"MQTT_HARD_ANTI_AFFINITY", "POSTGRES_LIMIT_MEMORY", "CLOUD_LOGGER_LIMIT_MEMORY",
		"VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED",
		"EDGE_REPLICAS", "EDGE_MAX_CONNECTIONS", "TURN_REPLICAS", "TURN_MIN_PORT", "TURN_MAX_PORT",
	)
	for _, key := range capacitySourceKeys() {
		out[key] = true
	}
	return out
}

func keySet(keys ...string) map[string]bool {
	out := make(map[string]bool, len(keys))
	for _, key := range keys {
		out[key] = true
	}
	return out
}

func runDeployment(args []string) error {
	return runDeploymentWithOperations(args, defaultDeploymentOperations())
}

func defaultDeploymentOperations() deploymentOperations {
	return deploymentOperations{
		plan: func(cfg deploymentConfig) error {
			return runProvision([]string{"--workspace", cfg.Workspace, "--env-root", cfg.RuntimeRoot, "--plan"})
		},
		prepareTest: validateEphemeralDeploymentEnvironmentAbsent,
		provision:   provisionDeploymentEnvironment,
		acceptance: func(cfg deploymentConfig) error {
			return runEnvironmentAcceptance([]string{"--workspace", cfg.Workspace, "--env-root", cfg.RuntimeRoot, "--confirm", cfg.Values["CLOUD_STACK_NAME"], "--no-resume"})
		},
		cleanup:   cleanupDeploymentEnvironment,
		normalize: normalizeDeploymentRuntime,
	}
}

func runDeploymentWithOperations(args []string, ops deploymentOperations) error {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printDeploymentUsage()
		return nil
	}
	action := args[0]
	fs := flag.NewFlagSet("deployment "+action, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	environment := fs.String("environment", "", "environment name under cloud_env")
	environmentRoot := fs.String("environment-root", "", "explicit environment root for tests/custom environments")
	workspace := fs.String("workspace", "", "workspace root")
	confirm := fs.String("confirm", "", "stack confirmation for mutation")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if action != "plan" && action != "provision" && action != "acceptance" && action != "remove" && action != "test" {
		return fmt.Errorf("unknown deployment action %q", action)
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, *environmentRoot)
	if err != nil {
		return err
	}
	if err := materializeDeploymentRuntime(cfg); err != nil {
		return err
	}
	stack := cfg.Values["CLOUD_STACK_NAME"]
	if action != "plan" && *confirm != stack {
		return fmt.Errorf("--confirm %s is required", stack)
	}
	if cfg.Adapter != "lke" && action != "plan" {
		return fmt.Errorf("deployment adapter %s is not implemented", cfg.Adapter)
	}
	switch action {
	case "plan":
		fmt.Printf("environment: %s\narchitecture: %s\nadapter: %s\ndns_adapter: %s\nruntime_root: %s\n", cfg.Environment, cfg.Architecture, cfg.Adapter, cfg.DNSAdapter, cfg.RuntimeRoot)
		if cfg.Adapter != "lke" {
			fmt.Printf("infrastructure: adapter not implemented; mutation will fail fast\n")
			return normalizeDeploymentRuntime(cfg)
		}
		if err := ops.plan(cfg); err != nil {
			return err
		}
		return ops.normalize(cfg)
	case "provision":
		err = ops.provision(cfg)
	case "acceptance":
		err = ops.acceptance(cfg)
	case "remove":
		err = ops.cleanup(cfg)
	case "test":
		if err = ops.prepareTest(cfg); err != nil {
			return fmt.Errorf("ephemeral test preflight failed: %w", err)
		}
		provisionErr := ops.provision(cfg)
		var acceptanceErr error
		if provisionErr == nil {
			acceptanceErr = ops.acceptance(cfg)
		}
		cleanupErr := ops.cleanup(cfg)
		err = errors.Join(
			wrapDeploymentPhaseError("provision", provisionErr),
			wrapDeploymentPhaseError("acceptance", acceptanceErr),
			wrapDeploymentPhaseError("cleanup", cleanupErr),
		)
	}
	if err != nil {
		return err
	}
	return ops.normalize(cfg)
}

func wrapDeploymentPhaseError(phase string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s phase failed: %w", phase, err)
}

func provisionDeploymentEnvironment(cfg deploymentConfig) error {
	if err := validateDNSBeforeMutation(cfg); err != nil {
		return err
	}
	if cfg.Adapter == "lke" {
		if err := validateLKEEnvironmentStateBeforeMutation(cfg); err != nil {
			return err
		}
		if err := resolveLKEImagesIfNeeded(cfg.Workspace, cfg.RuntimeRoot); err != nil {
			return err
		}
	}
	return runProvision([]string{"--workspace", cfg.Workspace, "--env-root", cfg.RuntimeRoot, "--preflight", "--plan", "--apply", "--deploy", "--dns", "--artifacts", "--confirm", cfg.Values["CLOUD_STACK_NAME"]})
}

func cleanupDeploymentEnvironment(cfg deploymentConfig) error {
	stack := cfg.Values["CLOUD_STACK_NAME"]
	volumeIDs, volumeCaptureErr := deploymentPersistentVolumeIDs(cfg)
	k8sErr := purgeDeploymentK8s(cfg)
	dnsErr := removeOwnedDNSRecords(
		newProvisionPaths(cfg.Workspace, cfg.RuntimeRoot, provisionOptions{}),
		appendMap(cfg.Values, cfg.DNSValues),
	)
	resourceErr := runDestroyEnvironmentResources([]string{
		"--workspace", cfg.Workspace,
		"--env-root", cfg.RuntimeRoot,
		"--yes",
		"--confirm-text", "destroy " + stack,
		"--include-object-storage",
	})
	if resourceErr == nil {
		resourceErr = deleteDeploymentVolumesWhenDetached(resolveLinodeToken(cfg.RuntimeRoot), volumeIDs, envDurationDefault("RTK_CLOUD_ENVIRONMENT_CLEANUP_TIMEOUT", 10*time.Minute))
	}
	if resourceErr == nil {
		resourceErr = waitForDeploymentEnvironmentRemoval(cfg)
	}
	return errors.Join(
		wrapDeploymentPhaseError("persistent volume ownership capture", volumeCaptureErr),
		wrapDeploymentPhaseError("Kubernetes storage cleanup", k8sErr),
		wrapDeploymentPhaseError("DNS cleanup", dnsErr),
		wrapDeploymentPhaseError("provider cleanup", resourceErr),
	)
}

func deploymentPersistentVolumeIDs(cfg deploymentConfig) ([]string, error) {
	kubeconfig := filepath.Join(cfg.RuntimeRoot, "state", "kubeconfig.yaml")
	if _, err := os.Stat(kubeconfig); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	cmd := exec.Command("kubectl", "--kubeconfig", kubeconfig, "get", "persistentvolume", "-o", "json")
	body, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok && len(exitErr.Stderr) > 0 {
			return nil, fmt.Errorf("list persistent volumes: %s", strings.TrimSpace(string(exitErr.Stderr)))
		}
		return nil, fmt.Errorf("list persistent volumes: %w", err)
	}
	return persistentVolumeIDsForStack(body, cfg.Values["CLOUD_STACK_NAME"])
}

func persistentVolumeIDsForStack(body []byte, stack string) ([]string, error) {
	var listed struct {
		Items []struct {
			Spec struct {
				ClaimRef struct {
					Namespace string `json:"namespace"`
				} `json:"claimRef"`
				CSI struct {
					VolumeHandle string `json:"volumeHandle"`
				} `json:"csi"`
			} `json:"spec"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &listed); err != nil {
		return nil, fmt.Errorf("decode persistent volumes: %w", err)
	}
	seen := map[string]bool{}
	ids := []string{}
	for _, item := range listed.Items {
		if !strings.HasPrefix(item.Spec.ClaimRef.Namespace, stack+"-") {
			continue
		}
		id := strings.TrimSpace(item.Spec.CSI.VolumeHandle)
		if id == "" {
			continue
		}
		volumeID := id
		if numericID, suffix, ok := strings.Cut(id, "-"); ok && strings.HasPrefix(suffix, "pvc-") {
			volumeID = numericID
		}
		if _, err := strconv.Atoi(volumeID); err != nil {
			return nil, fmt.Errorf("stack %s persistent volume has unsupported Linode volume handle %q", stack, id)
		}
		if !seen[volumeID] {
			seen[volumeID] = true
			ids = append(ids, volumeID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func purgeDeploymentK8s(cfg deploymentConfig) error {
	kubeconfig := filepath.Join(cfg.RuntimeRoot, "state", "kubeconfig.yaml")
	if _, err := os.Stat(kubeconfig); os.IsNotExist(err) {
		return nil
	} else if err != nil {
		return err
	}
	old, hadOld := os.LookupEnv("CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET")
	if err := os.Setenv("CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET", "1"); err != nil {
		return err
	}
	defer func() {
		if hadOld {
			_ = os.Setenv("CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET", old)
		} else {
			_ = os.Unsetenv("CLOUD_STAGING_E2E_K8S_DESTRUCTIVE_RESET")
		}
	}()
	return runRemoveK8s([]string{
		"--workspace", cfg.Workspace,
		"--env-root", cfg.RuntimeRoot,
		"--yes",
		"--purge-storage",
	})
}

func validateEphemeralDeploymentEnvironmentAbsent(cfg deploymentConfig) error {
	if cfg.Adapter != "lke" {
		return fmt.Errorf("ephemeral test preflight is not implemented for adapter %s", cfg.Adapter)
	}
	token := resolveLinodeToken(cfg.RuntimeRoot)
	if token == "" {
		return errors.New("LKE credentials reference is required before ephemeral test preflight")
	}
	plan, err := buildLinodeDestroyPlan(token, cfg.Values, cfg.Values["CLOUD_STACK_NAME"], "")
	if err != nil {
		return err
	}
	if labels := deploymentEnvironmentResourceLabels(plan); len(labels) > 0 {
		return fmt.Errorf("stack %s already owns provider resources: %s; use acceptance for an existing environment or remove it explicitly", cfg.Values["CLOUD_STACK_NAME"], strings.Join(labels, ", "))
	}
	ownership := filepath.Join(cfg.RuntimeRoot, "dns", cfg.DNSAdapter, "ownership.json")
	if body, readErr := os.ReadFile(ownership); readErr == nil && len(strings.TrimSpace(string(body))) > 0 {
		return fmt.Errorf("stack %s has existing DNS ownership state at %s", cfg.Values["CLOUD_STACK_NAME"], ownership)
	} else if readErr != nil && !os.IsNotExist(readErr) {
		return readErr
	}
	return nil
}

func waitForDeploymentEnvironmentRemoval(cfg deploymentConfig) error {
	if cfg.Adapter != "lke" {
		return nil
	}
	token := resolveLinodeToken(cfg.RuntimeRoot)
	if token == "" {
		return errors.New("LKE credentials reference is required to verify provider cleanup")
	}
	stack := cfg.Values["CLOUD_STACK_NAME"]
	timeout := envDurationDefault("RTK_CLOUD_ENVIRONMENT_CLEANUP_TIMEOUT", 10*time.Minute)
	deadline := time.Now().Add(timeout)
	for {
		plan, err := buildLinodeDestroyPlan(token, cfg.Values, stack, "")
		if err != nil {
			return err
		}
		labels := deploymentEnvironmentResourceLabels(plan)
		if len(labels) == 0 {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out after %s waiting for stack %s cleanup; resources still present: %s", timeout, stack, strings.Join(labels, ", "))
		}
		time.Sleep(5 * time.Second)
	}
}

func deploymentEnvironmentResourceLabels(plan linodeDestroyPlan) []string {
	labels := []string{}
	for _, group := range [][]linodeDestroyResource{plan.LKEClusters, plan.Instances, plan.Firewalls, plan.VPCs, plan.ObjectBuckets} {
		for _, resource := range group {
			labels = append(labels, resource.Label)
		}
	}
	sort.Strings(labels)
	return labels
}

func validateDNSBeforeMutation(cfg deploymentConfig) error {
	env := appendMap(cfg.Values, cfg.DNSValues)
	paths := newProvisionPaths(cfg.Workspace, cfg.RuntimeRoot, provisionOptions{})
	_, _, _, err := selectedDNSAdapter(paths, env)
	return err
}

func validateLKEEnvironmentStateBeforeMutation(cfg deploymentConfig) error {
	account, err := readLKEAccountState(cfg.RuntimeRoot, true)
	if err != nil {
		return err
	}
	if _, err := positiveIntValue("LKE_ACTIVE_SERVICE_LIMIT", account["LKE_ACTIVE_SERVICE_LIMIT"]); err != nil {
		return err
	}
	compatInput := appendMap(cfg.Values, cfg.AdapterResolved)
	compat := appendMap(compatInput, deploymentLegacyLKEValues(compatInput, cfg.Environment))
	compat["LKE_LINODE_ACTIVE_SERVICE_LIMIT"] = account["LKE_ACTIVE_SERVICE_LIMIT"]
	for _, key := range []string{"LKE_REGION", "LKE_GENERAL_NODE_TYPE", "LKE_BROKER_NODE_TYPE", "LKE_DATABASE_NODE_TYPE"} {
		if strings.TrimSpace(cfg.AdapterResolved[key]) == "" {
			return fmt.Errorf("LKE adapter setting %s is required before mutation", key)
		}
	}
	token := resolveLinodeToken(cfg.RuntimeRoot)
	if token == "" {
		return errors.New("LKE credentials reference is required before mutation: configure LINODE_TOKEN in the environment runtime secret")
	}
	_, err = discoverLKECluster(token, provisionPaths{EnvRoot: cfg.RuntimeRoot}, compat, false)
	if errors.Is(err, errLKEMissingCluster) {
		return nil
	}
	if err != nil {
		return err
	}
	required := []string{
		filepath.Join(cfg.RuntimeRoot, "state", "openbao", "unseal-key"),
		filepath.Join(cfg.RuntimeRoot, "state", "openbao", "root-token"),
		filepath.Join(cfg.RuntimeRoot, "state", "secrets", "postgres"),
	}
	missing := make([]string, 0, len(required))
	for _, path := range required {
		if info, statErr := os.Stat(path); statErr != nil || info.Size() == 0 {
			missing = append(missing, path)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("existing LKE cluster requires matching environment runtime state before mutation; missing %s; either restore the environment state or intentionally rebuild the cluster and persistent storage", strings.Join(missing, ", "))
	}
	return nil
}

func printDeploymentUsage() {
	fmt.Fprint(os.Stdout, `Usage:
  rtk-cloud deployment plan --environment NAME
  rtk-cloud deployment provision --environment NAME --confirm STACK
  rtk-cloud deployment acceptance --environment NAME --confirm STACK
  rtk-cloud deployment remove --environment NAME --confirm STACK
  rtk-cloud deployment test --environment NAME --confirm STACK
`)
}

func resolveDeploymentConfig(workspace, environment, environmentRoot string) (deploymentConfig, error) {
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return deploymentConfig{}, err
		}
	}
	if environmentRoot != "" {
		if strings.HasSuffix(filepath.Clean(environmentRoot), filepath.Join("staging", "lke")) {
			return deploymentConfig{}, errors.New("legacy provider env-root is not supported; use --environment staging")
		}
		environmentRoot, err = filepath.Abs(environmentRoot)
		if err != nil {
			return deploymentConfig{}, err
		}
		if environment == "" {
			environment = filepath.Base(environmentRoot)
		}
	} else {
		if environment == "" {
			return deploymentConfig{}, errors.New("--environment is required")
		}
		environmentRoot = filepath.Join(workspace, "cloud_env", environment)
	}
	envIdentity, err := readStrictEnv(filepath.Join(environmentRoot, "environment.env"))
	if err != nil {
		return deploymentConfig{}, err
	}
	for key := range envIdentity {
		if !keySet("CLOUD_STACK_NAME", "CLOUD_DNS_ROOT_DOMAIN", "DEPLOYMENT_LOCATION")[key] {
			return deploymentConfig{}, fmt.Errorf("unknown environment key %s", key)
		}
	}
	selection, err := readStrictEnv(filepath.Join(environmentRoot, "deployment.env"))
	if err != nil {
		return deploymentConfig{}, err
	}
	for key := range selection {
		if !keySet("DEPLOYMENT_ARCHITECTURE", "DEPLOYMENT_ADAPTER", "DNS_ADAPTER")[key] {
			return deploymentConfig{}, fmt.Errorf("unknown deployment selection key %s", key)
		}
	}
	for _, key := range []string{"CLOUD_STACK_NAME", "CLOUD_DNS_ROOT_DOMAIN", "DEPLOYMENT_LOCATION"} {
		if strings.TrimSpace(envIdentity[key]) == "" {
			return deploymentConfig{}, fmt.Errorf("%s is required in environment.env", key)
		}
	}
	architecture := selection["DEPLOYMENT_ARCHITECTURE"]
	adapter := selection["DEPLOYMENT_ADAPTER"]
	dnsAdapter := selection["DNS_ADAPTER"]
	if architecture == "" || adapter == "" || dnsAdapter == "" {
		return deploymentConfig{}, errors.New("DEPLOYMENT_ARCHITECTURE, DEPLOYMENT_ADAPTER, and DNS_ADAPTER are required")
	}
	values := map[string]string{}
	for _, name := range []string{"architecture.env", "capacity.env", "topology.env", "workloads.env"} {
		part, readErr := readStrictEnv(filepath.Join(workspace, "cloud_deploy", "architectures", architecture, name))
		if readErr != nil {
			return deploymentConfig{}, readErr
		}
		if err := mergeDeploymentLayer(values, part, "architecture"); err != nil {
			return deploymentConfig{}, err
		}
	}
	for k := range values {
		if strings.HasPrefix(k, "LKE_") || strings.HasPrefix(k, "EKS_") || strings.HasPrefix(k, "GKE_") {
			return deploymentConfig{}, fmt.Errorf("provider key %s is not allowed in architecture", k)
		}
		if !deploymentArchitectureKeys[k] {
			return deploymentConfig{}, fmt.Errorf("unknown architecture key %s", k)
		}
	}
	architectureOverride, err := readOptionalStrictEnv(filepath.Join(environmentRoot, "overrides", "architecture.env"))
	if err != nil {
		return deploymentConfig{}, err
	}
	for k := range architectureOverride {
		if _, ok := values[k]; !ok {
			return deploymentConfig{}, fmt.Errorf("unknown architecture override %s", k)
		}
		values[k] = architectureOverride[k]
	}
	for k, v := range envIdentity {
		values[k] = v
	}
	for k, v := range selection {
		values[k] = v
	}
	adapterValues, err := readStrictEnv(filepath.Join(workspace, "cloud_deploy", "adapters", adapter, "defaults.env"))
	if err != nil && adapter == "lke" {
		return deploymentConfig{}, err
	}
	if adapterValues == nil {
		adapterValues = map[string]string{}
	}
	adapterSchema, err := readStrictEnv(filepath.Join(workspace, "cloud_deploy", "adapters", adapter, "schema.env"))
	if err != nil {
		return deploymentConfig{}, err
	}
	if adapterSchema["ADAPTER_NAME"] != adapter || adapterSchema["ADAPTER_RUNTIME"] != values["DEPLOYMENT_RUNTIME"] {
		return deploymentConfig{}, fmt.Errorf("adapter %s schema is incompatible with runtime %s", adapter, values["DEPLOYMENT_RUNTIME"])
	}
	for key := range adapterValues {
		if deploymentArchitectureKeys[key] {
			return deploymentConfig{}, fmt.Errorf("adapter %s may not override architecture key %s", adapter, key)
		}
	}
	adapterOverride, err := readOptionalStrictEnv(filepath.Join(environmentRoot, "overrides", "adapter.env"))
	if err != nil {
		return deploymentConfig{}, err
	}
	for k := range adapterOverride {
		if _, ok := adapterValues[k]; !ok {
			return deploymentConfig{}, fmt.Errorf("unknown %s adapter override %s", adapter, k)
		}
		adapterValues[k] = adapterOverride[k]
	}
	dnsValues, err := readStrictEnv(filepath.Join(workspace, "cloud_deploy", "dns_adapters", dnsAdapter, "defaults.env"))
	if err != nil {
		return deploymentConfig{}, err
	}
	dnsSchema, err := readStrictEnv(filepath.Join(workspace, "cloud_deploy", "dns_adapters", dnsAdapter, "schema.env"))
	if err != nil {
		return deploymentConfig{}, err
	}
	if dnsSchema["DNS_ADAPTER_NAME"] != dnsAdapter {
		return deploymentConfig{}, fmt.Errorf("DNS adapter %s schema is invalid", dnsAdapter)
	}
	dnsAllowed := keySet("DNS_RECORD_TTL", "DNS_PROPAGATION_TIMEOUT_SECONDS", "DNS_PROPAGATION_INTERVAL_SECONDS")
	if dnsAdapter == "godaddy" {
		dnsAllowed["GODADDY_ENV"] = true
	}
	if dnsAdapter == "route53" {
		dnsAllowed["ROUTE53_CONTROL_PLANE_REGION"] = true
	}
	for key := range dnsValues {
		if !dnsAllowed[key] {
			return deploymentConfig{}, fmt.Errorf("unknown %s DNS adapter key %s", dnsAdapter, key)
		}
	}
	dnsOverride, err := readOptionalStrictEnv(filepath.Join(environmentRoot, "overrides", "dns.env"))
	if err != nil {
		return deploymentConfig{}, err
	}
	for k := range dnsOverride {
		if _, ok := dnsValues[k]; !ok {
			return deploymentConfig{}, fmt.Errorf("unknown %s DNS adapter override %s", dnsAdapter, k)
		}
		dnsValues[k] = dnsOverride[k]
	}
	for _, key := range []string{"DNS_RECORD_TTL", "DNS_PROPAGATION_TIMEOUT_SECONDS", "DNS_PROPAGATION_INTERVAL_SECONDS"} {
		if _, err := positiveIntValue(key, dnsValues[key]); err != nil {
			return deploymentConfig{}, err
		}
	}
	if dnsAdapter == "godaddy" {
		if dnsValues["GODADDY_ENV"] != "prod" && dnsValues["GODADDY_ENV"] != "ote" {
			return deploymentConfig{}, errors.New("GODADDY_ENV must be prod or ote")
		}
		if ttl, _ := strconv.Atoi(dnsValues["DNS_RECORD_TTL"]); ttl < 600 {
			return deploymentConfig{}, errors.New("GoDaddy DNS_RECORD_TTL must be at least 600")
		}
	}
	for k, v := range appendMap(values, adapterValues) {
		if deploymentIntegerKeys[k] {
			n, parseErr := strconv.Atoi(v)
			if parseErr != nil || n < 0 {
				return deploymentConfig{}, fmt.Errorf("%s must be a non-negative integer", k)
			}
		}
	}
	for _, key := range []string{
		"NODE_CLASS_GENERAL_MIN_COUNT", "NODE_CLASS_BROKER_MIN_COUNT", "NODE_CLASS_DATABASE_MIN_COUNT",
		"NODE_CLASS_GENERAL_MIN_VCPU", "NODE_CLASS_GENERAL_MIN_MEMORY_GIB",
		"NODE_CLASS_BROKER_MIN_VCPU", "NODE_CLASS_BROKER_MIN_MEMORY_GIB",
		"NODE_CLASS_DATABASE_MIN_VCPU", "NODE_CLASS_DATABASE_MIN_MEMORY_GIB",
	} {
		n, _ := strconv.Atoi(values[key])
		if n <= 0 {
			return deploymentConfig{}, fmt.Errorf("%s must be a positive integer", key)
		}
	}
	if raw := values["MQTT_HARD_ANTI_AFFINITY"]; raw != "true" && raw != "false" {
		return deploymentConfig{}, errors.New("MQTT_HARD_ANTI_AFFINITY must be true or false")
	}
	turnMin, _ := strconv.Atoi(values["TURN_MIN_PORT"])
	turnMax, _ := strconv.Atoi(values["TURN_MAX_PORT"])
	if turnMin > turnMax {
		return deploymentConfig{}, errors.New("TURN_MIN_PORT must not exceed TURN_MAX_PORT")
	}
	if values["DEPLOYMENT_RUNTIME"] != "kubernetes" {
		return deploymentConfig{}, fmt.Errorf("unsupported deployment runtime %q", values["DEPLOYMENT_RUNTIME"])
	}
	capacity, capacityValues, err := buildSharedCapacityPlan(values)
	if err != nil {
		return deploymentConfig{}, err
	}
	for key, value := range capacityValues {
		values[key] = value
	}
	adapterResolved := map[string]string{}
	if adapter == "lke" {
		adapterResolved, err = resolveLKEAdapterResources(workspace, values, adapterValues)
		if err != nil {
			return deploymentConfig{}, err
		}
	}
	return deploymentConfig{Workspace: workspace, Environment: environment, EnvironmentRoot: environmentRoot, RuntimeRoot: filepath.Join(environmentRoot, "runtime"), Architecture: architecture, Adapter: adapter, DNSAdapter: dnsAdapter, Values: values, AdapterValues: adapterValues, AdapterResolved: adapterResolved, DNSValues: dnsValues, Capacity: capacity}, nil
}

func appendMap(a, b map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}
func mergeDeploymentLayer(dst, src map[string]string, layer string) error {
	for k, v := range src {
		if _, ok := dst[k]; ok {
			return fmt.Errorf("duplicate %s key %s", layer, k)
		}
		dst[k] = v
	}
	return nil
}

func readStrictEnv(path string) (map[string]string, error) {
	body, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	for n, raw := range strings.Split(string(body), "\n") {
		line := strings.TrimSpace(raw)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) == "" {
			return nil, fmt.Errorf("%s:%d invalid env assignment", path, n+1)
		}
		key = strings.TrimSpace(key)
		if _, exists := out[key]; exists {
			return nil, fmt.Errorf("%s:%d duplicate key %s", path, n+1, key)
		}
		out[key] = strings.TrimSpace(value)
	}
	return out, nil
}
func readOptionalStrictEnv(path string) (map[string]string, error) {
	out, err := readStrictEnv(path)
	if os.IsNotExist(err) {
		return map[string]string{}, nil
	}
	return out, err
}

func readLKEAccountState(runtimeRoot string, required bool) (map[string]string, error) {
	path := filepath.Join(runtimeRoot, "adapters", "lke", "account.env")
	account, err := readStrictEnv(path)
	if os.IsNotExist(err) && !required {
		return map[string]string{}, nil
	}
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("LKE provider account state is required before mutation: create %s with LKE_ACTIVE_SERVICE_LIMIT", path)
		}
		return nil, err
	}
	for key := range account {
		if key != "LKE_ACTIVE_SERVICE_LIMIT" {
			return nil, fmt.Errorf("unknown LKE provider account key %s", key)
		}
	}
	return account, nil
}

func positiveIntValue(key, raw string) (int, error) {
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return n, nil
}

func materializeDeploymentRuntime(cfg deploymentConfig) error {
	for _, dir := range []string{"resolved", "env", "state", filepath.Join("adapters", cfg.Adapter), filepath.Join("dns", cfg.DNSAdapter), "services", "secrets", "devices", "artifacts", "backups"} {
		if err := os.MkdirAll(filepath.Join(cfg.RuntimeRoot, dir), 0o700); err != nil {
			return err
		}
	}
	resolved := appendMap(cfg.Values, nil)
	stack := appendMap(cfg.Values, deploymentRuntimeEndpoints(resolved))
	stack = appendMap(stack, cfg.DNSValues)
	stack["CLOUD_ENV_NAME"] = cfg.Environment
	stack["CLOUD_PROVIDER"] = cfg.Adapter
	stack["CLOUD_REGION"] = cfg.AdapterResolved["LKE_REGION"]
	if err := writeSortedEnv(filepath.Join(cfg.RuntimeRoot, "resolved", "deployment.env"), resolved, 0o600); err != nil {
		return err
	}
	if err := writeSortedEnv(filepath.Join(cfg.RuntimeRoot, "env", "stack.env"), stack, 0o600); err != nil {
		return err
	}
	adapterRuntime := appendMap(cfg.AdapterValues, cfg.AdapterResolved)
	compatInput := appendMap(resolved, cfg.AdapterResolved)
	adapterRuntime = appendMap(adapterRuntime, deploymentLegacyLKEValues(compatInput, cfg.Environment))
	account, err := readLKEAccountState(cfg.RuntimeRoot, false)
	if err != nil {
		return err
	}
	providerPreflight := map[string]string{}
	if region := cfg.AdapterResolved["LKE_REGION"]; region != "" {
		providerPreflight["PROVIDER_REGION"] = region
	}
	if limit := account["LKE_ACTIVE_SERVICE_LIMIT"]; limit != "" {
		if _, err := positiveIntValue("LKE_ACTIVE_SERVICE_LIMIT", limit); err != nil {
			return err
		}
		adapterRuntime["LKE_LINODE_ACTIVE_SERVICE_LIMIT"] = limit
		providerPreflight["PROVIDER_ACTIVE_SERVICE_LIMIT"] = limit
	}
	if err := writeSortedEnv(filepath.Join(cfg.RuntimeRoot, "adapters", cfg.Adapter, "config.env"), adapterRuntime, 0o600); err != nil {
		return err
	}
	if err := writeSortedEnv(filepath.Join(cfg.RuntimeRoot, "adapters", cfg.Adapter, "resolved-resources.env"), cfg.AdapterResolved, 0o600); err != nil {
		return err
	}
	if err := writeSortedEnv(filepath.Join(cfg.RuntimeRoot, "state", "provider-preflight.env"), providerPreflight, 0o600); err != nil {
		return err
	}
	if err := writeSortedEnv(filepath.Join(cfg.RuntimeRoot, "dns", cfg.DNSAdapter, "config.env"), cfg.DNSValues, 0o600); err != nil {
		return err
	}
	dnsPlan := buildGenericDNSPlan(cfg)
	if body, err := json.MarshalIndent(dnsPlan, "", "  "); err != nil {
		return err
	} else if err := os.WriteFile(filepath.Join(cfg.RuntimeRoot, "resolved", "dns-plan.json"), append(body, '\n'), 0o600); err != nil {
		return err
	}
	if err := writeSortedEnv(filepath.Join(cfg.RuntimeRoot, "state", "dns.env"), map[string]string{"DNS_ADAPTER": cfg.DNSAdapter, "DNS_ROOT_DOMAIN": cfg.Values["CLOUD_DNS_ROOT_DOMAIN"]}, 0o600); err != nil {
		return err
	}
	plan := map[string]any{"environment": cfg.Environment, "architecture": cfg.Architecture, "adapter": cfg.Adapter, "dns_adapter": cfg.DNSAdapter, "values": resolved, "capacity": cfg.Capacity}
	body, _ := json.MarshalIndent(plan, "", "  ")
	body = append(body, '\n')
	return os.WriteFile(filepath.Join(cfg.RuntimeRoot, "resolved", "deployment-plan.json"), body, 0o600)
}

func deploymentRuntimeEndpoints(v map[string]string) map[string]string {
	stack := strings.TrimSpace(v["CLOUD_STACK_NAME"])
	rootDomain := strings.TrimSpace(v["CLOUD_DNS_ROOT_DOMAIN"])
	if stack == "" || rootDomain == "" {
		return map[string]string{}
	}
	publicHost := firstNonEmpty(strings.TrimSpace(v["VIDEO_CLOUD_DOMAIN"]), stack+"."+rootDomain)
	accountHost := firstNonEmpty(strings.TrimSpace(v["ACCOUNT_MANAGER_DOMAIN"]), "account-manager."+stack+"."+rootDomain)
	deviceHost := firstNonEmpty(strings.TrimSpace(v["VIDEO_CLOUD_DEVICE_DOMAIN"]), "device."+publicHost)
	return map[string]string{
		"ACCOUNT_MANAGER_BASE_URL":    "https://" + accountHost,
		"VIDEO_CLOUD_BASE_URL":        "https://" + publicHost,
		"VIDEO_CLOUD_PUBLIC_BASE_URL": "https://" + publicHost,
		"VIDEO_CLOUD_MTLS_BASE_URL":   "https://" + deviceHost,
		"VIDEO_CLOUD_TOKEN_BASE_URL":  "https://" + deviceHost,
		"VIDEO_CLOUD_MQTT_ADDR":       publicHost + ":8883",
	}
}

func deploymentLegacyLKEValues(v map[string]string, environment string) map[string]string {
	if v["DEPLOYMENT_ADAPTER"] != "lke" {
		return map[string]string{}
	}
	out := map[string]string{
		"CLOUD_ENV_NAME": environment, "CLOUD_PROVIDER": "lke", "CLOUD_REGION": v["LKE_REGION"],
		"LKE_TARGET_CONNECTS": v["CAPACITY_TARGET_CONNECTIONS"], "LKE_MQTT_CONNECTIONS_PER_POD": v["CAPACITY_CONNECTIONS_PER_MQTT_POD"],
		"LKE_SYSTEM_RESERVED_CPU_PER_NODE": v["CAPACITY_SYSTEM_RESERVED_CPU_MILLI"] + "m", "LKE_SYSTEM_RESERVED_MEMORY_PER_NODE": v["CAPACITY_SYSTEM_RESERVED_MEMORY_MIB"] + "Mi",
		"LKE_NODE_COUNT": v["NODE_CLASS_BROKER_EFFECTIVE_COUNT"], "LKE_NODE_TYPE": v["LKE_BROKER_NODE_TYPE"],
		"LKE_GENERAL_NODE_COUNT": v["NODE_CLASS_GENERAL_EFFECTIVE_COUNT"], "LKE_GENERAL_NODE_TYPE": v["LKE_GENERAL_NODE_TYPE"],
		"LKE_POSTGRES_DEDICATED_NODE_POOL": "true", "LKE_POSTGRES_NODE_COUNT": v["NODE_CLASS_DATABASE_EFFECTIVE_COUNT"], "LKE_POSTGRES_NODE_TYPE": v["LKE_DATABASE_NODE_TYPE"],
		"LKE_MQTT_REPLICAS": v["MQTT_EFFECTIVE_REPLICAS"], "LKE_VIDEO_CLOUD_REPLICAS": v["VIDEO_CLOUD_API_EFFECTIVE_REPLICAS"],
		"LKE_POSTGRES_REQUEST_CPU": v["POSTGRES_REQUEST_CPU"], "LKE_POSTGRES_REQUEST_MEMORY": v["POSTGRES_REQUEST_MEMORY"], "LKE_POSTGRES_LIMIT_MEMORY": v["POSTGRES_LIMIT_MEMORY"],
		"LKE_CLOUD_LOGGER_REQUEST_CPU": v["CLOUD_LOGGER_REQUEST_CPU"], "LKE_CLOUD_LOGGER_REQUEST_MEMORY": v["CLOUD_LOGGER_REQUEST_MEMORY"], "LKE_CLOUD_LOGGER_LIMIT_MEMORY": v["CLOUD_LOGGER_LIMIT_MEMORY"],
		"VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED": v["VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED"],
		"LKE_EDGE_HAPROXY_COUNT":                 v["EDGE_REPLICAS"], "LKE_EDGE_HAPROXY_MAXCONN": v["EDGE_MAX_CONNECTIONS"],
		"LKE_COTURN_VM_COUNT": v["TURN_REPLICAS"], "LKE_COTURN_MIN_PORT": v["TURN_MIN_PORT"], "LKE_COTURN_MAX_PORT": v["TURN_MAX_PORT"],
	}
	for _, prefix := range []string{"INGRESS", "REDIS", "REDIS_EXPORTER"} {
		out["LKE_"+prefix+"_REQUEST_CPU"] = v[prefix+"_REQUEST_CPU"]
		out["LKE_"+prefix+"_REQUEST_MEMORY"] = v[prefix+"_REQUEST_MEMORY"]
	}
	return out
}

func writeSortedEnv(path string, values map[string]string, mode os.FileMode) error {
	keys := make([]string, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	for _, k := range keys {
		if values[k] != "" {
			fmt.Fprintf(&b, "%s=%s\n", k, values[k])
		}
	}
	return os.WriteFile(path, []byte(b.String()), mode)
}

func normalizeDeploymentRuntime(cfg deploymentConfig) error {
	normalized := filepath.Join(cfg.RuntimeRoot, "state", "kubeconfig.yaml")
	body, err := os.ReadFile(normalized)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if cfg.Adapter != "lke" {
		return nil
	}
	adapterState, err := readOptionalStrictEnv(filepath.Join(cfg.RuntimeRoot, "adapters", "lke", "state.env"))
	if err != nil {
		return err
	}
	clusterID := strings.TrimSpace(adapterState["LKE_CLUSTER_ID"])
	if clusterID == "" {
		return nil
	}
	replacements := []string{
		"lke" + clusterID + "-admin", "rtk-cloud-" + cfg.Environment + "-admin",
		"lke" + clusterID + "-ctx", "rtk-cloud-" + cfg.Environment + "-context",
		"lke" + clusterID, "rtk-cloud-" + cfg.Environment + "-cluster",
	}
	sanitized := strings.NewReplacer(replacements...).Replace(string(body))
	return os.WriteFile(normalized, []byte(sanitized), 0o600)
}
