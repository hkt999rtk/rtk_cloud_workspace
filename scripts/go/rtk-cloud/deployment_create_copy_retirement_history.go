package main

import (
	"context"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Secondary bootstrap copies are deleted before native finalization. That
// completed native DELETE is part of owner history even if finalization stops.
// Missing resources alone, pending intents and replacement UIDs are not proof.
func completedBootstrapCopyRetirementsAfter(ctx context.Context, e *environmentCreateExecution, key string, owners map[string]deploymentplan.Resource) (map[string]string, error) {
	bad := func() (map[string]string, error) {
		return nil, createProviderRecoveryBlock("original bootstrap copy retirement lacks its exact completed intent, UID and absence receipt")
	}
	if e == nil || e.Check(ctx) != nil {
		return bad()
	}
	start := -1
	for i, entry := range e.Journal.Entries {
		if entry.Key == key && entry.State == "completed" {
			if start >= 0 {
				return bad()
			}
			start = i
		}
	}
	if start < 0 {
		return bad()
	}
	retired := map[string]string{}
	for _, entry := range e.Journal.Entries[start+1:] {
		if !strings.HasPrefix(entry.Key, "bootstrap-copy-retire-") {
			continue
		}
		var receipt struct {
			ID     string `json:"resource_id"`
			UID    string `json:"uid"`
			Absent bool   `json:"absent"`
		}
		if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil {
			// No completed deletion may be inferred from an unconfirmed intent.
			if entry.State == "completed" {
				return bad()
			}
			continue
		}
		owner, tracked := owners[receipt.ID]
		if !tracked {
			continue
		}
		parts := strings.Split(receipt.ID, "/")
		if len(parts) != 3 || parts[0] != "Secret" || !strings.HasPrefix(parts[1], e.Journal.Stack+"-") {
			return bad()
		}
		session, ok := strings.CutPrefix(parts[2], "service-bootstrap-")
		intent := deploymentplan.Object{"resource_id": receipt.ID, "uid": receipt.UID, "session_id": session}
		if !ok || !environmentBootstrapOwnerUUID.MatchString(session) || entry.Key != "bootstrap-copy-retire-"+parts[1]+"-"+session || entry.Phase != "bootstrap" || entry.State != "completed" || entry.StartedAt.Before(e.Journal.Entries[start].CompletedAt) || entry.CompletedAt.Before(entry.StartedAt) || entry.CompletedAt.IsZero() || entry.CompletedAt.After(time.Now().UTC()) || !receipt.Absent || receipt.UID != owner.UID || entry.IntentSHA256 != activeTransportJournalDigest(intent) || retired[receipt.ID] != "" {
			return bad()
		}
		retired[receipt.ID] = receipt.UID
	}
	return retired, e.Check(ctx)
}
