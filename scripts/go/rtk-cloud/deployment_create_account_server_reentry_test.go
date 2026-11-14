package main

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type currentAccountExportBoundary struct {
	objects  map[string]deploymentplan.Object
	material environmentCreateOwnedServerMaterial
	execs    int
	mode     string
}

func (f *currentAccountExportBoundary) command(args []string, _ []byte) ([]byte, int, bool) {
	emit := func(o any) ([]byte, int, bool) { return mustProviderRecoveryJSON(o), 0, true }
	ns := ""
	if i := slices.Index(args, "--namespace"); i >= 0 && i+1 < len(args) {
		ns = args[i+1]
	}
	if i := slices.Index(args, "get"); i >= 0 && i+1 < len(args) {
		kind := args[i+1]
		if kind == "pods" {
			selector := ""
			if j := slices.Index(args, "--selector"); j >= 0 && j+1 < len(args) {
				selector = args[j+1]
			}
			name := strings.TrimPrefix(selector, "app.kubernetes.io/name=")
			d := f.objects["Deployment/"+ns+"/"+name]
			if d == nil {
				return emit(deploymentplan.Object{"items": []any{}})
			}
			uid := deploymentplan.String(deploymentplan.Map(d["metadata"])["uid"])
			rsName, podName := name+"-native-rs", name+"-native-pod"
			rsUID, podUID := sessionRenewalUUID(uid, name, "rs"), sessionRenewalUUID(uid, name, "pod")
			rs := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": deploymentplan.Object{"namespace": ns, "name": rsName, "uid": rsUID, "ownerReferences": []any{deploymentplan.Object{"kind": "Deployment", "controller": true, "name": name, "uid": uid}}}}
			template := deploymentplan.Map(deploymentplan.Map(d["spec"])["template"])
			pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"namespace": ns, "name": podName, "uid": podUID, "labels": deploymentplan.Clone(deploymentplan.Map(deploymentplan.Map(template["metadata"])["labels"])), "ownerReferences": []any{deploymentplan.Object{"kind": "ReplicaSet", "controller": true, "name": rsName, "uid": rsUID}}}, "spec": deploymentplan.Clone(deploymentplan.Map(template["spec"])), "status": deploymentplan.Object{"phase": "Running"}}
			f.objects[deploymentplan.ID(rs)], f.objects[deploymentplan.ID(pod)] = rs, pod
			return emit(deploymentplan.Object{"apiVersion": "v1", "kind": "PodList", "items": []any{pod}})
		}
		if i+2 < len(args) {
			o := f.objects[kind+"/"+ns+"/"+args[i+2]]
			if o == nil {
				return nil, 0, true
			}
			return emit(o)
		}
	}
	if slices.Contains(args, "export-server") {
		c := slices.Index(args, "--container")
		if c < 0 || c+1 >= len(args) || (args[c+1] != "custody" && args[c+1] != "pkimanagement") {
			return nil, 91, true
		}
		f.execs++
		material := f.material
		switch f.mode {
		case "certificate":
			material.Certificate += "\n"
		case "key":
			material.Key = "private-wrong-native-key"
		case "environment":
			material.Environment = "staging"
		case "uncertain":
			return []byte("private-export-error"), 1, true
		case "owner-after":
			id := "Deployment/" + ns + "/account-manager"
			deploymentplan.Map(f.objects[id]["metadata"])["uid"] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
		}
		return emit(material)
	}
	return nil, 0, false
}

// Install only an external command boundary. The production export routine
// creates/accepts its original reader, records the actual native material hash,
// and retires that same owner through the caller's ordinary scoped engine.
func installCurrentAccountRegistrationExportCheckpoint(t *testing.T, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, material environmentCreateOwnedServerMaterial, core []deploymentplan.Definition, apply environmentCreateBootstrapApply, objects map[string]deploymentplan.Object, install func(func([]string, []byte) ([]byte, int, bool))) *currentAccountExportBoundary {
	t.Helper()
	boundary := &currentAccountExportBoundary{objects: objects, material: material}
	install(boundary.command)
	ns := p.Stack + "-account-manager"
	actual, err := readEnvironmentCreateOwnedServer(t.Context(), cfg, p, runtime, service, ns, "account-manager", "account-manager", "account-manager-service-state", environmentCreateServiceState, environmentCreateServiceState+"/private/client.json", environmentCreateServiceState+"/private/server.json", material.Name, apply)
	if err != nil || actual.Certificate != material.Certificate || actual.Key != material.Key {
		t.Fatal("original native account export", err)
	}
	name := "pki-export-account-manager-" + runtime.ExecutionSHA256[:12]
	if objects["Deployment/"+ns+"/"+name] != nil || createOpenBaoACKCompletionEntry(runtime.Execution.Journal, "retire-"+name) == nil {
		t.Fatal("native exporter was not genuinely retired")
	}
	return boundary
}

func currentAccountServerReentryFixture(t *testing.T, remote bool) (deploymentConfig, environmentCreateBootstrapPlan, environmentCreateBootstrapRuntime, environmentCreateServiceCeremony, *completedPublicationBackend, *currentAccountExportBoundary, environmentCreateBootstrapApply) {
	t.Helper()
	cfg, p, s, certificate, key, _, _ := accountRegistrationFixture(t)
	// This fixture re-renders the maintained core, so its registered public
	// documents must match the real signed chain returned by the TLS fixture.
	for _, issuer := range []*environmentCreateRegisteredIssuer{&s.Root, &s.Intermediate} {
		cert, err := parseFirstCertificatePEM([]byte(issuer.Certificate))
		if err != nil {
			t.Fatal(err)
		}
		doc := deploymentplan.Clone(issuer.Document)
		chain := issuer.Certificate
		if issuer.Kind == "intermediate" {
			chain += s.Root.Certificate
		}
		doc["certificate_pem"], doc["chain_pem"] = issuer.Certificate, chain
		doc["certificate_fingerprint_sha256"], doc["trust_bundle_version"] = environmentPreflightBytesSHA256(cert.Raw), environmentPreflightBytesSHA256([]byte(chain))
		doc["not_before"], doc["not_after"] = cert.NotBefore.Format(time.RFC3339), cert.NotAfter.Format(time.RFC3339)
		updated, err := environmentCreateRegisteredIssuerFromMap(doc, s.Root.RootFingerprint)
		if err != nil {
			t.Fatal(err)
		}
		*issuer = updated
	}
	_, _, runtime, _ := createServiceRecipeFixture(t, "dev")
	cfg.Values["PKI_STATE_BACKEND"] = "pvc"
	store, e, template, native := freshStageFixture(t)
	runtime.Execution, runtime.ExecutionSHA256 = e, e.Journal.PlanSHA256
	runtime.Backend = environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-account-server-kubeconfig")}
	if remote {
		cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
		var err error
		runtime.Custody, err = createEnvironmentCustodyBinding(cfg, p, e.Journal.PlanSHA256, strings.Repeat("a", 64))
		if err != nil {
			t.Fatal(err)
		}
		runtime.Custody.ServiceRootSHA256 = s.Root.RootFingerprint
	}
	b := &completedPublicationBackend{native}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	apply := environmentCreateBootstrapApply(func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		projected := defs
		var err error
		if remote {
			projected, err = projectEnvironmentCustodyResources(cfg.Values, cfg.Environment, runtime.Custody, "", defs)
			if err != nil {
				return err
			}
		}
		var live []deploymentplan.Object
		for _, o := range b.objects {
			live = append(live, o)
		}
		in := template.Inputs
		in.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, projected)
		plan, err := deploymentplan.Compile(in, projected, live)
		if err != nil {
			return err
		}
		return executeEnvironmentCreateStagePlan(ctx, store, e, stage, plan, b, check, check, check, func(context.Context) error { return nil }, io.Discard)
	})
	if !remote {
		if err := apply(t.Context(), "fixture-original-account-storage", []deploymentplan.Definition{environmentCreateServiceClaimDefinition(cfg, p.Stack+"-account-manager", "account-manager-service-state")}); err != nil {
			t.Fatal(err)
		}
	}
	core, err := renderEnvironmentCreateManagedServiceDefinitions(t.Context(), cfg, p, runtime, s)
	if err != nil {
		t.Fatal(err)
	}
	if err := apply(t.Context(), "managed-complete-issuer", core); err != nil {
		t.Fatal(err)
	}
	var hook func([]string, []byte) ([]byte, int, bool)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var request struct {
			Args  []string
			Input []byte
		}
		if json.NewDecoder(r.Body).Decode(&request) != nil {
			w.WriteHeader(500)
			return
		}
		raw, code, handled := hook(request.Args, request.Input)
		if !handled {
			code = 99
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"output": raw, "code": code})
	}))
	t.Cleanup(server.Close)
	bin := t.TempDir()
	script := `#!/usr/bin/env python3
import sys,json,urllib.request,os,base64
p=json.dumps({'Args':sys.argv[1:],'Input':base64.b64encode(sys.stdin.buffer.read()).decode()}).encode()
r=json.load(urllib.request.urlopen(urllib.request.Request(os.environ['RTK_CURRENT_ACCOUNT_BOUNDARY'],data=p)))
sys.stdout.buffer.write(base64.b64decode(r.get('output') or ''));sys.exit(r['code'])
`
	path := filepath.Join(bin, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	t.Setenv("RTK_CURRENT_ACCOUNT_BOUNDARY", server.URL)
	material := environmentCreateOwnedServerMaterial{Environment: cfg.Environment, Root: s.Root.RootFingerprint, Name: "account-manager." + p.Stack + "-account-manager.svc.cluster.local", Certificate: certificate, Key: key}
	boundary := installCurrentAccountRegistrationExportCheckpoint(t, cfg, p, runtime, s, material, core, apply, b.objects, func(h func([]string, []byte) ([]byte, int, bool)) { hook = h })
	// This focused adapter test does not claim a product protocol run. Its
	// ordinary accepted core/export/retirement are the authorities under test;
	// the genuine five-output program is exercised by the joint tail fixture.
	if err := e.BeginEntry("product-identities/fresh", "bootstrap", mustProviderRecoveryJSON(map[string]string{"fixture": "focused-native-export-reentry"}), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	return cfg, p, runtime, s, b, boundary, apply
}

func TestCurrentAccountServerReentryPreservesRetiredExporterAndSameKey(t *testing.T) {
	for _, remote := range []bool{false, true} {
		name := "pvc"
		if remote {
			name = "kv"
		}
		t.Run(name, func(t *testing.T) {
			cfg, p, runtime, s, b, boundary, apply := currentAccountServerReentryFixture(t, remote)
			before := createBootstrapStateSnapshot(t, runtime.Execution.Store.Root)
			writes := deploymentplan.Digest(b.writes)
			got, err := readEnvironmentCreateOwnedServer(t.Context(), cfg, p, runtime, s, p.Stack+"-account-manager", "account-manager", "account-manager", "account-manager-service-state", environmentCreateServiceState, environmentCreateServiceState+"/private/client.json", environmentCreateServiceState+"/private/server.json", boundary.material.Name, apply)
			if err != nil || got.Certificate != boundary.material.Certificate || got.Key != boundary.material.Key {
				t.Fatal("retired native exporter cannot recover same serving identity", err)
			}
			if _, err := tls.X509KeyPair([]byte(got.Certificate), []byte(got.Key)); err != nil {
				t.Fatal(err)
			}
			if len(got.registrationCore) == 0 {
				t.Fatal("recovered export lost its same-call authenticated prerequisite facts")
			}
			if deploymentplan.Digest(jsonRawObject(mustProviderRecoveryJSON(got))) != deploymentplan.Digest(jsonRawObject(mustProviderRecoveryJSON(boundary.material))) {
				t.Fatal("runtime prerequisite reuse changed serialized native material")
			}
			if remote {
				custody, _, err := acceptedCreateStage(runtime.Execution.Store, runtime.Execution, "kubernetes/server-custody-account-manager")
				if err != nil || len(custody.Resources) != 10 {
					t.Fatal("KV fixture must exercise all ten projected exporter/auth resources", err)
				}
			}
			if writes != deploymentplan.Digest(b.writes) || boundary.execs != 2 {
				t.Fatal("recovery recreated exporter or replayed native writes", boundary.execs)
			}
			assertRecoveryContractReadOnly(t, runtime.Execution.Store, before)
		})
	}
}

func TestCurrentAccountServerReentryRefusesUnrecordedIdentityAndOwner(t *testing.T) {
	cfg, p, runtime, s, b, boundary, apply := currentAccountServerReentryFixture(t, true)
	for _, mode := range []string{"certificate", "key", "environment", "uncertain", "owner-after", "foreign-owner", "foreign-custody"} {
		t.Run(mode, func(t *testing.T) {
			snapshot := map[string]deploymentplan.Object{}
			for id, o := range b.objects {
				snapshot[id] = deploymentplan.Clone(o)
			}
			old := runtime.Custody
			boundary.mode = mode
			if mode == "foreign-owner" {
				deploymentplan.Map(b.objects["Deployment/"+p.Stack+"-account-manager/account-manager"]["metadata"])["uid"] = "ffffffff-ffff-4fff-8fff-ffffffffffff"
			}
			if mode == "foreign-custody" {
				copy := *runtime.Custody
				copy.Generation = strings.Repeat("e", 64)
				runtime.Custody = &copy
			}
			_, err := readEnvironmentCreateOwnedServer(t.Context(), cfg, p, runtime, s, p.Stack+"-account-manager", "account-manager", "account-manager", "account-manager-service-state", environmentCreateServiceState, environmentCreateServiceState+"/private/client.json", environmentCreateServiceState+"/private/server.json", boundary.material.Name, apply)
			if err == nil || strings.Contains(err.Error(), "private-export-error") || strings.Contains(err.Error(), "private-wrong-native-key") {
				t.Fatal("changed identity/owner accepted or native private error leaked", err)
			}
			runtime.Custody = old
			boundary.mode = ""
			clear(b.objects)
			for id, o := range snapshot {
				b.objects[id] = o
			}
		})
	}
}

func TestCurrentAccountRegistrationFirstKVPrerequisitesUseProjectedOwnedScope(t *testing.T) {
	cfg, p, runtime, s, b, _, _ := currentAccountServerReentryFixture(t, true)
	raw, err := renderEnvironmentCreateManagedServiceDefinitions(t.Context(), cfg, p, runtime, s)
	if err != nil {
		t.Fatal(err)
	}
	rawClaim := false
	for _, d := range raw {
		for _, value := range certIssuerObjectList(environmentPKIPod(d.Object)["volumes"]) {
			rawClaim = rawClaim || deploymentplan.Map(value)["persistentVolumeClaim"] != nil
		}
	}
	if !rawClaim {
		t.Fatal("first-export input must remain the ordinary raw renderer graph")
	}
	expected, _, err := acceptedCurrentCoreStage(runtime.Execution.Store, runtime.Execution)
	if err != nil {
		t.Fatal(err)
	}
	got, err := currentEnvironmentCreateAccountRegistrationCoreWithBackend(t.Context(), cfg, runtime, raw, b)
	if err != nil {
		t.Fatal("raw first-export graph cannot resolve original projected prerequisites", err)
	}
	if len(got) != len(expected.Resources) {
		t.Fatal("projected native dependency scope was dropped")
	}
	for _, d := range got {
		if d.Object["kind"] == "PersistentVolumeClaim" {
			t.Fatal("raw physical claim reintroduced into logical KV target")
		}
		for _, value := range certIssuerObjectList(environmentPKIPod(d.Object)["volumes"]) {
			if deploymentplan.Map(value)["persistentVolumeClaim"] != nil {
				t.Fatal("raw physical claim reference reintroduced into logical KV owner")
			}
		}
	}
}

// Changing only the retirement parent state is an explicit negative mutation:
// it must not select the new completed-only observer or recreate that owner.
func TestCurrentAccountServerPendingRetirementKeepsOriginalDispatch(t *testing.T) {
	cfg, p, runtime, s, b, boundary, apply := currentAccountServerReentryFixture(t, true)
	retired := createOpenBaoACKCompletionEntry(runtime.Execution.Journal, "retire-pki-export-account-manager-"+runtime.ExecutionSHA256[:12])
	if retired == nil {
		t.Fatal("missing genuine completed retirement")
	}
	original := *retired
	for _, state := range []string{"pending", "allocated"} {
		t.Run(state, func(t *testing.T) {
			retired.State, retired.CompletedAt = state, time.Time{}
			if state == "pending" {
				retired.Receipt = nil
			} else {
				retired.Receipt = original.Receipt
			}
			writes, execs := deploymentplan.Digest(b.writes), boundary.execs
			_, err := readEnvironmentCreateOwnedServer(t.Context(), cfg, p, runtime, s, p.Stack+"-account-manager", "account-manager", "account-manager", "account-manager-service-state", environmentCreateServiceState, environmentCreateServiceState+"/private/client.json", environmentCreateServiceState+"/private/server.json", boundary.material.Name, apply)
			if err == nil || strings.Contains(err.Error(), "completed account serving export differs") {
				t.Fatal("noncompleted retirement selected completed-only recovery", err)
			}
			if writes != deploymentplan.Digest(b.writes) || execs != boundary.execs {
				t.Fatal("noncompleted retirement recreated or exported an uncertain owner")
			}
			*retired = original
		})
	}
}
