package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestPKIDevBrokerCredentialsPersistWithoutReplacingPartialState(t *testing.T) {
	store, err := newSecretStore(t.TempDir(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	dir, err := preparePKIDevBroker(store)
	if err != nil {
		t.Fatal(err)
	}
	original := map[string][]byte{}
	for _, name := range pkiDevBrokerFiles {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		original[name] = raw
	}
	if _, err := preparePKIDevBroker(store); err != nil {
		t.Fatal(err)
	}
	for name, want := range original {
		got, _ := os.ReadFile(filepath.Join(dir, name))
		if !bytes.Equal(want, got) {
			t.Fatal("retry rotated credential")
		}
	}
	if err := os.Remove(filepath.Join(dir, "api-secret")); err != nil {
		t.Fatal(err)
	}
	if _, err := preparePKIDevBroker(store); err == nil {
		t.Fatal("partial credentials replaced")
	}
	got, _ := os.ReadFile(filepath.Join(dir, "database-password"))
	if !bytes.Equal(original["database-password"], got) {
		t.Fatal("failure rotated unrelated credential")
	}
	if err := os.Symlink(filepath.Join(dir, "api-key"), filepath.Join(dir, "api-secret")); err != nil {
		t.Fatal(err)
	}
	if _, err := preparePKIDevBroker(store); err == nil {
		t.Fatal("symlink accepted")
	}
	store.Environment = "staging"
	if _, err := preparePKIDevBroker(store); err == nil {
		t.Fatal("staging accepted")
	}
}
