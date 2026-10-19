package main

import (
	"context"
	"path/filepath"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateApplicationBootstrapUsesNativeRegistryInputsAndOriginalOwnedCRLState(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			cfg, p, _, service := createServiceRecipeFixture(t, environment)
			_, _, release := completeEnvironmentRenderFixture(environment)
			store := makeIsolatedTestSecretStore(t, environment)
			for _, entry := range rtkSecretCatalog() {
				if err := store.write(filepath.Join("runtime", entry.ID), []byte("isolated-test-value-"+entry.ID+"\n"), false); err != nil {
					t.Fatal(err)
				}
			}
			definitions, err := renderEnvironmentCreateApplicationOwners(context.Background(), cfg, p, release, store, nil, service)
			if err != nil {
				t.Fatal(err)
			}
			for _, owner := range environmentCreateApplicationOwners() {
				object := createRecipeObject(t, definitions, "Deployment/"+p.Stack+"-video-cloud/"+owner.Name)
				pod := environmentPKIPod(object)
				container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
				settings := createRecipeEnv(container)
				if owner.Name == "video-cloud-api-pki" || owner.Name == "video-cloud-api-app-pki" {
					security := deploymentplan.Map(pod["securityContext"])
					groups := certIssuerObjectList(security["supplementalGroups"])
					if len(groups) != 1 || groups[0] != float64(0) || security["runAsUser"] != float64(10001) || security["runAsGroup"] != float64(10001) || security["fsGroup"] != nil {
						t.Fatal("bootstrap TLS Secret reader lost its non-root supplementary group boundary", owner.Name)
					}
				}
				if owner.Role == "video-cloud-logingester" && (settings["VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_ROOT_SHA256"] != service.Root.RootFingerprint || settings["VIDEO_CLOUD_LOG_INGESTER_MQTT_ROOT_SHA256"] != "") {
					t.Fatal("log bootstrap pin differs from the current native config parser")
				}
				if owner.Role == "video-cloud-api" || owner.Role == "video-cloud-logingester" {
					var registryDSN bool
					for _, value := range certIssuerObjectList(container["env"]) {
						entry := deploymentplan.Map(value)
						if entry["name"] == "VIDEO_CLOUD_DB_DSN" {
							ref := deploymentplan.Map(deploymentplan.Map(entry["valueFrom"])["secretKeyRef"])
							registryDSN = ref["name"] == "pki-verifier-database" && ref["key"] == "url"
						}
					}
					if !registryDSN {
						t.Fatal("native trust-only owner received the business DSN instead of its registry grant", owner.Name)
					}
				}
				claims := map[string]string{}
				for _, value := range certIssuerObjectList(pod["volumes"]) {
					volume := deploymentplan.Map(value)
					claims[deploymentplan.String(volume["name"])] = deploymentplan.String(deploymentplan.Map(volume["persistentVolumeClaim"])["claimName"])
				}
				paths := map[string]bool{}
				for _, value := range certIssuerObjectList(container["volumeMounts"]) {
					mount := deploymentplan.Map(value)
					path, name := deploymentplan.String(mount["mountPath"]), deploymentplan.String(mount["name"])
					if paths[path] {
						t.Fatal("native bootstrap owner has overlapping mounts", owner.Name, path)
					}
					paths[path] = true
					if path == environmentCreateServiceState && (claims[name] != owner.Claim || mount["readOnly"] == true) {
						t.Fatal("native Service CRL durable state does not resolve to the original enrolled owner", owner.Name)
					}
				}
				if !paths[owner.StateRoot] || !paths[environmentCreateServiceState] {
					t.Fatal("owned client or actual public manifest CRL state path is not mounted", owner.Name)
				}
			}
		})
	}
}

func TestCreateApplicationInitialPoolsRemainSeparateAndUseActualAppController(t *testing.T) {
	cfg, p, _, _, _, roots, intermediates, definitions := createApplicationFinalizeFixture(t, "dev")
	before := deploymentplan.Digest(definitions)
	device, err := configureEnvironmentCreateDeviceConsumers(cfg, p, roots["device"], definitions)
	if err != nil {
		t.Fatal(err)
	}
	configured, err := configureEnvironmentCreateAppConsumers(p, roots["app"], []environmentCreateRegisteredIssuer{intermediates["app"]}, device)
	if err != nil {
		t.Fatal(err)
	}
	if deploymentplan.Digest(definitions) != before {
		t.Fatal("selected initial pool setup modified original owner inputs")
	}
	broker := createRecipeObject(t, configured, "Deployment/"+p.Stack+"-video-cloud/mqtt-pki")
	brokerEnv := createRecipeEnv(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(broker)["containers"])[0]))
	if brokerEnv["PKI_BROKER_APP_PKI_ENABLED"] != "true" || brokerEnv["PKI_BROKER_APP_BUNDLE_ACK_ENABLED"] != "true" {
		t.Fatal("App native bundle consumer lacks explicit enforcement")
	}
	for _, owner := range []string{"video-cloud-api-pki", "video-cloud-api-app-pki"} {
		object := createRecipeObject(t, configured, "Deployment/"+p.Stack+"-video-cloud/"+owner)
		settings := createRecipeEnv(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(object)["containers"])[0]))
		identity := settings["VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE"]
		if settings["VIDEO_CLOUD_AUTH_ROOT_TRUST_BOOTSTRAP_STATE"] != filepath.Join(filepath.Dir(identity), "incoming-root-bootstrap.json") {
			t.Fatal("incoming native admission did not remain on the original Service owner claim")
		}
		if settings["VIDEO_CLOUD_MQTT_SERVER_BUNDLE_MANIFEST"] != "" || settings["VIDEO_CLOUD_AUTH_ISSUER_TRUST_MANIFEST"] == "" || settings["VIDEO_CLOUD_AUTH_DEVICE_ROOT_TRUST_ROOT_ID"] != "" || settings["VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_ROOT_ID"] != "" {
			t.Fatal("incoming bootstrap inherited another pool or manufactured future policy state")
		}
		if owner == "video-cloud-api-app-pki" {
			if settings["VIDEO_CLOUD_AUTH_APP_ROOT_TRUST_CONTROLLER_URL"] != "https://pki-controller."+p.Stack+"-video-cloud.svc:18446" || settings["VIDEO_CLOUD_AUTH_APP_PKI_ENABLED"] != "true" || settings["VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED"] != "false" || settings["VIDEO_CLOUD_AUTH_DEVICE_CA_CERT"] != "" {
				t.Fatal("App receipt cannot use the native configured owner and controller")
			}
		} else if settings["VIDEO_CLOUD_AUTH_PRODUCT_PKI_ENABLED"] != "true" || settings["VIDEO_CLOUD_AUTH_APP_PKI_ENABLED"] != "false" || settings["VIDEO_CLOUD_AUTH_APP_CA_CERT"] != "" {
			t.Fatal("Device bootstrap pool inherited App trust")
		}
	}
}
