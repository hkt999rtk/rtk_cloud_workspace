package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestServiceAuthorizationEnrollmentRestoresOnlyOriginalNativeSession(t *testing.T) {
	for _, mode := range []string{"complete", "pending-last", "mqtt-boundary", "managed-host-boundary", "changed-release", "changed-intent", "unaccepted-child", "changed-secret-owner", "changed-secret-data", "changed-active-issuer", "changed-live-session", "expired-session", "unbounded-session", "changed-credential-receipt", "changed-custody-reader", "changed-cleanup-owner", "missing-crl"} {
		t.Run(mode, func(t *testing.T) {
			_, _, _, service := createServiceRecipeFixture(t, "staging")
			f := newStagingQualificationFixture(t)
			stack := f.parent.Stack
			ns := stack + "-video-cloud"
			namespaces := []string{ns, stack + "-account-manager", stack + "-secrets"}
			const session = "55555555-5555-4555-8555-555555555555"
			const operation = "66666666-6666-4666-8666-666666666666"
			secret := "service-bootstrap-" + session
			image := environmentReleaseImage{Reference: "ghcr.io/example/controller@sha256:" + strings.Repeat("a", 64), SourceRevision: strings.Repeat("b", 40)}
			repairStage := "service-authorization-controller-" + image.SourceRevision
			config := func(name string) []deploymentplan.Definition {
				return []deploymentplan.Definition{{Phase: "bootstrap", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": name, "namespace": ns}, "data": deploymentplan.Object{"original": "accepted"}}}}
			}
			f.stage(t, repairStage, config("controller-repair"), true)
			record := func(key string, receipt any) {
				t.Helper()
				if err := f.e.BeginEntry(key, "bootstrap", mustProviderRecoveryJSON(receipt), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				if err := f.e.RecordReceipt(key, mustProviderRecoveryJSON(receipt)); err != nil {
					t.Fatal(err)
				}
				if err := f.e.CompleteEntry(key, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			record("pki-activate-"+operation, service.Intermediate.Document)
			var defs []deploymentplan.Definition
			for _, namespace := range namespaces {
				defs = append(defs, deploymentplan.Definition{Phase: "bootstrap", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"namespace": namespace, "name": secret}, "immutable": true, "type": "Opaque", "data": deploymentplan.Object{"credential": "b3JpZ2luYWw="}}})
			}
			_, credentials := f.stage(t, "service-credential-secrets", defs, true)
			record("openbao-policy-create-"+session+"-cleanup-controller", map[string]string{"policy": "original"})
			record("openbao-role-pki-controller-staging-original", map[string]string{"role": "original-controller"})
			record("openbao-role-certissuer-staging-original", map[string]string{"role": "original-certissuer"})
			stages := []string{"service-enrollment-issuer", "service-issuer-server-enrollment", "service-managed-enrollment-issuer", "service-core-enrollment", "service-managed-core", "application-client-enrollment", "transport-client-enrollment"}
			var lastPlan *deploymentplan.Plan
			var lastJournal deploymentplan.Journal
			for i, stage := range stages {
				lastPlan, lastJournal = f.stage(t, stage, config(stage), mode != "pending-last" || i != 6)
			}
			if mode == "unaccepted-child" {
				lastJournal.Accepted = false
				f.save(t, "deployment/rollback/"+lastPlan.SHA256()+"/journal.json", lastJournal)
			}
			if mode == "mqtt-boundary" {
				mqtt := environmentCreateIssuerOperation{ID: "88888888-8888-4888-8888-888888888888", IssuerID: "99999999-9999-4999-8999-999999999999", Status: "requested"}
				record("pki-reserve-mqtt-root", mqtt)
				for _, action := range []string{"authorize", "provision", "import"} {
					record("pki-"+action+"-"+mqtt.ID, map[string]string{"original": "accepted"})
				}
				if err := f.e.BeginEntry("kubernetes/mqtt-root-trust", "mqtt-root-trust", json.RawMessage(`{"original":"mqtt-trust"}`), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "managed-host-boundary" {
				if err := f.e.BeginEntry("pki-reserve-mqtt-root", "bootstrap", json.RawMessage(`{"domain":"mqtt"}`), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			subjects, err := environmentCreatePlannedServiceSubjects(f.cfg, f.parent.Bootstrap)
			if err != nil {
				t.Fatal(err)
			}
			intent := createServiceSessionIntent{SessionID: session, IssuerID: service.Intermediate.ID, RootSHA: service.Root.RootFingerprint, Subjects: subjects, Namespaces: namespaces, Claim: "pki-service-bootstrap-state"}
			uids := map[string]string{}
			for _, namespace := range namespaces {
				uids[namespace] = credentials.Owners["Secret/"+namespace+"/"+secret]
			}
			namespaceUID := "77777777-7777-4777-8777-777777777777"
			for _, namespace := range namespaces {
				f.objects["Namespace//"+namespace] = deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": namespace, "uid": namespaceUID}}
			}
			cleanup := environmentBootstrapCleanupBinding{Version: 1, Environment: "staging", SessionID: session, NamespaceUID: namespaceUID, SecretUID: uids[ns], PolicySHA256: strings.Repeat("c", 64), KubernetesCA: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			observed := map[string]any{"session_id": session, "issuer_id": service.Intermediate.ID, "root_sha256": service.Root.RootFingerprint, "bootstrap_fingerprint": strings.Repeat("d", 64), "status": "active", "subjects": subjects, "expires_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339)}
			if mode == "expired-session" {
				observed["expires_at"] = time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
			}
			live := deploymentplan.Clone(observed)
			active := deploymentplan.Clone(service.Intermediate.Document)
			custody := "pki-bootstrap-custody-" + f.e.Journal.PlanSHA256[:12]
			switch mode {
			case "changed-intent":
				intent.IssuerID = operation
			case "changed-secret-owner":
				deploymentplan.Map(f.objects["Secret/"+ns+"/"+secret]["metadata"])["uid"] = operation
			case "changed-secret-data":
				deploymentplan.Map(f.objects["Secret/"+ns+"/"+secret]["data"])["credential"] = "c3Vic3RpdHV0ZWQ="
			case "changed-active-issuer":
				active["parent_issuer_id"] = operation
			case "changed-live-session":
				live["bootstrap_fingerprint"] = strings.Repeat("e", 64)
			case "changed-credential-receipt":
				uids[ns] = operation
			case "changed-custody-reader":
				custody = "another-session-custody"
			case "changed-cleanup-owner":
				cleanup.NamespaceUID = operation
			}
			runtime := environmentCreateBootstrapRuntime{Execution: f.e, ExecutionSHA256: f.e.Journal.PlanSHA256, StagingControllerHandoff: true}
			base := environmentCreateCeremonyDirectory(runtime, "service")
			savedImage := image
			if mode == "changed-release" {
				savedImage.SourceRevision = strings.Repeat("f", 40)
			}
			f.save(t, filepath.Join("deployment/create", f.e.Journal.PlanSHA256, "service-authorization-recovery/controller-release.json"), savedImage)
			f.save(t, filepath.Join(base, "session-intent.json"), intent)
			f.save(t, filepath.Join(base, "session-observed.json"), map[string]any{"session": observed, "cleanup_binding": cleanup, "credential_uids": uids, "custody_reader": custody})
			crl := filepath.Join(base, "root.crl")
			if mode != "missing-crl" {
				if err := f.store.write(crl, []byte("original-root-crl"), true); err != nil {
					t.Fatal(err)
				}
			}
			runtime.Backend = serviceAuthorizationEnrollmentNative(t, f.store, f.objects, active, live)
			_, clean, err := runtime.Backend.privateKubeconfig()
			if err != nil {
				t.Fatal(err)
			}
			clean()
			before := createBootstrapStateSnapshot(t, f.store.Root)
			recovery := &createServiceAuthorizationRecovery{Root: service.Root, Operation: environmentCreateIssuerOperation{ID: operation, IssuerID: service.Intermediate.ID}, CRLFile: crl}
			ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
			defer cancel()
			if mode == "unbounded-session" {
				ctx = t.Context()
			}
			hosts := mode == "managed-host-boundary"
			err = loadServiceAuthorizationEnrollment(ctx, f.cfg, f.store, &f.parent, f.e, runtime, recovery, repairStage, image, hosts || mode == "mqtt-boundary", hosts)
			valid := mode == "complete" || mode == "pending-last" || mode == "mqtt-boundary" || hosts
			if valid {
				if err != nil || recovery.Enrollment == nil {
					t.Fatal("original native session did not restore", err)
				}
				got := recovery.Enrollment
				if got.BootstrapSessionID != session || got.Intermediate.ID != service.Intermediate.ID || got.Root.ID != service.Root.ID || got.RootCRLPEM != "original-root-crl" || !reflect.DeepEqual(got.CredentialUIDs, uids) || got.CleanupBinding.NamespaceUID != namespaceUID {
					t.Fatal("restore changed original session, lineage, credential or custody")
				}
				completed := 7
				if mode == "pending-last" {
					completed = 6
				}
				if len(recovery.CompletedEnrollmentStages) != completed || recovery.CompletedEnrollmentStages["transport-client-enrollment"] != (completed == 7) {
					t.Fatal("restore fabricated completion of pending enrollment")
				}
			} else if err == nil || recovery.Enrollment != nil {
				t.Fatal("changed native evidence restored session", err)
			}
			if !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, f.store.Root)) {
				t.Fatal("session qualification changed original private state")
			}
		})
	}
}

func serviceAuthorizationEnrollmentNative(t *testing.T, store secretStore, objects map[string]deploymentplan.Object, issuer, session deploymentplan.Object) environmentKubernetesBackend {
	t.Helper()
	dir := t.TempDir()
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "state.json"), mustProviderRecoveryJSON(map[string]any{"objects": objects, "issuer": issuer, "session": session}), 0600); err != nil {
		t.Fatal(err)
	}
	script := "#!" + python + "\n" + `import json,os,sys
s=json.load(open(os.path.join(os.environ['RTK_SERVICE_AUTH_ENROLLMENT'],'state.json')))
a=sys.argv[1:]
assert a[0]=='--kubeconfig' and os.path.isfile(a[1])
if 'get' in a:
 i=a.index('get');kind=a[i+1]
 if kind=='namespace': print(json.dumps({'items':[o for o in s['objects'].values() if o['kind']=='Namespace']}));sys.exit()
 if ',' in kind:
  ns=a[a.index('--namespace')+1] if '--namespace' in a else None
  print(json.dumps({'items':[o for o in s['objects'].values() if ns and o['metadata'].get('namespace')==ns]}));sys.exit()
 if kind=='validatingwebhookconfiguration': print('{"items":[]}');sys.exit()
 ns=a[a.index('--namespace')+1] if '--namespace' in a else ''
 obj=s['objects'].get(kind+'/'+ns+'/'+a[i+2])
 if obj: print(json.dumps(obj))
 sys.exit()
assert 'exec' in a and 'registry-read' in a and 'PGOPTIONS=-c default_transaction_read_only=on' in a
q=a[-1]
if q.startswith('SELECT document FROM pki_issuers'): print(json.dumps(s['issuer']))
elif q.startswith('SELECT json_build_object') and 'pki_deployment_bootstrap_sessions' in q: print(json.dumps(s['session']))
else: sys.exit(1)
`
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	t.Setenv("RTK_SERVICE_AUTH_ENROLLMENT", dir)
	return environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-enrollment-original-kubeconfig")}
}

func TestServiceAuthorizationRecoveryResultRetainsUncertainOwnerAndReleasesCompletedOwner(t *testing.T) {
	for _, mode := range []string{"completed", "execution-error", "pending", "changed-owner", "report-write-error"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, operator, artifact := createStateFixture(t, "staging")
			e, err := startEnvironmentCreateExecution(t.Context(), cfg, artifact, operator, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			if err := e.BeginEntry("original-stage", "bootstrap", json.RawMessage(`{"original":true}`), time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if mode != "pending" {
				if err := e.RecordReceipt("original-stage", json.RawMessage(`{"native":"accepted"}`)); err != nil {
					t.Fatal(err)
				}
				if err := e.CompleteEntry("original-stage", time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			failure := errors.New("original execution interrupted")
			var cause error
			if mode == "execution-error" || mode == "report-write-error" {
				cause = failure
			}
			if mode == "changed-owner" {
				owner := e.owner
				owner.OwnerToken = strings.Repeat("f", 64)
				if err := store.write("deployment/.execution-active/owner.json", mustProviderRecoveryJSON(owner), true); err != nil {
					t.Fatal(err)
				}
			}
			report := filepath.Join(store.Root, "deployment/reports/authorization.json")
			if mode == "report-write-error" {
				if err := os.MkdirAll(report, 0700); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(e.JournalPath)
			if err != nil {
				t.Fatal(err)
			}
			err = finishServiceAuthorizationRecovery(t.Context(), cfg, store, e, report, "original-source", "immutable-image", cause)
			if mode == "completed" {
				if err != nil {
					t.Fatal(err)
				}
				if _, err := os.Lstat(filepath.Join(store.Root, "deployment/.execution-active")); !os.IsNotExist(err) {
					t.Fatal("successful completion retained active owner", err)
				}
				var j environmentCreateJournal
				if readExecutionJSON(e.JournalPath, &j) != nil || j.CompletedAt.IsZero() {
					t.Fatal("success report preceded durable completion")
				}
			} else {
				if err == nil {
					t.Fatal("uncertain recovery reported success")
				}
				if mode == "execution-error" && !errors.Is(err, failure) {
					t.Fatal("original failure lost", err)
				}
				after, readErr := os.ReadFile(e.JournalPath)
				if readErr != nil || string(before) != string(after) {
					t.Fatal("failed completion changed journal", readErr)
				}
				if _, err := os.Lstat(filepath.Join(store.Root, "deployment/.execution-active/owner.json")); err != nil {
					t.Fatal("uncertain recovery released original owner", err)
				}
			}
			if mode == "report-write-error" {
				return
			}
			var result map[string]any
			if err := readExecutionJSON(report, &result); err != nil {
				t.Fatal(err)
			}
			decision := "NO-GO"
			if mode == "completed" {
				decision = "GO"
			}
			if result["decision"] != decision || result["completed"] != (mode == "completed") || result["parent_plan_sha256"] != e.Journal.PlanSHA256 || result["controller_source"] != "original-source" || result["controller_image"] != "immutable-image" {
				t.Fatal("report lost original recovery identity or outcome")
			}
		})
	}
}
