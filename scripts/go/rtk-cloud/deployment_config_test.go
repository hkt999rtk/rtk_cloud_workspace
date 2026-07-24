package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestDeploymentTestUsesIdenticalLifecycleForEveryEnvironment(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			workspace := writeDeploymentFixture(t, environment, "lke")
			calls := []string{}
			ops := deploymentOperations{
				prepareTest: func(deploymentConfig) error { return nil },
				provision: func(deploymentConfig) error {
					calls = append(calls, "provision")
					return nil
				},
				acceptance: func(deploymentConfig) error {
					calls = append(calls, "acceptance")
					return nil
				},
				cleanup: func(deploymentConfig) error {
					calls = append(calls, "cleanup")
					return nil
				},
				normalize: func(deploymentConfig) error {
					calls = append(calls, "normalize")
					return nil
				},
			}
			err := runDeploymentWithOperations([]string{
				"test", "--workspace", workspace, "--environment", environment,
				"--confirm", "video-cloud-" + environment,
			}, ops)
			if err != nil {
				t.Fatal(err)
			}
			want := []string{"provision", "acceptance", "cleanup", "normalize"}
			if !reflect.DeepEqual(calls, want) {
				t.Fatalf("calls = %v, want %v", calls, want)
			}
		})
	}
}

func TestDeploymentTestAlwaysCleansUpAfterFailure(t *testing.T) {
	for _, tc := range []struct {
		name              string
		provisionErr      error
		acceptanceErr     error
		wantAcceptanceRun bool
		wantError         string
	}{
		{name: "provision", provisionErr: errors.New("partial provision"), wantError: "provision phase failed"},
		{name: "acceptance", acceptanceErr: errors.New("probe failed"), wantAcceptanceRun: true, wantError: "acceptance phase failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			workspace := writeDeploymentFixture(t, "dev", "lke")
			acceptanceRan := false
			cleanupRan := false
			ops := deploymentOperations{
				prepareTest: func(deploymentConfig) error { return nil },
				provision:   func(deploymentConfig) error { return tc.provisionErr },
				acceptance: func(deploymentConfig) error {
					acceptanceRan = true
					return tc.acceptanceErr
				},
				cleanup: func(deploymentConfig) error {
					cleanupRan = true
					return nil
				},
				normalize: func(deploymentConfig) error { return nil },
			}
			err := runDeploymentWithOperations([]string{
				"test", "--workspace", workspace, "--environment", "dev", "--confirm", "video-cloud-dev",
			}, ops)
			if err == nil || !strings.Contains(err.Error(), tc.wantError) {
				t.Fatalf("error = %v, want %q", err, tc.wantError)
			}
			if acceptanceRan != tc.wantAcceptanceRun {
				t.Fatalf("acceptanceRan = %t, want %t", acceptanceRan, tc.wantAcceptanceRun)
			}
			if !cleanupRan {
				t.Fatal("cleanup did not run")
			}
		})
	}
}

func TestDeploymentTestReportsCleanupFailure(t *testing.T) {
	workspace := writeDeploymentFixture(t, "prod", "lke")
	ops := deploymentOperations{
		prepareTest: func(deploymentConfig) error { return nil },
		provision:   func(deploymentConfig) error { return nil },
		acceptance:  func(deploymentConfig) error { return nil },
		cleanup:     func(deploymentConfig) error { return errors.New("resource remained") },
		normalize:   func(deploymentConfig) error { return nil },
	}
	err := runDeploymentWithOperations([]string{
		"test", "--workspace", workspace, "--environment", "prod", "--confirm", "video-cloud-prod",
	}, ops)
	if err == nil || !strings.Contains(err.Error(), "cleanup phase failed") {
		t.Fatalf("error = %v", err)
	}
}

func TestDeploymentTestDoesNotCleanupPreExistingEnvironment(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	cleanupRan := false
	ops := deploymentOperations{
		prepareTest: func(deploymentConfig) error { return errors.New("stack already exists") },
		provision:   func(deploymentConfig) error { return errors.New("must not run") },
		acceptance:  func(deploymentConfig) error { return errors.New("must not run") },
		cleanup: func(deploymentConfig) error {
			cleanupRan = true
			return nil
		},
		normalize: func(deploymentConfig) error { return nil },
	}
	err := runDeploymentWithOperations([]string{
		"test", "--workspace", workspace, "--environment", "staging", "--confirm", "video-cloud-staging",
	}, ops)
	if err == nil || !strings.Contains(err.Error(), "ephemeral test preflight failed") {
		t.Fatalf("error = %v", err)
	}
	if cleanupRan {
		t.Fatal("cleanup must not run for a pre-existing environment")
	}
}

func TestDeploymentEnvironmentResourceLabelsExcludeUnownedOrphans(t *testing.T) {
	plan := linodeDestroyPlan{
		LKEClusters:         []linodeDestroyResource{{Label: "stack-lke"}},
		Instances:           []linodeDestroyResource{{Label: "stack-edge"}},
		ObjectBuckets:       []linodeDestroyResource{{Label: "stack-artifacts"}},
		OrphanVolumes:       []linodeDestroyResource{{Label: "pvc-unrelated"}},
		OrphanNodeBalancers: []linodeDestroyResource{{Label: "lke-unrelated"}},
	}
	want := []string{"stack-artifacts", "stack-edge", "stack-lke"}
	if got := deploymentEnvironmentResourceLabels(plan); !reflect.DeepEqual(got, want) {
		t.Fatalf("labels = %v, want %v", got, want)
	}
}

func TestPersistentVolumeIDsForStackSelectsOnlyOwnedNumericHandles(t *testing.T) {
	body := []byte(`{"items":[
		{"spec":{"claimRef":{"namespace":"video-cloud-dev-platform"},"csi":{"volumeHandle":"102-pvc-example"}}},
		{"spec":{"claimRef":{"namespace":"video-cloud-dev-video-cloud"},"csi":{"volumeHandle":"101"}}},
		{"spec":{"claimRef":{"namespace":"video-cloud-staging-platform"},"csi":{"volumeHandle":"999"}}}
	]}`)
	got, err := persistentVolumeIDsForStack(body, "video-cloud-dev")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"101", "102"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ids = %v, want %v", got, want)
	}
	bad := []byte(`{"items":[{"spec":{"claimRef":{"namespace":"video-cloud-dev-platform"},"csi":{"volumeHandle":"not-a-linode-id"}}}]}`)
	if _, err := persistentVolumeIDsForStack(bad, "video-cloud-dev"); err == nil {
		t.Fatal("expected non-numeric owned volume handle to fail")
	}
}

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
	for key, want := range map[string]string{"LKE_REGION": "us-sea", "LKE_GENERAL_NODE_TYPE": "g6-standard-4", "LKE_BROKER_NODE_TYPE": "g6-standard-4", "LKE_DATABASE_NODE_TYPE": "g6-standard-8"} {
		if cfg.AdapterResolved[key] != want {
			t.Fatalf("%s = %q, want %q", key, cfg.AdapterResolved[key], want)
		}
	}
}

func TestResolveDeploymentConfigRejectsLegacyRoot(t *testing.T) {
	_, err := resolveDeploymentConfig(t.TempDir(), "", filepath.Join(t.TempDir(), "cloud_env", "staging", "lke"))
	if err == nil || !strings.Contains(err.Error(), "legacy provider env-root") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveDeploymentConfigRequiresEnvironmentIdentity(t *testing.T) {
	workspace := writeDeploymentFixture(t, "qa", "lke")
	writeTestFile(t, filepath.Join(workspace, "cloud_env", "qa", "environment.env"), "CLOUD_STACK_NAME=video-cloud-qa\n")
	_, err := resolveDeploymentConfig(workspace, "qa", "")
	if err == nil || !strings.Contains(err.Error(), "CLOUD_DNS_ROOT_DOMAIN is required") {
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

func TestResolveDeploymentConfigRejectsProviderKeyInEnvironment(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	appendFile(t, filepath.Join(workspace, "cloud_env", "dev", "environment.env"), "LKE_REGION=us-sea\n")
	if _, err := resolveDeploymentConfig(workspace, "dev", ""); err == nil || !strings.Contains(err.Error(), "unknown environment key LKE_REGION") {
		t.Fatalf("got %v", err)
	}
}

func TestResolveDeploymentConfigRejectsUnknownLocationAndUnsatisfiedShape(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	writeTestFile(t, filepath.Join(workspace, "cloud_env", "dev", "environment.env"), "CLOUD_STACK_NAME=video-cloud-dev\nCLOUD_DNS_ROOT_DOMAIN=example.test\nDEPLOYMENT_LOCATION=moon\n")
	if _, err := resolveDeploymentConfig(workspace, "dev", ""); err == nil || !strings.Contains(err.Error(), "no region mapping") {
		t.Fatalf("unknown location got %v", err)
	}
	writeTestFile(t, filepath.Join(workspace, "cloud_env", "dev", "environment.env"), "CLOUD_STACK_NAME=video-cloud-dev\nCLOUD_DNS_ROOT_DOMAIN=example.test\nDEPLOYMENT_LOCATION=us-west\n")
	writeTestFile(t, filepath.Join(workspace, "cloud_env", "dev", "overrides", "architecture.env"), "NODE_CLASS_GENERAL_MIN_VCPU=100\n")
	if _, err := resolveDeploymentConfig(workspace, "dev", ""); err == nil || !strings.Contains(err.Error(), "no Linode type satisfies") {
		t.Fatalf("unsatisfied shape got %v", err)
	}
}

func TestResolveDeploymentConfigRejectsNonPositiveNodeIntent(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	writeTestFile(t, filepath.Join(workspace, "cloud_env", "dev", "overrides", "architecture.env"), "NODE_CLASS_BROKER_MIN_VCPU=0\n")
	if _, err := resolveDeploymentConfig(workspace, "dev", ""); err == nil || !strings.Contains(err.Error(), "must be a positive integer") {
		t.Fatalf("got %v", err)
	}
}

func TestSelectLKENodeTypeDeterministicAndValidatesPins(t *testing.T) {
	for _, tc := range []struct {
		cpu, memory int
		want        string
	}{
		{cpu: 4, memory: 8, want: "g6-standard-4"},
		{cpu: 4, memory: 16, want: "g6-standard-6"},
		{cpu: 8, memory: 16, want: "g6-standard-8"},
	} {
		got, err := selectLKENodeType(tc.cpu, tc.memory, "")
		if err != nil || got != tc.want {
			t.Fatalf("select %d/%d = %q, %v; want %q", tc.cpu, tc.memory, got, err, tc.want)
		}
	}
	if _, err := selectLKENodeType(8, 16, "g6-standard-4"); err == nil || !strings.Contains(err.Error(), "below required") {
		t.Fatalf("undersized pin got %v", err)
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
	if strings.Contains(resolved, "LKE_") || strings.Contains(resolved, "LINODE_") {
		t.Fatalf("resolved generic config leaked compatibility keys:\n%s", resolved)
	}
	compat := readTestFile(t, filepath.Join(cfg.RuntimeRoot, "env", "stack.env"))
	for _, want := range []string{"CLOUD_PROVIDER=lke", "CAPACITY_TARGET_CONNECTIONS=1000", "MQTT_EFFECTIVE_REPLICAS=2"} {
		if !strings.Contains(compat, want) {
			t.Fatalf("missing %s", want)
		}
	}
	if strings.Contains(compat, "LKE_") {
		t.Fatalf("shared stack contains adapter compatibility keys:\n%s", compat)
	}
	resources := readTestFile(t, filepath.Join(cfg.RuntimeRoot, "adapters", "lke", "resolved-resources.env"))
	for _, want := range []string{"LKE_REGION=us-sea", "LKE_GENERAL_NODE_TYPE=g6-standard-4", "LKE_BROKER_NODE_TYPE=g6-standard-4", "LKE_DATABASE_NODE_TYPE=g6-standard-8"} {
		if !strings.Contains(resources, want) {
			t.Fatalf("adapter resources missing %s:\n%s", want, resources)
		}
	}
	adapterConfig := readTestFile(t, filepath.Join(cfg.RuntimeRoot, "adapters", "lke", "config.env"))
	for _, want := range []string{"LKE_TARGET_CONNECTS=1000", "LKE_MQTT_REPLICAS=2", "LKE_REGION=us-sea", "LKE_GENERAL_NODE_TYPE=g6-standard-4"} {
		if !strings.Contains(adapterConfig, want) {
			t.Fatalf("adapter config missing %s", want)
		}
	}
}

func TestLKEAccountStateRequiredOnlyForMutation(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := materializeDeploymentRuntime(cfg); err != nil {
		t.Fatal(err)
	}
	if err := validateLKEEnvironmentStateBeforeMutation(cfg); err == nil || !strings.Contains(err.Error(), "provider account state is required") {
		t.Fatalf("missing account state got %v", err)
	}
	writeTestFile(t, filepath.Join(cfg.RuntimeRoot, "adapters", "lke", "account.env"), "LKE_ACTIVE_SERVICE_LIMIT=20\n")
	if err := materializeDeploymentRuntime(cfg); err != nil {
		t.Fatal(err)
	}
	preflight := readTestFile(t, filepath.Join(cfg.RuntimeRoot, "state", "provider-preflight.env"))
	for _, want := range []string{"PROVIDER_ACTIVE_SERVICE_LIMIT=20", "PROVIDER_REGION=us-sea"} {
		if !strings.Contains(preflight, want+"\n") {
			t.Fatalf("provider preflight missing %s: %q", want, preflight)
		}
	}
	adapterConfig := readTestFile(t, filepath.Join(cfg.RuntimeRoot, "adapters", "lke", "config.env"))
	if !strings.Contains(adapterConfig, "LKE_LINODE_ACTIVE_SERVICE_LIMIT=20") {
		t.Fatalf("adapter config missing quota compatibility key:\n%s", adapterConfig)
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

func TestNormalizeEnvironmentArgsUsesLKEDeploymentRootForFeatureQualification(t *testing.T) {
	args, err := normalizeEnvironmentArgs([]string{"test-feature", "--workspace", "/tmp/ws", "--environment", "staging", "--feature", "device-shadow"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"test-feature", "--workspace", "/tmp/ws", "--env-root", "/tmp/ws/cloud_env/staging/lke", "--feature", "device-shadow"}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("normalizeEnvironmentArgs() = %#v, want %#v", args, want)
	}
}

func writeDeploymentFixture(t *testing.T, environment, adapter string) string {
	t.Helper()
	root := t.TempDir()
	var workloads strings.Builder
	for _, spec := range capacityWorkloadRegistry {
		replicas, nodeClass := 1, "general"
		if spec.Name == "mqtt" {
			replicas, nodeClass = 2, "broker"
		} else if spec.Name == "video-cloud-api" {
			replicas = 2
		} else if spec.Name == "postgresql" {
			nodeClass = "database"
		}
		fmt.Fprintf(&workloads, "%s_MIN_REPLICAS=%d\n%s_NODE_CLASS=%s\n%s_REQUEST_CPU=100m\n%s_REQUEST_MEMORY=128Mi\n", spec.Prefix, replicas, spec.Prefix, nodeClass, spec.Prefix, spec.Prefix)
	}
	workloads.WriteString("MQTT_HARD_ANTI_AFFINITY=true\nPOSTGRES_LIMIT_MEMORY=4Gi\nCLOUD_LOGGER_LIMIT_MEMORY=2Gi\nEDGE_REPLICAS=1\nEDGE_MAX_CONNECTIONS=400000\nTURN_REPLICAS=1\nTURN_MIN_PORT=49152\nTURN_MAX_PORT=49200\n")
	files := map[string]string{
		"cloud_deploy/architectures/kubernetes/architecture.env":   "DEPLOYMENT_RUNTIME=kubernetes\nNODE_CLASS_LABEL_KEY=rtk.io/node-class\nDEFAULT_WORKLOAD_NODE_CLASS=general\n",
		"cloud_deploy/architectures/kubernetes/capacity.env":       "CAPACITY_TARGET_CONNECTIONS=1000\nCAPACITY_CONNECTIONS_PER_MQTT_POD=20000\nCAPACITY_ACTIVE_DEVICES=1000\nCAPACITY_ACTIVE_DEVICES_PER_API_POD=40000\nCAPACITY_SYSTEM_RESERVED_CPU_MILLI=1000\nCAPACITY_SYSTEM_RESERVED_MEMORY_MIB=1536\n",
		"cloud_deploy/architectures/kubernetes/topology.env":       "NODE_CLASS_GENERAL_MIN_COUNT=2\nNODE_CLASS_GENERAL_MIN_VCPU=4\nNODE_CLASS_GENERAL_MIN_MEMORY_GIB=8\nNODE_CLASS_BROKER_MIN_COUNT=2\nNODE_CLASS_BROKER_MIN_VCPU=4\nNODE_CLASS_BROKER_MIN_MEMORY_GIB=8\nNODE_CLASS_DATABASE_MIN_COUNT=1\nNODE_CLASS_DATABASE_MIN_VCPU=8\nNODE_CLASS_DATABASE_MIN_MEMORY_GIB=16\n",
		"cloud_deploy/architectures/kubernetes/workloads.env":      workloads.String(),
		"cloud_deploy/adapters/lke/defaults.env":                   "LKE_REGION_PIN=\nLKE_GENERAL_NODE_TYPE_PIN=\nLKE_BROKER_NODE_TYPE_PIN=\nLKE_DATABASE_NODE_TYPE_PIN=\n",
		"cloud_deploy/adapters/lke/locations.env":                  "LOCATION_US_WEST=us-sea\n",
		"cloud_deploy/adapters/lke/schema.env":                     "ADAPTER_NAME=lke\nADAPTER_RUNTIME=kubernetes\nADAPTER_MUTATION_SUPPORTED=true\n",
		"cloud_deploy/adapters/eks/schema.env":                     "ADAPTER_NAME=eks\nADAPTER_RUNTIME=kubernetes\nADAPTER_MUTATION_SUPPORTED=false\n",
		"cloud_deploy/adapters/gke/schema.env":                     "ADAPTER_NAME=gke\nADAPTER_RUNTIME=kubernetes\nADAPTER_MUTATION_SUPPORTED=false\n",
		"cloud_deploy/dns_adapters/godaddy/defaults.env":           "DNS_RECORD_TTL=600\nDNS_PROPAGATION_TIMEOUT_SECONDS=900\nDNS_PROPAGATION_INTERVAL_SECONDS=10\nGODADDY_ENV=prod\n",
		"cloud_deploy/dns_adapters/godaddy/schema.env":             "DNS_ADAPTER_NAME=godaddy\n",
		filepath.Join("cloud_env", environment, "environment.env"): "CLOUD_STACK_NAME=video-cloud-" + environment + "\nCLOUD_DNS_ROOT_DOMAIN=example.test\nDEPLOYMENT_LOCATION=us-west\n",
		filepath.Join("cloud_env", environment, "deployment.env"):  "DEPLOYMENT_ARCHITECTURE=kubernetes\nDEPLOYMENT_ADAPTER=" + adapter + "\nDNS_ADAPTER=godaddy\n",
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
