package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// These objects are the final native definitions produced by the fresh-create
// renderers. The assigned UIDs represent an isolated completed live installation,
// not future identity evidence supplied to a deployment/preflight.
func createdNativeInventoryFixture(t *testing.T, environment string, useOpenBao ...bool) (deploymentConfig, environmentCreateBootstrapPlan, environmentRelease, environmentCreateBootstrapRuntime, environmentCreateServiceCeremony, []deploymentplan.Definition) {
	t.Helper()
	ctx := context.Background()
	cfg, p, release, runtime, service, roots, intermediates, _ := createApplicationFinalizeFixture(t, environment)
	remote := len(useOpenBao) == 1 && useOpenBao[0]
	if remote {
		cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
		cfg.Values["DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS"], cfg.Values["DEPLOYMENT_UPGRADE_WINDOW_SECONDS"] = "120", "300"
		var err error
		p.ProductIdentities, err = environmentCreateProductIdentityIntent(cfg)
		if err != nil {
			t.Fatal(err)
		}
	}
	p.AppRoutingMode = "internal-service"
	service.Root.Reference = "pki-issuers/service/" + service.Root.ID + "/v1"
	service.Intermediate.Reference = "pki-issuers/service/" + service.Intermediate.ID + "/v1"
	service.Root.Document["signer_reference"], service.Intermediate.Document["signer_reference"] = service.Root.Reference, service.Intermediate.Reference
	roots["service"], intermediates["service"] = service.Root, service.Intermediate
	for _, domain := range []string{"device", "app", "mqtt", "openbao_tls"} {
		_, _, _, lineage := createServiceRecipeFixture(t, environment)
		root, intermediate := lineage.Root, lineage.Intermediate
		root.ID, root.Domain, root.Reference = roots[domain].ID, domain, roots[domain].Reference
		intermediate.ID, intermediate.Domain = intermediates[domain].ID, domain
		intermediate.Reference = "pki-issuers/" + domain + "/" + intermediate.ID + "/v1"
		root.Document["issuer_id"], root.Document["trust_domain"], root.Document["signer_reference"] = root.ID, domain, root.Reference
		intermediate.Document["issuer_id"], intermediate.Document["trust_domain"], intermediate.Document["parent_issuer_id"], intermediate.Document["signer_reference"] = intermediate.ID, domain, root.ID, intermediate.Reference
		roots[domain], intermediates[domain] = root, intermediate
	}
	parentConfig, store, operatorSHA, artifact := createStateFixture(t, environment)
	execution, err := startEnvironmentCreateExecution(ctx, parentConfig, artifact, operatorSHA, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { execution.Close() })
	// All producers below belong to this selected parent generation. Earlier
	// issuer fixtures use separate stores and must not supply runtime inputs.
	runtime.Backend.Store, runtime.Execution = store, execution
	if remote {
		runtime.ExecutionSHA256 = execution.Journal.PlanSHA256
		runtime.Custody, err = createEnvironmentCustodyBinding(cfg, p, runtime.ExecutionSHA256, roots["openbao_tls"].RootFingerprint)
		if err != nil {
			t.Fatal(err)
		}
		runtime.Custody.ServiceRootSHA256 = service.Root.RootFingerprint
		runtime.ServiceCeremony = &service
	}
	admission, err := prepareEnvironmentCreateIngressAdmissionMaterial(ctx, cfg, p, release, store, execution)
	if err != nil {
		t.Fatal(err)
	}
	ingress, err := renderEnvironmentCreateIngressAdmission(cfg, p, release, admission)
	if err != nil {
		t.Fatal(err)
	}
	for _, definition := range ingress {
		if deploymentplan.Map(deploymentplan.Map(definition.Object["metadata"])["labels"])["rtk.realtek.com/stack"] != p.Stack {
			t.Fatal("actual ingress producer emitted an undiscoverable owner before fixture metadata normalization", deploymentplan.ID(definition.Object))
		}
	}
	for _, entry := range rtkSecretCatalog() {
		if err := store.write(filepath.Join("runtime", entry.ID), []byte("isolated-created-original-"+entry.ID+"\n"), false); err != nil {
			t.Fatal(err)
		}
	}
	applications, err := renderEnvironmentCreateApplicationOwners(ctx, cfg, p, release, store, nil, service)
	if err != nil {
		t.Fatal(err)
	}
	applications, err = configureEnvironmentCreateMQTTConsumers(p, service, roots["mqtt"], []environmentCreateRegisteredIssuer{intermediates["mqtt"]}, applications)
	if err != nil {
		t.Fatal(err)
	}
	applications, err = configureEnvironmentCreateDeviceConsumers(cfg, p, roots["device"], applications)
	if err != nil {
		t.Fatal(err)
	}
	applications, err = configureEnvironmentCreateAppConsumers(p, roots["app"], []environmentCreateRegisteredIssuer{intermediates["app"]}, applications)
	if err != nil {
		t.Fatal(err)
	}
	applications, err = finalizeEnvironmentCreateApplicationDefinitions(ctx, cfg, p, release, runtime, service, roots, intermediates, applications)
	if err != nil {
		t.Fatal(err)
	}
	core, err := renderEnvironmentCreateManagedServiceDefinitions(ctx, cfg, p, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	core, err = applyEnvironmentCreateManagedOpenBaoConsumers(p, service, roots, intermediates, core)
	if err != nil {
		t.Fatal(err)
	}
	core, err = configureEnvironmentCreateFinalIssuer(p, roots, intermediates, core)
	if err != nil {
		t.Fatal(err)
	}
	transport, err := renderEnvironmentCreateTransportDefinitions(ctx, cfg, p, runtime, service, roots, intermediates, release)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]deploymentplan.Definition{}
	for _, group := range [][]deploymentplan.Definition{runtime.InitialListeners, transport, applications, core, ingress} {
		for _, definition := range group {
			if definition.Object["kind"] != "Job" {
				byID[deploymentplan.ID(definition.Object)] = definition
			}
		}
	}
	for _, namespace := range lkeNamespaces(environmentCreateProviderValues(cfg)) {
		object := deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": namespace.Name}}
		byID[deploymentplan.ID(object)] = deploymentplan.Definition{Phase: "namespace", Object: object}
	}
	// Native schema enrollment creates these original runtime grants. Model
	// their real referenced keys with isolated values, never an empty Secret
	// placeholder or a made-up fallback name.
	grants := map[string]map[string]string{}
	for _, definition := range byID {
		namespace := deploymentplan.String(deploymentplan.Map(definition.Object["metadata"])["namespace"])
		for _, value := range certIssuerObjectList(environmentPKIPod(definition.Object)["containers"]) {
			for _, env := range certIssuerObjectList(deploymentplan.Map(value)["env"]) {
				ref := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(env)["valueFrom"])["secretKeyRef"])
				name, key := deploymentplan.String(ref["name"]), deploymentplan.String(ref["key"])
				id := "Secret/" + namespace + "/" + name
				if name == "" || key == "" || byID[id].Object != nil {
					continue
				}
				if grants[id] == nil {
					grants[id] = map[string]string{}
				}
				grants[id][key] = "isolated-native-original-" + key
			}
		}
	}
	for id, data := range grants {
		parts := strings.Split(id, "/")
		byID[id] = environmentCreateSecret(parts[1], parts[2], data)
	}
	var definitions []deploymentplan.Definition
	ids := make([]string, 0, len(byID))
	for id := range byID {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		definition := byID[id]
		if definition.Object["kind"] == "Secret" {
			data := deploymentplan.Map(definition.Object["data"])
			if data == nil {
				data = deploymentplan.Object{}
				definition.Object["data"] = data
			}
			for key, value := range deploymentplan.Map(definition.Object["stringData"]) {
				data[key] = base64.StdEncoding.EncodeToString([]byte(deploymentplan.String(value)))
			}
			delete(definition.Object, "stringData")
		}
		meta := deploymentplan.Map(definition.Object["metadata"])
		meta["uid"] = fmt.Sprintf("completed-fixture-owner-%d", len(definitions))
		if id == "Namespace//"+p.Stack+"-video-cloud" {
			meta["uid"] = service.CleanupBinding.NamespaceUID
		}
		meta["resourceVersion"] = "1"
		labels := deploymentplan.Map(meta["labels"])
		if labels == nil {
			labels = deploymentplan.Object{}
			meta["labels"] = labels
		}
		labels["rtk.realtek.com/stack"] = p.Stack
		definitions = append(definitions, definition)
	}
	ns := p.Stack + "-video-cloud"
	appService := createRecipeObject(t, definitions, "Service/"+ns+"/video-cloud-api-app-pki")
	appOwner := createRecipeObject(t, definitions, "Deployment/"+ns+"/video-cloud-api-app-pki")
	routingBinding := environmentAppRoutingBinding{Schema: "rtk-cloud.native-app-routing/v1", Environment: environment, Stack: p.Stack, Mode: p.AppRoutingMode, ServiceUID: deploymentplan.String(deploymentplan.Map(appService["metadata"])["uid"]), DeploymentUID: deploymentplan.String(deploymentplan.Map(appOwner["metadata"])["uid"]), CreatePlanSHA256: strings.Repeat("a", 64), DeclarationUID: "completed-fixture-routing-owner"}
	routing := environmentAppRoutingDefinition(routingBinding)
	deploymentplan.Map(routing.Object["metadata"])["uid"] = routingBinding.DeclarationUID
	if _, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join("pki", "app", "routing.json"), routingBinding); err != nil {
		t.Fatal(err)
	}
	definitions = append(definitions, routing)
	if remote {
		definitions, err = projectEnvironmentCustodyResources(cfg.Values, environment, runtime.Custody, "", definitions)
		if err != nil {
			t.Fatal("fresh complete native custody projection", err)
		}
		for i, d := range definitions {
			meta := deploymentplan.Map(d.Object["metadata"])
			if meta["uid"] == nil {
				meta["uid"] = fmt.Sprintf("completed-custody-fixture-owner-%d", i)
				meta["resourceVersion"] = "1"
			}
		}
	}
	return cfg, p, release, runtime, service, definitions
}

func TestCreatedCompleteNativeOwnerInventoryCanonicalRender(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			cfg, _, release, _, _, original := createdNativeInventoryFixture(t, environment)
			live := make([]deploymentplan.Object, 0, len(original))
			for _, definition := range original {
				live = append(live, definition.Object)
			}
			bindings, err := observeEnvironmentBindings(environment, release, live)
			if err != nil {
				t.Fatal("complete created native inventory cannot be reobserved", err)
			}
			target, err := renderEnvironmentResources(environmentCreateProviderValues(cfg), bindings, release)
			if err != nil {
				t.Fatal("complete created native inventory cannot be canonically rendered", err)
			}
			for _, name := range []string{"video-cloud-api", "video-cloud-api-pki", "video-cloud-api-app-pki", "video-cloud-logingester", "pki-controller", "certissuer", "factoryenroll", "mqtt-pki"} {
				id := "Deployment/" + bindings.Stack + "-video-cloud/" + name
				before, after := createRecipeObject(t, original, id), createRecipeObject(t, target, id)
				for _, value := range certIssuerObjectList(environmentPKIPod(before)["containers"]) {
					c := deploymentplan.Map(value)
					var actual deploymentplan.Object
					for _, next := range certIssuerObjectList(environmentPKIPod(after)["containers"]) {
						if deploymentplan.Map(next)["name"] == c["name"] {
							actual = deploymentplan.Map(next)
						}
					}
					if actual == nil {
						t.Fatal("canonical target lost original native consumer", name, c["name"])
					}
					baseline, wanted := createRecipeEnv(c), createRecipeEnv(actual)
					if name == "video-cloud-api" || name == "video-cloud-logingester" {
						for _, consumer := range []deploymentplan.Object{c, actual} {
							fields, err := environmentPKINamedFields(consumer["env"])
							if err != nil || fields["VIDEO_CLOUD_MQTT_MANAGEMENT_CERT"] != nil || fields["VIDEO_CLOUD_MQTT_MANAGEMENT_KEY"] != nil {
								t.Fatal("managed MQTT consumer introduced unused static credential configuration", name)
							}
						}
					}
					for _, key := range []string{"VIDEO_CLOUD_AUTH_CRL_MANIFEST", "VIDEO_CLOUD_AUTH_DEVICE_AUTOMATIC_STATE_DIR", "VIDEO_CLOUD_AUTH_ROOT_TRUST_BOOTSTRAP_STATE", "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_MANIFEST", "VIDEO_CLOUD_CONTROLLER_IDENTITY_STATE", "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_STATE", "PKI_BROKER_IDENTITY_STATE", "FACTORY_ENROLL_SERVICE_IDENTITY_STATE", "PKI_SERVICE_CLIENT_IDENTITY_STATE", "CERT_ISSUER_SERVICE_CLIENT_IDENTITY_STATE"} {
						if baseline[key] != "" && baseline[key] != wanted[key] {
							t.Error("canonical target changed original native state or enforcement", name, key)
						}
					}
				}
			}
			app := createRecipeObject(t, target, "Deployment/"+bindings.Stack+"-video-cloud/video-cloud-api-app-pki")
			if deploymentplan.Map(environmentPKIPod(app)["securityContext"])["fsGroup"] != nil {
				t.Fatal("canonical native App startup can rewrite original private Service state permissions")
			}
			controller := createRecipeObject(t, target, "Deployment/"+bindings.Stack+"-video-cloud/pki-controller")
			if deploymentplan.Map(environmentPKIPod(controller)["securityContext"])["fsGroupChangePolicy"] != "OnRootMismatch" {
				t.Fatal("canonical controller restart can rewrite original private Service state permissions")
			}
			for _, group := range [][]deploymentplan.Definition{original, target} {
				device := createRecipeObject(t, group, "Deployment/"+bindings.Stack+"-video-cloud/video-cloud-api-pki")
				consumer := mqttFoundationPrimary(device)
				file, err := coreMQTTFile(device, consumer, "mqtt-callback", "ca.crt")
				if err != nil || file.reference != "pki-create-mqtt-callback-ca" || mqttFoundationEnv(consumer, "VIDEO_CLOUD_MQTT_AUTH_CLIENT_CA") != file.file || mqttFoundationEnv(consumer, "VIDEO_CLOUD_MQTT_AUTH_CLIENT_NAME") != "service:emqx-pki" {
					t.Fatal("canonical callback lost its original enrolled Service caller or read-only CA", err)
				}
			}
		})
	}
}

func TestCustodyFreshNativeInventoryCanonicalRoundTrip(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			cfg, p, release, runtime, _, original := createdNativeInventoryFixture(t, environment, true)
			live := make([]deploymentplan.Object, 0, len(original))
			for _, d := range original {
				live = append(live, d.Object)
			}
			bindings, err := observeEnvironmentBindings(environment, release, live)
			if err != nil {
				t.Fatal("observe all created OpenBao owners together", err)
			}
			bindings.Custody = runtime.Custody
			target, err := renderEnvironmentResources(environmentCreateProviderValues(cfg), bindings, release)
			if err != nil {
				t.Fatal("render the same new generation's ordinary target", err)
			}
			for _, defs := range [][]deploymentplan.Definition{original, target} {
				var deployments []deploymentplan.Object
				for _, d := range defs {
					if d.Object["kind"] == "Deployment" {
						deployments = append(deployments, d.Object)
					}
				}
				generation, sources, err := environmentCustodyAdmissionWorkloads(cfg, deployments)
				if err != nil || generation != runtime.Custody.Generation {
					t.Fatal("shared checker lost original custody inventory", err)
				}
				for _, owner := range runtime.Custody.Owners {
					id := "Deployment/" + owner.Namespace + "/" + owner.ID
					var object deploymentplan.Object
					for _, d := range defs {
						if deploymentplan.ID(d.Object) == id {
							object = d.Object
							break
						}
					}
					// Product runtimes are installed by the normal tail after native bootstrap.
					if object == nil && len(p.ProductIdentities) > 0 {
						isProduct := false
						for _, product := range p.ProductIdentities {
							isProduct = isProduct || environmentCustodyProductWorkload(strings.TrimPrefix(product.Subject, "service:")) == owner.ID
						}
						if isProduct {
							continue
						}
					}
					if object == nil {
						t.Fatal("original applicable owner missing", id)
					}
					pod := environmentPKIPod(object)
					for _, raw := range certIssuerObjectList(pod["volumes"]) {
						claim := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(raw)["persistentVolumeClaim"])["claimName"])
						for _, alias := range owner.StorageAliases {
							if claim == alias {
								t.Fatal("identity-only PVC reappeared", id, alias)
							}
						}
					}
					selected := false
					for _, raw := range certIssuerObjectList(pod["containers"]) {
						c := deploymentplan.Map(raw)
						if mqttFoundationEnv(c, "PKI_STATE_BACKEND") != "openbao" {
							continue
						}
						selected = true
						if mqttFoundationEnv(c, "PKI_STATE_RECORD_PREFIX") != environment+"/"+runtime.Custody.Generation+"/"+owner.ID || pod["serviceAccountName"] != owner.ServiceAccount {
							t.Fatal("original custody scope changed", id)
						}
					}
					if !selected {
						t.Fatal("owner lost OpenBao configuration", id)
					}
					records, err := environmentCustodyAdmissionRecords(runtime.Custody, owner, strings.Repeat("f", 64))
					if err != nil || len(records) == 0 {
						t.Fatal("owner lost original credential purposes", id, err)
					}
					batch := environmentCustodyAdmissionBatch{ValidUntil: time.Now().UTC().Add(time.Minute), Records: records}
					source := sources[owner.Namespace+"/"+owner.ID]
					if _, err := environmentCustodyAdmissionJob(cfg, runtime.Custody, owner, source, p.Image, "", batch); err != nil {
						t.Fatal("cannot build a scoped public admission batch", id, err)
					}
				}
				providerFound := false
				for _, d := range defs {
					meta := deploymentplan.Map(d.Object["metadata"])
					pod := environmentPKIPod(d.Object)
					if meta["namespace"] != p.Stack+"-secrets" || meta["name"] != "openbao" || pod == nil {
						continue
					}
					for _, raw := range certIssuerObjectList(pod["containers"]) {
						if mqttFoundationEnv(deploymentplan.Map(raw), "PKI_STATE_BACKEND") == "openbao" {
							t.Fatal("OpenBao provider now depends on its own KV")
						}
					}
					for _, raw := range certIssuerObjectList(pod["volumes"]) {
						if deploymentplan.Map(deploymentplan.Map(raw)["persistentVolumeClaim"])["claimName"] == "openbao-pki-identity" {
							providerFound = true
						}
					}
				}
				if !providerFound {
					t.Fatal("provider lost independent cold-start identity claim")
				}
			}
			postgres := createRecipeObject(t, target, "StatefulSet/"+p.Stack+"-platform/postgresql")
			if len(certIssuerObjectList(deploymentplan.Map(postgres["spec"])["volumeClaimTemplates"])) == 0 {
				t.Fatal("business database volume was removed")
			}
		})
	}
}

func TestCreatedNativeInventoryCanonicalObservationRetainsRequiredAPITrust(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			_, p, release, _, _, definitions := createdNativeInventoryFixture(t, environment)
			for _, name := range []string{"video-cloud-api-pki", "video-cloud-api-app-pki"} {
				t.Run(name, func(t *testing.T) {
					object := createRecipeObject(t, definitions, "Deployment/"+p.Stack+"-video-cloud/"+name)
					bindings, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{object})
					if err != nil {
						t.Fatal("created native incoming owner cannot be observed", err)
					}
					observed := createRecipeEnv(deploymentplan.Object(bindings.Pods[0].Containers[0].Fields))
					original := createRecipeEnv(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(object)["containers"])[0]))
					keys := []string{"VIDEO_CLOUD_AUTH_CRL_MANIFEST", "VIDEO_CLOUD_AUTH_ROOT_TRUST_BOOTSTRAP_STATE"}
					if name == "video-cloud-api-pki" {
						keys = append(keys, "VIDEO_CLOUD_AUTH_DEVICE_AUTOMATIC_STATE_DIR")
					}
					for _, key := range keys {
						if original[key] == "" || observed[key] != original[key] {
							t.Error("canonical observation lost the original native trust requirement", key)
						}
					}
				})
			}
		})
	}
}

func TestCreatedNativeInventoryMQTTTransportCanUseItsOriginalMountedRoles(t *testing.T) {
	_, p, _, _, _, definitions := createdNativeInventoryFixture(t, "dev")
	for _, name := range []string{"video-cloud-api", "video-cloud-logingester"} {
		t.Run(name, func(t *testing.T) {
			object := createRecipeObject(t, definitions, "Deployment/"+p.Stack+"-video-cloud/"+name)
			container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(object)["containers"])[0])
			var err error
			if name == "video-cloud-api" {
				_, _, err = coreMQTTSettings(object, container, definitions)
			} else {
				_, _, err = logMQTTTransportSettings(object, container, definitions)
			}
			if err != nil {
				t.Fatal("created managed MQTT transport cannot be compiled from its actual native original mounts", err)
			}
		})
	}
}
