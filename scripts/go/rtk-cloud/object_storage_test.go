package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProvisionObjectStoreConfigurationAndHelpers(t *testing.T) {
	for _, name := range []string{
		"LINODE_OBJ_BUCKET", "LINODE_OBJ_ENDPOINT", "LINODE_OBJ_ACCESS_KEY_ID",
		"LINODE_OBJ_SECRET_ACCESS_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY",
		"LINODE_OBJ_REGION",
	} {
		t.Setenv(name, "")
	}
	if _, err := provisionObjectStoreFromEnv(nil); err == nil || !strings.Contains(err.Error(), "BUCKET") {
		t.Fatalf("missing bucket error = %v", err)
	}
	if _, err := provisionObjectStoreFromEnv(map[string]string{"LINODE_OBJ_BUCKET": "bucket"}); err == nil || !strings.Contains(err.Error(), "ENDPOINT") {
		t.Fatalf("missing endpoint error = %v", err)
	}
	if _, err := provisionObjectStoreFromEnv(map[string]string{
		"LINODE_OBJ_BUCKET": "bucket", "LINODE_OBJ_ENDPOINT": "https://us-sea-1.linodeobjects.com",
	}); err == nil || !strings.Contains(err.Error(), "ACCESS_KEY") {
		t.Fatalf("missing credentials error = %v", err)
	}
	store, err := provisionObjectStoreFromEnv(map[string]string{
		"LINODE_OBJ_BUCKET": "bucket", "LINODE_OBJ_ENDPOINT": "https://us-sea-1.linodeobjects.com/",
		"AWS_ACCESS_KEY_ID": "access", "AWS_SECRET_ACCESS_KEY": "secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if store.region != "us-sea-1" || strings.HasSuffix(store.endpoint, "/") {
		t.Fatalf("store = %#v", store)
	}
	if got := provisionObjectRegionFromEndpoint("https://objects.example.test"); got != "us-east-1" {
		t.Fatalf("fallback region = %q", got)
	}
	t.Setenv("LINODE_OBJ_BUCKET", "process-bucket")
	if overridden, err := provisionObjectStoreFromEnv(map[string]string{
		"LINODE_OBJ_BUCKET": "file-bucket", "LINODE_OBJ_ENDPOINT": "file:///tmp/object-store",
	}); err != nil || overridden.bucket != "process-bucket" {
		t.Fatalf("process environment did not override file values: store=%#v error=%v", overridden, err)
	}
	t.Setenv("LINODE_OBJ_BUCKET", "")
	if got := provisionEscapeObjectPath("folder/a b.txt"); got != "folder/a%20b.txt" {
		t.Fatalf("escaped path = %q", got)
	}
	query := url.Values{"z": {"2", "1"}, "a": {"a b"}}
	if got := provisionCanonicalQuery(query); got != "a=a+b&z=1&z=2" {
		t.Fatalf("canonical query = %q", got)
	}
	if provisionCanonicalQuery(nil) != "" || provisionHexSHA256(nil) == "" ||
		len(provisionHMACSHA256([]byte("key"), []byte("data"))) == 0 ||
		len(provisionSigningKey("secret", "20260724", "us-sea-1")) == 0 {
		t.Fatal("signing helpers returned empty output")
	}
}

func TestProvisionFileObjectStoreLifecycle(t *testing.T) {
	root := t.TempDir()
	store, err := provisionObjectStoreFromEnv(map[string]string{
		"LINODE_OBJ_BUCKET": "bucket",
		"LINODE_OBJ_ENDPOINT": (&url.URL{
			Scheme: "file",
			Path:   root,
		}).String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	objectPath := filepath.Join(root, "bucket", "runs", "run-1", "report.json")
	if err := os.MkdirAll(filepath.Dir(objectPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(objectPath, []byte(`{"status":"PASS"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	entries, err := provisionListObjects(store, "runs")
	if err != nil || len(entries) != 1 || entries[0].Key != "runs/run-1/report.json" {
		t.Fatalf("entries = %#v, error = %v", entries, err)
	}
	if err := provisionObjectExists(store, entries[0].Key); err != nil {
		t.Fatal(err)
	}
	body, err := provisionReadObject(store, entries[0].Key)
	if err != nil || !strings.Contains(string(body), "PASS") {
		t.Fatalf("body = %q, error = %v", body, err)
	}
	out := filepath.Join(t.TempDir(), "nested", "report.json")
	if err := provisionWriteObjectToFile(store, entries[0].Key, out); err != nil {
		t.Fatal(err)
	}
	if copied, err := os.ReadFile(out); err != nil || string(copied) != string(body) {
		t.Fatalf("copied = %q, error = %v", copied, err)
	}
	if err := provisionObjectExists(store, "missing"); err == nil {
		t.Fatal("missing object unexpectedly exists")
	}
}

func TestProvisionSignedObjectRequestsPaginateAndRejectFailures(t *testing.T) {
	page := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if !strings.HasPrefix(req.Header.Get("Authorization"), "AWS4-HMAC-SHA256 Credential=access/") {
			t.Fatalf("authorization = %q", req.Header.Get("Authorization"))
		}
		if req.URL.Path == "/bucket/denied" {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		if req.Method == http.MethodHead {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		if req.URL.Query().Get("list-type") == "2" {
			page++
			if page == 1 {
				_, _ = w.Write([]byte(`<ListBucketResult><Contents><Key>prefix/one</Key><Size>1</Size></Contents><IsTruncated>true</IsTruncated><NextContinuationToken>next</NextContinuationToken></ListBucketResult>`))
				return
			}
			if req.URL.Query().Get("continuation-token") != "next" {
				t.Fatalf("continuation token = %q", req.URL.Query().Get("continuation-token"))
			}
			_, _ = w.Write([]byte(`<ListBucketResult><Contents><Key>prefix/two</Key><Size>2</Size></Contents></ListBucketResult>`))
			return
		}
		_, _ = w.Write([]byte("object"))
	}))
	defer server.Close()

	store := provisionObjectStore{
		bucket: "bucket", endpoint: server.URL, accessKey: "access", secretKey: "secret", region: "us-test-1",
	}
	entries, err := provisionListObjects(store, "prefix/")
	if err != nil || len(entries) != 2 {
		t.Fatalf("entries = %#v, error = %v", entries, err)
	}
	if body, err := provisionReadObject(store, "prefix/object"); err != nil || string(body) != "object" {
		t.Fatalf("read body = %q, error = %v", body, err)
	}
	if err := provisionObjectExists(store, "prefix/object"); err != nil {
		t.Fatal(err)
	}
	if _, err := provisionReadObject(store, "denied"); err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("denied error = %v", err)
	}
}
