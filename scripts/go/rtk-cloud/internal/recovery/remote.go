package recovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

// Remote credentials deliberately do not fall back to a media/release profile.
func RemoteClient(r Remote) (*s3.Client, error) {
	key, secret := os.Getenv("RTK_BACKUP_ACCESS_KEY_ID"), os.Getenv("RTK_BACKUP_SECRET_ACCESS_KEY")
	if key == "" || secret == "" {
		return nil, errors.New("dedicated RTK_BACKUP_ACCESS_KEY_ID and RTK_BACKUP_SECRET_ACCESS_KEY required")
	}
	return s3.New(s3.Options{Region: r.Region, BaseEndpoint: aws.String(r.Endpoint), UsePathStyle: true,
		Credentials:                credentials.NewStaticCredentialsProvider(key, secret, os.Getenv("RTK_BACKUP_SESSION_TOKEN")),
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired}), nil
}

type Completion struct {
	Version     int      `json:"version"`
	ID          string   `json:"backup_id"`
	Environment string   `json:"environment"`
	Stack       string   `json:"stack"`
	Artifact    Artifact `json:"encrypted_artifact"`
}

type objectStore interface {
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

func remoteKey(c Config, id, suffix string) string {
	return c.Remote.Prefix + "/" + c.Stack + "/" + id + suffix
}

// Upload uses immutable names and only publishes completion after a full readback.
// v1 bounds archives to 4 GiB (single PUT); no auto deletion or bucket creation.
func Upload(ctx context.Context, c Config, id, file string) error {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	if !Name.MatchString(id) {
		return errors.New("invalid backup id")
	}
	client, err := RemoteClient(c.Remote)
	if err != nil {
		return err
	}
	return upload(ctx, client, c, id, file)
}

func upload(ctx context.Context, client objectStore, c Config, id, file string) error {
	i, err := os.Lstat(file)
	if err != nil || !i.Mode().IsRegular() {
		return errors.New("encrypted backup must be a regular file")
	}
	a, err := DigestFile(file)
	if err != nil {
		return err
	}
	if a.Size > c.MaxArchiveBytes {
		return errors.New("encrypted backup exceeds limit")
	}
	a.Path = id + ".age"
	f, err := os.Open(file)
	if err != nil {
		return err
	}
	defer f.Close()
	_, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(c.Remote.Bucket), Key: aws.String(remoteKey(c, id, ".age")), Body: f, ContentLength: aws.Int64(a.Size), IfNoneMatch: aws.String("*"), ContentType: aws.String("application/octet-stream")})
	// An ambiguous PUT or a retry may find an existing object. Only an exact
	// readback match may proceed; never overwrite a previous backup ID.
	got, getErr := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.Remote.Bucket), Key: aws.String(remoteKey(c, id, ".age"))})
	if getErr != nil {
		return errors.New("remote upload/readback failed; encrypted local backup retained")
	}
	h := sha256.New()
	n, copyErr := io.Copy(h, io.LimitReader(got.Body, c.MaxArchiveBytes+1))
	got.Body.Close()
	if copyErr != nil || n != a.Size || hex.EncodeToString(h.Sum(nil)) != a.SHA256 {
		return errors.New("remote readback mismatch; no completion marker published")
	}
	marker := Completion{Version: Version, ID: id, Environment: c.Environment, Stack: c.Stack, Artifact: a}
	b, _ := json.Marshal(marker)
	_, err = client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(c.Remote.Bucket), Key: aws.String(remoteKey(c, id, ".complete.json")), Body: bytes.NewReader(b), IfNoneMatch: aws.String("*"), ContentType: aws.String("application/json")})
	if err != nil {
		existing, e := readCompletion(ctx, client, c, id)
		if e != nil || Digest(existing) != Digest(marker) {
			return errors.New("remote completion publication failed")
		}
	}
	return WriteJSON(filepath.Join(filepath.Dir(file), id+".complete.json"), marker)
}

func readCompletion(ctx context.Context, client objectStore, c Config, id string) (Completion, error) {
	var m Completion
	r, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.Remote.Bucket), Key: aws.String(remoteKey(c, id, ".complete.json"))})
	if err != nil {
		return m, errors.New("remote backup has no readable completion marker")
	}
	defer r.Body.Close()
	if err = Decode(io.LimitReader(r.Body, 16384), &m); err != nil {
		return m, err
	}
	if m.Version != Version || m.ID != id || m.Environment != c.Environment || m.Stack != c.Stack || m.Artifact.Path != id+".age" || m.Artifact.Size < 1 || m.Artifact.Size > c.MaxArchiveBytes {
		return m, errors.New("remote completion identity/size mismatch")
	}
	return m, nil
}

func Download(ctx context.Context, c Config, id, directory string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	if !Name.MatchString(id) {
		return "", errors.New("invalid backup id")
	}
	client, err := RemoteClient(c.Remote)
	if err != nil {
		return "", err
	}
	return download(ctx, client, c, id, directory)
}

func download(ctx context.Context, client objectStore, c Config, id, directory string) (string, error) {
	m, err := readCompletion(ctx, client, c, id)
	if err != nil {
		return "", err
	}
	if err = PrivateDirectory(directory); err != nil {
		return "", err
	}
	dest := filepath.Join(directory, id+".age")
	f, err := os.OpenFile(dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", err
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(dest)
		}
	}()
	r, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.Remote.Bucket), Key: aws.String(remoteKey(c, id, ".age"))})
	if err != nil {
		return "", errors.New("remote download failed")
	}
	defer r.Body.Close()
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, h), io.LimitReader(r.Body, c.MaxArchiveBytes+1))
	if err != nil || n != m.Artifact.Size || hex.EncodeToString(h.Sum(nil)) != m.Artifact.SHA256 {
		return "", errors.New("download checksum mismatch")
	}
	if err = f.Sync(); err != nil {
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	ok = true
	return dest, WriteJSON(filepath.Join(directory, id+".complete.json"), m)
}
