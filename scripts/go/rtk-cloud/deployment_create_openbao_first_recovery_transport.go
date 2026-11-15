package main

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// readOpenBaoFirstRetainedTransport authenticates only original parent-scoped
// local material. It never creates keys, certificates or a deployment owner.
// A missing derived CA file is reported separately and can be restored only
// from this verified original record while the same parent fence is held.
type openBaoFirstObjectReader interface {
	Read(context.Context, string) (deploymentplan.Object, error)
}

func readOpenBaoFirstRetainedTransport(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, native openBaoFirstObjectReader) (environmentCreateTransport, bool, bool, error) {
	var original environmentCreateTransport
	bad := func() (environmentCreateTransport, bool, bool, error) {
		return environmentCreateTransport{}, false, false, createProviderRecoveryBlock("original OpenBao-first transport is incomplete or differs from its selected generation")
	}
	if e == nil || e.Store != store || cfg.Environment != store.Environment || e.Check(ctx) != nil {
		return bad()
	}
	plan, configSHA, err := loadEnvironmentCreateAdmittedGeneration(store, e.Journal.PlanSHA256, e.Journal.ArtifactSHA256)
	if err != nil || !plan.OpenBaoFirst || plan.Environment != cfg.Environment || plan.Stack != e.Journal.Stack || plan.ConfigurationSHA256 != configSHA || configSHA != deploymentplan.Digest(cfg) {
		return bad()
	}
	var checkpoint *environmentCreateJournalEntry
	for i := range e.Journal.Entries {
		if e.Journal.Entries[i].Key == "openbao-transport-material" {
			if checkpoint != nil {
				return bad()
			}
			checkpoint = &e.Journal.Entries[i]
		}
	}
	base := filepath.Join(store.Root, "deployment", "create", "material", e.Journal.PlanSHA256, "openbao-transport")
	if _, err := os.Lstat(base); errors.Is(err, os.ErrNotExist) {
		if checkpoint != nil {
			return bad()
		}
		for _, entry := range e.Journal.Entries {
			if entry.Key == "kubernetes/foundation" || strings.HasPrefix(entry.Key, "openbao-") {
				return bad()
			}
		}
		return original, false, false, nil
	} else if err != nil || validateEnvironmentPreflightDirectories(store, base) != nil {
		return bad()
	}
	if checkpoint == nil || checkpoint.Phase != "bootstrap" || checkpoint.State != "allocated" && checkpoint.State != "completed" || checkpoint.StartedAt.IsZero() || checkpoint.State == "allocated" && !checkpoint.CompletedAt.IsZero() || checkpoint.State == "completed" && (checkpoint.CompletedAt.IsZero() || checkpoint.CompletedAt.Before(checkpoint.StartedAt)) {
		return bad()
	}
	intentSHA, err := environmentCreatePlanSHA256(openBaoFirstTransportIntent(cfg))
	if err != nil || checkpoint.IntentSHA256 != intentSHA {
		return bad()
	}
	path := filepath.Join(base, "identities.json")
	rawIdentities, err := readProviderStorageKeyFile(path)
	var receipt struct {
		IdentitiesSHA256 string `json:"identities_sha256"`
		CASHA256         string `json:"ca_sha256"`
	}
	if err != nil || decodeEnvironmentJSON(rawIdentities, &original) != nil || len(original.Identities) != 1 || decodeEnvironmentJSON(checkpoint.Receipt, &receipt) != nil || receipt.IdentitiesSHA256 != environmentPreflightBytesSHA256(rawIdentities) || receipt.CASHA256 != environmentPreflightBytesSHA256([]byte(original.CACert)) {
		return bad()
	}
	block, rest := pem.Decode([]byte(original.CACert))
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return bad()
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !ca.IsCA || !ca.BasicConstraintsValid || ca.KeyUsage&x509.KeyUsageCertSign == 0 || ca.Subject.CommonName != "rtk-create-"+cfg.Environment+"-openbao" || ca.CheckSignatureFrom(ca) != nil {
		return bad()
	}
	identity, ok := original.Identities["openbao"]
	if !ok {
		return bad()
	}
	pair, err := tls.X509KeyPair([]byte(identity.Certificate), []byte(identity.Key))
	if err != nil || len(pair.Certificate) != 1 {
		return bad()
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil || leaf.IsCA || leaf.Subject.CommonName != "openbao" {
		return bad()
	}
	got, want := slices.Clone(leaf.DNSNames), slices.Clone(lkeOpenBaoDNSNames(environmentCreateProviderValues(cfg)))
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		return bad()
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	for _, usage := range []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth} {
		if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{usage}}); err != nil {
			return bad()
		}
	}
	if err := verifyOpenBaoFirstTransportStage(ctx, cfg, store, e, plan, original, native); err != nil {
		return bad()
	}
	caPath := filepath.Join(base, "ca.crt")
	raw, err := readProviderStorageKeyFile(caPath)
	if err != nil {
		if _, statErr := os.Lstat(caPath); errors.Is(statErr, os.ErrNotExist) {
			return original, true, true, nil
		}
		return bad()
	}
	if string(raw) != original.CACert || e.Check(ctx) != nil {
		return bad()
	}
	return original, true, false, nil
}

func restoreOpenBaoFirstDerivedCA(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, native openBaoFirstObjectReader) (environmentCreateTransport, error) {
	original, exists, missing, err := readOpenBaoFirstRetainedTransport(ctx, cfg, store, e, native)
	if err != nil {
		return original, err
	}
	if !exists {
		return original, createProviderRecoveryBlock("original OpenBao-first transport has not been created")
	}
	if missing {
		if err := e.Check(ctx); err != nil {
			return original, err
		}
		rel := filepath.Join("deployment", "create", "material", e.Journal.PlanSHA256, "openbao-transport", "ca.crt")
		if err := writeEnvironmentCreateDerivedPrivateFile(store, rel, []byte(original.CACert)); err != nil {
			return original, err
		}
	}
	verified, exists, missing, err := readOpenBaoFirstRetainedTransport(ctx, cfg, store, e, native)
	if err != nil || !exists || missing {
		return environmentCreateTransport{}, createProviderRecoveryBlock("original OpenBao-first transport could not be verified")
	}
	return verified, e.Check(ctx)
}

// Execution-only: once the recorded bytes and parent are verified, complete a
// receipt that was durably allocated before interruption. Preflight must use
// readOpenBaoFirstRetainedTransport and never invoke this mutating helper.
func completeOpenBaoFirstRetainedTransport(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, native openBaoFirstObjectReader) (environmentCreateTransport, error) {
	verified, err := restoreOpenBaoFirstDerivedCA(ctx, cfg, store, e, native)
	if err != nil {
		return verified, err
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "openbao-transport-material")
	if entry == nil {
		return environmentCreateTransport{}, createProviderRecoveryBlock("original OpenBao transport receipt is unavailable")
	}
	if entry.State == "allocated" {
		if err := e.CompleteEntry("openbao-transport-material", time.Now().UTC()); err != nil {
			return environmentCreateTransport{}, err
		}
	}
	return verified, e.Check(ctx)
}

// Once a foundation intent exists, the exact original child and, after a
// write, its original Kubernetes UID/body must authorize these same bytes.
func verifyOpenBaoFirstTransportStage(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, parent environmentCreatePlan, transport environmentCreateTransport, native openBaoFirstObjectReader) error {
	bad := func() error {
		return createProviderRecoveryBlock("original OpenBao transport is not bound to its recorded foundation owner")
	}
	p, err := readEnvironmentCreateStageInput(store, e, "foundation")
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/foundation")
	if err != nil {
		return bad()
	}
	if p == nil && entry == nil {
		return nil
	}
	// An orphan stage input is not yet authenticated by parent intent. It
	// requires separate reconciliation; never use it as identity authority.
	if p == nil || entry == nil || p.ConfigurationSHA256 != parent.ConfigurationSHA256 || p.ReleaseSHA256 != deploymentplan.Digest(parent.Release) {
		return bad()
	}
	j, exists, err := readEnvironmentCreateStageProgress(store, e, "foundation", p)
	if err != nil {
		return bad()
	}
	id := "Secret/" + cfg.Values["CLOUD_STACK_NAME"] + "-secrets/openbao-transport-tls"
	identity := transport.Identities["openbao"]
	expected := map[string]string{"ca.crt": transport.CACert, "tls.crt": identity.Certificate, "tls.key": identity.Key}
	var resource *deploymentplan.Resource
	for i := range p.Resources {
		if p.Resources[i].ID == id {
			if resource != nil {
				return bad()
			}
			resource = &p.Resources[i]
		}
	}
	if resource == nil || resource.Action != "create" {
		return bad()
	}
	data := deploymentplan.Map(resource.Desired["data"])
	if len(data) != len(expected) {
		return bad()
	}
	for key, value := range expected {
		if data[key] != base64.StdEncoding.EncodeToString([]byte(value)) {
			return bad()
		}
	}
	if exists && j.Owners[id] != "" {
		if native == nil {
			return bad()
		}
		live, err := native.Read(ctx, id)
		if err != nil || live == nil || deploymentplan.Map(live["metadata"])["uid"] != j.Owners[id] || !deploymentplan.Matches(resource.Desired, live) {
			return bad()
		}
	}
	return e.Check(ctx)
}
