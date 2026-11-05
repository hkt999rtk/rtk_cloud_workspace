package main

import (
	"encoding/base64"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestCoreMQTTStateInitializerRetainsExistingTrustAcrossEnvironments(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-core-initializer")
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			for _, workload := range []string{"video-cloud-api", "video-cloud-logingester"} {
				t.Run(workload, func(t *testing.T) {
					env, bindings, release := completeEnvironmentRenderFixture(environment)
					bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "original-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
					restore := installAllCredentialEnvironment(env)
					defer restore()
					definitions, err := renderEnvironmentResources(env, bindings, release)
					if err != nil {
						t.Fatal(err)
					}
					base := mqttFoundationTarget(definitions, "Deployment", workload)
					current := deploymentplan.Clone(base)
					deploymentplan.Map(current["metadata"])["uid"] = "original-core"
					init, err := maintainedCoreMQTTInitializer("mqtt-pki-state-init")
					if err != nil {
						t.Fatal(err)
					}
					init["image"] = "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:" + strings.Repeat("d", 64)
					init["env"] = []any{map[string]any{"name": "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256", "value": "original-root-pin"}}
					environmentPKIPod(current)["initContainers"] = []any{init}
					environmentPKIPod(current)["volumes"] = append(certIssuerObjectList(environmentPKIPod(current)["volumes"]), map[string]any{"name": "mqtt-pki-state", "persistentVolumeClaim": map[string]any{"claimName": "original-core-trust"}})
					observed, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{current})
					if err != nil {
						t.Fatal(err)
					}
					if err := validateEnvironmentIdentityBindings(observed, environment); err != nil {
						t.Fatal(err)
					}
					target := deploymentplan.Clone(base)
					if err := applyEnvironmentPodBindings(target, observed, release); err != nil {
						t.Fatal(err)
					}
					if err := validateEnvironmentPKITransition([]deploymentplan.Definition{{Object: target}}, []deploymentplan.Object{current}); err != nil {
						t.Fatal(err)
					}
					actual := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(target)["initContainers"])[0])
					if actual["image"] != release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference || mqttFoundationEnv(actual, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256") != "original-root-pin" {
						t.Fatal("initializer image or root binding lost")
					}
					claim := deploymentplan.Map(managedNamedField(environmentPKIPod(target)["volumes"], "mqtt-pki-state")["persistentVolumeClaim"])
					if claim["claimName"] != "original-core-trust" {
						t.Fatal("original state replaced")
					}
					bad := deploymentplan.Clone(current)
					deploymentplan.Map(certIssuerObjectList(environmentPKIPod(bad)["initContainers"])[0])["command"] = []any{"/bin/sh", "-ec", "rm -rf /var/lib/mqtt-pki"}
					if _, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{bad}); err == nil {
						t.Fatal("unknown initializer recipe adopted")
					}
				})
			}
		})
	}
}

func TestCoreMQTTUpgradePreservesInstalledLegacyCacheGroup(t *testing.T) {
	_, _, release := completeEnvironmentRenderFixture("dev")
	for _, tc := range []struct {
		name, claim string
		group       float64
		want        bool
	}{
		{"approved-cache", "video-cloud-api-service-state", 10001, true},
		{"foreign-claim", "foreign", 10001, false},
		{"foreign-group", "video-cloud-api-service-state", 42, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			current := deploymentplan.Object{
				"apiVersion": "apps/v1", "kind": "Deployment",
				"metadata": deploymentplan.Object{"name": "video-cloud-api", "namespace": "video-cloud-dev-video-cloud", "uid": "original-api"},
				"spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{
					"securityContext": deploymentplan.Object{"supplementalGroups": []any{tc.group}},
					"volumes":         []any{deploymentplan.Object{"name": "mqtt-bundle-state", "persistentVolumeClaim": deploymentplan.Object{"claimName": tc.claim}}},
					"containers": []any{deploymentplan.Object{
						"name": "app", "image": release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference,
						"volumeMounts": []any{deploymentplan.Object{"name": "mqtt-bundle-state", "mountPath": "/var/lib/mqtt-trust"}},
					}},
				}}},
			}
			bindings, err := observeEnvironmentBindings("dev", release, []deploymentplan.Object{current})
			if err != nil {
				t.Fatal(err)
			}
			target := deploymentplan.Clone(current)
			delete(environmentPKIPod(target), "securityContext")
			if err := applyEnvironmentPodBindings(target, bindings, release); err != nil {
				t.Fatal(err)
			}
			got := deploymentplan.Map(environmentPKIPod(target)["securityContext"])
			if (deploymentplan.Digest(got) == deploymentplan.Digest(deploymentplan.Object{"supplementalGroups": []any{float64(10001)}})) != tc.want {
				t.Fatalf("rendered unexpected supplemental groups: %v", got)
			}
		})
	}
}
