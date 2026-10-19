package main

import (
	"encoding/json"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateNormalTailRuntimeSnapshotAllowsOnlySelectedPaymentInputs(t *testing.T) {
	cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}}
	source := createNormalTailRuntimeSource{Configuration: cfg, Snapshot: environmentDeploymentSnapshot{Operator: map[string]string{"PAYPAL_ENABLED": "true", "PAYMENT_SIMULATOR_ENABLED": "false", "NEWEBPAY_ENABLED": "false", "UNCHANGED_OPERATOR": "original"}, Secrets: map[string]string{"postgres": "original-db"}, Kubeconfig: "original selected identity"}}
	before := deploymentplan.Digest(source)
	var current environmentDeploymentSnapshot
	if json.Unmarshal(mustProviderRecoveryJSON(source.Snapshot), &current) != nil {
		t.Fatal("snapshot clone")
	}
	current.Operator["PAYPAL_ENABLED"] = "false"
	inputs, err := qualifyCreateNormalTailRuntimeInputs(source, cfg, current)
	if err != nil {
		t.Fatal("selected disabled provider", err)
	}
	current, err = createNormalTailRuntimeEffectiveSnapshot(source, inputs)
	if err != nil || current.Operator["PAYPAL_ENABLED"] != "false" || current.Secrets["postgres"] != "original-db" || deploymentplan.Digest(source) != before {
		t.Fatal("exact source was not preserved", err)
	}
	for _, mutation := range []string{"extra-operator", "other-secret", "kubeconfig", "maintained", "bad-boolean", "missing-input", "extra-input", "absent-with-value"} {
		t.Run(mutation, func(t *testing.T) {
			var live environmentDeploymentSnapshot
			var selected createNormalTailRuntimeInputs
			json.Unmarshal(mustProviderRecoveryJSON(current), &live)
			json.Unmarshal(mustProviderRecoveryJSON(inputs), &selected)
			badcfg := cfg
			switch mutation {
			case "extra-operator":
				live.Operator["UNCHANGED_OPERATOR"] = "foreign"
			case "other-secret":
				live.Secrets["postgres"] = "foreign"
			case "kubeconfig":
				live.Kubeconfig = "foreign"
			case "maintained":
				badcfg = deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "foreign"}}
			case "bad-boolean":
				live.Operator["PAYPAL_ENABLED"] = "maybe"
			case "missing-input":
				delete(selected.Operator, "PAYPAL_ENABLED")
			case "extra-input":
				selected.Operator["OTHER_ENABLED"] = createNormalTailRuntimeValue{true, "false"}
			case "absent-with-value":
				selected.Operator["PAYPAL_ENABLED"] = createNormalTailRuntimeValue{false, "foreign"}
			}
			if mutation == "missing-input" || mutation == "extra-input" || mutation == "absent-with-value" {
				if _, err := createNormalTailRuntimeEffectiveSnapshot(source, selected); err == nil {
					t.Fatal("unqualified selected input set accepted")
				}
				return
			}
			if _, err := qualifyCreateNormalTailRuntimeInputs(source, badcfg, live); err == nil {
				t.Fatal("unrelated or invalid input change accepted")
			}
		})
	}
}
