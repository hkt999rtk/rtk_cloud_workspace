package recovery

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"filippo.io/age"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func walFixture(t *testing.T) (WALConfig, string, string, *age.X25519Identity) {
	t.Helper()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := WALConfig{Version: 1, Environment: "test", Stack: "pki", SystemIdentifier: "123456", SegmentBytes: 1 << 20, Directory: filepath.Join(dir, "spool"), Recipients: []string{identity.Recipient().String()}, Remote: Remote{Endpoint: "https://backup.example", Bucket: "private", Region: "test", Prefix: "test/pki"}, TimeoutSeconds: 10}
	name := "000000010000000000000001"
	raw := make([]byte, cfg.SegmentBytes)
	binary.LittleEndian.PutUint16(raw, 0xD113)
	binary.LittleEndian.PutUint16(raw[2:], 2)
	binary.LittleEndian.PutUint32(raw[4:], 1)
	binary.LittleEndian.PutUint64(raw[8:], uint64(cfg.SegmentBytes))
	binary.LittleEndian.PutUint64(raw[24:], 123456)
	binary.LittleEndian.PutUint32(raw[32:], uint32(cfg.SegmentBytes))
	binary.LittleEndian.PutUint32(raw[36:], 8192)
	source := filepath.Join(dir, name)
	if err = os.WriteFile(source, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return cfg, name, source, identity
}
func TestWALCiphertextRetryIdentityAndRemoteReadback(t *testing.T) {
	cfg, name, source, identity := walFixture(t)
	encrypted, id, err := stageWAL(context.Background(), cfg, name, source)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(encrypted)
	if err != nil {
		t.Fatal(err)
	}
	decrypted, err := age.Decrypt(bytes.NewReader(before), identity)
	if err != nil {
		t.Fatal(err)
	}
	var size uint32
	if err = binary.Read(decrypted, binary.BigEndian, &size); err != nil || size > 16384 {
		t.Fatal("metadata framing", err)
	}
	header := make([]byte, size)
	if _, err = io.ReadFull(decrypted, header); err != nil {
		t.Fatal(err)
	}
	var receipt walReceipt
	if json.Unmarshal(header, &receipt) != nil || receipt.Name != name || receipt.SystemIdentifier != cfg.SystemIdentifier {
		t.Fatal("encrypted identity mismatch")
	}
	plain, err := io.ReadAll(decrypted)
	if err != nil {
		t.Fatal(err)
	}
	original, _ := os.ReadFile(source)
	if !bytes.Equal(plain, original) {
		t.Fatal("WAL plaintext changed")
	}
	if _, _, err = stageWAL(context.Background(), cfg, name, source); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(encrypted)
	if !bytes.Equal(before, after) {
		t.Fatal("retry regenerated randomized ciphertext")
	}
	remote := cfg.Remote
	remote.Prefix += "/wal-v1/" + cfg.SystemIdentifier
	uploadConfig := Config{Environment: cfg.Environment, Stack: cfg.Stack, Remote: remote, MaxArchiveBytes: cfg.SegmentBytes + (1 << 20)}
	objects := &memoryObjects{objects: map[string][]byte{}, ambiguous: true}
	if err = upload(context.Background(), objects, uploadConfig, id, encrypted); err != nil {
		t.Fatal(err)
	}
	if err = upload(context.Background(), objects, uploadConfig, id, encrypted); err != nil {
		t.Fatal("remote immutable retry", err)
	}
	objects.corrupt = true
	if err = upload(context.Background(), objects, uploadConfig, id, encrypted); err == nil {
		t.Fatal("corrupt remote WAL accepted")
	}
	original[100] = 1
	if err = os.WriteFile(source, original, 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err = stageWAL(context.Background(), cfg, name, source); err == nil {
		t.Fatal("same WAL name different content accepted")
	}
}
func TestWALHeaderScopeAndUnsupportedFormats(t *testing.T) {
	cfg, name, source, _ := walFixture(t)
	raw, _ := os.ReadFile(source)
	for _, offset := range []int{0, 2, 4, 8, 24, 32, 36} {
		bad := append([]byte(nil), raw[:40]...)
		bad[offset] ^= 128
		if validateWALHeader(bad, name, cfg) == nil {
			t.Fatalf("bad header offset %d accepted", offset)
		}
	}
	for _, name := range []string{"../wal", "00000002.history", "000000010000000000000001.partial", "000000010000000000001000"} {
		if validateWALHeader(raw[:40], name, cfg) == nil {
			t.Fatal("unsupported filename accepted", name)
		}
	}
	cfg.SystemIdentifier = "999"
	if _, _, err := stageWAL(context.Background(), cfg, name, source); err == nil {
		t.Fatal("wrong cluster accepted")
	}
}
func TestRealPostgres16WALFixture(t *testing.T) {
	source := os.Getenv("RTK_TEST_WAL_FILE")
	if source == "" {
		t.Skip("real PostgreSQL WAL fixture not configured")
	}
	cfg, _, _, _ := walFixture(t)
	cfg.SystemIdentifier = os.Getenv("RTK_TEST_WAL_SYSTEM_ID")
	cfg.SegmentBytes = 16 << 20
	if _, _, err := stageWAL(context.Background(), cfg, filepath.Base(source), source); err != nil {
		t.Fatal(err)
	}
}
