package home100k

import (
	"fmt"
	"testing"
)

func TestDefaultPlanResolves100KHomeBaseline(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:   "cloud_env/staging/lke",
		Brandname: "RTK",
		Region:    "us-sea",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}

	if plan.Conditions.Devices != 100000 {
		t.Fatalf("devices = %d, want 100000", plan.Conditions.Devices)
	}
	if plan.Conditions.Users != 5000 {
		t.Fatalf("users = %d, want 5000", plan.Conditions.Users)
	}
	if plan.Conditions.DevicesPerUser != 20 {
		t.Fatalf("devices per user = %d, want 20", plan.Conditions.DevicesPerUser)
	}
	if got := plan.DeviceMix["light"]; got != 50000 {
		t.Fatalf("light count = %d, want 50000", got)
	}
	if got := plan.DeviceMix["air_conditioner"]; got != 20000 {
		t.Fatalf("air conditioner count = %d, want 20000", got)
	}
	if got := plan.DeviceMix["smart_meter"]; got != 30000 {
		t.Fatalf("smart meter count = %d, want 30000", got)
	}
	if got := plan.PresenceMix["online_steady"]; got != 85000 {
		t.Fatalf("online steady count = %d, want 85000", got)
	}
	if got := plan.PresenceMix["offline_desired_queue"]; got != 10000 {
		t.Fatalf("offline desired queue count = %d, want 10000", got)
	}
	if got := plan.PresenceMix["flapping_reconnect"]; got != 5000 {
		t.Fatalf("flapping reconnect count = %d, want 5000", got)
	}
}

func TestDefaultPlanCreatesDeterministicShardsAndStages(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:   "cloud_env/staging/lke",
		Brandname: "RTK",
		Region:    "us-sea",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}

	deviceShards := plan.ShardsByRole("device-mqtt")
	if len(deviceShards) != 5 {
		t.Fatalf("device shards = %d, want 5", len(deviceShards))
	}
	for idx, shard := range deviceShards {
		if shard.Index != idx {
			t.Fatalf("device shard index = %d, want %d", shard.Index, idx)
		}
		if shard.Start != idx*20000 || shard.End != (idx+1)*20000 {
			t.Fatalf("device shard %d range = [%d,%d), want [%d,%d)", idx, shard.Start, shard.End, idx*20000, (idx+1)*20000)
		}
	}

	userShards := plan.ShardsByRole("user-app")
	if len(userShards) != 5 {
		t.Fatalf("user shards = %d, want 5", len(userShards))
	}
	if userShards[0].Start != 0 || userShards[0].End != 1000 || userShards[4].Start != 4000 || userShards[4].End != 5000 {
		t.Fatalf("unexpected user shards: %#v", userShards)
	}
	if len(plan.Assignments) != 5 {
		t.Fatalf("VM assignments = %d, want 5", len(plan.Assignments))
	}
	for idx, assignment := range plan.Assignments {
		if assignment.Label != "home-100k-mixed-"+fmt.Sprintf("%03d", idx) {
			t.Fatalf("assignment label = %q", assignment.Label)
		}
		if assignment.Role != "mixed" || len(assignment.TaskShards) != 2 {
			t.Fatalf("assignment = %#v, want mixed device+user tasks", assignment)
		}
		if assignment.TaskShards[0].Role != "device-mqtt" || assignment.TaskShards[1].Role != "user-app" {
			t.Fatalf("assignment task order = %#v", assignment.TaskShards)
		}
	}

	wantStages := []int{25000, 50000, 75000, 100000}
	if len(plan.Stages) != len(wantStages) {
		t.Fatalf("stages = %d, want %d", len(plan.Stages), len(wantStages))
	}
	for idx, want := range wantStages {
		if plan.Stages[idx].ConnectedDevices != want {
			t.Fatalf("stage %d devices = %d, want %d", idx, plan.Stages[idx].ConnectedDevices, want)
		}
		if plan.Stages[idx].WarmUp != "1m" || plan.Stages[idx].SteadyState != "2m" || plan.Stages[idx].CoolDown != "45s" {
			t.Fatalf("stage %d durations = warm-up %s steady %s cool-down %s, want 1m/2m/45s", idx, plan.Stages[idx].WarmUp, plan.Stages[idx].SteadyState, plan.Stages[idx].CoolDown)
		}
	}
}

func TestPlanUsesConfiguredStageDurations(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:       "cloud_env/staging/lke",
		Brandname:     "RTK",
		Region:        "us-sea",
		StageWarmUp:   "1m",
		StageSteady:   "3m",
		StageCoolDown: "30s",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}

	for _, stage := range plan.Stages {
		if stage.WarmUp != "1m" || stage.SteadyState != "3m" || stage.CoolDown != "30s" {
			t.Fatalf("stage durations = warm-up %s steady %s cool-down %s", stage.WarmUp, stage.SteadyState, stage.CoolDown)
		}
	}
}

func TestDefaultPlanWorkflowIncludesAggregationBeforeCleanup(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:   "cloud_env/staging/lke",
		Brandname: "RTK",
		Region:    "us-sea",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}

	want := []string{"plan", "provision-vms", "sync", "run-stages", "collect", "collect-server-evidence", "aggregate", "destroy-vms"}
	if len(plan.Workflow) != len(want) {
		t.Fatalf("workflow = %#v, want %#v", plan.Workflow, want)
	}
	for idx := range want {
		if plan.Workflow[idx] != want[idx] {
			t.Fatalf("workflow = %#v, want %#v", plan.Workflow, want)
		}
	}
}

func TestDefaultPlanArtifactsMatchCollectedRunLayout(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:   "cloud_env/staging/lke",
		Brandname: "RTK",
		Region:    "us-sea",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}

	if plan.Artifacts.ShardResults != "loadtests/home-100k/reports/<run_id>/shards/" {
		t.Fatalf("shard results artifact = %q", plan.Artifacts.ShardResults)
	}
	if plan.Artifacts.AggregateReport != "loadtests/home-100k/reports/<run_id>/TEST_REPORT.md" {
		t.Fatalf("aggregate report artifact = %q", plan.Artifacts.AggregateReport)
	}
	if plan.Artifacts.ServerEvidence != "loadtests/home-100k/reports/<run_id>/server-evidence.json" {
		t.Fatalf("server evidence artifact = %q", plan.Artifacts.ServerEvidence)
	}
}

func TestPlanRequiresReviewCriticalInputs(t *testing.T) {
	if _, err := NewPlan(PlanOptions{Brandname: "RTK", Region: "us-sea"}); err == nil {
		t.Fatal("NewPlan() without env root succeeded, want error")
	}
	if _, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/lke", Region: "us-sea"}); err == nil {
		t.Fatal("NewPlan() without brandname succeeded, want error")
	}
	if _, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/lke", Brandname: "RTK"}); err == nil {
		t.Fatal("NewPlan() without region succeeded, want error")
	}
}
