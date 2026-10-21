package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"gopkg.in/yaml.v3"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This continuation owns only the exact refused managed-host rollout. Its
// renewed seed and completed host/client enrollments remain historical inputs.
type createManagedTransportPartialRecovery struct {
	SourceReview     string
	PriorForward     *createManagedTransportInterruptedForward `json:"PriorForward,omitempty"`
	AdditionalOwners map[string]deploymentplan.Resource        `json:"AdditionalOwners,omitempty"`
	AcceptedForward  *createManagedTransportAcceptedForward    `json:"AcceptedForward,omitempty"`
	PostManaged      *createManagedTransportPostManaged        `json:"PostManaged,omitempty"`
	Original         *deploymentplan.Plan
	Journal          deploymentplan.Journal
	SessionSHA       string
	ClientsSHA       string
	ServerSHA        string
	ObservedSHA      string
	PVCs             map[string]string
}

func isManagedTransportBaseCheckpoint(j environmentCreateJournal) bool {
	if len(j.Entries) < 4 {
		return false
	}
	seen := map[string]bool{}
	for i, entry := range j.Entries {
		if seen[entry.Key] || !environmentPreflightDigest.MatchString(entry.IntentSHA256) {
			return false
		}
		seen[entry.Key] = true
		if i == len(j.Entries)-1 {
			if entry.Key != "kubernetes/transport-managed-hosts" || entry.State != "pending" || !entry.CompletedAt.IsZero() || len(entry.Receipt) != 0 {
				return false
			}
		} else if entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 {
			return false
		}
	}
	last := len(j.Entries) - 1
	if j.Entries[last-1].Key != "kubernetes/transport-server-enrollment" || j.Entries[last-2].Key != "kubernetes/transport-enrollment-issuer" {
		return false
	}
	before := j
	before.Entries = j.Entries[:last-2]
	return isManagedTransportCheckpoint(before)
}

func isManagedTransportPartialCheckpoint(j environmentCreateJournal) bool {
	return isManagedTransportBaseCheckpoint(j) || managedTransportInterruptedForwardReview(j) != "" || managedTransportAcceptedForwardReview(j) != "" || managedTransportPostManagedReview(j) != ""
}

func loadManagedTransportPartialSource(store secretStore, e *environmentCreateExecution) (createFoundationRecoveryArtifact, environmentCreateJournal, string, error) {
	base, _, err := loadManagedTransportPartialBase(store, e)
	if err != nil {
		return createFoundationRecoveryArtifact{}, environmentCreateJournal{}, "", err
	}
	e = base
	var empty createFoundationRecoveryArtifact
	var before environmentCreateJournal
	bad := func() (createFoundationRecoveryArtifact, environmentCreateJournal, string, error) {
		return empty, before, "", createProviderRecoveryBlock("managed host forward requires its exact admitted seed, completed enrollment stages and original pending rollout")
	}
	if !isManagedTransportPartialCheckpoint(e.Journal) {
		return bad()
	}
	review := ""
	for _, entry := range e.Journal.Entries {
		const prefix = "kubernetes/service-session-original-key-renewal-"
		if strings.HasPrefix(entry.Key, prefix) {
			review = strings.TrimPrefix(entry.Key, prefix)
		}
	}
	a, source, err := loadAdmittedCreateRecovery(store, e, review)
	if err != nil || !isManagedTransportCheckpoint(source) || len(e.Journal.Entries) != len(source.Entries)+11 {
		return bad()
	}
	for i, entry := range source.Entries {
		if activeTransportJournalDigest(entry) != activeTransportJournalDigest(e.Journal.Entries[i]) {
			return bad()
		}
	}
	var observed createServiceSessionObserved
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-renewal", review, "observed.json"), &observed) != nil || validateEnvironmentBootstrapCleanupBinding(&observed.Cleanup, e.Journal.Environment) != nil {
		return bad()
	}
	session := observed.Cleanup.SessionID
	tail := e.Journal.Entries[len(source.Entries):]
	keys := []string{"openbao-recovery-admission-" + review, "openbao-policy-rtk-" + e.Journal.Environment + "-service-bootstrap-" + session, "", "kubernetes/service-session-original-key-renewal-" + review, "kubernetes/service-custody-reader-renewal-" + review, "kubernetes/service-credential-secrets-renewal-" + review, "openbao-policy-create-" + session + "-cleanup-controller", "", "kubernetes/transport-enrollment-issuer", "kubernetes/transport-server-enrollment", "kubernetes/transport-managed-hosts"}
	for i, key := range keys {
		if i == 2 || i == 7 {
			role := "pki-service-bootstrap"
			if i == 7 {
				role = "pki-controller"
			}
			if !strings.HasPrefix(tail[i].Key, "openbao-role-"+role+"-"+e.Journal.Environment+"-") {
				return bad()
			}
			continue
		}
		if tail[i].Key != key {
			return bad()
		}
	}
	return a, source, review, nil
}

func managedTransportPartialNativeCheckpoint(store secretStore, e *environmentCreateExecution) bool {
	_, _, _, err := loadManagedTransportPartialSource(store, e)
	return err == nil
}

func loadOriginalManagedTransportPartialPlan(store secretStore, e *environmentCreateExecution) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	var result *deploymentplan.Plan
	var state deploymentplan.Journal
	bad := func() (*deploymentplan.Plan, deploymentplan.Journal, error) {
		return nil, state, createProviderRecoveryBlock("managed host forward requires one original three-resource child with only the failed MQTT write and completed configuration")
	}
	if !isManagedTransportPartialCheckpoint(e.Journal) {
		return bad()
	}
	entry := e.Journal.Entries[len(e.Journal.Entries)-1]
	seen := map[string]bool{}
	ns := e.Journal.Stack + "-video-cloud"
	bao := e.Journal.Stack + "-secrets"
	cm, mqtt, host := "ConfigMap/"+bao+"/openbao-file", "Deployment/"+ns+"/mqtt-pki", "StatefulSet/"+bao+"/openbao"
	for _, lease := range e.Journal.KubernetesLeases {
		sha := strings.SplitN(lease.Holder, ":", 2)[0]
		if seen[sha] || !environmentPreflightDigest.MatchString(sha) {
			continue
		}
		seen[sha] = true
		var plan deploymentplan.Plan
		var journal deploymentplan.Journal
		base := filepath.Join(store.Root, "deployment/rollback", sha)
		if readExecutionJSON(filepath.Join(base, "plan.json"), &plan) != nil || readExecutionJSON(filepath.Join(base, "journal.json"), &journal) != nil || plan.SHA256() != sha || journal.PlanSHA256 != sha || plan.Environment != e.Journal.Environment || plan.Stack != e.Journal.Stack || len(plan.Resources) != 3 || journal.Accepted || journal.AcceptanceStarted || journal.InFlight != "" || journal.Failed != mqtt || !slices.Equal(journal.Completed, []string{cm}) || len(journal.Owners) != 2 || journal.Owners[cm] == "" || journal.Owners[mqtt] == "" {
			continue
		}
		intent, err := environmentCreateKubernetesStageIntent(&plan)
		hash, hashErr := environmentCreatePlanSHA256(intent)
		if err != nil || hashErr != nil || hash != entry.IntentSHA256 {
			continue
		}
		valid := true
		ids := map[string]bool{}
		for _, resource := range plan.Resources {
			ids[resource.ID] = true
			if resource.Action != "update" || resource.UID == "" || resource.Baseline == nil || resource.ID != cm && resource.ID != mqtt && resource.ID != host || journal.Owners[resource.ID] != "" && journal.Owners[resource.ID] != resource.UID {
				valid = false
			}
		}
		if !valid || len(ids) != 3 || !ids[cm] || !ids[mqtt] || !ids[host] {
			continue
		}
		if result != nil {
			return bad()
		}
		result, state = &plan, journal
	}
	if result == nil {
		return bad()
	}
	return result, state, nil
}

func verifyManagedTransportPartialBodies(ctx context.Context, b environmentKubernetesBackend, plan *deploymentplan.Plan, j deploymentplan.Journal) error {
	for _, resource := range plan.Resources {
		actual, err := b.Read(ctx, resource.ID)
		expected := resource.Desired
		if strings.HasPrefix(resource.ID, "StatefulSet/") {
			expected = resource.Baseline
		}
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != resource.UID || !deploymentplan.Matches(expected, actual) {
			return createProviderRecoveryBlock("original managed host write or unwritten OpenBao baseline changed")
		}
		if j.Owners[resource.ID] != "" && j.Owners[resource.ID] != resource.UID {
			return rootCRLRecoveryError()
		}
	}
	return nil
}

func authenticatedManagedTransportSourceStage(store secretStore, e *environmentCreateExecution, key string, size int, materials map[string]string) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	plan, j, err := acceptedCreateStage(store, e, key)
	if err != nil {
		return nil, j, err
	}
	if len(plan.Resources) != size || plan.Environment != e.Journal.Environment || plan.Stack != e.Journal.Stack {
		return nil, j, createProviderRecoveryBlock("completed managed transport source inventory changed")
	}
	intent, err := environmentCreateKubernetesStageIntent(plan)
	hash, hashErr := environmentCreatePlanSHA256(intent)
	bound := false
	for _, entry := range e.Journal.Entries {
		if entry.Key == key && err == nil && hashErr == nil && entry.IntentSHA256 == hash {
			bound = true
		}
	}
	if !bound {
		return nil, j, createProviderRecoveryBlock("completed managed transport source intent changed")
	}
	materials["accepted-service-core-managed-partial-source-plan-"+key] = plan.SHA256()
	materials["accepted-service-core-managed-partial-source-journal-"+key] = deploymentplan.Digest(j)
	return plan, j, nil
}

func managedTransportCredentialTrustSupersession(store secretStore, e *environmentCreateExecution, key string, resource deploymentplan.Resource, owner string, materials map[string]string) (deploymentplan.Object, error) {
	const prefix = "kubernetes/service-credential-secrets-renewal-"
	bad := func() (deploymentplan.Object, error) {
		return nil, createProviderRecoveryBlock("completed managed transport source supersession changed: stage=" + key + " resource=" + resource.ID)
	}
	if !strings.HasPrefix(key, prefix) || !environmentPreflightDigest.MatchString(strings.TrimPrefix(key, prefix)) || resource.ID != "ConfigMap/"+e.Journal.Stack+"-account-manager/pki-create-service-trust" || resource.Action != "update" || resource.UID == "" || resource.UID != owner {
		return bad()
	}
	laterKey := "kubernetes/transport-enrollment-issuer"
	priorIndex, laterIndex := -1, -1
	for index, entry := range e.Journal.Entries {
		if entry.Key == key {
			priorIndex = index
		}
		if entry.Key == laterKey {
			laterIndex = index
		}
	}
	if priorIndex < 0 || laterIndex <= priorIndex {
		return bad()
	}
	later, journal, err := authenticatedManagedTransportSourceStage(store, e, laterKey, 16, materials)
	if err != nil {
		return nil, err
	}
	for _, next := range later.Resources {
		if next.ID == resource.ID {
			if next.Action != "update" || next.UID != owner || journal.Owners[next.ID] != owner || next.Baseline == nil || !deploymentplan.Matches(resource.Desired, next.Baseline) {
				return bad()
			}
			return next.Desired, nil
		}
	}
	return bad()
}

func acceptedManagedTransportSourceStage(ctx context.Context, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, key string, size int, materials map[string]string, partial ...*createManagedTransportPartialRecovery) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	plan, journal, err := authenticatedManagedTransportSourceStage(store, e, key, size, materials)
	if err != nil {
		return nil, journal, err
	}
	for _, resource := range plan.Resources {
		expected := resource.Desired
		if strings.HasPrefix(key, "kubernetes/service-credential-secrets-renewal-") && resource.ID == "ConfigMap/"+e.Journal.Stack+"-account-manager/pki-create-service-trust" {
			expected, err = managedTransportCredentialTrustSupersession(store, e, key, resource, journal.Owners[resource.ID], materials)
			if err != nil {
				return nil, journal, err
			}
		}
		if len(partial) == 1 {
			expected, err = managedTransportWrittenPolicySourceOverride(partial[0], resource.ID, journal.Owners[resource.ID], expected)
			if err != nil {
				return nil, journal, err
			}
		}
		actual, err := b.Read(ctx, resource.ID)
		if err != nil || actual == nil || journal.Owners[resource.ID] == "" || deploymentplan.Map(actual["metadata"])["uid"] != journal.Owners[resource.ID] || !deploymentplan.Matches(expected, actual) {
			return nil, journal, createProviderRecoveryBlock("completed managed transport source owner or recipe changed: stage=" + key + " resource=" + resource.ID)
		}
	}
	return plan, journal, nil
}

type managedTransportPreservedClients struct {
	Clients  string `json:"clients_sha256"`
	Key      string `json:"original_key_sha256"`
	CSR      string `json:"original_csr_sha256"`
	Original string `json:"original_session_id"`
	Renewed  string `json:"renewed_session_id"`
}

func loadManagedTransportOriginalCustodyPins(store secretStore, e *environmentCreateExecution, proof managedTransportPreservedClients, currentSession string, materials map[string]string) (map[string]string, error) {
	bad := func() (map[string]string, error) {
		return nil, createProviderRecoveryBlock("managed partial continuation cannot authenticate original Service custody alias, admission and preserved-key proof")
	}
	var alias struct {
		Stage    string `json:"actual_qualified_forward_stage"`
		Original string `json:"original_failed_plan_sha256"`
		Session  string `json:"renewed_session_id"`
	}
	count := 0
	for _, entry := range e.Journal.Entries {
		if entry.Key == "kubernetes/service-issuer-server-enrollment" {
			count++
			if entry.State != "completed" || entry.CompletedAt.IsZero() || json.Unmarshal(entry.Receipt, &alias) != nil {
				return bad()
			}
		}
	}
	const prefix = "kubernetes/service-issuer-server-enrollment-renewal-"
	review := strings.TrimPrefix(alias.Stage, prefix)
	if count != 1 || !strings.HasPrefix(alias.Stage, prefix) || !environmentPreflightDigest.MatchString(review) || !environmentPreflightDigest.MatchString(alias.Original) || !environmentBootstrapOwnerUUID.MatchString(alias.Session) || !environmentBootstrapOwnerUUID.MatchString(proof.Original) || !environmentBootstrapOwnerUUID.MatchString(currentSession) || proof.Original == currentSession || proof.Renewed != currentSession || !environmentPreflightDigest.MatchString(proof.Clients) {
		return bad()
	}
	original, _, err := loadAdmittedCreateRecovery(store, e, review)
	if err != nil || original.Plan.Materials["original-service-session-plan"] != alias.Original {
		return bad()
	}
	pins := map[string]string{"bootstrap.key": original.Plan.Materials["original-service-session-custody-bootstrap.key"], "bootstrap.csr": original.Plan.Materials["original-service-session-custody-bootstrap.csr"]}
	if !environmentPreflightDigest.MatchString(pins["bootstrap.key"]) || !environmentPreflightDigest.MatchString(pins["bootstrap.csr"]) || proof.Key != pins["bootstrap.key"] || proof.CSR != pins["bootstrap.csr"] {
		return bad()
	}
	materials["accepted-service-core-managed-partial-original-custody-admission"] = deploymentplan.Digest(original)
	materials["accepted-service-core-managed-partial-preserved-clients-proof"] = deploymentplan.Digest(proof)
	return pins, nil
}

type managedTransportServerEvidence struct {
	Domain      string     `json:"domain"`
	Caller      string     `json:"caller"`
	Request     string     `json:"request_id"`
	Issuer      string     `json:"issuer_id"`
	Status      string     `json:"status"`
	Fingerprint string     `json:"fingerprint"`
	CSR         string     `json:"csr_pem"`
	Certificate string     `json:"certificate_pem"`
	DNS         []string   `json:"dns_names"`
	Revoked     *time.Time `json:"revoked_at"`
}

func readManagedTransportServers(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, p environmentCreateBootstrapPlan, roots, intermediates map[string]environmentCreateRegisteredIssuer) (string, error) {
	query := "SELECT COALESCE(json_agg(json_build_object('domain',domain,'caller',caller,'request_id',request_id,'issuer_id',issuer_id,'status',status,'fingerprint',fingerprint,'csr_pem',csr_pem,'certificate_pem',certificate_pem,'dns_names',dns_names,'revoked_at',revoked_at) ORDER BY domain,caller,request_id),'[]'::json) FROM pki_server_issuances WHERE environment='" + cfg.Environment + "' AND domain IN ('mqtt','openbao_tls')"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var rows []managedTransportServerEvidence
	if err != nil || decodeEnvironmentJSON(raw, &rows) != nil || len(rows) != 2 {
		return "", createProviderRecoveryBlock("managed transport server enrollment lacks its exact two native signing outcomes")
	}
	seen := map[string]bool{}
	for _, row := range rows {
		root, issuer := roots[row.Domain], intermediates[row.Domain]
		caller := "service:emqx-pki"
		if row.Domain == "openbao_tls" {
			caller = "service:openbao"
		}
		names, namesErr := environmentCreateTransportNames(p, row.Domain)
		leaf, leafErr := parseFirstCertificatePEM([]byte(row.Certificate))
		authority, authorityErr := parseFirstCertificatePEM([]byte(issuer.Certificate))
		block, rest := pem.Decode([]byte(row.CSR))
		if seen[row.Domain] || row.Domain != "mqtt" && row.Domain != "openbao_tls" || row.Caller != caller || row.Request == "" || row.Issuer != issuer.ID || row.Status != "succeeded" || row.Revoked != nil || namesErr != nil || leafErr != nil || authorityErr != nil || block == nil || block.Type != "CERTIFICATE REQUEST" || strings.TrimSpace(string(rest)) != "" || !slices.Equal(row.DNS, names) || !slices.Equal(leaf.DNSNames, names) || !leaf.NotAfter.After(time.Now().Add(time.Hour)) || leaf.NotBefore.After(time.Now()) || leaf.CheckSignatureFrom(authority) != nil || environmentPreflightBytesSHA256(leaf.Raw) != row.Fingerprint || issuer.RootFingerprint != root.RootFingerprint {
			return "", createProviderRecoveryBlock("managed transport server issuer, names, signature or native issuance differs from its completed source")
		}
		csr, err := x509.ParseCertificateRequest(block.Bytes)
		if err != nil || csr.CheckSignature() != nil || !slices.Equal(csr.RawSubjectPublicKeyInfo, leaf.RawSubjectPublicKeyInfo) {
			return "", createProviderRecoveryBlock("managed transport original server CSR or key pin differs")
		}
		serverAuth := false
		for _, usage := range leaf.ExtKeyUsage {
			if usage == x509.ExtKeyUsageServerAuth {
				serverAuth = true
			}
		}
		if !serverAuth {
			return "", rootCRLRecoveryError()
		}
		seen[row.Domain] = true
	}
	return deploymentplan.Digest(rows), nil
}

func loadManagedTransportPartialServiceCore(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, p *createFoundationRecoveryPlan) (*createServiceSessionRecovery, error) {
	bad := func(stage string) (*createServiceSessionRecovery, error) {
		return nil, createProviderRecoveryBlock("managed partial continuation cannot authenticate original native evidence: stage=" + stage)
	}
	baseExecution, priorForward, err := loadManagedTransportPartialBase(store, e)
	if err != nil {
		return nil, err
	}
	admitted, before, sourceReview, err := loadManagedTransportPartialSource(store, baseExecution)
	if err != nil {
		return nil, err
	}
	original, j, err := loadOriginalManagedTransportPartialPlan(store, baseExecution)
	if err != nil {
		return nil, err
	}
	partial := &createManagedTransportPartialRecovery{SourceReview: sourceReview, PriorForward: priorForward, Original: original, Journal: j, PVCs: map[string]string{}, AdditionalOwners: map[string]deploymentplan.Resource{}}
	if managedTransportPostManagedReview(e.Journal) != "" {
		_, _, partial.AcceptedForward, partial.PostManaged, err = loadManagedTransportPostManaged(store, e)
		if err != nil {
			return nil, err
		}
	} else if managedTransportAcceptedForwardReview(e.Journal) != "" {
		_, _, partial.AcceptedForward, err = loadManagedTransportAcceptedForward(store, e)
		if err != nil {
			return nil, err
		}
	}
	if err := verifyManagedTransportCurrentPartialBodies(ctx, b, partial); err != nil {
		return nil, err
	}
	if partial.PostManaged != nil {
		if err := verifyManagedTransportPostManagedExportOwners(ctx, cfg, e, b, partial.PostManaged); err != nil {
			return nil, err
		}
	}
	p.Plan.Bootstrap = admitted.Plan.Plan.Bootstrap
	r := &createServiceSessionRecovery{CoreAccepted: true, Renewal: true, PostServiceResume: true, PostTransportResume: true}
	historical := *e
	historical.Journal = before
	_, _, err = loadManagedTransportRecoverySource(store, &historical, r)
	if err != nil {
		return nil, err
	}
	d := r.ManagedTransportRecovery
	d.Partial = partial
	roots := map[string]environmentCreateRegisteredIssuer{}
	intermediates := map[string]environmentCreateRegisteredIssuer{}
	for _, domain := range managedPKIDomains {
		var public struct {
			Root         map[string]any `json:"root"`
			Intermediate map[string]any `json:"intermediate,omitempty"`
		}
		if readExecutionJSON(filepath.Join(store.Root, "pki", domain, "issuer.json"), &public) != nil {
			return bad("publication-" + domain)
		}
		root, err := environmentCreateRegisteredIssuerFromMap(public.Root, "")
		if err != nil || root.Environment != cfg.Environment || root.Domain != domain || root.Status != "active" {
			return bad("active-root-" + domain)
		}
		roots[domain] = root
		if domain != "device" {
			intermediate, err := environmentCreateRegisteredIssuerFromMap(public.Intermediate, root.RootFingerprint)
			if err != nil || intermediate.Environment != cfg.Environment || intermediate.Domain != domain || intermediate.Status != "active" || intermediate.Document["parent_issuer_id"] != root.ID {
				return bad("active-intermediate-" + domain)
			}
			intermediates[domain] = intermediate
		}
	}
	if err := verifyEnvironmentCreateRegisteredIssuers(store, roots, intermediates, p.Materials); err != nil {
		return nil, err
	}
	if err := verifyManagedTransportNativeRegistry(ctx, cfg, store, e, b, p); err != nil {
		return nil, err
	}
	d.Device, d.AppRoot, d.AppIntermediate = roots["device"], roots["app"], intermediates["app"]
	var deviceReady map[string]any
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/device/root-ready.json"), &deviceReady) != nil {
		return bad("device-ready")
	}
	d.DeviceReady, err = environmentCreateRegisteredIssuerFromMap(deviceReady, "")
	if err != nil || d.DeviceReady.ID != d.Device.ID || d.DeviceReady.Certificate != d.Device.Certificate || d.DeviceReady.Status != "ready" {
		return bad("device-ready-pin")
	}
	d.AppIntermediateReady, err = originalReadyIntermediate(store, e, d.AppIntermediate)
	if err != nil {
		return nil, err
	}
	r.AppTrustRecovery.DeviceReady, r.AppTrustRecovery.Device, r.AppTrustRecovery.Root = d.DeviceReady, d.Device, d.AppRoot
	mqttRoot, mqttIntermediate, baoRoot, baoIntermediate := roots["mqtt"], intermediates["mqtt"], roots["openbao_tls"], intermediates["openbao_tls"]
	r.MQTTRoot, r.MQTTIntermediate, r.OpenBaoRoot, r.OpenBaoIntermediate = &mqttRoot, &mqttIntermediate, &baoRoot, &baoIntermediate
	base := filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/service")
	if readExecutionJSON(filepath.Join(store.Root, base, "session-intent.json"), &r.Intent) != nil || !environmentBootstrapOwnerUUID.MatchString(r.Intent.SessionID) || r.Intent.Claim != "pki-service-bootstrap-state" || !slices.Equal(r.Intent.Namespaces, []string{p.Plan.Stack + "-video-cloud", p.Plan.Stack + "-account-manager", p.Plan.Stack + "-secrets"}) {
		return bad("original-session-intent")
	}
	var observed createServiceSessionObserved
	observedRaw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, base, "session-renewal", sourceReview, "observed.json"), true)
	if err != nil || json.Unmarshal(observedRaw, &observed) != nil || validateEnvironmentBootstrapCleanupBinding(&observed.Cleanup, cfg.Environment) != nil || observed.Reader != "pki-bootstrap-custody-"+e.Journal.PlanSHA256[:12] || len(observed.UIDs) != len(r.Intent.Namespaces) {
		return bad("renewed-session-observation")
	}
	r.Ceremony = environmentCreateServiceCeremony{Root: roots["service"], Intermediate: intermediates["service"], BootstrapSessionID: observed.Cleanup.SessionID, BootstrapCredentialSecret: "service-bootstrap-" + observed.Cleanup.SessionID, BootstrapCredentialNamespace: r.Intent.Namespaces[0], BootstrapCredentialNamespaces: slices.Clone(r.Intent.Namespaces), BootstrapClaim: r.Intent.Claim, CustodyReader: observed.Reader, CleanupBinding: observed.Cleanup, CredentialUIDs: observed.UIDs, RootCRLSecret: "pki-create-service-root-crl"}
	if partial.PostManaged != nil && partial.PostManaged.Session != r.Ceremony.BootstrapSessionID {
		return bad("post-managed-original-session")
	}
	if partial.PostManaged != nil && partial.PostManaged.ExportRetire != nil && partial.PostManaged.ExportRetire.RootSHA != r.Ceremony.Root.RootFingerprint {
		return bad("retained-api-export-root")
	}
	crl, proof, err := readCreateRootCRLRecovery(store, e.Journal.PlanSHA256)
	if err != nil || crl == nil || proof == nil || crl.Plan.Root.ID != r.Ceremony.Root.ID {
		return bad("effective-root-crl")
	}
	r.Ceremony.RootCRLFile, r.Ceremony.RootCRLPEM = proof.File, crl.Plan.Signed.PEM
	p.Materials["accepted-service-core-managed-service-root-crl-recovery"] = deploymentplan.Digest(crl)
	p.Materials["accepted-service-core-managed-service-root-crl-proof"] = deploymentplan.Digest(proof)
	for _, stage := range []struct {
		key  string
		size int
	}{{"kubernetes/service-session-original-key-renewal-" + sourceReview, 1}, {"kubernetes/service-custody-reader-renewal-" + sourceReview, 1}, {"kubernetes/service-credential-secrets-renewal-" + sourceReview, 7}, {"kubernetes/transport-enrollment-issuer", 16}, {"kubernetes/transport-server-enrollment", 4}} {
		plan, journal, err := acceptedManagedTransportSourceStage(ctx, store, e, b, stage.key, stage.size, p.Materials, partial)
		if err != nil {
			return nil, err
		}
		if priorForward != nil {
			for _, resource := range plan.Resources {
				if resource.ID == "Deployment/"+p.Plan.Stack+"-video-cloud/pki-controller" || resource.ID == "ConfigMap/"+p.Plan.Stack+"-secrets/pki-create-service-trust" {
					resource.UID = journal.Owners[resource.ID]
					resource.Desired, err = managedTransportWrittenPolicySourceOverride(partial, resource.ID, resource.UID, resource.Desired)
					if err != nil {
						return nil, err
					}
					d.Partial.AdditionalOwners[resource.ID] = resource
				}
			}
		}
		if strings.HasPrefix(stage.key, "kubernetes/service-session-original-key-renewal-") {
			r.Original, r.Journal = plan, journal
			var preview deploymentplan.Plan
			if readExecutionJSON(filepath.Join(store.Root, "deployment/create/openbao-recovery", sourceReview, "used-bootstrap-renewal-plan.json"), &preview) != nil || preview.SHA256() != admitted.Plan.Materials["accepted-service-core-used-renewal-plan"] {
				return bad("admitted-renewal-preview")
			}
			normalized := *plan
			normalized.BindingsSHA256 = preview.BindingsSHA256
			if normalized.SHA256() != preview.SHA256() {
				return bad("accepted-renewal-plan")
			}
		}
		if strings.HasPrefix(stage.key, "kubernetes/service-custody-reader-renewal-") {
			container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(plan.Resources[0].Desired)["containers"])[0])
			r.RenewedCertPath = mqttFoundationEnv(container, "PKI_BOOTSTRAP_CERT_PATH")
		}
		if stage.key == "kubernetes/transport-enrollment-issuer" {
			r.Trust, r.TrustJournal = plan, journal
		}
	}
	if !strings.HasPrefix(r.RenewedCertPath, "/run/pki-bootstrap/renew-") || !strings.HasSuffix(r.RenewedCertPath, "/bootstrap.crt") {
		return bad("renewed-certificate-path")
	}
	subjects, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil {
		return nil, err
	}
	runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: b}
	state, sessionRaw, err := readManagedSessionState(ctx, cfg, runtime, r.Ceremony.BootstrapSessionID)
	if err != nil || state.Status != "active" || state.Issuer != r.Ceremony.Intermediate.ID || state.Root != r.Ceremony.Root.RootFingerprint || state.OtherActive != 0 || !state.Expires.After(time.Now().Add(2*time.Hour)) || !slices.Equal(state.Subjects, subjects) {
		return bad("current-active-seed")
	}
	var saved managedSessionState
	if json.Unmarshal(observed.Session, &saved) != nil || saved.Session != state.Session || saved.Issuer != state.Issuer || saved.Root != state.Root || saved.Fingerprint != state.Fingerprint || !saved.Expires.Equal(state.Expires) || !slices.Equal(saved.Subjects, state.Subjects) {
		return bad("current-seed-native-observation")
	}
	material := map[string]string{}
	for _, namespace := range r.Ceremony.BootstrapCredentialNamespaces {
		secret, err := b.Read(ctx, "Secret/"+namespace+"/"+r.Ceremony.BootstrapCredentialSecret)
		if err != nil || secret == nil || deploymentplan.Map(secret["metadata"])["uid"] != observed.UIDs[namespace] {
			return bad("current-seed-secret-owner")
		}
		values := map[string]string{}
		for _, key := range []string{"tls.crt", "tls.key", "ca.crt"} {
			raw, err := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(deploymentplan.Map(secret["data"])[key]))
			if err != nil {
				return bad("current-seed-secret-material")
			}
			values[key] = string(raw)
		}
		if len(material) > 0 && deploymentplan.Digest(values) != deploymentplan.Digest(material) {
			return bad("current-seed-secret-copies")
		}
		material = values
	}
	pair, err := tls.X509KeyPair([]byte(material["tls.crt"]), []byte(material["tls.key"]))
	if err != nil || len(pair.Certificate) < 2 || material["ca.crt"] != r.Ceremony.Root.Certificate {
		return bad("current-seed-certificate")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.Subject.CommonName != "service:deployment-bootstrap" || environmentPreflightBytesSHA256(leaf.Raw) != state.Fingerprint || environmentPreflightBytesSHA256(pair.Certificate[len(pair.Certificate)-1]) != r.Ceremony.Root.RootFingerprint || !leaf.NotAfter.Equal(state.Expires) || leaf.NotBefore.After(time.Now()) {
		return bad("current-seed-native-leaf")
	}
	var clientProof managedTransportPreservedClients
	if readExecutionJSON(filepath.Join(store.Root, base, "session-renewal", sourceReview, "managed-clients-preserved.json"), &clientProof) != nil {
		return bad("preserved-client-proof")
	}
	custodyPins, err := loadManagedTransportOriginalCustodyPins(store, e, clientProof, state.Session, p.Materials)
	if err != nil {
		return nil, err
	}
	for _, file := range []string{"bootstrap.key", "bootstrap.csr", r.RenewedCertPath} {
		path := "/run/pki-bootstrap/" + file
		if strings.HasPrefix(file, "/") {
			path = file
		}
		raw, err := b.command(ctx, nil, "--namespace", r.Ceremony.BootstrapCredentialNamespace, "exec", "deployment/"+r.Ceremony.CustodyReader, "--container", "custody", "--", "cat", path)
		if err != nil {
			return bad("original-custody-file")
		}
		hash := environmentPreflightBytesSHA256(raw)
		if file == "bootstrap.key" || file == "bootstrap.csr" {
			if custodyPins[file] != hash {
				return bad("original-key-csr-pin")
			}
		}
		if file == "bootstrap.key" {
			r.OriginalKeySHA = hash
			if string(raw) != material["tls.key"] {
				return bad("original-key-secret-copy")
			}
		}
		if file == "bootstrap.csr" {
			r.OriginalCSRSHA = hash
		}
		if file == r.RenewedCertPath && string(raw) != material["tls.crt"] {
			return bad("renewed-leaf-secret-copy")
		}
	}
	clients, err := readManagedSessionClients(ctx, cfg, runtime, r.Ceremony, subjects)
	if err != nil || clients != clientProof.Clients {
		return bad("unchanged-original-client-inventory")
	}
	servers, err := readManagedTransportServers(ctx, cfg, runtime, p.Plan.Bootstrap, roots, intermediates)
	if err != nil {
		return nil, err
	}
	r.RenewedIntent = r.Intent
	r.RenewedIntent.SessionID = state.Session
	authentication := *r
	authentication.Intent = r.RenewedIntent
	if err := verifyManagedTransportRetainedAuthentication(ctx, cfg, store, e, b, &authentication); err != nil {
		return nil, err
	}
	var kube struct {
		Clusters []struct {
			Cluster map[string]any `yaml:"cluster"`
		} `yaml:"clusters"`
	}
	if yaml.Unmarshal(b.KubeconfigContent, &kube) != nil || len(kube.Clusters) != 1 || kube.Clusters[0].Cluster["certificate-authority-data"] != observed.Cleanup.KubernetesCA || observed.Cleanup.SecretUID != observed.UIDs[r.Ceremony.BootstrapCredentialNamespace] {
		return bad("current-cleanup-cluster-binding")
	}
	namespace, err := b.Read(ctx, "Namespace//"+r.Ceremony.BootstrapCredentialNamespace)
	if err != nil || namespace == nil || deploymentplan.Map(namespace["metadata"])["uid"] != observed.Cleanup.NamespaceUID {
		return bad("current-cleanup-namespace-owner")
	}
	r.PostServiceRenewal = false
	r.SourceRenewalStage = "kubernetes/service-session-original-key-renewal-" + sourceReview
	seenSeeds := map[string]bool{}
	for _, entry := range before.Entries {
		const prefix = "kubernetes/service-credential-secrets-renewal-"
		if !strings.HasPrefix(entry.Key, prefix) {
			continue
		}
		review := strings.TrimPrefix(entry.Key, prefix)
		if _, _, err := loadAdmittedCreateRecovery(store, e, review); err != nil {
			return bad("retained-seed-admission")
		}
		var old createServiceSessionObserved
		if readExecutionJSON(filepath.Join(store.Root, base, "session-renewal", review, "observed.json"), &old) != nil || validateEnvironmentBootstrapCleanupBinding(&old.Cleanup, cfg.Environment) != nil {
			return bad("retained-seed-history")
		}
		if old.Cleanup.SessionID == state.Session || old.Cleanup.SessionID == r.Intent.SessionID || seenSeeds[old.Cleanup.SessionID] {
			continue
		}
		seedPlan, seedJournal, err := acceptedCreateStage(store, e, entry.Key)
		if err != nil {
			return nil, err
		}
		for _, namespace := range r.Intent.Namespaces {
			id := "Secret/" + namespace + "/service-bootstrap-" + old.Cleanup.SessionID
			bound := false
			for _, resource := range seedPlan.Resources {
				if resource.ID == id && seedJournal.Owners[id] == old.UIDs[namespace] {
					bound = true
				}
			}
			if !bound {
				return bad("retained-seed-copy-owner")
			}
		}
		seenSeeds[old.Cleanup.SessionID] = true
		r.StaleSeeds = append(r.StaleSeeds, old)
	}
	p.Materials["accepted-service-core-managed-partial-stale-seeds"] = deploymentplan.Digest(r.StaleSeeds)
	// The source all-CA session is also retained for final exact UID cleanup.
	if !slices.ContainsFunc(r.StaleSeeds, func(old createServiceSessionObserved) bool { return old.Cleanup.SessionID == clientProof.Original }) {
		return bad("source-expired-seed-history")
	}
	ledger, err := readEnvironmentCreateLedger(e)
	if err != nil {
		return nil, err
	}
	var active managedTransportVolumeLedgerActive
	if readExecutionJSON(filepath.Join(store.Root, "deployment/resources/active.json"), &active) != nil || validateManagedTransportVolumeLedger(ledger, active) != nil {
		return bad("original-host-volume-ledger-active")
	}
	p.Materials["accepted-service-core-managed-partial-original-volume-ledger"] = active.LedgerSHA
	p.Materials["accepted-service-core-managed-partial-original-volume-ledger-active"] = deploymentplan.Digest(active)
	for _, id := range []string{"PersistentVolumeClaim/" + p.Plan.Stack + "-video-cloud/mqtt-pki-data", "PersistentVolumeClaim/" + p.Plan.Stack + "-video-cloud/mqtt-pki-trust", "PersistentVolumeClaim/" + p.Plan.Stack + "-video-cloud/mqtt-pki-host-identity", "PersistentVolumeClaim/" + p.Plan.Stack + "-secrets/openbao-pki-identity", "PersistentVolumeClaim/" + p.Plan.Stack + "-secrets/data-openbao-0", "PersistentVolumeClaim/" + p.Plan.Stack + "-secrets/audit-openbao-0"} {
		owner, err := managedTransportRecordedHostVolume(ledger, id)
		if err != nil {
			return nil, err
		}
		claim, err := b.Read(ctx, id)
		if err != nil {
			return nil, err
		}
		volume, err := b.Read(ctx, "PersistentVolume//"+owner.PVName)
		if err != nil {
			return nil, err
		}
		if err := validateManagedTransportHostVolume(owner, claim, volume); err != nil {
			return nil, err
		}
		d.Partial.PVCs[id] = owner.PVCUID
		p.Materials["accepted-service-core-managed-partial-original-volume-"+id] = deploymentplan.Digest(owner)
	}
	d.Partial.SessionSHA, d.Partial.ClientsSHA, d.Partial.ServerSHA, d.Partial.ObservedSHA = environmentPreflightBytesSHA256(sessionRaw), clients, servers, environmentPreflightBytesSHA256(observedRaw)
	p.Materials["accepted-service-core-managed-partial-source"] = deploymentplan.Digest(admitted)
	p.Materials["accepted-service-core-managed-partial-original-plan"] = original.SHA256()
	p.Materials["accepted-service-core-managed-partial-original-journal"] = deploymentplan.Digest(j)
	p.Materials["accepted-service-core-managed-partial-native-proof"] = deploymentplan.Digest(d.Partial)
	_, management, err := foundationRecoveryMaterials(store, e.Journal.PlanSHA256, createRecoveryTransportMinimumLife(ctx, 0, true))
	if err != nil {
		return nil, err
	}
	ca, err := parseFirstCertificatePEM([]byte(management.CACert))
	if err != nil {
		return nil, err
	}
	caPath := filepath.Join(store.Root, "deployment/create/material", e.Journal.PlanSHA256, "openbao-transport/ca.crt")
	_, renewal, err := readInitialTransportRenewal(store, e.Journal.PlanSHA256)
	if err != nil {
		return nil, err
	}
	if renewal != nil {
		caPath = filepath.Join(store.Root, "deployment/create/transport-renewal", renewal.Review, "openbao-transport/ca.crt")
	}
	listeners, _, err := loadInitialListenerCheckpoint(store, e, p, true)
	if err != nil {
		return nil, err
	}
	candidateRuntime := environmentCreateBootstrapRuntime{ExecutionSHA256: e.Journal.PlanSHA256, Execution: e, Backend: b, ServiceSessionRecovery: r, TransportCAPEM: management.CACert, TransportRootSHA256: environmentPreflightBytesSHA256(ca.Raw), TransportTrustConfigMap: "pki-create-transport-trust", TransportSecrets: map[string]string{"account-manager": "pki-create-account-manager-transport", "pki-controller": "pki-create-pki-controller-transport", "certissuer": "pki-create-certissuer-transport"}, OpenBao: environmentCreateOpenBaoBootstrap{CACertPath: caPath}}
	for _, resource := range listeners.Resources {
		candidateRuntime.InitialListeners = append(candidateRuntime.InitialListeners, deploymentplan.Definition{Phase: resource.Phase, Object: deploymentplan.Clone(resource.Desired)})
	}
	definitions, err := renderEnvironmentCreateTransportDefinitions(ctx, cfg, p.Plan.Bootstrap, candidateRuntime, r.Ceremony, roots, intermediates, p.Plan.Release)
	if err != nil {
		return nil, err
	}
	definitions, err = preserveManagedTransportPostManagedDefinitions(p.Plan.Bootstrap, candidateRuntime, r.Ceremony, definitions)
	if err != nil {
		return nil, err
	}
	if err := validateManagedTransportPartialForwardDefinitions(d.Partial, definitions); err != nil {
		return nil, err
	}
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return nil, err
	}
	r.Candidate, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("transport-managed-hosts-forward", definitions)}, definitions, live)
	if err != nil {
		return nil, err
	}
	if err := validateManagedTransportPartialForwardActions(d.Partial, r.Candidate); err != nil {
		return nil, err
	}
	p.Materials["accepted-service-core-managed-partial-forward-plan"] = r.Candidate.SHA256()
	return r, e.Check(ctx)
}

type managedTransportVolumeLedgerActive struct {
	Schema      string `json:"schema"`
	Environment string `json:"environment"`
	Stack       string `json:"stack"`
	Plan        string `json:"plan_sha256"`
	LedgerSHA   string `json:"ledger_sha256"`
}

func validateManagedTransportVolumeLedger(ledger environmentCreateResourceLedger, active managedTransportVolumeLedgerActive) error {
	if active.Schema != "rtk-cloud.resource-ledger-active/v1" || active.Environment != ledger.Environment || active.Stack != ledger.Stack || active.Plan != ledger.PlanSHA256 || !environmentPreflightDigest.MatchString(active.LedgerSHA) || active.LedgerSHA != deploymentplan.Digest(ledger) {
		return createProviderRecoveryBlock("original host volume ledger active pointer differs")
	}
	return nil
}

func managedTransportRecordedHostVolume(ledger environmentCreateResourceLedger, id string) (environmentCreateVolumeOwner, error) {
	var owner environmentCreateVolumeOwner
	count := 0
	for _, volume := range ledger.Volumes {
		if id == "PersistentVolumeClaim/"+volume.Namespace+"/"+volume.PVCName {
			owner = volume
			count++
		}
	}
	if _, err := positiveIntValue("volume_id", owner.NativeID); err != nil || count != 1 || owner.PVCUID == "" || owner.PVUID == "" || owner.PVName == "" || ledger.Kubernetes[id] != "" && ledger.Kubernetes[id] != owner.PVCUID {
		return owner, createProviderRecoveryBlock("original host volume custody is absent or changed: resource=" + id)
	}
	return owner, nil
}

func validateManagedTransportHostVolume(owner environmentCreateVolumeOwner, claim, volume deploymentplan.Object) error {
	id := "PersistentVolumeClaim/" + owner.Namespace + "/" + owner.PVCName
	claimMeta, volumeMeta := deploymentplan.Map(claim["metadata"]), deploymentplan.Map(volume["metadata"])
	claimSpec, volumeSpec := deploymentplan.Map(claim["spec"]), deploymentplan.Map(volume["spec"])
	ref, csi := deploymentplan.Map(volumeSpec["claimRef"]), deploymentplan.Map(volumeSpec["csi"])
	nativeID := deploymentplan.String(csi["volumeHandle"])
	if prefix, suffix, ok := strings.Cut(nativeID, "-"); ok && strings.HasPrefix(suffix, "pvc-") {
		nativeID = prefix
	}
	if claim == nil || volume == nil || claim["kind"] != "PersistentVolumeClaim" || volume["kind"] != "PersistentVolume" || claimMeta["namespace"] != owner.Namespace || claimMeta["name"] != owner.PVCName || claimMeta["uid"] != owner.PVCUID || volumeMeta["name"] != owner.PVName || volumeMeta["uid"] != owner.PVUID || claimMeta["deletionTimestamp"] != nil || volumeMeta["deletionTimestamp"] != nil || claimSpec["volumeName"] != owner.PVName || deploymentplan.Map(claim["status"])["phase"] != "Bound" || deploymentplan.Map(volume["status"])["phase"] != "Bound" || ref["namespace"] != owner.Namespace || ref["name"] != owner.PVCName || ref["uid"] != owner.PVCUID || csi["driver"] != "linodebs.csi.linode.com" || nativeID != owner.NativeID {
		return createProviderRecoveryBlock("original host PV/PVC or native CSI binding changed: resource=" + id)
	}
	return nil
}

func managedTransportPartialFoundationOverride(partial *createManagedTransportPartialRecovery, id, owner string, expected deploymentplan.Object) (deploymentplan.Object, bool, error) {
	if partial != nil && partial.Original != nil && partial.PriorForward != nil && partial.PriorForward.Previous != nil && id == "StatefulSet/"+partial.Original.Stack+"-secrets/openbao" {
		latest := partial.PriorForward
		if validateManagedTransportWrittenOpenBaoJournal(latest.Plan, latest.Journal) == nil {
			for _, resource := range latest.Plan.Resources {
				if resource.ID == id && owner != "" && owner == resource.UID && owner == latest.Journal.Owners[id] && deploymentplan.Matches(expected, resource.Baseline) {
					if partial.AcceptedForward != nil {
						for _, accepted := range partial.AcceptedForward.Plan.Resources {
							if accepted.ID == id && accepted.UID == owner && deploymentplan.Matches(resource.Desired, accepted.Baseline) {
								return accepted.Desired, true, nil
							}
						}
						return nil, false, createProviderRecoveryBlock("accepted OpenBao foundation continuation differs: resource=" + id)
					}
					return resource.Desired, true, nil
				}
			}
		}
		return nil, false, createProviderRecoveryBlock("written OpenBao differs from its original foundation owner and qualified baseline: resource=" + id)
	}
	if partial == nil || partial.Original == nil || id != "ConfigMap/"+partial.Original.Stack+"-secrets/openbao-file" {
		return expected, false, nil
	}
	bad := func() (deploymentplan.Object, bool, error) {
		return nil, false, createProviderRecoveryBlock("original foundation supersession differs from the recorded managed host write: resource=" + id)
	}
	plan, journal := partial.Original, partial.Journal
	if len(plan.Resources) != 3 || journal.PlanSHA256 != plan.SHA256() || journal.Accepted || journal.AcceptanceStarted || journal.InFlight != "" || journal.Failed != "Deployment/"+plan.Stack+"-video-cloud/mqtt-pki" || !slices.Equal(journal.Completed, []string{id}) || len(journal.Owners) != 2 || owner == "" || journal.Owners[id] != owner {
		return bad()
	}
	for _, resource := range plan.Resources {
		if resource.ID == id {
			if resource.Action != "update" || resource.UID != owner || resource.Baseline == nil || !deploymentplan.Matches(expected, resource.Baseline) {
				return bad()
			}
			if partial.PostManaged != nil {
				for _, current := range partial.PostManaged.Plan.Resources {
					if current.ID == id && current.UID == owner && partial.PostManaged.Journal.Owners[id] == owner && deploymentplan.Matches(resource.Desired, current.Baseline) {
						return current.Desired, true, nil
					}
				}
				return bad()
			}
			return resource.Desired, true, nil
		}
	}
	return bad()
}

func validateManagedTransportForwardDefinitions(original *deploymentplan.Plan, definitions []deploymentplan.Definition) error {
	compiled, err := deploymentplan.Compile(original.Inputs, definitions, nil)
	if err != nil {
		return err
	}
	byID := map[string]deploymentplan.Object{}
	for _, resource := range compiled.Resources {
		byID[resource.ID] = resource.Desired
	}
	allowed := map[string]bool{}
	for _, resource := range original.Resources {
		allowed[resource.ID] = true
	}
	allowed["ConfigMap/"+original.Stack+"-video-cloud/mqtt-pki-config"] = true
	allowed["Secret/"+original.Stack+"-video-cloud/mqtt-pki-runtime"] = true
	if len(byID) != 5 || len(definitions) != 5 {
		return createProviderRecoveryBlock("managed host forward cannot omit or duplicate an original owner")
	}
	for id := range byID {
		if !allowed[id] {
			return createProviderRecoveryBlock("managed host forward contains an unrelated resource")
		}
	}
	for _, resource := range original.Resources {
		body := byID[resource.ID]
		if body == nil {
			return createProviderRecoveryBlock("managed host forward lost an original resource")
		}
		if !strings.HasPrefix(resource.ID, "Deployment/") && deploymentplan.Digest(body) != deploymentplan.Digest(resource.Desired) {
			return createProviderRecoveryBlock("managed host forward changed an original OpenBao body")
		}
	}
	return nil
}

func validateManagedTransportForwardActions(original, candidate *deploymentplan.Plan) error {
	if original == nil || candidate == nil || len(original.Resources) != 3 || len(candidate.Resources) != 5 {
		return createProviderRecoveryBlock("managed host forward requires the original three owners and two absent dependency objects")
	}
	owners := map[string]string{}
	for _, resource := range original.Resources {
		owners[resource.ID] = resource.UID
	}
	for _, resource := range candidate.Resources {
		if owner, exists := owners[resource.ID]; exists {
			if owner == "" || resource.UID != owner || resource.Action != "update" && resource.Action != "retain" {
				return createProviderRecoveryBlock("managed host forward changed an original workload owner")
			}
		} else if resource.ID != "ConfigMap/"+original.Stack+"-video-cloud/mqtt-pki-config" && resource.ID != "Secret/"+original.Stack+"-video-cloud/mqtt-pki-runtime" || resource.Action != "create" || resource.UID != "" || resource.Baseline != nil {
			return createProviderRecoveryBlock("managed host forward dependencies must be two qualified new creates")
		}
	}
	return nil
}

func executeManagedTransportPartialForward(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, definitions []deploymentplan.Definition, apply environmentCreateBootstrapApply) error {
	r := runtime.ServiceSessionRecovery
	if r == nil || r.ManagedTransportRecovery == nil || r.ManagedTransportRecovery.Partial == nil || r.Candidate == nil {
		return createProviderRecoveryBlock("qualified original managed host partial continuation unavailable")
	}
	partial := r.ManagedTransportRecovery.Partial
	if err := verifyManagedTransportCurrentPartialBodies(ctx, runtime.Backend, partial); err != nil {
		return err
	}
	if err := validateManagedTransportPartialForwardDefinitions(partial, definitions); err != nil {
		return err
	}
	if partial.PostManaged != nil {
		return runtime.Execution.Check(ctx)
	}
	stage := "transport-managed-hosts-forward-" + r.Review
	if err := apply(ctx, stage, definitions); err != nil {
		return err
	}
	if partial.PriorForward != nil && partial.PriorForward.Previous != nil {
		if err := verifyManagedTransportRestoredOpenBao(ctx, cfg, runtime.Execution.Store, runtime); err != nil {
			return err
		}
	}
	for forward := partial.PriorForward; forward != nil; forward = forward.Previous {
		key := "kubernetes/transport-managed-hosts-forward-" + forward.Review
		if err := runtime.Execution.RecordReceipt(key, mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": forward.Plan.SHA256(), "retained_session_id": r.Ceremony.BootstrapSessionID})); err != nil {
			return err
		}
		if err := runtime.Execution.CompleteEntry(key, time.Now().UTC()); err != nil {
			return err
		}
	}
	if err := runtime.Execution.RecordReceipt("kubernetes/transport-managed-hosts", mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": partial.Original.SHA256(), "retained_session_id": r.Ceremony.BootstrapSessionID})); err != nil {
		return err
	}
	return runtime.Execution.CompleteEntry("kubernetes/transport-managed-hosts", time.Now().UTC())
}
