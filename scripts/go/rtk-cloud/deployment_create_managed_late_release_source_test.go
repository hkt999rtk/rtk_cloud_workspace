package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"
)

func normalTailReleaseHeldFixture(t *testing.T) (deploymentConfig, secretStore, *environmentCreateExecution) {
	t.Helper()
	cfg, _, _, store, e, _, _ := createCompletedIngressFixture(t)
	name := "retire-pki-bootstrap-custody-" + e.Journal.PlanSHA256[:12]
	for i, key := range []string{"kubernetes/account-manager-registration", name, "kubernetes/" + name} {
		e.Journal.Entries[428+i].Key = key
		e.Journal.Entries[428+i].IntentSHA256 = deploymentplan.Digest(key)
	}
	e.Journal.KubernetesLeases = []deploymentplan.LeaseOwner{{UID: "11111111-1111-4111-8111-111111111111"}, {UID: "22222222-2222-4222-8222-222222222222"}}
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	if _, err := prepareEnvironmentCreateDirectories(store, filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap")); err != nil {
		t.Fatal(err)
	}
	if !managedLateNormalTailControllerSelected(e.Journal) || e.Check(t.Context()) != nil {
		t.Fatal("held completed434 fixture refused")
	}
	return cfg, store, e
}

// A synthetic constructor result exercises the existing capability's ownership,
// source and manifest checks. It is never a substitute for the strong loader.
func normalTailReleaseTestScope(t *testing.T, store secretStore, e *environmentCreateExecution) *managedLateSourceReuse {
	t.Helper()
	manifest, err := readManagedLateSourceReuseManifest(store, e.Journal.PlanSHA256, nil)
	if err != nil {
		t.Fatal(err)
	}
	source := e.Journal
	source.Entries = source.Entries[:374]
	scope := &managedLateSourceReuse{store: store, source: source, ownerSHA: deploymentplan.Digest(e.owner), currentSHA: deploymentplan.Digest(e.Journal), manifest: manifest, values: map[string]json.RawMessage{}, outputs: map[string]bool{}}
	e.lateSourceReuse = scope
	if err := checkManagedLateSourceReuse(e); err != nil {
		t.Fatal(err)
	}
	return scope
}

func TestManagedLateNormalTailReleaseBindingsEndOldPhaseAndReauthenticate(t *testing.T) {
	cfg, store, e := normalTailReleaseHeldFixture(t)
	completed := e.Journal
	e.Journal = managedLateNormalTailOriginalSource(completed)
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	normalTailReleaseTestScope(t, store, e)
	if err := rememberManagedLateSourceReuse(store, e, cfg, "release-test-history", map[string]string{"phase": "old433"}); err != nil {
		t.Fatal(err)
	}
	old := *e
	e.Journal = completed
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	if managedLateSourceReuseActive(&old) {
		t.Fatal("completed434 reused invalid433 scope")
	}
	fresh434 := normalTailReleaseTestScope(t, store, e)
	bindings := environmentIdentityBindings{Environment: store.Environment, Stack: e.Journal.Stack, Version: 1, Owners: map[string]string{}}
	journalBefore, _ := os.ReadFile(e.JournalPath)
	if _, err := writeManagedLateNormalTailBindings(t.Context(), store, e, bindings); err != nil {
		t.Fatal("first bindings publication refused", err)
	}
	if managedLateSourceReuseActive(e) || fresh434.values != nil {
		t.Fatal("new local file retained pre-write source")
	}
	normalTailReleaseTestScope(t, store, e)
	derivations, liveReads := 0, 0
	for range 6 {
		var result map[string]string
		hit, err := readManagedLateSourceReuse(store, e, cfg, "release-test-history", &result)
		if err != nil {
			t.Fatal(err)
		}
		if !hit {
			derivations++
			result = map[string]string{"phase": "after-bindings434"}
			if err := rememberManagedLateSourceReuse(store, e, cfg, "release-test-history", result); err != nil {
				t.Fatal(err)
			}
		}
		if result["phase"] != "after-bindings434" {
			t.Fatal("new phase reused old source")
		}
		liveReads++
	}
	if derivations != 1 || liveReads != 6 {
		t.Fatalf("pure/live counts %d/%d", derivations, liveReads)
	}
	after, _ := os.ReadFile(e.JournalPath)
	if !bytes.Equal(journalBefore, after) {
		t.Fatal("bindings publication replayed journal/native operation")
	}
}

func TestManagedLateNormalTailReleaseBindingsRetryRemainsReadOnly(t *testing.T) {
	for _, mode := range []string{"same-bytes", "different-bindings", "public-mode", "symlink", "active-manifest-drift", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			_, store, e := normalTailReleaseHeldFixture(t)
			bindings := environmentIdentityBindings{Environment: store.Environment, Stack: e.Journal.Stack, Version: 1, Owners: map[string]string{}}
			if _, err := writeManagedLateNormalTailBindings(t.Context(), store, e, bindings); err != nil {
				t.Fatal(err)
			}
			scope := normalTailReleaseTestScope(t, store, e)
			path := filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/bindings.json")
			before, _ := os.ReadFile(path)
			info, _ := os.Stat(path)
			parentBefore := deploymentplan.Digest(e.Journal)
			ctx := t.Context()
			switch mode {
			case "different-bindings":
				bindings.Owners["ConfigMap/"+e.Journal.Stack+"-video-cloud/foreign"] = "foreign"
			case "public-mode":
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			case "symlink":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(e.JournalPath, path); err != nil {
					t.Fatal(err)
				}
			case "active-manifest-drift":
				managedLateSourceReuseNegativeWrite(t, store, "pki/foreign-input.json", []byte(`{"changed":true}`))
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			_, err := writeManagedLateNormalTailBindings(ctx, store, e, bindings)
			if mode == "same-bytes" {
				after, _ := os.ReadFile(path)
				afterInfo, _ := os.Stat(path)
				if err != nil || !bytes.Equal(before, after) || !info.ModTime().Equal(afterInfo.ModTime()) || e.lateSourceReuse != scope || !managedLateSourceReuseActive(e) {
					t.Fatal("identical retry wrote or reset phase", err)
				}
			} else if err == nil {
				t.Fatal("unsafe retry accepted", mode)
			}
			if parentBefore != deploymentplan.Digest(e.Journal) {
				t.Fatal("bindings retry rewrote completed434")
			}
		})
	}
}

func TestManagedLateNormalTailReleaseRequiresStrongCompletedSource(t *testing.T) {
	for _, mode := range []string{"unproved434", "pending432", "foreign-backend", "foreign-store", "wrong-parent", "wrong-owner", "expired-clock", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, e := normalTailReleaseHeldFixture(t)
			n, _, _, _ := normalTailAuthorityFixture(t)
			n.SourceSHA = deploymentplan.Digest(managedLateNormalTailOriginalSource(e.Journal))
			n.OwnerSHA = deploymentplan.Digest(e.owner)
			n.Authority = &createManagedLateNormalTailAuthority{Deadline: time.Now().Add(time.Hour)}
			runtime := environmentCreateBootstrapRuntime{Execution: e, ExecutionSHA256: e.Journal.PlanSHA256, Backend: environmentKubernetesBackend{Store: store}, ServiceSessionRecovery: &createServiceSessionRecovery{normalTail: n}}
			ctx := t.Context()
			switch mode {
			case "pending432":
				e.Journal.Entries[431].State = "pending"
				e.Journal.Entries[431].CompletedAt = time.Time{}
				e.Journal.Entries[431].Receipt = nil
				if err := e.save(); err != nil {
					t.Fatal(err)
				}
			case "foreign-backend":
				runtime.Backend.Store.Root += "-foreign"
			case "foreign-store":
				store.Root += "-foreign"
			case "wrong-parent":
				runtime.ExecutionSHA256 = strings.Repeat("a", 64)
			case "wrong-owner":
				n.OwnerSHA = strings.Repeat("b", 64)
			case "expired-clock":
				n.Authority.Deadline = time.Now().Add(-time.Second)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			if commands, err := managedLateNormalTailReleaseCommands(ctx, cfg, store, runtime); err == nil || commands != nil {
				t.Fatal("marker/foreign source gained memo authority", mode)
			}
		})
	}
	commands, err := managedLateNormalTailReleaseCommands(t.Context(), deploymentConfig{}, secretStore{}, environmentCreateBootstrapRuntime{})
	if err != nil || commands != nil {
		t.Fatal("ordinary release gained command cache", err)
	}
}

func TestCreateOpenBaoACKReleaseCommandKeepsFullProofChecks(t *testing.T) {
	f := newCreateOpenBaoACKCompletionFixture(t)
	if err := f.publish(t); err != nil {
		t.Fatal(err)
	}
	fence := filepath.Join(f.image.store.Root, "deployment/.execution-active")
	ownerInfo, err := os.Stat(fence)
	if err != nil {
		t.Fatal(err)
	}
	running, err := deploymentplan.LockExecutionOwner(fence, true)
	if err != nil {
		t.Fatal(err)
	}
	runningInfo, err := running.Stat()
	if err != nil {
		t.Fatal(err)
	}
	e := &environmentCreateExecution{Store: f.image.store, Journal: f.current, owner: f.owner, JournalPath: filepath.Join(f.image.store.Root, f.owner.Journal), Directory: filepath.Dir(filepath.Join(f.image.store.Root, f.owner.Journal)), running: running, runningInfo: runningInfo, ownerInfo: ownerInfo}
	t.Cleanup(func() { _ = e.Close() })
	run := f.image.command(t)
	release, err := effectiveCreateOpenBaoACKReleaseWithCommand(t.Context(), f.image.cfg, f.image.store, f.image.parent, f.original, run, e)
	if err != nil || release.Images["LKE_OPENBAO_PKI_IMAGE"].Reference != f.ack.Image.Reference {
		t.Fatal("held command proof refused", err)
	}
	if _, err := effectiveCreateOpenBaoACKReleaseWithCommand(t.Context(), f.image.cfg, f.image.store, f.image.parent, f.original, run, e, e); err == nil {
		t.Fatal("multiple commands accepted")
	}
	e.Journal.Entries[len(e.Journal.Entries)-1].Receipt = []byte(`{"changed":true}`)
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	if _, err := effectiveCreateOpenBaoACKReleaseWithCommand(t.Context(), f.image.cfg, f.image.store, f.image.parent, f.original, run, e); err == nil {
		t.Fatal("command reuse skipped changed ACK receipt")
	}
}
