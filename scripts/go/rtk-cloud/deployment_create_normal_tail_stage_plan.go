package main

import (
	"context"
	"os"
	"path/filepath"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type createNormalTailStagePlan struct {
	Stage, ProgramSHA256 string
	Plan                 *deploymentplan.Plan
}

func createNormalTailStagePlanPath(e *environmentCreateExecution, stage string) string {
	return filepath.Join(e.Directory, "normal-tail-stages", stage+".json")
}

// Save the original compiled input before the parent request. A crash before
// admission then has a real plan to inspect, without inventing a native effect
// or re-compiling a partial create as a retain plan.
func saveCreateNormalTailStagePlan(ctx context.Context, store secretStore, e *environmentCreateExecution, stage string, p *deploymentplan.Plan) error {
	if e == nil || e.Store != store {
		return createProviderRecoveryBlock("original preparation owner unavailable")
	}
	if err := e.Check(ctx); err != nil {
		return err
	}
	program, err := readCreateNormalTailProgram(store, e)
	if err != nil {
		return err
	}
	a := createNormalTailStagePlan{Stage: stage, ProgramSHA256: deploymentplan.Digest(program), Plan: p}
	if err := validateCreateNormalTailStagePlan(e, program, stage, a); err != nil {
		return err
	}
	if original, err := readCreateNormalTailPreparationPlan(store, e, stage); err != nil {
		return err
	} else if original != nil {
		if original.SHA256() != p.SHA256() {
			return createProviderRecoveryBlock("original preparation plan changed")
		}
		return nil
	}
	rel, err := filepath.Rel(store.Root, createNormalTailStagePlanPath(e, stage))
	if err != nil || filepath.IsAbs(rel) {
		return createProviderRecoveryBlock("preparation plan is outside original execution")
	}
	return store.write(rel, mustProviderRecoveryJSON(a), false)
}

func readCreateNormalTailPreparationPlan(store secretStore, e *environmentCreateExecution, stage string) (*deploymentplan.Plan, error) {
	if e == nil || e.Store != store || !secretEnvironmentPattern.MatchString(stage) {
		return nil, createProviderRecoveryBlock("original preparation stage unavailable")
	}
	path := createNormalTailStagePlanPath(e, stage)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
		return nil, createProviderRecoveryBlock("preparation input directory is unsafe")
	}
	program, err := readCreateNormalTailProgram(store, e)
	if err != nil {
		return nil, err
	}
	var a createNormalTailStagePlan
	if readEnvironmentPreflightJSON(path, &a) != nil {
		return nil, createProviderRecoveryBlock("original preparation input cannot be read")
	}
	if err := validateCreateNormalTailStagePlan(e, program, stage, a); err != nil {
		return nil, err
	}
	return a.Plan, nil
}

func validateCreateNormalTailStagePlan(e *environmentCreateExecution, program *createNormalTailProgram, stage string, a createNormalTailStagePlan) error {
	p := a.Plan
	if e == nil || program == nil || !secretEnvironmentPattern.MatchString(stage) || a.Stage != stage || a.ProgramSHA256 != deploymentplan.Digest(program) || p == nil || p.Operation != "scoped" || p.Environment != e.Journal.Environment || p.Stack != e.Journal.Stack || p.CreateParentSHA256 != e.Journal.PlanSHA256 || p.CreateHandoffSHA256 != program.HandoffSHA256 || p.ReleaseSHA256 != deploymentplan.Digest(program.Release) || p.ConfigurationSHA256 != program.MaintainedConfigSHA256 || len(p.Resources) == 0 {
		return createProviderRecoveryBlock("preparation plan differs from its frozen original program")
	}
	if stage == createNormalTailPostgresAccessStage {
		frozen, err := readCreateNormalTailFrozenTarget(e.Store, e)
		if err != nil {
			return err
		}
		return validateCreateNormalTailPostgresAccessPlan(p, frozen)
	}
	if program.Products != nil && stage == "product-identities-"+productIdentityScope(program.Products)[:12]+"-steady" {
		return validateProductSteadyPlanOwners(e.Store, e, p)
	}
	return nil
}
