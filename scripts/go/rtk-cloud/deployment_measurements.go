package main

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"
)

// Measurements are diagnostic data, kept outside plans and qualification
// artifacts so timestamps cannot alter admitted or historical evidence digests.
type deploymentMeasurementsKey struct{}
type deploymentMeasurement struct {
	Event       string     `json:"event"`
	Stage       string     `json:"stage"`
	Kind        string     `json:"kind,omitempty"`
	StartedAt   time.Time  `json:"started_at"`
	CompletedAt *time.Time `json:"completed_at,omitempty"`
	DurationMS  float64    `json:"duration_ms,omitempty"`
	QueryCount  int        `json:"query_count"`
	CacheHits   int        `json:"cache_hits"`
	Status      string     `json:"status,omitempty"`
}
type deploymentMeasurementScope struct {
	stage         string
	start         time.Time
	queries, hits int
}
type deploymentMeasurements struct {
	mu     sync.Mutex
	log    *environmentProgressLog
	active []*deploymentMeasurementScope
	run    *deploymentMeasurementScope
}

func openDeploymentMeasurements(ctx context.Context, environment, action string) (context.Context, func() error, error) {
	if _, ok := ctx.Value(deploymentMeasurementsKey{}).(*deploymentMeasurements); ok {
		return ctx, func() error { return nil }, nil
	}
	log, err := openEnvironmentProgressLog(environment, "metrics")
	if err != nil {
		return ctx, nil, err
	}
	run := &deploymentMeasurementScope{stage: "deployment." + action, start: time.Now()}
	m := &deploymentMeasurements{log: log, run: run}
	m.write(deploymentMeasurement{Event: "run-start", Stage: run.stage, StartedAt: run.start.UTC()})
	ctx = context.WithValue(ctx, deploymentInvocationCacheKey{}, &deploymentInvocationCache{values: map[string][]byte{}})
	return context.WithValue(ctx, deploymentMeasurementsKey{}, m), func() error {
		m.mu.Lock()
		m.finish(run, "run-complete", "")
		m.mu.Unlock()
		return log.Close()
	}, nil
}

func (m *deploymentMeasurements) write(row deploymentMeasurement) {
	raw, err := json.Marshal(row)
	if err != nil {
		return
	}
	_, _ = m.log.Write(append(raw, '\n')) // Close reports any durable write error.
}
func (m *deploymentMeasurements) finish(s *deploymentMeasurementScope, event, status string) {
	now := time.Now()
	utc := now.UTC()
	m.write(deploymentMeasurement{Event: event, Stage: s.stage, StartedAt: s.start.UTC(), CompletedAt: &utc, DurationMS: float64(now.Sub(s.start)) / float64(time.Millisecond), QueryCount: s.queries, CacheHits: s.hits, Status: status})
}

func beginDeploymentMeasurement(ctx context.Context, stage string) func(error) {
	m, ok := ctx.Value(deploymentMeasurementsKey{}).(*deploymentMeasurements)
	if !ok {
		return func(error) {}
	}
	s := &deploymentMeasurementScope{stage: stage, start: time.Now()}
	m.mu.Lock()
	m.active = append(m.active, s)
	m.write(deploymentMeasurement{Event: "gate-start", Stage: stage, StartedAt: s.start.UTC()})
	m.mu.Unlock()
	var once sync.Once
	return func(err error) {
		once.Do(func() {
			m.mu.Lock()
			defer m.mu.Unlock()
			status := "PASS"
			if err != nil || ctx.Err() != nil {
				status = "FAIL"
			}
			for i, current := range m.active {
				if current == s {
					m.active = append(m.active[:i], m.active[i+1:]...)
					break
				}
			}
			m.finish(s, "gate-complete", status)
		})
	}
}

// Fixed kind names only. Never record arguments, cache keys, URLs, bodies or
// errors: these may contain credentials and private identity material.
func beginDeploymentQuery(ctx context.Context, kind string) func(error) {
	switch kind {
	case "kubectl", "pki-http", "provider-http", "dns-http", "github", "docker", "git":
	default:
		return func(error) {}
	}
	m, ok := ctx.Value(deploymentMeasurementsKey{}).(*deploymentMeasurements)
	if !ok {
		return func(error) {}
	}
	start := time.Now()
	m.mu.Lock()
	stage := "deployment.preparation"
	if len(m.active) > 0 {
		stage = m.active[len(m.active)-1].stage
	}
	m.run.queries++
	for _, s := range m.active {
		s.queries++
	}
	m.mu.Unlock()
	return func(err error) {
		now := time.Now()
		utc := now.UTC()
		status := "PASS"
		if err != nil {
			status = "FAIL"
		}
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			status = "CANCELLED"
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		m.write(deploymentMeasurement{Event: "probe-complete", Stage: stage, Kind: kind, StartedAt: start.UTC(), CompletedAt: &utc, DurationMS: float64(now.Sub(start)) / float64(time.Millisecond), QueryCount: 1, Status: status})
	}
}
func deploymentMeasurementCacheHit(ctx context.Context) {
	deploymentMeasurementCacheHitKind(ctx, "kubernetes-read")
}
func deploymentMeasurementCacheHitKind(ctx context.Context, kind string) {
	m, ok := ctx.Value(deploymentMeasurementsKey{}).(*deploymentMeasurements)
	if !ok {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.run.hits++
	stage := "deployment.preparation"
	for _, s := range m.active {
		s.hits++
		stage = s.stage
	}
	m.write(deploymentMeasurement{Event: "cache-hit", Stage: stage, Kind: kind, StartedAt: time.Now().UTC(), CacheHits: 1})
}
