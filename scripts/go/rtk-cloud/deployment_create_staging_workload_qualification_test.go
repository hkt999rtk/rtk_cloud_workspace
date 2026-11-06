package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// All parent and child evidence lives under createStateFixture's isolated store.
// The fixture persists exact historical inputs, including an unwritten parent.
type stagingWorkloadQualificationFixture struct {
	cfg             deploymentConfig
	store           secretStore
	parent          environmentCreatePlan
	e               *environmentCreateExecution
	original, child *deploymentplan.Plan
	journal         deploymentplan.Journal
	stage           string
}

func newStagingWorkloadQualificationFixture(t *testing.T, resources []deploymentplan.Resource, configure ...func(*deploymentConfig, *environmentCreatePlan, secretStore)) *stagingWorkloadQualificationFixture {
	t.Helper()
	cfg, store, operator, artifact := createStateFixture(t, "staging")
	f := &stagingWorkloadQualificationFixture{cfg: cfg, store: store}
	if err := json.Unmarshal(artifact.Plan, &f.parent); err != nil {
		t.Fatal(err)
	}
	if len(configure) != 0 {
		for _, setup := range configure {
			setup(&f.cfg, &f.parent, store)
		}
		f.parent.ConfigurationSHA256 = deploymentplan.Digest(f.cfg)
		operator = f.parent.OperatorSHA256
		raw := mustProviderRecoveryJSON(f.parent)
		sha, err := environmentCreatePlanSHA256(raw)
		if err != nil {
			t.Fatal(err)
		}
		generation, err := invalidateEnvironmentPreflightProofWithToken(f.cfg)
		if err != nil {
			t.Fatal(err)
		}
		q := artifact.Qualification
		q.PlanSHA256 = sha
		proofs := coreContractPublicationProofs(t, &f.parent.Release)
		artifact, err = publishEnvironmentCreateArtifact(t.Context(), f.cfg, generation, raw, operator, q, proofs, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
	}
	e, err := startEnvironmentCreateExecution(t.Context(), f.cfg, artifact, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	f.e = e
	f.original = &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: deploymentplan.Inputs{Environment: "staging", Stack: f.parent.Stack, Operation: "scoped", ConfigurationSHA256: f.parent.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(f.parent.Release), BindingsSHA256: deploymentplan.Digest("original workload")}, Resources: resources}
	raw, _ := json.Marshal(f.original)
	f.child = &deploymentplan.Plan{}
	if err := json.Unmarshal(raw, f.child); err != nil {
		t.Fatal(err)
	}
	// Historical PostgreSQL allocation repair carries the server-assigned claim
	// template into its exact child, just as the original continuation did.
	for i, r := range f.child.Resources {
		if r.ID == "StatefulSet/"+f.parent.Stack+"-platform/postgresql" && r.Action == "update" && r.Baseline != nil {
			deploymentplan.Map(f.child.Resources[i].Desired["spec"])["volumeClaimTemplates"] = deploymentplan.Map(deploymentplan.Clone(r.Baseline)["spec"])["volumeClaimTemplates"]
		}
	}
	f.child.BindingsSHA256 = deploymentplan.Digest("original allocation continuation")
	f.stage = stagingWorkloadAllocationPrefix + f.original.SHA256()[:12]
	f.persistStage(t, "workload", f.original)
	f.persistStage(t, f.stage, f.child)
	f.journal = deploymentplan.Journal{PlanSHA256: f.child.SHA256(), Owners: map[string]string{}}
	return f
}

func (f *stagingWorkloadQualificationFixture) persistStage(t *testing.T, stage string, p *deploymentplan.Plan) {
	t.Helper()
	if err := saveEnvironmentCreateStageInput(t.Context(), f.store, f.e, stage, p); err != nil {
		t.Fatal(err)
	}
	intent, err := environmentCreateKubernetesStageIntent(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.e.BeginEntry("kubernetes/"+stage, stage, intent, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
}

func (f *stagingWorkloadQualificationFixture) saveProgress(t *testing.T, p *deploymentplan.Plan, j deploymentplan.Journal) {
	t.Helper()
	for name, value := range map[string]any{"plan": p, "journal": j} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := f.store.write("deployment/rollback/"+p.SHA256()+"/"+name+".json", raw, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.e.RecordKubernetesLease(deploymentplan.LeaseOwner{ResourceID: "Lease/" + f.parent.Stack + "-video-cloud/rtk-deployment-execution", UID: p.SHA256()[:8] + "-1111-4111-8111-111111111111", Holder: p.SHA256() + ":qualification"}); err != nil {
		t.Fatal(err)
	}
}

func stagingQualificationObject(kind, namespace, name, uid string, spec deploymentplan.Object) deploymentplan.Object {
	return deploymentplan.Object{"apiVersion": "v1", "kind": kind, "metadata": deploymentplan.Object{"namespace": namespace, "name": name, "uid": uid}, "spec": spec}
}

func stagingQualificationBackend(t *testing.T, store secretStore, objects map[string]deploymentplan.Object) environmentKubernetesBackend {
	t.Helper()
	dir := t.TempDir()
	raw, err := json.Marshal(objects)
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "objects.json"), string(raw))
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, filepath.Join(dir, "kubectl"), "#!"+python+"\n"+`import json,os,sys
from pathlib import Path
root=Path(os.environ['RTK_STAGING_QUALIFICATION_TEST'])
a=sys.argv[1:]
with (root/'calls').open('a') as f: f.write(' '.join(a)+'\n')
if 'get' not in a: sys.exit(1)
i=a.index('get'); ns=a[a.index('--namespace')+1] if '--namespace' in a else a[a.index('-n')+1]
o=json.loads((root/'objects.json').read_text()).get(a[i+1]+'/'+ns+'/'+a[i+2])
if o is not None: print(json.dumps(o))
`)
	if err := os.Chmod(filepath.Join(dir, "kubectl"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_STAGING_QUALIFICATION_TEST", dir)
	t.Cleanup(func() {
		calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
		for _, line := range strings.Split(strings.TrimSpace(string(calls)), "\n") {
			if line != "" && !strings.Contains(line, " get ") {
				t.Errorf("unexpected Kubernetes mutation: %s", line)
			}
		}
	})
	return environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated staging fixture")}
}

func TestStagingControllerHandoffQualificationKeepsOriginalOwners(t *testing.T) {
	for _, mode := range []string{"original", "service-replaced", "controller-replaced", "service-changed", "controller-already-changed", "unknown-write"} {
		t.Run(mode, func(t *testing.T) {
			ns := "video-cloud-staging-video-cloud"
			service := stagingQualificationObject("Service", ns, "pki-controller", "11111111-1111-4111-8111-111111111111", deploymentplan.Object{"ports": []any{deploymentplan.Object{"name": "https", "targetPort": float64(18446)}}})
			controller := stagingQualificationObject("Deployment", ns, "pki-controller", "22222222-2222-4222-8222-222222222222", deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "app", "ports": []any{deploymentplan.Object{"name": "tls", "containerPort": float64(18446)}}}}}}})
			newService, newController := deploymentplan.Clone(service), deploymentplan.Clone(controller)
			managedNamedField(deploymentplan.Map(newService["spec"])["ports"], "https")["targetPort"] = "https"
			managedNamedField(mqttFoundationPrimary(newController)["ports"], "tls")["name"] = "https"
			resources := []deploymentplan.Resource{
				{ID: deploymentplan.ID(service), UID: deploymentplan.Map(service["metadata"])["uid"].(string), Action: "update", Phase: "configuration", Baseline: service, Desired: newService},
				{ID: deploymentplan.ID(controller), UID: deploymentplan.Map(controller["metadata"])["uid"].(string), Action: "update", Phase: "workload", Baseline: controller, Desired: newController},
			}
			f := newStagingWorkloadQualificationFixture(t, resources)
			f.journal.Completed = []string{resources[0].ID}
			f.journal.Owners[resources[0].ID] = resources[0].UID
			objects := map[string]deploymentplan.Object{resources[0].ID: deploymentplan.Clone(newService), resources[1].ID: deploymentplan.Clone(controller)}
			switch mode {
			case "service-replaced":
				deploymentplan.Map(objects[resources[0].ID]["metadata"])["uid"] = "33333333-3333-4333-8333-333333333333"
			case "controller-replaced":
				deploymentplan.Map(objects[resources[1].ID]["metadata"])["uid"] = "33333333-3333-4333-8333-333333333333"
			case "service-changed":
				managedNamedField(deploymentplan.Map(objects[resources[0].ID]["spec"])["ports"], "https")["targetPort"] = "foreign"
			case "controller-already-changed":
				objects[resources[1].ID] = newController
			case "unknown-write":
				f.journal.InFlight = resources[1].ID
			}
			f.saveProgress(t, f.child, f.journal)
			before := deploymentplan.Digest(f.e.Journal)
			ok, err := stagingControllerHandoffRecorded(t.Context(), f.cfg, f.store, environmentCreateBootstrapRuntime{Execution: f.e, Backend: stagingQualificationBackend(t, f.store, objects)})
			if mode == "original" {
				if !ok || err != nil {
					t.Fatalf("original handoff rejected: %v", err)
				}
			} else if err == nil || ok {
				t.Fatal("changed or unknown handoff qualified")
			}
			if deploymentplan.Digest(f.e.Journal) != before {
				t.Fatal("qualification rewrote original parent")
			}
		})
	}
}

func TestStagingWorkloadAllocationQualificationCompletesAcceptedOriginalChild(t *testing.T) {
	object := stagingQualificationObject("StatefulSet", "video-cloud-staging-platform", "postgresql", "11111111-1111-4111-8111-111111111111", deploymentplan.Object{"volumeClaimTemplates": []any{deploymentplan.Object{"metadata": deploymentplan.Object{"name": "data"}, "spec": deploymentplan.Object{"storageClassName": "linode-block-storage-retain"}}}})
	id := deploymentplan.ID(object)
	f := newStagingWorkloadQualificationFixture(t, []deploymentplan.Resource{{ID: id, UID: "11111111-1111-4111-8111-111111111111", Action: "retain", Phase: "workload", Desired: object, Baseline: object}})
	f.journal.Completed = []string{id}
	f.journal.Owners[id] = f.child.Resources[0].UID
	f.journal.Accepted, f.journal.AcceptanceStarted = true, true
	f.saveProgress(t, f.child, f.journal)
	if err := f.e.RecordReceipt("kubernetes/"+f.stage, mustProviderRecoveryJSON(deploymentplan.Object{"plan_sha256": f.child.SHA256(), "owners": f.journal.Owners})); err != nil {
		t.Fatal(err)
	}
	if err := f.e.CompleteEntry("kubernetes/"+f.stage, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	receipt := stagingWorkloadAllocationReceipt{f.child.SHA256(), f.journal.Owners, "kubernetes/" + f.stage, f.original.SHA256()}
	if err := f.e.RecordReceipt("kubernetes/workload", mustProviderRecoveryJSON(receipt)); err != nil {
		t.Fatal(err)
	}
	before := *createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/workload")
	backend := stagingQualificationBackend(t, f.store, map[string]deploymentplan.Object{})
	handled, err := applyStagingWorkloadAllocationForward(t.Context(), f.cfg, f.store, &f.parent, f.e, backend)
	if err != nil || !handled {
		t.Fatal("accepted original allocation did not finish parent", err)
	}
	after := createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/workload")
	if after.State != "completed" || after.IntentSHA256 != before.IntentSHA256 || !after.StartedAt.Equal(before.StartedAt) || string(after.Receipt) != string(before.Receipt) {
		t.Fatal("parent completion replaced original accepted intent")
	}
	selected, err := freshCreateWorkloadStage(f.store, f.e)
	if err != nil || selected != f.stage {
		t.Fatalf("selected %q after parent completion: %v", selected, err)
	}
}
