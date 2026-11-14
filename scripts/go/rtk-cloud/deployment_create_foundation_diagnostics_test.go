package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestFoundationStepDiagnosticRedactsUnknownErrors(t *testing.T) {
	original := errors.New("private-original-error")
	if !errors.Is(environmentCreateFoundationStepError("foundation-apply", original), original) {
		t.Fatal("diagnostic must preserve the original error identity")
	}
	for _, step := range []string{"management-transport", "openbao-transport", "account-signer", "runtime-snapshot", "storage-snapshot", "foundation-render", "pull-secret-render", "openbao-render", "storage-class-binding", "foundation-apply"} {
		t.Run(step, func(t *testing.T) {
			err := environmentCreateFoundationStepError(step, errors.New("secret-token-and-private-path"))
			var blocker *deploymentplan.Blocker
			if !errors.As(err, &blocker) || blocker.ID != "deployment.bootstrap."+step || strings.Contains(blocker.Cause, "secret-token-and-private-path") || strings.Contains(err.Error(), "secret-token-and-private-path") {
				t.Fatalf("foundation step was not safely identified: %v", err)
			}
		})
	}
	known := environmentCreateBlocker("deployment.example", "known safe blocker")
	if environmentCreateFoundationStepError("foundation-render", known) != known || !errors.Is(environmentCreateFoundationStepError("foundation-render", context.DeadlineExceeded), context.DeadlineExceeded) || environmentCreateFoundationStepError("foundation-render", nil) != nil {
		t.Fatal("known blockers, cancellation or nil changed")
	}
	err := environmentCreateFoundationStepError("secret-token-and-private-path", errors.New("secret-token-and-private-path"))
	if strings.Contains(err.Error(), "secret-token-and-private-path") {
		t.Fatal("unknown step exposed private input")
	}
}

func TestHistoricalCreatePlanRetainsExactWireBytes(t *testing.T) {
	_, _, _, artifact := createStateFixture(t, "dev")
	var plan environmentCreatePlan
	if err := json.Unmarshal(artifact.Plan, &plan); err != nil || plan.OpenBaoFirst {
		t.Fatal("historical plan unexpectedly selected the new order", err)
	}
	raw, err := json.Marshal(plan)
	before, beforeErr := canonicalEnvironmentCreateJSON(artifact.Plan)
	after, afterErr := canonicalEnvironmentCreateJSON(raw)
	if err != nil || beforeErr != nil || afterErr != nil || !bytes.Equal(before, after) {
		t.Fatal("historical canonical plan bytes changed", err, beforeErr, afterErr)
	}
	sha, err := environmentCreatePlanSHA256(raw)
	if err != nil || sha != artifact.PlanSHA256 {
		t.Fatal("historical plan digest changed", err)
	}
}
