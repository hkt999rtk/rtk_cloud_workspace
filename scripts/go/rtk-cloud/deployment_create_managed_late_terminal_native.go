package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type managedLateTerminalRevocation struct {
	Session, Issuer, Fingerprint, Status, Certificate string
	SealedAt, RevokedAt                               time.Time
	FinalizedAt                                       *time.Time
	IssuanceCount, RevocationCount                    int
	Reason                                            string
}

// Only the read-only components of CurrentNative apply after the genuine
// finalizer sealed and revoked the seed and retired its two secondary copies.
// The actual journal remains attached to every native getter.
func verifyManagedLateTerminalFinalizerNative(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan) error {
	bad := func(stage string) error { return createOpenBaoACKBlock("terminal-finalizer-native-" + stage) }
	if ctx == nil || e == nil || r == nil || p == nil || p.Materials == nil || r.ManagedLateSession == nil || r.ManagedLateRemaining == nil || r.ManagedLateRemaining.TerminalFinalizerCompletion == nil || store.Root != e.Store.Root || e.Check(ctx) != nil {
		return bad("profile")
	}
	late, remaining := r.ManagedLateSession, r.ManagedLateRemaining
	terminal := remaining.TerminalFinalizerCompletion
	post := remaining.PostNativeCleanup
	residual := remaining.ResidualCleanup
	if r.completedTail != nil {
		if err := verifyManagedLateCompletedTailSource(ctx, cfg, store, e, r); err != nil {
			return err
		}
	} else if residual != nil {
		if err := verifyManagedLateResidualCleanupSource(ctx, cfg, store, e, r); err != nil {
			return err
		}
	} else if post == nil {
		if !managedLateTerminalFinalizerPendingIntent(e.Journal, r.Ceremony) || terminal.SourceSHA != deploymentplan.Digest(e.Journal) {
			return bad("profile")
		}
	} else if err := verifyManagedLatePostNativeCleanupSource(ctx, cfg, store, e, r); err != nil {
		return err
	}
	if r.Ceremony.BootstrapSessionID != late.NewSessionID || remaining.SessionID != late.NewSessionID || remaining.FinalizationAuthority == nil {
		return bad("source")
	}
	var bounded *environmentCreateExecution
	var admitted *createFoundationRecoveryArtifact
	var err error
	if r.completedTail != nil {
		bounded = r.completedTail.view
		var original createFoundationRecoveryArtifact
		original, _, err = loadAdmittedCreateRecovery(store, bounded, late.Review)
		admitted = &original
	} else {
		bounded, admitted, err = managedLateVerifiedProgressPrefix(store, e, late)
	}
	if err != nil || bounded == nil || admitted == nil || len(bounded.Journal.Entries) != 392 || terminal.PrefixSHA != deploymentplan.Digest(bounded.Journal) {
		return bad("accepted-progress")
	}
	if r.normalTail != nil && r.normalTail.KubeconfigSHA != admitted.Plan.KubeconfigSHA {
		return bad("original-kubeconfig")
	}
	// Rebuild the finite original material set independently; a freshly
	// qualified expected map is never copied in as observed native evidence.
	p.Materials = map[string]string{}
	p.Plan = admitted.Plan.Plan
	p.Schema = managedLateExpiredRenewalSchema
	for name, value := range admitted.Plan.Materials {
		p.Materials[name] = value
	}
	children, captured, _, err := managedLateCurrentChildren(store, bounded, late)
	if err != nil {
		return err
	}
	sourcePlan := createFoundationRecoveryPlan{Plan: admitted.Plan.Plan, Materials: map[string]string{}}
	source, loaded, err := loadManagedLateEffectiveSource(cfg, store, captured, &sourcePlan, late)
	if err != nil || loaded == nil || loaded.SourceSHA != late.SourceSHA {
		return bad("original-source")
	}
	source.ManagedLateSession = late
	owners, ready, err := managedLateCurrentOwners(source, late, children)
	if err != nil {
		return err
	}
	if r.normalTail != nil {
		if err := projectManagedLateNormalTailOwners(store, e, r, owners, ready); err != nil {
			return err
		}
	}
	objects, err := b.Inventory(ctx, e.Journal.Stack)
	if err != nil {
		return bad("owner-inventory")
	}
	if residual != nil {
		// These exact frozen credentials just authenticated the current API.
		// Their lifetime, rather than the destroyed seed's, caps cleanup.
		if err := bindManagedLateResidualCredential(residual, b, time.Now().UTC()); err != nil {
			return err
		}
		if r.normalTail != nil {
			if len(r.normalTail.NativeEvidence) != 0 && (r.normalTail.CredentialMode != residual.CredentialMode || r.normalTail.CredentialSHA != residual.CredentialSHA || !r.normalTail.CredentialExpires.Equal(residual.CredentialExpires)) {
				return bad("normal-tail-current-credential-drift")
			}
			r.normalTail.CredentialMode, r.normalTail.CredentialSHA, r.normalTail.CredentialExpires = residual.CredentialMode, residual.CredentialSHA, residual.CredentialExpires
		}
	}
	ownerProof, err := verifyManagedLateCurrentOwnerInventory(ctx, store, late.AcceptedCore.Inputs, owners, ready, objects)
	if err != nil {
		return err
	}
	if err := verifyManagedApplicationCurrentVolumes(ctx, cfg, e, b, source, p); err != nil {
		return err
	}
	if err := verifyManagedCoreAllVolumes(ctx, e, b, p.Materials); err != nil {
		return err
	}
	if p.Materials["accepted-service-core-managed-complete-current-native-volumes"] != admitted.Plan.Materials["accepted-service-core-managed-complete-current-native-volumes"] {
		return bad("eighteen-volumes")
	}
	if err := verifyManagedApplicationCurrentExports(ctx, e, b, source.ManagedApplicationPartial, p.Materials); err != nil {
		return err
	}
	if err := verifyManagedApplicationCurrentAPIServers(ctx, cfg, e, b, source, p.Materials, r); err != nil {
		return err
	}
	if err := bindManagedLateFreshAPIServers(p.Materials, admitted.Plan.Materials[managedLateAPICurrentMaterial]); err != nil {
		return err
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: r}
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil || len(subjects) != 9 {
		return bad("nine-subjects")
	}
	clients, err := readManagedSessionClients(ctx, cfg, runtime, late.SourceCeremony, subjects)
	if err != nil || clients != late.OriginalClientsSHA || clients != admitted.Plan.Materials["accepted-service-core-managed-application-current-clients"] {
		return bad("eleven-clients")
	}
	roots := map[string]environmentCreateRegisteredIssuer{"mqtt": *source.MQTTRoot, "openbao_tls": *source.OpenBaoRoot}
	icas := map[string]environmentCreateRegisteredIssuer{"mqtt": *source.MQTTIntermediate, "openbao_tls": *source.OpenBaoIntermediate}
	hosts, err := readManagedTransportServers(ctx, cfg, runtime, admitted.Plan.Plan.Bootstrap, roots, icas)
	if err != nil || hosts != source.ManagedTransportRecovery.Partial.ServerSHA || hosts != admitted.Plan.Materials["accepted-service-core-managed-application-current-servers"] {
		return bad("original-host-servers")
	}
	if err := verifyManagedTransportNativeRegistry(ctx, cfg, store, e, b, p, r); err != nil {
		return err
	}
	if p.Materials["accepted-service-core-managed-active-registry"] != admitted.Plan.Materials["accepted-service-core-managed-active-registry"] {
		return bad("nine-ca-pins")
	}
	var seed createManagedLateSeedObservation
	if createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create/openbao-recovery", late.Review, "managed-late-seed-observed.json"), &seed) != nil || seed.KeySHA != late.OriginalKeySHA || seed.CSRSHA != late.OriginalCSRSHA {
		return bad("original-seed")
	}
	if post == nil {
		key, err := b.command(ctx, nil, "--namespace", r.Ceremony.BootstrapCredentialNamespace, "exec", "deployment/"+r.Ceremony.CustodyReader, "--container", "custody", "--", "cat", "/run/pki-bootstrap/bootstrap.key")
		if err != nil || environmentPreflightBytesSHA256(key) != late.OriginalKeySHA {
			return bad("original-key")
		}
		csr, err := b.command(ctx, nil, "--namespace", r.Ceremony.BootstrapCredentialNamespace, "exec", "deployment/"+r.Ceremony.CustodyReader, "--container", "custody", "--", "cat", "/run/pki-bootstrap/bootstrap.csr")
		if err != nil || environmentPreflightBytesSHA256(csr) != late.OriginalCSRSHA {
			return bad("original-csr")
		}
		certificate, err := b.command(ctx, nil, "--namespace", r.Ceremony.BootstrapCredentialNamespace, "exec", "deployment/"+r.Ceremony.CustodyReader, "--container", "custody", "--", "cat", late.NewCertificatePath)
		pair, pairErr := tls.X509KeyPair(certificate, key)
		if err != nil || pairErr != nil || !managedLateBootstrapChainMatches(pair.Certificate, late.SourceCeremony) || seed.CertificatePEM != string(certificate) {
			return bad("original-seed")
		}
	}
	state, stateRaw, err := readManagedSessionState(ctx, cfg, runtime, late.NewSessionID)
	if err != nil || !managedLateTerminalSessionForRecovery(state, seed, r, subjects, time.Now().UTC()) {
		return bad("sealed-nine")
	}
	if post != nil {
		if err := observeManagedLatePostNativeFinalization(ctx, cfg, runtime, seed, state, subjects); err != nil {
			return err
		}
	}
	old, _, err := readManagedSessionState(ctx, cfg, runtime, late.OriginalSession)
	if err != nil || !managedLateOriginalSessionForScope(old, late, false, subjects, time.Now().UTC()) {
		return bad("original-expired-session")
	}
	if f := late.ExpiredRenewal; f != nil {
		ancestor, _, err := readManagedSessionState(ctx, cfg, runtime, f.PriorLate.OriginalSession)
		if err != nil || !managedLateOriginalSessionCurrent(ancestor, f.PriorLate.OriginalSessionState, false, subjects, time.Now().UTC()) {
			return bad("retained-e0")
		}
	}
	copyProof := map[string]string{}
	credential, ok := children["kubernetes/service-credential-secrets-renewal-"+late.Review]
	if !ok || !createOpenBaoACKCompletedChild(credential.Plan, credential.Journal) {
		return bad("allocated-copies")
	}
	for _, resource := range credential.Plan.Resources {
		namespace := deploymentplan.String(deploymentplan.Map(resource.Desired["metadata"])["namespace"])
		if post != nil {
			actual, err := b.Read(ctx, resource.ID)
			if !environmentBootstrapOwnerUUID.MatchString(credential.Journal.Owners[resource.ID]) || err != nil || actual != nil {
				return bad("retired-native-copy")
			}
			copyProof[resource.ID] = deploymentplan.Digest(deploymentplan.Object{"uid": credential.Journal.Owners[resource.ID], "absent": true})
			continue
		}
		if namespace != r.Ceremony.BootstrapCredentialNamespace {
			completed, err := managedLateCompletedCredentialRetirement(ctx, cfg, runtime, r.Ceremony, namespace)
			if err != nil || !completed {
				return bad("retired-secondary-copy")
			}
			copyProof[resource.ID] = deploymentplan.Digest(deploymentplan.Object{"uid": credential.Journal.Owners[resource.ID], "absent": true})
			continue
		}
		actual, err := b.Read(ctx, resource.ID)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != credential.Journal.Owners[resource.ID] || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(resource.Desired, actual) {
			return bad("primary-copy")
		}
		copyProof[resource.ID] = deploymentplan.Digest(deploymentplan.Projection(actual))
	}
	if len(copyProof) != 3 {
		return bad("three-copy-lineage")
	}
	records, crlProof, err := readManagedLateRegistryCRLs(ctx, cfg, e, b, source, managedLateObserveACKReady)
	if err != nil {
		return err
	}
	if err := verifyManagedLateCurrentCRLConsumers(ctx, cfg, store, e, b, late, owners, ready); err != nil {
		return err
	}
	record := records[r.Ceremony.Intermediate.ID]
	revocation, revocationRaw, err := readManagedLateTerminalRevocation(ctx, cfg, runtime, state)
	if err != nil || !managedLateTerminalRevocationForRecovery(revocation, state, seed.CertificatePEM, r) || !managedLateTerminalRevocationWithinAuthority(revocation, remaining.FinalizationAuthority) || !managedLateTerminalCRLCovers(record, state.Issuer, seed.CertificatePEM, revocation.RevokedAt, remaining.FinalizationAuthority.Deadline) {
		return bad("native-revocation-and-current-crl")
	}
	query := "SELECT COALESCE(json_agg(consumer_id ORDER BY consumer_id),'[]'::json) FROM pki_crl_acknowledgments WHERE issuer_id='" + state.Issuer + "' AND digest='" + record.Digest + "'"
	ackRaw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var acks []string
	wanted, wantedErr := managedTransportServiceConsumerIDs(findEnvironmentCreateDomainPolicy(admitted.Plan.Plan.Bootstrap, "service").Consumers)
	if err != nil || wantedErr != nil || decodeEnvironmentJSON(ackRaw, &acks) != nil || len(wanted) != 9 || !slices.Equal(acks, wanted) {
		return bad("current-nine-crl-acks")
	}
	// Fail closed if sealing/revocation/publication changed during this boundary.
	last, lastRaw, err := readManagedSessionState(ctx, cfg, runtime, late.NewSessionID)
	lastRevocation, lastRevocationRaw, revokeErr := readManagedLateTerminalRevocation(ctx, cfg, runtime, state)
	latest, _, crlErr := readManagedLateRegistryCRLs(ctx, cfg, e, b, source, managedLateObserveACKReady)
	if err != nil || revokeErr != nil || crlErr != nil || deploymentplan.Digest(last) != deploymentplan.Digest(state) || environmentPreflightBytesSHA256(lastRaw) != environmentPreflightBytesSHA256(stateRaw) || deploymentplan.Digest(lastRevocation) != deploymentplan.Digest(revocation) || environmentPreflightBytesSHA256(lastRevocationRaw) != environmentPreflightBytesSHA256(revocationRaw) || deploymentplan.Digest(latest) != deploymentplan.Digest(records) {
		return bad("stable-native-outcome")
	}
	evidence := map[string]string{"owners": deploymentplan.Digest(ownerProof), "all-volumes": p.Materials["accepted-service-core-managed-complete-current-native-volumes"], "clients": clients, "host-servers": hosts, "api-servers": p.Materials[managedLateAPICurrentMaterial], "ca-registry": p.Materials["accepted-service-core-managed-active-registry"], "current-crls": deploymentplan.Digest(crlProof), "sealed-session": environmentPreflightBytesSHA256(stateRaw), "revocation": environmentPreflightBytesSHA256(revocationRaw), "service-crl": deploymentplan.Digest(record), "service-crl-acks": deploymentplan.Digest(acks), "copies": deploymentplan.Digest(copyProof), "key": late.OriginalKeySHA, "csr": late.OriginalCSRSHA}
	expectedEvidence := terminal.NativeEvidence
	if post != nil {
		expectedEvidence = post.NativeEvidence
	}
	materialPrefix := "terminal-native-"
	if residual != nil {
		evidence["current-credential"] = managedLateResidualCredentialSHA(residual)
		expectedEvidence = residual.NativeEvidence
		materialPrefix = "residual-native-"
	}
	if remaining.ResidualRemaining != nil {
		expectedEvidence = remaining.ResidualRemaining.NativeEvidence
		materialPrefix = "residual-remaining-native-"
	}
	if r.normalTail != nil {
		expectedEvidence = r.normalTail.NativeEvidence
	}
	if r.completedTail != nil {
		expectedEvidence = r.completedTail.NativeEvidence
		materialPrefix = "completed-normal-tail-native-"
	}
	if len(expectedEvidence) != 0 && deploymentplan.Digest(expectedEvidence) != deploymentplan.Digest(evidence) {
		return bad("qualified-native-evidence")
	}
	for name, value := range evidence {
		if !environmentPreflightDigest.MatchString(value) {
			return bad("evidence-hash")
		}
		p.Materials[managedLateRemainingMaterial+materialPrefix+name] = value
	}
	if r.completedTail != nil {
		r.completedTail.NativeEvidence = evidence
		if r.normalTail != nil {
			r.normalTail.NativeEvidence = evidence
		}
	} else if remaining.ResidualRemaining != nil {
		remaining.ResidualRemaining.NativeEvidence = evidence
	} else if residual != nil {
		residual.NativeEvidence = evidence
	} else if post == nil {
		terminal.NativeEvidence = evidence
	} else {
		post.NativeEvidence = evidence
	}
	if err := bindManagedLateRemainingHistoricalCleanupMaterial(r, p.Materials); err != nil {
		return err
	}
	qualifiedPins := managedLateQualifiedMaterialPins(late)
	if qualifiedPins == nil {
		return bad("qualified-producers")
	}
	for name, value := range qualifiedPins {
		p.Materials["accepted-service-core-late-session-"+name] = value
	}
	for name, value := range managedLateRemainingPins(remaining) {
		p.Materials[managedLateRemainingMaterial+name] = value
	}
	return e.Check(ctx)
}

func observeManagedLatePostNativeFinalization(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, seed createManagedLateSeedObservation, state managedSessionState, subjects []string) error {
	r := runtime.ServiceSessionRecovery
	if r == nil || r.ManagedLateSession == nil || r.ManagedLateRemaining == nil || r.ManagedLateRemaining.PostNativeCleanup == nil || r.ManagedLateRemaining.TerminalAuthority == nil || !managedLatePostNativePublicSeedMatches(seed, r.ManagedLateSession) {
		return createOpenBaoACKBlock("post-native-cleanup-original-seed")
	}
	remaining, late := r.ManagedLateRemaining, r.ManagedLateSession
	post := remaining.PostNativeCleanup
	query := "SELECT json_build_object('environment',environment,'session_id',session_id,'bootstrap_leaf_sha256',bootstrap_fingerprint,'crl_sha256',finalized_crl_sha256,'cleanup_sha256',cleanup_sha256,'finalized_at',finalized_at) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND session_id='" + late.NewSessionID + "' AND finalized_at IS NOT NULL AND status IN ('sealed','expired')"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var native environmentCreateBootstrapFinalization
	if err != nil || decodeEnvironmentJSON(raw, &native) != nil || native.FinalizedAt.Before(remaining.TerminalAuthority.StartedAt) || native.FinalizedAt.After(remaining.TerminalAuthority.Deadline) {
		return createOpenBaoACKBlock("post-native-cleanup-finalized-row")
	}
	if remaining.ResidualCleanup != nil && !managedLateResidualFinalizationBeforeExpiry(r, seed, state, native, time.Now().UTC()) {
		return createOpenBaoACKBlock("residual-cleanup-historical-finalization")
	}
	if r.normalTail != nil {
		return verifyManagedLateNormalTailFinalizedCustody(ctx, cfg, runtime, seed, state, subjects, native)
	}
	marker, err := runtime.Backend.command(ctx, nil, "--namespace", r.Ceremony.BootstrapCredentialNamespace, "exec", "deployment/"+r.Ceremony.CustodyReader, "--container", "custody", "--", "cat", "/run/pki-bootstrap/bootstrap.finalized.json")
	var tombstone managedLateCustodyTombstone
	if err != nil || decodeEnvironmentJSON(marker, &tombstone) != nil || !managedLateFinalizedSessionCurrent(state, seed, late, subjects, native, tombstone, time.Now().UTC()) || post.Native != (environmentCreateBootstrapFinalization{}) && !managedLateFinalizationEqual(post.Native, native) || post.TombstoneSHA != "" && post.TombstoneSHA != environmentPreflightBytesSHA256(marker) {
		return createOpenBaoACKBlock("post-native-cleanup-finalized-tombstone")
	}
	if _, err := runtime.Backend.command(ctx, nil, "--namespace", r.Ceremony.BootstrapCredentialNamespace, "exec", "deployment/"+r.Ceremony.CustodyReader, "--container", "custody", "--", "/bin/sh", "-ec", `test ! -e /run/pki-bootstrap/bootstrap.key && test ! -L /run/pki-bootstrap/bootstrap.key && test ! -e /run/pki-bootstrap/bootstrap.csr && test ! -L /run/pki-bootstrap/bootstrap.csr && test ! -e /run/pki-bootstrap/bootstrap.crt && test ! -L /run/pki-bootstrap/bootstrap.crt`); err != nil {
		return createOpenBaoACKBlock("post-native-cleanup-original-files-absent")
	}
	post.Native, post.TombstoneSHA = native, environmentPreflightBytesSHA256(marker)
	return runtime.Execution.Check(ctx)
}

func managedLatePostNativePublicSeedMatches(seed createManagedLateSeedObservation, late *createManagedLateSessionRecovery) bool {
	if late == nil || seed.Schema != managedLateSessionSchema || seed.Parent != late.ParentSHA || seed.Review != late.Review || seed.KeySHA != late.OriginalKeySHA || seed.CSRSHA != late.OriginalCSRSHA || len(seed.CertificatePEM) > 256<<10 {
		return false
	}
	var chain [][]byte
	rest := []byte(seed.CertificatePEM)
	for len(strings.TrimSpace(string(rest))) != 0 {
		if !strings.HasPrefix(strings.TrimSpace(string(rest)), "-----BEGIN CERTIFICATE-----") {
			return false
		}
		block, tail := pem.Decode(rest)
		if block == nil || block.Type != "CERTIFICATE" || len(block.Headers) != 0 || len(chain) == 4 {
			return false
		}
		chain = append(chain, block.Bytes)
		rest = tail
	}
	if !managedLateBootstrapChainMatches(chain, late.SourceCeremony) {
		return false
	}
	leaf, err := x509.ParseCertificate(chain[0])
	return err == nil && seed.Session.Session == late.NewSessionID && seed.Session.Issuer == late.SourceCeremony.Intermediate.ID && seed.Session.Root == late.SourceCeremony.Root.RootFingerprint && seed.Session.Fingerprint == environmentPreflightBytesSHA256(leaf.Raw) && seed.Session.Expires.Equal(leaf.NotAfter)
}

func managedLateTerminalSealedSession(current, seed managedSessionState, ceremony environmentCreateServiceCeremony, subjects []string, now time.Time) bool {
	installed := slices.Clone(current.Installed)
	slices.Sort(installed)
	return current.Status == "sealed" && current.Session == ceremony.BootstrapSessionID && current.Session == seed.Session && current.Issuer == ceremony.Intermediate.ID && current.Issuer == seed.Issuer && current.Root == ceremony.Root.RootFingerprint && current.Root == seed.Root && current.Fingerprint == seed.Fingerprint && environmentPreflightDigest.MatchString(current.Fingerprint) && current.OtherActive == 0 && current.Expires.Equal(seed.Expires) && current.Expires.After(now.Add(managedLateFinalizerLimit+managedLateExpiryReserve)) && len(subjects) == 9 && slices.Equal(current.Subjects, subjects) && len(installed) == 9 && slices.Equal(installed, subjects)
}

func readManagedLateTerminalRevocation(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, state managedSessionState) (managedLateTerminalRevocation, []byte, error) {
	var result managedLateTerminalRevocation
	if !environmentBootstrapOwnerUUID.MatchString(state.Session) || !environmentBootstrapOwnerUUID.MatchString(state.Issuer) || !environmentPreflightDigest.MatchString(state.Fingerprint) {
		return result, nil, createOpenBaoACKBlock("terminal-finalizer-native-revocation-query")
	}
	query := "SELECT json_build_object('Session',b.session_id,'Issuer',b.issuer_id,'Fingerprint',b.bootstrap_fingerprint,'Status',b.status,'SealedAt',b.sealed_at,'FinalizedAt',b.finalized_at,'Certificate',(SELECT certificate_pem FROM pki_service_client_issuances WHERE issuer_id=b.issuer_id AND fingerprint=b.bootstrap_fingerprint),'RevokedAt',(SELECT revoked_at FROM pki_service_client_issuances WHERE issuer_id=b.issuer_id AND fingerprint=b.bootstrap_fingerprint),'IssuanceCount',(SELECT count(*) FROM pki_service_client_issuances WHERE issuer_id=b.issuer_id AND fingerprint=b.bootstrap_fingerprint AND status='succeeded'),'RevocationCount',(SELECT count(*) FROM pki_service_client_revocations WHERE issuer_id=b.issuer_id AND fingerprint=b.bootstrap_fingerprint),'Reason',(SELECT reason FROM pki_service_client_revocations WHERE issuer_id=b.issuer_id AND fingerprint=b.bootstrap_fingerprint)) FROM pki_deployment_bootstrap_sessions b WHERE b.environment='" + cfg.Environment + "' AND b.session_id='" + state.Session + "'"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	if err != nil || decodeEnvironmentJSON(raw, &result) != nil {
		return result, nil, createOpenBaoACKBlock("terminal-finalizer-native-revocation-read")
	}
	return result, raw, nil
}

func managedLateTerminalRevocationMatches(observed managedLateTerminalRevocation, state managedSessionState, certificate string) bool {
	leaf, err := parseFirstCertificatePEM([]byte(certificate))
	stored, storedErr := parseFirstCertificatePEM([]byte(observed.Certificate))
	return err == nil && storedErr == nil && leaf.Equal(stored) && environmentPreflightBytesSHA256(leaf.Raw) == state.Fingerprint && observed.Session == state.Session && observed.Issuer == state.Issuer && observed.Fingerprint == state.Fingerprint && observed.Status == "sealed" && observed.FinalizedAt == nil && !observed.SealedAt.IsZero() && !observed.RevokedAt.IsZero() && observed.SealedAt.Equal(observed.RevokedAt) && observed.IssuanceCount == 1 && observed.RevocationCount == 1 && observed.Reason == "deployment bootstrap finalized"
}

func managedLateTerminalRevocationForScope(observed managedLateTerminalRevocation, state managedSessionState, certificate string, post *createManagedLatePostNativeCleanup) bool {
	if post == nil {
		return managedLateTerminalRevocationMatches(observed, state, certificate)
	}
	if observed.FinalizedAt == nil || !observed.FinalizedAt.Equal(post.Native.FinalizedAt) || post.Native.SessionID != state.Session || post.Native.LeafSHA256 != state.Fingerprint {
		return false
	}
	// Preserve every original seal/revocation/leaf predicate. Only the already
	// proved finalization timestamp replaces the former finalized_at IS NULL.
	copy := observed
	copy.FinalizedAt = nil
	return managedLateTerminalRevocationMatches(copy, state, certificate)
}

func managedLateTerminalRevocationWithinAuthority(observed managedLateTerminalRevocation, original *createManagedLateFinalizationAuthority) bool {
	return original != nil && !original.StartedAt.IsZero() && original.Deadline.After(original.StartedAt) && !observed.SealedAt.Before(original.StartedAt) && !observed.RevokedAt.Before(original.StartedAt) && !observed.SealedAt.After(original.Deadline) && !observed.RevokedAt.After(original.Deadline)
}

func managedLateTerminalCRLCovers(record managedLateCRLRecord, issuer, certificate string, revokedAt, originalDeadline time.Time) bool {
	leaf, err := parseFirstCertificatePEM([]byte(certificate))
	block, _ := pem.Decode([]byte(record.PEM))
	if err != nil || block == nil || record.IssuerID != issuer || originalDeadline.IsZero() {
		return false
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil || record.Digest != environmentPreflightBytesSHA256(crl.Raw) || crl.ThisUpdate.Before(revokedAt.UTC().Truncate(time.Second)) {
		return false
	}
	for _, entry := range crl.RevokedCertificateEntries {
		if entry.SerialNumber.Cmp(leaf.SerialNumber) == 0 {
			// The registry records denial at request time; the provider records
			// its later revoke operation with whole-second CRL precision. Keep
			// both causal bounds instead of requiring those distinct events equal.
			return !entry.RevocationTime.Before(revokedAt.UTC().Truncate(time.Second)) && !entry.RevocationTime.After(crl.ThisUpdate) && !entry.RevocationTime.After(originalDeadline)
		}
	}
	return false
}
