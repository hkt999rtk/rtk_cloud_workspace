package deploymentplan

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

func TestQualificationObserverReceivesOnlyFinalVerifiedOutcomes(t *testing.T) {
	for _, outcome := range []string{"success", "missing", "failure", "mutation", "cancel"} {
		t.Run(outcome, func(t *testing.T) {
			in, definition, live := fixture(t, "dev", "Deployment")
			plan, err := Compile(in, []Definition{definition}, []Object{live})
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			checks := map[string]GateCheck{}
			for _, id := range RequiredGateIDs(false) {
				checks[id] = func(context.Context, *Plan) (GateEvidence, error) {
					return GateEvidence{SHA256: Digest("verified fixture")}, nil
				}
			}
			checks["deployment.target"] = func(_ context.Context, p *Plan) (GateEvidence, error) {
				switch outcome {
				case "missing":
					return GateEvidence{}, nil
				case "failure":
					return GateEvidence{}, errors.New("private-error-canary")
				case "mutation":
					p.Inputs.ConfigurationSHA256 = Digest("changed")
				case "cancel":
					cancel()
				}
				return GateEvidence{SHA256: Digest("verified fixture")}, nil
			}
			var observed []GateResult
			result, err := plan.Qualify(ctx, QualificationOptions{ExistingUpdate: true}, checks, func(row GateResult) { observed = append(observed, row) })
			if !reflect.DeepEqual(observed, result.Gates) || (err == nil) != (outcome == "success") {
				t.Fatal("progress disagreed with qualification", result, observed, err)
			}
			want := map[string]string{"success": "PASS", "missing": "BLOCKED", "failure": "FAIL", "mutation": "ERROR", "cancel": "ERROR"}[outcome]
			if observed[0].Status != want {
				t.Fatal("callback completion was mistaken for verified evidence", observed[0])
			}
		})
	}
}
