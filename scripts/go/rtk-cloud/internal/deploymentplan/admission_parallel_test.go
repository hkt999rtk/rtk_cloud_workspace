package deploymentplan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type parallelAdmissionFixture struct {
	mu              sync.Mutex
	objects         map[string]Object
	events          map[string][]string
	requests        map[string]Object
	active, maximum int
	readCount       int
	started         chan string
	gate            <-chan struct{}
	readHook        func(context.Context, string, Object) (Object, error)
	writeHook       func(string, Object) Object
	falseWrites     int
}

func (b *parallelAdmissionFixture) Read(ctx context.Context, id string) (Object, error) {
	b.mu.Lock()
	b.active++
	b.maximum = max(b.maximum, b.active)
	b.readCount++
	b.events[id] = append(b.events[id], "read")
	current := Clone(b.objects[id])
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.active--
		b.mu.Unlock()
	}()
	if b.started != nil {
		b.started <- id
	}
	if b.readHook != nil {
		return b.readHook(ctx, id, current)
	}
	if b.gate != nil {
		select {
		case <-b.gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return current, nil
}

func (b *parallelAdmissionFixture) Write(_ context.Context, action string, body Object, dry bool) (Object, error) {
	b.mu.Lock()
	if !dry {
		b.falseWrites++
	}
	b.events[ID(body)] = append(b.events[ID(body)], action)
	b.requests[ID(body)] = Clone(body)
	b.mu.Unlock()
	if !dry {
		return nil, errors.New("admission persisted a mutation")
	}
	result := Clone(body)
	if b.writeHook != nil {
		result = b.writeHook(ID(body), result)
	}
	return result, nil
}

func (*parallelAdmissionFixture) Wait(context.Context, Resource) error {
	return errors.New("admission waited for a rollout")
}

type parallelAdmissionOptIn struct {
	*parallelAdmissionFixture
	concurrency int
	progress    [][2]int
}

func (b *parallelAdmissionOptIn) AdmissionConcurrency() int { return b.concurrency }
func (b *parallelAdmissionOptIn) AdmissionProgress(_ context.Context, completed, total int) {
	// Intentionally no mutex: only the admission caller may update progress.
	// The race suite detects callbacks escaping into worker goroutines.
	b.progress = append(b.progress, [2]int{completed, total})
}

func parallelAdmissionPlan(t *testing.T, environment string, count int) (*Plan, *parallelAdmissionFixture) {
	t.Helper()
	in, _, _ := fixture(t, environment, "Deployment")
	plan := &Plan{Version: Version, Inputs: in}
	backend := &parallelAdmissionFixture{objects: map[string]Object{}, events: map[string][]string{}, requests: map[string]Object{}, started: make(chan string, count)}
	for index := 0; index < count; index++ {
		_, definition, current := fixture(t, environment, "Deployment")
		for _, object := range []Object{definition.Object, current} {
			Map(object["metadata"])["name"] = fmt.Sprintf("owned-%02d", index)
		}
		Map(current["metadata"])["uid"] = fmt.Sprintf("owner-%02d", index)
		Map(definition.Object["spec"])["replicas"] = float64(2)
		resource := Resource{ID: ID(current), Phase: "workload", Action: "update", UID: String(Map(current["metadata"])["uid"]), Desired: Projection(definition.Object), Baseline: Projection(current)}
		switch index % 4 {
		case 1:
			resource.Action, resource.Desired = "retain", Projection(current)
		case 2:
			resource.Action, resource.UID, resource.Baseline = "create", "", nil
			current = nil
		case 3:
			resource.Action, resource.Desired = "retire", Projection(current)
		}
		plan.Resources = append(plan.Resources, resource)
		backend.objects[resource.ID] = current
	}
	return plan, backend
}

func waitForAdmissionReads(t *testing.T, backend *parallelAdmissionFixture, count int) {
	t.Helper()
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for index := 0; index < count; index++ {
		select {
		case <-backend.started:
		case <-timer.C:
			t.Fatal("admission did not start the expected bounded concurrent reads")
		}
	}
}

func assertAdmissionProgress(t *testing.T, progress [][2]int, completed, total int) {
	t.Helper()
	if len(progress) != completed+1 {
		t.Fatalf("progress did not count each settled check exactly once: %v", progress)
	}
	for index, row := range progress {
		if row != [2]int{index, total} {
			t.Fatalf("progress is not monotonic completed/total: %v", progress)
		}
	}
}

func TestAdmissionOptInConcurrencyIsBoundedAndGenericRemainsSerial(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, concurrency := range []int{0, 4, 8, 32} {
			t.Run(fmt.Sprintf("%s/workers-%d", environment, concurrency), func(t *testing.T) {
				plan, fixture := parallelAdmissionPlan(t, environment, 24)
				release := make(chan struct{})
				fixture.gate = release
				beforePlan, beforeObjects := plan.SHA256(), Digest(fixture.objects)
				var backend Backend = fixture
				var opted *parallelAdmissionOptIn
				workers := 1
				if concurrency != 0 {
					opted = &parallelAdmissionOptIn{parallelAdmissionFixture: fixture, concurrency: concurrency}
					backend, workers = opted, min(concurrency, maxAdmissionConcurrency)
				}
				returned := make(chan error, 1)
				go func() { returned <- plan.Admit(context.Background(), backend) }()
				waitForAdmissionReads(t, fixture, workers)
				fixture.mu.Lock()
				maximum, active, reads := fixture.maximum, fixture.active, fixture.readCount
				fixture.mu.Unlock()
				if maximum != workers || active != workers || reads != workers {
					close(release)
					t.Fatalf("admission is serial or exceeds its opt-in cap: max=%d active=%d reads=%d", maximum, active, reads)
				}
				close(release)
				if err := <-returned; err != nil {
					t.Fatal(err)
				}
				if plan.SHA256() != beforePlan || Digest(fixture.objects) != beforeObjects || fixture.falseWrites != 0 || fixture.active != 0 || fixture.maximum > workers {
					t.Fatal("admission changed the plan/baseline or left active/persisted operations")
				}
				for _, resource := range plan.Resources {
					want := []string{"read", resource.Action}
					if resource.Action == "retain" {
						want = want[:1]
					}
					if !reflect.DeepEqual(fixture.events[resource.ID], want) {
						t.Fatal("resource skipped a fresh read or retain entered mutation admission", resource.ID)
					}
					body, err := resource.Body(fixture.objects[resource.ID])
					if err != nil || Digest(body) != Digest(fixture.requests[resource.ID]) {
						t.Fatal("parallel admission changed the exact UID/RV-bound request", resource.ID, err)
					}
				}
				if opted != nil {
					assertAdmissionProgress(t, opted.progress, len(plan.Resources), len(plan.Resources))
				}
			})
		}
	}
}

func TestParallelAdmissionKeepsOriginalPlanOrderForFailures(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			plan, fixture := parallelAdmissionPlan(t, environment, 24)
			slow, fastFailed := make(chan struct{}), make(chan struct{})
			first, second := plan.Resources[0].ID, plan.Resources[1].ID
			fixture.readHook = func(ctx context.Context, id string, object Object) (Object, error) {
				if id == first {
					select {
					case <-slow:
					case <-ctx.Done():
						return nil, ctx.Err()
					}
					return nil, safeReadFailure(id)
				}
				if id == second {
					close(fastFailed)
					return nil, safeReadFailure(id)
				}
				return object, nil
			}
			backend := &parallelAdmissionOptIn{parallelAdmissionFixture: fixture, concurrency: 8}
			returned := make(chan error, 1)
			go func() { returned <- plan.Admit(context.Background(), backend) }()
			<-fastFailed
			select {
			case err := <-returned:
				close(slow)
				t.Fatal("admission returned before joining the earlier slow resource", err)
			default:
			}
			close(slow)
			err := <-returned
			assertSafeReadFailure(t, err, first)
			if fixture.active != 0 || fixture.falseWrites != 0 {
				t.Fatal("failed admission left work running or persisted a mutation")
			}
			assertAdmissionProgress(t, backend.progress, fixture.readCount, len(plan.Resources))
		})
	}
}

func TestParallelAdmissionCancellationJoinsAndRetainsParentReason(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"cancel", "deadline", "pre-cancelled"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				plan, fixture := parallelAdmissionPlan(t, environment, 32)
				ctx, cancel := context.WithCancel(context.Background())
				want := context.Canceled
				if mode == "deadline" {
					cancel()
					ctx, cancel = context.WithTimeout(context.Background(), time.Second)
					want = context.DeadlineExceeded
				}
				defer cancel()
				cleanup, cancelled := make(chan struct{}), make(chan struct{}, 8)
				fixture.readHook = func(ctx context.Context, _ string, _ Object) (Object, error) {
					<-ctx.Done()
					cancelled <- struct{}{}
					<-cleanup
					return nil, ctx.Err()
				}
				backend := &parallelAdmissionOptIn{parallelAdmissionFixture: fixture, concurrency: 8}
				before := plan.SHA256()
				returned := make(chan error, 1)
				if mode == "pre-cancelled" {
					cancel()
				}
				go func() { returned <- plan.Admit(ctx, backend) }()
				if mode != "pre-cancelled" {
					waitForAdmissionReads(t, fixture, 8)
					if mode == "cancel" {
						cancel()
					}
					for index := 0; index < 8; index++ {
						<-cancelled
					}
					select {
					case err := <-returned:
						close(cleanup)
						t.Fatal("admission returned before cancelled work joined", err)
					default:
					}
				}
				close(cleanup)
				if err := <-returned; !errors.Is(err, want) {
					t.Fatal("parallel admission lost the parent's cancellation reason", err)
				}
				if fixture.active != 0 || fixture.falseWrites != 0 || len(fixture.requests) != 0 || fixture.readCount > 8 || plan.SHA256() != before {
					t.Fatal("cancelled admission started new API work, persisted data or changed the plan")
				}
				if mode == "pre-cancelled" {
					if fixture.readCount != 0 || len(backend.progress) != 0 {
						t.Fatal("pre-cancelled admission started work or progress")
					}
				} else {
					assertAdmissionProgress(t, backend.progress, len(backend.progress)-1, len(plan.Resources))
				}
			})
		}
	}
}

func TestParallelAdmissionUsesTheSameOwnerAndMutationFencesAsSerial(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"owner", "baseline", "missing", "image", "admitted-owner", "admitted-name", "empty-response", "rv-only", "bookkeeping"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				var serialID, serialCause string
				for _, concurrency := range []int{0, 8} {
					plan, fixture := parallelAdmissionPlan(t, environment, 16)
					first := plan.Resources[0].ID
					if mode == "bookkeeping" {
						for _, object := range []Object{plan.Resources[0].Desired, plan.Resources[0].Baseline, fixture.objects[first]} {
							Map(object["metadata"])["annotations"] = Object{"kubectl.kubernetes.io/last-applied-configuration": "original-bookkeeping"}
						}
					}
					fixture.readHook = func(_ context.Context, id string, object Object) (Object, error) {
						if id == first {
							switch mode {
							case "owner":
								Map(object["metadata"])["uid"] = "replacement-owner"
							case "baseline":
								Map(object["spec"])["replicas"] = float64(99)
							case "missing":
								return nil, nil
							case "rv-only":
								Map(object["metadata"])["resourceVersion"] = "5"
							}
						}
						return object, nil
					}
					fixture.writeHook = func(id string, object Object) Object {
						if id == first {
							switch mode {
							case "image":
								diagnosticContainer(object)["image"] = "private-unapproved-image"
							case "admitted-owner":
								Map(object["metadata"])["uid"] = "replacement-owner"
							case "admitted-name":
								Map(object["metadata"])["name"] = "replacement-name"
							case "empty-response":
								return nil
							case "bookkeeping":
								Map(object["metadata"])["annotations"] = Object{"kubectl.kubernetes.io/last-applied-configuration": "private-server-bookkeeping"}
							}
						}
						return object
					}
					beforePlan, beforeObjects := plan.SHA256(), Digest(fixture.objects)
					var backend Backend = fixture
					if concurrency != 0 {
						backend = &parallelAdmissionOptIn{parallelAdmissionFixture: fixture, concurrency: concurrency}
					}
					err := plan.Admit(context.Background(), backend)
					allowed := mode == "rv-only" || mode == "bookkeeping"
					if (err == nil) != allowed || plan.SHA256() != beforePlan || Digest(fixture.objects) != beforeObjects || fixture.falseWrites != 0 || fixture.active != 0 {
						t.Fatal("parallel admission changed an existing fence or its immutable inputs", err)
					}
					if !allowed {
						var failure *Blocker
						if !errors.As(err, &failure) || failure.Resource != first || strings.Contains(err.Error(), "private-") {
							t.Fatal("admission failure lost its exact resource or exposed private values", err)
						}
						if concurrency == 0 {
							serialID, serialCause = failure.ID, failure.Cause
						} else if failure.ID != serialID || failure.Cause != serialCause {
							t.Fatal("parallel failure verdict differs from serial admission", err)
						}
					}
					if mode == "rv-only" && Map(fixture.requests[first]["metadata"])["resourceVersion"] != "5" {
						t.Fatal("admission did not use the freshly read resource version")
					}
				}
			})
		}
	}
}

func TestParallelAdmissionKeepsRetainedAndRetiredOwnerFences(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, index := range []int{1, 3} {
			for _, mode := range []string{"owner", "baseline", "missing", "rv-only"} {
				t.Run(fmt.Sprintf("%s/resource-%d/%s", environment, index, mode), func(t *testing.T) {
					plan, fixture := parallelAdmissionPlan(t, environment, 16)
					id := plan.Resources[index].ID
					fixture.readHook = func(_ context.Context, selected string, object Object) (Object, error) {
						if selected == id {
							switch mode {
							case "owner":
								Map(object["metadata"])["uid"] = "replacement"
							case "baseline":
								Map(object["spec"])["replicas"] = float64(99)
							case "missing":
								return nil, nil
							case "rv-only":
								Map(object["metadata"])["resourceVersion"] = "5"
							}
						}
						return object, nil
					}
					before := plan.SHA256()
					err := plan.Admit(context.Background(), &parallelAdmissionOptIn{parallelAdmissionFixture: fixture, concurrency: 8})
					if mode == "rv-only" {
						if err != nil || index == 3 && Map(fixture.requests[id]["metadata"])["resourceVersion"] != "5" || index == 1 && fixture.requests[id] != nil {
							t.Fatal("retain/retire did not preserve fresh RV semantics", err)
						}
					} else {
						var failure *Blocker
						if !errors.As(err, &failure) || failure.ID != "deployment.drift" || failure.Resource != id || fixture.requests[id] != nil {
							t.Fatal("retained/retired owner was not fenced before dry-run", err)
						}
					}
					if plan.SHA256() != before || fixture.active != 0 || fixture.falseWrites != 0 {
						t.Fatal("retain/retire admission changed the plan or persisted data")
					}
				})
			}
		}
	}
}

func TestParallelAdmissionRetainsNativeDaemonSetNormalization(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, counter := range []string{"2", "3"} {
			t.Run(environment+"/counter-"+counter, func(t *testing.T) {
				plan, fixture := parallelAdmissionPlan(t, environment, 16)
				_, definition, live := daemonSetGenerationFixture(t, environment, "1")
				delete(fixture.objects, plan.Resources[0].ID)
				id := ID(live)
				plan.Resources[0] = Resource{ID: id, Phase: "workload", Action: "update", UID: "owner", Desired: Projection(definition.Object), Baseline: Projection(live)}
				fixture.objects[id] = live
				fixture.writeHook = func(selected string, object Object) Object {
					if selected == id {
						Map(Map(object["metadata"])["annotations"])[daemonSetGenerationAnnotation] = counter
						Map(object["metadata"])["generation"] = float64(2)
					}
					return object
				}
				beforePlan, beforeObjects := plan.SHA256(), Digest(fixture.objects)
				err := plan.Admit(context.Background(), &parallelAdmissionOptIn{parallelAdmissionFixture: fixture, concurrency: 8})
				if (err == nil) != (counter == "2") || plan.SHA256() != beforePlan || Digest(fixture.objects) != beforeObjects || Map(Map(fixture.requests[id]["metadata"])["annotations"])[daemonSetGenerationAnnotation] != "1" {
					t.Fatal("parallel admission rewrote or weakened the native counter rule", err)
				}
			})
		}
	}
}

type parallelExecutionFixture struct {
	mu sync.Mutex
	memoryBackend
	progress [][2]int
	trace    []string
}

func (*parallelExecutionFixture) AdmissionConcurrency() int { return 8 }
func (b *parallelExecutionFixture) AdmissionProgress(_ context.Context, completed, total int) {
	b.progress = append(b.progress, [2]int{completed, total})
}
func (b *parallelExecutionFixture) Read(ctx context.Context, id string) (Object, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.memoryBackend.Read(ctx, id)
}
func (b *parallelExecutionFixture) Write(ctx context.Context, action string, body Object, dry bool) (Object, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if !dry {
		b.trace = append(b.trace, "write:"+ID(body))
	}
	return b.memoryBackend.Write(ctx, action, body, dry)
}
func (b *parallelExecutionFixture) Wait(ctx context.Context, resource Resource) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.trace = append(b.trace, "wait:"+resource.ID)
	return b.memoryBackend.Wait(ctx, resource)
}

func TestParallelAdmissionDoesNotParallelizePersistedExecution(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			in, _, _ := fixture(t, environment, "Deployment")
			var definitions []Definition
			var originals []Object
			objects := map[string]Object{}
			for index := 0; index < 12; index++ {
				_, definition, live := fixture(t, environment, "Deployment")
				for _, object := range []Object{definition.Object, live} {
					Map(object["metadata"])["name"] = fmt.Sprintf("owned-%02d", index)
				}
				Map(definition.Object["spec"])["replicas"] = float64(2)
				definitions, originals = append(definitions, definition), append(originals, live)
				objects[ID(live)] = Clone(live)
			}
			plan, err := Compile(in, definitions, originals)
			if err != nil {
				t.Fatal(err)
			}
			backend := &parallelExecutionFixture{memoryBackend: memoryBackend{objects: objects}}
			qualified := func(context.Context, *Plan) error { return nil }
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			before := plan.SHA256()
			if err := plan.Execute(context.Background(), backend, before, root, qualified, qualified, qualified); err != nil {
				t.Fatal("opt-in admission changed the execution contract", err)
			}
			var expected []string
			for _, resource := range plan.Resources {
				expected = append(expected, "write:"+resource.ID, "wait:"+resource.ID)
			}
			if !reflect.DeepEqual(backend.trace, expected) || plan.SHA256() != before || backend.leaseHeld {
				t.Fatal("parallel admission changed persisted write/wait order or execution ownership")
			}
			assertAdmissionProgress(t, backend.progress, len(plan.Resources), len(plan.Resources))
		})
	}
}
