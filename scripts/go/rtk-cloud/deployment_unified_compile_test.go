package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
