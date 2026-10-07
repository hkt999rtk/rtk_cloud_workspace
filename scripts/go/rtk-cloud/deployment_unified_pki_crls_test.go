package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Registry signatures and SQL receipts are tested with the real shipped command
// in Video Cloud. Here the remote command is a protocol mock: test exact planned
// mounts, source/Pod ownership and coverage without claiming live crypto proof.
func TestOriginalCRLProbeBindsManifestClaimsReportsAndAllRequiredConsumers(t *testing.T) {
	testOriginalConsumerTrustProbe(t, false)
}
func TestOriginalRootProbeBindsPolicyStateProvisionedOwnerAndReceiptCoverage(t *testing.T) {
	testOriginalConsumerTrustProbe(t, true)
}
func testOriginalConsumerTrustProbe(t *testing.T, roots bool) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "wrong-manifest", "wrong-window", "missing-consumer", "source-drift", "wrong-pod-mount", "wrong-claim", "private-extra"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				makeIsolatedTestSecretStore(t, environment)
				raw, _ := json.Marshal(frozenKubernetesFixture(t))
				backend, err := newEnvironmentKubernetesBackend(string(raw), environment)
				if err != nil {
					t.Fatal(err)
				}
				stack, namespace := "video-cloud-"+environment, "video-cloud-"+environment+"-platform"
				plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: stack, Operation: "upgrade"}}
				objects := map[string]deploymentplan.Object{}
				manifests := map[string]string{}
				for _, workload := range []string{"pki-controller", "video-cloud-api-pki"} {
					env := []any{}
					domains := map[string][]string{"PKI_SERVICE_CLIENT_SERVER_CRL_MANIFEST": {"service"}, "OPENBAO_SERVER_CRL_MANIFEST": {"openbao_tls"}}
					if workload == "video-cloud-api-pki" {
						env = append(env, deploymentplan.Object{"name": "VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED", "value": "true"}, deploymentplan.Object{"name": "VIDEO_CLOUD_AUTH_APP_PKI_ENABLED", "value": "true"})
						domains = map[string][]string{"VIDEO_CLOUD_AUTH_CRL_MANIFEST": {"device", "app"}, "VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST": {"mqtt"}}
					}
					data := deploymentplan.Object{}
					for setting, ds := range domains {
						path := "/run/manifests/" + setting + ".json"
						env = append(env, deploymentplan.Object{"name": setting, "value": path})
						entries := []any{}
						for _, domain := range ds {
							for _, kind := range []string{"root", "issuer"} {
								entries = append(entries, map[string]any{"issuer": map[string]any{"environment": environment, "domain": domain, "issuer_id": domain + "-" + kind, "kind": kind}, "state_path": "/run/state/" + domain + "-" + kind + ".json"})
							}
						}
						raw, _ := json.Marshal(entries)
						data[setting+".json"] = string(raw)
						manifests[path] = string(raw)
						if roots {
							for _, domain := range ds {
								idSetting, stateSetting, rootsSetting := environmentRootTrustSettings(setting, domain)
								rootPath := "/run/manifests/" + domain + ".roots.pem"
								env = append(env, deploymentplan.Object{"name": idSetting, "value": domain + "-root"}, deploymentplan.Object{"name": stateSetting, "value": "/run/state/" + domain + "-policy.json"}, deploymentplan.Object{"name": rootsSetting, "value": rootPath})
								data[domain+".roots.pem"] = "fixture-roots-" + domain + "\n"
								manifests[rootPath] = data[domain+".roots.pem"].(string)
							}
						}

					}
					container := deploymentplan.Object{"name": workload, "env": env, "image": "fixture@sha256:" + strings.Repeat("a", 64), "volumeMounts": []any{deploymentplan.Object{"name": "state", "mountPath": "/run/state"}, deploymentplan.Object{"name": "manifests", "mountPath": "/run/manifests", "readOnly": true}}}
					spec := deploymentplan.Object{"containers": []any{container}, "volumes": []any{deploymentplan.Object{"name": "state", "persistentVolumeClaim": deploymentplan.Object{"claimName": workload + "-state"}}, deploymentplan.Object{"name": "manifests", "configMap": deploymentplan.Object{"name": workload + "-manifests"}}}}
					metadata := func(name, uid string) deploymentplan.Object {
						return deploymentplan.Object{"name": name, "namespace": namespace, "uid": uid, "resourceVersion": "1", "labels": deploymentplan.Object{"rtk.realtek.com/stack": stack}}
					}
					controller := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": metadata(workload, workload+"-uid"), "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Clone(spec)}}}
					owner := func(kind, name, uid string) []any {
						return []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": kind, "name": name, "uid": uid, "controller": true}}
					}
					rs := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": metadata(workload+"-rs", workload+"-rs-uid")}
					deploymentplan.Map(rs["metadata"])["ownerReferences"] = owner("Deployment", workload, workload+"-uid")
					pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": metadata(workload+"-pod", workload+"-pod-uid"), "spec": spec, "status": deploymentplan.Object{"phase": "Running"}}
					deploymentplan.Map(pod["metadata"])["ownerReferences"] = owner("ReplicaSet", workload+"-rs", workload+"-rs-uid")
					cm := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": metadata(workload+"-manifests", workload+"-manifest-uid"), "data": data}
					claim := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": metadata(workload+"-state", workload+"-claim-uid"), "spec": deploymentplan.Object{"volumeName": workload + "-pv", "accessModes": []any{"ReadWriteOnce"}}, "status": deploymentplan.Object{"phase": "Bound"}}
					pv := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": workload + "-pv", "uid": workload + "-volume-uid"}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"namespace": namespace, "name": workload + "-state", "uid": workload + "-claim-uid"}}, "status": deploymentplan.Object{"phase": "Bound"}}
					for _, object := range []deploymentplan.Object{controller, cm, claim} {
						plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: deploymentplan.ID(object), UID: deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"]), Action: "retain", Desired: deploymentplan.Projection(object), Baseline: deploymentplan.Projection(object)})
					}
					for _, object := range []deploymentplan.Object{controller, rs, pod, cm, claim, pv} {
						objects[deploymentplan.ID(object)] = object
					}
					if workload == "pki-controller" && mode == "wrong-pod-mount" {
						deploymentplan.Map(deploymentplan.Map(spec["volumes"].([]any)[1])["configMap"])["name"] = "foreign-manifest"
					}
					if workload == "pki-controller" && mode == "wrong-claim" {
						deploymentplan.Map(deploymentplan.Map(spec["volumes"].([]any)[0])["persistentVolumeClaim"])["claimName"] = "foreign-state"
					}
				}
				values := map[string]string{"DEPLOYMENT_UPGRADE_WINDOW_SECONDS": "1800", "PKI_REQUIRED_CONSUMERS_DEVICE": "video-cloud-api", "PKI_REQUIRED_CONSUMERS_APP": "video-cloud-api", "PKI_REQUIRED_CONSUMERS_SERVICE": "pki-controller", "PKI_REQUIRED_CONSUMERS_MQTT": "video-cloud-api", "PKI_REQUIRED_CONSUMERS_OPENBAO_TLS": "pki-controller"}
				if mode == "missing-consumer" {
					values["PKI_REQUIRED_CONSUMERS_SERVICE"] += " ,missing"
				}
				directory := t.TempDir()
				raw, _ = json.Marshal(objects)
				writeTestFile(t, filepath.Join(directory, "objects.json"), string(raw))
				raw, _ = json.Marshal(manifests)
				writeTestFile(t, filepath.Join(directory, "manifests.json"), string(raw))
				api := `import hashlib,json,os,sys
args=sys.argv[1:];mode=os.environ['RTK_CRL_MODE'];objects=json.load(open(os.environ['RTK_CRL_OBJECTS']))
if 'get' in args:
 i=args.index('get');kind=args[i+1]
 if kind=='namespace': print(json.dumps({'items':[{'kind':'Namespace','metadata':{'name':os.environ['RTK_CRL_NAMESPACE'],'uid':'namespace-original'}}]}))
 elif kind.startswith('clusterrole,'): print(json.dumps({'items':[]}))
 elif ',' in kind: print(json.dumps({'items':[o for o in objects.values() if o['metadata'].get('namespace')==os.environ['RTK_CRL_NAMESPACE']]}))
 else:
  ns=args[args.index('--namespace')+1] if '--namespace' in args else '';obj=objects.get(kind+'/'+ns+'/'+args[i+2])
  if obj and kind=='ConfigMap' and mode=='source-drift' and os.path.exists(os.environ['RTK_CRL_EXECUTED']):obj['metadata']['resourceVersion']='2'
  print(json.dumps(obj) if obj else '',end='')
elif 'selfsubjectaccessreviews' in ' '.join(args):print(json.dumps({'status':{'allowed':True,'denied':False}}))
elif 'exec' in args:
 command=args[args.index('--')+1:];assert command[0]=='/app/serviceidentity-bootstrap';assert command[1] in ['inspect-crls','inspect-auth-crls','inspect-roots']
 if command[1]=='inspect-roots':
  env,domain,consumer,root,state,path,window,database=command[2:]
 else:env,domain,consumer,path,window,database=command[2:]
 assert window=='1800';assert database==('PKI_DATABASE_URL' if consumer=='pki-controller' else 'VIDEO_CLOUD_DB_DSN')
 open(os.environ['RTK_CRL_EXECUTED'],'a').write(domain+':'+consumer+'\n')
 manifest=json.load(open(os.environ['RTK_CRL_MANIFESTS']))[path]
 if command[1]=='inspect-roots':
  report={'environment':env,'domain':domain,'consumer':consumer,'root_id':root,'state_sha256':'b'*64,'policy_sha256':'b'*64,'loaded_roots_sha256':'b'*64,'provisioned_roots_sha256':hashlib.sha256(manifest.encode()).hexdigest(),'roots_verified':True,'consumers_verified':True,'minimum_validity_seconds':1800}
 else:
  count=sum(1 for e in json.loads(manifest) if e['issuer']['domain']==domain)
  report={'environment':env,'domain':domain,'consumer':consumer,'manifest_sha256':hashlib.sha256(manifest.encode()).hexdigest(),'authorities':count,'crls_verified':True,'consumers_verified':True,'minimum_validity_seconds':1800}
 if mode=='wrong-manifest':report['provisioned_roots_sha256' if command[1]=='inspect-roots' else 'manifest_sha256']='f'*64
 if mode=='wrong-window':report['minimum_validity_seconds']=600
 if mode=='private-extra':report['private_key_pem']='private-diagnostic-canary'
 print(json.dumps(report))
else:sys.exit(2)
`
				writeTestFile(t, filepath.Join(directory, "api.py"), api)
				writeTestFile(t, filepath.Join(directory, "kubectl"), "#!/bin/sh\nexec python3 \"$RTK_CRL_API\" \"$@\"\n")
				if err := os.Chmod(filepath.Join(directory, "kubectl"), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
				for key, value := range map[string]string{"RTK_CRL_API": filepath.Join(directory, "api.py"), "RTK_CRL_OBJECTS": filepath.Join(directory, "objects.json"), "RTK_CRL_MANIFESTS": filepath.Join(directory, "manifests.json"), "RTK_CRL_NAMESPACE": namespace, "RTK_CRL_MODE": mode, "RTK_CRL_EXECUTED": filepath.Join(directory, "executed")} {
					t.Setenv(key, value)
				}
				bindings, err := backend.storageBindings(context.Background(), plan)
				if err != nil {
					t.Fatal(err)
				}
				plan.StorageSHA256 = deploymentplan.Digest(bindings)
				approved := plan.SHA256()
				if roots {
					err = backend.CheckInstalledRootTrust(context.Background(), plan, values)
				} else {
					err = backend.CheckInstalledCRLs(context.Background(), plan, values)
				}
				if (err == nil) != (mode == "valid") {
					t.Fatalf("wrong original CRL probe qualification: %v", err)
				}
				if err != nil {
					var block *deploymentplan.Blocker
					if !errors.As(err, &block) || block.Next == "" || strings.Contains(err.Error(), "private-diagnostic-canary") {
						t.Fatal("unsafe/inactionable CRL failure", err)
					}
				}
				if plan.SHA256() != approved {
					t.Fatal("CRL observation modified reviewed plan")
				}
				if mode == "valid" {
					executed := readTestFile(t, filepath.Join(directory, "executed"))
					for _, domain := range managedPKIDomains {
						if !strings.Contains(executed, domain+":") {
							t.Fatal("required domain was not inspected", domain)
						}
					}
				}
			})
		}
	}
}

func TestPublicManifestReferenceRequiresExactFileProjection(t *testing.T) {
	for _, kind := range []string{"Secret", "ConfigMap"} {
		for _, mode := range []string{"directory", "items", "subpath", "unresolved", "subpath-expression", "wrong-file"} {
			source := deploymentplan.Object{"name": "owner"}
			volume := deploymentplan.Object{"name": "manifest"}
			if kind == "Secret" {
				source = deploymentplan.Object{"secretName": "owner"}
				volume["secret"] = source
			} else {
				volume["configMap"] = source
			}
			mount := deploymentplan.Object{"name": "manifest", "mountPath": "/manifest", "readOnly": true}
			path := "/manifest/data.json"
			key := "data.json"
			if mode == "items" {
				source["items"] = []any{deploymentplan.Object{"key": "mapped.json", "path": "data.json"}}
				key = "mapped.json"
			}
			if mode == "subpath" {
				mount["mountPath"] = "/manifest/data.json"
				mount["subPath"] = "mapped.json"
				key = "mapped.json"
			}
			if mode == "unresolved" {
				volume = deploymentplan.Object{"name": "manifest", "emptyDir": deploymentplan.Object{}}
			}
			if mode == "subpath-expression" {
				mount["subPathExpr"] = "$(POD_NAME)"
			}
			if mode == "wrong-file" {
				source["items"] = []any{deploymentplan.Object{"key": "other.json", "path": "other.json"}}
			}
			ref, err := environmentPublicFileReference(deploymentplan.Object{"volumes": []any{volume}}, deploymentplan.Object{"volumeMounts": []any{mount}}, path)
			allowed := mode == "directory" || mode == "items" || mode == "subpath"
			if (err == nil) != allowed || allowed && (ref.Kind != kind || ref.Name != "owner" || ref.Key != key) {
				t.Fatal("wrong manifest file ownership mapping", kind, mode, err)
			}
		}
	}
	// Manifest hashing is raw-byte equality, not a reformatted JSON interpretation.
	raw := []byte("[]\n")
	sum := sha256.Sum256(raw)
	if hex.EncodeToString(sum[:]) == deploymentplan.Digest([]any{}) {
		t.Fatal("manifest bytes confused with JSON value hashing")
	}
}
