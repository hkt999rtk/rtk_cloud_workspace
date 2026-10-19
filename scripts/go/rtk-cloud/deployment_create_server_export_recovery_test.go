package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func originalExportRecoveryFixture(t *testing.T) (environmentCreateBootstrapRuntime, json.RawMessage, json.RawMessage, string, string) {
	t.Helper()
	cfg, _, operator, artifact := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	uid := "original-reader-uid"
	name := "pki-export-video-cloud-api-pki-" + e.Journal.PlanSHA256[:12]
	id := "Deployment/" + e.Journal.Stack + "-video-cloud/" + name
	intent := mustProviderRecoveryJSON(deploymentplan.Object{"owner": "video-cloud-api-pki", "namespace": e.Journal.Stack + "-video-cloud", "claim_uid": "original-claim", "reader_uid": uid, "pod_uid": "original-pod", "replicaset_uid": "original-rs", "server_name": "original-server", "root_sha256": strings.Repeat("a", 64)})
	proof := mustProviderRecoveryJSON(deploymentplan.Object{"certificate_sha256": strings.Repeat("b", 64), "claim_uid": "original-claim", "reader_uid": uid, "pod_uid": "original-pod", "replicaset_uid": "original-rs", "server_name": "original-server"})
	key := "server-export-video-cloud-api-pki"
	if err = e.BeginEntry(key, "bootstrap", intent, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err = e.RecordReceipt(key, proof); err != nil {
		t.Fatal(err)
	}
	if err = e.CompleteEntry(key, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	exported := e.Journal.Entries[len(e.Journal.Entries)-1]
	if err = e.BeginEntry("retire-"+name, "bootstrap", mustProviderRecoveryJSON(deploymentplan.Object{"resource_id": id, "uid": uid}), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	pending := e.Journal.Entries[len(e.Journal.Entries)-1]
	retained := &createManagedTransportExportRetire{ExportSHA: activeTransportJournalDigest(exported), RetireSHA: activeTransportJournalDigest(pending), ReaderUID: uid}
	runtime := environmentCreateBootstrapRuntime{Execution: e, ServiceSessionRecovery: &createServiceSessionRecovery{ManagedTransportRecovery: &createManagedTransportRecovery{Partial: &createManagedTransportPartialRecovery{PostManaged: &createManagedTransportPostManaged{ExportRetire: retained}}}}}
	return runtime, intent, proof, id, uid
}

func TestOriginalExportRecoveryVerifiesWithoutRewritingCompletedReceipt(t *testing.T) {
	for _, mode := range []string{"valid", "ordinary", "changed-certificate", "changed-owner", "changed-intent", "changed-entry", "duplicate-entry", "missing-entry"} {
		t.Run(mode, func(t *testing.T) {
			runtime, intent, proof, _, _ := originalExportRecoveryFixture(t)
			switch mode {
			case "ordinary":
				runtime.ServiceSessionRecovery = nil
			case "changed-certificate", "changed-owner":
				var value deploymentplan.Object
				if json.Unmarshal(proof, &value) != nil {
					t.Fatal("invalid fixture")
				}
				if mode == "changed-certificate" {
					value["certificate_sha256"] = strings.Repeat("c", 64)
				} else {
					value["pod_uid"] = "replacement-pod"
				}
				proof = mustProviderRecoveryJSON(value)
			case "changed-intent":
				intent = mustProviderRecoveryJSON(deploymentplan.Object{"owner": "replacement-owner"})
			case "changed-entry":
				runtime.Execution.Journal.Entries[len(runtime.Execution.Journal.Entries)-2].Receipt = mustProviderRecoveryJSON(deploymentplan.Object{"unreviewed": true})
			case "duplicate-entry":
				runtime.Execution.Journal.Entries = append(runtime.Execution.Journal.Entries, runtime.Execution.Journal.Entries[len(runtime.Execution.Journal.Entries)-2])
			case "missing-entry":
				runtime.Execution.Journal.Entries = runtime.Execution.Journal.Entries[len(runtime.Execution.Journal.Entries)-1:]
			}
			before := deploymentplan.Digest(runtime.Execution.Journal)
			reused, err := verifyManagedTransportRetainedServerExport(runtime, "video-cloud-api-pki", intent, proof)
			if mode == "ordinary" {
				if reused || err != nil {
					t.Fatal("ordinary create acquired replay permission", err)
				}
			} else if (mode == "valid") != (reused && err == nil) {
				t.Fatal("changed original export evidence admitted", mode, reused, err)
			}
			if deploymentplan.Digest(runtime.Execution.Journal) != before {
				t.Fatal("completed export receipt was rewritten")
			}
		})
	}
}

func TestOriginalExportRetirementRequiresItsExactUnattemptedIntent(t *testing.T) {
	for _, mode := range []string{"valid", "ordinary", "changed-uid", "changed-intent", "allocated", "duplicate-entry", "missing-entry"} {
		t.Run(mode, func(t *testing.T) {
			runtime, _, _, id, uid := originalExportRecoveryFixture(t)
			switch mode {
			case "ordinary":
				runtime.ServiceSessionRecovery = nil
			case "changed-uid":
				uid = "replacement-reader"
			case "changed-intent":
				runtime.Execution.Journal.Entries[len(runtime.Execution.Journal.Entries)-1].IntentSHA256 = strings.Repeat("c", 64)
			case "allocated":
				runtime.Execution.Journal.Entries[len(runtime.Execution.Journal.Entries)-1].State = "allocated"
			case "duplicate-entry":
				runtime.Execution.Journal.Entries = append(runtime.Execution.Journal.Entries, runtime.Execution.Journal.Entries[len(runtime.Execution.Journal.Entries)-1])
			case "missing-entry":
				runtime.Execution.Journal.Entries = runtime.Execution.Journal.Entries[:len(runtime.Execution.Journal.Entries)-1]
			}
			before := deploymentplan.Digest(runtime.Execution.Journal)
			reused, err := verifyManagedTransportPendingExportRetirement(runtime, id, uid)
			if mode == "ordinary" {
				if reused || err != nil {
					t.Fatal("ordinary create acquired retirement replay permission", err)
				}
			} else if (mode == "valid") != (reused && err == nil) {
				t.Fatal("changed or attempted retirement admitted", mode, reused, err)
			}
			if deploymentplan.Digest(runtime.Execution.Journal) != before {
				t.Fatal("original pending retirement intent was rewritten")
			}
		})
	}
}
