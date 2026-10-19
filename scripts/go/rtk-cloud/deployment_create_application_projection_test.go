package main

import (
	"context"
	"path"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateFinalApplicationCRLProjectionsKeepCanonicalRolesWithoutNestedMounts(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			cfg, p, release, runtime, service, roots, intermediates, initial := createApplicationFinalizeFixture(t, environment)
			before := deploymentplan.Digest(initial)
			final, err := finalizeEnvironmentCreateApplicationDefinitions(context.Background(), cfg, p, release, runtime, service, roots, intermediates, initial)
			if err != nil {
				t.Fatal(err)
			}
			if deploymentplan.Digest(initial) != before {
				t.Fatal("projection repair rewrote original enrolled definitions")
			}
			for _, name := range []string{"video-cloud-api", "video-cloud-logingester"} {
				object := createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/"+name)
				container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(object)["containers"])[0])
				for _, pair := range []struct{ role, key, source, file string }{
					{"mqtt-pki-bundles", "roots.pem", "pki-create-mqtt-consumer-trust", "/run/pki-mqtt/roots.pem"},
					{"mqtt-pki-crls", "crls.json", "pki-create-mqtt-consumer-trust", "/run/pki-mqtt-crls/crls.json"},
					{"service-root", "root.pem", "pki-create-service-trust", environmentCreateServiceTrust + "/root.pem"},
					{map[bool]string{true: "service-crls", false: "account-manager-crls"}[name == "video-cloud-logingester"], "crls.json", "pki-create-service-consumer-crls", "/run/pki-service-crls/outbound-crls.json"},
				} {
					file, err := coreMQTTFile(object, container, pair.role, pair.key)
					if err != nil || file.kind != "ConfigMap" || file.reference != pair.source || file.key != pair.key || file.file != pair.file {
						t.Fatalf("%s lost its exact original %s role: %v", name, pair.role, err)
					}
				}
				mounts := certIssuerObjectList(container["volumeMounts"])
				for i, raw := range mounts {
					first := deploymentplan.String(deploymentplan.Map(raw)["mountPath"])
					for j, other := range mounts {
						second := deploymentplan.String(deploymentplan.Map(other)["mountPath"])
						if i != j && (first == second || strings.HasPrefix(second, strings.TrimSuffix(first, "/")+"/")) {
							t.Fatalf("%s retains an ancestor/file mount collision: %s / %s", name, first, second)
						}
					}
				}
				settings := createRecipeEnv(container)
				if settings["VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST"] != "/run/pki-mqtt-crls/crls.json" {
					t.Fatal("native MQTT consumer does not read the exact sibling CRL file")
				}
				prefix := "VIDEO_CLOUD_CONTROLLER_IDENTITY"
				if name == "video-cloud-logingester" {
					prefix = "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY"
				}
				for _, suffix := range []string{"_SERVER_CRL_MANIFEST", "_RENEWAL_SERVER_CRL_MANIFEST"} {
					if settings[prefix+suffix] != "/run/pki-service-crls/outbound-crls.json" {
						t.Fatal("native managed client/renewal did not follow the Service CRL mount", prefix+suffix)
					}
				}
				for key, value := range settings {
					if strings.HasSuffix(key, "_SERVER_CRL_MANIFEST") && (value == "/run/pki-mqtt/crls.json" || value == environmentCreateServiceTrust+"/outbound-crls.json") {
						t.Fatal("native manifest still references the colliding projection", key)
					}
				}
			}
			mqtt := createRecipeObject(t, final, "ConfigMap/"+p.Stack+"-video-cloud/pki-create-mqtt-consumer-trust")
			serviceCRLs := createRecipeObject(t, final, "ConfigMap/"+p.Stack+"-video-cloud/pki-create-service-consumer-crls")
			if deploymentplan.Map(mqtt["data"])["roots.pem"] != roots["mqtt"].Certificate || deploymentplan.Map(mqtt["data"])["crls.json"] != environmentCreateServiceCRLManifest(roots["mqtt"], "/var/lib/mqtt-trust/root-crl.json", intermediates["mqtt"]) || deploymentplan.Map(serviceCRLs["data"])["crls.json"] != environmentCreateServiceCRLManifest(service.Root, path.Join(environmentCreateServiceState, "private/crl/outbound-root.json"), service.Intermediate) {
				t.Fatal("projection repair changed original CA or native CRL content/state semantics")
			}

			// The final compiler consumes these exact canonical roles. Exercise its
			// maintained native transport derivation, rather than an imitation.
			core, err := renderEnvironmentCreateManagedServiceDefinitions(context.Background(), cfg, p, runtime, service)
			if err != nil {
				t.Fatal(err)
			}
			for _, definition := range core {
				if definition.Object["kind"] == "Deployment" {
					final = append(final, definition)
				}
			}
			if err := deriveEnvironmentCoreMQTTTransport(nil, final); err != nil {
				t.Fatal("canonical final API transport rejected its repaired roles", err)
			}
			if err := deriveEnvironmentLogMQTTTransport(map[string]string{"CLOUD_ENV_NAME": environment}, final); err != nil {
				t.Fatal("canonical final LogIngester transport rejected its repaired roles", err)
			}
			if err := validateEnvironmentCoreMQTTTransport(final); err != nil {
				t.Fatal(err)
			}
			if _, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: strings.Repeat("b", 64)}, final, nil); err != nil {
				t.Fatal("repaired final definitions cannot compile through the actual planner", err)
			}
		})
	}
}
