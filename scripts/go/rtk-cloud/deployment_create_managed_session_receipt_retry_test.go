package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestManagedSessionPreservedClientsReceiptRetry(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	path := filepath.Join("deployment", "create", "test", "managed-clients-preserved.json")
	original := map[string]string{"original_session_id": "original", "renewed_session_id": "renewed", "clients_sha256": "abc", "original_key_sha256": "key", "original_csr_sha256": "csr"}
	if err := recordManagedSessionClients(store, path, original); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(store.Root, path))
	if err != nil {
		t.Fatal(err)
	}
	if err := recordManagedSessionClients(store, path, original); err != nil {
		t.Fatal("identical retry rejected:", err)
	}
	after, err := os.ReadFile(filepath.Join(store.Root, path))
	if err != nil || string(before) != string(after) {
		t.Fatal("original receipt changed", err)
	}
	changed := map[string]string{"original_session_id": "other", "renewed_session_id": "renewed", "clients_sha256": "abc", "original_key_sha256": "key", "original_csr_sha256": "csr"}
	if err := recordManagedSessionClients(store, path, changed); err == nil {
		t.Fatal("changed identity accepted")
	}
}
