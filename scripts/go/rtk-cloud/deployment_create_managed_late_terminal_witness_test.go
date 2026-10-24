package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func terminalWitnessFixture(t *testing.T) (secretStore, string, string, string) {
	t.Helper()
	root := t.TempDir()
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", root)
	store, err := newSecretStore(root, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if err := ensurePrivateDirectory(store.ConfigRoot); err != nil {
		t.Fatal(err)
	}
	parent := strings.Repeat("a", 64)
	primary := filepath.Join(store.Root, managedLateTerminalFinalizerPath(parent))
	witness := filepath.Join(store.Root, managedLateTerminalFinalizerAdmissionPath(parent))
	if err := ensurePrivateDirectory(filepath.Dir(primary)); err != nil {
		t.Fatal(err)
	}
	return store, parent, primary, witness
}

func TestManagedLateTerminalWitnessPreservesFirstAdmission(t *testing.T) {
	store, parent, primary, witness := terminalWitnessFixture(t)
	if raw, err := readManagedLateTerminalFinalizerAuthorityPair(store, parent); err != nil || raw != nil {
		t.Fatalf("fresh pair: raw=%q err=%v", raw, err)
	}
	raw := []byte(`{"StartedAt":"2030-01-01T00:00:00Z","Deadline":"2030-01-01T00:30:00Z","OverallDeadline":"2030-01-01T01:30:00Z"}`)
	if err := writeManagedLateTerminalFinalizerAuthorityPair(store, parent, raw); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{primary, witness} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			t.Fatalf("private admission %s: %v", path, err)
		}
	}
	if err := writeManagedLateTerminalFinalizerAuthorityPair(store, parent, []byte(`{"reset":true}`)); err == nil {
		t.Fatal("existing admission was overwritten")
	}
	observed, err := readManagedLateTerminalFinalizerAuthorityPair(store, parent)
	if err != nil || !bytes.Equal(observed, raw) {
		t.Fatalf("first clocks changed: %q %v", observed, err)
	}
}

func TestManagedLateTerminalWitnessRefusesPartialOrUnsafeState(t *testing.T) {
	for _, name := range []string{"primary-missing", "witness-missing", "different", "primary-public", "witness-public", "witness-symlink", "primary-directory", "empty-witness", "public-parent"} {
		t.Run(name, func(t *testing.T) {
			store, parent, primary, witness := terminalWitnessFixture(t)
			raw := []byte(`{"first_admission":true}`)
			if err := writeManagedLateTerminalFinalizerAuthorityPair(store, parent, raw); err != nil {
				t.Fatal(err)
			}
			var err error
			switch name {
			case "primary-missing":
				err = os.Remove(primary)
			case "witness-missing":
				err = os.Remove(witness)
			case "different":
				err = os.WriteFile(witness, []byte(`{"foreign":true}`), 0600)
			case "primary-public":
				err = os.Chmod(primary, 0644)
			case "witness-public":
				err = os.Chmod(witness, 0644)
			case "witness-symlink":
				if err = os.Remove(witness); err == nil {
					err = os.Symlink(primary, witness)
				}
			case "primary-directory":
				if err = os.Remove(primary); err == nil {
					err = os.Mkdir(primary, 0700)
				}
			case "empty-witness":
				err = os.Truncate(witness, 0)
			case "public-parent":
				err = os.Chmod(filepath.Dir(primary), 0755)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := readManagedLateTerminalFinalizerAuthorityPair(store, parent); err == nil {
				t.Fatal("unsafe or incomplete pair accepted")
			}
			if err := writeManagedLateTerminalFinalizerAuthorityPair(store, parent, []byte(`{"reset":true}`)); err == nil {
				t.Fatal("partial admission was replaced by a new clock")
			}
		})
	}
}
