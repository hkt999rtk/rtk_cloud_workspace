package main

import (
	"context"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Registration keeps its original dependency graph, but a completed enrollment
// closure may already have advanced those same native owners. Intent comes from
// authenticated child history; the live API only verifies the resulting body.
func currentEnvironmentCreateAccountRegistrationCore(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, core []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	if runtime.Execution == nil || createOpenBaoACKCompletionEntry(runtime.Execution.Journal, "product-identities/fresh") == nil {
		return core, nil
	}
	if entry := createOpenBaoACKCompletionEntry(runtime.Execution.Journal, "product-identities/fresh"); entry.State != "completed" {
		return nil, environmentCreateBootstrapBlocker("account registration requires its original completed fresh product program")
	}
	return currentEnvironmentCreateAccountRegistrationCoreWithBackend(ctx, cfg, runtime, core, runtime.Backend)
}

func currentEnvironmentCreateAccountRegistrationCoreWithBackend(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, core []deploymentplan.Definition, backend deploymentplan.Backend) ([]deploymentplan.Definition, error) {
	e := runtime.Execution
	bad := func() ([]deploymentplan.Definition, error) {
		return nil, environmentCreateBootstrapBlocker("account registration prerequisites differ from their original current native owner chain")
	}
	if e == nil || backend == nil || e.Journal.Environment != cfg.Environment || e.Journal.Stack != cfg.Values["CLOUD_STACK_NAME"] || runtime.ExecutionSHA256 != e.Journal.PlanSHA256 || e.Check(ctx) != nil {
		return bad()
	}
	p, j, err := acceptedCurrentCoreStage(e.Store, e)
	if err != nil {
		return nil, err
	}
	var key string
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/managed-complete-issuer")
	if entry != nil && currentCoreForwardReceiptMatches(entry.Receipt) {
		var r currentCoreForwardReceipt
		if decodeEnvironmentJSON(entry.Receipt, &r) != nil {
			return bad()
		}
		key = r.ForwardStage
	} else {
		key = "kubernetes/managed-complete-issuer"
	}
	checked, exists, err := readEnvironmentCreateStageProgress(e.Store, e, strings.TrimPrefix(key, "kubernetes/"), p)
	if err != nil || !exists || deploymentplan.Digest(checked) != deploymentplan.Digest(j) {
		return bad()
	}
	resources := map[string]deploymentplan.Resource{}
	if err := managedLateRecordedOwners(p, j, resources); err != nil {
		return nil, err
	}
	var base []activeTransportRenewalResource
	for _, r := range p.Resources {
		owner := resources[r.ID]
		base = append(base, sessionRecordedOwnerBase(owner.ID, owner.UID, owner.Desired))
	}
	latest, err := completedRecordedOwnerSuccessorsAfter(ctx, e.Store, e, backend, key, base)
	if err != nil {
		return nil, err
	}
	if err := verifyAcceptedStageAfterRecordedWrites(ctx, p, j, backend.Read, latest); err != nil {
		return nil, err
	}
	byID := map[string]activeTransportRenewalResource{}
	for _, owner := range latest {
		byID[owner.ID] = owner
	}
	out := make([]deploymentplan.Definition, 0, len(p.Resources))
	seen := map[string]bool{}
	// KV projection removes the renderer's physical claims and adds its
	// authenticated auth dependencies. Use that frozen accepted graph rather
	// than comparing raw renderer IDs with the projected native scope.
	for _, r := range p.Resources {
		owner := byID[r.ID]
		if owner.UID != resources[r.ID].UID {
			return bad()
		}
		d := deploymentplan.Definition{Object: deploymentplan.Clone(owner.Desired), Phase: r.Phase, Dependencies: slices.Clone(r.Dependencies)}
		deploymentplan.Map(d.Object["metadata"])["uid"] = owner.UID
		seen[r.ID] = true
		out = append(out, d)
	}
	for _, id := range []string{"Deployment/" + e.Journal.Stack + "-video-cloud/certissuer", "Deployment/" + e.Journal.Stack + "-video-cloud/pki-controller", "Deployment/" + e.Journal.Stack + "-account-manager/account-manager"} {
		if !seen[id] {
			return bad()
		}
	}
	return out, e.Check(ctx)
}
