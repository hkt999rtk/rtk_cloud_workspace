package deploymentplan

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type classifiedReadBackend struct {
	memoryBackend
	failRead, reads int
	err             error
}

func (b *classifiedReadBackend) Read(ctx context.Context, id string) (Object, error) {
	b.reads++
	if b.reads == b.failRead {
		return nil, b.err
	}
	return b.memoryBackend.Read(ctx, id)
}

func safeReadFailure(id string) error {
	return fmt.Errorf("private-api-output: %w", &Blocker{ID: "deployment.access.resource", Resource: id, Cause: "cannot read the selected resource [KUBE_UNAVAILABLE]", Next: "verify selected API connectivity"})
}

func assertSafeReadFailure(t *testing.T, err error, id string) {
	t.Helper()
	var failure *Blocker
	if !errors.As(err, &failure) || failure.ID != "deployment.access.resource" || failure.Resource != id || !strings.Contains(failure.Cause, "[KUBE_UNAVAILABLE]") || strings.Contains(err.Error(), "private-api-output") {
		t.Fatal("read boundary discarded safe classification or exposed an outer private wrapper", err)
	}
}

func TestReadAccessClassificationSurvivesAdmissionAndExecutionBoundaries(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		// Reads occur in admission, stage dependency, exact update, both
		// post-deploy verifications, and the final ownership fence.
		for failRead := 1; failRead <= 6; failRead++ {
			t.Run(fmt.Sprintf("%s/read-%d", environment, failRead), func(t *testing.T) {
				in, definition, live := fixture(t, environment, "Deployment")
				Map(definition.Object["spec"])["replicas"] = float64(2)
				plan, err := Compile(in, []Definition{definition}, []Object{live})
				if err != nil {
					t.Fatal(err)
				}
				id := ID(live)
				backend := &classifiedReadBackend{memoryBackend: memoryBackend{objects: map[string]Object{id: Clone(live)}}, failRead: failRead, err: safeReadFailure(id)}
				before := plan.SHA256()
				root := t.TempDir()
				if err := os.Chmod(root, 0700); err != nil {
					t.Fatal(err)
				}
				qualified := func(context.Context, *Plan) error { return nil }
				err = plan.Execute(context.Background(), backend, before, root, qualified, qualified, qualified)
				assertSafeReadFailure(t, err, id)
				if plan.SHA256() != before || backend.reads != failRead {
					t.Fatal("failed read changed the plan or continued into dependent operations")
				}
				if failRead <= 3 && len(backend.written) != 0 {
					t.Fatal("failed initial resource read persisted an update")
				}
				if failRead >= 4 {
					if len(backend.written) != 1 || !backend.leaseHeld {
						t.Fatal("post-write read failure discarded the completed update or ownership fence")
					}
					if _, err := os.Stat(filepath.Join(root, "deployment", ".execution-active")); err != nil {
						t.Fatal("post-write read failure lost private recovery fence", err)
					}
				}
			})
		}
	}
}

func TestReadAccessClassificationSurvivesVerificationAndRecovery(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"verify", "verify-retired", "reconcile-unstarted", "reconcile-accepted", "reconcile-retired"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				in, definition, live := fixture(t, environment, "Deployment")
				plan, err := Compile(in, []Definition{definition}, []Object{live})
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(mode, "retired") {
					plan.Resources[0].Action = "retire"
				}
				id := ID(live)
				backend := &classifiedReadBackend{memoryBackend: memoryBackend{objects: map[string]Object{id: Clone(live)}}, failRead: 1, err: safeReadFailure(id)}
				before := plan.SHA256()
				if strings.HasPrefix(mode, "verify") {
					err = plan.Verify(context.Background(), backend)
				} else {
					journal := Journal{PlanSHA256: before, Owners: map[string]string{}}
					if mode != "reconcile-unstarted" {
						journal.Accepted, journal.AcceptanceStarted = true, true
						journal.Completed, journal.Owners = []string{id}, map[string]string{id: "owner"}
					}
					_, err = plan.ReconcileOutcome(context.Background(), backend, journal)
				}
				assertSafeReadFailure(t, err, id)
				if plan.SHA256() != before || backend.reads != 1 || len(backend.written) != 0 {
					t.Fatal("unreadable verification/recovery mutated or accepted the target")
				}
			})
		}
	}
}

func TestReadAccessBoundaryRejectsUnclassifiedAndUnrelatedDiagnostics(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"untyped", "wrong-resource", "wrong-id", "storage-safe"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				in, definition, live := fixture(t, environment, "Deployment")
				plan, err := Compile(in, []Definition{definition}, []Object{live})
				if err != nil {
					t.Fatal(err)
				}
				id := ID(live)
				failure := error(errors.New("private-api-output"))
				switch mode {
				case "wrong-resource":
					failure = &Blocker{ID: "deployment.access.resource", Resource: "Secret/another/owner", Cause: "private-api-output"}
				case "wrong-id":
					failure = &Blocker{ID: "deployment.drift", Resource: id, Cause: "private-api-output"}
				case "storage-safe":
					failure = fmt.Errorf("private-api-output: %w", &Blocker{ID: "deployment.access.storage", Resource: id, Cause: "cannot read original storage owner [KUBE_FORBIDDEN]", Next: "restore original read access"})
				}
				backend := &classifiedReadBackend{memoryBackend: memoryBackend{objects: map[string]Object{id: Clone(live)}}, failRead: 1, err: failure}
				err = plan.Admit(context.Background(), backend)
				var blocker *Blocker
				want := "deployment.access"
				if mode == "storage-safe" {
					want = "deployment.access.storage"
				}
				if !errors.As(err, &blocker) || blocker.ID != want || blocker.Resource != id || strings.Contains(err.Error(), "private-api-output") || len(backend.dry) != 0 || len(backend.written) != 0 {
					t.Fatal("read boundary exposed unrelated diagnostics or advanced admission", err)
				}
			})
		}
	}
}
