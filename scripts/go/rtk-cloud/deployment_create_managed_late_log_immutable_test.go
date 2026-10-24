package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateLogImmutableFixture(t *testing.T) (secretStore, string, deploymentplan.Object) {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, "dev")
	path := managedLateLogFile(store, strings.Repeat("a", 64), "managed-late-log-observation.json")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	return store, path, deploymentplan.Object{"schema": "isolated-log-observation/v1", "actual": true}
}

func TestManagedLateLogImmutableCreatesAbsentAndRetriesExactBytes(t *testing.T) {
	store, path, value := managedLateLogImmutableFixture(t)
	if err := writeManagedLateLogImmutable(store, path, value); err != nil {
		t.Fatal("genuinely absent lifecycle file was refused", err)
	}
	want, _ := json.Marshal(value)
	raw, err := os.ReadFile(path)
	first, statErr := os.Lstat(path)
	if err != nil || statErr != nil || !bytes.Equal(raw, want) || !first.Mode().IsRegular() || first.Mode().Perm() != 0600 {
		t.Fatal("immutable lifecycle file bytes or private mode differ")
	}
	if err := writeManagedLateLogImmutable(store, path, value); err != nil {
		t.Fatal("exact existing bytes retry refused", err)
	}
	last, err := os.Lstat(path)
	if err != nil || !os.SameFile(first, last) || !first.ModTime().Equal(last.ModTime()) {
		t.Fatal("exact retry replaced or rewrote the lifecycle file")
	}
	if err := writeManagedLateLogImmutable(store, path, deploymentplan.Object{"different": "private-value-canary"}); err == nil || strings.Contains(err.Error(), "canary") {
		t.Fatal("changed immutable evidence passed or escaped")
	}
	after, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(raw, after) {
		t.Fatal("refusal changed original bytes")
	}
}

func TestManagedLateLogImmutableRefusesUnsafeExistingAndParentPaths(t *testing.T) {
	for _, mode := range []string{"malformed", "empty", "public-mode", "directory", "symlink", "dangling-symlink", "nonprivate-parent", "symlink-parent", "missing-parent", "outside-store", "oversized-existing", "unencodable", "oversized-value"} {
		t.Run(mode, func(t *testing.T) {
			store, path, value := managedLateLogImmutableFixture(t)
			var input any = value
			original := []byte("private-original-canary")
			write := func(target string, raw []byte, permission os.FileMode) {
				t.Helper()
				if err := os.WriteFile(target, raw, permission); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "malformed":
				write(path, original, 0600)
			case "empty":
				write(path, nil, 0600)
			case "public-mode":
				write(path, mustProviderRecoveryJSON(value), 0644)
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink", "dangling-symlink":
				target := filepath.Join(t.TempDir(), "foreign-target")
				if mode == "symlink" {
					write(target, mustProviderRecoveryJSON(value), 0600)
				}
				if err := os.Symlink(target, path); err != nil {
					t.Fatal(err)
				}
			case "nonprivate-parent":
				if err := os.Chmod(filepath.Dir(path), 0750); err != nil {
					t.Fatal(err)
				}
			case "symlink-parent":
				parent := filepath.Dir(path)
				if err := os.Remove(parent); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), parent); err != nil {
					t.Fatal(err)
				}
			case "missing-parent":
				path = filepath.Join(filepath.Dir(path), "missing", "observation.json")
			case "outside-store":
				path = filepath.Join(t.TempDir(), "observation.json")
			case "oversized-existing":
				write(path, bytes.Repeat([]byte("x"), (4<<20)+1), 0600)
			case "unencodable":
				input = make(chan int)
			case "oversized-value":
				input = strings.Repeat("x", environmentPreflightArtifactLimit+1)
			}
			before, beforeErr := os.ReadFile(path)
			beforeInfo, beforeStatErr := os.Lstat(path)
			err := writeManagedLateLogImmutable(store, path, input)
			if err == nil || strings.Contains(err.Error(), "canary") || strings.Contains(err.Error(), path) {
				t.Fatal("unsafe immutable artifact passed or exposed native contents/path")
			}
			after, afterErr := os.ReadFile(path)
			afterInfo, afterStatErr := os.Lstat(path)
			if beforeErr == nil && (afterErr != nil || !bytes.Equal(before, after)) || beforeStatErr == nil && (afterStatErr != nil || !os.SameFile(beforeInfo, afterInfo)) || os.IsNotExist(beforeStatErr) && !os.IsNotExist(afterStatErr) {
				t.Fatal("unsafe-file refusal wrote, replaced or removed evidence")
			}
		})
	}
}

type managedLateLogImmutableRace struct {
	path  string
	calls int
}

func (value *managedLateLogImmutableRace) MarshalJSON() ([]byte, error) {
	value.calls++
	if value.calls == 2 {
		// The first encoding preceded Lstat; the immutable primitive encodes
		// again before O_EXCL. A competing creator must retain its own bytes.
		if err := os.WriteFile(value.path, []byte("competing-original-canary"), 0600); err != nil {
			return nil, err
		}
	}
	return []byte(`{"actual":"reviewed"}`), nil
}

func TestManagedLateLogImmutableCreateRaceCannotOverwrite(t *testing.T) {
	store, path, _ := managedLateLogImmutableFixture(t)
	value := &managedLateLogImmutableRace{path: path}
	err := writeManagedLateLogImmutable(store, path, value)
	raw, readErr := os.ReadFile(path)
	if err == nil || value.calls != 2 || strings.Contains(err.Error(), "canary") || readErr != nil || string(raw) != "competing-original-canary" {
		t.Fatal("exclusive create race overwrote, accepted or leaked competing evidence")
	}
}
