package deploymentplan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

type phaseReadBackend struct {
	memoryBackend
	reads           []string
	waits           []string
	progress        []string
	progressResults []executionPhaseResult
	resourceEvents  []executionResourceEvent
	beforeRead      func(string)
	beforeWrite     func(Object, bool)
	progressHook    func(string, string)
	resourceHook    func(int, int, Resource, string)
}

type executionPhaseResult struct {
	Phase, Step string
	Passed      bool
}

func (b *phaseReadBackend) ExecutionProgressResult(_ context.Context, phase, step string, passed bool) {
	b.progressResults = append(b.progressResults, executionPhaseResult{phase, step, passed})
}

type executionResourceEvent struct {
	Index, Total     int
	ID, Action, Step string
}

func (b *phaseReadBackend) ExecutionResourceProgress(_ context.Context, index, total int, resource Resource, step string) {
	b.resourceEvents = append(b.resourceEvents, executionResourceEvent{index, total, resource.ID, resource.Action, step})
	if b.resourceHook != nil {
		b.resourceHook(index, total, resource, step)
	}
}

func (b *phaseReadBackend) ExecutionProgress(_ context.Context, phase, step string) {
	if b.progressHook != nil {
		b.progressHook(phase, step)
	}
	if phase != "" {
		step = phase + "/" + step
	}
	b.progress = append(b.progress, step)
}

func (b *phaseReadBackend) Read(ctx context.Context, id string) (Object, error) {
	b.reads = append(b.reads, id)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if b.beforeRead != nil {
		b.beforeRead(id)
	}
	return b.memoryBackend.Read(ctx, id)
}

func (b *phaseReadBackend) Write(ctx context.Context, action string, body Object, dry bool) (Object, error) {
	if b.beforeWrite != nil {
		b.beforeWrite(body, dry)
	}
	return b.memoryBackend.Write(ctx, action, body, dry)
}

func (b *phaseReadBackend) Wait(ctx context.Context, resource Resource) error {
	b.waits = append(b.waits, resource.ID)
	return b.memoryBackend.Wait(ctx, resource)
}

func (b *phaseReadBackend) snapshot() map[string]Object {
	objects := map[string]Object{}
	for id, object := range b.objects {
		objects[id] = Clone(object)
	}
	return objects
}

type phaseObservationFixture struct {
	plan                    *Plan
	backend                 *phaseReadBackend
	root                    string
	first, retained, future string
}

func phaseObservationProfile(t *testing.T, environment string, createFuture bool) phaseObservationFixture {
	t.Helper()
	in, workload, liveWorkload := fixture(t, environment, "Deployment")
	Map(workload.Object["spec"])["replicas"] = float64(2)
	config := func(name, uid string) Object {
		return Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": Object{"name": name, "namespace": in.Stack + "-video-cloud", "uid": uid, "resourceVersion": "1", "labels": Object{"rtk.realtek.com/stack": in.Stack, "app.kubernetes.io/managed-by": "rtk-cloud"}}, "data": Object{"value": "original"}}
	}
	first, retained := config("first", "first-owner"), config("retained", "retained-owner")
	desired := Clone(first)
	Map(desired["data"])["value"] = "desired"
	workload.Dependencies = []string{ID(first), ID(retained)}
	live := []Object{first, retained}
	if !createFuture {
		live = append(live, liveWorkload)
	}
	plan, err := Compile(in, []Definition{{Object: desired, Phase: "configuration"}, {Object: Clone(retained), Phase: "configuration", Retain: true}, workload}, live)
	if err != nil {
		t.Fatal(err)
	}
	backend := &phaseReadBackend{memoryBackend: memoryBackend{objects: map[string]Object{}}}
	for _, object := range live {
		backend.objects[ID(object)] = Clone(object)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	return phaseObservationFixture{plan, backend, root, ID(first), ID(retained), ID(workload.Object)}
}

func assertPhaseExecutionStopped(t *testing.T, f phaseObservationFixture, err error, failed string, completed, writes int) {
	t.Helper()
	var failure *Blocker
	if !errors.As(err, &failure) || failure.Resource != failed || f.backend.leaseHeld || len(f.backend.written) != writes {
		t.Fatal("phase drift lost its resource, persisted a dependent write, or retained execution ownership", err)
	}
	journal := readExecutionJournal(t, f.root, f.plan.SHA256())
	if journal.Failed != failed || journal.Accepted || journal.InFlight != "" || len(journal.Completed) != completed {
		t.Fatal("phase failure lost the original completed-resource journal", journal)
	}
	if _, err := os.Stat(filepath.Join(f.root, "deployment", ".execution-active")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("phase failure retained its active local lock", err)
	}
}

func TestPhaseObservationReusesOnlyEachStagesDependencySnapshot(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			reads := [2]int{}
			for index, optimized := range []bool{false, true} {
				f := phaseObservationProfile(t, environment, false)
				sha := f.plan.SHA256()
				fences, snapshots := 0, 0
				qualified := func(context.Context, *Plan) error { return nil }
				fence := func(context.Context, *Plan) error { fences++; return nil }
				var phaseFences []PhaseFence
				if optimized {
					phaseFences = append(phaseFences, func(context.Context, *Plan) (map[string]Object, error) {
						snapshots++
						objects := f.backend.snapshot()
						if snapshots == 2 && Map(objects[f.first]["data"])["value"] != "desired" {
							t.Fatal("next phase reused an observation from before the completed write")
						}
						return objects, nil
					})
				}
				if err := f.plan.Execute(context.Background(), f.backend, sha, f.root, qualified, fence, qualified, phaseFences...); err != nil || f.backend.leaseHeld || len(f.backend.written) != 2 || f.plan.SHA256() != sha {
					t.Fatal("phase observation changed the reviewed rollout or execution cleanup", err)
				}
				if optimized && (snapshots != 2 || fences != 1) || !optimized && fences != 3 {
					t.Fatal("phase snapshots replaced final acceptance fencing or were retained across phases", snapshots, fences)
				}
				if journal := readExecutionJournal(t, f.root, sha); !journal.Accepted || len(journal.Completed) != 3 {
					t.Fatal("snapshot rollout lost completion evidence")
				}
				reads[index] = len(f.backend.reads)
			}
			if reads[0]-reads[1] != 6 || reads[1] != 17 {
				t.Fatal("snapshot reuse changed individual write/readback checks instead of preserving readiness readbacks and removing six duplicate stage reads", reads)
			}
		})
	}
}

func TestPhaseObservationMissingIDsAlwaysFallBackToFreshReads(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"retained", "future", "create", "nil-entry"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				f := phaseObservationProfile(t, environment, mode == "create" || mode == "nil-entry")
				failed := f.future
				if mode == "retained" {
					failed = f.retained
				}
				phaseFence := func(context.Context, *Plan) (map[string]Object, error) {
					objects := f.backend.snapshot()
					delete(objects, failed)
					if mode == "nil-entry" {
						objects[failed] = nil
					}
					switch mode {
					case "retained":
						Map(f.backend.objects[failed]["data"])["value"] = "concurrent"
					case "future":
						Map(f.backend.objects[failed]["spec"])["replicas"] = float64(99)
					default:
						f.backend.objects[failed] = Clone(f.plan.Resources[2].Desired)
						Map(f.backend.objects[failed]["metadata"])["uid"] = "concurrent-owner"
					}
					return objects, nil
				}
				qualified := func(context.Context, *Plan) error { return nil }
				err := f.plan.Execute(context.Background(), f.backend, f.plan.SHA256(), f.root, qualified, qualified, qualified, phaseFence)
				assertPhaseExecutionStopped(t, f, err, failed, 0, 0)
				if f.backend.reads[len(f.backend.reads)-1] != failed {
					t.Fatal("missing inventory entry was treated as absence without a direct read", f.backend.reads)
				}
			})
		}
	}
}

func TestNextPhaseObservationStillFencesCompletedRetainedAndFutureResources(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"completed-spec", "completed-owner", "completed-deleted", "retained", "future"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				f := phaseObservationProfile(t, environment, false)
				phases, failed := 0, f.first
				phaseFence := func(context.Context, *Plan) (map[string]Object, error) {
					phases++
					if phases == 2 {
						switch mode {
						case "completed-spec":
							Map(f.backend.objects[f.first]["data"])["value"] = "concurrent"
						case "completed-owner":
							Map(f.backend.objects[f.first]["metadata"])["uid"] = "replacement"
						case "completed-deleted":
							delete(f.backend.objects, f.first)
						case "retained":
							failed = f.retained
							Map(f.backend.objects[failed]["data"])["value"] = "concurrent"
						case "future":
							failed = f.future
							Map(f.backend.objects[failed]["spec"])["replicas"] = float64(99)
						}
					}
					return f.backend.snapshot(), nil
				}
				qualified := func(context.Context, *Plan) error { return nil }
				err := f.plan.Execute(context.Background(), f.backend, f.plan.SHA256(), f.root, qualified, qualified, qualified, phaseFence)
				assertPhaseExecutionStopped(t, f, err, failed, 2, 1)
				if phases != 2 {
					t.Fatal("phase observations were reused after writes")
				}
			})
		}
	}
}

func TestPhaseObservationNeverReplacesTheReadBeforeAConditionalWrite(t *testing.T) {
	for _, mode := range []string{"spec-drift", "new-resource-version"} {
		t.Run(mode, func(t *testing.T) {
			f := phaseObservationProfile(t, "dev", false)
			observed := false
			f.backend.beforeRead = func(id string) {
				if observed && id == f.first {
					if mode == "spec-drift" {
						Map(f.backend.objects[id]["data"])["value"] = "concurrent"
					} else {
						Map(f.backend.objects[id]["metadata"])["resourceVersion"] = "2"
					}
				}
			}
			phaseFence := func(context.Context, *Plan) (map[string]Object, error) {
				objects := f.backend.snapshot()
				observed = true
				return objects, nil
			}
			qualified := func(context.Context, *Plan) error { return nil }
			err := f.plan.Execute(context.Background(), f.backend, f.plan.SHA256(), f.root, qualified, qualified, qualified, phaseFence)
			if mode == "spec-drift" {
				assertPhaseExecutionStopped(t, f, err, f.first, 0, 0)
			} else if err != nil || len(f.backend.written) != 2 || Map(f.backend.written[0]["metadata"])["resourceVersion"] != "2" {
				t.Fatal("conditional write used the phase snapshot's stale API resource version", err)
			}
		})
	}
}

func TestPhaseObservationFailureAndCancellationPreserveTheOriginalJournal(t *testing.T) {
	for _, mode := range []string{"callback-error", "cancel-with-error", "cancel-with-snapshot"} {
		t.Run(mode, func(t *testing.T) {
			f := phaseObservationProfile(t, "dev", false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			phaseFence := func(context.Context, *Plan) (map[string]Object, error) {
				if mode == "callback-error" {
					return nil, errors.New("private-phase-error")
				}
				cancel()
				if mode == "cancel-with-error" {
					return nil, ctx.Err()
				}
				return f.backend.snapshot(), nil
			}
			qualified := func(context.Context, *Plan) error { return nil }
			err := f.plan.Execute(ctx, f.backend, f.plan.SHA256(), f.root, qualified, qualified, qualified, phaseFence)
			assertPhaseExecutionStopped(t, f, err, f.first, 0, 0)
			var failure *Blocker
			if !errors.As(err, &failure) || failure.ID != "deployment.inputs" || len(f.backend.reads) != 3 {
				t.Fatal("phase failure changed journal semantics or continued dependency reads", err, f.backend.reads)
			}
		})
	}
}

func TestExecuteRejectsAmbiguousOptionalPhaseFencesBeforeQualification(t *testing.T) {
	phaseFence := PhaseFence(func(context.Context, *Plan) (map[string]Object, error) { return nil, nil })
	for _, callbacks := range [][]PhaseFence{{nil}, {phaseFence, phaseFence}} {
		f := phaseObservationProfile(t, "dev", false)
		qualified := func(context.Context, *Plan) error { t.Fatal("invalid phase fence reached qualification"); return nil }
		if err := f.plan.Execute(context.Background(), f.backend, f.plan.SHA256(), f.root, qualified, qualified, qualified, callbacks...); err == nil || f.backend.leaseHeld || len(f.backend.reads) != 0 || len(f.backend.written) != 0 {
			t.Fatal("ambiguous phase fences started an execution", err)
		}
	}
}

func TestExecutionProgressReportsOnlyActualStageTransitions(t *testing.T) {
	for _, failed := range []bool{false, true} {
		f := phaseObservationProfile(t, "dev", false)
		qualified := func(context.Context, *Plan) error { return nil }
		phaseFence := func(context.Context, *Plan) (map[string]Object, error) {
			if failed {
				return nil, errors.New("phase could not be observed")
			}
			return f.backend.snapshot(), nil
		}
		err := f.plan.Execute(context.Background(), f.backend, f.plan.SHA256(), f.root, qualified, qualified, qualified, phaseFence)
		want := []string{"qualification", "admission", "lease", "configuration/fence"}
		if !failed {
			want = append(want, "configuration/apply", "workload/fence", "workload/apply", "verification", "acceptance", "verification", "ownership", "journal")
		}
		want = append(want, "cleanup")
		if (err != nil) != failed || !reflect.DeepEqual(f.backend.progress, want) {
			t.Fatal("execution progress implied an unapplied stage or emitted per-resource events", err, f.backend.progress)
		}
	}
}

func TestExecutionRejectsPlanMutationFromPhaseCallbacksBeforeAnyWrite(t *testing.T) {
	for _, mode := range []string{"snapshot-fence", "legacy-fence", "phase-progress"} {
		t.Run(mode, func(t *testing.T) {
			f := phaseObservationProfile(t, "dev", false)
			sha := f.plan.SHA256()
			mutate := func() { Map(f.plan.Resources[0].Desired["data"])["value"] = "unapproved" }
			qualified := func(context.Context, *Plan) error { return nil }
			fence := qualified
			var phaseFences []PhaseFence
			if mode == "legacy-fence" {
				fence = func(context.Context, *Plan) error { mutate(); return nil }
			} else {
				phaseFences = append(phaseFences, func(context.Context, *Plan) (map[string]Object, error) {
					if mode == "snapshot-fence" {
						mutate()
					}
					return f.backend.snapshot(), nil
				})
				if mode == "phase-progress" {
					f.backend.progressHook = func(_, step string) {
						if step == "apply" {
							mutate()
						}
					}
				}
			}
			err := f.plan.Execute(context.Background(), f.backend, sha, f.root, qualified, fence, qualified, phaseFences...)
			if err == nil || !strings.Contains(err.Error(), "plan changed during execution") || len(f.backend.written) != 0 || f.backend.leaseHeld || len(f.backend.reads) != 3 {
				t.Fatal("callback mutation persisted an unapproved body or continued reading dependencies", err)
			}
			journal := readExecutionJournal(t, f.root, sha)
			if journal.Accepted || journal.Failed != f.first || len(journal.Completed) != 0 {
				t.Fatal("callback mutation lost the original plan's failure journal")
			}
		})
	}
}

func TestExecutionCancellationDuringPhaseChecksStopsBeforeWrites(t *testing.T) {
	for _, mode := range []string{"dependency-read", "apply-progress"} {
		t.Run(mode, func(t *testing.T) {
			f := phaseObservationProfile(t, "dev", false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			observed := false
			failed, wantReads := f.first, 3
			phaseFence := func(context.Context, *Plan) (map[string]Object, error) {
				observed = true
				objects := f.backend.snapshot()
				if mode == "dependency-read" {
					delete(objects, f.first)
				}
				return objects, nil
			}
			if mode == "dependency-read" {
				failed, wantReads = f.retained, 4
				f.backend.beforeRead = func(id string) {
					if observed && id == f.first {
						cancel()
					}
				}
			} else {
				f.backend.progressHook = func(_, step string) {
					if step == "apply" {
						cancel()
					}
				}
			}
			qualified := func(context.Context, *Plan) error { return nil }
			err := f.plan.Execute(ctx, f.backend, f.plan.SHA256(), f.root, qualified, qualified, qualified, phaseFence)
			if !errors.Is(err, context.Canceled) || len(f.backend.written) != 0 || f.backend.leaseHeld || len(f.backend.reads) != wantReads {
				t.Fatal("cancelled phase scan continued reading or persisted an update", err, f.backend.reads)
			}
			journal := readExecutionJournal(t, f.root, f.plan.SHA256())
			if journal.Failed != failed || journal.Accepted || len(journal.Completed) != 0 {
				t.Fatal("cancelled phase scan lost its exact failure journal")
			}
		})
	}
}

func TestExecutionResourceProgressFollowsActualWorkAndSavedCompletion(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			f := phaseObservationProfile(t, environment, false)
			Map(f.plan.Resources[0].Desired["data"])["value"] = "private-progress-fixture"
			sha := f.plan.SHA256()
			f.backend.beforeWrite = func(body Object, dry bool) {
				if !dry {
					journal := readExecutionJournal(t, f.root, sha)
					if journal.InFlight != ID(body) {
						t.Fatal("resource progress reordered persisted writes before their in-flight journal")
					}
				}
			}
			f.backend.resourceHook = func(index, total int, resource Resource, step string) {
				if total != 3 || index < 0 || index >= total {
					t.Fatal("resource progress lost its stable zero-based position")
				}
				if step == "complete" {
					journal := readExecutionJournal(t, f.root, sha)
					if len(journal.Completed) != index+1 || journal.Completed[index] != resource.ID || journal.Owners[resource.ID] == "" || journal.InFlight != "" || journal.Accepted {
						t.Fatal("resource completion was reported before durable evidence or as accepted deployment")
					}
				}
			}
			qualified := func(context.Context, *Plan) error { return nil }
			phaseFence := func(context.Context, *Plan) (map[string]Object, error) { return f.backend.snapshot(), nil }
			if err := f.plan.Execute(context.Background(), f.backend, sha, f.root, qualified, qualified, qualified, phaseFence); err != nil {
				t.Fatal(err)
			}
			var want []executionResourceEvent
			for index, resource := range f.plan.Resources {
				steps := []string{"read", "dry-run", "apply", "wait", "complete"}
				if resource.Action == "retain" {
					steps = []string{"read", "wait", "complete"}
				}
				if deferredWorkloadReadiness(resource) {
					steps = []string{"read", "dry-run", "apply", "complete", "wait"}
					if resource.Action == "retain" {
						steps = []string{"read", "complete", "wait"}
					}
				}
				for _, step := range steps {
					want = append(want, executionResourceEvent{index, 3, resource.ID, resource.Action, step})
				}
			}
			if !reflect.DeepEqual(f.backend.resourceEvents, want) || strings.Contains(fmt.Sprint(f.backend.resourceEvents), "private-progress-fixture") {
				t.Fatal("resource progress skipped work, reordered resources, or exposed their payload", f.backend.resourceEvents)
			}
		})
	}
}

func TestExecutionResourceProgressDoesNotReportAnEarlyWaitForDelayedPVC(t *testing.T) {
	in, _, _ := fixture(t, "dev", "Deployment")
	claim := Definition{Phase: "storage", Object: Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": Object{"name": "state", "namespace": in.Stack + "-video-cloud"}, "spec": Object{"storageClassName": "delayed", "accessModes": []any{"ReadWriteOnce"}, "resources": Object{"requests": Object{"storage": "1Gi"}}}}}
	plan, err := Compile(in, []Definition{claim}, nil)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	backend := &phaseReadBackend{memoryBackend: memoryBackend{objects: map[string]Object{}}}
	qualified := func(context.Context, *Plan) error { return nil }
	if err := plan.Execute(context.Background(), backend, plan.SHA256(), root, qualified, qualified, qualified); err != nil {
		t.Fatal(err)
	}
	steps := []string{}
	for _, event := range backend.resourceEvents {
		steps = append(steps, event.Step)
	}
	if !reflect.DeepEqual(steps, []string{"read", "dry-run", "apply", "complete"}) || len(backend.waits) == 0 {
		t.Fatal("delayed storage was reported as waiting during apply or skipped final binding verification", steps, backend.waits)
	}
}

func TestExecutionResourceProgressCancellationPreservesTruthfulCounts(t *testing.T) {
	for _, step := range []string{"read", "dry-run", "apply", "wait", "complete"} {
		t.Run(step, func(t *testing.T) {
			f := phaseObservationProfile(t, "dev", false)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			f.backend.resourceHook = func(index, _ int, _ Resource, current string) {
				if index == 0 && current == step {
					cancel()
				}
			}
			qualified := func(context.Context, *Plan) error { return nil }
			phaseFence := func(context.Context, *Plan) (map[string]Object, error) { return f.backend.snapshot(), nil }
			err := f.plan.Execute(ctx, f.backend, f.plan.SHA256(), f.root, qualified, qualified, qualified, phaseFence)
			wantWrites, wantCompleted, wantWaits := 0, 0, 0
			if step == "wait" || step == "complete" {
				wantWrites = 1
			}
			if step == "complete" {
				wantCompleted, wantWaits = 1, 1
			}
			if !errors.Is(err, context.Canceled) || len(f.backend.written) != wantWrites || len(f.backend.waits) != wantWaits || f.backend.leaseHeld {
				t.Fatal("resource progress cancellation continued writes or waits", err, f.backend.resourceEvents)
			}
			journal := readExecutionJournal(t, f.root, f.plan.SHA256())
			if journal.Failed != f.first || journal.Accepted || journal.InFlight != "" || len(journal.Completed) != wantCompleted {
				t.Fatal("resource progress fabricated completion or an uncertain API request", journal)
			}
			if last := f.backend.resourceEvents[len(f.backend.resourceEvents)-1]; last.Index != 0 || last.Total != 3 || last.Step != step {
				t.Fatal("cancelled progress advanced another resource", f.backend.resourceEvents)
			}
		})
	}
}

func TestExecutionResourceProgressCannotMutateTheApprovedPlanBeforeWrites(t *testing.T) {
	for _, step := range []string{"read", "dry-run", "apply"} {
		t.Run(step, func(t *testing.T) {
			f := phaseObservationProfile(t, "dev", false)
			sha := f.plan.SHA256()
			f.backend.resourceHook = func(_, _ int, resource Resource, current string) {
				if current == step {
					Map(resource.Desired["data"])["value"] = "private-unapproved-change"
				}
			}
			qualified := func(context.Context, *Plan) error { return nil }
			phaseFence := func(context.Context, *Plan) (map[string]Object, error) { return f.backend.snapshot(), nil }
			err := f.plan.Execute(context.Background(), f.backend, sha, f.root, qualified, qualified, qualified, phaseFence)
			if err == nil || !strings.Contains(err.Error(), "plan changed during execution") || strings.Contains(err.Error(), "private-unapproved-change") || len(f.backend.written) != 0 || f.backend.leaseHeld {
				t.Fatal("progress callback wrote an unapproved body or exposed its payload", err)
			}
			journal := readExecutionJournal(t, f.root, sha)
			if journal.Accepted || journal.Failed != f.first || journal.InFlight != "" || len(journal.Completed) != 0 {
				t.Fatal("progress callback mutation lost the original plan's journal")
			}
		})
	}
}
