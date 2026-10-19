package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func newForwardNativeFenceFixture(t *testing.T) (*normalPostgresAccessFixture, *compiledEnvironmentDeployment, func()) {
	t.Helper()
	const ns = "video-cloud-dev-platform"
	const oldClaimUID = "00000033-1111-4111-8111-111111111111"
	claim := func(name string) deploymentplan.Object {
		return deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": name, "namespace": ns}, "spec": deploymentplan.Object{"storageClassName": "fixture", "accessModes": []any{"ReadWriteOnce"}}}
	}
	old := claim("original-input")
	deploymentplan.Map(old["spec"])["volumeName"] = "original-pv"
	old["status"] = deploymentplan.Object{"phase": "Bound"}
	fresh := claim("new-planned")
	pv := func(name, uid, claimName, claimUID, native string) deploymentplan.Object {
		return deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": name, "uid": uid, "resourceVersion": "1"}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"namespace": ns, "name": claimName, "uid": claimUID}, "storageClassName": "fixture", "persistentVolumeReclaimPolicy": "Retain", "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": native}}, "status": deploymentplan.Object{"phase": "Bound"}}
	}
	oldPV := pv("original-pv", "00000044-1111-4111-8111-111111111111", "original-input", oldClaimUID, "1234")
	defs := managedMQTTConsumersFixture(t)
	for i := range defs {
		name := deploymentplan.String(deploymentplan.Map(defs[i].Object["metadata"])["name"])
		defs[i].Retain = name != "video-cloud-api-pki" && name != "video-cloud-mqttfoundation" && name != "video-cloud-shadowworker"
	}
	defs = append(defs, deploymentplan.Definition{Object: old, Retain: true, Phase: "configuration"}, deploymentplan.Definition{Object: fresh, Phase: "configuration"})
	setup := func(cfg *deploymentConfig, e *environmentCreateExecution, a *createNormalTailProgramArtifact) {
		workspace := writeDeploymentFixture(t, "dev", "lke")
		var err error
		*cfg, err = resolveDeploymentConfig(workspace, "dev", "")
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range rtkSecretCatalog() {
			if err := e.Store.write(filepath.Join("runtime", entry.ID), []byte(strings.Repeat("a", 32)), true); err != nil {
				t.Fatal(err)
			}
		}
		_, _, a.Program.Release = completeEnvironmentRenderFixture("dev")
		kube := map[string]any{"apiVersion": "v1", "kind": "Config", "current-context": "selected", "contexts": []any{map[string]any{"name": "selected", "context": map[string]any{"cluster": "selected", "user": "operator"}}}, "clusters": []any{map[string]any{"name": "selected", "cluster": map[string]any{"server": "https://selected.example:6443", "certificate-authority-data": base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}}}, "users": []any{map[string]any{"name": "operator", "user": map[string]any{"token": "private-isolated-token"}}}}
		raw, err := yaml.Marshal(kube)
		if err != nil {
			t.Fatal(err)
		}
		if err := e.Store.write("kube/kubeconfig.yaml", raw, true); err != nil {
			t.Fatal(err)
		}
		snapshot, err := readEnvironmentDeploymentSnapshot(*cfg, e.Store)
		if err != nil {
			t.Fatal("real input snapshot", err)
		}
		a.Program.ConfigurationSHA256 = environmentDeploymentSnapshotDigest(withCreateNormalTailHandoff(t.Context(), &createNormalTailHandoff{Execution: e}), *cfg, snapshot)
	}
	freeze := func(p *deploymentplan.Plan) {
		p.StorageSHA256 = deploymentplan.Digest([]environmentStorageBinding{{deploymentplan.ID(old), oldClaimUID, deploymentplan.ID(oldPV), deploymentplan.String(deploymentplan.Map(oldPV["metadata"])["uid"]), deploymentplan.Digest(oldPV["spec"])}})
	}
	// Keep the new claim in the configuration phase so its genuine write precedes
	// the existing PostgreSQL Wait/capture interruption.
	f := newNormalPostgresAccessFixtureWithFrozenInputs(t, setup, freeze, defs)
	f.b.objects[deploymentplan.ID(oldPV)] = oldPV
	_, j, err := readCreateNormalTailWorkloadOriginal(f.store, f.h.Execution, f.workload)
	if err != nil {
		t.Fatal(err)
	}
	id := deploymentplan.ID(fresh)
	uid := j.Owners[id]
	if uid == "" {
		t.Fatal("new claim was not genuinely written before interruption")
	}
	native := f.b.objects[id]
	deploymentplan.Map(native["spec"])["volumeName"] = "new-pv"
	native["status"] = deploymentplan.Object{"phase": "Bound"}
	newPV := pv("new-pv", "00000055-1111-4111-8111-111111111111", "new-planned", uid, "5678")
	f.b.objects[deploymentplan.ID(newPV)] = newPV
	ledger, err := readEnvironmentCreateLedger(f.h.Execution)
	if err != nil {
		t.Fatal(err)
	}
	ledger.Kubernetes[id] = uid
	ledger.Volumes = append(ledger.Volumes, environmentCreateVolumeOwner{NativeID: "5678", PVName: "new-pv", PVUID: "00000055-1111-4111-8111-111111111111", PVCName: "new-planned", PVCUID: uid, Namespace: ns})
	if err := saveEnvironmentCreateLedger(t.Context(), f.h.Execution, ledger); err != nil {
		t.Fatal(err)
	}
	if err := f.run(t.Context()); err != nil {
		t.Fatal(err)
	}
	c := &compiledEnvironmentDeployment{Store: f.store, Plan: f.workload, Configuration: f.cfg, Release: f.artifact.Program.Release, Bindings: f.artifact.Program.Bindings}
	c, err = prepareCreateNormalTailWorkloadForwardWithInventory(t.Context(), f.cfg, f.h, c, func(context.Context, string) ([]deploymentplan.Object, error) {
		var a []deploymentplan.Object
		for _, o := range f.b.objects {
			a = append(a, deploymentplan.Clone(o))
		}
		return a, nil
	})
	if err != nil {
		t.Fatal("real forward prepare", err)
	}
	snapshot, err := readEnvironmentDeploymentSnapshot(f.cfg, f.store)
	if err != nil {
		t.Fatal(err)
	}
	c.Backend, err = newEnvironmentKubernetesBackend(snapshot.Kubeconfig, "dev")
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	objectsPath := filepath.Join(directory, "objects.json")
	write := func() {
		t.Helper()
		raw, err := json.Marshal(f.b.objects)
		if err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, objectsPath, string(raw))
	}
	kubectl := filepath.Join(directory, "kubectl")
	writeTestFile(t, kubectl, `#!/usr/bin/env python3
import os,json,sys
args=sys.argv[1:];i=args.index('get');kind=args[i+1];objects=json.load(open(os.environ['RTK_FORWARD_NATIVE_OBJECTS']))
ns=args[args.index('--namespace')+1] if '--namespace' in args else ''
if kind=='namespace':
 names=sorted(set(o.get('metadata',{}).get('namespace','') for o in objects.values())-{''});result={'items':[{'apiVersion':'v1','kind':'Namespace','metadata':{'name':n,'uid':'namespace-owner'}} for n in names]}
elif kind.startswith('deployment,'):result={'items':[o for o in objects.values() if o.get('metadata',{}).get('namespace')==ns]}
elif ',' in kind or kind=='validatingwebhookconfiguration':result={'items':[]}
else:result=objects.get(kind+'/'+ns+'/'+args[i+2])
if result is not None:print(json.dumps(result))
`)
	if err := os.Chmod(kubectl, 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_FORWARD_NATIVE_OBJECTS", objectsPath)
	write()
	return f, c, write
}

func TestCreateNormalTailForwardStorageFenceGenuineInputsAndRetry(t *testing.T) {
	f, c, write := newForwardNativeFenceFixture(t)
	ctx := withCreateNormalTailHandoff(t.Context(), f.h)
	originalSHA, forwardSHA := f.workload.SHA256(), c.Plan.SHA256()
	journalPath := filepath.Join(f.store.Root, "deployment/rollback", originalSHA, "journal.json")
	before, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal(err)
	}
	observed, err := c.FenceInventory(ctx, c.Plan)
	if err != nil {
		t.Fatal("actual full forward fence", err)
	}
	if err := c.Backend.CheckStorageBindingsFromInventory(ctx, c.Plan, observed); err == nil {
		t.Fatal("fixture did not reproduce original expanded storage-cohort failure")
	}
	count := 0
	stop := errors.New("known forward admission interruption")
	phase := func(ctx context.Context, p *deploymentplan.Plan) (map[string]deploymentplan.Object, error) {
		write()
		count++
		return c.FenceInventory(ctx, p)
	}
	fence := func(ctx context.Context, p *deploymentplan.Plan) error { _, err := phase(ctx, p); return err }
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.h.Execution.Check(ctx) }
	if err := executeCreateNormalTailForwardWorkloadBackend(ctx, f.cfg, f.store, f.h, c, f.b, func(context.Context, *deploymentplan.Plan) error { return stop }, fence, check, io.Discard, func(context.Context) error { return nil }, phase); err == nil {
		t.Fatal("missing original child admission interruption")
	}
	if err := executeCreateNormalTailForwardWorkloadBackend(ctx, f.cfg, f.store, f.h, c, f.b, check, fence, check, io.Discard, func(context.Context) error { return nil }, phase); err != nil {
		t.Fatal("real phase-fenced standard forward retry", err)
	}
	if count < 3 || f.b.writes["StatefulSet/video-cloud-dev-platform/postgresql"] != 1 || f.b.writes["PersistentVolumeClaim/video-cloud-dev-platform/new-planned"] != 1 {
		t.Fatal("phase fence was bypassed or known native writes repeated")
	}
	after, err := os.ReadFile(journalPath)
	if err != nil || !bytes.Equal(before, after) || f.workload.SHA256() != originalSHA || c.Plan.SHA256() != forwardSHA {
		t.Fatal("immutable original/forward evidence changed")
	}
}

func TestCreateNormalTailForwardStorageFenceRejectsChangedInputsAndAllocations(t *testing.T) {
	for _, mode := range []string{"claim-owner", "claim-ref", "csi-id", "original-volume", "missing-ledger", "foreign-descriptor", "config", "release", "bindings"} {
		t.Run(mode, func(t *testing.T) {
			f, c, write := newForwardNativeFenceFixture(t)
			ctx := withCreateNormalTailHandoff(t.Context(), f.h)
			if _, err := c.FenceInventory(ctx, c.Plan); err != nil {
				t.Fatal("negative case baseline did not pass full fence", err)
			}
			switch mode {
			case "claim-owner":
				deploymentplan.Map(f.b.objects["PersistentVolumeClaim/video-cloud-dev-platform/new-planned"]["metadata"])["uid"] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			case "claim-ref":
				deploymentplan.Map(deploymentplan.Map(f.b.objects["PersistentVolume//new-pv"]["spec"])["claimRef"])["uid"] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			case "csi-id":
				deploymentplan.Map(deploymentplan.Map(f.b.objects["PersistentVolume//new-pv"]["spec"])["csi"])["volumeHandle"] = "9999"
			case "original-volume":
				deploymentplan.Map(f.b.objects["PersistentVolume//original-pv"]["spec"])["storageClassName"] = "foreign"
			case "missing-ledger":
				ledger, err := readEnvironmentCreateLedger(f.h.Execution)
				if err != nil {
					t.Fatal(err)
				}
				ledger.Volumes = nil
				ledger.Volumes = []environmentCreateVolumeOwner{}
				if err := saveEnvironmentCreateLedger(ctx, f.h.Execution, ledger); err != nil {
					t.Fatal(err)
				}
			case "foreign-descriptor":
				path := createNormalTailWorkloadForwardPath(f.h.Execution)
				raw, err := os.ReadFile(path)
				if err != nil {
					t.Fatal(err)
				}
				var d createNormalTailWorkloadForward
				if json.Unmarshal(raw, &d) != nil {
					t.Fatal("decode")
				}
				d.OwnerSHA256 = strings.Repeat("f", 64)
				rel, _ := filepath.Rel(f.store.Root, path)
				if err := f.store.write(rel, mustProviderRecoveryJSON(&d), true); err != nil {
					t.Fatal(err)
				}
			case "config":
				appendFile(t, filepath.Join(f.cfg.EnvironmentRoot, "environment.env"), "UNREVIEWED_SETTING=true\n")
			case "release":
				c.Release.WorkspaceRevision = strings.Repeat("f", 40)
			case "bindings":
				c.Bindings.Owners = map[string]string{"Secret/video-cloud-dev-platform/foreign": "replacement"}
			}
			write()
			before := c.Plan.SHA256()
			if _, err := c.FenceInventory(ctx, c.Plan); err == nil {
				t.Fatal("full fence accepted changed " + mode)
			}
			if c.Plan.SHA256() != before {
				t.Fatal("fence rewrote plan")
			}
		})
	}
}
