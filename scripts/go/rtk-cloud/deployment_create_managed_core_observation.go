package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A complete original session may be finalized after genuine expiry. This
// descriptor authorizes only that finalizer, not issuance or another renewal.
// Native active->expired bookkeeping does not change its immutable authority.
type managedCoreObservationAction string

const (
	managedCoreFinalizeComplete managedCoreObservationAction = "finalize-complete-original-session"
	managedCoreRestoreOnly      managedCoreObservationAction = "restore-core-only"
)

type managedCoreCompletedSession struct {
	Action                             string
	Session, Issuer, Root, Fingerprint string
	Expires                            time.Time
	Subjects, Installed                []string
}

func managedCoreObservationBlock(stage string) error {
	return createProviderRecoveryBlock("original stopped final-core observation differs: stage=" + stage)
}

func managedCoreObservationSource(store secretStore, e *environmentCreateExecution, core *createManagedCorePartialRecovery) error {
	if e == nil || core == nil || store.Environment != "dev" || store.Root != e.Store.Root || core.SourceSHA != deploymentplan.Digest(e.Journal) {
		return managedCoreObservationBlock("exact-source")
	}
	_, _, loaded, err := loadManagedCorePartialSource(store, e)
	if err != nil {
		return err
	}
	current := *core
	current.ForwardReview = loaded.ForwardReview
	// The formal repair publication is qualified independently after the pure
	// source descriptor is pinned; it is never part of historical owner state.
	current.CertIssuerPrepare = loaded.CertIssuerPrepare
	if deploymentplan.Digest(current) != deploymentplan.Digest(loaded) {
		return managedCoreObservationBlock("source-profile")
	}
	return nil
}

func validateManagedCoreCurrentOwner(core *createManagedCorePartialRecovery, index int, actual deploymentplan.Object) error {
	plan, journal, err := managedCorePartialCurrentPlan(core)
	if err != nil || index < 0 || index >= len(plan.Resources) {
		return managedCoreObservationBlock("bounded-child")
	}
	resource := plan.Resources[index]
	desired, uid := resource.Desired, journal.Owners[resource.ID]
	if index > len(journal.Completed) {
		desired, uid = resource.Baseline, resource.UID
	}
	metadata := deploymentplan.Map(actual["metadata"])
	if actual == nil || uid == "" || deploymentplan.ID(actual) != resource.ID || metadata["uid"] != uid || metadata["deletionTimestamp"] != nil || !deploymentplan.Matches(desired, actual) {
		return managedCoreObservationBlock("owner-" + resource.ID)
	}
	if index > len(journal.Completed) && deploymentplan.Digest(deploymentplan.Projection(actual)) != deploymentplan.Digest(resource.Baseline) {
		return managedCoreObservationBlock("untouched-" + resource.ID)
	}
	return nil
}

func managedCoreSessionComplete(state, saved managedSessionState, service environmentCreateServiceCeremony, subjects []string, now time.Time) (managedCoreCompletedSession, error) {
	return managedCoreSessionObservation(state, saved, service, subjects, now, managedCoreFinalizeComplete)
}

func managedCoreSessionObservation(state, saved managedSessionState, service environmentCreateServiceCeremony, subjects []string, now time.Time, action managedCoreObservationAction) (managedCoreCompletedSession, error) {
	var empty managedCoreCompletedSession
	if len(subjects) != 9 || !environmentBootstrapOwnerUUID.MatchString(state.Session) || state.Session != service.BootstrapSessionID || state.Session != saved.Session || state.Issuer != service.Intermediate.ID || state.Issuer != saved.Issuer || state.Root != service.Root.RootFingerprint || state.Root != saved.Root || !environmentPreflightDigest.MatchString(state.Fingerprint) || state.Fingerprint != saved.Fingerprint || state.OtherActive != 0 || state.Expires.IsZero() || !state.Expires.Equal(saved.Expires) || !slices.Equal(state.Subjects, subjects) || !slices.Equal(saved.Subjects, subjects) || state.Status != "active" && state.Status != "expired" || state.Status == "expired" && state.Expires.After(now) {
		return empty, managedCoreObservationBlock("complete-original-session")
	}
	declared := map[string]bool{}
	for _, subject := range subjects {
		if declared[subject] || !environmentCreateConsumerID.MatchString(strings.TrimPrefix(subject, "service:")) || !strings.HasPrefix(subject, "service:") {
			return empty, managedCoreObservationBlock("nine-original-subjects")
		}
		declared[subject] = true
	}
	installed := map[string]bool{}
	for _, subject := range state.Installed {
		if !declared[subject] || installed[subject] {
			return empty, managedCoreObservationBlock("nine-genuine-acks")
		}
		installed[subject] = true
	}
	if action != managedCoreFinalizeComplete && action != managedCoreRestoreOnly || action == managedCoreFinalizeComplete && len(installed) != 9 || action == managedCoreRestoreOnly && (len(installed) != 8 || installed["service:video-cloud-logingester"] || state.Expires.After(now)) {
		return empty, managedCoreObservationBlock("scoped-genuine-ack-count")
	}
	ordered := slices.Clone(state.Installed)
	slices.Sort(ordered)
	return managedCoreCompletedSession{Action: string(action), Session: state.Session, Issuer: state.Issuer, Root: state.Root, Fingerprint: state.Fingerprint, Expires: state.Expires, Subjects: slices.Clone(subjects), Installed: ordered}, nil
}

func verifyManagedCoreObservation(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan) error {
	return verifyManagedCoreObservationWithAction(ctx, cfg, store, e, b, r, p, managedCoreFinalizeComplete)
}

func verifyManagedCoreObservationWithAction(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan, action managedCoreObservationAction) error {
	if action != managedCoreFinalizeComplete && action != managedCoreRestoreOnly {
		return managedCoreObservationBlock("bounded-observation-action")
	}
	if r == nil || r.ManagedCorePartial == nil || r.ManagedApplicationPartial == nil || r.ManagedTransportRecovery == nil || r.ManagedTransportRecovery.Partial == nil || r.OpenBaoACK == nil || r.MQTTRoot == nil || r.MQTTIntermediate == nil || r.OpenBaoRoot == nil || r.OpenBaoIntermediate == nil || e == nil || p == nil || p.Materials == nil || cfg.Environment != "dev" || cfg.Values["CLOUD_STACK_NAME"] != e.Journal.Stack || b.Store.Root != store.Root || r.Review != r.ManagedCorePartial.OriginalRepairReview || e.Check(ctx) != nil {
		return managedCoreObservationBlock("authenticated-core")
	}
	core := r.ManagedCorePartial
	if core.Action != action {
		return managedCoreObservationBlock("source-action")
	}
	if err := managedCoreObservationSource(store, e, core); err != nil {
		return err
	}
	owners := map[string]string{}
	observe := func(plan *deploymentplan.Plan, j deploymentplan.Journal, partial bool) error {
		if plan == nil || j.PlanSHA256 != plan.SHA256() || !partial && (!j.Accepted || j.Failed != "" || j.InFlight != "" || len(j.Completed) != len(plan.Resources) || len(j.Owners) != len(plan.Resources)) {
			return managedCoreObservationBlock("accepted-owner-plan")
		}
		for index, resource := range plan.Resources {
			actual, err := b.Read(ctx, resource.ID)
			if err != nil {
				return managedCoreObservationBlock("owner-read-" + resource.ID)
			}
			if partial {
				if err := validateManagedCoreCurrentOwner(core, index, actual); err != nil {
					return err
				}
			} else if actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != j.Owners[resource.ID] || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(resource.Desired, actual) {
				return managedCoreObservationBlock("accepted-owner-" + resource.ID)
			}
			owners[resource.ID] = deploymentplan.Digest(deploymentplan.Projection(actual))
			if !partial || index < len(j.Completed) {
				retained := resource
				retained.Action, retained.UID = "retain", j.Owners[resource.ID]
				ready, cancel := context.WithTimeout(ctx, 30*time.Second)
				err = b.Wait(ready, retained)
				cancel()
				if err != nil {
					return managedCoreObservationBlock("accepted-ready-" + resource.ID)
				}
			}
		}
		return nil
	}
	currentCore, currentJournal, err := managedCorePartialCurrentPlan(core)
	if err != nil {
		return err
	}
	if err := observe(currentCore, currentJournal, true); err != nil {
		return err
	}
	if err := observe(core.Apps, core.AppsJournal, false); err != nil {
		return err
	}
	if err := observe(core.Routing, core.RoutingJournal, false); err != nil {
		return err
	}
	routingProof, err := readManagedCoreRoutingPublication(ctx, store, e, b, core)
	if err != nil {
		return err
	}
	p.Materials[managedCoreRoutingFileMaterial] = routingProof.FileSHA
	p.Materials[managedCoreRoutingProofMaterial] = deploymentplan.Digest(routingProof)
	if err := verifyManagedApplicationCurrentVolumes(ctx, cfg, e, b, r, p); err != nil {
		return err
	}
	if err := verifyManagedCoreAllVolumes(ctx, e, b, p.Materials); err != nil {
		return err
	}
	if err := verifyManagedApplicationCurrentExports(ctx, e, b, r.ManagedApplicationPartial, p.Materials); err != nil {
		return err
	}
	if err := verifyManagedApplicationCurrentAPIServers(ctx, cfg, e, b, r, p.Materials); err != nil {
		return err
	}
	if err := verifyManagedCoreCurrentSession(ctx, cfg, store, e, b, r, p, action); err != nil {
		return err
	}
	if err := verifyManagedTransportNativeRegistry(ctx, cfg, store, e, b, p); err != nil {
		return err
	}
	authentication := *r
	authentication.Intent = r.RenewedIntent
	// The accepted736 ACK owner supersedes the earlier interrupted host recipe.
	// Verify its actual new Pod/process/unseal/audit rather than re-entering the
	// legacy interrupted-replacement validator for a different checkpoint.
	ackProof, err := verifyCreateManagedOpenBaoACKNative(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: &authentication})
	if err != nil {
		return err
	}
	p.Materials["accepted-service-core-managed-complete-openbao-ack-native"] = deploymentplan.Digest(ackProof)
	for id, digest := range owners {
		actual, err := b.Read(ctx, id)
		if err != nil || actual == nil || deploymentplan.Digest(deploymentplan.Projection(actual)) != digest {
			return managedCoreObservationBlock("owner-changed-" + id)
		}
	}
	p.Materials["accepted-service-core-managed-complete-current-owners"] = deploymentplan.Digest(owners)
	p.Materials["accepted-service-core-managed-complete-observation-action"] = deploymentplan.Digest(string(action))
	if err := qualifyManagedHistoricalSeedCleanup(ctx, cfg, store, e, b, r, p); err != nil {
		return err
	}
	return e.Check(ctx)
}

func verifyManagedCoreAllVolumes(ctx context.Context, e *environmentCreateExecution, b environmentKubernetesBackend, materials map[string]string) error {
	ledger, err := readEnvironmentCreateLedger(e)
	var active managedTransportVolumeLedgerActive
	if err != nil || readExecutionJSON(filepath.Join(e.Store.Root, "deployment/resources/active.json"), &active) != nil || validateManagedTransportVolumeLedger(ledger, active) != nil {
		return managedCoreObservationBlock("current-eighteen-volumes")
	}
	original, projectionErr := managedLateRedisHistoricalVolumes(e.Store, e, ledger)
	if projectionErr != nil || len(original.Volumes) != 18 {
		return managedCoreObservationBlock("current-eighteen-volumes")
	}
	originalIDs := map[string]bool{}
	for _, record := range original.Volumes {
		originalIDs["PersistentVolumeClaim/"+record.Namespace+"/"+record.PVCName] = true
	}
	owners := map[string]string{}
	for _, record := range ledger.Volumes {
		id := "PersistentVolumeClaim/" + record.Namespace + "/" + record.PVCName
		owner, err := managedTransportRecordedHostVolume(ledger, id)
		if err != nil {
			return err
		}
		claim, claimErr := b.Read(ctx, id)
		volume, volumeErr := b.Read(ctx, "PersistentVolume//"+owner.PVName)
		if claimErr != nil {
			return claimErr
		}
		if volumeErr != nil {
			return volumeErr
		}
		if validateManagedTransportHostVolume(owner, claim, volume) != nil {
			return managedCoreObservationBlock("native-volume-" + id)
		}
		if originalIDs[id] {
			owners[id] = deploymentplan.Digest(owner)
		}
	}
	materials["accepted-service-core-managed-complete-current-native-volumes"] = deploymentplan.Digest(owners)
	return nil
}

func verifyManagedCoreCurrentSession(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan, action managedCoreObservationAction) error {
	var originalHostPin string
	if r.ManagedLateSession != nil {
		authority, err := managedLateOriginalHostAuthority(cfg, store, e, r, p)
		if err != nil {
			return err
		}
		originalHostPin = authority.InventorySHA
	} else {
		_, admission, core, err := loadManagedCorePartialSource(store, e)
		if err != nil || core.OriginalRepairReview != r.ManagedCorePartial.OriginalRepairReview || core.ArtifactSHA != r.ManagedCorePartial.ArtifactSHA || deploymentplan.Digest(admission) != core.ArtifactSHA {
			return managedCoreObservationBlock("original-host-inventory-admission")
		}
		originalHostPin = admission.Plan.Materials["accepted-service-core-managed-application-current-servers"]
	}
	return verifyManagedCoreCurrentSessionNative(ctx, cfg, store, e, b, r, p, action, originalHostPin)
}

func verifyManagedCoreCurrentSessionNative(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan, action managedCoreObservationAction, originalHostPin string) error {
	runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: b}
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil {
		return err
	}
	state, _, err := readManagedSessionState(ctx, cfg, runtime, r.Ceremony.BootstrapSessionID)
	var observed createServiceSessionObserved
	var saved managedSessionState
	path := filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-renewal", r.ManagedTransportRecovery.Partial.SourceReview, "observed.json")
	if err != nil || readExecutionJSON(path, &observed) != nil || json.Unmarshal(observed.Session, &saved) != nil {
		return managedCoreObservationBlock("original-observation")
	}
	descriptor, err := managedCoreSessionObservation(state, saved, r.Ceremony, subjects, time.Now().UTC(), action)
	if err != nil {
		return err
	}
	material := map[string]string{}
	for _, namespace := range r.Ceremony.BootstrapCredentialNamespaces {
		secret, err := b.Read(ctx, "Secret/"+namespace+"/"+r.Ceremony.BootstrapCredentialSecret)
		if err != nil || secret == nil || deploymentplan.Map(secret["metadata"])["uid"] != r.Ceremony.CredentialUIDs[namespace] || deploymentplan.Map(secret["metadata"])["deletionTimestamp"] != nil {
			return managedCoreObservationBlock("original-seed-owner")
		}
		values := map[string]string{}
		for _, key := range []string{"tls.crt", "tls.key", "ca.crt"} {
			decoded, err := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(deploymentplan.Map(secret["data"])[key]))
			if err != nil {
				return managedCoreObservationBlock("original-seed-material")
			}
			values[key] = string(decoded)
		}
		if len(material) > 0 && deploymentplan.Digest(material) != deploymentplan.Digest(values) {
			return managedCoreObservationBlock("original-seed-copies")
		}
		material = values
	}
	pair, err := tls.X509KeyPair([]byte(material["tls.crt"]), []byte(material["tls.key"]))
	if err != nil || len(pair.Certificate) < 2 || material["ca.crt"] != r.Ceremony.Root.Certificate {
		return managedCoreObservationBlock("original-seed-certificate")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.Subject.CommonName != "service:deployment-bootstrap" || environmentPreflightBytesSHA256(leaf.Raw) != state.Fingerprint || !leaf.NotAfter.Equal(state.Expires) || leaf.NotBefore.After(time.Now()) || environmentPreflightBytesSHA256(pair.Certificate[len(pair.Certificate)-1]) != r.Ceremony.Root.RootFingerprint {
		return managedCoreObservationBlock("original-seed-leaf")
	}
	authority, err := parseFirstCertificatePEM([]byte(r.Ceremony.Intermediate.Certificate))
	if err != nil || leaf.CheckSignatureFrom(authority) != nil {
		return managedCoreObservationBlock("original-seed-signature")
	}
	for _, file := range []string{"/run/pki-bootstrap/bootstrap.key", "/run/pki-bootstrap/bootstrap.csr", r.RenewedCertPath} {
		raw, err := b.command(ctx, nil, "--namespace", r.Ceremony.BootstrapCredentialNamespace, "exec", "deployment/"+r.Ceremony.CustodyReader, "--container", "custody", "--", "cat", file)
		if err != nil || file == "/run/pki-bootstrap/bootstrap.key" && (environmentPreflightBytesSHA256(raw) != r.OriginalKeySHA || string(raw) != material["tls.key"]) || file == "/run/pki-bootstrap/bootstrap.csr" && environmentPreflightBytesSHA256(raw) != r.OriginalCSRSHA || file == r.RenewedCertPath && string(raw) != material["tls.crt"] {
			return managedCoreObservationBlock("original-key-csr-custody")
		}
	}
	var kube struct {
		Clusters []struct {
			Cluster map[string]any `yaml:"cluster"`
		} `yaml:"clusters"`
	}
	if yaml.Unmarshal(b.KubeconfigContent, &kube) != nil || len(kube.Clusters) != 1 || kube.Clusters[0].Cluster["certificate-authority-data"] != observed.Cleanup.KubernetesCA || observed.Cleanup.SecretUID != r.Ceremony.CredentialUIDs[r.Ceremony.BootstrapCredentialNamespace] {
		return managedCoreObservationBlock("original-cleanup-cluster")
	}
	namespace, err := b.Read(ctx, "Namespace//"+r.Ceremony.BootstrapCredentialNamespace)
	if err != nil || namespace == nil || deploymentplan.Map(namespace["metadata"])["uid"] != observed.Cleanup.NamespaceUID {
		return managedCoreObservationBlock("original-cleanup-namespace")
	}
	clients, err := readManagedSessionClients(ctx, cfg, runtime, r.Ceremony, subjects)
	if err != nil || clients != r.ManagedTransportRecovery.Partial.ClientsSHA {
		return managedCoreObservationBlock("original-eleven-clients")
	}
	roots := map[string]environmentCreateRegisteredIssuer{"mqtt": *r.MQTTRoot, "openbao_tls": *r.OpenBaoRoot}
	issuers := map[string]environmentCreateRegisteredIssuer{"mqtt": *r.MQTTIntermediate, "openbao_tls": *r.OpenBaoIntermediate}
	servers, err := readManagedTransportServers(ctx, cfg, runtime, p.Plan.Bootstrap, roots, issuers)
	if err != nil {
		return err
	}
	if err := bindManagedCoreOriginalHostServerPin(r.ManagedTransportRecovery.Partial, map[string]string{"accepted-service-core-managed-application-current-servers": originalHostPin}, servers); err != nil {
		return err
	}
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, "SELECT COALESCE(json_agg(json_build_object('domain',domain,'request_id',request_id) ORDER BY domain),'[]'::json) FROM pki_server_issuances WHERE environment='"+cfg.Environment+"' AND domain IN ('mqtt','openbao_tls')")
	var requests []struct {
		Domain  string `json:"domain"`
		Request string `json:"request_id"`
	}
	if err != nil || decodeEnvironmentJSON(raw, &requests) != nil || len(requests) != 2 || requests[0].Domain != "mqtt" || requests[0].Request != state.Session+":emqx-pki:server" || requests[1].Domain != "openbao_tls" || requests[1].Request != state.Session+":openbao:server" {
		return managedCoreObservationBlock("two-original-server-requests")
	}
	p.Materials["accepted-service-core-managed-complete-current-session"] = deploymentplan.Digest(descriptor)
	p.Materials["accepted-service-core-managed-application-current-clients"] = clients
	p.Materials["accepted-service-core-managed-application-current-servers"] = servers
	p.Materials["accepted-service-core-managed-complete-original-key"] = r.OriginalKeySHA
	p.Materials["accepted-service-core-managed-complete-original-csr"] = r.OriginalCSRSHA
	return nil
}

// Source materials must already come from the exact authenticated historical
// admission. An empty runtime field is expected after pure derivation; a
// populated incompatible field is rejected, and a changed current identity can
// never become its own original pin.
func bindManagedCoreOriginalHostServerPin(partial *createManagedTransportPartialRecovery, originalMaterials map[string]string, current string) error {
	if partial == nil {
		return managedCoreObservationBlock("original-host-inventory-source")
	}
	original := originalMaterials["accepted-service-core-managed-application-current-servers"]
	if partial.ServerSHA != "" && partial.ServerSHA != original {
		return managedCoreObservationBlock("original-host-inventory-runtime-drift")
	}
	if err := validateManagedCoreOriginalHostServerDigest(current, original); err != nil {
		return err
	}
	partial.ServerSHA = original
	return nil
}

func validateManagedCoreOriginalHostServerDigest(current, original string) error {
	if !environmentPreflightDigest.MatchString(current) || !environmentPreflightDigest.MatchString(original) || current != original {
		return managedCoreObservationBlock("unchanged-original-host-csr-certificates")
	}
	return nil
}

// The authenticated Ready CertIssuer is only a carrier for the native
// SELECT-only policy renderer. Before repair this is the captured baseline;
// after repair it is the immutable accepted child. Its recipe, database Secret
// and owned Pod lineage remain pinned; no credential is copied into an argument
// or another workload.
func managedCoreRecordedAuthentication(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery) (string, string, string, error) {
	bad := func() (string, string, string, error) {
		return "", "", "", managedCoreObservationBlock("original-native-policy-carrier")
	}
	if r == nil || r.ManagedCorePartial == nil || r.ManagedTransportRecovery == nil || r.ManagedTransportRecovery.Partial == nil || r.Intent.SessionID != r.Ceremony.BootstrapSessionID || e == nil || e.Check(ctx) != nil {
		return bad()
	}
	if _, err := managedCoreAuthenticationSource(ctx, store, e, r); err != nil {
		return "", "", "", err
	}
	return observeManagedCoreRecordedAuthentication(ctx, cfg, store, e, b, r.ManagedCorePartial, r.Ceremony, r.Intent.SessionID)
}

// Historical/admission authentication stays in the caller. This native reader
// binds its explicit stopped Core model to the original database receipt,
// controller descendants and readonly policy response, then rereads those owners.
func observeManagedCoreRecordedAuthentication(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, core *createManagedCorePartialRecovery, service environmentCreateServiceCeremony, session string) (string, string, string, error) {
	bad := func() (string, string, string, error) {
		return "", "", "", managedCoreObservationBlock("original-native-policy-carrier")
	}
	if ctx == nil || e == nil || core == nil || validateManagedCorePartialJournal(core.Plan, core.Journal) != nil || cfg.Environment != e.Journal.Environment || store.Root != e.Store.Root || store.Environment != cfg.Environment || b.Store.Root != store.Root || core.Plan.Environment != cfg.Environment || core.Plan.Stack != e.Journal.Stack || !environmentBootstrapOwnerUUID.MatchString(session) || session != service.BootstrapSessionID || !environmentBootstrapOwnerUUID.MatchString(service.Intermediate.ID) || e.Check(ctx) != nil {
		return bad()
	}
	resource, carrier, err := managedCorePolicyCarrier(store, e, core)
	if err != nil {
		return bad()
	}
	actual, err := b.Read(ctx, resource.ID)
	if err != nil || validateManagedCorePolicyCarrierResource(core, resource, carrier, actual) != nil {
		return bad()
	}
	var databaseAlias map[string]string
	aliasCount := 0
	for _, entry := range e.Journal.Entries {
		if entry.Key == "kubernetes/database-runtime-secrets" {
			aliasCount++
			if entry.State != "completed" || entry.CompletedAt.IsZero() || json.Unmarshal(entry.Receipt, &databaseAlias) != nil {
				return bad()
			}
		}
	}
	stage := databaseAlias["actual_retained_verification_stage"]
	review := strings.TrimPrefix(stage, "kubernetes/database-secrets-recovery-")
	if aliasCount != 1 || stage != "kubernetes/database-secrets-recovery-"+review || !environmentPreflightDigest.MatchString(review) || !environmentPreflightDigest.MatchString(databaseAlias["original_plan_sha256"]) || !environmentPreflightDigest.MatchString(databaseAlias["original_failed_journal_sha256"]) {
		return bad()
	}
	var originalDatabasePlan deploymentplan.Plan
	var originalDatabaseJournal deploymentplan.Journal
	directory := filepath.Join(store.Root, "deployment/rollback", databaseAlias["original_plan_sha256"])
	if readExecutionJSON(filepath.Join(directory, "plan.json"), &originalDatabasePlan) != nil || readExecutionJSON(filepath.Join(directory, "journal.json"), &originalDatabaseJournal) != nil || originalDatabasePlan.SHA256() != databaseAlias["original_plan_sha256"] || deploymentplan.Digest(originalDatabaseJournal) != databaseAlias["original_failed_journal_sha256"] {
		return bad()
	}
	credentialPlan, credentialJournal, err := acceptedCreateStage(store, e, stage)
	if err != nil {
		return bad()
	}
	secretID := "Secret/" + e.Journal.Stack + "-video-cloud/pki-issuer-database"
	secretBound := false
	var originalDatabaseResource deploymentplan.Resource
	var originalDatabaseUID string
	for _, original := range credentialPlan.Resources {
		if original.ID == secretID {
			if originalDatabaseJournal.Owners[secretID] == "" || originalDatabaseJournal.Owners[secretID] != credentialJournal.Owners[secretID] || original.UID != credentialJournal.Owners[secretID] {
				return bad()
			}
			secret, err := b.Read(ctx, secretID)
			if err != nil || secret == nil || deploymentplan.Map(secret["metadata"])["uid"] != credentialJournal.Owners[secretID] || deploymentplan.Map(secret["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(original.Desired, secret) {
				return bad()
			}
			secretBound = true
			originalDatabaseResource, originalDatabaseUID = original, credentialJournal.Owners[secretID]
		}
	}
	if !secretBound {
		return bad()
	}
	ready := resource
	ready.Action, ready.Desired = "retain", carrier
	bounded, cancel := context.WithTimeout(ctx, 30*time.Second)
	err = b.Wait(bounded, ready)
	cancel()
	if err != nil {
		return bad()
	}
	raw, err := b.command(ctx, nil, "--namespace", e.Journal.Stack+"-video-cloud", "get", "pods", "-l", "app.kubernetes.io/name=certissuer", "-o", "json")
	var pods struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if err != nil || json.Unmarshal(raw, &pods) != nil || len(pods.Items) != 1 {
		return bad()
	}
	pod := pods.Items[0]
	metadata := deploymentplan.Map(pod["metadata"])
	refs := certIssuerObjectList(metadata["ownerReferences"])
	if !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(metadata["uid"])) || metadata["deletionTimestamp"] != nil || len(refs) != 1 {
		return bad()
	}
	ref := deploymentplan.Map(refs[0])
	if ref["kind"] != "ReplicaSet" || ref["controller"] != true || !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(ref["uid"])) {
		return bad()
	}
	rs, err := b.Read(ctx, "ReplicaSet/"+e.Journal.Stack+"-video-cloud/"+deploymentplan.String(ref["name"]))
	rsmeta := deploymentplan.Map(rs["metadata"])
	owners := certIssuerObjectList(rsmeta["ownerReferences"])
	if err != nil || rs == nil || rsmeta["uid"] != ref["uid"] || rsmeta["deletionTimestamp"] != nil || len(owners) != 1 {
		return bad()
	}
	owner := deploymentplan.Map(owners[0])
	if owner["kind"] != "Deployment" || owner["controller"] != true || owner["uid"] != resource.UID || owner["name"] != "certissuer" {
		return bad()
	}
	expected := deploymentplan.Object{"kind": "Deployment", "spec": deploymentplan.Object{"template": deploymentplan.Map(carrier["spec"])["template"]}}
	observed := deploymentplan.Object{"kind": "Deployment", "spec": deploymentplan.Object{"template": deploymentplan.Map(rs["spec"])["template"]}}
	if !deploymentplan.Matches(expected, observed) {
		return bad()
	}
	script := `export PKI_DATABASE_URL="$CERT_ISSUER_DB_DSN"
export PGOPTIONS='-c default_transaction_read_only=on'
exec /app/pkicontroller render-openbao-policy "$1"`
	raw, err = b.command(ctx, nil, "--namespace", e.Journal.Stack+"-video-cloud", "exec", "pod/"+deploymentplan.String(metadata["name"]), "--container", "certissuer", "--", "/bin/sh", "-ec", script, "original-native-read-policy", service.Intermediate.ID)
	var native environmentCreateOpenBaoIssuerPolicies
	if err != nil || decodeEnvironmentJSON(raw, &native) != nil || native.IssuerID != service.Intermediate.ID || native.Environment != cfg.Environment || native.ServiceClientSignerPolicy == "" {
		return bad()
	}
	name := "rtk-" + cfg.Environment + "-service-bootstrap-" + session
	role := "pki-service-bootstrap-" + cfg.Environment
	intent := deploymentplan.Object{"role": role, "namespace": e.Journal.Stack + "-video-cloud", "service_accounts": []string{"pki-service-bootstrap"}, "policies": []string{name}, "audience": "openbao", "ttl": "15m", "no_default_policy": true}
	policyBound, roleBound := false, false
	for _, entry := range e.Journal.Entries {
		var receipt deploymentplan.Object
		if entry.State != "completed" || json.Unmarshal(entry.Receipt, &receipt) != nil {
			continue
		}
		if entry.Key == "openbao-policy-"+name && receipt["environment"] == cfg.Environment && receipt["policy"] == name && receipt["policy_sha256"] == environmentPreflightBytesSHA256([]byte(native.ServiceClientSignerPolicy)) {
			policyBound = true
		}
		if entry.Key == "openbao-role-"+role+"-"+deploymentplan.Digest(intent)[:12] && deploymentplan.Digest(receipt) == deploymentplan.Digest(intent) {
			roleBound = true
		}
	}
	if !policyBound || !roleBound {
		return bad()
	}
	readback, err := b.Read(ctx, resource.ID)
	if err != nil || deploymentplan.Digest(deploymentplan.Projection(readback)) != deploymentplan.Digest(deploymentplan.Projection(actual)) {
		return bad()
	}
	secretAgain, secretErr := b.Read(ctx, secretID)
	podAgain, podErr := b.Read(ctx, "Pod/"+e.Journal.Stack+"-video-cloud/"+deploymentplan.String(metadata["name"]))
	if secretErr != nil || secretAgain == nil || deploymentplan.Map(secretAgain["metadata"])["uid"] != originalDatabaseUID || !deploymentplan.Matches(originalDatabaseResource.Desired, secretAgain) || podErr != nil || podAgain == nil || deploymentplan.Map(podAgain["metadata"])["uid"] != metadata["uid"] || deploymentplan.Digest(deploymentplan.Projection(podAgain)) != deploymentplan.Digest(deploymentplan.Projection(pod)) {
		return bad()
	}
	return name, role, native.ServiceClientSignerPolicy, e.Check(ctx)
}

func managedCorePolicyCarrier(store secretStore, e *environmentCreateExecution, core *createManagedCorePartialRecovery) (deploymentplan.Resource, deploymentplan.Object, error) {
	var empty deploymentplan.Resource
	if core == nil || e == nil || validateManagedCorePartialJournal(core.Plan, core.Journal) != nil {
		return empty, nil, managedCoreObservationBlock("native-carrier-source")
	}
	resource := core.Plan.Resources[14]
	stage := "kubernetes/managed-complete-issuer-forward-" + core.ForwardReview
	found := false
	for _, entry := range e.Journal.Entries {
		if entry.Key == stage {
			found = true
			break
		}
	}
	if !found {
		return resource, resource.Baseline, nil
	}
	plan, journal, err := managedTransportPostAcceptedStage(store, e, stage)
	if err != nil || validateManagedCorePartialCandidate(core, plan) != nil || !createOpenBaoACKCompletedChild(plan, journal) {
		return empty, nil, managedCoreObservationBlock("native-carrier-accepted-child")
	}
	resource = plan.Resources[14]
	if journal.Owners[resource.ID] != resource.UID {
		return empty, nil, managedCoreObservationBlock("native-carrier-accepted-owner")
	}
	return resource, resource.Desired, nil
}

func validateManagedCorePolicyCarrierResource(core *createManagedCorePartialRecovery, resource deploymentplan.Resource, carrier, actual deploymentplan.Object) error {
	if core == nil || validateManagedCorePartialJournal(core.Plan, core.Journal) != nil {
		return managedCoreObservationBlock("native-carrier-source")
	}
	metadata := deploymentplan.Map(actual["metadata"])
	expected := deploymentplan.Clone(carrier)
	deploymentplan.Map(expected["metadata"])["uid"] = resource.UID
	if resource.ID != "Deployment/video-cloud-dev-video-cloud/certissuer" || actual == nil || deploymentplan.ID(actual) != resource.ID || metadata["uid"] != resource.UID || metadata["deletionTimestamp"] != nil || !deploymentplan.Matches(carrier, actual) || deploymentplan.Digest(deploymentplan.Projection(actual)) != deploymentplan.Digest(deploymentplan.Projection(expected)) {
		return managedCoreObservationBlock("native-carrier-owner")
	}
	container := mqttFoundationPrimary(carrier)
	if container["name"] != "certissuer" || mqttFoundationEnv(container, "PKI_ENVIRONMENT") != "dev" {
		return managedCoreObservationBlock("native-carrier-environment")
	}
	db := 0
	for _, value := range certIssuerObjectList(container["env"]) {
		setting := deploymentplan.Map(value)
		if setting["name"] == "PKI_DATABASE_URL" {
			return managedCoreObservationBlock("native-carrier-no-database-override")
		}
		if setting["name"] == "CERT_ISSUER_DB_DSN" {
			db++
			ref := deploymentplan.Map(deploymentplan.Map(setting["valueFrom"])["secretKeyRef"])
			if ref["name"] != "pki-issuer-database" || ref["key"] != "url" || setting["value"] != nil {
				return managedCoreObservationBlock("native-carrier-database-reference")
			}
		}
	}
	if db != 1 {
		return managedCoreObservationBlock("native-carrier-one-database-reference")
	}
	return nil
}

const managedCoreRoutingFileMaterial = "accepted-service-core-managed-complete-app-routing-file"
const managedCoreRoutingProofMaterial = "accepted-service-core-managed-complete-app-routing-proof"

type managedCoreRoutingPublication struct {
	FileSHA, RoutingPlanSHA, RoutingJournalSHA             string
	AppsPlanSHA, AppsJournalSHA, ParentProofSHA, NativeSHA string
}

func validateManagedCoreRoutingPublication(core *createManagedCorePartialRecovery, parent environmentCreateJournal, raw []byte, actual deploymentplan.Object) (managedCoreRoutingPublication, error) {
	var empty managedCoreRoutingPublication
	if core == nil || core.Routing == nil || core.Apps == nil || len(core.Routing.Resources) != 1 || len(core.Apps.Resources) != 26 || core.Routing.Environment != parent.Environment || core.Routing.Stack != parent.Stack || core.Apps.Environment != parent.Environment || core.Apps.Stack != parent.Stack || core.RoutingJournal.PlanSHA256 != core.Routing.SHA256() || !core.RoutingJournal.Accepted || core.RoutingJournal.Failed != "" || core.RoutingJournal.InFlight != "" || len(core.RoutingJournal.Completed) != 1 || len(core.RoutingJournal.Owners) != 1 || core.AppsJournal.PlanSHA256 != core.Apps.SHA256() || !core.AppsJournal.Accepted || core.AppsJournal.Failed != "" || core.AppsJournal.InFlight != "" || len(core.AppsJournal.Completed) != 26 || len(core.AppsJournal.Owners) != 26 {
		return empty, managedCoreObservationBlock("accepted-app-routing-plans")
	}
	var binding environmentAppRoutingBinding
	ns := parent.Stack + "-video-cloud"
	id := "ConfigMap/" + ns + "/" + environmentAppRoutingDeclaration
	resource := core.Routing.Resources[0]
	if decodeEnvironmentJSON(raw, &binding) != nil || binding.Schema != "rtk-cloud.native-app-routing/v1" || binding.Environment != parent.Environment || binding.Stack != parent.Stack || binding.Mode != "internal-service" || binding.CreatePlanSHA256 != parent.PlanSHA256 || !environmentPreflightDigest.MatchString(binding.CreatePlanSHA256) || !environmentBootstrapOwnerUUID.MatchString(binding.DeclarationUID) || !environmentBootstrapOwnerUUID.MatchString(binding.ServiceUID) || !environmentBootstrapOwnerUUID.MatchString(binding.DeploymentUID) || binding.DeclarationUID != core.RoutingJournal.Owners[id] || binding.ServiceUID != core.AppsJournal.Owners["Service/"+ns+"/video-cloud-api-app-pki"] || binding.DeploymentUID != core.AppsJournal.Owners["Deployment/"+ns+"/video-cloud-api-app-pki"] || resource.ID != id || core.RoutingJournal.Completed[0] != id || !deploymentplan.Matches(environmentAppRoutingDefinition(binding).Object, resource.Desired) {
		return empty, managedCoreObservationBlock("original-app-routing-binding")
	}
	var entry environmentCreateJournalEntry
	count := 0
	for _, candidate := range parent.Entries {
		if candidate.Key == "native-app-routing-profile-proof" {
			entry = candidate
			count++
		}
	}
	if count != 1 || entry.Phase != "bootstrap" || entry.State != "completed" || entry.CompletedAt.IsZero() || entry.IntentSHA256 != activeTransportJournalDigest(binding) || activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(binding) {
		return empty, managedCoreObservationBlock("original-app-routing-parent-proof")
	}
	if actual == nil || deploymentplan.ID(actual) != id || deploymentplan.Map(actual["metadata"])["uid"] != binding.DeclarationUID || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(resource.Desired, actual) {
		return empty, managedCoreObservationBlock("original-app-routing-native-owner")
	}
	return managedCoreRoutingPublication{FileSHA: environmentPreflightBytesSHA256(raw), RoutingPlanSHA: core.Routing.SHA256(), RoutingJournalSHA: deploymentplan.Digest(core.RoutingJournal), AppsPlanSHA: core.Apps.SHA256(), AppsJournalSHA: deploymentplan.Digest(core.AppsJournal), ParentProofSHA: deploymentplan.Digest(entry), NativeSHA: deploymentplan.Digest(deploymentplan.Projection(actual))}, nil
}

func readManagedCoreRoutingPublication(ctx context.Context, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, core *createManagedCorePartialRecovery) (managedCoreRoutingPublication, error) {
	var empty managedCoreRoutingPublication
	if managedCoreObservationSource(store, e, core) != nil || b.Store.Root != store.Root || e.Check(ctx) != nil {
		return empty, managedCoreObservationBlock("original-app-routing-source")
	}
	path, err := store.safePath("pki/app/routing.json")
	if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
		return empty, managedCoreObservationBlock("original-app-routing-private-path")
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil {
		return empty, managedCoreObservationBlock("original-app-routing-file")
	}
	actual, err := b.Read(ctx, "ConfigMap/"+e.Journal.Stack+"-video-cloud/"+environmentAppRoutingDeclaration)
	if err != nil {
		return empty, managedCoreObservationBlock("original-app-routing-native-read")
	}
	return validateManagedCoreRoutingPublication(core, e.Journal, raw, actual)
}
