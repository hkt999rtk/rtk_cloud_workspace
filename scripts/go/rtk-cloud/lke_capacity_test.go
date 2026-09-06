package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLKEMissingPlannedVolumeServicesUsesExistingPVCs(t *testing.T) {
	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "kubeconfig.yaml")
	writeTestFile(t, kubeconfig, "test kubeconfig\n")
	kubectl := filepath.Join(dir, "kubectl")
	writeTestFile(t, kubectl, `#!/bin/sh
case "$*" in
  *" get pvc data-fleet-valkey-0 "*) printf 'Bound' ;;
esac
`)
	if err := os.Chmod(kubectl, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("RTK_CLOUD_KUBECONFIG", kubeconfig)

	got := lkeMissingPlannedVolumeServices(
		provisionPaths{EnvRoot: dir},
		map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"},
		lkeProviderServicePlan{PostgresVolumes: 1, FleetVolumes: 1},
	)
	if got != 1 {
		t.Fatalf("missing volume services = %d, want 1 for the absent PostgreSQL PVC", got)
	}
}

func TestLKEMissingPlannedVolumeServicesCountsPendingPVCs(t *testing.T) {
	dir := t.TempDir()
	kubeconfig := filepath.Join(dir, "kubeconfig.yaml")
	writeTestFile(t, kubeconfig, "test kubeconfig\n")
	kubectl := filepath.Join(dir, "kubectl")
	writeTestFile(t, kubectl, `#!/bin/sh
case "$*" in
  *" get pvc data-fleet-valkey-0 "*) printf 'Pending' ;;
  *" get pvc data-postgresql-0 "*) printf 'Bound' ;;
esac
`)
	if err := os.Chmod(kubectl, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", kubectl)
	t.Setenv("RTK_CLOUD_KUBECONFIG", kubeconfig)

	got := lkeMissingPlannedVolumeServices(
		provisionPaths{EnvRoot: dir},
		map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"},
		lkeProviderServicePlan{PostgresVolumes: 1, FleetVolumes: 1},
	)
	if got != 1 {
		t.Fatalf("missing volume services = %d, want 1 for the pending Fleet PVC", got)
	}
}

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
		"CLOUD_STACK_NAME":                          "video-cloud-staging",
		"CLOUD_PROVIDER":                            "lke",
		"VIDEO_CLOUD_DOMAIN":                        "video-cloud-staging.realtekconnect.com",
		"ACCOUNT_MANAGER_DOMAIN":                    "account-manager.video-cloud-staging.realtekconnect.com",
		"CLOUD_ADMIN_DOMAIN":                        "admin.video-cloud-staging.realtekconnect.com",
		"CLOUD_LOGGER_DOMAIN":                       "logger.video-cloud-staging.realtekconnect.com",
		"LKE_NODE_TYPE":                             "g6-standard-2",
		"LKE_NODE_COUNT":                            "2",
		"MQTT_EFFECTIVE_REPLICAS":                   "2",
		"NODE_CLASS_BROKER_EFFECTIVE_COUNT":         "3",
		"NODE_CLASS_BROKER_TOTAL_REQUEST_CPU_MILLI": "7000",
	}

	err := lkeCheckCapacity(env, provisionOptions{})
	if err == nil {
		t.Fatal("expected capacity check to fail")
	}
	msg := err.Error()
	for _, want := range []string{"LKE capacity check failed", "required_nodes="} {
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
		"MQTT_EFFECTIVE_REPLICAS":                 "5",
		"NODE_CLASS_BROKER_EFFECTIVE_COUNT":       "5",
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

func TestLKECapacityDoesNotRecalculateSharedMQTTIntent(t *testing.T) {
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
		"MQTT_EFFECTIVE_REPLICAS":                 "4",
		"NODE_CLASS_BROKER_EFFECTIVE_COUNT":       "5",
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

	if err := lkeCheckCapacity(env, provisionOptions{}); err != nil {
		t.Fatalf("LKE adapter must consume shared effective values without recalculating target capacity: %v", err)
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

	services := lkeProviderServices(env, 5, provisionOptions{})
	if services.EdgeVMs != 1 {
		t.Fatalf("edge VMs = %d, want 1", services.EdgeVMs)
	}
	if services.CoturnVMs != 1 {
		t.Fatalf("coturn VMs = %d, want 1", services.CoturnVMs)
	}
	if services.FleetVolumes != 1 {
		t.Fatalf("fleet volumes = %d, want 1", services.FleetVolumes)
	}
	if services.RequiredServices != 8 {
		t.Fatalf("required services = %d, want 8", services.RequiredServices)
	}

	err := lkeCheckCapacity(env, provisionOptions{})
	if err == nil {
		t.Fatal("expected provider capacity check to include coturn VM and fail")
	}
	for _, want := range []string{"required active services=8", "fleet_volumes=1", "coturn_vms=1", "reduce LKE_COTURN_VM_COUNT"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected %q in provider capacity error:\n%s", want, err.Error())
		}
	}
}

func TestLKEProviderServicesSkipsFleetVolumeForUnrelatedTargetedDeploy(t *testing.T) {
	env := map[string]string{
		"CLOUD_STACK_NAME":          "video-cloud-staging",
		"LKE_POSTGRES_STORAGE_MODE": "emptydir",
		"LKE_EDGE_HAPROXY_COUNT":    "0",
		"LKE_COTURN_VM_COUNT":       "0",
	}

	services := lkeProviderServices(env, 2, provisionOptions{workloads: []string{"frontend"}})
	if services.FleetVolumes != 0 {
		t.Fatalf("fleet volumes = %d, want 0 for a targeted frontend deploy", services.FleetVolumes)
	}
	if services.RequiredServices != 2 {
		t.Fatalf("required services = %d, want only the 2 worker nodes", services.RequiredServices)
	}
}

func TestLKEProviderServicesPlansDatabaseNodesForTargetedFleetDeploy(t *testing.T) {
	env := map[string]string{
		"FLEET_VALKEY_NODE_CLASS":          "database",
		"LKE_POSTGRES_DEDICATED_NODE_POOL": "true",
		"LKE_POSTGRES_NODE_COUNT":          "2",
	}

	fleet := lkeProviderServices(env, 1, provisionOptions{workloads: []string{"video-cloud"}})
	if fleet.DatabaseNodes != 2 {
		t.Fatalf("targeted Fleet database nodes = %d, want 2", fleet.DatabaseNodes)
	}
	unrelated := lkeProviderServices(env, 1, provisionOptions{workloads: []string{"frontend"}})
	if unrelated.DatabaseNodes != 0 {
		t.Fatalf("unrelated targeted database nodes = %d, want 0", unrelated.DatabaseNodes)
	}
}

func TestLKELiveProviderServicesCountsMissingDatabasePool(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	fakeLinodeCurl(t, map[string]string{
		"/volumes?page_size=500":       `{"data":[],"results":0}`,
		"/nodebalancers?page_size=500": `{"data":[],"results":0}`,
		"/linode/instances?page_size=500": `{"data":[
			{"id":1,"label":"lke-node-01"}
		],"results":1}`,
		"/lke/clusters?page_size=500": `{"data":[{"id":12345,"label":"video-cloud-staging-lke","region":"us-sea","k8s_version":"1.36"}]}`,
		"/lke/clusters/12345/pools":   `{"data":[{"id":111,"type":"g6-standard-4","count":1,"labels":{"rtk.io/node-class":"general"}}]}`,
	})
	t.Setenv("LINODE_TOKEN", "test-token")
	env := map[string]string{
		"CLOUD_STACK_NAME":       "video-cloud-staging",
		"CLOUD_REGION":           "us-sea",
		"LKE_NODE_TYPE":          "g6-standard-4",
		"LKE_POSTGRES_NODE_TYPE": "g6-standard-8",
	}
	plan := lkeProviderServicePlan{NodeServices: 1, DatabaseNodes: 1, Limit: 1}

	err := lkeCheckLiveProviderActiveServices(provisionPaths{Workspace: workspace, EnvRoot: envRoot}, env, plan)
	if err == nil {
		t.Fatal("expected missing database pool to exceed live provider quota")
	}
	for _, want := range []string{"projected active services=2", "additional_required=1"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected %q in error:\n%s", want, err.Error())
		}
	}
}

func TestLKELiveProviderServicesCountsExistingActiveLinodes(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	fakeLinodeCurl(t, map[string]string{
		"/volumes?page_size=500":       `{"data":[{"id":9001}],"results":1}`,
		"/nodebalancers?page_size=500": `{"data":[],"results":0}`,
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
			{"id":12,"label":"shared-ci"},
			{"id":13,"label":"shared-ci-02"}
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
		"LKE_LINODE_ACTIVE_SERVICE_LIMIT":    "14",
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
	for _, want := range []string{"projected active services=17", "current_active=14", "current_volumes=1", "additional_required=3"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("expected %q in error:\n%s", want, err.Error())
		}
	}
}

func TestLKELiveProviderServicesAccountsForPlannedNodePoolShrink(t *testing.T) {
	workspace, envRoot := makeLKETestEnv(t)
	fakeLinodeCurl(t, map[string]string{
		"/volumes?page_size=500":       `{"data":[],"results":0}`,
		"/nodebalancers?page_size=500": `{"data":[],"results":0}`,
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
