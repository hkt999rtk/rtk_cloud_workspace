package main

import (
	"encoding/json"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCustodyObservedMemoryControllerRoundTripsThroughFullRenderer(t *testing.T) {
	for _, bootstrapRoot := range []string{"", strings.Repeat("c", 64)} {
		t.Run("bootstrap-"+bootstrapRoot, func(t *testing.T) {
			t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-memory-controller-render")
			env, bindings, release, current := nativeControllerAcceptedRecipeFixture(t)
			env["PKI_STATE_BACKEND"], env["PKI_STATE_KV_MOUNT"], env["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
			ns := bindings.Stack + "-video-cloud"
			custody := &environmentCustodyBinding{Generation: strings.Repeat("a", 64), OpenBaoRootSHA256: strings.Repeat("b", 64), Owners: []environmentCustodyOwnerBinding{{ID: environmentCreateServiceOwnerClaim("pki-controller"), Namespace: ns, ServiceAccount: "pki-controller"}}}
			projected, err := projectEnvironmentCustodyResources(env, "dev", custody, bootstrapRoot, []deploymentplan.Definition{{Object: current}})
			if err != nil {
				t.Fatal(err)
			}
			current = createRecipeObject(t, projected, deploymentplan.ID(current))
			observed, err := observeEnvironmentBindings("dev", release, []deploymentplan.Object{current})
			if err != nil || len(observed.Pods) != 1 {
				t.Fatal("actual memory binding observation", err)
			}
			for i := range bindings.Pods {
				if bindings.Pods[i].Component == "pki-controller" {
					bindings.Pods[i] = observed.Pods[0]
				}
			}
			bindings.Custody = custody
			restore := installAllCredentialEnvironment(env)
			defer restore()
			definitions, err := renderEnvironmentResources(env, bindings, release)
			if err != nil {
				t.Fatal("full renderer rejected original memory-backed identity", err)
			}
			target := createRecipeObject(t, definitions, deploymentplan.ID(current))
			if err := validateEnvironmentPKITransition(definitions, []deploymentplan.Object{current}); err != nil {
				t.Fatal("render changed original KV identity", err)
			}
			pod := environmentPKIPod(target)
			c := managedNamedField(pod["containers"], "pki-controller")
			if mqttFoundationEnv(c, "PKI_STATE_RECORD_PREFIX") != "dev/"+custody.Generation+"/"+environmentCreateServiceOwnerClaim("pki-controller") {
				t.Fatal("changed original KV owner")
			}
			if deploymentplan.Map(managedNamedField(pod["volumes"], "service-state")["emptyDir"])["medium"] != "Memory" {
				t.Fatal("private state is not memory-backed")
			}
			dirs, err := controllerIdentityDirectories(deploymentplan.Resource{ID: deploymentplan.ID(target), Action: "update", UID: deploymentplan.String(deploymentplan.Map(current["metadata"])["uid"]), Desired: target})
			if err != nil || len(dirs) != 0 {
				t.Fatal("KV identity incorrectly requires persistent directory inspection", err)
			}
			invalid := deploymentplan.Clone(target)
			managedNamedField(environmentPKIPod(invalid)["containers"], "pki-controller")["securityContext"] = deploymentplan.Object{"runAsUser": float64(0)}
			if _, err := controllerIdentityDirectories(deploymentplan.Resource{ID: deploymentplan.ID(invalid), Action: "update", Desired: invalid}); err == nil {
				t.Fatal("remote identity placement accepted an invalid runtime owner")
			}
		})
	}
}

func TestCustodyCoreAndLogMQTTTransportUseMemoryProjectionWithoutPVCPreparation(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, role := range []string{"core", "log"} {
			t.Run(environment+"/"+role, func(t *testing.T) {
				var defs []deploymentplan.Definition
				if role == "core" {
					defs, _ = coreMQTTFixture(t, environment)
				} else {
					defs, _ = logMQTTFixture(t, environment)
				}
				values := map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + environment, "CLOUD_ENV_NAME": environment, "PKI_STATE_BACKEND": "openbao", "PKI_STATE_KV_MOUNT": "identities", "PKI_STATE_LEASE_DURATION": "30s"}
				if role == "core" {
					if err := deriveEnvironmentCoreMQTTTransport(nil, defs); err != nil {
						t.Fatal(err)
					}
					if err := applyCoreIdentityPreparation(defs, identityPermissionAlpine); err != nil {
						t.Fatal(err)
					}
				} else {
					if err := deriveEnvironmentLogMQTTTransport(values, defs); err != nil {
						t.Fatal(err)
					}
				}
				custody := &environmentCustodyBinding{Generation: strings.Repeat("a", 64), OpenBaoRootSHA256: strings.Repeat("b", 64), Owners: []environmentCustodyOwnerBinding{{ID: "original-client-identity", Namespace: values["CLOUD_STACK_NAME"] + "-video-cloud", ServiceAccount: "api"}}}
				projected, err := projectEnvironmentCustodyResources(values, environment, custody, "", defs)
				if err != nil {
					t.Fatal(err)
				}
				if role == "core" {
					if err := deriveEnvironmentCoreMQTTTransport(nil, projected); err != nil {
						t.Fatal("remote core transport", err)
					}
					if err := applyCoreIdentityPreparation(projected, identityPermissionAlpine); err != nil {
						t.Fatal("remote core preparation", err)
					}
					if err := validateCoreIdentityInitializer(projected[0].Object, identityPermissionAlpine); err != nil {
						t.Fatal("remote core validation", err)
					}
					if managedNamedField(environmentPKIPod(projected[0].Object)["initContainers"], coreIdentityInitializerName) != nil {
						t.Fatal("retained initializer expects nonexistent PVC state")
					}
				} else {
					if err := deriveEnvironmentLogMQTTTransport(values, projected); err != nil {
						t.Fatal("remote log transport", err)
					}
				}
			})
		}
	}
}

func TestCustodyLiveCRLPlacementRequiresOriginalScopedMemory(t *testing.T) {
	defs, _ := coreMQTTFixture(t, "dev")
	values := map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "PKI_STATE_BACKEND": "openbao", "PKI_STATE_KV_MOUNT": "identities", "PKI_STATE_LEASE_DURATION": "30s"}
	custody := &environmentCustodyBinding{Generation: strings.Repeat("a", 64), OpenBaoRootSHA256: strings.Repeat("b", 64), Owners: []environmentCustodyOwnerBinding{{ID: "original-client-identity", Namespace: "video-cloud-dev-video-cloud", ServiceAccount: "api"}}}
	projected, err := projectEnvironmentCustodyResources(values, "dev", custody, "", defs)
	if err != nil {
		t.Fatal(err)
	}
	original := projected[0].Object
	for _, mode := range []string{"valid", "foreign-environment", "foreign-role", "disk-backed", "read-only", "shadow", "subpath"} {
		t.Run(mode, func(t *testing.T) {
			object := deploymentplan.Clone(original)
			pod := environmentPKIPod(object)
			c := managedNamedField(pod["containers"], "app")
			switch mode {
			case "foreign-environment":
				managedNamedField(c["env"], "PKI_ENVIRONMENT")["value"] = "staging"
			case "foreign-role":
				managedNamedField(c["env"], "PKI_STATE_OPENBAO_ROLE")["value"] = "other-owner"
			case "disk-backed":
				deploymentplan.Map(managedNamedField(pod["volumes"], "service-identity")["emptyDir"])["medium"] = ""
			case "read-only":
				managedNamedField(c["volumeMounts"], "service-identity")["readOnly"] = true
			case "subpath":
				managedNamedField(c["volumeMounts"], "service-identity")["subPath"] = "other"
			case "shadow":
				c["volumeMounts"] = append(certIssuerObjectList(c["volumeMounts"]), deploymentplan.Object{"name": "shadow", "mountPath": "/identity"})
			}
			var live liveDeployment
			raw, _ := json.Marshal(object)
			if json.Unmarshal(raw, &live) != nil {
				t.Fatal("invalid fixture")
			}
			if got := livePKIStateOnStorage(live, "app", "/identity/client-crl.json"); got != (mode == "valid") {
				t.Fatal("live CRL state placement disagrees with original custody scope", mode, got)
			}
		})
	}
}

func TestCustodyManagedMQTTConsumersRebindWithoutReintroducingPVC(t *testing.T) {
	defs := managedMQTTConsumersFixture(t)
	if err := bindEnvironmentManagedMQTTConsumers(defs, "baa1f144-085c-4e75-aa8d-014b61f26f25"); err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "PKI_STATE_BACKEND": "openbao", "PKI_STATE_KV_MOUNT": "identities", "PKI_STATE_LEASE_DURATION": "30s", "MQTT_FOUNDATION_REGISTRATION_ENABLED": "true", "SHADOW_WORKER_REGISTRATION_ENABLED": "true"}
	custody := &environmentCustodyBinding{Generation: strings.Repeat("a", 64), OpenBaoRootSHA256: strings.Repeat("b", 64), ServiceRootSHA256: strings.Repeat("c", 64)}
	for _, owner := range []struct{ id, alias string }{{"api", "original-client-identity"}, {"video-cloud-api-pki", "video-cloud-api-pki-controller-identity"}, {"video-cloud-mqttfoundation", "pki-product-mqtt-identity"}, {"video-cloud-shadowworker", "pki-product-shadow-identity"}} {
		custody.Owners = append(custody.Owners, environmentCustodyOwnerBinding{ID: owner.id, Namespace: "video-cloud-dev-video-cloud", ServiceAccount: owner.id, StorageAliases: []string{owner.alias}})
	}
	projected, err := projectEnvironmentCustodyResources(values, "dev", custody, "", defs)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateEnvironmentManagedMQTTConsumers(projected); err != nil {
		t.Fatal("remote MQTT target rejects its original memory projection", err)
	}
	for _, d := range projected {
		if d.Object["kind"] != "Deployment" {
			continue
		}
		name := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"])
		container, _ := managedMQTTWorkerRole(name)
		if container == "" {
			continue
		}
		pod := environmentPKIPod(d.Object)
		c := managedNamedField(pod["containers"], container)
		uid := "11111111-1111-4111-8111-111111111111"
		p := environmentPodBindings{Kind: "Deployment", Name: name, NamespaceSuffix: "video-cloud", PodFields: map[string]any{"volumes": pod["volumes"], "serviceAccountName": pod["serviceAccountName"]}, Containers: []environmentContainerBindings{{Name: container, Fields: map[string]any{"env": c["env"], "volumeMounts": c["volumeMounts"]}}}, ManagedMQTT: &environmentManagedMQTTPlacement{OwnerUID: uid, Affinity: deploymentplan.Map(pod["affinity"])}}
		if err := validateEnvironmentManagedMQTTPlacement(environmentIdentityBindings{Stack: "video-cloud-dev", Owners: map[string]string{deploymentplan.ID(d.Object): uid}}, p); err != nil {
			t.Fatal("current memory-backed worker placement", err)
		}
	}
	before := deploymentplan.Digest(projected)
	if err := bindEnvironmentManagedMQTTConsumersInternal(projected, true); err != nil {
		t.Fatal("rebind remote consumers", err)
	}
	if deploymentplan.Digest(projected) != before {
		t.Fatal("rebinding changed original remote identity or transport")
	}
}
