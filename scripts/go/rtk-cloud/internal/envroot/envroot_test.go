package envroot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveAndPaths(t *testing.T) {
	workspace := t.TempDir()
	defaultRoot, err := Resolve(workspace, "")
	if err != nil {
		t.Fatal(err)
	}
	expectedDefault := filepath.Join(workspace, "cloud_env", "staging", "linode")
	if defaultRoot != expectedDefault {
		t.Fatalf("default root got %s want %s", defaultRoot, expectedDefault)
	}
	custom := filepath.Join(workspace, "custom", "env-root")
	if err := os.MkdirAll(custom, 0o755); err != nil {
		t.Fatal(err)
	}
	override, err := Resolve(workspace, custom)
	if err != nil {
		t.Fatal(err)
	}
	if override != custom {
		t.Fatalf("override got %s want %s", override, custom)
	}
	staging := filepath.Join(workspace, "cloud_env", "staging")
	if err := os.MkdirAll(filepath.Join(staging, "linode", "services"), 0o755); err != nil {
		t.Fatal(err)
	}
	stagingRoot, err := Resolve(workspace, staging)
	if err != nil {
		t.Fatal(err)
	}
	if stagingRoot != filepath.Join(staging, "linode") {
		t.Fatalf("staging root got %s", stagingRoot)
	}
	paths := NewPaths(defaultRoot)
	if paths.OperatorEnv != filepath.Join(expectedDefault, "env", "operator.env") {
		t.Fatalf("operator env got %s", paths.OperatorEnv)
	}
	if paths.StackEnv != filepath.Join(expectedDefault, "env", "stack.env") {
		t.Fatalf("stack env got %s", paths.StackEnv)
	}
	if paths.VideoConfig != filepath.Join(expectedDefault, "topology", "video-cloud-staging.yaml") {
		t.Fatalf("video config got %s", paths.VideoConfig)
	}
	if paths.TestDevicesDir != filepath.Join(expectedDefault, "devices", "test_device") {
		t.Fatalf("test devices dir got %s", paths.TestDevicesDir)
	}
}

func TestResolveStagingRootUsesLKEProvider(t *testing.T) {
	workspace := t.TempDir()
	staging := filepath.Join(workspace, "cloud_env", "staging")
	mkdir(t, filepath.Join(staging, "lke", "env"))
	write(t, filepath.Join(staging, "lke", "env", "stack.env"), `CLOUD_PROVIDER=lke
`)

	root, err := Resolve(workspace, staging)
	if err != nil {
		t.Fatal(err)
	}
	if root != filepath.Join(staging, "lke") {
		t.Fatalf("staging root got %s", root)
	}

	t.Setenv("CLOUD_PROVIDER", "linode")
	root, err = Resolve(workspace, staging)
	if err != nil {
		t.Fatal(err)
	}
	if root != filepath.Join(staging, "linode") {
		t.Fatalf("env override root got %s", root)
	}
}

func TestLoadAndValidate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "metadata", "staging", "linode")
	mkdir(t, filepath.Join(root, "env"))
	mkdir(t, filepath.Join(root, "topology"))
	mkdir(t, filepath.Join(root, "services", "account-manager"))
	mkdir(t, filepath.Join(root, "services", "cloud-admin"))
	mkdir(t, filepath.Join(root, "services", "cloud-logger"))
	write(t, filepath.Join(root, "env", "stack.env"), `CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=linode
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
CLOUD_STACK_NAME=video-cloud-staging
VIDEO_CLOUD_DOMAIN=video-cloud-staging.realtekconnect.com
VIDEO_CLOUD_CERTISSUER_DOMAIN=certissuer.video-cloud-staging.realtekconnect.com
ACCOUNT_MANAGER_DOMAIN=account-manager.video-cloud-staging.realtekconnect.com
CLOUD_ADMIN_DOMAIN=admin.video-cloud-staging.realtekconnect.com
VIDEO_CLOUD_LABEL_PREFIX=video-cloud-staging
VIDEO_CLOUD_VPC_LABEL=video-cloud-staging-vpc
VIDEO_CLOUD_SUBNET_LABEL=video-cloud-staging-subnet
ACCOUNT_MANAGER_LINODE_LABEL=rtk-account-manager-staging
ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=rtk-account-manager-staging-fw
ADMIN_LINODE_LABEL=rtk-cloud-admin-staging
CLOUD_LOGGER_DOMAIN=logger.video-cloud-staging.realtekconnect.com
CLOUD_LOGGER_LINODE_LABEL=rtk-cloud-logger-staging
CLOUD_LOGGER_LINODE_FIREWALL_LABEL=rtk-cloud-logger-staging-fw
ADMIN_LINODE_FIREWALL_LABEL=rtk-cloud-admin-staging-fw
`)
	write(t, filepath.Join(root, "topology", "video-cloud-staging.yaml"), `stack: video-cloud-staging
region: us-sea
vpc:
  label: video-cloud-staging-vpc
  subnet:
    label: video-cloud-staging-subnet
instances:
  edge:
    label: video-cloud-staging-edge
    letsencrypt:
      domain: video-cloud-staging.realtekconnect.com
  api:
    label: video-cloud-staging-api
  infra:
    label: video-cloud-staging-infra
  mqtt:
    label: video-cloud-staging-mqtt
  coturn:
    label: video-cloud-staging-coturn
deploy:
  certissuer_domain: certissuer.video-cloud-staging.realtekconnect.com
`)
	write(t, filepath.Join(root, "services", "account-manager", "account-manager-public-staging.env"), `ACCOUNT_MANAGER_LINODE_LABEL=rtk-account-manager-staging
ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=rtk-account-manager-staging-fw
ACCOUNT_MANAGER_LINODE_DOMAIN=account-manager.video-cloud-staging.realtekconnect.com
`)
	write(t, filepath.Join(root, "services", "cloud-admin", "admin-staging.env"), `ADMIN_LINODE_LABEL=rtk-cloud-admin-staging
ADMIN_LINODE_FIREWALL_LABEL=rtk-cloud-admin-staging-fw
ADMIN_LINODE_DOMAIN=admin.video-cloud-staging.realtekconnect.com
`)
	write(t, filepath.Join(root, "services", "cloud-logger", "logger.env"), `CLOUD_LOGGER_LINODE_LABEL=rtk-cloud-logger-staging
CLOUD_LOGGER_LINODE_FIREWALL_LABEL=rtk-cloud-logger-staging-fw
CLOUD_LOGGER_DOMAIN=logger.video-cloud-staging.realtekconnect.com
`)
	env, err := Load(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if env.Values["CLOUD_STACK_NAME"] != "video-cloud-staging" {
		t.Fatalf("stack name got %s", env.Values["CLOUD_STACK_NAME"])
	}
	if err := Validate(root, env); err != nil {
		t.Fatal(err)
	}
	good, err := os.ReadFile(filepath.Join(root, "services", "account-manager", "account-manager-public-staging.env"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "services", "account-manager", "account-manager-public-staging.env"), strings.ReplaceAll(string(good), "account-manager.video-cloud-staging.realtekconnect.com", "account-manager-mismatch.realtekconnect.com"))
	badEnv, err := Load(root, "")
	if err != nil {
		t.Fatal(err)
	}
	err = Validate(root, badEnv)
	if err == nil || !strings.Contains(err.Error(), "Account Manager domain mismatch") {
		t.Fatalf("expected Account Manager domain mismatch, got %v", err)
	}
}

func TestLoadAcceptsLKEProvider(t *testing.T) {
	root := filepath.Join(t.TempDir(), "metadata", "staging", "lke")
	mkdir(t, filepath.Join(root, "env"))
	write(t, filepath.Join(root, "env", "stack.env"), `CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=lke
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
`)
	env, err := Load(root, "")
	if err != nil {
		t.Fatal(err)
	}
	if env.Values["CLOUD_PROVIDER"] != "lke" {
		t.Fatalf("provider got %s", env.Values["CLOUD_PROVIDER"])
	}
	if env.Values["CLOUD_STACK_NAME"] != "video-cloud-staging" {
		t.Fatalf("stack got %s", env.Values["CLOUD_STACK_NAME"])
	}
}

func TestDeriveStackValuesFromEnvName(t *testing.T) {
	values := Derive(map[string]string{
		"CLOUD_ENV_NAME":        "stg",
		"CLOUD_PROVIDER":        "linode",
		"CLOUD_REGION":          "us-sea",
		"CLOUD_DNS_ROOT_DOMAIN": "realtekconnect.com",
	})
	want := map[string]string{
		"CLOUD_STACK_NAME":                      "video-cloud-stg",
		"VIDEO_CLOUD_DOMAIN":                    "video-cloud-stg.realtekconnect.com",
		"VIDEO_CLOUD_CERTISSUER_DOMAIN":         "certissuer.video-cloud-stg.realtekconnect.com",
		"ACCOUNT_MANAGER_DOMAIN":                "account-manager.video-cloud-stg.realtekconnect.com",
		"CLOUD_ADMIN_DOMAIN":                    "admin.video-cloud-stg.realtekconnect.com",
		"CLOUD_LOGGER_DOMAIN":                   "logger.video-cloud-stg.realtekconnect.com",
		"VIDEO_CLOUD_LABEL_PREFIX":              "video-cloud-stg",
		"VIDEO_CLOUD_VPC_LABEL":                 "video-cloud-stg-vpc",
		"VIDEO_CLOUD_SUBNET_LABEL":              "video-cloud-stg-subnet",
		"ACCOUNT_MANAGER_LINODE_LABEL":          "rtk-account-manager-stg",
		"ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL": "rtk-account-manager-stg-fw",
		"ADMIN_LINODE_LABEL":                    "rtk-cloud-admin-stg",
		"ADMIN_LINODE_FIREWALL_LABEL":           "rtk-cloud-admin-stg-fw",
		"CLOUD_LOGGER_LINODE_LABEL":             "rtk-cloud-logger-stg",
		"CLOUD_LOGGER_LINODE_FIREWALL_LABEL":    "rtk-cloud-logger-stg-fw",
	}
	for key, expected := range want {
		if values[key] != expected {
			t.Fatalf("%s got %q want %q", key, values[key], expected)
		}
	}
}

func TestLoadRejectsGeneratedMismatch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "metadata", "staging", "linode")
	mkdir(t, filepath.Join(root, "env"))
	write(t, filepath.Join(root, "env", "stack.env"), `CLOUD_ENV_NAME=stg
CLOUD_PROVIDER=linode
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
CLOUD_STACK_NAME=video-cloud-stg-0529
`)
	_, err := Load(root, "")
	if err == nil || !strings.Contains(err.Error(), "sync-env --env-root") {
		t.Fatalf("expected sync-env mismatch error, got %v", err)
	}
}

func TestLoadRejectsUnsupportedProvider(t *testing.T) {
	root := filepath.Join(t.TempDir(), "metadata", "staging", "aws")
	mkdir(t, filepath.Join(root, "env"))
	write(t, filepath.Join(root, "env", "stack.env"), `CLOUD_ENV_NAME=staging
CLOUD_PROVIDER=aws
CLOUD_REGION=us-east-1
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
`)
	_, err := Load(root, "")
	if err == nil || !strings.Contains(err.Error(), "unsupported CLOUD_PROVIDER=aws") {
		t.Fatalf("expected unsupported provider error, got %v", err)
	}
}

func mkdir(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
