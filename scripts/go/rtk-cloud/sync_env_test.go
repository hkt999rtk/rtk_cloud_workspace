package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncEnvCheckFailsWhenGeneratedFilesDrift(t *testing.T) {
	workspace, envRoot := writeSyncEnvFixture(t)
	err := run([]string{"sync-env", "--workspace", workspace, "--env-root", envRoot, "--check"})
	if err == nil || !strings.Contains(err.Error(), "environment metadata is not synchronized") {
		t.Fatalf("expected sync-env --check drift error, got %v", err)
	}
}

func TestSyncEnvRewritesDerivedMetadata(t *testing.T) {
	workspace, envRoot := writeSyncEnvFixture(t)
	if err := run([]string{"sync-env", "--workspace", workspace, "--env-root", envRoot}); err != nil {
		t.Fatalf("sync-env returned error: %v", err)
	}
	if err := run([]string{"sync-env", "--workspace", workspace, "--env-root", envRoot, "--check"}); err != nil {
		t.Fatalf("sync-env --check after sync returned error: %v", err)
	}

	resolved := filepath.Join(envRoot, "linode")
	stack := readFile(t, filepath.Join(resolved, "env", "stack.env"))
	for _, want := range []string{
		"CLOUD_ENV_NAME=stg",
		"CLOUD_STACK_NAME=video-cloud-stg",
		"VIDEO_CLOUD_DOMAIN=video-cloud-stg.realtekconnect.com",
		"ACCOUNT_MANAGER_LINODE_LABEL=rtk-account-manager-stg",
		"ADMIN_LINODE_FIREWALL_LABEL=rtk-cloud-admin-stg-fw",
		"CLOUD_LOGGER_LINODE_FIREWALL_LABEL=rtk-cloud-logger-stg-fw",
	} {
		if !strings.Contains(stack, want) {
			t.Fatalf("stack.env missing %q:\n%s", want, stack)
		}
	}
	topology := readFile(t, filepath.Join(resolved, "topology", "video-cloud-staging.yaml"))
	for _, want := range []string{
		"stack: video-cloud-stg",
		"label: video-cloud-stg-vpc",
		"label: video-cloud-stg-edge",
		"domain: video-cloud-stg.realtekconnect.com",
		"certissuer_domain: certissuer.video-cloud-stg.realtekconnect.com",
	} {
		if !strings.Contains(topology, want) {
			t.Fatalf("topology missing %q:\n%s", want, topology)
		}
	}
	account := readFile(t, filepath.Join(resolved, "services", "account-manager", "account-manager-public-staging.env"))
	for _, want := range []string{
		"ACCOUNT_MANAGER_LINODE_LABEL=rtk-account-manager-stg",
		"ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=rtk-account-manager-stg-fw",
		"ACCOUNT_MANAGER_LINODE_DOMAIN=account-manager.video-cloud-stg.realtekconnect.com",
		"APP_CERT_ISSUER_BASE_URL=https://certissuer.video-cloud-stg.realtekconnect.com",
	} {
		if !strings.Contains(account, want) {
			t.Fatalf("account env missing %q:\n%s", want, account)
		}
	}
	admin := readFile(t, filepath.Join(resolved, "services", "cloud-admin", "admin-staging.env"))
	for _, want := range []string{
		"ACCOUNT_MANAGER_BASE_URL=https://account-manager.video-cloud-stg.realtekconnect.com",
		"ADMIN_LINODE_LABEL=rtk-cloud-admin-stg",
		"ADMIN_LINODE_FIREWALL_LABEL=rtk-cloud-admin-stg-fw",
		"ADMIN_LINODE_DOMAIN=admin.video-cloud-stg.realtekconnect.com",
		"VIDEO_CLOUD_BASE_URL=https://video-cloud-stg.realtekconnect.com",
	} {
		if !strings.Contains(admin, want) {
			t.Fatalf("admin env missing %q:\n%s", want, admin)
		}
	}
	logger := readFile(t, filepath.Join(resolved, "services", "cloud-logger", "logger.env"))
	for _, want := range []string{
		"CLOUD_LOGGER_ENDPOINT=https://logger.video-cloud-stg.realtekconnect.com",
		"CLOUD_LOGGER_LINODE_LABEL=rtk-cloud-logger-stg",
		"CLOUD_LOGGER_LINODE_FIREWALL_LABEL=rtk-cloud-logger-stg-fw",
		"CLOUD_LOGGER_DOMAIN=logger.video-cloud-stg.realtekconnect.com",
	} {
		if !strings.Contains(logger, want) {
			t.Fatalf("logger env missing %q:\n%s", want, logger)
		}
	}
}

func TestSyncEnvRewritesLegacySlugAfterStackEnvWasSynced(t *testing.T) {
	workspace, envRoot := writeSyncEnvFixture(t)
	resolved := filepath.Join(envRoot, "linode")
	writeFile(t, filepath.Join(resolved, "env", "stack.env"), `CLOUD_ENV_NAME=stg
CLOUD_PROVIDER=linode
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
CLOUD_STACK_NAME=video-cloud-stg
VIDEO_CLOUD_DOMAIN=video-cloud-stg.realtekconnect.com
VIDEO_CLOUD_CERTISSUER_DOMAIN=certissuer.video-cloud-stg.realtekconnect.com
ACCOUNT_MANAGER_DOMAIN=account-manager.video-cloud-stg.realtekconnect.com
CLOUD_ADMIN_DOMAIN=admin.video-cloud-stg.realtekconnect.com
CLOUD_LOGGER_DOMAIN=logger.video-cloud-stg.realtekconnect.com
VIDEO_CLOUD_LABEL_PREFIX=video-cloud-stg
VIDEO_CLOUD_VPC_LABEL=video-cloud-stg-vpc
VIDEO_CLOUD_SUBNET_LABEL=video-cloud-stg-subnet
ACCOUNT_MANAGER_LINODE_LABEL=rtk-account-manager-stg
ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=rtk-account-manager-stg-fw
ADMIN_LINODE_LABEL=rtk-cloud-admin-stg
ADMIN_LINODE_FIREWALL_LABEL=rtk-cloud-admin-stg-fw
CLOUD_LOGGER_LINODE_LABEL=rtk-cloud-logger-stg
CLOUD_LOGGER_LINODE_FIREWALL_LABEL=rtk-cloud-logger-stg-fw
`)
	if err := run([]string{"sync-env", "--workspace", workspace, "--env-root", envRoot, "--check"}); err == nil {
		t.Fatal("expected sync-env --check to fail when service env still contains legacy stg-0529 URLs")
	}
	if err := run([]string{"sync-env", "--workspace", workspace, "--env-root", envRoot}); err != nil {
		t.Fatalf("sync-env returned error: %v", err)
	}
	account := readFile(t, filepath.Join(resolved, "services", "account-manager", "account-manager-public-staging.env"))
	if !strings.Contains(account, "APP_CERT_ISSUER_BASE_URL=https://certissuer.video-cloud-stg.realtekconnect.com") {
		t.Fatalf("legacy app cert issuer URL was not rewritten:\n%s", account)
	}
}

func writeSyncEnvFixture(t *testing.T) (string, string) {
	t.Helper()
	workspace := t.TempDir()
	envRoot := filepath.Join(workspace, "cloud_env", "staging")
	resolved := filepath.Join(envRoot, "linode")
	mkdirAll(t, filepath.Join(resolved, "env"))
	mkdirAll(t, filepath.Join(resolved, "topology"))
	mkdirAll(t, filepath.Join(resolved, "services", "account-manager"))
	mkdirAll(t, filepath.Join(resolved, "services", "cloud-admin"))
	mkdirAll(t, filepath.Join(resolved, "services", "cloud-logger"))
	writeFile(t, filepath.Join(resolved, "env", "stack.env"), `CLOUD_ENV_NAME=stg
CLOUD_PROVIDER=linode
CLOUD_REGION=us-sea
CLOUD_DNS_ROOT_DOMAIN=realtekconnect.com
CLOUD_STACK_NAME=video-cloud-stg-0529
VIDEO_CLOUD_DOMAIN=video-cloud-stg-0529.realtekconnect.com
VIDEO_CLOUD_CERTISSUER_DOMAIN=certissuer.video-cloud-stg-0529.realtekconnect.com
ACCOUNT_MANAGER_DOMAIN=account-manager.video-cloud-stg-0529.realtekconnect.com
CLOUD_ADMIN_DOMAIN=admin.video-cloud-stg-0529.realtekconnect.com
VIDEO_CLOUD_LABEL_PREFIX=video-cloud-stg-0529
VIDEO_CLOUD_VPC_LABEL=video-cloud-stg-0529-vpc
VIDEO_CLOUD_SUBNET_LABEL=video-cloud-stg-0529-subnet
ACCOUNT_MANAGER_LINODE_LABEL=rtk-account-manager-stg-0529
ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=rtk-account-manager-stg-0529-fw
ADMIN_LINODE_LABEL=rtk-cloud-admin-stg-0529
ADMIN_LINODE_FIREWALL_LABEL=rtk-cloud-admin-stg-0529-fw
CLOUD_LOGGER_DOMAIN=logger.video-cloud-stg-0529.realtekconnect.com
CLOUD_LOGGER_LINODE_LABEL=rtk-cloud-logger-stg-0529
CLOUD_LOGGER_LINODE_FIREWALL_LABEL=rtk-cloud-logger-stg-0529-fw
`)
	writeFile(t, filepath.Join(resolved, "topology", "video-cloud-staging.yaml"), `stack: video-cloud-stg-0529
region: us-sea
vpc:
  label: video-cloud-stg-0529-vpc
  subnet:
    label: video-cloud-stg-0529-subnet
instances:
  edge:
    label: video-cloud-stg-0529-edge
    letsencrypt:
      domain: video-cloud-stg-0529.realtekconnect.com
  api:
    label: video-cloud-stg-0529-api
  infra:
    label: video-cloud-stg-0529-infra
  mqtt:
    label: video-cloud-stg-0529-mqtt
  coturn:
    label: video-cloud-stg-0529-coturn
deploy:
  certissuer_domain: certissuer.video-cloud-stg-0529.realtekconnect.com
`)
	writeFile(t, filepath.Join(resolved, "services", "account-manager", "account-manager-public-staging.env"), `ACCOUNT_MANAGER_LINODE_LABEL=rtk-account-manager-stg-0529
ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL=rtk-account-manager-stg-0529-fw
ACCOUNT_MANAGER_LINODE_DOMAIN=account-manager.video-cloud-stg-0529.realtekconnect.com
APP_CERT_ISSUER_BASE_URL=https://certissuer.video-cloud-stg-0529.realtekconnect.com
`)
	writeFile(t, filepath.Join(resolved, "services", "cloud-admin", "admin-staging.env"), `ADMIN_LINODE_LABEL=rtk-cloud-admin-stg-0529
ADMIN_LINODE_FIREWALL_LABEL=rtk-cloud-admin-stg-0529-fw
ADMIN_LINODE_DOMAIN=admin.video-cloud-stg-0529.realtekconnect.com
ACCOUNT_MANAGER_BASE_URL=https://account-manager.video-cloud-stg-0529.realtekconnect.com
VIDEO_CLOUD_BASE_URL=https://video-cloud-stg-0529.realtekconnect.com
`)
	writeFile(t, filepath.Join(resolved, "services", "cloud-logger", "logger.env"), `CLOUD_LOGGER_LINODE_LABEL=rtk-cloud-logger-stg-0529
CLOUD_LOGGER_LINODE_FIREWALL_LABEL=rtk-cloud-logger-stg-0529-fw
CLOUD_LOGGER_DOMAIN=logger.video-cloud-stg-0529.realtekconnect.com
CLOUD_LOGGER_ENDPOINT=https://logger.video-cloud-stg-0529.realtekconnect.com
`)
	return workspace, envRoot
}
