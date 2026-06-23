package home100k

import (
	"strings"
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
	if plan.Conditions.RunnerNofileLimit != 1048576 {
		t.Fatalf("runner nofile limit = %d, want 1048576", plan.Conditions.RunnerNofileLimit)
	}
	if plan.Conditions.DeviceSessionModel != "lifetime-subscription" {
		t.Fatalf("device session model = %q, want lifetime-subscription", plan.Conditions.DeviceSessionModel)
	}
	if plan.Conditions.RunnerReadModel != "go-netpoll-bounded-reader-goroutine" {
		t.Fatalf("runner read model = %q, want go-netpoll-bounded-reader-goroutine", plan.Conditions.RunnerReadModel)
	}
	if plan.Conditions.FunctionalSuccessThresholdPercent != 99.5 {
		t.Fatalf("functional success threshold = %.2f, want 99.5", plan.Conditions.FunctionalSuccessThresholdPercent)
	}
	if plan.Conditions.ClientTargetCompletenessPercent != 100 {
		t.Fatalf("client target completeness = %.2f, want 100", plan.Conditions.ClientTargetCompletenessPercent)
	}
	if plan.Conditions.ExactEventCorrelationPercent != 100 {
		t.Fatalf("exact event correlation = %.2f, want 100", plan.Conditions.ExactEventCorrelationPercent)
	}
	if plan.Conditions.AggregateCorrelationTolerancePercent != 0.1 {
		t.Fatalf("aggregate correlation tolerance percent = %.2f, want 0.1", plan.Conditions.AggregateCorrelationTolerancePercent)
	}
	if plan.Conditions.AggregateCorrelationMinTolerance != 5 {
		t.Fatalf("aggregate correlation min tolerance = %d, want 5", plan.Conditions.AggregateCorrelationMinTolerance)
	}
	if plan.ScenarioProfile != "home-diverse-v1" {
		t.Fatalf("scenario profile = %q, want home-diverse-v1", plan.ScenarioProfile)
	}
	if got := plan.DeviceMix["light"]; got != 18000 {
		t.Fatalf("light count = %d, want 18000", got)
	}
	if got := plan.DeviceMix["switch"]; got != 7000 {
		t.Fatalf("switch count = %d, want 7000", got)
	}
	if got := plan.DeviceMix["smart_plug"]; got != 12000 {
		t.Fatalf("smart plug count = %d, want 12000", got)
	}
	if got := plan.DeviceMix["air_conditioner"]; got != 10000 {
		t.Fatalf("air conditioner count = %d, want 10000", got)
	}
	if got := plan.DeviceMix["environment_sensor"]; got != 12000 {
		t.Fatalf("environment sensor count = %d, want 12000", got)
	}
	if got := plan.DeviceMix["security_sensor"]; got != 10000 {
		t.Fatalf("security sensor count = %d, want 10000", got)
	}
	if got := plan.DeviceMix["smart_meter"]; got != 8000 {
		t.Fatalf("smart meter count = %d, want 8000", got)
	}
	if got := plan.DeviceMix["camera_status"]; got != 7000 {
		t.Fatalf("camera status count = %d, want 7000", got)
	}
	if got := plan.DeviceMix["door_lock"]; got != 4000 {
		t.Fatalf("door lock count = %d, want 4000", got)
	}
	if got := plan.DeviceMix["appliance"]; got != 7000 {
		t.Fatalf("appliance count = %d, want 7000", got)
	}
	if got := plan.DeviceMix["gateway"]; got != 5000 {
		t.Fatalf("gateway count = %d, want 5000", got)
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

func TestDefaultPlanIncludesDiverseDeviceAndUserProfiles(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:     "cloud_env/staging/lke",
		Brandname:   "RTK",
		Region:      "us-sea",
		DeviceCount: 37,
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}

	if sumMap(plan.DeviceMix) != 37 {
		t.Fatalf("device mix sum = %d, want 37: %#v", sumMap(plan.DeviceMix), plan.DeviceMix)
	}
	for _, name := range []string{"light", "switch", "smart_plug", "air_conditioner", "environment_sensor", "security_sensor", "smart_meter", "camera_status", "door_lock", "appliance", "gateway"} {
		if _, ok := plan.DeviceProfiles[name]; !ok {
			t.Fatalf("missing device profile %s in %#v", name, plan.DeviceProfiles)
		}
	}
	for _, name := range []string{"owner_admin", "daily_user", "background_app", "automation"} {
		if _, ok := plan.UserProfiles[name]; !ok {
			t.Fatalf("missing user profile %s in %#v", name, plan.UserProfiles)
		}
	}
	if len(plan.StageUsageWindows) != 0 {
		t.Fatalf("usage windows = %#v, want none for single target window", plan.StageUsageWindows)
	}
	if plan.DeviceProfiles["camera_status"].TrafficProfile != "event_burst" {
		t.Fatalf("camera_status traffic profile = %#v", plan.DeviceProfiles["camera_status"])
	}
	if plan.UserProfiles["automation"].ActionProfile != "automation_command" {
		t.Fatalf("automation user profile = %#v", plan.UserProfiles["automation"])
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
		wantLabel := []string{"lg01", "lg02", "lg03", "lg04", "lg05"}[idx]
		if assignment.Label != wantLabel {
			t.Fatalf("assignment label = %q, want %q", assignment.Label, wantLabel)
		}
		if assignment.Index != idx {
			t.Fatalf("assignment index = %d, want %d", assignment.Index, idx)
		}
		if assignment.Role != "mixed" || len(assignment.TaskShards) != 2 {
			t.Fatalf("assignment = %#v, want mixed device+user tasks", assignment)
		}
		if assignment.TaskShards[0].Role != "device-mqtt" || assignment.TaskShards[1].Role != "user-app" {
			t.Fatalf("assignment task order = %#v", assignment.TaskShards)
		}
	}

	if len(plan.Stages) != 1 {
		t.Fatalf("stages = %d, want 1 target window", len(plan.Stages))
	}
	if plan.Stages[0].Name != "target" {
		t.Fatalf("stage name = %q, want target", plan.Stages[0].Name)
	}
	if plan.Stages[0].ConnectedDevices != 100000 {
		t.Fatalf("stage devices = %d, want 100000", plan.Stages[0].ConnectedDevices)
	}
	if plan.Stages[0].WarmUp != "30s" || plan.Stages[0].SteadyState != "90s" || plan.Stages[0].CoolDown != "30s" {
		t.Fatalf("stage durations = warm-up %s steady %s cool-down %s, want 30s/90s/30s", plan.Stages[0].WarmUp, plan.Stages[0].SteadyState, plan.Stages[0].CoolDown)
	}
}

func TestPlanUsesConfiguredVMLabelPrefix(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:       "cloud_env/staging/lke",
		Brandname:     "RTK",
		Region:        "us-sea",
		VMLabelPrefix: "loadgen",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}

	for idx, assignment := range plan.Assignments {
		wantLabel := []string{"loadgen01", "loadgen02", "loadgen03", "loadgen04", "loadgen05"}[idx]
		if assignment.Label != wantLabel {
			t.Fatalf("assignment %d label = %q, want %q", idx, assignment.Label, wantLabel)
		}
		if assignment.Index != idx {
			t.Fatalf("assignment %d index = %d, want %d", idx, assignment.Index, idx)
		}
	}
}

func TestPlanDefaultLoadWindowIsTenMinutes(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:   "cloud_env/staging/lke",
		Brandname: "RTK",
		Region:    "us-sea",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}

	totalSeconds := 0
	stageSeconds := make([]int, 0, len(plan.Stages))
	for _, stage := range plan.Stages {
		seconds, err := stageWindowSeconds(stage)
		if err != nil {
			t.Fatalf("stageWindowSeconds(%s) error = %v", stage.Name, err)
		}
		stageSeconds = append(stageSeconds, seconds)
		totalSeconds += seconds
	}

	if totalSeconds != 150 {
		t.Fatalf("default load window = %d seconds from stages %v, want 150 seconds", totalSeconds, stageSeconds)
	}
	for idx, seconds := range stageSeconds {
		if seconds != 150 {
			t.Fatalf("stage %d window = %d seconds, want 150 seconds", idx, seconds)
		}
	}
}

func TestPlanUsesConfiguredDeviceCount(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:     "cloud_env/staging/lke",
		Brandname:   "RTK",
		Region:      "us-sea",
		DeviceCount: 9000,
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}

	if plan.Conditions.Devices != 9000 {
		t.Fatalf("devices = %d, want 9000", plan.Conditions.Devices)
	}
	if plan.Conditions.Users != 450 {
		t.Fatalf("users = %d, want 450", plan.Conditions.Users)
	}
	if plan.Conditions.VMCount != 1 {
		t.Fatalf("VM count = %d, want 1 from ceil(9000/20000)", plan.Conditions.VMCount)
	}
	if plan.Conditions.LoadGeneratorDevicesPerVM != 20000 {
		t.Fatalf("load-generator devices per VM = %d, want 20000", plan.Conditions.LoadGeneratorDevicesPerVM)
	}
	if plan.Conditions.LoadGeneratorSizingFormula != "vm_count = ceil(devices / load_generator_devices_per_vm)" {
		t.Fatalf("sizing formula = %q", plan.Conditions.LoadGeneratorSizingFormula)
	}
	if got := plan.DeviceMix["light"]; got != 1620 {
		t.Fatalf("light count = %d, want 1620", got)
	}
	if got := plan.DeviceMix["air_conditioner"]; got != 900 {
		t.Fatalf("air conditioner count = %d, want 900", got)
	}
	if got := plan.DeviceMix["smart_meter"]; got != 720 {
		t.Fatalf("smart meter count = %d, want 720", got)
	}
	if got := plan.DeviceMix["gateway"]; got != 450 {
		t.Fatalf("gateway count = %d, want 450", got)
	}
	if got := plan.PresenceMix["online_steady"]; got != 7650 {
		t.Fatalf("online steady count = %d, want 7650", got)
	}
	if got := plan.PresenceMix["offline_desired_queue"]; got != 900 {
		t.Fatalf("offline desired queue count = %d, want 900", got)
	}
	if got := plan.PresenceMix["flapping_reconnect"]; got != 450 {
		t.Fatalf("flapping reconnect count = %d, want 450", got)
	}

	deviceShards := plan.ShardsByRole("device-mqtt")
	if len(deviceShards) != 1 {
		t.Fatalf("device shards = %d, want 1", len(deviceShards))
	}
	if shard := deviceShards[0]; shard.Start != 0 || shard.End != 9000 || shard.Count != 9000 {
		t.Fatalf("device shard = [%d,%d) count=%d, want [0,9000) count=9000", shard.Start, shard.End, shard.Count)
	}
	userShards := plan.ShardsByRole("user-app")
	if len(userShards) != 1 || userShards[0].Start != 0 || userShards[0].End != 450 || userShards[0].Count != 450 {
		t.Fatalf("unexpected user shards: %#v", userShards)
	}

	if len(plan.Stages) != 1 {
		t.Fatalf("stages = %d, want 1 target window", len(plan.Stages))
	}
	if plan.Stages[0].Name != "target" || plan.Stages[0].ConnectedDevices != 9000 {
		t.Fatalf("stage = %s/%d, want target/9000", plan.Stages[0].Name, plan.Stages[0].ConnectedDevices)
	}
}

func TestPlanHonorsExplicitVMCountWithinGeneratorCapacity(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:     "cloud_env/staging/lke",
		Brandname:   "RTK",
		Region:      "us-sea",
		DeviceCount: 9000,
		VMCount:     2,
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}

	if plan.Conditions.VMCount != 2 {
		t.Fatalf("VM count = %d, want explicit 2", plan.Conditions.VMCount)
	}
	deviceShards := plan.ShardsByRole("device-mqtt")
	if len(deviceShards) != 2 {
		t.Fatalf("device shards = %d, want 2", len(deviceShards))
	}
	if deviceShards[0].Start != 0 || deviceShards[0].End != 4500 || deviceShards[0].Count != 4500 {
		t.Fatalf("first device shard = %#v, want [0,4500)", deviceShards[0])
	}
	if deviceShards[1].Start != 4500 || deviceShards[1].End != 9000 || deviceShards[1].Count != 4500 {
		t.Fatalf("second device shard = %#v, want [4500,9000)", deviceShards[1])
	}
}

func TestPlanUsesConfiguredLoadGeneratorDevicesPerVM(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:                   "cloud_env/staging/lke",
		Brandname:                 "RTK",
		Region:                    "us-sea",
		DeviceCount:               100000,
		LoadGeneratorDevicesPerVM: 16667,
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}

	if plan.Conditions.VMCount != 6 {
		t.Fatalf("VM count = %d, want ceil(100000/16667)=6", plan.Conditions.VMCount)
	}
	if plan.Conditions.LoadGeneratorDevicesPerVM != 16667 {
		t.Fatalf("load-generator devices per VM = %d, want 16667", plan.Conditions.LoadGeneratorDevicesPerVM)
	}
	if len(plan.Assignments) != 6 {
		t.Fatalf("assignments = %d, want 6", len(plan.Assignments))
	}
	last := plan.Assignments[5]
	if last.Role != "mixed" || last.Index != 5 || last.Label != "lg06" {
		t.Fatalf("last assignment = %#v, want mixed index=5 label=lg06", last)
	}
	deviceShards := plan.ShardsByRole("device-mqtt")
	if len(deviceShards) != 6 {
		t.Fatalf("device shards = %d, want 6", len(deviceShards))
	}
	if shard := deviceShards[5]; shard.Start != 83334 || shard.End != 100000 || shard.Count != 16666 {
		t.Fatalf("last device shard = %#v, want [83334,100000) count=16666", shard)
	}
	userShards := plan.ShardsByRole("user-app")
	if len(userShards) != 6 {
		t.Fatalf("user shards = %d, want 6", len(userShards))
	}
	if shard := userShards[5]; shard.Start != 4167 || shard.End != 5000 || shard.Count != 833 {
		t.Fatalf("last user shard = %#v, want [4167,5000) count=833", shard)
	}
}

func TestPlanRejectsExplicitVMCountBelowGeneratorCapacity(t *testing.T) {
	_, err := NewPlan(PlanOptions{
		EnvRoot:     "cloud_env/staging/lke",
		Brandname:   "RTK",
		Region:      "us-sea",
		DeviceCount: 100000,
		VMCount:     2,
	})
	if err == nil {
		t.Fatal("expected capacity error")
	}
	if got := err.Error(); !strings.Contains(got, "above configured load-generator capacity 20000") {
		t.Fatalf("unexpected error: %v", err)
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
