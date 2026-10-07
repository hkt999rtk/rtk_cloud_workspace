package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Native key/chain/registry verification lives in the service's PostgreSQL tests.
// This protocol fixture verifies the planner's original-owner and file bindings.
func TestSecretServerProbeRequiresExactOriginalProjectionAndIndependentPolicy(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "value-from", "env-from", "no-secret", "managed-host", "missing-policy", "stale-binding", "wrong-cert", "wrong-key", "wrong-window", "source-drift", "wrong-mount", "private-report", "missing-pod"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				makeIsolatedTestSecretStore(t, environment)
				raw, _ := json.Marshal(frozenKubernetesFixture(t))
				backend, err := newEnvironmentKubernetesBackend(string(raw), environment)
				if err != nil {
					t.Fatal(err)
				}
				stack := "video-cloud-" + environment
				namespace := stack + "-platform"
				metadata := func(name, uid string) deploymentplan.Object {
					return deploymentplan.Object{"name": name, "namespace": namespace, "uid": uid, "resourceVersion": "1", "labels": deploymentplan.Object{"rtk.realtek.com/stack": stack}}
				}
				env := []any{deploymentplan.Object{"name": "CERT_ISSUER_SERVER_CERT", "value": "/run/tls/tls.crt"}, deploymentplan.Object{"name": "CERT_ISSUER_SERVER_KEY", "value": "/run/tls/tls.key"}}
				if mode == "no-secret" || mode == "stale-binding" {
					env = nil
				}
				if mode == "managed-host" {
					env = append(env, deploymentplan.Object{"name": "CERT_ISSUER_HOST_IDENTITY_STATE", "value": "/run/state/identity.json"})
				}
				container := deploymentplan.Object{"name": "certissuer", "image": "fixture@sha256:" + strings.Repeat("a", 64), "env": env, "volumeMounts": []any{deploymentplan.Object{"name": "tls", "mountPath": "/run/tls", "readOnly": true}}}
				settings := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": metadata("paths", "paths-uid"), "data": deploymentplan.Object{"CERT_ISSUER_SERVER_CERT": "/run/tls/tls.crt", "CERT_ISSUER_SERVER_KEY": "/run/tls/tls.key"}}
				if mode == "value-from" {
					for _, item := range env {
						setting := deploymentplan.Map(item)
						delete(setting, "value")
						setting["valueFrom"] = deploymentplan.Object{"configMapKeyRef": deploymentplan.Object{"name": "paths", "key": setting["name"]}}
					}
				}
				if mode == "env-from" {
					container["env"] = []any{}
					container["envFrom"] = []any{deploymentplan.Object{"configMapRef": deploymentplan.Object{"name": "paths"}}}
				}
				spec := deploymentplan.Object{"containers": []any{container}, "volumes": []any{deploymentplan.Object{"name": "tls", "secret": deploymentplan.Object{"secretName": "server-tls"}}}}
				controller := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": metadata("certissuer", "controller-uid"), "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Clone(spec)}}}
				owner := func(kind, name, uid string) []any {
					return []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": kind, "name": name, "uid": uid, "controller": true}}
				}
				rs := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": metadata("certissuer-rs", "rs-uid")}
				deploymentplan.Map(rs["metadata"])["ownerReferences"] = owner("Deployment", "certissuer", "controller-uid")
				pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": metadata("certissuer-pod", "pod-uid"), "spec": spec, "status": deploymentplan.Object{"phase": "Running"}}
				deploymentplan.Map(pod["metadata"])["ownerReferences"] = owner("ReplicaSet", "certissuer-rs", "rs-uid")
				secret := deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": metadata("server-tls", "secret-uid"), "data": deploymentplan.Object{"tls.crt": base64.StdEncoding.EncodeToString([]byte("fixture-certificate")), "tls.key": base64.StdEncoding.EncodeToString([]byte("private-diagnostic-canary"))}}
				plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: stack, Operation: "upgrade"}}
				for _, object := range []deploymentplan.Object{controller, secret, settings} {
					plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: deploymentplan.ID(object), UID: deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"]), Action: "retain", Desired: deploymentplan.Projection(object), Baseline: deploymentplan.Projection(object)})
				}
				objects := map[string]deploymentplan.Object{}
				for _, object := range []deploymentplan.Object{controller, rs, pod, secret, settings} {
					objects[deploymentplan.ID(object)] = object
				}
				if mode == "wrong-mount" {
					deploymentplan.Map(deploymentplan.Map(spec["volumes"].([]any)[0])["secret"])["secretName"] = "foreign-secret"
				}
				if mode == "missing-pod" {
					delete(objects, deploymentplan.ID(pod))
				}
				bindings := []environmentSecretServerBinding{{Workload: deploymentplan.ID(controller), Container: "certissuer", Domain: "service", Subject: "certissuer.example.test", Root: strings.Repeat("b", 64), DNSNames: []string{"certissuer.example.test"}, CertificatePath: "/run/tls/tls.crt", KeyPath: "/run/tls/tls.key"}}
				if mode == "missing-policy" || mode == "no-secret" || mode == "managed-host" {
					bindings = nil
				}
				directory := t.TempDir()
				raw, _ = json.Marshal(objects)
				writeTestFile(t, filepath.Join(directory, "objects.json"), string(raw))
				api := `import base64,hashlib,json,os,sys
args=sys.argv[1:]; mode=os.environ['RTK_SECRET_MODE']; objects=json.load(open(os.environ['RTK_SECRET_OBJECTS'])); namespace=os.environ['RTK_SECRET_NAMESPACE']
if 'get' in args:
 i=args.index('get');kind=args[i+1]
 if kind=='namespace': print(json.dumps({'items':[{'kind':'Namespace','metadata':{'name':namespace,'uid':'namespace-uid'}}]}))
 elif kind.startswith('clusterrole,'):print(json.dumps({'items':[]}))
 elif ',' in kind:print(json.dumps({'items':[o for o in objects.values() if o['metadata'].get('namespace')==namespace]}))
 else:
  ns=args[args.index('--namespace')+1] if '--namespace' in args else ''; obj=objects.get(kind+'/'+ns+'/'+args[i+2])
  if obj and kind=='Secret' and mode=='source-drift' and os.path.exists(os.environ['RTK_SECRET_EXECUTED']):obj['metadata']['resourceVersion']='2'
  print(json.dumps(obj) if obj else '',end='')
elif 'selfsubjectaccessreviews' in ' '.join(args):print(json.dumps({'status':{'allowed':True,'denied':False}}))
elif 'exec' in args:
 command=args[args.index('--')+1:]; assert command[:2]==['/app/serviceidentity-bootstrap','inspect-secret-server']
 env,domain,subject,root,names,cert,key,window,database=command[2:]
 assert domain=='service' and subject==names=='certissuer.example.test' and root=='b'*64 and window=='1800' and database=='CERT_ISSUER_DB_DSN'
 assert cert=='/run/tls/tls.crt' and key=='/run/tls/tls.key'
 open(os.environ['RTK_SECRET_EXECUTED'],'a').write('verified\n')
 data=objects['Secret/'+namespace+'/server-tls']['data']
 report={'subject':subject,'fingerprint':'c'*64,'public_key_sha256':'d'*64,'root_sha256':root,'certificate_file_sha256':hashlib.sha256(base64.b64decode(data['tls.crt'])).hexdigest(),'key_file_sha256':hashlib.sha256(base64.b64decode(data['tls.key'])).hexdigest(),'registry_verified':True,'minimum_validity_seconds':1800}
 if mode=='wrong-cert':report['certificate_file_sha256']='f'*64
 if mode=='wrong-key':report['key_file_sha256']='f'*64
 if mode=='wrong-window':report['minimum_validity_seconds']=600
 if mode=='private-report':report['private_key_pem']='private-diagnostic-canary'
 print(json.dumps(report))
else:sys.exit(2)
`
				writeTestFile(t, filepath.Join(directory, "api.py"), api)
				writeTestFile(t, filepath.Join(directory, "kubectl"), "#!/bin/sh\nexec python3 \"$RTK_SECRET_API\" \"$@\"\n")
				if err := os.Chmod(filepath.Join(directory, "kubectl"), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
				for key, value := range map[string]string{"RTK_SECRET_API": filepath.Join(directory, "api.py"), "RTK_SECRET_OBJECTS": filepath.Join(directory, "objects.json"), "RTK_SECRET_NAMESPACE": namespace, "RTK_SECRET_MODE": mode, "RTK_SECRET_EXECUTED": filepath.Join(directory, "executed")} {
					t.Setenv(key, value)
				}
				storage, err := backend.storageBindings(context.Background(), plan)
				if err != nil {
					t.Fatal(err)
				}
				plan.StorageSHA256 = deploymentplan.Digest(storage)
				approved := plan.SHA256()
				err = backend.CheckSecretServerIdentities(context.Background(), plan, bindings, 1800)
				pass := keySet("valid", "value-from", "env-from", "no-secret", "managed-host")[mode]
				if (err == nil) != pass {
					t.Fatalf("unexpected Secret identity result: %v", err)
				}
				if err != nil {
					var block *deploymentplan.Blocker
					if !errors.As(err, &block) || block.Next == "" || strings.Contains(err.Error(), "private-diagnostic-canary") {
						t.Fatal("unsafe or inactionable failure", err)
					}
				}
				if approved != plan.SHA256() {
					t.Fatal("read-only observation changed plan")
				}
				if mode == "no-secret" || mode == "managed-host" {
					if _, err := os.Stat(filepath.Join(directory, "executed")); !os.IsNotExist(err) {
						t.Fatal("inactive Secret credential was inspected")
					}
				}
			})
		}
	}
}
