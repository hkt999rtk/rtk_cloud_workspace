package cloudmonitor

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func CollectHTTP(ctx context.Context, cfg Config, inv Inventory, rt Runtime, now time.Time) Collection {
	checks := append([]HTTPCheck{}, cfg.HTTP...)
	seen := map[string]bool{}
	for _, h := range checks {
		seen[h.Service+"/"+h.URL] = true
	}
	for _, e := range inv.Endpoints {
		fullURL := strings.TrimRight(e.URL, "/") + e.Path
		if !seen[e.Service+"/"+fullURL] {
			checks = append(checks, HTTPCheck{Name: "inventory-endpoint", Service: e.Service, URL: strings.TrimRight(e.URL, "/") + e.Path, SuccessCodes: e.SuccessCodes, Required: true, LivenessOnly: e.LivenessOnly})
		}
	}
	tasks := make([]func(context.Context) Collection, 0, len(checks))
	for _, h := range checks {
		h := h
		tasks = append(tasks, func(c context.Context) Collection {
			single := cfg
			single.HTTP = []HTTPCheck{h}
			return collectHTTPChecks(c, single, rt, now)
		})
	}
	return parallelCollections(ctx, cfg, tasks)
}
func collectHTTPChecks(ctx context.Context, cfg Config, rt Runtime, now time.Time) Collection {
	checks := cfg.HTTP
	var out Collection
	for _, h := range checks {
		r := result(h.Service, "http/"+h.Name, "readiness", h.Required, now)
		if h.LivenessOnly {
			r.Layer = "liveness"
		}
		r.Source = "http"
		client, err := HTTPClient(h.CAFile, h.ClientCertFile, h.ClientKeyFile, rt, timeout(cfg))
		if err != nil {
			r.Reason = "HTTP client 憑證設定無效"
			out.Results = append(out.Results, r)
			continue
		}
		client.CheckRedirect = func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }
		c, cancel := context.WithTimeout(ctx, timeout(cfg))
		req, err := http.NewRequestWithContext(c, http.MethodGet, h.URL, nil)
		if err != nil {
			cancel()
			r.Reason = "HTTP endpoint 設定無效"
			out.Results = append(out.Results, r)
			continue
		}
		if h.TokenFile != "" {
			token, err := ReadCredential(rt, h.TokenFile)
			if err != nil {
				cancel()
				r.Reason = "HTTP 授權資料無法讀取"
				out.Results = append(out.Results, r)
				continue
			}
			req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(token)))
		}
		start := time.Now()
		resp, err := client.Do(req)
		r.DurationMS = time.Since(start).Milliseconds()
		if err != nil {
			r.Status = Fail
			r.Reason = "HTTP 連線、TLS 驗證或逾時失敗"
		} else {
			body, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20+1))
			resp.Body.Close()
			codes := h.SuccessCodes
			if len(codes) == 0 {
				codes = []int{200}
			}
			valid := false
			for _, code := range codes {
				valid = valid || resp.StatusCode == code
			}
			r.Status = Pass
			r.Reason = "HTTP 狀態與內容符合預期"
			switch {
			case resp.StatusCode == 401 || resp.StatusCode == 403:
				r.Status = Unknown
				r.Reason = "HTTP 授權或權限不足"
			case !valid:
				r.Status = Fail
				r.Reason = "HTTP 狀態碼不符合預期"
			case readErr != nil || len(body) > 1<<20:
				r.Status = Unknown
				r.Reason = "HTTP 回應超限或無法完整讀取"
			case h.Contains != "" && !strings.Contains(string(body), h.Contains):
				r.Status = Fail
				r.Reason = "HTTP 回應缺少預期內容"
			case looksLikeLogin(resp, body) && !strings.Contains(strings.ToLower(h.Service), "frontend"):
				r.Status = Fail
				r.Reason = "HTTP 回傳登入頁或 frontend fallback"
			case h.MaxLatencyMS > 0 && float64(r.DurationMS) > h.MaxLatencyMS:
				r.Status = Warn
				r.Reason = "HTTP 探測延遲超過設定目標"
			}
			if r.Status == Pass && h.LivenessOnly {
				r.Reason = "HTTP 存活檢查通過；依賴與功能須另行驗證"
			}
		}
		cancel()
		out.Results = append(out.Results, r)
		out.Samples = append(out.Samples, Sample{Service: h.Service, Name: "http_probe_latency_ms", Value: float64(r.DurationMS), Unit: "ms", Kind: "gauge", ObservedAt: now, Identity: h.Name})
	}
	return out
}
func looksLikeLogin(resp *http.Response, b []byte) bool {
	ct := strings.ToLower(resp.Header.Get("Content-Type"))
	s := strings.ToLower(string(b))
	return strings.Contains(ct, "text/html") && (strings.Contains(s, "type=\"password\"") || strings.Contains(s, "type='password'") || strings.Contains(s, "<div id=\"root\"") || strings.Contains(s, "<div id=\"app\""))
}
func parseCertificates(b []byte) ([]*x509.Certificate, error) {
	var certs []*x509.Certificate
	for {
		block, rest := pem.Decode(b)
		if block == nil {
			break
		}
		b = rest
		if block.Type != "CERTIFICATE" {
			continue
		}
		c, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		certs = append(certs, c)
	}
	if len(certs) == 0 {
		return nil, x509.IncorrectPasswordError
	}
	return certs, nil
}
func fingerprintCert(c *x509.Certificate) string {
	sum := sha256.Sum256(c.Raw)
	return hex.EncodeToString(sum[:])
}
func expiryStatus(start, end, now time.Time, warn, critical float64) (Status, string) {
	if now.Before(start) {
		return Fail, "尚未生效"
	}
	if !end.After(now) {
		return Fail, "已過期"
	}
	days := end.Sub(now).Hours() / 24
	if days <= critical {
		return Warn, "效期已進入緊急更新窗口"
	}
	if days <= warn {
		return Warn, "即將到期"
	}
	return Pass, "效期充足"
}

func CollectTLS(ctx context.Context, cfg Config, rt Runtime, now time.Time) Collection {
	var tasks []func(context.Context) Collection
	for _, t := range cfg.TLS {
		t := t
		tasks = append(tasks, func(c context.Context) Collection {
			single := cfg
			single.TLS = []TLSCheck{t}
			return collectTLSChecks(c, single, rt, now)
		})
	}
	return parallelCollections(ctx, cfg, tasks)
}
func collectTLSChecks(ctx context.Context, cfg Config, rt Runtime, now time.Time) Collection {
	var out Collection
	for _, t := range cfg.TLS {
		r := result(t.Service, "tls/"+t.Name, "credential", t.Required, now)
		r.Source = "live-tls"
		if t.Address == "" {
			r.Source = "stored-certificate"
		}
		if t.ClientCertFile != "" {
			identity := collectMTLSIdentity(t, rt, now)
			if t.ApplicationURL != "" {
				failed := false
				for _, check := range identity.Results {
					failed = failed || check.Status == Fail
				}
				if !failed {
					for i := range identity.Results {
						if identity.Results[i].CheckID == "tls/"+t.Name+"/mtls-acceptance" {
							identity.Results[i] = collectMTLSApplication(ctx, cfg, t, rt, now)
						}
					}
				}
			}
			out = Merge(out, identity)
		}
		var certs []*x509.Certificate
		var verifiedChains [][]*x509.Certificate
		var verifyErr error
		if t.Address != "" {
			client, err := HTTPClient(t.CAFile, t.ClientCertFile, t.ClientKeyFile, rt, timeout(cfg))
			if err != nil {
				r.Reason = "TLS trust/client 憑證設定無效"
				out.Results = append(out.Results, r)
				continue
			}
			transport := client.Transport.(*http.Transport)
			tc := transport.TLSClientConfig.Clone()
			tc.ServerName = t.ServerName
			d := tls.Dialer{NetDialer: &net.Dialer{Timeout: timeout(cfg)}, Config: tc}
			c, cancel := context.WithTimeout(ctx, timeout(cfg))
			conn, err := d.DialContext(c, "tcp", t.Address)
			cancel()
			if err != nil {
				r.Status = Fail
				r.Reason = "TLS 握手、主機名稱或信任鏈驗證失敗"
				out.Results = append(out.Results, r)
				continue
			}
			state := conn.(*tls.Conn).ConnectionState()
			certs = state.PeerCertificates
			verifiedChains = state.VerifiedChains
			conn.Close()
		} else if t.CertFile != "" {
			raw, err := ReadPublicFile(rt, t.CertFile)
			if err == nil {
				certs, err = parseCertificates(raw)
			}
			if err != nil {
				r.Reason = "憑證檔案缺少或无法解析"
				out.Results = append(out.Results, r)
				continue
			}
			roots := x509.NewCertPool()
			if t.CAFile != "" {
				raw, err = ReadPublicFile(rt, t.CAFile)
				if err != nil || !roots.AppendCertsFromPEM(raw) {
					r.Reason = "信任鏈資料無法解析"
					out.Results = append(out.Results, r)
					continue
				}
			}
			inter := x509.NewCertPool()
			for _, c := range certs[1:] {
				inter.AddCert(c)
			}
			uses := []x509.ExtKeyUsage{x509.ExtKeyUsageAny}
			if t.ServerName != "" {
				uses = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
			}
			if t.Role == "ca" {
				if !certs[0].IsCA || certs[0].KeyUsage&x509.KeyUsageCertSign == 0 {
					verifyErr = x509.CertificateInvalidError{Cert: certs[0], Reason: x509.NotAuthorizedToSign}
				} else if t.CAFile == "" {
					verifyErr = certs[0].CheckSignatureFrom(certs[0])
				} else {
					verifiedChains, verifyErr = certs[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: now, KeyUsages: uses})
				}
			} else {
				if t.CAFile == "" {
					roots = nil
				}
				verifiedChains, verifyErr = certs[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: inter, CurrentTime: now, DNSName: t.ServerName, KeyUsages: uses})
			}
		} else {
			r.Reason = "沒有 live TLS 端點或憑證來源"
			out.Results = append(out.Results, r)
			continue
		}
		if len(certs) == 0 {
			r.Reason = "TLS 端點沒有憑證"
			out.Results = append(out.Results, r)
			continue
		}
		r.Status = Pass
		r.Reason = "憑證信任鏈與用途驗證通過"
		if t.Address == "" {
			r.Reason = "保存憑證信任鏈與用途驗證通過；尚未證明服務載入版本"
		}
		if verifyErr != nil {
			r.Status = Fail
			r.Reason = "憑證信任鏈、主機名稱或用途驗證失敗"
		}
		r.Fingerprint = fingerprintCert(certs[0])
		exp := certs[0].NotAfter
		r.ExpiresAt = &exp
		if t.ExpectedFingerprint != "" && !strings.EqualFold(t.ExpectedFingerprint, r.Fingerprint) {
			r.Status = Fail
			r.Reason = "憑證與預期 fingerprint 不符"
		}
		out.Results = append(out.Results, r)
		// Root CAs are commonly absent from the served chain; inspect the roots actually used by verification.
		seen := map[string]bool{}
		for _, c := range certs {
			seen[fingerprintCert(c)] = true
		}
		for _, chain := range verifiedChains {
			for _, c := range chain {
				if !seen[fingerprintCert(c)] {
					certs = append(certs, c)
					seen[fingerprintCert(c)] = true
				}
			}
		}
		for i, c := range certs {
			role := t.Role
			if c.IsCA {
				role = "ca"
			}
			warn, critical := 30.0, 7.0
			if role == "ca" {
				warn, critical = 180, 90
			}
			if t.WarnDays > 0 && i == 0 {
				warn = t.WarnDays
			}
			if t.CriticalDays > 0 && i == 0 {
				critical = t.CriticalDays
			}
			e := result(t.Service, "tls/"+t.Name+"/expiry/"+fingerprintCert(c)[:12], "credential", t.Required, now)
			e.Source = r.Source
			e.Fingerprint = fingerprintCert(c)
			exp := c.NotAfter
			e.ExpiresAt = &exp
			e.Value = numeric(exp.Sub(now).Hours() / 24)
			e.Unit = "days"
			e.Threshold = "renewal window"
			if role == "managed" && t.WarnDays == 0 {
				if now.Before(c.NotBefore) || !c.NotAfter.After(now) {
					e.Status = Fail
					e.Reason = "managed certificate 尚未有效或已過期"
				} else {
					e.Status = Unknown
					e.Reason = "短效憑證需配置續期窗口與更新證據"
				}
			} else {
				e.Status, e.Reason = expiryStatus(c.NotBefore, c.NotAfter, now, warn, critical)
			}
			out.Results = append(out.Results, e)
		}
		if len(t.CRLFiles) > 0 {
			out = Merge(out, collectCRLs(t, certs, rt, now))
		}
		if t.Role == "managed" || t.RenewalFile != "" {
			e := result(t.Service, "tls/"+t.Name+"/renewal", "credential", t.Required, now)
			raw, err := ReadPublicFile(rt, t.RenewalFile)
			var renewal struct {
				LastSucceeded        time.Time `json:"last_succeeded_at"`
				LastFailed           time.Time `json:"last_failed_at"`
				InstalledFingerprint string    `json:"installed_fingerprint"`
			}
			if err != nil || json.Unmarshal(raw, &renewal) != nil || renewal.LastSucceeded.IsZero() {
				e.Reason = "憑證更新與載入結果缺少"
			} else if renewal.LastSucceeded.After(now) || renewal.LastFailed.After(now) {
				e.Reason = "憑證更新紀錄時間在未來，無法作為成功證據"
			} else if t.Address == "" {
				e.Source = "stored-certificate"
				e.Reason = "保存憑證與更新紀錄未提供線上載入版本證據"
			} else if renewal.LastFailed.After(renewal.LastSucceeded) {
				e.Status = Warn
				e.Reason = "最近一次憑證更新失敗"
			} else if !strings.EqualFold(renewal.InstalledFingerprint, r.Fingerprint) {
				e.Status = Fail
				e.Reason = "更新紀錄與使用中憑證不符"
			} else {
				e.Status = Pass
				e.Reason = "更新紀錄與使用中版本相符"
			}
			out.Results = append(out.Results, e)
		}
	}
	return out
}
func collectCRLs(t TLSCheck, certs []*x509.Certificate, rt Runtime, now time.Time) Collection {
	var out Collection
	issuers := append([]*x509.Certificate{}, certs...)
	if t.CAFile != "" {
		if raw, err := ReadPublicFile(rt, t.CAFile); err == nil {
			more, _ := parseCertificates(raw)
			issuers = append(issuers, more...)
		}
	}
	for i, path := range t.CRLFiles {
		r := result(t.Service, "tls/"+t.Name+"/crl/"+string(rune('a'+i)), "credential", t.Required, now)
		raw, err := ReadPublicFile(rt, path)
		if b, _ := pem.Decode(raw); b != nil {
			raw = b.Bytes
		}
		crl, e := x509.ParseRevocationList(raw)
		if err != nil || e != nil {
			r.Reason = "CRL 缺少或無法解析"
			out.Results = append(out.Results, r)
			continue
		}
		verified := false
		var issuer *x509.Certificate
		for _, c := range issuers {
			if bytes.Equal(crl.RawIssuer, c.RawSubject) && crl.CheckSignatureFrom(c) == nil {
				verified = true
				issuer = c
				break
			}
		}
		r.Status = Fail
		r.Reason = "CRL 簽章或 issuer 驗證失敗"
		if verified {
			r.Status, r.Reason = expiryStatus(crl.ThisUpdate, crl.NextUpdate, now, 2, 1)
			for _, c := range certs {
				if issuer != nil && bytes.Equal(c.RawIssuer, issuer.RawSubject) && c.CheckSignatureFrom(issuer) == nil {
					for _, entry := range crl.RevokedCertificateEntries {
						if c.SerialNumber.Cmp(entry.SerialNumber) == 0 {
							r.Status = Fail
							r.Reason = "使用中憑證已撤銷"
						}
					}
				}
			}
		}
		exp := crl.NextUpdate
		r.ExpiresAt = &exp
		out.Results = append(out.Results, r)
	}
	return out
}

// Client identity validity is independent of the server certificate and of application acceptance.
func collectMTLSIdentity(t TLSCheck, rt Runtime, now time.Time) Collection {
	var out Collection
	r := result(t.Service, "tls/"+t.Name+"/client-chain", "credential", t.Required, now)
	r.Source = "configured-client-identity"
	acceptance := result(t.Service, "tls/"+t.Name+"/mtls-acceptance", "readiness", t.Required, now)
	acceptance.Source = "application-probe-required"
	acceptance.Reason = "TLS 握手完成未證明服務接受 client 身分；需有認證要求的應用探測證據"
	out.Results = append(out.Results, acceptance)
	raw, err := ReadPublicFile(rt, t.ClientCertFile)
	var certs []*x509.Certificate
	if err == nil {
		certs, err = parseCertificates(raw)
	}
	if err != nil {
		r.Reason = "client 憑證無法讀取或解析"
		out.Results = append(out.Results, r)
		return out
	}
	roots := x509.NewCertPool()
	caFile := t.ClientCAFile
	if caFile == "" {
		caFile = t.CAFile
	}
	if caFile == "" {
		r.Reason = "缺少 client issuer trust bundle"
	} else if raw, e := ReadPublicFile(rt, caFile); e != nil || !roots.AppendCertsFromPEM(raw) {
		r.Reason = "client issuer trust bundle 無法解析"
	} else {
		intermediates := x509.NewCertPool()
		for _, c := range certs[1:] {
			intermediates.AddCert(c)
		}
		chains, e := certs[0].Verify(x509.VerifyOptions{Roots: roots, Intermediates: intermediates, CurrentTime: now, KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}})
		if e != nil {
			r.Status = Fail
			r.Reason = "client 憑證 chain、ClientAuth 用途或有效期驗證失敗"
		} else {
			r.Status = Pass
			r.Reason = "client 憑證 chain 與 ClientAuth 用途驗證通過"
			seen := map[string]bool{}
			for _, c := range certs {
				seen[fingerprintCert(c)] = true
			}
			for _, chain := range chains {
				for _, c := range chain {
					if !seen[fingerprintCert(c)] {
						certs = append(certs, c)
						seen[fingerprintCert(c)] = true
					}
				}
			}
		}
	}
	r.Fingerprint = fingerprintCert(certs[0])
	expiry := certs[0].NotAfter
	r.ExpiresAt = &expiry
	out.Results = append(out.Results, r)
	for _, c := range certs {
		e := result(t.Service, "tls/"+t.Name+"/client-expiry/"+fingerprintCert(c)[:12], "credential", t.Required, now)
		e.Source = "configured-client-identity"
		warn, critical := 30.0, 7.0
		if c.IsCA {
			warn, critical = 180, 90
		}
		e.Status, e.Reason = expiryStatus(c.NotBefore, c.NotAfter, now, warn, critical)
		e.Fingerprint = fingerprintCert(c)
		expiry := c.NotAfter
		e.ExpiresAt = &expiry
		e.Value = numeric(expiry.Sub(now).Hours() / 24)
		e.Unit = "days"
		out.Results = append(out.Results, e)
	}
	crlFiles := t.ClientCRLFiles
	if len(crlFiles) == 0 {
		crlFiles = t.CRLFiles
	}
	if len(crlFiles) > 0 {
		client := t
		client.Name = t.Name + "/client"
		client.CAFile = caFile
		client.CRLFiles = crlFiles
		checks := collectCRLs(client, certs, rt, now)
		for i := range checks.Results {
			checks.Results[i].Source = "configured-client-identity"
		}
		out = Merge(out, checks)
	} else {
		r := result(t.Service, "tls/"+t.Name+"/client-crl", "credential", t.Required, now)
		r.Reason = "client 身分未配置撤銷狀態來源"
		out.Results = append(out.Results, r)
	}
	return out
}

func collectMTLSApplication(ctx context.Context, cfg Config, t TLSCheck, rt Runtime, now time.Time) Result {
	r := result(t.Service, "tls/"+t.Name+"/mtls-acceptance", "readiness", t.Required, now)
	r.Source = "authenticated-application"
	endpoint, err := url.Parse(t.ApplicationURL)
	if err != nil || endpoint.Scheme != "https" {
		r.Reason = "mTLS 應用驗證必須使用 HTTPS endpoint"
		return r
	}
	client, err := HTTPClient(t.CAFile, t.ClientCertFile, t.ClientKeyFile, rt, timeout(cfg))
	if err != nil {
		r.Reason = "mTLS 應用驗證 client identity/trust 設定無法載入"
		return r
	}
	anonymous, err := HTTPClient(t.CAFile, "", "", rt, timeout(cfg))
	if err != nil {
		r.Reason = "mTLS 對照驗證 trust 設定無法載入"
		return r
	}
	token := ""
	if t.ApplicationTokenFile != "" {
		raw, e := ReadCredential(rt, t.ApplicationTokenFile)
		if e != nil {
			r.Reason = "mTLS 應用驗證 bearer 資料無法讀取"
			return r
		}
		token = string(raw)
	}
	c, cancel := context.WithTimeout(ctx, 2*timeout(cfg))
	defer cancel()
	request := func(client *http.Client) (*http.Response, error) {
		req, e := http.NewRequestWithContext(c, http.MethodGet, t.ApplicationURL, nil)
		if e != nil {
			return nil, e
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		return client.Do(req)
	}
	positive, err := request(client)
	if err != nil {
		r.Status = Fail
		r.Reason = "攜帶 client 身分的應用請求失敗"
		return r
	}
	body, err := io.ReadAll(io.LimitReader(positive.Body, 1<<20+1))
	positive.Body.Close()
	if err != nil || len(body) > 1<<20 {
		r.Reason = "mTLS 應用回應超限或無法讀取"
		return r
	}
	if positive.StatusCode != http.StatusOK || looksLikeLogin(positive, body) || (t.ApplicationContains != "" && !strings.Contains(string(body), t.ApplicationContains)) {
		r.Status = Fail
		r.Reason = "攜帶 client 身分的應用回應不符合預期"
		return r
	}
	negative, err := request(anonymous)
	if err != nil {
		if explicitClientCertificateRejection(err) {
			r.Status = Pass
			r.Reason = "應用接受 client 身分，並以明確 TLS alert 拒絕未攜帶 client 憑證的對照請求"
		} else {
			r.Reason = "未攜帶 client 憑證的對照請求失敗原因未證明認證拒絕"
		}
		return r
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(negative.Body, 4096))
	negative.Body.Close()
	if negative.StatusCode == http.StatusUnauthorized || negative.StatusCode == http.StatusForbidden {
		r.Status = Pass
		r.Reason = "應用接受 client 身分，並拒絕同 bearer、未攜帶 client 憑證的對照請求"
	} else {
		r.Reason = "應用未拒絕缺少 client 憑證的對照請求；無法證明 mTLS 身分被接受"
	}
	return r
}
func explicitClientCertificateRejection(err error) bool {
	var alert tls.AlertError
	if errors.As(err, &alert) {
		return uint8(alert) == 116 || uint8(alert) == 42
	}
	var remote *net.OpError
	if errors.As(err, &remote) && remote.Op == "remote error" {
		return remote.Err.Error() == "tls: certificate required" || remote.Err.Error() == "tls: bad certificate"
	}
	return false
}
