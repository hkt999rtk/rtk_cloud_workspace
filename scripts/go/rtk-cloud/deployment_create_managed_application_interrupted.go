package main

import (
	"context"
	"path/filepath"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This is the actual stopped rollout after the admitted sibling-mount repair.
// Its empty Failed field is preserved; an owned API write and the exact
// completed prefix prove the interruption without fabricating a failure.
type createManagedApplicationInterruptedForward struct {
	Previous                       *createManagedApplicationInterruptedForward `json:"Previous,omitempty"`
	Review, SourceSHA, ArtifactSHA string
	LeaseSHA                       string
	Plan                           *deploymentplan.Plan
	Journal                        deploymentplan.Journal
}

func managedApplicationInterruptedReview(j environmentCreateJournal) string {
	return managedApplicationInterruptedReviewDepth(j, 3)
}

func managedApplicationInterruptedReviewDepth(j environmentCreateJournal, remaining int) string {
	if len(j.Entries) < 15 || remaining < 1 {
		return ""
	}
	last := j.Entries[len(j.Entries)-1]
	const prefix = "kubernetes/managed-application-owners-forward-"
	review := strings.TrimPrefix(last.Key, prefix)
	if !strings.HasPrefix(last.Key, prefix) || !environmentPreflightDigest.MatchString(review) || last.Phase != strings.TrimPrefix(last.Key, "kubernetes/") || last.State != "pending" || !last.CompletedAt.IsZero() || len(last.Receipt) != 0 || !environmentPreflightDigest.MatchString(last.IntentSHA256) {
		return ""
	}
	size := 2
	if j.Entries[len(j.Entries)-2].Key == "managed-application-interrupted-lease-release-"+review {
		size = 3
	}
	admission := j.Entries[len(j.Entries)-size]
	if admission.Key != "openbao-recovery-admission-"+review || admission.State != "completed" || admission.CompletedAt.IsZero() || len(admission.Receipt) == 0 || !environmentPreflightDigest.MatchString(admission.IntentSHA256) {
		return ""
	}
	before := j
	before.Entries = j.Entries[:len(j.Entries)-size]
	if size == 2 {
		if managedApplicationPartialReview(before) == "" {
			previous := managedApplicationInterruptedReviewDepth(before, remaining-1)
			if remaining < 3 || previous == "" || before.Entries[len(before.Entries)-2].Key != "managed-application-interrupted-lease-release-"+previous {
				return ""
			}
		}
	} else {
		release := j.Entries[len(j.Entries)-2]
		if release.Phase != "managed-application-interrupted-lease-release" || release.State != "completed" || release.CompletedAt.IsZero() || len(release.Receipt) == 0 || !environmentPreflightDigest.MatchString(release.IntentSHA256) || managedApplicationInterruptedReviewDepth(before, remaining-1) == "" {
			return ""
		}
	}
	return review
}

func validateManagedApplicationInterruptedJournal(plan *deploymentplan.Plan, j deploymentplan.Journal) error {
	bad := func() error {
		return createProviderRecoveryBlock("interrupted application forward requires exact twenty-two retained completed owners, one written API and three untouched updates")
	}
	if plan == nil || plan.Version != deploymentplan.Version || plan.Stack != "video-cloud-"+plan.Environment || plan.Operation != "scoped" || len(plan.Resources) != 26 || j.PlanSHA256 != plan.SHA256() || j.Accepted || j.AcceptanceStarted || j.InFlight != "" || j.Failed != "" || len(j.Completed) != 22 || len(j.Owners) != 23 {
		return bad()
	}
	ids := managedApplicationPartialResourceIDs(plan.Stack + "-video-cloud")
	for index, resource := range plan.Resources {
		phase := "configuration"
		if index < 2 {
			phase = "storage"
		} else if index < 4 {
			phase = "bootstrap"
		} else if index >= 20 {
			phase = "workload"
		}
		action := "retain"
		if index >= 22 {
			action = "update"
		}
		if resource.ID != ids[index] || deploymentplan.ID(resource.Desired) != resource.ID || resource.Phase != phase || resource.Action != action || resource.Baseline == nil || !environmentBootstrapOwnerUUID.MatchString(resource.UID) {
			return bad()
		}
		if index < 22 && (j.Completed[index] != resource.ID || j.Owners[resource.ID] != resource.UID) || index == 22 && j.Owners[resource.ID] != resource.UID || index > 22 && j.Owners[resource.ID] != "" {
			return bad()
		}
	}
	return nil
}

func validateManagedApplicationLatestInterruptedJournal(plan *deploymentplan.Plan, j deploymentplan.Journal) error {
	bad := func() error {
		return createProviderRecoveryBlock("latest application forward requires its completed main API, written App PKI API and untouched Device API/log owners")
	}
	if plan == nil || plan.Version != deploymentplan.Version || plan.Stack != "video-cloud-"+plan.Environment || plan.Operation != "scoped" || len(plan.Resources) != 26 || j.PlanSHA256 != plan.SHA256() || j.Accepted || j.AcceptanceStarted || j.InFlight != "" || j.Failed != "Deployment/"+plan.Stack+"-video-cloud/video-cloud-api-app-pki" || len(j.Completed) != 23 || len(j.Owners) != 24 {
		return bad()
	}
	ids := managedApplicationPartialResourceIDs(plan.Stack + "-video-cloud")
	for index, resource := range plan.Resources {
		phase := "configuration"
		if index < 2 {
			phase = "storage"
		} else if index < 4 {
			phase = "bootstrap"
		} else if index >= 20 {
			phase = "workload"
		}
		action := "retain"
		if index >= 22 {
			action = "update"
		}
		if resource.ID != ids[index] || deploymentplan.ID(resource.Desired) != resource.ID || resource.Phase != phase || resource.Action != action || resource.Baseline == nil || !environmentBootstrapOwnerUUID.MatchString(resource.UID) {
			return bad()
		}
		if index < 23 && (j.Completed[index] != resource.ID || j.Owners[resource.ID] != resource.UID) || index == 23 && j.Owners[resource.ID] != resource.UID || index > 23 && j.Owners[resource.ID] != "" {
			return bad()
		}
	}
	return nil
}

func validateManagedApplicationThirdInterruptedJournal(plan *deploymentplan.Plan, j deploymentplan.Journal) error {
	bad := func() error {
		return createProviderRecoveryBlock("App-role application forward requires exactly twenty-three retained complete owners, written App API and two untouched updates")
	}
	if plan == nil || plan.Version != deploymentplan.Version || plan.Stack != "video-cloud-"+plan.Environment || plan.Operation != "scoped" || len(plan.Resources) != 26 || j.PlanSHA256 != plan.SHA256() || j.Accepted || j.AcceptanceStarted || j.InFlight != "" || j.Failed != "Deployment/"+plan.Stack+"-video-cloud/video-cloud-api-app-pki" || len(j.Completed) != 23 || len(j.Owners) != 24 {
		return bad()
	}
	ids := managedApplicationPartialResourceIDs(plan.Stack + "-video-cloud")
	for index, resource := range plan.Resources {
		phase := "configuration"
		if index < 2 {
			phase = "storage"
		} else if index < 4 {
			phase = "bootstrap"
		} else if index >= 20 {
			phase = "workload"
		}
		action := "retain"
		if index >= 23 {
			action = "update"
		}
		if resource.ID != ids[index] || deploymentplan.ID(resource.Desired) != resource.ID || resource.Phase != phase || resource.Action != action || resource.Baseline == nil || !environmentBootstrapOwnerUUID.MatchString(resource.UID) {
			return bad()
		}
		if index < 23 && (j.Completed[index] != resource.ID || j.Owners[resource.ID] != resource.UID) || index == 23 && j.Owners[resource.ID] != resource.UID || index > 23 && j.Owners[resource.ID] != "" {
			return bad()
		}
	}
	return nil
}

func validateManagedApplicationInterruptedForward(partial *createManagedApplicationPartialRecovery, forward *createManagedApplicationInterruptedForward) error {
	return validateManagedApplicationInterruptedForwardDepth(partial, forward, 3)
}

func validateManagedApplicationInterruptedForwardDepth(partial *createManagedApplicationPartialRecovery, forward *createManagedApplicationInterruptedForward, remaining int) error {
	bad := func() error {
		return createProviderRecoveryBlock("interrupted application forward differs from its original qualified candidate or owners")
	}
	if remaining < 1 || partial == nil || validateManagedApplicationPartialJournal(partial.Plan, partial.Journal) != nil || forward == nil || !environmentPreflightDigest.MatchString(forward.Review) || !environmentPreflightDigest.MatchString(forward.SourceSHA) || !environmentPreflightDigest.MatchString(forward.ArtifactSHA) {
		return bad()
	}
	original := *partial
	original.Interrupted = nil
	if forward.Previous == nil {
		if validateManagedApplicationInterruptedJournal(forward.Plan, forward.Journal) != nil || validateManagedApplicationHistoricalPartialCandidate(&original, forward.Plan) != nil {
			return bad()
		}
	} else {
		if validateManagedApplicationInterruptedForwardDepth(&original, forward.Previous, remaining-1) != nil {
			return bad()
		}
		original.Interrupted = forward.Previous
		if forward.Previous.Previous == nil {
			if validateManagedApplicationLatestInterruptedJournal(forward.Plan, forward.Journal) != nil || validateManagedApplicationHistoricalServiceCandidate(&original, forward.Plan) != nil {
				return bad()
			}
		} else {
			if forward.Previous.Previous.Previous != nil || validateManagedApplicationThirdInterruptedJournal(forward.Plan, forward.Journal) != nil || validateManagedApplicationHistoricalAppRoleCandidate(&original, forward.Plan) != nil {
				return bad()
			}
		}
	}
	if forward.Plan.Environment != partial.Plan.Environment || forward.Plan.Stack != partial.Plan.Stack || forward.Plan.ConfigurationSHA256 != partial.Plan.ConfigurationSHA256 || forward.Plan.ReleaseSHA256 != partial.Plan.ReleaseSHA256 {
		return bad()
	}
	return nil
}

func managedApplicationPartialCurrentPlan(partial *createManagedApplicationPartialRecovery) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	if partial == nil || validateManagedApplicationPartialJournal(partial.Plan, partial.Journal) != nil {
		return nil, deploymentplan.Journal{}, createProviderRecoveryBlock("original application partial plan or journal is unavailable")
	}
	if partial.Interrupted != nil {
		if err := validateManagedApplicationInterruptedForward(partial, partial.Interrupted); err != nil {
			return nil, deploymentplan.Journal{}, err
		}
		return partial.Interrupted.Plan, partial.Interrupted.Journal, nil
	}
	return partial.Plan, partial.Journal, nil
}

func loadManagedApplicationInterruptedSource(store secretStore, e *environmentCreateExecution) (*environmentCreateExecution, createFoundationRecoveryArtifact, *createManagedApplicationInterruptedForward, error) {
	return loadManagedApplicationInterruptedSourceDepth(store, e, 3)
}

func loadManagedApplicationInterruptedSourceDepth(store secretStore, e *environmentCreateExecution, remaining int) (*environmentCreateExecution, createFoundationRecoveryArtifact, *createManagedApplicationInterruptedForward, error) {
	var empty createFoundationRecoveryArtifact
	bad := func() (*environmentCreateExecution, createFoundationRecoveryArtifact, *createManagedApplicationInterruptedForward, error) {
		return nil, empty, nil, createProviderRecoveryBlock("interrupted application continuation requires its exact admitted source, candidate, two-entry suffix and one child lease")
	}
	review := managedApplicationInterruptedReviewDepth(e.Journal, remaining)
	if review == "" {
		return bad()
	}
	a, source, err := loadAdmittedCreateRecovery(store, e, review)
	if err != nil {
		return bad()
	}
	latest := managedApplicationPartialReview(source) == ""
	size, schema := 2, "rtk-cloud.create-managed-application-partial-recovery/v1"
	if latest {
		size, schema = 3, "rtk-cloud.create-managed-application-interrupted-recovery/v1"
		if managedApplicationInterruptedReviewDepth(source, 1) == "" {
			size, schema = 2, "rtk-cloud.create-managed-application-app-role-recovery/v1"
		}
	}
	if a.Plan.Schema != schema || latest && managedApplicationInterruptedReviewDepth(source, remaining-1) == "" || len(e.Journal.Entries) != len(source.Entries)+size || len(e.Journal.KubernetesLeases) != len(source.KubernetesLeases)+1 {
		return bad()
	}
	prefix := e.Journal
	prefix.Entries = e.Journal.Entries[:len(source.Entries)]
	prefix.KubernetesLeases = e.Journal.KubernetesLeases[:len(source.KubernetesLeases)]
	if deploymentplan.Digest(prefix) != deploymentplan.Digest(source) {
		return bad()
	}
	historical := *e
	historical.Journal = source
	originalExecution := &historical
	var previous *createManagedApplicationInterruptedForward
	if latest {
		originalExecution, _, previous, err = loadManagedApplicationInterruptedSourceDepth(store, &historical, remaining-1)
		if err != nil {
			return bad()
		}
		if previous.Previous != nil {
			originalExecution, _, _, err = loadManagedApplicationInterruptedSourceDepth(store, originalExecution, remaining-2)
			if err != nil {
				return bad()
			}
		}
	}
	_, _, original, err := loadManagedApplicationPartialSource(store, originalExecution)
	if err != nil || a.Plan.Materials["accepted-service-core-managed-application-plan"] != original.Plan.SHA256() || a.Plan.Materials["accepted-service-core-managed-application-journal"] != deploymentplan.Digest(original.Journal) || a.Plan.Materials["accepted-service-core-managed-application-source"] != original.SourceSHA || a.Plan.Materials["accepted-service-core-managed-application-admission"] != original.ArtifactSHA {
		return bad()
	}
	if previous != nil {
		if a.Plan.Materials["accepted-service-core-managed-application-interrupted-plan"] != previous.Plan.SHA256() || a.Plan.Materials["accepted-service-core-managed-application-interrupted-journal"] != deploymentplan.Digest(previous.Journal) || a.Plan.Materials["accepted-service-core-managed-application-interrupted-source"] != previous.SourceSHA || a.Plan.Materials["accepted-service-core-managed-application-interrupted-admission"] != previous.ArtifactSHA {
			return bad()
		}
		previous.LeaseSHA = a.Plan.Materials[managedApplicationInterruptedLeaseMaterial]
		owner := source.KubernetesLeases[len(source.KubernetesLeases)-1]
		intent := managedApplicationInterruptedLeaseIntent{Review: review, SourceReview: previous.Review, SourceSHA: previous.SourceSHA, PlanSHA: previous.Plan.SHA256(), Lease: owner, LeaseSHA: previous.LeaseSHA}
		if size == 3 {
			release := e.Journal.Entries[len(source.Entries)+1]
			var receipt managedApplicationInterruptedLeaseReceipt
			if !environmentPreflightDigest.MatchString(previous.LeaseSHA) || owner.ResourceID != "Lease/"+source.Stack+"-video-cloud/rtk-deployment-execution" || !environmentBootstrapOwnerUUID.MatchString(owner.UID) || !strings.HasPrefix(owner.Holder, previous.Plan.SHA256()+":") || release.Key != "managed-application-interrupted-lease-release-"+review || release.IntentSHA256 != activeTransportJournalDigest(intent) || decodeEnvironmentJSON(release.Receipt, &receipt) != nil || !receipt.Absent || receipt.Lease != owner || receipt.LeaseSHA != previous.LeaseSHA {
				return bad()
			}
		} else if previous.Previous == nil || previous.LeaseSHA != deploymentplan.Digest(deploymentplan.Object(nil)) {
			return bad() // This actual third admission observed absence and performed no release.
		}
		original.Interrupted = previous
	}
	lease := e.Journal.KubernetesLeases[len(source.KubernetesLeases)]
	sha := strings.SplitN(lease.Holder, ":", 2)[0]
	if !environmentPreflightDigest.MatchString(sha) || lease.UID == "" || lease.ResourceID != "Lease/"+source.Stack+"-video-cloud/rtk-deployment-execution" {
		return bad()
	}
	var plan, preview deploymentplan.Plan
	var journal deploymentplan.Journal
	path := filepath.Join(store.Root, "deployment/rollback", sha)
	if readExecutionJSON(filepath.Join(path, "plan.json"), &plan) != nil || readExecutionJSON(filepath.Join(path, "journal.json"), &journal) != nil || plan.SHA256() != sha || readExecutionJSON(filepath.Join(store.Root, "deployment/create/openbao-recovery", review, "openbao-root-trust-plan.json"), &preview) != nil || preview.SHA256() != a.Plan.Materials["accepted-service-core-managed-application-forward-plan"] {
		return bad()
	}
	normalized := plan
	normalized.BindingsSHA256 = preview.BindingsSHA256
	if normalized.SHA256() != preview.SHA256() {
		return bad()
	}
	forward := &createManagedApplicationInterruptedForward{Previous: previous, Review: review, SourceSHA: deploymentplan.Digest(source), ArtifactSHA: deploymentplan.Digest(a), Plan: &plan, Journal: journal}
	if validateManagedApplicationInterruptedForward(original, forward) != nil {
		return bad()
	}
	// Stage bindings hash the authentic raw definitions. Compile adds retained
	// PVC volumeName/Service allocations to Desired, so compiled bodies cannot
	// reconstruct that input hash without changing its original meaning.
	definitions, definitionErr := managedApplicationPartialDefinitionsWithRepairs(original, previous != nil, previous != nil && previous.Previous != nil)
	if definitionErr != nil || plan.BindingsSHA256 != environmentCreateStageBindingsSHA(strings.TrimPrefix(e.Journal.Entries[len(e.Journal.Entries)-1].Key, "kubernetes/"), definitions) {
		return bad()
	}
	intent, err := environmentCreateKubernetesStageIntent(&plan)
	hash, hashErr := environmentCreatePlanSHA256(intent)
	if err != nil || hashErr != nil || hash != e.Journal.Entries[len(e.Journal.Entries)-1].IntentSHA256 {
		return bad()
	}
	return &historical, a, forward, nil
}

func loadManagedApplicationInterruptedServiceCore(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan) (*createServiceSessionRecovery, error) {
	source, _, forward, err := loadManagedApplicationInterruptedSource(store, e)
	if err != nil {
		return nil, err
	}
	history := source
	for remaining := 2; managedApplicationPartialReview(history.Journal) == "" && remaining > 0; remaining-- {
		history, _, _, err = loadManagedApplicationInterruptedSourceDepth(store, history, remaining)
		if err != nil {
			return nil, err
		}
	}
	r, err := deriveManagedApplicationPartialRecovery(cfg, store, history, p)
	if err != nil {
		return nil, err
	}
	r.ManagedApplicationPartial.Interrupted = forward
	p.Materials["accepted-service-core-managed-application-interrupted-source"] = forward.SourceSHA
	p.Materials["accepted-service-core-managed-application-interrupted-admission"] = forward.ArtifactSHA
	p.Materials["accepted-service-core-managed-application-interrupted-plan"] = forward.Plan.SHA256()
	p.Materials["accepted-service-core-managed-application-interrupted-journal"] = deploymentplan.Digest(forward.Journal)
	if err := verifyManagedApplicationPartialCurrent(ctx, cfg, store, e, b, r, p); err != nil {
		return nil, err
	}
	if err := prepareManagedApplicationPartialCandidate(ctx, cfg, e, b, p, r); err != nil {
		return nil, err
	}
	return r, e.Check(ctx)
}

func managedApplicationInterruptedNativeCheckpoint(store secretStore, e *environmentCreateExecution) bool {
	source, _, _, err := loadManagedApplicationInterruptedSource(store, e)
	return err == nil && (managedApplicationPartialNativeCheckpoint(store, source) || managedApplicationInterruptedNativeCheckpoint(store, source))
}

// Journal selection admits only the original application alias and the exact
// one or two named forwards in this bounded source shape. Full loaders then
// independently authenticate each admission, child intent and native owner.
func managedApplicationPendingForwardAlias(j environmentCreateJournal, key string) bool {
	before := j
	for remaining := 3; remaining > 0; remaining-- {
		review := managedApplicationInterruptedReviewDepth(before, remaining)
		if review == "" {
			return false
		}
		if key == "kubernetes/managed-application-owners" || key == "kubernetes/managed-application-owners-forward-"+review {
			return true
		}
		size := 2
		if before.Entries[len(before.Entries)-2].Key == "managed-application-interrupted-lease-release-"+review {
			size = 3
		}
		before.Entries = before.Entries[:len(before.Entries)-size]
	}
	return false
}
