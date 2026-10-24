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

func logScopePlan(defs []deploymentplan.Definition, live []deploymentplan.Object, environment string) (*deploymentplan.Plan, map[string]deploymentplan.Object) {
	plan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: "video-cloud-" + environment, Operation: "upgrade"}}
	sources := map[string]deploymentplan.Object{}
	for _, d := range defs {
		plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: deploymentplan.ID(d.Object), Desired: d.Object, Action: "update"})
	}
	for _, source := range live {
		id := deploymentplan.ID(source)
		plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: id, Desired: source, UID: deploymentplan.String(deploymentplan.Map(source["metadata"])["uid"]), Action: "retain"})
		sources[id] = source
	}
	return plan, sources
}

func TestLogRuntimeEnvironmentDerivedFromSelectedIntentAndOriginalManifests(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			defs, live := logMQTTFixture(t, environment)
			object := defs[0].Object
			pod := environmentPKIPod(object)
			app := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
			app["env"] = append(certIssuerObjectList(app["env"]), deploymentplan.Object{"name": "VIDEO_CLOUD_ENV", "value": "staging"})
			originalPod, originalSources := deploymentplan.Clone(pod), deploymentplan.Digest(live)
			originalRoots := map[string]string{}
			for _, key := range []string{logMQTTIdentityPrefix + "STATE", logMQTTIdentityPrefix + "ROOT_SHA256", "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256"} {
				originalRoots[key] = mqttFoundationEnv(app, key)
			}
			if err := deriveEnvironmentLogMQTTTransport(map[string]string{"CLOUD_ENV_NAME": environment}, defs); err != nil {
				t.Fatal(err)
			}
			if mqttFoundationEnv(app, "VIDEO_CLOUD_ENV") != environment || !reflect.DeepEqual(originalPod["volumes"], pod["volumes"]) || !reflect.DeepEqual(deploymentplan.Map(certIssuerObjectList(originalPod["containers"])[0])["volumeMounts"], app["volumeMounts"]) || deploymentplan.Digest(live) != originalSources {
				t.Fatal("selected environment was not projected or original public/private bindings changed")
			}
			for key, value := range originalRoots {
				if mqttFoundationEnv(app, key) != value {
					t.Fatal("scope repair replaced original identity or Root pin", key)
				}
			}
			plan, sources := logScopePlan(defs, live, environment)
			if err := checkEnvironmentLogMQTTTransport(context.Background(), plan, func(_ context.Context, id string) (deploymentplan.Object, error) {
				if sources[id] == nil {
					t.Fatal("candidate scope qualification depends on application readiness, cache writes or ACKs", id)
				}
				return sources[id], nil
			}); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestLogCandidateScopeRejectsWrongRuntimeBeforeNativeReads(t *testing.T) {
	for _, fault := range []string{"staging-on-dev", "missing", "indirect", "foreign-namespace"} {
		t.Run(fault, func(t *testing.T) {
			defs, live := logMQTTFixture(t, "dev")
			if err := deriveEnvironmentLogMQTTTransport(map[string]string{"CLOUD_ENV_NAME": "dev"}, defs); err != nil {
				t.Fatal(err)
			}
			object := defs[0].Object
			app := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(object)["containers"])[0])
			setting := managedNamedField(app["env"], "VIDEO_CLOUD_ENV")
			switch fault {
			case "staging-on-dev":
				setting["value"] = "staging"
			case "missing":
				delete(setting, "value")
			case "indirect":
				delete(setting, "value")
				setting["valueFrom"] = deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "private-canary", "key": "env"}}
			case "foreign-namespace":
				deploymentplan.Map(object["metadata"])["namespace"] = "video-cloud-staging-video-cloud"
			}
			plan, _ := logScopePlan(defs, live, "dev")
			err := checkEnvironmentLogMQTTTransport(context.Background(), plan, func(context.Context, string) (deploymentplan.Object, error) {
				t.Fatal("scope mismatch was missed until a live workload query")
				return nil, nil
			})
			var blocker *deploymentplan.Blocker
			if !errors.As(err, &blocker) || blocker.ID != "deployment.configuration.log-mqtt-scope" || blocker.Next == "" || strings.Contains(err.Error(), "private-canary") {
				t.Fatal("candidate scope mismatch has no safe stable diagnosis", err)
			}
		})
	}
}

func TestLogPreflightOriginalManifestValidatesNativeCRLConstructorInputs(t *testing.T) {
	for _, domain := range []string{"mqtt", "service"} {
		for _, fault := range []string{"foreign-environment", "foreign-domain", "missing-id", "inactive", "missing-chain", "wrong-chain-pin", "trailing-chain-data", "duplicate-entry"} {
			t.Run(domain+"/"+fault, func(t *testing.T) {
				defs, live := logMQTTFixture(t, "dev")
				if err := deriveEnvironmentLogMQTTTransport(map[string]string{"CLOUD_ENV_NAME": "dev"}, defs); err != nil {
					t.Fatal(err)
				}
				name := "mqtt-pki-crls"
				if domain == "service" {
					name = "account-manager-crls"
				}
				for _, source := range live {
					if deploymentplan.Map(source["metadata"])["name"] != name {
						continue
					}
					data := deploymentplan.Map(source["data"])
					var entries []deploymentplan.Object
					if err := json.Unmarshal([]byte(deploymentplan.String(data["crls.json"])), &entries); err != nil {
						t.Fatal(err)
					}
					issuer := deploymentplan.Map(entries[0]["issuer"])
					switch fault {
					case "foreign-environment":
						issuer["environment"] = "staging"
					case "foreign-domain":
						issuer["trust_domain"] = "device"
					case "missing-id":
						delete(issuer, "issuer_id")
					case "inactive":
						issuer["status"] = "ready"
					case "missing-chain":
						delete(issuer, "chain_pem")
					case "wrong-chain-pin":
						issuer["certificate_fingerprint_sha256"] = strings.Repeat("0", 64)
					case "trailing-chain-data":
						issuer["chain_pem"] = deploymentplan.String(issuer["chain_pem"]) + "private-canary"
					case "duplicate-entry":
						entries = append(entries, entries[0])
					}
					raw, _ := json.Marshal(entries)
					data["crls.json"] = string(raw)
				}
				plan, sources := logScopePlan(defs, live, "dev")
				err := checkEnvironmentLogMQTTTransport(context.Background(), plan, func(_ context.Context, id string) (deploymentplan.Object, error) {
					if sources[id] == nil {
						t.Fatal("manifest qualification queried a runtime consumer", id)
					}
					return sources[id], nil
				})
				var blocker *deploymentplan.Blocker
				if !errors.As(err, &blocker) || blocker.ID != "deployment.configuration.log-mqtt-scope" || blocker.Resource == "" || blocker.Next == "" || strings.Contains(err.Error(), "private-canary") {
					t.Fatal("incomplete native CRL consumer inputs escaped preflight", err)
				}
			})
		}
	}
}
