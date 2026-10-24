package main

import (
	"context"
	"encoding/json"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Only the separately qualified original export/retirement checkpoint may
// re-read registered material. Its completed receipt and pending deletion intent
// remain immutable; ordinary create stages still prohibit automatic replay.
func managedTransportExportRetireState(runtime environmentCreateBootstrapRuntime) *createManagedTransportExportRetire {
	r := runtime.ServiceSessionRecovery
	if r == nil || r.ManagedTransportRecovery == nil || r.ManagedTransportRecovery.Partial == nil || r.ManagedTransportRecovery.Partial.PostManaged == nil {
		return nil
	}
	return r.ManagedTransportRecovery.Partial.PostManaged.ExportRetire
}

func verifyManagedTransportRetainedServerExport(runtime environmentCreateBootstrapRuntime, owner string, intent, proof json.RawMessage) (bool, error) {
	retained := managedTransportExportRetireState(runtime)
	if retained == nil || owner != "video-cloud-api-pki" {
		return false, nil
	}
	bad := func() (bool, error) {
		return false, createProviderRecoveryBlock("original completed native export intent or registered material changed; receipt replay is prohibited")
	}
	if runtime.Execution == nil {
		return bad()
	}
	wantedIntent, err := environmentCreatePlanSHA256(intent)
	var wanted deploymentplan.Object
	if err != nil || json.Unmarshal(proof, &wanted) != nil {
		return bad()
	}
	count := 0
	for _, entry := range runtime.Execution.Journal.Entries {
		if entry.Key != "server-export-"+owner {
			continue
		}
		var observed deploymentplan.Object
		if activeTransportJournalDigest(entry) != retained.ExportSHA || entry.Phase != "bootstrap" || entry.State != "completed" || entry.CompletedAt.IsZero() || entry.IntentSHA256 != wantedIntent || json.Unmarshal(entry.Receipt, &observed) != nil || deploymentplan.Digest(observed) != deploymentplan.Digest(wanted) {
			return bad()
		}
		count++
	}
	if count != 1 {
		return bad()
	}
	return true, runtime.Execution.Check(context.Background())
}

func verifyManagedTransportPendingExportRetirement(runtime environmentCreateBootstrapRuntime, id, uid string) (bool, error) {
	retained := managedTransportExportRetireState(runtime)
	if retained == nil {
		return false, nil
	}
	if runtime.Execution == nil || !environmentPreflightDigest.MatchString(runtime.Execution.Journal.PlanSHA256) {
		return false, createProviderRecoveryBlock("original reader retirement lacks its active qualified execution")
	}
	expected := "Deployment/" + runtime.Execution.Journal.Stack + "-video-cloud/pki-export-video-cloud-api-pki-" + runtime.Execution.Journal.PlanSHA256[:12]
	if id != expected {
		return false, nil
	}
	bad := func() (bool, error) {
		return false, createProviderRecoveryBlock("original unattempted reader retirement requires its exact admitted pending intent and retained UID")
	}
	if uid != retained.ReaderUID {
		return bad()
	}
	intent, _ := json.Marshal(deploymentplan.Object{"resource_id": id, "uid": uid})
	wantedIntent, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		return bad()
	}
	count := 0
	for _, entry := range runtime.Execution.Journal.Entries {
		if entry.Key != "retire-"+strings.TrimPrefix(id, "Deployment/"+runtime.Execution.Journal.Stack+"-video-cloud/") {
			continue
		}
		if activeTransportJournalDigest(entry) != retained.RetireSHA || entry.Phase != "bootstrap" || entry.State != "pending" || !entry.CompletedAt.IsZero() || len(entry.Receipt) != 0 || entry.IntentSHA256 != wantedIntent {
			return bad()
		}
		count++
	}
	if count != 1 {
		return bad()
	}
	return true, runtime.Execution.Check(context.Background())
}
