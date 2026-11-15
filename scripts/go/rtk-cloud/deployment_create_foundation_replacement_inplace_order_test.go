package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// These are actual parent/child stage journals and the native unseal producer;
// Kubernetes and OpenBao effects remain confined to the historical test fake.
func managedFoundationReplacementOrderFixture(t *testing.T) *acceptedCoreExecutionContract {
	t.Helper()
	f, partial := newHistoricalInterruptedTransportHosts(t)
	if err := saveEnvironmentCreateStageInput(t.Context(), f.store, f.e, "transport-managed-hosts", partial.Original); err != nil {
		t.Fatal("original managed input", err)
	}
	id := "StatefulSet/" + f.e.Journal.Stack + "-secrets/openbao"
	current := f.native.objects[id]
	if current == nil {
		t.Fatal("original StatefulSet missing")
	}
	beforeUID := deploymentplan.String(deploymentplan.Map(current["metadata"])["uid"])
	desired := deploymentplan.Clone(current)
	metadata := deploymentplan.Map(desired["metadata"])
	metadata["annotations"] = deploymentplan.Object{"fixture.realtek.com/managed-forward": "selected"}
	stage := managedAllocationForwardStage(partial.Original)
	forward := f.acceptedHistoricalStage(t, stage, []deploymentplan.Definition{{Phase: "workload", Object: desired}})
	if len(forward.Resources) != 1 || forward.Resources[0].ID != id || forward.Resources[0].Action != "update" || forward.Resources[0].UID != beforeUID {
		t.Fatal("actual managed forward did not update original StatefulSet owner")
	}
	_, journal, err := acceptedCreateStage(f.store, f.e, "kubernetes/"+stage)
	if err != nil || journal.Owners[id] != beforeUID {
		t.Fatal("managed forward child was not accepted", err)
	}
	writes, err := completedManagedAllocationWrites(f.store, f.e)
	if err != nil || len(writes) != 1 || writes[0].ID != id || writes[0].UID != beforeUID {
		t.Fatal("native managed forward provenance unavailable", err)
	}
	pod := historicalManagedOpenBaoPod(t, f, true)
	installHistoricalManagedOpenBaoProtocol(t, f)
	if err := unsealEnvironmentCreateOpenBaoRestart(t.Context(), f.cfg, f.store, f.runtime, pod); err != nil {
		t.Fatal("original-share native replacement unseal", err)
	}
	return f
}

func TestManagedReplacementNeverSkipsLaterInPlaceHistory(t *testing.T) {
	f := managedFoundationReplacementOrderFixture(t)
	pod, set, err := originalCreateOpenBaoFoundationIdentity(t.Context(), f.cfg, f.store, f.e, f.backend, "kubernetes/foundation")
	if err != nil || pod == "" || set == "" {
		t.Fatal("genuine managed replacement was rejected", err)
	}
	for _, mode := range []string{"pending", "foreign"} {
		t.Run(mode, func(t *testing.T) {
			original := mustProviderRecoveryJSON(f.e.Journal)
			t.Cleanup(func() {
				if err := json.Unmarshal(original, &f.e.Journal); err != nil {
					t.Fatal(err)
				}
				if err := f.e.save(); err != nil {
					t.Fatal(err)
				}
			})
			key := "openbao-host-inplace-" + strings.Repeat("f", 64)
			if err := f.e.BeginEntry(key, "openbao-host-inplace", mustProviderRecoveryJSON(map[string]string{"scenario": "isolated-" + mode}), time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if mode == "foreign" {
				if err := f.e.RecordReceipt(key, mustProviderRecoveryJSON(map[string]string{"qualification_sha256": strings.Repeat("e", 64)})); err != nil {
					t.Fatal(err)
				}
				if err := f.e.CompleteEntry(key, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := originalCreateOpenBaoFoundationIdentity(t.Context(), f.cfg, f.store, f.e, f.backend, "kubernetes/foundation"); err == nil {
				t.Fatal("replacement skipped subsequent " + mode + " in-place history")
			}
		})
	}
	// The completed-child journal remains the exact producer outcome.
	if err := f.e.Check(context.Background()); err != nil {
		t.Fatal("original parent fence", err)
	}
}

// The original managed child, in-place image producer, replacement and unseal
// are all recorded by their native executors under a single isolated parent.
func newManagedWriteInPlaceFixture(t *testing.T, beforeHandoff func(unifiedOpenBaoParentFixture)) unifiedOpenBaoParentFixture {
	t.Helper()
	image := environmentReleaseImage{Reference: "ghcr.io/example/api@sha256:" + strings.Repeat("a", 64)}
	f := newUnifiedOpenBaoParentFixtureWithHook(t, image, func(f unifiedOpenBaoParentFixture, original *deploymentplan.Plan) {
		// Metadata-only writes do not change a StatefulSet ControllerRevision.
		// Preserve native status instead of simulating a false Pod replacement.
		path := filepath.Join(f.nativeDir, "unified-kubectl.py")
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		before := `        if kind == 'StatefulSet' and name == 'openbao':
            meta['generation'] = 2
            obj['status'] = {'observedGeneration': 1 if os.path.exists(os.path.join(root,'fail-template')) else 2, 'updateRevision':'new-revision'}`
		after := `        if kind == 'StatefulSet' and name == 'openbao':
            if old is not None and old.get('spec',{}).get('template') == obj.get('spec',{}).get('template'):
                meta['generation'] = old['metadata']['generation']
                obj['status'] = old['status']
            else:
                meta['generation'] = 2
                obj['status'] = {'observedGeneration': 1 if os.path.exists(os.path.join(root,'fail-template')) else 2, 'updateRevision':'new-revision'}`
		if strings.Count(string(raw), before) != 1 {
			t.Fatal("isolated StatefulSet revision hook changed")
		}
		script := strings.Replace(string(raw), before, after, 1)
		marker := "            elif kind == 'validatingwebhookconfiguration':"
		if strings.Count(script, marker) != 1 {
			t.Fatal("isolated Kubernetes inventory hook changed")
		}
		script = strings.Replace(script, marker, "            elif kind == 'persistentvolumes': items = [o for o in objs if o.get('kind') == 'PersistentVolume']\n"+marker, 1)
		if err := os.WriteFile(path, []byte(script), 0600); err != nil {
			t.Fatal(err)
		}
		// Keep the selected provider owner and canonical kubeconfig from this
		// isolated parent, then seed the controller-created data/audit bindings.
		if err := f.store.write("kube/kubeconfig.yaml", f.native.KubeconfigContent, false); err != nil {
			t.Fatal("isolated canonical kubeconfig", err)
		}
		var lke environmentCreateProviderReceipt
		for _, entry := range f.execution.Journal.Entries {
			if entry.Key == "lke" && entry.Phase == "provider" && entry.State == "completed" {
				if err := json.Unmarshal(entry.Receipt, &lke); err != nil {
					t.Fatal(err)
				}
			}
		}
		if lke.Key != "lke" || lke.Kind != "lke" || lke.NativeID == "" {
			t.Fatal("original LKE receipt missing")
		}
		if err := recordEnvironmentCreateProviderOwner(t.Context(), f.execution, lke); err != nil {
			t.Fatal("original LKE ledger", err)
		}
		ns := f.execution.Journal.Stack + "-secrets"
		var pvPaths []string
		for i, name := range []string{"data", "audit"} {
			claimName := name + "-openbao-0"
			claimUID := fmt.Sprintf("11111111-1111-4111-8111-%012d", i+1)
			pvUID := fmt.Sprintf("22222222-2222-4222-8222-%012d", i+1)
			pvName := "isolated-openbao-" + name
			pvc := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": claimName, "namespace": ns, "uid": claimUID, "resourceVersion": "1"}, "spec": deploymentplan.Object{"volumeName": pvName}, "status": deploymentplan.Object{"phase": "Bound"}}
			pv := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": pvName, "uid": pvUID, "resourceVersion": "1"}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"name": claimName, "namespace": ns, "uid": claimUID}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": fmt.Sprintf("%d-pvc-%s", 400+i, claimUID)}}, "status": deploymentplan.Object{"phase": "Bound"}}
			for _, obj := range []deploymentplan.Object{pvc, pv} {
				file := filepath.Join(f.nativeDir, strings.ReplaceAll(deploymentplan.ID(obj), "/", "_")+".json")
				if err := os.WriteFile(file, mustProviderRecoveryJSON(obj), 0600); err != nil {
					t.Fatal(err)
				}
				if obj["kind"] == "PersistentVolume" {
					pvPaths = append(pvPaths, file)
				}
			}
		}
		if err := saveEnvironmentCreateStageInput(t.Context(), f.store, f.execution, "transport-managed-hosts", original); err != nil {
			t.Fatal(err)
		}
		id := "StatefulSet/" + f.execution.Journal.Stack + "-secrets/openbao"
		current, err := f.native.Read(t.Context(), id)
		if err != nil || current == nil {
			t.Fatal("original StatefulSet", err)
		}
		desired := deploymentplan.Clone(current)
		deploymentplan.Map(desired["metadata"])["annotations"] = deploymentplan.Object{"fixture.realtek.com/managed-forward": "selected"}
		stage := managedAllocationForwardStage(original)
		inputs := original.Inputs
		defs := []deploymentplan.Definition{{Object: desired, Phase: "workload"}}
		inputs.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, defs)
		plan, err := deploymentplan.Compile(inputs, defs, []deploymentplan.Object{current})
		if err != nil || len(plan.Resources) != 1 || plan.Resources[0].Action != "update" {
			t.Fatal("managed child compilation", err)
		}
		backend := f.fault
		qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
			return p.AdmitResumable(ctx, backend, f.store.Root)
		}
		fence := func(ctx context.Context, p *deploymentplan.Plan) error {
			return backend.CheckParentExecution(ctx, p, f.store.Root)
		}
		accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, backend) }
		if err := executeEnvironmentCreateStagePlan(t.Context(), f.store, f.execution, stage, plan, backend, qualify, fence, accept, func(context.Context) error { return nil }, io.Discard); err != nil {
			t.Fatal("native managed child", err)
		}
		ledger, err := readEnvironmentCreateLedger(f.execution)
		if err != nil || ledger.Provider["lke"].NativeID != lke.NativeID || len(ledger.Volumes) != 2 {
			t.Fatal("original provider/PV ledger", err)
		}
		originalPV, err := os.ReadFile(pvPaths[0])
		if err != nil {
			t.Fatal(err)
		}
		var changed deploymentplan.Object
		if err := json.Unmarshal(originalPV, &changed); err != nil {
			t.Fatal(err)
		}
		deploymentplan.Map(deploymentplan.Map(changed["spec"])["claimRef"])["uid"] = "foreign-claim-uid"
		if err := os.WriteFile(pvPaths[0], mustProviderRecoveryJSON(changed), 0600); err != nil {
			t.Fatal(err)
		}
		if err := captureEnvironmentCreateVolumeOwners(t.Context(), f.cfg, f.native, f.execution); err == nil {
			t.Fatal("foreign PV/PVC owner was accepted")
		}
		if err := os.WriteFile(pvPaths[0], originalPV, 0600); err != nil {
			t.Fatal(err)
		}
		if beforeHandoff != nil {
			beforeHandoff(f)
		}
	}, true)
	return f
}

func replaceAndUnsealOpenBaoFixture(t *testing.T, f unifiedOpenBaoParentFixture) string {
	t.Helper()
	ns := f.execution.Journal.Stack + "-secrets"
	set, err := f.native.Read(t.Context(), "StatefulSet/"+ns+"/openbao")
	if err != nil || set == nil {
		t.Fatal("updated StatefulSet", err)
	}
	pod, err := f.native.Read(t.Context(), "Pod/"+ns+"/openbao-0")
	if err != nil || pod == nil {
		t.Fatal("original Pod", err)
	}
	meta := deploymentplan.Map(pod["metadata"])
	meta["uid"] = "99999999-9999-4999-8999-999999999999"
	meta["labels"] = deploymentplan.Object{"controller-revision-hash": "replacement-revision"}
	deploymentplan.Map(set["status"])["updateRevision"] = "replacement-revision"
	pod["spec"] = deploymentplan.Clone(environmentPKIPod(set))
	podSpec := deploymentplan.Map(pod["spec"])
	volumes := certIssuerObjectList(podSpec["volumes"])
	claims := certIssuerObjectList(deploymentplan.Map(set["spec"])["volumeClaimTemplates"])
	if len(claims) == 0 {
		t.Fatal("fixture must represent StatefulSet PVC claim templates")
	}
	for _, claim := range claims {
		name := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(claim)["metadata"])["name"])
		volumes = append(volumes, environmentCreateServiceVolume(name, "persistentVolumeClaim", name+"-openbao-0"))
	}
	podSpec["volumes"] = volumes
	status := deploymentplan.Map(pod["status"])
	managedNamedField(status["containerStatuses"], "openbao")["containerID"] = "replacement-provider"
	managedNamedField(status["containerStatuses"], "openbao-pki")["containerID"] = "replacement-host"
	managedNamedField(status["containerStatuses"], "openbao-pki")["imageID"] = managedNamedField(environmentPKIPod(set)["containers"], "openbao-pki")["image"]
	for _, object := range []deploymentplan.Object{set, pod} {
		path := filepath.Join(f.nativeDir, strings.ReplaceAll(deploymentplan.ID(object), "/", "_")+".json")
		if err := os.WriteFile(path, mustProviderRecoveryJSON(object), 0600); err != nil {
			t.Fatal(err)
		}
	}
	// A private test wrapper emulates an initialized sealed OpenBao and checks
	// that the real unseal producer supplies its original share on stdin.
	parent := os.Getenv("RTK_CLOUD_KUBECTL")
	dir := t.TempDir()
	script := `#!/usr/bin/env python3
import json,os,sys
args=sys.argv[1:]; joined=' '.join(args); marker=os.environ['TEST_UNSEAL_MARKER']
if 'bao status -format=json' in joined:
 print(json.dumps({'initialized': True, 'sealed': not os.path.exists(marker)}))
elif 'bao write -format=json sys/unseal -' in joined:
 if json.load(sys.stdin).get('key') != 'aXNvbGF0ZWQtdW5zZWFsLWtleQ==': sys.exit(1)
 open(marker,'w').close(); print('{}')
else: os.execv(os.environ['TEST_UNSEAL_PARENT'],[os.environ['TEST_UNSEAL_PARENT']]+args)
`
	wrapper := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TEST_UNSEAL_PARENT", parent)
	t.Setenv("TEST_UNSEAL_MARKER", filepath.Join(dir, "unsealed"))
	t.Setenv("RTK_CLOUD_KUBECTL", wrapper)
	runtime := environmentCreateBootstrapRuntime{Backend: f.native, Execution: f.execution}
	if err := unsealEnvironmentCreateOpenBaoRestart(t.Context(), f.cfg, f.store, runtime, pod); err != nil {
		t.Fatal("native replacement unseal", err)
	}
	return deploymentplan.String(meta["uid"])
}

func TestManagedWriteThenInPlaceThenReplacement(t *testing.T) {
	f := newManagedWriteInPlaceFixture(t, nil)
	wantPod := replaceAndUnsealOpenBaoFixture(t, f)
	podUID, setUID, err := originalCreateOpenBaoFoundationIdentity(t.Context(), f.cfg, f.store, f.execution, f.native, "kubernetes/original-openbao")
	if err != nil || podUID != wantPod || setUID != f.owner.StatefulSetUID {
		t.Fatal("ordered native successor", err)
	}
}

func TestManagedReplacementThenInPlaceRetainsOriginalUnseal(t *testing.T) {
	var wantPod string
	f := newManagedWriteInPlaceFixture(t, func(f unifiedOpenBaoParentFixture) {
		wantPod = replaceAndUnsealOpenBaoFixture(t, f)
	})
	before := deploymentplan.Digest(f.execution.Journal)
	podUID, setUID, err := originalCreateOpenBaoFoundationIdentity(t.Context(), f.cfg, f.store, f.execution, f.native, "kubernetes/original-openbao")
	if err != nil || podUID != wantPod || setUID != f.owner.StatefulSetUID {
		t.Fatal("same-Pod image handoff must preserve its earlier native replacement/unseal", err)
	}
	if deploymentplan.Digest(f.execution.Journal) != before {
		t.Fatal("verification rewrote original unseal or create journal")
	}
	entry := createOpenBaoACKCompletionEntry(f.execution.Journal, "openbao-managed-unseal-"+wantPod)
	if entry == nil {
		t.Fatal("native unseal producer did not record original evidence")
	}
	entry.IntentSHA256 = deploymentplan.Digest("foreign-init-or-share")
	if err := f.execution.save(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := originalCreateOpenBaoFoundationIdentity(t.Context(), f.cfg, f.store, f.execution, f.native, "kubernetes/original-openbao"); err == nil {
		t.Fatal("same-Pod image receipt substituted for original unseal evidence")
	}
}
