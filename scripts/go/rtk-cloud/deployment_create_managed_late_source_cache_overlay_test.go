package main

import (
	"context"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type overlayCompletedHistoryFixture struct {
	cfg      deploymentConfig
	store    secretStore
	actual   *environmentCreateExecution
	recovery *createServiceSessionRecovery
	original environmentCreateJournal
	pair     string
}

func overlayCompletedHistory(t *testing.T) overlayCompletedHistoryFixture {
	t.Helper()
	cfg, _, _, store, e, _, _ := createCompletedIngressFixture(t)
	original := managedLateExpiredRenewalNegativeJournal()
	original.PlanSHA256, original.Stack, original.Environment, original.OwnerToken = e.Journal.PlanSHA256, e.Journal.Stack, e.Journal.Environment, e.Journal.OwnerToken
	view := *e
	view.Journal = e.Journal
	view.Journal.Entries = slices.Clone(e.Journal.Entries[:392])
	view.Journal.KubernetesLeases = nil
	view.Journal.CompletedAt = time.Time{}
	pure := *e
	pure.Journal = original
	cap, err := newManagedLateSourceReuse(store, &pure)
	if err != nil {
		t.Fatal(err)
	}
	cap.currentSHA, cap.boundedSHA = deploymentplan.Digest(e.Journal), deploymentplan.Digest(view.Journal)
	e.lateSourceReuse, view.lateSourceReuse = cap, cap
	tail := &createManagedLateCompletedTail{SourceSHA: deploymentplan.Digest(e.Journal), OwnerSHA: deploymentplan.Digest(e.owner), ViewSHA: deploymentplan.Digest(view.Journal), ProofSHA: strings.Repeat("1", 64), AuthoritySHA: strings.Repeat("2", 64), LateReview: strings.Repeat("3", 64), SourceLeases: len(e.Journal.KubernetesLeases), OverallDeadline: time.Now().Add(time.Minute), NativeEvidence: map[string]string{"fresh-native": "unchanged"}, view: &view}
	r := &createServiceSessionRecovery{normalTail: &createManagedLateNormalTail{}, completedTail: tail}
	pair := filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/service/managed-late-normal-tail-timeout-supplement-authority.json")
	invalidateManagedLateSourceReuse(e)
	managedLateSourceReuseNegativeWrite(t, store, pair, []byte(`{"synthetic":"newly-admitted-pair"}`))
	managedLateSourceReuseNegativeWrite(t, store, strings.Replace(pair, "authority.json", "admission.json", 1), []byte(`{"synthetic":"newly-admitted-pair"}`))
	return overlayCompletedHistoryFixture{cfg, store, e, r, original, pair}
}

func (f overlayCompletedHistoryFixture) proved(t *testing.T) *createServiceSessionRecovery {
	t.Helper()
	pure := *f.actual
	pure.Journal = f.original
	cap, err := newManagedLateSourceReuse(f.store, &pure)
	if err != nil {
		t.Fatal(err)
	}
	tail := *f.recovery.completedTail
	view := *tail.view
	view.lateSourceReuse = cap
	tail.view = &view
	cap.currentSHA, cap.boundedSHA = deploymentplan.Digest(f.actual.Journal), tail.ViewSHA
	f.actual.lateSourceReuse = cap
	return &createServiceSessionRecovery{completedTail: &tail}
}

func TestManagedLateOverlayHistoryRefreshesKnownPairWriteOnlyOnce(t *testing.T) {
	f := overlayCompletedHistory(t)
	old := f.actual.lateSourceReuse
	oldView := f.recovery.completedTail.view
	nativeBefore := deploymentplan.Digest(f.recovery.completedTail.NativeEvidence)
	calls := 0
	loader := func(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution) (*createServiceSessionRecovery, error) {
		calls++
		if ctx.Err() != nil || deploymentplan.Digest(cfg) != deploymentplan.Digest(f.cfg) || store != f.store || e != f.actual {
			t.Fatal("refresh lost exact command inputs")
		}
		return f.proved(t), nil
	}
	for range 4 {
		if err := refreshManagedLateNormalTailHistory(context.Background(), f.cfg, f.store, f.actual, f.recovery, loader); err != nil {
			t.Fatal(err)
		}
	}
	if calls != 1 || f.actual.lateSourceReuse == old || !old.invalid || old.values != nil || f.recovery.completedTail.view == oldView {
		t.Fatal("write boundary revived old scope or repeated strong constructor")
	}
	if f.actual.lateSourceReuse.manifest[f.pair] != environmentPreflightBytesSHA256([]byte(`{"synthetic":"newly-admitted-pair"}`)) {
		t.Fatal("new pair omitted from authenticated input manifest")
	}
	if deploymentplan.Digest(f.recovery.completedTail.NativeEvidence) != nativeBefore {
		t.Fatal("pure refresh modified native evidence")
	}
	oldPairSHA := f.actual.lateSourceReuse.manifest[f.pair]
	managedLateSourceReuseNegativeWrite(t, f.store, f.pair, []byte(`{"synthetic":"changed"}`))
	if err := refreshManagedLateNormalTailHistory(context.Background(), f.cfg, f.store, f.actual, f.recovery, loader); err == nil || calls != 1 || managedLateSourceReuseActive(f.actual) {
		t.Fatal("warm scope ignored input drift or silently reauthenticated changed source")
	}
	if oldPairSHA == "" {
		t.Fatal("fixture did not bind publication")
	}
}

func TestManagedLateOverlayHistoryRejectsForeignProofAndOldScope(t *testing.T) {
	for _, mode := range []string{"source", "owner", "proof", "view", "authority", "old-scope", "missing", "error", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			f := overlayCompletedHistory(t)
			prior, oldView := f.actual.lateSourceReuse, f.recovery.completedTail.view
			ctx := context.Background()
			if mode == "cancelled" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			loader := func(context.Context, deploymentConfig, secretStore, *environmentCreateExecution) (*createServiceSessionRecovery, error) {
				if mode == "error" {
					return nil, errors.New("synthetic source refusal")
				}
				got := f.proved(t)
				switch mode {
				case "source":
					got.completedTail.SourceSHA = strings.Repeat("4", 64)
				case "owner":
					got.completedTail.OwnerSHA = strings.Repeat("4", 64)
				case "proof":
					got.completedTail.ProofSHA = strings.Repeat("4", 64)
				case "view":
					got.completedTail.view.Journal.Entries = slices.Clone(got.completedTail.view.Journal.Entries)
					got.completedTail.view.Journal.Entries[0].Key = "foreign"
				case "authority":
					got.completedTail.AuthoritySHA = strings.Repeat("4", 64)
				case "missing":
					got.completedTail.view = nil
				case "old-scope":
					prior.invalid = false
					prior.values = nil
					f.actual.lateSourceReuse = prior
					got.completedTail.view.lateSourceReuse = prior
				}
				return got, nil
			}
			if err := refreshManagedLateNormalTailHistory(ctx, f.cfg, f.store, f.actual, f.recovery, loader); err == nil {
				t.Fatal("foreign or invalid historical source accepted")
			}
			if f.recovery.completedTail.view != oldView {
				t.Fatal("failed refresh replaced original parsing view")
			}
			if mode != "cancelled" && managedLateSourceReuseActive(f.actual) {
				t.Fatal("failed constructor retained cache authority")
			}
		})
	}
}
