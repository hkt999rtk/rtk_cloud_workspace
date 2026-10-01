package main

import (
	"errors"
	"fmt"
	"strings"
)

func lkeLoggerProducerSealToken() string {
	if activeCanonicalSecretStore {
		return lkeRuntimeSecretValue("logger-producer-seal-token")
	}
	return lkeRuntimeSecretCache["logger-producer-seal-token"]
}

func lkeRequireLoggerProducerSealToken(env map[string]string) error {
	if !lkeLoggerBillingFactsEnabled(env) {
		return nil
	}
	if !lkeLoggerPeriodSealsEnabled(env) {
		return errors.New("Logger billing requires the immutable monthly source seal path")
	}
	token := lkeLoggerProducerSealToken()
	if len(token) < 32 || strings.TrimSpace(token) != token || strings.ContainsAny(token, " \t\r\n") ||
		token == lkeBillingServiceToken() || token == lkeBillingInternalToken() ||
		token == lkeOTAPlatformSealToken() || token == lkeOTAProducerSealToken() || token == lkeInternalAuthToken() {
		return errors.New("Logger billing requires a distinct canonical producer-seal token of at least 32 characters")
	}
	return nil
}

func lkeBillingLoggerProducerSealSecretFields(_ map[string]string) string {
	if token := lkeLoggerProducerSealToken(); token != "" {
		return fmt.Sprintf("  LOGGER_PRODUCER_SEAL_TOKEN: %q\n", token)
	}
	return ""
}

func lkeLoggerProducerSealWorkerSecretFields(_ map[string]string) string {
	if token := lkeLoggerProducerSealToken(); token != "" {
		return fmt.Sprintf("  VIDEO_CLOUD_LOGGER_PRODUCER_SEAL_TOKEN: %q\n", token)
	}
	return ""
}

func lkeLoggerProducerSealWorkerEnv(env map[string]string) string {
	if !lkeLoggerBillingFactsEnabled(env) {
		return ""
	}
	return fmt.Sprintf(`            - name: VIDEO_CLOUD_LOGGER_PERIOD_SEALS_ENABLED
              value: %q
            - name: VIDEO_CLOUD_LOGGER_PRODUCER_SEAL_TOKEN
              valueFrom:
                secretKeyRef:
                  name: video-cloud-workers-runtime
                  key: VIDEO_CLOUD_LOGGER_PRODUCER_SEAL_TOKEN
`, fmt.Sprint(lkeLoggerPeriodSealsEnabled(env)))
}

// No CronJob is installed: the environment operator starts each reviewed close.
// Retries use the same immutable source period and fact IDs in PostgreSQL.
func lkeLoggerPeriodSealJobManifest(env map[string]string, month, name string) string {
	return fmt.Sprintf(`apiVersion: batch/v1
kind: Job
metadata:
  name: %s
  namespace: %s
  labels:
    app.kubernetes.io/name: logger-period-seal
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/stack: %s
spec:
  backoffLimit: 2
  activeDeadlineSeconds: 3600
  ttlSecondsAfterFinished: 604800
  template:
    metadata:
      labels:
        app.kubernetes.io/name: logger-period-seal
        app.kubernetes.io/part-of: rtk-cloud
    spec:
      restartPolicy: Never
      automountServiceAccountToken: false
      imagePullSecrets:
        - name: %s
      containers:
        - name: seal
          image: %s
          imagePullPolicy: IfNotPresent
          command: ["/app/loggerperiodseal"]
          args: ["--all-brand-clouds", "--month", %q]
          env:
            - name: VIDEO_CLOUD_ENV
              value: %q
            - name: VIDEO_CLOUD_LOGGER_SERVICE_ENABLED
              value: "true"
            - name: VIDEO_CLOUD_LOGGER_BILLING_FACTS_ENABLED
              value: "true"
            - name: VIDEO_CLOUD_LOGGER_PERIOD_SEALS_ENABLED
              value: "true"
            - name: POSTGRES_PASSWORD
              valueFrom:
                secretKeyRef: { name: video-cloud-runtime, key: POSTGRES_PASSWORD }
            - name: VIDEO_CLOUD_DB_DSN
              value: "postgres://postgres:$(POSTGRES_PASSWORD)@postgresql.%s.svc.cluster.local:5432/video_cloud?sslmode=disable"
            - name: VIDEO_CLOUD_DB_ENSURE_SCHEMA
              value: "false"
            - name: VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_URL
              value: %q
            - name: VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN
              valueFrom:
                secretKeyRef: { name: video-cloud-runtime, key: VIDEO_CLOUD_ACCOUNT_MANAGER_INTERNAL_TOKEN }
            - name: VIDEO_CLOUD_BILLING_USAGE_ENDPOINT
              value: "http://billing.%s.svc.cluster.local:80/v1/internal/billing/usage-facts"
            - name: VIDEO_CLOUD_BILLING_USAGE_TOKEN
              valueFrom:
                secretKeyRef: { name: video-cloud-workers-runtime, key: VIDEO_CLOUD_BILLING_USAGE_TOKEN }
            - name: VIDEO_CLOUD_LOGGER_PRODUCER_SEAL_TOKEN
              valueFrom:
                secretKeyRef: { name: video-cloud-workers-runtime, key: VIDEO_CLOUD_LOGGER_PRODUCER_SEAL_TOKEN }
          resources:
            requests: { cpu: 50m, memory: 128Mi }
            limits: { cpu: 500m, memory: 512Mi }
`, name, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], lkeImagePullSecretName(env),
		lkeVideoCloudImage(env), month, env["CLOUD_ENV_NAME"], lkeNamespaceName(env, "platform"),
		lkeAccountManagerInternalURL(env), lkeNamespaceName(env, "billing"))
}
