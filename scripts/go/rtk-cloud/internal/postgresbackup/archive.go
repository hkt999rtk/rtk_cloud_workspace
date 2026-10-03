package postgresbackup

import (
	"archive/tar"
	"bufio"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"filippo.io/age"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
)

type contextReader struct {
	ctx context.Context
	r   io.Reader
}

func (r contextReader) Read(p []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.r.Read(p)
}

type boundedWriter struct {
	w         io.Writer
	remaining int64
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if int64(len(p)) > w.remaining {
		return 0, errors.New("encrypted backup exceeds configured limit")
	}
	n, err := w.w.Write(p)
	w.remaining -= int64(n)
	return n, err
}

// Pack writes metadata and the native plain backup as gzip level 1 inside age.
// No plaintext intermediate tar or private identity is needed by the writer.
func Pack(ctx context.Context, c Config, m Manifest, pgdata, destination string) (err error) {
	if err = c.MatchManifest(m); err != nil {
		return err
	}
	if err = validateTree(pgdata, c.MaxPlaintextBytes); err != nil {
		return err
	}
	var recipients []age.Recipient
	for _, public := range c.Recipients {
		r, e := age.ParseX25519Recipient(public)
		if e != nil {
			return errors.New("invalid backup recipient")
		}
		recipients = append(recipients, r)
	}
	f, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("cannot create encrypted backup")
	}
	ok := false
	defer func() {
		f.Close()
		if !ok {
			os.Remove(destination)
		}
	}()
	encrypted, err := age.Encrypt(&boundedWriter{w: f, remaining: c.MaxArchiveBytes}, recipients...)
	if err != nil {
		return errors.New("cannot initialize backup encryption")
	}
	compressed, err := gzip.NewWriterLevel(encrypted, gzip.BestSpeed)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(compressed)
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	if err = tw.WriteHeader(&tar.Header{Name: "manifest.json", Mode: 0600, Typeflag: tar.TypeReg, Size: int64(len(b))}); err != nil {
		return err
	}
	if _, err = tw.Write(b); err != nil {
		return err
	}
	var total int64
	err = filepath.WalkDir(pgdata, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return errors.New("cannot read native backup")
		}
		if e := ctx.Err(); e != nil {
			return e
		}
		rel, e := filepath.Rel(pgdata, path)
		if e != nil {
			return e
		}
		name := "pgdata"
		if rel != "." {
			name += "/" + filepath.ToSlash(rel)
		}
		i, e := entry.Info()
		if e != nil {
			return e
		}
		if i.IsDir() {
			return tw.WriteHeader(&tar.Header{Name: name, Mode: 0700, Typeflag: tar.TypeDir})
		}
		if !i.Mode().IsRegular() || i.Size() < 0 || i.Size() > c.MaxPlaintextBytes-total {
			return errors.New("unsafe or oversized native backup")
		}
		total += i.Size()
		if e = tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Typeflag: tar.TypeReg, Size: i.Size()}); e != nil {
			return e
		}
		in, e := os.Open(path)
		if e != nil {
			return e
		}
		defer in.Close()
		_, e = io.CopyN(tw, contextReader{ctx, in}, i.Size())
		return e
	})
	if err != nil {
		return err
	}
	for _, close := range []func() error{tw.Close, compressed.Close, encrypted.Close, f.Sync, f.Close} {
		if err = close(); err != nil {
			return err
		}
	}
	ok = true
	return nil
}

func validateTree(dir string, limit int64) error {
	return inspectTree(dir, limit, false)
}

func inspectTree(dir string, limit int64, copying bool) error {
	i, err := os.Lstat(dir)
	if copying && os.IsNotExist(err) {
		return nil
	}
	if err != nil || !i.IsDir() || i.Mode()&os.ModeSymlink != 0 {
		return errors.New("native backup directory unavailable")
	}
	var total int64
	return filepath.WalkDir(dir, func(path string, e fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			if copying && os.IsNotExist(walkErr) {
				return nil
			}
			return errors.New("cannot read native backup tree")
		}
		i, err := e.Info()
		if err != nil {
			if copying && os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if i.IsDir() {
			return nil
		}
		if !i.Mode().IsRegular() || i.Size() < 0 || i.Size() > limit-total {
			return errors.New("native backup has symlinks, special files or exceeds plaintext limit")
		}
		total += i.Size()
		return nil
	})
}

// Unpack authenticates the complete stream before returning. The caller may
// start PostgreSQL only after Restore also completes native verification.
func Unpack(ctx context.Context, c Config, file, identity, target string) (m Manifest, err error) {
	if err = c.Validate(); err != nil {
		return m, err
	}
	if err = emptyPrivateDirectory(target); err != nil {
		return m, err
	}
	ok := false
	defer func() {
		if !ok {
			removeExtracted(target)
		}
	}()
	key, err := readPrivateFile(identity, 1<<20)
	if err != nil {
		return m, errors.New("backup decryption identity unavailable")
	}
	identities, err := age.ParseIdentities(strings.NewReader(string(key)))
	if err != nil {
		return m, errors.New("invalid backup decryption identity")
	}
	i, err := os.Lstat(file)
	if err != nil || !i.Mode().IsRegular() || i.Size() < 1 || i.Size() > c.MaxArchiveBytes {
		return m, errors.New("encrypted backup unavailable or exceeds limit")
	}
	f, err := os.Open(file)
	if err != nil {
		return m, err
	}
	defer f.Close()
	plain, err := age.Decrypt(contextReader{ctx, f}, identities...)
	if err != nil {
		return m, errors.New("backup decryption failed")
	}
	buffered := bufio.NewReader(plain)
	z, err := gzip.NewReader(buffered)
	if err != nil {
		return m, errors.New("invalid compressed backup")
	}
	defer z.Close()
	z.Multistream(false)
	tr := tar.NewReader(z)
	seen := map[string]bool{}
	var total int64
	for {
		if err = ctx.Err(); err != nil {
			return m, err
		}
		h, e := tr.Next()
		if e == io.EOF {
			break
		}
		if e != nil {
			return m, errors.New("invalid backup archive")
		}
		if !recovery.SafeRelative(h.Name) || seen[h.Name] || len(seen) >= 1000000 || h.Size < 0 {
			return m, errors.New("unsafe or duplicated backup member")
		}
		if h.Name != "manifest.json" && h.Name != "pgdata" && !strings.HasPrefix(h.Name, "pgdata/") {
			return m, errors.New("unexpected backup member")
		}
		if strings.HasPrefix(h.Name, "pgdata/pg_tblspc/") {
			return m, errors.New("custom tablespaces are unsupported")
		}
		seen[h.Name] = true
		if h.Name == "manifest.json" {
			if len(seen) != 1 || h.Typeflag != tar.TypeReg || h.Size > 65536 {
				return m, errors.New("invalid metadata member")
			}
			if e = recovery.Decode(tr, &m); e != nil {
				return m, errors.New("invalid backup metadata")
			}
			if e = c.MatchManifest(m); e != nil {
				return m, e
			}
			continue
		}
		if !seen["manifest.json"] {
			return m, errors.New("backup metadata must precede data")
		}
		path := filepath.Join(target, filepath.FromSlash(h.Name))
		switch h.Typeflag {
		case tar.TypeDir:
			if h.Size != 0 {
				return m, errors.New("invalid directory member")
			}
			if e = os.MkdirAll(path, 0700); e != nil {
				return m, errors.New("cannot extract directory")
			}
		case tar.TypeReg:
			if h.Name == "pgdata" || h.Size > c.MaxPlaintextBytes-total {
				return m, errors.New("backup exceeds plaintext limit")
			}
			total += h.Size
			if e = os.MkdirAll(filepath.Dir(path), 0700); e != nil {
				return m, errors.New("cannot extract parent directory")
			}
			out, e := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
			if e != nil {
				return m, errors.New("cannot extract backup member")
			}
			_, e = io.Copy(out, contextReader{ctx, tr})
			closeErr := out.Close()
			if e != nil || closeErr != nil {
				return m, errors.New("cannot extract backup data")
			}
		default:
			return m, errors.New("links and special files are forbidden in backup")
		}
	}
	if !seen["manifest.json"] || !seen["pgdata/backup_manifest"] || !seen["pgdata/PG_VERSION"] {
		return m, errors.New("incomplete PostgreSQL backup")
	}
	// Consume gzip's checksum and age's final authentication tag; tar EOF alone
	// is not sufficient. Concatenated members and trailing data are rejected.
	if n, e := io.Copy(io.Discard, io.LimitReader(z, 1)); e != nil || n != 0 {
		return m, errors.New("compressed backup trailing data or checksum failure")
	}
	if n, e := io.Copy(io.Discard, io.LimitReader(buffered, 1)); e != nil || n != 0 {
		return m, errors.New("backup authentication or trailing-data failure")
	}
	a, err := recovery.DigestFile(filepath.Join(target, "pgdata", "backup_manifest"))
	if err != nil || a.SHA256 != m.NativeManifestSHA256 {
		return m, errors.New("native backup manifest checksum mismatch")
	}
	ok = true
	return m, nil
}

func emptyPrivateDirectory(dir string) error {
	if err := recovery.PrivateDirectory(dir); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	if len(entries) != 0 {
		return errors.New("restore requires an empty private target directory")
	}
	return nil
}

func removeExtracted(dir string) { _ = os.RemoveAll(filepath.Join(dir, "pgdata")) }

// Kubernetes projected Secrets use symlinks and can acquire group-read access
// from fsGroup. Read-only group access is allowed; world access and nonregular
// files are refused. No credential bytes are included in returned errors.
func readPrivateFile(path string, limit int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, errors.New("private credential unavailable")
	}
	defer f.Close()
	i, err := f.Stat()
	if err != nil || !i.Mode().IsRegular() || i.Mode().Perm()&0027 != 0 || i.Size() > limit {
		return nil, errors.New("private credential has invalid type, permissions or size")
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil || int64(len(b)) > limit {
		return nil, errors.New("private credential unavailable")
	}
	return b, nil
}
