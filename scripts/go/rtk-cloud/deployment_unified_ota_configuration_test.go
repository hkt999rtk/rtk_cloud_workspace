package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestSharedOTATopologyEnablesRendererAndKeepsExistingIdentityForEveryEnvironment(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-ota-configuration")
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, b, release := completeEnvironmentRenderFixture(environment)
			for _, key := range sharedOTADeploymentKeys {
				env[key], env["LKE_"+key] = "true", "true"
			}
			env["VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED"] = "true"
			b.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			restore := installAllCredentialEnvironment(env)
			defer restore()
			definitions, err := renderEnvironmentResources(env, b, release)
			if err != nil {
				t.Fatal(err)
			}
			ota := mqttFoundationTarget(definitions, "Deployment", otaServiceWorkloadName)
			container := mqttFoundationPrimary(ota)
			if ota == nil || container["image"] != release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference {
				t.Fatal("independent OTA missing or not CI pinned")
			}
			core := mqttFoundationPrimary(mqttFoundationTarget(definitions, "Deployment", "video-cloud-api"))
			if mqttFoundationEnv(core, "VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED") != "true" || mqttFoundationEnv(core, "VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED") != "true" {
				t.Fatal("core OTA forwarding/grants disabled")
			}
			if mqttFoundationTarget(definitions, "Deployment", "video-cloud-otaregistrar") != nil {
				t.Fatal("legacy architecture restored")
			}
			for _, v := range certIssuerObjectList(container["env"]) {
				if deploymentplan.Map(v)["name"] == "VIDEO_CLOUD_OTA_SERVICE_INSTANCE_ID" {
					deploymentplan.Map(v)["value"] = "approved-original-ota-instance"
				}
			}
			deploymentplan.Map(managedNamedField(environmentPKIPod(ota)["volumes"], "platform-service-identity")["secret"])["secretName"] = "original-ota-identity"
			deploymentplan.Map(ota["metadata"])["uid"] = "original-ota-owner"
			observed, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{ota})
			if err != nil {
				t.Fatal(err)
			}
			b.Pods = append(b.Pods, observed.Pods...)
			repeated, err := renderEnvironmentResources(env, b, release)
			if err != nil {
				t.Fatal(err)
			}
			target := mqttFoundationTarget(repeated, "Deployment", otaServiceWorkloadName)
			if mqttFoundationEnv(mqttFoundationPrimary(target), "VIDEO_CLOUD_OTA_SERVICE_INSTANCE_ID") != "approved-original-ota-instance" || mqttFoundationMountedSecret(target, mqttFoundationPrimary(target), "/etc/video_cloud/platform-service") != "original-ota-identity" {
				t.Fatal("original OTA registration identity replaced")
			}
			if err := validateEnvironmentPKITransition([]deploymentplan.Definition{{Object: target}}, []deploymentplan.Object{ota}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
func TestSharedOTATopologyResolvesFromArchitectureAndRejectsOverrides(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			root := writeDeploymentFixture(t, environment, "lke")
			architecture := filepath.Join(root, "cloud_deploy/architectures/kubernetes/architecture.env")
			body := readTestFile(t, architecture)
			for _, key := range sharedOTADeploymentKeys {
				body = strings.ReplaceAll(body, key+"=false", key+"=true")
			}
			body += "VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED=true\n"
			writeTestFile(t, architecture, body)
			cfg, err := resolveDeploymentConfig(root, environment, "")
			if err != nil {
				t.Fatal(err)
			}
			aliases := deploymentLegacyLKEValues(cfg.Values, environment)
			for _, key := range sharedOTADeploymentKeys {
				if aliases["LKE_"+key] != "true" {
					t.Fatal("shared OTA setting not projected", key)
				}
			}
			hash := (environmentDeploymentSnapshot{}).Digest(cfg)
			cfg.Values["OTA_SERVICE_EDGE_ENABLED"] = "false"
			if hash == (environmentDeploymentSnapshot{}).Digest(cfg) {
				t.Fatal("OTA setting not in plan digest")
			}
			writeTestFile(t, filepath.Join(root, "cloud_env", environment, "overrides/architecture.env"), "OTA_SERVICE_EDGE_ENABLED=false\n")
			if _, err := resolveDeploymentConfig(root, environment, ""); err == nil {
				t.Fatal("environment-specific OTA topology accepted")
			}
		})
	}
}
func TestUnifiedOutputHasNoPrecheckAndKeepsProgress(t *testing.T) {
	_, deps, _ := unifiedCLIProfile(t, "dev")
	var output bytes.Buffer
	err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "preflight", []string{"--environment", "dev"}, &output, &output, deps)
	if err != nil || strings.Contains(output.String(), "PRECHECK") || !strings.Contains(output.String(), "[RUNNING] deployment.compile") || !strings.Contains(output.String(), "GO environment=dev") {
		t.Fatal("preflight progress/results were not restored", err, output.String())
	}
}
