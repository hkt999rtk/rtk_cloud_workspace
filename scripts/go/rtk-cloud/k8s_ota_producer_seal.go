package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func lkeOTAProducerSealScheduleEnabled(env map[string]string) bool {
	return lkeFeatureEnabled(env, "LKE_OTA_PRODUCER_SEAL_SCHEDULE_ENABLED")
}

func lkeOTAProducerSealToken() string {
	if activeCanonicalSecretStore {
		return lkeRuntimeSecretValue("ota-producer-seal-token")
	}
	// Optional credentials are not fabricated by the disposable dev fallback.
	return lkeRuntimeSecretCache["ota-producer-seal-token"]
}

func lkeRequireOTAProducerSealSchedule(env map[string]string) error {
	if !lkeOTAProducerSealScheduleEnabled(env) {
		return nil
	}
	if !lkeOTAServiceRegistrationEnabled(env) {
		return errors.New("OTA producer seal schedule requires the independent registered OTA service")
	}
	token := lkeOTAProducerSealToken()
	if len(token) < 32 || strings.TrimSpace(token) != token || strings.ContainsAny(token, " \t\r\n") ||
		token == lkeBillingServiceToken() || token == lkeBillingInternalToken() || token == lkeOTAPlatformSealToken() {
		return errors.New("OTA producer seal schedule requires a distinct canonical token of at least 32 characters")
	}
	if lkeVideoCloudImage(env) == "" || lkeAccountManagerInternalURL(env) == "" {
		return errors.New("OTA producer seal schedule requires the selected Video Cloud image and Account Manager internal endpoint")
	}
	if err := lkeRequireOTAServiceInputs(env); err != nil {
		return err
	}
	month := env["LKE_OTA_PRODUCER_SEAL_FIRST_MONTH"]
	if parsed, err := time.Parse("2006-01", month); err != nil || parsed.Format("2006-01") != month {
		return errors.New("OTA producer seal requires an explicit first eligible UTC month")
	}
	return lkeRequireOTAProducerSealIdentity(env, false)
}

func lkeRequireOTAProducerSealDeployment(env map[string]string, opts provisionOptions) error {
	if !lkeOTAProducerSealScheduleEnabled(env) {
		return nil
	}
	if len(opts.workloads) > 0 {
		for _, key := range []string{"account-manager", "billing", "video-cloud"} {
			if !lkeWorkloadSelected(env, opts, key) {
				return fmt.Errorf("OTA producer seal schedule requires coordinated account-manager, billing, and video-cloud deployment")
			}
		}
	}
	return lkeRequireOTAProducerSealSchedule(env)
}

func lkeBillingOTAProducerSealSecretFields(_ map[string]string) string {
	if token := lkeOTAProducerSealToken(); token != "" {
		// An orphaned historical Job may still need the endpoint after the
		// CronJob is disabled. Rotate this credential only after close review.
		return fmt.Sprintf("  BILLING_OTA_PRODUCER_SEAL_TOKEN: %q\n", token)
	}
	return ""
}

func lkeOTAProducerSealRuntimeSecretManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: ota-producer-seal-runtime
  namespace: %s
  labels:
    app.kubernetes.io/name: ota-producer-period-seal
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
type: Opaque
stringData:
  VIDEO_CLOUD_OTA_PRODUCER_SEAL_TOKEN: %q
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], lkeOTAProducerSealToken())
}

// Replays on days 3-7 tolerate a late, reviewed CDN delivery report without
// ever turning an absent review into a positive one.
func lkeOTAProducerSealCronJobManifest(env map[string]string) string {
	videoNS := lkeNamespaceName(env, "video-cloud")
	return fmt.Sprintf(`apiVersion: batch/v1
kind: CronJob
metadata:
  name: ota-producer-period-seal
  namespace: %s
  labels:
    app.kubernetes.io/name: ota-producer-period-seal
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  schedule: "0 4 3-7 * *"
  suspend: %s
  timeZone: Etc/UTC
  concurrencyPolicy: Forbid
  startingDeadlineSeconds: 7200
  successfulJobsHistoryLimit: 5
  failedJobsHistoryLimit: 12
  jobTemplate:
    spec:
      backoffLimit: 2
      activeDeadlineSeconds: 82800
      template:
        metadata:
          labels:
            app.kubernetes.io/name: ota-producer-period-seal
            app.kubernetes.io/part-of: rtk-cloud
        spec:
          restartPolicy: Never
          automountServiceAccountToken: false
          serviceAccountName: ota-producer-period-seal
          securityContext:
            runAsNonRoot: true
            runAsUser: 10001
            runAsGroup: 10001
            fsGroup: 10001
            seccompProfile: { type: RuntimeDefault }
          imagePullSecrets:
            - name: %s
          containers:
            - name: seal
              image: %s
              imagePullPolicy: IfNotPresent
              command: ["/app/otaseal"]
              args: ["--all-brand-clouds", "--month", "previous", "--first-month", %q]
              env:
                - name: VIDEO_CLOUD_ENV
                  value: %q
                - name: POSTGRES_PASSWORD
                  valueFrom:
                    secretKeyRef: { name: video-cloud-runtime, key: POSTGRES_PASSWORD }
                - name: VIDEO_CLOUD_DB_DSN
                  value: "postgres://postgres:$(POSTGRES_PASSWORD)@postgresql.%s.svc.cluster.local:5432/video_cloud?sslmode=disable"
                - name: VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_URL
                  value: "https://account-manager.%s.svc.cluster.local:8443"
%s
                - name: VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN
                  valueFrom:
                    secretKeyRef: { name: video-cloud-runtime, key: VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN }
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
                - name: VIDEO_CLOUD_OTA_DELIVERY_MODE
                  value: %q
                - name: AWS_ACCESS_KEY_ID
                  valueFrom:
                    secretKeyRef: { name: video-cloud-runtime, key: AWS_ACCESS_KEY_ID }
                - name: AWS_SECRET_ACCESS_KEY
                  valueFrom:
                    secretKeyRef: { name: video-cloud-runtime, key: AWS_SECRET_ACCESS_KEY }
                - name: VIDEO_CLOUD_BILLING_USAGE_ENDPOINT
                  value: "http://billing.%s.svc.cluster.local:80/v1/internal/billing/usage-facts"
                - name: VIDEO_CLOUD_BILLING_USAGE_TOKEN
                  valueFrom:
                    secretKeyRef: { name: video-cloud-workers-runtime, key: VIDEO_CLOUD_BILLING_USAGE_TOKEN }
                - name: VIDEO_CLOUD_OTA_PRODUCER_SEAL_TOKEN
                  valueFrom:
                    secretKeyRef: { name: ota-producer-seal-runtime, key: VIDEO_CLOUD_OTA_PRODUCER_SEAL_TOKEN }
              resources:
                requests: { cpu: 50m, memory: 128Mi }
                limits: { cpu: 500m, memory: 768Mi }
              volumeMounts:
                - { name: identity-lock, mountPath: /var/lib/ota-period-identity }
                - { name: identity-trust, mountPath: /etc/ota-period-identity, readOnly: true }
                - { name: kubernetes-api, mountPath: /var/run/ota-period-api, readOnly: true }
          volumes:
            - name: identity-lock
              emptyDir: { medium: Memory }
            - name: identity-trust
              secret:
                secretName: ota-producer-period-seal-identity
                defaultMode: 0440
                items:
                  - { key: account-manager-ca.crt, path: account-manager-ca.crt }
                  - { key: certissuer-ca.crt, path: certissuer-ca.crt }
            - name: kubernetes-api
              projected:
                defaultMode: 0440
                sources:
                  - serviceAccountToken: { path: token, expirationSeconds: 3600 }
                  - configMap:
                      name: kube-root-ca.crt
                      items: [{ key: ca.crt, path: ca.crt }]
`, videoNS, env["CLOUD_STACK_NAME"], strconv.FormatBool(!strings.EqualFold(env["LKE_OTA_PRODUCER_SEAL_SCHEDULE_SUSPENDED"], "false")), lkeImagePullSecretName(env), lkeVideoCloudImage(env), env["LKE_OTA_PRODUCER_SEAL_FIRST_MONTH"],
		firstNonEmpty(lkeEnvValue(env, "VIDEO_CLOUD_ENV"), env["CLOUD_ENV_NAME"], env["ACCOUNT_MANAGER_ENV"], "staging"),
		lkeNamespaceName(env, "platform"), lkeNamespaceName(env, "account-manager"), lkeOTAProducerIdentityEnv(env), env["VIDEO_CLOUD_BLOB_ENDPOINT"], env["VIDEO_CLOUD_BLOB_REGION"],
		env["VIDEO_CLOUD_BLOB_BUCKET"], env["VIDEO_CLOUD_BLOB_PREFIX"], firstNonEmpty(env["VIDEO_CLOUD_BLOB_FORCE_PATH_STYLE"], "false"),
		lkeOTADeliveryMode(env), lkeNamespaceName(env, "billing"))
}

func lkeOTAProducerIdentityEnv(env map[string]string) string {
	settings := [][2]string{
		{"VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_STATE", "/var/lib/ota-period-identity/state/identity.json"},
		{"VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_BACKEND", "kubernetes-secret"},
		{"VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_SECRET_NAMESPACE", lkeNamespaceName(env, "video-cloud")},
		{"VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_SECRET_NAME", otaProducerIdentitySecret},
		{"VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_SECRET_KEY", "identity.json"},
		{"VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_SECRET_UID", env["LKE_OTA_PRODUCER_IDENTITY_SECRET_UID"]},
		{"VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_K8S_TOKEN_FILE", "/var/run/ota-period-api/token"},
		{"VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_K8S_CA_FILE", "/var/run/ota-period-api/ca.crt"},
		{"VIDEO_CLOUD_ACCOUNT_MANAGER_IDENTITY_ROOT_SHA256", env["LKE_OTA_PRODUCER_IDENTITY_ROOT_SHA256"]},
		{"VIDEO_CLOUD_ACCOUNT_MANAGER_SERVER_PKI_ROOT_SHA256", env["LKE_OTA_PRODUCER_ACCOUNT_MANAGER_ROOT_SHA256"]},
		{"VIDEO_CLOUD_ACCOUNT_MANAGER_SERVER_PKI_NAME", "account-manager." + lkeNamespaceName(env, "account-manager") + ".svc.cluster.local"},
		{"VIDEO_CLOUD_ACCOUNT_MANAGER_TLS_CA", "/etc/ota-period-identity/account-manager-ca.crt"},
		{"VIDEO_CLOUD_ACCOUNT_MANAGER_RENEWAL_URL", env["LKE_OTA_PRODUCER_RENEWAL_URL"]},
		{"VIDEO_CLOUD_ACCOUNT_MANAGER_RENEWAL_SERVER_PKI_ROOT_SHA256", env["LKE_OTA_PRODUCER_RENEWAL_ROOT_SHA256"]},
		{"VIDEO_CLOUD_ACCOUNT_MANAGER_RENEWAL_SERVER_PKI_NAME", env["LKE_OTA_PRODUCER_RENEWAL_SERVER_NAME"]},
		{"VIDEO_CLOUD_ACCOUNT_MANAGER_RENEWAL_TLS_CA", "/etc/ota-period-identity/certissuer-ca.crt"},
	}
	var out strings.Builder
	for _, setting := range settings {
		fmt.Fprintf(&out, "                - name: %s\n                  value: %q\n", setting[0], setting[1])
	}
	return out.String()
}

func lkeOTAProducerIdentityRBACManifest(env map[string]string) string {
	ns := lkeNamespaceName(env, "video-cloud")
	return fmt.Sprintf(`apiVersion: v1
kind: ServiceAccount
metadata: { name: ota-producer-period-seal, namespace: %s }
automountServiceAccountToken: false
---
apiVersion: rbac.authorization.k8s.io/v1
kind: Role
metadata: { name: ota-producer-period-seal-identity, namespace: %s }
rules:
  - apiGroups: [""]
    resources: ["secrets"]
    resourceNames: ["ota-producer-period-seal-identity"]
    verbs: ["get", "patch"]
---
apiVersion: rbac.authorization.k8s.io/v1
kind: RoleBinding
metadata: { name: ota-producer-period-seal-identity, namespace: %s }
subjects: [{ kind: ServiceAccount, name: ota-producer-period-seal, namespace: %s }]
roleRef: { apiGroup: rbac.authorization.k8s.io, kind: Role, name: ota-producer-period-seal-identity }
`, ns, ns, ns, ns)
}

func lkeAllowOTABillingNetworkPolicyManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-ota-billing
  namespace: %s
  labels:
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: billing
  policyTypes: [Ingress]
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: %s
          podSelector:
            matchExpressions:
              - key: app.kubernetes.io/name
                operator: In
                values: [video-cloud-otaservice, ota-producer-period-seal]
      ports:
        - { protocol: TCP, port: 8080 }
`, lkeNamespaceName(env, "billing"), env["CLOUD_STACK_NAME"], lkeNamespaceName(env, "video-cloud"))
}
