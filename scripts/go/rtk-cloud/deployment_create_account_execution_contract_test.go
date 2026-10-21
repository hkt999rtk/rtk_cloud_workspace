package main

import (
	"crypto/sha256"
	"encoding/base64"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func accountStartupExecutionContract(t *testing.T) (*recoveryAcceptanceFailureContract, *deploymentplan.Plan, deploymentplan.Journal) {
	t.Helper()
	ns := "video-cloud-dev-account-manager"
	labels := deploymentplan.Object{"app": "account-manager"}
	defs := []deploymentplan.Definition{
		{Phase: "workload", Object: deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "account-manager", "namespace": ns}, "spec": deploymentplan.Object{"replicas": float64(1), "selector": deploymentplan.Object{"matchLabels": labels}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": labels}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "account-manager", "image": "ghcr.io/hkt999rtk/rtk_account_manager/account-manager@sha256:" + strings.Repeat("a", 64)}}}}}}},
		{Phase: "routing", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Service", "metadata": deploymentplan.Object{"name": "account-manager", "namespace": ns}, "spec": deploymentplan.Object{"selector": labels, "ports": []any{deploymentplan.Object{"port": float64(8080), "targetPort": float64(8080)}}}}},
	}
	secret := deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": "account-manager-runtime", "namespace": ns}, "data": deploymentplan.Object{"EMAIL_OUTBOX_ENCRYPTION_KEY": base64.StdEncoding.EncodeToString([]byte("original-opaque-seed"))}}}
	f := newRecoveryAcceptanceFailureContract(t, "account-bootstrap", defs, []deploymentplan.Definition{secret})
	foundation, err := readEnvironmentCreateStageInput(f.store, f.e, "foundation")
	if err != nil || foundation == nil {
		t.Fatal("original Secret foundation unavailable", err)
	}
	var journal deploymentplan.Journal
	if err := readExecutionJSON(filepath.Join(f.store.Root, "deployment/rollback", foundation.SHA256(), "journal.json"), &journal); err != nil {
		t.Fatal(err)
	}
	return f, foundation, journal
}

func TestAccountStartupExecutionContractLoadsOnlyOriginalCompletedWriteFailure(t *testing.T) {
	for _, mode := range []string{"original", "missing-lease", "accepted", "before-acceptance", "uncertain-write", "different-failure", "missing-owner", "missing-completion", "foreign-intent"} {
		t.Run(mode, func(t *testing.T) {
			f, _, _ := accountStartupExecutionContract(t)
			original, journal, err := loadOriginalAccountStartupCheckpoint(f.store, f.e, &f.p)
			if err != nil || original.SHA256() != f.child.SHA256() || deploymentplan.Digest(journal) != deploymentplan.Digest(f.journal) {
				t.Fatal("genuine original account write history rejected", err)
			}
			switch mode {
			case "missing-lease":
				f.e.Journal.KubernetesLeases = nil
			case "accepted":
				f.journal.Accepted = true
			case "before-acceptance":
				f.journal.AcceptanceStarted = false
			case "uncertain-write":
				f.journal.InFlight = f.child.Resources[0].ID
			case "different-failure":
				f.journal.Failed = f.child.Resources[0].ID
			case "missing-owner":
				delete(f.journal.Owners, f.child.Resources[0].ID)
			case "missing-completion":
				f.journal.Completed = f.journal.Completed[:1]
			case "foreign-intent":
				f.e.Journal.Entries[len(f.e.Journal.Entries)-1].IntentSHA256 = deploymentplan.Digest("foreign account write")
			}
			f.saveJournal(t)
			if err := f.e.save(); err != nil {
				t.Fatal(err)
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			_, _, err = loadOriginalAccountStartupCheckpoint(f.store, f.e, &f.p)
			if (err == nil) != (mode == "original") {
				t.Fatal("unexpected account startup history decision", err)
			}
			if !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, f.store.Root)) {
				t.Fatal("loading account recovery changed original owners or writes")
			}
		})
	}
}

func TestAccountEmailRepairExecutionContractBindsOriginalUnusedSeedAndOwners(t *testing.T) {
	for _, mode := range []string{"original", "wrong-parent", "wrong-child", "wrong-secret-owner", "wrong-deployment-owner", "not-complete", "other-fields-changed", "existing-outbox", "wrong-original-file", "wrong-derivation", "missing-restart", "changed-original", "changed-current", "already-valid-original", "foreign-foundation-key", "wrong-directory", "missing-proof"} {
		t.Run(mode, func(t *testing.T) {
			f, foundation, foundationJournal := accountStartupExecutionContract(t)
			seed := []byte("original-opaque-seed\n")
			keyHash := sha256.Sum256([]byte(strings.TrimSpace(string(seed))))
			key := base64.StdEncoding.EncodeToString(keyHash[:])
			current := []byte(key + "\n")
			base := "deployment/repairs/account-email-format-isolated"
			path := filepath.Join(f.store.Root, base, "verified.json")
			secretID := "Secret/video-cloud-dev-account-manager/account-manager-runtime"
			proof := map[string]any{
				"schema": "rtk-cloud.initial-email-key-format-repair/v1", "environment": "dev", "parent_plan_sha256": f.p.ParentSHA,
				"original_child_plan_sha256": f.child.SHA256(), "secret_uid": foundationJournal.Owners[secretID],
				"deployment_uid":          f.journal.Owners["Deployment/video-cloud-dev-account-manager/account-manager"],
				"original_runtime_sha256": environmentPreflightBytesSHA256(seed), "runtime_sha256": environmentPreflightBytesSHA256(current),
				"original_file": "original-runtime-value", "derivation": "base64-standard-of-sha256-of-original-opaque-seed",
				"outbox_rows": 0, "outbox_table_present": true, "only_email_secret_field_changed": true, "pod_restart_uid": "isolated-original-restart", "completed": true,
			}
			write := func(relative string, raw []byte) {
				t.Helper()
				if err := f.store.write(relative, raw, true); err != nil {
					t.Fatal(err)
				}
			}
			write(filepath.Join(base, "original-runtime-value"), seed)
			write("runtime/email-outbox-encryption", current)
			write(filepath.Join(base, "verified.json"), mustProviderRecoveryJSON(proof))
			got, sha, err := verifyOriginalAccountEmailRepair(f.store, f.e, path, foundation, foundationJournal, f.child, f.journal)
			if err != nil || got != base64.StdEncoding.EncodeToString([]byte(key)) || sha != environmentPreflightBytesSHA256(mustProviderRecoveryJSON(proof)) {
				t.Fatal("genuine narrowly scoped seed repair was rejected", err)
			}
			switch mode {
			case "wrong-parent":
				proof["parent_plan_sha256"] = deploymentplan.Digest("foreign")
			case "wrong-child":
				proof["original_child_plan_sha256"] = deploymentplan.Digest("foreign")
			case "wrong-secret-owner":
				proof["secret_uid"] = "replacement"
			case "wrong-deployment-owner":
				proof["deployment_uid"] = "replacement"
			case "not-complete":
				proof["completed"] = false
			case "other-fields-changed":
				proof["only_email_secret_field_changed"] = false
			case "existing-outbox":
				proof["outbox_rows"] = 1
			case "wrong-original-file":
				proof["original_file"] = "../foreign"
			case "wrong-derivation":
				proof["derivation"] = "different algorithm"
			case "missing-restart":
				proof["pod_restart_uid"] = ""
			case "changed-original":
				write(filepath.Join(base, "original-runtime-value"), []byte("different original"))
			case "changed-current":
				write("runtime/email-outbox-encryption", []byte("different current"))
			case "already-valid-original":
				validOriginal := []byte(base64.StdEncoding.EncodeToString(make([]byte, 32)))
				write(filepath.Join(base, "original-runtime-value"), validOriginal)
				proof["original_runtime_sha256"] = environmentPreflightBytesSHA256(validOriginal)
			case "foreign-foundation-key":
				deploymentplan.Map(foundation.Resources[0].Desired["data"])["EMAIL_OUTBOX_ENCRYPTION_KEY"] = base64.StdEncoding.EncodeToString([]byte("foreign seed"))
			case "wrong-directory":
				path = filepath.Join(f.store.Root, "deployment/repairs/unqualified/verified.json")
			}
			write(filepath.Join(base, "verified.json"), mustProviderRecoveryJSON(proof))
			if mode == "missing-proof" {
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			_, _, err = verifyOriginalAccountEmailRepair(f.store, f.e, path, foundation, foundationJournal, f.child, f.journal)
			if (err == nil) != (mode == "original") {
				t.Fatal("unexpected original unused seed repair decision", err)
			}
			if !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, f.store.Root)) {
				t.Fatal("repair verification changed original key or receipt")
			}
		})
	}
}
