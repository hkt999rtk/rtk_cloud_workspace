package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type consoleCheckResult struct {
	Check  string `json:"check"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type consoleCheckClient struct {
	origin string
	http   *http.Client
}

// Deliberately contains no deployment, provider-refresh, payment or device writes.
// The only POST creates this check's own login session; cookies remain in memory.
func (c consoleCheckClient) request(ctx context.Context, method, path string, payload any) ([]byte, http.Header, error) {
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return nil, nil, errors.New("check requires an origin-relative path")
	}
	var reader io.Reader
	if payload != nil {
		body, err := json.Marshal(payload)
		if err != nil {
			return nil, nil, errors.New("encode check request")
		}
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.origin+path, reader)
	if err != nil {
		return nil, nil, errors.New("invalid check request")
	}
	if method == http.MethodPost {
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Origin", c.origin)
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, nil, errors.New("request failed or redirected; response and credentials suppressed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, nil, fmt.Errorf("HTTP %d (body suppressed)", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, 4*1024*1024+1))
	if err != nil || len(data) > 4*1024*1024 {
		return nil, nil, errors.New("response unreadable or exceeds 4 MiB")
	}
	return data, resp.Header, nil
}

func (c consoleCheckClient) json(ctx context.Context, path string, out any) (http.Header, error) {
	body, header, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	if !strings.Contains(header.Get("Content-Type"), "application/json") || json.Unmarshal(body, out) != nil {
		return nil, errors.New("expected JSON, not an HTML/login fallback")
	}
	return header, nil
}

func runDeploymentConsoleCheck(args []string) error {
	fs := flag.NewFlagSet("deployment console-check", flag.ContinueOnError)
	environment := fs.String("environment", "", "selected environment")
	workspace := fs.String("workspace", "", "workspace root")
	cloud := fs.String("cloud-id", "", "owned qualification Cloud UUID (no data is created)")
	product := fs.String("product-id", "", "qualification Product UUID for enabled Test Lab")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New("unexpected console-check argument")
	}
	idPattern := regexp.MustCompile(`^[0-9a-fA-F]{8}(-[0-9a-fA-F]{4}){3}-[0-9a-fA-F]{12}$`)
	if !idPattern.MatchString(*cloud) || (*product != "" && !idPattern.MatchString(*product)) {
		return errors.New("--cloud-id must be a UUID; --product-id must be a UUID when provided")
	}
	cfg, err := resolveDeploymentConfig(*workspace, *environment, "")
	if err != nil {
		return err
	}
	endpoint := "admin." + cfg.Values["CLOUD_STACK_NAME"] + "." + cfg.Values["CLOUD_DNS_ROOT_DOMAIN"]
	// Domain is trusted deployment configuration, never a provider-supplied URL.
	origin := "https://" + endpoint
	u, err := url.Parse(origin)
	if err != nil || u.Hostname() == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("invalid configured Admin origin")
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	password, err := store.readRuntime("platform-admin")
	if err != nil || password == "" {
		return errors.New("selected-environment runtime/platform-admin is missing or unreadable; no credential fallback")
	}
	email := "platform-admin@" + lkeName(cfg.Values["CLOUD_STACK_NAME"]) + ".local"
	if v, e := store.read(filepath.Join("operator", "env", "ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL")); e == nil && v != "" {
		email = v
	} else if e != nil && !os.IsNotExist(e) {
		return errors.New("canonical platform-admin email override is unreadable")
	}
	jar, _ := cookiejar.New(nil)
	client := consoleCheckClient{origin, &http.Client{Jar: jar, Timeout: 20 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	results := collectConsoleChecks(ctx, client, cfg.Values, email, password, *cloud, *product)
	if err := json.NewEncoder(os.Stdout).Encode(struct {
		Environment string               `json:"environment"`
		Checks      []consoleCheckResult `json:"checks"`
		Unverified  []string             `json:"unverified"`
	}{cfg.Environment, results, []string{"browser layout/WebGL/video playback", "external OAuth round trip", "non-empty MQTT -> persisted logs -> billing facts", "app and device mTLS independently", "Fleet/Analytics data semantics"}}); err != nil {
		return err
	}
	for _, r := range results {
		if r.Status != "PASS" {
			return errors.New("Console checks failed or incomplete; this is not release sign-off")
		}
	}
	return nil
}

func collectConsoleChecks(ctx context.Context, c consoleCheckClient, desired map[string]string, email, password, cloud, product string) []consoleCheckResult {
	var results []consoleCheckResult
	check := func(name string, run func() error) bool {
		err := run()
		r := consoleCheckResult{name, "PASS", "verified"}
		if err != nil {
			r.Status = "FAIL"
			r.Detail = err.Error()
		}
		results = append(results, r)
		return err == nil
	}
	check("social providers", func() error {
		var body struct {
			Providers []struct {
				ID string `json:"id"`
			} `json:"providers"`
		}
		if _, err := c.json(ctx, "/api/auth/social/providers", &body); err != nil {
			return err
		}
		found := map[string]bool{}
		for _, p := range body.Providers {
			found[p.ID] = true
		}
		for _, p := range []string{"google", "github"} {
			if strings.EqualFold(desired[strings.ToUpper(p)+"_LOGIN_ENABLED"], "true") && !found[p] {
				return fmt.Errorf("expected %s login is absent", p)
			}
		}
		return nil
	})
	login := func(next string) error {
		_, _, err := c.request(ctx, http.MethodPost, "/api/auth/login", map[string]string{"email": email, "password": password, "next": next})
		return err
	}
	if check("platform login", func() error { return login("/admin/chipset-providers") }) {
		check("published chipset snapshot and Board assets", func() error { return checkConsoleChipsets(ctx, c) })
	} else {
		results = append(results, consoleCheckResult{"published chipset snapshot and Board assets", "SKIP", "platform login failed"})
	}
	if !check("developer login", func() error { return login("/console/clouds") }) {
		return append(results, consoleCheckResult{"developer SDK/Billing/Test Lab", "SKIP", "developer login failed"})
	}
	check("SDK release catalog", func() error {
		var body struct {
			Source  string `json:"source_status"`
			Catalog struct {
				Packages []json.RawMessage `json:"packages"`
			} `json:"catalog"`
		}
		if _, err := c.json(ctx, "/api/developer/sdk-releases/latest", &body); err != nil {
			return err
		}
		if body.Source != "available" || len(body.Catalog.Packages) == 0 {
			return errors.New("SDK release catalog unavailable or empty")
		}
		return nil
	})
	check("Billing reads and ownership", func() error {
		version := ""
		for _, name := range []string{"account", "summary", "usage", "invoices", "activity"} {
			var body map[string]json.RawMessage
			header, err := c.json(ctx, "/api/developer/brand-clouds/"+cloud+"/billing/"+name, &body)
			if err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
			v := header.Get("X-Cloud-Ownership-Version")
			if v == "" || (version != "" && version != v) {
				return errors.New("missing or inconsistent Cloud ownership version")
			}
			version = v
			if name == "account" {
				var account struct {
					Organization string `json:"organization_id"`
				}
				if json.Unmarshal(body["account"], &account) != nil || account.Organization != cloud {
					return errors.New("Billing account belongs to a different or unknown Cloud")
				}
			}
		}
		return nil
	})
	if strings.EqualFold(desired["TEST_LAB_ENABLED"], "true") {
		if product == "" {
			results = append(results, consoleCheckResult{"Test Lab read route", "SKIP", "enabled Test Lab requires --product-id"})
		} else {
			check("Test Lab read route", func() error {
				var body map[string]json.RawMessage
				_, err := c.json(ctx, "/api/developer/brand-clouds/"+cloud+"/test-lab/manage/devices?product_id="+product, &body)
				return err
			})
		}
	}
	return results
}

func checkConsoleChipsets(ctx context.Context, c consoleCheckClient) error {
	const manifestPath = "/assets/chipset-packages/realtek-amebapro2.json"
	var list struct {
		Providers []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
			URL    string `json:"manifest_url"`
			SHA    string `json:"manifest_sha256"`
		} `json:"providers"`
	}
	if _, err := c.json(ctx, "/api/admin/chipset-providers", &list); err != nil {
		return err
	}
	matched := 0
	for _, p := range list.Providers {
		if p.URL != c.origin+manifestPath || p.Status != "published" {
			continue
		}
		matched++
		raw, _, err := c.request(ctx, http.MethodGet, manifestPath, nil)
		if err != nil {
			return err
		}
		if fmt.Sprintf("%x", sha256.Sum256(raw)) != p.SHA {
			return errors.New("published first-party snapshot is stale; review and refresh that provider, then rerun")
		}
		var snapshot struct {
			Chipsets []struct {
				Boards []struct {
					Model struct {
						Asset string `json:"asset_path"`
					} `json:"model"`
					Resources []struct {
						Type string `json:"type"`
					} `json:"resources"`
				} `json:"boards"`
			} `json:"chipsets"`
		}
		if _, err := c.json(ctx, "/api/admin/chipset-providers/"+url.PathEscape(p.ID), &snapshot); err != nil {
			return err
		}
		boards, videos := 0, 0
		for _, chip := range snapshot.Chipsets {
			for _, board := range chip.Boards {
				boards++
				asset := board.Model.Asset
				if !strings.HasPrefix(asset, "/assets/boards/") || !strings.HasSuffix(asset, ".glb") || strings.Contains(asset, "..") {
					return errors.New("missing or unsafe Board model path")
				}
				if _, header, err := c.request(ctx, http.MethodHead, asset, nil); err != nil {
					return fmt.Errorf("Board model: %w", err)
				} else if strings.Contains(header.Get("Content-Type"), "text/html") {
					return errors.New("Board model returned an HTML fallback")
				}
				for _, r := range board.Resources {
					if r.Type == "video" {
						videos++
					}
				}
			}
		}
		if boards == 0 || videos == 0 {
			return errors.New("published snapshot has no Boards or Board videos")
		}
	}
	if matched != 1 {
		return errors.New("expected exactly one published first-party AmebaPRO2 provider")
	}
	return nil
}
