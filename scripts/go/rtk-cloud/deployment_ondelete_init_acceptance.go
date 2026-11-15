package main

import (
	"context"
	"errors"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// OnDelete retains the original Pod. Only its completed, non-restartable init
// may use an older image, authenticated by that Pod's owned ControllerRevision.
// Current app containers and restartable init sidecars still use target images.
func onDeleteInitRuntimeController(controller, pod deploymentplan.Object, live map[string]deploymentplan.Object) (deploymentplan.Object, error) {
	if controller["kind"] != "StatefulSet" || deploymentplan.Map(deploymentplan.Map(controller["spec"])["updateStrategy"])["type"] != "OnDelete" {
		return controller, nil
	}
	target := controller
	cloned := false
	actualInit := certIssuerObjectList(deploymentplan.Map(pod["spec"])["initContainers"])
	for i, raw := range certIssuerObjectList(environmentPKIPod(controller)["initContainers"]) {
		desired := deploymentplan.Map(raw)
		if desired["restartPolicy"] == "Always" {
			continue
		}
		actual := managedNamedField(actualInit, deploymentplan.String(desired["name"]))
		if actual == nil || actual["image"] == desired["image"] || actual["restartPolicy"] == "Always" {
			continue
		}
		meta := deploymentplan.Map(controller["metadata"])
		podMeta := deploymentplan.Map(pod["metadata"])
		revisionName := deploymentplan.String(deploymentplan.Map(podMeta["labels"])["controller-revision-hash"])
		revision := live["ControllerRevision/"+deploymentplan.String(podMeta["namespace"])+"/"+revisionName]
		revisionMeta := deploymentplan.Map(revision["metadata"])
		owned := false
		controllerOwners := 0
		for _, ref := range certIssuerObjectList(revisionMeta["ownerReferences"]) {
			owner := deploymentplan.Map(ref)
			if owner["controller"] == true {
				controllerOwners++
			}
			if owner["controller"] == true && owner["kind"] == "StatefulSet" && owner["name"] == meta["name"] && owner["uid"] == meta["uid"] {
				owned = true
			}
		}
		if revision == nil || revision["kind"] != "ControllerRevision" || revisionName == "" || revisionMeta["name"] != revisionName || revisionMeta["namespace"] != meta["namespace"] || deploymentplan.String(revisionMeta["uid"]) == "" || !owned || controllerOwners != 1 {
			return nil, errors.New("completed init lacks its original owned revision")
		}
		original := managedNamedField(certIssuerObjectList(environmentPKIPod(deploymentplan.Map(revision["data"]))["initContainers"]), deploymentplan.String(desired["name"]))
		if original == nil || original["restartPolicy"] == "Always" || !deploymentplan.Matches(original, actual) {
			return nil, errors.New("completed init differs from its original revision")
		}
		if !cloned {
			target = deploymentplan.Clone(controller)
			cloned = true
		}
		deploymentplan.Map(certIssuerObjectList(environmentPKIPod(target)["initContainers"])[i])["image"] = actual["image"]
	}
	return target, nil
}

// Include just the original revisions needed by historical init verification.
// This is runtime evidence, not a new desired resource or renderer input.
func (b environmentKubernetesBackend) TargetWorkloadInventory(ctx context.Context, plan *deploymentplan.Plan) ([]deploymentplan.Object, error) {
	objects, err := b.Inventory(ctx, plan.Stack)
	if err != nil {
		return nil, err
	}
	return appendOnDeleteInitRevisions(ctx, plan, objects, b.Read)
}

func appendOnDeleteInitRevisions(ctx context.Context, plan *deploymentplan.Plan, objects []deploymentplan.Object, read func(context.Context, string) (deploymentplan.Object, error)) ([]deploymentplan.Object, error) {
	selected := map[string]bool{}
	live := map[string]deploymentplan.Object{}
	for _, resource := range plan.Resources {
		selected[resource.ID] = resource.Action != "retire"
	}
	for _, object := range objects {
		live[deploymentplan.ID(object)] = object
	}
	for _, pod := range objects {
		if pod["kind"] != "Pod" {
			continue
		}
		ownerID, err := environmentPodController(pod, live)
		if err != nil || !selected[ownerID] {
			continue
		}
		controller := live[ownerID]
		if controller["kind"] != "StatefulSet" || deploymentplan.Map(deploymentplan.Map(controller["spec"])["updateStrategy"])["type"] != "OnDelete" {
			continue
		}
		for _, raw := range certIssuerObjectList(environmentPKIPod(controller)["initContainers"]) {
			init := deploymentplan.Map(raw)
			actual := managedNamedField(certIssuerObjectList(deploymentplan.Map(pod["spec"])["initContainers"]), deploymentplan.String(init["name"]))
			if init["restartPolicy"] == "Always" || actual == nil || actual["restartPolicy"] == "Always" || actual["image"] == init["image"] {
				continue
			}
			meta := deploymentplan.Map(pod["metadata"])
			revisionName := deploymentplan.String(deploymentplan.Map(meta["labels"])["controller-revision-hash"])
			if revisionName == "" {
				return nil, targetWorkloadBlocker(ownerID, "completed init lacks its original owned revision")
			}
			id := "ControllerRevision/" + deploymentplan.String(meta["namespace"]) + "/" + revisionName
			if live[id] != nil {
				continue
			}
			revision, err := read(ctx, id)
			if err != nil || revision == nil || deploymentplan.ID(revision) != id {
				return nil, targetWorkloadBlocker(ownerID, "completed init original revision cannot be read")
			}
			objects = append(objects, revision)
			live[id] = revision
		}
	}
	return objects, nil
}
