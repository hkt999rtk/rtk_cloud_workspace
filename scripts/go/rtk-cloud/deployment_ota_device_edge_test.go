package main

import (
	"encoding/json"
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
		{"core route missing", func(ingress map[string]any) {
			ingress["spec"].(map[string]any)["rules"].([]any)[0].(map[string]any)["http"].(map[string]any)["paths"] = []any{}
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
