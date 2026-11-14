package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Synthetic K8s observation, conditioned on real owner-labelled processes and
// their SQL/PING probes. No actual Kubernetes/CSI status is claimed.
func (c *isolatedOpenBaoFirstController) observeFoundationProcesses(ctx context.Context) error {
	bad := func(stage string) error { return errors.New("observation-" + stage) }
	ns := c.cfg.Values["CLOUD_STACK_NAME"] + "-platform"
	objects := map[string]deploymentplan.Object{}
	for _, item := range []struct{ kind, name string }{{"StatefulSet", "postgresql"}, {"Deployment", "redis"}, {"Secret", "postgresql-runtime"}, {"ConfigMap", "postgresql-initdb"}} {
		var o deploymentplan.Object
		if readExecutionJSON(filepath.Join(c.objects, item.kind+"_"+ns+"_"+item.name+".json"), &o) != nil {
			return bad("01")
		}
		objects[item.kind+"/"+ns+"/"+item.name] = o
	}
	if err := c.startFoundationProcesses(ctx, objects); err != nil {
		c.t.Log("isolated foundation process:", err.Error())
		return bad("02")
	}
	a, err := isolatedFoundationProcessAuthority(c.cfg, c.store, objects)
	if err != nil {
		return bad("03")
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		return bad("04")
	}
	// Re-probe both exact containers before generating any Ready observation.
	for role, p := range c.foundationProcesses {
		probe := []string{"exec", p.Container, "redis-cli", "PING"}
		expected := "PONG"
		if role == "postgres" {
			probe = []string{"exec", p.Container, "psql", "-X", "-A", "-t", "-U", "postgres", "-d", "video_cloud", "-c", "SELECT 1"}
			expected = "1"
		} else if role != "redis" {
			return bad("05")
		}
		out, err := exec.CommandContext(ctx, docker, probe...).Output()
		if err != nil || strings.TrimSpace(string(out)) != expected {
			return bad("06")
		}
		raw, err := exec.CommandContext(ctx, docker, "inspect", "--format", `{{index .Config.Labels "rtk.test.owner"}}|{{.Id}}|{{.Image}}|{{.State.Running}}|{{.State.StartedAt}}`, p.Container).Output()
		parts := strings.Split(strings.TrimSpace(string(raw)), "|")
		if err != nil || len(parts) != 5 || parts[0] != c.foundationOwner || parts[1] != p.Container || parts[2] != p.ConfigDigest || parts[3] != "true" || parts[4] != p.StartedAt {
			return bad("07")
		}
		if _, err := time.Parse(time.RFC3339Nano, parts[4]); err != nil {
			return bad("08")
		}
	}
	if len(c.foundationProcesses) != 2 {
		return bad("09")
	}
	// Record exact mock PostgreSQL PVC/PV bound to the real process data mount.
	postgres := objects["StatefulSet/"+ns+"/postgresql"]
	templates := certIssuerObjectList(deploymentplan.Map(postgres["spec"])["volumeClaimTemplates"])
	if len(templates) != 1 || deploymentplan.Map(deploymentplan.Map(templates[0])["metadata"])["name"] != "data" {
		return bad("10")
	}
	if c.postgresPVCUID == "" {
		c.postgresPVCUID, err = isolatedOpenBaoMockUUID()
		if err != nil {
			return bad("11")
		}
		c.postgresPVUID, err = isolatedOpenBaoMockUUID()
		if err != nil {
			return bad("12")
		}
	}
	pvName := "isolated-mock-" + c.postgresPVUID
	pvcSpec := deploymentplan.Clone(deploymentplan.Map(deploymentplan.Map(templates[0])["spec"]))
	pvcSpec["volumeName"] = pvName
	pvc := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": "data-postgresql-0", "namespace": ns, "uid": c.postgresPVCUID, "resourceVersion": "isolated-mock-csi", "labels": deploymentplan.Object{"rtk.test/mock-csi": "true"}, "ownerReferences": []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "name": "postgresql", "uid": a.PostgresUID, "controller": true}}}, "spec": pvcSpec, "status": deploymentplan.Object{"phase": "Bound"}}
	pv := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": pvName, "uid": c.postgresPVUID, "resourceVersion": "isolated-mock-csi", "labels": deploymentplan.Object{"rtk.test/mock-csi": "true"}}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"namespace": ns, "name": "data-postgresql-0", "uid": c.postgresPVCUID}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": "390004"}}, "status": deploymentplan.Object{"phase": "Bound"}}
	// Confirm the PostgreSQL process actually binds exactly this mock PV path.
	raw, err := exec.CommandContext(ctx, docker, "inspect", "--format", "{{json .Mounts}}", c.foundationProcesses["postgres"].Container).Output()
	if err != nil {
		return bad("13")
	}
	var mounts []struct {
		Source, Destination string
		RW                  bool
	}
	if json.Unmarshal(raw, &mounts) != nil {
		return bad("14")
	}
	found := 0
	for _, m := range mounts {
		if m.Source == c.postgresVolumeRoot && m.Destination == "/var/lib/postgresql/data" && m.RW {
			found++
		}
	}
	if found != 1 {
		return bad("15")
	}
	if err := c.writeIsolatedFoundationObject(filepath.Join(c.objects, "PersistentVolumeClaim_"+ns+"_data-postgresql-0.json"), pvc); err != nil {
		return bad("16")
	}
	if err := c.writeIsolatedFoundationObject(filepath.Join(c.objects, "PersistentVolume__"+pvName+".json"), pv); err != nil {
		return bad("17")
	}
	// Actual UID lineage in the fake API: Deployment→ReplicaSet→Pod.
	if c.redisReplicaUID == "" {
		c.redisReplicaUID, err = isolatedOpenBaoMockUUID()
		if err != nil {
			return bad("18")
		}
		c.redisPodUID, err = isolatedOpenBaoMockUUID()
		if err != nil {
			return bad("19")
		}
		c.postgresPodUID, err = isolatedOpenBaoMockUUID()
		if err != nil {
			return bad("20")
		}
	}
	redisRS := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": deploymentplan.Object{"name": "redis-isolated", "namespace": ns, "uid": c.redisReplicaUID, "resourceVersion": "isolated", "ownerReferences": []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "name": "redis", "uid": a.RedisUID, "controller": true}}}}
	if err := c.writeIsolatedFoundationObject(filepath.Join(c.objects, "ReplicaSet_"+ns+"_redis-isolated.json"), redisRS); err != nil {
		return bad("21")
	}
	for _, role := range []string{"redis", "postgres"} {
		kind, name, ownerKind, ownerName, ownerUID, podName, podUID, containerName, image := "Deployment", "redis", "ReplicaSet", "redis-isolated", c.redisReplicaUID, "redis-isolated-0", c.redisPodUID, "redis", a.RedisImage
		if role == "postgres" {
			kind, name, ownerKind, ownerName, ownerUID, podName, podUID, containerName, image = "StatefulSet", "postgresql", "StatefulSet", "postgresql", a.PostgresUID, "postgresql-0", c.postgresPodUID, "postgres", a.PostgresImage
		}
		target := objects[kind+"/"+ns+"/"+name]
		spec := deploymentplan.Clone(environmentPKIPod(target))
		if role == "postgres" {
			spec["volumes"] = append(certIssuerObjectList(spec["volumes"]), deploymentplan.Object{"name": "data", "persistentVolumeClaim": deploymentplan.Object{"claimName": "data-postgresql-0"}})
		}
		process := c.foundationProcesses[role]
		pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": podName, "namespace": ns, "uid": podUID, "resourceVersion": "isolated", "ownerReferences": []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": ownerKind, "name": ownerName, "uid": ownerUID, "controller": true}}}, "spec": spec, "status": deploymentplan.Object{"phase": "Running", "conditions": []any{deploymentplan.Object{"type": "Ready", "status": "True"}}, "containerStatuses": []any{deploymentplan.Object{"name": containerName, "image": image, "imageID": process.ConfigDigest, "containerID": "docker://" + process.Container, "ready": true, "started": true, "state": deploymentplan.Object{"running": deploymentplan.Object{"startedAt": process.StartedAt}}}}}}
		if err := c.writeIsolatedFoundationObject(filepath.Join(c.objects, "Pod_"+ns+"_"+podName+".json"), pod); err != nil {
			return bad("22")
		}
	}
	// Both processes were re-probed above. Report only the generation the
	// synthetic API actually assigned to the original owner; Kubernetes/CSI
	// controller behavior remains explicitly simulated.
	for _, item := range []struct{ kind, name, uid string }{{"Deployment", "redis", a.RedisUID}, {"StatefulSet", "postgresql", a.PostgresUID}} {
		path := filepath.Join(c.objects, item.kind+"_"+ns+"_"+item.name+".json")
		var current deploymentplan.Object
		if readExecutionJSON(path, &current) != nil {
			return bad("controller-read")
		}
		original := objects[item.kind+"/"+ns+"/"+item.name]
		if !deploymentplan.Matches(original, current) {
			return bad("controller-drift")
		}
		status, err := isolatedFoundationControllerStatus(current, item.uid, true)
		if err != nil {
			return bad("controller-status")
		}
		current["status"] = status
		data, err := json.Marshal(current)
		if err != nil {
			return bad("controller-encode")
		}
		f, err := os.CreateTemp(c.objects, ".foundation-status-")
		if err != nil {
			return bad("controller-temp")
		}
		temp := f.Name()
		if err = f.Chmod(0600); err == nil {
			_, err = f.Write(data)
		}
		if closeErr := f.Close(); err == nil {
			err = closeErr
		}
		if err == nil {
			err = os.Rename(temp, path)
		}
		_ = os.Remove(temp)
		if err != nil {
			return bad("controller-write")
		}
	}
	return nil
}

func isolatedFoundationControllerStatus(controller deploymentplan.Object, expectedUID string, realProbeOK bool) (deploymentplan.Object, error) {
	bad := errors.New("synthetic controller observation rejected")
	meta := deploymentplan.Map(controller["metadata"])
	generation, ok := meta["generation"].(float64)
	if !realProbeOK || expectedUID == "" || meta["uid"] != expectedUID || meta["deletionTimestamp"] != nil || !ok || generation <= 0 || generation != float64(int(generation)) {
		return nil, bad
	}
	status := deploymentplan.Object{"observedGeneration": generation, "readyReplicas": float64(1), "updatedReplicas": float64(1)}
	switch controller["kind"] {
	case "Deployment":
		status["availableReplicas"] = float64(1)
	case "StatefulSet":
		template := deploymentplan.Map(deploymentplan.Map(controller["spec"])["template"])
		if len(template) == 0 {
			return nil, bad
		}
		revision := "isolated-" + deploymentplan.Digest(template)[:16]
		status["currentRevision"], status["updateRevision"] = revision, revision
	default:
		return nil, bad
	}
	return status, nil
}

func TestIsolatedFoundationControllerStatusRejectsDrift(t *testing.T) {
	base := deploymentplan.Object{"kind": "StatefulSet", "metadata": deploymentplan.Object{"uid": "original", "generation": float64(1)}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "postgres"}}}}}}
	if _, err := isolatedFoundationControllerStatus(base, "original", true); err != nil {
		t.Fatal("original synthetic generation rejected", err)
	}
	if _, err := isolatedFoundationControllerStatus(base, "foreign", true); err == nil {
		t.Fatal("foreign UID accepted")
	}
	if _, err := isolatedFoundationControllerStatus(base, "original", false); err == nil {
		t.Fatal("failed real probe accepted")
	}
	changed := deploymentplan.Clone(base)
	delete(deploymentplan.Map(changed["metadata"]), "generation")
	if _, err := isolatedFoundationControllerStatus(changed, "original", true); err == nil {
		t.Fatal("missing native generation accepted")
	}
}

func (c *isolatedOpenBaoFirstController) writeIsolatedFoundationObject(path string, obj deploymentplan.Object) error {
	if filepath.Dir(path) != c.objects {
		return errors.New("isolated output path required")
	}
	var current deploymentplan.Object
	if readExecutionJSON(path, &current) == nil {
		if !isolatedMockCSIObjectMatches(obj, current) {
			return errors.New("synthetic owner changed")
		}
		return nil
	}
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		return errors.New("synthetic object unreadable")
	}
	b, err := json.Marshal(obj)
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(c.objects, ".foundation-observation-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
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
	return os.Rename(tmp, path)
}
