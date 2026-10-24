package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func deviceCallbackFixture(t *testing.T, environment string) deploymentplan.Object {
	t.Helper()
	env, bindings, release := completeEnvironmentRenderFixture(environment)
	for _, binding := range bindings.Pods {
		if binding.Component != "managed-api" {
			continue
		}
		object, err := renderManagedEnvironmentComponent(env, binding)
		if err != nil {
			t.Fatal(err)
		}
		if err := applyEnvironmentPodBindings(object, bindings, release); err != nil {
			t.Fatal(err)
		}
		return object
	}
	t.Fatal("Device API fixture missing")
	return nil
}

func TestDeviceMQTTCallbackDerivesOriginalMountAcrossEnvironments(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			o := deviceCallbackFixture(t, environment)
			if err := validateEnvironmentDeviceMQTTCallback([]deploymentplan.Definition{{Object: o}}); err == nil {
				t.Fatal("original admission-only candidate missed incomplete callback settings")
			}
			pod := environmentPKIPod(o)
			app := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
			managedNamedField(pod["volumes"], "mqtt-callback")["configMap"] = deploymentplan.Object{"name": "original-broker-ca", "items": []any{deploymentplan.Object{"key": "ca.crt", "path": "broker/ca.pem"}}}
			managedNamedField(app["volumeMounts"], "mqtt-callback")["mountPath"] = "/original/callback"
			original := deploymentplan.Clone(o)
			if err := deriveEnvironmentDeviceMQTTCallback(nil, []deploymentplan.Definition{{Object: o}}); err != nil {
				t.Fatal(err)
			}
			if mqttFoundationEnv(app, "VIDEO_CLOUD_MQTT_AUTH_CLIENT_CA") != "/original/callback/broker/ca.pem" || mqttFoundationEnv(app, "VIDEO_CLOUD_MQTT_AUTH_CLIENT_NAME") != "emqx-pki" {
				t.Fatal("callback path ignored the original item mapping or broker client role")
			}
			before := environmentPKIPod(original)
			if !reflect.DeepEqual(before["volumes"], pod["volumes"]) || !reflect.DeepEqual(deploymentplan.Map(certIssuerObjectList(before["containers"])[0])["volumeMounts"], app["volumeMounts"]) {
				t.Fatal("callback derivation replaced original trust, identity or state references")
			}
		})
	}
}

func TestDeviceMQTTCallbackRejectsStartupBlockersWithoutLiveAPI(t *testing.T) {
	for _, problem := range []string{"ca", "client-name", "tls", "auth-secret", "acl", "headers", "address", "writable-ca", "ambiguous-setting"} {
		t.Run(problem, func(t *testing.T) {
			o := deviceCallbackFixture(t, "dev")
			defs := []deploymentplan.Definition{{Object: o}}
			if err := deriveEnvironmentDeviceMQTTCallback(nil, defs); err != nil {
				t.Fatal(err)
			}
			app := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(o)["containers"])[0])
			key := map[string]string{"ca": "VIDEO_CLOUD_MQTT_AUTH_CLIENT_CA", "client-name": "VIDEO_CLOUD_MQTT_AUTH_CLIENT_NAME", "tls": "VIDEO_CLOUD_API_TLS_KEY_PATH", "auth-secret": "VIDEO_CLOUD_MQTT_BROKER_AUTH_KEY", "acl": "VIDEO_CLOUD_AUTH_DISABLE_ACL", "headers": "VIDEO_CLOUD_AUTH_TRUSTED_CLIENT_CERT_HEADERS", "address": "VIDEO_CLOUD_MQTT_AUTH_ADDR"}[problem]
			switch problem {
			case "writable-ca":
				managedNamedField(app["volumeMounts"], "mqtt-callback")["readOnly"] = false
			case "ambiguous-setting":
				app["env"] = append(certIssuerObjectList(app["env"]), deploymentplan.Object{"name": "VIDEO_CLOUD_MQTT_AUTH_CLIENT_CA", "value": "private-canary"})
			case "auth-secret":
				setting := managedNamedField(app["env"], key)
				delete(setting, "valueFrom")
				setting["value"] = "private-canary"
			default:
				managedNamedField(app["env"], key)["value"] = "private-canary"
			}
			err := validateEnvironmentDeviceMQTTCallback(defs)
			var blocker *deploymentplan.Blocker
			if !errors.As(err, &blocker) || blocker.ID != "deployment.configuration.mqtt-callback" || blocker.Resource == "" || blocker.Next == "" || strings.Contains(err.Error(), "private-canary") {
				t.Fatal("startup blocker lacked safe actionable candidate evidence", err)
			}
		})
	}
}

func TestDeviceMQTTCallbackQualificationChecksOriginalPublicCA(t *testing.T) {
	for _, problem := range []string{"valid", "changed-owner", "missing-ca", "invalid-ca", "read-denied"} {
		t.Run(problem, func(t *testing.T) {
			o := deviceCallbackFixture(t, "dev")
			if err := deriveEnvironmentDeviceMQTTCallback(nil, []deploymentplan.Definition{{Object: o}}); err != nil {
				t.Fatal(err)
			}
			id := "ConfigMap/video-cloud-dev-video-cloud/pki-mqtt-callback-ca"
			plan := &deploymentplan.Plan{Resources: []deploymentplan.Resource{{ID: deploymentplan.ID(o), Desired: o, Action: "update"}, {ID: id, UID: "original-ca", Action: "retain", Desired: deploymentplan.Object{"kind": "ConfigMap"}}}}
			source := deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "original-ca"}, "data": deploymentplan.Object{"ca.crt": string(testOpenBaoCAPEM(t))}}
			if problem == "changed-owner" {
				deploymentplan.Map(source["metadata"])["uid"] = "other-ca"
			}
			if problem == "missing-ca" || problem == "invalid-ca" {
				deploymentplan.Map(source["data"])["ca.crt"] = ""
				if problem == "invalid-ca" {
					deploymentplan.Map(source["data"])["ca.crt"] = "private-canary"
				}
			}
			err := checkEnvironmentDeviceMQTTCallback(context.Background(), plan, func(_ context.Context, requested string) (deploymentplan.Object, error) {
				if requested != id {
					t.Fatal("qualification depends on a live API instead of original CA inputs", requested)
				}
				if problem == "read-denied" {
					return nil, errors.New("private-canary")
				}
				return source, nil
			})
			if (err == nil) != (problem == "valid") || err != nil && strings.Contains(err.Error(), "private-canary") {
				t.Fatal("incorrect original public CA qualification", err)
			}
		})
	}
}

func TestNativeDeviceCallbackRequiresOriginalManagedCallerAdmission(t *testing.T) {
	_, p, _, _, service, definitions := createdNativeInventoryFixture(t, "dev")
	id := "Deployment/" + p.Stack + "-video-cloud/video-cloud-api-pki"
	original := createRecipeObject(t, definitions, id)
	ca := createRecipeObject(t, definitions, "ConfigMap/"+p.Stack+"-video-cloud/pki-create-mqtt-callback-ca")
	if deploymentplan.Map(ca["data"])["ca.crt"] != service.Root.Certificate {
		t.Fatal("fresh callback borrowed another domain's trust instead of its actual enrolled Service Root")
	}
	if err := validateEnvironmentDeviceMQTTCallback([]deploymentplan.Definition{{Object: original}}); err != nil {
		t.Fatal("actual fresh native callback cannot start", err)
	}
	for _, problem := range []string{"legacy-caller", "foreign-caller", "missing-marker", "foreign-marker", "legacy-initializer", "readonly-identity", "missing-client-root"} {
		t.Run(problem, func(t *testing.T) {
			object := deploymentplan.Clone(original)
			pod, consumer := environmentPKIPod(object), mqttFoundationPrimary(object)
			switch problem {
			case "legacy-caller":
				managedNamedField(consumer["env"], "VIDEO_CLOUD_MQTT_AUTH_CLIENT_NAME")["value"] = "emqx-pki"
			case "foreign-caller":
				managedNamedField(consumer["env"], "VIDEO_CLOUD_MQTT_AUTH_CLIENT_NAME")["value"] = "service:another-consumer"
			case "missing-marker":
				consumer["env"] = environmentCreateServiceWithoutEnv(consumer["env"], "VIDEO_CLOUD_AUTH_ROOT_TRUST_BOOTSTRAP_STATE")
			case "foreign-marker":
				managedNamedField(consumer["env"], "VIDEO_CLOUD_AUTH_ROOT_TRUST_BOOTSTRAP_STATE")["value"] = "/another-owner/incoming-root-bootstrap.json"
			case "legacy-initializer":
				managedNamedField(pod["initContainers"], "initialize-trust-state")["command"] = []any{"/bin/sh", "-ec", deviceTrustPreparation(mqttFoundationEnv(consumer, "VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_STATE"))}
			case "readonly-identity":
				managedNamedField(consumer["volumeMounts"], "controller-identity")["readOnly"] = true
			case "missing-client-root":
				consumer["env"] = environmentCreateServiceWithoutEnv(consumer["env"], "VIDEO_CLOUD_CONTROLLER_IDENTITY_ROOT_SHA256")
			}
			if err := validateEnvironmentDeviceMQTTCallback([]deploymentplan.Definition{{Object: object}}); err == nil {
				t.Fatal("native callback accepted a different caller or missing original native admission")
			}
		})
	}
	legacy := deviceCallbackFixture(t, "dev")
	if err := deriveEnvironmentDeviceMQTTCallback(nil, []deploymentplan.Definition{{Object: legacy}}); err != nil || mqttFoundationEnv(mqttFoundationPrimary(legacy), "VIDEO_CLOUD_MQTT_AUTH_CLIENT_NAME") != "emqx-pki" {
		t.Fatal("fresh callback mapping changed the original legacy broker caller", err)
	}
}
