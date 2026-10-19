package main

import (
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// These original supported integrations authenticate as service IDs, not Pod
// names. Validate the candidate before its policy can deny retained consumers.
func checkEnvironmentCRLConsumerPolicy(p *deploymentplan.Plan) error {
	controller := map[string]string{}
	for _, r := range p.Resources {
		if r.ID == "Deployment/"+p.Stack+"-video-cloud/pki-controller" && r.Action != "retire" {
			for _, raw := range certIssuerObjectList(environmentPKIPod(r.Desired)["containers"]) {
				c := deploymentplan.Map(raw)
				if c["name"] == "pki-controller" {
					for _, setting := range certIssuerObjectList(c["env"]) {
						e := deploymentplan.Map(setting)
						controller[deploymentplan.String(e["name"])] = deploymentplan.String(e["value"])
					}
				}
			}
		}
	}
	for _, r := range p.Resources {
		if r.Action == "retire" || deploymentplan.Map(r.Desired["spec"])["replicas"] == float64(0) {
			continue
		}
		for _, raw := range certIssuerObjectList(environmentPKIPod(r.Desired)["containers"]) {
			c := deploymentplan.Map(raw)
			var requirements map[string]string
			consumer := ""
			switch {
			case r.ID == "Deployment/"+p.Stack+"-account-manager/account-manager" && c["name"] == "pkimanagement":
				consumer = "account-manager"
				requirements = map[string]string{"PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_SERVER_CRL_MANIFEST": "PKI_REQUIRED_CONSUMERS_SERVICE", "PKI_MANAGEMENT_ACCOUNT_SERVICE_CLIENT_BUNDLE_MANIFEST": "PKI_REQUIRED_BUNDLE_CONSUMERS_SERVICE"}
			case r.ID == "Deployment/"+p.Stack+"-video-cloud/video-cloud-logingester" && c["name"] == "app":
				consumer = "video-cloud-logingester"
				requirements = map[string]string{"VIDEO_CLOUD_LOG_INGESTER_MQTT_IDENTITY_SERVER_CRL_MANIFEST": "PKI_REQUIRED_CONSUMERS_SERVICE", "VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST": "PKI_REQUIRED_CONSUMERS_MQTT"}
			case managedMQTTConsumerClaim(r.Desired, c) != "" && mqttFoundationEnv(c, "VIDEO_CLOUD_MQTT_SERVER_PKI_ROOT_SHA256") != "":
				consumer = "video-cloud-api"
				if c["name"] == "mqttfoundation" {
					consumer = "mqtt"
				}
				if c["name"] == "shadowworker" {
					consumer = "shadow"
				}
				requirements = map[string]string{"VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST": "PKI_REQUIRED_CONSUMERS_MQTT", "VIDEO_CLOUD_MQTT_SERVER_BUNDLE_MANIFEST": "PKI_REQUIRED_CONSUMERS_MQTT"}
			default:
				continue
			}
			for _, rawSetting := range certIssuerObjectList(c["env"]) {
				setting := deploymentplan.Map(rawSetting)
				policy := requirements[deploymentplan.String(setting["name"])]
				if policy == "" || deploymentplan.String(setting["value"]) == "" && setting["valueFrom"] == nil {
					continue
				}
				value := controller[policy]
				if value == "" && strings.Contains(policy, "_BUNDLE_CONSUMERS_") {
					value = controller[strings.Replace(policy, "_BUNDLE_CONSUMERS_", "_CONSUMERS_", 1)]
				}
				found := false
				for _, id := range strings.Split(value, ",") {
					found = found || strings.TrimSpace(id) == consumer
				}
				if !found {
					return &deploymentplan.Blocker{ID: "deployment.pki.consumer-policy", Resource: r.ID, Cause: policy + " excludes required original consumer " + consumer, Next: "repair the declared consumer policy while preserving original identities, then verify actual CRL synchronization and acknowledgment and resume the recorded target; qualify again only if deployment intent changes"}
				}
			}
		}
	}
	return nil
}
