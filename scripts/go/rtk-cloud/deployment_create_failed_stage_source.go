package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// readLatestAcceptedCreateResource derives a resource from the latest original
// completed child, not from an unproven live ConfigMap or another environment.
func readLatestAcceptedCreateResource(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, id string) (deploymentplan.Object, error) {
	bad := func() (deploymentplan.Object, error) {
		return nil, createProviderRecoveryBlock("original latest accepted resource, journal owner and current desired body are required")
	}
	if e == nil || e.Store != store || b == nil || id == "" || e.Check(ctx) != nil {
		return bad()
	}
	for i := len(e.Journal.Entries) - 1; i >= 0; i-- {
		entry := e.Journal.Entries[i]
		if entry.State != "completed" || !strings.HasPrefix(entry.Key, "kubernetes/") {
			continue
		}
		var receipt struct {
			Plan   string            `json:"plan_sha256"`
			Owners map[string]string `json:"owners"`
		}
		if json.Unmarshal(entry.Receipt, &receipt) != nil || !environmentPreflightDigest.MatchString(receipt.Plan) {
			continue
		}
		var tentative deploymentplan.Plan
		if readExecutionJSON(filepath.Join(store.Root, "deployment", "rollback", receipt.Plan, "plan.json"), &tentative) != nil || tentative.SHA256() != receipt.Plan {
			return bad()
		}
		var resource *deploymentplan.Resource
		for n := range tentative.Resources {
			if tentative.Resources[n].ID == id {
				resource = &tentative.Resources[n]
				break
			}
		}
		if resource == nil {
			continue
		}
		plan, j, err := acceptedCreateStage(store, e, entry.Key)
		if err != nil || plan.SHA256() != receipt.Plan {
			return bad()
		}
		original, exists, err := plan.ReadExecution(store.Root)
		if err != nil || !exists || deploymentplan.Digest(original) != deploymentplan.Digest(j) || deploymentplan.Digest(receipt.Owners) != deploymentplan.Digest(j.Owners) || j.Owners[id] == "" || receipt.Owners[id] != j.Owners[id] || resource.Action == "retire" {
			return bad()
		}
		actual, err := b.Read(ctx, id)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != j.Owners[id] || !deploymentplan.Matches(resource.Desired, actual) || e.Check(ctx) != nil {
			return bad()
		}
		return deploymentplan.Clone(resource.Desired), nil
	}
	return bad()
}
