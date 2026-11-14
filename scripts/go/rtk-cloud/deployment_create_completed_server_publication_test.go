package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type completedPublicationBackend struct{ freshStageBackend }

func (b completedPublicationBackend) Write(ctx context.Context, action string, body deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	if action != "retire" {
		return b.freshStageBackend.Write(ctx, action, body, dry)
	}
	old := deploymentplan.Clone(b.objects[deploymentplan.ID(body)])
	if old == nil {
		return nil, fmt.Errorf("missing original retirement owner")
	}
	if !dry {
		delete(b.objects, deploymentplan.ID(body))
		b.writes[deploymentplan.ID(body)]++
	}
	return old, nil
}
func (b completedPublicationBackend) Wait(ctx context.Context, r deploymentplan.Resource) error {
	if r.Action == "retire" {
		if b.objects[r.ID] != nil {
			return fmt.Errorf("retirement failed")
		}
		return nil
	}
	return b.freshStageBackend.Wait(ctx, r)
}

func TestCompletedOpenBaoAPIServerPublicationDoesNotReenrollAfterSessionRenewal(t *testing.T) {
	cfg, p, _, service := createServiceRecipeFixture(t, "dev")
	store, e, _, memory := freshStageFixture(t)
	cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
	cfg.Values["DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS"] = "30"
	cfg.Values["DEPLOYMENT_UPGRADE_WINDOW_SECONDS"] = "600"
	binding, err := createEnvironmentCustodyBinding(cfg, p, e.Journal.PlanSHA256, "")
	if err != nil {
		t.Fatal(err)
	}
	binding.ServiceRootSHA256 = service.Root.RootFingerprint
	memory.captureJobLifecycle = true
	b := completedPublicationBackend{memory}
	runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, Custody: binding, Backend: environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-native-api")}}
	now := time.Now().UTC().Add(-time.Minute)
	makeCert := func(cn string, ca bool, parent *x509.Certificate, parentKey *ecdsa.PrivateKey) (*x509.Certificate, *ecdsa.PrivateKey, string) {
		key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			t.Fatal(err)
		}
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(now.UnixNano() + int64(len(cn))), Subject: pkix.Name{CommonName: cn}, NotBefore: now, NotAfter: now.Add(4 * time.Hour), IsCA: ca, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageDigitalSignature}
		if ca {
			tmpl.KeyUsage |= x509.KeyUsageCertSign | x509.KeyUsageCRLSign
		} else {
			tmpl.DNSNames = []string{cn}
			tmpl.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		}
		if parent == nil {
			parent = tmpl
			parentKey = key
		}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, parent, &key.PublicKey, parentKey)
		if err != nil {
			t.Fatal(err)
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			t.Fatal(err)
		}
		return cert, key, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	}
	root, rootKey, rootPEM := makeCert("original-service-root", true, nil, nil)
	issuer, issuerKey, issuerPEM := makeCert("original-service-issuer", true, root, rootKey)
	service.Root.Certificate, service.Root.RootFingerprint = rootPEM, environmentPreflightBytesSHA256(root.Raw)
	service.Intermediate.Certificate, service.Intermediate.RootFingerprint = issuerPEM, service.Root.RootFingerprint
	binding.ServiceRootSHA256 = service.Root.RootFingerprint
	binding.OpenBaoRootSHA256 = environmentPreflightBytesSHA256(root.Raw)
	runtime.OpenBao.CACertPath = filepath.Join(store.Root, "deployment/create/material", e.Journal.PlanSHA256, "openbao-transport/ca.crt")
	if err := store.write(filepath.Join("deployment/create/material", e.Journal.PlanSHA256, "openbao-transport/ca.crt"), []byte(rootPEM), false); err != nil {
		t.Fatal(err)
	}
	trust, err := renderEnvironmentCustodyTrust(cfg, e.Journal.PlanSHA256, rootPEM, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.write(filepath.Join("deployment/create", e.Journal.PlanSHA256, "custody/transport.json"), []byte(deploymentplan.String(deploymentplan.Map(trust[0].Object["data"])["transport.json"])), false); err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	stage := func(name string, defs []deploymentplan.Definition, project bool) *deploymentplan.Plan {
		t.Helper()
		if project {
			var err error
			defs, err = projectEnvironmentCustodyResources(cfg.Values, "dev", binding, "", defs)
			if err != nil {
				t.Fatal(err)
			}
		}
		var live []deploymentplan.Object
		for _, d := range defs {
			if o := b.objects[deploymentplan.ID(d.Object)]; o != nil {
				live = append(live, o)
			}
		}
		in := deploymentplan.Inputs{Environment: "dev", Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest("original-release"), BindingsSHA256: environmentCreateStageBindingsSHA(name, defs)}
		child, err := deploymentplan.Compile(in, defs, live)
		if err != nil {
			t.Fatal(name, err)
		}
		if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, name, child, b, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
			t.Fatal(name, err)
		}
		return child
	}
	public := map[string]environmentCustodyPublicState{}
	for _, owner := range environmentCreateApplicationOwners() {
		if owner.Name != "video-cloud-api-pki" && owner.Name != "video-cloud-api-app-pki" {
			continue
		}
		ns := p.Stack + "-video-cloud"
		name := owner.Name + "." + ns + ".svc"
		serverPath := filepath.Join(filepath.Dir(owner.ClientState), "server.json")
		leaf, key, leafPEM := makeCert(name, false, issuer, issuerKey)
		keyDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			t.Fatal(err)
		}
		keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}))
		chain := leafPEM + issuerPEM + rootPEM
		env := map[string]string{"PKI_ENVIRONMENT": "dev", "PKI_SERVER_DOMAIN": "service", "PKI_SERVER_NAME": name, "PKI_SERVER_DNS_NAMES": name, "PKI_SERVER_ROOT_SHA256": service.Root.RootFingerprint, "PKI_SERVER_IDENTITY_STATE": serverPath, "PKI_SERVER_BOOTSTRAP_REQUEST_ID": service.BootstrapSessionID + ":" + owner.Name + ":server", "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_STATE": owner.ClientState, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_SUBJECT": "service:" + owner.Role, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_ROOT_SHA256": service.Root.RootFingerprint}
		job := environmentCreateServiceEnrollmentJob(p, ns, owner.Name+"-server", owner.Claim, service, []any{"/app/serviceidentity-bootstrap", "server-bootstrap"}, env, false)
		environmentCreateTransportJobOwner(job.Object, p.Image, owner.StateRoot, serverPath, 10001)
		stage("server-enrollment-"+owner.Name, []deploymentplan.Definition{job}, true)
		readerName := "pki-export-" + owner.Name + "-" + e.Journal.PlanSHA256[:12]
		settings := map[string]string{"PKI_ENVIRONMENT": "dev", "PKI_SERVER_NAME": name, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_STATE": owner.ClientState, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_SUBJECT": "service:" + owner.Role, "PKI_SERVER_BOOTSTRAP_OWNER_CLIENT_ROOT_SHA256": service.Root.RootFingerprint, "PKI_CLIENT_IDENTITY_STATE": owner.ClientState, "PKI_SERVER_IDENTITY_STATE": serverPath}
		labels := deploymentplan.Object{"app.kubernetes.io/name": readerName, "rtk.realtek.com/stack": p.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}
		reader := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": readerName, "namespace": ns, "labels": labels}, "spec": deploymentplan.Object{"replicas": float64(1), "selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app.kubernetes.io/name": readerName}}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": labels}, "spec": deploymentplan.Object{"automountServiceAccountToken": false, "securityContext": environmentCreateServicePodSecurity(), "containers": []any{deploymentplan.Object{"name": "custody", "image": p.Image, "command": []any{"/bin/sh", "-ec", "exec sleep 3600"}, "env": append(environmentCreateEnv(settings), environmentCreateServiceDatabaseEnv("pki-verifier-database")), "securityContext": environmentCreateServiceContainerSecurity(), "volumeMounts": []any{environmentCreateServiceMount("owner-state", owner.StateRoot, false)}}}, "volumes": []any{environmentCreateServiceVolume("owner-state", "persistentVolumeClaim", owner.Claim)}}}}}
		stage("server-custody-"+owner.Name, []deploymentplan.Definition{{Phase: "bootstrap", Object: reader}}, true)
		rid := deploymentplan.ID(reader)
		uid := deploymentplan.String(deploymentplan.Map(b.objects[rid]["metadata"])["uid"])
		clientRef, err := newEnvironmentCustodyStateReference(cfg, binding, ns, owner.Claim, owner.ClientState, service.Root.RootFingerprint)
		if err != nil {
			t.Fatal(err)
		}
		serverRef, err := newEnvironmentCustodyStateReference(cfg, binding, ns, owner.Claim, serverPath, service.Root.RootFingerprint)
		if err != nil {
			t.Fatal(err)
		}
		podUID, rsUID := "aaaaaaaa-1111-4111-8111-111111111111", "bbbbbbbb-1111-4111-8111-111111111111"
		intent := deploymentplan.Object{"owner": owner.Name, "namespace": ns, "reader_uid": uid, "pod_uid": podUID, "replicaset_uid": rsUID, "server_name": name, "root_sha256": service.Root.RootFingerprint, "client_state": clientRef, "server_state": serverRef}
		proof := deploymentplan.Object{"certificate_sha256": environmentPreflightBytesSHA256([]byte(chain)), "reader_uid": uid, "pod_uid": podUID, "replicaset_uid": rsUID, "server_name": name, "client_state": clientRef, "server_state": serverRef}
		ek := "server-export-" + owner.Name
		if err := e.BeginEntry(ek, "bootstrap", mustProviderRecoveryJSON(intent), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		if err := e.RecordReceipt(ek, mustProviderRecoveryJSON(proof)); err != nil {
			t.Fatal(err)
		}
		if err := e.CompleteEntry(ek, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		retire := "retire-" + readerName
		ri := deploymentplan.Object{"resource_id": rid, "uid": uid}
		if err := e.BeginEntry(retire, "bootstrap", mustProviderRecoveryJSON(ri), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		stage(retire, []deploymentplan.Definition{{Phase: "retirement", Object: deploymentplan.Clone(b.objects[rid]), Retire: true}}, false)
		ri["absent"] = true
		if err := e.RecordReceipt(retire, mustProviderRecoveryJSON(ri)); err != nil {
			t.Fatal(err)
		}
		if err := e.CompleteEntry(retire, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		stage("server-secret-"+owner.Name, []deploymentplan.Definition{environmentCreateSecret(ns, owner.Name+"-tls", map[string]string{"tls.crt": chain, "tls.key": keyPEM, "ca.crt": rootPEM})}, false)
		public[serverPath] = environmentCustodyPublicState{Subject: name, RootSHA256: service.Root.RootFingerprint, Fingerprint: environmentPreflightBytesSHA256(leaf.Raw), PublicKeySHA256: environmentPreflightBytesSHA256(leaf.RawSubjectPublicKeyInfo), StateSHA256: strings.Repeat("d", 64)}
		public[owner.ClientState] = environmentCustodyPublicState{Subject: "service:" + owner.Role, RootSHA256: service.Root.RootFingerprint, Fingerprint: strings.Repeat("b", 64), PublicKeySHA256: strings.Repeat("c", 64), StateSHA256: strings.Repeat("d", 64)}
	}
	nativeFile := filepath.Join(t.TempDir(), "native.json")
	save := func() {
		t.Helper()
		if err := os.WriteFile(nativeFile, mustProviderRecoveryJSON(map[string]any{"objects": b.objects, "public": public, "inspections": 0}), 0600); err != nil {
			t.Fatal(err)
		}
	}
	save()
	script := `#!/usr/bin/env python3
import json,os,sys,uuid
p=os.environ['RTK_COMPLETED_PUBLICATION_NATIVE'];a=sys.argv[1:]
with open(p) as f:d=json.load(f)
def save():
 with open(p,'w') as f:json.dump(d,f)
def emit(v):print(json.dumps(v))
if 'get' in a:
 i=a.index('get');kind=a[i+1];ns=a[a.index('--namespace')+1] if '--namespace' in a else ''
 if kind=='pods':emit({'items':[v for v in d['objects'].values() if v['kind']=='Pod' and v['metadata']['namespace']==ns]});sys.exit()
 key=kind+'/'+ns+'/'+a[i+2]
 if key in d['objects']:emit(d['objects'][key])
 sys.exit()
if 'create' in a:
 v=json.load(sys.stdin);assert v['kind']=='Job';m=v['metadata'];m['uid']=str(uuid.uuid4());m['resourceVersion']='1';v['status']={'succeeded':1,'conditions':[{'type':'Complete','status':'True'}]};ns=m['namespace'];name=m['name'];d['objects']['Job/'+ns+'/'+name]=v
 pod={'apiVersion':'v1','kind':'Pod','metadata':{'namespace':ns,'name':name+'-pod','uid':str(uuid.uuid4()),'ownerReferences':[{'kind':'Job','name':name,'uid':m['uid'],'controller':True}]},'spec':v['spec']['template']['spec'],'status':{'phase':'Succeeded'}}
 d['objects']['Pod/'+ns+'/'+name+'-pod']=pod;d['inspections']+=1;save();emit(v);sys.exit()
if 'logs' in a:
 if d.get('revoked'):print('private-inspection-error-marker',file=sys.stderr);sys.exit(1)
 name=a[a.index('logs')+1].split('/')[1];v=next(v for v in d['objects'].values() if v['kind']=='Pod' and v['metadata']['name']==name);command=v['spec']['containers'][0]['command'];path=command[2];state=d['public'][path];assert command[4]==state['subject'];assert (command[3]=='-' and command[6]=='-' and command[4].startswith('service:')) or (command[3]=='service' and command[6]==state['subject']);emit(state);sys.exit()
if 'wait' in a:sys.exit()
if 'delete' in a:
 req=json.load(sys.stdin);uid=req['preconditions']['uid'];matches=[k for k,v in d['objects'].items() if v['kind']=='Job' and v['metadata']['uid']==uid];assert len(matches)==1
 key=matches[0];name=d['objects'][key]['metadata']['name'];ns=d['objects'][key]['metadata']['namespace'];del d['objects'][key];d['objects'].pop('Pod/'+ns+'/'+name+'-pod',None);save();emit({'status':'Success'});sys.exit()
sys.exit(99)
`
	exe := filepath.Join(t.TempDir(), "kubectl")
	if err := os.WriteFile(exe, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_COMPLETED_PUBLICATION_NATIVE", nativeFile)
	t.Setenv("RTK_CLOUD_KUBECTL", exe)
	before := deploymentplan.Digest(e.Journal)
	materialBefore := deploymentplan.Digest(b.objects)
	service.BootstrapSessionID = "99999999-9999-4999-8999-999999999999"
	forbidden := func(context.Context, string, []deploymentplan.Definition) error {
		return fmt.Errorf("completed publication attempted enrollment/export/retirement")
	}
	if err := publishEnvironmentCreateApplicationServers(t.Context(), cfg, p, runtime, service, forbidden); err != nil {
		t.Fatal("reuse both completed API publications after session renewal", err)
	}
	if deploymentplan.Digest(e.Journal) != before || deploymentplan.Digest(b.objects) != materialBefore {
		t.Fatal("completed publication changed original journal or keys")
	}
	var observed struct {
		Objects     map[string]deploymentplan.Object         `json:"objects"`
		Public      map[string]environmentCustodyPublicState `json:"public"`
		Inspections int                                      `json:"inspections"`
	}
	if err := readExecutionJSON(nativeFile, &observed); err != nil {
		t.Fatal(err)
	}
	if observed.Inspections != 4 || deploymentplan.Digest(observed.Objects) != materialBefore {
		t.Fatal("did not inspect committed KV client/server state without changing owners")
	}
	for _, mode := range []string{"foreign-secret", "changed-secret", "missing-retirement", "foreign-kv-reference", "pending-kv", "wrong-server-key", "revoked-native-state"} {
		t.Run(mode, func(t *testing.T) {
			objects := map[string]deploymentplan.Object{}
			for id, object := range b.objects {
				objects[id] = deploymentplan.Clone(object)
			}
			states := map[string]environmentCustodyPublicState{}
			for path, value := range public {
				states[path] = value
			}
			selected := environmentCreateApplicationOwners()[1]
			secretID := "Secret/" + p.Stack + "-video-cloud/" + selected.Name + "-tls"
			retired := createOpenBaoACKCompletionEntry(e.Journal, "retire-pki-export-"+selected.Name+"-"+e.Journal.PlanSHA256[:12])
			export := createOpenBaoACKCompletionEntry(e.Journal, "server-export-"+selected.Name)
			savedState, savedReceipt := retired.State, append([]byte(nil), export.Receipt...)
			t.Cleanup(func() { retired.State = savedState; export.Receipt = savedReceipt })
			switch mode {
			case "foreign-secret":
				deploymentplan.Map(objects[secretID]["metadata"])["uid"] = "99999999-1111-4111-8111-111111111111"
			case "changed-secret":
				deploymentplan.Map(objects[secretID]["data"])["tls.crt"] = "Y2hhbmdlZA=="
			case "missing-retirement":
				retired.State = "pending"
			case "foreign-kv-reference":
				proof := jsonRawObject(export.Receipt)
				deploymentplan.Map(proof["server_state"])["generation"] = strings.Repeat("f", 64)
				export.Receipt = mustProviderRecoveryJSON(proof)
			case "pending-kv":
				value := states[selected.ClientState]
				value.Pending = true
				states[selected.ClientState] = value
			case "wrong-server-key":
				path := filepath.Join(filepath.Dir(selected.ClientState), "server.json")
				value := states[path]
				value.PublicKeySHA256 = strings.Repeat("f", 64)
				states[path] = value
			}
			fixture := map[string]any{"objects": objects, "public": states, "inspections": 0, "revoked": mode == "revoked-native-state"}
			if err := os.WriteFile(nativeFile, mustProviderRecoveryJSON(fixture), 0600); err != nil {
				t.Fatal(err)
			}
			if err := publishEnvironmentCreateApplicationServers(t.Context(), cfg, p, runtime, service, forbidden); err == nil || strings.Contains(err.Error(), "private-inspection-error-marker") {
				t.Fatal("invalid completed publication was accepted or exposed native output", mode, err)
			}
			if deploymentplan.Digest(b.objects) != materialBefore {
				t.Fatal("failed observation changed original serving owners")
			}
		})
	}
}
