package main

import (
	"context"
	"encoding/base64"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"time"
)

const managedLatePostMintSchema = "rtk-cloud.create-managed-late-post-mint-recovery/v2"
const managedLatePostMintSourceFilename = "managed-late-post-mint-source.json"
const managedLatePostMintPlanFilename = "managed-late-post-mint-owners-plan.json"
const managedLatePostMintMaterial = "accepted-service-core-late-post-mint-"

type createManagedLatePostMint struct {
	Review, OriginalReview, SeedForwardReview, SourceSHA, SourceOwnerSHA, OriginalArtifactSHA, SeedForwardArtifactSHA string
	NativeStartedAt, NativeDeadline                                                                                   time.Time
	Ceremony                                                                                                          environmentCreateServiceCeremony
	CleanupOwners                                                                                                     map[string]deploymentplan.Resource
	Candidate                                                                                                         *deploymentplan.Plan
	AppRootReview, AppRootArtifactSHA, AppRootCompletionSHA                                                           string
	InstallationPolicy, Fingerprint                                                                                   string
	SessionExpires, LeafNotAfter                                                                                      time.Time
	InstallationClock                                                                                                 *createManagedLateInstallationClock `json:"-"`
}

func managedLatePostMintReview(j environmentCreateJournal) string {
	if len(j.Entries) >= 17 {
		n := len(j.Entries) - 4
		key := j.Entries[n].Key
		review := strings.TrimPrefix(key, "app-root-crl-renewal-")
		if key == "app-root-crl-renewal-"+review && environmentPreflightDigest.MatchString(review) && j.Entries[n+1].Key == key+"/native-import" && j.Entries[n+2].Key == key+"/native-runtime-video-cloud-api-app-pki" && j.Entries[n+3].Key == key+"/native-runtime-mqtt-pki" {
			for _, entry := range j.Entries[n:] {
				if entry.Phase != "bootstrap" || entry.State != "completed" || entry.StartedAt.IsZero() || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || len(entry.Receipt) == 0 {
					return ""
				}
			}
			j.Entries = j.Entries[:n]
		}
	}
	if len(j.Entries) < 13 || !j.CompletedAt.IsZero() {
		return ""
	}
	for _, entry := range j.Entries {
		if entry.State != "completed" || entry.StartedAt.IsZero() || entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || len(entry.Receipt) == 0 {
			return ""
		}
	}
	seed := j.Entries[len(j.Entries)-9]
	const prefix = "kubernetes/service-session-original-key-renewal-"
	review := strings.TrimPrefix(seed.Key, prefix)
	if seed.Key != prefix+review || !environmentPreflightDigest.MatchString(review) || j.Entries[len(j.Entries)-8].Key == "" || !strings.HasPrefix(j.Entries[len(j.Entries)-8].Key, "openbao-recovery-admission-") || j.Entries[len(j.Entries)-4].Key != "kubernetes/service-cleanup-binding-renewal-"+review {
		return ""
	}
	return review
}
func managedLatePostMintDigest(post *createManagedLatePostMint) string {
	if post == nil {
		return ""
	}
	copy := *post
	copy.Review = ""
	return deploymentplan.Digest(copy)
}
func managedLatePostMintBudgetPin(post *createManagedLatePostMint) string {
	return deploymentplan.Digest(deploymentplan.Object{"policy": post.InstallationPolicy, "limit": managedLateOverallLimit.String(), "reserve": managedLateExpiryReserve.String(), "session_expires": post.SessionExpires, "leaf_not_after": post.LeafNotAfter, "fingerprint": post.Fingerprint})
}

// Derive only actual accepted seed/custody/credentials/cleanup history. The old
// failed Job, original descriptors and every real child remain immutable.
func loadManagedLatePostMintSource(cfg deploymentConfig, store secretStore, e *environmentCreateExecution) (*createServiceSessionRecovery, *createManagedLatePostMint, error) {
	return loadManagedLatePostMintSourceView(cfg, store, e, false)
}

func loadManagedLatePostMintSourceView(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, admittedClock bool) (*createServiceSessionRecovery, *createManagedLatePostMint, error) {
	if e == nil {
		return nil, nil, appRootCRLError("selected-post-source")
	}
	if !admittedClock {
		if err := managedLateInstallationClockAbsent(store, e.Journal.PlanSHA256); err != nil {
			return nil, nil, err
		}
	}
	framed, app, err := managedLateAppRootCRLFrame(store, e, true)
	if err != nil {
		return nil, nil, err
	}
	r, post, err := loadManagedLatePostMintSourceContent(cfg, store, framed, admittedClock)
	if err != nil {
		return nil, nil, err
	}
	if app != nil {
		var proof createAppRootCRLCompletion
		if createOpenBaoACKCompletionRead(store, appRootCRLPointer(e.Journal.PlanSHA256), &proof) != nil {
			return nil, nil, appRootCRLError("post-App-completion")
		}
		post.SourceSHA = deploymentplan.Digest(e.Journal)
		post.AppRootReview = deploymentplan.Digest(app.Plan)
		post.AppRootArtifactSHA = deploymentplan.Digest(*app)
		post.AppRootCompletionSHA = deploymentplan.Digest(proof)
	}
	return r, post, nil
}

func loadManagedLatePostMintSourceContent(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, admittedClock bool) (*createServiceSessionRecovery, *createManagedLatePostMint, error) {
	kind := "completed-post-content"
	if admittedClock {
		kind += "/historical-clock"
	}
	var retained struct {
		Recovery *createServiceSessionRecovery
		Post     *createManagedLatePostMint
	}
	if hit, err := readManagedLateSourceReuse(store, e, cfg, kind, &retained); err != nil {
		return nil, nil, err
	} else if hit {
		if retained.Recovery == nil || retained.Post == nil {
			return nil, nil, managedLateSourceReuseBlock()
		}
		retained.Recovery.ManagedLatePostMint = retained.Post
		return retained.Recovery, retained.Post, nil
	}
	r, post, err := deriveManagedLatePostMintSourceContent(cfg, store, e, admittedClock)
	if err != nil {
		return nil, nil, err
	}
	retained.Recovery, retained.Post = r, post
	if err := rememberManagedLateSourceReuse(store, e, cfg, kind, retained); err != nil {
		return nil, nil, err
	}
	return r, post, nil
}

func deriveManagedLatePostMintSourceContent(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, admittedClock bool) (*createServiceSessionRecovery, *createManagedLatePostMint, error) {
	bad := func(stage string) (*createServiceSessionRecovery, *createManagedLatePostMint, error) {
		return nil, nil, environmentCreateBootstrapBlocker("late post-mint source differs: stage=" + stage)
	}
	if e == nil || e.Store.Root != store.Root {
		return bad("selected-source")
	}
	if !admittedClock {
		if err := managedLateInstallationClockAbsent(store, e.Journal.PlanSHA256); err != nil {
			return nil, nil, err
		}
	}
	review := managedLatePostMintReview(e.Journal)
	if review == "" {
		return bad("literal-completed-cleanup-checkpoint")
	}
	original, source, err := loadAdmittedCreateRecovery(store, e, review)
	if err != nil || original.Plan.Schema != managedLateSessionSchema || len(e.Journal.Entries) != len(source.Entries)+13 || len(e.Journal.KubernetesLeases) != len(source.KubernetesLeases)+7 {
		return bad("exact-thirteen-entry-seven-lease-suffix")
	}
	r, err := loadManagedLateAdmittedHistoricalSource(cfg, store, e, review)
	if err != nil {
		return nil, nil, err
	}
	late := r.ManagedLateSession
	children, _, _, err := managedLateCurrentChildren(store, e, late)
	if err != nil {
		return nil, nil, err
	}
	forward, _, _, err := managedLateSeedForwardFrame(store, e, late, source)
	if err != nil || forward == nil {
		return bad("genuine-accepted-seed-forward")
	}
	admitted, _, err := loadAdmittedCreateRecovery(store, e, forward.Review)
	if err != nil || admitted.Plan.Schema != managedLateSeedForwardSchema {
		return bad("original-forward-clock")
	}
	cleanupKey := "kubernetes/service-cleanup-binding-renewal-" + review
	credentialsKey := "kubernetes/service-credential-secrets-renewal-" + review
	custodyKey := "kubernetes/service-custody-reader-renewal-" + review
	for _, key := range []string{custodyKey, credentialsKey, cleanupKey} {
		child, ok := children[key]
		if !ok || child.Entry.State != "completed" || !createOpenBaoACKCompletedChild(child.Plan, child.Journal) {
			return bad("accepted-post-mint-child")
		}
	}
	outer := createOpenBaoACKCompletionEntry(e.Journal, "late-cleanup-authorization-"+review)
	roleKey, _, roleErr := managedLateAuthorizationEntryIntent(late.CleanupAuthorization, "role_intent")
	if outer == nil || roleErr != nil || e.Journal.Entries[len(e.Journal.Entries)-1].Key != roleKey || !managedLateAuthorizationCompleted(e.Journal, late, "late-cleanup-authorization", late.CleanupAuthorization) {
		return bad("completed-cleanup-authorization")
	}
	var observed createServiceSessionObserved
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create", late.ParentSHA, "bootstrap/service/session-renewal", review, "observed.json"), &observed) != nil || validateManagedLateAllocatedCredentialProof(cfg, late, children[credentialsKey].Plan, children[credentialsKey].Journal, observed, outer.CompletedAt) != nil {
		return bad("genuine-current-ceremony")
	}
	cleanupOwners, err := managedLateAcceptedCleanupRBAC(late, children)
	if err != nil || len(cleanupOwners) != 4 {
		return bad("accepted-original-cleanup-owners")
	}
	ceremony := late.SourceCeremony
	ceremony.BootstrapSessionID = late.NewSessionID
	ceremony.BootstrapCredentialSecret = "service-bootstrap-" + late.NewSessionID
	ceremony.CleanupBinding = observed.Cleanup
	ceremony.CredentialUIDs = observed.UIDs
	post := &createManagedLatePostMint{OriginalReview: review, SeedForwardReview: forward.Review, SourceSHA: deploymentplan.Digest(e.Journal), SourceOwnerSHA: deploymentplan.Digest(e.owner), OriginalArtifactSHA: deploymentplan.Digest(original), SeedForwardArtifactSHA: deploymentplan.Digest(admitted), InstallationPolicy: managedLateInstallationPolicy, Ceremony: ceremony, CleanupOwners: cleanupOwners}
	var allocated managedSessionState
	if decodeEnvironmentJSON(observed.Session, &allocated) != nil || allocated.Session != late.NewSessionID || allocated.Status != "active" || len(allocated.Installed) != 0 || !environmentPreflightDigest.MatchString(allocated.Fingerprint) {
		return bad("same-native-session-existing-cutoff-reserve")
	}
	cert, certErr := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(deploymentplan.Map(children[credentialsKey].Plan.Resources[0].Desired["data"])["tls.crt"]))
	leaf, leafErr := parseFirstCertificatePEM(cert)
	if certErr != nil || leafErr != nil || leaf == nil || !leaf.NotAfter.Equal(allocated.Expires) || environmentPreflightBytesSHA256(leaf.Raw) != allocated.Fingerprint {
		return bad("accepted-native-certificate-expiry")
	}
	post.SessionExpires, post.LeafNotAfter, post.Fingerprint = allocated.Expires, leaf.NotAfter, allocated.Fingerprint
	r.ManagedLateSeedForward = forward
	r.ManagedLatePostMint = post
	r.Renewal = true
	r.Ceremony = ceremony
	r.Review = review
	return r, post, nil
}

func managedLatePostMintOwnerSource(late *createManagedLateSessionRecovery, post *createManagedLatePostMint) (*createManagedLateSessionRecovery, error) {
	if late == nil || post == nil || post.OriginalReview != late.Review || post.Ceremony.BootstrapSessionID != late.NewSessionID || len(post.CleanupOwners) != 4 {
		return nil, environmentCreateBootstrapBlocker("late post-mint owner authority absent")
	}
	copy := *late
	copy.DependencyOwners = map[string]deploymentplan.Resource{}
	for id, owner := range late.DependencyOwners {
		copy.DependencyOwners[id] = owner
	}
	ns := late.AcceptedCore.Stack + "-video-cloud"
	expected := []string{"Role/" + ns + "/bootstrap-cleanup", "RoleBinding/" + ns + "/bootstrap-cleanup", "ClusterRole//" + late.AcceptedCore.Stack + "-bootstrap-cleanup", "ClusterRoleBinding//" + late.AcceptedCore.Stack + "-bootstrap-cleanup"}
	for id, owner := range post.CleanupOwners {
		if !slices.Contains(expected, id) || owner.ID != id || !environmentBootstrapOwnerUUID.MatchString(owner.UID) {
			return nil, environmentCreateBootstrapBlocker("late post-mint foreign cleanup owner")
		}
		if prior, exists := copy.DependencyOwners[id]; exists {
			if prior.UID != owner.UID || !deploymentplan.Matches(prior.Desired, owner.Baseline) {
				return nil, environmentCreateBootstrapBlocker("late post-mint cleanup supersession changed")
			}
			copy.DependencyOwners[id] = owner
		}
	}
	copy.OwnerCandidate = post.Candidate
	return &copy, nil
}
func validateManagedLatePostMintCandidate(late *createManagedLateSessionRecovery, post *createManagedLatePostMint, candidate *deploymentplan.Plan) error {
	copy, err := managedLatePostMintOwnerSource(late, post)
	if err != nil {
		return err
	}
	return validateManagedLateSessionOwnerCandidate(copy, candidate)
}
func managedLatePostMintDefinitions(late *createManagedLateSessionRecovery, post *createManagedLatePostMint) ([]deploymentplan.Definition, error) {
	copy, err := managedLatePostMintOwnerSource(late, post)
	if err != nil {
		return nil, err
	}
	defs, err := managedLateSessionOwnerDefinitions(copy, post.Ceremony)
	if err != nil {
		return nil, err
	}
	return managedLateDefinitionClosure(defs, copy.DependencyOwners)
}
func managedLatePostMintPins(post *createManagedLatePostMint) map[string]string {
	return map[string]string{"source": post.SourceSHA, "descriptor": managedLatePostMintDigest(post), "candidate": post.Candidate.SHA256(), "ceremony": deploymentplan.Digest(post.Ceremony), "original-review": post.OriginalReview, "seed-forward-review": post.SeedForwardReview, "original-artifact": post.OriginalArtifactSHA, "seed-forward-artifact": post.SeedForwardArtifactSHA, "native-budget": managedLatePostMintBudgetPin(post)}
}
func prepareManagedLatePostMint(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan) (*createServiceSessionRecovery, error) {
	r, post, err := loadManagedLatePostMintSource(cfg, store, e)
	if err != nil {
		return nil, err
	}
	if !post.SessionExpires.After(time.Now().UTC().Add(managedLateFinalizerLimit + managedLateExpiryReserve)) {
		return nil, createOpenBaoACKBlock("late-post-mint-existing-finalizer-deadline")
	}
	r.Review = post.OriginalReview
	original, _, err := loadAdmittedCreateRecovery(store, e, post.OriginalReview)
	if err != nil {
		return nil, err
	}
	for key, value := range original.Plan.Materials {
		if prior := p.Materials[key]; prior != "" && prior != value {
			return nil, environmentCreateBootstrapBlocker("late post-mint original material differs")
		}
		p.Materials[key] = value
	}
	p.Plan = original.Plan.Plan
	p.Schema = managedLatePostMintSchema
	ledger, ledgerErr := readEnvironmentCreateLedger(e)
	var active managedTransportVolumeLedgerActive
	if ledgerErr != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/resources/active.json"), &active) != nil || validateManagedTransportVolumeLedger(ledger, active) != nil {
		return nil, environmentCreateBootstrapBlocker("late post-mint current recorded volume authority differs")
	}
	p.Materials["accepted-service-core-managed-application-current-volume-ledger"] = active.LedgerSHA
	p.Materials["accepted-service-core-managed-application-current-volume-active"] = deploymentplan.Digest(active)
	if err := verifyManagedLateSessionCurrentNative(ctx, cfg, store, e, b, r, p, r.ManagedLateSession); err != nil {
		return nil, err
	}
	if err := verifyServiceSessionAuthentication(ctx, cfg, store, e, b, r); err != nil {
		return nil, err
	}
	defs, err := managedLatePostMintDefinitions(r.ManagedLateSession, post)
	if err != nil {
		return nil, err
	}
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return nil, err
	}
	post.Candidate, err = managedLateCompile(p.Plan, "managed-late-session-owners", defs, live)
	if err != nil || validateManagedLatePostMintCandidate(r.ManagedLateSession, post, post.Candidate) != nil {
		return nil, environmentCreateBootstrapBlocker("late post-mint exact remaining owner plan differs")
	}
	r.Candidate = post.Candidate
	for key, value := range managedLatePostMintPins(post) {
		p.Materials[managedLatePostMintMaterial+key] = value
	}
	return r, nil
}
func verifyManagedLatePostMintCurrent(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan) error {
	if r == nil || r.ManagedLatePostMint == nil || validateManagedLatePostMintCandidate(r.ManagedLateSession, r.ManagedLatePostMint, r.ManagedLatePostMint.Candidate) != nil {
		return environmentCreateBootstrapBlocker("late post-mint candidate absent")
	}
	original, _, err := loadAdmittedCreateRecovery(store, e, r.ManagedLatePostMint.OriginalReview)
	if err != nil {
		return err
	}
	for key, value := range original.Plan.Materials {
		p.Materials[key] = value
	}
	ledger, ledgerErr := readEnvironmentCreateLedger(e)
	var active managedTransportVolumeLedgerActive
	if ledgerErr != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/resources/active.json"), &active) != nil || validateManagedTransportVolumeLedger(ledger, active) != nil {
		return environmentCreateBootstrapBlocker("late post-mint current recorded volume authority differs")
	}
	p.Materials["accepted-service-core-managed-application-current-volume-ledger"] = active.LedgerSHA
	p.Materials["accepted-service-core-managed-application-current-volume-active"] = deploymentplan.Digest(active)
	if err := verifyManagedLateSessionCurrentNative(ctx, cfg, store, e, b, r, p, r.ManagedLateSession); err != nil {
		return err
	}
	if err := verifyServiceSessionAuthentication(ctx, cfg, store, e, b, r); err != nil {
		return err
	}
	for key, value := range managedLatePostMintPins(r.ManagedLatePostMint) {
		p.Materials[managedLatePostMintMaterial+key] = value
	}
	return r.ManagedLatePostMint.Candidate.Admit(ctx, b)
}
func loadManagedLatePostMintQualified(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, p *createFoundationRecoveryPlan, review string) (*createServiceSessionRecovery, error) {
	var a createFoundationRecoveryArtifact
	base := filepath.Join("deployment/create/openbao-recovery", review)
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, "qualification.json"), &a) != nil || !managedLateQualifiedArtifactValid(a, e, *p, review, time.Now().UTC(), managedLatePostMintSchema) {
		return nil, environmentCreateBootstrapBlocker("late post-mint requires fresh current-checker sixGO")
	}
	r, loaded, err := loadManagedLatePostMintSource(cfg, store, e)
	if err != nil {
		return nil, err
	}
	var post createManagedLatePostMint
	var preview deploymentplan.Plan
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, managedLatePostMintSourceFilename), &post) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(base, managedLatePostMintPlanFilename), &preview) != nil || post.Review != review || post.Candidate == nil || post.Candidate.SHA256() != preview.SHA256() || validateManagedLatePostMintCandidate(r.ManagedLateSession, &post, post.Candidate) != nil {
		return nil, environmentCreateBootstrapBlocker("late post-mint immutable source preview differs")
	}
	copy := post
	copy.Review = ""
	copy.Candidate = nil
	if managedLatePostMintDigest(&copy) != managedLatePostMintDigest(loaded) {
		return nil, environmentCreateBootstrapBlocker("late post-mint current source changed")
	}
	for key, value := range managedLatePostMintPins(&post) {
		if a.Plan.Materials[managedLatePostMintMaterial+key] != value {
			return nil, environmentCreateBootstrapBlocker("late post-mint qualified material differs")
		}
	}
	*p = a.Plan
	r.ManagedLatePostMint = &post
	r.Candidate = post.Candidate
	return r, nil
}

// Pure parsing removes only the separately qualified post-mint admission. It
// keeps every genuine child, seed-forward alias, failed journal and lease.
func managedLatePostMintRuntimeFrame(store secretStore, e *environmentCreateExecution, late *createManagedLateSessionRecovery) (*environmentCreateExecution, *createManagedLatePostMint, error) {
	if late != nil && late.ExpiredRenewal != nil {
		return e, nil, nil
	}
	bad := func() (*environmentCreateExecution, *createManagedLatePostMint, error) {
		return nil, nil, environmentCreateBootstrapBlocker("late post-mint exact admitted frame differs")
	}
	if e == nil || late == nil {
		return bad()
	}
	original, source, err := loadAdmittedCreateRecovery(store, e, late.Review)
	if err != nil {
		return nil, nil, err
	}
	index := len(source.Entries) + 13
	if len(e.Journal.Entries) > index && strings.HasPrefix(e.Journal.Entries[index].Key, "app-root-crl-renewal-") {
		index += 4
	}
	if len(e.Journal.Entries) <= index || !strings.HasPrefix(e.Journal.Entries[index].Key, "openbao-recovery-admission-") {
		frame, _, frameErr := managedLateAppRootCRLRuntimeParsingView(store, e)
		return frame, nil, frameErr
	}
	review := strings.TrimPrefix(e.Journal.Entries[index].Key, "openbao-recovery-admission-")
	artifact, captured, err := loadAdmittedCreateRecovery(store, e, review)
	var post createManagedLatePostMint
	if err != nil || artifact.Plan.Schema != managedLatePostMintSchema || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, managedLatePostMintSourceFilename), &post) != nil || post.Review != review || post.OriginalReview != late.Review || post.OriginalArtifactSHA != deploymentplan.Digest(original) || len(captured.Entries) != index || post.SourceSHA != deploymentplan.Digest(captured) || post.SourceOwnerSHA != artifact.Plan.ParentOwnerSHA || post.Candidate == nil || !post.NativeDeadline.IsZero() || !post.NativeStartedAt.IsZero() || post.InstallationPolicy != managedLateInstallationPolicy || validateManagedLatePostMintCandidate(late, &post, post.Candidate) != nil {
		return bad()
	}
	for key, value := range managedLatePostMintPins(&post) {
		if artifact.Plan.Materials[managedLatePostMintMaterial+key] != value {
			return bad()
		}
	}
	admission := e.Journal.Entries[index]
	clock, clockErr := validateManagedLateInstallationAdmission(store, e, &post, artifact, admission)
	if clockErr != nil || len(e.Journal.KubernetesLeases) < len(captured.KubernetesLeases) {
		return bad()
	}
	prefix := e.Journal
	prefix.Entries = e.Journal.Entries[:index]
	prefix.KubernetesLeases = e.Journal.KubernetesLeases[:len(captured.KubernetesLeases)]
	if deploymentplan.Digest(prefix) != deploymentplan.Digest(captured) {
		return bad()
	}
	// Authenticate App history with the actual installation admission still
	// present. Only then produce the bounded original-history parsing view.
	appFrame, app, appErr := managedLateAppRootCRLRuntimeParsingView(store, e)
	if appErr != nil {
		return bad()
	}
	parsedIndex := index
	pure := *e
	pure.Journal = captured
	if app != nil {
		parsedIndex -= 4
		if len(captured.KubernetesLeases) == 0 {
			return bad()
		}
		pure.Journal.Entries = slices.Clone(captured.Entries[:parsedIndex])
		pure.Journal.KubernetesLeases = slices.Clone(captured.KubernetesLeases[:len(captured.KubernetesLeases)-1])
	}
	// No prospective or synthetically completed body may replace the original
	// genuinely accepted seed, credentials and cleanup allocations.
	loaded, expected, err := loadManagedLatePostMintSourceContent(late.OpenBaoTLSRepair.Plan.Configuration, store, &pure, true)
	if err != nil || loaded == nil {
		return bad()
	}
	if app != nil {
		var proof createAppRootCRLCompletion
		if createOpenBaoACKCompletionRead(store, appRootCRLPointer(e.Journal.PlanSHA256), &proof) != nil {
			return bad()
		}
		expected.SourceSHA = deploymentplan.Digest(captured)
		expected.AppRootReview = deploymentplan.Digest(app.Plan)
		expected.AppRootArtifactSHA = deploymentplan.Digest(*app)
		expected.AppRootCompletionSHA = deploymentplan.Digest(proof)
	}
	comparison := post
	comparison.Review = ""
	comparison.Candidate = nil
	if managedLatePostMintDigest(&comparison) != managedLatePostMintDigest(expected) {
		return bad()
	}
	post.InstallationClock = clock
	frame, removeErr := managedLatePostMintRemoveAdmission(appFrame, parsedIndex)
	if removeErr != nil {
		return bad()
	}
	return frame, &post, nil
}

// Historical snapshots already authenticated by their enclosing source do
// not consult today's completion pointer. Actual App members always use the
// exact typed App frame before the original Late parser sees the journal.
func managedLateAppRootCRLRuntimeParsingView(store secretStore, e *environmentCreateExecution) (*environmentCreateExecution, *createAppRootCRLArtifact, error) {
	for _, entry := range e.Journal.Entries {
		if strings.HasPrefix(entry.Key, "app-root-crl-renewal-") && !strings.Contains(entry.Key, "/") {
			return managedLateAppRootCRLFrame(store, e, false)
		}
	}
	return e, nil, nil
}
func managedLatePostMintRemoveAdmission(e *environmentCreateExecution, index int) (*environmentCreateExecution, error) {
	if e == nil || index < 0 || index >= len(e.Journal.Entries) || !strings.HasPrefix(e.Journal.Entries[index].Key, "openbao-recovery-admission-") {
		return nil, environmentCreateBootstrapBlocker("late post-mint parsing admission differs")
	}
	frame := *e
	frame.Journal = e.Journal
	frame.Journal.Entries = append(slices.Clone(e.Journal.Entries[:index]), e.Journal.Entries[index+1:]...)
	return &frame, nil
}
func managedLateEffectiveOwnerCandidate(store secretStore, e *environmentCreateExecution, late *createManagedLateSessionRecovery) (*deploymentplan.Plan, error) {
	_, post, err := managedLatePostMintRuntimeFrame(store, e, late)
	if err != nil {
		return nil, err
	}
	if post != nil {
		return post.Candidate, nil
	}
	return late.OwnerCandidate, nil
}
func managedLateExpectedOwnerCandidate(r *createServiceSessionRecovery) *deploymentplan.Plan {
	if r == nil {
		return nil
	}
	if r.ManagedLatePostMint != nil {
		return r.ManagedLatePostMint.Candidate
	}
	if r.ManagedLateSession != nil {
		return r.ManagedLateSession.OwnerCandidate
	}
	return nil
}

func restoreManagedLatePostMintCeremony(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime) (environmentCreateServiceCeremony, error) {
	r := runtime.ServiceSessionRecovery
	if r == nil || r.ManagedLatePostMint == nil {
		return environmentCreateServiceCeremony{}, environmentCreateBootstrapBlocker("late post-mint ceremony absent")
	}
	if _, err := managedLateActiveBudget(ctx); err != nil {
		return environmentCreateServiceCeremony{}, err
	}
	if _, post, err := managedLatePostMintRuntimeFrame(store, runtime.Execution, r.ManagedLateSession); err != nil || post == nil {
		return environmentCreateServiceCeremony{}, environmentCreateBootstrapBlocker("late post-mint fresh admission absent")
	}
	if err := verifyManagedLateSessionRuntimeSource(ctx, cfg, store, runtime, r.ManagedLateSession); err != nil {
		return environmentCreateServiceCeremony{}, err
	}
	if err := verifyServiceSessionAuthentication(ctx, cfg, store, runtime.Execution, runtime.Backend, r); err != nil {
		return environmentCreateServiceCeremony{}, err
	}
	var observed createManagedLateSeedObservation
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", r.ManagedLateSession.Review, "managed-late-seed-observed.json"), &observed) != nil {
		return environmentCreateServiceCeremony{}, environmentCreateBootstrapBlocker("late post-mint accepted seed observation absent")
	}
	leaf, leafErr := parseFirstCertificatePEM([]byte(observed.CertificatePEM))
	if leafErr != nil || leaf == nil || !leaf.NotAfter.Equal(observed.Session.Expires) {
		return environmentCreateServiceCeremony{}, environmentCreateBootstrapBlocker("late post-mint existing certificate reserve differs")
	}
	if err := verifyManagedLateSeedBudget(ctx, leaf.NotAfter, observed.Session.Expires); err != nil {
		return environmentCreateServiceCeremony{}, err
	}
	return r.ManagedLatePostMint.Ceremony, nil
}
