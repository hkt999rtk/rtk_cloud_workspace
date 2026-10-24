package deploymentplan

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Readiness deliberately requires peers whose native writes sort later. The
// ordinary executor, journals, admission and leases are the production code;
// only the native API's object/readiness boundary is in memory.
type phaseReadinessBackend struct {
	*memoryBackend
	requires                                               map[string][]string
	waits, events                                          []string
	waitFailure, lostResponse, cancelWrite, cancelComplete string
	cancel                                                 context.CancelFunc
	mutateWait                                             func(Resource)
	writesAtFirstWait                                      int
	beforeWrite                                            func(Object, bool) error
}

func (b *phaseReadinessBackend) Write(ctx context.Context, action string, body Object, dry bool) (Object, error) {
	if b.beforeWrite != nil {
		if err := b.beforeWrite(body, dry); err != nil {
			return nil, err
		}
	}
	actual, err := b.memoryBackend.Write(ctx, action, body, dry)
	if dry || err != nil {
		return actual, err
	}
	b.events = append(b.events, "write:"+ID(body))
	if ID(body) == b.cancelWrite && b.cancel != nil {
		b.cancel()
	}
	if ID(body) == b.lostResponse {
		return nil, errors.New("isolated lost native write response")
	}
	return actual, nil
}

func (b *phaseReadinessBackend) Wait(ctx context.Context, r Resource) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(b.waits) == 0 {
		b.writesAtFirstWait = len(b.written)
	}
	b.waits = append(b.waits, r.ID)
	b.events = append(b.events, "wait:"+r.ID)
	for _, peer := range b.requires[r.ID] {
		if b.objects[peer] == nil {
			return errors.New("native readiness requires not-yet-created peer")
		}
	}
	if b.waitFailure == r.ID {
		return errors.New("isolated native readiness failed")
	}
	if b.mutateWait != nil {
		b.mutateWait(r)
	}
	return b.memoryBackend.Wait(ctx, r)
}

func (b *phaseReadinessBackend) ExecutionResourceProgress(_ context.Context, _ int, _ int, r Resource, step string) {
	if step == "complete" && r.ID == b.cancelComplete && b.cancel != nil {
		b.cancel()
	}
}

func phaseReadinessFixture(t *testing.T, environment, kind string) (*Plan, *phaseReadinessBackend, string) {
	t.Helper()
	in, first, _ := fixture(t, environment, kind)
	Map(first.Object["metadata"])["name"] = "a-nginx"
	if kind == "Pod" {
		in.Operation = "scoped"
		first.Object["apiVersion"] = "v1"
		first.Object["spec"] = Clone(Map(Map(first.Object["spec"])["template"])["spec"].(Object))
	}
	second := first
	second.Object = Clone(first.Object)
	Map(second.Object["metadata"])["name"] = "b-rails"
	route := Definition{Phase: "routing", Object: Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": Object{"name": "after-ready", "namespace": in.Stack + "-video-cloud"}, "data": Object{"routing": "only-after-readiness"}}}
	p, err := Compile(in, []Definition{first, second, route}, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	b := &phaseReadinessBackend{memoryBackend: &memoryBackend{objects: map[string]Object{}}, requires: map[string][]string{p.Resources[0].ID: {p.Resources[1].ID}}}
	return p, b, root
}

func phaseReadinessRun(ctx context.Context, p *Plan, b *phaseReadinessBackend, root string, accepted *int) error {
	check := func(ctx context.Context, _ *Plan) error { return ctx.Err() }
	return p.ExecuteResumable(ctx, b, p.SHA256(), root, check, check, func(ctx context.Context, _ *Plan) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		*accepted++
		return nil
	})
}

func phaseReadinessWrites(b *phaseReadinessBackend, id string) int {
	count := 0
	for _, o := range b.written {
		if ID(o) == id {
			count++
		}
	}
	return count
}

func TestWorkloadPhaseReadinessWaitsAfterEveryPeerWriteBeforeRouting(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "Pod"} {
			for _, cyclic := range []bool{false, true} {
				mode := "later-peer"
				if cyclic {
					mode = "cyclic-native-readiness"
				}
				t.Run(environment+"/"+kind+"/"+mode, func(t *testing.T) {
					p, b, root := phaseReadinessFixture(t, environment, kind)
					before := p.SHA256()
					accepted := 0
					if cyclic {
						b.requires[p.Resources[1].ID] = []string{p.Resources[0].ID}
					}
					if err := phaseReadinessRun(t.Context(), p, b, root, &accepted); err != nil {
						t.Fatal("same-phase peer never received its original write", err)
					}
					j, exists, err := p.ReadExecution(root)
					if err != nil || !exists || !j.Accepted || accepted != 1 || len(j.Completed) != 3 || b.writesAtFirstWait != 2 || len(b.written) != 3 || p.SHA256() != before || b.leaseHeld {
						t.Fatal("phase readiness was skipped, advanced routing early or altered the plan", j, err, b.events)
					}
					firstRoute := strings.Index(strings.Join(b.events, "\n"), "write:"+p.Resources[2].ID)
					for _, r := range p.Resources[:2] {
						if wait := strings.Index(strings.Join(b.events, "\n"), "wait:"+r.ID); wait < 0 || wait > firstRoute {
							t.Fatal("routing preceded complete workload readiness", b.events)
						}
					}
				})
			}
		}
	}
}

func TestWorkloadPhaseReadinessResumesKnownPartialWritesWithoutReplaying(t *testing.T) {
	for _, mode := range []string{"known-response", "midphase-completed", "phase-completed-before-barrier", "phase-completed-unready", "failed-barrier", "unknown-response", "replaced-owner", "changed-body", "replacement-during-wait", "inflight-tamper"} {
		t.Run(mode, func(t *testing.T) {
			p, b, root := phaseReadinessFixture(t, "dev", "Deployment")
			before := p.SHA256()
			accepted := 0
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			b.cancel = cancel
			if mode == "phase-completed-before-barrier" || mode == "phase-completed-unready" {
				b.cancelComplete = p.Resources[1].ID
			} else if mode == "failed-barrier" || mode == "replacement-during-wait" {
				b.waitFailure = p.Resources[0].ID
			} else if mode == "unknown-response" {
				b.lostResponse = p.Resources[0].ID
			} else {
				b.cancelComplete = p.Resources[0].ID
			}
			if err := phaseReadinessRun(ctx, p, b, root, &accepted); err == nil {
				t.Fatal("fixture did not interrupt ordinary execution")
			}
			journalPath := filepath.Join(root, "deployment", "rollback", p.SHA256(), "journal.json")
			j := readExecutionJournal(t, root, p.SHA256())
			if accepted != 0 || j.Accepted || j.Owners[p.Resources[0].ID] == "" && mode != "unknown-response" || b.objects[p.Resources[2].ID] != nil {
				t.Fatal("interrupted readiness accepted or advanced routing", j)
			}
			if mode == "known-response" {
				// Replay the earlier native-response checkpoint from an older runner:
				// retain the actual successful owner/write, without its later Complete.
				j.Completed = nil
				if err := saveJournal(filepath.Dir(journalPath), j); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "inflight-tamper" {
				j.InFlight = p.Resources[0].ID
				if err := saveJournal(filepath.Dir(journalPath), j); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "replaced-owner" {
				Map(b.objects[p.Resources[0].ID]["metadata"])["uid"] = "foreign-owner"
			}
			if mode == "changed-body" {
				Map(b.objects[p.Resources[0].ID]["spec"])["replicas"] = float64(99)
			}
			if mode == "replacement-during-wait" {
				b.mutateWait = func(r Resource) {
					if r.ID == p.Resources[0].ID {
						Map(b.objects[r.ID]["metadata"])["uid"] = "foreign-after-wait"
					}
				}
			}
			b.cancel, b.cancelComplete, b.cancelWrite, b.waitFailure = nil, "", "", ""
			priorWrites := len(b.written)
			if mode == "phase-completed-unready" {
				b.waitFailure = p.Resources[0].ID
			}
			err := phaseReadinessRun(t.Context(), p, b, root, &accepted)
			allowed := mode == "known-response" || mode == "midphase-completed" || mode == "phase-completed-before-barrier" || mode == "failed-barrier"
			if (err == nil) != allowed || p.SHA256() != before || b.leaseHeld {
				t.Fatal("unexpected original readiness recovery result", err, b.events)
			}
			if allowed {
				j, exists, readErr := p.ReadExecution(root)
				if readErr != nil || !exists || !j.Accepted || accepted != 1 || len(j.Completed) != 3 || phaseReadinessWrites(b, p.Resources[0].ID) != 1 || phaseReadinessWrites(b, p.Resources[1].ID) != 1 || phaseReadinessWrites(b, p.Resources[2].ID) != 1 {
					t.Fatal("known write was replayed or readiness never completed", j, readErr, b.events)
				}
			} else if accepted != 0 || len(b.written) != priorWrites || b.objects[p.Resources[2].ID] != nil {
				t.Fatal("uncertain/drifted outcome authorized another write", err, b.events)
			}
		})
	}
}

func TestWorkloadPhaseReadinessKeepsDeclaredDependenciesAndEarlierStagesImmediate(t *testing.T) {
	t.Run("declared-same-phase-dependency", func(t *testing.T) {
		p, b, root := phaseReadinessFixture(t, "dev", "Deployment")
		p.Resources[1].Dependencies = []string{p.Resources[0].ID}
		b.requires = map[string][]string{}
		b.beforeWrite = func(o Object, dry bool) error {
			if !dry && ID(o) == p.Resources[1].ID {
				for _, wait := range b.waits {
					if wait == p.Resources[0].ID {
						return nil
					}
				}
				return errors.New("declared dependency readiness was skipped before dependent write")
			}
			return nil
		}
		accepted := 0
		if err := phaseReadinessRun(t.Context(), p, b, root, &accepted); err != nil || accepted != 1 {
			t.Fatal("declared readiness edge lost", err, b.events)
		}
	})
	for _, entry := range []struct{ phase, kind string }{{"configuration", "Deployment"}, {"bootstrap", "StatefulSet"}, {"migration", "Job"}, {"workload", "Job"}} {
		t.Run(entry.phase+"/"+entry.kind, func(t *testing.T) {
			in, first, _ := fixture(t, "dev", entry.kind)
			first.Phase = entry.phase
			if entry.kind == "Job" {
				first.Object["apiVersion"] = "batch/v1"
			}
			second := Definition{Phase: "routing", Object: Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": Object{"namespace": in.Stack + "-video-cloud", "name": "later"}, "data": Object{"field": "later"}}}
			p, err := Compile(in, []Definition{first, second}, nil)
			if err != nil {
				t.Fatal(err)
			}
			root := t.TempDir()
			os.Chmod(root, 0700)
			b := &phaseReadinessBackend{memoryBackend: &memoryBackend{objects: map[string]Object{}}, requires: map[string][]string{p.Resources[0].ID: {p.Resources[1].ID}}}
			accepted := 0
			if err := phaseReadinessRun(t.Context(), p, b, root, &accepted); err == nil || accepted != 0 || len(b.written) != 1 || b.objects[p.Resources[1].ID] != nil {
				t.Fatal("non-deferred prerequisite was treated as an unordered peer", err, b.events)
			}
			j, exists, err := p.ReadExecution(root)
			if err != nil || !exists || len(j.Completed) != 0 || j.Owners[p.Resources[0].ID] == "" || j.Accepted {
				t.Fatal("immediate failed readiness lost its original known response", j, err)
			}
		})
	}
}

func TestWorkloadPhaseReadinessKeepsEveryFrozenContiguousPhaseBoundary(t *testing.T) {
	for _, mode := range []string{"success", "resume-first-block", "completed-earlier-block-unready"} {
		t.Run(mode, func(t *testing.T) {
			p, b, root := phaseReadinessFixture(t, "dev", "Deployment")
			first, second, route := p.Resources[0], p.Resources[1], p.Resources[2]
			third, fourth := first, second
			third.Desired, fourth.Desired = Clone(first.Desired), Clone(second.Desired)
			Map(third.Desired["metadata"])["name"] = "c-later-worker"
			Map(fourth.Desired["metadata"])["name"] = "d-later-peer"
			third.ID, fourth.ID = ID(third.Desired), ID(fourth.Desired)
			job := Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": Object{"name": "custody-between-blocks", "namespace": p.Stack + "-video-cloud", "labels": Object{"rtk.realtek.com/stack": p.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}}, "spec": Object{"backoffLimit": float64(0), "template": Object{"spec": Object{"restartPolicy": "Never", "containers": []any{Object{"name": "custody", "image": "registry.example/custody@sha256:" + strings.Repeat("a", 64)}}}}}}
			middle := Resource{ID: ID(job), Phase: "bootstrap", Action: "create", Desired: job}
			// Linked historical targets append accepted lifecycle resources and can
			// revisit a phase label. Preserve the frozen order; never regroup it.
			p.Resources = []Resource{first, second, middle, third, fourth, route}
			before := p.SHA256()
			accepted := 0
			b.requires[third.ID] = []string{fourth.ID}
			b.beforeWrite = func(o Object, dry bool) error {
				if dry {
					return nil
				}
				wanted := []string{}
				if ID(o) == middle.ID {
					wanted = []string{first.ID, second.ID}
				}
				if ID(o) == third.ID {
					wanted = []string{middle.ID}
				}
				for _, id := range wanted {
					found := false
					for _, wait := range b.waits {
						if wait == id {
							found = true
						}
					}
					if !found {
						return errors.New("prior contiguous block readiness was skipped")
					}
				}
				return nil
			}
			ctx := t.Context()
			if mode != "success" {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				defer cancel()
				b.cancel = cancel
				b.cancelComplete = first.ID
				if mode == "completed-earlier-block-unready" {
					b.cancelComplete = third.ID
				}
				if err := phaseReadinessRun(ctx, p, b, root, &accepted); err == nil {
					t.Fatal("missing genuine block interruption")
				}
				b.cancel, b.cancelComplete = nil, ""
				if mode == "completed-earlier-block-unready" {
					b.waitFailure = first.ID
				}
			}
			priorWrites := len(b.written)
			err := phaseReadinessRun(t.Context(), p, b, root, &accepted)
			if mode == "completed-earlier-block-unready" {
				if err == nil || accepted != 0 || len(b.written) != priorWrites || b.objects[fourth.ID] != nil || b.objects[route.ID] != nil {
					t.Fatal("an incomplete later block waived earlier completed-block readiness", err, b.events)
				}
				return
			}
			j, exists, readErr := p.ReadExecution(root)
			if err != nil || readErr != nil || !exists || !j.Accepted || accepted != 1 || len(j.Completed) != 6 || p.SHA256() != before || b.leaseHeld {
				t.Fatal("repeated literal phase labels were merged/reordered or never accepted", err, readErr, b.events)
			}
			for _, r := range p.Resources {
				if phaseReadinessWrites(b, r.ID) != 1 {
					t.Fatal("a literal original write was repeated", r.ID, b.events)
				}
			}
		})
	}
}

func TestWorkloadPhaseReadinessAcceptedRetryStillRequiresCurrentReadiness(t *testing.T) {
	p, b, root := phaseReadinessFixture(t, "dev", "Deployment")
	accepted := 0
	if err := phaseReadinessRun(t.Context(), p, b, root, &accepted); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "deployment", "rollback", p.SHA256(), "journal.json")
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	b.waitFailure = p.Resources[0].ID
	writes := len(b.written)
	if err := phaseReadinessRun(t.Context(), p, b, root, &accepted); err == nil || accepted != 1 || len(b.written) != writes || b.leaseHeld {
		t.Fatal("accepted retry bypassed fresh native readiness or rewrote resources", err, b.events)
	}
	after, err := os.ReadFile(path)
	if err != nil || string(before) != string(after) {
		t.Fatal("accepted historical journal was rewritten", err)
	}
}
