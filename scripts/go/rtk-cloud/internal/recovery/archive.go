package recovery

import (
	"archive/tar"
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

	"filippo.io/age"
)

func DigestFile(path string) (Artifact, error) {
	f, err := os.Open(path)
	if err != nil {
		return Artifact{}, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	return Artifact{Size: n, SHA256: hex.EncodeToString(h.Sum(nil))}, err
}

func Digest(v any) string {
	b, _ := json.Marshal(v)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}

func WriteJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".recovery-")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if _, err = f.Write(append(b, '\n')); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	return os.Rename(tmp, path)
}

// Pack only accepts regular files created inside the private capture directory.
// It never follows a symlink or imports a caller-provided archive path.
func Pack(dir, destination string, m Manifest, recipients []string) error {
	if m.Version != Version || m.Scope != "core" || !Name.MatchString(m.ID) {
		return errors.New("invalid backup manifest")
	}
	var rs []age.Recipient
	for _, s := range recipients {
		r, err := age.ParseX25519Recipient(s)
		if err != nil {
			return errors.New("invalid age X25519 recipient")
		}
		rs = append(rs, r)
	}
	if len(rs) == 0 {
		return errors.New("encryption recipient required")
	}
	m.Artifacts = nil
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if e.Name() == "manifest.json" {
			continue
		}
		if !e.Type().IsRegular() || !SafeRelative(e.Name()) {
			return errors.New("capture contains non-regular artifact")
		}
		a, err := DigestFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return err
		}
		a.Path = e.Name()
		m.Artifacts = append(m.Artifacts, a)
	}
	if len(m.Artifacts) == 0 {
		return errors.New("empty backup")
	}
	if err := WriteJSON(filepath.Join(dir, "manifest.json"), m); err != nil {
		return err
	}
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		out.Close()
		if !ok {
			os.Remove(destination)
		}
	}()
	encrypted, err := age.Encrypt(out, rs...)
	if err != nil {
		return err
	}
	tw := tar.NewWriter(encrypted)
	files := []string{"manifest.json"}
	for _, a := range m.Artifacts {
		files = append(files, a.Path)
	}
	for _, name := range files {
		f, err := os.Open(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		i, err := f.Stat()
		if err != nil {
			f.Close()
			return err
		}
		err = tw.WriteHeader(&tar.Header{Name: name, Mode: 0600, Size: i.Size(), Typeflag: tar.TypeReg})
		if err == nil {
			_, err = io.Copy(tw, f)
		}
		f.Close()
		if err != nil {
			return err
		}
	}
	if err = tw.Close(); err != nil {
		return err
	}
	if err = encrypted.Close(); err != nil {
		return err
	}
	if err = out.Sync(); err != nil {
		return err
	}
	if err = out.Close(); err != nil {
		return err
	}
	ok = true
	return nil
}

// Unpack authenticates the entire age stream, validates all members and hashes,
// and writes only to a new private staging directory. No target is mutated here.
func Unpack(source, identityFile, dir string, limit int64) (Manifest, error) {
	var m Manifest
	if limit < 1 {
		return m, errors.New("archive limit required")
	}
	i, err := os.Lstat(identityFile)
	if err != nil {
		return m, errors.New("age identity unavailable")
	}
	if !i.Mode().IsRegular() || i.Mode().Perm()&0077 != 0 {
		return m, errors.New("identity must be a private regular file")
	}
	key, err := os.Open(identityFile)
	if err != nil {
		return m, err
	}
	ids, err := age.ParseIdentities(io.LimitReader(key, 1<<20))
	key.Close()
	if err != nil {
		return m, errors.New("invalid age identity file")
	}
	f, err := os.Open(source)
	if err != nil {
		return m, err
	}
	defer f.Close()
	r, err := age.Decrypt(f, ids...)
	if err != nil {
		return m, errors.New("backup decryption failed")
	}
	if err = PrivateDirectory(dir); err != nil {
		return m, err
	}
	tr := tar.NewReader(r)
	seen := map[string]bool{}
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return m, errors.New("invalid encrypted archive")
		}
		if !SafeRelative(h.Name) || strings.Contains(h.Name, "/") || h.Typeflag != tar.TypeReg || seen[h.Name] || h.Size < 0 || h.Size > limit-total || len(seen) >= 10000 {
			return m, errors.New("unsafe or oversized archive member")
		}
		seen[h.Name] = true
		total += h.Size
		out, err := os.OpenFile(filepath.Join(dir, h.Name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err != nil {
			return m, err
		}
		_, err = io.Copy(out, tr)
		closeErr := out.Close()
		if err != nil {
			return m, err
		}
		if closeErr != nil {
			return m, closeErr
		}
	}
	// tar EOF alone does not authenticate a truncated/modified age final chunk.
	n, err := io.Copy(io.Discard, io.LimitReader(r, 1<<20))
	if err != nil || n != 0 {
		return m, errors.New("archive authentication/trailing-data failure")
	}
	mf, err := os.Open(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return m, err
	}
	err = Decode(io.LimitReader(mf, 4<<20), &m)
	mf.Close()
	if err != nil {
		return m, err
	}
	if m.Version != Version || m.Scope != "core" || !Name.MatchString(m.ID) || !Name.MatchString(m.Environment) || !Name.MatchString(m.Stack) || len(m.Artifacts) == 0 {
		return m, errors.New("invalid manifest")
	}
	if len(seen) != len(m.Artifacts)+1 {
		return m, errors.New("unlisted or missing artifact")
	}
	listed := map[string]bool{}
	for _, a := range m.Artifacts {
		if !SafeRelative(a.Path) || strings.Contains(a.Path, "/") || a.Path == "manifest.json" || listed[a.Path] || !seen[a.Path] {
			return m, errors.New("invalid manifest artifact")
		}
		listed[a.Path] = true
		actual, err := DigestFile(filepath.Join(dir, a.Path))
		if err != nil {
			return m, err
		}
		if actual.Size != a.Size || actual.SHA256 != a.SHA256 {
			return m, errors.New("artifact checksum mismatch")
		}
	}
	return m, nil
}

// WalkArchive rejects links/devices/traversal before a volume can be restored.
func WalkArchive(path string, limit int64, visit func(*tar.Header, io.Reader) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	tr := tar.NewReader(f)
	seen := map[string]bool{}
	regular := map[string]bool{}
	directories := map[string]bool{}
	var total int64
	for {
		h, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return errors.New("invalid volume archive")
		}
		name := strings.TrimPrefix(h.Name, "./")
		name = strings.TrimSuffix(name, "/")
		if name == "." || name == "" {
			if h.Typeflag == tar.TypeDir {
				continue
			}
			return errors.New("invalid archive root")
		}
		if !SafeRelative(name) || seen[name] || len(seen) >= 100000 || (h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeDir) || h.Size < 0 || h.Size > limit-total || h.Mode&06000 != 0 {
			return errors.New("unsafe volume archive")
		}
		if h.Typeflag == tar.TypeReg {
			if directories[name] {
				return errors.New("archive file conflicts with a directory")
			}
			regular[name] = true
		} else {
			directories[name] = true
		}
		for parent := filepath.ToSlash(filepath.Dir(name)); parent != "."; parent = filepath.ToSlash(filepath.Dir(parent)) {
			if regular[parent] {
				return errors.New("archive parent is a regular file")
			}
			directories[parent] = true
		}
		seen[name] = true
		total += h.Size
		h.Name = name
		if visit != nil {
			if err = visit(h, tr); err != nil {
				return err
			}
		}
	}
}

func PackPaths(root string, paths []string, destination string) error {
	out, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	defer out.Close()
	tw := tar.NewWriter(out)
	seen := map[string]bool{}
	for _, p := range paths {
		if !SafeRelative(p) {
			return errors.New("unsafe source path")
		}
		if err := safeAncestors(root, p); err != nil {
			return err
		}
		err = filepath.WalkDir(filepath.Join(root, p), func(path string, e os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			if seen[rel] {
				return nil
			}
			seen[rel] = true
			if e.IsDir() {
				return nil
			}
			if !e.Type().IsRegular() {
				return errors.New("source contains a link or special file")
			}
			f, err := os.Open(path)
			if err != nil {
				return err
			}
			defer f.Close()
			i, err := f.Stat()
			if err != nil {
				return err
			}
			if err = tw.WriteHeader(&tar.Header{Name: rel, Mode: 0600, Size: i.Size(), Typeflag: tar.TypeReg}); err != nil {
				return err
			}
			_, err = io.Copy(tw, f)
			return err
		})
		if err != nil {
			return err
		}
	}
	return tw.Close()
}

func safeAncestors(root, p string) error {
	for current := filepath.Join(root, filepath.FromSlash(p)); ; current = filepath.Dir(current) {
		i, err := os.Lstat(current)
		if err != nil {
			return err
		}
		if i.Mode()&os.ModeSymlink != 0 {
			return errors.New("source has a symlink ancestor")
		}
		if current == root {
			break
		}
		if current == filepath.Dir(current) {
			return errors.New("source is outside root")
		}
	}
	return nil
}

// ReplaceSecretPaths removes only inventoried regular files, after validating
// the entire archive and the existing selected trees. It preserves operator,
// kubeconfig, recovery journal and separately escrowed unseal material.
func ReplaceSecretPaths(archive, root string, c Component, limit int64) error {
	if err := WalkArchive(archive, limit, func(h *tar.Header, _ io.Reader) error {
		if !selectedSecret(c, h.Name) {
			return errors.New("unselected SecretStore artifact")
		}
		return nil
	}); err != nil {
		return err
	}
	var files []string
	for _, p := range c.Paths {
		full := filepath.Join(root, p)
		if _, err := os.Lstat(full); os.IsNotExist(err) {
			continue
		}
		if err := safeAncestors(root, p); err != nil {
			return err
		}
		if err := filepath.WalkDir(full, func(path string, d os.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			if !d.Type().IsRegular() {
				return errors.New("SecretStore target contains a link/special file")
			}
			files = append(files, path)
			return nil
		}); err != nil {
			return err
		}
	}
	for _, path := range files {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return RestorePaths(archive, root, limit, func(name string) bool { return selectedSecret(c, name) })
}

func RestorePaths(archive, root string, limit int64, allowed func(string) bool) error {
	if err := WalkArchive(archive, limit, func(h *tar.Header, _ io.Reader) error {
		if !allowed(h.Name) {
			return fmt.Errorf("path not allowed: %s", h.Name)
		}
		return nil
	}); err != nil {
		return err
	}
	return WalkArchive(archive, limit, func(h *tar.Header, r io.Reader) error {
		path := filepath.Join(root, filepath.FromSlash(h.Name))
		if h.Typeflag == tar.TypeDir {
			return PrivateDirectory(path)
		}
		if err := PrivateDirectory(filepath.Dir(path)); err != nil {
			return err
		}
		if i, err := os.Lstat(path); err == nil && !i.Mode().IsRegular() {
			return errors.New("target is not a regular file")
		}
		tmp, err := os.CreateTemp(filepath.Dir(path), ".restore-")
		if err != nil {
			return err
		}
		defer os.Remove(tmp.Name())
		_, err = io.Copy(tmp, r)
		ce := tmp.Close()
		if err != nil {
			return err
		}
		if ce != nil {
			return ce
		}
		return os.Rename(tmp.Name(), path)
	})
}

func InventoryFingerprint(c Config) string {
	// Physical PVC/pod identities may change after reprovisioning, but logical
	// dataset selections must not. Namespace and workload identity remain fixed.
	parts := append([]Component(nil), c.Components...)
	for i := range parts {
		parts[i].PVC = ""
		parts[i].Pod = ""
		parts[i].Image = ""
		parts[i].Container = ""
	}
	sort.Slice(parts, func(i, j int) bool { return parts[i].ID < parts[j].ID })
	return Digest(struct {
		Environment, Stack string
		Components         []Component
	}{c.Environment, c.Stack, parts})
}
