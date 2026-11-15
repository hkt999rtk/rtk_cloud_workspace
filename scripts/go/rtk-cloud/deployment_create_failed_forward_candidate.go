package main

import (
	"context"
	"encoding/json"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// renderCreateFailedForwardJobs derives all affected native CRL initialization
// jobs from the qualified original stage and authenticated original Service
// trust. The repair image is scoped to these jobs, never to environment intent.
func renderCreateFailedForwardJobs(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, source *deploymentplan.Plan, sourceSHA string, serviceTrust deploymentplan.Object, issuers []environmentCreateRegisteredIssuer, tool environmentReleaseImage) ([]deploymentplan.Definition, error) {
	bad := func() ([]deploymentplan.Definition, error) {
		return nil, createProviderRecoveryBlock("CRL repair requires the exact original failed stage, Service trust and formal repair image")
	}
	if source == nil || source.SHA256() != sourceSHA || !environmentPreflightDigest.MatchString(sourceSHA) || source.Environment != cfg.Environment || source.Stack != p.Stack || source.Operation != "scoped" || p.Environment != cfg.Environment || !immutableEnvironmentImage.MatchString(tool.Reference) || !deploymentUpgradeSHA.MatchString(tool.SourceRevision) || tool.RetainedPlanSHA256 != "" {
		return bad()
	}
	id := "ConfigMap/" + p.Stack + "-video-cloud/pki-create-service-trust"
	if deploymentplan.ID(serviceTrust) != id {
		return bad()
	}
	var definitions []deploymentplan.Definition
	for _, resource := range source.Resources {
		if deploymentplan.ID(resource.Desired) != resource.ID {
			return bad()
		}
		if resource.Desired["kind"] == "Deployment" || resource.Desired["kind"] == "ConfigMap" {
			if resource.ID == id && deploymentplan.Digest(resource.Desired) != deploymentplan.Digest(serviceTrust) {
				return bad()
			}
			definitions = append(definitions, deploymentplan.Definition{Object: deploymentplan.Clone(resource.Desired), Phase: resource.Phase})
		}
	}
	definitions = append(definitions, deploymentplan.Definition{Object: deploymentplan.Clone(serviceTrust), Phase: "configuration"})
	p.Image = tool.Reference
	jobs, err := renderEnvironmentCreateApplicationCRLInitialization(ctx, cfg, p, runtime, service, definitions, issuers)
	if err != nil {
		return nil, err
	}
	if len(jobs) == 0 {
		return bad()
	}
	return jobs, nil
}

// prepareFailedForwardCandidates binds the original failed stage and latest
// authenticated trust to both native candidate plans without changing state.
// project uses the maintained custody projector; inventory is a read-only
// Kubernetes snapshot so the same function can be exercised with fake state.
func prepareFailedForwardCandidates(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, parent *environmentCreatePlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, issuers []environmentCreateRegisteredIssuer, admitted issuerEnrollmentContinuationProof, admissionSHA string, tool failedForwardTool, project func(context.Context, []deploymentplan.Definition) ([]deploymentplan.Definition, error), inventory func(context.Context) ([]deploymentplan.Object, error), clock func() time.Time) (createFailedForwardProof, *deploymentplan.Plan, *deploymentplan.Plan, error) {
	var empty createFailedForwardProof
	bad := func() (createFailedForwardProof, *deploymentplan.Plan, *deploymentplan.Plan, error) {
		return empty, nil, nil, createProviderRecoveryBlock("failed-stage candidate requires the original admitted parent, exact trusted issuer data and native custody renderer")
	}
	if e == nil || e.Store != store || b == nil || parent == nil || project == nil || inventory == nil || clock == nil || parent.Environment != cfg.Environment || parent.Stack != e.Journal.Stack || parent.ConfigurationSHA256 == "" || admitted.Parent != e.Journal.PlanSHA256 || admitted.ParentOwner != deploymentplan.Digest(e.owner) || deploymentplan.Digest(admitted) != admissionSHA || e.Check(ctx) != nil {
		return bad()
	}
	stage, err := qualifyCreateFailedFullyWrittenStage(ctx, store, e, "managed-application-owners", b)
	if err != nil {
		return empty, nil, nil, err
	}
	original, err := readEnvironmentCreateStageInput(store, e, stage.Stage)
	if err != nil || original == nil || original.SHA256() != stage.Child.PlanSHA256 || original.ConfigurationSHA256 != parent.ConfigurationSHA256 || original.ReleaseSHA256 != deploymentplan.Digest(parent.Release) {
		return bad()
	}
	journal, exists, err := original.ReadExecution(store.Root)
	if err != nil || !exists || deploymentplan.Digest(journal) != stage.Child.JournalSHA256 {
		return bad()
	}
	trust, err := readLatestAcceptedCreateResource(ctx, store, e, b, "ConfigMap/"+parent.Stack+"-video-cloud/pki-create-service-trust")
	if err != nil {
		return empty, nil, nil, err
	}
	jobs, err := renderCreateFailedForwardJobs(ctx, cfg, parent.Bootstrap, runtime, service, original, stage.Child.PlanSHA256, trust, issuers, tool.Image)
	if err != nil {
		return empty, nil, nil, err
	}
	jobs, err = project(ctx, jobs)
	if err != nil {
		return empty, nil, nil, err
	}
	var snapshot environmentCreateJournal
	if json.Unmarshal(mustProviderRecoveryJSON(e.Journal), &snapshot) != nil {
		return bad()
	}
	q := createFailedForwardProof{Schema: failedForwardProofSchema, Environment: cfg.Environment, ParentOwnerSHA256: deploymentplan.Digest(e.owner), OriginalAdmissionSHA256: admissionSHA, SourceJournal: snapshot, FailedStage: stage, ToolImage: tool.Image, ToolPublication: tool.Publication, Capabilities: append(json.RawMessage(nil), tool.Capabilities...), CapabilitiesSHA256: environmentPreflightBytesSHA256(tool.Capabilities)}
	repairStage, retainStage := failedForwardStageNames(q)
	if repairStage == "" || retainStage == "" {
		return bad()
	}
	live, err := inventory(ctx)
	if err != nil {
		return empty, nil, nil, err
	}
	inputs := deploymentplan.Inputs{Environment: cfg.Environment, Stack: parent.Stack, Operation: "scoped", ConfigurationSHA256: parent.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: environmentCreateStageBindingsSHA(repairStage, jobs)}
	repair, err := deploymentplan.Compile(inputs, jobs, live)
	if err != nil {
		return empty, nil, nil, err
	}
	retained, err := compileFailedStageRetainForward(original, journal, stage, live, retainStage)
	if err != nil {
		return empty, nil, nil, err
	}
	q.CandidateSHA256, q.RetainCandidateSHA256 = repair.SHA256(), retained.SHA256()
	now, err := failedForwardQualificationTime(ctx, clock)
	if err != nil {
		return empty, nil, nil, err
	}
	q.QualifiedAt, q.ValidUntil = now, now.Add(time.Hour)
	if err = validateCreateFailedForwardProofBindings(q, admitted, admissionSHA, stage, repair, retained, now); err != nil {
		return empty, nil, nil, err
	}
	if err = repair.AdmitFailedForwardResumable(ctx, b, store.Root); err != nil {
		return empty, nil, nil, err
	}
	if err = retained.AdmitFailedForwardResumable(ctx, b, store.Root); err != nil {
		return empty, nil, nil, err
	}
	if e.Check(ctx) != nil || deploymentplan.Digest(e.Journal) != stage.ParentJournalSHA256 {
		return bad()
	}
	return q, repair, retained, nil
}

// The qualification timestamp must follow evidence and compilation and remain
// within the caller's existing deadline; it does not extend that deadline.
func failedForwardQualificationTime(ctx context.Context, clock func() time.Time) (time.Time, error) {
	if ctx == nil || clock == nil || ctx.Err() != nil {
		return time.Time{}, createProviderRecoveryBlock("failed-stage qualification exceeded its original deadline")
	}
	now := clock().UTC()
	if now.IsZero() {
		return time.Time{}, createProviderRecoveryBlock("failed-stage qualification clock is unavailable")
	}
	if deadline, ok := ctx.Deadline(); ok && !now.Before(deadline) {
		return time.Time{}, createProviderRecoveryBlock("failed-stage qualification exceeded its original deadline")
	}
	return now, nil
}
