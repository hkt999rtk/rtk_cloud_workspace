package main

import (
	"fmt"
	"strconv"
)

const (
	otaRegistrarIdentitySecretName = "ota-service-platform-identity"
	otaRegistrarInstanceID         = "ota-service-0"
)

func lkeOTARegistrarRegistrationEnabled(env map[string]string) bool {
	return lkeFeatureEnabled(env, "LKE_OTA_REGISTRAR_REGISTRATION_ENABLED")
}

func lkeOTAEntitlementsRequired(env map[string]string) bool {
	return lkeFeatureEnabled(env, "VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED")
}

func lkeRequireOTARegistrarIdentitySecret(env map[string]string) error {
	return lkeRequirePlatformServiceIdentitySecret(env, otaRegistrarIdentitySecretName, "OTA platform identity Secret", "service:ota")
}

// OTA routes remain in the existing API process. This single registrar probes
// the API's feature-specific readiness and owns the stable Platform lease.
func lkeOTARegistrarDeploymentManifest(env map[string]string) string {
	videoNS := lkeNamespaceName(env, "video-cloud")
	accountNS := lkeNamespaceName(env, "account-manager")
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: video-cloud-otaregistrar
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-otaregistrar
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app.kubernetes.io/name: video-cloud-otaregistrar
  template:
    metadata:
      labels:
        app.kubernetes.io/name: video-cloud-otaregistrar
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
        - name: otaregistrar
          image: %s
          imagePullPolicy: IfNotPresent
          command: ["/app/otaregistrar"]
          ports:
            - name: health
              containerPort: 18085
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
            - name: VIDEO_CLOUD_OTA_SERVICE_ENABLED
              value: "true"
            - name: VIDEO_CLOUD_OTA_ENTITLEMENTS_REQUIRED
              value: %q
            - name: VIDEO_CLOUD_OTA_REGISTRAR_HEALTH_ADDR
              value: ":18085"
            - name: VIDEO_CLOUD_OTA_UPSTREAM_URL
              value: "http://video-cloud-api.%s.svc.cluster.local:8080"
            - name: VIDEO_CLOUD_OTA_SERVICE_INSTANCE_ID
              value: %q
            - name: VIDEO_CLOUD_OTA_SERVICE_ENDPOINT_REF
              value: "ota"
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
`, videoNS, env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], lkeDeploymentImagePullSecretsManifest(env), lkeVideoCloudImage(env), firstNonEmpty(lkeEnvValue(env, "VIDEO_CLOUD_ENV"), env["CLOUD_ENV_NAME"], env["ACCOUNT_MANAGER_ENV"], "staging"), strconv.FormatBool(lkeOTAEntitlementsRequired(env)), videoNS, otaRegistrarInstanceID, accountNS, otaRegistrarIdentitySecretName)
}

func lkeOTARegistrarServiceManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: video-cloud-otaregistrar
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-otaregistrar
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: video-cloud-otaregistrar
  ports:
    - name: health
      port: 18085
      targetPort: health
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"])
}
