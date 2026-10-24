package deploymentplan

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestExecutionReportsValidatedPhaseOutcomesAndStopsOnFailure(t *testing.T) {
	for _, mode := range []string{"success", "qualification", "cancel", "admission", "fence", "apply", "verification", "acceptance"} {
		t.Run(mode, func(t *testing.T) {
			f := phaseObservationProfile(t, "dev", false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			qualified := func(context.Context, *Plan) error {
				if mode == "qualification" {
					return errors.New("private-qualification-error")
				}
				if mode == "cancel" {
					cancel()
				}
				return nil
			}
			fence := func(context.Context, *Plan) error {
				if mode == "fence" {
					return errors.New("private-fence-error")
				}
				return nil
			}
			accept := func(context.Context, *Plan) error {
				if mode == "acceptance" {
					return errors.New("private-acceptance-error")
				}
				return nil
			}
			if mode == "admission" {
				f.backend.failID = f.first
			}
			if mode == "apply" {
				f.backend.failWait = true
			}
			if mode == "verification" {
				f.backend.progressHook = func(_, step string) {
					if step == "verification" {
						Map(f.backend.objects[f.first]["data"])["value"] = "concurrent"
					}
				}
			}
			err := f.plan.Execute(ctx, f.backend, f.plan.SHA256(), f.root, qualified, fence, accept)
			if (err == nil) != (mode == "success") || f.backend.leaseHeld {
				t.Fatal("progress changed execution outcome or cleanup", mode, err)
			}
			results := f.backend.progressResults
			if mode == "success" {
				want := []executionPhaseResult{{"", "qualification", true}, {"", "admission", true}, {"", "lease", true}, {"configuration", "fence", true}, {"configuration", "apply", true}, {"workload", "fence", true}, {"workload", "apply", true}, {"", "verification", true}, {"", "acceptance", true}, {"", "verification", true}, {"", "ownership", true}, {"", "journal", true}, {"", "cleanup", true}}
				if !reflect.DeepEqual(results, want) || !readExecutionJournal(t, f.root, f.plan.SHA256()).Accepted {
					t.Fatal("successful phases stayed running or were accepted prematurely", results)
				}
				return
			}
			if mode != "qualification" && mode != "cancel" && mode != "admission" {
				if got := results[len(results)-1]; got.Step != "cleanup" || !got.Passed {
					t.Fatal("cleanup did not report its independent outcome", results)
				}
				results = results[:len(results)-1]
			}
			last := results[len(results)-1]
			wantStep := mode
			if mode == "cancel" {
				wantStep = "qualification"
			}
			if last.Passed || last.Step != wantStep {
				t.Fatal("failed or cancelled phase was reported OK", results)
			}
			for _, result := range results[:len(results)-1] {
				if !result.Passed {
					t.Fatal("execution advanced past a failed phase", results)
				}
			}
		})
	}
}
