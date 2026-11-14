package main

import (
	"context"
	"encoding/base64"
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

// Proof callers re-read existing fake observations and verify the actual
// containers. They must never start, recreate or rewrite a test resource.
func (c *isolatedOpenBaoFirstController) verifyOpenBaoReadOnly(ctx context.Context) error {
	bad := errors.New("isolated original OpenBao observation unavailable")
	if c.rpc == nil || c.podUID == "" || !c.rpc.containerOwned() {
		return bad
	}
	load := func(kind, name string) (deploymentplan.Object, error) {
		var o deploymentplan.Object
		err := readExecutionJSON(filepath.Join(c.objects, kind+"_"+c.namespace+"_"+name+".json"), &o)
		return o, err
	}
	set, e1 := load("StatefulSet", "openbao")
	secret, e2 := load("Secret", "openbao-transport-tls")
	config, e3 := load("ConfigMap", "openbao-file")
	pod, e4 := load("Pod", "openbao-0")
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
		return bad
	}
	_, owner, _, err := isolatedOpenBaoStageMaterial(c.cfg, c.store, set, secret, config)
	if err != nil || deploymentplan.Map(set["metadata"])["uid"] != owner || deploymentplan.Map(pod["metadata"])["uid"] != c.podUID || validateOpenBaoPodOwner(set, pod) != nil {
		return bad
	}
	if _, err := c.observeMockVolumes(set, owner, true); err != nil {
		return bad
	}
	if err := verifyScopedControllerStatus(set, 1); err != nil {
		return bad
	}
	if deploymentplan.Map(pod["status"])["phase"] != "Running" {
		return bad
	}
	statuses := certIssuerObjectList(deploymentplan.Map(pod["status"])["containerStatuses"])
	if len(statuses) != 1 || deploymentplan.Map(statuses[0])["containerID"] != "docker://"+c.rpc.container || deploymentplan.Map(statuses[0])["ready"] != true {
		return bad
	}
	if err := verifyEnvironmentPodContainers(ctx, set, pod, map[string]map[string]bool{}, func(context.Context, string) (map[string]bool, error) {
		return map[string]bool{"sha256:5cdbfe3c85e965315cd3220d2836b8d54b55f95715c551a46e233e0e6e6c9b03": true}, nil
	}); err != nil {
		return bad
	}
	var script string
	for s := range isolatedOpenBaoInitializerScripts() {
		if strings.HasSuffix(s, "result=$?; [ \"$result\" -eq 0 ] || [ \"$result\" -eq 2 ]") {
			script = s
			break
		}
	}
	if script == "" {
		return bad
	}
	response := c.forward(ctx, isolatedOpenBaoRPCRequest{Capability: c.capability, Namespace: c.namespace, Script: script})
	payload, err := base64.StdEncoding.DecodeString(response.Output)
	var actual struct {
		Initialized *bool `json:"initialized"`
		Sealed      *bool `json:"sealed"`
	}
	if response.Error || response.Code != 0 || err != nil || json.Unmarshal(payload, &actual) != nil || actual.Initialized == nil || actual.Sealed == nil || !*actual.Initialized || *actual.Sealed {
		return bad
	}
	return nil
}

func (c *isolatedOpenBaoFirstController) verifyFoundationReadOnly(ctx context.Context) error {
	bad := errors.New("isolated original foundation observation unavailable")
	if len(c.foundationProcesses) != 2 || c.foundationNetwork == "" || c.foundationOwner == "" || c.observeMockRedisClaim(true) != nil {
		return bad
	}
	ns := c.cfg.Values["CLOUD_STACK_NAME"] + "-platform"
	load := func(kind, name string) (deploymentplan.Object, error) {
		var o deploymentplan.Object
		err := readExecutionJSON(filepath.Join(c.objects, kind+"_"+ns+"_"+name+".json"), &o)
		return o, err
	}
	objects := map[string]deploymentplan.Object{}
	for _, v := range []struct{ kind, name string }{{"StatefulSet", "postgresql"}, {"Deployment", "redis"}, {"Secret", "postgresql-runtime"}, {"ConfigMap", "postgresql-initdb"}} {
		o, err := load(v.kind, v.name)
		if err != nil {
			return bad
		}
		objects[v.kind+"/"+ns+"/"+v.name] = o
	}
	authority, err := isolatedFoundationProcessAuthority(c.cfg, c.store, objects)
	if err != nil {
		return bad
	}
	docker, err := exec.LookPath("docker")
	if err != nil {
		return bad
	}
	raw, err := exec.CommandContext(ctx, docker, "network", "inspect", "--format", `{{index .Labels "rtk.test.owner"}}|{{.Internal}}`, c.foundationNetwork).Output()
	if err != nil || strings.TrimSpace(string(raw)) != c.foundationOwner+"|true" {
		return bad
	}
	rs, err := load("ReplicaSet", "redis-isolated")
	if err != nil || deploymentplan.Map(rs["metadata"])["uid"] != c.redisReplicaUID {
		return bad
	}
	live := map[string]deploymentplan.Object{"Deployment/" + ns + "/redis": objects["Deployment/"+ns+"/redis"], "StatefulSet/" + ns + "/postgresql": objects["StatefulSet/"+ns+"/postgresql"], "ReplicaSet/" + ns + "/redis-isolated": rs}
	for _, role := range []string{"redis", "postgres"} {
		kind, name, podName, podUID, controllerUID := "Deployment", "redis", "redis-isolated-0", c.redisPodUID, authority.RedisUID
		if role == "postgres" {
			kind, name, podName, podUID, controllerUID = "StatefulSet", "postgresql", "postgresql-0", c.postgresPodUID, authority.PostgresUID
		}
		controller := objects[kind+"/"+ns+"/"+name]
		pod, err := load("Pod", podName)
		if err != nil || deploymentplan.Map(controller["metadata"])["uid"] != controllerUID || deploymentplan.Map(pod["metadata"])["uid"] != podUID || deploymentplan.Map(pod["status"])["phase"] != "Running" {
			return bad
		}
		owner, err := environmentPodController(pod, live)
		if err != nil || owner != kind+"/"+ns+"/"+name || verifyScopedControllerStatus(controller, 1) != nil {
			return bad
		}
		p := c.foundationProcesses[role]
		statuses := certIssuerObjectList(deploymentplan.Map(pod["status"])["containerStatuses"])
		if len(statuses) != 1 || deploymentplan.Map(statuses[0])["containerID"] != "docker://"+p.Container || deploymentplan.Map(statuses[0])["imageID"] != p.ConfigDigest || deploymentplan.Map(statuses[0])["ready"] != true {
			return bad
		}
		if err := verifyEnvironmentPodContainers(ctx, controller, pod, map[string]map[string]bool{}, func(context.Context, string) (map[string]bool, error) {
			return map[string]bool{p.ConfigDigest: true}, nil
		}); err != nil {
			return bad
		}
		inspect, err := exec.CommandContext(ctx, docker, "inspect", "--format", `{{index .Config.Labels "rtk.test.owner"}}|{{.Id}}|{{.Image}}|{{.State.Running}}|{{.State.StartedAt}}`, p.Container).Output()
		parts := strings.Split(strings.TrimSpace(string(inspect)), "|")
		if err != nil || len(parts) != 5 || parts[0] != c.foundationOwner || parts[1] != p.Container || parts[2] != p.ConfigDigest || parts[3] != "true" || parts[4] != p.StartedAt {
			return bad
		}
		if _, err := time.Parse(time.RFC3339Nano, parts[4]); err != nil {
			return bad
		}
		mountsRaw, err := exec.CommandContext(ctx, docker, "inspect", "--format", "{{json .Mounts}}", p.Container).Output()
		if err != nil {
			return bad
		}
		var mounts []struct {
			Source, Destination string
			RW                  bool
		}
		if json.Unmarshal(mountsRaw, &mounts) != nil {
			return bad
		}
		source, destination := c.redisVolumeRoot, "/data"
		if role == "postgres" {
			source, destination = c.postgresVolumeRoot, "/var/lib/postgresql/data"
		}
		count := 0
		for _, m := range mounts {
			if m.Source == source && m.Destination == destination && m.RW {
				count++
			}
		}
		if count != 1 {
			return bad
		}
		probe := []string{"exec", p.Container, "redis-cli", "PING"}
		want := "PONG"
		if role == "postgres" {
			probe = []string{"exec", p.Container, "psql", "-X", "-A", "-t", "-U", "postgres", "-d", "video_cloud", "-c", "SELECT 1"}
			want = "1"
		}
		out, err := exec.CommandContext(ctx, docker, probe...).Output()
		if err != nil || strings.TrimSpace(string(out)) != want {
			return bad
		}
	}
	// Original synthetic PG PVC/PV must already exist and remain bound.
	pvc, err := load("PersistentVolumeClaim", "data-postgresql-0")
	if err != nil {
		return bad
	}
	pvcMeta := deploymentplan.Map(pvc["metadata"])
	pvName := deploymentplan.String(deploymentplan.Map(pvc["spec"])["volumeName"])
	if pvcMeta["uid"] != c.postgresPVCUID || pvName != "isolated-mock-"+c.postgresPVUID || deploymentplan.Map(pvc["status"])["phase"] != "Bound" {
		return bad
	}
	refs := certIssuerObjectList(pvcMeta["ownerReferences"])
	if len(refs) != 1 || deploymentplan.Map(refs[0])["uid"] != authority.PostgresUID || deploymentplan.Map(refs[0])["kind"] != "StatefulSet" || deploymentplan.Map(refs[0])["controller"] != true {
		return bad
	}
	var pv deploymentplan.Object
	if readExecutionJSON(filepath.Join(c.objects, "PersistentVolume__"+pvName+".json"), &pv) != nil {
		return bad
	}
	pvMeta, pvSpec := deploymentplan.Map(pv["metadata"]), deploymentplan.Map(pv["spec"])
	claim, csi := deploymentplan.Map(pvSpec["claimRef"]), deploymentplan.Map(pvSpec["csi"])
	if pvMeta["uid"] != c.postgresPVUID || claim["uid"] != c.postgresPVCUID || claim["namespace"] != ns || claim["name"] != "data-postgresql-0" || csi["driver"] != "linodebs.csi.linode.com" || csi["volumeHandle"] != "390004" {
		return bad
	}
	return nil
}

func TestIsolatedReadOnlyActionsRejectAbsentWithoutWrites(t *testing.T) {
	cfg, store, _ := openBaoFirstTransportFixture(t)
	objects := t.TempDir()
	c := newIsolatedOpenBaoFirstController(t, cfg, store, objects)
	before, err := os.ReadDir(objects)
	if err != nil {
		t.Fatal(err)
	}
	if c.verifyOpenBaoReadOnly(t.Context()) == nil {
		t.Fatal("missing original OpenBao accepted")
	}
	if c.verifyFoundationReadOnly(t.Context()) == nil {
		t.Fatal("missing foundation containers accepted")
	}
	if c.observeMockRedisClaim(true) == nil {
		t.Fatal("missing Redis binding accepted")
	}
	after, err := os.ReadDir(objects)
	if err != nil || len(before) != len(after) || c.redisVolumeRoot != "" || c.foundationNetwork != "" {
		t.Fatal("read-only action wrote state or started network")
	}
}
