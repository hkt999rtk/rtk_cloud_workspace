// PRIVATE ONLY. This validator is an additional hook AFTER the unchanged base
// OpenBao completion validator, iff native.late_review_sha256 is present.
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"path/filepath"
	"slices"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// No live calls: every new allocation is bound to the actually accepted child,
// separately archived native readback and genuine original owner ancestry.
func validateCreateManagedLateOpenBaoACKCompletion(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, proof *createOpenBaoACKCompletion) error {
	bad := func(stage string) error { return createOpenBaoACKBlock("completed-late-" + stage) }
	if ctx == nil || ctx.Err() != nil || e == nil || proof == nil || e.Store.Root != store.Root || proof.Parent != e.Journal.PlanSHA256 || proof.Environment != cfg.Environment || proof.CompletedAt.IsZero() {
		return bad("selected-generation")
	}
	review := deploymentplan.String(proof.Native["late_review_sha256"])
	if !environmentPreflightDigest.MatchString(review) || review == proof.Review {
		return bad("fresh-review")
	}
	base := filepath.Join("deployment/create/openbao-recovery", review)
	var late createManagedLateSessionRecovery
	var owner environmentCreateOwner
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, "managed-late-session-source.json"), &late) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(base, "source-owner.json"), &owner) != nil || late.Review != "" && late.Review != review || late.Mode != managedLateSessionACKReady || late.ParentSHA != proof.Parent || late.OriginalRepairReview != proof.Review || late.NewSessionID == late.OriginalSession || late.ExpiredRenewal == nil && late.OriginalSession != late.OriginalEnrollmentSession || !environmentBootstrapOwnerUUID.MatchString(late.NewSessionID) || proof.Native["session_id"] != late.NewSessionID || proof.Native["original_session_id"] != late.OriginalSession || proof.Native["late_source_sha256"] != late.SourceSHA || proof.Native["late_descriptor_sha256"] != managedLateSessionSourceDigest(&late) || late.SourceOwnerSHA != deploymentplan.Digest(owner) || owner.PlanSHA256 != proof.Parent || owner.OwnerToken != e.Journal.OwnerToken {
		return bad("immutable-source-descriptor")
	}
	late.Review = review
	// Base validator already authenticated current owner.json or its completed
	// archive. This copy supplies that same owner to pure source/progress readers.
	current := *e
	current.owner = owner
	if err := validateManagedLateTerminalFinalizerCompletion(store, &current, &late); err != nil {
		return err
	}
	if err := validateManagedLateRemainingCompletion(store, &current, &late, proof); err != nil {
		return err
	}
	bounded, a, err := managedLateVerifiedProgressPrefix(store, &current, &late)
	var captured *environmentCreateExecution
	if err == nil {
		captured, _, err = managedLateSessionRuntimeProgress(store, bounded, &late)
	}
	if err != nil || a == nil || !managedLateSchemaMatches(a.Plan.Schema, &late) || a.Plan.ParentOwnerSHA != deploymentplan.Digest(owner) || a.Plan.ParentJournalSHA != late.SourceSHA || deploymentplan.Digest(captured.Journal) != late.SourceSHA || !a.QualifiedAt.Before(proof.CompletedAt) || a.Qualification.Decision != "GO" {
		return bad("actual-late-admission")
	}
	p := createFoundationRecoveryPlan{Plan: a.Plan.Plan, Materials: map[string]string{}}
	source, loaded, err := loadManagedLateEffectiveSource(cfg, store, captured, &p, &late)
	if err != nil || loaded == nil || loaded.SourceSHA != late.SourceSHA || loaded.CoreSourceSHA != late.CoreSourceSHA || loaded.SourceArtifactSHA != late.SourceArtifactSHA || loaded.OriginalRepairReview != late.OriginalRepairReview || loaded.RestoreReview != late.RestoreReview || loaded.AcceptedCore.SHA256() != late.AcceptedCore.SHA256() || deploymentplan.Digest(loaded.AcceptedCoreJournal) != deploymentplan.Digest(late.AcceptedCoreJournal) || deploymentplan.Digest(loaded.SourceCeremony) != deploymentplan.Digest(late.SourceCeremony) || deploymentplan.Digest(loaded.SourceRenewedIntent) != deploymentplan.Digest(late.SourceRenewedIntent) || loaded.OriginalKeySHA != late.OriginalKeySHA || loaded.OriginalCSRSHA != late.OriginalCSRSHA || loaded.OriginalClientsSHA != late.OriginalClientsSHA {
		return bad("genuine-accepted-core-and-identities")
	}
	mqtt, mqttCompletion, err := readCreateMQTTRootCRLRecovery(store, proof.Parent)
	tlsRoot, tlsCompletion, tlsErr := readCreateOpenBaoTLSRootCRLRecovery(store, proof.Parent)
	if err != nil || tlsErr != nil || mqtt == nil || mqttCompletion == nil || tlsRoot == nil || tlsCompletion == nil || !mqttCompletion.Completed || !tlsCompletion.Completed || mqtt.Plan.Root.ID != source.MQTTRoot.ID || tlsRoot.Plan.Root.ID != source.OpenBaoRoot.ID || mqtt.Plan.Signed.Number != "2" || tlsRoot.Plan.Signed.Number != "2" || deploymentplan.Digest(mqtt) != deploymentplan.Digest(late.MQTTRepair) || deploymentplan.Digest(mqttCompletion) != deploymentplan.Digest(late.MQTTCompletion) || deploymentplan.Digest(tlsRoot) != deploymentplan.Digest(late.OpenBaoTLSRepair) || deploymentplan.Digest(tlsCompletion) != deploymentplan.Digest(late.OpenBaoTLSCompletion) || mqttCompletion.FinalJournalSHA != tlsRoot.Plan.SourceJournalSHA || tlsCompletion.FinalJournalSHA != managedLateCRLOriginalSourceSHA(&late) || len(captured.Journal.Entries) < 8 || !captured.Journal.Entries[len(captured.Journal.Entries)-1].CompletedAt.Before(a.QualifiedAt) {
		return bad("actual-original-mqtt-and-openbao-tls-root2")
	}
	if late.Renewal == nil || late.OwnerCandidate == nil || late.CustodyCandidate == nil || late.LogProducer == nil || validateManagedLateSessionOwnerCandidate(&late, late.OwnerCandidate) != nil || validateManagedLateLogCandidates(&late) != nil {
		return bad("exact-qualified-producers")
	}
	expected := map[string]string{
		"source": late.SourceSHA, "source-descriptor": managedLateSessionSourceDigest(&late), "original-enrollment-session": deploymentplan.Digest(late.OriginalEnrollmentSession), "original-key-seed-plan": late.Renewal.CandidateSHA256,
		"owners-plan": late.OwnerCandidate.SHA256(), "custody-plan": late.CustodyCandidate.SHA256(), "retained-trust": deploymentplan.Digest(late.TrustOwners), "retained-verifiers": deploymentplan.Digest(late.VerifierOwners),
		"log-producer": deploymentplan.Digest(late.LogProducer), "log-helper-binding": late.HelperBindingSHA, "log-stop-plan": late.LogStopCandidate.SHA256(), "log-helper-plan": late.LogHelperCandidate.SHA256(), "log-retire-plan": late.LogRetireCandidate.SHA256(), "log-restore-plan": late.LogRestoreCandidate.SHA256(),
		"native-late-seed-authorization": deploymentplan.Digest(late.SeedAuthorization), "native-late-cleanup-authorization": deploymentplan.Digest(late.CleanupAuthorization),
	}
	for key, value := range expected {
		if a.Plan.Materials["accepted-service-core-late-session-"+key] != value {
			return bad("qualified-" + key)
		}
	}
	if a.Plan.Materials["accepted-service-core-managed-complete-original-key"] != late.OriginalKeySHA || a.Plan.Materials["accepted-service-core-managed-complete-original-csr"] != late.OriginalCSRSHA || proof.Native["clients_sha256"] != late.OriginalClientsSHA || a.Plan.Materials["accepted-service-core-managed-application-current-clients"] != late.OriginalClientsSHA || proof.Native["servers_sha256"] != a.Plan.Materials["accepted-service-core-managed-application-current-servers"] || proof.Native["late_api_servers_sha256"] != a.Plan.Materials["accepted-service-core-managed-application-current-api-servers"] || proof.Native["late_ca_registry_sha256"] != a.Plan.Materials["accepted-service-core-managed-active-registry"] || proof.Native["late_all_volumes_sha256"] != a.Plan.Materials["accepted-service-core-managed-complete-current-native-volumes"] {
		return bad("unchanged-eleven-four-nine-eighteen")
	}
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	originalState, originalCeremony := late.OriginalSessionState, late.SourceCeremony
	if late.ExpiredRenewal != nil {
		originalState = late.ExpiredRenewal.PriorLate.OriginalSessionState
		originalCeremony = late.ExpiredRenewal.PriorLate.SourceCeremony
	}
	originalDescriptor, descriptorErr := managedCoreSessionObservation(originalState, originalState, originalCeremony, subjects, a.QualifiedAt, managedCoreRestoreOnly)
	if err != nil || descriptorErr != nil || deploymentplan.Digest(originalDescriptor) != a.Plan.Materials["accepted-service-core-managed-complete-current-session"] {
		return bad("retained-expired-eight-original-session")
	}
	volumePins, pinErr := managedLateOriginalVolumePins(a.Plan.Materials)
	if pinErr != nil || proof.Native["volume_sha256"] != deploymentplan.Digest(volumePins) {
		return bad("six-original-and-two-allocated-volume-pins")
	}
	seed, actualSeedEntry, _, err := readManagedLateEffectiveSeed(store, bounded, &late)
	seedEntry := &actualSeedEntry
	var seedPreview deploymentplan.Plan
	if err != nil || createOpenBaoACKCompletionRead(store, filepath.Join(base, "used-bootstrap-renewal-plan.json"), &seedPreview) != nil || seedPreview.SHA256() != late.Renewal.CandidateSHA256 || !managedLateSamePlan(seed, &seedPreview) && !managedLateEffectiveSeedIsForward(store, bounded, &late, seed) || len(seed.Resources) != 1 || seed.Resources[0].Action != "create" || seed.Resources[0].Desired["kind"] != "Job" || seed.Resources[0].UID != "" {
		return bad("real-native-new-authority")
	}
	custody, custodyEntry, err := readCreateOpenBaoACKCompletedStage(store, current.Journal, "service-custody-reader-renewal-"+review)
	if err != nil || !managedLateSamePlan(custody, late.CustodyCandidate) || len(custody.Resources) != 1 || custody.Resources[0].UID != late.Custody.UID {
		return bad("same-original-key-csr-custody")
	}
	credentials, credentialsEntry, err := readCreateOpenBaoACKCompletedStage(store, current.Journal, "service-credential-secrets-renewal-"+review)
	var observed createServiceSessionObserved
	path := filepath.Join("deployment/create", proof.Parent, "bootstrap/service/session-renewal", review, "observed.json")
	var credentialsJournal deploymentplan.Journal
	if err != nil || createOpenBaoACKCompletionRead(store, filepath.Join("deployment/rollback", credentials.SHA256(), "journal.json"), &credentialsJournal) != nil || !createOpenBaoACKCompletedChild(credentials, credentialsJournal) || createOpenBaoACKCompletionRead(store, path, &observed) != nil || validateManagedLateAllocatedCredentialProof(cfg, &late, credentials, credentialsJournal, observed, proof.CompletedAt) != nil {
		return bad("three-genuine-new-secrets")
	}
	cleanup, cleanupEntry, err := readCreateOpenBaoACKCompletedStage(store, current.Journal, "service-cleanup-binding-renewal-"+review)
	ceremony := late.SourceCeremony
	ceremony.BootstrapSessionID, ceremony.BootstrapCredentialSecret = late.NewSessionID, "service-bootstrap-"+late.NewSessionID
	ceremony.CleanupBinding, ceremony.CredentialUIDs = observed.Cleanup, observed.UIDs
	cleanupDefs, defErr := managedLateSessionCleanupDefinitions(&late, ceremony)
	if err != nil || defErr != nil || len(cleanup.Resources) != 5 || len(cleanupDefs) != 5 {
		return bad("real-cleanup-binding")
	}
	cleanupByID := map[string]deploymentplan.Definition{}
	for _, definition := range cleanupDefs {
		id := deploymentplan.ID(definition.Object)
		if _, duplicate := cleanupByID[id]; duplicate {
			return bad("duplicate-cleanup-definition")
		}
		cleanupByID[id] = definition
	}
	for _, resource := range cleanup.Resources {
		definition, ok := cleanupByID[resource.ID]
		if !ok || !deploymentplan.Matches(definition.Object, resource.Desired) {
			return bad("exact-cleanup-recipe")
		}
		delete(cleanupByID, resource.ID)
	}
	if len(cleanupByID) != 0 {
		return bad("missing-cleanup-owner")
	}
	effectiveOwnerCandidate, effectiveErr := managedLateEffectiveOwnerCandidate(store, bounded, &late)
	if effectiveErr != nil {
		return bad("qualified-effective-owner-plan")
	}
	owners, ownersEntry, err := readCreateOpenBaoACKCompletedStage(store, current.Journal, "managed-late-session-owners-"+review)
	if err != nil || !managedLateSamePlan(owners, effectiveOwnerCandidate) || proof.Native["late_owners_plan_sha256"] != owners.SHA256() {
		return bad("accepted-ten-original-native-owners")
	}
	effectiveOwnerSource := &late
	if _, post, frameErr := managedLatePostMintRuntimeFrame(store, bounded, &late); frameErr != nil {
		return bad("effective-owner-source-frame")
	} else if post != nil {
		effectiveOwnerSource, err = managedLatePostMintOwnerSource(&late, post)
		if err != nil {
			return bad("effective-owner-cleanup-dependencies")
		}
	}
	updated, retainedLog := 0, 0
	for _, resource := range owners.Resources {
		original, ok := late.Owners[resource.ID]
		if !ok {
			dependency, found := effectiveOwnerSource.DependencyOwners[resource.ID]
			if !found || resource.Action != "retain" || resource.UID != dependency.UID || deploymentplan.Digest(resource.Desired) != deploymentplan.Digest(dependency.Desired) {
				return bad("foreign-owner-dependency")
			}
			continue
		}
		if resource.UID != original.UID {
			return bad("owner-replacement")
		}
		if resource.ID == "Deployment/"+proof.Stack+"-video-cloud/video-cloud-logingester" {
			if resource.Action != "retain" || deploymentplan.Digest(resource.Desired) != deploymentplan.Digest(original.Desired) || mqttFoundationEnv(managedNamedField(environmentPKIPod(resource.Desired)["containers"], "app"), "PKI_BOOTSTRAP_SESSION_ID") != managedLateOriginalWorkloadSession(&late) {
				return bad("original-log-retained-until-exclusive-stop")
			}
			retainedLog++
		} else {
			if resource.Action != "update" {
				return bad("nine-native-owner-SID-updates")
			}
			updated++
		}
	}
	if updated != 9 || retainedLog != 1 {
		return bad("exact-nine-updates-and-original-log-retain")
	}
	restoredLog, _, restoreErr := readCreateOpenBaoACKCompletedStage(store, current.Journal, "managed-late-log-restore-"+review)
	if restoreErr != nil || !managedLateSamePlan(restoredLog, late.LogRestoreCandidate) || len(restoredLog.Resources) != 1 || restoredLog.Resources[0].Action != "update" || restoredLog.Resources[0].UID != late.LogProducer.Original.UID || mqttFoundationEnv(managedNamedField(environmentPKIPod(restoredLog.Resources[0].Desired)["containers"], "app"), "PKI_BOOTSTRAP_SESSION_ID") != late.NewSessionID {
		return bad("sole-real-accepted-log-SID-update")
	}
	log, err := readManagedLateLogCompletion(store, &current, &late, proof.CompletedAt)
	if err != nil || log == nil || proof.Native["late_helper_proof_sha256"] != deploymentplan.Digest(log) || !managedLateLogProofSessionHashMatches(store, log, deploymentplan.String(proof.Native["session_sha256"])) || proof.Native["clients_sha256"] != log.NativeClientsSHA {
		return bad("genuine-exclusive-log-and-native-nine")
	}
	if !managedLateSeedAuthorizationCompleted(current.Journal, &late) {
		return bad("actual-exclusive-new-signer-role")
	}
	seedAuth := createOpenBaoACKCompletionEntry(current.Journal, "late-seed-authorization-"+review)
	cleanupAuth := createOpenBaoACKCompletionEntry(current.Journal, "late-cleanup-authorization-"+review)
	if !managedLateCompletionRecipeEntry(cleanupAuth, late.CleanupAuthorization) || !managedLateCompletionRecipeEntry(seedAuth, late.SeedAuthorization) {
		return bad("genuine-native-wrapper-receipts")
	}
	chronology := []managedLateCompletionTime{{"seed-authorization", seedAuth.StartedAt, seedAuth.CompletedAt}, {"seed", seedEntry.StartedAt, seedEntry.CompletedAt}, {"custody", custodyEntry.StartedAt, custodyEntry.CompletedAt}, {"credentials", credentialsEntry.StartedAt, credentialsEntry.CompletedAt}, {"cleanup", cleanupEntry.StartedAt, cleanupEntry.CompletedAt}, {"cleanup-authorization", cleanupAuth.StartedAt, cleanupAuth.CompletedAt}, {"owners", ownersEntry.StartedAt, ownersEntry.CompletedAt}, {"exclusive-log", log.StoppedAt, log.RestoredAt}}
	if validateManagedLateCompletionTimes(chronology, a.QualifiedAt, proof.CompletedAt) != nil {
		return bad("real-stage-temporal-order")
	}
	return nil
}

func managedLateCompletionRecipeEntry(entry *environmentCreateJournalEntry, recipe deploymentplan.Object) bool {
	return entry != nil && entry.Phase == "late-native-authorization" && entry.State == "completed" && !entry.StartedAt.IsZero() && !entry.CompletedAt.Before(entry.StartedAt) && entry.IntentSHA256 == activeTransportJournalDigest(recipe) && activeTransportJournalDigest(jsonRawObject(entry.Receipt)) == activeTransportJournalDigest(recipe)
}

func validateManagedLateAllocatedCredentialProof(cfg deploymentConfig, late *createManagedLateSessionRecovery, plan *deploymentplan.Plan, journal deploymentplan.Journal, observed createServiceSessionObserved, at time.Time) error {
	bad := func() error { return createOpenBaoACKBlock("completed-late-native-credential") }
	if late == nil || plan == nil || len(plan.Resources) != 3 || len(late.SourceCeremony.BootstrapCredentialNamespaces) != 3 || len(observed.UIDs) != 3 || observed.Reader != deploymentplan.Map(late.Custody.Desired["metadata"])["name"] || observed.Cleanup.SessionID != late.NewSessionID || observed.Cleanup.NamespaceUID != late.SourceCeremony.CleanupBinding.NamespaceUID || observed.Cleanup.KubernetesCA != late.SourceCeremony.CleanupBinding.KubernetesCA || observed.Cleanup.PolicySHA256 != late.SignerPolicySHA || observed.Cleanup.SecretUID != observed.UIDs[late.SourceCeremony.BootstrapCredentialNamespace] || validateEnvironmentBootstrapCleanupBinding(&observed.Cleanup, cfg.Environment) != nil {
		return bad()
	}
	var state managedSessionState
	if decodeEnvironmentJSON(observed.Session, &state) != nil || state.Session != late.NewSessionID || state.Issuer != late.SourceCeremony.Intermediate.ID || state.Root != late.SourceCeremony.Root.RootFingerprint || state.Status != "active" || state.OtherActive != 0 || !state.Expires.After(at) || len(state.Installed) != 0 || !slices.Equal(state.Subjects, late.SourceRenewedIntent.Subjects) {
		return bad()
	}
	seen := map[string]bool{}
	materials := map[string]string{}
	for _, resource := range plan.Resources {
		meta := deploymentplan.Map(resource.Desired["metadata"])
		ns := deploymentplan.String(meta["namespace"])
		if resource.Action != "create" || resource.UID != "" || resource.Desired["kind"] != "Secret" || resource.Desired["immutable"] != true || meta["name"] != "service-bootstrap-"+late.NewSessionID || !slices.Contains(late.SourceCeremony.BootstrapCredentialNamespaces, ns) || seen[ns] || !environmentBootstrapOwnerUUID.MatchString(observed.UIDs[ns]) || journal.Owners[resource.ID] != observed.UIDs[ns] {
			return bad()
		}
		seen[ns] = true
		values := map[string]string{}
		data := deploymentplan.Map(resource.Desired["data"])
		if len(data) != 3 {
			return bad()
		}
		for _, key := range []string{"tls.crt", "tls.key", "ca.crt"} {
			raw, err := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(data[key]))
			if err != nil || len(raw) == 0 {
				return bad()
			}
			values[key] = string(raw)
		}
		if len(materials) != 0 && deploymentplan.Digest(materials) != deploymentplan.Digest(values) {
			return bad()
		}
		materials = values
	}
	if len(seen) != 3 || environmentPreflightBytesSHA256([]byte(materials["tls.key"])) != late.OriginalKeySHA || materials["ca.crt"] != late.SourceCeremony.Root.Certificate {
		return bad()
	}
	pair, err := tls.X509KeyPair([]byte(materials["tls.crt"]), []byte(materials["tls.key"]))
	if err != nil || !managedLateBootstrapChainMatches(pair.Certificate, late.SourceCeremony) {
		return bad()
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	issuer, issuerErr := parseFirstCertificatePEM([]byte(late.SourceCeremony.Intermediate.Certificate))
	if err != nil || issuerErr != nil || leaf.Subject.CommonName != "service:deployment-bootstrap" || leaf.CheckSignatureFrom(issuer) != nil || leaf.IsCA || leaf.NotBefore.After(at) || !leaf.NotAfter.Equal(state.Expires) || environmentPreflightBytesSHA256(leaf.Raw) != state.Fingerprint || environmentPreflightBytesSHA256(pair.Certificate[len(pair.Certificate)-1]) != late.SourceCeremony.Root.RootFingerprint {
		return bad()
	}
	return nil
}
