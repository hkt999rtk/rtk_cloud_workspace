package recovery

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"

	"filippo.io/age"
)

func TestWALRestoreCompletedRemote(t *testing.T) {
	c, name, source, identity := walFixture(t)
	encrypted, id, err := stageWAL(context.Background(), c, name, source)
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(filepath.Dir(source), "identity")
	if err = os.WriteFile(key, []byte(identity.String()), 0600); err != nil {
		t.Fatal(err)
	}
	remote := c.Remote
	remote.Prefix += "/wal-v1/" + c.SystemIdentifier
	config := Config{Environment: c.Environment, Stack: c.Stack, Remote: remote, MaxArchiveBytes: c.SegmentBytes + (1 << 20)}
	objects := &memoryObjects{objects: map[string][]byte{}}
	if err = upload(context.Background(), objects, config, id, encrypted); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(filepath.Dir(source), "restored")
	if err = restoreWAL(context.Background(), objects, c, name, destination, key); err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(source)
	actual, _ := os.ReadFile(destination)
	if !bytes.Equal(original, actual) {
		t.Fatal("restore bytes differ")
	}
	info, _ := os.Stat(destination)
	if info.Mode().Perm() != 0600 {
		t.Fatal("plaintext permissions")
	}
	if err = restoreWAL(context.Background(), objects, c, name, destination, key); err == nil {
		t.Fatal("existing target replaced")
	}
	for _, mode := range []string{"missing-completion", "corrupt-remote", "cancelled", "symlink", "public-key", "wrong-config"} {
		t.Run(mode, func(t *testing.T) {
			dest := filepath.Join(filepath.Dir(source), mode)
			ctx := context.Background()
			cfg := c
			marker := remoteKey(config, id, ".complete.json")
			switch mode {
			case "missing-completion":
				saved := objects.objects[marker]
				delete(objects.objects, marker)
				defer func() { objects.objects[marker] = saved }()
			case "corrupt-remote":
				objects.corrupt = true
				defer func() { objects.corrupt = false }()
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "symlink":
				if err := os.Symlink(source, dest); err != nil {
					t.Fatal(err)
				}
			case "public-key":
				os.Chmod(key, 0644)
				defer os.Chmod(key, 0600)
			case "wrong-config":
				cfg.TimeoutSeconds++
			}
			if err := restoreWAL(ctx, objects, cfg, name, dest, key); err == nil {
				t.Fatal("invalid restore accepted")
			}
			if mode != "symlink" {
				if _, err := os.Lstat(dest); !os.IsNotExist(err) {
					t.Fatal("failed restore published output", err)
				}
			}
			current, _ := os.ReadFile(source)
			if !bytes.Equal(current, original) {
				t.Fatal("existing source changed")
			}
		})
	}
	leftovers, _ := filepath.Glob(filepath.Join(c.Directory, ".wal-restore-*"))
	if len(leftovers) != 0 {
		t.Fatal("download temporary files retained")
	}
}

func TestWALRestoreAuthenticatedEnvelopeFailures(t *testing.T) {
	c, name, source, identity := walFixture(t)
	encrypted, _, err := stageWAL(context.Background(), c, name, source)
	if err != nil {
		t.Fatal(err)
	}
	cipher, _ := os.ReadFile(encrypted)
	reader, err := age.Decrypt(bytes.NewReader(cipher), identity)
	if err != nil {
		t.Fatal(err)
	}
	plain, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(filepath.Dir(source), "identity")
	if err = os.WriteFile(key, []byte(identity.String()), 0600); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"truncated-cipher", "damaged-tag", "extra-plaintext", "short-plaintext", "bad-header", "bad-hash", "wrong-name", "oversized-metadata", "wrong-key"} {
		t.Run(mode, func(t *testing.T) {
			raw := append([]byte(nil), plain...)
			offset := 4 + int(binary.BigEndian.Uint32(raw))
			switch mode {
			case "extra-plaintext":
				raw = append(raw, 0)
			case "short-plaintext":
				raw = raw[:len(raw)-1]
			case "bad-header":
				raw[offset] ^= 1
			case "bad-hash":
				raw[len(raw)-1] ^= 1
			case "wrong-name":
				var receipt walReceipt
				json.Unmarshal(raw[4:offset], &receipt)
				receipt.Name = "000000010000000000000002"
				meta, _ := json.Marshal(receipt)
				var b bytes.Buffer
				binary.Write(&b, binary.BigEndian, uint32(len(meta)))
				b.Write(meta)
				b.Write(raw[offset:])
				raw = b.Bytes()
			case "oversized-metadata":
				binary.BigEndian.PutUint32(raw, 16385)
			}
			recipient := identity.Recipient()
			if mode == "wrong-key" {
				other, _ := age.GenerateX25519Identity()
				recipient = other.Recipient()
			}
			var b bytes.Buffer
			w, err := age.Encrypt(&b, recipient)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = w.Write(raw); err != nil {
				t.Fatal(err)
			}
			if err = w.Close(); err != nil {
				t.Fatal(err)
			}
			bad := b.Bytes()
			if mode == "truncated-cipher" {
				bad = bad[:len(bad)-1]
			}
			if mode == "damaged-tag" {
				bad[len(bad)-1] ^= 1
			}
			path := filepath.Join(filepath.Dir(source), mode+".age")
			if err = os.WriteFile(path, bad, 0600); err != nil {
				t.Fatal(err)
			}
			dest := filepath.Join(filepath.Dir(source), mode)
			if err = decryptWAL(context.Background(), c, name, path, dest, key); err == nil {
				t.Fatal("invalid encrypted payload accepted")
			}
			if _, err = os.Lstat(dest); !os.IsNotExist(err) {
				t.Fatal("invalid plaintext published")
			}
			leftovers, _ := filepath.Glob(filepath.Join(filepath.Dir(source), ".wal-restore-*"))
			if len(leftovers) != 0 {
				t.Fatal("plaintext temporary files retained")
			}
		})
	}
}
