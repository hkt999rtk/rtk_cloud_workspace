package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

func plannedVolumeFixture(t *testing.T) (environmentCreateResourceLedger, map[string]environmentCreateVolumeOwner, *environmentCreateVolumeOwner, *deploymentplan.Plan, *deploymentplan.Plan, deploymentplan.Journal) {
	t.Helper()
	ledger := environmentCreateResourceLedger{Environment: "dev", Stack: "video-cloud-dev", Kubernetes: map[string]string{}}
	original := map[string]environmentCreateVolumeOwner{}
	volume := func(i int, name, ns string) environmentCreateVolumeOwner {
		return environmentCreateVolumeOwner{NativeID: fmt.Sprint(i), PVName: fmt.Sprint("pv-", i), PVUID: fmt.Sprintf("%08d-1111-4111-8111-111111111111", i), PVCUID: fmt.Sprintf("%08d-2222-4222-8222-222222222222", i), PVCName: name, Namespace: ns}
	}
	for i := 1; i <= 18; i++ {
		o := volume(i, fmt.Sprint("old-", i), ledger.Stack+"-video-cloud")
		original["PersistentVolumeClaim/"+o.Namespace+"/"+o.PVCName] = o
		ledger.Volumes = append(ledger.Volumes, o)
	}
	redis := volume(19, "redis-data", ledger.Stack+"-platform")
	ledger.Volumes = append(ledger.Volumes, redis)
	spec := deploymentplan.Object{"storageClassName": "linode-block-storage-retain", "accessModes": []any{"ReadWriteOnce"}, "resources": deploymentplan.Object{"requests": deploymentplan.Object{"storage": "5Gi"}}}
	frozen := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: deploymentplan.Inputs{Environment: "dev", Stack: ledger.Stack, Operation: "create", ConfigurationSHA256: strings.Repeat("1", 64), ReleaseSHA256: strings.Repeat("2", 64)}}
	for i, name := range []string{"cloud-logger-billing-inbox", "video-cloud-mqttusage-checkpoint", "video-cloud-prometheus-data"} {
		ns := ledger.Stack + "-video-cloud"
		if i == 0 {
			ns = ledger.Stack + "-logger"
		}
		body := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"namespace": ns, "name": name}, "spec": deploymentplan.Clone(spec)}
		frozen.Resources = append(frozen.Resources, deploymentplan.Resource{ID: deploymentplan.ID(body), Action: "create", Phase: "storage", Desired: body})
		o := volume(20+i, name, ns)
		ledger.Volumes = append(ledger.Volumes, o)
		ledger.Kubernetes[deploymentplan.ID(body)] = o.PVCUID
	}
	sts := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"name": "support-db", "namespace": ledger.Stack + "-support"}, "spec": deploymentplan.Object{"replicas": float64(3), "volumeClaimTemplates": []any{deploymentplan.Object{"metadata": deploymentplan.Object{"name": "data"}, "spec": deploymentplan.Clone(spec)}}}}
	frozen.Resources = append(frozen.Resources, deploymentplan.Resource{ID: deploymentplan.ID(sts), Action: "create", Phase: "workload", Desired: sts})
	for i := 0; i < 3; i++ {
		o := volume(23+i, fmt.Sprint("data-support-db-", i), ledger.Stack+"-support")
		ledger.Volumes = append(ledger.Volumes, o)
		ledger.Kubernetes["PersistentVolumeClaim/"+o.Namespace+"/"+o.PVCName] = o.PVCUID
	}
	child := *frozen
	child.Operation = "scoped"
	j := deploymentplan.Journal{PlanSHA256: child.SHA256(), Owners: map[string]string{}}
	for i, r := range child.Resources {
		uid := fmt.Sprintf("%08d-3333-4333-8333-333333333333", i+1)
		if i < 3 {
			uid = ledger.Volumes[19+i].PVCUID
			j.Completed = append(j.Completed, r.ID)
		}
		j.Owners[r.ID] = uid
		ledger.Kubernetes[r.ID] = uid
	}
	return ledger, original, &redis, frozen, &child, j
}

func plannedVolumeNativeObjects(t *testing.T, ledger environmentCreateResourceLedger, frozen, child *deploymentplan.Plan, journal deploymentplan.Journal) map[string]deploymentplan.Object {
	t.Helper()
	claims, err := managedLatePlannedVolumeClaims(frozen)
	if err != nil {
		t.Fatal(err)
	}
	objects := map[string]deploymentplan.Object{}
	for _, o := range ledger.Volumes {
		id := "PersistentVolumeClaim/" + o.Namespace + "/" + o.PVCName
		planned, ok := claims[id]
		if !ok {
			continue
		}
		claim := deploymentplan.Clone(planned.Claim)
		deploymentplan.Map(claim["metadata"])["uid"] = o.PVCUID
		deploymentplan.Map(claim["spec"])["volumeName"] = o.PVName
		claim["status"] = deploymentplan.Object{"phase": "Bound"}
		objects[id] = claim
		objects["PersistentVolume//"+o.PVName] = deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": o.PVName, "uid": o.PVUID}, "spec": deploymentplan.Object{"storageClassName": "linode-block-storage-retain", "persistentVolumeReclaimPolicy": "Retain", "claimRef": deploymentplan.Object{"namespace": o.Namespace, "name": o.PVCName, "uid": o.PVCUID}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": o.NativeID + "-pvc-original"}}, "status": deploymentplan.Object{"phase": "Bound"}}
		if planned.PodName == "" {
			continue
		}
		for _, r := range child.Resources {
			if r.ID == planned.ResourceID {
				objects[r.ID] = deploymentplan.Clone(r.Desired)
				deploymentplan.Map(objects[r.ID]["metadata"])["uid"] = journal.Owners[r.ID]
			}
		}
		objects["Pod/"+o.Namespace+"/"+planned.PodName] = deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": planned.PodName, "namespace": o.Namespace, "uid": "99999999-1111-4111-8111-111111111111", "ownerReferences": []any{deploymentplan.Object{"controller": true, "apiVersion": "apps/v1", "kind": "StatefulSet", "name": "support-db", "uid": journal.Owners[planned.ResourceID]}}}, "spec": deploymentplan.Object{"volumes": []any{deploymentplan.Object{"name": "data", "persistentVolumeClaim": deploymentplan.Object{"claimName": o.PVCName}}}}}
	}
	return objects
}

func TestManagedLatePlannedVolumesCaptureReadsWrittenOwnersBeforeReadinessCompletion(t *testing.T) {
	makeIsolatedTestSecretStore(t, "dev")
	ledger, original, redis, frozen, child, journal := plannedVolumeFixture(t)
	objects := plannedVolumeNativeObjects(t, ledger, frozen, child, journal)
	backend, publish := plannedVolumeReadBackend(t, objects)
	// The existing ledger records native allocation tuples; this capture adds
	// their exact child-write owners only after current PVC/PV/Pod GETs.
	ledger.Kubernetes = map[string]string{}
	before := deploymentplan.Digest(ledger)
	got, err := observeManagedLatePlannedVolumes(context.Background(), backend, ledger, original, redis, frozen, child, journal)
	if err != nil || len(got.Volumes) != 25 || len(got.Kubernetes) != 7 || deploymentplan.Digest(ledger) != before || journal.Accepted || len(journal.Completed) != 3 {
		t.Fatal("partial durable write did not yield current allocation owners", err)
	}
	claimID := "PersistentVolumeClaim/" + ledger.Volumes[24].Namespace + "/" + ledger.Volumes[24].PVCName
	deploymentplan.Map(objects[claimID]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
	publish()
	if _, err := observeManagedLatePlannedVolumes(context.Background(), backend, ledger, original, redis, frozen, child, journal); err == nil || deploymentplan.Digest(ledger) != before {
		t.Fatal("replaced native claim accepted or failed capture changed ledger")
	}
}

func TestManagedLatePlannedVolumesUseFrozenGraphAndInterruptedWriteProvenance(t *testing.T) {
	ledger, original, redis, frozen, child, j := plannedVolumeFixture(t)
	before := deploymentplan.Digest(ledger)
	got, err := managedLatePlannedVolumeProjection(ledger, original, redis, frozen, child, j)
	if err != nil || len(got.Volumes) != 18 || len(ledger.Volumes) != 25 || deploymentplan.Digest(ledger) != before {
		t.Fatal("18 + Redis + six declared allocations", err)
	}
	// StatefulSet Wait captures its native claims after its durable write owner,
	// but before Execute appends that resource to Completed.
	if len(j.Completed) != 3 || j.Accepted {
		t.Fatal("not an interrupted-stage fixture")
	}
	j.Completed = append(j.Completed, child.Resources[3].ID)
	j.AcceptanceStarted = true
	j.Accepted = true
	if _, err := managedLatePlannedVolumeProjection(ledger, original, redis, frozen, child, j); err != nil {
		t.Fatal("accepted stage", err)
	}
	for _, mode := range []string{"foreign-claim", "unplanned-frozen", "old-owner", "uid-drift", "native-alias", "pv-alias", "missing-receipt", "inflight-owner", "unwritten-owner", "foreign-template-owner", "changed-child-body", "accepted-missing-volume"} {
		t.Run(mode, func(t *testing.T) {
			l := ledger
			l.Volumes = slices.Clone(ledger.Volumes)
			l.Kubernetes = map[string]string{}
			for k, v := range ledger.Kubernetes {
				l.Kubernetes[k] = v
			}
			q := j
			q.Completed = slices.Clone(j.Completed)
			q.Owners = map[string]string{}
			for k, v := range j.Owners {
				q.Owners[k] = v
			}
			p := *child
			p.Resources = slices.Clone(child.Resources)
			switch mode {
			case "foreign-claim":
				l.Volumes[24].PVCName = "foreign"
			case "unplanned-frozen":
				p.Resources[3].Desired = deploymentplan.Clone(p.Resources[3].Desired)
				deploymentplan.Map(p.Resources[3].Desired["spec"])["replicas"] = float64(4)
				q.PlanSHA256 = p.SHA256()
			case "old-owner":
				l.Volumes[0].PVCUID = l.Volumes[24].PVCUID
			case "uid-drift":
				l.Kubernetes["PersistentVolumeClaim/"+l.Volumes[24].Namespace+"/"+l.Volumes[24].PVCName] = "99999999-9999-4999-8999-999999999999"
			case "native-alias":
				l.Volumes[24].NativeID = l.Volumes[0].NativeID
			case "pv-alias":
				l.Volumes[24].PVUID = l.Volumes[0].PVUID
			case "missing-receipt":
				delete(q.Owners, p.Resources[0].ID)
			case "inflight-owner":
				q.InFlight = p.Resources[3].ID
			case "unwritten-owner":
				q.Completed = q.Completed[:1]
			case "foreign-template-owner":
				l.Kubernetes[p.Resources[3].ID] = "99999999-9999-4999-8999-999999999999"
			case "changed-child-body":
				p.Resources[0].Desired = deploymentplan.Clone(p.Resources[0].Desired)
				deploymentplan.Map(p.Resources[0].Desired["spec"])["storageClassName"] = "foreign"
				q.PlanSHA256 = p.SHA256()
			case "accepted-missing-volume":
				l.Volumes = l.Volumes[:24]
			}
			if _, err := managedLatePlannedVolumeProjection(l, original, redis, frozen, &p, q); err == nil {
				t.Fatal("foreign/unproved allocation accepted")
			}
		})
	}
}

func TestManagedLatePlannedVolumesNativeCaptureBindsTemplateAndCSI(t *testing.T) {
	ledger, _, _, frozen, child, j := plannedVolumeFixture(t)
	claims, err := managedLatePlannedVolumeClaims(frozen)
	if err != nil {
		t.Fatal(err)
	}
	for _, index := range []int{19, 24} {
		o := ledger.Volumes[index]
		id := "PersistentVolumeClaim/" + o.Namespace + "/" + o.PVCName
		planned := claims[id]
		claim := deploymentplan.Clone(planned.Claim)
		deploymentplan.Map(claim["metadata"])["uid"] = o.PVCUID
		deploymentplan.Map(claim["spec"])["volumeName"] = o.PVName
		claim["status"] = deploymentplan.Object{"phase": "Bound"}
		pv := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": o.PVName, "uid": o.PVUID}, "spec": deploymentplan.Object{"storageClassName": "linode-block-storage-retain", "persistentVolumeReclaimPolicy": "Retain", "claimRef": deploymentplan.Object{"namespace": o.Namespace, "name": o.PVCName, "uid": o.PVCUID}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": o.NativeID + "-pvc-original"}}, "status": deploymentplan.Object{"phase": "Bound"}}
		var controller, pod deploymentplan.Object
		if planned.PodName != "" {
			controller = deploymentplan.Clone(child.Resources[3].Desired)
			uid := j.Owners[planned.ResourceID]
			deploymentplan.Map(controller["metadata"])["uid"] = uid
			pod = deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": planned.PodName, "namespace": o.Namespace, "uid": "99999999-1111-4111-8111-111111111111", "ownerReferences": []any{deploymentplan.Object{"controller": true, "apiVersion": "apps/v1", "kind": "StatefulSet", "name": "support-db", "uid": uid}}}, "spec": deploymentplan.Object{"volumes": []any{deploymentplan.Object{"name": "data", "persistentVolumeClaim": deploymentplan.Object{"claimName": o.PVCName}}}}}
		}
		if _, err := managedLateNativePlannedVolume(o, planned, child, j, claim, pv, controller, pod); err != nil {
			t.Fatal("fresh bound native", index, err)
		}
		for _, mode := range []string{"claim-uid", "claim-ref", "driver", "native-id", "pv-uid", "not-bound", "data-source", "controller-lineage"} {
			t.Run(fmt.Sprint(index, "/", mode), func(t *testing.T) {
				c, v, p := deploymentplan.Clone(claim), deploymentplan.Clone(pv), deploymentplan.Clone(pod)
				switch mode {
				case "claim-uid":
					deploymentplan.Map(c["metadata"])["uid"] = "foreign"
				case "claim-ref":
					deploymentplan.Map(deploymentplan.Map(v["spec"])["claimRef"])["uid"] = "foreign"
				case "driver":
					deploymentplan.Map(deploymentplan.Map(v["spec"])["csi"])["driver"] = "foreign"
				case "native-id":
					deploymentplan.Map(deploymentplan.Map(v["spec"])["csi"])["volumeHandle"] = "991-pvc-foreign"
				case "pv-uid":
					deploymentplan.Map(v["metadata"])["uid"] = "foreign"
				case "not-bound":
					deploymentplan.Map(v["status"])["phase"] = "Pending"
				case "data-source":
					deploymentplan.Map(c["spec"])["dataSource"] = deploymentplan.Object{"name": "foreign"}
				case "controller-lineage":
					if planned.PodName == "" {
						return
					}
					deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(p["metadata"])["ownerReferences"])[0])["uid"] = "foreign"
				}
				if _, err := managedLateNativePlannedVolume(o, planned, child, j, c, v, controller, p); err == nil {
					t.Fatal("foreign native capture accepted")
				}
			})
		}
	}
}

func plannedVolumeReadBackend(t *testing.T, objects map[string]deploymentplan.Object) (environmentKubernetesBackend, func()) {
	t.Helper()
	selectedRoot := os.Getenv("RTK_CLOUD_CONFIG_ROOT")
	raw, err := yaml.Marshal(frozenKubernetesFixture(t))
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", selectedRoot)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := newEnvironmentKubernetesBackend(string(raw), "dev")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	path := filepath.Join(directory, "objects.json")
	writeTestFile(t, filepath.Join(directory, "kubectl"), `#!/usr/bin/env python3
import json, os, sys
a=sys.argv[1:]; i=a.index('get'); kind,name=a[i+1:i+3]
ns=a[a.index('--namespace')+1] if '--namespace' in a else ''
with open(os.environ['RTK_TEST_PLANNED_OBJECTS']) as f: obj=json.load(f).get(kind+'/'+ns+'/'+name)
if obj is None: sys.exit(1)
print(json.dumps(obj))
`)
	if err := os.Chmod(filepath.Join(directory, "kubectl"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_TEST_PLANNED_OBJECTS", path)
	publish := func() {
		body, _ := json.Marshal(objects)
		if err := os.WriteFile(path, body, 0600); err != nil {
			t.Fatal(err)
		}
	}
	publish()
	return backend, publish
}

func TestManagedLatePlannedVolumesQuotaUsesOriginalWrittenChildAndFreshClaims(t *testing.T) {
	cfg, store, operator, artifact := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	ledger, original, redis, frozen, child, journal := plannedVolumeFixture(t)
	// The unified driver executes this literal frozen full plan, rather than
	// recompiling it as a separate scoped bootstrap child.
	child.Operation = frozen.Operation
	journal.PlanSHA256 = child.SHA256()
	objects := plannedVolumeNativeObjects(t, ledger, frozen, child, journal)
	backend, publish := plannedVolumeReadBackend(t, objects)
	c := &compiledEnvironmentDeployment{Store: store, Backend: backend, Plan: frozen}
	h := &createNormalTailHandoff{Execution: e, OriginalVolumes: original, RedisVolume: *redis}
	ctx := withCreateNormalTailHandoff(context.Background(), h)
	if count, err := c.recordedNormalTailClaimCount(ctx, frozen); err != nil || count != 0 {
		t.Fatal("unstarted target needs no allocation deduction or frozen file", err)
	}
	put := func(relative string, v any) {
		t.Helper()
		if err := store.write(relative, mustProviderRecoveryJSON(v), true); err != nil {
			t.Fatal(err)
		}
	}
	relative, _ := filepath.Rel(store.Root, filepath.Join(e.Directory, "normal-tail-target.json"))
	put(relative, createNormalTailTarget{Schema: createNormalTailTargetSchema, ParentSHA256: e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(e.owner), Pins: map[string]string{"source": strings.Repeat("a", 64)}, Plan: frozen})
	intent, _ := environmentCreateKubernetesStageIntent(child)
	if err := e.BeginEntry("kubernetes/workload", "workload", intent, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if count, err := c.recordedNormalTailClaimCount(ctx, frozen); err != nil || count != 0 {
		t.Fatal("new parent intent before first child write must keep full quota", err)
	}
	put(filepath.Join("deployment/rollback", child.SHA256(), "plan.json"), child)
	put(filepath.Join("deployment/rollback", child.SHA256(), "journal.json"), journal)
	if err := e.RecordKubernetesLease(deploymentplan.LeaseOwner{ResourceID: "Lease/" + e.Journal.Stack + "-video-cloud/rtk-deployment-execution", UID: "99999999-5555-4555-8555-555555555555", Holder: child.SHA256() + ":original"}); err != nil {
		t.Fatal(err)
	}
	ledger.Schema, ledger.PlanSHA256, ledger.Provider = "rtk-cloud.resource-ledger/v1", e.Journal.PlanSHA256, map[string]environmentCreateProviderReceipt{}
	if err := saveEnvironmentCreateLedger(ctx, e, ledger); err != nil {
		t.Fatal(err)
	}
	before := deploymentplan.Digest(ledger)
	if count, err := c.recordedNormalTailClaimCount(ctx, frozen); err != nil || count != 6 || deploymentplan.Digest(ledger) != before {
		t.Fatal("already allocated graph claims were counted twice", count, err)
	}
	owner := ledger.Volumes[24]
	claim := objects["PersistentVolumeClaim/"+owner.Namespace+"/"+owner.PVCName]
	deploymentplan.Map(claim["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
	publish()
	if _, err := c.recordedNormalTailClaimCount(ctx, frozen); err == nil {
		t.Fatal("replaced claim discounted from remaining quota")
	}
	deploymentplan.Map(claim["metadata"])["uid"] = owner.PVCUID
	publish()
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, err := c.recordedNormalTailClaimCount(cancelled, frozen); err == nil {
		t.Fatal("cancelled native quota proof accepted")
	}
	if err := os.Remove(filepath.Join(store.Root, "deployment/rollback", child.SHA256(), "plan.json")); err != nil {
		t.Fatal(err)
	}
	if _, err := c.recordedNormalTailClaimCount(ctx, frozen); err == nil {
		t.Fatal("orphaned native journal discounted from quota")
	}
}
