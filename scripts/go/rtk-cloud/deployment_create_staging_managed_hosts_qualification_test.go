package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// These fixtures persist the original admission, child plans, receipts and native
// owners in an isolated environment. The kubectl boundary permits only reads.
type stagingQualificationFixture struct {
	cfg      deploymentConfig
	store    secretStore
	parent   environmentCreatePlan
	e        *environmentCreateExecution
	b        environmentKubernetesBackend
	objects  map[string]deploymentplan.Object
	events   []deploymentplan.Object
	pods     []deploymentplan.Object
	registry any
	dir      string
}

func newStagingQualificationFixture(t *testing.T) *stagingQualificationFixture {
	t.Helper()
	cfg, store, operator, artifact := createStateFixture(t, "staging")
	e, err := startEnvironmentCreateExecution(t.Context(), cfg, artifact, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	f := &stagingQualificationFixture{cfg: cfg, store: store, e: e, objects: map[string]deploymentplan.Object{}, pods: []deploymentplan.Object{}, registry: map[string]int{"count": 0}, dir: t.TempDir()}
	if err := json.Unmarshal(artifact.Plan, &f.parent); err != nil {
		t.Fatal(err)
	}
	f.b = environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-staging-fixture")}
	if err := ensurePrivateDirectory(filepath.Join(store.Root, "deployment/tmp")); err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	script := "#!" + python + "\n" + `import json,os,sys
from pathlib import Path
root=Path(os.environ['RTK_STAGING_QUALIFICATION_TEST'])
a=sys.argv[1:]
with (root/'calls').open('a') as f: f.write(json.dumps(a)+'\n')
s=json.loads((root/'native.json').read_text())
if 'get' in a:
 i=a.index('get'); kind=a[i+1]
 if kind=='namespace':
  print(json.dumps({'items':[{'kind':'Namespace','metadata':{'name':n}} for n in sorted({o['metadata']['namespace'] for o in s['objects'].values()})]}));sys.exit()
 if kind=='validatingwebhookconfiguration' or ',' in kind and '--namespace' not in a:
  print(json.dumps({'items':[]}));sys.exit()
 if ',' in kind:
  ns=a[a.index('--namespace')+1];print(json.dumps({'items':[o for o in s['objects'].values() if o['metadata']['namespace']==ns]}));sys.exit()
 if kind in ('pods','events'): print(json.dumps({'items':s[kind]}));sys.exit()
 ns=a[a.index('--namespace')+1] if '--namespace' in a else ''
 obj=s['objects'].get(kind+'/'+ns+'/'+a[i+2])
 if obj is not None: print(json.dumps(obj))
 sys.exit()
if 'exec' in a and 'registry-read' in a:
 assert 'PGOPTIONS=-c default_transaction_read_only=on' in a and a[-1].startswith('SELECT ')
 if a[-1].startswith('SELECT document FROM pki_issuers'):
  issuers=json.loads((root/'issuers.json').read_text());print(json.dumps(issuers[a[-1].split("'")[1]]));sys.exit()
 print(json.dumps(s['registry']));sys.exit()
if 'exec' in a and 'sha256sum /openbao/transport/tls.crt' in a[-1]:
 print((root/'mounted').read_text());sys.exit()
if 'exec' in a and 'bao status -format=json' in a[-1]:
 print(json.dumps(s.get('status',{'initialized':True,'sealed':False})));sys.exit()
sys.exit(1)
`
	path := filepath.Join(f.dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", f.dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_STAGING_QUALIFICATION_TEST", f.dir)
	return f
}

func (f *stagingQualificationFixture) native(t *testing.T) {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"objects": f.objects, "pods": f.pods, "events": f.events, "registry": f.registry})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(f.dir, "native.json"), raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func (f *stagingQualificationFixture) save(t *testing.T, path string, v any) {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.write(path, raw, true); err != nil {
		t.Fatal(err)
	}
}
func (f *stagingQualificationFixture) stage(t *testing.T, stage string, defs []deploymentplan.Definition, accepted bool) (*deploymentplan.Plan, deploymentplan.Journal) {
	t.Helper()
	p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "staging", Stack: f.parent.Stack, Operation: "scoped", ConfigurationSHA256: f.parent.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(f.parent.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs)}, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
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
	j := deploymentplan.Journal{PlanSHA256: p.SHA256(), Owners: map[string]string{}, Accepted: accepted, AcceptanceStarted: accepted}
	if accepted {
		for i, r := range p.Resources {
			uid := fmt.Sprintf("%08d-1111-4111-8111-111111111111", i+1)
			o := deploymentplan.Clone(r.Desired)
			deploymentplan.Map(o["metadata"])["uid"] = uid
			f.objects[r.ID] = o
			j.Owners[r.ID] = uid
			j.Completed = append(j.Completed, r.ID)
		}
	}
	f.persistStage(t, p, j)
	if accepted {
		if err := f.e.RecordReceipt("kubernetes/"+stage, mustProviderRecoveryJSON(map[string]any{"plan_sha256": p.SHA256(), "owners": j.Owners})); err != nil {
			t.Fatal(err)
		}
		if err := f.e.CompleteEntry("kubernetes/"+stage, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	return p, j
}
func (f *stagingQualificationFixture) persistStage(t *testing.T, p *deploymentplan.Plan, j deploymentplan.Journal) {
	t.Helper()
	f.save(t, "deployment/rollback/"+p.SHA256()+"/plan.json", p)
	f.save(t, "deployment/rollback/"+p.SHA256()+"/journal.json", j)
	if err := f.e.RecordKubernetesLease(deploymentplan.LeaseOwner{ResourceID: "Lease/" + f.parent.Stack + "-video-cloud/rtk-deployment-execution", UID: p.SHA256()[:8] + "-1111-4111-8111-111111111111", Holder: p.SHA256() + ":original"}); err != nil {
		t.Fatal(err)
	}
}
func stagingTransportQualificationObject(kind, ns, name string, spec deploymentplan.Object) deploymentplan.Object {
	api := "v1"
	if kind == "Deployment" || kind == "StatefulSet" {
		api = "apps/v1"
	}
	if kind == "Job" {
		api = "batch/v1"
	}
	return deploymentplan.Object{"apiVersion": api, "kind": kind, "metadata": deploymentplan.Object{"name": name, "namespace": ns}, "spec": spec}
}
func stagingQualificationJob(ns, name, claim string) deploymentplan.Object {
	return stagingTransportQualificationObject("Job", ns, name, deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"restartPolicy": "Never", "containers": []any{deploymentplan.Object{"name": "enroll", "image": "fixture-image"}}, "volumes": []any{deploymentplan.Object{"name": "service-state", "persistentVolumeClaim": deploymentplan.Object{"claimName": claim}}}}}})
}
func (f *stagingQualificationFixture) attachment(job, uid, volume, owner string) {
	pod := job + "-original"
	event := func(kind, name, id, reason, message string) deploymentplan.Object {
		return deploymentplan.Object{"involvedObject": deploymentplan.Object{"kind": kind, "name": name, "uid": id}, "reason": reason, "message": message}
	}
	f.events = []deploymentplan.Object{event("Job", job, uid, "SuccessfulCreate", "Created pod: "+pod), event("Job", job, uid, "SuccessfulDelete", "Deleted pod: "+pod), event("Pod", pod, "22222222-2222-4222-8222-222222222222", "FailedAttachVolume", "Multi-Attach error for volume \""+volume+"\": already used by pod "+owner+"-original")}
}
func stagingQualificationFailedJob(o deploymentplan.Object, uid string) deploymentplan.Object {
	o = deploymentplan.Clone(o)
	deploymentplan.Map(o["metadata"])["uid"] = uid
	o["status"] = deploymentplan.Object{"failed": float64(1), "conditions": []any{deploymentplan.Object{"type": "Failed", "status": "True", "reason": "DeadlineExceeded"}}}
	return o
}
func (f *stagingQualificationFixture) unchanged(t *testing.T, p *deploymentplan.Plan) func() {
	t.Helper()
	path := filepath.Join(f.store.Root, "deployment/rollback", p.SHA256(), "journal.json")
	journal, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	native, err := os.ReadFile(filepath.Join(f.dir, "native.json"))
	if err != nil {
		t.Fatal(err)
	}
	parent := deploymentplan.Digest(f.e.Journal)
	return func() {
		t.Helper()
		after, _ := os.ReadFile(path)
		live, _ := os.ReadFile(filepath.Join(f.dir, "native.json"))
		if !bytes.Equal(journal, after) || !bytes.Equal(native, live) || parent != deploymentplan.Digest(f.e.Journal) {
			t.Fatal("qualification changed original journal or native resources")
		}
	}
}

func TestStagingServerEnrollmentQualificationPreservesOriginalFailure(t *testing.T) {
	for _, mode := range []string{"valid", "replaced-job", "changed-body", "unknown-write", "existing-issuance", "unknown-issuance", "started-pod", "missing-volume", "wrong-request-lineage"} {
		t.Run(mode, func(t *testing.T) {
			f := newStagingQualificationFixture(t)
			ns := f.parent.Stack + "-video-cloud"
			claim := "video-cloud-api-pki-controller-identity"
			_, cj := f.stage(t, "application-client-enrollment", []deploymentplan.Definition{{Object: stagingTransportQualificationObject("PersistentVolumeClaim", ns, claim, deploymentplan.Object{"accessModes": []any{"ReadWriteOnce"}}), Phase: "storage"}}, true)
			claimID := "PersistentVolumeClaim/" + ns + "/" + claim
			deploymentplan.Map(f.objects[claimID]["spec"])["volumeName"] = "original-volume"
			session := "33333333-3333-4333-8333-333333333333"
			name := "pki-enroll-video-cloud-api-pki-server-original"
			job := stagingQualificationJob(ns, name, claim)
			deploymentplan.Map(certIssuerObjectList(environmentPKIPod(job)["containers"])[0])["env"] = []any{deploymentplan.Object{"name": "PKI_SERVER_BOOTSTRAP_REQUEST_ID", "value": session + ":video-cloud-api-pki:server"}}
			if mode == "wrong-request-lineage" {
				deploymentplan.Map(certIssuerObjectList(environmentPKIPod(job)["containers"])[0])["env"] = []any{deploymentplan.Object{"name": "PKI_SERVER_BOOTSTRAP_REQUEST_ID", "value": "foreign:video-cloud-api-pki:server"}}
			}
			p, j := f.stage(t, "server-enrollment-video-cloud-api-pki", []deploymentplan.Definition{{Object: job, Phase: "bootstrap"}}, false)
			id := p.Resources[0].ID
			uid := "44444444-4444-4444-8444-444444444444"
			j.Failed = id
			j.Owners[id] = uid
			f.objects[id] = stagingQualificationFailedJob(p.Resources[0].Desired, uid)
			f.attachment(name, uid, "original-volume", "video-cloud-api-pki")
			switch mode {
			case "replaced-job":
				deploymentplan.Map(f.objects[id]["metadata"])["uid"] = cj.Owners[claimID]
			case "changed-body":
				environmentPKIPod(f.objects[id])["restartPolicy"] = "Always"
			case "unknown-write":
				j.InFlight = id
			case "existing-issuance":
				f.registry = map[string]int{"count": 1}
			case "unknown-issuance":
				f.registry = map[string]int{"other": 0}
			case "started-pod":
				f.events = append(f.events, deploymentplan.Object{"involvedObject": deploymentplan.Object{"kind": "Pod", "name": name + "-original", "uid": "22222222-2222-4222-8222-222222222222"}, "reason": "Started"})
			case "missing-volume":
				delete(deploymentplan.Map(f.objects[claimID]["spec"]), "volumeName")
			}
			f.persistStage(t, p, j)
			f.native(t)
			defer f.unchanged(t, p)()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			forward, err := qualifyStagingServerEnrollmentForward(ctx, f.cfg, f.store, &f.parent, f.e, environmentCreateBootstrapRuntime{Execution: f.e, Backend: f.b})
			if mode != "valid" {
				if err == nil {
					t.Fatal("unsafe enrollment recovery admitted", mode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if forward.Original != p.SHA256() || forward.FailedUID != uid || deploymentplan.Map(forward.Definition.Object["metadata"])["name"] != name+"-rwo" {
				t.Fatal("forward lost original lineage")
			}
			got := deploymentplan.Clone(forward.Definition.Object)
			deploymentplan.Map(got["metadata"])["name"] = name
			delete(environmentPKIPod(got), "affinity")
			if deploymentplan.Digest(got) != deploymentplan.Digest(p.Resources[0].Desired) || environmentPKIPod(forward.Definition.Object)["affinity"] == nil {
				t.Fatal("forward changed more than replacement name and same-node placement")
			}
		})
	}
}

func TestStagingCoreEnrollmentQualificationRetainsClaimsAndChecksNativeIssuance(t *testing.T) {
	for _, mode := range []string{"valid", "replaced-claim", "changed-claim", "replaced-resident", "later-job-exists", "unknown-write", "existing-issuance", "unknown-issuance", "null-issuance"} {
		t.Run(mode, func(t *testing.T) {
			f := newStagingQualificationFixture(t)
			ns := f.parent.Stack + "-account-manager"
			video := f.parent.Stack + "-video-cloud"
			for _, owner := range []struct{ ns, name, stage string }{{ns, "account-manager", "service-active-listeners"}, {video, "certissuer", "service-managed-enrollment-issuer"}} {
				d := stagingTransportQualificationObject("Deployment", owner.ns, owner.name, deploymentplan.Object{"replicas": float64(1), "selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app.kubernetes.io/name": owner.name}}, "template": deploymentplan.Object{"spec": deploymentplan.Object{"volumes": []any{deploymentplan.Object{"name": "state", "persistentVolumeClaim": deploymentplan.Object{"claimName": environmentCreateServiceOwnerClaim(owner.name)}}}}}})
				f.stage(t, owner.stage, []deploymentplan.Definition{{Object: d, Phase: "workload"}}, true)
			}
			session := "33333333-3333-4333-8333-333333333333"
			suffix := strings.ReplaceAll(session, "-", "")[:12]
			var defs []deploymentplan.Definition
			for _, owner := range []struct{ ns, name string }{{ns, "account-manager"}, {video, "certissuer"}, {video, "pki-controller"}} {
				defs = append(defs, deploymentplan.Definition{Object: stagingTransportQualificationObject("PersistentVolumeClaim", owner.ns, environmentCreateServiceOwnerClaim(owner.name), deploymentplan.Object{"accessModes": []any{"ReadWriteOnce"}}), Phase: "storage"})
			}
			for _, owner := range []struct{ ns, name, kind string }{{ns, "account-manager", "client"}, {ns, "account-manager", "server"}, {video, "certissuer", "client"}, {video, "pki-controller", "client"}, {video, "pki-controller", "server"}} {
				defs = append(defs, deploymentplan.Definition{Object: stagingQualificationJob(owner.ns, "pki-enroll-"+owner.name+"-"+owner.kind+"-"+suffix, environmentCreateServiceOwnerClaim(owner.name)), Phase: "bootstrap"})
			}
			p, j := f.stage(t, stagingCoreEnrollmentStage, defs, false)
			name := "pki-enroll-account-manager-client-" + suffix
			failed := "Job/" + ns + "/" + name
			uid := "44444444-4444-4444-8444-444444444444"
			j.Failed = failed
			for i, r := range p.Resources {
				if r.Phase == "storage" {
					id := fmt.Sprintf("%08d-1111-4111-8111-111111111111", i+10)
					j.Completed = append(j.Completed, r.ID)
					j.Owners[r.ID] = id
					o := deploymentplan.Clone(r.Desired)
					deploymentplan.Map(o["metadata"])["uid"] = id
					deploymentplan.Map(o["spec"])["volumeName"] = "original-volume"
					f.objects[r.ID] = o
				}
				if r.ID == failed {
					j.Owners[r.ID] = uid
					f.objects[r.ID] = stagingQualificationFailedJob(r.Desired, uid)
				}
			}
			f.attachment(name, uid, "original-volume", "account-manager")
			f.registry = map[string]int{"clients": 0, "servers": 0}
			claim := "PersistentVolumeClaim/" + ns + "/account-manager-service-state"
			switch mode {
			case "replaced-claim":
				deploymentplan.Map(f.objects[claim]["metadata"])["uid"] = uid
			case "changed-claim":
				deploymentplan.Map(f.objects[claim]["spec"])["accessModes"] = []any{"ReadWriteMany"}
			case "replaced-resident":
				deploymentplan.Map(f.objects["Deployment/"+ns+"/account-manager"]["metadata"])["uid"] = uid
			case "later-job-exists":
				for _, r := range p.Resources {
					if r.Desired["kind"] == "Job" && r.ID != failed {
						f.objects[r.ID] = stagingQualificationFailedJob(r.Desired, uid)
						break
					}
				}
			case "unknown-write":
				j.InFlight = failed
			case "existing-issuance":
				f.registry = map[string]int{"clients": 1, "servers": 0}
			case "unknown-issuance":
				f.registry = map[string]int{"unrelated": 0, "unexpected": 0}
			case "null-issuance":
				f.registry = map[string]any{"clients": nil, "servers": nil}
			}
			f.persistStage(t, p, j)
			f.native(t)
			defer f.unchanged(t, p)()
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			service := environmentCreateServiceCeremony{BootstrapSessionID: session, Intermediate: environmentCreateRegisteredIssuer{ID: "55555555-5555-4555-8555-555555555555"}}
			entry := *createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/"+stagingCoreEnrollmentStage)
			forward, err := qualifyStagingServiceCoreForward(ctx, f.cfg, f.store, &f.parent, f.e, environmentCreateBootstrapRuntime{Execution: f.e, Backend: f.b}, service, entry)
			if mode != "valid" {
				if err == nil {
					t.Fatal("unsafe core enrollment recovery admitted", mode)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if forward.OriginalSHA != p.SHA256() || forward.JournalSHA != deploymentplan.Digest(j) || forward.FailedJob != failed || forward.FailedUID != uid || len(forward.Definitions) != 8 {
				t.Fatal("forward lost original journal lineage")
			}
			retained, placed := 0, 0
			for _, d := range forward.Definitions {
				if d.Retain {
					retained++
					if deploymentplan.Map(d.Object["metadata"])["uid"] != j.Owners[deploymentplan.ID(d.Object)] {
						t.Fatal("retained claim lost owner")
					}
				}
				if environmentPKIPod(d.Object)["affinity"] != nil {
					placed++
				}
			}
			if retained != 3 || placed != 3 {
				t.Fatalf("retained %d claims, placed %d jobs", retained, placed)
			}
		})
	}
}

func TestStagingManagedHostsQualificationRetainsClaimsAndIssuerLineage(t *testing.T) {
	for _, mode := range []string{"valid", "replaced-openbao", "changed-openbao", "unknown-write", "changed-native-issuer", "wrong-intermediate-parent"} {
		t.Run(mode, func(t *testing.T) {
			_, _, _, service := createServiceRecipeFixture(t, "staging")
			f := newStagingQualificationFixture(t)
			ns := f.parent.Stack + "-secrets"
			claims := []any{}
			for _, name := range []string{"data", "audit"} {
				claims = append(claims, deploymentplan.Object{"metadata": deploymentplan.Object{"name": name}, "spec": deploymentplan.Object{"resources": deploymentplan.Object{"requests": deploymentplan.Object{"storage": "10Gi"}}}})
			}
			controller := stagingTransportQualificationObject("StatefulSet", ns, "openbao", deploymentplan.Object{"serviceName": "openbao", "podManagementPolicy": "OrderedReady", "selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app": "openbao"}}, "volumeClaimTemplates": claims, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app": "openbao"}}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "openbao", "image": "original-image"}}}}})
			_, foundation := f.stage(t, "foundation", []deploymentplan.Definition{{Object: controller, Phase: "workload"}}, true)
			for _, stage := range []string{"transport-enrollment-issuer", "transport-server-enrollment"} {
				f.stage(t, stage, []deploymentplan.Definition{{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": ns, "name": stage}, "data": deploymentplan.Object{"lineage": "original"}}, Phase: "configuration"}}, true)
			}
			defs := []deploymentplan.Definition{{Object: deploymentplan.Clone(controller), Phase: "workload"}}
			for i := 0; i < 6; i++ {
				defs = append(defs, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": ns, "name": fmt.Sprintf("managed-trust-%d", i)}, "data": deploymentplan.Object{"root": "original"}}, Phase: "configuration"})
			}
			original, j := f.stage(t, stagingManagedHostsStage, defs, false)
			// The stopped stage was admitted locally but has not acquired a native child
			// lease or written a native journal. Keep its immutable input and parent intent.
			for _, name := range []string{"plan.json", "journal.json"} {
				if err := os.Remove(filepath.Join(f.store.Root, "deployment/rollback", original.SHA256(), name)); err != nil {
					t.Fatal(err)
				}
			}
			f.e.Journal.KubernetesLeases = f.e.Journal.KubernetesLeases[:len(f.e.Journal.KubernetesLeases)-1]
			if err := f.e.save(); err != nil {
				t.Fatal(err)
			}
			roots, intermediates := map[string]environmentCreateRegisteredIssuer{}, map[string]environmentCreateRegisteredIssuer{}
			docs := map[string]map[string]any{}
			for i, domain := range managedPKIDomains {
				root := service.Root
				root.Document = deploymentplan.Clone(root.Document)
				root.ID = fmt.Sprintf("%08d-1111-4111-8111-111111111111", i+1)
				root.Domain = domain
				root.Document["issuer_id"], root.Document["trust_domain"] = root.ID, domain
				roots[domain] = root
				docs[root.ID] = deploymentplan.Clone(root.Document)
				if domain != "device" {
					issuer := service.Intermediate
					issuer.Document = deploymentplan.Clone(issuer.Document)
					issuer.ID = fmt.Sprintf("%08d-2222-4222-8222-222222222222", i+1)
					issuer.Domain = domain
					issuer.Document["issuer_id"], issuer.Document["trust_domain"], issuer.Document["parent_issuer_id"] = issuer.ID, domain, root.ID
					if mode == "wrong-intermediate-parent" && domain == "service" {
						issuer.Document["parent_issuer_id"] = "99999999-9999-4999-8999-999999999999"
					}
					intermediates[domain] = issuer
					docs[issuer.ID] = deploymentplan.Clone(issuer.Document)
				}
			}
			if err := saveEnvironmentCreateRegisteredIssuers(f.store, roots, intermediates); err != nil {
				t.Fatal(err)
			}
			id := deploymentplan.ID(controller)
			switch mode {
			case "replaced-openbao":
				deploymentplan.Map(f.objects[id]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "changed-openbao":
				deploymentplan.Map(f.objects[id]["spec"])["serviceName"] = "foreign-service"
			case "unknown-write":
				j.InFlight = original.Resources[0].ID
				f.persistStage(t, original, j)
			case "changed-native-issuer":
				docs[roots["service"].ID]["signer_reference"] = "different/1"
			}
			raw, err := json.Marshal(docs)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(f.dir, "issuers.json"), raw, 0600); err != nil {
				t.Fatal(err)
			}
			f.native(t)
			before := createBootstrapStateSnapshot(t, f.store.Root)
			nativeBefore, err := os.ReadFile(filepath.Join(f.dir, "native.json"))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
			defer cancel()
			got, err := qualifyStagingManagedHostsForward(ctx, f.cfg, f.store, &f.parent, f.e, environmentCreateBootstrapRuntime{Execution: f.e, Backend: f.b, StagingControllerHandoff: true})
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				if got.Original != original.SHA256() || got.AlreadyAccepted || len(got.Definitions) != 7 || len(got.Roots) != len(managedPKIDomains) || len(got.Intermediates) != len(managedPKIDomains)-1 {
					t.Fatal("managed forward lost its original scope")
				}
				found := false
				for _, d := range got.Definitions {
					if deploymentplan.ID(d.Object) == id {
						found = true
						if deploymentplan.Map(d.Object["metadata"])["uid"] != foundation.Owners[id] || deploymentplan.Digest(deploymentplan.Map(d.Object["spec"])["volumeClaimTemplates"]) != deploymentplan.Digest(claims) {
							t.Fatal("forward replaced original OpenBao allocation")
						}
					}
				}
				if !found {
					t.Fatal("forward omitted original OpenBao")
				}
			} else if err == nil {
				t.Fatal("unsafe managed-host recovery admitted", mode)
			}
			assertRecoveryContractReadOnly(t, f.store, before)
			nativeAfter, _ := os.ReadFile(filepath.Join(f.dir, "native.json"))
			if !bytes.Equal(nativeBefore, nativeAfter) {
				t.Fatal("qualification mutated native resources")
			}
		})
	}
}

func TestStagingManagedHostsExecutionRejectsDifferentServiceCeremony(t *testing.T) {
	for _, changed := range []string{"root", "intermediate"} {
		t.Run(changed, func(t *testing.T) {
			cfg, p, runtime, service := createServiceRecipeFixture(t, "staging")
			f := &stagingManagedHostsForward{Roots: map[string]environmentCreateRegisteredIssuer{"service": service.Root}, Intermediates: map[string]environmentCreateRegisteredIssuer{"service": service.Intermediate}}
			if changed == "root" {
				root := f.Roots["service"]
				root.Document = deploymentplan.Clone(root.Document)
				root.Document["issuer_id"] = "99999999-9999-4999-8999-999999999999"
				f.Roots["service"] = root
			} else {
				issuer := f.Intermediates["service"]
				issuer.Document = deploymentplan.Clone(issuer.Document)
				issuer.Document["parent_issuer_id"] = "99999999-9999-4999-8999-999999999999"
				f.Intermediates["service"] = issuer
			}
			before := createBootstrapStateSnapshot(t, runtime.Backend.Store.Root)
			recovery := &createServiceAuthorizationRecovery{ManagedHosts: f, Enrollment: &service}
			_, err := executeStagingManagedHostsForward(t.Context(), cfg, p, environmentRelease{}, runtime.Backend.Store, nil, runtime, recovery, func(context.Context, string, []deploymentplan.Definition) error {
				t.Fatal("different Service lineage reached native apply")
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), "Service ceremony differs") {
				t.Fatal("different original Service ceremony was not rejected", err)
			}
			assertRecoveryContractReadOnly(t, runtime.Backend.Store, before)
		})
	}
}
