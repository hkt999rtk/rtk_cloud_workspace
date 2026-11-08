package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateCustodyBootstrapReconcilesConfigurationAndRetainsOriginalScope(t *testing.T) {
	for _, mode := range []string{"valid", "lost-response", "wrong-type", "bad-readback"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, backend, e, _ := createOpenBaoNativeFixture(t, "valid")
			cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
			if err := store.write("openbao/root-token", []byte("fixture-custody-token\n"), false); err != nil {
				t.Fatal(err)
			}
			binding := &environmentCustodyBinding{Generation: e.Journal.PlanSHA256, Owners: []environmentCustodyOwnerBinding{{ID: "api", Namespace: "video-cloud-dev-video-cloud", ServiceAccount: "video-cloud-api"}, {ID: "account-manager", Namespace: "video-cloud-dev-account-manager", ServiceAccount: "account-manager"}}}
			dir := t.TempDir()
			frames := []byte(`{"data":{"cas_required":true}}` + "\n")
			for _, owner := range binding.Owners {
				policy, err := environmentCustodyPolicy(cfg.Values, cfg.Environment, *binding, owner)
				if err != nil {
					t.Fatal(err)
				}
				for _, data := range []any{map[string]any{"policy": policy}, map[string]any{"bound_service_account_names": []string{owner.ServiceAccount}, "bound_service_account_namespaces": []string{owner.Namespace}, "token_policies": []string{environmentCustodyRoleName(cfg.Environment + "/" + binding.Generation + "/" + owner.ID)}, "audience": "openbao", "token_no_default_policy": true, "token_ttl": 900, "token_max_ttl": 900}} {
					raw, _ := json.Marshal(map[string]any{"data": data})
					frames = append(frames, append(raw, '\n')...)
				}
			}
			if mode == "bad-readback" {
				frames = []byte(strings.Replace(string(frames), `"cas_required":true`, `"cas_required":false`, 1))
			}
			writeTestFile(t, filepath.Join(dir, "frames"), string(frames))
			tool := filepath.Join(dir, "kubectl")
			writeTestFile(t, tool, `#!/bin/sh
set -eu
IFS= read -r supplied
[ "$supplied" = fixture-custody-token ]
case "$*" in
 *'bao secrets list -format=json'*)
  if [ "$RTK_CUSTODY_TEST_MODE" = wrong-type ]; then printf '{"identities/":{"type":"pki"}}';
  elif [ -f "$RTK_CUSTODY_TEST_DIR/mounted" ]; then printf '{"identities/":{"type":"kv","options":{"version":"2"}}}'; else printf '{}'; fi;;
 *'bao policy write '*)
  printf 'write\n' >>"$RTK_CUSTODY_TEST_DIR/calls"
  touch "$RTK_CUSTODY_TEST_DIR/mounted"
  if [ "$RTK_CUSTODY_TEST_MODE" = lost-response ]; then exit 1; fi;;
 *'bao read -format=json identities/config'*) printf 'read\n' >>"$RTK_CUSTODY_TEST_DIR/calls"; cat "$RTK_CUSTODY_TEST_DIR/frames";;
 *) exit 2;;
esac
`)
			if err := os.Chmod(tool, 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("RTK_CLOUD_KUBECTL", tool)
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_CUSTODY_TEST_DIR", dir)
			t.Setenv("RTK_CUSTODY_TEST_MODE", mode)
			runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, Backend: backend, OpenBao: environmentCreateOpenBaoBootstrap{RootTokenPath: filepath.Join(store.Root, "openbao/root-token")}}
			err := configureEnvironmentCreateCustody(context.Background(), cfg, runtime, binding)
			valid := mode == "valid" || mode == "lost-response"
			if valid != (err == nil) {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if !valid {
				return
			}
			before := deploymentplan.Digest(e.Journal)
			if err = configureEnvironmentCreateCustody(context.Background(), cfg, runtime, binding); err != nil || before != deploymentplan.Digest(e.Journal) {
				t.Fatal("completed custody setup was not reused read-only", err)
			}
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			if strings.Count(string(calls), "write\n") != 1 || strings.Contains(string(calls), "fixture-custody-token") {
				t.Fatal("configuration replay mutated a completed scope or exposed credentials")
			}
			cfg.Values["PKI_STATE_KV_MOUNT"] = "other-identities"
			if err = configureEnvironmentCreateCustody(context.Background(), cfg, runtime, binding); err == nil {
				t.Fatal("existing generation custody scope was silently changed")
			}
		})
	}
}

func TestCreateCustodyBindingUsesCompleteMaintainedWorkloadInventory(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			cfg := deploymentConfig{Environment: environment, Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + environment, "PKI_STATE_BACKEND": "openbao", "PKI_STATE_KV_MOUNT": "identities", "PKI_STATE_LEASE_DURATION": "30s"}}
			for _, flag := range []string{"MQTT_FOUNDATION_REGISTRATION_ENABLED", "SHADOW_WORKER_REGISTRATION_ENABLED", "OTA_SERVICE_REGISTRATION_ENABLED", "VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED", "WEBRTC_SERVICE_REGISTRATION_ENABLED", "LOGGER_SERVICE_REGISTRATION_ENABLED"} {
				cfg.Values[flag] = "true"
			}
			products, err := environmentCreateProductIdentityIntent(cfg)
			if err != nil {
				t.Fatal(err)
			}
			p := environmentCreateBootstrapPlan{Environment: environment, Stack: cfg.Values["CLOUD_STACK_NAME"], ProductIdentities: products}
			before := deploymentplan.Digest([]any{cfg, p})
			binding, err := createEnvironmentCustodyBinding(cfg, p, strings.Repeat("a", 64), "")
			if err != nil {
				t.Fatal(err)
			}
			if len(binding.Owners) != 14 {
				t.Fatal("maintained permanent identity consumers are missing or duplicated", len(binding.Owners))
			}
			owners := map[string]environmentCustodyOwnerBinding{}
			aliases := map[string]bool{}
			for _, owner := range binding.Owners {
				if owner.ID == "openbao" || owner.ID == "openbao-pki-identity" || owner.ServiceAccount == "default" {
					t.Fatal("independent OpenBao startup or shared default account became a KV consumer")
				}
				if _, err = environmentCustodyPolicy(cfg.Values, environment, *binding, owner); err != nil {
					t.Fatal(err)
				}
				owners[owner.ID] = owner
				for _, alias := range owner.StorageAliases {
					if aliases[alias] || alias == "mqtt-pki-data" {
						t.Fatal("business storage or duplicate state was assigned to custody")
					}
					aliases[alias] = true
				}
			}
			if len(owners["mqtt-pki"].StorageAliases) != 2 || len(owners["video-cloud-logingester"].StorageAliases) != 2 || owners["account-manager"].Namespace != p.Stack+"-account-manager" {
				t.Fatal("multiple purposes or namespace ownership was lost")
			}
			if before != deploymentplan.Digest([]any{cfg, p}) {
				t.Fatal("custody producer changed environment settings or qualified intent")
			}
			p.ProductIdentities[0].Claim = "mqtt-pki-data"
			if _, err = createEnvironmentCustodyBinding(cfg, p, strings.Repeat("a", 64), ""); err == nil {
				t.Fatal("unqualified product storage mapping accepted")
			}
		})
	}
}
