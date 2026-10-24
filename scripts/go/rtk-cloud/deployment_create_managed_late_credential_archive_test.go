package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateCredentialArchiveFixture(t *testing.T) (secretStore, string, deploymentplan.Object) {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, "dev")
	sid, namespace := "11111111-1111-4111-8111-111111111111", "video-cloud-dev-video-cloud"
	relative := filepath.Join("deployment/create", strings.Repeat("a", 64), "bootstrap/service/session-renewal", strings.Repeat("b", 64), "expired-credential-copies", sid, namespace+".json")
	secret := deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": "service-bootstrap-" + sid, "namespace": namespace, "uid": "22222222-2222-4222-8222-222222222222", "resourceVersion": "123", "labels": deploymentplan.Object{"rtk.cloud/bootstrap-session": sid, "rtk.realtek.com/stack": "video-cloud-dev"}}, "data": deploymentplan.Object{"tls.crt": "isolated-certificate", "tls.key": "private-fixture-canary"}}
	return store, relative, secret
}

func TestManagedLateCredentialArchiveRetriesOnlyExactPrivateBytes(t *testing.T) {
	store, relative, secret := managedLateCredentialArchiveFixture(t)
	first, err := writeEnvironmentCreateBootstrapJSON(store, relative, secret)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(store.Root, relative)
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	before, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reuseManagedLateExpiredCredentialArchive(store, relative, secret)
	after, statErr := os.Lstat(path)
	if err != nil || statErr != nil || got != first || !os.SameFile(info, after) || !info.ModTime().Equal(after.ModTime()) {
		t.Fatal("same-byte retry rewrote or refused the original archive", err)
	}
	changed := deploymentplan.Clone(secret)
	deploymentplan.Map(changed["data"])["tls.key"] = "private-different-canary"
	if _, err := reuseManagedLateExpiredCredentialArchive(store, relative, changed); err == nil || strings.Contains(err.Error(), "canary") {
		t.Fatal("changed archive passed or leaked material", err)
	}
	retained, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(before, retained) {
		t.Fatal("refusal changed original archive bytes")
	}
}

func TestManagedLateCredentialArchiveRefusesUnsafeExistingOrEquivalentJSON(t *testing.T) {
	for _, mode := range []string{"whitespace", "malformed", "empty", "public-mode", "directory", "symlink", "dangling-symlink", "unsafe-parent", "symlink-parent", "missing", "oversized", "changed-resource-version"} {
		t.Run(mode, func(t *testing.T) {
			store, relative, secret := managedLateCredentialArchiveFixture(t)
			if _, err := writeEnvironmentCreateBootstrapJSON(store, relative, secret); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(store.Root, relative)
			raw := mustProviderRecoveryJSON(secret)
			switch mode {
			case "whitespace":
				if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
					t.Fatal(err)
				}
			case "malformed", "empty", "oversized":
				raw = []byte("not-json-private-canary")
				if mode == "empty" {
					raw = nil
				} else if mode == "oversized" {
					raw = bytes.Repeat([]byte("x"), (4<<20)+1)
				}
				if err := os.WriteFile(path, raw, 0600); err != nil {
					t.Fatal(err)
				}
			case "public-mode":
				if err := os.Chmod(path, 0640); err != nil {
					t.Fatal(err)
				}
			case "directory", "symlink", "dangling-symlink", "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if mode == "directory" {
					if err := os.Mkdir(path, 0700); err != nil {
						t.Fatal(err)
					}
				} else if mode == "symlink" || mode == "dangling-symlink" {
					target := filepath.Join(t.TempDir(), "original")
					if mode == "symlink" {
						if err := os.WriteFile(target, raw, 0600); err != nil {
							t.Fatal(err)
						}
					}
					if err := os.Symlink(target, path); err != nil {
						t.Fatal(err)
					}
				}
			case "unsafe-parent":
				if err := os.Chmod(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
			case "symlink-parent":
				parent := filepath.Dir(path)
				if err := os.Rename(parent, parent+"-original"); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(parent+"-original", parent); err != nil {
					t.Fatal(err)
				}
			case "changed-resource-version":
				secret = deploymentplan.Clone(secret)
				deploymentplan.Map(secret["metadata"])["resourceVersion"] = "124"
			}
			if _, err := reuseManagedLateExpiredCredentialArchive(store, relative, secret); err == nil || strings.Contains(err.Error(), "canary") {
				t.Fatal("unsafe or byte-different archive was reused", mode, err)
			}
		})
	}
}

func TestManagedLateCredentialArchiveKeepsExclusiveCreateAndRejectsUnadmittedReuse(t *testing.T) {
	store, relative, secret := managedLateCredentialArchiveFixture(t)
	meta := deploymentplan.Map(secret["metadata"])
	namespace := deploymentplan.String(meta["namespace"])
	r := &createServiceSessionRecovery{Review: strings.Repeat("b", 64)}
	runtime := environmentCreateBootstrapRuntime{ExecutionSHA256: strings.Repeat("a", 64), ServiceSessionRecovery: r}
	old := environmentCreateServiceCeremony{BootstrapSessionID: "11111111-1111-4111-8111-111111111111"}
	if _, err := writeEnvironmentCreateExpiredCredentialArchive(context.Background(), deploymentConfig{}, store, runtime, old, namespace, secret); err != nil {
		t.Fatal("absent original archive could not be created exclusively", err)
	}
	before, err := os.ReadFile(filepath.Join(store.Root, relative))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writeEnvironmentCreateExpiredCredentialArchive(context.Background(), deploymentConfig{}, store, runtime, old, namespace, secret); err == nil {
		t.Fatal("ordinary lifecycle gained global idempotent ceremony writes")
	}
	r.ManagedLateRemaining = &createManagedLateRemaining{PostNativeCleanup: &createManagedLatePostNativeCleanup{}}
	if _, err := writeEnvironmentCreateExpiredCredentialArchive(context.Background(), deploymentConfig{}, store, runtime, old, namespace, secret); err == nil {
		t.Fatal("bare PostNative marker reused an original archive")
	}
	after, err := os.ReadFile(filepath.Join(store.Root, relative))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("refused retry overwrote original archive")
	}
}

func TestManagedLateCredentialArchiveSourceRequiresOriginalLiveUIDAndInventory(t *testing.T) {
	for _, mode := range []string{"original", "uid", "name", "namespace", "session-label", "stack-label", "resource-version", "stale-sha", "duplicate", "unknown", "namespace-allowlist"} {
		t.Run(mode, func(t *testing.T) {
			_, _, secret := managedLateCredentialArchiveFixture(t)
			meta := deploymentplan.Map(secret["metadata"])
			namespace := deploymentplan.String(meta["namespace"])
			old := environmentCreateServiceCeremony{BootstrapSessionID: "11111111-1111-4111-8111-111111111111", BootstrapCredentialSecret: deploymentplan.String(meta["name"]), BootstrapCredentialNamespaces: []string{namespace}, CredentialUIDs: map[string]string{namespace: deploymentplan.String(meta["uid"])}}
			r := &createServiceSessionRecovery{Intent: createServiceSessionIntent{Namespaces: []string{namespace}}, ManagedLateSession: &createManagedLateSessionRecovery{}, ManagedLateRemaining: &createManagedLateRemaining{PostNativeCleanup: &createManagedLatePostNativeCleanup{}}, StaleSeeds: []createServiceSessionObserved{{Cleanup: environmentBootstrapCleanupBinding{SessionID: old.BootstrapSessionID}, UIDs: old.CredentialUIDs}}}
			switch mode {
			case "uid":
				meta["uid"] = "99999999-9999-4999-8999-999999999999"
			case "name":
				meta["name"] = "another"
			case "namespace":
				meta["namespace"] = "another"
			case "session-label":
				deploymentplan.Map(meta["labels"])["rtk.cloud/bootstrap-session"] = "another"
			case "stack-label":
				deploymentplan.Map(meta["labels"])["rtk.realtek.com/stack"] = "another"
			case "resource-version":
				delete(meta, "resourceVersion")
			case "duplicate":
				r.StaleSeeds = append(r.StaleSeeds, r.StaleSeeds[0])
			case "unknown":
				r.StaleSeeds = nil
			case "namespace-allowlist":
				r.Intent.Namespaces = nil
			}
			r.ManagedLateRemaining.StaleSeedsSHA = deploymentplan.Digest(r.StaleSeeds)
			if mode == "stale-sha" {
				r.ManagedLateRemaining.StaleSeedsSHA = strings.Repeat("e", 64)
			}
			before := deploymentplan.Digest([]any{r, old, secret})
			if managedLateExpiredCredentialArchiveSourceMatches(r, old, namespace, secret, "video-cloud-dev") != (mode == "original") || before != deploymentplan.Digest([]any{r, old, secret}) {
				t.Fatal("archive source drift accepted or original receipt mutated", mode)
			}
		})
	}
}
