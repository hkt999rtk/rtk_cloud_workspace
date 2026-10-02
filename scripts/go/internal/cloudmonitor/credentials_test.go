package cloudmonitor

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func TestCertificateCheckedAtUsesCompletionTime(t *testing.T) {
	for _, tc := range []struct {
		name      string
		checkedAt func(time.Time) time.Time
		want      Status
	}{
		{
			name:      "completion-after-collection-start",
			checkedAt: func(completed time.Time) time.Time { return completed },
			want:      Pass,
		},
		{
			name:      "stale-at-completion",
			checkedAt: func(completed time.Time) time.Time { return completed.Add(-5*time.Minute - time.Second) },
			want:      Unknown,
		},
		{
			name:      "future-at-completion",
			checkedAt: func(completed time.Time) time.Time { return completed.Add(2*time.Minute + 10*time.Second) },
			want:      Unknown,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			started := time.Now().UTC().Add(-3 * time.Minute)
			f := &collectorRunner{fn: func([]string) ([]byte, int, error) {
				completed := time.Now().UTC()
				body, _ := json.Marshal(map[string]any{
					"environment": "dev",
					"stack":       "video-cloud-dev",
					"checked_at":  tc.checkedAt(completed),
					"results": []map[string]any{{
						"source":    "endpoint/service",
						"kind":      "certificate",
						"status":    "OK",
						"not_after": completed.Add(24 * time.Hour),
					}},
				})
				return body, 0, nil
			}}
			c := CollectCertificates(context.Background(), Config{Environment: "dev", Stack: "video-cloud-dev", CertificateTool: "cert"}, Runtime{ConfigRoot: "/store/dev"}, f, started)
			inventory := findCollectorResult(t, c, "certificate-inventory")
			if inventory.Status != tc.want {
				t.Fatalf("inventory status = %s, want %s", inventory.Status, tc.want)
			}
			if tc.want != Pass {
				return
			}
			if !inventory.ObservedAt.After(started.Add(2 * time.Minute)) {
				t.Fatalf("inventory observation remained at collection start: %s", inventory.ObservedAt)
			}
			if len(c.Results) != 2 || !c.Results[1].ObservedAt.Equal(inventory.ObservedAt) {
				t.Fatalf("certificate result observation must match completion: %#v", c.Results)
			}
			if c.Results[1].Value == nil || *c.Results[1].Value >= 1 {
				t.Fatalf("days remaining must be calculated at completion: %#v", c.Results[1])
			}
		})
	}
}

func TestJWTVerificationScopesAndRedaction(t *testing.T) {
	now := time.Now().UTC()
	root := t.TempDir()
	key := []byte("a-long-secret-key-DO-NOT-EXPOSE")
	os.WriteFile(filepath.Join(root, "key"), key, 0600)
	claims := jwt.MapClaims{"sub": "monitor", "scope": "read monitor", "iss": "issuer", "aud": "service", "exp": now.Add(time.Hour).Unix()}
	token, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(key)
	os.WriteFile(filepath.Join(root, "token"), []byte(token), 0600)
	cfg := Config{Tokens: []TokenCheck{{Name: "t", Service: "s", Kind: "jwt", TokenFile: "token", KeyFile: "key", Algorithm: "HS256", Issuer: "issuer", Audience: "service", Subject: "monitor", RequiredScopes: []string{"monitor"}, Required: true, WarnSeconds: 300}}}
	c := CollectTokens(context.Background(), cfg, Runtime{ConfigRoot: root}, now)
	if findCollectorResult(t, c, "token/t/validity").Status != Pass {
		t.Fatal("valid jwt should pass")
	}
	cfg.Tokens[0].RequiredScopes = []string{"write"}
	c = CollectTokens(context.Background(), cfg, Runtime{ConfigRoot: root}, now)
	if findCollectorResult(t, c, "token/t/validity").Status != Fail {
		t.Fatal("scope missing must fail")
	}
	os.WriteFile(filepath.Join(root, "key"), []byte("wrongkey"), 0600)
	c = CollectTokens(context.Background(), cfg, Runtime{ConfigRoot: root}, now)
	if findCollectorResult(t, c, "token/t/validity").Status != Fail {
		t.Fatal("wrong signature must fail")
	}
	body, _ := json.Marshal(c)
	if strings.Contains(string(body), token) || strings.Contains(string(body), string(key)) {
		t.Fatal("secret leak")
	}
}
func TestOpaqueMetadataCannotProveValidity(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "token"), []byte("OPAQUE-SECRET"), 0600)
	now := time.Now()
	meta, _ := json.Marshal(map[string]any{"expires_at": now.Add(time.Hour), "last_succeeded_at": now.Add(-time.Hour)})
	publicFixture(t, root, "meta", meta)
	c := CollectTokens(context.Background(), Config{Tokens: []TokenCheck{{Name: "t", Service: "s", Kind: "opaque", TokenFile: "token", MetadataFile: "meta", WarnSeconds: 30, Required: true}}}, Runtime{ConfigRoot: root}, now)
	if findCollectorResult(t, c, "token/t/validity").Status != Unknown {
		t.Fatal("opaque metadata expiry cannot prove validity")
	}
}

func TestBearerValidationRequiresProtectedEndpoint(t *testing.T) {
	for _, public := range []bool{true, false} {
		s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !public && r.Header.Get("Authorization") == "" {
				w.WriteHeader(401)
				return
			}
			w.Write([]byte("ok"))
		}))
		status, _ := validateBearer(context.Background(), Config{}, Runtime{}, TokenCheck{ValidationURL: s.URL}, "secret")
		s.Close()
		if public && status != Unknown {
			t.Fatal("public endpoint is not validity evidence")
		}
		if !public && status != Pass {
			t.Fatal("protected endpoint accepts valid bearer")
		}
	}
}

func TestTokenExpiryRequiresTrustedEvidenceAndMetadataBinding(t *testing.T) {
	now := time.Now().UTC()
	root := t.TempDir()
	key := []byte("trusted-signing-key")
	os.WriteFile(filepath.Join(root, "key"), key, 0600)
	claims := jwt.MapClaims{"exp": now.Add(time.Hour).Unix(), "subject_id": "device-1", "scope": []string{"monitor"}}
	token, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString(key)
	os.WriteFile(filepath.Join(root, "token"), []byte(token), 0600)
	digest := sha256.Sum256([]byte(token))
	base := TokenCheck{Name: "t", Service: "s", Kind: "jwt", TokenFile: "token", KeyFile: "key", Algorithm: "HS256", Subject: "device-1", SubjectClaim: "subject_id", RequiredScopes: []string{"monitor"}, WarnSeconds: 30, Required: true}
	c := CollectTokens(context.Background(), Config{Tokens: []TokenCheck{base}}, Runtime{ConfigRoot: root}, now)
	if findCollectorResult(t, c, "token/t/validity").Status != Pass {
		t.Fatal("configured subject_id must verify")
	}
	verifiedExpiry := findCollectorResult(t, c, "token/t/expiry")
	if verifiedExpiry.Status != Pass {
		t.Fatal("verified expiry")
	}
	meta, _ := json.Marshal(map[string]any{"expires_at": now.Add(30 * 24 * time.Hour), "issuer_verified": true, "token_sha256": hex.EncodeToString(digest[:]), "last_succeeded_at": now.Add(-time.Minute)})
	publicFixture(t, root, "metadata", meta)
	base.MetadataFile = "metadata"
	c = CollectTokens(context.Background(), Config{Tokens: []TokenCheck{base}}, Runtime{ConfigRoot: root}, now)
	e := findCollectorResult(t, c, "token/t/expiry")
	if e.ExpiresAt == nil || !e.ExpiresAt.Equal(*verifiedExpiry.ExpiresAt) {
		t.Fatal("metadata must not override verified JWT exp")
	}
	os.WriteFile(filepath.Join(root, "key"), []byte("wrong signing key"), 0600)
	c = CollectTokens(context.Background(), Config{Tokens: []TokenCheck{base}}, Runtime{ConfigRoot: root}, now)
	e = findCollectorResult(t, c, "token/t/expiry")
	if e.Status != Unknown || e.ExpiresAt != nil {
		t.Fatal("invalid signature must not expose exp as trusted")
	}
	base.KeyFile = ""
	base.Algorithm = ""
	base.MetadataFile = ""
	c = CollectTokens(context.Background(), Config{Tokens: []TokenCheck{base}}, Runtime{ConfigRoot: root}, now)
	if findCollectorResult(t, c, "token/t/expiry").Status != Unknown {
		t.Fatal("unverified raw exp cannot pass")
	}
	opaque := "opaque-credential"
	os.WriteFile(filepath.Join(root, "opaque"), []byte(opaque), 0600)
	opaqueHash := sha256.Sum256([]byte(opaque))
	for _, tc := range []struct {
		name     string
		verified bool
		hash     string
		future   bool
		want     Status
	}{{"unattested", false, hex.EncodeToString(opaqueHash[:]), false, Unknown}, {"wrong-token", true, "wrong", false, Unknown}, {"bound", true, hex.EncodeToString(opaqueHash[:]), false, Pass}, {"future", true, hex.EncodeToString(opaqueHash[:]), true, Unknown}} {
		t.Run(tc.name, func(t *testing.T) {
			last := now.Add(-time.Minute)
			if tc.future {
				last = now.Add(time.Hour)
			}
			meta, _ := json.Marshal(map[string]any{"expires_at": now.Add(time.Hour), "issuer_verified": tc.verified, "token_sha256": tc.hash, "last_succeeded_at": last})
			publicFixture(t, root, "opaque-metadata", meta)
			c := CollectTokens(context.Background(), Config{Tokens: []TokenCheck{{Name: "o", Service: "s", Kind: "opaque", TokenFile: "opaque", MetadataFile: "opaque-metadata", WarnSeconds: 30, Required: true}}}, Runtime{ConfigRoot: root}, now)
			if e := findCollectorResult(t, c, "token/o/expiry"); e.Status != tc.want {
				t.Fatalf("expiry %s", e.Status)
			}
			if tc.future && findCollectorResult(t, c, "token/o/renewal").Status != Unknown {
				t.Fatal("future renewal cannot pass")
			}
		})
	}
}
func TestJWTRenewalFutureTimestampAndDefaultSubject(t *testing.T) {
	now := time.Now()
	root := t.TempDir()
	key := []byte("trusted-key")
	os.WriteFile(filepath.Join(root, "key"), key, 0600)
	token, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"sub": "account", "exp": now.Add(time.Hour).Unix()}).SignedString(key)
	os.WriteFile(filepath.Join(root, "token"), []byte(token), 0600)
	meta, _ := json.Marshal(map[string]any{"last_succeeded_at": now.Add(time.Hour)})
	publicFixture(t, root, "metadata", meta)
	c := CollectTokens(context.Background(), Config{Tokens: []TokenCheck{{Name: "t", Service: "s", Kind: "jwt", TokenFile: "token", KeyFile: "key", Algorithm: "HS256", Subject: "account", MetadataFile: "metadata", WarnSeconds: 30, Required: true}}}, Runtime{ConfigRoot: root}, now)
	if findCollectorResult(t, c, "token/t/validity").Status != Pass {
		t.Fatal("sub is default subject claim")
	}
	if findCollectorResult(t, c, "token/t/renewal").Status != Unknown {
		t.Fatal("future success timestamp not evidence")
	}
}

func TestMissingJWTVerificationKeyIsUnknown(t *testing.T) {
	root := t.TempDir()
	now := time.Now()
	token, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"exp": now.Add(time.Hour).Unix()}).SignedString([]byte("key"))
	os.WriteFile(filepath.Join(root, "token"), []byte(token), 0600)
	c := CollectTokens(context.Background(), Config{Tokens: []TokenCheck{{Name: "t", Service: "s", Kind: "jwt", TokenFile: "token", KeyFile: "missing", Algorithm: "HS256", Required: true, WarnSeconds: 30}}}, Runtime{ConfigRoot: root}, now)
	if findCollectorResult(t, c, "token/t/validity").Status != Unknown || findCollectorResult(t, c, "token/t/expiry").Status != Unknown {
		t.Fatal("missing verification source remains UNKNOWN")
	}
}
