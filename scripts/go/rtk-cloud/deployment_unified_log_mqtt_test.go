package main

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func logMQTTFixture(t *testing.T, environment string) ([]deploymentplan.Definition, []deploymentplan.Object) {
	t.Helper()
	defs, live := coreMQTTFixture(t, environment)
	o := defs[0].Object
	deploymentplan.Map(o["metadata"])["name"] = "video-cloud-logingester"
	pod := environmentPKIPod(o)
	c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	pin := mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256")
	c["env"] = []any{deploymentplan.Object{"name": "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256", "value": pin}, deploymentplan.Object{"name": logMQTTIdentityPrefix + "STATE", "value": "/var/lib/mqtt-pki/identity/original.json"}}
	for _, key := range []string{"ROOT_SHA256", "SERVER_PKI_ROOT_SHA256", "RENEWAL_SERVER_PKI_ROOT_SHA256"} {
		c["env"] = append(certIssuerObjectList(c["env"]), deploymentplan.Object{"name": logMQTTIdentityPrefix + key, "value": pin})
	}
	for _, fields := range [][]any{certIssuerObjectList(pod["volumes"]), certIssuerObjectList(c["volumeMounts"])} {
		for _, raw := range fields {
			field := deploymentplan.Map(raw)
			if field["name"] == "service-identity" {
				field["name"] = "mqtt-pki-state"
				if field["mountPath"] != nil {
					field["mountPath"] = "/var/lib/mqtt-pki"
				}
			}
			if field["name"] == "account-manager-crls" {
				field["name"] = "service-crls"
			}
		}
	}
	for _, source := range live {
		domain, statePath := "", ""
		if name := deploymentplan.Map(source["metadata"])["name"]; name == "mqtt-pki-crls" {
			domain, statePath = "mqtt", "/var/lib/mqtt-pki/crls/original.json"
		} else if name == "account-manager-crls" {
			domain, statePath = "service", "/var/lib/mqtt-pki/identity/service-crls/original.json"
		}
		if domain != "" {
			root := deploymentplan.String(deploymentplan.Map(live[0]["data"])["roots.pem"])
			raw, err := json.Marshal([]any{deploymentplan.Object{"issuer": deploymentplan.Object{"issuer_id": "64062c4a-2bdc-474c-bb11-ab1b61026574", "environment": environment, "trust_domain": domain, "status": "active", "chain_pem": root, "certificate_fingerprint_sha256": pin}, "state_path": statePath}})
			if err != nil {
				t.Fatal(err)
			}
			deploymentplan.Map(source["data"])["crls.json"] = string(raw)
		}
	}
	return defs, live
}

func TestLogMQTTTransportPreservesOriginalIdentityAcrossEnvironments(t *testing.T) {
	for _, envName := range []string{"dev", "staging", "prod"} {
		t.Run(envName, func(t *testing.T) {
			defs, live := logMQTTFixture(t, envName)
			original := deploymentplan.Clone(defs[0].Object)
			if err := validateEnvironmentLogMQTTTransport(defs, envName); err == nil {
				t.Fatal("missing TLS/CA startup failure escaped candidate validation")
			}
			if err := deriveEnvironmentLogMQTTTransport(map[string]string{"CLOUD_ENV_NAME": envName}, defs); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(deploymentplan.Map(defs[0].Object["spec"])["strategy"], deploymentplan.Object{"type": "Recreate"}) {
				t.Fatal("managed original-PVC consumer can start a rolling-surge state writer")
			}
			c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(defs[0].Object)["containers"])[0])
			if mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_TLS") != "true" || mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_TLS_CA_ROOT_PATH") != "/mounted/mqtt-pki-bundles/public/roots.pem" || mqttFoundationEnv(c, logMQTTIdentityPrefix+"STATE") != "/var/lib/mqtt-pki/identity/original.json" || !reflect.DeepEqual(environmentPKIPod(original)["volumes"], environmentPKIPod(defs[0].Object)["volumes"]) {
				t.Fatal("original identity or mount-derived TLS transport changed")
			}
			release := environmentRelease{Environment: envName, Stack: "video-cloud-" + envName, Images: map[string]environmentReleaseImage{"LKE_VIDEO_CLOUD_IMAGE": {Reference: deploymentplan.String(c["image"])}}}
			bindings, err := observeEnvironmentBindings(envName, release, append(live, defs[0].Object))
			if err != nil {
				t.Fatal(err)
			}
			settings, _ := environmentPKINamedFields(bindings.Pods[0].Containers[0].Fields["env"])
			if settings[logMQTTIdentityPrefix+"STATE"]["value"] != "/var/lib/mqtt-pki/identity/original.json" || settings[logMQTTIdentityPrefix+"ROOT_SHA256"]["value"] == nil {
				t.Fatal("next upgrade loses the original LogIngester identity binding")
			}
			plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: envName, Stack: "video-cloud-" + envName, Operation: "upgrade"}}
			for _, d := range defs {
				plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: deploymentplan.ID(d.Object), Desired: d.Object, Action: "update"})
			}
			sources := map[string]deploymentplan.Object{}
			for _, s := range live {
				id := deploymentplan.ID(s)
				plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: id, UID: deploymentplan.String(deploymentplan.Map(s["metadata"])["uid"]), Desired: s, Action: "retain"})
				sources[id] = s
			}
			if err := checkEnvironmentLogMQTTTransport(context.Background(), plan, func(_ context.Context, id string) (deploymentplan.Object, error) {
				if sources[id] == nil {
					t.Fatal("qualification depends on a running workload", id)
				}
				return sources[id], nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLogMQTTRolloutPreflightRejectsSurgeWithoutRunningWorkload(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, fault := range []string{"default-rolling", "explicit-rolling", "rolling-fields", "multiple-writers", "missing-pvc", "read-only-state"} {
			t.Run(environment+"/"+fault, func(t *testing.T) {
				defs, _ := logMQTTFixture(t, environment)
				if err := deriveEnvironmentLogMQTTTransport(map[string]string{"CLOUD_ENV_NAME": environment}, defs); err != nil {
					t.Fatal(err)
				}
				object := defs[0].Object
				spec := deploymentplan.Map(object["spec"])
				pod := environmentPKIPod(object)
				app := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
				switch fault {
				case "default-rolling":
					delete(spec, "strategy")
				case "explicit-rolling":
					spec["strategy"] = deploymentplan.Object{"type": "RollingUpdate", "rollingUpdate": deploymentplan.Object{"maxSurge": "25%", "maxUnavailable": float64(0)}}
				case "rolling-fields":
					deploymentplan.Map(spec["strategy"])["rollingUpdate"] = deploymentplan.Object{"maxSurge": float64(1)}
				case "multiple-writers":
					spec["replicas"] = float64(2)
				case "missing-pvc":
					managedNamedField(pod["volumes"], "mqtt-pki-state")["persistentVolumeClaim"] = nil
				case "read-only-state":
					managedNamedField(app["volumeMounts"], "mqtt-pki-state")["readOnly"] = true
				}
				before := deploymentplan.Digest(object)
				err := validateEnvironmentLogMQTTRollout(object, app)
				var blocker *deploymentplan.Blocker
				if !errors.As(err, &blocker) || blocker.ID != "deployment.configuration.log-mqtt-rollout" || blocker.Resource != deploymentplan.ID(object) || blocker.Next == "" || before != deploymentplan.Digest(object) {
					t.Fatal("candidate-only original-PVC rollout defect had no safe owner/repair or changed the target", err)
				}
				if fault != "missing-pvc" && fault != "read-only-state" {
					plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: "video-cloud-" + environment, Operation: "upgrade"}}
					for _, d := range defs {
						plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: deploymentplan.ID(d.Object), Desired: d.Object, Action: "update"})
					}
					err = checkEnvironmentLogMQTTTransport(context.Background(), plan, func(context.Context, string) (deploymentplan.Object, error) {
						t.Fatal("rolling-surge qualification depends on a running workload or current scheduling")
						return nil, nil
					})
					if !errors.As(err, &blocker) || blocker.ID != "deployment.configuration.log-mqtt-rollout" {
						t.Fatal("execution qualification missed the same candidate-only rollout blocker", err)
					}
				}
			})
		}
	}
}

func TestLogMQTTRolloutDerivationPreservesOthersAndOriginalBindings(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			defs, _ := logMQTTFixture(t, environment)
			object := defs[0].Object
			spec := deploymentplan.Map(object["spec"])
			spec["strategy"] = deploymentplan.Object{"type": "RollingUpdate", "rollingUpdate": deploymentplan.Object{"maxSurge": "25%"}}
			pod := environmentPKIPod(object)
			app := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
			originalPod := deploymentplan.Clone(pod)
			otherAPI := deploymentplan.Clone(object)
			deploymentplan.Map(otherAPI["metadata"])["name"] = "video-cloud-api"
			otherBefore := deploymentplan.Digest(otherAPI)
			unmanaged := deploymentplan.Clone(object)
			deploymentplan.Map(unmanaged["metadata"])["namespace"] = "video-cloud-" + environment + "-unmanaged"
			deploymentplan.Map(certIssuerObjectList(environmentPKIPod(unmanaged)["containers"])[0])["env"] = []any{}
			unmanagedBefore := deploymentplan.Digest(unmanaged)
			defs = append(defs, deploymentplan.Definition{Object: otherAPI}, deploymentplan.Definition{Object: unmanaged})
			if err := deriveEnvironmentLogMQTTTransport(map[string]string{"CLOUD_ENV_NAME": environment}, defs); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(spec["strategy"], deploymentplan.Object{"type": "Recreate"}) || !reflect.DeepEqual(originalPod["volumes"], pod["volumes"]) || !reflect.DeepEqual(deploymentplan.Map(certIssuerObjectList(originalPod["containers"])[0])["volumeMounts"], app["volumeMounts"]) || otherBefore != deploymentplan.Digest(otherAPI) || unmanagedBefore != deploymentplan.Digest(unmanaged) {
				t.Fatal("managed rollout changed original PVC/mounts or another working/unmanaged API")
			}
			for _, key := range []string{logMQTTIdentityPrefix + "STATE", logMQTTIdentityPrefix + "ROOT_SHA256", "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256"} {
				if mqttFoundationEnv(app, key) != mqttFoundationEnv(deploymentplan.Map(certIssuerObjectList(originalPod["containers"])[0]), key) {
					t.Fatal("rollout changed original identity path or independent Root pin", key)
				}
			}
		})
	}
}

func TestLogMQTTTransportRejectsMissingIdentityAndUnsafeSources(t *testing.T) {
	for _, problem := range []string{"missing-state", "missing-root", "manual-ca", "writable-ca", "static-client", "invalid-crl-state", "changed-owner", "foreign-root"} {
		t.Run(problem, func(t *testing.T) {
			defs, live := logMQTTFixture(t, "dev")
			c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(defs[0].Object)["containers"])[0])
			env := map[string]string{"CLOUD_ENV_NAME": "dev"}
			switch problem {
			case "missing-state":
				managedNamedField(c["env"], logMQTTIdentityPrefix+"STATE")["value"] = "private-canary"
			case "missing-root":
				managedNamedField(c["env"], logMQTTIdentityPrefix+"ROOT_SHA256")["value"] = ""
			case "manual-ca":
				env["VIDEO_CLOUD_MQTT_TLS_CA_ROOT_PATH"] = "private-canary"
			case "writable-ca":
				managedNamedField(c["volumeMounts"], "mqtt-pki-bundles")["readOnly"] = false
			case "static-client":
				c["env"] = append(certIssuerObjectList(c["env"]), deploymentplan.Object{"name": "VIDEO_CLOUD_MQTT_MANAGEMENT_KEY", "value": "private-canary"})
			}
			err := deriveEnvironmentLogMQTTTransport(env, defs)
			if problem == "invalid-crl-state" || problem == "changed-owner" || problem == "foreign-root" {
				if err != nil {
					t.Fatal(err)
				}
				plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "upgrade"}}
				for _, d := range defs {
					plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: deploymentplan.ID(d.Object), Desired: d.Object, Action: "update"})
				}
				sources := map[string]deploymentplan.Object{}
				for _, s := range live {
					id := deploymentplan.ID(s)
					plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: id, UID: deploymentplan.String(deploymentplan.Map(s["metadata"])["uid"]), Desired: s, Action: "retain"})
					sources[id] = s
				}
				if problem == "changed-owner" {
					deploymentplan.Map(live[0]["metadata"])["uid"] = "other-owner"
				}
				if problem == "foreign-root" {
					deploymentplan.Map(live[0]["data"])["roots.pem"] = "private-canary"
				}
				if problem == "invalid-crl-state" {
					deploymentplan.Map(live[1]["data"])["crls.json"] = "[{\"state_path\":\"/outside/private-canary.json\"}]"
				}
				err = checkEnvironmentLogMQTTTransport(context.Background(), plan, func(_ context.Context, id string) (deploymentplan.Object, error) { return sources[id], nil })
			}
			var b *deploymentplan.Blocker
			if !errors.As(err, &b) || b.ID != "deployment.configuration.log-mqtt-transport" || b.Next == "" || strings.Contains(err.Error(), "private-canary") {
				t.Fatal("unsafe or incomplete managed LogIngester accepted", err)
			}
		})
	}
}
