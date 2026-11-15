package main

import (
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestOpenBaoFirstDependencyCatalogTracksActualPullSecretStages(t *testing.T) {
	cfg, store, release, operator, bootstrap := createDependencyContractFixture(t)
	cfg.Values["PKI_STATE_BACKEND"] = "openbao"
	var err error
	bootstrap, err = buildEnvironmentCreateBootstrapPlan(t.Context(), cfg, release, store, operator)
	if err != nil {
		t.Fatal(err)
	}
	values := deploymentEndpointValues(cfg)
	outputs, err := environmentCreateDependencyOutputs(cfg, bootstrap, values)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ suffix, stage string }{
		{"secrets", "foundation"},
		{"video-cloud", "foundation-services"},
		{"account-manager", "foundation-services"},
	} {
		id := "Secret/" + bootstrap.Stack + "-" + tc.suffix + "/" + lkeImagePullSecretName(values)
		want := deploymentplan.Digest(struct {
			Recipe          environmentCreateBootstrapPlan
			Stage, Resource string
		}{bootstrap, tc.stage, id})
		if outputs.outputs[id] != want {
			t.Fatalf("%s producer did not bind stage %s", id, tc.stage)
		}
	}
	if _, err := qualifyEnvironmentCreateDependencyContract(t.Context(), cfg, release, store, operator, bootstrap); err != nil {
		t.Fatal("new foundation order must qualify the actual dependency catalog", err)
	}
}
