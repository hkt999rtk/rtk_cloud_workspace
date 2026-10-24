package main

import (
	"context"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedMQTTDependenciesReuseOriginalCredentialsWithoutLegacyTLS(t *testing.T) {
	t.Setenv("LKE_EMQX_NODE_COOKIE", "")
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			values := map[string]string{"CLOUD_ENV_NAME": environment, "CLOUD_STACK_NAME": "video-cloud-" + environment, "LKE_EMQX_NODE_COOKIE": "original-declared-cookie"}
			secrets := map[string]string{"emqx-dashboard-password": "original-dashboard", "mqtt-broker-auth": "original-callback-token", "emqx-handoff-api-key": "original-api-key", "emqx-handoff-api-secret": "original-api-secret"}
			before := deploymentplan.Digest(secrets)
			defs, err := renderManagedMQTTDependencyDefinitions(values, secrets)
			if err != nil {
				t.Fatal(err)
			}
			if len(defs) != 2 || deploymentplan.Digest(secrets) != before {
				t.Fatal("producer changed original runtime snapshot or omitted dependency")
			}
			cm := createRecipeObject(t, defs, "ConfigMap/"+values["CLOUD_STACK_NAME"]+"-video-cloud/mqtt-pki-config")
			hocon := deploymentplan.String(deploymentplan.Map(cm["data"])["base.hocon"])
			if !strings.Contains(hocon, `bootstrap_file = "/run/mqtt-pki/api-keys.conf"`) || strings.Contains(hocon, "/opt/emqx/etc/certs/") {
				t.Fatal("EMQX API bootstrap does not use its managed runtime mount")
			}
			if !strings.Contains(hocon, "authentication_settings.node_cache.enable = false") || !strings.Contains(hocon, "authorization.cache.enable = false") {
				t.Fatal("managed broker omitted the native PKI session cache policy")
			}
			for _, credential := range append([]string{values["LKE_EMQX_NODE_COOKIE"]}, secrets["emqx-dashboard-password"], secrets["mqtt-broker-auth"], secrets["emqx-handoff-api-key"], secrets["emqx-handoff-api-secret"]) {
				if strings.Contains(hocon, credential) {
					t.Fatal("public broker configuration contains private process credential")
				}
			}
			secret := createRecipeObject(t, defs, "Secret/"+values["CLOUD_STACK_NAME"]+"-video-cloud/mqtt-pki-runtime")
			data := deploymentplan.Map(secret["stringData"])
			if len(data) != 4 {
				t.Fatal("managed runtime added legacy TLS keys or omitted original process settings")
			}
			decode := func(key string) string { return deploymentplan.String(data[key]) }
			if decode("cookie") != "original-declared-cookie" || decode("dashboard-password") != secrets["emqx-dashboard-password"] || decode("api-keys.conf") != "original-api-key:original-api-secret:administrator\n" || !strings.Contains(decode("authentication"), secrets["mqtt-broker-auth"]) {
				t.Fatal("original selected environment process credential mapping differs")
			}
		})
	}
}

func TestManagedMQTTDependenciesRejectMissingOriginalCredentials(t *testing.T) {
	values := map[string]string{"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": "video-cloud-dev"}
	original := map[string]string{"emqx-dashboard-password": "original-dashboard", "mqtt-broker-auth": "original-callback", "emqx-handoff-api-key": "original-key", "emqx-handoff-api-secret": "original-secret"}
	for _, key := range []string{"emqx-dashboard-password", "mqtt-broker-auth", "emqx-handoff-api-key", "emqx-handoff-api-secret"} {
		t.Run(key, func(t *testing.T) {
			secrets := map[string]string{}
			for k, v := range original {
				secrets[k] = v
			}
			delete(secrets, key)
			if _, err := renderManagedMQTTDependencyDefinitions(values, secrets); err == nil {
				t.Fatal("missing original runtime value was regenerated or borrowed")
			}
		})
	}
}

func TestCreateManagedMQTTDependenciesPrecedeRetainedHostAndDelayBusinessWorker(t *testing.T) {
	cfg, p, release, runtime, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	defs, err := renderEnvironmentCreateTransportDefinitions(context.Background(), cfg, p, runtime, service, roots, intermediates, release)
	if err != nil {
		t.Fatal(err)
	}
	if len(defs) != 7 {
		t.Fatal("managed hosts lack their maintained configuration and Service policy dependencies")
	}
	mqtt := createRecipeObject(t, defs, "Deployment/"+p.Stack+"-video-cloud/mqtt-pki")
	pod := environmentPKIPod(mqtt)
	broker := managedNamedField(pod["containers"], "pkibroker")
	if command := certIssuerObjectList(broker["command"]); len(command) != 2 || command[1] != "--deployment-bootstrap-trust" {
		t.Fatal("business broker started before final API callback")
	}
	if createRecipeEnv(broker)["PKI_BROKER_APP_PKI_ENABLED"] != "true" || createRecipeEnv(broker)["PKI_BROKER_REQUIRE_CRLS"] != "true" || createRecipeEnv(broker)["PKI_BROKER_MANAGEMENT_CA"] != environmentCreateServiceTrust+"/root.pem" {
		t.Fatal("retained native Device/App trust policy was lost during host transport rollout")
	}
	for _, entry := range certIssuerObjectList(broker["env"]) {
		ref := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(entry)["valueFrom"])["secretKeyRef"])
		if ref["name"] == "mqtt-pki-worker" || ref["name"] == "mqtt-pki-database" || ref["name"] == "video-cloud-workers-runtime" {
			t.Fatal("bootstrap worker uses missing aliases or premature business credential")
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
	for _, id := range []string{"ConfigMap/" + p.Stack + "-video-cloud/mqtt-pki-config", "Secret/" + p.Stack + "-video-cloud/mqtt-pki-runtime"} {
		if at[id] >= at[deploymentplan.ID(mqtt)] {
			t.Fatal("native MQTT starts before its configuration dependency")
		}
	}
	final, err := finalizeEnvironmentCreateApplicationDefinitions(context.Background(), cfg, p, release, runtime, service, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	object := createRecipeObject(t, final, deploymentplan.ID(mqtt))
	pod = environmentPKIPod(object)
	broker = managedNamedField(pod["containers"], "pkibroker")
	host := managedNamedField(pod["containers"], "mqtt")
	if command := certIssuerObjectList(broker["command"]); len(command) != 2 || command[0] != "/app/pkibroker" || command[1] != "watch" {
		t.Fatal("final native broker remains in trust-only bootstrap")
	}
	settings := createRecipeEnv(host)
	if settings["EMQX_PKI_CALLBACK_URL"] != "https://video-cloud-api-pki."+p.Stack+"-video-cloud.svc:18447/v1/internal/mqtt/authenticate" || settings["EMQX_PKI_CALLBACK_CA"] != environmentCreateServiceTrust+"/root.pem" {
		t.Fatal("final callback does not use native HTTPS Service identity")
	}
	for name, key := range map[string]string{"PKI_BROKER_API_KEY": "VIDEO_CLOUD_EMQX_API_KEY", "PKI_BROKER_API_SECRET": "VIDEO_CLOUD_EMQX_API_SECRET"} {
		entry := managedNamedField(broker["env"], name)
		ref := deploymentplan.Map(deploymentplan.Map(entry["valueFrom"])["secretKeyRef"])
		if ref["name"] != "video-cloud-workers-runtime" || ref["key"] != key {
			t.Fatal("final native broker lost its original worker credential")
		}
	}
}
