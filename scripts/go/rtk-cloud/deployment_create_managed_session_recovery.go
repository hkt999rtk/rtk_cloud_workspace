package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A complete CA ceremony does not imply that every native host has acknowledged
// its Service identity. Renew only the expired deployment seed on its original
// key/CSR. The following managed runtime stages produce their own acknowledgments.
type createManagedSessionRecovery struct {
	PartialInventory          bool                 `json:"PartialInventory,omitempty"`
	OriginalState             *managedSessionState `json:"OriginalState,omitempty"`
	OriginalClientDigests     []string             `json:"OriginalClientDigests,omitempty"`
	OriginalSession           string
	OriginalCertificatePath   string
	OriginalCertificateSHA256 string
	SessionSHA256             string
	ClientsSHA256             string
	SourceStage               string
	SourcePlanSHA256          string
	SourceJournalSHA256       string
	BindingSHA256             string
	CandidateSHA256           string
}

type managedSessionState struct {
	Session     string    `json:"session_id"`
	Issuer      string    `json:"issuer_id"`
	Root        string    `json:"root_sha256"`
	Fingerprint string    `json:"bootstrap_fingerprint"`
	Status      string    `json:"status"`
	Expires     time.Time `json:"expires_at"`
	Subjects    []string  `json:"subjects"`
	Installed   []string  `json:"installed_subjects"`
	OtherActive int       `json:"other_active"`
}

func managedSessionMayRenew(state managedSessionState, service environmentCreateServiceCeremony, subjects []string, now time.Time) bool {
	if state.Session != service.BootstrapSessionID || state.Issuer != service.Intermediate.ID || state.Root != service.Root.RootFingerprint || !environmentPreflightDigest.MatchString(state.Fingerprint) || state.Status != "active" && state.Status != "expired" || state.Expires.IsZero() || state.Expires.After(now) || state.OtherActive != 0 || len(subjects) == 0 || !slices.Equal(state.Subjects, subjects) {
		return false
	}
	seen := map[string]bool{}
	for _, subject := range state.Installed {
		if seen[subject] || !slices.Contains(subjects, subject) {
			return false
		}
		seen[subject] = true
	}
	return true
}

func managedSeedSubjects(cfg deploymentConfig, plan environmentCreateBootstrapPlan, r *createServiceSessionRecovery) ([]string, error) {
	if r == nil || r.ManagedTransportRecovery == nil || !r.ManagedTransportRecovery.PartialSessionRenewal {
		return environmentCreateBootstrapSubjects(cfg)
	}
	policy := findEnvironmentCreateDomainPolicy(plan, "service")
	seen := map[string]bool{}
	for _, subject := range r.Intent.Subjects {
		if seen[subject] || subject == "service:deployment-bootstrap" || !slices.Contains(policy.ServiceClientIDs, subject) {
			return nil, createProviderRecoveryBlock("original partial session subjects differ from the qualified Service issuer policy")
		}
		seen[subject] = true
	}
	if len(seen) == 0 {
		return nil, createProviderRecoveryBlock("original partial session subjects are unavailable")
	}
	return slices.Clone(r.Intent.Subjects), nil
}

func selectedPartialSeedImage(plan environmentCreatePlan) (environmentReleaseImage, string, error) {
	image := plan.Release.Images["LKE_VIDEO_CLOUD_IMAGE"]
	if !immutableEnvironmentImage.MatchString(image.Reference) || image.Reference != plan.Bootstrap.Image {
		return environmentReleaseImage{}, "", createProviderRecoveryBlock("partial session renewal requires the qualified selected bootstrap image")
	}
	return image, deploymentplan.Digest(image), nil
}

func readManagedSessionState(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, session string) (managedSessionState, []byte, error) {
	var state managedSessionState
	if !environmentBootstrapOwnerUUID.MatchString(session) {
		return state, nil, createProviderRecoveryBlock("managed seed inspection requires the original canonical session")
	}
	query := "SELECT json_build_object('session_id',session_id,'issuer_id',issuer_id,'root_sha256',root_sha256,'bootstrap_fingerprint',bootstrap_fingerprint,'status',status,'expires_at',expires_at,'subjects',subjects,'installed_subjects',installed_subjects,'other_active',(SELECT count(*) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND status='active' AND expires_at>now() AND session_id<>'" + session + "')) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND session_id='" + session + "'"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	if err != nil {
		return state, nil, secretCheckFailure(err, "native managed seed lineage cannot be inspected")
	}
	if decodeEnvironmentJSON(raw, &state) != nil {
		return state, nil, createProviderRecoveryBlock("native managed seed lineage response is malformed")
	}
	return state, raw, nil
}

type managedSessionClient struct {
	Subject     string     `json:"subject"`
	Caller      string     `json:"caller"`
	Request     string     `json:"request_id"`
	Issuer      string     `json:"issuer_id"`
	Fingerprint string     `json:"fingerprint"`
	Status      string     `json:"status"`
	Revoked     *time.Time `json:"revoked_at"`
	CSR         string     `json:"csr_pem"`
	Certificate string     `json:"certificate_pem"`
}

// Three independently owned API clients share one Service subject. All other
// declared workload subjects have one original client. Seed rows are excluded.
func validateManagedSessionClients(rows []managedSessionClient, service environmentCreateServiceCeremony, subjects []string, now time.Time) bool {
	return validateManagedSessionClientsMode(rows, service, subjects, now, true)
}

func validateManagedSessionClientsPreserved(rows []managedSessionClient, service environmentCreateServiceCeremony, subjects []string) bool {
	return validateManagedSessionClientsMode(rows, service, subjects, time.Time{}, false)
}

func validateManagedSessionClientsMode(rows []managedSessionClient, service environmentCreateServiceCeremony, subjects []string, now time.Time, requireCurrent bool) bool {
	return validateManagedSessionClientInventory(rows, service, subjects, nil, now, requireCurrent, true)
}

// A session may expire between issuance and installation. Preserve all
// existing signed clients and require each recorded installation to have an
// issuance, without treating the unissued remainder as a failed deployment.
func validatePartialManagedSessionClients(rows []managedSessionClient, service environmentCreateServiceCeremony, subjects, installed []string, now time.Time) bool {
	return validateManagedSessionClientInventory(rows, service, subjects, installed, now, true, false)
}

func validateManagedSessionClientInventory(rows []managedSessionClient, service environmentCreateServiceCeremony, subjects, installed []string, now time.Time, requireCurrent, requireComplete bool) bool {
	expected := map[string]int{}
	for _, subject := range subjects {
		if expected[subject] != 0 || subject == "service:deployment-bootstrap" {
			return false
		}
		expected[subject] = 1
	}
	if requireComplete && expected["service:video-cloud-api"] != 1 {
		return false
	}
	if expected["service:video-cloud-api"] == 1 {
		expected["service:video-cloud-api"] += 2
	}
	seen := map[string]bool{}
	issued := map[string]bool{}
	intermediate, err := parseFirstCertificatePEM([]byte(service.Intermediate.Certificate))
	if err != nil {
		return false
	}
	for _, row := range rows {
		if expected[row.Subject] == 0 || row.Caller == "" || row.Request == "" || row.Issuer != service.Intermediate.ID || row.Status != "succeeded" || row.Revoked != nil || !environmentPreflightDigest.MatchString(row.Fingerprint) || seen[row.Fingerprint] {
			return false
		}
		leaf, err := parseFirstCertificatePEM([]byte(row.Certificate))
		block, rest := pem.Decode([]byte(row.CSR))
		if err != nil || leaf.Subject.CommonName != row.Subject || !leaf.NotAfter.After(leaf.NotBefore) || requireCurrent && (!leaf.NotAfter.After(now) || leaf.NotBefore.After(now)) || leaf.CheckSignatureFrom(intermediate) != nil || environmentPreflightBytesSHA256(leaf.Raw) != row.Fingerprint || block == nil || block.Type != "CERTIFICATE REQUEST" || strings.TrimSpace(string(rest)) != "" {
			return false
		}
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil || csr.CheckSignature() != nil || csr.Subject.CommonName != row.Subject || !slices.Equal(csr.RawSubjectPublicKeyInfo, leaf.RawSubjectPublicKeyInfo) {
			return false
		}
		clientAuth := false
		for _, usage := range leaf.ExtKeyUsage {
			if usage == x509.ExtKeyUsageClientAuth {
				clientAuth = true
			}
		}
		if !clientAuth {
			return false
		}
		expected[row.Subject]--
		seen[row.Fingerprint] = true
		issued[row.Subject] = true
	}
	if requireComplete {
		for _, count := range expected {
			if count != 0 {
				return false
			}
		}
	}
	acknowledged := map[string]bool{}
	for _, subject := range installed {
		if acknowledged[subject] || !issued[subject] {
			return false
		}
		acknowledged[subject] = true
	}
	return true
}

func readManagedSessionClients(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, subjects []string) (string, error) {
	return readManagedSessionClientsMode(ctx, cfg, runtime, service, subjects, true)
}

func readManagedSessionClientsPreserved(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, subjects []string) (string, error) {
	return readManagedSessionClientsMode(ctx, cfg, runtime, service, subjects, false)
}

func readManagedSessionClientsMode(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, subjects []string, requireCurrent bool) (string, error) {
	clients, err := readManagedSessionClientRows(ctx, cfg, runtime)
	if err != nil || !validateManagedSessionClientsMode(clients, service, subjects, time.Now().UTC(), requireCurrent) {
		return "", createProviderRecoveryBlock("managed seed renewal requires every unchanged original native workload client and no foreign or partial issuance")
	}
	return deploymentplan.Digest(clients), nil
}

func readManagedSessionClientRows(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime) ([]managedSessionClient, error) {
	query := "SELECT COALESCE(json_agg(json_build_object('subject',subject,'caller',caller,'request_id',request_id,'issuer_id',issuer_id,'fingerprint',fingerprint,'status',status,'revoked_at',revoked_at,'csr_pem',csr_pem,'certificate_pem',certificate_pem) ORDER BY subject,caller,request_id),'[]'::json) FROM pki_service_client_issuances WHERE environment='" + cfg.Environment + "' AND subject<>'service:deployment-bootstrap'"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var clients []managedSessionClient
	if err != nil || decodeEnvironmentJSON(raw, &clients) != nil {
		return nil, createProviderRecoveryBlock("original native workload client inventory cannot be inspected")
	}
	return clients, nil
}

func readManagedSessionInventory(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, subjects []string, state managedSessionState, partial bool) (string, error) {
	if !partial {
		return readManagedSessionClients(ctx, cfg, runtime, service, subjects)
	}
	clients, err := readManagedSessionClientRows(ctx, cfg, runtime)
	if err != nil || !validatePartialManagedSessionClients(clients, service, subjects, state.Installed, time.Now().UTC()) {
		return "", createProviderRecoveryBlock("partial session recovery requires the original issued identities and their recorded installations")
	}
	return deploymentplan.Digest(clients), nil
}

func partialSessionClientDigests(clients []managedSessionClient) []string {
	digests := make([]string, 0, len(clients))
	for _, client := range clients {
		digests = append(digests, deploymentplan.Digest(client))
	}
	slices.Sort(digests)
	return digests
}

func partialSessionClientsPreserved(clients []managedSessionClient, service environmentCreateServiceCeremony, subjects []string, state managedSessionState, proof *createManagedSessionRecovery, now time.Time) bool {
	if proof == nil || !proof.PartialInventory || !validatePartialManagedSessionClients(clients, service, subjects, state.Installed, now) {
		return false
	}
	current := partialSessionClientDigests(clients)
	for _, original := range proof.OriginalClientDigests {
		if !slices.Contains(current, original) {
			return false
		}
	}
	return true
}

func managedSessionRenewalDefinition(original deploymentplan.Object, parent, binding, session string, image environmentReleaseImage, managedStack ...string) (deploymentplan.Definition, error) {
	if !immutableEnvironmentImage.MatchString(image.Reference) || original["kind"] != "Job" || len(certIssuerObjectList(environmentPKIPod(original)["containers"])) != 1 {
		return deploymentplan.Definition{}, createProviderRecoveryBlock("managed seed renewal requires the exact qualified formal repair image")
	}
	object := deploymentplan.Clone(original)
	c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(object)["containers"])[0])
	c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/bootstrap.crt"}))
	definition, err := originalKeyRenewalDefinition(object, parent, binding, session)
	if err != nil {
		return definition, err
	}
	c = deploymentplan.Map(certIssuerObjectList(environmentPKIPod(definition.Object)["containers"])[0])
	c["image"] = image.Reference
	c["env"] = environmentCreateServiceWithoutEnv(c["env"], "PKI_BOOTSTRAP_EXPIRES_AT")
	c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_TTL": "8h"}))
	if len(managedStack) != 0 {
		if len(managedStack) != 1 {
			return deploymentplan.Definition{}, createProviderRecoveryBlock("one selected managed OpenBao trust is required")
		}
		bound, err := bindEnvironmentCreateManagedOpenBaoTrust([]deploymentplan.Definition{definition}, managedStack[0])
		if err != nil {
			return deploymentplan.Definition{}, err
		}
		definition = bound[0]
	}
	return definition, nil
}

// The first session stage contains the bootstrap Job together with its PVC
// and ServiceAccount; later renewal stages contain only the Job. In both
// cases use the one journal-owned original Job as the renewal recipe.
func managedSessionSourceJob(plan *deploymentplan.Plan, journal deploymentplan.Journal) (deploymentplan.Resource, bool) {
	var job deploymentplan.Resource
	if plan == nil || !journal.Accepted || (len(plan.Resources) != 1 && len(plan.Resources) != 3) {
		return job, false
	}
	kinds := map[string]bool{}
	for _, resource := range plan.Resources {
		kind, _ := resource.Desired["kind"].(string)
		if kinds[kind] || (kind != "Job" && kind != "PersistentVolumeClaim" && kind != "ServiceAccount") || !environmentBootstrapOwnerUUID.MatchString(journal.Owners[resource.ID]) {
			return deploymentplan.Resource{}, false
		}
		kinds[kind] = true
		if kind != "Job" {
			continue
		}
		if job.ID != "" {
			return deploymentplan.Resource{}, false
		}
		job = resource
	}
	return job, job.ID != ""
}

func prepareManagedSessionRecovery(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery) error {
	partial := r.ManagedTransportRecovery != nil && r.ManagedTransportRecovery.PartialSessionRenewal
	checkpoint := isManagedTransportCheckpoint(e.Journal)
	if partial {
		_, checkpoint, _ = initialSuccessorContinuationEntry(store, e)
	}
	if r.ManagedTransportRecovery == nil || !r.PostServiceRenewal || !r.Renewal || !checkpoint || !environmentPreflightDigest.MatchString(r.OriginalKeySHA) || !environmentPreflightDigest.MatchString(r.OriginalCSRSHA) {
		return createProviderRecoveryBlock("managed seed renewal requires the authenticated all-CA-ACTIVE checkpoint and original key/CSR custody")
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: b}
	subjects, err := managedSeedSubjects(cfg, p.Plan.Bootstrap, r)
	if err != nil {
		return err
	}
	state, raw, err := readManagedSessionState(ctx, cfg, runtime, r.Ceremony.BootstrapSessionID)
	if err != nil || !managedSessionMayRenew(state, r.Ceremony, subjects, time.Now().UTC()) {
		return createProviderRecoveryBlock("managed seed renewal requires its genuinely expired original allowlist and no competing active session")
	}
	clients, err := readManagedSessionInventory(ctx, cfg, runtime, r.Ceremony, subjects, state, partial)
	if err != nil {
		return err
	}
	original, journal, err := acceptedCreateStage(store, e, r.SourceRenewalStage)
	resource, ok := managedSessionSourceJob(original, journal)
	if err != nil || !ok {
		return createProviderRecoveryBlock("managed seed renewal requires its exact accepted original-key renewal Job")
	}
	actual, err := b.Read(ctx, resource.ID)
	if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != journal.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, actual) {
		return createProviderRecoveryBlock("original accepted seed renewal Job owner changed")
	}
	var image environmentReleaseImage
	var selection string
	if partial {
		image, selection, err = selectedPartialSeedImage(p.Plan)
		if err != nil {
			return err
		}
		if r.OpenBaoRoot == nil {
			return createProviderRecoveryBlock("partial session renewal requires the original managed OpenBao root")
		}
		trust, trustErr := verifyPartialSessionOpenBaoTrust(ctx, store, e, b, *r.OpenBaoRoot)
		if trustErr != nil {
			return trustErr
		}
		p.Materials["partial-session-managed-openbao-trust"] = trust
	} else {
		image, selection, err = loadServiceTrustRepairImage(ctx, cfg, store, e.Journal.PlanSHA256)
		if err != nil {
			return err
		}
	}
	binding := deploymentplan.Digest(map[string]string{"parent": e.Journal.PlanSHA256, "source_review": r.ManagedTransportRecovery.SourceReview, "source_plan": original.SHA256(), "original_session": state.Session, "clients": clients})
	oldPath := r.RenewedCertPath
	material, err := b.command(ctx, nil, "--namespace", r.Ceremony.BootstrapCredentialNamespace, "exec", "deployment/"+r.Ceremony.CustodyReader, "--container", "custody", "--", "cat", oldPath)
	leaf, parseErr := parseFirstCertificatePEM(material)
	if err != nil || parseErr != nil || leaf.NotAfter.After(time.Now().UTC()) || environmentPreflightBytesSHA256(leaf.Raw) != state.Fingerprint {
		return createProviderRecoveryBlock("original expired seed certificate differs from its native receipt")
	}
	r.GrantImage = image
	r.RenewalBindingSHA = binding
	r.RenewedIntent.SessionID = sessionRenewalUUID(e.Journal.PlanSHA256, binding, state.Session)
	var trustStack []string
	if partial {
		trustStack = []string{p.Plan.Stack}
	}
	definition, err := managedSessionRenewalDefinition(resource.Desired, e.Journal.PlanSHA256, binding, r.RenewedIntent.SessionID, image, trustStack...)
	if err != nil {
		return err
	}
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return err
	}
	r.RenewalCandidate, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("managed-seed-original-key-renewal", []deploymentplan.Definition{definition})}, []deploymentplan.Definition{definition}, live)
	if err != nil {
		return err
	}
	if len(r.RenewalCandidate.Resources) != 1 || r.RenewalCandidate.Resources[0].Action != "create" {
		return createProviderRecoveryBlock("managed seed renewal already has native progress; reconcile its original journal")
	}
	r.RenewedCertPath = "/run/pki-bootstrap/renew-" + binding[:12] + "/bootstrap.crt"
	r.ManagedTransportRecovery.SessionRenewal = &createManagedSessionRecovery{PartialInventory: partial, OriginalSession: state.Session, OriginalCertificatePath: oldPath, OriginalCertificateSHA256: environmentPreflightBytesSHA256(material), SessionSHA256: environmentPreflightBytesSHA256(raw), ClientsSHA256: clients, SourceStage: r.SourceRenewalStage, SourcePlanSHA256: original.SHA256(), SourceJournalSHA256: deploymentplan.Digest(journal), BindingSHA256: binding, CandidateSHA256: r.RenewalCandidate.SHA256()}
	if partial {
		r.ManagedTransportRecovery.SessionRenewal.OriginalState = &state
		rows, readErr := readManagedSessionClientRows(ctx, cfg, runtime)
		if readErr != nil || !validatePartialManagedSessionClients(rows, r.Ceremony, subjects, state.Installed, time.Now().UTC()) || deploymentplan.Digest(rows) != clients {
			return createProviderRecoveryBlock("original partial session workload identities changed during qualification")
		}
		r.ManagedTransportRecovery.SessionRenewal.OriginalClientDigests = partialSessionClientDigests(rows)
	}
	p.Materials["accepted-service-core-trust-repair-selection"] = selection
	p.Materials["accepted-service-core-trust-repair-image"] = deploymentplan.Digest(image)
	p.Materials["accepted-service-core-managed-session-renewal"] = deploymentplan.Digest(r.ManagedTransportRecovery.SessionRenewal)
	p.Materials["accepted-service-core-used-renewal-plan"] = r.RenewalCandidate.SHA256()
	return e.Check(ctx)
}

func verifyManagedSeedOriginalCustody(ctx context.Context, runtime environmentCreateBootstrapRuntime, r *createServiceSessionRecovery, proof *createManagedSessionRecovery) error {
	for path, want := range map[string]string{"/run/pki-bootstrap/bootstrap.key": r.OriginalKeySHA, "/run/pki-bootstrap/bootstrap.csr": r.OriginalCSRSHA, proof.OriginalCertificatePath: proof.OriginalCertificateSHA256} {
		if !strings.HasPrefix(path, "/run/pki-bootstrap/") || !environmentPreflightDigest.MatchString(want) {
			return environmentCreateBootstrapBlocker("managed seed original custody binding is invalid")
		}
		var raw []byte
		var err error
		for attempt := 0; attempt < deploymentReadMaxAttempts; attempt++ {
			if err = runtime.Execution.Check(ctx); err != nil {
				return err
			}
			raw, err = runtime.Backend.command(ctx, nil, "--namespace", r.Ceremony.BootstrapCredentialNamespace, "exec", "deployment/"+r.Ceremony.CustodyReader, "--container", "custody", "--", "cat", path)
			if err == nil || !transientDeploymentReadError(err) || attempt+1 == deploymentReadMaxAttempts {
				break
			}
			timer := time.NewTimer(newDeploymentCheckRuntime(ctx, runtime.Backend.Store.Environment).retryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				return classifyDeploymentRuntimeError(ctx, nil, ctx.Err(), false)
			case <-timer.C:
			}
		}
		if err != nil {
			return secretCheckFailure(err, "original bootstrap custody read unavailable")
		}
		if environmentPreflightBytesSHA256(raw) != want {
			return environmentCreateBootstrapBlocker("managed seed original key/CSR/certificate custody changed")
		}
	}
	return runtime.Execution.Check(ctx)
}

// Completed renewals keep their original client snapshot. Later declared
// product enrollments are verified by the same native successor observer;
// they never rerun the producer or replace its immutable completion evidence.
func observeCompletedManagedSessionRecovery(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, runtime environmentCreateBootstrapRuntime) (environmentCreateServiceCeremony, bool, error) {
	if finalized, err := observeFinalizedFreshBootstrap(ctx, cfg, store, runtime.Execution, runtime.Backend); err != nil {
		return environmentCreateServiceCeremony{}, false, err
	} else if finalized != nil {
		return finalized.Ceremony, true, nil
	}
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if r == nil || !r.SecondSession {
		return environmentCreateServiceCeremony{}, false, nil
	}
	service := r.Ceremony
	bad := func() (environmentCreateServiceCeremony, bool, error) {
		return service, false, createProviderRecoveryBlock("completed Service session requires its original preserved-client proof and native successor")
	}
	if e == nil || e.Store != store || runtime.Backend.Store != store || runtime.ExecutionSHA256 != e.Journal.PlanSHA256 || r.OpenBaoRoot == nil || e.Check(ctx) != nil {
		return bad()
	}
	q, err := loadSecondSessionAdmission(store, e, r.Review)
	if err != nil {
		return service, false, err
	}
	done, err := completedServiceSessionBatch(store, e, q, r.Review)
	if err != nil || !done {
		return service, false, err
	}
	path := filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-renewal", r.Review, "managed-clients-preserved.json")
	if _, err := os.Lstat(path); os.IsNotExist(err) && createOpenBaoACKCompletionEntry(e.Journal, "product-identities/fresh") == nil {
		// A producer interrupted before its final local proof still follows
		// the original completion path. Downstream products never authorize
		// reconstructing missing original evidence.
		return service, false, nil
	} else if err != nil {
		return bad()
	}
	want := map[string]string{"original_session_id": q.Renewal.Intent.SessionID, "renewed_session_id": q.Renewal.RenewedIntent.SessionID, "clients_sha256": q.Renewal.ClientsSHA, "original_key_sha256": q.Renewal.KeySHA, "original_csr_sha256": q.Renewal.CSRSHA}
	var preserved map[string]string
	if readExecutionJSON(path, &preserved) != nil || deploymentplan.Digest(preserved) != deploymentplan.Digest(want) || r.OriginalKeySHA != q.Renewal.KeySHA || r.OriginalCSRSHA != q.Renewal.CSRSHA || deploymentplan.Digest(r.RenewedIntent) != deploymentplan.Digest(q.Renewal.RenewedIntent) {
		return bad()
	}
	selected, err := qualifiedSecondSessionPlan(store, e, r.qualifiedPlan, r.RenewalCandidate)
	if err != nil || deploymentplan.Digest(selected.Bootstrap) != deploymentplan.Digest(p) {
		return bad()
	}
	if err := observeSecondSessionSuccessor(ctx, cfg, store, e, runtime.Backend, selected, service, *r.OpenBaoRoot, r.Review, time.Now().UTC()); err != nil {
		return service, false, err
	}
	for _, stage := range []string{"service-custody-reader-renewal-", "service-credential-secrets-renewal-"} {
		if _, err := verifyAcceptedStageAfterRecordedHistory(ctx, store, e, runtime.Backend, "kubernetes/"+stage+r.Review); err != nil {
			return service, false, err
		}
	}
	var observed createServiceSessionObserved
	if readExecutionJSON(filepath.Join(filepath.Dir(path), "observed.json"), &observed) != nil {
		return bad()
	}
	current, err := freshProductResumeCurrentCeremony(store, e, q, r.Review, service)
	if err != nil {
		return service, false, err
	}
	// The remaining-bootstrap renderer scopes the cleanup map later. Return
	// the literal original producer ceremony at this earlier boundary.
	current.CleanupBinding.ConfigMapName = observed.Cleanup.ConfigMapName
	if deploymentplan.Digest(current.CleanupBinding) != deploymentplan.Digest(observed.Cleanup) || deploymentplan.Digest(current.CredentialUIDs) != deploymentplan.Digest(observed.UIDs) || current.CustodyReader != observed.Reader {
		return bad()
	}
	return current, true, e.Check(ctx)
}

// This path executes only the qualified seed Job. Each retained runtime later
// acknowledges its current identity through mTLS; there is no enrollment replay,
// identity quiesce, synthetic installation receipt or private-key relocation.
func executeManagedSessionRecovery(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, runtime environmentCreateBootstrapRuntime, apply environmentCreateBootstrapApply) (environmentCreateServiceCeremony, error) {
	r := runtime.ServiceSessionRecovery
	if r == nil || r.ManagedTransportRecovery == nil || r.ManagedTransportRecovery.SessionRenewal == nil || !r.PostServiceRenewal || r.RenewalCandidate == nil {
		return environmentCreateServiceCeremony{}, environmentCreateBootstrapBlocker("qualified managed seed renewal is unavailable")
	}
	proof := r.ManagedTransportRecovery.SessionRenewal
	service := r.Ceremony
	subjects, err := managedSeedSubjects(cfg, p, r)
	if err != nil {
		return service, err
	}
	if current, completed, err := observeCompletedManagedSessionRecovery(ctx, cfg, p, store, runtime); err != nil || completed {
		return current, err
	}
	state, raw, err := readManagedSessionState(ctx, cfg, runtime, proof.OriginalSession)
	minted := false
	expectedCandidate := proof.CandidateSHA256
	if f := r.ManagedTransportRecovery.PartialTrustForward; f != nil {
		if !proof.PartialInventory || f.OriginalReview != r.Review || validateManagedLateSeedForwardCandidate(f, f.Candidate) != nil {
			return service, environmentCreateBootstrapBlocker("partial session trust forward candidate is invalid")
		}
		expectedCandidate = f.Candidate.SHA256()
	}
	if err != nil || r.RenewalCandidate.SHA256() != expectedCandidate {
		return service, environmentCreateBootstrapBlocker("qualified original expired seed changed before renewal")
	}
	unchanged := environmentPreflightBytesSHA256(raw) == proof.SessionSHA256
	if proof.PartialInventory && proof.OriginalState != nil && partialSessionOriginalStatePreserved(*proof.OriginalState, state, time.Now().UTC()) {
		unchanged = true
	}
	if r.SecondSession && managedSessionMayRenew(state, service, subjects, time.Now().UTC()) && unchanged {
		admission, loadErr := loadSecondSessionAdmission(store, runtime.Execution, r.Review)
		originalPlan, planErr := qualifiedSecondSessionPlan(store, runtime.Execution, r.qualifiedPlan, r.RenewalCandidate)
		now := time.Now().UTC()
		if loadErr != nil {
			return service, loadErr
		}
		if planErr != nil {
			return service, planErr
		}
		deadline, deadlineErr := sessionEffectiveDeadline(ctx, store, runtime.Execution, admission, now)
		if deadlineErr != nil {
			return service, deadlineErr
		}
		if r.OpenBaoRoot == nil || now.Before(admission.QualifiedAt) || !now.Before(deadline) {
			return service, environmentCreateBootstrapBlocker("second session requires its original live qualification and execution deadline")
		}
		current, observeErr := prepareSecondSessionCandidate(ctx, cfg, store, runtime.Execution, runtime.Backend, originalPlan, service, *r.OpenBaoRoot, now, admission.Renewal.PreviousReview)
		if observeErr != nil || deploymentplan.Digest(current) != deploymentplan.Digest(admission.Renewal) {
			return service, environmentCreateBootstrapBlocker("second session live prerequisites differ from the admitted original")
		}
		backend := environmentCreateKubernetesBackend{environmentKubernetesBackend: runtime.Backend, execution: runtime.Execution, cfg: cfg, store: store}
		backend.stagePlanSHA256 = r.RenewalCandidate.SHA256()
		if err := r.RenewalCandidate.AdmitResumable(ctx, backend, store.Root); err != nil {
			return service, err
		}
		if err := beginSessionLineage(store, runtime.Execution, admission, r.Review, now); err != nil {
			return service, err
		}
	}
	if !managedSessionMayRenew(state, service, subjects, time.Now().UTC()) || !unchanged {
		if !proof.PartialInventory {
			return service, environmentCreateBootstrapBlocker("qualified original expired seed changed before renewal")
		}
		if r.SecondSession {
			originalPlan, planErr := qualifiedSecondSessionPlan(store, runtime.Execution, r.qualifiedPlan, r.RenewalCandidate)
			if planErr != nil || r.OpenBaoRoot == nil || observeSecondSessionSuccessor(ctx, cfg, store, runtime.Execution, runtime.Backend, originalPlan, service, *r.OpenBaoRoot, r.Review, time.Now().UTC()) != nil {
				return service, environmentCreateBootstrapBlocker("second session requires the original qualified successor outcome")
			}
		} else {
			qualified, review, exists, loadErr := loadPartialSessionAdmission(store, runtime.Execution)
			if loadErr != nil || !exists || review != r.Review || observePartialSessionSuccessor(ctx, cfg, store, runtime.Execution, runtime.Backend, qualified, review, r) != nil {
				return service, environmentCreateBootstrapBlocker("partial session renewal requires the original qualified successor outcome")
			}
		}
		minted = true
	}
	clients, err := readManagedSessionInventory(ctx, cfg, runtime, service, subjects, state, proof.PartialInventory)
	if err != nil || !minted && clients != proof.ClientsSHA256 {
		return service, environmentCreateBootstrapBlocker("original managed clients changed before seed renewal")
	}
	if minted {
		rows, readErr := readManagedSessionClientRows(ctx, cfg, runtime)
		if readErr != nil || !partialSessionClientsPreserved(rows, service, subjects, state, proof, time.Now().UTC()) {
			return service, environmentCreateBootstrapBlocker("original managed clients changed before seed renewal")
		}
	}
	original, journal, err := acceptedCreateStage(store, runtime.Execution, proof.SourceStage)
	resource, ok := managedSessionSourceJob(original, journal)
	if err != nil || !ok || original.SHA256() != proof.SourcePlanSHA256 || deploymentplan.Digest(journal) != proof.SourceJournalSHA256 {
		return service, environmentCreateBootstrapBlocker("qualified original seed source Job changed")
	}
	actual, err := runtime.Backend.Read(ctx, resource.ID)
	if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != journal.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, actual) {
		return service, environmentCreateBootstrapBlocker("qualified original seed source Job owner changed")
	}
	if err := verifyManagedSeedOriginalCustody(ctx, runtime, r, proof); err != nil {
		return service, err
	}
	candidate := r.Candidate
	r.Candidate = r.RenewalCandidate
	service, err = createEnvironmentServiceSession(ctx, cfg, p, store, runtime, service, subjects, apply)
	r.Candidate = candidate
	if err != nil {
		return service, err
	}
	if err := verifyManagedSeedOriginalCustody(ctx, runtime, r, proof); err != nil {
		return service, err
	}
	clients, err = readManagedSessionInventory(ctx, cfg, runtime, service, subjects, state, proof.PartialInventory)
	if err != nil || !proof.PartialInventory && clients != proof.ClientsSHA256 {
		return service, environmentCreateBootstrapBlocker("managed seed renewal changed original workload identity inventory")
	}
	if proof.PartialInventory {
		rows, readErr := readManagedSessionClientRows(ctx, cfg, runtime)
		if readErr != nil || !partialSessionClientsPreserved(rows, service, subjects, state, proof, time.Now().UTC()) {
			return service, environmentCreateBootstrapBlocker("managed seed renewal changed original workload identity inventory")
		}
	}
	old, _, err := readManagedSessionState(ctx, cfg, runtime, proof.OriginalSession)
	if err != nil || old.Status != "expired" || old.Session != state.Session || old.Issuer != state.Issuer || old.Root != state.Root || old.Fingerprint != state.Fingerprint || !slices.Equal(old.Subjects, state.Subjects) || !slices.Equal(old.Installed, state.Installed) {
		return service, environmentCreateBootstrapBlocker("native original seed expiry lineage was not preserved")
	}
	if err = recordManagedSessionClients(store, filepath.Join("deployment/create", runtime.ExecutionSHA256, "bootstrap/service/session-renewal", r.Review, "managed-clients-preserved.json"), map[string]string{"original_session_id": proof.OriginalSession, "renewed_session_id": service.BootstrapSessionID, "clients_sha256": clients, "original_key_sha256": r.OriginalKeySHA, "original_csr_sha256": r.OriginalCSRSHA}); err != nil {
		return service, err
	}
	return service, nil
}

func recordManagedSessionClients(store secretStore, relative string, proof map[string]string) error {
	_, err := writeProductArtifact(store, relative, proof)
	return err
}
