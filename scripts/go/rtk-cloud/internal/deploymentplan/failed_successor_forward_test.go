package deploymentplan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFailedSuccessorChecksOriginalLeaseAndAcceptedRetainedOwners(t *testing.T) {
	for _, mode := range []string{"valid", "unaccepted", "foreign-owner", "foreign-desired", "original-changed", "successor-changed", "lease-lost", "parent-lost", "unready", "persist-fails"} {
		t.Run(mode, func(t *testing.T) {
			f := phaseObservationProfile(t, "dev", false)
			f.plan.Operation = "scoped"
			active := filepath.Join(f.root, "deployment", ".execution-active")
			if err := os.MkdirAll(active, 0700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(active, "parent"), []byte("original-parent"), 0600); err != nil {
				t.Fatal(err)
			}
			b := &parentExecutionFixture{memoryBackend: f.backend.memoryBackend, root: f.root}
			check := func(context.Context, *Plan) error { return nil }
			if err := f.plan.ExecuteResumable(t.Context(), b, f.plan.SHA256(), f.root, check, check, check); err != nil {
				t.Fatal(err)
			}
			sourceDir := filepath.Join(f.root, "deployment", "rollback", f.plan.SHA256())
			source := readExecutionJournal(t, f.root, f.plan.SHA256())
			source.Accepted = false
			source.AcceptanceStarted = false
			source.Failed = f.plan.Resources[0].ID
			if err := saveJournal(sourceDir, source); err != nil {
				t.Fatal(err)
			}
			sourceRaw, err := os.ReadFile(filepath.Join(sourceDir, "journal.json"))
			if err != nil {
				t.Fatal(err)
			}
			changed := f.plan.Resources[0].ID
			Map(b.objects[changed]["data"])["value"] = "successor"
			defs := []Definition{}
			live := []Object{}
			for _, r := range f.plan.Resources {
				d := Clone(r.Desired)
				if r.ID == changed {
					Map(d["data"])["value"] = "successor"
				}
				Map(d["metadata"])["uid"] = source.Owners[r.ID]
				defs = append(defs, Definition{Object: d, Phase: r.Phase, Retain: true, Dependencies: r.Dependencies})
				live = append(live, Clone(b.objects[r.ID]))
			}
			in := f.plan.Inputs
			in.BindingsSHA256 = Digest("successor")
			successor, err := Compile(in, defs, live)
			if err != nil {
				t.Fatal(err)
			}
			if err = successor.ExecuteResumable(t.Context(), b, successor.SHA256(), f.root, check, check, check); err != nil {
				t.Fatal(err)
			}
			succDir := filepath.Join(f.root, "deployment", "rollback", successor.SHA256())
			if mode == "unaccepted" {
				j := readExecutionJournal(t, f.root, successor.SHA256())
				j.Accepted = false
				j.AcceptanceStarted = false
				j.Failed = j.Completed[0]
				if err = saveJournal(succDir, j); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "foreign-owner" {
				Map(b.objects[f.plan.Resources[1].ID]["metadata"])["uid"] = "different"
			}
			if mode == "foreign-desired" {
				Map(b.objects[changed]["data"])["value"] = "drift"
			}
			if mode == "unready" {
				b.failWait = true
			}
			writes := len(b.written)
			persisted := false
			accept := func(_ context.Context, got *Plan) error {
				if got != successor {
					t.Fatal("accept did not receive successor")
				}
				switch mode {
				case "original-changed":
					j := source
					j.Failed = "changed"
					return saveJournal(sourceDir, j)
				case "successor-changed":
					j := readExecutionJournal(t, f.root, successor.SHA256())
					j.Accepted = false
					j.AcceptanceStarted = false
					j.Failed = j.Completed[0]
					return saveJournal(succDir, j)
				case "lease-lost":
					b.leaseHeld = false
				case "parent-lost":
					b.checkErr = os.ErrPermission
				}
				return nil
			}
			persist := func(ctx context.Context, _ FailedForwardEvidence, l ExecutionLease) error {
				if !b.leaseHeld || l.Check(ctx) != nil {
					t.Fatal("persist without lease")
				}
				if mode == "persist-fails" {
					return os.ErrPermission
				}
				persisted = true
				return nil
			}
			_, err = f.plan.VerifyFullyWrittenFailedSuccessor(t.Context(), b, f.root, f.plan.SHA256(), failedForwardBytesDigest(sourceRaw), source.Failed, successor, accept, persist)
			if (err == nil) != (mode == "valid") {
				t.Fatalf("%s: %v", mode, err)
			}
			if persisted != (mode == "valid") {
				t.Fatal("unverified alias persisted", mode)
			}
			if len(b.written) != writes || b.leaseHeld {
				t.Fatal("unexpected resource write or retained lease")
			}
			if mode != "original-changed" {
				after, err := os.ReadFile(filepath.Join(sourceDir, "journal.json"))
				if err != nil || string(after) != string(sourceRaw) {
					t.Fatal("original journal changed")
				}
			}
		})
	}
}
