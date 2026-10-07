package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func nativeServerStateFixture(t *testing.T, domain, subject, mode string) ([]byte, string) {
	t.Helper()
	now := time.Now().UTC()
	key := func() *ecdsa.PrivateKey {
		k, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if e != nil {
			t.Fatal(e)
		}
		return k
	}
	rootKey, issuerKey, leafKey := key(), key(), key()
	create := func(c, parent *x509.Certificate, k, parentKey *ecdsa.PrivateKey) (*x509.Certificate, []byte) {
		raw, e := x509.CreateCertificate(rand.Reader, c, parent, &k.PublicKey, parentKey)
		if e != nil {
			t.Fatal(e)
		}
		cert, e := x509.ParseCertificate(raw)
		if e != nil {
			t.Fatal(e)
		}
		return cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: raw})
	}
	rootTemplate := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "fixture-root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(5, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, SubjectKeyId: []byte{1, 2, 3}}
	root, rootPEM := create(rootTemplate, rootTemplate, rootKey, rootKey)
	issuer, issuerPEM := create(&x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "fixture-issuer"}, NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(1, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageCRLSign, SubjectKeyId: []byte{4, 5, 6}}, root, issuerKey, rootKey)
	leafTemplate := &x509.Certificate{SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: subject}, DNSNames: []string{subject}, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(7 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, BasicConstraintsValid: true}
	if mode == "expired" {
		leafTemplate.NotBefore = now.Add(-48 * time.Hour)
		leafTemplate.NotAfter = now.Add(-time.Hour)
	}
	if mode == "wrong-san" {
		leafTemplate.DNSNames = []string{"other.example.test"}
	}
	if domain == "" {
		leafTemplate.DNSNames = nil
		leafTemplate.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	if mode == "wrong-purpose" {
		usage := x509.ExtKeyUsageClientAuth
		if domain == "" {
			usage = x509.ExtKeyUsageServerAuth
		}
		leafTemplate.ExtKeyUsage = []x509.ExtKeyUsage{usage}
	}
	_, leafPEM := create(leafTemplate, issuer, leafKey, issuerKey)
	if mode == "wrong-key" {
		leafKey = key()
	}
	der, e := x509.MarshalPKCS8PrivateKey(leafKey)
	if e != nil {
		t.Fatal(e)
	}
	names := []string{subject}
	if domain == "" {
		names = nil
	}
	state := map[string]any{"version": 1, "subject": subject, "domain": domain, "dns_names": names, "current": map[string]any{"private_key_pem": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "certificate_chain_pem": string(leafPEM) + string(issuerPEM) + string(rootPEM), "installed_at": now}, "private-canary": "do-not-report-private-state"}
	if mode == "pending" {
		state["pending"] = map[string]any{"request_id": "original-pending", "private_key_pem": "private-canary", "csr_pem": "pending-csr"}
	}
	raw, e := json.Marshal(state)
	if e != nil {
		t.Fatal(e)
	}
	sum := sha256.Sum256(root.Raw)
	return raw, hex.EncodeToString(sum[:])
}

func TestNativeManagedServerInspectionUsesRealCryptoAndOriginalOwners(t *testing.T) {
	workspace, e := workspaceRoot()
	if e != nil {
		t.Fatal(e)
	}
	inspector := filepath.Join(t.TempDir(), "serviceidentity-bootstrap")
	build := exec.Command("go", "build", "-o", inspector, "./cmd/serviceidentity-bootstrap")
	build.Dir = filepath.Join(workspace, "repos", "rtk_video_cloud")
	build.Env = append(os.Environ(), "GOWORK=off", "GOTOOLCHAIN=local")
	if output, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build maintained native inspector: %v\n%s", e, output)
	}
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "expired", "wrong-san", "wrong-key", "wrong-root", "wrong-purpose", "pending", "partial-report", "bad-fingerprint", "private-report", "owner-replaced", "pod-drift", "controller-drift", "missing-pod", "foreign-parent", "wrong-claim", "missing-replica", "denied-exec", "inspector-unavailable", "ephemeral-state", "incomplete-policy", "stateful", "daemon", "mqtt", "openbao", "client", "client-expired", "client-wrong-key", "client-wrong-purpose", "client-pending", "registry-unavailable", "client-registry-unavailable", "secret-root", "configmap-root", "envfrom-policy", "source-drift", "changed-root-source", "missing-source", "foreign-source", "envfrom-shadowed", "envfrom-latest", "client-secret-root", "client-configmap-root", "client-envfrom-policy", "cancelled"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				store := makeIsolatedTestSecretStore(t, environment)
				raw, e := json.Marshal(frozenKubernetesFixture(t))
				if e != nil {
					t.Fatal(e)
				}
				backend, e := newEnvironmentKubernetesBackend(string(raw), environment)
				if e != nil {
					t.Fatal(e)
				}
				domain, prefix := "service", "CERT_ISSUER"
				if mode == "mqtt" {
					domain, prefix = "mqtt", "EMQX_PKI"
				}
				if mode == "openbao" {
					domain, prefix = "openbao_tls", "OPENBAO_PKI"
				}
				subject := "certissuer.video-cloud-" + environment + ".example.test"
				clients := mode == "client" || strings.HasPrefix(mode, "client-")
				workloadName, containerName := "certissuer", "certissuer"
				cryptoMode := mode
				if clients {
					domain, prefix, subject = "", "PKI_SERVICE_CLIENT", "service:pki-controller"
					workloadName, containerName = "pki-controller", "pki-controller"
					cryptoMode = strings.TrimPrefix(mode, "client-")
				}
				state, pin := nativeServerStateFixture(t, domain, subject, cryptoMode)
				directory := t.TempDir()
				statePath := filepath.Join(directory, "identity.json")
				if e := os.WriteFile(statePath, state, 0600); e != nil {
					t.Fatal(e)
				}
				if mode == "wrong-root" {
					pin = strings.Repeat("a", 64)
				}
				namespace, kind := store.Environment, "Deployment"
				namespace = "video-cloud-" + environment + "-video-cloud"
				if mode == "stateful" {
					kind = "StatefulSet"
				}
				if mode == "daemon" {
					kind = "DaemonSet"
				}
				claimName := "state"
				if mode == "stateful" {
					claimName = "state-certissuer-0"
				}
				env := []any{}
				for key, value := range map[string]string{"_HOST_IDENTITY_STATE": statePath, "_HOST_NAME": subject, "_HOST_ROOT_SHA256": pin, "_HOST_DNS_NAMES": subject} {
					env = append(env, deploymentplan.Object{"name": prefix + key, "value": value})
				}
				if clients {
					env = []any{deploymentplan.Object{"name": prefix + "_IDENTITY_STATE", "value": statePath}, deploymentplan.Object{"name": prefix + "_ROOT_SHA256", "value": pin}}
				}
				if mode == "incomplete-policy" {
					deploymentplan.Map(env[0])["name"] = "UNKNOWN_HOST_IDENTITY_STATE"
				}
				container := deploymentplan.Object{"name": containerName, "image": "fixture@sha256:" + strings.Repeat("a", 64), "env": env, "volumeMounts": []any{deploymentplan.Object{"name": "state", "mountPath": directory}}}
				podSpec := deploymentplan.Object{"containers": []any{container}, "volumes": []any{deploymentplan.Object{"name": "state", "persistentVolumeClaim": deploymentplan.Object{"claimName": claimName}}}}
				controller := deploymentplan.Object{"apiVersion": "apps/v1", "kind": kind, "metadata": deploymentplan.Object{"name": workloadName, "namespace": namespace, "uid": "controller-original", "resourceVersion": "1", "labels": deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-" + environment}}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Clone(podSpec)}}, "status": deploymentplan.Object{"desiredNumberScheduled": float64(1)}}
				if mode == "stateful" {
					environmentPKIPod(controller)["volumes"] = []any{}
					deploymentplan.Map(controller["spec"])["volumeClaimTemplates"] = []any{deploymentplan.Object{"metadata": deploymentplan.Object{"name": "state"}, "spec": deploymentplan.Object{"accessModes": []any{"ReadWriteOnce"}}}}
				}
				if mode == "ephemeral-state" {
					environmentPKIPod(controller)["volumes"] = []any{deploymentplan.Object{"name": "state", "emptyDir": deploymentplan.Object{}}}
				}
				pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "certissuer-0", "namespace": namespace, "uid": "pod-original", "ownerReferences": []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": kind, "name": workloadName, "uid": "controller-original", "controller": true}}}, "spec": podSpec, "status": deploymentplan.Object{"phase": "Running"}}
				claim := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": claimName, "namespace": namespace, "uid": "claim-original", "resourceVersion": "1", "labels": deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-" + environment}}, "spec": deploymentplan.Object{"volumeName": "pv-original", "accessModes": []any{"ReadWriteOnce"}}, "status": deploymentplan.Object{"phase": "Bound"}}
				volume := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": "pv-original", "uid": "volume-original"}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"name": claimName, "namespace": namespace, "uid": "claim-original"}}, "status": deploymentplan.Object{"phase": "Bound"}}
				objects := map[string]deploymentplan.Object{deploymentplan.ID(controller): controller, deploymentplan.ID(pod): pod, deploymentplan.ID(claim): claim, deploymentplan.ID(volume): volume}
				if kind == "Deployment" {
					rs := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": deploymentplan.Object{"name": "certissuer-selected", "namespace": namespace, "uid": "replicaset-original", "ownerReferences": deploymentplan.Map(pod["metadata"])["ownerReferences"]}}
					objects[deploymentplan.ID(rs)] = rs
					deploymentplan.Map(pod["metadata"])["ownerReferences"] = []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": "certissuer-selected", "uid": "replicaset-original", "controller": true}}
				}
				plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: "video-cloud-" + environment, Operation: "upgrade"}, Resources: []deploymentplan.Resource{{ID: deploymentplan.ID(controller), UID: "controller-original", Action: "retain", Desired: deploymentplan.Projection(controller), Baseline: deploymentplan.Projection(controller)}, {ID: deploymentplan.ID(claim), UID: "claim-original", Action: "retain", Desired: deploymentplan.Projection(claim), Baseline: deploymentplan.Projection(claim)}}}
				referenceMode := strings.TrimPrefix(mode, "client-")
				if keySet("secret-root", "configmap-root", "envfrom-policy", "source-drift", "changed-root-source", "missing-source", "foreign-source", "envfrom-shadowed", "envfrom-latest")[referenceMode] {
					sourceKind := "Secret"
					if referenceMode == "configmap-root" {
						sourceKind = "ConfigMap"
					}
					rootSettingName := prefix + "_HOST_ROOT_SHA256"
					if clients {
						rootSettingName = prefix + "_ROOT_SHA256"
					}
					data := deploymentplan.Object{rootSettingName: pin, "DATABASE_URL": "private-diagnostic-canary"}
					if sourceKind == "Secret" {
						for key, text := range data {
							data[key] = base64.StdEncoding.EncodeToString([]byte(text.(string)))
						}
					}
					source := deploymentplan.Object{"apiVersion": "v1", "kind": sourceKind, "metadata": deploymentplan.Object{"name": "identity-policy", "namespace": namespace, "uid": "source-original", "resourceVersion": "1", "labels": deploymentplan.Object{"rtk.realtek.com/stack": plan.Stack}}, "data": data}
					objects[deploymentplan.ID(source)] = source
					planned := deploymentplan.Resource{ID: deploymentplan.ID(source), UID: "source-original", Action: "retain", Desired: deploymentplan.Projection(source), Baseline: deploymentplan.Projection(source)}
					if mode == "changed-root-source" {
						planned.Action = "update"
						deploymentplan.Map(planned.Desired["data"])[rootSettingName] = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("f", 64)))
					}
					if mode == "foreign-source" {
						deploymentplan.Map(source["metadata"])["uid"] = "foreign-source"
					}
					if mode == "missing-source" {
						delete(objects, deploymentplan.ID(source))
					}
					plan.Resources = append(plan.Resources, planned)
					if referenceMode == "envfrom-policy" || referenceMode == "envfrom-shadowed" || referenceMode == "envfrom-latest" {
						container["envFrom"] = []any{deploymentplan.Object{"secretRef": deploymentplan.Object{"name": "identity-policy"}}}
						if referenceMode == "envfrom-latest" {
							earlier := deploymentplan.Clone(source)
							deploymentplan.Map(earlier["metadata"])["name"] = "identity-policy-earlier"
							deploymentplan.Map(earlier["metadata"])["uid"] = "source-earlier"
							deploymentplan.Map(earlier["data"])[rootSettingName] = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("f", 64)))
							objects[deploymentplan.ID(earlier)] = earlier
							plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: deploymentplan.ID(earlier), UID: "source-earlier", Action: "retain", Desired: deploymentplan.Projection(earlier), Baseline: deploymentplan.Projection(earlier)})
							container["envFrom"] = []any{deploymentplan.Object{"secretRef": deploymentplan.Object{"name": "identity-policy-earlier"}}, deploymentplan.Object{"secretRef": deploymentplan.Object{"name": "identity-policy"}}}
						}

						if referenceMode == "envfrom-policy" || referenceMode == "envfrom-latest" {
							for index, item := range env {
								if deploymentplan.Map(item)["name"] == rootSettingName {
									container["env"] = append(env[:index:index], env[index+1:]...)
									break
								}
							}
						} else {
							deploymentplan.Map(source["data"])[rootSettingName] = base64.StdEncoding.EncodeToString([]byte(strings.Repeat("f", 64)))
							plan.Resources[len(plan.Resources)-1].Desired = deploymentplan.Projection(source)
							plan.Resources[len(plan.Resources)-1].Baseline = deploymentplan.Projection(source)
						}
					} else {
						selector := "secretKeyRef"
						if sourceKind == "ConfigMap" {
							selector = "configMapKeyRef"
						}
						for _, item := range env {
							if deploymentplan.Map(item)["name"] == rootSettingName {
								delete(deploymentplan.Map(item), "value")
								deploymentplan.Map(item)["valueFrom"] = deploymentplan.Object{selector: deploymentplan.Object{"name": "identity-policy", "key": rootSettingName}}
							}
						}
					}
					environmentPKIPod(controller)["containers"] = []any{deploymentplan.Clone(container)}
					podSpec["containers"] = []any{container}
					plan.Resources[0].Desired = deploymentplan.Projection(controller)
					plan.Resources[0].Baseline = deploymentplan.Projection(controller)
				}
				switch mode {
				case "owner-replaced":
					deploymentplan.Map(controller["metadata"])["uid"] = "replacement"
				case "missing-pod":
					delete(objects, deploymentplan.ID(pod))
				case "foreign-parent":
					deploymentplan.Map(deploymentplan.Map(pod["metadata"])["ownerReferences"].([]any)[0])["uid"] = "foreign"
				case "wrong-claim":
					deploymentplan.Map(deploymentplan.Map(podSpec["volumes"].([]any)[0])["persistentVolumeClaim"])["claimName"] = "replacement"
				case "missing-replica":
					deploymentplan.Map(controller["spec"])["replicas"] = float64(2)
					plan.Resources[0].Desired = deploymentplan.Projection(controller)
					plan.Resources[0].Baseline = deploymentplan.Projection(controller)
				}
				raw, _ = json.Marshal(objects)
				writeTestFile(t, filepath.Join(directory, "objects.json"), string(raw))
				api := `import json,os,subprocess,sys
args=sys.argv[1:]; objects=json.load(open(os.environ['RTK_NATIVE_IDENTITY_OBJECTS'])); mode=os.environ['RTK_NATIVE_IDENTITY_MODE']
i=args.index('get') if 'get' in args else -1
if i>=0:
 kind=args[i+1]
 if kind=='namespace': print(json.dumps({'items':[{'apiVersion':'v1','kind':'Namespace','metadata':{'name':os.environ['RTK_NATIVE_IDENTITY_NS'],'uid':'namespace-original'}}]}))
 elif kind.startswith('clusterrole,'): print(json.dumps({'items':[]}))
 elif ',' in kind: print(json.dumps({'items':[o for o in objects.values() if o['metadata'].get('namespace')==os.environ['RTK_NATIVE_IDENTITY_NS']]}))
 else:
  ns=args[args.index('--namespace')+1] if '--namespace' in args else ''; obj=objects.get(kind+'/'+ns+'/'+args[i+2])
  if obj and kind in ['Secret','ConfigMap'] and mode=='source-drift' and os.path.exists(os.environ['RTK_NATIVE_IDENTITY_EXECUTED']): obj['metadata']['resourceVersion']='2'
  if obj and kind=='Pod' and mode=='pod-drift': obj['metadata']['uid']='replacement'
  if obj and kind in ['Deployment','StatefulSet','DaemonSet'] and mode=='controller-drift': obj['spec']['template']['spec']['containers'][0]['env'].append({'name':'changed','value':'parallel'})
  print(json.dumps(obj) if obj else '',end='')
else:
 if 'selfsubjectaccessreviews' in ' '.join(args):
  attrs=json.load(sys.stdin)['spec']['resourceAttributes']; assert attrs['resource']=='pods' and attrs['namespace']==os.environ['RTK_NATIVE_IDENTITY_NS'] and attrs['name']=='certissuer-0'; assert (attrs['verb'],attrs['subresource']) in [('get',''),('create','exec')]; denied=mode=='denied-exec' and attrs['subresource']=='exec'; print(json.dumps({'status':{'allowed':not denied,'denied':denied}}))
 elif 'exec' in args:
  open(os.environ['RTK_NATIVE_IDENTITY_EXECUTED'],'a').write('inspect-server\n')
  if mode=='inspector-unavailable': print('private-diagnostic-canary',file=sys.stderr);sys.exit(1)
  cmd=args[args.index('--')+1:]
  if mode in ['registry-unavailable','client-registry-unavailable']: assert cmd[1] in ['inspect-registry','inspect-server-registry'] and cmd[2]==os.environ['RTK_NATIVE_IDENTITY_ENV'] and cmd[-2]=='1800' and cmd[-1]==('PKI_DATABASE_URL' if mode=='client-registry-unavailable' else 'CERT_ISSUER_DB_DSN')
  cmd[0]=os.environ['RTK_NATIVE_IDENTITY_INSPECTOR']; result=subprocess.run(cmd,capture_output=True)
  if result.returncode: sys.stderr.buffer.write(result.stderr);sys.exit(result.returncode)
  report=json.loads(result.stdout)
  if mode=='partial-report': report.pop('pending')
  if mode=='bad-fingerprint': report['fingerprint']='z'*64
  if mode=='private-report': report['private_key_pem']='private-diagnostic-canary'
  print(json.dumps(report))
 else: sys.exit(2)
`
				writeTestFile(t, filepath.Join(directory, "api.py"), api)
				writeTestFile(t, filepath.Join(directory, "kubectl"), "#!/bin/sh\nexec python3 \"$RTK_NATIVE_IDENTITY_API\" \"$@\"\n")
				if e := os.Chmod(filepath.Join(directory, "kubectl"), 0700); e != nil {
					t.Fatal(e)
				}
				t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("RTK_NATIVE_IDENTITY_EXECUTED", filepath.Join(directory, "executed"))
				t.Setenv("RTK_NATIVE_IDENTITY_API", filepath.Join(directory, "api.py"))
				t.Setenv("RTK_NATIVE_IDENTITY_OBJECTS", filepath.Join(directory, "objects.json"))
				t.Setenv("RTK_NATIVE_IDENTITY_MODE", mode)
				t.Setenv("RTK_NATIVE_IDENTITY_NS", namespace)
				t.Setenv("RTK_NATIVE_IDENTITY_ENV", environment)
				t.Setenv("RTK_NATIVE_IDENTITY_INSPECTOR", inspector)
				bindings, e := backend.storageBindings(context.Background(), plan)
				if e != nil {
					t.Fatal(e)
				}
				plan.StorageSHA256 = deploymentplan.Digest(bindings)
				approved := plan.SHA256()
				before := readTestFile(t, statePath)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				if mode == "cancelled" {
					cancel()
				}
				if mode == "registry-unavailable" {
					t.Setenv("PKI_DATABASE_URL", "")
					t.Setenv("CERT_ISSUER_DB_DSN", "")
					e = backend.CheckManagedServerRegistry(ctx, plan, 1800)
				} else if mode == "client-registry-unavailable" {
					t.Setenv("PKI_DATABASE_URL", "")
					t.Setenv("CERT_ISSUER_DB_DSN", "")
					e = backend.CheckManagedClientRegistry(ctx, plan, 1800)
				} else if clients {
					e = backend.CheckManagedClientIdentities(ctx, plan)
				} else {
					e = backend.CheckManagedServerIdentities(ctx, plan)
				}
				allowed := keySet("valid", "stateful", "daemon", "mqtt", "openbao", "client", "secret-root", "configmap-root", "envfrom-policy", "envfrom-shadowed", "envfrom-latest", "client-secret-root", "client-configmap-root", "client-envfrom-policy")[mode]
				if (e == nil) != allowed {
					t.Fatal("incorrect native server qualification", mode, e)
				}
				if e != nil {
					var b *deploymentplan.Blocker
					if !errors.As(e, &b) || b.Next == "" || strings.Contains(e.Error(), "private-diagnostic-canary") || strings.Contains(e.Error(), "do-not-report-private-state") {
						t.Fatal("unsanitized or unactionable failure", e)
					}
					if mode == "registry-unavailable" && b.ID != "deployment.pki.server-registry" || mode == "client-registry-unavailable" && b.ID != "deployment.pki.client-registry" {
						t.Fatal("registry failure lost its stable check ID", b.ID)
					}
				}
				if keySet("expired", "wrong-san", "wrong-key", "wrong-root", "wrong-purpose", "pending", "partial-report", "bad-fingerprint", "private-report", "client-expired", "client-wrong-key", "client-wrong-purpose", "client-pending", "registry-unavailable", "client-registry-unavailable", "source-drift")[mode] {
					if _, err := os.Stat(filepath.Join(directory, "executed")); err != nil {
						t.Fatal("failure did not exercise the native inspector", err)
					}
				}
				if keySet("missing-source", "foreign-source", "changed-root-source")[mode] {
					if _, err := os.Stat(filepath.Join(directory, "executed")); !os.IsNotExist(err) {
						t.Fatal("unqualified public setting source reached identity inspection")
					}
				}
				if before != readTestFile(t, statePath) || plan.SHA256() != approved {
					t.Fatal("read-only qualification changed identity or plan")
				}
				if _, e := os.Lstat(statePath + ".lock"); !os.IsNotExist(e) {
					t.Fatal("inspection created a state lock")
				}
			})
		}
	}
}

func TestClientPolicyMatchesMaintainedConsumerSubjectsAndRootAliases(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, consumer := range []struct {
			prefix, workload, subject string
			alias                     bool
		}{
			{"CERT_ISSUER_SERVICE_CLIENT", "certissuer", "service:certissuer", false},
			{"PKI_SERVICE_CLIENT", "pki-controller", "service:pki-controller", false},
			{"EMQX_PKI_SERVICE_CLIENT", "mqtt-pki", "service:emqx-pki", false},
			{"OPENBAO_PKI_SERVICE_CLIENT", "openbao", "service:openbao", false},
			{"PKI_BROKER", "mqtt-pki", "service:pkibroker", false},
			{"VIDEO_CLOUD_CONTROLLER", "video-cloud-api-pki", "service:video-cloud-api", true},
			{"VIDEO_CLOUD_ACCOUNT_MANAGER", "video-cloud-api-pki", "service:video-cloud-api", true},
			{"VIDEO_CLOUD_LOG_INGESTER_MQTT", "video-cloud-logingester", "service:video-cloud-logingester", true},
		} {
			t.Run(environment+"/"+consumer.prefix, func(t *testing.T) {
				rootKey := consumer.prefix + "_ROOT_SHA256"
				if consumer.alias {
					rootKey = consumer.prefix + "_IDENTITY_ROOT_SHA256"
				}
				root := strings.Repeat("a", 64)
				object := deploymentplan.Object{"metadata": deploymentplan.Object{"name": consumer.workload, "namespace": "video-cloud-" + environment + "-video-cloud"}}
				settings := []any{deploymentplan.Object{"name": consumer.prefix + "_IDENTITY_STATE", "value": "/identity/client.json"}, deploymentplan.Object{"name": rootKey, "value": root}}
				container := deploymentplan.Object{"env": settings}
				policies, err := environmentClientIdentityPolicies(object, container)
				database := "PKI_DATABASE_URL"
				if consumer.prefix == "CERT_ISSUER_SERVICE_CLIENT" {
					database = "CERT_ISSUER_DB_DSN"
				}
				if consumer.alias {
					database = "VIDEO_CLOUD_DB_DSN"
				}
				if err != nil || len(policies) != 1 || policies[0].Database != database || policies[0].Subject != consumer.subject || policies[0].Root != root || policies[0].State != "/identity/client.json" {
					t.Fatal("maintained client policy changed", err)
				}
				deploymentplan.Map(object["metadata"])["name"] = "unregistered-consumer"
				if _, err := environmentClientIdentityPolicies(object, container); err == nil {
					t.Fatal("a different consumer borrowed the Service subject")
				}
				deploymentplan.Map(object["metadata"])["name"] = consumer.workload
				deploymentplan.Map(settings[1])["valueFrom"] = deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "unresolved", "key": "root"}}
				if _, err := environmentClientIdentityPolicies(object, container); err == nil {
					t.Fatal("unresolved root policy accepted")
				}
			})
		}
	}
}

func TestPKIUpgradeWindowIsMaintainedConfigurationAndHasNoImplicitDefault(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "missing", "zero", "negative", "overflow", "noncanonical", "private-canary"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				value := map[string]string{"valid": "1800", "missing": "", "zero": "0", "negative": "-1", "overflow": "9223372036854775807", "noncanonical": "01800", "private-canary": "do-not-report-private-state"}[mode]
				window, err := environmentPKIValidityWindow(map[string]string{"CLOUD_ENV_NAME": environment, "DEPLOYMENT_UPGRADE_WINDOW_SECONDS": value})
				if (err == nil) != (mode == "valid") {
					t.Fatalf("wrong upgrade window qualification: %v", err)
				}
				if mode == "valid" && window != 1800 {
					t.Fatal("reviewed window changed")
				}
				if err != nil {
					var block *deploymentplan.Blocker
					if !errors.As(err, &block) || block.ID != "deployment.pki.validity-window" || block.Next == "" || strings.Contains(err.Error(), "do-not-report-private-state") {
						t.Fatal("unsanitized/unactionable window failure", err)
					}
				}
			})
		}
	}
}

func TestUpgradeWindowResolvesThroughSharedConfigurationAndEnvironmentOverride(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		workspace := writeDeploymentFixture(t, environment, "lke")
		architecture := filepath.Join(workspace, "cloud_deploy", "architectures", "kubernetes", "architecture.env")
		original, err := os.ReadFile(architecture)
		if err != nil {
			t.Fatal(err)
		}
		writeTestFile(t, architecture, string(original)+"\nDEPLOYMENT_UPGRADE_WINDOW_SECONDS=1800\n")
		cfg, err := resolveDeploymentConfig(workspace, environment, "")
		if err != nil {
			t.Fatal(err)
		}
		window, err := environmentPKIValidityWindow(cfg.Values)
		if err != nil || window != 1800 {
			t.Fatal("shared window not selected", err)
		}
		writeTestFile(t, filepath.Join(workspace, "cloud_env", environment, "overrides", "architecture.env"), "DEPLOYMENT_UPGRADE_WINDOW_SECONDS=3600\n")
		cfg, err = resolveDeploymentConfig(workspace, environment, "")
		if err != nil {
			t.Fatal(err)
		}
		window, err = environmentPKIValidityWindow(cfg.Values)
		if err != nil || window != 3600 {
			t.Fatal("reviewed environment override not selected", err)
		}
	}
}

func TestRegistrySummaryRequiresExactReviewedWindowAndPublicFields(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "missing-window", "wrong-window", "zero-window", "null-window", "missing-registry", "false-registry", "private-extra"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				digest := strings.Repeat("a", 64)
				policy := environmentServerIdentityPolicy{Subject: "host.video-cloud-" + environment + ".example.test", Root: digest}
				report := map[string]any{"subject": policy.Subject, "root_sha256": digest, "fingerprint": digest, "public_key_sha256": digest, "state_sha256": digest, "pending": false, "registry_verified": true, "minimum_validity_seconds": int64(1800)}
				switch mode {
				case "missing-window":
					delete(report, "minimum_validity_seconds")
				case "wrong-window":
					report["minimum_validity_seconds"] = int64(600)
				case "zero-window":
					report["minimum_validity_seconds"] = int64(0)
				case "null-window":
					report["minimum_validity_seconds"] = nil
				case "missing-registry":
					delete(report, "registry_verified")
				case "false-registry":
					report["registry_verified"] = false
				case "private-extra":
					report["private_key_pem"] = "do-not-report-private-state"
				}
				raw, err := json.Marshal(report)
				if err != nil {
					t.Fatal(err)
				}
				err = validateEnvironmentIdentitySummary(raw, policy, true, 1800)
				if (err == nil) != (mode == "valid") || err != nil && strings.Contains(err.Error(), "do-not-report-private-state") {
					t.Fatal("unqualified or unsanitized registry protocol", err)
				}
			})
		}
	}
}
