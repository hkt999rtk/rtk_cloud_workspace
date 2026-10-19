package main

import (
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCandidateCRLPolicyIncludesOriginalReadersBeforeDeployment(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "missing-service-reader", "missing-mqtt-reader", "missing-bundle-reader", "bundle-fallback"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				stack := "video-cloud-" + environment
				p := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: stack, Operation: "upgrade"}}
				add := func(namespace, name, container string, settings map[string]string) {
					var env []any
					for k, v := range settings {
						env = append(env, deploymentplan.Object{"name": k, "value": v})
					}
					p.Resources = append(p.Resources, deploymentplan.Resource{ID: "Deployment/" + namespace + "/" + name, Action: "update", Desired: deploymentplan.Object{"spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": container, "env": env}}}}}}})
				}
				policy := map[string]string{"PKI_REQUIRED_CONSUMERS_SERVICE": "account-manager,certissuer,pki-controller,video-cloud-logingester", "PKI_REQUIRED_CONSUMERS_MQTT": "video-cloud-api,video-cloud-logingester", "PKI_REQUIRED_BUNDLE_CONSUMERS_SERVICE": "account-manager,certissuer,pki-controller"}
				switch mode {
				case "missing-service-reader":
					policy["PKI_REQUIRED_CONSUMERS_SERVICE"] = "certissuer,pki-controller"
				case "missing-mqtt-reader":
					policy["PKI_REQUIRED_CONSUMERS_MQTT"] = "video-cloud-api"
				case "missing-bundle-reader":
					policy["PKI_REQUIRED_BUNDLE_CONSUMERS_SERVICE"] = "certissuer,pki-controller"
				case "bundle-fallback":
					policy["PKI_REQUIRED_BUNDLE_CONSUMERS_SERVICE"] = ""
				}
				add(stack+"-video-cloud", "pki-controller", "pki-controller", policy)
				add(stack+"-account-manager", "account-manager", "pkimanagement", map[string]string{"PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST": "/original/crls.json", "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_BUNDLE_MANIFEST": "/original/issuers.json"})
				add(stack+"-video-cloud", "video-cloud-logingester", "app", map[string]string{"VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST": "/original/mqtt/crls.json", "VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_SERVER_CRL_MANIFEST": "/original/service/crls.json"})
				err := checkEnvironmentCRLConsumerPolicy(p)
				if (err == nil) != (mode == "valid" || mode == "bundle-fallback") {
					t.Fatalf("incorrect candidate policy qualification: %v", err)
				}
			})
		}
	}
}
