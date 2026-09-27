package main

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func otaTestSecretJSON(t *testing.T, values map[string]string) string {
	t.Helper()
	data := make(map[string]string, len(values))
	for key, value := range values {
		data[key] = base64.StdEncoding.EncodeToString([]byte(value))
	}
	encoded, err := json.Marshal(map[string]any{"data": data})
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

func TestLKEOTAServiceManifestIsPrivateAndOwnsBillingDelivery(t *testing.T) {
	env := map[string]string{
		"CLOUD_STACK_NAME":                           "video-cloud-dev",
		"CLOUD_ENV_NAME":                             "dev",
		"LKE_VIDEO_CLOUD_IMAGE":                      "example.test/video-cloud:reviewed",
		"VIDEO_CLOUD_BLOB_ENDPOINT":                  "https://objects.example.test",
		"VIDEO_CLOUD_BLOB_REGION":                    "us-east-1",
		"VIDEO_CLOUD_BLOB_BUCKET":                    "firmware",
		"VIDEO_CLOUD_OTA_CDN_BASE_URL":               "https://firmware.example.test",
		"VIDEO_CLOUD_OTA_CDN_TOKEN_NAME":             "__token__",
		"VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON": `{}`,
	}
	deployment := lkeOTAServiceDeploymentManifest(env)
	service := lkeOTAServiceServiceManifest(env)
	for label, manifest := range map[string]string{"deployment": deployment, "service": service} {
		var parsed map[string]any
		if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
			t.Fatalf("invalid %s manifest: %v", label, err)
		}
	}
	for _, want := range []string{
		"name: video-cloud-otaservice", "type: Recreate", "command: [\"/app/otaservice\"]",
		"name: VIDEO_CLOUD_DB_ENSURE_SCHEMA\n              value: \"false\"",
		"name: VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED\n              value: \"true\"",
		"name: VIDEO_CLOUD_OTA_SERVICE_ENABLED\n              value: \"true\"",
		"name: VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED\n              value: \"false\"",
		"name: VIDEO_CLOUD_BILLING_USAGE_ENDPOINT",
		"name: VIDEO_CLOUD_BILLING_USAGE_TOKEN", "name: VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX",
		"secretName: ota-service-platform-identity", "name: VIDEO_CLOUD_OTA_SERVICE_INSTANCE_ID",
		"value: \"ota-service-0\"",
	} {
		if !strings.Contains(deployment, want) {
			t.Fatalf("OTA service deployment lacks %q", want)
		}
	}
	if !strings.Contains(service, "type: ClusterIP") || strings.Contains(service, "LoadBalancer") {
		t.Fatal("OTA service must remain private behind the core API")
	}
}

func TestLKEOTAServiceUsesDedicatedBucketAndSecret(t *testing.T) {
	env := map[string]string{
		"CLOUD_STACK_NAME": "video-cloud-dev", "CLOUD_ENV_NAME": "dev",
		"VIDEO_CLOUD_OTA_STORAGE_MODE":  "dedicated",
		"VIDEO_CLOUD_BLOB_BUCKET":       "rtk-video-media-dev-us-sea",
		"VIDEO_CLOUD_OTA_BLOB_BUCKET":   "rtk-ota-firmware-dev-us-sea",
		"VIDEO_CLOUD_OTA_BLOB_REGION":   "us-sea",
		"VIDEO_CLOUD_OTA_BLOB_ENDPOINT": "https://us-sea-1.linodeobjects.com",
		"VIDEO_CLOUD_OTA_BLOB_PREFIX":   "environments/video-cloud-dev",
	}
	manifest := lkeOTAServiceDeploymentManifest(env)
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
		t.Fatalf("invalid dedicated OTA deployment: %v", err)
	}
	if !strings.Contains(manifest, `value: "rtk-ota-firmware-dev-us-sea"`) || !strings.Contains(manifest, "name: ota-object-storage") || strings.Contains(manifest, `value: "rtk-video-media-dev-us-sea"`) {
		t.Fatal("OTA deployment did not isolate its bucket and credentials")
	}
	if strings.Contains(manifest, "name: VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX") || strings.Contains(manifest, "name: ota-cdn-runtime") {
		t.Fatal("OTA deployment without a CDN URL must not require a CDN key Secret")
	}
}

func TestLKEOTAUploadIngressAllowsBINBeyond20MiBOnlyOnUploadPath(t *testing.T) {
	env := map[string]string{
		"CLOUD_STACK_NAME":          "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN":        "video-cloud-staging.example.test",
		"VIDEO_CLOUD_DEVICE_DOMAIN": "device.video-cloud-staging.example.test",
	}
	manifests := lkePublicHTTPSIngressManifests(env, lkePublicHTTPSBaseRoutes(env))
	uploadFound := false
	for _, manifest := range manifests {
		var ingress struct {
			Metadata struct {
				Name        string            `yaml:"name"`
				Annotations map[string]string `yaml:"annotations"`
			} `yaml:"metadata"`
			Spec struct {
				Rules []struct {
					Host string `yaml:"host"`
					HTTP struct {
						Paths []struct {
							Path     string `yaml:"path"`
							PathType string `yaml:"pathType"`
							Backend  struct {
								Service struct {
									Name string `yaml:"name"`
								} `yaml:"service"`
							} `yaml:"backend"`
						} `yaml:"paths"`
					} `yaml:"http"`
				} `yaml:"rules"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal([]byte(manifest), &ingress); err != nil {
			t.Fatalf("invalid ingress manifest: %v", err)
		}
		bodySize := ingress.Metadata.Annotations["nginx.ingress.kubernetes.io/proxy-body-size"]
		if ingress.Metadata.Name != "video-cloud-staging-ota-upload" {
			if bodySize != "20m" {
				t.Fatalf("%s changed the ordinary API upload cap to %q", ingress.Metadata.Name, bodySize)
			}
			continue
		}
		uploadFound = true
		if bodySize != "512m" || len(ingress.Spec.Rules) != 1 || ingress.Spec.Rules[0].Host != env["VIDEO_CLOUD_DOMAIN"] {
			t.Fatalf("OTA upload ingress is not limited to the public API host: %#v", ingress)
		}
		paths := ingress.Spec.Rules[0].HTTP.Paths
		if len(paths) != 1 || paths[0].Path != "/v1/device/ota/internal/upload/" || paths[0].PathType != "Prefix" || paths[0].Backend.Service.Name != "public-video-cloud-api-video-cloud" {
			t.Fatalf("OTA upload ingress must route only the upload token path to core: %#v", paths)
		}
	}
	if !uploadFound {
		t.Fatal("OTA upload path has no ingress for BIN files larger than 20 MiB")
	}
}

func TestLKEOTAServiceInputsRequirePrivateCDNAndSeparateLease(t *testing.T) {
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev", "VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED": "true"}
	if err := lkeRequireOTAServiceInputs(env); err == nil || !strings.Contains(err.Error(), "MQTT foundation") {
		t.Fatalf("missing MQTT dependency was accepted: %v", err)
	}
	env["LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED"] = "true"
	env["LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] = "true"
	env["VIDEO_CLOUD_BLOB_BUCKET"] = "firmware"
	env["VIDEO_CLOUD_BLOB_REGION"] = "us-east-1"
	env["VIDEO_CLOUD_OTA_CDN_BASE_URL"] = "http://firmware.example.test"
	if err := lkeRequireOTAServiceInputs(env); err == nil || !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("non-TLS CDN was accepted: %v", err)
	}
	env["LKE_OTA_REGISTRAR_REGISTRATION_ENABLED"] = "true"
	if err := lkeRequireOTAServiceInputs(env); err == nil || !strings.Contains(err.Error(), "same Platform lease") {
		t.Fatalf("dual OTA registrars were accepted: %v", err)
	}
}

func TestLKEOTACDNSecretRejectsShortToken(t *testing.T) {
	if err := validateOTACDNTokenKey([]byte("abc")); err == nil {
		t.Fatal("short OTA CDN key was accepted")
	}
	if err := validateOTACDNTokenKey([]byte(strings.Repeat("ab", 32))); err != nil {
		t.Fatalf("valid OTA CDN key was rejected: %v", err)
	}
}

func TestLKEOTAServicePreflightRequiresCDNRuntimeAndIdentity(t *testing.T) {
	fakeKubectl(t)
	t.Setenv("LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED", "true")
	t.Setenv("LKE_ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED", "true")
	env := map[string]string{
		"CLOUD_STACK_NAME": "video-cloud-staging", "VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED": "true",
		"VIDEO_CLOUD_BLOB_BUCKET": "firmware", "VIDEO_CLOUD_BLOB_REGION": "us-east-1",
		"VIDEO_CLOUD_OTA_CDN_BASE_URL": "https://firmware.example.test",
	}
	if err := lkeRequireOTAServiceInputs(env); err == nil || !strings.Contains(err.Error(), "CDN runtime Secret") {
		t.Fatalf("missing CDN runtime Secret was accepted: %v", err)
	}
	t.Setenv("FAKE_OTA_CDN_SECRET_JSON", otaTestSecretJSON(t, map[string]string{"VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX": "bad"}))
	if err := lkeRequireOTAServiceInputs(env); err == nil || !strings.Contains(err.Error(), "invalid token key") {
		t.Fatalf("invalid CDN key was accepted: %v", err)
	}
	t.Setenv("FAKE_OTA_CDN_SECRET_JSON", otaTestSecretJSON(t, map[string]string{"VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX": strings.Repeat("ab", 32)}))
	if err := lkeRequireOTAServiceInputs(env); err == nil || !strings.Contains(err.Error(), "runtime Secret") {
		t.Fatalf("missing runtime inputs were accepted: %v", err)
	}
	videoRuntime := map[string]string{
		"POSTGRES_PASSWORD": "test", "VIDEO_CLOUD_AUTH_SECRET": "test", "VIDEO_CLOUD_OTA_BFF_TOKEN": "test",
		"VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN": "test", "AWS_ACCESS_KEY_ID": "test", "AWS_SECRET_ACCESS_KEY": "test",
	}
	t.Setenv("FAKE_OTA_VIDEO_RUNTIME_SECRET_JSON", otaTestSecretJSON(t, videoRuntime))
	if err := lkeRequireOTAServiceInputs(env); err == nil || !strings.Contains(err.Error(), "video-cloud-workers-runtime") {
		t.Fatalf("missing Billing delivery token was accepted: %v", err)
	}
	t.Setenv("FAKE_OTA_WORKERS_RUNTIME_SECRET_JSON", otaTestSecretJSON(t, map[string]string{"VIDEO_CLOUD_BILLING_USAGE_TOKEN": "test"}))
	delete(videoRuntime, "AWS_SECRET_ACCESS_KEY")
	t.Setenv("FAKE_OTA_VIDEO_RUNTIME_SECRET_JSON", otaTestSecretJSON(t, videoRuntime))
	if err := lkeRequireOTAServiceInputs(env); err == nil || !strings.Contains(err.Error(), "AWS_SECRET_ACCESS_KEY") {
		t.Fatalf("missing object-storage key was accepted: %v", err)
	}
	videoRuntime["AWS_SECRET_ACCESS_KEY"] = "test"
	t.Setenv("FAKE_OTA_VIDEO_RUNTIME_SECRET_JSON", otaTestSecretJSON(t, videoRuntime))
	if err := lkeRequireOTAServiceInputs(env); err == nil || !strings.Contains(err.Error(), "identity Secret") {
		t.Fatalf("missing OTA service identity was accepted: %v", err)
	}
	setFakeLKEPlatformIdentitySecrets(t, env)
	if err := lkeRequireOTAServiceInputs(env); err == nil || !strings.Contains(err.Error(), "dedicated OTA bucket") {
		t.Fatalf("shared media bucket was accepted for billable OTA service: %v", err)
	}
	env["VIDEO_CLOUD_OTA_STORAGE_MODE"] = "dedicated"
	env["VIDEO_CLOUD_OTA_BLOB_BUCKET"] = "rtk-ota-firmware-staging-sg-sin-2"
	env["VIDEO_CLOUD_OTA_BLOB_REGION"] = "sg-sin-2"
	env["VIDEO_CLOUD_OTA_BLOB_ENDPOINT"] = "https://sg-sin-2.linodeobjects.com"
	env["VIDEO_CLOUD_OTA_BLOB_ENDPOINT_TYPE"] = "E3"
	delete(videoRuntime, "AWS_ACCESS_KEY_ID")
	delete(videoRuntime, "AWS_SECRET_ACCESS_KEY")
	t.Setenv("FAKE_OTA_VIDEO_RUNTIME_SECRET_JSON", otaTestSecretJSON(t, videoRuntime))
	if err := lkeRequireOTAServiceInputs(env); err == nil || !strings.Contains(err.Error(), "scoped OTA storage credentials") {
		t.Fatalf("missing dedicated OTA key was accepted: %v", err)
	}
	t.Setenv("LINODE_OTA_OBJ_ACCESS_KEY_ID", "dedicated-access")
	t.Setenv("LINODE_OTA_OBJ_SECRET_ACCESS_KEY", "dedicated-secret")
	if err := lkeRequireOTAServiceInputs(env); err != nil {
		t.Fatalf("dedicated OTA preflight required a Secret before it could be created: %v", err)
	}
	delete(env, "VIDEO_CLOUD_OTA_CDN_BASE_URL")
	t.Setenv("FAKE_OTA_CDN_SECRET_JSON", "")
	env["VIDEO_CLOUD_OTA_BLOB_ENDPOINT_TYPE"] = "E1"
	if err := lkeRequireOTAServiceInputs(env); err == nil || !strings.Contains(err.Error(), "validated E2/E3") {
		t.Fatalf("E1 OTA direct delivery was accepted: %v", err)
	}
	env["VIDEO_CLOUD_OTA_BLOB_ENDPOINT_TYPE"] = "E3"
	if err := lkeRequireOTAServiceInputs(env); err != nil {
		t.Fatalf("Object Storage delivery without CDN configuration was rejected: %v", err)
	}
	t.Setenv("FAKE_OTA_CDN_SECRET_JSON", otaTestSecretJSON(t, map[string]string{"VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX": strings.Repeat("ab", 32)}))
	if err := lkeRequireOTAServiceInputs(env); err == nil || !strings.Contains(err.Error(), "without a CDN base URL") {
		t.Fatalf("CDN key without URL was accepted: %v", err)
	}
}

func TestLKEOTAServiceCutoverRequiresReadyPrivateEndpoint(t *testing.T) {
	fakeKubectl(t)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	if err := lkeRequireReadyOTAServiceEndpoint(env); err == nil {
		t.Fatal("missing OTA Service was accepted")
	}
	t.Setenv("FAKE_OTA_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"video-cloud-otaservice"},"ports":[{"port":18084,"targetPort":"http"}]}}`)
	if err := lkeRequireReadyOTAServiceEndpoint(env); err == nil || !strings.Contains(err.Error(), "no ready") {
		t.Fatalf("OTA Service without a ready endpoint was accepted: %v", err)
	}
	t.Setenv("FAKE_OTA_ENDPOINTSLICES_JSON", `{"items":[{"ports":[{"port":18084}],"endpoints":[{"addresses":["10.0.0.5"],"conditions":{"ready":true}}]}]}`)
	if err := lkeRequireReadyOTAServiceEndpoint(env); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_OTA_SERVICE_JSON", `{"spec":{"type":"LoadBalancer","selector":{"app.kubernetes.io/name":"video-cloud-otaservice"},"ports":[{"port":18084,"targetPort":"http"}]}}`)
	if err := lkeRequireReadyOTAServiceEndpoint(env); err == nil {
		t.Fatal("public OTA Service was accepted")
	}
	t.Setenv("FAKE_OTA_SERVICE_JSON", `{"spec":{"type":"ClusterIP","externalIPs":["198.51.100.2"],"selector":{"app.kubernetes.io/name":"video-cloud-otaservice"},"ports":[{"port":18084,"targetPort":"http"}]}}`)
	if err := lkeRequireReadyOTAServiceEndpoint(env); err == nil {
		t.Fatal("externally addressed OTA Service was accepted")
	}
	t.Setenv("FAKE_OTA_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"video-cloud-api"},"ports":[{"port":18084,"targetPort":"http"}]}}`)
	if err := lkeRequireReadyOTAServiceEndpoint(env); err == nil {
		t.Fatal("OTA Service targeting core API was accepted")
	}
	t.Setenv("FAKE_OTA_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"video-cloud-otaservice"},"ports":[{"port":8080,"targetPort":"http"}]}}`)
	if err := lkeRequireReadyOTAServiceEndpoint(env); err == nil {
		t.Fatal("OTA Service with the wrong port was accepted")
	}
}

func TestLKEOTAServiceCutoverManifestAndPolicyAreOptIn(t *testing.T) {
	t.Setenv("LKE_OTA_CORE_CUTOVER_ENABLED", "false")
	t.Setenv("LKE_OTA_SERVICE_REGISTRATION_ENABLED", "false")
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "LKE_VIDEO_CLOUD_IMAGE": "example.test/video-cloud:reviewed"}
	workload := lkeWorkload{Key: "video-cloud", Name: "video-cloud-api", Namespace: lkeNamespaceName(env, "video-cloud"), Port: 8080, Image: env["LKE_VIDEO_CLOUD_IMAGE"]}
	baseline := lkeDeploymentManifest(env, workload, nil)
	if !strings.Contains(baseline, "name: VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED\n              value: \"false\"") || strings.Contains(baseline, "VIDEO_CLOUD_OTA_UPSTREAM_URL") {
		t.Fatal("OTA cutover was enabled in the default core Deployment")
	}
	for _, manifest := range lkePublicHTTPSNetworkPolicyManifests(env, nil) {
		if strings.Contains(manifest, "name: allow-video-cloud-api-otaservice") {
			t.Fatal("OTA private ingress was enabled without the service")
		}
	}
	t.Setenv("LKE_OTA_SERVICE_REGISTRATION_ENABLED", "true")
	found := false
	for _, manifest := range lkePublicHTTPSNetworkPolicyManifests(env, nil) {
		found = found || strings.Contains(manifest, "name: allow-video-cloud-api-otaservice")
	}
	if !found {
		t.Fatal("registered OTA service lacks private core-only ingress")
	}
	t.Setenv("LKE_OTA_CORE_CUTOVER_ENABLED", "true")
	cutover := lkeDeploymentManifest(env, workload, nil)
	if !strings.Contains(cutover, "name: VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED\n              value: \"true\"") || !strings.Contains(cutover, "http://video-cloud-otaservice.video-cloud-staging-video-cloud.svc.cluster.local:18084") {
		t.Fatal("OTA cutover Deployment lacks the private service URL")
	}
	policy := lkeAllowVideoCloudAPIOTAGatewayNetworkPolicyManifest(env)
	var parsed map[string]any
	if err := yaml.Unmarshal([]byte(policy), &parsed); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(policy, "app.kubernetes.io/name: video-cloud-otaservice") || !strings.Contains(policy, "app.kubernetes.io/name: video-cloud-api") || !strings.Contains(policy, "port: 18084") {
		t.Fatal("OTA gateway NetworkPolicy does not restrict the upstream to core")
	}
}

func TestLKEOTADeviceEdgeRequiresRegisteredServiceAndKeepsMTLS(t *testing.T) {
	fakeKubectl(t)
	t.Setenv("LKE_OTA_SERVICE_EDGE_ENABLED", "false")
	env := map[string]string{
		"CLOUD_STACK_NAME":          "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN":        "video.example.test",
		"VIDEO_CLOUD_DEVICE_DOMAIN": "device.video.example.test",
	}
	for _, route := range lkePublicHTTPSBaseRoutes(env) {
		if route.Path == "/v1/device/ota/" {
			t.Fatal("device OTA edge route is enabled by default")
		}
	}
	t.Setenv("LKE_OTA_SERVICE_EDGE_ENABLED", "true")
	routes := lkePublicHTTPSBaseRoutes(env)
	found := false
	legacyFound := false
	for _, route := range routes {
		if route.Path == "/v1/device/ota/" {
			found = route.Host == env["VIDEO_CLOUD_DEVICE_DOMAIN"] && route.Service == otaServiceWorkloadName && route.ServicePort == 18084 && !route.Exact
		}
		if route.Path == "/v1/device/ota/internal/artifact/" {
			legacyFound = route.Host == env["VIDEO_CLOUD_DEVICE_DOMAIN"] && route.Service == "video-cloud-api" && route.ServicePort == 80 && !route.Exact
		}
	}
	if !found || !legacyFound {
		t.Fatal("device OTA routes must send new requests to OTA and historical artifact URLs to core")
	}
	manifests := lkePublicHTTPSIngressManifests(env, routes)
	validIngressJSON := ""
	for _, manifest := range manifests {
		if strings.Contains(manifest, "name: video-cloud-staging-public\n") && strings.Contains(manifest, "path: /v1/device/ota/") {
			t.Fatal("device OTA route bypasses device-host mTLS")
		}
		if !strings.Contains(manifest, "name: video-cloud-staging-device-mtls\n") {
			continue
		}
		if !strings.Contains(manifest, "nginx.ingress.kubernetes.io/auth-tls-verify-client: \"on\"") || !strings.Contains(manifest, "path: /v1/device/ota/\n            pathType: Prefix") || !strings.Contains(manifest, "path: /v1/device/ota/internal/artifact/\n            pathType: Prefix") {
			t.Fatal("device OTA ingress lost mTLS or path routing")
		}
		var parsed map[string]any
		if err := yaml.Unmarshal([]byte(manifest), &parsed); err != nil {
			t.Fatal(err)
		}
		encoded, err := json.Marshal(parsed)
		if err != nil {
			t.Fatal(err)
		}
		validIngressJSON = string(encoded)
		t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", validIngressJSON)
	}
	if validIngressJSON == "" {
		t.Fatal("device OTA ingress was not rendered")
	}
	if err := lkeRequireActiveOTADeviceEdgeRoute(env); err != nil {
		t.Fatal(err)
	}
	var brokenLegacy map[string]any
	if err := json.Unmarshal([]byte(validIngressJSON), &brokenLegacy); err != nil {
		t.Fatal(err)
	}
	for _, rawRule := range brokenLegacy["spec"].(map[string]any)["rules"].([]any) {
		for _, rawPath := range rawRule.(map[string]any)["http"].(map[string]any)["paths"].([]any) {
			path := rawPath.(map[string]any)
			if path["path"] == "/v1/device/ota/internal/artifact/" {
				path["backend"].(map[string]any)["service"].(map[string]any)["name"] = "wrong-backend"
			}
		}
	}
	brokenJSON, err := json.Marshal(brokenLegacy)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", string(brokenJSON))
	if err := lkeRequireActiveOTADeviceEdgeRoute(env); err == nil || !strings.Contains(err.Error(), "legacy artifact") {
		t.Fatalf("cutover accepted a legacy artifact route to the wrong backend: %v", err)
	}
	wantBridge := lkePublicHTTPSBridgeServiceName(env, lkePublicHTTPSRoute{Namespace: lkeNamespaceName(env, "video-cloud"), Service: otaServiceWorkloadName})
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", strings.Replace(validIngressJSON, wantBridge, "public-video-cloud-api-video-cloud", 1))
	if err := lkeRequireActiveOTADeviceEdgeRoute(env); err == nil {
		t.Fatal("cutover accepted a device OTA route to the wrong backend")
	}
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", strings.Replace(validIngressJSON, `"nginx.ingress.kubernetes.io/auth-tls-verify-client":"on"`, `"nginx.ingress.kubernetes.io/auth-tls-verify-client":"off"`, 1))
	if err := lkeRequireActiveOTADeviceEdgeRoute(env); err == nil {
		t.Fatal("cutover accepted a device OTA route without mTLS")
	}
	policyFound := false
	for _, policy := range lkePublicHTTPSNetworkPolicyManifests(env, routes) {
		if strings.Contains(policy, "name: allow-public-ingress\n") && strings.Contains(policy, "namespace: video-cloud-staging-video-cloud") {
			policyFound = strings.Contains(policy, "port: 18084")
		}
	}
	if !policyFound {
		t.Fatal("OTA service lacks ingress-to-service NetworkPolicy port")
	}
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", `{"spec":{"rules":[]}}`)
	if err := lkeRequireActiveOTADeviceEdgeRoute(env); err == nil {
		t.Fatal("cutover accepted a missing live device OTA ingress route")
	}
}

func TestLKEOTADeviceEdgeRemovalKeepsCoreCutoverUntilRouteIsGone(t *testing.T) {
	fakeKubectl(t)
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", `{"spec":{"rules":[{"http":{"paths":[{"path":"/v1/device/ota/"}]}}]}}`)
	t.Setenv("LKE_OTA_CORE_CUTOVER_ENABLED", "true")
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	if err := lkePreventOTAEdgeRollbackOverlap(env); err == nil || !strings.Contains(err.Error(), "core API is absent") {
		t.Fatalf("edge removal accepted an absent core API: %v", err)
	}
	t.Setenv("FAKE_WEBRTC_CORE_DEPLOYMENT_JSON", `{"metadata":{"name":"video-cloud-api"},"spec":{"template":{"spec":{"containers":[{"name":"app","env":[{"name":"VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED","value":"false"}]}]}}}}`)
	if err := lkePreventOTAEdgeRollbackOverlap(env); err == nil || !strings.Contains(err.Error(), "observed core cutover") {
		t.Fatalf("edge removal accepted core ownership of old releases: %v", err)
	}
	t.Setenv("FAKE_WEBRTC_CORE_DEPLOYMENT_JSON", `{"metadata":{"name":"video-cloud-api"},"spec":{"template":{"spec":{"containers":[{"name":"app","env":[{"name":"VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED","value":"true"}]}]}}}}`)
	if err := lkePreventOTAEdgeRollbackOverlap(env); err != nil {
		t.Fatalf("edge removal blocked the safe temporary 503 phase: %v", err)
	}
	t.Setenv("LKE_OTA_CORE_CUTOVER_ENABLED", "false")
	if err := lkePreventOTAEdgeRollbackOverlap(env); err == nil || !strings.Contains(err.Error(), "before restoring core") {
		t.Fatalf("core restoration was allowed while device edge is live: %v", err)
	}
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", `{"spec":{"rules":[]}}`)
	if err := lkePreventOTAEdgeRollbackOverlap(env); err != nil {
		t.Fatalf("core restoration blocked after edge removal: %v", err)
	}
}

func TestLKEOTACoreFirstKeepsHistoricalArtifactRoute(t *testing.T) {
	fakeKubectl(t)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "VIDEO_CLOUD_DOMAIN": "video.example.test", "VIDEO_CLOUD_DEVICE_DOMAIN": "device.video.example.test"}
	t.Setenv("LKE_OTA_SERVICE_EDGE_ENABLED", "false")
	t.Setenv("LKE_OTA_CORE_CUTOVER_ENABLED", "true")
	manifest := ""
	for _, rendered := range lkePublicHTTPSIngressManifests(env, lkePublicHTTPSBaseRoutes(env)) {
		if strings.Contains(rendered, "name: video-cloud-staging-device-mtls\n") {
			manifest = rendered
		}
	}
	if manifest == "" || !strings.Contains(manifest, "auth-tls-verify-client: \"on\"") {
		t.Fatal("core-first device ingress lost mTLS")
	}
	var ingress map[string]any
	if err := yaml.Unmarshal([]byte(manifest), &ingress); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(ingress)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", string(encoded))
	if err := lkeRequireActiveOTACoreDeviceRoute(env); err != nil {
		t.Fatal(err)
	}
	badBackend := strings.Replace(string(encoded), "public-video-cloud-api-video-cloud", "wrong-backend", 1)
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", badBackend)
	if err := lkeRequireActiveOTACoreDeviceRoute(env); err == nil {
		t.Fatal("core-first cutover accepted an ingress that loses old artifact GETs")
	}
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", strings.Replace(string(encoded), `"nginx.ingress.kubernetes.io/auth-tls-verify-client":"on"`, `"nginx.ingress.kubernetes.io/auth-tls-verify-client":"off"`, 1))
	if err := lkeRequireActiveOTACoreDeviceRoute(env); err == nil {
		t.Fatal("core-first cutover accepted device ingress without mTLS")
	}
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", string(encoded))
	t.Setenv("LKE_OTA_SERVICE_EDGE_ENABLED", "true")
	edgeManifest := ""
	for _, rendered := range lkePublicHTTPSIngressManifests(env, lkePublicHTTPSBaseRoutes(env)) {
		if strings.Contains(rendered, "name: video-cloud-staging-device-mtls\n") {
			edgeManifest = rendered
		}
	}
	if err := yaml.Unmarshal([]byte(edgeManifest), &ingress); err != nil {
		t.Fatal(err)
	}
	encoded, err = json.Marshal(ingress)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", string(encoded))
	if err := lkeRequireActiveOTACoreDeviceRoute(env); err == nil {
		t.Fatal("core-first cutover accepted an already switched device edge")
	}
}

func TestLKEOTADeviceEdgeRejectsIncompleteLiveRoutes(t *testing.T) {
	fakeKubectl(t)
	env := map[string]string{
		"CLOUD_STACK_NAME":          "video-cloud-staging",
		"VIDEO_CLOUD_DOMAIN":        "video.example.test",
		"VIDEO_CLOUD_DEVICE_DOMAIN": "device.video.example.test",
	}
	for _, tc := range []struct {
		name string
		body string
	}{
		{"missing spec", `{}`},
		{"wrong host", `{"spec":{"rules":[{"host":"other.example.test","http":{"paths":[{"path":"/v1/device/ota/","pathType":"Prefix"}]}}]}}`},
		{"missing HTTP routes", `{"spec":{"rules":[{"host":"device.video.example.test"}]}}`},
		{"wrong path", `{"spec":{"rules":[{"host":"device.video.example.test","http":{"paths":[{"path":"/v1/device/shadow/","pathType":"Prefix"}]}}]}}`},
		{"wrong port", `{"spec":{"rules":[{"host":"device.video.example.test","http":{"paths":[{"path":"/v1/device/ota/","pathType":"Prefix","backend":{"service":{"name":"public-video-cloud-otaservice-video-cloud","port":{"number":18085}}}}]}}]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var ingress map[string]any
			if err := json.Unmarshal([]byte(tc.body), &ingress); err != nil {
				t.Fatal(err)
			}
			ingress["metadata"] = map[string]any{"annotations": map[string]any{"nginx.ingress.kubernetes.io/auth-tls-verify-client": "on"}}
			body, err := json.Marshal(ingress)
			if err != nil {
				t.Fatal(err)
			}
			t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", string(body))
			if err := lkeRequireActiveOTADeviceEdgeRoute(env); err == nil {
				t.Fatal("accepted an incomplete live OTA device route")
			}
		})
	}
}

func TestLKEOTADeviceEdgeRollbackRejectsUnknownCoreState(t *testing.T) {
	fakeKubectl(t)
	t.Setenv("LKE_OTA_CORE_CUTOVER_ENABLED", "true")
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", `{"spec":{"rules":[{"http":{"paths":[{"path":"/v1/device/ota/"}]}}]}}`)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"}
	for _, tc := range []struct {
		name string
		body string
		want string
	}{
		{"malformed deployment", `{`, "decode core OTA cutover"},
		{"wrong deployment", `{"metadata":{"name":"other-api"}}`, "unexpected Deployment"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("FAKE_WEBRTC_CORE_DEPLOYMENT_JSON", tc.body)
			err := lkePreventOTAEdgeRollbackOverlap(env)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("rollback accepted unknown core state: %v", err)
			}
		})
	}
	t.Setenv("FAKE_WEBRTC_CORE_DEPLOYMENT_JSON", "")
	if err := lkePreventOTAEdgeRollbackOverlap(env); err == nil || !strings.Contains(err.Error(), "core API is absent") {
		t.Fatalf("removed a live OTA edge route while core Deployment was absent: %v", err)
	}
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", `{`)
	if err := lkePreventOTAEdgeRollbackOverlap(env); err == nil || !strings.Contains(err.Error(), "decode device OTA ingress") {
		t.Fatalf("accepted an unreadable live ingress during rollback: %v", err)
	}
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", `{"spec":{"rules":[]}}`)
	if err := lkePreventOTAEdgeRollbackOverlap(env); err != nil {
		t.Fatalf("absent core Deployment should not block rollback: %v", err)
	}
}

func TestLKEOTADeviceEdgeApplyRequiresRegistrationAndReadyEndpoint(t *testing.T) {
	fakeKubectl(t)
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "VIDEO_CLOUD_DOMAIN": "video.example.test"}
	t.Setenv("LKE_OTA_SERVICE_EDGE_ENABLED", "true")
	if err := lkeApplyPublicHTTPS(provisionPaths{}, env, provisionOptions{}); err == nil || !strings.Contains(err.Error(), "core OTA cutover") {
		t.Fatalf("OTA edge accepted desired core ownership of releases: %v", err)
	}
	t.Setenv("LKE_OTA_CORE_CUTOVER_ENABLED", "true")
	if err := lkeApplyPublicHTTPS(provisionPaths{}, env, provisionOptions{}); err == nil || !strings.Contains(err.Error(), "registered OTA service") {
		t.Fatalf("OTA edge accepted an unregistered service: %v", err)
	}
	t.Setenv("LKE_OTA_SERVICE_REGISTRATION_ENABLED", "true")
	if err := lkeApplyPublicHTTPS(provisionPaths{}, env, provisionOptions{}); err == nil || !strings.Contains(err.Error(), "OTA Service is not private") {
		t.Fatalf("OTA edge accepted an absent service endpoint: %v", err)
	}
	t.Setenv("FAKE_OTA_SERVICE_JSON", `{"spec":{"type":"ClusterIP","selector":{"app.kubernetes.io/name":"video-cloud-otaservice"},"ports":[{"port":18084,"targetPort":"http"}]}}`)
	t.Setenv("FAKE_OTA_ENDPOINTSLICES_JSON", `{"items":[{"ports":[{"port":18084}],"endpoints":[{"addresses":["10.0.0.5"],"conditions":{"ready":true}}]}]}`)
	t.Setenv("FAKE_WEBRTC_CORE_DEPLOYMENT_JSON", `{"metadata":{"name":"video-cloud-api"},"spec":{"template":{"spec":{"containers":[{"name":"app","env":[{"name":"VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED","value":"false"}]}]}}}}`)
	if err := lkeApplyPublicHTTPS(provisionPaths{}, env, provisionOptions{}); err == nil || !strings.Contains(err.Error(), "observed core cutover") {
		t.Fatalf("OTA edge accepted core still owning operator releases: %v", err)
	}
}

func TestLKEOTACoreRestorationWaitsForLiveEdgeRemoval(t *testing.T) {
	fakeKubectl(t)
	t.Setenv("LKE_OTA_CORE_CUTOVER_ENABLED", "false")
	env := map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging", "VIDEO_CLOUD_DEVICE_DOMAIN": "device.example.test", "LKE_VIDEO_CLOUD_IMAGE": "example.test/video-cloud:reviewed", "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED": "false"}
	opts := provisionOptions{workloads: []string{"video-cloud"}}
	t.Setenv("LKE_OTA_SERVICE_EDGE_ENABLED", "true")
	if err := lkeDeployWorkloads(provisionPaths{}, env, opts); err == nil || !strings.Contains(err.Error(), "after disabling the device edge") {
		t.Fatalf("core restoration accepted desired device edge: %v", err)
	}
	t.Setenv("LKE_OTA_SERVICE_EDGE_ENABLED", "false")
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", `{"spec":{"rules":[{"http":{"paths":[{"path":"/v1/device/ota/"}]}}]}}`)
	if err := lkeDeployWorkloads(provisionPaths{}, env, opts); err == nil || !strings.Contains(err.Error(), "before restoring core") {
		t.Fatalf("core restoration accepted a live OTA device edge: %v", err)
	}
	t.Setenv("FAKE_WEBRTC_DEVICE_INGRESS_JSON", otaRollbackCoreIngressJSON)
	t.Setenv("FAKE_WEBRTC_CORE_DEPLOYMENT_JSON", otaRollbackCoreDeploymentJSON)
	if err := lkeDeployWorkloads(provisionPaths{}, env, opts); err == nil || !strings.Contains(err.Error(), "mTLS certificate") {
		t.Fatalf("core restoration skipped the post-ingress data-plane proof: %v", err)
	}
}
