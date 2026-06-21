package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestK8SWorkloadsRegistryIncludesServiceImages(t *testing.T) {
	env := k8sWorkloadTestEnv()
	workloads := k8sWorkloads(env)

	got := map[string]string{}
	for _, workload := range workloads {
		got[workload.Key] = workload.EnvKey
	}
	want := map[string]string{
		"video-cloud":     "LKE_VIDEO_CLOUD_IMAGE",
		"account-manager": "LKE_ACCOUNT_MANAGER_IMAGE",
		"cloud-admin":     "LKE_CLOUD_ADMIN_IMAGE",
		"frontend":        "LKE_FRONTEND_IMAGE",
		"cloud-logger":    "LKE_CLOUD_LOGGER_IMAGE",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("workload image env keys got %#v want %#v", got, want)
	}
}

func TestK8SSelectedWorkloadsMatchLKESelectionModes(t *testing.T) {
	env := k8sWorkloadTestEnv()
	tests := []struct {
		name string
		opts provisionOptions
		want []string
	}{
		{
			name: "default",
			want: []string{"video-cloud", "account-manager", "cloud-admin", "frontend", "cloud-logger"},
		},
		{
			name: "video only",
			opts: provisionOptions{videoOnly: true},
			want: []string{"video-cloud", "cloud-logger"},
		},
		{
			name: "logger only",
			opts: provisionOptions{loggerOnly: true},
			want: []string{"cloud-logger"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := workloadKeys(k8sSelectedWorkloads(env, tc.opts))
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("selected workload keys got %#v want %#v", got, tc.want)
			}
		})
	}
}

func TestK8SMissingDeployImageWorkloadsUsesRegistryEnvKeys(t *testing.T) {
	env := k8sWorkloadTestEnv()
	env["LKE_VIDEO_CLOUD_IMAGE"] = "registry.example.test/video:test"
	env["LKE_CLOUD_LOGGER_IMAGE"] = "registry.example.test/logger:test"

	missing := k8sMissingDeployImageWorkloads(env, provisionOptions{videoOnly: true})
	if len(missing) != 0 {
		t.Fatalf("video-only deploy missing got %#v want none", workloadKeys(missing))
	}

	missing = k8sMissingDeployImageWorkloads(env, provisionOptions{})
	if got, want := workloadKeys(missing), []string{"account-manager", "cloud-admin", "frontend"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("default deploy missing got %#v want %#v", got, want)
	}
}

func TestK8SPrometheusTargetsAreDerivedFromWorkloadRegistry(t *testing.T) {
	env := k8sWorkloadTestEnv()
	targets := k8sPrometheusTargets(env, provisionOptions{videoOnly: true})

	got := map[string]string{}
	for _, target := range targets {
		got[target.Job] = target.Service
	}
	checks := map[string]string{
		"video-cloud-api":              "video-cloud-api",
		"video-cloud-metrics-exporter": "video-cloud-metricsexporter",
		"video-cloud-logingester":      "video-cloud-logingester",
		"video-cloud-mqttusage":        "video-cloud-mqttusage",
		"video-cloud-factoryenroll":    "factoryenroll",
		"redis-exporter":               "redis-exporter",
		"video-cloud-prometheus":       "video-cloud-prometheus",
		"video-cloud-grafana":          "video-cloud-grafana",
	}
	for job, service := range checks {
		if got[job] != service {
			t.Fatalf("target %s service got %q want %q; all targets %#v", job, got[job], service, got)
		}
	}
	if _, ok := got["account-manager"]; ok {
		t.Fatalf("video-only prometheus targets should not include account-manager: %#v", got)
	}
}

func TestK8SPrometheusConfigKeepsExistingTargets(t *testing.T) {
	manifest := lkeVideoCloudPrometheusConfigManifest(k8sWorkloadTestEnv(), provisionOptions{})
	for _, want := range []string{
		"job_name: video-cloud-api",
		"job_name: account-manager",
		"job_name: cloud-admin",
		"job_name: frontend",
		"job_name: video-cloud-metrics-exporter",
		"job_name: video-cloud-prometheus",
		"job_name: video-cloud-grafana",
	} {
		if !strings.Contains(manifest, want) {
			t.Fatalf("prometheus config missing %q:\n%s", want, manifest)
		}
	}
}

func TestK8SRolloutTargetsUseRegistryTimeout(t *testing.T) {
	env := k8sWorkloadTestEnv()
	targets := k8sRolloutTargets(k8sSelectedWorkloads(env, provisionOptions{videoOnly: true}), "7m")

	got := []lkeRolloutTarget{}
	for _, target := range targets {
		if target.Resource == "deployment/cloud-logger" {
			t.Fatalf("cloud logger rollout is managed by logger apply path, got %#v", targets)
		}
		got = append(got, target)
	}
	want := []lkeRolloutTarget{{
		Namespace: "video-cloud-staging-video-cloud",
		Resource:  "deployment/video-cloud-api",
		Timeout:   "7m",
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rollout targets got %#v want %#v", got, want)
	}
}

func TestK8SRolloutTargetsFromEnvUseRegistryTimeoutKey(t *testing.T) {
	env := k8sWorkloadTestEnv()
	t.Setenv("LKE_WORKLOAD_ROLLOUT_TIMEOUT", "9m")

	targets := k8sRolloutTargetsFromEnv(k8sSelectedWorkloads(env, provisionOptions{videoOnly: true}))
	if len(targets) != 1 {
		t.Fatalf("rollout target count got %d want 1: %#v", len(targets), targets)
	}
	if targets[0].Timeout != "9m" {
		t.Fatalf("rollout timeout got %q want 9m", targets[0].Timeout)
	}
}

func k8sWorkloadTestEnv() map[string]string {
	return map[string]string{
		"CLOUD_STACK_NAME":          "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN":        "video-cloud-staging.realtekconnect.com",
		"ACCOUNT_MANAGER_DOMAIN":    "account-manager.video-cloud-staging.realtekconnect.com",
		"CLOUD_ADMIN_DOMAIN":        "admin.video-cloud-staging.realtekconnect.com",
		"CLOUD_LOGGER_DOMAIN":       "logger.video-cloud-staging.realtekconnect.com",
		"LKE_ACCOUNT_MANAGER_IMAGE": "",
		"LKE_CLOUD_ADMIN_IMAGE":     "",
		"LKE_FRONTEND_IMAGE":        "",
		"LKE_VIDEO_CLOUD_IMAGE":     "",
		"LKE_CLOUD_LOGGER_IMAGE":    "",
	}
}

func workloadKeys(workloads []k8sWorkload) []string {
	keys := make([]string, 0, len(workloads))
	for _, workload := range workloads {
		keys = append(keys, workload.Key)
	}
	return keys
}
