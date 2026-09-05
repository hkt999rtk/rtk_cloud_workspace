package main

import "strings"

func lkeTestLabEnabled(env map[string]string) bool {
	return (env["CLOUD_STACK_NAME"] == "video-cloud-dev" || env["CLOUD_STACK_NAME"] == "video-cloud-staging") && strings.EqualFold(lkeEnvValue(env, "TEST_LAB_ENABLED"), "true") && lkeMQTTTenantNamespaceEnabled(env)
}
