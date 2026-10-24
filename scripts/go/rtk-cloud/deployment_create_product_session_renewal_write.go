package main

import (
	"context"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func productSessionRenewalExpectedOwner(l *createNormalTailProductLifecycle, s productSessionRenewalSource, id string) string {
	if id == "Deployment/"+l.Bootstrap.Stack+"-video-cloud/pki-product-custody-"+productIdentityScope(l)[:12] {
		return s.CustodyUID
	}
	if uid := s.ControlOwners[id]; uid != "" {
		return uid
	}
	for _, d := range s.ControlSources {
		if deploymentplan.ID(d.Object) == id {
			return deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["uid"])
		}
	}
	return ""
}

// A source-derived new name is never permission to adopt an unrecorded object.
// Known standard child writes retain the ordinary engine's exact reconciliation.
func checkProductSessionRenewalWrite(ctx context.Context, store secretStore, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, s productSessionRenewalSource, suffix string, defs []deploymentplan.Definition) error {
	stage := productSessionRenewalStage(l, s, suffix)
	p, err := readCreateNormalTailPreparationPlan(store, runtime.Execution, stage)
	if err != nil {
		return err
	}
	var j deploymentplan.Journal
	exists := false
	if p != nil {
		j, exists, err = p.ReadExecution(store.Root)
		if err != nil {
			return err
		}
		if exists && createOpenBaoACKCompletionEntry(runtime.Execution.Journal, "kubernetes/"+stage) == nil {
			return productSessionRenewalBlock("renewal child has no parent request")
		}
		if p.BindingsSHA256 != environmentCreateStageBindingsSHA(stage, defs) || len(p.Resources) != len(defs) {
			return productSessionRenewalBlock("saved renewal child recipe")
		}
		for _, resource := range p.Resources {
			uid := productSessionRenewalExpectedOwner(l, s, resource.ID)
			if uid == "" {
				if resource.Action != "create" || resource.UID != "" || resource.Baseline != nil {
					return productSessionRenewalBlock("unrecorded renewal adoption")
				}
			} else if resource.UID != uid {
				return productSessionRenewalBlock("renewal original owner changed")
			}
		}
	}
	for _, d := range defs {
		id := deploymentplan.ID(d.Object)
		uid := productSessionRenewalExpectedOwner(l, s, id)
		actual, er := runtime.Backend.Read(ctx, id)
		if er != nil {
			return er
		}
		if uid != "" {
			if actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != uid || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil {
				return productSessionRenewalBlock("native renewal owner replaced")
			}
			continue
		}
		known := ""
		if exists {
			known = j.Owners[id]
		}
		if known == "" && actual != nil {
			return productSessionRenewalBlock("unrecorded renewal native resource exists")
		}
		if known != "" && (actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != known) {
			return productSessionRenewalBlock("recorded renewal native owner changed")
		}
	}
	return runtime.Execution.Check(ctx)
}
