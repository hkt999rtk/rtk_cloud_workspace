package main

import (
	"context"
	"encoding/base64"
	"os"
	"path/filepath"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Only newly frozen lifecycle catalogs opt in by carrying the exact original
// legacy registration Secret. Historical catalogs and their recipes stay literal.
const productRegistrationCRLSchema = "rtk-cloud.product-registration-crls/v1"

type productRegistrationCRLs struct {
	Schema, ParentSHA256, OwnerSHA256, ScopeSHA256, SecretUID string
	Issuers                                                   []environmentCreateRegisteredIssuer
	CRLs                                                      []string
	CapturedAt                                                time.Time
}

func productRegistrationCRLBase(l *createNormalTailProductLifecycle) string {
	return filepath.Join("deployment/create", l.ParentSHA256, "bootstrap/product-identities", productIdentityScope(l), "service", "account-registration-crls.json")
}
func productRegistrationCRLSource(l *createNormalTailProductLifecycle) *deploymentplan.Resource {
	if l == nil {
		return nil
	}
	id := createNormalTailAccountRegistrationCRLID(l.Bootstrap.Stack)
	for i := range l.SourceTrust {
		if l.SourceTrust[i].ID == id {
			return &l.SourceTrust[i]
		}
	}
	return nil
}
func validateProductRegistrationCRLs(l *createNormalTailProductLifecycle, next environmentCreateRegisteredIssuer, s *productRegistrationCRLs) error {
	source := productRegistrationCRLSource(l)
	if source == nil || s == nil || s.Schema != productRegistrationCRLSchema || s.ParentSHA256 != l.ParentSHA256 || s.OwnerSHA256 != l.OwnerSHA256 || s.ScopeSHA256 != productIdentityScope(l) || s.SecretUID != l.SourceOwners[source.ID] || s.SecretUID != deploymentplan.Map(source.Baseline["metadata"])["uid"] || deploymentplan.Digest(s.Issuers) != deploymentplan.Digest([]environmentCreateRegisteredIssuer{l.Root, l.Intermediate, next}) {
		return accountRegistrationCRLBlock("new lifecycle public CRL lineage")
	}
	root, re := parseFirstCertificatePEM([]byte(l.Root.Certificate))
	issuer, ie := parseFirstCertificatePEM([]byte(next.Certificate))
	if re != nil || ie != nil || !issuer.IsCA || issuer.CheckSignatureFrom(root) != nil || next.RootFingerprint != l.Root.RootFingerprint || next.ID == l.Intermediate.ID || next.Document["parent_issuer_id"] != l.Root.ID {
		return accountRegistrationCRLBlock("new lifecycle successor Root signature")
	}
	return validateAccountRegistrationCRLBundle(&createNormalTailAccountRegistrationCRLs{Issuers: s.Issuers, CRLs: s.CRLs, CapturedAt: s.CapturedAt}, s.CapturedAt)
}
func bindProductRegistrationCRLs(store secretStore, l *createNormalTailProductLifecycle, next environmentCreateRegisteredIssuer) error {
	if productRegistrationCRLSource(l) == nil || next.Status != "active" {
		return nil
	}
	path, err := store.safePath(productRegistrationCRLBase(l))
	if err != nil {
		return err
	}
	var s productRegistrationCRLs
	if readEnvironmentPreflightJSON(path, &s) != nil {
		return accountRegistrationCRLBlock("new lifecycle lost its original public CRL source")
	}
	if err := validateProductRegistrationCRLs(l, next, &s); err != nil {
		return err
	}
	l.registrationCRLs = &s
	return nil
}
func prepareProductRegistrationCRLs(ctx context.Context, store secretStore, runtime environmentCreateBootstrapRuntime, l *createNormalTailProductLifecycle, next environmentCreateRegisteredIssuer) error {
	if productRegistrationCRLSource(l) == nil {
		return nil
	}
	path, err := store.safePath(productRegistrationCRLBase(l))
	if err != nil {
		return err
	}
	if _, err = os.Lstat(path); err == nil {
		return bindProductRegistrationCRLs(store, l, next)
	} else if !os.IsNotExist(err) {
		return err
	}
	if runtime.Execution == nil || runtime.PKIClient == nil || runtime.Execution.Store != store {
		return accountRegistrationCRLBlock("new lifecycle current issuer reader")
	}
	stage := "kubernetes/product-identities-" + productIdentityScope(l)[:12] + "-successor-crls"
	if createOpenBaoACKCompletionEntry(runtime.Execution.Journal, stage) != nil {
		return accountRegistrationCRLBlock("started trust child lost its public source")
	}
	source := productRegistrationCRLSource(l)
	s := productRegistrationCRLs{Schema: productRegistrationCRLSchema, ParentSHA256: l.ParentSHA256, OwnerSHA256: l.OwnerSHA256, ScopeSHA256: productIdentityScope(l), SecretUID: l.SourceOwners[source.ID], Issuers: []environmentCreateRegisteredIssuer{l.Root, l.Intermediate, next}, CapturedAt: time.Now().UTC()}
	for _, issuer := range s.Issuers {
		raw, err := readProductCurrentCRL(ctx, runtime.PKIClient, issuer)
		if err != nil {
			return err
		}
		s.CRLs = append(s.CRLs, raw)
	}
	if err := validateProductRegistrationCRLs(l, next, &s); err != nil {
		return err
	}
	actual, err := runtime.Backend.Read(ctx, source.ID)
	if err != nil {
		return err
	}
	if actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != s.SecretUID || deploymentplan.Digest(deploymentplan.Projection(actual)) != deploymentplan.Digest(deploymentplan.Projection(source.Baseline)) {
		return accountRegistrationCRLBlock("new lifecycle original registration Secret body")
	}
	if err = runtime.Execution.Check(ctx); err != nil {
		return err
	}
	if _, err = writeProductArtifact(store, productRegistrationCRLBase(l), s); err != nil {
		return err
	}
	return bindProductRegistrationCRLs(store, l, next)
}
func productRegistrationCRLDefinition(l *createNormalTailProductLifecycle, next environmentCreateRegisteredIssuer) (*deploymentplan.Definition, error) {
	source := productRegistrationCRLSource(l)
	if source == nil || next.Status != "active" {
		return nil, nil
	}
	if err := validateProductRegistrationCRLs(l, next, l.registrationCRLs); err != nil {
		return nil, err
	}
	object := deploymentplan.Projection(source.Baseline)
	data := deploymentplan.Map(object["data"])
	if len(data) != 4 || data["client.crl"] == nil || data["client-ca.crt"] == nil || data["tls.crt"] == nil || data["tls.key"] == nil {
		return nil, accountRegistrationCRLBlock("new lifecycle exact registration Secret fields")
	}
	bundle := ""
	for _, raw := range l.registrationCRLs.CRLs {
		bundle += raw
		if len(raw) > 0 && raw[len(raw)-1] != '\n' {
			bundle += "\n"
		}
	}
	data["client.crl"] = base64.StdEncoding.EncodeToString([]byte(bundle))
	return &deploymentplan.Definition{Object: object, Phase: "configuration"}, nil
}
