package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestStorageAccountInventoryPaginates(t *testing.T) {
	requests := 0
	transport := consoleRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		requests++
		body := `{"page":2,"pages":2,"data":[{"label":"second","region":"sg-sin-2"}]}`
		if r.URL.Query().Get("page") == "1" {
			body = `{"page":1,"pages":2,"data":[{"label":"first","region":"us-sea"}]}`
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
	})
	c := defaultDeploymentCredentialChecker()
	c.linodeAPIRoot = "https://example.invalid/v4"
	c.client = &http.Client{Transport: transport}
	var buckets []storageBucketAudit
	if err := c.storageAccountInventory("token", "buckets", &buckets); err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 2 || requests != 2 {
		t.Fatalf("inventory incomplete: %d buckets, %d requests", len(buckets), requests)
	}
}

func TestStorageAuditCLIUsesOnlySelectedEnvironmentAndSanitizesReport(t *testing.T) {
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", t.TempDir())
	t.Setenv("RTK_CLOUD_DEPLOYMENT_CREDENTIAL_ENV_FILE", "")
	t.Setenv("RTK_CLOUD_LINODE_API_ROOT", "https://audit.invalid/v4")
	t.Setenv("LINODE_TOKEN", "ambient-token-must-not-be-used")
	for _, environment := range []string{"dev", "staging"} {
		store, err := newSecretStore("", environment)
		if err != nil {
			t.Fatal(err)
		}
		for name, value := range map[string]string{"LINODE_TOKEN": environment + "-token", "LINODE_MEDIA_OBJ_ACCESS_KEY_ID": environment + "-access", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": environment + "-secret"} {
			if err := store.write(filepath.Join("operator", "env", name), []byte(value), false); err != nil {
				t.Fatal(err)
			}
		}
	}
	oldTransport := http.DefaultTransport
	t.Cleanup(func() { http.DefaultTransport = oldTransport })
	requests := map[string]int{}
	http.DefaultTransport = consoleRoundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodGet {
			t.Fatalf("audit attempted mutation: %s", r.Method)
		}
		requests[r.URL.Path+"?"+r.URL.Query().Encode()]++
		status, body := http.StatusOK, ""
		if r.URL.Host == "audit.invalid" {
			if r.Header.Get("Authorization") != "Bearer dev-token" {
				t.Fatal("audit used credentials outside selected environment")
			}
			switch r.URL.Path {
			case "/v4/object-storage/buckets":
				if r.URL.Query().Get("page") == "1" {
					body = `{"page":1,"pages":2,"data":[{"label":"c-denied","region":"us-sea","s3_endpoint":"https://objects.invalid"},{"label":"b-unknown","region":"us-sea","s3_endpoint":"https://objects.invalid"}]}`
				} else {
					body = `{"page":2,"pages":2,"data":[{"label":"a-owned","region":"us-sea","s3_endpoint":"https://objects.invalid"}]}`
				}
			case "/v4/object-storage/keys":
				body = `{"page":1,"pages":1,"data":[{"id":1,"access_key":"dev-access","bucket_access":[{"bucket_name":"a-owned","region":"us-sea","permissions":"read_write"},{"bucket_name":"c-denied","region":"us-sea","permissions":"read_only"}]},{"id":2,"access_key":"staging-access","bucket_access":[{"bucket_name":"b-unknown","region":"us-sea","permissions":"read_write"}]}]}`
			default:
				t.Fatalf("unexpected API request %s", r.URL)
			}
		} else {
			if r.URL.Host != "objects.invalid" || !strings.Contains(r.Header.Get("Authorization"), "Credential=dev-access/") {
				t.Fatal("audit used an unverified scoped credential")
			}
			switch r.URL.Path {
			case "/c-denied":
				status, body = 403, `<Error><Code>AccessDenied</Code><Message>private-provider-message</Message></Error>`
			case "/a-owned":
				switch {
				case r.URL.Query().Get("list-type") == "2":
					body = `<ListBucketResult><Contents><Key>ci/repo/123/1/linux/private-artifact-name</Key><Size>17</Size><LastModified>2020-01-01T00:00:00Z</LastModified></Contents></ListBucketResult>`
				case r.URL.Query().Has("versions"):
					body = `<ListVersionsResult><Version><Key>private-version-key</Key><VersionId>private-version-id</VersionId></Version></ListVersionsResult>`
				case r.URL.Query().Has("uploads"):
					body = `<ListMultipartUploadsResult/>`
				default:
					body = `<Configuration><PrivatePrincipal>private-principal</PrivatePrincipal></Configuration>`
				}
			default:
				t.Fatalf("unknown bucket was probed with another environment's credentials: %s", r.URL.Path)
			}
		}
		return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}, nil
	})
	output := filepath.Join(t.TempDir(), "audit.json")
	stdout, stderr, err := captureOutput(func() error {
		return runObjectStorageAudit([]string{"--environment", "dev", "--inspect", "--out", output})
	})
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if stdout != "" || stderr != "" {
		t.Fatalf("file report unexpectedly printed output: %q %q", stdout, stderr)
	}
	info, err := os.Stat(output)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("audit report is not private")
	}
	var report storageAuditReport
	if err := json.Unmarshal(body, &report); err != nil {
		t.Fatal(err)
	}
	if report.Environment != "dev" || len(report.Buckets) != 3 || report.Buckets[0].Label != "a-owned" || report.Buckets[1].Label != "b-unknown" {
		t.Fatalf("incomplete or unsorted report: %+v", report)
	}
	owned := report.Buckets[0]
	if owned.Access != "scoped list verified" || owned.Prefixes["ci/"].Bytes != 17 || owned.Prefixes["ci/"].PastDefaultRetentionObjects != 1 || owned.Configuration["uploads"] != "empty" || !strings.HasPrefix(owned.Configuration["versions"], "present;") {
		t.Fatalf("incorrect verified inventory: %+v", owned)
	}
	for _, bucket := range report.Buckets[1:] {
		if !strings.Contains(bucket.Access, "ownership unresolved") || len(bucket.Prefixes) != 0 || len(bucket.Configuration) != 0 {
			t.Fatalf("unverified ownership became empty/verified: %+v", bucket)
		}
	}
	for _, secret := range []string{"dev-token", "dev-access", "dev-secret", "staging-token", "staging-access", "staging-secret", "private-artifact-name", "private-version-key", "private-version-id", "private-principal", "private-provider-message"} {
		if strings.Contains(string(body), secret) {
			t.Fatalf("audit exposed private data %q", secret)
		}
	}
	if requests["/v4/object-storage/buckets?page=2&page_size=500"] != 1 {
		t.Fatal("audit CLI did not read account pagination")
	}
	stdout, _, err = captureOutput(func() error { return runObjectStorageAudit([]string{"--environment", "dev"}) })
	if err != nil || !strings.Contains(stdout, `"not inspected"`) || requests["/v4/object-storage/keys?page=1&page_size=500"] != 1 {
		t.Fatalf("non-inspect audit performed scoped inspection: %v", err)
	}
}

func TestStorageAuditCLIRejectsMissingEnvironmentCredentials(t *testing.T) {
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", t.TempDir())
	t.Setenv("RTK_CLOUD_DEPLOYMENT_CREDENTIAL_ENV_FILE", "")
	t.Setenv("LINODE_TOKEN", "ambient-does-not-authorize-audit")
	store, err := newSecretStore("", "dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := store.write("operator/env/GHCR_PULL_USERNAME", []byte("user"), false); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{nil, {"--environment", "missing"}, {"--environment", "dev"}, {"--unknown"}} {
		if err := runObjectStorageAudit(args); err == nil {
			t.Fatalf("invalid audit accepted: %v", args)
		}
	}
}

func TestStorageAuditRejectsUnknownAndWrongScopeCredentials(t *testing.T) {
	bucket := linodeStorageBucket{Label: "owned", Region: "us-sea", S3Endpoint: "https://objects.invalid"}
	for _, grants := range []string{`[]`, `[{"bucket_name":"other","region":"us-sea"}]`, `[{"bucket_name":"owned","region":"us-lax"}]`} {
		var keys []linodeStorageKey
		if err := json.Unmarshal([]byte(`[{"access_key":"access","bucket_access":`+grants+`}]`), &keys); err != nil {
			t.Fatal(err)
		}
		if _, ok := storageAuditCredentials(map[string]string{"LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "access", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "secret"}, keys, bucket); ok {
			t.Fatalf("unscoped/wrong bucket/wrong region credential accepted: %s", grants)
		}
	}
}

func TestStorageAccountInventoryRejectsUnresolvedPages(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		err        error
	}{
		{name: "wrong page", body: `{"page":2,"pages":2,"data":[]}`, status: 200},
		{name: "invalid JSON", body: `broken`, status: 200},
		{name: "forbidden", body: `{}`, status: 403},
		{name: "transport failure", err: errors.New("private-network-detail")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := defaultDeploymentCredentialChecker()
			c.client = &http.Client{Transport: consoleRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if tc.err != nil {
					return nil, tc.err
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Header: make(http.Header)}, nil
			})}
			var buckets []storageBucketAudit
			if err := c.storageAccountInventory("token", "buckets", &buckets); err == nil {
				t.Fatal("unresolved account inventory accepted")
			}
			if err := c.storageAccountInventory("token", "unknown", &buckets); err == nil {
				t.Fatal("unsupported inventory accepted")
			}
		})
	}
}

func TestStorageAuditConfigurationKeepsFailuresUnresolved(t *testing.T) {
	for _, tc := range []struct {
		name, body, want string
		status           int
		err              error
	}{
		{name: "HTTP failure", status: 403, body: `<Error/>`, want: "HTTP 403"},
		{name: "malformed XML", status: 200, body: `<broken`, want: "unresolved invalid XML"},
		{name: "wrong XML root", status: 200, body: `<Error><Code>AccessDenied</Code></Error>`, want: "unresolved invalid XML"},
		{name: "network failure", err: errors.New("private-secret-network-details"), want: "unresolved request failure"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := defaultDeploymentCredentialChecker()
			c.client = &http.Client{Transport: consoleRoundTripFunc(func(r *http.Request) (*http.Response, error) {
				if r.Method != http.MethodGet {
					t.Fatal("configuration audit mutated storage")
				}
				if tc.err != nil {
					return nil, tc.err
				}
				return &http.Response{StatusCode: tc.status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			result := auditStorageConfiguration(c, provisionObjectStore{bucket: "bucket", region: "us-sea", endpoint: "https://objects.invalid", accessKey: "access", secretKey: "secret"})
			for _, name := range []string{"versions", "uploads"} {
				if !strings.HasPrefix(result[name], tc.want) {
					t.Fatalf("%s incorrectly resolved: %s", name, result[name])
				}
			}
		})
	}
}

func TestStorageAuditRetentionDoesNotExpireProtectedNamespaces(t *testing.T) {
	old := time.Now().UTC().AddDate(0, 0, -60).Format(time.RFC3339)
	entries := []provisionObjectEntry{}
	for _, prefix := range []string{"ci/", "tmp/", "reports/", "holds/", "sdk/", "releases/", "clips/", ""} {
		entries = append(entries, provisionObjectEntry{Key: prefix + "private-object", Size: 1, LastModified: old})
	}
	result := summarizeStoragePrefixes(entries)
	for name, row := range result {
		want := int64(0)
		if name == "ci/" || name == "tmp/" || name == "reports/" {
			want = 1
		}
		if row.PastDefaultRetentionObjects != want {
			t.Fatal(fmt.Sprintf("incorrect retention for %s: %+v", name, row))
		}
	}
}

func TestStorageAuditSummarizesWithoutObjectNames(t *testing.T) {
	result := summarizeStoragePrefixes([]provisionObjectEntry{{Key: "ci/repo/run/secret.zip", Size: 10, LastModified: "2026-01-01"}, {Key: "ci/repo/run2/output.zip", Size: 20, LastModified: "2026-01-02"}, {Key: "sdk/latest.json", Size: 5, LastModified: "2026-01-03"}})
	if result["ci/"].Objects != 2 || result["ci/"].Bytes != 30 || result["ci/"].Oldest != "2026-01-01" {
		t.Fatal(result)
	}
	if len(result) != 2 {
		t.Fatal("object names exposed in prefix summary")
	}
}
