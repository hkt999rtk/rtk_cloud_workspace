package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestTransportNativeServiceCRLBindingsPreserveOwnersAndRequireNineConsumers(t *testing.T) {
	cfg, p, release, runtime, service, roots, intermediates, _ := createApplicationFinalizeFixture(t, "dev")
	defs, err := renderEnvironmentCreateTransportDefinitions(context.Background(), cfg, p, runtime, service, roots, intermediates, release)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 7 {
		t.Fatal("native host policy omitted an existing-owner producer")
	}
	controllerID := "Deployment/" + p.Stack + "-video-cloud/pki-controller"
	controller := createRecipeObject(t, defs, controllerID)
	policy := createRecipeEnv(managedNamedField(environmentPKIPod(controller)["containers"], "pki-controller"))["PKI_REQUIRED_CONSUMERS_SERVICE"]
	want := "account-manager,certissuer,emqx-pki,factory-enroll,openbao,pki-controller,pkibroker,video-cloud-api,video-cloud-logingester"
	if policy != want {
		t.Fatal("native Service CRL policy differs from the nine actual managed consumers")
	}
	mqtt := createRecipeObject(t, defs, "Deployment/"+p.Stack+"-video-cloud/mqtt-pki")
	openbao := createRecipeObject(t, defs, "StatefulSet/"+p.Stack+"-secrets/openbao")
	for _, entry := range []struct {
		object                           deploymentplan.Object
		container, prefix, volume, claim string
	}{
		{mqtt, "mqtt", "EMQX_PKI_HOST_RENEWAL", "mqtt-service-state", "mqtt-pki-host-identity"},
		{mqtt, "pkibroker", "PKI_BROKER_IDENTITY", "service-state", "mqtt-pki-trust"},
		{mqtt, "pkibroker", "PKI_BROKER_IDENTITY_RENEWAL", "service-state", "mqtt-pki-trust"},
		{openbao, "openbao-pki", "OPENBAO_PKI_HOST_RENEWAL", "service-state", "openbao-pki-identity"},
	} {
		pod := environmentPKIPod(entry.object)
		c := managedNamedField(pod["containers"], entry.container)
		settings := createRecipeEnv(c)
		for key, value := range map[string]string{"_SERVER_CRL_MANIFEST": environmentCreateServiceTrust + "/outbound-crls.json", "_PKI_CONTROLLER_URL": "https://pki-controller." + p.Stack + "-video-cloud.svc:18446", "_MANAGEMENT_CA": environmentCreateServiceTrust + "/root.pem"} {
			if settings[entry.prefix+key] != value {
				t.Fatal("static transport lacks its real native Service CRL consumer", entry.prefix+key)
			}
		}
		volume := managedNamedField(pod["volumes"], entry.volume)
		mount := managedNamedField(c["volumeMounts"], entry.volume)
		if deploymentplan.Map(volume["persistentVolumeClaim"])["claimName"] != entry.claim || mount["mountPath"] != environmentCreateServiceState || mount["readOnly"] == true {
			t.Fatal("cache alias moved the native private owner")
		}
	}
	for _, item := range []struct {
		object     deploymentplan.Object
		name, path string
	}{
		{mqtt, "prepare-pki-state", "/run/pki-state/private/crl"},
		{mqtt, "prepare-mqtt-host-state", "/var/lib/emqx-pki/private/crl"},
		{openbao, "openbao-pki-install", environmentCreateServiceState + "/private/crl"},
	} {
		initializer := managedNamedField(environmentPKIPod(item.object)["initContainers"], item.name)
		command := mustProviderRecoveryJSON(initializer["command"])
		if !strings.Contains(string(command), "mkdir -p "+item.path) || !strings.Contains(string(command), "chmod 700 ") {
			t.Fatal("native cache lock parent is not privately prepared")
		}
	}
	trust := createRecipeObject(t, defs, "ConfigMap/"+p.Stack+"-secrets/pki-create-service-trust")
	var records []struct {
		Issuer map[string]any `json:"issuer"`
		Path   string         `json:"state_path"`
	}
	if json.Unmarshal([]byte(deploymentplan.String(deploymentplan.Map(trust["data"])["outbound-crls.json"])), &records) != nil || len(records) != 2 {
		t.Fatal("original Service root and intermediate CRL coverage omitted")
	}
	for _, record := range records {
		if !strings.HasPrefix(record.Path, environmentCreateServiceState+"/private/crl/") {
			t.Fatal("manifest escapes original private cache")
		}
	}
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: p.Environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(defs)}, defs, nil)
	if err != nil {
		t.Fatal(err)
	}
	at := map[string]int{}
	for i, r := range plan.Resources {
		at[r.ID] = i
	}
	if at[controllerID] >= at[deploymentplan.ID(mqtt)] || at[controllerID] >= at[deploymentplan.ID(openbao)] || at[deploymentplan.ID(trust)] >= at[deploymentplan.ID(openbao)] {
		t.Fatal("managed hosts start before Controller policy/CRL producer readiness")
	}
}

func TestTransportServicePolicyChangesOnlyAcceptedOwnerPublicFields(t *testing.T) {
	cfg, p, _, runtime, service, _, _, _ := createApplicationFinalizeFixture(t, "dev")
	core, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	controller := createRecipeObject(t, core, "Deployment/"+p.Stack+"-video-cloud/pki-controller")
	deploymentplan.Map(deploymentplan.Map(controller["spec"])["template"])["metadata"] = deploymentplan.Object{"annotations": deploymentplan.Object{"original-retained-hash": "accepted"}}
	before := deploymentplan.Digest(controller)
	updated, err := configureCreateTransportServiceConsumerController(p, controller)
	if err != nil {
		t.Fatal(err)
	}
	if deploymentplan.Digest(controller) != before {
		t.Fatal("public policy transformer mutated its accepted source")
	}
	c := managedNamedField(environmentPKIPod(updated)["containers"], "pki-controller")
	old := createRecipeEnv(managedNamedField(environmentPKIPod(controller)["containers"], "pki-controller"))["PKI_REQUIRED_CONSUMERS_SERVICE"]
	c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_REQUIRED_CONSUMERS_SERVICE": old}))
	if deploymentplan.Digest(updated) != before {
		t.Fatal("consumer policy replacement changed another original owner field")
	}
	ids, err := managedTransportServiceConsumerIDs(strings.Split("account-manager,certissuer,emqx-pki,factory-enroll,openbao,pki-controller,pkibroker,video-cloud-api,video-cloud-logingester", ","))
	if err != nil || len(ids) != 9 {
		t.Fatal("final policy repeated/removed an installed consumer")
	}
}

func TestTransportServerEnrollmentRetainsOriginalStaticPolicyBeforeRuntimeConsumers(t *testing.T) {
	cfg, p, release, runtime, service, roots, intermediates, _ := createApplicationFinalizeFixture(t, "dev")
	jobs, err := renderEnvironmentCreateTransportServerDefinitions(context.Background(), cfg, p, runtime, service, roots, intermediates)
	if err != nil {
		t.Fatal(err)
	}
	checked := 0
	for _, d := range jobs {
		if d.Object["kind"] != "Job" {
			continue
		}
		c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(d.Object)["containers"])[0])
		settings := createRecipeEnv(c)
		prefix := "EMQX_PKI_HOST_RENEWAL"
		if strings.Contains(deploymentplan.ID(d.Object), "openbao-server") {
			prefix = "OPENBAO_PKI_HOST_RENEWAL"
		}
		if settings["PKI_BOOTSTRAP_SESSION_ID"] != "" || settings[prefix+"_SERVER_CRL_MANIFEST"] != "" || settings[prefix+"_PKI_CONTROLLER_URL"] != "" || settings[prefix+"_MANAGEMENT_CA"] != "" {
			t.Fatal("completed enrollment recipe gained runtime installation/CRL side effects")
		}
		if settings[prefix+"_TLS_CA"] != environmentCreateServiceTrust+"/root.pem" || settings[prefix+"_SERVER_PKI_ROOT_SHA256"] != service.Root.RootFingerprint {
			t.Fatal("server enrollment lost its original exact TLS/registry policy")
		}
		checked++
	}
	if checked != 2 {
		t.Fatal("both original native server enrollment owners were not checked")
	}
	defs, err := renderEnvironmentCreateTransportDefinitions(context.Background(), cfg, p, runtime, service, roots, intermediates, release)
	if err != nil {
		t.Fatal(err)
	}
	for _, owner := range []struct{ id, name, prefix string }{
		{"Deployment/" + p.Stack + "-video-cloud/mqtt-pki", "mqtt", "EMQX_PKI_HOST_RENEWAL"},
		{"StatefulSet/" + p.Stack + "-secrets/openbao", "openbao-pki", "OPENBAO_PKI_HOST_RENEWAL"},
	} {
		object := createRecipeObject(t, defs, owner.id)
		settings := createRecipeEnv(managedNamedField(environmentPKIPod(object)["containers"], owner.name))
		if settings["PKI_BOOTSTRAP_SESSION_ID"] != service.BootstrapSessionID || settings[owner.prefix+"_SERVER_CRL_MANIFEST"] != environmentCreateServiceTrust+"/outbound-crls.json" || settings[owner.prefix+"_PKI_CONTROLLER_URL"] == "" || settings[owner.prefix+"_MANAGEMENT_CA"] == "" {
			t.Fatal("installed runtime host omitted its actual native ACK consumer")
		}
	}
}
