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

func TestLoadAndValidate(t *testing.T) {
	root := filepath.Join(t.TempDir(), "metadata", "staging", "linode")
	mkdir(t, filepath.Join(root, "env"))
	mkdir(t, filepath.Join(root, "topology"))
	mkdir(t, filepath.Join(root, "services", "account-manager"))
	mkdir(t, filepath.Join(root, "services", "cloud-admin"))
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
ADMIN_LINODE_FIREWALL_LABEL=rtk-cloud-admin-staging-firewall
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
ADMIN_LINODE_FIREWALL_LABEL=rtk-cloud-admin-staging-firewall
ADMIN_LINODE_DOMAIN=admin.video-cloud-staging.realtekconnect.com
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
	good, err := os.ReadFile(filepath.Join(root, "env", "stack.env"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(root, "env", "stack.env"), strings.ReplaceAll(string(good), "account-manager.video-cloud-staging.realtekconnect.com", "account-manager-mismatch.realtekconnect.com"))
	badEnv, err := Load(root, "")
	if err != nil {
		t.Fatal(err)
	}
	err = Validate(root, badEnv)
	if err == nil || !strings.Contains(err.Error(), "Account Manager domain mismatch") {
		t.Fatalf("expected Account Manager domain mismatch, got %v", err)
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
