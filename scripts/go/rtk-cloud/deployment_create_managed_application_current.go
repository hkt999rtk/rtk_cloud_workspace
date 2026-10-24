package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedApplicationCurrentBlock(stage string) error {
	return createProviderRecoveryBlock("managed application current original custody differs: stage=" + stage)
}

// A written API is not an accepted API. Select only the original failed child or
// its authenticated interrupted forward; never fabricate a historical failure.
// The authenticated unstarted tail retains its original baselines. Only the
// exact completed prefix is required to remain ready.
func validateManagedApplicationCurrentOwner(partial *createManagedApplicationPartialRecovery, index int, actual deploymentplan.Object) error {
	plan, journal, err := managedApplicationPartialCurrentPlan(partial)
	if err != nil || index < 0 || index >= len(plan.Resources) {
		return managedApplicationCurrentBlock("bounded-original-child")
	}
	resource := plan.Resources[index]
	expected, owner := resource.Desired, journal.Owners[resource.ID]
	if index > len(journal.Completed) {
		expected, owner = resource.Baseline, resource.UID
	}
	metadata := deploymentplan.Map(actual["metadata"])
	if actual == nil || owner == "" || deploymentplan.ID(actual) != resource.ID || metadata["uid"] != owner || metadata["deletionTimestamp"] != nil || !deploymentplan.Matches(expected, actual) {
		return managedApplicationCurrentBlock("owner-or-body-" + resource.ID)
	}
	if index > len(journal.Completed) && deploymentplan.Digest(deploymentplan.Projection(actual)) != deploymentplan.Digest(resource.Baseline) {
		return managedApplicationCurrentBlock("untouched-baseline-" + resource.ID)
	}
	return nil
}

func verifyManagedApplicationPartialCurrent(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan) error {
	if r == nil || r.ManagedApplicationPartial == nil || r.ManagedTransportRecovery == nil || r.ManagedTransportRecovery.Partial == nil || r.MQTTRoot == nil || r.MQTTIntermediate == nil || r.OpenBaoRoot == nil || r.OpenBaoIntermediate == nil || e == nil || p == nil || p.Materials == nil || cfg.Environment != "dev" || cfg.Values["CLOUD_STACK_NAME"] != e.Journal.Stack || store.Environment != cfg.Environment || store.Root != e.Store.Root || b.Store.Root != store.Root {
		return managedApplicationCurrentBlock("authenticated-partial")
	}
	partial := r.ManagedApplicationPartial
	plan, journal, err := managedApplicationPartialCurrentPlan(partial)
	if err != nil {
		return err
	}
	if err := verifyManagedApplicationCurrentExecutionBound(ctx, partial, p.Materials); err != nil {
		return err
	}
	owners := map[string]string{}
	for index, resource := range plan.Resources {
		actual, err := b.Read(ctx, resource.ID)
		if err != nil || validateManagedApplicationCurrentOwner(partial, index, actual) != nil {
			return managedApplicationCurrentBlock("owner-or-body-" + resource.ID)
		}
		owners[resource.ID] = deploymentplan.Digest(deploymentplan.Projection(actual))
		if index < len(journal.Completed) {
			retained := resource
			retained.Action, retained.UID = "retain", journal.Owners[resource.ID]
			ready, cancel := context.WithTimeout(ctx, 30*time.Second)
			err = b.Wait(ready, retained)
			cancel()
			if err != nil {
				return managedApplicationCurrentBlock("completed-owner-ready-" + resource.ID)
			}
		}
	}
	if err := verifyManagedApplicationCurrentVolumes(ctx, cfg, e, b, r, p); err != nil {
		return err
	}
	if err := verifyManagedApplicationCurrentExports(ctx, e, b, partial, p.Materials); err != nil {
		return err
	}
	if err := verifyManagedApplicationCurrentAPIServers(ctx, cfg, e, b, r, p.Materials); err != nil {
		return err
	}
	if err := verifyManagedApplicationCurrentSession(ctx, cfg, store, e, b, r, p); err != nil {
		return err
	}
	if err := verifyManagedTransportNativeRegistry(ctx, cfg, store, e, b, p); err != nil {
		return err
	}
	authentication := *r
	authentication.Intent = r.RenewedIntent
	if err := verifyManagedTransportRetainedAuthentication(ctx, cfg, store, e, b, &authentication); err != nil {
		return err
	}
	// Native readiness and crypto checks cannot authorize a concurrently replaced
	// owner. Re-read every original body before admitting its forward candidate.
	for index, resource := range plan.Resources {
		actual, err := b.Read(ctx, resource.ID)
		if err != nil || validateManagedApplicationCurrentOwner(partial, index, actual) != nil || deploymentplan.Digest(deploymentplan.Projection(actual)) != owners[resource.ID] {
			return managedApplicationCurrentBlock("owner-changed-during-verification-" + resource.ID)
		}
	}
	p.Materials["accepted-service-core-managed-application-current-owners"] = deploymentplan.Digest(owners)
	if err := qualifyManagedApplicationInterruptedLease(ctx, e, b, r, p); err != nil {
		return err
	}
	if err := qualifyManagedHistoricalSeedCleanup(ctx, cfg, store, e, b, r, p); err != nil {
		return err
	}
	return e.Check(ctx)
}

func verifyManagedApplicationCurrentVolumes(ctx context.Context, cfg deploymentConfig, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan) error {
	ledger, err := readEnvironmentCreateLedger(e)
	var active managedTransportVolumeLedgerActive
	if err != nil || readExecutionJSON(filepath.Join(e.Store.Root, "deployment/resources/active.json"), &active) != nil || validateManagedTransportVolumeLedger(ledger, active) != nil {
		return managedApplicationCurrentBlock("current-volume-ledger")
	}
	p.Materials["accepted-service-core-managed-application-current-volume-ledger"] = active.LedgerSHA
	p.Materials["accepted-service-core-managed-application-current-volume-active"] = deploymentplan.Digest(active)
	partial := r.ManagedApplicationPartial
	for _, id := range []string{"PersistentVolumeClaim/" + e.Journal.Stack + "-video-cloud/mqtt-pki-data", "PersistentVolumeClaim/" + e.Journal.Stack + "-video-cloud/mqtt-pki-trust", "PersistentVolumeClaim/" + e.Journal.Stack + "-video-cloud/mqtt-pki-host-identity", "PersistentVolumeClaim/" + e.Journal.Stack + "-secrets/openbao-pki-identity", "PersistentVolumeClaim/" + e.Journal.Stack + "-secrets/data-openbao-0", "PersistentVolumeClaim/" + e.Journal.Stack + "-secrets/audit-openbao-0"} {
		owner, err := managedTransportRecordedHostVolume(ledger, id)
		if err != nil || !environmentPreflightDigest.MatchString(p.Materials["accepted-service-core-managed-partial-original-volume-"+id]) || deploymentplan.Digest(owner) != p.Materials["accepted-service-core-managed-partial-original-volume-"+id] {
			return managedApplicationCurrentBlock("original-volume-pin-" + id)
		}
		claim, claimErr := b.Read(ctx, id)
		volume, volumeErr := b.Read(ctx, "PersistentVolume//"+owner.PVName)
		if claimErr != nil || volumeErr != nil || validateManagedTransportHostVolume(owner, claim, volume) != nil {
			return managedApplicationCurrentBlock("original-volume-native-" + id)
		}
		r.ManagedTransportRecovery.Partial.PVCs[id] = owner.PVCUID
	}
	// These allocations are genuine completed child writes. They can precede
	// the ordinary stage's ledger capture; inspect them without rewriting or
	// inventing an earlier ledger timestamp.
	for _, resource := range partial.Plan.Resources[:2] {
		claim, err := b.Read(ctx, resource.ID)
		if err != nil {
			return managedApplicationCurrentBlock("new-trust-claim-" + resource.ID)
		}
		pvName := deploymentplan.String(deploymentplan.Map(claim["spec"])["volumeName"])
		if pvName == "" {
			return managedApplicationCurrentBlock("new-trust-bound-" + resource.ID)
		}
		volume, err := b.Read(ctx, "PersistentVolume//"+pvName)
		if err != nil {
			return managedApplicationCurrentBlock("new-trust-volume-" + resource.ID)
		}
		owner, err := validateManagedApplicationNewTrustVolume(ledger, partial, resource, claim, volume)
		if err != nil {
			return err
		}
		partial.NewTrustVolumes[resource.ID] = owner
		p.Materials["accepted-service-core-managed-application-new-trust-volume-"+resource.ID] = deploymentplan.Digest(owner)
	}
	return nil
}

func validateManagedApplicationNewTrustVolume(ledger environmentCreateResourceLedger, partial *createManagedApplicationPartialRecovery, resource deploymentplan.Resource, claim, volume deploymentplan.Object) (environmentCreateVolumeOwner, error) {
	bad := func() (environmentCreateVolumeOwner, error) {
		return environmentCreateVolumeOwner{}, managedApplicationCurrentBlock("genuine-new-trust-volume-" + resource.ID)
	}
	if partial == nil || partial.Plan == nil || len(partial.Plan.Resources) != 26 || resource.Action != "create" || resource.UID != "" || resource.Baseline != nil || resource.ID != partial.Plan.Resources[0].ID && resource.ID != partial.Plan.Resources[1].ID || partial.Journal.Owners[resource.ID] == "" {
		return bad()
	}
	index := 0
	if resource.ID == partial.Plan.Resources[1].ID {
		index = 1
	}
	original := *partial
	original.Interrupted = nil
	if validateManagedApplicationCurrentOwner(&original, index, claim) != nil {
		return bad()
	}
	meta, spec := deploymentplan.Map(volume["metadata"]), deploymentplan.Map(volume["spec"])
	native := deploymentplan.String(deploymentplan.Map(spec["csi"])["volumeHandle"])
	if prefix, suffix, ok := strings.Cut(native, "-"); ok && strings.HasPrefix(suffix, "pvc-") {
		native = prefix
	}
	claimMeta := deploymentplan.Map(claim["metadata"])
	owner := environmentCreateVolumeOwner{NativeID: native, PVName: deploymentplan.String(meta["name"]), PVUID: deploymentplan.String(meta["uid"]), PVCName: deploymentplan.String(claimMeta["name"]), PVCUID: partial.Journal.Owners[resource.ID], Namespace: partial.Plan.Stack + "-video-cloud"}
	if _, err := positiveIntValue("volume_id", owner.NativeID); err != nil || owner.PVName == "" || !environmentBootstrapOwnerUUID.MatchString(owner.PVUID) || validateManagedTransportHostVolume(owner, claim, volume) != nil {
		return bad()
	}
	seen := 0
	for _, previous := range ledger.Volumes {
		if previous.Namespace == owner.Namespace && previous.PVCName == owner.PVCName {
			seen++
			if previous != owner {
				return bad()
			}
		} else if previous.NativeID == owner.NativeID || previous.PVCUID == owner.PVCUID || previous.PVUID == owner.PVUID || previous.PVName == owner.PVName {
			return bad()
		}
	}
	if seen > 1 {
		return bad()
	}
	return owner, nil
}

func verifyManagedApplicationCurrentExports(ctx context.Context, e *environmentCreateExecution, b environmentKubernetesBackend, partial *createManagedApplicationPartialRecovery, materials map[string]string) error {
	if len(partial.Exports) != 2 {
		return managedApplicationCurrentBlock("two-original-api-exports")
	}
	for _, name := range []string{"video-cloud-api-pki", "video-cloud-api-app-pki"} {
		proof := partial.Exports[name]
		plan, journal := partial.AcceptedPlans["kubernetes/server-secret-"+name], partial.AcceptedJournals["kubernetes/server-secret-"+name]
		if proof == nil || plan == nil || len(plan.Resources) != 1 {
			return managedApplicationCurrentBlock("serving-secret-proof-" + name)
		}
		resource := plan.Resources[0]
		actual, err := b.Read(ctx, resource.ID)
		if err != nil || actual == nil || journal.Owners[resource.ID] == "" || deploymentplan.Map(actual["metadata"])["uid"] != journal.Owners[resource.ID] || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(resource.Desired, actual) {
			return managedApplicationCurrentBlock("serving-secret-native-" + name)
		}
		claimID := "PersistentVolumeClaim/" + e.Journal.Stack + "-video-cloud/" + environmentManagedApplicationClaim(name)
		ledger, err := readEnvironmentCreateLedger(e)
		if err != nil {
			return managedApplicationCurrentBlock("original-api-volume-ledger-" + name)
		}
		owner, ownerErr := managedTransportRecordedHostVolume(ledger, claimID)
		if ownerErr != nil {
			return managedApplicationCurrentBlock("original-api-volume-owner-" + name)
		}
		claim, claimErr := b.Read(ctx, claimID)
		if claimErr != nil || claim == nil {
			return managedApplicationCurrentBlock("original-api-volume-claim-read-" + name)
		}
		volume, volumeErr := b.Read(ctx, "PersistentVolume//"+owner.PVName)
		if volumeErr != nil || volume == nil {
			return managedApplicationCurrentBlock("original-api-volume-pv-read-" + name)
		}
		if owner.PVCUID != proof.ClaimUID {
			return managedApplicationCurrentBlock("original-api-volume-proof-" + name)
		}
		if validateManagedTransportHostVolume(owner, claim, volume) != nil {
			return managedApplicationCurrentBlock("original-api-volume-binding-" + name)
		}
		reader, readerErr := b.Read(ctx, "Deployment/"+e.Journal.Stack+"-video-cloud/pki-export-"+name+"-"+e.Journal.PlanSHA256[:12])
		if readerErr != nil {
			return managedApplicationCurrentBlock("retired-original-reader-read-" + name)
		}
		if reader != nil {
			return managedApplicationCurrentBlock("retired-original-reader-present-" + name)
		}
		materials["accepted-service-core-managed-application-serving-secret-"+name] = deploymentplan.Digest(deploymentplan.Projection(actual))
		materials["accepted-service-core-managed-application-api-volume-"+name] = deploymentplan.Digest(owner)
	}
	return nil
}

func environmentManagedApplicationClaim(name string) string {
	for _, owner := range environmentCreateApplicationOwners() {
		if owner.Name == name {
			return owner.Claim
		}
	}
	return ""
}

func verifyManagedApplicationCurrentSession(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, p *createFoundationRecoveryPlan) error {
	runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: b}
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil {
		return err
	}
	state, raw, err := readManagedSessionState(ctx, cfg, runtime, r.Ceremony.BootstrapSessionID)
	var observed createServiceSessionObserved
	path := filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-renewal", r.ManagedTransportRecovery.Partial.SourceReview, "observed.json")
	var saved managedSessionState
	lifetime, lifetimeErr := managedApplicationCurrentSessionLifetime(r.ManagedApplicationPartial)
	if err != nil || lifetimeErr != nil || readExecutionJSON(path, &observed) != nil || json.Unmarshal(observed.Session, &saved) != nil || !managedApplicationSessionCurrentWithLifetime(state, saved, r.Ceremony, subjects, time.Now().UTC(), lifetime) {
		return managedApplicationCurrentBlock("active-original-session")
	}
	p.Materials["accepted-service-core-managed-application-session-minimum-life"] = deploymentplan.Digest(lifetime.minimumLife().String())
	material := map[string]string{}
	for _, namespace := range r.Ceremony.BootstrapCredentialNamespaces {
		secret, err := b.Read(ctx, "Secret/"+namespace+"/"+r.Ceremony.BootstrapCredentialSecret)
		if err != nil || secret == nil || deploymentplan.Map(secret["metadata"])["uid"] != r.Ceremony.CredentialUIDs[namespace] || deploymentplan.Map(secret["metadata"])["deletionTimestamp"] != nil {
			return managedApplicationCurrentBlock("original-seed-secret-owner")
		}
		values := map[string]string{}
		for _, key := range []string{"tls.crt", "tls.key", "ca.crt"} {
			decoded, err := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(deploymentplan.Map(secret["data"])[key]))
			if err != nil {
				return managedApplicationCurrentBlock("original-seed-secret-material")
			}
			values[key] = string(decoded)
		}
		if len(material) > 0 && deploymentplan.Digest(material) != deploymentplan.Digest(values) {
			return managedApplicationCurrentBlock("original-seed-secret-copies")
		}
		material = values
	}
	pair, err := tls.X509KeyPair([]byte(material["tls.crt"]), []byte(material["tls.key"]))
	if err != nil || len(pair.Certificate) < 2 || material["ca.crt"] != r.Ceremony.Root.Certificate {
		return managedApplicationCurrentBlock("original-seed-certificate")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.Subject.CommonName != "service:deployment-bootstrap" || environmentPreflightBytesSHA256(leaf.Raw) != state.Fingerprint || !leaf.NotAfter.Equal(state.Expires) || leaf.NotBefore.After(time.Now()) || environmentPreflightBytesSHA256(pair.Certificate[len(pair.Certificate)-1]) != r.Ceremony.Root.RootFingerprint {
		return managedApplicationCurrentBlock("original-seed-leaf")
	}
	for _, file := range []string{"/run/pki-bootstrap/bootstrap.key", "/run/pki-bootstrap/bootstrap.csr", r.RenewedCertPath} {
		bytes, err := b.command(ctx, nil, "--namespace", r.Ceremony.BootstrapCredentialNamespace, "exec", "deployment/"+r.Ceremony.CustodyReader, "--container", "custody", "--", "cat", file)
		if err != nil || file == "/run/pki-bootstrap/bootstrap.key" && (environmentPreflightBytesSHA256(bytes) != r.OriginalKeySHA || string(bytes) != material["tls.key"]) || file == "/run/pki-bootstrap/bootstrap.csr" && environmentPreflightBytesSHA256(bytes) != r.OriginalCSRSHA || file == r.RenewedCertPath && string(bytes) != material["tls.crt"] {
			return managedApplicationCurrentBlock("original-key-csr-leaf-custody")
		}
	}
	var kube struct {
		Clusters []struct {
			Cluster map[string]any `yaml:"cluster"`
		} `yaml:"clusters"`
	}
	if yaml.Unmarshal(b.KubeconfigContent, &kube) != nil || len(kube.Clusters) != 1 || kube.Clusters[0].Cluster["certificate-authority-data"] != observed.Cleanup.KubernetesCA || observed.Cleanup.SecretUID != r.Ceremony.CredentialUIDs[r.Ceremony.BootstrapCredentialNamespace] {
		return managedApplicationCurrentBlock("original-cleanup-cluster")
	}
	namespace, err := b.Read(ctx, "Namespace//"+r.Ceremony.BootstrapCredentialNamespace)
	if err != nil || namespace == nil || deploymentplan.Map(namespace["metadata"])["uid"] != observed.Cleanup.NamespaceUID {
		return managedApplicationCurrentBlock("original-cleanup-namespace")
	}
	clients, err := readManagedSessionClients(ctx, cfg, runtime, r.Ceremony, subjects)
	if err != nil || clients != r.ManagedTransportRecovery.Partial.ClientsSHA {
		return managedApplicationCurrentBlock("unchanged-eleven-native-clients")
	}
	roots := map[string]environmentCreateRegisteredIssuer{"mqtt": *r.MQTTRoot, "openbao_tls": *r.OpenBaoRoot}
	issuers := map[string]environmentCreateRegisteredIssuer{"mqtt": *r.MQTTIntermediate, "openbao_tls": *r.OpenBaoIntermediate}
	servers, err := readManagedTransportServers(ctx, cfg, runtime, p.Plan.Bootstrap, roots, issuers)
	if err != nil {
		return err
	}
	query := "SELECT COALESCE(json_agg(json_build_object('domain',domain,'request_id',request_id) ORDER BY domain),'[]'::json) FROM pki_server_issuances WHERE environment='" + cfg.Environment + "' AND domain IN ('mqtt','openbao_tls')"
	rows, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var requests []struct {
		Domain  string `json:"domain"`
		Request string `json:"request_id"`
	}
	if err != nil || decodeEnvironmentJSON(rows, &requests) != nil || len(requests) != 2 || requests[0].Domain != "mqtt" || requests[0].Request != state.Session+":emqx-pki:server" || requests[1].Domain != "openbao_tls" || requests[1].Request != state.Session+":openbao:server" {
		return managedApplicationCurrentBlock("original-two-server-request-ids")
	}
	r.ManagedTransportRecovery.Partial.SessionSHA, r.ManagedTransportRecovery.Partial.ServerSHA = environmentPreflightBytesSHA256(raw), servers
	p.Materials["accepted-service-core-managed-application-current-session"] = environmentPreflightBytesSHA256(raw)
	p.Materials["accepted-service-core-managed-application-current-clients"] = clients
	p.Materials["accepted-service-core-managed-application-current-servers"] = servers
	return nil
}

func managedApplicationSessionCurrent(state, saved managedSessionState, service environmentCreateServiceCeremony, subjects []string, now time.Time) bool {
	return managedApplicationSessionCurrentWithLifetime(state, saved, service, subjects, now, managedApplicationOriginalSessionLifetime)
}

type managedApplicationSessionLifetime uint8

const (
	managedApplicationOriginalSessionLifetime managedApplicationSessionLifetime = iota
	managedApplicationInterruptedSessionLifetime
	managedApplicationTrustACKSessionLifetime
	managedApplicationFinalizationSessionLifetime
)

func (profile managedApplicationSessionLifetime) minimumLife() time.Duration {
	switch profile {
	case managedApplicationOriginalSessionLifetime:
		return 2 * time.Hour
	case managedApplicationInterruptedSessionLifetime:
		return 50 * time.Minute
	case managedApplicationTrustACKSessionLifetime:
		return 25 * time.Minute
	case managedApplicationFinalizationSessionLifetime:
		return 20 * time.Minute
	default:
		return 0
	}
}

func managedApplicationCurrentSessionLifetime(partial *createManagedApplicationPartialRecovery) (managedApplicationSessionLifetime, error) {
	if _, _, err := managedApplicationPartialCurrentPlan(partial); err != nil {
		return managedApplicationOriginalSessionLifetime, err
	}
	if partial.Interrupted != nil {
		if partial.Interrupted.Previous != nil && partial.Interrupted.Previous.Previous != nil {
			return managedApplicationTrustACKSessionLifetime, nil
		}
		return managedApplicationInterruptedSessionLifetime, nil
	}
	return managedApplicationOriginalSessionLifetime, nil
}

// The third qualified continuation includes only public trust projections and
// retained native ACK installation. Its total executor is bounded independently
// of the immutable25-minute session prerequisite; clock time grants no authority.
func verifyManagedApplicationCurrentExecutionBound(ctx context.Context, partial *createManagedApplicationPartialRecovery, materials map[string]string) error {
	profile, err := managedApplicationCurrentSessionLifetime(partial)
	if err != nil {
		return err
	}
	if profile != managedApplicationTrustACKSessionLifetime {
		return nil
	}
	deadline, exists := ctx.Deadline()
	if ctx.Err() != nil || !exists || deadline.After(time.Now().Add(30*time.Minute)) || materials == nil {
		return managedApplicationCurrentBlock("bounded-trust-ack-executor")
	}
	materials["accepted-service-core-managed-application-execution-limit"] = deploymentplan.Digest((30 * time.Minute).String())
	materials["accepted-service-core-managed-application-native-bootstrap-limit"] = deploymentplan.Digest((20 * time.Minute).String())
	return nil
}

func managedApplicationSessionCurrentWithLifetime(state, saved managedSessionState, service environmentCreateServiceCeremony, subjects []string, now time.Time, lifetime managedApplicationSessionLifetime) bool {
	minimum := lifetime.minimumLife()
	if minimum <= 0 || state.Status != "active" || state.Session != service.BootstrapSessionID || state.Issuer != service.Intermediate.ID || state.Root != service.Root.RootFingerprint || state.OtherActive != 0 || !state.Expires.After(now.Add(minimum)) || !environmentPreflightDigest.MatchString(state.Fingerprint) || !slices.Equal(state.Subjects, subjects) || saved.Session != state.Session || saved.Issuer != state.Issuer || saved.Root != state.Root || saved.Fingerprint != state.Fingerprint || !saved.Expires.Equal(state.Expires) || !slices.Equal(saved.Subjects, subjects) {
		return false
	}
	seen := map[string]bool{}
	for _, subject := range state.Installed {
		if seen[subject] || !slices.Contains(subjects, subject) {
			return false
		}
		seen[subject] = true
	}
	if lifetime == managedApplicationFinalizationSessionLifetime && (len(subjects) != 9 || len(seen) != 9) {
		return false
	}
	return true
}

func verifyManagedApplicationCurrentAPIServers(ctx context.Context, cfg deploymentConfig, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, materials map[string]string, terminal ...*createServiceSessionRecovery) error {
	terminalSelected := false
	if len(terminal) != 0 {
		if len(terminal) != 1 || !managedApplicationTerminalAPISourceMatches(r, terminal[0]) || e == nil || verifyManagedLateTerminalNativeSource(ctx, cfg, e.Store, e, terminal[0]) != nil {
			return managedApplicationCurrentBlock("terminal-original-api-enrollment-source")
		}
		terminalSelected = true
	}
	if r == nil {
		return managedApplicationCurrentBlock("original-api-source")
	}
	service := r.Ceremony
	if r.ManagedLateSession != nil {
		if (!terminalSelected && !managedLateSessionNativeCheckpoint(e.Store, e) && !mqttRootCRLNativeSourceCheckpoint(e.Store, e) && !openBaoTLSRootCRLNativeSourceCheckpoint(e.Store, e)) || !environmentBootstrapOwnerUUID.MatchString(r.ManagedLateSession.OriginalEnrollmentSession) {
			return managedApplicationCurrentBlock("late-original-api-enrollment-source")
		}
		service.BootstrapSessionID = r.ManagedLateSession.OriginalEnrollmentSession
	}
	session := service.BootstrapSessionID
	if !environmentBootstrapOwnerUUID.MatchString(session) {
		return managedApplicationCurrentBlock("original-api-session")
	}
	query := "SELECT COALESCE(json_agg(json_build_object('domain',domain,'caller',caller,'request_id',request_id,'issuer_id',issuer_id,'status',status,'fingerprint',fingerprint,'csr_pem',csr_pem,'certificate_pem',certificate_pem,'dns_names',dns_names,'revoked_at',revoked_at) ORDER BY request_id),'[]'::json) FROM pki_server_issuances WHERE environment='" + cfg.Environment + "' AND domain='service' AND caller='service:video-cloud-api'"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b}, query)
	var rows []managedTransportServerEvidence
	if err != nil || decodeEnvironmentJSON(raw, &rows) != nil {
		return managedApplicationCurrentBlock("native-api-server-rows")
	}
	validate := validateManagedApplicationAPIServers
	if r.ManagedLateSession != nil {
		validate = validateManagedApplicationAPIServersPreserved
	}
	digest, err := validate(rows, service, r.ManagedApplicationPartial, time.Now().UTC())
	if err != nil {
		return err
	}
	materials["accepted-service-core-managed-application-native-api-servers"] = digest
	return nil
}

// Native SQL still uses the actual execution. Only its enrollment ancestry may
// come from the terminal profile's independently authenticated historical view.
func managedApplicationTerminalAPISourceMatches(source, current *createServiceSessionRecovery) bool {
	if source == nil || current == nil || source.ManagedLateSession == nil || current.ManagedLateSession == nil || source.ManagedApplicationPartial == nil || current.ManagedApplicationPartial == nil || current.ManagedLateRemaining == nil || current.ManagedLateRemaining.TerminalFinalizerCompletion == nil {
		return false
	}
	late := current.ManagedLateSession
	if managedLateSessionSourceDigest(source.ManagedLateSession) != managedLateSessionSourceDigest(late) || deploymentplan.Digest(source.Ceremony.Root) != deploymentplan.Digest(late.SourceCeremony.Root) || deploymentplan.Digest(source.Ceremony.Intermediate) != deploymentplan.Digest(late.SourceCeremony.Intermediate) {
		return false
	}
	// CurrentVolumes hydrates separate runtime volume maps before this call.
	// Bind precisely the immutable API custody inputs used by the validator.
	a, b := source.ManagedApplicationPartial, current.ManagedApplicationPartial
	if len(a.Exports) != 2 || len(b.Exports) != 2 {
		return false
	}
	for _, name := range []string{"video-cloud-api-pki", "video-cloud-api-app-pki"} {
		key := "kubernetes/server-secret-" + name
		if a.Exports[name] == nil || b.Exports[name] == nil || a.AcceptedPlans[key] == nil || b.AcceptedPlans[key] == nil || deploymentplan.Digest(a.Exports[name]) != deploymentplan.Digest(b.Exports[name]) || a.ExportProofSHAs[name] != b.ExportProofSHAs[name] || a.AcceptedPlans[key].SHA256() != b.AcceptedPlans[key].SHA256() || deploymentplan.Digest(a.AcceptedJournals[key]) != deploymentplan.Digest(b.AcceptedJournals[key]) {
			return false
		}
	}
	return true
}

func validateManagedApplicationAPIServers(rows []managedTransportServerEvidence, service environmentCreateServiceCeremony, partial *createManagedApplicationPartialRecovery, now time.Time) (string, error) {
	return validateManagedApplicationAPIServersMode(rows, service, partial, now, true)
}

func validateManagedApplicationAPIServersPreserved(rows []managedTransportServerEvidence, service environmentCreateServiceCeremony, partial *createManagedApplicationPartialRecovery, now time.Time) (string, error) {
	return validateManagedApplicationAPIServersMode(rows, service, partial, now, false)
}

func validateManagedApplicationAPIServersMode(rows []managedTransportServerEvidence, service environmentCreateServiceCeremony, partial *createManagedApplicationPartialRecovery, now time.Time, requireCurrent bool) (string, error) {
	bad := func() (string, error) {
		return "", managedApplicationCurrentBlock("original-native-api-server-custody")
	}
	authority, err := parseFirstCertificatePEM([]byte(service.Intermediate.Certificate))
	if err != nil || partial == nil || len(rows) != 2 || len(partial.Exports) != 2 {
		return bad()
	}
	root, err := parseFirstCertificatePEM([]byte(service.Root.Certificate))
	if err != nil || environmentPreflightBytesSHA256(root.Raw) != service.Root.RootFingerprint || authority.CheckSignatureFrom(root) != nil {
		return bad()
	}
	seen := map[string]bool{}
	for _, row := range rows {
		name := ""
		for _, owner := range []string{"video-cloud-api-pki", "video-cloud-api-app-pki"} {
			if row.Request == service.BootstrapSessionID+":"+owner+":server" {
				name = owner
			}
		}
		proof, plan := partial.Exports[name], partial.AcceptedPlans["kubernetes/server-secret-"+name]
		if name == "" || seen[name] || proof == nil || plan == nil || len(plan.Resources) != 1 || proof.RootSHA != service.Root.RootFingerprint || row.Domain != "service" || row.Caller != "service:video-cloud-api" || row.Issuer != service.Intermediate.ID || row.Status != "succeeded" || row.Revoked != nil || !slices.Equal(row.DNS, []string{proof.ServerName}) {
			return bad()
		}
		expected, err := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(deploymentplan.Map(plan.Resources[0].Desired["data"])["tls.crt"]))
		if err != nil || environmentPreflightBytesSHA256(expected) != proof.CertificateSHA {
			return bad()
		}
		currentChain, currentErr := managedApplicationPublicChain([]byte(row.Certificate))
		expectedChain, expectedErr := managedApplicationPublicChain(expected)
		// The registry stores its issued leaf; export-server joins that leaf to
		// the genuine intermediate and Root. Compare the real contracts rather
		// than requiring an exported chain in the native leaf-only SQL column.
		if currentErr != nil || expectedErr != nil || len(currentChain) != 1 || len(expectedChain) != 3 || !bytes.Equal(currentChain[0].Raw, expectedChain[0].Raw) || !bytes.Equal(expectedChain[1].Raw, authority.Raw) || !bytes.Equal(expectedChain[2].Raw, root.Raw) {
			return bad()
		}
		leaf := currentChain[0]
		validityInvalid := !leaf.NotAfter.After(leaf.NotBefore) || requireCurrent && (!leaf.NotAfter.After(now.Add(time.Hour)) || leaf.NotBefore.After(now))
		if leaf.CheckSignatureFrom(authority) != nil || validityInvalid || environmentPreflightBytesSHA256(leaf.Raw) != row.Fingerprint || !slices.Equal(leaf.DNSNames, []string{proof.ServerName}) || !slices.Contains(leaf.ExtKeyUsage, x509.ExtKeyUsageServerAuth) {
			return bad()
		}
		block, rest := pem.Decode([]byte(row.CSR))
		if block == nil || block.Type != "CERTIFICATE REQUEST" || strings.TrimSpace(string(rest)) != "" {
			return bad()
		}
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil || csr.CheckSignature() != nil || !bytes.Equal(csr.RawSubjectPublicKeyInfo, leaf.RawSubjectPublicKeyInfo) || !slices.Equal(csr.DNSNames, []string{proof.ServerName}) {
			return bad()
		}
		seen[name] = true
	}
	return deploymentplan.Digest(rows), nil
}

func managedApplicationPublicChain(raw []byte) ([]*x509.Certificate, error) {
	var chain []*x509.Certificate
	for len(bytes.TrimSpace(raw)) > 0 {
		block, rest := pem.Decode(raw)
		if block == nil || block.Type != "CERTIFICATE" {
			return nil, managedApplicationCurrentBlock("public-server-chain")
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, managedApplicationCurrentBlock("public-server-chain")
		}
		chain = append(chain, cert)
		raw = rest
	}
	if len(chain) == 0 {
		return nil, managedApplicationCurrentBlock("public-server-chain")
	}
	return chain, nil
}
