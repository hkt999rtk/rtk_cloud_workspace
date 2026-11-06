package main

import (
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestStagingWorkloadRuntimeForwardKeepsOriginalPVCAndSteadyEntrypoint(t *testing.T) {
	stack := "video-cloud-staging"
	p := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "staging", Stack: stack}}
	j := deploymentplan.Journal{Owners: map[string]string{}}
	add := func(kind, ns, name, phase, action string, spec deploymentplan.Object) string {
		o := deploymentplan.Object{"kind": kind, "metadata": deploymentplan.Object{"namespace": stack + "-" + ns, "name": name}, "spec": spec}
		id := deploymentplan.ID(o)
		p.Resources = append(p.Resources, deploymentplan.Resource{ID: id, Desired: o, Phase: phase, Action: action})
		if phase != "routing" {
			j.Completed = append(j.Completed, id)
			j.Owners[id] = "11111111-1111-4111-8111-111111111111"
		}
		return id
	}
	claim := add("PersistentVolumeClaim", "logger", "cloud-logger-billing-inbox", "storage", "create", nil)
	deployment := func(c deploymentplan.Object, volumes ...any) deploymentplan.Object {
		return deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{c}, "volumes": volumes}}}
	}
	logger := add("Deployment", "logger", "cloud-logger", "workload", "create", deployment(deploymentplan.Object{
		"name": "app", "args": []any{"-addr", ":18090", "-billing-inbox", "/var/lib/rtk-cloud-logger/billing-inbox.db"},
		"volumeMounts": []any{deploymentplan.Object{"name": "billing-inbox", "mountPath": "/var/lib/rtk-cloud-logger"}},
	}, deploymentplan.Object{"name": "billing-inbox", "persistentVolumeClaim": deploymentplan.Object{"claimName": "cloud-logger-billing-inbox"}}))
	api := add("Deployment", "video-cloud", "video-cloud-api", "workload", "update", deployment(deploymentplan.Object{
		"name": "app", "env": []any{deploymentplan.Object{"name": "PKI_ENVIRONMENT", "value": "staging"}},
	}))
	add("Ingress", "ingress", "staging-public", "routing", "create", nil)
	j.Failed = api
	if !stagingWorkloadRuntimeInterrupted(p, j) {
		t.Fatal("known completed writes and pending routing were rejected")
	}
	init, steady, err := stagingWorkloadRuntimeDefinitions(p, j)
	if err != nil {
		t.Fatal(err)
	}
	if len(init) != 2 || len(steady) != 4 || deploymentplan.ID(init[0].Object) != logger ||
		mqttFoundationPrimary(init[0].Object)["command"] == nil || mqttFoundationPrimary(steady[1].Object)["command"] != nil ||
		mqttFoundationEnv(mqttFoundationPrimary(init[1].Object), "VIDEO_CLOUD_ENV") != "staging" ||
		mqttFoundationEnv(mqttFoundationPrimary(steady[2].Object), "VIDEO_CLOUD_ENV") != "staging" ||
		!steady[0].Retain || deploymentplan.Map(steady[0].Object["metadata"])["uid"] != j.Owners[claim] || steady[1].Retain || !steady[2].Retain {
		t.Fatal("scoped initialization, retained owners, or final runtime scope changed")
	}
	j.InFlight = logger
	if stagingWorkloadRuntimeInterrupted(p, j) {
		t.Fatal("an unresolved write cannot be superseded")
	}
	p.Resources[0].Action = "retain"
	if _, _, err := stagingWorkloadRuntimeDefinitions(p, j); err == nil {
		t.Fatal("an existing inbox cannot receive first-generation initialization")
	}
}

func TestStagingGeneralCapacityKeepsOriginalPoolMembers(t *testing.T) {
	stack := "video-cloud-staging"
	want := stagingGeneralCapacityIntent{PoolID: 23, NodeType: "g6-standard-4", Members: []int{101, 102}, Count: 3}
	pool := stagingGeneralCapacityPool{
		lkeNodePool: lkeNodePool{ID: 23, Type: want.NodeType, Count: 2, Label: stack + "-general", Labels: map[string]string{"rtk.io/node-class": "general"}},
		Nodes:       []stagingGeneralCapacityPoolNode{{ID: 102}, {ID: 101}},
	}
	if !pool.matches(want, stack) {
		t.Fatal("original verified pool should qualify")
	}
	pool.Count = 3
	pool.Nodes = append(pool.Nodes, stagingGeneralCapacityPoolNode{ID: 103})
	if !pool.matches(want, stack) {
		t.Fatal("the original members plus one should qualify")
	}
	pool.Nodes[0].ID = 104
	if pool.matches(want, stack) {
		t.Fatal("replacing an original member must stop the continuation")
	}
}
