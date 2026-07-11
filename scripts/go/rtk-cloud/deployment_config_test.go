package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestResolveDeploymentConfigSupportsMultipleEnvironments(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Environment != "staging" || cfg.Architecture != "kubernetes" || cfg.Adapter != "lke" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
	if cfg.Values["CAPACITY_TARGET_CONNECTIONS"] != "1000" {
		t.Fatalf("target = %q", cfg.Values["CAPACITY_TARGET_CONNECTIONS"])
	}
	if cfg.RuntimeRoot != filepath.Join(workspace, "cloud_env", "staging", "runtime") {
		t.Fatalf("runtime = %s", cfg.RuntimeRoot)
	}
}

func TestResolveDeploymentConfigRejectsLegacyRoot(t *testing.T) {
	_, err := resolveDeploymentConfig(t.TempDir(), "", filepath.Join(t.TempDir(), "cloud_env", "staging", "lke"))
	if err == nil || !strings.Contains(err.Error(), "legacy provider env-root") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveDeploymentConfigRejectsProviderKeyInArchitecture(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	appendFile(t, filepath.Join(workspace, "cloud_deploy", "architectures", "kubernetes", "workloads.env"), "LKE_NODE_COUNT=2\n")
	_, err := resolveDeploymentConfig(workspace, "dev", "")
	if err == nil || !strings.Contains(err.Error(), "not allowed in architecture") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveDeploymentConfigRejectsUnknownArchitectureKey(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	appendFile(t, filepath.Join(workspace, "cloud_deploy", "architectures", "kubernetes", "workloads.env"), "UNDECLARED_WORKLOAD_SETTING=true\n")
	_, err := resolveDeploymentConfig(workspace, "dev", "")
	if err == nil || !strings.Contains(err.Error(), "unknown architecture key") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveDeploymentConfigRejectsUnimplementedMutation(t *testing.T) {
	for _, adapter := range []string{"eks", "gke"} {
		workspace := writeDeploymentFixture(t, "prod", adapter)
		if err := runDeployment([]string{"plan", "--workspace", workspace, "--environment", "prod"}); err != nil {
			t.Fatalf("%s plan failed: %v", adapter, err)
		}
		err := runDeployment([]string{"provision", "--workspace", workspace, "--environment", "prod", "--confirm", "video-cloud-prod"})
		if err == nil || !strings.Contains(err.Error(), "not implemented") {
			t.Fatalf("%s mutation got %v", adapter, err)
		}
	}
}

func TestMaterializeDeploymentRuntimeSeparatesSharedAndAdapterConfig(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := materializeDeploymentRuntime(cfg); err != nil {
		t.Fatal(err)
	}
	resolved := readTestFile(t, filepath.Join(cfg.RuntimeRoot, "resolved", "deployment.env"))
	if strings.Contains(resolved, "LKE_NODE_COUNT") {
		t.Fatalf("resolved generic config leaked compatibility keys:\n%s", resolved)
	}
	compat := readTestFile(t, filepath.Join(cfg.RuntimeRoot, "env", "stack.env"))
	for _, want := range []string{"CLOUD_PROVIDER=lke", "CAPACITY_TARGET_CONNECTIONS=1000", "MQTT_REPLICAS=2"} {
		if !strings.Contains(compat, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(compat, "LKE_") {
		t.Fatalf("shared stack contains adapter compatibility keys:\n%s", compat)
	}
	adapterConfig := readTestFile(t, filepath.Join(cfg.RuntimeRoot, "adapters", "lke", "config.env"))
	for _, want := range []string{"LKE_TARGET_CONNECTS=1000", "LKE_MQTT_REPLICAS=2"} {
		if !strings.Contains(adapterConfig, want) {
			t.Fatalf("adapter config missing %s", want)
		}
	}
}

func TestNormalizeEnvironmentArgs(t *testing.T) {
	args, err := normalizeEnvironmentArgs([]string{"mqtt-test", "--workspace", "/tmp/ws", "--environment", "dev", "--brandname", "RTK"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"mqtt-test", "--workspace", "/tmp/ws", "--env-root", "/tmp/ws/cloud_env/dev/runtime", "--brandname", "RTK"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("normalizeEnvironmentArgs() = %#v, want %#v", args, want)
	}
	if _, err := normalizeEnvironmentArgs([]string{"mqtt-test", "--env-root", "/tmp/runtime", "--environment", "dev"}); err == nil {
		t.Fatal("expected conflicting environment selectors to fail")
	}
}

func writeDeploymentFixture(t *testing.T, environment, adapter string) string {
	t.Helper()
	root := t.TempDir()
	files := map[string]string{
		"cloud_deploy/architectures/kubernetes/architecture.env":   "DEPLOYMENT_RUNTIME=kubernetes\nNODE_CLASS_LABEL_KEY=rtk.io/node-class\nDEFAULT_WORKLOAD_NODE_CLASS=general\n",
		"cloud_deploy/architectures/kubernetes/capacity.env":       "CAPACITY_TARGET_CONNECTIONS=1000\nCAPACITY_CONNECTIONS_PER_MQTT_POD=20000\n",
		"cloud_deploy/architectures/kubernetes/topology.env":       "NODE_CLASS_GENERAL_MIN_COUNT=2\nNODE_CLASS_BROKER_MIN_COUNT=2\nMQTT_NODE_CLASS=broker\n",
		"cloud_deploy/architectures/kubernetes/workloads.env":      "MQTT_REPLICAS=2\nMQTT_HARD_ANTI_AFFINITY=true\nVIDEO_CLOUD_API_REPLICAS=2\nPOSTGRES_REQUEST_CPU=1\nPOSTGRES_REQUEST_MEMORY=2Gi\nPOSTGRES_LIMIT_MEMORY=4Gi\nEDGE_REPLICAS=1\nEDGE_MAX_CONNECTIONS=400000\nTURN_REPLICAS=1\nTURN_MIN_PORT=49152\nTURN_MAX_PORT=49200\n",
		"cloud_deploy/adapters/lke/defaults.env":                   "LKE_REGION=us-sea\nLKE_GENERAL_NODE_TYPE=g6-standard-4\nLINODE_ACTIVE_SERVICE_LIMIT=20\n",
		"cloud_deploy/adapters/lke/schema.env":                     "ADAPTER_NAME=lke\nADAPTER_RUNTIME=kubernetes\nADAPTER_MUTATION_SUPPORTED=true\n",
		"cloud_deploy/adapters/eks/schema.env":                     "ADAPTER_NAME=eks\nADAPTER_RUNTIME=kubernetes\nADAPTER_MUTATION_SUPPORTED=false\n",
		"cloud_deploy/adapters/gke/schema.env":                     "ADAPTER_NAME=gke\nADAPTER_RUNTIME=kubernetes\nADAPTER_MUTATION_SUPPORTED=false\n",
		filepath.Join("cloud_env", environment, "environment.env"): "CLOUD_ENVIRONMENT=" + environment + "\nCLOUD_STACK_NAME=video-cloud-" + environment + "\nCLOUD_DNS_ROOT_DOMAIN=example.test\n",
		filepath.Join("cloud_env", environment, "deployment.env"):  "DEPLOYMENT_ARCHITECTURE=kubernetes\nDEPLOYMENT_ADAPTER=" + adapter + "\n",
	}
	for path, body := range files {
		writeTestFile(t, filepath.Join(root, path), body)
	}
	return root
}

func appendFile(t *testing.T, path, body string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err = f.WriteString(body); err != nil {
		t.Fatal(err)
	}
}
