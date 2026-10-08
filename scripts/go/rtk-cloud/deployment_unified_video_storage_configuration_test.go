package main

import (
	"encoding/base64"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestSharedVideoStorageWorkerKeepsCoreRoleAndOriginalIdentityAcrossEnvironments(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-video-storage-render")
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, b, release := completeEnvironmentRenderFixture(environment)
			env["VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED"], env["LKE_VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED"] = "true", "true"
			b.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			restore := installAllCredentialEnvironment(env)
			defer restore()
			definitions, err := renderEnvironmentResources(env, b, release)
			if err != nil {
				t.Fatal(err)
			}
			worker := mqttFoundationTarget(definitions, "Deployment", "video-cloud-videostorage")
			container := mqttFoundationPrimary(worker)
			if worker == nil || container["image"] != release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference || mqttFoundationEnv(container, "VIDEO_CLOUD_ENV") != environment {
				t.Fatal("VideoStorage worker missing, wrong environment or image")
			}
			core := mqttFoundationPrimary(mqttFoundationTarget(definitions, "Deployment", "video-cloud-api"))
			if mqttFoundationEnv(core, "VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_CUTOVER_ENABLED") != "false" {
				t.Fatal("core consumers or HTTP ownership changed")
			}
			for _, value := range certIssuerObjectList(container["env"]) {
				if deploymentplan.Map(value)["name"] == "VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_INSTANCE_ID" {
					deploymentplan.Map(value)["value"] = "approved-original-video-storage-instance"
				}
			}
			deploymentplan.Map(managedNamedField(environmentPKIPod(worker)["volumes"], "platform-service-identity")["secret"])["secretName"] = "original-video-storage-identity"
			deploymentplan.Map(worker["metadata"])["uid"] = "original-video-storage-owner"
			observed, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{worker})
			if err != nil {
				t.Fatal(err)
			}
			b.Pods = append(b.Pods, observed.Pods...)
			targets, err := renderEnvironmentResources(env, b, release)
			if err != nil {
				t.Fatal(err)
			}
			target := mqttFoundationTarget(targets, "Deployment", "video-cloud-videostorage")
			if mqttFoundationEnv(mqttFoundationPrimary(target), "VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_INSTANCE_ID") != "approved-original-video-storage-instance" || mqttFoundationMountedSecret(target, mqttFoundationPrimary(target), "/etc/video_cloud/platform-service") != "original-video-storage-identity" {
				t.Fatal("original instance or identity replaced")
			}
			if err := validateEnvironmentPKITransition([]deploymentplan.Definition{{Object: target}}, []deploymentplan.Object{worker}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestSharedVideoStorageSettingsRejectOverridesAndMissingDependencies(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			root := writeDeploymentFixture(t, environment, "lke")
			path := filepath.Join(root, "cloud_deploy/architectures/kubernetes/architecture.env")
			body := readTestFile(t, path)
			for _, key := range []string{"VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED", "MQTT_FOUNDATION_REGISTRATION_ENABLED", "ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"} {
				body = strings.ReplaceAll(body, key+"=false", key+"=true")
			}
			writeTestFile(t, path, body)
			cfg, err := resolveDeploymentConfig(root, environment, "")
			if err != nil {
				t.Fatal(err)
			}
			aliases := deploymentLegacyLKEValues(cfg.Values, environment)
			if aliases["LKE_VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED"] != "true" || aliases["LKE_VIDEO_STORAGE_CORE_CUTOVER_ENABLED"] != "false" {
				t.Fatal("shared settings were not projected")
			}
			before := (environmentDeploymentSnapshot{}).Digest(cfg)
			cfg.Values["VIDEO_STORAGE_CORE_CUTOVER_ENABLED"] = "true"
			if before == (environmentDeploymentSnapshot{}).Digest(cfg) {
				t.Fatal("VideoStorage configuration drift not hashed")
			}
			cfg.Values["MQTT_FOUNDATION_REGISTRATION_ENABLED"] = "false"
			if validateSharedVideoStorageDeploymentConfiguration(cfg.Values) == nil {
				t.Fatal("missing foundation accepted")
			}
			writeTestFile(t, filepath.Join(root, "cloud_env", environment, "overrides/architecture.env"), "VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED=false\n")
			if _, err := resolveDeploymentConfig(root, environment, ""); err == nil {
				t.Fatal("per-environment VideoStorage override accepted")
			}
		})
	}
}
