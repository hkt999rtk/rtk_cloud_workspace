package main

import (
	"slices"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// compileFailedStageRetainForward creates an independent native child whose
// only action is to retain and verify every exact original completed owner.
// The original failed child journal is never edited or replayed.
func compileFailedStageRetainForward(original *deploymentplan.Plan, journal deploymentplan.Journal, qualified createFailedStageQualification, live []deploymentplan.Object, stage string) (*deploymentplan.Plan, error) {
	bad := func() (*deploymentplan.Plan, error) {
		return nil, createProviderRecoveryBlock("fully written original stage requires exact journal owners and retain-only native forward")
	}
	if original == nil || !secretEnvironmentPattern.MatchString(stage) || original.SHA256() != qualified.Child.PlanSHA256 || journal.PlanSHA256 != qualified.Child.PlanSHA256 || deploymentplan.Digest(journal) != qualified.Child.JournalSHA256 || journal.Failed != qualified.Child.FailedResource || journal.Failed == "" || journal.InFlight != "" || journal.Accepted || journal.AcceptanceStarted || len(journal.Completed) != len(original.Resources) || len(journal.Owners) != len(original.Resources) {
		return bad()
	}
	defs := make([]deploymentplan.Definition, 0, len(original.Resources))
	for index, r := range original.Resources {
		uid := journal.Owners[r.ID]
		if journal.Completed[index] != r.ID || uid == "" || r.Action == "retire" || deploymentplan.ID(r.Desired) != r.ID {
			return bad()
		}
		desired := deploymentplan.Clone(r.Desired)
		meta := deploymentplan.Map(desired["metadata"])
		if meta == nil {
			return bad()
		}
		meta["uid"] = uid
		defs = append(defs, deploymentplan.Definition{Object: desired, Phase: r.Phase, Retain: true, Dependencies: slices.Clone(r.Dependencies)})
	}
	inputs := original.Inputs
	inputs.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, defs)
	candidate, err := deploymentplan.Compile(inputs, defs, live)
	if err != nil {
		return nil, err
	}
	if len(candidate.Resources) != len(original.Resources) {
		return bad()
	}
	for index, r := range candidate.Resources {
		src := original.Resources[index]
		if r.ID != src.ID || r.Action != "retain" || r.UID != journal.Owners[r.ID] || !deploymentplan.Matches(src.Desired, r.Desired) || deploymentplan.Digest(r.Dependencies) != deploymentplan.Digest(src.Dependencies) {
			return bad()
		}
	}
	return candidate, nil
}
