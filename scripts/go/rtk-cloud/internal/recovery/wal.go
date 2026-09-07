package recovery

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"filippo.io/age"
	"golang.org/x/sys/unix"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// WALConfig is independent of maintenance backups. No writer is paused.
type WALConfig struct {
	Version          int      `json:"version"`
	Environment      string   `json:"environment"`
	Stack            string   `json:"stack"`
	SystemIdentifier string   `json:"system_identifier"`
	SegmentBytes     int64    `json:"segment_bytes"`
	Directory        string   `json:"directory"`
	Recipients       []string `json:"recipients"`
	Remote           Remote   `json:"remote"`
	TimeoutSeconds   int      `json:"timeout_seconds"`
}
type walReceipt struct {
	Version             int      `json:"version"`
	Name                string   `json:"wal_name"`
	ConfigurationSHA256 string   `json:"configuration_sha256"`
	SystemIdentifier    string   `json:"system_identifier"`
	Plaintext           Artifact `json:"plaintext"`
	Encrypted           Artifact `json:"encrypted"`
}

var walName = regexp.MustCompile(`^[0-9A-F]{24}$`)

func (c WALConfig) Validate() error {
	sysid, err := strconv.ParseUint(c.SystemIdentifier, 10, 64)
	if c.Version != 1 || !Name.MatchString(c.Environment) || !Name.MatchString(c.Stack) || err != nil || sysid == 0 || strconv.FormatUint(sysid, 10) != c.SystemIdentifier {
		return errors.New("invalid WAL archive scope/system identifier")
	}
	if c.SegmentBytes < 1<<20 || c.SegmentBytes > 1<<30 || c.SegmentBytes&(c.SegmentBytes-1) != 0 || c.TimeoutSeconds < 1 || c.TimeoutSeconds > 900 {
		return errors.New("invalid WAL size/deadline")
	}
	if !filepath.IsAbs(c.Directory) || filepath.Clean(c.Directory) != c.Directory || c.Directory == "/" || (len(c.Recipients) == 0 || len(c.Recipients) > 32) {
		return errors.New("private WAL spool and recipients required")
	}
	for _, r := range c.Recipients {
		if _, err := age.ParseX25519Recipient(r); err != nil {
			return errors.New("X25519 WAL recipients required")
		}
	}
	u, err := url.Parse(c.Remote.Endpoint)
	if err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") || c.Remote.Bucket == "" || c.Remote.Region == "" || !SafeRelative(c.Remote.Prefix) || !strings.HasPrefix(c.Remote.Prefix, c.Environment+"/") {
		return errors.New("private environment-scoped HTTPS WAL destination required")
	}
	return nil
}

// PostgreSQL 16 little-endian/MAXALIGN=8 with 8192-byte WAL pages. Checks the
// long-page identity header, not record CRCs or replay eligibility.
func validateWALHeader(h []byte, name string, c WALConfig) error {
	if !walName.MatchString(name) || len(h) != 40 {
		return errors.New("full WAL segment name/header required")
	}
	o := binary.LittleEndian
	tli, _ := strconv.ParseUint(name[:8], 16, 32)
	log, _ := strconv.ParseUint(name[8:16], 16, 32)
	seg, _ := strconv.ParseUint(name[16:], 16, 32)
	sysid, _ := strconv.ParseUint(c.SystemIdentifier, 10, 64)
	if tli == 0 || seg >= (uint64(1)<<32)/uint64(c.SegmentBytes) || o.Uint16(h) != 0xD113 || o.Uint16(h[2:])&2 == 0 || o.Uint16(h[2:])&^uint16(15) != 0 || o.Uint32(h[4:]) != uint32(tli) || o.Uint64(h[8:]) != (log<<32)+seg*uint64(c.SegmentBytes) || o.Uint64(h[24:]) != sysid || o.Uint32(h[32:]) != uint32(c.SegmentBytes) || o.Uint32(h[36:]) != 8192 {
		return errors.New("WAL header differs from PostgreSQL 16 cluster/segment")
	}
	return nil
}

// ArchiveWAL succeeds only after immutable remote readback and completion.
// Failed upload retains exact randomized ciphertext for a retry.
func ArchiveWAL(ctx context.Context, c WALConfig, name, source string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	file, id, err := stageWAL(ctx, c, name, source)
	if err != nil {
		return err
	}
	remote := c.Remote
	remote.Prefix += "/wal-v1/" + c.SystemIdentifier
	return Upload(ctx, Config{Environment: c.Environment, Stack: c.Stack, Remote: remote, TimeoutSeconds: c.TimeoutSeconds, MaxArchiveBytes: c.SegmentBytes + (1 << 20)}, id, file)
}
func stageWAL(ctx context.Context, c WALConfig, name, source string) (string, string, error) {
	if err := c.Validate(); err != nil {
		return "", "", err
	}
	if !walName.MatchString(name) {
		return "", "", errors.New("only complete WAL segments supported")
	}
	if err := PrivateDirectory(c.Directory); err != nil {
		return "", "", err
	}
	id := "wal-" + strings.ToLower(name)
	lock, err := os.OpenFile(filepath.Join(c.Directory, id+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", "", err
	}
	defer lock.Close()
	if err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		return "", "", errors.New("WAL archive busy; retry")
	}
	defer unix.Flock(int(lock.Fd()), unix.LOCK_UN)
	info, err := os.Lstat(source)
	if err != nil || !info.Mode().IsRegular() || info.Size() != c.SegmentBytes {
		return "", "", errors.New("complete regular WAL segment required")
	}
	input, err := os.Open(source)
	if err != nil {
		return "", "", err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil || !os.SameFile(info, opened) {
		return "", "", errors.New("WAL source changed")
	}
	h := make([]byte, 40)
	if _, err = io.ReadFull(input, h); err != nil {
		return "", "", err
	}
	if err = validateWALHeader(h, name, c); err != nil {
		return "", "", err
	}
	if _, err = input.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	hash := sha256.New()
	n, err := io.Copy(hash, &walContextReader{ctx, io.LimitReader(input, c.SegmentBytes+1)})
	if err != nil || n != c.SegmentBytes {
		return "", "", errors.New("WAL source read failed")
	}
	plain := Artifact{Path: name, Size: n, SHA256: hex.EncodeToString(hash.Sum(nil))}
	destination := filepath.Join(c.Directory, id)
	cipherPath := filepath.Join(destination, id+".age")
	if info, err := os.Lstat(destination); err == nil {
		if !info.IsDir() || info.Mode().Perm() != 0700 {
			return "", "", errors.New("invalid WAL retry directory")
		}
		f, err := os.Open(filepath.Join(destination, "receipt.json"))
		if err != nil {
			return "", "", err
		}
		defer f.Close()
		var receipt walReceipt
		if err = Decode(io.LimitReader(f, 16384), &receipt); err != nil {
			return "", "", err
		}
		ci, err := os.Lstat(cipherPath)
		if err != nil || !ci.Mode().IsRegular() || ci.Mode().Perm() != 0600 || ci.Size() > c.SegmentBytes+(1<<20) {
			return "", "", errors.New("invalid WAL retry ciphertext")
		}
		encrypted, err := DigestFile(cipherPath)
		if err != nil || receipt.Version != 1 || receipt.Name != name || receipt.SystemIdentifier != c.SystemIdentifier || receipt.ConfigurationSHA256 != Digest(c) || receipt.Plaintext != plain || receipt.Encrypted.Path != id+".age" || receipt.Encrypted.Size != encrypted.Size || receipt.Encrypted.SHA256 != encrypted.SHA256 {
			return "", "", errors.New("WAL retry identity/content mismatch")
		}
		return cipherPath, id, nil
	} else if !os.IsNotExist(err) {
		return "", "", err
	}
	temp, err := os.MkdirTemp(c.Directory, ".wal-")
	if err != nil {
		return "", "", err
	}
	defer os.RemoveAll(temp)
	encryptedPath := filepath.Join(temp, id+".age")
	output, err := os.OpenFile(encryptedPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", "", err
	}
	defer output.Close()
	recipients := []age.Recipient{}
	for _, raw := range c.Recipients {
		r, _ := age.ParseX25519Recipient(raw)
		recipients = append(recipients, r)
	}
	writer, err := age.Encrypt(output, recipients...)
	if err != nil {
		return "", "", err
	}
	receipt := walReceipt{Version: 1, Name: name, SystemIdentifier: c.SystemIdentifier, ConfigurationSHA256: Digest(c), Plaintext: plain}
	metadata, _ := json.Marshal(receipt)
	if err = binary.Write(writer, binary.BigEndian, uint32(len(metadata))); err != nil {
		return "", "", err
	}
	if _, err = writer.Write(metadata); err != nil {
		return "", "", err
	}
	if _, err = input.Seek(0, io.SeekStart); err != nil {
		return "", "", err
	}
	hash.Reset()
	n, err = io.Copy(writer, io.TeeReader(&walContextReader{ctx, io.LimitReader(input, c.SegmentBytes+1)}, hash))
	if err != nil || n != plain.Size || hex.EncodeToString(hash.Sum(nil)) != plain.SHA256 {
		return "", "", errors.New("WAL changed during encryption")
	}
	if err = writer.Close(); err != nil {
		return "", "", err
	}
	if err = output.Sync(); err != nil {
		return "", "", err
	}
	if err = output.Close(); err != nil {
		return "", "", err
	}
	receipt.Encrypted, err = DigestFile(encryptedPath)
	if err != nil {
		return "", "", err
	}
	receipt.Encrypted.Path = id + ".age"
	if err = WriteJSON(filepath.Join(temp, "receipt.json"), receipt); err != nil {
		return "", "", err
	}
	if err = syncWALDirectory(temp); err != nil {
		return "", "", err
	}
	if err = ctx.Err(); err != nil {
		return "", "", err
	}
	if err = os.Rename(temp, destination); err != nil {
		return "", "", err
	}
	if err = syncWALDirectory(c.Directory); err != nil {
		return "", "", err
	}
	return cipherPath, id, nil
}
func syncWALDirectory(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

type walContextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r *walContextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}
