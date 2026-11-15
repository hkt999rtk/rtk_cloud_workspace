package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The Redis storage phase precedes the workload phase. Only the test fake API
// uses this provisioner; the resulting identity is a mock CSI observation and
// not a real Linode volume, Kubernetes controller, or durability guarantee.
func (c *isolatedOpenBaoFirstController) observeMockRedisClaim(readOnlyRequest bool) error {
	bad := errors.New("isolated Redis mock CSI rejected")
	if c.cfg.Environment != "dev" || !isolatedScriptTempPath(c.store.Root) || !validIsolatedOpenBaoVolumeParent(c.volumeParent) {
		return bad
	}
	checkpoint, err := inspectCreateRecoveryCheckpoint(c.cfg, c.store)
	if err != nil {
		return bad
	}
	original, sha, err := loadEnvironmentCreateAdmittedGeneration(c.store, checkpoint.Journal.PlanSHA256, checkpoint.Journal.ArtifactSHA256)
	if err != nil || !original.OpenBaoFirst || sha != deploymentplan.Digest(c.cfg) {
		return bad
	}
	var entry *environmentCreateJournalEntry
	for i := range checkpoint.Journal.Entries {
		e := &checkpoint.Journal.Entries[i]
		if e.Key == "kubernetes/foundation-services" {
			if entry != nil {
				return bad
			}
			entry = e
		}
	}
	if entry == nil || (entry.State != "pending" && entry.State != "allocated" && entry.State != "completed") {
		return bad
	}
	parent := &environmentCreateExecution{Store: c.store, owner: checkpoint.Owner, Journal: checkpoint.Journal}
	child, journal, err := originalCreateChildAtIntent(c.store, parent, *entry)
	if err != nil || child.Environment != c.cfg.Environment || child.Stack != original.Stack || child.ConfigurationSHA256 != original.ConfigurationSHA256 || child.ReleaseSHA256 != deploymentplan.Digest(original.Release) || journal.PlanSHA256 != child.SHA256() || journal.InFlight != "" {
		return bad
	}
	readOnlyFailure := journal.Failed != ""
	if readOnlyFailure && !isolatedRedisAcceptedWriteFailure(child, journal) {
		return bad
	}
	ns := original.Stack + "-platform"
	id := "PersistentVolumeClaim/" + ns + "/redis-data"
	claimPath := filepath.Join(c.objects, "PersistentVolumeClaim_"+ns+"_redis-data.json")
	var claim deploymentplan.Object
	if readExecutionJSON(claimPath, &claim) != nil {
		return bad
	}
	meta := deploymentplan.Map(claim["metadata"])
	uid := deploymentplan.String(meta["uid"])
	if uid == "" || journal.Owners[id] != uid || meta["deletionTimestamp"] != nil {
		return bad
	}
	match := false
	for _, r := range child.Resources {
		if r.ID == id && r.Action == "create" && deploymentplan.Matches(r.Desired, claim) {
			match = true
			break
		}
	}
	if !match {
		return bad
	}
	alreadyProvisioned := c.redisVolumeRoot != ""
	if !alreadyProvisioned {
		if readOnlyRequest || readOnlyFailure || journal.Accepted {
			return bad
		}
		root, err := os.MkdirTemp(c.volumeParent, "redis-volume-")
		if err != nil {
			return bad
		}
		if err := os.Chmod(root, 0700); err != nil {
			_ = os.RemoveAll(root)
			return bad
		}
		pvUID, err := isolatedOpenBaoMockUUID()
		if err != nil {
			_ = os.RemoveAll(root)
			return bad
		}
		c.redisVolumeRoot, c.redisPVCUID, c.redisPVUID = root, uid, pvUID
	}
	if c.redisPVCUID != uid {
		return bad
	}
	info, err := os.Lstat(c.redisVolumeRoot)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 || !isolatedScriptTempPath(c.redisVolumeRoot) {
		return bad
	}
	pvName := "isolated-mock-" + c.redisPVUID
	pv := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": pvName, "uid": c.redisPVUID, "resourceVersion": "isolated-mock-csi", "labels": deploymentplan.Object{"rtk.test/mock-csi": "true"}}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"namespace": ns, "name": "redis-data", "uid": uid}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": "390003"}}, "status": deploymentplan.Object{"phase": "Bound"}}
	pvPath := filepath.Join(c.objects, "PersistentVolume__"+pvName+".json")
	var existing deploymentplan.Object
	if err := readExecutionJSON(pvPath, &existing); err == nil {
		if !isolatedMockCSIObjectMatches(pv, existing) {
			return bad
		}
	} else if _, err := os.Lstat(pvPath); !os.IsNotExist(err) || readOnlyRequest || readOnlyFailure || journal.Accepted || alreadyProvisioned {
		return bad
	}
	spec := deploymentplan.Map(claim["spec"])
	if readOnlyRequest || readOnlyFailure || journal.Accepted || alreadyProvisioned {
		return isolatedRedisReadExistingBinding(claimPath, pvPath, pv, uid, pvName)
	}
	if current := deploymentplan.String(spec["volumeName"]); current != "" && current != pvName {
		return bad
	}
	spec["volumeName"] = pvName
	claim["status"] = deploymentplan.Object{"phase": "Bound"}
	labels := deploymentplan.Map(meta["labels"])
	if labels == nil {
		labels = deploymentplan.Object{}
		meta["labels"] = labels
	}
	labels["rtk.test/mock-csi"] = "true"
	write := func(path string, obj deploymentplan.Object) error {
		b, err := json.Marshal(obj)
		if err != nil {
			return err
		}
		f, err := os.CreateTemp(c.objects, ".isolated-redis-csi-")
		if err != nil {
			return err
		}
		temp := f.Name()
		defer os.Remove(temp)
		if err := f.Chmod(0600); err != nil {
			_ = f.Close()
			return err
		}
		if _, err := f.Write(b); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
		return os.Rename(temp, path)
	}
	if err := write(pvPath, pv); err != nil {
		return bad
	}
	return write(claimPath, claim)
}

func TestIsolatedRedisClaimRefusesMissingOriginalParent(t *testing.T) {
	cfg, store, _ := openBaoFirstTransportFixture(t)
	objects := t.TempDir()
	c := newIsolatedOpenBaoFirstController(t, cfg, store, objects)
	if err := c.observeMockRedisClaim(false); err == nil {
		t.Fatal("missing original Redis claim was accepted")
	}
	if c.redisVolumeRoot != "" {
		t.Fatal("missing original parent created a mock volume")
	}
}

// Read the already provisioned binding only; never create, repair or rewrite
// it when a proof or a repeated native PVC Wait inspects the original state.
func isolatedRedisReadExistingBinding(claimPath, pvPath string, wantPV deploymentplan.Object, uid, pvName string) error {
	bad := errors.New("isolated Redis existing binding unavailable")
	var claim, pv deploymentplan.Object
	if readExecutionJSON(claimPath, &claim) != nil || readExecutionJSON(pvPath, &pv) != nil || !isolatedMockCSIObjectMatches(wantPV, pv) {
		return bad
	}
	meta, spec := deploymentplan.Map(claim["metadata"]), deploymentplan.Map(claim["spec"])
	if uid == "" || meta["uid"] != uid || meta["deletionTimestamp"] != nil || deploymentplan.String(spec["volumeName"]) != pvName || deploymentplan.Map(claim["status"])["phase"] != "Bound" || deploymentplan.Map(meta["labels"])["rtk.test/mock-csi"] != "true" {
		return bad
	}
	return nil
}

func TestIsolatedRedisExistingBindingIsReadOnlyAndRejectsMissing(t *testing.T) {
	dir := t.TempDir()
	claimPath, pvPath := filepath.Join(dir, "claim.json"), filepath.Join(dir, "pv.json")
	claim := deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "claim-uid", "labels": deploymentplan.Object{"rtk.test/mock-csi": "true"}}, "spec": deploymentplan.Object{"volumeName": "pv-name"}, "status": deploymentplan.Object{"phase": "Bound"}}
	pv := deploymentplan.Object{"kind": "PersistentVolume", "metadata": deploymentplan.Object{"uid": "pv-uid"}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"uid": "claim-uid"}}}
	claimBytes, _ := json.Marshal(claim)
	pvBytes, _ := json.Marshal(pv)
	if err := os.WriteFile(claimPath, claimBytes, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(pvPath, pvBytes, 0600); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := isolatedRedisReadExistingBinding(claimPath, pvPath, pv, "claim-uid", "pv-name"); err != nil {
			t.Fatal(err)
		}
	}
	a, _ := os.ReadFile(claimPath)
	b, _ := os.ReadFile(pvPath)
	if !bytes.Equal(a, claimBytes) || !bytes.Equal(b, pvBytes) {
		t.Fatal("repeated read changed an object")
	}
	if err := os.Remove(pvPath); err != nil {
		t.Fatal(err)
	}
	if isolatedRedisReadExistingBinding(claimPath, pvPath, pv, "claim-uid", "pv-name") == nil {
		t.Fatal("partial binding accepted")
	}
	a, _ = os.ReadFile(claimPath)
	if !bytes.Equal(a, claimBytes) {
		t.Fatal("partial read changed claim")
	}
	if _, err := os.Lstat(pvPath); !os.IsNotExist(err) {
		t.Fatal("partial read recreated missing PV")
	}
}

// Match only the original fully-written acceptance failure which native
// AdmitResumable verifies. Unknown failures and partial writes stay rejected.
func isolatedRedisAcceptedWriteFailure(plan *deploymentplan.Plan, journal deploymentplan.Journal) bool {
	if plan == nil || journal.Accepted || !journal.AcceptanceStarted || journal.Failed != "deployment.post-deploy.workloads" || journal.InFlight != "" || len(journal.Completed) != len(plan.Resources) || len(journal.Owners) != len(plan.Resources) {
		return false
	}
	for i, r := range plan.Resources {
		if journal.Completed[i] != r.ID || journal.Owners[r.ID] == "" {
			return false
		}
	}
	return true
}

func TestIsolatedRedisReadOnlyFailureRejectsIncompleteAndForeign(t *testing.T) {
	plan := &deploymentplan.Plan{Resources: []deploymentplan.Resource{{ID: "PersistentVolumeClaim/ns/redis-data"}, {ID: "Deployment/ns/redis"}}}
	good := deploymentplan.Journal{AcceptanceStarted: true, Failed: "deployment.post-deploy.workloads", Completed: []string{plan.Resources[0].ID, plan.Resources[1].ID}, Owners: map[string]string{plan.Resources[0].ID: "pvc-owner", plan.Resources[1].ID: "deployment-owner"}}
	if !isolatedRedisAcceptedWriteFailure(plan, good) {
		t.Fatal("complete original failure rejected")
	}
	for _, mutate := range []func(*deploymentplan.Journal){
		func(j *deploymentplan.Journal) { j.Failed = "foreign" },
		func(j *deploymentplan.Journal) { j.InFlight = "unknown" },
		func(j *deploymentplan.Journal) { j.AcceptanceStarted = false },
		func(j *deploymentplan.Journal) { j.Completed = j.Completed[:1] },
		func(j *deploymentplan.Journal) { j.Owners = map[string]string{plan.Resources[0].ID: "pvc-owner"} },
	} {
		j := good
		mutate(&j)
		if isolatedRedisAcceptedWriteFailure(plan, j) {
			t.Fatal("foreign or incomplete journal accepted")
		}
	}
}
