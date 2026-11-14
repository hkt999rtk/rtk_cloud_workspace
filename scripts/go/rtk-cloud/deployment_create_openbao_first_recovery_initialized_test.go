package main

import (
	"encoding/json"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Unit test only: these synthetic journal entries exercise the reader, not an
// assertion that a real OpenBao initialization or auth operation occurred.
func TestOpenBaoFirstCompletedInitRejectsUncertainReceipts(t *testing.T) {
	keys := []string{"openbao-initialize", "openbao-unseal", "openbao-audit-enable", "openbao-auth-enable", "openbao-auth-config"}
	modes := []string{"complete", "changed-intent", "changed-receipt"}
	for _, key := range keys {
		for _, state := range []string{"missing", "pending", "allocated"} {
			modes = append(modes, state+":"+key)
		}
	}
	for _, mode := range modes {
		t.Run(mode, func(t *testing.T) {
			_, store, e := openBaoFirstTransportFixture(t)
			if err := store.write("openbao/unseal-key", []byte("synthetic-unseal\n"), false); err != nil {
				t.Fatal(err)
			}
			pod, set := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
			enable := deploymentplan.Object{"path": "auth/kubernetes", "type": "kubernetes"}
			config := deploymentplan.Object{"path": "auth/kubernetes/config", "reviewer": "rotating-local-projection", "host": "https://kubernetes.default.svc:443"}
			intents := []deploymentplan.Object{
				{"pod_uid": pod, "statefulset_uid": set, "key_shares": 1, "key_threshold": 1},
				{"pod_uid": pod, "unseal_sha256": environmentPreflightBytesSHA256([]byte("synthetic-unseal"))},
				{"path": "sys/audit/deployment-audit", "type": "file", "file_path": "/openbao/audit/audit.jsonl", "log_raw": false},
				enable, config,
			}
			receipts := []any{
				deploymentplan.Object{"init_sha256": deploymentplan.Digest("original-init"), "pod_uid": pod, "statefulset_uid": set, "root_token_ref": "openbao/root-token", "unseal_ref": "openbao/unseal-key"},
				lkeOpenBaoStatus{Initialized: true, Sealed: false},
				deploymentplan.Object{"audit_sha256": deploymentplan.Digest("actual-audit")},
				deploymentplan.Object{"configured": enable, "readback_sha256": deploymentplan.Digest("actual-auth")},
				deploymentplan.Object{"configured": config, "readback_sha256": deploymentplan.Digest("actual-config")},
			}
			for i, key := range keys {
				if mode == "missing:"+key {
					break
				}
				intent, _ := json.Marshal(intents[i])
				if err := e.BeginEntry(key, "bootstrap", intent, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
				if mode == "pending:"+key {
					break
				}
				receipt, _ := json.Marshal(receipts[i])
				if err := e.RecordReceipt(key, receipt); err != nil {
					t.Fatal(err)
				}
				if mode == "allocated:"+key {
					break
				}
				if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "changed-intent" {
				e.Journal.Entries[0].IntentSHA256 = deploymentplan.Digest("foreign")
				if err := e.save(); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "changed-receipt" {
				e.Journal.Entries[1].Receipt = []byte(`{"initialized":true,"sealed":true}`)
				if err := e.save(); err != nil {
					t.Fatal(err)
				}
			}
			before := deploymentplan.Digest(e.Journal)
			_, err := validateOpenBaoFirstCompletedInit(t.Context(), store, e)
			if mode == "complete" && err != nil {
				t.Fatal("complete synthetic receipt chain rejected", err)
			}
			if mode != "complete" && err == nil {
				t.Fatal("uncertain or changed original receipt accepted")
			}
			if deploymentplan.Digest(e.Journal) != before {
				t.Fatal("read-only qualification modified original journal")
			}
		})
	}
}
