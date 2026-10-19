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

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A product continuation is the ordered native program, not permission for a
// journal-key prefix. Every Kubernetes effect keeps its original scoped plan.
type productStageReader struct {
	store       secretStore
	e           *environmentCreateExecution
	l           *createNormalTailProductLifecycle
	entries     []environmentCreateJournalEntry
	n           int
	children    []*deploymentplan.Plan
	err         error
	admission   createNormalTailProgramAdmission
	completedAt time.Time
}

func (r *productStageReader) result() (int, []*deploymentplan.Plan, error) {
	return r.n, r.children, r.err
}
func (r *productStageReader) bad(cause string) bool {
	r.err = environmentCreateBootstrapBlocker("original product progress: " + cause)
	return false
}
func (r *productStageReader) take(key, phase string, intent any, verify func(environmentCreateJournalEntry) bool) bool {
	return r.takeNested(key, phase, intent, verify, 0)
}

func (r *productStageReader) takeNested(key, phase string, intent any, verify func(environmentCreateJournalEntry) bool, nested int) bool {
	if r.err != nil || r.n == len(r.entries) {
		return false
	}
	e := r.entries[r.n]
	if e.StartedAt.Before(r.admission.AdmittedAt) || !e.StartedAt.Before(r.admission.Deadline) || e.State == "completed" && !e.CompletedAt.Before(r.admission.Deadline) || r.n > 0 && (r.entries[r.n-1].State != "completed" || e.StartedAt.Before(r.entries[r.n-1].CompletedAt)) || e.StartedAt.Before(r.completedAt) {
		return r.bad("stage is outside its original admission or order")
	}
	if e.Key != key || e.Phase != phase || e.StartedAt.IsZero() || e.StartedAt.After(time.Now().UTC()) || e.State != "pending" && e.State != "allocated" && e.State != "completed" || !environmentPreflightDigest.MatchString(e.IntentSHA256) {
		return r.bad("unexpected stage " + e.Key)
	}
	if intent != nil {
		raw, err := json.Marshal(intent)
		sha, he := environmentCreatePlanSHA256(raw)
		if err != nil || he != nil || e.IntentSHA256 != sha {
			return r.bad("changed native intent " + key)
		}
	}
	if e.State == "pending" {
		if len(e.Receipt) != 0 || !e.CompletedAt.IsZero() || r.n+1+nested != len(r.entries) {
			return r.bad("uncertain native suffix " + key)
		}
	} else if len(e.Receipt) == 0 || e.State == "completed" && (e.CompletedAt.Before(e.StartedAt) || e.CompletedAt.After(time.Now().UTC())) || e.State == "allocated" && (!e.CompletedAt.IsZero() || r.n+1+nested != len(r.entries)) {
		return r.bad("invalid receipt " + key)
	}
	if verify != nil && !verify(e) {
		return r.bad("unbound evidence " + key)
	}
	r.n += 1 + nested
	if e.State == "completed" {
		r.completedAt = e.CompletedAt
	}
	return e.State == "completed"
}

func (r *productStageReader) native(key string, intent any, verify func(environmentCreateJournalEntry) bool) bool {
	return r.take("product-identities-"+productIdentityScope(r.l)+"-"+key, "bootstrap", intent, verify)
}

func (r *productStageReader) controllerPolicies() []string {
	var policies []string
	before := len(r.e.Journal.Entries) - len(r.entries) + r.n
	for _, entry := range r.e.Journal.Entries[:before] {
		if entry.State == "completed" && strings.HasPrefix(entry.Key, "openbao-policy-create-") && strings.HasSuffix(entry.Key, "-controller") {
			policies = append(policies, strings.TrimPrefix(entry.Key, "openbao-policy-"))
		}
	}
	slices.Sort(policies)
	return policies
}

func (r *productStageReader) role(name string, accounts, policies []string) bool {
	intent := deploymentplan.Object{"role": name, "namespace": r.e.Journal.Stack + "-video-cloud", "service_accounts": accounts, "policies": policies, "audience": "openbao", "ttl": "15m", "no_default_policy": true}
	key := "openbao-role-" + name + "-" + deploymentplan.Digest(intent)[:12]
	// An unchanged pre-existing exact auth role is read back, not republished.
	for _, old := range r.e.Journal.Entries {
		if old.Key == key && (r.n == len(r.entries) || r.entries[r.n].Key != key) {
			if old.State != "completed" || old.IntentSHA256 != activeTransportJournalDigest(intent) || activeTransportJournalDigest(jsonRawObject(old.Receipt)) != activeTransportJournalDigest(intent) {
				return r.bad("changed existing role")
			}
			return true
		}
	}
	return r.take(key, "bootstrap", intent, func(e environmentCreateJournalEntry) bool {
		return e.State == "pending" || activeTransportJournalDigest(jsonRawObject(e.Receipt)) == activeTransportJournalDigest(intent)
	})
}

func (r *productStageReader) policy(name, issuer, mount string) bool {
	if r.n == len(r.entries) {
		return false
	}
	key := "openbao-policy-" + name
	return r.take(key, "bootstrap", nil, func(e environmentCreateJournalEntry) bool {
		if e.State == "pending" {
			return e.Key == key
		}
		var receipt struct {
			Environment string `json:"environment"`
			Issuer      string `json:"issuer_id"`
			Mount       string `json:"mount"`
			Policy      string `json:"policy"`
			SHA         string `json:"policy_sha256"`
		}
		if decodeEnvironmentJSON(e.Receipt, &receipt) != nil || receipt.Environment != r.store.Environment || receipt.Policy != name || receipt.Issuer != issuer || receipt.Mount != mount || !environmentPreflightDigest.MatchString(receipt.SHA) {
			return false
		}
		body := deploymentplan.Object{"environment": receipt.Environment, "policy": name, "policy_sha256": receipt.SHA}
		if issuer != "" {
			body["issuer_id"], body["mount"] = issuer, mount
		}
		return e.IntentSHA256 == activeTransportJournalDigest(body) && activeTransportJournalDigest(jsonRawObject(e.Receipt)) == activeTransportJournalDigest(body)
	})
}

func (r *productStageReader) kube(stage string, definitions []deploymentplan.Definition, validate func(*deploymentplan.Plan) bool) bool {
	if r.n == len(r.entries) {
		return false
	}
	full := "product-identities-" + productIdentityScope(r.l)[:12] + "-" + stage
	p, err := readCreateNormalTailPreparationPlan(r.store, r.e, full)
	if err != nil || p == nil {
		r.bad("missing frozen preparation " + stage)
		return false
	}
	if definitions != nil {
		if p.BindingsSHA256 != environmentCreateStageBindingsSHA(full, definitions) || len(p.Resources) != len(definitions) {
			return r.bad("changed preparation recipe " + stage)
		}
		want := map[string]deploymentplan.Definition{}
		for _, d := range definitions {
			want[deploymentplan.ID(d.Object)] = d
		}
		for _, resource := range p.Resources {
			d, ok := want[resource.ID]
			if !ok || resource.Phase != d.Phase || !deploymentplan.Matches(deploymentplan.Projection(d.Object), resource.Desired) || d.Retire != (resource.Action == "retire") {
				return r.bad("foreign preparation resource " + stage + ": " + resource.ID)
			}
		}
	}
	if validate != nil && !validate(p) {
		return r.bad("foreign private output " + stage)
	}
	intent, err := environmentCreateKubernetesStageIntent(p)
	if err != nil {
		return r.bad("invalid preparation input")
	}
	if stage == "service-session" {
		if handled, completed := r.sessionTrustForward(p, json.RawMessage(intent)); handled {
			return completed
		}
	}
	child, journal, err := readCreateNormalTailStage(r.store, r.e, full, p)
	if err != nil {
		return r.bad("invalid original child " + stage)
	}
	nested, err := validateCreateNormalTailNestedUnseals(r.store, r.e, child, journal, r.entries[r.n], r.entries[r.n+1:], r.admission)
	if err != nil {
		r.err = err
		return false
	}
	return r.takeNested("kubernetes/"+full, full, json.RawMessage(intent), func(entry environmentCreateJournalEntry) bool {
		if child != nil {
			r.children = append(r.children, child)
		}
		return entry.State == "pending" || child != nil
	}, nested)
}

func readProductStageJSON(store secretStore, l *createNormalTailProductLifecycle, name string, out any) error {
	return createOpenBaoACKCompletionRead(store, filepath.Join("deployment/create", l.ParentSHA256, "bootstrap/product-identities", productIdentityScope(l), "service", name), out)
}

func validateCreateNormalTailProductStages(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, entries []environmentCreateJournalEntry) (int, []*deploymentplan.Plan, error) {
	if len(entries) == 0 {
		return 0, nil, nil
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return 0, nil, err
	}
	cfg, err := resolveDeploymentConfig(workspace, store.Environment, "")
	if err != nil {
		return 0, nil, err
	}
	return validateCreateNormalTailProductStagesWithConfig(cfg, store, e, l, entries)
}

func validateCreateNormalTailProductStagesWithConfig(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, entries []environmentCreateJournalEntry) (int, []*deploymentplan.Plan, error) {
	r := &productStageReader{store: store, e: e, l: l, entries: entries}
	if len(entries) == 0 {
		return r.result()
	}
	if e == nil || e.Store != store || l == nil || l.ParentSHA256 != e.Journal.PlanSHA256 || l.OwnerSHA256 != deploymentplan.Digest(e.owner) {
		r.bad("wrong original owner")
		return r.result()
	}
	program, err := readCreateNormalTailProgram(store, e)
	if err != nil || deploymentplan.Digest(program.Products) != productIdentityScope(l) || program.MaintainedConfigSHA256 != deploymentplan.Digest(cfg) {
		r.bad("changed frozen catalog")
		return r.result()
	}
	r.admission, err = readCreateNormalTailProgramAdmission(store, e)
	if err != nil {
		r.err = err
		return r.result()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	service := environmentCreateServiceCeremony{Root: l.Root, Intermediate: l.Intermediate}
	recipe := func(stage string) []deploymentplan.Definition {
		defs, err := productStageDefinitions(ctx, cfg, l, service, stage, store)
		if err != nil {
			r.err = err
		}
		return defs
	}
	var operation environmentCreateIssuerOperation
	if l.RequiresSuccessor {
		request := environmentCreateIssuerRequest{Environment: store.Environment, Domain: "service", Kind: "intermediate", ParentID: l.Root.ID, ServerDNSNames: l.Policy.ServerDNSNames, ServiceClientIDs: l.Policy.ServiceClientIDs}
		if !r.native("pki-reserve-service-intermediate", request, func(entry environmentCreateJournalEntry) bool {
			return entry.State == "pending" || decodeEnvironmentJSON(entry.Receipt, &operation) == nil && environmentBootstrapOwnerUUID.MatchString(operation.ID) && environmentBootstrapOwnerUUID.MatchString(operation.IssuerID) && operation.Digest == deploymentplan.Digest(request) && operation.Status == "requested"
		}) {
			return r.result()
		}
		var approved deploymentplan.Object
		if !r.native("pki-authorize-"+operation.ID, operation, func(entry environmentCreateJournalEntry) bool {
			if entry.State == "pending" {
				return true
			}
			return json.Unmarshal(entry.Receipt, &approved) == nil && approved["issuer_id"] == operation.IssuerID && approved["parent_issuer_id"] == l.Root.ID && approved["status"] == "approved" && approved["environment"] == store.Environment
		}) {
			return r.result()
		}
		mount := deploymentplan.String(approved["signer_reference"])
		if mount == "" {
			r.bad("successor mount unavailable")
			return r.result()
		}
		for _, suffix := range []string{"controller", "signer", "recovery", "service-client"} {
			if !r.policy("create-"+operation.IssuerID+"-"+suffix, operation.IssuerID, mount) {
				return r.result()
			}
		}
		if !r.role("pki-controller-"+store.Environment, []string{"pki-controller"}, r.controllerPolicies()) {
			return r.result()
		}
		var provisioned deploymentplan.Object
		if !r.native("pki-provision-"+operation.ID, deploymentplan.Object{"operation_id": operation.ID, "issuer_id": operation.IssuerID, "csr_sha256": environmentPreflightBytesSHA256(nil)}, func(entry environmentCreateJournalEntry) bool {
			if entry.State == "pending" {
				return true
			}
			return json.Unmarshal(entry.Receipt, &provisioned) == nil && provisioned["issuer_id"] == operation.IssuerID && provisioned["csr_pem"] != ""
		}) {
			return r.result()
		}
		var imported deploymentplan.Object
		if !r.take("product-identities-"+productIdentityScope(l)+"-pki-import-"+operation.ID, "bootstrap", nil, func(entry environmentCreateJournalEntry) bool {
			if entry.State == "pending" {
				return true
			}
			if json.Unmarshal(entry.Receipt, &imported) != nil || imported["issuer_id"] != operation.IssuerID || imported["parent_issuer_id"] != l.Root.ID {
				return false
			}
			cert := deploymentplan.String(imported["certificate_pem"])
			return entry.IntentSHA256 == activeTransportJournalDigest(deploymentplan.Object{"operation_id": operation.ID, "issuer_id": operation.IssuerID, "certificate_sha256": environmentPreflightBytesSHA256([]byte(cert))})
		}) {
			return r.result()
		}
		service.Intermediate, err = environmentCreateRegisteredIssuerFromMap(imported, l.Root.RootFingerprint)
		if err != nil {
			r.err = err
			return r.result()
		}
		leaf, ce := parseFirstCertificatePEM([]byte(service.Intermediate.Certificate))
		root, re := parseFirstCertificatePEM([]byte(l.Root.Certificate))
		block, rest := pem.Decode([]byte(deploymentplan.String(provisioned["csr_pem"])))
		var csr *x509.CertificateRequest
		if block != nil && block.Type == "CERTIFICATE REQUEST" && len(bytes.TrimSpace(rest)) == 0 {
			csr, _ = x509.ParseCertificateRequest(block.Bytes)
		}
		if ce != nil || re != nil || csr == nil || csr.CheckSignature() != nil || !leaf.IsCA || leaf.CheckSignatureFrom(root) != nil || !bytes.Equal(csr.RawSubjectPublicKeyInfo, leaf.RawSubjectPublicKeyInfo) {
			r.bad("successor certificate is not signed by original Root")
			return r.result()
		}
		if !r.kube("successor-trust", recipe("successor-trust"), nil) {
			return r.result()
		}
		var active deploymentplan.Object
		if !r.native("pki-activate-"+operation.ID, operation, func(entry environmentCreateJournalEntry) bool {
			if entry.State == "pending" {
				return true
			}
			return json.Unmarshal(entry.Receipt, &active) == nil && active["issuer_id"] == operation.IssuerID && active["status"] == "active" && active["certificate_pem"] == service.Intermediate.Certificate
		}) {
			return r.result()
		}
		service.Intermediate, err = environmentCreateRegisteredIssuerFromMap(active, l.Root.RootFingerprint)
		if err != nil {
			r.err = err
			return r.result()
		}
		if !r.kube("successor-crls", recipe("successor-crls"), nil) {
			return r.result()
		}
	}
	policies := slices.Clone(l.SignerPolicies)
	for _, suffix := range []string{"-signer", "-service-client"} {
		name := "create-" + service.Intermediate.ID + suffix
		if !slices.Contains(policies, name) {
			policies = append(policies, name)
		}
	}
	slices.Sort(policies)
	if !r.role("certissuer-"+store.Environment, []string{"certissuer"}, policies) {
		return r.result()
	}
	var intent struct {
		Session    string   `json:"session_id"`
		Claim      string   `json:"claim"`
		Subjects   []string `json:"subjects"`
		Issuer     string   `json:"issuer_id"`
		Root       string   `json:"root_sha256"`
		Namespaces []string `json:"credential_namespaces"`
	}
	if err := readProductStageJSON(store, l, "session-intent.json", &intent); err != nil {
		if r.n != len(entries) {
			r.bad("original session intent unavailable")
		}
		return r.result()
	}
	var subjects []string
	for _, output := range l.Outputs {
		subjects = append(subjects, output.Subject)
	}
	slices.Sort(subjects)
	ns := e.Journal.Stack + "-video-cloud"
	if !environmentBootstrapOwnerUUID.MatchString(intent.Session) || intent.Claim != "pki-product-bootstrap-"+productIdentityScope(l)[:12] || !slices.Equal(intent.Subjects, subjects) || intent.Issuer != service.Intermediate.ID || intent.Root != l.Root.RootFingerprint || !slices.Equal(intent.Namespaces, []string{ns}) {
		r.bad("changed native session intent")
		return r.result()
	}
	service.BootstrapSessionID, service.BootstrapClaim, service.BootstrapCredentialNamespace, service.BootstrapCredentialNamespaces = intent.Session, intent.Claim, ns, intent.Namespaces
	service.BootstrapCredentialSecret = "service-bootstrap-" + intent.Session
	service.CustodyReader = "pki-product-custody-" + productIdentityScope(l)[:12]
	sessionPolicy := "rtk-" + store.Environment + "-service-bootstrap-" + intent.Session
	if !r.policy(sessionPolicy, "", "") {
		return r.result()
	}
	account := "pki-product-bootstrap-" + productIdentityScope(l)[:12]
	if !r.role(account, []string{account}, []string{sessionPolicy}) {
		return r.result()
	}
	for _, stage := range []string{"service-session", "service-custody-reader"} {
		if !r.kube(stage, recipe(stage), nil) {
			return r.result()
		}
	}
	if !r.kube("service-credential-secrets", nil, func(p *deploymentplan.Plan) bool {
		return validateProductStageSecret(p, "Secret/"+ns+"/"+service.BootstrapCredentialSecret, "service:deployment-bootstrap", service, "")
	}) {
		return r.result()
	}
	if !r.policy("create-"+intent.Session+"-cleanup-controller", "", "") {
		return r.result()
	}
	if !r.role("pki-controller-"+store.Environment, []string{"pki-controller"}, r.controllerPolicies()) {
		return r.result()
	}
	var session environmentProductIdentitySession
	if readProductStageJSON(store, l, "product-session.json", &session) != nil || session.Schema != productIdentitySessionSchema || session.ParentSHA256 != l.ParentSHA256 || session.PlanSHA256 != productIdentityScope(l) || session.OwnerSHA256 != l.OwnerSHA256 || session.Ceremony.BootstrapSessionID != intent.Session || session.Ceremony.Root.RootFingerprint != l.Root.RootFingerprint || session.Ceremony.Intermediate.ID != service.Intermediate.ID || session.Ceremony.Intermediate.Certificate != service.Intermediate.Certificate || session.Ceremony.Intermediate.ChainPEM != service.Intermediate.ChainPEM || session.Ceremony.Intermediate.BundleVersion != service.Intermediate.BundleVersion || session.Ceremony.Intermediate.Reference != service.Intermediate.Reference {
		r.bad("original completed session unavailable")
		return r.result()
	}
	service = session.Ceremony
	credential := r.children[len(r.children)-1]
	credentialJournal, credentialExists, credentialErr := credential.ReadExecution(store.Root)
	secretID := "Secret/" + ns + "/service-bootstrap-" + intent.Session
	if credentialErr != nil || !credentialExists || !credentialJournal.Accepted || service.Root.Certificate != l.Root.Certificate || service.BootstrapClaim != intent.Claim || service.BootstrapCredentialNamespace != ns || !slices.Equal(service.BootstrapCredentialNamespaces, intent.Namespaces) || service.CredentialUIDs[ns] != credentialJournal.Owners[secretID] || service.CleanupBinding.SessionID != intent.Session || service.CleanupBinding.SecretUID != credentialJournal.Owners[secretID] {
		r.bad("session controls changed the original credential owner")
		return r.result()
	}
	if !r.kube("session-controls", recipe("session-controls"), nil) {
		return r.result()
	}
	for _, output := range l.Outputs {
		role := strings.TrimPrefix(output.Subject, "service:")
		if !r.kube("enroll-"+role, recipe("enroll-"+role), nil) {
			return r.result()
		}
		proof, err := readProductIdentityProof(store, l, role)
		if err != nil {
			key := "product-identities-" + productIdentityScope(l) + "-observe-" + role
			if r.n == len(entries) {
				return r.result()
			}
			if entries[r.n].Key != key || entries[r.n].State != "completed" || decodeEnvironmentJSON(entries[r.n].Receipt, &proof) != nil {
				r.err = err
				return r.result()
			}
		}
		if !r.native("observe-"+role, deploymentplan.Object{"product_sha256": deploymentplan.Digest(output), "owner_uid": proof.OwnerUID, "state_sha256": proof.StateSHA256}, func(entry environmentCreateJournalEntry) bool {
			return entry.State == "pending" || deploymentplan.Digest(jsonRawObject(entry.Receipt)) == deploymentplan.Digest(jsonRawObject(mustProviderRecoveryJSON(proof)))
		}) {
			return r.result()
		}
		if proof.Subject != output.Subject || proof.Row.Subject != output.Subject || proof.Row.Issuer != service.Intermediate.ID || !environmentPreflightDigest.MatchString(proof.StateSHA256) || !environmentBootstrapOwnerUUID.MatchString(proof.OwnerUID) {
			r.bad("product observation changed lineage")
			return r.result()
		}
		enroll := r.children[len(r.children)-1]
		journal, exists, err := enroll.ReadExecution(store.Root)
		ownerFound := false
		for _, resource := range enroll.Resources {
			if resource.Desired["kind"] == "Deployment" && journal.Owners[resource.ID] == proof.OwnerUID {
				ownerFound = true
			}
		}
		if err != nil || !exists || !journal.Accepted || !ownerFound || proof.Row.Status != "succeeded" || proof.Row.Revoked != nil || !environmentPreflightDigest.MatchString(proof.Row.Fingerprint) {
			r.bad("product observation has no original enrollment owner")
			return r.result()
		}
		if !r.kube("publish-"+role, nil, func(p *deploymentplan.Plan) bool {
			return validateProductStageSecret(p, output.ID, output.Subject, service, proof.Row.Fingerprint)
		}) {
			return r.result()
		}
		issuer, err := parseFirstCertificatePEM([]byte(service.Intermediate.Certificate))
		if err != nil {
			r.err = err
			return r.result()
		}
		approval := deploymentplan.Object{"service_id": output.Service, "instance_id": output.Instance, "certificate_subject": output.Subject, "issuer_fingerprint_sha256": environmentPreflightBytesSHA256(issuer.Raw), "allowed_option_codes": []string{output.Option}}
		if !r.native("approve-"+role, approval, func(entry environmentCreateJournalEntry) bool {
			return entry.State == "pending" || activeTransportJournalDigest(jsonRawObject(entry.Receipt)) == activeTransportJournalDigest(approval)
		}) {
			return r.result()
		}
	}
	if r.n == len(entries) {
		return r.result()
	}
	installed, err := readProductInstalled(store, l)
	if err != nil {
		r.err = err
		return r.result()
	}
	if !r.kube("enrollment-closed", recipe("enrollment-closed"), nil) {
		return r.result()
	}
	if r.n == len(entries) {
		return r.result()
	}
	frozen, err := readCreateNormalTailFrozenTarget(store, e)
	if err != nil {
		r.err = err
		return r.result()
	}
	workloadIntent, err := environmentCreateKubernetesStageIntent(frozen)
	if err != nil {
		r.err = err
		return r.result()
	}
	workload, journal, err := readCreateNormalTailWorkloadOriginal(store, e, frozen)
	if err != nil {
		r.err = err
		return r.result()
	}
	nested, repairs, err := validateCreateNormalTailNestedWorkloadStages(store, e, workload, journal, entries[r.n], entries[r.n+1:], r.admission)
	if err != nil {
		r.err = err
		return r.result()
	}
	r.children = append(r.children, repairs...)
	if !r.takeNested("kubernetes/workload", "workload", json.RawMessage(workloadIntent), func(entry environmentCreateJournalEntry) bool {
		if workload != nil {
			r.children = append(r.children, workload)
		}
		return entry.State == "pending" || workload != nil
	}, nested) {
		return r.result()
	}
	// The original enrollment proofs above remain tied to their actual session.
	// Only a fully accepted renewal changes the finalizer and steady inputs.
	if renewal, err := readProductSessionRenewal(store, e, l); err != nil {
		r.err = err
		return r.result()
	} else if renewal != nil {
		installed = &renewal.Installed
		service = installed.Ceremony
	}
	if !r.sessionRenewalFinalizer(service) {
		return r.result()
	}
	if r.n < len(entries) && entries[r.n].Key == "kubernetes/product-identities-"+productIdentityScope(l)[:12]+"-steady" {
		final, err := productSteadyDefinitions(cfg, l, installed)
		if err != nil {
			r.err = err
			return r.result()
		}
		if !r.kube("steady", final, nil) {
			return r.result()
		}
		if r.n < len(entries) && entries[r.n].Key == "kubernetes/product-identities-"+productIdentityScope(l)[:12]+"-producer-retire" {
			retire, err := productPreparationDefinitions(cfg, store, e, l, installed, true)
			if err != nil {
				r.err = err
				return r.result()
			}
			if !r.kube("producer-retire", retire, nil) {
				return r.result()
			}
		}
	}
	return r.result()
}

func productFinalizationReceiptMatches(entry environmentCreateJournalEntry, environment, session string) bool {
	if entry.State == "pending" {
		return true // no native outcome is inferred from an empty pending entry
	}
	var receipt struct {
		Native    environmentCreateBootstrapFinalization `json:"native_finalization"`
		Tombstone string                                 `json:"custody_tombstone_sha256"`
		Absent    bool                                   `json:"copies_absent"`
	}
	return decodeEnvironmentJSON(entry.Receipt, &receipt) == nil && receipt.Native.SessionID == session && receipt.Native.Environment == environment && !receipt.Native.FinalizedAt.IsZero() && !receipt.Native.FinalizedAt.Before(entry.StartedAt) && !receipt.Native.FinalizedAt.After(time.Now().UTC()) && (entry.State != "completed" || !receipt.Native.FinalizedAt.After(entry.CompletedAt)) && environmentPreflightDigest.MatchString(receipt.Tombstone) && receipt.Absent
}

func validateProductStageSecret(p *deploymentplan.Plan, id, subject string, s environmentCreateServiceCeremony, fingerprint string) bool {
	if p == nil || len(p.Resources) != 1 {
		return false
	}
	resource := p.Resources[0]
	o := resource.Desired
	if resource.ID != id || o["kind"] != "Secret" || o["immutable"] != true || resource.Action != "create" {
		return false
	}
	data := deploymentplan.Map(o["data"])
	certName, keyName, rootName := "client.crt", "client.key", "server-ca.crt"
	if subject == "service:deployment-bootstrap" {
		certName, keyName, rootName = "tls.crt", "tls.key", "ca.crt"
		labels := deploymentplan.Map(deploymentplan.Map(o["metadata"])["labels"])
		if labels["rtk.cloud/bootstrap-session"] != s.BootstrapSessionID || labels["rtk.realtek.com/stack"] != strings.TrimSuffix(s.BootstrapCredentialNamespace, "-video-cloud") {
			return false
		}
	}
	if len(data) != 3 {
		return false
	}
	cert, ce := base64.StdEncoding.DecodeString(deploymentplan.String(data[certName]))
	key, ke := base64.StdEncoding.DecodeString(deploymentplan.String(data[keyName]))
	root, re := base64.StdEncoding.DecodeString(deploymentplan.String(data[rootName]))
	pair, pe := tls.X509KeyPair(cert, key)
	if ce != nil || ke != nil || re != nil || pe != nil || string(root) != s.Root.Certificate || len(pair.Certificate) < 2 {
		return false
	}
	leaf, le := x509.ParseCertificate(pair.Certificate[0])
	issuer, ie := parseFirstCertificatePEM([]byte(s.Intermediate.Certificate))
	return le == nil && ie == nil && leaf.Subject.CommonName == subject && !leaf.IsCA && leaf.CheckSignatureFrom(issuer) == nil && (fingerprint == "" || environmentPreflightBytesSHA256(leaf.Raw) == fingerprint)
}

type createNormalTailVolumeStage struct {
	// Set only by the authenticated workload-forward reader; it delegates an
	// unwritten original claim to the same exact claim in its real forward child.
	originalPlanSHA256 string
	Stage              string
	Plan               *deploymentplan.Plan
	Journal            deploymentplan.Journal
}

func readCreateNormalTailProductVolumeStages(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle) ([]createNormalTailVolumeStage, error) {
	entry := createOpenBaoACKCompletionEntry(e.Journal, "normal-tail/program")
	if entry == nil {
		return nil, nil
	}
	start := -1
	for i := range e.Journal.Entries {
		if &e.Journal.Entries[i] == entry {
			start = i + 1
			break
		}
	}
	if start < 0 {
		return nil, environmentCreateBootstrapBlocker("original program admission missing")
	}
	program, err := readCreateNormalTailProgram(store, e)
	if err != nil {
		return nil, err
	}
	publicConsumed, _, err := validateCreateNormalTailPublicTLSStages(store, e, program.PublicTLS, e.Journal.Entries[start:])
	if err != nil {
		return nil, err
	}
	start += publicConsumed
	_, children, err := validateCreateNormalTailProductStages(store, e, l, e.Journal.Entries[start:])
	if err != nil {
		return nil, err
	}
	var result []createNormalTailVolumeStage
	for _, p := range children {
		intent, err := environmentCreateKubernetesStageIntent(p)
		if err != nil {
			return nil, err
		}
		sha, _ := environmentCreatePlanSHA256(intent)
		for _, entry := range e.Journal.Entries[start:] {
			if entry.IntentSHA256 == sha {
				stage := strings.TrimPrefix(entry.Key, "kubernetes/")
				if !productVolumeStageSelected(l, stage) {
					break // workload allocations have their own original child proof
				}
				j, exists, err := p.ReadExecution(store.Root)
				if err != nil || !exists {
					return nil, environmentCreateBootstrapBlocker("original product allocation journal unavailable")
				}
				result = append(result, createNormalTailVolumeStage{Stage: stage, Plan: p, Journal: j})
				break
			}
		}
	}
	return result, nil
}

func productVolumeStageSelected(l *createNormalTailProductLifecycle, stage string) bool {
	if l == nil {
		return false
	}
	prefix := "product-identities-" + productIdentityScope(l)[:12] + "-"
	if stage == prefix+"service-session" {
		return true
	}
	for _, output := range l.Outputs {
		if stage == prefix+"enroll-"+strings.TrimPrefix(output.Subject, "service:") {
			return true
		}
	}
	return false
}

// Only the exact old temporary authorization retirement may be nested beneath
// the native finalizer. The permanent product identity objects are unchanged.
func (r *productStageReader) sessionRenewalFinalizer(service environmentCreateServiceCeremony) bool {
	intent := deploymentplan.Object{"session_id": service.BootstrapSessionID, "issuer_id": service.Intermediate.ID, "cleanup_binding": service.CleanupBinding}
	renewal, err := readProductSessionRenewal(r.store, r.e, r.l)
	if err != nil {
		r.err = err
		return false
	}
	if renewal == nil {
		return r.native("service-session-finalize", intent, func(entry environmentCreateJournalEntry) bool {
			return productFinalizationReceiptMatches(entry, r.store.Environment, service.BootstrapSessionID)
		})
	}
	if r.n == len(r.entries) {
		return false
	}
	specs, err := productSessionRenewalFinalizerSpecs(r.store, r.e, r.l, renewal)
	if err != nil {
		r.err = err
		return false
	}
	outer := r.entries[r.n]
	nested := productStageReader{store: r.store, e: r.e, l: r.l, entries: r.entries[r.n+1:], admission: r.admission, completedAt: outer.StartedAt}
	for _, spec := range specs {
		if nested.n == len(nested.entries) {
			break
		}
		if spec.Suffix != "" {
			if spec.Definitions == nil {
				return r.bad("renewal finalizer recipe missing")
			}
			prefix := "product-identities-" + productIdentityScope(r.l)[:12] + "-"
			if !nested.kube(strings.TrimPrefix(spec.Phase, prefix), spec.Definitions, nil) {
				break
			}
		} else if !nested.take(spec.Key, spec.Phase, spec.Intent, func(entry environmentCreateJournalEntry) bool {
			return entry.State == "pending" || deploymentplan.Digest(jsonRawObject(entry.Receipt)) == deploymentplan.Digest(spec.Receipt)
		}) {
			break
		}
	}
	if nested.err != nil {
		r.err = nested.err
		return false
	}
	if outer.State != "pending" && (len(specs) == 0 || nested.n != len(specs) || nested.completedAt.After(outer.CompletedAt) && outer.State == "completed") {
		return r.bad("renewal cleanup is unfinished")
	}
	r.children = append(r.children, nested.children...)
	return r.takeNested("product-identities-"+productIdentityScope(r.l)+"-service-session-finalize", "bootstrap", intent, func(entry environmentCreateJournalEntry) bool {
		return productFinalizationReceiptMatches(entry, r.store.Environment, service.BootstrapSessionID)
	}, nested.n)
}
