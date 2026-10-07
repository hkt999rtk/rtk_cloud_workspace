package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestSharedGrafanaImageRecognizesExistingConsumerAcrossEnvironments(t *testing.T) {
	baseline, err := readStrictEnv(filepath.Join(mustWorkspaceRoot(t), "cloud_deploy", "architectures", "kubernetes", "architecture.env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			stack := "video-cloud-" + environment
			release := environmentRelease{Environment: environment, Stack: stack, Images: map[string]environmentReleaseImage{"LKE_GRAFANA_IMAGE": {Reference: baseline["GRAFANA_IMAGE"]}}}
			live := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "video-cloud-grafana", "namespace": stack + "-observability", "uid": "original-owner"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "grafana", "image": "grafana/grafana:13.0.2"}}}}}}
			bindings, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{live})
			if err != nil || bindings.Pods[0].Containers[0].ImageKey != "LKE_GRAFANA_IMAGE" {
				t.Fatal("common image did not recognize the existing Grafana container", err)
			}
			t.Setenv("LKE_GRAFANA_IMAGE", "ambient.example/grafana:unreviewed")
			env := map[string]string{"CLOUD_STACK_NAME": stack, "LKE_GRAFANA_IMAGE": baseline["GRAFANA_IMAGE"]}
			if !strings.Contains(lkeGrafanaDeploymentManifest(env), "image: "+baseline["GRAFANA_IMAGE"]) {
				t.Fatal("renderer did not consume the same maintained Grafana image")
			}
		})
	}
}

func TestBootstrapCleanupAdoptionPreservesImmutableOriginalOwners(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "mutable", "namespace-drift", "secret-drift", "foreign-environment", "invalid-record", "invalid-ca"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				env, _, release := completeEnvironmentRenderFixture(environment)
				binding := environmentBootstrapCleanupBinding{Version: 1, Environment: environment, SessionID: "aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa", NamespaceUID: "bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb", SecretUID: "cccccccc-cccc-cccc-cccc-cccccccccccc", PolicySHA256: strings.Repeat("d", 64)}
				if mode == "foreign-environment" {
					binding.Environment = "other"
				}
				raw, _ := json.Marshal(map[string]any{"schema_version": binding.Version, "environment": binding.Environment, "session_id": binding.SessionID, "namespace_uid": binding.NamespaceUID, "secret_uid": binding.SecretUID, "policy_sha256": binding.PolicySHA256})
				if mode == "invalid-record" {
					raw = []byte(`{"caller_proof":true}`)
				}
				ca := string(testOpenBaoCAPEM(t))
				if mode == "invalid-ca" {
					ca = "not-a-certificate"
				}
				namespace := env["CLOUD_STACK_NAME"] + "-video-cloud"
				config := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "pki-bootstrap-cleanup-binding", "namespace": namespace, "uid": "public-record-owner"}, "immutable": true, "data": map[string]any{"binding.json": string(raw), "ca.crt": ca}}
				if mode == "mutable" {
					config["immutable"] = false
				}
				nsUID, secretUID := binding.NamespaceUID, binding.SecretUID
				if mode == "namespace-drift" {
					nsUID = "11111111-1111-1111-1111-111111111111"
				}
				if mode == "secret-drift" {
					secretUID = "22222222-2222-2222-2222-222222222222"
				}
				live := []deploymentplan.Object{
					{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": namespace, "uid": nsUID}},
					config,
					{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "service-bootstrap-" + binding.SessionID, "namespace": namespace, "uid": secretUID}, "data": map[string]any{"private": "never-import"}},
					{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "video-cloud-api-pki", "namespace": namespace, "uid": "api-owner"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app", "image": release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference}}}}}},
				}
				before := deploymentplan.Digest(live)
				adopted, err := observeEnvironmentBindings(environment, release, live)
				if (err == nil) != (mode == "valid") {
					t.Fatal("bootstrap owner adoption result incorrect", err)
				}
				if deploymentplan.Digest(live) != before {
					t.Fatal("adoption mutated runtime")
				}
				if mode == "valid" {
					if adopted.BootstrapCleanup == nil || adopted.BootstrapCleanup.SecretUID != binding.SecretUID || adopted.BootstrapCleanup.NamespaceUID != binding.NamespaceUID || adopted.BootstrapCleanup.KubernetesCA != base64.StdEncoding.EncodeToString([]byte(ca)) {
						t.Fatal("original lifecycle owners were lost")
					}
					raw, _ := json.Marshal(adopted)
					if strings.Contains(string(raw), "never-import") {
						t.Fatal("Secret data became adoption input")
					}
				}
			})
		}
	}
}

func TestEnvironmentAdoptionPreservesReferencesWithoutImportingWorkloadBehavior(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "wrong-environment", "foreign-owner", "duplicate-owner", "deleting", "unknown-image", "inline-password", "legacy-bao"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				env, _, release := completeEnvironmentRenderFixture(environment)
				object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": map[string]any{"name": "video-cloud-api-pki", "namespace": env["CLOUD_STACK_NAME"] + "-video-cloud", "uid": "original-owner"}, "spec": map[string]any{"replicas": float64(99), "template": map[string]any{"spec": map[string]any{"volumes": []any{map[string]any{"name": "identity", "persistentVolumeClaim": map[string]any{"claimName": "original-identity"}}}, "containers": []any{map[string]any{"name": "app", "image": release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference, "command": []any{"legacy-command"}, "resources": map[string]any{"requests": map[string]any{"cpu": "999m"}}, "env": []any{map[string]any{"name": "ORDINARY_FEATURE", "value": "true"}, map[string]any{"name": "VIDEO_CLOUD_API_TLS_KEY_PATH", "value": "/identity/original.key"}}}}}}}}
				live := []deploymentplan.Object{object}
				meta := deploymentplan.Map(object["metadata"])
				container := deploymentplan.Map(certIssuerObjectList(managedTestPod(object)["containers"])[0])
				switch mode {
				case "wrong-environment":
					release.Environment = "other"
				case "foreign-owner":
					meta["labels"] = map[string]any{"rtk.realtek.com/stack": "another-stack"}
				case "duplicate-owner":
					live = append(live, object)
				case "deleting":
					meta["deletionTimestamp"] = "now"
				case "unknown-image":
					container["image"] = "unknown/api:latest"
				case "inline-password":
					container["env"] = []any{map[string]any{"name": "PKI_DATABASE_URL", "value": "postgres://user:never-public@db"}}
				case "legacy-bao":
					meta["name"], meta["namespace"] = "openbao", env["CLOUD_STACK_NAME"]+"-secrets"
				}
				before := deploymentplan.Digest(live)
				binding, err := observeEnvironmentBindings(environment, release, live)
				if (err == nil) != (mode == "valid") {
					t.Fatal("incorrect adoption decision", mode, err)
				}
				if err != nil && strings.Contains(err.Error(), "never-public") {
					t.Fatal("adoption leaked credentials")
				}
				if deploymentplan.Digest(live) != before {
					t.Fatal("adoption mutated actual state")
				}
				if err != nil {
					return
				}
				raw, _ := json.Marshal(binding)
				for _, forbidden := range []string{"legacy-command", "replicas", "999m", "ORDINARY_FEATURE", "desired", "baseline_spec"} {
					if strings.Contains(string(raw), forbidden) {
						t.Fatal("adoption imported workload behavior", forbidden)
					}
				}
				if binding.Owners[deploymentplan.ID(object)] != "original-owner" || !strings.Contains(string(raw), "original-identity") || !strings.Contains(string(raw), "/identity/original.key") {
					t.Fatal("original owner or lifecycle references lost")
				}
			})
		}
	}
}

func TestEnvironmentLegacyArchiveIsPrivateMatchedAndNeverSelectsRenderer(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			root := t.TempDir()
			store := secretStore{Root: root, Environment: environment}
			directory := filepath.Join(root, "deployment")
			if err := os.Mkdir(directory, 0700); err != nil {
				t.Fatal(err)
			}
			if err := archiveLegacyEnvironmentPlan(store); err != nil {
				t.Fatal("missing legacy file is harmless", err)
			}
			raw := []byte(`{"schema_version":1,"environment":"` + environment + `","stack":"video-cloud-` + environment + `","workloads":[{"private":"preserve"}]}`)
			path := filepath.Join(directory, "managed-upgrade.json")
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 2; i++ {
				if err := archiveLegacyEnvironmentPlan(store); err != nil {
					t.Fatal("idempotent archive", err)
				}
			}
			archives, err := filepath.Glob(filepath.Join(directory, "archive", "*.json"))
			if err != nil || len(archives) != 1 {
				t.Fatal("legacy archive missing or duplicated")
			}
			copy, _ := os.ReadFile(archives[0])
			info, _ := os.Stat(archives[0])
			dir, _ := os.Stat(filepath.Dir(archives[0]))
			if string(copy) != string(raw) || info.Mode().Perm() != 0600 || dir.Mode().Perm() != 0700 {
				t.Fatal("archive changed contents or permissions")
			}
			original, _ := os.ReadFile(path)
			if string(original) != string(raw) {
				t.Fatal("unreviewed migration removed original")
			}
			if err := os.Chmod(path, 0644); err != nil {
				t.Fatal(err)
			}
			if archiveLegacyEnvironmentPlan(store) == nil {
				t.Fatal("unsafe custody accepted")
			}
		})
	}
}
