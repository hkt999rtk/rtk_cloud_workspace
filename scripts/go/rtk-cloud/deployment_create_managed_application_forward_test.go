package main

import (
	"context"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"testing"
)

func TestManagedApplicationProjectionForwardMatchesMaintainedFinalRecipe(t *testing.T) {
	cfg, p, release, runtime, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, "dev")
	final, err := finalizeEnvironmentCreateApplicationDefinitions(context.Background(), cfg, p, release, runtime, service, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"video-cloud-api", "video-cloud-logingester"} {
		desired := createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/"+name)
		old := deploymentplan.Clone(desired)
		c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(old)["containers"])[0])
		for _, raw := range certIssuerObjectList(c["volumeMounts"]) {
			m := deploymentplan.Map(raw)
			if m["mountPath"] == "/run/pki-mqtt-crls/crls.json" {
				m["mountPath"] = "/run/pki-mqtt/crls.json"
			}
			if m["mountPath"] == "/run/pki-service-crls/outbound-crls.json" {
				m["mountPath"] = environmentCreateServiceTrust + "/outbound-crls.json"
			}
		}
		for _, raw := range certIssuerObjectList(c["env"]) {
			m := deploymentplan.Map(raw)
			if m["value"] == "/run/pki-mqtt-crls/crls.json" {
				m["value"] = "/run/pki-mqtt/crls.json"
			}
			if m["value"] == "/run/pki-service-crls/outbound-crls.json" {
				m["value"] = environmentCreateServiceTrust + "/outbound-crls.json"
			}
		}
		before := deploymentplan.Digest(old)
		actual, err := managedApplicationCRLProjectionRepair(old)
		if err != nil {
			t.Fatal(err)
		}
		if deploymentplan.Digest(actual) != deploymentplan.Digest(desired) || deploymentplan.Digest(old) != before {
			t.Fatal("forward does not preserve exact maintained recipe or original failed body")
		}
		for _, mode := range []string{"mount", "source", "env", "extra-mount"} {
			drift := deploymentplan.Clone(old)
			pod := environmentPKIPod(drift)
			c := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
			switch mode {
			case "mount":
				for _, v := range certIssuerObjectList(c["volumeMounts"]) {
					m := deploymentplan.Map(v)
					if m["name"] == "mqtt-pki-crls" {
						m["mountPath"] = "/foreign"
					}
				}
			case "source":
				for _, v := range certIssuerObjectList(pod["volumes"]) {
					m := deploymentplan.Map(v)
					if m["name"] == "mqtt-pki-crls" {
						deploymentplan.Map(m["configMap"])["name"] = "foreign"
					}
				}
			case "env":
				for _, v := range certIssuerObjectList(c["env"]) {
					m := deploymentplan.Map(v)
					if m["name"] == "VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST" {
						m["value"] = "/foreign"
					}
				}
			case "extra-mount":
				for _, v := range certIssuerObjectList(c["volumeMounts"]) {
					m := deploymentplan.Map(v)
					if m["name"] == "mqtt-pki-crls" {
						c["volumeMounts"] = append(certIssuerObjectList(c["volumeMounts"]), deploymentplan.Clone(m))
						break
					}
				}
			}
			if _, err := managedApplicationCRLProjectionRepair(drift); err == nil {
				t.Fatal("accepted altered original projection:", mode)
			}
		}
	}
}
