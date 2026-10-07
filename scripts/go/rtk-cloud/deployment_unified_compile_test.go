package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCommonRendererIncludesAndHashesSelectedAdapterWorkers(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			values, bindings, release := completeEnvironmentRenderFixture(environment)
			bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			cfg := deploymentConfig{Environment: environment, Values: values, AdapterValues: map[string]string{"LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED": "true"}}
			store := makeIsolatedTestSecretStore(t, environment)
			snapshot := environmentDeploymentSnapshot{}
			// Match the existing worker observation that previously failed to
			// find an enabled renderer. This must retain the maintained image,
			// launch command and Secret reference without copying a live spec.
			bindings.Pods = append(bindings.Pods, environmentPodBindings{Kind: "Deployment", NamespaceSuffix: "account-manager", Name: "account-manager-cloud-deletion-worker", Component: "existing-workload", Containers: []environmentContainerBindings{{Name: "worker", ImageKey: "LKE_ACCOUNT_MANAGER_IMAGE", Fields: map[string]any{}}}})
			t.Setenv("LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED", "false")
			before := snapshot.Digest(cfg)
			var resourceDigest string
			for i := 0; i < 2; i++ {
				if err := withEnvironmentRenderInputs(cfg, store, release, snapshot, func(env map[string]string) error {
					if !lkeAccountManagerHandoffWorkerEnabled(env) {
						t.Fatal("selected adapter setting was lost to ambient/default settings")
					}
					definitions, err := renderEnvironmentResources(env, bindings, release)
					if err != nil {
						return err
					}
					found := map[string]bool{}
					for _, definition := range definitions {
						name := deploymentplan.String(deploymentplan.Map(definition.Object["metadata"])["name"])
						found[name] = true
						if name == "account-manager-cloud-deletion-worker" {
							container := deploymentplan.Map(certIssuerObjectList(environmentZammadPod(definition.Object)["containers"])[0])
							if container["image"] != release.Images["LKE_ACCOUNT_MANAGER_IMAGE"].Reference || deploymentplan.Digest(container["command"]) != deploymentplan.Digest([]any{"/app/rtk-account-manager-cloud-deletion-worker"}) || !strings.Contains(mustJSON(t, container["envFrom"]), "account-manager-runtime") {
								t.Fatal("worker lost its maintained image, command or credential reference")
							}
						}
					}
					for _, name := range []string{"account-manager-handoff-worker", "account-manager-cloud-deletion-worker", "billing-settlement-collector"} {
						if !found[name] {
							t.Fatal("selected worker omitted from the shared target", name)
						}
					}
					if i == 0 {
						resourceDigest = deploymentplan.Digest(definitions)
					} else if resourceDigest != deploymentplan.Digest(definitions) {
						t.Fatal("identical selected inputs produced different shared targets")
					}
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			if os.Getenv("LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED") != "false" || snapshot.Digest(cfg) != before {
				t.Fatal("rendering changed the selected configuration or process environment")
			}
			cfg.AdapterValues["LKE_ACCOUNT_MANAGER_HANDOFF_WORKER_ENABLED"] = "false"
			if snapshot.Digest(cfg) == before {
				t.Fatal("adapter setting changes did not invalidate preflight configuration hash")
			}
		})
	}
}

func TestCommonRendererConsumesOnlyTheHashedPrivateSnapshot(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			store := secretStore{Root: root, Environment: environment}
			if err := os.Mkdir(store.RuntimeDir(), 0700); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(store.RuntimeDir(), "postgres")
			if err := os.WriteFile(path, []byte("different-file-value"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg := deploymentConfig{Environment: environment, Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + environment}}
			snapshot := environmentDeploymentSnapshot{Operator: map[string]string{"LINODE_TOKEN": "frozen-operator"}, Secrets: map[string]string{"postgres": "frozen-runtime"}}
			digest := snapshot.Digest(cfg)
			t.Setenv("LINODE_TOKEN", "ambient-profile")
			err := withEnvironmentRenderInputs(cfg, store, environmentRelease{}, snapshot, func(values map[string]string) error {
				if os.Getenv("LINODE_TOKEN") != "frozen-operator" || lkeRuntimeSecretValue("postgres") != "frozen-runtime" {
					t.Fatal("renderer re-read a different unqualified snapshot")
				}
				if lkeRuntimeSecretValue("absent") != "" {
					t.Fatal("renderer generated a missing runtime secret")
				}
				return errors.New("expected fixture failure")
			})
			if err == nil || os.Getenv("LINODE_TOKEN") != "ambient-profile" || snapshot.Digest(cfg) != digest {
				t.Fatal("bounded rendering did not preserve inputs/environment")
			}
			raw, err := os.ReadFile(path)
			if err != nil || string(raw) != "different-file-value" {
				t.Fatal("read-only rendering wrote runtime state")
			}
			if _, err := os.Stat(filepath.Join(store.RuntimeDir(), "absent")); !os.IsNotExist(err) {
				t.Fatal("missing secret was initialized")
			}
		})
	}
}

func TestExactReleaseRejectsDirtyOrDifferentExecutingCheckout(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, mode := range []string{"clean", "other-head", "tracked-change", "untracked-source", "git-error"} {
		t.Run(mode, func(t *testing.T) {
			calls := 0
			run := func(ctx context.Context, command string, args ...string) ([]byte, error) {
				calls++
				if command != "git" || len(args) < 3 || args[0] != "-C" || args[1] != "/reviewed/workspace" {
					t.Fatal("source verification escaped selected checkout")
				}
				if mode == "git-error" {
					return nil, errors.New("private command output")
				}
				if args[2] == "rev-parse" {
					if mode == "other-head" {
						return []byte(strings.Repeat("b", 40)), nil
					}
					return []byte(sha + "\n"), nil
				}
				if args[2] != "status" {
					t.Fatal("unexpected source probe")
				}
				if mode == "tracked-change" {
					return []byte(" M private-path\n"), nil
				}
				if mode == "untracked-source" {
					return []byte("?? private-source.go\n"), nil
				}
				return nil, nil
			}
			err := verifyEnvironmentReleaseCheckout(context.Background(), "/reviewed/workspace", sha, run)
			if (err == nil) != (mode == "clean") {
				t.Fatalf("unexpected %s outcome: %v", mode, err)
			}
			if err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal("source diagnostic leaked private paths/output")
			}
			if mode == "clean" && calls != 2 {
				t.Fatal("clean source not independently verified")
			}
		})
	}
}
