package postgresbackup

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
)

type memoryRepository struct {
	objects       map[string][]byte
	parts         map[int32][]byte
	corrupt       bool
	partFailure   bool
	ambiguous     bool
	aborted       bool
	versioned     bool
	deleted       []string
	deleteFailure string
}

func newMemoryRepository() *memoryRepository { return &memoryRepository{objects: map[string][]byte{}} }
func (m *memoryRepository) CreateMultipartUpload(context.Context, *s3.CreateMultipartUploadInput, ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	m.parts = map[int32][]byte{}
	return &s3.CreateMultipartUploadOutput{UploadId: aws.String("upload-1")}, nil
}
func (m *memoryRepository) UploadPart(_ context.Context, in *s3.UploadPartInput, _ ...func(*s3.Options)) (*s3.UploadPartOutput, error) {
	if m.partFailure {
		return nil, errors.New("failure")
	}
	b, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	if int64(len(b)) != aws.ToInt64(in.ContentLength) {
		return nil, errors.New("part length mismatch")
	}
	m.parts[aws.ToInt32(in.PartNumber)] = b
	return &s3.UploadPartOutput{ETag: aws.String("multipart-not-a-sha256")}, nil
}
func (m *memoryRepository) CompleteMultipartUpload(_ context.Context, in *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	if aws.ToString(in.IfNoneMatch) != "*" {
		return nil, errors.New("missing immutable precondition")
	}
	if _, ok := m.objects[*in.Key]; ok {
		return nil, errors.New("exists")
	}
	var b []byte
	for _, part := range in.MultipartUpload.Parts {
		b = append(b, m.parts[*part.PartNumber]...)
	}
	m.objects[*in.Key] = b
	if m.ambiguous {
		return nil, errors.New("connection lost after commit")
	}
	return &s3.CompleteMultipartUploadOutput{}, nil
}
func (m *memoryRepository) AbortMultipartUpload(context.Context, *s3.AbortMultipartUploadInput, ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	m.aborted = true
	return &s3.AbortMultipartUploadOutput{}, nil
}
func (m *memoryRepository) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if aws.ToString(in.IfNoneMatch) != "*" {
		return nil, errors.New("missing immutable precondition")
	}
	if _, ok := m.objects[*in.Key]; ok {
		return nil, errors.New("exists")
	}
	b, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	m.objects[*in.Key] = b
	return &s3.PutObjectOutput{}, nil
}
func (m *memoryRepository) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	b, ok := m.objects[*in.Key]
	if !ok {
		return nil, errors.New("not found")
	}
	if m.corrupt && strings.HasSuffix(*in.Key, ".age") {
		b = []byte("bad")
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(b))}, nil
}
func (m *memoryRepository) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	b, ok := m.objects[*in.Key]
	if !ok {
		return nil, errors.New("not found")
	}
	return &s3.HeadObjectOutput{ContentLength: aws.Int64(int64(len(b)))}, nil
}
func (m *memoryRepository) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	r := &s3.ListObjectsV2Output{}
	var keys []string
	for key := range m.objects {
		if strings.HasPrefix(key, aws.ToString(in.Prefix)) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		r.Contents = append(r.Contents, types.Object{Key: aws.String(key)})
	}
	return r, nil
}
func (m *memoryRepository) DeleteObject(_ context.Context, in *s3.DeleteObjectInput, _ ...func(*s3.Options)) (*s3.DeleteObjectOutput, error) {
	if *in.Key == m.deleteFailure {
		return nil, errors.New("delete denied")
	}
	m.deleted = append(m.deleted, *in.Key)
	delete(m.objects, *in.Key)
	return &s3.DeleteObjectOutput{}, nil
}
func (m *memoryRepository) GetBucketVersioning(context.Context, *s3.GetBucketVersioningInput, ...func(*s3.Options)) (*s3.GetBucketVersioningOutput, error) {
	r := &s3.GetBucketVersioningOutput{}
	if m.versioned {
		r.Status = types.BucketVersioningStatusEnabled
	}
	return r, nil
}

func remotePrivateTemp(t *testing.T) string {
	t.Helper()
	path, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0700); err != nil {
		t.Fatal(err)
	}
	return path
}

func remoteFixture(t *testing.T) (Config, Manifest, string) {
	t.Helper()
	dir := remotePrivateTemp(t)
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	c := Config{Version: Version, Environment: "staging", Stack: "video-cloud-staging", ClusterID: "main", Directory: dir, Source: Source{Host: "postgresql", Port: 5432, User: "backup", PasswordFile: "/run/secrets/password", SSLMode: "disable", SystemIdentifier: "12345", PostgresImage: "postgres@sha256:" + strings.Repeat("a", 64)}, Recipients: []string{key.Recipient().String()}, Remote: recovery.Remote{Endpoint: "https://backup.example", Region: "region", Bucket: "backup", Prefix: "staging/postgres-physical"}, TimeoutSeconds: 60, MaxArchiveBytes: 8 << 30, MaxPlaintextBytes: 16 << 30, RetentionDays: 14, MinimumBackups: 14}
	now := time.Now().UTC().Add(-time.Minute)
	m := Manifest{Version: Version, Scope: Scope, ID: "backup-1", Environment: c.Environment, Stack: c.Stack, ClusterID: c.ClusterID, PostgresMajor: 16, PostgresImage: c.Source.PostgresImage, SystemIdentifier: c.Source.SystemIdentifier, StartedAt: now.Add(-time.Minute), FinishedAt: now, NativeManifestSHA256: strings.Repeat("b", 64)}
	file := filepath.Join(dir, m.ID+".age")
	if err := os.WriteFile(file, []byte("opaque encrypted backup bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	return c, m, file
}

func TestMultipartBackupReadbackRetryAndDownload(t *testing.T) {
	c, manifest, file := remoteFixture(t)
	store := newMemoryRepository()
	store.ambiguous = true
	ctx := context.Background()
	if err := upload(ctx, store, c, manifest, file, 7); err != nil {
		t.Fatal(err)
	}
	if len(store.parts) < 2 {
		t.Fatal("did not exercise multipart")
	}
	if err := upload(ctx, store, c, manifest, file, 7); err != nil {
		t.Fatal("idempotent retry", err)
	}
	sets, err := ListFixture(ctx, store, c)
	if err != nil || len(sets) != 1 {
		t.Fatalf("list: %v %v", sets, err)
	}
	dir := remotePrivateTemp(t)
	os.Chmod(dir, 0700)
	path, err := download(ctx, store, c, manifest.ID, dir)
	if err != nil {
		t.Fatal(err)
	}
	want, _ := os.ReadFile(file)
	got, _ := os.ReadFile(path)
	if !bytes.Equal(want, got) {
		t.Fatal("download changed ciphertext")
	}
	if _, err := download(ctx, store, c, manifest.ID, dir); err == nil {
		t.Fatal("overwrote local backup")
	}
	if err := os.WriteFile(file, []byte("different bytes"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := upload(ctx, store, c, manifest, file, 7); err == nil {
		t.Fatal("overwrote immutable backup")
	}
}

func ListFixture(ctx context.Context, s objectStore, c Config) ([]Completion, error) {
	keys, err := listKeys(ctx, s, c)
	if err != nil {
		return nil, err
	}
	return listCompleted(ctx, s, c, keys)
}

func TestMultipartFailuresDoNotPublishOrDiscardRetry(t *testing.T) {
	for _, mode := range []string{"part", "corruption", "metadata-conflict"} {
		t.Run(mode, func(t *testing.T) {
			c, m, file := remoteFixture(t)
			s := newMemoryRepository()
			s.partFailure = mode == "part"
			s.corrupt = mode == "corruption"
			if mode == "metadata-conflict" {
				s.objects[remoteKey(c, m.ID, ".complete.json")] = []byte("foreign metadata")
			}
			if err := upload(context.Background(), s, c, m, file, 5); err == nil {
				t.Fatal("failure accepted")
			}
			if mode != "metadata-conflict" {
				if _, ok := s.objects[remoteKey(c, m.ID, ".complete.json")]; ok {
					t.Fatal("published incomplete backup")
				}
			}
			if _, err := os.Stat(file); err != nil {
				t.Fatal("discarded retry archive")
			}
			if mode == "part" && !s.aborted {
				t.Fatal("multipart not aborted")
			}
		})
	}
}

func addBackup(t *testing.T, s *memoryRepository, c Config, m Manifest) {
	t.Helper()
	body := []byte(m.ID)
	h := sha256.Sum256(body)
	set := Completion{Manifest: m, Artifact: recovery.Artifact{Path: m.ID + ".age", Size: int64(len(body)), SHA256: hex.EncodeToString(h[:])}}
	b, err := json.Marshal(set)
	if err != nil {
		t.Fatal(err)
	}
	s.objects[remoteKey(c, m.ID, ".complete.json")] = b
	s.objects[remoteKey(c, m.ID, ".age")] = body
}

func retentionFixture(t *testing.T) (Config, *memoryRepository, time.Time) {
	t.Helper()
	c, m, _ := remoteFixture(t)
	s := newMemoryRepository()
	now := time.Now().UTC()
	for i := 0; i < 20; i++ {
		m.ID = fmt.Sprintf("backup-%02d", i)
		m.FinishedAt = now.Add(-time.Duration(i) * 24 * time.Hour)
		m.StartedAt = m.FinishedAt.Add(-time.Minute)
		addBackup(t, s, c, m)
	}
	d := Drill{Version: 1, Environment: c.Environment, Stack: c.Stack, ClusterID: c.ClusterID, BackupID: "backup-19", SystemIdentifier: c.Source.SystemIdentifier, PostgresImage: c.Source.PostgresImage, FinishedAt: now, Success: true}
	b, _ := json.Marshal(d)
	s.objects[remotePrefix(c)+"drills/verified.json"] = b
	s.objects[remoteKey(c, "backup-18", ".hold.json")] = []byte("hold")
	return c, s, now
}

func TestRetentionProtectsWindowMinimumHoldAndRestoreEvidence(t *testing.T) {
	c, s, now := retentionFixture(t)
	ctx := context.Background()
	p, err := prune(ctx, s, c, true, now)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(p.Candidates, ",") != "backup-15,backup-16,backup-17" {
		t.Fatalf("unsafe candidates %+v", p)
	}
	if len(s.deleted) != 0 {
		t.Fatal("dry run mutated repository")
	}
	p, err = prune(ctx, s, c, false, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Deleted) != 3 {
		t.Fatalf("unexpected deletion %+v", p)
	}
	for i := 0; i < len(s.deleted); i += 2 {
		if !strings.HasSuffix(s.deleted[i], ".complete.json") || !strings.HasSuffix(s.deleted[i+1], ".age") {
			t.Fatal("unsafe delete order")
		}
	}
	if _, ok := s.objects[remoteKey(c, "backup-19", ".age")]; !ok {
		t.Fatal("deleted last verified backup")
	}
}

func TestRetentionRefusesUnverifiedOrVersionedRepository(t *testing.T) {
	c, s, now := retentionFixture(t)
	delete(s.objects, remotePrefix(c)+"drills/verified.json")
	p, err := prune(context.Background(), s, c, false, now)
	if err != nil || len(p.Candidates) != 0 || p.Reason == "" {
		t.Fatalf("unverified prune %+v %v", p, err)
	}
	c, s, now = retentionFixture(t)
	s.versioned = true
	if _, err := prune(context.Background(), s, c, false, now); err == nil || len(s.deleted) != 0 {
		t.Fatal("versioned bucket pruned")
	}
}

func TestRetentionRefusesMissingOrChangedCommittedArchive(t *testing.T) {
	for _, kind := range []string{"missing", "truncated"} {
		t.Run(kind, func(t *testing.T) {
			c, s, now := retentionFixture(t)
			key := remoteKey(c, "backup-00", ".age")
			if kind == "missing" {
				delete(s.objects, key)
			} else {
				s.objects[key] = []byte("bad")
			}
			if _, err := ListFixture(context.Background(), s, c); err == nil {
				t.Fatal("counted incomplete recovery point")
			}
			if _, err := prune(context.Background(), s, c, false, now); err == nil || len(s.deleted) != 0 {
				t.Fatal("deleted intact backups after a committed archive disappeared")
			}
		})
	}
}

func TestRetentionInterruptedDeleteNeverAdvertisesMissingData(t *testing.T) {
	c, s, now := retentionFixture(t)
	s.deleteFailure = remoteKey(c, "backup-15", ".age")
	if _, err := prune(context.Background(), s, c, false, now); err == nil {
		t.Fatal("ignored interrupted delete")
	}
	if _, ok := s.objects[remoteKey(c, "backup-15", ".complete.json")]; ok {
		t.Fatal("advertised partially deleted set")
	}
	if _, ok := s.objects[remoteKey(c, "backup-15", ".age")]; !ok {
		t.Fatal("expected orphan retained")
	}
}

func TestRepositoryRejectsForeignCompletionAndCorruptDownload(t *testing.T) {
	c, m, _ := remoteFixture(t)
	s := newMemoryRepository()
	addBackup(t, s, c, m)
	s.corrupt = true
	dir := remotePrivateTemp(t)
	os.Chmod(dir, 0700)
	if _, err := download(context.Background(), s, c, m.ID, dir); err == nil {
		t.Fatal("corrupt download accepted")
	}
	if _, err := os.Stat(filepath.Join(dir, m.ID+".age")); !os.IsNotExist(err) {
		t.Fatal("partial download retained")
	}
	m.Environment = "prod"
	addBackup(t, s, c, m)
	if _, err := ListFixture(context.Background(), s, c); err == nil {
		t.Fatal("cross-environment completion accepted")
	}
}

func TestRepositoryLifecycleAndIndependentDrillStatus(t *testing.T) {
	c, m, file := remoteFixture(t)
	s := newMemoryRepository()
	e := Engine{remote: s}
	ctx := context.Background()
	if err := e.Upload(ctx, c, m, file); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Download(ctx, c, m.ID, remotePrivateTemp(t)); err != nil {
		t.Fatal(err)
	}
	status, err := e.GetStatus(ctx, c)
	if err != nil || status.CompletedCount != 1 || status.Latest == nil || status.LatestDrill != nil {
		t.Fatalf("capture alone must not imply drill success: %+v %v", status, err)
	}
	d := Drill{Version: Version, Environment: c.Environment, Stack: c.Stack, ClusterID: c.ClusterID, BackupID: m.ID, SystemIdentifier: m.SystemIdentifier, PostgresImage: m.PostgresImage, FinishedAt: time.Now().UTC(), Success: true}
	if err := e.RecordDrill(ctx, c, d); err != nil {
		t.Fatal(err)
	}
	// A later failed drill is visible independently of the successful capture.
	d.FinishedAt = d.FinishedAt.Add(time.Second)
	d.Success = false
	if err := e.RecordDrill(ctx, c, d); err != nil {
		t.Fatal(err)
	}
	status, err = e.GetStatus(ctx, c)
	if err != nil || status.LatestDrill == nil || status.LatestDrill.Success || status.Latest.Manifest.FinishedAt != m.FinishedAt {
		t.Fatalf("independent status: %+v %v", status, err)
	}
	sets, err := e.List(ctx, c)
	if err != nil || len(sets) != 1 {
		t.Fatalf("list: %+v %v", sets, err)
	}
	if p, err := e.Prune(ctx, c, false); err != nil || len(p.Kept) != 1 || len(p.Deleted) != 0 {
		t.Fatalf("fresh backup was not protected: %+v %v", p, err)
	}
	d.PostgresImage = "postgres@sha256:" + strings.Repeat("c", 64)
	if err := e.RecordDrill(ctx, c, d); err == nil {
		t.Fatal("recorded drill from a different image")
	}
	d.PostgresImage = m.PostgresImage
	d.FinishedAt = m.StartedAt
	if err := e.RecordDrill(ctx, c, d); err == nil {
		t.Fatal("recorded drill before capture")
	}
	d.Environment = "prod"
	if err := e.RecordDrill(ctx, c, d); err == nil {
		t.Fatal("recorded foreign drill")
	}
	delete(s.objects, remoteKey(c, m.ID, ".age"))
	if _, err := e.GetStatus(ctx, c); err == nil {
		t.Fatal("status advertised missing archive")
	}
}

func TestRepositoryRequiresExplicitCredentialsAndValidConfiguration(t *testing.T) {
	c, m, file := remoteFixture(t)
	t.Setenv("RTK_BACKUP_ACCESS_KEY_ID", "")
	t.Setenv("RTK_BACKUP_SECRET_ACCESS_KEY", "")
	ctx := context.Background()
	for _, badConfig := range []bool{false, true} {
		if badConfig {
			c.Environment = "invalid/environment"
		}
		if err := Upload(ctx, c, m, file); err == nil {
			t.Fatal("upload accepted missing credentials or invalid config")
		}
		if _, err := Download(ctx, c, m.ID, c.Directory); err == nil {
			t.Fatal("download accepted missing credentials or invalid config")
		}
		if _, err := List(ctx, c); err == nil {
			t.Fatal("list accepted missing credentials or invalid config")
		}
		if _, err := GetStatus(ctx, c); err == nil {
			t.Fatal("status accepted missing credentials or invalid config")
		}
		if _, err := Prune(ctx, c, false); err == nil {
			t.Fatal("prune accepted missing credentials or invalid config")
		}
		if err := RecordDrill(ctx, c, Drill{}); err == nil {
			t.Fatal("drill accepted missing credentials or invalid config")
		}
	}
}
