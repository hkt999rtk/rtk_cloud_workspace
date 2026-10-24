package main

import (
	"context"
	"os"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateNormalTailPreparationInputSurvivesBeforeAndAfterParentRequest(t *testing.T) {
	for _, requested := range []bool{false, true} {
		t.Run(map[bool]string{false: "before-parent", true: "before-admission"}[requested], func(t *testing.T) {
			_, e, a := normalTailProgramFixture(t)
			if err := saveCreateNormalTailProgramArtifact(e.Store, e, a); err != nil {
				t.Fatal(err)
			}
			stage := "product-identities-test-successor-trust"
			body := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": e.Journal.Stack + "-video-cloud", "name": "trust"}, "data": deploymentplan.Object{"root": "original"}}
			p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: e.Journal.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: a.Program.MaintainedConfigSHA256, ReleaseSHA256: deploymentplan.Digest(a.Program.Release), BindingsSHA256: deploymentplan.Digest("stage"), CreateParentSHA256: e.Journal.PlanSHA256, CreateHandoffSHA256: a.Program.HandoffSHA256}, []deploymentplan.Definition{{Object: body, Phase: "configuration"}}, nil)
			if err != nil {
				t.Fatal(err)
			}
			if err = saveCreateNormalTailStagePlan(t.Context(), e.Store, e, stage, p); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(createNormalTailStagePlanPath(e, stage))
			if err != nil {
				t.Fatal(err)
			}
			if requested {
				intent, _ := environmentCreateKubernetesStageIntent(p)
				if err = e.BeginEntry("kubernetes/"+stage, stage, intent, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			original, err := readCreateNormalTailPreparationPlan(e.Store, e, stage)
			if err != nil || original == nil || original.SHA256() != p.SHA256() {
				t.Fatal("original input lost", err)
			}
			if child, _, err := readCreateNormalTailStage(e.Store, e, stage, p); err != nil || child != nil {
				t.Fatal("unstarted child adopted or blocked", err)
			}
			if err = saveCreateNormalTailStagePlan(t.Context(), e.Store, e, stage, p); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(createNormalTailStagePlanPath(e, stage))
			if string(before) != string(after) {
				t.Fatal("retry replaced original preparation input")
			}
			changed := *p
			changed.StorageSHA256 = deploymentplan.Digest("foreign")
			if saveCreateNormalTailStagePlan(t.Context(), e.Store, e, stage, &changed) == nil {
				t.Fatal("changed original preparation accepted")
			}
		})
	}
}

func TestCreateNormalTailPreparationInputRefusesCancellationAndMissingOwner(t *testing.T) {
	_, e, a := normalTailProgramFixture(t)
	if err := saveCreateNormalTailProgramArtifact(e.Store, e, a); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if saveCreateNormalTailStagePlan(ctx, e.Store, e, "stage", nil) == nil {
		t.Fatal("cancelled publication accepted")
	}
	if _, err := os.Lstat(createNormalTailStagePlanPath(e, "stage")); !os.IsNotExist(err) {
		t.Fatal("cancelled publication created input")
	}
	if saveCreateNormalTailStagePlan(t.Context(), e.Store, nil, "stage", nil) == nil {
		t.Fatal("missing owner accepted")
	}
}
