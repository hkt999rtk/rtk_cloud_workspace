package main

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// These identities are synthetic and exist only in the file-backed fake API.
// The two paths are real bind mounts of this isolated test's OpenBao container;
// they are not a Linode CSI driver or evidence of production durability.
type isolatedOpenBaoMockVolume struct{ PVCUID, PVUID, Handle string }

func isolatedOpenBaoMockUUID() (string, error) {
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return "", err
	}
	id[6] = id[6]&15 | 0x40
	id[8] = id[8]&63 | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:]), nil
}

func (c *isolatedOpenBaoFirstController) observeMockVolumes(set deploymentplan.Object, owner string, readOnly bool) ([]any, error) {
	bad := func(stage string) error { return errors.New("stage-" + stage) }
	if c.rpc == nil || !isolatedScriptTempPath(c.volumeRoot) || !c.rpc.containerOwned() {
		return nil, bad("process-owner")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	raw, err := exec.CommandContext(ctx, c.rpc.docker, "inspect", "--format", "{{json .Mounts}}", c.rpc.container).Output()
	if err != nil {
		return nil, bad("docker-inspect")
	}
	var mounts []struct {
		Source, Destination string
		RW                  bool
	}
	if json.Unmarshal(raw, &mounts) != nil {
		return nil, bad("docker-mount-json")
	}
	for _, name := range []string{"data", "audit"} {
		path := filepath.Join(c.volumeRoot, name)
		info, err := os.Lstat(path)
		if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
			return nil, bad("host-directory")
		}
		found := 0
		for _, mount := range mounts {
			if mount.Source == path && mount.Destination == "/openbao/"+name && mount.RW {
				found++
			}
		}
		if found != 1 {
			return nil, bad("bound-mount")
		}
	}
	templates := certIssuerObjectList(deploymentplan.Map(set["spec"])["volumeClaimTemplates"])
	if len(templates) != 2 {
		return nil, bad("claim-templates")
	}
	if c.mockVolumes == nil {
		if readOnly {
			return nil, bad("missing-original")
		}
		c.mockVolumes = map[string]isolatedOpenBaoMockVolume{}
	}
	byName := map[string]deploymentplan.Object{}
	for _, value := range templates {
		template := deploymentplan.Map(value)
		name := deploymentplan.String(deploymentplan.Map(template["metadata"])["name"])
		if (name != "data" && name != "audit") || byName[name] != nil {
			return nil, bad("claim-template-name")
		}
		byName[name] = template
	}
	var objects []struct {
		path string
		obj  deploymentplan.Object
	}
	var podVolumes []any
	for i, name := range []string{"data", "audit"} {
		template := byName[name]
		if template == nil {
			return nil, bad("claim-missing")
		}
		v, exists := c.mockVolumes[name]
		if !exists {
			if readOnly {
				return nil, bad("missing-original")
			}
			pvcUID, e1 := isolatedOpenBaoMockUUID()
			pvUID, e2 := isolatedOpenBaoMockUUID()
			if e1 != nil || e2 != nil {
				return nil, bad("uuid")
			}
			v = isolatedOpenBaoMockVolume{PVCUID: pvcUID, PVUID: pvUID, Handle: fmt.Sprintf("39000%d", i+1)}
			c.mockVolumes[name] = v
		}
		claimName := name + "-openbao-0"
		pvName := "isolated-mock-" + v.PVUID
		pvcSpec := deploymentplan.Clone(deploymentplan.Map(template["spec"]))
		pvcSpec["volumeName"] = pvName
		pvc := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": claimName, "namespace": c.namespace, "uid": v.PVCUID, "resourceVersion": "isolated-mock-csi", "labels": deploymentplan.Object{"rtk.test/mock-csi": "true"}, "ownerReferences": []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "name": "openbao", "uid": owner, "controller": true}}}, "spec": pvcSpec, "status": deploymentplan.Object{"phase": "Bound"}}
		pv := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": pvName, "uid": v.PVUID, "resourceVersion": "isolated-mock-csi", "labels": deploymentplan.Object{"rtk.test/mock-csi": "true"}}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"namespace": c.namespace, "name": claimName, "uid": v.PVCUID}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": v.Handle}}, "status": deploymentplan.Object{"phase": "Bound"}}
		for _, entry := range []struct {
			path string
			obj  deploymentplan.Object
		}{{filepath.Join(c.objects, "PersistentVolumeClaim_"+c.namespace+"_"+claimName+".json"), pvc}, {filepath.Join(c.objects, "PersistentVolume__"+pvName+".json"), pv}} {
			var current deploymentplan.Object
			err := readExecutionJSON(entry.path, &current)
			if err == nil && !isolatedMockCSIObjectMatches(entry.obj, current) {
				return nil, bad("existing-object-drift")
			}
			if err != nil {
				if _, statErr := os.Lstat(entry.path); !os.IsNotExist(statErr) {
					return nil, bad("existing-object-read")
				}
			}
			objects = append(objects, entry)
		}
		podVolumes = append(podVolumes, deploymentplan.Object{"name": name, "persistentVolumeClaim": deploymentplan.Object{"claimName": claimName}})
	}
	for _, entry := range objects {
		if _, err := os.Lstat(entry.path); err == nil {
			continue
		}
		if readOnly {
			return nil, bad("missing-original")
		}
		data, err := json.Marshal(entry.obj)
		if err != nil {
			return nil, bad("marshal")
		}
		temp, err := os.CreateTemp(c.objects, ".isolated-csi-")
		if err != nil {
			return nil, bad("temp-file")
		}
		path := temp.Name()
		if err := temp.Chmod(0600); err != nil {
			_ = temp.Close()
			_ = os.Remove(path)
			return nil, bad("temp-permissions")
		}
		if _, err := temp.Write(data); err != nil {
			_ = temp.Close()
			_ = os.Remove(path)
			return nil, bad("temp-write")
		}
		if err := temp.Close(); err != nil {
			_ = os.Remove(path)
			return nil, bad("temp-close")
		}
		if err := os.Rename(path, entry.path); err != nil {
			_ = os.Remove(path)
			return nil, bad("temp-rename")
		}
	}
	return podVolumes, nil
}

func isolatedMockCSIObjectMatches(expected, actual deploymentplan.Object) bool {
	if expected == nil || actual == nil || !deploymentplan.Matches(expected, actual) {
		return false
	}
	want, got := deploymentplan.Map(expected["metadata"]), deploymentplan.Map(actual["metadata"])
	return want["uid"] != nil && want["uid"] == got["uid"] && got["deletionTimestamp"] == nil && reflect.DeepEqual(want["ownerReferences"], got["ownerReferences"])
}

func TestIsolatedMockCSIRejectsForeignOwnersAndClaimBinding(t *testing.T) {
	pvc := deploymentplan.Object{"kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"uid": "pvc-uid", "ownerReferences": []any{deploymentplan.Object{"uid": "set-uid", "controller": true}}}}
	pv := deploymentplan.Object{"kind": "PersistentVolume", "metadata": deploymentplan.Object{"uid": "pv-uid"}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"uid": "pvc-uid", "name": "data-openbao-0", "namespace": "video-cloud-dev-secrets"}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": "390001"}}}
	for _, tc := range []struct {
		name     string
		original deploymentplan.Object
		mutate   func(deploymentplan.Object)
	}{
		{"pvc-uid", pvc, func(o deploymentplan.Object) { deploymentplan.Map(o["metadata"])["uid"] = "foreign" }},
		{"set-owner", pvc, func(o deploymentplan.Object) {
			deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(o["metadata"])["ownerReferences"])[0])["uid"] = "foreign"
		}},
		{"pv-uid", pv, func(o deploymentplan.Object) { deploymentplan.Map(o["metadata"])["uid"] = "foreign" }},
		{"claim-ref", pv, func(o deploymentplan.Object) {
			deploymentplan.Map(deploymentplan.Map(o["spec"])["claimRef"])["uid"] = "foreign"
		}},
		{"volume-handle", pv, func(o deploymentplan.Object) {
			deploymentplan.Map(deploymentplan.Map(o["spec"])["csi"])["volumeHandle"] = "390002"
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			actual := deploymentplan.Clone(tc.original)
			if !isolatedMockCSIObjectMatches(tc.original, actual) {
				t.Fatal("exact synthetic object refused")
			}
			tc.mutate(actual)
			if isolatedMockCSIObjectMatches(tc.original, actual) {
				t.Fatal("foreign mock CSI binding accepted")
			}
		})
	}
}
