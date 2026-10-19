package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestProductSessionRenewalCleanupOptionalForOriginalProtocol(t *testing.T) {
	for _, evidence := range []string{"absent", "directory", "source", "pending-policy", "pending-child", "retirement"} {
		t.Run(evidence, func(t *testing.T) {
			f := newProductProtocolFixture(t, false)
			if _, err := os.Lstat(createNormalTailProgramPath(f.e)); !os.IsNotExist(err) {
				t.Fatal("ordinary product fixture unexpectedly has a remaining program", err)
			}
			dir := filepath.Join(environmentCreateCeremonyDirectory(f.runtime, "service"), "session-renewal")
			ceremony := environmentCreateServiceCeremony{BootstrapSessionID: "12345678-1234-4234-8234-123456789abc"}
			switch evidence {
			case "directory":
				path, err := f.store.safePath(dir)
				if err != nil || os.MkdirAll(path, 0700) != nil {
					t.Fatal("isolated renewal directory unavailable", err)
				}
			case "source":
				if err := f.store.write(filepath.Join(dir, "source.json"), []byte(`{}`), false); err != nil {
					t.Fatal(err)
				}
			case "pending-child":
				f.e.Journal.Entries = append(f.e.Journal.Entries, environmentCreateJournalEntry{Key: "kubernetes/product-identities-" + f.runtime.ProductIdentityScope[:12] + "-session-renewal-123456789abc-job", State: "pending"})
			case "pending-policy":
				sid, _, _ := productSessionRenewalIdentity(f.runtime.ExecutionSHA256, f.runtime.ProductIdentityScope, ceremony.BootstrapSessionID)
				f.e.Journal.Entries = append(f.e.Journal.Entries, environmentCreateJournalEntry{Key: "openbao-policy-rtk-" + f.e.Journal.Environment + "-service-bootstrap-" + sid, State: "pending"})
			case "retirement":
				f.e.Journal.Entries = append(f.e.Journal.Entries, environmentCreateJournalEntry{Key: "product-session-renewal-retire-12345678-1234-4234-8234-123456789abc-secret", State: "pending"})
			}
			l, renewed, err := productSessionRenewalForRuntime(f.store, f.runtime, ceremony)
			if evidence != "absent" {
				if err == nil {
					t.Fatal("renewal evidence without its authenticated program was ignored")
				}
				return
			}
			if err != nil || l != nil || renewed != nil {
				t.Fatal("ordinary product session requires no renewal authority", err)
			}
			if err := prepareProductSessionRenewalCleanup(t.Context(), f.cfg, f.store, f.runtime, environmentCreateServiceCeremony{}, environmentCreateBootstrapFinalization{}); err != nil {
				t.Fatal("ordinary pre-cleanup acquired renewal dependency", err)
			}
			if err := retireProductSessionRenewalOriginal(t.Context(), f.cfg, f.store, f.runtime, environmentCreateServiceCeremony{}, environmentCreateBootstrapFinalization{}); err != nil {
				t.Fatal("ordinary finalization acquired renewal dependency", err)
			}
		})
	}
}

func TestProductSessionRenewalCleanupRejectsIncompleteStateWithOriginalProgram(t *testing.T) {
	for _, evidence := range []string{"directory", "source", "malformed-completion"} {
		t.Run(evidence, func(t *testing.T) {
			_, e, artifact := normalTailProgramFixture(t)
			if err := saveCreateNormalTailProgramArtifact(e.Store, e, artifact); err != nil {
				t.Fatal(err)
			}
			runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, ProductIdentityScope: productIdentityScope(artifact.Program.Products)}
			dir := filepath.Join(environmentCreateCeremonyDirectory(runtime, "service"), "session-renewal")
			path, err := e.Store.safePath(dir)
			if err != nil || os.MkdirAll(path, 0700) != nil {
				t.Fatal("isolated renewal directory unavailable", err)
			}
			if evidence != "directory" {
				name, raw := "source.json", []byte(`{}`)
				if evidence == "malformed-completion" {
					name, raw = "completed.json", []byte(`{"unrecognized":true}`)
				}
				if err := e.Store.write(filepath.Join(dir, name), raw, false); err != nil {
					t.Fatal(err)
				}
			}
			if _, _, err := productSessionRenewalForRuntime(e.Store, runtime, environmentCreateServiceCeremony{}); err == nil {
				t.Fatal("incomplete renewal state was treated as an ordinary session")
			}
		})
	}
}
