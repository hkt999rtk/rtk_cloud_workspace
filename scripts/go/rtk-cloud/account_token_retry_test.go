package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"testing"
	"time"
)

func TestAccountEnsureUserAppCertificateUsesExtendedTransientRetryBudget(t *testing.T) {
	t.Setenv("CLOUD_CREATE_USERS_APP_CERT_RETRIES", "8")
	origSleep := appCertificateRetrySleep
	appCertificateRetrySleep = func(time.Duration) {}
	t.Cleanup(func() {
		appCertificateRetrySleep = origSleep
	})

	loginAttempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/brand-clouds/rtk-test/auth/login" {
			http.NotFound(w, r)
			return
		}
		loginAttempts++
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["app_csr_pem"] == "" {
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user":            map[string]string{"id": "user-1", "email": "rtk+001@users.local"},
				"tokens":          map[string]string{"access_token": testJWT(time.Now().Add(time.Hour))},
				"app_certificate": map[string]string{"status": "csr_required"},
			})
			return
		}
		if loginAttempts <= 9 {
			w.WriteHeader(http.StatusInternalServerError)
			_ = json.NewEncoder(w).Encode(map[string]any{"code": "internal_error", "message": "Internal server error"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"user":   map[string]string{"id": "user-1", "email": "rtk+001@users.local"},
			"tokens": map[string]string{"access_token": testJWT(time.Now().Add(time.Hour))},
			"app_certificate": map[string]string{
				"status":             "issued",
				"fingerprint_sha256": "new-fingerprint",
				"certificate_pem":    "new-cert",
			},
		})
	}))
	defer server.Close()

	ctx := accountManagerContext{BaseURL: server.URL}
	_, certificate, _, err := accountEnsureUserAppCertificate(ctx, "rtk-test", "rtk+001@users.local", "pass", "app-brand-cloud-user:brand-user-1", nil, nil)
	if err != nil {
		t.Fatalf("accountEnsureUserAppCertificate() error = %v", err)
	}
	if stringValue(certificate["fingerprint_sha256"]) != "new-fingerprint" || loginAttempts != 10 {
		t.Fatalf("certificate=%v loginAttempts=%d", certificate, loginAttempts)
	}
}

func TestJWTExpiresAtUsesExpClaim(t *testing.T) {
	want := time.Now().Add(10 * time.Minute).Truncate(time.Second)
	got, ok := jwtExpiresAt(testJWT(want))
	if !ok {
		t.Fatal("expected JWT exp to decode")
	}
	if math.Abs(got.Sub(want).Seconds()) > 1 {
		t.Fatalf("expiresAt = %s, want %s", got, want)
	}
	if _, ok := jwtExpiresAt("not-a-jwt"); ok {
		t.Fatal("expected malformed JWT to return ok=false")
	}
}

func TestAccountCreateUserRefreshesPlatformTokenBeforeExpiry(t *testing.T) {
	loginCount := 0
	refreshCount := 0
	createAttempts := 0
	oldAccessToken := testJWT(time.Now().Add(30 * time.Second))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/login":
			loginCount++
			http.Error(w, "login should not be used for token refresh", http.StatusInternalServerError)
		case "/v1/auth/refresh":
			refreshCount++
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": map[string]string{"access_token": testJWT(time.Now().Add(time.Hour)), "refresh_token": testJWT(time.Now().Add(24 * time.Hour))}})
		case "/v1/admin/brand-clouds/brand-1/users":
			createAttempts++
			if r.Header.Get("authorization") == "Bearer "+oldAccessToken {
				t.Fatal("create user used a nearly expired access token")
			}
			if r.Header.Get("authorization") == "" {
				t.Fatalf("authorization header = %q", r.Header.Get("authorization"))
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"action": "created", "brand_cloud_user": map[string]string{"id": "brand-user-1"}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := accountManagerContext{BaseURL: server.URL, AdminEmail: "admin@example.test", AdminPassword: "pass"}
	session := accountPlatformSession{AccessToken: oldAccessToken, RefreshToken: testJWT(time.Now().Add(time.Hour))}
	action, err := accountCreateUser(ctx, &session, func(string, ...any) {}, "brand-1", "rtk+001@users.local", "RTK User 001", "user-pass", "member", true)
	if err != nil {
		t.Fatalf("accountCreateUser() error = %v", err)
	}
	if action.Action != "created" || action.BrandCloudUserID != "brand-user-1" || session.AccessToken == oldAccessToken || session.RefreshToken == "" || loginCount != 0 || refreshCount != 1 || createAttempts != 1 {
		t.Fatalf("action=%+v session=%+v loginCount=%d refreshCount=%d createAttempts=%d", action, session, loginCount, refreshCount, createAttempts)
	}
}

func TestAccountCreateUserReusesValidPlatformSession(t *testing.T) {
	loginCount := 0
	refreshCount := 0
	createAttempts := 0
	accessToken := testJWT(time.Now().Add(time.Hour))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/login":
			loginCount++
			http.Error(w, "login should not be used while token is valid", http.StatusInternalServerError)
		case "/v1/auth/refresh":
			refreshCount++
			http.Error(w, "refresh should not be used while token is valid", http.StatusInternalServerError)
		case "/v1/admin/brand-clouds/brand-1/users":
			createAttempts++
			if r.Header.Get("authorization") != "Bearer "+accessToken {
				t.Fatalf("authorization header = %q", r.Header.Get("authorization"))
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"action":           "created",
				"brand_cloud_user": map[string]string{"id": "brand-user"},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := accountManagerContext{BaseURL: server.URL, AdminEmail: "admin@example.test", AdminPassword: "pass"}
	session := accountPlatformSession{AccessToken: accessToken, RefreshToken: testJWT(time.Now().Add(time.Hour))}
	for i := 0; i < 3; i++ {
		if _, err := accountCreateUser(ctx, &session, func(string, ...any) {}, "brand-1", "rtk+001@users.local", "RTK User 001", "user-pass", "member", true); err != nil {
			t.Fatalf("accountCreateUser() attempt %d error = %v", i+1, err)
		}
	}
	if loginCount != 0 || refreshCount != 0 || createAttempts != 3 {
		t.Fatalf("loginCount=%d refreshCount=%d createAttempts=%d", loginCount, refreshCount, createAttempts)
	}
}

func TestCreateClaimTokenFallsBackToPlatformLoginWhenRefreshFails(t *testing.T) {
	loginCount := 0
	refreshCount := 0
	claimAttempts := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/login":
			loginCount++
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": map[string]string{"access_token": testJWT(time.Now().Add(time.Hour)), "refresh_token": testJWT(time.Now().Add(24 * time.Hour))}})
		case "/v1/auth/refresh":
			refreshCount++
			http.Error(w, `{"error":{"code":"invalid_refresh_token"}}`, http.StatusUnauthorized)
		case "/v1/admin/device-claim-tokens":
			claimAttempts++
			if r.Header.Get("authorization") == "" {
				t.Fatalf("authorization header = %q", r.Header.Get("authorization"))
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]string{"claim_token": "claim-token", "id": "claim-1"})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := accountManagerContext{BaseURL: server.URL, AdminEmail: "admin@example.test", AdminPassword: "pass"}
	session := accountPlatformSession{AccessToken: testJWT(time.Now().Add(30 * time.Second)), RefreshToken: testJWT(time.Now().Add(time.Hour))}
	oldAccessToken := session.AccessToken
	claim, err := createClaimToken(ctx, &session, nil, func(string, ...any) {}, "brand-1", bindAssignment{
		DeviceID:       "load-device-0001",
		DeviceType:     "camera",
		Category:       "ip_camera",
		ServiceOptions: []string{"mqtt", "video_streaming", "video_storage"},
	}, "run-1", 24)
	if err != nil {
		t.Fatalf("createClaimToken() error = %v", err)
	}
	if stringValue(claim["claim_token"]) != "claim-token" || session.AccessToken == oldAccessToken || session.RefreshToken == "" || loginCount != 1 || refreshCount != 1 || claimAttempts != 1 {
		t.Fatalf("claim=%v session=%+v loginCount=%d refreshCount=%d claimAttempts=%d", claim, session, loginCount, refreshCount, claimAttempts)
	}
}

func TestStartProvisionRefreshesBrandCloudUserTokenOnUnauthorized(t *testing.T) {
	refreshCount := 0
	provisionAttempts := 0
	oldAccessToken := testJWT(time.Now().Add(time.Hour))
	newAccessToken := testJWT(time.Now().Add(2 * time.Hour))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/brand-clouds/rtk-test/auth/refresh":
			refreshCount++
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": map[string]string{"access_token": newAccessToken, "refresh_token": testJWT(time.Now().Add(24 * time.Hour))}})
		case "/v1/orgs/brand-1/devices/device-1/provision":
			provisionAttempts++
			switch r.Header.Get("authorization") {
			case "Bearer " + oldAccessToken:
				http.Error(w, "expired", http.StatusUnauthorized)
			case "Bearer " + newAccessToken:
				w.WriteHeader(http.StatusAccepted)
			default:
				t.Fatalf("authorization header = %q", r.Header.Get("authorization"))
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := accountManagerContext{BaseURL: server.URL}
	user := &brandCloudUserSession{
		Email:    "rtk+001@users.local",
		Password: "pass",
		Session: accountPlatformSession{
			AccessToken:  oldAccessToken,
			RefreshToken: testJWT(time.Now().Add(time.Hour)),
		},
	}
	err := startProvisionWithBrandCloudUserRetry(ctx, "rtk-test", "brand-1", bindAssignment{
		DeviceID:        "load-device-0001",
		AccountDeviceID: "device-1",
		ServiceOptions:  []string{"mqtt"},
	}, "op-1", map[string]any{
		"video_cloud_devid": "load-device-0001",
		"activity_id":       "activity-1",
		"clip_public_key":   "key",
	}, user, func(string, ...any) {})
	if err != nil {
		t.Fatalf("startProvisionWithBrandCloudUserRetry() error = %v", err)
	}
	if refreshCount != 1 || provisionAttempts != 2 || user.Session.AccessToken != newAccessToken {
		t.Fatalf("refreshCount=%d provisionAttempts=%d session=%+v", refreshCount, provisionAttempts, user.Session)
	}
}

func TestBrandCloudUserAccessTokenReusesArtifactTokenWithoutLogin(t *testing.T) {
	loginCount := 0
	refreshCount := 0
	accessToken := testJWT(time.Now().Add(time.Hour))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/brand-clouds/rtk-test/auth/login":
			loginCount++
			http.Error(w, "login should not be used while artifact token is valid", http.StatusInternalServerError)
		case "/v1/brand-clouds/rtk-test/auth/refresh":
			refreshCount++
			http.Error(w, "refresh should not be used while artifact token is valid", http.StatusInternalServerError)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := accountManagerContext{BaseURL: server.URL}
	user := &brandCloudUserSession{
		Email:    "rtk+001@users.local",
		Password: "pass",
		Session: accountPlatformSession{
			AccessToken:  accessToken,
			RefreshToken: testJWT(time.Now().Add(24 * time.Hour)),
		},
	}
	got, err := brandCloudUserAccessToken(ctx, "rtk-test", user, func(string, ...any) {})
	if err != nil {
		t.Fatalf("brandCloudUserAccessToken() error = %v", err)
	}
	if got != accessToken || loginCount != 0 || refreshCount != 0 {
		t.Fatalf("token=%q loginCount=%d refreshCount=%d", got, loginCount, refreshCount)
	}
}

func TestBrandCloudUserAccessTokenRefreshesArtifactTokenBeforeExpiry(t *testing.T) {
	loginCount := 0
	refreshCount := 0
	oldAccessToken := testJWT(time.Now().Add(30 * time.Second))
	newAccessToken := testJWT(time.Now().Add(time.Hour))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/brand-clouds/rtk-test/auth/login":
			loginCount++
			http.Error(w, "login should not be used when refresh token is valid", http.StatusInternalServerError)
		case "/v1/brand-clouds/rtk-test/auth/refresh":
			refreshCount++
			_ = json.NewEncoder(w).Encode(map[string]any{"tokens": map[string]string{"access_token": newAccessToken, "refresh_token": testJWT(time.Now().Add(24 * time.Hour))}})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := accountManagerContext{BaseURL: server.URL}
	user := &brandCloudUserSession{
		Email:    "rtk+001@users.local",
		Password: "pass",
		Session: accountPlatformSession{
			AccessToken:  oldAccessToken,
			RefreshToken: testJWT(time.Now().Add(time.Hour)),
		},
	}
	got, err := brandCloudUserAccessToken(ctx, "rtk-test", user, func(string, ...any) {})
	if err != nil {
		t.Fatalf("brandCloudUserAccessToken() error = %v", err)
	}
	if got != newAccessToken || user.Session.AccessToken != newAccessToken || loginCount != 0 || refreshCount != 1 {
		t.Fatalf("token=%q session=%+v loginCount=%d refreshCount=%d", got, user.Session, loginCount, refreshCount)
	}
}

func TestAccountEnsureUserAppCertificateRecoversMissingLocalCredentials(t *testing.T) {
	loginAttempts := 0
	recovered := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/brand-clouds/rtk-test/auth/login" {
			http.NotFound(w, r)
			return
		}
		loginAttempts++
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		switch {
		case loginAttempts == 1:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user":            map[string]string{"id": "user-1", "email": "rtk+001@users.local"},
				"tokens":          map[string]string{"access_token": testJWT(time.Now().Add(time.Hour))},
				"app_certificate": map[string]string{"status": "issued", "fingerprint_sha256": "old-fingerprint", "certificate_pem": "old-cert"},
			})
		case loginAttempts == 2:
			if !recovered {
				t.Fatal("expected recovery before second login")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user":            map[string]string{"id": "user-1", "email": "rtk+001@users.local"},
				"tokens":          map[string]string{"access_token": testJWT(time.Now().Add(time.Hour))},
				"app_certificate": map[string]string{"status": "csr_required"},
			})
		default:
			if req["app_csr_pem"] == "" {
				t.Fatal("expected CSR on issuing login")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user":   map[string]string{"id": "user-1", "email": "rtk+001@users.local"},
				"tokens": map[string]string{"access_token": testJWT(time.Now().Add(time.Hour))},
				"app_certificate": map[string]string{
					"status":             "issued",
					"fingerprint_sha256": "new-fingerprint",
					"certificate_pem":    "new-cert",
				},
			})
		}
	}))
	defer server.Close()

	ctx := accountManagerContext{BaseURL: server.URL}
	credentials, certificate, _, err := accountEnsureUserAppCertificate(ctx, "rtk-test", "rtk+001@users.local", "pass", "app-brand-cloud-user:brand-user-1", nil, func() error {
		recovered = true
		return nil
	})
	if err != nil {
		t.Fatalf("accountEnsureUserAppCertificate() error = %v", err)
	}
	if !hasLocalAppCredentials(credentials) || stringValue(certificate["fingerprint_sha256"]) != "new-fingerprint" || loginAttempts != 3 {
		t.Fatalf("credentials=%v certificate=%v loginAttempts=%d", credentials, certificate, loginAttempts)
	}
}

func TestShouldRetryLegacyAppCertificateSubject(t *testing.T) {
	cases := []struct {
		name    string
		err     error
		subject string
		userID  string
		want    bool
	}{
		{
			name:    "csr invalid brand subject",
			err:     errors.New("login failed during app certificate bootstrap: email=rtk+001@users.local HTTP 400: code=app_certificate_csr_invalid message=App certificate CSR is invalid"),
			subject: "app-brand-cloud-user:brand-user-1",
			userID:  "brand-user-1",
			want:    true,
		},
		{
			name:    "internal error uses algorithm fallback instead of legacy subject",
			err:     errors.New("login failed during app certificate bootstrap: email=rtk+001@users.local HTTP 500: code=internal_error message=Internal server error"),
			subject: "app-brand-cloud-user:brand-user-1",
			userID:  "brand-user-1",
			want:    false,
		},
		{
			name:    "platform subject",
			err:     errors.New("login failed during app certificate bootstrap: email=rtk+001@users.local HTTP 500: code=internal_error message=Internal server error"),
			subject: "app-user:user-1",
			userID:  "user-1",
			want:    false,
		},
		{
			name:    "missing user id uses brand cloud subject id on csr invalid",
			err:     errors.New("login failed during app certificate bootstrap: email=rtk+001@users.local HTTP 400: code=app_certificate_csr_invalid message=App certificate CSR is invalid"),
			subject: "app-brand-cloud-user:brand-user-1",
			userID:  "",
			want:    true,
		},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			if got := shouldRetryLegacyAppCertificateSubject(tt.err, tt.subject, tt.userID); got != tt.want {
				t.Fatalf("shouldRetryLegacyAppCertificateSubject() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestShouldRetrySameAppCertificateSubject(t *testing.T) {
	if !shouldRetrySameAppCertificateSubject(errors.New("login failed during app certificate bootstrap: email=rtk+001@users.local HTTP 500: code=internal_error message=Internal server error"), "app-brand-cloud-user:brand-user-1") {
		t.Fatal("expected transient internal error to retry same brand-cloud subject")
	}
	if shouldRetrySameAppCertificateSubject(errors.New("login failed during app certificate bootstrap: email=rtk+001@users.local HTTP 400: code=app_certificate_csr_invalid message=App certificate CSR is invalid"), "app-brand-cloud-user:brand-user-1") {
		t.Fatal("did not expect CSR validation error to retry same subject")
	}
	if shouldRetrySameAppCertificateSubject(errors.New("login failed during app certificate bootstrap: email=rtk+001@users.local HTTP 500: code=internal_error message=Internal server error"), "app-user:user-1") {
		t.Fatal("did not expect platform subject to retry same subject")
	}
}

func TestShouldFallbackAppCertificateAlgorithm(t *testing.T) {
	err := errors.New("login failed during app certificate bootstrap: email=rtk+001@users.local HTTP 500: code=internal_error message=Internal server error")
	if !shouldFallbackAppCertificateAlgorithm(err, "ed25519") {
		t.Fatal("expected Ed25519 internal error to fall back to P-256")
	}
	if shouldFallbackAppCertificateAlgorithm(err, "p256") {
		t.Fatal("did not expect P-256 to fall back again")
	}
	if shouldFallbackAppCertificateAlgorithm(errors.New("login failed during app certificate bootstrap: email=rtk+001@users.local HTTP 400: code=app_certificate_csr_invalid message=App certificate CSR is invalid"), "ed25519") {
		t.Fatal("did not expect CSR validation error to trigger algorithm fallback")
	}
}

func TestLegacyAppCertificateSubjects(t *testing.T) {
	got := legacyAppCertificateSubjects("app-brand-cloud-user:brand-user-1", "user-1")
	want := []string{"app-user:brand-user-1", "app-user:user-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("legacyAppCertificateSubjects() = %#v, want %#v", got, want)
	}
	got = legacyAppCertificateSubjects("app-brand-cloud-user:brand-user-1", "brand-user-1")
	want = []string{"app-user:brand-user-1"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("legacyAppCertificateSubjects() = %#v, want %#v", got, want)
	}
}

func TestAccountEnsureUserAppCertificateRecoversMismatchedLocalCredentials(t *testing.T) {
	loginAttempts := 0
	recovered := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/brand-clouds/rtk-test/auth/login" {
			http.NotFound(w, r)
			return
		}
		loginAttempts++
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatal(err)
		}
		switch {
		case loginAttempts == 1:
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user":            map[string]string{"id": "user-1", "email": "rtk+001@users.local"},
				"tokens":          map[string]string{"access_token": testJWT(time.Now().Add(time.Hour))},
				"app_certificate": map[string]string{"status": "issued", "fingerprint_sha256": "old-fingerprint", "certificate_pem": "old-cert"},
			})
		case loginAttempts == 2:
			if !recovered {
				t.Fatal("expected recovery before second login")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user":            map[string]string{"id": "user-1", "email": "rtk+001@users.local"},
				"tokens":          map[string]string{"access_token": testJWT(time.Now().Add(time.Hour))},
				"app_certificate": map[string]string{"status": "csr_required"},
			})
		default:
			if req["app_csr_pem"] == "" {
				t.Fatal("expected CSR on issuing login")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"user":   map[string]string{"id": "user-1", "email": "rtk+001@users.local"},
				"tokens": map[string]string{"access_token": testJWT(time.Now().Add(time.Hour))},
				"app_certificate": map[string]string{
					"status":             "issued",
					"fingerprint_sha256": "new-fingerprint",
					"certificate_pem":    "new-cert",
				},
			})
		}
	}))
	defer server.Close()

	staleCredentials := map[string]any{
		"private_key_pem": "-----BEGIN PRIVATE KEY-----\nstale\n-----END PRIVATE KEY-----",
		"csr_pem":         "-----BEGIN CERTIFICATE REQUEST-----\nstale\n-----END CERTIFICATE REQUEST-----",
	}
	ctx := accountManagerContext{BaseURL: server.URL}
	credentials, certificate, _, err := accountEnsureUserAppCertificate(ctx, "rtk-test", "rtk+001@users.local", "pass", "app-brand-cloud-user:brand-user-1", staleCredentials, func() error {
		recovered = true
		return nil
	})
	if err != nil {
		t.Fatalf("accountEnsureUserAppCertificate() error = %v", err)
	}
	if !hasLocalAppCredentials(credentials) || stringValue(certificate["fingerprint_sha256"]) != "new-fingerprint" || loginAttempts != 3 {
		t.Fatalf("credentials=%v certificate=%v loginAttempts=%d", credentials, certificate, loginAttempts)
	}
}

func TestAccountRevokeBrandCloudUserAppCertificateUsesPost(t *testing.T) {
	revokeAttempts := 0
	accessToken := testJWT(time.Now().Add(time.Hour))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/admin/brand-clouds/brand-1/users/brand-user-1/app-certificate/revoke":
			revokeAttempts++
			if r.Method != http.MethodPost {
				t.Fatalf("method = %s, want POST", r.Method)
			}
			if r.Header.Get("authorization") != "Bearer "+accessToken {
				t.Fatalf("authorization header = %q", r.Header.Get("authorization"))
			}
			_ = json.NewEncoder(w).Encode(map[string]int{"revoked": 1})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	ctx := accountManagerContext{BaseURL: server.URL}
	session := accountPlatformSession{AccessToken: accessToken, RefreshToken: testJWT(time.Now().Add(time.Hour))}
	if err := accountRevokeBrandCloudUserAppCertificate(ctx, &session, func(string, ...any) {}, "brand-1", "brand-user-1"); err != nil {
		t.Fatalf("accountRevokeBrandCloudUserAppCertificate() error = %v", err)
	}
	if revokeAttempts != 1 {
		t.Fatalf("revokeAttempts = %d, want 1", revokeAttempts)
	}
}

func testJWT(expiresAt time.Time) string {
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"none","typ":"JWT"}`))
	payload, _ := json.Marshal(map[string]any{"exp": expiresAt.Unix()})
	return header + "." + base64.RawURLEncoding.EncodeToString(payload) + ".signature"
}
