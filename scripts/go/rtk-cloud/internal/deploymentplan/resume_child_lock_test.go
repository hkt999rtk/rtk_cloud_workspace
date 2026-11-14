package deploymentplan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFullyWrittenChildRecoversOnlyItsInterruptedEmptyLock(t *testing.T) {
	for _, mode := range []string{"recover", "partial", "failed", "in-flight", "nonempty", "foreign-parent"} {
		t.Run(mode, func(t *testing.T) {
			profile := phaseObservationProfile(t, "dev", false)
			active := filepath.Join(profile.root, "deployment", ".execution-active")
			if err := os.MkdirAll(active, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(active, "parent"), []byte("original-parent"), 0600); err != nil {
				t.Fatal(err)
			}
			b := &parentExecutionFixture{memoryBackend: profile.backend.memoryBackend, root: profile.root}
			check := func(context.Context, *Plan) error { return nil }
			if err := profile.plan.ExecuteResumable(t.Context(), b, profile.plan.SHA256(), profile.root, check, check, check); err != nil {
				t.Fatal(err)
			}
			dir := filepath.Join(profile.root, "deployment", "rollback", profile.plan.SHA256())
			journal := readExecutionJournal(t, profile.root, profile.plan.SHA256())
			journal.Accepted, journal.AcceptanceStarted = false, false
			switch mode {
			case "partial":
				journal.Completed = journal.Completed[:len(journal.Completed)-1]
				delete(journal.Owners, profile.plan.Resources[len(profile.plan.Resources)-1].ID)
			case "failed":
				journal.Failed = profile.plan.Resources[0].ID
			case "in-flight":
				journal.InFlight = profile.plan.Resources[0].ID
			}
			if err := saveJournal(dir, journal); err != nil {
				t.Fatal(err)
			}
			lock := filepath.Join(dir, ".active")
			if err := os.Mkdir(lock, 0700); err != nil {
				t.Fatal(err)
			}
			if mode == "nonempty" {
				if err := os.WriteFile(filepath.Join(lock, "owner"), []byte("unknown"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "foreign-parent" {
				if err := os.WriteFile(filepath.Join(active, "parent"), []byte("foreign"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			writes := len(b.written)
			err := profile.plan.ExecuteResumable(t.Context(), b, profile.plan.SHA256(), profile.root, check, check, check)
			if (err == nil) != (mode == "recover") {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if len(b.written) != writes {
				t.Fatal("interrupted writes were replayed")
			}
			if mode == "recover" {
				matches, err := filepath.Glob(filepath.Join(dir, ".active-recovered-*"))
				if err != nil || len(matches) != 1 || !readExecutionJournal(t, profile.root, profile.plan.SHA256()).Accepted {
					t.Fatal("original empty lock was not archived or child not accepted", err)
				}
			} else if _, err := os.Lstat(lock); err != nil {
				t.Fatal("unverified lock was removed", err)
			}
		})
	}
}
