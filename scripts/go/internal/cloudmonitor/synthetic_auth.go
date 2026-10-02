package cloudmonitor

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Synthetic identities are provisioned by the operator. No signup, device
// enrollment, certificate rotation, or business account creation occurs here.
type syntheticCredentials struct {
	Dedicated      bool   `json:"dedicated"`
	Email          string `json:"email,omitempty"`
	Password       string `json:"password,omitempty"`
	Username       string `json:"username,omitempty"`
	RefreshToken   string `json:"refresh_token,omitempty"`
	AccessToken    string `json:"access_token,omitempty"`
	ClientIDPrefix string `json:"client_id_prefix,omitempty"`
	ClientCertFile string `json:"client_cert_file,omitempty"`
	ClientKeyFile  string `json:"client_key_file,omitempty"`
	Scope          string `json:"scope,omitempty"`
	DeviceID       string `json:"devid,omitempty"`
	Service        string `json:"service,omitempty"`
}

type syntheticTokens struct {
	AccessToken           string    `json:"access_token"`
	AccessTokenExpiresAt  time.Time `json:"access_token_expires_at,omitempty"`
	RefreshToken          string    `json:"refresh_token,omitempty"`
	RefreshTokenExpiresAt time.Time `json:"refresh_token_expires_at,omitempty"`
}

type syntheticAuthState struct {
	Tokens    syntheticTokens `json:"tokens"`
	UpdatedAt time.Time       `json:"updated_at"`
}

func readSyntheticCredentials(rt Runtime, reference string) (syntheticCredentials, error) {
	var credentials syntheticCredentials
	raw, err := ReadCredential(rt, reference)
	if err != nil || json.Unmarshal(raw, &credentials) != nil || !credentials.Dedicated {
		return credentials, errors.New("dedicated monitoring identity unavailable")
	}
	return credentials, nil
}

func syntheticID() string {
	var id [12]byte
	if _, err := rand.Read(id[:]); err != nil {
		return ""
	}
	return hex.EncodeToString(id[:])
}

func syntheticStateDirectory(rt Runtime) (string, error) {
	// Validate the selected root before creating any state below it.
	info, err := os.Stat(rt.ConfigRoot)
	if err != nil || !info.IsDir() {
		return "", errors.New("selected monitoring credential root unavailable")
	}
	monitorDir := filepath.Join(rt.ConfigRoot, "monitor")
	if err := ensurePrivateDirectory(monitorDir); err != nil {
		return "", err
	}
	stateDir := filepath.Join(monitorDir, "state")
	if err := ensurePrivateDirectory(stateDir); err != nil {
		return "", err
	}
	return stateDir, nil
}

func acquireSyntheticLock(rt Runtime) (*WriterLock, error) {
	dir, err := syntheticStateDirectory(rt)
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "synthetic.lock")
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return nil, errors.New("invalid synthetic lock")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, errors.New("another synthetic group is active")
	}
	return &WriterLock{file: f}, nil
}

func syntheticAuthPath(rt Runtime, probe AuthProbe) string {
	// Bind rotation state to its exact endpoints and credential reference so a
	// changed configuration cannot send old tokens to a different server.
	identity, _ := json.Marshal(probe)
	sum := sha256.Sum256(identity)
	return filepath.Join(rt.ConfigRoot, "monitor", "state", "auth-"+hex.EncodeToString(sum[:])+".json")
}

func readSyntheticState(rt Runtime, probe AuthProbe) (syntheticAuthState, bool, error) {
	var state syntheticAuthState
	path := syntheticAuthPath(rt, probe)
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return state, false, nil
	}
	raw, err := ReadCredential(rt, path)
	if err != nil || json.Unmarshal(raw, &state) != nil || state.UpdatedAt.IsZero() {
		return state, true, errors.New("monitor token state unavailable")
	}
	return state, true, nil
}

func saveSyntheticState(rt Runtime, probe AuthProbe, tokens syntheticTokens, now time.Time) error {
	if _, err := syntheticStateDirectory(rt); err != nil {
		return err
	}
	raw, err := json.Marshal(syntheticAuthState{Tokens: tokens, UpdatedAt: now})
	if err != nil {
		return err
	}
	return atomicPrivateWrite(syntheticAuthPath(rt, probe), raw)
}

func syntheticURL(raw, expectedPath string) bool {
	u, err := url.Parse(raw)
	return err == nil && u.Host != "" && u.User == nil && (u.Scheme == "http" || u.Scheme == "https") && u.Path == expectedPath && u.RawQuery == "" && u.Fragment == ""
}

func syntheticJSON(ctx context.Context, client *http.Client, endpoint string, payload any) ([]byte, int, error) {
	if validURL(endpoint) != nil {
		return nil, 0, errors.New("invalid probe URL")
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, 0, errors.New("invalid probe payload")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, 0, errors.New("invalid probe request")
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, errors.New("probe request failed")
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(raw) > 1<<20 {
		return nil, resp.StatusCode, errors.New("probe response unavailable")
	}
	return raw, resp.StatusCode, nil
}

func decodeSyntheticTokens(raw []byte, kind string) (syntheticTokens, error) {
	var tokens syntheticTokens
	if kind == "account-manager" {
		var envelope struct {
			Tokens syntheticTokens `json:"tokens"`
		}
		if json.Unmarshal(raw, &envelope) != nil {
			return tokens, errors.New("invalid token response")
		}
		tokens = envelope.Tokens
		if tokens.RefreshToken == "" || tokens.RefreshTokenExpiresAt.IsZero() {
			return tokens, errors.New("invalid rotated grant response")
		}
	} else {
		if json.Unmarshal(raw, &tokens) != nil {
			return tokens, errors.New("invalid token response")
		}
		// The actual signed exp supplies expiry metadata. Acceptance by the
		// protected API is checked separately; decoding alone is not validity.
		parsed, _, err := jwt.NewParser().ParseUnverified(tokens.AccessToken, jwt.MapClaims{})
		if err != nil {
			return tokens, errors.New("invalid signed token response")
		}
		exp, err := parsed.Claims.GetExpirationTime()
		if err != nil || exp == nil {
			return tokens, errors.New("token expiry metadata unavailable")
		}
		tokens.AccessTokenExpiresAt = exp.Time
	}
	if tokens.AccessToken == "" || tokens.AccessTokenExpiresAt.IsZero() {
		return tokens, errors.New("invalid access token response")
	}
	return tokens, nil
}

func videoRequiresReauthentication(token string) (bool, error) {
	parsed, _, err := jwt.NewParser().ParseUnverified(token, jwt.MapClaims{})
	if err != nil {
		return false, errors.New("token renewal eligibility unavailable")
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return false, errors.New("token renewal eligibility unavailable")
	}
	// Video Cloud forbids stateless reissue for test-lab actors and revisioned
	// entitlements. Those identities must use their verified mTLS certificate
	// to resolve current entitlement state through /request_token each round.
	if actor, ok := claims["actor_type"].(string); ok && actor == "test_lab" {
		return true, nil
	}
	for _, claim := range []string{"product_service_revision", "entitlement_revision"} {
		value, exists := claims[claim]
		if !exists {
			continue
		}
		revision, ok := value.(float64)
		if !ok || revision < 0 || revision != float64(int64(revision)) {
			return false, errors.New("token renewal eligibility unavailable")
		}
		if revision > 0 {
			return true, nil
		}
	}
	return false, nil
}

func probeSyntheticAuth(ctx context.Context, cfg Config, rt Runtime, probe AuthProbe, now time.Time) (r Result) {
	r = result(probe.Kind, "synthetic/auth/"+probe.Name, "functional", true, now)
	r.Source = "dedicated authentication probe"
	start := time.Now()
	defer func() { r.DurationMS = time.Since(start).Milliseconds() }()
	if ctx.Err() != nil {
		r.Status = NotRun
		r.Reason = "功能測試執行期限已到"
		return r
	}
	credentials, err := readSyntheticCredentials(rt, probe.CredentialsFile)
	if err != nil {
		r.Reason = "缺少標記 dedicated=true 的專用監看身分"
		return r
	}
	if probe.Kind == "account-manager" && (!syntheticURL(probe.RefreshURL, "/v1/auth/refresh") || !syntheticURL(probe.ValidationURL, "/v1/me") || (probe.LoginURL != "" && !syntheticURL(probe.LoginURL, "/v1/auth/login"))) {
		r.Reason = "Account Manager 探測須使用 login、refresh 與受保護的 /v1/me 合約"
		return r
	}
	if probe.Kind == "video-cloud" && (!syntheticURL(probe.LoginURL, "/request_token") || !syntheticURL(probe.RefreshURL, "/refresh_token") || credentials.ClientCertFile == "" || credentials.ClientKeyFile == "" || (credentials.Scope != "device" && credentials.Scope != "camera" && credentials.Scope != "app")) {
		r.Reason = "Video Cloud 重新認證需要專用 mTLS 憑證、scope 與合約端點"
		return r
	}
	validationEndpoint, parseErr := url.Parse(probe.ValidationURL)
	if parseErr != nil || validURL(probe.ValidationURL) != nil || validationEndpoint.Path == "/healthz" || validationEndpoint.Path == "/readyz" || strings.HasPrefix(validationEndpoint.Path, "/metrics") {
		r.Reason = "驗證 endpoint 必須為受保護的唯讀 API"
		return r
	}
	client, err := HTTPClient(probe.CAFile, credentials.ClientCertFile, credentials.ClientKeyFile, rt, timeout(cfg))
	if err != nil {
		r.Reason = "專用認證 trust 或 mTLS 設定無效"
		return r
	}
	defer client.CloseIdleConnections()
	// Check the same mTLS client without a bearer before any login or rotation.
	// A public endpoint, or one authorized solely by the client certificate,
	// cannot establish that the newly issued bearer token is accepted.
	control, err := http.NewRequestWithContext(ctx, http.MethodGet, probe.ValidationURL, nil)
	if err != nil {
		r.Reason = "受保護的唯讀驗證 endpoint 無效"
		return r
	}
	denied, err := client.Do(control)
	if err != nil {
		r.Reason = "受保護 API 無法完成未授權控制檢查"
		return r
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(denied.Body, 4096))
	denied.Body.Close()
	if denied.StatusCode != http.StatusUnauthorized && denied.StatusCode != http.StatusForbidden {
		r.Reason = "驗證 endpoint 未要求 bearer token，無法證明新 token 有效"
		return r
	}
	state, exists, err := readSyntheticState(rt, probe)
	if err != nil {
		r.Reason = "專用 token 輪替狀態無法安全讀取"
		return r
	}
	tokens := state.Tokens
	if !exists {
		tokens.RefreshToken = credentials.RefreshToken
	}
	needsBootstrap := probe.Kind == "video-cloud" || tokens.RefreshToken == "" || (exists && !tokens.RefreshTokenExpiresAt.After(now))
	if needsBootstrap {
		var payload any
		if probe.Kind == "account-manager" {
			if credentials.Email == "" || credentials.Password == "" || probe.LoginURL == "" {
				r.Reason = "專用帳號尚未提供 login 資料或有效 refresh grant"
				return r
			}
			payload = map[string]string{"email": credentials.Email, "password": credentials.Password}
		} else {
			payload = map[string]string{"scope": credentials.Scope, "devid": credentials.DeviceID, "service": credentials.Service}
		}
		raw, code, err := syntheticJSON(ctx, client, probe.LoginURL, payload)
		if err != nil || code != http.StatusOK {
			r.Status = Fail
			r.Reason = "專用身分登入／mTLS 重新認證失敗"
			return r
		}
		tokens, err = decodeSyntheticTokens(raw, probe.Kind)
		if err != nil || !tokens.AccessTokenExpiresAt.After(now) {
			r.Status = Fail
			r.Reason = "重新認證回應缺少有效 token 與實際效期"
			return r
		}
		if err := saveSyntheticState(rt, probe, tokens, now); err != nil {
			r.Reason = "新 token 無法安全保存；停止更新流程"
			return r
		}
	}
	reauthenticationOnly := false
	if probe.Kind == "video-cloud" {
		reauthenticationOnly, err = videoRequiresReauthentication(tokens.AccessToken)
		if err != nil {
			r.Status = Fail
			r.Reason = "Video Cloud token 的續期合約資訊無效"
			return r
		}
	}
	rotated := tokens
	if !reauthenticationOnly {
		grant := tokens.RefreshToken
		if probe.Kind == "video-cloud" {
			grant = tokens.AccessToken
		}
		raw, code, err := syntheticJSON(ctx, client, probe.RefreshURL, map[string]string{"refresh_token": grant})
		if err != nil || code != http.StatusOK {
			r.Status = Fail
			r.Reason = "專用 token 更新／重新簽發失敗"
			return r
		}
		rotated, err = decodeSyntheticTokens(raw, probe.Kind)
		if err != nil || !rotated.AccessTokenExpiresAt.After(now) || (probe.Kind == "account-manager" && (rotated.RefreshToken == grant || !rotated.RefreshTokenExpiresAt.After(now))) {
			r.Status = Fail
			r.Reason = "更新回應缺少新 grant 或有效 token 效期"
			return r
		}
		// Persist the new grant before validation: the old AM grant has already
		// been consumed even if the following protected read fails.
		if err := saveSyntheticState(rt, probe, rotated, now); err != nil {
			r.Reason = "已輪替 grant 無法安全保存；需要重新取得專用身分"
			return r
		}
	}
	if validURL(probe.ValidationURL) != nil {
		r.Reason = "受保護的唯讀驗證 endpoint 未配置"
		return r
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, probe.ValidationURL, nil)
	if err != nil {
		r.Reason = "受保護的唯讀驗證 endpoint 無效"
		return r
	}
	req.Header.Set("Authorization", "Bearer "+rotated.AccessToken)
	resp, err := client.Do(req)
	if err != nil {
		r.Status = Fail
		r.Reason = "更新後的受保護 API 無法連線"
		return r
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(body) > 1<<20 || resp.StatusCode < 200 || resp.StatusCode >= 300 || looksLikeLogin(resp, body) || !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") || !json.Valid(body) {
		r.Status = Fail
		r.Reason = "更新後 token 未通過受保護的唯讀 API 驗證"
		return r
	}
	r.Status = Pass
	r.Reason = "專用身分重新認證／更新成功，新 token 已保存並通過受保護 API"
	exp := rotated.AccessTokenExpiresAt
	r.ExpiresAt = &exp
	r.DurationMS = time.Since(start).Milliseconds()
	return r
}
