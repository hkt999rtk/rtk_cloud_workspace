package main

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"os/exec"
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
			snapshot := environmentDeploymentSnapshot{Secrets: map[string]string{"emqx-dashboard-password": "isolated-dashboard", "mqtt-broker-auth": "isolated-callback", "emqx-handoff-api-key": "isolated-api-key", "emqx-handoff-api-secret": "isolated-api-secret"}}
			snapshot.Secrets["payment-reference-encryption"] = base64.StdEncoding.EncodeToString(make([]byte, 32))
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

func TestExactReleaseRequiresFrozenRevisionWithoutWorkingTreeQualification(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, mode := range []string{"clean", "other-head", "tracked-change", "untracked-source", "edited-docs", "git-error", "invalid-revision", "invalid-directory", "missing-run", "pre-cancelled", "cancel-during-revision"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			calls := 0
			run := func(ctx context.Context, command string, args ...string) ([]byte, error) {
				calls++
				if command != "git" || len(args) != 4 || args[0] != "-C" || args[1] != "/reviewed/workspace" || args[2] != "rev-parse" || args[3] != "HEAD" {
					t.Fatal("revision verification queried working-tree status or another checkout")
				}
				if err := ctx.Err(); err != nil {
					return nil, err
				}
				if mode == "git-error" {
					return nil, errors.New("private command output")
				}
				if mode == "other-head" {
					return []byte(strings.Repeat("b", 40)), nil
				}
				if mode == "cancel-during-revision" {
					cancel()
				}
				return []byte(sha + "\n"), nil
			}
			directory, revision := "/reviewed/workspace", sha
			if mode == "invalid-revision" {
				revision = "unqualified"
			}
			if mode == "invalid-directory" {
				directory = ""
			}
			if mode == "missing-run" {
				run = nil
			}
			if mode == "pre-cancelled" {
				cancel()
			}
			err := verifyEnvironmentReleaseCheckout(ctx, directory, revision, run)
			allowed := mode == "clean" || mode == "tracked-change" || mode == "untracked-source" || mode == "edited-docs"
			if (err == nil) != allowed {
				t.Fatalf("unexpected %s outcome: %v", mode, err)
			}
			if err != nil && strings.Contains(err.Error(), "private") {
				t.Fatal("source diagnostic leaked private paths/output")
			}
			if allowed && calls != 1 {
				t.Fatal("selected frozen HEAD was not independently verified exactly once")
			}
			if (mode == "invalid-revision" || mode == "invalid-directory" || mode == "missing-run" || mode == "pre-cancelled") && calls != 0 {
				t.Fatal("invalid or cancelled verification started a Git query")
			}
			if (mode == "pre-cancelled" || mode == "cancel-during-revision") && !errors.Is(err, context.Canceled) {
				t.Fatal("revision verification lost parent cancellation", err)
			}
		})
	}
}

func TestExactReleaseAllowsActualTrackedUntrackedAndDocumentationEdits(t *testing.T) {
	for _, mode := range []string{"tracked-source", "untracked-source", "edited-docs"} {
		t.Run(mode, func(t *testing.T) {
			workspace := t.TempDir()
			git := func(args ...string) []byte {
				t.Helper()
				cmd := exec.Command("git", append([]string{"-C", workspace}, args...)...)
				for _, setting := range os.Environ() {
					if !strings.HasPrefix(setting, "GIT_") {
						cmd.Env = append(cmd.Env, setting)
					}
				}
				cmd.Env = append(cmd.Env, "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null", "GIT_TERMINAL_PROMPT=0")
				raw, err := cmd.Output()
				if err != nil {
					t.Fatal("isolated source fixture Git setup failed", err)
				}
				return raw
			}
			git("init", "--quiet")
			git("config", "user.name", "Source Fixture")
			git("config", "user.email", "source-fixture@example.invalid")
			git("config", "core.hooksPath", "/dev/null")
			for _, name := range []string{"source.go", "architecture.md"} {
				if err := os.WriteFile(filepath.Join(workspace, name), []byte("committed original\n"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			git("add", "--", "source.go", "architecture.md")
			git("commit", "--quiet", "--no-gpg-sign", "-m", "Frozen source fixture")
			revision := strings.TrimSpace(string(git("rev-parse", "HEAD")))
			name := "source.go"
			if mode == "untracked-source" {
				name = "untracked.go"
			} else if mode == "edited-docs" {
				name = "architecture.md"
			}
			path := filepath.Join(workspace, name)
			const contents = "uncommitted private fixture change\n"
			if err := os.WriteFile(path, []byte(contents), 0600); err != nil {
				t.Fatal(err)
			}
			queries := 0
			run := func(ctx context.Context, command string, args ...string) ([]byte, error) {
				queries++
				if command != "git" || len(args) != 4 || args[0] != "-C" || args[1] != workspace || args[2] != "rev-parse" || args[3] != "HEAD" {
					t.Fatal("source verification queried working-tree status instead of frozen HEAD")
				}
				return deploymentUpgradeCommand(ctx, command, args...)
			}
			if err := verifyEnvironmentReleaseCheckout(context.Background(), workspace, revision, run); err != nil || queries != 1 {
				t.Fatal("uncommitted source or documentation changed the frozen release qualification", err)
			}
			actual, err := os.ReadFile(path)
			if err != nil || string(actual) != contents || strings.TrimSpace(string(git("rev-parse", "HEAD"))) != revision {
				t.Fatal("revision verification changed user files or the committed target", err)
			}
		})
	}
}
