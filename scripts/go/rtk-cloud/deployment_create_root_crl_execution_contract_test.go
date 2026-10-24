package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// crlExecutorBoundary emulates only the external kubectl protocol. The real
// executor owns admission, leases, signed CRL validation, journaling and recovery.
type crlExecutorBoundary struct {
	MountedDigests               string                `json:"mounted_digests,omitempty"`
	DeviceACKs                   []managedLatePriorACK `json:"device_acks,omitempty"`
	ProviderObservation          json.RawMessage       `json:"provider_observation,omitempty"`
	ProviderCRL                  string                `json:"provider_crl"`
	ProviderRotatedCRL           string                `json:"provider_rotated_crl"`
	ProviderTokenSHA             string                `json:"provider_token_sha"`
	ProviderCalls                []string              `json:"provider_calls"`
	ProviderFailure              string                `json:"provider_failure"`
	ProviderRotationResponseLost bool                  `json:"provider_rotation_response_lost"`

	FailWriteNumber int `json:"fail_write_number"`
	Reloads         int `json:"reloads"`

	Snapshots                                                   map[string]createRootCRLNativeSnapshot `json:"snapshots,omitempty"`
	Publications                                                map[string]json.RawMessage             `json:"publications,omitempty"`
	CacheFingerprints                                           map[string]string                      `json:"cache_fingerprints,omitempty"`
	Objects                                                     map[string]deploymentplan.Object       `json:"objects"`
	Snapshot                                                    createRootCRLNativeSnapshot            `json:"snapshot"`
	Signed                                                      createRootCRLRecord                    `json:"signed"`
	Request                                                     createRootCRLRequest                   `json:"request"`
	Fingerprint                                                 string                                 `json:"fingerprint"`
	Caches                                                      map[string]createRootCRLRecord         `json:"caches"`
	Imports, Installs, SecretWrites, LeaseCreates, LeaseDeletes int
	FailAfterImport                                             bool `json:"fail_after_import"`
	FailAfterSecret                                             bool `json:"fail_after_secret"`
	FailInstall                                                 bool `json:"fail_install"`
}

type crlExecutorFixture struct {
	cfg       deploymentConfig
	store     secretStore
	execution *environmentCreateExecution
	backend   environmentKubernetesBackend
	artifact  createRootCRLRecoveryArtifact
	statePath string
	custody   map[string][]byte
}

func (f *crlExecutorFixture) state(t *testing.T) crlExecutorBoundary {
	t.Helper()
	raw, err := os.ReadFile(f.statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state crlExecutorBoundary
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	return state
}
func (f *crlExecutorFixture) writeState(t *testing.T, state crlExecutorBoundary) {
	t.Helper()
	raw, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(f.statePath, raw, 0600); err != nil {
		t.Fatal(err)
	}
}
func newCRLExecutorFixture(t *testing.T) *crlExecutorFixture {
	t.Helper()
	cfg, store, operator, original := createStateFixture(t, "dev")
	execution, err := startEnvironmentCreateExecution(context.Background(), cfg, original, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { execution.Close() })
	root, key, snapshot, now := rootCRLFixture(t)
	request, err := buildCreateRootCRLRequest(root, snapshot, now)
	if err != nil {
		t.Fatal(err)
	}
	signed := signedRootCRLFixture(t, root, key, request)
	parent := execution.Journal.PlanSHA256
	plan := createRootCRLRecoveryPlan{Schema: createRootCRLRecoverySchema, Parent: parent, OwnerSHA: deploymentplan.Digest(execution.owner), JournalSHA: deploymentplan.Digest(execution.Journal), Candidate: "deployment/repairs/service-root-crl-executor", Root: root, Snapshot: snapshot, SnapshotSHA: deploymentplan.Digest(snapshot), Request: request, Signed: signed}
	custody := map[string][]byte{}
	for _, path := range []string{filepath.Join("pki/custody/roots", root.ID, "manifest.json"), filepath.Join("pki/custody/roots", root.ID, "ca-key.encrypted.pem"), filepath.Join("pki/custody/roots", root.ID, "certificate.pem"), filepath.Join("pki/custody/roots", root.ID, "csr.pem"), "pki/custody/service-root-v1-key-passphrase", filepath.Join("deployment/create", parent, "bootstrap/service/root-active.json")} {
		raw := []byte("isolated original custody: " + path)
		if strings.HasSuffix(path, "certificate.pem") {
			raw = []byte(root.Certificate)
		}
		if strings.HasSuffix(path, "root-active.json") {
			raw = mustProviderRecoveryJSON(root)
		}
		if err := store.write(path, raw, false); err != nil {
			t.Fatal(err)
		}
		custody[path] = raw
	}
	plan.Material, err = createRootCRLMaterial(store, parent, root)
	if err != nil {
		t.Fatal(err)
	}
	ns := cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud"
	state := crlExecutorBoundary{Objects: map[string]deploymentplan.Object{}, Snapshot: snapshot, Signed: signed, Request: request, Fingerprint: root.RootFingerprint, Caches: map[string]createRootCRLRecord{}}
	object := func(kind, name, uid string) deploymentplan.Object {
		return deploymentplan.Object{"apiVersion": "v1", "kind": kind, "metadata": deploymentplan.Object{"name": name, "namespace": ns, "uid": uid, "resourceVersion": "1"}}
	}
	before := object("Secret", "service-root-trust", "original-secret")
	before["data"] = deploymentplan.Object{"crl.pem": snapshot.Prior.PEM, "untouched": "preserved"}
	desired := deploymentplan.Clone(before)
	deploymentplan.Map(desired["data"])["crl.pem"] = signed.PEM
	plan.Secret = activeTransportRenewalResource{ID: deploymentplan.ID(before), UID: "original-secret", Before: deploymentplan.Projection(before), Desired: deploymentplan.Projection(desired)}
	state.Objects[deploymentplan.ID(before)] = before
	for i, name := range []string{"pki-controller", "certissuer"} {
		uid := fmt.Sprintf("original-deployment-%d", i)
		podName := name + "-original"
		body := object("Deployment", name, uid)
		body["spec"] = deploymentplan.Object{"replicas": float64(1)}
		claim := object("PersistentVolumeClaim", name+"-trust", "original-claim-"+name)
		pod := object("Pod", podName, "original-pod-"+name)
		target := createRootCRLStateTarget{ID: deploymentplan.ID(body), UID: uid, Desired: deploymentplan.Projection(body), Container: name, PodName: podName, PodUID: deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"]), ClaimID: deploymentplan.ID(claim), ClaimUID: deploymentplan.String(deploymentplan.Map(claim["metadata"])["uid"]), Paths: []string{"/var/lib/pki/root-crl.json", "/var/lib/pki/issuer-crl.json"}}
		plan.Targets = append(plan.Targets, target)
		for _, v := range []deploymentplan.Object{body, claim, pod} {
			state.Objects[deploymentplan.ID(v)] = v
		}
		for _, path := range target.Paths {
			state.Caches[podName+":"+path] = snapshot.Prior
		}
	}
	if err := store.write(filepath.Join(plan.Candidate, "signed/revocations.pem"), []byte(signed.PEM), false); err != nil {
		t.Fatal(err)
	}
	f := &crlExecutorFixture{cfg: cfg, store: store, execution: execution, backend: environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-kubeconfig"), RolloutTimeout: time.Second}, artifact: createRootCRLRecoveryArtifact{Schema: createRootCRLRecoverySchema, Plan: plan, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}, custody: custody}
	installCRLExecutorProtocol(t, f, state)
	return f
}

func installCRLExecutorProtocol(t *testing.T, f *crlExecutorFixture, state crlExecutorBoundary) {
	t.Helper()
	dir := t.TempDir()
	f.statePath = filepath.Join(dir, "state.json")
	f.writeState(t, state)
	script := `#!/usr/bin/env python3
import base64,hashlib,json,os,pathlib,re,subprocess,sys
p=pathlib.Path(os.environ['RTK_CRL_EXECUTOR_STATE'])
s=json.loads(p.read_text());a=sys.argv[1:]
while a and a[0].startswith('--'):
 if a[0] in ('--kubeconfig','--namespace'): a=a[2:]
 else: a=a[1:]
def save():p.write_text(json.dumps(s))
def emit(x):print(json.dumps(x))
def fail():save();sys.exit(1)
verb=a[0]
if verb=='get':
 ns=a[a.index('--namespace')+1] if '--namespace' in a else ''
 if a[1]=='namespace':
  emit({'items':[o for o in s['objects'].values() if o['kind']=='Namespace']});sys.exit(0)
 if ',' in a[1]:
  emit({'items':[o for o in s['objects'].values() if o['kind']!='Namespace' and o['metadata'].get('namespace','')==ns]});sys.exit(0)
 if a[1]=='validatingwebhookconfiguration':emit({'items':[]});sys.exit(0)
 obj=s['objects'].get(a[1]+'/'+ns+'/'+a[2])
 if obj is not None:emit(obj)
 sys.exit(0)
if verb in ('create','replace'):
 obj=json.load(sys.stdin);m=obj['metadata'];key=obj['kind']+'/'+m.get('namespace','')+'/'+m['name']
 dry='--dry-run=server' in a
 if not dry:
  m.setdefault('uid','00000000-0000-4000-8000-%012d'%(s['LeaseCreates']+1));m['resourceVersion']=str(int(m.get('resourceVersion','0'))+1)
  s['objects'][key]=obj
  if obj['kind']=='Lease':s['LeaseCreates']+=1
  else:s['SecretWrites']+=1
  save()
  if obj['kind']!='Lease' and s['fail_write_number']>0 and s['SecretWrites']==s['fail_write_number']:fail()
  if obj['kind']=='Secret' and s['fail_after_secret']:s['fail_after_secret']=False;fail()
 emit(obj);sys.exit(0)
if verb=='delete':
 request=json.load(sys.stdin);keys=[k for k in s['objects'] if k.startswith('Lease/')]
 assert len(keys)==1
 obj=s['objects'][keys[0]];pre=request['preconditions']
 assert pre['uid']==obj['metadata']['uid'] and pre['resourceVersion']==obj['metadata']['resourceVersion']
 del s['objects'][keys[0]];s['LeaseDeletes']+=1;save();emit({'status':'Success'});sys.exit(0)
if verb in ('rollout','wait'):sys.exit(0)
if verb=='exec':
 if a[-1].endswith('\nsha256sum /openbao/transport/tls.crt /openbao/transport/ca.crt /openbao/transport/tls.key'):print(s['mounted_digests']);sys.exit(0)
 if 'bao read ' in a[-1]:
  assert hashlib.sha256(sys.stdin.read().strip().encode()).hexdigest()==s['provider_token_sha']
  op='issuer' if '/issuer/default' in a[-1] else 'rotate' if '/crl/rotate' in a[-1] else 'current' if '/cert/crl' in a[-1] else None
  assert op is not None
  s['provider_calls']=(s['provider_calls'] or [])+[op];save()
  if s['provider_failure']==op:sys.exit(1)
  if op=='issuer':emit(s['provider_observation']);sys.exit(0)
  if op=='rotate':
   assert s['provider_calls'].count('rotate')==1
   if os.environ.get('RTK_CRL_DEVICE_SIGNER')=='1':
    subprocess.check_call([os.environ['RTK_CRL_DEVICE_BINARY'],'-test.run=^TestCRLExecutionContractDeviceSignerProcess$'],stdout=subprocess.DEVNULL)
    s=json.loads(p.read_text())
   s['provider_crl']=s['provider_rotated_crl'];save()
   if s['provider_rotation_response_lost']:sys.exit(1)
   emit({'data':{'success':True}});sys.exit(0)
  print(s['provider_crl'],end='');sys.exit(0)
 if 'kill -HUP 1' in ' '.join(a):s['reloads']+=1;save();sys.exit(1)
 if 'registry-read' in a:
  assert 'default_transaction_read_only=on' in ' '.join(a) and a[-1].startswith('SELECT ')
  issuer=re.search("issuer_id='([^']+)'",a[-1]).group(1)
  if a[-1].startswith("SELECT COALESCE(json_agg(json_build_object('issuer_id'"):emit(s['device_acks'])
  elif a[-1].startswith("SELECT json_build_object('imports'"):emit(s['publications'][issuer])
  else:emit(s.get('snapshots',{}).get(issuer,s['snapshot']))
  sys.exit(0)
 if 'reviewed-root-crl' in a:
  inp=sys.stdin.read().splitlines();request=json.loads(base64.b64decode(inp[0]));signed=base64.b64decode(inp[1]).decode()
  assert request==s['request'] and signed==s['signed']['crl_pem']
  if '--confirm dev' in ' '.join(a):
   s['snapshot']['prior']=s['signed'];s['Imports']+=1;save()
   if s['fail_after_import']:s['fail_after_import']=False;fail()
  emit({'imported':True});sys.exit(0)
 pod=next((x[4:] for x in a if x.startswith('pod/')),'')
 if 'crl-state' in a:
  assert a[-2]=='crl-state' and '--container' in a
  native=next((o for o in s['objects'].values() if o['kind']=='Pod' and o['metadata']['name']==pod),None)
  assert native is not None and native['metadata'].get('uid')
  script=a[a.index('-ec')+1]
  assert 'test ! -L' in script and 'stat -c %a' in script and '600' in script and 'stat -c %u' in script and '10001' in script
  cache=pod+':'+a[-1];assert cache in s['caches']
  emit({'issuer_fingerprint':s.get('cache_fingerprints',{}).get(cache,s['fingerprint']),'crl':s['caches'][cache],'terminal_issuer_id':''});sys.exit(0)
 if 'cat' in a or 'mqtt-root-crl-native-state' in a or 'openbao-tls-root-crl-native-state' in a or 'device-root-crl-native-state' in a:
  cache=pod+':'+a[-1]
  emit({'issuer_fingerprint':s.get('cache_fingerprints',{}).get(cache,s['fingerprint']),'crl':s['caches'][cache],'terminal_issuer_id':''});sys.exit(0)
 if 'test -x /app/pkitrust' in a:sys.exit(0)
 if 'reviewed-root-crl-state' in a:
  inp=sys.stdin.read().splitlines();record=json.loads(base64.b64decode(inp[1]));assert record==s['signed']
  if s['fail_install']:fail()
  for path in a[-2:]:s['caches'][pod+':'+path]=record
  s['Installs']+=1;save();emit({'installed':True});sys.exit(0)
raise AssertionError('unhandled external protocol: '+str(a))
`
	bin := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(bin, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CRL_EXECUTOR_STATE", f.statePath)
	t.Setenv("RTK_CLOUD_KUBECTL", bin)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestCRLExecutionContractServicePublicationAndResume(t *testing.T) {
	for _, interruption := range []string{"none", "after-import", "after-secret", "state-install"} {
		t.Run(interruption, func(t *testing.T) {
			f := newCRLExecutorFixture(t)
			state := f.state(t)
			state.FailAfterImport = interruption == "after-import"
			state.FailAfterSecret = interruption == "after-secret"
			state.FailInstall = interruption == "state-install"
			f.writeState(t, state)
			var out bytes.Buffer
			err := executeCreateRootCRLRecovery(context.Background(), f.cfg, f.store, f.execution, f.backend, f.artifact, &out)
			if interruption != "none" {
				if err == nil {
					t.Fatal("injected interrupted native operation was reported complete")
				}
				if _, err := f.store.read(filepath.Join("deployment/create/material", f.artifact.Plan.Parent, "service-root-crl-renewal.json")); !os.IsNotExist(err) {
					t.Fatal("interruption published completion", err)
				}
				state = f.state(t)
				state.FailInstall = false
				f.writeState(t, state)
				if err := executeCreateRootCRLRecovery(context.Background(), f.cfg, f.store, f.execution, f.backend, f.artifact, &out); err != nil {
					t.Fatal("resume", err)
				}
			} else if err != nil {
				t.Fatal(err)
			}
			state = f.state(t)
			if state.Imports != 1 || state.SecretWrites != 1 || state.Installs != 2 || state.LeaseCreates != state.LeaseDeletes {
				t.Fatalf("publication duplicated, cache omitted, or lease leaked: %+v", struct{ Imports, SecretWrites, Installs, LeaseCreates, LeaseDeletes int }{state.Imports, state.SecretWrites, state.Installs, state.LeaseCreates, state.LeaseDeletes})
			}
			if !strings.Contains(out.String(), "original-root-preserved=true") || len(f.execution.Journal.Entries) != 5 {
				t.Fatal("missing genuine complete executor evidence", out.String(), len(f.execution.Journal.Entries))
			}
			for _, entry := range f.execution.Journal.Entries {
				if entry.State != "completed" || len(entry.Receipt) == 0 {
					t.Fatal("unproven stage completed", entry.Key)
				}
			}
			for path, want := range f.custody {
				got, err := os.ReadFile(filepath.Join(f.store.Root, path))
				if err != nil || !bytes.Equal(got, want) {
					t.Fatal("original custody changed", path, err)
				}
			}
			for _, target := range f.artifact.Plan.Targets {
				for _, path := range target.Paths {
					if deploymentplan.Digest(state.Caches[target.PodName+":"+path]) != deploymentplan.Digest(f.artifact.Plan.Signed) {
						t.Fatal("signed CRL cache missing", target.ID, path)
					}
				}
			}
			// A repeated invocation verifies the same outcome without republishing it.
			if err := executeCreateRootCRLRecovery(context.Background(), f.cfg, f.store, f.execution, f.backend, f.artifact, &out); err != nil {
				t.Fatal("repeat", err)
			}
			repeated := f.state(t)
			if repeated.Imports != 1 || repeated.SecretWrites != 1 || repeated.Installs != 2 {
				t.Fatal("completed recovery replayed native writes")
			}
		})
	}
}

func TestCRLExecutionContractServiceTamperPreservesOriginal(t *testing.T) {
	for _, mode := range []string{"signed-bytes", "custody", "live-revocations", "deployment-owner", "claim-owner", "pod-owner", "secret-owner"} {
		t.Run(mode, func(t *testing.T) {
			f := newCRLExecutorFixture(t)
			s := f.state(t)
			p := f.artifact.Plan
			switch mode {
			case "signed-bytes":
				if err := os.WriteFile(filepath.Join(f.store.Root, p.Candidate, "signed/revocations.pem"), []byte("tampered"), 0600); err != nil {
					t.Fatal(err)
				}
			case "custody":
				if err := os.WriteFile(filepath.Join(f.store.Root, "pki/custody/service-root-v1-key-passphrase"), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			case "live-revocations":
				s.Snapshot.Bindings = append(s.Snapshot.Bindings, createRootCRLRevocation{"65", time.Now().Add(-time.Hour), 0})
			case "deployment-owner":
				deploymentplan.Map(s.Objects[p.Targets[0].ID]["metadata"])["uid"] = "replacement"
			case "claim-owner":
				deploymentplan.Map(s.Objects[p.Targets[0].ClaimID]["metadata"])["uid"] = "replacement"
			case "pod-owner":
				deploymentplan.Map(s.Objects["Pod/"+f.cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud/"+p.Targets[0].PodName]["metadata"])["uid"] = "replacement"
			case "secret-owner":
				deploymentplan.Map(s.Objects[p.Secret.ID]["metadata"])["uid"] = "replacement"
			}
			f.writeState(t, s)
			if err := executeCreateRootCRLRecovery(context.Background(), f.cfg, f.store, f.execution, f.backend, f.artifact, &bytes.Buffer{}); err == nil {
				t.Fatal("changed selected authority accepted")
			}
			after := f.state(t)
			if after.SecretWrites != 0 || after.Installs != 0 || after.LeaseCreates != after.LeaseDeletes {
				t.Fatal("rejected recovery overwrote resources or leaked lease")
			}
			if mode != "secret-owner" && after.Imports != 0 {
				t.Fatal("owner/custody drift allowed native publication")
			}
			if _, err := f.store.read(filepath.Join("deployment/create/material", p.Parent, "service-root-crl-renewal.json")); !os.IsNotExist(err) {
				t.Fatal("tampered recovery published completion", err)
			}
		})
	}
}
