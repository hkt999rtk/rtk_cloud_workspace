package main

import (
	"crypto/sha256"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStorageReinitializationProfileSnapshot(t *testing.T) {
	path := filepath.Join(t.TempDir(), "profile.env")
	body := []byte("# private profile\nRTK_STORAGE_SOURCE_ENVIRONMENT=dev\nLINODE_OBJ_PREFIX=\nLINODE_OBJ_BUCKET=\"legacy-bucket\"\nPAIR=first\nPAIR=\"last=value\"\n")
	if err := os.WriteFile(path, body, 0600); err != nil {
		t.Fatal(err)
	}
	values, hash, err := readStorageReinitializationProfile(path)
	if err != nil {
		t.Fatal(err)
	}
	if hash != fmt.Sprintf("%x", sha256.Sum256(body)) || values["PAIR"] != "last=value" || values["LINODE_OBJ_BUCKET"] != "legacy-bucket" {
		t.Fatal("profile values and hash did not describe the same byte snapshot")
	}
	if prefix, exists := values["LINODE_OBJ_PREFIX"]; !exists || prefix != "" {
		t.Fatal("explicit empty source prefix was lost")
	}
	if err := os.WriteFile(path, []byte("PAIR=replaced\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if values["PAIR"] != "last=value" || hash != fmt.Sprintf("%x", sha256.Sum256(body)) {
		t.Fatal("path replacement changed the held profile snapshot")
	}
	for _, kind := range []string{"missing", "directory", "symlink", "shared permissions"} {
		t.Run(kind, func(t *testing.T) {
			bad := filepath.Join(t.TempDir(), "profile")
			switch kind {
			case "directory":
				if err := os.Mkdir(bad, 0700); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Symlink(path, bad); err != nil {
					t.Fatal(err)
				}
			case "shared permissions":
				if err := os.WriteFile(bad, body, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Chmod(bad, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := readStorageReinitializationProfile(bad); err == nil {
				t.Fatal("unsafe profile path accepted")
			}
		})
	}
}

func TestReinitializeRejectsProfileReplacementDuringProviderValidation(t *testing.T) {
	for _, profile := range []string{"candidate", "source"} {
		t.Run(profile, func(t *testing.T) {
			f := newReinitializeFixture(t)
			path := f.candidate
			if profile == "source" {
				path = f.source
			}
			body, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			transport := f.checker.client.Transport
			replaced := false
			client := *f.checker.client
			client.Transport = rolloutRoundTrip(func(request *http.Request) (*http.Response, error) {
				if !replaced && strings.HasPrefix(request.URL.Path, "/object-storage/") {
					replaced = true
					if err := os.WriteFile(path, append(body, []byte("# replaced during provider checks\n")...), 0600); err != nil {
						return nil, err
					}
				}
				return transport.RoundTrip(request)
			})
			f.checker.client = &client
			if err := f.checker.reinitializeStorage(f.cfg, "media", f.source, f.candidate, "old", false); err == nil || !strings.Contains(err.Error(), "profile") {
				t.Fatalf("profile replacement during provider validation accepted: %v", err)
			}
			commands, _ := os.ReadFile(filepath.Join(f.mockRoot, "commands"))
			if strings.Contains(string(commands), " patch ") || strings.Contains(string(commands), "create ") {
				t.Fatal("changed profiles reached Kubernetes mutation")
			}
			active, err := f.store.readOperator()
			if err != nil {
				t.Fatal(err)
			}
			if active["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"] != "active-old" {
				t.Fatal("changed profile reached promotion")
			}
			if _, err := f.store.read(storageCutoverJournalName("media")); !os.IsNotExist(err) {
				t.Fatal("changed profile reached journal creation")
			}
			if _, err := os.Stat(mustStorageStatePath(t, f.cfg, "storage-cutover.json")); !os.IsNotExist(err) {
				t.Fatal("changed profile produced activation receipt")
			}
		})
	}
}
