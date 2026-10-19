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

type recordedStorageWaitFixture struct {
	cfg                       deploymentConfig
	store                     secretStore
	e                         *environmentCreateExecution
	b                         environmentCreateKubernetesBackend
	directory                 string
	claim, volume, controller deploymentplan.Object
}

func newRecordedStorageWaitFixture(t *testing.T) *recordedStorageWaitFixture {
	t.Helper()
	cfg, store, operator, artifact := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(t.Context(), cfg, artifact, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	kubeconfig := []byte("isolated-frozen-kubernetes-identity")
	if err := store.write("kube/kubeconfig.yaml", kubeconfig, false); err != nil {
		t.Fatal(err)
	}
	if err := recordEnvironmentCreateProviderOwner(t.Context(), e, environmentCreateProviderReceipt{Key: "lke", Kind: "lke", NativeID: "201"}); err != nil {
		t.Fatal(err)
	}
	f := &recordedStorageWaitFixture{cfg: cfg, store: store, e: e, directory: t.TempDir()}
	f.claim = deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": "data", "namespace": "video-cloud-dev-platform", "uid": "claim-original", "resourceVersion": "1"}, "spec": deploymentplan.Object{"volumeName": "pv-original", "storageClassName": "fast", "accessModes": []any{"ReadWriteOnce"}, "resources": deploymentplan.Object{"requests": deploymentplan.Object{"storage": "10Gi"}}}, "status": deploymentplan.Object{"phase": "Bound"}}
	deploymentplan.Map(f.claim["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-dev", "app.kubernetes.io/managed-by": "rtk-cloud"}
	f.volume = deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": "pv-original", "uid": "volume-original", "resourceVersion": "1"}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"name": "data", "namespace": "video-cloud-dev-platform", "uid": "claim-original"}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": "101-pvc-original"}}, "status": deploymentplan.Object{"phase": "Bound"}}
	f.controller = plannedEnvelopeStatefulSet("postgresql", 1)
	deploymentplan.Map(f.controller["metadata"])["uid"] = "controller-original"
	deploymentplan.Map(f.controller["metadata"])["resourceVersion"] = "1"
	f.publish(t, "claim.json", f.claim)
	f.publish(t, "volume.json", f.volume)
	f.publish(t, "controller.json", f.controller)
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(f.directory, "kubectl"), "#!"+python+"\n"+`import json,os,sys
from pathlib import Path
root=Path(os.environ['RTK_RECORDED_STORAGE_ROOT'])
args=sys.argv[1:]
with (root/'calls').open('a') as out: out.write(' '.join(args)+'\n')
if 'rollout' in args or 'wait' in args:
 sys.exit(1 if (root/'fail-readiness').exists() else 0)
if 'get' not in args: sys.exit(2)
i=args.index('get'); kind=args[i+1]
if kind=='persistentvolumes':
 print(json.dumps({'apiVersion':'v1','kind':'List','metadata':{},'items':[json.loads((root/'volume.json').read_text())]}))
elif kind=='PersistentVolumeClaim': print((root/'claim.json').read_text())
elif kind=='PersistentVolume': print((root/'volume.json').read_text())
elif kind=='StatefulSet': print((root/'controller.json').read_text())
elif kind=='Pod': print((root/'pod.json').read_text())
else: sys.exit(2)
`)
	if err := os.Chmod(filepath.Join(f.directory, "kubectl"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", f.directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_RECORDED_STORAGE_ROOT", f.directory)
	f.b = environmentCreateKubernetesBackend{environmentKubernetesBackend: environmentKubernetesBackend{Store: store, KubeconfigContent: kubeconfig, RolloutTimeout: time.Second}, execution: e, cfg: cfg, store: store}
	ledger, err := readEnvironmentCreateLedger(e)
	if err != nil {
		t.Fatal(err)
	}
	ledger.Volumes = []environmentCreateVolumeOwner{{NativeID: "101", PVName: "pv-original", PVUID: "volume-original", PVCName: "data", PVCUID: "claim-original", Namespace: "video-cloud-dev-platform"}}
	if err := saveEnvironmentCreateLedger(t.Context(), e, ledger); err != nil {
		t.Fatal(err)
	}
	if err := saveEnvironmentCreateRemovalVolumeProof(t.Context(), cfg, &compiledEnvironmentDeployment{Store: store, Backend: f.b.environmentKubernetesBackend}, ledger, e); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *recordedStorageWaitFixture) publish(t *testing.T, name string, object deploymentplan.Object) {
	t.Helper()
	raw, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(f.directory, name), string(raw))
}

func (f *recordedStorageWaitFixture) calls(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(f.directory, "calls"))
	if os.IsNotExist(err) {
		return ""
	}
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestCreateBackendStorageWaitCapturesOnlyOriginalAllocatingActions(t *testing.T) {
	for _, mode := range []string{"claim-retain", "stateful-retain", "claim-create", "claim-update", "stateful-create", "stateful-update", "fresh-ondelete-create", "ondelete-retain", "ondelete-replaced", "retained-readiness-failed"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecordedStorageWaitFixture(t)
			kind, action := "PersistentVolumeClaim", "retain"
			if strings.Contains(mode, "stateful") || strings.Contains(mode, "ondelete") {
				kind = "StatefulSet"
			}
			if strings.Contains(mode, "create") {
				action = "create"
			}
			if strings.Contains(mode, "update") {
				action = "update"
			}
			object := f.claim
			if kind == "StatefulSet" {
				object = f.controller
			}
			want := deploymentplan.Projection(object)
			r := deploymentplan.Resource{ID: deploymentplan.ID(want), Action: action, UID: deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"]), Desired: want}
			if strings.Contains(mode, "ondelete") {
				want = plannedEnvelopeStatefulSet("openbao", 1)
				deploymentplan.Map(want["metadata"])["namespace"] = "video-cloud-dev-secrets"
				deploymentplan.Map(want["spec"])["updateStrategy"] = deploymentplan.Object{"type": "OnDelete"}
				controller := deploymentplan.Clone(want)
				metadata := deploymentplan.Map(controller["metadata"])
				metadata["uid"], metadata["resourceVersion"], metadata["generation"] = "controller-original", "1", float64(1)
				controller["status"] = deploymentplan.Object{"observedGeneration": float64(1), "updateRevision": "original-revision"}
				pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "openbao-0", "namespace": metadata["namespace"], "uid": "pod-original", "resourceVersion": "1", "labels": deploymentplan.Object{"controller-revision-hash": "original-revision"}, "ownerReferences": []any{deploymentplan.Object{"controller": true, "kind": "StatefulSet", "name": "openbao", "uid": "controller-original"}}}, "spec": deploymentplan.Clone(environmentPKIPod(want)), "status": deploymentplan.Object{"phase": "Running", "conditions": []any{deploymentplan.Object{"type": "Ready", "status": "True"}}}}
				f.publish(t, "controller.json", controller)
				f.publish(t, "pod.json", pod)
				r.ID, r.Desired, r.UID = deploymentplan.ID(want), want, "controller-original"
				if mode == "ondelete-replaced" {
					metadata["uid"] = "replaced-controller"
					f.publish(t, "controller.json", controller)
				}
				if action == "create" {
					f.b.stagePlanSHA256 = strings.Repeat("b", 64)
					r.UID = ""
					j := deploymentplan.Journal{PlanSHA256: f.b.stagePlanSHA256, Owners: map[string]string{r.ID: "controller-original"}}
					if err := f.store.write(filepath.Join("deployment/rollback", f.b.stagePlanSHA256, "journal.json"), mustProviderRecoveryJSON(j), false); err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "retained-readiness-failed" {
				writeTestFile(t, filepath.Join(f.directory, "fail-readiness"), "fail")
			}
			beforePlan, beforeLedger := deploymentplan.Digest(r), readTestFile(t, filepath.Join(f.store.Root, environmentCreateLedgerPath(f.e)))
			err := f.b.Wait(t.Context(), r)
			bad := mode == "ondelete-replaced" || mode == "retained-readiness-failed"
			if (err != nil) != bad {
				t.Fatal("unexpected native readiness result", err)
			}
			calls := f.calls(t)
			captureCount := strings.Count(calls, "get persistentvolumes -o json")
			if action == "retain" {
				if captureCount != 0 || readTestFile(t, filepath.Join(f.store.Root, environmentCreateLedgerPath(f.e))) != beforeLedger {
					t.Fatal("retained readiness recaptured or changed durable ownership", calls)
				}
				if !bad && err == nil {
					if err := f.b.Wait(t.Context(), r); err != nil {
						t.Fatal(err)
					}
					if strings.Contains(f.calls(t), "get persistentvolumes") {
						t.Fatal("repeated retained wait recaptured allocations")
					}
				}
			} else {
				ledger, err := readEnvironmentCreateLedger(f.e)
				proof, known, proofErr := readDeploymentRemovalVolumes(f.cfg)
				if captureCount != 1 || err != nil || proofErr != nil || len(ledger.Volumes) != 1 || ledger.Volumes[0].PVCUID != "claim-original" || ledger.Volumes[0].PVUID != "volume-original" || !known || len(proof.VolumeIDs) != 1 || proof.VolumeIDs[0] != "101" {
					t.Fatal("original allocating action lost capture/proof", captureCount, err, proofErr)
				}
			}
			if beforePlan != deploymentplan.Digest(r) || !strings.Contains(calls, "--timeout=") && !strings.Contains(mode, "ondelete") {
				t.Fatal("native readiness skipped or original resource mutated", calls)
			}
		})
	}
}

type recordedStorageResumeBackend struct {
	*normalTailE2EBackend
	native environmentCreateKubernetesBackend
	reads  int
}

func (b *recordedStorageResumeBackend) Read(ctx context.Context, id string) (deploymentplan.Object, error) {
	b.reads++
	return b.native.Read(ctx, id)
}
func (b *recordedStorageResumeBackend) Wait(ctx context.Context, r deploymentplan.Resource) error {
	return b.native.Wait(ctx, r)
}

func TestCreateBackendRecordedStorageResumeKeepsNativeProofWithoutRecapture(t *testing.T) {
	for _, mode := range []string{"resume", "replaced-claim", "replaced-controller", "rebound-volume", "changed-csi", "readiness-failed"} {
		t.Run(mode, func(t *testing.T) {
			f := newRecordedStorageWaitFixture(t)
			defs := []deploymentplan.Definition{{Object: deploymentplan.Projection(f.claim), Phase: "configuration", Retain: true}, {Object: deploymentplan.Projection(f.controller), Phase: "workload", Retain: true, Dependencies: []string{deploymentplan.ID(f.claim)}}}
			p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: f.e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(f.cfg), ReleaseSHA256: deploymentplan.Digest("frozen-release"), BindingsSHA256: deploymentplan.Digest("original-bindings"), CreateParentSHA256: f.e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest("verified-parent-handoff")}, defs, []deploymentplan.Object{f.claim, f.controller})
			if err != nil {
				t.Fatal(err)
			}
			bindings, err := f.b.storageBindings(t.Context(), p)
			if err != nil {
				t.Fatal(err)
			}
			p.StorageSHA256 = deploymentplan.Digest(bindings)
			b := &recordedStorageResumeBackend{normalTailE2EBackend: &normalTailE2EBackend{e: f.e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}}, native: f.b}
			acceptCalls, fences := 0, 0
			check := func(ctx context.Context, p *deploymentplan.Plan) error {
				fences++
				if err := f.e.Check(ctx); err != nil {
					return err
				}
				return f.b.CheckStorageBindings(ctx, p)
			}
			accept := func(context.Context, *deploymentplan.Plan) error {
				acceptCalls++
				if acceptCalls == 1 {
					return errors.New("isolated interruption after genuine ready owners")
				}
				return nil
			}
			run := func() error {
				return p.ExecuteResumable(t.Context(), b, p.SHA256(), f.store.Root, check, check, accept)
			}
			if err := run(); err == nil {
				t.Fatal("missing controlled interruption")
			}
			j, exists, err := p.ReadExecution(f.store.Root)
			if err != nil || !exists || j.Accepted || len(j.Completed) != 2 || len(j.Owners) != 2 || j.InFlight != "" {
				t.Fatal("not a genuine known completed prefix", err)
			}
			beforePlan, beforeLedger := p.SHA256(), readTestFile(t, filepath.Join(f.store.Root, environmentCreateLedgerPath(f.e)))
			beforeCalls := len(f.calls(t))
			beforeFences := fences
			switch mode {
			case "replaced-claim":
				deploymentplan.Map(f.claim["metadata"])["uid"] = "foreign-claim"
				f.publish(t, "claim.json", f.claim)
			case "replaced-controller":
				deploymentplan.Map(f.controller["metadata"])["uid"] = "foreign-controller"
				f.publish(t, "controller.json", f.controller)
			case "rebound-volume":
				deploymentplan.Map(deploymentplan.Map(f.volume["spec"])["claimRef"])["uid"] = "foreign-claim"
				f.publish(t, "volume.json", f.volume)
			case "changed-csi":
				deploymentplan.Map(deploymentplan.Map(f.volume["spec"])["csi"])["volumeHandle"] = "foreign-data"
				f.publish(t, "volume.json", f.volume)
			case "readiness-failed":
				writeTestFile(t, filepath.Join(f.directory, "fail-readiness"), "fail")
			}
			err = run()
			j, exists, readErr := p.ReadExecution(f.store.Root)
			if (err == nil) != (mode == "resume") || readErr != nil || !exists || j.Accepted != (mode == "resume") || len(b.writes) != 0 || p.SHA256() != beforePlan || readTestFile(t, filepath.Join(f.store.Root, environmentCreateLedgerPath(f.e))) != beforeLedger || strings.Contains(f.calls(t), "get persistentvolumes -o json") {
				t.Fatal("resume changed authority, skipped proof, recaptured or wrote", err, readErr)
			}
			if mode == "resume" {
				calls := f.calls(t)[beforeCalls:]
				if strings.Count(calls, "rollout status StatefulSet/postgresql") != 2 || strings.Count(calls, "wait --for=jsonpath={.status.phase}=Bound PersistentVolumeClaim/data") < 2 || strings.Count(calls, "get PersistentVolume pv-original") < 2 || fences <= beforeFences || b.reads == 0 {
					t.Fatal("ordinary recorded-owner admission/locked checks or fresh native storage/readiness proof skipped", calls)
				}
			}
		})
	}
}
