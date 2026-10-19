package main

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateResidualNativeFixture(t *testing.T) (*createServiceSessionRecovery, createManagedLateSeedObservation, managedSessionState, []string, time.Time) {
	t.Helper()
	f := newManagedLateBootstrapChainFixture(t)
	f.ceremony.Root.Environment = "dev"
	f.ceremony.Intermediate.ID = "22222222-2222-4222-8222-222222222222"
	f.ceremony.BootstrapSessionID = "11111111-1111-4111-8111-111111111111"
	leaf, err := x509.ParseCertificate(f.leaf)
	if err != nil {
		t.Fatal(err)
	}
	subjects, err := environmentCreateBootstrapSubjects(deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}})
	if err != nil {
		t.Fatal(err)
	}
	late := &createManagedLateSessionRecovery{ParentSHA: strings.Repeat("a", 64), Review: strings.Repeat("b", 64), OriginalKeySHA: strings.Repeat("c", 64), OriginalCSRSHA: strings.Repeat("d", 64), NewSessionID: f.ceremony.BootstrapSessionID, SourceCeremony: f.ceremony}
	chain := []byte{}
	for _, der := range [][]byte{f.leaf, f.ica, f.root} {
		chain = append(chain, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})...)
	}
	state := managedSessionState{Session: late.NewSessionID, Issuer: f.ceremony.Intermediate.ID, Root: f.ceremony.Root.RootFingerprint, Fingerprint: environmentPreflightBytesSHA256(f.leaf), Status: "expired", Expires: leaf.NotAfter, Subjects: slices.Clone(subjects), Installed: slices.Clone(subjects)}
	seed := createManagedLateSeedObservation{Schema: managedLateSessionSchema, Parent: late.ParentSHA, Review: late.Review, CertificatePEM: string(chain), KeySHA: late.OriginalKeySHA, CSRSHA: late.OriginalCSRSHA, Session: state}
	seed.Session.Status, seed.Session.Installed = "active", nil
	finalized := leaf.NotAfter.Add(-time.Hour)
	remaining := &createManagedLateRemaining{ResidualCleanup: &createManagedLateResidualCleanup{}, PostNativeCleanup: &createManagedLatePostNativeCleanup{Native: environmentCreateBootstrapFinalization{Environment: "dev", SessionID: state.Session, LeafSHA256: state.Fingerprint, FinalizedAt: finalized}}, TerminalAuthority: &createManagedLateTerminalFinalizerAuthority{StartedAt: finalized.Add(-time.Minute), Deadline: finalized.Add(time.Minute)}}
	r := &createServiceSessionRecovery{Ceremony: f.ceremony, ManagedLateSession: late, ManagedLateRemaining: remaining}
	return r, seed, state, subjects, f.now
}

func TestManagedLateResidualNativeKeepsExpiryHistoricalOnlyAfterRealFinalization(t *testing.T) {
	for _, mode := range []string{"expired", "sealed-after-expiry", "ordinary-expired", "ordinary-sealed", "active", "future-expired", "wrong-session", "wrong-issuer", "wrong-root", "wrong-leaf", "changed-expiry", "missing-ack", "duplicate-ack", "other-active", "changed-roster", "missing-finalization", "finalized-at-expiry", "finalized-after-expiry", "finalized-before-leaf", "before-old-window", "after-old-window", "missing-old-window", "changed-public-seed"} {
		t.Run(mode, func(t *testing.T) {
			r, seed, state, subjects, now := managedLateResidualNativeFixture(t)
			p := r.ManagedLateRemaining.PostNativeCleanup
			switch mode {
			case "sealed-after-expiry":
				state.Status = "sealed"
			case "ordinary-expired":
				r.ManagedLateRemaining.ResidualCleanup = nil
			case "ordinary-sealed":
				r.ManagedLateRemaining.ResidualCleanup = nil
				state.Status = "sealed"
			case "active":
				state.Status = "active"
			case "future-expired":
				now = state.Expires.Add(-time.Second)
			case "wrong-session":
				state.Session = "33333333-3333-4333-8333-333333333333"
			case "wrong-issuer":
				state.Issuer = "33333333-3333-4333-8333-333333333333"
			case "wrong-root":
				state.Root = strings.Repeat("e", 64)
			case "wrong-leaf":
				state.Fingerprint = strings.Repeat("e", 64)
			case "changed-expiry":
				state.Expires = state.Expires.Add(time.Second)
			case "missing-ack":
				state.Installed = state.Installed[:8]
			case "duplicate-ack":
				state.Installed[8] = state.Installed[0]
			case "other-active":
				state.OtherActive = 1
			case "changed-roster":
				state.Subjects[8] = "service:foreign"
			case "missing-finalization":
				p.Native.FinalizedAt = time.Time{}
			case "finalized-at-expiry":
				p.Native.FinalizedAt = state.Expires
			case "finalized-after-expiry":
				p.Native.FinalizedAt = state.Expires.Add(time.Second)
			case "finalized-before-leaf":
				p.Native.FinalizedAt = state.Expires.Add(-24 * time.Hour)
			case "before-old-window":
				r.ManagedLateRemaining.TerminalAuthority.StartedAt = p.Native.FinalizedAt.Add(time.Second)
			case "after-old-window":
				r.ManagedLateRemaining.TerminalAuthority.Deadline = p.Native.FinalizedAt.Add(-time.Second)
			case "missing-old-window":
				r.ManagedLateRemaining.TerminalAuthority = nil
			case "changed-public-seed":
				seed.CSRSHA = strings.Repeat("e", 64)
			}
			before := deploymentplan.Digest(r)
			if managedLateTerminalSessionForRecovery(state, seed, r, subjects, now) != (mode == "expired" || mode == "sealed-after-expiry") || deploymentplan.Digest(r) != before {
				t.Fatal("historical finalization accepted substituted authority or mutated proof")
			}
		})
	}
}

func TestManagedLateResidualRevocationPreservesOriginalLeafAndNativeRevocation(t *testing.T) {
	for _, mode := range []string{"expired", "sealed", "ordinary-expired", "status-drift", "missing-finalized", "changed-finalized", "changed-leaf", "missing-revocation", "wrong-reason", "multiple-issuance"} {
		t.Run(mode, func(t *testing.T) {
			r, seed, state, _, _ := managedLateResidualNativeFixture(t)
			at := r.ManagedLateRemaining.PostNativeCleanup.Native.FinalizedAt
			obs := managedLateTerminalRevocation{Session: state.Session, Issuer: state.Issuer, Fingerprint: state.Fingerprint, Status: state.Status, Certificate: seed.CertificatePEM, SealedAt: at.Add(-time.Minute), RevokedAt: at.Add(-time.Minute), FinalizedAt: &at, IssuanceCount: 1, RevocationCount: 1, Reason: "deployment bootstrap finalized"}
			switch mode {
			case "sealed":
				obs.Status, state.Status = "sealed", "sealed"
			case "ordinary-expired":
				r.ManagedLateRemaining.ResidualCleanup = nil
			case "status-drift":
				obs.Status = "sealed"
			case "missing-finalized":
				obs.FinalizedAt = nil
			case "changed-finalized":
				changed := at.Add(time.Second)
				obs.FinalizedAt = &changed
			case "changed-leaf":
				obs.Fingerprint = strings.Repeat("e", 64)
			case "missing-revocation":
				obs.RevocationCount = 0
			case "wrong-reason":
				obs.Reason = "different"
			case "multiple-issuance":
				obs.IssuanceCount = 2
			}
			if managedLateTerminalRevocationForRecovery(obs, state, seed.CertificatePEM, r) != (mode == "expired" || mode == "sealed") {
				t.Fatal("residual revoked identity accepted drift or weakened ordinary expiry guard")
			}
		})
	}
}

func managedLateResidualJWT(header, payload string) string {
	return base64.RawURLEncoding.EncodeToString([]byte(header)) + "." + base64.RawURLEncoding.EncodeToString([]byte(payload)) + "." + base64.RawURLEncoding.EncodeToString([]byte(strings.Repeat("s", 64)))
}

func TestManagedLateResidualCurrentTokenLifetimeRequiresUnambiguousSignedJWT(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	for _, mode := range []string{"no-exp", "finite", "nested-kubernetes-claims", "opaque", "none", "unknown-alg", "empty-signature", "duplicate-exp", "escaped-duplicate", "duplicate-alg", "null-exp", "string-exp", "fractional-exp", "zero-exp", "expired", "reserve-boundary", "trailing", "array", "invalid-base64"} {
		t.Run(mode, func(t *testing.T) {
			header, payload := `{"alg":"RS256"}`, `{"sub":"fixture"}`
			future := strconv.FormatInt(now.Add(time.Hour).Unix(), 10)
			switch mode {
			case "finite":
				payload = `{"exp":` + future + `}`
			case "nested-kubernetes-claims":
				payload = `{"kubernetes.io":{"serviceaccount":{"name":"fixture"}},"sub":"fixture"}`
			case "none":
				header = `{"alg":"none"}`
			case "unknown-alg":
				header = `{"alg":"unknown"}`
			case "duplicate-alg":
				header = `{"alg":"RS256","alg":"RS256"}`
			case "duplicate-exp":
				payload = `{"exp":` + future + `,"exp":` + future + `}`
			case "escaped-duplicate":
				payload = `{"exp":` + future + `,"\u0065xp":` + future + `}`
			case "null-exp":
				payload = `{"exp":null}`
			case "string-exp":
				payload = `{"exp":"` + future + `"}`
			case "fractional-exp":
				payload = `{"exp":` + future + `.5}`
			case "zero-exp":
				payload = `{"exp":0}`
			case "expired":
				payload = `{"exp":` + strconv.FormatInt(now.Add(-time.Second).Unix(), 10) + `}`
			case "reserve-boundary":
				payload = `{"exp":` + strconv.FormatInt(now.Add(managedLateExpiryReserve).Unix(), 10) + `}`
			case "trailing":
				payload += `{}`
			case "array":
				payload = `[]`
			}
			token := managedLateResidualJWT(header, payload)
			if mode == "opaque" {
				token = "private-canary"
			} else if mode == "empty-signature" {
				token = token[:strings.LastIndex(token, ".")+1]
			} else if mode == "invalid-base64" {
				token = "%." + strings.Split(token, ".")[1] + ".%"
			}
			expires, err := managedLateResidualTokenExpiry(token, now)
			pass := mode == "no-exp" || mode == "finite" || mode == "nested-kubernetes-claims"
			if (err == nil) != pass || err != nil && strings.Contains(err.Error(), "private-canary") || pass && expires.IsZero() != (mode != "finite") {
				t.Fatal("current token lifetime accepted ambiguity or leaked token")
			}
		})
	}
}

func TestManagedLateResidualCredentialPinsExactFrozenCurrentIdentity(t *testing.T) {
	for _, mode := range []string{"no-exp", "finite", "client-certificate", "partial-pins", "different-mode", "different-bytes", "expired-token", "near-expiry-client", "unfrozen-context"} {
		t.Run(mode, func(t *testing.T) {
			config := frozenKubernetesFixture(t)
			user := config["users"].([]any)[0].(map[string]any)["user"].(map[string]any)
			now := time.Now().UTC().Truncate(time.Second)
			payload := `{"sub":"fixture"}`
			if mode == "finite" {
				payload = `{"exp":` + strconv.FormatInt(now.Add(time.Hour).Unix(), 10) + `}`
			} else if mode == "expired-token" {
				payload = `{"exp":` + strconv.FormatInt(now.Add(-time.Second).Unix(), 10) + `}`
			}
			user["token"] = managedLateResidualJWT(`{"alg":"RS256"}`, payload)
			if mode == "client-certificate" || mode == "near-expiry-client" {
				delete(user, "token")
				expiry := now.Add(time.Hour)
				if mode == "near-expiry-client" {
					expiry = now.Add(4 * time.Minute)
				}
				user["client-certificate-data"], user["client-key-data"] = frozenClientIdentity(t, expiry, x509.ExtKeyUsageClientAuth)
			}
			raw, _ := yaml.Marshal(config)
			b, err := newEnvironmentKubernetesBackendAt(string(raw), "dev", now)
			if err != nil {
				t.Fatal(err)
			}
			p := &createManagedLateResidualCleanup{}
			switch mode {
			case "partial-pins":
				p.CredentialMode = "non-expiring-kubernetes-token"
			case "different-mode":
				p.CredentialMode, p.CredentialSHA, p.CredentialExpires = "expiring-kubernetes-token", environmentPreflightBytesSHA256(b.KubeconfigContent), now.Add(time.Hour)
			case "different-bytes":
				p.CredentialMode, p.CredentialSHA = "non-expiring-kubernetes-token", strings.Repeat("e", 64)
			case "unfrozen-context":
				b.KubeconfigContent = raw
			}
			before := deploymentplan.Digest(p)
			err = bindManagedLateResidualCredential(p, b, now)
			pass := mode == "no-exp" || mode == "finite" || mode == "client-certificate"
			if (err == nil) != pass || !pass && deploymentplan.Digest(p) != before {
				t.Fatal("credential metadata drift was accepted or failed capture mutated descriptor")
			}
			if pass {
				if !managedLateResidualCredentialValid(p) || p.CredentialSHA != environmentPreflightBytesSHA256(b.KubeconfigContent) || !environmentPreflightDigest.MatchString(managedLateResidualCredentialSHA(p)) {
					t.Fatal("current credential metadata not pinned")
				}
				pinned := deploymentplan.Digest(p)
				if bindManagedLateResidualCredential(p, b, now.Add(time.Second)) != nil || deploymentplan.Digest(p) != pinned {
					t.Fatal("identical current credential retry changed immutable metadata")
				}
			}
		})
	}
}
