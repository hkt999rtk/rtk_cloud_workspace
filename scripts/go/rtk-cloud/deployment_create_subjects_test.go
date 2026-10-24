package main

import (
	"slices"
	"testing"
)

func TestCreateSessionSubjectsFollowActualEnabledOwners(t *testing.T) {
	for _, environment := range []string{"dev", "staging"} {
		t.Run(environment, func(t *testing.T) {
			cfg := deploymentConfig{Environment: environment, Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + environment, "LKE_LOGGER_SERVICE_REGISTRATION_ENABLED": "false", "MQTT_FOUNDATION_REGISTRATION_ENABLED": "true", "OTA_SERVICE_REGISTRATION_ENABLED": "true", "SHADOW_WORKER_REGISTRATION_ENABLED": "true", "VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED": "false", "WEBRTC_SERVICE_REGISTRATION_ENABLED": "false"}}
			t.Setenv("LKE_LOGGER_SERVICE_REGISTRATION_ENABLED", "true")
			subjects, err := environmentCreateBootstrapSubjects(cfg)
			if err != nil {
				t.Fatal(err)
			}
			for _, subject := range []string{"service:certissuer", "service:pkibroker", "service:video-cloud-api", "service:video-cloud-logingester", "service:emqx-pki", "service:openbao"} {
				if !slices.Contains(subjects, subject) {
					t.Fatal("enabled owner omitted", subject)
				}
			}
			for _, subject := range []string{"service:mqtt", "service:ota", "service:shadow", "service:logger", "service:webrtc", "service:video-storage", "service:deployment-bootstrap"} {
				if slices.Contains(subjects, subject) {
					t.Fatal("uninstalled/provisioner owner reserved", subject)
				}
			}
			cfg.Values["CLOUD_STACK_NAME"] = "video-cloud-other"
			if _, err := environmentCreateBootstrapSubjects(cfg); err == nil {
				t.Fatal("another stack's native owners were reserved")
			}
		})
	}
	if _, err := environmentCreateBootstrapSubjects(deploymentConfig{Environment: "prod"}); err == nil {
		t.Fatal("unqualified production session accepted")
	}
}
