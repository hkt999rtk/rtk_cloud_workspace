package main

import (
	"context"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateNormalTailProgramAdmittedRepairKeepsOriginalCheckerHistoryAndDeadline(t *testing.T) {
	for _, mode := range []string{"admitted-repair", "fixed-window-expired", "changed-program", "canceled-fence", "first-admission-wrong-checker"} {
		t.Run(mode, func(t *testing.T) {
			cfg, e, a := normalTailProgramFixture(t)
			checker, err := environmentPreflightCheckerSHA256()
			if err != nil {
				t.Fatal(err)
			}
			// The existing fingerprint cache models a different repaired binary;
			// the serialized original artifact and admission remain untouched.
			t.Cleanup(func() { environmentPreflightCheckerCache.sha = checker })
			a.CheckerSHA256 = checker
			if err = saveCreateNormalTailProgramArtifact(e.Store, e, a); err != nil {
				t.Fatal(err)
			}
			if err = writeEnvironmentPreflightActive(e.Store, environmentPreflightActive{Schema: environmentPreflightActiveSchema, Generation: a.Generation, Environment: cfg.Environment, Decision: "GO", PlanSHA256: deploymentplan.Digest(a.Program), ProofSHA256: deploymentplan.Digest(a), Artifact: deploymentplan.Digest(a.Program) + "/qualification-1"}); err != nil {
				t.Fatal(err)
			}
			h := &createNormalTailHandoff{Execution: e}
			fence := func(ctx context.Context) error { return e.Check(ctx) }
			now := time.Now().UTC()
			var original createNormalTailProgramAdmission
			if mode != "first-admission-wrong-checker" {
				original, err = admitCreateNormalTailProgramWithFence(t.Context(), cfg, h, a, 45*time.Minute, now, fence)
				if err != nil {
					t.Fatal("genuine first admission", err)
				}
			}
			journalSHA := deploymentplan.Digest(e.Journal)
			artifactSHA := deploymentplan.Digest(a)
			environmentPreflightCheckerCache.sha = deploymentplan.Digest("repaired checker")
			retryAt := now.Add(20 * time.Minute) // Original GO TTL is over; its admitted clock is live.
			ctx := t.Context()
			switch mode {
			case "fixed-window-expired":
				retryAt = original.Deadline
			case "changed-program":
				a.Program.ConfigurationSHA256 = deploymentplan.Digest("foreign inputs")
			case "canceled-fence":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "first-admission-wrong-checker":
				retryAt = now
			}
			got, err := admitCreateNormalTailProgramWithFence(ctx, cfg, h, a, 3*time.Hour, retryAt, fence)
			if mode == "admitted-repair" {
				if err != nil || got != original || !got.Deadline.Equal(now.Add(45*time.Minute)) {
					t.Fatal("repaired executable did not reuse the original admitted clock", err)
				}
			} else if err == nil {
				t.Fatal("invalid repair or unqualified first admission was accepted")
			}
			var recorded createNormalTailProgramArtifact
			if readEnvironmentPreflightJSON(createNormalTailProgramPath(e), &recorded) != nil || deploymentplan.Digest(recorded) != artifactSHA || deploymentplan.Digest(e.Journal) != journalSHA {
				t.Fatal("retry rewrote original qualification or admission history")
			}
		})
	}
}
