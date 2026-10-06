package deploymentplan

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestCompleteQualificationRequiresEveryActualGateForEveryEnvironment(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			in, definition, live := fixture(t, environment, "Deployment")
			plan, err := Compile(in, []Definition{definition}, []Object{live})
			if err != nil {
				t.Fatal(err)
			}
			// All environments run the same engine, including protected local
			// fixtures. Environment names cannot waive a required gate.
			options := QualificationOptions{Production: true}
			checks := map[string]GateCheck{}
			for _, id := range RequiredGateIDs(true) {
				checks[id] = func(context.Context, *Plan) (GateEvidence, error) {
					return GateEvidence{Digest("actual fixture verification")}, nil
				}
			}
			result, err := plan.Qualify(context.Background(), options, checks)
			if err != nil || result.Decision != "GO" || result.PlanSHA256 != plan.SHA256() {
				t.Fatal("complete verified fixture rejected", err)
			}
			for _, id := range RequiredGateIDs(true) {
				original := checks[id]
				for _, replacement := range []GateCheck{nil, func(context.Context, *Plan) (GateEvidence, error) { return GateEvidence{}, nil }, func(context.Context, *Plan) (GateEvidence, error) {
					return GateEvidence{}, errors.New("credential=never-display-this")
				}} {
					checks[id] = replacement
					result, err = plan.Qualify(context.Background(), options, checks)
					if err == nil || result.Decision != "NO-GO" {
						t.Fatal("required missing/failed evidence was GO", id)
					}
					raw, _ := json.Marshal(result)
					if strings.Contains(string(raw), "credential=") {
						t.Fatal("report leaked backend error")
					}
				}
				checks[id] = original
			}
			options.Fast = true
			result, err = plan.Qualify(context.Background(), options, checks)
			if err == nil || result.Decision != "NO-GO" {
				t.Fatal("fast qualified full environment")
			}
			for _, gate := range result.Gates {
				if !gate.Required || gate.Status != "SKIPPED" {
					t.Fatal("fast changed required gate")
				}
			}
		})
	}
}

func TestQualificationRejectsPlanMutationAndKeepsStableGateOrder(t *testing.T) {
	in, definition, live := fixture(t, "prod", "Deployment")
	plan, _ := Compile(in, []Definition{definition}, []Object{live})
	checks := map[string]GateCheck{}
	for i, id := range RequiredGateIDs(true) {
		checks[id] = func(context.Context, *Plan) (GateEvidence, error) { return GateEvidence{Digest("verified")}, nil }
		if i == 0 {
			checks[id] = func(_ context.Context, p *Plan) (GateEvidence, error) {
				p.ConfigurationSHA256 = Digest("concurrent input")
				return GateEvidence{Digest("verified")}, nil
			}
		}
	}
	result, err := plan.Qualify(context.Background(), QualificationOptions{Production: true}, checks)
	if err == nil || result.Decision != "NO-GO" {
		t.Fatal("mutated plan qualified")
	}
	for i, id := range RequiredGateIDs(true) {
		if result.Gates[i].ID != id || result.Gates[i].Status == "PASS" {
			t.Fatal("unstable gate order or invalid evidence accepted")
		}
	}
}

func TestQualificationReportsActionableTypedBlockers(t *testing.T) {
	in, definition, live := fixture(t, "dev", "Deployment")
	plan, err := Compile(in, []Definition{definition}, []Object{live})
	if err != nil {
		t.Fatal(err)
	}
	blocker := &Blocker{ID: "deployment.admission", Resource: ID(definition.Object), Cause: "immutable selector differs", Next: "qualify a separate owner migration"}
	result, err := plan.Qualify(context.Background(), QualificationOptions{}, map[string]GateCheck{
		"deployment.execution": func(context.Context, *Plan) (GateEvidence, error) { return GateEvidence{}, blocker },
	})
	if err == nil || result.Decision != "NO-GO" {
		t.Fatal("failed admission was approved")
	}
	for _, gate := range result.Gates {
		if gate.ID == "deployment.execution" && (gate.Cause != blocker.Cause+" ("+blocker.Resource+")" || gate.Next != blocker.Next || gate.Impact == "" || gate.Recheck == "") {
			t.Fatal("concrete migration repair or qualification scope was lost", gate)
		}
	}
}
