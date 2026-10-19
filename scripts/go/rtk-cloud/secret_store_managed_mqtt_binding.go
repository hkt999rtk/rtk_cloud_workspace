package main

import (
	"encoding/json"
	"errors"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Resolve the canonical dashboard mirror from the actual managed consumer, not
// by treating an absent legacy TLS Secret as permission to invent a replacement.
func secretStoreMQTTDashboardBinding(store secretStore, original secretK8SBinding) (secretK8SBinding, error) {
	stack := "video-cloud-" + store.Environment
	id := "Deployment/" + stack + "-video-cloud/mqtt-pki"
	var object deploymentplan.Object
	if store.checkRuntime != nil && store.checkRuntime.preflightPlan != nil {
		p := store.checkRuntime.preflightPlan
		if p.Environment != store.Environment || p.Stack != stack {
			return original, errors.New("MQTT canonical mirror plan belongs to another environment")
		}
		for _, r := range p.Resources {
			if r.ID == id && r.Action != "retire" {
				if object != nil {
					return original, errors.New("MQTT canonical mirror has ambiguous consumers")
				}
				object = r.Desired
			}
		}
	}
	if object == nil {
		raw, err := secretCheckKubectl([]*deploymentCheckRuntime{store.checkRuntime}, false, "--kubeconfig", store.KubeconfigPath(), "-n", stack+"-video-cloud", "get", "deployment", "mqtt-pki", "--ignore-not-found=true", "-o", "json")
		if err != nil {
			return original, secretCheckFailure(err, "cannot inspect the selected MQTT canonical mirror consumer")
		}
		if len(strings.TrimSpace(string(raw))) == 0 {
			return original, nil
		}
		if json.Unmarshal(raw, &object) != nil {
			return original, errors.New("MQTT canonical mirror consumer metadata is invalid")
		}
	}
	if !managedMQTTDashboardMirror(object, stack) {
		return original, errors.New("MQTT canonical mirror consumer differs from its managed runtime contract")
	}
	return secretK8SBinding{NamespaceSuffix: "-video-cloud", Secret: "mqtt-pki-runtime", Key: "dashboard-password"}, nil
}

func managedMQTTDashboardMirror(object deploymentplan.Object, stack string) bool {
	if deploymentplan.ID(object) != "Deployment/"+stack+"-video-cloud/mqtt-pki" {
		return false
	}
	containers := certIssuerObjectList(environmentPKIPod(object)["containers"])
	if len(containers) != 2 {
		return false
	}
	seen := map[string]bool{}
	for _, v := range containers {
		c := deploymentplan.Map(v)
		name := deploymentplan.String(c["name"])
		if name != "mqtt" && name != "pkibroker" || seen[name] {
			return false
		}
		seen[name] = true
		if name != "mqtt" {
			continue
		}
		count := 0
		for _, v := range certIssuerObjectList(c["env"]) {
			e := deploymentplan.Map(v)
			if e["name"] != "EMQX_DASHBOARD__DEFAULT_PASSWORD" {
				continue
			}
			count++
			ref := deploymentplan.Map(deploymentplan.Map(e["valueFrom"])["secretKeyRef"])
			if e["value"] != nil || ref["name"] != "mqtt-pki-runtime" || ref["key"] != "dashboard-password" || ref["optional"] == true {
				return false
			}
		}
		if count != 1 {
			return false
		}
	}
	return true
}
