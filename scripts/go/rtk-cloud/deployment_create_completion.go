package main

import (
	"context"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The durable completion marker is written after final acceptance and capture.
// Reopening only the archive step neither requalifies nor executes a workload.
func resumeCreateCompletedOwnerArchive(ctx context.Context, e *environmentCreateExecution, o unifiedDeploymentOptions) error {
	if e == nil || e.Check(ctx) != nil || e.Journal.CompletedAt.IsZero() || e.Journal.CompletedAt.Before(e.Journal.AdmittedAt) || e.Journal.CompletedAt.After(time.Now().UTC()) || len(e.Journal.Entries) == 0 {
		return environmentCreateStateBlocker("deployment.create.execution", "original completed execution cannot be verified for owner archival")
	}
	for _, entry := range e.Journal.Entries {
		if entry.State != "completed" || len(entry.Receipt) == 0 || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(e.Journal.CompletedAt) {
			return environmentCreateStateBlocker("deployment.create.execution", "incomplete original outcomes cannot release the create owner")
		}
	}
	if o.ParentPlanSHA256 != "" && o.ParentPlanSHA256 != e.Journal.PlanSHA256 {
		return environmentCreateStateBlocker("deployment.create.execution", "completed execution belongs to another selected parent")
	}
	if sha := o.PlanSHA256; sha != "" && sha != e.Journal.PlanSHA256 {
		matched := false
		if p, err := readCreateNormalTailProgram(e.Store, e); err == nil && deploymentplan.Digest(p) == sha && verifyCreateNormalTailProgramAdmission(e.Store, e) == nil {
			matched = true
		}
		if p, err := readCreateNormalTailFrozenTarget(e.Store, e); err == nil && p.SHA256() == sha {
			matched = true
		}
		if !matched {
			return environmentCreateStateBlocker("deployment.create.execution", "selected target differs from the completed original execution")
		}
	}
	switch o.Action {
	case "plan", "preflight":
		return e.Check(ctx) // no owner publication during read-only commands
	case "create":
		return e.CompleteAndReleaseContext(ctx, time.Now().UTC())
	default:
		return environmentCreateStateBlocker("deployment.create.execution", "completed original owner requires a selected create command")
	}
}
