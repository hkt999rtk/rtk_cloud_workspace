package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The native session fixture uses partial MQTT/App API stand-ins and does not
// provision issuer database/JWT Secrets. Observe the complete PKI owners and
// preparations with isolated role-key fixtures; never change provider inventory.
func joinedObservationIssuerSecrets(t *testing.T, environment string, inventory []deploymentplan.Object) []deploymentplan.Object {
	t.Helper()
	stack := "video-cloud-" + environment
	secrets := []deploymentplan.Object{
		{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": "pki-issuer-database", "namespace": stack + "-video-cloud", "uid": "isolated-native-database"}, "data": deploymentplan.Object{"url": "isolated-database-url"}},
		{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": "factoryenroll-runtime", "namespace": stack + "-video-cloud", "uid": "isolated-native-jwt"}, "data": deploymentplan.Object{"FACTORY_ENROLL_PRODUCTION_JWT_SECRET": "isolated-original-jwt", "FACTORY_ENROLL_PRODUCTION_JWT_AUDIENCE": "isolated-original-audience"}},
	}
	copy := make([]deploymentplan.Object, 0, len(inventory)+len(secrets))
	indices := make(map[string]int, len(inventory))
	for _, object := range inventory {
		metadata := deploymentplan.Map(object["metadata"])
		name := deploymentplan.String(metadata["name"])
		if object["kind"] == "Deployment" && metadata["namespace"] == "video-cloud-"+environment+"-video-cloud" && (name == "mqtt-pki" || name == "video-cloud-api-app-pki") {
			continue // Partial native protocol stand-ins, not maintained workload fixtures.
		}
		indices[deploymentplan.ID(object)] = len(copy)
		copy = append(copy, deploymentplan.Clone(object))
	}
	for _, secret := range secrets {
		id := deploymentplan.ID(secret)
		i, exists := indices[id]
		if !exists {
			indices[id] = len(copy)
			copy = append(copy, deploymentplan.Clone(secret))
			continue
		}
		data := deploymentplan.Map(copy[i]["data"])
		if data == nil {
			data = deploymentplan.Object{}
			copy[i]["data"] = data
		}
		for key, value := range deploymentplan.Map(secret["data"]) {
			if deploymentplan.String(data[key]) == "" {
				data[key] = value
			}
		}
	}
	return copy
}

func TestJoinedObservationIssuerSecretsUsesIsolatedRoleKeys(t *testing.T) {
	_, release, issuer, secrets := registryIssuerCanonicalFixture(t, "dev")
	partial := deploymentplan.Clone(secrets[0])
	delete(deploymentplan.Map(partial["data"]), "url")
	original := []deploymentplan.Object{issuer, partial}
	for _, name := range []string{"mqtt-pki", "video-cloud-api-app-pki"} {
		original = append(original, deploymentplan.Object{"kind": "Deployment", "metadata": deploymentplan.Object{"name": name, "namespace": release.Stack + "-video-cloud", "uid": "isolated-partial-" + name}})
	}
	before := deploymentplan.Digest(original)
	storeBefore, err := newSecretStore("", "dev")
	if err != nil {
		t.Fatal(err)
	}
	complete := joinedObservationIssuerSecrets(t, "dev", original)
	storeAfter, err := newSecretStore("", "dev")
	if err != nil || storeBefore != storeAfter {
		t.Fatal("observation fixture changed the original private store", err)
	}
	if _, err := observeEnvironmentBindings("dev", release, complete); err != nil {
		t.Fatal("isolated original issuer references", err)
	}
	if deploymentplan.Digest(original) != before {
		t.Fatal("observation fixture modified native provider inventory")
	}
	if len(complete) != 3 {
		t.Fatal("partial protocol stand-ins entered the full PKI observation")
	}
}

// Original provider, native session, core and product phases share one owner,
// store and native API. Only external HTTP/registry/signing are substituted.
type freshProductTerminalJoinedScenario struct {
	admission          providerExecutionContractInputs
	apiImage           environmentReleaseImage
	ociManifest        []byte
	initialTTL         time.Duration
	renewal            *secondSessionAdmission
	mintedReview       string
	renewAfterIssuance bool
	dockerPath         string
	rootKey, issuerKey *ecdsa.PrivateKey
	key, csr           []byte
	certificates       map[string][]byte
	states             map[string]managedSessionState
	clients            []managedSessionClient
	operatorID         string
	custodyPods        map[string]deploymentplan.Object
	custodyResults     map[string][]byte
	deletedInspections map[string]string
}

func (j *freshProductTerminalJoinedScenario) capture(root, issuer *ecdsa.PrivateKey, key, csr []byte, certificates map[string][]byte, states map[string]managedSessionState, clients []managedSessionClient) {
	j.rootKey, j.issuerKey, j.key, j.csr, j.certificates, j.states, j.clients = root, issuer, key, csr, certificates, states, clients
}

// The separately exercised native Mint callback emits only these observations;
// every credential UID below comes from its real accepted native child.
func (j *freshProductTerminalJoinedScenario) observeMint(t *testing.T, store secretStore, e *environmentCreateExecution, api deploymentplan.Backend, service environmentCreateServiceCeremony, state managedSessionState, review string) {
	t.Helper()
	stage := "service-credential-secrets"
	path := filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-observed.json")
	if review != "" {
		stage += "-renewal-" + review
		path = filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-renewal", review, "observed.json")
	}
	_, journal, err := acceptedCreateStage(store, e, "kubernetes/"+stage)
	if err != nil {
		t.Fatal(err)
	}
	uids := map[string]string{}
	for _, ns := range service.BootstrapCredentialNamespaces {
		id := "Secret/" + ns + "/service-bootstrap-" + state.Session
		o, err := api.Read(t.Context(), id)
		if err != nil || o == nil || journal.Owners[id] == "" || deploymentplan.Map(o["metadata"])["uid"] != journal.Owners[id] {
			t.Fatal("original accepted Mint credential UID", id, err)
		}
		uids[ns] = journal.Owners[id]
	}
	binding := service.CleanupBinding
	binding.SessionID, binding.SecretUID, binding.ConfigMapName = state.Session, uids[service.BootstrapCredentialNamespace], ""
	native := deploymentplan.Object{"session_id": state.Session, "issuer_id": state.Issuer, "root_sha256": state.Root, "bootstrap_fingerprint": state.Fingerprint, "status": state.Status, "subjects": state.Subjects, "expires_at": state.Expires}
	expected := createServiceSessionObserved{Cleanup: binding, UIDs: uids, Reader: service.CustodyReader, Session: mustProviderRecoveryJSON(native)}
	canonical, err := store.safePath(path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(canonical); err == nil {
		var existing createServiceSessionObserved
		if readExecutionJSON(canonical, &existing) != nil || deploymentplan.Digest(existing) != deploymentplan.Digest(expected) {
			t.Fatal("same native Mint observation differs from its original saved record")
		}
		return
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	if _, err := writeEnvironmentCreateBootstrapJSON(store, path, expected); err != nil {
		t.Fatal(err)
	}
}

// This constructor never invokes another fixture/store constructor and never
// generates another Root, ICA, seed key, CSR or parent execution.
func (j *freshProductTerminalJoinedScenario) productAdapter(t *testing.T, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, api deploymentplan.Backend, parent environmentCreatePlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, state managedSessionState) *productProtocolFixture {
	t.Helper()
	f := &productProtocolFixture{t: t, cfg: cfg, p: parent.Bootstrap, store: store, e: e, plans: map[string]*deploymentplan.Plan{}, rootKey: j.rootKey, icaKey: j.issuerKey, next: service.Intermediate, policies: map[string]string{}, roles: map[string][]string{}, rows: map[string]managedSessionClient{}, states: map[string][]byte{}, inspections: map[string][]byte{}, bootstrap: map[string]string{}, posts: map[string]int{}, session: state.Session, status: state.Status, expires: state.Expires, subjects: slices.Clone(state.Subjects), installed: slices.Clone(state.Installed), unscoped: true, registration: true, currentCRLs: map[string]deploymentplan.Object{}}
	var err error
	f.root, err = parseFirstCertificatePEM([]byte(service.Root.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	f.ica, err = parseFirstCertificatePEM([]byte(service.Intermediate.Certificate))
	if err != nil {
		t.Fatal(err)
	}
	f.l = &createNormalTailProductLifecycle{ParentSHA256: e.Journal.PlanSHA256, SourceSHA256: deploymentplan.Digest(e.Journal), OwnerSHA256: deploymentplan.Digest(e.owner), Root: service.Root, Intermediate: service.Intermediate, Policy: findEnvironmentCreateDomainPolicy(parent.Bootstrap, "service"), Outputs: slices.Clone(parent.Bootstrap.ProductIdentities), Bootstrap: parent.Bootstrap, SourceOwners: map[string]string{}}
	f.b = &productProtocolBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}}}
	for _, row := range j.clients {
		f.rows[row.Subject] = row
	}
	f.bootstrap["bootstrap.key"], f.bootstrap["bootstrap.csr"] = string(j.key), string(j.csr)
	f.bootstrap["bootstrap.crt"] = string(j.certificates[runtime.ServiceSessionRecovery.RenewedCertPath])
	f.rows["service:deployment-bootstrap"] = managedSessionClient{Subject: "service:deployment-bootstrap", Issuer: service.Intermediate.ID, Fingerprint: state.Fingerprint, Status: "succeeded"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/boundary/docker" {
			raw, err := io.ReadAll(r.Body)
			if err != nil {
				http.Error(w, "isolated command input", 500)
				return
			}
			var call struct {
				Args []string `json:"args"`
			}
			if json.Unmarshal(raw, &call) != nil {
				http.Error(w, "isolated command input", 500)
				return
			}
			var output []byte
			handled := false
			if len(call.Args) > 0 && call.Args[0] == "info" {
				output = mustProviderRecoveryJSON([]deploymentplan.Object{{"Name": "buildx", "Path": j.dockerPath}})
				handled = true
			}
			if len(call.Args) > 1 && call.Args[0] == "buildx" && call.Args[1] == "version" {
				output = []byte("github.com/docker/buildx v0.20.0")
				handled = true
			}
			if index := slices.Index(call.Args, "buildx"); index >= 0 && len(call.Args) > index+2 && call.Args[index+1] == "imagetools" && call.Args[index+2] == "inspect" {
				ref := call.Args[len(call.Args)-1]
				if strings.HasSuffix(ref, "@sha256:"+environmentPreflightBytesSHA256(j.ociManifest)) {
					output = j.ociManifest
					handled = true
				}
			}
			if handled {
				_ = json.NewEncoder(w).Encode(deploymentplan.Object{"output": base64.StdEncoding.EncodeToString(output), "code": 0})
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(raw))
		}
		f.serve(w, r)
	}))
	t.Cleanup(server.Close)
	f.server = server
	origin, _ := url.Parse(server.URL)
	operatorID := j.operatorID
	if runtime.PKIClient != nil {
		operatorID = runtime.PKIClient.OperatorID
	}
	runtime.PKIClient = &environmentCreatePKIClient{Origin: origin, HTTP: server.Client(), Environment: cfg.Environment, OperatorID: operatorID, Execution: e, token: "isolated-original-operator", authenticatedAt: time.Now().UTC()}
	f.runtime = runtime
	return f
}

func (j *freshProductTerminalJoinedScenario) attachNativeBoundary(t *testing.T, f *productProtocolFixture, q secondSessionAdmission) {
	t.Helper()
	original := os.Getenv("RTK_CLOUD_KUBECTL")
	if original == "" {
		var err error
		original, err = exec.LookPath("kubectl")
		if err != nil {
			t.Fatal(err)
		}
	}
	f.nativeBoundary = func(args []string, input []byte) ([]byte, int, bool) {
		joined := strings.Join(args, " ")
		if slices.Contains(args, "registry-read") {
			query := args[len(args)-1]
			if strings.HasPrefix(query, "SELECT json_build_object('count',count(*))") {
				count := 0
				for id := range j.states {
					if strings.Contains(query, "session_id='"+id+"'") {
						count++
					}
				}
				return mustProviderRecoveryJSON(deploymentplan.Object{"count": count}), 0, true
			}
			var fileStates map[string]managedSessionState
			if readExecutionJSON(filepath.Join(os.Getenv("RTK_LATE_NATIVE_OBJECTS"), "mint-states.json"), &fileStates) == nil {
				for id, state := range fileStates {
					j.states[id] = state
				}
			}
			if j.renewal != nil {
				j.acknowledgeInstalledBatch(t, f, *j.renewal)
			}
			if strings.Contains(query, "finalized_at IS NOT NULL") {
				if readExecutionJSON(filepath.Join(os.Getenv("RTK_LATE_NATIVE_OBJECTS"), "cleanup-native-final.json"), &f.final) == nil {
					f.status = "sealed"
				}
				cmd := exec.CommandContext(t.Context(), original, args...)
				raw, err := cmd.Output()
				if err != nil {
					return nil, 1, true
				}
				return raw, 0, true
			}
			if strings.Contains(query, "'complete',subjects <@ installed_subjects") {
				return mustProviderRecoveryJSON(completedBootstrapExpiry{Session: f.session, Issuer: f.next.ID, Root: f.l.Root.RootFingerprint, Status: f.status, Expires: f.expires, Complete: len(f.installed) == len(f.subjects)}), 0, true
			}

			if strings.Contains(query, "pki_service_client_issuances") && strings.Contains(query, "subject<>") {
				rows := slices.Clone(j.clients)
				for _, output := range f.l.Outputs {
					if row, ok := f.rows[output.Subject]; ok {
						rows = append(rows, row)
					}
				}
				slices.SortFunc(rows, func(a, b managedSessionClient) int {
					if v := strings.Compare(a.Subject, b.Subject); v != 0 {
						return v
					}
					if v := strings.Compare(a.Caller, b.Caller); v != 0 {
						return v
					}
					return strings.Compare(a.Request, b.Request)
				})
				return mustProviderRecoveryJSON(rows), 0, true
			}
			if strings.Contains(query, "pki_service_client_issuances") || strings.Contains(query, "'installed',installed_subjects") && !strings.HasPrefix(query, joinedNativeAncestorSessionQueryPrefix) || strings.Contains(query, "finalized_at IS NOT NULL") {
				return nil, 0, false
			}
			for id, saved := range j.states {
				if strings.Contains(query, "session_id='"+id+"'") {
					if id == f.session {
						saved.Status, saved.Installed = f.status, slices.Clone(f.installed)
					}
					saved.OtherActive = 0
					for otherID, other := range j.states {
						if otherID == f.session {
							other.Status = f.status
							other.Expires = f.expires
						}
						if otherID != id && other.Status == "active" && other.Expires.After(time.Now()) {
							saved.OtherActive++
						}
					}
					return mustProviderRecoveryJSON(joinedNativeSessionRegistryRow(query, saved)), 0, true
				}
			}
		}
		if slices.Contains(args, "stat") && args[len(args)-1] == "/run/pki-bootstrap" {
			return []byte(q.Renewal.CustodyOwner + "\n"), 0, true
		}

		if strings.Contains(joined, "openbao-0") && (strings.Contains(joined, "bao policy") || strings.Contains(joined, "auth/kubernetes/role/")) {
			return nil, 0, false
		}
		if raw, code, handled := j.inspectionBoundary(t, f, args, input); handled {
			return raw, code, true
		}
		if i := slices.Index(args, "--"); i >= 0 && i+2 < len(args) && args[i+1] == "/app/serviceidentity-bootstrap" && (args[i+2] == "inspect" || args[i+2] == "inspect-admitted") {
			return nil, 0, false
		}
		if raw, code, handled := j.productStateBoundary(t, f, args, input); handled {
			return raw, code, true
		}
		if raw, code, handled := j.completedJobWaitBoundary(t, f, args); handled {
			return raw, code, true
		}

		if len(args) > 0 && args[len(args)-1] == "expire" {
			f.status = "expired"
			saved := j.states[f.session]
			saved.Status = "expired"
			saved.Installed = slices.Clone(f.installed)
			j.states[f.session] = saved
			if err := os.WriteFile(filepath.Join(os.Getenv("RTK_LATE_NATIVE_OBJECTS"), "mint-states.json"), mustProviderRecoveryJSON(j.states), 0600); err != nil {
				return nil, 1, true
			}
			return nil, 0, true
		}
		if raw, code, handled := joinedProducerRetirementBoundary(t, f, args, input); handled {
			return raw, code, true
		}
		cmd := exec.CommandContext(t.Context(), original, args...)
		cmd.Stdin = bytes.NewReader(input)
		raw, err := cmd.Output()
		if err == nil {
			if (slices.Contains(args, "create") || slices.Contains(args, "replace")) && !slices.Contains(args, "--dry-run=server") {
				var job deploymentplan.Object
				if json.Unmarshal(raw, &job) == nil && job["kind"] == "Job" && strings.HasPrefix(deploymentplan.String(deploymentplan.Map(job["metadata"])["name"]), "pki-custody-inspect-") {
					j.observeInspectionJob(t, f, job)
				} else if job != nil {
					j.observeControllerWrite(t, f, job)
				}
			}
			return raw, 0, true
		}
		if exit, ok := err.(*exec.ExitError); ok {
			return nil, exit.ExitCode(), true
		}
		return nil, 1, true
	}
	bin := t.TempDir()
	bridge := `#!/usr/bin/env python3
import sys,json,urllib.request,base64,os
p=json.dumps({'args':sys.argv[1:],'input':base64.b64encode(sys.stdin.buffer.read()).decode()}).encode()
r=json.load(urllib.request.urlopen(urllib.request.Request(os.environ['PRODUCT_PROTOCOL_BOUNDARY']+'/'+os.path.basename(sys.argv[0]),data=p,headers={'Content-Type':'application/json'})))
sys.stdout.buffer.write(base64.b64decode(r.get('output','')));sys.exit(r.get('code',0))
`
	j.dockerPath = filepath.Join(bin, "docker")
	for _, name := range []string{"kubectl", "docker"} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(bridge), 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(bin, "kubectl"))
	t.Setenv("PRODUCT_PROTOCOL_BOUNDARY", f.server.URL+"/boundary")
}

func joinedNativeProductApply(t *testing.T, f *productProtocolFixture, parent environmentCreatePlan, api deploymentplan.Backend) environmentCreateBootstrapApply {
	t.Helper()
	return func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		plan, err := readEnvironmentCreateStageInput(f.store, f.e, stage)
		if err != nil {
			return err
		}
		ctx = withEnvironmentCreateBootstrapRuntime(ctx, f.runtime)
		projected, err := projectEnvironmentCreateCustodyStage(ctx, f.cfg, f.store, &parent, f.e, defs, plan)
		if err != nil {
			return err
		}
		auditJoinedNativeContainerImages(t, projected)
		if plan == nil {
			live, err := f.runtime.Backend.Inventory(ctx, parent.Stack)
			if err != nil {
				return err
			}
			plan, err = deploymentplan.Compile(deploymentplan.Inputs{Operation: "scoped", Environment: f.cfg.Environment, Stack: parent.Stack, ConfigurationSHA256: parent.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, projected)}, projected, live)
			if err != nil {
				return err
			}
		}
		if plan.BindingsSHA256 != environmentCreateStageBindingsSHA(stage, projected) {
			return environmentCreateBootstrapBlocker("joined native stage changed its frozen original input")
		}
		check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.e.Check(ctx) }
		if err := executeEnvironmentCreateStagePlan(ctx, f.store, f.e, stage, plan, api, func(ctx context.Context, p *deploymentplan.Plan) error {
			return p.AdmitResumable(ctx, api, f.store.Root)
		}, check, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, api) }, func(context.Context) error { return nil }, io.Discard); err != nil {
			return err
		}
		objects, err := f.runtime.Backend.Inventory(ctx, parent.Stack)
		if err != nil {
			return err
		}
		clear(f.b.objects)
		for _, o := range objects {
			f.b.objects[deploymentplan.ID(o)] = deploymentplan.Clone(o)
		}
		return nil
	}
}

func (j *freshProductTerminalJoinedScenario) observeInspectionJob(t *testing.T, f *productProtocolFixture, job deploymentplan.Object) {
	t.Helper()
	if j.custodyPods == nil {
		j.custodyPods = map[string]deploymentplan.Object{}
		j.custodyResults = map[string][]byte{}
	}
	container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(job)["containers"])[0])
	command := certIssuerObjectList(container["command"])
	if len(command) != 7 || command[1] != "inspect-admitted" {
		t.Fatal("unexpected native public inspection command")
	}
	subject := deploymentplan.String(command[4])
	result := f.inspections[subject]
	if len(result) == 0 {
		t.Fatal("public inspector lacks original issued identity", subject)
	}
	for _, o := range coreContractNativeInventory(map[string]deploymentplan.Object{deploymentplan.ID(job): job}) {
		if err := os.WriteFile(filepath.Join(os.Getenv("RTK_LATE_NATIVE_OBJECTS"), strings.ReplaceAll(deploymentplan.ID(o), "/", "_")+".json"), mustProviderRecoveryJSON(o), 0600); err != nil {
			t.Fatal(err)
		}
		if o["kind"] == "Pod" {
			uid := deploymentplan.String(deploymentplan.Map(job["metadata"])["uid"])
			j.custodyPods[uid] = o
			j.custodyResults[deploymentplan.String(deploymentplan.Map(o["metadata"])["name"])] = result
		}
	}
}

func (j *freshProductTerminalJoinedScenario) inspectionBoundary(t *testing.T, f *productProtocolFixture, args []string, input []byte) ([]byte, int, bool) {
	if i := slices.Index(args, "get"); i >= 0 && i+1 < len(args) && args[i+1] == "pods" && slices.Contains(args, "--selector") {
		selector := args[slices.Index(args, "--selector")+1]
		if strings.HasPrefix(selector, "batch.kubernetes.io/controller-uid=") {
			uid := strings.TrimPrefix(selector, "batch.kubernetes.io/controller-uid=")
			items := []deploymentplan.Object{}
			if p := j.custodyPods[uid]; p != nil {
				items = append(items, p)
			} else {
				files, err := filepath.Glob(filepath.Join(os.Getenv("RTK_LATE_NATIVE_OBJECTS"), "Pod_*_*.json"))
				if err != nil {
					return nil, 1, true
				}
				for _, file := range files {
					var p deploymentplan.Object
					if readExecutionJSON(file, &p) != nil {
						continue
					}
					for _, owner := range certIssuerObjectList(deploymentplan.Map(p["metadata"])["ownerReferences"]) {
						if deploymentplan.Map(owner)["kind"] == "Job" && deploymentplan.Map(owner)["uid"] == uid {
							items = append(items, p)
						}
					}
				}
			}
			return mustProviderRecoveryJSON(deploymentplan.Object{"items": items}), 0, true
		}
	}
	if i := slices.Index(args, "logs"); i >= 0 && i+1 < len(args) {
		name := strings.TrimPrefix(args[i+1], "Pod/")
		if raw, ok := j.custodyResults[name]; ok {
			return raw, 0, true
		}
	}
	if i := slices.Index(args, "delete"); i >= 0 && i+2 < len(args) && args[i+1] == "--raw" && strings.HasPrefix(args[i+2], "/apis/batch/v1/namespaces/") {
		parts := strings.Split(strings.TrimPrefix(args[i+2], "/apis/batch/v1/namespaces/"), "/")
		if len(parts) != 3 || parts[1] != "jobs" || !strings.HasPrefix(parts[2], "pki-custody-inspect-") {
			return nil, 1, true
		}
		file := filepath.Join(os.Getenv("RTK_LATE_NATIVE_OBJECTS"), "Job_"+parts[0]+"_"+parts[2]+".json")
		var old, options deploymentplan.Object
		raw, err := os.ReadFile(file)
		if err != nil || json.Unmarshal(raw, &old) != nil || json.Unmarshal(input, &options) != nil || deploymentplan.Map(options["preconditions"])["uid"] != deploymentplan.Map(old["metadata"])["uid"] {
			return nil, 1, true
		}
		uid := deploymentplan.String(deploymentplan.Map(old["metadata"])["uid"])
		if pod := j.custodyPods[uid]; pod != nil {
			_ = os.Remove(filepath.Join(os.Getenv("RTK_LATE_NATIVE_OBJECTS"), strings.ReplaceAll(deploymentplan.ID(pod), "/", "_")+".json"))
			delete(j.custodyPods, uid)
		}
		if os.Remove(file) != nil {
			return nil, 1, true
		}
		if j.deletedInspections == nil {
			j.deletedInspections = map[string]string{}
		}
		j.deletedInspections[deploymentplan.ID(old)] = uid
		return []byte("{}"), 0, true
	}
	return nil, 0, false
}

type joinedClosureInterrupt struct {
	*sessionNativeFaultBackend
	id           string
	writesBefore int
	stopped      bool
}

func (b *joinedClosureInterrupt) Wait(ctx context.Context, r deploymentplan.Resource) error {
	if r.ID == b.id && b.writes[b.id] > b.writesBefore && !b.stopped {
		b.stopped = true
		return errors.New("isolated interruption after original enrollment-close write")
	}
	return b.sessionNativeFaultBackend.Wait(ctx, r)
}

var _ deploymentplan.LeasingBackend = (*joinedClosureInterrupt)(nil)
var _ deploymentplan.ParentExecutionBackend = (*joinedClosureInterrupt)(nil)

const joinedNativeAncestorSessionQueryPrefix = "SELECT json_build_object('status',status,'issuer_id',issuer_id,'root_sha256',root_sha256,'installed',installed_subjects) FROM pki_deployment_bootstrap_sessions "

// Native mint and ancestor retirement each request their own strict DTO.
// Full state readers retain all installation/counter facts; current eight-field
// installation queries continue through the original product protocol adapter.
func joinedNativeSessionRegistryRow(query string, state managedSessionState) any {
	if strings.HasPrefix(query, joinedNativeAncestorSessionQueryPrefix) {
		return deploymentplan.Object{"status": state.Status, "issuer_id": state.Issuer, "root_sha256": state.Root, "installed": state.Installed}
	}
	const mintQuery = "SELECT json_build_object('session_id',session_id,'issuer_id',issuer_id,'root_sha256',root_sha256,'bootstrap_fingerprint',bootstrap_fingerprint,'status',status,'subjects',subjects,'expires_at',expires_at) FROM pki_deployment_bootstrap_sessions "
	if strings.HasPrefix(query, mintQuery) {
		return deploymentplan.Object{"session_id": state.Session, "issuer_id": state.Issuer, "root_sha256": state.Root, "bootstrap_fingerprint": state.Fingerprint, "status": state.Status, "subjects": state.Subjects, "expires_at": state.Expires}
	}
	return state
}

func TestJoinedNativeSessionRegistryProjectionKeepsExactRequestedColumns(t *testing.T) {
	state := managedSessionState{Session: "original-session", Issuer: "original-issuer", Root: strings.Repeat("a", 64), Fingerprint: strings.Repeat("b", 64), Status: "active", Subjects: []string{"service:core", "service:product"}, Installed: []string{"service:core", "service:product"}, OtherActive: 1, Expires: time.Now().UTC().Add(time.Hour)}
	query := "SELECT json_build_object('session_id',session_id,'issuer_id',issuer_id,'root_sha256',root_sha256,'bootstrap_fingerprint',bootstrap_fingerprint,'status',status,'subjects',subjects,'expires_at',expires_at) FROM pki_deployment_bootstrap_sessions WHERE environment='dev' AND session_id='original-session'"
	var minted struct {
		Session     string    `json:"session_id"`
		Issuer      string    `json:"issuer_id"`
		Root        string    `json:"root_sha256"`
		Fingerprint string    `json:"bootstrap_fingerprint"`
		Status      string    `json:"status"`
		Subjects    []string  `json:"subjects"`
		Expires     time.Time `json:"expires_at"`
	}
	if err := decodeEnvironmentJSON(mustProviderRecoveryJSON(joinedNativeSessionRegistryRow(query, state)), &minted); err != nil || minted.Session != state.Session || minted.Issuer != state.Issuer || minted.Fingerprint != state.Fingerprint || !minted.Expires.Equal(state.Expires) || !slices.Equal(minted.Subjects, state.Subjects) {
		t.Fatal("native mint SQL projection differs from strict row reader", err)
	}
	ancestorState := state
	ancestorState.Session, ancestorState.Status = "ancestor-session", "expired"
	ancestorState.Installed = []string{"service:core"}
	ancestorQuery := joinedNativeAncestorSessionQueryPrefix + "WHERE environment='dev' AND session_id='ancestor-session'"
	var ancestor struct {
		Status    string   `json:"status"`
		Issuer    string   `json:"issuer_id"`
		Root      string   `json:"root_sha256"`
		Installed []string `json:"installed"`
	}
	if err := decodeEnvironmentJSON(mustProviderRecoveryJSON(joinedNativeSessionRegistryRow(ancestorQuery, ancestorState)), &ancestor); err != nil || ancestor.Status != "expired" || ancestor.Issuer != ancestorState.Issuer || ancestor.Root != ancestorState.Root || !slices.Equal(ancestor.Installed, ancestorState.Installed) || slices.Equal(ancestor.Installed, state.Installed) {
		t.Fatal("native ancestor SQL projection differs from strict retirement row reader", err)
	}
	var complete managedSessionState
	if err := decodeEnvironmentJSON(mustProviderRecoveryJSON(joinedNativeSessionRegistryRow("SELECT full native session state with installed_subjects and other_active", state)), &complete); err != nil || deploymentplan.Digest(complete) != deploymentplan.Digest(state) {
		t.Fatal("native observation lost its actual counters or installed roster", err)
	}
}

func TestFreshProductTerminalJoinedOriginalOpenBaoWorkflow(t *testing.T) {
	runFreshProductJoinedOriginalOpenBaoWorkflow(t, false)
}
func TestFreshProductIssuedSessionSuccessorJoinedOriginalOpenBaoWorkflow(t *testing.T) {
	runFreshProductJoinedOriginalOpenBaoWorkflow(t, true)
}
func runFreshProductJoinedOriginalOpenBaoWorkflow(t *testing.T, renew bool) {
	scenario, originalService := newCurrentCoreForwardTestScenario(t)
	j := &freshProductTerminalJoinedScenario{operatorID: "77777777-7777-4777-8777-777777777777", initialTTL: 5 * time.Minute, renewAfterIssuance: renew}
	scenario.joined = j
	j.ociManifest = []byte(`{"schemaVersion":2,"mediaType":"application/vnd.oci.image.manifest.v1+json","config":{"digest":"sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},"layers":[]}`)
	digest := environmentPreflightBytesSHA256(j.ociManifest)
	j.apiImage = environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + digest}
	scenario.bootstrap.Image = j.apiImage.Reference
	for role, ref := range scenario.bootstrap.Images {
		if at := strings.Index(ref, "@sha256:"); at >= 0 {
			scenario.bootstrap.Images[role] = ref[:at] + "@sha256:" + digest
		}
	}
	scenario.bootstrap.Images["LKE_VIDEO_CLOUD_IMAGE"] = j.apiImage.Reference
	scenario.bootstrap.Images["LKE_OPENBAO_PKI_IMAGE"] = "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("a", 64)
	scenario.bootstrap.AccountManagerImage = scenario.bootstrap.Images["LKE_ACCOUNT_MANAGER_IMAGE"]
	selectedRelease := environmentRelease{Images: map[string]environmentReleaseImage{}}
	for role, image := range scenario.bootstrap.Images {
		selectedRelease.Images[role] = environmentReleaseImage{Reference: image}
	}
	for i := range scenario.bootstrap.Domains {
		slices.Sort(scenario.bootstrap.Domains[i].ServiceClientIDs)
		slices.Sort(scenario.bootstrap.Domains[i].ServerDNSNames)
	}
	policy := findEnvironmentCreateDomainPolicy(scenario.bootstrap, "service")
	for i := range scenario.bootstrap.ProductIdentities {
		scenario.bootstrap.ProductIdentities[i].PolicySHA256 = deploymentplan.Digest(policy)
	}
	scenario.cfg.Storage.OTAMode = "dedicated"
	scenario.cfg.Storage.OTAFirmware = deploymentStorageTarget{Policy: "colocated", Bucket: "rtk-cloud-" + scenario.cfg.Environment + "-ota-firmware-us-lax", Region: "us-lax", Prefix: "original/ota"}
	scenario.cfg.Storage.RuntimeMedia = deploymentStorageTarget{Policy: "colocated", Bucket: "rtk-cloud-" + scenario.cfg.Environment + "-runtime-us-lax", Region: "us-lax", Prefix: "original/media"}
	scenario.cfg.Workspace = mustWorkspaceRoot(t)
	template := "deploy/pki/render-staging-controller.py"
	templateRaw, templateErr := os.ReadFile(filepath.Join(scenario.cfg.Workspace, "repos/rtk_video_cloud", template))
	if templateErr != nil {
		t.Fatal(templateErr)
	}
	scenario.bootstrap.Templates = map[string]string{template: environmentPreflightBytesSHA256(templateRaw)}
	currentCoreMaintainedWrapperPolicy(t, &scenario.cfg)
	architecture, err := readStrictEnv(filepath.Join(mustWorkspaceRoot(t), "cloud_deploy/architectures/kubernetes/architecture.env"))
	if err != nil {
		t.Fatal(err)
	}
	scenario.cfg.Values[environmentPreflightValidityKey] = architecture[environmentPreflightValidityKey]
	if _, err := environmentPreflightValidity(scenario.cfg.Values); err != nil {
		t.Fatal("joined original preflight policy", err)
	}
	scenario.cfg.Values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] = "true"
	scenario.cfg = joinedMaintainedDeploymentConfig(t, scenario.cfg, scenario.bootstrap)
	listeners, err := renderEnvironmentCreateInitialPKIListeners(scenario.cfg, selectedRelease, "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", map[string]string{"PKI_OPERATOR_SIGNER_REF": "fixture-operator-key"})
	if err != nil {
		t.Fatal("original selected initial listener recipe", err)
	}
	scenario.runtime.InitialListeners = listeners
	auditJoinedNativeContainerImages(t, scenario.render(t, originalService, originalService.BootstrapSessionID))
	scenario.binding = nil
	j.admission = providerExecutionContractInputs{cfg: scenario.cfg, bootstrap: scenario.bootstrap, kubeconfig: scenario.kubeconfig, operator: map[string]string{"ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL": "original@example.test", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD": "isolated-original-password", "LINODE_TOKEN": "isolated-original-provider-token", "LINODE_OTA_OBJ_ACCESS_KEY_ID": "isolated-original-ota-key", "LINODE_OTA_OBJ_SECRET_ACCESS_KEY": "isolated-original-ota-secret", "LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "isolated-original-media-key", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "isolated-original-media-secret"}}
	runSecondSessionCandidateNativeJournal(t, true, scenario)
}

func (j *freshProductTerminalJoinedScenario) run(t *testing.T, scenario *currentCoreForwardTestScenario, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, api deploymentplan.Backend, native environmentKubernetesBackend, parent environmentCreatePlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, q secondSessionAdmission, review string, state managedSessionState) {
	t.Helper()
	if _, err := verifyServiceSessionHistoricalJobs(t.Context(), store, e, api, q); err != nil {
		t.Fatal("original native Job history before product issuance", err)
	}
	jobPlan, jobJournal, err := acceptedCreateStage(store, e, "kubernetes/service-session-original-key-renewal-"+review)
	if err != nil || jobPlan == nil || q.Renewal.Candidate == nil || jobPlan.SHA256() != q.Renewal.Candidate.SHA256() || len(jobPlan.Resources) != 1 {
		t.Fatal("current original native mint input before products", err)
	}
	// The actual Mint preserves this complete maintained reader recipe and
	// changes only its SID and certificate path. Check that invariant before
	// product issuance so a fixture that dropped database/key/CSR env fields
	// cannot defer its mismatch until the later same-key successor.
	custodyPlan, custodyJournal, err := acceptedCreateStage(store, e, "kubernetes/service-custody-reader-renewal-"+review)
	if err != nil || custodyPlan == nil || len(custodyPlan.Resources) != 1 {
		t.Fatal("current maintained custody input before products", err)
	}
	currentService := service
	currentService.BootstrapSessionID = q.Renewal.RenewedIntent.SessionID
	currentService.BootstrapClaim = q.Renewal.RenewedIntent.Claim
	expectedCustody := renderEnvironmentCreateCustodyReader(cfg, parent.Bootstrap, currentService)
	expectedContainer := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(expectedCustody.Object)["containers"])[0])
	expectedContainer["env"] = mergeEnvironmentNamedFields(expectedContainer["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/renew-" + q.Renewal.BindingSHA[:12] + "/bootstrap.crt"}))
	custody := custodyPlan.Resources[0]
	actualCustody, err := api.Read(t.Context(), custody.ID)
	if err != nil || custody.ID != deploymentplan.ID(expectedCustody.Object) || actualCustody == nil || deploymentplan.Map(actualCustody["metadata"])["uid"] != custodyJournal.Owners[custody.ID] || !deploymentplan.Matches(custody.Desired, actualCustody) || deploymentplan.Digest(environmentPKIPod(custody.Desired)) != deploymentplan.Digest(environmentPKIPod(expectedCustody.Object)) {
		t.Fatal("current custody differs from maintained recipe before products", err)
	}
	job := jobPlan.Resources[0]
	actualJob, err := api.Read(t.Context(), job.ID)
	if err != nil || actualJob == nil || deploymentplan.Map(actualJob["metadata"])["uid"] != jobJournal.Owners[job.ID] || !deploymentplan.Matches(job.Desired, actualJob) || !serviceSessionJobComplete(actualJob) {
		t.Fatal("current original native mint outcome before products", err)
	}
	nativeDir := os.Getenv("RTK_LATE_NATIVE_OBJECTS")
	native.RolloutTimeout = 30 * time.Second
	runtime.Backend = native
	material := map[string][]byte{"/run/pki-bootstrap/bootstrap.key": j.key, "/run/pki-bootstrap/bootstrap.csr": j.csr}
	for path, raw := range j.certificates {
		material[path] = raw
	}
	installServiceSessionNativeMintProtocol(t, nativeDir, cfg, service, j.states, material)
	installBootstrapCleanupNativeProtocol(t, nativeDir, material)
	f := j.productAdapter(t, cfg, store, e, api, parent, runtime, service, state)
	j.attachNativeBoundary(t, f, q)
	apply := joinedNativeProductApply(t, f, parent, api)
	observed, err := native.Inventory(t.Context(), parent.Stack)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range observed {
		j.observeControllerWrite(t, f, o)
	}
	// Publish the same selected generation's public authority through its actual
	// native producer. No identity or receipt is supplied by the adapter.
	caPath, _, err := initialTransportAuthorityPath(store, e.Journal.PlanSHA256, "openbao")
	if err != nil {
		t.Fatal("same original effective transport", err)
	}
	f.runtime.OpenBao.CACertPath = caPath
	root := scenario.roots["openbao_tls"]
	if err := publishEnvironmentCreateCustodyTrust(t.Context(), cfg, f.runtime, &root, true, apply); err != nil {
		t.Fatal("same-generation custody trust producer", err)
	}
	ctx := withEnvironmentCreateBootstrapRuntime(t.Context(), f.runtime)
	closeID := "Deployment/" + parent.Stack + "-video-cloud/certissuer"
	stop := &joinedClosureInterrupt{sessionNativeFaultBackend: api.(*sessionNativeFaultBackend), id: closeID, writesBefore: api.(*sessionNativeFaultBackend).writes[closeID]}
	interruptedApply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		if stage == "fresh-product-enrollment-closed" {
			return joinedNativeProductApply(t, f, parent, stop)(ctx, stage, defs)
		}
		return apply(ctx, stage, defs)
	}
	producerApply := environmentCreateBootstrapApply(interruptedApply)
	if j.renewAfterIssuance {
		producerApply = apply
	}
	producerErr := executeEnvironmentCreateFreshProductIdentities(ctx, cfg, parent.Bootstrap, parent.Release, store, f.runtime, service, producerApply)
	if j.renewAfterIssuance {
		if producerErr != nil {
			t.Fatal("actual five-output and accepted original closure", producerErr)
		}
	} else if producerErr == nil || !stop.stopped {
		t.Fatal("actual five-output producer must preserve interrupted native closure", producerErr)
	}
	program, err := readFreshProductProgram(store, e)
	if err != nil {
		t.Fatal("original joined product program", err)
	}
	f.l = &program.Lifecycle
	installed, err := readProductInstalled(store, f.l)
	if err != nil {
		t.Fatal(err)
	}
	beforeProgram, _ := os.ReadFile(filepath.Join(store.Root, freshProductProgramPath(e.Journal.PlanSHA256)))
	birth := captureJoinedProductBirth(t, f, api, installed)

	if len(installed.Outputs) != len(parent.Bootstrap.ProductIdentities) || len(f.installed) != len(state.Installed) {
		t.Fatal("issuance was confused with native workload installation")
	}
	incomplete := state
	incomplete.Installed = slices.Clone(f.installed)
	if terminal, err := observeFreshProductTerminalSession(ctx, cfg, store, e, native, q, review, incomplete, time.Now().UTC()); err != nil || terminal != nil {
		t.Fatal("issued-only phase became terminal", err)
	}
	if j.renewAfterIssuance {
		waitJoinedSeedExpiry(t, ctx, f.expires)
		if terminal, err := observeFreshProductTerminalSession(ctx, cfg, store, e, native, q, review, incomplete, time.Now().UTC()); err != nil || terminal != nil {
			t.Fatal("issued-only expired phase must require renewal", err)
		}
		ready := func(ctx context.Context) error { return e.Check(ctx) }
		next, nextReview, err := qualifyServiceSessionBatches(ctx, cfg, store, e, &parent, native, service, *f.runtime.ServiceSessionRecovery.OpenBaoRoot, ready, time.Now().UTC(), q.ExecutionDeadline)
		if err != nil {
			t.Fatal("original same-key successor qualification", err)
		}
		j.renewal = &next
		adapters, err := nativeServiceSessionBatchExecution(ctx, cfg, store, e, &parent, native, f.runtime, service, *f.runtime.ServiceSessionRecovery.OpenBaoRoot, next, nextReview, ready, io.Discard)
		if err != nil {
			t.Fatal("actual native Mint adapter", err)
		}
		bounded, cancel := context.WithDeadline(ctx, next.ExecutionDeadline)
		defer cancel()
		if err := executeServiceSessionBatches(bounded, store, e, nextReview, adapters, io.Discard); err != nil {
			t.Fatal("actual native same-key successor Job/batches", err)
		}
		if err := ensureFreshProductSessionSuccessor(ctx, cfg, store, e, native, next, nextReview); err != nil {
			t.Fatal("original program successor binding", err)
		}
		recovery, err := restoreSecondSessionRuntime(ctx, store, e, next, nextReview, service, *f.runtime.ServiceSessionRecovery.OpenBaoRoot, parent)
		if err != nil {
			t.Fatal(err)
		}
		f.runtime.ServiceSessionRecovery = recovery
		// Resume the actual full-create envelope after a later same-key session.
		// Its already-completed core qualification remains bound to the original
		// predecessor and must not be re-executed or rewritten.
		var selected struct {
			Review string `json:"review_sha256"`
		}
		if err := readExecutionJSON(filepath.Join(store.Root, currentCoreForwardBase(e.Journal.PlanSHA256), review, "selected.json"), &selected); err != nil {
			t.Fatal("original completed core selection", err)
		}
		envelope, err := writeCurrentCoreForwardEnvelope(store, e.Journal.PlanSHA256, review, selected.Review, "")
		if err != nil {
			t.Fatal("original completed core envelope", err)
		}
		foreign := *recovery
		foreign.Review = strings.Repeat("e", 64)
		foreignRuntime := f.runtime
		foreignRuntime.ServiceSessionRecovery = &foreign
		if loaded, err := loadCurrentCoreForwardEnvelope(ctx, store, e, envelope, review, &parent, foreignRuntime); err == nil || loaded != nil {
			t.Fatal("full-create envelope accepted a foreign session head")
		}
		if loaded, err := loadCurrentCoreForwardEnvelope(ctx, store, e, envelope, review, &parent, f.runtime); err != nil || loaded == nil {
			t.Fatal("full-create envelope after native same-key successor", err)
		}
		current, err := freshProductResumeCurrentCeremony(store, e, next, nextReview, service)
		if err != nil {
			t.Fatal(err)
		}
		f.runtime.ServiceCeremony = &current
		f.session, f.status, f.expires = next.Renewal.RenewedIntent.SessionID, "active", j.states[next.Renewal.RenewedIntent.SessionID].Expires
		f.installed = slices.Clone(j.states[f.session].Installed)
		ctx = withEnvironmentCreateBootstrapRuntime(t.Context(), f.runtime)
		if err := qualifyFreshProductSuccessorControls(ctx, cfg, store, f.runtime); err != nil {
			t.Fatal("native successor cleanup admission", err)
		}
		if err := applyFreshProductSuccessorControls(ctx, cfg, store, f.runtime, apply); err != nil {
			t.Fatal("native successor cleanup projection", err)
		}
		installed, err = readEffectiveFreshProductInstalled(store, e, f.l)
		if err != nil {
			t.Fatal("effective original issued outputs", err)
		}
		service = installed.Ceremony
		q = next
		review = nextReview
		assertJoinedProductBirthPreserved(t, f, j, api, birth)
		assertJoinedFreshSuccessorBindingRefusesChanges(t, f, next, nextReview)
	}
	// Each maintained runtime consumer receives the original mirror and native
	// bootstrap tuple through the actual fresh-target binder and ordinary engine.
	renderInventory, err := native.Inventory(ctx, parent.Stack)
	if err != nil {
		t.Fatal("same original provider render inventory", err)
	}
	// Exercise the real target observation at the point where the completed
	// enrollment producers coexist with their permanent workload identities.
	_, _, observationRelease := completeEnvironmentRenderFixture(cfg.Environment)
	for role, selected := range parent.Release.Images {
		if old, ok := observationRelease.Images[role]; ok {
			old.Reference = selected.Reference
			observationRelease.Images[role] = old
		}
	}
	observationInventory := joinedObservationIssuerSecrets(t, cfg.Environment, renderInventory)
	// These isolated role Secrets must be real owners in the fake native API,
	// just as they are in a deployed environment. Observation-only synthetic
	// UIDs cannot enter the durable original ownership bindings.
	var roleKeys []deploymentplan.Definition
	for _, object := range observationInventory {
		if object["kind"] != "Secret" {
			continue
		}
		name := deploymentplan.String(deploymentplan.Map(object["metadata"])["name"])
		if name != "pki-issuer-database" && name != "factoryenroll-runtime" {
			continue
		}
		definition := deploymentplan.Clone(object)
		delete(deploymentplan.Map(definition["metadata"]), "uid")
		roleKeys = append(roleKeys, deploymentplan.Definition{Object: definition, Phase: "configuration"})
	}
	if err := apply(ctx, "fixture-original-role-keys", roleKeys); err != nil {
		t.Fatal("native original isolated role-key owners", err)
	}
	renderInventory, err = native.Inventory(ctx, parent.Stack)
	if err != nil {
		t.Fatal(err)
	}
	observationInventory = joinedObservationIssuerSecrets(t, cfg.Environment, renderInventory)
	targetObserved, err := observeEnvironmentBindings(cfg.Environment, observationRelease, observationInventory)
	if err != nil {
		t.Fatal("fresh target inventory observation", err)
	}
	beforeOwners := deploymentplan.Digest(targetObserved.Owners)
	countPreparations := 0
	for _, pod := range targetObserved.Pods {
		if strings.HasPrefix(pod.Name, "pki-product-") || pod.Name == service.CustodyReader {
			countPreparations++
		}
	}
	if countPreparations != len(f.l.Outputs)+1 {
		t.Fatalf("expected original preparation owners, got %d", countPreparations)
	}
	if err := omitAuthenticatedFreshPreparations(ctx, cfg, f.runtime, &targetObserved, renderInventory); err != nil {
		t.Fatal("authenticate and omit original preparations", err)
	}
	if deploymentplan.Digest(targetObserved.Owners) != beforeOwners {
		t.Fatal("target observation discarded original resource owners")
	}
	for _, pod := range targetObserved.Pods {
		if strings.HasPrefix(pod.Name, "pki-product-") || pod.Name == service.CustodyReader {
			t.Fatal("temporary preparation was treated as a permanent target", pod.Name)
		}
	}
	if err := restoreEnvironmentCustodyBindings(ctx, cfg, store, "create", &targetObserved); err != nil {
		t.Fatal("restore original custody with temporary producers", err)
	}
	if err := applyEnvironmentFreshProductBindings(ctx, cfg, e, &targetObserved); err != nil {
		t.Fatal("original product bindings for resumed bootstrap", err)
	}
	if err := writeOrReuseFreshBootstrapBindings(ctx, cfg, store, e, targetObserved); err != nil {
		t.Fatal("save original fresh bootstrap bindings", err)
	}
	bindingsPath := filepath.Join(store.Root, "deployment", "create", e.Journal.PlanSHA256, "bootstrap", "bindings.json")
	originalBindings, err := os.ReadFile(bindingsPath)
	if err != nil {
		t.Fatal(err)
	}
	withLaterResource := targetObserved
	withLaterResource.Owners = make(map[string]string, len(targetObserved.Owners)+1)
	for id, uid := range targetObserved.Owners {
		withLaterResource.Owners[id] = uid
	}
	withLaterResource.Owners["Secret/"+parent.Stack+"-ingress/later-public-tls"] = "isolated-later-tls-owner"
	if err := writeOrReuseFreshBootstrapBindings(ctx, cfg, store, e, withLaterResource); err != nil {
		t.Fatal("reuse original bindings after later resource", err)
	}
	unchangedBindings, err := os.ReadFile(bindingsPath)
	if err != nil || !bytes.Equal(originalBindings, unchangedBindings) {
		t.Fatal("fresh resume overwrote original bindings", err)
	}
	t.Log("original temporary preparation identities, custody observation and immutable bindings reuse verified")
	catalog := joinedMaintainedConsumerCatalog(t, cfg, parent, f.runtime, renderInventory)
	outputs := map[string]environmentCreateProductIdentity{}
	for _, o := range f.l.Outputs {
		outputs[o.ID] = o
	}
	var consumers []deploymentplan.Definition
	for _, d := range catalog {
		counts := map[string]int{}
		body := deploymentplan.Clone(d.Object)
		if err := resolveCreateNormalTailProductACK(body, outputs, counts, service, false); err != nil {
			t.Fatal(err)
		}
		if len(counts) > 0 {
			consumers = append(consumers, deploymentplan.Definition{Object: body, Phase: "workload"})
		}
	}
	if len(consumers) != len(outputs) {
		t.Fatal("maintained five-consumer catalog", len(consumers))
	}
	bindings := environmentIdentityBindings{Version: 1, Environment: cfg.Environment, Stack: parent.Stack, Owners: map[string]string{}}
	objects, err := f.runtime.Backend.Inventory(ctx, parent.Stack)
	if err != nil {
		t.Fatal(err)
	}
	for _, o := range objects {
		bindings.Owners[deploymentplan.ID(o)] = deploymentplan.String(deploymentplan.Map(o["metadata"])["uid"])
	}
	if err := applyEnvironmentFreshProductBindings(ctx, cfg, e, &bindings); err != nil {
		t.Fatal(err)
	}
	bound, err := bindFreshProductTargetDefinitions(ctx, cfg, store, bindings, consumers)
	if err != nil {
		t.Fatal("actual five runtime bindings", err)
	}
	if j.renewAfterIssuance {
		assertJoinedOriginalMQTTPlacement(t, bound, birth.session, service.BootstrapSessionID)
	}
	if err := apply(ctx, "workload", bound); err != nil {
		t.Fatal("actual native five runtime installation", err)
	}
	// This is the isolated native registry boundary: ACKs arrive only after those
	// consumers have actually accepted their original mirror/session tuple.
	for _, o := range f.l.Outputs {
		if !slices.Contains(f.installed, o.Subject) {
			f.installed = append(f.installed, o.Subject)
		}
	}
	slices.Sort(f.installed)
	if len(f.installed) != len(f.subjects) {
		t.Fatal("native installed roster", len(f.installed), len(f.subjects))
	}
	currentNative := j.states[f.session]
	currentNative.Installed = slices.Clone(f.installed)
	j.states[f.session] = currentNative
	if err := os.WriteFile(filepath.Join(nativeDir, "mint-states.json"), mustProviderRecoveryJSON(j.states), 0600); err != nil {
		t.Fatal(err)
	}

	// A later registry read must retain the five actual runtime ACKs when
	// scoped core installation observations are seen again.
	if _, _, err := readManagedSessionState(ctx, cfg, f.runtime, f.session); err != nil {
		t.Fatal("current installed native session readback", err)
	}
	var persistedACK map[string]managedSessionState
	if readExecutionJSON(filepath.Join(nativeDir, "mint-states.json"), &persistedACK) != nil || !slices.Equal(persistedACK[f.session].Installed, f.installed) {
		t.Fatal("scoped native ACK observation removed installed runtime subjects")
	}
	start := time.Now().UTC()
	if !start.Before(f.expires) {
		t.Fatal("joined seed must still be live before clock boundary", time.Until(f.expires))
	}
	if !j.renewAfterIssuance {
		waitJoinedSeedExpiry(t, ctx, f.expires)
	}
	current := j.states[f.session]
	current.Status = f.status
	current.Installed = slices.Clone(f.installed)
	terminal, err := observeFreshProductTerminalSession(ctx, cfg, store, e, native, q, review, current, time.Now().UTC())
	if err != nil || terminal == nil {
		t.Fatal("joined original terminal observation across real expiry", err)
	}
	first, _, exists, err := loadPartialSessionAdmission(store, e)
	if err != nil || !exists {
		t.Fatal(err)
	}
	beforeRead := deploymentplan.Digest(e.Journal)
	handled, err := tryUnifiedServiceSessionContinuation(ctx, cfg, unifiedDeploymentOptions{Action: "preflight"}, &unifiedDeploymentReport{}, store, e, native, parent, nil, environmentCreateJournalEntry{}, first, io.Discard, io.Discard)
	if err != nil || handled {
		t.Fatal("ordinary dispatcher renewed completed original program", handled, err)
	}
	if err := observeSecondSessionSuccessor(ctx, cfg, store, e, native, parent, f.runtime.ServiceSessionRecovery.Ceremony, *f.runtime.ServiceSessionRecovery.OpenBaoRoot, review, start); err != nil {
		t.Fatal("ordinary shared observer across caller/read clock boundary", err)
	}
	old := j.states[first.PartialSession.Intent.SessionID]
	old.OtherActive = 0
	if err := verifyCompletedIssuerSessionContinuation(ctx, cfg, store, e, native, q, first.PartialSession.Intent, old, time.Now().UTC()); err != nil {
		t.Fatal("ordinary issuer continuation", err)
	}
	if err := completePartialSessionAdmission(ctx, cfg, store, f.runtime, service); err != nil {
		t.Fatal("ordinary partial admission", err)
	}
	if deploymentplan.Digest(e.Journal) != beforeRead {
		t.Fatal("terminal observation rewrote native history")
	}
	if err := executeEnvironmentCreateFreshProductIdentities(ctx, cfg, parent.Bootstrap, parent.Release, store, f.runtime, service, apply); err != nil {
		t.Fatal("resume exact original close-only producer", err)
	}
	if err := waitProductSessionInstallations(ctx, cfg, f.runtime, service, f.subjects); err != nil {
		t.Fatal("actual expired complete ACK wrapper", err)
	}
	cleanupFixture := serviceSessionNativeMintFixture{cfg: cfg, store: store, e: e, parent: parent, native: native, runtime: f.runtime, service: service, q: q, review: review, nativeDir: nativeDir, custody: material}
	client, _, calls := installBootstrapCleanupFinalizer(t, cleanupFixture)
	f.runtime.PKIClient = client
	ctx = withEnvironmentCreateBootstrapRuntime(t.Context(), f.runtime)
	finalRuntime, err := freshProductFinalizerRuntime(ctx, cfg, f.l, e, native, client, service)
	if err != nil {
		t.Fatal("actual fresh finalizer runtime handoff", err)
	}
	f.runtime = finalRuntime
	if err := finalizeEnvironmentCreateServiceSession(ctx, client, cfg, parent.Bootstrap, store, f.runtime, service, nil); err != nil {
		t.Fatal("joined original native expiry/finalizer/cleanup", err)
	}
	if err := completeFreshProductPreparations(ctx, cfg, store, e, f.l, installed, api, apply); err != nil {
		t.Fatal("joined original native product preparation retirement", err)
	}
	// A new ordinary qualification follows real native cleanup and retirement.
	// It must consume permanent-state/finalization evidence, not erased keys,
	// deleted credential copies or the removed custody Deployment.
	finished := deploymentplan.Digest(e.Journal)
	needsRenewal, minted := false, false
	if err := verifyAcceptedIssuerEnrollmentContinuation(ctx, cfg, store, e, native, true, issuerEnrollmentSessionAllowance{Completed: &q, NeedsRenewal: &needsRenewal, Minted: &minted}); err != nil || !needsRenewal || !minted {
		t.Fatal("ordinary preflight after full native PKI closure", err)
	}
	if err := observeSecondSessionSuccessor(ctx, cfg, store, e, native, parent, f.runtime.ServiceSessionRecovery.Ceremony, *f.runtime.ServiceSessionRecovery.OpenBaoRoot, review, time.Now().UTC()); err != nil {
		t.Fatal("ordinary same-key observer after credential/reader retirement", err)
	}
	if _, done, err := observeCompletedManagedSessionRecovery(ctx, cfg, parent.Bootstrap, store, f.runtime); err != nil || !done || finished != deploymentplan.Digest(e.Journal) {
		t.Fatal("post-finalization recovery changed history or demanded retired bootstrap state", err)
	}
	afterProgram, _ := os.ReadFile(filepath.Join(store.Root, freshProductProgramPath(e.Journal.PlanSHA256)))
	assertJoinedProductBirthPreserved(t, f, j, api, birth)
	if !bytes.Equal(beforeProgram, afterProgram) || *calls != 1 {
		t.Fatal("joined finalizer changed original program or repeated finalization")
	}
	for _, name := range []string{"bootstrap.key", "bootstrap.csr", "bootstrap.crt"} {
		if _, err := os.Stat(filepath.Join(nativeDir, "cleanup-custody", name)); !os.IsNotExist(err) {
			t.Fatal("native cleanup retained active seed", name, err)
		}
	}
	if j.renewAfterIssuance {
		t.Logf("joined original native core -> %d issued/9 ACK -> actual native same-key successor -> same outputs install/%d ACK -> native finalizer/preparation retirement", len(installed.Outputs), len(f.installed))
	} else {
		t.Logf("joined original native core -> %d products -> interrupted closure -> %d ACK/expiry -> four ordinary readers -> original closure resume/native finalizer/preparation retirement", len(installed.Outputs), len(f.installed))
	}
}

func waitJoinedSeedExpiry(t *testing.T, ctx context.Context, expires time.Time) {
	t.Helper()
	delay := time.Until(expires) + 20*time.Millisecond
	if delay > 12*time.Minute {
		t.Fatal("unbounded joined clock wait")
	}
	if delay > 0 {
		timer := time.NewTimer(delay)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			t.Fatal(ctx.Err())
		}
	}
}

// These are external controller/registry observations, never child receipts.
func (j *freshProductTerminalJoinedScenario) observeControllerWrite(t *testing.T, f *productProtocolFixture, o deploymentplan.Object) {
	t.Helper()
	kind := deploymentplan.String(o["kind"])
	if kind != "Deployment" && kind != "Job" {
		return
	}
	if kind == "Deployment" {
		if subject, _ := j.productEnrollmentSubject(t, f, o, false); subject != "" {
			// Model the native process's original issuance before the controller
			// can observe its private-file readiness probe succeeding.
			newIdentity := len(f.states[subject]) == 0
			if newIdentity && (f.status != "active" || !f.expires.After(time.Now().UTC())) {
				t.Fatal("original native enrollment authority expired before new issuance")
			}
			f.issue(subject)
			if newIdentity {
				t.Logf("original native product issued before session expiry: %s", subject)
			}
		}
	}
	if kind == "Job" {
		if j.renewal == nil || len(j.renewal.Renewal.Candidate.Resources) != 1 || deploymentplan.ID(o) != j.renewal.Renewal.Candidate.Resources[0].ID {
			return
		}
		if !deploymentplan.Matches(j.renewal.Renewal.Candidate.Resources[0].Desired, o) {
			t.Fatal("actual renewal Job changed frozen execution scope")
		}
		now := time.Now().UTC()
		o["status"] = deploymentplan.Object{"startTime": now.Format(time.RFC3339Nano), "completionTime": now.Format(time.RFC3339Nano), "succeeded": float64(1), "conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
		j.executeRenewalJob(t, f, *j.renewal)
	}
	for _, observed := range coreContractNativeInventory(map[string]deploymentplan.Object{deploymentplan.ID(o): o}) {
		file := filepath.Join(os.Getenv("RTK_LATE_NATIVE_OBJECTS"), strings.ReplaceAll(deploymentplan.ID(observed), "/", "_")+".json")
		if err := os.WriteFile(file, mustProviderRecoveryJSON(observed), 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func (j *freshProductTerminalJoinedScenario) executeRenewalJob(t *testing.T, f *productProtocolFixture, q secondSessionAdmission) {
	t.Helper()
	if j.mintedReview == deploymentplan.Digest(q) {
		return
	}
	if environmentPreflightBytesSHA256(j.key) != q.Renewal.KeySHA || environmentPreflightBytesSHA256(j.csr) != q.Renewal.CSRSHA {
		t.Fatal("native external Job changed original key/CSR")
	}
	keyBlock, _ := pem.Decode(j.key)
	csrBlock, _ := pem.Decode(j.csr)
	if keyBlock == nil || csrBlock == nil {
		t.Fatal("original native Job input malformed")
	}
	key, err := x509.ParseECPrivateKey(keyBlock.Bytes)
	if err != nil {
		t.Fatal("original native fixture EC key", err)
	}
	request, err := x509.ParseCertificateRequest(csrBlock.Bytes)
	if err != nil || request.CheckSignature() != nil {
		t.Fatal("original native CSR signature")
	}
	wantSPKI, err := x509.MarshalPKIXPublicKey(key.Public())
	if err != nil || !bytes.Equal(wantSPKI, request.RawSubjectPublicKeyInfo) {
		t.Fatal("native Job was given another CSR/key")
	}
	now := time.Now().UTC()
	template := &x509.Certificate{SerialNumber: big.NewInt(103), Subject: request.Subject, NotBefore: now.Add(-time.Minute), NotAfter: now.Add(12 * time.Minute).Truncate(time.Second), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, f.ica, &key.PublicKey, j.issuerKey)
	if err != nil {
		t.Fatal(err)
	}
	leaf, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatal(err)
	}
	// Native bootstrap preserves IssuingCA plus CAChain=[ICA,Root].
	chain := append(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), []byte(f.next.Certificate+f.next.Certificate+f.l.Root.Certificate)...)
	path := "/run/pki-bootstrap/renew-" + q.Renewal.BindingSHA[:12] + "/bootstrap.crt"
	j.certificates[path] = chain
	file := filepath.Join(os.Getenv("RTK_LATE_NATIVE_OBJECTS"), "cleanup-custody", strings.TrimPrefix(path, "/run/pki-bootstrap/"))
	if err := os.MkdirAll(filepath.Dir(file), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, chain, 0600); err != nil {
		t.Fatal(err)
	}
	old := j.states[q.Renewal.Intent.SessionID]
	old.Status = "expired"
	j.states[old.Session] = old
	f.status = "expired"
	next := q.Renewal.RenewedIntent
	j.states[next.SessionID] = managedSessionState{Session: next.SessionID, Issuer: next.IssuerID, Root: next.RootSHA, Fingerprint: environmentPreflightBytesSHA256(der), Status: "active", Subjects: slices.Clone(next.Subjects), Expires: leaf.NotAfter}
	if err := os.WriteFile(filepath.Join(os.Getenv("RTK_LATE_NATIVE_OBJECTS"), "mint-states.json"), mustProviderRecoveryJSON(j.states), 0600); err != nil {
		t.Fatal(err)
	}
	j.mintedReview = deploymentplan.Digest(q)
}

func (j *freshProductTerminalJoinedScenario) acknowledgeInstalledBatch(t *testing.T, f *productProtocolFixture, q secondSessionAdmission) {
	// The isolated runtime publishes ACK facts only after every exact admitted
	// Deployment has its recorded native UID/body and the reference is current.
	t.Helper()
	if j.mintedReview != deploymentplan.Digest(q) {
		return
	}
	for _, batch := range q.Batches {
		if batch.Plan == nil {
			continue
		}
		journal, exists, err := batch.Plan.ReadExecution(f.store.Root)
		if err != nil || !exists || !journal.Accepted {
			return
		}
		for _, r := range batch.Plan.Resources {
			if r.Desired["kind"] != "Deployment" {
				continue
			}
			o, err := f.runtime.Backend.Read(t.Context(), r.ID)
			if err != nil || o == nil || deploymentplan.Map(o["metadata"])["uid"] != journal.Owners[r.ID] || !deploymentplan.Matches(r.Desired, o) {
				return
			}
		}
	}
	if q.Reference == nil {
		return
	}
	for _, r := range q.Reference.Plan.Resources {
		o, err := f.runtime.Backend.Read(t.Context(), r.ID)
		if err != nil || o == nil || !deploymentplan.Matches(r.Desired, o) {
			return
		}
	}
	state := j.states[q.Renewal.RenewedIntent.SessionID]
	for _, subject := range q.RequiredACK {
		if !slices.Contains(state.Installed, subject) {
			state.Installed = append(state.Installed, subject)
		}
	}
	slices.Sort(state.Installed)
	j.states[state.Session] = state
	if err := os.WriteFile(filepath.Join(os.Getenv("RTK_LATE_NATIVE_OBJECTS"), "mint-states.json"), mustProviderRecoveryJSON(j.states), 0600); err != nil {
		t.Fatal(err)
	}
}

func (j *freshProductTerminalJoinedScenario) prepareInitialTransport(t *testing.T, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, api deploymentplan.Backend, native environmentKubernetesBackend, parent environmentCreatePlan, root environmentCreateRegisteredIssuer, scenario *currentCoreForwardTestScenario, service environmentCreateServiceCeremony) string {
	t.Helper()
	protocolPath := filepath.Join(os.Getenv("RTK_LATE_NATIVE_OBJECTS"), "unified-kubectl.py")
	protocol, err := os.ReadFile(protocolPath)
	if err != nil {
		t.Fatal(err)
	}
	originalGuard := "    if f.read() != b'isolated-native-kubeconfig': sys.exit(72)"
	if strings.Count(string(protocol), originalGuard) != 1 {
		t.Fatal("original isolated kube identity boundary changed")
	}
	guard := "    identity = f.read()\n    if identity != b'isolated-native-kubeconfig' and hashlib.sha256(identity).hexdigest() != '" + environmentPreflightBytesSHA256(native.KubeconfigContent) + "': sys.exit(72)"
	protocol = []byte("import hashlib\n" + strings.Replace(string(protocol), originalGuard, guard, 1))
	controllerMarker := "        if kind == 'Deployment':\n"
	if strings.Count(string(protocol), controllerMarker) != 1 {
		t.Fatal("original isolated write boundary changed")
	}
	protocol = []byte(strings.Replace(string(protocol), controllerMarker, "        if kind == 'PersistentVolumeClaim' and '--dry-run=server' not in a:\n            obj['status']={'phase':'Bound'}\n"+controllerMarker, 1))
	listMarker := "            if kind == 'namespace': items = [o for o in objs if o.get('kind') == 'Namespace']"
	if strings.Count(string(protocol), listMarker) != 1 {
		t.Fatal("original isolated inventory boundary changed")
	}
	protocol = []byte(strings.Replace(string(protocol), listMarker, "            if kind == 'persistentvolumes': items = [o for o in objs if o.get('kind') == 'PersistentVolume']\n            elif kind == 'namespace': items = [o for o in objs if o.get('kind') == 'Namespace']", 1))
	saveMarker := "        if '--dry-run=server' not in a: save(file, obj)"
	if strings.Count(string(protocol), saveMarker) != 1 {
		t.Fatal("original native storage write boundary changed")
	}
	volumeController := `        if kind == 'PersistentVolumeClaim' and '--dry-run=server' not in a:
            pvname='pv-'+meta['uid']
            obj.setdefault('spec',{})['volumeName']=pvname
            pv={'apiVersion':'v1','kind':'PersistentVolume','metadata':{'name':pvname,'uid':str(uuid.uuid5(uuid.NAMESPACE_DNS,'pv:'+meta['uid'])),'resourceVersion':'1'},'spec':{'persistentVolumeReclaimPolicy':'Retain','storageClassName':obj['spec'].get('storageClassName',''),'accessModes':obj['spec'].get('accessModes',[]),'volumeMode':'Filesystem','capacity':obj['spec'].get('resources',{}).get('requests',{}),'claimRef':{'name':name,'namespace':ns,'uid':meta['uid']},'csi':{'driver':'linodebs.csi.linode.com','volumeHandle':str(1000+int(hashlib.sha256(meta['uid'].encode()).hexdigest()[:8],16))}},'status':{'phase':'Bound'}}
            save(path('PersistentVolume','',pvname),pv)
`
	protocol = []byte(strings.Replace(string(protocol), saveMarker, volumeController+saveMarker, 1))
	failMarker := "    def fail(message):\n"
	if strings.Count(string(protocol), failMarker) != 1 {
		t.Fatal("original native diagnostic boundary changed")
	}
	protocol = []byte(strings.Replace(string(protocol), failMarker, failMarker+"        with open(os.path.join(root,'joined-native-errors.log'),'a') as f: f.write('operation='+a[0]+' reason='+message+'\\n')\n", 1))
	t.Cleanup(func() {
		if t.Failed() {
			if raw, err := os.ReadFile(filepath.Join(os.Getenv("RTK_LATE_NATIVE_OBJECTS"), "joined-native-errors.log")); err == nil {
				t.Logf("isolated native operation diagnostics: %s", raw)
			}
		}
	})
	if err := os.WriteFile(protocolPath, protocol, 0600); err != nil {
		t.Fatal(err)
	}
	ctx := t.Context()
	now := time.Now().UTC()
	// Establish this synthetic generation's ORIGINAL temporary transport during
	// its admitted initial bootstrap, before any Service session qualification.
	// The successor below reuses those same keys; no existing identity is replaced.
	original := map[string]environmentCreateTransport{}
	for _, role := range []string{"management", "openbao"} {
		material, err := createEnvironmentTransport(store, e.Journal.PlanSHA256, role, map[string][]string{role: {role + ".isolated.svc"}}, now.Add(-time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		original[role] = material
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, Backend: native}
	runtime.OpenBao.CACertPath = filepath.Join(store.Root, "deployment/create/material", e.Journal.PlanSHA256, "openbao-transport/ca.crt")
	apply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		auditJoinedNativeContainerImages(t, defs)
		live, err := native.Inventory(ctx, parent.Stack)
		if err != nil {
			return err
		}
		p, err := deploymentplan.Compile(deploymentplan.Inputs{Operation: "scoped", Environment: cfg.Environment, Stack: parent.Stack, ConfigurationSHA256: parent.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs)}, defs, live)
		if err != nil {
			return err
		}
		return executeEnvironmentCreateStagePlan(ctx, store, e, stage, p, api, func(ctx context.Context, p *deploymentplan.Plan) error { return p.AdmitResumable(ctx, api, store.Root) }, func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, api) }, func(context.Context) error { return nil }, io.Discard)
	}
	sourcePlan, sourceJournal, err := acceptedCreateStage(store, e, "kubernetes/service-session")
	if err != nil {
		t.Fatal(err)
	}
	sourceJob, ok := managedSessionSourceJob(sourcePlan, sourceJournal)
	if !ok {
		t.Fatal("original source Job required")
	}
	claim := ""
	for _, v := range certIssuerObjectList(environmentPKIPod(sourceJob.Desired)["volumes"]) {
		volume := deploymentplan.Map(v)
		if volume["name"] == "state" {
			claim = deploymentplan.String(deploymentplan.Map(volume["persistentVolumeClaim"])["claimName"])
		}
	}
	if claim == "" {
		t.Fatal("original source custody claim required")
	}
	pull, err := newK8SDockerConfigSecretObject(parent.Stack+"-video-cloud", "ghcr-pull", "isolated-original-publisher", "isolated-original-pull-token")
	if err != nil {
		t.Fatal(err)
	}
	foundation := []deploymentplan.Definition{environmentCreateSecret(parent.Stack+"-video-cloud", "pki-verifier-database", map[string]string{"url": "postgres://isolated-original-verifier"}), environmentCreateSecret(parent.Stack+"-video-cloud", "pki-migration-database", map[string]string{"url": "postgres://isolated-original-migration"}), environmentCreateServiceClaimDefinition(cfg, parent.Stack+"-video-cloud", claim), {Phase: "configuration", Object: deploymentplan.Object(pull)}}
	if err := apply(ctx, "fixture-original-session-foundation", foundation); err != nil {
		t.Fatal("original native session foundation", err)
	}
	if err := publishEnvironmentCreateCustodyTrust(ctx, cfg, runtime, nil, false, apply); err != nil {
		t.Fatal("original custody trust producer", err)
	}
	runtime.OpenBao.RootTokenPath = filepath.Join(store.Root, "openbao/root-token")
	binding, err := createEnvironmentCustodyBinding(cfg, parent.Bootstrap, e.Journal.PlanSHA256, root.RootFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	binding.ServiceRootSHA256 = service.Root.RootFingerprint
	j.configureOriginalCustody(t, cfg, runtime, binding)

	ns := parent.Stack + "-video-cloud"
	cm := func(name string, data deploymentplan.Object) deploymentplan.Definition {
		return deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": ns, "name": name}, "data": data}}
	}
	consumer := cm("pki-create-openbao-tls-consumer-trust", deploymentplan.Object{"roots.pem": original["openbao"].CACert + root.Certificate})
	ca := cm("pki-openbao-transport-ca", deploymentplan.Object{"ca.crt": original["openbao"].CACert})
	if err := apply(ctx, "fixture-original-openbao-consumer-trust", []deploymentplan.Definition{consumer, ca}); err != nil {
		t.Fatal(err)
	}
	candidate, err := prepareInitialTransportSuccessor(store, e.Journal.PlanSHA256, now)
	if err != nil {
		t.Fatal(err)
	}
	var resources []activeTransportRenewalResource
	ids := []string{deploymentplan.ID(consumer.Object), deploymentplan.ID(ca.Object)}
	for _, suffix := range []string{"-video-cloud", "-account-manager"} {
		ids = append(ids, "ConfigMap/"+parent.Stack+suffix+"/"+environmentCustodyTrustName)
	}
	for _, id := range ids {
		before, err := api.Read(ctx, id)
		if err != nil || before == nil {
			t.Fatal("original native transport owner", id, err)
		}
		after := deploymentplan.Clone(before)
		if deploymentplan.Map(before["metadata"])["name"] == environmentCustodyTrustName {
			after, err = initialTransportCustodySuccessor(before, original["openbao"].CACert, candidate.Transports["openbao"].CACert)
			if err != nil {
				t.Fatal(err)
			}
		} else {
			data := deploymentplan.Map(after["data"])
			for key, value := range data {
				data[key] = strings.ReplaceAll(deploymentplan.String(value), original["openbao"].CACert, candidate.Transports["openbao"].CACert)
			}
		}
		resources = append(resources, activeTransportRenewalResource{ID: id, UID: deploymentplan.String(deploymentplan.Map(before["metadata"])["uid"]), Before: deploymentplan.Clone(before), Desired: after})
	}
	// This is admission of the same synthetic original issuer operation, not
	// a completed milestone. Its exact selected parent and accepted source Job
	// are frozen through the same qualification/admission serialization as CLI.
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		t.Fatal(err)
	}
	operator, err := store.readOperator()
	if err != nil {
		t.Fatal(err)
	}
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/object-storage/buckets" || r.Header.Get("Authorization") != "Bearer "+operator["LINODE_TOKEN"] {
			t.Error("joined fixture attempted an unselected provider operation", r.Method, r.URL.Path)
			http.Error(w, "readonly original allocation required", http.StatusForbidden)
			return
		}
		var buckets []linodeStorageBucket
		for _, target := range []deploymentStorageTarget{cfg.Storage.OTAFirmware, cfg.Storage.RuntimeMedia} {
			buckets = append(buckets, linodeStorageBucket{Label: target.Bucket, Region: target.Region, S3Endpoint: "https://isolated-joined-storage.example.invalid", EndpointType: "E3"})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": buckets, "page": 1, "pages": 1, "results": len(buckets)})
	}))
	t.Cleanup(provider.Close)
	t.Setenv("RTK_CLOUD_LINODE_API_ROOT", provider.URL)
	checkerAPI := defaultDeploymentCredentialChecker()
	if _, err := observeOTAStorageForRender(ctx, cfg, store, operator, checkerAPI); err != nil {
		t.Fatal("actual original OTA allocation metadata", err)
	}
	if _, err := observeMediaStorageForRender(ctx, cfg, store, operator, checkerAPI); err != nil {
		t.Fatal("actual original media allocation metadata", err)
	}
	for _, secret := range rtkSecretCatalog() {
		if _, err := os.Stat(filepath.Join(store.Root, "runtime", secret.ID)); os.IsNotExist(err) {
			if err := store.write(filepath.Join("runtime", secret.ID), []byte("isolated-original-"+secret.ID), false); err != nil {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
	}
	originalAdmission := issuerEnrollmentContinuationProof{Parent: e.Journal.PlanSHA256, ParentJournal: deploymentplan.Digest(e.Journal), ParentOwner: deploymentplan.Digest(e.owner), Checker: checker, Selection: deploymentplan.Digest(operator), Release: deploymentplan.Digest(parent.Release), FirstStage: sourcePlan.SHA256()}
	originalReview := deploymentplan.Digest(originalAdmission)
	qualified := originalAdmission
	qualified.QualifiedAt = time.Now().UTC()
	qualified.ValidUntil = qualified.QualifiedAt.Add(time.Hour)
	if err := store.write(filepath.Join("deployment/create/issuer-enrollment-continuation", originalReview, "qualification.json"), mustProviderRecoveryJSON(qualified), false); err != nil {
		t.Fatal(err)
	}
	if err := e.BeginEntry(issuerEnrollmentContinuationPrefix+originalReview, "continuation", mustProviderRecoveryJSON(originalAdmission), qualified.QualifiedAt); err != nil {
		t.Fatal("same original issuer admission", err)
	}
	device, err := renderEnvironmentCreateDeviceBootstrapJob(ctx, cfg, parent.Bootstrap, scenario.roots["device"].ID, "active")
	if err != nil {
		t.Fatal("maintained device active recipe", err)
	}
	if err := apply(ctx, "device-root-active", device); err != nil {
		t.Fatal("actual device active child", err)
	}
	applications, err := renderEnvironmentCreateApplicationOwners(ctx, cfg, parent.Bootstrap, parent.Release, store, operator, service)
	if err != nil {
		t.Fatal("maintained original App owners", err)
	}
	applications = environmentCreateApplicationDefinitionsFor(applications, "video-cloud-api-app-pki", "mqtt-pki")
	for _, stage := range []string{"app-root-trust", "app-intermediate-trust"} {
		additional := []environmentCreateRegisteredIssuer(nil)
		if stage == "app-intermediate-trust" {
			additional = []environmentCreateRegisteredIssuer{scenario.issuers["app"]}
		}
		trust, err := configureEnvironmentCreateAppConsumers(parent.Bootstrap, scenario.roots["app"], additional, applications)
		if err != nil {
			t.Fatal("maintained App trust recipe", err)
		}
		if err := apply(ctx, stage, environmentCreateApplicationDefinitionsFor(trust, "video-cloud-api-app-pki", "mqtt-pki")); err != nil {
			t.Fatal("actual App trust child", err)
		}
		applications = trust
	}
	roots, issuers := map[string]environmentCreateRegisteredIssuer{}, map[string]environmentCreateRegisteredIssuer{}
	for k, v := range scenario.roots {
		roots[k] = v
	}
	for k, v := range scenario.issuers {
		issuers[k] = v
	}
	roots["service"] = service.Root
	issuers["service"] = service.Intermediate
	runtime.OpenBao.RootTokenPath = filepath.Join(store.Root, "openbao/root-token")
	if err := configureEnvironmentCreateCompleteSignerRole(ctx, cfg, runtime, roots, issuers); err == nil {
		t.Fatal("isolated external Bao interruption must preserve pending role intent")
	}
	if _, ok := initialTransportSuccessorCheckpoint(e.Journal); !ok {
		t.Fatal("actual original transport checkpoint is incomplete")
	}
	var source environmentCreateJournal
	if json.Unmarshal(mustProviderRecoveryJSON(e.Journal), &source) != nil {
		t.Fatal("original transport source journal")
	}
	artifact := initialTransportSuccessorArtifact{Schema: "rtk-cloud.initial-transport-successor-artifact/v1", Plan: initialTransportSuccessorPlan{Schema: "rtk-cloud.initial-transport-successor-plan/v1", Parent: e.Journal.PlanSHA256, OwnerSHA: deploymentplan.Digest(e.owner), ConfigurationSHA: deploymentplan.Digest(cfg), JournalSHA: deploymentplan.Digest(source), CandidateSHA: deploymentplan.Digest(candidate), Resources: resources}, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
	base := filepath.Join("deployment/create/transport-successor", deploymentplan.Digest(artifact))
	for path, value := range map[string]any{filepath.Join(base, "qualification.json"): artifact, filepath.Join(base, "source-journal.json"): source} {
		if err := store.write(path, mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	check := func() error { return e.Check(ctx) }
	// TLS process reload is an external boundary; the actual producer performs
	// every native write/readback and emits all receipts itself.
	if err := executeInitialTransportSuccessor(ctx, cfg, store, e, api, artifact, source, candidate, check, check, check); err != nil {
		t.Fatal("actual initial transport successor producer", err)
	}
	managedDefs, err := applyEnvironmentCreateManagedOpenBaoConsumers(parent.Bootstrap, service, scenario.roots, scenario.issuers, scenario.runtime.InitialListeners)
	if err != nil {
		t.Fatal("maintained original managed-root trust recipe", err)
	}
	var managed []deploymentplan.Definition
	for _, d := range managedDefs {
		if deploymentplan.ID(d.Object) == deploymentplan.ID(consumer.Object) {
			managed = append(managed, d)
		}
	}
	if len(managed) != 1 {
		t.Fatal("maintained original managed-root trust owner is not unique")
	}
	if err := apply(ctx, "fixture-managed-openbao-consumer-root", managed); err != nil {
		t.Fatal("actual original managed-root trust publication", err)
	}
	runtime.OpenBao.CACertPath, _, err = initialTransportAuthorityPath(store, e.Journal.PlanSHA256, "openbao")
	if err != nil {
		t.Fatal(err)
	}
	if err := publishEnvironmentCreateCustodyTrust(ctx, cfg, runtime, &root, true, apply); err != nil {
		t.Fatal("actual original managed-only custody trust", err)
	}
	runtime.Custody, runtime.ServiceCeremony = binding, &service
	projectionContext := withEnvironmentCreateBootstrapRuntime(ctx, runtime)
	if _, err := projectEnvironmentCreateCustodyStage(projectionContext, cfg, store, &parent, e, scenario.runtime.InitialListeners, nil); err != nil {
		t.Fatal("original custody owner receipt and transport projection", err)
	}
	// Smoke the exact maintained five-consumer renderer with the original
	// observed inputs before the lengthy session/product lineage.
	renderInventory, err := native.Inventory(ctx, parent.Stack)
	if err != nil {
		t.Fatal("original provider render inventory", err)
	}
	auditJoinedNativeContainerImages(t, joinedMaintainedConsumerCatalog(t, cfg, parent, runtime, renderInventory))
	sha, err := verifyPartialSessionOpenBaoTrust(ctx, store, e, native, root)
	if err != nil {
		t.Fatal("actual recorded original trust", err)
	}
	return sha
}

type joinedProductBirth struct {
	programme, installed []byte
	materials            map[string]string
	mirrors              map[string]string
	uids                 map[string]string
	key, csr, session    string
}

func captureJoinedProductBirth(t *testing.T, f *productProtocolFixture, api deploymentplan.Backend, installed *environmentProductIdentitySession) joinedProductBirth {
	t.Helper()
	out := joinedProductBirth{materials: map[string]string{}, mirrors: map[string]string{}, uids: map[string]string{}, key: environmentPreflightBytesSHA256([]byte(f.bootstrap["bootstrap.key"])), csr: environmentPreflightBytesSHA256([]byte(f.bootstrap["bootstrap.csr"])), session: installed.Ceremony.BootstrapSessionID}
	var err error
	out.programme, err = os.ReadFile(filepath.Join(f.store.Root, freshProductProgramPath(f.e.Journal.PlanSHA256)))
	if err != nil {
		t.Fatal(err)
	}
	out.installed, err = os.ReadFile(filepath.Join(f.store.Root, productIdentitySessionPath(f.l)))
	if err != nil {
		t.Fatal(err)
	}
	for _, receipt := range installed.Outputs {
		o, err := api.Read(t.Context(), receipt.ResourceID)
		if err != nil || o == nil {
			t.Fatal("original native output", err)
		}
		out.uids[receipt.ResourceID] = deploymentplan.String(deploymentplan.Map(o["metadata"])["uid"])
		out.mirrors[receipt.ResourceID] = deploymentplan.Digest(deploymentplan.Projection(o))
		out.materials[receipt.Subject] = environmentPreflightBytesSHA256(f.states[receipt.Subject]) + deploymentplan.Digest(f.rows[receipt.Subject])
	}
	return out
}
func assertJoinedProductBirthPreserved(t *testing.T, f *productProtocolFixture, j *freshProductTerminalJoinedScenario, api deploymentplan.Backend, birth joinedProductBirth) {
	t.Helper()
	programme, _ := os.ReadFile(filepath.Join(f.store.Root, freshProductProgramPath(f.e.Journal.PlanSHA256)))
	installed, _ := os.ReadFile(filepath.Join(f.store.Root, productIdentitySessionPath(f.l)))
	if !bytes.Equal(programme, birth.programme) || !bytes.Equal(installed, birth.installed) || environmentPreflightBytesSHA256(j.key) != birth.key || environmentPreflightBytesSHA256(j.csr) != birth.csr {
		t.Fatal("same-key successor changed original programme/installed/key/CSR")
	}
	for id, uid := range birth.uids {
		o, err := api.Read(t.Context(), id)
		if err != nil || o == nil || deploymentplan.Map(o["metadata"])["uid"] != uid || deploymentplan.Digest(deploymentplan.Projection(o)) != birth.mirrors[id] {
			t.Fatal("same-key successor changed native product mirror", id, err)
		}
	}
	for subject, hash := range birth.materials {
		if environmentPreflightBytesSHA256(f.states[subject])+deploymentplan.Digest(f.rows[subject]) != hash {
			t.Fatal("same-key successor re-enrolled original product", subject)
		}
	}
}
func assertJoinedOriginalMQTTPlacement(t *testing.T, defs []deploymentplan.Definition, oldSID, newSID string) {
	t.Helper()
	for _, worker := range []struct{ name, role string }{{"video-cloud-mqttfoundation", "mqtt"}, {"video-cloud-shadowworker", "shadow"}} {
		producerName := "pki-product-" + worker.role + "-" + strings.ReplaceAll(oldSID, "-", "")[:12]
		var consumer, producer deploymentplan.Object
		for _, d := range defs {
			name := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"])
			if name == worker.name {
				consumer = d.Object
			}
			if name == producerName {
				producer = d.Object
				if !d.Retain || !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["uid"])) {
					t.Fatal("original producer lost its actual retained UID")
				}
			}
		}
		if consumer == nil || producer == nil || mqttFoundationEnv(mqttFoundationPrimary(consumer), "PKI_BOOTSTRAP_SESSION_ID") != newSID {
			t.Fatal("renewed ACK lost original MQTT/shadow producer", worker.name)
		}
		if deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(producer["spec"])["template"])["metadata"])["labels"] == nil {
			t.Fatal("original producer labels missing")
		}
		// OpenBao has no same-PVC affinity. If a maintained RWO affinity is present,
		// its only producer term must reference the actual original producer.
		terms := certIssuerObjectList(deploymentplan.Map(deploymentplan.Map(environmentPKIPod(consumer)["affinity"])["podAffinity"])["requiredDuringSchedulingIgnoredDuringExecution"])
		if len(terms) > 0 {
			if len(terms) != 1 {
				t.Fatal("ambiguous original RWO affinity")
			}
			expressions := certIssuerObjectList(deploymentplan.Map(deploymentplan.Map(terms[0])["labelSelector"])["matchExpressions"])
			if len(expressions) != 1 || deploymentplan.Digest(deploymentplan.Map(expressions[0])["values"]) != deploymentplan.Digest([]any{worker.name, producerName}) {
				t.Fatal("renewed ACK moved original RWO placement")
			}
		}
	}
}

func assertJoinedFreshSuccessorBindingRefusesChanges(t *testing.T, f *productProtocolFixture, q secondSessionAdmission, review string) {
	t.Helper()
	path, err := f.store.safePath(freshProductSuccessorPath(f.e.Journal.PlanSHA256, review))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	restore := func() {
		if err := os.WriteFile(path, raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, mode := range []string{"missing", "foreign", "current-pending", "credential-uid"} {
		t.Run("successor-binding-"+mode, func(t *testing.T) {
			defer restore()
			var extraRestore func()
			defer func() {
				if extraRestore != nil {
					extraRestore()
				}
			}()
			switch mode {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "foreign":
				var changed freshProductSessionSuccessor
				if json.Unmarshal(raw, &changed) != nil {
					t.Fatal("original bridge JSON")
				}
				changed.ParentSHA256 = strings.Repeat("f", 64)
				if err := os.WriteFile(path, mustProviderRecoveryJSON(changed), 0600); err != nil {
					t.Fatal(err)
				}
			case "current-pending":
				entry := createOpenBaoACKCompletionEntry(f.e.Journal, "service-session-batches-"+review)
				if entry == nil {
					t.Fatal("real completed batch entry missing")
				}
				saved := *entry
				entry.State = "pending"
				entry.Receipt = nil
				entry.CompletedAt = time.Time{}
				extraRestore = func() { *entry = saved }
			case "credential-uid":
				observedPath := filepath.Join(f.store.Root, "deployment/create", f.e.Journal.PlanSHA256, "bootstrap/service/session-renewal", review, "observed.json")
				original, err := os.ReadFile(observedPath)
				if err != nil {
					t.Fatal(err)
				}
				extraRestore = func() {
					if err := os.WriteFile(observedPath, original, 0600); err != nil {
						t.Fatal(err)
					}
				}
				var observed createServiceSessionObserved
				if json.Unmarshal(original, &observed) != nil {
					t.Fatal("native observed JSON")
				}
				ns := q.Renewal.RenewedIntent.Namespaces[0]
				observed.UIDs[ns] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
				observed.Cleanup.SecretUID = observed.UIDs[ns]
				if err := os.WriteFile(observedPath, mustProviderRecoveryJSON(observed), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := readEffectiveFreshProductInstalled(f.store, f.e, f.l); err == nil {
				t.Fatal("changed successor binding granted current-session authority", mode)
			}
		})
	}
	if _, err := readEffectiveFreshProductInstalled(f.store, f.e, f.l); err != nil {
		t.Fatal("restored genuine completed successor", err)
	}
}

func (j *freshProductTerminalJoinedScenario) configureOriginalCustody(t *testing.T, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, binding *environmentCustodyBinding) {
	t.Helper()
	authorizations, err := environmentCustodyAuthorizations(cfg, *binding)
	if err != nil {
		t.Fatal(err)
	}
	var scopes []deploymentplan.Object
	for _, auth := range authorizations {
		scopes = append(scopes, deploymentplan.Object{"role": auth.role, "policy": auth.policy})

	}
	dir := t.TempDir()
	for name, raw := range map[string][]byte{"scopes": mustProviderRecoveryJSON(scopes)} {
		if err := os.WriteFile(filepath.Join(dir, name), raw, 0600); err != nil {
			t.Fatal(err)
		}
	}
	tool := filepath.Join(dir, "kubectl")
	protocol := `#!/usr/bin/env python3
import hashlib,json,os,re,sys
a=sys.argv[1:]
if len(a)<4 or a[0]!='--kubeconfig': sys.exit(71)
with open(a[1],'rb') as f:
 if hashlib.sha256(f.read()).hexdigest()!=os.environ['RTK_JOINED_CUSTODY_KUBE_SHA']: sys.exit(72)
with open(os.environ['RTK_JOINED_CUSTODY_TOKEN'],'rb') as f: token=f.read().strip()
if sys.stdin.buffer.read().strip()!=token: sys.exit(73)
script=a[-1];root=os.environ['RTK_JOINED_CUSTODY_DIR'];mount=os.environ['RTK_JOINED_CUSTODY_MOUNT'];statefile=root+'/observed.json'
state=None
if os.path.exists(statefile):
 with open(statefile) as f: state=json.load(f)
if 'bao secrets list -format=json' in script:
 print(json.dumps({mount+'/':{'type':'kv','options':{'version':'2'}}} if state else {}))
elif 'bao policy write ' in script:
 with open(root+'/scopes') as f: scopes={v['role']:v['policy'] for v in json.load(f)}
 if script.count('bao policy write ')!=len(scopes) or ('bao write '+mount+'/config cas_required=true') not in script: sys.exit(74)
 if state is None and ('bao secrets enable -path='+mount+' -version=2 kv') not in script: sys.exit(75)
 entries=re.findall(r"bao policy write ([a-zA-Z0-9_-]+) - >/dev/null <<'RTK_CUSTODY_POLICY'\n(.*?)RTK_CUSTODY_POLICY\nbao write auth/kubernetes/role/([a-zA-Z0-9_-]+) - >/dev/null <<'RTK_CUSTODY_ROLE'\n(.*?)\nRTK_CUSTODY_ROLE",script,re.S)
 if len(entries)!=len(scopes): sys.exit(76)
 observed={'cas_required':True,'policies':{},'roles':{}}
 for policy,text,role,body in entries:
  if policy!=role or policy not in scopes or text!=scopes[policy] or policy in observed['roles']: sys.exit(77)
  observed['policies'][policy]=text;observed['roles'][role]=json.loads(body)
 with open(statefile+'.tmp','w') as f: json.dump(observed,f)
 os.chmod(statefile+'.tmp',0o600);os.replace(statefile+'.tmp',statefile)
elif ('bao read -format=json '+mount+'/config') in script and state is not None:
 for line in script.splitlines():
  prefix='bao read -format=json '
  if not line.startswith(prefix): continue
  path=line[len(prefix):]
  if path==mount+'/config': data={'cas_required':state['cas_required']}
  elif path.startswith('sys/policies/acl/'): data={'policy':state['policies'][path[len('sys/policies/acl/'):]]}
  elif path.startswith('auth/kubernetes/role/'): data=state['roles'][path[len('auth/kubernetes/role/'):]]
  else: sys.exit(78)
  print(json.dumps({'data':data}))
else: sys.exit(79)
`
	if err := os.WriteFile(tool, []byte(protocol), 0700); err != nil {
		t.Fatal(err)
	}
	previous := os.Getenv("RTK_CLOUD_KUBECTL")
	t.Setenv("RTK_JOINED_CUSTODY_DIR", dir)
	t.Setenv("RTK_JOINED_CUSTODY_TOKEN", runtime.OpenBao.RootTokenPath)
	t.Setenv("RTK_JOINED_CUSTODY_MOUNT", cfg.Values["PKI_STATE_KV_MOUNT"])
	t.Setenv("RTK_JOINED_CUSTODY_KUBE_SHA", environmentPreflightBytesSHA256(runtime.Backend.KubeconfigContent))
	t.Setenv("RTK_CLOUD_KUBECTL", tool)
	defer func() {
		if err := os.Setenv("RTK_CLOUD_KUBECTL", previous); err != nil {
			t.Error(err)
		}
	}()
	if err := configureEnvironmentCreateCustody(t.Context(), cfg, runtime, binding); err != nil {
		t.Fatal("actual original native custody configuration", err)
	}
}

func (j *freshProductTerminalJoinedScenario) productEnrollmentSubject(t *testing.T, f *productProtocolFixture, object deploymentplan.Object, accepted bool) (string, string) {
	t.Helper()
	if object["kind"] != "Deployment" || deploymentplan.Map(object["metadata"])["namespace"] != f.p.Stack+"-video-cloud" {
		return "", ""
	}
	id := deploymentplan.ID(object)
	for _, output := range f.l.Outputs {
		role := strings.TrimPrefix(output.Subject, "service:")
		name := deploymentplan.String(deploymentplan.Map(object["metadata"])["name"])
		if !strings.HasPrefix(name, "pki-product-"+role+"-") {
			continue
		}
		for _, entry := range f.e.Journal.Entries {
			if !strings.HasPrefix(entry.Key, "kubernetes/product-identities-") || !strings.HasSuffix(entry.Key, "-enroll-"+role) {
				continue
			}
			stage := strings.TrimPrefix(entry.Key, "kubernetes/")
			plan, err := readEnvironmentCreateStageInput(f.store, f.e, stage)
			if err != nil || plan == nil {
				t.Fatal("original product native execution input missing", err)
			}
			for _, resource := range plan.Resources {
				if resource.ID != id {
					continue
				}
				containers := certIssuerObjectList(environmentPKIPod(object)["containers"])
				if len(containers) != 1 || !deploymentplan.Matches(resource.Desired, object) || !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])) {
					t.Fatal("external native product owner differs from frozen input")
				}
				container := deploymentplan.Map(containers[0])
				expected := []any{"/bin/sh", "-ec", "/app/serviceidentity-bootstrap " + output.Subject + " && exec sleep 3600"}
				if container["name"] != "enroll" || deploymentplan.Digest(container["command"]) != deploymentplan.Digest(expected) {
					t.Fatal("external native product command changed its admitted subject")
				}
				if accepted {
					journal, exists, err := readEnvironmentCreateStageProgress(f.store, f.e, stage, plan)
					if err != nil || !exists || !journal.Accepted || journal.Owners[id] != deploymentplan.Map(object["metadata"])["uid"] {
						t.Fatal("native product read lacks its accepted owner", err)
					}
				}
				return output.Subject, role
			}
		}
	}
	return "", ""
}

func (j *freshProductTerminalJoinedScenario) productStateBoundary(t *testing.T, f *productProtocolFixture, args []string, input []byte) ([]byte, int, bool) {
	i, n := slices.Index(args, "exec"), slices.Index(args, "--namespace")
	if i < 0 || n < 0 || n+1 >= len(args) || len(args[i:]) != 7 || args[i+2] != "--container" || args[i+3] != "enroll" || args[i+4] != "--" || args[i+5] != "cat" || !strings.HasPrefix(args[i+1], "deployment/pki-product-") {
		return nil, 0, false
	}
	if len(input) != 0 || args[n+1] != f.p.Stack+"-video-cloud" {
		return nil, 1, true
	}
	id := "Deployment/" + args[n+1] + "/" + strings.TrimPrefix(args[i+1], "deployment/")
	var owner deploymentplan.Object
	if readExecutionJSON(filepath.Join(os.Getenv("RTK_LATE_NATIVE_OBJECTS"), strings.ReplaceAll(id, "/", "_")+".json"), &owner) != nil {
		return nil, 1, true
	}
	subject, role := j.productEnrollmentSubject(t, f, owner, true)
	if subject == "" || args[i+6] != environmentCreateServiceOwnerIdentityPath(role, "client.json") || len(f.states[subject]) == 0 {
		return nil, 1, true
	}
	return f.states[subject], 0, true
}

func (j *freshProductTerminalJoinedScenario) completedJobWaitBoundary(t *testing.T, f *productProtocolFixture, args []string) ([]byte, int, bool) {
	i, n := slices.Index(args, "wait"), slices.Index(args, "--namespace")
	if i < 0 || n < 0 || n+1 >= len(args) {
		return nil, 0, false
	}
	name := ""
	for _, arg := range args[i+1:] {
		if strings.HasPrefix(arg, "Job/") {
			if name != "" {
				return nil, 1, true
			}
			name = strings.TrimPrefix(arg, "Job/")
		}
	}
	if name == "" || strings.ContainsAny(name, `/\`) {
		return nil, 0, false
	}
	id := "Job/" + args[n+1] + "/" + name
	root := os.Getenv("RTK_LATE_NATIVE_OBJECTS")
	path := filepath.Join(root, strings.ReplaceAll(id, "/", "_")+".json")
	if slices.Contains(args, "--for=delete") {
		if j.deletedInspections[id] != "" {
			if _, err := os.Lstat(path); os.IsNotExist(err) {
				return nil, 0, true
			}
		}
		return nil, 0, false
	}
	if !slices.Contains(args, "--for=condition=complete") {
		return nil, 0, false
	}
	if _, err := os.Stat(filepath.Join(root, "fail-ready")); err == nil {
		return nil, 0, false
	}
	var job deploymentplan.Object
	if readExecutionJSON(path, &job) != nil || deploymentplan.ID(job) != id {
		return nil, 0, false
	}
	uid := deploymentplan.String(deploymentplan.Map(job["metadata"])["uid"])
	known := j.custodyPods[uid] != nil
	if j.renewal != nil && len(j.renewal.Renewal.Candidate.Resources) == 1 {
		r := j.renewal.Renewal.Candidate.Resources[0]
		if r.ID == id && deploymentplan.Matches(r.Desired, job) {
			journal, exists, err := j.renewal.Renewal.Candidate.ReadExecution(f.store.Root)
			known = err == nil && exists && journal.Owners[id] == uid
		}
	}
	if !known || deploymentplan.Map(job["metadata"])["deletionTimestamp"] != nil || !environmentBootstrapOwnerUUID.MatchString(uid) || deploymentplan.Map(job["status"])["succeeded"] != float64(1) {
		return nil, 0, false
	}
	complete := false
	for _, value := range certIssuerObjectList(deploymentplan.Map(job["status"])["conditions"]) {
		condition := deploymentplan.Map(value)
		if condition["type"] == "Complete" && condition["status"] == "True" {
			complete = true
		}
		if (condition["type"] == "Failed" || condition["type"] == "FailureTarget") && condition["status"] == "True" {
			return nil, 0, false
		}
	}
	if !complete {
		return nil, 0, false
	}
	files, err := filepath.Glob(filepath.Join(root, "Pod_"+args[n+1]+"_*.json"))
	if err != nil {
		return nil, 1, true
	}
	matched := 0
	for _, file := range files {
		var pod deploymentplan.Object
		if readExecutionJSON(file, &pod) != nil {
			continue
		}
		owned := false
		for _, ref := range certIssuerObjectList(deploymentplan.Map(pod["metadata"])["ownerReferences"]) {
			o := deploymentplan.Map(ref)
			owned = owned || o["kind"] == "Job" && o["name"] == name && o["uid"] == uid && o["controller"] == true
		}
		if !owned {
			continue
		}
		meta := deploymentplan.Map(pod["metadata"])
		if meta["namespace"] != args[n+1] || meta["deletionTimestamp"] != nil || !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(meta["uid"])) {
			return nil, 0, false
		}
		if deploymentplan.Map(pod["status"])["phase"] != "Succeeded" || !deploymentplan.Matches(deploymentplan.Object{"spec": environmentPKIPod(job)}, deploymentplan.Object{"spec": pod["spec"]}) {
			return nil, 0, false
		}
		imageEvidence := func(_ context.Context, image string) (map[string]bool, error) {
			digest := environmentPreflightBytesSHA256(j.ociManifest)
			if !strings.HasSuffix(image, "@sha256:"+digest) {
				return nil, errors.New("unknown isolated OCI artifact")
			}
			return map[string]bool{"sha256:" + digest: true}, nil
		}
		if verifyEnvironmentPodContainers(t.Context(), job, pod, map[string]map[string]bool{}, imageEvidence) != nil {
			return nil, 0, false
		}
		matched++
	}
	if matched != 1 {
		return nil, 0, false
	}
	return nil, 0, true
}

// Observe the same original provider references through the common adoption
// reader, then render only the five consumers owned by this workflow boundary.
// Full environment create separately qualifies and installs ingress.
func joinedMaintainedConsumerCatalog(t *testing.T, cfg deploymentConfig, parent environmentCreatePlan, runtime environmentCreateBootstrapRuntime, inventory []deploymentplan.Object) []deploymentplan.Definition {
	t.Helper()
	renderEnv, renderBindings, renderRelease := completeEnvironmentRenderFixture(cfg.Environment)
	renderEnv = appendMap(renderEnv, cfg.Values)
	renderEnv["CLOUD_ENV_NAME"] = cfg.Environment
	for role, selected := range parent.Release.Images {
		if old, ok := renderRelease.Images[role]; ok {
			old.Reference = selected.Reference
			renderRelease.Images[role] = old
			renderEnv[role] = selected.Reference
		}
	}
	ns := parent.Stack + "-secrets"
	var provider []deploymentplan.Object
	for _, object := range inventory {
		if deploymentplan.ID(object) == "StatefulSet/"+ns+"/openbao" || deploymentplan.ID(object) == "ConfigMap/"+ns+"/openbao-config" {
			provider = append(provider, object)
		}
	}
	observed, err := observeEnvironmentBindings(cfg.Environment, renderRelease, provider)
	if err != nil || len(observed.Pods) != 1 || observed.Pods[0].Component != "managed-openbao" || observed.Pods[0].OpenBao == nil {
		t.Fatal("same original observed OpenBao provider binding", err)
	}
	replaced := false
	for i := range renderBindings.Pods {
		if renderBindings.Pods[i].Component == "managed-openbao" {
			if replaced {
				t.Fatal("ambiguous original OpenBao render binding")
			}
			renderBindings.Pods[i] = observed.Pods[0]
			replaced = true
		}
	}
	if !replaced {
		t.Fatal("original OpenBao render binding missing")
	}
	for _, object := range inventory {
		meta := deploymentplan.Map(object["metadata"])
		if uid := deploymentplan.String(meta["uid"]); uid != "" {
			renderBindings.Owners[deploymentplan.ID(object)] = uid
		}
	}
	renderBindings.Custody = runtime.Custody
	ctx := withEnvironmentCreateBootstrapRuntime(t.Context(), runtime)
	if err := applyEnvironmentFreshProductBindings(ctx, cfg, runtime.Execution, &renderBindings); err != nil {
		t.Fatal("original/effective fresh bindings before consumer render", err)
	}
	_, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		t.Fatal("original selected consumer operator", err)
	}
	secrets, err := environmentRuntimeSecretSnapshot(runtime.Execution.Store)
	if err != nil {
		t.Fatal("original consumer runtime snapshot", err)
	}
	snapshot := environmentDeploymentSnapshot{Operator: operator, Secrets: secrets}
	if err := readEnvironmentCreateStorageRenderSnapshot(cfg, runtime.Execution.Store, &snapshot); err != nil {
		t.Fatal("original consumer storage observations", err)
	}
	renderCfg := cfg
	renderCfg.Values = renderEnv
	var definitions []deploymentplan.Definition
	err = withEnvironmentRenderInputs(renderCfg, runtime.Execution.Store, renderRelease, snapshot, func(values map[string]string) error {
		// These are the same five maintained manifests selected from the full
		// target. Service declarations supply normal startup derivation inputs;
		// they are not claimed installed and are not applied by this boundary.
		manifests := []string{}
		if lkeMQTTFoundationRegistrationEnabled(values) {
			manifests = append(manifests, lkeMQTTFoundationDeploymentManifest(values))
		}
		if lkeOTAServiceRegistrationEnabled(values) {
			manifests = append(manifests, lkeOTAServiceDeploymentManifest(values))
		}
		if lkeShadowWorkerRegistrationEnabled(values) {
			manifests = append(manifests, lkeShadowWorkerDeploymentManifest(values))
		}
		if lkeVideoStorageServiceRegistrationEnabled(values) {
			manifests = append(manifests, lkeVideoStorageServiceDeploymentManifest(values))
		}
		if lkeWebRTCServiceRegistrationEnabled(values) {
			manifests = append(manifests, lkeWebRTCServiceDeploymentManifest(values))
		}
		manifests = append(manifests, lkePostgresServiceManifest(values))
		for _, workload := range k8sWorkloads(values) {
			if workload.ServiceEnabled && (workload.Key == "account-manager" || workload.Key == "billing") {
				manifests = append(manifests, lkeServiceManifest(values, workload))
			}
		}
		var err error
		definitions, err = decodeEnvironmentCreateManifests(manifests...)
		if err != nil {
			return err
		}
		consumers := 0
		for _, d := range definitions {
			if d.Object["kind"] == "Deployment" {
				consumers++
			}
		}
		if consumers != len(parent.Bootstrap.ProductIdentities) {
			return errors.New("maintained consumer declarations differ from original outputs")
		}
		for _, d := range definitions {
			if err := applyEnvironmentPodBindings(d.Object, renderBindings, renderRelease); err != nil {
				return err
			}
			canonicalEnvironmentEmptyEnvLiterals(d.Object)
		}
		if err = bindEnvironmentNewStorageClasses(definitions, values["DURABLE_DATA_STORAGE_CLASS"], renderBindings.Owners); err != nil {
			return err
		}
		for _, d := range definitions {
			if err = applyEnvironmentMQTTRole(values, d.Object); err != nil {
				return err
			}
		}
		if err = deriveEnvironmentCoreMQTTTransport(values, definitions); err != nil {
			return err
		}
		if err = bindEnvironmentManagedMQTTConsumersForRender(definitions, renderBindings.ProductIdentities); err != nil {
			return err
		}
		if err = bindEnvironmentCurrentMQTTConsumers(definitions, renderBindings); err != nil {
			return err
		}
		if err = applyCoreIdentityPreparation(definitions, renderRelease.Images["LKE_ALPINE_IMAGE"].Reference); err != nil {
			return err
		}
		bindEnvironmentVideoStorageObjectStorage(definitions)
		if err = deriveEnvironmentOTAStartup(definitions); err != nil {
			return err
		}
		if err = deriveEnvironmentVideoStorageStartup(values, definitions); err != nil {
			return err
		}
		if err = pinEnvironmentResourceImages(definitions, renderRelease); err != nil {
			return err
		}
		if err = validateEnvironmentOTARegistrationRelease(definitions, renderRelease); err != nil {
			return err
		}
		definitions, err = projectEnvironmentCustodyResources(values, cfg.Environment, renderBindings.Custody, "", definitions)
		return err
	})
	if err != nil {
		t.Fatal("maintained original five-consumer renderer", err)
	}
	return definitions
}
