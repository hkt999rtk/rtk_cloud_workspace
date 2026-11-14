package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"maps"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const acceptedIssuerEnrollmentProfile = "accepted-certissuer-server-enrollment"
const acceptedIssuerCoreCRLsProfile = "accepted-service-core-crls"
const issuerMQTTCustodyCAProfile = "mqtt-root-custody-ca"
const issuerMQTTLogTrustProfile = "mqtt-root-log-public-trust"
const issuerDeviceCustodyProfile = "device-root-openbao-custody"
const issuerLeaseMetadataRepair = "issuer-lease-metadata"
const issuerLeaseMetadataRepairSchema = "rtk-cloud.create-issuer-lease-metadata-selection/v1"

const issuerEnrollmentContinuationPrefix = "issuer-enrollment-continuation-"

var issuerCoreCheckpointStages = [...]string{
	"kubernetes/service-managed-enrollment-issuer",
	"kubernetes/service-core-enrollment",
	"kubernetes/service-core-initial-crls",
}

var issuerMQTTCheckpointStages = [...]string{
	"kubernetes/service-managed-core",
	"kubernetes/application-client-enrollment",
	"kubernetes/transport-client-enrollment",
}

func issuerMQTTCustodyCheckpoint(j environmentCreateJournal) (environmentCreateJournalEntry, bool) {
	const rootSteps = 4
	start := len(j.Entries) - len(issuerMQTTCheckpointStages) - rootSteps - 1
	if start < 0 {
		return environmentCreateJournalEntry{}, false
	}
	prior := j
	prior.Entries = j.Entries[:start]
	entry, ok := issuerCoreContinuationEntry(prior)
	if !ok {
		return entry, false
	}
	for i, key := range issuerMQTTCheckpointStages {
		step := j.Entries[start+i]
		if step.Key != key || step.State != "completed" || step.CompletedAt.IsZero() || len(step.Receipt) == 0 {
			return entry, false
		}
	}
	steps := j.Entries[start+len(issuerMQTTCheckpointStages):]
	last := steps[rootSteps]
	return entry, steps[0].Key == "pki-reserve-mqtt-root" && last.Key == "kubernetes/mqtt-root-trust" && last.State == "pending" && len(last.Receipt) == 0
}

func issuerMQTTLogTrustCheckpoint(j environmentCreateJournal) (environmentCreateJournalEntry, bool) {
	if len(j.Entries) == 0 {
		return environmentCreateJournalEntry{}, false
	}
	last := j.Entries[len(j.Entries)-1]
	if last.Key != "kubernetes/"+mqttCustodyCAStage || last.State != "pending" || len(last.Receipt) != 0 {
		return environmentCreateJournalEntry{}, false
	}
	j.Entries = j.Entries[:len(j.Entries)-1]
	return issuerMQTTCustodyCheckpoint(j)
}

func issuerCoreContinuationEntry(j environmentCreateJournal) (environmentCreateJournalEntry, bool) {
	index := len(j.Entries) - len(issuerCoreCheckpointStages) - 1
	if index < 1 || j.Entries[index-1].Key != "kubernetes/service-issuer-server-enrollment" {
		return environmentCreateJournalEntry{}, false
	}
	entry := j.Entries[index]
	sha := strings.TrimPrefix(entry.Key, issuerEnrollmentContinuationPrefix)
	if sha == entry.Key || !environmentPreflightDigest.MatchString(sha) || entry.Phase != "continuation" ||
		entry.State != "pending" || len(entry.Receipt) != 0 || entry.StartedAt.IsZero() {
		return environmentCreateJournalEntry{}, false
	}
	for i, key := range issuerCoreCheckpointStages {
		stage := j.Entries[index+i+1]
		if stage.Key != key || stage.State != "completed" || stage.CompletedAt.IsZero() || len(stage.Receipt) == 0 {
			return environmentCreateJournalEntry{}, false
		}
	}
	return entry, true
}

func verifyIssuerCoreContinuationAdmission(store secretStore, e *environmentCreateExecution, entry environmentCreateJournalEntry) (issuerEnrollmentContinuationProof, error) {
	var proof issuerEnrollmentContinuationProof
	bad := func() (issuerEnrollmentContinuationProof, error) {
		return proof, createProviderRecoveryBlock("original Service core continuation lacks its admitted plan and completed checkpoint provenance")
	}
	sha := strings.TrimPrefix(entry.Key, issuerEnrollmentContinuationPrefix)
	if !environmentPreflightDigest.MatchString(sha) ||
		readEnvironmentPreflightJSON(filepath.Join(store.Root, "deployment/create/issuer-enrollment-continuation", sha, "qualification.json"), &proof) != nil ||
		proof.Parent != e.Journal.PlanSHA256 || proof.ParentOwner != deploymentplan.Digest(e.owner) ||
		!proof.ValidUntil.Equal(proof.QualifiedAt.Add(time.Hour)) || entry.StartedAt.Before(proof.QualifiedAt) || !entry.StartedAt.Before(proof.ValidUntil) {
		return bad()
	}
	proof.QualifiedAt, proof.ValidUntil = time.Time{}, time.Time{}
	intentSHA, err := environmentCreatePlanSHA256(mustProviderRecoveryJSON(proof))
	if err != nil || intentSHA != entry.IntentSHA256 || deploymentplan.Digest(proof) != sha {
		return bad()
	}
	prior := e.Journal
	index := slices.IndexFunc(prior.Entries, func(v environmentCreateJournalEntry) bool { return v.Key == entry.Key })
	if index < 0 {
		return bad()
	}
	prior.Entries = prior.Entries[:index]
	for len(prior.KubernetesLeases) > 0 && deploymentplan.Digest(prior) != proof.ParentJournal {
		prior.KubernetesLeases = prior.KubernetesLeases[:len(prior.KubernetesLeases)-1]
	}
	if deploymentplan.Digest(prior) != proof.ParentJournal {
		return bad()
	}
	first, _, err := acceptedCreateStage(store, e, issuerCoreCheckpointStages[0])
	if err != nil || first.SHA256() != proof.FirstStage || first.ReleaseSHA256 != proof.Release {
		return bad()
	}
	return proof, nil
}

func issuerEnrollmentForwardPlan(original environmentCreatePlan, images map[string]environmentReleaseImage) (environmentCreatePlan, map[string]string, error) {
	plan := original
	plan.Release.Images = maps.Clone(original.Release.Images)
	plan.Bootstrap.Images = maps.Clone(original.Bootstrap.Images)
	replacements := map[string]string{}
	selected := map[string]bool{}
	for role, old := range original.Release.Images {
		repository, _, _ := strings.Cut(old.Reference, "@")
		name, official := strings.CutPrefix(repository, "ghcr.io/hkt999rtk/rtk_video_cloud/")
		image, applies := images[name]
		if !official || !applies {
			continue
		}
		if !immutableEnvironmentImage.MatchString(image.Reference) || !strings.HasPrefix(image.Reference, repository+"@") ||
			!deploymentUpgradeSHA.MatchString(image.SourceRevision) || image.Reference == old.Reference ||
			original.Bootstrap.Images[role] != old.Reference {
			return plan, nil, createProviderRecoveryBlock("exact formally published custody repair image differs from original role: " + role)
		}
		selected[name] = true
		replacements[old.Reference] = image.Reference
		plan.Release.Images[role], plan.Bootstrap.Images[role] = image, image.Reference
	}
	if len(selected) != len(images) || replacements[original.Bootstrap.Image] == "" {
		return plan, nil, createProviderRecoveryBlock("formal custody repair publication does not match the remaining bootstrap image roles")
	}
	plan.Bootstrap.Image = replacements[original.Bootstrap.Image]
	return plan, replacements, nil
}

func prepareIssuerEnrollmentRuntime(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, original environmentCreatePlan, replacements map[string]string, mqttRootReady, deviceRootReady bool, allowExpired ...bool) (environmentCreateBootstrapRuntime, error) {
	runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, Backend: b, InitialAuthSeparated: true}
	if _, _, err := originalCreateOpenBaoFoundationIdentity(ctx, cfg, store, e, b, "kubernetes/foundation"); err != nil {
		return runtime, err
	}
	binding, recorded, err := resolveInitialOperatorBinding(ctx, cfg, store, e, b, operator)
	if err != nil || !recorded {
		return runtime, createProviderRecoveryBlock("original registered Platform Admin binding is unavailable")
	}
	runtime.APIOrigin, runtime.APIClient, err = newEnvironmentCreateAccountClient(ctx, cfg, b, e)
	if err != nil {
		return runtime, err
	}
	runtime.PKIClient, err = newEnvironmentCreatePKIClient(runtime.APIOrigin, runtime.APIClient, cfg.Environment, binding.UserID)
	if err != nil {
		return runtime, err
	}
	if _, err := runtime.PKIClient.Authenticate(ctx, operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]); err != nil {
		return runtime, err
	}
	service, err := restoreAcceptedIssuerEnrollment(ctx, cfg, store, e, b, operator, runtime.PKIClient, original, mqttRootReady, deviceRootReady, allowExpired...)
	if err != nil {
		return runtime, err
	}
	runtime.AcceptedIssuerEnrollment = &service
	caPath, managementPEM, err := readIssuerEnrollmentInitialTrust(store, e.Journal.PlanSHA256)
	if err != nil {
		return runtime, err
	}
	managementCA := []byte(managementPEM)
	rootTokenPath, err := store.safePath("openbao/root-token")
	if err != nil {
		return runtime, err
	}
	runtime.OpenBao = environmentCreateOpenBaoBootstrap{RootTokenPath: rootTokenPath, URL: "https://openbao." + original.Stack + "-secrets.svc:8200", CACertPath: caPath}
	runtime.TransportTrustConfigMap = "pki-create-transport-trust"
	runtime.TransportCAPEM = string(managementCA)
	root, _ := parseFirstCertificatePEM(managementCA)
	runtime.TransportRootSHA256 = environmentPreflightBytesSHA256(root.Raw)
	runtime.TransportSecrets = map[string]string{"account-manager": "pki-create-account-manager-transport", "pki-controller": "pki-create-pki-controller-transport", "certissuer": "pki-create-certissuer-transport", "video-cloud-api": "pki-create-video-cloud-api-transport", "video-cloud-api-app-pki": "pki-create-video-cloud-api-app-pki-transport"}
	runtime.Custody, err = createEnvironmentCustodyBinding(cfg, original.Bootstrap, e.Journal.PlanSHA256, "")
	if err != nil || runtime.Custody == nil {
		return runtime, createProviderRecoveryBlock("original OpenBao workload custody binding is unavailable")
	}
	runtime.Custody.ServiceRootSHA256 = service.Root.RootFingerprint
	operatorBinding := appendMap(operator, map[string]string{"PKI_OPERATOR_USER_ID": binding.UserID})
	listeners, err := renderEnvironmentCreateInitialPKIListeners(cfg, original.Release, binding.UserID, operatorBinding)
	if err != nil {
		return runtime, err
	}
	runtime.InitialListeners, _, err = initialCertIssuerAuthDefinitions(cfg, listeners)
	if err != nil {
		return runtime, err
	}
	for _, d := range runtime.InitialListeners {
		if d.Object["kind"] != "Deployment" {
			continue
		}
		pod := environmentPKIPod(d.Object)
		for _, field := range []string{"containers", "initContainers"} {
			for _, raw := range certIssuerObjectList(pod[field]) {
				container := deploymentplan.Map(raw)
				if newImage := replacements[deploymentplan.String(container["image"])]; newImage != "" {
					container["image"] = newImage
				}
			}
		}
	}
	return runtime, e.Check(ctx)
}

// Reconstruct the original template inputs, retaining their exact authorities.
// Historical bootstrap CA expiry does not describe the current managed TLS
// connection. Session candidate admission separately binds its live managed
// Root, original trust owner and native transport; this reader cannot renew it.
func readIssuerEnrollmentInitialTrust(store secretStore, parent string) (string, string, error) {
	caPath, openBaoPEM, err := initialTransportAuthorityPath(store, parent, "openbao")
	if err != nil {
		return "", "", err
	}
	_, managementPEM, err := initialTransportAuthorityPath(store, parent, "management")
	if err != nil {
		return "", "", err
	}
	for _, raw := range []string{openBaoPEM, managementPEM} {
		cert, err := parseFirstCertificatePEM([]byte(raw))
		if err != nil || cert == nil || !cert.IsCA || cert.CheckSignatureFrom(cert) != nil || !cert.NotAfter.After(cert.NotBefore) {
			return "", "", createProviderRecoveryBlock("original initial transport CA is unavailable or invalid")
		}
	}
	return caPath, managementPEM, nil
}

type issuerEnrollmentSessionAllowance struct {
	Expired              bool
	Successor            *partialSessionContinuationProof
	Completed            *secondSessionAdmission
	NeedsRenewal, Minted *bool
}

func verifyAcceptedIssuerEnrollmentContinuation(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, laterCore bool, allowance ...issuerEnrollmentSessionAllowance) error {
	if finalized, err := observeFinalizedFreshBootstrap(ctx, cfg, store, e, b); err != nil || finalized != nil {
		if err == nil && len(allowance) == 1 {
			if allowance[0].NeedsRenewal != nil {
				*allowance[0].NeedsRenewal = true // Historical completed enrollment, never mint again.
			}
			if allowance[0].Minted != nil {
				*allowance[0].Minted = true
			}
		}
		return err
	}
	plan, _, err := acceptedCreateStage(store, e, "kubernetes/service-issuer-server-enrollment")
	if err != nil {
		return err
	}
	if err := plan.AdmitResumable(ctx, b, store.Root); err != nil {
		return err
	}
	var intent createServiceSessionIntent
	path := filepath.Join(store.Root, "deployment", "create", e.Journal.PlanSHA256, "bootstrap", "service", "session-intent.json")
	if readExecutionJSON(path, &intent) != nil || !environmentBootstrapOwnerUUID.MatchString(intent.SessionID) ||
		intent.Claim != "pki-service-bootstrap-state" || !slices.Equal(intent.Namespaces, []string{plan.Stack + "-video-cloud", plan.Stack + "-account-manager", plan.Stack + "-secrets"}) {
		return createProviderRecoveryBlock("original Service bootstrap session intent differs from the accepted CertIssuer enrollment")
	}
	credentialPlan, credentialJournal, err := acceptedCreateStage(store, e, "kubernetes/service-credential-secrets")
	if err != nil {
		return err
	}
	if laterCore {
		core, coreJournal, err := acceptedCreateStage(store, e, "kubernetes/service-managed-core")
		if err != nil {
			return err
		}
		successor, err := completedInitialTransportSuccessorResources(ctx, store, e, b)
		if err != nil {
			return err
		}
		managed, err := completedManagedAllocationWrites(store, e)
		if err != nil {
			return err
		}
		if err := verifyIssuerCredentialOwnersAfterRecordedTransport(ctx, b.Read, credentialPlan, credentialJournal, core, coreJournal, successor, managed); err != nil {
			return err
		}
	} else if err := credentialPlan.AdmitResumable(ctx, b, store.Root); err != nil {
		return err
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: b}
	state, _, err := readManagedSessionState(ctx, cfg, runtime, intent.SessionID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	expired := len(allowance) == 1 && allowance[0].Expired && issuerEnrollmentSessionExpired(intent, state, now)
	if len(allowance) == 1 && allowance[0].Completed != nil {
		if err := verifyCompletedIssuerSessionContinuation(ctx, cfg, store, e, b, *allowance[0].Completed, intent, state, now); err != nil {
			return err
		}
		expired = true // Historical original; execution uses the completed head below.
		if allowance[0].Minted != nil {
			*allowance[0].Minted = true
		}
	}
	if len(allowance) == 1 && allowance[0].Successor != nil && !expired {
		next, _, nextErr := readManagedSessionState(ctx, cfg, runtime, allowance[0].Successor.RenewedIntent.SessionID)
		if nextErr == nil && partialSessionSuccessorStatesValid(allowance[0].Successor, state, next, now) {
			expired = true
			if allowance[0].Minted != nil {
				*allowance[0].Minted = true
			}
		}
	}
	if expired && len(allowance) == 1 && allowance[0].NeedsRenewal != nil {
		*allowance[0].NeedsRenewal = true
	}
	if !issuerEnrollmentSessionUsable(intent, state, now) && !expired {
		return createProviderRecoveryBlock("original Service bootstrap session is expired, competing or inconsistent; preserve existing issuance and qualify original-session recovery")
	}
	for _, namespace := range intent.Namespaces {
		id := "Secret/" + namespace + "/service-bootstrap-" + intent.SessionID
		actual, err := b.Read(ctx, id)
		if err != nil {
			return err
		}
		uid := credentialJournal.Owners[id]
		cert, decodeErr := base64.StdEncoding.DecodeString(deploymentplan.String(deploymentplan.Map(actual["data"])["tls.crt"]))
		leaf, parseErr := parseFirstCertificatePEM(cert)
		if !environmentBootstrapOwnerUUID.MatchString(uid) || deploymentplan.Map(actual["metadata"])["uid"] != uid ||
			decodeErr != nil || parseErr != nil || environmentPreflightBytesSHA256(leaf.Raw) != state.Fingerprint ||
			!issuerEnrollmentCertificateTimeValid(leaf.NotBefore, leaf.NotAfter, state.Expires, now, expired) {
			return createProviderRecoveryBlock("original session certificate or accepted namespace owner differs: " + id)
		}
	}
	objects, err := b.TargetWorkloadInventory(ctx, plan)
	if err != nil {
		return err
	}
	return verifyEnvironmentTargetWorkloads(ctx, plan, environmentCreateStageInventory(plan, objects), func(ctx context.Context, image string) (map[string]bool, error) {
		return environmentRuntimeImageDigests(ctx, store, image)
	})
}

// Session lifetime may be shorter than its original certificate. Expiry of
// the former permits recovery without requiring premature expiry of the latter.
func issuerEnrollmentCertificateTimeValid(notBefore, notAfter, sessionExpires, now time.Time, expired bool) bool {
	return !now.Before(notBefore) && (expired || notAfter.After(now)) && !notAfter.Before(sessionExpires)
}

// Reconcile the original credential-stage owners with the accepted Core
// stage that installed CRLs on the same trust ConfigMap. The receipt chain
// binds both the UID and the newer plan's original baseline.
func verifyIssuerCredentialOwnersAfterRecordedTransport(ctx context.Context, read func(context.Context, string) (deploymentplan.Object, error), credentials *deploymentplan.Plan, credentialJournal deploymentplan.Journal, core *deploymentplan.Plan, coreJournal deploymentplan.Journal, transport, managed []activeTransportRenewalResource) error {
	// Transport already folds every recorded later native write for its owners,
	// including managed allocation and completed session references. Append only
	// managed owners outside that chain; replaying an overlap uses a stale Before.
	remaining := slices.DeleteFunc(slices.Clone(managed), func(write activeTransportRenewalResource) bool {
		return slices.ContainsFunc(transport, func(recorded activeTransportRenewalResource) bool { return recorded.ID == write.ID })
	})
	return verifyIssuerCredentialOwners(ctx, read, credentials, credentialJournal, core, coreJournal, transport, remaining)
}

func verifyIssuerCredentialOwners(ctx context.Context, read func(context.Context, string) (deploymentplan.Object, error), credentials *deploymentplan.Plan, credentialJournal deploymentplan.Journal, core *deploymentplan.Plan, coreJournal deploymentplan.Journal, successors ...[]activeTransportRenewalResource) error {
	owners := map[string]deploymentplan.Resource{}
	if err := managedLateRecordedOwners(credentials, credentialJournal, owners); err != nil {
		return err
	}
	if err := managedLateRecordedOwners(core, coreJournal, owners); err != nil {
		return err
	}
	for _, original := range credentials.Resources {
		resource := owners[original.ID]
		for _, group := range successors {
			for _, successor := range group {
				if successor.ID != resource.ID {
					continue
				}
				if successor.UID != resource.UID || !deploymentplan.Matches(resource.Desired, successor.Before) {
					return createProviderRecoveryBlock("temporary transport successor does not follow the accepted credential owner: " + resource.ID)
				}
				resource.Desired = successor.Desired
			}
		}
		actual, err := read(ctx, resource.ID)
		if err != nil || deploymentplan.Map(actual["metadata"])["uid"] != resource.UID || !deploymentplan.Matches(resource.Desired, actual) {
			return createProviderRecoveryBlock("live credential owner differs from its latest accepted create stage: " + resource.ID)
		}
	}
	return nil
}

func issuerEnrollmentSessionUsable(intent createServiceSessionIntent, state managedSessionState, now time.Time) bool {
	return state.Session == intent.SessionID && state.Issuer == intent.IssuerID && state.Root == intent.RootSHA &&
		environmentPreflightDigest.MatchString(state.Fingerprint) && state.Status == "active" && state.Expires.After(now) &&
		state.OtherActive == 0 && len(intent.Subjects) > 0 && slices.Equal(state.Subjects, intent.Subjects)
}

// Expiry is a recoverable condition only for the original session and exact
// issuer/subject lineage, without a competing active session. It does not by
// itself authorize continuing enrollment; renewal must still be qualified.
func issuerEnrollmentSessionExpired(intent createServiceSessionIntent, state managedSessionState, now time.Time) bool {
	service := environmentCreateServiceCeremony{
		BootstrapSessionID: intent.SessionID,
		Root:               environmentCreateRegisteredIssuer{RootFingerprint: intent.RootSHA},
		Intermediate:       environmentCreateRegisteredIssuer{ID: intent.IssuerID},
	}
	return managedSessionMayRenew(state, service, intent.Subjects, now)
}

func restoreAcceptedIssuerEnrollment(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, client *environmentCreatePKIClient, plan environmentCreatePlan, mqttRootReady, deviceRootReady bool, allowExpired ...bool) (environmentCreateServiceCeremony, error) {
	var service environmentCreateServiceCeremony
	bad := func() (environmentCreateServiceCeremony, error) {
		return service, createProviderRecoveryBlock("original CertIssuer enrollment requires its exact active Service issuer and observed session custody")
	}
	materials := map[string]string{}
	if deviceRootReady {
		materials["device-custody-continuation"] = e.Journal.PlanSHA256
	}
	root, _, err := loadOriginalReadyServiceRoot(ctx, cfg, store, e, b, operator, &createFoundationRecoveryPlan{Plan: plan, Materials: materials}, true, mqttRootReady)
	if err != nil {
		return service, err
	}
	base := filepath.Join(store.Root, "deployment", "create", e.Journal.PlanSHA256, "bootstrap", "service")
	var intent createServiceSessionIntent
	var observed createServiceSessionObserved
	var session managedSessionState
	var intermediateDocument map[string]any
	if readExecutionJSON(filepath.Join(base, "session-intent.json"), &intent) != nil ||
		readExecutionJSON(filepath.Join(base, "session-observed.json"), &observed) != nil ||
		readExecutionJSON(filepath.Join(base, "intermediate-active.json"), &intermediateDocument) != nil ||
		json.Unmarshal(observed.Session, &session) != nil || !(issuerEnrollmentSessionUsable(intent, session, time.Now().UTC()) || len(allowExpired) == 1 && allowExpired[0] && issuerEnrollmentSessionExpired(intent, session, time.Now().UTC())) ||
		intent.RootSHA != root.RootFingerprint || observed.Cleanup.SessionID != intent.SessionID ||
		validateEnvironmentBootstrapCleanupBinding(&observed.Cleanup, cfg.Environment) != nil ||
		len(observed.UIDs) != len(intent.Namespaces) || !slices.Equal(intent.Namespaces, []string{plan.Stack + "-video-cloud", plan.Stack + "-account-manager", plan.Stack + "-secrets"}) ||
		observed.Reader != "pki-bootstrap-custody-"+e.Journal.PlanSHA256[:12] {
		return bad()
	}
	intermediate, err := environmentCreateRegisteredIssuerFromMap(intermediateDocument, root.RootFingerprint)
	if err != nil || intermediate.ID != intent.IssuerID || intermediate.Status != "active" ||
		intermediate.Domain != "service" || intermediate.Kind != "intermediate" || intermediate.Environment != cfg.Environment ||
		intermediateDocument["parent_issuer_id"] != root.ID {
		return bad()
	}
	live, err := readOriginalCreateIssuerDocument(ctx, cfg, e, b, intermediate.ID)
	if err != nil || deploymentplan.Digest(live) != deploymentplan.Digest(intermediateDocument) {
		return bad()
	}
	_, credentialJournal, err := acceptedCreateStage(store, e, "kubernetes/service-credential-secrets")
	if err != nil {
		return service, err
	}
	for _, ns := range intent.Namespaces {
		if uid := observed.UIDs[ns]; !environmentBootstrapOwnerUUID.MatchString(uid) || uid != credentialJournal.Owners["Secret/"+ns+"/service-bootstrap-"+intent.SessionID] {
			return bad()
		}
	}
	namespace, err := b.Read(ctx, "Namespace//"+intent.Namespaces[0])
	if err != nil || deploymentplan.Map(namespace["metadata"])["uid"] != observed.Cleanup.NamespaceUID || observed.Cleanup.SecretUID != observed.UIDs[intent.Namespaces[0]] {
		return bad()
	}
	crlFile, err := originalCreateRootCRL(store, e, root, "service")
	if err != nil {
		return service, err
	}
	crlPath, err := store.safePath(crlFile)
	if err != nil {
		return service, err
	}
	crl, err := readBoundedEnvironmentFile(crlPath, true)
	if err != nil {
		return service, err
	}
	return environmentCreateServiceCeremony{
		Root: root, Intermediate: intermediate, RootCRLFile: crlFile, RootCRLSecret: "pki-create-service-root-crl", RootCRLPEM: string(crl),
		BootstrapSessionID: intent.SessionID, BootstrapCredentialSecret: "service-bootstrap-" + intent.SessionID,
		BootstrapCredentialNamespace: intent.Namespaces[0], BootstrapCredentialNamespaces: intent.Namespaces,
		BootstrapClaim: intent.Claim, CleanupBinding: observed.Cleanup, CredentialUIDs: observed.UIDs, CustodyReader: observed.Reader,
	}, nil
}
