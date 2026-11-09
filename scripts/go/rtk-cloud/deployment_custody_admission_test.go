package main

import (
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCustodyAdmissionBatchesOriginalClientAndServerUnderOneScope(t *testing.T) {
	ns := "video-cloud-dev-video-cloud"
	owner := environmentCustodyOwnerBinding{ID: "pki-controller", Namespace: ns, ServiceAccount: "pki-controller", StorageAliases: []string{"pki-controller-state"},
		Clients: []environmentCustodyClientBinding{{Subject: "service:pki-controller", StatePath: "/state/client.json"}},
		Servers: []environmentCustodyServerBinding{{Domain: "service", Name: "pki-controller." + ns + ".svc", StatePath: "/state/server.json", DNSNames: "pki-controller." + ns + ".svc"}},
	}
	cfg := deploymentConfig{Environment: "dev", Values: map[string]string{
		"CLOUD_STACK_NAME": "video-cloud-dev", "PKI_STATE_BACKEND": "openbao", "PKI_STATE_KV_MOUNT": "identities", "PKI_STATE_LEASE_DURATION": "30s", "DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS": "120", "DEPLOYMENT_UPGRADE_WINDOW_SECONDS": "300",
	}}
	binding := &environmentCustodyBinding{Generation: strings.Repeat("a", 64), OpenBaoRootSHA256: strings.Repeat("b", 64), ServiceRootSHA256: strings.Repeat("c", 64), Owners: []environmentCustodyOwnerBinding{owner}}
	source := deploymentplan.Object{"serviceAccountName": owner.ServiceAccount, "securityContext": environmentCreateServicePodSecurity(),
		"containers": []any{deploymentplan.Object{"securityContext": environmentCreateServiceContainerSecurity()}},
	}
	records, err := environmentCustodyAdmissionRecords(binding, owner, "")
	if err != nil || len(records) != 2 {
		t.Fatal("original client and server references were not retained", err)
	}
	batch := environmentCustodyAdmissionBatch{ValidUntil: time.Now().UTC().Add(time.Minute), Records: records}
	image := "ghcr.io/fixture/native@sha256:" + strings.Repeat("d", 64)
	job, err := environmentCustodyAdmissionJob(cfg, binding, owner, source, image, "", batch)
	if err != nil {
		t.Fatal(err)
	}
	pod := environmentPKIPod(job)
	c := managedNamedField(pod["containers"], "inspect")
	command := certIssuerObjectList(c["command"])
	var wire environmentCustodyAdmissionBatch
	if len(command) != 3 || command[1] != "inspect-admitted-batch" ||
		decodeEnvironmentJSON([]byte(deploymentplan.String(command[2])), &wire) != nil || deploymentplan.Digest(wire) != deploymentplan.Digest(batch) {
		t.Fatal("native Job did not receive the exact public batch")
	}
	if pod["serviceAccountName"] != owner.ServiceAccount || mqttFoundationEnv(c, "PKI_STATE_RECORD_PREFIX") != "dev/"+binding.Generation+"/"+owner.ID ||
		managedNamedField(pod["volumes"], "identity") != nil || mqttFoundationEnv(c, "PKI_INSPECTION_IDENTITY_STATE") != "" {
		t.Fatal("native admission lost original authentication or retained a private projection")
	}
	batch.Records[1].Names = []string{"other.example"}
	if _, err := environmentCustodyAdmissionJob(cfg, binding, owner, source, image, "", batch); err == nil {
		t.Fatal("batch admitted a server name outside the original DNS policy")
	}
}

func TestCustodyAdmissionDefersOnlyUnprojectedQualifiedProduct(t *testing.T) {
	root := strings.Repeat("c", 64)
	owner := environmentCustodyOwnerBinding{ID: "video-cloud-logingester", StorageAliases: []string{"pki-product-logger-identity"}}
	record := environmentCustodyAdmissionRecord{
		Path: environmentCreateServiceOwnerIdentityPath("logger", "client.json"), Subject: "service:logger", Root: root,
	}
	outputs := []environmentCreateProductIdentity{{Subject: record.Subject, Claim: owner.StorageAliases[0], RootSHA256: root}}
	if !environmentCustodyAdmissionDeferred(owner, nil, record, outputs) {
		t.Fatal("qualified, not yet projected product identity should be covered by its producer")
	}
	source := deploymentplan.Object{"containers": []any{
		deploymentplan.Object{"name": "app"},
		deploymentplan.Object{"name": "registration", "env": environmentCreateEnv(map[string]string{"PKI_REGISTRATION_IDENTITY_STATE": record.Path})},
	}}
	if environmentCustodyAdmissionDeferred(owner, source, record, outputs) {
		t.Fatal("a live registration already using this path must pass committed admission")
	}
	core := record
	core.Subject = "service:video-cloud-logingester"
	if environmentCustodyAdmissionDeferred(owner, nil, core, outputs) || environmentCustodyAdmissionDeferred(owner, nil, record, nil) {
		t.Fatal("a product producer must not defer a core or ordinary health requirement")
	}
}
