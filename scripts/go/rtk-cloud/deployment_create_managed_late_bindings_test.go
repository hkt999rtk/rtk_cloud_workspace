package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"testing"
	"time"
)

func managedLateCleanupFixture(t *testing.T) (*createManagedLateSessionRecovery, environmentCreateServiceCeremony, *deploymentplan.Plan, []deploymentplan.Object) {
	t.Helper()
	late, _ := managedLateOwnersFixture(t)
	late.SourceCeremony.Root.Environment = "dev"
	old := environmentBootstrapCleanupBinding{Version: 1, Environment: "dev", SessionID: late.OriginalSession, NamespaceUID: "33333333-3333-4333-8333-333333333333", SecretUID: "44444444-4444-4444-8444-444444444444", PolicySHA256: strings.Repeat("d", 64), KubernetesCA: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
	late.SourceCeremony.CleanupBinding = old
	id := "Deployment/video-cloud-dev-video-cloud/pki-controller"
	controller := late.Owners[id]
	pod := environmentPKIPod(controller.Desired)
	pod["serviceAccountName"] = "pki-controller"
	pod["automountServiceAccountToken"] = false
	late.Owners[id] = controller
	originalDefs, err := applyEnvironmentBootstrapCleanup(map[string]string{"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": "video-cloud-dev"}, deploymentplan.Clone(controller.Desired), old)
	if err != nil {
		t.Fatal(err)
	}
	inputs := deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}
	core, err := deploymentplan.Compile(inputs, originalDefs, nil)
	if err != nil {
		t.Fatal(err)
	}
	var live []deploymentplan.Object
	journal := deploymentplan.Journal{Owners: map[string]string{}, Accepted: true, AcceptanceStarted: true}
	for index := range core.Resources {
		r := &core.Resources[index]
		r.UID = fmt.Sprintf("%08d-5555-4555-8555-555555555555", index+1)
		r.Action = "retain"
		r.Baseline = deploymentplan.Clone(r.Desired)
		journal.Owners[r.ID] = r.UID
		journal.Completed = append(journal.Completed, r.ID)
		actual := deploymentplan.Clone(r.Desired)
		deploymentplan.Map(actual["metadata"])["uid"] = r.UID
		live = append(live, actual)
	}
	journal.PlanSHA256 = core.SHA256()
	late.AcceptedCore, late.AcceptedCoreJournal = core, journal
	service := late.SourceCeremony
	service.BootstrapSessionID = late.NewSessionID
	service.CleanupBinding = old
	service.CleanupBinding.SessionID = late.NewSessionID
	service.CleanupBinding.SecretUID = "66666666-6666-4666-8666-666666666666"
	service.CleanupBinding.ConfigMapName = "pki-bootstrap-cleanup-binding-" + late.NewSessionID[:12]
	defs, err := managedLateSessionCleanupDefinitions(late, service)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := deploymentplan.Compile(inputs, defs, live)
	if err != nil {
		t.Fatal(err)
	}
	if deploymentplan.ID(defs[0].Object) == plan.Resources[0].ID {
		t.Fatal("fixture did not exercise actual compiler reorder")
	}
	return late, service, plan, live
}

func TestManagedLateCleanupUsesAllocatedSecretAndRealCompilerOrder(t *testing.T) {
	late, service, plan, _ := managedLateCleanupFixture(t)
	late.SignerPolicySHA = service.CleanupBinding.PolicySHA256
	late.SourceCeremony.BootstrapCredentialNamespace = "video-cloud-dev-video-cloud"
	credentials := deploymentplan.Journal{Owners: map[string]string{"Secret/video-cloud-dev-video-cloud/service-bootstrap-" + late.NewSessionID: service.CleanupBinding.SecretUID}}
	if err := validateManagedLateCleanupChild(late, plan, credentials); err != nil {
		t.Fatal(err)
	}
	wrong := credentials
	wrong.Owners = map[string]string{}
	if validateManagedLateCleanupChild(late, plan, wrong) == nil {
		t.Fatal("unallocated new Secret UID admitted")
	}
}

func TestManagedLateFinalBindingsChooseAcceptedCurrentCMAndPreserveOld(t *testing.T) {
	cfg, store, operator, artifact := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	late, service, cleanup, oldLive := managedLateCleanupFixture(t)
	late.Review = strings.Repeat("e", 64)
	late.ParentSHA = e.Journal.PlanSHA256
	stage := "service-cleanup-binding-renewal-" + late.Review
	journal := deploymentplan.Journal{PlanSHA256: cleanup.SHA256(), Accepted: true, AcceptanceStarted: true, Owners: map[string]string{}}
	var live []deploymentplan.Object
	for index, resource := range cleanup.Resources {
		uid := resource.UID
		if uid == "" {
			uid = fmt.Sprintf("%08d-7777-4777-8777-777777777777", index+1)
		}
		journal.Owners[resource.ID] = uid
		journal.Completed = append(journal.Completed, resource.ID)
		actual := deploymentplan.Clone(resource.Desired)
		deploymentplan.Map(actual["metadata"])["uid"] = uid
		live = append(live, actual)
	}
	controllerID := "Deployment/video-cloud-dev-video-cloud/pki-controller"
	controller := deploymentplan.Clone(late.Owners[controllerID].Desired)
	deploymentplan.Map(controller["metadata"])["uid"] = late.Owners[controllerID].UID
	deploymentplan.Map(managedNamedField(environmentPKIPod(controller)["volumes"], "bootstrap-cleanup")["configMap"])["name"] = service.CleanupBinding.ConfigMapName
	live = append(live, controller)
	live = append(live, deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": e.Journal.Stack + "-video-cloud", "uid": service.CleanupBinding.NamespaceUID}})
	// Old immutable CM remains an original retained owner, but cannot select the
	// active binding after the exact accepted new child and current mount exist.
	for _, object := range oldLive {
		if object["kind"] == "ConfigMap" {
			live = append(live, object)
		}
	}
	for relative, value := range map[string]any{"deployment/rollback/" + cleanup.SHA256() + "/plan.json": cleanup, "deployment/rollback/" + cleanup.SHA256() + "/journal.json": journal} {
		if _, err := writeEnvironmentCreateBootstrapJSON(store, relative, value); err != nil {
			t.Fatal(err)
		}
	}
	intent, err := environmentCreateKubernetesStageIntent(cleanup)
	if err != nil {
		t.Fatal(err)
	}
	e.Journal.Entries = append(e.Journal.Entries, environmentCreateJournalEntry{Key: "kubernetes/" + stage, Phase: stage, State: "completed", StartedAt: time.Now().Add(-time.Minute), CompletedAt: time.Now().Add(-30 * time.Second), IntentSHA256: activeTransportJournalDigest(jsonRawObject(intent)), Receipt: mustProviderRecoveryJSON(deploymentplan.Object{"plan_sha256": cleanup.SHA256(), "owners": journal.Owners})}, environmentCreateJournalEntry{Key: "service-session-finalize", Phase: "bootstrap", State: "completed", StartedAt: time.Now().Add(-20 * time.Second), CompletedAt: time.Now().Add(-10 * time.Second), IntentSHA256: strings.Repeat("f", 64), Receipt: mustProviderRecoveryJSON(deploymentplan.Object{"synthetic_finalized_session": late.NewSessionID})})
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	r := &createServiceSessionRecovery{ManagedLateSession: late, Ceremony: service}
	runtime := environmentCreateBootstrapRuntime{Execution: e, ServiceSessionRecovery: r}
	release := environmentRelease{Environment: cfg.Environment, Stack: e.Journal.Stack, Images: map[string]environmentReleaseImage{"LKE_VIDEO_CLOUD_IMAGE": {Reference: deploymentplan.String(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(controller)["containers"])[0])["image"])}}}
	plain, observeErr := observeEnvironmentTargetBindings(context.Background(), cfg, release, "create", live)
	if observeErr != nil {
		t.Fatal(observeErr)
	}
	if plain.BootstrapCleanup == nil || plain.BootstrapCleanup.SessionID != late.OriginalSession {
		t.Fatal("fixture did not reproduce historical cleanup selection")
	}
	current, observeErr := observeEnvironmentTargetBindings(withEnvironmentCreateBootstrapRuntime(context.Background(), runtime), cfg, release, "create", live)
	if observeErr != nil {
		t.Fatal(observeErr)
	}
	if current.BootstrapCleanup == nil || current.BootstrapCleanup.SessionID != late.NewSessionID || current.BootstrapCleanup.ConfigMapName != service.CleanupBinding.ConfigMapName {
		t.Fatal("final create compiler lost accepted cleanup binding")
	}
	rendered, renderErr := applyEnvironmentBootstrapCleanup(map[string]string{"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": e.Journal.Stack}, deploymentplan.Clone(controller), *current.BootstrapCleanup)
	if renderErr != nil {
		t.Fatal(renderErr)
	}
	roleFound := false
	for _, def := range rendered {
		if def.Object["kind"] == "Role" {
			roleFound = true
			rule := deploymentplan.Map(certIssuerObjectList(def.Object["rules"])[0])
			if deploymentplan.Digest(rule["resourceNames"]) != deploymentplan.Digest([]any{"service-bootstrap-" + late.NewSessionID}) {
				t.Fatal("compiled cleanup Role selected historical session")
			}
		}
	}
	if !roleFound {
		t.Fatal("final cleanup Role absent")
	}
	completedJournal := e.Journal
	e.Journal.Entries = e.Journal.Entries[:len(e.Journal.Entries)-1]
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	if _, err := observeEnvironmentTargetBindings(withEnvironmentCreateBootstrapRuntime(context.Background(), runtime), cfg, release, "create", live); err == nil {
		t.Fatal("target compiler accepted missing finalization")
	}
	e.Journal = completedJournal
	if err := e.save(); err != nil {
		t.Fatal(err)
	}

	unchanged, observeErr := observeEnvironmentTargetBindings(withEnvironmentCreateBootstrapRuntime(context.Background(), runtime), cfg, release, "upgrade", live)
	if observeErr != nil || deploymentplan.Digest(unchanged) != deploymentplan.Digest(plain) {
		t.Fatal("late create adapter changed ordinary adoption", observeErr)
	}
	bindings := environmentIdentityBindings{Environment: "dev", Stack: "video-cloud-dev", Owners: map[string]string{}, BootstrapCleanup: &late.SourceCeremony.CleanupBinding}
	for _, object := range live {
		bindings.Owners[deploymentplan.ID(object)] = deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
	}
	if err := applyManagedLateBootstrapBindings(context.Background(), cfg, runtime, &bindings, live); err != nil {
		t.Fatal(err)
	}
	if bindings.BootstrapCleanup.SessionID != late.NewSessionID || bindings.BootstrapCleanup.SecretUID != service.CleanupBinding.SecretUID || bindings.BootstrapCleanup.ConfigMapName != service.CleanupBinding.ConfigMapName {
		t.Fatal("current actual allocation was not adopted")
	}
	for _, test := range []struct {
		name string
		edit func([]deploymentplan.Object)
	}{{"foreign current CM UID", func(v []deploymentplan.Object) {
		for _, o := range v {
			if deploymentplan.Map(o["metadata"])["name"] == service.CleanupBinding.ConfigMapName {
				deploymentplan.Map(o["metadata"])["uid"] = "88888888-8888-4888-8888-888888888888"
			}
		}
	}}, {"old controller mount", func(v []deploymentplan.Object) {
		for _, o := range v {
			if deploymentplan.ID(o) == controllerID {
				deploymentplan.Map(managedNamedField(environmentPKIPod(o)["volumes"], "bootstrap-cleanup")["configMap"])["name"] = "pki-bootstrap-cleanup-binding"
			}
		}
	}}, {"terminating current CM", func(v []deploymentplan.Object) {
		for _, o := range v {
			if deploymentplan.Map(o["metadata"])["name"] == service.CleanupBinding.ConfigMapName {
				deploymentplan.Map(o["metadata"])["deletionTimestamp"] = "now"
			}
		}
	}}} {
		t.Run(test.name, func(t *testing.T) {
			changed := make([]deploymentplan.Object, len(live))
			for i, o := range live {
				changed[i] = deploymentplan.Clone(o)
			}
			test.edit(changed)
			copy := bindings
			copy.Owners = map[string]string{}
			for key, value := range bindings.Owners {
				copy.Owners[key] = value
			}
			if _, err := observeEnvironmentTargetBindings(withEnvironmentCreateBootstrapRuntime(context.Background(), runtime), cfg, release, "create", changed); err == nil {
				t.Fatal("target compiler accepted changed cleanup owner or mount")
			}
			if applyManagedLateBootstrapBindings(context.Background(), cfg, runtime, &copy, changed) == nil {
				t.Fatal("foreign late binding accepted")
			}
		})
	}
	_ = slices.Clone(journal.Completed)
}
