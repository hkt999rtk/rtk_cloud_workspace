package main

import (
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestInitializerForwardDefinitionsOnlyAcceptsQualifiedCommand(t *testing.T) {
	inputs := deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: "config", ReleaseSHA256: "release", BindingsSHA256: "source"}
	old := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": "video-cloud-dev-video-cloud", "name": "app"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"initContainers": []any{deploymentplan.Object{"name": "trust", "command": []any{"old"}}}, "containers": []any{deploymentplan.Object{"name": "app", "image": "original"}}}}}}
	defs := []deploymentplan.Definition{{Object: old, Phase: "workload"}}
	original, err := deploymentplan.Compile(inputs, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	live := deploymentplan.Clone(original.Resources[0].Desired)
	deploymentplan.Map(live["metadata"])["uid"] = "uid"
	corrected := deploymentplan.Clone(old)
	managedNamedField(environmentPKIPod(corrected)["initContainers"], "trust")["command"] = []any{"new"}
	update, err := deploymentplan.Compile(inputs, []deploymentplan.Definition{{Object: corrected, Phase: "workload"}}, []deploymentplan.Object{live})
	if err != nil {
		t.Fatal(err)
	}
	correctedDefs := []deploymentplan.Definition{{Object: corrected, Phase: "workload"}}
	if err := verifyInitializerForwardDefinitions(original, correctedDefs, update, "trust"); err != nil {
		t.Fatal(err)
	}
	if deploymentplan.Digest(managedNamedField(environmentPKIPod(corrected)["initContainers"], "trust")["command"]) != deploymentplan.Digest([]any{"new"}) {
		t.Fatal("mutated input")
	}
	changed := deploymentplan.Clone(corrected)
	managedNamedField(environmentPKIPod(changed)["containers"], "app")["image"] = "foreign"
	if verifyInitializerForwardDefinitions(original, []deploymentplan.Definition{{Object: changed, Phase: "workload"}}, update, "trust") == nil {
		t.Fatal("accepted unrelated image change")
	}
	if verifyInitializerForwardDefinitions(original, defs, update, "trust") == nil {
		t.Fatal("accepted old command")
	}
}
