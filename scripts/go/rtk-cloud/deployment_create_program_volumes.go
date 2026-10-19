package main

import (
	"context"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Allocation authority is the original compiled child, not a claim-name prefix
// or a completed whole workload. Product preparation and the final workload use
// the same persisted write owner / exact next-resource journal contract.
func readCreateNormalTailVolumeStages(store secretStore, e *environmentCreateExecution) ([]createNormalTailVolumeStage, error) {
	var stages []createNormalTailVolumeStage
	if createOpenBaoACKCompletionEntry(e.Journal, "normal-tail/program") != nil {
		program, err := readCreateNormalTailProgram(store, e)
		if err != nil {
			return nil, err
		}
		stages, err = readCreateNormalTailProductVolumeStages(store, e, program.Products)
		if err != nil {
			return nil, err
		}
	}
	if createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload") != nil {
		frozen, err := readCreateNormalTailFrozenTarget(store, e)
		if err != nil {
			return nil, err
		}
		workload, err := readCreateNormalTailWorkloadSources(store, e, frozen)
		if err != nil {
			return nil, err
		}
		stages = append(stages, workload...)
	}
	return stages, nil
}

func managedLateVolumeStageClaims(stages []createNormalTailVolumeStage) (map[string]int, []map[string]managedLatePlannedVolumeClaim, error) {
	index := map[string]int{}
	claims := make([]map[string]managedLatePlannedVolumeClaim, len(stages))
	for i, stage := range stages {
		var err error
		claims[i], err = managedLatePlannedVolumeClaims(stage.Plan)
		if err != nil {
			return nil, nil, err
		}
		for id := range claims[i] {
			if old, repeated := index[id]; repeated {
				prior := claims[old][id]
				next := claims[i][id]
				if stage.originalPlanSHA256 != stages[old].Plan.SHA256() || stages[old].Journal.Owners[prior.ResourceID] != "" || prior.ResourceID != next.ResourceID || prior.PodName != next.PodName || deploymentplan.Digest(prior.Claim) != deploymentplan.Digest(next.Claim) {
					return nil, nil, createOpenBaoACKBlock("planned-volume-stage-duplicate")
				}
			}
			index[id] = i
		}
	}
	return index, claims, nil
}

func managedLatePlannedVolumeStagesProjection(ledger environmentCreateResourceLedger, original map[string]environmentCreateVolumeOwner, redis *environmentCreateVolumeOwner, stages []createNormalTailVolumeStage) (environmentCreateResourceLedger, error) {
	bad := func() (environmentCreateResourceLedger, error) {
		return environmentCreateResourceLedger{}, createOpenBaoACKBlock("planned-volume-program")
	}
	index, claims, err := managedLateVolumeStageClaims(stages)
	if err != nil {
		return environmentCreateResourceLedger{}, err
	}
	base := ledger
	base.Volumes = nil
	selected := make([][]environmentCreateVolumeOwner, len(stages))
	native, pvc, pv, ids := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, volume := range ledger.Volumes {
		id := "PersistentVolumeClaim/" + volume.Namespace + "/" + volume.PVCName
		if native[volume.NativeID] || pvc[volume.PVCUID] || pv[volume.PVUID] || ids[id] {
			return bad()
		}
		native[volume.NativeID], pvc[volume.PVCUID], pv[volume.PVUID], ids[id] = true, true, true, true
		if _, old := original[id]; old || redis != nil && volume == *redis {
			base.Volumes = append(base.Volumes, volume)
			continue
		}
		i, ok := index[id]
		if !ok {
			return bad()
		}
		selected[i] = append(selected[i], volume)
	}
	// Every linked child retains its literal predecessor. An accepted
	// descendant makes all known ancestor allocations mandatory, including
	// claims allocated by an intermediate failed StatefulSet execution.
	for i, forward := range stages {
		ancestor, before := forward.originalPlanSHA256, i
		for ancestor != "" {
			selected := -1
			for n := 0; n < before; n++ {
				if stages[n].Plan.SHA256() == ancestor {
					if selected != -1 {
						return bad()
					}
					selected = n
				}
			}
			if selected == -1 {
				return bad() // unknown, later or cyclic allocation predecessor
			}
			original := stages[selected]
			if forward.Journal.Accepted {
				for id, claim := range claims[selected] {
					if original.Journal.Owners[claim.ResourceID] != "" && !ids[id] {
						return bad()
					}
				}
			}
			ancestor, before = original.originalPlanSHA256, selected
		}
	}

	for i, stage := range stages {
		part := ledger
		part.Volumes = append(append([]environmentCreateVolumeOwner(nil), base.Volumes...), selected[i]...)
		if _, err := managedLatePlannedVolumeProjection(part, original, redis, stage.Plan, stage.Plan, stage.Journal); err != nil {
			return environmentCreateResourceLedger{}, err
		}
	}
	return managedLateRedisDurableVolumeProjection(base, original, redis)
}

func observeManagedLatePlannedVolumeStages(ctx context.Context, backend environmentKubernetesBackend, ledger environmentCreateResourceLedger, original map[string]environmentCreateVolumeOwner, redis *environmentCreateVolumeOwner, stages []createNormalTailVolumeStage) (environmentCreateResourceLedger, error) {
	index, claims, err := managedLateVolumeStageClaims(stages)
	if err != nil {
		return environmentCreateResourceLedger{}, err
	}
	selected := ledger
	selected.Kubernetes = map[string]string{}
	for id, uid := range ledger.Kubernetes {
		selected.Kubernetes[id] = uid
	}
	for _, volume := range ledger.Volumes {
		id := "PersistentVolumeClaim/" + volume.Namespace + "/" + volume.PVCName
		if _, old := original[id]; old || redis != nil && volume == *redis {
			continue
		}
		i, ok := index[id]
		if !ok {
			return environmentCreateResourceLedger{}, createOpenBaoACKBlock("planned-volume-program-native")
		}
		planned, stage := claims[i][id], stages[i]
		uid, err := observeManagedLatePlannedVolume(ctx, backend, volume, planned, stage.Plan, stage.Journal)
		if err != nil {
			return environmentCreateResourceLedger{}, err
		}
		if prior := selected.Kubernetes[id]; prior != "" && prior != volume.PVCUID {
			return environmentCreateResourceLedger{}, createOpenBaoACKBlock("planned-volume-capture-owner")
		}
		if prior := selected.Kubernetes[planned.ResourceID]; prior != "" && prior != uid {
			return environmentCreateResourceLedger{}, createOpenBaoACKBlock("planned-volume-capture-owner")
		}
		selected.Kubernetes[id], selected.Kubernetes[planned.ResourceID] = volume.PVCUID, uid
	}
	if _, err := managedLatePlannedVolumeStagesProjection(selected, original, redis, stages); err != nil {
		return environmentCreateResourceLedger{}, err
	}
	return selected, ctx.Err()
}
