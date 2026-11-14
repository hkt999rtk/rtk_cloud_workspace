package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestSecondSessionPlanKeepsQualifiedRepairRelease(t *testing.T) {
	_, store, old, _, base := providerExecutionContractFixture(t)
	e, _ := providerExecutionContractFork(t, old, base, "")
	raw := mustProviderRecoveryJSON(base)
	var selected environmentCreatePlan
	if err := decodeEnvironmentJSON(raw, &selected); err != nil {
		t.Fatal(err)
	}
	image := environmentReleaseImage{Reference: "ghcr.io/example/api@sha256:" + strings.Repeat("d", 64)}
	selected.Release.Images["LKE_VIDEO_CLOUD_IMAGE"] = image
	selected.Bootstrap.Image = image.Reference
	target := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: store.Environment, Stack: base.Stack, ConfigurationSHA256: base.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(selected.Release)}}
	for _, mode := range []string{"qualified", "missing", "release-drift", "configuration-drift"} {
		t.Run(mode, func(t *testing.T) {
			p := selected
			chosen := &p
			switch mode {
			case "missing":
				chosen = nil
			case "release-drift":
				p.Release.Schema = "foreign-release"
			case "configuration-drift":
				p.ConfigurationSHA256 = strings.Repeat("e", 64)
			}
			plan, err := qualifiedSecondSessionPlan(store, e, chosen, target)
			if mode == "qualified" {
				if err != nil || deploymentplan.Digest(plan) != deploymentplan.Digest(selected) {
					t.Fatal("executor did not use the exact preflight-selected repair plan", err)
				}
			} else if err == nil {
				t.Fatal("unqualified session plan accepted", mode)
			}
		})
	}
}
