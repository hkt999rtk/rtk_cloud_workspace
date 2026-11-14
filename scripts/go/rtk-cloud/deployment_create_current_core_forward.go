package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const currentCoreForwardSchema = "rtk-cloud.current-core-forward/v1"

type currentCoreForwardQualification struct {
	Schema, Parent, Owner, Checker, OriginalPlan, OriginalJournal, SessionReview, Stage string
	Source                                                                              environmentCreateJournal
	Selected                                                                            environmentCreatePlan
	Service                                                                             environmentCreateServiceCeremony
	Definitions                                                                         []deploymentplan.Definition
	Candidate                                                                           *deploymentplan.Plan
	Deadline                                                                            time.Time
}
type currentCoreForwardReceipt struct {
	Schema          string `json:"schema"`
	Qualification   string `json:"qualification_sha256"`
	OriginalPlan    string `json:"original_failed_plan_sha256"`
	OriginalJournal string `json:"original_failed_journal_sha256"`
	ForwardStage    string `json:"actual_qualified_forward_stage"`
	ForwardPlan     string `json:"actual_qualified_forward_plan_sha256"`
}
type currentCoreForwardEnvelope struct {
	Schema, OriginalReview, ApplicationReview, Qualification string
	WorkloadImageForward                                     string `json:",omitempty"`
}

func currentCoreForwardBlock(boundary string) error {
	return createProviderRecoveryBlock("current core successor differs from its original native scope; boundary=" + boundary)
}
func currentCoreForwardBase(parent string) string {
	return filepath.Join("deployment/create", parent, "current-core-forward")
}

// The caller authenticates all owner facts together against the original
// journal and completed session. This compiler permits only that session's
// literal references and the existing native cleanup projection to change.
func compileCurrentCoreForwardRecipe(cfg deploymentConfig, original *deploymentplan.Plan, journal deploymentplan.Journal, owners []activeTransportRenewalResource, cleanup environmentBootstrapCleanupBinding, session string, definitions []deploymentplan.Definition, stage string) (*deploymentplan.Plan, error) {
	bad := func() (*deploymentplan.Plan, error) { return nil, currentCoreForwardBlock("exact-recipe") }
	if original == nil || journal.PlanSHA256 != original.SHA256() || journal.Accepted || journal.AcceptanceStarted || journal.InFlight != "" || journal.Failed == "" || len(journal.Completed) > len(original.Resources) || len(definitions) == 0 || !secretEnvironmentPattern.MatchString(stage) || original.Environment != cfg.Environment || original.Stack != cfg.Values["CLOUD_STACK_NAME"] || cleanup.SessionID != session || validateEnvironmentBootstrapCleanupBinding(&cleanup, cfg.Environment) != nil {
		return bad()
	}
	controllerID := "Deployment/" + original.Stack + "-video-cloud/pki-controller"
	var oldController deploymentplan.Object
	byID := map[string]deploymentplan.Definition{}
	for _, r := range original.Resources {
		if r.Action == "retire" || deploymentplan.ID(r.Desired) != r.ID {
			return bad()
		}
		if r.ID == controllerID {
			oldController = deploymentplan.Clone(r.Desired)
		}
		byID[r.ID] = deploymentplan.Definition{Object: deploymentplan.Clone(r.Desired), Phase: r.Phase, Dependencies: slices.Clone(r.Dependencies)}
	}
	if oldController == nil {
		return bad()
	}
	oldSession := mqttFoundationEnv(mqttFoundationPrimary(oldController), "PKI_BOOTSTRAP_SESSION_ID")
	if !environmentBootstrapOwnerUUID.MatchString(oldSession) || oldSession == session {
		return bad()
	}
	oldMapName := deploymentplan.String(deploymentplan.Map(managedNamedField(environmentPKIPod(oldController)["volumes"], "bootstrap-cleanup")["configMap"])["name"])
	oldMapID := "ConfigMap/" + original.Stack + "-video-cloud/" + oldMapName
	oldMap, ok := byID[oldMapID]
	if !ok || oldMap.Object["immutable"] != true || (oldMapName != "pki-bootstrap-cleanup-binding" && oldMapName != "pki-bootstrap-cleanup-binding-"+oldSession[:12]) {
		return bad()
	}
	var oldBinding environmentBootstrapCleanupBinding
	oldData := deploymentplan.Map(oldMap.Object["data"])
	ca, err := base64.StdEncoding.Strict().DecodeString(cleanup.KubernetesCA)
	if err != nil || decodeEnvironmentJSON([]byte(deploymentplan.String(oldData["binding.json"])), &oldBinding) != nil || oldBinding.SessionID != oldSession || oldBinding.NamespaceUID != cleanup.NamespaceUID || oldBinding.PolicySHA256 != cleanup.PolicySHA256 || deploymentplan.String(oldData["ca.crt"]) != string(ca) {
		return bad()
	}
	for id, d := range byID {
		if _, err := serviceSessionSIDSlots(d.Object, oldSession, session); err != nil {
			return bad()
		}
		for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
			c := deploymentplan.Map(raw)
			for _, entry := range certIssuerObjectList(c["env"]) {
				value := deploymentplan.Map(entry)
				if value["name"] == "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID" {
					if value["value"] != oldSession || value["valueFrom"] != nil {
						return bad()
					}
					value["value"] = session
				}
			}
		}
		byID[id] = d
	}
	controller := byID[controllerID]
	controls, err := applyEnvironmentBootstrapCleanup(appendMap(cfg.Values, map[string]string{"CLOUD_ENV_NAME": cfg.Environment}), controller.Object, cleanup)
	if err != nil {
		return nil, err
	}
	byID[controllerID] = controller
	for _, d := range controls {
		id := deploymentplan.ID(d.Object)
		if previous, ok := byID[id]; ok {
			d.Dependencies = slices.Clone(previous.Dependencies)
		}
		byID[id] = d
	}
	// Keep the original immutable map as a no-write owned resource. Its UID and
	// bytes remain part of acceptance; only the separately named map is created.
	actual := append([]deploymentplan.Definition(nil), definitions...)
	foundOld := false
	for _, d := range actual {
		foundOld = foundOld || deploymentplan.ID(d.Object) == oldMapID
	}
	if !foundOld {
		oldMap.Retain = true
		actual = append(actual, oldMap)
	}
	// Compile both recipes against the same authenticated observed owners. The
	// planner preserves their existing Service/PVC allocations on both sides.
	var comparisonLive []deploymentplan.Object
	for _, owner := range owners {
		comparisonLive = append(comparisonLive, owner.Desired)
	}
	canonical := func(defs []deploymentplan.Definition) (map[string]deploymentplan.Resource, error) {
		copied := make([]deploymentplan.Definition, len(defs))
		for i, d := range defs {
			copied[i] = deploymentplan.Definition{Object: deploymentplan.Clone(d.Object), Phase: d.Phase, Dependencies: slices.Clone(d.Dependencies)}
		}
		in := original.Inputs
		in.BindingsSHA256 = deploymentplan.Digest("current-core-recipe-comparison")
		p, err := deploymentplan.Compile(in, copied, comparisonLive)
		if err != nil {
			return nil, err
		}
		out := map[string]deploymentplan.Resource{}
		for _, r := range p.Resources {
			out[r.ID] = r
		}
		return out, nil
	}
	var expected []deploymentplan.Definition
	for _, d := range byID {
		expected = append(expected, d)
	}
	want, err := canonical(expected)
	if err != nil {
		return nil, err
	}
	have, err := canonical(actual)
	if err != nil || len(want) != len(have) {
		return bad()
	}
	for id, r := range want {
		other, ok := have[id]
		if !ok || r.Phase != other.Phase || !sameCurrentCoreDependencies(r.Dependencies, other.Dependencies, oldMapID, "ConfigMap/"+original.Stack+"-video-cloud/"+cleanup.ConfigMapName) || !deploymentplan.Matches(r.Desired, other.Desired) || !deploymentplan.Matches(other.Desired, r.Desired) {
			return nil, currentCoreForwardBlock("recipe-resource=" + id)
		}
	}
	facts := map[string]activeTransportRenewalResource{}
	var live []deploymentplan.Object
	for _, owner := range owners {
		if facts[owner.ID].ID != "" || owner.UID == "" || owner.Desired == nil || deploymentplan.Map(owner.Desired["metadata"])["uid"] != owner.UID {
			return bad()
		}
		facts[owner.ID] = owner
		live = append(live, owner.Desired)
	}
	for _, r := range original.Resources {
		uid := journal.Owners[r.ID]
		if uid == "" {
			uid = r.UID
		}
		if uid == "" {
			if facts[r.ID].ID != "" {
				return bad()
			}
			continue
		}
		if facts[r.ID].UID != uid {
			return bad()
		}
	}
	in := original.Inputs
	in.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, actual)
	p, err := deploymentplan.Compile(in, actual, live)
	if err != nil {
		return nil, err
	}
	newMapID := "ConfigMap/" + original.Stack + "-video-cloud/" + cleanup.ConfigMapName
	for _, r := range p.Resources {
		if prior, ok := facts[r.ID]; ok {
			if r.UID != prior.UID || r.Action != "retain" && r.Action != "update" {
				return bad()
			}
		} else if r.ID == newMapID {
			if r.Action != "create" || r.UID != "" || r.Baseline != nil {
				return bad()
			}
		} else {
			return bad()
		}
	}
	return p, nil
}

func currentCoreOriginal(store secretStore, e *environmentCreateExecution) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	p, err := readEnvironmentCreateStageInput(store, e, "managed-complete-issuer")
	if err != nil || p == nil {
		return nil, deploymentplan.Journal{}, currentCoreForwardBlock("original-input")
	}
	j, exists, err := p.ReadExecution(store.Root)
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/managed-complete-issuer")
	intent, intentErr := environmentCreateKubernetesStageIntent(p)
	sha, hashErr := environmentCreatePlanSHA256(intent)
	leased := false
	for _, lease := range e.Journal.KubernetesLeases {
		leased = leased || managedLateLeaseMatches(lease, e.Journal.Stack, p.SHA256())
	}
	if err != nil || !exists || entry == nil || entry.Phase != "managed-complete-issuer" || intentErr != nil || hashErr != nil || entry.IntentSHA256 != sha || !leased || j.Accepted || j.AcceptanceStarted || j.InFlight != "" || j.Failed == "" || len(j.Owners) == 0 {
		return nil, j, currentCoreForwardBlock("literal-original-prefix")
	}
	return p, j, nil
}

// All original owners are folded together. A later session is included only
// through the pinned completed review, never by selecting a current template.
func currentCoreSessionOwners(ctx context.Context, store secretStore, e *environmentCreateExecution, p *deploymentplan.Plan, j deploymentplan.Journal, review string) ([]activeTransportRenewalResource, error) {
	var base []activeTransportRenewalResource
	for _, r := range p.Resources {
		uid, body := j.Owners[r.ID], r.Desired
		if uid == "" {
			uid, body = r.UID, r.Baseline
		}
		if uid == "" {
			continue
		}
		if !environmentBootstrapOwnerUUID.MatchString(uid) || body == nil {
			return nil, currentCoreForwardBlock("original-owner-baseline")
		}
		base = append(base, sessionRecordedOwnerBase(r.ID, uid, body))
	}
	start := -1
	order := map[string]int{}
	for i, entry := range e.Journal.Entries {
		if entry.Key == "kubernetes/managed-complete-issuer" {
			start = i
		}
		if strings.HasPrefix(entry.Key, "kubernetes/") {
			order[strings.TrimPrefix(entry.Key, "kubernetes/")] = i
		}
	}
	allowed := map[string]bool{}
	found := false
	for _, entry := range e.Journal.Entries {
		if entry.Phase != "service-session-batches" || !strings.HasPrefix(entry.Key, "service-session-batches-") {
			continue
		}
		selected := strings.TrimPrefix(entry.Key, "service-session-batches-")
		q, err := loadSecondSessionAdmission(store, e, selected)
		if err != nil {
			return nil, err
		}
		for _, batch := range q.Batches {
			if batch.Plan != nil {
				allowed[batch.Stage] = true
			}
		}
		if selected == review {
			found = true
			break
		}
	}
	if start < 0 || !found {
		return nil, currentCoreForwardBlock("session-after-source")
	}
	sources, err := sessionRecordedStages(ctx, store, e)
	if err != nil {
		return nil, err
	}
	var stages []completedRecordedSuccessorStage
	for _, source := range sources {
		if allowed[source.Stage] && order[source.Stage] > start {
			stages = append(stages, completedRecordedSuccessorStage{Plan: source.Plan, Journal: source.Journal, Written: !source.Journal.Accepted})
		}
	}
	return foldCompletedRecordedSuccessors(base, stages)
}

func validateCurrentCoreForward(ctx context.Context, store secretStore, e *environmentCreateExecution, q *currentCoreForwardQualification, review string) error {
	if e == nil || q == nil || q.Schema != currentCoreForwardSchema || q.Parent != e.Journal.PlanSHA256 || q.Owner != deploymentplan.Digest(e.owner) || deploymentplan.Digest(q) != review || q.Candidate == nil || !environmentPreflightDigest.MatchString(q.SessionReview) || !partialSessionJournalExtends(q.Source, e.Journal) || e.Check(ctx) != nil {
		return currentCoreForwardBlock("qualified-owner")
	}
	p, j, err := currentCoreOriginal(store, e)
	if err != nil {
		return err
	}
	if p.SHA256() != q.OriginalPlan || deploymentplan.Digest(j) != q.OriginalJournal || p.ConfigurationSHA256 != q.Selected.ConfigurationSHA256 || p.ReleaseSHA256 != deploymentplan.Digest(q.Selected.Release) || q.Candidate.ConfigurationSHA256 != p.ConfigurationSHA256 || q.Candidate.ReleaseSHA256 != p.ReleaseSHA256 || q.Stage != "current-core-forward-"+p.SHA256()[:12]+"-"+q.SessionReview[:12] {
		return currentCoreForwardBlock("original-source-binding")
	}
	latest, _, err := sessionLineageHead(store, e)
	if err != nil || latest == nil {
		return currentCoreForwardBlock("admitted-lineage")
	}
	head, err := loadSecondSessionAdmission(store, e, q.SessionReview)
	if err != nil {
		return err
	}
	done, err := completedServiceSessionBatch(store, e, head, q.SessionReview)
	if err != nil || !done {
		return currentCoreForwardBlock("completed-native-session")
	}
	if _, err := qualifiedSecondSessionPlan(store, e, &q.Selected, head.Renewal.Candidate); err != nil {
		return err
	}
	original, _, err := loadEnvironmentCreateAdmittedGeneration(store, e.Journal.PlanSHA256, e.Journal.ArtifactSHA256)
	if err != nil {
		return err
	}
	_, _, release, err := freshProductResumeAdmission(store, e)
	if err != nil {
		return err
	}
	custody, _, err := managedTransportPostAcceptedStage(store, e, "kubernetes/service-custody-reader-renewal-"+q.SessionReview)
	if err != nil {
		return err
	}
	binding := environmentFreshProductResumeBinding{SelectedPlanSHA256: deploymentplan.Digest(q.Selected), SessionReviewSHA256: q.SessionReview, CustodyStage: "service-custody-reader-renewal-" + q.SessionReview, CustodyPlanSHA256: custody.SHA256()}
	if err := verifyFreshProductResumeSelection(original, q.Selected, binding, release); err != nil {
		return err
	}
	service, err := freshProductResumeCurrentCeremony(store, e, head, q.SessionReview, q.Service)
	if err != nil || deploymentplan.Digest(service) != deploymentplan.Digest(q.Service) {
		return currentCoreForwardBlock("observed-current-cleanup")
	}
	effectiveDeadline, err := currentInitializerEffectiveDeadline(ctx, store, e, time.Now().UTC())
	if err != nil || q.Deadline.Before(head.ExecutionDeadline) || q.Deadline.After(effectiveDeadline) {
		return currentCoreForwardBlock("original-deadline")
	}
	owners, err := currentCoreSessionOwners(ctx, store, e, p, j, q.SessionReview)
	if err != nil {
		return err
	}
	baselines := map[string]deploymentplan.Resource{}
	for _, r := range q.Candidate.Resources {
		baselines[r.ID] = r
	}
	for i, owner := range owners {
		r := baselines[owner.ID]
		if r.Baseline == nil || r.UID != owner.UID || !deploymentplan.Matches(owner.Desired, r.Baseline) {
			return currentCoreForwardBlock("frozen-same-uid-baseline=" + owner.ID)
		}
		observed := deploymentplan.Clone(r.Baseline)
		deploymentplan.Map(observed["metadata"])["uid"] = owner.UID
		owners[i].Desired = observed
	}
	expected, err := compileCurrentCoreForwardRecipe(deploymentConfig{Environment: q.Selected.Environment, Values: map[string]string{"CLOUD_STACK_NAME": q.Selected.Stack}}, p, j, owners, q.Service.CleanupBinding, q.Service.BootstrapSessionID, q.Definitions, q.Stage)
	if err != nil || expected.SHA256() != q.Candidate.SHA256() {
		return currentCoreForwardBlock("deterministic-frozen-plan")
	}
	return e.Check(ctx)
}

func readCurrentCoreForward(store secretStore, e *environmentCreateExecution, review string) (*currentCoreForwardQualification, error) {
	if !environmentPreflightDigest.MatchString(review) {
		return nil, currentCoreForwardBlock("qualification-digest")
	}
	var q currentCoreForwardQualification
	path, err := store.safePath(filepath.Join(currentCoreForwardBase(e.Journal.PlanSHA256), review, "qualification.json"))
	if err != nil || readExecutionJSON(path, &q) != nil || deploymentplan.Digest(q) != review {
		return nil, currentCoreForwardBlock("immutable-qualification")
	}
	return &q, nil
}

// Admission checks literal recorded outcomes and dry-runs unstarted writes.
// Readiness is an execution/acceptance outcome, never a preflight prerequisite.
type currentCoreAdmissionBackend struct{ deploymentplan.Backend }

func (b currentCoreAdmissionBackend) Wait(ctx context.Context, _ deploymentplan.Resource) error {
	return ctx.Err()
}

func prepareCurrentCoreForward(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, definitions []deploymentplan.Definition) (*currentCoreForwardQualification, string, error) {
	e, r := runtime.Execution, runtime.ServiceSessionRecovery
	if e == nil || r == nil || !r.SecondSession || parent == nil || deploymentplan.Digest(parent) != deploymentplan.Digest(r.qualifiedPlan) || parent.ConfigurationSHA256 != deploymentplan.Digest(cfg) || e.Check(ctx) != nil {
		return nil, "", currentCoreForwardBlock("selected-original-context")
	}
	head, sessionReview, err := sessionLineageHead(store, e)
	if err != nil || head == nil || sessionReview != r.Review {
		return nil, "", currentCoreForwardBlock("current-completed-head")
	}
	service, err = freshProductResumeCurrentCeremony(store, e, *head, sessionReview, service)
	if err != nil {
		return nil, "", err
	}
	pointer, err := store.safePath(filepath.Join(currentCoreForwardBase(e.Journal.PlanSHA256), sessionReview, "selected.json"))
	if err != nil {
		return nil, "", err
	}
	var selected struct {
		Review string `json:"review_sha256"`
	}
	if _, statErr := os.Lstat(pointer); statErr == nil {
		raw, readErr := readBoundedDerivedArtifact(pointer)
		if readErr != nil {
			return nil, "", readErr
		}
		if decodeEnvironmentJSON(raw, &selected) != nil {
			return nil, "", currentCoreForwardBlock("selected-proof")
		}
		q, err := readCurrentCoreForward(store, e, selected.Review)
		if err != nil {
			return nil, "", err
		}
		if err = validateCurrentCoreForward(ctx, store, e, q, selected.Review); err != nil {
			return nil, "", err
		}
		if deploymentplan.Digest(q.Definitions) != deploymentplan.Digest(definitions) || deploymentplan.Digest(q.Selected) != deploymentplan.Digest(parent) {
			return nil, "", currentCoreForwardBlock("frozen-current-recipe")
		}
		checker, err := environmentPreflightCheckerSHA256()
		if err != nil {
			return nil, "", err
		}
		entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+q.Stage)
		if entry == nil || entry.State != "completed" {
			effective, err := currentInitializerEffectiveDeadline(ctx, store, e, time.Now().UTC())
			if err != nil {
				return nil, "", err
			}
			if checker != q.Checker || !q.Deadline.Equal(effective) {
				priorReview := selected.Review
				next := *q
				next.Checker, next.Deadline = checker, effective
				nextReview := deploymentplan.Digest(next)
				if err := validateCurrentCoreForward(ctx, store, e, &next, nextReview); err != nil {
					return nil, "", err
				}
				if err := writeCurrentCoreForwardQualification(store, &next); err != nil {
					return nil, "", err
				}
				var guard struct {
					Review string `json:"review_sha256"`
				}
				if raw, err := readBoundedDerivedArtifact(pointer); err != nil || decodeEnvironmentJSON(raw, &guard) != nil || guard.Review != priorReview || e.Check(ctx) != nil {
					return nil, "", currentCoreForwardBlock("qualification-pointer-changed")
				}
				if err := store.write(filepath.Join(currentCoreForwardBase(next.Parent), sessionReview, "selected.json"), mustProviderRecoveryJSON(map[string]string{"review_sha256": nextReview}), true); err != nil {
					return nil, "", err
				}
				q, selected.Review = &next, nextReview
			}
			if !time.Now().Before(q.Deadline) {
				return nil, "", currentCoreForwardBlock("original-execution-deadline")
			}
			backend := environmentCreateKubernetesBackend{environmentKubernetesBackend: runtime.Backend, execution: e, cfg: cfg, store: store}
			if err := q.Candidate.AdmitResumable(ctx, currentCoreAdmissionBackend{backend}, store.Root); err != nil {
				return nil, "", err
			}
		}
		return q, selected.Review, nil
	} else if !os.IsNotExist(statErr) {
		return nil, "", statErr
	}
	p, j, err := currentCoreOriginal(store, e)
	if err != nil {
		return nil, "", err
	}
	if p.ConfigurationSHA256 != parent.ConfigurationSHA256 || p.ReleaseSHA256 != deploymentplan.Digest(parent.Release) {
		return nil, "", currentCoreForwardBlock("same-configuration-release")
	}
	owners, err := currentCoreSessionOwners(ctx, store, e, p, j, sessionReview)
	if err != nil {
		return nil, "", err
	}
	for i, owner := range owners {
		live, err := runtime.Backend.Read(ctx, owner.ID)
		if err != nil || live == nil || deploymentplan.Map(live["metadata"])["uid"] != owner.UID || !deploymentplan.Matches(owner.Desired, live) {
			return nil, "", currentCoreForwardBlock("current-recorded-owner=" + owner.ID)
		}
		owners[i].Desired = live
	}
	newMap := "ConfigMap/" + parent.Stack + "-video-cloud/" + service.CleanupBinding.ConfigMapName
	if live, err := runtime.Backend.Read(ctx, newMap); err != nil || live != nil {
		return nil, "", currentCoreForwardBlock("new-scoped-cleanup-absence")
	}
	stage := "current-core-forward-" + p.SHA256()[:12] + "-" + sessionReview[:12]
	candidate, err := compileCurrentCoreForwardRecipe(cfg, p, j, owners, service.CleanupBinding, service.BootstrapSessionID, definitions, stage)
	if err != nil {
		return nil, "", err
	}
	backend := environmentCreateKubernetesBackend{environmentKubernetesBackend: runtime.Backend, execution: e, cfg: cfg, store: store}
	if err := candidate.AdmitResumable(ctx, currentCoreAdmissionBackend{backend}, store.Root); err != nil {
		return nil, "", err
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return nil, "", err
	}
	deadline, err := currentInitializerEffectiveDeadline(ctx, store, e, time.Now().UTC())
	if err != nil || deadline.Before(head.ExecutionDeadline) || !time.Now().Before(deadline) {
		return nil, "", currentCoreForwardBlock("original-execution-deadline")
	}
	q := &currentCoreForwardQualification{Schema: currentCoreForwardSchema, Parent: e.Journal.PlanSHA256, Owner: deploymentplan.Digest(e.owner), Checker: checker, OriginalPlan: p.SHA256(), OriginalJournal: deploymentplan.Digest(j), SessionReview: sessionReview, Stage: stage, Selected: *parent, Service: service, Definitions: definitions, Candidate: candidate, Deadline: deadline}
	if json.Unmarshal(mustProviderRecoveryJSON(e.Journal), &q.Source) != nil {
		return nil, "", currentCoreForwardBlock("source-snapshot")
	}
	review := deploymentplan.Digest(q)
	if err := validateCurrentCoreForward(ctx, store, e, q, review); err != nil {
		return nil, "", err
	}
	if err := writeCurrentCoreForwardQualification(store, q); err != nil {
		return nil, "", err
	}
	if _, err := prepareEnvironmentCreateDirectories(store, filepath.Dir(filepath.Join(currentCoreForwardBase(q.Parent), sessionReview, "selected.json"))); err != nil {
		return nil, "", err
	}
	if err := writeEnvironmentPreflightImmutableJSON(pointer, map[string]string{"review_sha256": review}); err != nil {
		return nil, "", err
	}
	return q, review, e.Check(ctx)
}

func sameCurrentCoreDependencies(first, second []string, oldMap, newMap string) bool {
	a, b := slices.Clone(first), slices.Clone(second)
	for i := range a {
		if a[i] == oldMap {
			a[i] = newMap
		}
	}
	slices.Sort(a)
	slices.Sort(b)
	return slices.Equal(a, b)
}

func currentCoreForwardReceiptMatches(raw json.RawMessage) bool {
	var r currentCoreForwardReceipt
	return decodeEnvironmentJSON(raw, &r) == nil && r.Schema == currentCoreForwardSchema
}
func validateCurrentCoreForwardReceipt(store secretStore, e *environmentCreateExecution, stage string, p *deploymentplan.Plan, j deploymentplan.Journal, raw json.RawMessage) error {
	var receipt currentCoreForwardReceipt
	if stage != "managed-complete-issuer" || decodeEnvironmentJSON(raw, &receipt) != nil || receipt.Schema != currentCoreForwardSchema || receipt.OriginalPlan != p.SHA256() || receipt.OriginalJournal != deploymentplan.Digest(j) {
		return currentCoreForwardBlock("typed-original-receipt")
	}
	q, err := readCurrentCoreForward(store, e, receipt.Qualification)
	if err != nil {
		return err
	}
	if err := validateCurrentCoreForward(context.Background(), store, e, q, receipt.Qualification); err != nil {
		return err
	}
	if receipt.ForwardStage != "kubernetes/"+q.Stage || receipt.ForwardPlan != q.Candidate.SHA256() {
		return currentCoreForwardBlock("accepted-successor-link")
	}
	actual, accepted, err := acceptedCreateStage(store, e, receipt.ForwardStage)
	if err != nil || actual.SHA256() != q.Candidate.SHA256() {
		return currentCoreForwardBlock("actual-accepted-child")
	}
	checked, exists, err := readEnvironmentCreateStageProgress(store, e, q.Stage, actual)
	if err != nil || !exists || deploymentplan.Digest(checked) != deploymentplan.Digest(accepted) {
		return currentCoreForwardBlock("native-successor-progress")
	}
	for _, r := range p.Resources {
		uid := j.Owners[r.ID]
		if uid == "" {
			uid = r.UID
		}
		if uid != "" && accepted.Owners[r.ID] != uid {
			return currentCoreForwardBlock("preserved-original-uid=" + r.ID)
		}
	}
	return nil
}

// Resolve the genuinely accepted child without ever returning an accepted
// version of the deliberately failed original journal.
func acceptedCurrentCoreStage(store secretStore, e *environmentCreateExecution) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/managed-complete-issuer")
	if entry == nil || !currentCoreForwardReceiptMatches(entry.Receipt) {
		return acceptedCreateStage(store, e, "kubernetes/managed-complete-issuer")
	}
	p, j, err := currentCoreOriginal(store, e)
	if err != nil {
		return nil, j, err
	}
	if entry.State != "completed" {
		return nil, j, currentCoreForwardBlock("logical-core-not-completed")
	}
	if err := validateCurrentCoreForwardReceipt(store, e, "managed-complete-issuer", p, j, entry.Receipt); err != nil {
		return nil, j, err
	}
	var receipt currentCoreForwardReceipt
	_ = decodeEnvironmentJSON(entry.Receipt, &receipt)
	return acceptedCreateStage(store, e, receipt.ForwardStage)
}

func executeCurrentCoreForward(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, runtime environmentCreateBootstrapRuntime, definitions []deploymentplan.Definition, execute func(context.Context, string, *deploymentplan.Plan) error) error {
	e, r := runtime.Execution, runtime.ServiceSessionRecovery
	if e == nil || r == nil || r.currentCore == nil || execute == nil {
		return currentCoreForwardBlock("exact-preflight-forward-required")
	}
	q := r.currentCore
	review := deploymentplan.Digest(q)
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+q.Stage)
	if deploymentplan.Digest(parent) != deploymentplan.Digest(q.Selected) || (entry == nil || entry.State != "completed") && deploymentplan.Digest(definitions) != deploymentplan.Digest(q.Definitions) {
		return currentCoreForwardBlock("same-qualified-current-recipe")
	}
	if err := validateCurrentCoreForward(ctx, store, e, q, review); err != nil {
		return err
	}
	backend := environmentCreateKubernetesBackend{environmentKubernetesBackend: runtime.Backend, execution: e, cfg: cfg, store: store}
	if entry != nil && entry.State == "completed" {
		observed, err := verifyAcceptedStageAfterRecordedHistory(ctx, store, e, backend, "kubernetes/"+q.Stage)
		if err != nil || observed.SHA256() != q.Candidate.SHA256() {
			return currentCoreForwardBlock("completed-current-owner-history")
		}
	} else {
		checker, err := environmentPreflightCheckerSHA256()
		if err != nil || checker != q.Checker || !time.Now().Before(q.Deadline) {
			return currentCoreForwardBlock("admitted-checker-deadline")
		}
		head, current, err := sessionLineageHead(store, e)
		if err != nil || head == nil || current != q.SessionReview || r.Review != q.SessionReview {
			return currentCoreForwardBlock("unchanged-current-head")
		}
		if err := q.Candidate.AdmitResumable(ctx, currentCoreAdmissionBackend{backend}, store.Root); err != nil {
			return err
		}
		bounded, cancel := context.WithDeadline(ctx, q.Deadline)
		defer cancel()
		if err := execute(bounded, q.Stage, q.Candidate); err != nil {
			return err
		}
	}
	actual, _, err := acceptedCreateStage(store, e, "kubernetes/"+q.Stage)
	if err != nil || actual.SHA256() != q.Candidate.SHA256() {
		return currentCoreForwardBlock("genuine-native-acceptance")
	}
	receipt := currentCoreForwardReceipt{Schema: currentCoreForwardSchema, Qualification: review, OriginalPlan: q.OriginalPlan, OriginalJournal: q.OriginalJournal, ForwardStage: "kubernetes/" + q.Stage, ForwardPlan: q.Candidate.SHA256()}
	raw, err := canonicalEnvironmentCreateJSON(mustProviderRecoveryJSON(receipt))
	if err != nil {
		return err
	}
	p, j, err := currentCoreOriginal(store, e)
	if err != nil {
		return err
	}
	if err := validateCurrentCoreForwardReceipt(store, e, "managed-complete-issuer", p, j, raw); err != nil {
		return err
	}
	original := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/managed-complete-issuer")
	if original.State == "completed" {
		if !samePersistedJSONReceipt(original.Receipt, raw) {
			return currentCoreForwardBlock("original-completion-receipt")
		}
		return e.Check(ctx)
	}
	if original.State == "pending" {
		if err := e.RecordReceipt(original.Key, raw); err != nil {
			return err
		}
	} else if original.State != "allocated" || !samePersistedJSONReceipt(original.Receipt, raw) {
		return currentCoreForwardBlock("original-allocated-receipt")
	}
	return e.CompleteEntry(original.Key, time.Now().UTC())
}

func writeCurrentCoreForwardEnvelope(store secretStore, parent, original, qualification, application string, workloadImages ...string) (string, error) {
	envelope := currentCoreForwardEnvelope{Schema: currentCoreForwardSchema, OriginalReview: original, ApplicationReview: application, Qualification: qualification}
	if len(workloadImages) > 1 {
		return "", currentCoreForwardBlock("exact-workload-image-review")
	}
	if len(workloadImages) == 1 {
		envelope.WorkloadImageForward = workloadImages[0]
	}
	if envelope.WorkloadImageForward != "" && !environmentPreflightDigest.MatchString(envelope.WorkloadImageForward) {
		return "", currentCoreForwardBlock("exact-workload-image-review")
	}

	if !environmentPreflightDigest.MatchString(parent) || !environmentPreflightDigest.MatchString(original) || qualification != "" && !environmentPreflightDigest.MatchString(qualification) || qualification == "" && envelope.WorkloadImageForward == "" || application != "" && !environmentPreflightDigest.MatchString(application) {
		return "", currentCoreForwardBlock("full-go-envelope")
	}
	review := deploymentplan.Digest(envelope)
	relative := filepath.Join("deployment/create/issuer-enrollment-continuation", review, "current-core-forward.json")
	if _, err := prepareEnvironmentCreateDirectories(store, filepath.Dir(relative)); err != nil {
		return "", err
	}
	path, err := store.safePath(relative)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		if err := writeEnvironmentPreflightImmutableJSON(path, envelope); err != nil {
			return "", err
		}
	} else if err != nil {
		return "", err
	} else {
		var prior currentCoreForwardEnvelope
		if readExecutionJSON(path, &prior) != nil || deploymentplan.Digest(prior) != review {
			return "", currentCoreForwardBlock("immutable-full-go-envelope")
		}
	}
	return review, nil
}
func loadCurrentCoreForwardEnvelope(ctx context.Context, store secretStore, e *environmentCreateExecution, review, original string, parent *environmentCreatePlan, runtime environmentCreateBootstrapRuntime) (*currentCoreForwardEnvelope, error) {
	path, err := store.safePath(filepath.Join("deployment/create/issuer-enrollment-continuation", review, "current-core-forward.json"))
	if err != nil || !environmentPreflightDigest.MatchString(review) {
		return nil, currentCoreForwardBlock("exact-reviewed-full-envelope")
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	var envelope currentCoreForwardEnvelope
	if readExecutionJSON(path, &envelope) != nil || envelope.Schema != currentCoreForwardSchema || deploymentplan.Digest(envelope) != review || envelope.OriginalReview != original || envelope.ApplicationReview != "" && !environmentPreflightDigest.MatchString(envelope.ApplicationReview) {
		return nil, currentCoreForwardBlock("exact-reviewed-full-envelope")
	}
	if envelope.WorkloadImageForward != "" {
		workload, _, err := selectedCreateWorkloadImageForward(ctx, store, e, runtime.Backend)
		if err != nil || workload == nil || deploymentplan.Digest(workload) != envelope.WorkloadImageForward || e.versionState == nil || e.versionState.cache == nil || deploymentplan.Digest(e.versionState.cache.RuntimeImages) != deploymentplan.Digest(workload.Images) {
			return nil, currentCoreForwardBlock("exact-workload-image-review")
		}
	}
	if envelope.Qualification == "" {
		if envelope.WorkloadImageForward == "" {
			return nil, currentCoreForwardBlock("missing-full-scope-review")
		}
		return &envelope, nil
	}
	q, err := readCurrentCoreForward(store, e, envelope.Qualification)
	if err != nil {
		return nil, err
	}
	if err := validateCurrentCoreForward(ctx, store, e, q, envelope.Qualification); err != nil {
		return nil, err
	}
	r := runtime.ServiceSessionRecovery
	if r == nil || !r.SecondSession || deploymentplan.Digest(parent) != deploymentplan.Digest(q.Selected) {
		return nil, currentCoreForwardBlock("selected-envelope-session")
	}
	if r.Review != q.SessionReview {
		// A completed core belongs to its original session. Later same-key
		// sessions may continue it only through the authenticated native lineage.
		entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+q.Stage)
		head, current, err := sessionLineageHead(store, e)
		if entry == nil || entry.State != "completed" || err != nil || head == nil || current != r.Review {
			return nil, currentCoreForwardBlock("selected-envelope-session")
		}
		done, err := completedServiceSessionBatch(store, e, *head, current)
		if err != nil || !done {
			return nil, currentCoreForwardBlock("selected-envelope-session")
		}
		if _, err := freshProductResumeSourceAdmission(store, e, q.SessionReview); err != nil {
			return nil, currentCoreForwardBlock("selected-envelope-session")
		}
	}
	r.currentCore = q
	return &envelope, nil
}

// Preserve the application's separate SHA only at its own dispatch boundary.
func currentCoreApplicationDispatchReview(original, supplied string, envelope *currentCoreForwardEnvelope) (string, error) {
	if envelope == nil {
		return supplied, nil
	}
	if envelope.OriginalReview != original || envelope.Schema != currentCoreForwardSchema {
		return "", currentCoreForwardBlock("application-dispatch-envelope")
	}
	if envelope.ApplicationReview == "" {
		return original, nil
	}
	if !environmentPreflightDigest.MatchString(envelope.ApplicationReview) {
		return "", currentCoreForwardBlock("application-dispatch-review")
	}
	return envelope.ApplicationReview, nil
}

func reportCurrentCoreApplicationReview(store secretStore, parent, original, application string, q *currentCoreForwardQualification, report *unifiedDeploymentReport, environment string, qualifiedAt, validUntil time.Time, workloadImages ...string) (string, error) {
	review := application
	if q != nil || len(workloadImages) > 1 || len(workloadImages) == 1 && workloadImages[0] != "" {
		qualification := ""
		if q != nil {
			qualification = deploymentplan.Digest(q)
		}
		var err error
		review, err = writeCurrentCoreForwardEnvelope(store, parent, original, qualification, application, workloadImages...)
		if err != nil {
			return "", err
		}
	}
	for i := range report.Gates {
		report.Gates[i].EvidenceSHA256 = review
	}
	report.Plan = &deploymentplan.Summary{SchemaVersion: 1, Environment: environment, Operation: "create", PlanSHA256: review}
	for _, stage := range []string{"application-crl-initialization", "application-owner-readiness", "remaining-bootstrap", "workload", "routing", "acceptance"} {
		report.Plan.Resources = append(report.Plan.Resources, deploymentplan.SummaryResource{ID: "Stage/" + stage, Phase: stage, Action: "create"})
	}
	report.Decision, report.QualifiedAt, report.EvidenceValidUntil = "GO", &qualifiedAt, &validUntil
	report.QualificationSHA256 = review
	return review, nil
}

func writeCurrentCoreForwardQualification(store secretStore, q *currentCoreForwardQualification) error {
	raw, err := json.Marshal(q)
	if err != nil || len(raw) > environmentPreflightArtifactLimit {
		return currentCoreForwardBlock("bounded-qualification")
	}
	relative := filepath.Join(currentCoreForwardBase(q.Parent), deploymentplan.Digest(q), "qualification.json")
	path, err := store.safePath(relative)
	if err != nil {
		return err
	}
	if prior, err := readBoundedDerivedArtifact(path); err == nil {
		if !bytes.Equal(prior, raw) {
			return currentCoreForwardBlock("immutable-qualification-bytes")
		}
		return nil
	} else if _, statErr := os.Lstat(path); !os.IsNotExist(statErr) {
		return currentCoreForwardBlock("unsafe-qualification-file")
	}
	if _, err := prepareEnvironmentCreateDirectories(store, filepath.Dir(relative)); err != nil {
		return err
	}
	return writeEnvironmentPreflightImmutableJSON(path, q)
}
