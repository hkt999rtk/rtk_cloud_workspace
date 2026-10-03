package postgresbackup

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// This opt-in test sends >5 GiB through the actual SDK and HTTPS transport. The
// server spools bounded parts to disk, never retaining a whole archive in RAM.
// It proves client size/transport behavior, not a provider's S3 compatibility.
func TestMultipartLargeArchiveIntegration(t *testing.T) {
	if os.Getenv("RTK_POSTGRES_BACKUP_LARGE_INTEGRATION") != "1" {
		t.Skip("set RTK_POSTGRES_BACKUP_LARGE_INTEGRATION=1; needs 7 GiB free temporary disk and loopback HTTPS")
	}
	c, m, file := remoteFixture(t)
	const size = int64(6<<30) + 17
	f, err := os.OpenFile(file, os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if err = f.Truncate(size); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()
	partDir := remotePrivateTemp(t)
	var mu sync.Mutex
	partPaths := map[int]string{}
	markers := map[string][]byte{}
	committed := false
	total := int64(0)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		w.Header().Set("Content-Type", "application/xml")
		switch {
		case r.Method == http.MethodPost && q.Has("uploads"):
			io.WriteString(w, "<InitiateMultipartUploadResult><UploadId>large-upload</UploadId></InitiateMultipartUploadResult>")
		case r.Method == http.MethodPut && q.Get("partNumber") != "":
			n, err := strconv.Atoi(q.Get("partNumber"))
			if err != nil || n < 1 {
				http.Error(w, "part", 400)
				return
			}
			path := filepath.Join(partDir, fmt.Sprintf("part-%05d", n))
			out, err := os.Create(path)
			if err != nil {
				http.Error(w, "disk", 500)
				return
			}
			written, err := io.Copy(out, r.Body)
			out.Close()
			if err != nil {
				http.Error(w, "copy", 500)
				return
			}
			mu.Lock()
			partPaths[n] = path
			total += written
			mu.Unlock()
			w.Header().Set("ETag", fmt.Sprintf("\"part-%d\"", n))
		case r.Method == http.MethodPost && q.Get("uploadId") != "":
			io.Copy(io.Discard, r.Body)
			mu.Lock()
			defer mu.Unlock()
			if committed || r.Header.Get("If-None-Match") != "*" {
				http.Error(w, "precondition", 412)
				return
			}
			committed = true
			io.WriteString(w, "<CompleteMultipartUploadResult><ETag>multipart-etag</ETag></CompleteMultipartUploadResult>")
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, ".age"):
			mu.Lock()
			ok := committed
			count := len(partPaths)
			mu.Unlock()
			if !ok {
				http.NotFound(w, r)
				return
			}
			w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
			w.Header().Set("Content-Type", "application/octet-stream")
			for n := 1; n <= count; n++ {
				part, err := os.Open(partPaths[n])
				if err != nil {
					return
				}
				_, err = io.Copy(w, part)
				part.Close()
				if err != nil {
					return
				}
			}
		case r.Method == http.MethodPut && strings.HasSuffix(r.URL.Path, ".complete.json"):
			if r.Header.Get("If-None-Match") != "*" {
				http.Error(w, "precondition", 412)
				return
			}
			b, err := io.ReadAll(io.LimitReader(r.Body, 65536))
			if err != nil {
				http.Error(w, "metadata", 400)
				return
			}
			mu.Lock()
			markers[r.URL.Path] = b
			mu.Unlock()
			w.Header().Set("ETag", "\"marker\"")
		case r.Method == http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			http.Error(w, "unexpected S3 operation", 400)
		}
	}))
	defer server.Close()
	c.Remote.Endpoint = server.URL
	client := s3.New(s3.Options{Region: c.Remote.Region, BaseEndpoint: aws.String(server.URL), UsePathStyle: true, HTTPClient: server.Client(), Credentials: credentials.NewStaticCredentialsProvider("fixture", "fixture", ""), RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired})
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := upload(ctx, client, c, m, file, 64<<20); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if total != size || len(partPaths) != 97 || len(markers) != 1 {
		t.Fatalf("unexpected large upload: bytes=%d parts=%d markers=%d", total, len(partPaths), len(markers))
	}
	t.Logf("verified %d encrypted-transport bytes via %d multipart uploads and a full remote readback", total, len(partPaths))
}
