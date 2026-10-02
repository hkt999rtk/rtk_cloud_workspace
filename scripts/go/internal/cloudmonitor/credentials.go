package cloudmonitor

import (
	"context"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

func CollectCertificates(ctx context.Context, cfg Config, rt Runtime, runner CommandRunner, now time.Time) Collection {
	return parallelCollections(ctx, cfg, []func(context.Context) Collection{func(c context.Context) Collection { return collectCertificateInventory(c, cfg, rt, runner, now) }})
}
func collectCertificateInventory(ctx context.Context, cfg Config, rt Runtime, runner CommandRunner, now time.Time) Collection {
	r := result("pki", "certificate-inventory", "credential", true, now)
	if cfg.CertificateTool == "" {
		r.Reason = "未配置 certificate-tools；完整憑證 discovery 未執行"
		return Collection{Results: []Result{r}}
	}
	args := []string{"check", "--environment", cfg.Environment, "--workspace", rt.Workspace, "--config-root", filepath.Dir(rt.ConfigRoot), "--json"}
	if cfg.CertificateInventory != "" {
		args = append(args, "--inventory", cfg.CertificateInventory)
	}
	// Discovery reads many live Pod state files; it has its own bounded budget
	// and does not block the independent service/metrics watch groups.
	c, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	raw, code, err := runner.Run(c, cfg.CertificateTool, args, nil, nil)
	completedAt := time.Now().UTC()
	r.ObservedAt = completedAt
	var report struct {
		Environment string    `json:"environment"`
		Stack       string    `json:"stack"`
		CheckedAt   time.Time `json:"checked_at"`
		Results     []struct {
			Source      string    `json:"source"`
			Kind        string    `json:"kind"`
			Status      string    `json:"status"`
			Fingerprint string    `json:"sha256"`
			NotAfter    time.Time `json:"not_after"`
			DaysLeft    float64   `json:"days_left"`
		} `json:"results"`
		Coverage []string `json:"coverage_notes"`
	}
	if len(raw) == 0 || len(raw) > 32<<20 || json.Unmarshal(raw, &report) != nil || report.Environment != cfg.Environment || report.Stack != cfg.Stack || report.CheckedAt.IsZero() || report.CheckedAt.After(completedAt.Add(2*time.Minute)) || completedAt.Sub(report.CheckedAt) > 5*time.Minute || code < 0 || code > 1 || (err != nil && code != 1) {
		r.Reason = "certificate-tools 未完成、輸出格式或環境不符"
		return Collection{Results: []Result{r}}
	}
	var out Collection
	r.Status = Pass
	r.Reason = "完整憑證 discovery 已取得；效期盤點需搭配鏈與撤銷驗證"
	out.Results = append(out.Results, r)
	for _, v := range report.Results {
		identityHash := sha256.Sum256([]byte(v.Source + "/" + v.Kind + "/" + v.Fingerprint))
		saved := strings.HasPrefix(v.Source, "saved/")
		e := result("pki", "certificate-inventory/"+hex.EncodeToString(identityHash[:8]), "credential", !saved, completedAt)
		e.Source = "certificate-tools/" + v.Source + "/" + v.Kind
		e.Fingerprint = v.Fingerprint
		if !v.NotAfter.IsZero() {
			exp := v.NotAfter
			e.ExpiresAt = &exp
			e.Value = numeric(v.NotAfter.Sub(completedAt).Hours() / 24)
			e.Unit = "days"
		}
		switch v.Status {
		case "OK":
			e.Status = Pass
			e.Reason = "盤點效期有效"
		case "RENEW_SOON", "RENEW_NOW":
			e.Status = Warn
			e.Reason = "盤點項目即將到期"
		case "NOT_YET_VALID", "EXPIRED":
			e.Status = Fail
			e.Reason = "盤點項目尚未有效或已過期"
		default:
			e.Status = Unknown
			e.Reason = "盤點項目無法檢查"
		}
		if saved {
			e.Layer = "reference"
			e.Reason = "保存版本：" + e.Reason
		}
		out.Results = append(out.Results, e)
	}
	if len(report.Results) == 0 {
		out.Results[0].Status = Unknown
		out.Results[0].Reason = "憑證 discovery 無結果"
	}
	for i := range report.Coverage {
		e := result("pki", "certificate-coverage/"+strconvInt(i), "coverage", false, completedAt)
		e.Reason = "certificate-tools 已回報覆蓋限制；需補充明確來源"
		out.Results = append(out.Results, e)
	}
	return out
}
func strconvInt(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	n := len(b)
	for i > 0 {
		n--
		b[n] = byte('0' + i%10)
		i /= 10
	}
	return string(b[n:])
}

func CollectTokens(ctx context.Context, cfg Config, rt Runtime, now time.Time) Collection {
	var tasks []func(context.Context) Collection
	for _, t := range cfg.Tokens {
		t := t
		tasks = append(tasks, func(c context.Context) Collection {
			single := cfg
			single.Tokens = []TokenCheck{t}
			return collectTokenChecks(c, single, rt, now)
		})
	}
	return parallelCollections(ctx, cfg, tasks)
}
func collectTokenChecks(ctx context.Context, cfg Config, rt Runtime, now time.Time) Collection {
	var out Collection
	for _, t := range cfg.Tokens {
		r := result(t.Service, "token/"+t.Name+"/validity", "credential", t.Required, now)
		raw, err := ReadCredential(rt, t.TokenFile)
		if err != nil {
			r.Reason = "token 檔案無法讀取"
			out.Results = append(out.Results, r)
			continue
		}
		token := strings.TrimSpace(string(raw))
		var claims jwt.MapClaims
		trustedExpiry := false
		var expiry time.Time
		validatorOptions := []jwt.ParserOption{jwt.WithTimeFunc(func() time.Time { return now }), jwt.WithExpirationRequired(), jwt.WithIssuedAt()}
		if t.Issuer != "" {
			validatorOptions = append(validatorOptions, jwt.WithIssuer(t.Issuer))
		}
		if t.Audience != "" {
			validatorOptions = append(validatorOptions, jwt.WithAudience(t.Audience))
		}
		if t.Kind == "jwt" {
			if t.KeyFile != "" && t.Algorithm != "" {
				verificationAttempted := false
				var key []byte
				if strings.HasPrefix(t.Algorithm, "HS") {
					key, err = ReadCredential(rt, t.KeyFile)
				} else {
					key, err = ReadPublicFile(rt, t.KeyFile)
				}
				if err == nil {
					var parsedKey any
					parsedKey, err = parseJWTKey(key, t.Algorithm)
					if err == nil {
						var parsed *jwt.Token
						verificationAttempted = true
						parsed, err = jwt.Parse(token, func(_ *jwt.Token) (any, error) { return parsedKey, nil }, jwt.WithValidMethods([]string{t.Algorithm}), jwt.WithoutClaimsValidation())
						if err == nil && parsed.Valid {
							claims = parsed.Claims.(jwt.MapClaims)
							trustedExpiry = true
							r.Status = Pass
							r.Reason = "JWT 簽章驗證通過"
						}
					}
				}
				if err != nil {
					r.Status = Unknown
					r.Reason = "JWT 驗證金鑰缺少或無法解析"
					if verificationAttempted {
						r.Status = Fail
						r.Reason = "JWT 簽章或 pinned algorithm 驗證失敗"
					}
				}
			} else {
				r.Reason = "JWT 缺少 pinned key/algorithm；需線上驗證"
			}
			if claims == nil {
				if parsed, _, e := jwt.NewParser().ParseUnverified(token, jwt.MapClaims{}); e == nil {
					claims, _ = parsed.Claims.(jwt.MapClaims)
				}
			}
			if claims != nil {
				claimName := t.SubjectClaim
				if claimName == "" {
					claimName = "sub"
				}
				if t.Subject != "" {
					subject, ok := claims[claimName].(string)
					if !ok || subject != t.Subject {
						r.Status = Fail
						r.Reason = "token subject claim 與設定不符"
					}
				}
				scopes := map[string]bool{}
				switch scope := claims["scope"].(type) {
				case string:
					for _, name := range strings.Fields(scope) {
						scopes[name] = true
					}
				case []any:
					for _, name := range scope {
						if s, ok := name.(string); ok {
							scopes[s] = true
						}
					}
				}
				for _, scope := range t.RequiredScopes {
					if !scopes[scope] {
						r.Status = Fail
						r.Reason = "token 缺少要求的 scope"
					}
				}
			}
		} else {
			r.Reason = "opaque token 需發行方或線上驗證"
		}
		if t.ValidationURL != "" && r.Status != Fail {
			r.Status, r.Reason = validateBearer(ctx, cfg, rt, t, token)
			if t.Kind == "jwt" && r.Status == Pass {
				trustedExpiry = true
			}
		}
		if t.Kind == "jwt" && trustedExpiry && claims != nil {
			if e := jwt.NewValidator(validatorOptions...).Validate(claims); e != nil {
				r.Status = Fail
				r.Reason = "JWT 時間、issuer 或 audience 驗證失敗"
			}
			if exp, e := claims.GetExpirationTime(); e == nil && exp != nil {
				expiry = exp.Time
			}
		}
		meta := struct {
			Expires        time.Time `json:"expires_at"`
			RenewAt        time.Time `json:"renew_at"`
			LastSucceeded  time.Time `json:"last_succeeded_at"`
			LastFailed     time.Time `json:"last_failed_at"`
			IssuerVerified bool      `json:"issuer_verified"`
			TokenSHA256    string    `json:"token_sha256"`
		}{}
		hasMeta := false
		if t.MetadataFile != "" {
			if b, e := ReadPublicFile(rt, t.MetadataFile); e == nil && json.Unmarshal(b, &meta) == nil {
				hasMeta = true
			}
		}
		digest := sha256.Sum256([]byte(token))
		boundMeta := meta.IssuerVerified && strings.EqualFold(meta.TokenSHA256, hex.EncodeToString(digest[:]))
		futureMeta := meta.LastSucceeded.After(now) || meta.LastFailed.After(now)
		if t.Kind == "opaque" && hasMeta && boundMeta && !futureMeta {
			expiry = meta.Expires
			trustedExpiry = true
		}
		out.Results = append(out.Results, r)
		e := result(t.Service, "token/"+t.Name+"/expiry", "credential", t.Required, now)
		if !trustedExpiry || expiry.IsZero() {
			e.Reason = "缺少經驗證且綁定此 token 的到期資訊"
		} else {
			e.ExpiresAt = &expiry
			e.Value = numeric(expiry.Sub(now).Seconds())
			e.Unit = "seconds"
			switch {
			case !expiry.After(now):
				e.Status = Fail
				e.Reason = "token 已過期"
			case t.CriticalSeconds > 0 && expiry.Sub(now) <= time.Duration(t.CriticalSeconds)*time.Second:
				e.Status = Warn
				e.Reason = "token 即將到期，剩餘恢復時間不足"
			case t.WarnSeconds > 0 && expiry.Sub(now) <= time.Duration(t.WarnSeconds)*time.Second:
				e.Status = Warn
				e.Reason = "token 已進入更新窗口"
			case t.WarnSeconds == 0 && (!hasMeta || meta.RenewAt.IsZero()):
				e.Reason = "到期時間已驗證；短效 token 續期窗口未配置"
			default:
				e.Status = Pass
				e.Reason = "尚未到達設定更新期限"
			}
		}
		out.Results = append(out.Results, e)
		renew := result(t.Service, "token/"+t.Name+"/renewal", "credential", t.Required, now)
		switch {
		case futureMeta:
			renew.Reason = "token 更新紀錄時間在未來，無法作為成功證據"
		case !hasMeta || meta.LastSucceeded.IsZero():
			renew.Reason = "缺少最近更新／重新認證結果"
		case t.Kind == "opaque" && !boundMeta:
			renew.Reason = "opaque metadata 未經發行方確認或未綁定此 token"
		case meta.TokenSHA256 != "" && !strings.EqualFold(meta.TokenSHA256, hex.EncodeToString(digest[:])):
			renew.Reason = "更新紀錄屬於其他 token"
		case meta.LastFailed.After(meta.LastSucceeded):
			renew.Status = Warn
			renew.Reason = "最近一次 token 更新失敗"
		case !meta.RenewAt.IsZero() && !meta.RenewAt.After(now):
			renew.Status = Warn
			renew.Reason = "token 更新期限已到，需確認更新"
		default:
			renew.Status = Pass
			renew.Reason = "已有最近成功更新紀錄"
		}
		out.Results = append(out.Results, renew)
	}
	return out
}
func parseJWTKey(raw []byte, algorithm string) (any, error) {
	switch {
	case strings.HasPrefix(algorithm, "HS"):
		return raw, nil
	case strings.HasPrefix(algorithm, "RS") || strings.HasPrefix(algorithm, "PS"):
		return jwt.ParseRSAPublicKeyFromPEM(raw)
	case strings.HasPrefix(algorithm, "ES"):
		return jwt.ParseECPublicKeyFromPEM(raw)
	case algorithm == "EdDSA":
		return jwt.ParseEdPublicKeyFromPEM(raw)
	}
	block, _ := pem.Decode(raw)
	if block != nil {
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err == nil {
			switch key.(type) {
			case *rsa.PublicKey, *ecdsa.PublicKey, ed25519.PublicKey:
				return key, nil
			}
		}
	}
	return nil, x509.IncorrectPasswordError
}
func validateBearer(ctx context.Context, cfg Config, rt Runtime, t TokenCheck, token string) (Status, string) {
	client, err := HTTPClient(t.CAFile, "", "", rt, timeout(cfg))
	if err != nil {
		return Unknown, "token 驗證 trust 設定無效"
	}
	c, cancel := context.WithTimeout(ctx, timeout(cfg))
	defer cancel()
	req, err := http.NewRequestWithContext(c, http.MethodGet, t.ValidationURL, nil)
	if err != nil {
		return Unknown, "token 驗證 URL 無效"
	}
	// A success response from a public health endpoint does not establish token validity.
	control := req.Clone(c)
	denied, controlErr := client.Do(control)
	if controlErr != nil {
		return Unknown, "token 驗證服務無法完成授權控制檢查"
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(denied.Body, 4096))
	denied.Body.Close()
	if denied.StatusCode != http.StatusUnauthorized && denied.StatusCode != http.StatusForbidden {
		return Unknown, "驗證 endpoint 未要求 bearer token，無法證明 token 有效"
	}
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return Unknown, "token 驗證服務無法連線"
	}
	defer resp.Body.Close()
	b, e := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
	if e != nil || len(b) > 1<<20 {
		return Unknown, "token 驗證回應超限或無法讀取"
	}
	if resp.StatusCode == 401 || resp.StatusCode == 403 {
		return Fail, "服務拒絕 token"
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 || looksLikeLogin(resp, b) {
		return Unknown, "驗證 endpoint 未提供有效驗證證據"
	}
	return Pass, "指定服務接受 token"
}
