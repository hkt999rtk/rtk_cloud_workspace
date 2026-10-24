package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateServiceSessionPrerequisitesPrecedeJob(t *testing.T) {
	cfg, p, _, s := createServiceRecipeFixture(t, "dev")
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Workspace = workspace
	raw, err := os.ReadFile(filepath.Join(workspace, "repos/rtk_video_cloud/deploy/pki/render-staging-controller.py"))
	if err != nil {
		t.Fatal(err)
	}
	p.Templates = map[string]string{"deploy/pki/render-staging-controller.py": environmentPreflightBytesSHA256(raw)}
	defs, err := renderEnvironmentCreatePKIPhase(context.Background(), cfg, p, "service-bootstrap", "--openbao-ca-configmap", "pki-openbao-transport-ca", "--service-bootstrap-subject", "service:deployment-bootstrap", "--service-bootstrap-subjects", "service:account-manager", "--service-bootstrap-issuer-id", s.Intermediate.ID, "--service-bootstrap-issuer-mount", "service-issuer/1", "--service-bootstrap-root-sha256", s.Root.RootFingerprint, "--service-bootstrap-deployment-id", "create-fixture", "--service-bootstrap-session-id", s.BootstrapSessionID)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest("fixture-config"), ReleaseSHA256: deploymentplan.Digest("fixture-release"), BindingsSHA256: deploymentplan.Digest("fixture-bindings")}, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Resources) != 3 || plan.Resources[0].Desired["kind"] != "PersistentVolumeClaim" || plan.Resources[1].Desired["kind"] != "ServiceAccount" || plan.Resources[2].Desired["kind"] != "Job" {
		t.Fatal("bootstrap Job is ordered ahead of its native prerequisites")
	}
	if deploymentplan.Map(plan.Resources[0].Desired["spec"])["storageClassName"] != firstNonEmpty(cfg.Values["LKE_STORAGE_CLASS"], "linode-block-storage-retain") {
		t.Fatal("bootstrap custody claim relies on an unqualified default storage class")
	}
}

func TestCreateServiceSessionForwardRejectsAnyActualJobExecution(t *testing.T) {
	terminal := deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Failed", "status": "True", "reason": "DeadlineExceeded"}}, "uncountedTerminatedPods": deploymentplan.Object{}}
	if !serviceSessionJobNeverRan(terminal) {
		t.Fatal("known never-executed terminal Job rejected")
	}
	for _, key := range []string{"active", "failed", "succeeded", "ready", "terminating"} {
		t.Run(key, func(t *testing.T) {
			status := deploymentplan.Clone(terminal)
			status[key] = float64(1)
			if serviceSessionJobNeverRan(status) {
				t.Fatal("native Pod execution accepted")
			}
		})
	}
	if serviceSessionJobNeverRan(deploymentplan.Object{}) {
		t.Fatal("live unstarted Job could compete with the forward executor")
	}
	status := deploymentplan.Clone(terminal)
	status["uncountedTerminatedPods"] = deploymentplan.Object{"failed": []any{"original-pod-uid"}}
	if serviceSessionJobNeverRan(status) {
		t.Fatal("unaccounted original Pod accepted")
	}
	status = deploymentplan.Clone(terminal)
	deploymentplan.Map(certIssuerObjectList(status["conditions"])[0])["reason"] = "BackoffLimitExceeded"
	if serviceSessionJobNeverRan(status) {
		t.Fatal("actual native bootstrap failure accepted for a forward attempt")
	}
}

func TestCreateServiceEnrollmentClaimsPrecedeEveryJob(t *testing.T) {
	cfg, p, runtime, s := createServiceRecipeFixture(t, "dev")
	for _, stage := range []struct {
		name   string
		render func(context.Context, deploymentConfig, environmentCreateBootstrapPlan, environmentCreateBootstrapRuntime, environmentCreateServiceCeremony) ([]deploymentplan.Definition, error)
	}{
		{"issuer-server", renderEnvironmentCreateServiceIssuerServerEnrollmentDefinitions},
		{"core", renderEnvironmentCreateServiceEnrollmentDefinitions},
	} {
		t.Run(stage.name, func(t *testing.T) {
			definitions, err := stage.render(context.Background(), cfg, p, runtime, s)
			if err != nil {
				t.Fatal(err)
			}
			plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest("fixture-config"), ReleaseSHA256: deploymentplan.Digest("fixture-release"), BindingsSHA256: deploymentplan.Digest(stage.name)}, definitions, nil)
			if err != nil {
				t.Fatal(err)
			}
			claims := map[string]bool{}
			for _, resource := range plan.Resources {
				if resource.Desired["kind"] == "PersistentVolumeClaim" {
					claims[deploymentplan.String(deploymentplan.Map(resource.Desired["metadata"])["name"])] = true
					continue
				}
				if resource.Desired["kind"] != "Job" {
					continue
				}
				for _, value := range certIssuerObjectList(environmentPKIPod(resource.Desired)["volumes"]) {
					claim := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(value)["persistentVolumeClaim"])["claimName"])
					if claim != "" && !claims[claim] {
						t.Fatal("enrollment Job precedes its private state claim", claim)
					}
				}
			}
		})
	}
}
