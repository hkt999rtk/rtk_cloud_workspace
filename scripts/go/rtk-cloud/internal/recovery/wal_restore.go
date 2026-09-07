package recovery

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"filippo.io/age"
)

// RestoreWAL downloads only completed ciphertext and publishes a verified segment
// without replacing any existing destination. The original archive configuration
// is required because its digest is authenticated inside the encrypted envelope.
func RestoreWAL(ctx context.Context, c WALConfig, name, destination, identityFile string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if !walName.MatchString(name) {
		return errors.New("only complete WAL segments supported")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(c.TimeoutSeconds)*time.Second)
	defer cancel()
	client, err := RemoteClient(c.Remote)
	if err != nil {
		return err
	}
	return restoreWAL(ctx, client, c, name, destination, identityFile)
}

func restoreWAL(ctx context.Context, client objectStore, c WALConfig, name, destination, identityFile string) error {
	if err := c.Validate(); err != nil {
		return err
	}
	if !walName.MatchString(name) {
		return errors.New("only complete WAL segments supported")
	}
	if err := PrivateDirectory(c.Directory); err != nil {
		return err
	}
	temp, err := os.MkdirTemp(c.Directory, ".wal-restore-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(temp)
	remote := c.Remote
	remote.Prefix += "/wal-v1/" + c.SystemIdentifier
	config := Config{Environment: c.Environment, Stack: c.Stack, Remote: remote, MaxArchiveBytes: c.SegmentBytes + (1 << 20)}
	encrypted, err := download(ctx, client, config, "wal-"+strings.ToLower(name), temp)
	if err != nil {
		return err
	}
	return decryptWAL(ctx, c, name, encrypted, destination, identityFile)
}

func decryptWAL(ctx context.Context, c WALConfig, name, encrypted, destination, identityFile string) error {
	info, err := os.Lstat(identityFile)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 || info.Size() > 1<<20 {
		return errors.New("age identity must be a private regular file of at most 1 MiB")
	}
	key, err := os.Open(identityFile)
	if err != nil {
		return err
	}
	defer key.Close()
	opened, err := key.Stat()
	if err != nil || !os.SameFile(info, opened) || opened.Mode().Perm()&0077 != 0 {
		return errors.New("age identity changed")
	}
	ids, err := age.ParseIdentities(io.LimitReader(key, 1<<20))
	if err != nil {
		return errors.New("invalid age identity file")
	}
	input, err := os.Open(encrypted)
	if err != nil {
		return err
	}
	defer input.Close()
	decrypted, err := age.Decrypt(&walContextReader{ctx, input}, ids...)
	if err != nil {
		return errors.New("WAL decryption failed")
	}
	var size uint32
	if err = binary.Read(decrypted, binary.BigEndian, &size); err != nil {
		return err
	}
	if size == 0 || size > 16384 {
		return errors.New("invalid WAL envelope size")
	}
	metadata := make([]byte, size)
	if _, err = io.ReadFull(decrypted, metadata); err != nil {
		return err
	}
	var receipt walReceipt
	if err = Decode(bytes.NewReader(metadata), &receipt); err != nil {
		return err
	}
	if receipt.Version != 1 || receipt.Name != name || receipt.SystemIdentifier != c.SystemIdentifier || receipt.ConfigurationSHA256 != Digest(c) || receipt.Plaintext.Path != name || receipt.Plaintext.Size != c.SegmentBytes || receipt.Encrypted != (Artifact{}) {
		return errors.New("WAL envelope scope mismatch")
	}
	// Resolve the existing parent once so relative PostgreSQL %p paths work too.
	destination, err = filepath.Abs(destination)
	if err != nil {
		return err
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(destination))
	if err != nil {
		return err
	}
	destination = filepath.Join(parent, filepath.Base(destination))
	if _, err = os.Lstat(destination); !os.IsNotExist(err) {
		return errors.New("WAL restore destination already exists or is inaccessible")
	}
	output, err := os.CreateTemp(parent, ".wal-restore-")
	if err != nil {
		return err
	}
	defer os.Remove(output.Name())
	defer output.Close()
	hash := sha256.New()
	header := make([]byte, 40)
	if _, err = io.ReadFull(decrypted, header); err != nil {
		return err
	}
	if err = validateWALHeader(header, name, c); err != nil {
		return err
	}
	reader := io.MultiReader(bytes.NewReader(header), decrypted)
	n, err := io.Copy(io.MultiWriter(output, hash), &walContextReader{ctx, io.LimitReader(reader, c.SegmentBytes+1)})
	// Reading beyond the expected segment forces age's final authentication/EOF.
	if err != nil {
		return err
	}
	if n != c.SegmentBytes || hex.EncodeToString(hash.Sum(nil)) != receipt.Plaintext.SHA256 {
		return errors.New("WAL plaintext size/checksum mismatch")
	}
	if err = output.Sync(); err != nil {
		return err
	}
	if err = output.Close(); err != nil {
		return err
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	// Link is atomic and fails if another restore or PostgreSQL created the target.
	if err = os.Link(output.Name(), destination); err != nil {
		return err
	}
	return syncWALDirectory(parent)
}
