package deploymentplan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

func TestExistingUpdateRequiresEveryUpdateGateWithoutActivationOrRestoreProjects(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			in, definition, live := fixture(t, environment, "Deployment")
			plan, err := Compile(in, []Definition{definition}, []Object{live})
			if err != nil {
				t.Fatal(err)
			}
			checks := map[string]GateCheck{}
			for _, gate := range fullEnvironmentGates[:6] {
				checks[gate.id] = func(context.Context, *Plan) (GateEvidence, error) {
					return GateEvidence{Digest("fixture update evidence")}, nil
				}
			}
			for _, id := range []string{"deployment.recovery", "deployment.production"} {
				checks[id] = func(context.Context, *Plan) (GateEvidence, error) {
					t.Fatal("update started an activation/restore project")
					return GateEvidence{}, nil
				}
			}
			options := QualificationOptions{Production: environment == "prod", ExistingUpdate: true}
			result, err := plan.Qualify(context.Background(), options, checks)
			if err != nil || result.Decision != "GO" || len(result.Gates) != 6 {
				t.Fatal("wrong update scope", err, result)
			}
			for _, gate := range fullEnvironmentGates[:6] {
				original := checks[gate.id]
				delete(checks, gate.id)
				if result, err := plan.Qualify(context.Background(), options, checks); err == nil || result.Decision != "NO-GO" {
					t.Fatal("required update gate omitted", gate.id)
				}
				checks[gate.id] = original
			}
			options.Fast = true
			if result, err := plan.Qualify(context.Background(), options, checks); err == nil || result.Decision != "NO-GO" {
				t.Fatal("fast mode authorized update")
			}
		})
	}
}

// Turn synchronous cancellation into deadline expiration for these tests;
// waiting for a real deadline would make callback invocation race a timer.
type qualificationDeadlineContext struct{ context.Context }

func (ctx qualificationDeadlineContext) Err() error {
	if ctx.Context.Err() != nil {
		return context.DeadlineExceeded
	}
	return nil
}

func TestQualificationInterruptionDuringCheckTakesPrecedenceOverEveryResult(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, interruption := range []string{"deadline", "cancellation"} {
			for _, outcome := range []string{"private error", "typed blocker", "success evidence"} {
				t.Run(environment+"/"+interruption+"/"+outcome, func(t *testing.T) {
					in, definition, live := fixture(t, environment, "Deployment")
					plan, err := Compile(in, []Definition{definition}, []Object{live})
					if err != nil {
						t.Fatal(err)
					}
					baseContext, cancel := context.WithCancel(context.Background())
					defer cancel()
					var ctx context.Context = baseContext
					if interruption == "deadline" {
						ctx = qualificationDeadlineContext{baseContext}
					}
					production := environment == "prod"
					ids := RequiredGateIDs(production)
					checks := map[string]GateCheck{}
					var invoked []string
					for _, id := range ids {
						checks[id] = func(context.Context, *Plan) (GateEvidence, error) {
							invoked = append(invoked, id)
							evidence := GateEvidence{Digest("verified before interruption")}
							if id != "deployment.release" {
								return evidence, nil
							}
							cancel()
							switch outcome {
							case "private error":
								return evidence, errors.New("credential=never-display-this")
							case "typed blocker":
								return evidence, &Blocker{Cause: "credential=never-display-this", Next: "private-repair-instruction"}
							default:
								return evidence, nil
							}
						}
					}
					result, err := plan.Qualify(ctx, QualificationOptions{Production: production}, checks)
					if err == nil || result.Decision != "NO-GO" || result.PlanSHA256 != plan.SHA256() || len(result.Gates) != len(ids) {
						t.Fatal("interrupted qualification was approved or lost its scope", err, result)
					}
					if strings.Join(invoked, ",") != "deployment.target,deployment.release" {
						t.Fatal("interrupted qualification invoked a later gate", invoked)
					}
					for i, gate := range result.Gates {
						if gate.ID != ids[i] || !gate.Required {
							t.Fatal("interruption changed required gates or their order", result.Gates)
						}
						if i == 0 {
							if gate.Status != "PASS" || gate.EvidenceSHA256 != Digest("verified before interruption") || gate.Cause != "" || gate.Next != "" {
								t.Fatal("interruption discarded a completed gate", gate)
							}
							continue
						}
						cause := "qualification was canceled before this required gate completed"
						next := "repeat full preflight for the same release when qualification can run to completion"
						if interruption == "deadline" {
							cause = "qualification deadline exceeded before this required gate completed"
							next = "repeat full preflight for the same release with a timeout long enough to complete all required checks"
						}
						if gate.Status != "ERROR" || gate.Cause != cause || gate.Next != next || gate.EvidenceSHA256 != "" || gate.Impact == "" || gate.Recheck == "" {
							t.Fatal("interruption was reported as a gate failure or accepted evidence", gate)
						}
					}
					raw, marshalErr := json.Marshal(result)
					if marshalErr != nil {
						t.Fatal(marshalErr)
					}
					if strings.Contains(string(raw)+err.Error(), "credential=") || strings.Contains(string(raw), "private-repair-instruction") {
						t.Fatal("interruption report leaked callback details")
					}
				})
			}
		}
	}
}

func TestQualificationCanceledBeforeStartingDoesNotInvokeChecks(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, deadline := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/deadline=%t", environment, deadline), func(t *testing.T) {
				in, definition, live := fixture(t, environment, "Deployment")
				plan, err := Compile(in, []Definition{definition}, []Object{live})
				if err != nil {
					t.Fatal(err)
				}
				baseContext, cancel := context.WithCancel(context.Background())
				cancel()
				var ctx context.Context = baseContext
				if deadline {
					ctx = qualificationDeadlineContext{baseContext}
				}
				production := environment == "prod"
				ids := RequiredGateIDs(production)
				checks := map[string]GateCheck{}
				for _, id := range ids {
					checks[id] = func(context.Context, *Plan) (GateEvidence, error) {
						t.Fatal("an already interrupted qualification invoked a callback")
						return GateEvidence{}, nil
					}
				}
				result, err := plan.Qualify(ctx, QualificationOptions{Production: production}, checks)
				if err == nil || result.Decision != "NO-GO" || len(result.Gates) != len(ids) {
					t.Fatal("already interrupted qualification was approved or lost gates", err, result)
				}
				for i, gate := range result.Gates {
					if gate.ID != ids[i] || gate.Status != "ERROR" || gate.EvidenceSHA256 != "" || !gate.Required {
						t.Fatal("already interrupted qualification changed gate order or status", gate)
					}
				}
			})
		}
	}
}

func TestQualificationRecognizesWrappedInterruptionErrorsWithoutExpiredContext(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, interruption := range []error{context.DeadlineExceeded, context.Canceled} {
			t.Run(environment+"/"+interruption.Error(), func(t *testing.T) {
				in, definition, live := fixture(t, environment, "Deployment")
				plan, err := Compile(in, []Definition{definition}, []Object{live})
				if err != nil {
					t.Fatal(err)
				}
				production := environment == "prod"
				ids := RequiredGateIDs(production)
				checks := map[string]GateCheck{}
				for _, id := range ids {
					checks[id] = func(context.Context, *Plan) (GateEvidence, error) {
						evidence := GateEvidence{Digest("verified")}
						if id == "deployment.release" {
							return evidence, errors.Join(fmt.Errorf("credential=never-display-this: %w", interruption), &Blocker{Cause: "private-blocker", Next: "private-repair"})
						}
						return evidence, nil
					}
				}
				result, err := plan.Qualify(context.Background(), QualificationOptions{Production: production}, checks)
				if err == nil || result.Decision != "NO-GO" || len(result.Gates) != len(ids) {
					t.Fatal("wrapped interruption was approved or lost gates", err, result)
				}
				for i, gate := range result.Gates {
					if gate.ID != ids[i] {
						t.Fatal("wrapped interruption changed gate order", result.Gates)
					}
					if gate.ID == "deployment.release" {
						if gate.Status != "ERROR" || gate.EvidenceSHA256 != "" || !strings.HasPrefix(gate.Next, "repeat full preflight") {
							t.Fatal("wrapped interruption used blocker repair advice or accepted evidence", gate)
						}
					} else if gate.Status != "PASS" {
						t.Fatal("a callback error interrupted an otherwise live context", gate)
					}
				}
				raw, marshalErr := json.Marshal(result)
				if marshalErr != nil {
					t.Fatal(marshalErr)
				}
				if strings.Contains(string(raw)+err.Error(), "credential=") || strings.Contains(string(raw), "private-") {
					t.Fatal("wrapped interruption leaked callback details")
				}
			})
		}
	}
}
