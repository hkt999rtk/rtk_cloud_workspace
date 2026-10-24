package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedLatePostNativePolicyAbsenceRequiresSuccessfulNativeList(t *testing.T) {
	name := "rtk-dev-service-bootstrap-11111111-1111-4111-8111-111111111111"
	for _, mode := range []string{"absent", "present", "denied", "transport", "malformed", "null", "empty", "object", "duplicate", "trailing", "foreign-name", "no-token", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, _, _ := createStateFixture(t, "dev")
			bin := t.TempDir()
			body, exit := `["default","root","another-original-policy"]`, "0"
			switch mode {
			case "present":
				body = `["default","root","` + name + `"]`
			case "denied":
				exit = "43" // Even success-looking stdout cannot prove absence.
			case "transport":
				exit, body = "91", ""
			case "malformed":
				body = `["default",`
			case "null":
				body = `null`
			case "empty":
				body = `[]`
			case "object":
				body = `{"errors":["private-response-canary"]}`
			case "duplicate":
				body = `["default","root","default"]`
			case "trailing":
				body += ` {"private":"response-canary"}`
			}
			if err := os.WriteFile(filepath.Join(bin, "response"), []byte(body), 0600); err != nil {
				t.Fatal(err)
			}
			calls := filepath.Join(bin, "calls")
			script := `#!/bin/sh
case "$*" in
*'bao policy list -format=json'*)
  cat >/dev/null
  printf list > "$RTK_TEST_POST_NATIVE_POLICY_CALLS"
  cat "$RTK_TEST_POST_NATIVE_POLICY_RESPONSE"
  printf private-stderr-canary >&2
  exit "$RTK_TEST_POST_NATIVE_POLICY_EXIT" ;;
*) exit 92 ;;
esac
`
			if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_TEST_POST_NATIVE_POLICY_CALLS", calls)
			t.Setenv("RTK_TEST_POST_NATIVE_POLICY_RESPONSE", filepath.Join(bin, "response"))
			t.Setenv("RTK_TEST_POST_NATIVE_POLICY_EXIT", exit)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			selected, token := name, []byte("private-token-canary")
			if mode == "foreign-name" {
				selected = "root"
			} else if mode == "no-token" {
				token = nil
			} else if mode == "cancelled" {
				cancel()
			}
			err := verifyManagedLatePostNativePolicyAbsent(ctx, cfg, environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-policy-kubeconfig")}, token, selected)
			if (err == nil) != (mode == "absent") || err != nil && strings.Contains(err.Error(), "canary") {
				t.Fatal("failed native read counted as policy absence or leaked diagnostics", mode, err)
			}
			_, callErr := os.Lstat(calls)
			if (mode == "foreign-name" || mode == "no-token" || mode == "cancelled") && !os.IsNotExist(callErr) {
				t.Fatal("invalid policy request reached native command")
			}
		})
	}
}

func TestManagedLatePostNativeAuthenticationRejectsUnprovedMarkerWithoutNativeCalls(t *testing.T) {
	for _, sid := range []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"} {
		t.Run(sid, func(t *testing.T) {
			cfg, store, _, _ := createStateFixture(t, "dev")
			r := &createServiceSessionRecovery{Intent: createServiceSessionIntent{SessionID: sid}, ManagedLateSession: &createManagedLateSessionRecovery{NewSessionID: "22222222-2222-4222-8222-222222222222"}, ManagedLateRemaining: &createManagedLateRemaining{PostNativeCleanup: &createManagedLatePostNativeCleanup{}}}
			before := deploymentplan.Digest(r)
			err := verifyManagedLatePostNativeAuthenticationSource(context.Background(), cfg, store, nil, environmentKubernetesBackend{}, r, "rtk-dev-service-bootstrap-22222222-2222-4222-8222-222222222222")
			if err == nil || !strings.Contains(err.Error(), "post-native-cleanup-current-source") || before != deploymentplan.Digest(r) {
				t.Fatal("unproved original or ACK intent selected cleanup semantics", err)
			}
		})
	}
}

func TestServiceSessionAuthenticationKeepsActivePolicyAndSharedRoleChecks(t *testing.T) {
	for _, mode := range []string{"original", "active-policy-absent", "wrong-account", "wrong-namespace", "wrong-policy", "wrong-audience", "wrong-ttl", "wrong-max-ttl", "default-policy"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, operator, artifact := createStateFixture(t, "dev")
			e, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operator, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { e.Close() })
			sid, signer := "11111111-1111-4111-8111-111111111111", "original-exact-session-signer-policy"
			name, role := "rtk-dev-service-bootstrap-"+sid, "pki-service-bootstrap-dev"
			r := &createServiceSessionRecovery{Intent: createServiceSessionIntent{SessionID: sid}, Ceremony: environmentCreateServiceCeremony{Intermediate: environmentCreateRegisteredIssuer{ID: "22222222-2222-4222-8222-222222222222"}}}
			roleIntent := deploymentplan.Object{"role": role, "namespace": cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud", "service_accounts": []string{"pki-service-bootstrap"}, "policies": []string{name}, "audience": "openbao", "ttl": "15m", "no_default_policy": true}
			e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "openbao-policy-" + name, State: "completed", Receipt: mustProviderRecoveryJSON(deploymentplan.Object{"environment": "dev", "policy": name, "policy_sha256": environmentPreflightBytesSHA256([]byte(signer))})}, environmentCreateJournalEntry{Key: "openbao-role-" + role + "-" + deploymentplan.Digest(roleIntent)[:12], State: "completed", Receipt: mustProviderRecoveryJSON(roleIntent)})
			if err := e.save(); err != nil {
				t.Fatal(err)
			}
			if err := store.write("openbao/root-token", []byte("private-root-token-canary"), false); err != nil {
				t.Fatal(err)
			}
			data := deploymentplan.Object{"bound_service_account_names": []string{"pki-service-bootstrap"}, "bound_service_account_namespaces": []string{cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud"}, "token_policies": []string{name}, "audience": "openbao", "token_ttl": 900, "token_max_ttl": 900, "token_no_default_policy": true}
			switch mode {
			case "wrong-account":
				data["bound_service_account_names"] = []string{"*"}
			case "wrong-namespace":
				data["bound_service_account_namespaces"] = []string{"*"}
			case "wrong-policy":
				data["token_policies"] = []string{"root"}
			case "wrong-audience":
				data["audience"] = "other"
			case "wrong-ttl":
				data["token_ttl"] = 901
			case "wrong-max-ttl":
				data["token_max_ttl"] = 901
			case "default-policy":
				data["token_no_default_policy"] = false
			}
			bin := t.TempDir()
			writeTestFile(t, filepath.Join(bin, "native"), string(mustProviderRecoveryJSON(environmentCreateOpenBaoIssuerPolicies{IssuerID: r.Ceremony.Intermediate.ID, Environment: "dev", ServiceClientSignerPolicy: signer})))
			writeTestFile(t, filepath.Join(bin, "role"), string(mustProviderRecoveryJSON(deploymentplan.Object{"data": data})))
			policy := signer
			if mode == "active-policy-absent" {
				policy = ""
			}
			writeTestFile(t, filepath.Join(bin, "policy"), policy)
			script := `#!/bin/sh
case "$*" in
*'render-openbao-policy'*) cat "$RTK_TEST_ACTIVE_AUTH_DIR/native" ;;
*'bao policy read '*) cat >/dev/null; cat "$RTK_TEST_ACTIVE_AUTH_DIR/policy" ;;
*'bao read -format=json auth/kubernetes/role/'*) cat >/dev/null; cat "$RTK_TEST_ACTIVE_AUTH_DIR/role" ;;
*) exit 92 ;;
esac
`
			if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_TEST_ACTIVE_AUTH_DIR", bin)
			err = verifyServiceSessionAuthentication(context.Background(), cfg, store, e, environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-auth-kubeconfig")}, r)
			if (err == nil) != (mode == "original") || err != nil && strings.Contains(err.Error(), "canary") {
				t.Fatal("ordinary policy/shared role predicate changed", mode, err)
			}
		})
	}
}
