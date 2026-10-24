package main

import (
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCurrentMQTTBindingsRejectForeignCurrentProfiles(t *testing.T) {
	for _, mode := range []string{"run-as", "fs-group", "supplemental-group", "foreign-claim", "foreign-secret", "foreign-affinity", "foreign-public-root", "missing-original-claim"} {
		t.Run(mode, func(t *testing.T) {
			env, release, live := currentMQTTBindingsFixture(t)
			var worker deploymentplan.Object
			for _, o := range live {
				if deploymentplan.Map(o["metadata"])["name"] == "video-cloud-mqttfoundation" {
					worker = o
				}
			}
			pod := environmentPKIPod(worker)
			switch mode {
			case "run-as":
				deploymentplan.Map(pod["securityContext"])["runAsUser"] = float64(0)
			case "fs-group":
				deploymentplan.Map(pod["securityContext"])["fsGroup"] = float64(10001)
			case "supplemental-group":
				deploymentplan.Map(pod["securityContext"])["supplementalGroups"] = []any{float64(27)}
			case "foreign-claim":
				v := managedNamedField(pod["volumes"], "mqtt-bundle-state")
				deploymentplan.Map(v["persistentVolumeClaim"])["claimName"] = "foreign-identity"
			case "foreign-secret":
				v := managedNamedField(pod["volumes"], "platform-service-identity")
				deploymentplan.Map(v["secret"])["secretName"] = "foreign-identity"
			case "foreign-affinity":
				term := deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(deploymentplan.Map(pod["affinity"])["podAffinity"])["requiredDuringSchedulingIgnoredDuringExecution"])[0])
				term["namespaces"] = []any{"foreign-video-cloud"}
			case "foreign-public-root":
				for _, o := range live {
					if o["kind"] == "ConfigMap" && deploymentplan.Map(o["metadata"])["name"] == "mqtt-pki-bundles" {
						deploymentplan.Map(o["data"])["roots.pem"] = "foreign-public-root"
					}
				}
			case "missing-original-claim":
				var keep []deploymentplan.Object
				for _, o := range live {
					if deploymentplan.ID(o) != "PersistentVolumeClaim/video-cloud-dev-video-cloud/pki-product-mqtt-identity" {
						keep = append(keep, o)
					}
				}
				live = keep
			}
			if _, err := observeEnvironmentTargetBindings(t.Context(), deploymentConfig{Environment: "dev", Values: env}, release, "upgrade", live); err == nil {
				t.Fatal("foreign current MQTT profile accepted", mode)
			}
		})
	}
}

func TestCurrentMQTTBindingsSerializedProfileCannotChangeOwnerOrPlacement(t *testing.T) {
	env, release, live := currentMQTTBindingsFixture(t)
	b, err := observeEnvironmentTargetBindings(t.Context(), deploymentConfig{Environment: "dev", Values: env}, release, "upgrade", live)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"owner", "foreign-worker", "extra-affinity"} {
		t.Run(mode, func(t *testing.T) {
			copy := b
			copy.Pods = append([]environmentPodBindings(nil), b.Pods...)
			for i := range copy.Pods {
				p := &copy.Pods[i]
				if p.ManagedMQTT == nil {
					continue
				}
				profile := *p.ManagedMQTT
				profile.Affinity = deploymentplan.Clone(profile.Affinity)
				p.ManagedMQTT = &profile
				switch mode {
				case "owner":
					p.ManagedMQTT.OwnerUID = "99999999-9999-4999-8999-999999999999"
				case "foreign-worker":
					p.Name = "foreign-worker"
				case "extra-affinity":
					profile.Affinity["nodeAffinity"] = deploymentplan.Object{}
				}
				break
			}
			if validateEnvironmentIdentityBindings(copy, "dev") == nil {
				t.Fatal("serialized protected MQTT profile changed", mode)
			}
		})
	}
}
