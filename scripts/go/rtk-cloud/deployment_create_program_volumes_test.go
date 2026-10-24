package main

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedLatePlannedVolumeStagesKeepOriginalPinsAcrossInterruptedProducers(t *testing.T) {
	ledger, original, redis, _, child, journal := plannedVolumeFixture(t)
	var stages []createNormalTailVolumeStage
	for i, r := range child.Resources {
		plan := *child
		plan.Resources = []deploymentplan.Resource{r}
		j := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Owners: map[string]string{r.ID: journal.Owners[r.ID]}}
		if i != 1 && i != 3 {
			j.Completed = []string{r.ID}
			j.Accepted = true
		}
		stages = append(stages, createNormalTailVolumeStage{Stage: r.ID, Plan: &plan, Journal: j})
	}
	before := deploymentplan.Digest(ledger)
	projected, err := managedLatePlannedVolumeStagesProjection(ledger, original, redis, stages)
	if err != nil || len(projected.Volumes) != 18 || deploymentplan.Digest(ledger) != before {
		t.Fatal("genuine mixed producer/current-next-write provenance failed or mutated ledger", err)
	}
	for _, mode := range []string{"unknown-claim", "missing-owner", "cross-stage-native-id", "duplicate-stage", "missing-accepted-claim", "original-owner", "foreign-claim-body"} {
		t.Run(mode, func(t *testing.T) {
			changed := ledger
			changed.Volumes = slices.Clone(ledger.Volumes)
			selected := slices.Clone(stages)
			switch mode {
			case "unknown-claim":
				selected = selected[1:]
			case "missing-owner":
				j := selected[1].Journal
				j.Owners = map[string]string{}
				selected[1].Journal = j
			case "cross-stage-native-id":
				changed.Volumes[20].NativeID = changed.Volumes[19].NativeID
			case "duplicate-stage":
				selected = append(selected, selected[0])
			case "missing-accepted-claim":
				changed.Volumes = append(slices.Clone(changed.Volumes[:19]), changed.Volumes[20:]...)
			case "original-owner":
				changed.Volumes[0].PVCUID = "99999999-1111-4111-8111-111111111111"
			case "foreign-claim-body":
				p := *selected[1].Plan
				p.Resources = slices.Clone(p.Resources)
				p.Resources[0].Desired = deploymentplan.Clone(p.Resources[0].Desired)
				deploymentplan.Map(p.Resources[0].Desired["metadata"])["name"] = "unqualified-identity"
				p.Resources[0].ID = deploymentplan.ID(p.Resources[0].Desired)
				selected[1].Plan = &p
			}
			if _, err := managedLatePlannedVolumeStagesProjection(changed, original, redis, selected); err == nil {
				t.Fatal("foreign or unrecorded allocation qualified")
			}
		})
	}
}

func TestManagedLatePlannedVolumeStagesUseProductClaimsAndOneOriginalWorkload(t *testing.T) {
	ledger, original, redis, _, workload, written := plannedVolumeFixture(t)
	cfg, l, _ := productTargetFixture(t)
	l.Bootstrap.Environment, l.Bootstrap.Stack = cfg.Environment, ledger.Stack
	l.StorageClass = "linode-block-storage-retain"
	definitions, err := createNormalTailProductClaimDefinitions(cfg, l)
	if err != nil || len(definitions) != 2 {
		t.Fatal("maintained session/enrollment claim recipes unavailable", err)
	}
	prefix := "product-identities-" + productIdentityScope(l)[:12] + "-"
	var candidates []createNormalTailVolumeStage
	for i, d := range definitions {
		input := workload.Inputs
		input.Operation = "scoped"
		input.BindingsSHA256 = deploymentplan.Digest(d)
		plan, err := deploymentplan.Compile(input, []deploymentplan.Definition{d}, nil)
		if err != nil {
			t.Fatal(err)
		}
		id := plan.Resources[0].ID
		owner := environmentCreateVolumeOwner{NativeID: fmt.Sprint(26 + i), PVName: fmt.Sprintf("pv-product-%d", i), PVUID: fmt.Sprintf("%08d-1111-4111-8111-111111111111", 26+i), PVCUID: fmt.Sprintf("%08d-2222-4222-8222-222222222222", 26+i), PVCName: deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"]), Namespace: ledger.Stack + "-video-cloud"}
		ledger.Volumes = append(ledger.Volumes, owner)
		ledger.Kubernetes[id] = owner.PVCUID
		journal := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Completed: []string{id}, Owners: map[string]string{id: owner.PVCUID}, Accepted: true}
		stage := prefix + "service-session"
		if i == 1 {
			stage = prefix + "enroll-" + strings.TrimPrefix(l.Outputs[0].Subject, "service:")
			journal.Completed = nil
			journal.Accepted = false
		}
		candidates = append(candidates, createNormalTailVolumeStage{Stage: stage, Plan: plan, Journal: journal})
	}
	// The stage validator also returns these two full workload children. Only
	// prep producers feed product allocation; initial workload is added once.
	candidates = append(candidates, createNormalTailVolumeStage{Stage: "workload", Plan: workload, Journal: written})
	steady := *workload
	steady.Resources = append([]deploymentplan.Resource(nil), workload.Resources...)
	for i := range steady.Resources {
		steady.Resources[i].Action = "retain"
		steady.Resources[i].UID = written.Owners[steady.Resources[i].ID]
	}
	candidates = append(candidates, createNormalTailVolumeStage{Stage: prefix + "steady", Plan: &steady, Journal: deploymentplan.Journal{PlanSHA256: steady.SHA256(), Owners: written.Owners}})
	var sources []createNormalTailVolumeStage
	for _, stage := range candidates {
		if productVolumeStageSelected(l, stage.Stage) {
			sources = append(sources, stage)
		}
	}
	if len(sources) != 2 {
		t.Fatal("workload/steady leaked into product allocation sources")
	}
	sources = append(sources, createNormalTailVolumeStage{Stage: "workload", Plan: workload, Journal: written})
	before := deploymentplan.Digest(ledger)
	projected, err := managedLatePlannedVolumeStagesProjection(ledger, original, redis, sources)
	if err != nil || len(projected.Volumes) != 18 || deploymentplan.Digest(ledger) != before {
		t.Fatal("mixed accepted/next-written product and workload allocations failed", err)
	}
	if _, err := managedLatePlannedVolumeStagesProjection(ledger, original, redis, append(sources, sources[len(sources)-1])); err == nil {
		t.Fatal("same workload was counted twice")
	}
	if _, err := managedLatePlannedVolumeStagesProjection(ledger, original, redis, sources[1:]); err == nil {
		t.Fatal("missing native session claim provenance inferred from its name")
	}
}
