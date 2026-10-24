package deploymentplan

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func resumableFixture(t *testing.T) (*Plan, *memoryBackend, string) {
	t.Helper()
	in, first, _ := fixture(t, "dev", "Deployment")
	second := first
	second.Object = Clone(first.Object)
	Map(second.Object["metadata"])["name"] = "second"
	second.Dependencies = []string{ID(first.Object)}
	p, err := Compile(in, []Definition{first, second}, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return p, &memoryBackend{objects: map[string]Object{}}, root
}

func TestResumablePlanPreservesWrittenPrefixAndRetriesReadOnlyAcceptance(t *testing.T) {
	for _, stop := range []string{"after-write", "after-first-complete", "acceptance"} {
		t.Run(stop, func(t *testing.T) {
			p, b, root := resumableFixture(t)
			check := func(context.Context, *Plan) error { return nil }
			acceptCalls := 0
			accept := func(context.Context, *Plan) error {
				acceptCalls++
				if stop == "acceptance" && acceptCalls == 1 {
					return errors.New("read-only health failed")
				}
				return nil
			}
			if stop == "after-write" {
				b.failWait = true
			}
			if stop == "after-first-complete" {
				b.failID = p.Resources[1].ID
			}
			if err := p.ExecuteResumable(context.Background(), b, p.SHA256(), root, check, check, accept); err == nil {
				t.Fatal("injected interruption did not stop execution")
			}
			b.failID, b.failWait = "", false
			if err := p.ExecuteResumable(context.Background(), b, p.SHA256(), root, check, check, accept); err != nil {
				t.Fatal("known writes could not resume", err)
			}
			if len(b.written) != 2 {
				t.Fatal("resource write was replayed", len(b.written))
			}
			j, exists, err := p.ReadExecution(root)
			if err != nil || !exists || !j.Accepted || j.Failed != "" || len(j.Completed) != 2 {
				t.Fatal(j, exists, err)
			}
			before, _ := os.ReadFile(filepath.Join(root, "deployment/rollback", p.SHA256(), "journal.json"))
			if err := p.ExecuteResumable(context.Background(), b, p.SHA256(), root, check, check, accept); err != nil {
				t.Fatal(err)
			}
			after, _ := os.ReadFile(filepath.Join(root, "deployment/rollback", p.SHA256(), "journal.json"))
			if string(before) != string(after) || len(b.written) != 2 {
				t.Fatal("accepted execution was rewritten or replayed")
			}
		})
	}
}

func TestResumablePlanRefusesUnknownWritesAndReplacementOwners(t *testing.T) {
	for _, mode := range []string{"in-flight", "missing-owner", "foreign-owner", "unordered-prefix", "unknown-owner", "changed-plan", "deleted", "replacement", "changed-body", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			p, b, root := resumableFixture(t)
			check := func(context.Context, *Plan) error { return nil }
			b.failWait = true
			if err := p.ExecuteResumable(context.Background(), b, p.SHA256(), root, check, check, check); err == nil {
				t.Fatal("fixture did not stop")
			}
			b.failWait = false
			j, _, err := p.ReadExecution(root)
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			switch mode {
			case "in-flight":
				j.InFlight = p.Resources[0].ID
			case "missing-owner":
				j.Completed = []string{p.Resources[0].ID}
				delete(j.Owners, p.Resources[0].ID)
			case "foreign-owner":
				j.Owners[p.Resources[0].ID] = "foreign"
			case "unordered-prefix":
				j.Completed = []string{p.Resources[1].ID}
			case "unknown-owner":
				j.Owners["Deployment/other/unknown"] = "other"
			case "changed-plan":
				p.Resources[0].Desired["unexpected"] = true
			case "deleted":
				delete(b.objects, p.Resources[0].ID)
			case "replacement":
				Map(b.objects[p.Resources[0].ID]["metadata"])["uid"] = "replacement"
			case "changed-body":
				Map(b.objects[p.Resources[0].ID]["spec"])["replicas"] = float64(8)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if mode != "changed-plan" {
				if err := saveJournal(filepath.Join(root, "deployment/rollback", p.SHA256()), j); err != nil {
					t.Fatal(err)
				}
			}
			before := len(b.written)
			if err := p.ExecuteResumable(ctx, b, p.SHA256(), root, check, check, check); err == nil {
				t.Fatal("invalid resume accepted")
			}
			if len(b.written) != before {
				t.Fatal("invalid resume wrote resources")
			}
		})
	}
}

func TestResumablePlanDoesNotChangeOrdinaryExecutionRecovery(t *testing.T) {
	p, b, root := resumableFixture(t)
	check := func(context.Context, *Plan) error { return nil }
	b.failWait = true
	_ = p.Execute(context.Background(), b, p.SHA256(), root, check, check, check)
	b.failWait = false
	if err := p.Execute(context.Background(), b, p.SHA256(), root, check, check, check); err == nil {
		t.Fatal("ordinary Execute silently opted into resume")
	}
	raw, _ := os.ReadFile(filepath.Join(root, "deployment/rollback", p.SHA256(), "journal.json"))
	var j Journal
	if json.Unmarshal(raw, &j) != nil || j.Accepted || len(b.written) != 1 {
		t.Fatal("ordinary journal changed")
	}
}

func TestResumableAdmissionVerifiesWritesWithoutRecreatingThem(t *testing.T) {
	p, b, root := resumableFixture(t)
	check := func(context.Context, *Plan) error { return nil }
	b.failWait = true
	_ = p.ExecuteResumable(context.Background(), b, p.SHA256(), root, check, check, check)
	b.failWait = false
	before, _ := os.ReadFile(filepath.Join(root, "deployment/rollback", p.SHA256(), "journal.json"))
	b.dry = nil
	if err := p.AdmitResumable(context.Background(), b, root); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "deployment/rollback", p.SHA256(), "journal.json"))
	if string(before) != string(after) || len(b.written) != 1 || len(b.dry) != 1 || ID(b.dry[0]) != p.Resources[1].ID {
		t.Fatal("qualification wrote evidence or admitted an already applied create")
	}
	Map(b.objects[p.Resources[0].ID]["metadata"])["uid"] = "replacement"
	if err := p.AdmitResumable(context.Background(), b, root); err == nil {
		t.Fatal("replacement was treated as a recorded write")
	}
}
