package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func otaEdgeTestEnv() map[string]string {
	return map[string]string{
		"CLOUD_STACK_NAME":   "video-cloud-dev",
		"VIDEO_CLOUD_DOMAIN": "video-cloud-dev.example.test",
	}
}

func otaEdgeIngressFixture(env map[string]string) map[string]any {
	return map[string]any{
		"metadata": map[string]any{
			"resourceVersion": "1234",
			"annotations": map[string]any{
				"nginx.ingress.kubernetes.io/auth-tls-verify-client": "on",
				"nginx.ingress.kubernetes.io/auth-tls-verify-depth":  "2",
				"nginx.ingress.kubernetes.io/auth-tls-secret":        lkeIngressNamespace(env) + "/" + lkeDeviceMTLSAppCASecretName(env),
				"nginx.ingress.kubernetes.io/configuration-snippet":  "proxy_set_header X-Client-Verify $ssl_client_verify;\nproxy_set_header X-Client-Cert $ssl_client_escaped_cert;\n",
			},
		},
		"spec": map[string]any{
			"ingressClassName": "nginx",
			"rules": []any{map[string]any{
				"host": otaDeviceHost(env),
				"http": map[string]any{"paths": []any{map[string]any{
					"path": "/", "pathType": "Prefix",
					"backend": map[string]any{"service": map[string]any{
						"name": lkePublicHTTPSBridgeServiceName(env, lkePublicHTTPSRoute{
							Namespace: lkeNamespaceName(env, "video-cloud"), Service: "video-cloud-api",
						}),
						"port": map[string]any{"number": float64(80)},
					}},
				}}},
			}},
		},
	}
}

func otaEdgePaths(ingress map[string]any) []any {
	spec := ingress["spec"].(map[string]any)
	rule := spec["rules"].([]any)[0].(map[string]any)
	return rule["http"].(map[string]any)["paths"].([]any)
}

func TestOTADeviceEdgePatchPreservesDeviceMTLSAndCoreRoute(t *testing.T) {
	t.Setenv("LKE_DEVICE_DOMAIN", "")
	env := otaEdgeTestEnv()
	ingress := otaEdgeIngressFixture(env)
	patch, already, err := otaDeviceEdgePatch(env, ingress)
	if err != nil || already {
		t.Fatalf("safe ingress patch = %q, %t, %v", patch, already, err)
	}
	var operations []map[string]any
	if err := json.Unmarshal([]byte(patch), &operations); err != nil {
		t.Fatal(err)
	}
	if len(operations) != 3 || operations[0]["op"] != "test" ||
		operations[0]["value"] != "1234" || operations[1]["value"] != otaDeviceHost(env) ||
		operations[2]["op"] != "add" || operations[2]["path"] != "/spec/rules/0/http/paths/-" {
		t.Fatalf("OTA patch replaced existing ingress state: %+v", operations)
	}
	added := operations[2]["value"].(map[string]any)
	service := added["backend"].(map[string]any)["service"].(map[string]any)
	if added["path"] != "/v1/device/ota/" || added["pathType"] != "Prefix" ||
		service["name"] != "public-video-cloud-otaservice-video-cloud" ||
		service["port"].(map[string]any)["number"] != float64(18084) {
		t.Fatalf("OTA device route = %+v", added)
	}
	policy := lkeOTADeviceEdgeNetworkPolicyManifest(env)
	for _, required := range []string{
		"name: allow-public-otaservice", "namespace: video-cloud-dev-video-cloud",
		"app.kubernetes.io/name: video-cloud-otaservice",
		"kubernetes.io/metadata.name: video-cloud-dev-ingress", "port: 18084",
	} {
		if !strings.Contains(policy, required) {
			t.Fatalf("OTA ingress policy lacks %q", required)
		}
	}
	paths := otaEdgePaths(ingress)
	ingress["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["http"].(map[string]any)["paths"] =
		append(paths, added)
	patch, already, err = otaDeviceEdgePatch(env, ingress)
	if err != nil || !already || patch != "" {
		t.Fatalf("idempotent OTA route = %q, %t, %v", patch, already, err)
	}
}

func TestOTADeviceEdgePatchRejectsUnsafeIngress(t *testing.T) {
	t.Setenv("LKE_DEVICE_DOMAIN", "")
	env := otaEdgeTestEnv()
	for _, scenario := range []struct {
		name   string
		change func(map[string]any)
	}{
		{"missing metadata", func(ingress map[string]any) {
			delete(ingress, "metadata")
		}},
		{"missing resource version", func(ingress map[string]any) {
			delete(ingress["metadata"].(map[string]any), "resourceVersion")
		}},
		{"mTLS disabled", func(ingress map[string]any) {
			ingress["metadata"].(map[string]any)["annotations"].(map[string]any)["nginx.ingress.kubernetes.io/auth-tls-verify-client"] = "off"
		}},
		{"wrong app CA", func(ingress map[string]any) {
			ingress["metadata"].(map[string]any)["annotations"].(map[string]any)["nginx.ingress.kubernetes.io/auth-tls-secret"] = "wrong/app-ca"
		}},
		{"wrong certificate depth", func(ingress map[string]any) {
			ingress["metadata"].(map[string]any)["annotations"].(map[string]any)["nginx.ingress.kubernetes.io/auth-tls-verify-depth"] = "0"
		}},
		{"client certificate not forwarded", func(ingress map[string]any) {
			ingress["metadata"].(map[string]any)["annotations"].(map[string]any)["nginx.ingress.kubernetes.io/configuration-snippet"] = ""
		}},
		{"wrong device host", func(ingress map[string]any) {
			ingress["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["host"] = "other.example.test"
		}},
		{"wrong ingress class", func(ingress map[string]any) {
			ingress["spec"].(map[string]any)["ingressClassName"] = "other"
		}},
		{"core route missing", func(ingress map[string]any) {
			ingress["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["http"].(map[string]any)["paths"] = []any{}
		}},
		{"core route redirected", func(ingress map[string]any) {
			backend := otaEdgePaths(ingress)[0].(map[string]any)["backend"].(map[string]any)
			backend["service"].(map[string]any)["name"] = "unrelated-service"
		}},
		{"OTA path owned elsewhere", func(ingress map[string]any) {
			paths := otaEdgePaths(ingress)
			ingress["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["http"].(map[string]any)["paths"] =
				append(paths, map[string]any{
					"path": "/v1/device/ota/", "pathType": "Prefix",
					"backend": map[string]any{"service": map[string]any{
						"name": "other-service", "port": map[string]any{"number": float64(18084)},
					}},
				})
		}},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ingress := otaEdgeIngressFixture(env)
			scenario.change(ingress)
			if _, _, err := otaDeviceEdgePatch(env, ingress); err == nil {
				t.Fatal("unsafe device ingress was accepted")
			}
		})
	}
}

func TestOTADeviceEdgeCommandAppliesScopedResourcesAndFailsClosed(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	storeRoot := t.TempDir()
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", storeRoot)
	writeTestFile(t, filepath.Join(storeRoot, "dev/env/stack.env"), strings.Join([]string{
		"CLOUD_ENV_NAME=dev", "CLOUD_PROVIDER=lke", "CLOUD_STACK_NAME=video-cloud-dev",
		"CLOUD_DNS_ROOT_DOMAIN=example.test", "CLOUD_REGION=us-sea", "",
	}, "\n"))
	t.Setenv("LKE_OTA_SERVICE_REGISTRATION_ENABLED", "true")
	t.Setenv("LKE_OTA_SERVICE_EDGE_ENABLED", "true")
	t.Setenv("LKE_OTA_CORE_CUTOVER_ENABLED", "false")
	t.Setenv("LKE_OTA_REGISTRAR_REGISTRATION_ENABLED", "false")
	t.Setenv("VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED", "true")
	t.Setenv("LKE_DEVICE_DOMAIN", "")
	t.Setenv("FAKE_OTA_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"video-cloud-otaservice"},"ports":[{"port":18084,"targetPort":"http"}]}}`)
	t.Setenv("FAKE_OTA_ENDPOINTSLICES_JSON", `{"items":[{"ports":[{"port":18084}],"endpoints":[{"addresses":["10.0.0.1"],"conditions":{"ready":true}}]}]}`)
	logPath := fakeKubectl(t)
	config, err := resolveDeploymentConfig(workspace, "dev", "")
	if err != nil {
		t.Fatal(err)
	}
	root, err := loadLKEImageEnv(workspace, filepath.Join(storeRoot, "dev"))
	if err != nil {
		t.Fatal(err)
	}
	env := appendMap(root.Values, config.Values)
	env = appendMap(env, config.AdapterValues)
	env = appendMap(env, config.AdapterResolved)
	ingress := otaEdgeIngressFixture(env)
	patch, _, err := otaDeviceEdgePatch(env, ingress)
	if err != nil {
		t.Fatal(err)
	}
	var operations []map[string]any
	if err := json.Unmarshal([]byte(patch), &operations); err != nil {
		t.Fatal(err)
	}
	ingress["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["http"].(map[string]any)["paths"] =
		append(otaEdgePaths(ingress), operations[2]["value"])
	encoded, err := json.Marshal(ingress)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", string(encoded))
	credentials := func(environment string) (func(), error) {
		if environment != "dev" {
			return nil, fmt.Errorf("wrong environment %s", environment)
		}
		return func() {}, nil
	}
	args := []string{"--workspace", workspace, "--environment", "dev", "--confirm", "video-cloud-dev"}
	if err := runDeploymentOTADeviceEdgeWithCredentials(args, credentials); err != nil {
		t.Fatal(err)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"name: allow-public-otaservice", "name: public-video-cloud-otaservice-video-cloud", "port: 18084"} {
		if !strings.Contains(string(log), want) {
			t.Fatalf("OTA edge apply lacks %q", want)
		}
	}
	if strings.Contains(string(log), "kind: Deployment") || strings.Contains(string(log), "video-cloud-staging-public") {
		t.Fatal("OTA edge changed a workload or unrelated public ingress")
	}
	if got := strings.Count(string(log), "ARGS apply -f -"); got != 2 {
		t.Fatalf("scoped OTA edge apply count = %d, want 2", got)
	}

	// A Kubernetes patch which does not become visible must fail the postflight.
	paths := otaEdgePaths(ingress)
	ingress["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["http"].(map[string]any)["paths"] = paths[:len(paths)-1]
	encoded, err = json.Marshal(ingress)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", string(encoded))
	if err := runDeploymentOTADeviceEdgeWithCredentials(args, credentials); err == nil || !strings.Contains(err.Error(), "requires live device OTA ingress route") {
		t.Fatalf("unobserved OTA ingress patch accepted: %v", err)
	}
	log, err = os.ReadFile(logPath)
	if err != nil || !strings.Contains(string(log), "patch ingress video-cloud-staging-device-mtls --type=json") {
		t.Fatalf("OTA JSON patch was not attempted: %v", err)
	}

	for _, tc := range []struct{ key, value, want string }{
		{"LKE_OTA_SERVICE_REGISTRATION_ENABLED", "false", "both be true"},
		{"LKE_OTA_SERVICE_EDGE_ENABLED", "false", "both be true"},
		{"LKE_OTA_REGISTRAR_REGISTRATION_ENABLED", "true", "old registrar"},
		{"LKE_OTA_CORE_CUTOVER_ENABLED", "true", "core cutover"},
		{"VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED", "false", "strict Product"},
	} {
		t.Run(tc.key, func(t *testing.T) {
			old := os.Getenv(tc.key)
			t.Setenv(tc.key, tc.value)
			if err := runDeploymentOTADeviceEdgeWithCredentials(args, credentials); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unsafe OTA edge flags accepted: %v", err)
			}
			t.Setenv(tc.key, old)
		})
	}
	if err := runDeploymentOTADeviceEdgeWithCredentials([]string{"--workspace", workspace, "--environment", "dev"}, credentials); err != nil {
		t.Fatalf("read-only plan failed: %v", err)
	}
	if err := runDeploymentOTADeviceEdgeWithCredentials([]string{"--workspace", workspace, "--environment", "dev", "--confirm", "wrong"}, credentials); err == nil {
		t.Fatal("wrong stack confirmation accepted")
	}
	if err := runDeploymentOTADeviceEdgeWithCredentials(args, func(string) (func(), error) {
		return nil, fmt.Errorf("credential failure")
	}); err == nil || !strings.Contains(err.Error(), "credential failure") {
		t.Fatalf("credential failure did not block OTA edge: %v", err)
	}
	if err := runDeploymentOTADeviceEdgeWithCredentials([]string{"--workspace", workspace}, credentials); err == nil {
		t.Fatal("missing environment accepted")
	}
	if err := runDeploymentWithOperations([]string{"ota-device-edge", "--unknown"}, deploymentOperations{}); err == nil {
		t.Fatal("deployment dispatcher did not route OTA edge")
	}
	if err := runDeploymentOTADeviceEdge([]string{"--unknown"}); err == nil {
		t.Fatal("unknown OTA edge option was accepted")
	}
}
