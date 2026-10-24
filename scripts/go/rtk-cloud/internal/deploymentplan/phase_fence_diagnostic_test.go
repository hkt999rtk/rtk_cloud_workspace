package deploymentplan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestExecutionPhaseFencePreservesTypedBlockerAndRedactsUnknown(t *testing.T) {
	for _, callback := range []string{"snapshot", "legacy"} {
		for _, boundary := range []string{"first", "after-completed-phase"} {
			for _, mode := range []string{"typed-resource", "typed-empty-resource", "untyped"} {
				t.Run(callback+"/"+boundary+"/"+mode, func(t *testing.T) {
					f := phaseObservationProfile(t, "dev", false)
					original := f.plan.SHA256()
					failure := &Blocker{ID: "deployment.runtime.dependency", Resource: f.retained, Cause: "the original retained input owner changed", Next: "reconcile the recorded owner and resume the admitted execution"}
					if mode == "typed-empty-resource" {
						failure.Resource = ""
					}
					before := *failure
					var source error = fmt.Errorf("token=PRIVATE_PHASE_CREDENTIAL: %w", failure)
					if mode == "untyped" {
						source = errors.New("token=PRIVATE_PHASE_CREDENTIAL")
					}
					calls := 0
					fence := func(context.Context, *Plan) error {
						calls++
						if boundary == "first" || calls == 2 {
							return source
						}
						return nil
					}
					qualified := func(context.Context, *Plan) error { return nil }
					ordinary := fence
					var snapshots []PhaseFence
					if callback == "snapshot" {
						ordinary = qualified
						snapshots = []PhaseFence{func(ctx context.Context, p *Plan) (map[string]Object, error) {
							if err := fence(ctx, p); err != nil {
								return nil, err
							}
							return f.backend.snapshot(), nil
						}}
					}
					accepted := 0
					err := f.plan.Execute(t.Context(), f.backend, original, f.root, qualified, ordinary, func(context.Context, *Plan) error { accepted++; return nil }, snapshots...)
					var got *Blocker
					if !errors.As(err, &got) || got == nil || strings.Contains(err.Error(), "PRIVATE_PHASE_CREDENTIAL") {
						t.Fatal("phase failure lost its safe typed diagnostic or exposed callback output", err)
					}
					failed, completed, writes := f.first, 0, 0
					if boundary == "after-completed-phase" {
						failed, completed, writes = f.future, 2, 1
					}
					if mode == "untyped" {
						if got.ID != "deployment.inputs" || got.Resource != failed || got.Cause != "environment configuration, release or PKI bindings changed" {
							t.Fatal("unexpected error was not safely generalized", got)
						}
					} else {
						wanted := before
						if wanted.Resource == "" {
							wanted.Resource = failed
						}
						if *got != wanted || got == failure || *failure != before {
							t.Fatal("typed blocker fields or original callback error changed", got)
						}
					}
					j := readExecutionJournal(t, f.root, original)
					if j.Failed != failed || len(j.Completed) != completed || j.InFlight != "" || j.Accepted || len(f.backend.written) != writes || f.backend.leaseHeld || accepted != 0 || f.plan.SHA256() != original {
						t.Fatal("diagnostic preservation changed stop, write, lease or journal behavior", j)
					}
					raw, readErr := os.ReadFile(filepath.Join(f.root, "deployment", "rollback", original, "journal.json"))
					if readErr != nil || strings.Contains(string(raw), "PRIVATE_PHASE_CREDENTIAL") {
						t.Fatal("private callback output entered the durable journal", readErr)
					}
				})
			}
		}
	}
}
