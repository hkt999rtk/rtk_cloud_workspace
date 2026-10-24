package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"time"
)

const createNormalTailWorkloadForwardSchema = "rtk-cloud.create-normal-tail-workload-forward/v1"
const createNormalTailWorkloadForwardStage = "workload-forward"

type createNormalTailWorkloadForward struct {
	Schema, ParentSHA256, OwnerSHA256, ProgramSHA256        string
	OriginalPlanSHA256, OriginalJournalSHA256, ForwardStage string
	Target                                                  createNormalTailTarget
}

type createNormalTailWorkloadForwardReceipt struct {
	OriginalPlanSHA256    string `json:"original_plan_sha256"`
	OriginalJournalSHA256 string `json:"original_journal_sha256"`
	ForwardStage          string `json:"forward_stage"`
	ForwardPlanSHA256     string `json:"forward_plan_sha256"`
	ForwardJournalSHA256  string `json:"forward_journal_sha256"`
}

func createNormalTailWorkloadForwardPath(e *environmentCreateExecution) string {
	return filepath.Join(e.Directory, "normal-tail-workload-forward.json")
}

// A forward target is an exact correction of the frozen desired graph. Every
// recorded write becomes an original-owner retain; unknown writes are never
// reconstructed from a current inventory.
func validateCreateNormalTailWorkloadForwardPlan(original *deploymentplan.Plan, j deploymentplan.Journal, forward *deploymentplan.Plan) error {
	return validateCreateNormalTailWorkloadForwardProfile(original, j, forward, bindEnvironmentManagedMQTTConsumersCanonical)
}
func bindEnvironmentManagedMQTTConsumersCanonical(defs []deploymentplan.Definition) error {
	return bindEnvironmentManagedMQTTConsumers(defs)
}
func validateCreateNormalTailWorkloadForwardLegacyPlan(original *deploymentplan.Plan, j deploymentplan.Journal, forward *deploymentplan.Plan) error {
	return validateCreateNormalTailWorkloadForwardProfile(original, j, forward, bindEnvironmentManagedMQTTConsumersLegacy)
}
func validateCreateNormalTailWorkloadForwardProfile(original *deploymentplan.Plan, j deploymentplan.Journal, forward *deploymentplan.Plan, bind func([]deploymentplan.Definition) error) error {
	if original == nil || forward == nil || original.SHA256() != j.PlanSHA256 || j.Accepted || j.InFlight != "" || len(j.Completed) >= len(original.Resources) || deploymentplan.Digest(original.Inputs) != deploymentplan.Digest(forward.Inputs) || len(original.Resources) > len(forward.Resources) {
		return createProviderRecoveryBlock("forward workload differs from its original known interrupted graph")
	}
	definitions := make([]deploymentplan.Definition, 0, len(original.Resources))
	for _, r := range original.Resources {
		definitions = append(definitions, deploymentplan.Definition{Object: deploymentplan.Clone(r.Desired), Phase: r.Phase, Dependencies: append([]string(nil), r.Dependencies...)})
	}
	if err := bind(definitions); err != nil {
		return err
	}
	for i, old := range original.Resources {
		got := forward.Resources[i]
		if got.ID != old.ID || got.Phase != old.Phase || deploymentplan.Digest(got.Dependencies) != deploymentplan.Digest(old.Dependencies) || deploymentplan.Digest(got.Desired) != deploymentplan.Digest(definitions[i].Object) {
			return createProviderRecoveryBlock("forward workload changes an undeclared desired resource: " + old.ID)
		}
		if owner := j.Owners[old.ID]; owner != "" {
			if deploymentplan.Digest(old.Desired) != deploymentplan.Digest(definitions[i].Object) {
				return createProviderRecoveryBlock("forward correction changes an already recorded effect: " + old.ID)
			}
			if got.Action != "retain" || got.UID != owner || deploymentplan.Map(got.Baseline["metadata"])["uid"] != owner || !deploymentplan.Matches(old.Desired, got.Baseline) || deploymentplan.Digest(got.Baseline) != deploymentplan.Digest(deploymentplan.Projection(got.Baseline)) {
				return createProviderRecoveryBlock("forward workload loses a recorded original owner: " + old.ID)
			}
		} else if got.Action != old.Action && !(old.ID == "Deployment/"+original.Stack+"-video-cloud/pki-controller" && old.Action == "retain" && got.Action == "update" && deploymentplan.Digest(old.Desired) != deploymentplan.Digest(definitions[i].Object)) || got.UID != old.UID || deploymentplan.Digest(got.Baseline) != deploymentplan.Digest(old.Baseline) {
			return createProviderRecoveryBlock("forward workload adopts or changes an unrecorded effect: " + old.ID)
		}
	}

	if len(forward.Resources) > len(original.Resources) {
		defs, err := createNormalTailPostgresAccessDefinitions(original.Stack, original)
		if err != nil {
			return err
		}
		expected := map[string]deploymentplan.Definition{}
		for _, d := range defs {
			expected[deploymentplan.ID(d.Object)] = d
		}
		if len(forward.Resources)-len(original.Resources) != len(expected) {
			return createProviderRecoveryBlock("forward workload runtime policy set is incomplete")
		}
		for _, r := range forward.Resources[len(original.Resources):] {
			d, ok := expected[r.ID]
			if !ok || r.Action != "retain" || !environmentBootstrapOwnerUUID.MatchString(r.UID) || r.Phase != d.Phase || len(r.Dependencies) != 0 || deploymentplan.Digest(r.Desired) != deploymentplan.Digest(d.Object) || deploymentplan.Map(r.Baseline["metadata"])["uid"] != r.UID || !deploymentplan.Matches(r.Desired, r.Baseline) || deploymentplan.Digest(r.Baseline) != deploymentplan.Digest(deploymentplan.Projection(r.Baseline)) {
				return createProviderRecoveryBlock("forward workload has an undeclared runtime policy retain")
			}
			delete(expected, r.ID)
		}
	}
	return nil
}

func buildCreateNormalTailWorkloadForward(original *deploymentplan.Plan, j deploymentplan.Journal, policies ...deploymentplan.Resource) (*deploymentplan.Plan, error) {
	return buildCreateNormalTailWorkloadForwardWithInventory(original, j, nil, policies...)
}
func buildCreateNormalTailWorkloadForwardWithInventory(original *deploymentplan.Plan, j deploymentplan.Journal, observed map[string]deploymentplan.Object, policies ...deploymentplan.Resource) (*deploymentplan.Plan, error) {
	raw, err := json.Marshal(original)
	if err != nil {
		return nil, err
	}
	var next deploymentplan.Plan
	if json.Unmarshal(raw, &next) != nil {
		return nil, createProviderRecoveryBlock("original workload cannot be cloned")
	}
	defs := make([]deploymentplan.Definition, len(next.Resources))
	for i, r := range next.Resources {
		defs[i] = deploymentplan.Definition{Object: r.Desired, Phase: r.Phase}
	}
	if err := bindEnvironmentManagedMQTTConsumers(defs); err != nil {
		return nil, err
	}
	changed := false
	for i, old := range original.Resources {
		r := &next.Resources[i]
		if deploymentplan.Digest(old.Desired) != deploymentplan.Digest(r.Desired) {
			changed = true
			if r.ID == "Deployment/"+original.Stack+"-video-cloud/pki-controller" && r.Action == "retain" {
				r.Action = "update"
			}
			if j.Owners[r.ID] != "" {
				return nil, createProviderRecoveryBlock("MQTT correction would change an already recorded write: " + r.ID)
			}
		}
		if owner := j.Owners[r.ID]; owner != "" {
			r.Action = "retain"
			r.UID = owner
			r.Baseline = deploymentplan.Projection(old.Desired)
			deploymentplan.Map(r.Baseline["metadata"])["uid"] = owner
			if observed != nil {
				current := observed[r.ID]
				if current == nil || deploymentplan.Map(current["metadata"])["uid"] != owner || !deploymentplan.Matches(old.Desired, current) {
					return nil, createProviderRecoveryBlock("recorded original owner or desired body changed before forward: " + r.ID)
				}
				r.Baseline = deploymentplan.Projection(current)
			}
		}
	}
	if !changed {
		return nil, nil
	}
	next.Resources = append(next.Resources, policies...)
	if err := validateCreateNormalTailWorkloadForwardPlan(original, j, &next); err != nil {
		return nil, err
	}
	return &next, nil
}

// Publish one immutable corrected desired graph before its ordinary child can
// write. Native observations still belong to the original conditional fences.
func prepareCreateNormalTailWorkloadForward(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, c *compiledEnvironmentDeployment) (*compiledEnvironmentDeployment, error) {
	return prepareCreateNormalTailWorkloadForwardWithInventory(ctx, cfg, h, c, c.Backend.Inventory)
}
func prepareCreateNormalTailWorkloadForwardWithInventory(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, c *compiledEnvironmentDeployment, inventory func(context.Context, string) ([]deploymentplan.Object, error)) (*compiledEnvironmentDeployment, error) {
	store, e := h.Execution.Store, h.Execution
	original, err := readCreateNormalTailFrozenTarget(store, e)
	if err != nil {
		return nil, err
	}
	source, j, err := readCreateNormalTailWorkloadOriginal(store, e, original)
	if err != nil {
		return nil, err
	}
	if source == nil || j.Accepted {
		return c, nil
	}
	descriptor, err := readCreateNormalTailWorkloadForwardDescriptor(store, e, original, j)
	if err != nil {
		return nil, err
	}
	if descriptor == nil {

		access, accessJournal, err := createNormalTailPostgresAccessEvidence(store, e)
		if err != nil {
			return nil, err
		}
		if access == nil || !accessJournal.Accepted {
			return nil, createProviderRecoveryBlock("forward target requires accepted runtime dependency access")
		}

		live, err := inventory(ctx, original.Stack)
		if err != nil {
			return nil, err
		}
		observed := map[string]deploymentplan.Object{}
		for _, o := range live {
			id := deploymentplan.ID(o)
			if observed[id] != nil {
				return nil, createProviderRecoveryBlock("forward source inventory has duplicate owners")
			}
			observed[id] = o
		}
		needed := map[string]bool{}
		for id := range j.Owners {
			needed[id] = true
		}
		for _, r := range access.Resources {
			needed[r.ID] = true
		}
		for id := range needed {
			if observed[id] == nil {
				observed[id], err = c.Backend.Read(ctx, id)
				if err != nil {
					return nil, err
				}
			}
		}
		policies := make([]deploymentplan.Resource, 0, len(access.Resources))
		for _, r := range access.Resources {
			r.Action = "retain"
			r.UID = accessJournal.Owners[r.ID]
			current := observed[r.ID]
			if current == nil || deploymentplan.Map(current["metadata"])["uid"] != r.UID || !deploymentplan.Matches(r.Desired, current) {
				return nil, createProviderRecoveryBlock("accepted runtime access changed before forward: " + r.ID)
			}
			r.Baseline = deploymentplan.Projection(current)
			policies = append(policies, r)
		}
		next, err := buildCreateNormalTailWorkloadForwardWithInventory(original, j, observed, policies...)
		if err != nil {
			return nil, err
		}
		if next == nil {
			return c, nil
		}
		var target createNormalTailTarget
		if readEnvironmentPreflightJSON(filepath.Join(e.Directory, "normal-tail-target.json"), &target) != nil || target.Plan == nil || target.Plan.SHA256() != original.SHA256() {
			return nil, createProviderRecoveryBlock("forward workload lost its original target wrapper")
		}
		target.Plan = next
		program, err := readCreateNormalTailProgram(store, e)
		if err != nil {
			return nil, err
		}
		descriptor = &createNormalTailWorkloadForward{Schema: createNormalTailWorkloadForwardSchema, ParentSHA256: e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(e.owner), ProgramSHA256: deploymentplan.Digest(program), OriginalPlanSHA256: original.SHA256(), OriginalJournalSHA256: deploymentplan.Digest(j), ForwardStage: createNormalTailWorkloadForwardStage, Target: target}
		if err := e.Check(ctx); err != nil {
			return nil, err
		}
		relative, err := filepath.Rel(store.Root, createNormalTailWorkloadForwardPath(e))
		if err != nil {
			return nil, err
		}
		if err := store.write(relative, mustProviderRecoveryJSON(descriptor), false); err != nil {
			return nil, err
		}
	}
	if err := validateCreateNormalTailWorkloadForwardPlan(original, j, descriptor.Target.Plan); err != nil {
		descriptor, err = refreshCreateNormalTailWorkloadForward(ctx, store, e, original, j, descriptor)
		if err != nil {
			return nil, err
		}
	}
	next := *c
	next.Plan = descriptor.Target.Plan
	next.Release = descriptor.Target.Release
	next.Bindings = descriptor.Target.Bindings
	selected, err := prepareCreateNormalTailProductSessionForward(ctx, cfg, h, &next, descriptor, original, j)
	if err != nil {
		return nil, err
	}
	return prepareCreateNormalTailRuntimeForwardWithInventory(ctx, cfg, h, selected, inventory)
}

func selectCreateNormalTailWorkloadTarget(store secretStore, e *environmentCreateExecution, original *deploymentplan.Plan) (*deploymentplan.Plan, error) {
	if d, err := readCreateNormalTailRuntimeForward(store, e); err != nil {
		return nil, err
	} else if d != nil {
		return d.Target.Plan, nil
	}
	if _, err := os.Lstat(createNormalTailWorkloadForwardPath(e)); os.IsNotExist(err) {
		return original, nil
	} else if err != nil {
		return nil, err
	}
	_, j, err := readCreateNormalTailWorkloadOriginal(store, e, original)
	if err != nil {
		return nil, err
	}
	d, err := readCreateNormalTailWorkloadForwardDescriptor(store, e, original, j)
	if err != nil {
		return nil, err
	}
	if d == nil {
		return original, nil
	}
	successor, err := readCreateNormalTailProductSessionForward(store, e, original, j, d)
	if err != nil {
		return nil, err
	}
	if successor != nil {
		return successor.Target.Plan, nil
	}
	return d.Target.Plan, nil
}

func executeCreateNormalTailForwardWorkload(ctx context.Context, cfg deploymentConfig, store secretStore, h *createNormalTailHandoff, c *compiledEnvironmentDeployment, qualify, fence, accept func(context.Context, *deploymentplan.Plan) error, out io.Writer) error {
	if err := checkEnvironmentManagedMQTTConsumers(ctx, c.Plan, c.Backend.Read); err != nil {
		return err
	}
	return executeCreateNormalTailForwardWorkloadBackend(ctx, cfg, store, h, c, createNormalTailWorkloadBackend(cfg, store, h, c), qualify, fence, accept, out, func(ctx context.Context) error {
		return captureEnvironmentCreateVolumeOwners(ctx, cfg, c.Backend, h.Execution)
	}, c.FenceInventory)
}

func executeCreateNormalTailForwardWorkloadBackend(ctx context.Context, cfg deploymentConfig, store secretStore, h *createNormalTailHandoff, c *compiledEnvironmentDeployment, backend deploymentplan.Backend, qualify, fence, accept func(context.Context, *deploymentplan.Plan) error, out io.Writer, capture func(context.Context) error, phaseFences ...deploymentplan.PhaseFence) error {
	if runtime, err := readCreateNormalTailRuntimeForward(store, h.Execution); err != nil {
		return err
	} else if runtime != nil {
		return executeCreateNormalTailRuntimeForwardBackend(ctx, cfg, store, h, c, runtime, backend, qualify, fence, accept, out, capture, phaseFences...)
	}
	original, err := readCreateNormalTailFrozenTarget(store, h.Execution)
	if err != nil {
		return err
	}
	if c.Plan.SHA256() == original.SHA256() {
		return executeCreateNormalTailStageBackendWithPhaseFence(ctx, store, h, "workload", c.Plan, backend, qualify, fence, accept, out, capture, phaseFences...)
	}
	_, j, err := readCreateNormalTailWorkloadOriginal(store, h.Execution, original)
	if err != nil {
		return err
	}
	d, err := readCreateNormalTailWorkloadForwardDescriptor(store, h.Execution, original, j)
	if err != nil {
		return err
	}
	successor, err := readCreateNormalTailProductSessionForward(store, h.Execution, original, j, d)
	if err != nil {
		return err
	}
	if successor != nil {
		return executeCreateNormalTailProductSessionForwardBackend(ctx, cfg, store, h, c, successor, backend, qualify, fence, accept, out, capture, phaseFences...)
	}
	if d == nil || d.Target.Plan.SHA256() != c.Plan.SHA256() {
		return createProviderRecoveryBlock("selected forward workload has no original correction descriptor")
	}
	if err := validateCreateNormalTailWorkloadForwardPlan(original, j, c.Plan); err != nil {
		return err
	}
	if err := executeCreateNormalTailStageBackendWithPhaseFence(ctx, store, h, d.ForwardStage, c.Plan, backend, qualify, fence, accept, out, capture, phaseFences...); err != nil {
		return err
	}
	winner, exists, err := c.Plan.ReadExecution(store.Root)
	if err != nil || !exists || !winner.Accepted {
		return createProviderRecoveryBlock("forward workload has no genuinely accepted child")
	}
	receipt := createNormalTailWorkloadForwardReceipt{OriginalPlanSHA256: original.SHA256(), OriginalJournalSHA256: deploymentplan.Digest(j), ForwardStage: d.ForwardStage, ForwardPlanSHA256: c.Plan.SHA256(), ForwardJournalSHA256: deploymentplan.Digest(winner)}
	raw, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	entry := createOpenBaoACKCompletionEntry(h.Execution.Journal, "kubernetes/workload")
	if entry == nil {
		return createProviderRecoveryBlock("original workload intent disappeared")
	}
	if entry.State == "completed" {
		if activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(jsonRawObject(raw)) {
			return createProviderRecoveryBlock("original workload forward composite changed")
		}
		return h.Execution.Check(ctx)
	}
	if err := h.Execution.RecordReceipt("kubernetes/workload", raw); err != nil {
		return err
	}
	if err := h.Execution.Check(ctx); err != nil {
		return err
	}
	return h.Execution.CompleteEntry("kubernetes/workload", time.Now().UTC())
}

// Only an input that has never acquired a native execution may be refreshed.
// All recorded requests, saved child artifacts and native lease evidence fence it.
func createNormalTailWorkloadForwardUnstarted(store secretStore, e *environmentCreateExecution, d *createNormalTailWorkloadForward) error {
	bad := func() error {
		return createProviderRecoveryBlock("workload forward input already has execution evidence and cannot be refreshed")
	}
	if e == nil || e.Store != store || d == nil || d.Target.Plan == nil || d.ForwardStage != createNormalTailWorkloadForwardStage {
		return bad()
	}
	if createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+d.ForwardStage) != nil {
		return bad()
	}
	for _, path := range []string{filepath.Join(store.Root, "deployment", "rollback", d.Target.Plan.SHA256()), createNormalTailStagePlanPath(e, d.ForwardStage)} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			return bad()
		}
	}
	for _, lease := range e.Journal.KubernetesLeases {
		if strings.HasPrefix(lease.Holder, d.Target.Plan.SHA256()+":") {
			return bad()
		}
	}
	return nil
}

func refreshCreateNormalTailWorkloadForward(ctx context.Context, store secretStore, e *environmentCreateExecution, original *deploymentplan.Plan, j deploymentplan.Journal, d *createNormalTailWorkloadForward) (*createNormalTailWorkloadForward, error) {
	if err := validateCreateNormalTailWorkloadForwardLegacyPlan(original, j, d.Target.Plan); err != nil {
		return nil, err
	}
	if err := createNormalTailWorkloadForwardUnstarted(store, e, d); err != nil {
		return nil, err
	}
	observed := map[string]deploymentplan.Object{}
	for _, r := range d.Target.Plan.Resources[:len(original.Resources)] {
		if j.Owners[r.ID] != "" {
			observed[r.ID] = r.Baseline
		}
	}
	p, err := buildCreateNormalTailWorkloadForwardWithInventory(original, j, observed, d.Target.Plan.Resources[len(original.Resources):]...)
	if err != nil {
		return nil, err
	}
	if p == nil || p.SHA256() == d.Target.Plan.SHA256() {
		return nil, createProviderRecoveryBlock("rejected workload forward input has no exact source-derived correction")
	}
	next := *d
	next.Target.Plan = p
	if err := createNormalTailWorkloadForwardUnstarted(store, e, &next); err != nil {
		return nil, err
	}
	if err := e.Check(ctx); err != nil {
		return nil, err
	}
	path := createNormalTailWorkloadForwardPath(e)
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var saved createNormalTailWorkloadForward
	if decodeEnvironmentJSON(raw, &saved) != nil || deploymentplan.Digest(saved) != deploymentplan.Digest(d) {
		return nil, createProviderRecoveryBlock("rejected workload forward input changed before refresh")
	}
	archive := filepath.Join(e.Directory, "rejected", environmentPreflightBytesSHA256(raw)+".json")
	if err := ensurePrivateDirectory(filepath.Dir(archive)); err != nil {
		return nil, err
	}
	// Link a complete fsynced private temporary file exclusively. A process
	// dying during the write leaves no partial archive at its authoritative name.
	f, err := os.CreateTemp(filepath.Dir(archive), ".rejected-workload-*")
	if err != nil {
		return nil, err
	}
	temporary := f.Name()
	defer os.Remove(temporary)
	writeErr := f.Chmod(0o600)
	if writeErr == nil {
		_, writeErr = f.Write(raw)
	}
	if writeErr == nil {
		writeErr = f.Sync()
	}
	closeErr := f.Close()
	if writeErr != nil {
		return nil, writeErr
	}
	if closeErr != nil {
		return nil, closeErr
	}
	if err := os.Link(temporary, archive); os.IsExist(err) {
		info, statErr := os.Lstat(archive)
		prior, readErr := os.ReadFile(archive)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 || readErr != nil || !bytes.Equal(prior, raw) {
			return nil, createProviderRecoveryBlock("rejected workload input archive changed")
		}
	} else if err != nil {
		return nil, err
	}

	if err := syncCreateNormalTailWorkloadInputDirectory(filepath.Dir(archive)); err != nil {
		return nil, err
	}
	if err := e.Check(ctx); err != nil {
		return nil, err
	}
	if err := createNormalTailWorkloadForwardUnstarted(store, e, d); err != nil {
		return nil, err
	}
	current, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(current, raw) {
		return nil, createProviderRecoveryBlock("rejected workload forward input changed before replacement")
	}
	relative, err := filepath.Rel(store.Root, path)
	if err != nil {
		return nil, err
	}
	if err := store.write(relative, mustProviderRecoveryJSON(&next), true); err != nil {
		return nil, err
	}
	if err := syncCreateNormalTailWorkloadInputDirectory(filepath.Dir(path)); err != nil {
		return nil, err
	}
	return &next, nil
}
func syncCreateNormalTailWorkloadInputDirectory(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
