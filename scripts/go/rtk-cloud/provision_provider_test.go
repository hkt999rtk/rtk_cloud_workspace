package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProvisionProviderRuntimeSelection(t *testing.T) {
	tests := []struct {
		name     string
		provider string
		runtime  provisionRuntime
	}{
		{name: "lke uses kubernetes runtime", provider: "lke", runtime: provisionRuntimeKubernetes},
		{name: "linode is retired before VM runtime dispatch", provider: "linode", runtime: provisionRuntimeKubernetes},
		{name: "k8s reserves kubernetes runtime", provider: "k8s", runtime: provisionRuntimeKubernetes},
		{name: "gke reserves kubernetes runtime", provider: "gke", runtime: provisionRuntimeKubernetes},
		{name: "aks reserves kubernetes runtime", provider: "aks", runtime: provisionRuntimeKubernetes},
		{name: "eks reserves kubernetes runtime", provider: "eks", runtime: provisionRuntimeKubernetes},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			provider, err := newCloudProvider(tc.provider)
			if err != nil {
				t.Fatal(err)
			}
			if provider.Runtime() != tc.runtime {
				t.Fatalf("runtime got %s want %s", provider.Runtime(), tc.runtime)
			}
		})
	}
}

func TestLinodeProviderRetiresVMRuntime(t *testing.T) {
	provider, err := newCloudProvider("linode")
	if err != nil {
		t.Fatal(err)
	}
	err = provider.RunProvision(provisionContext{})
	if !errors.Is(err, errVMRuntimeRetired) {
		t.Fatalf("RunProvision error got %v want errVMRuntimeRetired", err)
	}
	if !strings.Contains(err.Error(), "retired VM runtime") && !strings.Contains(err.Error(), "VM runtime") {
		t.Fatalf("retired error should mention VM runtime, got %v", err)
	}
}

func TestFutureKubernetesProvidersFailFastBeforeMutation(t *testing.T) {
	for _, name := range []string{"k8s", "gke", "aks", "eks"} {
		t.Run(name, func(t *testing.T) {
			provider, err := newCloudProvider(name)
			if err != nil {
				t.Fatal(err)
			}

			err = provider.EnsureKubeAccess(provisionContext{})
			if !errors.Is(err, errProviderUnsupported) {
				t.Fatalf("EnsureKubeAccess error got %v want errProviderUnsupported", err)
			}
			if !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), "not implemented") {
				t.Fatalf("unsupported error should name provider and implementation state, got %v", err)
			}
		})
	}
}

func TestRunProvisionFutureKubernetesProviderFailsBeforeKubectl(t *testing.T) {
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging", "gke")
	if err := os.MkdirAll(filepath.Join(envRoot, "env"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(envRoot, "env", "stack.env"), `CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=gke
CLOUD_REGION=us-central1
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
`)

	err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--apply"})
	if !errors.Is(err, errProviderUnsupported) {
		t.Fatalf("runProvision error got %v want errProviderUnsupported", err)
	}
	if !strings.Contains(err.Error(), "gke") {
		t.Fatalf("unsupported error should mention provider, got %v", err)
	}
}

func TestKubernetesProvisionStepsExposeProviderNeutralOrder(t *testing.T) {
	steps := kubernetesProvisionSteps(lkeCloudProvider{})
	got := make([]string, 0, len(steps))
	for _, step := range steps {
		got = append(got, step.Name)
	}
	want := []string{
		"preflight",
		"plan",
		"dns-adapter-preflight",
		"capacity-check",
		"ensure-kube-access",
		"ensure-lke-node-pool",
		"wait-kube-api-ready",
		"apply-base",
		"deploy-workloads",
		"public-https",
		"write-artifacts",
		"e2e",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("steps got %v want %v", got, want)
	}
}

func TestLKEImagePullSecretApplyDoesNotLogRawToken(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	logPath := fakeKubectl(t)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("LINODE_TOKEN", "")
	t.Setenv("GHCR_PULL_USERNAME", "deploy-bot")
	t.Setenv("GHCR_PULL_TOKEN", "raw-secret-token")

	if err := runProvision([]string{"--workspace", workspace, "--env-root", envRoot, "--apply"}); err != nil {
		t.Fatal(err)
	}
	log := readTestFile(t, logPath)
	if strings.Contains(log, "raw-secret-token") {
		t.Fatalf("kubectl apply log exposed raw GHCR token:\n%s", log)
	}
}
