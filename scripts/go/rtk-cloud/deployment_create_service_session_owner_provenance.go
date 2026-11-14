package main

import "rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"

// selectLatestServiceSessionOwners consumes chronologically ordered child
// plans/journals authenticated by the caller against the same parent. It does
// not treat live Kubernetes inventory as authority or mutate those plans.
// The output is directly consumable by compileServiceSessionSIDForward.
func selectLatestServiceSessionOwners(sources []serviceSessionRecordedPlan) ([]serviceSessionRecordedPlan, error) {
	bad := func() ([]serviceSessionRecordedPlan, error) {
		return nil, createProviderRecoveryBlock("Service owner history lacks a continuous recorded same-UID successor")
	}
	if len(sources) == 0 {
		return bad()
	}
	type owner struct {
		index   int
		uid     string
		desired deploymentplan.Object
	}
	selected := map[string]owner{}
	var first deploymentplan.Inputs
	out := make([]serviceSessionRecordedPlan, len(sources))
	for index, s := range sources {
		p, j := s.Plan, s.Journal
		if p == nil || p.Version != deploymentplan.Version || j.PlanSHA256 != p.SHA256() || j.InFlight != "" || s.SelectedIDs != nil {
			return bad()
		}
		if index == 0 {
			first = p.Inputs
		} else if p.Environment != first.Environment || p.Stack != first.Stack || p.ConfigurationSHA256 != first.ConfigurationSHA256 {
			return bad()
		}
		out[index] = serviceSessionRecordedPlan{Stage: s.Stage, Plan: p, Journal: j, SelectedIDs: map[string]bool{}}
		known := map[string]deploymentplan.Resource{}
		for _, r := range p.Resources {
			if r.ID == "" || known[r.ID].ID != "" || deploymentplan.ID(r.Desired) != r.ID {
				return bad()
			}
			known[r.ID] = r
		}
		done := map[string]bool{}
		for _, id := range j.Completed {
			if known[id].ID == "" || done[id] || j.Owners[id] == "" {
				return bad()
			}
			done[id] = true
		}
		for id, uid := range j.Owners {
			r, ok := known[id]
			if !ok || uid == "" || !done[id] && id != j.Failed || r.UID != "" && r.UID != uid || r.Action == "retire" {
				return bad()
			}
		}
		for _, r := range p.Resources {
			uid := j.Owners[r.ID]
			if uid == "" {
				if _, ok := selected[r.ID]; !ok {
					selected[r.ID] = owner{index: index}
				}
				continue
			}
			if previous, ok := selected[r.ID]; ok && previous.uid != "" {
				if uid != previous.uid || r.Baseline == nil || !deploymentplan.Matches(previous.desired, r.Baseline) {
					return bad()
				}
			}
			selected[r.ID] = owner{index: index, uid: uid, desired: r.Desired}
		}
	}
	for id, chosen := range selected {
		out[chosen.index].SelectedIDs[id] = true
	}
	return out, nil
}
