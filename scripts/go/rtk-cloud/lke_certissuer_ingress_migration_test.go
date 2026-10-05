package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type certIssuerMigrationFixture struct {
	objects       map[string]certIssuerIngressObject
	ops           []string
	revision      int
	beforeWrite   func(string, certIssuerIngressObject) error
	beforeRemove  func(certIssuerIngressObject)
	servingError  error
	servingChecks int
}

func certIssuerIngressTestEnv() map[string]string {
	return map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "VIDEO_CLOUD_CERTISSUER_DOMAIN": "certissuer.video-cloud-dev.example.test", "CERTIFICATE_INTERNAL_TLS_KEY_ALGORITHM": "p256"}
}

func certIssuerMigrationFixtureKey(obj certIssuerIngressObject) string {
	meta := certIssuerObjectMap(obj["metadata"])
	return fmt.Sprint(meta["namespace"], "/", meta["name"])
}

func newCertIssuerIngressFixture(objects ...certIssuerIngressObject) *certIssuerMigrationFixture {
	f := &certIssuerMigrationFixture{objects: map[string]certIssuerIngressObject{}, revision: 10}
	for _, object := range objects {
		f.objects[certIssuerMigrationFixtureKey(object)] = certIssuerSanitizeIngress(object)
	}
	return f
}

func (f *certIssuerMigrationFixture) access() certIssuerIngressIO {
	return certIssuerIngressIO{
		list: func() ([]certIssuerIngressObject, error) {
			keys := []string{}
			for key := range f.objects {
				keys = append(keys, key)
			}
			sort.Strings(keys)
			objects := []certIssuerIngressObject{}
			for _, key := range keys {
				objects = append(objects, certIssuerSanitizeIngress(f.objects[key]))
			}
			return objects, nil
		},
		read: func(namespace, name string) (certIssuerIngressObject, error) {
			return certIssuerSanitizeIngress(f.objects[namespace+"/"+name]), nil
		},
		write: func(verb string, object certIssuerIngressObject) (certIssuerIngressObject, error) {
			if f.beforeWrite != nil {
				if err := f.beforeWrite(verb, object); err != nil {
					return nil, err
				}
			}
			key := certIssuerMigrationFixtureKey(object)
			before := f.objects[key]
			if verb == "create" && before != nil {
				return nil, errors.New("AlreadyExists")
			}
			if verb == "replace" && (before == nil || certIssuerObjectMap(before["metadata"])["uid"] != certIssuerObjectMap(object["metadata"])["uid"] || certIssuerObjectMap(before["metadata"])["resourceVersion"] != certIssuerObjectMap(object["metadata"])["resourceVersion"]) {
				return nil, errors.New("Conflict")
			}
			f.ops = append(f.ops, verb+" "+key)
			f.revision++
			after := certIssuerSanitizeIngress(object)
			meta := certIssuerObjectMap(after["metadata"])
			meta["resourceVersion"] = fmt.Sprint(f.revision)
			if verb == "create" {
				meta["uid"] = "created-" + fmt.Sprint(f.revision)
			}
			f.objects[key] = after
			return certIssuerSanitizeIngress(after), nil
		},
		remove: func(object certIssuerIngressObject) error {
			if f.beforeRemove != nil {
				f.beforeRemove(object)
			}
			key := certIssuerMigrationFixtureKey(object)
			before := f.objects[key]
			if before == nil || certIssuerObjectMap(before["metadata"])["uid"] != certIssuerObjectMap(object["metadata"])["uid"] || certIssuerObjectMap(before["metadata"])["resourceVersion"] != certIssuerObjectMap(object["metadata"])["resourceVersion"] {
				return errors.New("Conflict")
			}
			f.ops = append(f.ops, "delete "+key)
			delete(f.objects, key)
			return nil
		},
		serving: func(lkeCertIssuerTLSConfig, bool, bool) error { f.servingChecks++; return f.servingError },
	}
}

func certIssuerLegacyIngress(t *testing.T, mixed bool) certIssuerIngressObject {
	t.Helper()
	env := certIssuerIngressTestEnv()
	obj := certIssuerCanonicalIngress(env, lkeResolveCertIssuerTLSConfig(env))
	meta := certIssuerObjectMap(obj["metadata"])
	meta["name"], meta["namespace"], meta["uid"], meta["resourceVersion"] = "video-cloud-staging-certissuer", lkeIngressNamespace(env), "legacy-uid", "1"
	meta["annotations"] = map[string]any{"nginx.ingress.kubernetes.io/backend-protocol": "HTTPS", "example.test/preserve": "kept", "kubectl.kubernetes.io/last-applied-configuration": "omitted-from-backup"}
	meta["managedFields"] = []any{map[string]any{"manager": "old-operator"}}
	obj["status"] = map[string]any{"loadBalancer": map[string]any{"ingress": []any{map[string]any{"ip": "192.0.2.1"}}}}
	spec := certIssuerObjectMap(obj["spec"])
	rules := certIssuerObjectList(spec["rules"])
	path := certIssuerObjectMap(certIssuerObjectList(certIssuerObjectMap(certIssuerObjectMap(rules[0])["http"])["paths"])[0])
	certIssuerObjectMap(certIssuerObjectMap(path["backend"])["service"])["name"] = "public-certissuer-video-cloud"
	certIssuerObjectMap(certIssuerObjectList(spec["tls"])[0])["secretName"] = "edge-web-pki"
	if mixed {
		other := certIssuerSanitizeIngress(obj)
		otherRule := certIssuerObjectMap(certIssuerObjectList(certIssuerObjectMap(other["spec"])["rules"])[0])
		otherRule["host"] = "other.example.test"
		otherPath := certIssuerObjectMap(certIssuerObjectList(certIssuerObjectMap(otherRule["http"])["paths"])[0])
		certIssuerObjectMap(certIssuerObjectMap(otherPath["backend"])["service"])["name"] = "other-service"
		spec["rules"] = append(rules, otherRule)
		certIssuerObjectMap(certIssuerObjectList(spec["tls"])[0])["hosts"] = []any{env["VIDEO_CLOUD_CERTISSUER_DOMAIN"], "other.example.test"}
	}
	return obj
}

func certIssuerIngressRestoredConfig(obj certIssuerIngressObject) certIssuerIngressObject {
	out := certIssuerSanitizeIngress(obj)
	if out != nil {
		meta := certIssuerObjectMap(out["metadata"])
		delete(meta, "uid")
		delete(meta, "resourceVersion")
	}
	return out
}

func TestCertIssuerIngressMigrationLegacyUpgradeAndRepeat(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		t.Run(fmt.Sprint("mixed-", mixed), func(t *testing.T) {
			env := certIssuerIngressTestEnv()
			legacy := certIssuerLegacyIngress(t, mixed)
			unrelated := certIssuerSanitizeIngress(legacy)
			certIssuerObjectMap(unrelated["metadata"])["name"] = "unrelated"
			rule := certIssuerObjectMap(certIssuerObjectList(certIssuerObjectMap(unrelated["spec"])["rules"])[0])
			rule["host"] = "unrelated.example.test"
			certIssuerObjectMap(unrelated["spec"])["rules"] = []any{rule}
			path := certIssuerObjectMap(certIssuerObjectList(certIssuerObjectMap(rule["http"])["paths"])[0])
			certIssuerObjectMap(certIssuerObjectMap(path["backend"])["service"])["name"] = "unrelated-service"
			f := newCertIssuerIngressFixture(legacy, unrelated)
			paths := provisionPaths{EnvRoot: t.TempDir()}
			p, err := lkePlanCertIssuerIngressMigrationWithIO(paths, env, f.access())
			if err != nil {
				t.Fatal(err)
			}
			if len(f.ops) != 0 {
				t.Fatal("read-only plan mutated Kubernetes")
			}
			if _, err := os.Stat(certIssuerIngressJournalDir(paths)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("read-only plan wrote a journal")
			}
			if err := p.Apply(); err != nil {
				t.Fatal(err)
			}
			if !certIssuerObjectsSame(f.objects[certIssuerMigrationFixtureKey(unrelated)], unrelated) {
				t.Fatal("unrelated ingress changed")
			}
			currentLegacy := f.objects[certIssuerMigrationFixtureKey(legacy)]
			if mixed {
				rules := certIssuerObjectList(certIssuerObjectMap(currentLegacy["spec"])["rules"])
				if len(rules) != 1 || certIssuerObjectString(certIssuerObjectMap(rules[0])["host"]) != "other.example.test" {
					t.Fatal("unrelated legacy host removed")
				}
				tlsEntry := certIssuerObjectMap(certIssuerObjectList(certIssuerObjectMap(currentLegacy["spec"])["tls"])[0])
				if !reflect.DeepEqual(tlsEntry["hosts"], []any{"other.example.test"}) || tlsEntry["secretName"] != "edge-web-pki" {
					t.Fatal("unrelated TLS policy changed")
				}
			} else if currentLegacy != nil {
				t.Fatal("legacy hostname owner remains")
			}
			raw, err := os.ReadFile(p.RestorePath)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "managedFields") || strings.Contains(string(raw), "loadBalancer") || strings.Contains(string(raw), "omitted-from-backup") {
				t.Fatal("journal contains generated metadata")
			}
			beforeOps := len(f.ops)
			repeated, err := lkePlanCertIssuerIngressMigrationWithIO(paths, env, f.access())
			if err != nil {
				t.Fatal(err)
			}
			if len(repeated.Changes) != 0 {
				t.Fatal("repeat planned new mutations")
			}
			if err := repeated.Apply(); err != nil {
				t.Fatal(err)
			}
			if len(f.ops) != beforeOps {
				t.Fatal("repeat changed existing routing")
			}
		})
	}
}

func TestCertIssuerIngressMigrationRejectsForeignOrMismatchedRoute(t *testing.T) {
	for _, scenario := range []string{"foreign-label", "foreign-name", "foreign-namespace", "wrong-host", "wrong-backend", "extra-path", "default-backend", "empty-public-rule", "canonical-host", "canonical-backend"} {
		t.Run(scenario, func(t *testing.T) {
			env := certIssuerIngressTestEnv()
			obj := certIssuerLegacyIngress(t, false)
			if strings.HasPrefix(scenario, "canonical") {
				obj = certIssuerCanonicalIngress(env, lkeResolveCertIssuerTLSConfig(env))
				certIssuerObjectMap(obj["metadata"])["uid"], certIssuerObjectMap(obj["metadata"])["resourceVersion"] = "canonical", "1"
			}
			meta := certIssuerObjectMap(obj["metadata"])
			rule := certIssuerObjectMap(certIssuerObjectList(certIssuerObjectMap(obj["spec"])["rules"])[0])
			path := certIssuerObjectMap(certIssuerObjectList(certIssuerObjectMap(rule["http"])["paths"])[0])
			switch scenario {
			case "foreign-label":
				certIssuerObjectMap(meta["labels"])["rtk.realtek.com/stack"] = "another-stack"
			case "foreign-name":
				meta["name"] = "foreign-ingress"
			case "foreign-namespace":
				meta["namespace"] = "another-stack-ingress"
			case "wrong-host", "canonical-host":
				rule["host"] = "different.example.test"
			case "wrong-backend", "canonical-backend":
				certIssuerObjectMap(certIssuerObjectMap(path["backend"])["service"])["name"] = "different-service"
			case "extra-path":
				extra := map[string]any{"path": "/unrelated", "pathType": "Prefix", "backend": map[string]any{"service": map[string]any{"name": "unrelated-service", "port": map[string]any{"number": 8080}}}}
				certIssuerObjectMap(rule["http"])["paths"] = []any{path, extra}
			case "default-backend":
				certIssuerObjectMap(obj["spec"])["defaultBackend"] = path["backend"]
				certIssuerObjectMap(obj["spec"])["rules"] = []any{}
			case "empty-public-rule":
				delete(rule, "http")
			}
			f := newCertIssuerIngressFixture(obj)
			if _, err := lkePlanCertIssuerIngressMigrationWithIO(provisionPaths{EnvRoot: t.TempDir()}, env, f.access()); err == nil {
				t.Fatal("unsafe ingress accepted")
			}
			if len(f.ops) != 0 {
				t.Fatal("unsafe plan mutated ingress")
			}
		})
	}
}

func TestCertIssuerIngressMigrationBlocksUnreadyServingIdentity(t *testing.T) {
	f := newCertIssuerIngressFixture(certIssuerLegacyIngress(t, false))
	f.servingError = errors.New("public hostname absent from installed certificate")
	if _, err := lkePlanCertIssuerIngressMigrationWithIO(provisionPaths{EnvRoot: t.TempDir()}, certIssuerIngressTestEnv(), f.access()); err == nil {
		t.Fatal("unready serving identity accepted")
	}
	if len(f.ops) != 0 {
		t.Fatal("unready identity caused mutations")
	}
}

func TestCertIssuerIngressMigrationResourceVersionRace(t *testing.T) {
	for _, duringMutation := range []bool{false, true} {
		t.Run(fmt.Sprint(duringMutation), func(t *testing.T) {
			legacy := certIssuerLegacyIngress(t, false)
			f := newCertIssuerIngressFixture(legacy)
			paths := provisionPaths{EnvRoot: t.TempDir()}
			p, err := lkePlanCertIssuerIngressMigrationWithIO(paths, certIssuerIngressTestEnv(), f.access())
			if err != nil {
				t.Fatal(err)
			}
			drift := func() {
				certIssuerObjectMap(f.objects[certIssuerMigrationFixtureKey(legacy)]["metadata"])["resourceVersion"] = "concurrent"
			}
			if duringMutation {
				f.beforeRemove = func(certIssuerIngressObject) { drift() }
			} else {
				drift()
			}
			if err := p.Apply(); err == nil {
				t.Fatal("resourceVersion race accepted")
			}
			if len(f.ops) != 0 || len(f.objects) != 1 {
				t.Fatal("race overwrote a resource")
			}
			if duringMutation {
				if p.Status != "outcome-unknown" {
					t.Fatal(p.Status)
				}
				if _, err := lkePlanCertIssuerIngressMigrationWithIO(paths, certIssuerIngressTestEnv(), f.access()); err == nil || !strings.Contains(err.Error(), "unfinished") {
					t.Fatal("uncertain journal did not fence retry")
				}
			}
		})
	}
}

func TestCertIssuerIngressMigrationRollsBackCreateOrPostcheckFailure(t *testing.T) {
	for _, postcheck := range []bool{false, true} {
		t.Run(fmt.Sprint(postcheck), func(t *testing.T) {
			legacy := certIssuerLegacyIngress(t, true)
			f := newCertIssuerIngressFixture(legacy)
			f.beforeWrite = func(verb string, object certIssuerIngressObject) error {
				if certIssuerObjectMap(object["metadata"])["name"] == "certissuer-public-mtls" {
					if postcheck {
						f.servingError = errors.New("postcheck failure")
					} else {
						return errors.New("admission rejected")
					}
				}
				return nil
			}
			p, err := lkePlanCertIssuerIngressMigrationWithIO(provisionPaths{EnvRoot: t.TempDir()}, certIssuerIngressTestEnv(), f.access())
			if err != nil {
				t.Fatal(err)
			}
			if err := p.Apply(); err == nil || !strings.Contains(err.Error(), "prior ingress routes restored") {
				t.Fatal(err)
			}
			if p.Status != "rolled-back" {
				t.Fatal(p.Status)
			}
			if len(f.objects) != 1 || !reflect.DeepEqual(certIssuerIngressRestoredConfig(f.objects[certIssuerMigrationFixtureKey(legacy)]), certIssuerIngressRestoredConfig(legacy)) {
				t.Fatal("legacy route was not restored exactly")
			}
		})
	}
}

func TestCertIssuerIngressMigrationFencesUnknownCreateOutcome(t *testing.T) {
	legacy := certIssuerLegacyIngress(t, false)
	f := newCertIssuerIngressFixture(legacy)
	f.beforeWrite = func(verb string, object certIssuerIngressObject) error {
		if certIssuerObjectMap(object["metadata"])["name"] == "certissuer-public-mtls" {
			created := certIssuerSanitizeIngress(object)
			certIssuerObjectMap(created["metadata"])["uid"], certIssuerObjectMap(created["metadata"])["resourceVersion"] = "created-with-lost-response", "12"
			f.objects[certIssuerMigrationFixtureKey(created)] = created
			return errors.New("response lost")
		}
		return nil
	}
	paths := provisionPaths{EnvRoot: t.TempDir()}
	p, err := lkePlanCertIssuerIngressMigrationWithIO(paths, certIssuerIngressTestEnv(), f.access())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(); err == nil || !strings.Contains(err.Error(), "uncertain") {
		t.Fatal(err)
	}
	if p.Status != "outcome-unknown" || len(f.objects) != 1 {
		t.Fatal("unknown create outcome was guessed or overwritten")
	}
	if _, err := lkePlanCertIssuerIngressMigrationWithIO(paths, certIssuerIngressTestEnv(), f.access()); err == nil {
		t.Fatal("retry bypassed uncertain restore record")
	}
}

func TestCertIssuerIngressMigrationRollsBackPureLegacyDeletion(t *testing.T) {
	legacy := certIssuerLegacyIngress(t, false)
	f := newCertIssuerIngressFixture(legacy)
	f.beforeWrite = func(verb string, object certIssuerIngressObject) error {
		if certIssuerObjectMap(object["metadata"])["name"] == "certissuer-public-mtls" {
			return errors.New("new route rejected")
		}
		return nil
	}
	p, err := lkePlanCertIssuerIngressMigrationWithIO(provisionPaths{EnvRoot: t.TempDir()}, certIssuerIngressTestEnv(), f.access())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(); err == nil {
		t.Fatal("create failure omitted")
	}
	if p.Status != "rolled-back" || !reflect.DeepEqual(certIssuerIngressRestoredConfig(f.objects[certIssuerMigrationFixtureKey(legacy)]), certIssuerIngressRestoredConfig(legacy)) {
		t.Fatal("deleted legacy route was not restored")
	}
	raw, err := os.ReadFile(p.RestorePath)
	if err != nil {
		t.Fatal(err)
	}
	var record lkeCertIssuerIngressMigration
	if json.Unmarshal(raw, &record) != nil || record.Status != "rolled-back" {
		t.Fatal("restore record missing outcome")
	}
}

func TestCertIssuerIngressMigrationRollbackRefusesConcurrentDrift(t *testing.T) {
	legacy := certIssuerLegacyIngress(t, false)
	f := newCertIssuerIngressFixture(legacy)
	access := f.access()
	read := access.read
	f.beforeWrite = func(verb string, obj certIssuerIngressObject) error {
		if certIssuerObjectMap(obj["metadata"])["name"] == "certissuer-public-mtls" {
			f.servingError = errors.New("postcheck failure")
		}
		return nil
	}
	access.read = func(namespace, name string) (certIssuerIngressObject, error) {
		if name == "certissuer-public-mtls" && f.objects[namespace+"/"+name] != nil {
			certIssuerObjectMap(f.objects[namespace+"/"+name]["metadata"])["resourceVersion"] = "concurrent"
		}
		return read(namespace, name)
	}
	paths := provisionPaths{EnvRoot: t.TempDir()}
	p, err := lkePlanCertIssuerIngressMigrationWithIO(paths, certIssuerIngressTestEnv(), access)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(); err == nil || !strings.Contains(err.Error(), "rollback refused concurrent") {
		t.Fatal(err)
	}
	if p.Status != "rollback-blocked" || len(f.objects) != 1 {
		t.Fatal("rollback overwrote concurrent ingress")
	}
	if _, err := lkePlanCertIssuerIngressMigrationWithIO(paths, certIssuerIngressTestEnv(), access); err == nil {
		t.Fatal("blocked rollback did not fence retry")
	}
}

func TestCertIssuerIngressMigrationCanonicalRepairPreservesAnnotations(t *testing.T) {
	env := certIssuerIngressTestEnv()
	current := certIssuerCanonicalIngress(env, lkeResolveCertIssuerTLSConfig(env))
	meta := certIssuerObjectMap(current["metadata"])
	meta["uid"], meta["resourceVersion"] = "owned-canonical", "1"
	annotations := certIssuerObjectMap(meta["annotations"])
	annotations["nginx.ingress.kubernetes.io/ssl-passthrough"] = "false"
	annotations["example.test/keep"] = "kept"
	f := newCertIssuerIngressFixture(current)
	p, err := lkePlanCertIssuerIngressMigrationWithIO(provisionPaths{EnvRoot: t.TempDir()}, env, f.access())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Apply(); err != nil {
		t.Fatal(err)
	}
	after := f.objects[certIssuerMigrationFixtureKey(current)]
	if certIssuerObjectMap(certIssuerObjectMap(after["metadata"])["annotations"])["example.test/keep"] != "kept" {
		t.Fatal("unrelated annotation lost")
	}
	if len(f.ops) != 1 || !strings.HasPrefix(f.ops[0], "replace ") {
		t.Fatal("canonical repair did not use conditional replacement")
	}
}

func certIssuerServingFixture(t *testing.T) (lkeCertIssuerTLSConfig, certIssuerIngressObject, certIssuerIngressObject, certIssuerIngressCommand) {
	t.Helper()
	env := certIssuerIngressTestEnv()
	cfg := lkeResolveCertIssuerTLSConfig(env)
	material, err := newLKECertIssuerMaterial(env)
	if err != nil {
		t.Fatal(err)
	}
	labels := map[string]any{"app.kubernetes.io/name": "certissuer"}
	service := certIssuerIngressObject{"metadata": map[string]any{"name": "certissuer", "namespace": cfg.Namespace}, "spec": map[string]any{"type": "ClusterIP", "clusterIP": "10.0.0.10", "selector": labels, "ports": []any{map[string]any{"port": cfg.HTTPSPort}}}}
	container := map[string]any{"name": "certissuer", "env": []any{map[string]any{"name": "CERT_ISSUER_SERVER_CERT", "value": "/etc/pki/tls.crt"}, map[string]any{"name": "CERT_ISSUER_CLIENT_CA", "value": "/etc/pki/client-ca.crt"}}, "volumeMounts": []any{map[string]any{"name": "pki", "mountPath": "/etc/pki", "readOnly": true}}}
	deployment := certIssuerIngressObject{"metadata": map[string]any{"name": "certissuer", "namespace": cfg.Namespace, "generation": 2}, "spec": map[string]any{"replicas": 1, "template": map[string]any{"metadata": map[string]any{"labels": labels}, "spec": map[string]any{"containers": []any{container}, "volumes": []any{map[string]any{"name": "pki", "secret": map[string]any{"secretName": "certissuer-runtime"}}}}}}, "status": map[string]any{"availableReplicas": 0, "updatedReplicas": 0, "observedGeneration": 1, "unavailableReplicas": 1}}
	command := func(stdin io.Reader, args ...string) ([]byte, error) {
		if stdin != nil {
			t.Fatal("serving qualification attempted a write")
		}
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "get service certissuer") {
			return json.Marshal(service)
		}
		if strings.Contains(joined, "get deployment certissuer") {
			return json.Marshal(deployment)
		}
		if strings.Contains(joined, "get secret certissuer-runtime -o jsonpath={.data.tls\\.crt}") {
			return []byte(base64.StdEncoding.EncodeToString([]byte(material.ServerCert))), nil
		}
		if strings.Contains(joined, "get secret certissuer-runtime -o jsonpath={.data.client-ca\\.crt}") {
			return []byte(base64.StdEncoding.EncodeToString([]byte(material.ServiceCA))), nil
		}
		return nil, fmt.Errorf("unexpected public qualification command: %s", joined)
	}
	return cfg, service, deployment, command
}

func TestCertIssuerIngressMigrationStaticPublicServingPhases(t *testing.T) {
	cfg, service, deployment, command := certIssuerServingFixture(t)
	if err := certIssuerValidateInstalledServingPolicy(command, cfg, false, false); err != nil {
		t.Fatal("pre-deploy blocked repairable old health:", err)
	}
	if err := certIssuerValidateInstalledServingPolicy(command, cfg, false, true); err == nil {
		t.Fatal("apply accepted unavailable old workload")
	}
	status := certIssuerObjectMap(deployment["status"])
	status["availableReplicas"], status["updatedReplicas"], status["observedGeneration"], status["unavailableReplicas"] = 1, 1, 2, 0
	if err := certIssuerValidateInstalledServingPolicy(command, cfg, false, true); err != nil {
		t.Fatal(err)
	}
	status["observedGeneration"] = 1
	if err := certIssuerValidateInstalledServingPolicy(command, cfg, false, true); err == nil {
		t.Fatal("apply accepted stale serving generation")
	}
	status["observedGeneration"] = 2
	certIssuerObjectMap(certIssuerObjectMap(service["spec"])["selector"])["app.kubernetes.io/name"] = "unrelated"
	if err := certIssuerValidateInstalledServingPolicy(command, cfg, false, false); err == nil {
		t.Fatal("pre-deploy accepted wrong Service owner")
	}
}

func TestCertIssuerIngressMigrationManagedPublicPolicyDefersOldHealth(t *testing.T) {
	cfg, _, deployment, base := certIssuerServingFixture(t)
	pod := certIssuerObjectMap(certIssuerObjectMap(certIssuerObjectMap(deployment["spec"])["template"])["spec"])
	container := certIssuerObjectMap(certIssuerObjectList(pod["containers"])[0])
	root := strings.Repeat("a", 64)
	settings := map[string]string{"CERT_ISSUER_HOST_IDENTITY_STATE": "/var/lib/pki/server.json", "CERT_ISSUER_HOST_NAME": "certissuer.internal.example.test", "CERT_ISSUER_HOST_ROOT_SHA256": root, "CERT_ISSUER_HOST_DNS_NAMES": "certissuer.internal.example.test," + cfg.PublicHost}
	applySettings := func() {
		entries := []any{}
		for name, value := range settings {
			entries = append(entries, map[string]any{"name": name, "value": value})
		}
		container["env"] = entries
	}
	applySettings()
	inspections := 0
	command := func(stdin io.Reader, args ...string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), " inspect-server ") {
			inspections++
			return json.Marshal(map[string]any{"fingerprint": strings.Repeat("b", 64), "root_sha256": root})
		}
		return base(stdin, args...)
	}
	if err := certIssuerValidateInstalledServingPolicy(command, cfg, false, false); err != nil {
		t.Fatal(err)
	}
	if inspections != 0 {
		t.Fatal("pre-deploy attempted state inspection through unavailable old pod")
	}
	status := certIssuerObjectMap(deployment["status"])
	status["availableReplicas"], status["updatedReplicas"], status["observedGeneration"], status["unavailableReplicas"] = 1, 1, 2, 0
	if err := certIssuerValidateInstalledServingPolicy(command, cfg, false, true); err != nil {
		t.Fatal(err)
	}
	if inspections != 1 {
		t.Fatal("apply omitted installed owner identity validation")
	}
	settings["CERT_ISSUER_HOST_DNS_NAMES"] = "certissuer.internal.example.test"
	applySettings()
	if err := certIssuerValidateInstalledServingPolicy(command, cfg, false, false); err == nil {
		t.Fatal("pre-deploy accepted missing public SAN policy")
	}
}

func TestCertIssuerIngressMigrationRejectsWrongTargetPortAndSubPath(t *testing.T) {
	for _, scenario := range []string{"targetPort", "namedTargetPort", "listener", "subPath", "subPathExpr"} {
		t.Run(scenario, func(t *testing.T) {
			cfg, service, deployment, command := certIssuerServingFixture(t)
			pod := certIssuerObjectMap(certIssuerObjectMap(certIssuerObjectMap(deployment["spec"])["template"])["spec"])
			container := certIssuerObjectMap(certIssuerObjectList(pod["containers"])[0])
			port := certIssuerObjectMap(certIssuerObjectList(certIssuerObjectMap(service["spec"])["ports"])[0])
			switch scenario {
			case "targetPort":
				port["targetPort"] = 80
			case "namedTargetPort":
				port["targetPort"] = "http"
				container["ports"] = []any{map[string]any{"name": "http", "containerPort": 80}}
			case "listener":
				container["env"] = append(certIssuerObjectList(container["env"]), map[string]any{"name": "CERT_ISSUER_LISTEN_ADDR", "value": ":8080"})
			case "subPath", "subPathExpr":
				certIssuerObjectMap(certIssuerObjectList(container["volumeMounts"])[0])[scenario] = "alternate"
			}
			if err := certIssuerValidateInstalledServingPolicy(command, cfg, false, false); err == nil {
				t.Fatal("unsafe target or mounted certificate accepted")
			}
		})
	}
	cfg, service, deployment, command := certIssuerServingFixture(t)
	certIssuerObjectMap(certIssuerObjectList(certIssuerObjectMap(service["spec"])["ports"])[0])["targetPort"] = "tls"
	pod := certIssuerObjectMap(certIssuerObjectMap(certIssuerObjectMap(deployment["spec"])["template"])["spec"])
	certIssuerObjectMap(certIssuerObjectList(pod["containers"])[0])["ports"] = []any{map[string]any{"name": "tls", "containerPort": cfg.HTTPSPort}}
	if err := certIssuerValidateInstalledServingPolicy(command, cfg, false, false); err != nil {
		t.Fatal("valid named TLS port rejected:", err)
	}
}

func TestCertIssuerIngressMigrationRejectsMalformedInventory(t *testing.T) {
	access := newCertIssuerIngressIOWithCommand(func(io.Reader, ...string) ([]byte, error) { return []byte(`{"metadata":{}}`), nil })
	if _, err := access.list(); err == nil {
		t.Fatal("malformed inventory treated as an absent environment")
	}
}

func TestCertIssuerIngressMigrationContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := lkePlanCertIssuerIngressMigrationWithContext(ctx, provisionPaths{EnvRoot: t.TempDir()}, certIssuerIngressTestEnv())
	var classified interface{ CheckCode() string }
	if !errors.As(err, &classified) || classified.CheckCode() != "CHECK_CANCELLED" {
		t.Fatal("pre-deploy metadata check ignored cancellation:", err)
	}
}
