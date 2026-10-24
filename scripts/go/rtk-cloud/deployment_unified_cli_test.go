package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestUnifiedCompilationErrorsExposeSafeCauseAndResource(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, action := range []string{"preflight"} {
			for _, mode := range []string{"missing-renderer", "unknown-sidecar", "provider-error", "unclassified"} {
				t.Run(environment+"/"+action+"/"+mode, func(t *testing.T) {
					c, deps, writes := unifiedCLIProfile(t, environment)
					env, bindings, release := completeEnvironmentRenderFixture(environment)
					resource := "Deployment/video-cloud-" + environment + "-video-cloud/video-cloud-api-app-pki"
					id, cause, next := "deployment.inventory.renderer", "existing workload has no enabled maintained renderer", "common component catalog"
					deps.Compile = func(context.Context, deploymentConfig, string) (*compiledEnvironmentDeployment, error) {
						var err error
						switch mode {
						case "missing-renderer":
							bindings.Pods = append(bindings.Pods, environmentPodBindings{Kind: "Deployment", NamespaceSuffix: "video-cloud", Name: "video-cloud-api-app-pki", Component: "existing-workload", Containers: []environmentContainerBindings{{Name: "app", ImageKey: "LKE_VIDEO_CLOUD_IMAGE"}}})
							_, err = renderEnvironmentResources(env, bindings, release)
						case "unknown-sidecar":
							binding := environmentPodBindings{Kind: "Deployment", NamespaceSuffix: "video-cloud", Name: "video-cloud-api-app-pki", Component: "existing-workload", Containers: []environmentContainerBindings{{Name: "app", ImageKey: "LKE_VIDEO_CLOUD_IMAGE"}, {Name: "unmaintained-sidecar", ImageKey: "LKE_VIDEO_CLOUD_IMAGE"}}}
							bindings.Pods = []environmentPodBindings{binding}
							object := deploymentplan.Object{"kind": "Deployment", "metadata": map[string]any{"name": binding.Name, "namespace": bindings.Stack + "-video-cloud"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "app"}}}}}}
							err = applyEnvironmentPodBindings(object, bindings, release)
						case "provider-error":
							err = environmentCompilationError(errors.New("private provider output: never-public-password"), "deployment.inventory.read", "selected Kubernetes inventory could not be read", "verify API connectivity and inventory permissions")
						case "unclassified":
							err = errors.New("private unclassified output: never-public-password")
						}
						if err == nil {
							t.Fatal("fixture did not reach the real compiler failure")
						}
						// A wrapped tool error may contain a private prefix; the
						// public flow must extract only the structured safe fields.
						return nil, fmt.Errorf("never-public-wrapper: %w", err)
					}
					if mode == "unknown-sidecar" {
						id, cause, next = "deployment.inventory.sidecar", "unmaintained-sidecar", "shared renderer"
					} else if mode == "provider-error" {
						id, cause, next, resource = "deployment.inventory.read", "Kubernetes inventory could not be read", "inventory permissions", ""
					} else if mode == "unclassified" {
						id, cause, next, resource = "deployment.compilation.internal", "unclassified internal error", "failing compiler boundary", ""
					}
					args := []string{"--environment", environment}
					if action == "upgrade" {
						args = append(args, "--confirm", "video-cloud-"+environment, "--plan-sha256", c.Plan.SHA256())
					}
					var out bytes.Buffer
					err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), action, args, &out, &out, deps)
					if err != exitCode(1) || *writes != 0 || !strings.Contains(out.String(), id) || !strings.Contains(out.String(), cause) || !strings.Contains(out.String(), next) || resource != "" && !strings.Contains(out.String(), resource) || strings.Contains(out.String(), "never-public") || strings.Contains(out.String(), "complete target plan could not be compiled") {
						t.Fatal("public compiler diagnosis lost actionable metadata or exposed private data", err, out.String())
					}
				})
			}
		}
	}
}

func TestUnifiedQualifiedLoaderErrorsExposeSafeCauseAndResource(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"missing-proof", "expired-proof", "baseline-drift", "unclassified"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				c, deps, writes := unifiedCLIProfile(t, environment)
				id, cause, next, resource := "deployment.preflight.proof", "complete GO evidence is unavailable", "run complete preflight", ""
				if mode == "expired-proof" {
					cause = "qualified preflight evidence has expired"
				} else if mode == "baseline-drift" {
					id, cause, next, resource = "deployment.drift", "original resource baseline changed after preflight", "repeat preflight against the current selected owner", "Secret/video-cloud-"+environment+"-platform/original-identity"
				} else if mode == "unclassified" {
					cause = "qualified execution evidence could not be loaded"
				}
				deps.Compile = func(context.Context, deploymentConfig, string) (*compiledEnvironmentDeployment, error) {
					t.Fatal("deployment compiled instead of loading qualified evidence")
					return nil, nil
				}
				deps.Checks = func(*compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
					t.Fatal("loader failure reached full preflight qualification")
					return nil
				}
				deps.LoadQualified = func(context.Context, deploymentConfig, string, string) (*compiledEnvironmentDeployment, error) {
					if mode == "unclassified" {
						return nil, errors.New("private loader output: never-public-password")
					}
					return nil, fmt.Errorf("never-public-wrapper: %w", &deploymentplan.Blocker{ID: id, Resource: resource, Cause: cause, Next: next})
				}
				var out bytes.Buffer
				err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "upgrade", []string{"--environment", environment, "--confirm", c.Plan.Stack, "--plan-sha256", c.Plan.SHA256()}, &out, &out, deps)
				if err != exitCode(1) || *writes != 0 || !strings.Contains(out.String(), id) || !strings.Contains(out.String(), cause) || !strings.Contains(out.String(), next) || resource != "" && !strings.Contains(out.String(), resource) || strings.Contains(out.String(), "never-public") || strings.Contains(out.String(), "compiler") {
					t.Fatal("public loader diagnosis lost actionable metadata or exposed private data", err, out.String())
				}
			})
		}
	}
}

func unifiedCLIProfile(t *testing.T, environment string) (*compiledEnvironmentDeployment, unifiedDeploymentDependencies, *int) {
	t.Helper()
	makeIsolatedTestSecretStore(t, environment)
	stack := "video-cloud-" + environment
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: environment, Stack: stack, Operation: "upgrade", ConfigurationSHA256: deploymentplan.Digest("config"), ReleaseSHA256: deploymentplan.Digest("release"), BindingsSHA256: deploymentplan.Digest("identity")}, []deploymentplan.Definition{{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "private", "namespace": stack + "-platform", "labels": map[string]any{"rtk.realtek.com/stack": stack}}, "stringData": map[string]any{"credential": "never-public"}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &compiledEnvironmentDeployment{Plan: plan, Configuration: deploymentConfig{Environment: environment, Values: map[string]string{"CLOUD_STACK_NAME": stack, "DEPLOYMENT_PREFLIGHT_VALIDITY_SECONDS": "1800", "DEPLOYMENT_UPGRADE_WINDOW_SECONDS": "1800", "DEPLOYMENT_PROTECTION_PROFILE": map[bool]string{true: "production", false: "standard"}[environment == "prod"]}}}
	writes := 0
	deps := unifiedDeploymentDependencies{
		InvalidateProof: func(deploymentConfig) (string, error) { return "fixture-generation", nil },
		PublishObservations: func(cfg deploymentConfig, c *compiledEnvironmentDeployment, _ string, publish func(deploymentConfig, *compiledEnvironmentDeployment, string) error) error {
			return publish(cfg, c, "NO-GO")
		},
		Resolve: func(string, string, string) (deploymentConfig, error) { return c.Configuration, nil },
		Compile: func(ctx context.Context, cfg deploymentConfig, operation string) (*compiledEnvironmentDeployment, error) {
			if operation != "upgrade" {
				t.Fatal("lost operation or selected release")
			}
			return c, nil
		},
		Checks: func(*compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
			checks := map[string]deploymentplan.GateCheck{}
			for _, id := range deploymentplan.RequiredGateIDs(environment == "prod") {
				checks[id] = func(context.Context, *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
					return deploymentplan.GateEvidence{SHA256: deploymentplan.Digest("isolated fixture evidence")}, nil
				}
			}
			return checks
		},
		Execute: func(ctx context.Context, c *compiledEnvironmentDeployment, sha string, qualify func(context.Context, *deploymentplan.Plan) error, accept func(context.Context, *compiledEnvironmentDeployment) error) error {
			if sha != c.Plan.SHA256() {
				t.Fatal("unreviewed plan executed")
			}
			if err := qualify(ctx, c.Plan); err != nil {
				return err
			}
			writes++
			return accept(ctx, c)
		},
		Accept: func(context.Context, *compiledEnvironmentDeployment) error { return nil },
	}
	setFixtureProof := func(q deploymentplan.Qualification) {
		now := time.Now().UTC()
		c.Proof = &environmentPreflightProof{Schema: environmentPreflightProofSchema, Environment: environment, Stack: stack, Operation: "upgrade", PlanSHA256: plan.SHA256(), QualifiedAt: now, ValidUntil: now.Add(30 * time.Minute), Qualification: q}
	}
	var fixtureGates []deploymentplan.GateResult
	for _, id := range deploymentplan.RequiredGateIDs(false)[:6] {
		fixtureGates = append(fixtureGates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: deploymentplan.Digest("isolated fixture evidence")})
	}
	setFixtureProof(deploymentplan.Qualification{PlanSHA256: plan.SHA256(), Decision: "GO", Gates: fixtureGates})
	deps.PublishProof = func(_ context.Context, _ *compiledEnvironmentDeployment, q deploymentplan.Qualification) error {
		setFixtureProof(q)
		return nil
	}
	deps.LoadQualified = func(context.Context, deploymentConfig, string, string) (*compiledEnvironmentDeployment, error) {
		return c, nil
	}
	deps.LiveChecks = func(c *compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck { return deps.Checks(c) }
	return c, deps, &writes
}

func TestUnifiedPublicFlowUsesSamePlanAndRequiredGatesForAllEnvironments(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			c, deps, writes := unifiedCLIProfile(t, environment)
			args := []string{"--environment", environment}
			for _, action := range []string{"plan", "preflight", "upgrade"} {
				var out, diagnostics bytes.Buffer
				selected := append([]string{}, args...)
				store, _ := newSecretStore("", environment)
				path := filepath.Join(store.Root, "deployment", "reports", action+".json")
				selected = append(selected, "--report", path)
				if action == "upgrade" {
					selected = append(selected, "--confirm", "video-cloud-"+environment, "--plan-sha256", c.Plan.SHA256())
				}
				if err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), action, selected, &out, &diagnostics, deps); err != nil {
					t.Fatal(action, err, diagnostics.String())
				}
				var report unifiedDeploymentReport
				raw, err := os.ReadFile(path)
				if err != nil || json.Unmarshal(raw, &report) != nil {
					t.Fatal("missing report")
				}
				if report.ValidityWindowSeconds != 1800 || report.Plan == nil || report.Plan.PlanSHA256 != c.Plan.SHA256() || strings.Contains(string(raw), "never-public") || strings.Contains(out.String(), "never-public") {
					t.Fatal("changed plan or leaked desired Secret")
				}
				info, _ := os.Stat(path)
				if info.Mode().Perm() != 0600 {
					t.Fatal("public report permissions")
				}
				if report.Scope != "existing-environment-update" || action == "plan" && report.Decision != "PLANNED" || action != "plan" && report.Decision != "GO" || report.Completed != (action == "upgrade") {
					t.Fatal("scope or completion changed")
				}
				if strings.Contains(out.String(), "[OK]") || strings.Contains(out.String(), "[PASS]") || !strings.Contains(out.String(), report.Decision+" environment="+environment) {
					t.Fatal("success rows were displayed or final verdict was lost", out.String())
				}
				if action != "plan" {
					if len(report.Gates) != map[string]int{"preflight": 6, "upgrade": 11}[action] || report.ReusedPreflight != (action == "upgrade") {
						t.Fatal("saved qualification or current execution evidence was removed from the report")
					}
					for _, gate := range report.Gates {
						if gate.Status != "PASS" || len(gate.EvidenceSHA256) != 64 {
							t.Fatal("successful gate lost its evidence", gate)
						}
					}
				}
			}
			if *writes != 1 {
				t.Fatal("planning/preflight wrote resources")
			}
		})
	}
}

func TestPublicUpdateHasEveryRuntimeGateAndAcceptanceAdapter(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		c, _, _ := unifiedCLIProfile(t, environment)
		deps := defaultUnifiedDeploymentDependencies()
		checks := deps.Checks(c)
		for _, id := range []string{"deployment.target", "deployment.release", "deployment.execution", "deployment.pki", "deployment.compatibility", "deployment.network"} {
			if checks[id] == nil {
				t.Fatal("required public update adapter absent", environment, id)
			}
		}
		if deps.Accept == nil || deps.Execute == nil || deps.CheckExecutionAvailable == nil || deps.PrepareStorage == nil || deps.PublishBindings == nil || deps.RequireBindings == nil || deps.InvalidateProof == nil || deps.PublishProof == nil || deps.LoadQualified == nil || deps.LiveChecks == nil {
			t.Fatal("public update cannot execute or accept")
		}
	}
}

func TestRolloutAcceptanceReusesRequiredReadOnlyHealthChecksAcrossEnvironments(t *testing.T) {
	if defaultUnifiedDeploymentDependencies().Accept == nil {
		t.Fatal("public deployment has no rollout acceptance")
	}
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"healthy", "secret-failure", "pki-failure", "workload-failure", "public-failure", "cancelled"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				checks, _ := facadeFixture(t, true)
				store := makeIsolatedTestSecretStore(t, environment)
				if err := store.write("kube/kubeconfig.yaml", []byte("test-only-kubeconfig"), true); err != nil {
					t.Fatal(err)
				}
				checks.store = func(selected string) (secretStore, error) {
					if selected != environment {
						t.Fatal("wrong health environment store")
					}
					return store, nil
				}
				c := &compiledEnvironmentDeployment{Plan: &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment}}, Configuration: deploymentConfig{Environment: environment, Workspace: t.TempDir(), Values: map[string]string{"VIDEO_CLOUD_CERTISSUER_DOMAIN": "issuer." + environment + ".example.test"}}}
				checks.resolve = func(_, selected, _ string) (deploymentConfig, error) {
					if selected != environment {
						t.Fatal("acceptance selected another environment")
					}
					return c.Configuration, nil
				}
				called := make(map[string]bool)
				var mu sync.Mutex
				result := func(name string) error {
					mu.Lock()
					called[name] = true
					mu.Unlock()
					if mode == name+"-failure" {
						return errors.New("required runtime check failed")
					}
					return nil
				}
				checks.bindings = func(secretStore) error { return result("secret") }
				checks.runtime = func(secretStore, time.Time) error { return result("pki") }
				checks.workloads = func(secretStore) error { return result("workload") }
				checks.public = func(context.Context, deploymentConfig, secretStore) error { return result("public") }
				base := checks.collect
				checks.collect = func(ctx context.Context, cfg deploymentConfig, path string, o deploymentCredentialCheckOptions, writes bool, emit func(deploymentCredentialCheck)) []deploymentCredentialCheck {
					if writes || !o.readOnly || o.fast {
						t.Fatal("acceptance allows writes or reduced health verification")
					}
					return base(ctx, cfg, path, o, writes, emit)
				}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "cancelled" {
					cancel()
				}
				var out bytes.Buffer
				err := acceptEnvironmentRollout(ctx, c, checks, &out)
				if (err == nil) != (mode == "healthy") {
					t.Fatal("incorrect rollout acceptance", err, out.String())
				}
				if mode != "cancelled" {
					for _, name := range []string{"secret", "pki", "workload", "public"} {
						if !called[name] {
							t.Fatal("required health check omitted", name, out.String())
						}
					}
				}
				if err != nil {
					var block *deploymentplan.Blocker
					if !errors.As(err, &block) || block.ID != "deployment.post-deploy" || block.Next == "" {
						t.Fatal("inactionable acceptance failure", err)
					}
				}
			})
		}
	}
}

func TestUnifiedPublicFlowNeverExecutesIncompleteEvidenceOrUnreviewedPlan(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"missing-gate", "failed-gate", "missing-acceptance", "wrong-plan", "partial-failure", "acceptance-failure"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				c, deps, writes := unifiedCLIProfile(t, environment)
				original := deps.Checks
				deps.LiveChecks = func(c *compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
					checks := original(c)
					if mode == "missing-gate" {
						delete(checks, "deployment.pki")
					}
					if mode == "failed-gate" {
						checks["deployment.compatibility"] = func(context.Context, *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
							return deploymentplan.GateEvidence{}, errors.New("private failure=never-public")
						}
					}
					return checks
				}
				if mode == "missing-acceptance" {
					deps.Accept = nil
				}
				if mode == "partial-failure" {
					deps.Execute = func(ctx context.Context, compiled *compiledEnvironmentDeployment, _ string, recheck func(context.Context, *deploymentplan.Plan) error, _ func(context.Context, *compiledEnvironmentDeployment) error) error {
						if err := recheck(ctx, compiled.Plan); err != nil {
							return err
						}
						*writes++
						return errors.New("private partial=never-public")
					}
				}
				if mode == "acceptance-failure" {
					deps.Accept = func(context.Context, *compiledEnvironmentDeployment) error { return errors.New("never-public") }
				}
				sha := c.Plan.SHA256()
				if mode == "wrong-plan" {
					sha = strings.Repeat("b", 64)
				}
				action := "upgrade"
				args := []string{"--environment", environment, "--confirm", "video-cloud-" + environment, "--plan-sha256", sha}
				var out, diagnostics bytes.Buffer
				err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), action, args, &out, &diagnostics, deps)
				if err != exitCode(1) || !strings.Contains(out.String(), "NO-GO") || strings.Contains(out.String(), "never-public") || strings.Contains(out.String(), "completed=true") || strings.Contains(out.String(), "[OK]") {
					t.Fatal(mode, err, out.String())
				}
				if mode != "partial-failure" && mode != "acceptance-failure" && *writes != 0 {
					t.Fatal("unqualified environment was mutated")
				}
			})
		}
	}
}

func TestUnifiedEntryRejectsRetiredActionsAndBadInputs(t *testing.T) {
	for _, args := range [][]string{{"--deploy"}, {"--apply"}, {"--all"}, {"--reset-and-all"}} {
		args = append(args, "--confirm", "video-cloud-dev", "--env-root", "/reviewed/dev")
		if err := runProvisionCompatibilityEntry(args); err == nil || !strings.Contains(err.Error(), "retired") {
			t.Fatal("direct provision bypasses common complete gates", args)
		}
	}
	for _, action := range []string{"prepare-upgrade", "provision", "test"} {
		if err := runDeployment([]string{action, "--environment", "dev"}); err == nil || !strings.Contains(err.Error(), "retired") {
			t.Fatal("legacy fallback remained reachable", action)
		}
	}
	for _, args := range [][]string{{"--environment", "dev"}, {"--environment", "dev", "--plan-sha256", "bad"}, {"--environment", "dev", "--plan-sha256", strings.Repeat("a", 64), "--fast"}, {"--environment", "dev", "--plan-sha256", strings.Repeat("a", 64), "--operation", "create"}} {
		if _, err := parseUnifiedDeploymentOptions("upgrade", args, &bytes.Buffer{}); err == nil {
			t.Fatal("invalid execution flags accepted", args)
		}
	}
}

func TestUnifiedReportsRejectWorktreesOtherEnvironmentsAndSymlinkParents(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			_, deps, _ := unifiedCLIProfile(t, environment)
			deps.Compile = func(context.Context, deploymentConfig, string) (*compiledEnvironmentDeployment, error) {
				t.Fatal("invalid report destination reached planning")
				return nil, nil
			}
			store, _ := newSecretStore("", environment)
			for _, path := range []string{filepath.Join(t.TempDir(), "report.json"), filepath.Join(store.ConfigRoot, "other", "deployment", "reports", "report.json"), filepath.Join(store.Root, "pki", "report.json"), filepath.Join(store.Root, "deployment", "reports", "report.txt")} {
				var out, diagnostics bytes.Buffer
				err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "preflight", []string{"--environment", environment, "--report", path}, &out, &diagnostics, deps)
				if err != exitCode(2) {
					t.Fatal("deployment report escaped its environment authority", err)
				}
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("rejected report was created")
				}
			}
			if err := os.MkdirAll(filepath.Join(store.Root, "deployment"), 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(t.TempDir(), filepath.Join(store.Root, "deployment", "reports")); err != nil {
				t.Fatal(err)
			}
			if err := validateEnvironmentDeploymentReportPath(environment, filepath.Join(store.Root, "deployment", "reports", "report.json")); err == nil {
				t.Fatal("symlink report parent accepted")
			}
		})
	}
}

func TestUnifiedReportsRetainSanitizedAcceptanceResourceAndRepair(t *testing.T) {
	c, deps, _ := unifiedCLIProfile(t, "dev")
	deps.Accept = func(context.Context, *compiledEnvironmentDeployment) error {
		return &deploymentplan.Blocker{ID: "deployment.post-deploy.workloads", Resource: "Deployment/video-cloud-dev-application/application", Cause: "runtime image cannot be verified", Next: "repair the original planned runtime"}
	}
	var out, diagnostics bytes.Buffer
	err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "upgrade", []string{"--environment", "dev", "--confirm", "video-cloud-dev", "--plan-sha256", c.Plan.SHA256()}, &out, &diagnostics, deps)
	if err == nil || !strings.Contains(out.String(), "deployment.post-deploy.workloads") || !strings.Contains(out.String(), "Deployment/video-cloud-dev-application/application") || !strings.Contains(out.String(), "repair the original planned runtime") || !strings.Contains(out.String(), "completed=false") {
		t.Fatal("acceptance repair lost", err, out.String())
	}
}

func TestDefaultUnifiedExecutionKeepsAdmittedCallerDeadline(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, duration := range []time.Duration{time.Minute, 90 * time.Minute} {
			t.Run(environment+"/"+duration.String(), func(t *testing.T) {
				c, _, _ := unifiedCLIProfile(t, environment)
				c.Proof.ValidUntil = time.Now().Add(50 * time.Millisecond)
				ctx, cancel := context.WithTimeout(context.Background(), duration)
				defer cancel()
				parentDeadline, _ := ctx.Deadline()
				ctx, admission, err := admitQualifiedEnvironmentExecution(ctx, c, time.Now().UTC())
				if err != nil {
					t.Fatal(err)
				}
				defer admission.active.Store(false)
				time.Sleep(time.Until(c.Proof.ValidUntil) + time.Millisecond)
				called := 0
				stop := errors.New("qualification-stop")
				qualify := func(ctx context.Context, _ *deploymentplan.Plan) error {
					called++
					deadline, ok := ctx.Deadline()
					if !ok || !deadline.Equal(parentDeadline) {
						t.Fatal("native execution replaced the admitted caller deadline")
					}
					return stop
				}
				accept := func(context.Context, *compiledEnvironmentDeployment) error {
					t.Fatal("acceptance ran after failed live checks")
					return nil
				}
				execute := defaultUnifiedDeploymentDependencies().Execute
				if err = execute(ctx, c, c.Plan.SHA256(), qualify, accept); called != 1 || !errors.Is(err, stop) {
					t.Fatal("admitted live checks not reached", err)
				}
				if err = execute(ctx, c, c.Plan.SHA256(), qualify, accept); err == nil || called != 1 {
					t.Fatal("admission token was replayed", err)
				}
			})
		}
	}
}

func TestUnifiedPreflightPreparesBucketBeforeCompilationAndStopsOnFailure(t *testing.T) {
	for _, fail := range []bool{false, true} {
		_, deps, _ := unifiedCLIProfile(t, "dev")
		calls, compiled := 0, 0
		compile := deps.Compile
		deps.PrepareStorage = func(context.Context, deploymentConfig, io.Writer) error {
			calls++
			if fail {
				return errors.New("fixture failure")
			}
			return nil
		}
		deps.Compile = func(ctx context.Context, cfg deploymentConfig, operation string) (*compiledEnvironmentDeployment, error) {
			compiled++
			return compile(ctx, cfg, operation)
		}
		var out bytes.Buffer
		err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "preflight", []string{"--environment", "dev"}, &out, &out, deps)
		if calls != 1 || (err != nil) != fail || (compiled == 0) != fail {
			t.Fatalf("prepare=%d compile=%d err=%v", calls, compiled, err)
		}
		if fail && !strings.Contains(out.String(), "provider.storage.media.bucket") {
			t.Fatal("bucket failure not reported")
		}
	}
}

func TestUnifiedPreflightReportsUnsupportedKubeconfigWithoutPrivatePayload(t *testing.T) {
	_, deps, writes := unifiedCLIProfile(t, "dev")
	kube := frozenKubernetesFixture(t)
	user := kube["users"].([]any)[0].(map[string]any)["user"].(map[string]any)
	user["as-user-extra"] = map[string]any{"private-extra": []any{"never-public-impersonation"}}
	raw, _ := json.Marshal(kube)
	deps.Compile = func(context.Context, deploymentConfig, string) (*compiledEnvironmentDeployment, error) {
		_, err := newEnvironmentKubernetesBackend(string(raw), "dev")
		return nil, err
	}
	var out bytes.Buffer
	err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "preflight", []string{"--environment", "dev"}, &out, &out, deps)
	if err != exitCode(1) || *writes != 0 || !strings.Contains(out.String(), "deployment.access.kubeconfig") || strings.Contains(out.String(), "never-public-impersonation") || strings.Contains(out.String(), "private-frozen-token") {
		t.Fatal("preflight lost the safe authentication cause or leaked private data", err)
	}
}

func TestPublicDeploymentHasNoLegacyPKIRenderer(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	forbidden := []string{"lkeCertIssuerDeploymentManifest", "lkeFactoryEnrollDeploymentManifest", "lkePlanCertIssuerIngressMigrationForStaticRenderWithContext", "lkePlanCertIssuerIngressMigrationForStaticRenderWithIO", "deploymentCertIssuerIngressReadiness", "loadManagedUpgradePlan"}
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, name := range forbidden {
			if bytes.Contains(source, []byte("func "+name+"(")) {
				t.Fatalf("retired runtime renderer %s exists in %s", name, path)
			}
		}
	}
}

func TestPreflightProducesBindingsAndDeploymentConsumesExactEvidence(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "missing", "failed-recheck", "compiler-failure", "tampered", "plan-drift"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				c, deps, writes := unifiedCLIProfile(t, environment)
				c.Store, _ = newSecretStore("", environment)
				c.Bindings = environmentIdentityBindings{Version: 1, Environment: environment, Stack: c.Plan.Stack, Owners: map[string]string{}}
				c.Plan.BindingsSHA256 = deploymentplan.Digest(c.Bindings)
				defaults := defaultUnifiedDeploymentDependencies()
				deps.PublishBindings, deps.RequireBindings = defaults.PublishBindings, defaults.RequireBindings
				publishProof := deps.PublishProof
				deps.PublishProof = func(ctx context.Context, compiled *compiledEnvironmentDeployment, qualification deploymentplan.Qualification) error {
					if err := publishProof(ctx, compiled, qualification); err != nil {
						return err
					}
					// Native proof publication owns the matching GO observation.
					// This isolated fixture models that publication boundary too.
					return defaults.PublishBindings(compiled.Configuration, compiled, "GO")
				}
				path := filepath.Join(c.Store.Root, "deployment", "identity-bindings.json")
				if _, err := os.Stat(path); !os.IsNotExist(err) {
					t.Fatal("fixture already had bindings")
				}
				var out, diagnostics bytes.Buffer
				if mode != "missing" {
					if err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "preflight", []string{"--environment", environment}, &out, &diagnostics, deps); err != nil {
						t.Fatal("first preflight required prior deployment", err, out.String(), diagnostics.String())
					}
					info, err := os.Stat(path)
					if err != nil || info.Mode().Perm() != 0600 {
						t.Fatal("preflight did not publish private bindings", err)
					}
					dir, _ := os.Stat(filepath.Dir(path))
					if dir.Mode().Perm() != 0700 {
						t.Fatal("bindings directory not private")
					}
					raw, _ := os.ReadFile(path)
					if strings.Contains(string(raw), "never-public") || strings.Contains(string(raw), "stringData") {
						t.Fatal("target Secret leaked into observed bindings")
					}
				}
				switch mode {
				case "failed-recheck", "compiler-failure":
					args := []string{"--environment", environment}
					if mode == "failed-recheck" {
						deps.Checks = func(*compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
							return map[string]deploymentplan.GateCheck{}
						}
					}
					if mode == "compiler-failure" {
						deps.Compile = func(context.Context, deploymentConfig, string) (*compiledEnvironmentDeployment, error) {
							return nil, errors.New("private-error")
						}
					}
					if err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "preflight", args, &out, &diagnostics, deps); err != exitCode(1) {
						t.Fatal("incomplete preflight passed", err)
					}
					if verifyEnvironmentPreflightBindings(c) == nil {
						t.Fatal("failed preflight retained old GO")
					}
				case "tampered":
					if err := c.Store.write("deployment/identity-bindings.json", []byte(`{"schema":"caller-supplied","decision":"GO"}`), true); err != nil {
						t.Fatal(err)
					}
				case "plan-drift":
					c.Plan.ConfigurationSHA256 = "changed"
				}
				// Deployment consumes qualification and checks only current mutable prerequisites.
				cleanChecks := func(*compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
					checks := map[string]deploymentplan.GateCheck{}
					for _, id := range deploymentplan.RequiredGateIDs(environment == "prod") {
						checks[id] = func(context.Context, *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
							return deploymentplan.GateEvidence{SHA256: deploymentplan.Digest("fixture")}, nil
						}
					}
					return checks
				}
				deps.Compile = func(context.Context, deploymentConfig, string) (*compiledEnvironmentDeployment, error) {
					t.Fatal("deployment recompiled after preflight")
					return nil, nil
				}
				deps.LiveChecks = cleanChecks
				err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "upgrade", []string{"--environment", environment, "--confirm", c.Plan.Stack, "--plan-sha256", c.Plan.SHA256()}, &out, &diagnostics, deps)
				if (err == nil) != (mode == "valid") || *writes != map[bool]int{true: 1, false: 0}[mode == "valid"] {
					t.Fatal("deployment consumed invalid evidence", err, *writes, out.String())
				}
			})
		}
	}
}
