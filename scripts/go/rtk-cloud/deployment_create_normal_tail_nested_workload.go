package main

import (
	"encoding/json"
	"os"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The original workload may contain one standard additive access repair before
// its remaining writes. It remains a real child, not success inferred from a
// policy found in inventory; its plan also supplies the recorded lease owner.
func validateCreateNormalTailNestedAccessRepair(store secretStore, e *environmentCreateExecution, child *deploymentplan.Plan, journal deploymentplan.Journal, outer environmentCreateJournalEntry, following []environmentCreateJournalEntry, admission createNormalTailProgramAdmission) (int, []*deploymentplan.Plan, error) {
	if len(following) == 0 || following[0].Key != "kubernetes/"+createNormalTailPostgresAccessStage {
		count, err := validateCreateNormalTailNestedUnseals(store, e, child, journal, outer, following, admission)
		return count, nil, err
	}
	bad := func() (int, []*deploymentplan.Plan, error) {
		return 0, nil, createProviderRecoveryBlock("nested PostgreSQL access repair differs from its original workload, child or admitted window")
	}
	if e == nil || e.Store != store || child == nil || outer.Key != "kubernetes/workload" || outer.Phase != "workload" || journal.PlanSHA256 != child.SHA256() || journal.InFlight != "" {
		return bad()
	}
	p, _, err := readCreateNormalTailPostgresAccessStage(store, e, child, journal)
	if err != nil {
		return 0, nil, err
	}
	if p == nil {
		return bad()
	}
	entry := following[0]
	intent, err := environmentCreateKubernetesStageIntent(p)
	if err != nil {
		return bad()
	}
	sha, err := environmentCreatePlanSHA256(intent)
	if err != nil || entry.Phase != createNormalTailPostgresAccessStage || entry.IntentSHA256 != sha || entry.StartedAt.IsZero() || entry.StartedAt.Before(outer.StartedAt) || entry.StartedAt.Before(admission.AdmittedAt) || !entry.StartedAt.Before(admission.Deadline) || entry.StartedAt.After(time.Now().UTC()) {
		return bad()
	}
	switch entry.State {
	case "pending":
		if len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero() {
			return bad()
		}
	case "allocated":
		if len(entry.Receipt) == 0 || !entry.CompletedAt.IsZero() {
			return bad()
		}
	case "completed":
		if len(entry.Receipt) == 0 || entry.CompletedAt.Before(entry.StartedAt) || !entry.CompletedAt.Before(admission.Deadline) || entry.CompletedAt.After(time.Now().UTC()) || outer.State == "completed" && entry.CompletedAt.After(outer.CompletedAt) {
			return bad()
		}
	default:
		return bad()
	}
	if entry.State != "completed" {
		if outer.State == "completed" || len(following) != 1 {
			return bad()
		}
		return 1, []*deploymentplan.Plan{p}, nil
	}
	if len(following) > 1 && following[1].StartedAt.Before(entry.CompletedAt) {
		return bad()
	}
	count, err := validateCreateNormalTailNestedUnseals(store, e, child, journal, outer, following[1:], admission)
	if err != nil {
		return 0, nil, err
	}
	return 1 + count, []*deploymentplan.Plan{p}, nil
}

// The original pending workload can contain its exact access correction and a
// linked corrected workload. Each still owns a standard child and real lease.
func validateCreateNormalTailNestedWorkloadStages(store secretStore, e *environmentCreateExecution, original *deploymentplan.Plan, j deploymentplan.Journal, outer environmentCreateJournalEntry, following []environmentCreateJournalEntry, admission createNormalTailProgramAdmission) (int, []*deploymentplan.Plan, error) {
	bad := func() (int, []*deploymentplan.Plan, error) {
		return 0, nil, createProviderRecoveryBlock("workload forward differs from its original logical stage or nested child order")
	}
	if original == nil {
		if e == nil {
			return bad()
		}
		if _, err := os.Lstat(createNormalTailWorkloadForwardPath(e)); !os.IsNotExist(err) {
			return bad()
		}
		count, err := validateCreateNormalTailNestedUnseals(store, e, original, j, outer, following, admission)
		return count, nil, err
	}
	f, err := readCreateNormalTailWorkloadForwardDescriptor(store, e, original, j)
	if err != nil {
		return 0, nil, err
	}
	consumed := 0
	var children []*deploymentplan.Plan
	previous := outer.StartedAt
	if len(following) > 0 && following[0].Key == "kubernetes/"+createNormalTailPostgresAccessStage {
		// Validate this singleton entry here; the following forward is an ordinary
		// separate child, not an unseal or permission for an arbitrary suffix.
		count, plans, err := validateCreateNormalTailNestedAccessRepair(store, e, original, j, outer, following[:1], admission)
		if err != nil {
			return 0, nil, err
		}
		consumed = count
		children = append(children, plans...)
		if following[0].State != "completed" {
			if len(following) != 1 {
				return bad()
			}
			return consumed, children, nil
		}
		previous = following[0].CompletedAt
	}
	remaining := following[consumed:]
	if len(remaining) > 0 && remaining[0].Key == "kubernetes/"+createNormalTailWorkloadForwardStage {
		if f == nil || outer.Key != "kubernetes/workload" || outer.Phase != "workload" || remaining[0].StartedAt.Before(previous) {
			return bad()
		}
		if count, plans, handled, err := validateCreateNormalTailNestedSessionRecovery(store, e, original, j, f, outer, remaining, admission); handled || err != nil {
			return consumed + count, append(children, plans...), err
		}
		p, actual, err := readCreateNormalTailStage(store, e, f.ForwardStage, f.Target.Plan)
		if err != nil {
			return 0, nil, err
		}
		nested, err := validateCreateNormalTailNestedUnseals(store, e, f.Target.Plan, actual, remaining[0], remaining[1:], admission)
		if err != nil {
			return 0, nil, err
		}
		intent, err := environmentCreateKubernetesStageIntent(f.Target.Plan)
		if err != nil {
			return 0, nil, err
		}
		r := productStageReader{entries: remaining, admission: admission}
		complete := r.takeNested("kubernetes/"+f.ForwardStage, f.ForwardStage, json.RawMessage(intent), func(entry environmentCreateJournalEntry) bool {
			return entry.State == "pending" || p != nil && actual.Accepted
		}, nested)
		if r.err != nil {
			return 0, nil, r.err
		}
		if outer.State != "pending" && (!complete || p == nil || !createNormalTailWorkloadForwardResponseMatches(outer, f, j, p, actual)) {
			return bad()
		}
		if outer.State == "completed" && remaining[0].CompletedAt.After(outer.CompletedAt) {
			return bad()
		}
		if p != nil {
			children = append(children, p)
		}
		return consumed + r.n, children, nil
	}
	if f != nil && outer.State != "pending" {
		return bad()
	}
	count, err := validateCreateNormalTailNestedUnseals(store, e, original, j, outer, remaining, admission)
	if err != nil {
		return 0, nil, err
	}
	return consumed + count, children, nil
}

// Renewal is nested inside the truthful zero-write first forward. Every native
// operation and child is still ordered by the original admitted program.
func validateCreateNormalTailNestedSessionRecovery(store secretStore, e *environmentCreateExecution, original *deploymentplan.Plan, originalJournal deploymentplan.Journal, previous *createNormalTailWorkloadForward, outer environmentCreateJournalEntry, entries []environmentCreateJournalEntry, admission createNormalTailProgramAdmission) (int, []*deploymentplan.Plan, bool, error) {
	bad := func() (int, []*deploymentplan.Plan, bool, error) { return 0, nil, true, productSessionForwardBlocked() }
	program, err := readCreateNormalTailProgram(store, e)
	if err != nil {
		return 0, nil, true, err
	}
	if program.Products == nil || len(entries) == 0 {
		return 0, nil, false, nil
	}
	source, err := readProductSessionRenewalSourceForGeneration(store, e, program.Products, 0)
	if err != nil {
		return 0, nil, true, err
	}
	if source == nil {
		return 0, nil, false, nil
	}
	oldPlan, oldJournal, err := readCreateNormalTailPreviousForward(store, e, previous)
	if err != nil || oldPlan == nil || !productSessionForwardKnownZeroWrite(oldPlan, oldJournal) {
		return bad()
	}
	old := entries[0]
	r := productStageReader{store: store, e: e, l: program.Products, entries: entries[1:], admission: admission, completedAt: old.StartedAt}
	specs, err := productSessionRenewalProgressSpecs(store, e, program.Products, *source)
	if err != nil {
		return 0, nil, true, err
	}
	for _, spec := range specs {
		if r.n == len(r.entries) {
			break
		}
		if spec.Suffix != "" {
			if spec.Definitions == nil {
				return bad()
			}
			prefix := "product-identities-" + productIdentityScope(program.Products)[:12] + "-"
			if !r.kube(strings.TrimPrefix(spec.Phase, prefix), spec.Definitions, nil) {
				break
			}
		} else if !r.take(spec.Key, spec.Phase, spec.Intent, func(entry environmentCreateJournalEntry) bool {
			return entry.State == "pending" || deploymentplan.Digest(jsonRawObject(entry.Receipt)) == deploymentplan.Digest(spec.Intent)
		}) {
			break
		}
	}
	if r.err != nil {
		return 0, nil, true, r.err
	}
	var winner *deploymentplan.Plan
	var winnerJournal deploymentplan.Journal
	var successor *createNormalTailProductSessionForward
	winnerStage := ""
	if r.n < len(r.entries) {
		// A later workload cannot exist until the complete native renewal and its
		// exact preparation children have been authenticated.
		renewal, er := readProductSessionRenewalFirst(store, e, program.Products)
		if er != nil {
			return 0, nil, true, er
		}
		if renewal == nil {
			return bad()
		}
		successor, er = readCreateNormalTailProductSessionForward(store, e, original, originalJournal, previous)
		if er != nil {
			return 0, nil, true, er
		}
		if successor == nil || r.entries[r.n].Key != "kubernetes/"+successor.ForwardStage {
			return bad()
		}
		winnerStage = successor.ForwardStage
		intent, er := environmentCreateKubernetesStageIntent(successor.Target.Plan)
		if er != nil {
			return 0, nil, true, er
		}
		var nested int
		var runtime *createNormalTailRuntimeForward
		var runtimeChain []createNormalTailRuntimeStep
		if r.n+1 < len(r.entries) && r.entries[r.n+1].Key == "kubernetes/"+createNormalTailRuntimeForwardStage {
			runtimeChain, er = readCreateNormalTailRuntimeChain(store, e)
			if er != nil {
				return bad()
			}
			runtime, er = readCreateNormalTailRuntimeForwardFromChain(store, e, runtimeChain)
			if er != nil || runtime == nil {
				return bad()
			}
			prior, _, er := readCreateNormalTailRuntimePreviousStage(store, e, successor)
			if er != nil {
				return 0, nil, true, er
			}
			r.children = append(r.children, prior)
			var plans []*deploymentplan.Plan
			nested, plans, winner, winnerJournal, er = validateCreateNormalTailNestedRuntimeRecoveryFromChain(store, e, runtime, r.entries[r.n], r.entries[r.n+1:], admission, runtimeChain)
			if er != nil {
				return 0, nil, true, er
			}
			r.children = append(r.children, plans...)
			winnerStage = runtime.ForwardStage
		} else {
			winner, winnerJournal, er = readCreateNormalTailStage(store, e, successor.ForwardStage, successor.Target.Plan)
			if er != nil {
				return 0, nil, true, er
			}
			nested, er = validateCreateNormalTailNestedUnseals(store, e, winner, winnerJournal, r.entries[r.n], r.entries[r.n+1:], admission)
			if er != nil {
				return 0, nil, true, er
			}
		}
		r.takeNested("kubernetes/"+successor.ForwardStage, successor.ForwardStage, json.RawMessage(intent), func(entry environmentCreateJournalEntry) bool {
			if runtime != nil {
				if entry.State == "pending" {
					return createNormalTailRuntimeResponseMatches(entry, nil, nil, deploymentplan.Journal{})
				}
				effective := *runtimeChain[0].Descriptor
				effective.ForwardStage = runtime.ForwardStage
				return createNormalTailRuntimeResponseMatches(entry, &effective, winner, winnerJournal)
			}
			if winner != nil {
				r.children = append(r.children, winner)
			}
			return entry.State == "pending" || winner != nil && winnerJournal.Accepted
		}, nested)
		if r.err != nil {
			return 0, nil, true, r.err
		}
	}
	intent, err := environmentCreateKubernetesStageIntent(previous.Target.Plan)
	if err != nil {
		return 0, nil, true, err
	}
	oldReader := productStageReader{entries: entries, admission: admission}
	complete := oldReader.takeNested("kubernetes/"+previous.ForwardStage, previous.ForwardStage, json.RawMessage(intent), func(entry environmentCreateJournalEntry) bool {
		if entry.State == "pending" {
			return true
		}
		if successor == nil {
			return false
		}
		effective := *successor
		effective.ForwardStage = winnerStage
		return productSessionForwardResponseMatches(entry, &effective, winner, winnerJournal)
	}, r.n)
	if oldReader.err != nil {
		return 0, nil, true, oldReader.err
	}
	if outer.State != "pending" {
		if !complete || successor == nil || winner == nil || !winnerJournal.Accepted {
			return bad()
		}
		var response createNormalTailWorkloadForwardReceipt
		want := createNormalTailWorkloadForwardReceipt{OriginalPlanSHA256: original.SHA256(), OriginalJournalSHA256: deploymentplan.Digest(originalJournal), ForwardStage: winnerStage, ForwardPlanSHA256: winner.SHA256(), ForwardJournalSHA256: deploymentplan.Digest(winnerJournal)}
		if decodeEnvironmentJSON(outer.Receipt, &response) != nil || deploymentplan.Digest(response) != deploymentplan.Digest(want) || outer.State == "completed" && old.CompletedAt.After(outer.CompletedAt) {
			return bad()
		}
	}
	return oldReader.n, append([]*deploymentplan.Plan{oldPlan}, r.children...), true, nil
}

// The runtime correction is one exact ordinary child nested beneath the
// literal failed renewed workload. Its native journal remains unchanged.
func validateCreateNormalTailNestedRuntimeRecovery(store secretStore, e *environmentCreateExecution, d *createNormalTailRuntimeForward, outer environmentCreateJournalEntry, entries []environmentCreateJournalEntry, admission createNormalTailProgramAdmission) (int, []*deploymentplan.Plan, *deploymentplan.Plan, deploymentplan.Journal, error) {
	chain, err := readCreateNormalTailRuntimeChain(store, e)
	if err != nil {
		return 0, nil, nil, deploymentplan.Journal{}, err
	}
	return validateCreateNormalTailNestedRuntimeRecoveryFromChain(store, e, d, outer, entries, admission, chain)
}

// The caller has authenticated this chain, including winner and logical aliases,
// in this read. Independent callers retain the wrapper's fresh chain read.
func validateCreateNormalTailNestedRuntimeRecoveryFromChain(store secretStore, e *environmentCreateExecution, d *createNormalTailRuntimeForward, outer environmentCreateJournalEntry, entries []environmentCreateJournalEntry, admission createNormalTailProgramAdmission, chain []createNormalTailRuntimeStep) (int, []*deploymentplan.Plan, *deploymentplan.Plan, deploymentplan.Journal, error) {
	bad := func() (int, []*deploymentplan.Plan, *deploymentplan.Plan, deploymentplan.Journal, error) {
		return 0, nil, nil, deploymentplan.Journal{}, createNormalTailRuntimeBlock("numbered nested child order")
	}
	if d == nil || len(chain) == 0 || len(entries) == 0 || outer.Key != "kubernetes/"+createNormalTailProductSessionForwardStage || deploymentplan.Digest(d) != deploymentplan.Digest(chain[len(chain)-1].Descriptor) {
		return bad()
	}
	var consume func(int, environmentCreateJournalEntry, []environmentCreateJournalEntry) (int, []*deploymentplan.Plan, *deploymentplan.Plan, deploymentplan.Journal, error)
	consume = func(index int, parent environmentCreateJournalEntry, following []environmentCreateJournalEntry) (int, []*deploymentplan.Plan, *deploymentplan.Plan, deploymentplan.Journal, error) {
		if len(following) == 0 {
			return 0, nil, nil, deploymentplan.Journal{}, nil
		}
		step := chain[index]
		entry := following[0]
		if entry.Key != "kubernetes/"+step.Descriptor.ForwardStage {
			return bad()
		}
		p, j := step.Child, step.Journal
		if index == len(chain)-1 {
			var err error
			p, j, err = readCreateNormalTailStage(store, e, step.Descriptor.ForwardStage, step.Descriptor.Target.Plan)
			if err != nil {
				return 0, nil, nil, j, err
			}
		}
		unseals, err := validateCreateNormalTailNestedUnseals(store, e, p, j, entry, following[1:], admission)
		if err != nil {
			return 0, nil, nil, j, err
		}
		nested := unseals
		winner, winnerJournal := p, j
		var plans []*deploymentplan.Plan
		if p != nil {
			plans = append(plans, p)
		}
		previousAt := entry.StartedAt
		if unseals != 0 {
			previousAt = following[unseals].CompletedAt
		}
		repair, repairPlans, err := validateCreateNormalTailNestedAccountRegistrationCRLs(store, e, p, j, entry, following[1+unseals:], admission, previousAt)
		if err != nil {
			return 0, nil, nil, j, err
		}
		nested += repair
		plans = append(plans, repairPlans...)
		if repair != 0 {
			previousAt = following[nested].CompletedAt
		}
		renewed, renewalPlans, err := validateCreateNormalTailNestedSecondSessionRenewal(store, e, entry, following[1+nested:], admission, previousAt)
		if err != nil {
			return 0, nil, nil, j, err
		}
		nested += renewed
		plans = append(plans, renewalPlans...)
		if index+1 < len(chain) {
			if chain[index+1].Source.SessionRenewal != nil && renewed == 0 {
				return bad()
			}
			if chain[index+1].Source.RegistrationCRLs != nil && repair != 1 {
				return bad()
			}
			winner, winnerJournal = nil, deploymentplan.Journal{}
			if len(following) > 1+nested {
				count, children, w, wj, err := consume(index+1, entry, following[1+nested:])
				if err != nil {
					return 0, nil, nil, wj, err
				}
				nested += count
				plans = append(plans, children...)
				winner, winnerJournal = w, wj
			}
		}
		intent, err := environmentCreateKubernetesStageIntent(step.Descriptor.Target.Plan)
		if err != nil {
			return 0, nil, nil, j, err
		}
		r := productStageReader{store: store, e: e, entries: following, admission: admission, completedAt: parent.StartedAt}
		r.takeNested("kubernetes/"+step.Descriptor.ForwardStage, step.Descriptor.ForwardStage, json.RawMessage(intent), func(got environmentCreateJournalEntry) bool {
			if index == len(chain)-1 {
				return got.State == "pending" || p != nil && j.Accepted
			}
			next := *chain[index+1].Descriptor
			next.ForwardStage = d.ForwardStage
			return createNormalTailRuntimeResponseMatches(got, &next, winner, winnerJournal)
		}, nested)
		if r.err != nil {
			return 0, nil, nil, j, r.err
		}
		return r.n, plans, winner, winnerJournal, nil
	}
	count, plans, winner, j, err := consume(0, outer, entries)
	if err != nil {
		return count, plans, winner, j, err
	}
	effective := *chain[0].Descriptor
	effective.ForwardStage = d.ForwardStage
	if outer.State != "pending" && !createNormalTailRuntimeResponseMatches(outer, &effective, winner, j) {
		return bad()
	}
	return count, plans, winner, j, nil
}

// This one exact Secret correction is an ordinary child of the literal partial
// runtime workload. Its accepted owner remains visible to lease verification.
func validateCreateNormalTailNestedAccountRegistrationCRLs(store secretStore, e *environmentCreateExecution, previous *deploymentplan.Plan, journal deploymentplan.Journal, outer environmentCreateJournalEntry, following []environmentCreateJournalEntry, admission createNormalTailProgramAdmission, previousAt time.Time) (int, []*deploymentplan.Plan, error) {
	if len(following) == 0 || following[0].Key != "kubernetes/"+createNormalTailAccountRegistrationCRLStage {
		return 0, nil, nil
	}
	bad := func() (int, []*deploymentplan.Plan, error) {
		return 0, nil, accountRegistrationCRLBlock("nested ordinary child chronology or outcome")
	}
	source, child, actual, err := readCreateNormalTailAccountRegistrationCRLs(store, e, previous, journal)
	if err != nil {
		return 0, nil, err
	}
	if source == nil || actual.InFlight != "" || source.CapturedAt.Before(outer.StartedAt) || source.CapturedAt.Before(admission.AdmittedAt) || !source.CapturedAt.Before(admission.Deadline) || following[0].StartedAt.Before(source.CapturedAt) {
		return bad()
	}
	expected := child
	if expected == nil {
		expected, err = readCreateNormalTailPreparationPlan(store, e, createNormalTailAccountRegistrationCRLStage)
		if err != nil {
			return 0, nil, err
		}
	}
	if expected == nil {
		return bad()
	}
	intent, err := environmentCreateKubernetesStageIntent(expected)
	if err != nil {
		return 0, nil, err
	}
	r := productStageReader{store: store, e: e, entries: following, admission: admission, completedAt: previousAt}
	complete := r.take("kubernetes/"+createNormalTailAccountRegistrationCRLStage, createNormalTailAccountRegistrationCRLStage, json.RawMessage(intent), func(entry environmentCreateJournalEntry) bool {
		return entry.State == "pending" || child != nil && actual.Accepted
	})
	if r.err != nil {
		return 0, nil, r.err
	}
	if outer.State == "completed" && (!complete || following[0].CompletedAt.After(outer.CompletedAt)) || !complete && len(following) != 1 {
		return bad()
	}
	var children []*deploymentplan.Plan
	if child != nil {
		children = append(children, child)
	}
	return r.n, children, nil
}

// A second expiration can renew the same product ceremony while the literal
// runtime child stays pending. Each exact native step and Kubernetes child
// remains part of that parent's original admitted chronology.
func validateCreateNormalTailNestedSecondSessionRenewal(store secretStore, e *environmentCreateExecution, outer environmentCreateJournalEntry, following []environmentCreateJournalEntry, admission createNormalTailProgramAdmission, previousAt time.Time) (int, []*deploymentplan.Plan, error) {
	if len(following) == 0 || outer.Key != "kubernetes/"+createNormalTailRuntimeStage(3) {
		return 0, nil, nil
	}
	program, err := readCreateNormalTailProgram(store, e)
	if err != nil {
		return 0, nil, err
	}
	if program.Products == nil {
		return 0, nil, nil
	}
	first, err := readProductSessionRenewalFirst(store, e, program.Products)
	if err != nil {
		return 0, nil, err
	}
	if first == nil {
		return 0, nil, nil
	}
	sid, _, _ := productSessionRenewalIdentity(e.Journal.PlanSHA256, productIdentityScope(program.Products), first.Installed.Ceremony.BootstrapSessionID)
	if following[0].Key != "openbao-policy-rtk-"+e.Journal.Environment+"-service-bootstrap-"+sid {
		return 0, nil, nil
	}
	source, err := readProductSessionRenewalSourceForGeneration(store, e, program.Products, 2)
	if err != nil {
		return 0, nil, err
	}
	if source == nil {
		return 0, nil, nil
	}
	specs, err := productSessionRenewalProgressSpecs(store, e, program.Products, *source)
	if err != nil {
		return 0, nil, err
	}
	if len(specs) == 0 || following[0].Key != specs[0].Key {
		return 0, nil, nil
	}
	bad := func() (int, []*deploymentplan.Plan, error) {
		return 0, nil, productSessionRenewalBlock("nested second renewal chronology")
	}
	if source.Generation != 2 || outer.Key != "kubernetes/"+createNormalTailRuntimeStage(source.RuntimeSequence) || source.QualifiedAt.Before(outer.StartedAt) || following[0].StartedAt.Before(source.QualifiedAt) {
		return bad()
	}
	r := productStageReader{store: store, e: e, l: program.Products, entries: following, admission: admission, completedAt: previousAt}
	for _, spec := range specs {
		if r.n == len(r.entries) {
			break
		}
		if spec.Suffix != "" {
			if spec.Definitions == nil {
				return bad()
			}
			prefix := "product-identities-" + productIdentityScope(program.Products)[:12] + "-"
			if !r.kube(strings.TrimPrefix(spec.Phase, prefix), spec.Definitions, nil) {
				break
			}
		} else if !r.take(spec.Key, spec.Phase, spec.Intent, func(entry environmentCreateJournalEntry) bool {
			return entry.State == "pending" || deploymentplan.Digest(jsonRawObject(entry.Receipt)) == deploymentplan.Digest(spec.Intent)
		}) {
			break
		}
	}
	if r.err != nil {
		return 0, nil, r.err
	}
	if r.n < len(following) || outer.State == "completed" {
		renewal, err := readProductSessionRenewalAt(store, e, program.Products, 2)
		if err != nil {
			return 0, nil, err
		}
		if renewal == nil || deploymentplan.Digest(renewal.Source) != deploymentplan.Digest(source) || r.n == 0 || r.entries[r.n-1].State != "completed" || outer.State == "completed" && r.entries[r.n-1].CompletedAt.After(outer.CompletedAt) {
			return bad()
		}
	}
	return r.n, r.children, nil
}
