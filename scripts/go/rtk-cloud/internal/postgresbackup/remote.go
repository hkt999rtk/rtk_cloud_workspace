package postgresbackup

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
)

type Completion struct {
	Manifest Manifest          `json:"manifest"`
	Artifact recovery.Artifact `json:"encrypted_artifact"`
}

type Attempt struct {
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	Status     string    `json:"status"`
}

type Drill struct {
	Version          int       `json:"version"`
	Environment      string    `json:"environment"`
	Stack            string    `json:"stack"`
	ClusterID        string    `json:"cluster_id"`
	BackupID         string    `json:"backup_id"`
	SystemIdentifier string    `json:"system_identifier"`
	PostgresImage    string    `json:"postgres_image"`
	FinishedAt       time.Time `json:"finished_at"`
	Success          bool      `json:"success"`
}

type Status struct {
	Version        int         `json:"version"`
	Environment    string      `json:"environment"`
	Stack          string      `json:"stack"`
	ClusterID      string      `json:"cluster_id"`
	GeneratedAt    time.Time   `json:"generated_at"`
	Latest         *Completion `json:"latest,omitempty"`
	LatestDrill    *Drill      `json:"latest_drill,omitempty"`
	CompletedCount int         `json:"completed_count"`
	LastAttempt    *Attempt    `json:"last_attempt,omitempty"`
}

// The native S3 SDK already supplies the multipart operations; bounded file
// SectionReaders avoid retaining complete parts or backups in memory.
type objectStore interface {
	CreateMultipartUpload(context.Context, *s3.CreateMultipartUploadInput, ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error)
	UploadPart(context.Context, *s3.UploadPartInput, ...func(*s3.Options)) (*s3.UploadPartOutput, error)
	CompleteMultipartUpload(context.Context, *s3.CompleteMultipartUploadInput, ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error)
	AbortMultipartUpload(context.Context, *s3.AbortMultipartUploadInput, ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObject(context.Context, *s3.DeleteObjectInput, ...func(*s3.Options)) (*s3.DeleteObjectOutput, error)
	GetBucketVersioning(context.Context, *s3.GetBucketVersioningInput, ...func(*s3.Options)) (*s3.GetBucketVersioningOutput, error)
}

func (e Engine) repository(c Config) (objectStore, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if e.remote != nil {
		return e.remote, nil
	}
	return recovery.RemoteClient(c.Remote)
}

func remotePrefix(c Config) string {
	return strings.TrimSuffix(c.Remote.Prefix, "/") + "/" + c.Stack + "/" + c.ClusterID + "/"
}
func remoteKey(c Config, id, suffix string) string { return remotePrefix(c) + id + suffix }

func matchesRepository(c Config, m Manifest) error {
	if err := m.Validate(); err != nil {
		return err
	}
	if m.Environment != c.Environment || m.Stack != c.Stack || m.ClusterID != c.ClusterID || m.SystemIdentifier != c.Source.SystemIdentifier {
		return errors.New("backup repository identity mismatch")
	}
	return nil
}

func validateCompletion(c Config, m Completion, id string) error {
	if err := matchesRepository(c, m.Manifest); err != nil {
		return err
	}
	if m.Manifest.ID != id || m.Artifact.Path != id+".age" || m.Artifact.Size < 1 || m.Artifact.Size > c.MaxArchiveBytes || !sha256Hex.MatchString(m.Artifact.SHA256) {
		return errors.New("invalid backup completion metadata")
	}
	return nil
}

func Upload(ctx context.Context, c Config, m Manifest, file string) error {
	return (Engine{}).Upload(ctx, c, m, file)
}

func (e Engine) Upload(ctx context.Context, c Config, m Manifest, file string) error {
	client, err := e.repository(c)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	return upload(ctx, client, c, m, file, 64<<20)
}

func upload(ctx context.Context, client objectStore, c Config, m Manifest, file string, partSize int64) error {
	if err := c.MatchManifest(m); err != nil {
		return err
	}
	if file != filepath.Join(c.Directory, m.ID+".age") {
		return errors.New("upload requires named archive in private backup directory")
	}
	if err := recovery.PrivateDirectory(c.Directory); err != nil {
		return err
	}
	i, err := os.Lstat(file)
	if err != nil || !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 {
		return errors.New("encrypted backup must be a private regular file")
	}
	a, err := recovery.DigestFile(file)
	if err != nil {
		return errors.New("encrypted backup unavailable")
	}
	a.Path = m.ID + ".age"
	if a.Size < 1 || a.Size > c.MaxArchiveBytes || partSize < 1 {
		return errors.New("encrypted backup exceeds configured limit")
	}
	// S3 allows no more than 10,000 parts, each at most 5 GiB.
	if required := (a.Size + 9999) / 10000; required > partSize {
		partSize = required
	}
	if partSize > 5<<30 {
		return errors.New("backup exceeds multipart capacity")
	}
	marker := Completion{Manifest: m, Artifact: a}
	key := remoteKey(c, m.ID, ".age")
	f, err := os.Open(file)
	if err != nil {
		return errors.New("encrypted backup unavailable")
	}
	defer f.Close()
	created, err := client.CreateMultipartUpload(ctx, &s3.CreateMultipartUploadInput{Bucket: aws.String(c.Remote.Bucket), Key: aws.String(key), ContentType: aws.String("application/octet-stream")})
	if err != nil || created == nil || aws.ToString(created.UploadId) == "" {
		return errors.New("cannot begin backup multipart upload")
	}
	finished := false
	defer func() {
		if !finished {
			abort, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
			defer cancel()
			_, _ = client.AbortMultipartUpload(abort, &s3.AbortMultipartUploadInput{Bucket: aws.String(c.Remote.Bucket), Key: aws.String(key), UploadId: created.UploadId})
		}
	}()
	parts := []types.CompletedPart{}
	for offset := int64(0); offset < a.Size; offset += partSize {
		n := min(partSize, a.Size-offset)
		number := int32(len(parts) + 1)
		part, err := client.UploadPart(ctx, &s3.UploadPartInput{Bucket: aws.String(c.Remote.Bucket), Key: aws.String(key), UploadId: created.UploadId, PartNumber: aws.Int32(number), Body: io.NewSectionReader(f, offset, n), ContentLength: aws.Int64(n)})
		if err != nil || part == nil || aws.ToString(part.ETag) == "" {
			return errors.New("backup multipart upload failed; encrypted local archive retained")
		}
		parts = append(parts, types.CompletedPart{PartNumber: aws.Int32(number), ETag: part.ETag})
	}
	_, completeErr := client.CompleteMultipartUpload(ctx, &s3.CompleteMultipartUploadInput{Bucket: aws.String(c.Remote.Bucket), Key: aws.String(key), UploadId: created.UploadId, IfNoneMatch: aws.String("*"), MultipartUpload: &types.CompletedMultipartUpload{Parts: parts}})
	if completeErr == nil {
		finished = true
	}
	// An ambiguous completion or an existing immutable object is only accepted
	// when a complete readback matches exactly. Never use a multipart ETag here.
	if err := verifyRemote(ctx, client, c, key, a); err != nil {
		return err
	}
	if err := putImmutableJSON(ctx, client, c, remoteKey(c, m.ID, ".complete.json"), marker); err != nil {
		return err
	}
	return recovery.WriteJSON(filepath.Join(c.Directory, m.ID+".complete.json"), marker)
}

func verifyRemote(ctx context.Context, client objectStore, c Config, key string, a recovery.Artifact) error {
	r, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.Remote.Bucket), Key: aws.String(key)})
	if err != nil || r == nil || r.Body == nil {
		return errors.New("remote backup readback unavailable; no completion published")
	}
	defer r.Body.Close()
	h := sha256.New()
	n, err := io.Copy(h, io.LimitReader(r.Body, a.Size+1))
	if err != nil || n != a.Size || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return errors.New("remote backup checksum mismatch; no completion published")
	}
	return nil
}

func putImmutableJSON(ctx context.Context, client objectStore, c Config, key string, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return errors.New("invalid backup metadata")
	}
	_, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(c.Remote.Bucket), Key: aws.String(key), Body: bytes.NewReader(b), ContentLength: aws.Int64(int64(len(b))), IfNoneMatch: aws.String("*"), ContentType: aws.String("application/json")})
	if err == nil {
		return nil
	}
	r, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.Remote.Bucket), Key: aws.String(key)})
	if err != nil || r == nil || r.Body == nil {
		return errors.New("backup metadata publication failed")
	}
	defer r.Body.Close()
	got, err := io.ReadAll(io.LimitReader(r.Body, int64(len(b))+1))
	if err != nil || !bytes.Equal(got, b) {
		return errors.New("backup metadata conflict; existing object preserved")
	}
	return nil
}

func readJSON(ctx context.Context, client objectStore, c Config, key string, target any) error {
	r, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.Remote.Bucket), Key: aws.String(key)})
	if err != nil || r == nil || r.Body == nil {
		return errors.New("backup metadata unavailable")
	}
	defer r.Body.Close()
	if err := recovery.Decode(io.LimitReader(r.Body, 64<<10), target); err != nil {
		return errors.New("invalid backup metadata")
	}
	return nil
}

func readCompletion(ctx context.Context, client objectStore, c Config, id string) (Completion, error) {
	var m Completion
	if !recovery.Name.MatchString(id) {
		return m, errors.New("invalid backup ID")
	}
	if err := readJSON(ctx, client, c, remoteKey(c, id, ".complete.json"), &m); err != nil {
		return m, err
	}
	return m, validateCompletion(c, m, id)
}

func Download(ctx context.Context, c Config, id, directory string) (string, error) {
	return (Engine{}).Download(ctx, c, id, directory)
}

func (e Engine) Download(ctx context.Context, c Config, id, directory string) (string, error) {
	client, err := e.repository(c)
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	return download(ctx, client, c, id, directory)
}

func download(ctx context.Context, client objectStore, c Config, id, directory string) (string, error) {
	m, err := readCompletion(ctx, client, c, id)
	if err != nil {
		return "", err
	}
	if err := recovery.PrivateDirectory(directory); err != nil {
		return "", err
	}
	path := filepath.Join(directory, id+".age")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", errors.New("restore download target exists or unavailable")
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(path)
		}
	}()
	r, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.Remote.Bucket), Key: aws.String(remoteKey(c, id, ".age"))})
	if err != nil || r == nil || r.Body == nil {
		return "", errors.New("remote backup unavailable")
	}
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(r.Body, m.Artifact.Size+1))
	r.Body.Close()
	if copyErr != nil || n != m.Artifact.Size || hex.EncodeToString(h.Sum(nil)) != m.Artifact.SHA256 {
		return "", errors.New("downloaded backup checksum mismatch")
	}
	if err := f.Sync(); err != nil {
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	if err := recovery.WriteJSON(filepath.Join(directory, id+".complete.json"), m); err != nil {
		return "", err
	}
	ok = true
	return path, nil
}

func listKeys(ctx context.Context, client objectStore, c Config) ([]string, error) {
	var token *string
	var keys []string
	seen := map[string]bool{}
	for {
		r, err := client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(c.Remote.Bucket), Prefix: aws.String(remotePrefix(c)), ContinuationToken: token})
		if err != nil || r == nil {
			return nil, errors.New("backup repository listing unavailable")
		}
		for _, o := range r.Contents {
			key := aws.ToString(o.Key)
			if !strings.HasPrefix(key, remotePrefix(c)) {
				return nil, errors.New("repository returned out-of-scope object")
			}
			keys = append(keys, key)
			if len(keys) > 100000 {
				return nil, errors.New("backup repository listing exceeds limit")
			}
		}
		if !aws.ToBool(r.IsTruncated) {
			return keys, nil
		}
		next := aws.ToString(r.NextContinuationToken)
		if next == "" || seen[next] {
			return nil, errors.New("invalid repository pagination")
		}
		seen[next] = true
		token = aws.String(next)
	}
}

func List(ctx context.Context, c Config) ([]Completion, error) {
	return (Engine{}).List(ctx, c)
}

func (e Engine) List(ctx context.Context, c Config) ([]Completion, error) {
	client, err := e.repository(c)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	keys, err := listKeys(ctx, client, c)
	if err != nil {
		return nil, err
	}
	return listCompleted(ctx, client, c, keys)
}

func listCompleted(ctx context.Context, client objectStore, c Config, keys []string) ([]Completion, error) {
	sets := []Completion{}
	for _, key := range keys {
		name := strings.TrimPrefix(key, remotePrefix(c))
		if !strings.HasSuffix(name, ".complete.json") {
			continue
		}
		id := strings.TrimSuffix(name, ".complete.json")
		if !recovery.Name.MatchString(id) {
			continue
		}
		m, err := readCompletion(ctx, client, c, id)
		if err != nil {
			return nil, err
		}
		// A marker alone is not a retained recovery point. Fail closed if a
		// committed archive was removed or truncated outside this workflow.
		head, err := client.HeadObject(ctx, &s3.HeadObjectInput{Bucket: aws.String(c.Remote.Bucket), Key: aws.String(remoteKey(c, id, ".age"))})
		if err != nil || head == nil || head.ContentLength == nil || *head.ContentLength != m.Artifact.Size {
			return nil, errors.New("completed backup archive missing or changed; repository requires investigation")
		}
		sets = append(sets, m)
	}
	sort.Slice(sets, func(i, j int) bool {
		if sets[i].Manifest.FinishedAt.Equal(sets[j].Manifest.FinishedAt) {
			return sets[i].Manifest.ID > sets[j].Manifest.ID
		}
		return sets[i].Manifest.FinishedAt.After(sets[j].Manifest.FinishedAt)
	})
	return sets, nil
}

func validDrill(c Config, d Drill) error {
	if d.Version != Version || d.Environment != c.Environment || d.Stack != c.Stack || d.ClusterID != c.ClusterID || !recovery.Name.MatchString(d.BackupID) || d.SystemIdentifier != c.Source.SystemIdentifier || !imageDigest.MatchString(d.PostgresImage) || d.FinishedAt.IsZero() {
		return errors.New("invalid restore drill evidence")
	}
	return nil
}

func RecordDrill(ctx context.Context, c Config, d Drill) error {
	return (Engine{}).RecordDrill(ctx, c, d)
}

func (e Engine) RecordDrill(ctx context.Context, c Config, d Drill) error {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	client, err := e.repository(c)
	if err != nil {
		return err
	}
	if err := validDrill(c, d); err != nil {
		return err
	}
	m, err := readCompletion(ctx, client, c, d.BackupID)
	if err != nil {
		return err
	}
	if d.PostgresImage != m.Manifest.PostgresImage || d.FinishedAt.Before(m.Manifest.FinishedAt) {
		return errors.New("restore drill differs from backup")
	}
	nonce := make([]byte, 8)
	if _, err := rand.Read(nonce); err != nil {
		return err
	}
	return putImmutableJSON(ctx, client, c, remotePrefix(c)+"drills/"+d.BackupID+"-"+hex.EncodeToString(nonce)+".json", d)
}

func listDrills(ctx context.Context, client objectStore, c Config, keys []string) ([]Drill, error) {
	var drills []Drill
	for _, key := range keys {
		if !strings.HasPrefix(key, remotePrefix(c)+"drills/") || !strings.HasSuffix(key, ".json") {
			continue
		}
		var d Drill
		if err := readJSON(ctx, client, c, key, &d); err != nil {
			return nil, err
		}
		if err := validDrill(c, d); err != nil {
			return nil, err
		}
		drills = append(drills, d)
	}
	sort.Slice(drills, func(i, j int) bool { return drills[i].FinishedAt.After(drills[j].FinishedAt) })
	return drills, nil
}

func GetStatus(ctx context.Context, c Config) (Status, error) {
	return (Engine{}).GetStatus(ctx, c)
}

func (e Engine) GetStatus(ctx context.Context, c Config) (Status, error) {
	s := Status{Version: Version, Environment: c.Environment, Stack: c.Stack, ClusterID: c.ClusterID, GeneratedAt: time.Now().UTC()}
	client, err := e.repository(c)
	if err != nil {
		return s, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	keys, err := listKeys(ctx, client, c)
	if err != nil {
		return s, err
	}
	sets, err := listCompleted(ctx, client, c, keys)
	if err != nil {
		return s, err
	}
	drills, err := listDrills(ctx, client, c, keys)
	if err != nil {
		return s, err
	}
	s.CompletedCount = len(sets)
	if len(sets) > 0 {
		s.Latest = &sets[0]
	}
	if len(drills) > 0 {
		s.LatestDrill = &drills[0]
	}
	return s, nil
}
