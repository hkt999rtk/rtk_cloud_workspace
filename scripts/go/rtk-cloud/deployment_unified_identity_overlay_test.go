package main

import (
	"reflect"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"testing"
)

func TestObservedBrokerIdentityDoesNotGainBootstrapCredentialSources(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			env, bindings, release := completeEnvironmentRenderFixture(environment)
			var binding environmentPodBindings
			for _, b := range bindings.Pods {
				if b.Component == "managed-mqtt" {
					binding = b
				}
			}
			current, err := renderManagedEnvironmentComponent(env, binding)
			if err != nil {
				t.Fatal(err)
			}
			primary := managedNamedField(environmentPKIPod(current)["containers"], "mqtt")
			primary["image"] = release.Images["LKE_MQTT_PKI_IMAGE"].Reference
			broker := managedNamedField(environmentPKIPod(current)["containers"], "pkibroker")
			broker["image"] = release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference
			var settings []any
			for _, v := range certIssuerObjectList(broker["env"]) {
				if deploymentplan.Map(v)["name"] != "PKI_DATABASE_URL" {
					settings = append(settings, v)
				}
			}
			broker["env"] = settings
			broker["envFrom"] = []any{map[string]any{"secretRef": map[string]any{"name": "original-broker-credentials"}}}
			deploymentplan.Map(current["metadata"])["uid"] = "original-mqtt-owner"
			observed, err := observeEnvironmentBindings(environment, release, []deploymentplan.Object{current})
			if err != nil {
				t.Fatal(err)
			}
			desired, err := renderManagedEnvironmentComponent(env, observed.Pods[0])
			if err != nil {
				t.Fatal(err)
			}
			if err := applyEnvironmentPodBindings(desired, observed, release); err != nil {
				t.Fatal(err)
			}
			wanted := managedNamedField(environmentPKIPod(desired)["containers"], "pkibroker")
			if managedNamedField(wanted["env"], "PKI_DATABASE_URL") != nil || !reflect.DeepEqual(wanted["envFrom"], broker["envFrom"]) {
				t.Fatal("bootstrap default masks original inherited credentials")
			}
			if err := validateEnvironmentPKITransition([]deploymentplan.Definition{{Object: desired}}, []deploymentplan.Object{current}); err != nil {
				t.Fatal(err)
			}
			delete(broker, "envFrom")
			observed, err = observeEnvironmentBindings(environment, release, []deploymentplan.Object{current})
			if err != nil {
				t.Fatal(err)
			}
			desired, err = renderManagedEnvironmentComponent(env, observed.Pods[0])
			if err != nil {
				t.Fatal(err)
			}
			if err := applyEnvironmentPodBindings(desired, observed, release); err != nil {
				t.Fatal(err)
			}
			wanted = managedNamedField(environmentPKIPod(desired)["containers"], "pkibroker")
			if wanted["envFrom"] != nil {
				t.Fatal("absent inherited credentials were initialized on upgrade")
			}
			if err := validateEnvironmentPKITransition([]deploymentplan.Definition{{Object: desired}}, []deploymentplan.Object{current}); err != nil {
				t.Fatal(err)
			}
		})
	}
}
