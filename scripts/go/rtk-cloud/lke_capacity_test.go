package main

import (
	"strings"
	"testing"
)

func TestLKECapacityPlanAcceptsExplicitOneKValidationProfile(t *testing.T) {
	env := map[string]string{
		"CLOUD_STACK_NAME":                        "video-cloud-staging",
		"CLOUD_REGION":                            "us-sea",
		"CLOUD_PROVIDER":                          "lke",
		"VIDEO_CLOUD_DOMAIN":                      "video-cloud-staging.realtekconnect.com",
		"ACCOUNT_MANAGER_DOMAIN":                  "account-manager.video-cloud-staging.realtekconnect.com",
		"CLOUD_ADMIN_DOMAIN":                      "admin.video-cloud-staging.realtekconnect.com",
		"CLOUD_LOGGER_DOMAIN":                     "logger.video-cloud-staging.realtekconnect.com",
		"LKE_NODE_TYPE":                           "g6-standard-2",
		"LKE_NODE_COUNT":                          "2",
		"LKE_INGRESS_REQUEST_CPU":                 "100m",
		"LKE_ACCOUNT_MANAGER_REQUEST_CPU":         "150m",
		"LKE_CLOUD_LOGGER_REQUEST_CPU":            "50m",
		"LKE_POSTGRES_REQUEST_CPU":                "250m",
		"LKE_POSTGRES_REQUEST_MEMORY":             "512Mi",
		"LKE_POSTGRES_LIMIT_MEMORY":               "2Gi",
		"LKE_MQTT_REPLICAS":                       "2",
		"LKE_MQTT_REQUEST_CPU":                    "200m",
		"LKE_VIDEO_CLOUD_REPLICAS":                "1",
		"LKE_VIDEO_CLOUD_API_REQUEST_CPU":         "250m",
		"LKE_VIDEO_CLOUD_API_REQUEST_MEMORY":      "512Mi",
		"LKE_VIDEO_CLOUD_LOGINGESTER_REQUEST_CPU": "100m",
		"LKE_VIDEO_CLOUD_MQTTUSAGE_REQUEST_CPU":   "100m",
	}

	plan, err := lkeCapacityPlan(env, provisionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.RequiredNodes > 2 {
		t.Fatalf("required nodes = %d, want <= 2: %#v", plan.RequiredNodes, plan)
	}
	if err := lkeCheckCapacity(env, provisionOptions{}); err != nil {
		t.Fatalf("capacity check should pass: %v", err)
	}
}

func TestLKECapacityCheckFailsBeforeSchedulingWhenRequestsExceedNodes(t *testing.T) {
	env := map[string]string{
		"CLOUD_STACK_NAME":       "video-cloud-staging",
		"CLOUD_PROVIDER":         "lke",
		"VIDEO_CLOUD_DOMAIN":     "video-cloud-staging.realtekconnect.com",
		"ACCOUNT_MANAGER_DOMAIN": "account-manager.video-cloud-staging.realtekconnect.com",
		"CLOUD_ADMIN_DOMAIN":     "admin.video-cloud-staging.realtekconnect.com",
		"CLOUD_LOGGER_DOMAIN":    "logger.video-cloud-staging.realtekconnect.com",
		"LKE_NODE_TYPE":          "g6-standard-2",
		"LKE_NODE_COUNT":         "2",
		"LKE_MQTT_REPLICAS":      "2",
	}

	err := lkeCheckCapacity(env, provisionOptions{})
	if err == nil {
		t.Fatal("expected capacity check to fail")
	}
	msg := err.Error()
	for _, want := range []string{"LKE capacity check failed", "required_nodes=", "postgresql", "video-cloud-api"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("expected %q in error:\n%s", want, msg)
		}
	}
}

func TestLKECapacityDerivesMQTTAndNodeCountFromTargetConnects(t *testing.T) {
	env := map[string]string{
		"CLOUD_STACK_NAME":                        "video-cloud-staging",
		"CLOUD_PROVIDER":                          "lke",
		"VIDEO_CLOUD_DOMAIN":                      "video-cloud-staging.realtekconnect.com",
		"ACCOUNT_MANAGER_DOMAIN":                  "account-manager.video-cloud-staging.realtekconnect.com",
		"CLOUD_ADMIN_DOMAIN":                      "admin.video-cloud-staging.realtekconnect.com",
		"CLOUD_LOGGER_DOMAIN":                     "logger.video-cloud-staging.realtekconnect.com",
		"LKE_NODE_TYPE":                           "g6-standard-2",
		"LKE_NODE_COUNT":                          "auto",
		"LKE_TARGET_CONNECTS":                     "100000",
		"LKE_MQTT_REPLICAS":                       "auto",
		"LKE_MQTT_CONNECTIONS_PER_POD":            "20000",
		"LKE_INGRESS_REQUEST_CPU":                 "100m",
		"LKE_ACCOUNT_MANAGER_REQUEST_CPU":         "150m",
		"LKE_CLOUD_LOGGER_REQUEST_CPU":            "50m",
		"LKE_POSTGRES_REQUEST_CPU":                "250m",
		"LKE_POSTGRES_REQUEST_MEMORY":             "512Mi",
		"LKE_MQTT_REQUEST_CPU":                    "200m",
		"LKE_VIDEO_CLOUD_REPLICAS":                "1",
		"LKE_VIDEO_CLOUD_API_REQUEST_CPU":         "250m",
		"LKE_VIDEO_CLOUD_API_REQUEST_MEMORY":      "512Mi",
		"LKE_VIDEO_CLOUD_LOGINGESTER_REQUEST_CPU": "100m",
		"LKE_VIDEO_CLOUD_MQTTUSAGE_REQUEST_CPU":   "100m",
	}

	if got := lkeMQTTReplicas(env); got != 5 {
		t.Fatalf("mqtt replicas = %d, want 5", got)
	}
	nodes, err := lkeNodeCount(env)
	if err != nil {
		t.Fatal(err)
	}
	if nodes < 5 {
		t.Fatalf("auto node count = %d, want at least MQTT spread 5", nodes)
	}
	plan, err := lkeCapacityPlan(env, provisionOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if plan.TargetConnects != 100000 || plan.MQTTCapacity != 100000 || plan.RequiredMQTTPods != 5 {
		t.Fatalf("target/mqtt capacity mismatch: %#v", plan)
	}
}

func TestLKECapacityCheckFailsWhenTargetConnectsExceedFixedMQTTCapacity(t *testing.T) {
	env := map[string]string{
		"CLOUD_STACK_NAME":                        "video-cloud-staging",
		"CLOUD_PROVIDER":                          "lke",
		"VIDEO_CLOUD_DOMAIN":                      "video-cloud-staging.realtekconnect.com",
		"ACCOUNT_MANAGER_DOMAIN":                  "account-manager.video-cloud-staging.realtekconnect.com",
		"CLOUD_ADMIN_DOMAIN":                      "admin.video-cloud-staging.realtekconnect.com",
		"CLOUD_LOGGER_DOMAIN":                     "logger.video-cloud-staging.realtekconnect.com",
		"LKE_NODE_TYPE":                           "g6-standard-2",
		"LKE_NODE_COUNT":                          "5",
		"LKE_TARGET_CONNECTS":                     "100000",
		"LKE_MQTT_REPLICAS":                       "4",
		"LKE_MQTT_CONNECTIONS_PER_POD":            "20000",
		"LKE_INGRESS_REQUEST_CPU":                 "100m",
		"LKE_ACCOUNT_MANAGER_REQUEST_CPU":         "150m",
		"LKE_CLOUD_LOGGER_REQUEST_CPU":            "50m",
		"LKE_POSTGRES_REQUEST_CPU":                "250m",
		"LKE_POSTGRES_REQUEST_MEMORY":             "512Mi",
		"LKE_MQTT_REQUEST_CPU":                    "200m",
		"LKE_VIDEO_CLOUD_REPLICAS":                "1",
		"LKE_VIDEO_CLOUD_API_REQUEST_CPU":         "250m",
		"LKE_VIDEO_CLOUD_API_REQUEST_MEMORY":      "512Mi",
		"LKE_VIDEO_CLOUD_LOGINGESTER_REQUEST_CPU": "100m",
		"LKE_VIDEO_CLOUD_MQTTUSAGE_REQUEST_CPU":   "100m",
	}

	err := lkeCheckCapacity(env, provisionOptions{})
	if err == nil {
		t.Fatal("expected MQTT capacity failure")
	}
	if !strings.Contains(err.Error(), "requires at least 5 MQTT replicas") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLKECapacityParsesQuantities(t *testing.T) {
	cpu, err := parseCPUQuantity("0.25")
	if err != nil || cpu != 250 {
		t.Fatalf("cpu got %d err %v, want 250 nil", cpu, err)
	}
	mem, err := parseMemoryMi("1Gi")
	if err != nil || mem != 1024 {
		t.Fatalf("memory got %d err %v, want 1024 nil", mem, err)
	}
}
