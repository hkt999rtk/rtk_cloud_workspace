package main

import (
	"context"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"sync"
	"testing"
	"time"
)

func programWindowChainFixture(t *testing.T, started time.Time) (*environmentCreateExecution, createNormalTailProgramAdmission) {
	t.Helper()
	cfg, e, a := normalTailProgramFixture(t)
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		t.Fatal(err)
	}
	a.CheckerSHA256 = checker
	a.QualifiedAt = started.Add(-time.Minute)
	a.ValidUntil = started.Add(time.Minute)
	if err := saveCreateNormalTailProgramArtifact(e.Store, e, a); err != nil {
		t.Fatal(err)
	}
	if err := writeEnvironmentPreflightActive(e.Store, environmentPreflightActive{Schema: environmentPreflightActiveSchema, Generation: a.Generation, Environment: cfg.Environment, Decision: "GO", PlanSHA256: deploymentplan.Digest(a.Program), ProofSHA256: deploymentplan.Digest(a), Artifact: deploymentplan.Digest(a.Program) + "/qualification-1"}); err != nil {
		t.Fatal(err)
	}
	original, err := admitCreateNormalTailProgramWithFence(t.Context(), cfg, &createNormalTailHandoff{Execution: e}, a, 45*time.Minute, started, func(ctx context.Context) error { return e.Check(ctx) })
	if err != nil {
		t.Fatal(err)
	}
	return e, original
}
func TestCreateNormalTailProgramWindowChainKeepsOriginalAndAllEarlierBytes(t *testing.T) {
	e, original := programWindowChainFixture(t, time.Now().UTC().Add(-time.Second))
	journal := deploymentplan.Digest(e.Journal)
	artifact, _ := os.ReadFile(createNormalTailProgramPath(e))
	first, err := admitCreateNormalTailProgramWindow(t.Context(), e, original, time.Now().UTC(), 180*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	base, _ := os.ReadFile(createNormalTailProgramWindowPath(e))
	next, err := admitCreateNormalTailProgramWindow(t.Context(), e, original, time.Now().UTC(), 360*time.Minute)
	if err != nil || next.AdmittedAt != original.AdmittedAt || next.Deadline != original.AdmittedAt.Add(360*time.Minute) || first.Deadline != original.AdmittedAt.Add(180*time.Minute) {
		t.Fatal("fixed-anchor extension", err)
	}
	secondPath := createNormalTailProgramWindowNumberPath(e, 2)
	second, _ := os.ReadFile(secondPath)
	records, err := readCreateNormalTailProgramWindows(e.Store, e, original)
	if err != nil || len(records) != 2 || records[1].Window.PreviousSHA256 != environmentPreflightBytesSHA256(base) || records[0].Window.PreviousSHA256 != "" {
		t.Fatal("unbound append-only chain", err)
	}
	for _, total := range []time.Duration{0, 360 * time.Minute} {
		retry, err := admitCreateNormalTailProgramWindow(t.Context(), e, original, time.Now().UTC(), total)
		if err != nil || retry != next {
			t.Fatal("retry reset clock", err)
		}
	}
	if _, err := admitCreateNormalTailProgramWindow(t.Context(), e, original, time.Now().UTC(), 180*time.Minute); err == nil {
		t.Fatal("lower total silently reused larger authority")
	}
	// A competing exclusive writer cannot replace a genuine earlier record.
	if err := writeExclusivePrivateDeploymentJSON(secondPath, []byte(`{"foreign":true}`)); err == nil {
		t.Fatal("exclusive record overwritten")
	}
	baseAfter, _ := os.ReadFile(createNormalTailProgramWindowPath(e))
	secondAfter, _ := os.ReadFile(secondPath)
	artifactAfter, _ := os.ReadFile(createNormalTailProgramPath(e))
	if string(base) != string(baseAfter) || string(second) != string(secondAfter) || string(artifact) != string(artifactAfter) || deploymentplan.Digest(e.Journal) != journal {
		t.Fatal("original history mutated")
	}
	effective, err := readCreateNormalTailProgramAdmission(e.Store, e)
	if err != nil || effective != next {
		t.Fatal("semantic effective reader differs", err)
	}
	rawOriginal, err := readCreateNormalTailProgramOriginalAdmission(e.Store, e)
	if err != nil || rawOriginal != original {
		t.Fatal("original admission mutated", err)
	}
}
func TestCreateNormalTailProgramWindowChainHistoricalExpiryNeedsExplicitLargerTotal(t *testing.T) {
	started := time.Now().UTC().Add(-7 * time.Hour)
	e, original := programWindowChainFixture(t, started)
	if _, err := admitCreateNormalTailProgramWindow(t.Context(), e, original, started.Add(time.Hour), 180*time.Minute); err != nil {
		t.Fatal(err)
	}
	want, err := admitCreateNormalTailProgramWindow(t.Context(), e, original, started.Add(2*time.Hour), 360*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	historical, err := readCreateNormalTailProgramAdmission(e.Store, e)
	if err != nil || historical != want || !time.Now().After(historical.Deadline) {
		t.Fatal("expired genuine historical window rejected", err)
	}
	for _, total := range []time.Duration{0, 360 * time.Minute} {
		if _, err := admitCreateNormalTailProgramWindow(t.Context(), e, original, time.Now().UTC(), total); err == nil {
			t.Fatal("expired window automatically renewed")
		}
	}
	if _, err := os.Lstat(createNormalTailProgramWindowNumberPath(e, 3)); !os.IsNotExist(err) {
		t.Fatal("implicit window created")
	}
	var artifact createNormalTailProgramArtifact
	if err := readEnvironmentPreflightJSON(createNormalTailProgramPath(e), &artifact); err != nil {
		t.Fatal(err)
	}
	// This is the actual create resume admission boundary: immutable program
	// and held fence are checked before the explicit total reaches the writer.
	next, err := admitCreateNormalTailProgramWithFence(t.Context(), deploymentConfig{}, &createNormalTailHandoff{Execution: e}, artifact, 45*time.Minute, time.Now().UTC(), func(ctx context.Context) error { return e.Check(ctx) }, 480*time.Minute)
	if err != nil || next.Deadline != original.AdmittedAt.Add(480*time.Minute) {
		t.Fatal("explicit larger total lost original anchor", err)
	}
}
func TestCreateNormalTailProgramWindowChainRefusesGapsTamperingAndPartialWrites(t *testing.T) {
	e, original := programWindowChainFixture(t, time.Now().UTC().Add(-time.Second))
	for _, total := range []time.Duration{180 * time.Minute, 360 * time.Minute} {
		if _, err := admitCreateNormalTailProgramWindow(t.Context(), e, original, time.Now().UTC(), total); err != nil {
			t.Fatal(err)
		}
	}
	basePath, secondPath := createNormalTailProgramWindowPath(e), createNormalTailProgramWindowNumberPath(e, 2)
	base, _ := os.ReadFile(basePath)
	second, _ := os.ReadFile(secondPath)
	for _, mode := range []string{"orphan", "gap", "wrong-link", "lower-total", "foreign-owner", "changed-anchor", "future-record", "unsafe-file", "symlink", "malformed-name", "partial-write"} {
		t.Run(mode, func(t *testing.T) {
			if err := os.WriteFile(basePath, base, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(secondPath, second, 0600); err != nil {
				t.Fatal(err)
			}
			_ = os.Chmod(secondPath, 0600)
			var changed createNormalTailProgramWindow
			_ = decodeEnvironmentJSON(second, &changed)
			cleanup := func() {}
			switch mode {
			case "orphan":
				_ = os.Rename(basePath, basePath+".saved")
				cleanup = func() { _ = os.Rename(basePath+".saved", basePath) }
			case "gap":
				p := createNormalTailProgramWindowNumberPath(e, 3)
				_ = os.Rename(secondPath, p)
				cleanup = func() { _ = os.Rename(p, secondPath) }
			case "wrong-link":
				changed.PreviousSHA256 = deploymentplan.Digest("foreign prior")
			case "lower-total":
				changed.TotalTimeout = 180 * time.Minute
				changed.Deadline = changed.OriginalAdmittedAt.Add(changed.TotalTimeout)
			case "foreign-owner":
				changed.OwnerSHA256 = deploymentplan.Digest("foreign owner")
			case "changed-anchor":
				changed.OriginalAdmittedAt = changed.OriginalAdmittedAt.Add(time.Minute)
				changed.Deadline = changed.OriginalAdmittedAt.Add(changed.TotalTimeout)
			case "future-record":
				changed.RecordedAt = time.Now().Add(time.Hour)
			case "unsafe-file":
				_ = os.Chmod(secondPath, 0644)
			case "symlink":
				_ = os.Remove(secondPath)
				_ = os.Symlink(basePath, secondPath)
				cleanup = func() { _ = os.Remove(secondPath) }
			case "malformed-name":
				p := filepath.Join(e.Directory, "normal-tail-program-window-other.json")
				_ = os.WriteFile(p, second, 0600)
				cleanup = func() { _ = os.Remove(p) }
			case "partial-write":
				_ = os.WriteFile(secondPath, []byte(`{"Schema":`), 0600)
			}
			defer cleanup()
			switch mode {
			case "wrong-link", "lower-total", "foreign-owner", "changed-anchor", "future-record":
				_ = os.WriteFile(secondPath, mustProviderRecoveryJSON(changed), 0600)
			}
			if _, err := readCreateNormalTailProgramWindow(e.Store, e, original); err == nil {
				t.Fatal("invalid chain accepted")
			}
			if _, err := admitCreateNormalTailProgramWindow(t.Context(), e, original, time.Now().UTC(), 480*time.Minute); err == nil {
				t.Fatal("new authority appended over unresolved chain")
			}
		})
	}
}
func TestCreateNormalTailProgramWindowChainConcurrentSameTotalRetainsOneRecord(t *testing.T) {
	e, original := programWindowChainFixture(t, time.Now().UTC().Add(-time.Second))
	if _, err := admitCreateNormalTailProgramWindow(t.Context(), e, original, time.Now().UTC(), 180*time.Minute); err != nil {
		t.Fatal(err)
	}
	base, _ := os.ReadFile(createNormalTailProgramWindowPath(e))
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := admitCreateNormalTailProgramWindow(t.Context(), e, original, time.Now().UTC(), 360*time.Minute)
			results <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes == 0 {
		t.Fatal("no exclusive writer completed")
	}
	records, err := readCreateNormalTailProgramWindows(e.Store, e, original)
	if err != nil || len(records) != 2 || records[1].Window.Deadline != original.AdmittedAt.Add(360*time.Minute) {
		t.Fatal("concurrent window changed fixed total", err)
	}
	after, _ := os.ReadFile(createNormalTailProgramWindowPath(e))
	if string(base) != string(after) {
		t.Fatal("concurrent append rewrote original")
	}
	if _, err := admitCreateNormalTailProgramWindow(t.Context(), e, original, time.Now().UTC(), 360*time.Minute); err != nil {
		t.Fatal("known complete winning record cannot resume", err)
	}
}
