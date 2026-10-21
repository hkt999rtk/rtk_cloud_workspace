package main

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Initializer receipts and retained foundation ownership come from their real
// producers. The read-only renewal check must bind them to the same Pod, image,
// StatefulSet and original private initialization bytes.
func TestCRLExecutionContractActiveTransportOriginalIdentity(t *testing.T) {
	for _, mode := range []string{"original", "changed-init", "missing-token", "foreign-set", "foreign-pod", "foreign-pod-owner", "foreign-image", "changed-admitted-body", "missing-initializer-receipt"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, backend, e, _ := createOpenBaoNativeFixture(t, "valid")
			ns := e.Journal.Stack + "-secrets"
			containers := []any{deploymentplan.Object{"name": "openbao", "image": "docker.io/openbao/openbao@sha256:" + deploymentplan.Digest("original immutable image")}}
			set := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"namespace": ns, "name": "openbao", "uid": "stateful-owner", "resourceVersion": "1", "labels": deploymentplan.Object{"rtk.realtek.com/stack": e.Journal.Stack}}, "spec": deploymentplan.Object{"replicas": float64(1), "serviceName": "openbao", "selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app": "openbao"}}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app": "openbao"}}, "spec": deploymentplan.Object{"containers": containers}}}}
			defs := []deploymentplan.Definition{{Object: deploymentplan.Projection(set), Phase: "workload", Retain: true}}
			stage := "foundation-recovery-" + deploymentplan.Digest(defs)
			p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest("original release"), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs)}, defs, []deploymentplan.Object{set})
			if err != nil {
				t.Fatal(err)
			}
			memory := freshStageBackend{&normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{deploymentplan.ID(set): set}, writes: map[string]int{}}}
			if err := executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, p, memory, func(ctx context.Context, p *deploymentplan.Plan) error {
				return p.AdmitResumable(ctx, memory, store.Root)
			}, func(ctx context.Context, p *deploymentplan.Plan) error {
				return memory.CheckParentExecution(ctx, p, store.Root)
			}, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, memory) }, func(ctx context.Context) error { return e.Check(ctx) }, io.Discard); err != nil {
				t.Fatal("actual retained foundation", err)
			}
			if _, err := initializeEnvironmentCreateOpenBao(t.Context(), cfg, store, backend, e); err != nil {
				t.Fatal("actual initializer", err)
			}
			pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"namespace": ns, "name": "openbao-0", "uid": "pod-owner", "resourceVersion": "1", "ownerReferences": []any{deploymentplan.Object{"kind": "StatefulSet", "name": "openbao", "uid": "stateful-owner", "controller": true}}}, "spec": deploymentplan.Object{"containers": certIssuerObjectList(environmentPKIPod(set)["containers"])}}
			// Clone through the JSON-shaped projection so mutation cannot change the
			// admitted StatefulSet's template through a shared container slice.
			pod = deploymentplan.Clone(pod)
			f := &crlExecutorFixture{cfg: cfg, store: store, execution: e, backend: backend}
			state := crlExecutorBoundary{Objects: map[string]deploymentplan.Object{deploymentplan.ID(set): set, deploymentplan.ID(pod): pod}}
			switch mode {
			case "changed-init":
				if err := os.WriteFile(filepath.Join(store.Root, "openbao/init.json"), []byte("{}"), 0600); err != nil {
					t.Fatal(err)
				}
			case "missing-token":
				if err := os.Remove(filepath.Join(store.Root, "openbao/root-token")); err != nil {
					t.Fatal(err)
				}
			case "foreign-set":
				deploymentplan.Map(set["metadata"])["uid"] = "replacement"
			case "foreign-pod":
				deploymentplan.Map(pod["metadata"])["uid"] = "replacement"
			case "foreign-pod-owner":
				deploymentplan.Map(deploymentplan.Map(pod["metadata"])["ownerReferences"].([]any)[0])["uid"] = "replacement"
			case "foreign-image":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["spec"])["containers"])[0])["image"] = "foreign"
			case "changed-admitted-body":
				deploymentplan.Map(set["spec"])["replicas"] = float64(2)
			case "missing-initializer-receipt":
				for i := range e.Journal.Entries {
					if e.Journal.Entries[i].Key == "openbao-initialize" {
						e.Journal.Entries[i].Receipt = nil
					}
				}
				if err := e.save(); err != nil {
					t.Fatal(err)
				}
			}
			installCRLExecutorProtocol(t, f, state)
			before := deploymentplan.Digest(e.Journal)
			gotPod, gotSet, err := activeTransportOriginalIdentity(t.Context(), cfg, store, e, backend)
			if mode == "original" {
				if err != nil || gotPod != "pod-owner" || gotSet != "stateful-owner" {
					t.Fatal("genuine original identity rejected", gotPod, gotSet, err)
				}
			} else if err == nil {
				t.Fatal("changed identity accepted", gotPod, gotSet)
			}
			if deploymentplan.Digest(e.Journal) != before {
				t.Fatal("read-only identity check altered receipt history")
			}
			after := f.state(t)
			if after.SecretWrites != 0 || after.Reloads != 0 || after.Imports != 0 {
				t.Fatal("identity qualification changed original native state")
			}
		})
	}
}

func TestCRLExecutionContractActiveTransportReloadsOriginalOwner(t *testing.T) {
	for _, mode := range []string{"original", "held-lock", "changed-configuration", "changed-artifact", "changed-journal-owner"} {
		t.Run(mode, func(t *testing.T) {
			f := newInitializedAuditContractFixture(t)
			switch mode {
			case "held-lock":
				lock, err := deploymentplan.LockExecutionOwner(filepath.Join(f.store.Root, "deployment/.execution-active"), false)
				if err != nil {
					t.Fatal(err)
				}
				defer lock.Close()
			case "changed-configuration":
				f.cfg.Values["SYNTHETIC_CHANGED_INTENT"] = "changed"
			case "changed-artifact":
				a := f.artifact
				a.Plan.Plan.OperatorSHA256 = deploymentplan.Digest("foreign")
				f.write(t, filepath.Join("deployment/create/recovery", f.sha, "qualification.json"), a)
			case "changed-journal-owner":
				f.parent.Journal.OwnerToken = "foreign"
				if err := f.parent.save(); err != nil {
					t.Fatal(err)
				}
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			e, a, err := loadActiveTransportExecution(f.cfg, f.store, f.sha)
			if mode == "original" {
				if err != nil {
					t.Fatal("original qualified owner unavailable", err)
				}
				if deploymentplan.Digest(a) != deploymentplan.Digest(f.artifact) || deploymentplan.Digest(e.Journal) != deploymentplan.Digest(f.parent.Journal) || e.Check(t.Context()) != nil {
					t.Fatal("loaded another original execution")
				}
				if second, _, err := loadActiveTransportExecution(f.cfg, f.store, f.sha); err == nil {
					second.Close()
					t.Fatal("second command acquired same active owner")
				}
				if err := e.Close(); err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				e.Close()
				t.Fatal("unproven original execution admitted")
			}
			after := createBootstrapStateSnapshot(t, f.store.Root)
			for path, sha := range before {
				if after[path] != sha {
					t.Fatal("owner reload changed retained evidence", path)
				}
			}
		})
	}
}
