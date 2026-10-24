package main

import (
	"context"
	"math"
	"strconv"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Claims come from the frozen maintained graph. StatefulSet claim names follow
// the same native ordinal expansion used by quota qualification.
type managedLatePlannedVolumeClaim struct {
	Claim               deploymentplan.Object
	ResourceID, PodName string
}

func managedLatePlannedVolumeClaims(plan *deploymentplan.Plan) (map[string]managedLatePlannedVolumeClaim, error) {
	bad := func() (map[string]managedLatePlannedVolumeClaim, error) {
		return nil, environmentCreateBlocker("deployment.volume.planned-graph", "recorded workload volume allocation graph is inconsistent")
	}
	if plan == nil || plan.Stack == "" || plan.Environment == "" {
		return bad()
	}
	claims := map[string]managedLatePlannedVolumeClaim{}
	add := func(claim deploymentplan.Object, resourceID, podName string) bool {
		id := deploymentplan.ID(claim)
		meta, spec := deploymentplan.Map(claim["metadata"]), deploymentplan.Map(claim["spec"])
		if deploymentplan.String(meta["name"]) == "" || !strings.HasPrefix(deploymentplan.String(meta["namespace"]), plan.Stack+"-") || deploymentplan.String(spec["storageClassName"]) == "" || spec["selector"] != nil || spec["volumeName"] != nil && spec["volumeName"] != "" {
			return false
		}
		if _, exists := claims[id]; exists {
			return false
		}
		claims[id] = managedLatePlannedVolumeClaim{Claim: claim, ResourceID: resourceID, PodName: podName}
		return true
	}
	for _, resource := range plan.Resources {
		if resource.Action == "retire" {
			continue
		}
		switch resource.Desired["kind"] {
		case "PersistentVolumeClaim":
			if resource.Action == "create" && (resource.UID != "" || resource.Baseline != nil || !add(deploymentplan.Clone(resource.Desired), resource.ID, "")) {
				return bad()
			}
		case "StatefulSet":
			if resource.Action == "retain" {
				continue
			}
			meta, spec := deploymentplan.Map(resource.Desired["metadata"]), deploymentplan.Map(resource.Desired["spec"])
			templates := certIssuerObjectList(spec["volumeClaimTemplates"])
			if len(templates) == 0 {
				continue
			}
			replicas, ok := spec["replicas"].(float64)
			if !ok || replicas < 0 || replicas > 10000 || replicas != math.Trunc(replicas) || spec["ordinals"] != nil {
				return bad()
			}
			first := 0
			if resource.Action == "create" {
				if resource.UID != "" || resource.Baseline != nil {
					return bad()
				}
			} else if resource.Action == "update" {
				prior := deploymentplan.Map(resource.Baseline["spec"])
				count, ok := prior["replicas"].(float64)
				if !ok || count < 0 || count > 10000 || count != math.Trunc(count) || !environmentBootstrapOwnerUUID.MatchString(resource.UID) || !managedLateVolumeClaimTemplatesEqual(prior["volumeClaimTemplates"], spec["volumeClaimTemplates"]) {
					return bad()
				}
				first = int(count)
			} else {
				return bad()
			}
			for _, value := range templates {
				template := deploymentplan.Map(value)
				name := deploymentplan.String(deploymentplan.Map(template["metadata"])["name"])
				if name == "" {
					return bad()
				}
				for ordinal := first; ordinal < int(replicas); ordinal++ {
					pod := deploymentplan.String(meta["name"]) + "-" + strconv.Itoa(ordinal)
					claim := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"namespace": meta["namespace"], "name": name + "-" + pod}, "spec": deploymentplan.Clone(template)["spec"]}
					if !add(claim, resource.ID, pod) {
						return bad()
					}
				}
			}
		}
	}
	return claims, nil
}

// Claim templates have the PVC API envelope in native readback, while maintained
// recipes may omit that envelope. Compare cloned projections so status and other
// server-owned metadata cannot change the frozen allocation graph.
func managedLateVolumeClaimTemplatesEqual(recorded, desired any) bool {
	old, want := certIssuerObjectList(recorded), certIssuerObjectList(desired)
	if len(old) != len(want) {
		return false
	}
	for i := range old {
		a, b := deploymentplan.Clone(deploymentplan.Map(old[i])), deploymentplan.Clone(deploymentplan.Map(want[i]))
		if a == nil || b == nil {
			return false
		}
		for _, template := range []deploymentplan.Object{a, b} {
			for key, expected := range map[string]string{"apiVersion": "v1", "kind": "PersistentVolumeClaim"} {
				if value, exists := template[key]; exists && value != expected {
					return false
				}
				template[key] = expected
			}
		}
		if deploymentplan.Digest(deploymentplan.Projection(a)) != deploymentplan.Digest(deploymentplan.Projection(b)) {
			return false
		}
	}
	return true
}

// A write owner is persisted before Wait, while Completed is appended after
// Wait. Accept that exact next written resource too; no in-flight owner is proof.
func managedLateWrittenVolumeOwner(plan *deploymentplan.Plan, journal deploymentplan.Journal, id string) (string, bool) {
	if plan == nil || journal.PlanSHA256 != plan.SHA256() || len(journal.Completed) > len(plan.Resources) {
		return "", false
	}
	for i, completed := range journal.Completed {
		if completed != plan.Resources[i].ID {
			return "", false
		}
	}
	for i, resource := range plan.Resources {
		if resource.ID != id {
			continue
		}
		uid := journal.Owners[id]
		if i > len(journal.Completed) || journal.InFlight == id || !environmentBootstrapOwnerUUID.MatchString(uid) || resource.UID != "" && resource.UID != uid || resource.Action == "retire" {
			return "", false
		}
		return uid, true
	}
	return "", false
}

func managedLateNativePlannedVolume(owner environmentCreateVolumeOwner, planned managedLatePlannedVolumeClaim, plan *deploymentplan.Plan, journal deploymentplan.Journal, claim, pv, controller, pod deploymentplan.Object) (string, error) {
	bad := func() (string, error) { return "", createOpenBaoACKBlock("planned-volume-native-owner") }
	uid, written := managedLateWrittenVolumeOwner(plan, journal, planned.ResourceID)
	cm, cs := deploymentplan.Map(claim["metadata"]), deploymentplan.Map(claim["spec"])
	pm, ps := deploymentplan.Map(pv["metadata"]), deploymentplan.Map(pv["spec"])
	ref, csi := deploymentplan.Map(ps["claimRef"]), deploymentplan.Map(ps["csi"])
	handle := deploymentplan.String(csi["volumeHandle"])
	if prefix, suffix, ok := strings.Cut(handle, "-"); ok && strings.HasPrefix(suffix, "pvc-") {
		handle = prefix
	}
	if !written || !environmentBootstrapOwnerUUID.MatchString(owner.PVCUID) || !environmentBootstrapOwnerUUID.MatchString(owner.PVUID) || claim == nil || pv == nil || deploymentplan.ID(claim) != deploymentplan.ID(planned.Claim) || !deploymentplan.Matches(planned.Claim, claim) || cm["name"] != owner.PVCName || cm["namespace"] != owner.Namespace || cm["uid"] != owner.PVCUID || cm["deletionTimestamp"] != nil || deploymentplan.Map(claim["status"])["phase"] != "Bound" || cs["volumeName"] != owner.PVName || pm["name"] != owner.PVName || pm["uid"] != owner.PVUID || pm["deletionTimestamp"] != nil || deploymentplan.Map(pv["status"])["phase"] != "Bound" || ref["uid"] != owner.PVCUID || ref["name"] != owner.PVCName || ref["namespace"] != owner.Namespace || csi["driver"] != "linodebs.csi.linode.com" || handle != owner.NativeID || ps["persistentVolumeReclaimPolicy"] != "Retain" || ps["storageClassName"] != cs["storageClassName"] {
		return bad()
	}
	for _, field := range []string{"selector", "dataSource", "dataSourceRef"} {
		if deploymentplan.Digest(cs[field]) != deploymentplan.Digest(deploymentplan.Map(planned.Claim["spec"])[field]) {
			return bad()
		}
	}
	if planned.PodName == "" {
		if uid != owner.PVCUID {
			return bad()
		}
		return uid, nil
	}
	if controller == nil || pod == nil || deploymentplan.ID(controller) != planned.ResourceID || deploymentplan.Map(controller["metadata"])["uid"] != uid || deploymentplan.Map(controller["metadata"])["deletionTimestamp"] != nil || deploymentplan.Map(pod["metadata"])["namespace"] != owner.Namespace || deploymentplan.Map(pod["metadata"])["name"] != planned.PodName || !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"])) || deploymentplan.Map(pod["metadata"])["deletionTimestamp"] != nil {
		return bad()
	}
	for _, resource := range plan.Resources {
		if resource.ID == planned.ResourceID && !deploymentplan.Matches(resource.Desired, controller) {
			return bad()
		}
	}
	owners := 0
	for _, value := range certIssuerObjectList(deploymentplan.Map(pod["metadata"])["ownerReferences"]) {
		ref := deploymentplan.Map(value)
		if ref["controller"] == true {
			if ref["kind"] != "StatefulSet" || ref["uid"] != uid || ref["name"] != deploymentplan.Map(controller["metadata"])["name"] || ref["apiVersion"] != controller["apiVersion"] {
				return bad()
			}
			owners++
		}
	}
	volumes := 0
	for _, value := range certIssuerObjectList(deploymentplan.Map(pod["spec"])["volumes"]) {
		if deploymentplan.Map(deploymentplan.Map(value)["persistentVolumeClaim"])["claimName"] == owner.PVCName {
			volumes++
		}
	}
	if owners != 1 || volumes != 1 {
		return bad()
	}
	return uid, nil
}

func observeManagedLatePlannedVolume(ctx context.Context, backend environmentKubernetesBackend, owner environmentCreateVolumeOwner, planned managedLatePlannedVolumeClaim, plan *deploymentplan.Plan, journal deploymentplan.Journal) (string, error) {
	claim, err := backend.Read(ctx, deploymentplan.ID(planned.Claim))
	if err != nil {
		return "", err
	}
	pv, err := backend.Read(ctx, "PersistentVolume//"+owner.PVName)
	if err != nil {
		return "", err
	}
	var controller, pod deploymentplan.Object
	if planned.PodName != "" {
		controller, err = backend.Read(ctx, planned.ResourceID)
		if err != nil {
			return "", err
		}
		pod, err = backend.Read(ctx, "Pod/"+owner.Namespace+"/"+planned.PodName)
		if err != nil {
			return "", err
		}
	}
	uid, err := managedLateNativePlannedVolume(owner, planned, plan, journal, claim, pv, controller, pod)
	if err != nil {
		return "", err
	}
	return uid, ctx.Err()
}

// The stage caller authenticates its frozen plan, parent intent and lease.
// Native capture records the claim/controller UIDs in the existing ledger.
func managedLatePlannedVolumeProjection(ledger environmentCreateResourceLedger, original map[string]environmentCreateVolumeOwner, redis *environmentCreateVolumeOwner, frozen, child *deploymentplan.Plan, journal deploymentplan.Journal) (environmentCreateResourceLedger, error) {
	bad := func() (environmentCreateResourceLedger, error) {
		return environmentCreateResourceLedger{}, createOpenBaoACKBlock("planned-volume-allocation")
	}
	claims, err := managedLatePlannedVolumeClaims(frozen)
	if err != nil || child == nil || frozen.Environment != ledger.Environment || frozen.Stack != ledger.Stack || child.Environment != ledger.Environment || child.Stack != ledger.Stack || child.ConfigurationSHA256 != frozen.ConfigurationSHA256 || child.ReleaseSHA256 != frozen.ReleaseSHA256 {
		return bad()
	}
	resources := map[string]deploymentplan.Resource{}
	for _, resource := range child.Resources {
		if _, exists := resources[resource.ID]; exists {
			return bad()
		}
		resources[resource.ID] = resource
	}
	frozenResources := map[string]deploymentplan.Resource{}
	for _, resource := range frozen.Resources {
		frozenResources[resource.ID] = resource
	}
	selected := ledger
	selected.Volumes = nil
	native, pvc, pv, ids := map[string]bool{}, map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, owner := range ledger.Volumes {
		id := "PersistentVolumeClaim/" + owner.Namespace + "/" + owner.PVCName
		if ids[id] || native[owner.NativeID] || pvc[owner.PVCUID] || pv[owner.PVUID] || !environmentBootstrapOwnerUUID.MatchString(owner.PVCUID) || !environmentBootstrapOwnerUUID.MatchString(owner.PVUID) || owner.PVName == "" {
			return bad()
		}
		if _, err := positiveIntValue("volume-id", owner.NativeID); err != nil {
			return bad()
		}
		ids[id], native[owner.NativeID], pvc[owner.PVCUID], pv[owner.PVUID] = true, true, true, true
		if prior, ok := original[id]; ok {
			if prior != owner {
				return bad()
			}
			selected.Volumes = append(selected.Volumes, owner)
			continue
		}
		if redis != nil && *redis == owner {
			selected.Volumes = append(selected.Volumes, owner)
			continue
		}
		claim, ok := claims[id]
		resource, present := resources[claim.ResourceID]
		approved := frozenResources[claim.ResourceID]
		if !ok || !present || resource.Action != approved.Action || resource.UID != approved.UID || deploymentplan.Digest(resource.Desired) != deploymentplan.Digest(approved.Desired) || ledger.Kubernetes[id] != owner.PVCUID {
			return bad()
		}
		uid, written := managedLateWrittenVolumeOwner(child, journal, claim.ResourceID)
		if !written || claim.PodName == "" && uid != owner.PVCUID || claim.PodName != "" && ledger.Kubernetes[claim.ResourceID] != uid {
			return bad()
		}
	}
	if journal.Accepted {
		for id := range claims {
			if _, prior := original[id]; !prior && !ids[id] {
				return bad()
			}
		}
	}
	return managedLateRedisDurableVolumeProjection(selected, original, redis)
}

// This runs at the existing capture boundary. Every new claim belongs to an
// authenticated product child or the one frozen workload; historical bootstrap
// and migration capture keep their existing lifecycle.
func captureManagedLatePlannedVolumes(ctx context.Context, backend environmentKubernetesBackend, e *environmentCreateExecution, ledger *environmentCreateResourceLedger) error {
	if e == nil || ledger == nil {
		return createOpenBaoACKBlock("planned-volume-capture")
	}
	if createOpenBaoACKCompletionEntry(e.Journal, managedLateRedisDurableKey) == nil || createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload") == nil && createOpenBaoACKCompletionEntry(e.Journal, "normal-tail/program") == nil {
		return nil
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	original, redis, err := readManagedLateRedisDurableVolumeAuthority(e.Store, e)
	if err != nil {
		return err
	}
	stages, err := readCreateNormalTailVolumeStages(e.Store, e)
	if err != nil {
		return err
	}
	selected, err := observeManagedLatePlannedVolumeStages(ctx, backend, *ledger, original, redis, stages)
	if err != nil {
		return err
	}
	closing, err := readCreateNormalTailVolumeStages(e.Store, e)
	if err != nil {
		return err
	}
	if deploymentplan.Digest(closing) != deploymentplan.Digest(stages) {
		return createOpenBaoACKBlock("planned-volume-capture-changed")
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	*ledger = selected
	return nil
}

func observeManagedLatePlannedVolumes(ctx context.Context, backend environmentKubernetesBackend, ledger environmentCreateResourceLedger, original map[string]environmentCreateVolumeOwner, redis *environmentCreateVolumeOwner, frozen, child *deploymentplan.Plan, journal deploymentplan.Journal) (environmentCreateResourceLedger, error) {
	claims, err := managedLatePlannedVolumeClaims(frozen)
	if err != nil {
		return environmentCreateResourceLedger{}, err
	}
	// Commit only after every current allocation has fresh native ownership.
	selected := ledger
	selected.Kubernetes = map[string]string{}
	for id, uid := range ledger.Kubernetes {
		selected.Kubernetes[id] = uid
	}
	for _, volume := range ledger.Volumes {
		id := "PersistentVolumeClaim/" + volume.Namespace + "/" + volume.PVCName
		if _, old := original[id]; old || redis != nil && *redis == volume {
			continue
		}
		planned, ok := claims[id]
		if !ok {
			return environmentCreateResourceLedger{}, createOpenBaoACKBlock("planned-volume-capture")
		}
		uid, err := observeManagedLatePlannedVolume(ctx, backend, volume, planned, child, journal)
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
	if _, err := managedLatePlannedVolumeProjection(selected, original, redis, frozen, child, journal); err != nil {
		return environmentCreateResourceLedger{}, err
	}
	return selected, ctx.Err()
}
