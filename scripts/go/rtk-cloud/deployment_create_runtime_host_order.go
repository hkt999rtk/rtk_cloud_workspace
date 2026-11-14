package main

import (
	"context"
	"io"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const createRuntimeHostStage = "workload-runtime-hosts"

// The reviewed package repair must precede session consumer readiness. Its
// exact two updates come from the existing immutable workload qualification.
func createRuntimeHostPlan(q *createWorkloadImageForward) (*deploymentplan.Plan, error) {
	if q == nil || q.Plan == nil {
		return nil, createProviderRecoveryBlock("reviewed runtime host plan unavailable")
	}
	p := *q.Plan
	p.Resources = nil
	for _, r := range q.Plan.Resources {
		if r.Action != "update" {
			continue
		}
		if r.Phase != "workload" || r.UID == "" || r.Baseline == nil {
			return nil, createProviderRecoveryBlock("runtime host update differs from known original owner")
		}
		r.Dependencies = nil
		p.Resources = append(p.Resources, r)
	}
	if len(p.Resources) != len(q.Images) || len(p.Resources) != 2 {
		return nil, createProviderRecoveryBlock("runtime host scope differs from the two selected packages")
	}
	p.BindingsSHA256 = deploymentplan.Digest(struct{ Stage, Review string }{createRuntimeHostStage, deploymentplan.Digest(q)})
	return &p, nil
}

func tryUnifiedRuntimeHostCompatibility(ctx context.Context, cfg deploymentConfig, o unifiedDeploymentOptions, report *unifiedDeploymentReport, store secretStore, e *environmentCreateExecution, native environmentKubernetesBackend, parent *environmentCreatePlan, out, diagnostics io.Writer) (bool, error) {
	q, err := readOriginalCreateWorkloadImageForward(store, e)
	if err != nil {
		return true, err
	}
	if q == nil {
		return false, nil
	}
	if e.versionState == nil || e.versionState.cache == nil || deploymentplan.Digest(e.versionState.cache.RuntimeImages) != deploymentplan.Digest(q.Images) {
		if e.versionState != nil && e.versionState.cache != nil && completedFreshRuntimeImageSource(ctx, store, e) == nil {
			// The original host compatibility write is completed history. The
			// current role selection must reach full recovery qualification and
			// its own ordinary image child, never repeat session preparation.
			return false, nil
		}
		return true, createProviderRecoveryBlock("runtime hosts differ from the explicitly selected environment packages")
	}
	p, err := createRuntimeHostPlan(q)
	if err != nil {
		return true, err
	}
	key := "kubernetes/" + createRuntimeHostStage
	if entry := createOpenBaoACKCompletionEntry(e.Journal, key); entry != nil && entry.State == "completed" {
		if _, err := verifyAcceptedStageAfterRecordedHistory(ctx, store, e, native, key); err != nil {
			return true, err
		}
		return tryUnifiedCustodyTokenAccess(ctx, cfg, o, report, store, e, native, parent, out, diagnostics)
	}
	deadline, err := currentInitializerEffectiveDeadline(ctx, store, e, time.Now().UTC())
	if err != nil || !time.Now().Before(deadline) {
		return true, createProviderRecoveryBlock("original runtime host execution deadline unavailable")
	}
	if _, err := verifyOpenBaoInPlaceReady(ctx, cfg, store, e, native); err != nil {
		return true, err
	}
	if err := p.AdmitResumable(ctx, native, store.Root); err != nil {
		return true, err
	}
	if o.Action == "create" {
		if o.Confirm != parent.Stack || o.PlanSHA256 != p.SHA256() {
			return true, createProviderRecoveryBlock("run complete preflight and execute the exact reviewed runtime host compatibility plan before session recovery")
		}
		bounded, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		if err := executeCompiledEnvironmentCreateStage(bounded, cfg, store, parent, e, createRuntimeHostStage, p, native, out); err != nil {
			return true, err
		}
	} else if o.Action != "preflight" {
		return true, createProviderRecoveryBlock("runtime host compatibility requires the original create flow")
	}
	report.Scope = "original-generation-runtime-host-templates"
	report.Decision = "GO"
	if o.Action == "create" {
		report.Decision, report.ReusedPreflight = "PASS", true
	}
	report.Plan = &deploymentplan.Summary{SchemaVersion: 1, Environment: cfg.Environment, Operation: "create", PlanSHA256: p.SHA256()}
	for _, r := range p.Resources {
		report.Plan.Resources = append(report.Plan.Resources, deploymentplan.SummaryResource{ID: r.ID, Phase: r.Phase, Action: r.Action})
	}
	now := time.Now().UTC()
	report.QualifiedAt, report.EvidenceValidUntil, report.ExecutionDeadline = &now, &deadline, &deadline
	report.QualificationSHA256, report.Completed = p.SHA256(), false
	report.Gates = append(report.Gates, deploymentplan.GateResult{ID: "deployment.recovery.runtime-hosts", Status: "PASS", Required: true, EvidenceSHA256: p.SHA256()})
	_, _ = io.WriteString(out, "Runtime templates only; actual consumer readiness and ACKs remain in the following session and full deployment stages.\n")
	return true, emitUnifiedDeploymentReport(out, diagnostics, o.Report, *report, nil)
}

// Keep the reviewed original artifact immutable. Later same-parent host/session
// writes advance only known original owners along their native baseline chain.
// Stop before this forward's own first request, so retries and final receipts
// always resolve the same plan even after its routing tail has been written.
func readCreateWorkloadImageForward(store secretStore, e *environmentCreateExecution) (*createWorkloadImageForward, error) {
	q, err := readOriginalCreateWorkloadImageForward(store, e)
	if err != nil || q == nil {
		return q, err
	}
	original, err := readEnvironmentCreateStageInput(store, e, "workload")
	if err != nil {
		return nil, err
	}
	j, ok, err := original.ReadExecution(store.Root)
	if err != nil || !ok {
		return nil, createProviderRecoveryBlock("original workload known prefix unavailable")
	}
	var base []activeTransportRenewalResource
	for i, r := range original.Resources[:len(j.Completed)] {
		if j.Completed[i] != r.ID {
			return nil, createProviderRecoveryBlock("original workload known prefix changed")
		}
		base = append(base, sessionRecordedOwnerBase(r.ID, j.Owners[r.ID], r.Desired))
	}
	var stages []completedRecordedSuccessorStage
	after := false
	for _, entry := range e.Journal.Entries {
		if entry.Key == "kubernetes/workload" {
			after = true
			continue
		}
		if !after {
			continue
		}
		if entry.Key == "kubernetes/"+createWorkloadImageForwardStage {
			break
		}
		if !strings.HasPrefix(entry.Key, "kubernetes/") {
			continue
		}
		stage := strings.TrimPrefix(entry.Key, "kubernetes/")
		p, err := readEnvironmentCreateStageInput(store, e, stage)
		if err != nil || p == nil {
			return nil, createProviderRecoveryBlock("later workload owner input unavailable")
		}
		progress, exists, err := readEnvironmentCreateStageProgress(store, e, stage, p)
		if err != nil {
			return nil, err
		}
		if exists && len(progress.Owners) > 0 {
			stages = append(stages, completedRecordedSuccessorStage{Plan: p, Journal: progress, Written: !progress.Accepted})
		}
	}
	if len(stages) == 0 {
		return q, nil
	}
	latest, err := foldCompletedRecordedSuccessors(base, stages)
	if err != nil {
		return nil, err
	}
	p := *q.Plan
	p.Resources = append([]deploymentplan.Resource(nil), q.Plan.Resources...)
	for i, owner := range latest {
		r := p.Resources[i]
		r.Desired = deploymentplan.Clone(owner.Desired)
		r.Baseline = recordedWorkloadBaseline(r.Baseline, original.Resources[i].Desired, owner.Desired)
		deploymentplan.Map(r.Baseline["metadata"])["uid"] = owner.UID
		r.UID, r.Action = owner.UID, "retain"
		for _, raw := range certIssuerObjectList(environmentPKIPod(r.Desired)["containers"]) {
			c := deploymentplan.Map(raw)
			role := workloadImageRole(p.Stack, r.ID, deploymentplan.String(c["name"]))
			if image, exists := q.Images[role]; exists && c["image"] != image.Reference {
				c["image"] = image.Reference
				r.Action = "update"
			}
		}
		p.Resources[i] = r
	}
	p.BindingsSHA256 = environmentCreateStageBindingsSHA(createWorkloadImageForwardStage, environmentCreateDefinitions(&p))
	next := *q
	next.Plan = &p
	return &next, nil
}
