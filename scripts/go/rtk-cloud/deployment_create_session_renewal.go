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
	"os"
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
	if key == "kubernetes/workload" {
		entry := createOpenBaoACKCompletionEntry(e.Journal, key)
		if entry != nil && entry.State == "completed" && workloadImageForwardReceiptMatches(entry.Receipt) {
			original, err := readEnvironmentCreateStageInput(store, e, "workload")
			if err != nil || original == nil {
				return nil, deploymentplan.Journal{}, createProviderRecoveryBlock("original workload input unavailable")
			}
			j, exists, err := original.ReadExecution(store.Root)
			if err != nil || !exists {
				return nil, j, createProviderRecoveryBlock("original workload journal unavailable")
			}
			if err = validateWorkloadImageForwardReceipt(store, e, original, j, entry.Receipt); err != nil {
				return nil, j, err
			}
			return acceptedCreateStage(store, e, "kubernetes/"+createWorkloadImageForwardStage)
		}
	}

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
	// A normally completed first session needs no earlier ordering failure.
	// An existing completed ordering recovery remains authoritative; a malformed
	// or ambiguous recovery cannot fall back to the earlier normal stage.
	bootStage := "kubernetes/service-session"
	for _, entry := range e.Journal.Entries {
		if strings.HasPrefix(entry.Key, "kubernetes/service-session-order-recovery-") && entry.State == "completed" {
			if bootStage != "kubernetes/service-session" {
				return nil, createProviderRecoveryBlock("ambiguous accepted bootstrap executors")
			}
			bootStage = entry.Key
		}
	}
	boot, bootJournal, err := acceptedCreateStage(store, e, bootStage)
	if err != nil {
		return nil, createProviderRecoveryBlock("original accepted bootstrap executor unavailable")
	}
	bootIntent, intentErr := environmentCreateKubernetesStageIntent(boot)
	bootIntentSHA, hashErr := environmentCreatePlanSHA256(bootIntent)
	bound := 0
	for _, entry := range e.Journal.Entries {
		if entry.Key == bootStage && entry.State == "completed" {
			if intentErr != nil || hashErr != nil || entry.IntentSHA256 != bootIntentSHA {
				return nil, createProviderRecoveryBlock("original bootstrap executor parent binding changed")
			}
			bound++
		}
	}
	normal := bootStage == "kubernetes/service-session"
	if bound != 1 || normal && (len(boot.Resources) != 3 || boot.Inputs.Environment != cfg.Environment || boot.Inputs.Stack != p.Plan.Stack || boot.Inputs.ConfigurationSHA256 != p.Plan.ConfigurationSHA256) {
		return nil, createProviderRecoveryBlock("exact original accepted bootstrap executor required")
	}
	p.Materials["original-service-session-bootstrap-executor"] = boot.SHA256()
	p.Materials["original-service-session-bootstrap-executor-journal"] = deploymentplan.Digest(bootJournal)
	var defs []deploymentplan.Definition
	for _, resource := range boot.Resources {
		if resource.Desired["kind"] != "Job" {
			if normal && resource.ID != "PersistentVolumeClaim/"+p.Plan.Stack+"-video-cloud/"+r.Intent.Claim && resource.ID != "ServiceAccount/"+p.Plan.Stack+"-video-cloud/pki-service-bootstrap" {
				return nil, createProviderRecoveryBlock("original bootstrap prerequisites differ from session intent")
			}
			actual, readErr := b.Read(ctx, resource.ID)
			if readErr != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != bootJournal.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, actual) {
				return nil, createProviderRecoveryBlock("original bootstrap claim or ServiceAccount changed")
			}
			continue
		}
		pod := environmentPKIPod(resource.Desired)
		containers := certIssuerObjectList(pod["containers"])
		if len(containers) != 1 || mqttFoundationEnv(deploymentplan.Map(containers[0]), "PKI_BOOTSTRAP_SESSION_ID") != r.Intent.SessionID {
			return nil, createProviderRecoveryBlock("original bootstrap executor session changed")
		}
		if normal && (deploymentplan.Map(resource.Desired["metadata"])["namespace"] != p.Plan.Stack+"-video-cloud" || pod["serviceAccountName"] != "pki-service-bootstrap" || deploymentplan.Map(managedNamedField(pod["volumes"], "bootstrap-state")["persistentVolumeClaim"])["claimName"] != r.Intent.Claim) {
			return nil, createProviderRecoveryBlock("original bootstrap executor custody changed")
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
	if runtime.Execution == nil || runtime.Execution.Store.Root != store.Root || runtime.Execution.Store.ConfigRoot != store.ConfigRoot || runtime.Execution.Store.Environment != store.Environment || !environmentPreflightDigest.MatchString(runtime.ExecutionSHA256) || runtime.ExecutionSHA256 != runtime.Execution.Journal.PlanSHA256 || len(r.Intent.Namespaces) == 0 {
		return s, environmentCreateBootstrapBlocker("original selected renewal execution is required")
	}
	if err := runtime.Execution.Check(ctx); err != nil {
		return s, err
	}
	s.BootstrapSessionID = r.RenewedIntent.SessionID
	s.BootstrapClaim = r.Intent.Claim
	s.BootstrapCredentialNamespace = r.Intent.Namespaces[0]
	s.BootstrapCredentialNamespaces = slices.Clone(r.Intent.Namespaces)
	s.BootstrapCredentialSecret = "service-bootstrap-" + s.BootstrapSessionID
	s.CustodyReader = "pki-bootstrap-custody-" + runtime.ExecutionSHA256[:12]
	relative := filepath.Join("deployment/create", runtime.ExecutionSHA256, "bootstrap/service/session-renewal", r.Review, "intent.json")
	path, pathErr := privateDeploymentStatePath(store.Environment, relative)
	if pathErr != nil || path != filepath.Join(store.Root, relative) {
		return s, environmentCreateBootstrapBlocker("original selected renewal intent path is unavailable")
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		if _, err := writeEnvironmentCreateBootstrapJSON(store, relative, r.RenewedIntent); err != nil {
			return s, err
		}
	} else {
		// A child readiness interruption retains its immutable intent. Resume
		// only the same private bytes, before any native policy or Job effects.
		if err != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
			return s, environmentCreateBootstrapBlocker("original renewal intent changed or cannot be inspected")
		}
		expected, encodeErr := json.Marshal(r.RenewedIntent)
		retained, readErr := readBoundedEnvironmentFile(path, true)
		if encodeErr != nil || readErr != nil || !bytes.Equal(retained, expected) {
			return s, environmentCreateBootstrapBlocker("original renewal intent changed or cannot be inspected")
		}
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
	if r.SecondSession {
		return prepareSecondSessionBootstrapCleanup(ctx, cfg, runtime, s, finalized)
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
	var expired []createServiceSessionObserved
	if r.SecondSession {
		ancestors, _, err := secondSessionBootstrapCleanupAncestors(ctx, cfg, store, runtime, current)
		if err != nil {
			return err
		}
		expired = ancestors
	} else {
		if readExecutionJSON(filepath.Join(store.Root, "deployment/create", runtime.ExecutionSHA256, "bootstrap/service/session-observed.json"), &original) != nil || original.Cleanup.SessionID != r.Intent.SessionID {
			return environmentCreateBootstrapBlocker("original expired credential owners unavailable")
		}
		expired = []createServiceSessionObserved{original}
	}
	if !r.SecondSession && len(r.StaleSeeds) > 0 {
		expired = append(expired, r.StaleSeeds...)
	} else if !r.SecondSession && r.StaleRenewal != nil {
		expired = append(expired, *r.StaleRenewal)
	}
	if !r.SecondSession && r.ManagedLateSession != nil {
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
		// Second-session ancestors were checked against their exact admitted
		// native snapshots, including installed subjects, before any mutation.
		known := r.SecondSession || len(session.Installed) == 0
		if !r.SecondSession && r.ApplicationForward {
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
		old.CleanupBinding = observed.Cleanup
		for _, namespace := range r.Intent.Namespaces {
			if completed, err := completedRenewedBootstrapCredentialRetirement(ctx, cfg, runtime, old, namespace); err != nil {
				return err
			} else if completed {
				continue
			}
			secret, err := runtime.Backend.Read(ctx, "Secret/"+namespace+"/"+old.BootstrapCredentialSecret)
			if err != nil || secret == nil || deploymentplan.Map(secret["metadata"])["uid"] != old.CredentialUIDs[namespace] {
				return environmentCreateBootstrapBlocker("original expired namespace copy owner changed")
			}
			if r.SecondSession {
				data := deploymentplan.Map(secret["data"])
				key, keyErr := base64.StdEncoding.DecodeString(deploymentplan.String(data["tls.key"]))
				certificate, certErr := base64.StdEncoding.DecodeString(deploymentplan.String(data["tls.crt"]))
				root, rootErr := base64.StdEncoding.DecodeString(deploymentplan.String(data["ca.crt"]))
				leaf, leafErr := parseFirstCertificatePEM(certificate)
				var saved managedSessionState
				if keyErr != nil || certErr != nil || rootErr != nil || leafErr != nil || decodeEnvironmentJSON(observed.Session, &saved) != nil || environmentPreflightBytesSHA256(key) != r.OriginalKeySHA || environmentPreflightBytesSHA256(leaf.Raw) != saved.Fingerprint || string(root) != current.Root.Certificate {
					return environmentCreateBootstrapBlocker("expired ancestor credential differs from its original same-key observation")
				}
				relative := filepath.Join("deployment/create", runtime.ExecutionSHA256, "bootstrap/service/session-renewal", r.Review, "expired-credential-copies", sessionID, namespace+".json")
				path, pathErr := store.safePath(relative)
				if pathErr != nil {
					return pathErr
				}
				if _, err := os.Lstat(path); err == nil {
					if _, err := reuseManagedLateExpiredCredentialArchive(store, relative, secret); err != nil {
						return err
					}
				} else if !os.IsNotExist(err) {
					return err
				} else if _, err := writeEnvironmentCreateExpiredCredentialArchive(ctx, cfg, store, runtime, old, namespace, secret); err != nil {
					return err
				}
			} else if _, err := writeEnvironmentCreateExpiredCredentialArchive(ctx, cfg, store, runtime, old, namespace, secret); err != nil {
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
			if ctx.Err() != nil {
				return state, fmt.Errorf("%w: %v", environmentCreateBootstrapBlocker("native renewed bootstrap expiry state unavailable"), ctx.Err())
			}
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

// Each expired copy belongs to an authenticated predecessor of the completed
// current session. Its observation and UID map are independent of the latest
// runtime's Intent, which names only the immediate predecessor.
func secondSessionBootstrapCleanupAncestors(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, current environmentCreateServiceCeremony) ([]createServiceSessionObserved, string, error) {
	bad := func() ([]createServiceSessionObserved, string, error) {
		return nil, "", environmentCreateBootstrapBlocker("bootstrap cleanup requires its completed current session and authenticated original ancestor owners")
	}
	e, r := runtime.Execution, runtime.ServiceSessionRecovery
	if e == nil || r == nil || !r.Renewal || !r.SecondSession || e.Store != store || runtime.ExecutionSHA256 != e.Journal.PlanSHA256 || current.BootstrapSessionID != r.RenewedIntent.SessionID || e.Check(ctx) != nil {
		return bad()
	}
	head, review, err := sessionLineageHead(store, e)
	if err != nil || head == nil || review != r.Review || head.Renewal.RenewedIntent.SessionID != current.BootstrapSessionID || deploymentplan.Digest(r.Intent) != deploymentplan.Digest(head.Renewal.Intent) || deploymentplan.Digest(r.RenewedIntent) != deploymentplan.Digest(head.Renewal.RenewedIntent) || current.CleanupBinding.SessionID != current.BootstrapSessionID || current.CleanupBinding.SecretUID != current.CredentialUIDs[current.BootstrapCredentialNamespace] || head.Renewal.KeySHA != r.OriginalKeySHA || head.Renewal.CSRSHA != r.OriginalCSRSHA || head.Renewal.Intent.IssuerID != current.Intermediate.ID || head.Renewal.Intent.RootSHA != current.Root.RootFingerprint || !slices.Equal(head.Renewal.Intent.Namespaces, current.BootstrapCredentialNamespaces) {
		return bad()
	}
	complete, err := completedServiceSessionBatch(store, e, *head, review)
	if err != nil || !complete {
		return bad()
	}
	first, firstReview, exists, err := loadPartialSessionAdmission(store, e)
	if err != nil || !exists || first.PartialSession == nil || firstReview != head.Renewal.OriginalReview {
		return bad()
	}
	original := first.PartialSession
	currentPlan, currentJournal, err := acceptedCreateStage(store, e, "kubernetes/service-credential-secrets-renewal-"+review)
	if err != nil || currentPlan == nil || r.RenewedCertPath != "/run/pki-bootstrap/renew-"+head.Renewal.BindingSHA[:12]+"/bootstrap.crt" {
		return bad()
	}
	for _, ns := range current.BootstrapCredentialNamespaces {
		if currentJournal.Owners["Secret/"+ns+"/service-bootstrap-"+current.BootstrapSessionID] != current.CredentialUIDs[ns] {
			return bad()
		}
	}
	if original.KeySHA != r.OriginalKeySHA || original.CSRSHA != r.OriginalCSRSHA || original.Recovery.OriginalState == nil || !slices.Equal(original.Intent.Namespaces, current.BootstrapCredentialNamespaces) || !partialSessionOriginalStatePreserved(*original.Recovery.OriginalState, head.Renewal.OriginalState, time.Now().UTC()) {
		return bad()
	}
	originalPlan, originalJournal, err := acceptedCreateStage(store, e, "kubernetes/service-credential-secrets")
	originalEntry := createOpenBaoACKCompletionEntry(original.SourceJournal, "kubernetes/service-credential-secrets")
	currentEntry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/service-credential-secrets")
	if err != nil || originalPlan == nil || originalEntry == nil || currentEntry == nil || deploymentplan.Digest(*originalEntry) != deploymentplan.Digest(*currentEntry) {
		return bad()
	}
	originalUIDs := map[string]string{}
	for _, ns := range original.Intent.Namespaces {
		originalUIDs[ns] = originalJournal.Owners["Secret/"+ns+"/service-bootstrap-"+original.Intent.SessionID]
	}
	type source struct {
		path  string
		state managedSessionState
		uids  map[string]string
	}
	base := filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/service")
	sources := []source{{filepath.Join(base, "session-observed.json"), head.Renewal.OriginalState, originalUIDs}}
	var previous []source
	cursor := *head
	seen := map[string]bool{}
	for {
		prior := cursor.Renewal.PreviousReview
		if seen[prior] {
			return bad()
		}
		seen[prior] = true
		credentialPlan, credentialJournal, err := acceptedCreateStage(store, e, "kubernetes/service-credential-secrets-renewal-"+prior)
		if err != nil || credentialPlan == nil || credentialPlan.SHA256() != cursor.Renewal.PreviousCredentialPlanSHA || deploymentplan.Digest(credentialJournal) != cursor.Renewal.PreviousCredentialJournalSHA {
			return bad()
		}
		for _, ns := range original.Intent.Namespaces {
			if credentialJournal.Owners["Secret/"+ns+"/service-bootstrap-"+cursor.Renewal.Intent.SessionID] != cursor.Renewal.PreviousCredentialUIDs[ns] {
				return bad()
			}
		}
		previous = append(previous, source{filepath.Join(base, "session-renewal", prior, "observed.json"), cursor.Renewal.PreviousState, cursor.Renewal.PreviousCredentialUIDs})
		if prior == firstReview {
			break
		}
		cursor, err = loadSecondSessionAdmission(store, e, prior)
		if err != nil {
			return bad()
		}
		// A superseded ancestor batch can remain pending. Its admitted mint
		// observation/credential source is authority; only the current batch
		// must have completed all of the recorded consumer transitions.
	}
	for i := len(previous) - 1; i >= 0; i-- {
		sources = append(sources, previous[i])
	}
	var result []createServiceSessionObserved
	sessions := map[string]bool{}
	for _, source := range sources {
		var observed createServiceSessionObserved
		var saved managedSessionState
		if readExecutionJSON(source.path, &observed) != nil || decodeEnvironmentJSON(observed.Session, &saved) != nil || source.state.Session == current.BootstrapSessionID || sessions[source.state.Session] || observed.Cleanup.SessionID != source.state.Session || saved.Session != source.state.Session || saved.Issuer != current.Intermediate.ID || saved.Root != current.Root.RootFingerprint || saved.Fingerprint != source.state.Fingerprint || !saved.Expires.Equal(source.state.Expires) || !slices.Equal(saved.Subjects, source.state.Subjects) || observed.Reader != current.CustodyReader || validateEnvironmentBootstrapCleanupBinding(&observed.Cleanup, cfg.Environment) != nil || observed.Cleanup.NamespaceUID != current.CleanupBinding.NamespaceUID || observed.Cleanup.KubernetesCA != current.CleanupBinding.KubernetesCA || deploymentplan.Digest(observed.UIDs) != deploymentplan.Digest(source.uids) || len(source.uids) != len(original.Intent.Namespaces) {
			return bad()
		}
		for _, ns := range original.Intent.Namespaces {
			if !environmentBootstrapOwnerUUID.MatchString(source.uids[ns]) || source.uids[ns] == current.CredentialUIDs[ns] {
				return bad()
			}
		}
		if observed.Cleanup.SecretUID != source.uids[current.BootstrapCredentialNamespace] {
			return bad()
		}
		actual, _, err := readManagedSessionState(ctx, cfg, runtime, source.state.Session)
		expected := source.state
		actual.OtherActive, expected.OtherActive = 0, 0
		if err != nil || actual.Status != "expired" || !partialSessionOriginalStatePreserved(expected, actual, time.Now().UTC()) {
			return bad()
		}
		sessions[source.state.Session] = true
		result = append(result, observed)
	}
	return result, original.Recovery.OriginalCertificateSHA256, e.Check(ctx)
}

func completedRenewedBootstrapCredentialRetirement(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, namespace string) (bool, error) {
	if done, handled, err := reconcileFreshCredentialRetirement(ctx, cfg, runtime, s, namespace); err != nil || handled {
		return done, err
	}
	r := runtime.ServiceSessionRecovery
	if r == nil || !r.SecondSession {
		return managedLateCompletedCredentialRetirement(ctx, cfg, runtime, s, namespace)
	}
	e := runtime.Execution
	if e == nil || e.Check(ctx) != nil {
		return false, environmentCreateBootstrapBlocker("original credential retirement owner unavailable")
	}
	key := "bootstrap-copy-retire-" + namespace + "-" + s.BootstrapSessionID
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	if entry == nil {
		return false, nil
	}
	id := "Secret/" + namespace + "/" + s.BootstrapCredentialSecret
	intent := deploymentplan.Object{"resource_id": id, "uid": s.CredentialUIDs[namespace], "session_id": s.BootstrapSessionID}
	receipt := deploymentplan.Object{"resource_id": id, "uid": s.CredentialUIDs[namespace], "absent": true}
	if entry.Phase != "bootstrap" || entry.StartedAt.IsZero() || entry.IntentSHA256 != activeTransportJournalDigest(intent) {
		return false, environmentCreateBootstrapBlocker("original credential retirement intent changed")
	}
	switch entry.State {
	case "pending":
		if len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero() {
			return false, environmentCreateBootstrapBlocker("original pending credential retirement changed")
		}
	case "allocated", "completed":
		if activeTransportJournalDigest(jsonRawObject(entry.Receipt)) != activeTransportJournalDigest(receipt) || (entry.State == "allocated") != entry.CompletedAt.IsZero() || entry.State == "completed" && entry.CompletedAt.Before(entry.StartedAt) {
			return false, environmentCreateBootstrapBlocker("original credential retirement receipt changed")
		}
	default:
		return false, environmentCreateBootstrapBlocker("original credential retirement state changed")
	}
	actual, err := runtime.Backend.Read(ctx, id)
	if err != nil {
		return false, environmentCreateBootstrapBlocker("retired credential native outcome is unavailable")
	}
	if entry.State == "pending" {
		current, err := secondSessionBootstrapCleanupCurrent(runtime)
		if err != nil {
			return false, err
		}
		_, resumed, err := resumeSecondSessionNativeCleanup(ctx, cfg, e.Store, runtime, current)
		if err != nil || !resumed {
			return false, environmentCreateBootstrapBlocker("pending retirement requires the exact current native cleanup proof")
		}
		if actual != nil {
			meta := deploymentplan.Map(actual["metadata"])
			if meta["uid"] != s.CredentialUIDs[namespace] || deploymentplan.String(meta["resourceVersion"]) == "" {
				return false, environmentCreateBootstrapBlocker("pending retirement has a replacement credential owner")
			}
			// The retained archive is checked below before this original DELETE may continue.
		}
	} else if actual != nil {
		return false, environmentCreateBootstrapBlocker("retired credential has a replacement native owner")
	}
	if s.BootstrapSessionID != r.RenewedIntent.SessionID {
		path := filepath.Join(e.Store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-renewal", r.Review, "expired-credential-copies", s.BootstrapSessionID, namespace+".json")
		var archived deploymentplan.Object
		if readExecutionJSON(path, &archived) != nil || deploymentplan.ID(archived) != id || deploymentplan.Map(archived["metadata"])["uid"] != s.CredentialUIDs[namespace] {
			return false, environmentCreateBootstrapBlocker("retired ancestor credential requires its retained original archive")
		}
		data := deploymentplan.Map(archived["data"])
		key, keyErr := base64.StdEncoding.DecodeString(deploymentplan.String(data["tls.key"]))
		certificate, certErr := base64.StdEncoding.DecodeString(deploymentplan.String(data["tls.crt"]))
		root, rootErr := base64.StdEncoding.DecodeString(deploymentplan.String(data["ca.crt"]))
		leaf, leafErr := parseFirstCertificatePEM(certificate)
		state, _, stateErr := readManagedSessionState(ctx, cfg, runtime, s.BootstrapSessionID)
		if keyErr != nil || certErr != nil || rootErr != nil || leafErr != nil || stateErr != nil || state.Session != s.BootstrapSessionID || state.Status != "expired" || state.Issuer != s.Intermediate.ID || state.Root != s.Root.RootFingerprint || environmentPreflightBytesSHA256(key) != r.OriginalKeySHA || environmentPreflightBytesSHA256(leaf.Raw) != state.Fingerprint || string(root) != s.Root.Certificate {
			return false, environmentCreateBootstrapBlocker("retired ancestor archive differs from its native same-key lineage")
		}
	}
	if entry.State == "pending" && actual != nil {
		return false, nil
	}
	if entry.State == "pending" {
		// Absence is now observed under the original UID intent, preserved
		// same-key archive and current native tombstone; record that truth.
		if err := e.RecordReceipt(key, mustProviderRecoveryJSON(receipt)); err != nil {
			return false, err
		}
		if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
			return false, err
		}
	} else if entry.State == "allocated" {
		if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
			return false, err
		}
	}
	return true, e.Check(ctx)
}

// Recover the already-recorded current observation without changing the
// recovery pointer or confusing its predecessor Intent with the current seed.
func secondSessionBootstrapCleanupCurrent(runtime environmentCreateBootstrapRuntime) (environmentCreateServiceCeremony, error) {
	r, e := runtime.ServiceSessionRecovery, runtime.Execution
	if e == nil || r == nil || !r.SecondSession {
		return environmentCreateServiceCeremony{}, environmentCreateBootstrapBlocker("current cleanup observation unavailable")
	}
	var observed createServiceSessionObserved
	if readExecutionJSON(filepath.Join(e.Store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-renewal", r.Review, "observed.json"), &observed) != nil || observed.Cleanup.SessionID != r.RenewedIntent.SessionID || observed.Reader != r.Ceremony.CustodyReader {
		return environmentCreateServiceCeremony{}, environmentCreateBootstrapBlocker("current cleanup observation changed")
	}
	current := r.Ceremony
	current.BootstrapSessionID, current.BootstrapCredentialSecret = r.RenewedIntent.SessionID, "service-bootstrap-"+r.RenewedIntent.SessionID
	current.BootstrapCredentialNamespaces = slices.Clone(r.RenewedIntent.Namespaces)
	current.CredentialUIDs, current.CleanupBinding = observed.UIDs, observed.Cleanup
	entry := createOpenBaoACKCompletionEntry(e.Journal, environmentCreateServiceFinalizationKey(runtime))
	if entry == nil {
		return current, nil
	}
	intentSHA := func() string {
		return activeTransportJournalDigest(deploymentplan.Object{"session_id": current.BootstrapSessionID, "issuer_id": current.Intermediate.ID, "cleanup_binding": current.CleanupBinding})
	}
	if entry.Phase == "bootstrap" && entry.IntentSHA256 == intentSHA() {
		return current, nil
	}
	// Fresh continuation can scope the legacy binding in memory without
	// rewriting its immutable observation. Recover only that exact accepted
	// finalization intent; an older pending legacy intent keeps its binding.
	if observed.Cleanup.ConfigMapName == "" && environmentBootstrapOwnerUUID.MatchString(current.BootstrapSessionID) {
		current.CleanupBinding.ConfigMapName = "pki-bootstrap-cleanup-binding-" + current.BootstrapSessionID[:12]
		if entry.Phase == "bootstrap" && entry.IntentSHA256 == intentSHA() {
			return current, nil
		}
	}
	return environmentCreateServiceCeremony{}, environmentCreateBootstrapBlocker("current cleanup binding differs from its original finalization intent")
}

func prepareSecondSessionBootstrapCleanup(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, finalized environmentCreateBootstrapFinalization) error {
	r := runtime.ServiceSessionRecovery
	if runtime.Execution == nil || finalized.Environment != cfg.Environment || finalized.SessionID != s.BootstrapSessionID || finalized.SessionID != r.RenewedIntent.SessionID || finalized.FinalizedAt.IsZero() || !environmentPreflightDigest.MatchString(finalized.LeafSHA256) {
		return environmentCreateBootstrapBlocker("actual current session finalization required before ancestor custody cleanup")
	}
	ancestors, originalSHA, err := secondSessionBootstrapCleanupAncestors(ctx, cfg, runtime.Execution.Store, runtime, s)
	if err != nil {
		return err
	}
	if len(ancestors) == 0 {
		return environmentCreateBootstrapBlocker("original cleanup ancestor unavailable")
	}
	base := "/run/pki-bootstrap/"
	archive := base + "expired-" + ancestors[0].Cleanup.SessionID
	read := func(path string) ([]byte, error) {
		return runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "cat", path)
	}
	// Native cleanup can erase the canonical files before ancestor retirement
	// and the local finalization record complete. Its accepted proof and
	// retained public certificates must authorize re-entry before key reads.
	marker, markerErr := read(base + "bootstrap.finalized.json")
	if markerErr == nil && len(bytes.TrimSpace(marker)) > 0 {
		var tombstone managedLateCustodyTombstone
		renewed, renewedErr := read(r.RenewedCertPath)
		expired, expiredErr := read(archive + "/bootstrap.crt")
		var chain [][]byte
		for rest := bytes.TrimSpace(renewed); len(rest) > 0; {
			block, tail := pem.Decode(rest)
			if block == nil || block.Type != "CERTIFICATE" {
				return environmentCreateBootstrapBlocker("retained finalized public certificate chain changed")
			}
			chain = append(chain, block.Bytes)
			rest = bytes.TrimSpace(tail)
		}
		old, oldErr := parseFirstCertificatePEM(expired)
		leaf, leafErr := parseFirstCertificatePEM(renewed)
		proof := deploymentplan.Object{"session_id": s.BootstrapSessionID, "original_session_id": ancestors[0].Cleanup.SessionID, "expired_certificate_sha256": originalSHA, "renewed_certificate_sha256": environmentPreflightBytesSHA256(renewed), "archive_path": archive + "/bootstrap.crt", "key_sha256": r.OriginalKeySHA, "csr_sha256": r.OriginalCSRSHA}
		accepted := createOpenBaoACKCompletionEntry(runtime.Execution.Journal, "bootstrap-renewed-cleanup-custody-"+r.Review)
		if decodeEnvironmentJSON(marker, &tombstone) != nil || tombstone.Schema != "rtk-cloud.bootstrap-credentials/v1" || !managedLateFinalizationEqual(tombstone.Finalization, finalized) || tombstone.Root != s.Root.RootFingerprint || len(tombstone.Files) != 3 || tombstone.Files["bootstrap.key"] != r.OriginalKeySHA || tombstone.Files["bootstrap.csr"] != r.OriginalCSRSHA || tombstone.Files["bootstrap.crt"] != environmentPreflightBytesSHA256(renewed) || renewedErr != nil || expiredErr != nil || !managedLateBootstrapChainMatches(chain, s) || oldErr != nil || leafErr != nil || environmentPreflightBytesSHA256(leaf.Raw) != finalized.LeafSHA256 || environmentPreflightBytesSHA256(expired) != originalSHA || !bytes.Equal(old.RawSubjectPublicKeyInfo, leaf.RawSubjectPublicKeyInfo) || accepted == nil || accepted.Phase != "bootstrap" || accepted.State != "completed" || accepted.StartedAt.IsZero() || accepted.CompletedAt.Before(accepted.StartedAt) || accepted.IntentSHA256 != activeTransportJournalDigest(proof) || !samePersistedJSONReceipt(accepted.Receipt, mustProviderRecoveryJSON(proof)) {
			return environmentCreateBootstrapBlocker("native cleanup tombstone differs from its accepted current same-key proof and original archive")
		}
		return runtime.Execution.Check(ctx)
	}
	key, keyErr := read(base + "bootstrap.key")
	csr, csrErr := read(base + "bootstrap.csr")
	renewed, renewedErr := read(r.RenewedCertPath)
	if keyErr != nil || csrErr != nil || renewedErr != nil || environmentPreflightBytesSHA256(key) != r.OriginalKeySHA || environmentPreflightBytesSHA256(csr) != r.OriginalCSRSHA {
		return environmentCreateBootstrapBlocker("original same-key custody changed before current session cleanup")
	}
	identity, err := tls.X509KeyPair(renewed, key)
	if err != nil || len(identity.Certificate) < 2 || environmentPreflightBytesSHA256(identity.Certificate[0]) != finalized.LeafSHA256 || environmentPreflightBytesSHA256(identity.Certificate[len(identity.Certificate)-1]) != s.Root.RootFingerprint {
		return environmentCreateBootstrapBlocker("current finalized certificate differs from original same-key lineage")
	}
	leaf, err := x509.ParseCertificate(identity.Certificate[0])
	if err != nil {
		return environmentCreateBootstrapBlocker("current finalized bootstrap leaf malformed")
	}
	expired, readErr := read(base + "bootstrap.crt")
	if readErr != nil || environmentPreflightBytesSHA256(expired) != originalSHA {
		// Only an existing exact parent request can account for a prior link or
		// unlink interruption. No different expired certificate may replace it.
		if createOpenBaoACKCompletionEntry(runtime.Execution.Journal, "bootstrap-renewed-cleanup-custody-"+r.Review) == nil {
			return environmentCreateBootstrapBlocker("original certificate slot changed before cleanup admission")
		}
		expired, readErr = read(archive + "/bootstrap.crt")
	}
	old, parseErr := parseFirstCertificatePEM(expired)
	if readErr != nil || parseErr != nil || environmentPreflightBytesSHA256(expired) != originalSHA || old.Subject.CommonName != "service:deployment-bootstrap" || old.NotAfter.After(time.Now().UTC()) || !bytes.Equal(old.RawSubjectPublicKeyInfo, leaf.RawSubjectPublicKeyInfo) {
		return environmentCreateBootstrapBlocker("original archived certificate differs from authenticated same-key session")
	}
	proof := deploymentplan.Object{"session_id": s.BootstrapSessionID, "original_session_id": ancestors[0].Cleanup.SessionID, "expired_certificate_sha256": originalSHA, "renewed_certificate_sha256": environmentPreflightBytesSHA256(renewed), "archive_path": archive + "/bootstrap.crt", "key_sha256": r.OriginalKeySHA, "csr_sha256": r.OriginalCSRSHA}
	entry := "bootstrap-renewed-cleanup-custody-" + r.Review
	recorded := createOpenBaoACKCompletionEntry(runtime.Execution.Journal, entry)
	if recorded == nil {
		if err := runtime.Execution.BeginEntry(entry, "bootstrap", mustProviderRecoveryJSON(proof), time.Now().UTC()); err != nil {
			return err
		}
	} else {
		if recorded.Phase != "bootstrap" || recorded.IntentSHA256 != activeTransportJournalDigest(proof) || recorded.StartedAt.IsZero() {
			return environmentCreateBootstrapBlocker("original cleanup archive intent changed")
		}
		switch recorded.State {
		case "pending":
			if len(recorded.Receipt) != 0 || !recorded.CompletedAt.IsZero() {
				return environmentCreateBootstrapBlocker("original cleanup archive has an unknown pending outcome")
			}
		case "allocated", "completed":
			if !samePersistedJSONReceipt(recorded.Receipt, mustProviderRecoveryJSON(proof)) || (recorded.State == "allocated") != recorded.CompletedAt.IsZero() {
				return environmentCreateBootstrapBlocker("original cleanup archive receipt changed")
			}
		default:
			return environmentCreateBootstrapBlocker("original cleanup archive state changed")
		}
	}
	check := func(sha, path string) string {
		return "printf '%s  %s\\n' '" + sha + "' '" + path + "' | sha256sum -c - >/dev/null"
	}
	originalCheck := check(originalSHA, base+"bootstrap.crt")
	renewedCheck := check(environmentPreflightBytesSHA256(renewed), base+"bootstrap.crt")
	script := "test ! -L /run/pki-bootstrap && test ! -L " + r.RenewedCertPath + " && " + check(r.OriginalKeySHA, base+"bootstrap.key") + " && " + check(r.OriginalCSRSHA, base+"bootstrap.csr") + " && " + check(environmentPreflightBytesSHA256(renewed), r.RenewedCertPath) + " && if test -e " + archive + "; then test -d " + archive + " && test ! -L " + archive + "; else mkdir " + archive + " && chmod 0700 " + archive + "; fi && if test -e " + archive + "/bootstrap.crt; then test ! -L " + archive + "/bootstrap.crt && " + check(originalSHA, archive+"/bootstrap.crt") + "; else test ! -L /run/pki-bootstrap/bootstrap.crt && " + originalCheck + " && ln /run/pki-bootstrap/bootstrap.crt " + archive + "/bootstrap.crt; fi && if test -e /run/pki-bootstrap/bootstrap.crt; then test ! -L /run/pki-bootstrap/bootstrap.crt && if " + renewedCheck + "; then :; else " + originalCheck + " && rm /run/pki-bootstrap/bootstrap.crt && ln " + r.RenewedCertPath + " /run/pki-bootstrap/bootstrap.crt; fi; else ln " + r.RenewedCertPath + " /run/pki-bootstrap/bootstrap.crt; fi"
	if recorded == nil || recorded.State == "pending" {
		if _, err := runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "/bin/sh", "-ec", script); err != nil {
			return environmentCreateBootstrapBlocker("current certificate archive/link is partial; preserve its original cleanup intent")
		}
	}
	for path, want := range map[string][]byte{archive + "/bootstrap.crt": expired, base + "bootstrap.crt": renewed} {
		actual, err := read(path)
		if err != nil || !bytes.Equal(actual, want) {
			return environmentCreateBootstrapBlocker("actual original archive and current cleanup certificate differ")
		}
	}
	if recorded != nil && recorded.State == "completed" {
		return runtime.Execution.Check(ctx)
	}
	if recorded == nil || recorded.State == "pending" {
		if err := runtime.Execution.RecordReceipt(entry, mustProviderRecoveryJSON(proof)); err != nil {
			return err
		}
	}
	return runtime.Execution.CompleteEntry(entry, time.Now().UTC())
}
