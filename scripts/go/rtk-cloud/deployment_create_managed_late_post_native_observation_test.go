package main

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedLatePostNativePublicSeedRetainsExactSignedOriginal(t *testing.T) {
	f := newManagedLateBootstrapChainFixture(t)
	f.ceremony.Intermediate.ID = "22222222-2222-4222-8222-222222222222"
	leaf, err := x509.ParseCertificate(f.leaf)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"three", "identical-ICA-four", "schema", "parent", "review", "key", "csr", "session", "issuer", "root", "fingerprint", "expiry", "missing-ICA", "reordered", "foreign-leaf", "prefix-garbage", "suffix-garbage", "PEM-headers", "noncertificate", "extra-certificate"} {
		t.Run(mode, func(t *testing.T) {
			late := &createManagedLateSessionRecovery{ParentSHA: strings.Repeat("a", 64), Review: strings.Repeat("b", 64), NewSessionID: "11111111-1111-4111-8111-111111111111", OriginalKeySHA: strings.Repeat("c", 64), OriginalCSRSHA: strings.Repeat("d", 64), SourceCeremony: f.ceremony}
			seed := createManagedLateSeedObservation{Schema: managedLateSessionSchema, Parent: late.ParentSHA, Review: late.Review, KeySHA: late.OriginalKeySHA, CSRSHA: late.OriginalCSRSHA, Session: managedSessionState{Session: late.NewSessionID, Issuer: late.SourceCeremony.Intermediate.ID, Root: late.SourceCeremony.Root.RootFingerprint, Fingerprint: environmentPreflightBytesSHA256(leaf.Raw), Expires: leaf.NotAfter}}
			chain := [][]byte{f.leaf, f.ica, f.root}
			switch mode {
			case "identical-ICA-four":
				chain = [][]byte{f.leaf, f.ica, slices.Clone(f.ica), f.root}
			case "schema":
				seed.Schema = "unknown"
			case "parent":
				seed.Parent = strings.Repeat("e", 64)
			case "review":
				seed.Review = strings.Repeat("e", 64)
			case "key":
				seed.KeySHA = strings.Repeat("e", 64)
			case "csr":
				seed.CSRSHA = strings.Repeat("e", 64)
			case "session":
				seed.Session.Session = "99999999-9999-4999-8999-999999999999"
			case "issuer":
				seed.Session.Issuer = "99999999-9999-4999-8999-999999999999"
			case "root":
				seed.Session.Root = strings.Repeat("e", 64)
			case "fingerprint":
				seed.Session.Fingerprint = strings.Repeat("e", 64)
			case "expiry":
				seed.Session.Expires = seed.Session.Expires.Add(time.Second)
			case "missing-ICA":
				chain = [][]byte{f.leaf, f.root}
			case "reordered":
				chain = [][]byte{f.leaf, f.root, f.ica}
			case "foreign-leaf":
				chain[0] = newManagedLateBootstrapChainFixture(t).leaf
			case "extra-certificate":
				chain = append(chain, f.root)
			}
			for i, der := range chain {
				block := &pem.Block{Type: "CERTIFICATE", Bytes: der}
				if i == 0 && mode == "PEM-headers" {
					block.Headers = map[string]string{"Unexpected": "value"}
				} else if i == 0 && mode == "noncertificate" {
					block.Type = "PUBLIC KEY"
				}
				seed.CertificatePEM += string(pem.EncodeToMemory(block))
			}
			if mode == "prefix-garbage" {
				seed.CertificatePEM = "unrecorded material\n" + seed.CertificatePEM
			} else if mode == "suffix-garbage" {
				seed.CertificatePEM += "unrecorded material"
			}
			before := deploymentplan.Digest(seed)
			if managedLatePostNativePublicSeedMatches(seed, late) != (mode == "three" || mode == "identical-ICA-four") || deploymentplan.Digest(seed) != before {
				t.Fatal("post-native source accepted changed seed or normalized original bytes", mode)
			}
		})
	}
}

func TestManagedLatePostNativeRevocationRequiresSameAlreadyFinalizedLeaf(t *testing.T) {
	f := newManagedLateBootstrapChainFixture(t)
	certificate := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.leaf}))
	state := managedSessionState{Session: "11111111-1111-4111-8111-111111111111", Issuer: "22222222-2222-4222-8222-222222222222", Fingerprint: environmentPreflightBytesSHA256(f.leaf)}
	for _, mode := range []string{"genuine", "not-finalized", "different-finalized-time", "wrong-session", "wrong-leaf", "active", "not-revoked", "missing-revocation", "wrong-reason", "other-certificate"} {
		t.Run(mode, func(t *testing.T) {
			finalizedAt := f.now.Add(time.Minute)
			post := &createManagedLatePostNativeCleanup{Native: environmentCreateBootstrapFinalization{SessionID: state.Session, LeafSHA256: state.Fingerprint, FinalizedAt: finalizedAt}}
			observed := managedLateTerminalRevocation{Session: state.Session, Issuer: state.Issuer, Fingerprint: state.Fingerprint, Status: "sealed", Certificate: certificate, SealedAt: f.now, RevokedAt: f.now, FinalizedAt: &finalizedAt, IssuanceCount: 1, RevocationCount: 1, Reason: "deployment bootstrap finalized"}
			switch mode {
			case "not-finalized":
				observed.FinalizedAt = nil
			case "different-finalized-time":
				changed := finalizedAt.Add(time.Second)
				observed.FinalizedAt = &changed
			case "wrong-session":
				post.Native.SessionID = "99999999-9999-4999-8999-999999999999"
			case "wrong-leaf":
				post.Native.LeafSHA256 = strings.Repeat("e", 64)
			case "active":
				observed.Status = "active"
			case "not-revoked":
				observed.RevokedAt = time.Time{}
			case "missing-revocation":
				observed.RevocationCount = 0
			case "wrong-reason":
				observed.Reason = "unknown"
			case "other-certificate":
				observed.Certificate = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: f.signedLeaf(t, "service:other", false)}))
			}
			before := deploymentplan.Digest(observed)
			if managedLateTerminalRevocationForScope(observed, state, certificate, post) != (mode == "genuine") || deploymentplan.Digest(observed) != before {
				t.Fatal("post-native scope changed or substituted original revocation", mode)
			}
			if managedLateTerminalRevocationForScope(observed, state, certificate, nil) {
				// The ordinary terminal observer must still require finalized_at NULL.
				if mode != "not-finalized" {
					t.Fatal("ordinary unfinalized scope admitted completed native finalization")
				}
			}
		})
	}
}

func TestManagedLatePostNativeCleanupMarkerCannotIssueAnotherNativePOST(t *testing.T) {
	_, observed, ceremony, _, _ := managedHistoricalSeedFixture(t)
	ceremony.CleanupBinding = observed.Cleanup
	calls := 0
	client, err := newEnvironmentCreatePKIClient("http://127.0.0.1", &http.Client{Transport: consoleRoundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return nil, errors.New("unapproved request")
	})}, "dev", "operator")
	if err != nil {
		t.Fatal(err)
	}
	runtime := environmentCreateBootstrapRuntime{ServiceSessionRecovery: &createServiceSessionRecovery{ManagedLateRemaining: &createManagedLateRemaining{PostNativeCleanup: &createManagedLatePostNativeCleanup{}}}}
	err = finalizeEnvironmentCreateServiceSession(context.Background(), client, deploymentConfig{Environment: "dev"}, environmentCreateBootstrapPlan{}, secretStore{}, runtime, ceremony, nil)
	if err == nil || !strings.Contains(err.Error(), "post-native-cleanup-current-source") || calls != 0 {
		t.Fatal("unadmitted cleanup marker reached native POST", err, calls)
	}
}
