package main

import (
	"fmt"
	"os"
)

const (
	videoStorageServiceIdentitySecretName = "video-storage-service-platform-identity"
	videoStorageServiceInstanceID         = "video-storage-service-0"
)

func lkeVideoStorageServiceRegistrationEnabled(env map[string]string) bool {
	return lkeFeatureEnabled(env, "LKE_VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED")
}

func lkeRequireVideoStorageServiceIdentitySecret(env map[string]string) error {
	return lkeRequirePlatformServiceIdentitySecret(env, videoStorageServiceIdentitySecretName, "video storage service platform identity Secret", "service:video-storage")
}

// This Pod is private until a segment-aware edge route owns every media path.
// Recreate keeps the preapproved service/instance/certificate tuple singular.
func lkeVideoStorageServiceDeploymentManifest(env map[string]string) string {
	videoNS := lkeNamespaceName(env, "video-cloud")
	platformNS := lkeNamespaceName(env, "platform")
	accountNS := lkeNamespaceName(env, "account-manager")
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: video-cloud-videostorage
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-videostorage
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app.kubernetes.io/name: video-cloud-videostorage
  template:
    metadata:
      labels:
        app.kubernetes.io/name: video-cloud-videostorage
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
        - name: videostorage
          image: %s
          imagePullPolicy: IfNotPresent
          command: ["/app/videostorage"]
          ports:
            - name: http
              containerPort: 18083
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
              value: ":18083"
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
            - name: VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_URL
              value: %q
            - name: VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN
            - name: VIDEO_CLOUD_MQTT_ENABLED
              value: "false"
            - name: VIDEO_CLOUD_WEBRTC_SIGNALING_STORE_ENABLED
              value: "false"
            - name: VIDEO_CLOUD_CLIP_PRIVATE_KEY_PATH
              value: "/etc/video_cloud/clip-crypto/clip-private-key.pem"
%s            - name: VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_ENABLED
              value: "true"
            - name: VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_INSTANCE_ID
              value: %q
            - name: VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_ENDPOINT_REF
              value: "video-storage-service"
            - name: VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_REGISTRATION_URL
              value: "https://account-manager.%s.svc.cluster.local:8443"
            - name: VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_CLIENT_CERT
              value: "/etc/video_cloud/platform-service/client.crt"
            - name: VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_CLIENT_KEY
              value: "/etc/video_cloud/platform-service/client.key"
            - name: VIDEO_CLOUD_VIDEO_STORAGE_SERVICE_SERVER_CA
              value: "/etc/video_cloud/platform-service/server-ca.crt"
            - name: VIDEO_CLOUD_LOGGER_SPOOL_DIR
              value: "/var/lib/video_cloud/logger-spool"
          volumeMounts:
            - name: platform-service-identity
              mountPath: /etc/video_cloud/platform-service
              readOnly: true
            - name: clip-crypto
              mountPath: /etc/video_cloud/clip-crypto
              readOnly: true
            - name: writable-state
              mountPath: /var/lib/video_cloud
      volumes:
        - name: platform-service-identity
          secret:
            secretName: %s
            defaultMode: 0440
        - name: clip-crypto
          secret:
            secretName: video-cloud-runtime
            defaultMode: 0440
            items:
              - key: clip-private-key.pem
                path: clip-private-key.pem
        - name: writable-state
          emptyDir: {}
`, videoNS, env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], lkeDeploymentImagePullSecretsManifest(env), lkeEnvValue(env, "LKE_VIDEO_CLOUD_IMAGE"),
		firstNonEmpty(os.Getenv("ACCOUNT_MANAGER_ENV"), env["ACCOUNT_MANAGER_ENV"], env["CLOUD_ENV_NAME"], "staging"), lkeVideoCloudAPIBaseURL(env), platformNS,
		lkeAccountManagerInternalURL(env), lkeBlobEnvironmentManifest(env, "video-cloud-runtime"), videoStorageServiceInstanceID, accountNS, videoStorageServiceIdentitySecretName)
}

func lkeVideoStorageServiceServiceManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: video-cloud-videostorage
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-videostorage
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: video-cloud-videostorage
  ports:
    - name: http
      port: 18083
      targetPort: http
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"])
}
