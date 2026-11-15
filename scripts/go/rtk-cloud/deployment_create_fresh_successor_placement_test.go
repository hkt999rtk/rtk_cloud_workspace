package main

import (
	"encoding/json"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestFreshSuccessorRenderKeepsOriginalMQTTEnrollmentPlacement(t *testing.T) {
	defs, original := actualManagedMQTTConsumersEnrollmentFixture(t, "dev", true)
	successor := "bbbbbbbb-2222-4333-8444-555555555555"
	var binding environmentFreshProductBinding
	raw, _ := json.Marshal(map[string]string{"SessionID": successor, "EnrollmentSessionID": original})
	if err := json.Unmarshal(raw, &binding); err != nil {
		t.Fatal(err)
	}
	setRenewedMQTTACK(defs)
	if err := bindEnvironmentManagedMQTTConsumersForRender(defs, &binding); err != nil {
		t.Fatal("original producer placement under renewed ACK", err)
	}
	for _, d := range defs {
		name := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"])
		role := "mqtt"
		if name == "video-cloud-shadowworker" {
			role = "shadow"
		} else if name != "video-cloud-mqttfoundation" {
			continue
		}
		pod := environmentPKIPod(d.Object)
		expected := "pki-product-" + role + "-" + strings.ReplaceAll(original, "-", "")[:12]
		if !strings.Contains(string(mustProviderRecoveryJSON(pod["affinity"])), expected) {
			t.Fatal("original producer affinity lost", name)
		}
		if strings.Contains(string(mustProviderRecoveryJSON(pod["affinity"])), strings.ReplaceAll(successor, "-", "")[:12]) {
			t.Fatal("renewed ACK relocated original identity", name)
		}
	}
}
