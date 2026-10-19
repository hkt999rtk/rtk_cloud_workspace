package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const managedLateExpiredRenewalSchema = "rtk-cloud.create-managed-late-expired-unused-renewal/v1"
const managedLateExpiredRenewalSourceFilename = "managed-late-expired-unused-source.json"
const managedLateExpiredRenewalPlanFilename = "managed-late-expired-unused-seed-plan.json"
const managedLateExpiredRenewalMaterial = "accepted-service-core-late-expired-unused-"

// The prior accepted Late and its minted authority remain immutable. This
// proof authorizes one distinct temporary leaf/session on their original key.
// It does not authorize issuer creation or workload enrollment.
type createManagedLateExpiredRenewal struct {
	Schema, Review, ParentSHA, SourceSHA, SourceOwnerSHA string
	PriorReview, PriorDescriptorSHA, PriorPostSHA        string
	PriorSession                                         managedSessionState
	PriorCertificatePath, PriorCertificateSHA            string
	OriginalKeySHA, OriginalCSRSHA                       string
	PriorLate                                            *createManagedLateSessionRecovery
	PriorPost                                            *createManagedLatePostMint
	PriorOwners                                          map[string]deploymentplan.Resource
	Candidate                                            *deploymentplan.Plan
	NativeEvidence                                       map[string]string
}

func managedLateExpiredRenewalBlock(stage string) error {
	return environmentCreateBootstrapBlocker("expired unused late renewal requires exact preserved authority: stage=" + stage)
}

func managedLateExpiredRenewalDigest(f *createManagedLateExpiredRenewal) string {
	if f == nil {
		return ""
	}
	copy := *f
	copy.Review = ""
	return deploymentplan.Digest(copy)
}

func managedLateExpiredUnusedSession(current, saved managedSessionState, service environmentCreateServiceCeremony, subjects []string, now time.Time) bool {
	return len(subjects) == 9 && len(current.Installed) == 0 && len(saved.Installed) == 0 && managedSessionMayRenew(current, service, subjects, now) && current.Session == saved.Session && current.Issuer == saved.Issuer && current.Root == saved.Root && current.Fingerprint == saved.Fingerprint && current.Expires.Equal(saved.Expires) && slices.Equal(current.Subjects, saved.Subjects) && (saved.Status == "active" || saved.Status == "expired")
}

// Structural routing grants no authority. The source constructor authenticates
// the completed App tail and all original admission/accepted-child evidence.
func managedLateExpiredRenewalReview(j environmentCreateJournal) string {
	if len(j.Entries) == 374 && managedLatePostMintReview(j) != "" {
		return managedLatePostMintReview(j)
	}
	for i := 374; i < len(j.Entries); i++ {
		entry := j.Entries[i]
		const prefix = "openbao-recovery-admission-"
		if len(entry.Key) == len(prefix)+64 && entry.Key[:len(prefix)] == prefix {
			return entry.Key[len(prefix):]
		}
		return ""
	}
	return ""
}

func managedLateExpiredRenewalSelected(store secretStore, e *environmentCreateExecution) (bool, error) {
	if e == nil {
		return false, nil
	}
	if len(e.Journal.Entries) > 374 {
		review := managedLateExpiredRenewalReview(e.Journal)
		if review == "" {
			return false, nil
		}
		var a createFoundationRecoveryArtifact
		if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, "qualification.json"), &a) != nil {
			return false, nil
		}
		return a.Plan.Schema == managedLateExpiredRenewalSchema, nil
	}
	review := managedLatePostMintReview(e.Journal)
	if len(e.Journal.Entries) != 374 || review == "" {
		return false, nil
	}
	var late createManagedLateSessionRecovery
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, "managed-late-session-source.json"), &late) != nil || late.OpenBaoTLSRepair == nil {
		return true, managedLateExpiredRenewalBlock("original-descriptor")
	}
	_, post, err := loadManagedLatePostMintSource(late.OpenBaoTLSRepair.Plan.Configuration, store, e)
	if err != nil {
		return true, err
	}
	return !post.SessionExpires.After(time.Now().UTC()), nil
}

func loadManagedLateExpiredRenewalSource(cfg deploymentConfig, store secretStore, e *environmentCreateExecution) (*createServiceSessionRecovery, *createManagedLateExpiredRenewal, error) {
	if e != nil && len(e.Journal.Entries) == 375 {
		r, _, err := loadManagedLateExpiredAdmittedOnlySource(context.Background(), cfg, store, e)
		if err != nil {
			return nil, nil, err
		}
		return r, r.ManagedLateExpiredRenewal, nil
	}
	var retained struct {
		Recovery *createServiceSessionRecovery
		Renewal  *createManagedLateExpiredRenewal
	}
	if hit, err := readManagedLateSourceReuse(store, e, cfg, "expired-unused-source", &retained); err != nil {
		return nil, nil, err
	} else if hit {
		if retained.Recovery == nil || retained.Recovery.ManagedLateSession == nil || retained.Renewal == nil {
			return nil, nil, managedLateSourceReuseBlock()
		}
		retained.Recovery.ManagedLateExpiredRenewal = retained.Renewal
		retained.Recovery.ManagedLateSession.ExpiredRenewal = retained.Renewal
		return retained.Recovery, retained.Renewal, nil
	}
	r, renewal, err := deriveManagedLateExpiredRenewalSource(cfg, store, e)
	if err != nil {
		return nil, nil, err
	}
	retained.Recovery, retained.Renewal = r, renewal
	if err := rememberManagedLateSourceReuse(store, e, cfg, "expired-unused-source", retained); err != nil {
		return nil, nil, err
	}
	return r, renewal, nil
}

func deriveManagedLateExpiredRenewalSource(cfg deploymentConfig, store secretStore, e *environmentCreateExecution) (*createServiceSessionRecovery, *createManagedLateExpiredRenewal, error) {
	bad := func(stage string) (*createServiceSessionRecovery, *createManagedLateExpiredRenewal, error) {
		return nil, nil, managedLateExpiredRenewalBlock(stage)
	}
	if e == nil || e.Store.Root != store.Root || len(e.Journal.Entries) != 374 || managedLatePostMintReview(e.Journal) == "" || !e.Journal.CompletedAt.IsZero() {
		return bad("literal-completed-original-source")
	}
	if err := managedLateInstallationClockAbsent(store, e.Journal.PlanSHA256); err != nil {
		return nil, nil, err
	}
	r, post, err := loadManagedLatePostMintSource(cfg, store, e)
	if err != nil {
		return nil, nil, err
	}
	old := r.ManagedLateSession
	if old == nil || old.ExpiredRenewal != nil || post.OriginalReview != old.Review || post.Ceremony.BootstrapSessionID != old.NewSessionID {
		return bad("accepted-prior-session")
	}
	var observed createManagedLateSeedObservation
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", old.Review, "managed-late-seed-observed.json"), &observed) != nil || observed.Session.Session != old.NewSessionID || observed.Session.Fingerprint != post.Fingerprint || !observed.Session.Expires.Equal(post.SessionExpires) || len(observed.Session.Subjects) != 9 || len(observed.Session.Installed) != 0 || observed.KeySHA != old.OriginalKeySHA || observed.CSRSHA != old.OriginalCSRSHA {
		return bad("genuine-prior-allocation")
	}
	children, _, _, err := managedLateCurrentChildren(store, e, old)
	if err != nil {
		return nil, nil, err
	}
	owners, _, err := managedLateCurrentOwners(r, old, children)
	if err != nil {
		return nil, nil, err
	}
	raw, err := json.Marshal(old)
	if err != nil {
		return nil, nil, err
	}
	var late createManagedLateSessionRecovery
	if json.Unmarshal(raw, &late) != nil {
		return bad("original-descriptor-copy")
	}
	f := &createManagedLateExpiredRenewal{Schema: managedLateExpiredRenewalSchema, ParentSHA: e.Journal.PlanSHA256, SourceSHA: deploymentplan.Digest(e.Journal), SourceOwnerSHA: deploymentplan.Digest(e.owner), PriorReview: old.Review, PriorDescriptorSHA: managedLateSessionSourceDigest(old), PriorPostSHA: managedLatePostMintDigest(post), PriorSession: observed.Session, PriorCertificatePath: old.NewCertificatePath, PriorCertificateSHA: environmentPreflightBytesSHA256([]byte(observed.CertificatePEM)), OriginalKeySHA: old.OriginalKeySHA, OriginalCSRSHA: old.OriginalCSRSHA, PriorLate: old, PriorPost: post, PriorOwners: owners}
	late.Review = ""
	late.SourceSHA = f.SourceSHA
	late.SourceOwnerSHA = f.SourceOwnerSHA
	late.SourceCeremony = post.Ceremony
	late.SourceRenewedIntent = r.RenewedIntent
	late.SourceRenewedIntent.SessionID = old.NewSessionID
	late.OriginalSession = old.NewSessionID
	late.OriginalSessionState = observed.Session
	late.Renewal = &createManagedSessionRecovery{OriginalSession: old.NewSessionID, OriginalCertificatePath: f.PriorCertificatePath, OriginalCertificateSHA256: f.PriorCertificateSHA, ClientsSHA256: old.OriginalClientsSHA}
	late.ExpiredRenewal = f
	late.SeedCandidate = nil
	late.OwnerCandidate = nil
	late.CustodyCandidate = nil
	late.LogProducer = nil
	late.LogStopCandidate = nil
	late.LogHelperCandidate = nil
	late.LogRetireCandidate = nil
	late.LogRestoreCandidate = nil
	binding := managedLateExpiredRenewalBinding(f, &late)
	late.NewSessionID = sessionRenewalUUID(f.ParentSHA, binding, late.OriginalSession)
	late.NewCertificatePath = "/run/pki-bootstrap/renew-" + binding[:12] + "/bootstrap.crt"
	late.SeedAuthorization = nil
	late.CleanupAuthorization = nil
	r.ManagedLatePostMint = nil
	r.ManagedLateSeedForward = nil
	r.ManagedLateSession = &late
	r.ManagedLateExpiredRenewal = f
	r.Ceremony = post.Ceremony
	r.RenewedCertPath = f.PriorCertificatePath
	r.RenewedIntent = late.SourceRenewedIntent
	r.Review = ""
	return r, f, nil
}

func loadManagedLateEffectiveSource(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, p *createFoundationRecoveryPlan, late *createManagedLateSessionRecovery) (*createServiceSessionRecovery, *createManagedLateSessionRecovery, error) {
	if late != nil && late.ExpiredRenewal != nil {
		r, f, err := loadManagedLateExpiredRenewalSource(cfg, store, e)
		if err != nil {
			return nil, nil, err
		}
		if managedLateExpiredRenewalSourcePins(f) != managedLateExpiredRenewalSourcePins(late.ExpiredRenewal) {
			return nil, nil, managedLateExpiredRenewalBlock("preserved-source")
		}
		original, _, err := loadAdmittedCreateRecovery(store, e, f.PriorReview)
		if err != nil {
			return nil, nil, err
		}
		p.Plan = original.Plan.Plan
		for key, value := range original.Plan.Materials {
			p.Materials[key] = value
		}
		return r, r.ManagedLateSession, nil
	}
	return loadManagedLateSessionSource(cfg, store, e, p, late.Mode)
}

func managedLateExpiredRenewalSourcePins(f *createManagedLateExpiredRenewal) string {
	if f == nil {
		return ""
	}
	copy := *f
	copy.Review = ""
	copy.Candidate = nil
	copy.NativeEvidence = nil
	return deploymentplan.Digest(copy)
}

func prepareManagedLateExpiredRenewal(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan) (*createServiceSessionRecovery, error) {
	if e != nil && managedLateRemainingSelected(e.Journal) {
		return prepareManagedLateRemaining(ctx, cfg, store, e, b, p)
	}
	if e != nil && len(e.Journal.Entries) == 375 {
		return prepareManagedLateExpiredAdmittedOnly(ctx, cfg, store, e, b, p)
	}
	r, f, err := loadManagedLateExpiredRenewalSource(cfg, store, e)
	if err != nil {
		return nil, err
	}
	original, _, err := loadAdmittedCreateRecovery(store, e, f.PriorReview)
	if err != nil {
		return nil, err
	}
	p.Plan = original.Plan.Plan
	p.Schema = managedLateExpiredRenewalSchema
	for key, value := range original.Plan.Materials {
		p.Materials[key] = value
	}
	late := r.ManagedLateSession
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil {
		return nil, err
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: r}
	state, raw, err := readManagedSessionState(ctx, cfg, runtime, late.OriginalSession)
	if err != nil || !managedLateExpiredUnusedSession(state, f.PriorSession, late.SourceCeremony, subjects, time.Now().UTC()) {
		return nil, managedLateExpiredRenewalBlock("genuinely-expired-zero-installed")
	}
	late.Renewal.SessionSHA256 = environmentPreflightBytesSHA256(raw)
	if err := verifyManagedSeedOriginalCustody(ctx, runtime, r, late.Renewal); err != nil {
		return nil, err
	}
	seed, entry, journal, err := readManagedLateEffectiveSeed(store, e, f.PriorLate)
	if err != nil || len(seed.Resources) != 1 || !createOpenBaoACKCompletedChild(seed, journal) {
		return nil, managedLateExpiredRenewalBlock("prior-accepted-corrected-job")
	}
	r.SourceRenewalStage = entry.Key
	late.Renewal.SourceStage = entry.Key
	late.Renewal.SourcePlanSHA256 = seed.SHA256()
	late.Renewal.SourceJournalSHA256 = deploymentplan.Digest(journal)
	image, selection, err := loadServiceTrustRepairImage(ctx, cfg, store, e.Journal.PlanSHA256)
	if err != nil {
		return nil, err
	}
	binding := managedLateExpiredRenewalBinding(f, late)
	late.NewSessionID = sessionRenewalUUID(f.ParentSHA, binding, late.OriginalSession)
	late.NewCertificatePath = "/run/pki-bootstrap/renew-" + binding[:12] + "/bootstrap.crt"
	object := deploymentplan.Clone(seed.Resources[0].Desired)
	container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(object)["containers"])[0])
	container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/bootstrap.crt"}))
	definition, err := managedSessionRenewalDefinition(object, f.ParentSHA, binding, late.NewSessionID, image)
	if err != nil {
		return nil, err
	}
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return nil, err
	}
	late.SeedCandidate, err = managedLateCompile(p.Plan, "managed-late-original-key-session", []deploymentplan.Definition{definition}, live)
	if err != nil || len(late.SeedCandidate.Resources) != 1 || late.SeedCandidate.Resources[0].Action != "create" {
		return nil, managedLateExpiredRenewalBlock("distinct-unissued-job")
	}
	late.Renewal.BindingSHA256 = binding
	late.Renewal.CandidateSHA256 = late.SeedCandidate.SHA256()
	f.Candidate = late.SeedCandidate
	r.GrantImage = image
	r.RenewalBindingSHA = binding
	r.RenewalCandidate = late.SeedCandidate
	r.RenewedIntent.SessionID = late.NewSessionID
	r.RenewedCertPath = late.NewCertificatePath
	p.Materials["accepted-service-core-late-session-image-selection"] = selection
	p.Materials["accepted-service-core-late-session-image"] = deploymentplan.Digest(image)
	if err := prepareManagedLateSessionOwners(ctx, cfg, store, e, b, p, r, late); err != nil {
		return nil, err
	}
	if err := prepareManagedLateSessionAuthorizations(ctx, cfg, store, e, b, p, r, late); err != nil {
		return nil, err
	}
	if err := prepareManagedLateLogCandidates(ctx, cfg, store, e, b, p, r, late); err != nil {
		return nil, err
	}
	if err := verifyManagedLateExpiredRenewalCurrent(ctx, cfg, store, e, b, r, p); err != nil {
		return nil, err
	}
	r.Candidate = late.OwnerCandidate
	return r, nil
}

// Historical admission materials seed the original authentication context.
// The separately qualified successor's finite preview/recipe pins must then be
// rederived from its typed descriptor, never inherited from that old admission.
func managedLateExpiredRenewalMaterialPins(r *createServiceSessionRecovery, image environmentReleaseImage, selection string) (map[string]string, error) {
	bad := func() (map[string]string, error) {
		return nil, managedLateExpiredRenewalBlock("current-qualified-materials")
	}
	if r == nil || r.ManagedLateSession == nil || r.ManagedLateExpiredRenewal == nil {
		return bad()
	}
	late, f := r.ManagedLateSession, r.ManagedLateExpiredRenewal
	qualified := managedLateQualifiedMaterialPins(late)
	if qualified == nil || late.ExpiredRenewal == nil || managedLateExpiredRenewalDigest(late.ExpiredRenewal) != managedLateExpiredRenewalDigest(f) || f.Schema != managedLateExpiredRenewalSchema || f.ParentSHA != late.ParentSHA || f.SourceSHA != late.SourceSHA || f.SourceOwnerSHA != late.SourceOwnerSHA || f.Candidate == nil || f.Candidate.SHA256() != late.SeedCandidate.SHA256() || late.Renewal.CandidateSHA256 != late.SeedCandidate.SHA256() || !environmentPreflightDigest.MatchString(f.PriorDescriptorSHA) || !immutableEnvironmentImage.MatchString(image.Reference) || deploymentplan.Digest(image) != deploymentplan.Digest(r.GrantImage) || !environmentPreflightDigest.MatchString(selection) || len(f.NativeEvidence) != 7 {
		return bad()
	}
	for _, key := range []string{"expired-session", "source", "prior-certificate", "key", "csr", "native-no-mint", "prospective-certificate"} {
		if !environmentPreflightDigest.MatchString(f.NativeEvidence[key]) {
			return bad()
		}
	}
	if f.NativeEvidence["source"] != f.SourceSHA || f.NativeEvidence["prior-certificate"] != f.PriorCertificateSHA || f.NativeEvidence["key"] != f.OriginalKeySHA || f.NativeEvidence["csr"] != f.OriginalCSRSHA || f.NativeEvidence["prospective-certificate"] != deploymentplan.Digest(late.NewCertificatePath) {
		return bad()
	}
	pins := map[string]string{}
	for name, value := range qualified {
		if !environmentPreflightDigest.MatchString(value) {
			return bad()
		}
		pins["accepted-service-core-late-session-"+name] = value
	}
	pins["accepted-service-core-late-session-image"] = deploymentplan.Digest(image)
	pins["accepted-service-core-late-session-image-selection"] = selection
	pins[managedLateExpiredRenewalMaterial+"descriptor"] = managedLateExpiredRenewalDigest(f)
	pins[managedLateExpiredRenewalMaterial+"source"] = f.SourceSHA
	pins[managedLateExpiredRenewalMaterial+"prior-descriptor"] = f.PriorDescriptorSHA
	pins[managedLateExpiredRenewalMaterial+"native"] = deploymentplan.Digest(f.NativeEvidence)
	return pins, nil
}

func verifyManagedLateExpiredRenewalCurrent(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan) error {
	if r != nil && r.ManagedLateRemaining != nil {
		return verifyManagedLateRemainingCurrent(ctx, cfg, store, e, b, r, p)
	}
	if r == nil || r.ManagedLateSession == nil || r.ManagedLateExpiredRenewal == nil {
		return managedLateExpiredRenewalBlock("typed-profile")
	}
	if err := beginManagedLateAdmittedSourcePhase(ctx, store, e, r.ManagedLateSession); err != nil {
		return err
	}
	late, f := r.ManagedLateSession, r.ManagedLateExpiredRenewal
	original, _, loadErr := loadAdmittedCreateRecovery(store, e, f.PriorReview)
	if loadErr != nil {
		return loadErr
	}
	if p.Materials == nil {
		p.Materials = map[string]string{}
	}
	for key, value := range original.Plan.Materials {
		if _, exists := p.Materials[key]; !exists {
			p.Materials[key] = value
		}
	}
	p.Plan = original.Plan.Plan
	if f.SourceSHA != deploymentplan.Digest(e.Journal) {
		if _, _, err := managedLateSessionRuntimeProgress(store, e, late); err != nil {
			return err
		}
	}
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil {
		return err
	}
	state, _, err := readManagedSessionState(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: r}, f.PriorSession.Session)
	if err != nil || !managedLateExpiredUnusedSession(state, f.PriorSession, late.SourceCeremony, subjects, time.Now().UTC()) {
		return managedLateExpiredRenewalBlock("current-unused-authority")
	}
	if late.SeedCandidate == nil || len(late.SeedCandidate.Resources) != 1 {
		return managedLateExpiredRenewalBlock("exact-new-seed-preview")
	}
	producer := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(late.SeedCandidate.Resources[0].Desired)["containers"])[0])
	query, queryErr := managedLateSeedForwardNoMintQuery(cfg.Environment, late.NewSessionID, mqttFoundationEnv(producer, "PKI_BOOTSTRAP_DEPLOYMENT_ID"))
	if queryErr != nil {
		return queryErr
	}
	native, nativeErr := readEnvironmentCreateRegistryJSON(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: r}, query)
	if nativeErr != nil || !managedLateSeedForwardNoMintCountsValid(native) {
		return managedLateExpiredRenewalBlock("prospective-session-deployment-request-already-present")
	}
	if late.NewCertificatePath == f.PriorCertificatePath || late.NewSessionID == f.PriorSession.Session || late.NewSessionID == late.OriginalEnrollmentSession || !managedLateExpiredRenewalCertificatePath(late.NewCertificatePath) {
		return managedLateExpiredRenewalBlock("distinct-prospective-certificate")
	}
	if _, absenceErr := b.command(ctx, nil, "--namespace", late.SourceCeremony.BootstrapCredentialNamespace, "exec", "deployment/"+late.SourceCeremony.CustodyReader, "--container", "custody", "--", "/bin/sh", "-ec", "test ! -e "+late.NewCertificatePath+" && test ! -L "+late.NewCertificatePath); absenceErr != nil {
		return managedLateExpiredRenewalBlock("prospective-certificate-not-absent")
	}
	if err := verifyManagedLateSessionCurrentNative(ctx, cfg, store, e, b, r, p, late); err != nil {
		return err
	}
	if err := verifyServiceSessionAuthentication(ctx, cfg, store, e, b, r); err != nil {
		return err
	}
	if _, scopedRepair := ctx.Value(managedLateDeviceRootCRLObservationKey{}).(managedLateDeviceRootCRLObservation); !scopedRepair {
		deviceProof, proofErr := verifyManagedLateDeviceRootCRLFreshness(ctx, cfg, store, e, b, r)
		if proofErr != nil {
			return proofErr
		}
		for name, value := range deviceProof {
			p.Materials[managedLateExpiredRenewalMaterial+name] = value
		}
	}
	stableState := state
	stableState.Status = "expired"
	evidence := map[string]string{"expired-session": deploymentplan.Digest(stableState), "source": f.SourceSHA, "prior-certificate": f.PriorCertificateSHA, "key": f.OriginalKeySHA, "csr": f.OriginalCSRSHA, "native-no-mint": environmentPreflightBytesSHA256(native), "prospective-certificate": deploymentplan.Digest(late.NewCertificatePath)}
	if len(f.NativeEvidence) == 0 {
		f.NativeEvidence = evidence
	} else if deploymentplan.Digest(f.NativeEvidence) != deploymentplan.Digest(evidence) {
		return managedLateExpiredRenewalBlock("qualified-current-observation")
	}
	image, selection, err := loadServiceTrustRepairImage(ctx, cfg, store, e.Journal.PlanSHA256)
	if err != nil {
		return err
	}
	pins, err := managedLateExpiredRenewalMaterialPins(r, image, selection)
	if err != nil {
		return err
	}
	for key, value := range pins {
		p.Materials[key] = value
	}
	return nil
}

func loadManagedLateExpiredRenewalQualified(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, p *createFoundationRecoveryPlan, review string) (*createServiceSessionRecovery, error) {
	return loadManagedLateExpiredRenewalQualifiedContent(ctx, cfg, store, e, p, review, time.Now().UTC())
}

func loadManagedLateExpiredRenewalQualifiedContent(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, p *createFoundationRecoveryPlan, review string, authorityAt time.Time) (*createServiceSessionRecovery, error) {
	var a createFoundationRecoveryArtifact
	base := filepath.Join("deployment/create/openbao-recovery", review)
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, "qualification.json"), &a) != nil {
		return nil, managedLateExpiredRenewalBlock("fresh-current-checker-six-go")
	}
	if a.Plan.Materials[managedLateRemainingMaterial+"descriptor"] != "" {
		return loadManagedLateRemainingQualified(ctx, cfg, store, e, p, review)
	}
	if a.Plan.Materials[managedLateExpiredAdmittedOnlyMaterial] != "" {
		return loadManagedLateExpiredAdmittedOnlyQualified(ctx, cfg, store, e, p, review, a)
	}
	if !managedLateQualifiedArtifactValid(a, e, *p, review, authorityAt, managedLateExpiredRenewalSchema) {
		return nil, managedLateExpiredRenewalBlock("fresh-current-checker-six-go")
	}
	var late createManagedLateSessionRecovery
	var f createManagedLateExpiredRenewal
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, "managed-late-session-source.json"), &late) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(base, managedLateExpiredRenewalSourceFilename), &f) != nil || late.Review != review || f.Review != review || late.ExpiredRenewal == nil || managedLateExpiredRenewalDigest(late.ExpiredRenewal) != managedLateExpiredRenewalDigest(&f) || a.Plan.Materials[managedLateExpiredRenewalMaterial+"descriptor"] != managedLateExpiredRenewalDigest(&f) {
		return nil, managedLateExpiredRenewalBlock("archived-descriptor")
	}
	derived := *p
	derived.Materials = map[string]string{}
	r, loaded, err := loadManagedLateEffectiveSource(cfg, store, e, &derived, &late)
	if err != nil {
		return nil, err
	}
	if !managedLateQualifiedAncestryMatches(loaded, &late) || deploymentplan.Digest(derived.Plan) != deploymentplan.Digest(a.Plan.Plan) {
		return nil, managedLateExpiredRenewalBlock("immutable-ancestry")
	}
	if err := validateManagedLateQualifiedPreviews(store, review, &late, a.Plan.Materials); err != nil {
		return nil, err
	}
	if err := validateManagedLateQualifiedOwners(store, e, r, &late); err != nil {
		return nil, err
	}
	r.SourceRenewalStage = late.Renewal.SourceStage
	if err := validateManagedLateQualifiedRecipes(cfg, store, e, r, a.Plan, &late, time.Now().UTC()); err != nil {
		return nil, err
	}
	r.ManagedLateSession = &late
	r.ManagedLateExpiredRenewal = late.ExpiredRenewal
	r.Review = review
	r.Ceremony = late.SourceCeremony
	r.RenewedIntent = late.SourceRenewedIntent
	r.RenewedIntent.SessionID = late.NewSessionID
	r.RenewedCertPath = late.NewCertificatePath
	r.RenewalCandidate = late.SeedCandidate
	r.RenewalBindingSHA = late.Renewal.BindingSHA256
	r.Candidate = late.OwnerCandidate
	*p = a.Plan
	return r, nil
}

func managedLateSchemaMatches(schema string, late *createManagedLateSessionRecovery) bool {
	if late == nil {
		return false
	}
	if late.ExpiredRenewal != nil {
		return schema == managedLateExpiredRenewalSchema
	}
	return schema == managedLateSessionSchema
}

func managedLateSessionMayRenewForScope(state managedSessionState, service environmentCreateServiceCeremony, subjects []string, late *createManagedLateSessionRecovery, now time.Time) bool {
	if late != nil && late.ExpiredRenewal != nil {
		return managedLateExpiredUnusedSession(state, late.ExpiredRenewal.PriorSession, service, subjects, now)
	}
	return managedLateSessionMayRenew(state, service, subjects, now)
}

func managedLateOriginalSessionForScope(current managedSessionState, late *createManagedLateSessionRecovery, newActive bool, subjects []string, now time.Time) bool {
	if late == nil {
		return false
	}
	if late.ExpiredRenewal == nil {
		return managedLateOriginalSessionCurrent(current, late.OriginalSessionState, newActive, subjects, now)
	}
	expectedOthers := 0
	if newActive {
		expectedOthers = 1
	}
	if current.OtherActive != expectedOthers {
		return false
	}
	current.OtherActive = 0
	return managedLateExpiredUnusedSession(current, late.ExpiredRenewal.PriorSession, late.SourceCeremony, subjects, now)
}

// The temporary provisioner may advance while retained workloads still carry
// their original enrollment SID. That original owner field is never fabricated.
func managedLateOriginalWorkloadSession(late *createManagedLateSessionRecovery) string {
	if late != nil && late.ExpiredRenewal != nil {
		return late.OriginalEnrollmentSession
	}
	if late == nil {
		return ""
	}
	return late.OriginalSession
}

func managedLateRBACSource(late *createManagedLateSessionRecovery, id string) (deploymentplan.Resource, bool) {
	if late == nil || late.AcceptedCore == nil {
		return deploymentplan.Resource{}, false
	}
	var source deploymentplan.Resource
	for _, resource := range late.AcceptedCore.Resources {
		if resource.ID == id {
			source = resource
		}
	}
	if source.ID == "" || source.UID != late.AcceptedCoreJournal.Owners[id] {
		return deploymentplan.Resource{}, false
	}
	if f := late.ExpiredRenewal; f != nil {
		prior, ok := f.PriorPost.CleanupOwners[id]
		if !ok || prior.UID != source.UID || deploymentplan.Digest(prior) != deploymentplan.Digest(f.PriorOwners[id]) {
			return deploymentplan.Resource{}, false
		}
		source = prior
	}
	return source, true
}

func managedLateCRLOriginalSourceSHA(late *createManagedLateSessionRecovery) string {
	if late != nil && late.ExpiredRenewal != nil {
		return late.ExpiredRenewal.PriorLate.SourceSHA
	}
	if late == nil {
		return ""
	}
	return late.SourceSHA
}

func managedLateVerifiedAppFrame(store secretStore, e *environmentCreateExecution, late *createManagedLateSessionRecovery) (*environmentCreateExecution, *createAppRootCRLArtifact, error) {
	if late != nil && late.ExpiredRenewal != nil {
		return e, nil, nil
	}
	return managedLateAppRootCRLFrame(store, e, true)
}

func managedLateExpiredRenewalCertificatePath(path string) bool {
	return regexp.MustCompile(`^/run/pki-bootstrap/renew-[a-f0-9]{12}/bootstrap\.crt$`).MatchString(path)
}

func managedLateExpiredRenewalBinding(f *createManagedLateExpiredRenewal, late *createManagedLateSessionRecovery) string {
	return deploymentplan.Digest(map[string]string{"schema": managedLateExpiredRenewalSchema, "source": f.SourceSHA, "prior": late.OriginalSession, "certificate": f.PriorCertificateSHA, "key": late.OriginalKeySHA, "csr": late.OriginalCSRSHA, "issuer": late.SourceCeremony.Intermediate.ID})
}

// Finalization authority is already independently proved by the caller. These
// branches authorize retirement of the exact retained e1/e0 copies only.
func verifyManagedLateExpiredHistoricalCopy(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, current environmentCreateServiceCeremony, observed createServiceSessionObserved, artifact *createFoundationRecoveryArtifact) (bool, bool, error) {
	late := runtime.ServiceSessionRecovery.ManagedLateSession
	f := late.ExpiredRenewal
	if f == nil {
		return false, false, nil
	}
	prior := f.PriorLate
	var expected environmentCreateServiceCeremony
	var saved managedSessionState
	switch observed.Cleanup.SessionID {
	case f.PriorSession.Session:
		expected = late.SourceCeremony
		saved = f.PriorSession
	case prior.OriginalSession:
		expected = prior.SourceCeremony
		saved = prior.OriginalSessionState
	default:
		return false, false, nil
	}
	bad := func() (bool, bool, error) { return false, true, managedHistoricalSeedBlock() }
	if deploymentplan.Digest(observed.Cleanup) != deploymentplan.Digest(expected.CleanupBinding) || deploymentplan.Digest(observed.UIDs) != deploymentplan.Digest(expected.CredentialUIDs) {
		return bad()
	}
	state, _, err := readManagedSessionState(ctx, cfg, runtime, saved.Session)
	if err != nil {
		return false, true, err
	}
	if state.Status != "expired" || state.OtherActive != 0 || !managedLateOriginalSessionUnchanged(saved, state, time.Now().UTC()) || !state.Expires.Equal(saved.Expires) || state.Fingerprint != saved.Fingerprint || state.Issuer != current.Intermediate.ID || state.Root != current.Root.RootFingerprint {
		return bad()
	}
	if saved.Session == f.PriorSession.Session {
		if len(state.Installed) != 0 || artifact.Plan.Materials[managedLateExpiredRenewalMaterial+"descriptor"] != managedLateExpiredRenewalDigest(f) {
			return bad()
		}
	} else {
		descriptor := managedCoreCompletedSession{Action: string(managedCoreRestoreOnly), Session: state.Session, Issuer: state.Issuer, Root: state.Root, Fingerprint: state.Fingerprint, Expires: state.Expires, Subjects: slices.Clone(state.Subjects), Installed: slices.Clone(state.Installed)}
		slices.Sort(descriptor.Installed)
		if len(state.Installed) != 8 || slices.Contains(state.Installed, "service:video-cloud-logingester") || artifact.Plan.Materials["accepted-service-core-managed-complete-current-session"] != deploymentplan.Digest(descriptor) {
			return bad()
		}
	}
	return true, true, runtime.Execution.Check(ctx)
}

func managedLateOriginalWorkloadCleanup(late *createManagedLateSessionRecovery) environmentBootstrapCleanupBinding {
	if late != nil && late.ExpiredRenewal != nil {
		return late.ExpiredRenewal.PriorLate.SourceCeremony.CleanupBinding
	}
	if late == nil {
		return environmentBootstrapCleanupBinding{}
	}
	return late.SourceCeremony.CleanupBinding
}

// Owner plans execute after the accepted new cleanup child. Bind the one
// sanctioned future Role target without inventing UID or acceptance evidence.
func managedLateExpiredOwnerDependency(late *createManagedLateSessionRecovery, source deploymentplan.Resource) deploymentplan.Resource {
	if late == nil || late.ExpiredRenewal == nil || source.ID != "Role/"+late.AcceptedCore.Stack+"-video-cloud/bootstrap-cleanup" {
		return source
	}
	source.Desired = deploymentplan.Clone(source.Desired)
	rules := certIssuerObjectList(source.Desired["rules"])
	if len(rules) != 1 {
		return deploymentplan.Resource{}
	}
	rule := deploymentplan.Map(rules[0])
	expected := deploymentplan.Object{"apiGroups": []any{""}, "resources": []any{"secrets"}, "resourceNames": []any{"service-bootstrap-" + late.OriginalSession}, "verbs": []any{"get", "delete"}}
	if deploymentplan.Digest(rule) != deploymentplan.Digest(expected) {
		return deploymentplan.Resource{}
	}
	rule["resourceNames"] = []any{"service-bootstrap-" + late.NewSessionID}
	return source
}

func admitManagedLateExpiredRenewalCandidates(ctx context.Context, b environmentKubernetesBackend, late *createManagedLateSessionRecovery) error {
	if late == nil || late.ExpiredRenewal == nil || validateManagedLateSessionOwnerCandidate(late, late.OwnerCandidate) != nil || validateManagedLateLogCandidates(late) != nil {
		return managedLateExpiredRenewalBlock("qualified-candidates")
	}
	for _, candidate := range []*deploymentplan.Plan{late.SeedCandidate, late.CustodyCandidate, late.LogStopCandidate, late.LogHelperCandidate} {
		if candidate == nil {
			return managedLateExpiredRenewalBlock("candidate-absent")
		}
		if err := candidate.Admit(ctx, b); err != nil {
			return err
		}
	}
	return nil
}
