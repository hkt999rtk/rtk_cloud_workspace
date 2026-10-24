package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const productIdentitySessionSchema = "rtk-cloud.product-identities/v1"

func productIdentityRuntime(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, c *compiledEnvironmentDeployment, l *createNormalTailProductLifecycle) (environmentCreateBootstrapRuntime, environmentCreateBootstrapPlan, error) {
	var runtime environmentCreateBootstrapRuntime
	var plan environmentCreateBootstrapPlan
	if h == nil || h.Execution == nil || h.OriginalPlan == nil || c == nil || l == nil || c.Store != h.Execution.Store || l.ParentSHA256 != h.Execution.Journal.PlanSHA256 || l.OwnerSHA256 != deploymentplan.Digest(h.Execution.owner) {
		return runtime, plan, environmentCreateBootstrapBlocker("product operation has no original qualified parent and owner")
	}
	program, err := readCreateNormalTailProgram(c.Store, h.Execution)
	if err != nil || deploymentplan.Digest(program.Products) != productIdentityScope(l) || deploymentplan.Digest(program.Release) != deploymentplan.Digest(c.Release) {
		return runtime, plan, environmentCreateBootstrapBlocker("product operation differs from its one admitted remaining program")
	}
	if err = verifyCreateNormalTailProgramAdmission(c.Store, h.Execution); err != nil {
		return runtime, plan, err
	}
	if err = hydrateCreateNormalTailProducts(ctx, cfg, h, c.Backend, l); err != nil {
		return runtime, plan, err
	}
	runtime = l.runtime
	client := *runtime.PKIClient
	client.mutationScope = productIdentityScope(l)
	runtime.PKIClient = &client
	runtime.ProductIdentityScope = productIdentityScope(l)
	runtime.ProductIdentityOpenBaoTrustProfile = l.OpenBaoTrustProfile
	if l.OpenBaoTrustProfile != "" {
		id, _, _, pinErr := productIdentityOpenBaoTrustPins(l)
		if pinErr != nil || l.OpenBaoTrustProfile != productIdentityManagedOpenBaoTrustProfile {
			return runtime, plan, environmentCreateBootstrapBlocker("qualified product OpenBao trust profile is unavailable")
		}
		trust, readErr := c.Backend.Read(ctx, id)
		if readErr != nil || validateProductIdentityOpenBaoTrust(l, trust, time.Now().UTC()) != nil {
			return runtime, plan, environmentCreateBootstrapBlocker("original product OpenBao listener trust changed")
		}
	}
	if decodeEnvironmentJSON(mustProviderRecoveryJSON(l.Bootstrap), &plan) != nil {
		return runtime, plan, environmentCreateBootstrapBlocker("qualified product recipe cannot be restored")
	}
	if plan.Image != c.Release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference || plan.AccountManagerImage != c.Release.Images["LKE_ACCOUNT_MANAGER_IMAGE"].Reference {
		return runtime, plan, environmentCreateBootstrapBlocker("product runtime differs from its one frozen formal release")
	}
	for role, image := range c.Release.Images {
		if plan.Images[role] != image.Reference {
			return runtime, plan, environmentCreateBootstrapBlocker("product image roles differ from the qualified original recipe")
		}
	}
	plan.ProductIdentities = slices.Clone(l.Outputs)
	for i := range plan.Domains {
		if plan.Domains[i].Domain == "service" {
			plan.Domains[i] = l.Policy
		}
	}
	cm, err := c.Backend.Read(ctx, "ConfigMap/"+plan.Stack+"-video-cloud/pki-create-transport-trust")
	if err != nil || cm == nil || l.SourceOwners[deploymentplan.ID(cm)] != deploymentplan.Map(cm["metadata"])["uid"] {
		return runtime, plan, environmentCreateBootstrapBlocker("product enrollment requires the original recorded management transport authority")
	}
	runtime.TransportCAPEM = deploymentplan.String(deploymentplan.Map(cm["data"])["ca.crt"])
	cert, err := parseFirstCertificatePEM([]byte(runtime.TransportCAPEM))
	if err != nil {
		return runtime, plan, environmentCreateBootstrapBlocker("original management transport authority is malformed")
	}
	runtime.TransportRootSHA256 = environmentPreflightBytesSHA256(cert.Raw)
	runtime.TransportTrustConfigMap = "pki-create-transport-trust"
	runtime.TransportSecrets = map[string]string{"pki-controller": "pki-create-pki-controller-transport", "certissuer": "pki-create-certissuer-transport", "account-manager": "pki-create-account-manager-transport"}
	return runtime, plan, h.Execution.Check(ctx)
}

func readProductInstalled(store secretStore, l *createNormalTailProductLifecycle) (*environmentProductIdentitySession, error) {
	path, err := store.safePath(productIdentitySessionPath(l))
	if err != nil {
		return nil, err
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil {
		return nil, err
	}
	var state environmentProductIdentitySession
	if decodeEnvironmentJSON(raw, &state) != nil || state.Schema != productIdentitySessionSchema || state.ParentSHA256 != l.ParentSHA256 || state.PlanSHA256 != productIdentityScope(l) || state.OwnerSHA256 != l.OwnerSHA256 || state.Ceremony.Root.ID != l.Root.ID || state.Ceremony.Root.RootFingerprint != l.Root.RootFingerprint || len(state.Outputs) != len(l.Outputs) {
		return nil, environmentCreateBootstrapBlocker("original product output receipt differs from the admitted lifecycle")
	}
	if err := bindProductRegistrationCRLs(store, l, state.Ceremony.Intermediate); err != nil {
		return nil, err
	}
	return &state, nil
}

func executeCreateNormalTailProducts(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, c *compiledEnvironmentDeployment, l *createNormalTailProductLifecycle) error {
	if l == nil {
		return nil
	}
	if path, err := c.Store.safePath(productIdentitySessionPath(l)); err != nil {
		return err
	} else if _, err = os.Lstat(path); err == nil {
		runtime, _, er := productIdentityRuntime(ctx, cfg, h, c, l)
		if er != nil {
			return er
		}
		original, er := readProductInstalled(c.Store, l)
		if er != nil {
			return er
		}
		if er = verifyProductIdentityOutputs(ctx, cfg, runtime, l, original); er != nil {
			return er
		}
		s, err := readProductInstalled(c.Store, l)
		if err != nil {
			return err
		}
		defs, err := productEnrollmentClosedDefinitions(cfg, l, s.Ceremony)
		if err != nil {
			return err
		}
		if err = productLifecycleApply(ctx, cfg, h, c, l)(ctx, "enrollment-closed", defs); err != nil {
			return err
		}
		_, err = executeProductSessionRenewalVerified(ctx, cfg, c.Store, runtime, l, original, productLifecycleApply(ctx, cfg, h, c, l))
		return err
	} else if !os.IsNotExist(err) {
		return environmentCreateBootstrapBlocker("original product outputs cannot be inspected")
	}
	runtime, p, err := productIdentityRuntime(ctx, cfg, h, c, l)
	if err != nil {
		return err
	}
	if err = executeProductIdentityProtocol(ctx, cfg, p, c.Store, runtime, l, productLifecycleApply(ctx, cfg, h, c, l)); err != nil {
		return err
	}
	return verifyCreateNormalTailProductOutputs(ctx, cfg, h, c, l)
}

// Keep native protocol orchestration independent of the authenticated transport
// constructor so isolated boundary tests exercise these same production steps.
func executeProductIdentityProtocol(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, apply environmentCreateBootstrapApply) error {
	if runtime.Execution == nil || runtime.PKIClient == nil || runtime.PKIClient.Execution != runtime.Execution || runtime.ProductIdentityScope != productIdentityScope(l) || runtime.PKIClient.mutationScope != productIdentityScope(l) || apply == nil || runtime.Execution.Check(ctx) != nil {
		return environmentCreateBootstrapBlocker("product protocol requires its original authenticated execution and frozen scope")
	}
	service, resumed, err := resumeProductIdentitySession(ctx, cfg, p, store, runtime, l)
	if err != nil {
		return err
	}
	if !resumed {
		controllerPolicies, err := readProductRolePolicies(ctx, cfg, runtime, "pki-controller-"+cfg.Environment, "pki-controller")
		if err != nil || !slices.Equal(controllerPolicies, environmentCreateControllerPolicies(runtime)) {
			return environmentCreateBootstrapBlocker("original controller role changed before product issuer mutation")
		}
		service = environmentCreateServiceCeremony{Root: l.Root, Intermediate: l.Intermediate}
		if l.RequiresSuccessor {
			custodySHA, verifyErr := qualifyProductRootCustody(ctx, store, p.Image, l.Root, runtime.PKIClient.OperatorID)
			if verifyErr != nil || custodySHA != l.RootCustodySHA256 {
				return environmentCreateBootstrapBlocker("original qualified Service Root custody changed before successor mutation")
			}
			service.Intermediate, err = executeProductIdentitySuccessor(ctx, cfg, p, store, runtime, l, apply)
			if err != nil {
				return err
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
		currentPolicies, err := readProductSignerPolicies(ctx, cfg, runtime)
		if err != nil || !slices.Equal(currentPolicies, l.SignerPolicies) && !slices.Equal(currentPolicies, policies) {
			return environmentCreateBootstrapBlocker("product enrollment cannot replace an unrelated Service signer policy binding")
		}
		if err = configureEnvironmentCreateOpenBaoRole(ctx, cfg, runtime, "certissuer-"+cfg.Environment, []string{"certissuer"}, policies); err != nil {
			return err
		}
		crl, err := readProductCurrentCRL(ctx, runtime.PKIClient, service.Root)
		if err != nil {
			return err
		}
		service.RootCRLPEM, service.RootCRLSecret = crl, "pki-service-root-crl"
		var subjects []string
		for _, output := range l.Outputs {
			subjects = append(subjects, output.Subject)
		}
		slices.Sort(subjects)
		service, err = createEnvironmentServiceSession(ctx, cfg, p, store, runtime, service, subjects, apply)
		if err != nil {
			return err
		}
	}
	session := environmentProductIdentitySession{Schema: productIdentitySessionSchema, ParentSHA256: l.ParentSHA256, PlanSHA256: productIdentityScope(l), OwnerSHA256: l.OwnerSHA256, Ceremony: service}
	if _, err = writeProductArtifact(store, filepath.Join(environmentCreateCeremonyDirectory(runtime, "service"), "product-session.json"), session); err != nil {
		return err
	}
	controls, err := productIdentityControlDefinitions(cfg, l, service, true)
	if err != nil {
		return err
	}
	if err = apply(ctx, "session-controls", controls); err != nil {
		return err
	}
	session.Outputs, err = enrollProductIdentityOutputs(ctx, cfg, p, store, runtime, l, service, apply)
	if err != nil {
		return err
	}
	if _, err = writeProductArtifact(store, productIdentitySessionPath(l), session); err != nil {
		return err
	}
	closed, err := productEnrollmentClosedDefinitions(cfg, l, service)
	if err != nil {
		return err
	}
	if err = apply(ctx, "enrollment-closed", closed); err != nil {
		return err
	}
	return runtime.Execution.Check(ctx)
}

type environmentProductIdentityProof struct {
	Subject, StateSHA256, OwnerUID string
	Row                            managedSessionClient
}

func productIdentityProofPath(l *createNormalTailProductLifecycle, role string) string {
	return filepath.Join("deployment/create", l.ParentSHA256, "bootstrap/product-identities", productIdentityScope(l), "service", "identity-"+role+".json")
}
func readProductIdentityProof(store secretStore, l *createNormalTailProductLifecycle, role string) (environmentProductIdentityProof, error) {
	var proof environmentProductIdentityProof
	path, err := store.safePath(productIdentityProofPath(l, role))
	if err != nil {
		return proof, err
	}
	raw, err := readBoundedEnvironmentFile(path, true)
	if err != nil || decodeEnvironmentJSON(raw, &proof) != nil || proof.Subject != "service:"+role || proof.Row.Subject != proof.Subject || !environmentPreflightDigest.MatchString(proof.StateSHA256) || !environmentBootstrapOwnerUUID.MatchString(proof.OwnerUID) {
		return proof, environmentCreateBootstrapBlocker("native product issuance proof cannot be authenticated")
	}
	return proof, nil
}

func acceptedCreateStageForProduct(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, stage string) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	return acceptedCreateStage(store, e, "kubernetes/product-identities-"+productIdentityScope(l)[:12]+"-"+stage)
}

func readProductIssuances(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, subject string) ([]managedSessionClient, error) {
	if !environmentCreateConsumerID.MatchString(strings.TrimPrefix(subject, "service:")) || !environmentBootstrapOwnerUUID.MatchString(s.Intermediate.ID) {
		return nil, environmentCreateBootstrapBlocker("product issuance query lacks its exact qualified subject and issuer")
	}
	query := "SELECT COALESCE(json_agg(json_build_object('subject',subject,'caller',caller,'request_id',request_id,'issuer_id',issuer_id,'fingerprint',fingerprint,'status',status,'revoked_at',revoked_at,'csr_pem',csr_pem,'certificate_pem',certificate_pem) ORDER BY request_id),'[]'::json) FROM pki_service_client_issuances WHERE environment='" + cfg.Environment + "' AND subject='" + subject + "' AND issuer_id='" + s.Intermediate.ID + "'"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var rows []managedSessionClient
	if err != nil || decodeEnvironmentJSON(raw, &rows) != nil {
		return nil, environmentCreateBootstrapBlocker("native product issuance registry cannot be read")
	}
	return rows, nil
}

func approveProductWorkload(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, p environmentCreateProductIdentity, s environmentCreateServiceCeremony) error {
	issuer, err := parseFirstCertificatePEM([]byte(s.Intermediate.Certificate))
	if err != nil {
		return err
	}
	intent := deploymentplan.Object{"service_id": p.Service, "instance_id": p.Instance, "certificate_subject": p.Subject, "issuer_fingerprint_sha256": environmentPreflightBytesSHA256(issuer.Raw), "allowed_option_codes": []string{p.Option}}
	key := "approve-" + strings.TrimPrefix(p.Subject, "service:")
	var receipt deploymentplan.Object
	found, err := readProductMutation(ctx, runtime.PKIClient, key, intent, &receipt, func(ctx context.Context) error {
		if deploymentplan.Digest(receipt) != deploymentplan.Digest(intent) {
			return environmentCreateBootstrapBlocker("recorded product approval differs from its original intent")
		}
		return verifyProductWorkloadApproval(ctx, cfg, runtime, intent)
	})
	if err != nil {
		return err
	}
	if found {
		if deploymentplan.Digest(receipt) != deploymentplan.Digest(intent) {
			return environmentCreateBootstrapBlocker("original product registration approval changed")
		}
		return runtime.Execution.Check(ctx)
	}
	if err = runtime.PKIClient.beginMutation(key, intent); err != nil {
		return err
	}
	if err = runtime.PKIClient.call(ctx, http.MethodPost, "/v1/platform/service-workloads", "", intent, nil); err != nil {
		return err
	}
	return runtime.PKIClient.completeMutation(key, intent)
}

func readProductCurrentCRL(ctx context.Context, client *environmentCreatePKIClient, issuer environmentCreateRegisteredIssuer) (string, error) {
	var record struct {
		Issuer string    `json:"issuer_id"`
		SHA    string    `json:"crl_sha256"`
		Number string    `json:"crl_number"`
		PEM    string    `json:"crl_pem"`
		This   time.Time `json:"this_update"`
		Next   time.Time `json:"next_update"`
	}
	if err := client.call(ctx, http.MethodGet, "/v1/platform/pki/issuers/"+issuer.ID+"/crl", "", nil, &record); err != nil {
		return "", err
	}
	block, rest := pem.Decode([]byte(record.PEM))
	if block == nil || block.Type != "X509 CRL" || len(bytes.TrimSpace(rest)) != 0 {
		return "", environmentCreateBootstrapBlocker("native current Service CRL is malformed")
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	cert, certErr := parseFirstCertificatePEM([]byte(issuer.Certificate))
	now := time.Now().UTC()
	if err != nil || certErr != nil || crl.CheckSignatureFrom(cert) != nil || record.Issuer != issuer.ID || record.SHA != environmentPreflightBytesSHA256(crl.Raw) || !record.This.Equal(crl.ThisUpdate) || !record.Next.Equal(crl.NextUpdate) || record.Number != crl.Number.String() || crl.ThisUpdate.After(now) || !crl.NextUpdate.After(now) {
		return "", environmentCreateBootstrapBlocker("native current Service CRL signature, lineage or validity differs")
	}
	return record.PEM, nil
}

func verifyProductLeafCRL(ctx context.Context, client *environmentCreatePKIClient, issuer environmentCreateRegisteredIssuer, certificate string) error {
	raw, err := readProductCurrentCRL(ctx, client, issuer)
	if err != nil {
		return err
	}
	block, _ := pem.Decode([]byte(raw))
	crl, err := x509.ParseRevocationList(block.Bytes)
	leaf, leafErr := parseFirstCertificatePEM([]byte(certificate))
	if err != nil || leafErr != nil {
		return environmentCreateBootstrapBlocker("native product CRL or leaf is malformed")
	}
	for _, revoked := range crl.RevokedCertificateEntries {
		if revoked.SerialNumber.Cmp(leaf.SerialNumber) == 0 {
			return environmentCreateBootstrapBlocker("native product identity is revoked")
		}
	}
	return nil
}

func verifyCreateNormalTailProductOutputs(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, c *compiledEnvironmentDeployment, l *createNormalTailProductLifecycle) error {
	if l == nil {
		return nil
	}
	runtime, _, err := productIdentityRuntime(ctx, cfg, h, c, l)
	if err != nil {
		return err
	}
	installed, err := readEffectiveProductInstalled(c.Store, h.Execution, l)
	if err != nil {
		return err
	}
	if _, err = resolveCreateNormalTailProductDefinitions(cfg, l, installed); err != nil {
		return err
	}
	return verifyProductIdentityOutputs(ctx, cfg, runtime, l, installed)
}

func verifyProductIdentityOutputs(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, installed *environmentProductIdentitySession) error {
	if l == nil || installed == nil || len(installed.Outputs) != len(l.Outputs) || runtime.Execution == nil {
		return environmentCreateBootstrapBlocker("product outputs lack their exact original installed roster")
	}
	for i, output := range l.Outputs {
		receipt := installed.Outputs[i]
		if receipt.ResourceID != output.ID || receipt.Subject != output.Subject {
			return environmentCreateBootstrapBlocker("product output ordering or qualified subject changed")
		}
		actual, err := runtime.Backend.Read(ctx, output.ID)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != receipt.SecretUID || actual["immutable"] != true || deploymentplan.Map(actual["metadata"])["deletionTimestamp"] != nil {
			return environmentCreateBootstrapBlocker("original product identity mirror UID or immutability changed")
		}
		data := deploymentplan.Map(actual["data"])
		if len(data) != 3 {
			return environmentCreateBootstrapBlocker("product mirror does not contain its exact native identity fields")
		}
		material := map[string]string{}
		for _, name := range []string{"client.crt", "client.key", "server-ca.crt"} {
			raw, err := base64.StdEncoding.DecodeString(deploymentplan.String(data[name]))
			if err != nil || len(raw) == 0 || len(raw) > 256<<10 {
				return environmentCreateBootstrapBlocker("private product mirror fields cannot be decoded")
			}
			material[name] = string(raw)
		}
		rows, err := readProductIssuances(ctx, cfg, runtime, installed.Ceremony, output.Subject)
		if err != nil || len(rows) != 1 || rows[0].Fingerprint != receipt.CertificateSHA256 {
			return environmentCreateBootstrapBlocker("product mirror has no unique unchanged registered native issuance")
		}
		if err = validateProductMirror(material, output, installed.Ceremony, rows[0], time.Now().UTC()); err != nil {
			return err
		}
		if err = verifyProductLeafCRL(ctx, runtime.PKIClient, installed.Ceremony.Intermediate, material["client.crt"]); err != nil {
			return err
		}
		claim, err := runtime.Backend.Read(ctx, "PersistentVolumeClaim/"+cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud/"+output.Claim)
		if err != nil || claim == nil || deploymentplan.Map(claim["metadata"])["uid"] != receipt.ClaimUID {
			return environmentCreateBootstrapBlocker("original product identity state claim UID changed")
		}
	}
	return runtime.Execution.Check(ctx)
}

func validateProductMirror(material map[string]string, product environmentCreateProductIdentity, s environmentCreateServiceCeremony, row managedSessionClient, now time.Time) error {
	raw, _ := json.Marshal(deploymentplan.Object{"version": 1, "subject": product.Subject, "current": deploymentplan.Object{"private_key_pem": material["client.key"], "certificate_chain_pem": material["client.crt"], "installed_at": now}})
	leaf, err := parseFirstCertificatePEM([]byte(material["client.crt"]))
	if err != nil || material["server-ca.crt"] != s.Root.Certificate {
		return environmentCreateBootstrapBlocker("product mirror lineage differs")
	}
	proof, _ := json.Marshal(deploymentplan.Object{"subject": product.Subject, "fingerprint": environmentPreflightBytesSHA256(leaf.Raw), "public_key_sha256": environmentPreflightBytesSHA256(leaf.RawSubjectPublicKeyInfo), "root_sha256": s.Root.RootFingerprint, "state_sha256": environmentPreflightBytesSHA256(raw), "pending": false})
	_, err = productIdentityMaterial(raw, proof, product, s, row, now)
	return err
}

func completeCreateNormalTailProducts(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, c *compiledEnvironmentDeployment, l *createNormalTailProductLifecycle) error {
	if l == nil {
		return nil
	}
	if err := verifyCreateNormalTailProductOutputs(ctx, cfg, h, c, l); err != nil {
		return err
	}
	runtime, p, err := productIdentityRuntime(ctx, cfg, h, c, l)
	if err != nil {
		return err
	}
	installed, err := readEffectiveProductInstalled(c.Store, h.Execution, l)
	if err != nil {
		return err
	}
	if err = completeProductIdentitySession(ctx, cfg, p, c.Store, runtime, l, installed, l.operator, productLifecycleApply(ctx, cfg, h, c, l)); err != nil {
		return err
	}
	return finishProductSteady(ctx, cfg, h, c, l, installed)
}

func completeProductIdentitySession(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, installed *environmentProductIdentitySession, operator map[string]string, renewalApply ...environmentCreateBootstrapApply) error {
	if len(renewalApply) > 1 {
		return productSessionRenewalBlock("ambiguous cleanup executor")
	}
	if len(renewalApply) == 1 {
		ctx = context.WithValue(ctx, productSessionRenewalApplyKey{}, renewalApply[0])
	}
	s := installed.Ceremony
	finalizedPath := filepath.Join(environmentCreateCeremonyDirectory(runtime, "service"), "finalized.json")
	if path, err := store.safePath(finalizedPath); err != nil {
		return err
	} else if _, err = os.Lstat(path); err == nil {
		var proof struct {
			Native    environmentCreateBootstrapFinalization `json:"native_finalization"`
			Tombstone json.RawMessage                        `json:"custody_tombstone"`
			UIDs      map[string]string                      `json:"credential_uids"`
		}
		raw, err := readBoundedEnvironmentFile(path, true)
		if err != nil || decodeEnvironmentJSON(raw, &proof) != nil || proof.Native.SessionID != s.BootstrapSessionID || proof.Native.Environment != cfg.Environment || proof.Native.FinalizedAt.IsZero() {
			return environmentCreateBootstrapBlocker("product finalization lacks its original native durable proof")
		}
		if entry := createOpenBaoACKCompletionEntry(runtime.Execution.Journal, environmentCreateServiceFinalizationKey(runtime)); entry != nil && entry.State != "completed" {
			if err = reconcileProductFinalization(ctx, cfg, runtime, l, installed, proof.Native); err != nil {
				return err
			}
		}
		if _, err = verifyProductFinalization(ctx, cfg, runtime, l, installed); err != nil {
			return err
		}
		return runtime.Execution.Check(ctx)
	} else if !os.IsNotExist(err) {
		return environmentCreateBootstrapBlocker("original product finalization proof cannot be inspected")
	}
	var expected []string
	for _, product := range l.Outputs {
		expected = append(expected, product.Subject)
	}
	if err := waitProductSessionInstallations(ctx, cfg, runtime, s, expected); err != nil {
		return err
	}
	if err := finalizeEnvironmentCreateServiceSession(ctx, runtime.PKIClient, cfg, p, store, runtime, s, operator); err != nil {
		return err
	}
	_, err := verifyProductFinalization(ctx, cfg, runtime, l, installed)
	return err
}

func enrollProductIdentityOutputs(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, service environmentCreateServiceCeremony, apply environmentCreateBootstrapApply) ([]environmentProductIdentityReceipt, error) {
	var receipts []environmentProductIdentityReceipt
	for _, product := range l.Outputs {
		role := strings.TrimPrefix(product.Subject, "service:")
		owner, err := productIdentityEnrollmentOwner(cfg, p, runtime, service, product)
		if err != nil {
			return nil, err
		}
		claim := environmentCreateServiceClaimDefinition(cfg, p.Stack+"-video-cloud", product.Claim)
		if err = apply(ctx, "enroll-"+role, []deploymentplan.Definition{claim, owner}); err != nil {
			return nil, err
		}
		original, journal, err := acceptedCreateStageForProduct(store, runtime.Execution, l, "enroll-"+role)
		if err != nil {
			return nil, err
		}
		ownerID := deploymentplan.ID(owner.Object)
		uid := journal.Owners[ownerID]
		if !environmentBootstrapOwnerUUID.MatchString(uid) {
			return nil, environmentCreateBootstrapBlocker("native product owner lacks its original accepted UID")
		}
		actual, err := runtime.Backend.Read(ctx, ownerID)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != uid || !deploymentplan.Matches(owner.Object, actual) {
			return nil, environmentCreateBootstrapBlocker("native product owner changed before its private state was read")
		}
		_ = original
		name := deploymentplan.String(deploymentplan.Map(owner.Object["metadata"])["name"])
		path := environmentCreateServiceOwnerIdentityPath(role, "client.json")
		proof, err := runtime.Backend.command(ctx, nil, "--namespace", p.Stack+"-video-cloud", "exec", "deployment/"+name, "--container", "enroll", "--", "/app/serviceidentity-bootstrap", "inspect", path, product.Subject, l.Root.RootFingerprint)
		if err != nil {
			return nil, environmentCreateBootstrapBlocker("native product owner has no complete original installed identity")
		}
		raw, err := runtime.Backend.command(ctx, nil, "--namespace", p.Stack+"-video-cloud", "exec", "deployment/"+name, "--container", "enroll", "--", "cat", path)
		if err != nil {
			return nil, environmentCreateBootstrapBlocker("original native product state is unavailable and cannot be regenerated")
		}
		rows, err := readProductIssuances(ctx, cfg, runtime, service, product.Subject)
		if err != nil || len(rows) != 1 {
			return nil, environmentCreateBootstrapBlocker("product enrollment has no unique genuine registered issuance")
		}
		material, err := productIdentityMaterial(raw, proof, product, service, rows[0], time.Now().UTC())
		if err != nil {
			return nil, err
		}
		if err = verifyProductLeafCRL(ctx, runtime.PKIClient, service.Intermediate, rows[0].Certificate); err != nil {
			return nil, err
		}
		identityProof := environmentProductIdentityProof{Subject: product.Subject, StateSHA256: environmentPreflightBytesSHA256(raw), OwnerUID: uid, Row: rows[0]}
		proofIntent := deploymentplan.Object{"product_sha256": deploymentplan.Digest(product), "owner_uid": uid, "state_sha256": identityProof.StateSHA256}
		var savedProof environmentProductIdentityProof
		found, err := readProductMutation(ctx, runtime.PKIClient, "observe-"+role, proofIntent, &savedProof, func(ctx context.Context) error {
			if deploymentplan.Digest(savedProof) != deploymentplan.Digest(identityProof) {
				return environmentCreateBootstrapBlocker("recorded observation differs from the freshly verified original owner and issuance")
			}
			return runtime.Execution.Check(ctx)
		})
		if err != nil {
			return nil, err
		}
		if found {
			if deploymentplan.Digest(savedProof) != deploymentplan.Digest(identityProof) {
				return nil, environmentCreateBootstrapBlocker("original native product issuance observation changed")
			}
		} else {
			if err = runtime.PKIClient.beginMutation("observe-"+role, proofIntent); err != nil {
				return nil, err
			}
			if err = runtime.PKIClient.completeMutation("observe-"+role, identityProof); err != nil {
				return nil, err
			}
		}
		if _, err = writeProductArtifact(store, productIdentityProofPath(l, role), identityProof); err != nil {
			return nil, err
		}
		parts := strings.Split(product.ID, "/")
		secret := environmentCreateSecret(parts[1], parts[2], material)
		secret.Object["immutable"] = true
		if err = apply(ctx, "publish-"+role, []deploymentplan.Definition{secret}); err != nil {
			return nil, err
		}
		actual, err = runtime.Backend.Read(ctx, product.ID)
		published, _, publishedErr := acceptedCreateStageForProduct(store, runtime.Execution, l, "publish-"+role)
		if err != nil || publishedErr != nil || len(published.Resources) != 1 || published.Resources[0].ID != product.ID || actual == nil || !deploymentplan.Matches(published.Resources[0].Desired, actual) {
			return nil, environmentCreateBootstrapBlocker("native product identity mirror has no exact private readback")
		}
		claimObject, err := runtime.Backend.Read(ctx, "PersistentVolumeClaim/"+p.Stack+"-video-cloud/"+product.Claim)
		if err != nil || claimObject == nil {
			return nil, environmentCreateBootstrapBlocker("original product identity claim cannot be read")
		}
		receipt := environmentProductIdentityReceipt{ResourceID: product.ID, Subject: product.Subject, SecretUID: deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]), ClaimUID: deploymentplan.String(deploymentplan.Map(claimObject["metadata"])["uid"]), CertificateSHA256: rows[0].Fingerprint}
		if !environmentBootstrapOwnerUUID.MatchString(receipt.SecretUID) || !environmentBootstrapOwnerUUID.MatchString(receipt.ClaimUID) {
			return nil, environmentCreateBootstrapBlocker("product mirror and original claim lack genuine UID receipts")
		}
		receipts = append(receipts, receipt)
		if err = approveProductWorkload(ctx, cfg, runtime, l, product, service); err != nil {
			return nil, err
		}
	}

	return receipts, runtime.Execution.Check(ctx)
}
