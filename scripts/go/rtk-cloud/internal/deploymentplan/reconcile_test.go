package deploymentplan

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestExecutionReconciliationNeverRevertsPartialOrConcurrentChanges(t *testing.T) {
	for _, env := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"no-writes", "accepted", "in-flight", "partial", "baseline-drift", "target-drift", "replaced-owner", "unready", "incomplete-acceptance", "missing-completion", "failed-acceptance", "wrong-plan"} {
			t.Run(env+"/"+mode, func(t *testing.T) {
				in, d, live := fixture(t, env, "Deployment")
				Map(d.Object["spec"])["replicas"] = float64(2)
				p, err := Compile(in, []Definition{d}, []Object{live})
				if err != nil {
					t.Fatal(err)
				}
				id := p.Resources[0].ID
				b := &memoryBackend{objects: map[string]Object{id: Clone(live)}}
				j := Journal{PlanSHA256: p.SHA256(), Owners: map[string]string{}}
				if mode == "accepted" || mode == "target-drift" || mode == "replaced-owner" || mode == "unready" || mode == "missing-completion" || mode == "failed-acceptance" {
					j.Accepted, j.AcceptanceStarted = true, true
					j.Completed, j.Owners[id] = []string{id}, "owner"
					b.objects[id] = Clone(p.Resources[0].Desired)
					Map(b.objects[id]["metadata"])["uid"] = "owner"
				}
				switch mode {
				case "in-flight":
					j.InFlight = id
				case "partial":
					j.Owners[id] = "owner"
				case "baseline-drift", "target-drift":
					Map(b.objects[id]["spec"])["replicas"] = float64(3)
				case "replaced-owner":
					Map(b.objects[id]["metadata"])["uid"] = "replacement"
					j.Owners[id] = "replacement"
				case "unready":
					b.failWait = true
				case "incomplete-acceptance":
					j.AcceptanceStarted = true
				case "missing-completion":
					j.Completed = nil
				case "failed-acceptance":
					j.Failed = "deployment.post-deploy"
				case "wrong-plan":
					j.PlanSHA256 = Digest("another plan")
				}
				before := Digest(b.objects)
				outcome, err := p.ReconcileOutcome(context.Background(), b, j)
				want := mode == "no-writes" || mode == "accepted"
				if (err == nil) != want || len(b.written) != 0 || len(b.dry) != 0 || Digest(b.objects) != before {
					t.Fatal("reconciliation accepted uncertainty or modified a resource", outcome, err)
				}
				if want && outcome != "no-resource-writes" && outcome != "accepted-target" {
					t.Fatal("missing exact reconciled outcome")
				}
			})
		}
	}
}

func TestRetainedResourcesReleaseStoppedExecutionWithoutAcceptingInterruptedPKI(t *testing.T) {
	in, d, live := fixture(t, "dev", "Deployment")
	d.Retain = true
	p, err := Compile(in, []Definition{d}, []Object{live})
	if err != nil {
		t.Fatal(err)
	}
	b := &memoryBackend{objects: map[string]Object{ID(live): live}}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	pass := func(context.Context, *Plan) error { return nil }
	err = p.Execute(context.Background(), b, p.SHA256(), root, pass, pass, func(context.Context, *Plan) error { return errors.New("unconfirmed PKI sealing") })
	if err == nil || b.leaseHeld || len(b.written) != 0 {
		t.Fatal("failed acceptance completed or retained its execution lease", err)
	}
	var j Journal
	raw, err := os.ReadFile(filepath.Join(root, "deployment", "rollback", p.SHA256(), "journal.json"))
	if err != nil || json.Unmarshal(raw, &j) != nil || !j.AcceptanceStarted || j.Accepted {
		t.Fatal("interrupted acceptance was not durably recorded", err)
	}
	if _, err := p.ReconcileOutcome(context.Background(), b, j); err == nil {
		t.Fatal("PKI/business uncertainty was mistaken for no mutations")
	}
	active := filepath.Join(root, "deployment", ".execution-active")
	if _, err := os.Stat(active); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stopped executor retained its active local lock", err)
	}
	archives := executionOwnerArchives(t, root, p.SHA256())
	if len(archives) != 1 {
		t.Fatal("stopped executor did not archive its exact owner")
	}
	running, err := LockExecutionOwner(archives[0], false)
	if err != nil {
		t.Fatal("stopped executor still holds its OS lock", err)
	}
	defer running.Close()
	if another, err := LockExecutionOwner(archives[0], false); err == nil {
		another.Close()
		t.Fatal("running reconciler lock was stolen")
	}
}
