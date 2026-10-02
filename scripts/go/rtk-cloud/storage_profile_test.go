package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCandidateStorageProfilePreservesActive(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "active.env")
	target := filepath.Join(root, "candidate.env")
	original := []byte("LINODE_TOKEN=token\nLINODE_MEDIA_OBJ_ACCESS_KEY_ID=old\n")
	if err := os.WriteFile(source, original, 0600); err != nil {
		t.Fatal(err)
	}
	if err := stageDeploymentStorageProfile("dev", source, target, true); err != nil {
		t.Fatal(err)
	}
	active, _ := os.ReadFile(source)
	if string(active) != string(original) {
		t.Fatal("active profile changed")
	}
	if err := stageDeploymentStorageProfile("staging", source, target, false); err == nil {
		t.Fatal("cross-environment candidate accepted")
	}
	if err := stageDeploymentStorageProfile("dev", source, source, true); err == nil {
		t.Fatal("active overwrite accepted")
	}
}

func TestStoragePromotionRestoresInterruptedCredentialPair(t *testing.T) {
	for _, hadOld := range []bool{true, false} {
		t.Run(map[bool]string{true: "replacing existing pair", false: "first activation"}[hadOld], func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			state := storageCredentialPromotion{Environment: "dev", Purpose: "media", Before: map[string]string{}, After: map[string]string{"LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "new-access", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "new-secret"}}
			if hadOld {
				state.Before = map[string]string{"LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "old-access", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "old-secret"}
				for key, value := range state.Before {
					if err := store.write(filepath.Join("operator", "env", key), []byte(value), true); err != nil {
						t.Fatal(err)
					}
				}
			}
			if err := store.write("operator/env/LINODE_TOKEN", []byte("unrelated-token"), true); err != nil {
				t.Fatal(err)
			}
			// Model interruption after the first file was written. The saved pair
			// must restore both an existing key and an originally absent key.
			if err := store.write("operator/env/LINODE_MEDIA_OBJ_ACCESS_KEY_ID", []byte("new-access"), true); err != nil {
				t.Fatal(err)
			}
			if err := restoreStorageCredentialPair(store, state, false); err != nil {
				t.Fatal(err)
			}
			active, err := store.readOperator()
			if err != nil {
				t.Fatal(err)
			}
			for key := range state.After {
				if active[key] != state.Before[key] {
					t.Fatalf("partially promoted key %s survived rollback", key)
				}
			}
			if active["LINODE_TOKEN"] != "unrelated-token" {
				t.Fatal("recovery changed unrelated credential")
			}
		})
	}
}

func TestStoragePromotionRecoveryPreservesJournalWhenOneFileCannotRestore(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	state := storageCredentialPromotion{Environment: "dev", Purpose: "media", Before: map[string]string{"LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "old-access", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "old-secret"}, After: map[string]string{"LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "new-access", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "new-secret"}}
	path, _ := storagePromotionPath("media")
	body, err := json.Marshal(state)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.write(path, body, false); err != nil {
		t.Fatal(err)
	}
	if err := store.write("operator/env/LINODE_MEDIA_OBJ_ACCESS_KEY_ID", []byte("new-access"), true); err != nil {
		t.Fatal(err)
	}
	blocked := filepath.Join(store.Root, "operator", "env", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY")
	if err := os.Mkdir(blocked, 0700); err != nil {
		t.Fatal(err)
	}
	if err := restoreStorageCredentialPair(store, state, false); err == nil {
		t.Fatal("partial recovery silently succeeded")
	}
	got, err := store.read("operator/env/LINODE_MEDIA_OBJ_ACCESS_KEY_ID")
	if err != nil || got != "old-access" {
		t.Fatal("recoverable half of pair was not restored")
	}
	if got, err := store.read(path); err != nil || got != string(body) {
		t.Fatal("failed recovery lost original credential journal")
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	if err := restoreStorageCredentialPair(store, state, false); err != nil {
		t.Fatal(err)
	}
	got, err = store.read("operator/env/LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY")
	if err != nil || got != "old-secret" {
		t.Fatal("retry did not recover saved secret")
	}
}

func TestStorageActivationRejectsInvalidCandidatesWithoutChangingActive(t *testing.T) {
	for _, tc := range []struct {
		name, purpose string
		values        map[string]string
	}{
		{name: "wrong environment", purpose: "media", values: map[string]string{"RTK_STORAGE_CANDIDATE_ENVIRONMENT": "staging", "LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "new", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "new-secret"}},
		{name: "missing marker", purpose: "media", values: map[string]string{"LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "new", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "new-secret"}},
		{name: "missing secret", purpose: "media", values: map[string]string{"RTK_STORAGE_CANDIDATE_ENVIRONMENT": "dev", "LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "new"}},
		{name: "unknown purpose", purpose: "backup", values: map[string]string{"RTK_STORAGE_CANDIDATE_ENVIRONMENT": "dev"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			if err := store.write("operator/env/LINODE_MEDIA_OBJ_ACCESS_KEY_ID", []byte("old"), true); err != nil {
				t.Fatal(err)
			}
			candidate := filepath.Join(t.TempDir(), "candidate.env")
			if err := writeSortedEnv(candidate, tc.values, 0600); err != nil {
				t.Fatal(err)
			}
			if err := activateStorageCandidate(deploymentConfig{Environment: "dev"}, candidate, tc.purpose); err == nil {
				t.Fatal("invalid candidate accepted")
			}
			got, err := store.read("operator/env/LINODE_MEDIA_OBJ_ACCESS_KEY_ID")
			if err != nil || got != "old" {
				t.Fatal("failed activation changed active access key")
			}
		})
	}
}

func TestStorageActivationRejectsAnotherPromotionAndTamperedRollback(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	if err := store.write("operator/env/LINODE_TOKEN", []byte("keep-token"), true); err != nil {
		t.Fatal(err)
	}
	cfg := deploymentConfig{Environment: "dev"}
	candidate := filepath.Join(t.TempDir(), "candidate.env")
	values := map[string]string{"RTK_STORAGE_CANDIDATE_ENVIRONMENT": "dev", "LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID": "first-access", "LINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY": "first-secret"}
	if err := writeSortedEnv(candidate, values, 0600); err != nil {
		t.Fatal(err)
	}
	if err := activateStorageCandidate(cfg, candidate, "artifacts"); err != nil {
		t.Fatal(err)
	}
	values["LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID"] = "second-access"
	if err := writeSortedEnv(candidate, values, 0600); err != nil {
		t.Fatal(err)
	}
	if err := activateStorageCandidate(cfg, candidate, "artifacts"); err == nil {
		t.Fatal("another promotion overwrote rollback journal")
	}
	got, err := store.read("operator/env/LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID")
	if err != nil || got != "first-access" {
		t.Fatal("conflicting activation changed active credentials")
	}
	path, _ := storagePromotionPath("artifacts")
	for _, body := range []string{
		`{"environment":"dev","purpose":"artifacts","after":{"LINODE_TOKEN":"keep-token","LINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY":"first-secret"}}`,
		`{"environment":"dev","purpose":"artifacts","before":{"LINODE_TOKEN":"replace-token"},"after":{"LINODE_ARTIFACT_OBJ_ACCESS_KEY_ID":"first-access","LINODE_ARTIFACT_OBJ_SECRET_ACCESS_KEY":"first-secret"}}`,
		`{"environment":"staging","purpose":"artifacts","after":{}}`,
		`not-json`,
	} {
		if err := store.write(path, []byte(body), true); err != nil {
			t.Fatal(err)
		}
		if err := rollbackStorageCandidate(cfg, candidate, "artifacts"); err == nil {
			t.Fatal("invalid rollback journal accepted")
		}
		if body == "not-json" {
			if err := activateStorageCandidate(cfg, candidate, "artifacts"); err == nil {
				t.Fatal("activation replaced an invalid existing journal")
			}
		}
		got, err := store.read("operator/env/LINODE_TOKEN")
		if err != nil || got != "keep-token" {
			t.Fatal("invalid rollback changed unrelated token")
		}
	}
	if err := rollbackStorageCandidate(cfg, candidate, "media"); err != nil {
		t.Fatal("no activation should require no credential rollback")
	}
}

func TestStorageActivationCannotWriteCredentialsWithoutRecoveryJournal(t *testing.T) {
	store := makeIsolatedTestSecretStore(t, "dev")
	for key, value := range map[string]string{"LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "old-access", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "old-secret"} {
		if err := store.write(filepath.Join("operator", "env", key), []byte(value), true); err != nil {
			t.Fatal(err)
		}
	}
	candidate := filepath.Join(t.TempDir(), "candidate.env")
	if err := writeSortedEnv(candidate, map[string]string{"RTK_STORAGE_CANDIDATE_ENVIRONMENT": "dev", "LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "new-access", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "new-secret"}, 0600); err != nil {
		t.Fatal(err)
	}
	// A blocking file makes the recovery journal unwritable without racing the
	// two credential writes or relying on privileged permission semantics.
	if err := os.WriteFile(filepath.Join(store.Root, "operator", "storage"), []byte("blocked"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := activateStorageCandidate(deploymentConfig{Environment: "dev"}, candidate, "media"); err == nil {
		t.Fatal("activation proceeded without a durable recovery journal")
	}
	active, err := store.readOperator()
	if err != nil || active["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"] != "old-access" || active["LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY"] != "old-secret" {
		t.Fatal("journal failure changed the active pair")
	}
}

func TestStorageCandidateRejectsUnsafeExistingPaths(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "source.env")
	if err := os.WriteFile(source, []byte("LINODE_TOKEN=token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := stageDeploymentStorageProfile("dev", source, "relative.env", true); err == nil {
		t.Fatal("relative candidate accepted")
	}
	if err := stageDeploymentStorageProfile("dev", source, filepath.Join(root, "missing.env"), false); err == nil {
		t.Fatal("missing candidate accepted")
	}
	for _, tc := range []struct {
		name, body string
		mode       os.FileMode
	}{
		{name: "insecure", body: "RTK_STORAGE_CANDIDATE_ENVIRONMENT=dev\n", mode: 0644},
		{name: "unmarked", body: "LINODE_TOKEN=token\n", mode: 0600},
	} {
		path := filepath.Join(root, tc.name)
		if err := os.WriteFile(path, []byte(tc.body), tc.mode); err != nil {
			t.Fatal(err)
		}
		if err := stageDeploymentStorageProfile("dev", source, path, true); err == nil {
			t.Fatalf("unsafe candidate accepted: %s", tc.name)
		}
	}
	if err := stageDeploymentStorageProfile("dev", source, root, true); err == nil {
		t.Fatal("directory candidate accepted")
	}
	valid := filepath.Join(root, "valid.env")
	if err := stageDeploymentStorageProfile("dev", source, valid, true); err != nil {
		t.Fatal(err)
	}
	if err := stageDeploymentStorageProfile("dev", source, valid, false); err != nil {
		t.Fatal("same candidate could not resume")
	}
}

func TestStorageCLIGuardsActiveCredentialsBeforeCutover(t *testing.T) {
	workspace := writeDeploymentFixture(t, "dev", "lke")
	writeTestFile(t, filepath.Join(workspace, "cloud_env", "dev", "storage.env"), "RUNTIME_MEDIA_STORAGE_POLICY=colocated\nRUNTIME_MEDIA_STORAGE_BUCKET=rtk-cloud-dev-runtime-us-sea\nRUNTIME_MEDIA_STORAGE_PREFIX=environments/video-cloud-dev\nRUNTIME_MEDIA_STORAGE_CUTOVER_REQUIRED=true\n")
	mutated := false
	ops := deploymentOperations{bootstrapCredentials: func(deploymentConfig, string) error { mutated = true; return nil }, grantObjectStorageCredentials: func(deploymentConfig, string) error { mutated = true; return nil }}
	for _, flag := range []string{"--create-missing-object-storage-bucket", "--grant-object-storage-bucket-access"} {
		err := runDeploymentWithOperations([]string{"credentials-check", "--workspace", workspace, "--environment", "dev", flag}, ops)
		if err == nil || !strings.Contains(err.Error(), "isolated candidate") {
			t.Fatalf("active credential repair escaped cutover guard: %v", err)
		}
	}
	for _, action := range []string{"storage-bootstrap", "storage-migrate", "storage-cutover"} {
		err := runDeploymentWithOperations([]string{action, "--workspace", workspace, "--environment", "dev", "--confirm", "video-cloud-dev"}, ops)
		if err == nil || !strings.Contains(err.Error(), "--destination-env-file is required") {
			t.Fatalf("%s accepted active default profile: %v", action, err)
		}
	}
	if mutated {
		t.Fatal("active credentials mutated before isolated profile/cutover validation")
	}
}

func TestStorageCredentialPromotionAndRollback(t *testing.T) {
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", t.TempDir())
	store, err := newSecretStore("", "dev")
	if err != nil {
		t.Fatal(err)
	}
	old := map[string]string{"LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "old-access", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "old-secret", "LINODE_TOKEN": "unchanged"}
	for key, value := range old {
		if err := store.write(filepath.Join("operator", "env", key), []byte(value), false); err != nil {
			t.Fatal(err)
		}
	}
	candidate := filepath.Join(t.TempDir(), "candidate.env")
	if err := writeSortedEnv(candidate, map[string]string{"RTK_STORAGE_CANDIDATE_ENVIRONMENT": "dev", "LINODE_MEDIA_OBJ_ACCESS_KEY_ID": "new-access", "LINODE_MEDIA_OBJ_SECRET_ACCESS_KEY": "new-secret", "LINODE_TOKEN": "unrelated"}, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := deploymentConfig{Environment: "dev"}
	if err := activateStorageCandidate(cfg, candidate, "media"); err != nil {
		t.Fatal(err)
	}
	active, err := store.readOperator()
	if err != nil {
		t.Fatal(err)
	}
	if active["LINODE_MEDIA_OBJ_ACCESS_KEY_ID"] != "new-access" || active["LINODE_TOKEN"] != "unchanged" {
		t.Fatal("wrong credentials promoted")
	}
	if err := activateStorageCandidate(cfg, candidate, "media"); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if err := rollbackStorageCandidate(cfg, candidate, "media"); err != nil {
		t.Fatal(err)
	}
	active, _ = store.readOperator()
	if !equalCredentialPairs(active, old) {
		t.Fatal("prior credentials not restored")
	}
}

func TestStorageCredentialRollbackRejectsConcurrentChange(t *testing.T) {
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", t.TempDir())
	store, _ := newSecretStore("", "dev")
	if err := store.write("operator/env/LINODE_TOKEN", []byte("token"), false); err != nil {
		t.Fatal(err)
	}
	candidate := filepath.Join(t.TempDir(), "candidate.env")
	if err := writeSortedEnv(candidate, map[string]string{"RTK_STORAGE_CANDIDATE_ENVIRONMENT": "dev", "LINODE_OTA_OBJ_ACCESS_KEY_ID": "new-access", "LINODE_OTA_OBJ_SECRET_ACCESS_KEY": "new-secret"}, 0600); err != nil {
		t.Fatal(err)
	}
	cfg := deploymentConfig{Environment: "dev"}
	if err := activateStorageCandidate(cfg, candidate, "ota"); err != nil {
		t.Fatal(err)
	}
	if err := store.write("operator/env/LINODE_OTA_OBJ_ACCESS_KEY_ID", []byte("concurrent"), true); err != nil {
		t.Fatal(err)
	}
	if err := rollbackStorageCandidate(cfg, candidate, "ota"); err == nil {
		t.Fatal("concurrent credential overwrite accepted")
	}
}
