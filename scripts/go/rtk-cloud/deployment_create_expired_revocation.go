package main

import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A completed, expired enrollment is already terminal. Its original finalizer
// still requires native revocation publication. OpenBao otherwise acknowledges
// the revoke request with a warning and no revocation_time or CRL entry.
// This compatibility setting grants no issuance or additional certificate life.
func prepareFreshExpiredBootstrapRevocation(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony) error {
	query := "SELECT json_build_object('session_id',b.session_id,'issuer_id',b.issuer_id,'root_sha256',b.root_sha256,'expires_at',b.expires_at,'certificate',i.certificate_pem,'fingerprint',b.bootstrap_fingerprint) FROM pki_deployment_bootstrap_sessions b JOIN pki_service_client_issuances i ON i.fingerprint=b.bootstrap_fingerprint AND i.issuer_id=b.issuer_id AND i.environment=b.environment AND i.status='succeeded' WHERE b.environment='" + cfg.Environment + "' AND b.session_id='" + s.BootstrapSessionID + "'"
	var record struct {
		Session     string    `json:"session_id"`
		Issuer      string    `json:"issuer_id"`
		Root        string    `json:"root_sha256"`
		Expires     time.Time `json:"expires_at"`
		Certificate string    `json:"certificate"`
		Fingerprint string    `json:"fingerprint"`
	}
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	if err != nil || decodeEnvironmentJSON(raw, &record) != nil || record.Session != s.BootstrapSessionID || record.Issuer != s.Intermediate.ID || record.Root != s.Root.RootFingerprint || record.Expires.IsZero() {
		return environmentCreateBootstrapBlocker("bootstrap revocation requires its original registered session and leaf")
	}
	now := time.Now().UTC()
	if record.Expires.After(now) {
		return nil
	}
	leafBlock, rest := pem.Decode([]byte(record.Certificate))
	issuerBlock, _ := pem.Decode([]byte(s.Intermediate.Certificate))
	if leafBlock == nil || issuerBlock == nil || len(bytes.TrimSpace(rest)) != 0 {
		return environmentCreateBootstrapBlocker("expired bootstrap leaf or original issuer is malformed")
	}
	leaf, leafErr := x509.ParseCertificate(leafBlock.Bytes)
	issuer, issuerErr := x509.ParseCertificate(issuerBlock.Bytes)
	if leafErr != nil || issuerErr != nil || leaf.IsCA || leaf.Subject.CommonName != "service:deployment-bootstrap" || leaf.CheckSignatureFrom(issuer) != nil || environmentPreflightBytesSHA256(leaf.Raw) != record.Fingerprint || !leaf.NotAfter.Equal(record.Expires) {
		return environmentCreateBootstrapBlocker("expired bootstrap leaf does not match its original registered issuer and expiry")
	}
	if err := verifyEnvironmentCreateFinalizerInstallations(ctx, cfg, runtime, s); err != nil {
		return err
	}
	tokenPath, err := runtime.Execution.Store.safePath("openbao/root-token")
	if err != nil || tokenPath != runtime.OpenBao.RootTokenPath {
		return environmentCreateBootstrapBlocker("expired bootstrap publication requires the original selected OpenBao authority")
	}
	if _, _, err = activeTransportOriginalIdentity(ctx, cfg, runtime.Execution.Store, runtime.Execution, runtime.Backend); err != nil {
		return err
	}
	token, err := readBoundedEnvironmentFile(tokenPath, true)
	if err != nil {
		return err
	}
	api := func(ctx context.Context, path string, body deploymentplan.Object) (deploymentplan.Object, error) {
		input := append(bytes.TrimSpace(token), '\n')
		script := "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\n"
		if body != nil {
			payload, err := json.Marshal(body)
			if err != nil {
				return nil, err
			}
			input = append(input, payload...)
			script += "bao write " + path + " - >/dev/null\n"
		}
		raw, err := environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, input, script+"bao read -format=json "+path+"\n")
		var response struct {
			Data deploymentplan.Object `json:"data"`
		}
		if err != nil || json.Unmarshal(raw, &response) != nil || response.Data == nil {
			return nil, environmentCreateBootstrapBlocker("original issuer CRL compatibility readback failed; reconcile its recorded intent")
		}
		return response.Data, nil
	}
	return configureExpiredBootstrapRevocation(ctx, runtime.Execution, s, record.Fingerprint, record.Expires, now, api)
}

type expiredBootstrapRevocationIntent struct {
	Schema, Parent, Session, Issuer, Mount, Fingerprint string
	Expires                                             time.Time
	Before, After                                       deploymentplan.Object
}

func configureExpiredBootstrapRevocation(ctx context.Context, e *environmentCreateExecution, s environmentCreateServiceCeremony, fingerprint string, expires, now time.Time, api func(context.Context, string, deploymentplan.Object) (deploymentplan.Object, error)) error {
	bad := func() error {
		return environmentCreateBootstrapBlocker("expired bootstrap CRL compatibility differs from its original session, issuer or unchanged provider configuration")
	}
	if e == nil || api == nil || e.Check(ctx) != nil || s.Intermediate.Environment != e.Journal.Environment || s.Intermediate.Domain != "service" || s.Intermediate.Kind != "intermediate" || !environmentBootstrapOwnerUUID.MatchString(s.BootstrapSessionID) || !environmentBootstrapOwnerUUID.MatchString(s.Intermediate.ID) || !environmentPreflightDigest.MatchString(fingerprint) || expires.IsZero() {
		return bad()
	}
	version, matches := strings.CutPrefix(s.Intermediate.Reference, "pki-issuers/service/"+s.Intermediate.ID+"/v")
	number, err := strconv.ParseUint(version, 10, 32)
	if !matches || err != nil || number == 0 || strconv.FormatUint(number, 10) != version {
		return bad()
	}
	if expires.After(now) {
		return nil
	}
	path := s.Intermediate.Reference + "/config/crl"
	current, err := api(ctx, path, nil)
	if err != nil {
		return err
	}
	if _, ok := current["allow_expired_cert_revocation"].(bool); !ok {
		return bad()
	}
	relative := filepath.Join("deployment/create/material", e.Journal.PlanSHA256, "expired-bootstrap-revocation", s.BootstrapSessionID+".json")
	intent := expiredBootstrapRevocationIntent{Schema: "rtk-cloud.expired-bootstrap-revocation/v1", Parent: e.Journal.PlanSHA256, Session: s.BootstrapSessionID, Issuer: s.Intermediate.ID, Mount: s.Intermediate.Reference, Fingerprint: fingerprint, Expires: expires, Before: deploymentplan.Clone(current), After: deploymentplan.Clone(current)}
	intent.After["allow_expired_cert_revocation"] = true
	file, err := e.Store.safePath(relative)
	if err != nil {
		return err
	}
	if _, statErr := os.Lstat(file); statErr == nil {
		raw, readErr := readBoundedEnvironmentFile(file, true)
		if readErr != nil {
			return readErr
		}
		var saved expiredBootstrapRevocationIntent
		if decodeEnvironmentJSON(raw, &saved) != nil || saved.Before == nil {
			return bad()
		}
		identity := intent
		identity.Before, identity.After = saved.Before, saved.After
		expected := deploymentplan.Clone(saved.Before)
		expected["allow_expired_cert_revocation"] = true
		if deploymentplan.Digest(identity) != deploymentplan.Digest(saved) || deploymentplan.Digest(expected) != deploymentplan.Digest(saved.After) {
			return bad()
		}
		intent = saved
	} else if !os.IsNotExist(statErr) {
		return statErr
	} else if _, err = writeEnvironmentCreateBootstrapJSON(e.Store, relative, intent); err != nil {
		return err
	}
	key := "expired-bootstrap-revocation-" + s.BootstrapSessionID
	entry := createOpenBaoACKCompletionEntry(e.Journal, key)
	intentRaw := mustProviderRecoveryJSON(intent)
	sha, _ := environmentCreatePlanSHA256(intentRaw)
	if entry == nil {
		if err := e.BeginEntry(key, "bootstrap", intentRaw, now); err != nil {
			return err
		}
		entry = createOpenBaoACKCompletionEntry(e.Journal, key)
	}
	if entry.IntentSHA256 != sha || entry.Phase != "bootstrap" || entry.State != "pending" && entry.State != "allocated" && entry.State != "completed" {
		return bad()
	}
	if deploymentplan.Digest(current) != deploymentplan.Digest(intent.After) {
		if entry.State != "pending" || len(entry.Receipt) != 0 || deploymentplan.Digest(current) != deploymentplan.Digest(intent.Before) {
			return bad()
		}
		if err = e.Check(ctx); err != nil {
			return err
		}
		current, err = api(ctx, path, deploymentplan.Object{"allow_expired_cert_revocation": true})
		if err != nil {
			return err
		}
		if deploymentplan.Digest(current) != deploymentplan.Digest(intent.After) {
			return bad()
		}
	}
	if err = e.Check(ctx); err != nil {
		return err
	}
	if entry.State == "completed" {
		var receipt expiredBootstrapRevocationIntent
		if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || deploymentplan.Digest(receipt) != deploymentplan.Digest(intent) {
			return bad()
		}
		return nil
	}
	if err = e.RecordReceipt(key, intentRaw); err != nil {
		return err
	}
	return e.CompleteEntry(key, time.Now().UTC())
}
