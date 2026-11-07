package main

import (
	"context"
	"encoding/json"
	"errors"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
)

// A deterministic public annotation makes an interrupted recovery restart
// idempotent. Only pending Deployment consumers may restart after the native
// reload grace period; StatefulSets retain their own lifecycle. Fresh pre-expiry
// renewal uses the normal native reload loops.
func (r *maintenanceRuntime) restartExpiredConsumers(ctx context.Context, inventory crlMaintenanceInventory, records map[string]createRootCRLRecord) error {
	digest := deploymentplan.Digest(records)
	owners := map[string]deploymentplan.Object{}
	for _, c := range inventory.Consumers {
		pod, err := r.read(ctx, "Pod/"+c.Namespace+"/"+c.Pod)
		if err != nil || pod == nil || deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"]) != c.PodUID {
			return errors.New("CRL restart original Pod changed")
		}
		object := pod
		for depth := 0; depth < 2; depth++ {
			refs := certIssuerObjectList(deploymentplan.Map(object["metadata"])["ownerReferences"])
			var owner deploymentplan.Object
			for _, rv := range refs {
				ref := deploymentplan.Map(rv)
				if ref["controller"] == true {
					if owner != nil {
						return errors.New("ambiguous CRL consumer owner")
					}
					owner = ref
				}
			}
			kind := deploymentplan.String(owner["kind"])
			name := deploymentplan.String(owner["name"])
			if kind == "StatefulSet" {
				break
			} // OpenBao/MQTT require their native lifecycle; never restart stateful owners here.
			if kind != "ReplicaSet" && kind != "Deployment" && kind != "StatefulSet" {
				return errors.New("CRL recovery requires an existing managed workload owner")
			}
			id := kind + "/" + c.Namespace + "/" + name
			parent, err := r.read(ctx, id)
			if err != nil || parent == nil || deploymentplan.Map(parent["metadata"])["uid"] != owner["uid"] {
				return errors.New("CRL workload owner changed")
			}
			if kind != "ReplicaSet" {
				owners[id] = parent
				break
			}
			object = parent
		}
	}
	for id, owner := range owners {
		if err := r.lease.Check(ctx); err != nil {
			return err
		}
		meta := deploymentplan.Map(owner["metadata"])
		template := deploymentplan.Map(deploymentplan.Map(owner["spec"])["template"])
		tm := deploymentplan.Map(template["metadata"])
		annotations := deploymentplan.Map(tm["annotations"])
		if annotations["rtk.cloud/crl-maintenance"] != digest {
			next := map[string]any{}
			for k, v := range annotations {
				next[k] = v
			}
			next["rtk.cloud/crl-maintenance"] = digest
			patch, _ := json.Marshal([]map[string]any{{"op": "test", "path": "/metadata/uid", "value": meta["uid"]}, {"op": "test", "path": "/metadata/resourceVersion", "value": meta["resourceVersion"]}, {"op": "add", "path": "/spec/template/metadata/annotations", "value": next}})
			parts := strings.Split(id, "/")
			if _, err := r.command(ctx, nil, "--namespace", parts[1], "patch", strings.ToLower(parts[0]), parts[2], "--type=json", "--patch", string(patch)); err != nil {
				return errors.New("CRL recovery workload restart rejected")
			}
		}
	}
	for id := range owners {
		parts := strings.Split(id, "/")
		if _, err := r.command(ctx, nil, "--namespace", parts[1], "rollout", "status", strings.ToLower(parts[0])+"/"+parts[2], "--timeout=180s"); err != nil {
			return errors.New("CRL consumer restart did not become ready; preserve renewed state and inspect original startup")
		}
	}
	return nil
}
