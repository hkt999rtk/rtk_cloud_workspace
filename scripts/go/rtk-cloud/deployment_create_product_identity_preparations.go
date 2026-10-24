package main

import (
	"context"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The native enrollment and custody controllers remain real, owned workloads
// until session finalization has erased the temporary bootstrap credential.
// Their frozen child plans supply both their desired bodies and exact UIDs;
// a current inventory is never used as a renderer.
func productPreparationDefinitions(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, installed *environmentProductIdentitySession, retire bool, fresh ...bool) ([]deploymentplan.Definition, error) {
	bad := func() ([]deploymentplan.Definition, error) {
		return nil, environmentCreateBootstrapBlocker("product preparation owner differs from its accepted original recipe")
	}
	if len(fresh) > 1 || e == nil || l == nil || installed == nil || e.Store != store || installed.PlanSHA256 != productIdentityScope(l) || installed.ParentSHA256 != e.Journal.PlanSHA256 || installed.OwnerSHA256 != deploymentplan.Digest(e.owner) {
		return bad()
	}
	isFresh := len(fresh) == 1 && fresh[0]
	originalInstalled := installed
	var renewal *productSessionRenewal
	if !isFresh {
		var er error
		renewal, er = readProductSessionRenewal(store, e, l)
		if er != nil {
			return nil, er
		}
		if renewal != nil {
			originalInstalled, er = readProductInstalled(store, l)
			if er != nil {
				return nil, er
			}
		}
	}
	stages := []string{"service-custody-reader"}
	for _, output := range l.Outputs {
		stages = append(stages, "enroll-"+strings.TrimPrefix(output.Subject, "service:"))
	}
	seen := map[string]bool{}
	var definitions []deploymentplan.Definition
	for _, stage := range stages {
		recipe, err := productStageDefinitions(context.Background(), cfg, l, originalInstalled.Ceremony, stage)
		if err != nil {
			return nil, err
		}
		full := "product-identities-" + productIdentityScope(l)[:12] + "-" + stage
		if renewal != nil && stage == "service-custody-reader" {
			recipe, err = productSessionRenewalCustodyDefinitions(cfg, l, renewal.Source, renewal.Original.Ceremony)
			if err != nil {
				return nil, err
			}
			full = productSessionRenewalStage(l, renewal.Source, "custody")
		}
		if isFresh && stage == "service-custody-reader" {
			full = stage
		}
		p, j, err := acceptedCreateStage(store, e, "kubernetes/"+full)
		if err != nil || !isFresh && p.CreateParentSHA256 != e.Journal.PlanSHA256 || p.Environment != cfg.Environment || p.Stack != e.Journal.Stack || p.BindingsSHA256 != environmentCreateStageBindingsSHA(full, recipe) || len(p.Resources) != len(recipe) {
			return bad()
		}
		verified, err := readProductPreparationProgress(store, e, full, p, isFresh)
		if err != nil || !verified.Accepted || deploymentplan.Digest(verified) != deploymentplan.Digest(j) {
			return bad()
		}
		want := map[string]deploymentplan.Definition{}
		for _, definition := range recipe {
			want[deploymentplan.ID(definition.Object)] = definition
		}
		owners := 0
		for _, resource := range p.Resources {
			definition, exists := want[resource.ID]
			if !exists || resource.Phase != definition.Phase || !deploymentplan.Matches(deploymentplan.Projection(definition.Object), resource.Desired) || resource.Action == "retire" {
				return bad()
			}
			if resource.Desired["kind"] != "Deployment" {
				continue
			}
			owners++
			if seen[resource.ID] || !environmentBootstrapOwnerUUID.MatchString(j.Owners[resource.ID]) {
				return bad()
			}
			seen[resource.ID] = true
			object := deploymentplan.Clone(resource.Desired)
			deploymentplan.Map(object["metadata"])["uid"] = j.Owners[resource.ID]
			definition = deploymentplan.Definition{Object: object, Phase: "workload", Retain: true}
			if retire {
				definition.Phase, definition.Retain, definition.Retire = "retirement", false, true
			}
			definitions = append(definitions, definition)
		}
		if owners != 1 {
			return bad()
		}
	}
	return definitions, nil
}

func readProductPreparationProgress(store secretStore, e *environmentCreateExecution, stage string, p *deploymentplan.Plan, fresh bool) (deploymentplan.Journal, error) {
	if fresh {
		input, err := readEnvironmentCreateStageInput(store, e, stage)
		if err != nil || input == nil || input.SHA256() != p.SHA256() {
			return deploymentplan.Journal{}, environmentCreateBootstrapBlocker("fresh product preparation lost its original stage input")
		}
		j, exists, err := readEnvironmentCreateStageProgress(store, e, stage, p)
		if err != nil {
			return j, err
		}
		if !exists {
			return j, environmentCreateBootstrapBlocker("fresh product preparation has no original child")
		}
		return j, nil
	}
	child, j, err := readCreateNormalTailStage(store, e, stage, p)
	if err != nil {
		return j, err
	}
	if child == nil {
		return j, environmentCreateBootstrapBlocker("product preparation has no original child")
	}
	return j, nil
}

func productPreparationsRetired(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, installed *environmentProductIdentitySession, backend deploymentplan.Backend, fresh ...bool) error {
	definitions, err := productPreparationDefinitions(cfg, store, e, l, installed, true, fresh...)
	if err != nil {
		return err
	}
	full := "product-identities-" + productIdentityScope(l)[:12] + "-producer-retire"
	isFresh := len(fresh) == 1 && fresh[0]
	if isFresh {
		full = "fresh-product-producer-retire"
	}
	p, j, err := acceptedCreateStage(store, e, "kubernetes/"+full)
	if err != nil || p.BindingsSHA256 != environmentCreateStageBindingsSHA(full, definitions) || len(p.Resources) != len(definitions) {
		return environmentCreateBootstrapBlocker("product preparations have no genuine completed retirement")
	}
	verified, err := readProductPreparationProgress(store, e, full, p, isFresh)
	if err != nil || !verified.Accepted || deploymentplan.Digest(verified) != deploymentplan.Digest(j) {
		return environmentCreateBootstrapBlocker("product retirement parent differs from its genuinely accepted child")
	}
	expected := map[string]deploymentplan.Definition{}
	for _, definition := range definitions {
		expected[deploymentplan.ID(definition.Object)] = definition
	}
	for _, resource := range p.Resources {
		definition, exists := expected[resource.ID]
		if !exists || resource.Action != "retire" || resource.Phase != "retirement" || resource.UID != deploymentplan.Map(definition.Object["metadata"])["uid"] || j.Owners[resource.ID] != resource.UID || !deploymentplan.Matches(deploymentplan.Projection(definition.Object), resource.Desired) {
			return environmentCreateBootstrapBlocker("product preparation retirement changed its original owner")
		}
		actual, err := backend.Read(ctx, resource.ID)
		if err != nil || actual != nil {
			return environmentCreateBootstrapBlocker("retired product preparation is not absent")
		}
	}
	return e.Check(ctx)
}

// Steady is an exact update to the already accepted workload, including owners
// first created by that workload. A fresh inventory cannot select a replacement.
func validateProductSteadyPlanOwners(store secretStore, e *environmentCreateExecution, steady *deploymentplan.Plan) error {
	initial, err := readCreateNormalTailFrozenTarget(store, e)
	if err != nil {
		return err
	}
	_, original, err := readCreateNormalTailWorkload(store, e, initial)
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload")
	if err != nil || !original.Accepted || entry == nil || entry.State != "completed" || steady == nil {
		return environmentCreateBootstrapBlocker("product steady transition requires its original accepted workload owners")
	}
	journal, exists, err := steady.ReadExecution(store.Root)
	if err != nil {
		return err
	}
	for _, resource := range steady.Resources {
		owner := original.Owners[resource.ID]
		if !environmentBootstrapOwnerUUID.MatchString(owner) || resource.UID != owner || exists && journal.Owners[resource.ID] != "" && journal.Owners[resource.ID] != owner {
			return environmentCreateBootstrapBlocker("product steady transition changed an original workload owner: " + resource.ID)
		}
	}
	return nil
}
