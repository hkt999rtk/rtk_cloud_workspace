package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type productSessionRenewalNative struct {
	productSessionInstallations
	Fingerprint  string     `json:"bootstrap_fingerprint"`
	Caller       string     `json:"caller"`
	DeploymentID string     `json:"deployment_id"`
	FinalizedAt  *time.Time `json:"finalized_at"`
}

func readProductSessionRenewalNative(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, sid string) (productSessionRenewalNative, error) {
	var value productSessionRenewalNative
	if !environmentBootstrapOwnerUUID.MatchString(sid) {
		return value, productSessionRenewalBlock("native session ID")
	}
	query := "SELECT json_build_object('session_id',session_id,'issuer_id',issuer_id,'root_sha256',root_sha256,'subjects',subjects,'installed',installed_subjects,'status',status,'expires_at',expires_at,'bootstrap_fingerprint',bootstrap_fingerprint,'finalized_at',finalized_at,'caller',caller,'deployment_id',deployment_id) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND session_id='" + sid + "'"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	if err != nil || decodeEnvironmentJSON(raw, &value) != nil {
		return value, productSessionRenewalBlock("native session readback")
	}
	return value, nil
}
func readProductSessionRenewalPolicy(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, issuer string) (environmentCreateOpenBaoIssuerPolicies, error) {
	var policy environmentCreateOpenBaoIssuerPolicies
	raw, err := runtime.Backend.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud", "exec", "deployment/pki-controller", "--container", "pki-controller", "--", "/app/pkicontroller", "render-openbao-policy", issuer)
	if err != nil || decodeEnvironmentJSON(raw, &policy) != nil || policy.IssuerID != issuer || policy.Environment != cfg.Environment || policy.ServiceClientSignerPolicy == "" {
		return policy, productSessionRenewalBlock("same active signer policy")
	}
	return policy, nil
}
func ensureProductSessionRenewal(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, c *compiledEnvironmentDeployment, l *createNormalTailProductLifecycle, apply environmentCreateBootstrapApply) (*productSessionRenewal, error) {
	if h == nil || c == nil || h.Execution == nil || l == nil {
		return nil, productSessionRenewalBlock("qualified runtime")
	}
	runtime, _, err := productIdentityRuntime(ctx, cfg, h, c, l)
	if err != nil {
		return nil, err
	}
	return executeProductSessionRenewal(ctx, cfg, c.Store, runtime, l, apply)
}

// This is the same production protocol with already-authenticated transport
// supplied explicitly so tests can exercise the native and Kubernetes seams.
func executeProductSessionRenewal(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, apply environmentCreateBootstrapApply) (*productSessionRenewal, error) {
	if runtime.Execution == nil {
		return nil, productSessionRenewalBlock("original runtime")
	}
	original, err := readProductInstalled(store, l)
	if err != nil {
		return nil, err
	}
	if err = verifyProductIdentityOutputs(ctx, cfg, runtime, l, original); err != nil {
		return nil, err
	}
	return executeProductSessionRenewalVerified(ctx, cfg, store, runtime, l, original, apply)
}
func executeProductSessionRenewalVerified(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, original *environmentProductIdentitySession, apply environmentCreateBootstrapApply) (renewed *productSessionRenewal, resultErr error) {
	finish := beginDeploymentMeasurement(ctx, "deployment.create.remaining.product-session-renewal")
	defer func() { finish(resultErr) }()
	e := runtime.Execution
	if e == nil || e.Store != store || l == nil || apply == nil || runtime.ProductIdentityScope != productIdentityScope(l) || runtime.PKIClient == nil || runtime.PKIClient.Execution != e {
		return nil, productSessionRenewalBlock("exact runtime")
	}
	if err := productSessionRenewalWindow(ctx, e); err != nil {
		return nil, err
	}
	if completed, err := readProductSessionRenewal(store, e, l); err != nil {
		return nil, err
	} else if completed != nil {
		native, nativeErr := readProductSessionRenewalNative(ctx, cfg, runtime, completed.Installed.Ceremony.BootstrapSessionID)
		if nativeErr != nil {
			return nil, nativeErr
		}
		if completed.Source.Generation == 0 && !native.Expires.After(time.Now().UTC()) && native.FinalizedAt == nil && len(native.Installed) < len(native.Subjects) {
			return executeProductSessionRenewalNext(ctx, cfg, store, runtime, l, completed, native, apply)
		}
		if err = verifyProductSessionRenewalCurrent(ctx, cfg, runtime, l, completed); err != nil {
			return nil, err
		}
		return completed, nil
	}
	if original == nil {
		return nil, productSessionRenewalBlock("verified original products")
	}
	source, err := readProductSessionRenewalSource(store, e, l)
	if err != nil {
		return nil, err
	}
	if source == nil {
		if er := productSessionRenewalUnstartedSource(store, e, l, original); er != nil {
			return nil, er
		}
		native, er := readProductSessionRenewalNative(ctx, cfg, runtime, original.Ceremony.BootstrapSessionID)
		if er != nil {
			return nil, er
		}
		if native.Expires.After(time.Now().UTC()) {
			return nil, nil
		}
		made, er := qualifyProductSessionRenewalSource(ctx, cfg, store, runtime, l, original, native)
		if er != nil {
			return nil, er
		}
		if err = productSessionRenewalWrite(store, l, "source.json", made); err != nil {
			return nil, err
		}
		source = &made
	}
	if err = validateProductSessionRenewalSource(store, e, l, *source); err != nil {
		return nil, err
	}
	return executeProductSessionRenewalPrepared(ctx, cfg, store, runtime, l, original, source, apply)
}
func executeProductSessionRenewalPrepared(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, original *environmentProductIdentitySession, source *productSessionRenewalSource, apply environmentCreateBootstrapApply) (*productSessionRenewal, error) {
	e := runtime.Execution
	var err error
	// A new native session may monotonically expire the old row. Its exact
	// original expiry, fingerprint and ACK subset remain immutable evidence.
	oldNative, err := readProductSessionRenewalNative(ctx, cfg, runtime, source.OriginalSession.Session)
	if err != nil {
		return nil, err
	}
	if !productSessionRenewalOldNativeMatches(*source, oldNative) {
		return nil, productSessionRenewalBlock("original native expiry or ACK history changed")
	}
	policy, err := readProductSessionRenewalPolicy(ctx, cfg, runtime, original.Ceremony.Intermediate.ID)
	if err != nil || environmentPreflightBytesSHA256([]byte(policy.ServiceClientSignerPolicy)) != source.SignerPolicySHA256 {
		return nil, productSessionRenewalBlock("original signer policy changed")
	}
	if err = productSessionRenewalWindow(ctx, e); err != nil {
		return nil, err
	}
	name := "rtk-" + cfg.Environment + "-service-bootstrap-" + source.SessionID
	if err = installEnvironmentCreateOpenBaoPolicy(ctx, cfg, runtime, name, policy.ServiceClientSignerPolicy); err != nil {
		return nil, err
	}
	account := "pki-product-bootstrap-" + productIdentityScope(l)[:12]
	if err = configureEnvironmentCreateOpenBaoRole(ctx, cfg, runtime, account, []string{account}, []string{name}); err != nil {
		return nil, err
	}
	job, err := productSessionRenewalSourceJob(store, e, l, *source)
	if err != nil {
		return nil, err
	}
	definitions, err := productSessionRenewalJobDefinitions(cfg, l, *source, job)
	if err != nil {
		return nil, err
	}
	applyStage := func(suffix string, defs []deploymentplan.Definition) error {
		if er := productSessionRenewalWindow(ctx, e); er != nil {
			return er
		}
		full := productSessionRenewalStage(l, *source, suffix)
		if er := checkProductSessionRenewalWrite(ctx, store, runtime, l, *source, suffix, defs); er != nil {
			return er
		}
		return apply(ctx, strings.TrimPrefix(full, "product-identities-"+productIdentityScope(l)[:12]+"-"), defs)
	}
	if err = applyStage("job", definitions); err != nil {
		return nil, err
	}
	definitions, err = productSessionRenewalCustodyDefinitions(cfg, l, *source, original.Ceremony)
	if err != nil {
		return nil, err
	}
	if err = applyStage("custody", definitions); err != nil {
		return nil, err
	}
	material, leaf, err := readProductSessionRenewalMaterial(ctx, cfg, runtime, l, *source, original.Ceremony)
	if err != nil {
		return nil, err
	}
	next := *original
	next.EnrollmentSessionID = original.EnrollmentSessionID
	if next.EnrollmentSessionID == "" {
		next.EnrollmentSessionID = original.Ceremony.BootstrapSessionID
	}
	next.Ceremony = original.Ceremony
	next.Ceremony.BootstrapSessionID = source.SessionID
	next.Ceremony.BootstrapCredentialSecret = "service-bootstrap-" + source.SessionID
	next.Ceremony.CredentialUIDs = map[string]string{}
	definitions, err = productSessionRenewalCredentialDefinitions(l, *source, material)
	if err != nil {
		return nil, err
	}
	if err = applyStage("credentials", definitions); err != nil {
		return nil, err
	}
	credential, cj, err := acceptedCreateStage(store, e, "kubernetes/"+productSessionRenewalStage(l, *source, "credentials"))
	if err != nil || len(credential.Resources) != 1 {
		return nil, productSessionRenewalBlock("new credential accepted owner")
	}
	ns := next.Ceremony.BootstrapCredentialNamespace
	next.Ceremony.CredentialUIDs[ns] = cj.Owners[credential.Resources[0].ID]
	var frozen struct {
		Clusters []struct {
			Cluster map[string]any `yaml:"cluster"`
		} `yaml:"clusters"`
	}
	if yaml.Unmarshal(runtime.Backend.KubeconfigContent, &frozen) != nil || len(frozen.Clusters) != 1 {
		return nil, productSessionRenewalBlock("original Kubernetes CA")
	}
	binding := original.Ceremony.CleanupBinding
	binding.SessionID = source.SessionID
	binding.SecretUID = next.Ceremony.CredentialUIDs[ns]
	binding.PolicySHA256 = source.SignerPolicySHA256
	binding.ConfigMapName = "pki-bootstrap-cleanup-binding-" + source.SessionID[:12]
	if binding.KubernetesCA != deploymentplan.String(frozen.Clusters[0].Cluster["certificate-authority-data"]) {
		return nil, productSessionRenewalBlock("original cleanup Kubernetes CA")
	}
	next.Ceremony.CleanupBinding = binding
	observed := productSessionRenewalObserved{SourceSHA256: deploymentplan.Digest(*source), Installed: next, CertificateSHA256: environmentPreflightBytesSHA256(leaf.Raw)}
	if err = productSessionRenewalWrite(store, l, "observed.json", observed, source.Generation); err != nil {
		return nil, err
	}
	cleanupText := fmt.Sprintf("path %q {\n  capabilities = [\"read\", \"delete\"]\n}\n", "sys/policies/acl/"+name)
	if err = installEnvironmentCreateOpenBaoPolicy(ctx, cfg, runtime, "create-"+source.SessionID+"-cleanup-controller", cleanupText); err != nil {
		return nil, err
	}
	if err = configureEnvironmentCreateOpenBaoRole(ctx, cfg, runtime, "pki-controller-"+cfg.Environment, []string{"pki-controller"}, environmentCreateControllerPolicies(runtime)); err != nil {
		return nil, err
	}
	result := &productSessionRenewal{Schema: productSessionRenewalSchema, ScopeSHA256: productIdentityScope(l), SourceSHA256: deploymentplan.Digest(*source), Source: *source, Original: *original, Installed: next, Stages: map[string]productSessionRenewalStageReceipt{}}
	definitions, err = productSessionRenewalControlDefinitions(cfg, l, result)
	if err != nil {
		return nil, err
	}
	if err = applyStage("controls", definitions); err != nil {
		return nil, err
	}
	for _, suffix := range []string{"job", "custody", "credentials", "controls"} {
		p, j, er := acceptedCreateStage(store, e, "kubernetes/"+productSessionRenewalStage(l, *source, suffix))
		if er != nil {
			return nil, er
		}
		result.Stages[suffix] = productSessionRenewalStageReceipt{PlanSHA256: p.SHA256(), JournalSHA256: deploymentplan.Digest(j)}
	}
	if _, err = productSessionRenewalDependencyResources(cfg, store, e, l, result); err != nil {
		return nil, err
	}
	if err = verifyProductIdentityOutputs(ctx, cfg, runtime, l, &next); err != nil {
		return nil, err
	}
	if err = productSessionRenewalWrite(store, l, "completed.json", result, source.Generation); err != nil {
		return nil, err
	}
	return result, productSessionRenewalWindow(ctx, e)
}
func productSessionRenewalOldNativeMatches(s productSessionRenewalSource, n productSessionRenewalNative) bool {
	old := s.OriginalSession
	return n.Caller == "service:deployment-bootstrap" && n.DeploymentID == s.OriginalDeploymentID && n.Session == old.Session && n.Issuer == old.Issuer && n.Root == old.Root && n.Expires.Equal(old.Expires) && n.Fingerprint == s.OriginalFingerprint && slices.Equal(n.Subjects, old.Subjects) && slices.Equal(n.Installed, old.Installed) && (n.Status == old.Status || old.Status == "active" && n.Status == "expired")
}
func qualifyProductSessionRenewalSource(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, original *environmentProductIdentitySession, native productSessionRenewalNative) (productSessionRenewalSource, error) {
	var out productSessionRenewalSource
	e := runtime.Execution
	if original == nil || native.Session != original.Ceremony.BootstrapSessionID || native.Expires.IsZero() || native.Expires.After(time.Now().UTC()) || native.Status != "active" && native.Status != "expired" {
		return out, productSessionRenewalBlock("expired original session")
	}
	p, err := readCreateNormalTailFrozenTarget(store, e)
	if err != nil {
		return out, err
	}
	op, oj, err := readCreateNormalTailWorkloadOriginal(store, e, p)
	if err != nil {
		return out, err
	}
	f, err := readCreateNormalTailWorkloadForwardDescriptor(store, e, op, oj)
	if err != nil || f == nil {
		return out, productSessionRenewalBlock("started forward source")
	}
	fp, fj, err := readCreateNormalTailStage(store, e, f.ForwardStage, f.Target.Plan)
	if err != nil || fp == nil || fj.PlanSHA256 != fp.SHA256() || fj.Accepted || fj.AcceptanceStarted || fj.InFlight != "" || len(fj.Completed) != 0 || len(fj.Owners) != 0 || fj.Failed != fp.Resources[0].ID {
		return out, productSessionRenewalBlock("known zero-write forward failure")
	}
	raw, err := readBoundedEnvironmentFile(createNormalTailWorkloadForwardPath(e), true)
	if err != nil {
		return out, err
	}
	program, err := readCreateNormalTailProgram(store, e)
	if err != nil {
		return out, err
	}
	cp, cj, err := acceptedCreateStageForProduct(store, e, l, "service-credential-secrets")
	if err != nil {
		return out, err
	}
	custody, j, err := acceptedCreateStageForProduct(store, e, l, "service-custody-reader")
	if err != nil || len(custody.Resources) != 1 {
		return out, productSessionRenewalBlock("original custody")
	}
	bp, err := readCreateNormalTailPreparationPlan(store, e, "product-identities-"+productIdentityScope(l)[:12]+"-service-session")
	if err != nil || bp == nil || len(bp.Resources) != 3 {
		return out, productSessionRenewalBlock("original bootstrap source")
	}
	bj, exists, err := bp.ReadExecution(store.Root)
	if err != nil || !exists {
		return out, productSessionRenewalBlock("bootstrap original owners")
	}
	for _, r := range []deploymentplan.Resource{bp.Resources[0], bp.Resources[1], custody.Resources[0]} {
		uid := bj.Owners[r.ID]
		if r.ID == custody.Resources[0].ID {
			uid = j.Owners[r.ID]
		}
		live, er := runtime.Backend.Read(ctx, r.ID)
		if er != nil || live == nil || deploymentplan.Map(live["metadata"])["uid"] != uid || !deploymentplan.Matches(r.Desired, live) {
			return out, productSessionRenewalBlock("original native claim, account or custody owner")
		}
	}
	if len(cp.Resources) != 1 {
		return out, productSessionRenewalBlock("original credential count")
	}
	secret, err := runtime.Backend.Read(ctx, cp.Resources[0].ID)
	if err != nil || secret == nil || deploymentplan.Map(secret["metadata"])["uid"] != cj.Owners[cp.Resources[0].ID] || !deploymentplan.Matches(cp.Resources[0].Desired, secret) {
		return out, productSessionRenewalBlock("original credential owner")
	}
	material := map[string][]byte{}
	for file, key := range map[string]string{"bootstrap.key": "tls.key", "bootstrap.crt": "tls.crt"} {
		v, er := base64.StdEncoding.DecodeString(deploymentplan.String(deploymentplan.Map(secret["data"])[key]))
		if er != nil {
			return out, productSessionRenewalBlock("original credential bytes")
		}
		material[file] = v
	}
	pair, err := tls.X509KeyPair(material["bootstrap.crt"], material["bootstrap.key"])
	if err != nil || !managedLateBootstrapChainMatches(pair.Certificate, original.Ceremony) {
		return out, productSessionRenewalBlock("original bootstrap chain")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.Subject.CommonName != "service:deployment-bootstrap" || leaf.NotAfter.After(time.Now().UTC()) || !leaf.NotAfter.Equal(native.Expires) || environmentPreflightBytesSHA256(leaf.Raw) != native.Fingerprint {
		return out, productSessionRenewalBlock("expired credential native lineage")
	}
	ns := original.Ceremony.BootstrapCredentialNamespace
	for _, file := range []string{"bootstrap.key", "bootstrap.csr", "bootstrap.crt"} {
		v, er := runtime.Backend.command(ctx, nil, "--namespace", ns, "exec", "deployment/"+original.Ceremony.CustodyReader, "--container", "custody", "--", "cat", "/run/pki-bootstrap/"+file)
		if er != nil || len(v) == 0 || len(v) > 256<<10 {
			return out, productSessionRenewalBlock("original private custody readback")
		}
		if before := material[file]; before != nil && !bytes.Equal(before, v) {
			return out, productSessionRenewalBlock("custody differs from original Secret")
		}
		material[file] = v
	}
	block, rest := pem.Decode(material["bootstrap.csr"])
	if block == nil || block.Type != "CERTIFICATE REQUEST" || len(bytes.TrimSpace(rest)) != 0 {
		return out, productSessionRenewalBlock("original CSR")
	}
	csr, err := x509.ParseCertificateRequest(block.Bytes)
	if err != nil || csr.CheckSignature() != nil || !bytes.Equal(csr.RawSubjectPublicKeyInfo, leaf.RawSubjectPublicKeyInfo) {
		return out, productSessionRenewalBlock("original CSR key")
	}
	modes, err := runtime.Backend.command(ctx, nil, "--namespace", ns, "exec", "deployment/"+original.Ceremony.CustodyReader, "--container", "custody", "--", "/bin/sh", "-ec", "test ! -L /run/pki-bootstrap; for f in bootstrap.key bootstrap.csr; do test ! -L /run/pki-bootstrap/$f; stat -c '%a %u %g' /run/pki-bootstrap/$f; done")
	if err != nil || string(bytes.TrimSpace(modes)) != "600 10001 10001\n600 10001 10001" {
		return out, productSessionRenewalBlock("private custody permissions")
	}
	trustID, _, _, err := productIdentityOpenBaoTrustPins(l)
	if err != nil {
		return out, err
	}
	trust, err := runtime.Backend.Read(ctx, trustID)
	if err != nil || validateProductIdentityOpenBaoTrust(l, trust, time.Now().UTC()) != nil {
		return out, productSessionRenewalBlock("managed current OpenBao trust")
	}
	policy, err := readProductSessionRenewalPolicy(ctx, cfg, runtime, original.Ceremony.Intermediate.ID)
	if err != nil {
		return out, err
	}
	token, err := readBoundedEnvironmentFile(runtime.OpenBao.RootTokenPath, true)
	if err != nil {
		return out, productSessionRenewalBlock("original signer policy owner")
	}
	oldPolicy := "rtk-" + cfg.Environment + "-service-bootstrap-" + original.Ceremony.BootstrapSessionID
	actualPolicy, err := environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, append(bytes.TrimSpace(token), '\n'), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao policy read "+oldPolicy+"\n")
	if err != nil || strings.TrimSpace(string(actualPolicy)) != strings.TrimSpace(policy.ServiceClientSignerPolicy) {
		return out, productSessionRenewalBlock("original signer policy native body")
	}
	account := "pki-product-bootstrap-" + productIdentityScope(l)[:12]
	priorPolicies, er := readProductRolePolicies(ctx, cfg, runtime, account, account)
	if er != nil || !slices.Equal(priorPolicies, []string{"rtk-" + cfg.Environment + "-service-bootstrap-" + original.Ceremony.BootstrapSessionID}) {
		return out, productSessionRenewalBlock("original bootstrap auth role")
	}
	controllerPolicies, er := readProductRolePolicies(ctx, cfg, runtime, "pki-controller-"+cfg.Environment, "pki-controller")
	if er != nil || !slices.Equal(controllerPolicies, environmentCreateControllerPolicies(runtime)) {
		return out, productSessionRenewalBlock("original controller auth role")
	}

	out = productSessionRenewalSource{CertificateDirectoryProfile: productSessionRenewalSGIDDirectoryProfile, Schema: productSessionRenewalSchema, ParentSHA256: e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(e.owner), ScopeSHA256: productIdentityScope(l), ProgramSHA256: deploymentplan.Digest(program), OriginalInstalledSHA256: deploymentplan.Digest(original), OriginalCredentialPlanSHA256: cp.SHA256(), OriginalCredentialJournalSHA256: deploymentplan.Digest(cj), OriginalBootstrapPlanSHA256: bp.SHA256(), CustodyPlanSHA256: custody.SHA256(), CustodyJournalSHA256: deploymentplan.Digest(j), ClaimUID: bj.Owners[bp.Resources[0].ID], ServiceAccountUID: bj.Owners[bp.Resources[1].ID], CustodyUID: j.Owners[custody.Resources[0].ID], OriginalForwardDescriptorSHA256: environmentPreflightBytesSHA256(raw), OriginalForwardPlanSHA256: fp.SHA256(), OriginalForwardJournalSHA256: deploymentplan.Digest(fj), OriginalCertificateSHA256: environmentPreflightBytesSHA256(material["bootstrap.crt"]), OriginalFingerprint: native.Fingerprint, KeySHA256: environmentPreflightBytesSHA256(material["bootstrap.key"]), CSRSHA256: environmentPreflightBytesSHA256(material["bootstrap.csr"]), SignerPolicySHA256: environmentPreflightBytesSHA256([]byte(policy.ServiceClientSignerPolicy)), OriginalSession: native.productSessionInstallations, OriginalDeploymentID: native.DeploymentID, QualifiedAt: time.Now().UTC()}
	out.ControlOwners = map[string]string{}
	originalControls, originalControlJournal, er := acceptedCreateStageForProduct(store, e, l, "session-controls")
	if er != nil {
		return out, er
	}
	for _, resource := range originalControls.Resources {
		if resource.Desired["kind"] == "Role" || resource.Desired["kind"] == "RoleBinding" || resource.Desired["kind"] == "ClusterRole" || resource.Desired["kind"] == "ClusterRoleBinding" {
			out.ControlOwners[resource.ID] = originalControlJournal.Owners[resource.ID]
		}
	}
	out.SessionID, out.DeploymentID, out.CertificatePath = productSessionRenewalIdentity(out.ParentSHA256, out.ScopeSHA256, original.Ceremony.BootstrapSessionID)
	query := "SELECT json_build_object('count',count(*)) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND (session_id='" + out.SessionID + "' OR deployment_id='" + out.DeploymentID + "')"
	rawNative, readErr := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var absent struct {
		Count int `json:"count"`
	}
	if readErr != nil || decodeEnvironmentJSON(rawNative, &absent) != nil || absent.Count != 0 {
		return out, productSessionRenewalBlock("unrecorded renewal session or deployment exists")
	}

	for _, r := range fp.Resources {
		if r.ID == "Deployment/"+fp.Stack+"-video-cloud/pki-controller" || r.ID == "Deployment/"+fp.Stack+"-video-cloud/certissuer" {
			o := deploymentplan.Clone(r.Desired)
			deploymentplan.Map(o["metadata"])["uid"] = r.UID
			out.ControlSources = append(out.ControlSources, deploymentplan.Definition{Phase: r.Phase, Object: o})
		}
	}
	if err = validateProductSessionRenewalSource(store, e, l, out); err != nil {
		return out, err
	}
	return out, productSessionRenewalWindow(ctx, e)
}
func readProductSessionRenewalMaterial(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, s productSessionRenewalSource, old environmentCreateServiceCeremony) (map[string]string, *x509.Certificate, error) {
	material := map[string]string{}
	for name, path := range map[string]string{"tls.key": "/run/pki-bootstrap/bootstrap.key", "csr": "/run/pki-bootstrap/bootstrap.csr", "tls.crt": s.CertificatePath} {
		v, err := runtime.Backend.command(ctx, nil, "--namespace", old.BootstrapCredentialNamespace, "exec", "deployment/"+old.CustodyReader, "--container", "custody", "--", "cat", path)
		if err != nil || len(v) == 0 || len(v) > 256<<10 {
			return nil, nil, productSessionRenewalBlock("renewed private material readback")
		}
		material[name] = string(v)
	}
	if environmentPreflightBytesSHA256([]byte(material["tls.key"])) != s.KeySHA256 || environmentPreflightBytesSHA256([]byte(material["csr"])) != s.CSRSHA256 {
		return nil, nil, productSessionRenewalBlock("renewal replaced original key or CSR")
	}
	pair, err := tls.X509KeyPair([]byte(material["tls.crt"]), []byte(material["tls.key"]))
	if err != nil || !managedLateBootstrapChainMatches(pair.Certificate, old) {
		return nil, nil, productSessionRenewalBlock("renewed lineage")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.Subject.CommonName != "service:deployment-bootstrap" || !time.Now().UTC().Before(leaf.NotAfter) || time.Now().UTC().Before(leaf.NotBefore) {
		return nil, nil, productSessionRenewalBlock("new bootstrap lifetime")
	}
	native, err := readProductSessionRenewalNative(ctx, cfg, runtime, s.SessionID)
	if err != nil || native.Session != s.SessionID || native.Issuer != old.Intermediate.ID || native.Root != old.Root.RootFingerprint || native.Status != "active" || native.Caller != "service:deployment-bootstrap" || native.DeploymentID != s.DeploymentID || native.Fingerprint != environmentPreflightBytesSHA256(leaf.Raw) || !native.Expires.Equal(leaf.NotAfter) || !slices.Equal(native.Subjects, s.OriginalSession.Subjects) {
		return nil, nil, productSessionRenewalBlock("new exact native session")
	}
	for _, v := range native.Installed {
		if !slices.Contains(native.Subjects, v) {
			return nil, nil, productSessionRenewalBlock("new ACK subject")
		}
	}
	delete(material, "csr")
	material["ca.crt"] = old.Root.Certificate
	return material, leaf, runtime.Execution.Check(ctx)
}

func verifyProductSessionRenewalCurrent(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, r *productSessionRenewal) error {
	n, err := readProductSessionRenewalNative(ctx, cfg, runtime, r.Source.SessionID)
	if err != nil {
		return err
	}
	observed, err := readProductSessionRenewalObserved(runtime.Execution.Store, runtime.Execution, l, r.Source)
	if err != nil || observed == nil {
		return productSessionRenewalBlock("renewed observed proof")
	}
	if n.Caller != "service:deployment-bootstrap" || n.DeploymentID != r.Source.DeploymentID || n.Session != r.Source.SessionID || n.Issuer != r.Installed.Ceremony.Intermediate.ID || n.Root != r.Installed.Ceremony.Root.RootFingerprint || n.Fingerprint != observed.CertificateSHA256 || !slices.Equal(n.Subjects, r.Source.OriginalSession.Subjects) {
		return productSessionRenewalBlock("renewed native session drift")
	}
	allowed := map[string]bool{}
	for _, v := range n.Installed {
		if allowed[v] || !slices.Contains(n.Subjects, v) {
			return productSessionRenewalBlock("renewed ACK roster")
		}
		allowed[v] = true
	}
	if n.Status == "sealed" && len(allowed) != len(n.Subjects) {
		return productSessionRenewalBlock("renewed sealed ACK roster")
	}
	if n.FinalizedAt != nil && !n.FinalizedAt.IsZero() {
		if len(allowed) != len(n.Subjects) || n.Status != "sealed" && n.Status != "expired" {
			return productSessionRenewalBlock("renewed native finalization progress")
		}
		return runtime.Execution.Check(ctx)
	}
	if len(allowed) == len(n.Subjects) && (n.Status == "sealed" || n.Status == "expired") {
		return runtime.Execution.Check(ctx)
	}
	if !n.Expires.After(time.Now().UTC()) || n.Status != "active" && n.Status != "sealed" {
		return productSessionRenewalBlock("renewed session expired before remaining workloads completed")
	}
	secret, err := runtime.Backend.Read(ctx, "Secret/"+r.Installed.Ceremony.BootstrapCredentialNamespace+"/"+r.Installed.Ceremony.BootstrapCredentialSecret)
	if err != nil || secret == nil || deploymentplan.Map(secret["metadata"])["uid"] != r.Installed.Ceremony.CredentialUIDs[r.Installed.Ceremony.BootstrapCredentialNamespace] {
		return productSessionRenewalBlock("renewed credential owner")
	}
	credential, _, err := acceptedCreateStage(runtime.Execution.Store, runtime.Execution, "kubernetes/"+productSessionRenewalStage(l, r.Source, "credentials"))
	if err != nil || len(credential.Resources) != 1 || secret["immutable"] != true || !deploymentplan.Matches(credential.Resources[0].Desired, secret) {
		return productSessionRenewalBlock("renewed credential native body changed")
	}
	return runtime.Execution.Check(ctx)
}

func productSessionRenewalUnstartedSource(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, original *environmentProductIdentitySession) error {
	sid, _, _ := productSessionRenewalIdentity(e.Journal.PlanSHA256, productIdentityScope(l), original.Ceremony.BootstrapSessionID)
	for _, entry := range e.Journal.Entries {
		if entry.Key == "openbao-policy-rtk-"+l.Bootstrap.Environment+"-service-bootstrap-"+sid || entry.Key == "openbao-policy-create-"+sid+"-cleanup-controller" || strings.HasPrefix(entry.Key, "kubernetes/product-identities-"+productIdentityScope(l)[:12]+"-session-renewal-") {
			return productSessionRenewalBlock("renewal request lost its original source")
		}
	}
	path, err := store.safePath(productSessionRenewalBase(l))
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return productSessionRenewalBlock("renewal artifacts lost their original source")
	}
	return nil
}
