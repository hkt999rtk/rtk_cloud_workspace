package deploymentplan

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type knownRetireResponseBackend struct {
	*memoryBackend
	firstID, readErrorID                                                    string
	stopWait, terminating, keepAfterWait, replaceAfterWait, unknownResponse bool
	waits                                                                   int
}

func (b *knownRetireResponseBackend) Read(ctx context.Context, id string) (Object, error) {
	if id == b.readErrorID {
		return nil, errors.New("native read unavailable")
	}
	return b.memoryBackend.Read(ctx, id)
}

func (b *knownRetireResponseBackend) Write(ctx context.Context, action string, body Object, dry bool) (Object, error) {
	if dry || action != "retire" || ID(body) != b.firstID {
		return b.memoryBackend.Write(ctx, action, body, dry)
	}
	b.written = append(b.written, Clone(body))
	if b.terminating {
		meta := Map(b.objects[b.firstID]["metadata"])
		meta["deletionTimestamp"] = time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)
		meta["deletionGracePeriodSeconds"] = float64(30)
		meta["finalizers"] = []any{"foregroundDeletion"}
	} else {
		delete(b.objects, b.firstID)
	}
	if b.unknownResponse {
		return nil, errors.New("delete response lost")
	}
	return Clone(body), nil
}

func (b *knownRetireResponseBackend) Wait(ctx context.Context, r Resource) error {
	if r.ID == b.firstID {
		b.waits++
		if b.stopWait {
			b.stopWait = false
			return errors.New("interrupted after successful delete response")
		}
		if b.replaceAfterWait {
			Map(b.objects[r.ID]["metadata"])["uid"] = "concurrent-replacement"
		} else if !b.keepAfterWait {
			delete(b.objects, r.ID)
		}
	}
	return b.memoryBackend.Wait(ctx, r)
}

func knownRetireResponseFixture(t *testing.T, terminating bool) (*Plan, *knownRetireResponseBackend, string) {
	t.Helper()
	in, first, live := fixture(t, "dev", "Deployment")
	in.Operation = "scoped"
	first.Retire, first.Phase = true, "retirement"
	Map(first.Object["metadata"])["uid"] = "owner"
	second := first
	second.Object = Clone(first.Object)
	Map(second.Object["metadata"])["name"] = "second"
	Map(second.Object["metadata"])["uid"] = "second-owner"
	secondLive := Clone(live)
	Map(secondLive["metadata"])["name"] = "second"
	Map(secondLive["metadata"])["uid"] = "second-owner"
	p, err := Compile(in, []Definition{first, second}, []Object{live, secondLive})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	b := &knownRetireResponseBackend{memoryBackend: &memoryBackend{objects: map[string]Object{ID(live): live, ID(secondLive): secondLive}}, firstID: p.Resources[0].ID, stopWait: true, terminating: terminating}
	return p, b, root
}

func TestKnownRetirementResponseResumesWithoutAnotherDelete(t *testing.T) {
	for _, terminating := range []bool{false, true} {
		t.Run(map[bool]string{false: "already-absent", true: "same-owner-terminating"}[terminating], func(t *testing.T) {
			p, b, root := knownRetireResponseFixture(t, terminating)
			check := func(context.Context, *Plan) error { return nil }
			planBytes := knownRetireJSON(t, p)
			if err := p.ExecuteResumable(t.Context(), b, p.SHA256(), root, check, check, check); err == nil {
				t.Fatal("interruption was not reached")
			}
			j, exists, err := p.ReadExecution(root)
			if err != nil || !exists || len(j.Completed) != 0 || j.InFlight != "" || j.Owners[b.firstID] != p.Resources[0].UID || len(b.written) != 1 {
				t.Fatal("successful native response was not preserved", j, exists, err)
			}
			if err := p.ExecuteResumable(t.Context(), b, p.SHA256(), root, check, check, check); err != nil {
				t.Fatal("known retirement could not resume", err)
			}
			j, exists, err = p.ReadExecution(root)
			if err != nil || !exists || !j.Accepted || len(j.Completed) != 2 || len(b.written) != 2 || string(planBytes) != string(knownRetireJSON(t, p)) {
				t.Fatal("resume rewrote a delete/plan or lost genuine completion", j, exists, err)
			}
			raw, err := os.ReadFile(filepath.Join(root, "deployment/rollback", p.SHA256(), "journal.json"))
			if err != nil {
				t.Fatal(err)
			}
			if err := p.ExecuteResumable(t.Context(), b, p.SHA256(), root, check, check, check); err != nil {
				t.Fatal("accepted retirement could not be reread", err)
			}
			after, _ := os.ReadFile(filepath.Join(root, "deployment/rollback", p.SHA256(), "journal.json"))
			if len(b.written) != 2 || string(raw) != string(after) {
				t.Fatal("accepted retirement was rewritten")
			}
		})
	}
}

func TestKnownRetirementResponseRefusesForeignOrUncertainOutcomes(t *testing.T) {
	for _, mode := range []string{"unknown-response", "in-flight", "foreign-recorded-owner", "replacement", "not-terminating", "bad-deletion-time", "changed-body", "changed-user-metadata", "changed-finalizer", "read-unavailable", "wait-incomplete", "replacement-during-wait", "completed-owner-present"} {
		t.Run(mode, func(t *testing.T) {
			p, b, root := knownRetireResponseFixture(t, true)
			check := func(context.Context, *Plan) error { return nil }
			b.unknownResponse = mode == "unknown-response"
			if err := p.ExecuteResumable(t.Context(), b, p.SHA256(), root, check, check, check); err == nil {
				t.Fatal("fixture did not stop")
			}
			if mode != "unknown-response" {
				j, _, err := p.ReadExecution(root)
				if err != nil {
					t.Fatal(err)
				}
				meta := Map(b.objects[b.firstID]["metadata"])
				switch mode {
				case "in-flight":
					j.InFlight = b.firstID
				case "foreign-recorded-owner":
					j.Owners[b.firstID] = "foreign"
				case "replacement":
					meta["uid"] = "foreign"
				case "not-terminating":
					delete(meta, "deletionTimestamp")
				case "bad-deletion-time":
					meta["deletionTimestamp"] = "not-a-time"
				case "changed-body":
					Map(b.objects[b.firstID]["spec"])["replicas"] = float64(7)
				case "changed-user-metadata":
					meta["annotations"] = Object{"user.example/changed": "yes"}
				case "changed-finalizer":
					meta["finalizers"] = []any{"foregroundDeletion", "user.example/new"}
				case "read-unavailable":
					b.readErrorID = b.firstID
				case "wait-incomplete":
					b.keepAfterWait = true
				case "replacement-during-wait":
					b.replaceAfterWait = true
				case "completed-owner-present":
					j.Completed = []string{b.firstID}
				}
				if err := saveJournal(filepath.Join(root, "deployment/rollback", p.SHA256()), j); err != nil {
					t.Fatal(err)
				}
			}
			before := len(b.written)
			if err := p.ExecuteResumable(t.Context(), b, p.SHA256(), root, check, check, check); err == nil {
				t.Fatal("foreign/uncertain outcome accepted")
			}
			if len(b.written) != before {
				t.Fatal("rejected resume wrote another resource")
			}
		})
	}
}

func knownRetireJSON(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}
