package main

import (
	"encoding/base64"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestSharedShadowWorkerKeepsCoreRoleAndOriginalIdentityAcrossEnvironments(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-shadow-render")
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, b, release := completeEnvironmentRenderFixture(environment)
			env["SHADOW_WORKER_REGISTRATION_ENABLED"], env["LKE_SHADOW_WORKER_REGISTRATION_ENABLED"] = "true", "true"
			b.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			restore := installAllCredentialEnvironment(env)
			defer restore()
			definitions, err := renderEnvironmentResources(env, b, release)
			if err != nil {
				t.Fatal(err)
			}
			worker := mqttFoundationTarget(definitions, "Deployment", "video-cloud-shadowworker")
			container := mqttFoundationPrimary(worker)
			if worker == nil || container["image"] != release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference || mqttFoundationEnv(container, "VIDEO_CLOUD_ENV") != environment {
				t.Fatal("Shadow worker missing, wrong environment or image")
			}
			core := mqttFoundationPrimary(mqttFoundationTarget(definitions, "Deployment", "video-cloud-api"))
			if mqttFoundationEnv(core, "VIDEO_CLOUD_SHADOW_WORKER_ENABLED") != "true" || mqttFoundationEnv(core, "VIDEO_CLOUD_SHADOW_HTTP_SERVICE_CUTOVER_ENABLED") != "false" {
				t.Fatal("core consumers or HTTP ownership changed")
			}
			for _, value := range certIssuerObjectList(container["env"]) {
				if deploymentplan.Map(value)["name"] == "VIDEO_CLOUD_SHADOW_WORKER_INSTANCE_ID" {
					deploymentplan.Map(value)["value"] = "approved-original-shadow-instance"
				}
			}
			deploymentplan.Map(managedNamedField(environmentPKIPod(worker)["volumes"], "platform-service-identity")["secret"])["secretName"] = "original-shadow-identity"
			deploymentplan.Map(worker["metadata"])["uid"] = "original-shadow-owner"
			observed, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{worker})
			if err != nil {
				t.Fatal(err)
			}
			b.Pods = append(b.Pods, observed.Pods...)
			targets, err := renderEnvironmentResources(env, b, release)
			if err != nil {
				t.Fatal(err)
			}
			target := mqttFoundationTarget(targets, "Deployment", "video-cloud-shadowworker")
			if mqttFoundationEnv(mqttFoundationPrimary(target), "VIDEO_CLOUD_SHADOW_WORKER_INSTANCE_ID") != "approved-original-shadow-instance" || mqttFoundationMountedSecret(target, mqttFoundationPrimary(target), "/etc/video_cloud/platform-service") != "original-shadow-identity" {
				t.Fatal("original instance or identity replaced")
			}
			if err := validateEnvironmentPKITransition([]deploymentplan.Definition{{Object: target}}, []deploymentplan.Object{worker}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestSharedShadowSettingsRejectOverridesAndMissingDependencies(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			root := writeDeploymentFixture(t, environment, "lke")
			path := filepath.Join(root, "cloud_deploy/architectures/kubernetes/architecture.env")
			body := readTestFile(t, path)
			for _, key := range []string{"SHADOW_WORKER_REGISTRATION_ENABLED", "MQTT_FOUNDATION_REGISTRATION_ENABLED", "ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"} {
				body = strings.ReplaceAll(body, key+"=false", key+"=true")
			}
			writeTestFile(t, path, body)
			cfg, err := resolveDeploymentConfig(root, environment, "")
			if err != nil {
				t.Fatal(err)
			}
			aliases := deploymentLegacyLKEValues(cfg.Values, environment)
			if aliases["LKE_SHADOW_WORKER_REGISTRATION_ENABLED"] != "true" || aliases["LKE_SHADOW_HTTP_CORE_CUTOVER_ENABLED"] != "false" {
				t.Fatal("shared settings were not projected")
			}
			before := (environmentDeploymentSnapshot{}).Digest(cfg)
			cfg.Values["SHADOW_HTTP_CORE_CUTOVER_ENABLED"] = "true"
			if before == (environmentDeploymentSnapshot{}).Digest(cfg) {
				t.Fatal("Shadow configuration drift not hashed")
			}
			cfg.Values["MQTT_FOUNDATION_REGISTRATION_ENABLED"] = "false"
			if validateSharedShadowDeploymentConfiguration(cfg.Values) == nil {
				t.Fatal("missing foundation accepted")
			}
			writeTestFile(t, filepath.Join(root, "cloud_env", environment, "overrides/architecture.env"), "SHADOW_WORKER_REGISTRATION_ENABLED=false\n")
			if _, err := resolveDeploymentConfig(root, environment, ""); err == nil {
				t.Fatal("per-environment Shadow override accepted")
			}
		})
	}
}
