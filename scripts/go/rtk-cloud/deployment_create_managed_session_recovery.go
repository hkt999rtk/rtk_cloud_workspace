package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
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

func readManagedSessionState(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, session string) (managedSessionState, []byte, error) {
	var state managedSessionState
	if !environmentBootstrapOwnerUUID.MatchString(session) {
		return state, nil, createProviderRecoveryBlock("managed seed inspection requires the original canonical session")
	}
	query := "SELECT json_build_object('session_id',session_id,'issuer_id',issuer_id,'root_sha256',root_sha256,'bootstrap_fingerprint',bootstrap_fingerprint,'status',status,'expires_at',expires_at,'subjects',subjects,'installed_subjects',installed_subjects,'other_active',(SELECT count(*) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND status='active' AND expires_at>now() AND session_id<>'" + session + "')) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND session_id='" + session + "'"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	if err != nil || decodeEnvironmentJSON(raw, &state) != nil {
		return state, nil, createProviderRecoveryBlock("native managed seed lineage cannot be inspected")
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
	expected := map[string]int{}
	for _, subject := range subjects {
		if expected[subject] != 0 || subject == "service:deployment-bootstrap" {
			return false
		}
		expected[subject] = 1
	}
	if expected["service:video-cloud-api"] != 1 {
		return false
	}
	expected["service:video-cloud-api"] += 2
	seen := map[string]bool{}
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
		if err != nil || leaf.Subject.CommonName != row.Subject || !leaf.NotAfter.After(now) || leaf.NotBefore.After(now) || leaf.CheckSignatureFrom(intermediate) != nil || environmentPreflightBytesSHA256(leaf.Raw) != row.Fingerprint || block == nil || block.Type != "CERTIFICATE REQUEST" || strings.TrimSpace(string(rest)) != "" {
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
	}
	for _, count := range expected {
		if count != 0 {
			return false
		}
	}
	return true
}

func readManagedSessionClients(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, subjects []string) (string, error) {
	query := "SELECT COALESCE(json_agg(json_build_object('subject',subject,'caller',caller,'request_id',request_id,'issuer_id',issuer_id,'fingerprint',fingerprint,'status',status,'revoked_at',revoked_at,'csr_pem',csr_pem,'certificate_pem',certificate_pem) ORDER BY subject,caller,request_id),'[]'::json) FROM pki_service_client_issuances WHERE environment='" + cfg.Environment + "' AND subject<>'service:deployment-bootstrap'"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var clients []managedSessionClient
	if err != nil || decodeEnvironmentJSON(raw, &clients) != nil || !validateManagedSessionClients(clients, service, subjects, time.Now().UTC()) {
		return "", createProviderRecoveryBlock("managed seed renewal requires every unchanged original native workload client and no foreign or partial issuance")
	}
	return deploymentplan.Digest(clients), nil
}

func managedSessionRenewalDefinition(original deploymentplan.Object, parent, binding, session string, image environmentReleaseImage) (deploymentplan.Definition, error) {
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
	return definition, nil
}

func prepareManagedSessionRecovery(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery) error {
	if r.ManagedTransportRecovery == nil || !r.PostServiceRenewal || !r.Renewal || !isManagedTransportCheckpoint(e.Journal) || !environmentPreflightDigest.MatchString(r.OriginalKeySHA) || !environmentPreflightDigest.MatchString(r.OriginalCSRSHA) {
		return createProviderRecoveryBlock("managed seed renewal requires the authenticated all-CA-ACTIVE checkpoint and original key/CSR custody")
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: b}
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil {
		return err
	}
	state, raw, err := readManagedSessionState(ctx, cfg, runtime, r.Ceremony.BootstrapSessionID)
	if err != nil || !managedSessionMayRenew(state, r.Ceremony, subjects, time.Now().UTC()) {
		return createProviderRecoveryBlock("managed seed renewal requires its genuinely expired original allowlist and no competing active session")
	}
	clients, err := readManagedSessionClients(ctx, cfg, runtime, r.Ceremony, subjects)
	if err != nil {
		return err
	}
	original, journal, err := acceptedCreateStage(store, e, r.SourceRenewalStage)
	if err != nil || len(original.Resources) != 1 || original.Resources[0].Desired["kind"] != "Job" {
		return createProviderRecoveryBlock("managed seed renewal requires its exact accepted original-key renewal Job")
	}
	resource := original.Resources[0]
	actual, err := b.Read(ctx, resource.ID)
	if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != journal.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, actual) {
		return createProviderRecoveryBlock("original accepted seed renewal Job owner changed")
	}
	image, selection, err := loadServiceTrustRepairImage(ctx, cfg, store, e.Journal.PlanSHA256)
	if err != nil {
		return err
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
	definition, err := managedSessionRenewalDefinition(resource.Desired, e.Journal.PlanSHA256, binding, r.RenewedIntent.SessionID, image)
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
	r.ManagedTransportRecovery.SessionRenewal = &createManagedSessionRecovery{OriginalSession: state.Session, OriginalCertificatePath: oldPath, OriginalCertificateSHA256: environmentPreflightBytesSHA256(material), SessionSHA256: environmentPreflightBytesSHA256(raw), ClientsSHA256: clients, SourceStage: r.SourceRenewalStage, SourcePlanSHA256: original.SHA256(), SourceJournalSHA256: deploymentplan.Digest(journal), BindingSHA256: binding, CandidateSHA256: r.RenewalCandidate.SHA256()}
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
		raw, err := runtime.Backend.command(ctx, nil, "--namespace", r.Ceremony.BootstrapCredentialNamespace, "exec", "deployment/"+r.Ceremony.CustodyReader, "--container", "custody", "--", "cat", path)
		if err != nil || environmentPreflightBytesSHA256(raw) != want {
			return environmentCreateBootstrapBlocker("managed seed original key/CSR/certificate custody changed")
		}
	}
	return runtime.Execution.Check(ctx)
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
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil {
		return service, err
	}
	state, raw, err := readManagedSessionState(ctx, cfg, runtime, proof.OriginalSession)
	if err != nil || !managedSessionMayRenew(state, service, subjects, time.Now().UTC()) || environmentPreflightBytesSHA256(raw) != proof.SessionSHA256 || r.RenewalCandidate.SHA256() != proof.CandidateSHA256 {
		return service, environmentCreateBootstrapBlocker("qualified original expired seed changed before renewal")
	}
	clients, err := readManagedSessionClients(ctx, cfg, runtime, service, subjects)
	if err != nil || clients != proof.ClientsSHA256 {
		return service, environmentCreateBootstrapBlocker("original managed clients changed before seed renewal")
	}
	original, journal, err := acceptedCreateStage(store, runtime.Execution, proof.SourceStage)
	if err != nil || original.SHA256() != proof.SourcePlanSHA256 || deploymentplan.Digest(journal) != proof.SourceJournalSHA256 || len(original.Resources) != 1 {
		return service, environmentCreateBootstrapBlocker("qualified original seed source Job changed")
	}
	resource := original.Resources[0]
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
	clients, err = readManagedSessionClients(ctx, cfg, runtime, service, subjects)
	if err != nil || clients != proof.ClientsSHA256 {
		return service, environmentCreateBootstrapBlocker("managed seed renewal changed original workload identity inventory")
	}
	old, _, err := readManagedSessionState(ctx, cfg, runtime, proof.OriginalSession)
	if err != nil || old.Status != "expired" || old.Session != state.Session || old.Issuer != state.Issuer || old.Root != state.Root || old.Fingerprint != state.Fingerprint || !slices.Equal(old.Subjects, state.Subjects) || !slices.Equal(old.Installed, state.Installed) {
		return service, environmentCreateBootstrapBlocker("native original seed expiry lineage was not preserved")
	}
	if _, err = writeEnvironmentCreateBootstrapJSON(store, filepath.Join("deployment/create", runtime.ExecutionSHA256, "bootstrap/service/session-renewal", r.Review, "managed-clients-preserved.json"), map[string]string{"original_session_id": proof.OriginalSession, "renewed_session_id": service.BootstrapSessionID, "clients_sha256": clients, "original_key_sha256": r.OriginalKeySHA, "original_csr_sha256": r.OriginalCSRSHA}); err != nil {
		return service, err
	}
	return service, nil
}
