package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadTokenMapFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	if err := os.WriteFile(path, []byte(`{"cam-1":"token-from-file"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := loadTokenMapFlag("device-token-map", "", path)
	if err != nil {
		t.Fatal(err)
	}
	if got["cam-1"] != "token-from-file" {
		t.Fatalf("token map = %#v", got)
	}
}

func TestLoadTokenMapRejectsJSONAndFileTogether(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	if err := os.WriteFile(path, []byte(`{"cam-1":"token-from-file"}`), 0o600); err != nil {
		t.Fatal(err)
	}

	if _, err := loadTokenMapFlag("device-token-map", `{"cam-1":"token-from-json"}`, path); err == nil {
		t.Fatal("expected ambiguity error")
	}
}
