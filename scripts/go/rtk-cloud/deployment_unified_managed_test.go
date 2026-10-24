package main

import (
	"encoding/base64"
	"errors"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func managedTestPod(object deploymentplan.Object) map[string]any {
	return deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])["spec"])
}

func TestControllerMaintainedPolicyRejectsMissingConsumersAndDisabledRefresh(t *testing.T) {
	for _, envName := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "reordered-domains", "missing-domain", "duplicate-consumer", "service-subject", "disabled-refresh", "missing-refresh-domain", "duplicate-refresh-domain", "global-policy", "invalid-bundle", "missing-auth-policy", "production-no-mfa"} {
			t.Run(envName+"/"+mode, func(t *testing.T) {
				env, bindings, _ := completeEnvironmentRenderFixture(envName)
				var controller environmentPodBindings
				for _, b := range bindings.Pods {
					if b.Component == "pki-controller" {
						controller = b
					}
				}
				switch mode {
				case "reordered-domains":
					env["PKI_SERVER_CRL_DOMAIN"] = "mqtt, openbao_tls, service"
				case "missing-domain":
					delete(env, "PKI_REQUIRED_CONSUMERS_SERVICE")
				case "duplicate-consumer":
					env["PKI_REQUIRED_CONSUMERS_SERVICE"] = "controller, controller"
				case "service-subject":
					env["PKI_REQUIRED_CONSUMERS_SERVICE"] = "service:controller"
				case "disabled-refresh":
					env["PKI_APP_CRL_WORKER_ENABLED"] = "false"
				case "missing-refresh-domain":
					env["PKI_SERVER_CRL_DOMAIN"] = "service,mqtt"
				case "duplicate-refresh-domain":
					env["PKI_SERVER_CRL_DOMAIN"] = "service,mqtt,openbao_tls,mqtt"
				case "global-policy":
					env["PKI_REQUIRED_CONSUMERS"] = "legacy-global"
				case "invalid-bundle":
					env["PKI_REQUIRED_BUNDLE_CONSUMERS_DEVICE"] = "unknown/path"
				case "missing-auth-policy":
					delete(env, "PKI_REQUIRE_USER_MFA")
				case "production-no-mfa":
					env["PKI_REQUIRE_USER_MFA"] = "false"
				}
				object, err := renderManagedEnvironmentComponent(env, controller)
				if err != nil {
					t.Fatal(err)
				}
				err = applyEnvironmentControllerPolicy(env, object)
				allowed := mode == "valid" || mode == "reordered-domains" || mode == "production-no-mfa" && envName != "prod"
				if (err == nil) != allowed {
					t.Fatal("incorrect controller process policy decision", err)
				}
				if err != nil {
					var blocker *deploymentplan.Blocker
					if !errors.As(err, &blocker) || blocker.ID != "deployment.pki.controller-policy" || blocker.Resource == "" || blocker.Next == "" {
						t.Fatal("missing policy repair and owner", err)
					}
					return
				}
				c := managedNamedField(managedTestPod(object)["containers"], "pki-controller")
				for _, domain := range managedPKIDomains {
					if managedNamedField(c["env"], "PKI_REQUIRED_CONSUMERS_"+strings.ToUpper(domain))["value"] != "fixture-consumer" || managedNamedField(c["env"], "PKI_REQUIRED_BUNDLE_CONSUMERS_"+strings.ToUpper(domain))["value"] != "" {
						t.Fatal("a domain lost its maintained policy or inherited an old bundle policy")
					}
				}
				if managedNamedField(c["env"], "PKI_REQUIRED_CONSUMERS")["value"] != "" || managedNamedField(c["env"], "PKI_APP_CRL_WORKER_ENABLED")["value"] != "true" || managedNamedField(c["env"], "PKI_SERVER_CRL_DOMAIN")["value"] != "service,mqtt,openbao_tls" {
					t.Fatal("global environment imports or disabled workers can bypass maintained policy")
				}
			})
		}
	}
}

func TestIdentityBindingsCannotSupplyControllerPolicies(t *testing.T) {
	for _, name := range []string{"PKI_REQUIRED_CONSUMERS_SERVICE", "PKI_APP_CRL_WORKER_ENABLED", "PKI_REQUIRE_USER_MFA"} {
		for _, reference := range []bool{false, true} {
			_, b, _ := completeEnvironmentRenderFixture("dev")
			setting := deploymentplan.Object{"name": name, "value": "false"}
			if reference {
				delete(setting, "value")
				setting["valueFrom"] = deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "legacy-policy", "key": name}}
			}
			b.Pods[0].Containers[0].Fields["env"] = []any{setting}
			if err := validateEnvironmentIdentityBindings(b, "dev"); err == nil {
				t.Fatal("private bindings can override maintained process policy", name, reference)
			}
		}
	}
}

func TestManagedConsumersKeepMaintainedConfigurationAndDurableIdentityForEveryEnvironment(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-render-fixture")
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, bindings, release := completeEnvironmentRenderFixture(environment)
			env["VIDEO_CLOUD_API_EFFECTIVE_REPLICAS"] = "3"
			bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			restore := installAllCredentialEnvironment(env)
			defer restore()
			definitions, err := renderEnvironmentResources(env, bindings, release)
			if err != nil {
				t.Fatal(err)
			}
			objects := map[string]deploymentplan.Object{}
			for _, definition := range definitions {
				objects[deploymentplan.ID(definition.Object)] = definition.Object
			}
			namespace := bindings.Stack + "-video-cloud"
			api := objects["Deployment/"+namespace+"/video-cloud-api-pki"]
			ordinary := objects["Deployment/"+namespace+"/video-cloud-api"]
			if deploymentplan.Map(api["spec"])["replicas"] != deploymentplan.Map(ordinary["spec"])["replicas"] || deploymentplan.Map(api["spec"])["replicas"] != float64(3) || deploymentplan.Digest(deploymentplan.Map(api["spec"])["strategy"]) != deploymentplan.Digest(deploymentplan.Map(ordinary["spec"])["strategy"]) {
				t.Fatal("managed API overrides maintained capacity or rollout policy")
			}
			apiPod := managedTestPod(api)
			app := managedNamedField(apiPod["containers"], "app")
			baseApp := deploymentplan.Map(certIssuerObjectList(managedTestPod(ordinary)["containers"])[0])
			if app == nil || apiPod["automountServiceAccountToken"] != false {
				t.Fatal("API identity or token policy absent")
			}
			for _, item := range certIssuerObjectList(baseApp["env"]) {
				name := deploymentplan.String(deploymentplan.Map(item)["name"])
				// Ordinary service settings are inherited from maintained configuration,
				// while managed identity/authentication has an explicit overlay.
				if name == "VIDEO_CLOUD_DB_DSN" || name == "VIDEO_CLOUD_DB_MAX_OPEN_CONNS" || name == "VIDEO_CLOUD_DB_MAX_IDLE_CONNS" {
					if deploymentplan.Digest(item) != deploymentplan.Digest(managedNamedField(app["env"], name)) {
						t.Fatal("managed API lost ordinary database settings", name)
					}
				}
			}
			for name, want := range map[string]string{"VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED": "true", "VIDEO_CLOUD_AUTH_MTLS_REQUIRED": "true", "VIDEO_CLOUD_AUTH_DISABLE_ACL": "false", "VIDEO_CLOUD_AUTH_TRUSTED_CLIENT_CERT_HEADERS": "false", "VIDEO_CLOUD_AUTH_ENABLE_LEGACY_CERT": "false", "VIDEO_CLOUD_DB_ENSURE_SCHEMA": "false"} {
				if managedNamedField(app["env"], name)["value"] != want {
					t.Fatal("API unsafe authentication or schema default", name)
				}
			}
			if managedNamedField(apiPod["volumes"], "pki-server")["secret"] == nil {
				t.Fatal("API private leaf is not a Secret mount")
			}
			controller := managedTestPod(objects["Deployment/"+namespace+"/pki-controller"])
			c := managedNamedField(controller["containers"], "pki-controller")
			if controller["serviceAccountName"] != "pki-controller" || controller["automountServiceAccountToken"] != false {
				t.Fatal("controller service-account authorization absent")
			}
			ref := deploymentplan.Map(deploymentplan.Map(managedNamedField(c["env"], "PKI_DATABASE_URL")["valueFrom"])["secretKeyRef"])
			if ref["name"] != "pki-controller-database" || ref["key"] != "url" {
				t.Fatal("controller database credential is not independently managed")
			}
			projected := deploymentplan.Map(managedNamedField(controller["volumes"], "openbao-auth")["projected"])
			token := deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(projected["sources"])[0])["serviceAccountToken"])
			if token["audience"] != "openbao" || token["expirationSeconds"] != float64(3600) {
				t.Fatal("controller OpenBao token is not bound and short-lived")
			}
			if objects["Service/"+namespace+"/pki-controller"] == nil || objects["ServiceAccount/"+namespace+"/pki-controller"] == nil {
				t.Fatal("controller owned dependencies omitted")
			}
			mqtt := managedTestPod(objects["Deployment/"+namespace+"/mqtt-pki"])
			for name, claim := range map[string]string{"data": "mqtt-pki-data", "pki-state": "mqtt-pki-trust", "mqtt-host-state": "mqtt-pki-host-identity"} {
				volume := managedNamedField(mqtt["volumes"], name)
				if volume["emptyDir"] != nil || deploymentplan.Map(volume["persistentVolumeClaim"])["claimName"] != claim {
					t.Fatal("MQTT durable identity or data became ephemeral", name)
				}
			}
			primary, broker := managedNamedField(mqtt["containers"], "mqtt"), managedNamedField(mqtt["containers"], "pkibroker")
			if primary == nil || broker == nil || primary["image"] != release.Images["LKE_MQTT_PKI_IMAGE"].Reference || broker["image"] != release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference {
				t.Fatal("managed MQTT consumer roles or images swapped")
			}
			for _, container := range []map[string]any{primary, broker} {
				if managedNamedField(container["env"], "PKI_DATABASE_URL")["valueFrom"] == nil {
					t.Fatal("MQTT PKI database credential leaked inline")
				}
			}
			if managedNamedField(primary["env"], "EMQX_PKI_HOST_IDENTITY_STATE")["value"] != "/var/lib/emqx-pki/identity/state.json" {
				t.Fatal("MQTT host identity outside its persistent mount")
			}
		})
	}
}

func TestManagedCallbackPolicyExcludesIngressAndOtherWorkloads(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		env, bindings, _ := completeEnvironmentRenderFixture(environment)
		objects := map[string]deploymentplan.Object{}
		for _, d := range environmentManagedConsumerDependencies(env) {
			objects[deploymentplan.ID(d.Object)] = d.Object
		}
		namespace := bindings.Stack + "-video-cloud"
		api := objects["Service/"+namespace+"/video-cloud-api-pki"]
		if deploymentplan.Map(deploymentplan.Map(api["spec"])["selector"])["app.kubernetes.io/name"] != "video-cloud-api-pki" {
			t.Fatal("managed endpoint still selects legacy API")
		}
		policy := objects["NetworkPolicy/"+namespace+"/allow-video-cloud-api-pki"]
		privateRules := 0
		for _, value := range certIssuerObjectList(deploymentplan.Map(policy["spec"])["ingress"]) {
			rule := deploymentplan.Map(value)
			ports := certIssuerObjectList(rule["ports"])
			if len(ports) != 1 {
				t.Fatal("a rule grants unbounded listener access")
			}
			if deploymentplan.Map(ports[0])["port"] != float64(18447) {
				continue
			}
			privateRules++
			peers := certIssuerObjectList(rule["from"])
			if len(peers) != 1 {
				t.Fatal("callback admits extra peers")
			}
			peer := deploymentplan.Map(peers[0])
			pod := deploymentplan.Map(deploymentplan.Map(peer["podSelector"])["matchLabels"])
			ns := deploymentplan.Map(deploymentplan.Map(peer["namespaceSelector"])["matchLabels"])
			if pod["app.kubernetes.io/name"] != "mqtt-pki" || ns["kubernetes.io/metadata.name"] != namespace {
				t.Fatal("private callback admits ingress, another workload or another environment")
			}
		}
		if privateRules != 1 {
			t.Fatal("private callback rule missing or duplicated")
		}
	}
}
