package main

import (
	"context"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// observeSecondSessionSuccessor only observes an already-admitted native Job.
// It never signs or mutates anything and does not synthesize installation ACKs.
func observeSecondSessionSuccessor(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, plan environmentCreatePlan, service environmentCreateServiceCeremony, openBaoRoot environmentCreateRegisteredIssuer, review string, now time.Time) error {
	q, err := loadSecondSessionAdmission(store, e, review)
	if err != nil {
		return err
	}
	if finalized, err := observeFinalizedFreshBootstrap(ctx, cfg, store, e, b); err != nil {
		return err
	} else if finalized != nil {
		if q.Renewal.RenewedIntent.SessionID != finalized.Ceremony.BootstrapSessionID || service.Root.RootFingerprint != finalized.Ceremony.Root.RootFingerprint || service.Intermediate.ID != finalized.Ceremony.Intermediate.ID {
			return environmentCreateBootstrapBlocker("finalized fresh continuation differs from its original admitted session")
		}
		return e.Check(ctx)
	}
	state, _, err := readManagedSessionState(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b}, q.Renewal.RenewedIntent.SessionID)
	if err != nil {
		return err
	}
	now = time.Now().UTC()
	terminal, err := observeFreshProductTerminalSession(ctx, cfg, store, e, b, q, review, state, now)
	if err != nil {
		return err
	}
	return observeSecondSessionSuccessorWithReads(ctx, cfg, store, e, nativeSecondSessionReads(cfg, store, e, b), plan, service, openBaoRoot, review, now, terminal)
}

func observeSecondSessionSuccessorWithReads(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, reads secondSessionReads, plan environmentCreatePlan, service environmentCreateServiceCeremony, openBaoRoot environmentCreateRegisteredIssuer, review string, now time.Time, terminalProof ...*freshProductTerminalObservation) error {
	boundary := "admission"
	bad := func() error {
		return createProviderRecoveryBlock("second session requires its exact admitted Job, original custody and preserved native identities; boundary=" + boundary)
	}
	currentTime := func() time.Time {
		if reads.clock != nil {
			return reads.clock().UTC()
		}
		return now
	}
	now = currentTime()
	q, err := loadSecondSessionAdmission(store, e, review)
	if err != nil || now.IsZero() || now.Before(q.QualifiedAt) {
		return bad()
	}
	boundary = "execution-deadline"
	deadline, err := sessionEffectiveDeadline(ctx, store, e, q, now)
	if err != nil || !now.Before(deadline) {
		return bad()
	}
	boundary = "terminal-observation"
	var terminal *freshProductTerminalObservation
	if len(terminalProof) > 1 {
		return bad()
	}
	if len(terminalProof) == 1 {
		terminal = terminalProof[0]
	}
	if terminal != nil && (terminal.review != review || terminal.at.Before(q.QualifiedAt) || !terminal.at.Before(now)) {
		return bad()
	}
	p := q.Renewal
	boundary = "original-admission"
	first, firstReview, ok, err := loadPartialSessionAdmission(store, e)
	if err != nil || !ok || first.PartialSession == nil || firstReview != p.OriginalReview {
		return bad()
	}
	boundary = "lineage-head"
	head, headReview, err := sessionLineageHead(store, e)
	if err != nil || head == nil || headReview != review || deploymentplan.Digest(*head) != deploymentplan.Digest(q) {
		return bad()
	}
	boundary = "original-key-lineage"
	if p.KeySHA != first.PartialSession.KeySHA || p.CSRSHA != first.PartialSession.CSRSHA || p.SourceStage != first.PartialSession.Recovery.SourceStage || p.SourcePlanSHA != first.PartialSession.Recovery.SourcePlanSHA256 || p.SourceJournalSHA != first.PartialSession.Recovery.SourceJournalSHA256 || service.Root.RootFingerprint != p.Intent.RootSHA || service.Intermediate.ID != p.Intent.IssuerID || len(p.Intent.Namespaces) == 0 || p.PreviousCustodyID != "Deployment/"+p.Intent.Namespaces[0]+"/"+service.CustodyReader {
		return bad()
	}
	stage, previous := "service-session-original-key-renewal-"+firstReview, first.PartialSession.Candidate
	forward, forwardReview, forwarded, err := loadPartialSessionTrustForwardAdmission(store, e, first, firstReview)
	if err != nil {
		return bad()
	}
	if p.PreviousReview != firstReview {
		prior, err := loadSecondSessionAdmission(store, e, p.PreviousReview)
		if err != nil || deploymentplan.Digest(prior.Renewal.RenewedIntent) != deploymentplan.Digest(p.Intent) || prior.Renewal.BindingSHA != p.PreviousBindingSHA {
			return bad()
		}
		stage, previous = "service-session-original-key-renewal-"+p.PreviousReview, prior.Renewal.Candidate
	} else if forwarded {
		if forward.PartialTrustForward == nil || forward.PartialTrustForward.Forward == nil {
			return bad()
		}
		f := forward.PartialTrustForward.Forward
		entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stage)
		alias := mustProviderRecoveryJSON(partialSessionTrustForwardAlias(f, forwardReview))
		if entry == nil || entry.State == "pending" && len(entry.Receipt) != 0 || entry.State != "pending" && (entry.State != "allocated" && entry.State != "completed" || activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(jsonRawObject(alias))) {
			return bad()
		}
		failed, exists, err := first.PartialSession.Candidate.ReadExecution(store.Root)
		if err != nil || !exists || deploymentplan.Digest(failed) != f.FailedJournalSHA {
			return bad()
		}
		stage, previous = "partial-session-trust-forward-"+forwardReview, f.Candidate
	}
	boundary = "previous-job-intent"
	if previous == nil || stage != p.PreviousStage || previous.SHA256() != p.PreviousPlanSHA || len(previous.Resources) != 1 || previous.Resources[0].ID != p.PreviousJobID {
		return bad()
	}
	boundary = "previous-job-journal"
	previousJournal, exists, err := readEnvironmentCreateStageProgress(store, e, stage, previous)
	if err != nil || !exists || deploymentplan.Digest(previousJournal) != p.PreviousJournalSHA || previousJournal.Owners[p.PreviousJobID] != p.PreviousJobUID {
		return bad()
	}
	checkOwner := func(id, uid string, desired deploymentplan.Object) bool {
		actual, err := reads.read(ctx, id)
		return err == nil && actual != nil && environmentBootstrapOwnerUUID.MatchString(uid) && deploymentplan.Map(actual["metadata"])["uid"] == uid && deploymentplan.Matches(desired, actual)
	}
	boundary = "previous-job-owner"
	if !checkOwner(p.PreviousJobID, p.PreviousJobUID, previous.Resources[0].Desired) {
		return bad()
	}
	boundary = "source-job"
	source, sourceJournal, err := acceptedCreateStage(store, e, p.SourceStage)
	sourceJob, found := managedSessionSourceJob(source, sourceJournal)
	if err != nil || !found || source.SHA256() != p.SourcePlanSHA || deploymentplan.Digest(sourceJournal) != p.SourceJournalSHA || sourceJob.ID != p.SourceJobID || sourceJournal.Owners[sourceJob.ID] != p.SourceJobUID || !checkOwner(p.SourceJobID, p.SourceJobUID, sourceJob.Desired) {
		return bad()
	}
	boundary = "selected-seed-image"
	image, _, err := selectedPartialSeedImage(plan)
	if err != nil {
		return bad()
	}
	boundary = "seed-candidate"
	rebuilt := p
	if err := compileSecondSessionCandidate(cfg, plan, &rebuilt, sourceJob.Desired, image, nil); err != nil || rebuilt.BindingSHA != p.BindingSHA || rebuilt.Candidate.SHA256() != p.Candidate.SHA256() {
		return bad()
	}
	boundary = "current-job-journal"
	jobStage := "service-session-original-key-renewal-" + review
	jobJournal, exists, err := readEnvironmentCreateStageProgress(store, e, jobStage, p.Candidate)
	if err != nil || !exists || len(p.Candidate.Resources) != 1 {
		return bad()
	}
	job := p.Candidate.Resources[0]
	boundary = "current-job-owner"
	if !checkOwner(job.ID, jobJournal.Owners[job.ID], job.Desired) {
		return bad()
	}
	boundary = "previous-custody-journal"
	custodyPlan, custodyJournal, err := acceptedCreateStage(store, e, "kubernetes/service-custody-reader-renewal-"+p.PreviousReview)
	if err != nil || custodyPlan.SHA256() != p.PreviousCustodyPlanSHA || deploymentplan.Digest(custodyJournal) != p.PreviousCustodyJournalSHA || custodyJournal.Owners[p.PreviousCustodyID] != p.PreviousCustodyUID {
		return bad()
	}
	var custody *deploymentplan.Resource
	for i := range custodyPlan.Resources {
		if custodyPlan.Resources[i].ID == p.PreviousCustodyID {
			custody = &custodyPlan.Resources[i]
		}
	}
	boundary = "current-custody-owner"
	if custody == nil || !secondSessionCustodyOwnerValid(ctx, store, e, reads, review, &p, *custody) {
		return bad()
	}
	boundary = "previous-credential-journal"
	credentialPlan, credentialJournal, err := acceptedCreateStage(store, e, "kubernetes/service-credential-secrets-renewal-"+p.PreviousReview)
	if err != nil || credentialPlan.SHA256() != p.PreviousCredentialPlanSHA || deploymentplan.Digest(credentialJournal) != p.PreviousCredentialJournalSHA || len(p.Intent.Namespaces) != len(p.PreviousCredentialUIDs) {
		return bad()
	}
	boundary = "previous-credential-owners"
	for _, namespace := range p.Intent.Namespaces {
		id := "Secret/" + namespace + "/service-bootstrap-" + p.Intent.SessionID
		var expected *deploymentplan.Resource
		for i := range credentialPlan.Resources {
			if credentialPlan.Resources[i].ID == id {
				expected = &credentialPlan.Resources[i]
			}
		}
		secret, err := reads.read(ctx, id)
		uid := p.PreviousCredentialUIDs[namespace]
		if expected == nil || err != nil || credentialJournal.Owners[id] != uid || !secondSessionCredentialValid(uid, expected.Desired, secret, p.KeySHA, p.PreviousCertSHA, service.Root.Certificate) {
			return bad()
		}
	}
	boundary = "openbao-trust"
	trust, err := reads.trust(ctx, openBaoRoot)
	if err != nil || trust != p.OpenBaoTrustSHA || len(p.Intent.Namespaces) == 0 {
		return bad()
	}
	boundary = "original-registry-state"
	original, err := reads.state(ctx, p.OriginalState.Session)
	if err != nil || !secondSessionTerminalHistoricalState(p.OriginalState, original, terminal != nil) {
		return bad()
	}
	boundary = "previous-registry-state"
	prior, err := reads.state(ctx, p.Intent.SessionID)
	if err != nil || !secondSessionTerminalHistoricalState(p.PreviousState, prior, terminal != nil) {
		return bad()
	}
	boundary = "historical-registry-states"
	checkedSessions := map[string]bool{p.OriginalState.Session: true, p.Intent.SessionID: true}
	for _, entry := range e.Journal.Entries {
		if !strings.HasPrefix(entry.Key, sessionLineagePrefix) {
			continue
		}
		history, loadErr := loadSecondSessionAdmission(store, e, strings.TrimPrefix(entry.Key, sessionLineagePrefix))
		if loadErr != nil {
			return bad()
		}
		saved := history.Renewal.PreviousState
		if checkedSessions[saved.Session] {
			continue
		}
		current, stateErr := reads.state(ctx, saved.Session)
		if stateErr != nil || !secondSessionTerminalHistoricalState(saved, current, terminal != nil) {
			return bad()
		}
		checkedSessions[saved.Session] = true
	}
	boundary = "current-registry-state"
	next, err := reads.state(ctx, p.RenewedIntent.SessionID)
	now = currentTime()
	if err != nil {
		return bad()
	}
	if terminal == nil {
		if !issuerEnrollmentSessionUsable(p.RenewedIntent, next, now) {
			return bad()
		}
	} else if deploymentplan.Digest(next) != deploymentplan.Digest(terminal.state) {
		return bad()
	}
	seen := map[string]bool{}
	for _, subject := range next.Installed {
		if seen[subject] || !slices.Contains(next.Subjects, subject) {
			return bad()
		}
		seen[subject] = true
	}
	boundary = "permanent-clients"
	clients, err := reads.clients(ctx)
	now = currentTime()
	clientProof := &createManagedSessionRecovery{PartialInventory: true, OriginalClientDigests: p.ClientDigests}
	if err != nil || !partialSessionClientsPreserved(clients, service, p.Intent.Subjects, original, clientProof, now) || !partialSessionClientsPreserved(clients, service, p.Intent.Subjects, prior, clientProof, now) || !partialSessionClientsPreserved(clients, service, p.Intent.Subjects, next, clientProof, now) {
		return bad()
	}
	read := func(path string) ([]byte, error) {
		return reads.command(ctx, nil, "--namespace", p.Intent.Namespaces[0], "exec", "deployment/"+service.CustodyReader, "--container", "custody", "--", "cat", path)
	}
	boundary = "custody-files"
	key, e1 := read("/run/pki-bootstrap/bootstrap.key")
	csr, e2 := read("/run/pki-bootstrap/bootstrap.csr")
	cert, e3 := read("/run/pki-bootstrap/renew-" + p.BindingSHA[:12] + "/bootstrap.crt")
	owner, e4 := reads.command(ctx, nil, "--namespace", p.Intent.Namespaces[0], "exec", "deployment/"+service.CustodyReader, "--container", "custody", "--", "stat", "-c", "%u:%g", "/run/pki-bootstrap")
	boundary = "final-current-state"
	now = currentTime()
	deadline, err = sessionEffectiveDeadline(ctx, store, e, q, now)
	if err != nil || !now.Before(deadline) || terminal == nil && !issuerEnrollmentSessionUsable(p.RenewedIntent, next, now) || !partialSessionClientsPreserved(clients, service, p.Intent.Subjects, original, clientProof, now) || !partialSessionClientsPreserved(clients, service, p.Intent.Subjects, prior, clientProof, now) || !partialSessionClientsPreserved(clients, service, p.Intent.Subjects, next, clientProof, now) {
		return bad()
	}
	credentialAt := now
	if terminal != nil {
		credentialAt = terminal.at
	}
	boundary = "same-key-custody-certificate"
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || strings.TrimSpace(string(owner)) != p.CustodyOwner || environmentPreflightBytesSHA256(key) != p.KeySHA || environmentPreflightBytesSHA256(csr) != p.CSRSHA || !partialSessionSuccessorCustodyValid(key, cert, []byte(service.Root.Certificate), csr, service, next, credentialAt) {
		return bad()
	}
	return e.Check(ctx)
}

// The second native session updates the same reader after signing. A retry
// accepts that update only when its own parent intent and child journal record
// the same UID and the only desired changes are the session and certificate.
func secondSessionCustodyOwnerValid(ctx context.Context, store secretStore, e *environmentCreateExecution, reads secondSessionReads, review string, q *secondSessionQualification, old deploymentplan.Resource) bool {
	if q == nil || old.ID != q.PreviousCustodyID || !environmentPreflightDigest.MatchString(q.PreviousBindingSHA) {
		return false
	}
	actual, err := reads.read(ctx, old.ID)
	if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != q.PreviousCustodyUID {
		return false
	}
	stage := "service-custody-reader-renewal-" + review
	next, err := readEnvironmentCreateStageInput(store, e, stage)
	if err != nil {
		return false
	}
	if deploymentplan.Matches(old.Desired, actual) {
		if next == nil {
			return true
		}
		journal, exists, err := readEnvironmentCreateStageProgress(store, e, stage, next)
		return err == nil && (!exists || journal.Owners[old.ID] == "")
	}
	if next == nil || len(next.Resources) != 1 {
		return false
	}
	journal, exists, err := readEnvironmentCreateStageProgress(store, e, stage, next)
	if err != nil || !exists || journal.Owners[old.ID] != q.PreviousCustodyUID {
		return false
	}
	updated := next.Resources[0]
	if updated.ID != old.ID || updated.Action != "update" || updated.UID != q.PreviousCustodyUID || !deploymentplan.Matches(old.Desired, updated.Baseline) {
		return false
	}
	expected := deploymentplan.Clone(old.Desired)
	containers := certIssuerObjectList(environmentPKIPod(expected)["containers"])
	if len(containers) != 1 {
		return false
	}
	c := deploymentplan.Map(containers[0])
	if mqttFoundationEnv(c, "PKI_BOOTSTRAP_SESSION_ID") != q.Intent.SessionID || mqttFoundationEnv(c, "PKI_BOOTSTRAP_CERT_PATH") != "/run/pki-bootstrap/renew-"+q.PreviousBindingSHA[:12]+"/bootstrap.crt" {
		return false
	}
	c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": q.RenewedIntent.SessionID, "PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/renew-" + q.BindingSHA[:12] + "/bootstrap.crt"}))
	return deploymentplan.Digest(expected) == deploymentplan.Digest(updated.Desired) && deploymentplan.Matches(updated.Desired, actual)
}

func secondSessionObservedExpiredState(saved, current managedSessionState) bool {
	return current.Status == "expired" && current.OtherActive == 1 && saved.Session == current.Session && saved.Issuer == current.Issuer && saved.Root == current.Root && saved.Fingerprint == current.Fingerprint && saved.Expires.Equal(current.Expires) && slices.Equal(saved.Subjects, current.Subjects) && slices.Equal(saved.Installed, current.Installed)
}
