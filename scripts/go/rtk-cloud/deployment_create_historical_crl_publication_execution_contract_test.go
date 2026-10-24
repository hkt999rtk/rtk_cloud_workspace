package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The external registry and consumers process only the actual signed request
// presented to the native importer. Production keeps source authentication,
// admission, lease ownership, durable receipts and independent final readback.
func installHistoricalRootCRLPublicationProtocol(t *testing.T, f *acceptedCoreExecutionContract, root environmentCreateRegisteredIssuer, request createRootCRLRequest, signed createRootCRLRecord, targets []createRootCRLStateTarget) func() {
	t.Helper()
	original, originalPath := os.Getenv("RTK_CLOUD_KUBECTL"), os.Getenv("PATH")
	if original == "" || len(targets) != 2 || f.protocol["historical_crls"] == nil {
		t.Fatal("native CRL import requires actual original registry and two consumers")
	}
	dir := t.TempDir()
	statePath := filepath.Join(dir, "native-import.json")
	state := map[string]any{"root": root, "request": request, "request_sha": deploymentplan.Digest(request), "signed": signed, "targets": targets, "leases": map[string]any{}, "imports": 0, "lease_creates": 0, "lease_deletes": 0}
	if err := os.WriteFile(statePath, mustProviderRecoveryJSON(state), 0600); err != nil {
		t.Fatal(err)
	}
	literal := func(value string) string { raw, _ := json.Marshal(value); return string(raw) }
	script := `#!/usr/bin/env python3
import base64,datetime,hashlib,json,os,sys
state_path=` + literal(statePath) + `
original=` + literal(original) + `
with open(state_path) as source:s=json.load(source)
native_path=os.environ['RTK_CORE_CONTRACT_NATIVE']
with open(native_path) as source:d=json.load(source)
a=sys.argv[1:];query=a[-1] if a else '';raw=list(a)
while a and a[0].startswith('--'):
 if a[0] in ('--kubeconfig','--namespace'):a=a[2:]
 else:a=a[1:]
def save():
 with open(state_path,'w') as target:json.dump(s,target)
 with open(native_path,'w') as target:json.dump(d,target)
def emit(value):print(json.dumps(value))
if a[:1]==['get'] and a[1] in ('Lease','lease'):
 ns=raw[raw.index('--namespace')+1] if '--namespace' in raw else ''
 key='Lease/'+ns+'/'+a[2]
 if key in s['leases']:emit(s['leases'][key])
 sys.exit(0)
if a[:1] in (['create'],['replace']):
 body=json.load(sys.stdin)
 if body['kind']!='Lease':sys.exit('publication cannot mutate Kubernetes workload owners')
 meta=body['metadata'];key='Lease/'+meta.get('namespace','')+'/'+meta['name'];old=s['leases'].get(key)
 if '--dry-run=server' not in a:
  if a[0]=='create':
   if old is not None:sys.exit('native lease already exists')
   s['lease_creates']+=1;sha=hashlib.sha256((key+s['root']['ID']+str(s['lease_creates'])).encode()).hexdigest()
   meta['uid']=sha[:8]+'-'+sha[8:12]+'-4'+sha[13:16]+'-8'+sha[17:20]+'-'+sha[20:32];meta['resourceVersion']='1'
  else:
   if old is None or meta.get('uid')!=old['metadata']['uid'] or meta.get('resourceVersion')!=old['metadata']['resourceVersion']:sys.exit('native lease owner/version changed')
   meta['resourceVersion']=str(int(meta['resourceVersion'])+1)
  s['leases'][key]=body;save()
 emit(body);sys.exit(0)
if a[:1]==['delete']:
 body=json.load(sys.stdin)
 if len(s['leases'])!=1:sys.exit('exact original publication lease required')
 key=next(iter(s['leases']));old=s['leases'][key]['metadata']
 if body.get('preconditions')!={'uid':old['uid'],'resourceVersion':old['resourceVersion']}:sys.exit('native lease release precondition differs')
 del s['leases'][key];s['lease_deletes']+=1;save();emit({'status':'Success'});sys.exit(0)
if 'registry-read' in a and query.startswith("SELECT json_build_object('imports'"):
 ids=set(__import__('re').findall("issuer_id='([^']+)'",query))
 if ids=={s['root']['ID']}:
  publication=d.get('historical_crl_publications',{}).get(s['root']['ID'])
  if publication is None:sys.exit('native import event does not exist')
  emit(publication);sys.exit(0)
if 'reviewed-root-crl' in a:
 at=a.index('reviewed-root-crl')
 if a[at+1:]!=[s['root']['ID'],s['root']['RootFingerprint'],s['request_sha']]:sys.exit('native original authority/request selection differs')
 rows=sys.stdin.read().splitlines()
 if len(rows)!=2 or json.loads(base64.b64decode(rows[0]))!=s['request'] or base64.b64decode(rows[1]).decode()!=s['signed']['crl_pem']:sys.exit('native signed payload differs')
 execute='--confirm dev' in ' '.join(a)
 if execute:
  if s['imports']!=0:sys.exit('native original request imported twice')
  s['imports']=1;now=datetime.datetime.now(datetime.timezone.utc).isoformat().replace('+00:00','Z')
  issuer=s['root']['ID'];history=d.setdefault('historical_crl_history',{}).setdefault(issuer,[])
  if any(row['crl_sha256']==s['signed']['crl_sha256'] for row in history):sys.exit('native signed request already exists in CRL history')
  history.append(s['signed']);history.sort(key=lambda row:int(row['crl_number']));d['historical_crls'][issuer]=s['signed'];acks=[]
  for target in s['targets']:
   cache=d['historical_crl_caches'].get(target['claim_uid']+':'+target['paths'][0])
   if cache is None or cache['uid']!=10001 or cache['mode']!='600':sys.exit('actual original consumer cache missing')
   with open(cache['file']) as source:body=json.load(source)
   if body['issuer_fingerprint']!=s['root']['RootFingerprint'] or body['crl']['issuer_id']!=issuer:sys.exit('consumer changed original authority')
   body['crl']=s['signed']
   with open(cache['file'],'w') as output:json.dump(body,output,separators=(',',':'))
   cache['digest']=s['signed']['crl_sha256']
   acks.append({'issuer_id':issuer,'digest':s['signed']['crl_sha256'],'consumer_id':target['desired']['metadata']['name'],'acknowledged_at':now})
  acks.sort(key=lambda item:item['consumer_id'])
  d['historical_crl_acks']=[row for row in d['historical_crl_acks'] if row['issuer_id']!=issuer]+acks
  d.setdefault('historical_crl_publications',{})[issuer]={'imports':1,'imported_at':now,'acks':acks};save()
  print('imported dev '+s['root']['Domain']+' Root CRL #2 digest '+s['signed']['crl_sha256']+'; reviewed request '+s['request_sha'])
 else:print('validated dev '+s['root']['Domain']+' Root CRL #2 through '+s['request']['next_update']+'; no changes made; reviewed request '+s['request_sha'])
 sys.exit(0)
os.execv(original,[original]+raw)
`
	bin := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", bin)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+originalPath)
	return func() {
		var native struct {
			Records      map[string]managedLateCRLRecord   `json:"historical_crls"`
			History      map[string][]managedLateCRLRecord `json:"historical_crl_history"`
			ACKs         []managedLatePriorACK             `json:"historical_crl_acks"`
			Caches       map[string]map[string]any         `json:"historical_crl_caches"`
			Publications map[string]any                    `json:"historical_crl_publications"`
		}
		raw, err := readBoundedEnvironmentFile(os.Getenv("RTK_CORE_CONTRACT_NATIVE"), true)
		if err != nil || json.Unmarshal(raw, &native) != nil {
			t.Fatal("native protocol readback unavailable", err)
		}
		f.protocol["historical_crls"], f.protocol["historical_crl_history"], f.protocol["historical_crl_acks"], f.protocol["historical_crl_caches"], f.protocol["historical_crl_publications"] = native.Records, native.History, native.ACKs, native.Caches, native.Publications
		f.publish(t)
		t.Setenv("RTK_CLOUD_KUBECTL", original)
		t.Setenv("PATH", originalPath)
	}
}

// This checks the native command boundary only. Full late-source qualification
// still belongs to the connected Core/Apps/transport fixture.
func exerciseHistoricalRootCRLNativeImport(t *testing.T, f *acceptedCoreExecutionContract, root environmentCreateRegisteredIssuer, prior, signed createRootCRLRecord, request createRootCRLRequest) {
	t.Helper()
	objects := historicalNativeInventory(f.native.objects)
	ns := f.p.Plan.Stack + "-video-cloud"
	caches := map[string]map[string]any{}
	var targets []createRootCRLStateTarget
	for _, name := range []string{"certissuer", "pki-controller"} {
		id := "Deployment/" + ns + "/" + name
		owner := objects[id]
		claimID := "PersistentVolumeClaim/" + ns + "/" + environmentCreateServiceOwnerClaim(name)
		claim := objects[claimID]
		if owner == nil || claim == nil {
			t.Fatal("actual original native import owner missing", id)
		}
		target := createRootCRLStateTarget{ID: id, UID: deploymentplan.String(deploymentplan.Map(owner["metadata"])["uid"]), Desired: deploymentplan.Projection(owner), Container: name, ClaimID: claimID, ClaimUID: deploymentplan.String(deploymentplan.Map(claim["metadata"])["uid"]), Paths: []string{environmentCreateServiceState + "/private/crl/isolated-import.json"}}
		for _, pod := range objects {
			if pod["kind"] != "Pod" {
				continue
			}
			controller, err := environmentPodController(pod, objects)
			if err == nil && controller == id {
				target.PodName = deploymentplan.String(deploymentplan.Map(pod["metadata"])["name"])
				target.PodUID = deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"])
			}
		}
		if target.PodName == "" {
			t.Fatal("actual owned native import Pod missing")
		}
		file := filepath.Join(t.TempDir(), "cache.json")
		if err := os.WriteFile(file, mustProviderRecoveryJSON(map[string]any{"issuer_fingerprint": root.RootFingerprint, "crl": prior}), 0600); err != nil {
			t.Fatal(err)
		}
		caches[target.ClaimUID+":"+target.Paths[0]] = map[string]any{"file": file, "uid": 10001, "mode": "600", "digest": prior.Digest}
		targets = append(targets, target)
	}
	f.protocol["historical_crls"] = map[string]managedLateCRLRecord{root.ID: {prior.IssuerID, prior.Digest, prior.Number, prior.PEM, prior.ThisUpdate, prior.NextUpdate}}
	f.protocol["historical_crl_acks"], f.protocol["historical_crl_caches"] = []managedLatePriorACK{}, caches
	f.publish(t)
	finish := installHistoricalRootCRLPublicationProtocol(t, f, root, request, signed, targets)
	defer finish()
	assertCustody := func(stage string) {
		t.Helper()
		for path, want := range map[string]string{
			"/run/pki-bootstrap/bootstrap.key": f.key,
			"/run/pki-bootstrap/bootstrap.csr": f.csr,
		} {
			raw, err := f.backend.command(t.Context(), nil, "--namespace", f.service.BootstrapCredentialNamespace, "exec", "deployment/"+f.service.CustodyReader, "--container", "custody", "--", "cat", path)
			if err != nil || string(raw) != want {
				t.Fatal(stage, "CRL wrapper did not transparently retain original custody", path, err)
			}
		}
	}
	assertCustody("before lease")
	plan := createRootCRLRecoveryPlan{Root: root, Request: request, Signed: signed, Targets: targets}
	lease, err := f.backend.AcquireExecution(t.Context(), rootCRLLeasePlan(f.cfg, deploymentplan.Digest(plan)))
	if err != nil {
		t.Fatal("native publication lease acquisition", err)
	}
	defer func() {
		if err := lease.Release(t.Context()); err != nil {
			t.Fatal("native publication lease release", err)
		}
	}()
	if !environmentBootstrapOwnerUUID.MatchString(lease.Owner().UID) || lease.Check(t.Context()) != nil {
		t.Fatal("native publication lease ownership was not observed")
	}
	assertCustody("after lease")
	for _, execute := range []bool{false, true} {
		raw, err := runNativeCreateRootCRLImport(t.Context(), f.cfg, f.backend, plan, execute)
		if err != nil {
			t.Fatal("native exact signed import command", err)
		}
		if root.Domain == "openbao_tls" && !validateOpenBaoTLSRootCRLNativeResult(raw, request.NextUpdate, deploymentplan.Digest(request), signed.Digest, execute) {
			t.Fatal("native TLS importer result differs from maintained protocol")
		}
		assertCustody(map[bool]string{false: "after dry run", true: "after import"}[execute])
		for _, cache := range caches {
			var body struct {
				Fingerprint string              `json:"issuer_fingerprint"`
				CRL         createRootCRLRecord `json:"crl"`
			}
			if readExecutionJSON(cache["file"].(string), &body) != nil || body.CRL.Digest != map[bool]string{false: prior.Digest, true: signed.Digest}[execute] {
				t.Fatal("native dry-run changed a cache or actual import did not install signed bytes")
			}
		}
	}
}
