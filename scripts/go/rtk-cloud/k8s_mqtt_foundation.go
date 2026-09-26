package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	mqttFoundationIdentitySecretName = "mqtt-foundation-platform-identity"
	mqttFoundationInstanceID         = "mqtt-foundation-0"
)

var mqttFoundationIdentitySecretKeys = []string{"client.crt", "client.key", "server-ca.crt"}

func lkeMQTTEntitlementsRequired(env map[string]string) bool {
	return lkeFeatureEnabled(env, "VIDEO_CLOUD_MQTT_ENTITLEMENTS_REQUIRED")
}

func lkeMQTTFoundationRegistrationEnabled(env map[string]string) bool {
	raw := firstNonEmpty(os.Getenv("LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED"), env["LKE_MQTT_FOUNDATION_REGISTRATION_ENABLED"], "false")
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func lkeRequireMQTTFoundationIdentitySecret(env map[string]string) error {
	return lkeRequirePlatformServiceIdentitySecret(env, mqttFoundationIdentitySecretName, "MQTT foundation platform identity Secret", "service:mqtt")
}

// Targeted Video Cloud rollouts must not create a registrar pointed at an
// Account Manager Service that was never upgraded with the private listener.
func lkeRequireExistingServiceRegistrationEndpoint(env map[string]string) error {
	service, err := kubectlResourceJSON(lkeNamespaceName(env, "account-manager"), "service", "account-manager")
	if err != nil {
		return fmt.Errorf("Account Manager service registration endpoint is unavailable: %w", err)
	}
	spec, ok := service["spec"].(map[string]any)
	if !ok || spec["type"] != "ClusterIP" {
		return fmt.Errorf("Account Manager service registration endpoint is not private")
	}
	if externalIPs, ok := spec["externalIPs"].([]any); ok && len(externalIPs) > 0 {
		return fmt.Errorf("Account Manager service registration endpoint has external addresses")
	}
	selector, ok := spec["selector"].(map[string]any)
	if !ok || selector["app.kubernetes.io/name"] != "account-manager" {
		return fmt.Errorf("Account Manager service registration endpoint targets the wrong Pods")
	}
	ports, ok := spec["ports"].([]any)
	if !ok {
		return fmt.Errorf("Account Manager service registration endpoint has no ports")
	}
	for _, raw := range ports {
		port, ok := raw.(map[string]any)
		if ok && port["name"] == "service-registry" && port["port"] == float64(8443) && port["targetPort"] == "service-registry" {
			return nil
		}
	}
	return fmt.Errorf("Account Manager service registration endpoint lacks the private 8443 port")
}

// A single stable registrar instance is deliberate: Account Manager authorizes
// the exact service/instance/certificate tuple. Recreate avoids two Pods
// heartbeating as the same approved instance during a rollout. Additional
// replicas need separately approved identities and stable instance IDs.
func lkeMQTTFoundationDeploymentManifest(env map[string]string) string {
	videoNS := lkeNamespaceName(env, "video-cloud")
	accountNS := lkeNamespaceName(env, "account-manager")
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: video-cloud-mqttfoundation
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-mqttfoundation
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app.kubernetes.io/name: video-cloud-mqttfoundation
  template:
    metadata:
      labels:
        app.kubernetes.io/name: video-cloud-mqttfoundation
        app.kubernetes.io/part-of: rtk-cloud
        rtk.realtek.com/provider: lke
        rtk.realtek.com/stack: %s
    spec:
%s      terminationGracePeriodSeconds: 15
      securityContext:
        runAsNonRoot: true
        runAsUser: 10001
        runAsGroup: 10001
        fsGroup: 10001
        seccompProfile:
          type: RuntimeDefault
      containers:
        - name: mqttfoundation
          image: %s
          imagePullPolicy: IfNotPresent
          command: ["/app/mqttfoundation"]
          ports:
            - name: health
              containerPort: 18080
          livenessProbe:
            httpGet:
              path: /healthz
              port: health
            periodSeconds: 10
          readinessProbe:
            httpGet:
              path: /readyz
              port: health
            periodSeconds: 5
          resources:
            requests:
              cpu: "50m"
              memory: "128Mi"
            limits:
              memory: "256Mi"
          env:
            - name: VIDEO_CLOUD_ENV
              value: %q
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: POSTGRES_PASSWORD
            - name: VIDEO_CLOUD_DB_DSN
              value: "postgres://postgres:$(POSTGRES_PASSWORD)@postgresql.%s.svc.cluster.local:5432/video_cloud?sslmode=disable"
            - name: VIDEO_CLOUD_MQTT_ENABLED
              value: "true"
            - name: VIDEO_CLOUD_MQTT_ADDR
              value: %q
            - name: VIDEO_CLOUD_MQTT_USERNAME
              value: "video-cloud-server"
            - name: VIDEO_CLOUD_MQTT_PASSWORD
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: VIDEO_CLOUD_MQTT_SERVER_PASSWORD
            - name: VIDEO_CLOUD_MQTT_TENANT_NAMESPACE_ENABLED
              value: %q
            - name: VIDEO_CLOUD_MQTT_TENANT_PREFIX
              value: "_bc"
            - name: VIDEO_CLOUD_MQTT_TOPIC_ROOT
              value: "devices"
            - name: VIDEO_CLOUD_MQTT_OUTBOUND_CONNECTIONS
              value: "1"
            - name: VIDEO_CLOUD_MQTT_FOUNDATION_ENABLED
              value: "true"
            - name: VIDEO_CLOUD_MQTT_FOUNDATION_HEALTH_ADDR
              value: ":18080"
            - name: VIDEO_CLOUD_MQTT_FOUNDATION_INSTANCE_ID
              value: %q
            - name: VIDEO_CLOUD_MQTT_FOUNDATION_ENDPOINT_REF
              value: "mqtt"
            - name: VIDEO_CLOUD_MQTT_FOUNDATION_REGISTRATION_URL
              value: "https://account-manager.%s.svc.cluster.local:8443"
            - name: VIDEO_CLOUD_MQTT_FOUNDATION_CLIENT_CERT
              value: "/etc/video_cloud/platform-service/client.crt"
            - name: VIDEO_CLOUD_MQTT_FOUNDATION_CLIENT_KEY
              value: "/etc/video_cloud/platform-service/client.key"
            - name: VIDEO_CLOUD_MQTT_FOUNDATION_SERVER_CA
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
`,
		videoNS,
		env["CLOUD_STACK_NAME"],
		env["CLOUD_STACK_NAME"],
		lkeDeploymentImagePullSecretsManifest(env),
		lkeEnvValue(env, "LKE_VIDEO_CLOUD_IMAGE"),
		firstNonEmpty(os.Getenv("ACCOUNT_MANAGER_ENV"), env["ACCOUNT_MANAGER_ENV"], "staging"),
		lkeNamespaceName(env, "platform"),
		lkeMQTTInternalAddr(env),
		strconv.FormatBool(lkeMQTTTenantNamespaceEnabled(env)),
		mqttFoundationInstanceID,
		accountNS,
		mqttFoundationIdentitySecretName,
	)
}

func lkeMQTTFoundationServiceManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: video-cloud-mqttfoundation
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-mqttfoundation
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: video-cloud-mqttfoundation
  ports:
    - name: health
      port: 18080
      targetPort: health
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"])
}
