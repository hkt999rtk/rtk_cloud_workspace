package deploymentplan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func fixture(t *testing.T, environment, kind string) (Inputs, Definition, Object) {
	t.Helper()
	stack := "video-cloud-" + environment
	wanted := Object{"apiVersion": "apps/v1", "kind": kind, "metadata": Object{"name": "owned", "namespace": stack + "-video-cloud", "labels": Object{"rtk.realtek.com/stack": stack, "app.kubernetes.io/managed-by": "rtk-cloud"}}, "spec": Object{"replicas": float64(1), "selector": Object{"matchLabels": Object{"app": "owned"}}, "template": Object{"spec": Object{"containers": []any{Object{"name": "owned", "image": "registry.example/owned@sha256:" + strings.Repeat("a", 64)}}}}}}
	live := Clone(wanted)
	Map(live["metadata"])["uid"] = "owner"
	Map(live["metadata"])["resourceVersion"] = "4"
	live["status"] = Object{"readyReplicas": float64(1)}
	return Inputs{Environment: environment, Stack: stack, Operation: "upgrade", ConfigurationSHA256: Digest("configuration"), ReleaseSHA256: Digest("release"), BindingsSHA256: Digest("bindings")}, Definition{Object: wanted, Phase: "workload"}, live
}

func TestAllEnvironmentsUseSameCompleteResourcePlan(t *testing.T) {
	for _, env := range []string{"dev", "staging", "prod"} {
		t.Run(env, func(t *testing.T) {
			for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "Service", "ConfigMap", "NetworkPolicy", "Ingress"} {
				in, definition, live := fixture(t, env, kind)
				Map(definition.Object["spec"])["replicas"] = float64(2)
				plan, err := Compile(in, []Definition{definition}, []Object{live})
				if err != nil {
					t.Fatal(err)
				}
				if len(plan.Resources) != 1 || plan.Resources[0].Action != "update" {
					t.Fatal("desired update was not planned")
				}
				body, err := plan.Resources[0].Body(live)
				if err != nil {
					t.Fatal(err)
				}
				if Map(body["metadata"])["resourceVersion"] != "4" || body["status"] != nil {
					t.Fatal("API identity fence lost")
				}
				if Map(live["spec"])["replicas"] != float64(1) {
					t.Fatal("live state became desired state")
				}
			}
		})
	}
}

func TestUnknownOwnerAndImmutableMigrationFailClosed(t *testing.T) {
	in, definition, live := fixture(t, "dev", "StatefulSet")
	foreign := Clone(live)
	Map(foreign["metadata"])["name"] = "unregistered"
	if _, err := Compile(in, []Definition{definition}, []Object{live, foreign}); err == nil {
		t.Fatal("unknown controller passed")
	}
	Map(definition.Object["spec"])["serviceName"] = "changed"
	Map(live["spec"])["serviceName"] = "original"
	if _, err := Compile(in, []Definition{definition}, []Object{live}); err == nil {
		t.Fatal("immutable owner change passed")
	}
	Map(live["metadata"])["labels"] = Object{"rtk.realtek.com/stack": "video-cloud-staging"}
	if _, err := Compile(in, []Definition{definition}, []Object{live}); err == nil {
		t.Fatal("foreign owner passed")
	}
}

func TestRetainedAllocatedDependenciesKeepOriginalStateWithoutRenderingSpecs(t *testing.T) {
	for _, env := range []string{"dev", "staging", "prod"} {
		for _, kind := range []string{"Service", "PersistentVolumeClaim"} {
			t.Run(env+"/"+kind, func(t *testing.T) {
				in, definition, live := fixture(t, env, kind)
				definition.Retain = true
				definition.Object["apiVersion"], live["apiVersion"] = "v1", "v1"
				delete(definition.Object, "spec")
				allocation := "volumeName"
				if kind == "Service" {
					allocation = "clusterIP"
				}
				Map(live["spec"])[allocation] = "original-allocation"
				before := Digest(live)
				plan, err := Compile(in, []Definition{definition}, []Object{live})
				if err != nil || len(plan.Resources) != 1 {
					t.Fatal("retained lifecycle dependency could not be planned", err)
				}
				r := plan.Resources[0]
				body, err := r.Body(live)
				if err != nil || body != nil || r.Action != "retain" || r.Desired["spec"] != nil || Digest(live) != before {
					t.Fatal("retained dependency was rendered or changed", err)
				}
				changed := Clone(live)
				Map(changed["spec"])[allocation] = "replaced-allocation"
				if _, err := r.Body(changed); err == nil {
					t.Fatal("retained original allocation drift was accepted")
				}
			})
		}
	}
}

func TestUnknownJobsAndPodsRequireExactDeclaredControllerLineage(t *testing.T) {
	for _, env := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid", "no-owner", "wrong-uid", "foreign-stack", "wrong-parent-kind", "duplicate-controller", "parent-not-declared", "new-unknown-job"} {
			t.Run(env+"/"+mode, func(t *testing.T) {
				in, d, parent := fixture(t, env, "Deployment")
				metadata := Map(parent["metadata"])
				namespace := String(metadata["namespace"])
				ref := func(kind, name, uid, api string) Object {
					return Object{"kind": kind, "name": name, "uid": uid, "apiVersion": api, "controller": true}
				}
				rs := Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": Object{"name": "generated", "namespace": namespace, "uid": "rs-owner", "ownerReferences": []any{ref("Deployment", "owned", "owner", "apps/v1")}}}
				pod := Object{"apiVersion": "v1", "kind": "Pod", "metadata": Object{"name": "generated-pod", "namespace": namespace, "uid": "pod-owner", "ownerReferences": []any{ref("ReplicaSet", "generated", "rs-owner", "apps/v1")}}}
				objects := []Object{parent, rs, pod}
				switch mode {
				case "no-owner":
					delete(Map(pod["metadata"]), "ownerReferences")
				case "wrong-uid":
					Map(Map(pod["metadata"])["ownerReferences"].([]any)[0])["uid"] = "replacement"
				case "foreign-stack":
					Map(pod["metadata"])["labels"] = Object{"rtk.realtek.com/stack": "another"}
				case "wrong-parent-kind":
					Map(Map(pod["metadata"])["ownerReferences"].([]any)[0])["kind"] = "Deployment"
				case "duplicate-controller":
					Map(pod["metadata"])["ownerReferences"] = []any{ref("ReplicaSet", "generated", "rs-owner", "apps/v1"), ref("ReplicaSet", "generated", "rs-owner", "apps/v1")}
				case "parent-not-declared":
					Map(rs["metadata"])["ownerReferences"] = []any{ref("Deployment", "absent", "absent-owner", "apps/v1")}
				case "new-unknown-job":
					objects = append(objects, Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": Object{"name": "unknown-job", "namespace": namespace, "uid": "job-owner"}})
				}
				p, err := Compile(in, []Definition{d}, objects)
				if (err == nil) != (mode == "valid") {
					t.Fatal("lineage decision changed", mode, err)
				}
				if mode == "valid" {
					if len(p.Resources) != 1 || p.CheckWorkloadInventory(objects) != nil {
						t.Fatal("derived children became renderer input")
					}
					objects = append(objects, Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": Object{"name": "new-job", "namespace": namespace, "uid": "new-owner"}})
					if p.CheckWorkloadInventory(objects) == nil {
						t.Fatal("unknown job introduced after planning was allowed")
					}
				}
			})
		}
	}
}

func TestServiceAllocationAndConfigurationDrift(t *testing.T) {
	in, d, live := fixture(t, "staging", "Service")
	Map(live["spec"])["clusterIP"] = "10.0.0.1"
	p, err := Compile(in, []Definition{d}, []Object{live})
	if err != nil {
		t.Fatal(err)
	}
	if Map(p.Resources[0].Desired["spec"])["clusterIP"] != "10.0.0.1" {
		t.Fatal("allocation lost")
	}
	Map(live["metadata"])["resourceVersion"] = "5"
	if _, err = p.Resources[0].Body(live); err != nil {
		t.Fatal("status-only version change rejected", err)
	}
	Map(live["spec"])["replicas"] = float64(9)
	if _, err = p.Resources[0].Body(live); err == nil {
		t.Fatal("configuration drift passed")
	}
}

type memoryBackend struct {
	objects          map[string]Object
	dry              []Object
	written          []Object
	failID           string
	drift            bool
	replaceAfterWait bool
	failWait         bool
	leaseHeld        bool
}

type memoryExecutionLease struct {
	backend *memoryBackend
	plan    *Plan
}

func (b *memoryBackend) AcquireExecution(_ context.Context, p *Plan) (ExecutionLease, error) {
	if b.leaseHeld {
		return nil, errors.New("another controller owns the cluster lease")
	}
	b.leaseHeld = true
	return &memoryExecutionLease{b, p}, nil
}
func (l *memoryExecutionLease) Owner() LeaseOwner {
	return LeaseOwner{"Lease/" + l.plan.Stack + "-video-cloud/rtk-deployment-execution", "fixture-owner", l.plan.SHA256() + ":fixture"}
}
func (l *memoryExecutionLease) Check(context.Context) error {
	if !l.backend.leaseHeld {
		return errors.New("lease lost")
	}
	return nil
}
func (l *memoryExecutionLease) Release(ctx context.Context) error {
	if err := l.Check(ctx); err != nil {
		return err
	}
	l.backend.leaseHeld = false
	return nil
}

func TestPartialMutationReleasesExecutionLocksAndPreservesEvidenceAcrossControllers(t *testing.T) {
	in, d, _ := fixture(t, "dev", "Deployment")
	p, err := Compile(in, []Definition{d}, nil)
	if err != nil {
		t.Fatal(err)
	}
	backend := &memoryBackend{objects: map[string]Object{}, failWait: true}
	qualified := func(context.Context, *Plan) error { return nil }
	firstRoot := t.TempDir()
	if err := os.Chmod(firstRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := p.Execute(context.Background(), backend, p.SHA256(), firstRoot, qualified, qualified, func(context.Context, *Plan) error { return nil }); err == nil {
		t.Fatal("failed rollout reported success")
	}
	if backend.leaseHeld {
		t.Fatal("stopped partial mutation retained cluster execution")
	}
	if _, err := os.Stat(filepath.Join(firstRoot, "deployment", ".execution-active")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("stopped partial mutation retained its active local lock", err)
	}
	journalPath := filepath.Join(firstRoot, "deployment", "rollback", p.SHA256(), "journal.json")
	before, err := os.ReadFile(journalPath)
	if err != nil {
		t.Fatal("partial mutation lost its private journal", err)
	}
	in.ConfigurationSHA256 = Digest("second controller config")
	second, err := Compile(in, []Definition{d}, []Object{backend.objects[ID(d.Object)]})
	if err != nil {
		t.Fatal(err)
	}
	writes := len(backend.written)
	backend.failWait = false
	secondRoot := t.TempDir()
	if err := os.Chmod(secondRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := second.Execute(context.Background(), backend, second.SHA256(), secondRoot, qualified, qualified, func(context.Context, *Plan) error { return nil }); err != nil || len(backend.written) != writes || backend.leaseHeld {
		t.Fatal("fresh plan could not qualify the actual target after the old controller stopped", err)
	}
	after, err := os.ReadFile(journalPath)
	if err != nil || string(after) != string(before) {
		t.Fatal("fresh execution changed the original partial journal", err)
	}
}

type mutatingAdmissionBackend struct {
	memoryBackend
	mode string
}

func (b *mutatingAdmissionBackend) Write(ctx context.Context, action string, body Object, dry bool) (Object, error) {
	actual, err := b.memoryBackend.Write(ctx, action, body, dry)
	if !dry || err != nil {
		return actual, err
	}
	actual = Clone(actual)
	containers := Map(Map(Map(actual["spec"])["template"])["spec"])["containers"].([]any)
	switch b.mode {
	case "image":
		Map(containers[0])["image"] = "registry.example/unqualified:latest"
	case "sidecar":
		pod := Map(Map(Map(actual["spec"])["template"])["spec"])
		pod["containers"] = append(containers, Object{"name": "unqualified", "image": "unknown:latest"})
	case "resource":
		Map(actual["metadata"])["name"] = "another-owner"
	case "empty":
		return nil, nil
	}
	return actual, nil
}

func TestAdmissionCannotSubstituteImageConsumerOrResource(t *testing.T) {
	for _, mode := range []string{"image", "sidecar", "resource", "empty"} {
		t.Run(mode, func(t *testing.T) {
			in, d, _ := fixture(t, "dev", "Deployment")
			p, err := Compile(in, []Definition{d}, nil)
			if err != nil {
				t.Fatal(err)
			}
			backend := &mutatingAdmissionBackend{memoryBackend: memoryBackend{objects: map[string]Object{}}, mode: mode}
			root := t.TempDir()
			os.Chmod(root, 0700)
			qualified := func(context.Context, *Plan) error { return nil }
			if p.Admit(context.Background(), backend) == nil || p.Execute(context.Background(), backend, p.SHA256(), root, qualified, qualified, func(context.Context, *Plan) error { return nil }) == nil {
				t.Fatal("different server candidate admitted")
			}
			if len(backend.written) != 0 {
				t.Fatal("unqualified admission mutation persisted")
			}
		})
	}
}

func TestLaterStageStopsWhenCompletedDependencyChanges(t *testing.T) {
	for _, mode := range []string{"desired-drift", "owner-replaced", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			in, first, _ := fixture(t, "dev", "Deployment")
			first.Phase = "configuration"
			second := Definition{Object: Clone(first.Object), Phase: "workload", Dependencies: []string{ID(first.Object)}}
			Map(second.Object["metadata"])["name"] = "dependent"
			p, err := Compile(in, []Definition{first, second}, nil)
			if err != nil {
				t.Fatal(err)
			}
			backend := &memoryBackend{objects: map[string]Object{}}
			root := t.TempDir()
			os.Chmod(root, 0700)
			stages := 0
			fence := func(context.Context, *Plan) error {
				stages++
				if stages == 2 {
					switch mode {
					case "desired-drift":
						Map(backend.objects[ID(first.Object)]["spec"])["replicas"] = float64(99)
					case "owner-replaced":
						Map(backend.objects[ID(first.Object)]["metadata"])["uid"] = "concurrent-owner"
					case "deleted":
						delete(backend.objects, ID(first.Object))
					}
				}
				return nil
			}
			if p.Execute(context.Background(), backend, p.SHA256(), root, func(context.Context, *Plan) error { return nil }, fence, func(context.Context, *Plan) error { return nil }) == nil {
				t.Fatal("completed dependency drift accepted")
			}
			if len(backend.written) != 1 || backend.objects[ID(second.Object)] != nil {
				t.Fatal("dependent stage updated after drift")
			}
			raw, _ := os.ReadFile(filepath.Join(root, "deployment", "rollback", p.SHA256(), "journal.json"))
			var journal Journal
			if json.Unmarshal(raw, &journal) != nil || journal.Failed != ID(first.Object) || len(journal.Completed) != 1 {
				t.Fatal("completed-stage failure not retained")
			}
		})
	}
}

func (b *memoryBackend) Read(_ context.Context, id string) (Object, error) {
	if b.drift {
		Map(b.objects[id]["spec"])["replicas"] = float64(8)
	}
	return Clone(b.objects[id]), nil
}
func (b *memoryBackend) Write(_ context.Context, action string, body Object, dry bool) (Object, error) {
	if ID(body) == b.failID {
		return nil, errors.New("admission rejected")
	}
	if dry {
		b.dry = append(b.dry, Clone(body))
		return body, nil
	}
	b.written = append(b.written, Clone(body))
	if action == "retire" {
		delete(b.objects, ID(body))
		return body, nil
	}
	b.objects[ID(body)] = Clone(body)
	if action == "create" {
		Map(b.objects[ID(body)]["metadata"])["uid"] = "new-owner"
	}
	return Clone(b.objects[ID(body)]), nil
}
func (b *memoryBackend) Wait(_ context.Context, r Resource) error {
	if b.failWait {
		return errors.New("workload did not become ready")
	}
	if b.replaceAfterWait && b.objects[r.ID] != nil {
		Map(b.objects[r.ID]["metadata"])["uid"] = "concurrent-owner"
	}
	return nil
}

type delayedStorageBackend struct {
	*memoryBackend
	consumerReady bool
	unbound       bool
	storageWaits  int
}

func (b *delayedStorageBackend) Wait(ctx context.Context, r Resource) error {
	switch String(r.Desired["kind"]) {
	case "Deployment":
		b.consumerReady = true
	case "PersistentVolumeClaim":
		b.storageWaits++
		if !b.consumerReady || b.unbound {
			return errors.New("volume awaits a scheduled consumer")
		}
	}
	return b.memoryBackend.Wait(ctx, r)
}

func TestDelayedVolumeBindingWaitsAfterConsumerAndBlocksIncompleteStorage(t *testing.T) {
	for _, env := range []string{"dev", "staging", "prod"} {
		for _, unbound := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/unbound=%t", env, unbound), func(t *testing.T) {
				in, consumer, _ := fixture(t, env, "Deployment")
				claim := Definition{Phase: "storage", Object: Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": Object{"name": "state", "namespace": in.Stack + "-video-cloud"}, "spec": Object{"storageClassName": "delayed", "accessModes": []any{"ReadWriteOnce"}, "resources": Object{"requests": Object{"storage": "1Gi"}}}}}
				consumer.Dependencies = []string{ID(claim.Object)}
				Map(Map(consumer.Object["spec"])["template"])["spec"].(Object)["volumes"] = []any{Object{"name": "state", "persistentVolumeClaim": Object{"claimName": "state"}}}
				p, err := Compile(in, []Definition{claim, consumer}, nil)
				if err != nil {
					t.Fatal(err)
				}
				backend := &delayedStorageBackend{memoryBackend: &memoryBackend{objects: map[string]Object{}}, unbound: unbound}
				root := t.TempDir()
				if err := os.Chmod(root, 0700); err != nil {
					t.Fatal(err)
				}
				accepts := 0
				check := func(context.Context, *Plan) error { return nil }
				err = p.Execute(context.Background(), backend, p.SHA256(), root, check, check, func(context.Context, *Plan) error { accepts++; return nil })
				if !backend.consumerReady || backend.storageWaits == 0 {
					t.Fatal("storage was waited before consumer rollout or never verified")
				}
				if unbound {
					var blocker *Blocker
					if !errors.As(err, &blocker) || blocker.ID != "deployment.storage.binding" || accepts != 0 || backend.leaseHeld {
						t.Fatal("unbound storage completed deployment or retained its execution lock", err)
					}
					raw, readErr := os.ReadFile(filepath.Join(root, "deployment", "rollback", p.SHA256(), "journal.json"))
					var journal Journal
					if readErr != nil || json.Unmarshal(raw, &journal) != nil || journal.Failed != ID(claim.Object) || journal.Accepted {
						t.Fatal("the failed storage owner was not recorded for recovery")
					}
				} else if err != nil || accepts != 1 || backend.leaseHeld {
					t.Fatal("bound delayed storage did not complete", err)
				}
			})
		}
	}
}

func TestFailedWaitPreservesAppliedOwnerAndRejectsJournalOverwrite(t *testing.T) {
	in, d, _ := fixture(t, "dev", "Deployment")
	p, err := Compile(in, []Definition{d}, nil)
	if err != nil {
		t.Fatal(err)
	}
	backend := &memoryBackend{objects: map[string]Object{}, failWait: true}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	qualified := func(context.Context, *Plan) error { return nil }
	if p.Execute(context.Background(), backend, p.SHA256(), root, qualified, qualified, func(context.Context, *Plan) error { return nil }) == nil {
		t.Fatal("failed readiness marked complete")
	}
	path := filepath.Join(root, "deployment", "rollback", p.SHA256(), "journal.json")
	raw, err := os.ReadFile(path)
	var journal Journal
	if err != nil || json.Unmarshal(raw, &journal) != nil || len(journal.Owners) != 1 || journal.Owners[ID(d.Object)] != "new-owner" || journal.Failed != ID(d.Object) || len(journal.Completed) != 1 || journal.Completed[0] != ID(d.Object) || journal.InFlight != "" || journal.AcceptanceStarted || journal.Accepted {
		t.Fatal("applied but unready owner missing from recovery evidence", err)
	}
	// A second command must not erase the first command's partial result even
	// if an operator erroneously asks to reuse the same plan hash.
	delete(backend.objects, ID(d.Object))
	backend.failWait = false
	if p.Execute(context.Background(), backend, p.SHA256(), root, qualified, qualified, func(context.Context, *Plan) error { return nil }) == nil {
		t.Fatal("earlier journal overwritten")
	}
	after, _ := os.ReadFile(path)
	if string(raw) != string(after) || len(backend.written) != 1 || backend.leaseHeld {
		t.Fatal("recovery evidence or original resource outcome changed, or rejected retry retained its lease")
	}
	if _, err := os.Stat(filepath.Join(root, "deployment", ".execution-active")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("rejected same-plan retry retained its active local lock", err)
	}
}

func TestDifferentPlansCannotOverlapEnvironmentExecution(t *testing.T) {
	in, d, _ := fixture(t, "dev", "Deployment")
	p, err := Compile(in, []Definition{d}, nil)
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
	owner := []byte(`{"plan_sha256":"another-reviewed-plan"}`)
	if err := os.WriteFile(filepath.Join(active, "owner.json"), owner, 0600); err != nil {
		t.Fatal(err)
	}
	backend := &memoryBackend{objects: map[string]Object{}}
	qualified := func(context.Context, *Plan) error { return nil }
	if err := p.Execute(context.Background(), backend, p.SHA256(), root, qualified, qualified, func(context.Context, *Plan) error { return nil }); err == nil {
		t.Fatal("different plan bypassed environment execution lock")
	}
	if len(backend.written) != 0 {
		t.Fatal("overlapping plan persisted a resource")
	}
	after, err := os.ReadFile(filepath.Join(active, "owner.json"))
	if err != nil || string(after) != string(owner) {
		t.Fatal("other execution lock was removed or overwritten")
	}
}

func TestSecretPlanUsesPersistedRepresentationForAdmissionAndReadback(t *testing.T) {
	in, _, _ := fixture(t, "dev", "Secret")
	object := Object{"apiVersion": "v1", "kind": "Secret", "metadata": Object{"name": "archive", "namespace": in.Stack + "-platform"}, "type": "Opaque", "data": Object{"same-key": "b2xk"}, "stringData": Object{"same-key": "new", "private-key": "private fixture"}}
	p, err := Compile(in, []Definition{{Object: object, Phase: "configuration"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, exists := p.Resources[0].Desired["stringData"]; exists {
		t.Fatal("write-only field remained in persisted target")
	}
	if Map(p.Resources[0].Desired["data"])["same-key"] != "bmV3" {
		t.Fatal("stringData did not override encoded data")
	}
	if object["stringData"] == nil {
		t.Fatal("compiler changed caller's private input")
	}
	b := &memoryBackend{objects: map[string]Object{}}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	qualified := func(context.Context, *Plan) error { return nil }
	if err := p.Execute(context.Background(), b, p.SHA256(), root, qualified, qualified, func(context.Context, *Plan) error { return nil }); err != nil {
		t.Fatal("persisted Secret could not be verified", err)
	}
	public, _ := json.Marshal(p.Summary())
	if strings.Contains(string(public), "private fixture") || strings.Contains(string(public), "cHJpdmF0ZSBmaXh0dXJl") {
		t.Fatal("public summary leaked Secret payload")
	}
}

func TestControlledRetirementFencesUIDAndVersionAndPreservesData(t *testing.T) {
	for _, env := range []string{"dev", "staging", "prod"} {
		in, d, live := fixture(t, env, "Deployment")
		d.Object = Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": Object{"name": "owned", "namespace": in.Stack + "-video-cloud"}}
		d.Retire, d.Phase = true, "retirement"
		p, err := Compile(in, []Definition{d}, []Object{live})
		if err != nil {
			t.Fatal(err)
		}
		body, err := p.Resources[0].Body(live)
		if err != nil || Map(body["metadata"])["uid"] != "owner" || Map(body["metadata"])["resourceVersion"] != "4" {
			t.Fatal("retirement lost concurrency fences", err)
		}
		b := &memoryBackend{objects: map[string]Object{ID(live): live}}
		root := t.TempDir()
		os.Chmod(root, 0700)
		if err := p.Execute(context.Background(), b, p.SHA256(), root, func(context.Context, *Plan) error { return nil }, func(context.Context, *Plan) error { return nil }, func(context.Context, *Plan) error { return nil }); err != nil || len(b.objects) != 0 {
			t.Fatal("retirement failed", err)
		}
		for _, kind := range []string{"Secret", "PersistentVolumeClaim", "Namespace", "ConfigMap"} {
			d.Object["kind"] = kind
			if _, err := Compile(in, []Definition{d}, nil); err == nil {
				t.Fatal("ordinary retirement deleted lifecycle-owned state", kind)
			}
		}
	}
}

func TestCronJobRetirementPreservesOriginalOwnerAndRejectsDrift(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			in, _, _ := fixture(t, environment, "Deployment")
			live := Object{"apiVersion": "batch/v1", "kind": "CronJob", "metadata": Object{"name": "scheduled-worker", "namespace": in.Stack + "-platform", "uid": "original-worker", "resourceVersion": "4", "labels": Object{"rtk.realtek.com/stack": in.Stack}}, "spec": Object{"schedule": "*/15 * * * *", "suspend": false}}
			d := Definition{Phase: "retirement", Retire: true, Object: Object{"apiVersion": "batch/v1", "kind": "CronJob", "metadata": Object{"name": "scheduled-worker", "namespace": in.Stack + "-platform"}}}
			plan, err := Compile(in, []Definition{d}, []Object{live})
			if err != nil || plan.Resources[0].Action != "retire" {
				t.Fatal("explicit scheduled worker retirement was rejected", err)
			}
			body, err := plan.Resources[0].Body(live)
			if err != nil || Map(body["metadata"])["uid"] != "original-worker" || Map(body["metadata"])["resourceVersion"] != "4" {
				t.Fatal("scheduled worker owner fence was lost", err)
			}
			changed := Clone(live)
			Map(changed["metadata"])["uid"] = "replacement-worker"
			if _, err := plan.Resources[0].Body(changed); err == nil {
				t.Fatal("replacement scheduled worker could be retired")
			}
		})
	}
}

func TestNewOwnerIsJournaledAndConcurrentReplacementStopsCompletion(t *testing.T) {
	for _, replace := range []bool{false, true} {
		in, d, _ := fixture(t, "dev", "Deployment")
		in.Operation = "create"
		p, err := Compile(in, []Definition{d}, nil)
		if err != nil {
			t.Fatal(err)
		}
		b := &memoryBackend{objects: map[string]Object{}, replaceAfterWait: replace}
		root := t.TempDir()
		os.Chmod(root, 0700)
		err = p.Execute(context.Background(), b, p.SHA256(), root, func(context.Context, *Plan) error { return nil }, func(context.Context, *Plan) error { return nil }, func(context.Context, *Plan) error { return nil })
		if (err != nil) != replace {
			t.Fatal("new owner completion fence differs", err)
		}
		var journal Journal
		raw, _ := os.ReadFile(filepath.Join(root, "deployment", "rollback", p.SHA256(), "journal.json"))
		if json.Unmarshal(raw, &journal) != nil || journal.Owners[ID(d.Object)] != "new-owner" {
			t.Fatal("new UID missing from recovery journal")
		}
	}
}

func TestAdmissionAndExecutionShareBodiesAndKeepPrivateRecovery(t *testing.T) {
	in, d, live := fixture(t, "prod", "Deployment")
	Map(d.Object["spec"])["replicas"] = float64(2)
	p, err := Compile(in, []Definition{d}, []Object{live})
	if err != nil {
		t.Fatal(err)
	}
	b := &memoryBackend{objects: map[string]Object{ID(live): live}}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	qualified := false
	err = p.Execute(context.Background(), b, p.SHA256(), root, func(context.Context, *Plan) error { qualified = true; return nil }, func(context.Context, *Plan) error { return nil }, func(context.Context, *Plan) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if !qualified || len(b.written) != 1 || Digest(b.written[0]) != Digest(b.dry[len(b.dry)-1]) {
		t.Fatal("execution differs from admission")
	}
	file := filepath.Join(root, "deployment", "rollback", p.SHA256(), "plan.json")
	info, err := os.Stat(file)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal("private rollback missing")
	}
	raw, _ := json.Marshal(p.Summary())
	if strings.Contains(string(raw), "containers") || strings.Contains(string(raw), "baseline") {
		t.Fatal("report exposed private specs")
	}
}

func TestQualificationFailureAndDriftNeverWrite(t *testing.T) {
	in, d, live := fixture(t, "dev", "Deployment")
	Map(d.Object["spec"])["replicas"] = float64(2)
	p, _ := Compile(in, []Definition{d}, []Object{live})
	b := &memoryBackend{objects: map[string]Object{ID(live): live}}
	if err := p.Execute(context.Background(), b, p.SHA256(), t.TempDir(), func(context.Context, *Plan) error { return errors.New("missing release") }, func(context.Context, *Plan) error { return nil }, func(context.Context, *Plan) error { return nil }); err == nil || len(b.written) != 0 {
		t.Fatal("failed qualification wrote resources")
	}
	b.drift = true
	if err := p.Admit(context.Background(), b); err == nil || len(b.dry) != 0 {
		t.Fatal("drift passed admission")
	}
}

func TestInputFenceStopsBetweenStagesWithoutBlindRollback(t *testing.T) {
	in, first, live := fixture(t, "prod", "Deployment")
	first.Phase = "configuration"
	Map(first.Object["spec"])["replicas"] = float64(2)
	second := Definition{Object: Clone(first.Object), Phase: "workload"}
	Map(second.Object["metadata"])["name"] = "second"
	secondLive := Clone(live)
	Map(secondLive["metadata"])["name"] = "second"
	p, err := Compile(in, []Definition{first, second}, []Object{live, secondLive})
	if err != nil {
		t.Fatal(err)
	}
	b := &memoryBackend{objects: map[string]Object{ID(live): live, ID(secondLive): secondLive}}
	root := t.TempDir()
	os.Chmod(root, 0700)
	stages := 0
	err = p.Execute(context.Background(), b, p.SHA256(), root, func(context.Context, *Plan) error { return nil }, func(context.Context, *Plan) error {
		stages++
		if stages == 2 {
			return errors.New("release drift")
		}
		return nil
	}, func(context.Context, *Plan) error { return nil })
	if err == nil || len(b.written) != 1 || Map(b.objects[ID(live)]["spec"])["replicas"] != float64(2) || Map(b.objects[ID(secondLive)]["spec"])["replicas"] != float64(1) {
		t.Fatal("phase drift wrote a later stage or rolled back prior state", err)
	}
	var journal Journal
	raw, _ := os.ReadFile(filepath.Join(root, "deployment", "rollback", p.SHA256(), "journal.json"))
	json.Unmarshal(raw, &journal)
	if len(journal.Completed) != 1 || journal.Failed != ID(secondLive) {
		t.Fatal("partial progress lost")
	}
}

func TestCreateAndDependencies(t *testing.T) {
	in, d, _ := fixture(t, "dev", "Deployment")
	in.Operation = "create"
	p, err := Compile(in, []Definition{d}, nil)
	if err != nil || p.Resources[0].Action != "create" {
		t.Fatal("new environment cannot plan", err)
	}
	d.Dependencies = []string{"missing"}
	if _, err := Compile(in, []Definition{d}, nil); err == nil {
		t.Fatal("missing dependency passed")
	}
	if _, err = p.Resources[0].Body(d.Object); err == nil {
		t.Fatal("concurrently created owner accepted")
	}
}

func TestAdoptionIsBoundToExactUIDAndCannotTakeForeignOwnership(t *testing.T) {
	in, d, live := fixture(t, "dev", "Deployment")
	delete(Map(live["metadata"]), "labels")
	if _, err := Compile(in, []Definition{d}, []Object{live}); err == nil {
		t.Fatal("unreviewed owner adopted")
	}
	if _, err := Compile(in, []Definition{d}, []Object{live}, map[string]string{ID(live): "different"}); err == nil {
		t.Fatal("wrong UID adopted")
	}
	if _, err := Compile(in, []Definition{d}, []Object{live}, map[string]string{ID(live): "owner"}); err != nil {
		t.Fatal(err)
	}
	Map(live["metadata"])["labels"] = Object{"rtk.realtek.com/stack": "video-cloud-prod"}
	if _, err := Compile(in, []Definition{d}, []Object{live}, map[string]string{ID(live): "owner"}); err == nil {
		t.Fatal("foreign owner overridden by adoption")
	}
}

func TestAPIDefaultsInsideContainersAndDependencySorting(t *testing.T) {
	in, d, live := fixture(t, "dev", "Deployment")
	container := Map(Map(Map(live["spec"])["template"])["spec"])["containers"].([]any)[0].(map[string]any)
	container["imagePullPolicy"] = "IfNotPresent"
	if !Matches(d.Object, live) {
		t.Fatal("API container default rejected")
	}
	other := Definition{Object: Clone(d.Object), Phase: "workload"}
	Map(other.Object["metadata"])["name"] = "zzz"
	d.Dependencies = []string{ID(other.Object)}
	p, err := Compile(in, []Definition{d, other}, nil)
	if err != nil || p.Resources[0].ID != ID(other.Object) {
		t.Fatal("same-stage dependency was not ordered", err)
	}
	other.Dependencies = []string{ID(d.Object)}
	if _, err := Compile(in, []Definition{d, other}, nil); err == nil {
		t.Fatal("cycle accepted")
	}
}

func TestAcceptanceKeepsDistributedFenceAndRejectsConcurrentChanges(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"success", "failed", "typed-failed", "owner-drift", "spec-drift", "lease-lost", "input-drift", "plan-drift", "missing"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				in, d, _ := fixture(t, environment, "Deployment")
				p, err := Compile(in, []Definition{d}, nil)
				if err != nil {
					t.Fatal(err)
				}
				sha := p.SHA256()
				b := &memoryBackend{objects: map[string]Object{}}
				root := t.TempDir()
				if err := os.Chmod(root, 0700); err != nil {
					t.Fatal(err)
				}
				accepted := false
				qualified := func(context.Context, *Plan) error { return nil }
				fence := func(context.Context, *Plan) error {
					if accepted && mode == "input-drift" {
						return errors.New("private-input")
					}
					return nil
				}
				accept := func(ctx context.Context, candidate *Plan) error {
					if !b.leaseHeld || len(b.written) != 1 {
						t.Fatal("acceptance ran outside execution ownership")
					}
					if _, err := os.Stat(filepath.Join(root, "deployment", ".execution-active", "owner.json")); err != nil {
						t.Fatal("local fence absent during acceptance")
					}
					if _, err := b.AcquireExecution(ctx, candidate); err == nil {
						t.Fatal("concurrent controller acquired acceptance lease")
					}
					accepted = true
					switch mode {
					case "failed":
						return errors.New("private-endpoint")
					case "typed-failed":
						return fmt.Errorf("private-endpoint: %w", &Blocker{ID: "deployment.post-deploy.workloads", Resource: ID(d.Object), Cause: "owned Pod image differs", Next: "repair the actual target"})
					case "owner-drift":
						Map(b.objects[ID(d.Object)]["metadata"])["uid"] = "replacement"
					case "spec-drift":
						Map(b.objects[ID(d.Object)]["spec"])["replicas"] = float64(99)
					case "lease-lost":
						b.leaseHeld = false
					case "plan-drift":
						candidate.Inputs.ConfigurationSHA256 = Digest("concurrent-input")
					}
					return nil
				}
				if mode == "missing" {
					accept = nil
				}
				err = p.Execute(context.Background(), b, sha, root, qualified, fence, accept)
				if mode == "missing" {
					if err == nil || len(b.written) != 0 || b.leaseHeld {
						t.Fatal("missing acceptance mutated environment")
					}
					return
				}
				var journal Journal
				raw, readErr := os.ReadFile(filepath.Join(root, "deployment", "rollback", sha, "journal.json"))
				if readErr != nil || json.Unmarshal(raw, &journal) != nil {
					t.Fatal("acceptance journal missing")
				}
				if mode == "success" {
					if err != nil || b.leaseHeld || !journal.Accepted {
						t.Fatal("qualified acceptance did not complete", err)
					}
					if _, err := os.Stat(filepath.Join(root, "deployment", ".execution-active")); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("completed fence retained")
					}
				} else {
					if err == nil || journal.Accepted || len(b.written) != 1 {
						t.Fatal("failed acceptance completed or overwrote concurrent state")
					}
					if b.leaseHeld {
						t.Fatal("failed acceptance retained distributed execution ownership")
					}
					if mode != "lease-lost" {
						if _, err := os.Stat(filepath.Join(root, "deployment", ".execution-active")); !errors.Is(err, os.ErrNotExist) {
							t.Fatal("failed acceptance retained its active local lock", err)
						}
					}
					if mode == "typed-failed" {
						var failure *Blocker
						if !errors.As(err, &failure) || failure.ID != "deployment.post-deploy.workloads" || failure.Resource != ID(d.Object) || journal.Failed != failure.ID || strings.Contains(err.Error(), "private-endpoint") {
							t.Fatal("typed acceptance cause or privacy lost", err)
						}
					}
					if mode == "failed" && (journal.Failed != "deployment.post-deploy" || strings.Contains(err.Error(), "private-endpoint")) {
						t.Fatal("acceptance failure lost its stable safe cause")
					}
				}
			})
		}
	}
}
