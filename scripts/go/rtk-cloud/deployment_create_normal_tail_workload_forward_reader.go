package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The logical workload may have a composite response, while its literal first
// child remains the truthful failed prefix. Never copy forward owners into it.
func readCreateNormalTailWorkloadOriginal(store secretStore, e *environmentCreateExecution, frozen *deploymentplan.Plan) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	if e == nil || e.Store != store {
		return nil, deploymentplan.Journal{}, createProviderRecoveryBlock("original workload owner unavailable")
	}
	view := *e
	view.Journal = e.Journal
	view.Journal.Entries = slices.Clone(e.Journal.Entries)
	for i := range view.Journal.Entries {
		entry := &view.Journal.Entries[i]
		if entry.Key != "kubernetes/workload" {
			continue
		}
		var response createNormalTailWorkloadForwardReceipt
		if decodeEnvironmentJSON(entry.Receipt, &response) == nil && response.ForwardStage != "" {
			entry.State = "pending"
			entry.Receipt = nil
			entry.CompletedAt = time.Time{}
		}
	}
	return readCreateNormalTailStage(store, &view, "workload", frozen)
}

func readCreateNormalTailWorkloadForwardDescriptor(store secretStore, e *environmentCreateExecution, original *deploymentplan.Plan, j deploymentplan.Journal) (*createNormalTailWorkloadForward, error) {
	bad := func() (*createNormalTailWorkloadForward, error) {
		return nil, createProviderRecoveryBlock("workload forward lost its original program, source, owner or exact correction")
	}
	if e == nil || e.Store != store || original == nil {
		return bad()
	}
	path := createNormalTailWorkloadForwardPath(e)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		if createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+createNormalTailWorkloadForwardStage) != nil {
			return bad()
		}
		if entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload"); entry != nil {
			var receipt createNormalTailWorkloadForwardReceipt
			if decodeEnvironmentJSON(entry.Receipt, &receipt) == nil && receipt.ForwardStage != "" {
				return bad()
			}
		}
		return nil, nil
	} else if err != nil {
		return bad()
	}
	if validateEnvironmentPreflightDirectories(store, e.Directory) != nil {
		return bad()
	}
	var f createNormalTailWorkloadForward
	if readEnvironmentPreflightJSON(path, &f) != nil {
		return bad()
	}
	program, err := readCreateNormalTailProgram(store, e)
	if err != nil {
		return nil, err
	}
	var target createNormalTailTarget
	if readEnvironmentPreflightJSON(filepath.Join(e.Directory, "normal-tail-target.json"), &target) != nil || target.Schema != createNormalTailTargetSchema || target.ParentSHA256 != e.Journal.PlanSHA256 || target.OwnerSHA256 != deploymentplan.Digest(e.owner) || len(target.Pins) == 0 || target.Plan == nil || target.Plan.SHA256() != original.SHA256() {
		return bad()
	}
	if f.Schema != createNormalTailWorkloadForwardSchema || f.ParentSHA256 != e.Journal.PlanSHA256 || f.OwnerSHA256 != deploymentplan.Digest(e.owner) || f.ProgramSHA256 != deploymentplan.Digest(program) || f.OriginalPlanSHA256 != original.SHA256() || f.OriginalJournalSHA256 != deploymentplan.Digest(j) || f.ForwardStage != createNormalTailWorkloadForwardStage || f.Target.Schema != createNormalTailTargetSchema || f.Target.ParentSHA256 != f.ParentSHA256 || f.Target.OwnerSHA256 != f.OwnerSHA256 || f.Target.Plan == nil || deploymentplan.Digest(f.Target.Pins) != deploymentplan.Digest(target.Pins) || deploymentplan.Digest(f.Target.Release) != deploymentplan.Digest(target.Release) || deploymentplan.Digest(f.Target.Bindings) != deploymentplan.Digest(target.Bindings) || f.Target.Plan.CreateHandoffSHA256 != deploymentplan.Digest(target.Pins) {
		return bad()
	}
	if err := validateCreateNormalTailWorkloadForwardPlan(original, j, f.Target.Plan); err != nil {
		// An exact legacy derivation can only be read far enough to prepare
		// its corrected replacement before any forward request or child exists.
		// It never authenticates a started/accepted legacy execution.
		if validateCreateNormalTailWorkloadForwardLegacyPlan(original, j, f.Target.Plan) != nil || createNormalTailWorkloadForwardUnstarted(store, e, &f) != nil {
			return nil, err
		}
	}
	{
		access, accepted, err := readCreateNormalTailPostgresAccessStage(store, e, original, j)
		if err != nil {
			return nil, err
		}
		if access == nil || !accepted.Accepted || len(f.Target.Plan.Resources)-len(original.Resources) != len(access.Resources) {
			return bad()
		}
		expected := map[string]deploymentplan.Resource{}
		for _, resource := range access.Resources {
			expected[resource.ID] = resource
		}
		for _, resource := range f.Target.Plan.Resources[len(original.Resources):] {
			source, exists := expected[resource.ID]
			if !exists || resource.Action != "retain" || resource.UID != accepted.Owners[resource.ID] || !environmentBootstrapOwnerUUID.MatchString(resource.UID) || resource.Phase != source.Phase || deploymentplan.Digest(resource.Desired) != deploymentplan.Digest(source.Desired) || deploymentplan.Map(resource.Baseline["metadata"])["uid"] != resource.UID || !deploymentplan.Matches(source.Desired, resource.Baseline) || deploymentplan.Digest(resource.Baseline) != deploymentplan.Digest(deploymentplan.Projection(resource.Baseline)) || deploymentplan.Digest(resource.Dependencies) != deploymentplan.Digest(source.Dependencies) {
				return bad()
			}
			delete(expected, resource.ID)
		}
	}
	return &f, nil
}

func readCreateNormalTailWorkloadSources(store secretStore, e *environmentCreateExecution, frozen *deploymentplan.Plan) ([]createNormalTailVolumeStage, error) {
	bad := func() ([]createNormalTailVolumeStage, error) {
		return nil, createProviderRecoveryBlock("workload forward child differs from its preserved original source or composite response")
	}
	original, err := readCreateNormalTailFrozenTarget(store, e)
	if err != nil {
		return nil, err
	}
	p, j, err := readCreateNormalTailWorkloadOriginal(store, e, original)
	if err != nil {
		return nil, err
	}
	if p == nil {
		if frozen == nil || frozen.SHA256() != original.SHA256() {
			return bad()
		}
		if _, err := os.Lstat(createNormalTailWorkloadForwardPath(e)); !os.IsNotExist(err) {
			return bad()
		}
		return nil, nil
	}
	f, err := readCreateNormalTailWorkloadForwardDescriptor(store, e, p, j)
	if err != nil {
		return nil, err
	}
	successor, err := readCreateNormalTailProductSessionForward(store, e, original, j, f)
	if err != nil {
		return nil, err
	}
	if successor != nil {
		chain, err := readCreateNormalTailRuntimeChain(store, e)
		if err != nil {
			return nil, err
		}
		runtime, err := readCreateNormalTailRuntimeForwardFromChain(store, e, chain)
		if err != nil {
			return nil, err
		}
		if runtime != nil {
			return readCreateNormalTailRuntimeWorkloadSourcesFromChain(frozen, p, j, f, successor, runtime, chain)
		}
		return readCreateNormalTailProductSessionWorkloadSources(store, e, frozen, p, j, f, successor)
	}
	if frozen == nil || frozen.SHA256() != original.SHA256() && (f == nil || frozen.SHA256() != f.Target.Plan.SHA256()) {
		return bad()
	}
	sources := []createNormalTailVolumeStage{{Stage: "workload", Plan: p, Journal: j}}
	if f == nil {
		return sources, nil
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+f.ForwardStage)
	if entry == nil {
		if _, exists, err := f.Target.Plan.ReadExecution(store.Root); err != nil || exists {
			return bad()
		}
		for _, lease := range e.Journal.KubernetesLeases {
			if strings.HasPrefix(lease.Holder, f.Target.Plan.SHA256()+":") {
				return bad()
			}
		}
		return sources, nil
	}
	forward, actual, err := readCreateNormalTailStage(store, e, f.ForwardStage, f.Target.Plan)
	if err != nil {
		return nil, err
	}
	if forward == nil {
		return sources, nil
	}
	source := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload")
	if source == nil {
		return bad()
	}
	if source.State != "pending" {
		var receipt createNormalTailWorkloadForwardReceipt
		if decodeEnvironmentJSON(source.Receipt, &receipt) != nil || receipt.OriginalPlanSHA256 != p.SHA256() || receipt.OriginalJournalSHA256 != deploymentplan.Digest(j) || receipt.ForwardStage != f.ForwardStage || receipt.ForwardPlanSHA256 != forward.SHA256() || receipt.ForwardJournalSHA256 != deploymentplan.Digest(actual) || !actual.Accepted || entry.State != "completed" {
			return bad()
		}
	}
	return append(sources, createNormalTailVolumeStage{Stage: f.ForwardStage, Plan: forward, Journal: actual, originalPlanSHA256: p.SHA256()}), nil
}

func readCreateNormalTailWorkloadEffective(store secretStore, e *environmentCreateExecution, frozen *deploymentplan.Plan) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	sources, err := readCreateNormalTailWorkloadSources(store, e, frozen)
	if err != nil || len(sources) == 0 {
		return nil, deploymentplan.Journal{}, err
	}
	last := sources[len(sources)-1]
	return last.Plan, last.Journal, nil
}

// Compare a recorded five-field response exactly; no ordinary accepted-child
// receipt is fabricated for the original failed native execution.
func createNormalTailWorkloadForwardResponseMatches(entry environmentCreateJournalEntry, f *createNormalTailWorkloadForward, original deploymentplan.Journal, forward *deploymentplan.Plan, j deploymentplan.Journal) bool {
	if entry.State == "pending" {
		return len(entry.Receipt) == 0 && entry.CompletedAt.IsZero()
	}
	var actual createNormalTailWorkloadForwardReceipt
	want := createNormalTailWorkloadForwardReceipt{f.OriginalPlanSHA256, deploymentplan.Digest(original), f.ForwardStage, forward.SHA256(), deploymentplan.Digest(j)}
	return j.Accepted && decodeEnvironmentJSON(entry.Receipt, &actual) == nil && deploymentplan.Digest(actual) == deploymentplan.Digest(want)
}
