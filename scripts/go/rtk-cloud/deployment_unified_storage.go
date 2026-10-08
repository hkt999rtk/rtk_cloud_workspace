package main

import (
	"context"
	"errors"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentStorageBinding struct {
	ClaimID, ClaimUID, VolumeID, VolumeUID, VolumeSpecSHA256 string
}

func (b environmentKubernetesBackend) readStorageOwner(ctx context.Context, id string) (deploymentplan.Object, error) {
	object, failure := b.readOwner(ctx, id)
	if failure != nil {
		return nil, storageReadBlocker(id, failure)
	}
	return object, nil
}

func storageReadBlocker(resource string, failure *deploymentRuntimeError) error {
	next := "verify the selected frozen Kubernetes identity, get permission for this resource and API connectivity, then repeat preflight while preserving the original PVC/PV data"
	switch failure.code {
	case "KUBE_FORBIDDEN":
		next = "grant the selected Kubernetes identity get permission for this storage resource, then repeat preflight while preserving the original PVC/PV data"
	case "KUBE_UNAUTHORIZED":
		next = "restore the selected environment's Kubernetes authentication, then regenerate and qualify the frozen plan while preserving the original PVC/PV data"
	case "KUBE_INVALID_RESPONSE":
		next = "verify the Kubernetes API response and kubectl compatibility for this storage resource, then repeat preflight while preserving the original PVC/PV data"
	case "KUBE_TIMEOUT", "KUBE_RATE_LIMITED", "KUBE_UNAVAILABLE":
		next = "verify cluster connectivity and API availability, then repeat preflight while preserving the original PVC/PV data"
	case "CHECK_CANCELLED":
		next = "repeat the cancelled preflight to obtain fresh storage evidence"
	}
	return &deploymentplan.Blocker{ID: "deployment.access.storage", Resource: resource, Cause: "cannot read original storage owner [" + failure.code + "]", Next: next}
}

// Existing bound data volumes are execution inputs, never desired spec
// templates. Retained claim UID, native claimRef and original PV UID/spec must
// agree; status/resourceVersion updates alone do not change data identity.
func (b environmentKubernetesBackend) storageBindings(ctx context.Context, plan *deploymentplan.Plan) ([]environmentStorageBinding, error) {
	if plan == nil || plan.Environment != b.Store.Environment {
		return nil, errors.New("selected environment plan required for storage qualification")
	}
	var bindings []environmentStorageBinding
	for _, resource := range plan.Resources {
		if !strings.HasPrefix(resource.ID, "PersistentVolumeClaim/") || resource.Action == "create" || resource.Action == "retire" {
			continue
		}
		claim, err := b.readStorageOwner(ctx, resource.ID)
		if err != nil {
			return nil, err
		}
		if claim == nil || resource.UID == "" || deploymentplan.String(deploymentplan.Map(claim["metadata"])["uid"]) != resource.UID || deploymentplan.Map(claim["metadata"])["deletionTimestamp"] != nil {
			return nil, storageQualificationBlocker(resource.ID, "original claim owner is absent, replaced or terminating")
		}
		spec := deploymentplan.Map(claim["spec"])
		volume := deploymentplan.String(spec["volumeName"])
		original := deploymentplan.Map(resource.Baseline["spec"])
		if volume != deploymentplan.String(original["volumeName"]) || spec["storageClassName"] != original["storageClassName"] || spec["volumeMode"] != original["volumeMode"] || deploymentplan.Digest(spec["accessModes"]) != deploymentplan.Digest(original["accessModes"]) {
			return nil, storageQualificationBlocker(resource.ID, "original volume allocation or access contract changed")
		}
		if resource.Action == "retain" {
			if _, err := resource.Body(claim); err != nil {
				return nil, err
			}
		}
		if deploymentplan.Map(claim["status"])["phase"] != "Bound" || volume == "" {
			return nil, storageQualificationBlocker(resource.ID, "existing durable claim is not bound to its original volume")
		}
		pvID := "PersistentVolume//" + volume
		pv, err := b.readStorageOwner(ctx, pvID)
		if err != nil {
			return nil, err
		}
		if pv == nil || deploymentplan.Map(pv["metadata"])["deletionTimestamp"] != nil || deploymentplan.String(deploymentplan.Map(pv["metadata"])["uid"]) == "" || deploymentplan.Map(pv["status"])["phase"] != "Bound" {
			return nil, storageQualificationBlocker(resource.ID, "bound volume owner is absent, terminating or not Bound")
		}
		parts := strings.Split(resource.ID, "/")
		ref := deploymentplan.Map(deploymentplan.Map(pv["spec"])["claimRef"])
		if ref["uid"] != resource.UID || ref["namespace"] != parts[1] || ref["name"] != parts[2] {
			return nil, storageQualificationBlocker(resource.ID, "volume claimRef does not match the original claim UID, namespace and name")
		}
		bindings = append(bindings, environmentStorageBinding{resource.ID, resource.UID, pvID, deploymentplan.String(deploymentplan.Map(pv["metadata"])["uid"]), deploymentplan.Digest(deploymentplan.Map(pv["spec"]))})
	}
	return bindings, nil
}

func storageQualificationBlocker(resource, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.execution.storage", Resource: resource, Cause: cause, Next: "restore the original PVC/PV binding and persistent data through its verified migration/recovery lifecycle, then regenerate and qualify the plan; do not delete or recreate a data volume to pass preflight"}
}

func (b environmentKubernetesBackend) CheckStorageBindings(ctx context.Context, plan *deploymentplan.Plan) error {
	bindings, err := b.storageBindings(ctx, plan)
	if err != nil {
		return err
	}
	if plan.StorageSHA256 == "" || deploymentplan.Digest(bindings) != plan.StorageSHA256 {
		return storageQualificationBlocker("", "original storage identity or volume spec changed after planning")
	}
	return nil
}
