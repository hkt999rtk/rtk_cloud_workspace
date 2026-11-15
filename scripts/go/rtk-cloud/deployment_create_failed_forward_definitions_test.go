package main

import (
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestFailedForwardDefinitionsReproduceExactQualifiedPlan(t *testing.T) {
	_, _, p, _ := freshStageFixture(t)
	r := p.Resources[0]
	defs := []deploymentplan.Definition{{Object: deploymentplan.Clone(r.Desired), Phase: r.Phase, Dependencies: r.Dependencies}}
	if err := verifyFailedForwardDefinitions(p, defs); err != nil {
		t.Fatal(err)
	}
	live := deploymentplan.Clone(r.Desired)
	deploymentplan.Map(live["metadata"])["uid"] = "00000001-1111-4111-8111-111111111111"
	retainedDefs := []deploymentplan.Definition{{Object: deploymentplan.Clone(r.Desired), Phase: r.Phase, Retain: true}}
	retained, err := deploymentplan.Compile(p.Inputs, retainedDefs, []deploymentplan.Object{live})
	if err != nil {
		t.Fatal(err)
	}
	if err = verifyFailedForwardDefinitions(retained, retainedDefs); err != nil {
		t.Fatal("original retain baseline", err)
	}
	deploymentplan.Map(defs[0].Object["data"])["root"] = "changed"
	if err := verifyFailedForwardDefinitions(p, defs); err == nil {
		t.Fatal("changed renderer output accepted")
	}
}
