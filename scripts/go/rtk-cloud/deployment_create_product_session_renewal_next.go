package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Only the reviewed second generation is supported. The first files and every
// historical child remain literal; a later clock never mutates those proofs.
func productSessionRenewalSelectedBase(l *createNormalTailProductLifecycle, generations ...int) string {
	if len(generations) == 1 && generations[0] == 2 {
		return filepath.Join(filepath.Dir(productSessionForwardPath(l)), "session-renewal-0002")
	}
	return productSessionRenewalBase(l)
}
func readProductSessionRenewalFirst(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle) (*productSessionRenewal, error) {
	return readProductSessionRenewalAt(store, e, l, 0)
}
func readProductSessionRenewal(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle) (*productSessionRenewal, error) {
	r, _, err := readProductSessionRenewalWithDependencies(store, e, l, nil)
	return r, err
}

func readProductSessionRenewalWithDependencies(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, cfg *deploymentConfig) (*productSessionRenewal, []deploymentplan.Resource, error) {
	first, firstDeps, err := readProductSessionRenewalAtWithDependencies(store, e, l, 0, cfg)
	if err != nil {
		return nil, nil, err
	}
	next, nextDeps, err := readProductSessionRenewalAtWithDependencies(store, e, l, 2, cfg)
	if err != nil {
		return nil, nil, err
	}
	if next != nil {
		return next, nextDeps, nil
	}
	return first, firstDeps, nil
}
func readProductSessionRenewalSource(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle) (*productSessionRenewalSource, error) {
	next, err := readProductSessionRenewalSourceForGeneration(store, e, l, 2)
	if err != nil || next != nil {
		return next, err
	}
	return readProductSessionRenewalSourceForGeneration(store, e, l, 0)
}
func productSessionRenewalSourceInstalled(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, s productSessionRenewalSource) (*environmentProductIdentitySession, error) {
	if s.Generation == 0 {
		return readProductInstalled(store, l)
	}
	if s.Generation != 2 {
		return nil, productSessionRenewalBlock("unsupported generation")
	}
	first, err := readProductSessionRenewalFirst(store, e, l)
	if err != nil || first == nil {
		return nil, productSessionRenewalBlock("first completed generation")
	}
	return &first.Installed, nil
}
func productSessionRenewalSourceJob(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, s productSessionRenewalSource) (deploymentplan.Object, error) {
	if s.Generation == 2 {
		first, err := readProductSessionRenewalFirst(store, e, l)
		if err != nil || first == nil {
			return nil, productSessionRenewalBlock("previous accepted Job source")
		}
		original, err := productSessionRenewalSourceJob(store, e, l, first.Source)
		if err != nil {
			return nil, err
		}
		o := deploymentplan.Clone(original)
		c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(o)["containers"])[0])
		found := 0
		for _, raw := range certIssuerObjectList(c["env"]) {
			v := deploymentplan.Map(raw)
			if v["name"] == "PKI_BOOTSTRAP_SESSION_ID" {
				if v["value"] != first.Source.OriginalSession.Session || v["valueFrom"] != nil {
					return nil, productSessionRenewalBlock("immutable original Job session slot")
				}
				v["value"] = s.OriginalSession.Session
				found++
			}
		}
		if found != 1 {
			return nil, productSessionRenewalBlock("unique original Job session slot")
		}
		return o, nil
	}
	p, err := readCreateNormalTailPreparationPlan(store, e, "product-identities-"+productIdentityScope(l)[:12]+"-service-session")
	if err != nil || p == nil || len(p.Resources) != 3 {
		return nil, productSessionRenewalBlock("original Job")
	}
	return p.Resources[2].Desired, nil
}
func productSessionRenewalDependencyUnion(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, r *productSessionRenewal, current []deploymentplan.Resource) ([]deploymentplan.Resource, error) {
	if r.Source.Generation == 0 {
		return current, nil
	}
	first, err := readProductSessionRenewalFirst(store, e, l)
	if err != nil || first == nil {
		return nil, productSessionRenewalBlock("previous dependency proof")
	}
	old, err := productSessionRenewalDependencyResources(cfg, store, e, l, first)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, v := range current {
		seen[v.ID] = true
	}
	var result []deploymentplan.Resource
	for _, v := range old {
		if !seen[v.ID] {
			result = append(result, v)
		}
	}
	return append(result, current...), nil
}
func productSessionRenewalNextRuntime(store secretStore, e *environmentCreateExecution, s productSessionRenewalSource) (createNormalTailRuntimeStep, error) {
	chain, err := readCreateNormalTailRuntimeChain(store, e, s.RuntimeSequence)
	if err != nil || len(chain) == 0 {
		return createNormalTailRuntimeStep{}, productSessionRenewalBlock("stopped runtime history")
	}
	last := chain[len(chain)-1]
	if createNormalTailRuntimeSequence(last.Descriptor) != s.RuntimeSequence || last.Child == nil || !createNormalTailRuntimePartial(last.Child, last.Journal) || deploymentplan.Digest(last.Descriptor) != s.OriginalForwardDescriptorSHA256 || last.Child.SHA256() != s.OriginalForwardPlanSHA256 || deploymentplan.Digest(last.Journal) != s.OriginalForwardJournalSHA256 {
		return createNormalTailRuntimeStep{}, productSessionRenewalBlock("literal stopped runtime source")
	}
	return last, nil
}
func validateProductSessionRenewalNextSource(store secretStore, e *environmentCreateExecution, l *createNormalTailProductLifecycle, s productSessionRenewalSource) error {
	if e == nil || l == nil || e.Store != store || s.Generation != 2 || s.RuntimeSequence != 3 || s.Schema != productSessionRenewalSchema || s.ParentSHA256 != e.Journal.PlanSHA256 || s.OwnerSHA256 != deploymentplan.Digest(e.owner) || s.ScopeSHA256 != productIdentityScope(l) {
		return productSessionRenewalBlock("next generation scope")
	}
	previous, err := readProductSessionRenewalFirst(store, e, l)
	if err != nil || previous == nil || s.PreviousRenewalSHA256 != deploymentplan.Digest(previous) {
		return productSessionRenewalBlock("immutable previous renewal")
	}
	old := previous.Installed
	program, err := readCreateNormalTailProgram(store, e)
	if err != nil {
		return err
	}
	sid, did, path := productSessionRenewalIdentity(s.ParentSHA256, s.ScopeSHA256, old.Ceremony.BootstrapSessionID)
	if s.ProgramSHA256 != deploymentplan.Digest(program) || s.OriginalInstalledSHA256 != deploymentplan.Digest(old) || s.SessionID != sid || s.DeploymentID != did || s.CertificatePath != path || s.OriginalDeploymentID != previous.Source.DeploymentID || s.QualifiedAt.IsZero() || s.QualifiedAt.After(time.Now().UTC()) || s.OriginalSession.Expires.IsZero() || s.OriginalSession.Expires.After(s.QualifiedAt) || s.OriginalSession.Session != old.Ceremony.BootstrapSessionID || s.OriginalSession.Issuer != old.Ceremony.Intermediate.ID || s.OriginalSession.Root != old.Ceremony.Root.RootFingerprint || s.OriginalSession.Status != "active" && s.OriginalSession.Status != "expired" {
		return productSessionRenewalBlock("next expired session identity")
	}
	if s.KeySHA256 != previous.Source.KeySHA256 || s.CSRSHA256 != previous.Source.CSRSHA256 || s.SignerPolicySHA256 != previous.Source.SignerPolicySHA256 || s.ClaimUID != previous.Source.ClaimUID || s.ServiceAccountUID != previous.Source.ServiceAccountUID || s.CustodyUID != previous.Source.CustodyUID || s.OriginalBootstrapPlanSHA256 != previous.Source.OriginalBootstrapPlanSHA256 || !slices.Equal(s.OriginalSession.Subjects, previous.Source.OriginalSession.Subjects) || len(s.OriginalSession.Installed) >= len(s.OriginalSession.Subjects) {
		return productSessionRenewalBlock("same key CSR claim and incomplete roster")
	}
	seen := map[string]bool{}
	for _, v := range s.OriginalSession.Installed {
		if seen[v] || !slices.Contains(s.OriginalSession.Subjects, v) {
			return productSessionRenewalBlock("next original ACK subset")
		}
		seen[v] = true
	}
	cp, cj, err := acceptedCreateStage(store, e, "kubernetes/"+productSessionRenewalStage(l, previous.Source, "credentials"))
	if err != nil || len(cp.Resources) != 1 || cp.SHA256() != s.OriginalCredentialPlanSHA256 || deploymentplan.Digest(cj) != s.OriginalCredentialJournalSHA256 || cj.Owners[cp.Resources[0].ID] != old.Ceremony.CredentialUIDs[old.Ceremony.BootstrapCredentialNamespace] {
		return productSessionRenewalBlock("previous accepted credential")
	}
	data := deploymentplan.Map(cp.Resources[0].Desired["data"])
	cert, err := base64.StdEncoding.DecodeString(deploymentplan.String(data["tls.crt"]))
	leaf, le := parseFirstCertificatePEM(cert)
	if err != nil || le != nil || environmentPreflightBytesSHA256(cert) != s.OriginalCertificateSHA256 || environmentPreflightBytesSHA256(leaf.Raw) != s.OriginalFingerprint || !leaf.NotAfter.Equal(s.OriginalSession.Expires) {
		return productSessionRenewalBlock("previous expired public leaf")
	}
	custody, cj, err := acceptedCreateStage(store, e, "kubernetes/"+productSessionRenewalStage(l, previous.Source, "custody"))
	if err != nil || len(custody.Resources) != 1 || custody.SHA256() != s.CustodyPlanSHA256 || deploymentplan.Digest(cj) != s.CustodyJournalSHA256 || cj.Owners[custody.Resources[0].ID] != s.CustodyUID {
		return productSessionRenewalBlock("previous accepted custody")
	}
	last, err := productSessionRenewalNextRuntime(store, e, s)
	if err != nil {
		return err
	}
	wanted := map[string]deploymentplan.Object{}
	for _, v := range last.Child.Resources {
		if v.ID == "Deployment/"+l.Bootstrap.Stack+"-video-cloud/pki-controller" || v.ID == "Deployment/"+l.Bootstrap.Stack+"-video-cloud/certissuer" {
			o := deploymentplan.Clone(v.Desired)
			uid := last.Journal.Owners[v.ID]
			if uid == "" {
				uid = v.UID
			}
			if !environmentBootstrapOwnerUUID.MatchString(uid) {
				return productSessionRenewalBlock("runtime control owner")
			}
			deploymentplan.Map(o["metadata"])["uid"] = uid
			wanted[v.ID] = o
		}
	}
	if len(s.ControlSources) != 2 || len(wanted) != 2 || deploymentplan.Digest(s.ControlOwners) != deploymentplan.Digest(previous.Source.ControlOwners) {
		return productSessionRenewalBlock("next control sources")
	}
	for _, d := range s.ControlSources {
		if deploymentplan.Digest(wanted[deploymentplan.ID(d.Object)]) != deploymentplan.Digest(d.Object) {
			return productSessionRenewalBlock("exact current control body")
		}
		delete(wanted, deploymentplan.ID(d.Object))
	}
	if len(wanted) != 0 {
		return productSessionRenewalBlock("missing current control")
	}
	return nil
}
func executeProductSessionRenewalNext(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, previous *productSessionRenewal, native productSessionRenewalNative, apply environmentCreateBootstrapApply) (*productSessionRenewal, error) {
	e := runtime.Execution
	source, err := readProductSessionRenewalSourceForGeneration(store, e, l, 2)
	if err != nil {
		return nil, err
	}
	if source == nil {
		made, er := qualifyProductSessionRenewalNextSource(ctx, cfg, store, runtime, l, previous, native)
		if er != nil {
			return nil, er
		}
		if er = productSessionRenewalWrite(store, l, "source.json", made, 2); er != nil {
			return nil, er
		}
		source = &made
	}
	if err = validateProductSessionRenewalSource(store, e, l, *source); err != nil {
		return nil, err
	}
	return executeProductSessionRenewalPrepared(ctx, cfg, store, runtime, l, &previous.Installed, source, apply)
}
func qualifyProductSessionRenewalNextSource(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, previous *productSessionRenewal, native productSessionRenewalNative) (productSessionRenewalSource, error) {
	var s productSessionRenewalSource
	e := runtime.Execution
	if previous == nil || previous.Source.Generation != 0 || native.Expires.After(time.Now().UTC()) || native.FinalizedAt != nil || native.Status != "active" && native.Status != "expired" || native.Session != previous.Installed.Ceremony.BootstrapSessionID || native.Fingerprint == "" || len(native.Installed) >= len(native.Subjects) {
		return s, productSessionRenewalBlock("expired previous renewal only")
	}
	chain, err := readCreateNormalTailRuntimeChain(store, e, 3)
	if err != nil || len(chain) == 0 {
		return s, productSessionRenewalBlock("current runtime prefix")
	}
	last := chain[len(chain)-1]
	if createNormalTailRuntimeSequence(last.Descriptor) != 3 || last.Child == nil || !createNormalTailRuntimePartial(last.Child, last.Journal) {
		return s, productSessionRenewalBlock("known stopped runtime")
	}
	cp, cj, err := acceptedCreateStage(store, e, "kubernetes/"+productSessionRenewalStage(l, previous.Source, "credentials"))
	if err != nil || len(cp.Resources) != 1 {
		return s, productSessionRenewalBlock("previous credential source")
	}
	custody, j, err := acceptedCreateStage(store, e, "kubernetes/"+productSessionRenewalStage(l, previous.Source, "custody"))
	if err != nil || len(custody.Resources) != 1 {
		return s, productSessionRenewalBlock("previous custody source")
	}
	old := previous.Installed.Ceremony
	secret, err := runtime.Backend.Read(ctx, cp.Resources[0].ID)
	if err != nil || secret == nil || deploymentplan.Map(secret["metadata"])["uid"] != cj.Owners[cp.Resources[0].ID] || !deploymentplan.Matches(cp.Resources[0].Desired, secret) {
		return s, productSessionRenewalBlock("previous native credential owner")
	}
	key, err := base64.StdEncoding.DecodeString(deploymentplan.String(deploymentplan.Map(secret["data"])["tls.key"]))
	if err != nil {
		return s, productSessionRenewalBlock("previous key encoding")
	}
	cert, err := base64.StdEncoding.DecodeString(deploymentplan.String(deploymentplan.Map(secret["data"])["tls.crt"]))
	if err != nil {
		return s, productSessionRenewalBlock("previous leaf encoding")
	}
	pair, err := tls.X509KeyPair(cert, key)
	if err != nil || !managedLateBootstrapChainMatches(pair.Certificate, old) {
		return s, productSessionRenewalBlock("previous exact keypair")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || !leaf.NotAfter.Equal(native.Expires) || leaf.NotAfter.After(time.Now().UTC()) || environmentPreflightBytesSHA256(leaf.Raw) != native.Fingerprint {
		return s, productSessionRenewalBlock("previous native expired fingerprint")
	}
	current, err := runtime.Backend.Read(ctx, custody.Resources[0].ID)
	if err != nil || current == nil || deploymentplan.Map(current["metadata"])["uid"] != j.Owners[custody.Resources[0].ID] || !deploymentplan.Matches(custody.Resources[0].Desired, current) {
		return s, productSessionRenewalBlock("previous current custody")
	}
	for path, expected := range map[string]string{"/run/pki-bootstrap/bootstrap.key": previous.Source.KeySHA256, "/run/pki-bootstrap/bootstrap.csr": previous.Source.CSRSHA256, previous.Source.CertificatePath: environmentPreflightBytesSHA256(cert)} {
		v, er := runtime.Backend.command(ctx, nil, "--namespace", old.BootstrapCredentialNamespace, "exec", "deployment/"+old.CustodyReader, "--container", "custody", "--", "cat", path)
		if er != nil || environmentPreflightBytesSHA256(v) != expected {
			return s, productSessionRenewalBlock("same current key CSR and previous certificate")
		}
		if strings.HasSuffix(path, "bootstrap.csr") {
			block, rest := pem.Decode(v)
			if block == nil || len(bytes.TrimSpace(rest)) != 0 {
				return s, productSessionRenewalBlock("preserved CSR")
			}
			csr, er := x509.ParseCertificateRequest(block.Bytes)
			if er != nil || csr.CheckSignature() != nil || !bytes.Equal(csr.RawSubjectPublicKeyInfo, leaf.RawSubjectPublicKeyInfo) {
				return s, productSessionRenewalBlock("preserved CSR public key")
			}
		}
	}
	// Existing exact key guard is the same Linux ownership/mode contract used by
	// the original producer, and does not alter any file.
	if _, err = runtime.Backend.command(ctx, nil, "--namespace", old.BootstrapCredentialNamespace, "exec", "deployment/"+old.CustodyReader, "--container", "custody", "--", "/bin/sh", "-ec", productSessionForwardKeyGuard(&environmentProductSessionForward{KeySHA256: previous.Source.KeySHA256, CSRSHA256: previous.Source.CSRSHA256})); err != nil {
		return s, productSessionRenewalBlock("preserved private custody guard")
	}
	if err = verifyProductSessionRenewalNextAuthorities(ctx, cfg, store, runtime, l, previous); err != nil {
		return s, err
	}
	program, err := readCreateNormalTailProgram(store, e)
	if err != nil {
		return s, err
	}
	s = productSessionRenewalSource{Generation: 2, PreviousRenewalSHA256: deploymentplan.Digest(previous), RuntimeSequence: 3, Schema: productSessionRenewalSchema, ParentSHA256: e.Journal.PlanSHA256, OwnerSHA256: deploymentplan.Digest(e.owner), ScopeSHA256: productIdentityScope(l), ProgramSHA256: deploymentplan.Digest(program), OriginalInstalledSHA256: deploymentplan.Digest(previous.Installed), OriginalCredentialPlanSHA256: cp.SHA256(), OriginalCredentialJournalSHA256: deploymentplan.Digest(cj), OriginalBootstrapPlanSHA256: previous.Source.OriginalBootstrapPlanSHA256, CustodyPlanSHA256: custody.SHA256(), CustodyJournalSHA256: deploymentplan.Digest(j), ClaimUID: previous.Source.ClaimUID, ServiceAccountUID: previous.Source.ServiceAccountUID, CustodyUID: previous.Source.CustodyUID, OriginalForwardDescriptorSHA256: deploymentplan.Digest(last.Descriptor), OriginalForwardPlanSHA256: last.Child.SHA256(), OriginalForwardJournalSHA256: deploymentplan.Digest(last.Journal), OriginalCertificateSHA256: environmentPreflightBytesSHA256(cert), OriginalFingerprint: native.Fingerprint, KeySHA256: previous.Source.KeySHA256, CSRSHA256: previous.Source.CSRSHA256, SignerPolicySHA256: previous.Source.SignerPolicySHA256, OriginalSession: native.productSessionInstallations, OriginalDeploymentID: previous.Source.DeploymentID, ControlOwners: previous.Source.ControlOwners, QualifiedAt: time.Now().UTC()}
	s.SessionID, s.DeploymentID, s.CertificatePath = productSessionRenewalIdentity(s.ParentSHA256, s.ScopeSHA256, old.BootstrapSessionID)
	for _, v := range last.Child.Resources {
		if v.ID == "Deployment/"+l.Bootstrap.Stack+"-video-cloud/pki-controller" || v.ID == "Deployment/"+l.Bootstrap.Stack+"-video-cloud/certissuer" {
			actual, er := runtime.Backend.Read(ctx, v.ID)
			uid := last.Journal.Owners[v.ID]
			if uid == "" {
				uid = v.UID
			}
			if er != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != uid || !deploymentplan.Matches(v.Desired, actual) {
				return s, productSessionRenewalBlock("frozen runtime control native owner")
			}
			o := deploymentplan.Clone(v.Desired)
			deploymentplan.Map(o["metadata"])["uid"] = uid
			s.ControlSources = append(s.ControlSources, deploymentplan.Definition{Phase: v.Phase, Object: o})
		}
	}
	query := "SELECT json_build_object('count',count(*)) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND (session_id='" + s.SessionID + "' OR deployment_id='" + s.DeploymentID + "')"
	raw, er := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var absent struct {
		Count int `json:"count"`
	}
	if er != nil || decodeEnvironmentJSON(raw, &absent) != nil || absent.Count != 0 {
		return s, productSessionRenewalBlock("unrecorded next session exists")
	}
	if err = validateProductSessionRenewalSource(store, e, l, s); err != nil {
		return s, err
	}
	return s, productSessionRenewalWindow(ctx, e)
}

// Make a missing next-generation completion fail closed once its parent has
// emitted a completion intent; ordinary pending sources are resumable.
func productSessionRenewalNextExists(store secretStore, l *createNormalTailProductLifecycle) bool {
	p, err := store.safePath(productSessionRenewalSelectedBase(l, 2))
	if err != nil {
		return true
	}
	_, err = os.Lstat(p)
	return !os.IsNotExist(err)
}
