package main

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedTestLabRequiresActualImageCallbackCapabilityOnlyWhenEnabled(t *testing.T) {
	_, _, release := completeEnvironmentRenderFixture("dev")
	if err := verifyManagedTestLabCapability(t.Context(), release, false, nil); err != nil {
		t.Fatal("disabled feature added a capability gate", err)
	}
	if slices.Contains(managedMQTTRequiredCapabilities(false), "managed-test-lab-callback-v1") || !slices.Contains(managedMQTTRequiredCapabilities(true), "managed-test-lab-callback-v1") {
		t.Fatal("fresh capability selection differs from exact feature intent")
	}
	for _, mode := range []string{"pass", "old-image", "foreign-service", "malformed", "private-error", "unqualified-image", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			current := release
			if mode == "unqualified-image" {
				current.Images = map[string]environmentReleaseImage{"LKE_MQTT_PKI_IMAGE": {Reference: "mutable:latest"}}
			}
			run := func(ctx context.Context, args ...string) ([]byte, error) {
				if !slices.Contains(args, "/usr/local/bin/emqxpkihost") || !slices.Contains(args, release.Images["LKE_MQTT_PKI_IMAGE"].Reference) || !slices.Contains(args, "none") {
					t.Fatal("queried another image or a live runtime")
				}
				features := managedMQTTRequiredCapabilities(true)
				service := "emqxpkihost"
				switch mode {
				case "old-image":
					features = managedMQTTRequiredCapabilities(false)
				case "foreign-service":
					service = "foreign"
				case "malformed":
					return []byte("bad-json"), nil
				case "private-error":
					return nil, errors.New("private-tool-output-canary")
				}
				return json.Marshal(deploymentplan.Object{"schema": "rtk-cloud.deployment-capabilities/v1", "service": service, "features": features})
			}
			ctx := t.Context()
			if mode == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err := verifyManagedTestLabCapability(ctx, current, true, run)
			if (err == nil) != (mode == "pass") {
				t.Fatal("actual native capability requirement differs", err)
			}
			if err != nil && strings.Contains(err.Error(), "canary") {
				t.Fatal("private subprocess output escaped")
			}
		})
	}
}
