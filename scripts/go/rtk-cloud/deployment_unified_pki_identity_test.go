package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
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
	if mode == "wrong-purpose" {
		leafTemplate.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	_, leafPEM := create(leafTemplate, issuer, leafKey, issuerKey)
	if mode == "wrong-key" {
		leafKey = key()
	}
	der, e := x509.MarshalPKCS8PrivateKey(leafKey)
	if e != nil {
		t.Fatal(e)
	}
	state := map[string]any{"version": 1, "subject": subject, "domain": domain, "dns_names": []string{subject}, "current": map[string]any{"private_key_pem": string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})), "certificate_chain_pem": string(leafPEM) + string(issuerPEM) + string(rootPEM), "installed_at": now}, "private-canary": "do-not-report-private-state"}
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
		for _, mode := range []string{"valid", "expired", "wrong-san", "wrong-key", "wrong-root", "wrong-purpose", "pending", "partial-report", "bad-fingerprint", "private-report", "owner-replaced", "pod-drift", "controller-drift", "missing-pod", "foreign-parent", "wrong-claim", "missing-replica", "denied-exec", "inspector-unavailable", "ephemeral-state", "incomplete-policy", "stateful", "daemon", "mqtt", "openbao", "cancelled"} {
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
				state, pin := nativeServerStateFixture(t, domain, subject, mode)
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
				if mode == "incomplete-policy" {
					deploymentplan.Map(env[0])["name"] = "UNKNOWN_HOST_IDENTITY_STATE"
				}
				container := deploymentplan.Object{"name": "certissuer", "image": "fixture@sha256:" + strings.Repeat("a", 64), "env": env, "volumeMounts": []any{deploymentplan.Object{"name": "state", "mountPath": directory}}}
				podSpec := deploymentplan.Object{"containers": []any{container}, "volumes": []any{deploymentplan.Object{"name": "state", "persistentVolumeClaim": deploymentplan.Object{"claimName": claimName}}}}
				controller := deploymentplan.Object{"apiVersion": "apps/v1", "kind": kind, "metadata": deploymentplan.Object{"name": "certissuer", "namespace": namespace, "uid": "controller-original", "resourceVersion": "1", "labels": deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-" + environment}}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Clone(podSpec)}}, "status": deploymentplan.Object{"desiredNumberScheduled": float64(1)}}
				if mode == "stateful" {
					environmentPKIPod(controller)["volumes"] = []any{}
					deploymentplan.Map(controller["spec"])["volumeClaimTemplates"] = []any{deploymentplan.Object{"metadata": deploymentplan.Object{"name": "state"}, "spec": deploymentplan.Object{"accessModes": []any{"ReadWriteOnce"}}}}
				}
				if mode == "ephemeral-state" {
					environmentPKIPod(controller)["volumes"] = []any{deploymentplan.Object{"name": "state", "emptyDir": deploymentplan.Object{}}}
				}
				pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "certissuer-0", "namespace": namespace, "uid": "pod-original", "ownerReferences": []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": kind, "name": "certissuer", "uid": "controller-original", "controller": true}}}, "spec": podSpec, "status": deploymentplan.Object{"phase": "Running"}}
				claim := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": claimName, "namespace": namespace, "uid": "claim-original", "resourceVersion": "1", "labels": deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-" + environment}}, "spec": deploymentplan.Object{"volumeName": "pv-original", "accessModes": []any{"ReadWriteOnce"}}, "status": deploymentplan.Object{"phase": "Bound"}}
				volume := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": "pv-original", "uid": "volume-original"}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"name": claimName, "namespace": namespace, "uid": "claim-original"}}, "status": deploymentplan.Object{"phase": "Bound"}}
				objects := map[string]deploymentplan.Object{deploymentplan.ID(controller): controller, deploymentplan.ID(pod): pod, deploymentplan.ID(claim): claim, deploymentplan.ID(volume): volume}
				if kind == "Deployment" {
					rs := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": deploymentplan.Object{"name": "certissuer-selected", "namespace": namespace, "uid": "replicaset-original", "ownerReferences": deploymentplan.Map(pod["metadata"])["ownerReferences"]}}
					objects[deploymentplan.ID(rs)] = rs
					deploymentplan.Map(pod["metadata"])["ownerReferences"] = []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": "certissuer-selected", "uid": "replicaset-original", "controller": true}}
				}
				plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: "video-cloud-" + environment, Operation: "upgrade"}, Resources: []deploymentplan.Resource{{ID: deploymentplan.ID(controller), UID: "controller-original", Action: "retain", Desired: deploymentplan.Projection(controller), Baseline: deploymentplan.Projection(controller)}, {ID: deploymentplan.ID(claim), UID: "claim-original", Action: "retain", Desired: deploymentplan.Projection(claim), Baseline: deploymentplan.Projection(claim)}}}
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
  if obj and kind=='Pod' and mode=='pod-drift': obj['metadata']['uid']='replacement'
  if obj and kind in ['Deployment','StatefulSet','DaemonSet'] and mode=='controller-drift': obj['spec']['template']['spec']['containers'][0]['env'].append({'name':'changed','value':'parallel'})
  print(json.dumps(obj) if obj else '',end='')
else:
 if 'selfsubjectaccessreviews' in ' '.join(args):
  attrs=json.load(sys.stdin)['spec']['resourceAttributes']; assert attrs['resource']=='pods' and attrs['namespace']==os.environ['RTK_NATIVE_IDENTITY_NS'] and attrs['name']=='certissuer-0'; assert (attrs['verb'],attrs['subresource']) in [('get',''),('create','exec')]; denied=mode=='denied-exec' and attrs['subresource']=='exec'; print(json.dumps({'status':{'allowed':not denied,'denied':denied}}))
 elif 'exec' in args:
  open(os.environ['RTK_NATIVE_IDENTITY_EXECUTED'],'a').write('inspect-server\n')
  if mode=='inspector-unavailable': print('private-diagnostic-canary',file=sys.stderr);sys.exit(1)
  cmd=args[args.index('--')+1:]; cmd[0]=os.environ['RTK_NATIVE_IDENTITY_INSPECTOR']; result=subprocess.run(cmd,capture_output=True)
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
				e = backend.CheckManagedServerIdentities(ctx, plan)
				allowed := keySet("valid", "stateful", "daemon", "mqtt", "openbao")[mode]
				if (e == nil) != allowed {
					t.Fatal("incorrect native server qualification", mode, e)
				}
				if e != nil {
					var b *deploymentplan.Blocker
					if !errors.As(e, &b) || b.Next == "" || strings.Contains(e.Error(), "private-diagnostic-canary") || strings.Contains(e.Error(), "do-not-report-private-state") {
						t.Fatal("unsanitized or unactionable failure", e)
					}
				}
				if keySet("expired", "wrong-san", "wrong-key", "wrong-root", "wrong-purpose", "pending", "partial-report", "bad-fingerprint", "private-report")[mode] {
					if _, err := os.Stat(filepath.Join(directory, "executed")); err != nil {
						t.Fatal("failure did not exercise the native inspector", err)
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
