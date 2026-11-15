package main

import (
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestRecordedManagedOpenBaoReplacementUsesOriginalOwnerAndUnseal(t *testing.T) {
	set := deploymentplan.Object{"kind": "StatefulSet", "metadata": deploymentplan.Object{"name": "openbao", "namespace": "ns", "uid": "original"}, "spec": deploymentplan.Object{"replicas": float64(1)}, "status": deploymentplan.Object{"updateRevision": "new-revision"}}
	pod := deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "new-pod", "labels": deploymentplan.Object{"controller-revision-hash": "new-revision"}}}
	writes := []activeTransportRenewalResource{{ID: deploymentplan.ID(set), UID: "original", Desired: deploymentplan.Clone(set)}}
	delete(writes[0].Desired, "status")
	intent := deploymentplan.Object{"pod_uid": "new-pod", "controller_uid": "original", "revision": "new-revision", "original_init_sha256": "original-init", "unseal_sha256": environmentPreflightBytesSHA256([]byte("original-share"))}
	receipt := deploymentplan.Object{"pod_uid": "new-pod", "initialized": true, "sealed": false, "revision": "new-revision"}
	j := environmentCreateJournal{Entries: []environmentCreateJournalEntry{{Key: "openbao-managed-unseal-new-pod", Phase: "openbao-unseal", State: "completed", IntentSHA256: deploymentplan.Digest(intent), CompletedAt: time.Now(), Receipt: mustProviderRecoveryJSON(receipt)}}}
	if !recordedManagedOpenBaoReplacement(j, writes, set, pod, "original-init", "original-share") {
		t.Fatal("recorded replacement with original identity rejected")
	}
	if recordedManagedOpenBaoReplacement(j, writes, set, pod, "original-init", "other-share") {
		t.Fatal("accepted a foreign unseal share")
	}
	writes[0].UID = "foreign"
	if recordedManagedOpenBaoReplacement(j, writes, set, pod, "original-init", "original-share") {
		t.Fatal("accepted a replacement StatefulSet owner")
	}
}
