package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A renewal continuation must extend the exact journal saved by qualification.
// Individual native outcomes still require their own stage and UID checks.
func partialSessionJournalExtends(source, current environmentCreateJournal) bool {
	if source.Schema != current.Schema || source.Environment != current.Environment || source.Stack != current.Stack || source.PlanSHA256 != current.PlanSHA256 || source.ArtifactSHA256 != current.ArtifactSHA256 || source.OwnerToken != current.OwnerToken || !source.AdmittedAt.Equal(current.AdmittedAt) || source.OriginalJournal != current.OriginalJournal || source.OriginalJournalSHA256 != current.OriginalJournalSHA256 || source.ProviderMaterialPlanSHA256 != current.ProviderMaterialPlanSHA256 || !source.CompletedAt.IsZero() || !current.CompletedAt.IsZero() || len(source.Entries) > len(current.Entries) {
		return false
	}
	for _, lease := range source.KubernetesLeases {
		if !slices.Contains(current.KubernetesLeases, lease) {
			return false
		}
	}
	seen := map[string]bool{}
	for i, entry := range current.Entries {
		if entry.Key == "" || seen[entry.Key] {
			return false
		}
		seen[entry.Key] = true
		if i >= len(source.Entries) {
			continue
		}
		original := source.Entries[i]
		if entry.Key != original.Key || entry.Phase != original.Phase || entry.IntentSHA256 != original.IntentSHA256 || !entry.StartedAt.Equal(original.StartedAt) || !partialSessionEntryAdvances(original, entry) {
			return false
		}
	}
	return true
}

func partialSessionEntryAdvances(original, current environmentCreateJournalEntry) bool {
	valid := func(e environmentCreateJournalEntry) bool {
		switch e.State {
		case "pending":
			return len(e.Receipt) == 0 && e.CompletedAt.IsZero()
		case "allocated":
			return len(e.Receipt) != 0 && e.CompletedAt.IsZero()
		case "completed":
			return len(e.Receipt) != 0 && !e.CompletedAt.IsZero() && !e.CompletedAt.Before(e.StartedAt)
		}
		return false
	}
	if !valid(original) || !valid(current) {
		return false
	}
	if original.State == "completed" {
		return current.State == "completed" && original.CompletedAt.Equal(current.CompletedAt) && samePersistedJSONReceipt(original.Receipt, current.Receipt)
	}
	if original.State == "allocated" {
		return current.State != "pending" && samePersistedJSONReceipt(original.Receipt, current.Receipt)
	}
	return true
}

// Compare the canonical representation persisted by RecordReceipt, preserving
// values while accepting changes in whitespace and JSON object field order.
func samePersistedJSONReceipt(first, second json.RawMessage) bool {
	if len(first) == 0 || len(second) == 0 {
		return false
	}
	// RecordReceipt persists a canonical JSON object. Retry input may come
	// from a struct with a different field order; compare that same canonical
	// representation, retaining exact numbers and all original values.
	a, errA := canonicalEnvironmentCreateJSON(first)
	b, errB := canonicalEnvironmentCreateJSON(second)
	return errA == nil && errB == nil && bytes.Equal(a, b)
}

// Bind the private source snapshot and candidate to the actual qualified plan.
type partialSessionContinuationProof struct {
	SourceJournal environmentCreateJournal     `json:"source_journal"`
	Recovery      createManagedSessionRecovery `json:"recovery"`
	Intent        createServiceSessionIntent   `json:"intent"`
	RenewedIntent createServiceSessionIntent   `json:"renewed_intent"`
	KeySHA        string                       `json:"key_sha256"`
	CSRSHA        string                       `json:"csr_sha256"`
	Candidate     *deploymentplan.Plan         `json:"candidate"`
}

func partialSessionAdmissionIntent(parent, review string, p *partialSessionContinuationProof) deploymentplan.Object {
	if p == nil || p.Candidate == nil {
		return nil
	}
	return deploymentplan.Object{"parent": parent, "qualification_sha256": review, "original_session": p.Intent.SessionID, "successor_session": p.RenewedIntent.SessionID, "candidate_sha256": p.Candidate.SHA256()}
}

// The durable parent entry proves that this exact qualification was admitted
// while fresh. After a process interruption it remains the same operation,
// rather than requiring another certificate or resetting its deadline.
func partialSessionAdmissionValid(q issuerEnrollmentContinuationProof, review string, entry environmentCreateJournalEntry, current environmentCreateJournal) bool {
	if !environmentPreflightDigest.MatchString(review) || q.QualifiedAt.IsZero() || !q.ValidUntil.Equal(q.QualifiedAt.Add(time.Hour)) || entry.Key != "partial-session-renewal-"+review || entry.Phase != "partial-session-renewal" || entry.StartedAt.Before(q.QualifiedAt) || !entry.StartedAt.Before(q.ValidUntil) {
		return false
	}
	q.QualifiedAt, q.ValidUntil = time.Time{}, time.Time{}
	if deploymentplan.Digest(q) != review || !partialSessionProofMatches(q, current) {
		return false
	}
	actual := createOpenBaoACKCompletionEntry(current, entry.Key)
	if actual == nil || activeTransportJournalDigest(*actual) != activeTransportJournalDigest(entry) {
		return false
	}
	intent := partialSessionAdmissionIntent(q.Parent, review, q.PartialSession)
	if intent == nil || entry.IntentSHA256 != activeTransportJournalDigest(intent) {
		return false
	}
	return partialSessionEntryAdvances(entry, entry)
}

func loadPartialSessionAdmission(store secretStore, e *environmentCreateExecution) (issuerEnrollmentContinuationProof, string, bool, error) {
	var q issuerEnrollmentContinuationProof
	if e == nil || e.Store.Root != store.Root {
		return q, "", false, createProviderRecoveryBlock("partial session original execution is unavailable")
	}
	const prefix = "partial-session-renewal-"
	var entry *environmentCreateJournalEntry
	for i := range e.Journal.Entries {
		if strings.HasPrefix(e.Journal.Entries[i].Key, prefix) {
			if entry != nil {
				return q, "", false, createProviderRecoveryBlock("partial session renewal has ambiguous admissions")
			}
			entry = &e.Journal.Entries[i]
		}
	}
	if entry == nil {
		return q, "", false, nil
	}
	review := strings.TrimPrefix(entry.Key, prefix)
	if !environmentPreflightDigest.MatchString(review) {
		return q, "", false, createProviderRecoveryBlock("partial session renewal admission is malformed")
	}
	path, err := store.safePath(filepath.Join("deployment/create/issuer-enrollment-continuation", review, "qualification.json"))
	if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil || readEnvironmentPreflightJSON(path, &q) != nil || q.ParentOwner != deploymentplan.Digest(e.owner) || !partialSessionAdmissionValid(q, review, *entry, e.Journal) {
		return issuerEnrollmentContinuationProof{}, "", false, createProviderRecoveryBlock("partial session original qualification or parent admission is unavailable")
	}
	q.QualifiedAt, q.ValidUntil = time.Time{}, time.Time{}
	return q, review, true, nil
}

func beginPartialSessionAdmission(store secretStore, e *environmentCreateExecution, q issuerEnrollmentContinuationProof, review string, now time.Time) error {
	if e == nil || e.Store.Root != store.Root {
		return createProviderRecoveryBlock("partial session original execution is unavailable")
	}
	if _, priorReview, exists, err := loadPartialSessionAdmission(store, e); err != nil {
		return err
	} else if exists {
		if priorReview != review {
			return createProviderRecoveryBlock("partial session continuation already has a different admitted qualification")
		}
		return e.Check(context.Background())
	}
	if q.ParentOwner != deploymentplan.Digest(e.owner) || q.QualifiedAt.After(now) || !q.ValidUntil.Equal(q.QualifiedAt.Add(time.Hour)) || !now.Before(q.ValidUntil) || deploymentplan.Digest(e.Journal) != q.ParentJournal {
		return createProviderRecoveryBlock("fresh original partial session qualification is required before admission")
	}
	q.QualifiedAt, q.ValidUntil = time.Time{}, time.Time{}
	if deploymentplan.Digest(q) != review || !partialSessionProofMatches(q, e.Journal) {
		return createProviderRecoveryBlock("partial session qualification differs from the current original execution")
	}
	intent := partialSessionAdmissionIntent(q.Parent, review, q.PartialSession)
	return e.BeginEntry("partial-session-renewal-"+review, "partial-session-renewal", mustProviderRecoveryJSON(intent), now)
}

func partialSessionAllowsPendingOpenBao(cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, kind, name string) bool {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if e == nil || r == nil || r.ManagedTransportRecovery == nil || !r.ManagedTransportRecovery.PartialSessionRenewal || r.ManagedTransportRecovery.SessionRenewal == nil || !environmentPreflightDigest.MatchString(r.Review) {
		return false
	}
	if r.SecondSession {
		q, err := loadSecondSessionAdmission(e.Store, e, r.Review)
		if err != nil || r.RenewalCandidate == nil || q.Renewal.Intent.SessionID != r.Intent.SessionID || q.Renewal.RenewedIntent.SessionID != r.RenewedIntent.SessionID || q.Renewal.Candidate.SHA256() != r.RenewalCandidate.SHA256() {
			return false
		}
	} else {
		q, review, ok, err := loadPartialSessionAdmission(e.Store, e)
		if err != nil || !ok || review != r.Review || q.PartialSession == nil || q.PartialSession.RenewedIntent.SessionID != r.RenewedIntent.SessionID {
			return false
		}
	}
	session := r.RenewedIntent.SessionID
	switch kind {
	case "policy":
		return name == "rtk-"+cfg.Environment+"-service-bootstrap-"+session || name == "create-"+session+"-cleanup-controller"
	case "role":
		return name == "pki-service-bootstrap-"+cfg.Environment || name == "pki-controller-"+cfg.Environment
	}
	return false
}

func makePartialSessionProof(q issuerEnrollmentContinuationProof, e *environmentCreateExecution, r *createServiceSessionRecovery) (*partialSessionContinuationProof, error) {
	if e == nil || r == nil || r.ManagedTransportRecovery == nil || r.ManagedTransportRecovery.SessionRenewal == nil || !r.ManagedTransportRecovery.PartialSessionRenewal || r.RenewalCandidate == nil {
		return nil, createProviderRecoveryBlock("qualified partial session renewal is unavailable")
	}
	p := &partialSessionContinuationProof{Recovery: *r.ManagedTransportRecovery.SessionRenewal, Intent: r.Intent, RenewedIntent: r.RenewedIntent, KeySHA: r.OriginalKeySHA, CSRSHA: r.OriginalCSRSHA}
	// The execution mutates its journal and candidate in place. Freeze the
	// qualification rather than keeping aliases to mutable slices or maps.
	if json.Unmarshal(mustProviderRecoveryJSON(e.Journal), &p.SourceJournal) != nil || json.Unmarshal(mustProviderRecoveryJSON(r.RenewalCandidate), &p.Candidate) != nil {
		return nil, createProviderRecoveryBlock("partial session qualification cannot be frozen")
	}
	var frozen partialSessionContinuationProof
	if json.Unmarshal(mustProviderRecoveryJSON(p), &frozen) != nil {
		return nil, createProviderRecoveryBlock("partial session qualification cannot be frozen")
	}
	p = &frozen
	q.PartialSession = p
	if !partialSessionProofMatches(q, e.Journal) {
		return nil, createProviderRecoveryBlock("partial session qualification differs from its original source and renewal candidate")
	}
	return p, nil
}

// Restore only the candidate already covered by the immutable qualification;
// native session, stage ownership and custody are checked by the caller.
func restorePartialSessionProof(q issuerEnrollmentContinuationProof, review string, current environmentCreateJournal, r *createServiceSessionRecovery) error {
	if r == nil || r.ManagedTransportRecovery == nil || !environmentPreflightDigest.MatchString(review) || deploymentplan.Digest(q) != review || !partialSessionProofMatches(q, current) {
		return createProviderRecoveryBlock("partial session continuation differs from its original qualified plan")
	}
	p := q.PartialSession
	if r.Ceremony.BootstrapSessionID != p.Intent.SessionID || r.Ceremony.Intermediate.ID != p.Intent.IssuerID || r.Ceremony.Root.RootFingerprint != p.Intent.RootSHA || r.Ceremony.BootstrapClaim != p.Intent.Claim || !slices.Equal(r.Ceremony.BootstrapCredentialNamespaces, p.Intent.Namespaces) {
		return createProviderRecoveryBlock("partial session continuation differs from the original Service lineage")
	}
	var candidate deploymentplan.Plan
	if json.Unmarshal(mustProviderRecoveryJSON(p.Candidate), &candidate) != nil {
		return createProviderRecoveryBlock("qualified partial session candidate cannot be restored")
	}
	recovery := p.Recovery
	r.Intent, r.RenewedIntent = p.Intent, p.RenewedIntent
	r.OriginalKeySHA, r.OriginalCSRSHA = p.KeySHA, p.CSRSHA
	r.Review, r.RenewalBindingSHA = review, recovery.BindingSHA256
	r.SourceRenewalStage, r.RenewedCertPath = recovery.SourceStage, "/run/pki-bootstrap/renew-"+recovery.BindingSHA256[:12]+"/bootstrap.crt"
	r.Renewal, r.PostServiceRenewal = true, true
	r.RenewalCandidate = &candidate
	r.ManagedTransportRecovery.PartialSessionRenewal, r.ManagedTransportRecovery.SessionRenewal = true, &recovery
	return nil
}

func partialSessionProofMatches(q issuerEnrollmentContinuationProof, current environmentCreateJournal) bool {
	p := q.PartialSession
	if p == nil || p.Candidate == nil || !environmentPreflightDigest.MatchString(q.Parent) || q.Parent != current.PlanSHA256 || deploymentplan.Digest(p.SourceJournal) != q.ParentJournal || !partialSessionJournalExtends(p.SourceJournal, current) || !p.Recovery.PartialInventory || p.Recovery.SourceStage != "kubernetes/service-session" || p.Recovery.OriginalSession != p.Intent.SessionID || !environmentBootstrapOwnerUUID.MatchString(p.Intent.SessionID) || !environmentBootstrapOwnerUUID.MatchString(p.Intent.IssuerID) || !environmentPreflightDigest.MatchString(p.Intent.RootSHA) || len(p.Intent.Subjects) == 0 || len(p.Intent.Namespaces) == 0 || !environmentBootstrapOwnerUUID.MatchString(p.RenewedIntent.SessionID) || p.RenewedIntent.SessionID != sessionRenewalUUID(q.Parent, p.Recovery.BindingSHA256, p.Intent.SessionID) || !environmentPreflightDigest.MatchString(p.KeySHA) || !environmentPreflightDigest.MatchString(p.CSRSHA) || !environmentPreflightDigest.MatchString(p.Recovery.BindingSHA256) || !environmentPreflightDigest.MatchString(p.Recovery.SessionSHA256) || !environmentPreflightDigest.MatchString(p.Recovery.ClientsSHA256) || !environmentPreflightDigest.MatchString(p.Recovery.SourcePlanSHA256) || !environmentPreflightDigest.MatchString(p.Recovery.SourceJournalSHA256) || !environmentPreflightDigest.MatchString(p.Recovery.OriginalCertificateSHA256) || p.Recovery.OriginalCertificatePath != "/run/pki-bootstrap/bootstrap.crt" || p.Candidate.SHA256() != p.Recovery.CandidateSHA256 || p.Candidate.Environment != current.Environment || p.Candidate.Stack != current.Stack || p.Candidate.ReleaseSHA256 != q.Release || len(p.Candidate.Resources) != 1 || p.Candidate.Resources[0].Action != "create" || p.Candidate.Resources[0].Desired["kind"] != "Job" {
		return false
	}
	entry := createOpenBaoACKCompletionEntry(p.SourceJournal, p.Recovery.SourceStage)
	var receipt struct {
		Plan string `json:"plan_sha256"`
	}
	if entry == nil || entry.State != "completed" || json.Unmarshal(entry.Receipt, &receipt) != nil || receipt.Plan != p.Recovery.SourcePlanSHA256 {
		return false
	}
	if p.Recovery.OriginalState == nil || !issuerEnrollmentSessionExpired(p.Intent, *p.Recovery.OriginalState, p.Recovery.OriginalState.Expires) {
		return false
	}
	if len(p.Recovery.OriginalClientDigests) < len(p.Recovery.OriginalState.Installed) || !slices.IsSorted(p.Recovery.OriginalClientDigests) {
		return false
	}
	for i, digest := range p.Recovery.OriginalClientDigests {
		if !environmentPreflightDigest.MatchString(digest) || i > 0 && p.Recovery.OriginalClientDigests[i-1] == digest {
			return false
		}
	}
	original, renewed := p.Intent, p.RenewedIntent
	original.SessionID = renewed.SessionID
	if deploymentplan.Digest(original) != deploymentplan.Digest(renewed) {
		return false
	}
	containers := certIssuerObjectList(environmentPKIPod(p.Candidate.Resources[0].Desired)["containers"])
	if len(containers) != 1 {
		return false
	}
	container := deploymentplan.Map(containers[0])
	return mqttFoundationEnv(container, "PKI_BOOTSTRAP_SESSION_ID") == renewed.SessionID &&
		mqttFoundationEnv(container, "PKI_BOOTSTRAP_ISSUER_ID") == renewed.IssuerID &&
		mqttFoundationEnv(container, "PKI_BOOTSTRAP_ROOT_SHA256") == renewed.RootSHA &&
		mqttFoundationEnv(container, "PKI_BOOTSTRAP_SUBJECTS") == strings.Join(renewed.Subjects, ",") &&
		mqttFoundationEnv(container, "PKI_BOOTSTRAP_KEY_PATH") == "/run/pki-bootstrap/bootstrap.key" &&
		mqttFoundationEnv(container, "PKI_BOOTSTRAP_CSR_PATH") == "/run/pki-bootstrap/bootstrap.csr" &&
		mqttFoundationEnv(container, "PKI_BOOTSTRAP_CERT_PATH") == "/run/pki-bootstrap/renew-"+p.Recovery.BindingSHA256[:12]+"/bootstrap.crt"
}

// After the native Job has issued the successor, the former session is no
// longer usable. Accept only its exact recorded lineage and one active,
// intended successor; the caller must additionally verify the Job and key.
func partialSessionSuccessorStatesValid(p *partialSessionContinuationProof, old, next managedSessionState, now time.Time) bool {
	if p == nil || p.Recovery.OriginalState == nil {
		return false
	}
	saved := p.Recovery.OriginalState
	if old.Session != saved.Session || old.Issuer != saved.Issuer || old.Root != saved.Root || old.Fingerprint != saved.Fingerprint || !old.Expires.Equal(saved.Expires) || !slices.Equal(old.Subjects, saved.Subjects) || !slices.Equal(old.Installed, saved.Installed) || old.Status != "expired" || old.OtherActive != 1 {
		return false
	}
	if !issuerEnrollmentSessionUsable(p.RenewedIntent, next, now) {
		return false
	}
	seen := map[string]bool{}
	for _, subject := range next.Installed {
		if seen[subject] || !slices.Contains(next.Subjects, subject) {
			return false
		}
		seen[subject] = true
	}
	return true
}

func partialSessionOriginalStatePreserved(saved, current managedSessionState, now time.Time) bool {
	return saved.Session == current.Session && saved.Issuer == current.Issuer && saved.Root == current.Root && saved.Fingerprint == current.Fingerprint && saved.Expires.Equal(current.Expires) && !current.Expires.After(now) && slices.Equal(saved.Subjects, current.Subjects) && slices.Equal(saved.Installed, current.Installed) && saved.OtherActive == 0 && current.OtherActive == 0 && (saved.Status == current.Status && (saved.Status == "active" || saved.Status == "expired") || saved.Status == "active" && current.Status == "expired")
}

// Observe an already minted successor without signing again. The exact child
// Job, original custody and both native session rows must all agree with the
// qualified plan before the caller can resume credential publication.
func observePartialSessionSuccessor(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, q issuerEnrollmentContinuationProof, review string, r *createServiceSessionRecovery) error {
	bad := func() error {
		return createProviderRecoveryBlock("partial session successor requires its original qualified Job, custody and native session outcomes")
	}
	if e == nil || e.Store.Root != store.Root || r == nil || r.ManagedTransportRecovery == nil || r.ManagedTransportRecovery.SessionRenewal == nil || r.Review != review || deploymentplan.Digest(q) != review || !partialSessionProofMatches(q, e.Journal) {
		return bad()
	}
	p := q.PartialSession
	stage := "service-session-original-key-renewal-" + review
	candidate := p.Candidate
	if f := r.ManagedTransportRecovery.PartialTrustForward; f != nil {
		forward, forwardReview, ok, err := loadPartialSessionTrustForwardAdmission(store, e, q, review)
		if err != nil || !ok || forwardReview != r.ManagedTransportRecovery.PartialTrustForwardReview || forward.PartialTrustForward == nil || deploymentplan.Digest(forward.PartialTrustForward.Forward) != deploymentplan.Digest(f) {
			return bad()
		}
		entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
		alias := mustProviderRecoveryJSON(partialSessionTrustForwardAlias(f, forwardReview))
		if entry == nil || entry.State == "pending" && len(entry.Receipt) != 0 || entry.State != "pending" && (entry.State != "allocated" && entry.State != "completed" || activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(jsonRawObject(alias))) {
			return bad()
		}
		failed, exists, err := p.Candidate.ReadExecution(store.Root)
		if err != nil || !exists || deploymentplan.Digest(failed) != f.FailedJournalSHA {
			return bad()
		}
		stage, candidate = "partial-session-trust-forward-"+forwardReview, f.Candidate
	}
	journal, exists, err := readEnvironmentCreateStageProgress(store, e, stage, candidate)
	if err != nil || !exists || len(candidate.Resources) != 1 {
		return bad()
	}
	job := candidate.Resources[0]
	uid := journal.Owners[job.ID]
	actual, err := b.Read(ctx, job.ID)
	if err != nil || !environmentBootstrapOwnerUUID.MatchString(uid) || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != uid || !deploymentplan.Matches(job.Desired, actual) {
		return bad()
	}
	original, originalJournal, err := acceptedCreateStage(store, e, p.Recovery.SourceStage)
	if err != nil || original.SHA256() != p.Recovery.SourcePlanSHA256 || deploymentplan.Digest(originalJournal) != p.Recovery.SourceJournalSHA256 {
		return bad()
	}
	originalJob, ok := managedSessionSourceJob(original, originalJournal)
	if !ok {
		return bad()
	}
	actual, err = b.Read(ctx, originalJob.ID)
	if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != originalJournal.Owners[originalJob.ID] || !deploymentplan.Matches(originalJob.Desired, actual) {
		return bad()
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: b}
	old, _, err := readManagedSessionState(ctx, cfg, runtime, p.Intent.SessionID)
	if err != nil {
		return bad()
	}
	next, _, err := readManagedSessionState(ctx, cfg, runtime, p.RenewedIntent.SessionID)
	if err != nil || !partialSessionSuccessorStatesValid(p, old, next, time.Now().UTC()) || verifyManagedSeedOriginalCustody(ctx, runtime, r, &p.Recovery) != nil {
		return bad()
	}
	key, err1 := b.command(ctx, nil, "--namespace", p.Intent.Namespaces[0], "exec", "deployment/"+r.Ceremony.CustodyReader, "--container", "custody", "--", "cat", "/run/pki-bootstrap/bootstrap.key")
	csr, err2 := b.command(ctx, nil, "--namespace", p.Intent.Namespaces[0], "exec", "deployment/"+r.Ceremony.CustodyReader, "--container", "custody", "--", "cat", "/run/pki-bootstrap/bootstrap.csr")
	cert, err3 := b.command(ctx, nil, "--namespace", p.Intent.Namespaces[0], "exec", "deployment/"+r.Ceremony.CustodyReader, "--container", "custody", "--", "cat", r.RenewedCertPath)
	if err1 != nil || err2 != nil || err3 != nil || environmentPreflightBytesSHA256(key) != p.KeySHA || environmentPreflightBytesSHA256(csr) != p.CSRSHA || !partialSessionSuccessorCustodyValid(key, cert, []byte(r.Ceremony.Root.Certificate), csr, r.Ceremony, next, time.Now().UTC()) {
		return bad()
	}
	clients, err := readManagedSessionClientRows(ctx, cfg, runtime)
	if err != nil || !partialSessionClientsPreserved(clients, r.Ceremony, p.Intent.Subjects, old, &p.Recovery, time.Now().UTC()) {
		return bad()
	}
	return e.Check(ctx)
}

func completePartialSessionAdmission(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony) error {
	bad := func() error {
		return environmentCreateBootstrapBlocker("partial session completion requires its original qualification and native credential owners")
	}
	e, r := runtime.Execution, runtime.ServiceSessionRecovery
	if e == nil || r == nil || e.Store.Root != store.Root {
		return bad()
	}
	if r.SecondSession {
		q, err := loadSecondSessionAdmission(store, e, r.Review)
		if err != nil {
			return bad()
		}
		completed, err := completedServiceSessionBatch(store, e, q, r.Review)
		if err != nil || !completed || service.BootstrapSessionID != q.Renewal.RenewedIntent.SessionID || service.Intermediate.ID != q.Renewal.Intent.IssuerID || service.Root.RootFingerprint != q.Renewal.Intent.RootSHA || service.Root.Certificate != r.Ceremony.Root.Certificate || service.BootstrapCredentialSecret != "service-bootstrap-"+service.BootstrapSessionID || service.CustodyReader != r.Ceremony.CustodyReader || !slices.Equal(service.BootstrapCredentialNamespaces, q.Renewal.Intent.Namespaces) || len(service.CredentialUIDs) != len(q.Renewal.Intent.Namespaces) || validateEnvironmentBootstrapCleanupBinding(&service.CleanupBinding, cfg.Environment) != nil || service.CleanupBinding.SessionID != service.BootstrapSessionID || service.CleanupBinding.SecretUID != service.CredentialUIDs[service.BootstrapCredentialNamespace] {
			return bad()
		}
		state, _, err := readManagedSessionState(ctx, cfg, runtime, service.BootstrapSessionID)
		if err != nil {
			return bad()
		}
		if !sessionExistingConsumersACKed(q, state, time.Now().UTC()) {
			terminal, err := observeFreshProductTerminalSession(ctx, cfg, store, e, runtime.Backend, q, r.Review, state, time.Now().UTC())
			if err != nil {
				return err
			}
			if terminal == nil {
				return bad()
			}
		}
		return e.Check(ctx)
	}
	q, review, exists, err := loadPartialSessionAdmission(store, e)
	if err != nil || !exists || review != r.Review || observePartialSessionSuccessor(ctx, cfg, store, e, runtime.Backend, q, review, r) != nil {
		return bad()
	}
	p := q.PartialSession
	state, _, err := readManagedSessionState(ctx, cfg, runtime, p.RenewedIntent.SessionID)
	if err != nil || !issuerEnrollmentSessionUsable(p.RenewedIntent, state, time.Now().UTC()) || service.BootstrapSessionID != p.RenewedIntent.SessionID || service.Intermediate.ID != p.Intent.IssuerID || service.Root.RootFingerprint != p.Intent.RootSHA || service.Root.Certificate != r.Ceremony.Root.Certificate || service.BootstrapCredentialSecret != "service-bootstrap-"+service.BootstrapSessionID || service.CustodyReader != r.Ceremony.CustodyReader || !slices.Equal(service.BootstrapCredentialNamespaces, p.Intent.Namespaces) || len(service.CredentialUIDs) != len(p.Intent.Namespaces) || validateEnvironmentBootstrapCleanupBinding(&service.CleanupBinding, cfg.Environment) != nil || service.CleanupBinding.SessionID != service.BootstrapSessionID || service.CleanupBinding.SecretUID != service.CredentialUIDs[service.BootstrapCredentialNamespace] {
		return bad()
	}
	for _, ns := range p.Intent.Namespaces {
		uid := service.CredentialUIDs[ns]
		secret, readErr := runtime.Backend.Read(ctx, "Secret/"+ns+"/"+service.BootstrapCredentialSecret)
		if readErr != nil || secret == nil || !environmentBootstrapOwnerUUID.MatchString(uid) || deploymentplan.Map(secret["metadata"])["uid"] != uid || secret["immutable"] != true {
			return bad()
		}
		data := deploymentplan.Map(secret["data"])
		cert, certErr := base64.StdEncoding.DecodeString(deploymentplan.String(data["tls.crt"]))
		key, keyErr := base64.StdEncoding.DecodeString(deploymentplan.String(data["tls.key"]))
		root, rootErr := base64.StdEncoding.DecodeString(deploymentplan.String(data["ca.crt"]))
		leaf, parseErr := parseFirstCertificatePEM(cert)
		if certErr != nil || keyErr != nil || rootErr != nil || parseErr != nil || environmentPreflightBytesSHA256(key) != p.KeySHA || environmentPreflightBytesSHA256(leaf.Raw) != state.Fingerprint || string(root) != service.Root.Certificate {
			return bad()
		}
	}
	receipt := mustProviderRecoveryJSON(deploymentplan.Object{"qualification_sha256": review, "session_id": state.Session, "fingerprint": state.Fingerprint, "expires_at": state.Expires, "cleanup_binding_sha256": deploymentplan.Digest(service.CleanupBinding), "credential_uids_sha256": deploymentplan.Digest(service.CredentialUIDs)})
	entry := createOpenBaoACKCompletionEntry(e.Journal, "partial-session-renewal-"+review)
	if entry == nil {
		return bad()
	}
	if entry.State == "completed" {
		if !samePersistedJSONReceipt(entry.Receipt, receipt) {
			return bad()
		}
		return e.Check(ctx)
	}
	if err := e.RecordReceipt(entry.Key, receipt); err != nil {
		return err
	}
	return e.CompleteEntry(entry.Key, time.Now().UTC())
}
