package main

import (
	"strings"
	"testing"
)

func TestLKETestLabIsOptInAndNeverProduction(t *testing.T) {
	t.Setenv("TEST_LAB_ENABLED", "")
	for _, tc := range []struct {
		stack, enabled string
		want           bool
	}{{"video-cloud-dev", "true", true}, {"video-cloud-staging", "true", true}, {"video-cloud-production", "true", false}, {"video-cloud-dev", "false", false}} {
		env := map[string]string{"CLOUD_STACK_NAME": tc.stack, "TEST_LAB_ENABLED": tc.enabled, "VIDEO_CLOUD_DOMAIN": "video.example.test", "CLOUD_ADMIN_DOMAIN": "console.example.test"}
		if got := lkeTestLabEnabled(env); got != tc.want {
			t.Fatalf("gate %s/%s=%v", tc.stack, tc.enabled, got)
		}
		if tc.want {
			if !strings.Contains(lkeEMQXTenantBaseHOCON(env), `bind = "0.0.0.0:8085"`) || !strings.Contains(lkeMQTTServiceManifest(env), "targetPort: 8085") {
				t.Fatal("test listener must not collide with EMQX default websocket port 8083")
			}
			for _, key := range []string{"account-manager", "video-cloud", "cloud-admin"} {
				manifest := lkeDeploymentManifest(env, lkeWorkload{Key: key, Name: key, Host: "console.example.test", Image: "example.test/dev:test"}, nil)
				if !strings.Contains(manifest, "TEST_LAB_ENABLED") {
					t.Fatalf("missing runtime gate for %s", key)
				}
			}
		}
		for _, manifest := range []string{lkeMQTTServiceManifest(env), lkeEMQXTenantBaseHOCON(env), lkeEMQXListenerAuthEnvManifest(env)} {
			contains := strings.Contains(strings.ToLower(manifest), "testlab")
			if contains != tc.want {
				t.Fatalf("unexpected test listener exposure for %s", tc.stack)
			}
		}
	}
}
