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
	if len(results) != 1 {
		t.Fatalf("stage results = %d, want 1", len(results))
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
		if result.DeviceMQTTTotals.ConnectAttempts == 0 || result.DeviceMQTTTotals.Publishes == 0 || result.DeviceMQTTTotals.ReceivedMessages == 0 {
			t.Fatalf("%s device MQTT totals missing: %+v", result.Name, result.DeviceMQTTTotals)
		}
		if result.AppUserTotals.LoginAttempts == 0 || result.AppUserTotals.DesiredWrites == 0 || result.AppUserTotals.ReceivedAcks == 0 {
			t.Fatalf("%s APP/user totals missing: %+v", result.Name, result.AppUserTotals)
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

func TestAggregateStageResultsSumsDeviceAndAppTotals(t *testing.T) {
	result := aggregateStageResults([]StageResult{
		{
			Name:                  "25k",
			ConnectedDevices:      10000,
			ShardConnectedDevices: 4000,
			DeviceMQTTTotals: DeviceMQTTTotals{
				ConnectAttempts:     10,
				ActiveConnections:   4000,
				ActiveSubscriptions: 4000,
				Publishes:           20,
				BytesSent:           100,
			},
			AppUserTotals: AppUserTotals{
				LoginAttempts: 2,
				DesiredWrites: 5,
				BytesReceived: 50,
			},
			FailureReasons: map[string]int64{
				"app_desired_publish_failed": 3,
			},
			StageDiagnostics: []map[string]any{{"skip_reason": "device_connect_target_missed"}},
		},
		{
			Name:                  "25k",
			ConnectedDevices:      10000,
			ShardConnectedDevices: 6000,
			DeviceMQTTTotals: DeviceMQTTTotals{
				ConnectAttempts:     11,
				ActiveConnections:   6000,
				ActiveSubscriptions: 6000,
				Publishes:           21,
				BytesSent:           101,
			},
			AppUserTotals: AppUserTotals{
				LoginAttempts: 3,
				DesiredWrites: 6,
				BytesReceived: 51,
			},
			FailureReasons: map[string]int64{
				"app_desired_publish_failed": 5,
				"device_delta_wait_failed":   7,
			},
			StageDiagnostics: []map[string]any{{"skip_reason": "zero_desired_writes_scheduled_or_attempted"}},
		},
	})
	if result.DeviceMQTTTotals.ConnectAttempts != 21 || result.DeviceMQTTTotals.Publishes != 41 || result.DeviceMQTTTotals.BytesSent != 201 {
		t.Fatalf("device totals not summed: %+v", result.DeviceMQTTTotals)
	}
	if result.DeviceMQTTTotals.ActiveConnections != 10000 || result.DeviceMQTTTotals.ActiveSubscriptions != 10000 {
		t.Fatalf("active gauges not summed across shards: %+v", result.DeviceMQTTTotals)
	}
	if result.ShardConnectedDevices != 10000 {
		t.Fatalf("shard connected devices = %d, want 10000", result.ShardConnectedDevices)
	}
	if result.AppUserTotals.LoginAttempts != 5 || result.AppUserTotals.DesiredWrites != 11 || result.AppUserTotals.BytesReceived != 101 {
		t.Fatalf("app totals not summed: %+v", result.AppUserTotals)
	}
	if result.FailureReasons["app_desired_publish_failed"] != 8 || result.FailureReasons["device_delta_wait_failed"] != 7 {
		t.Fatalf("failure reasons not summed: %+v", result.FailureReasons)
	}
	if len(result.StageDiagnostics) != 2 {
		t.Fatalf("stage diagnostics len = %d, want 2", len(result.StageDiagnostics))
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
