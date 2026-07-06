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
		"LKE_CLOUD_LOGGER_REQUEST_MEMORY":         "512Mi",
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

func TestLKEProviderServicesCountsCoturnVM(t *testing.T) {
	env := map[string]string{
		"CLOUD_STACK_NAME":                "video-cloud-staging",
		"LKE_EDGE_HAPROXY_COUNT":          "1",
		"LKE_COTURN_VM_COUNT":             "1",
		"LKE_POSTGRES_STORAGE_MODE":       "emptydir",
		"LKE_LINODE_ACTIVE_SERVICE_LIMIT": "6",
	}

	services := lkeProviderServices(env, 5)
	if services.EdgeVMs != 1 {
		t.Fatalf("edge VMs = %d, want 1", services.EdgeVMs)
	}
	if services.CoturnVMs != 1 {
		t.Fatalf("coturn VMs = %d, want 1", services.CoturnVMs)
	}
	if services.RequiredServices != 7 {
		t.Fatalf("required services = %d, want 7", services.RequiredServices)
	}

	err := lkeCheckCapacity(env, provisionOptions{})
	if err == nil {
		t.Fatal("expected provider capacity check to include coturn VM and fail")
	}
	for _, want := range []string{"required active services=7", "coturn_vms=1", "reduce LKE_COTURN_VM_COUNT"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected %q in provider capacity error:\n%s", want, err.Error())
		}
	}
}

func TestLKELiveProviderServicesCountsExistingActiveLinodes(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	fakeLinodeCurl(t, map[string]string{
		"/linode/instances?page_size=500": `{"data":[
			{"id":1,"label":"lke-node-01"},
			{"id":2,"label":"lke-node-02"},
			{"id":3,"label":"lke-node-03"},
			{"id":4,"label":"lke-node-04"},
			{"id":5,"label":"lke-node-05"},
			{"id":6,"label":"lke-node-06"},
			{"id":7,"label":"lke-node-07"},
			{"id":8,"label":"lke-node-08"},
			{"id":9,"label":"lke-node-09"},
			{"id":10,"label":"lke-node-10"},
			{"id":11,"label":"lke-postgres-01"},
			{"id":12,"label":"shared-ci"}
		]}`,
		"/lke/clusters?page_size=500": `{"data":[{"id":12345,"label":"video-cloud-staging-lke","region":"us-sea","k8s_version":"1.36"}]}`,
		"/lke/clusters/12345/pools":   `{"data":[{"id":111,"type":"g6-standard-6","count":10}]}`,
	})
	t.Setenv("LINODE_TOKEN", "test-token")
	env := map[string]string{
		"CLOUD_STACK_NAME":                   "video-cloud-staging",
		"CLOUD_REGION":                       "us-sea",
		"LKE_NODE_TYPE":                      "g6-standard-6",
		"LKE_NODE_COUNT":                     "10",
		"LKE_MQTT_REPLICAS":                  "7",
		"LKE_MQTT_CONNECTIONS_PER_POD":       "20000",
		"LKE_TARGET_CONNECTS":                "100000",
		"LKE_EDGE_HAPROXY_COUNT":             "1",
		"LKE_COTURN_VM_COUNT":                "1",
		"LKE_POSTGRES_STORAGE_MODE":          "emptydir",
		"LKE_LINODE_ACTIVE_SERVICE_LIMIT":    "13",
		"LKE_INGRESS_REQUEST_CPU":            "100m",
		"LKE_ACCOUNT_MANAGER_REQUEST_CPU":    "150m",
		"LKE_CLOUD_LOGGER_REQUEST_CPU":       "50m",
		"LKE_MQTT_REQUEST_CPU":               "200m",
		"LKE_VIDEO_CLOUD_REPLICAS":           "1",
		"LKE_VIDEO_CLOUD_API_REQUEST_CPU":    "250m",
		"LKE_VIDEO_CLOUD_API_REQUEST_MEMORY": "512Mi",
	}

	err := lkeCheckCapacityWithPaths(provisionPaths{Workspace: workspace, EnvRoot: envRoot}, env, provisionOptions{})
	if err == nil {
		t.Fatal("expected live provider active service failure")
	}
	for _, want := range []string{"projected active services=14", "current_active=12", "additional_required=2"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected %q in error:\n%s", want, err.Error())
		}
	}
}

func TestLKELiveProviderServicesAccountsForPlannedNodePoolShrink(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	fakeLinodeCurl(t, map[string]string{
		"/linode/instances?page_size=500": `{"data":[
			{"id":1,"label":"lke-node-01"},
			{"id":2,"label":"lke-node-02"},
			{"id":3,"label":"lke-node-03"},
			{"id":4,"label":"lke-node-04"},
			{"id":5,"label":"lke-node-05"},
			{"id":6,"label":"lke-node-06"},
			{"id":7,"label":"lke-node-07"},
			{"id":8,"label":"lke-node-08"},
			{"id":9,"label":"lke-node-09"},
			{"id":10,"label":"lke-node-10"},
			{"id":11,"label":"lke-postgres-01"},
			{"id":12,"label":"shared-ci"}
		]}`,
		"/lke/clusters?page_size=500": `{"data":[{"id":12345,"label":"video-cloud-staging-lke","region":"us-sea","k8s_version":"1.36"}]}`,
		"/lke/clusters/12345/pools": `{"data":[
			{"id":111,"type":"g6-standard-6","count":10},
			{"id":222,"type":"g6-standard-6","count":1,"labels":{"role":"postgres"},"taints":[{"key":"workload","value":"postgres","effect":"NoSchedule"}]}
		]}`,
	})
	t.Setenv("LINODE_TOKEN", "test-token")
	env := map[string]string{
		"CLOUD_STACK_NAME":                   "video-cloud-staging",
		"CLOUD_REGION":                       "us-sea",
		"LKE_NODE_TYPE":                      "g6-standard-6",
		"LKE_NODE_COUNT":                     "5",
		"LKE_MQTT_REPLICAS":                  "5",
		"LKE_MQTT_CONNECTIONS_PER_POD":       "20000",
		"LKE_TARGET_CONNECTS":                "50000",
		"LKE_EDGE_HAPROXY_COUNT":             "1",
		"LKE_COTURN_VM_COUNT":                "1",
		"LKE_POSTGRES_NODE_COUNT":            "1",
		"LKE_LINODE_ACTIVE_SERVICE_LIMIT":    "12",
		"LKE_INGRESS_REQUEST_CPU":            "100m",
		"LKE_ACCOUNT_MANAGER_REQUEST_CPU":    "150m",
		"LKE_CLOUD_LOGGER_REQUEST_CPU":       "50m",
		"LKE_MQTT_REQUEST_CPU":               "200m",
		"LKE_VIDEO_CLOUD_REPLICAS":           "1",
		"LKE_VIDEO_CLOUD_API_REQUEST_CPU":    "250m",
		"LKE_VIDEO_CLOUD_API_REQUEST_MEMORY": "512Mi",
	}

	if err := lkeCheckCapacityWithPaths(provisionPaths{Workspace: workspace, EnvRoot: envRoot}, env, provisionOptions{}); err != nil {
		t.Fatalf("capacity check should allow scripted shrink before adding edge/coturn: %v", err)
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
