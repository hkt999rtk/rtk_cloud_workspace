package main

import (
	"errors"
	"fmt"
	"net/url"
	"strings"
)

func lkeOTAPlatformSealScheduleEnabled(env map[string]string) bool {
	return lkeFeatureEnabled(env, "LKE_OTA_PLATFORM_SEAL_SCHEDULE_ENABLED")
}

func lkeOTAPlatformSealBaseURL(env map[string]string) string {
	return "https://" + lkeBillingPublicDomain(env)
}

func lkeOTAPlatformSealToken() string {
	if activeCanonicalSecretStore {
		return lkeRuntimeSecretValue("ota-platform-seal-token")
	}
	// Optional credentials must not be synthesized by the disposable test and
	// development fallback used for required runtime secrets.
	return lkeRuntimeSecretCache["ota-platform-seal-token"]
}

func lkeRequireOTAPlatformSealSchedule(env map[string]string) error {
	if !lkeOTAPlatformSealScheduleEnabled(env) {
		return nil
	}
	token := lkeOTAPlatformSealToken()
	if len(token) < 32 || token == lkeBillingServiceToken() || token == lkeBillingInternalToken() {
		return errors.New("OTA Platform seal schedule requires a distinct canonical token of at least 32 characters")
	}
	endpoint, err := url.Parse(lkeOTAPlatformSealBaseURL(env))
	if err != nil || endpoint.Scheme != "https" || endpoint.Host == "" || endpoint.User != nil || endpoint.RawQuery != "" || endpoint.Fragment != "" || strings.Contains(endpoint.Host, " ") || !strings.Contains(endpoint.Hostname(), ".") || strings.HasSuffix(endpoint.Hostname(), ".") {
		return errors.New("OTA Platform seal schedule requires the staging-owned HTTPS Billing domain")
	}
	if lkeAccountManagerImage(env) == "" {
		return errors.New("OTA Platform seal schedule requires a selected Account Manager image")
	}
	return nil
}

func lkeBillingOTAPlatformSealSecretFields(_ map[string]string) string {
	if token := lkeOTAPlatformSealToken(); token != "" {
		// Keep the endpoint credential while an orphaned Job finishes after the
		// schedule is disabled. Revocation is a separate period-closure decision.
		return fmt.Sprintf("  BILLING_OTA_PLATFORM_SEAL_TOKEN: %q\n", token)
	}
	return ""
}

func lkeOTAPlatformSealRuntimeSecretManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: v1
kind: Secret
metadata:
  name: ota-platform-seal-runtime
  namespace: %s
  labels:
    app.kubernetes.io/name: ota-platform-seal
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
type: Opaque
stringData:
  DATABASE_URL: %q
  BILLING_OTA_PERIOD_SEAL_BASE_URL: %q
  BILLING_OTA_PLATFORM_SEAL_TOKEN: %q
`, lkeNamespaceName(env, "account-manager"), env["CLOUD_STACK_NAME"], lkeAccountManagerDatabaseURL(env), lkeOTAPlatformSealBaseURL(env), lkeOTAPlatformSealToken())
}

// The batch command retries exact deterministic seals. Scheduling on day three
// allows the previous UTC month to close before the producer lateness window.
func lkeOTAPlatformSealCronJobManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: batch/v1
kind: CronJob
metadata:
  name: ota-platform-period-seal
  namespace: %s
  labels:
    app.kubernetes.io/name: ota-platform-period-seal
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  schedule: "0 3 3 * *"
  timeZone: Etc/UTC
  concurrencyPolicy: Forbid
  startingDeadlineSeconds: 86400
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 12
  jobTemplate:
    spec:
      backoffLimit: 3
      activeDeadlineSeconds: 86400
      template:
        metadata:
          labels:
            app.kubernetes.io/name: ota-platform-period-seal
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
              command: ["/app/rtk-account-manager-ota-period-seal"]
              args: ["--all-brand-clouds", "--month", "previous", "--submit"]
              envFrom:
                - secretRef: { name: ota-platform-seal-runtime }
              resources:
                requests: { cpu: 25m, memory: 64Mi }
                limits: { cpu: 250m, memory: 256Mi }
`, lkeNamespaceName(env, "account-manager"), env["CLOUD_STACK_NAME"], lkeImagePullSecretName(env), lkeAccountManagerImage(env))
}
