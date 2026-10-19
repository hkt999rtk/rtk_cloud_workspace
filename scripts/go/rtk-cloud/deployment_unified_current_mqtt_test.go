package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func currentMQTTBindingsFixture(t *testing.T) (map[string]string, environmentRelease, []deploymentplan.Object) {
	t.Helper()
	env, _, release := completeEnvironmentRenderFixture("dev")
	defs := managedMQTTConsumersFixture(t)
	if err := bindEnvironmentManagedMQTTConsumers(defs); err != nil {
		t.Fatal(err)
	}
	_, cleanupOwner, _, cleanupLive := normalTailCleanupBindingFixture(t)
	if err := cleanupOwner.Execution.Close(); err != nil {
		t.Fatal(err)
	}
	nativeController := cleanupLive[0]
	var live []deploymentplan.Object
	claims := map[string]bool{}
	for _, d := range defs {
		o := deploymentplan.Clone(d.Object)
		if d.Object["kind"] == "Deployment" && deploymentplan.Map(d.Object["metadata"])["name"] == "pki-controller" {
			o = deploymentplan.Clone(nativeController)
			c := mqttFoundationPrimary(o)
			c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_REQUIRED_CONSUMERS_MQTT": "video-cloud-api,video-cloud-logingester,mqtt,shadow", "PKI_REQUIRED_BUNDLE_CONSUMERS_MQTT": "video-cloud-api,video-cloud-logingester"}))
		}
		if o["kind"] == "Deployment" && deploymentplan.Map(o["metadata"])["name"] == "video-cloud-api-pki" {
			pod := environmentPKIPod(o)
			c := mqttFoundationPrimary(o)
			pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{deploymentplan.Object{"name": "mqtt-callback", "configMap": deploymentplan.Object{"name": "pki-mqtt-callback-ca"}}})
			c["volumeMounts"] = mergeEnvironmentNamedFields(c["volumeMounts"], []any{environmentCreateServiceMount("mqtt-callback", "/run/mqtt-callback", true)})
		}
		meta := deploymentplan.Map(o["metadata"])
		name := deploymentplan.String(meta["name"])
		if strings.HasPrefix(name, "pki-product-") || o["kind"] == "Deployment" && name == "certissuer" {
			continue
		}
		meta["uid"] = fmt.Sprintf("00000000-0000-4000-8000-%012d", len(live)+1)
		meta["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-dev"}
		if o["kind"] == "Deployment" && name == "mqtt-pki" {
			broker, err := environmentManagedSidecar("pkibroker")
			if err != nil {
				t.Fatal(err)
			}
			environmentPKIPod(o)["containers"] = append(certIssuerObjectList(environmentPKIPod(o)["containers"]), broker)
		}
		for _, raw := range certIssuerObjectList(environmentPKIPod(o)["containers"]) {
			c := deploymentplan.Map(raw)
			c["image"] = release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference
			if name == "mqtt-pki" && c["name"] == "mqtt" {
				c["image"] = release.Images["LKE_MQTT_PKI_IMAGE"].Reference
			}
		}
		for _, raw := range certIssuerObjectList(environmentPKIPod(o)["volumes"]) {
			claim := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(raw)["persistentVolumeClaim"])["claimName"])
			if claim != "" {
				claims[claim] = true
			}
		}
		live = append(live, o)
	}
	live = append(live, cleanupLive[1:]...)
	_, _, nativeIssuer, issuerSecrets := registryIssuerCanonicalFixture(t, "dev")
	live = append(live, nativeIssuer)
	live = append(live, issuerSecrets...)
	for claim := range claims {
		live = append(live, deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": claim, "namespace": "video-cloud-dev-video-cloud", "uid": fmt.Sprintf("00000000-0000-4000-8000-%012d", len(live)+1)}})
	}
	env["MQTT_FOUNDATION_REGISTRATION_ENABLED"], env["LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED"] = "true", "true"
	env["SHADOW_WORKER_REGISTRATION_ENABLED"], env["LKE_SHADOW_WORKER_REGISTRATION_ENABLED"] = "true", "true"
	env["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"], env["LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] = "true", "true"
	return env, release, live
}

func TestCurrentMQTTBindingsOrdinaryRenderRetainsRetiredProducerPlacement(t *testing.T) {
	env, release, live := currentMQTTBindingsFixture(t)
	cfg := deploymentConfig{Environment: "dev", Values: env}
	plain, err := observeEnvironmentBindings("dev", release, live)
	if err != nil {
		t.Fatal("original observation", err)
	}
	before := deploymentplan.Digest(plain)
	create, err := observeEnvironmentTargetBindings(t.Context(), cfg, release, "create", live)
	if err != nil || deploymentplan.Digest(create) != before {
		t.Fatal("create bindings changed historical profile", err)
	}
	bindings, err := observeEnvironmentTargetBindings(t.Context(), cfg, release, "upgrade", live)
	if err != nil {
		t.Fatal("ordinary current observation", err)
	}
	profiles := 0
	for _, p := range bindings.Pods {
		if p.ManagedMQTT != nil {
			profiles++
		}
	}
	if profiles != 2 || bindings.ProductIdentities != nil {
		t.Fatal("ordinary observation did not preserve exactly both current workers")
	}
	raw, err := json.Marshal(bindings)
	if err != nil {
		t.Fatal(err)
	}
	var decoded environmentIdentityBindings
	if decodeEnvironmentJSON(raw, &decoded) != nil || validateEnvironmentIdentityBindings(decoded, "dev") != nil {
		t.Fatal("serialized current profile cannot be read")
	}
	// Actual shared renderer uses maintained release/image/resource behavior;
	// no original bootstrap or deleted enrollment controller is supplied.
	_, base, _ := completeEnvironmentRenderFixture("dev")
	seen := map[string]bool{}
	for _, p := range decoded.Pods {
		seen[p.Name] = true
	}
	for _, p := range base.Pods {
		if !seen[p.Name] {
			decoded.Pods = append(decoded.Pods, p)
		}
	}
	decoded.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
	restore := installAllCredentialEnvironment(env)
	defer restore()
	target, err := renderEnvironmentResources(env, decoded, release)
	if err != nil {
		t.Fatal("ordinary full renderer", err)
	}
	if err = validateEnvironmentManagedMQTTConsumers(target); err != nil {
		t.Fatal("ordinary target profile", err)
	}
	for _, name := range []string{"video-cloud-mqttfoundation", "video-cloud-shadowworker"} {
		wanted := mqttFoundationTarget(target, "Deployment", name)
		var original deploymentplan.Object
		for _, o := range live {
			if deploymentplan.ID(o) == deploymentplan.ID(wanted) {
				original = o
			}
		}
		if original == nil || deploymentplan.Digest(environmentPKIPod(original)["affinity"]) != deploymentplan.Digest(environmentPKIPod(wanted)["affinity"]) {
			t.Fatal("original RWO lineage changed", name)
		}
		if mqttFoundationEnv(mqttFoundationPrimary(wanted), "VIDEO_CLOUD_MQTT_ADDR") != "mqtt-pki.video-cloud-dev-video-cloud.svc:8883" {
			t.Fatal("ordinary target restored cleartext endpoint", name)
		}
		if err = validateEnvironmentPKITransition([]deploymentplan.Definition{{Object: wanted}}, []deploymentplan.Object{original}); err != nil {
			t.Fatal("ordinary PKI transition", err)
		}
		uid := decoded.Owners[deploymentplan.ID(wanted)]
		p, err := deploymentplan.Compile(deploymentplan.Inputs{Operation: "scoped", Environment: "dev", Stack: "video-cloud-dev", ConfigurationSHA256: deploymentplan.Digest("current-mqtt-configuration"), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(decoded)}, []deploymentplan.Definition{{Object: wanted, Phase: "workload"}}, live, map[string]string{deploymentplan.ID(wanted): uid})
		if err != nil || len(p.Resources) != 1 || p.Resources[0].UID != uid {
			t.Fatal("ordinary child owner compile", err)
		}
	}
	checkPlan := &deploymentplan.Plan{}
	current := map[string]deploymentplan.Object{}
	for _, o := range live {
		current[deploymentplan.ID(o)] = o
	}
	for _, d := range target {
		id := deploymentplan.ID(d.Object)
		if original := current[id]; original != nil {
			checkPlan.Resources = append(checkPlan.Resources, deploymentplan.Resource{ID: id, Action: "retain", UID: decoded.Owners[id], Desired: d.Object})
		}
	}
	retained := map[string]bool{}
	for _, r := range checkPlan.Resources {
		retained[r.ID] = true
	}
	for _, o := range live {
		id := deploymentplan.ID(o)
		if !retained[id] && (o["kind"] == "ConfigMap" || o["kind"] == "PersistentVolumeClaim") {
			checkPlan.Resources = append(checkPlan.Resources, deploymentplan.Resource{ID: id, Action: "retain", UID: decoded.Owners[id], Desired: o})
		}
	}
	if err = checkEnvironmentManagedMQTTConsumers(context.Background(), checkPlan, func(_ context.Context, id string) (deploymentplan.Object, error) { return current[id], nil }); err != nil {
		t.Fatal("ordinary full profile check", err)
	}
}
