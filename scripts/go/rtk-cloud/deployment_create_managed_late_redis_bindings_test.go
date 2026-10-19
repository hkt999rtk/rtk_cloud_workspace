package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func redisPrePauseBindingsFixture(t *testing.T) (secretStore, *environmentCreateExecution, environmentIdentityBindings, string) {
	t.Helper()
	store, e, r, _ := redisPrePauseFixture(t)
	original := environmentIdentityBindings{Version: 1, Environment: store.Environment, Stack: e.Journal.Stack, Owners: map[string]string{r.redisDurable.ResourceID: r.redisDurable.UID}, Pods: []environmentPodBindings{{Kind: "Deployment", NamespaceSuffix: "platform", Name: "redis", Component: "redis", PodFields: map[string]any{"volumes": []any{deploymentplan.Object{"name": "data", "emptyDir": deploymentplan.Object{}}}}}}}
	relative := filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/bindings.json")
	if _, err := writeEnvironmentCreateBootstrapJSON(store, relative, original); err != nil {
		t.Fatal(err)
	}
	_, volume, err := readManagedLateRedisDurableVolumeAuthority(store, e)
	if err != nil || volume == nil {
		t.Fatal("genuine accepted claim", err)
	}
	observed := original
	observed.Owners = map[string]string{}
	for id, uid := range original.Owners {
		observed.Owners[id] = uid
	}
	observed.Owners[deploymentplan.ID(r.redisDurable.Claim)] = volume.PVCUID
	return store, e, observed, filepath.Join(store.Root, relative)
}

func TestManagedLateRedisPrePauseBindingsPreserveOriginalUntilGenuineCutover(t *testing.T) {
	store, e, observed, path := redisPrePauseBindingsFixture(t)
	before, _ := os.ReadFile(path)
	info, _ := os.Stat(path)
	parentBefore, _ := os.ReadFile(e.JournalPath)
	grantBefore, err := readManagedLateNormalTailExtensionPair(store, e.Journal.PlanSHA256)
	if err != nil {
		t.Fatal(err)
	}
	observedSHA := deploymentplan.Digest(observed)
	scope := normalTailReleaseTestScope(t, store, e)
	original, err := readManagedLateRedisPrePauseBindings(t.Context(), store, e, observed)
	if err != nil {
		t.Fatal("only genuine accepted claim delta refused", err)
	}
	originalRaw, _ := json.Marshal(original)
	claimID := "PersistentVolumeClaim/" + e.Journal.Stack + "-platform/redis-data"
	if !bytes.Equal(originalRaw, before) || original.Owners[claimID] != "" || observed.Owners[claimID] == "" || deploymentplan.Digest(observed) != observedSHA {
		t.Fatal("comparison changed original bindings or fresh inventory")
	}
	if _, err := writeManagedLateNormalTailBindings(t.Context(), store, e, original); err != nil {
		t.Fatal("unchanged private bindings cannot be reused", err)
	}
	after, _ := os.ReadFile(path)
	afterInfo, _ := os.Stat(path)
	parentAfter, _ := os.ReadFile(e.JournalPath)
	grantAfter, _ := readManagedLateNormalTailExtensionPair(store, e.Journal.PlanSHA256)
	if !bytes.Equal(before, after) || !info.ModTime().Equal(afterInfo.ModTime()) || !bytes.Equal(parentBefore, parentAfter) || !bytes.Equal(grantBefore, grantAfter) || e.lateSourceReuse != scope || !managedLateSourceReuseActive(e) {
		t.Fatal("read-only pre-pause reuse wrote artifacts, changed authority, or reset scope")
	}
	for _, name := range []string{"source.rdb", "completed.json", "bindings.json"} {
		if _, err := os.Lstat(filepath.Join(store.Root, managedLateRedisDurableBase(e.Journal.PlanSHA256), name)); !os.IsNotExist(err) {
			t.Fatal("pre-pause comparison fabricated cutover evidence", name)
		}
	}
}

func TestManagedLateRedisPrePauseBindingsRejectUnprovedOrAdditionalChanges(t *testing.T) {
	for _, mode := range []string{"foreign-claim-uid", "missing-claim", "extra-owner", "changed-owner", "removed-owner", "pod-field-drift", "missing-record", "public-record", "symlink-record", "malformed-record", "foreign-volume", "unknown-child-outcome", "active-manifest-drift", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			store, e, observed, path := redisPrePauseBindingsFixture(t)
			parentBefore, _ := os.ReadFile(e.JournalPath)
			claimID := "PersistentVolumeClaim/" + e.Journal.Stack + "-platform/redis-data"
			ctx := t.Context()
			switch mode {
			case "foreign-claim-uid":
				observed.Owners[claimID] = "99999999-9999-4999-8999-999999999999"
			case "missing-claim":
				delete(observed.Owners, claimID)
			case "extra-owner":
				observed.Owners["ConfigMap/"+e.Journal.Stack+"-platform/foreign"] = "99999999-9999-4999-8999-999999999999"
			case "changed-owner", "removed-owner":
				id := "Deployment/" + e.Journal.Stack + "-platform/redis"
				if mode == "removed-owner" {
					delete(observed.Owners, id)
				} else {
					observed.Owners[id] = "99999999-9999-4999-8999-999999999999"
				}
			case "pod-field-drift":
				observed.Pods[0].PodFields["volumes"] = []any{deploymentplan.Object{"name": "data", "persistentVolumeClaim": deploymentplan.Object{"claimName": "redis-data"}}}
			case "missing-record":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "public-record":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink-record":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(e.JournalPath, path); err != nil {
					t.Fatal(err)
				}
			case "malformed-record":
				if err := os.WriteFile(path, []byte(`{"foreign":true}`), 0600); err != nil {
					t.Fatal(err)
				}
			case "foreign-volume":
				volumePath := filepath.Join(store.Root, managedLateRedisDurableBase(e.Journal.PlanSHA256), "volume.json")
				var volume environmentCreateVolumeOwner
				if readExecutionJSON(volumePath, &volume) != nil {
					t.Fatal("volume fixture")
				}
				volume.PVCUID = "99999999-9999-4999-8999-999999999999"
				if err := os.WriteFile(volumePath, mustProviderRecoveryJSON(volume), 0600); err != nil {
					t.Fatal(err)
				}
			case "unknown-child-outcome":
				resume, _, err := readManagedLateRedisPrePauseSource(store, e)
				if err != nil {
					t.Fatal(err)
				}
				childPath := filepath.Join(store.Root, "deployment/rollback", resume.ChildPlanSHA, "journal.json")
				var child deploymentplan.Journal
				if readExecutionJSON(childPath, &child) != nil {
					t.Fatal("child fixture")
				}
				child.InFlight = child.Completed[0]
				if err := os.WriteFile(childPath, mustProviderRecoveryJSON(child), 0600); err != nil {
					t.Fatal(err)
				}
			case "active-manifest-drift":
				normalTailReleaseTestScope(t, store, e)
				managedLateSourceReuseNegativeWrite(t, store, "pki/foreign-input.json", []byte(`{"changed":true}`))
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if _, err := readManagedLateRedisPrePauseBindings(ctx, store, e, observed); err == nil {
				t.Fatal("unqualified binding delta accepted", mode)
			}
			parentAfter, _ := os.ReadFile(e.JournalPath)
			if !bytes.Equal(parentBefore, parentAfter) {
				t.Fatal("refused binding comparison replayed parent operations")
			}
		})
	}
}
