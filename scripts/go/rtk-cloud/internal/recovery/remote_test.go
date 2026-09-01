package recovery

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type memoryObjects struct {
	objects   map[string][]byte
	corrupt   bool
	ambiguous bool
}

func (m *memoryObjects) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if in.IfNoneMatch == nil || *in.IfNoneMatch != "*" {
		return nil, errors.New("immutable precondition required")
	}
	if _, exists := m.objects[*in.Key]; exists {
		return nil, errors.New("already exists")
	}
	b, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	m.objects[*in.Key] = b
	if m.ambiguous {
		return nil, errors.New("ambiguous transport error")
	}
	return &s3.PutObjectOutput{}, nil
}
func (m *memoryObjects) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	b, exists := m.objects[*in.Key]
	if !exists {
		return nil, errors.New("not found")
	}
	if m.corrupt && strings.HasSuffix(*in.Key, ".age") {
		b = []byte("corrupted")
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b))}, nil
}

func TestRemoteCompletionReadbackRetryAndFetch(t *testing.T) {
	c := fixture(t)
	ctx := context.Background()
	m := &memoryObjects{objects: map[string][]byte{}, ambiguous: true}
	file := filepath.Join(c.Directory, "backup-1.age")
	writeTest(t, file, []byte("opaque encrypted bytes"))
	if err := upload(ctx, m, c, "backup-1", file); err != nil {
		t.Fatal(err)
	}
	if _, ok := m.objects[remoteKey(c, "backup-1", ".complete.json")]; !ok {
		t.Fatal("missing completion")
	}
	if err := upload(ctx, m, c, "backup-1", file); err != nil {
		t.Fatal("idempotent retry failed:", err)
	}
	dest := privateTemp(t)
	path, err := download(ctx, m, c, "backup-1", dest)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "opaque encrypted bytes" {
		t.Fatal("fetch mismatch")
	}
	if _, err = download(ctx, m, c, "backup-1", dest); err == nil {
		t.Fatal("fetch overwrote local archive")
	}
	m.corrupt = true
	if _, err = download(ctx, m, c, "backup-1", privateTemp(t)); err == nil {
		t.Fatal("corrupt fetch accepted")
	}
}

func TestRemoteMismatchNeverPublishesCompletion(t *testing.T) {
	c := fixture(t)
	m := &memoryObjects{objects: map[string][]byte{}, corrupt: true}
	file := filepath.Join(c.Directory, "backup-2.age")
	writeTest(t, file, []byte("ciphertext"))
	if upload(context.Background(), m, c, "backup-2", file) == nil {
		t.Fatal("readback mismatch accepted")
	}
	if _, ok := m.objects[remoteKey(c, "backup-2", ".complete.json")]; ok {
		t.Fatal("incomplete backup published")
	}
	if _, err := os.Stat(file); err != nil {
		t.Fatal("local encrypted archive removed")
	}
	if _, err := download(context.Background(), m, c, "backup-2", privateTemp(t)); err == nil {
		t.Fatal("incomplete remote backup fetched")
	}
}
