package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestMQTTInitializersRetainMountsAndUseFormalImagesAcrossEnvironments(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, bindings, release := completeEnvironmentRenderFixture(environment)
			var binding environmentPodBindings
			for _, b := range bindings.Pods {
				if b.Component == "managed-mqtt" {
					binding = b
				}
			}
			for _, name := range []string{"prepare-pki-state", "prepare-mqtt-host-state"} {
				binding.InitContainers = append(binding.InitContainers, environmentContainerBindings{Name: name, ImageKey: "LKE_VIDEO_CLOUD_IMAGE", Fields: map[string]any{}})
			}
			current, err := renderManagedEnvironmentComponent(env, binding)
			if err != nil {
				t.Fatal(err)
			}
			deploymentplan.Map(current["metadata"])["uid"] = "original-mqtt"
			for _, v := range certIssuerObjectList(environmentPKIPod(current)["containers"]) {
				c := deploymentplan.Map(v)
				role := "LKE_VIDEO_CLOUD_IMAGE"
				if c["name"] == "mqtt" {
					role = "LKE_MQTT_PKI_IMAGE"
				}
				c["image"] = release.Images[role].Reference
			}
			for _, v := range certIssuerObjectList(environmentPKIPod(current)["initContainers"]) {
				deploymentplan.Map(v)["image"] = "ghcr.io/hkt999rtk/rtk_cloud_dev/video-cloud-api@sha256:" + strings.Repeat("d", 64)
			}
			observed, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{current})
			if err != nil {
				t.Fatal(err)
			}
			if len(observed.Pods[0].InitContainers) != 2 {
				t.Fatal("original initializers not observed")
			}
			if err := validateEnvironmentIdentityBindings(observed, environment); err != nil {
				t.Fatal(err)
			}
			target, err := renderManagedEnvironmentComponent(env, observed.Pods[0])
			if err != nil {
				t.Fatal(err)
			}
			if err := applyEnvironmentPodBindings(target, observed, release); err != nil {
				t.Fatal(err)
			}
			if err := validateEnvironmentPKITransition([]deploymentplan.Definition{{Object: target}}, []deploymentplan.Object{current}); err != nil {
				t.Fatal(err)
			}
			for _, v := range certIssuerObjectList(environmentPKIPod(target)["initContainers"]) {
				if deploymentplan.Map(v)["image"] != release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference {
					t.Fatal("initializer not formally pinned")
				}
			}
			for _, scenario := range []string{"unknown-name", "unsafe-script", "foreign-image"} {
				bad := deploymentplan.Clone(current)
				c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(bad)["initContainers"])[0])
				switch scenario {
				case "unknown-name":
					c["name"] = "unknown-initializer"
				case "unsafe-script":
					c["command"] = []any{"/bin/sh", "-ec", "rm -rf /run/pki-state"}
				case "foreign-image":
					c["image"] = "foreign.example/initializer:latest"
				}
				if _, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{bad}); err == nil {
					t.Fatal("unqualified initializer accepted", scenario)
				}
			}
		})
	}
}
