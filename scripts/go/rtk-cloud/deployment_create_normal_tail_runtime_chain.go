package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type createNormalTailRuntimeStep struct {
	Descriptor      *createNormalTailRuntimeForward
	Source          *createNormalTailRuntimeSource
	PreviousPlan    *deploymentplan.Plan
	PreviousJournal deploymentplan.Journal
	Child           *deploymentplan.Plan
	Journal         deploymentplan.Journal
}

func createNormalTailRuntimeSequence(d *createNormalTailRuntimeForward) int {
	if d.Sequence == 0 {
		return 2
	}
	return d.Sequence
}
func createNormalTailRuntimeStage(sequence int) string {
	return fmt.Sprintf("workload-forward-%04d", sequence)
}
func createNormalTailRuntimeDescriptorPath(e *environmentCreateExecution, sequence int) string {
	return filepath.Join(e.Directory, fmt.Sprintf("normal-tail-workload-forward-%04d.json", sequence))
}
func createNormalTailRuntimeSequenceSourcePath(e *environmentCreateExecution, sequence int) string {
	if sequence == 2 {
		return createNormalTailRuntimeSourcePath(e)
	}
	return filepath.Join(e.Directory, fmt.Sprintf("normal-tail-runtime-configuration-source-%04d.json", sequence))
}

func createNormalTailRuntimePartial(p *deploymentplan.Plan, j deploymentplan.Journal) bool {
	if p == nil || j.PlanSHA256 != p.SHA256() || j.Accepted || j.AcceptanceStarted || j.InFlight != "" || len(j.Completed) >= len(p.Resources) || len(j.Owners) < len(j.Completed) || len(j.Owners) > len(j.Completed)+1 {
		return false
	}
	known := map[string]bool{}
	for i, id := range j.Completed {
		if id != p.Resources[i].ID || known[id] || !environmentBootstrapOwnerUUID.MatchString(j.Owners[id]) {
			return false
		}
		known[id] = true
	}
	next := p.Resources[len(j.Completed)].ID
	for id, uid := range j.Owners {
		if !environmentBootstrapOwnerUUID.MatchString(uid) || !known[id] && id != next {
			return false
		}
	}
	return true
}

// Read the literal standard child even when its logical parent later carries a
// descendant response. Native acceptance and owner evidence are never replaced.
func readCreateNormalTailRuntimeLiteral(store secretStore, e *environmentCreateExecution, d *createNormalTailRuntimeForward) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	bad := func() (*deploymentplan.Plan, deploymentplan.Journal, error) {
		return nil, deploymentplan.Journal{}, createNormalTailRuntimeBlock("literal numbered child")
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+d.ForwardStage)
	j, exists, err := d.Target.Plan.ReadExecution(store.Root)
	if err != nil {
		return bad()
	}
	if entry == nil {
		if exists {
			return bad()
		}
		for _, lease := range e.Journal.KubernetesLeases {
			if strings.HasPrefix(lease.Holder, d.Target.Plan.SHA256()+":") {
				return bad()
			}
		}
		return nil, j, nil
	}
	intent, err := environmentCreateKubernetesStageIntent(d.Target.Plan)
	digest, hashErr := environmentCreatePlanSHA256(intent)
	if err != nil || hashErr != nil || entry.IntentSHA256 != digest || entry.Phase != d.ForwardStage || entry.StartedAt.IsZero() || entry.StartedAt.After(time.Now().UTC()) || entry.State != "pending" && entry.State != "allocated" && entry.State != "completed" || entry.State == "pending" && (len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero()) || entry.State == "allocated" && (len(entry.Receipt) == 0 || !entry.CompletedAt.IsZero()) || entry.State == "completed" && (len(entry.Receipt) == 0 || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(time.Now().UTC())) {
		return bad()
	}
	if !exists {
		if entry.State != "pending" {
			return bad()
		}
		return nil, j, nil
	}
	p, raw, err := originalCreateChildAtIntent(store, e, *entry)
	if err != nil || p == nil || p.SHA256() != d.Target.Plan.SHA256() || deploymentplan.Digest(raw) != deploymentplan.Digest(j) {
		return bad()
	}
	return p, j, nil
}

func readCreateNormalTailRuntimeChain(store secretStore, e *environmentCreateExecution, limits ...int) ([]createNormalTailRuntimeStep, error) {
	bad := func(stage string) ([]createNormalTailRuntimeStep, error) {
		return nil, createNormalTailRuntimeBlock(stage)
	}
	if e == nil || e.Store != store || validateEnvironmentPreflightDirectories(store, e.Directory) != nil {
		return bad("numbered owner")
	}
	entries, err := os.ReadDir(e.Directory)
	if err != nil {
		return nil, err
	}
	maximum, maxSource := 1, 2
	for _, entry := range entries {
		for _, prefix := range []string{"normal-tail-workload-forward-", "normal-tail-runtime-configuration-source-"} {
			name := entry.Name()
			if !strings.HasPrefix(name, prefix) || !strings.HasSuffix(name, ".json") {
				continue
			}
			number := strings.TrimSuffix(strings.TrimPrefix(name, prefix), ".json")
			sequence, err := strconv.Atoi(number)
			if err != nil || len(number) != 4 || sequence < 2 || sequence > 9999 || entry.IsDir() {
				return bad("numbered artifact name")
			}
			if prefix == "normal-tail-workload-forward-" {
				if sequence > maximum {
					maximum = sequence
				}
			} else {
				if sequence < 3 {
					return bad("numbered source name")
				}
				if sequence > maxSource {
					maxSource = sequence
				}
			}
		}
	}
	if maxSource > maximum+1 {
		return bad("numbered source gap")
	}
	for _, entry := range e.Journal.Entries {
		if strings.HasPrefix(entry.Key, "kubernetes/workload-forward-") {
			number := strings.TrimPrefix(entry.Key, "kubernetes/workload-forward-")
			sequence, err := strconv.Atoi(number)
			if err != nil || len(number) != 4 || sequence < 2 || sequence > maximum {
				return bad("missing numbered descriptor")
			}
		}
	}
	if maximum == 1 {
		return nil, nil
	}
	// Historical renewal source verification authenticates its pinned prefix
	// without recursively selecting the later target that embeds that renewal.
	// The directory/journal name and gap checks above still inspect all files.
	if len(limits) > 1 {
		return bad("historical numbered bound")
	}
	if len(limits) == 1 {
		if limits[0] < 2 || limits[0] > maximum {
			return bad("historical numbered bound")
		}
		maximum = limits[0]
	}

	program, err := readCreateNormalTailProgram(store, e)
	if err != nil {
		return nil, err
	}
	previous, previousJournal, err := readCreateNormalTailRuntimePrevious(store, e)
	if err != nil {
		return nil, err
	}
	originalSource, err := readCreateNormalTailRuntimeSource(store, e)
	if err != nil || originalSource == nil {
		return bad("numbered original source")
	}
	admission, err := readCreateNormalTailProgramAdmission(store, e)
	if err != nil {
		return nil, err
	}
	priorPlan, priorJournal, priorDescriptor, priorStage := previous.Target.Plan, previousJournal, deploymentplan.Digest(previous), previous.ForwardStage
	var chain []createNormalTailRuntimeStep
	for sequence := 2; sequence <= maximum; sequence++ {
		var d createNormalTailRuntimeForward
		if readEnvironmentPreflightJSON(createNormalTailRuntimeDescriptorPath(e, sequence), &d) != nil {
			return bad("numbered descriptor gap or encoding")
		}
		source := originalSource
		if sequence > 2 {
			var saved createNormalTailRuntimeSource
			if readEnvironmentPreflightJSON(createNormalTailRuntimeSequenceSourcePath(e, sequence), &saved) != nil {
				return bad("numbered source encoding")
			}
			source = &saved
			prior := chain[len(chain)-1]
			expected, err := createNormalTailRuntimeEffectiveSnapshot(*prior.Source, prior.Descriptor.Inputs)
			entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+priorStage)
			if err != nil || !createNormalTailRuntimePartial(priorPlan, priorJournal) || entry == nil || source.Sequence != sequence || source.Profile == "" || source.Schema != createNormalTailRuntimeSourceSchema || source.ParentSHA256 != e.Journal.PlanSHA256 || source.OwnerSHA256 != deploymentplan.Digest(e.owner) || source.ProgramSHA256 != deploymentplan.Digest(program) || source.PreviousDescriptorSHA256 != priorDescriptor || source.PreviousPlanSHA256 != priorPlan.SHA256() || source.PreviousJournalSHA256 != deploymentplan.Digest(priorJournal) || source.PreviousIntentSHA256 != entry.IntentSHA256 || deploymentplan.Digest(source.Configuration) != deploymentplan.Digest(prior.Source.Configuration) || deploymentplan.Digest(source.Release) != deploymentplan.Digest(prior.Source.Release) || deploymentplan.Digest(source.Snapshot) != deploymentplan.Digest(expected) || source.Snapshot.Digest(source.Configuration) != priorPlan.ConfigurationSHA256 || source.CapturedAt.Before(entry.StartedAt) || source.CapturedAt.Before(prior.Source.CapturedAt) {
				return bad("numbered exact source")
			}
		}
		if source.CapturedAt.IsZero() || source.CapturedAt.Before(admission.AdmittedAt) || !source.CapturedAt.Before(admission.Deadline) || source.CapturedAt.After(time.Now().UTC()) || d.Schema != createNormalTailRuntimeForwardSchema || d.ParentSHA256 != e.Journal.PlanSHA256 || d.OwnerSHA256 != deploymentplan.Digest(e.owner) || d.ProgramSHA256 != deploymentplan.Digest(program) || d.SourceSHA256 != deploymentplan.Digest(source) || d.PreviousDescriptorSHA256 != priorDescriptor || d.PreviousPlanSHA256 != priorPlan.SHA256() || d.PreviousJournalSHA256 != deploymentplan.Digest(priorJournal) || d.ForwardStage != createNormalTailRuntimeStage(sequence) || createNormalTailRuntimeSequence(&d) != sequence || sequence > 2 && d.Profile != source.Profile || d.Profile != "" && d.Profile != environmentBillingRuntimeLegacyProfile && d.Profile != environmentBillingRuntimeKeyProfile && d.Profile != createNormalTailAccountRegistrationCRLProfile || d.Target.Plan == nil || d.Target.Schema != previous.Target.Schema || d.Target.ParentSHA256 != previous.Target.ParentSHA256 || d.Target.OwnerSHA256 != previous.Target.OwnerSHA256 || deploymentplan.Digest(d.Target.Pins) != deploymentplan.Digest(previous.Target.Pins) || deploymentplan.Digest(d.Target.Release) != deploymentplan.Digest(previous.Target.Release) || deploymentplan.Digest(d.Target.Bindings) != deploymentplan.Digest(previous.Target.Bindings) {
			return bad("numbered descriptor lineage")
		}
		if d.Profile == createNormalTailAccountRegistrationCRLProfile {
			proof, child, cj, er := readCreateNormalTailAccountRegistrationCRLs(store, e, priorPlan, priorJournal)
			if er != nil {
				return nil, er
			}
			if proof == nil || child == nil || !cj.Accepted || deploymentplan.Digest(proof) != deploymentplan.Digest(source.RegistrationCRLs) || d.RegistrationCRLSHA256 != deploymentplan.Digest(proof) {
				return bad("accepted registration CRL child")
			}
		} else if source.RegistrationCRLs != nil || d.RegistrationCRLSHA256 != "" {
			return bad("unexpected registration CRL source")
		}
		if source.SessionRenewal != nil {
			r, deps, er := readProductSessionRenewalWithDependencies(store, e, program.Products, &source.Configuration)
			if er != nil {
				return nil, er
			}
			if r == nil || deploymentplan.Digest(r) != deploymentplan.Digest(source.SessionRenewal) || d.SessionRenewalSHA256 != deploymentplan.Digest(r) {
				return bad("numbered completed session renewal")
			}
			if deploymentplan.Digest(deps) != deploymentplan.Digest(source.SessionRenewalResources) {
				return bad("numbered accepted renewal dependencies")
			}
		} else if len(source.SessionRenewalResources) != 0 || d.SessionRenewalSHA256 != "" {
			return bad("orphan numbered session renewal")
		}
		if err := validateCreateNormalTailRuntimeForward(store, *source, d.Inputs, priorPlan, priorJournal, d.Target.Plan, d.Profile); err != nil {
			return nil, err
		}
		child, j, err := readCreateNormalTailRuntimeLiteral(store, e, &d)
		if err != nil {
			return nil, err
		}
		if entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+d.ForwardStage); entry != nil && (entry.StartedAt.Before(source.CapturedAt) || !entry.StartedAt.Before(admission.Deadline) || entry.State == "completed" && !entry.CompletedAt.Before(admission.Deadline)) {
			return bad("numbered child admission")
		}
		if sequence < maximum && (child == nil || !createNormalTailRuntimePartial(child, j)) {
			return bad("numbered previous native response")
		}
		step := createNormalTailRuntimeStep{Descriptor: &d, Source: source, PreviousPlan: priorPlan, PreviousJournal: priorJournal, Child: child, Journal: j}
		chain = append(chain, step)
		priorPlan, priorJournal, priorDescriptor, priorStage = d.Target.Plan, j, deploymentplan.Digest(&d), d.ForwardStage
	}
	return chain, nil
}

func readCreateNormalTailRuntimeForwardSource(store secretStore, e *environmentCreateExecution, d *createNormalTailRuntimeForward) (*createNormalTailRuntimeSource, error) {
	chain, err := readCreateNormalTailRuntimeChain(store, e)
	if err != nil {
		return nil, err
	}
	for _, step := range chain {
		if deploymentplan.Digest(step.Descriptor) == deploymentplan.Digest(d) {
			return step.Source, nil
		}
	}
	return nil, createNormalTailRuntimeBlock("selected numbered source")
}

func captureCreateNormalTailRuntimeNextSource(ctx context.Context, store secretStore, e *environmentCreateExecution, chain []createNormalTailRuntimeStep, profile string) (*createNormalTailRuntimeSource, error) {
	previous := chain[len(chain)-1]
	sequence := createNormalTailRuntimeSequence(previous.Descriptor) + 1
	if sequence > 9999 || previous.Child == nil || !createNormalTailRuntimePartial(previous.Child, previous.Journal) {
		return nil, createNormalTailRuntimeBlock("known numbered partial source")
	}
	snapshot, err := createNormalTailRuntimeEffectiveSnapshot(*previous.Source, previous.Descriptor.Inputs)
	if err != nil {
		return nil, err
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+previous.Descriptor.ForwardStage)
	source := &createNormalTailRuntimeSource{Schema: createNormalTailRuntimeSourceSchema, ParentSHA256: e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(e.owner), ProgramSHA256: previous.Source.ProgramSHA256, PreviousDescriptorSHA256: deploymentplan.Digest(previous.Descriptor), PreviousPlanSHA256: previous.Child.SHA256(), PreviousJournalSHA256: deploymentplan.Digest(previous.Journal), PreviousIntentSHA256: entry.IntentSHA256, Sequence: sequence, Profile: profile, Configuration: previous.Source.Configuration, Release: previous.Source.Release, Snapshot: snapshot, CapturedAt: time.Now().UTC()}
	if profile == createNormalTailAccountRegistrationCRLProfile {
		proof, child, j, err := readCreateNormalTailAccountRegistrationCRLs(store, e, previous.Child, previous.Journal)
		if err != nil {
			return nil, err
		}
		if proof == nil || child == nil || !j.Accepted {
			return nil, accountRegistrationCRLBlock("accepted early child before numbered publication")
		}
		source.RegistrationCRLs = proof
	}
	program, err := readCreateNormalTailProgram(store, e)
	if err != nil {
		return nil, err
	}
	if program.Products != nil {
		r, er := readProductSessionRenewal(store, e, program.Products)
		if er != nil {
			return nil, er
		}
		if r != nil && r.Source.OriginalForwardPlanSHA256 == previous.Child.SHA256() && r.Source.OriginalForwardJournalSHA256 == deploymentplan.Digest(previous.Journal) {
			source.SessionRenewal = r
			source.SessionRenewalResources, er = productSessionRenewalDependencyResources(source.Configuration, store, e, program.Products, r)
			if er != nil {
				return nil, er
			}
		}
	}
	path := createNormalTailRuntimeSequenceSourcePath(e, sequence)
	if _, err := os.Lstat(path); err == nil {
		var saved createNormalTailRuntimeSource
		if readEnvironmentPreflightJSON(path, &saved) != nil {
			return nil, createNormalTailRuntimeBlock("pending numbered source encoding")
		}
		source.CapturedAt = saved.CapturedAt
		if saved.CapturedAt.Before(previous.Source.CapturedAt) || saved.CapturedAt.After(time.Now().UTC()) || deploymentplan.Digest(&saved) != deploymentplan.Digest(source) {
			return nil, createNormalTailRuntimeBlock("pending numbered source changed")
		}
		return &saved, nil
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := e.Check(ctx); err != nil {
		return nil, err
	}
	rel, err := filepath.Rel(store.Root, path)
	if err != nil {
		return nil, err
	}
	if err := store.write(rel, mustProviderRecoveryJSON(source), false); err != nil {
		return nil, err
	}
	return source, nil
}
