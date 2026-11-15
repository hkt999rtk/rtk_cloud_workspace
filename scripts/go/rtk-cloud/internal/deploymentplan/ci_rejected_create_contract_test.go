package deploymentplan

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type ciRejectedJobBackend struct {
	*parentExecutionFixture
	rejectID string
	rejects  int
}

func (b *ciRejectedJobBackend) Write(ctx context.Context, action string, body Object, dry bool) (Object, error) {
	if !dry && ID(body) == b.rejectID {
		b.rejects++
		return nil, errors.New("fixture refused request before API send")
	}
	return b.parentExecutionFixture.Write(ctx, action, body, dry)
}

type ciRejectedJobFixture struct {
	plan            *Plan
	backend         *ciRejectedJobBackend
	root, directory string
	jobID           string
	source          []byte
}

func newCIRejectedJobFixture(t *testing.T) ciRejectedJobFixture {
	t.Helper()
	in, definition, _ := fixture(t, "dev", "Job")
	in.Operation = "scoped"
	definition.Object["apiVersion"] = "batch/v1"
	definition.Object["spec"] = Object{"template": Object{"spec": Object{"restartPolicy": "Never", "containers": []any{Object{"name": "migration", "image": "example.invalid/migration@sha256:" + strings.Repeat("a", 64)}}}}}
	config := Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": Object{"name": "migration-input", "namespace": in.Stack + "-video-cloud"}, "data": Object{"schema": "original"}}
	definition.Dependencies = []string{ID(config)}
	p, err := Compile(in, []Definition{{Object: config, Phase: "configuration"}, definition}, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	active := filepath.Join(root, "deployment", ".execution-active")
	if err := os.MkdirAll(active, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(active, "parent"), []byte("original-parent"), 0600); err != nil {
		t.Fatal(err)
	}
	b := &ciRejectedJobBackend{parentExecutionFixture: &parentExecutionFixture{memoryBackend: memoryBackend{objects: map[string]Object{}}, root: root}, rejectID: ID(definition.Object)}
	check := func(context.Context, *Plan) error { return nil }
	if err := p.ExecuteResumable(t.Context(), b, p.SHA256(), root, check, check, check); err == nil || b.rejects != 1 {
		t.Fatal("fixture did not reach the rejected Job request", err)
	}
	j := readExecutionJournal(t, root, p.SHA256())
	if j.InFlight != b.rejectID || j.Failed != b.rejectID || len(j.Completed) != 1 || len(b.written) != 1 || b.objects[b.rejectID] != nil || b.leaseHeld {
		t.Fatal("fixture did not preserve the exact pre-send failure and completed dependency", j)
	}
	directory := filepath.Join(root, "deployment", "rollback", p.SHA256())
	source, err := os.ReadFile(filepath.Join(directory, "journal.json"))
	if err != nil {
		t.Fatal(err)
	}
	return ciRejectedJobFixture{p, b, root, directory, b.rejectID, source}
}

func TestCIPlanRejectedJobResumesOnlyAfterIndependentPreSendProof(t *testing.T) {
	f := newCIRejectedJobFixture(t)
	evidence := Object{"target": f.jobID, "outcome": "pre-send-rejection"}
	verified := false
	verify := func(ctx context.Context) error {
		verified = true
		if err := f.backend.CheckParentExecution(ctx, f.plan, f.root); err != nil {
			return err
		}
		lock, err := LockExecutionOwner(filepath.Join(f.directory, ".active"), false)
		if err == nil {
			lock.Close()
			t.Fatal("independent verification ran without the original child lock")
		}
		current, err := f.backend.Read(ctx, f.jobID)
		if err != nil || current != nil {
			t.Fatal("rejected Job was not independently observed as unstarted", err)
		}
		return nil
	}
	sha := rejectedCreateDigest(f.source)
	if err := f.plan.ReconcileRejectedCreate(t.Context(), f.backend, f.root, sha, f.jobID, evidence, verify); err != nil {
		t.Fatal(err)
	}
	if !verified || len(f.backend.written) != 1 || f.backend.objects[f.jobID] != nil {
		t.Fatal("local reconciliation replayed a native write or skipped independent verification")
	}
	archive := filepath.Join(f.directory, "rejected-creates", sha)
	archived, err := os.ReadFile(filepath.Join(archive, "journal.json"))
	if err != nil || !bytes.Equal(archived, f.source) {
		t.Fatal("reconciliation replaced original failure bytes", err)
	}
	j := readExecutionJournal(t, f.root, f.plan.SHA256())
	if j.InFlight != "" || j.Failed != f.jobID || len(j.RejectedCreates) != 1 || j.RejectedCreates[0].EvidenceSHA256 != Digest(evidence) || validateRejectedCreates(f.directory, f.plan, j) != nil {
		t.Fatal("durable pre-send proof cannot be independently re-read", j)
	}
	for _, path := range []string{filepath.Join(archive, "journal.json"), filepath.Join(archive, "evidence.json")} {
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
			t.Fatal("reconciliation evidence is not private", err)
		}
	}
	f.backend.rejectID = ""
	check := func(context.Context, *Plan) error { return nil }
	if err := f.plan.ExecuteResumable(t.Context(), f.backend, f.plan.SHA256(), f.root, check, check, check); err != nil {
		t.Fatal("qualified rejected write did not resume", err)
	}
	final := readExecutionJournal(t, f.root, f.plan.SHA256())
	if !final.Accepted || len(final.Completed) != 2 || len(f.backend.written) != 2 || final.Owners[final.Completed[0]] != j.Owners[j.Completed[0]] || len(final.RejectedCreates) != 1 || f.backend.leaseHeld {
		t.Fatal("resume rewrote an accepted dependency or lost original rejection evidence", final)
	}
}

func TestCIPlanRejectedJobRefusesChangedAuthorityWithoutNativeWrites(t *testing.T) {
	for _, mode := range []string{"source-digest", "completed-owner", "busy-child", "verify-denied", "parent-lost", "journal-race", "plan-race", "archive-conflict"} {
		t.Run(mode, func(t *testing.T) {
			f := newCIRejectedJobFixture(t)
			sha := rejectedCreateDigest(f.source)
			want := append([]byte(nil), f.source...)
			verify := func(context.Context) error { return nil }
			wantID := "deployment.recovery.rejected-create"
			switch mode {
			case "source-digest":
				sha = strings.Repeat("f", 64)
			case "completed-owner":
				j := readExecutionJournal(t, f.root, f.plan.SHA256())
				Map(f.backend.objects[j.Completed[0]]["metadata"])["uid"] = "replacement"
				wantID = "deployment.drift"
			case "busy-child":
				if err := os.Mkdir(filepath.Join(f.directory, ".active"), 0700); err != nil {
					t.Fatal(err)
				}
			case "verify-denied":
				verify = func(context.Context) error { return os.ErrPermission }
			case "parent-lost":
				verify = func(context.Context) error { f.backend.checkErr = os.ErrPermission; return nil }
			case "journal-race":
				want = append(want, '\n')
				verify = func(context.Context) error {
					return os.WriteFile(filepath.Join(f.directory, "journal.json"), want, 0600)
				}
			case "plan-race":
				verify = func(context.Context) error { f.plan.Resources[0].Phase = "workload"; return nil }
			case "archive-conflict":
				archive := filepath.Join(f.directory, "rejected-creates", sha)
				if err := os.MkdirAll(archive, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(archive, "evidence.json"), []byte(`{"review":"different"}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := f.plan.ReconcileRejectedCreate(t.Context(), f.backend, f.root, sha, f.jobID, Object{"outcome": "pre-send-rejection"}, verify)
			if mode == "verify-denied" || mode == "parent-lost" {
				if !errors.Is(err, os.ErrPermission) {
					t.Fatal("independent refusal was not preserved", err)
				}
			} else if mode == "archive-conflict" {
				if err == nil || !strings.Contains(err.Error(), "do not overwrite recovery evidence") {
					t.Fatal("existing reviewed proof was overwritten", err)
				}
			} else {
				var failure *Blocker
				if !errors.As(err, &failure) || failure.ID != wantID {
					t.Fatal("changed evidence did not fail at its original fence", err)
				}
			}
			after, readErr := os.ReadFile(filepath.Join(f.directory, "journal.json"))
			if readErr != nil || !bytes.Equal(after, want) || len(f.backend.written) != 1 || f.backend.objects[f.jobID] != nil || f.backend.leaseHeld {
				t.Fatal("refused reconciliation modified journal or native state", readErr)
			}
			if mode == "archive-conflict" {
				raw, err := os.ReadFile(filepath.Join(f.directory, "rejected-creates", sha, "evidence.json"))
				if err != nil || string(raw) != `{"review":"different"}` {
					t.Fatal("original reviewed evidence changed", err)
				}
			}
		})
	}
}

func TestCIPlanRejectedJobArchiveCannotBeSubstituted(t *testing.T) {
	for _, mode := range []string{"source", "evidence", "directory-permission", "receipt-digest"} {
		t.Run(mode, func(t *testing.T) {
			f := newCIRejectedJobFixture(t)
			sha := rejectedCreateDigest(f.source)
			if err := f.plan.ReconcileRejectedCreate(t.Context(), f.backend, f.root, sha, f.jobID, Object{"target": f.jobID}, func(context.Context) error { return nil }); err != nil {
				t.Fatal(err)
			}
			j := readExecutionJournal(t, f.root, f.plan.SHA256())
			archive := filepath.Join(f.directory, "rejected-creates", sha)
			switch mode {
			case "source":
				if err := os.WriteFile(filepath.Join(archive, "journal.json"), append(f.source, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			case "evidence":
				if err := os.WriteFile(filepath.Join(archive, "evidence.json"), []byte(`{"target":"replacement"}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "directory-permission":
				if err := os.Chmod(archive, 0755); err != nil {
					t.Fatal(err)
				}
			case "receipt-digest":
				j.RejectedCreates[0].EvidenceSHA256 = "untrusted"
			}
			if err := validateRejectedCreates(f.directory, f.plan, j); err == nil {
				t.Fatal("substituted or public rejection archive was trusted")
			}
			if len(f.backend.written) != 1 || f.backend.objects[f.jobID] != nil {
				t.Fatal("archive verification replayed a native write")
			}
		})
	}
}
