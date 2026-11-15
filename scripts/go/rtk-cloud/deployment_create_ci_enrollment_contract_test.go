package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func ciEnrollmentBlock(t *testing.T, err error, id string) {
	t.Helper()
	var block *deploymentplan.Blocker
	if !errors.As(err, &block) || block.ID != id {
		t.Fatalf("blocker = %v, want %s", err, id)
	}
}

func ciEnrollmentAcceptStage(t *testing.T, store secretStore, e *environmentCreateExecution, p *deploymentplan.Plan, b freshStageBackend, stage string) {
	t.Helper()
	fence := func(ctx context.Context, p *deploymentplan.Plan) error {
		return b.CheckParentExecution(ctx, p, store.Root)
	}
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, p, b, fence, fence, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, b) }, func(context.Context) error { return nil }, io.Discard); err != nil {
		t.Fatal(err)
	}
}

func TestCIEnrollmentResumeAdmissionPreservesOriginalEvidence(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	now := time.Now().UTC()
	review := strings.Repeat("a", 64)
	original := "issuer-enrollment-continuation-" + strings.Repeat("b", 64)
	relative := filepath.Join("deployment/create/issuer-enrollment-continuation", review, "resume-admission.json")
	for _, tc := range []struct {
		name, relative, review, original string
		now, deadline                    time.Time
	}{
		{"bad-review", relative, "bad", original, now, now.Add(time.Hour)},
		{"missing-owner", relative, review, "", now, now.Add(time.Hour)},
		{"zero-clock", relative, review, original, time.Time{}, now.Add(time.Hour)},
		{"expired", relative, review, original, now, now},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ciEnrollmentBlock(t, recordIssuerResumeAdmission(store, tc.relative, tc.review, tc.original, tc.now, tc.deadline), "deployment.create.recovery")
		})
	}
	if err := recordIssuerResumeAdmission(store, relative, review, original, now, now.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	first, err := os.ReadFile(filepath.Join(store.Root, relative))
	if err != nil {
		t.Fatal(err)
	}
	if err := recordIssuerResumeAdmission(store, relative, review, original, now.Add(time.Minute), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err := recordIssuerResumeAdmission(store, relative, review, original, now.Add(2*time.Minute), now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(filepath.Join(store.Root, relative))
	if !bytes.Equal(first, got) {
		t.Fatal("timeout supplement overwrote original admission")
	}
	supplement := filepath.Join(filepath.Dir(relative), "resume-admission-"+deploymentplan.Digest(now.Add(2 * time.Hour))[:16]+".json")
	var extended issuerResumeAdmission
	if readExecutionJSON(filepath.Join(store.Root, supplement), &extended) != nil || extended.Original != original || !extended.Deadline.Equal(now.Add(2*time.Hour)) {
		t.Fatal("extension lacks original authority and deadline")
	}
	ciEnrollmentBlock(t, recordIssuerResumeAdmission(store, relative, review, original, now.Add(-time.Minute), now.Add(time.Hour)), "deployment.create.recovery")
	if err := store.write(relative, []byte(`{"review_sha256":"changed"}`), true); err != nil {
		t.Fatal(err)
	}
	ciEnrollmentBlock(t, recordIssuerResumeAdmission(store, relative, review, original, now, now.Add(time.Hour)), "deployment.create.recovery")
}

func TestCIEnrollmentIngressDoesNotClaimUnrelatedOperations(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	for _, tc := range []struct{ environment, operation, action string }{{"staging", "create", "create"}, {"dev", "upgrade", "preflight"}, {"dev", "create", "status"}, {"dev", "create", "preflight"}} {
		report := &unifiedDeploymentReport{}
		handled, err := tryUnifiedIssuerEnrollment(t.Context(), deploymentConfig{Environment: tc.environment, Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + tc.environment}}, unifiedDeploymentOptions{Operation: tc.operation, Action: tc.action}, report, io.Discard, io.Discard)
		if handled || err != nil || report.Scope != "" {
			t.Fatalf("unrelated or absent checkpoint claimed: %v %v %+v", handled, err, report)
		}
	}
	if _, err := os.Stat(filepath.Join(store.Root, "deployment/.execution-active")); !os.IsNotExist(err) {
		t.Fatal("routing created an execution owner")
	}
}

func TestCIEnrollmentCLIRejectsUnreviewedLeaseMutations(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	digest := strings.Repeat("a", 64)
	for _, args := range [][]string{nil, {"--environment", "production", "--parent-plan-sha256", digest}, {"--environment", "dev", "--parent-plan-sha256", "bad"}, {"--environment", "dev", "--parent-plan-sha256", digest, "--execute"}, {"--environment", "dev", "--parent-plan-sha256", digest, "--review-sha256", digest}, {"--environment", "dev", "--parent-plan-sha256", digest, "--confirm", "video-cloud-dev"}, {"--environment", "dev", "--parent-plan-sha256", digest, "extra"}} {
		ciEnrollmentBlock(t, runEnvironmentCreateIssuerLeaseRepair(args), "deployment.create.recovery")
	}
	if err := runEnvironmentCreateIssuerLeaseRepair([]string{"--unknown-option"}); err == nil {
		t.Fatal("unknown command option accepted")
	}
	if _, err := os.Stat(filepath.Join(store.Root, "deployment/.execution-active")); !os.IsNotExist(err) {
		t.Fatal("invalid CLI created an owner")
	}
}

func TestCIEnrollmentCoreAdmissionRetainsQualifiedParent(t *testing.T) {
	store, e, p, b := freshStageFixture(t)
	ciEnrollmentAcceptStage(t, store, e, p, b, "service-issuer-server-enrollment")
	next := *p
	next.Resources = slices.Clone(p.Resources)
	next.Resources[0].Desired = deploymentplan.Clone(p.Resources[0].Desired)
	deploymentplan.Map(next.Resources[0].Desired["metadata"])["name"] = "original-service-core"
	next.Resources[0].ID = deploymentplan.ID(next.Resources[0].Desired)
	now := time.Now().UTC()
	proof := issuerEnrollmentContinuationProof{Parent: e.Journal.PlanSHA256, ParentJournal: deploymentplan.Digest(e.Journal), ParentOwner: deploymentplan.Digest(e.owner), FirstStage: next.SHA256(), Release: next.ReleaseSHA256}
	review := deploymentplan.Digest(proof)
	qualified := proof
	qualified.QualifiedAt = now
	qualified.ValidUntil = now.Add(time.Hour)
	relative := filepath.Join("deployment/create/issuer-enrollment-continuation", review, "qualification.json")
	if err := store.write(relative, mustProviderRecoveryJSON(qualified), false); err != nil {
		t.Fatal(err)
	}
	if err := e.BeginEntry(issuerEnrollmentContinuationPrefix+review, "continuation", mustProviderRecoveryJSON(proof), now); err != nil {
		t.Fatal(err)
	}
	entry := *createOpenBaoACKCompletionEntry(e.Journal, issuerEnrollmentContinuationPrefix+review)
	ciEnrollmentAcceptStage(t, store, e, &next, b, "service-managed-enrollment-issuer")
	before := deploymentplan.Digest(e.Journal)
	got, err := verifyIssuerCoreContinuationAdmission(store, e, entry)
	if err != nil || deploymentplan.Digest(got) != review || deploymentplan.Digest(e.Journal) != before {
		t.Fatalf("original admission rejected or changed: %v", err)
	}
	for _, mode := range []string{"owner", "deadline", "intent", "entry-absent", "parent-history", "stage-receipt"} {
		t.Run(mode, func(t *testing.T) {
			copy := *e
			copy.Journal.Entries = slices.Clone(e.Journal.Entries)
			changed := qualified
			candidate := entry
			switch mode {
			case "owner":
				changed.ParentOwner = strings.Repeat("f", 64)
			case "deadline":
				changed.ValidUntil = changed.ValidUntil.Add(time.Second)
			case "intent":
				candidate.IntentSHA256 = strings.Repeat("f", 64)
			case "entry-absent":
				copy.Journal.Entries = slices.DeleteFunc(copy.Journal.Entries, func(v environmentCreateJournalEntry) bool { return v.Key == entry.Key })
			case "parent-history":
				copy.Journal.AdmittedAt = copy.Journal.AdmittedAt.Add(time.Second)
			case "stage-receipt":
				copy.Journal.Entries[len(copy.Journal.Entries)-1].Receipt = json.RawMessage(`{"plan_sha256":"missing"}`)
			}
			if err := store.write(relative, mustProviderRecoveryJSON(changed), true); err != nil {
				t.Fatal(err)
			}
			_, err := verifyIssuerCoreContinuationAdmission(store, &copy, candidate)
			ciEnrollmentBlock(t, err, "deployment.create.recovery")
			if err := store.write(relative, mustProviderRecoveryJSON(qualified), true); err != nil {
				t.Fatal(err)
			}
			if deploymentplan.Digest(e.Journal) != before {
				t.Fatal("admission verification mutated the original history")
			}
		})
	}
}

func ciEnrollmentPartialProof(t *testing.T, e *environmentCreateExecution, candidate *deploymentplan.Plan) issuerEnrollmentContinuationProof {
	t.Helper()
	now := time.Now().UTC()
	digest := strings.Repeat("a", 64)
	if err := e.BeginEntry("kubernetes/service-session", "service-session", json.RawMessage(`{"source":"original-session"}`), now); err != nil {
		t.Fatal(err)
	}
	if err := e.RecordReceipt("kubernetes/service-session", json.RawMessage(`{"plan_sha256":"`+digest+`"}`)); err != nil {
		t.Fatal(err)
	}
	if err := e.CompleteEntry("kubernetes/service-session", now); err != nil {
		t.Fatal(err)
	}
	intent := createServiceSessionIntent{SessionID: "33333333-3333-4333-8333-333333333333", IssuerID: "44444444-4444-4444-8444-444444444444", RootSHA: digest, Subjects: []string{"service:certissuer"}, Namespaces: []string{e.Journal.Stack + "-video-cloud"}}
	renewed := intent
	renewed.SessionID = sessionRenewalUUID(e.Journal.PlanSHA256, digest, intent.SessionID)
	env := environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": renewed.SessionID, "PKI_BOOTSTRAP_ISSUER_ID": renewed.IssuerID, "PKI_BOOTSTRAP_ROOT_SHA256": renewed.RootSHA, "PKI_BOOTSTRAP_SUBJECTS": strings.Join(renewed.Subjects, ","), "PKI_BOOTSTRAP_KEY_PATH": "/run/pki-bootstrap/bootstrap.key", "PKI_BOOTSTRAP_CSR_PATH": "/run/pki-bootstrap/bootstrap.csr", "PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/renew-" + digest[:12] + "/bootstrap.crt"})
	if candidate == nil {
		candidate = &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: e.Journal.Environment, Stack: e.Journal.Stack, ReleaseSHA256: digest}, Resources: []deploymentplan.Resource{{ID: "Job/" + intent.Namespaces[0] + "/renew", Action: "create", Desired: deploymentplan.Object{"kind": "Job", "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"env": env}}}}}}}}}
	}
	container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(candidate.Resources[0].Desired)["containers"])[0])
	container["env"] = mergeEnvironmentNamedFields(container["env"], env)
	state := &managedSessionState{Session: intent.SessionID, Issuer: intent.IssuerID, Root: intent.RootSHA, Fingerprint: digest, Status: "active", Expires: now.Add(-time.Minute), Subjects: intent.Subjects}
	recovery := createManagedSessionRecovery{PartialInventory: true, OriginalState: state, OriginalSession: intent.SessionID, OriginalCertificatePath: "/run/pki-bootstrap/bootstrap.crt", OriginalCertificateSHA256: digest, SessionSHA256: digest, ClientsSHA256: digest, SourceStage: "kubernetes/service-session", SourcePlanSHA256: digest, SourceJournalSHA256: digest, BindingSHA256: digest, CandidateSHA256: candidate.SHA256()}
	q := issuerEnrollmentContinuationProof{Parent: e.Journal.PlanSHA256, ParentJournal: deploymentplan.Digest(e.Journal), ParentOwner: deploymentplan.Digest(e.owner), Release: candidate.ReleaseSHA256, PartialSession: &partialSessionContinuationProof{SourceJournal: e.Journal, Recovery: recovery, Intent: intent, RenewedIntent: renewed, KeySHA: digest, CSRSHA: digest, Candidate: candidate}}
	if !partialSessionProofMatches(q, e.Journal) {
		t.Fatal("isolated original session proof invalid")
	}
	return q
}

func TestCIEnrollmentPartialAdmissionRejectsAmbiguousAndChangedAuthority(t *testing.T) {
	store, e, _, _ := freshStageFixture(t)
	q := ciEnrollmentPartialProof(t, e, nil)
	review := deploymentplan.Digest(q)
	now := time.Now().UTC()
	empty, got, exists, err := loadPartialSessionAdmission(store, e)
	if err != nil || exists || got != "" || empty.Parent != "" {
		t.Fatal("no admission invented", err)
	}
	q.QualifiedAt = now
	q.ValidUntil = now.Add(time.Hour)
	relative := filepath.Join("deployment/create/issuer-enrollment-continuation", review, "qualification.json")
	if err := store.write(relative, mustProviderRecoveryJSON(q), false); err != nil {
		t.Fatal(err)
	}
	if err := beginPartialSessionAdmission(store, e, q, review, now); err != nil {
		t.Fatal(err)
	}
	first := deploymentplan.Digest(e.Journal)
	if err := beginPartialSessionAdmission(store, e, q, review, now.Add(2*time.Hour)); err != nil {
		t.Fatal("retry discarded original admitted deadline", err)
	}
	for _, mode := range []string{"ambiguous", "malformed-review", "missing-proof", "owner", "receipt"} {
		t.Run(mode, func(t *testing.T) {
			copy := *e
			copy.Journal.Entries = slices.Clone(e.Journal.Entries)
			switch mode {
			case "ambiguous":
				copy.Journal.Entries = append(copy.Journal.Entries, copy.Journal.Entries[len(copy.Journal.Entries)-1])
			case "malformed-review":
				copy.Journal.Entries[len(copy.Journal.Entries)-1].Key = "partial-session-renewal-bad"
			case "missing-proof":
				copy.Journal.Entries[len(copy.Journal.Entries)-1].Key = "partial-session-renewal-" + strings.Repeat("f", 64)
			case "owner":
				copy.owner.OwnerToken = "foreign"
			case "receipt":
				copy.Journal.Entries[len(copy.Journal.Entries)-1].Receipt = json.RawMessage(`{"unadmitted":true}`)
			}
			_, _, _, err := loadPartialSessionAdmission(store, &copy)
			ciEnrollmentBlock(t, err, "deployment.create.recovery")
			if deploymentplan.Digest(e.Journal) != first {
				t.Fatal("inspection changed admitted original journal")
			}
		})
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, ServiceSessionRecovery: &createServiceSessionRecovery{Review: review, ManagedTransportRecovery: &createManagedTransportRecovery{PartialSessionRenewal: true, SessionRenewal: &q.PartialSession.Recovery}}}
	for _, kind := range []string{"policy", "role", "secret"} {
		if partialSessionAllowsPendingOpenBao(deploymentConfig{Environment: "dev"}, runtime, kind, "foreign-owner") {
			t.Fatal("foreign pending effect accepted")
		}
	}
	ciEnrollmentBlock(t, observePartialSessionSuccessor(t.Context(), deploymentConfig{}, store, e, environmentKubernetesBackend{}, issuerEnrollmentContinuationProof{}, review, runtime.ServiceSessionRecovery), "deployment.create.recovery")
	ciEnrollmentBlock(t, completePartialSessionAdmission(t.Context(), deploymentConfig{}, store, environmentCreateBootstrapRuntime{}, environmentCreateServiceCeremony{}), "deployment.bootstrap.pki")
}

func TestCIEnrollmentTrustForwardAdmissionBindsOriginalFailedGeneration(t *testing.T) {
	store, e, _, _ := freshStageFixture(t)
	f := managedLateSeedForwardFixture(t)
	original := ciEnrollmentPartialProof(t, e, f.FailedPlan)
	oldReview := deploymentplan.Digest(original)
	now := time.Now().UTC()
	f.FailedPlanSHA = f.FailedPlan.SHA256()
	f.FailedJournal.PlanSHA256 = f.FailedPlanSHA
	f.FailedJournalSHA = deploymentplan.Digest(f.FailedJournal)
	if err := e.BeginEntry("kubernetes/service-session-original-key-renewal-"+oldReview, "seed", json.RawMessage(`{"original":"failed-job"}`), now); err != nil {
		t.Fatal(err)
	}
	f.OriginalSource = original.PartialSession.SourceJournal
	f.OriginalReview = oldReview
	f.OriginalArtifactSHA = oldReview
	f.SourceSHA = deploymentplan.Digest(e.Journal)
	f.SourceOwnerSHA = deploymentplan.Digest(e.owner)
	f.NativeEvidence = map[string]string{"managed_trust": strings.Repeat("a", 64), "terminal_pod": f.FailedPodUID, "no_mint": strings.Repeat("a", 64), "original_state": strings.Repeat("a", 64), "clients": original.PartialSession.Recovery.ClientsSHA256}
	f.Candidate = managedLateSeedForwardFixtureCandidate(t, f)
	forward := issuerEnrollmentContinuationProof{Parent: e.Journal.PlanSHA256, ParentJournal: deploymentplan.Digest(e.Journal), ParentOwner: deploymentplan.Digest(e.owner), PartialTrustForward: &partialSessionTrustForwardProof{SourceJournal: e.Journal, OriginalReview: oldReview, Forward: f}}
	review := deploymentplan.Digest(forward)
	if !partialSessionTrustForwardProofMatches(forward, original, oldReview, e.Journal) {
		t.Fatal("exact original failed job proof rejected")
	}
	forward.QualifiedAt = now
	forward.ValidUntil = now.Add(time.Hour)
	path := filepath.Join("deployment/create/issuer-enrollment-continuation", review, "qualification.json")
	if err := store.write(path, mustProviderRecoveryJSON(forward), false); err != nil {
		t.Fatal(err)
	}
	if err := beginPartialSessionTrustForward(store, e, forward, review, original, oldReview, now); err != nil {
		t.Fatal(err)
	}
	before := deploymentplan.Digest(e.Journal)
	loaded, got, exists, err := loadPartialSessionTrustForwardAdmission(store, e, original, oldReview)
	if err != nil || !exists || got != review || deploymentplan.Digest(loaded) != review {
		t.Fatalf("durable trust-only admission rejected: %v", err)
	}
	if err := beginPartialSessionTrustForward(store, e, forward, review, original, oldReview, now.Add(2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	ciEnrollmentBlock(t, beginPartialSessionTrustForward(store, e, forward, strings.Repeat("f", 64), original, oldReview, now), "deployment.create.recovery")
	for _, mode := range []string{"ambiguous", "phase", "receipt", "owner", "original-review"} {
		t.Run(mode, func(t *testing.T) {
			copy := *e
			copy.Journal.Entries = slices.Clone(e.Journal.Entries)
			old := oldReview
			switch mode {
			case "ambiguous":
				copy.Journal.Entries = append(copy.Journal.Entries, copy.Journal.Entries[len(copy.Journal.Entries)-1])
			case "phase":
				copy.Journal.Entries[len(copy.Journal.Entries)-1].Phase = "foreign"
			case "receipt":
				copy.Journal.Entries[len(copy.Journal.Entries)-1].Receipt = json.RawMessage(`{"foreign":true}`)
			case "owner":
				copy.owner.OwnerToken = "foreign"
			case "original-review":
				old = strings.Repeat("f", 64)
			}
			_, _, _, err := loadPartialSessionTrustForwardAdmission(store, &copy, original, old)
			ciEnrollmentBlock(t, err, "deployment.create.recovery")
		})
	}
	if deploymentplan.Digest(e.Journal) != before {
		t.Fatal("trust-only retry changed original admission")
	}
	if partialSessionTrustForwardAlias(nil, review) != nil || partialSessionTrustForwardIntent(e.Journal.PlanSHA256, review, nil) != nil {
		t.Fatal("missing forward was granted identity")
	}
}

func ciEnrollmentKubectl(t *testing.T, store secretStore, objects map[string]deploymentplan.Object) (environmentKubernetesBackend, string) {
	t.Helper()
	dir := t.TempDir()
	state := filepath.Join(dir, "objects.json")
	log := filepath.Join(dir, "calls.jsonl")
	if err := os.WriteFile(state, mustProviderRecoveryJSON(objects), 0600); err != nil {
		t.Fatal(err)
	}
	python, err := exec.LookPath("python3")
	if err != nil {
		t.Fatal(err)
	}
	code := `import json,sys,os
root=os.path.dirname(__file__)
a=sys.argv[1:];a=a[2:];a=[x for x in a if not x.startswith('--request-timeout=')]
with open(os.path.join(root,'calls.jsonl'),'a') as f:f.write(json.dumps(a)+'\n')
with open(os.path.join(root,'objects.json')) as f:objects=json.load(f)
ns=a[a.index('--namespace')+1] if '--namespace' in a else ''
if a[0]=='--namespace':a=a[2:]
if a[0]=='get':
 if len(a)<3 or a[2].startswith('-'):
  if a[1]=='namespace':
   names=sorted({v.get('metadata',{}).get('namespace','') for v in objects.values() if v.get('kind')})
   rows=[{'apiVersion':'v1','kind':'Namespace','metadata':{'name':n}} for n in names if n]
  else:rows=[v for v in objects.values() if v.get('kind') and v.get('metadata',{}).get('namespace','')==ns]
  print(json.dumps({'items':rows}))
 else:
  key=a[1]+'/'+ns+'/'+a[2]
  if key in objects:print(json.dumps(objects[key]))
elif a[0]=='patch':
 key=a[1]+'/'+ns+'/'+a[2];v=objects[key]
 for op in json.loads(a[a.index('--patch')+1]):
  if op['op']=='test' and v['metadata'].get(op['path'].split('/')[-1])!=op['value']:sys.exit(42)
  if op['op']=='add':v['metadata']['labels']=op['value']
 if '--dry-run=server' not in a:
  objects[key]=v
  with open(os.path.join(root,'objects.json'),'w') as f:json.dump(objects,f)
 print(json.dumps(v))
elif a[0]=='create':
 v=json.load(sys.stdin);m=v['metadata'];key=v['kind']+'/'+m.get('namespace','')+'/'+m['name']
 assert v['kind']=='Lease' and m['name']=='rtk-deployment-execution' and key not in objects
 m['uid']='99999999-9999-4999-8999-999999999999';m['resourceVersion']='1'
 if '--dry-run=server' not in a:
  objects[key]=v
  with open(os.path.join(root,'objects.json'),'w') as f:json.dump(objects,f)
 print(json.dumps(v))
elif a[0]=='delete':
 raw=next(x for x in a if x.startswith('--raw='));parts=raw.split('/');name=parts[-1];namespace=parts[parts.index('namespaces')+1]
 key='Lease/'+namespace+'/'+name;request=json.load(sys.stdin)
 assert name=='rtk-deployment-execution' and objects[key]['metadata']['uid']==request['preconditions']['uid']
 del objects[key]
 with open(os.path.join(root,'objects.json'),'w') as f:json.dump(objects,f)
 print('{}')
elif a[0]=='wait':
 pass
elif a[0]=='exec' and a[-1].startswith('SELECT '):
 if 'pki_service_client_issuances' in a[-1]:print('[]')
 elif '__registry_by_session__' in objects:print(json.dumps(next(v for sid,v in objects['__registry_by_session__'].items() if sid in a[-1])))
 else:print(json.dumps(objects['__registry__']))
elif a[0]=='exec' and 'cat' in a:
 print(objects['__custody__'][a[-1]],end='')
else:sys.exit(45)
`
	if err := os.WriteFile(filepath.Join(dir, "fixture.py"), []byte(code), 0600); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(dir, "kubectl")
	body := "#!/bin/sh\nexec '" + python + "' '" + filepath.Join(dir, "fixture.py") + "' \"$@\"\n"
	if err := os.WriteFile(executable, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", executable)
	return environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-selected-kubeconfig"), RolloutTimeout: time.Minute}, log
}

func ciEnrollmentLeaseFixture(t *testing.T) (issuerLeaseEnrollmentRecovery, map[string]deploymentplan.Object) {
	t.Helper()
	const ns = "video-cloud-dev-video-cloud"
	lease := "pki-state-" + strings.Repeat("a", 32)
	p := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped"}}
	j := deploymentplan.Journal{Owners: map[string]string{}}
	objects := map[string]deploymentplan.Object{}
	for _, tc := range []struct{ kind, namespace, name, action string }{{"Lease", ns, lease, "create"}, {"Role", ns, lease, "create"}, {"RoleBinding", ns, lease, "create"}, {"Job", ns, "pki-enroll-certissuer-server-123456789abc", "create"}, {"ServiceAccount", ns, "certissuer", "retain"}, {"ServiceAccount", ns, "pki-state-" + strings.Repeat("b", 32), "create"}, {"NetworkPolicy", "video-cloud-dev-secrets", "allow-openbao-workload-custody", "retain"}} {
		desired := deploymentplan.Object{"apiVersion": "v1", "kind": tc.kind, "metadata": deploymentplan.Object{"namespace": tc.namespace, "name": tc.name, "labels": deploymentplan.Object{"app.kubernetes.io/managed-by": "rtk-cloud", "rtk.realtek.com/stack": "video-cloud-dev"}}}
		if tc.kind == "Lease" {
			desired["apiVersion"] = "coordination.k8s.io/v1"
		}
		actual := deploymentplan.Clone(desired)
		meta := deploymentplan.Map(actual["metadata"])
		meta["uid"] = "11111111-1111-4111-8111-111111111111"
		meta["resourceVersion"] = "42"
		id := deploymentplan.ID(desired)
		resource := deploymentplan.Resource{ID: id, Action: tc.action, Phase: "bootstrap", Desired: desired}
		if tc.kind == "Lease" {
			delete(meta, "labels")
			actual["spec"] = deploymentplan.Object{"leaseTransitions": 1}
			j.Failed = id
		}
		if tc.kind == "Job" {
			actual["status"] = deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
		}
		if tc.action == "retain" {
			resource.UID = deploymentplan.String(meta["uid"])
			resource.Baseline = deploymentplan.Projection(actual)
		}
		p.Resources = append(p.Resources, resource)
		j.Completed = append(j.Completed, id)
		j.Owners[id] = deploymentplan.String(meta["uid"])
		objects[id] = actual
	}
	j.PlanSHA256 = p.SHA256()
	return issuerLeaseEnrollmentRecovery{plan: p, journal: j}, objects
}

func TestCIEnrollmentLeaseLabelQualificationIsDryRunAndFenced(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	r, objects := ciEnrollmentLeaseFixture(t)
	backend, log := ciEnrollmentKubectl(t, store, objects)
	path := filepath.Join("deployment/rollback", r.plan.SHA256())
	if err := store.write(filepath.Join(path, "plan.json"), mustProviderRecoveryJSON(r.plan), false); err != nil {
		t.Fatal(err)
	}
	if err := store.write(filepath.Join(path, "journal.json"), mustProviderRecoveryJSON(r.journal), false); err != nil {
		t.Fatal(err)
	}
	before := deploymentplan.Digest(objects)
	patch, err := r.prepareLabelPatch(t.Context(), backend)
	if err != nil || patch.Resource != r.journal.Failed || patch.BeforeSHA != deploymentplan.Digest(objects[r.journal.Failed]) || patch.AfterSHA == "" {
		t.Fatalf("exact released Lease qualification failed: %v", err)
	}
	if err := r.checkLabelPatch(t.Context(), backend); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var args []string
		if json.Unmarshal([]byte(line), &args) != nil {
			t.Fatal("invalid fixture call")
		}
		verb := args[0]
		if verb == "--namespace" {
			verb = args[2]
		}
		if verb != "get" && verb != "wait" && (verb != "patch" || !slices.Contains(args, "--dry-run=server")) {
			t.Fatalf("qualification issued a native mutation: %v", args)
		}
	}
	if deploymentplan.Digest(objects) != before {
		t.Fatal("qualification altered original observations")
	}
	got, err := patch.call(t.Context(), backend, true)
	if err != nil || deploymentplan.Map(got["metadata"])["uid"] != "11111111-1111-4111-8111-111111111111" {
		t.Fatalf("patch lost recorded owner: %v", err)
	}
	_, err = (issuerLeasePatchPlan{Resource: "not-a-resource"}).call(t.Context(), backend, true)
	if err == nil {
		t.Fatal("malformed resource identity accepted")
	}
}

func TestCIEnrollmentLeaseRecoveryRejectsUncertainOriginalOwners(t *testing.T) {
	for _, mode := range []string{"duplicate-completion", "missing-resource", "deleting", "read-error", "wrong-phase", "wrong-kind", "desired-lease-spec", "lease-version", "job-failed", "job-ambiguous", "uncompleted-job", "labels"} {
		t.Run(mode, func(t *testing.T) {
			r, objects := ciEnrollmentLeaseFixture(t)
			id := r.journal.Failed
			switch mode {
			case "duplicate-completion":
				r.journal.Completed[1] = r.journal.Completed[0]
			case "missing-resource":
				delete(objects, id)
			case "deleting":
				deploymentplan.Map(objects[id]["metadata"])["deletionTimestamp"] = "2026-01-01T00:00:00Z"
			case "wrong-phase":
				r.plan.Resources[0].Phase = "foreign"
			case "wrong-kind":
				r.plan.Resources[0].Desired["kind"] = "ConfigMap"
			case "desired-lease-spec":
				r.plan.Resources[0].Desired["spec"] = deploymentplan.Object{"holderIdentity": "foreign"}
			case "lease-version":
				delete(deploymentplan.Map(objects[id]["metadata"]), "resourceVersion")
			case "job-failed":
				objects[r.plan.Resources[3].ID]["status"] = deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Failed", "status": "True"}}}
			case "job-ambiguous":
				r.plan.Resources[2].Desired["kind"] = "Job"
				r.plan.Resources[2].ID = deploymentplan.ID(r.plan.Resources[2].Desired)
			case "uncompleted-job":
				delete(objects[r.plan.Resources[3].ID], "status")
			case "labels":
				deploymentplan.Map(r.plan.Resources[0].Desired["metadata"])["labels"] = deploymentplan.Object{"unexpected": "foreign"}
			}
			r.journal.PlanSHA256 = r.plan.SHA256()
			before := deploymentplan.Digest([]any{r.plan, r.journal})
			sentinel := errors.New("selected API read denied")
			err := r.verify(t.Context(), func(_ context.Context, id string) (deploymentplan.Object, error) {
				if mode == "read-error" {
					return nil, sentinel
				}
				return objects[id], nil
			})
			if mode == "read-error" {
				if !errors.Is(err, sentinel) {
					t.Fatal("lost original API failure", err)
				}
			} else {
				ciEnrollmentBlock(t, err, "deployment.create.recovery.issuer-lease")
			}
			if deploymentplan.Digest([]any{r.plan, r.journal}) != before {
				t.Fatal("verification rewrote original plan or receipt")
			}
		})
	}
}

func TestCIEnrollmentPartialRecoveryRefusesMissingCustody(t *testing.T) {
	store, e, _, _ := freshStageFixture(t)
	cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": e.Journal.Stack}}
	backend, log := ciEnrollmentKubectl(t, store, map[string]deploymentplan.Object{})
	r := &createServiceSessionRecovery{ManagedTransportRecovery: &createManagedTransportRecovery{}}
	for _, candidate := range []*createServiceSessionRecovery{nil, r} {
		ciEnrollmentBlock(t, preparePartialIssuerSessionRenewal(t.Context(), cfg, store, e, backend, candidate), "deployment.create.recovery")
	}
	_, err := verifyPartialSessionOpenBaoTrust(t.Context(), store, e, backend, environmentCreateRegisteredIssuer{RootFingerprint: "invalid"})
	ciEnrollmentBlock(t, err, "deployment.create.recovery")
	_, err = prepareIssuerEnrollmentRuntime(t.Context(), cfg, store, e, backend, nil, environmentCreatePlan{}, nil, false, false)
	if err == nil {
		t.Fatal("missing original foundation identity admitted")
	}
	_, err = restoreAcceptedIssuerEnrollment(t.Context(), cfg, store, e, backend, nil, nil, environmentCreatePlan{}, false, false)
	ciEnrollmentBlock(t, err, "deployment.create.recovery")
	_, err = preparePartialSessionTrustForward(t.Context(), cfg, store, e, backend, issuerEnrollmentContinuationProof{}, "invalid", r)
	ciEnrollmentBlock(t, err, "deployment.create.recovery")
	if raw, err := os.ReadFile(log); err == nil && len(raw) != 0 {
		t.Fatalf("missing custody triggered native I/O: %s", raw)
	}
}

func ciEnrollmentCredentialMaterial(t *testing.T, expired bool) (environmentCreateServiceCeremony, createServiceSessionIntent, managedSessionState, map[string]string) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	rootKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	root := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "original enrolled Service Root"}, NotBefore: now.Add(-time.Hour), NotAfter: now.Add(2 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	rootDER, err := x509.CreateCertificate(rand.Reader, root, root, &rootKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	root, err = x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	expiry := now.Add(time.Hour)
	if expired {
		expiry = now.Add(-time.Minute)
	}
	leaf := &x509.Certificate{SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "service:deployment-bootstrap"}, NotBefore: now.Add(-time.Hour), NotAfter: expiry, KeyUsage: x509.KeyUsageDigitalSignature}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, root, &key.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: leaf.Subject}, key)
	if err != nil {
		t.Fatal(err)
	}
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: rootDER}))
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER}))
	rootKeyDER, err := x509.MarshalECPrivateKey(rootKey)
	if err != nil {
		t.Fatal(err)
	}
	cert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})) + ca
	csr := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))
	intent := createServiceSessionIntent{SessionID: "33333333-3333-4333-8333-333333333333", IssuerID: "44444444-4444-4444-8444-444444444444", RootSHA: environmentPreflightBytesSHA256(rootDER), Claim: "pki-service-bootstrap-state", Subjects: []string{"service:certissuer"}, Namespaces: []string{"video-cloud-dev-video-cloud", "video-cloud-dev-account-manager", "video-cloud-dev-secrets"}}
	service := environmentCreateServiceCeremony{Root: environmentCreateRegisteredIssuer{Certificate: ca, RootFingerprint: intent.RootSHA}, Intermediate: environmentCreateRegisteredIssuer{ID: intent.IssuerID, Certificate: ca}, BootstrapSessionID: intent.SessionID, BootstrapClaim: intent.Claim, BootstrapCredentialSecret: "service-bootstrap-" + intent.SessionID, BootstrapCredentialNamespaces: intent.Namespaces, CustodyReader: "original-bootstrap-custody"}
	state := managedSessionState{Session: intent.SessionID, Issuer: intent.IssuerID, Root: intent.RootSHA, Fingerprint: environmentPreflightBytesSHA256(leafDER), Status: "active", Expires: expiry, Subjects: intent.Subjects}
	return service, intent, state, map[string]string{"tls.key": keyPEM, "tls.crt": cert, "ca.crt": ca, "bootstrap.csr": csr, "fixture-root.key": string(pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: rootKeyDER}))}
}

func ciEnrollmentCredentialFixture(t *testing.T, expired bool) (deploymentConfig, secretStore, *environmentCreateExecution, environmentKubernetesBackend, environmentCreateServiceCeremony, createServiceSessionIntent, managedSessionState, map[string]deploymentplan.Object, string) {
	t.Helper()
	store, e, p, b := freshStageFixture(t)
	service, intent, state, material := ciEnrollmentCredentialMaterial(t, expired)
	ciEnrollmentAcceptStage(t, store, e, p, b, "service-issuer-server-enrollment")
	var defs []deploymentplan.Definition
	for _, ns := range intent.Namespaces {
		d := environmentCreateSecret(ns, service.BootstrapCredentialSecret, map[string]string{"tls.key": material["tls.key"], "tls.crt": material["tls.crt"], "ca.crt": material["ca.crt"]})
		d.Object["immutable"] = true
		d.Phase = "bootstrap"
		defs = append(defs, d)
	}
	inputs := p.Inputs
	inputs.BindingsSHA256 = environmentCreateStageBindingsSHA("service-credential-secrets", defs)
	credentials, err := deploymentplan.Compile(inputs, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	ciEnrollmentAcceptStage(t, store, e, credentials, b, "service-credential-secrets")
	_, journal, err := acceptedCreateStage(store, e, "kubernetes/service-credential-secrets")
	if err != nil {
		t.Fatal(err)
	}
	observed := createServiceSessionObserved{UIDs: map[string]string{}}
	for _, ns := range intent.Namespaces {
		observed.UIDs[ns] = journal.Owners["Secret/"+ns+"/"+service.BootstrapCredentialSecret]
	}
	base := filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/service")
	if err := store.write(filepath.Join(base, "session-intent.json"), mustProviderRecoveryJSON(intent), false); err != nil {
		t.Fatal(err)
	}
	if err := store.write(filepath.Join(base, "session-observed.json"), mustProviderRecoveryJSON(observed), false); err != nil {
		t.Fatal(err)
	}
	objects := map[string]deploymentplan.Object{}
	for id, body := range b.objects {
		objects[id] = deploymentplan.Clone(body)
	}
	raw := mustProviderRecoveryJSON(state)
	var stateObject deploymentplan.Object
	if err := json.Unmarshal(raw, &stateObject); err != nil {
		t.Fatal(err)
	}
	objects["__registry__"] = stateObject
	objects["__custody__"] = deploymentplan.Object{"/run/pki-bootstrap/bootstrap.key": material["tls.key"], "/run/pki-bootstrap/bootstrap.crt": material["tls.crt"], "/run/pki-bootstrap/bootstrap.csr": material["bootstrap.csr"]}
	backend, log := ciEnrollmentKubectl(t, store, objects)
	cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": e.Journal.Stack}}
	return cfg, store, e, backend, service, intent, state, objects, log
}

func TestCIEnrollmentAcceptedCredentialsRequireOriginalSessionAndOwners(t *testing.T) {
	cfg, store, e, backend, _, intent, state, objects, log := ciEnrollmentCredentialFixture(t, false)
	if err := verifyAcceptedIssuerEnrollmentContinuation(t.Context(), cfg, store, e, backend, false); err != nil {
		t.Fatal("accepted original session rejected", err)
	}
	baseline := deploymentplan.Digest(e.Journal)
	path := filepath.Join(filepath.Dir(log), "objects.json")
	for _, mode := range []string{"competing-session", "expired-session", "replacement-namespace-owner", "foreign-certificate", "invalid-original-intent", "missing-core"} {
		t.Run(mode, func(t *testing.T) {
			saved := mustProviderRecoveryJSON(objects)
			var live map[string]deploymentplan.Object
			if err := json.Unmarshal(saved, &live); err != nil {
				t.Fatal(err)
			}
			currentIntent := intent
			switch mode {
			case "competing-session":
				live["__registry__"]["other_active"] = 1
			case "expired-session":
				live["__registry__"]["expires_at"] = time.Now().Add(-time.Minute)
			case "replacement-namespace-owner":
				deploymentplan.Map(live["Secret/"+intent.Namespaces[1]+"/service-bootstrap-"+intent.SessionID]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "foreign-certificate":
				deploymentplan.Map(live["Secret/"+intent.Namespaces[1]+"/service-bootstrap-"+intent.SessionID]["data"])["tls.crt"] = base64.StdEncoding.EncodeToString([]byte("foreign"))
			case "invalid-original-intent":
				currentIntent.Claim = "foreign-state"
			}
			if err := os.WriteFile(path, mustProviderRecoveryJSON(live), 0600); err != nil {
				t.Fatal(err)
			}
			relative := filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-intent.json")
			if err := store.write(relative, mustProviderRecoveryJSON(currentIntent), true); err != nil {
				t.Fatal(err)
			}
			err := verifyAcceptedIssuerEnrollmentContinuation(t.Context(), cfg, store, e, backend, mode == "missing-core")
			want := "deployment.create.recovery"
			if mode == "replacement-namespace-owner" || mode == "foreign-certificate" {
				want = "deployment.drift"
			}
			ciEnrollmentBlock(t, err, want)
			if err := store.write(relative, mustProviderRecoveryJSON(intent), true); err != nil {
				t.Fatal(err)
			}
			if deploymentplan.Digest(e.Journal) != baseline {
				t.Fatal("failed credential observation rewrote enrollment")
			}
		})
	}
	if state.Session != intent.SessionID {
		t.Fatal("fixture changed original session")
	}
}

func TestCIEnrollmentExpiredSessionReusesExactKeyCSRCustody(t *testing.T) {
	cfg, store, e, backend, service, intent, _, objects, log := ciEnrollmentCredentialFixture(t, true)
	r := &createServiceSessionRecovery{Ceremony: service, ManagedTransportRecovery: &createManagedTransportRecovery{}}
	before := deploymentplan.Digest(e.Journal)
	if err := preparePartialIssuerSessionRenewal(t.Context(), cfg, store, e, backend, r); err != nil {
		t.Fatal("original expired custody rejected", err)
	}
	if !r.Renewal || !r.PostServiceRenewal || !r.ManagedTransportRecovery.PartialSessionRenewal || r.Intent.SessionID != intent.SessionID || r.RenewedIntent.SessionID != intent.SessionID || r.OriginalKeySHA == "" || r.OriginalCSRSHA == "" || r.SourceRenewalStage != "kubernetes/service-session" {
		t.Fatal("renewal changed original session or lost original key/CSR pins")
	}
	if deploymentplan.Digest(e.Journal) != before {
		t.Fatal("read-only renewal preparation changed original journal")
	}
	path := filepath.Join(filepath.Dir(log), "objects.json")
	for _, mode := range []string{"namespace-key-disagreement", "reader-key-disagreement", "reader-csr-disagreement", "unexpired-session"} {
		t.Run(mode, func(t *testing.T) {
			var changed map[string]deploymentplan.Object
			if err := json.Unmarshal(mustProviderRecoveryJSON(objects), &changed); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "namespace-key-disagreement":
				deploymentplan.Map(changed["Secret/"+intent.Namespaces[1]+"/service-bootstrap-"+intent.SessionID]["data"])["tls.key"] = base64.StdEncoding.EncodeToString([]byte("foreign"))
			case "reader-key-disagreement":
				changed["__custody__"]["/run/pki-bootstrap/bootstrap.key"] = "foreign"
			case "reader-csr-disagreement":
				changed["__custody__"]["/run/pki-bootstrap/bootstrap.csr"] = "foreign"
			case "unexpired-session":
				changed["__registry__"]["expires_at"] = time.Now().Add(time.Hour)
			}
			if err := os.WriteFile(path, mustProviderRecoveryJSON(changed), 0600); err != nil {
				t.Fatal(err)
			}
			next := &createServiceSessionRecovery{Ceremony: service, ManagedTransportRecovery: &createManagedTransportRecovery{}}
			ciEnrollmentBlock(t, preparePartialIssuerSessionRenewal(t.Context(), cfg, store, e, backend, next), "deployment.create.recovery")
			if next.Renewal || next.OriginalKeySHA != "" || deploymentplan.Digest(e.Journal) != before {
				t.Fatal("rejected custody acquired renewal authority")
			}
		})
	}
}

func TestCIEnrollmentIngressPreservesStoppedOwnerOnInvalidAuthority(t *testing.T) {
	for _, mode := range []string{"foreign-parent", "email-proof", "total-timeout", "missing-original-operator"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, e, _, plan := providerExecutionContractFixture(t)
			e, _ = providerExecutionContractFork(t, e, plan, "")
			definition := deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": e.Journal.Stack + "-video-cloud", "name": "accepted-server-checkpoint"}, "data": deploymentplan.Object{"owner": "original"}}}
			child, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest("original-enrollment-release"), BindingsSHA256: environmentCreateStageBindingsSHA("service-issuer-server-enrollment", []deploymentplan.Definition{definition})}, []deploymentplan.Definition{definition}, nil)
			if err != nil {
				t.Fatal(err)
			}
			backend := freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}}}
			ciEnrollmentAcceptStage(t, store, e, child, backend, "service-issuer-server-enrollment")
			parent := e.Journal.PlanSHA256
			if err := e.Close(); err != nil {
				t.Fatal(err)
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			options := unifiedDeploymentOptions{Operation: "create", Action: "preflight"}
			switch mode {
			case "foreign-parent":
				options.ParentPlanSHA256 = strings.Repeat("f", 64)
			case "email-proof":
				options.EmailKeyRepairProof = strings.Repeat("f", 64)
			case "total-timeout":
				options.TotalTimeout = time.Hour
			}
			var out, diagnostics bytes.Buffer
			report := &unifiedDeploymentReport{}
			handled, err := tryUnifiedIssuerEnrollment(t.Context(), cfg, options, report, &out, &diagnostics)
			if !handled || err == nil || report.Scope != "original-generation-recovery" {
				t.Fatalf("original recovery was not explicitly blocked: handled=%v err=%v report=%+v", handled, err, report)
			}
			if len(report.Gates) != 1 || report.Gates[0].ID != "deployment.create.recovery" || report.Gates[0].Status != "BLOCKED" {
				t.Fatalf("original continuation rejection lost its recovery gate: %+v", report.Gates)
			}
			assertRecoveryContractReadOnly(t, store, before)
			reopened, _, err := loadActiveTransportExecution(cfg, store, parent)
			if err != nil {
				t.Fatal("rejected continuation consumed original execution owner", err)
			}
			if err := reopened.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestCIEnrollmentMintedSuccessorRequiresExactNativeCustodyAndCompletion(t *testing.T) {
	store, e, base, b := freshStageFixture(t)
	service, intent, old, material := ciEnrollmentCredentialMaterial(t, true)
	service.BootstrapCredentialNamespace = intent.Namespaces[0]
	makeJob := func(name, sid, certPath string) deploymentplan.Definition {
		return deploymentplan.Definition{Phase: "bootstrap", Object: deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"namespace": intent.Namespaces[0], "name": name}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "bootstrap", "image": "ghcr.io/example/fixture@sha256:" + strings.Repeat("a", 64), "env": environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": sid, "PKI_BOOTSTRAP_ISSUER_ID": intent.IssuerID, "PKI_BOOTSTRAP_ROOT_SHA256": intent.RootSHA, "PKI_BOOTSTRAP_SUBJECTS": strings.Join(intent.Subjects, ","), "PKI_BOOTSTRAP_KEY_PATH": "/run/pki-bootstrap/bootstrap.key", "PKI_BOOTSTRAP_CSR_PATH": "/run/pki-bootstrap/bootstrap.csr", "PKI_BOOTSTRAP_CERT_PATH": certPath})}}}}}}}
	}
	makePlan := func(stage string, d deploymentplan.Definition) *deploymentplan.Plan {
		t.Helper()
		in := base.Inputs
		in.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, []deploymentplan.Definition{d})
		p, err := deploymentplan.Compile(in, []deploymentplan.Definition{d}, nil)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	source := makePlan("service-session", makeJob("original-session", intent.SessionID, "/run/pki-bootstrap/bootstrap.crt"))
	ciEnrollmentAcceptStage(t, store, e, source, b, "service-session")
	sourceJournal, ok, err := source.ReadExecution(store.Root)
	if err != nil || !ok {
		t.Fatal(err)
	}
	binding := strings.Repeat("b", 64)
	renewed := intent
	renewed.SessionID = sessionRenewalUUID(e.Journal.PlanSHA256, binding, intent.SessionID)
	certPath := "/run/pki-bootstrap/renew-" + binding[:12] + "/bootstrap.crt"
	candidate := makePlan("renewal", makeJob("same-key-renewal", renewed.SessionID, certPath))
	recovery := createManagedSessionRecovery{PartialInventory: true, OriginalState: &old, OriginalSession: intent.SessionID, OriginalCertificatePath: "/run/pki-bootstrap/bootstrap.crt", OriginalCertificateSHA256: environmentPreflightBytesSHA256([]byte(material["tls.crt"])), SessionSHA256: deploymentplan.Digest(old), ClientsSHA256: deploymentplan.Digest([]managedSessionClient{}), SourceStage: "kubernetes/service-session", SourcePlanSHA256: source.SHA256(), SourceJournalSHA256: deploymentplan.Digest(sourceJournal), BindingSHA256: binding, CandidateSHA256: candidate.SHA256()}
	q := issuerEnrollmentContinuationProof{Parent: e.Journal.PlanSHA256, ParentJournal: deploymentplan.Digest(e.Journal), ParentOwner: deploymentplan.Digest(e.owner), Release: candidate.ReleaseSHA256, PartialSession: &partialSessionContinuationProof{SourceJournal: e.Journal, Recovery: recovery, Intent: intent, RenewedIntent: renewed, KeySHA: environmentPreflightBytesSHA256([]byte(material["tls.key"])), CSRSHA: environmentPreflightBytesSHA256([]byte(material["bootstrap.csr"])), Candidate: candidate}}
	review := deploymentplan.Digest(q)
	qualified := q
	now := time.Now().UTC()
	qualified.QualifiedAt = now
	qualified.ValidUntil = now.Add(time.Hour)
	if err := store.write(filepath.Join("deployment/create/issuer-enrollment-continuation", review, "qualification.json"), mustProviderRecoveryJSON(qualified), false); err != nil {
		t.Fatal(err)
	}
	if err := beginPartialSessionAdmission(store, e, qualified, review, now); err != nil {
		t.Fatal(err)
	}
	r := &createServiceSessionRecovery{Ceremony: service, ManagedTransportRecovery: &createManagedTransportRecovery{}}
	if err := restorePartialSessionProof(q, review, e.Journal, r); err != nil {
		t.Fatal(err)
	}
	ciEnrollmentAcceptStage(t, store, e, candidate, b, "service-session-original-key-renewal-"+review)
	rootBlock, _ := pem.Decode([]byte(material["fixture-root.key"]))
	rootKey, err := x509.ParseECPrivateKey(rootBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	leafBlock, _ := pem.Decode([]byte(material["tls.key"]))
	leafKey, err := x509.ParseECPrivateKey(leafBlock.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	root, err := parseFirstCertificatePEM([]byte(material["ca.crt"]))
	if err != nil {
		t.Fatal(err)
	}
	expiry := now.Add(time.Hour).Truncate(time.Second)
	leafDER, err := x509.CreateCertificate(rand.Reader, &x509.Certificate{SerialNumber: big.NewInt(3), Subject: pkix.Name{CommonName: "service:deployment-bootstrap"}, NotBefore: now.Add(-time.Minute), NotAfter: expiry, KeyUsage: x509.KeyUsageDigitalSignature}, root, &leafKey.PublicKey, rootKey)
	if err != nil {
		t.Fatal(err)
	}
	cert := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER})) + material["ca.crt"]
	next := old
	next.Session = renewed.SessionID
	next.Expires = expiry
	next.Fingerprint = environmentPreflightBytesSHA256(leafDER)
	observedOld := old
	observedOld.Status = "expired"
	observedOld.OtherActive = 1
	asObject := func(v any) deploymentplan.Object {
		t.Helper()
		var object deploymentplan.Object
		if err := json.Unmarshal(mustProviderRecoveryJSON(v), &object); err != nil {
			t.Fatal(err)
		}
		return object
	}
	objects := map[string]deploymentplan.Object{}
	for id, v := range b.objects {
		objects[id] = deploymentplan.Clone(v)
	}
	objects["__registry_by_session__"] = deploymentplan.Object{intent.SessionID: asObject(observedOld), renewed.SessionID: asObject(next)}
	objects["__custody__"] = deploymentplan.Object{"/run/pki-bootstrap/bootstrap.key": material["tls.key"], "/run/pki-bootstrap/bootstrap.crt": material["tls.crt"], "/run/pki-bootstrap/bootstrap.csr": material["bootstrap.csr"], certPath: cert}
	published := service
	published.BootstrapSessionID = renewed.SessionID
	published.BootstrapCredentialSecret = "service-bootstrap-" + renewed.SessionID
	published.CredentialUIDs = map[string]string{}
	for i, ns := range intent.Namespaces {
		uid := []string{"66666666-6666-4666-8666-666666666666", "77777777-7777-4777-8777-777777777777", "88888888-8888-4888-8888-888888888888"}[i]
		published.CredentialUIDs[ns] = uid
		d := environmentCreateSecret(ns, published.BootstrapCredentialSecret, map[string]string{"tls.key": material["tls.key"], "tls.crt": cert, "ca.crt": material["ca.crt"]})
		d.Object["immutable"] = true
		deploymentplan.Map(d.Object["metadata"])["uid"] = uid
		d.Object["data"] = deploymentplan.Object{"tls.key": base64.StdEncoding.EncodeToString([]byte(material["tls.key"])), "tls.crt": base64.StdEncoding.EncodeToString([]byte(cert)), "ca.crt": base64.StdEncoding.EncodeToString([]byte(material["ca.crt"]))}
		delete(d.Object, "stringData")
		objects[deploymentplan.ID(d.Object)] = d.Object
	}
	published.CleanupBinding = environmentBootstrapCleanupBinding{Version: 1, Environment: "dev", SessionID: renewed.SessionID, NamespaceUID: "55555555-5555-4555-8555-555555555555", SecretUID: published.CredentialUIDs[intent.Namespaces[0]], PolicySHA256: strings.Repeat("c", 64), KubernetesCA: base64.StdEncoding.EncodeToString([]byte(material["ca.crt"]))}
	backend, log := ciEnrollmentKubectl(t, store, objects)
	cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": e.Journal.Stack}}
	before := deploymentplan.Digest(e.Journal)
	if err := observePartialSessionSuccessor(t.Context(), cfg, store, e, backend, q, review, r); err != nil {
		t.Fatal("exact same-key native successor rejected", err)
	}
	if deploymentplan.Digest(e.Journal) != before {
		t.Fatal("successor observation rewrote original history")
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: backend, ServiceSessionRecovery: r}
	if err := completePartialSessionAdmission(t.Context(), cfg, store, runtime, published); err != nil {
		t.Fatal("qualified successor completion rejected", err)
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "partial-session-renewal-"+review)
	if entry == nil || entry.State != "completed" || entry.CompletedAt.IsZero() {
		t.Fatal("completion did not retain its original admission")
	}
	completed := deploymentplan.Digest(e.Journal)
	if err := completePartialSessionAdmission(t.Context(), cfg, store, runtime, published); err != nil || deploymentplan.Digest(e.Journal) != completed {
		t.Fatal("retry replaced the original completion", err)
	}
	path := filepath.Join(filepath.Dir(log), "objects.json")
	for _, mode := range []string{"candidate-owner", "competing-session", "original-key", "successor-certificate", "credential-owner"} {
		t.Run(mode, func(t *testing.T) {
			var changed map[string]deploymentplan.Object
			if err := json.Unmarshal(mustProviderRecoveryJSON(objects), &changed); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "candidate-owner":
				deploymentplan.Map(changed[candidate.Resources[0].ID]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "competing-session":
				deploymentplan.Map(changed["__registry_by_session__"][intent.SessionID])["other_active"] = 2
			case "original-key":
				changed["__custody__"]["/run/pki-bootstrap/bootstrap.key"] = "replacement-key"
			case "successor-certificate":
				changed["__custody__"][certPath] = "foreign-certificate"
			case "credential-owner":
				deploymentplan.Map(changed["Secret/"+intent.Namespaces[1]+"/"+published.BootstrapCredentialSecret]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			}
			if err := os.WriteFile(path, mustProviderRecoveryJSON(changed), 0600); err != nil {
				t.Fatal(err)
			}
			if mode == "credential-owner" {
				ciEnrollmentBlock(t, completePartialSessionAdmission(t.Context(), cfg, store, runtime, published), "deployment.bootstrap.pki")
			} else {
				ciEnrollmentBlock(t, observePartialSessionSuccessor(t.Context(), cfg, store, e, backend, q, review, r), "deployment.create.recovery")
			}
			if deploymentplan.Digest(e.Journal) != completed {
				t.Fatal("failed native evidence replaced completion or original identity")
			}
		})
	}
}

func TestCIEnrollmentAuthenticatedIngressStopsAtUnavailableOriginalProvider(t *testing.T) {
	kubeconfig := mustProviderRecoveryJSON(frozenKubernetesFixture(t))
	workspace := writeDeploymentFixture(t, "dev", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "dev", filepath.Join(workspace, "cloud_env/dev"))
	if err != nil {
		t.Fatal(err)
	}
	cfg.Values["DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS"] = "60"
	cfg, store, e, _, plan := providerExecutionContractFixture(t, providerExecutionContractInputs{cfg: cfg, operator: map[string]string{"ISOLATED_OPERATOR": "original"}, kubeconfig: kubeconfig})
	e, _ = providerExecutionContractFork(t, e, plan, "")
	d := deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": e.Journal.Stack + "-video-cloud", "name": "original-accepted-enrollment"}, "data": deploymentplan.Object{"owner": "original"}}}
	child, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest("original-release"), BindingsSHA256: environmentCreateStageBindingsSHA("service-issuer-server-enrollment", []deploymentplan.Definition{d})}, []deploymentplan.Definition{d}, nil)
	if err != nil {
		t.Fatal(err)
	}
	b := freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}}}
	ciEnrollmentAcceptStage(t, store, e, child, b, "service-issuer-server-enrollment")
	_, calls := ciEnrollmentKubectl(t, store, b.objects)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	before := createBootstrapStateSnapshot(t, store.Root)
	report := &unifiedDeploymentReport{}
	handled, err := tryUnifiedIssuerEnrollment(t.Context(), cfg, unifiedDeploymentOptions{Operation: "create", Action: "preflight"}, report, io.Discard, io.Discard)
	if !handled || err == nil || len(report.Gates) != 1 || report.Gates[0].ID != "deployment.create.recovery" || report.Gates[0].Status != "BLOCKED" || !strings.Contains(report.Gates[0].Cause, "recorded original provider resource is unavailable") {
		t.Fatalf("unavailable provider bypassed original qualification: handled=%v err=%v gates=%+v", handled, err, report.Gates)
	}
	if raw, err := os.ReadFile(calls); err == nil && len(raw) != 0 {
		t.Fatal("failed provider qualification reached Kubernetes mutation or inspection")
	}
	assertRecoveryContractReadOnly(t, store, before)
	reopened, _, err := loadActiveTransportExecution(cfg, store, e.Journal.PlanSHA256)
	if err != nil {
		t.Fatal("failed provider qualification consumed stopped owner", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCIEnrollmentLeaseExecutionPersistsIntentAndRefusesReplay(t *testing.T) {
	store, e, _, _ := freshStageFixture(t)
	r, objects := ciEnrollmentLeaseFixture(t)
	backend, log := ciEnrollmentKubectl(t, store, objects)
	before := objects[r.journal.Failed]
	after := deploymentplan.Clone(before)
	deploymentplan.Map(after["metadata"])["labels"] = deploymentplan.Object{"app.kubernetes.io/managed-by": "rtk-cloud", "rtk.realtek.com/stack": "video-cloud-dev"}
	patch := issuerLeasePatchPlan{Resource: r.journal.Failed, BeforeSHA: deploymentplan.Digest(before), AfterSHA: deploymentplan.Digest(deploymentplan.Projection(after)), Patch: issuerLeaseLabelPatch(before, deploymentplan.Map(deploymentplan.Map(after["metadata"])["labels"]))}
	review := issuerLeaseRepairReview{Parent: e.Journal.PlanSHA256, ParentJournal: deploymentplan.Digest(e.Journal), Child: r.plan.SHA256(), ChildJournal: deploymentplan.Digest(r.journal), Kubeconfig: environmentPreflightBytesSHA256(backend.KubeconfigContent), Checker: strings.Repeat("a", 64), Patch: patch}
	directory := filepath.Join("deployment/create/issuer-lease-repair", e.Journal.PlanSHA256, deploymentplan.Digest(review))
	original := deploymentplan.Digest(e.Journal)
	if err := executeIssuerLeaseRepair(t.Context(), store, e, backend, r.plan, review, directory); err != nil {
		t.Fatal("exact reviewed Lease correction rejected", err)
	}
	var completed struct {
		Review      string    `json:"review_sha256"`
		Lease       string    `json:"lease_sha256"`
		CompletedAt time.Time `json:"completed_at"`
	}
	if readExecutionJSON(filepath.Join(store.Root, directory, "completed.json"), &completed) != nil || completed.Review != deploymentplan.Digest(review) || completed.Lease != patch.AfterSHA || completed.CompletedAt.IsZero() {
		t.Fatal("repair completion did not bind exact review and corrected Lease")
	}
	var intent struct {
		Review     string                    `json:"review_sha256"`
		Owner      deploymentplan.LeaseOwner `json:"execution_lease"`
		AdmittedAt time.Time                 `json:"admitted_at"`
	}
	if readExecutionJSON(filepath.Join(store.Root, directory, "intent.json"), &intent) != nil || intent.Review != completed.Review || intent.Owner.UID != "99999999-9999-4999-8999-999999999999" || !strings.HasPrefix(intent.Owner.Holder, r.plan.SHA256()+":") || intent.AdmittedAt.IsZero() || completed.CompletedAt.Before(intent.AdmittedAt) {
		t.Fatal("intent lost exact original execution lease")
	}
	actual, err := backend.Read(t.Context(), patch.Resource)
	if err != nil || deploymentplan.Digest(deploymentplan.Projection(actual)) != patch.AfterSHA {
		t.Fatal("native corrected Lease does not match approved result", err)
	}
	held, err := backend.Read(t.Context(), "Lease/"+r.plan.Stack+"-video-cloud/rtk-deployment-execution")
	if err != nil || held != nil {
		t.Fatal("owned execution lease was not released", err)
	}
	intentBefore, _ := os.ReadFile(filepath.Join(store.Root, directory, "intent.json"))
	completionBefore, _ := os.ReadFile(filepath.Join(store.Root, directory, "completed.json"))
	ciEnrollmentBlock(t, executeIssuerLeaseRepair(t.Context(), store, e, backend, r.plan, review, directory), "deployment.create.recovery")
	intentAfter, _ := os.ReadFile(filepath.Join(store.Root, directory, "intent.json"))
	completionAfter, _ := os.ReadFile(filepath.Join(store.Root, directory, "completed.json"))
	if !bytes.Equal(intentBefore, intentAfter) || !bytes.Equal(completionBefore, completionAfter) || deploymentplan.Digest(e.Journal) != original {
		t.Fatal("replay replaced original intent, completion or parent history")
	}
	raw, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	writes := 0
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		var args []string
		if json.Unmarshal([]byte(line), &args) != nil {
			t.Fatal("invalid native call log")
		}
		if args[0] == "patch" && !slices.Contains(args, "--dry-run=server") {
			writes++
		}
	}
	if writes != 1 {
		t.Fatalf("exact original Lease patched %d times, want once", writes)
	}
}

func TestCIEnrollmentSecondSessionReadFailuresCannotAcquireAuthority(t *testing.T) {
	// Keep both fixture workspace and store beneath this test's temporary root.
	t.Setenv("TMPDIR", filepath.Dir(t.TempDir()))
	f := newServiceSessionNativeMintFixture(t)
	original := deploymentplan.Digest(f.e.Journal)
	base := nativeSecondSessionReads(f.cfg, f.store, f.e, f.native)
	predecessor := f.q.Renewal.PreviousReview
	sentinel := errors.New("isolated original observation unavailable")
	for _, boundary := range []string{"custody-owner", "predecessor-job", "source-job", "original-session", "predecessor-session", "native-client-inventory", "original-custody", "predecessor-material", "custody-permissions", "credential-owners", "managed-openbao-trust", "live-inventory"} {
		t.Run(boundary, func(t *testing.T) {
			reads := base
			switch boundary {
			case "custody-owner", "predecessor-job", "source-job", "credential-owners":
				selected := map[string]string{"custody-owner": f.q.Renewal.PreviousCustodyID, "predecessor-job": f.q.Renewal.PreviousJobID, "source-job": f.q.Renewal.SourceJobID, "credential-owners": "Secret/" + f.q.Renewal.Intent.Namespaces[0] + "/service-bootstrap-" + f.q.Renewal.Intent.SessionID}[boundary]
				reads.read = func(ctx context.Context, id string) (deploymentplan.Object, error) {
					if id == selected {
						return nil, sentinel
					}
					return base.read(ctx, id)
				}
			case "original-session", "predecessor-session":
				selected := f.q.Renewal.OriginalState.Session
				if boundary == "predecessor-session" {
					selected = f.q.Renewal.Intent.SessionID
				}
				reads.state = func(ctx context.Context, sid string) (managedSessionState, error) {
					if sid == selected {
						return managedSessionState{}, sentinel
					}
					return base.state(ctx, sid)
				}
			case "native-client-inventory":
				reads.clients = func(context.Context) ([]managedSessionClient, error) { return nil, sentinel }
			case "original-custody":
				reads.custody = func(context.Context, *createServiceSessionRecovery, *createManagedSessionRecovery) error {
					return sentinel
				}
			case "predecessor-material", "custody-permissions":
				reads.command = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
					if boundary == "predecessor-material" && args[len(args)-1] == "/run/pki-bootstrap/bootstrap.key" || boundary == "custody-permissions" && args[len(args)-1] == "/run/pki-bootstrap" {
						return nil, sentinel
					}
					return base.command(ctx, input, args...)
				}
			case "managed-openbao-trust":
				reads.trust = func(context.Context, environmentCreateRegisteredIssuer) (string, error) { return "", sentinel }
			case "live-inventory":
				reads.inventory = func(context.Context, string) ([]deploymentplan.Object, error) { return nil, sentinel }
			}
			candidate, err := prepareSecondSessionCandidateWithReads(t.Context(), f.cfg, f.store, f.e, reads, f.parent, f.service, f.service.Root, time.Now().UTC(), predecessor)
			ciEnrollmentBlock(t, err, "deployment.create.recovery")
			if candidate != nil || !strings.Contains(err.Error(), "boundary="+boundary) || deploymentplan.Digest(f.e.Journal) != original {
				t.Fatalf("rejected %s observation gained authority or reported another boundary: %v", boundary, err)
			}
		})
	}
	for _, mode := range []string{"before-admission", "deadline-exhausted", "multiple-terminal-proofs", "previous-job-owner", "source-job"} {
		t.Run("observer/"+mode, func(t *testing.T) {
			reads := base
			now := time.Now().UTC()
			var terminal []*freshProductTerminalObservation
			want := ""
			switch mode {
			case "before-admission":
				now = f.q.QualifiedAt.Add(-time.Second)
				reads.clock = func() time.Time { return now }
				want = "admission"
			case "deadline-exhausted":
				now = f.q.ExecutionDeadline
				reads.clock = func() time.Time { return now }
				want = "execution-deadline"
			case "multiple-terminal-proofs":
				terminal = []*freshProductTerminalObservation{nil, nil}
				want = "terminal-observation"
			case "previous-job-owner", "source-job":
				selected := f.q.Renewal.PreviousJobID
				want = "previous-job-owner"
				if mode == "source-job" {
					selected = f.q.Renewal.SourceJobID
					want = "source-job"
				}
				reads.read = func(ctx context.Context, id string) (deploymentplan.Object, error) {
					if id == selected {
						return nil, sentinel
					}
					return base.read(ctx, id)
				}
			}
			err := observeSecondSessionSuccessorWithReads(t.Context(), f.cfg, f.store, f.e, reads, f.parent, f.service, f.service.Root, f.review, now, terminal...)
			ciEnrollmentBlock(t, err, "deployment.create.recovery")
			if !strings.Contains(err.Error(), "boundary="+want) || deploymentplan.Digest(f.e.Journal) != original {
				t.Fatalf("observer lost %s boundary or changed history: %v", want, err)
			}
		})
	}
	report := &unifiedDeploymentReport{}
	handled, err := tryUnifiedServiceSessionContinuation(t.Context(), f.cfg, unifiedDeploymentOptions{Operation: "create", Action: "preflight"}, report, f.store, f.e, f.native, f.parent, nil, environmentCreateJournalEntry{}, issuerEnrollmentContinuationProof{PartialSession: &partialSessionContinuationProof{}}, io.Discard, io.Discard)
	if !handled {
		t.Fatal("admitted second-session routing silently fell through")
	}
	ciEnrollmentBlock(t, err, "deployment.create.recovery")
	if !strings.Contains(err.Error(), "original q2") || report.Scope != "" || deploymentplan.Digest(f.e.Journal) != original {
		t.Fatal("unproven q2 acquired a scoped continuation", err)
	}
	for _, o := range []unifiedDeploymentOptions{{Operation: "upgrade", Action: "create"}, {Operation: "create", Action: "create", PlanSHA256: f.review}, {Operation: "create", Action: "create", PlanSHA256: "foreign", Confirm: f.parent.Stack}} {
		ciEnrollmentBlock(t, dispatchServiceSessionBatches(t.Context(), f.cfg, o, &unifiedDeploymentReport{}, f.store, f.e, &f.parent, f.native, f.runtime, f.service, f.service.Root, f.q.ExecutionDeadline, func(context.Context) error { t.Fatal("invalid execution reached OpenBao readiness"); return nil }, false, io.Discard, io.Discard), "deployment.create.recovery")
	}
	if deploymentplan.Digest(f.e.Journal) != original {
		t.Fatal("invalid scoped execution rewrote original admissions")
	}
}

func TestCIEnrollmentExpiredPrivateRepairRequiresUnambiguousOriginalAdmissions(t *testing.T) {
	store, e, _, _ := freshStageFixture(t)
	parent := &environmentCreatePlan{Stack: e.Journal.Stack}
	partial := issuerEnrollmentContinuationProof{PartialSession: &partialSessionContinuationProof{}}
	run := func(execution *environmentCreateExecution, admitted bool) (bool, error) {
		return tryExpiredSessionPrivateRepair(t.Context(), deploymentConfig{}, store, execution, environmentKubernetesBackend{}, parent, environmentCreateJournalEntry{}, partial, "", admitted, issuerEnrollmentContinuationProof{}, "", false, unifiedDeploymentOptions{}, &unifiedDeploymentReport{}, io.Discard, io.Discard)
	}
	handled, err := run(e, false)
	if handled || err != nil {
		t.Fatal("unadmitted operation was selected", err)
	}
	for _, mode := range []string{"no-original-forward", "only-application-forward", "completed-mount", "duplicate-application", "duplicate-initializer", "missing-bundles"} {
		t.Run(mode, func(t *testing.T) {
			copy := *e
			copy.Journal.Entries = slices.Clone(e.Journal.Entries)
			a := environmentCreateJournalEntry{Key: "failed-stage-forward-" + strings.Repeat("a", 64), Phase: "failed-stage-forward"}
			b := environmentCreateJournalEntry{Key: "initializer-forward-" + strings.Repeat("b", 64), Phase: "initializer-forward"}
			switch mode {
			case "only-application-forward":
				copy.Journal.Entries = append(copy.Journal.Entries, a)
			case "completed-mount":
				copy.Journal.Entries = append(copy.Journal.Entries, environmentCreateJournalEntry{Phase: "initializer-mount-forward", State: "completed"})
			case "duplicate-application":
				copy.Journal.Entries = append(copy.Journal.Entries, a, a)
			case "duplicate-initializer":
				copy.Journal.Entries = append(copy.Journal.Entries, b, b)
			case "missing-bundles":
				copy.Journal.Entries = append(copy.Journal.Entries, a, b)
			}
			before := deploymentplan.Digest(copy.Journal)
			handled, err := run(&copy, true)
			if mode == "duplicate-application" || mode == "duplicate-initializer" || mode == "missing-bundles" {
				if !handled {
					t.Fatal("invalid admitted repair silently fell through")
				}
				ciEnrollmentBlock(t, err, "deployment.create.recovery")
			} else if handled || err != nil {
				t.Fatal("unrelated repair selected", err)
			}
			if deploymentplan.Digest(copy.Journal) != before {
				t.Fatal("scoped repair selection rewrote original evidence")
			}
		})
	}
}

func TestCIEnrollmentCoreContinuationCannotRestartItsOriginalDeadline(t *testing.T) {
	cfg, store, e, _, parent := providerExecutionContractFixture(t)
	e, _ = providerExecutionContractFork(t, e, parent, "")
	backend := freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}}}
	plans := map[string]*deploymentplan.Plan{}
	for _, stage := range append([]string{"service-issuer-server-enrollment"}, []string{"service-managed-enrollment-issuer", "service-core-enrollment", "service-core-initial-crls"}...) {
		d := deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": e.Journal.Stack + "-video-cloud", "name": stage}, "data": deploymentplan.Object{"authority": "original"}}}
		child, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest("original-release"), BindingsSHA256: environmentCreateStageBindingsSHA(stage, []deploymentplan.Definition{d})}, []deploymentplan.Definition{d}, nil)
		if err != nil {
			t.Fatal(err)
		}
		plans[stage] = child
	}
	ciEnrollmentAcceptStage(t, store, e, plans["service-issuer-server-enrollment"], backend, "service-issuer-server-enrollment")
	now := time.Now().UTC()
	q := issuerEnrollmentContinuationProof{Parent: e.Journal.PlanSHA256, ParentJournal: deploymentplan.Digest(e.Journal), ParentOwner: deploymentplan.Digest(e.owner), FirstStage: plans["service-managed-enrollment-issuer"].SHA256(), Release: plans["service-managed-enrollment-issuer"].ReleaseSHA256}
	review := deploymentplan.Digest(q)
	qualified := q
	qualified.QualifiedAt = now
	qualified.ValidUntil = now.Add(time.Hour)
	if err := store.write(filepath.Join("deployment/create/issuer-enrollment-continuation", review, "qualification.json"), mustProviderRecoveryJSON(qualified), false); err != nil {
		t.Fatal(err)
	}
	if err := e.BeginEntry(issuerEnrollmentContinuationPrefix+review, "continuation", mustProviderRecoveryJSON(q), now); err != nil {
		t.Fatal(err)
	}
	for _, stage := range []string{"service-managed-enrollment-issuer", "service-core-enrollment", "service-core-initial-crls"} {
		ciEnrollmentAcceptStage(t, store, e, plans[stage], backend, stage)
	}
	if profile, _ := createRecoveryCheckpointProfile(e.Journal); profile != acceptedIssuerCoreCRLsProfile {
		t.Fatalf("original core checkpoint did not retain its continuation: %s", profile)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	before := createBootstrapStateSnapshot(t, store.Root)
	report := &unifiedDeploymentReport{}
	handled, err := tryUnifiedIssuerEnrollment(t.Context(), cfg, unifiedDeploymentOptions{Operation: "create", Action: "create", Timeout: time.Nanosecond}, report, io.Discard, io.Discard)
	if !handled || err == nil || len(report.Gates) != 1 || report.Gates[0].ID != "deployment.create.recovery" || !strings.Contains(report.Gates[0].Cause, "original continuation execution timeout is exhausted") {
		t.Fatalf("expired original continuation acquired a fresh timeout: %v %v %+v", handled, err, report.Gates)
	}
	assertRecoveryContractReadOnly(t, store, before)
	loaded, _, err := loadActiveTransportExecution(cfg, store, e.Journal.PlanSHA256)
	if err != nil {
		t.Fatal("expired continuation consumed held original owner", err)
	}
	if err := loaded.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCIEnrollmentLeaseCLIRefusesAChangedSelectedConfiguration(t *testing.T) {
	cfg, store, e, _, plan := providerExecutionContractFixture(t)
	e, _ = providerExecutionContractFork(t, e, plan, "")
	parent := e.Journal.PlanSHA256
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	before := createBootstrapStateSnapshot(t, store.Root)
	err := runEnvironmentCreateIssuerLeaseRepair([]string{"--environment", "dev", "--parent-plan-sha256", parent})
	ciEnrollmentBlock(t, err, "deployment.create.recovery")
	if !strings.Contains(err.Error(), "configuration") {
		t.Fatalf("repair did not revalidate maintained configuration before execution: %v", err)
	}
	assertRecoveryContractReadOnly(t, store, before)
	reopened, _, err := loadActiveTransportExecution(cfg, store, parent)
	if err != nil {
		t.Fatal("configuration mismatch consumed original owner", err)
	}
	if err := reopened.Close(); err != nil {
		t.Fatal(err)
	}
}
