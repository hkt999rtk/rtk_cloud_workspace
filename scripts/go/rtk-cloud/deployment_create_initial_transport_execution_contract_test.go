package main

import (
	"context"
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

type initialTransportExecutionContract struct {
	cfg      deploymentConfig
	store    secretStore
	e        *environmentCreateExecution
	backend  environmentKubernetesBackend
	plan     *deploymentplan.Plan
	journal  deploymentplan.Journal
	original map[string]environmentCreateTransport
	state    string
}

type initialTransportNativeState struct {
	Objects     map[string]deploymentplan.Object `json:"objects"`
	Mode        string                           `json:"mode"`
	Writes      int                              `json:"writes"`
	Leases      int                              `json:"leases"`
	Releases    int                              `json:"releases"`
	Reloads     int                              `json:"reloads"`
	IssuerReads int                              `json:"issuer_reads"`
}

// This is the before-managed-issuers executor boundary. The ten original
// projections and initializer receipts are produced through their real writers;
// no public admission or later managed-identity history is asserted.
func newInitialTransportExecutionContract(t *testing.T, mode string) *initialTransportExecutionContract {
	t.Helper()
	cfg, store, backend, e, _ := createOpenBaoNativeFixture(t, "valid")
	original := map[string]environmentCreateTransport{}
	for _, role := range []string{"management", "openbao"} {
		names := map[string][]string{"openbao": {"openbao." + e.Journal.Stack + "-secrets.svc"}}
		if role == "management" {
			names = map[string][]string{}
			for i := 0; i < 7; i++ {
				name := fmt.Sprintf("original-client-%d", i)
				names[name] = []string{name + ".isolated.svc"}
			}
		}
		// The initializer's narrow fixture starts with an unbound CA placeholder.
		// Select real transport bytes before its first initialization or receipt.
		isolated := secretStore{Environment: "dev", Root: filepath.Join(t.TempDir(), "dev")}
		if err := os.MkdirAll(isolated.Root, 0700); err != nil {
			t.Fatal(err)
		}
		material, err := createEnvironmentTransport(isolated, e.Journal.PlanSHA256, role, names, time.Now().UTC())
		if err != nil {
			t.Fatal(err)
		}
		base := filepath.Join("deployment/create/material", e.Journal.PlanSHA256, role+"-transport")
		if err := store.write(filepath.Join(base, "identities.json"), mustProviderRecoveryJSON(material), false); err != nil {
			t.Fatal(err)
		}
		if err := store.write(filepath.Join(base, "ca.crt"), []byte(material.CACert), role == "openbao"); err != nil {
			t.Fatal(err)
		}
		original[role] = material
	}
	if _, err := initializeEnvironmentCreateOpenBao(t.Context(), cfg, store, backend, e); err != nil {
		t.Fatal("actual original initializer", err)
	}
	parent := filepath.Join(os.Getenv("RTK_CREATE_BAO_DIRECTORY"), "kubectl")
	var defs []deploymentplan.Definition
	add := func(kind, name, ns string) {
		body := deploymentplan.Object{"apiVersion": "v1", "kind": kind, "metadata": deploymentplan.Object{"name": name, "namespace": ns}, "data": deploymentplan.Object{}}
		body, changed := initialTransportOverride(body, original)
		if !changed {
			t.Fatal("unmodeled original projection", name)
		}
		defs = append(defs, deploymentplan.Definition{Phase: "configuration", Object: body})
	}
	for i := 0; i < 7; i++ {
		add("Secret", fmt.Sprintf("pki-create-original-client-%d-transport", i), e.Journal.Stack+"-video-cloud")
	}
	add("Secret", "openbao-transport-tls", e.Journal.Stack+"-secrets")
	add("ConfigMap", "pki-create-transport-trust", e.Journal.Stack+"-video-cloud")
	add("ConfigMap", "pki-openbao-transport-ca", e.Journal.Stack+"-video-cloud")
	stage := "initial-transport-foundation"
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest("original-transport-release"), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs)}, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	native := freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}}}
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, plan, native, func(ctx context.Context, p *deploymentplan.Plan) error {
		return p.AdmitResumable(ctx, native, store.Root)
	}, func(ctx context.Context, p *deploymentplan.Plan) error {
		return native.CheckParentExecution(ctx, p, store.Root)
	}, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, native) }, func(ctx context.Context) error { return e.Check(ctx) }, io.Discard); err != nil {
		t.Fatal(err)
	}
	_, journal, err := acceptedCreateStage(store, e, "kubernetes/"+stage)
	if err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "native.json")
	objects := map[string]deploymentplan.Object{}
	for id, body := range native.objects {
		objects[id] = deploymentplan.Clone(body)
	}
	first := plan.Resources[0].ID
	switch mode {
	case "foreign-uid":
		deploymentplan.Map(objects[first]["metadata"])["uid"] = "00000000-0000-4000-8000-999999999999"
	case "foreign-body":
		deploymentplan.Map(objects[first]["data"])["ca.crt"] = "foreign"
	}
	if err := os.WriteFile(state, mustProviderRecoveryJSON(map[string]any{"objects": objects, "mode": mode, "writes": 0, "leases": 0, "releases": 0, "reloads": 0, "issuer_reads": 0}), 0600); err != nil {
		t.Fatal(err)
	}
	script := `#!/usr/bin/env python3
import base64,hashlib,json,os,sys,uuid
p=os.environ['RTK_INITIAL_RENEWAL_NATIVE'];a=sys.argv[1:]
with open(p) as f:d=json.load(f)
b=list(a)
while b and b[0].startswith('--'):
 if b[0] in ('--kubeconfig','--namespace'):b=b[2:]
 else:b=b[1:]
def save():
 with open(p,'w') as f:json.dump(d,f)
def emit(o):print(json.dumps(o))
def sha(s):return hashlib.sha256(s).hexdigest()
verb=b[0]
if verb=='get':
 ns=b[b.index('--namespace')+1] if '--namespace' in b else ''
 key=b[1]+'/'+ns+'/'+b[2]
 if b[1]=='Lease' or key in d['objects']:
  if key in d['objects']:emit(d['objects'][key])
  sys.exit()
if verb in ('create','replace'):
 o=json.load(sys.stdin);m=o['metadata'];key=o['kind']+'/'+m.get('namespace','')+'/'+m['name'];dry='--dry-run=server' in b
 old=d['objects'].get(key)
 if verb=='replace':
  assert old and m['uid']==old['metadata']['uid'] and m['resourceVersion']==old['metadata']['resourceVersion']
 else:assert old is None and o['kind']=='Lease'
 if not dry:
  m.setdefault('uid',str(uuid.uuid4()));m['resourceVersion']=str(int(m.get('resourceVersion','0'))+1)
  d['objects'][key]=o
  if o['kind']=='Lease':d['leases']+=1
  else:d['writes']+=1
  save()
  if o['kind']!='Lease' and d['mode']=='response-lost' and d['writes']==4:sys.exit(1)
 emit(o);sys.exit()
if verb=='delete':
 request=json.load(sys.stdin);leases=[k for k in d['objects'] if k.startswith('Lease/')];assert len(leases)==1
 key=leases[0];m=d['objects'][key]['metadata'];pre=request['preconditions']
 expected='--raw=/apis/coordination.k8s.io/v1/namespaces/'+m['namespace']+'/leases/'+m['name']
 assert b==['delete',expected,'-f','-'] and request['apiVersion']=='v1' and request['kind']=='DeleteOptions'
 assert pre['uid']==m['uid'] and pre['resourceVersion']==m['resourceVersion']
 del d['objects'][key];d['releases']+=1;save();emit({'status':'Success'});sys.exit()
joined=' '.join(a)
if 'kill -HUP 1' in joined:
 o=next(o for o in d['objects'].values() if o['kind']=='Secret' and o['metadata']['name']=='openbao-transport-tls')
 cert=sha(base64.b64decode(o['data']['tls.crt']));ca=sha(base64.b64decode(o['data']['ca.crt']))
 assert cert in a[-1] and ca in a[-1] and '/openbao/transport/tls.crt' in a[-1] and '/openbao/transport/ca.crt' in a[-1]
 assert '--namespace' in a and a[a.index('--namespace')+1]==o['metadata']['namespace'] and 'openbao-0' in a and '--container' in a and a[a.index('--container')+1]=='openbao'
 d['reloads']+=1;save()
 sys.exit(1 if d['mode']=='reload-response-lost' else 0)
if 'SELECT count(*) FROM pki_issuers' in joined:
 assert '-n' in a and a[a.index('-n')+1]==STACK+'-platform' and 'postgresql-0' in a and 'postgres' in a
 d['issuer_reads']+=1;save();print('1' if d['mode']=='managed-issuer-present' else '0');sys.exit()
os.execv(PARENT,[PARENT]+a)
`
	script = strings.ReplaceAll(script, "PARENT", strconv.Quote(parent))
	script = strings.ReplaceAll(script, "STACK", strconv.Quote(e.Journal.Stack))
	dir := t.TempDir()
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_INITIAL_RENEWAL_NATIVE", state)
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &initialTransportExecutionContract{cfg: cfg, store: store, e: e, backend: backend, plan: plan, journal: journal, original: original, state: state}
}

func TestInitialTransportExecutionPreservesOriginalKeysAndRefusesUncertainReplay(t *testing.T) {
	for _, mode := range []string{"complete", "foreign-uid", "foreign-body", "response-lost", "reload-response-lost", "managed-issuer-present"} {
		t.Run(mode, func(t *testing.T) {
			f := newInitialTransportExecutionContract(t, mode)
			before := createBootstrapStateSnapshot(t, f.store.Root)
			review := deploymentplan.Digest("isolated-initial-transport-" + mode)
			err := renewEnvironmentInitialTransports(t.Context(), f.cfg, f.store, f.e, f.backend, f.plan, f.journal, review)
			var native initialTransportNativeState
			if readErr := readExecutionJSON(f.state, &native); readErr != nil {
				t.Fatal(readErr)
			}
			wantWrites := 10
			if mode == "foreign-uid" || mode == "foreign-body" {
				wantWrites = 0
			}
			if mode == "response-lost" {
				wantWrites = 4
			}
			if native.Writes != wantWrites || native.Leases != native.Releases {
				t.Fatal("unexpected native write count or retained lease", native.Writes, native.Leases, native.Releases, err)
			}
			wantReloads, wantIssuerReads := 0, 0
			if mode == "complete" || mode == "reload-response-lost" || mode == "managed-issuer-present" {
				wantReloads = 1
			}
			if mode == "complete" || mode == "managed-issuer-present" {
				wantIssuerReads = 1
			}
			if native.Reloads != wantReloads || native.IssuerReads != wantIssuerReads {
				t.Fatal("renewal stopped at an unexpected native boundary", mode, native.Reloads, native.IssuerReads, err)
			}
			afterMaterial := createBootstrapStateSnapshot(t, f.store.Root)
			for path, digest := range before {
				if (strings.HasPrefix(path, "openbao/") || strings.Contains(path, "-transport/")) && afterMaterial[path] != digest {
					t.Fatal("original private material changed", path)
				}
			}

			if mode == "complete" {
				if err != nil || native.Reloads != 1 || native.IssuerReads != 1 {
					t.Fatal("actual transport renewal did not complete", err)
				}
				current, proof, readErr := readLegacyInitialTransportRenewal(f.store, f.e.Journal.PlanSHA256)
				if readErr != nil || proof == nil || len(proof.Owners) != 10 || len(proof.Material) != 8 {
					t.Fatal("actual completed proof unavailable", readErr)
				}
				for role, old := range f.original {
					if current[role].CACert == old.CACert {
						t.Fatal("temporary authority did not renew", role)
					}
					ca, parseErr := parseFirstCertificatePEM([]byte(current[role].CACert))
					if parseErr != nil {
						t.Fatal(parseErr)
					}
					for name, leaf := range old.Identities {
						next := current[role].Identities[name]
						cert, parseErr := parseFirstCertificatePEM([]byte(next.Certificate))
						if parseErr != nil || next.Key != leaf.Key || cert.CheckSignatureFrom(ca) != nil {
							t.Fatal("original serving key or genuine signature changed", role, name, parseErr)
						}
					}
				}
				for id, uid := range proof.Owners {
					if deploymentplan.Map(native.Objects[id]["metadata"])["uid"] != uid || uid != f.journal.Owners[id] {
						t.Fatal("original UID changed", id)
					}
				}
				testInitialTransportCompletedProofDrift(t, f, review)
			} else {
				if err == nil {
					t.Fatal("unproven renewal accepted", mode)
				}
				if _, statErr := os.Stat(filepath.Join(f.store.Root, "deployment/create/transport-renewal", review, "completed.json")); !os.IsNotExist(statErr) {
					t.Fatal("failed renewal published completed proof", statErr)
				}
			}
			// Repeating a legacy execution is explicitly forbidden. Unknown writes and
			// original signed material must remain available for scoped reconciliation.
			snapshot := createBootstrapStateSnapshot(t, f.store.Root)
			retryErr := renewEnvironmentInitialTransports(t.Context(), f.cfg, f.store, f.e, f.backend, f.plan, f.journal, review)
			var after initialTransportNativeState
			if readErr := readExecutionJSON(f.state, &after); readErr != nil {
				t.Fatal(readErr)
			}
			if retryErr == nil || !strings.Contains(retryErr.Error(), "automatic replay is prohibited") || after.Writes != native.Writes || after.Leases != after.Releases {
				t.Fatal("retry rewrote uncertain or completed projections", retryErr, after)
			}
			for path, digest := range snapshot {
				if strings.Contains(path, "transport") && createBootstrapStateSnapshot(t, f.store.Root)[path] != digest {
					t.Fatal("retry changed transport evidence", path)
				}
			}
		})
	}
}

func testInitialTransportCompletedProofDrift(t *testing.T, f *initialTransportExecutionContract, review string) {
	t.Helper()
	base := filepath.Join("deployment/create/transport-renewal", review)
	for _, rel := range []string{filepath.Join(base, "completed.json"), filepath.Join(base, "management-transport/identities.json"), filepath.Join(base, "openbao-transport/ca.crt"), filepath.Join(base, "openbao-transport/ca.key"), filepath.Join("deployment/create/material", f.e.Journal.PlanSHA256, "management-transport/identities.json")} {
		t.Run("proof-drift-"+strings.ReplaceAll(rel, "/", "-"), func(t *testing.T) {
			path := filepath.Join(f.store.Root, rel)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := os.WriteFile(path, original, 0600); err != nil {
					t.Fatal(err)
				}
			}()
			if err := os.WriteFile(path, []byte("unreviewed immutable proof change"), 0600); err != nil {
				t.Fatal(err)
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			if _, _, err := readLegacyInitialTransportRenewal(f.store, f.e.Journal.PlanSHA256); err == nil {
				t.Fatal("changed immutable transport evidence accepted", rel)
			}
			assertRecoveryContractReadOnly(t, f.store, before)
		})
	}
}

// Check the external API model as well as the production caller: the correct
// UID/RV must never authorize a DELETE sent to a different API resource.
func TestInitialTransportNativeLeaseRetirementBindsExactAPITarget(t *testing.T) {
	f := newInitialTransportExecutionContract(t, "complete")
	lease, err := f.backend.AcquireExecution(t.Context(), f.plan)
	if err != nil {
		t.Fatal(err)
	}
	var native initialTransportNativeState
	if err := readExecutionJSON(f.state, &native); err != nil {
		t.Fatal(err)
	}
	id := "Lease/" + f.e.Journal.Stack + "-video-cloud/rtk-deployment-execution"
	original := native.Objects[id]
	if original == nil {
		t.Fatal("actual native lease missing")
	}
	meta := deploymentplan.Map(original["metadata"])
	uid, rv := deploymentplan.String(meta["uid"]), deploymentplan.String(meta["resourceVersion"])
	before := readTestFile(t, f.state)
	for _, mutation := range []struct{ name, id, uid, rv string }{
		{"foreign-namespace", "Lease/foreign/rtk-deployment-execution", uid, rv},
		{"foreign-uid", id, "00000000-0000-4000-8000-999999999999", rv},
		{"stale-version", id, uid, "999999"},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			if err := f.backend.DeleteExecutionLease(t.Context(), mutation.id, mutation.uid, mutation.rv); err == nil {
				t.Fatal("foreign native DELETE target accepted")
			}
			if readTestFile(t, f.state) != before {
				t.Fatal("refused DELETE changed the original owner")
			}
		})
	}
	if err := lease.Release(t.Context()); err != nil {
		t.Fatal("original native owner release", err)
	}
	native = initialTransportNativeState{}
	if err := readExecutionJSON(f.state, &native); err != nil {
		t.Fatal(err)
	}
	if native.Objects[id] != nil || native.Leases != 1 || native.Releases != 1 {
		t.Fatal("legitimate exact release did not retire its sole owner")
	}
}
