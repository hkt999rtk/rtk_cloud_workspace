package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	shadowWorkerIdentitySecretName = "shadow-worker-platform-identity"
	shadowWorkerInstanceID         = "shadow-worker-0"
)

func lkeShadowWorkerRegistrationEnabled(env map[string]string) bool {
	raw := firstNonEmpty(os.Getenv("LKE_SHADOW_WORKER_REGISTRATION_ENABLED"), env["LKE_SHADOW_WORKER_REGISTRATION_ENABLED"], "false")
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func lkeRequireShadowWorkerIdentitySecret(env map[string]string) error {
	return lkeRequirePlatformServiceIdentitySecret(env, shadowWorkerIdentitySecretName, "Shadow worker platform identity Secret", "service:shadow")
}

// Turning off the rollout flag also gives Shadow subscriptions back to the
// core API. Refuse that change while a worker Deployment could still consume
// the same shared MQTT request group; drain and remove it explicitly first.
func lkePreventShadowWorkerRollbackOverlap(env map[string]string) error {
	out, err := kubectlCombinedOutput(nil, "-n", lkeNamespaceName(env, "video-cloud"), "get", "deployment", "video-cloud-shadowworker", "--ignore-not-found=true", "-o", "name")
	if err != nil {
		return fmt.Errorf("inspect existing Shadow worker before core rollback: %w", err)
	}
	if strings.TrimSpace(string(out)) != "" {
		return fmt.Errorf("Shadow worker Deployment still exists; drain and remove it before returning Shadow MQTT subscriptions to the core API")
	}
	return nil
}

// The fixed single instance matches a preapproved service/instance/certificate
// tuple. Recreate prevents overlapping Pods from renewing the same lease.
func lkeShadowWorkerDeploymentManifest(env map[string]string) string {
	videoNS := lkeNamespaceName(env, "video-cloud")
	accountNS := lkeNamespaceName(env, "account-manager")
	return fmt.Sprintf(`apiVersion: apps/v1
kind: Deployment
metadata:
  name: video-cloud-shadowworker
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-shadowworker
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  replicas: 1
  strategy:
    type: Recreate
  selector:
    matchLabels:
      app.kubernetes.io/name: video-cloud-shadowworker
  template:
    metadata:
      labels:
        app.kubernetes.io/name: video-cloud-shadowworker
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
        - name: shadowworker
          image: %s
          imagePullPolicy: IfNotPresent
          command: ["/app/shadowworker"]
          ports:
            - name: health
              containerPort: 18081
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
              memory: "512Mi"
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
            - name: VIDEO_CLOUD_AUTH_SECRET
              valueFrom:
                secretKeyRef:
                  name: video-cloud-runtime
                  key: VIDEO_CLOUD_AUTH_SECRET
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
              value: "2"
            - name: VIDEO_CLOUD_SHADOW_CACHE_ENABLED
              value: "true"
            - name: VIDEO_CLOUD_SHADOW_MUTATIONS_ENABLED
              value: "true"
            - name: VIDEO_CLOUD_SHADOW_CACHE_ADDR
              value: "redis.%s.svc.cluster.local:6379"
            - name: VIDEO_CLOUD_SHADOW_WORKER_ENABLED
              value: "true"
            - name: VIDEO_CLOUD_SHADOW_WORKER_HEALTH_ADDR
              value: ":18081"
            - name: VIDEO_CLOUD_SHADOW_WORKER_INSTANCE_ID
              value: %q
            - name: VIDEO_CLOUD_SHADOW_WORKER_ENDPOINT_REF
              value: "shadow-worker"
            - name: VIDEO_CLOUD_SHADOW_WORKER_REGISTRATION_URL
              value: "https://account-manager.%s.svc.cluster.local:8443"
            - name: VIDEO_CLOUD_SHADOW_WORKER_CLIENT_CERT
              value: "/etc/video_cloud/platform-service/client.crt"
            - name: VIDEO_CLOUD_SHADOW_WORKER_CLIENT_KEY
              value: "/etc/video_cloud/platform-service/client.key"
            - name: VIDEO_CLOUD_SHADOW_WORKER_SERVER_CA
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
		lkeNamespaceName(env, "platform"),
		shadowWorkerInstanceID,
		accountNS,
		shadowWorkerIdentitySecretName,
	)
}

func lkeShadowWorkerServiceManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Service
metadata:
  name: video-cloud-shadowworker
  namespace: %s
  labels:
    app.kubernetes.io/name: video-cloud-shadowworker
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  type: ClusterIP
  selector:
    app.kubernetes.io/name: video-cloud-shadowworker
  ports:
    - name: health
      port: 18081
      targetPort: health
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"])
}
