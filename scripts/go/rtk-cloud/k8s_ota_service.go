package main

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
)

const otaServiceWorkloadName = "video-cloud-otaservice"

func lkeOTAServiceRegistrationEnabled(env map[string]string) bool {
	return lkeFeatureEnabled(env, "LKE_OTA_SERVICE_REGISTRATION_ENABLED")
}

func lkeOTAServiceEdgeEnabled(env map[string]string) bool {
	return lkeFeatureEnabled(env, "LKE_OTA_SERVICE_EDGE_ENABLED")
}

func lkeOTACoreCutoverEnabled(env map[string]string) bool {
	return lkeFeatureEnabled(env, "LKE_OTA_CORE_CUTOVER_ENABLED")
}

func lkeRequireOTAServiceInputs(env map[string]string) error {
	if lkeOTARegistrarRegistrationEnabled(env) {
		return fmt.Errorf("independent OTA service and core OTA registrar cannot own the same Platform lease")
	}
	if !lkeOTAEntitlementsRequired(env) {
		return fmt.Errorf("OTA service registration requires strict Product OTA entitlements on the core API")
	}
	if !lkeMQTTFoundationRegistrationEnabled(env) {
		return fmt.Errorf("OTA service registration requires the MQTT foundation registrar")
	}
	if !lkeAccountManagerServiceRegistrationEnabled(env) {
		return fmt.Errorf("OTA service registration requires the Account Manager service registration listener")
	}
	if strings.TrimSpace(env["VIDEO_CLOUD_BLOB_BUCKET"]) == "" || strings.TrimSpace(env["VIDEO_CLOUD_BLOB_REGION"]) == "" {
		return fmt.Errorf("OTA service requires private object storage")
	}
	cdnURL, err := url.Parse(strings.TrimSpace(env["VIDEO_CLOUD_OTA_CDN_BASE_URL"]))
	if err != nil || cdnURL.Scheme != "https" || cdnURL.Host == "" || cdnURL.User != nil || cdnURL.RawQuery != "" || cdnURL.Fragment != "" {
		return fmt.Errorf("OTA service requires an HTTPS private-origin CDN base URL")
	}
	if err := lkeRequireOTACDNRuntimeSecret(env); err != nil {
		return err
	}
	if err := lkeRequireOTAServiceRuntimeSecrets(env); err != nil {
		return err
	}
	return lkeRequireOTARegistrarIdentitySecret(env)
}

func lkeRequireOTAServiceRuntimeSecrets(env map[string]string) error {
	namespace := lkeNamespaceName(env, "video-cloud")
	for secretName, keys := range map[string][]string{
		"video-cloud-runtime": {
			"POSTGRES_PASSWORD", "VIDEO_CLOUD_AUTH_SECRET", "VIDEO_CLOUD_OTA_BFF_TOKEN",
			"VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
		},
		"video-cloud-workers-runtime": {"VIDEO_CLOUD_BILLING_USAGE_TOKEN"},
	} {
		secret, err := kubectlResourceJSON(namespace, "secret", secretName)
		if err != nil {
			return fmt.Errorf("OTA service runtime Secret %s is unavailable: %w", secretName, err)
		}
		for _, key := range keys {
			value, err := kubernetesSecretBytes(secret, key)
			if err != nil || len(strings.TrimSpace(string(value))) == 0 {
				return fmt.Errorf("OTA service runtime Secret %s lacks %s", secretName, key)
			}
		}
	}
	return nil
}

func lkeRequireOTACDNRuntimeSecret(env map[string]string) error {
	secret, err := kubectlResourceJSON(lkeNamespaceName(env, "video-cloud"), "secret", "ota-cdn-runtime")
	if err != nil {
		return fmt.Errorf("OTA CDN runtime Secret is unavailable: %w", err)
	}
	raw, err := kubernetesSecretBytes(secret, "VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX")
	if err != nil {
		return fmt.Errorf("OTA CDN runtime Secret is incomplete: %w", err)
	}
	return validateOTACDNTokenKey(raw)
}

func validateOTACDNTokenKey(raw []byte) error {
	decoded, err := hex.DecodeString(strings.TrimSpace(string(raw)))
	if err != nil || len(decoded) < 32 {
		return fmt.Errorf("OTA CDN runtime Secret has an invalid token key")
	}
	return nil
}

// The OTA process, not the core API or the registrar, owns the billable
// artifact namespace and the only OTA Billing outbox delivery loop.
func lkeOTAServiceDeploymentManifest(env map[string]string) string {
	videoNS := lkeNamespaceName(env, "video-cloud")
	platformNS := lkeNamespaceName(env, "platform")
	accountNS := lkeNamespaceName(env, "account-manager")
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/name: %s
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app.kubernetes.io/name: %s
  template:
    metadata:
      labels:
        app.kubernetes.io/name: %s
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
%s      terminationGracePeriodSeconds: 30
      securityContext:
        runAsNonRoot: true
        runAsUser: 10001
        runAsGroup: 10001
        fsGroup: 10001
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: otaservice
          image: %s
          imagePullPolicy: IfNotPresent
          command: ["/app/otaservice"]
          ports:
            - name: http
              containerPort: 18084
          livenessProbe:
            httpGet:
              path: /healthz
              port: http
            periodSeconds: 10
          readinessProbe:
            httpGet:
              path: /readyz
              port: http
            periodSeconds: 5
          resources:
            requests:
              cpu: "100m"
              memory: "256Mi"
            limits:
              memory: "768Mi"
          env:
            - name: VIDEO_CLOUD_ENV
              value: %q
            - name: VIDEO_CLOUD_API_ADDR
              value: ":18084"
            - name: VIDEO_CLOUD_API_BASE_URL
              value: %q
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: POSTGRES_PASSWORD
            - name: VIDEO_CLOUD_DB_DSN
              value: "postgres://postgres:$(POSTGRES_PASSWORD)@postgresql.%s.svc.cluster.local:5432/video_cloud?sslmode=disable"
            - name: VIDEO_CLOUD_DB_ENSURE_SCHEMA
              value: "false"
            - name: VIDEO_CLOUD_AUTH_SECRET
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: VIDEO_CLOUD_AUTH_SECRET
            - name: VIDEO_CLOUD_AUTH_TRUSTED_CLIENT_CERT_HEADERS
              value: "true"
            - name: VIDEO_CLOUD_OTA_BFF_TOKEN
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: VIDEO_CLOUD_OTA_BFF_TOKEN
            - name: VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON
              value: %q
            - name: VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED
              value: "true"
            - name: VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_URL
              value: %q
            - name: VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN
            - name: VIDEO_CLOUD_MQTT_ENABLED
              value: "false"
            - name: VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED
              value: "false"
            - name: VIDEO_CLOUD_WEBRTC_SIGNALING_STORE_ENABLED
              value: "false"
            - name: VIDEO_CLOUD_BLOB_ENDPOINT
              value: %q
            - name: VIDEO_CLOUD_BLOB_REGION
              value: %q
            - name: VIDEO_CLOUD_BLOB_BUCKET
              value: %q
            - name: VIDEO_CLOUD_BLOB_PREFIX
              value: %q
            - name: VIDEO_CLOUD_BLOB_FORCE_PATH_STYLE
              value: %q
            - name: AWS_ACCESS_KEY_ID
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: AWS_ACCESS_KEY_ID
            - name: AWS_SECRET_ACCESS_KEY
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: AWS_SECRET_ACCESS_KEY
            - name: VIDEO_CLOUD_OTA_CDN_BASE_URL
              value: %q
            - name: VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX
              valueFrom:
                secretKeyRef:
                  name: ota-cdn-runtime
                  key: VIDEO_CLOUD_OTA_CDN_TOKEN_KEY_HEX
            - name: VIDEO_CLOUD_OTA_CDN_TOKEN_NAME
              value: %q
            - name: VIDEO_CLOUD_BILLING_USAGE_ENDPOINT
              value: "http://billing.%s.svc.cluster.local:80/v1/internal/billing/usage-facts"
            - name: VIDEO_CLOUD_BILLING_USAGE_TOKEN
              valueFrom:
                secretKeyRef:
                  name: video-cloud-workers-runtime
                  key: VIDEO_CLOUD_BILLING_USAGE_TOKEN
            - name: VIDEO_CLOUD_BILLING_USAGE_FORWARD_INTERVAL
              value: %q
            - name: VIDEO_CLOUD_OTA_SERVICE_ENABLED
              value: "true"
            - name: VIDEO_CLOUD_OTA_SERVICE_INSTANCE_ID
              value: %q
            - name: VIDEO_CLOUD_OTA_SERVICE_ENDPOINT_REF
              value: "ota-service"
            - name: VIDEO_CLOUD_OTA_SERVICE_REGISTRATION_URL
              value: "https://account-manager.%s.svc.cluster.local:8443"
            - name: VIDEO_CLOUD_OTA_SERVICE_CLIENT_CERT
              value: "/etc/video_cloud/platform-service/client.crt"
            - name: VIDEO_CLOUD_OTA_SERVICE_CLIENT_KEY
              value: "/etc/video_cloud/platform-service/client.key"
            - name: VIDEO_CLOUD_OTA_SERVICE_SERVER_CA
              value: "/etc/video_cloud/platform-service/server-ca.crt"
          volumeMounts:
            - name: platform-service-identity
              mountPath: /etc/video_cloud/platform-service
              readOnly: true
      volumes:
        - name: platform-service-identity
          secret:
            secretName: %s
            defaultMode: 0440
`, otaServiceWorkloadName, videoNS, otaServiceWorkloadName, env["CLOUD_STACK_NAME"],
		otaServiceWorkloadName, otaServiceWorkloadName, env["CLOUD_STACK_NAME"],
		lkeDeploymentImagePullSecretsManifest(env), lkeVideoCloudImage(env),
		firstNonEmpty(lkeEnvValue(env, "VIDEO_CLOUD_ENV"), env["CLOUD_ENV_NAME"], env["ACCOUNT_MANAGER_ENV"], "staging"),
		lkeVideoCloudAPIBaseURL(env), platformNS, env["VIDEO_CLOUD_OTA_TRUSTED_MANIFEST_KEYS_JSON"],
		lkeAccountManagerInternalURL(env), env["VIDEO_CLOUD_BLOB_ENDPOINT"], env["VIDEO_CLOUD_BLOB_REGION"],
		env["VIDEO_CLOUD_BLOB_BUCKET"], env["VIDEO_CLOUD_BLOB_PREFIX"], firstNonEmpty(env["VIDEO_CLOUD_BLOB_FORCE_PATH_STYLE"], "false"),
		env["VIDEO_CLOUD_OTA_CDN_BASE_URL"], firstNonEmpty(env["VIDEO_CLOUD_OTA_CDN_TOKEN_NAME"], "__token__"),
		lkeNamespaceName(env, "billing"), firstNonEmpty(env["VIDEO_CLOUD_BILLING_USAGE_FORWARD_INTERVAL"], "5s"),
		otaRegistrarInstanceID, accountNS, otaRegistrarIdentitySecretName)
}

func lkeOTAServiceServiceManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/name: %s
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: %s
  ports:
    - name: http
      port: 18084
      targetPort: http
`, otaServiceWorkloadName, lkeNamespaceName(env, "video-cloud"), otaServiceWorkloadName, env["CLOUD_STACK_NAME"], otaServiceWorkloadName)
}

// Remove the device edge route only after the observed core Deployment has
// restored its own OTA handler. A desired flag alone cannot prove that rollout.
func lkePreventOTAEdgeRollbackOverlap(env map[string]string) error {
	if lkeOTACoreCutoverEnabled(env) {
		return fmt.Errorf("OTA device edge route cannot be removed while core cutover remains enabled")
	}
	body, err := kubectlCombinedOutput(nil, "-n", lkeNamespaceName(env, "video-cloud"), "get", "deployment", "video-cloud-api", "--ignore-not-found=true", "-o", "json")
	if err != nil {
		return fmt.Errorf("inspect core OTA cutover before edge rollback: %w", err)
	}
	if len(strings.TrimSpace(string(body))) == 0 {
		ingressBody, err := kubectlCombinedOutput(nil, "-n", lkeIngressNamespace(env), "get", "ingress", "video-cloud-staging-device-mtls", "--ignore-not-found=true", "-o", "json")
		if err != nil {
			return fmt.Errorf("inspect device OTA ingress before edge rollback: %w", err)
		}
		if len(strings.TrimSpace(string(ingressBody))) > 0 {
			var ingress map[string]any
			if err := json.Unmarshal(ingressBody, &ingress); err != nil {
				return fmt.Errorf("decode device OTA ingress before edge rollback: %w", err)
			}
			spec, _ := ingress["spec"].(map[string]any)
			rules, _ := spec["rules"].([]any)
			for _, rawRule := range rules {
				rule, _ := rawRule.(map[string]any)
				httpRule, _ := rule["http"].(map[string]any)
				paths, _ := httpRule["paths"].([]any)
				for _, rawPath := range paths {
					path, _ := rawPath.(map[string]any)
					if path["path"] == "/v1/device/ota/" {
						return fmt.Errorf("core API is absent while live device ingress still routes OTA to the edge service")
					}
				}
			}
		}
		return nil
	}
	var deployment struct {
		Metadata struct {
			Name string `json:"name"`
		} `json:"metadata"`
		Spec struct {
			Template struct {
				Spec struct {
					Containers []struct {
						Name string `json:"name"`
						Env  []struct {
							Name  string `json:"name"`
							Value string `json:"value"`
						} `json:"env"`
					} `json:"containers"`
				} `json:"spec"`
			} `json:"template"`
		} `json:"spec"`
	}
	if err := json.Unmarshal(body, &deployment); err != nil {
		return fmt.Errorf("decode core OTA cutover state: %w", err)
	}
	if deployment.Metadata.Name != "video-cloud-api" {
		return fmt.Errorf("inspect core OTA cutover: unexpected Deployment")
	}
	for _, container := range deployment.Spec.Template.Spec.Containers {
		if container.Name != "app" {
			continue
		}
		for _, variable := range container.Env {
			if variable.Name == "VIDEO_CLOUD_OTA_SERVICE_CUTOVER_ENABLED" && strings.EqualFold(variable.Value, "true") {
				return fmt.Errorf("core API still has OTA handlers disabled; restore core handlers before removing device edge route")
			}
		}
	}
	if err := runKubectl("-n", lkeNamespaceName(env, "video-cloud"), "rollout", "status", "deployment/video-cloud-api", "--timeout", firstNonEmpty(os.Getenv("LKE_WORKLOAD_ROLLOUT_TIMEOUT"), "5m")); err != nil {
		return fmt.Errorf("core API has not completed OTA handler restoration: %w", err)
	}
	return nil
}

// The core may drop its device OTA handlers only after the live device-host
// ingress sends that path to the independent service and still requires mTLS.
func lkeRequireActiveOTADeviceEdgeRoute(env map[string]string) error {
	ingress, err := kubectlResourceJSON(lkeIngressNamespace(env), "ingress", "video-cloud-staging-device-mtls")
	if err != nil {
		return fmt.Errorf("OTA core cutover requires device mTLS ingress: %w", err)
	}
	if !lkeIngressRequiresDeviceMTLS(ingress) {
		return fmt.Errorf("OTA core cutover requires device mTLS ingress authentication")
	}
	host := firstNonEmpty(lkeEnvValue(env, "LKE_DEVICE_DOMAIN"), env["VIDEO_CLOUD_DEVICE_DOMAIN"], "device."+env["VIDEO_CLOUD_DOMAIN"])
	service := lkePublicHTTPSBridgeServiceName(env, lkePublicHTTPSRoute{Namespace: lkeNamespaceName(env, "video-cloud"), Service: otaServiceWorkloadName})
	spec, ok := ingress["spec"].(map[string]any)
	if !ok {
		return fmt.Errorf("OTA core cutover requires live device OTA ingress route")
	}
	rules, _ := spec["rules"].([]any)
	for _, rawRule := range rules {
		rule, ok := rawRule.(map[string]any)
		if !ok || rule["host"] != host {
			continue
		}
		httpRule, _ := rule["http"].(map[string]any)
		paths, _ := httpRule["paths"].([]any)
		for _, rawPath := range paths {
			path, ok := rawPath.(map[string]any)
			if !ok || path["path"] != "/v1/device/ota/" || path["pathType"] != "Prefix" {
				continue
			}
			backend, _ := path["backend"].(map[string]any)
			backendService, _ := backend["service"].(map[string]any)
			port, _ := backendService["port"].(map[string]any)
			if backendService["name"] == service && port["number"] == float64(18084) {
				return nil
			}
		}
	}
	return fmt.Errorf("OTA core cutover requires live device OTA ingress route")
}

// Cutover requires a private Service with a Ready endpoint. The process only
// becomes Ready after its Platform lease and OTA dependencies are available.
func lkeRequireReadyOTAServiceEndpoint(env map[string]string) error {
	namespace := lkeNamespaceName(env, "video-cloud")
	service, err := kubectlResourceJSON(namespace, "service", otaServiceWorkloadName)
	if err != nil {
		return fmt.Errorf("OTA service is not deployed: %w", err)
	}
	spec, ok := service["spec"].(map[string]any)
	if !ok || spec["type"] != "ClusterIP" {
		return fmt.Errorf("OTA Service is not private")
	}
	if externalIPs, ok := spec["externalIPs"].([]any); ok && len(externalIPs) > 0 {
		return fmt.Errorf("OTA Service has external addresses")
	}
	selector, ok := spec["selector"].(map[string]any)
	if !ok || selector["app.kubernetes.io/name"] != otaServiceWorkloadName {
		return fmt.Errorf("OTA Service targets the wrong Pods")
	}
	ports, ok := spec["ports"].([]any)
	validPort := false
	if ok {
		for _, raw := range ports {
			port, ok := raw.(map[string]any)
			if ok && port["port"] == float64(18084) && port["targetPort"] == "http" {
				validPort = true
			}
		}
	}
	if !validPort {
		return fmt.Errorf("OTA Service lacks port 18084")
	}
	body, err := kubectlCombinedOutput(nil, "-n", namespace, "get", "endpointslices", "-l", "kubernetes.io/service-name="+otaServiceWorkloadName, "-o", "json")
	if err != nil {
		return fmt.Errorf("inspect OTA ready endpoints: %w", err)
	}
	var list struct {
		Items []struct {
			Ports []struct {
				Port int `json:"port"`
			} `json:"ports"`
			Endpoints []struct {
				Addresses  []string `json:"addresses"`
				Conditions struct {
					Ready bool `json:"ready"`
				} `json:"conditions"`
			} `json:"endpoints"`
		} `json:"items"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return fmt.Errorf("decode OTA ready endpoints: %w", err)
	}
	for _, slice := range list.Items {
		portFound := false
		for _, port := range slice.Ports {
			if port.Port == 18084 {
				portFound = true
			}
		}
		if !portFound {
			continue
		}
		for _, endpoint := range slice.Endpoints {
			if endpoint.Conditions.Ready && len(endpoint.Addresses) > 0 {
				return nil
			}
		}
	}
	return fmt.Errorf("OTA service has no ready registered endpoint")
}

func lkeAllowVideoCloudAPIOTAGatewayNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-video-cloud-api-otaservice
  namespace: %s
  labels:
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: %s
  policyTypes: [Ingress]
  ingress:
    - from:
        - podSelector:
            matchLabels:
              app.kubernetes.io/name: video-cloud-api
      ports:
        - { protocol: TCP, port: 18084 }
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], otaServiceWorkloadName)
}
