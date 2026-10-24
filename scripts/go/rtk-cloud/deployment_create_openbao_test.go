package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestFreshCreateOpenBaoFoundationUsesRealFileSingleWriterAndTLS(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			values, _, release := completeEnvironmentRenderFixture(environment)
			values["OPENBAO_STORAGE_BACKEND"], values["OPENBAO_EFFECTIVE_REPLICAS"] = "file", "1"
			values["OPENBAO_DATA_STORAGE"], values["OPENBAO_AUDIT_STORAGE"] = "10Gi", "5Gi"
			values["OPENBAO_POD_MANAGEMENT_POLICY"], values["OPENBAO_UPDATE_STRATEGY"] = "OrderedReady", "OnDelete"
			values["DEPLOYMENT_PROTECTION_PROFILE"] = "standard"
			cfg := deploymentConfig{Environment: environment, Adapter: "lke", Values: values}
			definitions, err := renderEnvironmentCreateOpenBaoFoundation(cfg, release, "initial-openbao-tls", "initial-openbao-ca")
			if environment == "prod" {
				if err == nil {
					t.Fatal("production received a single-writer bootstrap")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var set, config deploymentplan.Object
			for _, definition := range definitions {
				if definition.Object["kind"] == "StatefulSet" {
					set = definition.Object
				}
				if definition.Object["kind"] == "ConfigMap" {
					config = definition.Object
				}
			}
			pod := environmentPKIPod(set)
			main := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
			if envObjectReplicas(set) != 1 || len(certIssuerObjectList(pod["containers"])) != 1 || main["image"] != release.Images["LKE_OPENBAO_IMAGE"].Reference {
				t.Fatal("OpenBao writer or formal image differs from maintained intent")
			}
			if deploymentplan.Map(main["readinessProbe"])["tcpSocket"] == nil {
				t.Fatal("initialization deadlocked behind unseal readiness")
			}
			serverName := ""
			for _, value := range certIssuerObjectList(main["env"]) {
				entry := deploymentplan.Map(value)
				if entry["name"] == "BAO_TLS_SERVER_NAME" {
					serverName = deploymentplan.String(entry["value"])
				}
			}
			if !createProviderContains(lkeOpenBaoDNSNames(values), serverName) {
				t.Fatal("initial TLS client name is not one of the issued transport certificate DNS identities")
			}
			hcl := deploymentplan.String(deploymentplan.Map(config["data"])["file.hcl"])
			if !strings.Contains(hcl, "storage \"file\"") || !strings.Contains(hcl, "audit \"file\" \"deployment-audit\"") || !strings.Contains(hcl, "log_raw = \"false\"") || !strings.Contains(hcl, "file_path = \"/openbao/audit/audit.jsonl\"") || strings.Contains(hcl, "unsafe_allow_api_audit_creation") || strings.Contains(hcl, "root/generate") || !strings.Contains(hcl, "tls_min_version = \"tls12\"") {
				t.Fatal("unsupported config or unreviewed CA generation")
			}
			if len(certIssuerObjectList(deploymentplan.Map(set["spec"])["volumeClaimTemplates"])) != 2 {
				t.Fatal("data/audit durable claims missing")
			}
			if _, err := observeOpenBaoProfile(set, []deploymentplan.Object{set, config}); err != nil {
				t.Fatal("fresh file profile cannot be consumed by shared compiler", err)
			}
		})
	}
}

func createOpenBaoNativeFixture(t *testing.T, mode string) (deploymentConfig, secretStore, environmentKubernetesBackend, *environmentCreateExecution, string) {
	t.Helper()
	cfg, store, operatorSHA, artifact := createStateFixture(t, "dev")
	execution, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operatorSHA, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { execution.Close() })
	cfg.Values = appendMap(cfg.Values, map[string]string{"DEPLOYMENT_PROTECTION_PROFILE": "standard"})
	caRel := filepath.Join("deployment", "create", "material", execution.Journal.PlanSHA256, "openbao-transport", "ca.crt")
	if err := writeEnvironmentCreateDerivedPrivateFile(store, caRel, []byte("private-fixture-ca")); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	log := filepath.Join(directory, "calls")
	initOut, _ := json.Marshal(map[string]any{"unseal_keys_b64": []string{base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 32)))}, "root_token": "private-native-root-token"})
	writeTestFile(t, filepath.Join(directory, "init.json"), string(initOut))
	script := `#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$RTK_CREATE_BAO_LOG"
case "$*" in
  *'get StatefulSet openbao'*) printf '{"apiVersion":"apps/v1","kind":"StatefulSet","metadata":{"name":"openbao","namespace":"video-cloud-dev-secrets","uid":"stateful-owner","resourceVersion":"1","labels":{"rtk.realtek.com/stack":"video-cloud-dev"}}}' ;;
  *'get Pod openbao-0'*)
    if [ "$RTK_CREATE_BAO_MODE" = wrong-pod ]; then owner=foreign; else owner=stateful-owner; fi
    printf '{"apiVersion":"v1","kind":"Pod","metadata":{"name":"openbao-0","namespace":"video-cloud-dev-secrets","uid":"pod-owner","resourceVersion":"1","ownerReferences":[{"kind":"StatefulSet","name":"openbao","uid":"%s","controller":true}]}}' "$owner" ;;
  *'bao status -format=json'*)
    if [ "$RTK_CREATE_BAO_MODE" = invalid-status ]; then printf '{}';
    elif [ -e "$RTK_CREATE_BAO_DIRECTORY/unsealed" ]; then printf '{"initialized":true,"sealed":false}';
    elif [ "$RTK_CREATE_BAO_MODE" = already-initialized ] || [ -e "$RTK_CREATE_BAO_DIRECTORY/initialized" ]; then printf '{"initialized":true,"sealed":true}';
    else printf '{"initialized":false,"sealed":true}'; fi ;;
  *'bao operator init '*)
    touch "$RTK_CREATE_BAO_DIRECTORY/initialized"
    if [ "$RTK_CREATE_BAO_MODE" = init-response-lost ]; then exit 1; fi
    cat "$RTK_CREATE_BAO_DIRECTORY/init.json" ;;
  *'bao write -format=json sys/unseal -'*)
    cat >/dev/null
    test -s "$RTK_CREATE_BAO_STORE/openbao/init.json"
    test -s "$RTK_CREATE_BAO_STORE/openbao/root-token"
    test -s "$RTK_CREATE_BAO_STORE/openbao/unseal-key"
    touch "$RTK_CREATE_BAO_DIRECTORY/unsealed"
    printf '{"initialized":true,"sealed":false}' ;;
  *'bao audit list -format=json'*) cat >/dev/null;printf '{"deployment-audit/":{"type":"file","options":{"file_path":"/openbao/audit/audit.jsonl","log_raw":"false"}}}' ;;
  *'bao auth list -format=json'*) cat >/dev/null;printf '{"kubernetes/":{"type":"kubernetes"}}' ;;
  *'bao read -format=json auth/kubernetes/config'*) cat >/dev/null;printf '{"data":{"kubernetes_host":"https://kubernetes.default.svc:443","disable_local_ca_jwt":false,"token_reviewer_jwt_set":false}}' ;;
  *'bao auth enable -path=kubernetes'*|*'bao write auth/kubernetes/config'*) cat >/dev/null ;;
  *) exit 2 ;;
esac
`
	writeTestFile(t, filepath.Join(directory, "kubectl"), script)
	os.Chmod(filepath.Join(directory, "kubectl"), 0755)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_CREATE_BAO_LOG", log)
	t.Setenv("RTK_CREATE_BAO_MODE", mode)
	t.Setenv("RTK_CREATE_BAO_DIRECTORY", directory)
	t.Setenv("RTK_CREATE_BAO_STORE", store.Root)
	return cfg, store, environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("private-frozen-test-kubeconfig")}, execution, log
}

func TestFreshCreateOpenBaoInitializationPersistsBeforeUnsealAndRetainsUnknownOutcomes(t *testing.T) {
	for _, mode := range []string{"valid", "retained-root", "already-initialized", "wrong-pod", "invalid-status", "init-response-lost"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, backend, execution, log := createOpenBaoNativeFixture(t, mode)
			if mode == "retained-root" {
				if err := store.write("openbao/root-token", []byte("original-token"), false); err != nil {
					t.Fatal(err)
				}
			}
			result, err := initializeEnvironmentCreateOpenBao(context.Background(), cfg, store, backend, execution)
			if mode == "valid" {
				if err != nil || result.RootTokenPath != filepath.Join(store.Root, "openbao", "root-token") || result.URL != "https://openbao.video-cloud-dev-secrets.svc:8200" {
					t.Fatal("native OpenBao initialization failed", err)
				}
				for _, key := range []string{"openbao-initialize", "openbao-unseal", "openbao-audit-enable", "openbao-auth-enable", "openbao-auth-config"} {
					found := false
					for _, entry := range execution.Journal.Entries {
						if entry.Key == key {
							found = entry.State == "completed" && len(entry.Receipt) > 0
						}
					}
					if !found {
						t.Fatal("actual initialization/auth evidence missing", key)
					}
				}
				for _, relative := range []string{"openbao/init.json", "openbao/root-token", "openbao/unseal-key"} {
					info, err := os.Lstat(filepath.Join(store.Root, relative))
					if err != nil || info.Mode().Perm() != 0600 {
						t.Fatal("unsafe initial operator material", err)
					}
				}
			} else if err == nil {
				t.Fatal("unknown or original OpenBao identity accepted")
			}
			calls, _ := os.ReadFile(log)
			if strings.Contains(string(calls), "private-native-root-token") || strings.Contains(string(calls), "private-frozen-test-kubeconfig") {
				t.Fatal("private credential entered subprocess arguments")
			}
			if mode != "valid" && mode != "init-response-lost" && strings.Contains(string(calls), "bao operator init") {
				t.Fatal("existing or unverifiable owner was initialized")
			}
			if mode == "init-response-lost" {
				if len(execution.Journal.Entries) != 1 || execution.Journal.Entries[0].State != "pending" || strings.Contains(string(calls), "sys/unseal") {
					t.Fatal("unknown init outcome replayed or unsealed")
				}
			}
			journal := readTestFile(t, execution.JournalPath)
			if strings.Contains(journal, "private-native-root-token") {
				t.Fatal("native secrets leaked into public journal projections")
			}
		})
	}
}
