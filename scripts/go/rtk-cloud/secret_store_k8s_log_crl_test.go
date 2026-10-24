package main

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func testLogCRLRuntimeFixture(t *testing.T, environment string) (liveDeployment, map[string]string) {
	t.Helper()
	deployment := testLiveCRLDeployment(t, "video-cloud-logingester", "app", "VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST", true)
	raw, _ := json.Marshal(deployment)
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	pod := object["spec"].(map[string]any)["template"].(map[string]any)["spec"].(map[string]any)
	app := pod["containers"].([]any)[0].(map[string]any)
	app["env"] = []any{
		map[string]string{"name": "VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST", "value": "/run/original-mqtt-crls/crls.json"},
		map[string]string{"name": "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_SERVER_CRL_MANIFEST", "value": "/run/original-service-crls/crls.json"},
	}
	app["volumeMounts"] = []any{
		map[string]any{"name": "mqtt-crls", "mountPath": "/run/original-mqtt-crls", "readOnly": true},
		map[string]any{"name": "service-crls", "mountPath": "/run/original-service-crls", "readOnly": true},
		map[string]any{"name": "mqtt-pki-state", "mountPath": "/var/lib/mqtt-pki"},
	}
	pod["volumes"] = []any{
		map[string]any{"name": "mqtt-crls", "configMap": map[string]string{"name": "original-mqtt-crls"}},
		map[string]any{"name": "service-crls", "configMap": map[string]string{"name": "original-service-crls"}},
		map[string]any{"name": "mqtt-pki-state", "persistentVolumeClaim": map[string]string{"claimName": "original-log-identity"}},
	}
	raw, _ = json.Marshal(object)
	if err := json.Unmarshal(raw, &deployment); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{}
	for index, domain := range []string{"mqtt", "service"} {
		now := time.Now().UTC().Truncate(time.Second)
		manifest, state, registry := testLiveCRLEvidenceAt(t, environment, domain, now.Add(-time.Minute), now.Add(time.Hour))
		issuer := fmt.Sprintf("64062c4a-2bdc-474c-bb11-ab1b6102657%d", 4+index)
		manifest = strings.ReplaceAll(manifest, "64062c4a-2bdc-474c-bb11-ab1b61026574", issuer)
		state = strings.ReplaceAll(state, "64062c4a-2bdc-474c-bb11-ab1b61026574", issuer)
		statePath := "/var/lib/mqtt-pki/crls/original-mqtt.json"
		if domain == "service" {
			statePath = "/var/lib/mqtt-pki/identity/service-crls/original-service.json"
		}
		manifest = strings.ReplaceAll(manifest, "/var/lib/pki-state/crls/issuer.json", statePath)
		encoded, _ := json.Marshal(map[string]any{"data": map[string]string{"crls.json": manifest}})
		files[domain+"-manifest"] = string(encoded)
		files[domain+"-state"] = state
		files[domain+"-registry"] = registry
	}
	return deployment, files
}

func installLogCRLRuntimeFixture(t *testing.T, files map[string]string) (string, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	paths := map[string]string{}
	for name, body := range files {
		paths[name] = filepath.Join(dir, name+".json")
		if err := os.WriteFile(paths[name], []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	trace := filepath.Join(dir, "requests")
	// Accept only public ConfigMap/cache reads and a SELECT for the actual
	// LogIngester consumer. Unexpected operations fail within this isolated root.
	script := fmt.Sprintf(`#!/bin/sh
umask 077
printf '%%s\n' "$*" >> '%s'
case "$*" in
 *'get configmap original-mqtt-crls '*) cat '%s';;
 *'get configmap original-service-crls '*) cat '%s';;
 *'exec deployment/video-cloud-logingester -c app -- cat /var/lib/mqtt-pki/crls/original-mqtt.json') cat '%s';;
 *'exec deployment/video-cloud-logingester -c app -- cat /var/lib/mqtt-pki/identity/service-crls/original-service.json') cat '%s';;
 *'get pods -l app.kubernetes.io/name=postgresql '*) printf '%%s' '{"items":[{"metadata":{"name":"postgresql-0"}}]}';;
 *'exec postgresql-0 '*"consumer_id='video-cloud-logingester'"*"issuer_id='64062c4a-2bdc-474c-bb11-ab1b61026574'"*) cat '%s';;
 *'exec postgresql-0 '*"consumer_id='video-cloud-logingester'"*"issuer_id='64062c4a-2bdc-474c-bb11-ab1b61026575'"*) cat '%s';;
 *) printf 'Forbidden private-canary' >&2; exit 99;;
esac
`, trace, paths["mqtt-manifest"], paths["service-manifest"], paths["mqtt-state"], paths["service-state"], paths["mqtt-registry"], paths["service-registry"])
	kubectl := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(kubectl, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	return trace, paths
}

func TestPostDeployLogCRLChecksOriginalCachesAndReceiptsAcrossEnvironments(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			deployment, files := testLogCRLRuntimeFixture(t, environment)
			before, _ := json.Marshal(deployment)
			trace, paths := installLogCRLRuntimeFixture(t, files)
			runtime := newDeploymentCheckRuntime(context.Background(), environment)
			if err := verifyMountedPKICRLManifests("isolated-kubeconfig", "video-cloud-"+environment+"-video-cloud", environment, liveDeploymentList{Items: []liveDeployment{deployment}}, "video-cloud-logingester", runtime); err != nil {
				t.Fatal(err)
			}
			after, _ := json.Marshal(deployment)
			if string(before) != string(after) {
				t.Fatal("runtime inspection changed original workload bindings")
			}
			for name, body := range files {
				actual, err := os.ReadFile(paths[name])
				if err != nil || string(actual) != body {
					t.Fatal("runtime inspection changed original cache or manifest", name, err)
				}
			}
			requests, err := os.ReadFile(trace)
			if err != nil || strings.Count(string(requests), "exec deployment/video-cloud-logingester -c app -- cat") != 2 || strings.Count(string(requests), "consumer_id='video-cloud-logingester'") != 2 || !strings.Contains(string(requests), "/identity/service-crls/original-service.json") {
				t.Fatal("both original caches and exact native receipts were not checked", err)
			}
		})
	}
}

func TestPreflightLogCRLDoesNotReadWorkloadCachesOrRequireACKs(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			deployment, files := testLogCRLRuntimeFixture(t, environment)
			files["mqtt-state"], files["service-state"] = "", ""
			files["mqtt-registry"], files["service-registry"] = "", ""
			trace, _ := installLogCRLRuntimeFixture(t, files)
			runtime := newDeploymentCheckRuntime(context.Background(), environment)
			runtime.preflightPlan = &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: "video-cloud-" + environment, Operation: "upgrade"}}
			if err := verifyMountedPKICRLManifests("isolated-kubeconfig", "video-cloud-"+environment+"-video-cloud", environment, liveDeploymentList{Items: []liveDeployment{deployment}}, "video-cloud-logingester", runtime); err != nil {
				t.Fatal("preflight depended on future runtime caches or receipts", err)
			}
			if _, err := os.Stat(trace); !os.IsNotExist(err) {
				t.Fatal("preflight contacted a live consumer or registry for LogIngester cache acceptance", err)
			}
		})
	}
}

func TestPostDeployLogCRLRejectsMissingStaleOrInvalidEvidence(t *testing.T) {
	for _, domain := range []string{"mqtt", "service"} {
		for _, fault := range []string{"missing-cache", "foreign-authority", "signature", "missing-ack", "superseded-registry"} {
			t.Run(domain+"/"+fault, func(t *testing.T) {
				deployment, files := testLogCRLRuntimeFixture(t, "dev")
				switch fault {
				case "missing-cache":
					files[domain+"-state"] = ""
				case "foreign-authority", "signature":
					var state map[string]any
					if err := json.Unmarshal([]byte(files[domain+"-state"]), &state); err != nil {
						t.Fatal(err)
					}
					if fault == "foreign-authority" {
						state["issuer_fingerprint"] = strings.Repeat("0", 64)
					} else {
						crl := state["crl"].(map[string]any)
						block, _ := pem.Decode([]byte(crl["crl_pem"].(string)))
						block.Bytes[len(block.Bytes)-1] ^= 1
						crl["crl_pem"] = string(pem.EncodeToMemory(block))
					}
					raw, _ := json.Marshal(state)
					files[domain+"-state"] = string(raw)
				case "missing-ack":
					files[domain+"-registry"] = strings.Replace(files[domain+"-registry"], `"ack":true`, `"ack":false`, 1)
				case "superseded-registry":
					files[domain+"-registry"] = `{"digest":"superseded","number":"2","ack":true}`
				}
				installLogCRLRuntimeFixture(t, files)
				err := verifyMountedPKICRLManifests("isolated-kubeconfig", "video-cloud-dev-video-cloud", "dev", liveDeploymentList{Items: []liveDeployment{deployment}}, "video-cloud-logingester", newDeploymentCheckRuntime(context.Background(), "dev"))
				if err == nil || strings.Contains(err.Error(), "private-canary") {
					t.Fatal("invalid runtime evidence was accepted or leaked", err)
				}
			})
		}
	}
}

func TestCertIssuerAcceptanceAlsoChecksEnabledLogCRLConsumer(t *testing.T) {
	deployment, files := testLogCRLRuntimeFixture(t, "staging")
	files["service-state"] = ""
	installLogCRLRuntimeFixture(t, files)
	list := liveDeploymentList{Items: []liveDeployment{deployment}}
	err := verifyMountedPKICRLManifests("isolated-kubeconfig", "video-cloud-staging-video-cloud", "staging", list, "certissuer", newDeploymentCheckRuntime(context.Background(), "staging"))
	if err == nil || !strings.Contains(err.Error(), "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_SERVER_CRL_MANIFEST") || !strings.Contains(err.Error(), "state is unreadable") {
		t.Fatal("normal Video Cloud runtime entry point omitted LogIngester cache acceptance", err)
	}
	zero := int32(0)
	list.Items[0].Spec.Replicas = &zero
	if err := verifyMountedPKICRLManifests("isolated-kubeconfig", "video-cloud-staging-video-cloud", "staging", list, "certissuer", newDeploymentCheckRuntime(context.Background(), "staging")); err != nil {
		t.Fatal("disabled LogIngester required runtime evidence", err)
	}
}

func TestLogCRLAcceptanceScopesManagedAndRequiredProfiles(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, profile := range []string{"unmanaged", "empty-root", "empty-state", "required-policy"} {
			t.Run(environment+"/"+profile, func(t *testing.T) {
				deployment, files := testLogCRLRuntimeFixture(t, environment)
				app := &deployment.Spec.Template.Spec.Containers[0]
				originalSetting := app.Env[0]
				app.Env = nil
				list := liveDeploymentList{Items: []liveDeployment{deployment}}
				if profile == "empty-root" || profile == "empty-state" {
					originalSetting.Name = "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256"
					if profile == "empty-state" {
						originalSetting.Name = "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_STATE"
					}
					originalSetting.Value = ""
					list.Items[0].Spec.Template.Spec.Containers[0].Env = append(app.Env, originalSetting)
				}
				if profile == "required-policy" {
					controller := testLiveCRLDeployment(t, "pki-controller", "controller", "PKI_REQUIRED_CONSUMERS_MQTT", true)
					controller.Spec.Template.Spec.Containers[0].Env[0].Value = "video-cloud-api,video-cloud-logingester"
					list.Items = append(list.Items, controller)
				}
				trace, _ := installLogCRLRuntimeFixture(t, files)
				err := verifyMountedPKICRLManifests("isolated-kubeconfig", "video-cloud-"+environment+"-video-cloud", environment, list, "video-cloud-logingester", newDeploymentCheckRuntime(context.Background(), environment))
				if profile == "unmanaged" {
					if err != nil {
						t.Fatal("unmanaged workload received a new PKI prerequisite", err)
					}
				} else if err == nil || !strings.Contains(err.Error(), "requires exactly one") {
					t.Fatal("partial original or policy-required profile escaped runtime acceptance", err)
				}
				if _, err := os.Stat(trace); !os.IsNotExist(err) {
					t.Fatal("profile qualification fabricated runtime evidence", err)
				}
			})
		}
	}
}
