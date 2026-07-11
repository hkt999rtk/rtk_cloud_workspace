package main

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestSharedCapacityTargetScaling(t *testing.T) {
	for _, tc := range []struct {
		target, mqtt, api, brokerNodes int
	}{
		{target: 1000, mqtt: 2, api: 2, brokerNodes: 2},
		{target: 50000, mqtt: 3, api: 2, brokerNodes: 3},
		{target: 100000, mqtt: 5, api: 3, brokerNodes: 5},
	} {
		workspace := writeDeploymentFixture(t, "staging", "lke")
		cfg, err := resolveDeploymentConfig(workspace, "staging", "")
		if err != nil {
			t.Fatal(err)
		}
		cfg.Values["CAPACITY_TARGET_CONNECTIONS"] = strconv.Itoa(tc.target)
		cfg.Values["CAPACITY_ACTIVE_DEVICES"] = strconv.Itoa(tc.target)
		plan, _, err := buildSharedCapacityPlan(cfg.Values)
		if err != nil {
			t.Fatal(err)
		}
		if got := plan.Workloads["mqtt"].EffectiveReplicas; got != tc.mqtt {
			t.Fatalf("target %d MQTT replicas=%d want=%d", tc.target, got, tc.mqtt)
		}
		if got := plan.Workloads["video-cloud-api"].EffectiveReplicas; got != tc.api {
			t.Fatalf("target %d API replicas=%d want=%d", tc.target, got, tc.api)
		}
		if got := plan.NodeClasses["broker"].EffectiveCount; got != tc.brokerNodes {
			t.Fatalf("target %d broker nodes=%d want=%d", tc.target, got, tc.brokerNodes)
		}
	}
}

func TestSharedCapacityNodeClassBottlenecksAndFit(t *testing.T) {
	workspace := writeDeploymentFixture(t, "staging", "lke")
	cfg, err := resolveDeploymentConfig(workspace, "staging", "")
	if err != nil {
		t.Fatal(err)
	}
	values := appendMap(cfg.Values, nil)
	values["NODE_CLASS_GENERAL_MIN_COUNT"] = "1"
	values["VIDEO_CLOUD_API_MIN_REPLICAS"] = "1"
	values["VIDEO_CLOUD_API_REQUEST_CPU"] = "2500m"
	values["VIDEO_CLOUD_API_REQUEST_MEMORY"] = "128Mi"
	plan, _, err := buildSharedCapacityPlan(values)
	if err != nil {
		t.Fatal(err)
	}
	if plan.NodeClasses["general"].RequiredByCPU < 2 {
		t.Fatalf("expected CPU bottleneck: %+v", plan.NodeClasses["general"])
	}
	values["VIDEO_CLOUD_API_REQUEST_CPU"] = "100m"
	values["VIDEO_CLOUD_API_REQUEST_MEMORY"] = "7Gi"
	if _, _, err := buildSharedCapacityPlan(values); err == nil || !strings.Contains(err.Error(), "cannot fit") {
		t.Fatalf("oversized pod got %v", err)
	}
	values["VIDEO_CLOUD_API_REQUEST_MEMORY"] = "128Mi"
	values["CAPACITY_SYSTEM_RESERVED_CPU_MILLI"] = "4000"
	if _, _, err := buildSharedCapacityPlan(values); err == nil || !strings.Contains(err.Error(), "no resources after system reserve") {
		t.Fatalf("invalid reserve got %v", err)
	}
}

func TestSharedCapacityPlanIsAdapterIndependent(t *testing.T) {
	var plans []sharedCapacityPlan
	for _, adapter := range []string{"lke", "eks", "gke"} {
		workspace := writeDeploymentFixture(t, "prod", adapter)
		cfg, err := resolveDeploymentConfig(workspace, "prod", "")
		if err != nil {
			t.Fatal(err)
		}
		plans = append(plans, cfg.Capacity)
	}
	if !reflect.DeepEqual(plans[0], plans[1]) || !reflect.DeepEqual(plans[1], plans[2]) {
		t.Fatal("generic capacity plan changed with adapter selection")
	}
}
