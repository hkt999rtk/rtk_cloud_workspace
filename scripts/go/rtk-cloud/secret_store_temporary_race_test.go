package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func nativeTemporaryRaceStore(t *testing.T) secretStore {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, "dev")
	for _, entry := range rtkSecretCatalog() {
		if err := store.write("runtime/"+entry.ID, []byte("isolated-test-value"), true); err != nil {
			t.Fatal(err)
		}
	}
	if err := verifySecretStoreContents(store); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestSecretStoreNativeTemporaryCleanupKeepsContentsReadable(t *testing.T) {
	store := nativeTemporaryRaceStore(t)
	backend := environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-kubeconfig")}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	var wg sync.WaitGroup
	ready := make(chan struct{}, 4)
	failures := make(chan error, 4)
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			first := true
			for ctx.Err() == nil {
				_, cleanup, err := backend.privateKubeconfig()
				if err != nil {
					failures <- err
					return
				}
				if first {
					ready <- struct{}{}
					first = false
				}
				cleanup()
			}
		}()
	}
	for i := 0; i < 4; i++ {
		<-ready
	}
	var observed error
	for i := 0; i < 200; i++ {
		if err := verifySecretStorePermissionsOnly(store); err != nil {
			observed = err
			break
		}
		if err := verifySecretStoreContents(store); err != nil {
			observed = err
			break
		}
	}
	cancel()
	wg.Wait()
	close(failures)
	for err := range failures {
		t.Fatalf("temporary native identity producer failed: %v", err)
	}
	if observed != nil {
		t.Fatalf("whole-store input verifier failed during native temporary cleanup: %v (not-exist=%t)", observed, errors.Is(observed, os.ErrNotExist))
	}
	if err := verifySecretStoreContents(store); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(filepath.Join(store.Root, "deployment", "tmp"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatal("native cleanup left temporary artifacts")
	}
}

func nativeTemporaryEntry(t *testing.T, path string) os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == filepath.Base(path) {
			return entry
		}
	}
	t.Fatal("missing controlled native temporary entry")
	return nil
}

func TestSecretStoreNativeTemporaryDisappearanceIsExact(t *testing.T) {
	store := nativeTemporaryRaceStore(t)
	base := filepath.Join(store.Root, "deployment", "tmp")
	dir := filepath.Join(base, "kubernetes-api-123456")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(dir, "kubeconfig.yaml")
	if err := os.WriteFile(file, []byte("isolated-kubeconfig"), 0600); err != nil {
		t.Fatal(err)
	}
	fileEntry, dirEntry := nativeTemporaryEntry(t, file), nativeTemporaryEntry(t, dir)
	if err := os.RemoveAll(dir); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"directory-walk-error", "directory-lstat", "file-lstat"} {
		t.Run(kind, func(t *testing.T) {
			path, entry := dir, dirEntry
			if kind == "file-lstat" {
				path, entry = file, fileEntry
			}
			_, err := os.Lstat(path)
			if !errors.Is(err, os.ErrNotExist) {
				t.Fatal("controlled entry did not disappear")
			}
			if !disappearedNativeKubernetesTemporaryEntry(store.Root, path, entry, err) {
				t.Fatal("exact disappearing native temporary entry rejected")
			}
		})
	}
	bad := []struct {
		name, path string
		entry      os.DirEntry
		err        error
	}{
		{"store-root", store.Root, dirEntry, os.ErrNotExist},
		{"deployment-root", filepath.Join(store.Root, "deployment"), dirEntry, os.ErrNotExist},
		{"temporary-root", base, dirEntry, os.ErrNotExist},
		{"runtime-secret", filepath.Join(store.Root, "runtime", "postgres"), fileEntry, os.ErrNotExist},
		{"pki-state", filepath.Join(store.Root, "pki", "certissuer", "key.pem"), fileEntry, os.ErrNotExist},
		{"journal-state", filepath.Join(store.Root, "deployment", "rollback", "original", "journal.json"), fileEntry, os.ErrNotExist},
		{"foreign-temporary-producer", filepath.Join(base, "zammad-123456"), dirEntry, os.ErrNotExist},
		{"foreign-suffix", filepath.Join(base, "kubernetes-api-foreign"), dirEntry, os.ErrNotExist},
		{"empty-suffix", filepath.Join(base, "kubernetes-api-"), dirEntry, os.ErrNotExist},
		{"foreign-leaf", filepath.Join(dir, "private-key.pem"), fileEntry, os.ErrNotExist},
		{"nested-leaf", filepath.Join(dir, "nested", "kubeconfig.yaml"), fileEntry, os.ErrNotExist},
		{"wrong-entry-kind", dir, fileEntry, os.ErrNotExist},
		{"permission-error", dir, dirEntry, os.ErrPermission},
		{"other-error", file, fileEntry, errors.New("isolated filesystem failure")},
		{"no-error", file, fileEntry, nil},
		{"not-enumerated", dir, nil, os.ErrNotExist},
	}
	for _, x := range bad {
		t.Run(x.name, func(t *testing.T) {
			if disappearedNativeKubernetesTemporaryEntry(store.Root, x.path, x.entry, x.err) {
				t.Fatal("foreign or current state was waived")
			}
		})
	}
}

func TestSecretStoreNativeTemporaryCleanupPreservesExistingGuards(t *testing.T) {
	for _, mode := range []string{"insecure-file", "insecure-directory", "symlink", "missing-required-secret", "missing-root", "insecure-root"} {
		t.Run(mode, func(t *testing.T) {
			store := nativeTemporaryRaceStore(t)
			base := filepath.Join(store.Root, "deployment", "tmp", "kubernetes-api-123456")
			if err := os.MkdirAll(base, 0700); err != nil {
				t.Fatal(err)
			}
			file := filepath.Join(base, "kubeconfig.yaml")
			switch mode {
			case "insecure-file":
				if err := os.WriteFile(file, []byte("isolated"), 0644); err != nil {
					t.Fatal(err)
				}
			case "insecure-directory":
				if err := os.Chmod(base, 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(filepath.Join(store.Root, "inventory.json"), file); err != nil {
					t.Fatal(err)
				}
			case "missing-required-secret":
				if err := os.Remove(filepath.Join(store.RuntimeDir(), "postgres")); err != nil {
					t.Fatal(err)
				}
			case "missing-root":
				if err := os.RemoveAll(store.Root); err != nil {
					t.Fatal(err)
				}
			case "insecure-root":
				if err := os.Chmod(store.Root, 0755); err != nil {
					t.Fatal(err)
				}
			}
			if err := verifySecretStoreContents(store); err == nil {
				t.Fatal("unsafe or missing state accepted")
			} else if mode == "missing-required-secret" && !strings.Contains(err.Error(), "required runtime secrets are missing") {
				t.Fatal("required-state validation did not execute")
			}
			if mode == "missing-root" || mode == "insecure-root" {
				if err := verifySecretStorePermissionsOnly(store); err == nil {
					t.Fatal("unsafe root accepted by root permission verifier")
				}
			}
		})
	}
}
