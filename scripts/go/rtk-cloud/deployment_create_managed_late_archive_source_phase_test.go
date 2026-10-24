package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedLateNewArchiveEndsExact403HistoricalPhaseBeforeWrite(t *testing.T) {
	for _, mode := range []string{"new-archive", "after-completed-copy", "foreign-archive-uid", "foreign-archive-namespace", "changed-input", "changed-owner", "changed-journal", "foreign-current-scope", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			runtime, _, _ := managedLateResidualRetry403Fixture(t)
			e, r := runtime.Execution, runtime.ServiceSessionRecovery
			input := filepath.Join(e.Store.Root, "openbao/root-token")
			if err := ensurePrivateDirectory(filepath.Dir(input)); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(input, []byte("isolated-pinned-input"), 0600); err != nil {
				t.Fatal(err)
			}
			manifest, err := readManagedLateSourceReuseManifest(e.Store, e.Journal.PlanSHA256, nil)
			if err != nil {
				t.Fatal(err)
			}
			source := e.Journal
			source.Entries = source.Entries[:374]
			reuse := &managedLateSourceReuse{store: e.Store, source: source, ownerSHA: deploymentplan.Digest(e.owner), currentSHA: deploymentplan.Digest(e.Journal), manifest: manifest, values: map[string]json.RawMessage{}, outputs: map[string]bool{}}
			e.lateSourceReuse = reuse
			// Keep an actual shared projected view: invalidation must reach it too.
			view := *e
			view.Journal = e.Journal
			view.Journal.Entries = view.Journal.Entries[:392]
			reuse.boundedSHA = deploymentplan.Digest(view.Journal)
			if err := checkManagedLateSourceReuse(e); err != nil {
				t.Fatal(err)
			}
			originalRemaining := r.ManagedLateRemaining
			object := originalRemaining.ResidualCleanup.Approval.Objects[3]
			if mode == "after-completed-copy" {
				key := "bootstrap-copy-retire-" + object.Namespace + "-" + object.SessionID
				intent := deploymentplan.Object{"resource_id": object.ResourceID, "uid": object.UID, "session_id": object.SessionID}
				if err := e.BeginEntry(key, "bootstrap", mustProviderRecoveryJSON(intent), time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				if err := e.RecordReceipt(key, mustProviderRecoveryJSON(deploymentplan.Object{"resource_id": object.ResourceID, "uid": object.UID, "absent": true})); err != nil {
					t.Fatal(err)
				}
				if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				if managedLateSourceReuseActive(e) || len(e.Journal.Entries) != 404 || e.Journal.Entries[403].State != "completed" {
					t.Fatal("completed copy did not invalidate its historical phase")
				}
				previous := object
				for _, candidate := range originalRemaining.ResidualCleanup.Approval.Objects {
					if candidate.SessionID == previous.SessionID && candidate.Namespace != previous.Namespace {
						object = candidate
						break
					}
				}
				if object.SessionID != previous.SessionID || object.Namespace == previous.Namespace {
					t.Fatal("fixture did not select the next namespace of the same original seed")
				}
			}
			old := environmentCreateServiceCeremony{BootstrapSessionID: object.SessionID, BootstrapCredentialSecret: "service-bootstrap-" + object.SessionID, CredentialUIDs: map[string]string{}}
			for _, approved := range originalRemaining.ResidualCleanup.Approval.Objects {
				if approved.SessionID == object.SessionID {
					old.BootstrapCredentialNamespaces = append(old.BootstrapCredentialNamespaces, approved.Namespace)
					old.CredentialUIDs[approved.Namespace] = approved.UID
				}
			}
			// The archive boundary consumes the already authenticated in-memory
			// observation inventory; keep the stored authority fixture unchanged.
			r.Intent.Namespaces = append([]string(nil), old.BootstrapCredentialNamespaces...)
			r.StaleSeeds = []createServiceSessionObserved{{Cleanup: environmentBootstrapCleanupBinding{SessionID: old.BootstrapSessionID}, UIDs: old.CredentialUIDs}}
			localRemaining := *originalRemaining
			localRemaining.StaleSeedsSHA = deploymentplan.Digest(r.StaleSeeds)
			r.ManagedLateRemaining = &localRemaining
			secret := deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"namespace": object.Namespace, "name": old.BootstrapCredentialSecret, "uid": object.UID, "resourceVersion": "123", "labels": deploymentplan.Object{"rtk.cloud/bootstrap-session": object.SessionID, "rtk.realtek.com/stack": e.Journal.Stack}}, "data": deploymentplan.Object{"tls.key": "isolated-archive-canary"}}
			relative := filepath.Join("deployment/create", runtime.ExecutionSHA256, "bootstrap/service/session-renewal", r.Review, "expired-credential-copies", old.BootstrapSessionID, object.Namespace+".json")
			path := filepath.Join(e.Store.Root, relative)
			ctx := context.Background()
			switch mode {
			case "foreign-archive-uid":
				deploymentplan.Map(secret["metadata"])["uid"] = "99999999-1111-4111-8111-111111111111"
			case "foreign-archive-namespace":
				r.Intent.Namespaces = nil
			case "changed-input":
				if err := os.WriteFile(input, []byte("changed-input"), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed-owner":
				e.owner.OwnerToken += "changed"
			case "changed-journal":
				e.Journal.OwnerToken += "changed"
			case "foreign-current-scope":
				reuse.currentSHA = strings.Repeat("f", 64)
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			before, err := os.ReadFile(e.JournalPath)
			if err != nil {
				t.Fatal(err)
			}
			_, err = writeEnvironmentCreateExpiredCredentialArchive(ctx, deploymentConfig{Values: map[string]string{"CLOUD_STACK_NAME": e.Journal.Stack}}, e.Store, runtime, old, object.Namespace, secret)
			if (err == nil) != (mode == "new-archive" || mode == "after-completed-copy") {
				t.Fatal("archive crossed an unverified historical phase", mode, err)
			}
			after, _ := os.ReadFile(e.JournalPath)
			if !bytes.Equal(before, after) {
				t.Fatal("archive publication rewrote or replayed403 progress")
			}
			if mode != "new-archive" && mode != "after-completed-copy" {
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("refused archive was created")
				}
				return
			}
			if managedLateSourceReuseActive(e) || managedLateSourceReuseActive(&view) || checkManagedLateSourceReuse(&view) == nil {
				t.Fatal("new archive retained old shared historical authority")
			}
			got, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(got, mustProviderRecoveryJSON(secret)) {
				t.Fatal("original exclusive archive bytes differ", err)
			}
			if !managedLateResidualCleanupProgressValid(e.Journal, originalRemaining) || e.Check(context.Background()) != nil {
				t.Fatal("archive changed canonical403 authority")
			}
			fresh, err := readManagedLateSourceReuseManifest(e.Store, e.Journal.PlanSHA256, nil)
			if err != nil || sameManagedLateSourceReuseManifest(manifest, fresh) || fresh[filepath.ToSlash(relative)] == "" {
				t.Fatal("next phase did not capture the new archive as an input", err)
			}
			// A subsequent authenticated phase starts empty and may retain only
			// newly derived pure content under the updated manifest.
			e.lateSourceReuse = &managedLateSourceReuse{store: e.Store, source: source, ownerSHA: deploymentplan.Digest(e.owner), currentSHA: deploymentplan.Digest(e.Journal), boundedSHA: deploymentplan.Digest(view.Journal), manifest: fresh, values: map[string]json.RawMessage{}, outputs: map[string]bool{}}
			pure := map[string]string{"source": deploymentplan.Digest(e.Journal)}
			if err := rememberManagedLateSourceReuse(e.Store, e, deploymentConfig{Environment: "dev"}, "archive-phase-fixture", pure); err != nil {
				t.Fatal("fresh unchanged403 history could not be retained", err)
			}
			var restored map[string]string
			if hit, err := readManagedLateSourceReuse(e.Store, e, deploymentConfig{Environment: "dev"}, "archive-phase-fixture", &restored); err != nil || !hit || deploymentplan.Digest(restored) != deploymentplan.Digest(pure) || managedLateSourceReuseActive(&view) {
				t.Fatal("fresh phase failed or revived the stale shared view", err)
			}
		})
	}
}
