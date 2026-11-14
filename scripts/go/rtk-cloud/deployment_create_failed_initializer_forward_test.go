package main

import (
	"fmt"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"testing"
)

func TestFailedInitializerForwardUsesExactRecordedOwners(t *testing.T) {
	inputs := deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: "config", ReleaseSHA256: "release", BindingsSHA256: "source"}
	definitions := []deploymentplan.Definition{}
	for i := 0; i < 58; i++ {
		definitions = append(definitions, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"namespace": "video-cloud-dev-video-cloud", "name": fmt.Sprintf("item-%02d", i)}, "data": deploymentplan.Object{"value": "original"}}})
	}
	target := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"namespace": "video-cloud-dev-video-cloud", "name": "api"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"initContainers": []any{deploymentplan.Object{"name": "trust", "image": "original", "command": []any{"/bin/sh", "-ec", "old"}}}, "containers": []any{deploymentplan.Object{"name": "app", "image": "same"}}}}}}
	definitions = append(definitions, deploymentplan.Definition{Phase: "workload", Object: target})
	original, err := deploymentplan.Compile(inputs, definitions, nil)
	if err != nil {
		t.Fatal(err)
	}
	sourceJournal := deploymentplan.Journal{PlanSHA256: original.SHA256(), Owners: map[string]string{}}
	live := []deploymentplan.Object{}
	retainDefs := []deploymentplan.Definition{}
	for i, r := range original.Resources {
		uid := fmt.Sprintf("uid-%d", i)
		sourceJournal.Completed = append(sourceJournal.Completed, r.ID)
		sourceJournal.Owners[r.ID] = uid
		actual := deploymentplan.Clone(r.Desired)
		deploymentplan.Map(actual["metadata"])["uid"] = uid
		live = append(live, actual)
		desired := deploymentplan.Clone(r.Desired)
		deploymentplan.Map(desired["metadata"])["uid"] = uid
		retainDefs = append(retainDefs, deploymentplan.Definition{Object: desired, Phase: r.Phase, Retain: true, Dependencies: r.Dependencies})
		if r.Desired["kind"] == "Deployment" {
			sourceJournal.Failed = r.ID
		}
	}
	oldRetain, err := deploymentplan.Compile(inputs, retainDefs, live)
	if err != nil {
		t.Fatal(err)
	}
	targetID := sourceJournal.Failed
	sourceJournal.Failed = original.Resources[0].ID
	failedRetain := deploymentplan.Journal{PlanSHA256: oldRetain.SHA256(), Failed: targetID, Owners: map[string]string{}}
	for _, r := range oldRetain.Resources {
		failedRetain.Completed = append(failedRetain.Completed, r.ID)
		failedRetain.Owners[r.ID] = r.UID
	}
	corrected := deploymentplan.Definition{Object: deploymentplan.Clone(target), Phase: "workload"}
	init := managedNamedField(environmentPKIPod(corrected.Object)["initContainers"], "trust")
	init["command"] = []any{"/bin/sh", "-ec", "new"}
	call := func(d deploymentplan.Definition, sj, rj deploymentplan.Journal, objects []deploymentplan.Object) (*deploymentplan.Plan, *deploymentplan.Plan, error) {
		var admitted deploymentplan.Object
		for _, object := range objects {
			if deploymentplan.ID(object) == targetID {
				admitted = deploymentplan.Clone(object)
				admitted["spec"] = deploymentplan.Clone(d.Object)["spec"]
				deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(admitted["spec"])["template"])["spec"])["dnsPolicy"] = "ClusterFirst"
			}
		}
		return compileFailedInitializerForward(original, sj, oldRetain, rj, d, objects, admitted, "trust", "repair-init", "retain-init")
	}
	update, forward, err := call(corrected, sourceJournal, failedRetain, live)
	if err != nil {
		t.Fatal(err)
	}
	if len(update.Resources) != 1 || update.Resources[0].Action != "update" || update.Resources[0].UID != sourceJournal.Owners[targetID] || len(forward.Resources) != 59 {
		t.Fatal("wrong update or retained owners")
	}
	for _, r := range forward.Resources {
		if r.ID == targetID && deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(r.Baseline["spec"])["template"])["spec"])["dnsPolicy"] != "ClusterFirst" {
			t.Fatal("server-admitted default missing from future baseline")
		}
		if r.Action != "retain" || r.UID != sourceJournal.Owners[r.ID] {
			t.Fatal("retain lost owner", r.ID)
		}
	}
	bad := deploymentplan.Definition{Object: deploymentplan.Clone(corrected.Object), Phase: corrected.Phase}
	managedNamedField(environmentPKIPod(bad.Object)["containers"], "app")["image"] = "foreign"
	if _, _, err = call(bad, sourceJournal, failedRetain, live); err == nil {
		t.Fatal("accepted unrelated image change")
	}
	foreign := append([]deploymentplan.Object(nil), live...)
	for i, o := range foreign {
		if deploymentplan.ID(o) == targetID {
			foreign[i] = deploymentplan.Clone(o)
			deploymentplan.Map(foreign[i]["metadata"])["uid"] = "replacement"
		}
	}
	if _, _, err = call(corrected, sourceJournal, failedRetain, foreign); err == nil {
		t.Fatal("accepted replacement owner")
	}
	unchanged := deploymentplan.Definition{Object: deploymentplan.Clone(target), Phase: "workload"}
	if _, _, err = call(unchanged, sourceJournal, failedRetain, live); err == nil {
		t.Fatal("accepted unchanged initializer")
	}
	wrongJournal := failedRetain
	wrongJournal.InFlight = targetID
	if _, _, err = call(corrected, sourceJournal, wrongJournal, live); err == nil {
		t.Fatal("accepted uncertain in-flight write")
	}
	wrongJournal = failedRetain
	wrongJournal.PlanSHA256 = "foreign"
	if _, _, err = call(corrected, sourceJournal, wrongJournal, live); err == nil {
		t.Fatal("accepted unrelated retain journal")
	}
	drift := append([]deploymentplan.Object(nil), live...)
	drift[0] = deploymentplan.Clone(drift[0])
	deploymentplan.Map(drift[0]["data"])["value"] = "changed"
	if _, _, err = call(corrected, sourceJournal, failedRetain, drift); err == nil {
		t.Fatal("accepted drift in another original owner")
	}
	sourceJournal.Failed = "foreign"
	if _, _, err = call(corrected, sourceJournal, failedRetain, live); err == nil {
		t.Fatal("accepted changed source journal")
	}
}
