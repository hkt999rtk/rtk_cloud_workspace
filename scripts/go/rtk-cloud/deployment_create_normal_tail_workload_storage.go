package main

import (
	"context"
	"os"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// StorageSHA256 still identifies the original plan's existing input volumes.
// A linked forward retains allocations made by that child; those allocations
// keep their separate original write and recorded PVC/PV/CSI provenance.
func checkCreateNormalTailWorkloadStorage(ctx context.Context, c *compiledEnvironmentDeployment, plan *deploymentplan.Plan, observed map[string]deploymentplan.Object) error {
	ordinary := func() error { return c.Backend.CheckStorageBindingsFromInventory(ctx, plan, observed) }
	h, ok := createNormalTailHandoffFromContext(ctx)
	if !ok {
		return ordinary()
	}
	if _, err := os.Lstat(createNormalTailWorkloadForwardPath(h.Execution)); os.IsNotExist(err) {
		return ordinary()
	} else if err != nil {
		return err
	}
	original, err := readCreateNormalTailFrozenTarget(c.Store, h.Execution)
	if err != nil {
		return err
	}
	source, j, err := readCreateNormalTailWorkloadOriginal(c.Store, h.Execution, original)
	if err != nil {
		return err
	}
	d, err := readCreateNormalTailWorkloadForwardDescriptor(c.Store, h.Execution, original, j)
	if err != nil {
		return err
	}
	if d == nil {
		return ordinary()
	}
	var runtimeSources []createNormalTailVolumeStage
	if d.Target.Plan.SHA256() != plan.SHA256() {
		chain, err := readCreateNormalTailRuntimeChain(c.Store, h.Execution)
		if err != nil {
			return err
		}
		runtime, err := readCreateNormalTailRuntimeForwardFromChain(c.Store, h.Execution, chain)
		if err != nil {
			return err
		}
		if runtime != nil && runtime.Target.Plan.SHA256() == plan.SHA256() {
			for _, step := range chain {
				runtimeSources = append(runtimeSources, createNormalTailVolumeStage{Plan: step.PreviousPlan, Journal: step.PreviousJournal})
			}
		} else {
			successor, err := readCreateNormalTailProductSessionForward(c.Store, h.Execution, original, j, d)
			if err != nil {
				return err
			}
			if successor == nil || successor.Target.Plan.SHA256() != plan.SHA256() {
				return ordinary()
			}
		}
	}
	if source == nil || source.SHA256() != original.SHA256() || plan.StorageSHA256 != original.StorageSHA256 {
		return storageQualificationBlocker("", "linked workload lost its original storage input cohort")
	}
	if err := c.Backend.CheckStorageBindingsFromInventory(ctx, original, observed); err != nil {
		return err
	}
	ledger, err := readEnvironmentCreateLedger(h.Execution)
	if err != nil {
		return err
	}
	claims, err := managedLatePlannedVolumeClaims(original)
	if err != nil {
		return err
	}
	read := func(id string) (deploymentplan.Object, error) {
		if o := observed[id]; o != nil {
			return o, nil
		}
		return c.Backend.Read(ctx, id)
	}
	for i, old := range original.Resources {
		r := plan.Resources[i]
		if !strings.HasPrefix(old.ID, "PersistentVolumeClaim/") || old.Action != "create" || r.Action != "retain" {
			continue
		}
		bad := func() error {
			return storageQualificationBlocker(old.ID, "linked workload allocation lost its original write owner or recorded PVC/PV/CSI identity")
		}
		owner, written := managedLateWrittenVolumeOwner(original, j, old.ID)
		if !written || owner == "" || owner != r.UID {
			return bad()
		}
		planned, exists := claims[old.ID]
		if !exists || planned.ResourceID != old.ID || planned.PodName != "" {
			return bad()
		}
		var recorded *environmentCreateVolumeOwner
		for _, volume := range ledger.Volumes {
			if "PersistentVolumeClaim/"+volume.Namespace+"/"+volume.PVCName == old.ID {
				if recorded != nil {
					return bad()
				}
				v := volume
				recorded = &v
			}
		}
		if recorded == nil || recorded.PVCUID != owner {
			return bad()
		}
		claim, err := read(old.ID)
		if err != nil {
			return err
		}
		if _, err := r.Body(claim); err != nil {
			return err
		}
		pv, err := read("PersistentVolume//" + recorded.PVName)
		if err != nil {
			return err
		}
		if _, err := managedLateNativePlannedVolume(*recorded, planned, original, j, claim, pv, nil, nil); err != nil {
			return bad()
		}
	}
	for _, source := range runtimeSources {
		if err := checkCreateNormalTailRuntimeAllocatedClaims(ctx, c, plan, source.Plan, source.Journal, ledger, observed); err != nil {
			return err
		}
	}
	return ctx.Err()
}

// StatefulSet claims already allocated by the partial renewed child retain that
// child's literal controller write and ledger authority in the new target.
func checkCreateNormalTailRuntimeAllocatedClaims(ctx context.Context, c *compiledEnvironmentDeployment, plan, previous *deploymentplan.Plan, j deploymentplan.Journal, ledger environmentCreateResourceLedger, observed map[string]deploymentplan.Object) error {
	claims, err := managedLatePlannedVolumeClaims(previous)
	if err != nil {
		return err
	}
	resources := map[string]deploymentplan.Resource{}
	for _, r := range plan.Resources {
		resources[r.ID] = r
	}
	originals := map[string]deploymentplan.Resource{}
	for _, r := range previous.Resources {
		originals[r.ID] = r
	}
	read := func(id string) (deploymentplan.Object, error) {
		if o := observed[id]; o != nil {
			return o, nil
		}
		return c.Backend.Read(ctx, id)
	}
	for id, planned := range claims {
		if j.Owners[planned.ResourceID] == "" {
			continue
		}
		old, r := originals[planned.ResourceID], resources[planned.ResourceID]
		bad := func() error {
			return storageQualificationBlocker(id, "runtime successor lost recorded partial-workload PVC/PV/CSI or controller identity")
		}
		uid, written := managedLateWrittenVolumeOwner(previous, j, planned.ResourceID)
		if !written || old.Action != "create" || r.Action != "retain" || r.UID != uid {
			return bad()
		}
		var owner *environmentCreateVolumeOwner
		for _, v := range ledger.Volumes {
			if "PersistentVolumeClaim/"+v.Namespace+"/"+v.PVCName == id {
				if owner != nil {
					return bad()
				}
				copy := v
				owner = &copy
			}
		}
		if owner == nil {
			return bad()
		}
		claim, err := read(id)
		if err != nil {
			return err
		}
		pv, err := read("PersistentVolume//" + owner.PVName)
		if err != nil {
			return err
		}
		var controller, pod deploymentplan.Object
		if planned.PodName != "" {
			controller, err = read(planned.ResourceID)
			if err != nil {
				return err
			}
			if _, err = r.Body(controller); err != nil {
				return err
			}
			pod, err = read("Pod/" + owner.Namespace + "/" + planned.PodName)
			if err != nil {
				return err
			}
		} else {
			if _, err = r.Body(claim); err != nil {
				return err
			}
		}
		if _, err = managedLateNativePlannedVolume(*owner, planned, previous, j, claim, pv, controller, pod); err != nil {
			return bad()
		}
	}
	return ctx.Err()
}
