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
	"reflect"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type earlyExecutionLeaseAPI struct {
	executionLeaseAPIFixture
	reads   []string
	readErr error
}

func (f *earlyExecutionLeaseAPI) Read(ctx context.Context, id string) (deploymentplan.Object, error) {
	f.reads = append(f.reads, id)
	if f.readErr != nil {
		return nil, f.readErr
	}
	return f.executionLeaseAPIFixture.Read(ctx, id)
}

func earlyLeaseCLIProfile(t *testing.T, environment string) (*compiledEnvironmentDeployment, unifiedDeploymentDependencies, secretStore, *int) {
	t.Helper()
	c, deps, writes := unifiedCLIProfile(t, environment)
	c.Bindings = environmentIdentityBindings{Version: 1, Environment: environment, Stack: c.Plan.Stack, Owners: map[string]string{}}
	c.Plan.BindingsSHA256 = deploymentplan.Digest(c.Bindings)
	store, err := newSecretStore("", environment)
	if err != nil {
		t.Fatal(err)
	}
	if err := publishEnvironmentPreflightBindings(c.Configuration, c, "GO"); err != nil {
		t.Fatal("could not seed successful prior preflight evidence", err)
	}
	return c, deps, store, writes
}

func TestUnifiedPreflightRejectsExecutionOwnersBeforePreparationOrCompilation(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"local-owned", "remote-owned", "unreadable", "timeout", "cancelled", "unclassified"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				c, deps, store, writes := earlyLeaseCLIProfile(t, environment)
				api := &earlyExecutionLeaseAPI{}
				if mode == "local-owned" {
					if err := os.Mkdir(filepath.Join(store.Root, "deployment", ".execution-active"), 0700); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "remote-owned" {
					api.object = environmentExecutionLeaseBody(c.Plan, "private-original-holder")
				}
				if mode == "unreadable" {
					api.readErr = errors.New("private-api-credential-output")
				}
				before := deploymentplan.Digest(api.object)
				trace := []string{}
				deps.PublishBindings = func(cfg deploymentConfig, compiled *compiledEnvironmentDeployment, decision string) error {
					trace = append(trace, "invalidate")
					if compiled != nil || decision != "NO-GO" {
						t.Fatal("blocked startup published compiled or successful evidence")
					}
					return publishEnvironmentPreflightBindings(cfg, compiled, decision)
				}
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				deps.CheckExecutionAvailable = func(ctx context.Context, cfg deploymentConfig, operation string) error {
					if operation != "upgrade" {
						t.Fatal("startup guard lost the selected operation")
					}
					trace = append(trace, "early")
					if cfg.Environment != environment {
						t.Fatal("startup guard inspected another environment")
					}
					if mode == "timeout" || mode == "cancelled" {
						if mode == "cancelled" {
							cancel()
						}
						<-ctx.Done()
						return fmt.Errorf("private-execution-wrapper: %w", ctx.Err())
					}
					if mode == "unclassified" {
						return errors.New("private-unclassified-output")
					}
					return fmt.Errorf("private-execution-wrapper: %w", checkEnvironmentExecutionOccupancy(ctx, api, c.Plan, store.Root))
				}
				deps.PrepareStorage = func(context.Context, deploymentConfig, io.Writer) error {
					t.Fatal("blocked execution prepared or created a storage bucket")
					return nil
				}
				deps.Compile = func(context.Context, deploymentConfig, string) (*compiledEnvironmentDeployment, error) {
					t.Fatal("blocked execution reached CI selection or compilation")
					return nil, nil
				}
				deps.Checks = func(*compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
					t.Fatal("blocked execution reached full qualification")
					return nil
				}
				path := filepath.Join(store.Root, "deployment", "reports", "startup.json")
				args := []string{"--environment", environment, "--report", path}
				if mode == "timeout" {
					args = append(args, "--timeout", "20ms")
				}
				var out, diagnostics bytes.Buffer
				err := runUnifiedEnvironmentDeploymentWithDependencies(parent, "preflight", args, &out, &diagnostics, deps)
				if err != exitCode(1) || *writes != 0 || !reflect.DeepEqual(trace, []string{"invalidate", "early"}) {
					t.Fatal("startup failure did not stop immediately after invalidating GO", err, trace)
				}
				var report unifiedDeploymentReport
				raw, readErr := os.ReadFile(path)
				if readErr != nil || json.Unmarshal(raw, &report) != nil || report.Decision != "NO-GO" || report.Completed || report.Plan != nil || len(report.Gates) != 1 || report.Gates[0].ID != "deployment.execution" || report.Gates[0].Status != "FAIL" || !report.Gates[0].Required {
					t.Fatal("startup failure reported a plan, successful gate, or completion", readErr, string(raw))
				}
				if !strings.Contains(out.String(), "[RUNNING] deployment.execution.lease") || strings.Contains(out.String(), "[PASS]") || strings.Contains(out.String(), "plan-sha256:") || strings.Contains(out.String()+diagnostics.String()+string(raw), "private-") {
					t.Fatal("startup diagnostics leaked private output or implied full qualification", out.String(), diagnostics.String())
				}
				if mode == "timeout" && !strings.Contains(report.Gates[0].Cause, "timed out") || mode == "cancelled" && !strings.Contains(report.Gates[0].Cause, "cancelled") {
					t.Fatal("startup interruption lost its actionable diagnosis", report.Gates[0])
				}
				var bindings environmentPreflightBindingsRecord
				raw, readErr = os.ReadFile(filepath.Join(store.Root, "deployment", "identity-bindings.json"))
				if readErr != nil || json.Unmarshal(raw, &bindings) != nil || bindings.Decision != "NO-GO" || bindings.PlanSHA256 != "" || bindings.Observed != nil {
					t.Fatal("blocked startup left an earlier GO eligible for deployment", readErr)
				}
				if api.dry != 0 || api.writes != 0 || api.deletes != 0 || deploymentplan.Digest(api.object) != before || mode == "local-owned" && len(api.reads) != 0 {
					t.Fatal("startup inspection performed admission or changed an existing owner")
				}
			})
		}
	}
}

func TestUnifiedPreflightFreeStartupRetainsFormalExecutionGateAndRaceFence(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, race := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/race=%t", environment, race), func(t *testing.T) {
				c, deps, store, writes := earlyLeaseCLIProfile(t, environment)
				trace := []string{}
				api := &earlyExecutionLeaseAPI{}
				deps.PublishBindings = func(cfg deploymentConfig, compiled *compiledEnvironmentDeployment, decision string) error {
					if decision != "NO-GO" {
						t.Fatal("orchestration published GO outside qualified proof publication")
					}
					stage := "observed"
					if compiled == nil {
						stage = "invalidate"
					}
					trace = append(trace, stage)
					return publishEnvironmentPreflightBindings(cfg, compiled, decision)
				}
				deps.CheckExecutionAvailable = func(ctx context.Context, cfg deploymentConfig, operation string) error {
					if operation != "upgrade" {
						t.Fatal("startup guard lost the selected operation")
					}
					trace = append(trace, "early")
					return checkEnvironmentExecutionOccupancy(ctx, api, c.Plan, store.Root)
				}
				deps.PrepareStorage = func(context.Context, deploymentConfig, io.Writer) error {
					trace = append(trace, "prepare")
					if race {
						api.object = environmentExecutionLeaseBody(c.Plan, "other-controller")
					}
					return nil
				}
				originalCompile, originalChecks := deps.Compile, deps.Checks
				deps.Compile = func(ctx context.Context, cfg deploymentConfig, operation string) (*compiledEnvironmentDeployment, error) {
					trace = append(trace, "compile")
					return originalCompile(ctx, cfg, operation)
				}
				deps.Checks = func(compiled *compiledEnvironmentDeployment) map[string]deploymentplan.GateCheck {
					trace = append(trace, "checks")
					checks := originalChecks(compiled)
					checks["deployment.execution"] = func(ctx context.Context, plan *deploymentplan.Plan) (deploymentplan.GateEvidence, error) {
						trace = append(trace, "execution-gate")
						return deploymentplan.GateEvidence{SHA256: plan.SHA256()}, checkEnvironmentExecutionLease(ctx, api, plan, store.Root)
					}
					return checks
				}
				publishProof, published := deps.PublishProof, 0
				deps.PublishProof = func(ctx context.Context, compiled *compiledEnvironmentDeployment, qualification deploymentplan.Qualification) error {
					trace = append(trace, "qualified")
					published++
					if qualification.Decision != "GO" || qualification.PlanSHA256 != compiled.Plan.SHA256() || compiled.PreflightGeneration != "fixture-generation" || ctx.Err() != nil {
						t.Fatal("proof publication lost its complete qualified plan or owning generation")
					}
					if err := publishProof(ctx, compiled, qualification); err != nil {
						return err
					}
					// The native proof publisher commits its matching GO observation
					// together with the active artifact; model that boundary here.
					if err := publishEnvironmentPreflightBindings(compiled.Configuration, compiled, "GO"); err != nil {
						return err
					}
					compiled.Proof.Generation = compiled.PreflightGeneration
					trace = append(trace, "active-commit")
					return nil
				}
				var out, diagnostics bytes.Buffer
				err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "preflight", []string{"--environment", environment}, &out, &diagnostics, deps)
				want := []string{"invalidate", "early", "prepare", "compile", "observed", "checks", "execution-gate"}
				if !race {
					want = append(want, "qualified", "active-commit")
				}
				if (err == nil) == race || !reflect.DeepEqual(trace, want) || published != map[bool]int{true: 0, false: 1}[race] || *writes != 0 || len(api.reads) != 2 || api.writes != 0 || api.deletes != 0 || api.dry != map[bool]int{true: 0, false: 1}[race] {
					t.Fatal("early availability replaced formal qualification or acquired ownership", err, trace, api)
				}
				if race && (!strings.Contains(out.String(), "NO-GO") || strings.Contains(out.String(), "[PASS] deployment.execution")) {
					t.Fatal("concurrent owner acquired after startup was accepted", out.String())
				}
				var observation environmentPreflightBindingsRecord
				raw, readErr := os.ReadFile(filepath.Join(store.Root, "deployment", "identity-bindings.json"))
				if readErr != nil || json.Unmarshal(raw, &observation) != nil || observation.Decision != map[bool]string{true: "NO-GO", false: "GO"}[race] || observation.PlanSHA256 != c.Plan.SHA256() {
					t.Fatal("proof publication committed GO before the race fence or lost its exact observations", readErr, observation.Decision)
				}
			})
		}
	}
}

func TestUnifiedEarlyExecutionGuardDoesNotChangePlanOrUpgradeFlow(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, action := range []string{"plan", "upgrade"} {
			t.Run(environment+"/"+action, func(t *testing.T) {
				c, deps, writes := unifiedCLIProfile(t, environment)
				deps.CheckExecutionAvailable = func(context.Context, deploymentConfig, string) error {
					t.Fatal("preflight startup guard changed another action's flow")
					return nil
				}
				args := []string{"--environment", environment}
				if action == "upgrade" {
					args = append(args, "--confirm", c.Plan.Stack, "--plan-sha256", c.Plan.SHA256())
				}
				var out, diagnostics bytes.Buffer
				if err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), action, args, &out, &diagnostics, deps); err != nil || *writes != map[string]int{"plan": 0, "upgrade": 1}[action] {
					t.Fatal("non-preflight action changed", err, diagnostics.String())
				}
			})
		}
	}
}

func TestEarlyExecutionOccupancyOnlyReadsTheSelectedLease(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"free", "remote-owned", "unreadable", "local-owned", "local-symlink", "local-uninspectable", "cancelled", "timeout"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: "video-cloud-" + environment}}
				root := t.TempDir()
				api := &earlyExecutionLeaseAPI{}
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				switch mode {
				case "remote-owned":
					api.object = environmentExecutionLeaseBody(plan, "private-holder")
				case "unreadable":
					api.readErr = errors.New("private-api-output")
				case "local-owned", "local-symlink":
					if err := os.Mkdir(filepath.Join(root, "deployment"), 0700); err != nil {
						t.Fatal(err)
					}
					active := filepath.Join(root, "deployment", ".execution-active")
					var err error
					if mode == "local-owned" {
						err = os.Mkdir(active, 0700)
					} else {
						err = os.Symlink(filepath.Join(root, "missing"), active)
					}
					if err != nil {
						t.Fatal(err)
					}
				case "local-uninspectable":
					if err := os.WriteFile(filepath.Join(root, "deployment"), []byte("private-obstruction"), 0600); err != nil {
						t.Fatal(err)
					}
				case "cancelled":
					cancel()
				case "timeout":
					var expire context.CancelFunc
					ctx, expire = context.WithDeadline(ctx, time.Now().Add(-time.Second))
					defer expire()
				}
				before := deploymentplan.Digest(api.object)
				err := checkEnvironmentExecutionOccupancy(ctx, api, plan, root)
				if (err == nil) != (mode == "free") || api.dry != 0 || api.writes != 0 || api.deletes != 0 || deploymentplan.Digest(api.object) != before || err != nil && strings.Contains(err.Error(), "private-") {
					t.Fatal("occupancy inspection mutated an owner or exposed private output", err, api)
				}
				wantReads := 0
				if mode == "free" || mode == "remote-owned" || mode == "unreadable" {
					wantReads = 1
				}
				if len(api.reads) != wantReads || wantReads == 1 && api.reads[0] != "Lease/"+plan.Stack+"-video-cloud/rtk-deployment-execution" {
					t.Fatal("startup inspection queried unrelated inventory or passed a local failure to Kubernetes", api.reads)
				}
			})
		}
	}
}

func TestEarlyExecutionDefaultGuardFindsLocalOwnerBeforeLoadingKubeconfig(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, environment)
			if err := os.MkdirAll(filepath.Join(store.Root, "deployment", ".execution-active"), 0700); err != nil {
				t.Fatal(err)
			}
			if _, err := os.Stat(store.KubeconfigPath()); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("local startup fixture unexpectedly has Kubernetes credentials")
			}
			err := checkEnvironmentExecutionAvailable(context.Background(), deploymentConfig{Environment: environment, Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + environment}}, "upgrade")
			var failure *deploymentplan.Blocker
			if !errors.As(err, &failure) || failure.ID != "deployment.execution.lease" || !strings.Contains(failure.Cause, "local execution fence") || strings.Contains(failure.Cause, "kubeconfig") {
				t.Fatal("startup inspected credentials before rejecting a local owner", err)
			}
		})
	}
}
