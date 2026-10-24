package main

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func actualManagedMQTTConsumersFixture(t *testing.T, environment string) ([]deploymentplan.Definition, string) {
	t.Helper()
	return actualManagedMQTTConsumersEnrollmentFixture(t, environment, false)
}

func actualManagedMQTTConsumersEnrollmentFixture(t *testing.T, environment string, enrollment bool) ([]deploymentplan.Definition, string) {
	t.Helper()
	cfg, p, release, runtime, service, roots, intermediates, _ := createApplicationFinalizeFixture(t, environment)
	store := runtime.Backend.Store
	if store.Root == "" || store.Environment != environment {
		t.Fatal("actual native fixture lost selected runtime store")
	}
	for _, entry := range rtkSecretCatalog() {
		if _, err := store.read(filepath.Join("runtime", entry.ID)); err == nil {
			continue
		}
		if err := store.write(filepath.Join("runtime", entry.ID), []byte("isolated-existing-"+entry.ID+"\n"), false); err != nil {
			t.Fatal(err)
		}
	}
	// Use the real bootstrap application producer, followed by its real native
	// finalizer. In particular api-pki already owns service-trust at /run/pki-service.
	initial, err := renderEnvironmentCreateApplicationOwners(t.Context(), cfg, p, release, store, nil, service)
	if err != nil {
		t.Fatal(err)
	}
	final, err := finalizeEnvironmentCreateApplicationDefinitions(t.Context(), cfg, p, release, runtime, service, roots, intermediates, initial)
	if err != nil {
		t.Fatal(err)
	}
	api := createRecipeObject(t, final, "Deployment/"+p.Stack+"-video-cloud/video-cloud-api-pki")
	deploymentplan.Map(api["metadata"])["uid"] = "11111111-1111-4111-8111-111111111111"
	observed, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{api})
	if err != nil || len(observed.Pods) != 1 {
		t.Fatal("actual API observation", err)
	}
	// The ordinary target restores its maintained runtime while preserving the
	// enrolled state, original public mount aliases and exact private references.
	wanted, err := renderManagedEnvironmentComponent(cfg.Values, observed.Pods[0])
	if err != nil {
		t.Fatal(err)
	}
	if err = applyEnvironmentPodBindings(wanted, observed, release); err != nil {
		t.Fatal(err)
	}
	if mqttFoundationEnv(mqttFoundationPrimary(wanted), "VIDEO_CLOUD_MQTT_ENABLED") != "true" {
		t.Fatal("actual maintained API does not exercise MQTT")
	}
	var defs []deploymentplan.Definition
	for _, d := range final {
		if deploymentplan.ID(d.Object) == deploymentplan.ID(wanted) {
			d.Object = wanted
		}
		defs = append(defs, d)
	}
	managed, err := renderEnvironmentCreateManagedServiceDefinitions(t.Context(), cfg, p, runtime, service)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range managed {
		name := deploymentplan.Map(d.Object["metadata"])["name"]
		if name == "pki-create-service-trust" || name == "pki-controller" && d.Object["kind"] == "Deployment" {
			defs = append(defs, d)
		}
	}
	transport, err := renderEnvironmentCreateTransportDefinitions(t.Context(), cfg, p, runtime, service, roots, intermediates, release)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range transport {
		if d.Object["kind"] == "Service" && deploymentplan.Map(d.Object["metadata"])["name"] == "mqtt-pki" {
			found := false
			for _, existing := range defs {
				found = found || deploymentplan.ID(existing.Object) == deploymentplan.ID(d.Object)
			}
			if !found {
				defs = append(defs, d)
			}
		}
	}
	workers, err := decodeEnvironmentCreateManifests(lkeMQTTFoundationDeploymentManifest(cfg.Values), lkeShadowWorkerDeploymentManifest(cfg.Values))
	if err != nil {
		t.Fatal(err)
	}
	defs = append(defs, workers...)
	for _, claim := range []string{"video-cloud-api-pki-controller-identity", "pki-product-mqtt-identity", "pki-product-shadow-identity"} {
		defs = append(defs, environmentCreateServiceClaimDefinition(cfg, p.Stack+"-video-cloud", claim))
	}
	if enrollment {
		for _, role := range []string{"mqtt", "shadow"} {
			owner, err := productIdentityEnrollmentOwner(cfg, p, runtime, service, environmentCreateProductIdentity{Subject: "service:" + role, Claim: "pki-product-" + role + "-identity"})
			if err != nil {
				t.Fatal(err)
			}
			defs = append(defs, owner)
		}
	}
	seen := map[string]bool{}
	for i := range defs {
		id := deploymentplan.ID(defs[i].Object)
		if seen[id] {
			t.Fatal("actual producer fixture contains duplicate resource", id)
		}
		seen[id] = true
		deploymentplan.Map(defs[i].Object["metadata"])["uid"] = fmt.Sprintf("aaaaaaaa-bbbb-4ccc-8ddd-%012d", i+1)
	}
	return defs, service.BootstrapSessionID
}

func checkActualManagedMQTTMounts(t *testing.T, defs []deploymentplan.Definition) {
	t.Helper()
	selected := 0
	plan := &deploymentplan.Plan{}
	objects := map[string]deploymentplan.Object{}
	for _, d := range defs {
		id := deploymentplan.ID(d.Object)
		objects[id] = d.Object
		plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: id, Action: "retain", UID: deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["uid"]), Desired: d.Object})
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
			c := deploymentplan.Map(raw)
			if managedMQTTConsumerClaim(d.Object, c) == "" {
				continue
			}
			selected++
			for _, group := range []string{"containers", "initContainers"} {
				for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)[group]) {
					container := deploymentplan.Map(raw)
					paths := map[string]bool{}
					for _, raw := range certIssuerObjectList(container["volumeMounts"]) {
						mount := deploymentplan.Map(raw)
						path := deploymentplan.String(mount["mountPath"])
						if paths[path] {
							t.Fatal("duplicate selected-consumer mountPath", id, container["name"], path)
						}
						paths[path] = true
					}
				}
			}
		}
	}
	if selected != 3 {
		t.Fatal("all three actual maintained consumers must be checked", selected)
	}
	if err := checkEnvironmentManagedMQTTConsumers(context.Background(), plan, func(_ context.Context, id string) (deploymentplan.Object, error) { return objects[id], nil }); err != nil {
		t.Fatal("actual runtime profile check", err)
	}
}

func TestManagedMQTTActualMaintainedAPIReusesServiceTrustAliasWithoutDuplicateMounts(t *testing.T) {
	for _, profile := range []struct{ environment, alias string }{{"dev", "service-trust"}, {"staging", "service-trust"}, {"dev", "retained-service-public"}} {
		environment, alias := profile.environment, profile.alias
		t.Run(environment+"/"+alias, func(t *testing.T) {
			defs, session := actualManagedMQTTConsumersFixture(t, environment)
			api := mqttFoundationTarget(defs, "Deployment", "video-cloud-api-pki")
			pod := environmentPKIPod(api)
			c := mqttFoundationPrimary(api)
			mount := managedNamedField(c["volumeMounts"], "service-trust")
			volume := managedNamedField(pod["volumes"], "service-trust")
			if mount["mountPath"] != "/run/pki-service" || mount["readOnly"] != true || deploymentplan.Map(volume["configMap"])["name"] != "pki-create-service-trust" {
				t.Fatal("fixture did not exercise the original maintained ServiceRoot alias")
			}
			mount["name"], volume["name"] = alias, alias
			original := deploymentplan.Digest([]any{mount, volume})
			if err := bindEnvironmentManagedMQTTConsumers(defs, session); err != nil {
				t.Fatal(err)
			}
			checkActualManagedMQTTMounts(t, defs)
			if deploymentplan.Digest([]any{managedNamedField(c["volumeMounts"], alias), managedNamedField(pod["volumes"], alias)}) != original {
				t.Fatal("original ServiceRoot projection was rewritten")
			}
			first := deploymentplan.Digest(defs)
			if err := bindEnvironmentManagedMQTTConsumers(defs, session); err != nil || deploymentplan.Digest(defs) != first {
				t.Fatal("actual maintained profile is not idempotent", err)
			}
			checkActualManagedMQTTMounts(t, defs)
		})
	}
}

func TestManagedMQTTActualMaintainedAPIRejectsForeignServiceTrustAlias(t *testing.T) {
	for _, mode := range []string{"missing-source", "foreign-configmap", "writable", "subpath", "shadowed"} {
		t.Run(mode, func(t *testing.T) {
			defs, session := actualManagedMQTTConsumersFixture(t, "dev")
			api := mqttFoundationTarget(defs, "Deployment", "video-cloud-api-pki")
			pod := environmentPKIPod(api)
			c := mqttFoundationPrimary(api)
			mount := managedNamedField(c["volumeMounts"], "service-trust")
			volume := managedNamedField(pod["volumes"], "service-trust")
			switch mode {
			case "missing-source":
				delete(volume, "configMap")
			case "foreign-configmap":
				deploymentplan.Map(volume["configMap"])["name"] = "foreign-service-trust"
			case "writable":
				mount["readOnly"] = false
			case "subpath":
				mount["subPath"] = "root.pem"
			case "shadowed":
				c["volumeMounts"] = append(certIssuerObjectList(c["volumeMounts"]), environmentCreateServiceMount("foreign-root", "/run/pki-service", true))
				pod["volumes"] = append(certIssuerObjectList(pod["volumes"]), environmentCreateServiceVolume("foreign-root", "configMap", "foreign-trust"))
			}
			if err := bindEnvironmentManagedMQTTConsumers(defs, session); err == nil {
				t.Fatal("foreign or ambiguous public ServiceRoot alias accepted", mode)
			}
		})
	}
}
