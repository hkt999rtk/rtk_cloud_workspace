package main

import (
	"encoding/base64"
	"errors"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestNativeFullCatalogUsesOnlyManagedMQTTAndPreservesLegacyFence(t *testing.T) {
	env, bindings, release := completeEnvironmentRenderFixture("dev")
	bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
	restore := installAllCredentialEnvironment(env)
	defer restore()
	defs, err := renderEnvironmentResources(env, bindings, release)
	if err != nil {
		t.Fatal(err)
	}
	byID := map[string]deploymentplan.Object{}
	for _, d := range defs {
		id := deploymentplan.ID(d.Object)
		byID[id] = d.Object
		if id == "StatefulSet/video-cloud-dev-video-cloud/mqtt" || id == "ConfigMap/video-cloud-dev-video-cloud/mqtt-config" || id == "Service/video-cloud-dev-video-cloud/mqtt-headless" {
			t.Fatal("native target still schedules obsolete broker", id)
		}
		for _, ref := range environmentResourceReferences(d.Object) {
			if ref.ID == "Secret/video-cloud-dev-video-cloud/mqtt-runtime" {
				t.Fatal("native target requires fabricated legacy TLS material", id)
			}
		}
	}
	if byID["Deployment/video-cloud-dev-video-cloud/mqtt-pki"] == nil {
		t.Fatal("native broker omitted")
	}
	mqtt := managedNamedField(environmentPKIPod(byID["Deployment/video-cloud-dev-video-cloud/mqtt-pki"])["containers"], "mqtt")
	settings := createRecipeEnv(mqtt)
	if settings["EMQX_LISTENERS__WS__TESTLAB__ENABLE"] != "true" || settings["EMQX_LISTENERS__WS__TESTLAB__ENABLE_AUTHN"] != "true" {
		t.Fatal("native callback process did not receive explicit selected listener intent")
	}

	alias := deploymentplan.Map(byID["Service/video-cloud-dev-video-cloud/mqtt"]["spec"])
	ports := certIssuerObjectList(alias["ports"])
	if deploymentplan.Map(alias["selector"])["app.kubernetes.io/name"] != "mqtt-pki" || len(ports) != 2 || deploymentplan.Map(ports[0])["port"] != float64(18083) {
		t.Fatal("billing management does not route to exact native owner")
	}
	if deploymentplan.Map(ports[1])["port"] != float64(8083) || deploymentplan.Map(ports[1])["targetPort"] != float64(8085) {
		t.Fatal("enabled Test Lab did not retain native WebSocket listener mapping")
	}
	policy := deploymentplan.Map(byID["NetworkPolicy/video-cloud-dev-video-cloud/allow-mqtt-pki"]["spec"])
	ws := 0
	for _, value := range certIssuerObjectList(policy["ingress"]) {
		rule := deploymentplan.Map(value)
		if deploymentplan.Map(certIssuerObjectList(rule["ports"])[0])["port"] != float64(8085) {
			continue
		}
		ws++
		from := certIssuerObjectList(rule["from"])
		if len(from) != 1 || deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(from[0])["namespaceSelector"])["matchLabels"])["kubernetes.io/metadata.name"] != "video-cloud-dev-admin" || deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(from[0])["podSelector"])["matchLabels"])["app.kubernetes.io/name"] != "cloud-admin" {
			t.Fatal("Test Lab admits a different actor")
		}
	}
	if ws != 1 {
		t.Fatal("Test Lab listener policy missing")
	}
	// Historical bootstrap endpoint recipe retains its four original objects.
	if len(environmentManagedConsumerDependencies(env)) != 4 {
		t.Fatal("ordinary full-target repair changed historical bootstrap recipe")
	}
	bindings.Pods = append(bindings.Pods, environmentPodBindings{Kind: "StatefulSet", NamespaceSuffix: "video-cloud", Name: "mqtt", Component: "existing-workload"})
	_, err = renderEnvironmentResources(env, bindings, release)
	var blocker *deploymentplan.Blocker
	if !errors.As(err, &blocker) || blocker.ID != "deployment.migration.mqtt" {
		t.Fatal("installed legacy broker silently vanished or was overwritten", err)
	}
}

func TestManagedMQTTDashboardMirrorSelectsOnlyObservedNativeProfile(t *testing.T) {
	p := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "create"}}
	mqtt := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "mqtt-pki", "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{
		deploymentplan.Object{"name": "mqtt", "env": []any{deploymentplan.Object{"name": "EMQX_DASHBOARD__DEFAULT_PASSWORD", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "mqtt-pki-runtime", "key": "dashboard-password"}}}}}, deploymentplan.Object{"name": "pkibroker"},
	}}}}}
	p.Resources = []deploymentplan.Resource{{ID: deploymentplan.ID(mqtt), Action: "retain", Desired: mqtt}}
	store := secretStore{Environment: "dev", checkRuntime: &deploymentCheckRuntime{preflightPlan: p}}
	original := secretK8SBinding{NamespaceSuffix: "-video-cloud", Secret: "mqtt-runtime", Key: "EMQX_DASHBOARD_PASSWORD"}
	binding, err := secretStoreMQTTDashboardBinding(store, original)
	if err != nil || binding.Secret != "mqtt-pki-runtime" || binding.Key != "dashboard-password" {
		t.Fatal("managed dashboard checks nonexistent legacy TLS mirror", err)
	}
	for _, mode := range []string{"foreign-namespace", "foreign-secret", "wrong-key", "literal-value", "optional", "duplicate-env", "extra-container"} {
		q := deploymentplan.Clone(mqtt)
		c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(q)["containers"])[0])
		e := deploymentplan.Map(certIssuerObjectList(c["env"])[0])
		ref := deploymentplan.Map(deploymentplan.Map(e["valueFrom"])["secretKeyRef"])
		switch mode {
		case "foreign-namespace":
			deploymentplan.Map(q["metadata"])["namespace"] = "video-cloud-prod-video-cloud"
		case "foreign-secret":
			ref["name"] = "arbitrary-secret"
		case "wrong-key":
			ref["key"] = "arbitrary-key"
		case "literal-value":
			e["value"] = "private"
		case "optional":
			ref["optional"] = true
		case "duplicate-env":
			c["env"] = append(certIssuerObjectList(c["env"]), e)
		case "extra-container":
			environmentPKIPod(q)["containers"] = append(certIssuerObjectList(environmentPKIPod(q)["containers"]), deploymentplan.Object{"name": "extra"})
		}
		if managedMQTTDashboardMirror(q, "video-cloud-dev") {
			t.Fatal("foreign managed profile selected", mode)
		}
	}
}
