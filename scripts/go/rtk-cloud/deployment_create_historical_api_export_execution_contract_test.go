package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// API server keys are generated once as the native enrollment response and
// remain bound to the original Service ICA, request and owner claim. The
// exporter returns the actual leaf/ICA/Root chain; SQL retains its leaf only.
type historicalAPIIdentity struct {
	Row      managedTransportServerEvidence
	Material environmentCreateOwnedServerMaterial
}

func historicalAPIServerIdentity(t *testing.T, f *acceptedCoreExecutionContract, service environmentCreateServiceCeremony, name string) historicalAPIIdentity {
	t.Helper()
	dns := name + "." + f.p.Plan.Stack + "-video-cloud.svc"
	certificate, key, csr := historicalTransportSignedLeaf(t, service.Intermediate, f.issuerKey, "service:video-cloud-api", []string{dns})
	leaf, err := parseFirstCertificatePEM([]byte(certificate))
	if err != nil {
		t.Fatal(err)
	}
	return historicalAPIIdentity{
		Row:      managedTransportServerEvidence{Domain: "service", Caller: "service:video-cloud-api", Request: service.BootstrapSessionID + ":" + name + ":server", Issuer: service.Intermediate.ID, Status: "succeeded", Fingerprint: environmentPreflightBytesSHA256(leaf.Raw), CSR: csr, Certificate: certificate, DNS: []string{dns}},
		Material: environmentCreateOwnedServerMaterial{Certificate: certificate + service.Intermediate.Certificate + service.Root.Certificate, Key: key, Root: service.Root.RootFingerprint, Name: dns, Environment: f.cfg.Environment},
	}
}

func installHistoricalAPIExportProtocol(t *testing.T, f *acceptedCoreExecutionContract, identities map[string]historicalAPIIdentity) {
	t.Helper()
	f.protocol["historical_api_exports"] = identities
	f.publish(t)
	original := os.Getenv("RTK_CLOUD_KUBECTL")
	selectedNames := []string{}
	for _, identity := range identities {
		selectedNames = append(selectedNames, identity.Material.Name)
	}
	dir := t.TempDir()
	script := `#!/usr/bin/env python3
import json,os,sys
with open(os.environ['RTK_CORE_CONTRACT_NATIVE']) as source:d=json.load(source)
a=sys.argv[1:];joined=' '.join(a)
def issued(value):
 session,name,kind=value['Row']['request_id'].split(':')
 if kind!='server':return False
 namespace=value['Material']['server_name'].removeprefix(name+'.').removesuffix('.svc')
 job=d['objects'].get('Job/'+namespace+'/pki-enroll-'+name+'-server-'+session.replace('-','')[:12])
 if job is None:return False
 status=job.get('status',{})
 return status.get('succeeded')==1 and status.get('failed',0)==0 and status.get('active',0)==0
if 'get' in a and a[a.index('get')+1]=='pods' and '--selector' in a:
 ns=a[a.index('--namespace')+1];key,value=a[a.index('--selector')+1].split('=',1)
 print(json.dumps({'apiVersion':'v1','kind':'PodList','items':[v for v in d['objects'].values() if v['kind']=='Pod' and v.get('metadata',{}).get('namespace')==ns and v.get('metadata',{}).get('labels',{}).get(key)==value]}))
elif 'wait' in a and '--for=condition=complete' in a and any(x.startswith('Job/') for x in a):
 ns=a[a.index('--namespace')+1];target=next(x for x in a if x.startswith('Job/'));kind,name=target.split('/',1)
 job=d['objects'].get(kind+'/'+ns+'/'+name)
 if job is None or job.get('metadata',{}).get('deletionTimestamp'):sys.exit('original API enrollment owner absent')
 status=job.get('status',{});conditions=status.get('conditions',[])
 if status.get('succeeded')!=1 or status.get('failed',0)!=0 or status.get('active',0)!=0 or not any(x.get('type')=='Complete' and x.get('status')=='True' for x in conditions) or any(x.get('type') in ('Failed','FailureTarget') and x.get('status')=='True' for x in conditions):sys.exit('original API enrollment is not natively complete')
 print(name+' condition met')
elif 'rollout' in a and 'status' in a and any(x.startswith('Deployment/') for x in a):
 ns=a[a.index('--namespace')+1];target=next(x for x in a if x.startswith('Deployment/'));kind,name=target.split('/',1)
 owner=d['objects'].get(kind+'/'+ns+'/'+name)
 if owner is None or owner.get('metadata',{}).get('deletionTimestamp'):sys.exit('original API custody owner absent')
 meta=owner['metadata'];status=owner.get('status',{});replicas=owner.get('spec',{}).get('replicas',1)
 if not isinstance(replicas,(int,float)) or replicas<=0 or int(replicas)!=replicas or status.get('observedGeneration',0)<meta.get('generation',1) or any(status.get(key)!=replicas for key in ('replicas','readyReplicas','updatedReplicas','availableReplicas')) or status.get('unavailableReplicas',0)!=0:sys.exit('original API custody is not natively ready')
 sets=[x for x in d['objects'].values() if x['kind']=='ReplicaSet' and x.get('metadata',{}).get('namespace')==ns and any(r.get('controller') is True and r.get('uid')==meta['uid'] for r in x['metadata'].get('ownerReferences',[]))]
 current=[x for x in sets if x.get('spec',{}).get('template')==owner.get('spec',{}).get('template')]
 setids={x['metadata']['uid'] for x in current};pods=[x for x in d['objects'].values() if x['kind']=='Pod' and x.get('metadata',{}).get('namespace')==ns and any(r.get('controller') is True and r.get('uid') in setids for r in x['metadata'].get('ownerReferences',[]))]
 if len(current)!=1 or len(pods)!=replicas or any(x.get('metadata',{}).get('deletionTimestamp') or x.get('status',{}).get('phase')!='Running' or not any(c.get('type')=='Ready' and c.get('status')=='True' for c in x.get('status',{}).get('conditions',[])) for x in pods):sys.exit('original API custody current Pod lineage is not ready')
 print(name+' successfully rolled out')
elif 'wait' in a and '--for=jsonpath={.status.phase}=Bound' in a and any(x.startswith('PersistentVolumeClaim/') for x in a):
 ns=a[a.index('--namespace')+1];target=next(x for x in a if x.startswith('PersistentVolumeClaim/'));kind,name=target.split('/',1)
 claim=d['objects'].get(kind+'/'+ns+'/'+name)
 if claim is None or claim.get('metadata',{}).get('deletionTimestamp') or claim.get('status',{}).get('phase')!='Bound':sys.exit('original native claim is not bound')
 volume=d['objects'].get('PersistentVolume//'+claim.get('spec',{}).get('volumeName',''))
 if volume is None or volume.get('metadata',{}).get('deletionTimestamp') or volume.get('status',{}).get('phase')!='Bound' or volume.get('spec',{}).get('claimRef')!={'namespace':ns,'name':name,'uid':claim['metadata']['uid']}:sys.exit('original native PV claim binding differs')
 print(name+' condition met')
elif 'export-server' in a and a[-1] in SELECTED_NAMES:
 dns=a[-1];matches=[v for v in d['historical_api_exports'].values() if v['Material']['server_name']==dns]
 if len(matches)!=1 or not issued(matches[0]):sys.exit('unknown or unissued actual API server owner')
 print(json.dumps(matches[0]['Material']))
elif 'FROM pki_server_issuances' in joined and "domain='service'" in joined:
 print(json.dumps([v['Row'] for _,v in sorted(d['historical_api_exports'].items()) if issued(v)]))
else:os.execv(PARENT,[PARENT]+a)
`
	// Capture the predecessor and exact API scope per wrapper. Reinstallation
	// must not redirect an older wrapper back to itself or swallow another
	// owner's native export.
	script = strings.ReplaceAll(script, "PARENT", strconv.Quote(original))
	script = strings.ReplaceAll(script, "SELECTED_NAMES", string(mustProviderRecoveryJSON(selectedNames)))
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(dir, "kubectl"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// The external API implements a conditional delete against the actual stored
// UID. Production still constructs the retirement, journals it and proves absence.
type historicalAPIRetirementBackend struct {
	historicalAcceptedScopedBackend
}

func (b historicalAPIRetirementBackend) Write(ctx context.Context, action string, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	if action != "retire" {
		return b.normalTailE2EBackend.Write(ctx, action, body, dry)
	}
	if err := b.e.Check(ctx); err != nil {
		return nil, err
	}
	id := deploymentplan.ID(body)
	actual := b.objects[id]
	if actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != deploymentplan.Map(body["metadata"])["uid"] {
		return nil, fmt.Errorf("isolated API retirement native UID precondition failed: %s", id)
	}
	if !dry {
		delete(b.objects, id)
		b.writes[id]++
	}
	return deploymentplan.Clone(actual), nil
}
func (b historicalAPIRetirementBackend) Wait(ctx context.Context, r deploymentplan.Resource) error {
	if r.Action != "retire" {
		return b.normalTailE2EBackend.Wait(ctx, r)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if b.objects[r.ID] != nil {
		return fmt.Errorf("isolated API retired owner remains")
	}
	return nil
}
func historicalAPIApply(t *testing.T, f *acceptedCoreExecutionContract) environmentCreateBootstrapApply {
	t.Helper()
	return func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		retire := false
		for _, d := range defs {
			retire = retire || d.Retire
		}
		if !retire {
			f.acceptedHistoricalStage(t, stage, defs)
			return nil
		}
		var live []deploymentplan.Object
		for _, object := range f.native.objects {
			live = append(live, object)
		}
		p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: f.cfg.Environment, Stack: f.e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: f.p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(f.p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs)}, defs, live)
		if err != nil {
			return err
		}
		b := historicalAPIRetirementBackend{historicalAcceptedScopedBackend{normalTailE2EBackend: f.native, parent: environmentCreateKubernetesBackend{execution: f.e, cfg: f.cfg, store: f.store}, parentSHA: f.e.Journal.PlanSHA256}}
		qualify := func(ctx context.Context, p *deploymentplan.Plan) error { return p.AdmitResumable(ctx, b, f.store.Root) }
		fence := func(ctx context.Context, p *deploymentplan.Plan) error {
			return b.CheckParentExecution(ctx, p, f.store.Root)
		}
		accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, b) }
		if err := executeEnvironmentCreateStagePlan(ctx, f.store, f.e, stage, p, b, qualify, fence, accept, f.e.Check, io.Discard); err != nil {
			return err
		}
		f.protocol["objects"] = historicalNativeInventory(f.native.objects)
		f.publish(t)
		return nil
	}
}

func TestHistoricalAPIRolloutAllowsRetainedStaleReplicaSet(t *testing.T) {
	f := newAcceptedCoreExecutionContractWithListeners(t, false, true)
	f.backend.RolloutTimeout = time.Minute
	installHistoricalAPIExportProtocol(t, f, map[string]historicalAPIIdentity{})
	objects := historicalNativeInventory(f.native.objects)
	ns := f.p.Plan.Stack + "-video-cloud"
	id := "Deployment/" + ns + "/pki-controller"
	owner := objects[id]
	if owner == nil {
		t.Fatal("current rollout owner unavailable")
	}
	ownerUID := deploymentplan.String(deploymentplan.Map(owner["metadata"])["uid"])
	var currentSet, currentPod deploymentplan.Object
	for _, object := range objects {
		if object["kind"] == "ReplicaSet" {
			for _, raw := range certIssuerObjectList(deploymentplan.Map(object["metadata"])["ownerReferences"]) {
				if deploymentplan.Map(raw)["uid"] == ownerUID {
					currentSet = object
				}
			}
		}
	}
	if currentSet == nil {
		t.Fatal("current rollout ReplicaSet unavailable")
	}
	setUID := deploymentplan.String(deploymentplan.Map(currentSet["metadata"])["uid"])
	for _, object := range objects {
		if object["kind"] != "Pod" {
			continue
		}
		for _, raw := range certIssuerObjectList(deploymentplan.Map(object["metadata"])["ownerReferences"]) {
			if deploymentplan.Map(raw)["uid"] == setUID {
				currentPod = object
			}
		}
	}
	if currentPod == nil {
		t.Fatal("current rollout Pod unavailable")
	}
	staleSet := deploymentplan.Clone(currentSet)
	staleSetMeta := deploymentplan.Map(staleSet["metadata"])
	staleSetMeta["name"], staleSetMeta["uid"] = "pki-controller-stale", "10101010-1010-4010-8010-101010101010"
	deploymentplan.Map(deploymentplan.Map(staleSet["spec"])["template"])["metadata"] = deploymentplan.Object{"labels": deploymentplan.Object{"historical": "stale"}}
	objects[deploymentplan.ID(staleSet)] = staleSet
	stalePod := deploymentplan.Clone(currentPod)
	stalePodMeta := deploymentplan.Map(stalePod["metadata"])
	stalePodMeta["name"], stalePodMeta["uid"] = "pki-controller-stale-pod", "20202020-2020-4020-8020-202020202020"
	stalePodMeta["ownerReferences"] = []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": staleSetMeta["name"], "uid": staleSetMeta["uid"], "controller": true}}
	objects[deploymentplan.ID(stalePod)] = stalePod
	f.protocol["objects"] = objects
	f.publish(t)
	target := deploymentplan.Resource{ID: id, UID: ownerUID, Desired: deploymentplan.Projection(owner), Action: "retain"}
	if err := f.backend.Wait(t.Context(), target); err != nil {
		t.Fatal("a retained stale ReplicaSet blocked the actually complete current rollout", err)
	}
}

func TestHistoricalAPIServerEnrollmentExportsSignedOriginalOwnerKeys(t *testing.T) {
	f, _, _, _ := newHistoricalTransportCoordinatorExecutionContract(t)
	// Establish the three API client owner claims through their actual
	// maintained enrollment producer before the native serving-key export.
	clients, err := renderEnvironmentCreateApplicationEnrollment(f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service)
	if err != nil {
		t.Fatal(err)
	}
	f.acceptedHistoricalStage(t, "isolated-original-api-client-enrollment", clients)
	identities := map[string]historicalAPIIdentity{}
	for _, name := range []string{"video-cloud-api-pki", "video-cloud-api-app-pki"} {
		identities[name] = historicalAPIServerIdentity(t, f, f.service, name)
	}
	installHistoricalAPIExportProtocol(t, f, identities)
	claims := map[string]string{}
	for _, owner := range environmentCreateApplicationOwners() {
		if _, ok := identities[owner.Name]; ok {
			id := "PersistentVolumeClaim/" + f.p.Plan.Stack + "-video-cloud/" + owner.Claim
			claims[id] = deploymentplan.Digest(f.native.objects[id])
		}
	}
	if err := publishEnvironmentCreateApplicationServers(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service, historicalAPIApply(t, f)); err != nil {
		t.Fatal("actual native API enrollment/export", err)
	}
	for name, identity := range identities {
		p, j, err := acceptedCreateStage(f.store, f.e, "kubernetes/server-secret-"+name)
		if err != nil || !j.Accepted || len(p.Resources) != 1 {
			t.Fatal("API serving Secret was not actually accepted", name, err)
		}
		data := deploymentplan.Map(p.Resources[0].Desired["data"])
		cert, err := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(data["tls.crt"]))
		if err != nil || string(cert) != identity.Material.Certificate {
			t.Fatal("native API server chain changed", name, err)
		}
		key, err := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(data["tls.key"]))
		if err != nil || string(key) != identity.Material.Key {
			t.Fatal("native API private owner changed", name, err)
		}
		id := "Deployment/" + f.p.Plan.Stack + "-video-cloud/pki-export-" + name + "-" + f.e.Journal.PlanSHA256[:12]
		if f.native.objects[id] != nil {
			t.Fatal("finished private exporter was not retired", id)
		}
	}
	for id, sha := range claims {
		if deploymentplan.Digest(f.native.objects[id]) != sha {
			t.Fatal("API export changed original owner claim", id)
		}
	}
}

// Resume consumes the actual accepted enrollment and custody child journals.
// Only the outer historical source admission is supplied at this selected
// producer boundary; current owners and native Job readiness remain real.
func TestHistoricalAPIServerResumesAcceptedEnrollmentAndCustody(t *testing.T) {
	f, _, _, _ := newHistoricalTransportCoordinatorExecutionContract(t)
	// This selected API boundary needs the same native PV-list protocol as
	// the full transport source, without requesting transport identity rows.
	installHistoricalTransportProtocol(t, f, historicalTransportIdentityObservations{})
	clients, err := renderEnvironmentCreateApplicationEnrollment(f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service)
	if err != nil {
		t.Fatal(err)
	}
	f.acceptedHistoricalStage(t, "isolated-original-api-client-enrollment", clients)
	identities := map[string]historicalAPIIdentity{}
	for _, name := range []string{"video-cloud-api-pki", "video-cloud-api-app-pki"} {
		identities[name] = historicalAPIServerIdentity(t, f, f.service, name)
	}
	installHistoricalAPIExportProtocol(t, f, identities)
	apply := historicalAPIApply(t, f)
	stop := errors.New("isolated actual API custody interruption")
	err = publishEnvironmentCreateApplicationServers(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service, func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		if err := apply(ctx, stage, defs); err != nil {
			return err
		}
		if stage == "server-custody-video-cloud-api-pki" {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) {
		t.Fatal("original custody boundary", err)
	}
	ledger := historicalTransportCaptureVolumes(t, f)
	if len(ledger.Volumes) == 0 {
		t.Fatal("original native claims missing")
	}
	owner := ledger.Volumes[0]
	claimID, volumeID := "PersistentVolumeClaim/"+owner.Namespace+"/"+owner.PVCName, "PersistentVolume//"+owner.PVName
	objects := f.protocol["objects"].(map[string]deploymentplan.Object)
	claimResource := deploymentplan.Resource{ID: claimID, UID: owner.PVCUID, Action: "retain", Desired: deploymentplan.Clone(objects[claimID])}
	if err := f.runtime.Backend.Wait(t.Context(), claimResource); err != nil {
		t.Fatal("actual native Bound claim", err)
	}
	originalClaim, originalVolume := deploymentplan.Clone(objects[claimID]), deploymentplan.Clone(objects[volumeID])
	for _, mode := range []string{"pending-claim", "missing-volume", "foreign-claim"} {
		switch mode {
		case "pending-claim":
			objects[claimID]["status"] = deploymentplan.Object{"phase": "Pending"}
		case "missing-volume":
			delete(objects, volumeID)
		case "foreign-claim":
			deploymentplan.Map(deploymentplan.Map(objects[volumeID]["spec"])["claimRef"])["uid"] = "foreign-native-owner"
		}
		f.publish(t)
		if err := f.runtime.Backend.Wait(t.Context(), claimResource); err == nil {
			t.Fatal("native claim wait accepted drift", mode)
		}
		objects[claimID], objects[volumeID] = deploymentplan.Clone(originalClaim), deploymentplan.Clone(originalVolume)
	}
	f.publish(t)
	retainedWrites := map[string]int{}
	post := &createManagedTransportPostManaged{ExportPlans: map[string]*deploymentplan.Plan{}, ExportJournals: map[string]deploymentplan.Journal{}}
	for _, stage := range []string{"server-enrollment-video-cloud-api-pki", "server-custody-video-cloud-api-pki"} {
		p, j, err := acceptedCreateStage(f.store, f.e, "kubernetes/"+stage)
		if err != nil {
			t.Fatal(err)
		}
		post.ExportPlans["kubernetes/"+stage], post.ExportJournals["kubernetes/"+stage] = p, j
		resource := p.Resources[0]
		retainedWrites[resource.ID] = f.native.writes[resource.ID]
		resource.UID, resource.Action = j.Owners[resource.ID], "retain"
		if err := f.runtime.Backend.Wait(t.Context(), resource); err != nil {
			t.Fatal("actual accepted native readiness", stage, err)
		}
		objects := f.protocol["objects"].(map[string]deploymentplan.Object)
		original := deploymentplan.Clone(objects[resource.ID])
		for _, mode := range []string{"missing", "incomplete", "terminal-failure"} {
			if mode == "missing" {
				delete(objects, resource.ID)
			} else {
				changed := deploymentplan.Clone(original)
				changed["status"] = deploymentplan.Object{}
				if mode == "terminal-failure" {
					changed["status"] = deploymentplan.Object{"failed": float64(1), "conditions": []any{deploymentplan.Object{"type": "Failed", "status": "True"}}}
				}
				objects[resource.ID] = changed
			}
			f.publish(t)
			if err := f.runtime.Backend.Wait(t.Context(), resource); err == nil {
				t.Fatal("native API readiness accepted drift", stage, mode)
			}
			objects[resource.ID] = deploymentplan.Clone(original)
		}
		f.publish(t)
	}
	runtime := f.runtime
	runtime.ServiceSessionRecovery = &createServiceSessionRecovery{ManagedTransportRecovery: &createManagedTransportRecovery{Partial: &createManagedTransportPartialRecovery{PostManaged: post}}}
	if err := publishEnvironmentCreateApplicationServers(t.Context(), f.cfg, f.p.Plan.Bootstrap, runtime, f.service, apply); err != nil {
		t.Fatal("actual accepted API continuation", err)
	}
	for id, writes := range retainedWrites {
		expected := writes
		// The custody reader has exactly one additional conditional retirement.
		if strings.HasPrefix(id, "Deployment/") {
			expected++
		}
		if f.native.writes[id] != expected {
			t.Fatal("accepted native producer replayed", id, f.native.writes[id], expected)
		}
	}
}
