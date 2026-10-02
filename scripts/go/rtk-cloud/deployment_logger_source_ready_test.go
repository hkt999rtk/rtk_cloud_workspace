package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoggerPeriodSourceRequiresCurrentSingletonImmutableRuntime(t *testing.T) {
	previousCanonical, previousState, previousCache := activeCanonicalSecretStore, lkeRuntimeSecretStateDir, lkeRuntimeSecretCache
	activeCanonicalSecretStore, lkeRuntimeSecretStateDir = true, t.TempDir()
	lkeRuntimeSecretCache = map[string]string{}
	t.Cleanup(func() {
		activeCanonicalSecretStore, lkeRuntimeSecretStateDir, lkeRuntimeSecretCache = previousCanonical, previousState, previousCache
	})
	for _, key := range []string{"postgres", "video-auth", "ota-bff-token", "fleet-read-token", "mqtt-broker-auth", "mqtt-server-password", "clip-private-key-seed", "logger-producer-seal-token"} {
		value := "fixture-" + key
		if key == "logger-producer-seal-token" {
			value = strings.Repeat("l", 40)
		}
		if err := os.WriteFile(filepath.Join(lkeRuntimeSecretStateDir, key), []byte(value), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	env := map[string]string{
		"CLOUD_ENV_NAME": "staging", "CLOUD_STACK_NAME": "video-cloud-staging",
		"LKE_VIDEO_CLOUD_IMAGE":                   "ghcr.io/example/video-cloud@sha256:" + strings.Repeat("a", 64),
		"LKE_LOGGER_SERVICE_REGISTRATION_ENABLED": "true", "LKE_LOGGER_BILLING_FACTS_ENABLED": "true", "LKE_LOGGER_PERIOD_SEALS_ENABLED": "true",
	}
	for _, key := range []string{"LINODE_OBJ_ACCESS_KEY_ID", "LINODE_OBJ_SECRET_ACCESS_KEY", "LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED", "LKE_VIDEO_CLOUD_IMAGE"} {
		t.Setenv(key, "")
	}
	checksum := lkeVideoCloudRuntimeChecksum(env)
	bin, calls := t.TempDir(), filepath.Join(t.TempDir(), "calls")
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$LOGGER_SOURCE_CALLS"
case "$*" in
  *"get deployment video-cloud-logingester -o json"*) printf '%s' "$LOGGER_SOURCE_DEPLOYMENT" ;;
  *"get pods -l app.kubernetes.io/name=video-cloud-logingester -o json"*) printf '%s' "$LOGGER_SOURCE_PODS" ;;
  *"get service video-cloud-logingester -o json"*) printf '{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"video-cloud-logingester"},"ports":[{"port":19300}]}}' ;;
  *"get endpointslices -l kubernetes.io/service-name=video-cloud-logingester -o json"*) printf '{"items":[{"endpoints":[{"addresses":["10.0.0.9"],"conditions":{"ready":true}}]}]}' ;;
  *"exec deployment/video-cloud-logingester -c app -- /app/schema-maintenance logger-verify"*) test "${LOGGER_SCHEMA_INVALID:-}" != true ;;
  *) exit 1 ;;
esac
`
	kubectl := filepath.Join(bin, "kubectl")
	if err := os.WriteFile(kubectl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("RTK_CLOUD_KUBECONFIG", "/fixture/staging/kubeconfig.yaml")
	t.Setenv("LOGGER_SOURCE_CALLS", calls)
	setJSON := func(key string, value any) {
		t.Helper()
		body, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		t.Setenv(key, string(body))
	}
	template := func() map[string]any {
		return map[string]any{
			"metadata": map[string]any{"annotations": map[string]any{"rtk.realtek.com/runtime-checksum": checksum}},
			"spec": map[string]any{"containers": []any{map[string]any{
				"name": "app", "image": env["LKE_VIDEO_CLOUD_IMAGE"],
				"env": []any{
					map[string]any{"name": "VIDEO_CLOUD_LOGGER_SERVICE_ENABLED", "value": "true"},
					map[string]any{"name": "VIDEO_CLOUD_LOGGER_BILLING_FACTS_ENABLED", "value": "true"},
					map[string]any{"name": "VIDEO_CLOUD_LOGGER_PERIOD_SEALS_ENABLED", "value": "true"},
				},
			}}},
		}
	}
	pod := func() map[string]any {
		value := template()
		value["status"] = map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}}
		return value
	}
	app := func(value map[string]any) map[string]any {
		return value["spec"].(map[string]any)["containers"].([]any)[0].(map[string]any)
	}
	qualifiedKubeconfig := ""
	for _, tc := range []struct {
		name, want string
		change     func(map[string]any, *[]any)
	}{
		{name: "complete current source"},
		{name: "old Ready Pod while new revision pending", want: "updatedReplicas", change: func(d map[string]any, pods *[]any) { d["status"].(map[string]any)["updatedReplicas"] = 0 }},
		{name: "controller has not observed updated config", want: "current revision", change: func(d map[string]any, pods *[]any) { d["status"].(map[string]any)["observedGeneration"] = 1 }},
		{name: "updated template but old Pod checksum", want: "live Pod: runtime checksum", change: func(d map[string]any, pods *[]any) {
			(*pods)[0].(map[string]any)["metadata"].(map[string]any)["annotations"].(map[string]any)["rtk.realtek.com/runtime-checksum"] = "old"
		}},
		{name: "updated runtime inputs only", want: "Deployment: runtime checksum", change: func(d map[string]any, pods *[]any) {
			d["spec"].(map[string]any)["template"].(map[string]any)["metadata"].(map[string]any)["annotations"].(map[string]any)["rtk.realtek.com/runtime-checksum"] = "before-token-update"
		}},
		{name: "mixed old mutable and new immutable Pods", want: "VIDEO_CLOUD_LOGGER_PERIOD_SEALS_ENABLED", change: func(d map[string]any, pods *[]any) {
			old := pod()
			app(old)["env"].([]any)[2].(map[string]any)["value"] = "false"
			*pods = append(*pods, old)
		}},
		{name: "extra current Pod still overlaps", want: "exactly one live Pod", change: func(d map[string]any, pods *[]any) { *pods = append(*pods, pod()) }},
		{name: "old terminating process can still forward", want: "terminating live Pod", change: func(d map[string]any, pods *[]any) {
			old := pod()
			old["metadata"].(map[string]any)["deletionTimestamp"] = "2026-10-01T16:00:00Z"
			*pods = append(*pods, old)
		}},
		{name: "wrong Deployment release", want: "selected immutable release", change: func(d map[string]any, pods *[]any) {
			app(d["spec"].(map[string]any)["template"].(map[string]any))["image"] = "ghcr.io/example/video-cloud:old"
		}},
		{name: "wrong live Pod release", want: "live Pod: app image", change: func(d map[string]any, pods *[]any) {
			app((*pods)[0].(map[string]any))["image"] = "ghcr.io/example/video-cloud:old"
		}},
		{name: "missing Deployment immutable mode", want: "VIDEO_CLOUD_LOGGER_PERIOD_SEALS_ENABLED", change: func(d map[string]any, pods *[]any) {
			app(d["spec"].(map[string]any)["template"].(map[string]any))["env"] = app(d["spec"].(map[string]any)["template"].(map[string]any))["env"].([]any)[:2]
		}},
		{name: "missing live Pod Billing flag", want: "VIDEO_CLOUD_LOGGER_BILLING_FACTS_ENABLED", change: func(d map[string]any, pods *[]any) {
			app((*pods)[0].(map[string]any))["env"].([]any)[1].(map[string]any)["value"] = "false"
		}},
		{name: "duplicated mode overridden false", want: "VIDEO_CLOUD_LOGGER_PERIOD_SEALS_ENABLED", change: func(d map[string]any, pods *[]any) {
			c := app((*pods)[0].(map[string]any))
			c["env"] = append(c["env"].([]any), map[string]any{"name": "VIDEO_CLOUD_LOGGER_PERIOD_SEALS_ENABLED", "value": "false"})
		}},
		{name: "current Pod not Ready", want: "not Running and Ready", change: func(d map[string]any, pods *[]any) {
			(*pods)[0].(map[string]any)["status"].(map[string]any)["conditions"] = []any{}
		}},
		{name: "no live Pod", want: "exactly one live Pod", change: func(d map[string]any, pods *[]any) { *pods = nil }},
		{name: "completed old Pod cannot forward", change: func(d map[string]any, pods *[]any) {
			old := pod()
			old["status"].(map[string]any)["phase"] = "Succeeded"
			app(old)["env"].([]any)[2].(map[string]any)["value"] = "false"
			*pods = append(*pods, old)
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			deployment := map[string]any{
				"metadata": map[string]any{"generation": 2},
				"spec":     map[string]any{"replicas": 1, "strategy": map[string]any{"type": "Recreate"}, "template": template()},
				"status":   map[string]any{"observedGeneration": 2, "replicas": 1, "updatedReplicas": 1, "availableReplicas": 1, "readyReplicas": 1},
			}
			pods := []any{pod()}
			if tc.change != nil {
				tc.change(deployment, &pods)
			}
			setJSON("LOGGER_SOURCE_DEPLOYMENT", deployment)
			setJSON("LOGGER_SOURCE_PODS", map[string]any{"items": pods})
			err := lkeRequireReadyLoggerPeriodSource(env)
			if (err != nil) != (tc.want != "") || (tc.want != "" && !strings.Contains(err.Error(), tc.want)) {
				t.Fatalf("unexpected source readiness: %v, want %q", err, tc.want)
			}
			if tc.name == "complete current source" {
				root := t.TempDir()
				t.Setenv("RTK_CLOUD_CONFIG_ROOT", root)
				store, err := newSecretStore(root, "staging")
				if err != nil {
					t.Fatal(err)
				}
				if err := store.ensureLayout(); err != nil {
					t.Fatal(err)
				}
				writeTestFile(t, filepath.Join(store.Root, "env", "stack.env"), "CLOUD_ENV_NAME=staging\nCLOUD_STACK_NAME=video-cloud-staging\nCLOUD_PROVIDER=lke\n")
				writeTestFile(t, store.KubeconfigPath(), "fixture")
				qualifiedKubeconfig = store.KubeconfigPath()
				for key, value := range env {
					if strings.HasPrefix(key, "LKE_") {
						if err := store.write("operator/env/"+key, []byte(value), false); err != nil {
							t.Fatal(err)
						}
					}
				}
				entries, err := os.ReadDir(lkeRuntimeSecretStateDir)
				if err != nil {
					t.Fatal(err)
				}
				for _, entry := range entries {
					body, err := os.ReadFile(filepath.Join(lkeRuntimeSecretStateDir, entry.Name()))
					if err != nil {
						t.Fatal(err)
					}
					if err := store.write("runtime/"+entry.Name(), body, false); err != nil {
						t.Fatal(err)
					}
				}
				cfg := deploymentConfig{
					Environment: "staging", Adapter: "lke", Workspace: t.TempDir(), RuntimeRoot: t.TempDir(),
					Values:          appendMap(env, map[string]string{"DEPLOYMENT_ADAPTER": "lke", "CLOUD_DNS_ROOT_DOMAIN": "example.test"}),
					AdapterValues:   map[string]string{"LKE_MQTT_TENANT_NAMESPACE_ENABLED": "false"},
					AdapterResolved: map[string]string{"LKE_REGION": "us-sea"},
					Storage:         deploymentStoragePlan{RuntimeMedia: deploymentStorageTarget{Bucket: "selected-media", Region: "us-sea", Prefix: "selected-prefix"}},
				}
				// Runtime metadata and validated storage live outside the SecretStore.
				writeTestFile(t, filepath.Join(cfg.RuntimeRoot, "env", "stack.env"), "CLOUD_ENV_NAME=staging\nCLOUD_STACK_NAME=video-cloud-staging\nCLOUD_PROVIDER=lke\nCLOUD_REGION=us-sea\nCLOUD_DNS_ROOT_DOMAIN=example.test\nVIDEO_CLOUD_BLOB_ENDPOINT=https://stale.example.test\nVIDEO_CLOUD_BLOB_BUCKET=stale-media\n")
				if err := writeDeploymentStorageReceipt(cfg.RuntimeRoot, deploymentStorageReceipt{Bucket: "selected-media", Region: "us-sea", Endpoint: "https://selected.example.test"}); err != nil {
					t.Fatal(err)
				}
				resolved, err := loadLKEImageEnv(cfg.Workspace, cfg.RuntimeRoot)
				if err != nil {
					t.Fatal(err)
				}
				canonicalEnv := appendMap(resolved.Values, cfg.Values)
				for key, value := range map[string]string{
					"VIDEO_CLOUD_BLOB_ENDPOINT": "https://selected.example.test", "VIDEO_CLOUD_BLOB_BUCKET": "selected-media",
					"VIDEO_CLOUD_BLOB_REGION": "us-sea", "VIDEO_CLOUD_BLOB_PREFIX": "selected-prefix",
					"LKE_MQTT_TENANT_NAMESPACE_ENABLED": "false",
				} {
					canonicalEnv[key] = value
				}
				for key, value := range map[string]string{
					"LINODE_OBJ_ACCESS_KEY_ID": "legacy-media-id", "LINODE_OBJ_SECRET_ACCESS_KEY": "legacy-media-secret",
					"LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "scoped-media-id", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "scoped-media-secret",
				} {
					if err := store.write("operator/env/"+key, []byte(value), false); err != nil {
						t.Fatal(err)
					}
				}
				// The normal deploy uses scoped media inputs even when legacy credentials coexist.
				canonicalEnv["LINODE_OBJ_ACCESS_KEY_ID"] = "scoped-media-id"
				canonicalEnv["LINODE_OBJ_SECRET_ACCESS_KEY"] = "scoped-media-secret"
				previousDir := lkeRuntimeSecretStateDir
				lkeRuntimeSecretStateDir = store.RuntimeDir()
				canonicalChecksum := lkeVideoCloudRuntimeChecksum(canonicalEnv)
				legacyChecksum := lkeVideoCloudRuntimeChecksum(appendMap(canonicalEnv, map[string]string{
					"LINODE_OBJ_ACCESS_KEY_ID": "legacy-media-id", "LINODE_OBJ_SECRET_ACCESS_KEY": "legacy-media-secret",
				}))
				if canonicalChecksum == legacyChecksum {
					t.Fatal("mixed credentials must produce distinct runtime checksums")
				}
				lkeRuntimeSecretStateDir = previousDir
				deployment["spec"].(map[string]any)["template"].(map[string]any)["metadata"].(map[string]any)["annotations"].(map[string]any)["rtk.realtek.com/runtime-checksum"] = canonicalChecksum
				pods[0].(map[string]any)["metadata"].(map[string]any)["annotations"].(map[string]any)["rtk.realtek.com/runtime-checksum"] = canonicalChecksum
				setJSON("LOGGER_SOURCE_DEPLOYMENT", deployment)
				setJSON("LOGGER_SOURCE_PODS", map[string]any{"items": pods})
				t.Setenv("LINODE_OBJ_ACCESS_KEY_ID", "unrelated-shell-id")
				t.Setenv("LINODE_OBJ_SECRET_ACCESS_KEY", "unrelated-shell-secret")
				check := checkRolloutLoggerPeriodSource(cfg)
				if !check.Passed {
					t.Fatalf("canonical live source qualification failed: %+v", check)
				}
				if os.Getenv("LINODE_OBJ_ACCESS_KEY_ID") != "unrelated-shell-id" || os.Getenv("LINODE_OBJ_SECRET_ACCESS_KEY") != "unrelated-shell-secret" {
					t.Fatal("selected credential bindings leaked into caller")
				}
				for name, staleChecksum := range map[string]string{
					"legacy credentials": legacyChecksum,
					"stale storage":      lkeVideoCloudRuntimeChecksum(appendMap(canonicalEnv, map[string]string{"VIDEO_CLOUD_BLOB_BUCKET": "stale-media"})),
				} {
					deployment["spec"].(map[string]any)["template"].(map[string]any)["metadata"].(map[string]any)["annotations"].(map[string]any)["rtk.realtek.com/runtime-checksum"] = staleChecksum
					pods[0].(map[string]any)["metadata"].(map[string]any)["annotations"].(map[string]any)["rtk.realtek.com/runtime-checksum"] = staleChecksum
					setJSON("LOGGER_SOURCE_DEPLOYMENT", deployment)
					setJSON("LOGGER_SOURCE_PODS", map[string]any{"items": pods})
					check = checkRolloutLoggerPeriodSource(cfg)
					if check.Passed || !strings.Contains(check.Detail, "runtime checksum") {
						t.Fatalf("%s checksum must remain rejected: %+v", name, check)
					}
				}
				if err := writeDeploymentStorageReceipt(cfg.RuntimeRoot, deploymentStorageReceipt{Bucket: "different-media", Region: "us-sea", Endpoint: "https://stale.example.test"}); err != nil {
					t.Fatal(err)
				}
				selected, err := loggerPeriodSourceSelectedEnv(cfg, store)
				if err != nil || selected["VIDEO_CLOUD_BLOB_ENDPOINT"] != "" {
					t.Fatal("mismatched receipt borrowed a stale runtime endpoint")
				}
				if os.Getenv("RTK_CLOUD_KUBECONFIG") != "/fixture/staging/kubeconfig.yaml" || lkeRuntimeSecretStateDir == store.RuntimeDir() {
					t.Fatal("canonical qualification leaked selected context")
				}
			}
		})
	}
	t.Run("missing source migration", func(t *testing.T) {
		deployment := map[string]any{
			"metadata": map[string]any{"generation": 2},
			"spec":     map[string]any{"replicas": 1, "strategy": map[string]any{"type": "Recreate"}, "template": template()},
			"status":   map[string]any{"observedGeneration": 2, "replicas": 1, "updatedReplicas": 1, "availableReplicas": 1, "readyReplicas": 1},
		}
		setJSON("LOGGER_SOURCE_DEPLOYMENT", deployment)
		setJSON("LOGGER_SOURCE_PODS", map[string]any{"items": []any{pod()}})
		t.Setenv("LOGGER_SCHEMA_INVALID", "true")
		if err := lkeRequireReadyLoggerPeriodSource(env); err == nil || !strings.Contains(err.Error(), "source schema verification failed") {
			t.Fatalf("unmigrated source accepted: %v", err)
		}
	})
	for _, key := range []string{"LKE_LOGGER_SERVICE_REGISTRATION_ENABLED", "LKE_LOGGER_BILLING_FACTS_ENABLED", "LKE_LOGGER_PERIOD_SEALS_ENABLED"} {
		t.Run("selected disabled "+key, func(t *testing.T) {
			t.Setenv(key, "true")
			selected := appendMap(env, map[string]string{key: "false"})
			if err := lkeRequireReadyLoggerPeriodSource(selected); err == nil || !strings.Contains(err.Error(), "selected "+key) {
				t.Fatalf("inherited true masked selected false: %v", err)
			}
		})
	}
	activeCanonicalSecretStore = false
	if err := lkeRequireReadyLoggerPeriodSource(env); err == nil || !strings.Contains(err.Error(), "canonical runtime") || len(lkeRuntimeSecretCache) != 0 {
		t.Fatalf("unbound readiness generated runtime material: %v", err)
	}
	commands, err := os.ReadFile(calls)
	if err != nil {
		t.Fatal(err)
	}
	for _, command := range strings.Split(strings.TrimSpace(string(commands)), "\n") {
		readOnly := strings.Contains(command, " get ") || strings.Contains(command, " exec deployment/video-cloud-logingester -c app -- /app/schema-maintenance logger-verify")
		correctTarget := strings.Contains(command, "--kubeconfig /fixture/staging/kubeconfig.yaml") || (qualifiedKubeconfig != "" && strings.Contains(command, "--kubeconfig "+qualifiedKubeconfig))
		if !readOnly || !correctTarget {
			t.Fatalf("source readiness used a mutation or different target: %s", command)
		}
	}
}
