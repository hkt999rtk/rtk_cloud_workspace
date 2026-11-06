package main

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Native SQL supplies the original session/revocation join and installed ACKs.
// Qualification must retain the pending finalizer and accepted workload.
func TestStagingSealedFinalizationRequiresOriginalPublicationAndAllNativeACKs(t *testing.T) {
	for _, mode := range []string{"published", "publication-progress", "unsealed", "incomplete", "wrong-root", "expired-crl", "invalid-digest", "missing-host-ack", "malformed-acks", "unaccepted-workload", "foreign-workload-proof", "foreign-environment", "invalid-consumer"} {
		t.Run(mode, func(t *testing.T) {
			f := newStagingQualificationFixture(t)
			ns := f.parent.Stack + "-video-cloud"
			workload := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": ns, "name": "video-cloud-api"}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "video-cloud-api", "image": "ghcr.io/example/api@sha256:" + strings.Repeat("a", 64)}}}}}}
			child, journal := f.stage(t, "workload", []deploymentplan.Definition{{Object: workload, Phase: "workload"}}, true)
			if mode == "unaccepted-workload" {
				journal.Accepted = false
				f.save(t, "deployment/rollback/"+child.SHA256()+"/journal.json", journal)
			}
			if mode == "foreign-workload-proof" {
				journal.PlanSHA256 = strings.Repeat("f", 64)
				f.save(t, "deployment/rollback/"+child.SHA256()+"/journal.json", journal)
			}
			s := environmentCreateServiceCeremony{BootstrapSessionID: "11111111-1111-4111-8111-111111111111", Root: environmentCreateRegisteredIssuer{RootFingerprint: strings.Repeat("b", 64)}, Intermediate: environmentCreateRegisteredIssuer{ID: "22222222-2222-4222-8222-222222222222"}}
			if err := f.e.BeginEntry("service-session-finalize", "bootstrap", mustProviderRecoveryJSON(map[string]string{"session_id": s.BootstrapSessionID, "issuer_id": s.Intermediate.ID}), time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			policy := environmentCreateBootstrapPlan{Domains: []environmentCreateIssuerPolicy{{Domain: "service", Consumers: []string{"account-manager", "certissuer", "pki-controller", "factory-enroll", "video-cloud-api", "video-cloud-logingester"}}}}
			if mode == "invalid-consumer" {
				policy.Domains[0].Consumers = append(policy.Domains[0].Consumers, "not a consumer")
			}
			cfg := f.cfg
			if mode == "foreign-environment" {
				cfg.Environment = "dev"
			}
			backend, log := sealedFinalizationNativeFixture(t, f.store, s, mode)
			runtime := environmentCreateBootstrapRuntime{Execution: f.e, ExecutionSHA256: f.e.Journal.PlanSHA256, Backend: backend, ServiceAuthorizationRecovery: &createServiceAuthorizationRecovery{}}
			_, clean, err := backend.privateKubeconfig()
			if err != nil {
				t.Fatal(err)
			}
			clean()
			before := createBootstrapStateSnapshot(t, f.store.Root)
			budget := 10 * time.Second
			if mode == "expired-crl" || mode == "invalid-digest" || mode == "missing-host-ack" {
				budget = time.Second
			}
			ctx, cancel := context.WithTimeout(t.Context(), budget)
			defer cancel()
			err = qualifyStagingSealedFinalization(ctx, cfg, policy, runtime, s)
			valid := mode == "published" || mode == "publication-progress"
			if valid && err != nil {
				t.Fatal("genuine sealed publication rejected", err)
			}
			if !valid && err == nil {
				t.Fatal("unconfirmed finalization authorized")
			}
			if !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, f.store.Root)) {
				t.Fatal("qualification rewrote workload, pending finalizer or private evidence")
			}
			calls, readErr := os.ReadFile(log)
			if mode == "unaccepted-workload" || mode == "foreign-workload-proof" || mode == "foreign-environment" || mode == "invalid-consumer" {
				if !os.IsNotExist(readErr) || len(calls) != 0 {
					t.Fatal("unqualified owner reached registry", readErr)
				}
				return
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			queries := strings.Split(strings.TrimSpace(string(calls)), "\n")
			if valid {
				want := 2
				if mode == "publication-progress" {
					want = 3
				}
				if len(queries) != want || !strings.Contains(queries[len(queries)-1], "pki_crl_acknowledgments") {
					t.Fatal("publication did not wait for native consumer proof", queries)
				}
			} else if (mode == "unsealed" || mode == "incomplete" || mode == "wrong-root") && len(queries) != 1 {
				t.Fatal("unconfirmed progress retried", queries)
			}
		})
	}
}

func sealedFinalizationNativeFixture(t *testing.T, store secretStore, s environmentCreateServiceCeremony, mode string) (environmentKubernetesBackend, string) {
	t.Helper()
	dir := t.TempDir()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	state := map[string]any{"mode": mode, "session": s.BootstrapSessionID, "issuer": s.Intermediate.ID, "root": s.Root.RootFingerprint, "digest": strings.Repeat("c", 64), "expiry": time.Now().UTC().Add(time.Hour).Format(time.RFC3339)}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), mustProviderRecoveryJSON(state), 0600); err != nil {
		t.Fatal(err)
	}
	script := "#!" + python + "\n" + `import json,os,sys
from pathlib import Path
root=Path(os.environ['RTK_SEALED_FINALIZATION_NATIVE']);s=json.loads((root/'state.json').read_text());a=sys.argv[1:]
assert a[0]=='--kubeconfig' and Path(a[1]).is_file()
assert '--namespace' in a and a[a.index('--namespace')+1]=='video-cloud-staging-platform'
assert 'exec' in a and 'registry-read' in a and 'PGOPTIONS=-c default_transaction_read_only=on' in a
q=a[-1];assert q.startswith('SELECT ') and ';' not in q
log=root/'queries';previous=log.read_text().splitlines() if log.exists() else []
with log.open('a') as f:f.write(q+'\n')
if 'FROM pki_deployment_bootstrap_sessions s JOIN pki_service_client_revocations r' in q:
 assert "r.fingerprint=s.bootstrap_fingerprint AND r.issuer_id=s.issuer_id AND r.reason='deployment bootstrap finalized'" in q
 assert "s.environment='staging' AND s.session_id='"+s['session']+"' AND s.issuer_id='"+s['issuer']+"'" in q
 value={'sealed':s['mode']!='unsealed','complete':s['mode']!='incomplete','root':'f'*64 if s['mode']=='wrong-root' else s['root'],'digest':s['digest'],'expires_at':s['expiry']}
 if s['mode']=='publication-progress' and len(previous)==0:value['digest']=''
 if s['mode']=='invalid-digest':value['digest']='unknown'
 if s['mode']=='expired-crl':value['expires_at']='2020-01-01T00:00:00Z'
 print(json.dumps(value));sys.exit()
assert "FROM pki_crl_acknowledgments WHERE issuer_id='"+s['issuer']+"' AND digest='"+s['digest']+"'" in q
acks=['account-manager','certissuer','pki-controller','factory-enroll','video-cloud-api','video-cloud-logingester','pkibroker','emqx-pki','openbao']
if s['mode']=='missing-host-ack':acks.remove('openbao')
if s['mode']=='malformed-acks':acks={'unknown':'native-outcome'}
print(json.dumps(acks))
`
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	t.Setenv("RTK_SEALED_FINALIZATION_NATIVE", dir)
	return environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-original-finalization-kubeconfig")}, filepath.Join(dir, "queries")
}

func TestFreshRegistrationBindsOnlyAcceptedClosedIssuerAndPreservesOriginalOwner(t *testing.T) {
	for _, mode := range []string{"accepted", "unaccepted", "foreign-proof", "invalid-owner", "wrong-resource", "extra-resource", "missing-registration-dependency"} {
		t.Run(mode, func(t *testing.T) {
			f := newStagingQualificationFixture(t)
			stack := f.parent.Stack
			ns := stack + "-video-cloud"
			closed := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": ns, "name": "certissuer"}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "certissuer", "image": "immutable-closed-issuer", "env": []any{deploymentplan.Object{"name": "CERT_ISSUER_REGISTRY_ONLY_ENABLED", "value": "true"}}}}}}}}
			if mode == "wrong-resource" {
				deploymentplan.Map(closed["metadata"])["name"] = "foreign-certissuer"
			}
			defs := []deploymentplan.Definition{{Object: closed, Phase: "bootstrap"}}
			if mode == "extra-resource" {
				defs = append(defs, deploymentplan.Definition{Phase: "bootstrap", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "unrelated", "namespace": ns}}})
			}
			p, j := f.stage(t, "fresh-product-enrollment-closed", defs, true)
			id := "Deployment/" + ns + "/certissuer"
			switch mode {
			case "unaccepted":
				j.Accepted = false
			case "foreign-proof":
				j.PlanSHA256 = strings.Repeat("f", 64)
			case "invalid-owner":
				j.Owners[id] = "unknown-owner"
			}
			f.save(t, "deployment/rollback/"+p.SHA256()+"/journal.json", j)
			open := deploymentplan.Clone(closed)
			deploymentplan.Map(open["metadata"])["name"] = "certissuer"
			deploymentplan.Map(certIssuerObjectList(environmentPKIPod(open)["containers"])[0])["image"] = "original-open-enrollment"
			unrelated := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": ns, "name": "registration-input"}, "data": deploymentplan.Object{"original": "retained"}}
			registration := []deploymentplan.Definition{{Object: unrelated, Phase: "configuration"}, {Object: open, Phase: "bootstrap"}}
			if mode == "missing-registration-dependency" {
				registration = registration[:1]
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			originalDefinitions := mustProviderRecoveryJSON(registration)
			err := bindFreshProductClosedIssuer(registration, f.store, f.e, stack)
			if mode != "accepted" {
				if err == nil {
					t.Fatal("unproven closed issuer rebound registration")
				}
				if string(originalDefinitions) != string(mustProviderRecoveryJSON(registration)) {
					t.Fatal("rejected binding changed registration recipe")
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				body := registration[1].Object
				if deploymentplan.Map(body["metadata"])["uid"] != j.Owners[id] || deploymentplan.Map(certIssuerObjectList(environmentPKIPod(body)["containers"])[0])["image"] != "immutable-closed-issuer" || deploymentplan.ID(body) != id {
					t.Fatal("registration lost accepted closed issuer or owner")
				}
				if !reflect.DeepEqual(registration[0].Object, unrelated) || registration[1].Phase != "bootstrap" {
					t.Fatal("binding changed unrelated registration inputs")
				}
				deploymentplan.Map(certIssuerObjectList(environmentPKIPod(body)["containers"])[0])["image"] = "downstream-edit"
				if deploymentplan.Map(certIssuerObjectList(environmentPKIPod(open)["containers"])[0])["image"] != "original-open-enrollment" {
					t.Fatal("binding aliased original enrollment recipe")
				}
			}
			if !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, f.store.Root)) {
				t.Fatal("binding rewrote closed issuer proof")
			}
		})
	}
}
