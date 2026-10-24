package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func readProductMutation(ctx context.Context, c *environmentCreatePKIClient, key string, intent, target any, freshProof ...func(context.Context) error) (bool, error) {
	if c == nil || c.Execution == nil || !environmentPreflightDigest.MatchString(c.mutationScope) {
		return false, environmentCreateBootstrapBlocker("product mutation has no exact admitted lifecycle scope")
	}
	shortKey := key
	key = c.mutationKey(key)
	var matched *environmentCreateJournalEntry
	for i := range c.Execution.Journal.Entries {
		entry := &c.Execution.Journal.Entries[i]
		if entry.Key == key {
			if matched != nil {
				return false, environmentCreateBootstrapBlocker("product mutation receipt is ambiguous")
			}
			matched = entry
		}
	}
	if matched == nil {
		return false, nil
	}
	raw, _ := json.Marshal(intent)
	sha, _ := environmentCreatePlanSHA256(raw)
	if (matched.State != "completed" && matched.State != "allocated") || matched.Phase != "bootstrap" || matched.StartedAt.IsZero() || matched.State == "completed" && matched.CompletedAt.Before(matched.StartedAt) || matched.State == "allocated" && !matched.CompletedAt.IsZero() || matched.IntentSHA256 != sha || len(matched.Receipt) == 0 || decodeEnvironmentJSON(matched.Receipt, target) != nil {
		return false, environmentCreateBootstrapBlocker("product native operation has an uncertain or changed original outcome; reconcile the same operation")
	}
	if err := c.Execution.Check(ctx); err != nil {
		return false, err
	}
	if matched.State == "allocated" {
		original := deploymentplan.Digest(*matched)
		if err := reconcileProductMutation(ctx, c, shortKey, intent, matched.Receipt, freshProof...); err != nil {
			return false, err
		}
		if err := c.Execution.Check(ctx); err != nil {
			return false, err
		}
		if deploymentplan.Digest(*matched) != original {
			return false, environmentCreateBootstrapBlocker("recorded product outcome changed during native readback")
		}
		if err := c.Execution.CompleteEntry(key, time.Now().UTC()); err != nil {
			return false, err
		}
	}
	return true, nil
}

func writeProductArtifact(store secretStore, relative string, value any) (string, error) {
	path, err := store.safePath(relative)
	if err != nil {
		return "", err
	}
	if _, err := os.Lstat(path); err == nil {
		raw, err := readBoundedEnvironmentFile(path, true)
		if err != nil {
			return "", err
		}
		expected, err := json.Marshal(value)
		if err != nil || !bytes.Equal(bytes.TrimSpace(raw), expected) {
			return "", environmentCreateBootstrapBlocker("immutable product lifecycle metadata changed")
		}
		return "/store/" + filepath.ToSlash(relative), nil
	} else if !os.IsNotExist(err) {
		return "", environmentCreateBootstrapBlocker("product lifecycle metadata cannot be inspected")
	}
	return writeEnvironmentCreateBootstrapJSON(store, relative, value)
}

func executeProductIdentitySuccessor(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, apply environmentCreateBootstrapApply) (environmentCreateRegisteredIssuer, error) {
	client := runtime.PKIClient
	request := environmentCreateIssuerRequest{Environment: cfg.Environment, Domain: "service", Kind: "intermediate", ParentID: l.Root.ID, ServerDNSNames: l.Policy.ServerDNSNames, ServiceClientIDs: l.Policy.ServiceClientIDs}
	var operation environmentCreateIssuerOperation
	found, err := readProductMutation(ctx, client, "pki-reserve-service-intermediate", request, &operation)
	if err != nil {
		return environmentCreateRegisteredIssuer{}, err
	}
	if !found {
		operation, err = client.ReserveIssuer(ctx, "product-"+l.ParentSHA256+"-"+productIdentityScope(l), request)
		if err != nil {
			return environmentCreateRegisteredIssuer{}, err
		}
	}
	if operation.Digest != deploymentplan.Digest(request) || !environmentBootstrapOwnerUUID.MatchString(operation.ID) || !environmentBootstrapOwnerUUID.MatchString(operation.IssuerID) {
		return environmentCreateRegisteredIssuer{}, environmentCreateBootstrapBlocker("product successor reservation differs from its qualified exact policy")
	}
	var approved map[string]any
	found, err = readProductMutation(ctx, client, "pki-authorize-"+operation.ID, operation, &approved)
	if err != nil {
		return environmentCreateRegisteredIssuer{}, err
	}
	if !found {
		if err = client.AuthorizeIssuer(ctx, operation); err != nil {
			return environmentCreateRegisteredIssuer{}, err
		}
		approved, err = client.ReadIssuer(ctx, operation.IssuerID)
		if err != nil {
			return environmentCreateRegisteredIssuer{}, err
		}
	}
	if approved["status"] != "approved" || approved["issuer_id"] != operation.IssuerID || approved["parent_issuer_id"] != l.Root.ID {
		return environmentCreateRegisteredIssuer{}, environmentCreateBootstrapBlocker("product successor approval changed its original Root")
	}
	if _, err = configureEnvironmentCreateIssuerPolicies(ctx, cfg, runtime, approved); err != nil {
		return environmentCreateRegisteredIssuer{}, err
	}
	var provisioning map[string]any
	provisionIntent := deploymentplan.Object{"operation_id": operation.ID, "issuer_id": operation.IssuerID, "csr_sha256": environmentPreflightBytesSHA256(nil)}
	found, err = readProductMutation(ctx, client, "pki-provision-"+operation.ID, provisionIntent, &provisioning)
	if err != nil {
		return environmentCreateRegisteredIssuer{}, err
	}
	if !found {
		provisioning, err = client.ProvisionIssuer(ctx, operation, "")
		if err != nil {
			return environmentCreateRegisteredIssuer{}, err
		}
	}
	csrPEM := []byte(deploymentplan.String(provisioning["csr_pem"]))
	block, rest := pem.Decode(csrPEM)
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(bytes.TrimSpace(rest)) != 0 {
		return environmentCreateRegisteredIssuer{}, environmentCreateBootstrapBlocker("product successor has no original native CSR")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil {
		return environmentCreateRegisteredIssuer{}, environmentCreateBootstrapBlocker("product successor CSR is invalid")
	}
	base := environmentCreateCeremonyDirectory(runtime, "service")
	issuerPath, err := writeProductArtifact(store, filepath.Join(base, "intermediate-provisioning.json"), provisioning)
	if err != nil {
		return environmentCreateRegisteredIssuer{}, err
	}
	parentPath, err := writeProductArtifact(store, filepath.Join(base, "active-parent.json"), l.Root.Document)
	if err != nil {
		return environmentCreateRegisteredIssuer{}, err
	}
	signed := filepath.Join(base, "intermediate-signed")
	certPath, err := store.safePath(filepath.Join(signed, "certificate.pem"))
	if err != nil {
		return environmentCreateRegisteredIssuer{}, err
	}
	if _, statErr := os.Lstat(certPath); os.IsNotExist(statErr) {
		csrHash := environmentPreflightBytesSHA256(csr.Raw)
		if err = runEnvironmentCreatePKICeremony(ctx, store, p.Image, "sign", "--issuer", issuerPath, "--parent", parentPath, "--key", "/store/pki/custody/roots/"+l.Root.ID+"/ca-key.encrypted.pem", "--passphrase-file", "/store/pki/custody/service-root-v1-key-passphrase", "--expected-request-sha256", operation.Digest, "--expected-parent-sha256", l.Root.RootFingerprint, "--expected-csr-sha256", csrHash, "--out", "/store/"+filepath.ToSlash(signed)); err != nil {
			return environmentCreateRegisteredIssuer{}, err
		}
	} else if statErr != nil {
		return environmentCreateRegisteredIssuer{}, environmentCreateBootstrapBlocker("product successor signed artifact cannot be inspected")
	}
	certificate, err := readBoundedEnvironmentFile(certPath, true)
	if err != nil {
		return environmentCreateRegisteredIssuer{}, err
	}
	leaf, err := parseFirstCertificatePEM(certificate)
	root, rootErr := parseFirstCertificatePEM([]byte(l.Root.Certificate))
	if err != nil || rootErr != nil || !leaf.IsCA || leaf.CheckSignatureFrom(root) != nil || !bytes.Equal(leaf.RawSubjectPublicKeyInfo, csr.RawSubjectPublicKeyInfo) {
		return environmentCreateRegisteredIssuer{}, environmentCreateBootstrapBlocker("product successor certificate differs from native CSR or original Root")
	}
	var imported map[string]any
	importIntent := deploymentplan.Object{"operation_id": operation.ID, "issuer_id": operation.IssuerID, "certificate_sha256": environmentPreflightBytesSHA256(certificate)}
	found, err = readProductMutation(ctx, client, "pki-import-"+operation.ID, importIntent, &imported)
	if err != nil {
		return environmentCreateRegisteredIssuer{}, err
	}
	if !found {
		if err = client.ImportIssuer(ctx, operation, string(certificate)); err != nil {
			return environmentCreateRegisteredIssuer{}, err
		}
		imported, err = client.ReadIssuer(ctx, operation.IssuerID)
		if err != nil {
			return environmentCreateRegisteredIssuer{}, err
		}
	}
	ready, err := environmentCreateRegisteredIssuerFromMap(imported, l.Root.RootFingerprint)
	if err != nil || ready.ID != operation.IssuerID || ready.Status != "ready" || ready.Certificate != string(certificate) {
		return ready, environmentCreateBootstrapBlocker("product successor import has no genuine ready receipt")
	}
	var active map[string]any
	found, err = readProductMutation(ctx, client, "pki-activate-"+operation.ID, operation, &active)
	if err != nil {
		return ready, err
	}
	if !found {
		trust, err := productIdentityTrustStageDefinitions(l, ready)
		if err != nil {
			return ready, err
		}
		if err = apply(ctx, "successor-trust", trust); err != nil {
			return ready, err
		}
		if err = waitEnvironmentCreateIssuerAcks(ctx, cfg, runtime, ready, l.Policy.BundleConsumers); err != nil {
			return ready, err
		}
		if err = client.ActivateIssuer(ctx, operation); err != nil {
			return ready, err
		}
		active, err = client.ReadIssuer(ctx, operation.IssuerID)
		if err != nil {
			return ready, err
		}
	}
	result, err := environmentCreateRegisteredIssuerFromMap(active, l.Root.RootFingerprint)
	if err != nil || result.Status != "active" || result.ID != ready.ID || result.Certificate != ready.Certificate || result.BundleVersion != ready.BundleVersion {
		return result, environmentCreateBootstrapBlocker("product successor activation changed its genuine ready identity")
	}
	// Native CRL publication starts only after activation. A READY issuer must
	// never enter a runtime CRL manifest or prevent its listeners from starting.
	for _, issuer := range []environmentCreateRegisteredIssuer{l.Root, result} {
		if _, err = waitProductCurrentCRL(ctx, runtime.PKIClient, issuer); err != nil {
			return result, err
		}
	}
	if err := prepareProductRegistrationCRLs(ctx, store, runtime, l, result); err != nil {
		return result, err
	}
	trust, err := productIdentityTrustStageDefinitions(l, result)
	if err != nil {
		return result, err
	}
	if err = apply(ctx, "successor-crls", trust); err != nil {
		return result, err
	}
	consumers, err := productIdentityCRLConsumers(l)
	if err != nil {
		return result, err
	}
	for _, issuer := range []environmentCreateRegisteredIssuer{l.Root, result} {
		raw, err := waitProductCurrentCRL(ctx, runtime.PKIClient, issuer)
		if err != nil {
			return result, err
		}
		block, _ := pem.Decode([]byte(raw)) // The signed record was validated above.
		if err = waitEnvironmentCreateCRLAcks(ctx, cfg, runtime, issuer.ID, environmentPreflightBytesSHA256(block.Bytes), consumers); err != nil {
			return result, err
		}
	}
	return result, runtime.Execution.Check(ctx)
}

type environmentProductIdentityReceipt struct {
	ResourceID, Subject, SecretUID, ClaimUID, CertificateSHA256 string
}

type environmentProductIdentitySession struct {
	Schema, ParentSHA256, PlanSHA256, OwnerSHA256 string
	Ceremony                                      environmentCreateServiceCeremony
	Outputs                                       []environmentProductIdentityReceipt
	EnrollmentSessionID                           string `json:",omitempty"`
}

func productIdentityScope(l *createNormalTailProductLifecycle) string {
	return deploymentplan.Digest(l)
}

func productIdentitySessionPath(l *createNormalTailProductLifecycle) string {
	return filepath.Join("deployment/create", l.ParentSHA256, "bootstrap/product-identities", productIdentityScope(l), "installed.json")
}

// Once the original credential child is accepted, an earlier issuer or role
// recipe may already have been superseded by this session's cleanup role. Read
// that exact UID-bound credential and native session instead of replaying the
// earlier protocol. An incomplete or unknown credential child is never adopted.
func resumeProductIdentitySession(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle) (environmentCreateServiceCeremony, bool, error) {
	s := environmentCreateServiceCeremony{Root: l.Root, Intermediate: l.Intermediate}
	key := "kubernetes/product-identities-" + productIdentityScope(l)[:12] + "-service-credential-secrets"
	if createOpenBaoACKCompletionEntry(runtime.Execution.Journal, key) == nil {
		return s, false, nil
	}
	bad := func(stage string) (environmentCreateServiceCeremony, bool, error) {
		return s, false, environmentCreateBootstrapBlocker("original product session credential, accepted owner or native lineage changed: " + stage)
	}
	if l.RequiresSuccessor {
		request := environmentCreateIssuerRequest{Environment: cfg.Environment, Domain: "service", Kind: "intermediate", ParentID: l.Root.ID, ServerDNSNames: l.Policy.ServerDNSNames, ServiceClientIDs: l.Policy.ServiceClientIDs}
		var operation environmentCreateIssuerOperation
		found, err := readProductMutation(ctx, runtime.PKIClient, "pki-reserve-service-intermediate", request, &operation)
		if err != nil || !found || operation.Digest != deploymentplan.Digest(request) {
			return bad("reservation-receipt")
		}
		var active map[string]any
		found, err = readProductMutation(ctx, runtime.PKIClient, "pki-activate-"+operation.ID, operation, &active)
		if err != nil || !found {
			return bad("activation-receipt")
		}
		s.Intermediate, err = environmentCreateRegisteredIssuerFromMap(active, l.Root.RootFingerprint)
		if err != nil || s.Intermediate.ID != operation.IssuerID || s.Intermediate.Status != "active" {
			return bad("active-original-issuer")
		}
	}
	actualIssuer, err := runtime.PKIClient.ReadIssuer(ctx, s.Intermediate.ID)
	if err != nil {
		return s, false, err
	}
	registered, err := environmentCreateRegisteredIssuerFromMap(actualIssuer, l.Root.RootFingerprint)
	if err != nil || registered.Status != "active" || registered.ID != s.Intermediate.ID || registered.Certificate != s.Intermediate.Certificate || registered.Reference != s.Intermediate.Reference || registered.BundleVersion != s.Intermediate.BundleVersion {
		return bad("live-issuer-lineage")
	}
	var intent struct {
		Session    string   `json:"session_id"`
		Claim      string   `json:"claim"`
		Subjects   []string `json:"subjects"`
		Issuer     string   `json:"issuer_id"`
		Root       string   `json:"root_sha256"`
		Namespaces []string `json:"credential_namespaces"`
	}
	base := environmentCreateCeremonyDirectory(runtime, "service")
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, "session-intent.json"), &intent) != nil || !environmentBootstrapOwnerUUID.MatchString(intent.Session) || intent.Claim != "pki-product-bootstrap-"+productIdentityScope(l)[:12] || intent.Issuer != s.Intermediate.ID || intent.Root != l.Root.RootFingerprint || !slices.Equal(intent.Namespaces, []string{p.Stack + "-video-cloud"}) {
		return bad("session-intent")
	}
	var subjects []string
	for _, output := range l.Outputs {
		subjects = append(subjects, output.Subject)
	}
	slices.Sort(subjects)
	if !slices.Equal(intent.Subjects, subjects) {
		return bad("subject-policy")
	}
	s.BootstrapSessionID, s.BootstrapClaim = intent.Session, intent.Claim
	s.BootstrapCredentialNamespace, s.BootstrapCredentialNamespaces = intent.Namespaces[0], slices.Clone(intent.Namespaces)
	s.BootstrapCredentialSecret = "service-bootstrap-" + intent.Session
	s.CustodyReader = "pki-product-custody-" + productIdentityScope(l)[:12]
	plan, journal, err := acceptedCreateStage(store, runtime.Execution, key)
	id := "Secret/" + s.BootstrapCredentialNamespace + "/" + s.BootstrapCredentialSecret
	if err != nil || len(plan.Resources) != 1 || plan.Resources[0].ID != id || !environmentBootstrapOwnerUUID.MatchString(journal.Owners[id]) {
		return bad("accepted-credential-child")
	}
	actual, err := runtime.Backend.Read(ctx, id)
	if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != journal.Owners[id] || !deploymentplan.Matches(plan.Resources[0].Desired, actual) {
		return bad("native-credential-uid-body")
	}
	material := map[string]string{}
	data := deploymentplan.Map(actual["data"])
	for _, name := range []string{"tls.crt", "tls.key", "ca.crt"} {
		raw, err := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(data[name]))
		if err != nil || len(raw) == 0 {
			return bad("credential-base64")
		}
		material[name] = string(raw)
	}
	pair, err := tls.X509KeyPair([]byte(material["tls.crt"]), []byte(material["tls.key"]))
	if err != nil || !managedLateBootstrapChainMatches(pair.Certificate, s) || material["ca.crt"] != s.Root.Certificate {
		return bad("credential-keypair-chain")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || time.Now().Before(leaf.NotBefore) || !time.Now().Before(leaf.NotAfter) {
		return bad("credential-leaf-validity")
	}
	query := "SELECT json_build_object('session_id',session_id,'issuer_id',issuer_id,'root_sha256',root_sha256,'bootstrap_fingerprint',bootstrap_fingerprint,'status',status,'subjects',subjects,'expires_at',expires_at) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND session_id='" + s.BootstrapSessionID + "'"
	sessionRaw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var native struct {
		Session     string    `json:"session_id"`
		Issuer      string    `json:"issuer_id"`
		Root        string    `json:"root_sha256"`
		Fingerprint string    `json:"bootstrap_fingerprint"`
		Status      string    `json:"status"`
		Subjects    []string  `json:"subjects"`
		Expires     time.Time `json:"expires_at"`
	}
	if err != nil || decodeEnvironmentJSON(sessionRaw, &native) != nil || native.Session != s.BootstrapSessionID || native.Issuer != s.Intermediate.ID || native.Root != l.Root.RootFingerprint || native.Fingerprint != environmentPreflightBytesSHA256(leaf.Raw) || native.Status != "active" || !native.Expires.After(time.Now()) || !slices.Equal(native.Subjects, subjects) {
		return bad("native-session-row")
	}
	readerPlan, readerJournal, err := acceptedCreateStageForProduct(store, runtime.Execution, l, "service-custody-reader")
	reader := renderEnvironmentCreateCustodyReader(cfg, p, s)
	if err != nil || len(readerPlan.Resources) != 1 || readerPlan.Resources[0].ID != deploymentplan.ID(reader.Object) || !deploymentplan.Matches(reader.Object, readerPlan.Resources[0].Desired) {
		return bad("accepted-custody-child")
	}
	owner, err := runtime.Backend.Read(ctx, readerPlan.Resources[0].ID)
	if err != nil || owner == nil || deploymentplan.Map(owner["metadata"])["uid"] != readerJournal.Owners[readerPlan.Resources[0].ID] || !deploymentplan.Matches(readerPlan.Resources[0].Desired, owner) {
		return bad("native-custody-owner")
	}
	for _, file := range []struct{ name, expected string }{{"bootstrap.key", material["tls.key"]}, {"bootstrap.crt", material["tls.crt"]}} {
		raw, err := runtime.Backend.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "cat", "/run/pki-bootstrap/"+file.name)
		if err != nil || !bytes.Equal(raw, []byte(file.expected)) {
			return bad("custody-key-certificate-bytes")
		}
	}
	policyRaw, err := runtime.Backend.command(ctx, nil, "--namespace", p.Stack+"-video-cloud", "exec", "deployment/pki-controller", "--container", "pki-controller", "--", "/app/pkicontroller", "render-openbao-policy", s.Intermediate.ID)
	var policy environmentCreateOpenBaoIssuerPolicies
	if err != nil || decodeEnvironmentJSON(policyRaw, &policy) != nil || policy.IssuerID != s.Intermediate.ID || policy.Environment != cfg.Environment || policy.ServiceClientSignerPolicy == "" {
		return bad("policy-render")
	}
	namespace, err := runtime.Backend.Read(ctx, "Namespace//"+s.BootstrapCredentialNamespace)
	var frozen struct {
		Clusters []struct {
			Cluster map[string]any `yaml:"cluster"`
		} `yaml:"clusters"`
	}
	if err != nil || namespace == nil || yaml.Unmarshal(runtime.Backend.KubeconfigContent, &frozen) != nil || len(frozen.Clusters) != 1 {
		return bad("namespace-kubernetes-ca")
	}
	s.CredentialUIDs = map[string]string{s.BootstrapCredentialNamespace: journal.Owners[id]}
	s.CleanupBinding = environmentBootstrapCleanupBinding{Version: 1, Environment: cfg.Environment, SessionID: s.BootstrapSessionID, NamespaceUID: deploymentplan.String(deploymentplan.Map(namespace["metadata"])["uid"]), SecretUID: journal.Owners[id], PolicySHA256: environmentPreflightBytesSHA256([]byte(policy.ServiceClientSignerPolicy)), KubernetesCA: deploymentplan.String(frozen.Clusters[0].Cluster["certificate-authority-data"]), ConfigMapName: "pki-bootstrap-cleanup-binding-" + s.BootstrapSessionID[:12]}
	if validateEnvironmentBootstrapCleanupBinding(&s.CleanupBinding, cfg.Environment) != nil {
		return bad("cleanup-binding")
	}
	sessionPolicy := "rtk-" + cfg.Environment + "-service-bootstrap-" + s.BootstrapSessionID
	text := fmt.Sprintf("path %q {\n  capabilities = [\"read\", \"delete\"]\n}\n", "sys/policies/acl/"+sessionPolicy)
	if err = installEnvironmentCreateOpenBaoPolicy(ctx, cfg, runtime, "create-"+s.BootstrapSessionID+"-cleanup-controller", text); err != nil {
		return s, false, err
	}
	if err = configureEnvironmentCreateOpenBaoRole(ctx, cfg, runtime, "pki-controller-"+cfg.Environment, []string{"pki-controller"}, environmentCreateControllerPolicies(runtime)); err != nil {
		return s, false, err
	}
	if _, err = writeProductArtifact(store, filepath.Join(base, "session-observed.json"), deploymentplan.Object{"session": json.RawMessage(sessionRaw), "cleanup_binding": s.CleanupBinding, "credential_uids": s.CredentialUIDs, "custody_reader": s.CustodyReader}); err != nil {
		return s, false, err
	}
	s.RootCRLPEM, err = readProductCurrentCRL(ctx, runtime.PKIClient, s.Root)
	s.RootCRLSecret = "pki-service-root-crl"
	if err != nil {
		return s, false, err
	}
	return s, true, runtime.Execution.Check(ctx)
}

func readProductSignerPolicies(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime) ([]string, error) {
	return readProductRolePolicies(ctx, cfg, runtime, "certissuer-"+cfg.Environment, "certissuer")
}

func readProductRolePolicies(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, name, account string) ([]string, error) {
	token, err := readBoundedEnvironmentFile(runtime.OpenBao.RootTokenPath, true)
	if err != nil {
		return nil, err
	}
	raw, err := environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, append(bytes.TrimSpace(token), '\n'), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao read -format=json auth/kubernetes/role/"+name+"\n")
	var role struct {
		Data struct {
			Accounts   []string `json:"bound_service_account_names"`
			Namespaces []string `json:"bound_service_account_namespaces"`
			Policies   []string `json:"token_policies"`
			Audience   string   `json:"audience"`
			NoDefault  bool     `json:"token_no_default_policy"`
			TTL        int      `json:"token_ttl"`
			MaxTTL     int      `json:"token_max_ttl"`
		} `json:"data"`
	}
	if err != nil || len(raw) > 256<<10 || json.Unmarshal(raw, &role) != nil || !slices.Equal(role.Data.Accounts, []string{account}) || !slices.Equal(role.Data.Namespaces, []string{cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud"}) || role.Data.Audience != "openbao" || !role.Data.NoDefault || role.Data.TTL != 900 || role.Data.MaxTTL != 900 || len(role.Data.Policies) == 0 {
		return nil, environmentCreateBootstrapBlocker("existing exact Service signer role cannot be qualified")
	}
	slices.Sort(role.Data.Policies)
	for i, name := range role.Data.Policies {
		if !secretEnvironmentPattern.MatchString(name) || i > 0 && name == role.Data.Policies[i-1] {
			return nil, environmentCreateBootstrapBlocker("existing Service signer policy set is ambiguous")
		}
	}
	return role.Data.Policies, nil
}

func productLifecycleApply(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, c *compiledEnvironmentDeployment, l *createNormalTailProductLifecycle) environmentCreateBootstrapApply {
	return func(ctx context.Context, stage string, definitions []deploymentplan.Definition) error {
		if err := h.Execution.Check(ctx); err != nil {
			return err
		}
		parent := *h.OriginalPlan
		parent.Release = c.Release
		ctx = context.WithValue(ctx, environmentCreateBootstrapRuntimeKey{}, l.runtime)
		apply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
			return applyCreateNormalTailStage(ctx, cfg, c.Store, &parent, h.Execution, "product-identities-"+productIdentityScope(l)[:12]+"-"+stage, defs, io.Discard)
		}
		if stage == "service-session" {
			if handled, err := executeProductSessionTrustForward(ctx, cfg, c.Store, l.runtime, l, apply); handled || err != nil {
				return err
			}
		}
		return applyCreateNormalTailStage(ctx, cfg, c.Store, &parent, h.Execution, "product-identities-"+productIdentityScope(l)[:12]+"-"+stage, definitions, io.Discard)
	}
}

// All source definitions come from the one frozen maintained catalog. Only
// native-assigned public issuer references and the exact session controls vary.
func productIdentityControlDefinitions(cfg deploymentConfig, l *createNormalTailProductLifecycle, service environmentCreateServiceCeremony, bootstrap bool) ([]deploymentplan.Definition, error) {
	if l == nil || len(l.SourceDefinitions) == 0 || l.Root.RootFingerprint != service.Root.RootFingerprint {
		return nil, environmentCreateBootstrapBlocker("product control-plane source is not the frozen maintained catalog")
	}
	var out []deploymentplan.Definition
	found := map[string]bool{}
	for _, original := range l.SourceDefinitions {
		id := deploymentplan.ID(original.Object)
		if id != "Deployment/"+cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud/pki-controller" && id != "Deployment/"+cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud/certissuer" {
			continue
		}
		if found[id] || !environmentBootstrapOwnerUUID.MatchString(l.SourceOwners[id]) {
			return nil, environmentCreateBootstrapBlocker("product control-plane original owner is ambiguous")
		}
		found[id] = true
		d := deploymentplan.Definition{Object: deploymentplan.Clone(original.Object), Phase: "workload"}
		deploymentplan.Map(d.Object["metadata"])["uid"] = l.SourceOwners[id]
		name := deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"])
		if name == "pki-controller" {
			common, err := applyEnvironmentBootstrapCleanup(deploymentEndpointValues(cfg), d.Object, service.CleanupBinding)
			if err != nil {
				return nil, err
			}
			out = append(out, common...)
		} else {
			var container deploymentplan.Object
			for _, raw := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
				c := deploymentplan.Map(raw)
				if c["name"] == "certissuer" {
					if container != nil {
						return nil, environmentCreateBootstrapBlocker("product issuer container is ambiguous")
					}
					container = c
				}
			}
			if container == nil {
				return nil, environmentCreateBootstrapBlocker("product issuer container is absent")
			}
			settings := map[string]string{"CERT_ISSUER_OPENBAO_PKI_MOUNT": service.Intermediate.Reference, "CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN": "^$"}
			container["env"] = environmentCreateServiceWithoutEnv(container["env"], "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CALLER", "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID", "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SUBJECTS")
			if bootstrap {
				var subjects []string
				for _, product := range l.Outputs {
					subjects = append(subjects, product.Subject)
				}
				slices.Sort(subjects)
				settings["CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CALLER"], settings["CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID"], settings["CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SUBJECTS"] = "service:deployment-bootstrap", service.BootstrapSessionID, strings.Join(subjects, ",")
			}
			container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(settings))
		}
		out = append(out, d)
	}
	if len(found) != 2 {
		return nil, environmentCreateBootstrapBlocker("both maintained Service control-plane owners are required for product enrollment")
	}
	declared, err := environmentProductConnectivityDeclared(cfg, l)
	if err != nil {
		return nil, err
	}
	if declared {
		dependencies, err := environmentProductEnrollmentConnectivityDefinitions(context.Background(), cfg, l, service)
		if err != nil {
			return nil, err
		}
		out = append(out, dependencies...)
	}
	return out, nil
}

func productIdentityTrustDefinitions(l *createNormalTailProductLifecycle, next environmentCreateRegisteredIssuer) ([]deploymentplan.Definition, error) {
	if l == nil || next.ID == l.Intermediate.ID || next.Environment != l.Root.Environment || next.Domain != "service" || next.Kind != "intermediate" || next.RootFingerprint != l.Root.RootFingerprint || next.Document["parent_issuer_id"] != l.Root.ID || next.Status != "ready" && next.Status != "active" {
		return nil, environmentCreateBootstrapBlocker("successor trust requires its actual same-Root ready or active issuer")
	}
	var out []deploymentplan.Definition
	for _, source := range l.SourceTrust {
		if source.Baseline["kind"] != "ConfigMap" {
			continue
		}
		object := deploymentplan.Clone(source.Baseline)
		changed := false
		for key, value := range deploymentplan.Map(object["data"]) {
			text, ok := value.(string)
			if !ok {
				continue
			}
			updated, _, didChange, err := productIdentityTrustManifest(l, text, &next, next.Status == "active")
			if err != nil {
				return nil, err
			}
			if didChange {
				deploymentplan.Map(object["data"])[key] = updated
				changed = true
			}
		}
		if changed {
			id := deploymentplan.ID(object)
			if !environmentBootstrapOwnerUUID.MatchString(l.SourceOwners[id]) {
				return nil, environmentCreateBootstrapBlocker("Service bundle owner lacks its recorded UID")
			}
			deploymentplan.Map(object["metadata"])["uid"] = l.SourceOwners[id]
			out = append(out, deploymentplan.Definition{Object: object, Phase: "configuration"})
		}
	}
	registration, err := productRegistrationCRLDefinition(l, next)
	if err != nil {
		return nil, err
	}
	if registration != nil {
		out = append(out, *registration)
	}
	if len(out) == 0 {
		return nil, environmentCreateBootstrapBlocker("frozen Service consumer catalog has no original ICA bundle distribution")
	}
	return out, nil
}

type productIdentityCRLReference struct {
	Issuer    map[string]any `json:"issuer"`
	StatePath string         `json:"state_path"`
}

// Recognize only the two maintained trust formats. Unrelated ConfigMap data
// remains byte-for-byte intact; references to this ICA retain exact lineage.
func productIdentityTrustManifest(l *createNormalTailProductLifecycle, text string, next *environmentCreateRegisteredIssuer, includeCRL bool) (string, bool, bool, error) {
	fail := func() (string, bool, bool, error) {
		return text, false, false, environmentCreateBootstrapBlocker("original Service trust manifest differs from its exact registered lineage or state path")
	}
	var bundle []map[string]string
	if decodeEnvironmentJSON([]byte(text), &bundle) == nil && len(bundle) > 0 {
		contains := false
		for _, ref := range bundle {
			contains = contains || ref["issuer_id"] == l.Intermediate.ID
		}
		if !contains {
			return text, false, false, nil
		}
		seen := map[string]bool{}
		for _, ref := range bundle {
			id := ref["issuer_id"]
			if len(ref) != 2 || !environmentBootstrapOwnerUUID.MatchString(id) || !environmentPreflightDigest.MatchString(ref["trust_bundle_version"]) || seen[id] || id == l.Intermediate.ID && ref["trust_bundle_version"] != l.Intermediate.BundleVersion || next != nil && id == next.ID && ref["trust_bundle_version"] != next.BundleVersion {
				return fail()
			}
			seen[id] = true
		}
		if next == nil || seen[next.ID] {
			return text, true, false, nil
		}
		bundle = append(bundle, map[string]string{"issuer_id": next.ID, "trust_bundle_version": next.BundleVersion})
		raw, _ := json.Marshal(bundle)
		return string(raw), true, true, nil
	}
	var crls []productIdentityCRLReference
	if decodeEnvironmentJSON([]byte(text), &crls) != nil || len(crls) == 0 {
		// A malformed recognized reference cannot disappear from the qualified
		// source merely because an unknown field defeated the strict decoder.
		var entries []map[string]json.RawMessage
		if json.Unmarshal([]byte(text), &entries) == nil {
			for _, entry := range entries {
				var id string
				var issuer map[string]any
				_ = json.Unmarshal(entry["issuer_id"], &id)
				_ = json.Unmarshal(entry["issuer"], &issuer)
				if id == l.Intermediate.ID || issuer["issuer_id"] == l.Intermediate.ID {
					return fail()
				}
			}
		}
		return text, false, false, nil
	}
	old, root := -1, -1
	for i, ref := range crls {
		if ref.Issuer["issuer_id"] == l.Intermediate.ID {
			old = i
		}
		if ref.Issuer["issuer_id"] == l.Root.ID {
			root = i
		}
	}
	if old < 0 {
		return text, false, false, nil
	}
	if root < 0 || deploymentplan.Digest(crls[old].Issuer) != deploymentplan.Digest(l.Intermediate.Document) || deploymentplan.Digest(crls[root].Issuer) != deploymentplan.Digest(l.Root.Document) || !strings.HasSuffix(crls[root].StatePath, ".json") || crls[old].StatePath != strings.TrimSuffix(crls[root].StatePath, ".json")+"-"+l.Intermediate.ID+".json" {
		return fail()
	}
	seen, paths := map[string]bool{}, map[string]bool{}
	for _, ref := range crls {
		id := deploymentplan.String(ref.Issuer["issuer_id"])
		if !environmentBootstrapOwnerUUID.MatchString(id) || seen[id] || !filepath.IsAbs(ref.StatePath) || filepath.Clean(ref.StatePath) != ref.StatePath || paths[ref.StatePath] {
			return fail()
		}
		seen[id], paths[ref.StatePath] = true, true
	}
	if next == nil || !includeCRL {
		return text, true, false, nil
	}
	path := strings.TrimSuffix(crls[root].StatePath, ".json") + "-" + next.ID + ".json"
	if seen[next.ID] {
		for _, ref := range crls {
			if ref.Issuer["issuer_id"] == next.ID && (deploymentplan.Digest(ref.Issuer) != deploymentplan.Digest(next.Document) || ref.StatePath != path) {
				return fail()
			}
		}
		return text, true, false, nil
	}
	if paths[path] || next.Status != "active" || next.Document["status"] != "active" {
		return fail()
	}
	crls = append(crls, productIdentityCRLReference{Issuer: deploymentplan.Clone(next.Document), StatePath: path})
	raw, _ := json.Marshal(crls)
	return string(raw), true, true, nil
}

// Reload only the original current readers. This early transition never copies
// them into the separately resolved final maintained workload target.
func productIdentityTrustStageDefinitions(l *createNormalTailProductLifecycle, next environmentCreateRegisteredIssuer) ([]deploymentplan.Definition, error) {
	definitions, err := productIdentityTrustDefinitions(l, next)
	if err != nil {
		return nil, err
	}
	changed := map[string]string{}
	for _, d := range definitions {
		changed[deploymentplan.ID(d.Object)] = deploymentplan.Digest(d.Object["data"])
	}
	for _, source := range l.SourceTrust {
		switch source.Baseline["kind"] {
		case "Deployment", "StatefulSet", "DaemonSet":
		default:
			continue
		}
		refs := map[string]string{}
		for _, ref := range environmentResourceReferences(source.Baseline) {
			if hash := changed[ref.ID]; hash != "" {
				refs[ref.ID] = hash
			}
		}
		if len(refs) == 0 {
			continue
		}
		if deploymentplan.ID(source.Baseline) != source.ID || !environmentBootstrapOwnerUUID.MatchString(l.SourceOwners[source.ID]) || deploymentplan.Map(source.Baseline["metadata"])["uid"] != l.SourceOwners[source.ID] {
			return nil, environmentCreateBootstrapBlocker("successor trust reload differs from its original native owner")
		}
		object := deploymentplan.Clone(source.Baseline)
		environmentCreateServiceBundleRollout(object, deploymentplan.Digest(refs))
		definitions = append(definitions, deploymentplan.Definition{Object: object, Phase: "workload"})
	}
	return definitions, nil
}

func productIdentityCRLConsumers(l *createNormalTailProductLifecycle) ([]string, error) {
	want, err := managedTransportServiceConsumerIDs(l.Policy.Consumers)
	if err != nil {
		return nil, err
	}
	for _, source := range l.SourceDefinitions {
		if deploymentplan.ID(source.Object) != "Deployment/"+l.Bootstrap.Stack+"-video-cloud/pki-controller" {
			continue
		}
		for _, item := range certIssuerObjectList(environmentPKIPod(source.Object)["containers"]) {
			container := deploymentplan.Map(item)
			if container["name"] == "pki-controller" {
				actual := strings.Split(mqttFoundationEnv(container, "PKI_REQUIRED_CONSUMERS_SERVICE"), ",")
				slices.Sort(actual)
				if slices.Equal(actual, want) {
					return actual, nil
				}
			}
		}
	}
	return nil, environmentCreateBootstrapBlocker("successor CRL receipts require the exact frozen current Service consumer roster")
}

func waitProductCurrentCRL(ctx context.Context, client *environmentCreatePKIClient, issuer environmentCreateRegisteredIssuer) (string, error) {
	for {
		if client == nil || client.Execution == nil {
			return "", environmentCreateBootstrapBlocker("successor CRL publication requires its original held execution")
		}
		if err := client.Execution.Check(ctx); err != nil {
			return "", err
		}
		raw, err := readProductCurrentCRL(ctx, client, issuer)
		if err == nil {
			return raw, nil
		}
		var unavailable *environmentCreatePKIHTTPFailure
		if !errors.As(err, &unavailable) || unavailable.Status != http.StatusNotFound && unavailable.Status != http.StatusServiceUnavailable {
			return "", err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
		}
	}
}

func productIdentityEnrollmentOwner(cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, product environmentCreateProductIdentity) (deploymentplan.Definition, error) {
	job, err := environmentCreateClientEnrollmentDefinition(cfg, p, runtime, service, p.Stack+"-video-cloud", strings.TrimPrefix(product.Subject, "service:"), product.Claim)
	if err != nil {
		return job, err
	}
	pod := environmentPKIPod(job.Object)
	name := "pki-product-" + strings.TrimPrefix(product.Subject, "service:") + "-" + strings.ReplaceAll(service.BootstrapSessionID, "-", "")[:12]
	container := deploymentplan.Map(certIssuerObjectList(pod["containers"])[0])
	container["command"] = []any{"/bin/sh", "-ec", "/app/serviceidentity-bootstrap " + product.Subject + " && exec sleep 3600"}
	container["readinessProbe"] = deploymentplan.Object{"exec": deploymentplan.Object{"command": []any{"/bin/sh", "-ec", "test -s /var/lib/pki-service/private/client.json"}}, "periodSeconds": float64(2)}
	labels := deploymentplan.Object{"app.kubernetes.io/name": name, "rtk.realtek.com/stack": p.Stack}
	delete(pod, "restartPolicy")
	return deploymentplan.Definition{Phase: "bootstrap", Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": name, "namespace": p.Stack + "-video-cloud", "labels": labels}, "spec": deploymentplan.Object{"replicas": float64(1), "strategy": deploymentplan.Object{"type": "Recreate"}, "selector": deploymentplan.Object{"matchLabels": labels}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": labels}, "spec": pod}}}}, nil
}

func productIdentityMaterial(raw, inspected []byte, product environmentCreateProductIdentity, service environmentCreateServiceCeremony, row managedSessionClient, now time.Time) (map[string]string, error) {
	bad := func() (map[string]string, error) {
		return nil, environmentCreateBootstrapBlocker("native product identity state, registered receipt or original lineage differs")
	}
	if len(raw) == 0 || len(raw) > 256<<10 || len(inspected) > 64<<10 {
		return bad()
	}
	var state struct {
		Version  int      `json:"version"`
		Subject  string   `json:"subject"`
		Domain   string   `json:"domain,omitempty"`
		DNSNames []string `json:"dns_names,omitempty"`
		Current  *struct {
			PrivateKey  string    `json:"private_key_pem"`
			Chain       string    `json:"certificate_chain_pem"`
			InstalledAt time.Time `json:"installed_at"`
		} `json:"current,omitempty"`
		Pending json.RawMessage `json:"pending,omitempty"`
	}
	var proof struct {
		Subject     string `json:"subject"`
		Fingerprint string `json:"fingerprint"`
		PublicKey   string `json:"public_key_sha256"`
		Root        string `json:"root_sha256"`
		State       string `json:"state_sha256"`
		Pending     bool   `json:"pending"`
	}
	if decodeEnvironmentJSON(raw, &state) != nil || decodeEnvironmentJSON(inspected, &proof) != nil || state.Version != 1 || state.Subject != product.Subject || state.Domain != "" || len(state.DNSNames) != 0 || state.Current == nil || len(state.Pending) != 0 && string(state.Pending) != "null" || proof.Pending || proof.Subject != product.Subject || proof.Root != service.Root.RootFingerprint || proof.State != environmentPreflightBytesSHA256(raw) {
		return bad()
	}
	pair, err := tls.X509KeyPair([]byte(state.Current.Chain), []byte(state.Current.PrivateKey))
	if err != nil || len(pair.Certificate) != 3 {
		return bad()
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return bad()
	}
	ica, err := parseFirstCertificatePEM([]byte(service.Intermediate.Certificate))
	if err != nil {
		return bad()
	}
	root, err := parseFirstCertificatePEM([]byte(service.Root.Certificate))
	if err != nil {
		return bad()
	}
	if leaf.Subject.CommonName != product.Subject || leaf.IsCA || leaf.KeyUsage != x509.KeyUsageDigitalSignature || !slices.Equal(leaf.ExtKeyUsage, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}) || len(leaf.Subject.Names) != 1 || len(leaf.DNSNames)+len(leaf.IPAddresses)+len(leaf.EmailAddresses)+len(leaf.URIs) != 0 || leaf.CheckSignatureFrom(ica) != nil || ica.CheckSignatureFrom(root) != nil || root.CheckSignatureFrom(root) != nil || !slices.Equal(pair.Certificate[1], ica.Raw) || !slices.Equal(pair.Certificate[2], root.Raw) || leaf.NotBefore.After(now) || !leaf.NotAfter.After(now) || proof.Fingerprint != environmentPreflightBytesSHA256(leaf.Raw) || proof.PublicKey != environmentPreflightBytesSHA256(leaf.RawSubjectPublicKeyInfo) || row.Subject != product.Subject || row.Issuer != service.Intermediate.ID || row.Status != "succeeded" || row.Revoked != nil || row.Fingerprint != proof.Fingerprint || row.Caller != "service:deployment-bootstrap" || row.Request == "" {
		return bad()
	}
	registered, err := parseFirstCertificatePEM([]byte(row.Certificate))
	if err != nil || !slices.Equal(registered.Raw, leaf.Raw) {
		return bad()
	}
	request, rest := pem.Decode([]byte(row.CSR))
	if request == nil || request.Type != "CERTIFICATE REQUEST" || len(bytes.TrimSpace(rest)) != 0 {
		return bad()
	}
	csr, err := x509.ParseCertificateRequest(request.Bytes)
	if err != nil || csr.CheckSignature() != nil || csr.Subject.CommonName != product.Subject || !bytes.Equal(csr.RawSubjectPublicKeyInfo, leaf.RawSubjectPublicKeyInfo) || len(csr.DNSNames)+len(csr.IPAddresses)+len(csr.URIs)+len(csr.EmailAddresses) != 0 || state.Current.InstalledAt.IsZero() || state.Current.InstalledAt.After(now) {
		return bad()
	}
	for _, extension := range leaf.Extensions {
		if extension.Id.Equal([]int{2, 5, 29, 17}) {
			return bad()
		}
	}
	roots := x509.NewCertPool()
	roots.AddCert(root)
	intermediates := x509.NewCertPool()
	intermediates.AddCert(ica)
	if _, err = leaf.Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return bad()
	}
	return map[string]string{"client.crt": state.Current.Chain, "client.key": state.Current.PrivateKey, "server-ca.crt": service.Root.Certificate}, nil
}
