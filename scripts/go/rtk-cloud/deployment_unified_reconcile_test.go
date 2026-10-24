package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type executionReconciliationFixture struct {
	*executionLeaseAPIFixture
	resources              map[string]deploymentplan.Object
	resourceReads          int
	driftAfterVerification bool
}

func (f *executionReconciliationFixture) Read(ctx context.Context, id string) (deploymentplan.Object, error) {
	if strings.HasPrefix(id, "Lease/") {
		return f.executionLeaseAPIFixture.Read(ctx, id)
	}
	f.resourceReads++
	if f.driftAfterVerification && f.resourceReads > 1 {
		deploymentplan.Map(f.resources[id]["data"])["value"] = "concurrent-change"
	}
	return deploymentplan.Clone(f.resources[id]), nil
}
func (f *executionReconciliationFixture) Wait(context.Context, deploymentplan.Resource) error {
	return nil
}

func TestExecutionReconciliationReleasesOnlyStoppedVerifiedOwners(t *testing.T) {
	for _, env := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"no-writes", "accepted", "lease-already-released", "live-owner", "foreign-host", "wrong-owner", "missing-journal", "corrupt-journal", "uncertain-write", "partial-write", "acceptance-started", "late-drift", "delete-race", "symlink-owner"} {
			t.Run(env+"/"+mode, func(t *testing.T) {
				stack := "video-cloud-" + env
				live := deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": map[string]any{"name": "owned", "namespace": stack + "-video-cloud", "uid": "original-owner", "resourceVersion": "4", "labels": map[string]any{"rtk.realtek.com/stack": stack, "app.kubernetes.io/managed-by": "rtk-cloud"}}, "data": map[string]any{"value": "original"}}
				wanted := deploymentplan.Clone(live)
				deploymentplan.Map(wanted["data"])["value"] = "private-evidence-must-not-leak"
				p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: env, Stack: stack, Operation: "upgrade", ConfigurationSHA256: deploymentplan.Digest("config"), ReleaseSHA256: deploymentplan.Digest("release"), BindingsSHA256: deploymentplan.Digest("bindings")}, []deploymentplan.Definition{{Object: wanted, Phase: "configuration"}}, []deploymentplan.Object{live})
				if err != nil {
					t.Fatal(err)
				}
				sha, id := p.SHA256(), deploymentplan.ID(live)
				api := &executionReconciliationFixture{executionLeaseAPIFixture: &executionLeaseAPIFixture{}, resources: map[string]deploymentplan.Object{id: live}, driftAfterVerification: mode == "late-drift"}
				api.object = environmentExecutionLeaseBody(p, sha+":fixture")
				deploymentplan.Map(api.object["metadata"])["uid"], deploymentplan.Map(api.object["metadata"])["resourceVersion"] = "lease-owner", "1"
				hostname, err := os.Hostname()
				if err != nil {
					t.Fatal(err)
				}
				owner := deploymentplan.ExecutionOwner{PlanSHA256: sha, Environment: env, Stack: stack, Hostname: hostname, Lease: deploymentplan.LeaseOwner{ResourceID: deploymentplan.ID(api.object), UID: "lease-owner", Holder: sha + ":fixture"}}
				journal := deploymentplan.Journal{PlanSHA256: sha, Owners: map[string]string{}}
				switch mode {
				case "accepted":
					journal.Accepted, journal.AcceptanceStarted = true, true
					journal.Completed, journal.Owners[id] = []string{id}, "original-owner"
					api.resources[id] = deploymentplan.Clone(p.Resources[0].Desired)
					deploymentplan.Map(api.resources[id]["metadata"])["uid"] = "original-owner"
				case "lease-already-released":
					api.object = nil
				case "foreign-host":
					owner.Hostname = "another-controller"
				case "wrong-owner":
					deploymentplan.Map(api.object["metadata"])["uid"] = "concurrent-owner"
				case "uncertain-write":
					journal.InFlight = id
				case "partial-write":
					journal.Owners[id] = "original-owner"
				case "acceptance-started":
					journal.AcceptanceStarted = true
				case "delete-race":
					api.replaceOnDelete = true
				}
				root := t.TempDir()
				if err := os.Chmod(root, 0700); err != nil {
					t.Fatal(err)
				}
				active := filepath.Join(root, "deployment", ".execution-active")
				directory := filepath.Join(root, "deployment", "rollback", sha)
				for _, path := range []string{root, filepath.Join(root, "deployment"), filepath.Join(root, "deployment", "rollback"), directory, active} {
					if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
						t.Fatal(err)
					}
				}
				if err := os.Mkdir(filepath.Join(directory, ".active"), 0700); err != nil {
					t.Fatal(err)
				}
				for path, value := range map[string]any{filepath.Join(active, "owner.json"): owner, filepath.Join(directory, "plan.json"): p, filepath.Join(directory, "journal.json"): journal} {
					raw, err := json.Marshal(value)
					if err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, raw, 0600); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "missing-journal" {
					if err := os.Remove(filepath.Join(directory, "journal.json")); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "corrupt-journal" {
					if err := os.WriteFile(filepath.Join(directory, "journal.json"), []byte("private-evidence-must-not-leak"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "symlink-owner" {
					if err := os.Remove(filepath.Join(active, "owner.json")); err != nil {
						t.Fatal(err)
					}
					if err := os.Symlink(filepath.Join(directory, "plan.json"), filepath.Join(active, "owner.json")); err != nil {
						t.Fatal(err)
					}
				}
				running, err := deploymentplan.LockExecutionOwner(active, true)
				if err != nil {
					t.Fatal(err)
				}
				if mode == "live-owner" {
					defer running.Close()
				} else {
					if err := running.Close(); err != nil {
						t.Fatal(err)
					}
				}
				outcome, err := reconcileEnvironmentExecution(context.Background(), api, root, env, stack, sha)
				want := mode == "no-writes" || mode == "accepted" || mode == "lease-already-released"
				if (err == nil) != want || api.writes != 0 || api.dry != 0 {
					t.Fatal("reconciliation released an unverified owner or wrote resources", outcome, err)
				}
				if err != nil && strings.Contains(err.Error(), "private-evidence-must-not-leak") {
					t.Fatal("private journal or target leaked")
				}
				_, activeErr := os.Lstat(active)
				if want {
					if !errors.Is(activeErr, os.ErrNotExist) || api.object != nil {
						t.Fatal("confirmed reconciliation left ownership active")
					}
					entries, _ := filepath.Glob(filepath.Join(directory, "reconciled-*", "owner", "owner.json"))
					if len(entries) != 1 {
						t.Fatal("original owner evidence was deleted")
					}
				} else {
					if activeErr != nil || api.deletes != 0 {
						t.Fatal("blocked reconciliation deleted its fence")
					}
				}
			})
		}
	}
}
