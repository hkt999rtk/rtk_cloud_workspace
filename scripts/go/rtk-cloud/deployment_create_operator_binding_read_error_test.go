package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestInitialOperatorBindingDistinguishesReadDeadlineFromOwnerDrift(t *testing.T) {
	for _, mode := range []string{"expired", "cancelled", "backend-timeout", "absent", "wrong-owner", "binding-drift", "retained"} {
		t.Run(mode, func(t *testing.T) {
			const owner = "11111111-1111-4111-8111-111111111111"
			const user = "22222222-2222-4222-8222-222222222222"
			store := makeIsolatedTestSecretStore(t, "dev")
			cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}}
			e := &environmentCreateExecution{Store: store, Journal: environmentCreateJournal{Environment: "dev", PlanSHA256: strings.Repeat("a", 64)}}
			e.Journal.Entries = []environmentCreateJournalEntry{{Key: "kubernetes/account-bootstrap", State: "completed", Receipt: mustProviderRecoveryJSON(map[string]any{"owners": map[string]string{"Deployment/video-cloud-dev-account-manager/account-manager": owner}})}}
			operator := map[string]string{"ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL": "operator@example.test", "PKI_OPERATOR_USER_ID": "33333333-3333-4333-8333-333333333333"}
			binding := initialOperatorBinding{Schema: "rtk-cloud.initial-operator-binding/v1", Parent: e.Journal.PlanSHA256, UserID: user, AccountUID: owner, EmailSHA: environmentPreflightBytesSHA256([]byte(operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"])), OriginalConfiguredSHA: environmentPreflightBytesSHA256([]byte(operator["PKI_OPERATOR_USER_ID"]))}
			if mode == "binding-drift" {
				binding.AccountUID = "44444444-4444-4444-8444-444444444444"
			}
			relative := filepath.Join("deployment/create/material", e.Journal.PlanSHA256, "operator-binding.json")
			raw := mustProviderRecoveryJSON(binding)
			if err := writeEnvironmentCreateDerivedPrivateFile(store, relative, raw); err != nil {
				t.Fatal(err)
			}
			before := deploymentplan.Digest([]any{e.Journal, operator})
			dir := t.TempDir()
			calls := filepath.Join(dir, "calls.txt")
			script := `#!/bin/sh
set -eu
printf 'call\n' >> "$RTK_OPERATOR_READ_TEST_CALLS"
case "$*" in
 *'get Deployment account-manager'*)
  case "$RTK_OPERATOR_READ_TEST_MODE" in
   backend-timeout) printf '%s\n' 'private-command-canary: request deadline exceeded; credential=must-not-escape' >&2; exit 1 ;;
   absent) exit 0 ;;
   wrong-owner) uid=55555555-5555-4555-8555-555555555555 ;;
   *) uid=11111111-1111-4111-8111-111111111111 ;;
  esac
  printf '{"apiVersion":"apps/v1","kind":"Deployment","metadata":{"name":"account-manager","namespace":"video-cloud-dev-account-manager","uid":"%s"}}' "$uid" ;;
 *'SELECT id FROM users'*) printf 22222222-2222-4222-8222-222222222222 ;;
 *) exit 73 ;;
esac
`
			if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_OPERATOR_READ_TEST_CALLS", calls)
			t.Setenv("RTK_OPERATOR_READ_TEST_MODE", mode)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if mode == "expired" {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			} else if mode == "cancelled" {
				cancel()
			}
			defer cancel()
			got, recorded, err := resolveInitialOperatorBinding(ctx, cfg, store, e, environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-test-kubeconfig")}, operator)
			if mode == "retained" {
				if err != nil || !recorded || deploymentplan.Digest(got) != deploymentplan.Digest(binding) {
					t.Fatal("valid original operator/owner binding changed", err)
				}
			} else {
				if err == nil {
					t.Fatal("failed owner read or ownership drift was admitted")
				}
				if strings.Contains(err.Error(), "private-command-canary") || strings.Contains(err.Error(), "must-not-escape") || strings.Contains(err.Error(), "credential=") {
					t.Fatal("native private diagnostic leaked")
				}
				switch mode {
				case "expired", "cancelled", "backend-timeout":
					code := "KUBE_TIMEOUT"
					if mode == "cancelled" {
						code = "CHECK_CANCELLED"
					}
					var blocker *deploymentplan.Blocker
					if !errors.As(err, &blocker) || blocker.ID != "deployment.access.resource" || blocker.Resource != "Deployment/video-cloud-dev-account-manager/account-manager" || !strings.Contains(blocker.Cause, "["+code+"]") || strings.Contains(err.Error(), "owner unavailable") {
						t.Fatal("read deadline/cancellation was misreported as missing original owner", err)
					}
				case "absent":
					if !strings.Contains(err.Error(), "original initial Account Manager owner unavailable") {
						t.Fatal("actual absence lost its original refusal", err)
					}
				case "wrong-owner":
					if !strings.Contains(err.Error(), "original accepted Account Manager owner") {
						t.Fatal("wrong UID lost its original refusal", err)
					}
				case "binding-drift":
					if !strings.Contains(err.Error(), "recorded generation operator binding changed") {
						t.Fatal("recorded operator binding drift was accepted", err)
					}
				}
			}
			if mode == "expired" || mode == "cancelled" {
				if _, err := os.Lstat(calls); !os.IsNotExist(err) {
					t.Fatal("expired/cancelled owner read started a native command")
				}
			}
			after, readErr := readBoundedEnvironmentFile(filepath.Join(store.Root, relative), true)
			if readErr != nil || string(after) != string(raw) || deploymentplan.Digest([]any{e.Journal, operator}) != before {
				t.Fatal("operator inspection mutated original binding, journal or operator", readErr)
			}
		})
	}
}
