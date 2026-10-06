package postgresbackup

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"filippo.io/age"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
)

var walName = regexp.MustCompile(`^([0-9A-F]{24}(\.[0-9A-F]{8}\.backup)?|[0-9A-F]{8}\.history)$`)

type WALManifest struct {
	Version          int       `json:"version"`
	Environment      string    `json:"environment"`
	Stack            string    `json:"stack"`
	ClusterID        string    `json:"cluster_id"`
	SystemIdentifier string    `json:"system_identifier"`
	Name             string    `json:"name"`
	PlaintextSHA256  string    `json:"plaintext_sha256"`
	Size             int64     `json:"size"`
	ArchivedAt       time.Time `json:"archived_at"`
}
type WALCompletion struct {
	Manifest WALManifest       `json:"manifest"`
	Artifact recovery.Artifact `json:"encrypted_artifact"`
}

func walKey(c Config, name string) string { return remotePrefix(c) + "wal/" + name }

// VerifyWALSource binds a native archive command to the actual PostgreSQL
// cluster. A configured identifier alone cannot authenticate a source segment.
func (e Engine) VerifyWALSource(ctx context.Context, c Config, pgdata, name, source string) error {
	if !cleanAbsolute(pgdata) || !walName.MatchString(name) {
		return errors.New("absolute PostgreSQL data directory and exact WAL name required")
	}
	want := filepath.Join(pgdata, "pg_wal", name)
	if !filepath.IsAbs(source) {
		source = filepath.Join(pgdata, source)
	}
	if filepath.Clean(source) != want {
		return errors.New("WAL source is not the selected cluster's native archive segment")
	}
	version, err := os.ReadFile(filepath.Join(pgdata, "PG_VERSION"))
	if err != nil || strings.TrimSpace(string(version)) != "16" {
		return errors.New("WAL source PostgreSQL version mismatch")
	}
	var out cappedBuffer
	if err := e.run(ctx, []string{"pg_controldata", pgdata}, safeEnvironment(), &out); err != nil {
		return err
	}
	identifier := ""
	for _, line := range strings.Split(out.String(), "\n") {
		if key, value, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(key) == "Database system identifier" {
			identifier = strings.TrimSpace(value)
		}
	}
	if identifier != c.Source.SystemIdentifier {
		return errors.New("WAL source system identifier mismatch")
	}
	return nil
}
func validateWAL(c Config, m WALManifest, name string) error {
	if m.Version != 1 || m.Environment != c.Environment || m.Stack != c.Stack || m.ClusterID != c.ClusterID || m.SystemIdentifier != c.Source.SystemIdentifier || m.Name != name || !walName.MatchString(name) || !sha256Hex.MatchString(m.PlaintextSHA256) || m.Size < 1 || m.Size > min(c.MaxPlaintextBytes, int64(1<<30)) || m.ArchivedAt.IsZero() {
		return errors.New("WAL archive identity or integrity is invalid")
	}
	return nil
}

// ArchiveWAL is suitable for archive_command: success is returned only after
// encrypted remote data is read back and an immutable completion is published.
// PostgreSQL retains/retries the segment whenever this method returns an error.
func (e Engine) ArchiveWAL(ctx context.Context, c Config, name, source string) (WALCompletion, error) {
	var receipt WALCompletion
	client, err := e.repository(c)
	if err != nil {
		return receipt, err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	if !walName.MatchString(name) {
		return receipt, errors.New("invalid WAL segment, history or backup-history name")
	}
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Size() < 1 || info.Size() > min(c.MaxPlaintextBytes, int64(1<<30)) {
		return receipt, errors.New("WAL source must be a bounded regular file")
	}
	plain, err := recovery.DigestFile(source)
	if err != nil {
		return receipt, errors.New("cannot read WAL source")
	}
	if existing, err := readWALCompletion(ctx, client, c, name); err == nil {
		if existing.Manifest.PlaintextSHA256 != plain.SHA256 || existing.Manifest.Size != plain.Size {
			return receipt, errors.New("archived WAL name already belongs to different data")
		}
		if err := verifyWALRemote(ctx, client, c, existing.Artifact); err != nil {
			return receipt, err
		}
		return existing, nil
	}
	if err := recovery.PrivateDirectory(c.Directory); err != nil {
		return receipt, err
	}
	f, err := os.CreateTemp(c.Directory, ".wal-*.age")
	if err != nil {
		return receipt, errors.New("cannot create private encrypted WAL spool")
	}
	defer os.Remove(f.Name())
	defer f.Close()
	m := WALManifest{1, c.Environment, c.Stack, c.ClusterID, c.Source.SystemIdentifier, name, plain.SHA256, plain.Size, time.Now().UTC()}
	var recipients []age.Recipient
	for _, public := range c.Recipients {
		recipient, err := age.ParseX25519Recipient(public)
		if err != nil {
			return receipt, errors.New("invalid WAL recipient")
		}
		recipients = append(recipients, recipient)
	}
	encrypted, err := age.Encrypt(&boundedWriter{w: f, remaining: c.MaxArchiveBytes}, recipients...)
	if err != nil {
		return receipt, err
	}
	header, _ := json.Marshal(m)
	if _, err = encrypted.Write(append(header, '\n')); err != nil {
		return receipt, errors.New("cannot encrypt WAL metadata")
	}
	input, err := os.Open(source)
	if err != nil {
		return receipt, errors.New("WAL source unavailable")
	}
	hash := sha256.New()
	n, copyErr := io.Copy(encrypted, io.TeeReader(io.LimitReader(contextReader{ctx, input}, m.Size+1), hash))
	input.Close()
	closeErr := encrypted.Close()
	syncErr := f.Sync()
	if copyErr != nil || closeErr != nil || syncErr != nil || n != m.Size || hex.EncodeToString(hash.Sum(nil)) != m.PlaintextSHA256 {
		return receipt, errors.New("WAL changed during encrypted capture or capture failed")
	}
	artifact, err := recovery.DigestFile(f.Name())
	if err != nil {
		return receipt, err
	}
	artifact.Path = "wal/" + name + "/" + artifact.SHA256 + ".age"
	if _, err = f.Seek(0, io.SeekStart); err != nil {
		return receipt, err
	}
	_, uploadErr := client.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(c.Remote.Bucket), Key: aws.String(remotePrefix(c) + artifact.Path), Body: f, ContentLength: aws.Int64(artifact.Size), IfNoneMatch: aws.String("*"), ContentType: aws.String("application/octet-stream")})
	if err := verifyWALRemote(ctx, client, c, artifact); err != nil {
		if uploadErr != nil {
			return receipt, errors.New("WAL upload incomplete; PostgreSQL must retry")
		}
		return receipt, err
	}
	receipt = WALCompletion{m, artifact}
	if err := putImmutableJSON(ctx, client, c, walKey(c, name)+".json", receipt); err != nil {
		old, err := readWALCompletion(ctx, client, c, name)
		if err != nil || old.Manifest.PlaintextSHA256 != m.PlaintextSHA256 || old.Manifest.Size != m.Size {
			return WALCompletion{}, errors.New("WAL completion was not published; PostgreSQL must retry")
		}
		if err := verifyWALRemote(ctx, client, c, old.Artifact); err != nil {
			return WALCompletion{}, err
		}
		return old, nil
	}
	return receipt, nil
}

func readWALCompletion(ctx context.Context, client objectStore, c Config, name string) (WALCompletion, error) {
	var receipt WALCompletion
	response, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.Remote.Bucket), Key: aws.String(walKey(c, name) + ".json")})
	if err != nil || response == nil || response.Body == nil {
		return receipt, errors.New("WAL completion unavailable")
	}
	defer response.Body.Close()
	if recovery.Decode(io.LimitReader(response.Body, 32<<10), &receipt) != nil || validateWAL(c, receipt.Manifest, name) != nil || !sha256Hex.MatchString(receipt.Artifact.SHA256) || receipt.Artifact.Path != "wal/"+name+"/"+receipt.Artifact.SHA256+".age" || receipt.Artifact.Size < 1 || receipt.Artifact.Size > c.MaxArchiveBytes {
		return receipt, errors.New("WAL completion invalid")
	}
	return receipt, nil
}
func verifyWALRemote(ctx context.Context, client objectStore, c Config, artifact recovery.Artifact) error {
	response, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.Remote.Bucket), Key: aws.String(remotePrefix(c) + artifact.Path)})
	if err != nil || response == nil || response.Body == nil {
		return errors.New("encrypted WAL read-back unavailable")
	}
	defer response.Body.Close()
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(contextReader{ctx, response.Body}, artifact.Size+1))
	if err != nil || n != artifact.Size || hex.EncodeToString(hash.Sum(nil)) != artifact.SHA256 {
		return errors.New("encrypted WAL read-back checksum mismatch")
	}
	return nil
}

// RestoreWAL binds authenticated age metadata to the explicitly selected
// database and segment. A target is created exclusively, never overwritten.
func (e Engine) RestoreWAL(ctx context.Context, c Config, name, identityFile, target string) error {
	client, err := e.repository(c)
	if err != nil {
		return err
	}
	if !walName.MatchString(name) || !filepath.IsAbs(target) {
		return errors.New("exact WAL name and absolute restore target required")
	}
	receipt, err := readWALCompletion(ctx, client, c, name)
	if err != nil {
		return err
	}
	if err := verifyWALRemote(ctx, client, c, receipt.Artifact); err != nil {
		return err
	}
	identityInfo, err := os.Lstat(identityFile)
	if err != nil || !identityInfo.Mode().IsRegular() || identityInfo.Mode().Perm() != 0600 {
		return errors.New("private regular age identity required")
	}
	raw, err := os.ReadFile(identityFile)
	if err != nil {
		return errors.New("restore identity unavailable")
	}
	identities, err := age.ParseIdentities(bytes.NewReader(raw))
	if err != nil {
		return errors.New("restore identity invalid")
	}
	response, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(c.Remote.Bucket), Key: aws.String(remotePrefix(c) + receipt.Artifact.Path)})
	if err != nil || response == nil || response.Body == nil {
		return errors.New("encrypted WAL unavailable")
	}
	defer response.Body.Close()
	decrypted, err := age.Decrypt(contextReader{ctx, io.LimitReader(response.Body, receipt.Artifact.Size+1)}, identities...)
	if err != nil {
		return errors.New("cannot authenticate WAL archive")
	}
	buffer := bufio.NewReaderSize(decrypted, 32<<10)
	header, err := buffer.ReadSlice('\n')
	if err != nil {
		return errors.New("invalid encrypted WAL metadata")
	}
	var m WALManifest
	if recovery.Decode(bytes.NewReader(header), &m) != nil || m != receipt.Manifest {
		return errors.New("authenticated WAL metadata does not match selected restore")
	}
	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("WAL restore target must be absent")
	}
	complete := false
	defer func() {
		f.Close()
		if !complete {
			os.Remove(target)
		}
	}()
	hash := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, hash), io.LimitReader(buffer, m.Size+1))
	if err != nil || n != m.Size || hex.EncodeToString(hash.Sum(nil)) != m.PlaintextSHA256 || f.Sync() != nil {
		return errors.New("restored WAL authentication or checksum failed")
	}
	complete = true
	return nil
}
