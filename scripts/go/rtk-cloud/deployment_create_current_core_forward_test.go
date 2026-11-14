package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type currentCoreForwardTestScenario struct {
	joined                  *freshProductTerminalJoinedScenario
	cfg                     deploymentConfig
	bootstrap               environmentCreateBootstrapPlan
	runtime                 environmentCreateBootstrapRuntime
	roots, issuers          map[string]environmentCreateRegisteredIssuer
	cleanup                 environmentBootstrapCleanupBinding
	rootCRLSecret, rootCRL  string
	binding                 *environmentCustodyBinding
	namespaces              []string
	initial                 *deploymentplan.Plan
	source                  *deploymentplan.Plan
	sourceBytes, inputBytes []byte
	kubeconfig              []byte
}

type currentCoreStopBeforeAccount struct {
	*sessionNativeFaultBackend
	controller, issuer, account string
}

func (b currentCoreStopBeforeAccount) Read(ctx context.Context, id string) (deploymentplan.Object, error) {
	if id == b.account && b.writes[b.controller] > 1 && b.writes[b.issuer] > 1 {
		return nil, errors.New("isolated prerequisite failure before original account update")
	}
	return b.sessionNativeFaultBackend.Read(ctx, id)
}

func newCurrentCoreForwardTestScenario(t *testing.T) (*currentCoreForwardTestScenario, environmentCreateServiceCeremony) {
	t.Helper()
	cfg, p, _, runtime, service, roots, issuers, _ := createApplicationFinalizeFixture(t, "dev")
	enableProductIdentityFixture(&cfg)
	cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
	products, err := environmentCreateProductIdentityIntent(cfg)
	if err != nil {
		t.Fatal(err)
	}
	p.ProductIdentities = products
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil {
		t.Fatal(err)
	}
	for _, output := range products {
		subjects = append(subjects, output.Subject)
	}
	p.Domains[0].ServiceClientIDs = append(subjects, "service:deployment-bootstrap")
	slices.Sort(p.Domains[0].ServiceClientIDs)
	for i := range p.ProductIdentities {
		p.ProductIdentities[i].PolicySHA256 = deploymentplan.Digest(p.Domains[0])
	}
	scenario := &currentCoreForwardTestScenario{cfg: cfg, bootstrap: p, runtime: runtime, roots: roots, issuers: issuers, cleanup: service.CleanupBinding, rootCRLSecret: service.RootCRLSecret, rootCRL: service.RootCRLPEM}
	// Generate this helper's isolated root before the native parent selects its
	// final store. No fixture constructor may switch roots during continuation.
	scenario.kubeconfig, err = yaml.Marshal(frozenKubernetesFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, d := range scenario.render(t, service, service.BootstrapSessionID) {
		ns := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["namespace"])
		if ns != "" && !seen[ns] {
			scenario.namespaces = append(scenario.namespaces, ns)
			seen[ns] = true
		}
	}
	slices.Sort(scenario.namespaces)
	return scenario, service
}

type currentCoreStopMemory struct {
	freshStageBackend
	controller, issuer, account string
}

func (b currentCoreStopMemory) Read(ctx context.Context, id string) (deploymentplan.Object, error) {
	if id == b.account && b.writes[b.controller] > 1 && b.writes[b.issuer] > 1 {
		return nil, errors.New("isolated original next-resource prerequisite failure")
	}
	return b.freshStageBackend.Read(ctx, id)
}

func currentCoreMaintainedWrapperPolicy(t *testing.T, cfg *deploymentConfig) {
	t.Helper()
	values, err := readStrictEnv(filepath.Join(mustWorkspaceRoot(t), "cloud_deploy/architectures/kubernetes/architecture.env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{deploymentRolloutTimeoutKey, "DEPLOYMENT_UPGRADE_WINDOW_SECONDS"} {
		cfg.Values[key] = values[key]
	}
	if _, err := environmentRolloutTimeout(cfg.Values); err != nil {
		t.Fatal("original maintained wrapper policy", err)
	}
}

func installCurrentCoreWrapperKubeconfig(t *testing.T, store secretStore, kubeconfig []byte) {
	t.Helper()
	// The shared fixture normally injects an in-memory sentinel. The public
	// wrapper reconstructs its backend from the canonical frozen kubeconfig.
	if err := store.write("kube/kubeconfig.yaml", kubeconfig, false); err != nil {
		t.Fatal(err)
	}
	protocolPath := filepath.Join(os.Getenv("RTK_LATE_NATIVE_OBJECTS"), "unified-kubectl.py")
	protocol, err := os.ReadFile(protocolPath)
	if err != nil {
		t.Fatal(err)
	}
	protocol = []byte(strings.Replace(string(protocol), "    if f.read() != b'isolated-native-kubeconfig': sys.exit(72)", "    identity = f.read()\n    if identity != b'isolated-native-kubeconfig' and not (b'token: private-frozen-token\\n' in identity and b'server: https://selected.example:6443\\n' in identity and b'current-context: selected\\n' in identity): sys.exit(72)", 1))
	if err := os.WriteFile(protocolPath, protocol, 0600); err != nil {
		t.Fatal(err)
	}
}

func TestCurrentCoreForwardCanonicalWrapperBackendFixture(t *testing.T) {
	kubeconfig, err := yaml.Marshal(frozenKubernetesFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	store, e, _, _ := freshStageFixture(t)
	policy := deploymentConfig{Environment: store.Environment, Values: map[string]string{"CLOUD_STACK_NAME": e.Journal.Stack}}
	currentCoreMaintainedWrapperPolicy(t, &policy)
	if _, err := environmentRolloutTimeout(policy.Values); err != nil {
		t.Fatal("original canonical wrapper timeout policy", err)
	}
	id := "ConfigMap/" + e.Journal.Stack + "-video-cloud/wrapper-observer"
	object := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "wrapper-observer", "namespace": e.Journal.Stack + "-video-cloud", "uid": "11111111-1111-4111-8111-111111111111"}, "data": deploymentplan.Object{"original": "preserved"}}
	native, _, _ := unifiedSessionNativeBackend(t, store, map[string]deploymentplan.Object{id: object}, nil)
	if _, err := native.Read(t.Context(), id); err != nil {
		t.Fatal("original isolated native identity", err)
	}
	installCurrentCoreWrapperKubeconfig(t, store, kubeconfig)
	raw, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := newEnvironmentKubernetesBackend(string(raw), store.Environment)
	if err != nil {
		t.Fatal("actual wrapper selected kubeconfig constructor", err)
	}
	if restored.Store.Root != store.Root || e.Check(t.Context()) != nil {
		t.Fatal("canonical wrapper changed original environment owner/store")
	}
	observed, err := restored.Read(t.Context(), id)
	if err != nil || deploymentplan.Digest(observed) != deploymentplan.Digest(object) {
		t.Fatal("actual wrapper did not retain isolated native identity", err)
	}
}

func TestCurrentCoreForwardCompilerKeepsOriginalKnownPrefix(t *testing.T) {
	f, service := newCurrentCoreForwardTestScenario(t)
	store, e, template, backend := freshStageFixture(t)
	parent := environmentCreatePlan{Environment: e.Journal.Environment, Stack: e.Journal.Stack, ConfigurationSHA256: template.ConfigurationSHA256}
	f.beforeSession(t, f.cfg, store, e, backend, nil, parent, service, service.BootstrapSessionID)
	j, _, err := f.source.ReadExecution(store.Root)
	if err != nil {
		t.Fatal(err)
	}
	var live []deploymentplan.Object
	var owners []activeTransportRenewalResource
	next := "66666666-6666-4666-8666-666666666666"
	for _, r := range f.source.Resources {
		o := deploymentplan.Clone(backend.objects[r.ID])
		if o == nil {
			t.Fatal("missing original owner", r.ID)
		}
		if o["kind"] == "Deployment" {
			if _, err := serviceSessionSIDSlots(o, service.BootstrapSessionID, next); err != nil {
				t.Fatal(err)
			}
		}
		// This compiler test supplies already authenticated current owner facts;
		// the genuine native test separately exercises the admitted session chain.
		backend.objects[r.ID] = o
		uid := deploymentplan.String(deploymentplan.Map(o["metadata"])["uid"])
		owners = append(owners, sessionRecordedOwnerBase(r.ID, uid, o))
		live = append(live, o)
	}
	current := service
	current.CleanupBinding.SessionID = next
	current.CleanupBinding.ConfigMapName = "pki-bootstrap-cleanup-binding-" + next[:12]
	current.CleanupBinding.SecretUID = "77777777-7777-4777-8777-777777777777"
	defs := f.render(t, current, next)
	candidate, err := compileCurrentCoreForwardRecipe(f.cfg, f.source, j, owners, current.CleanupBinding, next, defs, "current-core-forward-compiler")
	if err != nil {
		t.Fatal("original known prefix cannot compile exact current recipe", err)
	}
	if candidate == nil || len(candidate.Resources) != len(f.source.Resources)+1 {
		t.Fatal("forward lost original owners or current cleanup binding")
	}
	for _, resource := range candidate.Resources {
		if resource.Desired["kind"] == "Service" {
			for _, field := range []string{"clusterIP", "clusterIPs", "ipFamilies", "ipFamilyPolicy"} {
				if deploymentplan.Map(resource.Desired["spec"])[field] == nil {
					t.Fatal("native allocation lost", resource.ID, field)
				}
			}
		}
	}

	for _, mutation := range []string{"dependency-delete", "dependency-add", "body", "owner-uid"} {
		t.Run(mutation, func(t *testing.T) {
			changed := make([]deploymentplan.Definition, len(defs))
			for i, d := range defs {
				changed[i] = d
				changed[i].Object = deploymentplan.Clone(d.Object)
				changed[i].Dependencies = slices.Clone(d.Dependencies)
			}
			facts := slices.Clone(owners)
			for i := range changed {
				if deploymentplan.ID(changed[i].Object) != "Deployment/"+e.Journal.Stack+"-video-cloud/certissuer" {
					continue
				}
				switch mutation {
				case "dependency-delete":
					changed[i].Dependencies = nil
				case "dependency-add":
					changed[i].Dependencies = append(changed[i].Dependencies, "ConfigMap/"+e.Journal.Stack+"-video-cloud/"+current.CleanupBinding.ConfigMapName)
				case "body":
					deploymentplan.Map(changed[i].Object["spec"])["replicas"] = float64(99)
				case "owner-uid":
					facts[0].UID = "88888888-8888-4888-8888-888888888888"
				}
			}
			if _, err := compileCurrentCoreForwardRecipe(f.cfg, f.source, j, facts, current.CleanupBinding, next, changed, "current-core-forward-compiler"); err == nil {
				t.Fatal("foreign recipe/owner accepted", mutation)
			}
		})
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, "current-core-forward-compiler", candidate, backend, check, check, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, backend) }, func(context.Context) error { return nil }, io.Discard); err != nil {
		t.Fatal(err)
	}
	original, err := os.ReadFile(filepath.Join(store.Root, "deployment/rollback", f.source.SHA256(), "journal.json"))
	if err != nil || string(original) != string(f.sourceBytes) {
		t.Fatal("forward rewrote original failed source", err)
	}
}

func TestCurrentCoreForwardNativeIncompleteSourceAndCompletedSession(t *testing.T) {
	scenario, _ := newCurrentCoreForwardTestScenario(t)
	runSecondSessionCandidateNativeJournal(t, true, scenario)
}

func (f *currentCoreForwardTestScenario) registeredService(t *testing.T, service *environmentCreateServiceCeremony) {
	t.Helper()
	makeIssuer := func(i environmentCreateRegisteredIssuer, kind, parent string) environmentCreateRegisteredIssuer {
		cert, err := parseFirstCertificatePEM([]byte(i.Certificate))
		if err != nil {
			t.Fatal(err)
		}
		reference := "pki-issuers/service/" + i.ID + "/v1"
		document := deploymentplan.Object{"issuer_id": i.ID, "issuer_version": float64(1), "environment": "dev", "trust_domain": "service", "kind": kind, "status": "active", "signer_provider": "openbao", "signer_reference": reference, "certificate_pem": i.Certificate, "chain_pem": i.Certificate, "certificate_fingerprint_sha256": environmentPreflightBytesSHA256(cert.Raw), "trust_bundle_version": environmentPreflightBytesSHA256([]byte(i.Certificate)), "not_before": cert.NotBefore.Format(time.RFC3339), "not_after": cert.NotAfter.Format(time.RFC3339)}
		if parent != "" {
			document["parent_issuer_id"] = parent
		}
		out, err := environmentCreateRegisteredIssuerFromMap(document, service.Root.RootFingerprint)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	service.Root = makeIssuer(service.Root, "root", "")
	service.Intermediate = makeIssuer(service.Intermediate, "intermediate", service.Root.ID)
	if f.joined != nil {
		policy := findEnvironmentCreateDomainPolicy(f.bootstrap, "service")
		service.Intermediate.Document["service_client_ids"] = slices.Clone(policy.ServiceClientIDs)
		service.Intermediate.Document["server_dns_names"] = slices.Clone(policy.ServerDNSNames)
	}
}

func (f *currentCoreForwardTestScenario) render(t *testing.T, service environmentCreateServiceCeremony, sid string) []deploymentplan.Definition {
	t.Helper()
	service.RootCRLSecret, service.RootCRLPEM = f.rootCRLSecret, f.rootCRL
	service.BootstrapSessionID = sid
	service.BootstrapCredentialSecret = "service-bootstrap-" + sid
	service.CleanupBinding.SessionID = sid
	service.CleanupBinding.ConfigMapName = "pki-bootstrap-cleanup-binding-" + sid[:12]
	f.roots["service"], f.issuers["service"] = service.Root, service.Intermediate
	defs, err := remainingFreshProductIssuerDefinitions(t.Context(), f.cfg, f.bootstrap, f.runtime, service, f.roots, f.issuers)
	if err != nil {
		t.Fatal(err)
	}
	if f.binding == nil {
		binding, err := createEnvironmentCustodyBinding(f.cfg, f.bootstrap, strings.Repeat("a", 64), f.roots["openbao_tls"].RootFingerprint)
		if err != nil {
			t.Fatal(err)
		}
		binding.ServiceRootSHA256 = service.Root.RootFingerprint
		f.binding = binding
	}
	defs, err = projectEnvironmentCustodyResources(f.cfg.Values, f.cfg.Environment, f.binding, "", defs)
	if err != nil {
		t.Fatal(err)
	}
	return defs
}

func (f *currentCoreForwardTestScenario) beforeSession(t *testing.T, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, api deploymentplan.Backend, native *sessionNativeFaultBackend, parent environmentCreatePlan, service environmentCreateServiceCeremony, sid string) {
	t.Helper()
	for _, name := range []string{"emqx-dashboard-password", "mqtt-broker-auth", "emqx-handoff-api-key", "emqx-handoff-api-secret"} {
		if f.joined != nil {
			old, err := os.ReadFile(filepath.Join(store.Root, "runtime", name))
			if err == nil {
				if string(old) != "isolated-original-"+name {
					t.Fatal("joined original runtime input changed", name)
				}
				continue
			}
			if !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
		if err := store.write("runtime/"+name, []byte("isolated-original-"+name), false); err != nil {
			t.Fatal(err)
		}
	}
	f.runtime.Backend.Store = store
	f.binding = nil
	binding, err := createEnvironmentCustodyBinding(f.cfg, f.bootstrap, e.Journal.PlanSHA256, f.roots["openbao_tls"].RootFingerprint)
	if err != nil {
		t.Fatal(err)
	}
	binding.ServiceRootSHA256 = service.Root.RootFingerprint
	f.binding = binding
	defs := f.render(t, service, sid)
	var initial []deploymentplan.Definition
	serviceAllocation := 100
	for _, d := range defs {
		d.Object = deploymentplan.Clone(d.Object)
		if d.Object["kind"] == "Service" {
			serviceAllocation++
			spec := deploymentplan.Map(d.Object["spec"])
			ip := fmt.Sprintf("10.240.0.%d", serviceAllocation)
			spec["clusterIP"], spec["clusterIPs"], spec["ipFamilies"], spec["ipFamilyPolicy"] = ip, []any{ip}, []any{"IPv4"}, "SingleStack"
		}
		if d.Object["kind"] == "Deployment" {
			c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(d.Object)["containers"])[0])
			c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"FIXTURE_PRE_CORE": "original"}))
		}
		initial = append(initial, d)
	}
	input := deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: parent.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: environmentCreateStageBindingsSHA("fixture-current-core-original-owners", initial)}
	var initialLive []deploymentplan.Object
	if f.joined != nil {
		initialLive, err = native.Inventory(t.Context(), parent.Stack)
		if err != nil {
			t.Fatal(err)
		}
	}
	p, err := deploymentplan.Compile(input, initial, initialLive)
	if err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	if err = executeEnvironmentCreateStagePlan(t.Context(), store, e, "fixture-current-core-original-owners", p, api, check, check, check, func(context.Context) error { return nil }, io.Discard); err != nil {
		t.Fatal(err)
	}
	var live []deploymentplan.Object
	for _, r := range p.Resources {
		o, err := api.Read(t.Context(), r.ID)
		if err != nil {
			t.Fatal(err)
		}
		live = append(live, o)
	}
	input.BindingsSHA256 = environmentCreateStageBindingsSHA("managed-complete-issuer", defs)
	f.source, err = deploymentplan.Compile(input, defs, live)
	if err != nil {
		t.Fatal(err)
	}
	f.initial = p
	ns := e.Journal.Stack + "-video-cloud"
	var stopped deploymentplan.Backend = currentCoreStopBeforeAccount{native, "Deployment/" + ns + "/pki-controller", "Deployment/" + ns + "/certissuer", "Deployment/" + e.Journal.Stack + "-account-manager/account-manager"}
	account := "Deployment/" + e.Journal.Stack + "-account-manager/account-manager"
	if native == nil {
		stopped = currentCoreStopMemory{api.(freshStageBackend), "Deployment/" + ns + "/pki-controller", "Deployment/" + ns + "/certissuer", account}
	}
	if err = executeEnvironmentCreateStagePlan(t.Context(), store, e, "managed-complete-issuer", f.source, stopped, check, check, check, func(context.Context) error { return nil }, io.Discard); err == nil {
		t.Fatal("original core did not stop before account update")
	}
	j, ok, err := f.source.ReadExecution(store.Root)
	if err != nil || !ok || len(f.source.Resources) != 39 || len(j.Completed) != 38 || len(j.Owners) != 38 || j.InFlight != "" || j.Failed != account {
		t.Fatal("fixture did not reproduce real39/38 known prefix", len(f.source.Resources), len(j.Completed), j.Failed, err)
	}
	f.sourceBytes, err = os.ReadFile(filepath.Join(store.Root, "deployment/rollback", f.source.SHA256(), "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	f.inputBytes, err = os.ReadFile(environmentCreateStageInputPath(e, "managed-complete-issuer"))
	if err != nil {
		t.Fatal(err)
	}
}

type currentCoreInterruptWrittenController struct {
	*sessionNativeFaultBackend
	id      string
	stopped bool
}

func (b *currentCoreInterruptWrittenController) Wait(ctx context.Context, r deploymentplan.Resource) error {
	if err := b.sessionNativeFaultBackend.Wait(ctx, r); err != nil {
		return err
	}
	if r.ID == b.id && !b.stopped {
		b.stopped = true
		return errors.New("isolated successor interruption after known controller write")
	}
	return nil
}
func (f *currentCoreForwardTestScenario) afterSession(t *testing.T, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, api deploymentplan.Backend, native environmentKubernetesBackend, parent environmentCreatePlan, service environmentCreateServiceCeremony, head secondSessionAdmission, review string, state managedSessionState) {
	t.Helper()
	openBaoRoot := service.Root
	if f.joined != nil {
		openBaoRoot = f.roots["openbao_tls"]
	}
	r, err := restoreSecondSessionRuntime(t.Context(), store, e, head, review, service, openBaoRoot, parent)
	if err != nil {
		t.Fatal(err)
	}
	parent = *r.qualifiedPlan
	credential, journal, err := managedTransportPostAcceptedStage(store, e, "kubernetes/service-credential-secrets-renewal-"+review)
	if err != nil || credential == nil {
		t.Fatal("genuine completed credential child", err)
	}
	uids := map[string]string{}
	for _, ns := range head.Renewal.RenewedIntent.Namespaces {
		id := "Secret/" + ns + "/service-bootstrap-" + head.Renewal.RenewedIntent.SessionID
		if !environmentBootstrapOwnerUUID.MatchString(journal.Owners[id]) {
			t.Fatal("missing genuine accepted credential UID", id)
		}
		uids[ns] = journal.Owners[id]
	}
	// The existing fixture's Mint adapter uses real native children and isolated
	// registry reads. Persist exactly those observations as the real adapter does.
	nativeObservation := map[string]any{"session_id": state.Session, "issuer_id": state.Issuer, "root_sha256": state.Root, "bootstrap_fingerprint": state.Fingerprint, "status": state.Status, "subjects": state.Subjects, "expires_at": state.Expires}
	observed := createServiceSessionObserved{Cleanup: service.CleanupBinding, UIDs: uids, Reader: service.CustodyReader, Session: mustProviderRecoveryJSON(nativeObservation)}
	observed.Cleanup.SessionID = head.Renewal.RenewedIntent.SessionID
	observed.Cleanup.SecretUID = uids[service.BootstrapCredentialNamespace]
	observed.Cleanup.ConfigMapName = ""
	observedPath := filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-renewal", review, "observed.json")
	if f.joined == nil {
		if err := store.write(observedPath, mustProviderRecoveryJSON(observed), false); err != nil {
			t.Fatal(err)
		}
	} else {
		var original createServiceSessionObserved
		if readExecutionJSON(filepath.Join(store.Root, observedPath), &original) != nil || deploymentplan.Digest(original) != deploymentplan.Digest(observed) {
			t.Fatal("joined original native Mint observation changed")
		}
	}
	current, err := freshProductResumeCurrentCeremony(store, e, head, review, r.Ceremony)
	if err != nil {
		t.Fatal("genuine current completed ceremony", err)
	}
	defs := f.render(t, current, head.Renewal.RenewedIntent.SessionID)
	runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, Backend: native, ServiceSessionRecovery: r, ServiceCeremony: &current}
	if f.joined != nil {
		runtime.Custody = f.binding
		runtime.OpenBao = f.runtime.OpenBao
		runtime.OpenBao.RootTokenPath = filepath.Join(store.Root, "openbao/root-token")
		runtime.OpenBao.CACertPath, _, err = initialTransportAuthorityPath(store, e.Journal.PlanSHA256, "openbao")
		if err != nil {
			t.Fatal("joined original effective custody authority", err)
		}
	}
	ctx := withEnvironmentCreateBootstrapRuntime(t.Context(), runtime)
	q, qualification, err := prepareCurrentCoreForward(ctx, cfg, store, &parent, runtime, current, defs)
	if err != nil {
		t.Fatal("actual current-core qualification", err)
	}
	r.currentCore = q
	envelope, err := writeCurrentCoreForwardEnvelope(store, e.Journal.PlanSHA256, review, qualification, "")
	if err != nil {
		t.Fatal(err)
	}
	r.currentCore = nil
	if loaded, err := loadCurrentCoreForwardEnvelope(ctx, store, e, envelope, review, &parent, runtime); err != nil || loaded == nil {
		t.Fatal("exact reviewed envelope", err)
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	run := func(backend deploymentplan.Backend) func(context.Context, string, *deploymentplan.Plan) error {
		return func(ctx context.Context, stage string, plan *deploymentplan.Plan) error {
			return executeEnvironmentCreateStagePlan(ctx, store, e, stage, plan, backend, check, check, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, backend) }, func(context.Context) error { return nil }, io.Discard)
		}
	}
	controller := "Deployment/" + e.Journal.Stack + "-video-cloud/pki-controller"
	stop := &currentCoreInterruptWrittenController{sessionNativeFaultBackend: api.(*sessionNativeFaultBackend), id: controller}
	if err := executeCurrentCoreForward(ctx, cfg, store, &parent, runtime, defs, run(stop)); err == nil || !stop.stopped {
		t.Fatal("successor did not preserve a known-write interruption", err)
	}
	interrupted, exists, err := q.Candidate.ReadExecution(store.Root)
	if err != nil || !exists || interrupted.Accepted || interrupted.InFlight != "" || interrupted.Owners[controller] == "" {
		t.Fatal("successor interruption lost known native outcome", err)
	}
	recorded := maps.Clone(interrupted.Owners)
	// Preserve a genuine prior checker-bound proof, then refreeze the same
	// candidate/deadline through the ordinary selected-pointer path.
	older := *q
	older.Checker = strings.Repeat("f", 64)
	if err := writeCurrentCoreForwardQualification(store, &older); err != nil {
		t.Fatal(err)
	}
	if err := store.write(filepath.Join(currentCoreForwardBase(q.Parent), review, "selected.json"), mustProviderRecoveryJSON(map[string]string{"review_sha256": deploymentplan.Digest(older)}), true); err != nil {
		t.Fatal(err)
	}
	// Requalification must validate its own recorded successor rather than demand
	// that the already-written Controller still has its predecessor body.
	again, sameReview, err := prepareCurrentCoreForward(ctx, cfg, store, &parent, runtime, current, defs)
	if err != nil || sameReview != qualification || again.Candidate.SHA256() != q.Candidate.SHA256() {
		t.Fatal("partial successor cannot resume exact qualified plan", err)
	}
	if err := executeCurrentCoreForward(ctx, cfg, store, &parent, runtime, defs, run(api)); err != nil {
		t.Fatal("real native successor resume", err)
	}
	accepted, j, err := acceptedCurrentCoreStage(store, e)
	if err != nil || accepted.SHA256() != q.Candidate.SHA256() || !j.Accepted {
		t.Fatal("logical core did not resolve genuine accepted child", err)
	}
	for id, uid := range recorded {
		if j.Owners[id] != uid {
			t.Fatal("known native UID replaced", id)
		}
	}
	old, exists, err := readEnvironmentCreateStageProgress(store, e, "managed-complete-issuer", f.source)
	if err != nil || !exists || old.Accepted || old.Failed == "" {
		t.Fatal("original source was fabricated as accepted", err)
	}
	if f.joined != nil {
		original, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
		if err != nil || string(original) != string(f.kubeconfig) {
			t.Fatal("joined original admitted kubeconfig changed", err)
		}
	} else {
		installCurrentCoreWrapperKubeconfig(t, store, f.kubeconfig)
	}
	before := deploymentplan.Digest(e.Journal)
	if err := applyEnvironmentCreateStage(ctx, cfg, store, &parent, e, "managed-complete-issuer", defs, io.Discard); err != nil {
		t.Fatal("completed producer did not observe chronological owners", err)
	}
	if deploymentplan.Digest(e.Journal) != before {
		t.Fatal("completed observation changed parent/leases")
	}
	for _, uid := range []bool{false, true} {
		if _, err := verifyAcceptedStageAfterRecordedHistory(ctx, store, e, currentCoreOwnerDrift{Backend: api, id: controller, uid: uid}, "kubernetes/"+q.Stage); err == nil {
			t.Fatal("completed successor accepted foreign UID/body", uid)
		}
	}
	for _, mutation := range []string{"lineage", "source-owner"} {
		var changed currentCoreForwardQualification
		if decodeEnvironmentJSON(mustProviderRecoveryJSON(q), &changed) != nil {
			t.Fatal("copy qualification")
		}
		if mutation == "lineage" {
			changed.SessionReview = "short"
		} else {
			changed.Source.PlanSHA256 = strings.Repeat("e", 64)
		}
		if err := validateCurrentCoreForward(ctx, store, e, &changed, deploymentplan.Digest(changed)); err == nil {
			t.Fatal("foreign qualification lineage/owner accepted", mutation)
		}
	}

	original, err := os.ReadFile(filepath.Join(store.Root, "deployment/rollback", f.source.SHA256(), "journal.json"))
	input, inputErr := os.ReadFile(environmentCreateStageInputPath(e, "managed-complete-issuer"))
	if err != nil || inputErr != nil || string(original) != string(f.sourceBytes) || string(input) != string(f.inputBytes) {
		t.Fatal("successor changed original failed bytes/input", err, inputErr)
	}
	if f.joined != nil {
		runtime.Custody = f.binding
		runtime.OpenBao = f.runtime.OpenBao
		runtime.OpenBao.RootTokenPath = filepath.Join(store.Root, "openbao/root-token")
		runtime.TransportCAPEM, runtime.TransportRootSHA256 = f.runtime.TransportCAPEM, f.runtime.TransportRootSHA256
		runtime.TransportTrustConfigMap, runtime.TransportSecrets = f.runtime.TransportTrustConfigMap, f.runtime.TransportSecrets
		f.joined.run(t, f, cfg, store, e, api, native, parent, runtime, current, head, review, state)
	}
}

func TestCurrentCoreForwardFixtureFullRecipePrerequisites(t *testing.T) {
	scenario, s := newCurrentCoreForwardTestScenario(t)
	s.Root = environmentCreateRegisteredIssuer{ID: s.Root.ID, Certificate: s.Root.Certificate, RootFingerprint: s.Root.RootFingerprint}
	s.Intermediate = environmentCreateRegisteredIssuer{ID: s.Intermediate.ID, Certificate: s.Intermediate.Certificate}
	s.RootCRLPEM, s.RootCRLSecret = "", ""
	scenario.registeredService(t, &s)
	defs := scenario.render(t, s, s.BootstrapSessionID)
	if len(defs) != 39 {
		t.Fatal("maintained original core recipe shape", len(defs))
	}
	for _, d := range defs {
		ns := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["namespace"])
		if ns != "" && !slices.Contains(scenario.namespaces, ns) {
			t.Fatal("rendered owner namespace absent from original native fixture", ns)
		}
	}
	issuer := createRecipeObject(t, defs, "Deployment/"+scenario.bootstrap.Stack+"-video-cloud/certissuer")
	c := managedNamedField(environmentPKIPod(issuer)["containers"], "certissuer")
	if mqttFoundationEnv(c, "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID") != s.BootstrapSessionID {
		t.Fatal("current product provisioning was not rendered")
	}
}

type currentCoreOwnerDrift struct {
	deploymentplan.Backend
	id  string
	uid bool
}

func (b currentCoreOwnerDrift) Read(ctx context.Context, id string) (deploymentplan.Object, error) {
	o, err := b.Backend.Read(ctx, id)
	if err != nil || o == nil || id != b.id {
		return o, err
	}
	o = deploymentplan.Clone(o)
	if b.uid {
		deploymentplan.Map(o["metadata"])["uid"] = "88888888-8888-4888-8888-888888888888"
	} else {
		deploymentplan.Map(o["spec"])["replicas"] = float64(99)
	}
	return o, nil
}
func TestCurrentCoreForwardQualificationBoundedCodec(t *testing.T) {
	store, e, _, _ := freshStageFixture(t)
	q := &currentCoreForwardQualification{Schema: currentCoreForwardSchema, Parent: e.Journal.PlanSHA256, Source: e.Journal, Definitions: []deploymentplan.Definition{{Object: deploymentplan.Object{"fixture": strings.Repeat("x", 300<<10)}}}}
	raw := mustProviderRecoveryJSON(q)
	if len(raw) <= 256<<10 {
		t.Fatal("fixture is smaller than real parent proof boundary")
	}
	if err := writeCurrentCoreForwardQualification(store, q); err != nil {
		t.Fatal(err)
	}
	saved, err := readCurrentCoreForward(store, e, deploymentplan.Digest(q))
	if err != nil || deploymentplan.Digest(saved) != deploymentplan.Digest(q) {
		t.Fatal("large immutable qualification changed", err)
	}
	if err := writeCurrentCoreForwardQualification(store, q); err != nil {
		t.Fatal("byte-identical proof retry", err)
	}
}

var _ deploymentplan.LeasingBackend = (*currentCoreInterruptWrittenController)(nil)
var _ deploymentplan.ParentExecutionBackend = (*currentCoreInterruptWrittenController)(nil)

func TestCurrentCoreForwardCombinedApplicationReviewReportAndDispatch(t *testing.T) {
	store, e, plan, _ := freshStageFixture(t)
	original, application := strings.Repeat("a", 64), strings.Repeat("b", 64)
	q := &currentCoreForwardQualification{Schema: currentCoreForwardSchema, Parent: e.Journal.PlanSHA256, Candidate: plan}
	var report unifiedDeploymentReport
	now := time.Now().UTC()
	review, err := reportCurrentCoreApplicationReview(store, e.Journal.PlanSHA256, original, application, q, &report, store.Environment, now, now.Add(time.Hour))
	if err != nil || review == application || report.QualificationSHA256 != review || report.Plan.PlanSHA256 != review || report.Decision != "GO" {
		t.Fatal("application scope replaced combined full GO", err)
	}
	if len(report.Plan.Resources) != 6 {
		t.Fatal("combined review lost remaining original create scope")
	}
	var envelope currentCoreForwardEnvelope
	path := filepath.Join(store.Root, "deployment/create/issuer-enrollment-continuation", review, "current-core-forward.json")
	if err := readExecutionJSON(path, &envelope); err != nil || envelope.OriginalReview != original || envelope.ApplicationReview != application || envelope.Qualification != deploymentplan.Digest(q) {
		t.Fatal("combined envelope omitted selected authority", err)
	}
	dispatch, err := currentCoreApplicationDispatchReview(original, review, &envelope)
	if err != nil || dispatch != application {
		t.Fatal("application dispatcher did not retain its exact separate admission", err)
	}
	if original == dispatch || review == dispatch {
		t.Fatal("combined or original SHA substituted at application boundary")
	}
	if got, err := currentCoreApplicationDispatchReview(original, application, nil); err != nil || got != application {
		t.Fatal("legacy separate admission changed", err)
	}
	envelope.OriginalReview = strings.Repeat("f", 64)
	if _, err := currentCoreApplicationDispatchReview(original, review, &envelope); err == nil {
		t.Fatal("foreign original envelope admitted")
	}
	var legacy unifiedDeploymentReport
	if got, err := reportCurrentCoreApplicationReview(store, e.Journal.PlanSHA256, original, application, nil, &legacy, store.Environment, now, now.Add(time.Hour)); err != nil || got != application || legacy.QualificationSHA256 != application {
		t.Fatal("legacy application report changed", err)
	}
}

func TestCurrentCoreApplicationReportBindsWorkloadInEveryScope(t *testing.T) {
	for _, withCore := range []bool{false, true} {
		t.Run(fmt.Sprint(withCore), func(t *testing.T) {
			store, e, plan, _ := freshStageFixture(t)
			original, application, workload := strings.Repeat("a", 64), strings.Repeat("b", 64), strings.Repeat("c", 64)
			var q *currentCoreForwardQualification
			if withCore {
				q = &currentCoreForwardQualification{Schema: currentCoreForwardSchema, Parent: e.Journal.PlanSHA256, Candidate: plan}
			}
			var oldReport unifiedDeploymentReport
			old, err := reportCurrentCoreApplicationReview(store, e.Journal.PlanSHA256, original, application, q, &oldReport, store.Environment, time.Now(), time.Now().Add(time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			var report unifiedDeploymentReport
			review, err := reportCurrentCoreApplicationReview(store, e.Journal.PlanSHA256, original, application, q, &report, store.Environment, time.Now(), time.Now().Add(time.Hour), workload)
			if err != nil || review == old || report.QualificationSHA256 != review || report.Plan.PlanSHA256 != review {
				t.Fatal("full GO omitted workload binding", err)
			}
			var envelope currentCoreForwardEnvelope
			path := filepath.Join(store.Root, "deployment/create/issuer-enrollment-continuation", review, "current-core-forward.json")
			if err := readExecutionJSON(path, &envelope); err != nil || envelope.OriginalReview != original || envelope.ApplicationReview != application || envelope.WorkloadImageForward != workload {
				t.Fatal("application full GO lost exact workload review", err)
			}
			if withCore && envelope.Qualification != deploymentplan.Digest(q) {
				t.Fatal("full GO lost original core qualification")
			}
			if dispatch, err := currentCoreApplicationDispatchReview(original, review, &envelope); err != nil || dispatch != application {
				t.Fatal("full GO did not dispatch original application review", err)
			}
			for _, gate := range report.Gates {
				if gate.EvidenceSHA256 != review {
					t.Fatal("gate retained incomplete evidence SHA")
				}
			}
			var prior unifiedDeploymentReport
			if got, err := reportCurrentCoreApplicationReview(store, e.Journal.PlanSHA256, original, application, q, &prior, store.Environment, time.Now(), time.Now().Add(time.Hour)); err != nil || got != old {
				t.Fatal("previous immutable review changed", err)
			}
		})
	}
}
