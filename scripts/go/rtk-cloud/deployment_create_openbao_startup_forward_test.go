package main

import (
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestOpenBaoStartupForwardNeverReissuesIdentity(t *testing.T) {
	stack := "video-cloud-dev"
	job := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": "pki-enroll-openbao-server-original", "namespace": stack + "-secrets"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "enroll", "command": []any{"/app/openbaopkihost", "bootstrap"}, "env": environmentCreateEnv(map[string]string{"OPENBAO_PKI_HOST_IDENTITY_STATE": "/var/lib/openbao-pki/identity/state.json"})}}}}}}
	inputs := deploymentplan.Inputs{Environment: "dev", Stack: stack, Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}
	original, err := deploymentplan.Compile(inputs, []deploymentplan.Definition{{Object: job, Phase: "bootstrap"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	id := deploymentplan.ID(job)
	journal := deploymentplan.Journal{PlanSHA256: original.SHA256(), Failed: id, Owners: map[string]string{id: "346e6133-4e39-4036-9230-ac1a2ccb473a"}}
	forward, err := compileOpenBaoStartupForward(original, journal, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(forward.Resources) != 1 || forward.Resources[0].ID == id {
		t.Fatal("failed immutable Job was reused")
	}
	c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(forward.Resources[0].Desired)["containers"])[0])
	command := certIssuerObjectList(c["command"])
	if len(command) != 2 || command[0] != "/app/openbaopkihost" || command[1] != "prepare-startup" || mqttFoundationEnv(c, "OPENBAO_PKI_STARTUP_TRUST") != "/var/lib/openbao-pki/identity/startup-trust.json" {
		t.Fatal("forward Job can reissue an identity or lacks its original startup path")
	}
	journal.Completed = []string{id}
	if _, err := compileOpenBaoStartupForward(original, journal, nil); err == nil {
		t.Fatal("accepted a different original write history")
	}
}
