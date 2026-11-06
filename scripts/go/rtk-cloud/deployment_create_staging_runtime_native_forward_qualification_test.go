package main

import (
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type stagingRuntimeNativeState struct {
	Objects          map[string]deploymentplan.Object `json:"objects"`
	Writes           map[string]int                   `json:"writes"`
	Mode             string                           `json:"mode"`
	Interrupted      bool                             `json:"interrupted"`
	Leases, Releases int
}

type stagingRuntimeNativeFixture struct {
	*stagingWorkloadQualificationFixture
	statePath string
	backend   environmentKubernetesBackend
}

func (f *stagingRuntimeNativeFixture) native(t *testing.T) stagingRuntimeNativeState {
	t.Helper()
	raw, err := os.ReadFile(f.statePath)
	if err != nil {
		t.Fatal(err)
	}
	var state stagingRuntimeNativeState
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	return state
}
func (f *stagingRuntimeNativeFixture) saveNative(t *testing.T, state stagingRuntimeNativeState) {
	t.Helper()
	if err := os.WriteFile(f.statePath, mustProviderRecoveryJSON(state), 0600); err != nil {
		t.Fatal(err)
	}
}

func newStagingRuntimeNativeFixture(t *testing.T, mode string) *stagingRuntimeNativeFixture {
	t.Helper()
	workspace := writeDeploymentFixture(t, "staging", "lke")
	architecture := filepath.Join(workspace, "cloud_deploy/architectures/kubernetes/architecture.env")
	writeTestFile(t, architecture, readTestFile(t, architecture)+"\nDEPLOYMENT_PROTECTION_PROFILE=standard\n")
	cfg, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	oci := []byte(`{"schemaVersion":2,"config":{"digest":"sha256:` + strings.Repeat("c", 64) + `"},"layers":[]}`)
	image := "registry.example.test/isolated-runtime@sha256:" + environmentPreflightBytesSHA256(oci)
	resources := stagingRuntimeQualificationResources()
	for i := range resources {
		o := resources[i].Desired
		meta := deploymentplan.Map(o["metadata"])
		meta["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-staging", "app.kubernetes.io/managed-by": "rtk-cloud"}
		if o["kind"] == "Deployment" {
			o["apiVersion"] = "apps/v1"
			mqttFoundationPrimary(o)["image"] = image
			name := meta["name"].(string)
			deploymentplan.Map(o["spec"])["selector"] = deploymentplan.Object{"matchLabels": deploymentplan.Object{"app": name}}
			deploymentplan.Map(deploymentplan.Map(o["spec"])["template"])["metadata"] = deploymentplan.Object{"labels": deploymentplan.Object{"app": name}}
		}
		if o["kind"] == "PersistentVolumeClaim" {
			deploymentplan.Map(o["spec"])["storageClassName"] = "linode-block-storage-retain"
			deploymentplan.Map(o["spec"])["resources"] = deploymentplan.Object{"requests": deploymentplan.Object{"storage": "1Gi"}}
		}
		if o["kind"] == "Ingress" {
			o["apiVersion"] = "networking.k8s.io/v1"
			o["spec"] = deploymentplan.Object{"defaultBackend": deploymentplan.Object{"service": deploymentplan.Object{"name": "video-cloud-api", "port": deploymentplan.Object{"number": float64(8080)}}}}
		}
	}
	kube := map[string]any{"apiVersion": "v1", "kind": "Config", "current-context": "isolated", "contexts": []any{map[string]any{"name": "isolated", "context": map[string]any{"cluster": "isolated", "user": "operator"}}}, "clusters": []any{map[string]any{"name": "isolated", "cluster": map[string]any{"server": "https://isolated.example.test:6443", "certificate-authority-data": base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}}}, "users": []any{map[string]any{"name": "operator", "user": map[string]any{"token": "isolated-native-token"}}}}
	kubeRaw, err := yaml.Marshal(kube)
	if err != nil {
		t.Fatal(err)
	}
	base := newStagingWorkloadQualificationFixture(t, resources, func(selected *deploymentConfig, parent *environmentCreatePlan, store secretStore) {
		*selected = cfg
		operator := map[string]string{"LINODE_TOKEN": "isolated-staging-operator"}
		parent.OperatorSHA256 = deploymentplan.Digest(operator)
		parent.Release.Images["LKE_CLOUD_LOGGER_IMAGE"] = environmentReleaseImage{Reference: image}
		for key, value := range operator {
			if err := store.write("operator/env/"+key, []byte(value), false); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.write("kube/kubeconfig.yaml", kubeRaw, false); err != nil {
			t.Fatal(err)
		}
	})
	for i, r := range base.child.Resources {
		if r.Phase == "routing" {
			continue
		}
		base.journal.Completed = append(base.journal.Completed, r.ID)
		base.journal.Owners[r.ID] = []string{"11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222", "33333333-3333-4333-8333-333333333333"}[i]
	}
	base.journal.Failed = base.child.Resources[2].ID
	base.saveProgress(t, base.child, base.journal)
	dir := t.TempDir()
	f := &stagingRuntimeNativeFixture{stagingWorkloadQualificationFixture: base, statePath: filepath.Join(dir, "native.json")}
	state := stagingRuntimeNativeState{Objects: map[string]deploymentplan.Object{}, Writes: map[string]int{}, Mode: mode}
	for _, suffix := range []string{"logger", "video-cloud", "ingress"} {
		ns := base.parent.Stack + "-" + suffix
		state.Objects["Namespace//"+ns] = deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": ns, "uid": "namespace-" + suffix}}
	}
	for _, r := range base.child.Resources {
		if r.Phase == "routing" {
			continue
		}
		o := deploymentplan.Clone(r.Desired)
		m := deploymentplan.Map(o["metadata"])
		m["uid"] = base.journal.Owners[r.ID]
		m["resourceVersion"] = "1"
		m["generation"] = float64(1)
		if o["kind"] == "PersistentVolumeClaim" {
			deploymentplan.Map(o["spec"])["volumeName"] = "original-inbox-volume"
			o["status"] = deploymentplan.Object{"phase": "Bound"}
		}
		state.Objects[r.ID] = o
	}
	pv := stagingQualificationObject("PersistentVolume", "", "original-inbox-volume", "44444444-4444-4444-8444-444444444444", deploymentplan.Object{"csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": "501"}, "claimRef": deploymentplan.Object{"namespace": base.parent.Stack + "-logger", "name": "cloud-logger-billing-inbox", "uid": base.journal.Owners[base.child.Resources[0].ID]}})
	state.Objects[deploymentplan.ID(pv)] = pv
	f.saveNative(t, state)
	script := `#!/usr/bin/env python3
import copy,fcntl,json,os,sys,uuid
p=os.environ['RTK_STAGING_RUNTIME_NATIVE_STATE']
with open(p+'.lock','a') as lock:
 fcntl.flock(lock,fcntl.LOCK_EX);d=json.load(open(p));a=sys.argv[1:];ns=''
 while a and a[0].startswith('--'):
  if a[0] in ('--kubeconfig','--namespace'):
   if a[0]=='--namespace':ns=a[1]
   a=a[2:]
  else:a=a[1:]
 if '--namespace' in a:ns=a[a.index('--namespace')+1]
 def save():
  with open(p,'w') as out:json.dump(d,out)
 def emit(o):print(json.dumps(o))
 def ready(o):
  m=o['metadata'];generation=m['generation'];name=m['name'];namespace=m['namespace'];uid=m['uid']
  o['status']={'observedGeneration':generation,'readyReplicas':1,'updatedReplicas':1,'availableReplicas':1,'replicas':1}
  rsname=name+'-current';rsuid='rs-'+uid
  ref=lambda kind,name,uid:[{'apiVersion':'apps/v1','kind':kind,'name':name,'uid':uid,'controller':True}]
  d['objects']['ReplicaSet/'+namespace+'/'+rsname]={'apiVersion':'apps/v1','kind':'ReplicaSet','metadata':{'namespace':namespace,'name':rsname,'uid':rsuid,'ownerReferences':ref('Deployment',name,uid)}}
  spec=copy.deepcopy(o['spec']['template']['spec']);containers=spec['containers']
  statuses=[{'name':c['name'],'ready':True,'started':True,'containerID':'containerd://'+c['name'],'imageID':'docker-pullable://'+c['image'],'state':{'running':{'startedAt':'2026-01-01T00:00:00Z'}}} for c in containers]
  d['objects']['Pod/'+namespace+'/'+name+'-current']={'apiVersion':'v1','kind':'Pod','metadata':{'namespace':namespace,'name':name+'-current','uid':'pod-'+uid,'ownerReferences':ref('ReplicaSet',rsname,rsuid)},'spec':spec,'status':{'phase':'Running','conditions':[{'type':'Ready','status':'True'}],'containerStatuses':statuses}}
 # Initial prior writes have live descendants; updates replace the target body
 # of those descendants, preserving each original Deployment UID.
 for o in list(d['objects'].values()):
  if o['kind']=='Deployment':ready(o)
 if a[0]=='get':
  kind=a[1]
  if len(a)>2 and not a[2].startswith('-'):
   o=d['objects'].get(kind+'/'+ns+'/'+a[2])
   if o is not None:emit(o)
  else:
   kinds=['persistentvolume' if x=='persistentvolumes' else x for x in kind.lower().split(',')]
   items=[o for o in d['objects'].values() if o['kind'].lower() in kinds and o['metadata'].get('namespace','')==ns]
   if '-l' in a:
    k,v=a[a.index('-l')+1].split('=',1);items=[o for o in items if o['metadata'].get('labels',{}).get(k)==v]
   emit({'apiVersion':'v1','kind':'List','items':items})
  save();sys.exit()
 if a[0] in ('create','replace'):
  o=json.load(sys.stdin);m=o['metadata'];kind=o['kind'];key=kind+'/'+m.get('namespace','')+'/'+m['name'];old=d['objects'].get(key)
  assert kind in ('Lease','Deployment','Ingress')
  assert 'Namespace//'+m['namespace'] in d['objects']
  assert (a[0]=='create' and old is None) or (a[0]=='replace' and old and old['metadata']['uid']==m['uid'] and old['metadata']['resourceVersion']==m['resourceVersion'])
  if '--dry-run=server' not in a:
   m.setdefault('uid',str(uuid.uuid4()));m['resourceVersion']=str(int(m.get('resourceVersion','0'))+1);m['generation']=int(m.get('generation',0))+1
   d['objects'][key]=o
   if kind=='Lease':d['Leases']+=1
   else:d['writes'][key]=d['writes'].get(key,0)+1
   if kind=='Deployment':ready(o)
   save()
  emit(o);sys.exit()
 if a[0]=='delete':
  request=json.load(sys.stdin);raw=a[1];assert '/leases/' in raw
  namespace=raw.split('/namespaces/')[1].split('/')[0];name=raw.split('/')[-1];key='Lease/'+namespace+'/'+name
  m=d['objects'][key]['metadata'];assert request['preconditions']=={'uid':m['uid'],'resourceVersion':m['resourceVersion']}
  del d['objects'][key];d['Releases']+=1;save();emit({'status':'Success'});sys.exit()
 if a[0] in ('rollout','wait'):
  if a[:2]==['rollout','status']:
   key=a[2].split('/')[0]+'/'+ns+'/'+a[2].split('/')[1]
   count=d['writes'].get(key,0)
   if not d['interrupted'] and key.endswith('/cloud-logger') and ((d['mode']=='initial-readiness' and count==1) or (d['mode']=='steady-readiness' and count==2)):
    d['interrupted']=True;save();sys.exit(1)
  save();sys.exit()
 print('unsupported native staging operation '+repr(a),file=sys.stderr);sys.exit(2)
`
	kubePath := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(kubePath, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "manifest.json"), oci, 0600); err != nil {
		t.Fatal(err)
	}
	docker := `#!/usr/bin/env python3
import json,os,pathlib,sys
if sys.argv[1:]==['info','--format','{{json .ClientInfo.Plugins}}']:
 print(json.dumps([{'Name':'buildx','Path':'/isolated/plugins/docker-buildx'}]))
elif sys.argv[1]=='--config' and sys.argv[3:7]==['buildx','imagetools','inspect','--raw']:
 assert pathlib.Path(sys.argv[2],'config.json').is_file()
 assert sys.argv[7]==os.environ['RTK_STAGING_RUNTIME_IMAGE']
 sys.stdout.buffer.write(pathlib.Path(os.environ['RTK_STAGING_RUNTIME_OCI']).read_bytes())
else:raise AssertionError('unexpected isolated OCI operation')
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(docker), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_STAGING_RUNTIME_NATIVE_STATE", f.statePath)
	t.Setenv("RTK_CLOUD_KUBECTL", kubePath)
	t.Setenv("RTK_STAGING_RUNTIME_IMAGE", image)
	t.Setenv("RTK_STAGING_RUNTIME_OCI", filepath.Join(dir, "manifest.json"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	f.backend, err = newEnvironmentKubernetesBackend(string(kubeRaw), "staging")
	if err != nil {
		t.Fatal(err)
	}
	return f
}

func TestStagingRuntimeNativeForwardAppliesBothStagesAndResumesKnownWrites(t *testing.T) {
	for _, mode := range []string{"complete", "initial-readiness", "steady-readiness", "foreign-logger", "changed-api", "unbound-inbox"} {
		t.Run(mode, func(t *testing.T) {
			f := newStagingRuntimeNativeFixture(t, mode)
			state := f.native(t)
			logger, api, claim := f.child.Resources[1].ID, f.child.Resources[2].ID, f.child.Resources[0].ID
			switch mode {
			case "foreign-logger":
				deploymentplan.Map(state.Objects[logger]["metadata"])["uid"] = "foreign-logger"
			case "changed-api":
				mqttFoundationPrimary(state.Objects[api])["command"] = []any{"foreign-command"}
			case "unbound-inbox":
				state.Objects[claim]["status"] = deploymentplan.Object{"phase": "Pending"}
			}
			// Body drift changes a declared field, not a harmless server default.
			if mode == "changed-api" {
				deploymentplan.Map(state.Objects[api]["spec"])["replicas"] = float64(2)
			}
			f.saveNative(t, state)
			inputBefore, err := os.ReadFile(environmentCreateStageInputPath(f.e, f.stage))
			if err != nil {
				t.Fatal(err)
			}
			run := func() error {
				return applyStagingWorkloadRuntimeForward(t.Context(), f.cfg, f.store, &f.parent, f.e, f.stage, f.original, f.child, f.journal, f.backend)
			}
			err = run()
			if mode == "foreign-logger" || mode == "changed-api" || mode == "unbound-inbox" {
				if err == nil || len(f.native(t).Writes) != 0 || f.native(t).Leases != 0 {
					t.Fatal("changed original runtime owner reached native mutation", err)
				}
				return
			}
			if mode == "complete" {
				if err != nil {
					t.Fatal("native initial/steady forward failed", err)
				}
			} else {
				if err == nil || !f.native(t).Interrupted {
					t.Fatal("native readiness interruption was not reached", err)
				}
				if err := run(); err != nil {
					t.Fatal("original native stage did not resume known writes", err)
				}
			}
			current := f.native(t)
			if current.Writes[logger] != 2 || current.Writes[api] != 1 || current.Writes[f.child.Resources[3].ID] != 1 || current.Writes[claim] != 0 || current.Leases != current.Releases {
				t.Fatalf("native stage replayed writes or lost lease ownership: %+v", current.Writes)
			}
			if deploymentplan.Map(current.Objects[claim]["metadata"])["uid"] != f.journal.Owners[claim] || deploymentplan.Map(current.Objects[claim]["spec"])["volumeName"] != "original-inbox-volume" || mqttFoundationPrimary(current.Objects[logger])["command"] != nil || mqttFoundationEnv(mqttFoundationPrimary(current.Objects[api]), "VIDEO_CLOUD_ENV") != "staging" {
				t.Fatal("steady runtime replaced original inbox or retained temporary launcher")
			}
			selected, err := freshCreateWorkloadStage(f.store, f.e)
			if err != nil || selected != stagingWorkloadRuntimePrefix+f.child.SHA256()[:12] {
				t.Fatal("native accepted successor lost original parent binding", err)
			}
			for _, stage := range []string{stagingLoggerInitPrefix + f.child.SHA256()[:12], selected} {
				if _, _, err := acceptedCreateStage(f.store, f.e, "kubernetes/"+stage); err != nil {
					t.Fatal("native stage lacks real accepted child evidence", err)
				}
			}
			before := deploymentplan.Digest(f.e.Journal)
			if err := run(); err != nil {
				t.Fatal("completed native forward retry", err)
			}
			if deploymentplan.Digest(f.native(t).Writes) != deploymentplan.Digest(current.Writes) || deploymentplan.Digest(f.e.Journal) != before {
				t.Fatal("completed native forward replayed writes or replaced checkpoint")
			}
			inputAfter, _ := os.ReadFile(environmentCreateStageInputPath(f.e, f.stage))
			if string(inputBefore) != string(inputAfter) {
				t.Fatal("native forward replaced interrupted predecessor input")
			}
		})
	}
}

func TestStagingRuntimeNativeCRLChecksCurrentRegistryAndRevokedLeaf(t *testing.T) {
	for _, mode := range []string{"current", "revoked", "changed-digest", "foreign-environment", "malformed-registry"} {
		t.Run(mode, func(t *testing.T) {
			f := stagingRuntimeQualificationFixture(t)
			issuer, key, _, now := rootCRLFixture(t)
			issuer.Environment = "staging"
			root, err := parseFirstCertificatePEM([]byte(issuer.Certificate))
			if err != nil {
				t.Fatal(err)
			}
			leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(42), NotBefore: now.Add(-time.Hour), NotAfter: now.Add(time.Hour)}
			leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, root, &key.PublicKey, key)
			if err != nil {
				t.Fatal(err)
			}
			leaf := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}))
			request := createRootCRLRequest{IssuerID: issuer.ID, Fingerprint: issuer.RootFingerprint, Number: "2", ThisUpdate: now.Add(-time.Minute), NextUpdate: now.Add(time.Hour)}
			if mode == "revoked" {
				request.Entries = []createRootCRLRevocation{{Serial: "2a", RevokedAt: now.Add(-2 * time.Minute), Reason: 1}}
			}
			signed := signedRootCRLFixture(t, issuer, key, request)
			record := productCurrentCRL{Issuer: issuer.ID, SHA: signed.Digest, Number: signed.Number, PEM: signed.PEM, This: signed.ThisUpdate, Next: signed.NextUpdate}
			if mode == "changed-digest" {
				record.SHA = strings.Repeat("f", 64)
			}
			if mode == "foreign-environment" {
				issuer.Environment = "dev"
			}
			protocol := newStagingNativeRecoveryProtocol(t, f.store, map[string]deploymentplan.Object{})
			state := protocol.read(t)
			state.Registry = record
			if mode == "malformed-registry" {
				state.Registry = "not a CRL record"
			}
			protocol.save(t, state)
			err = verifyStagingProductLeafCRL(t.Context(), f.cfg, environmentCreateBootstrapRuntime{Execution: f.e, Backend: protocol.backend, StagingControllerHandoff: true}, issuer, leaf)
			if mode == "current" {
				if err != nil {
					t.Fatal("valid original issuer/leaf rejected during handoff", err)
				}
			} else if err == nil {
				t.Fatal("unverified or revoked product leaf accepted")
			}
			calls := protocol.read(t).Calls
			if mode == "foreign-environment" {
				if len(calls) != 0 {
					t.Fatal("foreign issuer reached native registry")
				}
			} else if len(calls) != 1 || !strings.Contains(calls[0], "FROM pki_crls WHERE issuer_id='11111111-1111-4111-8111-111111111111' ORDER BY number DESC LIMIT 1") {
				t.Fatalf("CRL lookup escaped current original issuer scope: %v", calls)
			}
		})
	}
}
