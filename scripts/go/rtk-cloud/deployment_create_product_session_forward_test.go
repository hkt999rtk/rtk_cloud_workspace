package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"testing"
	"time"
)

type productForwardFixture struct {
	*productProtocolFixture
	program             createNormalTailProgram
	old                 *deploymentplan.Plan
	oldJournal          deploymentplan.Journal
	service             environmentCreateServiceCeremony
	pod                 deploymentplan.Object
	cert                string
	bootstrapRow        managedSessionClient
	minted, noMintReads int
	failure             string
	nativeOverride      func([]string) ([]byte, int, bool)
	snapshots           []environmentCreateJournal
	tokenFailure        bool
	tokenPod            deploymentplan.Object
	tokenJobName        string
}
type productForwardBackend struct {
	*productProtocolBackend
	f *productForwardFixture
}

func (b *productForwardBackend) Write(ctx context.Context, a string, o deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	actual, err := b.productProtocolBackend.Write(ctx, a, o, dry)
	if err == nil && !dry && a == "retire" {
		delete(b.objects, deploymentplan.ID(o))
	}
	return actual, err
}
func (b *productForwardBackend) Wait(ctx context.Context, r deploymentplan.Resource) error {
	if r.Action == "retire" {
		return nil
	}
	if r.Desired["kind"] == "Job" {
		o := b.objects[r.ID]
		if len(deploymentplan.String(deploymentplan.Map(r.Desired["metadata"])["name"])) > 34 {
			helper := "Deployment/" + b.f.p.Stack + "-video-cloud/pki-product-custody-" + productIdentityScope(b.f.l)[:12] + "-trust"
			if b.objects[helper] != nil {
				return errors.New("RWO custody must be retired before native forward")
			}
			init := certIssuerObjectList(environmentPKIPod(r.Desired)["initContainers"])
			command := deploymentplan.Map(init[len(init)-1])["command"].([]any)[2].(string)
			if !strings.Contains(command, environmentPreflightBytesSHA256([]byte(b.f.bootstrap["bootstrap.key"]))) || !strings.Contains(command, environmentPreflightBytesSHA256([]byte(b.f.bootstrap["bootstrap.csr"]))) {
				return errors.New("native immutable key/CSR guard refused before mint")
			}

			if b.f.tokenFailure {
				b.f.tokenFailure = false
				o["status"] = deploymentplan.Object{"failed": float64(1), "conditions": []any{deploymentplan.Object{"type": "Failed", "status": "True", "reason": "BackoffLimitExceeded"}}}
				uid := deploymentplan.Map(o["metadata"])["uid"]
				b.f.tokenJobName = deploymentplan.Map(o["metadata"])["name"].(string)
				b.f.tokenPod = deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": b.f.tokenJobName + "-failure", "namespace": deploymentplan.Map(o["metadata"])["namespace"], "uid": "ecececec-ecec-4cec-8cec-ecececececec", "ownerReferences": []any{deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "name": b.f.tokenJobName, "uid": uid, "controller": true}}}, "spec": deploymentplan.Clone(environmentPKIPod(r.Desired)), "status": deploymentplan.Object{"phase": "Failed", "containerStatuses": []any{deploymentplan.Object{"name": "pki-service-bootstrap", "restartCount": float64(0), "state": deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": float64(1), "reason": "Error"}}}}}}
				var statuses []any
				for _, raw := range certIssuerObjectList(environmentPKIPod(r.Desired)["initContainers"]) {
					statuses = append(statuses, deploymentplan.Object{"name": deploymentplan.Map(raw)["name"], "restartCount": float64(0), "state": deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": float64(0), "reason": "Completed"}}})
				}
				deploymentplan.Map(b.f.tokenPod["status"])["initContainerStatuses"] = statuses
				return errors.New("isolated native projected token permission failure")
			}
			if deploymentplan.Map(o["status"])["succeeded"] != float64(1) {
				b.f.minted++
				b.f.bootstrap["bootstrap.crt"] = b.f.cert
				b.f.rows["service:deployment-bootstrap"] = b.f.bootstrapRow
				c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(r.Desired)["containers"])[0])
				b.f.session = mqttFoundationEnv(c, "PKI_BOOTSTRAP_SESSION_ID")
				b.f.expires = time.Now().UTC().Add(time.Hour)
			}
			o["status"] = deploymentplan.Object{"succeeded": float64(1), "conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
		} else {
			o["status"] = deploymentplan.Object{"failed": float64(1), "conditions": []any{deploymentplan.Object{"type": "Failed", "status": "True", "reason": "BackoffLimitExceeded"}}}
			uid := deploymentplan.Map(o["metadata"])["uid"]
			b.f.pod = deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": deploymentplan.Map(o["metadata"])["name"].(string) + "-failure", "namespace": deploymentplan.Map(o["metadata"])["namespace"], "uid": "dededede-dede-4ede-8ede-dededededede", "ownerReferences": []any{deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "name": deploymentplan.Map(o["metadata"])["name"], "uid": uid, "controller": true}}}, "spec": deploymentplan.Clone(environmentPKIPod(r.Desired)), "status": deploymentplan.Object{"phase": "Failed", "containerStatuses": []any{deploymentplan.Object{"name": "pki-service-bootstrap", "restartCount": float64(0), "state": deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": float64(1), "reason": "Error"}}}}}}
			var init []any
			for _, raw := range certIssuerObjectList(environmentPKIPod(r.Desired)["initContainers"]) {
				init = append(init, deploymentplan.Object{"name": deploymentplan.Map(raw)["name"], "restartCount": float64(0), "state": deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": float64(0), "reason": "Completed"}}})
			}
			deploymentplan.Map(b.f.pod["status"])["initContainerStatuses"] = init
			return errors.New("isolated native terminal TLS Job")
		}
	}
	return nil
}
func (f *productForwardFixture) apply(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
	if f.failure == "before-"+stage {
		f.failure = ""
		return errors.New("isolated before stage")
	}
	full := "product-identities-" + productIdentityScope(f.l)[:12] + "-" + stage
	p := f.plans[full]
	if p == nil {
		var live []deploymentplan.Object
		for _, o := range f.b.objects {
			live = append(live, o)
		}
		var err error
		p, err = deploymentplan.Compile(deploymentplan.Inputs{Operation: "scoped", Environment: "dev", Stack: f.p.Stack, ConfigurationSHA256: deploymentplan.Digest(f.cfg), ReleaseSHA256: deploymentplan.Digest(f.program.Release), BindingsSHA256: environmentCreateStageBindingsSHA(full, defs), CreateParentSHA256: f.e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(f.h.Pins)}, defs, live)
		if err != nil {
			return err
		}
		f.plans[full] = p
	}
	if err := saveCreateNormalTailStagePlan(ctx, f.store, f.e, full, p); err != nil {
		return err
	}
	if f.failure == "unknown-"+stage {
		f.b.failure = "unknown-write"
		f.b.failed = false
		f.failure = ""
	}
	backend := &productForwardBackend{f.b, f}
	err := executeCreateNormalTailStageBackend(ctx, f.store, f.h, full, p, backend, func(ctx context.Context, p *deploymentplan.Plan) error {
		return p.AdmitResumable(ctx, backend, f.store.Root)
	}, func(ctx context.Context, _ *deploymentplan.Plan) error { return f.e.Check(ctx) }, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, backend) }, io.Discard, func(context.Context) error { return nil })
	var snap environmentCreateJournal
	_ = decodeEnvironmentJSON(mustProviderRecoveryJSON(f.e.Journal), &snap)
	f.snapshots = append(f.snapshots, snap)
	if err == nil && f.failure == "after-"+stage {
		f.failure = ""
		return errors.New("isolated after stage")
	}
	return err
}
func newProductForwardFixture(t *testing.T, deferOriginal ...bool) *productForwardFixture {
	t.Helper()
	f := &productForwardFixture{productProtocolFixture: newProductProtocolFixture(t, false)}
	ns := f.p.Stack + "-video-cloud"
	trustID := "ConfigMap/" + ns + "/pki-create-openbao-tls-consumer-trust"
	uid := "abababab-abab-4bab-8bab-abababababab"
	trust := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-create-openbao-tls-consumer-trust", "namespace": ns, "uid": uid}, "data": deploymentplan.Object{"roots.pem": f.l.Root.Certificate}}
	// The frozen old Program retained the trust owner, not a fabricated copy of
	// its future public data. Its native OpenBao sidecar pins the actual Root.
	f.l.SourceDefinitions = append(f.l.SourceDefinitions, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-create-openbao-tls-consumer-trust", "namespace": ns}}, Phase: "configuration", Retain: true}, deploymentplan.Definition{Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"name": "openbao", "namespace": f.p.Stack + "-secrets"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "openbao-pki", "env": environmentCreateEnv(map[string]string{"OPENBAO_PKI_HOST_ROOT_SHA256": f.l.Root.RootFingerprint})}}}}}}, Phase: "workload"})
	f.l.SourceOwners[trustID] = uid
	f.b.objects[trustID] = trust
	old := deploymentplan.Clone(trust)
	deploymentplan.Map(old["metadata"])["name"] = "pki-openbao-transport-ca"
	old["data"] = deploymentplan.Object{"ca.crt": f.l.Intermediate.Certificate}
	deploymentplan.Map(old["metadata"])["uid"] = "acacacac-acac-4cac-8cac-acacacacacac"
	f.l.SourceOwners[deploymentplan.ID(old)] = "acacacac-acac-4cac-8cac-acacacacacac"
	f.b.objects[deploymentplan.ID(old)] = old
	f.runtime.ProductIdentityScope = productIdentityScope(f.l)
	f.runtime.PKIClient.mutationScope = f.runtime.ProductIdentityScope
	f.issue("service:deployment-bootstrap")
	f.cert = f.bootstrap["bootstrap.crt"]
	f.bootstrapRow = f.rows["service:deployment-bootstrap"]
	delete(f.bootstrap, "bootstrap.crt")
	delete(f.rows, "service:deployment-bootstrap")
	f.service = environmentCreateServiceCeremony{Root: f.l.Root, Intermediate: f.l.Intermediate, BootstrapSessionID: "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", BootstrapClaim: "pki-product-bootstrap-" + productIdentityScope(f.l)[:12], BootstrapCredentialNamespace: ns}
	var subjects []string
	for _, o := range f.l.Outputs {
		subjects = append(subjects, o.Subject)
	}
	slices.Sort(subjects)
	f.service.CustodyReader = "pki-product-custody-" + productIdentityScope(f.l)[:12]
	intent := deploymentplan.Object{"session_id": f.service.BootstrapSessionID, "claim": f.service.BootstrapClaim, "subjects": subjects, "issuer_id": f.service.Intermediate.ID, "root_sha256": f.l.Root.RootFingerprint, "credential_namespaces": []string{ns}}
	if _, err := writeProductArtifact(f.store, filepath.Join(filepath.Dir(productSessionForwardPath(f.l)), "session-intent.json"), intent); err != nil {
		t.Fatal(err)
	}
	digest := deploymentplan.Digest("frozen-forward-fixture")
	f.program = createNormalTailProgram{Schema: createNormalTailProgramSchema, ParentSHA256: f.e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(f.e.owner), HandoffSHA256: deploymentplan.Digest(f.h.Pins), ConfigurationSHA256: digest, MaintainedConfigSHA256: deploymentplan.Digest(f.cfg), ClusterSHA256: digest, IdentitySHA256: digest, Release: environmentRelease{Environment: "dev", Stack: f.p.Stack}, Bindings: environmentIdentityBindings{Version: 1, Environment: "dev", Stack: f.p.Stack, Owners: f.l.SourceOwners}, Products: f.l, Definitions: f.l.SourceDefinitions}
	checks := map[string]func(context.Context) error{}
	for _, id := range deploymentplan.RequiredGateIDs(false) {
		checks[id] = f.e.Check
	}
	q, err := qualifyCreateNormalTailProgram(t.Context(), &f.program, checks, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	a := createNormalTailProgramArtifact{Program: f.program, Qualification: q, CheckerSHA256: checker, Generation: digest, QualifiedAt: now, ValidUntil: now.Add(time.Hour)}
	if err = saveCreateNormalTailProgramArtifact(f.store, f.e, a); err != nil {
		t.Fatal(err)
	}
	if err = writeEnvironmentPreflightActive(f.store, environmentPreflightActive{Schema: environmentPreflightActiveSchema, Generation: digest, Environment: "dev", Decision: "GO", PlanSHA256: deploymentplan.Digest(f.program), ProofSHA256: deploymentplan.Digest(a), Artifact: deploymentplan.Digest(f.program) + "/qualification-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err = admitCreateNormalTailProgramWithFence(t.Context(), f.cfg, f.h, a, time.Hour, now, f.e.Check); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		r.Body = io.NopCloser(bytes.NewReader(raw))
		var req struct {
			Args []string `json:"args"`
		}
		_ = json.Unmarshal(raw, &req)
		respond := func(out []byte, code int) {
			_ = json.NewEncoder(w).Encode(deploymentplan.Object{"output": base64.StdEncoding.EncodeToString(out), "code": code})
		}
		if f.nativeOverride != nil {
			if out, code, yes := f.nativeOverride(req.Args); yes {
				respond(out, code)
				return
			}
		}
		joined := strings.Join(req.Args, " ")
		switch {
		case f.tokenPod != nil && strings.Contains(joined, " get pods --selector job-name="+f.tokenJobName):
			respond(mustProviderRecoveryJSON(deploymentplan.Object{"apiVersion": "v1", "kind": "List", "metadata": deploymentplan.Object{"resourceVersion": "2390321"}, "items": []any{f.tokenPod}}), 0)
		case f.tokenPod != nil && strings.Contains(joined, " logs pod/"+f.tokenJobName+"-failure"):
			respond([]byte(productSessionTokenFailure), 0)
		case strings.Contains(joined, " get pods --selector job-name="):
			respond(mustProviderRecoveryJSON(deploymentplan.Object{"apiVersion": "v1", "kind": "List", "metadata": deploymentplan.Object{"resourceVersion": "2390123"}, "items": []any{f.pod}}), 0)
		case strings.Contains(joined, " logs pod/"):
			respond([]byte(productSessionForwardTLSLog(f.old.Resources[2].Desired)), 0)
		case strings.Contains(joined, "sha256sum bootstrap.key bootstrap.csr"):
			absent := !strings.Contains(joined, "test -f bootstrap.crt")
			if absent != (f.bootstrap["bootstrap.crt"] == "") {
				respond(nil, 1)
				return
			}
			respond([]byte("700 10001 10001\n600 10001 10001\n600 10001 10001\n"+environmentPreflightBytesSHA256([]byte(f.bootstrap["bootstrap.key"]))+"  bootstrap.key\n"+environmentPreflightBytesSHA256([]byte(f.bootstrap["bootstrap.csr"]))+"  bootstrap.csr\n"), 0)
		case strings.Contains(joined, "registry-read") && strings.Contains(joined, "'sessions'"):
			f.noMintReads++
			if f.minted != 0 {
				t.Error("no-mint prerequisite rerun after genuine forward mint")
				respond(nil, 1)
				return
			}
			respond([]byte(`{"sessions":0,"deployments":0,"issuances":0}`), 0)
		default:
			f.productProtocolFixture.serveBoundary(w, r)
		}
	}))
	t.Cleanup(server.Close)
	t.Setenv("PRODUCT_PROTOCOL_BOUNDARY", server.URL)
	if len(deferOriginal) > 0 && deferOriginal[0] {
		return f
	}
	defs, err := productStageDefinitions(t.Context(), f.cfg, f.l, f.service, "service-session")
	if err != nil {
		t.Fatal(err)
	}
	if err = f.apply(t.Context(), "service-session", defs); err == nil {
		t.Fatal("original terminal Job did not fail")
	}
	f.old = f.plans["product-identities-"+productIdentityScope(f.l)[:12]+"-service-session"]
	f.oldJournal, _, err = f.old.ReadExecution(f.store.Root)
	if err != nil || validateProductSessionFailedJournal(f.old, f.oldJournal) != nil {
		t.Fatal(err, "invalid genuine original failure")
	}
	return f
}
func TestProductSessionTrustForwardPreservesOriginalAndEverySupportedInterruption(t *testing.T) {
	for _, failure := range []string{"none", "before-service-session-trust-custody", "after-service-session-trust-custody", "before-service-session-trust-forward", "after-service-session-trust-forward", "before-service-session-trust-custody-retire", "after-service-session-trust-custody-retire"} {
		t.Run(failure, func(t *testing.T) {
			f := newProductForwardFixture(t)
			old := deploymentplan.Digest(f.oldJournal)
			key, csr := f.bootstrap["bootstrap.key"], f.bootstrap["bootstrap.csr"]
			f.failure = failure
			handled, err := executeProductSessionTrustForward(t.Context(), f.cfg, f.store, f.runtime, f.l, f.apply)
			if !handled {
				t.Fatal("failed original was not handled")
			}
			if failure != "none" && err == nil {
				t.Fatal("missing requested interruption")
			}
			if failure == "none" && err != nil {
				t.Fatal(err)
			}
			for i := 0; err != nil && i < 2; i++ {
				_, err = executeProductSessionTrustForward(t.Context(), f.cfg, f.store, f.runtime, f.l, f.apply)
			}
			if err != nil {
				t.Fatal("resume", err)
			}
			if _, err = executeProductSessionTrustForward(t.Context(), f.cfg, f.store, f.runtime, f.l, f.apply); err != nil {
				t.Fatal("completed retry", err)
			}
			j, _, err := f.old.ReadExecution(f.store.Root)
			if err != nil || deploymentplan.Digest(j) != old || j.Accepted || f.minted != 1 || f.bootstrap["bootstrap.key"] != key || f.bootstrap["bootstrap.csr"] != csr {
				t.Fatal("original execution/custody changed", err, f.minted)
			}
			source, err := readProductSessionForward(f.store, f.e, f.l)
			if err != nil || source == nil {
				t.Fatal("saved pure source", err)
			}
			for _, r := range f.old.Resources {
				if f.b.writes[r.ID] != 1 {
					t.Fatal("original resource replay", r.ID, f.b.writes[r.ID])
				}
			}
			if f.b.objects["Deployment/"+f.p.Stack+"-video-cloud/pki-product-custody-"+productIdentityScope(f.l)[:12]+"-trust"] != nil {
				t.Fatal("read-only helper retained")
			}
			entry := createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/product-identities-"+productIdentityScope(f.l)[:12]+"-service-session")
			var receipt environmentProductSessionForwardReceipt
			if entry.State != "completed" || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt.SourceSHA256 != deploymentplan.Digest(source) {
				t.Fatal("composite not genuine")
			}
		})
	}
}
func TestProductSessionTrustForwardRefusesNativeUnknownOrChangedStateBeforeForwardWrite(t *testing.T) {
	for _, bad := range []string{"running-original", "foreign-job-uid", "foreign-pod-uid", "non-tls-failure", "key-permissions", "certificate-present", "minted-session", "claim-uid", "sa-uid", "trust-owner"} {
		t.Run(bad, func(t *testing.T) {
			f := newProductForwardFixture(t)
			switch bad {
			case "running-original":
				f.b.objects[f.old.Resources[2].ID]["status"] = deploymentplan.Object{"active": float64(1)}
			case "foreign-job-uid":
				deploymentplan.Map(f.b.objects[f.old.Resources[2].ID]["metadata"])["uid"] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			case "foreign-pod-uid":
				deploymentplan.Map(f.pod["metadata"])["uid"] = "bad"
			case "certificate-present":
				f.bootstrap["bootstrap.crt"] = f.cert
			case "claim-uid":
				deploymentplan.Map(f.b.objects[f.old.Resources[0].ID]["metadata"])["uid"] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			case "sa-uid":
				deploymentplan.Map(f.b.objects[f.old.Resources[1].ID]["metadata"])["uid"] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			case "trust-owner":
				for id, o := range f.b.objects {
					if strings.HasSuffix(id, "/pki-create-openbao-tls-consumer-trust") {
						deploymentplan.Map(o["metadata"])["uid"] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
					}
				}
			}
			f.nativeOverride = func(args []string) ([]byte, int, bool) {
				v := strings.Join(args, " ")
				switch {
				case bad == "non-tls-failure" && strings.Contains(v, " logs pod/"):
					return []byte("sign bootstrap certificate: denied"), 0, true
				case bad == "key-permissions" && strings.Contains(v, "sha256sum bootstrap.key"):
					return []byte("700 10001 10001\n660 10001 10001\n600 10001 10001\n"), 0, true
				case bad == "minted-session" && strings.Contains(v, "'sessions'"):
					return []byte(`{"sessions":1,"deployments":1,"issuances":1}`), 0, true
				}
				return nil, 0, false
			}
			handled, err := executeProductSessionTrustForward(t.Context(), f.cfg, f.store, f.runtime, f.l, f.apply)
			if !handled || err == nil {
				t.Fatal("unsafe source allowed", bad)
			}
			for id, n := range f.b.writes {
				if strings.Contains(id, "pki-service-bootstrap-") && id != f.old.Resources[2].ID && n != 0 {
					t.Fatal("refusal wrote forward", id)
				}
			}
		})
	}
}

func TestProductSessionTrustForwardUnknownNativeWritesNeverReplay(t *testing.T) {
	for _, stage := range []string{"service-session-trust-custody", "service-session-trust-forward", "service-session-trust-custody-retire"} {
		t.Run(stage, func(t *testing.T) {
			f := newProductForwardFixture(t)
			f.failure = "unknown-" + stage
			if handled, err := executeProductSessionTrustForward(t.Context(), f.cfg, f.store, f.runtime, f.l, f.apply); !handled || err == nil {
				t.Fatal("unknown write was accepted")
			}
			writes := deploymentplan.Digest(f.b.writes)
			if _, err := executeProductSessionTrustForward(t.Context(), f.cfg, f.store, f.runtime, f.l, f.apply); err == nil {
				t.Fatal("unknown write retried")
			}
			if writes != deploymentplan.Digest(f.b.writes) {
				t.Fatal("unknown outcome replayed a native write")
			}
			old, _, err := f.old.ReadExecution(f.store.Root)
			if err != nil || deploymentplan.Digest(old) != deploymentplan.Digest(f.oldJournal) {
				t.Fatal("original failed child changed", err)
			}
		})
	}
}
func TestProductSessionTrustForwardClosesOnlyRecordedCompositeAfterInterruption(t *testing.T) {
	f := newProductForwardFixture(t)
	if _, err := executeProductSessionTrustForward(t.Context(), f.cfg, f.store, f.runtime, f.l, f.apply); err != nil {
		t.Fatal(err)
	}
	key := "kubernetes/product-identities-" + productIdentityScope(f.l)[:12] + "-service-session"
	for i := range f.e.Journal.Entries {
		if f.e.Journal.Entries[i].Key == key {
			f.e.Journal.Entries[i].State = "allocated"
			f.e.Journal.Entries[i].CompletedAt = time.Time{}
		}
	}
	// The actual receipt and all child outcomes are retained; only the process
	// boundary between RecordReceipt and CompleteEntry is replayed here.
	rel, err := filepath.Rel(f.store.Root, f.e.JournalPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.store.write(rel, mustProviderRecoveryJSON(f.e.Journal), true); err != nil {
		t.Fatal(err)
	}
	writes := deploymentplan.Digest(f.b.writes)
	if _, err := executeProductSessionTrustForward(t.Context(), f.cfg, f.store, f.runtime, f.l, f.apply); err != nil {
		t.Fatal(err)
	}
	if createOpenBaoACKCompletionEntry(f.e.Journal, key).State != "completed" || writes != deploymentplan.Digest(f.b.writes) || f.minted != 1 {
		t.Fatal("composite closure replayed effects")
	}
}

func TestProductSessionTrustForwardGuardPreservesFilesAcrossRetirementCrash(t *testing.T) {
	f := newProductForwardFixture(t)
	f.failure = "before-service-session-trust-forward"
	if _, err := executeProductSessionTrustForward(t.Context(), f.cfg, f.store, f.runtime, f.l, f.apply); err == nil {
		t.Fatal("missing after-retirement interruption")
	}
	f.bootstrap["bootstrap.key"] = "different existing key"
	if _, err := executeProductSessionTrustForward(t.Context(), f.cfg, f.store, f.runtime, f.l, f.apply); err == nil {
		t.Fatal("changed key minted")
	}
	if f.minted != 0 {
		t.Fatal("key guard did not precede native mint")
	}
	j, _, err := f.old.ReadExecution(f.store.Root)
	if err != nil || deploymentplan.Digest(j) != deploymentplan.Digest(f.oldJournal) {
		t.Fatal("original failure changed", err)
	}
}
func TestProductSessionTrustForwardReadonlyGuardActuallyChecksModeHashAndExistence(t *testing.T) {
	dir := t.TempDir()
	bin := t.TempDir()
	stat := `#!/usr/bin/env python3
import os,sys
s=os.stat(sys.argv[-1],follow_symlinks=False)
print(oct(s.st_mode&0o777)[2:]+" 10001 10001")
`
	if err := os.WriteFile(filepath.Join(bin, "stat"), []byte(stat), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	f := &environmentProductSessionForward{KeySHA256: environmentPreflightBytesSHA256([]byte("original-key")), CSRSHA256: environmentPreflightBytesSHA256([]byte("original-csr"))}
	command := strings.ReplaceAll(productSessionForwardKeyGuard(f), "/run/pki-bootstrap", dir)
	for _, bad := range []string{"valid", "key-mode", "csr-hash", "missing-key", "symlink-key"} {
		t.Run(bad, func(t *testing.T) {
			for _, name := range []string{"bootstrap.key", "bootstrap.csr"} {
				_ = os.Remove(filepath.Join(dir, name))
			}
			if err := os.WriteFile(filepath.Join(dir, "bootstrap.key"), []byte("original-key"), 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "bootstrap.csr"), []byte("original-csr"), 0600); err != nil {
				t.Fatal(err)
			}
			switch bad {
			case "key-mode":
				_ = os.Chmod(filepath.Join(dir, "bootstrap.key"), 0660)
			case "csr-hash":
				_ = os.WriteFile(filepath.Join(dir, "bootstrap.csr"), []byte("different-csr"), 0600)
			case "missing-key":
				_ = os.Remove(filepath.Join(dir, "bootstrap.key"))
			case "symlink-key":
				_ = os.Remove(filepath.Join(dir, "bootstrap.key"))
				_ = os.Symlink(filepath.Join(dir, "bootstrap.csr"), filepath.Join(dir, "bootstrap.key"))
			}
			beforeKey, _ := os.ReadFile(filepath.Join(dir, "bootstrap.key"))
			beforeCSR, _ := os.ReadFile(filepath.Join(dir, "bootstrap.csr"))
			cmd := exec.CommandContext(t.Context(), "/bin/sh", "-ec", command)
			raw, err := cmd.CombinedOutput()
			if (bad == "valid") != (err == nil) {
				t.Fatal("native read-only custody guard outcome", bad, err, string(raw))
			}
			afterKey, _ := os.ReadFile(filepath.Join(dir, "bootstrap.key"))
			afterCSR, _ := os.ReadFile(filepath.Join(dir, "bootstrap.csr"))
			if !bytes.Equal(beforeKey, afterKey) || !bytes.Equal(beforeCSR, afterCSR) {
				t.Fatal("guard changed custody")
			}
		})
	}
}
