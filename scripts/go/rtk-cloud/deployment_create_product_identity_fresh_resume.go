package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This binds the original product program to one already admitted continuation.
// A missing program never authorizes selecting a different release or seed.
type environmentFreshProductResumeBinding struct {
	SelectedPlanSHA256, SessionReviewSHA256, CustodyStage, CustodyPlanSHA256 string
}

func qualifyFreshProductResume(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, store secretStore, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony) (*environmentFreshProductResumeBinding, *environmentCreatePlan, error) {
	r := runtime.ServiceSessionRecovery
	if r == nil {
		return nil, nil, nil
	}
	bad := func(boundary string) (*environmentFreshProductResumeBinding, *environmentCreatePlan, error) {
		return nil, nil, freshProductResumeBlock(boundary)
	}
	e := runtime.Execution
	if ctx.Err() != nil {
		return nil, nil, ctx.Err()
	}
	if e == nil || e.Store != store || runtime.Backend.Store != store || runtime.ExecutionSHA256 != e.Journal.PlanSHA256 || cfg.Environment != store.Environment || e.Check(ctx) != nil {
		return bad("runtime-owner")
	}
	if cfg.Values["PKI_STATE_BACKEND"] != "openbao" || !r.SecondSession || !r.Renewal || r.qualifiedPlan == nil || r.RenewalCandidate == nil || r.OpenBaoRoot == nil {
		return bad("recovery-session-context")
	}
	if runtime.Custody == nil || runtime.Custody.Generation != e.Journal.PlanSHA256 {
		return bad("custody-generation")
	}
	if runtime.Custody.ServiceRootSHA256 != s.Root.RootFingerprint {
		return bad("custody-service-root")
	}
	original, _, err := loadEnvironmentCreateAdmittedGeneration(store, e.Journal.PlanSHA256, e.Journal.ArtifactSHA256)
	if err != nil {
		return nil, nil, err
	}
	selected, err := qualifiedSecondSessionPlan(store, e, r.qualifiedPlan, r.RenewalCandidate)
	if err != nil {
		return bad("selected-plan")
	}
	if err := freshProductResumeSelectedInputs(cfg, p, release, selected); err != nil {
		return nil, nil, err
	}
	head, review, err := sessionLineageHead(store, e)
	if err != nil || head == nil || review != r.Review || head.Renewal.KeySHA != r.OriginalKeySHA || head.Renewal.CSRSHA != r.OriginalCSRSHA || deploymentplan.Digest(head.Renewal.RenewedIntent) != deploymentplan.Digest(r.RenewedIntent) || s.Root.RootFingerprint != head.Renewal.RenewedIntent.RootSHA || s.Intermediate.ID != head.Renewal.RenewedIntent.IssuerID {
		return bad("completed-head-binding")
	}
	if err := freshProductResumeCeremonyMetadata(s, r.Ceremony); err != nil {
		return nil, nil, err
	}
	s, err = freshProductResumeCurrentCeremony(store, e, *head, review, s)
	if err != nil {
		return nil, nil, err
	}
	stage := "service-custody-reader-renewal-" + review
	custody, _, err := managedTransportPostAcceptedStage(store, e, "kubernetes/"+stage)
	if err != nil {
		return nil, nil, err
	}
	binding := &environmentFreshProductResumeBinding{SelectedPlanSHA256: deploymentplan.Digest(selected), SessionReviewSHA256: review, CustodyStage: stage, CustodyPlanSHA256: custody.SHA256()}
	l := createNormalTailProductLifecycle{ParentSHA256: e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(e.owner), Bootstrap: p, Policy: findEnvironmentCreateDomainPolicy(p, "service"), Root: s.Root, Intermediate: s.Intermediate, FreshResume: binding}
	saved := &environmentFreshProductProgram{Lifecycle: l, SelectedPlan: &selected}
	if err := authenticateFreshProductResume(store, e, original, saved); err != nil {
		return nil, nil, err
	}
	subjects, err := environmentCreatePlannedServiceSubjects(cfg, p)
	core, coreErr := environmentCreateBootstrapSubjects(cfg)
	if err != nil || coreErr != nil || len(original.Bootstrap.ProductIdentities) == 0 || len(subjects) != len(core)+len(original.Bootstrap.ProductIdentities) || !slices.Equal(subjects, head.Renewal.RenewedIntent.Subjects) {
		return bad("original-roster")
	}
	state, _, err := readManagedSessionState(ctx, cfg, runtime, s.BootstrapSessionID)
	now := time.Now().UTC()
	if err != nil {
		return bad("current-session-read")
	}
	if !issuerEnrollmentSessionUsable(head.Renewal.RenewedIntent, state, now) {
		return bad("current-session-state")
	}
	if !sessionExistingConsumersACKed(*head, state, now) {
		return bad("current-session-acks")
	}
	// Reuse the same original key/CSR, credential, Job and managed trust observer
	// as native session continuation, without issuing or acknowledging anything.
	if err := observeSecondSessionSuccessor(ctx, cfg, store, e, runtime.Backend, selected, r.Ceremony, *r.OpenBaoRoot, review, now); err != nil {
		return nil, nil, err
	}
	policy, err := prepareEnvironmentCreateProductIdentityLifecycle(ctx, cfg, p, runtime, s)
	if err != nil {
		return bad("original-policy-read")
	}
	if policy.RequiresSuccessor {
		return bad("original-policy-successor")
	}
	if deploymentplan.Digest(policy.Policy) != deploymentplan.Digest(l.Policy) {
		return bad("original-policy")
	}
	if len(policy.Outputs) != len(p.ProductIdentities) {
		return bad("original-output-count")
	}
	for i, output := range policy.Outputs {
		output.RootID, output.RootSHA256, output.IssuerID = "", "", ""
		if deploymentplan.Digest(output) != deploymentplan.Digest(original.Bootstrap.ProductIdentities[i]) {
			return bad("original-output-definition")
		}
	}
	// Return a private snapshot, not aliases to a mutable release/image map.
	var frozen environmentCreatePlan
	if json.Unmarshal(mustProviderRecoveryJSON(selected), &frozen) != nil {
		return bad("selected-snapshot")
	}
	return binding, &frozen, e.Check(ctx)
}

func authenticateFreshProductResume(store secretStore, e *environmentCreateExecution, original environmentCreatePlan, saved *environmentFreshProductProgram) error {
	if saved == nil {
		return freshProductResumeBlock("saved-program")
	}
	binding := saved.Lifecycle.FreshResume
	if binding == nil {
		if saved.SelectedPlan != nil {
			return freshProductResumeBlock("unbound-selected-plan")
		}
		return nil
	}
	if e == nil || e.Store != store || e.Check(context.Background()) != nil || saved.SelectedPlan == nil || saved.Lifecycle.ParentSHA256 != e.Journal.PlanSHA256 || saved.Lifecycle.OwnerSHA256 != deploymentplan.Digest(e.owner) {
		return freshProductResumeBlock("original-owner")
	}
	first, firstReview, effectiveRelease, err := freshProductResumeAdmission(store, e)
	if err != nil {
		return err
	}
	selected := *saved.SelectedPlan
	if err := verifyFreshProductResumeSelection(original, selected, *binding, effectiveRelease); err != nil {
		return err
	}
	l := &saved.Lifecycle
	if deploymentplan.Digest(l.Bootstrap) != deploymentplan.Digest(selected.Bootstrap) || deploymentplan.Digest(l.Policy) != deploymentplan.Digest(findEnvironmentCreateDomainPolicy(original.Bootstrap, "service")) || l.RequiresSuccessor {
		return freshProductResumeBlock("original-declarations")
	}
	source, err := freshProductResumeSourceAdmission(store, e, binding.SessionReviewSHA256)
	if err != nil {
		return err
	}
	if source.Renewal.OriginalReview != firstReview || source.Renewal.Candidate.ReleaseSHA256 != effectiveRelease || source.Renewal.Candidate.ConfigurationSHA256 != selected.ConfigurationSHA256 || source.Renewal.KeySHA != first.PartialSession.KeySHA || source.Renewal.CSRSHA != first.PartialSession.CSRSHA || source.Renewal.RenewedIntent.RootSHA != l.Root.RootFingerprint || source.Renewal.RenewedIntent.IssuerID != l.Intermediate.ID || l.Intermediate.RootFingerprint != l.Root.RootFingerprint {
		return freshProductResumeBlock("completed-session")
	}
	_, err = freshProductResumeCustody(store, e, *binding, source)
	return err
}

func normalizeFreshProductSourcePrefix(store secretStore, e *environmentCreateExecution, saved *environmentFreshProductProgram, prefix environmentCreateJournal) (environmentCreateJournal, error) {
	if saved == nil {
		return prefix, freshProductResumeBlock("source-program")
	}
	if saved.Lifecycle.FreshResume == nil {
		return prefix, nil
	}
	first, _, release, err := freshProductResumeAdmission(store, e)
	if err != nil || saved.SelectedPlan == nil || deploymentplan.Digest(saved.SelectedPlan.Release) != release || deploymentplan.Digest(saved.SelectedPlan) != saved.Lifecycle.FreshResume.SelectedPlanSHA256 {
		return prefix, freshProductResumeBlock("source-admission")
	}
	var original *environmentCreateJournalEntry
	for _, entry := range first.PartialSession.SourceJournal.Entries {
		if entry.Phase == "continuation" && strings.HasPrefix(entry.Key, issuerEnrollmentContinuationPrefix) {
			if original != nil {
				return prefix, freshProductResumeBlock("ambiguous-original-continuation")
			}
			copy := entry
			original = &copy
		}
	}
	if original == nil {
		return prefix, freshProductResumeBlock("original-continuation")
	}
	return normalizeFreshProductResumePrefix(prefix, *original, e.Journal.PlanSHA256, release, saved.Lifecycle.SourceSHA256)
}

func verifyFreshProductResumeSelection(original, selected environmentCreatePlan, binding environmentFreshProductResumeBinding, admittedRelease string) error {
	bad := func() error { return freshProductResumeBlock("admitted-selected-plan") }
	if !environmentPreflightDigest.MatchString(binding.SelectedPlanSHA256) || !environmentPreflightDigest.MatchString(binding.SessionReviewSHA256) || !environmentPreflightDigest.MatchString(binding.CustodyPlanSHA256) || binding.CustodyStage != "service-custody-reader-renewal-"+binding.SessionReviewSHA256 || deploymentplan.Digest(selected) != binding.SelectedPlanSHA256 || deploymentplan.Digest(selected.Release) != admittedRelease || original.ConfigurationSHA256 != selected.ConfigurationSHA256 || original.Environment != selected.Environment || original.Stack != selected.Stack || deploymentplan.Digest(original.Bootstrap.ProductIdentities) != deploymentplan.Digest(selected.Bootstrap.ProductIdentities) || deploymentplan.Digest(findEnvironmentCreateDomainPolicy(original.Bootstrap, "service")) != deploymentplan.Digest(findEnvironmentCreateDomainPolicy(selected.Bootstrap, "service")) {
		return bad()
	}
	if deploymentplan.Digest(original) == binding.SelectedPlanSHA256 {
		return nil
	}
	images := map[string]environmentReleaseImage{}
	for role, before := range original.Release.Images {
		after, exists := selected.Release.Images[role]
		if !exists {
			return bad()
		}
		if deploymentplan.Digest(before) == deploymentplan.Digest(after) {
			continue
		}
		name, official := strings.CutPrefix(strings.Split(before.Reference, "@")[0], "ghcr.io/hkt999rtk/rtk_video_cloud/")
		if !official || !slices.Contains([]string{"video-cloud-api", "video-cloud-emqx-pki", "video-cloud-openbao-pki"}, name) {
			return bad()
		}
		images[name] = after
	}
	rebuilt, _, err := issuerEnrollmentForwardPlan(original, images)
	if err != nil || deploymentplan.Digest(rebuilt) != binding.SelectedPlanSHA256 {
		return bad()
	}
	return nil
}

func normalizeFreshProductResumePrefix(prefix environmentCreateJournal, original environmentCreateJournalEntry, parent, release, sourceSHA string) (environmentCreateJournal, error) {
	bad := func() (environmentCreateJournal, error) {
		return prefix, freshProductResumeBlock("original-source-prefix")
	}
	if original.State != "pending" || original.Phase != "continuation" || !strings.HasPrefix(original.Key, issuerEnrollmentContinuationPrefix) || !environmentPreflightDigest.MatchString(strings.TrimPrefix(original.Key, issuerEnrollmentContinuationPrefix)) || !partialSessionEntryAdvances(original, original) || prefix.PlanSHA256 != parent || !environmentPreflightDigest.MatchString(release) {
		return bad()
	}
	copy := prefix
	copy.Entries = slices.Clone(prefix.Entries)
	found := false
	for i, entry := range copy.Entries {
		if entry.Key != original.Key {
			continue
		}
		if found || entry.Phase != original.Phase || entry.IntentSHA256 != original.IntentSHA256 || !entry.StartedAt.Equal(original.StartedAt) || !partialSessionEntryAdvances(original, entry) {
			return bad()
		}
		found = true
		if entry.State != "pending" {
			var receipt struct {
				Accepted bool   `json:"accepted"`
				Parent   string `json:"original_parent"`
				Release  string `json:"release_sha256"`
			}
			if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || !receipt.Accepted || receipt.Parent != parent || receipt.Release != release {
				return bad()
			}
		}
		copy.Entries[i] = original
	}
	if !found || deploymentplan.Digest(copy) != sourceSHA {
		return bad()
	}
	return copy, nil
}

func freshProductResumeAdmission(store secretStore, e *environmentCreateExecution) (issuerEnrollmentContinuationProof, string, string, error) {
	first, review, exists, err := loadPartialSessionAdmission(store, e)
	if err != nil || !exists || first.PartialSession == nil {
		return first, "", "", freshProductResumeBlock("original-partial-admission")
	}
	release := first.Release
	forward, _, forwarded, err := loadPartialSessionTrustForwardAdmission(store, e, first, review)
	if err != nil {
		return first, "", "", err
	}
	if forwarded {
		release = forward.Release
	}
	if !environmentPreflightDigest.MatchString(release) {
		return first, "", "", freshProductResumeBlock("original-release")
	}
	return first, review, release, nil
}

// The completed session's reader is the same original native owner. Its exact
// accepted stage is pinned even after product finalization removes credentials.
func freshProductResumeCustody(store secretStore, e *environmentCreateExecution, binding environmentFreshProductResumeBinding, q secondSessionAdmission) (*deploymentplan.Plan, error) {
	bad := func() (*deploymentplan.Plan, error) { return nil, freshProductResumeBlock("completed-custody-child") }
	if q.Renewal.Candidate == nil || !environmentPreflightDigest.MatchString(q.Renewal.BindingSHA) || binding.CustodyStage != "service-custody-reader-renewal-"+binding.SessionReviewSHA256 {
		return bad()
	}
	p, j, err := managedTransportPostAcceptedStage(store, e, "kubernetes/"+binding.CustodyStage)
	if err != nil || p.SHA256() != binding.CustodyPlanSHA256 || len(p.Resources) != 1 || p.ConfigurationSHA256 != q.Renewal.Candidate.ConfigurationSHA256 || p.ReleaseSHA256 != q.Renewal.Candidate.ReleaseSHA256 {
		return bad()
	}
	checked, exists, err := readEnvironmentCreateStageProgress(store, e, binding.CustodyStage, p)
	if err != nil || !exists || deploymentplan.Digest(checked) != deploymentplan.Digest(j) {
		return bad()
	}
	r := p.Resources[0]
	containers := certIssuerObjectList(environmentPKIPod(r.Desired)["containers"])
	if r.ID != q.Renewal.PreviousCustodyID || r.Desired["kind"] != "Deployment" || j.Owners[r.ID] != q.Renewal.PreviousCustodyUID || len(containers) != 1 {
		return bad()
	}
	c := deploymentplan.Map(containers[0])
	if mqttFoundationEnv(c, "PKI_BOOTSTRAP_SESSION_ID") != q.Renewal.RenewedIntent.SessionID || mqttFoundationEnv(c, "PKI_BOOTSTRAP_CERT_PATH") != "/run/pki-bootstrap/renew-"+q.Renewal.BindingSHA[:12]+"/bootstrap.crt" {
		return bad()
	}
	return p, e.Check(context.Background())
}

// Preflight may still hold the predecessor ceremony. Derive its already
// completed successor from immutable observation and native credential owners;
// this never calls the session producer or creates a cleanup binding.
func freshProductResumeCurrentCeremony(store secretStore, e *environmentCreateExecution, q secondSessionAdmission, review string, service environmentCreateServiceCeremony) (environmentCreateServiceCeremony, error) {
	bad := func() (environmentCreateServiceCeremony, error) {
		return service, freshProductResumeBlock("observed-current-ceremony")
	}
	if e == nil || e.Store != store || deploymentplan.Digest(q) != review || !validSecondSessionAdmission(store, e, q) || service.BootstrapSessionID != q.Renewal.Intent.SessionID && service.BootstrapSessionID != q.Renewal.RenewedIntent.SessionID || service.Root.RootFingerprint != q.Renewal.RenewedIntent.RootSHA || service.Intermediate.ID != q.Renewal.RenewedIntent.IssuerID {
		return bad()
	}
	done, err := completedServiceSessionBatch(store, e, q, review)
	if err != nil || !done {
		return bad()
	}
	var observed createServiceSessionObserved
	path := filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-renewal", review, "observed.json")
	if readExecutionJSON(path, &observed) != nil || validateEnvironmentBootstrapCleanupBinding(&observed.Cleanup, store.Environment) != nil || observed.Cleanup.SessionID != q.Renewal.RenewedIntent.SessionID || len(observed.UIDs) != len(q.Renewal.RenewedIntent.Namespaces) || "Deployment/"+service.BootstrapCredentialNamespace+"/"+observed.Reader != q.Renewal.PreviousCustodyID || observed.Reader != service.CustodyReader {
		return bad()
	}
	var native struct {
		SessionID   string    `json:"session_id"`
		IssuerID    string    `json:"issuer_id"`
		Root        string    `json:"root_sha256"`
		Fingerprint string    `json:"bootstrap_fingerprint"`
		Status      string    `json:"status"`
		Subjects    []string  `json:"subjects"`
		Expires     time.Time `json:"expires_at"`
	}
	if decodeEnvironmentJSON(observed.Session, &native) != nil || native.SessionID != observed.Cleanup.SessionID || native.IssuerID != service.Intermediate.ID || native.Root != service.Root.RootFingerprint || !environmentPreflightDigest.MatchString(native.Fingerprint) || native.Status != "active" || !slices.Equal(native.Subjects, q.Renewal.RenewedIntent.Subjects) || native.Expires.IsZero() {
		return bad()
	}
	stage := "service-credential-secrets-renewal-" + review
	plan, journal, err := managedTransportPostAcceptedStage(store, e, "kubernetes/"+stage)
	if err != nil {
		return bad()
	}
	checked, exists, err := readEnvironmentCreateStageProgress(store, e, stage, plan)
	if err != nil || !exists || deploymentplan.Digest(checked) != deploymentplan.Digest(journal) {
		return bad()
	}
	for _, ns := range q.Renewal.RenewedIntent.Namespaces {
		id := "Secret/" + ns + "/service-bootstrap-" + native.SessionID
		if !environmentBootstrapOwnerUUID.MatchString(observed.UIDs[ns]) || observed.UIDs[ns] != journal.Owners[id] {
			return bad()
		}
	}
	if observed.Cleanup.SecretUID != observed.UIDs[service.BootstrapCredentialNamespace] {
		return bad()
	}
	copy := service
	copy.BootstrapSessionID, copy.BootstrapCredentialSecret = native.SessionID, "service-bootstrap-"+native.SessionID
	copy.BootstrapCredentialNamespaces = slices.Clone(q.Renewal.RenewedIntent.Namespaces)
	copy.CredentialUIDs, copy.CleanupBinding = observed.UIDs, observed.Cleanup
	copy.CleanupBinding.ConfigMapName = "pki-bootstrap-cleanup-binding-" + native.SessionID[:12]
	return copy, e.Check(context.Background())
}

func freshProductResumeBlock(boundary string) error {
	return environmentCreateBootstrapBlocker("original fresh product resume authority differs: " + boundary)
}

// These comparisons retain the exact qualified inputs while exposing only a
// constant boundary when preflight and execution supply different contexts.
func freshProductResumeSelectedInputs(cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, selected environmentCreatePlan) error {
	if selected.ConfigurationSHA256 != deploymentplan.Digest(cfg) {
		return freshProductResumeBlock("selected-configuration")
	}
	if deploymentplan.Digest(selected.Bootstrap) != deploymentplan.Digest(p) {
		return freshProductResumeBlock("selected-bootstrap")
	}
	if deploymentplan.Digest(selected.Release) != deploymentplan.Digest(release) {
		return freshProductResumeBlock("selected-release")
	}
	return nil
}
func freshProductResumeCeremonyMetadata(service, recorded environmentCreateServiceCeremony) error {
	if deploymentplan.Digest(service.Root) != deploymentplan.Digest(recorded.Root) {
		return freshProductResumeBlock("service-root-metadata")
	}
	if deploymentplan.Digest(service.Intermediate) != deploymentplan.Digest(recorded.Intermediate) {
		return freshProductResumeBlock("service-intermediate-metadata")
	}
	return nil
}

// Resolve the completed admission that originally authorized a literal product
// program. A later installation successor is separate authority.
func freshProductResumeSourceAdmission(store secretStore, e *environmentCreateExecution, review string) (secondSessionAdmission, error) {
	bad := func() (secondSessionAdmission, error) {
		return secondSessionAdmission{}, freshProductResumeBlock("original-session-lineage")
	}
	if e == nil || e.Store != store || !environmentPreflightDigest.MatchString(review) || e.Check(context.Background()) != nil {
		return bad()
	}
	head, currentReview, err := sessionLineageHead(store, e)
	if err != nil || head == nil {
		return bad()
	}
	source := *head
	if currentReview != review {
		_, history, err := serviceSessionPriorLineage(store, e, *head)
		if err != nil {
			return bad()
		}
		found := false
		for _, prior := range history {
			if deploymentplan.Digest(prior) == review {
				source, found = prior, true
				break
			}
		}
		if !found {
			return bad()
		}
	}
	done, err := completedServiceSessionBatch(store, e, source, review)
	if err != nil || !done {
		return secondSessionAdmission{}, freshProductResumeBlock("native-session-completion")
	}
	return source, nil
}
