package deploymentplan

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestFailedFullyWrittenForwardPreservesOriginalJournal(t *testing.T) {
	for _, mode := range []string{"ok", "foreign-parent", "changed-journal", "changed-owner", "changed-desired", "wrong-failed", "wrong-plan", "tampered-during-acceptance", "failed-acceptance", "unready", "failed-persist", "lease-lost-during-persist", "parent-lost-during-persist"} {
		t.Run(mode, func(t *testing.T) {
			f := phaseObservationProfile(t, "dev", false)
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
			dir := filepath.Join(f.root, "deployment", "rollback", f.plan.SHA256())
			j := readExecutionJournal(t, f.root, f.plan.SHA256())
			j.Accepted = false
			j.AcceptanceStarted = false
			j.Failed = f.plan.Resources[0].ID
			if err := saveJournal(dir, j); err != nil {
				t.Fatal(err)
			}
			before, err := os.ReadFile(filepath.Join(dir, "journal.json"))
			if err != nil {
				t.Fatal(err)
			}
			digest := failedForwardBytesDigest(before)
			switch mode {
			case "unready":
				b.failWait = true
			case "foreign-parent":
				b.checkErr = os.ErrPermission
			case "changed-journal":
				digest = failedForwardBytesDigest([]byte("different"))
			case "wrong-failed":
				j.Failed = "foreign"
				if err := saveJournal(dir, j); err != nil {
					t.Fatal(err)
				}
				before, _ = os.ReadFile(filepath.Join(dir, "journal.json"))
				digest = failedForwardBytesDigest(before)
			case "changed-owner":
				r := f.plan.Resources[0]
				Map(b.objects[r.ID]["metadata"])["uid"] = "different-owner"
			case "changed-desired":
				r := f.plan.Resources[0]
				Map(b.objects[r.ID]["metadata"])["name"] = "different-name"
			}
			expectedPlan := f.plan.SHA256()
			if mode == "wrong-plan" {
				expectedPlan = failedForwardBytesDigest([]byte("another plan"))
			}
			n := len(b.written)
			accept := func(context.Context, *Plan) error {
				if mode == "tampered-during-acceptance" {
					j.Failed = "tampered"
					if err := saveJournal(dir, j); err != nil {
						return err
					}
				}
				if mode == "failed-acceptance" {
					return os.ErrPermission
				}
				return nil
			}
			persisted := false
			persist := func(ctx context.Context, evidence FailedForwardEvidence, lease ExecutionLease) error {
				if !b.leaseHeld || lease.Check(ctx) != nil || b.CheckParentExecution(ctx, f.plan, f.root) != nil {
					t.Fatal("receipt written without live parent and lease")
				}
				if mode == "failed-persist" {
					return os.ErrPermission
				}
				persisted = true
				if mode == "lease-lost-during-persist" {
					b.leaseHeld = false
				}
				if mode == "parent-lost-during-persist" {
					b.checkErr = os.ErrPermission
				}
				return nil
			}
			_, err = f.plan.VerifyFullyWrittenFailedForward(t.Context(), b, f.root, expectedPlan, digest, f.plan.Resources[0].ID, accept, persist)
			if persisted != (mode == "ok" || mode == "lease-lost-during-persist" || mode == "parent-lost-during-persist") {
				t.Fatal("receipt persisted for unverified candidate")
			}
			if (err == nil) != (mode == "ok") {
				t.Fatalf("mode %s: %v", mode, err)
			}
			if mode == "tampered-during-acceptance" {
				if err := os.WriteFile(filepath.Join(dir, "journal.json"), before, 0600); err != nil {
					t.Fatal(err)
				}
			}
			after, err := os.ReadFile(filepath.Join(dir, "journal.json"))
			if err != nil || string(before) != string(after) {
				t.Fatal("original failed journal changed", err)
			}
			if len(b.written) != n || b.leaseHeld {
				t.Fatal("wrote resources or leaked lease")
			}
		})
	}
}

func TestQualifyFullyWrittenFailedForwardDoesNotRequireReady(t *testing.T) {
	f := phaseObservationProfile(t, "dev", false)
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
	dir := filepath.Join(f.root, "deployment", "rollback", f.plan.SHA256())
	j := readExecutionJournal(t, f.root, f.plan.SHA256())
	j.Accepted = false
	j.AcceptanceStarted = false
	j.Failed = f.plan.Resources[0].ID
	if err := saveJournal(dir, j); err != nil {
		t.Fatal(err)
	}
	b.failWait = true // A readiness failure is the exact reason recovery is needed.
	proof, err := f.plan.QualifyFullyWrittenFailedForward(t.Context(), b, f.root, f.plan.SHA256(), j.Failed)
	if err != nil || proof.PlanSHA256 != f.plan.SHA256() || proof.FailedResource != j.Failed || proof.JournalSHA256 == "" {
		t.Fatal(proof, err)
	}
	r := f.plan.Resources[0]
	Map(b.objects[r.ID]["metadata"])["uid"] = "other"
	if _, err := f.plan.QualifyFullyWrittenFailedForward(t.Context(), b, f.root, f.plan.SHA256(), j.Failed); err == nil {
		t.Fatal("qualified a replaced owner")
	}
}

func TestFailedForwardResumableQualificationDoesNotWaitForUnreadyRetainedOwner(t *testing.T) {
	f := phaseObservationProfile(t, "dev", false)
	active := filepath.Join(f.root, "deployment", ".execution-active")
	if err := os.MkdirAll(active, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(active, "parent"), []byte("original-parent"), 0600); err != nil {
		t.Fatal(err)
	}
	b := &parentExecutionFixture{memoryBackend: f.backend.memoryBackend, root: f.root}
	body := Clone(f.plan.Resources[0].Desired)
	Map(body["metadata"])["uid"] = "original-retained-owner"
	b.objects[ID(body)] = Clone(body)
	p, err := Compile(f.plan.Inputs, []Definition{{Object: body, Phase: f.plan.Resources[0].Phase, Retain: true}}, []Object{body})
	if err != nil {
		t.Fatal(err)
	}
	check := func(context.Context, *Plan) error { return nil }
	if err = p.ExecuteResumable(t.Context(), b, p.SHA256(), f.root, check, check, check); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(f.root, "deployment", "rollback", p.SHA256())
	j := readExecutionJournal(t, f.root, p.SHA256())
	j.Accepted, j.AcceptanceStarted, j.Failed = false, false, p.Resources[0].ID
	if err = saveJournal(dir, j); err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(filepath.Join(dir, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	b.failWait = true
	if err = p.AdmitResumable(t.Context(), b, f.root); err == nil {
		t.Fatal("fixture did not simulate an unready workload")
	}
	if err = p.AdmitFailedForwardResumable(t.Context(), b, f.root); err != nil {
		t.Fatal("preflight waited for unready retained owner", err)
	}
	Map(b.objects[p.Resources[0].ID]["metadata"])["uid"] = "replacement-owner"
	if err = p.AdmitFailedForwardResumable(t.Context(), b, f.root); err == nil {
		t.Fatal("preflight adopted replacement owner")
	}
	after, err := os.ReadFile(filepath.Join(dir, "journal.json"))
	if err != nil || string(before) != string(after) {
		t.Fatal("qualification changed original journal", err)
	}
}
