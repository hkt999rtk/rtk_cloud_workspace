package main

import (
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCoreMQTTManagementRejectsIncompleteIdentityBeforeRollout(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, problem := range []string{"missing-state", "missing-root", "outside-pvc", "readonly", "static-key", "bootstrap", "missing-renewal", "missing-host-pin", "reference-state", "missing-client-crl"} {
			t.Run(environment+"/"+problem, func(t *testing.T) {
				defs, _ := coreMQTTFixture(t, environment)
				pod := environmentPKIPod(defs[0].Object)
				c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
				settings, _ := environmentPKINamedFields(c["env"])
				switch problem {
				case "missing-state":
					settings["VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE"]["value"] = ""
				case "missing-root":
					settings["VIDEO_CLOUD_CONTROLLER_IDENTITY_ROOT_SHA256"]["value"] = ""
				case "outside-pvc":
					settings["VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE"]["value"] = "/other/identity.json"
				case "reference-state":
					delete(settings["VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE"], "value")
					settings["VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE"]["valueFrom"] = deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "unknown", "key": "path"}}
				case "readonly":
					mounts, _ := environmentPKINamedFields(c["volumeMounts"])
					mounts["service-identity"]["readOnly"] = true
				case "static-key", "bootstrap":
					key := "VIDEO_CLOUD_MQTT_MANAGEMENT_KEY"
					if problem == "bootstrap" {
						key = "VIDEO_CLOUD_CONTROLLER_IDENTITY_BOOTSTRAP_CERT"
					}
					c["env"] = append(certIssuerObjectList(c["env"]), deploymentplan.Object{"name": key, "value": "/static/private.pem"})
				case "missing-renewal":
					defs = append(defs[:3], defs[4:]...)
				case "missing-host-pin":
					server := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(defs[4].Object)["containers"])[0])
					server["env"] = nil
				case "missing-client-crl":
					mounts := []any{}
					for _, raw := range certIssuerObjectList(c["volumeMounts"]) {
						if deploymentplan.Map(raw)["name"] != "account-manager-crls" {
							mounts = append(mounts, raw)
						}
					}
					c["volumeMounts"] = mounts
				}
				if err := deriveEnvironmentCoreMQTTTransport(nil, defs); err == nil {
					t.Fatal("incomplete MQTT management identity qualified")
				}
			})
		}
	}
}

func TestControllerIdentityLifecycleSettingsArePreserved(t *testing.T) {
	for _, key := range []string{"VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE", "VIDEO_CLOUD_CONTROLLER_IDENTITY_ROOT_SHA256", "VIDEO_CLOUD_CONTROLLER_SERVER_CRL_MANIFEST", "VIDEO_CLOUD_CONTROLLER_IDENTITY_RENEWAL_URL", "VIDEO_CLOUD_MQTT_MANAGEMENT_CERT"} {
		if !environmentIdentitySetting(key) {
			t.Fatal("identity binding discarded", key)
		}
	}
	defs, _ := coreMQTTFixture(t, "dev")
	if err := deriveEnvironmentCoreMQTTTransport(nil, defs); err != nil {
		t.Fatal(err)
	}
	c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(defs[0].Object)["containers"])[0])
	settings, _ := environmentPKINamedFields(c["env"])
	settings["VIDEO_CLOUD_CONTROLLER_IDENTITY_TLS_CA"]["value"] = "/missing"
	if err := validateEnvironmentCoreMQTTTransport(defs); err == nil || strings.Contains(err.Error(), "private-token") {
		t.Fatal("lost management configuration accepted", err)
	}
}
