package main

// Private typed final-binding adapter. Historical verifier maps are preserved;
// only the actual accepted new cleanup producer and native Controller reference
// choose the active binding. No prefix search supplies authority.
import (
	"context"
	"encoding/base64"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func applyManagedLateBootstrapBindings(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, bindings *environmentIdentityBindings, live []deploymentplan.Object) error {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if r == nil || r.ManagedLateSession == nil {
		return nil
	}
	late := r.ManagedLateSession
	bad := func() error {
		return environmentCreateBootstrapBlocker("late final bindings require the genuine accepted cleanup producer and original Controller reference")
	}
	if bindings == nil || e == nil || e.Check(ctx) != nil || r.Ceremony.BootstrapSessionID != late.NewSessionID || bindings.Environment != cfg.Environment || bindings.Stack != e.Journal.Stack {
		return bad()
	}
	finalized := createOpenBaoACKCompletionEntry(e.Journal, "service-session-finalize")
	if finalized == nil || finalized.State != "completed" || finalized.CompletedAt.IsZero() {
		return bad()
	}
	cleanup, journal, err := managedTransportPostAcceptedStage(e.Store, e, "kubernetes/service-cleanup-binding-renewal-"+late.Review)
	definitions, defErr := managedLateSessionCleanupDefinitions(late, r.Ceremony)
	if err != nil || defErr != nil || len(cleanup.Resources) != 5 || len(definitions) != 5 {
		return bad()
	}
	expected := map[string]deploymentplan.Object{}
	for _, definition := range definitions {
		id := deploymentplan.ID(definition.Object)
		if expected[id] != nil {
			return bad()
		}
		expected[id] = definition.Object
	}
	seen := map[string]bool{}
	for _, resource := range cleanup.Resources {
		if expected[resource.ID] == nil || seen[resource.ID] || !deploymentplan.Matches(expected[resource.ID], resource.Desired) || !environmentBootstrapOwnerUUID.MatchString(journal.Owners[resource.ID]) {
			return bad()
		}
		seen[resource.ID] = true
	}
	name := r.Ceremony.CleanupBinding.ConfigMapName
	if name != "pki-bootstrap-cleanup-binding-"+late.NewSessionID[:12] {
		return bad()
	}
	id := "ConfigMap/" + e.Journal.Stack + "-video-cloud/" + name
	controllerID := "Deployment/" + e.Journal.Stack + "-video-cloud/pki-controller"
	var cm, controller deploymentplan.Object
	for _, object := range live {
		switch deploymentplan.ID(object) {
		case id:
			if cm != nil {
				return bad()
			}
			cm = object
		case controllerID:
			if controller != nil {
				return bad()
			}
			controller = object
		}
	}
	if cm == nil || controller == nil || cm["immutable"] != true || deploymentplan.Map(cm["metadata"])["uid"] != journal.Owners[id] || deploymentplan.Map(controller["metadata"])["uid"] != late.Owners[controllerID].UID || deploymentplan.Map(cm["metadata"])["deletionTimestamp"] != nil || deploymentplan.Map(controller["metadata"])["deletionTimestamp"] != nil {
		return bad()
	}
	var resource *deploymentplan.Resource
	for index := range cleanup.Resources {
		if cleanup.Resources[index].ID == id {
			resource = &cleanup.Resources[index]
		}
	}
	if resource == nil || !deploymentplan.Matches(resource.Desired, cm) || bindings.Owners[id] != journal.Owners[id] || bindings.Owners[controllerID] != late.Owners[controllerID].UID {
		return bad()
	}
	volumes, volErr := environmentPKINamedFields(environmentPKIPod(controller)["volumes"])
	if volErr != nil || deploymentplan.Map(volumes["bootstrap-cleanup"]["configMap"])["name"] != name {
		return bad()
	}
	var actual environmentBootstrapCleanupBinding
	data := deploymentplan.Map(cm["data"])
	if decodeEnvironmentJSON([]byte(deploymentplan.String(data["binding.json"])), &actual) != nil {
		return bad()
	}
	actual.KubernetesCA = base64.StdEncoding.EncodeToString([]byte(deploymentplan.String(data["ca.crt"])))
	actual.ConfigMapName = name
	if validateEnvironmentBootstrapCleanupBinding(&actual, cfg.Environment) != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(r.Ceremony.CleanupBinding) {
		return bad()
	}
	bindings.BootstrapCleanup = &actual
	return e.Check(ctx)
}
