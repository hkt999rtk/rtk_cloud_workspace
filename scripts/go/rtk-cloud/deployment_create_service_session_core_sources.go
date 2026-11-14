package main

import (
	"context"
	"encoding/json"
	"sort"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// serviceSessionCoreOwnerIDs discovers the latest recorded core owners from
// the original parent journals. Excluded IDs are supplied by the separately
// authenticated interrupted application lineage, never by live inventory.
func serviceSessionCoreOwnerIDs(store secretStore, e *environmentCreateExecution, previous []string, excluded map[string]bool) ([]string, error) {
	bad := func() ([]string, error) {
		return nil, createProviderRecoveryBlock("Service core session owner history is unavailable")
	}
	if e == nil || e.Store != store || len(previous) == 0 || e.Check(context.Background()) != nil {
		return bad()
	}
	seenSID := map[string]bool{}
	for _, sid := range previous {
		if !environmentBootstrapOwnerUUID.MatchString(sid) || seenSID[sid] {
			return bad()
		}
		seenSID[sid] = true
	}
	history, _, err := serviceSessionOwnerHistory(context.Background(), store, e)
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	var ids []string
	for i := len(history) - 1; i >= 0; i-- {
		p, j := history[i].Plan, history[i].Journal
		if p == nil || p.Environment != e.Journal.Environment || p.Stack != e.Journal.Stack {
			return bad()
		}
		for n, r := range p.Resources {
			written := n < len(j.Completed) && j.Completed[n] == r.ID || j.Failed == r.ID && j.Owners[r.ID] != ""
			if !written || seen[r.ID] || excluded[r.ID] {
				continue
			}
			seen[r.ID] = true
			if r.Action == "retire" {
				continue
			}
			switch r.Desired["kind"] {
			case "Deployment", "StatefulSet", "DaemonSet", "Job":
			default:
				continue
			}
			if r.ID == "" || j.Owners[r.ID] == "" || deploymentplan.ID(r.Desired) != r.ID {
				return bad()
			}
			for _, sid := range previous {
				slots, err := serviceSessionSIDSlots(r.Desired, sid, "")
				if err != nil {
					return bad()
				}
				if slots > 0 {
					ids = append(ids, r.ID)
					break
				}
			}
		}
	}
	if e.Check(context.Background()) != nil {
		return bad()
	}
	sort.Strings(ids)
	return ids, nil
}

// loadServiceSessionCoreSources verifies explicit original core owners against
// their latest accepted child and the live UID and desired state. Application
// owners with interrupted journals use the separately verified q2 lineage.
func loadServiceSessionCoreSources(ctx context.Context, store secretStore, e *environmentCreateExecution, b deploymentplan.Backend, ids []string) ([]serviceSessionRecordedPlan, error) {
	boundary, owner := "original-parent", ""
	bad := func() ([]serviceSessionRecordedPlan, error) {
		err := createProviderRecoveryBlock("Service core owner lacks its recorded original stage and live UID; boundary=" + boundary)
		if detail, ok := err.(*deploymentplan.Blocker); ok {
			detail.Resource = owner
		}
		return nil, err
	}
	if e == nil || e.Store != store || b == nil || len(ids) == 0 || e.Check(ctx) != nil {
		return bad()
	}
	wanted := map[string]bool{}
	for _, id := range ids {
		if id == "" || wanted[id] {
			return bad()
		}
		wanted[id] = true
	}
	var sources []serviceSessionRecordedPlan
	boundary = "recorded-owner-history"
	later, stageOrder, err := serviceSessionOwnerHistory(ctx, store, e)
	if err != nil {
		return nil, err
	}
	for i := len(e.Journal.Entries) - 1; i >= 0 && len(wanted) > 0; i-- {
		entry := e.Journal.Entries[i]
		if entry.State != "completed" || !strings.HasPrefix(entry.Key, "kubernetes/") {
			continue
		}
		var receipt struct {
			Plan   string            `json:"plan_sha256"`
			Owners map[string]string `json:"owners"`
		}
		if json.Unmarshal(entry.Receipt, &receipt) != nil {
			continue
		}
		selected := map[string]bool{}
		for id := range wanted {
			if receipt.Owners[id] != "" {
				selected[id] = true
			}
		}
		if len(selected) == 0 {
			continue
		}
		boundary, owner = "accepted-core-stage", ""
		p, j, err := acceptedCreateStage(store, e, entry.Key)
		if err != nil || p == nil || p.Environment != e.Journal.Environment || p.Stack != e.Journal.Stack || receipt.Plan != p.SHA256() || deploymentplan.Digest(receipt.Owners) != deploymentplan.Digest(j.Owners) {
			return bad()
		}
		for id := range selected {
			owner, boundary = id, "original-resource"
			var resource *deploymentplan.Resource
			position := -1
			for n := range p.Resources {
				if p.Resources[n].ID == id {
					resource = &p.Resources[n]
					position = n
					break
				}
			}
			uid := j.Owners[id]
			if resource == nil || position >= len(j.Completed) || j.Completed[position] != id || deploymentplan.ID(resource.Desired) != id || resource.Action == "retire" || uid == "" || uid != receipt.Owners[id] || resource.UID != "" && resource.UID != uid {
				return bad()
			}
			boundary = "live-original-uid"
			live, err := b.Read(ctx, id)
			if err != nil || live == nil || deploymentplan.Map(live["metadata"])["uid"] != uid {
				return bad()
			}
			// Matches is intentionally a subset match: the live object may still
			// match the old body after an admitted update adds a new field. Always
			// follow recorded later writes before selecting the authoritative owner.
			base := serviceSessionRecordedPlan{Stage: strings.TrimPrefix(entry.Key, "kubernetes/"), Plan: p, Journal: j, SelectedIDs: map[string]bool{id: true}}
			boundary = "recorded-later-effects"
			latest, ok := followServiceSessionCoreOwner(base, id, uid, resource.Desired, live, later, stageOrder, i)
			if !ok {
				return bad()
			}
			if latest.Stage != base.Stage {
				sources = append(sources, latest)
				delete(selected, id)
			}
			delete(wanted, id)
		}
		if len(selected) > 0 {
			sources = append(sources, serviceSessionRecordedPlan{Stage: strings.TrimPrefix(entry.Key, "kubernetes/"), Plan: p, Journal: j, SelectedIDs: selected})
		}
	}
	// An ordinary original create can establish a new owner before the whole
	// workload reaches acceptance. Its literal known create outcome supplies
	// the birth UID; later updates must extend that exact native baseline.
	for _, source := range later {
		for n, r := range source.Plan.Resources {
			if !wanted[r.ID] || r.Action != "create" {
				continue
			}
			j := source.Journal
			written := n < len(j.Completed) && j.Completed[n] == r.ID || j.Failed == r.ID && j.Owners[r.ID] != ""
			if !written || j.Owners[r.ID] == "" {
				continue
			}
			owner, boundary = r.ID, "known-original-create"
			uid := j.Owners[r.ID]
			if source.Plan.Environment != e.Journal.Environment || source.Plan.Stack != e.Journal.Stack || deploymentplan.ID(r.Desired) != r.ID {
				return bad()
			}
			live, err := b.Read(ctx, r.ID)
			if err != nil || live == nil || deploymentplan.Map(live["metadata"])["uid"] != uid {
				return bad()
			}
			base := serviceSessionRecordedPlan{Stage: source.Stage, Plan: source.Plan, Journal: j, SelectedIDs: map[string]bool{r.ID: true}}
			latest, ok := followServiceSessionCoreOwner(base, r.ID, uid, r.Desired, live, later, stageOrder, stageOrder[source.Stage])
			if !ok {
				return bad()
			}
			sources = append(sources, latest)
			delete(wanted, r.ID)
		}
	}
	boundary = "unresolved-owner"
	if len(wanted) != 0 || e.Check(ctx) != nil {
		return bad()
	}
	return sources, nil
}

// Follow only writes already recorded by native child journals after the
// accepted base. Unwritten owners keep their previous desired state.
func followServiceSessionCoreOwner(base serviceSessionRecordedPlan, id, uid string, body, live deploymentplan.Object, later []serviceSessionRecordedPlan, order map[string]int, start int) (serviceSessionRecordedPlan, bool) {
	latest := base
	for _, next := range later {
		if order[next.Stage] <= start || next.Plan == nil {
			continue
		}
		for n, update := range next.Plan.Resources {
			if update.ID != id {
				continue
			}
			written := n < len(next.Journal.Completed) && next.Journal.Completed[n] == id || next.Journal.Failed == id && next.Journal.Owners[id] != ""
			if !written {
				continue
			}
			if update.UID != uid || next.Journal.Owners[id] != uid || update.Baseline == nil || !deploymentplan.Matches(body, update.Baseline) {
				return serviceSessionRecordedPlan{}, false
			}
			// A retained original owner records no mutation. Preserve the last
			// actual source and its body, using the same rule as owner folding.
			if update.Action == "retain" {
				if !deploymentplan.Matches(update.Desired, update.Baseline) {
					return serviceSessionRecordedPlan{}, false
				}
				continue
			}
			if update.Action != "update" {
				return serviceSessionRecordedPlan{}, false
			}
			body = update.Desired
			latest = serviceSessionRecordedPlan{Stage: next.Stage, Plan: next.Plan, Journal: next.Journal, SelectedIDs: map[string]bool{id: true}}
		}
	}
	return latest, deploymentplan.Matches(body, live)
}
