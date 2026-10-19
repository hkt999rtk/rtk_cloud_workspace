package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The residual source guard is mandatory at each caller. This predicate only
// changes the historical seed's present-day lifetime expectation; it does not
// authorize authenticating or issuing with that already destroyed identity.
func managedLateTerminalSessionForRecovery(current managedSessionState, seed createManagedLateSeedObservation, r *createServiceSessionRecovery, subjects []string, now time.Time) bool {
	if r == nil {
		return false
	}
	if r.ManagedLateRemaining == nil || r.ManagedLateRemaining.ResidualCleanup == nil {
		return managedLateTerminalSealedSession(current, seed.Session, r.Ceremony, subjects, now)
	}
	remaining := r.ManagedLateRemaining
	if r.ManagedLateSession == nil || remaining.PostNativeCleanup == nil || !managedLateResidualFinalizationBeforeExpiry(r, seed, current, remaining.PostNativeCleanup.Native, now) {
		return false
	}
	installed := slices.Clone(current.Installed)
	slices.Sort(installed)
	return (current.Status == "sealed" || current.Status == "expired" && !current.Expires.After(now)) && current.Session == r.Ceremony.BootstrapSessionID && current.Session == seed.Session.Session && current.Issuer == r.Ceremony.Intermediate.ID && current.Issuer == seed.Session.Issuer && current.Root == r.Ceremony.Root.RootFingerprint && current.Root == seed.Session.Root && current.Fingerprint == seed.Session.Fingerprint && environmentPreflightDigest.MatchString(current.Fingerprint) && current.OtherActive == 0 && current.Expires.Equal(seed.Session.Expires) && len(subjects) == 9 && slices.Equal(current.Subjects, subjects) && len(installed) == 9 && slices.Equal(installed, subjects)
}

func managedLateResidualFinalizationBeforeExpiry(r *createServiceSessionRecovery, seed createManagedLateSeedObservation, state managedSessionState, native environmentCreateBootstrapFinalization, now time.Time) bool {
	if r == nil || r.ManagedLateSession == nil || r.ManagedLateRemaining == nil || r.ManagedLateRemaining.ResidualCleanup == nil || r.ManagedLateRemaining.TerminalAuthority == nil || !managedLatePostNativePublicSeedMatches(seed, r.ManagedLateSession) {
		return false
	}
	leaf, err := parseFirstCertificatePEM([]byte(seed.CertificatePEM))
	a := r.ManagedLateRemaining.TerminalAuthority
	return err == nil && !native.FinalizedAt.IsZero() && !native.FinalizedAt.Before(leaf.NotBefore) && !native.FinalizedAt.Before(a.StartedAt) && !native.FinalizedAt.After(a.Deadline) && !native.FinalizedAt.After(now) && native.FinalizedAt.Before(leaf.NotAfter) && native.FinalizedAt.Before(state.Expires) && state.Expires.Equal(seed.Session.Expires) && state.Expires.Equal(leaf.NotAfter) && native.SessionID == state.Session && native.LeafSHA256 == state.Fingerprint
}

func managedLateTerminalRevocationForRecovery(observed managedLateTerminalRevocation, state managedSessionState, certificate string, r *createServiceSessionRecovery) bool {
	if r == nil {
		return false
	}
	var post *createManagedLatePostNativeCleanup
	if r.ManagedLateRemaining != nil {
		post = r.ManagedLateRemaining.PostNativeCleanup
	}
	if r.ManagedLateRemaining == nil || r.ManagedLateRemaining.ResidualCleanup == nil {
		return managedLateTerminalRevocationForScope(observed, state, certificate, post)
	}
	if post == nil || observed.Status != state.Status || observed.Status != "sealed" && observed.Status != "expired" {
		return false
	}
	// Expiry changes only the retained session bookkeeping status. All existing
	// leaf, single issuance/revocation, reason and finalized-row pins still apply.
	copy := observed
	copy.Status = "sealed"
	return managedLateTerminalRevocationForScope(copy, state, certificate, post)
}

// Called only after these exact frozen credentials authenticated a fresh
// Inventory request. Parsing lifetime metadata is not token authentication.
func bindManagedLateResidualCredential(p *createManagedLateResidualCleanup, b environmentKubernetesBackend, now time.Time) error {
	bad := func() error { return createOpenBaoACKBlock("residual-cleanup-current-credential") }
	if p == nil || now.IsZero() {
		return bad()
	}
	frozen, err := newEnvironmentKubernetesBackendAt(string(b.KubeconfigContent), b.Store.Environment, now)
	if err != nil || !bytes.Equal(frozen.KubeconfigContent, b.KubeconfigContent) {
		return bad()
	}
	var config struct {
		Users []struct {
			User map[string]string `yaml:"user"`
		} `yaml:"users"`
	}
	if yaml.Unmarshal(b.KubeconfigContent, &config) != nil || len(config.Users) != 1 {
		return bad()
	}
	user := config.Users[0].User
	var mode string
	var expires time.Time
	if token := user["token"]; token != "" {
		expires, err = managedLateResidualTokenExpiry(token, now)
		if err != nil {
			return bad()
		}
		mode = "expiring-kubernetes-token"
		if expires.IsZero() {
			mode = "non-expiring-kubernetes-token"
		}
	} else {
		certificate, decodeErr := base64.StdEncoding.Strict().DecodeString(user["client-certificate-data"])
		leaf, leafErr := parseFirstCertificatePEM(certificate)
		if decodeErr != nil || leafErr != nil {
			return bad()
		}
		mode, expires = "kubernetes-client-certificate", leaf.NotAfter
	}
	if !expires.IsZero() && !expires.After(now.Add(managedLateExpiryReserve)) {
		return bad()
	}
	sha := environmentPreflightBytesSHA256(b.KubeconfigContent)
	if p.CredentialMode != "" || p.CredentialSHA != "" || !p.CredentialExpires.IsZero() {
		if !managedLateResidualCredentialValid(p) || p.CredentialMode != mode || p.CredentialSHA != sha || !p.CredentialExpires.Equal(expires) {
			return bad()
		}
		return nil
	}
	p.CredentialMode, p.CredentialSHA, p.CredentialExpires = mode, sha, expires
	return nil
}

func managedLateResidualCredentialSHA(p *createManagedLateResidualCleanup) string {
	if !managedLateResidualCredentialValid(p) {
		return ""
	}
	return deploymentplan.Digest(deploymentplan.Object{"mode": p.CredentialMode, "kubeconfig_sha256": p.CredentialSHA, "expires": p.CredentialExpires})
}

func managedLateResidualTokenExpiry(token string, now time.Time) (time.Time, error) {
	bad := func() (time.Time, error) {
		return time.Time{}, createOpenBaoACKBlock("residual-cleanup-current-credential")
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(token) > 256<<10 {
		return bad()
	}
	header, hErr := base64.RawURLEncoding.Strict().DecodeString(parts[0])
	payload, pErr := base64.RawURLEncoding.Strict().DecodeString(parts[1])
	signature, sErr := base64.RawURLEncoding.Strict().DecodeString(parts[2])
	h, hErrJSON := managedLateResidualJWTObject(header)
	claims, pErrJSON := managedLateResidualJWTObject(payload)
	var alg string
	if hErr != nil || pErr != nil || sErr != nil || hErrJSON != nil || pErrJSON != nil || json.Unmarshal(h["alg"], &alg) != nil || !keySet("RS256", "RS384", "RS512", "ES256", "ES384", "ES512", "EdDSA", "HS256", "HS384", "HS512")[alg] || len(signature) < 32 {
		return bad()
	}
	raw, ok := claims["exp"]
	if !ok {
		return time.Time{}, nil
	}
	var number json.Number
	if len(raw) == 0 || raw[0] < '0' || raw[0] > '9' || json.Unmarshal(raw, &number) != nil {
		return bad()
	}
	seconds, err := number.Int64()
	if err != nil || seconds <= 0 {
		return bad()
	}
	expires := time.Unix(seconds, 0).UTC()
	if !expires.After(now.Add(managedLateExpiryReserve)) {
		return bad()
	}
	return expires, nil
}

func managedLateResidualJWTObject(raw []byte) (map[string]json.RawMessage, error) {
	bad := func() (map[string]json.RawMessage, error) {
		return nil, createOpenBaoACKBlock("residual-cleanup-current-credential")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	token, err := d.Token()
	if err != nil || token != json.Delim('{') {
		return bad()
	}
	result := map[string]json.RawMessage{}
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || key == "" || result[key] != nil {
			return bad()
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return bad()
		}
		result[key] = value
	}
	if token, err = d.Token(); err != nil || token != json.Delim('}') {
		return bad()
	}
	if _, err = d.Token(); err != io.EOF {
		return bad()
	}
	return result, nil
}
