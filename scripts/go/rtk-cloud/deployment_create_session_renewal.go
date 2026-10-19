package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type createServiceSessionObserved struct {
	Cleanup environmentBootstrapCleanupBinding `json:"cleanup_binding"`
	UIDs    map[string]string                  `json:"credential_uids"`
	Reader  string                             `json:"custody_reader"`
	Session json.RawMessage                    `json:"session"`
}

// Resolve only a stopped child bound to the exact pending parent intent and its
// recorded native lease. Never search another generation or accept a receiptless
// write as completed.
func originalPendingServicePlan(store secretStore, e *environmentCreateExecution, key string, size int) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	var found *deploymentplan.Plan
	var journal deploymentplan.Journal
	var intentSHA string
	for _, entry := range e.Journal.Entries {
		if entry.Key == key && entry.State == "pending" && len(entry.Receipt) == 0 {
			intentSHA = entry.IntentSHA256
		}
	}
	seen := map[string]bool{}
	for _, lease := range e.Journal.KubernetesLeases {
		sha := strings.SplitN(lease.Holder, ":", 2)[0]
		if !environmentPreflightDigest.MatchString(sha) || seen[sha] {
			continue
		}
		seen[sha] = true
		var candidate deploymentplan.Plan
		var state deploymentplan.Journal
		if readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "plan.json"), &candidate) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "journal.json"), &state) != nil || candidate.SHA256() != sha || state.PlanSHA256 != sha || state.Accepted || state.InFlight != "" || state.Failed == "" || len(candidate.Resources) != size || len(state.Owners) != 1 || len(state.Completed) != 0 {
			continue
		}
		intent, err := environmentCreateKubernetesStageIntent(&candidate)
		hash, hashErr := environmentCreatePlanSHA256(intent)
		if err != nil || hashErr != nil || hash != intentSHA {
			continue
		}
		if found != nil {
			return nil, journal, createProviderRecoveryBlock("ambiguous original pending bootstrap child")
		}
		found, journal = &candidate, state
	}
	if found == nil {
		return nil, journal, createProviderRecoveryBlock("exact pending bootstrap child and recorded lease required")
	}
	return found, journal, nil
}

func verifyFailedOriginalKeyRenewal(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend, r *createServiceSessionRecovery, corrected deploymentplan.Definition, parent string) error {
	bad := func() error {
		return createProviderRecoveryBlock("permissions forward recovery requires one unchanged terminal renewal Job, an empty certificate directory and no native signing outcome")
	}
	if r.FailedRenewal == nil || len(r.FailedRenewal.Resources) != 1 {
		return bad()
	}
	resource := r.FailedRenewal.Resources[0]
	old := deploymentplan.Clone(corrected.Object)
	dir := filepath.Dir(r.RenewedCertPath)
	deploymentplan.Map(certIssuerObjectList(environmentPKIPod(old)["initContainers"])[0])["command"] = []any{"/bin/sh", "-ec", "mkdir " + dir + " && chown 10001:10001 " + dir + " && chmod 0700 " + dir}
	if deploymentplan.Digest(old) != deploymentplan.Digest(resource.Desired) || resource.ID != r.FailedRenewalJournal.Failed {
		return bad()
	}
	actual, err := b.Read(ctx, resource.ID)
	if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != r.FailedRenewalJournal.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, actual) {
		return bad()
	}
	status := deploymentplan.Map(actual["status"])
	terminal := false
	for _, value := range certIssuerObjectList(status["conditions"]) {
		c := deploymentplan.Map(value)
		if c["status"] != "True" || c["reason"] != "BackoffLimitExceeded" || c["type"] != "Failed" && c["type"] != "FailureTarget" {
			return bad()
		}
		if c["type"] == "Failed" {
			terminal = true
		}
	}
	if !terminal || status["failed"] != float64(1) || status["active"] != nil && status["active"] != float64(0) || status["succeeded"] != nil && status["succeeded"] != float64(0) {
		return bad()
	}
	ns := cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud"
	name := deploymentplan.String(deploymentplan.Map(actual["metadata"])["name"])
	logs, err := b.command(ctx, nil, "--namespace", ns, "logs", "job/"+name, "--container", deploymentplan.String(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(old)["containers"])[0])["name"]))
	if err != nil || strings.TrimSpace(string(logs)) != "bootstrap material requires real private 0700 directories" {
		return bad()
	}
	if !environmentPreflightDigest.MatchString(parent) {
		return bad()
	}
	_, err = b.command(ctx, nil, "--namespace", ns, "exec", "deployment/pki-bootstrap-custody-"+parent[:12], "--container", "custody", "--", "/bin/sh", "-ec", "test ! -L /run/pki-bootstrap && test ! -L "+dir+" && test -d "+dir+" && test -z \"$(ls -A "+dir+")\"")
	if err != nil {
		return bad()
	}
	return nil
}

func acceptedCreateStage(store secretStore, e *environmentCreateExecution, key string) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	var receipt struct {
		Plan string `json:"plan_sha256"`
	}
	var plan deploymentplan.Plan
	var journal deploymentplan.Journal
	for _, entry := range e.Journal.Entries {
		if entry.Key == key && entry.State == "completed" {
			if json.Unmarshal(entry.Receipt, &receipt) != nil {
				return nil, journal, createProviderRecoveryBlock("accepted checkpoint receipt is malformed")
			}
		}
	}
	if !environmentPreflightDigest.MatchString(receipt.Plan) || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", receipt.Plan, "plan.json"), &plan) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", receipt.Plan, "journal.json"), &journal) != nil || plan.SHA256() != receipt.Plan || journal.PlanSHA256 != receipt.Plan || !journal.Accepted || journal.InFlight != "" || journal.Failed != "" || len(journal.Owners) != len(plan.Resources) || len(journal.Completed) != len(plan.Resources) {
		return nil, journal, createProviderRecoveryBlock("exact accepted checkpoint plan, writes and ownership required")
	}
	return &plan, journal, nil
}

func sessionRenewalUUID(parent, child, old string) string {
	hex := deploymentplan.Digest([]string{"renew-original-bootstrap-key/v1", parent, child, old})[:32]
	return hex[:8] + "-" + hex[8:12] + "-4" + hex[13:16] + "-8" + hex[17:20] + "-" + hex[20:]
}

func prepareExpiredServiceSessionRecovery(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery) (*createServiceSessionRecovery, error) {
	issuer, issuerJournal, err := acceptedCreateStage(store, e, "kubernetes/service-enrollment-issuer")
	if err != nil {
		return nil, err
	}
	if len(issuer.Resources) != 1 || issuer.Resources[0].ID != "Deployment/"+p.Plan.Stack+"-video-cloud/certissuer" {
		return nil, createProviderRecoveryBlock("exact original accepted enrollment issuer required")
	}
	for index, resource := range r.Trust.Resources {
		if resource.ID == issuer.Resources[0].ID {
			if r.TrustJournal.Owners[resource.ID] != issuerJournal.Owners[resource.ID] {
				return nil, createProviderRecoveryBlock("enrollment issuer owner changed")
			}
			r.Trust.Resources[index].Desired = deploymentplan.Clone(issuer.Resources[0].Desired)
		}
	}
	p.Materials["original-service-session-enrollment-issuer"] = issuer.SHA256()
	p.Materials["original-service-session-enrollment-issuer-journal"] = deploymentplan.Digest(issuerJournal)
	if err := verifyExpiredServiceSessionRecovery(ctx, cfg, store, e, b, r); err != nil {
		return nil, err
	}
	var observed createServiceSessionObserved
	path := filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-observed.json")
	if readExecutionJSON(path, &observed) != nil || validateEnvironmentBootstrapCleanupBinding(&observed.Cleanup, cfg.Environment) != nil || observed.Cleanup.SessionID != r.Intent.SessionID || observed.Reader != "pki-bootstrap-custody-"+e.Journal.PlanSHA256[:12] {
		return nil, createProviderRecoveryBlock("original session cleanup and custody binding required")
	}
	r.RenewedIntent = r.Intent
	r.RenewedIntent.SessionID = sessionRenewalUUID(e.Journal.PlanSHA256, r.Original.SHA256(), r.Intent.SessionID)
	r.RenewedCertPath = "/run/pki-bootstrap/renew-" + r.Original.SHA256()[:12] + "/bootstrap.crt"
	// Reuse the exact accepted one-shot recipe and original persistent key/CSR.
	var boot *deploymentplan.Plan
	var bootJournal deploymentplan.Journal
	for _, entry := range e.Journal.Entries {
		if strings.HasPrefix(entry.Key, "kubernetes/service-session-order-recovery-") && entry.State == "completed" {
			boot, bootJournal, err = acceptedCreateStage(store, e, entry.Key)
		}
	}
	if err != nil || boot == nil {
		return nil, createProviderRecoveryBlock("original accepted bootstrap executor unavailable")
	}
	p.Materials["original-service-session-bootstrap-executor"] = boot.SHA256()
	p.Materials["original-service-session-bootstrap-executor-journal"] = deploymentplan.Digest(bootJournal)
	var defs []deploymentplan.Definition
	for _, resource := range boot.Resources {
		if resource.Desired["kind"] != "Job" {
			actual, readErr := b.Read(ctx, resource.ID)
			if readErr != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != bootJournal.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, actual) {
				return nil, createProviderRecoveryBlock("original bootstrap claim or ServiceAccount changed")
			}
			continue
		}
		definition, err := originalKeyRenewalDefinition(resource.Desired, e.Journal.PlanSHA256, r.Original.SHA256(), r.RenewedIntent.SessionID)
		if err != nil {
			return nil, err
		}
		if r.PermissionsForward {
			if err := verifyFailedOriginalKeyRenewal(ctx, cfg, b, r, definition, e.Journal.PlanSHA256); err != nil {
				return nil, err
			}
			deploymentplan.Map(definition.Object["metadata"])["name"] = "pki-bootstrap-renew-" + r.Original.SHA256()[:12] + "-f" + r.FailedRenewal.SHA256()[:6]
			p.Materials["original-failed-bootstrap-renewal-plan"] = r.FailedRenewal.SHA256()
			p.Materials["original-failed-bootstrap-renewal-journal"] = deploymentplan.Digest(r.FailedRenewalJournal)
		}
		defs = append(defs, definition)
	}
	if len(defs) != 1 {
		return nil, createProviderRecoveryBlock("one original-key renewal executor required")
	}
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return nil, err
	}
	r.Candidate, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("bootstrap-original-key-renewal", defs)}, defs, live)
	if err != nil {
		return nil, err
	}
	if len(r.Candidate.Resources) != 1 || r.Candidate.Resources[0].Action != "create" {
		return nil, createProviderRecoveryBlock("renewal executor already exists; inspect its actual outcome instead of replaying it")
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil {
		return nil, err
	}
	p.Materials["original-service-session-observed"] = environmentPreflightBytesSHA256(raw)
	p.Materials["original-service-session-plan"] = r.Original.SHA256()
	p.Materials["original-service-session-journal"] = deploymentplan.Digest(r.Journal)
	p.Materials["original-service-session-intent"] = deploymentplan.Digest(r.Intent)
	p.Materials["original-service-session-forward-plan"] = r.Candidate.SHA256()
	for _, filename := range []string{"bootstrap.key", "bootstrap.csr", "bootstrap.crt"} {
		raw, err := b.command(ctx, nil, "--namespace", p.Plan.Stack+"-video-cloud", "exec", "deployment/"+observed.Reader, "--container", "custody", "--", "cat", "/run/pki-bootstrap/"+filename)
		if err != nil || len(raw) == 0 {
			return nil, createProviderRecoveryBlock("original bootstrap custody file unavailable")
		}
		p.Materials["original-service-session-custody-"+filename] = environmentPreflightBytesSHA256(raw)
		if filename == "bootstrap.key" {
			r.OriginalKeySHA = environmentPreflightBytesSHA256(raw)
		}
		if filename == "bootstrap.csr" {
			r.OriginalCSRSHA = environmentPreflightBytesSHA256(raw)
		}
	}
	return r, nil
}

func originalKeyRenewalDefinition(original deploymentplan.Object, parent, child, session string) (deploymentplan.Definition, error) {
	bad := func() (deploymentplan.Definition, error) {
		return deploymentplan.Definition{}, createProviderRecoveryBlock("exact original-key bootstrap recipe required")
	}
	if !environmentPreflightDigest.MatchString(parent) || !environmentPreflightDigest.MatchString(child) || !environmentBootstrapOwnerUUID.MatchString(session) || original["kind"] != "Job" {
		return bad()
	}
	object := deploymentplan.Clone(original)
	pod := environmentPKIPod(object)
	containers := certIssuerObjectList(pod["containers"])
	initializers := certIssuerObjectList(pod["initContainers"])
	if len(containers) != 1 || len(initializers) != 1 {
		return bad()
	}
	container := deploymentplan.Map(containers[0])
	if mqttFoundationEnv(container, "PKI_BOOTSTRAP_KEY_PATH") != "/run/pki-bootstrap/bootstrap.key" || mqttFoundationEnv(container, "PKI_BOOTSTRAP_CSR_PATH") != "/run/pki-bootstrap/bootstrap.csr" || mqttFoundationEnv(container, "PKI_BOOTSTRAP_CERT_PATH") != "/run/pki-bootstrap/bootstrap.crt" {
		return bad()
	}
	path := "/run/pki-bootstrap/renew-" + child[:12] + "/bootstrap.crt"
	directory := filepath.Dir(path)
	deploymentplan.Map(initializers[0])["command"] = []any{"/bin/sh", "-ec", "test ! -L /run/pki-bootstrap && test ! -L " + directory + " && chown 10001:10001 /run/pki-bootstrap && chmod 0700 /run/pki-bootstrap && for f in bootstrap.key bootstrap.csr bootstrap.crt bootstrap.lock; do test -f /run/pki-bootstrap/$f && test ! -L /run/pki-bootstrap/$f && chmod 0600 /run/pki-bootstrap/$f || exit 1; done && mkdir -p " + directory + " && chown 10001:10001 " + directory + " && chmod 0700 " + directory}
	deploymentplan.Map(object["metadata"])["name"] = "pki-bootstrap-renew-" + child[:12]
	container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": session, "PKI_BOOTSTRAP_CERT_PATH": path, "PKI_BOOTSTRAP_DEPLOYMENT_ID": "create-" + parent[:20] + "-r" + child[:12]}))
	return deploymentplan.Definition{Phase: "bootstrap", Object: object}, nil
}

func verifyExpiredServiceSessionRecovery(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery) error {
	bad := func() error {
		return createProviderRecoveryBlock("expired bootstrap recovery requires original owners, an unexecuted enrollment, no issued workload identity and an expired unused session")
	}
	for _, resource := range r.Original.Resources {
		actual, err := b.Read(ctx, resource.ID)
		if err != nil {
			return err
		}
		if resource.Desired["kind"] == "PersistentVolumeClaim" {
			if resource.ID != "PersistentVolumeClaim/"+cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud/certissuer-service-state" || actual != nil || r.Journal.Owners[resource.ID] != "" {
				return bad()
			}
			continue
		}
		if resource.Desired["kind"] != "Job" || resource.ID != r.Journal.Failed || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != r.Journal.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, actual) {
			return bad()
		}
		status := deploymentplan.Map(actual["status"])
		terminal := false
		for _, value := range certIssuerObjectList(status["conditions"]) {
			condition := deploymentplan.Map(value)
			if condition["status"] != "True" || condition["reason"] != "DeadlineExceeded" {
				return bad()
			}
			if condition["type"] == "Failed" {
				terminal = true
			}
		}
		if !terminal || status["active"] != nil && status["active"] != float64(0) || status["succeeded"] != nil && status["succeeded"] != float64(0) {
			return bad()
		}
		raw, err := b.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud", "get", "pods", "-l", "job-name="+deploymentplan.String(deploymentplan.Map(actual["metadata"])["name"]), "-o", "json")
		var pods struct {
			Items []deploymentplan.Object `json:"items"`
		}
		if err != nil || json.Unmarshal(raw, &pods) != nil {
			return bad()
		}
		for _, pod := range pods.Items {
			state := deploymentplan.Map(pod["status"])
			if len(certIssuerObjectList(state["containerStatuses"])) != 0 || len(certIssuerObjectList(state["initContainerStatuses"])) != 0 {
				return bad()
			}
		}
	}
	for _, resource := range r.Trust.Resources {
		actual, err := b.Read(ctx, resource.ID)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != r.TrustJournal.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, actual) {
			return bad()
		}
	}
	query := "SELECT json_build_object('count',(SELECT count(*) FROM pki_deployment_bootstrap_sessions),'servers',(SELECT count(*) FROM pki_server_issuances),'clients',(SELECT count(*) FROM pki_service_client_issuances),'session_id',session_id,'issuer_id',issuer_id,'root',root_sha256,'caller',caller,'fingerprint',bootstrap_fingerprint,'subjects',subjects,'installed',installed_subjects,'status',status,'expires',expires_at) FROM pki_deployment_bootstrap_sessions WHERE session_id='" + r.Intent.SessionID + "'"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b}, query)
	var native struct {
		Count, Servers, Clients           int
		Session                           string `json:"session_id"`
		Issuer                            string `json:"issuer_id"`
		Root, Caller, Fingerprint, Status string
		Subjects, Installed               []string
		Expires                           time.Time
	}
	if err != nil || json.Unmarshal(raw, &native) != nil || native.Count != 1 || native.Servers != 0 || native.Clients != 1 || native.Session != r.Intent.SessionID || native.Issuer != r.Ceremony.Intermediate.ID || native.Root != r.Ceremony.Root.RootFingerprint || native.Caller != "service:deployment-bootstrap" || len(native.Installed) != 0 || !slices.Equal(native.Subjects, r.Intent.Subjects) || native.Status != "active" && native.Status != "expired" || !native.Expires.Before(time.Now().UTC()) {
		return bad()
	}
	var credential map[string]string
	var original createServiceSessionObserved
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-observed.json"), &original) != nil {
		return bad()
	}
	for _, ns := range r.Intent.Namespaces {
		secret, err := b.Read(ctx, "Secret/"+ns+"/service-bootstrap-"+r.Intent.SessionID)
		if err != nil || secret == nil || deploymentplan.Map(secret["metadata"])["uid"] != original.UIDs[ns] {
			return bad()
		}
		data := deploymentplan.Map(secret["data"])
		values := map[string]string{}
		for _, name := range []string{"tls.crt", "tls.key", "ca.crt"} {
			decoded, err := base64.StdEncoding.DecodeString(deploymentplan.String(data[name]))
			if err != nil {
				return bad()
			}
			values[name] = string(decoded)
		}
		if credential != nil && deploymentplan.Digest(credential) != deploymentplan.Digest(values) {
			return bad()
		}
		credential = values
	}
	identity, err := tls.X509KeyPair([]byte(credential["tls.crt"]), []byte(credential["tls.key"]))
	if err != nil || len(identity.Certificate) < 2 {
		return bad()
	}
	leaf, err := x509.ParseCertificate(identity.Certificate[0])
	if err != nil || leaf.Subject.CommonName != "service:deployment-bootstrap" || leaf.NotAfter.After(time.Now().UTC()) || environmentPreflightBytesSHA256(leaf.Raw) != native.Fingerprint || credential["ca.crt"] != r.Ceremony.Root.Certificate {
		return bad()
	}
	reader := "pki-bootstrap-custody-" + e.Journal.PlanSHA256[:12]
	for name, want := range map[string]string{"bootstrap.key": credential["tls.key"], "bootstrap.crt": credential["tls.crt"]} {
		actual, err := b.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud", "exec", "deployment/"+reader, "--container", "custody", "--", "cat", "/run/pki-bootstrap/"+name)
		if err != nil || string(actual) != want {
			return bad()
		}
	}
	csrPEM, err := b.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud", "exec", "deployment/"+reader, "--container", "custody", "--", "cat", "/run/pki-bootstrap/bootstrap.csr")
	block, _ := pem.Decode(csrPEM)
	if err != nil || block == nil || block.Type != "CERTIFICATE REQUEST" {
		return bad()
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil || csr.Subject.CommonName != leaf.Subject.CommonName || !bytes.Equal(csr.RawSubjectPublicKeyInfo, leaf.RawSubjectPublicKeyInfo) {
		return bad()
	}
	return e.Check(ctx)
}

func executeOriginalKeySessionRenewal(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, apply environmentCreateBootstrapApply) (environmentCreateServiceCeremony, error) {
	r := runtime.ServiceSessionRecovery
	if r == nil || !r.Renewal || !environmentPreflightDigest.MatchString(r.Review) {
		return s, environmentCreateBootstrapBlocker("qualified original-key renewal required")
	}
	s.BootstrapSessionID = r.RenewedIntent.SessionID
	s.BootstrapClaim = r.Intent.Claim
	s.BootstrapCredentialNamespace = r.Intent.Namespaces[0]
	s.BootstrapCredentialNamespaces = slices.Clone(r.Intent.Namespaces)
	s.BootstrapCredentialSecret = "service-bootstrap-" + s.BootstrapSessionID
	s.CustodyReader = "pki-bootstrap-custody-" + runtime.ExecutionSHA256[:12]
	if _, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join("deployment/create", runtime.ExecutionSHA256, "bootstrap/service/session-renewal", r.Review, "intent.json"), r.RenewedIntent); err != nil {
		return s, err
	}
	raw, err := runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/pki-controller", "--container", "pki-controller", "--", "/app/pkicontroller", "render-openbao-policy", s.Intermediate.ID)
	var policy environmentCreateOpenBaoIssuerPolicies
	if err != nil || decodeEnvironmentJSON(raw, &policy) != nil || policy.IssuerID != s.Intermediate.ID || policy.Environment != cfg.Environment || policy.ServiceClientSignerPolicy == "" {
		return s, environmentCreateBootstrapBlocker("original active signer policy unavailable")
	}
	name := "rtk-" + cfg.Environment + "-service-bootstrap-" + s.BootstrapSessionID
	if !r.PermissionsForward {
		if err := installEnvironmentCreateOpenBaoPolicy(ctx, cfg, runtime, name, policy.ServiceClientSignerPolicy); err != nil {
			return s, err
		}
		if err := configureEnvironmentCreateOpenBaoRole(ctx, cfg, runtime, "pki-service-bootstrap-"+cfg.Environment, []string{"pki-service-bootstrap"}, []string{name}); err != nil {
			return s, err
		}
	}
	var defs []deploymentplan.Definition
	for _, resource := range r.Candidate.Resources {
		defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: deploymentplan.Clone(resource.Desired)})
	}
	if err := apply(ctx, "service-session-original-key-renewal-"+r.Review, defs); err != nil {
		return s, err
	}
	if r.PermissionsForward {
		if err := runtime.Execution.RecordReceipt(r.FailedRenewalKey, mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/service-session-original-key-renewal-" + r.Review, "original_failed_plan_sha256": r.FailedRenewal.SHA256(), "renewed_session_id": s.BootstrapSessionID})); err != nil {
			return s, err
		}
		if err := runtime.Execution.CompleteEntry(r.FailedRenewalKey, time.Now().UTC()); err != nil {
			return s, err
		}
	}
	return s, nil
}

func renewalStage(runtime environmentCreateBootstrapRuntime, stage string) string {
	if r := runtime.ServiceSessionRecovery; r != nil && r.Renewal {
		return fmt.Sprintf("%s-renewal-%s", stage, r.Review)
	}
	return stage
}

// The formal native cleanup program deliberately accepts only the canonical
// three files in one locked directory. Preserve the expired public certificate
// before linking the genuinely finalized renewed certificate into that slot.
// No private key is copied, replaced or regenerated.
func prepareRenewedBootstrapCleanup(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, finalized environmentCreateBootstrapFinalization) error {
	r := runtime.ServiceSessionRecovery
	if r == nil || !r.Renewal {
		return nil
	}
	if finalized.SessionID != r.RenewedIntent.SessionID || finalized.SessionID != s.BootstrapSessionID || finalized.Environment != cfg.Environment || finalized.FinalizedAt.IsZero() || !environmentPreflightDigest.MatchString(finalized.LeafSHA256) || !environmentBootstrapOwnerUUID.MatchString(r.Intent.SessionID) {
		return environmentCreateBootstrapBlocker("actual renewed session finalization required before custody cleanup")
	}
	base := "/run/pki-bootstrap/"
	material := map[string][]byte{}
	for name, path := range map[string]string{"key": base + "bootstrap.key", "csr": base + "bootstrap.csr", "expired": base + "bootstrap.crt", "renewed": r.RenewedCertPath} {
		raw, err := runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "cat", path)
		if err != nil {
			return environmentCreateBootstrapBlocker("original renewal custody cannot be inspected before native cleanup")
		}
		material[name] = raw
	}
	if environmentPreflightBytesSHA256(material["key"]) != r.OriginalKeySHA || environmentPreflightBytesSHA256(material["csr"]) != r.OriginalCSRSHA {
		return environmentCreateBootstrapBlocker("original bootstrap key or CSR changed before cleanup")
	}
	identity, err := tls.X509KeyPair(material["renewed"], material["key"])
	if err != nil || len(identity.Certificate) < 2 || environmentPreflightBytesSHA256(identity.Certificate[0]) != finalized.LeafSHA256 || environmentPreflightBytesSHA256(identity.Certificate[len(identity.Certificate)-1]) != s.Root.RootFingerprint {
		return environmentCreateBootstrapBlocker("renewed certificate differs from real finalized native lineage")
	}
	leaf, err := x509.ParseCertificate(identity.Certificate[0])
	if err != nil {
		return environmentCreateBootstrapBlocker("actual finalized bootstrap leaf is malformed")
	}
	block, _ := pem.Decode(material["expired"])
	if block == nil {
		return environmentCreateBootstrapBlocker("original expired certificate unavailable")
	}
	old, err := x509.ParseCertificate(block.Bytes)
	if err != nil || old.NotAfter.After(time.Now().UTC()) || old.Subject.CommonName != "service:deployment-bootstrap" || !bytes.Equal(old.RawSubjectPublicKeyInfo, leaf.RawSubjectPublicKeyInfo) {
		return environmentCreateBootstrapBlocker("original expired certificate belongs to another bootstrap key")
	}
	archive := base + "expired-" + r.Intent.SessionID
	proof := deploymentplan.Object{"session_id": s.BootstrapSessionID, "original_session_id": r.Intent.SessionID, "expired_certificate_sha256": environmentPreflightBytesSHA256(material["expired"]), "renewed_certificate_sha256": environmentPreflightBytesSHA256(material["renewed"]), "archive_path": archive + "/bootstrap.crt", "key_sha256": r.OriginalKeySHA, "csr_sha256": r.OriginalCSRSHA}
	key := "bootstrap-renewed-cleanup-custody-" + r.Review
	if err := runtime.Execution.BeginEntry(key, "bootstrap", mustProviderRecoveryJSON(proof), time.Now().UTC()); err != nil {
		return err
	}
	script := "test ! -L /run/pki-bootstrap && test ! -L /run/pki-bootstrap/bootstrap.crt && test ! -L " + r.RenewedCertPath + " && test ! -e " + archive + " && printf '%s  %s\\n' '" + deploymentplan.String(proof["expired_certificate_sha256"]) + "' '/run/pki-bootstrap/bootstrap.crt' '" + deploymentplan.String(proof["renewed_certificate_sha256"]) + "' '" + r.RenewedCertPath + "' | sha256sum -c - >/dev/null && mkdir " + archive + " && chmod 0700 " + archive + " && ln /run/pki-bootstrap/bootstrap.crt " + archive + "/bootstrap.crt && rm /run/pki-bootstrap/bootstrap.crt && ln " + r.RenewedCertPath + " /run/pki-bootstrap/bootstrap.crt"
	if _, err := runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "/bin/sh", "-ec", script); err != nil {
		return environmentCreateBootstrapBlocker("renewed certificate cleanup preparation is partial; original evidence retained for reconciliation")
	}
	for path, want := range map[string][]byte{archive + "/bootstrap.crt": material["expired"], base + "bootstrap.crt": material["renewed"]} {
		actual, err := runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "cat", path)
		if err != nil || !bytes.Equal(actual, want) {
			return environmentCreateBootstrapBlocker("actual archived and finalized certificate bytes differ")
		}
	}
	if err := runtime.Execution.RecordReceipt(key, mustProviderRecoveryJSON(proof)); err != nil {
		return err
	}
	return runtime.Execution.CompleteEntry(key, time.Now().UTC())
}

func retireExpiredServiceBootstrapCopies(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, current environmentCreateServiceCeremony) error {
	r := runtime.ServiceSessionRecovery
	if r == nil || !r.Renewal {
		return nil
	}
	var original createServiceSessionObserved
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create", runtime.ExecutionSHA256, "bootstrap/service/session-observed.json"), &original) != nil || original.Cleanup.SessionID != r.Intent.SessionID {
		return environmentCreateBootstrapBlocker("original expired credential owners unavailable")
	}
	expired := []createServiceSessionObserved{original}
	if len(r.StaleSeeds) > 0 {
		expired = append(expired, r.StaleSeeds...)
	} else if r.StaleRenewal != nil {
		expired = append(expired, *r.StaleRenewal)
	}
	if r.ManagedLateSession != nil {
		if err := prepareManagedLateOriginalSeedRetirement(store, runtime.Execution, r, r.ManagedLateSession); err != nil {
			return err
		}
		// Rebuild after adding the exact e0 observation; the first unused seed is unchanged.
		expired = append([]createServiceSessionObserved{original}, r.StaleSeeds...)
	}
	for _, observed := range expired {
		sessionID := observed.Cleanup.SessionID
		query := "SELECT json_build_object('status',status,'issuer_id',issuer_id,'root_sha256',root_sha256,'installed',installed_subjects) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND session_id='" + sessionID + "'"
		raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
		var session struct {
			Status    string   `json:"status"`
			Issuer    string   `json:"issuer_id"`
			Root      string   `json:"root_sha256"`
			Installed []string `json:"installed"`
		}
		if err != nil || decodeEnvironmentJSON(raw, &session) != nil || session.Status != "expired" || session.Issuer != current.Intermediate.ID || session.Root != current.Root.RootFingerprint {
			return environmentCreateBootstrapBlocker("only original expired unused bootstrap copies may be archived and retired")
		}
		known := len(session.Installed) == 0
		if r.ApplicationForward {
			known = true
			for _, subject := range session.Installed {
				allowed := []string{"service:account-manager", "service:certissuer", "service:pki-controller"}
				if r.PostServiceResume {
					allowed = r.Intent.Subjects
				}
				if !slices.Contains(allowed, subject) {
					known = false
				}
			}
		}
		if !known {
			var proofErr error
			known, proofErr = verifyManagedHistoricalSeedCleanup(ctx, cfg, store, runtime, current, observed)
			if proofErr != nil {
				return proofErr
			}
		}
		if !known {
			return environmentCreateBootstrapBlocker("expired seed has unqualified workload installation history")
		}
		old := current
		old.BootstrapSessionID, old.BootstrapCredentialSecret, old.CredentialUIDs = sessionID, "service-bootstrap-"+sessionID, observed.UIDs
		for _, namespace := range r.Intent.Namespaces {
			if completed, err := managedLateCompletedCredentialRetirement(ctx, cfg, runtime, old, namespace); err != nil {
				return err
			} else if completed {
				continue
			}
			secret, err := runtime.Backend.Read(ctx, "Secret/"+namespace+"/"+old.BootstrapCredentialSecret)
			if err != nil || secret == nil || deploymentplan.Map(secret["metadata"])["uid"] != old.CredentialUIDs[namespace] {
				return environmentCreateBootstrapBlocker("original expired namespace copy owner changed")
			}
			if _, err := writeEnvironmentCreateExpiredCredentialArchive(ctx, cfg, store, runtime, old, namespace, secret); err != nil {
				return err
			}
			if err := removeEnvironmentCreateCredentialCopy(ctx, cfg, runtime, old, namespace, true); err != nil {
				return err
			}
		}
	}
	return nil
}

type completedBootstrapExpiry struct {
	Session  string    `json:"session_id"`
	Issuer   string    `json:"issuer_id"`
	Root     string    `json:"root_sha256"`
	Status   string    `json:"status"`
	Expires  time.Time `json:"expires_at"`
	Complete bool      `json:"complete"`
	Others   int       `json:"other_active"`
}

func completedBootstrapMayExpire(state completedBootstrapExpiry, s environmentCreateServiceCeremony, now time.Time) bool {
	return state.Session == s.BootstrapSessionID && state.Issuer == s.Intermediate.ID && state.Root == s.Root.RootFingerprint && state.Status == "active" && !state.Expires.IsZero() && !state.Expires.After(now) && state.Complete && state.Others == 0
}

// Complete installations may outlive the seed's deliberately short lifetime
// while other domains finish. Close its real expired clock through the native
// lifecycle; never extend its lifetime or manufacture an installation receipt.
func expireCompletedRenewedBootstrapSession(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony) error {
	if runtime.ServiceSessionRecovery == nil || !runtime.ServiceSessionRecovery.Renewal {
		return nil
	}
	query := "SELECT json_build_object('session_id',session_id,'issuer_id',issuer_id,'root_sha256',root_sha256,'status',status,'expires_at',expires_at,'complete',subjects <@ installed_subjects,'other_active',(SELECT count(*) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND status='active' AND session_id<>'" + s.BootstrapSessionID + "')) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND session_id='" + s.BootstrapSessionID + "'"
	read := func() (completedBootstrapExpiry, error) {
		var state completedBootstrapExpiry
		raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
		if err != nil || decodeEnvironmentJSON(raw, &state) != nil {
			return state, environmentCreateBootstrapBlocker("native renewed bootstrap expiry state unavailable")
		}
		return state, nil
	}
	before, err := read()
	if err != nil {
		return err
	}
	if before.Session != s.BootstrapSessionID || before.Issuer != s.Intermediate.ID || before.Root != s.Root.RootFingerprint || before.Expires.IsZero() {
		return environmentCreateBootstrapBlocker("renewed bootstrap finalization lineage differs")
	}
	if before.Status != "active" || before.Expires.After(time.Now().UTC()) {
		return nil
	}
	if !completedBootstrapMayExpire(before, s, time.Now().UTC()) {
		return environmentCreateBootstrapBlocker("expired bootstrap must have real complete installation receipts and no competing active session before native expiry finalization")
	}
	key := "bootstrap-completed-expiry-" + s.BootstrapSessionID
	if err := runtime.Execution.BeginEntry(key, "bootstrap", mustProviderRecoveryJSON(before), time.Now().UTC()); err != nil {
		return err
	}
	if _, err := runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "/app/serviceidentity-bootstrap", "expire"); err != nil {
		return environmentCreateBootstrapBlocker("native completed bootstrap expiry outcome requires reconciliation")
	}
	after, err := read()
	if err != nil || after.Session != before.Session || after.Issuer != before.Issuer || after.Root != before.Root || after.Expires != before.Expires || after.Status != "expired" || !after.Complete || after.Others != 0 {
		return environmentCreateBootstrapBlocker("actual completed bootstrap native expiry not verified")
	}
	if err := runtime.Execution.RecordReceipt(key, mustProviderRecoveryJSON(after)); err != nil {
		return err
	}
	return runtime.Execution.CompleteEntry(key, time.Now().UTC())
}
