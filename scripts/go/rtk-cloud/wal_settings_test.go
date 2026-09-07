package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/recovery"
)

func TestWALArchiveConfigPublication(t *testing.T) {
	dir := t.TempDir()
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	c := recovery.WALConfig{Version: 1, Environment: "test", Stack: "pki", SystemIdentifier: "123", SegmentBytes: 16 << 20, Directory: filepath.Join(dir, "spool"), Recipients: []string{identity.Recipient().String()}, Remote: recovery.Remote{Endpoint: "https://backup.example", Region: "test", Bucket: "private", Prefix: "test/pki"}, TimeoutSeconds: 60}
	config := filepath.Join(dir, "wal.json")
	if err = recovery.WriteJSON(config, c); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(dir, "postgresql.conf")
	args := []string{"--config", config, "--executable", "/opt/rtk/rtk-cloud", "--output", output, "--confirm-environment", "wrong", "--confirm-stack", "pki"}
	if err = runWALArchiveConfig(args); err == nil {
		t.Fatal("scope mismatch accepted")
	}
	if _, err = os.Lstat(output); !os.IsNotExist(err) {
		t.Fatal("scope mismatch wrote config")
	}
	args[7] = "test"
	if err = runWALArchiveConfig(args); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(before), "archive_timeout = '60s'") {
		t.Fatal("missing timeout")
	}
	if err = runWALArchiveConfig(args); err == nil {
		t.Fatal("existing config overwritten")
	}
	after, _ := os.ReadFile(output)
	if string(before) != string(after) {
		t.Fatal("existing config changed")
	}
	info, _ := os.Stat(output)
	if info.Mode().Perm() != 0600 {
		t.Fatal("config permissions")
	}
}
