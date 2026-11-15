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
	"os"
	"path/filepath"
	"slices"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func openBaoFirstManagementNames(cfg deploymentConfig) map[string][]string {
	stack := cfg.Values["CLOUD_STACK_NAME"]
	return map[string][]string{"account-manager": {"account-manager." + stack + "-account-manager.svc.cluster.local"}, "pki-controller": {"pki-controller." + stack + "-video-cloud.svc"}, "certissuer": {"certissuer." + stack + "-video-cloud.svc"}, "video-cloud-api": {"video-cloud-api-pki." + stack + "-video-cloud.svc"}, "video-cloud-api-app-pki": {"video-cloud-api-app-pki." + stack + "-video-cloud.svc"}}
}

func openBaoFirstMaterialSpec(cfg deploymentConfig, parent, key string) (json.RawMessage, map[string]string, error) {
	var intent any
	paths := map[string]string{}
	switch key {
	case "management-transport-material":
		names := openBaoFirstManagementNames(cfg)
		intent = map[string]string{"role": "management", "names_sha256": deploymentplan.Digest(names)}
		base := filepath.Join("deployment", "create", "material", parent, "management-transport")
		paths["identities.json"] = filepath.Join(base, "identities.json")
		paths["ca.crt"] = filepath.Join(base, "ca.crt")
	case "account-signer-material":
		names := []string{"access.key", "access.pub", "refresh.key", "refresh.pub"}
		intent = map[string]any{"role": "account-manager-jwt", "files": names}
		for _, name := range names {
			paths[name] = filepath.Join("pki", "account-manager-jwt", name)
		}
	default:
		return nil, nil, createProviderRecoveryBlock("unsupported original material checkpoint")
	}
	raw, err := json.Marshal(intent)
	return raw, paths, err
}

func verifyOpenBaoFirstMaterialParent(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution) error {
	if e == nil || e.Store != store || cfg.Environment != store.Environment || e.Check(ctx) != nil {
		return createProviderRecoveryBlock("original material parent unavailable")
	}
	p, sha, err := loadEnvironmentCreateAdmittedGeneration(store, e.Journal.PlanSHA256, e.Journal.ArtifactSHA256)
	if err != nil || !p.OpenBaoFirst || p.Environment != cfg.Environment || p.Stack != e.Journal.Stack || p.ConfigurationSHA256 != sha || sha != deploymentplan.Digest(cfg) {
		return createProviderRecoveryBlock("original material generation differs")
	}
	return nil
}

// The producer records intent before touching material and only commits a
// receipt after rereading every exact private file. Existing unidentified files
// or an interrupted attempt are never silently claimed or regenerated.
func createOpenBaoFirstMaterial(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, key string, produce func() error) error {
	if err := verifyOpenBaoFirstMaterialParent(ctx, cfg, store, e); err != nil {
		return err
	}
	intent, paths, err := openBaoFirstMaterialSpec(cfg, e.Journal.PlanSHA256, key)
	if err != nil {
		return err
	}
	if produce == nil || createOpenBaoACKCompletionEntry(e.Journal, key) != nil {
		return createProviderRecoveryBlock("original material operation already has intent")
	}
	for _, rel := range paths {
		path, err := store.safePath(rel)
		if err != nil {
			return err
		}
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			return createProviderRecoveryBlock("unidentified original material must be preserved")
		}
	}
	if err := e.BeginEntry(key, "bootstrap", intent, time.Now().UTC()); err != nil {
		return err
	}
	if err := produce(); err != nil {
		return err
	}
	digests := map[string]string{}
	for name, rel := range paths {
		path, pathErr := store.safePath(rel)
		if pathErr != nil || path != filepath.Join(store.Root, rel) || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
			return createProviderRecoveryBlock("original produced material path is unsafe")
		}
		raw, err := readProviderStorageKeyFile(path)
		if err != nil {
			return createProviderRecoveryBlock("original produced material is incomplete")
		}
		digests[name] = environmentPreflightBytesSHA256(raw)
	}
	receipt, _ := json.Marshal(map[string]any{"files": digests})
	if err := e.RecordReceipt(key, receipt); err != nil {
		return err
	}
	if _, missing, state, err := readOpenBaoFirstMaterial(ctx, cfg, store, e, key, nil); err != nil || len(missing) != 0 || state != "allocated" {
		return createProviderRecoveryBlock("original produced material cannot be verified")
	}
	return e.CompleteEntry(key, time.Now().UTC())
}

// readOpenBaoFirstMaterial never writes. Only a missing derived public file may
// be computed from its recorded original private material, and only if its
// bytes match the already durable receipt. Missing private material blocks.
func readOpenBaoFirstMaterial(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, key string, native openBaoFirstObjectReader) (map[string][]byte, []string, string, error) {
	bad := func() (map[string][]byte, []string, string, error) {
		return nil, nil, "", createProviderRecoveryBlock("original material checkpoint or owner is incomplete or changed")
	}
	if verifyOpenBaoFirstMaterialParent(ctx, cfg, store, e) != nil {
		return bad()
	}
	intent, paths, err := openBaoFirstMaterialSpec(cfg, e.Journal.PlanSHA256, key)
	if err != nil {
		return bad()
	}
	var entry *environmentCreateJournalEntry
	for i := range e.Journal.Entries {
		if e.Journal.Entries[i].Key == key {
			if entry != nil {
				return bad()
			}
			entry = &e.Journal.Entries[i]
		}
	}
	if entry == nil {
		for _, rel := range paths {
			if _, err := os.Lstat(filepath.Join(store.Root, rel)); !errors.Is(err, os.ErrNotExist) {
				return bad()
			}
		}
		return nil, nil, "absent", nil
	}
	sha, err := environmentCreatePlanSHA256(intent)
	if err != nil || entry.Phase != "bootstrap" || entry.IntentSHA256 != sha || (entry.State != "allocated" && entry.State != "completed") || entry.StartedAt.IsZero() || (entry.State == "allocated" && !entry.CompletedAt.IsZero()) || (entry.State == "completed" && (entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt))) {
		return bad()
	}
	var receipt struct {
		Files map[string]string `json:"files"`
	}
	if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || len(receipt.Files) != len(paths) {
		return bad()
	}
	values := map[string][]byte{}
	var missing []string
	for name, rel := range paths {
		if !environmentPreflightDigest.MatchString(receipt.Files[name]) {
			return bad()
		}
		path, pathErr := store.safePath(rel)
		if pathErr != nil || path != filepath.Join(store.Root, rel) || validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
			return bad()
		}
		raw, err := readProviderStorageKeyFile(path)
		if err != nil {
			if _, statErr := os.Lstat(path); errors.Is(statErr, os.ErrNotExist) && (name == "ca.crt" || name == "access.pub" || name == "refresh.pub") {
				missing = append(missing, name)
				continue
			}
			return bad()
		}
		if environmentPreflightBytesSHA256(raw) != receipt.Files[name] {
			return bad()
		}
		values[name] = raw
	}
	if key == "management-transport-material" {
		var material environmentCreateTransport
		if decodeEnvironmentJSON(values["identities.json"], &material) != nil || validateOpenBaoFirstManagementTransport(cfg, material) != nil {
			return bad()
		}
		ca := []byte(material.CACert)
		if _, ok := values["ca.crt"]; !ok {
			values["ca.crt"] = ca
		} else if !bytes.Equal(values["ca.crt"], ca) {
			return bad()
		}
		if err := verifyOpenBaoFirstMaterialStage(ctx, cfg, store, e, key, material, nil, native); err != nil {
			return bad()
		}
	} else {
		for _, role := range []string{"access", "refresh"} {
			block, rest := pem.Decode(values[role+".key"])
			if block == nil || block.Type != "RSA PRIVATE KEY" || len(bytes.TrimSpace(rest)) != 0 {
				return bad()
			}
			private, err := x509.ParsePKCS1PrivateKey(block.Bytes)
			if err != nil || private.Validate() != nil {
				return bad()
			}
			der, err := x509.MarshalPKIXPublicKey(&private.PublicKey)
			if err != nil {
				return bad()
			}
			derived := pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
			if pub, ok := values[role+".pub"]; ok {
				if !bytes.Equal(pub, derived) {
					return bad()
				}
			} else {
				values[role+".pub"] = derived
			}
		}
		if err := verifyOpenBaoFirstMaterialStage(ctx, cfg, store, e, key, environmentCreateTransport{}, values, native); err != nil {
			return bad()
		}
	}
	for _, name := range missing {
		if environmentPreflightBytesSHA256(values[name]) != receipt.Files[name] {
			return bad()
		}
	}
	if e.Check(ctx) != nil {
		return bad()
	}
	return values, missing, entry.State, nil
}

func completeOpenBaoFirstMaterial(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, key string, native openBaoFirstObjectReader) error {
	values, missing, state, err := readOpenBaoFirstMaterial(ctx, cfg, store, e, key, native)
	if err != nil {
		return err
	}
	if state == "absent" {
		return createProviderRecoveryBlock("original material has not been generated")
	}
	_, paths, err := openBaoFirstMaterialSpec(cfg, e.Journal.PlanSHA256, key)
	if err != nil {
		return err
	}
	for _, name := range missing {
		if err := e.Check(ctx); err != nil {
			return err
		}
		if err := writeEnvironmentCreateDerivedPrivateFile(store, paths[name], values[name]); err != nil {
			return err
		}
	}
	if _, missing, _, err = readOpenBaoFirstMaterial(ctx, cfg, store, e, key, native); err != nil || len(missing) != 0 {
		return createProviderRecoveryBlock("original derived material could not be verified")
	}
	if state == "allocated" {
		if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
			return err
		}
	}
	return e.Check(ctx)
}

func validateOpenBaoFirstManagementTransport(cfg deploymentConfig, m environmentCreateTransport) error {
	bad := func() error { return createProviderRecoveryBlock("original management transport identity differs") }
	block, rest := pem.Decode([]byte(m.CACert))
	if block == nil || block.Type != "CERTIFICATE" || len(bytes.TrimSpace(rest)) != 0 {
		return bad()
	}
	ca, err := x509.ParseCertificate(block.Bytes)
	if err != nil || !ca.IsCA || !ca.BasicConstraintsValid || ca.KeyUsage&x509.KeyUsageCertSign == 0 || ca.Subject.CommonName != "rtk-create-"+cfg.Environment+"-management" || ca.CheckSignatureFrom(ca) != nil {
		return bad()
	}
	names := openBaoFirstManagementNames(cfg)
	if len(m.Identities) != len(names) {
		return bad()
	}
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	for name, dns := range names {
		identity, ok := m.Identities[name]
		if !ok {
			return bad()
		}
		pair, err := tls.X509KeyPair([]byte(identity.Certificate), []byte(identity.Key))
		if err != nil || len(pair.Certificate) != 1 {
			return bad()
		}
		leaf, err := x509.ParseCertificate(pair.Certificate[0])
		if err != nil || leaf.IsCA || leaf.Subject.CommonName != name {
			return bad()
		}
		got, want := slices.Clone(leaf.DNSNames), slices.Clone(dns)
		slices.Sort(got)
		slices.Sort(want)
		if !slices.Equal(got, want) {
			return bad()
		}
		for _, usage := range []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth, x509.ExtKeyUsageServerAuth} {
			if _, err := leaf.Verify(x509.VerifyOptions{Roots: roots, KeyUsages: []x509.ExtKeyUsage{usage}}); err != nil {
				return bad()
			}
		}
	}
	return nil
}

func verifyOpenBaoFirstMaterialStage(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, key string, transport environmentCreateTransport, signer map[string][]byte, native openBaoFirstObjectReader) error {
	bad := func() error {
		return createProviderRecoveryBlock("original material differs from foundation-services owner")
	}
	p, err := readEnvironmentCreateStageInput(store, e, "foundation-services")
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/foundation-services")
	if err != nil {
		return bad()
	}
	if p == nil && entry == nil {
		return nil
	}
	if p == nil || entry == nil {
		return bad()
	}
	parent, _, err := loadEnvironmentCreateAdmittedGeneration(store, e.Journal.PlanSHA256, e.Journal.ArtifactSHA256)
	if err != nil || p.ConfigurationSHA256 != parent.ConfigurationSHA256 || p.ReleaseSHA256 != deploymentplan.Digest(parent.Release) {
		return bad()
	}
	journal, exists, err := readEnvironmentCreateStageProgress(store, e, "foundation-services", p)
	if err != nil {
		return bad()
	}
	expected := map[string]map[string][]byte{}
	if key == "management-transport-material" {
		for name, identity := range transport.Identities {
			ns := e.Journal.Stack + "-video-cloud"
			if name == "account-manager" {
				ns = e.Journal.Stack + "-account-manager"
			}
			expected["Secret/"+ns+"/pki-create-"+name+"-transport"] = map[string][]byte{"ca.crt": []byte(transport.CACert), "tls.crt": []byte(identity.Certificate), "tls.key": []byte(identity.Key)}
		}
	} else {
		expected["Secret/"+e.Journal.Stack+"-account-manager/account-manager-jwt"] = signer
	}
	for id, values := range expected {
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
		if len(data) != len(values) {
			return bad()
		}
		for name, value := range values {
			if data[name] != base64.StdEncoding.EncodeToString(value) {
				return bad()
			}
		}
		if exists && journal.Owners[id] != "" {
			if native == nil {
				return bad()
			}
			live, err := native.Read(ctx, id)
			if err != nil || live == nil || deploymentplan.Map(live["metadata"])["uid"] != journal.Owners[id] || !deploymentplan.Matches(resource.Desired, live) {
				return bad()
			}
		}
	}
	return e.Check(ctx)
}
