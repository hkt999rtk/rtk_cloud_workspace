package main

import (
	"fmt"
	"os"
	"strings"
)

const (
	webrtcServiceIdentitySecretName = "webrtc-service-platform-identity"
	webrtcServiceInstanceID         = "webrtc-service-0"
)

func lkeWebRTCServiceRegistrationEnabled(env map[string]string) bool {
	raw := firstNonEmpty(os.Getenv("LKE_WEBRTC_SERVICE_REGISTRATION_ENABLED"), env["LKE_WEBRTC_SERVICE_REGISTRATION_ENABLED"], "false")
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func lkeRequireWebRTCServiceIdentitySecret(env map[string]string) error {
	return lkeRequirePlatformServiceIdentitySecret(env, webrtcServiceIdentitySecretName, "WebRTC service platform identity Secret", "service:webrtc")
}

// One stable Pod matches the preapproved service/instance/certificate tuple.
// The public edge and core-handler cutover are separate, later steps.
func lkeWebRTCServiceDeploymentManifest(env map[string]string) string {
	videoNS := lkeNamespaceName(env, "video-cloud")
	platformNS := lkeNamespaceName(env, "platform")
	accountNS := lkeNamespaceName(env, "account-manager")
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: video-cloud-webrtcservice
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-webrtcservice
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app.kubernetes.io/name: video-cloud-webrtcservice
  template:
    metadata:
      labels:
        app.kubernetes.io/name: video-cloud-webrtcservice
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
        - name: webrtcservice
          image: %s
          imagePullPolicy: IfNotPresent
          command: ["/app/webrtcservice"]
          ports:
            - name: http
              containerPort: 18082
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
              value: ":18082"
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
            - name: VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED
              value: "false"
            - name: VIDEO_CLOUD_WEBRTC_SIGNALING_STORE_ENABLED
              value: "true"
            - name: VIDEO_CLOUD_WEBRTC_SIGNALING_STORE_ADDR
              value: "redis.%s.svc.cluster.local:6379"
            - name: VIDEO_CLOUD_WEBRTC_SIGNALING_STORE_PREFIX
              value: "video_cloud:webrtc"
            - name: VIDEO_CLOUD_WEBRTC_STUN_URLS
              value: %q
            - name: VIDEO_CLOUD_WEBRTC_TURN_URLS
              value: %q
            - name: VIDEO_CLOUD_TURN_REALM
              value: %q
            - name: VIDEO_CLOUD_TURN_SHARED_SECRET
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: VIDEO_CLOUD_TURN_SHARED_SECRET
            - name: VIDEO_CLOUD_TURN_CREDENTIAL_TTL
              value: %q
            - name: VIDEO_CLOUD_WEBRTC_ICE_POLICY
              value: %q
            - name: VIDEO_CLOUD_TURN_REGISTRY_ADDR
              value: "http://video-cloud-turnregistry.%s.svc.cluster.local:18190"
            - name: VIDEO_CLOUD_TURN_REGISTRY_CLIENT_NODE_ID
              value: "video-cloud-webrtcservice"
            - name: VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY
              valueFrom:
                secretKeyRef:
                  name: video-cloud-workers-runtime
                  key: VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY
            - name: VIDEO_CLOUD_WEBRTC_SERVICE_ENABLED
              value: "true"
            - name: VIDEO_CLOUD_WEBRTC_SERVICE_INSTANCE_ID
              value: %q
            - name: VIDEO_CLOUD_WEBRTC_SERVICE_ENDPOINT_REF
              value: "webrtc-service"
            - name: VIDEO_CLOUD_WEBRTC_SERVICE_REGISTRATION_URL
              value: "https://account-manager.%s.svc.cluster.local:8443"
            - name: VIDEO_CLOUD_WEBRTC_SERVICE_CLIENT_CERT
              value: "/etc/video_cloud/platform-service/client.crt"
            - name: VIDEO_CLOUD_WEBRTC_SERVICE_CLIENT_KEY
              value: "/etc/video_cloud/platform-service/client.key"
            - name: VIDEO_CLOUD_WEBRTC_SERVICE_SERVER_CA
              value: "/etc/video_cloud/platform-service/server-ca.crt"
            - name: VIDEO_CLOUD_LOGGER_SPOOL_DIR
              value: "/var/lib/video_cloud/logger-spool"
          volumeMounts:
            - name: platform-service-identity
              mountPath: /etc/video_cloud/platform-service
              readOnly: true
            - name: writable-state
              mountPath: /var/lib/video_cloud
      volumes:
        - name: platform-service-identity
          secret:
            secretName: %s
            defaultMode: 0440
        - name: writable-state
          emptyDir: {}
`, videoNS, env["CLOUD_STACK_NAME"], env["CLOUD_STACK_NAME"], lkeDeploymentImagePullSecretsManifest(env), lkeEnvValue(env, "LKE_VIDEO_CLOUD_IMAGE"),
		firstNonEmpty(os.Getenv("ACCOUNT_MANAGER_ENV"), env["ACCOUNT_MANAGER_ENV"], "staging"), lkeVideoCloudAPIBaseURL(env), platformNS,
		lkeAccountManagerInternalURL(env), platformNS, lkeCoturnSTUNURLs(env), lkeCoturnTURNURLs(env), lkeCoturnRealm(env),
		lkeCoturnCredentialTTL(env), lkeWebRTCICEPolicy(env), videoNS, webrtcServiceInstanceID, accountNS, webrtcServiceIdentitySecretName)
}

func lkeWebRTCServiceServiceManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: video-cloud-webrtcservice
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-webrtcservice
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: video-cloud-webrtcservice
  ports:
    - name: http
      port: 18082
      targetPort: http
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"])
}
