package main

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The intermediate failed child owns both support VCTs. The original child
// owns the direct claims; the final child creates only the still-unwritten
// Fleet claim. Each allocation therefore has one literal write authority.
func runtimeAllocationFixture(t *testing.T) (environmentCreateResourceLedger, map[string]environmentCreateVolumeOwner, *environmentCreateVolumeOwner, []createNormalTailVolumeStage) {
	t.Helper()
	ledger, original, redis, _, first, _ := plannedVolumeFixture(t)
	ledger.Volumes = ledger.Volumes[:22]
	ledger.Kubernetes = map[string]string{}
	a := *first
	a.Resources = append([]deploymentplan.Resource(nil), first.Resources[:3]...)
	template := deploymentplan.Clone(first.Resources[3].Desired)
	deploymentplan.Map(template["spec"])["replicas"] = float64(1)
	for _, name := range []string{"zammad-elasticsearch-master", "zammad-postgres"} {
		body := deploymentplan.Clone(template)
		deploymentplan.Map(body["metadata"])["name"] = name
		a.Resources = append(a.Resources, deploymentplan.Resource{ID: deploymentplan.ID(body), Action: "create", Phase: "workload", Desired: body})
	}
	billing := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "billing", "namespace": a.Stack + "-billing"}, "spec": deploymentplan.Object{"replicas": float64(1)}}
	a.Resources = append(a.Resources, deploymentplan.Resource{ID: deploymentplan.ID(billing), Action: "create", Phase: "workload", Desired: billing})
	fleet := deploymentplan.Clone(template)
	deploymentplan.Map(fleet["metadata"])["name"] = "fleet-valkey"
	a.Resources = append(a.Resources, deploymentplan.Resource{ID: deploymentplan.ID(fleet), Action: "create", Phase: "workload", Desired: fleet})
	ja := deploymentplan.Journal{PlanSHA256: a.SHA256(), Owners: map[string]string{}}
	for i := 0; i < 3; i++ {
		r := a.Resources[i]
		uid := ledger.Volumes[19+i].PVCUID
		ja.Owners[r.ID] = uid
		ja.Completed = append(ja.Completed, r.ID)
		ledger.Kubernetes[r.ID] = uid
	}
	ja.Failed = a.Resources[3].ID
	b := a
	b.Resources = append([]deploymentplan.Resource(nil), a.Resources...)
	for i := 0; i < 3; i++ {
		r := &b.Resources[i]
		r.Action = "retain"
		r.UID = ja.Owners[r.ID]
		r.Baseline = deploymentplan.Clone(r.Desired)
		deploymentplan.Map(r.Baseline["metadata"])["uid"] = r.UID
	}
	jb := deploymentplan.Journal{PlanSHA256: b.SHA256(), Owners: map[string]string{}}
	for i, r := range b.Resources[:6] {
		uid := ja.Owners[r.ID]
		if uid == "" {
			uid = fmt.Sprintf("%08d-3333-4333-8333-333333333333", i+1)
		}
		jb.Owners[r.ID] = uid
		ledger.Kubernetes[r.ID] = uid
		if i < 5 {
			jb.Completed = append(jb.Completed, r.ID)
		}
	}
	jb.Failed = b.Resources[5].ID
	c := b
	c.Resources = append([]deploymentplan.Resource(nil), b.Resources...)
	for i := 0; i < 6; i++ {
		r := &c.Resources[i]
		r.Action = "retain"
		r.UID = jb.Owners[r.ID]
		r.Baseline = deploymentplan.Clone(r.Desired)
		deploymentplan.Map(r.Baseline["metadata"])["uid"] = r.UID
	}
	c.Resources[5].Action = "update"
	c.Resources[5].Desired = deploymentplan.Clone(c.Resources[5].Desired)
	deploymentplan.Map(c.Resources[5].Desired["spec"])["replicas"] = float64(0)
	jc := deploymentplan.Journal{PlanSHA256: c.SHA256(), Accepted: true, AcceptanceStarted: true, Owners: map[string]string{}}
	for i, r := range c.Resources {
		uid := jb.Owners[r.ID]
		if uid == "" {
			uid = fmt.Sprintf("%08d-3333-4333-8333-333333333333", i+1)
		}
		jc.Owners[r.ID] = uid
		jc.Completed = append(jc.Completed, r.ID)
		ledger.Kubernetes[r.ID] = uid
	}
	for i, name := range []string{"zammad-elasticsearch-master", "zammad-postgres", "fleet-valkey"} {
		n := 23 + i
		o := environmentCreateVolumeOwner{NativeID: fmt.Sprint(n), PVName: fmt.Sprint("pv-", n), PVUID: fmt.Sprintf("%08d-1111-4111-8111-111111111111", n), PVCUID: fmt.Sprintf("%08d-2222-4222-8222-222222222222", n), PVCName: "data-" + name + "-0", Namespace: a.Stack + "-support"}
		ledger.Volumes = append(ledger.Volumes, o)
		ledger.Kubernetes["PersistentVolumeClaim/"+o.Namespace+"/"+o.PVCName] = o.PVCUID
	}
	return ledger, original, redis, []createNormalTailVolumeStage{{Stage: "workload", Plan: &a, Journal: ja}, {Stage: "workload-product-session-forward", Plan: &b, Journal: jb, originalPlanSHA256: a.SHA256()}, {Stage: createNormalTailRuntimeForwardStage, Plan: &c, Journal: jc, originalPlanSHA256: b.SHA256()}}
}

func runtimeAllocationNativeObjects(t *testing.T, ledger environmentCreateResourceLedger, stages []createNormalTailVolumeStage) map[string]deploymentplan.Object {
	t.Helper()
	objects := map[string]deploymentplan.Object{}
	index, claims, err := managedLateVolumeStageClaims(stages)
	if err != nil {
		t.Fatal(err)
	}
	for _, volume := range ledger.Volumes {
		id := "PersistentVolumeClaim/" + volume.Namespace + "/" + volume.PVCName
		n, ok := index[id]
		if !ok {
			continue
		}
		stage, planned := stages[n], claims[n][id]
		claim := deploymentplan.Clone(planned.Claim)
		deploymentplan.Map(claim["metadata"])["uid"] = volume.PVCUID
		deploymentplan.Map(claim["spec"])["volumeName"] = volume.PVName
		claim["status"] = deploymentplan.Object{"phase": "Bound"}
		objects[id] = claim
		objects["PersistentVolume//"+volume.PVName] = deploymentplan.Object{
			"apiVersion": "v1", "kind": "PersistentVolume",
			"metadata": deploymentplan.Object{"name": volume.PVName, "uid": volume.PVUID},
			"spec":     deploymentplan.Object{"storageClassName": "linode-block-storage-retain", "persistentVolumeReclaimPolicy": "Retain", "claimRef": deploymentplan.Object{"namespace": volume.Namespace, "name": volume.PVCName, "uid": volume.PVCUID}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": volume.NativeID + "-pvc-original"}},
			"status":   deploymentplan.Object{"phase": "Bound"},
		}
		if planned.PodName == "" {
			continue
		}
		uid := stage.Journal.Owners[planned.ResourceID]
		for _, r := range stage.Plan.Resources {
			if r.ID == planned.ResourceID {
				objects[r.ID] = deploymentplan.Clone(r.Desired)
				deploymentplan.Map(objects[r.ID]["metadata"])["uid"] = uid
			}
		}
		parts := strings.SplitN(planned.ResourceID, "/", 3)
		objects["Pod/"+volume.Namespace+"/"+planned.PodName] = deploymentplan.Object{
			"apiVersion": "v1", "kind": "Pod",
			"metadata": deploymentplan.Object{"name": planned.PodName, "namespace": volume.Namespace, "uid": "99999999-1111-4111-8111-111111111111", "ownerReferences": []any{deploymentplan.Object{"controller": true, "apiVersion": "apps/v1", "kind": "StatefulSet", "name": parts[2], "uid": uid}}},
			"spec":     deploymentplan.Object{"volumes": []any{deploymentplan.Object{"name": "data", "persistentVolumeClaim": deploymentplan.Object{"claimName": volume.PVCName}}}},
		}
	}
	return objects
}

func TestCreateNormalTailRuntimeAllocationKeepsBothFailedAncestors(t *testing.T) {
	ledger, original, redis, stages := runtimeAllocationFixture(t)
	before := deploymentplan.Digest(ledger)
	got, err := managedLatePlannedVolumeStagesProjection(ledger, original, redis, stages)
	if err != nil || len(got.Volumes) != 18 || deploymentplan.Digest(ledger) != before {
		t.Fatal("three-source allocation projection", err)
	}
	for _, mode := range []string{"missing-original-direct", "missing-es-template", "missing-support-pg-template", "missing-future-fleet", "unknown-ancestor", "cyclic-ancestor", "ambiguous-ancestor", "changed-delegated-template", "lost-template-owner", "replaced-template-pvc"} {
		t.Run(mode, func(t *testing.T) {
			l, o, r, s := runtimeAllocationFixture(t)
			switch mode {
			case "missing-original-direct":
				l.Volumes = append(l.Volumes[:19], l.Volumes[20:]...)
			case "missing-es-template":
				l.Volumes = append(l.Volumes[:22], l.Volumes[23:]...)
			case "missing-support-pg-template":
				l.Volumes = append(l.Volumes[:23], l.Volumes[24:]...)
			case "missing-future-fleet":
				l.Volumes = l.Volumes[:24]
			case "unknown-ancestor":
				s[2].originalPlanSHA256 = strings.Repeat("a", 64)
			case "cyclic-ancestor":
				s[1].originalPlanSHA256 = s[2].Plan.SHA256()
			case "ambiguous-ancestor":
				s = append(s[:1], append([]createNormalTailVolumeStage{s[0]}, s[1:]...)...)
			case "changed-delegated-template":
				p := *s[2].Plan
				p.Resources = append([]deploymentplan.Resource(nil), p.Resources...)
				p.Resources[6].Desired = deploymentplan.Clone(p.Resources[6].Desired)
				vct := deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(p.Resources[6].Desired["spec"])["volumeClaimTemplates"])[0])
				deploymentplan.Map(vct["spec"])["storageClassName"] = "foreign"
				s[2].Plan = &p
				s[2].Journal.PlanSHA256 = p.SHA256()
			case "lost-template-owner":
				delete(s[1].Journal.Owners, s[1].Plan.Resources[3].ID)
			case "replaced-template-pvc":
				l.Kubernetes["PersistentVolumeClaim/"+l.Volumes[22].Namespace+"/"+l.Volumes[22].PVCName] = "99999999-9999-4999-8999-999999999999"
			}
			if _, err := managedLatePlannedVolumeStagesProjection(l, o, r, s); err == nil {
				t.Fatal("unproved three-source allocation accepted")
			}
		})
	}
}

func TestCreateNormalTailRuntimeAllocationReadsCurrentTemplateOwners(t *testing.T) {
	makeIsolatedTestSecretStore(t, "dev")
	for _, mode := range []string{"original-and-support-and-future", "pvc-replaced", "pv-replaced", "csi-replaced", "statefulset-replaced", "pod-owner-replaced"} {
		t.Run(mode, func(t *testing.T) {
			l, o, r, s := runtimeAllocationFixture(t)
			objects := runtimeAllocationNativeObjects(t, l, s)
			volume := l.Volumes[22]
			claimID := "PersistentVolumeClaim/" + volume.Namespace + "/" + volume.PVCName
			stsID := s[1].Plan.Resources[3].ID
			switch mode {
			case "pvc-replaced":
				deploymentplan.Map(objects[claimID]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "pv-replaced":
				deploymentplan.Map(objects["PersistentVolume//"+volume.PVName]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "csi-replaced":
				deploymentplan.Map(deploymentplan.Map(objects["PersistentVolume//"+volume.PVName]["spec"])["csi"])["volumeHandle"] = "999-foreign"
			case "statefulset-replaced":
				deploymentplan.Map(objects[stsID]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "pod-owner-replaced":
				pod := objects["Pod/"+volume.Namespace+"/zammad-elasticsearch-master-0"]
				refs := certIssuerObjectList(deploymentplan.Map(pod["metadata"])["ownerReferences"])
				deploymentplan.Map(refs[0])["uid"] = "99999999-9999-4999-8999-999999999999"
			}
			b, _ := plannedVolumeReadBackend(t, objects)
			before := deploymentplan.Digest(l)
			got, err := observeManagedLatePlannedVolumeStages(context.Background(), b, l, o, r, s)
			if mode == "original-and-support-and-future" {
				if err != nil || len(got.Volumes) != 25 {
					t.Fatal("fresh three-source native proof", err)
				}
			} else if err == nil {
				t.Fatal("foreign native allocation accepted")
			}
			if deploymentplan.Digest(l) != before {
				t.Fatal("observation rewrote ledger")
			}
		})
	}
}
