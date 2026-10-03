package billingbackup

import (
	"bytes"
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/hkt999rtk/rtk_cloud_logger/billingarchive"
)

func testS3(t *testing.T, handler http.HandlerFunc) *S3Store {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	store, err := NewS3Store(Remote{Endpoint: server.URL, Region: "us-sea", SigningRegion: "us-east-1", Bucket: "test-bucket"}, "local-access", "local-secret")
	if err != nil {
		t.Fatal(err)
	}
	// Trust only this test's TLS certificate; production constructor remains
	// pinned HTTPS with the standard trust roots and no redirect following.
	client := store.Client.Options().HTTPClient.(*http.Client)
	client.Transport = server.Client().Transport
	return store
}

func TestVerifierS3CreateOnlyExactReadbackAndDedicatedSigningScope(t *testing.T) {
	var mu sync.Mutex
	objects := map[string][]byte{}
	store := testS3(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.Header.Get("Authorization"), "Credential=local-access/") || !strings.Contains(r.Header.Get("Authorization"), "/us-east-1/s3/aws4_request") {
			t.Error("dedicated signer credential or signing region missing")
		}
		mu.Lock()
		defer mu.Unlock()
		switch r.Method {
		case http.MethodPut:
			if r.Header.Get("If-None-Match") != "*" {
				t.Error("publication not create-only")
			}
			body, _ := io.ReadAll(r.Body)
			if _, exists := objects[r.URL.Path]; exists {
				w.WriteHeader(http.StatusPreconditionFailed)
				fmt.Fprint(w, "<Error><Code>PreconditionFailed</Code></Error>")
				return
			}
			objects[r.URL.Path] = body
			// Model a committed write with a rejected/lost acknowledgment: only
			// exact authenticated readback can reconcile the ambiguous outcome.
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, "<Error><Code>AccessDenied</Code></Error>")
		case http.MethodGet:
			body, exists := objects[r.URL.Path]
			if !exists {
				w.WriteHeader(http.StatusNotFound)
				fmt.Fprint(w, "<Error><Code>NoSuchKey</Code></Error>")
				return
			}
			w.Write(body)
		default:
			t.Error("unexpected storage mutation", r.Method)
			w.WriteHeader(405)
		}
	})
	ctx := context.Background()
	body := []byte(`{"verified":true}`)
	if err := store.PutImmutable(ctx, "set/complete.json", body); err != nil {
		t.Fatal("ambiguous committed write not reconciled", err)
	}
	if err := store.PutImmutable(ctx, "set/complete.json", body); err != nil {
		t.Fatal("identical sealed retry rejected", err)
	}
	if err := store.PutImmutable(ctx, "set/complete.json", []byte(`{"verified":false}`)); err == nil {
		t.Fatal("conflicting sealed retry overwritten")
	}
	if err := store.PutImmutable(ctx, "huge", make([]byte, billingarchive.MaxManifestBytes+32769)); err == nil {
		t.Fatal("publication metadata bound ignored")
	}
	if _, err := store.Read(ctx, "missing"); err != ErrObjectMissing {
		t.Fatal("authoritative missing object not distinguished", err)
	}
	r, err := store.Read(ctx, "set/complete.json")
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(r)
	r.Close()
	if !bytes.Equal(got, body) {
		t.Fatal("sealed bytes changed")
	}
}

func TestVerifierS3RejectsUnconfirmedPublicationAndReadFailures(t *testing.T) {
	for _, status := range []int{http.StatusForbidden, http.StatusNotFound} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			store := testS3(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				if status == 404 {
					fmt.Fprint(w, "<Error><Code>NoSuchKey</Code></Error>")
				} else {
					fmt.Fprint(w, "<Error><Code>AccessDenied</Code><Message>untrusted details</Message></Error>")
				}
			})
			if err := store.PutImmutable(context.Background(), "complete.json", []byte(`{}`)); err == nil || strings.Contains(err.Error(), "untrusted") {
				t.Fatal("unconfirmed publication or storage error disclosure", err)
			}
			if _, err := store.Read(context.Background(), "complete.json"); err == nil || strings.Contains(err.Error(), "untrusted") {
				t.Fatal("read failure or storage error disclosure", err)
			}
		})
	}
	for _, remote := range []Remote{{Endpoint: "http://localhost", Region: "us-sea", SigningRegion: "us-east-1", Bucket: "test"}, {Endpoint: "https://user:password@example.test", Region: "us-sea", SigningRegion: "us-east-1", Bucket: "test"}, {Endpoint: "https://example.test/path", Region: "us-sea", SigningRegion: "us-east-1", Bucket: "test"}} {
		if _, err := NewS3Store(remote, "access", "secret"); err == nil {
			t.Fatal("unqualified endpoint accepted")
		}
	}
	if _, err := NewS3Store(Remote{}, "", ""); err == nil {
		t.Fatal("ambient credentials accepted")
	}
}

func TestVerifierS3CatalogBoundedPaginationAndMalformedContinuations(t *testing.T) {
	for _, mode := range []string{"pages", "missing-token", "repeated-token", "bound", "denied"} {
		t.Run(mode, func(t *testing.T) {
			pages := 0
			store := testS3(t, func(w http.ResponseWriter, r *http.Request) {
				pages++
				if r.URL.Query().Get("list-type") != "2" || r.URL.Query().Get("prefix") != "billing-inbox-snapshots/" {
					t.Error("catalog query scope lost")
				}
				if mode == "denied" {
					w.WriteHeader(403)
					fmt.Fprint(w, "<Error><Code>AccessDenied</Code></Error>")
					return
				}
				w.Header().Set("Content-Type", "application/xml")
				fmt.Fprint(w, `<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
				switch mode {
				case "pages":
					if pages == 1 {
						fmt.Fprint(w, "<IsTruncated>true</IsTruncated><NextContinuationToken>next</NextContinuationToken><Contents><Key>one/manifest.json</Key></Contents><Contents><Key>one/snapshot.age</Key></Contents>")
					} else {
						if r.URL.Query().Get("continuation-token") != "next" {
							t.Error("continuation not preserved")
						}
						fmt.Fprint(w, "<IsTruncated>false</IsTruncated><Contents><Key>two/manifest.json</Key></Contents>")
					}
				case "missing-token":
					fmt.Fprint(w, "<IsTruncated>true</IsTruncated>")
				case "repeated-token":
					fmt.Fprint(w, "<IsTruncated>true</IsTruncated><NextContinuationToken>repeat</NextContinuationToken>")
				case "bound":
					fmt.Fprint(w, "<IsTruncated>false</IsTruncated>")
					for i := 0; i < 10001; i++ {
						fmt.Fprintf(w, "<Contents><Key>set-%d/manifest.json</Key></Contents>", i)
					}
				}
				fmt.Fprint(w, "</ListBucketResult>")
			})
			listed, err := store.ListManifests(context.Background(), "billing-inbox-snapshots/")
			if mode == "pages" {
				if err != nil || strings.Join(listed, ",") != "one,two" || pages != 2 {
					t.Fatal(listed, err, pages)
				}
			} else if err == nil {
				t.Fatal("catalog safety guard ignored", mode)
			}
		})
	}
}

func TestNativeVerifierSignerMustMatchPrivateCustodyAndApprovedRegistry(t *testing.T) {
	e, _, _ := fixture(t)
	root := privateTestDir(t)
	directory := filepath.Join(root, "operator", "recovery", "billing-backup")
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(directory, "verifier.ed25519key")
	if err := os.WriteFile(file, []byte(base64.StdEncoding.EncodeToString(e.Signer)), 0600); err != nil {
		t.Fatal(err)
	}
	keys, _ := e.Config.PublicKeys()
	if got, err := ReadSigner(root, file, keys[e.Config.VerifierKeyID]); err != nil || !bytes.Equal(got, e.Signer) {
		t.Fatal("qualified native key rejected", err)
	}
	for _, body := range []string{"not base64", base64.StdEncoding.EncodeToString([]byte("too short"))} {
		if err := os.WriteFile(file, []byte(body), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := ReadSigner(root, file, keys[e.Config.VerifierKeyID]); err == nil {
			t.Fatal("malformed native key accepted")
		}
	}
	if err := os.WriteFile(file, []byte(base64.StdEncoding.EncodeToString(e.Signer)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSigner(root, file, make([]byte, 32)); err == nil {
		t.Fatal("unapproved private signer accepted")
	}
	wrong := filepath.Join(directory, "verifier.agekey")
	if err := os.WriteFile(wrong, []byte("not signer"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadSigner(root, wrong, keys[e.Config.VerifierKeyID]); err == nil {
		t.Fatal("wrong-role key file accepted")
	}
}
