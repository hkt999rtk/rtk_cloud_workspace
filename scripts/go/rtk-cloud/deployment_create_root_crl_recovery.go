package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const createRootCRLRecoverySchema = "rtk-cloud.create-service-root-crl-recovery/v1"

type createRootCRLRecord struct {
	IssuerID   string    `json:"issuer_id"`
	Digest     string    `json:"crl_sha256"`
	Number     string    `json:"crl_number"`
	PEM        string    `json:"crl_pem"`
	ThisUpdate time.Time `json:"this_update"`
	NextUpdate time.Time `json:"next_update"`
}
type createRootCRLRevocation struct {
	Serial    string    `json:"serial_hex"`
	RevokedAt time.Time `json:"revoked_at"`
	Reason    int       `json:"reason_code"`
}

// The field order is the native pkiceremony.CRLRequest digest contract.
type createRootCRLRequest struct {
	IssuerID    string                    `json:"issuer_id"`
	Fingerprint string                    `json:"issuer_fingerprint_sha256"`
	Number      string                    `json:"crl_number"`
	ThisUpdate  time.Time                 `json:"this_update"`
	NextUpdate  time.Time                 `json:"next_update"`
	Entries     []createRootCRLRevocation `json:"revocations"`
}
type createRootCRLNativeSnapshot struct {
	Prior        createRootCRLRecord       `json:"prior"`
	Bindings     []createRootCRLRevocation `json:"bindings"`
	Certificates []struct {
		PEM       string    `json:"certificate_pem"`
		RevokedAt time.Time `json:"revoked_at"`
	} `json:"certificates"`
}
type createRootCRLStateTarget struct {
	ClaimID   string                `json:"claim_id"`
	ClaimUID  string                `json:"claim_uid"`
	PodName   string                `json:"pod_name"`
	PodUID    string                `json:"pod_uid"`
	ID        string                `json:"resource_id"`
	UID       string                `json:"uid"`
	Container string                `json:"container"`
	Desired   deploymentplan.Object `json:"desired"`
	Paths     []string              `json:"paths"`
	StateSHA  map[string]string     `json:"state_sha256"`
}
type createRootCRLRecoveryPlan struct {
	Schema           string                            `json:"schema"`
	Parent           string                            `json:"parent_plan_sha256"`
	OwnerSHA         string                            `json:"parent_owner_sha256"`
	JournalSHA       string                            `json:"parent_journal_sha256"`
	CheckerSHA       string                            `json:"checker_sha256"`
	ConfigurationSHA string                            `json:"configuration_sha256"`
	KubeconfigSHA    string                            `json:"kubeconfig_sha256"`
	Image            string                            `json:"image"`
	Candidate        string                            `json:"candidate_relative_path"`
	Root             environmentCreateRegisteredIssuer `json:"original_root"`
	Snapshot         createRootCRLNativeSnapshot       `json:"native_snapshot"`
	SnapshotSHA      string                            `json:"native_snapshot_sha256"`
	Request          createRootCRLRequest              `json:"request"`
	Signed           createRootCRLRecord               `json:"signed"`
	Material         map[string]string                 `json:"material_sha256"`
	Secret           activeTransportRenewalResource    `json:"secret"`
	Targets          []createRootCRLStateTarget        `json:"state_targets"`
}
type createRootCRLRecoveryArtifact struct {
	Schema        string                       `json:"schema"`
	Plan          createRootCRLRecoveryPlan    `json:"plan"`
	Qualification deploymentplan.Qualification `json:"qualification"`
	QualifiedAt   time.Time                    `json:"qualified_at"`
	ValidUntil    time.Time                    `json:"valid_until"`
}
type createRootCRLRecoveryProof struct {
	Schema    string `json:"schema"`
	Parent    string `json:"parent_plan_sha256"`
	Review    string `json:"review_sha256"`
	File      string `json:"crl_file"`
	Digest    string `json:"crl_sha256"`
	Completed bool   `json:"completed"`
}

var createRootCRLGates = []string{"deployment.root-crl.original-authority", "deployment.root-crl.live-revocations", "deployment.root-crl.native-signature", "deployment.root-crl.native-dry-run", "deployment.root-crl.original-owners", "deployment.root-crl.execution-lease"}

func rootCRLRecoveryError() error {
	return createProviderRecoveryBlock("original Service Root CRL repair requires its pinned ACTIVE authority, unchanged offline custody, complete live revocations, native signing/import and exact original consumers")
}

func parseCreateRootCRL(root environmentCreateRegisteredIssuer, record createRootCRLRecord, fresh bool, now time.Time) (*x509.RevocationList, error) {
	return parseCreateRootCRLForDomain(root, record, "service", fresh, now)
}

func parseCreateRootCRLForDomain(root environmentCreateRegisteredIssuer, record createRootCRLRecord, expectedDomain string, fresh bool, now time.Time) (*x509.RevocationList, error) {
	block, rest := pem.Decode([]byte(record.PEM))
	cert, err := parseFirstCertificatePEM([]byte(root.Certificate))
	if root.Environment != "dev" || (expectedDomain != "service" && expectedDomain != "mqtt" && expectedDomain != "openbao_tls") || root.Domain != expectedDomain || root.Kind != "root" || root.Status != "active" || root.Document["signer_provider"] != "offline" || err != nil || cert.CheckSignatureFrom(cert) != nil || environmentPreflightBytesSHA256(cert.Raw) != root.RootFingerprint {
		return nil, rootCRLRecoveryError()
	}
	if block == nil || block.Type != "X509 CRL" || len(block.Headers) != 0 || len(bytes.TrimSpace(rest)) != 0 || err != nil {
		return nil, rootCRLRecoveryError()
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil || crl.CheckSignatureFrom(cert) != nil || !bytes.Equal(crl.RawIssuer, cert.RawSubject) || !bytes.Equal(crl.AuthorityKeyId, cert.SubjectKeyId) || len(cert.SubjectKeyId) == 0 || record.IssuerID != root.ID || record.Digest != environmentPreflightBytesSHA256(crl.Raw) || crl.Number == nil || crl.Number.Sign() <= 0 || crl.Number.BitLen() > 159 || record.Number != crl.Number.String() || !record.ThisUpdate.Equal(crl.ThisUpdate) || !record.NextUpdate.Equal(crl.NextUpdate) {
		return nil, rootCRLRecoveryError()
	}
	if fresh && (crl.ThisUpdate.After(now) || crl.ThisUpdate.Before(now.Add(-7*24*time.Hour)) || !crl.NextUpdate.After(now.Add(45*time.Minute)) || crl.NextUpdate.After(crl.ThisUpdate.Add(7*24*time.Hour)) || crl.NextUpdate.After(cert.NotAfter) || now.Before(cert.NotBefore)) {
		return nil, rootCRLRecoveryError()
	}
	for _, ext := range crl.Extensions {
		if ext.Id.String() != "2.5.29.20" && ext.Id.String() != "2.5.29.35" {
			return nil, rootCRLRecoveryError()
		}
	}
	seen := map[string]bool{}
	for _, entry := range crl.RevokedCertificateEntries {
		if entry.SerialNumber == nil || entry.SerialNumber.Sign() <= 0 || entry.RevocationTime.IsZero() || entry.RevocationTime.After(crl.ThisUpdate) || entry.ReasonCode < 0 || entry.ReasonCode > 10 || entry.ReasonCode == 7 || entry.ReasonCode == 8 || seen[entry.SerialNumber.Text(16)] {
			return nil, rootCRLRecoveryError()
		}
		seen[entry.SerialNumber.Text(16)] = true
		for _, ext := range entry.Extensions {
			if ext.Id.String() != "2.5.29.21" {
				return nil, rootCRLRecoveryError()
			}
		}
	}
	return crl, nil
}

func buildCreateRootCRLRequest(root environmentCreateRegisteredIssuer, snapshot createRootCRLNativeSnapshot, now time.Time) (createRootCRLRequest, error) {
	return buildCreateRootCRLRequestForDomain(root, snapshot, "service", now)
}

func buildCreateRootCRLRequestForDomain(root environmentCreateRegisteredIssuer, snapshot createRootCRLNativeSnapshot, expectedDomain string, now time.Time) (createRootCRLRequest, error) {
	var out createRootCRLRequest
	prior, err := parseCreateRootCRLForDomain(root, snapshot.Prior, expectedDomain, false, now)
	if err != nil {
		return out, err
	}
	cert, err := parseFirstCertificatePEM([]byte(root.Certificate))
	if err != nil {
		return out, err
	}
	now = now.UTC().Truncate(time.Second)
	entries := map[string]createRootCRLRevocation{}
	for _, entry := range prior.RevokedCertificateEntries {
		entries[entry.SerialNumber.Text(16)] = createRootCRLRevocation{entry.SerialNumber.Text(16), entry.RevocationTime, entry.ReasonCode}
	}
	include := func(entry createRootCRLRevocation) error {
		serial, ok := new(big.Int).SetString(entry.Serial, 16)
		entry.RevokedAt = entry.RevokedAt.UTC().Truncate(time.Second)
		if !ok || serial.Sign() <= 0 || serial.Text(16) != entry.Serial || entry.RevokedAt.IsZero() || entry.RevokedAt.After(now) || entry.Reason < 0 || entry.Reason > 10 || entry.Reason == 7 || entry.Reason == 8 {
			return rootCRLRecoveryError()
		}
		if old, ok := entries[entry.Serial]; ok {
			// Native unspecified reason does not remove an already published reason.
			if !old.RevokedAt.Equal(entry.RevokedAt) || entry.Reason != 0 && old.Reason != entry.Reason {
				return rootCRLRecoveryError()
			}
			return nil
		}
		entries[entry.Serial] = entry
		return nil
	}
	for _, entry := range snapshot.Bindings {
		if err := include(entry); err != nil {
			return out, err
		}
	}
	for _, entry := range snapshot.Certificates {
		leaf, err := parseFirstCertificatePEM([]byte(entry.PEM))
		if err != nil || leaf.CheckSignatureFrom(cert) != nil {
			return out, rootCRLRecoveryError()
		}
		if err := include(createRootCRLRevocation{Serial: leaf.SerialNumber.Text(16), RevokedAt: entry.RevokedAt}); err != nil {
			return out, err
		}
	}
	serials := make([]string, 0, len(entries))
	for serial := range entries {
		serials = append(serials, serial)
	}
	slices.Sort(serials)
	revoked := make([]createRootCRLRevocation, 0, len(serials))
	for _, serial := range serials {
		revoked = append(revoked, entries[serial])
	}
	next := now.Add(6 * 24 * time.Hour)
	if next.After(cert.NotAfter) {
		return out, rootCRLRecoveryError()
	}
	out = createRootCRLRequest{root.ID, root.RootFingerprint, new(big.Int).Add(prior.Number, big.NewInt(1)).String(), now, next, revoked}
	return out, nil
}

func validateCreateRootCRLSigned(root environmentCreateRegisteredIssuer, snapshot createRootCRLNativeSnapshot, request createRootCRLRequest, raw []byte, now time.Time) (createRootCRLRecord, error) {
	return validateCreateRootCRLSignedForDomain(root, snapshot, request, raw, "service", now)
}

func validateCreateRootCRLSignedForDomain(root environmentCreateRegisteredIssuer, snapshot createRootCRLNativeSnapshot, request createRootCRLRequest, raw []byte, expectedDomain string, now time.Time) (createRootCRLRecord, error) {
	var record createRootCRLRecord
	block, rest := pem.Decode(raw)
	if block == nil || len(bytes.TrimSpace(rest)) != 0 {
		return record, rootCRLRecoveryError()
	}
	crl, err := x509.ParseRevocationList(block.Bytes)
	if err != nil || crl.Number == nil {
		return record, rootCRLRecoveryError()
	}
	record = createRootCRLRecord{root.ID, environmentPreflightBytesSHA256(crl.Raw), crl.Number.String(), string(raw), crl.ThisUpdate, crl.NextUpdate}
	if _, err = parseCreateRootCRLForDomain(root, record, expectedDomain, true, now); err != nil {
		return record, err
	}
	if request.IssuerID != root.ID || request.Fingerprint != root.RootFingerprint || record.Number != request.Number || !record.ThisUpdate.Equal(request.ThisUpdate) || !record.NextUpdate.Equal(request.NextUpdate) || len(crl.RevokedCertificateEntries) != len(request.Entries) {
		return record, rootCRLRecoveryError()
	}
	wanted := map[string]createRootCRLRevocation{}
	for _, entry := range request.Entries {
		if _, ok := wanted[entry.Serial]; ok {
			return record, rootCRLRecoveryError()
		}
		wanted[entry.Serial] = entry
	}
	for _, entry := range crl.RevokedCertificateEntries {
		reviewed, ok := wanted[entry.SerialNumber.Text(16)]
		if !ok || !entry.RevocationTime.Equal(reviewed.RevokedAt) || entry.ReasonCode != reviewed.Reason {
			return record, rootCRLRecoveryError()
		}
		delete(wanted, entry.SerialNumber.Text(16))
	}
	prior, err := parseCreateRootCRLForDomain(root, snapshot.Prior, expectedDomain, false, now)
	if err != nil || crl.Number.Cmp(prior.Number) <= 0 || crl.ThisUpdate.Before(prior.ThisUpdate) {
		return record, rootCRLRecoveryError()
	}
	for _, old := range prior.RevokedCertificateEntries {
		found := false
		for _, entry := range crl.RevokedCertificateEntries {
			if entry.SerialNumber.Cmp(old.SerialNumber) == 0 && entry.RevocationTime.Equal(old.RevocationTime) && entry.ReasonCode == old.ReasonCode {
				found = true
			}
		}
		if !found {
			return record, rootCRLRecoveryError()
		}
	}
	return record, nil
}

func readCreateRootCRLNativeSnapshot(ctx context.Context, cfg deploymentConfig, e *environmentCreateExecution, b environmentKubernetesBackend, root string) (createRootCRLNativeSnapshot, []byte, error) {
	var snapshot createRootCRLNativeSnapshot
	if !environmentBootstrapOwnerUUID.MatchString(root) {
		return snapshot, nil, rootCRLRecoveryError()
	}
	query := "SELECT json_build_object('prior',(SELECT json_build_object('issuer_id',issuer_id,'crl_sha256',digest,'crl_number',number::text,'crl_pem',pem,'this_update',this_update,'next_update',next_update) FROM pki_crls WHERE issuer_id='" + root + "' ORDER BY number DESC LIMIT 1),'bindings',COALESCE((SELECT json_agg(json_build_object('serial_hex',serial,'revoked_at',revoked_at,'reason_code',0) ORDER BY serial) FROM pki_certificate_bindings WHERE issuer_id='" + root + "' AND revoked_at IS NOT NULL),'[]'::json),'certificates',COALESCE((SELECT json_agg(json_build_object('certificate_pem',certificate_pem,'revoked_at',revoked_at) ORDER BY certificate_pem) FROM (SELECT certificate_pem,revoked_at FROM pki_service_client_issuances WHERE issuer_id='" + root + "' AND revoked_at IS NOT NULL UNION ALL SELECT certificate_pem,revoked_at FROM pki_server_issuances WHERE issuer_id='" + root + "' AND revoked_at IS NOT NULL UNION ALL SELECT certificate_pem,revoked_at FROM pki_app_issuances WHERE issuer_id='" + root + "' AND revoked_at IS NOT NULL UNION ALL SELECT i.document->>'certificate_pem',(SELECT min(a.created_at) FROM pki_audit a WHERE a.issuer_id=i.id AND a.event IN ('issuer_revoked','issuer_compromised')) FROM pki_issuers i WHERE i.parent_id='" + root + "' AND i.status IN ('revoked','compromised')) revoked),'[]'::json))"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b}, query)
	if err != nil || json.Unmarshal(raw, &snapshot) != nil {
		return snapshot, nil, rootCRLRecoveryError()
	}
	return snapshot, raw, nil
}

func loadCreateRootCRLStateTargets(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, root environmentCreateRegisteredIssuer, prior createRootCRLRecord, image string) ([]createRootCRLStateTarget, error) {
	ns := cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud"
	originals, owners, err := loadCreateRootCRLCoreOwners(store, e)
	if err != nil {
		return nil, err
	}
	// An already completed temporary transport renewal is a separately pinned
	// owner update, never permission to accept an arbitrary current Deployment.
	overrides, err := activeTransportRenewalOverrides(store, e.Journal.PlanSHA256)
	if err != nil {
		return nil, err
	}
	for id, resource := range overrides {
		if _, ok := originals[id]; ok {
			if owners[id] != resource.UID {
				return nil, rootCRLRecoveryError()
			}
			originals[id] = resource
		}
	}
	cm, err := b.Read(ctx, "ConfigMap/"+ns+"/pki-create-service-trust")
	if err != nil || cm == nil {
		return nil, rootCRLRecoveryError()
	}
	var paths []string
	for _, key := range []string{"listener-crls.json", "outbound-crls.json"} {
		var manifest []struct {
			Issuer    map[string]any `json:"issuer"`
			StatePath string         `json:"state_path"`
		}
		if json.Unmarshal([]byte(deploymentplan.String(deploymentplan.Map(cm["data"])[key])), &manifest) != nil {
			return nil, rootCRLRecoveryError()
		}
		found := false
		for _, entry := range manifest {
			if entry.Issuer["issuer_id"] != root.ID {
				continue
			}
			if found || deploymentplan.Digest(entry.Issuer) != deploymentplan.Digest(root.Document) || entry.StatePath != environmentCreateServiceState+"/private/crl/"+strings.TrimSuffix(key, "-crls.json")+"-root.json" {
				return nil, rootCRLRecoveryError()
			}
			found = true
			paths = append(paths, entry.StatePath)
		}
		if !found {
			return nil, rootCRLRecoveryError()
		}
	}
	core, coreJournal, err := acceptedCreateStage(store, e, "kubernetes/service-core-enrollment")
	if err != nil {
		return nil, err
	}
	live, err := b.Inventory(ctx, cfg.Values["CLOUD_STACK_NAME"])
	if err != nil {
		return nil, err
	}
	liveByID := map[string]deploymentplan.Object{}
	for _, object := range live {
		liveByID[deploymentplan.ID(object)] = object
	}
	retained := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"]}}
	for _, resource := range originals {
		retained.Resources = append(retained.Resources, resource)
	}
	if err := verifyOriginalMigrationCheckpointInventory(ctx, store, retained, deploymentplan.Journal{Owners: owners}, live); err != nil {
		return nil, err
	}
	var targets []createRootCRLStateTarget
	for _, name := range []string{"pki-controller", "certissuer"} {
		id := "Deployment/" + ns + "/" + name
		actual, err := b.Read(ctx, id)
		expected := originals[id]
		if err != nil || actual == nil || owners[id] == "" || deploymentplan.Map(actual["metadata"])["uid"] != owners[id] || !deploymentplan.Matches(expected.Desired, actual) {
			return nil, rootCRLRecoveryError()
		}
		containers := certIssuerObjectList(environmentPKIPod(actual)["containers"])
		if len(containers) != 1 || deploymentplan.Map(containers[0])["name"] != name || deploymentplan.Map(containers[0])["image"] != image {
			return nil, rootCRLRecoveryError()
		}
		target := createRootCRLStateTarget{ID: id, UID: owners[id], Container: name, Desired: expected.Desired, Paths: slices.Clone(paths), StateSHA: map[string]string{}}
		target.ClaimID = "PersistentVolumeClaim/" + ns + "/" + environmentCreateServiceOwnerClaim(name)
		for _, resource := range core.Resources {
			if resource.ID == target.ClaimID {
				claim := liveByID[resource.ID]
				target.ClaimUID = coreJournal.Owners[resource.ID]
				if claim == nil || target.ClaimUID == "" || deploymentplan.Map(claim["metadata"])["uid"] != target.ClaimUID || !deploymentplan.Matches(resource.Desired, claim) {
					return nil, rootCRLRecoveryError()
				}
			}
		}
		if target.ClaimUID == "" {
			return nil, rootCRLRecoveryError()
		}
		for _, pod := range live {
			if pod["kind"] == "Pod" {
				controller, err := environmentPodController(pod, liveByID)
				if err == nil && controller == id {
					if target.PodUID != "" {
						return nil, rootCRLRecoveryError()
					}
					target.PodName = deploymentplan.String(deploymentplan.Map(pod["metadata"])["name"])
					target.PodUID = deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"])
				}
			}
		}
		if target.PodUID == "" || target.PodName == "" {
			return nil, rootCRLRecoveryError()
		}
		for _, path := range paths {
			raw, err := b.command(ctx, nil, "--namespace", ns, "exec", "pod/"+target.PodName, "--container", name, "--", "/bin/sh", "-ec", `test -f "$1" && test ! -L "$1" && test "$(stat -c %a "$1")" = 600 && test "$(stat -c %u "$1")" = 10001 && cat "$1"`, "crl-state", path)
			var state struct {
				Fingerprint string              `json:"issuer_fingerprint"`
				CRL         createRootCRLRecord `json:"crl"`
				Terminal    string              `json:"terminal_issuer_id"`
			}
			if err != nil || json.Unmarshal(raw, &state) != nil || state.Fingerprint != root.RootFingerprint || state.Terminal != "" || deploymentplan.Digest(state.CRL) != deploymentplan.Digest(prior) {
				return nil, rootCRLRecoveryError()
			}
			target.StateSHA[path] = environmentPreflightBytesSHA256(raw)
		}
		targets = append(targets, target)
	}
	return targets, nil
}

func runNativeCreateRootCRLImport(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend, plan createRootCRLRecoveryPlan, execute bool) ([]byte, error) {
	manifest, _ := json.Marshal(plan.Request)
	input := []byte(base64.StdEncoding.EncodeToString(manifest) + "\n" + base64.StdEncoding.EncodeToString([]byte(plan.Signed.PEM)) + "\n")
	script := `set -eu
umask 077
rtk_crl_tmp=$(mktemp -d /tmp/rtk-root-crl.XXXXXXXX)
trap 'rm -rf "$rtk_crl_tmp"' EXIT
IFS= read -r rtk_crl_request
IFS= read -r rtk_crl_signed
printf '%s' "$rtk_crl_request" | base64 -d > "$rtk_crl_tmp/request.json"
printf '%s' "$rtk_crl_signed" | base64 -d > "$rtk_crl_tmp/revocations.pem"
/app/pkicontroller recovery-import-dev-root-crl "$1" "$2" "$rtk_crl_tmp/request.json" "$3" "$rtk_crl_tmp/revocations.pem"`
	if execute {
		script += " --confirm dev"
	}
	podName := ""
	for _, target := range plan.Targets {
		if target.Container == "pki-controller" {
			podName = target.PodName
		}
	}
	if podName == "" {
		return nil, rootCRLRecoveryError()
	}
	raw, err := b.command(ctx, input, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud", "exec", "-i", "pod/"+podName, "--container", "pki-controller", "--", "/bin/sh", "-ec", script, "reviewed-root-crl", plan.Root.ID, plan.Root.RootFingerprint, deploymentplan.Digest(plan.Request))
	if err != nil {
		return nil, rootCRLRecoveryError()
	}
	return raw, nil
}

func installCreateRootCRLState(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend, plan createRootCRLRecoveryPlan, target createRootCRLStateTarget) ([]byte, error) {
	issuer, _ := json.Marshal(plan.Root.Document)
	record, _ := json.Marshal(plan.Signed)
	input := []byte(base64.StdEncoding.EncodeToString(issuer) + "\n" + base64.StdEncoding.EncodeToString(record) + "\n")
	script := `set -eu
umask 077
rtk_crl_tmp=$(mktemp -d /tmp/rtk-root-crl-state.XXXXXXXX)
trap 'rm -rf "$rtk_crl_tmp"' EXIT
IFS= read -r rtk_crl_issuer
IFS= read -r rtk_crl_record
printf '%s' "$rtk_crl_issuer" | base64 -d > "$rtk_crl_tmp/issuer.json"
printf '%s' "$rtk_crl_record" | base64 -d > "$rtk_crl_tmp/crl.json"
/app/pkitrust apply-crl "$rtk_crl_tmp/issuer.json" "$rtk_crl_tmp/crl.json" "$1"
/app/pkitrust apply-crl "$rtk_crl_tmp/issuer.json" "$rtk_crl_tmp/crl.json" "$2"`
	raw, err := b.command(ctx, input, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud", "exec", "-i", "pod/"+target.PodName, "--container", target.Container, "--", "/bin/sh", "-ec", script, "reviewed-root-crl-state", target.Paths[0], target.Paths[1])
	if err != nil {
		return nil, rootCRLRecoveryError()
	}
	return raw, nil
}

func createRootCRLMaterial(store secretStore, parent string, root environmentCreateRegisteredIssuer) (map[string]string, error) {
	paths := []string{filepath.Join("pki/custody/roots", root.ID, "manifest.json"), filepath.Join("pki/custody/roots", root.ID, "ca-key.encrypted.pem"), filepath.Join("pki/custody/roots", root.ID, "certificate.pem"), filepath.Join("pki/custody/roots", root.ID, "csr.pem"), "pki/custody/service-root-v1-key-passphrase", filepath.Join("deployment/create", parent, "bootstrap/service/root-active.json")}
	hashes := map[string]string{}
	for _, path := range paths {
		raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, path), true)
		if err != nil {
			return nil, rootCRLRecoveryError()
		}
		hashes[filepath.ToSlash(path)] = environmentPreflightBytesSHA256(raw)
	}
	return hashes, nil
}

func runEnvironmentCreateRootCRLRecovery(args []string) error {
	return runEnvironmentCreateRootCRLRecoveryContext(context.Background(), args, os.Stdout, os.Stderr)
}
func runEnvironmentCreateRootCRLRecoveryContext(parentContext context.Context, args []string, out, diagnostics io.Writer) error {
	fs := flag.NewFlagSet("recover-create-service-root-crl", flag.ContinueOnError)
	fs.SetOutput(diagnostics)
	env := fs.String("environment", "", "selected dev")
	parent := fs.String("parent-plan-sha256", "", "original generation")
	workspaceOpt := fs.String("workspace", "", "original logical workspace")
	execute := fs.Bool("execute", false, "publish exact qualified CRL")
	review := fs.String("review-sha256", "", "qualified digest")
	confirm := fs.String("confirm", "", "exact stack")
	report := fs.String("report", "", "canonical report")
	timeout := fs.Duration("timeout", 15*time.Minute, "bounded timeout")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *env != "dev" || !environmentPreflightDigest.MatchString(*parent) || *timeout <= 0 || *timeout > 30*time.Minute {
		return rootCRLRecoveryError()
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
	if recoveryMutationGuard(append([]string{"deployment", "recover-create-service-root-crl"}, args...)) != nil {
		return rootCRLRecoveryError()
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
		return rootCRLRecoveryError()
	}
	kube, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || environmentPreflightBytesSHA256(kube) != original.Plan.KubeconfigSHA256 {
		return rootCRLRecoveryError()
	}
	b, err := newEnvironmentKubernetesBackend(string(kube), *env)
	if err != nil {
		return err
	}
	checker, err := environmentPreflightCheckerSHA256()
	if err != nil {
		return err
	}
	if *execute {
		a, source, err := loadCreateRootCRLArtifact(store, e, *review)
		if err != nil {
			return err
		}
		if *confirm != cfg.Values["CLOUD_STACK_NAME"] || a.Plan.CheckerSHA != checker || a.Plan.ConfigurationSHA != deploymentplan.Digest(cfg) || a.Plan.KubeconfigSHA != environmentPreflightBytesSHA256(kube) || a.Plan.Image != original.Plan.Plan.Bootstrap.Image || len(e.Journal.Entries) < len(source.Entries) {
			return rootCRLRecoveryError()
		}
		for i, entry := range source.Entries {
			if deploymentplan.Digest(entry) != deploymentplan.Digest(e.Journal.Entries[i]) {
				return rootCRLRecoveryError()
			}
		}
		if len(e.Journal.Entries) > len(source.Entries) && !rootCRLJournalTailValid(e.Journal, a, source, false) {
			return rootCRLRecoveryError()
		}
		admitted := false
		key := "service-root-crl-renewal-" + *review
		for _, entry := range e.Journal.Entries[len(source.Entries):] {
			if entry.Key != key && !strings.HasPrefix(entry.Key, key+"/") {
				return rootCRLRecoveryError()
			}
			if entry.Key == key && entry.IntentSHA256 == activeTransportJournalDigest(a.Plan) && !entry.StartedAt.Before(a.QualifiedAt) && entry.StartedAt.Before(a.ValidUntil) {
				admitted = true
			}
		}
		if !time.Now().Before(a.ValidUntil) && !admitted {
			return rootCRLRecoveryError()
		}
		p := createFoundationRecoveryPlan{Plan: original.Plan.Plan, Materials: map[string]string{}}
		if err := verifyManagedTransportNativeRegistry(ctx, cfg, store, e, b, &p); err != nil {
			return err
		}
		for key, sha := range a.Plan.Material {
			if strings.HasPrefix(key, "evidence/") && p.Materials[strings.TrimPrefix(key, "evidence/")] != sha {
				return rootCRLRecoveryError()
			}
		}
		return executeCreateRootCRLRecovery(ctx, cfg, store, e, b, a, out)
	}
	if !isManagedTransportCheckpoint(e.Journal) {
		return rootCRLRecoveryError()
	}
	if _, err := os.Lstat(filepath.Join(store.Root, "deployment/create/material", *parent, "service-root-crl-renewal.json")); err == nil {
		return createProviderRecoveryBlock("original Service Root CRL renewal already completed; consume its qualified proof without replay")
	} else if !os.IsNotExist(err) {
		return err
	}
	p := createFoundationRecoveryPlan{Plan: original.Plan.Plan, Materials: map[string]string{}}
	if err := verifyManagedTransportNativeRegistry(ctx, cfg, store, e, b, &p); err != nil {
		return err
	}
	var publication struct {
		Root         map[string]any `json:"root"`
		Intermediate map[string]any `json:"intermediate"`
	}
	if readExecutionJSON(filepath.Join(store.Root, "pki/service/issuer.json"), &publication) != nil {
		return rootCRLRecoveryError()
	}
	root, err := environmentCreateRegisteredIssuerFromMap(publication.Root, "")
	if err != nil || root.Domain != "service" || root.Kind != "root" || root.Status != "active" || root.Document["signer_provider"] != "offline" {
		return rootCRLRecoveryError()
	}
	snapshot, native, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, root.ID)
	if err != nil {
		return err
	}
	now := time.Now().UTC().Truncate(time.Second)
	request, err := buildCreateRootCRLRequest(root, snapshot, now)
	if err != nil {
		return err
	}
	if snapshot.Prior.NextUpdate.After(now.Add(45 * time.Minute)) {
		return createProviderRecoveryBlock("original Service Root CRL already has the required deployment lifetime")
	}
	material, err := createRootCRLMaterial(store, *parent, root)
	if err != nil {
		return err
	}
	for key, value := range p.Materials {
		material["evidence/"+key] = value
	}
	candidate := filepath.Join("deployment/repairs", "root-crl-"+now.Format("20060102T150405Z")+"-"+environmentPreflightBytesSHA256(native)[:12])
	if err := prepareCreateRootCRLRepairDirectory(store, candidate); err != nil {
		return err
	}
	requestPath, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join(candidate, "request.json"), request)
	if err != nil {
		return err
	}
	issuerPath, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join(candidate, "issuer.json"), root.Document)
	if err != nil {
		return err
	}
	if err := runEnvironmentCreatePKICeremony(ctx, store, original.Plan.Plan.Bootstrap.Image, "crl", "--issuer", issuerPath, "--crl-request", requestPath, "--key", "/store/pki/custody/roots/"+root.ID+"/ca-key.encrypted.pem", "--passphrase-file", "/store/pki/custody/service-root-v1-key-passphrase", "--expected-request-sha256", deploymentplan.Digest(request), "--out", "/store/"+filepath.ToSlash(filepath.Join(candidate, "signed"))); err != nil {
		return err
	}
	signed, err := readBoundedEnvironmentFile(filepath.Join(store.Root, candidate, "signed/revocations.pem"), true)
	if err != nil {
		return err
	}
	record, err := validateCreateRootCRLSigned(root, snapshot, request, signed, time.Now().UTC())
	if err != nil {
		return err
	}
	targets, err := loadCreateRootCRLStateTargets(ctx, cfg, store, e, b, root, snapshot.Prior, original.Plan.Plan.Bootstrap.Image)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if _, err := b.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud", "exec", "pod/"+target.PodName, "--container", target.Container, "--", "/bin/sh", "-ec", "test -x /app/pkitrust"); err != nil {
			return createProviderRecoveryBlock("original Root CRL state helper is unavailable; require the governed native synchronization and installed-state continuation")
		}
	}
	stage, j, err := acceptedCreateStage(store, e, "kubernetes/service-active-trust")
	if err != nil || len(stage.Resources) != 1 {
		return rootCRLRecoveryError()
	}
	resource := stage.Resources[0]
	actual, err := b.Read(ctx, resource.ID)
	if err != nil || actual == nil || resource.ID != "Secret/"+cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud/pki-create-service-root-crl" || deploymentplan.Map(actual["metadata"])["uid"] != j.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, actual) {
		return rootCRLRecoveryError()
	}
	desired := deploymentplan.Clone(resource.Desired)
	deploymentplan.Map(desired["data"])["revocations.pem"] = base64.StdEncoding.EncodeToString(signed)
	plan := createRootCRLRecoveryPlan{Schema: createRootCRLRecoverySchema, Parent: *parent, OwnerSHA: deploymentplan.Digest(e.owner), JournalSHA: deploymentplan.Digest(e.Journal), CheckerSHA: checker, ConfigurationSHA: deploymentplan.Digest(cfg), KubeconfigSHA: environmentPreflightBytesSHA256(kube), Image: original.Plan.Plan.Bootstrap.Image, Candidate: filepath.ToSlash(candidate), Root: root, Snapshot: snapshot, SnapshotSHA: environmentPreflightBytesSHA256(native), Request: request, Signed: record, Material: material, Secret: activeTransportRenewalResource{ID: resource.ID, UID: j.Owners[resource.ID], Before: resource.Desired, Desired: desired}, Targets: targets}
	if err := checkCreateRootCRLLeaseAvailable(ctx, e, b, rootCRLLeasePlan(cfg, deploymentplan.Digest(plan))); err != nil {
		return err
	}
	body, err := activeTransportResourceBody(plan.Secret, actual)
	if err != nil {
		return err
	}
	if _, err := b.Write(ctx, "update", body, true); err != nil {
		return err
	}
	raw, err := runNativeCreateRootCRLImport(ctx, cfg, b, plan, false)
	if err != nil {
		return err
	}
	if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(candidate, "native-dry-run.log"), raw); err != nil {
		return err
	}
	sha := deploymentplan.Digest(plan)
	now = time.Now().UTC()
	q := deploymentplan.Qualification{PlanSHA256: sha, Decision: "GO"}
	for _, id := range createRootCRLGates {
		q.Gates = append(q.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: sha})
	}
	a := createRootCRLRecoveryArtifact{Schema: createRootCRLRecoverySchema + "/artifact", Plan: plan, Qualification: q, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
	base := filepath.Join("deployment/create/root-crl-recovery", sha)
	if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(base, "source-journal.json"), mustProviderRecoveryJSON(e.Journal)); err != nil {
		return err
	}
	if err := store.write(filepath.Join(base, "qualification.json"), mustProviderRecoveryJSON(a), false); err != nil {
		return err
	}
	if *report != "" {
		if err := store.write(strings.TrimPrefix(*report, store.Root+string(os.PathSeparator)), mustProviderRecoveryJSON(a), true); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "GO environment=dev scope=original-service-root-crl review-sha256=%s crl-number=%s revocations=%d\n", sha, request.Number, len(request.Entries))
	return nil
}

func loadCreateRootCRLArtifact(store secretStore, e *environmentCreateExecution, review string) (createRootCRLRecoveryArtifact, environmentCreateJournal, error) {
	var a createRootCRLRecoveryArtifact
	var source environmentCreateJournal
	base := filepath.Join(store.Root, "deployment/create/root-crl-recovery", review)
	if !environmentPreflightDigest.MatchString(review) || readExecutionJSON(filepath.Join(base, "qualification.json"), &a) != nil || readExecutionJSON(filepath.Join(base, "source-journal.json"), &source) != nil || a.Schema != createRootCRLRecoverySchema+"/artifact" || a.Plan.Schema != createRootCRLRecoverySchema || deploymentplan.Digest(a.Plan) != review || a.Plan.Parent != e.Journal.PlanSHA256 || a.Plan.OwnerSHA != deploymentplan.Digest(e.owner) || a.Plan.JournalSHA != deploymentplan.Digest(source) || source.OwnerToken != e.Journal.OwnerToken || !isManagedTransportCheckpoint(source) || a.Qualification.PlanSHA256 != review || a.Qualification.Decision != "GO" || len(a.Qualification.Gates) != len(createRootCRLGates) || !a.ValidUntil.Equal(a.QualifiedAt.Add(15*time.Minute)) || a.QualifiedAt.After(time.Now()) {
		return a, source, rootCRLRecoveryError()
	}
	for i, id := range createRootCRLGates {
		g := a.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != review {
			return a, source, rootCRLRecoveryError()
		}
	}
	return a, source, nil
}

func rootCRLStepBegin(e *environmentCreateExecution, key string, intent any) error {
	digest := activeTransportJournalDigest(intent)
	for _, entry := range e.Journal.Entries {
		if entry.Key == key {
			if entry.IntentSHA256 != digest || entry.Phase != "bootstrap" || entry.State != "pending" && entry.State != "allocated" && entry.State != "completed" {
				return rootCRLRecoveryError()
			}
			return nil
		}
	}
	return e.BeginEntry(key, "bootstrap", mustProviderRecoveryJSON(intent), time.Now().UTC())
}
func rootCRLStepComplete(e *environmentCreateExecution, key string, receipt any) error {
	for _, entry := range e.Journal.Entries {
		if entry.Key == key {
			raw := mustProviderRecoveryJSON(receipt)
			if entry.State == "completed" {
				if activeTransportJournalDigest(json.RawMessage(entry.Receipt)) != activeTransportJournalDigest(json.RawMessage(raw)) {
					return rootCRLRecoveryError()
				}
				return nil
			}
			if entry.State == "allocated" {
				if activeTransportJournalDigest(json.RawMessage(entry.Receipt)) != activeTransportJournalDigest(json.RawMessage(raw)) {
					return rootCRLRecoveryError()
				}
				return e.CompleteEntry(key, time.Now().UTC())
			}
			if err := e.RecordReceipt(key, raw); err != nil {
				return err
			}
			return e.CompleteEntry(key, time.Now().UTC())
		}
	}
	return rootCRLRecoveryError()
}

func executeCreateRootCRLRecovery(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, a createRootCRLRecoveryArtifact, out io.Writer) (resultErr error) {
	plan := a.Plan
	review := deploymentplan.Digest(plan)
	key := "service-root-crl-renewal-" + review
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, plan.Candidate, "signed/revocations.pem"), true)
	if err != nil {
		return err
	}
	record, err := validateCreateRootCRLSigned(plan.Root, plan.Snapshot, plan.Request, raw, time.Now().UTC())
	if err != nil || deploymentplan.Digest(record) != deploymentplan.Digest(plan.Signed) {
		return rootCRLRecoveryError()
	}
	material, err := createRootCRLMaterial(store, plan.Parent, plan.Root)
	if err != nil {
		return err
	}
	for path, sha := range material {
		if plan.Material[path] != sha {
			return rootCRLRecoveryError()
		}
	}
	current, _, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, plan.Root.ID)
	if err != nil {
		return err
	}
	imported := deploymentplan.Digest(current.Prior) == deploymentplan.Digest(plan.Signed)
	if imported {
		current.Prior = plan.Snapshot.Prior
	}
	if deploymentplan.Digest(current) != deploymentplan.Digest(plan.Snapshot) {
		return rootCRLRecoveryError()
	}
	for _, target := range plan.Targets {
		actual, err := b.Read(ctx, target.ID)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != target.UID || !deploymentplan.Matches(target.Desired, actual) {
			return rootCRLRecoveryError()
		}
	}
	if err := rootCRLStepBegin(e, key, plan); err != nil {
		return err
	}
	lease, err := b.AcquireExecution(ctx, rootCRLLeasePlan(cfg, review))
	if err != nil {
		return err
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		resultErr = errors.Join(resultErr, lease.Release(cleanup))
	}()
	if err := e.RecordKubernetesLease(lease.Owner()); err != nil {
		return err
	}
	guard := func() error {
		if err := e.Check(ctx); err != nil {
			return err
		}
		return lease.Check(ctx)
	}

	importKey := key + "/native-import"
	intent := map[string]string{"issuer_id": plan.Root.ID, "root_sha256": plan.Root.RootFingerprint, "prior_crl_sha256": plan.Snapshot.Prior.Digest, "signed_crl_sha256": plan.Signed.Digest, "request_sha256": deploymentplan.Digest(plan.Request)}
	if err := rootCRLStepBegin(e, importKey, intent); err != nil {
		return err
	}
	if !imported {
		if err := guard(); err != nil {
			return err
		}
		if err := verifyCreateRootCRLTargetOwners(ctx, cfg, b, plan.Targets[0]); err != nil {
			return err
		}
		result, err := runNativeCreateRootCRLImport(ctx, cfg, b, plan, true)
		if err != nil {
			return err
		}
		if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(plan.Candidate, "native-import.log"), result); err != nil {
			return err
		}
	}
	current, _, err = readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, plan.Root.ID)
	if err != nil || deploymentplan.Digest(current.Prior) != deploymentplan.Digest(plan.Signed) {
		return rootCRLRecoveryError()
	}
	published := current.Prior
	current.Prior = plan.Snapshot.Prior
	if deploymentplan.Digest(current) != deploymentplan.Digest(plan.Snapshot) {
		return rootCRLRecoveryError()
	}
	if err := rootCRLStepComplete(e, importKey, map[string]any{"native_crl": published, "request_sha256": deploymentplan.Digest(plan.Request)}); err != nil {
		return err
	}
	secretKey := key + "/original-secret"
	if err := rootCRLStepBegin(e, secretKey, plan.Secret); err != nil {
		return err
	}
	actual, err := b.Read(ctx, plan.Secret.ID)
	if err != nil {
		return err
	}
	body, err := activeTransportResourceBody(plan.Secret, actual)
	if err != nil {
		return err
	}
	if !deploymentplan.Matches(plan.Secret.Desired, actual) {
		if err := guard(); err != nil {
			return err
		}
		if _, err := b.Write(ctx, "update", body, false); err != nil {
			return err
		}
	}
	actual, err = b.Read(ctx, plan.Secret.ID)
	if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != plan.Secret.UID || !deploymentplan.Matches(plan.Secret.Desired, actual) {
		return rootCRLRecoveryError()
	}
	if err := rootCRLStepComplete(e, secretKey, map[string]string{"resource_id": plan.Secret.ID, "uid": plan.Secret.UID, "desired_sha256": deploymentplan.Digest(plan.Secret.Desired)}); err != nil {
		return err
	}
	for _, target := range plan.Targets {
		stateKey := key + "/state-" + target.Container
		if err := rootCRLStepBegin(e, stateKey, map[string]any{"target": target, "native_crl_sha256": plan.Signed.Digest}); err != nil {
			return err
		}
		if err := guard(); err != nil {
			return err
		}
		if err := verifyCreateRootCRLTargetOwners(ctx, cfg, b, target); err != nil {
			return err
		}
		if err := verifyCreateRootCRLTargetState(ctx, cfg, b, plan, target); err != nil {
			if _, err := b.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud", "exec", "pod/"+target.PodName, "--container", target.Container, "--", "/bin/sh", "-ec", "test -x /app/pkitrust"); err != nil {
				return createProviderRecoveryBlock("original Root CRL state helper is unavailable; require a fresh qualified observation of the native synchronized signed state")
			}
			result, err := installCreateRootCRLState(ctx, cfg, b, plan, target)
			if err != nil {
				return err
			}
			if err := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join(plan.Candidate, "native-state-"+target.Container+".log"), result); err != nil {
				return err
			}
		}
		if err := verifyCreateRootCRLTargetState(ctx, cfg, b, plan, target); err != nil {
			return err
		}
		if err := rootCRLStepComplete(e, stateKey, map[string]any{"resource_id": target.ID, "uid": target.UID, "crl_sha256": plan.Signed.Digest, "paths": target.Paths, "installed_on_disk": true, "runtime_reload_required": true}); err != nil {
			return err
		}
	}
	proof := createRootCRLRecoveryProof{Schema: createRootCRLRecoverySchema, Parent: plan.Parent, Review: review, File: filepath.ToSlash(filepath.Join(plan.Candidate, "signed/revocations.pem")), Digest: plan.Signed.Digest, Completed: true}
	pointer := filepath.Join("deployment/create/material", plan.Parent, "service-root-crl-renewal.json")
	if old, err := store.read(pointer); err == nil {
		var prior createRootCRLRecoveryProof
		if json.Unmarshal([]byte(old), &prior) != nil || prior != proof {
			return rootCRLRecoveryError()
		}
	} else if os.IsNotExist(err) {
		if err := store.write(pointer, mustProviderRecoveryJSON(proof), false); err != nil {
			return err
		}
	} else {
		return err
	}
	if err := rootCRLStepComplete(e, key, proof); err != nil {
		return err
	}
	fmt.Fprintf(out, "PASS environment=dev scope=original-service-root-crl review-sha256=%s crl-number=%s states=4 original-root-preserved=true native-acks=pending-runtime-reload\n", review, plan.Signed.Number)
	return nil
}

func verifyCreateRootCRLTargetState(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend, plan createRootCRLRecoveryPlan, target createRootCRLStateTarget) error {
	resource := "pod/" + target.PodName
	if target.PodName == "" {
		resource = "deployment/" + target.Container
	}
	for _, path := range target.Paths {
		raw, err := b.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud", "exec", resource, "--container", target.Container, "--", "cat", path)
		var state struct {
			Fingerprint string              `json:"issuer_fingerprint"`
			CRL         createRootCRLRecord `json:"crl"`
			Terminal    string              `json:"terminal_issuer_id"`
		}
		if err != nil || json.Unmarshal(raw, &state) != nil || state.Fingerprint != plan.Root.RootFingerprint || state.Terminal != "" || deploymentplan.Digest(state.CRL) != deploymentplan.Digest(plan.Signed) {
			return rootCRLRecoveryError()
		}
	}
	return nil
}

func readCreateRootCRLRecovery(store secretStore, parent string) (*createRootCRLRecoveryArtifact, *createRootCRLRecoveryProof, error) {
	pointer := filepath.Join(store.Root, "deployment/create/material", parent, "service-root-crl-renewal.json")
	if _, err := os.Lstat(pointer); os.IsNotExist(err) {
		return nil, nil, nil
	} else if err != nil {
		return nil, nil, err
	}
	var proof createRootCRLRecoveryProof
	if readExecutionJSON(pointer, &proof) != nil || proof.Schema != createRootCRLRecoverySchema || proof.Parent != parent || !proof.Completed || !environmentPreflightDigest.MatchString(proof.Review) {
		return nil, nil, rootCRLRecoveryError()
	}
	e := &environmentCreateExecution{}
	if readExecutionJSON(filepath.Join(store.Root, "deployment/.execution-active/owner.json"), &e.owner) != nil || e.owner.PlanSHA256 != parent {
		return nil, nil, rootCRLRecoveryError()
	}
	journalPath, err := privateDeploymentStatePath(store.Environment, e.owner.Journal)
	if err != nil || readExecutionJSON(journalPath, &e.Journal) != nil {
		return nil, nil, rootCRLRecoveryError()
	}
	a, source, err := loadCreateRootCRLArtifact(store, e, proof.Review)
	if err != nil {
		return nil, nil, err
	}
	if proof.File != filepath.ToSlash(filepath.Join(a.Plan.Candidate, "signed/revocations.pem")) || proof.Digest != a.Plan.Signed.Digest || filepath.Clean(a.Plan.Candidate) != a.Plan.Candidate || !strings.HasPrefix(a.Plan.Candidate, "deployment/repairs/root-crl-") || len(e.Journal.Entries) < len(source.Entries) {
		return nil, nil, rootCRLRecoveryError()
	}
	for i, entry := range source.Entries {
		if activeTransportJournalDigest(entry) != activeTransportJournalDigest(e.Journal.Entries[i]) {
			return nil, nil, rootCRLRecoveryError()
		}
	}
	key := "service-root-crl-renewal-" + proof.Review
	bound := false
	for _, entry := range e.Journal.Entries {
		if entry.Key == key {
			bound = entry.State == "completed" && !entry.CompletedAt.IsZero() && !entry.StartedAt.Before(a.QualifiedAt) && entry.StartedAt.Before(a.ValidUntil) && entry.IntentSHA256 == activeTransportJournalDigest(a.Plan) && activeTransportJournalDigest(json.RawMessage(entry.Receipt)) == activeTransportJournalDigest(proof)
		}
	}
	if !bound || !rootCRLJournalTailValid(e.Journal, a, source, true) {
		return nil, nil, rootCRLRecoveryError()
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, proof.File), true)
	if err != nil {
		return nil, nil, err
	}
	// Historical publication proof remains valid at its authenticated qualification
	// epoch; it does not establish current runtime freshness.
	record, err := validateCreateRootCRLSigned(a.Plan.Root, a.Plan.Snapshot, a.Plan.Request, raw, a.QualifiedAt)
	if err != nil || deploymentplan.Digest(record) != deploymentplan.Digest(a.Plan.Signed) {
		return nil, nil, rootCRLRecoveryError()
	}
	material, err := createRootCRLMaterial(store, parent, a.Plan.Root)
	if err != nil {
		return nil, nil, err
	}
	for path, sha := range material {
		if a.Plan.Material[path] != sha {
			return nil, nil, rootCRLRecoveryError()
		}
	}
	if err := verifyCreateRootCRLInstalledCompletion(store, e, a, source, proof); err != nil {
		return nil, nil, err
	}
	return &a, &proof, nil
}

func verifyManagedServiceRootCRLFreshness(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend) (map[string]string, error) {
	a, proof, err := readCreateRootCRLRecovery(store, e.Journal.PlanSHA256)
	if err != nil {
		return nil, err
	}
	if a == nil || proof == nil {
		return nil, createProviderRecoveryBlock("temporary TLS restart requires the completed original Service Root CRL renewal and genuine current consumer state")
	}
	if _, err := parseCreateRootCRL(a.Plan.Root, a.Plan.Signed, true, time.Now().UTC()); err != nil {
		return nil, err
	}
	current, _, err := readCreateRootCRLNativeSnapshot(ctx, cfg, e, b, a.Plan.Root.ID)
	if err != nil || deploymentplan.Digest(current.Prior) != deploymentplan.Digest(a.Plan.Signed) {
		return nil, rootCRLRecoveryError()
	}
	current.Prior = a.Plan.Snapshot.Prior
	if deploymentplan.Digest(current) != deploymentplan.Digest(a.Plan.Snapshot) {
		return nil, rootCRLRecoveryError()
	}
	actual, err := b.Read(ctx, a.Plan.Secret.ID)
	if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != a.Plan.Secret.UID || !deploymentplan.Matches(a.Plan.Secret.Desired, actual) {
		return nil, rootCRLRecoveryError()
	}
	for _, target := range a.Plan.Targets {
		actual, err := b.Read(ctx, target.ID)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != target.UID {
			return nil, rootCRLRecoveryError()
		}
		claim, err := b.Read(ctx, target.ClaimID)
		if err != nil || claim == nil || deploymentplan.Map(claim["metadata"])["uid"] != target.ClaimUID {
			return nil, rootCRLRecoveryError()
		}
		containers := certIssuerObjectList(environmentPKIPod(actual)["containers"])
		if len(containers) != 1 || deploymentplan.Map(containers[0])["name"] != target.Container || deploymentplan.Map(containers[0])["image"] != a.Plan.Image {
			return nil, rootCRLRecoveryError()
		}
		readTarget := target
		readTarget.PodName = "" // Native readonly selection after controlled runtime reload.
		if err := verifyCreateRootCRLTargetState(ctx, cfg, b, a.Plan, readTarget); err != nil {
			return nil, err
		}
	}
	proofRaw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment/create/material", proof.Parent, "service-root-crl-renewal.json"), true)
	if err != nil {
		return nil, err
	}
	signedRaw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, proof.File), true)
	if err != nil {
		return nil, err
	}
	return map[string]string{"service-root-crl-renewal-proof": environmentPreflightBytesSHA256(proofRaw), "service-root-crl-signed-file": environmentPreflightBytesSHA256(signedRaw), "service-root-crl-native-publication": deploymentplan.Digest(a.Plan.Signed), "service-root-crl-original-secret": deploymentplan.Digest(a.Plan.Secret.Desired), "service-root-crl-original-secret-owner": deploymentplan.Digest(a.Plan.Secret.UID)}, e.Check(ctx)
}

func rootCRLNativeSourceCheckpoint(store secretStore, e *environmentCreateExecution) bool {
	const prefix = "service-root-crl-renewal-"
	review := ""
	for _, entry := range e.Journal.Entries {
		if strings.HasPrefix(entry.Key, prefix) && !strings.Contains(entry.Key, "/") {
			review = strings.TrimPrefix(entry.Key, prefix)
		}
	}
	if !environmentPreflightDigest.MatchString(review) {
		return false
	}
	a, source, err := loadCreateRootCRLArtifact(store, e, review)
	return err == nil && rootCRLJournalTailValid(e.Journal, a, source, false)
}

func rootCRLJournalTailValid(current environmentCreateJournal, a createRootCRLRecoveryArtifact, source environmentCreateJournal, completed bool) bool {
	if len(current.Entries) <= len(source.Entries) {
		return false
	}
	for i, entry := range source.Entries {
		if activeTransportJournalDigest(entry) != activeTransportJournalDigest(current.Entries[i]) {
			return false
		}
	}
	plan := a.Plan
	review := deploymentplan.Digest(plan)
	key := "service-root-crl-renewal-" + review
	proof := createRootCRLRecoveryProof{Schema: createRootCRLRecoverySchema, Parent: plan.Parent, Review: review, File: filepath.ToSlash(filepath.Join(plan.Candidate, "signed/revocations.pem")), Digest: plan.Signed.Digest, Completed: true}
	type step struct {
		key             string
		intent, receipt any
	}
	steps := []step{{key, plan, proof}, {key + "/native-import", map[string]string{"issuer_id": plan.Root.ID, "root_sha256": plan.Root.RootFingerprint, "prior_crl_sha256": plan.Snapshot.Prior.Digest, "signed_crl_sha256": plan.Signed.Digest, "request_sha256": deploymentplan.Digest(plan.Request)}, map[string]any{"native_crl": plan.Signed, "request_sha256": deploymentplan.Digest(plan.Request)}}, {key + "/original-secret", plan.Secret, map[string]string{"resource_id": plan.Secret.ID, "uid": plan.Secret.UID, "desired_sha256": deploymentplan.Digest(plan.Secret.Desired)}}}
	if len(plan.Targets) != 2 {
		return false
	}
	for i, target := range plan.Targets {
		name := []string{"pki-controller", "certissuer"}[i]
		if target.Container != name || target.ID != "Deployment/"+source.Stack+"-video-cloud/"+name || target.ClaimID != "PersistentVolumeClaim/"+source.Stack+"-video-cloud/"+environmentCreateServiceOwnerClaim(name) || target.ClaimUID == "" || target.PodUID == "" || target.PodName == "" || len(target.Paths) != 2 || target.Paths[0] != environmentCreateServiceState+"/private/crl/listener-root.json" || target.Paths[1] != environmentCreateServiceState+"/private/crl/outbound-root.json" {
			return false
		}
		steps = append(steps, step{key + "/state-" + name, map[string]any{"target": target, "native_crl_sha256": plan.Signed.Digest}, map[string]any{"resource_id": target.ID, "uid": target.UID, "crl_sha256": plan.Signed.Digest, "paths": target.Paths, "installed_on_disk": true, "runtime_reload_required": true}})
	}
	tail := current.Entries[len(source.Entries):]
	if completed {
		if len(tail) < len(steps) {
			return false
		}
		tail = tail[:len(steps)]
	} else if len(tail) > len(steps) {
		return false
	}
	for i, entry := range tail {
		if entry.Key != steps[i].key || entry.Phase != "bootstrap" || entry.IntentSHA256 != activeTransportJournalDigest(steps[i].intent) || entry.StartedAt.Before(a.QualifiedAt) || entry.State != "pending" && entry.State != "allocated" && entry.State != "completed" {
			return false
		}
		if i == 0 && !entry.StartedAt.Before(a.ValidUntil) {
			return false
		}
		if completed && entry.State != "completed" {
			return false
		}
		if entry.State == "pending" {
			if len(entry.Receipt) != 0 || !entry.CompletedAt.IsZero() {
				return false
			}
		} else if len(entry.Receipt) == 0 || activeTransportJournalDigest(json.RawMessage(entry.Receipt)) != activeTransportJournalDigest(steps[i].receipt) || entry.State == "completed" && entry.CompletedAt.IsZero() || entry.State == "allocated" && !entry.CompletedAt.IsZero() {
			return false
		}
	}
	return true
}

func rootCRLLeasePlan(cfg deploymentConfig, review string) *deploymentplan.Plan {
	return &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: cfg.Environment, Stack: cfg.Values["CLOUD_STACK_NAME"], Operation: "scoped", BindingsSHA256: review}}
}
func checkCreateRootCRLLeaseAvailable(ctx context.Context, e *environmentCreateExecution, b environmentKubernetesBackend, plan *deploymentplan.Plan) error {
	if err := e.Check(ctx); err != nil {
		return err
	}
	actual, err := b.Read(ctx, "Lease/"+plan.Stack+"-video-cloud/rtk-deployment-execution")
	if err != nil || actual != nil {
		return rootCRLRecoveryError()
	}
	body := environmentExecutionLeaseBody(plan, plan.SHA256()+":preflight")
	admitted, err := b.Write(ctx, "create", body, true)
	if err != nil || admitted == nil || !deploymentplan.Matches(deploymentplan.Projection(body), deploymentplan.Projection(admitted)) {
		return rootCRLRecoveryError()
	}
	return e.Check(ctx)
}
func verifyCreateRootCRLTargetOwners(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend, target createRootCRLStateTarget) error {
	if verifyCreateRootCRLTargetOwnersStage(ctx, cfg, b, target) != "" {
		return rootCRLRecoveryError()
	}
	return nil
}

// Keep the ordinary recovery error deliberately opaque while allowing domain
// adapters to report which public Kubernetes ownership boundary changed. This
// avoids collapsing Deployment, PVC and Pod drift into one retry-only gate.
func verifyCreateRootCRLTargetOwnersStage(ctx context.Context, cfg deploymentConfig, b environmentKubernetesBackend, target createRootCRLStateTarget) string {
	deployment, err := b.Read(ctx, target.ID)
	if err != nil || deployment == nil {
		return "deployment-read"
	}
	if deploymentplan.Map(deployment["metadata"])["uid"] != target.UID {
		return "deployment-owner"
	}
	if !deploymentplan.Matches(target.Desired, deployment) {
		return "deployment-desired"
	}
	claim, err := b.Read(ctx, target.ClaimID)
	if err != nil || claim == nil {
		return "claim-read"
	}
	if target.ClaimUID == "" || deploymentplan.Map(claim["metadata"])["uid"] != target.ClaimUID {
		return "claim-owner"
	}
	pod, err := b.Read(ctx, "Pod/"+cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud/"+target.PodName)
	if err != nil || pod == nil {
		return "pod-read"
	}
	if target.PodUID == "" || deploymentplan.Map(pod["metadata"])["uid"] != target.PodUID {
		return "pod-owner"
	}
	return ""
}

// Repair material directories use the selected private store's directory guard;
// the deployment state file helper deliberately refuses an existing directory.
func prepareCreateRootCRLRepairDirectory(store secretStore, candidate string) error {
	if filepath.Clean(candidate) != candidate || !strings.HasPrefix(candidate, "deployment/repairs/root-crl-") {
		return rootCRLRecoveryError()
	}
	for _, relative := range []string{"deployment", "deployment/repairs", candidate} {
		path, err := store.safePath(relative)
		if err != nil {
			return err
		}
		parent := filepath.Dir(path)
		if parent == store.Root {
			if err := validateEnvironmentPreflightDirectory(store.ConfigRoot); err != nil {
				return err
			}
			if err := validateEnvironmentPreflightDirectory(store.Root); err != nil {
				return err
			}
		} else if err := validateEnvironmentPreflightDirectories(store, parent); err != nil {
			return err
		}
		if err := ensurePrivateDirectory(path); err != nil {
			return err
		}
	}
	return nil
}

// Historical refused forwards can be completed aliases to a later real stage.
// Only the original Device trust receipt selects its accepted forward; a closed
// alias is never treated as an accepted runtime plan or replayed.
func loadCreateRootCRLCoreOwners(store secretStore, e *environmentCreateExecution) (map[string]deploymentplan.Resource, map[string]string, error) {
	bad := func(stage string) (map[string]deploymentplan.Resource, map[string]string, error) {
		return nil, nil, createProviderRecoveryBlock("original Root CRL consumer ownership cannot be verified: stage=" + stage)
	}
	var forward struct {
		Stage    string `json:"actual_qualified_forward_stage"`
		Original string `json:"original_failed_plan_sha256"`
		Issuer   string `json:"original_root_issuer_id"`
	}
	for _, entry := range e.Journal.Entries {
		if entry.Key == "kubernetes/device-root-trust" && entry.State == "completed" && !entry.CompletedAt.IsZero() {
			if json.Unmarshal(entry.Receipt, &forward) != nil {
				return bad("device-trust-receipt")
			}
		}
	}
	review := deviceTrustForwardReview(forward.Stage)
	if review == "" || !environmentPreflightDigest.MatchString(forward.Original) || !environmentBootstrapOwnerUUID.MatchString(forward.Issuer) {
		return bad("device-trust-forward-reference")
	}
	admitted, before, err := loadAdmittedCreateRecovery(store, e, review)
	if err != nil || !isDeviceTrustCheckpoint(before) || !environmentPreflightDigest.MatchString(admitted.Plan.Materials["accepted-service-core-device-trust-forward-plan"]) || forward.Original != admitted.Plan.Materials["accepted-service-core-device-trust-original-plan"] {
		return bad("device-trust-forward-admission")
	}
	var ready map[string]any
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/device/root-ready.json"), &ready) != nil || deploymentplan.Digest(ready) != admitted.Plan.Materials["accepted-service-core-device-trust-root"] {
		return bad("device-trust-original-root")
	}
	root, rootErr := environmentCreateRegisteredIssuerFromMap(ready, "")
	if rootErr != nil || root.ID != forward.Issuer || root.Environment != before.Environment || root.Domain != "device" || root.Kind != "root" || root.Status != "ready" {
		return bad("device-trust-original-root")
	}
	rootBound := false
	for _, entry := range before.Entries {
		if entry.Key == "device-root-bootstrap" {
			var receipt map[string]any
			expected := deploymentplan.Object{"environment": root.Environment, "issuer_id": root.ID, "signer_reference": root.Reference, "root_sha256": root.RootFingerprint, "bundle_version": root.BundleVersion}
			rootBound = json.Unmarshal(entry.Receipt, &receipt) == nil && deploymentplan.Digest(receipt) == deploymentplan.Digest(expected)
		}
	}
	if !rootBound {
		return bad("device-trust-source-root-receipt")
	}
	var candidate deploymentplan.Plan
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create/openbao-recovery", review, "openbao-root-trust-plan.json"), &candidate) != nil || candidate.SHA256() != admitted.Plan.Materials["accepted-service-core-device-trust-forward-plan"] {
		return bad("device-trust-forward-candidate")
	}
	originals := map[string]deploymentplan.Resource{}
	owners := map[string]string{}
	for _, stage := range []struct {
		key  string
		size int
	}{{"kubernetes/openbao-intermediate-trust", 16}, {forward.Stage, 7}} {
		plan, j, err := acceptedCreateStage(store, e, stage.key)
		if err != nil || len(plan.Resources) != stage.size || plan.Environment != e.Journal.Environment || plan.Stack != e.Journal.Stack {
			return bad(stage.key)
		}
		if stage.key == forward.Stage {
			// Execution binds its scoped stage name after admission; all resource
			// bodies, baselines, owners and other inputs must remain the preview.
			normalized := *plan
			normalized.BindingsSHA256 = candidate.BindingsSHA256
			if normalized.SHA256() != candidate.SHA256() {
				return bad("device-trust-forward-candidate-binding")
			}
		}
		intent, err := environmentCreateKubernetesStageIntent(plan)
		hash, hashErr := environmentCreatePlanSHA256(intent)
		bound := false
		for _, entry := range e.Journal.Entries {
			if entry.Key == stage.key && entry.IntentSHA256 == hash && err == nil && hashErr == nil {
				bound = true
			}
		}
		if !bound {
			return bad(stage.key + "-intent")
		}
		for _, resource := range plan.Resources {
			if resource.ID != "Deployment/"+e.Journal.Stack+"-video-cloud/pki-controller" && resource.ID != "Deployment/"+e.Journal.Stack+"-video-cloud/certissuer" {
				continue
			}
			if owners[resource.ID] != "" && owners[resource.ID] != j.Owners[resource.ID] {
				return bad(stage.key + "-owner")
			}
			originals[resource.ID], owners[resource.ID] = resource, j.Owners[resource.ID]
		}
	}
	if len(originals) != 2 || len(owners) != 2 {
		return bad("native-core-owners")
	}
	return originals, owners, nil
}
