package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type registrationPublicHealthFixture struct {
	cfg                    deploymentConfig
	store                  secretStore
	account, listener, pod deploymentplan.Object
	objects                map[string]deploymentplan.Object
	public                 []byte
	now                    time.Time
	states                 map[string][]byte
	execs                  int
}

func newRegistrationPublicHealthFixture(t *testing.T) *registrationPublicHealthFixture {
	t.Helper()
	cfg, p, s, cert, key, crl, defs := accountRegistrationFixture(t)
	out, err := renderEnvironmentCreateAccountRegistration(cfg, p, s, cert, key, crl, defs)
	if err != nil {
		t.Fatal(err)
	}
	account := deploymentplan.Clone(createRecipeObject(t, out, "Deployment/"+p.Stack+"-account-manager/account-manager"))
	image := "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:" + strings.Repeat("d", 64)
	next, err := environmentAccountRegistrationCRLRefreshDefinition(cfg, account, image)
	if err != nil {
		t.Fatal(err)
	}
	account = next.Object
	deploymentplan.Map(account["metadata"])["uid"] = "account-uid"
	listener := deploymentplan.Clone(createRecipeObject(t, out, "Secret/"+p.Stack+"-account-manager/"+serviceRegistrationTLSSecretName))
	data := deploymentplan.Object{}
	for k, v := range deploymentplan.Map(listener["stringData"]) {
		data[k] = base64.StdEncoding.EncodeToString([]byte(deploymentplan.String(v)))
	}
	delete(listener, "stringData")
	listener["data"] = data
	// Deliberately unusable old compatibility seed: only the new public file is
	// current authority, while cert/key/CA remain the original fixture bytes.
	data["client.crl"] = base64.StdEncoding.EncodeToString([]byte("expired compatibility seed"))
	f := &registrationPublicHealthFixture{cfg: cfg, store: secretStore{Root: t.TempDir(), Environment: "dev"}, account: account, listener: listener, objects: map[string]deploymentplan.Object{}, public: []byte(s.RootCRLPEM + crl), now: time.Now(), states: map[string][]byte{}}
	f.store.ConfigRoot = f.store.Root
	for k, v := range map[string]string{"ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE": accountRegistrationCRLRefreshProfile, "PKI_MANAGEMENT_IMAGE": image} {
		if err := f.store.write("operator/env/"+k, []byte(v), false); err != nil {
			t.Fatal(err)
		}
	}
	ns := p.Stack + "-account-manager"
	f.pod = deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "account-pod", "namespace": ns, "uid": "pod-uid", "ownerReferences": []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": "account-rs", "uid": "rs-uid", "controller": true}}}, "spec": deploymentplan.Clone(environmentPKIPod(account)), "status": deploymentplan.Object{"phase": "Running", "conditions": []any{deploymentplan.Object{"type": "Ready", "status": "True"}}}}
	f.objects[deploymentplan.ID(account)] = account
	f.objects[deploymentplan.ID(f.pod)] = f.pod
	f.objects["ReplicaSet/"+ns+"/account-rs"] = deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "rs-uid", "ownerReferences": []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "name": "account-manager", "uid": "account-uid", "controller": true}}}}
	var typed liveDeployment
	if err := json.Unmarshal(mustProviderRecoveryJSON(account), &typed); err != nil {
		t.Fatal(err)
	}
	manager := managedNamedField(environmentPKIPod(account)["containers"], "pkimanagement")
	cm, mkey, err := mountedPKICRLConfigMap(typed, "pkimanagement", mqttFoundationEnv(manager, "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST"))
	if err != nil {
		t.Fatal(err)
	}
	mount := ""
	for _, m := range certIssuerObjectList(manager["volumeMounts"]) {
		x := deploymentplan.Map(m)
		for _, v := range certIssuerObjectList(environmentPKIPod(account)["volumes"]) {
			y := deploymentplan.Map(v)
			if x["name"] == y["name"] && y["persistentVolumeClaim"] != nil && x["readOnly"] != true {
				mount = deploymentplan.String(x["mountPath"])
			}
		}
	}
	if mount == "" {
		t.Fatal("no original state mount")
	}
	entries := []any{}
	for i, pem := range []string{s.Root.Certificate, s.Intermediate.Certificate} {
		c, err := parseFirstCertificatePEM([]byte(pem))
		if err != nil {
			t.Fatal(err)
		}
		entries = append(entries, deploymentplan.Object{"issuer": deploymentplan.Object{"issuer_id": fmt.Sprintf("64062c4a-2bdc-474c-bb11-ab1b6102657%d", i), "environment": "dev", "trust_domain": "service", "status": "active", "certificate_fingerprint_sha256": environmentPreflightBytesSHA256(c.Raw), "certificate_pem": pem}, "state_path": fmt.Sprintf("%s/crls/issuer%d.json", mount, i)})
	}
	lists, _ := pemCRLs(f.public)
	for i, raw := range entries {
		e := deploymentplan.Map(raw)
		issuer := deploymentplan.Map(e["issuer"])
		list := lists[i]
		sum := sha256.Sum256(list.Raw)
		f.states[deploymentplan.String(e["state_path"])] = mustProviderRecoveryJSON(deploymentplan.Object{"issuer_fingerprint": issuer["certificate_fingerprint_sha256"], "crl": deploymentplan.Object{"issuer_id": issuer["issuer_id"], "crl_sha256": hex.EncodeToString(sum[:]), "crl_number": list.Number.String(), "crl_pem": pemCRLForHealthTest(list.Raw), "this_update": list.ThisUpdate, "next_update": list.NextUpdate}})
	}
	f.objects["ConfigMap/"+ns+"/"+cm] = deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": cm, "namespace": ns, "uid": "manifest-uid"}, "data": deploymentplan.Object{mkey: string(mustProviderRecoveryJSON(entries))}}
	return f
}
func (f *registrationPublicHealthFixture) read(_ context.Context, id string) (deploymentplan.Object, error) {
	return deploymentplan.Clone(f.objects[id]), nil
}
func (f *registrationPublicHealthFixture) command(_ context.Context, args ...string) ([]byte, error) {
	if len(args) >= 4 && args[2] == "get" && args[3] == "pods" {
		return mustProviderRecoveryJSON(deploymentplan.Object{"items": []any{f.pod}}), nil
	}
	if len(args) == 9 && args[2] == "exec" && args[3] == "pod/account-pod" && args[4] == "-c" && args[5] == "pkimanagement" && args[6] == "--" && args[7] == "cat" {
		if v, ok := f.states[args[8]]; ok {
			return v, nil
		}
	}
	if len(args) == 9 && args[2] == "exec" && args[3] == "pod/account-pod" && args[4] == "-c" && args[5] == "pkimanagement" && args[6] == "--" && args[7] == "cat" && args[8] == accountRegistrationCRLBundlePath {
		f.execs++
		return f.public, nil
	}
	return nil, fmt.Errorf("unexpected public-only command")
}
func (f *registrationPublicHealthFixture) verify() (deploymentplan.Object, error) {
	return readAccountRegistrationPublicListener(context.Background(), f.cfg, f.store, f.account, f.listener, f.now, f.read, f.command)
}

func TestAccountRegistrationPublicCRLHealthUsesVerifiedLiveExport(t *testing.T) {
	f := newRegistrationPublicHealthFixture(t)
	before := deploymentplan.Digest(f.listener)
	got, err := f.verify()
	if err != nil {
		t.Fatal(err)
	}
	if f.execs != 1 || deploymentplan.Digest(f.listener) != before {
		t.Fatal("live public read mutated original")
	}
	if err := validateRegistrationServerMaterialAt(got, lkeRegistrationServerDNS(f.cfg.Values), f.now); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"tls.crt", "tls.key", "client-ca.crt"} {
		if deploymentplan.Map(got["data"])[k] != deploymentplan.Map(f.listener["data"])[k] {
			t.Fatal("original listener identity changed")
		}
	}
}
func TestAccountRegistrationPublicCRLHealthNativePodDefaults(t *testing.T) {
	for _, mode := range []string{"empty-value-omitted", "read-only-false-omitted"} {
		t.Run(mode, func(t *testing.T) {
			f := newRegistrationPublicHealthFixture(t)
			p := deploymentplan.Map(f.pod["spec"])
			switch mode {
			case "empty-value-omitted":
				for _, v := range certIssuerObjectList(p["containers"]) {
					for _, v := range certIssuerObjectList(deploymentplan.Map(v)["env"]) {
						e := deploymentplan.Map(v)
						if e["value"] == "" {
							delete(e, "value")
						}
					}
				}
			case "read-only-false-omitted":
				for _, v := range certIssuerObjectList(p["containers"]) {
					for _, v := range certIssuerObjectList(deploymentplan.Map(v)["volumeMounts"]) {
						e := deploymentplan.Map(v)
						if e["readOnly"] == false {
							delete(e, "readOnly")
						}
					}
				}
			}
			if _, err := f.verify(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestAccountRegistrationPublicCRLHealthRefusesWrongSourceOrOwner(t *testing.T) {
	for _, mode := range []string{"expired-output", "not-yet-valid", "missing-issuer", "duplicate-issuer", "foreign-signature", "malformed-output", "missing-profile", "unknown-profile", "wrong-image", "foreign-deployment-uid", "missing-known-uid", "foreign-rs", "foreign-controller", "not-ready", "extra-container", "extra-mount", "value-from-instead-empty", "changed-init", "changed-pod-uid", "undeclared-export-path", "current-state-differs"} {
		t.Run(mode, func(t *testing.T) {
			f := newRegistrationPublicHealthFixture(t)
			p := deploymentplan.Map(f.pod["spec"])
			read := f.read
			switch mode {
			case "current-state-differs":
				for k := range f.states {
					f.states[k] = []byte(`{"issuer_fingerprint":"foreign"}`)
				}
			case "expired-output":
				f.now = f.now.Add(2 * time.Hour)
			case "not-yet-valid":
				f.now = f.now.Add(-2 * time.Hour)
			case "missing-issuer":
				lists, _ := pemCRLs(f.public)
				f.public = []byte(pemCRLForHealthTest(lists[0].Raw))
			case "duplicate-issuer":
				f.public = append(f.public, f.public...)
			case "foreign-signature":
				_, state, _ := testLiveCRLEvidence(t, "service", false)
				var x struct {
					CRL struct {
						PEM string `json:"crl_pem"`
					} `json:"crl"`
				}
				json.Unmarshal([]byte(state), &x)
				f.public = []byte(x.CRL.PEM)
			case "malformed-output":
				f.public = []byte("not a CRL")
			case "missing-profile":
				f.store.write("operator/env/ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE", []byte(""), true)
			case "unknown-profile":
				f.store.write("operator/env/ACCOUNT_MANAGER_SERVICE_REGISTRATION_CRL_REFRESH_PROFILE", []byte("other"), true)
			case "wrong-image":
				f.store.write("operator/env/PKI_MANAGEMENT_IMAGE", []byte("example.invalid/latest"), true)
			case "foreign-deployment-uid":
				want := deploymentplan.Clone(f.account)
				deploymentplan.Map(want["metadata"])["uid"] = "foreign"
				f.account = want
			case "missing-known-uid":
				want := deploymentplan.Clone(f.account)
				delete(deploymentplan.Map(want["metadata"]), "uid")
				f.account = want
			case "foreign-rs":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(f.pod["metadata"])["ownerReferences"])[0])["uid"] = "foreign"
			case "foreign-controller":
				for id, o := range f.objects {
					if strings.HasPrefix(id, "ReplicaSet/") {
						deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(o["metadata"])["ownerReferences"])[0])["uid"] = "foreign"
					}
				}
			case "not-ready":
				deploymentplan.Map(f.pod["status"])["conditions"] = []any{}
			case "extra-container":
				p["containers"] = append(certIssuerObjectList(p["containers"]), deploymentplan.Object{"name": "unowned", "image": "unowned"})
			case "extra-mount":
				c := managedNamedField(p["containers"], "app")
				c["volumeMounts"] = append(certIssuerObjectList(c["volumeMounts"]), deploymentplan.Object{"name": "private", "mountPath": "/private"})
			case "value-from-instead-empty":
				c := managedNamedField(p["containers"], "app")
				found := false
				for _, raw := range certIssuerObjectList(c["env"]) {
					e := deploymentplan.Map(raw)
					if e["name"] == "APP_CERT_ISSUER_CLIENT_CERT" {
						delete(e, "value")
						e["valueFrom"] = deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "private", "key": "key"}}
						found = true
					}
				}
				if !found {
					t.Fatal("real empty managed issuer field absent")
				}
			case "changed-init":
				deploymentplan.Map(certIssuerObjectList(p["initContainers"])[0])["command"] = []any{"sh", "-ec", "cat /private/key"}
			case "changed-pod-uid":
				read = func(ctx context.Context, id string) (deploymentplan.Object, error) {
					o, e := f.read(ctx, id)
					if strings.HasPrefix(id, "Pod/") {
						deploymentplan.Map(o["metadata"])["uid"] = "replacement"
					}
					return o, e
				}
			case "undeclared-export-path":
				registrationCRLSetEnvironment(managedNamedField(p["containers"], "pkimanagement"), accountRegistrationCRLExportEnv, "/private/key")
			}
			if _, err := readAccountRegistrationPublicListener(context.Background(), f.cfg, f.store, f.account, f.listener, f.now, read, f.command); err == nil {
				t.Fatal("accepted mismatched current public export/owner")
			}
		})
	}
}
func TestAccountRegistrationPublicCRLHealthLegacyHasNoNewRuntimeRead(t *testing.T) {
	s := secretStore{Root: t.TempDir(), Environment: "dev"}
	if err := verifyAccountRegistrationPublicCRLRuntime(s, time.Now(), liveDeploymentList{}); err != nil {
		t.Fatal(err)
	}
	f := newRegistrationPublicHealthFixture(t)
	var d liveDeployment
	json.Unmarshal(mustProviderRecoveryJSON(f.account), &d)
	if err := verifyAccountRegistrationPublicCRLRuntime(s, time.Now(), liveDeploymentList{Items: []liveDeployment{d}}); err == nil {
		t.Fatal("orphan public profile accepted")
	}
}

func pemCRLForHealthTest(raw []byte) string {
	return string(pem.EncodeToMemory(&pem.Block{Type: "X509 CRL", Bytes: raw}))
}

func TestAccountRegistrationPublicCRLHealthRejectsUnselectedAdmissionToken(t *testing.T) {
	f := newRegistrationPublicHealthFixture(t)
	p := deploymentplan.Map(f.pod["spec"])
	if environmentPKIPod(f.account)["automountServiceAccountToken"] != false {
		t.Fatal("maintained AM boundary lost automount refusal")
	}
	p["volumes"] = append(certIssuerObjectList(p["volumes"]), deploymentplan.Object{"name": "kube-api-access-abcde", "projected": deploymentplan.Object{"defaultMode": float64(0644), "sources": []any{deploymentplan.Object{"serviceAccountToken": deploymentplan.Object{"expirationSeconds": float64(3607), "path": "token"}}, deploymentplan.Object{"configMap": deploymentplan.Object{"name": "kube-root-ca.crt", "items": []any{deploymentplan.Object{"key": "ca.crt", "path": "ca.crt"}}}}, deploymentplan.Object{"downwardAPI": deploymentplan.Object{"items": []any{deploymentplan.Object{"fieldRef": deploymentplan.Object{"apiVersion": "v1", "fieldPath": "metadata.namespace"}, "path": "namespace"}}}}}}})
	for _, field := range []string{"containers", "initContainers"} {
		for _, raw := range certIssuerObjectList(p[field]) {
			c := deploymentplan.Map(raw)
			c["volumeMounts"] = append(certIssuerObjectList(c["volumeMounts"]), deploymentplan.Object{"name": "kube-api-access-abcde", "mountPath": "/var/run/secrets/kubernetes.io/serviceaccount", "readOnly": true})
		}
	}
	if _, err := f.verify(); err == nil {
		t.Fatal("public-only AM profile accepted admission token")
	}
}

func TestAccountRegistrationPublicCRLHealthOrdinaryRuntimeAndScopedBackend(t *testing.T) {
	f := newRegistrationPublicHealthFixture(t)
	ns := f.cfg.Values["CLOUD_STACK_NAME"] + "-account-manager"
	rs := f.objects["ReplicaSet/"+ns+"/account-rs"]
	rs["apiVersion"] = "apps/v1"
	rs["kind"] = "ReplicaSet"
	deploymentplan.Map(rs["metadata"])["name"] = "account-rs"
	deploymentplan.Map(rs["metadata"])["namespace"] = ns
	f.objects["Secret/"+ns+"/"+serviceRegistrationTLSSecretName] = f.listener
	// Same public/native fake boundary as the shared health tests. All production
	// owner, profile, file, cryptographic and existing listener checks execute.
	dir := t.TempDir()
	payload := map[string]any{"objects": f.objects, "pod": f.pod, "public": string(f.public), "states": map[string]string{}}
	for k, v := range f.states {
		payload["states"].(map[string]string)[k] = string(v)
	}
	if err := os.WriteFile(filepath.Join(dir, "payload.json"), mustProviderRecoveryJSON(payload), 0600); err != nil {
		t.Fatal(err)
	}
	script := `#!/usr/bin/env python3
import json,os,sys
p=json.load(open(os.environ['RTK_ACCOUNT_PUBLIC_HEALTH_PAYLOAD']))
a=sys.argv[1:]
ns=a[a.index('-n')+1] if '-n' in a else a[a.index('--namespace')+1] if '--namespace' in a else ''
if 'get' in a:
 i=a.index('get');kind=a[i+1].lower()
 if kind=='pods':print(json.dumps({'items':[p['pod']]}));sys.exit(0)
 name=a[i+2]
 matches=[v for k,v in p['objects'].items() if k.split('/')[0].lower()==kind and k.split('/')[1:]==[ns,name]]
 if len(matches)!=1:sys.exit(91)
 print(json.dumps(matches[0]));sys.exit(0)
if 'exec' in a and a[-2]=='cat':
 path=a[-1]
 if path=='/run/account-pki/private/service-registration.crl.pem':print(p['public']);sys.exit(0)
 if path in p['states']:print(p['states'][path]);sys.exit(0)
sys.exit(92)
`
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_ACCOUNT_PUBLIC_HEALTH_PAYLOAD", filepath.Join(dir, "payload.json"))
	var d liveDeployment
	json.Unmarshal(mustProviderRecoveryJSON(f.account), &d)
	if err := verifyAccountRegistrationPublicCRLRuntime(f.store, f.now, liveDeploymentList{Items: []liveDeployment{d}}); err != nil {
		t.Fatal(err)
	}
	b := environmentKubernetesBackend{Store: f.store, KubeconfigContent: []byte("isolated synthetic health transport"), RolloutTimeout: time.Second}
	if err := verifyAccountRegistrationPublicCRLForBackend(context.Background(), f.cfg, f.store, b, f.account, f.now); err != nil {
		t.Fatal(err)
	}
}

func TestAccountRegistrationPublicCRLHealthSnapshotAdvance(t *testing.T) {
	for _, mode := range []string{"normal-advance", "persistent-mismatch", "cancel-during-advance"} {
		t.Run(mode, func(t *testing.T) {
			f := newRegistrationPublicHealthFixture(t)
			certPEM, cert, key := testSigningCA(t, "additional approved public fixture issuer", nil, nil, 42)
			makeList := func(n int64) []byte {
				der, err := x509.CreateRevocationList(rand.Reader, &x509.RevocationList{Number: big.NewInt(n), ThisUpdate: f.now.Add(-time.Minute), NextUpdate: f.now.Add(time.Hour)}, cert, key)
				if err != nil {
					t.Fatal(err)
				}
				return []byte(pemCRLForHealthTest(der))
			}
			older, newer := makeList(7), makeList(8)
			data := deploymentplan.Map(f.listener["data"])
			ca, _ := base64.StdEncoding.DecodeString(deploymentplan.String(data["client-ca.crt"]))
			data["client-ca.crt"] = base64.StdEncoding.EncodeToString(append(ca, []byte(certPEM)...))
			var source deploymentplan.Object
			var sourceKey string
			var entries []any
			for id, o := range f.objects {
				if strings.HasPrefix(id, "ConfigMap/") {
					source = o
					for k, v := range deploymentplan.Map(o["data"]) {
						sourceKey = k
						json.Unmarshal([]byte(deploymentplan.String(v)), &entries)
					}
				}
			}
			path := ""
			for k := range f.states {
				path = filepath.Dir(k) + "/coherent.json"
				break
			}
			issuerID := "64062c4a-2bdc-474c-bb11-ab1b61026579"
			fingerprint := environmentPreflightBytesSHA256(cert.Raw)
			entries = append(entries, deploymentplan.Object{"issuer": deploymentplan.Object{"issuer_id": issuerID, "environment": "dev", "trust_domain": "service", "status": "active", "certificate_fingerprint_sha256": fingerprint, "certificate_pem": certPEM}, "state_path": path})
			deploymentplan.Map(source["data"])[sourceKey] = string(mustProviderRecoveryJSON(entries))
			installed, _ := pemCRLs(newer)
			list := installed[0]
			digest := sha256.Sum256(list.Raw)
			f.states[path] = mustProviderRecoveryJSON(deploymentplan.Object{"issuer_fingerprint": fingerprint, "crl": deploymentplan.Object{"issuer_id": issuerID, "crl_sha256": hex.EncodeToString(digest[:]), "crl_number": list.Number.String(), "crl_pem": string(newer), "this_update": list.ThisUpdate, "next_update": list.NextUpdate}})
			base := append([]byte{}, f.public...)
			f.public = append(append([]byte{}, base...), older...)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			command := func(ctx context.Context, args ...string) ([]byte, error) {
				if args[len(args)-1] == accountRegistrationCRLBundlePath && f.execs > 0 && mode == "normal-advance" {
					f.public = append(append([]byte{}, base...), newer...)
				}
				out, err := f.command(ctx, args...)
				if args[len(args)-1] == accountRegistrationCRLBundlePath && mode == "cancel-during-advance" {
					cancel()
				}
				return out, err
			}
			_, err := readAccountRegistrationPublicListener(ctx, f.cfg, f.store, f.account, f.listener, f.now, f.read, command)
			switch mode {
			case "normal-advance":
				if err != nil || f.execs != 2 {
					t.Fatalf("coherent new snapshot not accepted: reads=%d err=%v", f.execs, err)
				}
			case "persistent-mismatch":
				if err == nil || f.execs != 3 {
					t.Fatalf("persistent mismatch did not fail bounded: reads=%d err=%v", f.execs, err)
				}
			case "cancel-during-advance":
				if !errors.Is(err, context.Canceled) || f.execs != 1 {
					t.Fatal("snapshot retry lost cancellation", err)
				}
			}
		})
	}
}
