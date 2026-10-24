package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCertIssuerJournalIsEnvironmentOwnedAcrossCheckouts(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, environment)
			values := map[string]string{"CLOUD_ENV_NAME": environment, "CLOUD_STACK_NAME": "video-cloud-" + environment}
			paths := provisionPaths{EnvRoot: t.TempDir(), ArtifactsDir: filepath.Join(t.TempDir(), "unused")}
			p := &lkeCertIssuerIngressMigration{paths: paths, env: values, ID: "certissuer-123", Status: "rollback-blocked", Changes: []certIssuerIngressChange{{Before: certIssuerIngressObject{"metadata": map[string]any{"uid": "original", "resourceVersion": "7"}}}}}
			if err := p.save(); err != nil {
				t.Fatal(err)
			}
			wanted := filepath.Join(store.Root, certIssuerIngressJournalRelativeDir, p.ID+".json")
			if p.RestorePath != wanted {
				t.Fatal("journal location depends on checkout or artifact override")
			}
			raw, err := readStorageEvidenceFile(wanted)
			if err != nil {
				t.Fatal(err)
			}
			var record lkeCertIssuerIngressMigration
			if err := json.Unmarshal(raw, &record); err != nil || !certIssuerObjectsSame(record.Changes[0].Before, p.Changes[0].Before) {
				t.Fatal("journal lost the original UID/resourceVersion", err)
			}
			for dir := filepath.Dir(wanted); dir != filepath.Dir(store.ConfigRoot); dir = filepath.Dir(dir) {
				if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
					t.Fatal("journal directory is not private", dir, err)
				}
			}
			other := provisionPaths{EnvRoot: t.TempDir(), ArtifactsDir: filepath.Join(t.TempDir(), "unused")}
			if err := certIssuerCheckPendingIngressMigration(other, values, ""); err == nil || !strings.Contains(err.Error(), "unfinished") {
				t.Fatal("switching checkouts bypassed unfinished migration", err)
			}
			if err := certIssuerCheckPendingIngressMigration(other, values, p.ID); err != nil {
				t.Fatal("current operation cannot inspect its own record", err)
			}
			p.Status = "complete"
			if err := p.save(); err != nil {
				t.Fatal(err)
			}
			if err := certIssuerCheckPendingIngressMigration(other, values, ""); err != nil {
				t.Fatal(err)
			}
			for _, forbidden := range []string{paths.ArtifactsDir, filepath.Join(paths.EnvRoot, "artifacts")} {
				if _, err := os.Lstat(forbidden); !os.IsNotExist(err) {
					t.Fatal("journal writer created checkout artifacts", forbidden)
				}
			}
			p.RestorePath = filepath.Join(t.TempDir(), "redirect.json")
			if err := p.save(); err == nil {
				t.Fatal("restore path redirect accepted")
			}
		})
	}
}

func TestCertIssuerJournalRejectsUnsafeRecordsAndLegacyFallback(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"missing-environment", "unsafe-parent", "symlink-record", "public-record", "invalid-id", "invalid-json", "unfinished", "legacy-checkout", "legacy-override"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				store := makeIsolatedTestSecretStore(t, environment)
				values := map[string]string{"CLOUD_ENV_NAME": environment}
				paths := provisionPaths{EnvRoot: t.TempDir(), ArtifactsDir: t.TempDir()}
				relative := filepath.Join(certIssuerIngressJournalRelativeDir, "certissuer-456.json")
				raw := []byte(`{"id":"certissuer-456","status":"complete"}`)
				switch mode {
				case "missing-environment":
					delete(values, "CLOUD_ENV_NAME")
				case "unsafe-parent":
					if err := os.Symlink(t.TempDir(), filepath.Join(store.Root, "deployment")); err != nil {
						t.Fatal(err)
					}
				case "legacy-checkout", "legacy-override":
					dir := filepath.Join(paths.EnvRoot, "artifacts", "certissuer-ingress")
					if mode == "legacy-override" {
						dir = filepath.Join(paths.ArtifactsDir, "certissuer-ingress")
					}
					if err := os.MkdirAll(dir, 0700); err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(dir, "certissuer-456.json")
					if err := os.WriteFile(path, raw, 0600); err != nil {
						t.Fatal(err)
					}
					if err := certIssuerCheckPendingIngressMigration(paths, values, ""); err == nil || !strings.Contains(err.Error(), "explicit migration") {
						t.Fatal("legacy journal ignored", err)
					}
					if string(raw) != readTestFile(t, path) {
						t.Fatal("legacy original changed")
					}
					return
				default:
					if mode == "invalid-id" {
						raw = []byte(`{"id":"certissuer-another","status":"complete"}`)
					}
					if mode == "invalid-json" {
						raw = []byte(`{`)
					}
					if mode == "unfinished" {
						raw = []byte(`{"id":"certissuer-456","status":"unknown-outcome"}`)
					}
					if err := writePrivateDeploymentState(environment, relative, raw); err != nil {
						t.Fatal(err)
					}
					path := filepath.Join(store.Root, relative)
					if mode == "public-record" {
						if err := os.Chmod(path, 0644); err != nil {
							t.Fatal(err)
						}
					}
					if mode == "symlink-record" {
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
						if err := os.Symlink(filepath.Join(t.TempDir(), "missing"), path); err != nil {
							t.Fatal(err)
						}
					}
				}
				if err := certIssuerCheckPendingIngressMigration(paths, values, ""); err == nil {
					t.Fatal("unsafe or unfinished journal accepted")
				}
			})
		}
	}
}
