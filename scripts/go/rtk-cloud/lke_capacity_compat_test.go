package main

import (
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

func testLKECompatibilityCapacityRoundTrip(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	writeTestFile(t, filepath.Join(workspace, "cloud_env/staging/overrides/architecture.env"), "VIDEO_CLOUD_API_REQUEST_CPU=350m\nVIDEO_CLOUD_API_REQUEST_MEMORY=384Mi\nVIDEO_CLOUD_API_MIN_REPLICAS=3\nVIDEO_CLOUD_LOG_INGESTER_REQUEST_CPU=275m\nVIDEO_CLOUD_MQTT_USAGE_REQUEST_CPU=125m\n")
	cfg, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := materializeDeploymentRuntime(cfg); err != nil {
		t.Fatal(err)
	}
	current, err := envroot.Load(cfg.RuntimeRoot, "")
	if err != nil {
		t.Fatal(err)
	}
	current.Values["VIDEO_CLOUD_API_LIMIT_MEMORY"] = "2Gi"
	for _, key := range []string{"VIDEO_CLOUD_LOAD_ADMIN_TOKEN", "FACTORY_PRODUCTION_JWT_SECRET", "LINODE_OBJ_SECRET_ACCESS_KEY", "LKE_NOT_A_RESOURCE_REQUEST_CPU_SECRET", "UNKNOWN_REQUEST_CPU"} {
		current.Values[key] = "must-not-be-written"
	}
	for _, spec := range capacityWorkloadRegistry {
		for _, suffix := range []string{"_REQUEST_CPU", "_REQUEST_MEMORY", "_LIMIT_MEMORY"} {
			t.Setenv(spec.Prefix+suffix, "")
		}
	}
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "capacity-test")
	paths := provisionPaths{EnvRoot: cfg.RuntimeRoot}
	for cycle := 0; cycle < 2; cycle++ {
		if err := writeLKECompatibilityArtifacts(paths, current.Values); err != nil {
			t.Fatal(err)
		}
		current, err = envroot.Load(cfg.RuntimeRoot, "")
		if err != nil {
			t.Fatal(err)
		}
		for key, expected := range cfg.Values {
			if deploymentArchitectureKeys[key] || strings.HasPrefix(key, "NODE_CLASS_") || strings.HasSuffix(key, "_EFFECTIVE_REPLICAS") || key == "DEPLOYMENT_ARCHITECTURE" || key == "DEPLOYMENT_ADAPTER" || key == "DNS_ADAPTER" {
				if current.Values[key] != expected {
					t.Fatalf("cycle %d lost %s: got %q want %q", cycle, key, current.Values[key], expected)
				}
			}
		}
		if strings.Contains(readTestFile(t, filepath.Join(cfg.RuntimeRoot, "env/stack.env")), "must-not-be-written") {
			t.Fatal("capacity projection persisted credentials or an unknown suffix key")
		}
		if lkeWorkloadReplicas(current.Values, lkeWorkload{Key: "video-cloud", Name: "video-cloud-api"}) != "3" {
			t.Fatal("computed API replicas were not retained")
		}
		for _, tc := range []struct{ name, cpu, memory, limit string }{
			{"video-cloud-api", "350m", "384Mi", "2Gi"},
			{"video-cloud-logingester", "275m", "128Mi", "1Gi"},
			{"video-cloud-mqttusage", "125m", "128Mi", "1Gi"},
			{"video-cloud-cleaner", "100m", "128Mi", "128Mi"},
			{"video-cloud-statistics", "100m", "128Mi", "128Mi"},
			{"video-cloud-metricsexporter", "100m", "128Mi", "128Mi"},
			{"video-cloud-turnregistry", "100m", "128Mi", "128Mi"},
		} {
			var manifest string
			if tc.name == "video-cloud-api" {
				manifest = lkeDeploymentManifest(current.Values, lkeWorkload{Key: "video-cloud", Name: tc.name, Namespace: "video-cloud-staging-video-cloud", Image: "video:test", Port: 8080}, nil)
			} else {
				manifest = lkeVideoCloudAuxiliaryDeploymentManifest(current.Values, lkeVideoCloudAuxiliaryService{Name: tc.name, Binary: "fixture"})
			}
			for _, want := range []string{"resources:\n", `cpu: "` + tc.cpu + `"`, `memory: "` + tc.memory + `"`, `memory: "` + tc.limit + `"`} {
				if !strings.Contains(manifest, want) {
					t.Fatalf("cycle %d %s missing %s", cycle, tc.name, want)
				}
			}
		}
		factory := lkeFactoryEnrollDeploymentManifest(current.Values, lkeCertIssuerMaterial{})
		for _, want := range []string{"resources:\n", `cpu: "100m"`, `memory: "128Mi"`} {
			if !strings.Contains(factory, want) {
				t.Fatalf("cycle %d Factory missing authoritative resource budget %s", cycle, want)
			}
		}
		plan, err := lkeCapacityPlan(current.Values, provisionOptions{workloads: []string{"video-cloud"}})
		if err != nil || plan.WorkloadCPU <= 0 || plan.WorkloadMemMi <= 0 {
			t.Fatalf("cycle %d empty reloaded capacity plan: %#v, %v", cycle, plan, err)
		}
	}
	previousCanonical := activeCanonicalSecretStore
	activeCanonicalSecretStore = true
	t.Cleanup(func() { activeCanonicalSecretStore = previousCanonical })
	if err := lkeRequireCanonicalCapacityProfile(current.Values); err != nil {
		t.Fatal(err)
	}
	for _, environment := range []string{"staging", "prod", "production"} {
		for _, missing := range []string{"DEPLOYMENT_ARCHITECTURE", "VIDEO_CLOUD_API_REQUEST_CPU", "VIDEO_CLOUD_LOG_INGESTER_REQUEST_MEMORY", "VIDEO_CLOUD_API_EFFECTIVE_REPLICAS", "NODE_CLASS_BROKER_TOTAL_REQUEST_CPU_MILLI"} {
			broken := appendMap(current.Values, nil)
			broken["CLOUD_ENV_NAME"] = environment
			delete(broken, missing)
			if err := lkeRequireCanonicalCapacityProfile(broken); err == nil || !strings.Contains(err.Error(), "run deployment plan") {
				t.Fatalf("%s missing %s did not require rematerialization: %v", environment, missing, err)
			}
		}
	}
	stale := appendMap(current.Values, nil)
	stale["NODE_CLASS_BROKER_TOTAL_REQUEST_CPU_MILLI"] = "0"
	if err := lkeRequireCanonicalCapacityProfile(stale); err == nil || !strings.Contains(err.Error(), "stale NODE_CLASS_BROKER_TOTAL_REQUEST_CPU_MILLI") {
		t.Fatalf("stale derived broker capacity was accepted: %v", err)
	}
	legacy := map[string]string{"CLOUD_ENV_NAME": "dev"}
	if err := lkeRequireCanonicalCapacityProfile(legacy); err != nil {
		t.Fatal("protected guard unexpectedly changed dev legacy entry")
	}
}
