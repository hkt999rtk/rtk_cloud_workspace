package main

// This profile is deliberately separate from first-bootstrap renewal. It can
// repair only the temporary transport projection of the original generation,
// after every recorded managed authority is ACTIVE and before managed transport
// enrollment. It never generates a CA key, issuer, OpenBao identity or receipt.
import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const activeTransportRenewalSchema = "rtk-cloud.active-temporary-transport-renewal/v2"

type activeTransportRenewalResource struct {
	ID      string                `json:"id"`
	UID     string                `json:"uid"`
	Before  deploymentplan.Object `json:"before"`
	Desired deploymentplan.Object `json:"desired"`
}

type activeTransportRenewalPlan struct {
	Schema           string                           `json:"schema"`
	Parent           string                           `json:"parent_plan_sha256"`
	OwnerSHA         string                           `json:"parent_owner_sha256"`
	JournalSHA       string                           `json:"parent_journal_sha256"`
	OriginalProofSHA string                           `json:"original_renewal_proof_sha256"`
	ConfigurationSHA string                           `json:"configuration_sha256"`
	CheckerSHA       string                           `json:"checker_sha256"`
	KubeconfigSHA    string                           `json:"kubeconfig_sha256"`
	Candidate        string                           `json:"candidate_relative_path"`
	Material         map[string]string                `json:"material_sha256"`
	AuthorityKeys    map[string]string                `json:"authority_key_paths"`
	Resources        []activeTransportRenewalResource `json:"resources"`
	PodUID           string                           `json:"openbao_pod_uid"`
	SetUID           string                           `json:"openbao_statefulset_uid"`
}

type activeTransportRenewalArtifact struct {
	Schema        string                       `json:"schema"`
	Plan          activeTransportRenewalPlan   `json:"plan"`
	Qualification deploymentplan.Qualification `json:"qualification"`
	QualifiedAt   time.Time                    `json:"qualified_at"`
	ValidUntil    time.Time                    `json:"valid_until"`
}

type activeTransportRenewalProof struct {
	Schema           string            `json:"schema"`
	Parent           string            `json:"parent_plan_sha256"`
	Review           string            `json:"review_sha256"`
	OriginalProofSHA string            `json:"original_renewal_proof_sha256"`
	Material         map[string]string `json:"material_sha256"`
	AuthorityKeys    map[string]string `json:"authority_key_paths"`
	Owners           map[string]string `json:"owners"`
	PodUID           string            `json:"openbao_pod_uid"`
	SetUID           string            `json:"openbao_statefulset_uid"`
	Completed        bool              `json:"completed"`
}

func activeTransportError() error {
	return createProviderRecoveryBlock("same-key temporary TLS repair requires the exact original completed generation, managed ACTIVE issuers, owners, material and qualified proof")
}

func temporaryTransportSigningKey(raw []byte, certificate *x509.Certificate) (crypto.Signer, error) {
	block, rest := pem.Decode(raw)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 || block.Type != "PRIVATE KEY" {
		return nil, activeTransportError()
	}
	key, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, activeTransportError()
	}
	signer, ok := key.(*ecdsa.PrivateKey)
	if !ok || signer.Curve.Params().Name != "P-256" {
		return nil, activeTransportError()
	}
	pub, err := x509.MarshalPKIXPublicKey(signer.Public())
	old, oldErr := x509.MarshalPKIXPublicKey(certificate.PublicKey)
	if err != nil || oldErr != nil || !bytes.Equal(pub, old) {
		return nil, activeTransportError()
	}
	return signer, nil
}

// Candidate creation is an execution preparation action, never a preflight side
// effect. No key generator is reachable from this helper.
func renewActiveTransportSameKey(old environmentCreateTransport, caKey []byte, now time.Time) (environmentCreateTransport, error) {
	out := environmentCreateTransport{Identities: map[string]environmentCreateTransportIdentity{}}
	ca, err := parseFirstCertificatePEM([]byte(old.CACert))
	if err != nil || ca == nil || !ca.IsCA || ca.CheckSignatureFrom(ca) != nil || len(old.Identities) == 0 {
		return out, activeTransportError()
	}
	signer, err := temporaryTransportSigningKey(caKey, ca)
	if err != nil {
		return out, err
	}
	reissue := func(certificate, parent *x509.Certificate, public any) ([]byte, error) {
		template := *certificate
		template.SerialNumber, err = rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if err != nil {
			return nil, err
		}
		template.NotBefore, template.NotAfter = now.Add(-time.Minute), now.Add(24*time.Hour)
		template.ExtraExtensions = certificate.Extensions
		der, err := x509.CreateCertificate(rand.Reader, &template, parent, public, signer)
		if err != nil {
			return nil, err
		}
		return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
	}
	root, err := reissue(ca, ca, ca.PublicKey)
	if err != nil {
		return out, err
	}
	out.CACert = string(root)
	renewedCA, err := parseFirstCertificatePEM(root)
	if err != nil {
		return out, err
	}
	for name, identity := range old.Identities {
		leaf, err := parseFirstCertificatePEM([]byte(identity.Certificate))
		if err != nil || leaf == nil || leaf.CheckSignatureFrom(ca) != nil {
			return out, activeTransportError()
		}
		if _, err := temporaryTransportSigningKey([]byte(identity.Key), leaf); err != nil {
			return out, err
		}
		cert, err := reissue(leaf, renewedCA, leaf.PublicKey)
		if err != nil {
			return out, err
		}
		out.Identities[name] = environmentCreateTransportIdentity{Certificate: string(cert), Key: identity.Key}
	}
	return out, verifyActiveTransportSameKey(old, out, caKey, now)
}

func verifyActiveTransportSameKey(old, current environmentCreateTransport, caKey []byte, now time.Time) error {
	return verifyActiveTransportSameKeyForMinimum(old, current, caKey, now, 2*time.Hour)
}

func verifyActiveTransportSameKeyForMinimum(old, current environmentCreateTransport, caKey []byte, now time.Time, minimum time.Duration) error {
	if minimum <= 0 {
		return activeTransportError()
	}
	before, err := parseFirstCertificatePEM([]byte(old.CACert))
	after, newErr := parseFirstCertificatePEM([]byte(current.CACert))
	if err != nil || newErr != nil || before == nil || after == nil || !before.IsCA || !after.IsCA || before.CheckSignatureFrom(before) != nil || after.CheckSignatureFrom(after) != nil || len(old.Identities) == 0 || len(old.Identities) != len(current.Identities) {
		return activeTransportError()
	}
	if _, err := temporaryTransportSigningKey(caKey, before); err != nil {
		return err
	}
	if _, err := temporaryTransportSigningKey(caKey, after); err != nil {
		return err
	}
	equivalent := func(a, b *x509.Certificate) bool {
		return bytes.Equal(a.RawSubject, b.RawSubject) && bytes.Equal(a.RawIssuer, b.RawIssuer) && bytes.Equal(a.RawSubjectPublicKeyInfo, b.RawSubjectPublicKeyInfo) && a.SignatureAlgorithm == b.SignatureAlgorithm && deploymentplan.Digest(a.Extensions) == deploymentplan.Digest(b.Extensions) && a.KeyUsage == b.KeyUsage && a.IsCA == b.IsCA && a.BasicConstraintsValid == b.BasicConstraintsValid && a.MaxPathLen == b.MaxPathLen && a.MaxPathLenZero == b.MaxPathLenZero && deploymentplan.Digest(a.ExtKeyUsage) == deploymentplan.Digest(b.ExtKeyUsage) && deploymentplan.Digest(a.UnknownExtKeyUsage) == deploymentplan.Digest(b.UnknownExtKeyUsage) && deploymentplan.Digest(a.DNSNames) == deploymentplan.Digest(b.DNSNames) && deploymentplan.Digest(a.IPAddresses) == deploymentplan.Digest(b.IPAddresses) && deploymentplan.Digest(a.EmailAddresses) == deploymentplan.Digest(b.EmailAddresses) && deploymentplan.Digest(a.URIs) == deploymentplan.Digest(b.URIs) && a.SerialNumber.Cmp(b.SerialNumber) != 0 && !b.NotBefore.After(now) && b.NotAfter.After(now.Add(minimum)) && b.NotAfter.Sub(b.NotBefore) <= 24*time.Hour+time.Minute
	}
	if !equivalent(before, after) {
		return activeTransportError()
	}
	for name, identity := range old.Identities {
		renewed, ok := current.Identities[name]
		if !ok || identity.Key != renewed.Key {
			return activeTransportError()
		}
		original, err := parseFirstCertificatePEM([]byte(identity.Certificate))
		candidate, candidateErr := parseFirstCertificatePEM([]byte(renewed.Certificate))
		if err != nil || candidateErr != nil || original == nil || candidate == nil || !equivalent(original, candidate) || original.CheckSignatureFrom(before) != nil || candidate.CheckSignatureFrom(after) != nil || !candidate.NotAfter.Equal(after.NotAfter) || !candidate.NotBefore.Equal(after.NotBefore) {
			return activeTransportError()
		}
		if _, err := temporaryTransportSigningKey([]byte(renewed.Key), candidate); err != nil {
			return err
		}
	}
	return nil
}

// V1 remains immutable. The new pointer contains only a completed admitted V2
// proof. Normal consumers read its renewed public certificates, while authority
// key references still point to the original V1 custody files.
func readActiveTransportRenewal(store secretStore, parent string, old map[string]environmentCreateTransport, legacy *initialTransportRenewal, command ...*environmentCreateExecution) (map[string]environmentCreateTransport, *initialTransportRenewal, error) {
	relative := filepath.Join("deployment/create/material", parent, "active-transport-renewal.json")
	if _, err := os.Lstat(filepath.Join(store.Root, relative)); os.IsNotExist(err) {
		return old, legacy, nil
	}
	bad := func() (map[string]environmentCreateTransport, *initialTransportRenewal, error) {
		return nil, nil, activeTransportError()
	}
	raw, err := activeTransportPrivateFile(store, relative)
	if err != nil {
		return bad()
	}
	var proof activeTransportRenewalProof
	if legacy == nil || json.Unmarshal(raw, &proof) != nil || proof.Schema != activeTransportRenewalSchema || proof.Parent != parent || !proof.Completed || !environmentPreflightDigest.MatchString(proof.Review) || proof.OriginalProofSHA != deploymentplan.Digest(legacy) || len(proof.Owners) != 13 || len(proof.AuthorityKeys) != 2 || proof.PodUID == "" || proof.SetUID == "" {
		return bad()
	}
	base := filepath.Join("deployment/create/transport-renewal", proof.Review)
	completed, err := readBoundedEnvironmentFile(filepath.Join(store.Root, base, "completed.json"), true)
	if err != nil || !bytes.Equal(completed, raw) {
		return bad()
	}
	var artifact activeTransportRenewalArtifact
	if readExecutionJSON(filepath.Join(store.Root, base, "qualification.json"), &artifact) != nil || artifact.Schema != activeTransportRenewalSchema+"/artifact" || deploymentplan.Digest(artifact.Plan) != proof.Review || artifact.Plan.Parent != parent || artifact.Plan.OriginalProofSHA != proof.OriginalProofSHA || artifact.Qualification.Decision != "GO" || artifact.Qualification.PlanSHA256 != proof.Review || len(artifact.Qualification.Gates) != 5 {
		return bad()
	}
	for i, id := range activeTransportQualificationGates {
		g := artifact.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != proof.Review {
			return bad()
		}
	}
	var before environmentCreateJournal
	if readExecutionJSON(filepath.Join(store.Root, base, "source-journal.json"), &before) != nil || deploymentplan.Digest(before) != artifact.Plan.JournalSHA {
		return bad()
	}
	var owner environmentCreateOwner
	var journal environmentCreateJournal
	if readEnvironmentPreflightJSON(filepath.Join(store.Root, "deployment/.execution-active/owner.json"), &owner) != nil || owner.PlanSHA256 != parent || deploymentplan.Digest(owner) != artifact.Plan.OwnerSHA || readExecutionJSON(filepath.Join(store.Root, owner.Journal), &journal) != nil || journal.OwnerToken != before.OwnerToken {
		return bad()
	}
	found := false
	for _, entry := range journal.Entries {
		if entry.Key == "active-transport-renewal-"+proof.Review && entry.State == "completed" && !entry.CompletedAt.IsZero() && entry.StartedAt.Before(artifact.ValidUntil) && !entry.StartedAt.Before(artifact.QualifiedAt) && deploymentplan.Digest(json.RawMessage(entry.Receipt)) == activeTransportJournalDigest(proof) {
			found = true
		}
	}
	repairJournal := journal
	repairEnd := len(before.Entries) + 1 + len(artifact.Plan.Resources)
	if len(journal.Entries) < repairEnd {
		return bad()
	}
	repairJournal.Entries = repairJournal.Entries[:repairEnd]
	if !found || !activeTransportNativeSourceCheckpoint(store, &environmentCreateExecution{Store: store, owner: owner, Journal: repairJournal}) {
		return bad()
	}
	if len(artifact.Plan.Resources) != 13 || len(proof.Material) != len(artifact.Plan.Material)-activeTransportEvidenceCount(artifact.Plan.Material)+4 {
		return bad()
	}
	for path, sha := range artifact.Plan.Material {
		if !strings.HasPrefix(path, "evidence/") && proof.Material[path] != sha {
			return bad()
		}
	}
	for _, r := range artifact.Plan.Resources {
		if r.UID == "" || proof.Owners[r.ID] != r.UID {
			return bad()
		}
		intent := map[string]string{"resource": r.ID, "uid": r.UID, "before_sha256": deploymentplan.Digest(r.Before), "desired_sha256": deploymentplan.Digest(r.Desired)}
		key := "active-transport-renewal-" + proof.Review + "-" + strings.ReplaceAll(r.ID, "/", "-")
		matched := false
		for _, entry := range journal.Entries {
			if entry.Key == key && entry.State == "completed" && !entry.CompletedAt.IsZero() && entry.IntentSHA256 == deploymentplan.Digest(intent) && deploymentplan.Digest(json.RawMessage(entry.Receipt)) == deploymentplan.Digest(intent) {
				matched = true
			}
		}
		if !matched {
			return bad()
		}
	}
	for path, sha := range proof.Material {
		bytes, err := activeTransportPrivateFile(store, path)
		if err != nil || environmentPreflightBytesSHA256(bytes) != sha {
			return bad()
		}
	}
	verifiedAt := time.Now().UTC()
	if selected, scopeErr := managedLatePostMintHistoricalTransportScope(store, parent, command...); scopeErr != nil {
		return bad()
	} else if selected {
		// Every original artifact/receipt/private-byte/same-key pin above remains
		// authoritative. This completed transport no longer serves Post TLS.
		verifiedAt = artifact.QualifiedAt
	}
	current := map[string]environmentCreateTransport{}
	for _, role := range []string{"management", "openbao"} {
		expected := filepath.Join("deployment/create/transport-renewal", legacy.Review, role+"-transport/ca.key")
		if proof.AuthorityKeys[role] != expected {
			return bad()
		}
		key, err := activeTransportPrivateFile(store, expected)
		if err != nil {
			return bad()
		}
		var material environmentCreateTransport
		if readExecutionJSON(filepath.Join(store.Root, base, role+"-transport/identities.json"), &material) != nil {
			return bad()
		}
		cert, err := readBoundedEnvironmentFile(filepath.Join(store.Root, base, role+"-transport/ca.crt"), true)
		if err != nil || string(cert) != material.CACert || verifyActiveTransportSameKey(old[role], material, key, verifiedAt) != nil {
			return bad()
		}
		current[role] = material
	}
	effective := &initialTransportRenewal{Schema: proof.Schema, Parent: parent, Review: proof.Review, Completed: true, Material: proof.Material, Owners: proof.Owners}
	return current, effective, nil
}

var activeTransportQualificationGates = []string{"deployment.active-transport.target", "deployment.active-transport.issuers", "deployment.active-transport.material", "deployment.active-transport.owners", "deployment.active-transport.dry-run"}

func loadActiveTransportExecution(cfg deploymentConfig, store secretStore, parent string) (*environmentCreateExecution, environmentCreateProviderRecoveryArtifact, error) {
	var artifact environmentCreateProviderRecoveryArtifact
	bad := func() (*environmentCreateExecution, environmentCreateProviderRecoveryArtifact, error) {
		return nil, artifact, activeTransportError()
	}
	if cfg.Environment != "dev" || cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] != "standard" || !environmentPreflightDigest.MatchString(parent) {
		return bad()
	}
	fence := filepath.Join(store.Root, "deployment/.execution-active")
	if validateEnvironmentPreflightDirectories(store, fence) != nil {
		return bad()
	}
	lock, err := deploymentplan.LockExecutionOwner(fence, false)
	if err != nil {
		return bad()
	}
	e := &environmentCreateExecution{Store: store, running: lock}
	loaded := false
	defer func() {
		if !loaded {
			e.Close()
		}
	}()
	e.ownerInfo, err = os.Lstat(fence)
	if err != nil {
		return bad()
	}
	e.runningInfo, err = lock.Stat()
	if err != nil {
		return bad()
	}
	if readEnvironmentPreflightJSON(filepath.Join(fence, "owner.json"), &e.owner) != nil || e.owner.Schema != environmentCreateOwnerSchema || e.owner.PlanSHA256 != parent || e.owner.Environment != cfg.Environment || e.owner.Stack != cfg.Values["CLOUD_STACK_NAME"] || filepath.Clean(e.owner.Journal) != e.owner.Journal || !strings.HasPrefix(e.owner.Journal, "deployment/create/executions/"+parent+"/execution-") {
		return bad()
	}
	e.JournalPath, err = privateDeploymentStatePath(cfg.Environment, e.owner.Journal)
	if err != nil {
		return bad()
	}
	e.Directory = filepath.Dir(e.JournalPath)
	if readEnvironmentPreflightJSON(e.JournalPath, &e.Journal) != nil || e.Journal.OwnerToken != e.owner.OwnerToken || e.Journal.PlanSHA256 != parent || !e.Journal.CompletedAt.IsZero() || len(e.Journal.Entries) == 0 {
		return bad()
	}
	if readEnvironmentPreflightJSON(filepath.Join(store.Root, "deployment/create/recovery", parent, "qualification.json"), &artifact) != nil || artifact.Schema != "rtk-cloud.provider-create-recovery-artifact/v1" || deploymentplan.Digest(artifact.Plan) != parent || deploymentplan.Digest(artifact) != e.Journal.ArtifactSHA256 || artifact.Qualification.Decision != "GO" || deploymentplan.Digest(cfg) != artifact.Plan.Plan.ConfigurationSHA256 {
		return bad()
	}
	if e.Check(context.Background()) != nil {
		return bad()
	}
	loaded = true
	return e, artifact, nil
}

func activeTransportOriginalIdentity(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend) (string, string, error) {
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "openbao/init.json"), true)
	if err != nil {
		return "", "", err
	}
	var initialized struct {
		RootToken  string   `json:"root_token"`
		UnsealKeys []string `json:"unseal_keys_b64"`
	}
	root, rootErr := store.read("openbao/root-token")
	unseal, unsealErr := store.read("openbao/unseal-key")
	if json.Unmarshal(raw, &initialized) != nil || len(initialized.UnsealKeys) != 1 || rootErr != nil || unsealErr != nil || root == "" || initialized.RootToken != root || initialized.UnsealKeys[0] != unseal {
		return "", "", activeTransportError()
	}
	var proof struct {
		InitSHA   string `json:"init_sha256"`
		PodUID    string `json:"pod_uid"`
		SetUID    string `json:"statefulset_uid"`
		RootRef   string `json:"root_token_ref"`
		UnsealRef string `json:"unseal_ref"`
	}
	for _, entry := range e.Journal.Entries {
		if entry.Key == "openbao-initialize" && entry.State == "completed" {
			if json.Unmarshal(entry.Receipt, &proof) != nil {
				return "", "", activeTransportError()
			}
		}
	}
	ns := cfg.Values["CLOUD_STACK_NAME"] + "-secrets"
	set, err := b.Read(ctx, "StatefulSet/"+ns+"/openbao")
	if err != nil {
		return "", "", err
	}
	pod, err := b.Read(ctx, "Pod/"+ns+"/openbao-0")
	if err != nil || proof.InitSHA != environmentPreflightBytesSHA256(raw) || proof.RootRef != "openbao/root-token" || proof.UnsealRef != "openbao/unseal-key" || deploymentplan.String(deploymentplan.Map(set["metadata"])["uid"]) != proof.SetUID || deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"]) != proof.PodUID || validateOpenBaoPodOwner(set, pod) != nil {
		return "", "", activeTransportError()
	}
	// The original foundation remains the workload authority before transport
	// cutover; bind its exact StatefulSet body and immutable Pod container image.
	foundationKey := ""
	for _, entry := range e.Journal.Entries {
		if strings.HasPrefix(entry.Key, "kubernetes/foundation-recovery-") && entry.State == "completed" {
			foundationKey = entry.Key
		}
	}
	foundation, journal, err := acceptedCreateStage(store, e, foundationKey)
	if err != nil {
		return "", "", err
	}
	matched := false
	for _, r := range foundation.Resources {
		if r.ID != "StatefulSet/"+ns+"/openbao" {
			continue
		}
		if journal.Owners[r.ID] != proof.SetUID || !deploymentplan.Matches(r.Desired, set) {
			return "", "", activeTransportError()
		}
		wanted := certIssuerObjectList(environmentPKIPod(r.Desired)["containers"])
		actual := certIssuerObjectList(deploymentplan.Map(pod["spec"])["containers"])
		if len(wanted) != len(actual) {
			return "", "", activeTransportError()
		}
		for i, c := range wanted {
			if deploymentplan.Map(c)["name"] != deploymentplan.Map(actual[i])["name"] || deploymentplan.Map(c)["image"] != deploymentplan.Map(actual[i])["image"] {
				return "", "", activeTransportError()
			}
		}
		matched = true
	}
	if !matched {
		return "", "", activeTransportError()
	}
	// Exact mounted original material is checked through the authenticated
	// Kubernetes channel. An expired API certificate never authorizes a TLS bypass.
	return proof.PodUID, proof.SetUID, e.Check(ctx)
}

func activeTransportOwners(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, old, current map[string]environmentCreateTransport) ([]activeTransportRenewalResource, error) {
	latest := map[string]deploymentplan.Resource{}
	owners := map[string]string{}
	for _, entry := range e.Journal.Entries {
		if entry.State != "completed" || !strings.HasPrefix(entry.Key, "kubernetes/") {
			continue
		}
		var receipt struct {
			Plan string `json:"plan_sha256"`
		}
		if json.Unmarshal(entry.Receipt, &receipt) != nil || !environmentPreflightDigest.MatchString(receipt.Plan) {
			continue
		}
		plan, journal, err := acceptedCreateStage(store, e, entry.Key)
		if err != nil {
			return nil, err
		}
		intent, err := environmentCreateKubernetesStageIntent(plan)
		sha, hashErr := environmentCreatePlanSHA256(intent)
		if err != nil || hashErr != nil || sha != entry.IntentSHA256 {
			return nil, activeTransportError()
		}
		for _, r := range plan.Resources {
			latest[r.ID] = r
			owners[r.ID] = journal.Owners[r.ID]
		}
	}
	result := []activeTransportRenewalResource{}
	for id, r := range latest {
		before, changed := initialTransportOverride(r.Desired, old)
		if !changed {
			continue
		}
		desired, _ := initialTransportOverride(r.Desired, current)
		actual, err := b.Read(ctx, id)
		if err != nil || actual == nil || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) != owners[id] || !deploymentplan.Matches(before, actual) {
			return nil, activeTransportError()
		}
		// Use admitted bodies rather than copy arbitrary live configuration. Preserve
		// the exact live resourceVersion for a Kubernetes replace precondition.
		result = append(result, activeTransportRenewalResource{ID: id, UID: owners[id], Before: before, Desired: desired})
	}
	id := "ConfigMap/" + cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud/pki-create-openbao-tls-consumer-trust"
	r, ok := latest[id]
	if !ok {
		return nil, activeTransportError()
	}
	actual, err := b.Read(ctx, id)
	if err != nil || actual == nil || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) != owners[id] || !deploymentplan.Matches(r.Desired, actual) {
		return nil, activeTransportError()
	}
	roots := deploymentplan.String(deploymentplan.Map(r.Desired["data"])["roots.pem"])
	if strings.Count(roots, strings.TrimSpace(old["openbao"].CACert)) != 1 {
		return nil, activeTransportError()
	}
	desired := deploymentplan.Clone(r.Desired)
	deploymentplan.Map(desired["data"])["roots.pem"] = strings.Replace(roots, strings.TrimSpace(old["openbao"].CACert), strings.TrimSpace(current["openbao"].CACert), 1)
	result = append(result, activeTransportRenewalResource{ID: id, UID: owners[id], Before: r.Desired, Desired: desired})
	// Both native OpenBao clients build their CA pools at startup. Restart only
	// the admitted Controller and CertIssuer pod templates with this public hash.
	for _, name := range []string{"pki-controller", "certissuer"} {
		id := "Deployment/" + cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud/" + name
		r, ok := latest[id]
		if !ok {
			return nil, activeTransportError()
		}
		actual, err := b.Read(ctx, id)
		if err != nil || actual == nil || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) != owners[id] || !deploymentplan.Matches(r.Desired, actual) {
			return nil, activeTransportError()
		}
		desired := activeTransportConsumerRestart(r.Desired, current["openbao"].CACert)
		result = append(result, activeTransportRenewalResource{ID: id, UID: owners[id], Before: r.Desired, Desired: desired})
	}
	if len(result) != 13 {
		return nil, activeTransportError()
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func activeTransportReadMaterial(store secretStore, parent, candidate string) (map[string]environmentCreateTransport, map[string]environmentCreateTransport, *initialTransportRenewal, map[string]string, map[string]string, error) {
	bad := func() (map[string]environmentCreateTransport, map[string]environmentCreateTransport, *initialTransportRenewal, map[string]string, map[string]string, error) {
		return nil, nil, nil, nil, nil, activeTransportError()
	}
	old, legacy, err := readLegacyInitialTransportRenewal(store, parent)
	if err != nil || legacy == nil {
		return bad()
	}
	if filepath.Clean(candidate) != candidate || filepath.IsAbs(candidate) || !strings.HasPrefix(candidate, "deployment/repairs/") {
		return bad()
	}
	current := map[string]environmentCreateTransport{}
	hashes := map[string]string{}
	keys := map[string]string{}
	for path, sha := range legacy.Material {
		hashes[path] = sha
	}
	pointer := filepath.Join("deployment/create/material", parent, "transport-renewal.json")
	raw, err := activeTransportPrivateFile(store, pointer)
	if err != nil {
		return bad()
	}
	hashes[pointer] = environmentPreflightBytesSHA256(raw)
	for _, role := range []string{"management", "openbao"} {
		base := filepath.Join(candidate, role+"-transport")
		var value environmentCreateTransport
		for _, name := range []string{"identities.json", "ca.crt"} {
			rel := filepath.Join(base, name)
			raw, err := activeTransportPrivateFile(store, rel)
			if err != nil {
				return bad()
			}
			hashes[rel] = environmentPreflightBytesSHA256(raw)
			if name == "identities.json" && json.Unmarshal(raw, &value) != nil {
				return bad()
			}
			if name == "ca.crt" && string(raw) != value.CACert {
				return bad()
			}
		}
		rel := filepath.Join("deployment/create/transport-renewal", legacy.Review, role+"-transport/ca.key")
		key, err := activeTransportPrivateFile(store, rel)
		if err != nil || verifyActiveTransportSameKey(old[role], value, key, time.Now().UTC()) != nil {
			return bad()
		}
		hashes[rel] = environmentPreflightBytesSHA256(key)
		keys[role] = rel
		current[role] = value
	}
	for _, rel := range []string{"openbao/init.json", "openbao/root-token", "openbao/unseal-key", "pki/service/issuer.json", "pki/mqtt/issuer.json", "pki/openbao_tls/issuer.json", "pki/device/issuer.json", "pki/app/issuer.json"} {
		raw, err := activeTransportPrivateFile(store, rel)
		if err != nil {
			return bad()
		}
		hashes[rel] = environmentPreflightBytesSHA256(raw)
	}
	return old, current, legacy, hashes, keys, nil
}

func activeTransportResourceBody(r activeTransportRenewalResource, actual deploymentplan.Object) (deploymentplan.Object, error) {
	if actual == nil || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) != r.UID || !deploymentplan.Matches(r.Before, actual) && !deploymentplan.Matches(r.Desired, actual) {
		return nil, activeTransportError()
	}
	body := deploymentplan.Clone(actual)
	if r.Desired["kind"] == "Deployment" {
		template := deploymentplan.Map(deploymentplan.Map(r.Desired["spec"])["template"])
		desiredAnnotations := deploymentplan.Map(deploymentplan.Map(template["metadata"])["annotations"])
		hash := deploymentplan.String(desiredAnnotations["rtk.realtek.com/temporary-openbao-ca-sha256"])
		expected := activeTransportConsumerRestartHash(r.Before, hash)
		if !environmentPreflightDigest.MatchString(hash) || deploymentplan.Digest(expected) != deploymentplan.Digest(r.Desired) {
			return nil, activeTransportError()
		}
		liveTemplate := deploymentplan.Map(deploymentplan.Map(body["spec"])["template"])
		metadata := deploymentplan.Map(liveTemplate["metadata"])
		if metadata == nil {
			metadata = deploymentplan.Object{}
			liveTemplate["metadata"] = metadata
		}
		annotations := deploymentplan.Map(metadata["annotations"])
		if annotations == nil {
			annotations = deploymentplan.Object{}
			metadata["annotations"] = annotations
		}
		annotations["rtk.realtek.com/temporary-openbao-ca-sha256"] = hash
	} else {
		data := deploymentplan.Map(body["data"])
		if data == nil {
			data = deploymentplan.Object{}
			body["data"] = data
		}
		for key, value := range deploymentplan.Map(r.Desired["data"]) {
			data[key] = value
		}
	}
	delete(body, "status")
	return body, nil
}

func runEnvironmentCreateActiveTransportRenewal(args []string) error {
	return runEnvironmentCreateActiveTransportRenewalContext(context.Background(), args, os.Stdout, os.Stderr)
}
func runEnvironmentCreateActiveTransportRenewalContext(parentContext context.Context, args []string, out, diagnostics io.Writer) (resultErr error) {
	fs := flag.NewFlagSet("recover-create-active-transports", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	env := fs.String("environment", "", "selected dev environment")
	parent := fs.String("parent-plan-sha256", "", "original generation")
	workspaceOpt := fs.String("workspace", "", "selected workspace")
	candidate := fs.String("candidate", "", "canonical prepared same-key certificate directory")
	execute := fs.Bool("execute", false, "apply an exact fresh qualified repair")
	qualify := fs.Bool("qualify", false, "qualify the exact same-key repair without mutation (default)")
	review := fs.String("review-sha256", "", "exact qualified repair")
	confirm := fs.String("confirm", "", "exact selected stack")
	report := fs.String("report", "", "canonical private report")
	timeout := fs.Duration("timeout", 15*time.Minute, "bounded repair timeout")
	if fs.Parse(args) != nil || *qualify && *execute || fs.NArg() != 0 || *env != "dev" || !environmentPreflightDigest.MatchString(*parent) || *timeout <= 0 || *timeout > 30*time.Minute {
		return activeTransportError()
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	if *workspaceOpt != "" {
		workspace = *workspaceOpt
	}
	cfg, err := resolveDeploymentConfig(workspace, *env, "")
	if err != nil {
		return err
	}
	store, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return err
	}
	if recoveryMutationGuard(append([]string{"deployment", "recover-create-active-transports"}, args...)) != nil {
		return activeTransportError()
	}
	if *report != "" {
		if err := validateEnvironmentDeploymentReportPath(*env, *report); err != nil {
			return err
		}
	}
	ctx, cancel := context.WithTimeout(parentContext, *timeout)
	defer cancel()
	e, original, err := loadActiveTransportExecution(cfg, store, *parent)
	if err != nil {
		return err
	}
	defer e.Close()
	if deploymentplan.Digest(operator) != original.Plan.Plan.OperatorSHA256 {
		return activeTransportError()
	}
	if _, err := os.Lstat(filepath.Join(store.Root, "deployment/create/material", *parent, "active-transport-renewal.json")); err == nil {
		return createProviderRecoveryBlock("same-key active transport renewal already completed; continue the original deployment without replay")
	} else if !os.IsNotExist(err) {
		return err
	}
	kube, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || environmentPreflightBytesSHA256(kube) != original.Plan.KubeconfigSHA256 {
		return activeTransportError()
	}
	b, err := newEnvironmentKubernetesBackend(string(kube), *env)
	if err != nil {
		return err
	}
	b.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return err
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return err
	}
	var a activeTransportRenewalArtifact
	var source environmentCreateJournal
	if *execute {
		if !environmentPreflightDigest.MatchString(*review) {
			return activeTransportError()
		}
		base := filepath.Join(store.Root, "deployment/create/transport-renewal", *review)
		if readExecutionJSON(filepath.Join(base, "qualification.json"), &a) != nil || readExecutionJSON(filepath.Join(base, "source-journal.json"), &source) != nil || a.Schema != activeTransportRenewalSchema+"/artifact" || deploymentplan.Digest(a.Plan) != *review || a.Plan.Parent != *parent || a.Plan.OwnerSHA != deploymentplan.Digest(e.owner) || a.Plan.JournalSHA != deploymentplan.Digest(source) || source.OwnerToken != e.Journal.OwnerToken || a.Plan.ConfigurationSHA != deploymentplan.Digest(cfg) || a.Plan.CheckerSHA != checker || a.Plan.KubeconfigSHA != environmentPreflightBytesSHA256(kube) || *confirm != cfg.Values["CLOUD_STACK_NAME"] || a.Qualification.Decision != "GO" || a.Qualification.PlanSHA256 != *review || len(a.Qualification.Gates) != 5 || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.QualifiedAt.After(time.Now().UTC()) {
			return activeTransportError()
		}
		if len(e.Journal.Entries) < len(source.Entries) {
			return activeTransportError()
		}
		for i, entry := range source.Entries {
			if deploymentplan.Digest(entry) != deploymentplan.Digest(e.Journal.Entries[i]) {
				return activeTransportError()
			}
		}
		for _, entry := range e.Journal.Entries[len(source.Entries):] {
			if !strings.HasPrefix(entry.Key, "active-transport-renewal-"+*review) {
				return activeTransportError()
			}
		}
		// A started exact repair can be reconciled after qualification expiry only
		// through its original immutable admission; no fresh mutation is admitted.
		admitted := false
		for _, entry := range e.Journal.Entries {
			if entry.Key == "active-transport-renewal-"+*review && !entry.StartedAt.Before(a.QualifiedAt) && entry.StartedAt.Before(a.ValidUntil) && entry.IntentSHA256 == activeTransportJournalDigest(a.Plan) {
				admitted = true
			}
		}
		if !time.Now().Before(a.ValidUntil) && !admitted {
			return activeTransportError()
		}
		if len(e.Journal.Entries) > len(source.Entries) && !activeTransportNativeSourceCheckpoint(store, e) {
			return activeTransportError()
		}
		*candidate = a.Plan.Candidate
	} else {
		for _, entry := range e.Journal.Entries {
			if entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 || strings.HasPrefix(entry.Key, "kubernetes/transport-") && entry.Key != "kubernetes/transport-client-enrollment" && !strings.HasPrefix(entry.Key, "kubernetes/transport-client-enrollment-renewal-") {
				return activeTransportError()
			}
		}
		if !isManagedTransportCheckpoint(e.Journal) {
			return activeTransportError()
		}
		source = e.Journal
	}
	relative, err := activeTransportCandidateRelative(store, *candidate)
	if err != nil {
		return err
	}
	old, current, legacy, material, keys, err := activeTransportReadMaterial(store, *parent, relative)
	if err != nil {
		return err
	}
	p := createFoundationRecoveryPlan{Plan: original.Plan.Plan, Materials: map[string]string{}}
	crlMaterial, err := verifyManagedServiceRootCRLFreshness(ctx, cfg, store, e, b)
	if err != nil {
		return err
	}
	for path, sha := range crlMaterial {
		p.Materials["active-transport-service-root-crl-"+path] = sha
	}
	if err := verifyManagedTransportNativeRegistry(ctx, cfg, store, e, b, &p); err != nil {
		return err
	}
	if err := verifyActiveTransportMounted(ctx, cfg, b, old["openbao"], current["openbao"], *execute); err != nil {
		return err
	}
	pod, set, err := activeTransportOriginalIdentity(ctx, cfg, store, e, b)
	if err != nil {
		return err
	}
	if *execute {
		if a.Plan.PodUID != pod || a.Plan.SetUID != set || a.Plan.OriginalProofSHA != deploymentplan.Digest(legacy) || deploymentplan.Digest(a.Plan.AuthorityKeys) != deploymentplan.Digest(keys) {
			return activeTransportError()
		}
		for path, sha := range a.Plan.Material {
			if strings.HasPrefix(path, "evidence/") {
				if sha != p.Materials[strings.TrimPrefix(path, "evidence/")] {
					return activeTransportError()
				}
				continue
			}
			if material[path] != sha {
				return activeTransportError()
			}
		}
		for i, id := range activeTransportQualificationGates {
			g := a.Qualification.Gates[i]
			if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != *review {
				return activeTransportError()
			}
		}
		return executeActiveTransportRenewal(ctx, cfg, store, e, b, a, current, out)
	}
	resources, err := activeTransportOwners(ctx, cfg, store, e, b, old, current)
	if err != nil {
		return err
	}
	for path, sha := range p.Materials {
		material["evidence/"+path] = sha
	}
	plan := activeTransportRenewalPlan{Schema: activeTransportRenewalSchema, Parent: *parent, OwnerSHA: deploymentplan.Digest(e.owner), JournalSHA: deploymentplan.Digest(source), OriginalProofSHA: deploymentplan.Digest(legacy), ConfigurationSHA: deploymentplan.Digest(cfg), CheckerSHA: checker, KubeconfigSHA: environmentPreflightBytesSHA256(kube), Candidate: relative, Material: material, AuthorityKeys: keys, Resources: resources, PodUID: pod, SetUID: set}
	sha := deploymentplan.Digest(plan)
	// Verify every native write's admission before admitting any update.
	for _, r := range resources {
		actual, err := b.Read(ctx, r.ID)
		if err != nil {
			return err
		}
		body, err := activeTransportResourceBody(r, actual)
		if err != nil {
			return err
		}
		if _, err := b.Write(ctx, "update", body, true); err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	q := deploymentplan.Qualification{PlanSHA256: sha, Decision: "GO"}
	for _, id := range activeTransportQualificationGates {
		q.Gates = append(q.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: sha})
	}
	a = activeTransportRenewalArtifact{Schema: activeTransportRenewalSchema + "/artifact", Plan: plan, Qualification: q, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
	base := filepath.Join("deployment/create/transport-renewal", sha)
	if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(base, "source-journal.json"), mustProviderRecoveryJSON(source)); err != nil {
		return err
	}
	if err := store.write(filepath.Join(base, "qualification.json"), mustProviderRecoveryJSON(a), true); err != nil {
		return err
	}
	if *report != "" {
		if err := store.write(strings.TrimPrefix(*report, store.Root+string(os.PathSeparator)), mustProviderRecoveryJSON(a), true); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "GO environment=dev scope=same-key-active-temporary-tls review-sha256=%s resources=%d\n", sha, len(resources))
	return nil
}

func executeActiveTransportRenewal(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, a activeTransportRenewalArtifact, current map[string]environmentCreateTransport, out io.Writer) (resultErr error) {
	review := deploymentplan.Digest(a.Plan)
	key := "active-transport-renewal-" + review
	base := filepath.Join("deployment/create/transport-renewal", review)
	var parentEntry *environmentCreateJournalEntry
	for i := range e.Journal.Entries {
		if e.Journal.Entries[i].Key == key {
			parentEntry = &e.Journal.Entries[i]
		}
	}
	if parentEntry == nil {
		if err := e.BeginEntry(key, "recovery", mustProviderRecoveryJSON(a.Plan), time.Now().UTC()); err != nil {
			return err
		}
	} else if parentEntry.IntentSHA256 != activeTransportJournalDigest(a.Plan) {
		return activeTransportError()
	}
	leasePlan := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], Operation: "scoped", BindingsSHA256: review}}
	lease, err := b.AcquireExecution(ctx, leasePlan)
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := lease.Release(cleanup); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	if err := e.RecordKubernetesLease(lease.Owner()); err != nil {
		return err
	}
	proof := activeTransportRenewalProof{Schema: activeTransportRenewalSchema, Parent: a.Plan.Parent, Review: review, OriginalProofSHA: a.Plan.OriginalProofSHA, Material: map[string]string{}, AuthorityKeys: a.Plan.AuthorityKeys, Owners: map[string]string{}, PodUID: a.Plan.PodUID, SetUID: a.Plan.SetUID}
	for path, sha := range a.Plan.Material {
		if !strings.HasPrefix(path, "evidence/") {
			proof.Material[path] = sha
		}
	}
	for _, role := range []string{"management", "openbao"} {
		for name, bytes := range map[string][]byte{"identities.json": mustProviderRecoveryJSON(current[role]), "ca.crt": []byte(current[role].CACert)} {
			path := filepath.Join(base, role+"-transport", name)
			if err := writeEnvironmentCreateDerivedPrivateFile(store, path, bytes); err != nil {
				return err
			}
			proof.Material[path] = environmentPreflightBytesSHA256(bytes)
		}
	}
	for _, r := range a.Plan.Resources {
		if e.Check(ctx) != nil || lease.Check(ctx) != nil {
			return activeTransportError()
		}
		part := key + "-" + strings.ReplaceAll(r.ID, "/", "-")
		actual, err := b.Read(ctx, r.ID)
		if err != nil {
			return err
		}
		body, err := activeTransportResourceBody(r, actual)
		if err != nil {
			return createProviderRecoveryBlock("same-key TLS write is uncertain: preserve all repair and original material; restore only after exact UID and before/after hashes are reconciled")
		}
		var entry *environmentCreateJournalEntry
		for i := range e.Journal.Entries {
			if e.Journal.Entries[i].Key == part {
				entry = &e.Journal.Entries[i]
			}
		}
		intent := map[string]string{"resource": r.ID, "uid": r.UID, "before_sha256": deploymentplan.Digest(r.Before), "desired_sha256": deploymentplan.Digest(r.Desired)}
		if entry == nil {
			if !deploymentplan.Matches(r.Before, actual) {
				return activeTransportError()
			}
			if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(base, "original", strings.ReplaceAll(r.ID, "/", "-")+".json"), mustProviderRecoveryJSON(actual)); err != nil {
				return err
			}
			if err := e.BeginEntry(part, "recovery", mustProviderRecoveryJSON(intent), time.Now().UTC()); err != nil {
				return err
			}
		} else if entry.IntentSHA256 != deploymentplan.Digest(intent) {
			return activeTransportError()
		}
		if !deploymentplan.Matches(r.Desired, actual) {
			if entry != nil && entry.State != "pending" {
				return activeTransportError()
			}
			if _, err := b.Write(ctx, "update", body, true); err != nil {
				return err
			}
			if e.Check(ctx) != nil || lease.Check(ctx) != nil {
				return activeTransportError()
			}
			if _, err := b.Write(ctx, "update", body, false); err != nil {
				return err
			}
		}
		readback, err := b.Read(ctx, r.ID)
		if err != nil || readback == nil || deploymentplan.String(deploymentplan.Map(readback["metadata"])["uid"]) != r.UID || !deploymentplan.Matches(r.Desired, readback) {
			return activeTransportError()
		}
		proof.Owners[r.ID] = r.UID
		if entry == nil || entry.State == "pending" {
			if err := e.RecordReceipt(part, mustProviderRecoveryJSON(intent)); err != nil {
				return err
			}
		}
		if entry == nil || entry.State != "completed" {
			if err := e.CompleteEntry(part, time.Now().UTC()); err != nil {
				return err
			}
		}
	}
	// Verify Secret projection including the identical original serving key before
	// reloading the original PID. No restart, init, unseal or token write occurs.
	leaf := current["openbao"].Identities["openbao"]
	script := "set -eu; for n in $(seq 1 90); do cert=$(sha256sum /openbao/transport/tls.crt | cut -d' ' -f1); ca=$(sha256sum /openbao/transport/ca.crt | cut -d' ' -f1); key=$(sha256sum /openbao/transport/tls.key | cut -d' ' -f1); if [ \"$cert\" = \"" + environmentPreflightBytesSHA256([]byte(leaf.Certificate)) + "\" ] && [ \"$ca\" = \"" + environmentPreflightBytesSHA256([]byte(current["openbao"].CACert)) + "\" ] && [ \"$key\" = \"" + environmentPreflightBytesSHA256([]byte(leaf.Key)) + "\" ]; then kill -HUP 1; exit 0; fi; sleep 2; done; exit 1"
	if e.Check(ctx) != nil || lease.Check(ctx) != nil {
		return activeTransportError()
	}
	if _, err := environmentCreateOpenBaoExec(ctx, cfg, b, nil, script); err != nil {
		return err
	}
	if _, _, err := verifyInitializedOpenBaoIdentity(ctx, cfg, store, e, b); err != nil {
		return err
	}
	for _, r := range a.Plan.Resources {
		if r.Desired["kind"] == "Deployment" {
			if err := b.Wait(ctx, deploymentplan.Resource{ID: r.ID, Action: "update", Phase: "workload", Desired: r.Desired, UID: r.UID}); err != nil {
				return err
			}
			actual, err := b.Read(ctx, r.ID)
			if err != nil || actual == nil || deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]) != r.UID || !deploymentplan.Matches(r.Desired, actual) {
				return activeTransportError()
			}
		}
	}
	pod, set, err := activeTransportOriginalIdentity(ctx, cfg, store, e, b)
	if err != nil || pod != a.Plan.PodUID || set != a.Plan.SetUID {
		return activeTransportError()
	}
	p := createFoundationRecoveryPlan{Plan: environmentCreatePlan{Stack: cfg.Values["CLOUD_STACK_NAME"]}, Materials: map[string]string{}}
	crlMaterial, err := verifyManagedServiceRootCRLFreshness(ctx, cfg, store, e, b)
	if err != nil {
		return err
	}
	for path, sha := range crlMaterial {
		p.Materials["active-transport-service-root-crl-"+path] = sha
	}
	if err := verifyManagedTransportNativeRegistry(ctx, cfg, store, e, b, &p); err != nil {
		return err
	}
	for path, sha := range a.Plan.Material {
		if strings.HasPrefix(path, "evidence/") {
			if p.Materials[strings.TrimPrefix(path, "evidence/")] != sha {
				return activeTransportError()
			}
			continue
		}
		raw, err := activeTransportPrivateFile(store, path)
		if err != nil || environmentPreflightBytesSHA256(raw) != sha {
			return activeTransportError()
		}
	}
	proof.Completed = true
	if parentEntry != nil && parentEntry.State != "pending" && deploymentplan.Digest(json.RawMessage(parentEntry.Receipt)) != activeTransportJournalDigest(proof) {
		return activeTransportError()
	}
	if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(base, "completed.json"), mustProviderRecoveryJSON(proof)); err != nil {
		return err
	}
	if parentEntry == nil || parentEntry.State == "pending" {
		if err := e.RecordReceipt(key, mustProviderRecoveryJSON(proof)); err != nil {
			return err
		}
	}
	if parentEntry == nil || parentEntry.State != "completed" {
		if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
			return err
		}
	}
	if err := store.write(filepath.Join("deployment/create/material", proof.Parent, "active-transport-renewal.json"), mustProviderRecoveryJSON(proof), false); err != nil {
		return err
	}
	fmt.Fprintf(out, "PASS environment=dev scope=same-key-active-temporary-tls review-sha256=%s original-openbao-preserved=true managed-issuers-preserved=true\n", review)
	return nil
}

func activeTransportConsumerRestart(original deploymentplan.Object, ca string) deploymentplan.Object {
	return activeTransportConsumerRestartHash(original, environmentPreflightBytesSHA256([]byte(ca)))
}

func activeTransportConsumerRestartHash(original deploymentplan.Object, hash string) deploymentplan.Object {
	out := deploymentplan.Clone(original)
	template := deploymentplan.Map(deploymentplan.Map(out["spec"])["template"])
	metadata := deploymentplan.Map(template["metadata"])
	annotations := deploymentplan.Map(metadata["annotations"])
	if annotations == nil {
		annotations = deploymentplan.Object{}
		metadata["annotations"] = annotations
	}
	annotations["rtk.realtek.com/temporary-openbao-ca-sha256"] = hash
	return out
}

func verifyActiveTransportMounted(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend, old, current environmentCreateTransport, reconciling bool) error {
	raw, err := environmentCreateOpenBaoExec(ctx, cfg, b, nil, "sha256sum /openbao/transport/tls.crt /openbao/transport/ca.crt /openbao/transport/tls.key")
	if err != nil {
		return err
	}
	fields := strings.Fields(string(raw))
	if len(fields) != 6 || fields[1] != "/openbao/transport/tls.crt" || fields[3] != "/openbao/transport/ca.crt" || fields[5] != "/openbao/transport/tls.key" {
		return activeTransportError()
	}
	key := environmentPreflightBytesSHA256([]byte(old.Identities["openbao"].Key))
	if key != environmentPreflightBytesSHA256([]byte(current.Identities["openbao"].Key)) || fields[4] != key {
		return activeTransportError()
	}
	matches := func(t environmentCreateTransport) bool {
		return fields[0] == environmentPreflightBytesSHA256([]byte(t.Identities["openbao"].Certificate)) && fields[2] == environmentPreflightBytesSHA256([]byte(t.CACert))
	}
	if !matches(old) && (!reconciling || !matches(current)) {
		return activeTransportError()
	}
	return nil
}

// This permits registry reads during an already-admitted repair only. It binds
// the immutable original checkpoint and every durable resource intent; it does
// not permit normal deployment to consume an incomplete pointer.
func activeTransportNativeSourceCheckpoint(store secretStore, e *environmentCreateExecution) bool {
	key, review := "", ""
	for _, entry := range e.Journal.Entries {
		const prefix = "active-transport-renewal-"
		suffix := strings.TrimPrefix(entry.Key, prefix)
		if strings.HasPrefix(entry.Key, prefix) && environmentPreflightDigest.MatchString(suffix) {
			key, review = entry.Key, suffix
		}
	}
	if review == "" {
		return false
	}
	var a activeTransportRenewalArtifact
	var before environmentCreateJournal
	base := filepath.Join(store.Root, "deployment/create/transport-renewal", review)
	if readExecutionJSON(filepath.Join(base, "qualification.json"), &a) != nil || readExecutionJSON(filepath.Join(base, "source-journal.json"), &before) != nil || a.Schema != activeTransportRenewalSchema+"/artifact" || deploymentplan.Digest(a.Plan) != review || a.Plan.Parent != e.Journal.PlanSHA256 || a.Plan.OwnerSHA != deploymentplan.Digest(e.owner) || a.Plan.JournalSHA != deploymentplan.Digest(before) || before.OwnerToken != e.Journal.OwnerToken || !isManagedTransportCheckpoint(before) || a.Qualification.Decision != "GO" || a.Qualification.PlanSHA256 != review || len(a.Qualification.Gates) != 5 || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || len(e.Journal.Entries) <= len(before.Entries) {
		return false
	}
	for i, id := range activeTransportQualificationGates {
		g := a.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != review {
			return false
		}
	}
	for i, entry := range before.Entries {
		if deploymentplan.Digest(entry) != deploymentplan.Digest(e.Journal.Entries[i]) {
			return false
		}
	}
	expected := map[string]string{key: activeTransportJournalDigest(a.Plan)}
	for _, r := range a.Plan.Resources {
		intent := map[string]string{"resource": r.ID, "uid": r.UID, "before_sha256": deploymentplan.Digest(r.Before), "desired_sha256": deploymentplan.Digest(r.Desired)}
		expected[key+"-"+strings.ReplaceAll(r.ID, "/", "-")] = deploymentplan.Digest(intent)
	}
	first := e.Journal.Entries[len(before.Entries)]
	if first.Key != key || first.StartedAt.Before(a.QualifiedAt) || !first.StartedAt.Before(a.ValidUntil) || a.QualifiedAt.After(time.Now().UTC()) || first.StartedAt.After(time.Now().UTC()) {
		return false
	}
	seen := map[string]bool{}
	for _, entry := range e.Journal.Entries[len(before.Entries):] {
		if seen[entry.Key] || expected[entry.Key] == "" || expected[entry.Key] != entry.IntentSHA256 || entry.Phase != "recovery" || entry.StartedAt.Before(first.StartedAt) || entry.StartedAt.After(time.Now().UTC()) {
			return false
		}
		seen[entry.Key] = true
		switch entry.State {
		case "pending":
			if len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero() {
				return false
			}
		case "allocated", "completed":
			if len(entry.Receipt) == 0 || entry.State == "completed" && (entry.CompletedAt.IsZero() || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.After(time.Now().UTC())) {
				return false
			}
			if entry.Key != key && deploymentplan.Digest(json.RawMessage(entry.Receipt)) != expected[entry.Key] {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Normal continuation overlays only the exact admitted completed repair bodies,
// with unchanged resource UIDs. All private material and parent receipts have
// already been authenticated by the effective reader.
func activeTransportRenewalOverrides(store secretStore, parent string) (map[string]deploymentplan.Resource, error) {
	_, effective, err := readInitialTransportRenewal(store, parent)
	if err != nil {
		return nil, err
	}
	out := map[string]deploymentplan.Resource{}
	if effective == nil || effective.Schema != activeTransportRenewalSchema {
		return out, nil
	}
	var artifact activeTransportRenewalArtifact
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create/transport-renewal", effective.Review, "qualification.json"), &artifact) != nil || deploymentplan.Digest(artifact.Plan) != effective.Review {
		return nil, activeTransportError()
	}
	for _, r := range artifact.Plan.Resources {
		if effective.Owners[r.ID] != r.UID {
			return nil, activeTransportError()
		}
		out[r.ID] = deploymentplan.Resource{ID: r.ID, UID: r.UID, Action: "update", Desired: r.Desired}
	}
	return out, nil
}

func activeTransportEvidenceCount(material map[string]string) int {
	n := 0
	for path := range material {
		if strings.HasPrefix(path, "evidence/") {
			n++
		}
	}
	return n
}

func activeTransportJournalDigest(value any) string {
	sha, err := environmentCreatePlanSHA256(mustProviderRecoveryJSON(value))
	if err != nil {
		return ""
	}
	return sha
}

func activeTransportPrivateFile(store secretStore, relative string) ([]byte, error) {
	if filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
		return nil, activeTransportError()
	}
	path, err := store.safePath(relative)
	if err != nil {
		return nil, activeTransportError()
	}
	if validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
		return nil, activeTransportError()
	}
	return readBoundedEnvironmentFile(path, true)
}

// Executions consume the canonical relative candidate stored in qualification;
// only the qualifier's absolute input needs conversion against the store root.
func activeTransportCandidateRelative(store secretStore, candidate string) (string, error) {
	relative := candidate
	if filepath.IsAbs(candidate) {
		var err error
		relative, err = filepath.Rel(store.Root, candidate)
		if err != nil {
			return "", activeTransportError()
		}
	}
	if filepath.IsAbs(relative) || filepath.Clean(relative) != relative || !strings.HasPrefix(relative, "deployment/repairs/") {
		return "", activeTransportError()
	}
	return relative, nil
}
