package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/internal/cloudmonitor"
)

// This command resolves desired intent only. In particular it does not invoke
// deployment plan, materialization, SecretStore, kubectl, or provider APIs.
func runMonitorInventory(args []string) error {
	return runMonitorInventoryTo(args, os.Stdout)
}

func runMonitorInventoryTo(args []string, out io.Writer) error {
	fs := flag.NewFlagSet("monitor-inventory", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	environment := fs.String("environment", "", "environment under cloud_env (required)")
	workspace := fs.String("workspace", "", "workspace root")
	environmentRoot := fs.String("environment-root", "", "explicit environment directory for custom configurations")
	jsonOutput := fs.Bool("json", false, "emit the versioned monitor inventory JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("monitor-inventory does not accept positional arguments")
	}
	if !*jsonOutput {
		return errors.New("monitor-inventory requires --json")
	}
	if err := rejectMonitorAmbientOverrides(); err != nil {
		return err
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, *environmentRoot)
	if err != nil {
		return err
	}
	inventory, err := buildMonitorInventory(cfg, time.Now().UTC())
	if err != nil {
		return err
	}
	encoder := json.NewEncoder(out)
	encoder.SetIndent("", "  ")
	return encoder.Encode(inventory)
}

// Legacy descriptor helpers consult os.Getenv. Reject relevant overrides rather
// than mutating the process environment or accidentally exporting another stack.
func rejectMonitorAmbientOverrides() error {
	managed := keySet("CLOUD_STACK_NAME", "CLOUD_ENV_NAME", "CLOUD_RUNTIME_COVERAGE_STACK", "CLOUD_DNS_ROOT_DOMAIN", "VIDEO_CLOUD_DOMAIN", "VIDEO_CLOUD_DEVICE_DOMAIN", "ACCOUNT_MANAGER_DOMAIN", "CLOUD_ADMIN_DOMAIN", "CLOUD_LOGGER_DOMAIN", "FRONTEND_DOMAIN", "BILLING_DOMAIN", "PAYMENT_SIMULATOR_DOMAIN", "DEPLOYMENT_REQUIRE_PKITURN", "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED", "TEST_LAB_ENABLED")
	var poisoned []string
	for _, entry := range os.Environ() {
		key, value, _ := strings.Cut(entry, "=")
		if value != "" && (strings.HasPrefix(key, "LKE_") || managed[key]) {
			poisoned = append(poisoned, key)
		}
	}
	if len(poisoned) == 0 {
		return nil
	}
	sort.Strings(poisoned)
	return fmt.Errorf("monitor-inventory rejects ambient deployment overrides; use the selected environment files: %s", strings.Join(poisoned, ", "))
}

var monitorFeatureKeys = []string{
	"LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED", "LKE_SHADOW_WORKER_REGISTRATION_ENABLED",
	"LKE_WEBRTC_SERVICE_REGISTRATION_ENABLED", "LKE_VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED",
	"LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED",
}

// Rollout gates currently live in the non-secret runtime stack file. Read only
// explicitly allowlisted intent; identities and DNS remain canonical cfg values.
func monitorFeatureIntent(cfg deploymentConfig) (map[string]string, []string, error) {
	features := map[string]string{}
	path := filepath.Join(cfg.RuntimeRoot, "env", "stack.env")
	runtimeValues, err := readOptionalStrictEnv(path)
	if err != nil {
		return nil, nil, fmt.Errorf("monitor runtime intent: %w", err)
	}
	for key, want := range map[string]string{"CLOUD_STACK_NAME": cfg.Values["CLOUD_STACK_NAME"], "CLOUD_ENV_NAME": cfg.Environment} {
		if actual := runtimeValues[key]; actual != "" && actual != want {
			return nil, nil, fmt.Errorf("monitor runtime intent %s does not match the selected environment", key)
		}
	}
	for _, key := range monitorFeatureKeys {
		if value, ok := runtimeValues[key]; ok {
			value = strings.ToLower(strings.TrimSpace(value))
			switch value {
			case "true", "1", "yes", "on":
				features[key] = "true"
			case "false", "0", "no", "off":
				features[key] = "false"
			default:
				return nil, nil, fmt.Errorf("invalid runtime feature declaration %s", key)
			}
		}
	}
	var missing []string
	for _, key := range monitorFeatureKeys {
		if _, ok := features[key]; !ok {
			missing = append(missing, key)
		}
	}
	var notes []string
	if len(missing) > 0 {
		notes = append(notes, "Optional rollout intent is undeclared in runtime/env/stack.env; targets remain UNKNOWN until explicitly declared: "+strings.Join(missing, ", "))
	}
	return features, notes, nil
}

// Resolve endpoint defaults from the selected configuration without requiring
// deployment materialization. Reuse the deployment resolver so monitor
// inventory matches the canonical hostname and alias policy.
func monitorEndpointValues(cfg deploymentConfig) map[string]string {
	values := deploymentEndpointValues(cfg)
	// Committed TURN descriptor helpers consume legacy count keys. Supply the
	// canonical architecture intent without reading adapter runtime files.
	return appendMap(values, deploymentLegacyLKEValues(values, cfg.Environment))
}

func buildMonitorInventory(cfg deploymentConfig, now time.Time) (cloudmonitor.Inventory, error) {
	if err := rejectMonitorAmbientOverrides(); err != nil {
		return cloudmonitor.Inventory{}, err
	}
	values := monitorEndpointValues(cfg)
	features, notes, err := monitorFeatureIntent(cfg)
	if err != nil {
		return cloudmonitor.Inventory{}, err
	}
	values = appendMap(values, features)
	if v := values["DEPLOYMENT_REQUIRE_PKITURN"]; v != "" && v != "true" && v != "false" {
		return cloudmonitor.Inventory{}, errors.New("DEPLOYMENT_REQUIRE_PKITURN must be true or false")
	}
	inventory := cloudmonitor.Inventory{SchemaVersion: cloudmonitor.SchemaVersion, Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], GeneratedAt: now.UTC(), Targets: []cloudmonitor.WorkloadTarget{}, Endpoints: []cloudmonitor.Endpoint{}, MetricTargets: []cloudmonitor.MetricTarget{}, CoverageNotes: notes}
	add := func(service, namespace, kind, name string, enabled bool, reason string) {
		inventory.Targets = append(inventory.Targets, cloudmonitor.WorkloadTarget{ID: namespace + "/" + kind + "/" + name, Service: service, Namespace: namespace, Kind: kind, Name: name, Required: enabled, Enabled: enabled, DisabledReason: reason})
	}
	ns := func(key string) string { return lkeNamespaceName(values, key) }
	for _, workload := range k8sWorkloads(values) {
		add(workload.Key, workload.Namespace, "deployment", workload.Name, true, "")
	}
	for _, workload := range k8sAuxiliaryWorkloads() {
		enabled, reason := true, ""
		if workload.Name == "video-cloud-clipverifier" && !lkeClipDirectUploadEnabled(values) {
			enabled, reason = false, "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED=false; deployment scales verifier to zero"
		}
		add(workload.Name, ns("video-cloud"), "deployment", workload.Name, enabled, reason)
	}
	for _, spec := range []struct{ service, namespace, kind, name string }{
		{"postgres", "platform", "statefulset", "postgresql"},
		{"redis", "platform", "deployment", "redis"},
		{"redis", "platform", "deployment", "redis-exporter"},
		{"fleet-valkey", "platform", "statefulset", "fleet-valkey"},
		{"fleet-valkey", "platform", "deployment", "fleet-valkey-exporter"},
		{"mqtt", "video-cloud", "statefulset", "mqtt"},
		{"certissuer", "video-cloud", "deployment", "certissuer"},
		{"factoryenroll", "video-cloud", "deployment", "factoryenroll"},
		{"openbao", "secrets", "statefulset", "openbao"},
		{"account-manager-email-worker", "account-manager", "deployment", "account-manager-email-worker"},
		{"account-manager-outbox-worker", "account-manager", "deployment", "account-manager-outbox-worker"},
		{"billing-payment-worker", "billing", "deployment", "billing-payment-worker"},
		{"payment-simulator", "billing", "deployment", "payment-simulator"},
		{"prometheus", "observability", "deployment", "video-cloud-prometheus"},
		{"loki", "observability", "deployment", "video-cloud-loki"},
		{"grafana", "observability", "deployment", "video-cloud-grafana"},
		{"log-collector", "observability", "daemonset", "video-cloud-log-collector"},
		{"ingress", "ingress", "deployment", "ingress-nginx-controller"},
	} {
		add(spec.service, ns(spec.namespace), spec.kind, spec.name, true, "")
	}
	for _, spec := range []struct{ key, namespace, name string }{
		{"LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED", "video-cloud", "video-cloud-mqttfoundation"},
		{"LKE_SHADOW_WORKER_REGISTRATION_ENABLED", "video-cloud", "video-cloud-shadowworker"},
		{"LKE_WEBRTC_SERVICE_REGISTRATION_ENABLED", "video-cloud", "video-cloud-webrtcservice"},
		{"LKE_VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED", "video-cloud", "video-cloud-videostorage"},
		{"LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED", "account-manager", "account-manager-handoff-worker"},
		{"LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED", "account-manager", "account-manager-cloud-deletion-worker"},
		{"LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED", "billing", "billing-settlement-collector"},
	} {
		declaration, declared := features[spec.key]
		enabled, reason := declaration == "true" || !declared, ""
		if declared && !enabled {
			reason = spec.key + "=false"
		}
		add(spec.name, ns(spec.namespace), "deployment", spec.name, enabled, reason)
		if !declared {
			inventory.Targets[len(inventory.Targets)-1].IntentUnknown = true
		}
	}
	for _, name := range []string{"pkiturn", "pki-controller"} {
		declaration := values["DEPLOYMENT_REQUIRE_PKITURN"]
		// This flag requires PKI TURN acceptance; false does not assert that
		// the deployments are absent. Keep their installation intent unknown.
		add(name, ns("video-cloud"), "deployment", name, true, "")
		target := &inventory.Targets[len(inventory.Targets)-1]
		target.Required = declaration != "false"
		target.IntentUnknown = declaration != "true"
	}
	// Public edge and TURN run outside Kubernetes in the current architecture.
	add("public-edge", "", "external", "haproxy", true, "")
	if lkeCoturnVMCount(values) > 0 {
		for _, domain := range lkeCoturnDomains(values) {
			add("turn", "", "external", domain, true, "")
		}
	}
	for _, endpoint := range []struct {
		service, domain, path string
		codes                 []int
	}{
		{"video-cloud", values["VIDEO_CLOUD_DOMAIN"], "/healthz", []int{200}},
		{"account-manager", values["ACCOUNT_MANAGER_DOMAIN"], "/v1/health", []int{200}},
		{"billing", lkeBillingPublicDomain(values), "/healthz", []int{200}},
		{"cloud-admin", values["CLOUD_ADMIN_DOMAIN"], "/healthz", []int{200}},
		{"frontend", lkeFrontendPublicDomain(values), "/", []int{200}},
		{"cloud-logger", values["CLOUD_LOGGER_DOMAIN"], "/healthz", []int{204}},
	} {
		inventory.Endpoints = append(inventory.Endpoints, cloudmonitor.Endpoint{Service: endpoint.service, URL: "https://" + endpoint.domain, Path: endpoint.path, SuccessCodes: endpoint.codes, LivenessOnly: true})
	}
	disabled := map[string]bool{}
	for _, target := range inventory.Targets {
		if !target.Enabled {
			disabled[target.Namespace+"/"+target.Name] = true
		}
	}
	for _, target := range k8sPrometheusTargets(values, provisionOptions{}) {
		if !disabled[target.Namespace+"/"+target.Service] {
			inventory.MetricTargets = append(inventory.MetricTargets, cloudmonitor.MetricTarget{Job: target.Job, Namespace: target.Namespace, Service: target.Service, Port: target.Port, Path: target.Path})
		}
	}
	// Hash effective desired configuration, never secret runtime fields or paths.
	source, err := json.Marshal(struct {
		Environment, Architecture, Adapter, DNSAdapter              string
		Values, Features, AdapterValues, AdapterResolved, DNSValues map[string]string
		Storage                                                     deploymentStoragePlan
	}{cfg.Environment, cfg.Architecture, cfg.Adapter, cfg.DNSAdapter, cfg.Values, features, cfg.AdapterValues, cfg.AdapterResolved, cfg.DNSValues, cfg.Storage})
	if err != nil {
		return cloudmonitor.Inventory{}, err
	}
	sum := sha256.Sum256(source)
	inventory.SourceFingerprint = hex.EncodeToString(sum[:])
	sort.Slice(inventory.Targets, func(i, j int) bool { return inventory.Targets[i].ID < inventory.Targets[j].ID })
	return inventory, nil
}
