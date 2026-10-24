package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"
)

// A completed role is immutable execution evidence: reuse must issue only a
// native read, preserve the original journal, and reject widened live bindings.
func TestCreateOpenBaoCompletedRoleReusesOnlyExactNativeRead(t *testing.T) {
	for _, mode := range []string{"valid", "ttl", "max-ttl", "policy", "audience", "account", "namespace", "default-policy", "pending", "receipt", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, backend, e, _ := createOpenBaoNativeFixture(t, "valid")
			tokenRel := "openbao/role-test-token"
			if err := store.write(tokenRel, []byte("private-role-fixture-token"), false); err != nil {
				t.Fatal(err)
			}
			accounts, policies := []string{"certissuer"}, []string{"create-device-signer", "create-service-signer"}
			name := "certissuer-dev"
			intent := deploymentplan.Object{"role": name, "namespace": "video-cloud-dev-video-cloud", "service_accounts": accounts, "policies": policies, "audience": "openbao", "ttl": "15m", "no_default_policy": true}
			key := "openbao-role-" + name + "-" + deploymentplan.Digest(intent)[:12]
			body, _ := json.Marshal(intent)
			if err := e.BeginEntry(key, "bootstrap", body, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if mode != "pending" {
				receipt := body
				if mode == "receipt" {
					receipt = []byte(`{"role":"foreign"}`)
				}
				if err := e.RecordReceipt(key, receipt); err != nil {
					t.Fatal(err)
				}
				if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "duplicate" {
				e.Journal.Entries = append(e.Journal.Entries, e.Journal.Entries[len(e.Journal.Entries)-1])
			}
			before := deploymentplan.Digest(e.Journal)
			data := deploymentplan.Object{"bound_service_account_names": accounts, "bound_service_account_namespaces": []string{"video-cloud-dev-video-cloud"}, "token_policies": policies, "audience": "openbao", "token_no_default_policy": true, "token_ttl": 900, "token_max_ttl": 900}
			switch mode {
			case "ttl":
				data["token_ttl"] = 1800
			case "max-ttl":
				data["token_max_ttl"] = 1800
			case "policy":
				data["token_policies"] = []string{"root"}
			case "audience":
				data["audience"] = "foreign"
			case "account":
				data["bound_service_account_names"] = []string{"*"}
			case "namespace":
				data["bound_service_account_namespaces"] = []string{"*"}
			case "default-policy":
				data["token_no_default_policy"] = false
			}
			dir := t.TempDir()
			response, _ := json.Marshal(map[string]any{"data": data})
			os.WriteFile(filepath.Join(dir, "response"), response, 0600)
			log := filepath.Join(dir, "calls")
			script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$RTK_ROLE_TEST_LOG"
case "$*" in
 *'bao write '*) exit 99;;
 *'bao read -format=json auth/kubernetes/role/certissuer-dev'*) cat >/dev/null;cat "$RTK_ROLE_TEST_RESPONSE";;
 *) exit 98;;
esac
`
			tool := filepath.Join(dir, "kubectl")
			os.WriteFile(tool, []byte(script), 0700)
			t.Setenv("RTK_CLOUD_KUBECTL", tool)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_ROLE_TEST_LOG", log)
			t.Setenv("RTK_ROLE_TEST_RESPONSE", filepath.Join(dir, "response"))
			rt := environmentCreateBootstrapRuntime{Execution: e, Backend: backend, OpenBao: environmentCreateOpenBaoBootstrap{RootTokenPath: filepath.Join(store.Root, tokenRel)}}
			err := configureEnvironmentCreateOpenBaoRole(context.Background(), cfg, rt, name, accounts, policies)
			if (mode == "valid") != (err == nil) {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if before != deploymentplan.Digest(e.Journal) {
				t.Fatal("reuse changed original execution evidence")
			}
			calls, _ := os.ReadFile(log)
			if strings.Contains(string(calls), "bao write") || strings.Contains(string(calls), "private-role-fixture-token") {
				t.Fatal("reuse wrote role or exposed credential")
			}
			if mode == "pending" || mode == "receipt" || mode == "duplicate" {
				if len(calls) != 0 {
					t.Fatal("unverified original role was queried")
				}
			} else if !strings.Contains(string(calls), "bao read") {
				t.Fatal("native read missing")
			}
		})
	}
}
