package main

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"
)

func mqttFoundationRenderFixture(t *testing.T, environment string) (map[string]string, []deploymentplan.Definition, environmentRelease) {
	t.Helper()
	env, b, release := completeEnvironmentRenderFixture(environment)
	env["MQTT_FOUNDATION_REGISTRATION_ENABLED"], env["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] = "true", "true"
	env["LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED"], env["LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] = "true", "true"
	b.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
	restore := installAllCredentialEnvironment(env)
	defer restore()
	definitions, err := renderEnvironmentResources(env, b, release)
	if err != nil {
		t.Fatal(err)
	}
	return env, definitions, release
}
func TestMQTTFoundationSharedTargetKeepsIdentityAndFormalImageAcrossEnvironments(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-foundation-render")
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, definitions, release := mqttFoundationRenderFixture(t, environment)
			object := mqttFoundationTarget(definitions, "Deployment", "video-cloud-mqttfoundation")
			container := mqttFoundationPrimary(object)
			if container["image"] != release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference || !reflect.DeepEqual(container["command"], []any{"/app/mqttfoundation"}) {
				t.Fatal("registrar does not use its formal CI binary/image")
			}
			if mqttFoundationEnv(container, "VIDEO_CLOUD_ENV") != environment {
				t.Fatal("registrar selected a different environment")
			}
			// Existing custom-approved instance and original Secret are references,
			// never replaced with generated credentials or a copied workload spec.
			settings := certIssuerObjectList(container["env"])
			for _, v := range settings {
				if deploymentplan.Map(v)["name"] == "VIDEO_CLOUD_MQTT_FOUNDATION_INSTANCE_ID" {
					deploymentplan.Map(v)["value"] = "approved-existing-instance"
				}
			}
			volume := managedNamedField(environmentPKIPod(object)["volumes"], "platform-service-identity")
			deploymentplan.Map(volume["secret"])["secretName"] = "original-foundation-identity"
			deploymentplan.Map(object["metadata"])["uid"] = "original-registrar"
			observed, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{object})
			if err != nil {
				t.Fatal(err)
			}
			if observed.Pods[0].Component != "managed-mqtt-foundation" {
				t.Fatal("registrar is not in the common catalog")
			}
			_, bindings, _ := completeEnvironmentRenderFixture(environment)
			bindings.Pods = append(bindings.Pods, observed.Pods...)
			bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			restore := installAllCredentialEnvironment(env)
			targets, renderErr := renderEnvironmentResources(env, bindings, release)
			restore()
			if renderErr != nil {
				t.Fatal(renderErr)
			}
			target := mqttFoundationTarget(targets, "Deployment", "video-cloud-mqttfoundation")
			if mqttFoundationMountedSecret(target, mqttFoundationPrimary(target), "/etc/video_cloud/platform-service") != "original-foundation-identity" || mqttFoundationEnv(mqttFoundationPrimary(target), "VIDEO_CLOUD_MQTT_FOUNDATION_INSTANCE_ID") != "approved-existing-instance" {
				t.Fatal("approved identity/instance changed")
			}
			if err := validateEnvironmentPKITransition([]deploymentplan.Definition{{Object: target}}, []deploymentplan.Object{object}); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{"video-cloud-shadowworker", "video-cloud-webrtcservice", "video-cloud-videostorage", "video-cloud-otaservice"} {
				if mqttFoundationTarget(definitions, "Deployment", name) != nil {
					t.Fatal("optional plugin enabled", name)
				}
			}
			// Configuration changes invalidate prior plans via the existing shared hash.
			cfg := deploymentConfig{Values: env}
			before := (environmentDeploymentSnapshot{}).Digest(cfg)
			env["ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT"] = "9445"
			if before == (environmentDeploymentSnapshot{}).Digest(cfg) {
				t.Fatal("registration port drift not hashed")
			}
		})
	}
}
func TestMQTTFoundationTargetRejectsMissingOrWrongDependencies(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-foundation-render")
	for _, scenario := range []string{"listener", "port", "identity", "network", "foreign-url"} {
		t.Run(scenario, func(t *testing.T) {
			env, definitions, _ := mqttFoundationRenderFixture(t, "dev")
			switch scenario {
			case "listener":
				for i, d := range definitions {
					if d.Object["kind"] == "Deployment" && deploymentplan.Map(d.Object["metadata"])["name"] == "account-manager" {
						definitions = append(definitions[:i], definitions[i+1:]...)
						break
					}
				}
			case "port":
				listener := mqttFoundationPrimary(mqttFoundationTarget(definitions, "Deployment", "account-manager"))
				managedNamedField(listener["ports"], "service-reg")["containerPort"] = float64(8443)
			case "identity":
				foundation := mqttFoundationTarget(definitions, "Deployment", "video-cloud-mqttfoundation")
				environmentPKIPod(foundation)["volumes"] = nil
			case "network":
				for i, d := range definitions {
					if d.Object["kind"] == "NetworkPolicy" && deploymentplan.Map(d.Object["metadata"])["name"] == "allow-platform-service-registration" {
						definitions = append(definitions[:i], definitions[i+1:]...)
						break
					}
				}
			case "foreign-url":
				c := mqttFoundationPrimary(mqttFoundationTarget(definitions, "Deployment", "video-cloud-mqttfoundation"))
				managedNamedField(c["env"], "VIDEO_CLOUD_MQTT_FOUNDATION_REGISTRATION_URL")["value"] = "https://other-environment.invalid:8443"
			}
			var blocker *deploymentplan.Blocker
			if err := validateMQTTFoundationTarget(env, definitions); !errors.As(err, &blocker) || blocker.ID != "deployment.mqtt-foundation.dependencies" {
				t.Fatal("dependency did not fail with a safe specific blocker", err)
			}
		})
	}
}
func TestMQTTFoundationQualificationValidatesRetainedMutualTLSAndCRL(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-foundation-render")
	env, definitions, _ := mqttFoundationRenderFixture(t, "dev")
	fixture := newLKEPlatformCertificateFixture(t, env)
	for _, scenario := range []string{"valid", "missing", "wrong-service", "expired", "provider-error"} {
		t.Run(scenario, func(t *testing.T) {
			read := func(_ context.Context, id string) (deploymentplan.Object, error) {
				if scenario == "provider-error" {
					return nil, errors.New("private-provider-output-canary")
				}
				if strings.HasSuffix(id, "/"+mqttFoundationIdentitySecretName) {
					if scenario == "missing" {
						return nil, nil
					}
					subject := "service:mqtt"
					if scenario == "wrong-service" {
						subject = "service:shadow"
					}
					return fixture.identities[subject], nil
				}
				if strings.HasSuffix(id, "/"+serviceRegistrationTLSSecretName) {
					return fixture.listener, nil
				}
				t.Fatal("foreign Secret requested", id)
				return nil, nil
			}
			at := fixture.now
			if scenario == "expired" {
				at = at.Add(48 * time.Hour)
			}
			err := checkMQTTFoundationIdentity(context.Background(), env, definitions, at, read)
			if (err == nil) != (scenario == "valid") {
				t.Fatal("unexpected qualification", err)
			}
			if err != nil && strings.Contains(err.Error(), "private-provider-output-canary") {
				t.Fatal("private provider output leaked")
			}
		})
	}
}
func TestMQTTFoundationConfigurationIsSharedAndDependenciesExplicit(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			workspace := writeDeploymentFixture(t, environment, "lke")
			cfg, err := resolveDeploymentConfig(workspace, environment, "")
			if err != nil {
				t.Fatal(err)
			}
			cfg.Values["MQTT_FOUNDATION_REGISTRATION_ENABLED"], cfg.Values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] = "true", "true"
			legacy := deploymentLegacyLKEValues(cfg.Values, environment)
			if legacy["LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED"] != "true" || legacy["LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] != "true" {
				t.Fatal("shared flags not projected")
			}
			if err := validateMQTTFoundationConfiguration(cfg.Values); err != nil {
				t.Fatal(err)
			}
			cfg.Values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] = "false"
			if validateMQTTFoundationConfiguration(cfg.Values) == nil {
				t.Fatal("registrar without listener accepted")
			}
			writeTestFile(t, workspace+"/cloud_env/"+environment+"/overrides/architecture.env", "MQTT_FOUNDATION_REGISTRATION_ENABLED=true\n")
			if _, err := resolveDeploymentConfig(workspace, environment, ""); err == nil {
				t.Fatal("per-environment registration override accepted")
			}
		})
	}
}
