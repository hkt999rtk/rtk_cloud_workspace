package main

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestEnvironmentMQTTRolesConfiguration(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			workspace := writeDeploymentFixture(t, environment, "lke")
			cfg, err := resolveDeploymentConfig(workspace, environment, "")
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Values["DEVICE_API_MQTT_ENABLED"] != "false" || cfg.Values["CORE_API_MQTT_ENABLED"] != "true" {
				t.Fatal("lost explicit runtime roles")
			}
			override := filepath.Join(workspace, "cloud_env", environment, "overrides", "architecture.env")
			writeTestFile(t, override, "DEVICE_API_MQTT_ENABLED=true\n")
			changed, err := resolveDeploymentConfig(workspace, environment, "")
			if err != nil {
				t.Fatal(err)
			}
			if changed.Values["DEVICE_API_MQTT_ENABLED"] != "true" || deploymentplan.Digest(changed) == deploymentplan.Digest(cfg) {
				t.Fatal("environment override is not bound to plan inputs")
			}
			writeTestFile(t, override, "DEVICE_API_MQTT_ENABLED=perhaps\n")
			if _, err := resolveDeploymentConfig(workspace, environment, ""); err == nil || !strings.Contains(err.Error(), "DEVICE_API_MQTT_ENABLED") {
				t.Fatal("invalid environment policy was accepted", err)
			}
		})
	}
}

func TestEnvironmentMQTTRolesCreateUpgradeAgree(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			cfg, p, release, runtime, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, environment)
			cfg.Values["CORE_API_MQTT_ENABLED"] = "true"
			cfg.Values["DEVICE_API_MQTT_ENABLED"] = "false"
			cfg.Values["APP_API_MQTT_ENABLED"] = "false"
			before := deploymentplan.Digest(initial)
			created, err := finalizeEnvironmentCreateApplicationDefinitions(context.Background(), cfg, p, release, runtime, service, roots, intermediates, initial)
			if err != nil {
				t.Fatal(err)
			}
			if deploymentplan.Digest(initial) != before {
				t.Fatal("changed enrolled evidence")
			}
			env, bindings, _ := completeEnvironmentRenderFixture(environment)
			for key, value := range cfg.Values {
				env[key] = value
			}
			for _, binding := range bindings.Pods {
				if binding.Component != "managed-api" {
					continue
				}
				upgraded, err := renderManagedEnvironmentAPI(env, binding)
				if err != nil {
					t.Fatal(err)
				}
				newObject := createRecipeObject(t, created, "Deployment/"+p.Stack+"-video-cloud/"+binding.Name)
				createValue := mqttFoundationEnv(mqttFoundationPrimary(newObject), "VIDEO_CLOUD_MQTT_ENABLED")
				upgradeValue := mqttFoundationEnv(mqttFoundationPrimary(upgraded), "VIDEO_CLOUD_MQTT_ENABLED")
				if createValue != "false" || upgradeValue != createValue {
					t.Fatalf("same environment produces create=%q upgrade=%q", createValue, upgradeValue)
				}
				delete(env, "DEVICE_API_MQTT_ENABLED")
				if _, err := renderManagedEnvironmentAPI(env, binding); err == nil || !strings.Contains(err.Error(), "DEVICE_API_MQTT_ENABLED") {
					t.Fatal("missing environment policy must identify the missing key", err)
				}
			}
		})
	}
}

// Reproduce the retained Device API profile: MQTT client off, but original
// server DNS/Root and public trust volume still installed.
func TestDisabledMQTTClientRetainsCompleteTrustAcrossCreateUpgrade(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, plan, release, runtime, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, environment)
			name := "video-cloud-api-pki"
			host := "mqtt-pki." + plan.Stack + "-video-cloud.svc"
			pin := strings.Repeat("a", 64)
			mount := environmentCreateServiceMount("mqtt-pki-bundles", "/original/mqtt", true)
			volume := environmentCreateServiceVolume("mqtt-pki-bundles", "configMap", "original-mqtt-trust")
			// A projected filename must be used rather than hardcoding roots.pem.
			deploymentplan.Map(deploymentplan.Map(volume)["configMap"])["items"] = []any{deploymentplan.Object{"key": "roots.pem", "path": "public/root-pool.pem"}}
			stale := map[string]string{"VIDEO_CLOUD_MQTT_SERVER_PKI_NAME": host, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256": pin}
			for _, d := range initial {
				if deploymentplan.Map(d.Object["metadata"])["name"] != name {
					continue
				}
				pod := environmentPKIPod(d.Object)
				c := mqttFoundationPrimary(d.Object)
				pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{volume})
				c["volumeMounts"] = mergeEnvironmentNamedFields(c["volumeMounts"], []any{mount})
				c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(stale))
			}
			original := deploymentplan.Digest(initial)
			var created []deploymentplan.Definition
			// Main's existing bootstrap eligibility is unchanged by this PR;
			// production still exercises the shared upgrade projection below.
			if environment != "prod" {
				var err error
				created, err = finalizeEnvironmentCreateApplicationDefinitions(context.Background(), cfg, plan, release, runtime, service, roots, intermediates, initial)
				if err != nil {
					t.Fatal(err)
				}
			}
			if deploymentplan.Digest(initial) != original {
				t.Fatal("create changed original enrollment evidence")
			}
			env, bindings, images := completeEnvironmentRenderFixture(environment)
			bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "original-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			restore := installAllCredentialEnvironment(env)
			defer restore()
			for i := range bindings.Pods {
				b := &bindings.Pods[i]
				if b.Name != name {
					continue
				}
				b.PodFields["volumes"] = mergeEnvironmentNamedFields(b.PodFields["volumes"], []any{volume})
				b.Containers[0].Fields["volumeMounts"] = mergeEnvironmentNamedFields(b.Containers[0].Fields["volumeMounts"], []any{mount})
				b.Containers[0].Fields["env"] = mergeEnvironmentNamedFields(b.Containers[0].Fields["env"], environmentCreateEnv(stale))
			}
			before := deploymentplan.Digest(bindings)
			upgraded, err := renderEnvironmentResources(env, bindings, images)
			if err != nil {
				t.Fatal(err)
			}
			if deploymentplan.Digest(bindings) != before {
				t.Fatal("upgrade changed original bindings")
			}
			flows := [][]deploymentplan.Definition{upgraded}
			if created != nil {
				flows = append(flows, created)
			}
			for _, defs := range flows {
				o := createRecipeObject(t, defs, "Deployment/"+plan.Stack+"-video-cloud/"+name)
				c := mqttFoundationPrimary(o)
				if mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_ENABLED") != "false" || mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_SERVER_PKI_NAME") != host || mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256") != pin {
					t.Fatal("changed environment role or original trust")
				}
				if mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_TLS") != "true" || mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_TLS_CA_ROOT_PATH") != "/original/mqtt/public/root-pool.pem" {
					t.Fatal("disabled native client retained incomplete TLS trust")
				}
				if err := validateEnvironmentCoreMQTTTransport([]deploymentplan.Definition{{Object: o}}); err != nil {
					t.Fatal(err)
				}
				bad := deploymentplan.Clone(o)
				bc := mqttFoundationPrimary(bad)
				bc["env"] = mergeEnvironmentNamedFields(bc["env"], environmentCreateEnv(map[string]string{"VIDEO_CLOUD_MQTT_TLS": "false"}))
				if err := validateEnvironmentCoreMQTTTransport([]deploymentplan.Definition{{Object: bad}}); err == nil {
					t.Fatal("preflight accepted native startup failure")
				}
				if err := validateEnvironmentPKITransition([]deploymentplan.Definition{{Object: o}}, []deploymentplan.Object{bad}); err != nil {
					t.Fatal("TLS completion changed original identity bindings", err)
				}
				if mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_AUTH_ADDR") != ":18447" {
					t.Fatal("disabled outbound client lost inbound broker authentication callback")
				}
				unsafe := deploymentplan.Clone(o)
				for _, raw := range certIssuerObjectList(mqttFoundationPrimary(unsafe)["volumeMounts"]) {
					m := deploymentplan.Map(raw)
					if m["name"] == "mqtt-pki-bundles" {
						m["readOnly"] = false
					}
				}
				if err := applyEnvironmentMQTTRole(env, unsafe); err == nil {
					t.Fatal("writable trust source silently accepted")
				}
			}
		})
	}
}
