package main

import (
	"encoding/json"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedLateResidualRetirementsRequireEveryApprovedUIDAndWindow(t *testing.T) {
	for _, mode := range []string{"all-approved", "missing", "wrong-uid", "wrong-resource", "duplicate", "early", "late", "pending", "wrong-intent", "wrong-absence"} {
		t.Run(mode, func(t *testing.T) {
			r, _, now := managedLateResidualAuditFixture(t)
			a := r.ResidualCleanup.Approval
			j := environmentCreateJournal{Entries: make([]environmentCreateJournalEntry, 400)}
			for _, object := range a.Objects {
				intent := deploymentplan.Object{"resource_id": object.ResourceID, "uid": object.UID, "session_id": object.SessionID}
				j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: "bootstrap-copy-retire-" + object.Namespace + "-" + object.SessionID, Phase: "bootstrap", State: "completed", StartedAt: now, CompletedAt: now.Add(time.Second), IntentSHA256: activeTransportJournalDigest(intent), Receipt: mustProviderRecoveryJSON(deploymentplan.Object{"resource_id": object.ResourceID, "uid": object.UID, "absent": true})})
			}
			entry := &j.Entries[400]
			switch mode {
			case "missing":
				j.Entries = j.Entries[:423]
			case "wrong-uid", "wrong-resource", "wrong-absence":
				var receipt deploymentplan.Object
				if err := json.Unmarshal(entry.Receipt, &receipt); err != nil {
					t.Fatal(err)
				}
				if mode == "wrong-uid" {
					receipt["uid"] = a.Objects[1].UID
				}
				if mode == "wrong-resource" {
					receipt["resource_id"] = a.Objects[1].ResourceID
				}
				if mode == "wrong-absence" {
					receipt["absent"] = false
				}
				entry.Receipt = mustProviderRecoveryJSON(receipt)
			case "duplicate":
				j.Entries = append(j.Entries, *entry)
			case "early":
				entry.StartedAt = now.Add(-time.Nanosecond)
			case "late":
				entry.CompletedAt = now.Add(60*time.Minute + time.Nanosecond)
			case "pending":
				entry.State = "pending"
			case "wrong-intent":
				entry.IntentSHA256 = r.OwnerSHA
			}
			before := deploymentplan.Digest([]any{j, a})
			if managedLateResidualRetirementsComplete(j, a, now, now.Add(60*time.Minute)) != (mode == "all-approved") || before != deploymentplan.Digest([]any{j, a}) {
				t.Fatal("residual completion lost exact approved object, receipt or immutable window")
			}
		})
	}
}

func TestManagedLateResidualCopyPermissionIsExactApprovedObject(t *testing.T) {
	r, _, _ := managedLateResidualAuditFixture(t)
	recovery := &createServiceSessionRecovery{ManagedLateRemaining: r}
	object := r.ResidualCleanup.Approval.Objects[0]
	s := environmentCreateServiceCeremony{BootstrapSessionID: object.SessionID, BootstrapCredentialSecret: "service-bootstrap-" + object.SessionID, CredentialUIDs: map[string]string{object.Namespace: object.UID}}
	if !managedLateResidualCopyAllowed(recovery, s, object.Namespace) {
		t.Fatal("approved object refused")
	}
	s.CredentialUIDs[object.Namespace] = r.ResidualCleanup.Approval.Objects[1].UID
	if managedLateResidualCopyAllowed(recovery, s, object.Namespace) {
		t.Fatal("replacement UID gained permission")
	}
	if managedLateResidualCopyAllowed(nil, s, object.Namespace) {
		t.Fatal("unproved profile gained permission")
	}
}
