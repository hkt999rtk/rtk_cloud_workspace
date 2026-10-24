package main

import (
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A standard child can unseal its replaced original OpenBao Pod while its
// outer Kubernetes entry is still pending. This is a nested native receipt,
// not permission to skip an arbitrary journal key or an unknown outcome.
func validateCreateNormalTailNestedUnseals(store secretStore, e *environmentCreateExecution, child *deploymentplan.Plan, journal deploymentplan.Journal, outer environmentCreateJournalEntry, following []environmentCreateJournalEntry, admission createNormalTailProgramAdmission) (int, error) {
	count := 0
	for count < len(following) && strings.HasPrefix(following[count].Key, "openbao-managed-unseal-") {
		count++
	}
	if count == 0 {
		return 0, nil
	}
	bad := func() (int, error) {
		return 0, createProviderRecoveryBlock("nested OpenBao unseal differs from its original child owner or completed receipt")
	}
	if e == nil || e.Store != store || child == nil || child.CreateParentSHA256 != e.Journal.PlanSHA256 || child.Stack != e.Journal.Stack || journal.PlanSHA256 != child.SHA256() || journal.InFlight != "" {
		return bad()
	}
	id := "StatefulSet/" + child.Stack + "-secrets/openbao"
	var owner deploymentplan.Resource
	for _, resource := range child.Resources {
		if resource.ID == id {
			owner = resource
		}
	}
	if !environmentOnDeleteOpenBao(owner) || owner.Action != "update" && owner.Action != "retain" || !environmentBootstrapOwnerUUID.MatchString(owner.UID) || journal.Owners[id] != owner.UID {
		return bad()
	}
	// Reuse the same original-share/Pod/revision intent validator as the
	// existing native producer, with its owner supplied by this genuine child.
	proof := &createManagedLateSessionRecovery{AcceptedCore: child, Owners: map[string]deploymentplan.Resource{id: owner}}
	previous := outer.StartedAt
	seen := map[string]bool{}
	for _, entry := range following[:count] {
		if seen[entry.Key] || entry.StartedAt.Before(previous) || entry.StartedAt.Before(admission.AdmittedAt) || !entry.StartedAt.Before(admission.Deadline) || entry.CompletedAt.Before(entry.StartedAt) || !entry.CompletedAt.Before(admission.Deadline) || entry.CompletedAt.After(time.Now().UTC()) || outer.State == "completed" && entry.CompletedAt.After(outer.CompletedAt) || validateManagedLateUnseal(store, proof, entry) != nil {
			return bad()
		}
		seen[entry.Key] = true
		previous = entry.CompletedAt
	}
	return count, nil
}
