package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func managedMQTTConsumersFixture(t *testing.T) []deploymentplan.Definition {
	t.Helper()
	defs, public := coreMQTTFixture(t, "dev")
	donor := mqttFoundationPrimary(defs[0].Object)
	if err := deriveEnvironmentCoreMQTTTransport(nil, defs); err != nil {
		t.Fatal(err)
	}
	donor["env"] = mergeEnvironmentNamedFields(donor["env"], environmentCreateEnv(map[string]string{"VIDEO_CLOUD_MQTT_ENABLED": "true", "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_MANIFEST": "/mounted/mqtt-pki-bundles/public/bundles.json", "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_ROOT_SHA256": mqttFoundationEnv(donor, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256"), "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_PKI_CONTROLLER_URL": "https://pki-controller.video-cloud-dev-video-cloud.svc:18446", "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_MANAGEMENT_CA": "/mounted/service-root/public/root.pem"}))
	// The native broker's own pin independently binds the selected outbound pool.
	broker := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "mqtt-pki", "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "mqtt", "env": environmentCreateEnv(map[string]string{"EMQX_PKI_HOST_ROOT_SHA256": mqttFoundationEnv(donor, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256")})}}}}}}
	defs = append(defs, deploymentplan.Definition{Object: broker, Phase: "workload"})
	for _, d := range defs {
		if d.Object["kind"] == "Deployment" && deploymentplan.Map(d.Object["metadata"])["name"] == "pki-controller" {
			c := mqttFoundationPrimary(d.Object)
			c["name"] = "pki-controller"
			c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_REQUIRED_CONSUMERS_MQTT": "video-cloud-api,video-cloud-logingester"}))
		}
	}
	api := deploymentplan.Clone(defs[0].Object)
	deploymentplan.Map(api["metadata"])["name"] = "video-cloud-api-pki"
	delete(deploymentplan.Map(api["metadata"]), "uid")
	pod := environmentPKIPod(api)
	c := mqttFoundationPrimary(api)
	var envs, volumes, mounts []any
	for _, raw := range certIssuerObjectList(c["env"]) {
		e := deploymentplan.Map(raw)
		if !managedMQTTConsumerSettings(deploymentplan.String(e["name"])) {
			envs = append(envs, raw)
		}
	}
	c["env"] = mergeEnvironmentNamedFields(envs, environmentCreateEnv(map[string]string{"VIDEO_CLOUD_MQTT_ENABLED": "true", "VIDEO_CLOUD_MQTT_ADDR": "mqtt.video-cloud-dev-video-cloud.svc.cluster.local:1883"}))
	for _, raw := range certIssuerObjectList(pod["volumes"]) {
		v := deploymentplan.Map(raw)
		if v["name"] == "service-identity" {
			deploymentplan.Map(v["persistentVolumeClaim"])["claimName"] = "video-cloud-api-pki-controller-identity"
		}
		if v["name"] != "mqtt-pki-bundles" && v["name"] != "mqtt-pki-crls" && v["name"] != "service-root" {
			volumes = append(volumes, raw)
		}
	}
	for _, raw := range certIssuerObjectList(c["volumeMounts"]) {
		m := deploymentplan.Map(raw)
		if m["name"] != "mqtt-pki-bundles" && m["name"] != "mqtt-pki-crls" && m["name"] != "service-root" {
			mounts = append(mounts, raw)
		}
	}
	// Real native api-pki keeps the service-trust role mounted before MQTT
	// transport is bound. Omitting it previously masked duplicate mountPaths.
	for _, raw := range certIssuerObjectList(environmentPKIPod(defs[0].Object)["volumes"]) {
		if deploymentplan.Map(raw)["name"] == "service-root" {
			v := deploymentplan.Clone(deploymentplan.Map(raw))
			v["name"] = "service-trust"
			volumes = append(volumes, v)
		}
	}
	for _, raw := range certIssuerObjectList(donor["volumeMounts"]) {
		if deploymentplan.Map(raw)["name"] == "service-root" {
			m := deploymentplan.Clone(deploymentplan.Map(raw))
			m["name"] = "service-trust"
			mounts = append(mounts, m)
		}
	}
	pod["volumes"], c["volumeMounts"] = volumes, mounts
	defs = append(defs, deploymentplan.Definition{Object: api, Phase: "workload"})
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "CLOUD_ENV_NAME": "dev", "LKE_VIDEO_CLOUD_IMAGE": "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + strings.Repeat("a", 64)}
	workers, err := decodeEnvironmentCreateManifests(lkeMQTTFoundationDeploymentManifest(env), lkeShadowWorkerDeploymentManifest(env))
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range workers {
		d.Phase = "workload"
		defs = append(defs, d)
	}
	session := "baa1f144-085c-4e75-aa8d-014b61f26f25"
	for _, role := range []string{"mqtt", "shadow"} {
		name := "pki-product-" + role + "-" + strings.ReplaceAll(session, "-", "")[:12]
		labels := deploymentplan.Object{"app.kubernetes.io/name": name}
		template := deploymentplan.Object{"metadata": deploymentplan.Object{"labels": labels}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "enroll", "image": env["LKE_VIDEO_CLOUD_IMAGE"]}}, "volumes": []any{environmentCreateServiceVolume("service-state", "persistentVolumeClaim", "pki-product-"+role+"-identity")}}}
		object := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": name, "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"replicas": float64(1), "selector": deploymentplan.Object{"matchLabels": labels}, "template": template}}
		defs = append(defs, deploymentplan.Definition{Phase: "workload", Retain: true, Object: object})
	}

	for _, o := range public {
		defs = append(defs, deploymentplan.Definition{Object: o, Phase: "configuration", Retain: true})
	}
	for i := range defs {
		if defs[i].Phase == "" {
			defs[i].Phase = "workload"
		}
	}
	return defs
}

func TestManagedMQTTConsumersPreserveIdentityAndRWOPlacement(t *testing.T) {
	defs := managedMQTTConsumersFixture(t)
	before := deploymentplan.Digest(defs)
	secretSources := map[string]string{}
	for _, d := range defs {
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["volumes"]) {
			v := deploymentplan.Map(raw)
			if v["secret"] != nil {
				secretSources[deploymentplan.ID(d.Object)+"/"+deploymentplan.String(v["name"])] = deploymentplan.Digest(v)
			}
		}
	}
	if err := bindEnvironmentManagedMQTTConsumers(defs); err != nil {
		t.Fatal(err)
	}
	if before == deploymentplan.Digest(defs) {
		t.Fatal("bad projection unchanged")
	}
	for _, d := range defs {
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
			c := deploymentplan.Map(raw)
			claim := managedMQTTConsumerClaim(d.Object, c)
			if claim == "" {
				continue
			}
			if mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_ADDR") != "mqtt-pki.video-cloud-dev-video-cloud.svc:8883" || mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_TLS") != "true" {
				t.Fatal("native broker not selected")
			}
			pod := environmentPKIPod(d.Object)
			if c["name"] != "app" {
				if deploymentplan.Map(pod["securityContext"])["fsGroup"] != nil {
					t.Fatal("existing private files could be chmodded")
				}
				affinity := deploymentplan.Map(deploymentplan.Map(pod["affinity"])["podAffinity"])
				terms := certIssuerObjectList(affinity["requiredDuringSchedulingIgnoredDuringExecution"])
				if len(terms) != 1 {
					t.Fatal("missing exact same-node placement")
				}
				term := deploymentplan.Map(terms[0])
				if term["topologyKey"] != "kubernetes.io/hostname" || deploymentplan.Digest(term["namespaces"]) != deploymentplan.Digest([]any{"video-cloud-dev-video-cloud"}) {
					t.Fatal("foreign placement domain")
				}
				expressions := certIssuerObjectList(deploymentplan.Map(term["labelSelector"])["matchExpressions"])
				values := certIssuerObjectList(deploymentplan.Map(expressions[0])["values"])
				if len(values) != 2 || values[0] != deploymentplan.Map(d.Object["metadata"])["name"] {
					t.Fatal("replacement cannot satisfy first-pod self affinity")
				}
			}
		}
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["volumes"]) {
			v := deploymentplan.Map(raw)
			if v["secret"] != nil && secretSources[deploymentplan.ID(d.Object)+"/"+deploymentplan.String(v["name"])] != deploymentplan.Digest(v) {
				t.Fatal("identity Secret source changed")
			}
		}
	}
	first := deploymentplan.Digest(defs)
	if err := bindEnvironmentManagedMQTTConsumers(defs); err != nil || deploymentplan.Digest(defs) != first {
		t.Fatal("projection not idempotent", err)
	}
	// After genuine retirement the same immutable installed SID reproduces the
	// selector; its self member permits the first replacement without a producer.
	var final []deploymentplan.Definition
	for _, d := range defs {
		if !strings.HasPrefix(deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"]), "pki-product-") {
			final = append(final, d)
		}
	}
	if err := bindEnvironmentManagedMQTTConsumers(final, "baa1f144-085c-4e75-aa8d-014b61f26f25"); err != nil {
		t.Fatal("retired producer blocked replacement", err)
	}
	if err := validateEnvironmentManagedMQTTConsumers(final); err != nil {
		t.Fatal("final health loses declared RWO binding", err)
	}
}

func TestManagedMQTTConsumersRejectForeignNativeProfiles(t *testing.T) {
	for _, mode := range []string{"broker-root", "identity-secret", "foreign-claim", "foreign-namespace", "foreign-affinity", "controller-policy"} {
		t.Run(mode, func(t *testing.T) {
			defs := managedMQTTConsumersFixture(t)
			worker := mqttFoundationTarget(defs, "Deployment", "video-cloud-mqttfoundation")
			switch mode {
			case "broker-root":
				c := mqttFoundationPrimary(mqttFoundationTarget(defs, "Deployment", "mqtt-pki"))
				c["env"] = environmentCreateEnv(map[string]string{"EMQX_PKI_HOST_ROOT_SHA256": strings.Repeat("b", 64)})
			case "identity-secret":
				for _, raw := range certIssuerObjectList(environmentPKIPod(worker)["volumes"]) {
					v := deploymentplan.Map(raw)
					if v["secret"] != nil {
						deploymentplan.Map(v["secret"])["secretName"] = "foreign"
					}
				}
			case "foreign-claim":
				for _, d := range defs {
					if strings.HasPrefix(deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"]), "pki-product-mqtt-") {
						deploymentplan.Map(certIssuerObjectList(environmentPKIPod(d.Object)["volumes"])[0])["persistentVolumeClaim"] = deploymentplan.Object{"claimName": "foreign"}
					}
				}
			case "foreign-namespace":
				deploymentplan.Map(worker["metadata"])["namespace"] = "video-cloud-staging-video-cloud"
			case "foreign-affinity":
				environmentPKIPod(worker)["affinity"] = deploymentplan.Object{"podAffinity": deploymentplan.Object{"requiredDuringSchedulingIgnoredDuringExecution": []any{deploymentplan.Object{"topologyKey": "foreign"}}}}
			case "controller-policy":
				mqttFoundationPrimary(mqttFoundationTarget(defs, "Deployment", "pki-controller"))["env"] = environmentCreateEnv(map[string]string{"PKI_REQUIRED_CONSUMERS_MQTT": "video-cloud-api,,shadow"})
			}
			if err := bindEnvironmentManagedMQTTConsumers(defs); err == nil {
				t.Fatal("foreign native profile accepted")
			}
		})
	}
}
