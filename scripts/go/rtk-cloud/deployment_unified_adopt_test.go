package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

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
				binding, err := adoptEnvironmentBindings(environment, release, live)
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
