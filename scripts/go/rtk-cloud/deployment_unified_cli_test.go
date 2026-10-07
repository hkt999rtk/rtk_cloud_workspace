package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func unifiedCLIProfile(t *testing.T, environment string) (*compiledEnvironmentDeployment, unifiedDeploymentDependencies, *int) {
	t.Helper()
	makeIsolatedTestSecretStore(t, environment)
	stack := "video-cloud-" + environment
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: environment, Stack: stack, Operation: "upgrade", ConfigurationSHA256: deploymentplan.Digest("config"), ReleaseSHA256: deploymentplan.Digest("release"), BindingsSHA256: deploymentplan.Digest("identity")}, []deploymentplan.Definition{{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": map[string]any{"name": "private", "namespace": stack + "-platform", "labels": map[string]any{"rtk.realtek.com/stack": stack}}, "stringData": map[string]any{"credential": "never-public"}}}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	c := &compiledEnvironmentDeployment{Plan: plan, Configuration: deploymentConfig{Environment: environment, Values: map[string]string{"CLOUD_STACK_NAME": stack, "DEPLOYMENT_UPGRADE_WINDOW_SECONDS": "1800", "DEPLOYMENT_PROTECTION_PROFILE": map[bool]string{true: "production", false: "standard"}[environment == "prod"]}}}
	writes := 0
	deps := unifiedDeploymentDependencies{
		Resolve: func(string, string, string) (deploymentConfig, error) { return c.Configuration, nil },
		Compile: func(ctx context.Context, cfg deploymentConfig, operation, release string) (*compiledEnvironmentDeployment, error) {
			if operation != "upgrade" || release != "/reviewed/release.json" {
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
	return c, deps, &writes
}

func TestUnifiedPublicFlowUsesSamePlanAndRequiredGatesForAllEnvironments(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			c, deps, writes := unifiedCLIProfile(t, environment)
			args := []string{"--environment", environment, "--release", "/reviewed/release.json"}
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
		if deps.Accept == nil || deps.Execute == nil || deps.PrepareStorage == nil {
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
		for _, mode := range []string{"missing-gate", "failed-gate", "missing-acceptance", "wrong-plan", "partial-failure", "acceptance-failure", "fast"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				c, deps, writes := unifiedCLIProfile(t, environment)
				original := deps.Checks
				deps.Checks = func(c *compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
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
					deps.Execute = func(context.Context, *compiledEnvironmentDeployment, string, func(context.Context, *deploymentplan.Plan) error, func(context.Context, *compiledEnvironmentDeployment) error) error {
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
				args := []string{"--environment", environment, "--release", "/reviewed/release.json", "--confirm", "video-cloud-" + environment, "--plan-sha256", sha}
				if mode == "fast" {
					action = "preflight"
					args = []string{"--environment", environment, "--release", "/reviewed/release.json", "--fast"}
				}
				var out, diagnostics bytes.Buffer
				err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), action, args, &out, &diagnostics, deps)
				if err != exitCode(1) || !strings.Contains(out.String(), "NO-GO") || strings.Contains(out.String(), "never-public") || strings.Contains(out.String(), "completed=true") {
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
			deps.Compile = func(context.Context, deploymentConfig, string, string) (*compiledEnvironmentDeployment, error) {
				t.Fatal("invalid report destination reached planning")
				return nil, nil
			}
			store, _ := newSecretStore("", environment)
			for _, path := range []string{filepath.Join(t.TempDir(), "report.json"), filepath.Join(store.ConfigRoot, "other", "deployment", "reports", "report.json"), filepath.Join(store.Root, "pki", "report.json"), filepath.Join(store.Root, "deployment", "reports", "report.txt")} {
				var out, diagnostics bytes.Buffer
				err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "preflight", []string{"--environment", environment, "--release", "/reviewed/release.json", "--report", path}, &out, &diagnostics, deps)
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
	deps.Execute = func(context.Context, *compiledEnvironmentDeployment, string, func(context.Context, *deploymentplan.Plan) error, func(context.Context, *compiledEnvironmentDeployment) error) error {
		return &deploymentplan.Blocker{ID: "deployment.post-deploy.workloads", Resource: "Deployment/video-cloud-dev-application/application", Cause: "runtime image cannot be verified", Next: "repair the original planned runtime"}
	}
	var out, diagnostics bytes.Buffer
	err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "upgrade", []string{"--environment", "dev", "--release", "/reviewed/release.json", "--confirm", "video-cloud-dev", "--plan-sha256", c.Plan.SHA256()}, &out, &diagnostics, deps)
	if err == nil || !strings.Contains(out.String(), "deployment.post-deploy.workloads") || !strings.Contains(out.String(), "Deployment/video-cloud-dev-application/application") || !strings.Contains(out.String(), "repair the original planned runtime") || !strings.Contains(out.String(), "completed=false") {
		t.Fatal("acceptance repair lost", err, out.String())
	}
}

func TestDefaultUnifiedExecutionUsesReviewedValidityWindowAndCallerDeadline(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"window", "earlier-caller", "missing-window"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				c, _, _ := unifiedCLIProfile(t, environment)
				c.Configuration.Values["DEPLOYMENT_UPGRADE_WINDOW_SECONDS"] = "1800"
				if mode == "missing-window" {
					delete(c.Configuration.Values, "DEPLOYMENT_UPGRADE_WINDOW_SECONDS")
				}
				duration := time.Hour
				if mode == "earlier-caller" {
					duration = time.Minute
				}
				ctx, cancel := context.WithTimeout(context.Background(), duration)
				defer cancel()
				parentDeadline, _ := ctx.Deadline()
				called := false
				stop := errors.New("qualification-stop")
				start := time.Now()
				err := defaultUnifiedDeploymentDependencies().Execute(ctx, c, c.Plan.SHA256(), func(ctx context.Context, _ *deploymentplan.Plan) error {
					called = true
					deadline, ok := ctx.Deadline()
					if !ok || deadline.After(parentDeadline) || deadline.After(start.Add(1800*time.Second+time.Second)) {
						t.Fatal("execution exceeds reviewed window or caller deadline")
					}
					if mode == "window" && deadline.Before(start.Add(1799*time.Second)) {
						t.Fatal("reviewed window silently changed")
					}
					return stop
				}, func(context.Context, *compiledEnvironmentDeployment) error {
					t.Fatal("acceptance ran after failed qualification")
					return nil
				})
				if mode == "missing-window" {
					var block *deploymentplan.Blocker
					if called || !errors.As(err, &block) || block.ID != "deployment.pki.validity-window" {
						t.Fatal("missing window reached execution", err)
					}
				} else if !called || !errors.Is(err, stop) {
					t.Fatal("fenced qualification not reached", err)
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
		deps.Compile = func(ctx context.Context, cfg deploymentConfig, operation, release string) (*compiledEnvironmentDeployment, error) {
			compiled++
			return compile(ctx, cfg, operation, release)
		}
		var out bytes.Buffer
		err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "preflight", []string{"--environment", "dev", "--release", "/reviewed/release.json"}, &out, &out, deps)
		if calls != 1 || (err != nil) != fail || (compiled == 0) != fail {
			t.Fatalf("prepare=%d compile=%d err=%v", calls, compiled, err)
		}
		if fail && !strings.Contains(out.String(), "provider.storage.media.bucket") {
			t.Fatal("bucket failure not reported")
		}
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
