package main

import (
	"slices"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateNormalTailWorkloadForwardAllocationSources(t *testing.T) {
	ledger, original, redis, _, p, written := plannedVolumeFixture(t)
	old := written
	old.Owners = map[string]string{}
	for _, r := range p.Resources[:3] {
		old.Owners[r.ID] = written.Owners[r.ID]
	}
	old.Completed = slices.Clone(written.Completed)
	next := *p
	next.Resources = slices.Clone(p.Resources)
	for i := range next.Resources[:3] {
		r := &next.Resources[i]
		r.Action = "retain"
		r.UID = old.Owners[r.ID]
		r.Baseline = deploymentplan.Projection(r.Desired)
	}
	current := written
	current.PlanSHA256 = next.SHA256()
	current.Owners = map[string]string{}
	for id, uid := range written.Owners {
		current.Owners[id] = uid
	}
	sources := []createNormalTailVolumeStage{{Stage: "workload", Plan: p, Journal: old}, {Stage: "workload-forward", Plan: &next, Journal: current, originalPlanSHA256: p.SHA256()}}
	before := deploymentplan.Digest(ledger)
	got, err := managedLatePlannedVolumeStagesProjection(ledger, original, redis, sources)
	if err != nil || len(got.Volumes) != 18 || deploymentplan.Digest(ledger) != before {
		t.Fatal("original direct claims plus known forward StatefulSet allocation failed", err)
	}
	for _, mode := range []string{"missing-link", "foreign-link", "already-written-original", "changed-template", "missing-forward-owner", "unknown-forward-write", "foreign-claim", "duplicate-forward", "missing-original-at-accepted"} {
		t.Run(mode, func(t *testing.T) {
			stages := slices.Clone(sources)
			stages[0].Journal.Owners = map[string]string{}
			for id, uid := range old.Owners {
				stages[0].Journal.Owners[id] = uid
			}
			stages[1].Journal.Owners = map[string]string{}
			for id, uid := range current.Owners {
				stages[1].Journal.Owners[id] = uid
			}
			selected := ledger
			selected.Volumes = slices.Clone(ledger.Volumes)
			switch mode {
			case "missing-link":
				stages[1].originalPlanSHA256 = ""
			case "foreign-link":
				stages[1].originalPlanSHA256 = deploymentplan.Digest("foreign")
			case "already-written-original":
				stages[0].Journal.Owners[p.Resources[3].ID] = written.Owners[p.Resources[3].ID]
			case "changed-template":
				changed := next
				changed.Resources = slices.Clone(next.Resources)
				changed.Resources[3].Desired = deploymentplan.Clone(next.Resources[3].Desired)
				spec := deploymentplan.Map(changed.Resources[3].Desired["spec"])
				template := deploymentplan.Map(certIssuerObjectList(spec["volumeClaimTemplates"])[0])
				deploymentplan.Map(template["spec"])["storageClassName"] = "foreign-class"
				stages[1].Plan = &changed
				stages[1].Journal.PlanSHA256 = changed.SHA256()
			case "missing-forward-owner":
				delete(stages[1].Journal.Owners, p.Resources[3].ID)
			case "unknown-forward-write":
				stages[1].Journal.InFlight = p.Resources[3].ID
			case "foreign-claim":
				selected.Volumes[len(selected.Volumes)-1].PVCName = "foreign"
			case "duplicate-forward":
				stages = append(stages, stages[1])
			case "missing-original-at-accepted":
				stages[1].Journal.Completed = append(slices.Clone(current.Completed), next.Resources[3].ID)
				stages[1].Journal.Accepted = true
				stages[1].Journal.AcceptanceStarted = true
				selected.Volumes = append(slices.Clone(selected.Volumes[:19]), selected.Volumes[20:]...)
			}
			if _, err := managedLatePlannedVolumeStagesProjection(selected, original, redis, stages); err == nil {
				t.Fatal("unproved or changed allocation source accepted")
			}
		})
	}
}
