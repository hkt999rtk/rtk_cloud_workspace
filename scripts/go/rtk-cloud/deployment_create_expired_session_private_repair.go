package main

import (
	"context"
	"io"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A previously admitted same-owner private-state successor can repair the
// running application even when an unrelated bootstrap session has expired.
// This is deliberately a scoped repair, never a complete deployment GO.
func tryExpiredSessionPrivateRepair(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, parent *environmentCreatePlan, prior environmentCreateJournalEntry, partial issuerEnrollmentContinuationProof, partialReview string, hasAdmission bool, forward issuerEnrollmentContinuationProof, forwardReview string, hasForward bool, o unifiedDeploymentOptions, report *unifiedDeploymentReport, out, diagnostics io.Writer) (bool, error) {
	if !hasAdmission || partial.PartialSession == nil || parent == nil {
		return false, nil
	}
	var q0Review, q1Review string
	for _, entry := range e.Journal.Entries {
		if entry.Phase == "failed-stage-forward" && strings.HasPrefix(entry.Key, "failed-stage-forward-") {
			if q0Review != "" {
				return true, createProviderRecoveryBlock("original application repair admission is ambiguous")
			}
			q0Review = strings.TrimPrefix(entry.Key, "failed-stage-forward-")
		}
		if entry.Phase == "initializer-forward" && strings.HasPrefix(entry.Key, "initializer-forward-") {
			if q1Review != "" {
				return true, createProviderRecoveryBlock("original initializer repair admission is ambiguous")
			}
			q1Review = strings.TrimPrefix(entry.Key, "initializer-forward-")
		}
		if entry.Phase == "initializer-mount-forward" && entry.State == "completed" {
			return false, nil
		}
	}
	if q0Review == "" || q1Review == "" {
		return false, nil
	}
	q0, _, _, err := loadFailedForwardBundle(store, e, q0Review)
	if err != nil {
		return true, err
	}
	q1, _, _, err := loadInitializerForwardBundle(store, e, q1Review)
	if err != nil || q1.FailedForwardSHA256 != q0Review {
		return true, createProviderRecoveryBlock("original private-state repair admission is unavailable")
	}
	selected, selection := partial, partialReview
	if hasForward && forwardReview == q0.OriginalAdmissionSHA256 {
		selected, selection = forward, forwardReview
	}
	if selection != q0.OriginalAdmissionSHA256 || deploymentplan.Digest(selected) != selection {
		return true, createProviderRecoveryBlock("original scoped repair admission changed")
	}
	previous, err := readEnvironmentCreateStageInput(store, e, q1.UpdateStage)
	if err != nil || previous == nil {
		return false, nil
	}
	j, exists, err := previous.ReadExecution(store.Root)
	if err != nil {
		return true, err
	}
	if !exists || j.Failed == "" || j.Accepted {
		return false, nil
	}
	session, _, err := readManagedSessionState(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b}, partial.PartialSession.RenewedIntent.SessionID)
	if err != nil {
		return true, err
	}
	if !issuerEnrollmentSessionExpired(partial.PartialSession.RenewedIntent, session, time.Now().UTC()) {
		return false, nil
	}
	if deploymentplan.Digest(cfg) != parent.ConfigurationSHA256 {
		return true, createProviderRecoveryBlock("original scoped repair configuration changed")
	}
	frozen, found, err := readIssuerResumeDeadline(store, e, selection, prior)
	if err != nil || !found {
		return true, createProviderRecoveryBlock("original scoped repair execution deadline is unavailable")
	}
	deadline, found, err := effectiveIssuerDeadline(ctx, store, e, selection, prior)
	if err != nil {
		return true, err
	}
	if !found || !time.Now().Before(deadline) {
		return true, createProviderRecoveryBlock("original scoped repair execution deadline is unavailable or expired")
	}
	if o.Action == "create" && (o.Confirm != parent.Stack || deadline.Equal(frozen) && !prior.StartedAt.Add(o.Timeout).Equal(deadline)) {
		return true, createProviderRecoveryBlock("scoped repair requires the original target and admitted execution timeout")
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, Backend: b}
	q, err := dispatchFailedApplicationForward(ctx, cfg, store, e, b, parent, runtime, selected, selection, o.Action, o.PlanSHA256, out, time.Now().UTC(), deadline)
	if err != nil {
		return true, err
	}
	if q == nil || q.initializer == nil || q.initializer.mount == nil {
		return true, createProviderRecoveryBlock("original scoped private-state repair is unavailable")
	}
	mount := q.initializer.mount
	review := deploymentplan.Digest(*mount)
	report.Scope = "original-generation-private-state-repair"
	report.Plan = &deploymentplan.Summary{SchemaVersion: 1, Environment: cfg.Environment, Operation: "create", PlanSHA256: review, Resources: []deploymentplan.SummaryResource{{ID: "Stage/private-state-repair", Phase: "workload", Action: "update"}}}
	report.QualificationSHA256 = review
	report.QualifiedAt = &mount.QualifiedAt
	report.EvidenceValidUntil = &mount.ExecutionDeadline
	report.ExecutionDeadline = &mount.ExecutionDeadline
	report.Completed = false
	if o.Action == "preflight" {
		report.Decision = "GO"
	} else {
		report.Decision = "PASS"
		report.ReusedPreflight = true
	}
	report.Gates = append(report.Gates, deploymentplan.GateResult{ID: "deployment.recovery.private-state", Status: "PASS", Required: true, EvidenceSHA256: review})
	_, _ = io.WriteString(out, "Scoped private-state repair only; the original deployment and full health acceptance remain incomplete.\n")
	return true, emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, nil)
}
