package main

import (
	"context"
	"io"
	"sort"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const createNormalTailPostgresAccessStage = "runtime-dependency-access"

func createNormalTailPostgresAccessDefinitions(stack string, sources ...*deploymentplan.Plan) ([]deploymentplan.Definition, error) {
	env := map[string]string{"CLOUD_STACK_NAME": stack}
	if len(sources) > 1 {
		return nil, createProviderRecoveryBlock("one original runtime dependency graph required")
	}
	if len(sources) == 1 && sources[0] != nil {
		for _, r := range sources[0].Resources {
			if r.Action == "retire" || r.Desired["kind"] != "Deployment" || deploymentplan.Map(r.Desired["spec"])["replicas"] == float64(0) {
				continue
			}
			switch deploymentplan.String(deploymentplan.Map(r.Desired["metadata"])["name"]) {
			case "video-cloud-mqttfoundation":
				env["MQTT_FOUNDATION_REGISTRATION_ENABLED"] = "true"
			case "video-cloud-shadowworker":
				env["SHADOW_WORKER_REGISTRATION_ENABLED"] = "true"
			case "video-cloud-otaservice":
				env["OTA_SERVICE_REGISTRATION_ENABLED"] = "true"
			case "video-cloud-videostorage":
				env["VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED"] = "true"
			case "video-cloud-webrtcservice":
				env["WEBRTC_SERVICE_REGISTRATION_ENABLED"] = "true"
			}
		}
	}
	defs, err := decodeEnvironmentCreateManifests(lkeAllowPostgresOpenBaoNetworkPolicyManifest(env))
	if err != nil {
		return nil, err
	}
	defs = append(defs, environmentNativeConnectivityDefinitionsV1(env)...)
	sort.Slice(defs, func(i, j int) bool { return deploymentplan.ID(defs[i].Object) < deploymentplan.ID(defs[j].Object) })
	for i := range defs {
		meta := deploymentplan.Map(defs[i].Object["metadata"])
		labels := deploymentplan.Map(meta["labels"])
		if labels == nil {
			labels = deploymentplan.Object{}
			meta["labels"] = labels
		}
		labels["rtk.realtek.com/stack"] = stack
		labels["app.kubernetes.io/managed-by"] = "rtk-cloud"
	}
	return defs, nil
}
func validateCreateNormalTailPostgresAccessPlan(p *deploymentplan.Plan, sources ...*deploymentplan.Plan) error {
	bad := func() error {
		return createProviderRecoveryBlock("runtime dependency access differs from its exact maintained policy set")
	}
	if p == nil {
		return bad()
	}
	defs, err := createNormalTailPostgresAccessDefinitions(p.Stack, sources...)
	if err != nil || len(p.Resources) != len(defs) || p.BindingsSHA256 != environmentCreateStageBindingsSHA(createNormalTailPostgresAccessStage, defs) {
		return bad()
	}
	byID := map[string]deploymentplan.Definition{}
	for _, d := range defs {
		byID[deploymentplan.ID(d.Object)] = d
	}
	for _, r := range p.Resources {
		d, ok := byID[r.ID]
		if !ok || r.Action != "create" || r.Phase != "configuration" || r.UID != "" || r.Baseline != nil || len(r.Dependencies) != 0 || deploymentplan.Digest(deploymentplan.Projection(r.Desired)) != deploymentplan.Digest(deploymentplan.Projection(d.Object)) {
			return bad()
		}
		delete(byID, r.ID)
	}
	return nil
}

// Authenticate the original pending workload and its genuinely completed
// policy. No checkpoint count or new approval/clock defines this correction.
func validateCreateNormalTailPostgresAccessSource(e *environmentCreateExecution, workload *deploymentplan.Plan, j deploymentplan.Journal) error {
	bad := func() error {
		return createProviderRecoveryBlock("PostgreSQL access repair lacks its original completed policy and workload owner")
	}
	if e == nil || workload == nil || workload.CreateParentSHA256 != e.Journal.PlanSHA256 || workload.Environment != e.Journal.Environment || workload.Stack != e.Journal.Stack || j.PlanSHA256 != workload.SHA256() || j.InFlight != "" || len(j.Completed) > len(workload.Resources) {
		return bad()
	}
	for i, id := range j.Completed {
		if id != workload.Resources[i].ID {
			return bad()
		}
	}
	defs, err := decodeEnvironmentCreateManifests(lkeAllowPostgresClientsNetworkPolicyManifest(map[string]string{"CLOUD_STACK_NAME": workload.Stack}))
	if err != nil || len(defs) != 1 {
		return bad()
	}
	labels := deploymentplan.Map(deploymentplan.Map(defs[0].Object["metadata"])["labels"])
	labels["rtk.realtek.com/stack"] = workload.Stack
	labels["app.kubernetes.io/managed-by"] = "rtk-cloud"
	policyID := deploymentplan.ID(defs[0].Object)
	policy, postgres, openbao := false, false, false
	for i, r := range workload.Resources {
		switch r.ID {
		case policyID:
			if i >= len(j.Completed) || r.Action == "retire" || !environmentBootstrapOwnerUUID.MatchString(j.Owners[r.ID]) || r.UID != "" && r.UID != j.Owners[r.ID] || deploymentplan.Digest(deploymentplan.Projection(r.Desired)) != deploymentplan.Digest(deploymentplan.Projection(defs[0].Object)) {
				return bad()
			}
			policy = true
		case "StatefulSet/" + workload.Stack + "-platform/postgresql":
			postgres = environmentBootstrapOwnerUUID.MatchString(r.UID) && r.Action != "retire" && (j.Owners[r.ID] == "" || j.Owners[r.ID] == r.UID)
		case "StatefulSet/" + workload.Stack + "-secrets/openbao":
			openbao = environmentBootstrapOwnerUUID.MatchString(r.UID) && r.Action != "retire" && (j.Owners[r.ID] == "" || j.Owners[r.ID] == r.UID)
		}
	}
	if !policy || !postgres || !openbao {
		return bad()
	}
	return nil
}

// Pure reader shared by semantic history, current fences and final target
// resolution. The standard scoped child retains its own Plan, journal and lease.
func readCreateNormalTailPostgresAccessStage(store secretStore, e *environmentCreateExecution, workload *deploymentplan.Plan, j deploymentplan.Journal) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	var empty deploymentplan.Journal
	if err := validateCreateNormalTailPostgresAccessSource(e, workload, j); err != nil {
		return nil, empty, err
	}
	p, err := readCreateNormalTailPreparationPlan(store, e, createNormalTailPostgresAccessStage)
	if err != nil {
		return nil, empty, err
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+createNormalTailPostgresAccessStage)
	if entry == nil {
		if p != nil {
			if _, exists, err := p.ReadExecution(store.Root); err != nil || exists {
				return nil, empty, createProviderRecoveryBlock("PostgreSQL access child has no original parent request")
			}
			for _, lease := range e.Journal.KubernetesLeases {
				if managedLateLeaseMatches(lease, e.Journal.Stack, p.SHA256()) {
					return nil, empty, createProviderRecoveryBlock("PostgreSQL access lease has no original parent request")
				}
			}
		}
		return nil, empty, nil
	}
	if p == nil {
		return nil, empty, createProviderRecoveryBlock("PostgreSQL access request lost its original compiled policy")
	}
	if err := validateCreateNormalTailPostgresAccessPlan(p, workload); err != nil {
		return nil, empty, err
	}
	_, journal, err := readCreateNormalTailStage(store, e, createNormalTailPostgresAccessStage, p)
	if err != nil {
		return nil, journal, err
	}
	return p, journal, nil
}

func createNormalTailPostgresAccessEvidence(store secretStore, e *environmentCreateExecution) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	var empty deploymentplan.Journal
	frozen, err := readCreateNormalTailFrozenTarget(store, e)
	if err != nil {
		return nil, empty, err
	}
	if frozen == nil {
		return nil, empty, nil
	}
	workload, j, err := readCreateNormalTailWorkloadOriginal(store, e, frozen)
	if err != nil || workload == nil {
		return nil, empty, err
	}
	return readCreateNormalTailPostgresAccessStage(store, e, workload, j)
}

func ensureCreateNormalTailPostgresAccess(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, program *createNormalTailProgram, backend environmentKubernetesBackend, out io.Writer) error {
	return ensureCreateNormalTailPostgresAccessWithApply(ctx, cfg, h, program, backend.Read, func(ctx context.Context, defs []deploymentplan.Definition) error {
		parent := *h.OriginalPlan
		parent.Release = program.Release
		return applyCreateNormalTailStage(withCreateNormalTailHandoff(ctx, h), cfg, h.Execution.Store, &parent, h.Execution, createNormalTailPostgresAccessStage, defs, out)
	})
}

func ensureCreateNormalTailPostgresAccessWithApply(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, program *createNormalTailProgram, read func(context.Context, string) (deploymentplan.Object, error), apply func(context.Context, []deploymentplan.Definition) error) error {
	if h == nil || h.Execution == nil || h.OriginalPlan == nil || program == nil {
		return createProviderRecoveryBlock("original PostgreSQL access owner unavailable")
	}
	e := h.Execution
	if createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload") == nil {
		return nil
	}
	frozen, err := readCreateNormalTailFrozenTarget(e.Store, e)
	if err != nil {
		return err
	}
	workload, j, err := readCreateNormalTailWorkloadOriginal(e.Store, e, frozen)
	if err != nil {
		return err
	}
	defs, err := createNormalTailPostgresAccessDefinitions(e.Journal.Stack, workload)
	if err != nil {
		return err
	}
	declared := map[string]bool{}
	for _, d := range program.Definitions {
		declared[deploymentplan.ID(d.Object)] = true
	}
	complete := true
	for _, d := range defs {
		complete = complete && declared[deploymentplan.ID(d.Object)]
	}
	if complete {
		return nil
	}
	if err := validateCreateNormalTailPostgresAccessSource(e, workload, j); err != nil {
		return err
	}
	if createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+createNormalTailPostgresAccessStage) == nil && createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/workload").State != "pending" {
		return createProviderRecoveryBlock("PostgreSQL access repair requires the original pending workload")
	}
	prior, priorJournal, err := readCreateNormalTailPostgresAccessStage(e.Store, e, workload, j)
	if err != nil {
		return err
	}
	if prior != nil && priorJournal.Accepted {
		for _, r := range prior.Resources {
			current, err := read(ctx, r.ID)
			if err != nil || current == nil || deploymentplan.Map(current["metadata"])["uid"] != priorJournal.Owners[r.ID] || !deploymentplan.Matches(r.Desired, current) {
				return createProviderRecoveryBlock("accepted runtime dependency access policy owner or body changed: " + r.ID)
			}
		}
		return apply(withCreateNormalTailHandoff(ctx, h), defs)
	}

	// First application authenticates current source bodies without requiring
	// OpenBao registry readiness. Later accepted retries fence the policy itself;
	// the original workload/steady executors own legitimate subsequent changes.
	for _, r := range workload.Resources {
		if r.ID != "NetworkPolicy/"+workload.Stack+"-platform/allow-postgres-clients" && r.ID != "StatefulSet/"+workload.Stack+"-platform/postgresql" && r.ID != "StatefulSet/"+workload.Stack+"-secrets/openbao" {
			continue
		}
		current, err := read(ctx, r.ID)
		expected := r.UID
		if j.Owners[r.ID] != "" {
			expected = j.Owners[r.ID]
		}
		if err != nil || current == nil || expected == "" || deploymentplan.Map(current["metadata"])["uid"] != expected {
			return createProviderRecoveryBlock("PostgreSQL access source owner changed: " + r.ID)
		}
		want := r.Baseline
		if j.Owners[r.ID] != "" {
			want = r.Desired
		}
		if want == nil || !deploymentplan.Matches(want, current) {
			return createProviderRecoveryBlock("original PostgreSQL access source body changed: " + r.ID)
		}
	}
	return apply(withCreateNormalTailHandoff(ctx, h), defs)
}

func verifyCreateNormalTailPostgresAccessFromInventory(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, backend environmentKubernetesBackend, observed map[string]deploymentplan.Object) error {
	if h == nil || h.Execution == nil || createOpenBaoACKCompletionEntry(h.Execution.Journal, "kubernetes/"+createNormalTailPostgresAccessStage) == nil {
		return nil
	}
	p, j, err := createNormalTailPostgresAccessEvidence(h.Execution.Store, h.Execution)
	if err != nil {
		return err
	}
	if p == nil || !j.Accepted {
		return createProviderRecoveryBlock("PostgreSQL access policy has no accepted original child")
	}
	for _, r := range p.Resources {
		current := observed[r.ID]
		if current == nil {
			current, err = backend.Read(ctx, r.ID)
			if err != nil {
				return err
			}
		}
		if current == nil || !environmentBootstrapOwnerUUID.MatchString(j.Owners[r.ID]) || deploymentplan.Map(current["metadata"])["uid"] != j.Owners[r.ID] || !deploymentplan.Matches(r.Desired, current) {
			return createProviderRecoveryBlock("accepted runtime dependency access policy owner or body changed: " + r.ID)
		}
	}

	return h.Execution.Check(ctx)
}

func retainCreateNormalTailPostgresAccess(store secretStore, e *environmentCreateExecution, definitions []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	if createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+createNormalTailPostgresAccessStage) == nil {
		return definitions, nil
	}
	p, j, err := createNormalTailPostgresAccessEvidence(store, e)
	if err != nil {
		return nil, err
	}
	if p == nil || !j.Accepted {
		return nil, createProviderRecoveryBlock("final target requires accepted PostgreSQL access")
	}
	for _, r := range p.Resources {
		object := deploymentplan.Clone(r.Desired)
		deploymentplan.Map(object["metadata"])["uid"] = j.Owners[r.ID]
		found := false
		for i, d := range definitions {
			if deploymentplan.ID(d.Object) == r.ID {
				if deploymentplan.Digest(d.Object["spec"]) != deploymentplan.Digest(r.Desired["spec"]) {
					return nil, createProviderRecoveryBlock("final runtime dependency policy differs from accepted child")
				}
				definitions[i] = deploymentplan.Definition{Object: object, Phase: r.Phase, Retain: true}
				found = true
			}
		}
		if !found {
			definitions = append(definitions, deploymentplan.Definition{Object: object, Phase: r.Phase, Retain: true})
		}
	}
	return definitions, nil
}
