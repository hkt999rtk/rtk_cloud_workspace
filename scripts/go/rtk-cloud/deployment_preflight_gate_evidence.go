package main

import (
	"context"
	"sort"
	"strings"
	"sync"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type preflightGateEvidenceKey struct{}
type preflightGateEvidenceCapture struct {
	mu        sync.Mutex
	responses []string
}

// Keep only response digests. Secret bodies, certificates, command arguments
// and private diagnostics never become report or artifact observation payloads.
func capturePreflightGateResponse(ctx context.Context, response []byte) {
	capture, ok := ctx.Value(preflightGateEvidenceKey{}).(*preflightGateEvidenceCapture)
	if !ok {
		return
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	capture.responses = append(capture.responses, environmentPreflightBytesSHA256(response))
}

func checkedPreflightGate(ctx context.Context, id string, plan *deploymentplan.Plan, check deploymentplan.GateCheck) (deploymentplan.GateEvidence, error) {
	capture := &preflightGateEvidenceCapture{}
	evidence, err := check(context.WithValue(ctx, preflightGateEvidenceKey{}, capture), plan)
	if err != nil {
		return evidence, err
	}
	capture.mu.Lock()
	defer capture.mu.Unlock()
	if len(capture.responses) > 0 && len(evidence.SHA256) == 64 && strings.Trim(evidence.SHA256, "0123456789abcdef") == "" {
		sort.Strings(capture.responses)
		evidence.SHA256 = deploymentplan.Digest(struct {
			Checker, Gate, Plan, Validated string
			Responses                      []string
		}{environmentPreflightCheckerVersion, id, plan.SHA256(), evidence.SHA256, capture.responses})
	}
	return evidence, nil
}
