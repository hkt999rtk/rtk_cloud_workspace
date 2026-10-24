package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This is the existing explicit admitted-stage fixture, not public preflight
// qualification. It adds real maintained configuration, private input capture
// and a real isolated source checkout before the original Program is frozen.
// The full coordinator must apply its production fence on every later retry.
func newNormalTailCoordinatorExecutionContract(t *testing.T) *normalPostgresAccessFixture {
	t.Helper()
	workspace := writeDeploymentFixture(t, "dev", "lke")
	architecture := filepath.Join(workspace, "cloud_deploy/architectures/kubernetes/architecture.env")
	source := readTestFile(t, architecture)
	for _, key := range []string{"MQTT_FOUNDATION_REGISTRATION_ENABLED", "ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"} {
		source = strings.Replace(source, key+"=false", key+"=true", 1)
	}
	writeTestFile(t, architecture, source)
	cfg, err := resolveDeploymentConfig(workspace, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) string {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = workspace
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("isolated checkout: %v: %s", err, out)
		}
		return strings.TrimSpace(string(out))
	}
	git("init", "-q")
	git("add", ".")
	git("-c", "user.name=isolated-test", "-c", "user.email=isolated@example.invalid", "-c", "commit.gpgsign=false", "commit", "-q", "-m", "Original isolated maintained configuration")
	revision := git("rev-parse", "HEAD")
	kube, err := yaml.Marshal(frozenKubernetesFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	return newNormalPostgresAccessFixtureWithFrozenInputs(t, func(selected *deploymentConfig, e *environmentCreateExecution, a *createNormalTailProgramArtifact) {
		*selected = cfg
		for _, item := range rtkSecretCatalog() {
			if err := e.Store.write(filepath.Join("runtime", item.ID), []byte(strings.Repeat("a", 32)), true); err != nil {
				t.Fatal(err)
			}
		}
		if err := e.Store.write("kube/kubeconfig.yaml", kube, true); err != nil {
			t.Fatal(err)
		}
		a.Program.ConfigurationSHA256, err = environmentDeploymentInputDigestContext(t.Context(), cfg, e.Store)
		if err != nil {
			t.Fatal("original private input capture", err)
		}
		backend, backendErr := newEnvironmentKubernetesBackend(string(kube), cfg.Environment)
		if backendErr != nil {
			t.Fatal(backendErr)
		}
		a.Program.ClusterSHA256, a.Program.IdentitySHA256, err = environmentPreflightKubernetesIdentity(backend.KubeconfigContent)
		if err != nil {
			t.Fatal(err)
		}
		a.Program.Release = environmentRelease{Schema: "rtk-cloud.release/v1", Environment: cfg.Environment, Stack: e.Journal.Stack, WorkspaceRevision: revision, Images: map[string]environmentReleaseImage{}}
	}, nil)
}

func TestNormalTailCoordinatorRechecksAdmittedProgramWithoutResettingItsClock(t *testing.T) {
	f := newNormalTailCoordinatorExecutionContract(t)
	original, err := readCreateNormalTailProgramAdmission(f.store, f.h.Execution)
	if err != nil {
		t.Fatal(err)
	}
	artifact := readTestFile(t, createNormalTailProgramPath(f.h.Execution))
	journal := deploymentplan.Digest(f.h.Execution.Journal)
	active, err := readEnvironmentPreflightActive(f.store)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"unchanged", "configuration-drift", "runtime-drift", "source-drift", "cancelled", "unchanged-again"} {
		t.Run(mode, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			switch mode {
			case "configuration-drift":
				path := filepath.Join(f.cfg.EnvironmentRoot, "environment.env")
				before := readTestFile(t, path)
				appendFile(t, path, "UNREVIEWED_SETTING=true\n")
				defer writeTestFile(t, path, before)
			case "runtime-drift":
				path := "runtime/postgres"
				before := readTestFile(t, filepath.Join(f.store.Root, path))
				if err := f.store.write(path, []byte("changed-private-input"), true); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := f.store.write(path, []byte(before), true); err != nil {
						t.Fatal(err)
					}
				}()
			case "source-drift":
				path := filepath.Join(f.cfg.Workspace, ".git/HEAD")
				before := readTestFile(t, path)
				writeTestFile(t, path, strings.Repeat("f", 40)+"\n")
				defer writeTestFile(t, path, before)
			case "cancelled":
				cancel()
			}
			var out, diagnostics bytes.Buffer
			report := unifiedDeploymentReport{}
			err := runCreateNormalTailProgram(ctx, f.cfg, unifiedDeploymentOptions{Action: "preflight", Timeout: 3 * time.Hour, TotalTimeout: 4 * time.Hour}, f.h, &report, &out, &diagnostics)
			pass := mode == "unchanged" || mode == "unchanged-again"
			if (err == nil) != pass {
				t.Fatalf("coordinator retry %s: %v; report=%s", mode, err, out.String())
			}
			if pass {
				if report.Decision != "GO" || !report.ReusedPreflight || report.ExecutionAdmittedAt == nil || report.ExecutionDeadline == nil || !report.ExecutionAdmittedAt.Equal(original.AdmittedAt) || !report.ExecutionDeadline.Equal(original.Deadline) {
					t.Fatal("retry did not retain original admitted program and clock")
				}
			} else if report.Decision != "NO-GO" {
				t.Fatal("changed input or cancelled fence accepted")
			}
			got, err := readCreateNormalTailProgramAdmission(f.store, f.h.Execution)
			current, activeErr := readEnvironmentPreflightActive(f.store)
			if err != nil || activeErr != nil || got != original || deploymentplan.Digest(current) != deploymentplan.Digest(active) || deploymentplan.Digest(f.h.Execution.Journal) != journal || readTestFile(t, createNormalTailProgramPath(f.h.Execution)) != artifact {
				t.Fatal("retry replaced original program, admission, qualification or child history", err, activeErr)
			}
		})
	}
}

func TestNormalTailCoordinatorCreateRejectsUnselectedProgramAndUnprovedRedisBeforeWrites(t *testing.T) {
	f := newNormalTailCoordinatorExecutionContract(t)
	before := deploymentplan.Digest(f.h.Execution.Journal)
	original, err := readCreateNormalTailProgramAdmission(f.store, f.h.Execution)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"wrong-program", "unproved-redis", "wrong-kubernetes-identity", "invalid-kubeconfig", "missing-private-input"} {
		t.Run(mode, func(t *testing.T) {
			a := f.artifact
			options := unifiedDeploymentOptions{Action: "create", PlanSHA256: deploymentplan.Digest(a.Program), Timeout: 3 * time.Hour}
			var out, diagnostics bytes.Buffer
			report := unifiedDeploymentReport{}
			var err error
			switch mode {
			case "wrong-program":
				options.PlanSHA256 = strings.Repeat("f", 64)
				err = runCreateNormalTailProgram(t.Context(), f.cfg, options, f.h, &report, &out, &diagnostics)
			default:
				if mode == "wrong-kubernetes-identity" {
					a.Program.IdentitySHA256 = strings.Repeat("f", 64)
				}
				if mode == "invalid-kubeconfig" || mode == "missing-private-input" {
					path := "kube/kubeconfig.yaml"
					if mode == "missing-private-input" {
						path = "runtime/postgres"
					}
					originalBytes := readTestFile(t, filepath.Join(f.store.Root, path))
					if mode == "missing-private-input" {
						if err := os.Remove(filepath.Join(f.store.Root, path)); err != nil {
							t.Fatal(err)
						}
					} else if err := f.store.write(path, []byte("not a Kubernetes context"), true); err != nil {
						t.Fatal(err)
					}
					defer func() {
						if err := f.store.write(path, []byte(originalBytes), true); err != nil {
							t.Fatal(err)
						}
					}()
				}
				err = executeCreateNormalTailProgram(t.Context(), f.cfg, options, f.h, a, &report, &out, &diagnostics)
			}
			if err == nil || report.Decision != "NO-GO" || report.ReusedPreflight {
				t.Fatal("unselected or unproved execution crossed admission", mode, err)
			}
			want := map[string]string{"wrong-program": "selected program differs", "unproved-redis": "immutable source", "wrong-kubernetes-identity": "Kubernetes target or identity changed", "invalid-kubeconfig": "execution stopped; private execution evidence is retained", "missing-private-input": ""}[mode]
			if want != "" && !strings.Contains(out.String()+diagnostics.String(), want) {
				t.Fatal("wrong refusing boundary", mode, out.String(), diagnostics.String())
			}
			got, readErr := readCreateNormalTailProgramAdmission(f.store, f.h.Execution)
			if readErr != nil || got != original || deploymentplan.Digest(f.h.Execution.Journal) != before {
				t.Fatal("refused execution changed original clock or progress", readErr)
			}
		})
	}
}

func TestNormalTailProgramChecksUseRealAdaptersAndReportEveryUnavailablePrerequisite(t *testing.T) {
	cfg, e, a := normalTailProgramFixture(t)
	h := &createNormalTailHandoff{Execution: e, Pins: map[string]string{"original": "isolated"}, OriginalPlan: &environmentCreatePlan{}}
	p := a.Program
	p.HandoffSHA256 = deploymentplan.Digest(h.Pins)
	rendered := &renderedEnvironmentTarget{Store: e.Store, Backend: environmentKubernetesBackend{Store: e.Store}}
	checks, err := createNormalTailProgramChecks(t.Context(), cfg, h, &p, rendered)
	if err != nil {
		t.Fatal("actual static admission preview", err)
	}
	if len(checks) != len(deploymentplan.RequiredGateIDs(false)) {
		t.Fatal("coordinator omitted an independent prerequisite")
	}
	before := deploymentplan.Digest(e.Journal)
	var out bytes.Buffer
	qualification, err := qualifyCreateNormalTailProgram(t.Context(), &p, checks, &out)
	if err == nil || qualification.Decision != "NO-GO" || len(qualification.Gates) != len(checks) {
		t.Fatal("incomplete fixture obtained public qualification", err)
	}
	for i, row := range qualification.Gates {
		if row.ID != deploymentplan.RequiredGateIDs(false)[i] || !row.Required || row.Status == "PASS" && row.EvidenceSHA256 != qualification.PlanSHA256 {
			t.Fatal("actual adapter result lost its gate binding", row)
		}
	}
	for _, row := range qualification.Gates {
		if row.ID == "deployment.compatibility" {
			if row.Status != "PASS" {
				t.Fatal("disabled product/schema checks did not complete", row)
			}
		} else if row.Status != "FAIL" || row.EvidenceSHA256 != "" {
			t.Fatal("unavailable real prerequisite was accepted", row)
		}
	}
	if deploymentplan.Digest(e.Journal) != before {
		t.Fatal("read-only program qualification changed original history")
	}
	// The complete report remains serializable, without a producer replacing any
	// prerequisite callback or publishing an active GO for this incomplete source.
	if _, err := json.Marshal(qualification); err != nil {
		t.Fatal(err)
	}
}

func TestNormalTailCoordinatorFreshRefusalDoesNotPublishAProgram(t *testing.T) {
	for _, action := range []string{"plan", "preflight"} {
		t.Run(action, func(t *testing.T) {
			cfg, e, _ := normalTailProgramFixture(t)
			h := &createNormalTailHandoff{Execution: e}
			prior, err := readEnvironmentPreflightActive(e.Store)
			if err != nil {
				t.Fatal(err)
			}
			before := deploymentplan.Digest(e.Journal)
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			var out, diagnostics bytes.Buffer
			report := unifiedDeploymentReport{}
			err = runCreateNormalTailProgram(ctx, cfg, unifiedDeploymentOptions{Action: action}, h, &report, &out, &diagnostics)
			if err == nil || report.Decision != "NO-GO" || len(report.Gates) != 1 {
				t.Fatal("failed complete compile was not reported", err)
			}
			if _, err := os.Stat(createNormalTailProgramPath(e)); !os.IsNotExist(err) {
				t.Fatal("incomplete source published a Program", err)
			}
			current, err := readEnvironmentPreflightActive(e.Store)
			if err != nil || deploymentplan.Digest(e.Journal) != before {
				t.Fatal("failed qualification changed parent execution", err)
			}
			if action == "plan" {
				if deploymentplan.Digest(current) != deploymentplan.Digest(prior) {
					t.Fatal("read-only plan replaced active admission")
				}
			} else if current.Decision != "NO-GO" || current.Generation == prior.Generation {
				t.Fatal("failed fresh qualification left the previous active GO selectable")
			}
		})
	}
}
