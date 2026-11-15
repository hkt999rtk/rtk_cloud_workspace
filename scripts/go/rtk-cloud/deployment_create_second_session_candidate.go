package main

import (
	"context"
	"encoding/base64"
	"errors"
	"regexp"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// secondSessionQualification contains public evidence and hashes only. Its
// caller must durably admit its digest before any candidate is executed.
type secondSessionQualification struct {
	ParentSHA, OriginalReview, SourceStage, SourcePlanSHA, SourceJournalSHA                  string
	PreviousReview, PreviousBindingSHA                                                       string
	SourceJobID, SourceJobUID, PreviousStage, PreviousPlanSHA, PreviousJournalSHA            string
	PreviousJobID, PreviousJobUID                                                            string
	PreviousCredentialPlanSHA, PreviousCredentialJournalSHA                                  string
	PreviousCredentialUIDs                                                                   map[string]string
	PreviousCustodyPlanSHA, PreviousCustodyJournalSHA, PreviousCustodyID, PreviousCustodyUID string
	OriginalState, PreviousState                                                             managedSessionState
	ClientsSHA                                                                               string
	ClientDigests                                                                            []string
	KeySHA, CSRSHA, PreviousCertSHA, OpenBaoTrustSHA, BindingSHA                             string
	CustodyOwner                                                                             string
	BootstrapTTL                                                                             string
	Intent, RenewedIntent                                                                    createServiceSessionIntent
	Candidate                                                                                *deploymentplan.Plan
}

// The production adapter uses the normal Kubernetes backend and native
// registry readers. An isolated test can supply the same read-only operations.
type secondSessionReads struct {
	read      func(context.Context, string) (deploymentplan.Object, error)
	command   func(context.Context, []byte, ...string) ([]byte, error)
	inventory func(context.Context, string) ([]deploymentplan.Object, error)
	state     func(context.Context, string) (managedSessionState, error)
	clients   func(context.Context) ([]managedSessionClient, error)
	custody   func(context.Context, *createServiceSessionRecovery, *createManagedSessionRecovery) error
	trust     func(context.Context, environmentCreateRegisteredIssuer) (string, error)
	clock     func() time.Time
}

func secondSessionBinding(q *secondSessionQualification) string {
	if q == nil {
		return ""
	}
	copy := *q
	copy.BindingSHA, copy.RenewedIntent, copy.Candidate = "", createServiceSessionIntent{}, nil
	return deploymentplan.Digest(copy)
}

// prepareSecondSessionCandidate observes the original accepted Job, the
// already-admitted first renewal, and the native key/CSR/certificate. It does
// not sign, write an admission, or modify Kubernetes. A retry after admission
// must load that same admission rather than inventing a different candidate.
func prepareSecondSessionCandidate(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, plan environmentCreatePlan, service environmentCreateServiceCeremony, openBaoRoot environmentCreateRegisteredIssuer, now time.Time, predecessorReview ...string) (*secondSessionQualification, error) {
	return prepareSecondSessionCandidateWithReads(ctx, cfg, store, e, nativeSecondSessionReads(cfg, store, e, b), plan, service, openBaoRoot, now, predecessorReview...)
}

func nativeSecondSessionReads(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend) secondSessionReads {
	runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: b}
	return secondSessionReads{read: b.Read, command: b.command, inventory: b.Inventory, clock: time.Now,
		state: func(ctx context.Context, session string) (managedSessionState, error) {
			state, _, err := readManagedSessionState(ctx, cfg, runtime, session)
			return state, err
		},
		clients: func(ctx context.Context) ([]managedSessionClient, error) {
			return readManagedSessionClientRows(ctx, cfg, runtime)
		},
		custody: func(ctx context.Context, r *createServiceSessionRecovery, proof *createManagedSessionRecovery) error {
			return verifyManagedSeedOriginalCustody(ctx, runtime, r, proof)
		},
		trust: func(ctx context.Context, root environmentCreateRegisteredIssuer) (string, error) {
			return verifyPartialSessionOpenBaoTrust(ctx, store, e, b, root)
		},
	}
}

func prepareSecondSessionCandidateWithReads(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, reads secondSessionReads, plan environmentCreatePlan, service environmentCreateServiceCeremony, openBaoRoot environmentCreateRegisteredIssuer, now time.Time, predecessorReview ...string) (*secondSessionQualification, error) {
	boundary := "execution-owner"
	bad := func() (*secondSessionQualification, error) {
		return nil, createProviderRecoveryBlock("second session requires its admitted predecessor, original custody and unchanged native clients; boundary=" + boundary)
	}
	if e == nil || e.Store.Root != store.Root || now.IsZero() || plan.Stack != e.Journal.Stack || plan.Environment != cfg.Environment || e.Check(ctx) != nil {
		return bad()
	}
	boundary = "partial-admission"
	q, review, exists, err := loadPartialSessionAdmission(store, e)
	if err != nil || !exists || q.PartialSession == nil || q.Release != deploymentplan.Digest(plan.Release) {
		return bad()
	}
	p := q.PartialSession
	boundary = "session-lineage"
	head, previousReview, err := sessionLineageHead(store, e)
	if err != nil {
		return bad()
	}
	var admitted *secondSessionAdmission
	if len(predecessorReview) == 1 && head != nil && head.Renewal.PreviousReview == predecessorReview[0] {
		admitted = head
	}
	if len(predecessorReview) > 1 {
		return bad()
	}
	if len(predecessorReview) == 1 && predecessorReview[0] != previousReview {
		if head == nil || head.Renewal.PreviousReview != predecessorReview[0] {
			return bad()
		}
		previousReview = predecessorReview[0]
		if previousReview == review {
			head = nil
		} else {
			previous, err := loadSecondSessionAdmission(store, e, previousReview)
			if err != nil {
				return bad()
			}
			head = &previous
		}
	}
	previousIntent, previousBinding, retainedClients := p.RenewedIntent, p.Recovery.BindingSHA256, p.Recovery.OriginalClientDigests
	if head != nil {
		previousIntent, previousBinding, retainedClients = head.Renewal.RenewedIntent, head.Renewal.BindingSHA, head.Renewal.ClientDigests
	}
	if len(p.Intent.Namespaces) == 0 || service.CustodyReader == "" {
		return bad()
	}
	boundary = "custody-stage"
	custodyPlan, custodyJournal, err := acceptedCreateStage(store, e, "kubernetes/service-custody-reader-renewal-"+previousReview)
	if err != nil {
		return bad()
	}
	custodyID := "Deployment/" + p.Intent.Namespaces[0] + "/" + service.CustodyReader
	var custodyResource *deploymentplan.Resource
	for i := range custodyPlan.Resources {
		if custodyPlan.Resources[i].ID == custodyID {
			custodyResource = &custodyPlan.Resources[i]
		}
	}
	boundary = "custody-owner"
	custodyUID := custodyJournal.Owners[custodyID]
	custodyActual, err := reads.read(ctx, custodyID)
	if custodyResource == nil || err != nil || !environmentBootstrapOwnerUUID.MatchString(custodyUID) || custodyActual == nil || deploymentplan.Map(custodyActual["metadata"])["uid"] != custodyUID || !deploymentplan.Matches(custodyResource.Desired, custodyActual) {
		return bad()
	}
	boundary = "custody-volume"
	volumes, err := environmentPKINamedFields(environmentPKIPod(custodyResource.Desired)["volumes"])
	if err != nil || deploymentplan.Map(volumes["state"]["persistentVolumeClaim"])["claimName"] != p.Intent.Claim {
		return bad()
	}
	boundary = "predecessor-stage"
	stage, previous := "service-session-original-key-renewal-"+review, p.Candidate
	forward, forwardReview, forwarded, err := loadPartialSessionTrustForwardAdmission(store, e, q, review)
	if err != nil {
		return bad()
	}
	if head != nil {
		stage, previous = "service-session-original-key-renewal-"+previousReview, head.Renewal.Candidate
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
		failed, ok, err := p.Candidate.ReadExecution(store.Root)
		if err != nil || !ok || deploymentplan.Digest(failed) != f.FailedJournalSHA {
			return bad()
		}
		stage, previous = "partial-session-trust-forward-"+forwardReview, f.Candidate
	}
	if previous == nil || len(previous.Resources) != 1 || previous.Resources[0].Action != "create" || previous.Resources[0].Desired["kind"] != "Job" {
		return bad()
	}
	pj, ok, err := readEnvironmentCreateStageProgress(store, e, stage, previous)
	if err != nil || !ok {
		return bad()
	}
	boundary = "predecessor-job"
	prevJob := previous.Resources[0]
	prevUID := pj.Owners[prevJob.ID]
	actual, err := reads.read(ctx, prevJob.ID)
	if err != nil || !environmentBootstrapOwnerUUID.MatchString(prevUID) || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != prevUID || !deploymentplan.Matches(prevJob.Desired, actual) {
		return bad()
	}
	boundary = "source-job"
	source, sj, err := acceptedCreateStage(store, e, p.Recovery.SourceStage)
	sourceJob, ok := managedSessionSourceJob(source, sj)
	if err != nil || !ok || source.SHA256() != p.Recovery.SourcePlanSHA256 || deploymentplan.Digest(sj) != p.Recovery.SourceJournalSHA256 {
		return bad()
	}
	sourceUID := sj.Owners[sourceJob.ID]
	actual, err = reads.read(ctx, sourceJob.ID)
	if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != sourceUID || !deploymentplan.Matches(sourceJob.Desired, actual) {
		return bad()
	}
	boundary = "original-session"
	original, err := reads.state(ctx, p.Intent.SessionID)
	if err != nil {
		return bad()
	}
	boundary = "predecessor-session"
	predecessor, err := reads.state(ctx, previousIntent.SessionID)
	if err != nil {
		return bad()
	}
	boundary = "native-client-inventory"
	clients, err := reads.clients(ctx)
	if err != nil || !sessionLineagePredecessorValid(p, previousIntent, retainedClients, original, predecessor, service, clients, now) {
		return bad()
	}
	boundary = "historical-sessions"
	checkedSessions := map[string]bool{original.Session: true, predecessor.Session: true}
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
		if stateErr != nil || !partialSessionOriginalStatePreserved(saved, current, now) {
			return bad()
		}
		checkedSessions[saved.Session] = true
	}
	boundary = "original-custody"
	r := &createServiceSessionRecovery{Ceremony: service, OriginalKeySHA: p.KeySHA, OriginalCSRSHA: p.CSRSHA}
	if err := reads.custody(ctx, r, &p.Recovery); err != nil {
		var classified interface{ CheckCode() string }
		if errors.As(err, &classified) {
			return nil, err
		}
		return bad()
	}
	if len(p.Intent.Namespaces) == 0 {
		return bad()
	}
	read := func(path string) ([]byte, error) {
		return reads.command(ctx, nil, "--namespace", p.Intent.Namespaces[0], "exec", "deployment/"+service.CustodyReader, "--container", "custody", "--", "cat", path)
	}
	boundary = "predecessor-material"
	key, e1 := read("/run/pki-bootstrap/bootstrap.key")
	csr, e2 := read("/run/pki-bootstrap/bootstrap.csr")
	cert, e3 := read("/run/pki-bootstrap/renew-" + previousBinding[:12] + "/bootstrap.crt")
	if e1 != nil || e2 != nil || e3 != nil || environmentPreflightBytesSHA256(key) != p.KeySHA || environmentPreflightBytesSHA256(csr) != p.CSRSHA || !secondSessionExpiredPredecessorCustodyValid(key, cert, []byte(service.Root.Certificate), csr, service, predecessor, now) {
		return bad()
	}
	boundary = "custody-permissions"
	ownerRaw, err := reads.command(ctx, nil, "--namespace", p.Intent.Namespaces[0], "exec", "deployment/"+service.CustodyReader, "--container", "custody", "--", "stat", "-c", "%u:%g", "/run/pki-bootstrap")
	owner := strings.TrimSpace(string(ownerRaw))
	if err != nil || !regexp.MustCompile(`^[0-9]+:[0-9]+$`).MatchString(owner) {
		return bad()
	}
	boundary = "credential-owners"
	credentialPlan, credentialJournal, err := acceptedCreateStage(store, e, "kubernetes/service-credential-secrets-renewal-"+previousReview)
	if err != nil || len(credentialPlan.Resources) != len(p.Intent.Namespaces) {
		return bad()
	}
	credentialUIDs := make(map[string]string, len(p.Intent.Namespaces))
	for _, namespace := range p.Intent.Namespaces {
		id := "Secret/" + namespace + "/service-bootstrap-" + predecessor.Session
		var resource *deploymentplan.Resource
		for i := range credentialPlan.Resources {
			if credentialPlan.Resources[i].ID == id {
				resource = &credentialPlan.Resources[i]
			}
		}
		uid := credentialJournal.Owners[id]
		secret, readErr := reads.read(ctx, id)
		if resource == nil || readErr != nil || !secondSessionCredentialValid(uid, resource.Desired, secret, p.KeySHA, environmentPreflightBytesSHA256(cert), service.Root.Certificate) {
			return bad()
		}
		credentialUIDs[namespace] = uid
	}
	boundary = "managed-openbao-trust"
	trust, err := reads.trust(ctx, openBaoRoot)
	if err != nil {
		var blocker *deploymentplan.Blocker
		if errors.As(err, &blocker) && blocker.ID == "deployment.create.recovery" {
			return nil, err
		}
		return bad()
	}
	boundary = "selected-seed-image"
	image, _, err := selectedPartialSeedImage(plan)
	if err != nil {
		return bad()
	}
	out := &secondSessionQualification{ParentSHA: e.Journal.PlanSHA256, OriginalReview: review, SourceStage: p.Recovery.SourceStage, SourcePlanSHA: source.SHA256(), SourceJournalSHA: deploymentplan.Digest(sj), SourceJobID: sourceJob.ID, SourceJobUID: sourceUID, PreviousStage: stage, PreviousPlanSHA: previous.SHA256(), PreviousJournalSHA: deploymentplan.Digest(pj), PreviousJobID: prevJob.ID, PreviousJobUID: prevUID, PreviousCredentialPlanSHA: credentialPlan.SHA256(), PreviousCredentialJournalSHA: deploymentplan.Digest(credentialJournal), PreviousCredentialUIDs: credentialUIDs, OriginalState: original, PreviousState: predecessor, ClientsSHA: deploymentplan.Digest(clients), ClientDigests: partialSessionClientDigests(clients), KeySHA: p.KeySHA, CSRSHA: p.CSRSHA, PreviousCertSHA: environmentPreflightBytesSHA256(cert), OpenBaoTrustSHA: trust, CustodyOwner: owner, BootstrapTTL: cfg.Values["PKI_BOOTSTRAP_TTL"], Intent: previousIntent}
	out.PreviousReview, out.PreviousBindingSHA = previousReview, previousBinding
	out.PreviousCustodyPlanSHA, out.PreviousCustodyJournalSHA, out.PreviousCustodyID, out.PreviousCustodyUID = custodyPlan.SHA256(), deploymentplan.Digest(custodyJournal), custodyID, custodyUID
	boundary = "live-inventory"
	live, err := reads.inventory(ctx, plan.Stack)
	if err != nil {
		return bad()
	}
	boundary = "candidate"
	if admitted == nil {
		// Fresh qualification still requires genuine native target absence.
		if err := compileSecondSessionCandidate(cfg, plan, out, sourceJob.Desired, image, live); err != nil {
			return bad()
		}
	} else {
		// Rebuild the original recipe, as post-mint observation does, without
		// replacing its saved create plan with a plan for advanced live state.
		if err := compileSecondSessionCandidate(cfg, plan, out, sourceJob.Desired, image, nil); err != nil || deploymentplan.Digest(out) != deploymentplan.Digest(admitted.Renewal) {
			return bad()
		}
		if err := verifyAdmittedSecondSessionCandidate(store, e, *admitted, live); err != nil {
			return nil, err
		}
		out.Candidate = admitted.Renewal.Candidate
	}
	if e.Check(ctx) != nil {
		return bad()
	}
	return out, nil
}

func secondSessionExpiredPredecessorCustodyValid(key, cert, root, csr []byte, service environmentCreateServiceCeremony, state managedSessionState, now time.Time) bool {
	leaf, err := parseFirstCertificatePEM(cert)
	if err != nil || state.Expires.IsZero() || state.Expires.After(now) {
		return false
	}
	if leaf.NotAfter.After(now) {
		return partialSessionSuccessorCustodyValid(key, cert, root, csr, service, state, now)
	}
	return partialSessionCustodyValid(key, cert, root, csr, service, state, now)
}

func secondSessionCredentialValid(uid string, expected, secret deploymentplan.Object, keySHA, certSHA, root string) bool {
	if !environmentBootstrapOwnerUUID.MatchString(uid) || secret == nil || deploymentplan.Map(secret["metadata"])["uid"] != uid || secret["immutable"] != true || !deploymentplan.Matches(expected, secret) {
		return false
	}
	data := deploymentplan.Map(secret["data"])
	key, keyErr := base64.StdEncoding.DecodeString(deploymentplan.String(data["tls.key"]))
	cert, certErr := base64.StdEncoding.DecodeString(deploymentplan.String(data["tls.crt"]))
	actualRoot, rootErr := base64.StdEncoding.DecodeString(deploymentplan.String(data["ca.crt"]))
	return keyErr == nil && certErr == nil && rootErr == nil && environmentPreflightBytesSHA256(key) == keySHA && environmentPreflightBytesSHA256(cert) == certSHA && string(actualRoot) == root
}

// The inputs are authenticated by prepareSecondSessionCandidate. Split the
// deterministic recipe out so a saved admission can be checked independently.
func compileSecondSessionCandidate(cfg deploymentConfig, plan environmentCreatePlan, out *secondSessionQualification, source deploymentplan.Object, image environmentReleaseImage, live []deploymentplan.Object) error {
	if out == nil || out.BootstrapTTL != cfg.Values["PKI_BOOTSTRAP_TTL"] || !regexp.MustCompile(`^[0-9]+:[0-9]+$`).MatchString(out.CustodyOwner) {
		return createProviderRecoveryBlock("second session source is unavailable")
	}
	out.BindingSHA = secondSessionBinding(out)
	out.RenewedIntent = out.Intent
	out.RenewedIntent.SessionID = sessionRenewalUUID(out.ParentSHA, out.BindingSHA, out.Intent.SessionID)
	definition, err := managedSessionRenewalDefinition(source, out.ParentSHA, out.BindingSHA, out.RenewedIntent.SessionID, image, plan.Stack)
	if err != nil {
		return err
	}
	init := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(definition.Object)["initContainers"])[0])
	command := certIssuerObjectList(init["command"])
	if len(command) != 3 {
		return createProviderRecoveryBlock("second session original custody preparation is unavailable")
	}
	shell := deploymentplan.String(command[2])
	if strings.Count(shell, "chown 10001:10001 ") != 2 {
		return createProviderRecoveryBlock("second session original custody preparation changed")
	}
	command[2] = strings.ReplaceAll(shell, "chown 10001:10001 ", "chown "+out.CustodyOwner+" ")
	init["command"] = command
	container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(definition.Object)["containers"])[0])
	container["env"] = environmentCreateServiceWithoutEnv(container["env"], "PKI_BOOTSTRAP_TTL")
	if out.BootstrapTTL != "" {
		container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_TTL": out.BootstrapTTL}))
	}
	out.Candidate, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: plan.Stack, Operation: "scoped", ConfigurationSHA256: plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("managed-seed-original-key-renewal", []deploymentplan.Definition{definition})}, []deploymentplan.Definition{definition}, live)
	if err != nil || out.Candidate == nil || len(out.Candidate.Resources) != 1 || out.Candidate.Resources[0].Action != "create" {
		return createProviderRecoveryBlock("second session candidate already has native progress or is invalid")
	}
	return nil
}

// Re-observe real native presence separately from reconstructing the saved
// recipe. A retained Job is eligible only through its original parent intent,
// immutable input and recorded child/Lease owner; it is never a new create.
func verifyAdmittedSecondSessionCandidate(store secretStore, e *environmentCreateExecution, q secondSessionAdmission, live []deploymentplan.Object) error {
	bad := func() error {
		return createProviderRecoveryBlock("admitted second session Job differs from its original native owner or recipe")
	}
	if !validSecondSessionAdmission(store, e, q) || q.Renewal.Candidate == nil || len(q.Renewal.Candidate.Resources) != 1 {
		return bad()
	}
	p := q.Renewal.Candidate
	stage := "service-session-original-key-renewal-" + deploymentplan.Digest(q)
	saved, err := readEnvironmentCreateStageInput(store, e, stage)
	if err != nil || saved != nil && saved.SHA256() != p.SHA256() {
		return bad()
	}
	journal, found, err := readEnvironmentCreateStageProgress(store, e, stage, p)
	if err != nil || found && saved == nil {
		return bad()
	}
	r := p.Resources[0]
	observed, err := deploymentplan.Compile(p.Inputs, []deploymentplan.Definition{{Object: deploymentplan.Clone(r.Desired), Phase: r.Phase}}, live)
	if err != nil || len(observed.Resources) != 1 {
		return bad()
	}
	current := observed.Resources[0]
	if current.ID != r.ID {
		return bad()
	}
	switch current.Action {
	case "create":
		if found && (journal.Owners[r.ID] != "" || journal.InFlight != "") {
			return bad()
		}
	case "retain":
		if !found || current.UID == "" || journal.Owners[r.ID] != current.UID {
			return bad()
		}
	default:
		return bad()
	}
	return nil
}
