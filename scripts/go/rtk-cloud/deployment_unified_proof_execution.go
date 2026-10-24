package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentExecutionAdmissionKey struct{}

// Admission is private to one running attempt. The reviewed evidence is frozen
// here; its age may increase, but its identity and the caller's cancellation
// and deadline remain binding throughout execution.
type environmentExecutionAdmission struct {
	deployment          *compiledEnvironmentDeployment
	proof               *environmentPreflightProof
	plan                *deploymentplan.Plan
	proofSHA256         string
	planSHA256          string
	generation          string
	preflightGeneration string
	admittedAt          time.Time
	deadline            time.Time
	parent              context.Context
	active              atomic.Bool
	claimed             atomic.Bool
}

func admitQualifiedEnvironmentExecution(ctx context.Context, c *compiledEnvironmentDeployment, now time.Time) (context.Context, *environmentExecutionAdmission, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	if c == nil || c.Plan == nil || c.Proof == nil || c.Proof.Qualification.Decision != "GO" || c.Proof.Qualification.PlanSHA256 != c.Plan.SHA256() {
		return nil, nil, preflightExecutionProofBlocker("complete qualified evidence is missing or differs from the selected plan")
	}
	if err := environmentPreflightProofTimingError(*c.Proof, now); err != nil {
		return nil, nil, err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return nil, nil, preflightExecutionProofBlocker("execution requires the selected --timeout or an earlier caller deadline")
	}
	if !now.Before(deadline) {
		return nil, nil, context.DeadlineExceeded
	}
	a := &environmentExecutionAdmission{deployment: c, proof: c.Proof, plan: c.Plan, proofSHA256: c.Proof.SHA256(), planSHA256: c.Plan.SHA256(), generation: c.Proof.Generation, preflightGeneration: c.PreflightGeneration, admittedAt: now, deadline: deadline, parent: ctx}
	a.active.Store(true)
	return context.WithValue(ctx, environmentExecutionAdmissionKey{}, a), a, nil
}

func admittedEnvironmentExecution(ctx context.Context, c *compiledEnvironmentDeployment) (*environmentExecutionAdmission, error) {
	a, _ := ctx.Value(environmentExecutionAdmissionKey{}).(*environmentExecutionAdmission)
	if a == nil || !a.active.Load() || c == nil || c != a.deployment || c.Proof == nil || c.Proof != a.proof || c.Plan == nil || c.Plan != a.plan || c.Proof.SHA256() != a.proofSHA256 || c.Plan.SHA256() != a.planSHA256 || c.Proof.Generation != a.generation || c.PreflightGeneration != a.preflightGeneration {
		return nil, preflightExecutionProofBlocker("the running attempt no longer has its original admitted qualification and plan")
	}
	if err := a.parent.Err(); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !time.Now().Before(a.deadline) {
		return nil, context.DeadlineExceeded
	}
	deadline, ok := ctx.Deadline()
	if !ok || deadline.After(a.deadline) {
		return nil, preflightExecutionProofBlocker("the admitted execution deadline was removed or extended")
	}
	return a, nil
}

func validateEnvironmentExecutionProof(ctx context.Context, c *compiledEnvironmentDeployment, cfg deploymentConfig, plan *deploymentplan.Plan, now time.Time) error {
	if ctx.Value(environmentExecutionAdmissionKey{}) == nil {
		return validateEnvironmentPreflightProof(*c.Proof, cfg, plan, now)
	}
	admission, err := admittedEnvironmentExecution(ctx, c)
	if err != nil {
		return err
	}
	return validateEnvironmentPreflightProof(*c.Proof, cfg, plan, admission.admittedAt)
}

func preflightExecutionProofBlocker(cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.preflight.proof", Cause: cause, Next: "run complete preflight for the selected environment and review its exact plan SHA-256; deployment does not repeat or fabricate qualification"}
}

func preflightExecutionProofRow(cause string) deploymentplan.GateResult {
	return deploymentplan.GateResult{ID: "deployment.preflight.proof", Status: "BLOCKED", Required: true, Cause: cause, Next: "run complete preflight and review the newly qualified plan SHA-256", Impact: "deployment has no current consumable qualification", Recheck: "verify the qualified artifact, exact inputs and evidence expiry"}
}

func setUnifiedPreflightEvidenceReport(report *unifiedDeploymentReport, proof *environmentPreflightProof, reused bool) {
	qualified, until := proof.QualifiedAt, proof.ValidUntil
	report.QualificationSHA256 = proof.SHA256()
	report.QualifiedAt, report.EvidenceValidUntil = &qualified, &until
	report.ReusedPreflight = reused
}

func safeQualifiedExecutionFailure(err error) deploymentplan.GateResult {
	row := deploymentplan.GateResult{ID: "deployment.execution", Status: "FAIL", Required: true, Cause: "execution stopped; private execution evidence is retained when created", Next: "inspect actual state and private evidence before repeating preflight", Impact: "deployment has not completed", Recheck: "verify actual target state, PKI and required public endpoints"}
	var blocker *deploymentplan.Blocker
	if errors.As(err, &blocker) {
		row.ID, row.Cause, row.Next = blocker.ID, blocker.Cause, blocker.Next
		if blocker.Resource != "" {
			row.Cause += " (" + blocker.Resource + ")"
		}
	} else if errors.Is(err, context.DeadlineExceeded) {
		row.Cause = "deployment execution reached its --timeout or earlier caller deadline"
	} else if errors.Is(err, context.Canceled) {
		row.Cause = "deployment execution was cancelled"
	}
	return row
}

// The saved qualification supplies all preflight gates. Only current mutable
// execution prerequisites run here; neither compilation nor Qualify is repeated.
func executeQualifiedEnvironmentDeployment(ctx context.Context, c *compiledEnvironmentDeployment, o unifiedDeploymentOptions, out, diagnostics io.Writer, deps unifiedDeploymentDependencies, report *unifiedDeploymentReport) error {
	fail := func(err error) error {
		report.Decision = "NO-GO"
		report.Gates = append(report.Gates, safeQualifiedExecutionFailure(err))
		return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, exitCode(1))
	}
	if c == nil || c.Plan == nil || c.Proof == nil || c.Proof.Qualification.Decision != "GO" || c.Proof.Qualification.PlanSHA256 != c.Plan.SHA256() {
		return fail(preflightExecutionProofBlocker("complete qualified evidence is missing or differs from the selected plan"))
	}
	if err := environmentPreflightProofTimingError(*c.Proof, time.Now().UTC()); err != nil {
		return fail(err)
	}
	setUnifiedPreflightEvidenceReport(report, c.Proof, true)
	expectedProof := c.Proof.SHA256()
	report.Gates = append([]deploymentplan.GateResult(nil), c.Proof.Qualification.Gates...)
	for i := range report.Gates {
		report.Gates[i].EvidenceSource = "preflight"
	}
	report.Decision = "GO"
	if o.PlanSHA256 != c.Plan.SHA256() {
		return fail(&deploymentplan.Blocker{ID: "deployment.plan", Cause: "reviewed SHA-256 differs from the qualified saved plan", Next: "review complete preflight and use its exact --plan-sha256"})
	}
	if deps.Accept == nil || deps.Execute == nil || deps.LiveChecks == nil {
		return fail(&deploymentplan.Blocker{ID: "deployment.execution", Cause: "immediate execution checks or complete post-deploy acceptance are unavailable", Next: "restore the execution and acceptance adapters before using qualified evidence"})
	}
	checks := deps.LiveChecks(c)
	livePassed, acceptancePassed, liveFailed := false, false, false
	recheck := func(checkCtx context.Context, plan *deploymentplan.Plan) error {
		if livePassed {
			return preflightExecutionProofBlocker("immediate execution checks must run once per attempt")
		}
		if _, err := admittedEnvironmentExecution(checkCtx, c); err != nil {
			return err
		}
		if plan != c.Plan {
			return preflightExecutionProofBlocker("immediate execution checks received a different plan")
		}
		for _, id := range []string{"deployment.target", "deployment.execution", "deployment.pki", "deployment.compatibility", "deployment.network"} {
			stage := "deployment.execution.recheck." + strings.TrimPrefix(id, "deployment.")
			row := deploymentplan.GateResult{ID: stage, Status: "PASS", Required: true, EvidenceSource: "execution"}
			var evidence deploymentplan.GateEvidence
			environmentDeploymentProgress(checkCtx, stage)
			err := func(progressCtx context.Context) error {
				if checks[id] == nil {
					return &deploymentplan.Blocker{ID: stage, Cause: "required immediate execution check is unavailable", Next: "restore this current-state checker before execution"}
				}
				var checkErr error
				evidence, checkErr = checks[id](progressCtx, plan)
				if checkErr != nil {
					return checkErr
				}
				if _, err := admittedEnvironmentExecution(progressCtx, c); err != nil {
					return err
				}
				if plan.SHA256() != o.PlanSHA256 || c.Proof.SHA256() != expectedProof || len(evidence.SHA256) != 64 || strings.Trim(evidence.SHA256, "0123456789abcdef") != "" {
					return preflightExecutionProofBlocker("execution inputs, deadline or current check evidence became invalid")
				}
				return nil
			}(checkCtx)
			status := "PASS"
			if err != nil {
				status = "FAIL"
			}
			environmentDeploymentProgressResult(checkCtx, stage, status)
			if err != nil {
				row = safeQualifiedExecutionFailure(err)
				row.CheckID, row.ID, row.EvidenceSource = row.ID, stage, "execution"
				if row.CheckID == "deployment.execution" && !errors.Is(err, context.Canceled) && !errors.Is(err, context.DeadlineExceeded) {
					row.Cause = "required immediate execution evidence could not be verified"
				}
				if row.CheckID == stage {
					row.CheckID = ""
				}
				report.Gates = append(report.Gates, row)
				liveFailed = true
				return err
			}
			row.EvidenceSHA256, row.PendingRolloutHealth = evidence.SHA256, evidence.PendingRolloutHealth
			report.Gates = append(report.Gates, row)
		}
		livePassed = true
		return nil
	}
	accept := func(acceptCtx context.Context, accepted *compiledEnvironmentDeployment) error {
		if _, err := admittedEnvironmentExecution(acceptCtx, accepted); err != nil {
			return err
		}
		err := deps.Accept(acceptCtx, accepted)
		if err == nil {
			_, err = admittedEnvironmentExecution(acceptCtx, accepted)
		}
		acceptancePassed = err == nil
		return err
	}
	// Recheck freshness at the admission boundary, after loading and setup.
	if c.Proof == nil || c.Proof.SHA256() != expectedProof || c.Plan.SHA256() != o.PlanSHA256 {
		return fail(preflightExecutionProofBlocker("the selected proof or plan changed before execution admission"))
	}
	admittedCtx, admission, err := admitQualifiedEnvironmentExecution(ctx, c, time.Now().UTC())
	if err != nil {
		return fail(err)
	}
	defer admission.active.Store(false)
	ctx = admittedCtx
	admittedAt, deadline := admission.admittedAt.UTC(), admission.deadline.UTC()
	report.ExecutionAdmittedAt, report.ExecutionDeadline = &admittedAt, &deadline
	err = runEnvironmentProgress(ctx, out, "deployment.execution", func(progressCtx context.Context) error {
		return deps.Execute(progressCtx, c, o.PlanSHA256, recheck, accept)
	})
	if err != nil {
		if liveFailed {
			report.Decision = "NO-GO"
			return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, exitCode(1))
		}
		return fail(err)
	}
	if _, err := admittedEnvironmentExecution(ctx, c); err != nil {
		return fail(err)
	}
	if !livePassed || !acceptancePassed || c.Plan.SHA256() != o.PlanSHA256 || c.Proof.SHA256() != expectedProof {
		return fail(preflightExecutionProofBlocker("current checks, post-deploy acceptance or valid execution completion are missing"))
	}
	report.Completed = true
	for i := range report.Gates {
		report.Gates[i].PendingRolloutHealth = nil
	}
	return emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, nil)
}
