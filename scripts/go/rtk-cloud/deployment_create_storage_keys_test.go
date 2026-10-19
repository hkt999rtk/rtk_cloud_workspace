package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestProviderStorageKeyPromotionRetainsExactBytesAndRejectsDrift(t *testing.T) {
	for _, state := range []string{"original", "already-promoted", "concurrent-value"} {
		t.Run(state, func(t *testing.T) {
			root := t.TempDir()
			store := secretStore{ConfigRoot: root, Environment: "dev", Root: filepath.Join(root, "dev")}
			name := "LINODE_MEDIA_OBJ_ACCESS_KEY_ID"
			previous, next := []byte("original-key"), []byte("new-key\n")
			active := previous
			if state == "already-promoted" {
				active = next
			}
			if state == "concurrent-value" {
				active = []byte("unrelated-key\n")
			}
			if err := store.write(filepath.Join("operator", "env", name), active, false); err != nil {
				t.Fatal(err)
			}
			if err := store.write(filepath.Join("deployment", "create", "recovery-storage-keys", "original", name), previous, false); err != nil {
				t.Fatal(err)
			}
			err := writeProviderStorageKeyValue(store, name, previous, next)
			if (err != nil) != (state == "concurrent-value") {
				t.Fatalf("unexpected promotion result: %v", err)
			}
			actual, err := os.ReadFile(filepath.Join(store.Root, "operator", "env", name))
			if err != nil {
				t.Fatal(err)
			}
			expected := next
			if state == "concurrent-value" {
				expected = active
			}
			if string(actual) != string(expected) {
				t.Fatal("promotion changed exact bytes or overwrote concurrent credentials")
			}
			preserved, err := os.ReadFile(filepath.Join(store.Root, "deployment", "create", "recovery-storage-keys", "original", name))
			if err != nil || string(preserved) != string(previous) {
				t.Fatal("original bytes were changed")
			}
		})
	}
}

func TestProviderStorageKeyReadDistinguishesAbsentAndInvalidEvidence(t *testing.T) {
	root := t.TempDir()
	missing := filepath.Join(root, "absent.json")
	if _, err := readProviderStorageKeyFile(missing); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing response must remain distinguishable: %v", err)
	}
	unsafe := filepath.Join(root, "unsafe.json")
	if err := os.WriteFile(unsafe, []byte("one-time-secret-response"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readProviderStorageKeyFile(unsafe); err == nil || errors.Is(err, os.ErrNotExist) {
		t.Fatal("unsafe response must block issuance rather than be treated as absent")
	}
	link := filepath.Join(root, "link.json")
	if err := os.Symlink(unsafe, link); err != nil {
		t.Fatal(err)
	}
	if _, err := readProviderStorageKeyFile(link); err == nil {
		t.Fatal("symlink response accepted")
	}
}
