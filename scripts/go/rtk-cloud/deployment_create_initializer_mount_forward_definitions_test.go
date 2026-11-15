package main

import (
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestInitializerMountForwardDefinitionsStayBoundToOriginal(t *testing.T) {
	inputs := deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: "config", ReleaseSHA256: "release", BindingsSHA256: "bindings"}
	path := "/run/pki-state/root-trust-01010101-0202-4303-8404-050505050505.json"

	pod := deploymentplan.Object{
		"initContainers": []any{deploymentplan.Object{"name": "initialize-trust-state", "command": []any{"/bin/sh", "-ec", deviceNativeTrustPreparation(path)}, "volumeMounts": []any{deploymentplan.Object{"name": "pki-state", "mountPath": "/run/pki-state"}}}},
		"containers":     []any{deploymentplan.Object{"name": "app", "image": "original", "volumeMounts": []any{deploymentplan.Object{"name": "pki-state", "mountPath": "/run/pki-state"}}}},
		"volumes":        []any{deploymentplan.Object{"name": "pki-state", "emptyDir": deploymentplan.Object{"medium": "Memory"}}},
	}
	originalObject := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": "video-cloud-dev-video-cloud", "name": "app"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": pod}}}
	defs := []deploymentplan.Definition{{Object: originalObject, Phase: "workload"}}
	original, err := deploymentplan.Compile(inputs, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	q1 := deploymentplan.Clone(original.Resources[0].Desired)
	if !projectDeviceMemoryTrustInitializer(q1) {
		t.Fatal("q1 projection missing")
	}
	live := deploymentplan.Clone(q1)
	deploymentplan.Map(live["metadata"])["uid"] = "uid"
	q2 := deploymentplan.Clone(q1)
	changed, err := projectDeviceMemoryTrustPrivateSubPath(q2)
	if err != nil || !changed {
		t.Fatal("q2 projection missing", err)
	}
	updated := deploymentplan.Clone(q2)
	deploymentplan.Map(updated["metadata"])["uid"] = "uid"
	update, err := deploymentplan.Compile(inputs, []deploymentplan.Definition{{Object: updated, Phase: "workload"}}, []deploymentplan.Object{live})
	if err != nil {
		t.Fatal(err)
	}
	current := deploymentplan.Clone(originalObject)
	if !projectDeviceMemoryTrustInitializer(current) {
		t.Fatal("q1 renderer")
	}
	if ok, err := projectDeviceMemoryTrustPrivateSubPath(current); err != nil || !ok {
		t.Fatal("q2 renderer", err)
	}
	currentDefs := []deploymentplan.Definition{{Object: current, Phase: "workload"}}
	if err := verifyInitializerMountForwardDefinitions(original, currentDefs, update); err != nil {
		t.Fatal(err)
	}
	foreign := deploymentplan.Clone(current)
	managedNamedField(environmentPKIPod(foreign)["containers"], "app")["image"] = "foreign"
	if verifyInitializerMountForwardDefinitions(original, []deploymentplan.Definition{{Object: foreign, Phase: "workload"}}, update) == nil {
		t.Fatal("accepted unrelated image change")
	}
}

func TestPrivateStateInitializerUsesApplicationConfiguredIdentity(t *testing.T) {
	cfg, p, release, runtime, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	defs, err := finalizeEnvironmentCreateApplicationDefinitions(t.Context(), cfg, p, release, runtime, service, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	obj := deploymentplan.Clone(createRecipeObject(t, defs, "Deployment/"+p.Stack+"-video-cloud/video-cloud-api-pki"))
	pod := environmentPKIPod(obj)
	volume := managedNamedField(pod["volumes"], "pki-state")
	delete(volume, "persistentVolumeClaim")
	volume["emptyDir"] = deploymentplan.Object{"medium": "Memory"}
	app := managedNamedField(pod["containers"], "app")
	security := deploymentplan.Map(app["securityContext"])
	if security == nil {
		security = deploymentplan.Object{}
		app["securityContext"] = security
	}
	security["runAsUser"] = float64(23456)
	security["runAsGroup"] = float64(23457)
	projectDeviceMemoryTrustInitializer(obj)
	changed, err := projectDeviceMemoryTrustPrivateSubPath(obj)
	if err != nil || !changed {
		t.Fatal("projection", err)
	}
	init := managedNamedField(pod["initContainers"], "initialize-trust-state")
	actual := deploymentplan.Map(init["securityContext"])
	if actual["runAsUser"] != float64(23456) || actual["runAsGroup"] != float64(23457) {
		t.Fatal("initializer ignored application UID/GID", actual)
	}
}
