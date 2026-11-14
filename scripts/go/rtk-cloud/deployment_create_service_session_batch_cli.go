package main

import (
	"context"
	"io"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// dispatchServiceSessionBatches is the scoped continuation of the original
// create. It requires a real OpenBao host readiness proof and handoff; success
// means this session continuation passed, not that deployment is complete.
func dispatchServiceSessionBatches(ctx context.Context, cfg deploymentConfig, o unifiedDeploymentOptions, report *unifiedDeploymentReport, store secretStore, e *environmentCreateExecution, parent *environmentCreatePlan, native environmentKubernetesBackend, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, openBaoRoot environmentCreateRegisteredIssuer, deadline time.Time, readyOpenBao func(context.Context) error, newGeneration bool, out, diagnostics io.Writer) error {
	if e == nil || parent == nil || report == nil || out == nil || diagnostics == nil || readyOpenBao == nil || o.Operation != "create" || (o.Action != "preflight" && o.Action != "create") || deadline.IsZero() || e.Check(ctx) != nil {
		return createProviderRecoveryBlock("Service session continuation requires the original create and native OpenBao host proof")
	}
	if err := verifyFreshProductSessionSuccessorSource(ctx, cfg, store, e, native); err != nil {
		return err
	}
	var q secondSessionAdmission
	var review string
	var err error
	if o.Action == "preflight" {
		var resumed bool
		if !newGeneration {
			q, review, resumed, err = resumeServiceSessionBatchesQualification(ctx, cfg, store, e, native, readyOpenBao, time.Now().UTC(), deadline)
		}
		if err == nil && !resumed {
			q, review, err = qualifyServiceSessionBatches(ctx, cfg, store, e, parent, native, service, openBaoRoot, readyOpenBao, time.Now().UTC(), deadline)
		}
	} else {
		if o.Confirm != parent.Stack || !environmentPreflightDigest.MatchString(o.PlanSHA256) {
			return createProviderRecoveryBlock("Service session continuation requires its exact qualified target and plan")
		}
		review = o.PlanSHA256
		q, err = loadSecondSessionAdmission(store, e, review)
		if err == nil && q.ExecutionDeadline.After(deadline) {
			err = createProviderRecoveryBlock("Service session continuation deadline differs from the original admission")
		}
		if err == nil && !q.ExecutionDeadline.Equal(deadline) {
			var effective time.Time
			effective, err = currentInitializerEffectiveDeadline(ctx, store, e, time.Now().UTC())
			if err == nil && !effective.Equal(deadline) {
				err = createProviderRecoveryBlock("Service session continuation supplement changed")
			}
		}
		if err == nil {
			var adapters serviceSessionBatchExecution
			adapters, err = nativeServiceSessionBatchExecution(ctx, cfg, store, e, parent, native, runtime, service, openBaoRoot, q, review, readyOpenBao, out)
			if err == nil {
				bounded, cancel := context.WithDeadline(ctx, deadline)
				defer cancel()
				err = executeServiceSessionBatches(bounded, store, e, review, adapters, out)
			}
		}
	}
	if err != nil {
		return err
	}
	if o.Action == "create" {
		if err := ensureFreshProductSessionSuccessor(ctx, cfg, store, e, native, q, review); err != nil {
			return err
		}
	}
	report.Scope = "original-generation-service-session-continuation"
	report.Plan = &deploymentplan.Summary{SchemaVersion: 1, Environment: cfg.Environment, Operation: "create", PlanSHA256: review, Resources: []deploymentplan.SummaryResource{{ID: "Stage/service-session-continuation", Phase: "bootstrap", Action: "update"}}}
	report.QualificationSHA256 = review
	report.QualifiedAt = &q.QualifiedAt
	report.EvidenceValidUntil = &deadline
	report.ExecutionDeadline = &deadline
	report.Completed = false
	if o.Action == "preflight" {
		report.Decision = "GO"
	} else {
		report.Decision, report.ReusedPreflight = "PASS", true
	}
	report.Gates = append(report.Gates, deploymentplan.GateResult{ID: "deployment.recovery.service-session", Status: "PASS", Required: true, EvidenceSHA256: review})
	_, _ = io.WriteString(out, "Scoped Service session continuation only; original deployment and full health acceptance remain incomplete.\n")
	return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, nil)
}
