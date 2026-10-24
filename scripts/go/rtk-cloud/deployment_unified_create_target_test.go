package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestUnifiedCreateInspectsOnlyItsFreshProviderTargetBeforeBootstrap(t *testing.T) {
	const first = "/lke/clusters?page_size=500&page=1"
	const second = "/lke/clusters?page_size=500&page=2"
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"absent", "absent-paginated", "existing-label", "renamed-stack-tag", "later-renamed-stack-tag", "missing-tags", "invalid-tags", "incomplete-pagination", "excessive-pagination", "denied", "recorded-existing", "recorded-missing", "missing-token", "private-token-permissions", "private-token-directory", "private-token-symlink", "local-owner", "missing-adapter"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				c, deps, store, writes := earlyLeaseCLIProfile(t, environment)
				c.Configuration.Adapter, c.Configuration.RuntimeRoot = "lke", t.TempDir()
				if err := store.write(filepath.Join("operator", "env", "LINODE_TOKEN"), []byte("private-selected-token"), true); err != nil {
					t.Fatal(err)
				}
				responses := map[string]string{first: `{"data":[],"page":1,"pages":0,"results":0}`}
				wantID, wantCalls := "deployment.bootstrap.provider-stage", "GET "+first+"\n"
				wantScope := "environment-bootstrap"
				cluster := fmt.Sprintf(`{"id":123,"label":"%s-lke","tags":[]}`, c.Plan.Stack)
				switch mode {
				case "absent-paginated":
					responses[first] = `{"data":[{"id":456,"label":"other-stack","tags":[]}],"page":1,"pages":2,"results":2}`
					responses[second] = `{"data":[{"id":789,"label":"another-stack","tags":[]}],"page":2,"pages":2,"results":2}`
					wantCalls += "GET " + second + "\n"
				case "existing-label":
					responses[first] = fmt.Sprintf(`{"data":[%s],"page":1,"pages":1,"results":1}`, cluster)
					wantID = "deployment.create.target"
				case "renamed-stack-tag", "later-renamed-stack-tag":
					renamed := fmt.Sprintf(`{"id":123,"label":"renamed-original-owner","tags":["%s"]}`, c.Plan.Stack)
					responses[first] = fmt.Sprintf(`{"data":[%s],"page":1,"pages":1,"results":1}`, renamed)
					if mode == "later-renamed-stack-tag" {
						responses[first] = `{"data":[{"id":456,"label":"other-stack","tags":[]}],"page":1,"pages":2,"results":2}`
						responses[second] = fmt.Sprintf(`{"data":[%s],"page":2,"pages":2,"results":2}`, renamed)
						wantCalls += "GET " + second + "\n"
					}
					wantID = "deployment.create.target"
				case "missing-tags", "invalid-tags":
					tags := ""
					if mode == "invalid-tags" {
						tags = `,"tags":"private-provider-payload"`
					}
					responses[first] = fmt.Sprintf(`{"data":[{"id":456,"label":"other-stack"%s}],"page":1,"pages":1,"results":1}`, tags)
					wantID = "deployment.create.target"
				case "incomplete-pagination":
					responses[first] = `{"data":[],"page":1,"results":0}`
					wantID = "deployment.create.target"
				case "excessive-pagination":
					responses[first] = `{"data":[],"page":1,"pages":1001,"results":0}`
					wantID = "deployment.create.target"
				case "denied":
					responses[first] = "__ERROR_404__"
					wantID = "deployment.create.target"
				case "recorded-existing", "recorded-missing":
					writeTestLKEState(t, environment, "LKE_CLUSTER_ID=123\n")
					responses["/lke/clusters/123"] = cluster
					if mode == "recorded-missing" {
						responses["/lke/clusters/123"] = "__ERROR_404__"
					}
					wantID, wantCalls = "deployment.create.target", "GET /lke/clusters/123\n"
				case "missing-token":
					if err := os.Remove(filepath.Join(store.Root, "operator", "env", "LINODE_TOKEN")); err != nil {
						t.Fatal(err)
					}
					wantID, wantCalls = "deployment.create.target", ""
				case "private-token-permissions":
					if err := os.Chmod(filepath.Join(store.Root, "operator", "env", "LINODE_TOKEN"), 0644); err != nil {
						t.Fatal(err)
					}
					wantID, wantCalls = "deployment.create.target", ""
				case "private-token-directory":
					if err := os.Chmod(filepath.Join(store.Root, "operator", "env"), 0755); err != nil {
						t.Fatal(err)
					}
					wantID, wantCalls = "deployment.create.target", ""
				case "private-token-symlink":
					path := filepath.Join(store.Root, "operator", "env", "LINODE_TOKEN")
					if err := os.Rename(path, path+".original"); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(path+".original", path); err != nil {
						t.Fatal(err)
					}
					wantID, wantCalls = "deployment.create.target", ""
				case "local-owner":
					if err := os.Mkdir(filepath.Join(store.Root, "deployment", ".execution-active"), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.Remove(filepath.Join(store.Root, "operator", "env", "LINODE_TOKEN")); err != nil {
						t.Fatal(err)
					}
					// Create now routes retained fences to the formal recovery
					// adapter; an empty fixture fence must remain BLOCKED.
					deps.RecoveryPreflight = runEnvironmentCreateRecoveryPreflight
					wantID, wantCalls, wantScope = "deployment.create.recovery.preflight", "", "original-generation-recovery"
				case "missing-adapter":
					wantID, wantCalls = "deployment.create.target", ""
				}
				log := fakeDeploymentPreflightLKERead(t, responses)
				if mode == "denied" {
					curl, err := exec.LookPath("curl")
					if err != nil {
						t.Fatal(err)
					}
					raw, err := os.ReadFile(curl)
					if err != nil || os.WriteFile(curl, bytes.ReplaceAll(raw, []byte("error: 404"), []byte("error: 403")), 0700) != nil {
						t.Fatal("could not prepare denied provider fixture", err)
					}
				}
				kubeLog := filepath.Join(t.TempDir(), "kube-called")
				bin := t.TempDir()
				if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte("#!/bin/sh\nprintf 'private-unexpected-kube-call' >> "+shellSingleQuote(kubeLog)+"\nexit 1\n"), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("LKE_CLUSTER_ID", "999")
				t.Setenv("LKE_CLUSTER_LABEL", "foreign-ambient-target")
				t.Setenv("LINODE_TOKEN", "private-ambient-token")
				deps.CheckExecutionAvailable = checkEnvironmentExecutionAvailable
				if mode == "missing-adapter" {
					deps.CheckExecutionAvailable = nil
				}
				deps.PrepareStorage = func(context.Context, deploymentConfig, io.Writer) error {
					t.Fatal("blocked fresh qualification prepared or recreated a bucket")
					return nil
				}
				deps.Compile = func(context.Context, deploymentConfig, string) (*compiledEnvironmentDeployment, error) {
					t.Fatal("blocked fresh qualification reached CI or Kubernetes compilation")
					return nil, nil
				}
				path := filepath.Join(store.Root, "deployment", "reports", "fresh-target.json")
				var out, diagnostics bytes.Buffer
				err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "preflight", []string{"--environment", environment, "--operation", "create", "--report", path}, &out, &diagnostics, deps)
				var report unifiedDeploymentReport
				raw, readErr := os.ReadFile(path)
				if err != exitCode(1) || readErr != nil || json.Unmarshal(raw, &report) != nil || report.Operation != "create" || report.Scope != wantScope || report.Decision != "NO-GO" || report.Completed || report.Plan != nil || len(report.Gates) != 1 || report.Gates[0].ID != wantID || report.Gates[0].Status != "BLOCKED" || !report.Gates[0].Required || *writes != 0 {
					t.Fatal("fresh target inspection fabricated a plan, GO or bootstrap completion", err, readErr, out.String(), diagnostics.String())
				}
				providerCalls, _ := os.ReadFile(log)
				if string(providerCalls) != wantCalls {
					t.Fatal("fresh target discovery used an ambient target, incomplete inventory or provider mutation", string(providerCalls), wantCalls)
				}
				if _, err := os.Stat(kubeLog); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("fresh create inspected a Kubernetes API before it existed", err)
				}
				if _, err := os.Stat(store.KubeconfigPath()); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("fresh target discovery produced kubeconfig or revived old identity", err)
				}
				text := out.String() + diagnostics.String() + string(raw)
				if strings.Contains(text, "private-") || strings.Contains(text, "[PASS]") || strings.Contains(text, "plan-sha256:") || os.Getenv("LKE_CLUSTER_ID") != "999" || os.Getenv("LKE_CLUSTER_LABEL") != "foreign-ambient-target" || os.Getenv("LINODE_TOKEN") != "private-ambient-token" {
					t.Fatal("fresh inspection leaked private inputs, implied qualification or changed caller selection", text)
				}
			})
		}
	}
}

func TestUpgradeStartupDistinguishesMissingPrivateAndInvalidIdentity(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"missing", "permissions", "directory-permissions", "invalid", "local-owner"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				store := makeIsolatedTestSecretStore(t, environment)
				path := store.KubeconfigPath()
				want := "kubeconfig is missing"
				if mode != "missing" {
					if err := os.WriteFile(path, []byte("private-invalid-identity"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				switch mode {
				case "permissions":
					if err := os.Chmod(path, 0644); err != nil {
						t.Fatal(err)
					}
					want = "private permissions cannot be verified"
				case "directory-permissions":
					if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
						t.Fatal(err)
					}
					want = "private permissions cannot be verified"
				case "invalid":
					want = "identity cannot be verified"
				case "local-owner":
					if err := os.MkdirAll(filepath.Join(store.Root, "deployment", ".execution-active"), 0700); err != nil {
						t.Fatal(err)
					}
					if err := os.Chmod(path, 0644); err != nil {
						t.Fatal(err)
					}
					want = "local execution fence"
				}
				err := checkEnvironmentExecutionAvailable(context.Background(), deploymentConfig{Environment: environment, Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + environment}}, "upgrade")
				var blocker *deploymentplan.Blocker
				if !errors.As(err, &blocker) || blocker.ID != "deployment.execution.lease" || !strings.Contains(blocker.Cause, want) || strings.Contains(blocker.Cause+blocker.Next, "private-invalid-identity") || mode != "missing" && strings.Contains(blocker.Cause, "kubeconfig is missing") {
					t.Fatal("upgrade startup obscured missing vs private/invalid state or leaked identity", err)
				}
				if mode == "missing" && (!strings.Contains(blocker.Next, "only if that cluster still exists") || !strings.Contains(blocker.Next, "explicit --operation create qualification") || !strings.Contains(blocker.Next, "never revive archived cluster identity")) {
					t.Fatal("removed-environment diagnosis encourages old identity revival or automatic creation", blocker.Next)
				}
			})
		}
	}
}

func TestDirectFreshPlanCompilationStopsBeforeExistingRuntimeInputs(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, frozenImages := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/frozen-images=%t", environment, frozenImages), func(t *testing.T) {
				store := makeIsolatedTestSecretStore(t, environment)
				if err := store.write(filepath.Join("operator", "env", "LINODE_TOKEN"), []byte("private-selected-token"), true); err != nil {
					t.Fatal(err)
				}
				cfg := deploymentConfig{Environment: environment, Adapter: "lke", RuntimeRoot: t.TempDir(), Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + environment}}
				log := fakeDeploymentPreflightLKERead(t, map[string]string{"/lke/clusters?page_size=500&page=1": `{"data":[],"page":1,"pages":1,"results":0}`})
				var compiled *compiledEnvironmentDeployment
				var err error
				if frozenImages {
					compiled, err = compileEnvironmentDeploymentWithImages(context.Background(), cfg, "create", environmentRelease{}, nil)
				} else {
					compiled, err = compileEnvironmentDeployment(context.Background(), cfg, "create")
				}
				var blocker *deploymentplan.Blocker
				if compiled != nil || !errors.As(err, &blocker) || blocker.ID != "deployment.bootstrap.provider-stage" || readTestFile(t, log) != "GET /lke/clusters?page_size=500&page=1\n" {
					t.Fatal("fresh plan read existing runtime/identity or skipped the missing stage capability", compiled, err)
				}
			})
		}
	}
}
