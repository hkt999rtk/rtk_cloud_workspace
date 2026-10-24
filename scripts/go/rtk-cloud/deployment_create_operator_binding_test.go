package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestInitialOperatorDiscoveryPreservesOldConfigurationAndPinsNewGeneration(t *testing.T) {
	const original = "11111111-1111-4111-8111-111111111111"
	const native = "22222222-2222-4222-8222-222222222222"
	for _, mode := range []string{"fresh", "retained", "changed-user", "wrong-owner", "existing-issuer", "no-admin", "multiple-admins"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}}
			e := &environmentCreateExecution{Store: store, Journal: environmentCreateJournal{Environment: "dev", PlanSHA256: strings.Repeat("a", 64)}}
			e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "kubernetes/account-bootstrap", State: "completed", Receipt: mustProviderRecoveryJSON(map[string]any{"owners": map[string]string{"Deployment/video-cloud-dev-account-manager/account-manager": "original-account-owner"}})})
			operator := map[string]string{"ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL": "operator@example.test", "PKI_OPERATOR_USER_ID": original}
			binding := initialOperatorBinding{Schema: "rtk-cloud.initial-operator-binding/v1", Parent: e.Journal.PlanSHA256, UserID: native, AccountUID: "original-account-owner", EmailSHA: environmentPreflightBytesSHA256([]byte(operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"])), OriginalConfiguredSHA: environmentPreflightBytesSHA256([]byte(original))}
			if mode == "retained" || mode == "changed-user" {
				if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create/material", e.Journal.PlanSHA256, "operator-binding.json"), mustProviderRecoveryJSON(binding)); err != nil {
					t.Fatal(err)
				}
			}
			dir := t.TempDir()
			binary := filepath.Join(dir, "kubectl")
			script := `#!/bin/sh
set -eu
case "$*" in
 *'get Deployment account-manager'*)
  uid=original-account-owner
  if [ "$RTK_BINDING_TEST_MODE" = wrong-owner ]; then uid=replacement; fi
  printf '{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"account-manager","namespace":"video-cloud-dev-account-manager","uid":"%s"}}' "$uid" ;;
 *'pki_issuers'*) if [ "$RTK_BINDING_TEST_MODE" = existing-issuer ]; then printf 1;else printf 0;fi ;;
 *'SELECT id FROM users'*)
  case "$RTK_BINDING_TEST_MODE" in
   changed-user) printf 33333333-3333-4333-8333-333333333333 ;;
   no-admin) : ;;
   multiple-admins) printf '22222222-2222-4222-8222-222222222222\n33333333-3333-4333-8333-333333333333' ;;
   *) printf 22222222-2222-4222-8222-222222222222 ;;
  esac ;;
 *) exit 2 ;;
esac
`
			if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("RTK_CLOUD_KUBECTL", binary)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_BINDING_TEST_MODE", mode)
			got, recorded, err := resolveInitialOperatorBinding(context.Background(), cfg, store, e, environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-test-kubeconfig")}, operator)
			if mode == "fresh" || mode == "retained" {
				if err != nil || got.UserID != native || recorded != (mode == "retained") || operator["PKI_OPERATOR_USER_ID"] != original {
					t.Fatal("new native binding lost original configuration or identity", err)
				}
				if mode == "fresh" {
					if _, err := os.Lstat(filepath.Join(store.Root, "deployment/create/material", e.Journal.PlanSHA256, "operator-binding.json")); !os.IsNotExist(err) {
						t.Fatal("preflight created a binding")
					}
				}
			} else if err == nil {
				t.Fatal("unsafe generation binding accepted", mode)
			}
		})
	}
}
