package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestMissingCreateInputsCannotQualifyOrAllocate(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, environment)
			cfg := deploymentConfig{Environment: environment, Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + environment}}
			before, err := os.ReadDir(store.Root)
			if err != nil {
				t.Fatal(err)
			}
			calls := map[string]func() error{
				"qualification": func() error {
					return qualifyEnvironmentCreateBootstrap(context.Background(), cfg, environmentCreateBootstrapPlan{}, store, nil)
				},
				"provider execution": func() error {
					// Missing plan/execution would panic if allocation were reached.
					return executeEnvironmentCreatePlan(context.Background(), cfg, nil, nil, store, nil, io.Discard)
				},
				"bootstrap execution": func() error {
					bindings, err := executeEnvironmentCreateBootstrap(context.Background(), cfg, environmentCreateBootstrapPlan{}, environmentRelease{}, store, nil, func(context.Context, string, []deploymentplan.Definition) error {
						t.Fatal("incomplete bootstrap applied Kubernetes resources")
						return nil
					})
					if !reflect.DeepEqual(bindings, environmentIdentityBindings{}) {
						t.Fatal("incomplete bootstrap fabricated identity bindings")
					}
					return err
				},
			}
			for name, run := range calls {
				t.Run(name, func(t *testing.T) {
					var blocker *deploymentplan.Blocker
					if err := run(); !errors.As(err, &blocker) || blocker.ID == "deployment.bootstrap.provider-stage" {
						t.Fatal("missing inputs did not report their specific prerequisite blocker", err)
					}
				})
			}
			after, err := os.ReadDir(store.Root)
			if err != nil || !reflect.DeepEqual(before, after) {
				t.Fatal("incomplete bootstrap wrote deployment state", err)
			}
		})
	}
}

func TestStagedCreateDiscoversAbsenceAndReachesFormalReleaseWithoutKubeconfig(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		for _, action := range []string{"preflight", "plan"} {
			t.Run(environment+"/"+action, func(t *testing.T) {
				store := makeIsolatedTestSecretStore(t, environment)
				if err := store.write("operator/env/LINODE_TOKEN", []byte("private-selected-token"), true); err != nil {
					t.Fatal(err)
				}
				cfg := deploymentConfig{Environment: environment, Adapter: "lke", RuntimeRoot: t.TempDir(), Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + environment}}
				log := fakeDeploymentPreflightLKERead(t, map[string]string{"/lke/clusters?page_size=500&page=1": `{"data":[],"page":1,"pages":1,"results":0}`})
				deps := defaultEnvironmentCreateDependencies()
				resolved := false
				deps.Release = func(context.Context, deploymentConfig) (environmentRelease, []environmentReleasePublicationProof, error) {
					resolved = true
					return environmentRelease{}, nil, environmentCreateBlocker("deployment.create.release", "isolated formal publication is unavailable")
				}
				deps.Execute = func(context.Context, deploymentConfig, *environmentCreatePlan, *environmentCreateExecution, secretStore, map[string]string, io.Writer) error {
					t.Fatal("preflight or plan allocated provider resources")
					return nil
				}
				report := unifiedDeploymentReport{Environment: environment, Operation: "create", Decision: "NO-GO"}
				var out, diagnostics bytes.Buffer
				err := runUnifiedFreshEnvironmentDeployment(context.Background(), cfg, unifiedDeploymentOptions{Action: action, Operation: "create"}, &report, &out, &diagnostics, deps)
				if err != exitCode(1) || !resolved || report.Decision != "NO-GO" || report.Completed || len(report.Gates) != 1 || report.Gates[0].ID != "deployment.create.release" {
					t.Fatalf("fresh target did not reach formal release: err=%v resolved=%v report=%+v", err, resolved, report)
				}
				if actual := readTestFile(t, log); actual != "GET /lke/clusters?page_size=500&page=1\n" {
					t.Fatalf("unexpected provider calls: %q", actual)
				}
				for _, relative := range []string{"kube/kubeconfig.yaml", "deployment/.execution-active"} {
					if _, err := os.Lstat(filepath.Join(store.Root, relative)); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("fresh qualification created future owner", relative, err)
					}
				}
			})
		}
	}
}

func TestCreateOperatorAllowsEmptyOptionalSettingsAndRejectsUnsafeInputs(t *testing.T) {
	for _, mode := range []string{"optional-empty", "nonprivate", "symlink"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			if err := store.write("operator/env/PKI_BROKER_MANAGEMENT_CERT", nil, true); err != nil {
				t.Fatal(err)
			}
			if err := store.write("operator/env/LINODE_TOKEN", []byte("private-selected-token"), true); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(store.Root, "operator/env/PKI_BROKER_MANAGEMENT_CERT")
			switch mode {
			case "nonprivate":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(filepath.Join(store.Root, "operator/env/LINODE_TOKEN"), path); err != nil {
					t.Fatal(err)
				}
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			_, operator, err := readEnvironmentCreateOperator(deploymentConfig{Environment: "dev"})
			if mode == "optional-empty" {
				if err != nil || operator["PKI_BROKER_MANAGEMENT_CERT"] != "" || operator["LINODE_TOKEN"] != "private-selected-token" {
					t.Fatal("empty optional value rejected", err)
				}
				if _, err := readBoundedEnvironmentFile(path, true); err == nil {
					t.Fatal("nonempty identity read contract changed")
				}
			} else {
				var blocker *deploymentplan.Blocker
				if !errors.As(err, &blocker) || blocker.Resource != "operator/env/PKI_BROKER_MANAGEMENT_CERT" {
					t.Fatal("unsafe credential lacks safe exact resource diagnostic", err)
				}
			}
			if !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, store.Root)) {
				t.Fatal("operator read changed private inputs")
			}
		})
	}
}
