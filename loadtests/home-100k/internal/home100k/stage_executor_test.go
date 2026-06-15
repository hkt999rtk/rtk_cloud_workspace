package home100k

import (
	"testing"
	"time"
)

func TestExecuteStagesUsesActorFlowsForShadowMetrics(t *testing.T) {
	plan, err := NewPlan(PlanOptions{EnvRoot: "cloud_env/staging/lke", Brandname: "RTK", Region: "us-sea"})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	results, err := ExecuteStages(plan, StageExecutionOptions{SampleFlowsPerPresence: 2})
	if err != nil {
		t.Fatalf("ExecuteStages() error = %v", err)
	}
	if len(results) != 4 {
		t.Fatalf("stage results = %d, want 4", len(results))
	}
	for _, result := range results {
		if result.DesiredReportedConvergenceRate != 100 {
			t.Fatalf("%s desired/reported convergence = %.2f, want 100", result.Name, result.DesiredReportedConvergenceRate)
		}
		if result.OfflineDesiredConvergenceRate != 100 {
			t.Fatalf("%s offline convergence = %.2f, want 100", result.Name, result.OfflineDesiredConvergenceRate)
		}
		if result.DeltaClearSuccessRatePercent != 100 {
			t.Fatalf("%s delta clear = %.2f, want 100", result.Name, result.DeltaClearSuccessRatePercent)
		}
		if result.DuplicateApplyCount != 0 {
			t.Fatalf("%s duplicate apply = %d, want 0", result.Name, result.DuplicateApplyCount)
		}
		if result.MQTTReconnectCount == 0 {
			t.Fatalf("%s reconnect count = 0, want non-zero flapping coverage", result.Name)
		}
		if result.ShadowGetP50MS <= 0 || result.ShadowGetP95MS <= 0 || result.ShadowGetP99MS <= 0 {
			t.Fatalf("%s shadow get percentiles missing: p50=%.2f p95=%.2f p99=%.2f", result.Name, result.ShadowGetP50MS, result.ShadowGetP95MS, result.ShadowGetP99MS)
		}
		if result.ShadowGetP50MS > result.ShadowGetP95MS || result.ShadowGetP95MS > result.ShadowGetP99MS {
			t.Fatalf("%s shadow get percentiles out of order: p50=%.2f p95=%.2f p99=%.2f", result.Name, result.ShadowGetP50MS, result.ShadowGetP95MS, result.ShadowGetP99MS)
		}
	}
}

func TestAggregateFlowResultsComputesOfflineRateOnlyFromOfflineQueue(t *testing.T) {
	result := aggregateFlowResults(Stage{Name: "25k", ConnectedDevices: 25000}, []ActorFlowResult{
		{Presence: PresenceOnlineSteady, Converged: true, DeltaCleared: true},
		{Presence: PresenceOfflineDesiredQueue, Converged: true, OfflineConverged: true, DeltaCleared: true},
		{Presence: PresenceOfflineDesiredQueue, Converged: false, OfflineConverged: false, DeltaCleared: false},
		{Presence: PresenceFlappingReconnect, Converged: true, DeltaCleared: true},
	})
	if result.OfflineDesiredConvergenceRate != 50 {
		t.Fatalf("offline convergence = %.2f, want 50", result.OfflineDesiredConvergenceRate)
	}
	if result.DesiredReportedConvergenceRate != 75 {
		t.Fatalf("desired/reported convergence = %.2f, want 75", result.DesiredReportedConvergenceRate)
	}
}

func TestAggregateFlowResultsCountsClientTokenCorrelation(t *testing.T) {
	result := aggregateFlowResults(Stage{Name: "25k", ConnectedDevices: 25000}, []ActorFlowResult{
		{Presence: PresenceOnlineSteady, Converged: true, DeltaCleared: true, ClientTokens: []string{"desired-1", "reported-1"}},
		{Presence: PresenceOfflineDesiredQueue, Converged: true, OfflineConverged: true, DeltaCleared: true, ClientTokens: []string{"desired-2"}},
	})
	if result.ClientTokenCorrelationCount != 3 {
		t.Fatalf("client token correlation count = %d, want 3", result.ClientTokenCorrelationCount)
	}
}

func TestExecuteStagesCanHonorConfiguredStageDurations(t *testing.T) {
	plan, err := NewPlan(PlanOptions{
		EnvRoot:       "cloud_env/staging/lke",
		Brandname:     "RTK",
		Region:        "us-sea",
		StageWarmUp:   "1ms",
		StageSteady:   "2ms",
		StageCoolDown: "3ms",
	})
	if err != nil {
		t.Fatalf("NewPlan() error = %v", err)
	}
	plan.Stages = plan.Stages[:1]

	sleeps := []time.Duration{}
	oldSleep := stageSleep
	stageSleep = func(duration time.Duration) {
		sleeps = append(sleeps, duration)
	}
	defer func() { stageSleep = oldSleep }()

	if _, err := ExecuteStages(plan, StageExecutionOptions{SampleFlowsPerPresence: 1, HonorStageDurations: true}); err != nil {
		t.Fatalf("ExecuteStages() error = %v", err)
	}
	want := []time.Duration{time.Millisecond, 2 * time.Millisecond, 3 * time.Millisecond}
	if len(sleeps) != len(want) {
		t.Fatalf("sleeps = %#v, want %#v", sleeps, want)
	}
	for idx := range want {
		if sleeps[idx] != want[idx] {
			t.Fatalf("sleeps = %#v, want %#v", sleeps, want)
		}
	}
}
