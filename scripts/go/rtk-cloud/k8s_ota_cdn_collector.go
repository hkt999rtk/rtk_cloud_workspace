package main

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"
)

func lkeOTACDNCollectorEnabled(env map[string]string) bool {
	return lkeFeatureEnabled(env, "LKE_OTA_CDN_COLLECTOR_ENABLED")
}

func lkeRequireOTACDNCollectorDeployment(env map[string]string, opts provisionOptions) error {
	if !lkeOTACDNCollectorEnabled(env) {
		return nil
	}
	if len(opts.workloads) > 0 && !lkeWorkloadSelected(env, opts, "video-cloud") {
		return fmt.Errorf("OTA CDN collector requires the selected Video Cloud workload")
	}
	for _, key := range []string{"VIDEO_CLOUD_OTA_CDN_STREAM_ID", "VIDEO_CLOUD_OTA_CDN_HOST", "VIDEO_CLOUD_OTA_CDN_PATH_ROOT", "VIDEO_CLOUD_OTA_CDN_LOG_BUCKET", "VIDEO_CLOUD_OTA_CDN_LOG_PREFIX", "VIDEO_CLOUD_OTA_CDN_LOG_REGION"} {
		if strings.TrimSpace(env[key]) == "" {
			return fmt.Errorf("OTA CDN collector requires %s", key)
		}
	}
	if !strings.HasPrefix(env["VIDEO_CLOUD_OTA_CDN_PATH_ROOT"], "/") || strings.ContainsAny(env["VIDEO_CLOUD_OTA_CDN_PATH_ROOT"], "?#") {
		return fmt.Errorf("OTA CDN collector requires an absolute URL path root")
	}
	if strings.ContainsAny(env["VIDEO_CLOUD_OTA_CDN_HOST"], "/?# ") {
		return fmt.Errorf("OTA CDN collector requires an exact request host")
	}
	if _, err := strconv.ParseBool(firstNonEmpty(env["VIDEO_CLOUD_OTA_CDN_LOG_PATH_STYLE"], "false")); err != nil {
		return fmt.Errorf("OTA CDN collector requires a Boolean path-style setting")
	}
	endpoint := strings.TrimSpace(env["VIDEO_CLOUD_OTA_CDN_LOG_ENDPOINT"])
	u, err := url.Parse(endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("OTA CDN collector requires an HTTPS S3 endpoint")
	}
	if lkeVideoCloudImage(env) == "" {
		return fmt.Errorf("OTA CDN collector requires a selected Video Cloud image")
	}
	ns := lkeNamespaceName(env, "video-cloud")
	for _, required := range []struct {
		name string
		keys []string
	}{
		{name: "video-cloud-runtime", keys: []string{"POSTGRES_PASSWORD"}},
		{name: "ota-cdn-datastream-reader", keys: []string{"AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"}},
	} {
		secretName, keys := required.name, required.keys
		secret, err := kubectlResourceJSON(ns, "secret", secretName)
		if err != nil {
			return fmt.Errorf("OTA CDN collector Secret %s unavailable: %w", secretName, err)
		}
		for _, key := range keys {
			value, err := kubernetesSecretBytes(secret, key)
			if err != nil || strings.TrimSpace(string(value)) == "" {
				return fmt.Errorf("OTA CDN collector Secret %s lacks %s", secretName, key)
			}
		}
	}
	return nil
}

func lkeOTACDNCollectorCronJobManifest(env map[string]string) string {
	return fmt.Sprintf(`apiVersion: batch/v1
kind: CronJob
metadata:
  name: ota-cdn-collector
  namespace: %s
  labels:
    app.kubernetes.io/name: ota-cdn-collector
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  schedule: "*/5 * * * *"
  timeZone: Etc/UTC
  concurrencyPolicy: Forbid
  startingDeadlineSeconds: 300
  successfulJobsHistoryLimit: 3
  failedJobsHistoryLimit: 12
  jobTemplate:
    spec:
      backoffLimit: 2
      activeDeadlineSeconds: 1800
      template:
        metadata:
          labels:
            app.kubernetes.io/name: ota-cdn-collector
            app.kubernetes.io/part-of: rtk-cloud
        spec:
          restartPolicy: Never
          automountServiceAccountToken: false
          imagePullSecrets:
            - name: %s
          containers:
            - name: collector
              image: %s
              imagePullPolicy: IfNotPresent
              command: ["/app/otacdncollect"]
              args: ["-path-style=%s"]
              env:
                - name: POSTGRES_PASSWORD
                  valueFrom:
                    secretKeyRef: { name: video-cloud-runtime, key: POSTGRES_PASSWORD }
                - name: VIDEO_CLOUD_DB_DSN
                  value: "postgres://postgres:$(POSTGRES_PASSWORD)@postgresql.%s.svc.cluster.local:5432/video_cloud?sslmode=disable"
                - name: VIDEO_CLOUD_OTA_CDN_STREAM_ID
                  value: %q
                - name: VIDEO_CLOUD_OTA_CDN_HOST
                  value: %q
                - name: VIDEO_CLOUD_OTA_CDN_PATH_ROOT
                  value: %q
                - name: VIDEO_CLOUD_OTA_CDN_LOG_BUCKET
                  value: %q
                - name: VIDEO_CLOUD_OTA_CDN_LOG_PREFIX
                  value: %q
                - name: VIDEO_CLOUD_OTA_CDN_LOG_REGION
                  value: %q
                - name: VIDEO_CLOUD_OTA_CDN_LOG_ENDPOINT
                  value: %q
                - name: AWS_ACCESS_KEY_ID
                  valueFrom:
                    secretKeyRef: { name: ota-cdn-datastream-reader, key: AWS_ACCESS_KEY_ID }
                - name: AWS_SECRET_ACCESS_KEY
                  valueFrom:
                    secretKeyRef: { name: ota-cdn-datastream-reader, key: AWS_SECRET_ACCESS_KEY }
              resources:
                requests: { cpu: 100m, memory: 256Mi }
                limits: { cpu: 1, memory: 1Gi }
`, lkeNamespaceName(env, "video-cloud"), env["CLOUD_STACK_NAME"], lkeImagePullSecretName(env), lkeVideoCloudImage(env),
		firstNonEmpty(env["VIDEO_CLOUD_OTA_CDN_LOG_PATH_STYLE"], "false"), lkeNamespaceName(env, "platform"),
		env["VIDEO_CLOUD_OTA_CDN_STREAM_ID"], env["VIDEO_CLOUD_OTA_CDN_HOST"], env["VIDEO_CLOUD_OTA_CDN_PATH_ROOT"],
		env["VIDEO_CLOUD_OTA_CDN_LOG_BUCKET"], env["VIDEO_CLOUD_OTA_CDN_LOG_PREFIX"], env["VIDEO_CLOUD_OTA_CDN_LOG_REGION"], env["VIDEO_CLOUD_OTA_CDN_LOG_ENDPOINT"])
}
