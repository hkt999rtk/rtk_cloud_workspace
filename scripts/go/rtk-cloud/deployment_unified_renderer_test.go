package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func completeEnvironmentRenderFixture(environment string) (map[string]string, environmentIdentityBindings, environmentRelease) {
	stack := "video-cloud-" + environment
	env := map[string]string{
		"VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED": "false", "VIDEO_STORAGE_CORE_CUTOVER_ENABLED": "false",
		"WEBRTC_SERVICE_REGISTRATION_ENABLED": "false", "WEBRTC_SERVICE_EDGE_ENABLED": "false", "WEBRTC_CORE_CUTOVER_ENABLED": "false", "CLOUD_STACK_NAME": stack, "CLOUD_ENV_NAME": environment, "OPENBAO_EFFECTIVE_REPLICAS": "3", "OPENBAO_REQUEST_CPU": "500m", "OPENBAO_REQUEST_MEMORY": "512Mi", "OPENBAO_NODE_CLASS": "general", "CLOUD_DNS_ROOT_DOMAIN": "example.invalid", "TEST_LAB_ENABLED": "true", "FRONTEND_DOMAIN": "www." + environment + ".example.invalid", "CONSOLE_DOMAIN": "console." + environment + ".example.invalid"}
	for _, role := range environmentAuthRoles {
		for _, key := range role {
			env[key] = "true"
		}
	}
	env["DEVICE_API_AUTH_APP_PKI_ENABLED"], env["APP_API_AUTH_PRODUCT_PKI_ENABLED"] = "false", "false"
	env["MQTT_FOUNDATION_REGISTRATION_ENABLED"], env["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"], env["ACCOUNT_MANAGER_SERVICE_REGISTRATION_PORT"] = "false", "false", "9444"
	env["LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED"], env["LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] = "false", "false"
	for _, key := range sharedOTADeploymentKeys {
		env[key], env["LKE_"+key] = "false", "false"
	}
	for _, key := range sharedShadowDeploymentKeys {
		env[key], env["LKE_"+key] = "false", "false"
	}
	env["OPENBAO_STORAGE_BACKEND"], env["OPENBAO_DATA_STORAGE"], env["OPENBAO_AUDIT_STORAGE"], env["OPENBAO_POD_MANAGEMENT_POLICY"], env["OPENBAO_UPDATE_STRATEGY"] = "raft", "20Gi", "10Gi", "Parallel", "RollingUpdate"
	env["PKI_APP_CRL_WORKER_ENABLED"], env["PKI_SERVER_CRL_DOMAIN"], env["PKI_REQUIRE_USER_MFA"] = "true", "service,mqtt,openbao_tls", "true"
	env["REDIS_DATA_STORAGE"], env["PROMETHEUS_DATA_STORAGE"] = "5Gi", "20Gi"
	env["DURABLE_DATA_STORAGE_CLASS"] = "linode-block-storage-retain"
	for _, domain := range managedPKIDomains {
		env["PKI_REQUIRED_CONSUMERS_"+strings.ToUpper(domain)] = "fixture-consumer"
	}
	release := environmentRelease{Schema: "rtk-cloud.release/v1", Environment: environment, Stack: stack, WorkspaceRevision: strings.Repeat("a", 40), Images: map[string]environmentReleaseImage{}}
	for _, source := range lkeServiceImageSources() {
		image := environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/" + source.RepoName + "/" + source.Name + "@sha256:" + strings.Repeat("a", 64), SourceRevision: strings.Repeat("b", 40)}
		if source.EnvKey == "LKE_VIDEO_CLOUD_IMAGE" {
			image.OTARegistration = otaRegistrationManifestFixture()
		}
		release.Images[source.EnvKey] = image
		env[source.EnvKey] = image.Reference
	}
	for key, repository := range map[string]string{"POSTGRES": "postgres", "REDIS": "redis", "FLEET_VALKEY": "valkey/valkey", "REDIS_EXPORTER": "oliver006/redis_exporter", "MQTT": "emqx/emqx", "PROMETHEUS": "prom/prometheus", "LOKI": "grafana/loki", "GRAFANA": "grafana/grafana", "LOG_COLLECTOR": "fluent/fluent-bit", "OPENBAO": "quay.io/openbao/openbao", "ALPINE": "alpine", "INGRESS": "registry.k8s.io/ingress-nginx/controller"} {
		key = "LKE_" + key + "_IMAGE"
		image := environmentReleaseImage{Reference: repository + "@sha256:" + strings.Repeat("c", 64)}
		release.Images[key] = image
		env[key] = image.Reference
	}
	release.Images["LKE_MQTT_PKI_IMAGE"] = environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-emqx-pki@sha256:" + strings.Repeat("a", 64), SourceRevision: strings.Repeat("b", 40)}
	env["LKE_MQTT_PKI_IMAGE"] = release.Images["LKE_MQTT_PKI_IMAGE"].Reference
	release.Images["LKE_OPENBAO_PKI_IMAGE"] = environmentReleaseImage{Reference: "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-openbao-pki@sha256:" + strings.Repeat("a", 64), SourceRevision: strings.Repeat("b", 40)}
	env["LKE_OPENBAO_PKI_IMAGE"] = release.Images["LKE_OPENBAO_PKI_IMAGE"].Reference
	bindings := environmentIdentityBindings{Version: 1, Environment: environment, Stack: stack, Owners: map[string]string{}}
	for name, component := range map[string]string{"certissuer": "certissuer", "factoryenroll": "certissuer", "pki-controller": "pki-controller", "video-cloud-api-pki": "managed-api", "mqtt-pki": "managed-mqtt", "openbao": "managed-openbao"} {
		kind, suffix, imageKey := "Deployment", "video-cloud", "LKE_VIDEO_CLOUD_IMAGE"
		if name == "mqtt-pki" {
			imageKey = "LKE_MQTT_PKI_IMAGE"
		}
		if name == "openbao" {
			kind, suffix, imageKey = "StatefulSet", "secrets", "LKE_OPENBAO_IMAGE"
		}
		containerName := name
		if name == "video-cloud-api-pki" {
			containerName = "app"
		}
		if name == "mqtt-pki" {
			containerName = "mqtt"
		}
		bindings.Pods = append(bindings.Pods, environmentPodBindings{Kind: kind, NamespaceSuffix: suffix, Name: name, Component: component, Containers: []environmentContainerBindings{{Name: containerName, ImageKey: imageKey, Fields: map[string]any{"env": []any{map[string]any{"name": "PKI_ENVIRONMENT", "value": environment}}}}}})
	}
	// The generic unowned Controller uses the maintained complete static TLS
	// profile. Native identity tests replace its binding with the actual accepted
	// Service recipe and owner; partial native state cannot augment static TLS.
	for i := range bindings.Pods {
		if bindings.Pods[i].Component == "managed-api" {
			b := &bindings.Pods[i]
			b.PodFields = map[string]any{"volumes": []any{map[string]any{"name": "mqtt-callback", "configMap": map[string]any{"name": "pki-mqtt-callback-ca"}}}}
			b.Containers[0].Fields["volumeMounts"] = []any{map[string]any{"name": "mqtt-callback", "mountPath": "/run/mqtt-callback", "readOnly": true}}
		}
	}
	for i := range bindings.Pods {
		if bindings.Pods[i].Component == "managed-mqtt" {
			bindings.Pods[i].Containers = append(bindings.Pods[i].Containers, environmentContainerBindings{Name: "pkibroker", ImageKey: "LKE_VIDEO_CLOUD_IMAGE", Fields: map[string]any{}})
		}
	}
	// Stable catalog order is intentional; a Go map iteration cannot change
	// the resources or plan digest selected for an environment.
	attachEnvironmentIssuerFixture(&bindings)
	sort.Slice(bindings.Pods, func(i, j int) bool { return bindings.Pods[i].Name < bindings.Pods[j].Name })
	return env, bindings, release
}

func TestCommonEnvironmentRendererHasCompletePinnedInventoryWithoutPrivatePlanSelection(t *testing.T) {
	t.Setenv("LKE_RUNTIME_SECRET_SEED", "isolated-render-fixture")
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, bindings, release := completeEnvironmentRenderFixture(environment)
			bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			restore := installAllCredentialEnvironment(env)
			defer restore()
			definitions, err := renderEnvironmentResources(env, bindings, release)
			if err != nil {
				t.Fatal(err)
			}
			ids, kinds := map[string]bool{}, map[string]bool{}
			for _, definition := range definitions {
				ids[deploymentplan.ID(definition.Object)] = true
				kinds[deploymentplan.String(definition.Object["kind"])] = true
				spec := deploymentplan.Map(definition.Object["spec"])
				pod := deploymentplan.Map(deploymentplan.Map(spec["template"])["spec"])
				for _, field := range []string{"containers", "initContainers"} {
					for _, item := range certIssuerObjectList(pod[field]) {
						if !immutableEnvironmentImage.MatchString(deploymentplan.String(deploymentplan.Map(item)["image"])) {
							t.Fatal("unpinned container", deploymentplan.ID(definition.Object))
						}
					}
				}
			}
			for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "Service", "ConfigMap", "NetworkPolicy", "Ingress", "IngressClass", "ValidatingWebhookConfiguration"} {
				if !kinds[kind] {
					t.Fatal("missing resource family", kind)
				}
			}
			if !ids["Deployment/"+bindings.Stack+"-ingress/ingress-nginx-controller"] {
				t.Fatal("ingress controller omitted from the common plan")
			}
			for _, binding := range bindings.Pods {
				if !ids[binding.Kind+"/"+bindings.Stack+"-"+binding.NamespaceSuffix+"/"+binding.Name] {
					t.Fatal("managed consumer missing", binding.Name)
				}
			}
			privateRoot := t.TempDir()
			t.Setenv("RTK_CLOUD_CONFIG_ROOT", privateRoot)
			path := filepath.Join(privateRoot, environment, "deployment")
			if err := os.MkdirAll(path, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(path, "managed-upgrade.json"), []byte("retired invalid schema-1 plan"), 0600); err != nil {
				t.Fatal(err)
			}
			withOldFile, err := renderEnvironmentResources(env, bindings, release)
			if err != nil || deploymentplan.Digest(definitions) != deploymentplan.Digest(withOldFile) {
				t.Fatal("private file selected a different renderer", err)
			}
		})
	}
}

func TestCommonOfficialImageBaselinePinsAllTargetContainersAcrossEnvironments(t *testing.T) {
	baseline, err := readStrictEnv(filepath.Join(mustWorkspaceRoot(t), "cloud_deploy", "architectures", "kubernetes", "architecture.env"))
	if err != nil {
		t.Fatal(err)
	}
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, bindings, release := completeEnvironmentRenderFixture(environment)
			bindings.IngressAdmission = &environmentAdmissionBinding{SecretName: "ingress-nginx-admission", CABundle: base64.StdEncoding.EncodeToString(testOpenBaoCAPEM(t))}
			for _, role := range environmentProviderImageKeys {
				env[role] = baseline[environmentProviderImageConfigKey(role)]
				release.Images[role] = environmentReleaseImage{Reference: env[role]}
			}
			restore := installAllCredentialEnvironment(env)
			defer restore()
			resources, err := renderEnvironmentResources(env, bindings, release)
			if err != nil {
				t.Fatal("common official images cannot render the target", err)
			}
			selected := map[string]bool{}
			for _, image := range release.Images {
				selected[image.Reference] = true
			}
			for _, resource := range resources {
				spec := deploymentplan.Map(resource.Object["spec"])
				if resource.Object["kind"] == "CronJob" {
					spec = deploymentplan.Map(deploymentplan.Map(spec["jobTemplate"])["spec"])
				}
				pod := deploymentplan.Map(deploymentplan.Map(spec["template"])["spec"])
				for _, field := range []string{"containers", "initContainers"} {
					for _, value := range certIssuerObjectList(pod[field]) {
						if !selected[deploymentplan.String(deploymentplan.Map(value)["image"])] {
							t.Fatal("target container bypassed the selected common/CI image", deploymentplan.ID(resource.Object))
						}
					}
				}
			}
		})
	}
}

func TestImageRepositoryNormalizationPreservesRegistryPort(t *testing.T) {
	for input, want := range map[string]string{"postgres:16-alpine": "library/postgres", "docker.io/library/postgres@sha256:abc": "library/postgres", "localhost:5000/project/provider:release": "localhost:5000/project/provider", "ghcr.io/org/api@sha256:abc": "ghcr.io/org/api"} {
		if got := environmentImageRepository(input); got != want {
			t.Fatalf("%s normalized to %s", input, got)
		}
	}
}
