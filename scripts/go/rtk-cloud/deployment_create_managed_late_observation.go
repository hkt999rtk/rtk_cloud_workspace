// Authenticate accepted source ownership and native identity continuity before
// a scoped original-root CRL repair or distinct-session ACK continuation.
package main

import (
	"context"
	"crypto/x509"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func verifyManagedLateSessionObservation(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan, late *createManagedLateSessionRecovery) error {
	return verifyManagedLateSessionObservationWithMode(ctx, cfg, store, e, b, r, p, late, managedLateSessionACKReady)
}

func authenticateManagedLateObservationSource(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan, late *createManagedLateSessionRecovery, mode managedLateSessionMode) (*createServiceSessionRecovery, error) {
	if e == nil || r == nil || p == nil || late == nil || late.Mode != mode || store.Root != e.Store.Root || !environmentPreflightDigest.MatchString(late.SourceArtifactSHA) {
		return nil, managedCoreObservationBlock("late-exact-source")
	}
	// Source is either the exact current journal or one explicitly authenticated
	// in-progress MQTT artifact. Native getters always retain the actual live e.
	pureSource := e
	if late.SourceSHA != deploymentplan.Digest(e.Journal) {
		if mode == managedLateSessionACKReady {
			captured, admitted, err := managedLateSessionRuntimeProgress(store, e, late)
			if err != nil || admitted == nil || !managedLateSchemaMatches(admitted.Plan.Schema, late) || admitted.Plan.ParentJournalSHA != late.SourceSHA {
				return nil, managedCoreObservationBlock("late-authenticated-runtime-progress")
			}
			pureSource = captured
		} else {
			if mode != managedLateSessionMQTTRepair && mode != managedLateSessionOpenBaoTLSRepair || len(e.Journal.Entries) == 0 {
				return nil, managedCoreObservationBlock("late-current-source")
			}
			key := strings.SplitN(e.Journal.Entries[len(e.Journal.Entries)-1].Key, "/", 2)[0]
			old := *e
			if mode == managedLateSessionMQTTRepair {
				review := strings.TrimPrefix(key, "mqtt-root-crl-renewal-")
				if key != "mqtt-root-crl-renewal-"+review || !environmentPreflightDigest.MatchString(review) {
					return nil, managedCoreObservationBlock("late-mqtt-progress-key")
				}
				artifact, captured, err := loadCreateMQTTRootCRLArtifact(cfg, store, e, review)
				if err != nil {
					return nil, err
				}
				if artifact.Plan.SourceJournalSHA != late.SourceSHA {
					return nil, managedCoreObservationBlock("late-authenticated-mqtt-source")
				}
				if failure := mqttRootCRLProgressFailure(e.Journal, artifact, captured, false); failure != "" {
					return nil, managedCoreObservationBlock("late-authenticated-mqtt-tail-" + failure)
				}
				old.Journal = captured
			} else {
				review := strings.TrimPrefix(key, "openbao-tls-root-crl-renewal-")
				if key != "openbao-tls-root-crl-renewal-"+review || !environmentPreflightDigest.MatchString(review) {
					return nil, managedCoreObservationBlock("late-openbao-tls-progress-key")
				}
				artifact, captured, err := loadCreateOpenBaoTLSRootCRLArtifact(cfg, store, e, review)
				if err != nil {
					return nil, err
				}
				if artifact.Plan.SourceJournalSHA != late.SourceSHA {
					return nil, managedCoreObservationBlock("late-authenticated-openbao-tls-source")
				}
				if !openBaoTLSRootCRLProgressValid(e.Journal, artifact, captured, false) {
					return nil, managedCoreObservationBlock("late-authenticated-openbao-tls-tail")
				}
				old.Journal = captured
			}
			pureSource = &old
		}
	}
	freshPlan := *p
	freshPlan.Materials = map[string]string{}
	source, loaded, err := loadManagedLateEffectiveSource(cfg, store, pureSource, &freshPlan, late)
	if err != nil || loaded == nil || loaded.Mode != late.Mode || loaded.CoreSourceSHA != late.CoreSourceSHA || deploymentplan.Digest(loaded.MQTTRepair) != deploymentplan.Digest(late.MQTTRepair) || deploymentplan.Digest(loaded.MQTTCompletion) != deploymentplan.Digest(late.MQTTCompletion) || deploymentplan.Digest(loaded.OpenBaoTLSRepair) != deploymentplan.Digest(late.OpenBaoTLSRepair) || deploymentplan.Digest(loaded.OpenBaoTLSCompletion) != deploymentplan.Digest(late.OpenBaoTLSCompletion) || loaded.OriginalRepairReview != late.OriginalRepairReview || loaded.RestoreReview != late.RestoreReview || loaded.SourceSHA != late.SourceSHA || loaded.SourceArtifactSHA != late.SourceArtifactSHA || loaded.OriginalSession != late.OriginalSession || loaded.OriginalEnrollmentSession != late.OriginalEnrollmentSession || loaded.OriginalClientsSHA != late.OriginalClientsSHA || loaded.OriginalKeySHA != late.OriginalKeySHA || loaded.OriginalCSRSHA != late.OriginalCSRSHA || loaded.AcceptedCore.SHA256() != late.AcceptedCore.SHA256() || deploymentplan.Digest(loaded.AcceptedCoreJournal) != deploymentplan.Digest(late.AcceptedCoreJournal) || deploymentplan.Digest(loaded.SourceCeremony) != deploymentplan.Digest(late.SourceCeremony) || deploymentplan.Digest(loaded.SourceRenewedIntent) != deploymentplan.Digest(late.SourceRenewedIntent) {
		return nil, managedCoreObservationBlock("late-authenticated-source-profile")
	}
	// Re-derive the original source certificate path rather than using the new
	// preview path. Auth alone points to original736 while fresh GO stays separate.
	source.ManagedLateSession = late
	originalAdmission, _, err := loadAdmittedCreateRecovery(store, e, late.OriginalRepairReview)
	if err != nil || source.ManagedCorePartial == nil || source.ManagedCorePartial.ArtifactSHA != deploymentplan.Digest(originalAdmission) || deploymentplan.Digest(originalAdmission.Plan) != late.OriginalRepairReview || !environmentPreflightDigest.MatchString(originalAdmission.Plan.Materials["accepted-service-core-managed-application-current-servers"]) {
		return nil, managedCoreObservationBlock("late-authentic-original-host-admission")
	}
	source.ManagedTransportRecovery.Partial.ServerSHA = originalAdmission.Plan.Materials["accepted-service-core-managed-application-current-servers"]
	source.Review = late.OriginalRepairReview
	source.Ceremony = loaded.SourceCeremony
	source.Intent, source.RenewedIntent = loaded.SourceRenewedIntent, loaded.SourceRenewedIntent
	return source, nil
}

func verifyManagedLateSessionObservationWithMode(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan, late *createManagedLateSessionRecovery, mode managedLateSessionMode) error {
	if mode != managedLateSessionMQTTRepair && mode != managedLateSessionACKReady && mode != managedLateSessionOpenBaoTLSRepair || ctx == nil || p == nil || p.Materials == nil || e == nil || e.Check(ctx) != nil || b.Store.Root != store.Root {
		return managedCoreObservationBlock("late-exact-observation-mode")
	}
	source, err := authenticateManagedLateObservationSource(cfg, store, e, r, p, late, mode)
	if err != nil {
		return err
	}
	if source.Ceremony.BootstrapSessionID != late.OriginalSession || late.OriginalSession != late.OriginalEnrollmentSession || late.SourceRenewedIntent.SessionID != late.OriginalSession || source.OriginalKeySHA != late.OriginalKeySHA || source.OriginalCSRSHA != late.OriginalCSRSHA || source.ManagedTransportRecovery.Partial.ClientsSHA != late.OriginalClientsSHA {
		return managedCoreObservationBlock("late-original-identity")
	}
	if mode != managedLateSessionACKReady && (late.NewSessionID != "" || late.Renewal != nil || r.RenewalCandidate != nil || late.OwnerCandidate != nil) {
		return managedCoreObservationBlock("mqtt-prior-observation-has-no-new-authority")
	}
	core := source.ManagedCorePartial
	originalOwners := map[string]string{}
	ids, actions := []string{}, []string{}
	for index, resource := range late.AcceptedCore.Resources {
		ids = append(ids, resource.ID)
		actions = append(actions, resource.Action)
		owner := core.Journal.Owners[resource.ID]
		if index > 13 {
			owner = core.Plan.Resources[index].UID
		}
		originalOwners[resource.ID] = owner
	}
	if err := validateManagedLateAcceptedCore(ids, late.AcceptedCoreJournal.Completed, originalOwners, late.AcceptedCoreJournal.Owners, late.AcceptedCoreJournal.Accepted, late.AcceptedCoreJournal.AcceptanceStarted, late.AcceptedCoreJournal.Failed, late.AcceptedCoreJournal.InFlight, actions, core.PriorForward == nil); err != nil {
		return managedCoreObservationBlock("late-actual-restored16")
	}
	owners := map[string]deploymentplan.Resource{}
	fingerprints := map[string]string{}
	for _, item := range []struct {
		Plan    *deploymentplan.Plan
		Journal deploymentplan.Journal
		Count   int
	}{{late.AcceptedCore, late.AcceptedCoreJournal, 16}, {core.Apps, core.AppsJournal, 26}, {core.Routing, core.RoutingJournal, 1}} {
		if err := observeManagedLateAcceptedOwners(ctx, b, item.Plan, item.Journal, item.Count, owners, fingerprints); err != nil {
			return err
		}
	}
	// MQTT CRL renewal is publication/cache-only, so no Desired overrides exist.
	// Any future controlled restart is a separately admitted, authenticated child.
	if err := verifyManagedApplicationCurrentVolumes(ctx, cfg, e, b, source, p); err != nil {
		return err
	}
	if err := verifyManagedCoreAllVolumes(ctx, e, b, p.Materials); err != nil {
		return err
	}
	if err := verifyManagedApplicationCurrentExports(ctx, e, b, source.ManagedApplicationPartial, p.Materials); err != nil {
		return err
	}
	originalAPIRows := p.Materials[managedLateAPINativeMaterial]
	if err := verifyManagedApplicationCurrentAPIServers(ctx, cfg, e, b, source, p.Materials); err != nil {
		return err
	}
	if err := bindManagedLateFreshAPIServers(p.Materials, originalAPIRows); err != nil {
		return err
	}
	// Explicit source copy: original11 clients, two host issuances, e0 raw key/CSR,
	// three real expired copies/namespace/KubeCA and exact8 ACK only missing Log.
	if err := verifyManagedCoreCurrentSession(ctx, cfg, store, e, b, source, p, managedCoreRestoreOnly); err != nil {
		return err
	}
	if p.Materials["accepted-service-core-managed-application-current-servers"] != source.ManagedTransportRecovery.Partial.ServerSHA {
		return managedCoreObservationBlock("late-original-host-csr-certificates")
	}
	// Integration: add only managedLateSessionNativeCheckpoint(store,e), which
	// reruns the same bounded pure loader, to this existing helper's source guard.
	if err := verifyManagedTransportNativeRegistry(ctx, cfg, store, e, b, p); err != nil {
		return err
	}
	native, err := verifyCreateManagedOpenBaoACKNative(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: source})
	if err != nil {
		return err
	}
	p.Materials["accepted-service-core-managed-late-observation-openbao-native"] = deploymentplan.Digest(native)
	path, err := store.safePath("pki/app/routing.json")
	if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
		return managedCoreObservationBlock("late-routing-private-path")
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil {
		return managedCoreObservationBlock("late-routing-file")
	}
	cm, err := b.Read(ctx, "ConfigMap/"+e.Journal.Stack+"-video-cloud/"+environmentAppRoutingDeclaration)
	routing, routingErr := validateManagedCoreRoutingPublication(core, e.Journal, raw, cm)
	if err != nil || routingErr != nil {
		return managedCoreObservationBlock("late-original-routing-proof")
	}
	p.Materials["accepted-service-core-managed-late-observation-routing"] = deploymentplan.Digest(routing)
	guardMode := managedLateObserveACKReady
	if mode == managedLateSessionMQTTRepair {
		guardMode = managedLateObserveMQTTPrior
	} else if mode == managedLateSessionOpenBaoTLSRepair {
		guardMode = managedLateObserveOpenBaoTLSPrior
	}
	var pairedOriginals map[string]managedLateCRLRecord
	if mode == managedLateSessionMQTTRepair {
		pairedOriginals, err = managedLatePairedOriginalRecords(store, e, source, mode)
		if err != nil {
			return err
		}
	}
	records, crlProof, err := readManagedLateRegistryCRLs(ctx, cfg, e, b, source, guardMode, pairedOriginals)
	if err != nil {
		return err
	}
	p.Materials["accepted-service-core-managed-late-observation-current-crls"] = deploymentplan.Digest(crlProof)
	if mode != managedLateSessionMQTTRepair {
		artifact, completion, err := readCreateMQTTRootCRLRecovery(store, e.Journal.PlanSHA256)
		if err != nil || artifact == nil || completion == nil || artifact.Plan.Root.ID != source.MQTTRoot.ID || artifact.Plan.Signed.Number != "2" || records[source.MQTTRoot.ID].Digest != artifact.Plan.Signed.Digest {
			return managedCoreObservationBlock("late-genuine-mqtt-root2-completion")
		}
		freshness, err := verifyCreateMQTTRootCRLFreshness(ctx, cfg, store, e, b, owners)
		if err != nil {
			return err
		}
		p.Materials["accepted-service-core-managed-late-observation-mqtt-installed"] = deploymentplan.Digest(freshness)
		if mode == managedLateSessionACKReady {
			tlsArtifact, tlsCompletion, err := readCreateOpenBaoTLSRootCRLRecovery(store, e.Journal.PlanSHA256)
			if err != nil || tlsArtifact == nil || tlsCompletion == nil || tlsArtifact.Plan.Root.ID != source.OpenBaoRoot.ID || tlsArtifact.Plan.Signed.Number != "2" || records[source.OpenBaoRoot.ID].Digest != tlsArtifact.Plan.Signed.Digest {
				return managedCoreObservationBlock("late-genuine-openbao-tls-root2-completion")
			}
			tlsFresh, err := verifyCreateOpenBaoTLSRootCRLFreshness(ctx, cfg, store, e, b, owners)
			if err != nil {
				return err
			}
			p.Materials["accepted-service-core-managed-late-observation-openbao-tls-installed"] = deploymentplan.Digest(tlsFresh)
		}
	} else {
		// TLS adapter's qualifier separately reads its exact two original Root#1
		// cache targets before signing. This observer binds fresh all-native records;
		// it does not treat expiry as runtime ACK readiness or perform any install.
		snapshot, nativeRaw, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, source.MQTTRoot.ID)
		if err != nil || snapshot.Prior.Number != "1" || snapshot.Prior.Digest != records[source.MQTTRoot.ID].Digest || snapshot.Prior.NextUpdate.After(time.Now().UTC()) {
			return managedCoreObservationBlock("mqtt-original-expired-prior")
		}
		p.Materials["accepted-service-core-managed-late-observation-mqtt-prior-revocations"] = environmentPreflightBytesSHA256(nativeRaw)
		_, _, targets, _, _, err := loadMQTTRootCRLTargets(ctx, cfg, e, b, source, late, *source.MQTTRoot, snapshot.Prior)
		if err != nil || len(targets) != 2 {
			return managedCoreObservationBlock("mqtt-original-two-prior-caches")
		}
		cacheProof := map[string]string{}
		for _, target := range targets {
			for path, hash := range target.StateSHA {
				cacheProof[target.ID+":"+path] = hash
			}
		}
		acks, err := readManagedLateMQTTPriorACKs(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b}, source.MQTTRoot.ID, snapshot.Prior.Digest)
		if err != nil || len(acks) != 2 || acks[0].Consumer != "video-cloud-api" || acks[1].Consumer != "video-cloud-logingester" {
			return managedCoreObservationBlock("mqtt-original-two-prior-acks")
		}
		for _, ack := range acks {
			if ack.IssuerID != source.MQTTRoot.ID || ack.Digest != snapshot.Prior.Digest || ack.At.IsZero() || ack.At.Before(snapshot.Prior.ThisUpdate) || ack.At.After(time.Now().UTC()) {
				return managedCoreObservationBlock("mqtt-original-prior-ack")
			}
		}
		p.Materials["accepted-service-core-managed-late-observation-mqtt-prior-caches"] = deploymentplan.Digest(cacheProof)
		p.Materials["accepted-service-core-managed-late-observation-mqtt-prior-acks"] = deploymentplan.Digest(acks)
		paired, err := observeManagedLatePairedCRLPrior(ctx, cfg, store, e, b, source, late, records)
		if err != nil {
			return err
		}
		p.Materials[managedLatePairedCRLPriorMaterial] = deploymentplan.Digest(paired)
	}
	for id, expected := range fingerprints {
		current, err := b.Read(ctx, id)
		if err != nil || current == nil || deploymentplan.Digest(deploymentplan.Projection(current)) != expected {
			return managedCoreObservationBlock("late-owner-changed-" + id)
		}
	}
	_, freshCRLs, err := readManagedLateRegistryCRLs(ctx, cfg, e, b, source, guardMode, pairedOriginals)
	if err != nil || deploymentplan.Digest(freshCRLs) != deploymentplan.Digest(crlProof) {
		return managedCoreObservationBlock("late-crl-registry-changed")
	}
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil {
		return err
	}
	state, _, err := readManagedSessionState(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b}, late.OriginalSession)
	var observed createServiceSessionObserved
	var saved managedSessionState
	observedPath := filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-renewal", source.ManagedTransportRecovery.Partial.SourceReview, "observed.json")
	if err != nil || readExecutionJSON(observedPath, &observed) != nil || json.Unmarshal(observed.Session, &saved) != nil {
		return managedCoreObservationBlock("late-original-session-final-read")
	}
	descriptor, err := managedCoreSessionObservation(state, saved, source.Ceremony, subjects, time.Now().UTC(), managedCoreRestoreOnly)
	if err != nil || deploymentplan.Digest(descriptor) != p.Materials["accepted-service-core-managed-complete-current-session"] {
		return managedCoreObservationBlock("late-session-changed")
	}
	p.Materials["accepted-service-core-managed-late-observation-current-owners"] = deploymentplan.Digest(fingerprints)
	p.Materials["accepted-service-core-managed-late-observation-mode"] = deploymentplan.Digest(mode)
	return e.Check(ctx)
}

func observeManagedLateAcceptedOwners(ctx context.Context, b environmentKubernetesBackend, plan *deploymentplan.Plan, j deploymentplan.Journal, count int, owners map[string]deploymentplan.Resource, fingerprints map[string]string) error {
	if plan == nil || len(plan.Resources) != count || !j.Accepted || !j.AcceptanceStarted || j.Failed != "" || j.InFlight != "" || j.PlanSHA256 != plan.SHA256() || len(j.Completed) != count || len(j.Owners) != count {
		return managedCoreObservationBlock("late-accepted-owner-plan")
	}
	for index, resource := range plan.Resources {
		actual, err := b.Read(ctx, resource.ID)
		uid := j.Owners[resource.ID]
		if err != nil || actual == nil || uid == "" || j.Completed[index] != resource.ID || deploymentplan.Map(actual["metadata"])["uid"] != uid || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(resource.Desired, actual) {
			return managedCoreObservationBlock("late-owner-" + resource.ID)
		}
		ready := resource
		ready.Action, ready.UID = "retain", uid
		bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
		err = b.Wait(bounded, ready)
		cancel()
		if err != nil {
			return managedCoreObservationBlock("late-ready-" + resource.ID)
		}
		recorded := resource
		recorded.UID = uid
		owners[resource.ID] = recorded
		fingerprints[resource.ID] = deploymentplan.Digest(deploymentplan.Projection(actual))
	}
	return nil
}

func readManagedLateRegistryCRLs(ctx context.Context, cfg deploymentConfig, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, mode managedLateObservationMode, paired ...map[string]managedLateCRLRecord) (map[string]managedLateCRLRecord, map[string]managedLateCRLProof, error) {
	if len(paired) > 1 || len(paired) == 1 && paired[0] != nil && mode != managedLateObserveMQTTPrior {
		return nil, nil, managedCoreObservationBlock("paired-repair-only-registry-read")
	}
	roots := map[string]environmentCreateRegisteredIssuer{"service": r.Ceremony.Root, "device": r.ManagedTransportRecovery.Device, "mqtt": *r.MQTTRoot, "openbao_tls": *r.OpenBaoRoot, "app": r.ManagedTransportRecovery.AppRoot}
	issuers := map[string]environmentCreateRegisteredIssuer{}
	for _, issuer := range roots {
		issuers[issuer.ID] = issuer
	}
	for _, issuer := range []environmentCreateRegisteredIssuer{r.Ceremony.Intermediate, *r.MQTTIntermediate, *r.OpenBaoIntermediate, r.ManagedTransportRecovery.AppIntermediate} {
		if issuers[issuer.ID].ID != "" {
			return nil, nil, managedCoreObservationBlock("late-duplicate-crl-authority")
		}
		issuers[issuer.ID] = issuer
	}
	ids := []string{}
	for id := range issuers {
		if !environmentBootstrapOwnerUUID.MatchString(id) {
			return nil, nil, managedCoreObservationBlock("late-crl-original-issuer")
		}
		ids = append(ids, "'"+id+"'")
	}
	slices.Sort(ids)
	if len(ids) != 9 {
		return nil, nil, managedCoreObservationBlock("late-nine-crl-authorities")
	}
	query := "SELECT COALESCE(json_agg(json_build_object('issuer_id',issuer_id,'crl_sha256',digest,'crl_number',number::text,'crl_pem',pem,'this_update',this_update,'next_update',next_update) ORDER BY issuer_id),'[]'::json) FROM (SELECT DISTINCT ON(issuer_id) issuer_id,digest,number,pem,this_update,next_update FROM pki_crls WHERE issuer_id IN (" + strings.Join(ids, ",") + ") ORDER BY issuer_id,number DESC) latest"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b}, query)
	var rows []managedLateCRLRecord
	if err != nil || decodeEnvironmentJSON(raw, &rows) != nil || len(rows) != 9 {
		return nil, nil, managedCoreObservationBlock("late-nine-native-latest-crls")
	}
	records, proofs := map[string]managedLateCRLRecord{}, map[string]managedLateCRLProof{}
	for _, record := range rows {
		issuer, ok := issuers[record.IssuerID]
		if !ok || records[record.IssuerID].IssuerID != "" {
			return nil, nil, managedCoreObservationBlock("late-native-crl-authority")
		}
		cert, err := parseFirstCertificatePEM([]byte(issuer.Certificate))
		if err != nil || !cert.IsCA || cert.KeyUsage&x509.KeyUsageCRLSign == 0 {
			return nil, nil, managedCoreObservationBlock("late-crl-original-certificate")
		}
		var proof managedLateCRLProof
		if scopedProof, selected, scopeErr := managedLateAppRootCRLPrior(ctx, e.Store, e, r, record); selected {
			proof, err = scopedProof, scopeErr
		} else if scopedProof, selected, scopeErr := managedLateDeviceRootCRLPrior(ctx, e.Store, e, r, record); selected {
			proof, err = scopedProof, scopeErr
		} else if len(paired) == 1 && paired[0] != nil {
			proof, err = validateManagedLatePairedPriorCRL(issuer.Domain, issuer.ID, environmentPreflightBytesSHA256(cert.Raw), issuer.Status, issuer.ChainPEM, record, paired[0], mode, time.Now().UTC())
		} else {
			proof, err = validateManagedLateSignedCRL(issuer.Domain, issuer.ID, environmentPreflightBytesSHA256(cert.Raw), issuer.Status, issuer.ChainPEM, record, mode, time.Now().UTC())
		}
		if err != nil {
			return nil, nil, managedCoreObservationBlock("late-native-crl-signature-freshness-" + issuer.Domain)
		}
		records[issuer.ID], proofs[issuer.ID] = record, proof
	}
	return records, proofs, nil
}

// Historical Root#1 inspection is bounded to its exact native digest. It never
// acknowledges or installs anything; the repair adapter owns the genuine #2.
type managedLatePriorACK struct {
	IssuerID string    `json:"issuer_id"`
	Digest   string    `json:"digest"`
	Consumer string    `json:"consumer_id"`
	At       time.Time `json:"acknowledged_at"`
}

func readManagedLateMQTTPriorACKs(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, root, digest string) ([]managedLatePriorACK, error) {
	if root != "14c1a269-e32e-4eac-aa93-db5ad8e85f7e" {
		return nil, managedCoreObservationBlock("mqtt-exact-prior-ack-query")
	}
	return readManagedLateRootPriorACKs(ctx, cfg, runtime, root, digest)
}

func readManagedLateRootPriorACKs(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, root, digest string) ([]managedLatePriorACK, error) {
	if !environmentBootstrapOwnerUUID.MatchString(root) || !environmentPreflightDigest.MatchString(digest) || root != "14c1a269-e32e-4eac-aa93-db5ad8e85f7e" && root != "734487b8-abcf-40be-a813-8b52c64f7954" {
		return nil, managedCoreObservationBlock("paired-exact-prior-ack-query")
	}
	query := "SELECT COALESCE(json_agg(json_build_object('issuer_id',issuer_id,'digest',digest,'consumer_id',consumer_id,'acknowledged_at',acknowledged_at) ORDER BY consumer_id),'[]'::json) FROM pki_crl_acknowledgments WHERE issuer_id='" + root + "' AND digest='" + digest + "'"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var rows []managedLatePriorACK
	if err != nil || decodeEnvironmentJSON(raw, &rows) != nil {
		return nil, managedCoreObservationBlock("mqtt-native-prior-ack-query")
	}
	return rows, nil
}
