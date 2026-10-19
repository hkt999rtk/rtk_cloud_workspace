package main

import (
	"context"
	"errors"
	"io"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"testing"
	"time"
)

type postCleanupUnknownWriteBackend struct{ *normalTailPhaseBackend }

func (b *postCleanupUnknownWriteBackend) Write(ctx context.Context, action string, o deploymentplan.Object, dry bool) (deploymentplan.Object, error) {
	actual, err := b.normalTailPhaseBackend.Write(ctx, action, o, dry)
	if err != nil || dry {
		return actual, err
	}
	return nil, errors.New("lost ordinary native write response")
}

// The full correction reader authenticates its saved finite recipe before this
// seam. These tests exercise the shared ordinary stage and native journal only.
func TestPostCleanupCorrectionKnownZeroEffectUsesOriginalOrdinaryAdmission(t *testing.T) {
	for _, part := range []string{"prepare", "apply", "retire"} {
		t.Run(part, func(t *testing.T) {
			f := newNormalTailPhaseFixture(t, false)
			stage := createNormalTailPostCleanupStage + "-" + part
			e := f.h.Execution
			check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
			rejected := func(context.Context, *deploymentplan.Plan) error {
				return errors.New("known admission failure before native child")
			}
			run := func(q func(context.Context, *deploymentplan.Plan) error) error {
				return executeCreateNormalTailStageBackend(t.Context(), f.store, f.h, stage, f.p, f.b, q, check, check, io.Discard, func(context.Context) error { return nil })
			}
			if run(rejected) == nil {
				t.Fatal("missing actual ordinary admission interruption")
			}
			entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
			if entry == nil || entry.State != "pending" || len(entry.Receipt) != 0 || len(e.Journal.KubernetesLeases) != 0 || len(f.b.writes) != 0 {
				t.Fatal("admission failure acquired authority or wrote")
			}
			raw, j, err := readCreateNormalTailStage(f.store, e, stage, f.p)
			if err != nil || raw != nil || deploymentplan.Digest(j) != deploymentplan.Digest(deploymentplan.Journal{}) {
				t.Fatal("actual common reader zero-effect proof", err)
			}
			p, j, err := readPostCleanupCorrectionStage(f.store, e, stage, f.p)
			if err != nil || p == nil || p.SHA256() != f.p.SHA256() || j.Accepted {
				t.Fatal("same saved pending plan cannot resume", err)
			}
			qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
				return p.AdmitResumable(ctx, f.b, f.store.Root)
			}
			if err := run(qualify); err != nil {
				t.Fatal("genuine same ordinary stage resume", err)
			}
			p, j, err = readPostCleanupCorrectionStage(f.store, e, stage, f.p)
			if err != nil || p.SHA256() != f.p.SHA256() || !j.Accepted || j.InFlight != "" {
				t.Fatal("native accepted ordinary child", err)
			}
			before := deploymentplan.Digest(f.b.writes)
			if err := run(qualify); err != nil || deploymentplan.Digest(f.b.writes) != before {
				t.Fatal("accepted retry repeated original writes", err)
			}
		})
	}
}
func TestPostCleanupCorrectionKnownZeroEffectRejectsUnknownOrMissingAuthority(t *testing.T) {
	for _, mode := range []string{"missing-intent", "orphan-lease", "allocated-without-child", "unknown-native-response"} {
		t.Run(mode, func(t *testing.T) {
			f := newNormalTailPhaseFixture(t, false)
			e := f.h.Execution
			stage := createNormalTailPostCleanupStage + "-apply"
			if mode != "missing-intent" {
				intent, err := environmentCreateKubernetesStageIntent(f.p)
				if err != nil {
					t.Fatal(err)
				}
				if err = e.BeginEntry("kubernetes/"+stage, stage, intent, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "orphan-lease":
				e.Journal.KubernetesLeases = append(e.Journal.KubernetesLeases, deploymentplan.LeaseOwner{Holder: f.p.SHA256() + ":orphan"})
				if err := e.save(); err != nil {
					t.Fatal(err)
				}
			case "allocated-without-child":
				if err := e.RecordReceipt("kubernetes/"+stage, mustProviderRecoveryJSON(deploymentplan.Object{"plan_sha256": f.p.SHA256(), "owners": deploymentplan.Object{}})); err != nil {
					t.Fatal(err)
				}
			case "unknown-native-response":
				b := &postCleanupUnknownWriteBackend{f.b}
				check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
				q := func(ctx context.Context, p *deploymentplan.Plan) error { return p.AdmitResumable(ctx, b, f.store.Root) }
				if err := executeCreateNormalTailStageBackend(t.Context(), f.store, f.h, stage, f.p, b, q, check, check, io.Discard, func(context.Context) error { return nil }); err == nil {
					t.Fatal("unknown actual write response unexpectedly succeeded")
				}
				j := f.journal(t)
				if j.InFlight == "" || len(f.b.writes) == 0 {
					t.Fatal("actual unknown native outcome absent")
				}
			}
			writes := deploymentplan.Digest(f.b.writes)
			if _, _, err := readPostCleanupCorrectionStage(f.store, e, stage, f.p); err == nil {
				t.Fatal("missing/unknown authority became replay permission")
			}
			if deploymentplan.Digest(f.b.writes) != writes {
				t.Fatal("read-only reconciliation wrote")
			}
		})
	}
}
