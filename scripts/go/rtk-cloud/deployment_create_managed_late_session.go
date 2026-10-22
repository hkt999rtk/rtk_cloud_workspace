// Recover the original expired bootstrap session after accepted managed Core.
// Qualification binds its authenticated source, original key/CSR and exact
// native owners before a separately admitted continuation may write.
package main

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const managedLateSessionSchema = "rtk-cloud.create-managed-original-key-session-recovery/v1"
const managedLateSessionOwnerPlanFilename = "managed-late-session-owners-plan.json"

type managedLateSessionMode string

const (
	managedLateSessionMQTTRepair       managedLateSessionMode = "mqtt-root-crl-repair"
	managedLateSessionACKReady         managedLateSessionMode = "late-original-key-ack"
	managedLateSessionOpenBaoTLSRepair managedLateSessionMode = "openbao-tls-root-crl-repair"
)

type createManagedLateSessionRecovery struct {
	ExpiredRenewal                                                                *createManagedLateExpiredRenewal `json:"ExpiredRenewal,omitempty"`
	ParentSHA, SourceOwnerSHA                                                     string
	Mode                                                                          managedLateSessionMode
	CoreSourceSHA                                                                 string
	MQTTRepair                                                                    *createMQTTRootCRLArtifact         `json:"MQTTRepair,omitempty"`
	MQTTCompletion                                                                *createMQTTRootCRLCompletion       `json:"MQTTCompletion,omitempty"`
	OpenBaoTLSRepair                                                              *createOpenBaoTLSRootCRLArtifact   `json:"OpenBaoTLSRepair,omitempty"`
	OpenBaoTLSCompletion                                                          *createOpenBaoTLSRootCRLCompletion `json:"OpenBaoTLSCompletion,omitempty"`
	SourceCeremony                                                                environmentCreateServiceCeremony
	SourceRenewedIntent                                                           createServiceSessionIntent
	OwnerCandidate                                                                *deploymentplan.Plan
	SeedCandidate                                                                 *deploymentplan.Plan
	CustodyCandidate                                                              *deploymentplan.Plan
	HelperBindingSHA                                                              string
	LogProducer                                                                   *createLogACKHelperDraft
	LogStopCandidate, LogHelperCandidate, LogRetireCandidate, LogRestoreCandidate *deploymentplan.Plan
	OriginalRepairReview, RestoreReview, SourceSHA, SourceArtifactSHA             string
	Review                                                                        string `json:"Review,omitempty"` // assigned after qualification digest
	OriginalSession, OriginalEnrollmentSession                                    string
	OriginalSessionState                                                          managedSessionState
	OriginalClientsSHA, OriginalKeySHA, OriginalCSRSHA                            string
	AcceptedCore                                                                  *deploymentplan.Plan
	AcceptedCoreJournal                                                           deploymentplan.Journal
	Renewal                                                                       *createManagedSessionRecovery
	NewSessionID, NewCertificatePath                                              string
	LogStateSHA, LogFingerprint, LogSPKISHA                                       string
	LogNotDueUntil                                                                time.Time
	SignerPolicySHA, OriginalSignerRoleSHA, OriginalControllerRoleSHA             string
	ControllerPolicies                                                            []string
	OriginalSignerRole, OriginalControllerRole                                    deploymentplan.Object
	SeedAuthorization, CleanupAuthorization                                       deploymentplan.Object
	Custody                                                                       deploymentplan.Resource
	TrustOwners, VerifierOwners                                                   map[string]deploymentplan.Resource
	Owners                                                                        map[string]deploymentplan.Resource // exact ten native ACK controllers
	DependencyOwners                                                              map[string]deploymentplan.Resource // exact retained DAG closure
}

// A selector is never acceptance authority. The source loader below also
// requires the exact archived admission, canonical source and real child proof.
// In particular the current original pending final-core stage cannot match.
func managedLateSessionRestoreReview(j environmentCreateJournal) string {
	if len(j.Entries) < 2 {
		return ""
	}
	entry := j.Entries[len(j.Entries)-1]
	const prefix = "kubernetes/managed-complete-issuer-forward-"
	review := strings.TrimPrefix(entry.Key, prefix)
	if !strings.HasPrefix(entry.Key, prefix) || !environmentPreflightDigest.MatchString(review) || entry.Key != prefix+review || entry.Phase != "managed-complete-issuer-forward-"+review || entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 {
		return ""
	}
	admission := j.Entries[len(j.Entries)-2]
	if admission.Key != "openbao-recovery-admission-"+review || admission.Phase != "recovery" || admission.State != "completed" || admission.CompletedAt.IsZero() || len(admission.Receipt) == 0 {
		return ""
	}
	return review
}

// Pure historical derivation only. No current owner, seed Secret, reader or
// workload observation is fabricated to call an earlier live recovery loader.
func loadManagedLateSessionSource(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, p *createFoundationRecoveryPlan, modes ...managedLateSessionMode) (*createServiceSessionRecovery, *createManagedLateSessionRecovery, error) {
	bad := func(stage string) (*createServiceSessionRecovery, *createManagedLateSessionRecovery, error) {
		return nil, nil, createProviderRecoveryBlock("late original-key session requires accepted original core restoration: stage=" + stage)
	}
	mode := managedLateSessionACKReady
	if len(modes) > 1 {
		return bad("exact-operation-mode")
	}
	if len(modes) == 1 {
		mode = modes[0]
	}
	if mode != managedLateSessionMQTTRepair && mode != managedLateSessionACKReady && mode != managedLateSessionOpenBaoTLSRepair {
		return bad("exact-operation-mode")
	}
	if e == nil || p == nil || p.Materials == nil || store.Root != e.Store.Root {
		return bad("accepted-restore")
	}
	coreSource, mqttArtifact, mqttCompletion, tlsArtifact, tlsCompletion, err := loadManagedLateSessionOriginalSource(store, e)
	if err != nil {
		return nil, nil, err
	}
	review := managedLateSessionRestoreReview(coreSource.Journal)
	if review == "" {
		return bad("accepted-restore-tail")
	}
	admission, captured, err := loadAdmittedCreateRecovery(store, coreSource, review)
	if err != nil || admission.Plan.Schema != managedCorePartialSchema || admission.Plan.Materials["accepted-service-core-complete-issuer-partial-action"] != deploymentplan.Digest(managedCoreRestoreOnly) {
		return bad("restore-admission")
	}
	// Direct MQTT callers have no candidate yet. Hydrate only the complete
	// authenticated original create plan; a supplied plan must select that exact
	// generation/configuration/release, never a newly rendered parent.
	if p.Plan.Stack == "" {
		p.Plan = admission.Plan.Plan
	} else if p.Plan.Environment != admission.Plan.Plan.Environment || p.Plan.Stack != admission.Plan.Plan.Stack || p.Plan.ConfigurationSHA256 != admission.Plan.Plan.ConfigurationSHA256 || deploymentplan.Digest(p.Plan.Release) != deploymentplan.Digest(admission.Plan.Plan.Release) {
		return bad("authenticated-original-parent-plan")
	}
	old := *coreSource
	old.Journal = captured
	historical337, originalAdmission, core, err := loadManagedCorePartialSource(store, &old)
	if err != nil || core.Action != managedCoreRestoreOnly {
		return bad("original-partial-core")
	}
	if core.PriorForward != nil {
		core.CertIssuerPrepare, err = loadManagedCoreQualifiedCertIssuerPrepare(store, review, admission.Plan.Materials)
		if err != nil {
			return bad("accepted-original-role-image")
		}
	}
	accepted, _, err := readCreateOpenBaoACKCompletedStage(store, coreSource.Journal, "managed-complete-issuer-forward-"+review)
	var acceptedJournal deploymentplan.Journal
	if err != nil || validateManagedCorePartialCandidate(core, accepted) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", accepted.SHA256(), "journal.json"), &acceptedJournal) != nil || !createOpenBaoACKCompletedChild(accepted, acceptedJournal) {
		return bad("accepted-core-child")
	}
	for index, resource := range accepted.Resources {
		if acceptedJournal.Completed[index] != resource.ID || acceptedJournal.Owners[resource.ID] != resource.UID {
			return bad("accepted-core-order-owner")
		}
	}
	source330, _, latest, err := loadManagedApplicationInterruptedSource(store, historical337)
	if err != nil || latest.Previous == nil || latest.Previous.Previous == nil || latest.Previous.Previous.Previous != nil {
		return bad("bounded-original-application-source")
	}
	for remaining := 2; managedApplicationPartialReview(source330.Journal) == "" && remaining > 0; remaining-- {
		source330, _, _, err = loadManagedApplicationInterruptedSourceDepth(store, source330, remaining)
		if err != nil {
			return bad("bounded-original-application-history")
		}
	}
	r, err := deriveManagedApplicationPartialRecovery(cfg, store, source330, p)
	if err != nil || r.ManagedTransportRecovery == nil || r.ManagedTransportRecovery.Partial == nil {
		return bad("immutable-original-identity")
	}
	r.ManagedApplicationPartial.Interrupted = latest
	r.ManagedCorePartial = core
	r.ManagedCorePartial.ForwardReview = review
	r.Review = core.OriginalRepairReview
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create/openbao-recovery", core.OriginalRepairReview, "openbao-host-ack-source.json"), &r.OpenBaoACK) != nil || r.OpenBaoACK == nil || r.OpenBaoACK.OriginalPlanSHA != r.ManagedApplicationPartial.Historical.PostManaged.Plan.SHA256() || r.OpenBaoACK.OriginalJournalSHA != deploymentplan.Digest(r.ManagedApplicationPartial.Historical.PostManaged.Journal) {
		return bad("original-retained-openbao-role")
	}
	r.CoreAccepted, r.PostServiceResume = true, true
	r.Renewal, r.PostServiceRenewal, r.ApplicationForward = false, false, false
	r.GrantCandidate, r.RenewalCandidate, r.InstallationCandidate, r.QuiesceCandidate, r.Candidate = nil, nil, nil, nil, nil
	for key, value := range originalAdmission.Plan.Materials {
		if strings.HasPrefix(key, "accepted-service-core-managed-application-") || strings.HasPrefix(key, "accepted-service-core-openbao-ack-") {
			p.Materials[key] = value
		}
	}
	if err := hydrateManagedLateOriginalHostServerPin(r, &originalAdmission, core.OriginalRepairReview); err != nil {
		return bad("original-host-server-admission")
	}
	late := &createManagedLateSessionRecovery{ParentSHA: e.Journal.PlanSHA256, SourceOwnerSHA: deploymentplan.Digest(e.owner), OriginalRepairReview: core.OriginalRepairReview, RestoreReview: review, SourceSHA: deploymentplan.Digest(e.Journal), SourceArtifactSHA: deploymentplan.Digest(admission), OriginalSession: r.Ceremony.BootstrapSessionID, OriginalEnrollmentSession: r.Ceremony.BootstrapSessionID, OriginalClientsSHA: r.ManagedTransportRecovery.Partial.ClientsSHA, OriginalKeySHA: r.OriginalKeySHA, OriginalCSRSHA: r.OriginalCSRSHA, AcceptedCore: accepted, AcceptedCoreJournal: acceptedJournal}
	late.Mode, late.CoreSourceSHA, late.MQTTRepair, late.MQTTCompletion = mode, deploymentplan.Digest(coreSource.Journal), mqttArtifact, mqttCompletion
	late.OpenBaoTLSRepair, late.OpenBaoTLSCompletion = tlsArtifact, tlsCompletion
	if mode == managedLateSessionMQTTRepair && (mqttArtifact != nil || tlsArtifact != nil) || mode == managedLateSessionOpenBaoTLSRepair && (mqttArtifact == nil || tlsArtifact != nil) || mode == managedLateSessionACKReady && (mqttArtifact == nil || tlsArtifact == nil) {
		return bad("exact-repair-order")
	}
	late.SourceCeremony, late.SourceRenewedIntent = r.Ceremony, r.RenewedIntent
	late.SourceCeremony.BootstrapCredentialNamespaces = slices.Clone(r.Ceremony.BootstrapCredentialNamespaces)
	late.SourceCeremony.CredentialUIDs = map[string]string{}
	for namespace, uid := range r.Ceremony.CredentialUIDs {
		late.SourceCeremony.CredentialUIDs[namespace] = uid
	}
	late.SourceRenewedIntent.Subjects = slices.Clone(r.RenewedIntent.Subjects)
	late.SourceRenewedIntent.Namespaces = slices.Clone(r.RenewedIntent.Namespaces)
	return r, late, nil
}

// The current actual journal is never substituted for a historical journal in
// a live verifier. The returned copy is a pure immutable ancestry view whose
// hash is independently bound by the completed MQTT repair; SourceSHA above
// still binds every current entry/lease/owner metadata field.
func loadManagedLateSessionCoreSource(store secretStore, e *environmentCreateExecution) (*environmentCreateExecution, *createMQTTRootCRLArtifact, *createMQTTRootCRLCompletion, error) {
	bad := func(stage string) (*environmentCreateExecution, *createMQTTRootCRLArtifact, *createMQTTRootCRLCompletion, error) {
		return nil, nil, nil, createProviderRecoveryBlock("late source requires exact accepted Core and completed native MQTT repair authority: stage=" + stage)
	}
	if e == nil || store.Root != e.Store.Root {
		return bad("selected-original-owner")
	}
	if managedLateSessionRestoreReview(e.Journal) != "" && managedCorePartialNativeCheckpoint(store, e) {
		return e, nil, nil, nil
	}
	if len(e.Journal.Entries) < 4 {
		return bad("completed-mqtt-tail")
	}
	tail := e.Journal.Entries[len(e.Journal.Entries)-4:]
	review := strings.TrimPrefix(tail[0].Key, "mqtt-root-crl-renewal-")
	if !environmentPreflightDigest.MatchString(review) || tail[0].Key != "mqtt-root-crl-renewal-"+review {
		return bad("completed-mqtt-tail")
	}
	keys := []string{tail[0].Key, tail[0].Key + "/native-import", tail[0].Key + "/native-runtime-video-cloud-api", tail[0].Key + "/native-runtime-video-cloud-logingester"}
	for index, entry := range tail {
		if entry.Key != keys[index] || entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 || !environmentPreflightDigest.MatchString(entry.IntentSHA256) {
			return bad("completed-mqtt-tail")
		}
	}
	artifact, completion, err := readCreateMQTTRootCRLRecovery(store, e.Journal.PlanSHA256)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("authenticate original MQTT completion: %w", err)
	}
	if artifact == nil || completion == nil || !completion.Completed || completion.Parent != e.Journal.PlanSHA256 || completion.Review != review || deploymentplan.Digest(artifact.Plan) != review || completion.FinalEntryCount != len(e.Journal.Entries) || completion.FinalEntriesSHA != deploymentplan.Digest(e.Journal.Entries) || !environmentPreflightDigest.MatchString(completion.FinalEntriesSHA) || completion.FinalJournalSHA != deploymentplan.Digest(e.Journal) {
		return bad("canonical-completed-mqtt-proof")
	}
	var source environmentCreateJournal
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create/mqtt-root-crl-recovery", review, "source-journal.json"), &source) != nil || deploymentplan.Digest(source) != artifact.Plan.SourceJournalSHA || completion.SourceEntryCount != len(source.Entries) || len(e.Journal.Entries) != len(source.Entries)+4 || len(e.Journal.KubernetesLeases) < len(source.KubernetesLeases) {
		return bad("original-core-source")
	}
	prefix := e.Journal
	prefix.Entries = e.Journal.Entries[:len(source.Entries)]
	prefix.KubernetesLeases = e.Journal.KubernetesLeases[:len(source.KubernetesLeases)]
	if deploymentplan.Digest(prefix) != deploymentplan.Digest(source) {
		return bad("unchanged-complete-prefix")
	}
	// The MQTT reader independently authenticates every exact added native
	// receipt and lease. There is no general extra-entry or metadata allowance.
	core := *e
	core.Journal = source
	if managedLateSessionRestoreReview(source) == "" || !managedCorePartialNativeCheckpoint(store, &core) {
		return bad("accepted-original-core-prefix")
	}
	return &core, artifact, completion, nil
}

// TLS is the newest exact four-entry suffix. Its reader authenticates the
// stored pre-TLS source; the MQTT reader then authenticates the preceding
// four entries, before the original Core view is exposed.
func loadManagedLateSessionOriginalSource(store secretStore, e *environmentCreateExecution) (*environmentCreateExecution, *createMQTTRootCRLArtifact, *createMQTTRootCRLCompletion, *createOpenBaoTLSRootCRLArtifact, *createOpenBaoTLSRootCRLCompletion, error) {
	bad := func() (*environmentCreateExecution, *createMQTTRootCRLArtifact, *createMQTTRootCRLCompletion, *createOpenBaoTLSRootCRLArtifact, *createOpenBaoTLSRootCRLCompletion, error) {
		return nil, nil, nil, nil, nil, environmentCreateBootstrapBlocker("late source requires exact completed original TLS repair suffix")
	}
	if e == nil {
		return bad()
	}
	prior := e
	var artifact *createOpenBaoTLSRootCRLArtifact
	var completion *createOpenBaoTLSRootCRLCompletion
	if len(e.Journal.Entries) >= 4 && strings.HasPrefix(e.Journal.Entries[len(e.Journal.Entries)-4].Key, "openbao-tls-root-crl-renewal-") {
		review := strings.TrimPrefix(e.Journal.Entries[len(e.Journal.Entries)-4].Key, "openbao-tls-root-crl-renewal-")
		var err error
		artifact, completion, err = readCreateOpenBaoTLSRootCRLRecovery(store, e.Journal.PlanSHA256)
		if err != nil {
			return nil, nil, nil, nil, nil, fmt.Errorf("authenticate original TLS completion: %w", err)
		}
		if artifact == nil || completion == nil || !completion.Completed || completion.Review != review || completion.Parent != e.Journal.PlanSHA256 || deploymentplan.Digest(artifact.Plan) != review || completion.FinalEntryCount != len(e.Journal.Entries) || completion.FinalEntriesSHA != deploymentplan.Digest(e.Journal.Entries) || !environmentPreflightDigest.MatchString(completion.FinalEntriesSHA) || completion.FinalJournalSHA != deploymentplan.Digest(e.Journal) {
			return bad()
		}
		var source environmentCreateJournal
		if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-tls-root-crl-recovery", review, "source-journal.json"), &source) != nil || artifact.Plan.SourceJournalSHA != deploymentplan.Digest(source) || completion.SourceEntryCount != len(source.Entries) || len(e.Journal.Entries) != len(source.Entries)+4 || len(e.Journal.KubernetesLeases) < len(source.KubernetesLeases) {
			return bad()
		}
		prefix := e.Journal
		prefix.Entries = e.Journal.Entries[:len(source.Entries)]
		prefix.KubernetesLeases = e.Journal.KubernetesLeases[:len(source.KubernetesLeases)]
		if deploymentplan.Digest(prefix) != deploymentplan.Digest(source) {
			return bad()
		}
		copy := *e
		copy.Journal = source
		prior = &copy
	}
	core, mqtt, mqttDone, err := loadManagedLateSessionCoreSource(store, prior)
	if err != nil {
		return nil, nil, nil, nil, nil, err
	}
	if artifact != nil && (mqtt == nil || mqttDone == nil || mqttDone.FinalJournalSHA != artifact.Plan.SourceJournalSHA) {
		return bad()
	}
	return core, mqtt, mqttDone, artifact, completion, nil
}

func managedLateSessionNativeCheckpoint(store secretStore, e *environmentCreateExecution) bool {
	if _, _, _, _, _, err := loadManagedLateSessionOriginalSource(store, e); err == nil {
		return true
	}
	if e == nil {
		return false
	}
	// Only an actual literal admission can select its own immutable descriptor.
	// No directory search or foreign journal suffix creates authority.
	for index := len(e.Journal.Entries) - 1; index >= 0; index-- {
		entry := e.Journal.Entries[index]
		review := strings.TrimPrefix(entry.Key, "openbao-recovery-admission-")
		if entry.Key != "openbao-recovery-admission-"+review || !environmentPreflightDigest.MatchString(review) {
			continue
		}
		var a createFoundationRecoveryArtifact
		if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, "qualification.json"), &a) != nil || a.Plan.Schema != managedLateSessionSchema && a.Plan.Schema != managedLateExpiredRenewalSchema {
			continue
		}
		var late createManagedLateSessionRecovery
		if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", review, "managed-late-session-source.json"), &late) != nil || late.Review != "" && late.Review != review {
			return false
		}
		late.Review = review
		_, _, err := managedLateSessionRuntimeProgress(store, e, &late)
		return err == nil
	}
	return false
}

func managedLateSessionMayRenew(state managedSessionState, service environmentCreateServiceCeremony, subjects []string, now time.Time) bool {
	if !managedSessionMayRenew(state, service, subjects, now) || len(subjects) != 9 || len(state.Installed) != 8 || !slices.Contains(subjects, "service:video-cloud-logingester") {
		return false
	}
	for _, subject := range subjects {
		if slices.Contains(state.Installed, subject) != (subject != "service:video-cloud-logingester") {
			return false
		}
	}
	return true
}

// This draft plans one distinct native session on the original key/CSR. A new
// complete native observation of accepted16+26+routing, 9CAs, 11clients,
// 4registeredservers, 18volumes and original custody must run before it. That
// observer must use late.AcceptedCore, not the old failed-core current guard.
func prepareManagedLateSessionSeed(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery, late *createManagedLateSessionRecovery) error {
	bad := func(stage string) error {
		return createProviderRecoveryBlock("late original-key seed qualification requires exact preserved native identity: stage=" + stage)
	}
	if late == nil || r == nil || late.Mode != managedLateSessionACKReady || late.SourceSHA != deploymentplan.Digest(e.Journal) || !managedLateSessionNativeCheckpoint(store, e) || late.OriginalSession != r.Ceremony.BootstrapSessionID || late.OriginalEnrollmentSession != late.OriginalSession || !environmentPreflightDigest.MatchString(late.OriginalClientsSHA) || late.OriginalKeySHA != r.OriginalKeySHA || late.OriginalCSRSHA != r.OriginalCSRSHA {
		return bad("authenticated-source")
	}
	if err := verifyManagedLateSessionObservationWithMode(ctx, cfg, store, e, b, r, p, late, managedLateSessionACKReady); err != nil {
		return err
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: b}
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil {
		return err
	}
	state, raw, err := readManagedSessionState(ctx, cfg, runtime, late.OriginalSession)
	if err != nil || !managedLateSessionMayRenew(state, r.Ceremony, subjects, time.Now().UTC()) {
		return bad("original-expired-eight-only-missing-log")
	}
	clients, err := readManagedSessionClients(ctx, cfg, runtime, r.Ceremony, subjects)
	if err != nil || clients != late.OriginalClientsSHA {
		return bad("eleven-original-clients")
	}
	original, originalJournal, err := acceptedCreateStage(store, e, r.SourceRenewalStage)
	if err != nil || len(original.Resources) != 1 || original.Resources[0].Desired["kind"] != "Job" {
		return bad("original-key-job")
	}
	resource := original.Resources[0]
	actual, err := b.Read(ctx, resource.ID)
	if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != originalJournal.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, actual) {
		return bad("original-key-job-owner")
	}
	oldPath := r.RenewedCertPath
	material, err := b.command(ctx, nil, "--namespace", r.Ceremony.BootstrapCredentialNamespace, "exec", "deployment/"+r.Ceremony.CustodyReader, "--container", "custody", "--", "cat", oldPath)
	leaf, parseErr := parseFirstCertificatePEM(material)
	intermediate, issuerErr := parseFirstCertificatePEM([]byte(r.Ceremony.Intermediate.Certificate))
	if err != nil || parseErr != nil || issuerErr != nil || leaf.Subject.CommonName != "service:deployment-bootstrap" || leaf.CheckSignatureFrom(intermediate) != nil || leaf.NotAfter.After(time.Now().UTC()) || !leaf.NotAfter.Equal(state.Expires) || environmentPreflightBytesSHA256(leaf.Raw) != state.Fingerprint {
		return bad("original-expired-certificate")
	}
	proof := &createManagedSessionRecovery{OriginalSession: state.Session, OriginalCertificatePath: oldPath, OriginalCertificateSHA256: environmentPreflightBytesSHA256(material), SessionSHA256: environmentPreflightBytesSHA256(raw), ClientsSHA256: clients, SourceStage: r.SourceRenewalStage, SourcePlanSHA256: original.SHA256(), SourceJournalSHA256: deploymentplan.Digest(originalJournal)}
	late.OriginalSessionState = state
	if err := verifyManagedSeedOriginalCustody(ctx, runtime, r, proof); err != nil {
		return err
	}
	image, selection, err := loadServiceTrustRepairImage(ctx, cfg, store, e.Journal.PlanSHA256)
	if err != nil {
		return err
	}
	binding := deploymentplan.Digest(map[string]string{"schema": managedLateSessionSchema, "parent": e.Journal.PlanSHA256, "accepted_source": late.SourceSHA, "accepted_core": late.AcceptedCore.SHA256(), "accepted_core_journal": deploymentplan.Digest(late.AcceptedCoreJournal), "original_session": state.Session, "original_session_state": proof.SessionSHA256, "clients": clients, "original_key": r.OriginalKeySHA, "original_csr": r.OriginalCSRSHA})
	newSession := sessionRenewalUUID(e.Journal.PlanSHA256, binding, state.Session)
	if newSession == state.Session {
		return bad("distinct-native-session")
	}
	definition, err := managedSessionRenewalDefinition(resource.Desired, e.Journal.PlanSHA256, binding, newSession, image)
	if err != nil {
		return err
	}
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return err
	}
	candidate, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("managed-late-original-key-session", []deploymentplan.Definition{definition})}, []deploymentplan.Definition{definition}, live)
	if err != nil || len(candidate.Resources) != 1 || candidate.Resources[0].Action != "create" {
		return bad("one-new-original-key-session-job")
	}
	proof.BindingSHA256, proof.CandidateSHA256 = binding, candidate.SHA256()
	late.Renewal, late.NewSessionID, late.SeedCandidate = proof, newSession, candidate
	late.NewCertificatePath = "/run/pki-bootstrap/renew-" + binding[:12] + "/bootstrap.crt"
	r.RenewedIntent = r.Intent
	r.RenewedIntent.SessionID = newSession
	r.RenewedIntent.Subjects = slices.Clone(subjects)
	r.GrantImage, r.RenewalBindingSHA, r.RenewalCandidate = image, binding, candidate
	r.RenewedCertPath = late.NewCertificatePath
	// Do not change r.Ceremony or r.Review here. The separately admitted late
	// executor uses the fresh review for all new paths/stages, and returns the
	// actual new ceremony only after new native certificate/Secret UID readback.
	p.Materials["accepted-service-core-late-session-source"] = late.SourceSHA
	p.Materials["accepted-service-core-late-session-original-enrollment-session"] = deploymentplan.Digest(late.OriginalEnrollmentSession)
	p.Materials["accepted-service-core-late-session-original-key-seed-plan"] = candidate.SHA256()
	p.Materials["accepted-service-core-late-session-image-selection"] = selection
	p.Materials["accepted-service-core-late-session-image"] = deploymentplan.Digest(image)
	if err := prepareManagedLateSessionOwners(ctx, cfg, store, e, b, p, r, late); err != nil {
		return err
	}
	if err := prepareManagedLateSessionAuthorizations(ctx, cfg, store, e, b, p, r, late); err != nil {
		return err
	}
	if err := prepareManagedLateLogCandidates(ctx, cfg, store, e, b, p, r, late); err != nil {
		return err
	}
	p.Materials["accepted-service-core-late-session-source-descriptor"] = managedLateSessionSourceDigest(late)
	if err := e.Check(ctx); err != nil {
		return err
	}
	// A successfully prepared late session is one recovery object. Bind the
	// authenticated descriptor and its owner candidate here so every caller,
	// including isolated qualification harnesses, publishes the complete
	// archive. Requiring each caller to repeat this binding allowed a valid
	// preparation to silently emit an ordinary session archive without the
	// managed-late descriptor commit marker.
	r.ManagedLateSession = late
	r.Candidate = late.OwnerCandidate
	return nil
}

// Hydrate an original host pin only from the already authenticated original
// repair admission. Native observations may verify this pin but cannot set it.
func hydrateManagedLateOriginalHostServerPin(r *createServiceSessionRecovery, admission *createFoundationRecoveryArtifact, originalReview string) error {
	bad := func() error { return managedCoreObservationBlock("late-original-host-server-admission") }
	if r == nil || admission == nil || r.ManagedCorePartial == nil || r.ManagedTransportRecovery == nil || r.ManagedTransportRecovery.Partial == nil || !environmentPreflightDigest.MatchString(originalReview) {
		return bad()
	}
	if admission.Plan.Schema != "rtk-cloud.create-managed-application-trust-openbao-ack-recovery/v1" || r.Review != originalReview || r.ManagedCorePartial.OriginalRepairReview != originalReview || deploymentplan.Digest(admission.Plan) != originalReview || r.ManagedCorePartial.ArtifactSHA != deploymentplan.Digest(admission) {
		return bad()
	}
	pin := admission.Plan.Materials["accepted-service-core-managed-application-current-servers"]
	if !environmentPreflightDigest.MatchString(pin) || r.ManagedTransportRecovery.Partial.ServerSHA != "" && r.ManagedTransportRecovery.Partial.ServerSHA != pin {
		return bad()
	}
	r.ManagedTransportRecovery.Partial.ServerSHA = pin
	return nil
}
