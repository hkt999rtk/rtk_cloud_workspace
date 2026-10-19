package main

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type productSessionWorkloadFixture struct {
	cfg             deploymentConfig
	e               *environmentCreateExecution
	h               *createNormalTailHandoff
	l               *createNormalTailProductLifecycle
	renewal         *productSessionRenewal
	previous        *deploymentplan.Plan
	previousJournal deploymentplan.Journal
	dependencies    []deploymentplan.Resource
	b               *normalTailE2EBackend
}

// Real maintained component renderers, ordinary plan/journal execution, and an
// accepted controls child exercise this boundary. Native certificate issuance
// and session authorization are covered by the separate renewal protocol test.
func newProductSessionWorkloadFixture(t *testing.T) *productSessionWorkloadFixture {
	t.Helper()
	cfg, e, _ := normalTailProgramFixture(t)
	env, bindings, release := completeEnvironmentRenderFixture("dev")
	cfg.Values = appendMap(cfg.Values, env)
	cfg.Values["DEPLOYMENT_ADAPTER"] = "lke"
	enableProductIdentityFixture(&cfg)
	cfg.Values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] = "true"
	cfg.Values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT"] = "9444"
	oldID := "11111111-aaaa-4aaa-8aaa-111111111111"
	newID := "22222222-bbbb-4bbb-8bbb-222222222222"
	oldBinding := environmentBootstrapCleanupBinding{Version: 1, Environment: "dev", SessionID: oldID, NamespaceUID: "33333333-cccc-4ccc-8ccc-333333333333", SecretUID: "44444444-dddd-4ddd-8ddd-444444444444", PolicySHA256: deploymentplan.Digest("original exact cleanup policy"), KubernetesCA: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t)), ConfigMapName: "pki-bootstrap-cleanup-binding-" + oldID[:12]}
	bindings.BootstrapCleanup = &oldBinding
	bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: oldBinding.KubernetesCA}
	values := appendMap(cfg.Values, deploymentLegacyLKEValues(cfg.Values, cfg.Environment))
	for role, image := range release.Images {
		values[role] = image.Reference
	}
	restore := installAllCredentialEnvironment(values)
	defer restore()
	defs, err := decodeEnvironmentCreateManifests(lkeMQTTFoundationDeploymentManifest(values), lkeShadowWorkerDeploymentManifest(values), lkeOTAServiceDeploymentManifest(values), lkeVideoStorageServiceDeploymentManifest(values), lkeWebRTCServiceDeploymentManifest(values))
	if err != nil {
		t.Fatal("maintained product renderers", err)
	}
	var controller deploymentplan.Object
	for _, binding := range bindings.Pods {
		if binding.Component == "pki-controller" {
			controller, err = renderManagedEnvironmentComponent(values, binding)
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	if controller == nil {
		t.Fatal("maintained Controller missing")
	}
	if err = applyEnvironmentPodBindings(controller, bindings, release); err != nil {
		t.Fatal(err)
	}
	controls, err := applyEnvironmentBootstrapCleanup(deploymentEndpointValues(cfg), controller, oldBinding)
	if err != nil {
		t.Fatal(err)
	}
	defs = append(defs, controls...)
	defs = append(defs, deploymentplan.Definition{Object: controller, Phase: "workload"})
	outputs, err := environmentCreateProductIdentityIntent(cfg)
	if err != nil || len(outputs) != 5 {
		t.Fatal("five product contracts", err)
	}
	l := &createNormalTailProductLifecycle{ParentSHA256: e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(e.owner), Outputs: outputs}
	root := environmentCreateRegisteredIssuer{ID: "55555555-eeee-4eee-8eee-555555555555", RootFingerprint: deploymentplan.Digest("original Root")}
	ica := environmentCreateRegisteredIssuer{ID: "66666666-ffff-4fff-8fff-666666666666", RootFingerprint: root.RootFingerprint}
	old := environmentProductIdentitySession{Schema: productIdentitySessionSchema, ParentSHA256: l.ParentSHA256, OwnerSHA256: l.OwnerSHA256, Ceremony: environmentCreateServiceCeremony{Root: root, Intermediate: ica, BootstrapSessionID: oldID, CleanupBinding: oldBinding}}
	for _, o := range outputs {
		old.Outputs = append(old.Outputs, environmentProductIdentityReceipt{ResourceID: o.ID, Subject: o.Subject, SecretUID: "77777777-1111-4111-8111-777777777777", ClaimUID: "88888888-2222-4222-8222-888888888888", CertificateSHA256: deploymentplan.Digest(o.ID)})
	}
	l.Root = root
	l.Intermediate = ica
	old.PlanSHA256 = productIdentityScope(l)
	next := old
	next.Ceremony = old.Ceremony
	next.Ceremony.BootstrapSessionID = newID
	next.Ceremony.CleanupBinding = oldBinding
	next.Ceremony.CleanupBinding.SessionID = newID
	next.Ceremony.CleanupBinding.SecretUID = "99999999-3333-4333-8333-999999999999"
	next.Ceremony.CleanupBinding.ConfigMapName = "pki-bootstrap-cleanup-binding-" + newID[:12]
	renewal := &productSessionRenewal{ScopeSHA256: productIdentityScope(l), Original: old, Installed: next}
	outputsByID := map[string]environmentCreateProductIdentity{}
	for _, o := range outputs {
		outputsByID[o.ID] = o
	}
	consumers := map[string]int{}
	var selected []deploymentplan.Definition
	objects := map[string]deploymentplan.Object{}
	controllerID := "Deployment/" + e.Journal.Stack + "-video-cloud/pki-controller"
	for _, d := range defs {
		name := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"])
		keep := deploymentplan.ID(d.Object) == controllerID || name == oldBinding.ConfigMapName || name == "bootstrap-cleanup" || name == e.Journal.Stack+"-bootstrap-cleanup"
		probe := deploymentplan.Clone(d.Object)
		before := len(consumers)
		if err := resolveCreateNormalTailProductACK(probe, outputsByID, consumers, old.Ceremony, true); err != nil {
			t.Fatal(err)
		}
		product := len(consumers) > before
		if product && name == "video-cloud-mqttfoundation" {
			// The real forward MQTT projection appends transport settings after
			// existing ACK fields; their original order remains significant.
			container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(probe)["containers"])[0])
			container["env"] = environmentCreateServiceWithoutEnv(container["env"], "VIDEO_CLOUD_MQTT_TLS_ENABLED")
			container["env"] = append(certIssuerObjectList(container["env"]), deploymentplan.Object{"name": "VIDEO_CLOUD_MQTT_TLS_ENABLED", "value": "true"})
		}
		if !keep && !product {
			continue
		}
		d.Object = probe
		d.Dependencies = nil
		d.Retain = !product
		if !product {
			current := deploymentplan.Clone(probe)
			metadata := deploymentplan.Map(current["metadata"])
			metadata["uid"] = "aaaaaaaa-4444-4444-8444-aaaaaaaaaaaa"
			metadata["resourceVersion"] = "1"
			labels := deploymentplan.Map(metadata["labels"])
			if labels == nil {
				labels = deploymentplan.Object{}
				metadata["labels"] = labels
			}
			labels["rtk.realtek.com/stack"] = e.Journal.Stack
			objects[deploymentplan.ID(current)] = current
		}
		selected = append(selected, d)
	}
	if len(consumers) != 5 {
		t.Fatal("maintained renderer did not select all five products", consumers)
	}
	digest := deploymentplan.Digest("frozen program input")
	h := &createNormalTailHandoff{Execution: e, Pins: map[string]string{"original": digest}}
	in := deploymentplan.Inputs{Environment: "dev", Stack: e.Journal.Stack, Operation: "create", ConfigurationSHA256: digest, ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(bindings), StorageSHA256: deploymentplan.Digest(nil), CreateParentSHA256: e.Journal.PlanSHA256, CreateHandoffSHA256: deploymentplan.Digest(h.Pins)}
	var live []deploymentplan.Object
	for _, o := range objects {
		live = append(live, o)
	}
	p, err := deploymentplan.Compile(in, selected, live)
	if err != nil {
		t.Fatal(err)
	}
	b := &normalTailE2EBackend{e: e, objects: objects, writes: map[string]int{}}
	intent, _ := environmentCreateKubernetesStageIntent(p)
	if err = e.BeginEntry("kubernetes/workload", "workload", intent, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	phase := func(context.Context, *deploymentplan.Plan) (map[string]deploymentplan.Object, error) {
		return nil, &deploymentplan.Blocker{ID: "deployment.execution.storage", Cause: "known first phase input failure"}
	}
	err = executeCreateNormalTailStageBackendWithPhaseFence(t.Context(), e.Store, h, createNormalTailWorkloadForwardStage, p, b, check, check, check, io.Discard, func(context.Context) error { return nil }, phase)
	if err == nil {
		t.Fatal("original forward should stop before its first native write")
	}
	j, exists, err := p.ReadExecution(e.Store.Root)
	if err != nil || !exists || !productSessionForwardKnownZeroWrite(p, j) {
		t.Fatal("genuine zero-write journal required", j, err)
	}
	renewal.Source = productSessionRenewalSource{ParentSHA256: l.ParentSHA256, OwnerSHA256: l.OwnerSHA256, ScopeSHA256: productIdentityScope(l), OriginalForwardPlanSHA256: p.SHA256(), OriginalForwardJournalSHA256: deploymentplan.Digest(j), OriginalInstalledSHA256: deploymentplan.Digest(old)}
	newController := deploymentplan.Clone(objects[controllerID])
	delete(deploymentplan.Map(newController["metadata"]), "resourceVersion")
	controls, err = applyEnvironmentBootstrapCleanup(deploymentEndpointValues(cfg), newController, next.Ceremony.CleanupBinding)
	if err != nil {
		t.Fatal(err)
	}
	controls = append(controls, deploymentplan.Definition{Object: newController, Phase: "workload"})
	live = nil
	for _, o := range objects {
		live = append(live, o)
	}
	controlInputs := in
	controlInputs.Operation = "scoped"
	controlInputs.BindingsSHA256 = environmentCreateStageBindingsSHA("renewal-controls", controls)
	control, err := deploymentplan.Compile(controlInputs, controls, live)
	if err != nil {
		t.Fatal(err)
	}
	if err = executeCreateNormalTailStageBackend(t.Context(), e.Store, h, "renewal-controls", control, b, check, check, check, io.Discard, func(context.Context) error { return nil }); err != nil {
		t.Fatal("real accepted controls", err)
	}
	cj, exists, err := control.ReadExecution(e.Store.Root)
	if err != nil || !exists || !cj.Accepted {
		t.Fatal(err)
	}
	var dependencies []deploymentplan.Resource
	for _, r := range control.Resources {
		r.UID = cj.Owners[r.ID]
		r.Baseline = deploymentplan.Projection(objects[r.ID])
		dependencies = append(dependencies, r)
	}
	return &productSessionWorkloadFixture{cfg, e, h, l, renewal, p, j, dependencies, b}
}

func TestCreateNormalTailProductSessionForwardPreservesStartedPlanAndTruthfulClosure(t *testing.T) {
	f := newProductSessionWorkloadFixture(t)
	path := filepath.Join(f.e.Store.Root, "deployment/rollback", f.previous.SHA256(), "journal.json")
	before, _ := os.ReadFile(path)
	planSHA := f.previous.SHA256()
	installedSHA := deploymentplan.Digest(f.renewal.Original)
	next, err := buildCreateNormalTailProductSessionForward(f.cfg, f.l, f.previous, f.previousJournal, f.renewal, f.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if f.previous.SHA256() != planSHA || deploymentplan.Digest(f.renewal.Original) != installedSHA || deploymentplan.Digest(next.Inputs) != deploymentplan.Digest(f.previous.Inputs) {
		t.Fatal("immutable original or fixed inputs changed")
	}
	for i, r := range f.previous.Resources {
		if strings.HasPrefix(r.ID, "PersistentVolumeClaim/") && deploymentplan.Digest(r) != deploymentplan.Digest(next.Resources[i]) {
			t.Fatal("volume lineage changed")
		}
	}
	c := &compiledEnvironmentDeployment{Plan: next, Store: f.e.Store}
	d := &createNormalTailProductSessionForward{ForwardStage: createNormalTailProductSessionForwardStage, OriginalPlanSHA256: f.previous.SHA256(), OriginalJournalSHA256: deploymentplan.Digest(f.previousJournal), PreviousPlanSHA256: f.previous.SHA256(), PreviousJournalSHA256: deploymentplan.Digest(f.previousJournal), RenewalSHA256: deploymentplan.Digest(f.renewal), Target: createNormalTailTarget{Plan: next}}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.e.Check(ctx) }
	if err = executeCreateNormalTailProductSessionForwardBackend(t.Context(), f.cfg, f.e.Store, f.h, c, d, f.b, check, check, check, io.Discard, func(context.Context) error { return nil }); err != nil {
		t.Fatal("ordinary successor and composite response", err)
	}
	old, exists, err := f.previous.ReadExecution(f.e.Store.Root)
	if err != nil || !exists || old.Accepted || deploymentplan.Digest(old) != deploymentplan.Digest(f.previousJournal) {
		t.Fatal("old child was relabeled accepted", err)
	}
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("old literal child changed")
	}
	winner, exists, err := next.ReadExecution(f.e.Store.Root)
	if err != nil || !exists || !winner.Accepted {
		t.Fatal("no genuinely accepted successor", err)
	}
	oldEntry := createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/"+createNormalTailWorkloadForwardStage)
	if oldEntry.State != "completed" || !productSessionForwardResponseMatches(*oldEntry, d, next, winner) {
		t.Fatal("supersession did not close original intent truthfully")
	}
	previous := &createNormalTailWorkloadForward{ForwardStage: createNormalTailWorkloadForwardStage, Target: createNormalTailTarget{Plan: f.previous}}
	if err := validateCreateNormalTailProductSessionSupersession(f.e.Store, f.e, previous, d); err != nil {
		t.Fatal("genuine supersession reader", err)
	}
	for _, mode := range []string{"unknown-state", "pending-with-receipt", "pending-with-completion", "changed-receipt", "allocated-with-completion", "completed-before-winner"} {
		t.Run(mode, func(t *testing.T) {
			entry := createOpenBaoACKCompletionEntry(f.e.Journal, "kubernetes/"+createNormalTailWorkloadForwardStage)
			saved := *entry
			defer func() { *entry = saved }()
			switch mode {
			case "unknown-state":
				entry.State = "superseded"
			case "pending-with-receipt":
				entry.State = "pending"
				entry.CompletedAt = time.Time{}
			case "pending-with-completion":
				entry.State = "pending"
				entry.Receipt = nil
			case "changed-receipt":
				entry.Receipt = mustProviderRecoveryJSON(map[string]string{"schema": createNormalTailProductSessionForwardSchema, "renewal_sha256": deploymentplan.Digest("foreign")})
			case "allocated-with-completion":
				entry.State = "allocated"
			case "completed-before-winner":
				entry.CompletedAt = entry.StartedAt
			}
			if err := validateCreateNormalTailProductSessionSupersession(f.e.Store, f.e, previous, d); err == nil {
				t.Fatal("unauthenticated supersession state accepted")
			}
		})
	}
	writes := deploymentplan.Digest(f.b.writes)
	if err = executeCreateNormalTailProductSessionForwardBackend(t.Context(), f.cfg, f.e.Store, f.h, c, d, f.b, check, check, check, io.Discard, func(context.Context) error { return nil }); err != nil || deploymentplan.Digest(f.b.writes) != writes {
		t.Fatal("completed retry repeated native writes", err)
	}
	if err = executeCreateNormalTailSequence(t.Context(), f.e, []func(context.Context) error{f.e.Check}); err != nil || f.e.Journal.CompletedAt.IsZero() {
		t.Fatal("original parent closure", err)
	}
}

func TestCreateNormalTailProductSessionForwardRejectsUnknownAndChangedIdentity(t *testing.T) {
	f := newProductSessionWorkloadFixture(t)
	for _, mode := range []string{"in-flight", "known-owner", "completed", "accepted", "acceptance-started", "foreign-failure", "same-session", "changed-product", "foreign-issuer", "foreign-scope", "foreign-cleanup", "foreign-dependency-owner", "changed-configuration", "changed-intent"} {
		t.Run(mode, func(t *testing.T) {
			var r productSessionRenewal
			_ = decodeEnvironmentJSON(mustProviderRecoveryJSON(f.renewal), &r)
			var j deploymentplan.Journal
			_ = decodeEnvironmentJSON(mustProviderRecoveryJSON(f.previousJournal), &j)
			var prior deploymentplan.Plan
			_ = decodeEnvironmentJSON(mustProviderRecoveryJSON(f.previous), &prior)
			deps := append([]deploymentplan.Resource(nil), f.dependencies...)
			switch mode {
			case "in-flight":
				j.InFlight = f.previous.Resources[0].ID
			case "known-owner":
				j.Owners[f.previous.Resources[0].ID] = "aaaaaaaa-4444-4444-8444-aaaaaaaaaaaa"
			case "completed":
				j.Completed = []string{f.previous.Resources[0].ID}
			case "accepted":
				j.Accepted = true
			case "acceptance-started":
				j.AcceptanceStarted = true
			case "foreign-failure":
				j.Failed = "Deployment/video-cloud-dev-video-cloud/foreign"
			case "same-session":
				r.Installed.Ceremony.BootstrapSessionID = r.Original.Ceremony.BootstrapSessionID
			case "changed-product":
				r.Installed.Outputs[0].CertificateSHA256 = deploymentplan.Digest("replacement product leaf")
			case "foreign-issuer":
				r.Installed.Ceremony.Intermediate.ID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
			case "foreign-scope":
				r.ScopeSHA256 = deploymentplan.Digest("another program")
			case "foreign-cleanup":
				r.Installed.Ceremony.CleanupBinding.SessionID = r.Original.Ceremony.BootstrapSessionID
			case "changed-configuration":
				prior.ConfigurationSHA256 = deploymentplan.Digest("changed canonical input")
			case "changed-intent":
				prior.CreateHandoffSHA256 = deploymentplan.Digest("another handoff")
			case "foreign-dependency-owner":
				deps[0].UID = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
			}
			before := deploymentplan.Digest(f.b.writes)
			if _, err := buildCreateNormalTailProductSessionForward(f.cfg, f.l, &prior, j, &r, deps); err == nil {
				t.Fatal("unsafe successor accepted")
			}
			if deploymentplan.Digest(f.b.writes) != before {
				t.Fatal("validation wrote native state")
			}
		})
	}
}

func TestCreateNormalTailProductSessionForwardDoesNotCloseBeforeAcceptedWinner(t *testing.T) {
	f := newProductSessionWorkloadFixture(t)
	next, err := buildCreateNormalTailProductSessionForward(f.cfg, f.l, f.previous, f.previousJournal, f.renewal, f.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	d := &createNormalTailProductSessionForward{ForwardStage: createNormalTailProductSessionForwardStage, Target: createNormalTailTarget{Plan: next}}
	c := &compiledEnvironmentDeployment{Plan: next, Store: f.e.Store}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return f.e.Check(ctx) }
	stop := func(context.Context, *deploymentplan.Plan) error {
		return errors.New("isolated acceptance interrupted")
	}
	if err = executeCreateNormalTailProductSessionForwardBackend(t.Context(), f.cfg, f.e.Store, f.h, c, d, f.b, check, check, stop, io.Discard, func(context.Context) error { return nil }); err == nil {
		t.Fatal("acceptance failure hidden")
	}
	for _, key := range []string{"kubernetes/workload", "kubernetes/" + createNormalTailWorkloadForwardStage} {
		e := createOpenBaoACKCompletionEntry(f.e.Journal, key)
		if e.State != "pending" || len(e.Receipt) != 0 {
			t.Fatal("old intent closed before acceptance")
		}
	}
}

func TestCreateNormalTailProductSessionForwardKeepsNativeDefaultedDependencySnapshots(t *testing.T) {
	f := newProductSessionWorkloadFixture(t)
	id := "Deployment/" + f.e.Journal.Stack + "-video-cloud/pki-controller"
	actual := f.b.objects[id]
	deploymentplan.Map(actual["spec"])["revisionHistoryLimit"] = float64(10)
	deploymentplan.Map(actual["metadata"])["annotations"] = deploymentplan.Object{"deployment.kubernetes.io/revision": "19"}
	deps, err := captureProductSessionForwardDependencies(t.Context(), f.dependencies, f.b.Read)
	if err != nil {
		t.Fatal(err)
	}
	next, err := buildCreateNormalTailProductSessionForward(f.cfg, f.l, f.previous, f.previousJournal, f.renewal, deps)
	if err != nil {
		t.Fatal(err)
	}
	restored, err := savedProductSessionForwardDependencies(f.dependencies, next)
	if err != nil {
		t.Fatal(err)
	}
	if deploymentplan.Digest(deps) != deploymentplan.Digest(restored) {
		t.Fatal("saved native defaulted snapshot could not be authenticated")
	}
	for _, r := range next.Resources {
		if r.ID == id {
			if _, exists := deploymentplan.Map(r.Desired["spec"])["revisionHistoryLimit"]; exists {
				t.Fatal("native defaults copied into desired recipe")
			}
			if deploymentplan.Map(r.Baseline["spec"])["revisionHistoryLimit"] != float64(10) {
				t.Fatal("native defaulted baseline lost")
			}
		}
	}
	deploymentplan.Map(actual["metadata"])["uid"] = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
	if _, err := captureProductSessionForwardDependencies(t.Context(), f.dependencies, f.b.Read); err == nil {
		t.Fatal("replacement dependency owner adopted")
	}
}

func TestCreateNormalTailProductSessionForwardReportsSafeBoundary(t *testing.T) {
	resource := "Deployment/video-cloud-dev-video-cloud/pki-controller"
	trusted := &deploymentplan.Blocker{ID: "deployment.bindings.bootstrap", Cause: "exact bootstrap authorization owner required", Next: "restore the exact original owner"}
	var actual *deploymentplan.Blocker
	if err := productSessionForwardFailure("prepare-dependency-derivation", resource, trusted); !errors.As(err, &actual) || actual.ID != trusted.ID || actual.Resource != resource || !strings.Contains(actual.Cause, trusted.Cause) || !strings.Contains(actual.Cause, "prepare-dependency-derivation") || trusted.Resource != "" {
		t.Fatal("trusted typed cause lost or original error mutated")
	}
	private := "synthetic-private-token-that-must-not-appear"
	if err := productSessionForwardFailure("dependency-readback", resource, errors.New(private)); !errors.As(err, &actual) || actual.Resource != resource || !strings.Contains(actual.Cause, "dependency-readback") || strings.Contains(err.Error(), private) {
		t.Fatal("unexpected error exposed private content or lost boundary")
	}
}

func TestCreateNormalTailProductSessionForwardPreservesMQTTEnvOrder(t *testing.T) {
	f := newProductSessionWorkloadFixture(t)
	next, err := buildCreateNormalTailProductSessionForward(f.cfg, f.l, f.previous, f.previousJournal, f.renewal, f.dependencies)
	if err != nil {
		t.Fatal(err)
	}
	var original deploymentplan.Object
	for i, before := range f.previous.Resources {
		if before.ID != "Deployment/video-cloud-dev-video-cloud/video-cloud-mqttfoundation" {
			continue
		}
		original = before.Desired
		want := deploymentplan.Clone(before.Desired)
		container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(want)["containers"])[0])
		fields, err := environmentPKINamedFields(container["env"])
		if err != nil {
			t.Fatal(err)
		}
		fields["PKI_BOOTSTRAP_SESSION_ID"]["value"] = f.renewal.Installed.Ceremony.BootstrapSessionID
		if deploymentplan.Digest(want) != deploymentplan.Digest(next.Resources[i].Desired) {
			t.Fatal("successor reordered env fields or changed unrelated desired data")
		}
	}
	if original == nil {
		t.Fatal("actual maintained MQTT consumer missing")
	}
	outputs := map[string]environmentCreateProductIdentity{}
	for _, output := range f.l.Outputs {
		outputs[output.ID] = output
	}
	for _, mode := range []string{"missing-session", "duplicate-session", "foreign-session", "foreign-issuer", "value-from"} {
		t.Run(mode, func(t *testing.T) {
			body := deploymentplan.Clone(original)
			container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(body)["containers"])[0])
			fields, _ := environmentPKINamedFields(container["env"])
			switch mode {
			case "missing-session":
				container["env"] = environmentCreateServiceWithoutEnv(container["env"], "PKI_BOOTSTRAP_SESSION_ID")
			case "duplicate-session":
				container["env"] = append(certIssuerObjectList(container["env"]), deploymentplan.Clone(fields["PKI_BOOTSTRAP_SESSION_ID"]))
			case "foreign-session":
				fields["PKI_BOOTSTRAP_SESSION_ID"]["value"] = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
			case "foreign-issuer":
				fields["PKI_BOOTSTRAP_ISSUER_ID"]["value"] = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"
			case "value-from":
				delete(fields["PKI_BOOTSTRAP_SESSION_ID"], "value")
				fields["PKI_BOOTSTRAP_SESSION_ID"]["valueFrom"] = deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "foreign", "key": "session"}}
			}
			if err := productSessionForwardACKInPlace(body, outputs, map[string]int{}, f.renewal.Original.Ceremony, f.renewal.Installed.Ceremony); err == nil {
				t.Fatal("unknown original ACK accepted")
			}
		})
	}
}
