package main

import (
	"context"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func newExpiredSessionExecutionContract(t *testing.T, setup ...func(*environmentCreateBootstrapPlan)) (*acceptedCoreExecutionContract, *createServiceSessionRecovery) {
	t.Helper()
	return newExpiredSessionExecutionContractWithHooks(t, coreExecutionContractHooks{}, setup...)
}

func newExpiredSessionExecutionContractWithHooks(t *testing.T, hooks coreExecutionContractHooks, setup ...func(*environmentCreateBootstrapPlan)) (*acceptedCoreExecutionContract, *createServiceSessionRecovery) {
	t.Helper()
	f := newCoreExecutionContractAtBoundaryWithHooks(t, true, false, true, hooks, setup...)
	defs, err := renderEnvironmentCreateServiceIssuerDefinitions(context.Background(), f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service)
	if err != nil {
		t.Fatal(err)
	}
	trust := f.stage(t, "kubernetes/service-enrollment-issuer", defs)
	_, trustJournal, err := acceptedCreateStage(f.store, f.e, "kubernetes/service-enrollment-issuer")
	if err != nil {
		t.Fatal(err)
	}
	defs, err = renderEnvironmentCreateServiceIssuerServerEnrollmentDefinitions(context.Background(), f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service)
	if err != nil {
		t.Fatal(err)
	}
	failed := ""
	for i := range defs {
		defs[i].Dependencies = nil
		if defs[i].Object["kind"] == "Job" {
			defs[i].Phase = "bootstrap"
			failed = deploymentplan.ID(defs[i].Object)
		} else {
			defs[i].Phase = "configuration"
		}
	}
	original, journal := f.interruptedStage(t, "kubernetes/service-issuer-server-enrollment", defs, failed)
	if len(original.Resources) != 2 || len(journal.Owners) != 1 || len(journal.Completed) != 0 {
		t.Fatal("original unstarted enrollment boundary changed")
	}
	var observed createServiceSessionObserved
	if err := readExecutionJSON(filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-renewal", f.review, "observed.json"), &observed); err != nil {
		t.Fatal(err)
	}
	f.write(t, filepath.Join("deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-observed.json"), observed)
	intent := createServiceSessionIntent{SessionID: f.session, Claim: f.service.BootstrapClaim, Subjects: f.protocol["session"].(map[string]any)["subjects"].([]string), IssuerID: f.service.Intermediate.ID, RootSHA: f.service.Root.RootFingerprint, Namespaces: f.service.BootstrapCredentialNamespaces}
	objects := coreContractNativeInventory(f.native.objects)
	objects[failed]["status"] = deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Failed", "status": "True", "reason": "DeadlineExceeded"}}}
	// Scheduling never reached a container: the Job deadline expired while its
	// original certificate PVC had not yet been created by the legacy producer.
	name := deploymentplan.String(deploymentplan.Map(objects[failed]["metadata"])["name"])
	pod := objects["Pod/"+f.p.Plan.Stack+"-video-cloud/"+name+"-pod"]
	deploymentplan.Map(pod["metadata"])["labels"] = deploymentplan.Object{"job-name": name}
	pod["status"] = deploymentplan.Object{"phase": "Pending"}
	f.protocol["objects"] = objects
	f.protocol["custody"].(map[string]string)["/run/pki-bootstrap/bootstrap.crt"] = f.certificate
	native := f.protocol["session"].(map[string]any)
	native["count"], native["servers"], native["clients"], native["caller"] = 1, 0, 1, "service:deployment-bootstrap"
	f.publish(t)
	return f, &createServiceSessionRecovery{Renewal: true, Original: original, Journal: journal, Trust: trust, TrustJournal: trustJournal, Intent: intent, Ceremony: f.service}
}

func TestExpiredSessionExecutionContractQualifiesOriginalKeyCSRAndUnstartedEnrollment(t *testing.T) {
	f, r := newExpiredSessionExecutionContract(t)
	before := createBootstrapStateSnapshot(t, f.store.Root)
	p := f.p
	p.Materials = map[string]string{}
	if err := verifyExpiredServiceSessionRecovery(context.Background(), f.cfg, f.store, f.e, f.backend, r); err != nil {
		t.Fatal("real expired original credential rejected", err)
	}
	prepared, err := prepareExpiredServiceSessionRecovery(context.Background(), f.cfg, f.store, f.e, f.backend, &p, r)
	if err != nil {
		t.Fatal("original same-key renewal could not be prepared", err)
	}
	if prepared != r || r.Candidate == nil || len(r.Candidate.Resources) != 1 || r.Candidate.Resources[0].Action != "create" || r.OriginalKeySHA != environmentPreflightBytesSHA256([]byte(f.key)) || r.OriginalCSRSHA != environmentPreflightBytesSHA256([]byte(f.csr)) {
		t.Fatal("renewal replaced original key/CSR or lost one-shot boundary")
	}
	env := createRecipeEnv(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(r.Candidate.Resources[0].Desired)["containers"])[0]))
	if env["PKI_BOOTSTRAP_KEY_PATH"] != "/run/pki-bootstrap/bootstrap.key" || env["PKI_BOOTSTRAP_CSR_PATH"] != "/run/pki-bootstrap/bootstrap.csr" || env["PKI_BOOTSTRAP_CERT_PATH"] != r.RenewedCertPath || env["PKI_BOOTSTRAP_SESSION_ID"] != sessionRenewalUUID(f.e.Journal.PlanSHA256, r.Original.SHA256(), f.session) {
		t.Fatal("renewal did not retain original custody")
	}
	if p.Materials["original-service-session-forward-plan"] != r.Candidate.SHA256() || p.Materials["original-service-session-custody-bootstrap.crt"] != environmentPreflightBytesSHA256([]byte(f.certificate)) {
		t.Fatal("qualification omitted original certificate or candidate binding")
	}
	assertRecoveryContractReadOnly(t, f.store, before)
}

func TestExpiredSessionExecutionContractRefusesUsedIdentityOrChangedCustody(t *testing.T) {
	for _, mode := range []string{"issued-workload", "installed-subject", "started-container", "credential-key-mismatch", "different-csr", "different-custody-certificate", "existing-certificate-claim", "foreign-job-owner"} {
		t.Run(mode, func(t *testing.T) {
			f, r := newExpiredSessionExecutionContract(t)
			objects := f.protocol["objects"].(map[string]deploymentplan.Object)
			switch mode {
			case "issued-workload":
				f.protocol["session"].(map[string]any)["servers"] = 1
			case "installed-subject":
				f.protocol["session"].(map[string]any)["installed"] = []string{"service:certissuer"}
			case "started-container":
				for _, o := range objects {
					if o["kind"] == "Pod" && deploymentplan.Map(deploymentplan.Map(o["metadata"])["labels"])["job-name"] != nil {
						deploymentplan.Map(o["status"])["containerStatuses"] = []any{deploymentplan.Object{"name": "enroll", "state": deploymentplan.Object{"waiting": deploymentplan.Object{"reason": "CrashLoopBackOff"}}}}
					}
				}
			case "credential-key-mismatch":
				der, err := x509.MarshalPKCS8PrivateKey(f.issuerKey)
				if err != nil {
					t.Fatal(err)
				}
				key := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
				for _, ns := range r.Intent.Namespaces {
					secret := objects["Secret/"+ns+"/service-bootstrap-"+f.session]
					deploymentplan.Map(secret["data"])["tls.key"] = base64.StdEncoding.EncodeToString(key)
				}
			case "different-csr":
				der, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: "service:deployment-bootstrap"}}, f.issuerKey)
				if err != nil {
					t.Fatal(err)
				}
				f.protocol["custody"].(map[string]string)["/run/pki-bootstrap/bootstrap.csr"] = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: der}))
			case "different-custody-certificate":
				f.protocol["custody"].(map[string]string)["/run/pki-bootstrap/bootstrap.crt"] = f.service.Root.Certificate
			case "existing-certificate-claim":
				for _, v := range r.Original.Resources {
					if v.Desired["kind"] == "PersistentVolumeClaim" {
						o := deploymentplan.Clone(v.Desired)
						deploymentplan.Map(o["metadata"])["uid"] = "foreign-claim"
						objects[v.ID] = o
					}
				}
			case "foreign-job-owner":
				deploymentplan.Map(objects[r.Journal.Failed]["metadata"])["uid"] = "foreign-job"
			}
			f.publish(t)
			before := createBootstrapStateSnapshot(t, f.store.Root)
			p := f.p
			p.Materials = map[string]string{}
			if _, err := prepareExpiredServiceSessionRecovery(context.Background(), f.cfg, f.store, f.e, f.backend, &p, r); err == nil || !strings.Contains(err.Error(), "expired bootstrap recovery") {
				t.Fatal("changed/used original identity accepted", mode, err)
			}
			if r.Candidate != nil {
				t.Fatal("refused original source produced renewal candidate")
			}
			assertRecoveryContractReadOnly(t, f.store, before)
		})
	}
}

func TestExpiredSessionExecutionContractExecutesSameKeyRenewalWithExactNativeRole(t *testing.T) {
	for _, mode := range []string{"success", "after-intent", "after-policy", "after-role", "policy-response-lost", "role-response-lost", "native-role-drift", "renewal-readiness-interruption", "renewal-intent-changed", "renewal-intent-malformed"} {
		t.Run(mode, func(t *testing.T) {
			f, r := newExpiredSessionExecutionContract(t)
			p := f.p
			p.Materials = map[string]string{}
			if _, err := prepareExpiredServiceSessionRecovery(context.Background(), f.cfg, f.store, f.e, f.backend, &p, r); err != nil {
				t.Fatal(err)
			}
			r.Review = deploymentplan.Digest(p)
			runtime := f.runtime
			runtime.Backend = f.backend
			runtime.ServiceSessionRecovery = r
			runtime.OpenBao.RootTokenPath = filepath.Join(f.store.Root, "openbao/root-token")
			protocolPath := filepath.Join(t.TempDir(), "openbao.json")
			protocol := map[string]any{"name": "rtk-dev-service-bootstrap-" + r.RenewedIntent.SessionID, "namespace": f.p.Plan.Stack + "-video-cloud", "policy": f.protocol["policy"].(environmentCreateOpenBaoIssuerPolicies).ServiceClientSignerPolicy, "policy_writes": 0, "role_writes": 0, "bad_role": mode == "native-role-drift", "lose_policy": mode == "policy-response-lost", "lose_role": mode == "role-response-lost"}
			if err := os.WriteFile(protocolPath, mustProviderRecoveryJSON(protocol), 0600); err != nil {
				t.Fatal(err)
			}
			originalKubectl := os.Getenv("RTK_CLOUD_KUBECTL")
			wrapper := filepath.Join(t.TempDir(), "kubectl")
			script := `#!/usr/bin/env python3
import os,sys,json
args=sys.argv[1:];joined=' '.join(args)
if 'bao policy write ' not in joined and 'bao policy read ' not in joined and 'bao read -format=json auth/kubernetes/role/' not in joined:os.execv(os.environ['RTK_RENEWAL_NATIVE'],[os.environ['RTK_RENEWAL_NATIVE']]+args)
with open(os.environ['RTK_RENEWAL_PROTOCOL']) as f:d=json.load(f)
raw=sys.stdin.read();token,_,body=raw.partition('\n')
if token!='isolated-original-bao-token':sys.exit('wrong original OpenBao credential')
if 'bao policy ' in joined:
 if 'bao policy write '+d['name']+' -' in joined:
  if body!=d['policy']:sys.exit('wrong exact native policy')
  d['policy_writes']+=1
  if d['lose_policy']:
   with open(os.environ['RTK_RENEWAL_PROTOCOL'],'w') as f:json.dump(d,f)
   sys.exit('isolated policy response lost')
 if 'bao policy read '+d['name'] not in joined:sys.exit('foreign policy name')
 print(d['policy'],end='')
else:
 if 'bao write auth/kubernetes/role/pki-service-bootstrap-dev ' in joined:
  expected=['bound_service_account_names=pki-service-bootstrap','bound_service_account_namespaces='+d['namespace'],'audience=openbao','token_policies='+d['name'],'token_ttl=15m','token_max_ttl=15m','token_no_default_policy=true']
  if any(v not in joined for v in expected):sys.exit('wrong exclusive native role')
  d['role_writes']+=1
  if d['lose_role']:
   with open(os.environ['RTK_RENEWAL_PROTOCOL'],'w') as f:json.dump(d,f)
   sys.exit('isolated role response lost')
 if d['role_writes']==0:sys.exit('role was never installed')
 print(json.dumps({'data':{'bound_service_account_names':['pki-service-bootstrap'],'bound_service_account_namespaces':[d['namespace']],'token_policies':[d['name']],'audience':'openbao','token_ttl':900,'token_max_ttl':900,'token_no_default_policy':not d['bad_role']}}))
with open(os.environ['RTK_RENEWAL_PROTOCOL'],'w') as f:json.dump(d,f)
`
			if err := os.WriteFile(wrapper, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("RTK_RENEWAL_NATIVE", originalKubectl)
			t.Setenv("RTK_RENEWAL_PROTOCOL", protocolPath)
			t.Setenv("RTK_CLOUD_KUBECTL", wrapper)
			t.Setenv("PATH", filepath.Dir(wrapper)+string(os.PathListSeparator)+os.Getenv("PATH"))
			if strings.HasPrefix(mode, "after-") {
				if _, err := writeEnvironmentCreateBootstrapJSON(f.store, filepath.Join("deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-renewal", r.Review, "intent.json"), r.RenewedIntent); err != nil {
					t.Fatal(err)
				}
				if mode == "after-policy" || mode == "after-role" {
					if err := installEnvironmentCreateOpenBaoPolicy(context.Background(), f.cfg, runtime, protocol["name"].(string), protocol["policy"].(string)); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "after-role" {
					if err := configureEnvironmentCreateOpenBaoRole(context.Background(), f.cfg, runtime, "pki-service-bootstrap-dev", []string{"pki-service-bootstrap"}, []string{protocol["name"].(string)}); err != nil {
						t.Fatal(err)
					}
				}
			}
			stage := "service-session-original-key-renewal-" + r.Review
			apply := currentInstallationContractApply(t, f, map[string]*deploymentplan.Plan{stage: r.Candidate})
			if strings.HasPrefix(mode, "renewal-") {
				f.native.failure = "after-write-bootstrap"
			}
			actual, err := executeOriginalKeySessionRenewal(context.Background(), f.cfg, f.store, runtime, f.service, apply)
			if mode == "policy-response-lost" || mode == "role-response-lost" {
				if err == nil {
					t.Fatal("uncertain native response accepted")
				}
				before := createBootstrapStateSnapshot(t, f.store.Root)
				_, retryErr := executeOriginalKeySessionRenewal(context.Background(), f.cfg, f.store, runtime, f.service, apply)
				if retryErr == nil {
					t.Fatal("pending legacy mutation was replayed")
				}
				var state map[string]any
				if err := readExecutionJSON(protocolPath, &state); err != nil {
					t.Fatal(err)
				}
				if state["policy_writes"] != float64(1) || state["role_writes"].(float64) > 1 || f.native.objects[r.Candidate.Resources[0].ID] != nil {
					t.Fatal("uncertain native mutation was repeated or allowed enrollment")
				}
				assertRecoveryContractReadOnly(t, f.store, before)
				return
			}
			if mode == "native-role-drift" {
				if err == nil || !strings.Contains(err.Error(), "exclusive binding") {
					t.Fatal("native nonexclusive role was accepted", err)
				}
				if f.native.objects[r.Candidate.Resources[0].ID] != nil {
					t.Fatal("renewal executed before exact role readback")
				}
				return
			}
			if strings.HasPrefix(mode, "renewal-") {
				if err == nil || !f.native.failed || f.native.held {
					t.Fatal("renewal readiness interruption was lost", err)
				}
				if mode == "renewal-intent-changed" || mode == "renewal-intent-malformed" {
					path := filepath.Join("deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-renewal", r.Review, "intent.json")
					changed := r.RenewedIntent
					changed.SessionID = f.session
					raw := mustProviderRecoveryJSON(changed)
					if mode == "renewal-intent-malformed" {
						raw = []byte("{broken original intent")
					}
					if err := f.store.write(path, raw, true); err != nil {
						t.Fatal(err)
					}
					before := createBootstrapStateSnapshot(t, f.store.Root)
					writes := deploymentplan.Digest(f.native.writes)
					_, err = executeOriginalKeySessionRenewal(context.Background(), f.cfg, f.store, runtime, f.service, apply)
					if err == nil || !strings.Contains(err.Error(), "original renewal intent changed") {
						t.Fatal("changed immutable renewal intent accepted", err)
					}
					if writes != deploymentplan.Digest(f.native.writes) {
						t.Fatal("refused intent performed native writes")
					}
					assertRecoveryContractReadOnly(t, f.store, before)
					return
				}
				actual, err = executeOriginalKeySessionRenewal(context.Background(), f.cfg, f.store, runtime, f.service, apply)
			}
			if err != nil {
				t.Fatal("genuine original-key renewal execution rejected", err)
			}
			// This derived intent is reconstructed from the same admitted model;
			// deleting it cannot authorize fresh policy, role or enrollment writes.
			intentPath := filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-renewal", r.Review, "intent.json")
			if mode == "success" {
				if err := os.Remove(intentPath); err != nil {
					t.Fatal(err)
				}
			}
			// A completed retry must re-read the native policy/role and accepted
			// Job without repeating any mutation or resetting the chosen session.
			actual, err = executeOriginalKeySessionRenewal(context.Background(), f.cfg, f.store, runtime, f.service, apply)
			if err != nil {
				t.Fatal("completed original renewal could not be reconciled", err)
			}
			retained, readErr := readBoundedEnvironmentFile(intentPath, true)
			if readErr != nil || string(retained) != string(mustProviderRecoveryJSON(r.RenewedIntent)) {
				t.Fatal("same admitted renewal intent was not preserved", readErr)
			}
			if actual.BootstrapSessionID != r.RenewedIntent.SessionID || actual.BootstrapClaim != f.service.BootstrapClaim || actual.Root.ID != f.service.Root.ID || actual.Intermediate.ID != f.service.Intermediate.ID || f.native.writes[r.Candidate.Resources[0].ID] != 1 {
				t.Fatal("renewal replayed enrollment or replaced original authority/custody")
			}
			_, j, err := acceptedCreateStage(f.store, f.e, "kubernetes/"+stage)
			if err != nil || !j.Accepted {
				t.Fatal("native original-key renewal has no accepted child", err)
			}
			var state map[string]any
			if err := readExecutionJSON(protocolPath, &state); err != nil {
				t.Fatal(err)
			}
			if state["role_writes"] != float64(1) || state["policy_writes"] != float64(1) {
				t.Fatal("exact native policy/role was not installed once before renewal")
			}
			if f.protocol["custody"].(map[string]string)["/run/pki-bootstrap/bootstrap.key"] != f.key || f.protocol["custody"].(map[string]string)["/run/pki-bootstrap/bootstrap.csr"] != f.csr || f.protocol["custody"].(map[string]string)["/run/pki-bootstrap/bootstrap.crt"] != f.certificate {
				t.Fatal("renewal replaced original custody files")
			}
		})
	}
}
