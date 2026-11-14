package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"sort"
)

type serviceSessionRecordedPlan struct {
	Stage   string
	Plan    *deploymentplan.Plan
	Journal deploymentplan.Journal
	// nil selects every resource; otherwise only the authenticated latest owners.
	SelectedIDs map[string]bool
}
type serviceSessionPendingOwner struct {
	ID     string
	Reason string
}

// serviceSessionSIDSlots scans only native pod environment literals. It never
// edits secrets, identity state, projected paths, or an unselected container.
func serviceSessionSIDSlots(o deploymentplan.Object, expected, replacement string) (int, error) {
	pod := environmentPKIPod(o)
	if o["kind"] == "Pod" {
		pod = deploymentplan.Map(o["spec"])
	}
	if pod == nil {
		return 0, nil
	}
	count := 0
	for _, group := range []string{"containers", "initContainers"} {
		for _, raw := range certIssuerObjectList(pod[group]) {
			c := deploymentplan.Map(raw)
			var slots []deploymentplan.Object
			found := false
			for _, item := range certIssuerObjectList(c["env"]) {
				v := deploymentplan.Map(item)
				if v["name"] != "PKI_BOOTSTRAP_SESSION_ID" {
					continue
				}
				slots = append(slots, v)
				if v["value"] == expected {
					found = true
				}
			}
			if !found {
				continue
			}
			if len(slots) != 1 || slots[0]["valueFrom"] != nil {
				return 0, createProviderRecoveryBlock("Service session reference is ambiguous or differs from the recorded owner")
			}
			if replacement != "" {
				slots[0]["value"] = replacement
			}
			count++
		}
	}
	return count, nil
}

// compileServiceSessionSIDForward uses only caller-authenticated frozen plans
// and journals as authority; inventory supplies baselines, never new owners.
// Pending Jobs and not-yet-written resources go back to the normal renderer.
func compileServiceSessionSIDForward(sources []serviceSessionRecordedPlan, live []deploymentplan.Object, oldSID, newSID, stage string, verifiedHistory ...[]serviceSessionHistoricalJob) (*deploymentplan.Plan, []serviceSessionPendingOwner, error) {
	boundary, sourceName, resourceID := "inputs", "", ""
	bad := func() (*deploymentplan.Plan, []serviceSessionPendingOwner, error) {
		return nil, nil, createProviderRecoveryBlock("Service session successor requires exact recorded owners and unchanged identity settings; boundary=" + boundary + " source=" + sourceName + " resource=" + resourceID)
	}
	if len(sources) == 0 || len(verifiedHistory) > 1 || !environmentBootstrapOwnerUUID.MatchString(oldSID) || !environmentBootstrapOwnerUUID.MatchString(newSID) || oldSID == newSID || !secretEnvironmentPattern.MatchString(stage) {
		return bad()
	}
	inventory := map[string]deploymentplan.Object{}
	for _, o := range live {
		id := deploymentplan.ID(o)
		if inventory[id] != nil {
			return bad()
		}
		inventory[id] = o
	}
	// The caller supplies only historical Jobs authenticated by
	// verifyServiceSessionHistoricalJobs against the admitted same-key/CSR
	// lineage. Recheck the live UID and success condition on every compile.
	historical := map[string]bool{}
	failedOwners := map[string]string{}
	inheritedOwners := map[string]string{}
	mintOwners := map[string]serviceSessionHistoricalJob{}
	if len(verifiedHistory) == 1 {
		for _, job := range verifiedHistory[0] {
			boundary, resourceID = "historical-owner", job.ID
			o := inventory[job.ID]
			if !job.verified || historical[job.ID] || o == nil || deploymentplan.Map(o["metadata"])["uid"] != job.UID {
				return bad()
			}
			if job.failedPodUID != "" {
				if !environmentBootstrapOwnerUUID.MatchString(job.failedPodUID) || job.desired == nil || !deploymentplan.Matches(job.desired, o) || !serviceSessionJobFailed(o) {
					return bad()
				}
				failedOwners[job.ID] = job.failedPodUID
			} else if job.mintCustody {
				if o["kind"] != "Deployment" || job.desired == nil || !deploymentplan.Matches(job.desired, o) {
					return bad()
				}
				mintOwners[job.ID] = job
			} else if o["kind"] != "Job" || !serviceSessionJobComplete(o) {
				return bad()
			}
			historical[job.ID] = true
			if job.failedPodUID == "" {
				inheritedOwners[job.ID] = job.UID
			}
		}
	}
	recorded := map[string]bool{}
	var defs []deploymentplan.Definition
	var pending []serviceSessionPendingOwner
	var in deploymentplan.Inputs
	for index, source := range sources {
		boundary, sourceName, resourceID = "source-journal", source.Stage, ""
		p, j := source.Plan, source.Journal
		if p == nil || p.Version != deploymentplan.Version || j.PlanSHA256 != p.SHA256() || j.InFlight != "" {
			return bad()
		}
		// Each original source is bound to its own authenticated release.
		// Approved same-generation repairs may have different image releases.
		boundary = "source-scope"
		if index == 0 {
			in = p.Inputs
			in.Operation = "scoped"
		} else if p.Environment != in.Environment || p.Stack != in.Stack || p.ConfigurationSHA256 != in.ConfigurationSHA256 {
			return bad()
		}
		known := map[string]bool{}
		for _, r := range p.Resources {
			if known[r.ID] {
				return bad()
			}
			known[r.ID] = true
		}
		done := map[string]bool{}
		for _, id := range j.Completed {
			if !known[id] || done[id] || j.Owners[id] == "" {
				return bad()
			}
			done[id] = true
		}
		for id, uid := range j.Owners {
			if !known[id] || uid == "" || !done[id] && id != j.Failed {
				return bad()
			}
		}
		for _, r := range p.Resources {
			boundary, resourceID = "source-resource", r.ID
			current, uid := inventory[r.ID], j.Owners[r.ID]
			// A recorded controller still owns its native children when its
			// latest template no longer needs a session update. Selection limits
			// writes, not the authenticated same-generation ownership history.
			if uid != "" && current != nil && deploymentplan.Map(current["metadata"])["uid"] == uid {
				switch r.Desired["kind"] {
				case "Deployment", "StatefulSet", "DaemonSet", "Job":
					inheritedOwners[r.ID] = uid
				}
			}
			if failedOwners[r.ID] != "" || source.SelectedIDs != nil && !source.SelectedIDs[r.ID] {
				continue
			}
			desired := deploymentplan.Clone(r.Desired)
			slots, err := serviceSessionSIDSlots(desired, oldSID, newSID)
			if err != nil {
				return bad()
			}
			if slots == 0 {
				continue
			}
			if recorded[r.ID] || deploymentplan.ID(r.Desired) != r.ID {
				return bad()
			}
			recorded[r.ID] = true
			boundary = "original-owner"
			if uid == "" {
				if current != nil {
					return bad()
				}
				pending = append(pending, serviceSessionPendingOwner{ID: r.ID, Reason: "not-written"})
				continue
			}
			if current == nil || deploymentplan.String(deploymentplan.Map(current["metadata"])["uid"]) != uid || !deploymentplan.Matches(r.Desired, current) {
				return bad()
			}
			if owner, ok := mintOwners[r.ID]; ok {
				if uid != owner.UID || !deploymentplan.Matches(owner.desired, r.Desired) {
					return bad()
				}
				continue // The admitted native mint writes this exact original owner.
			}
			if r.Desired["kind"] != "Deployment" {
				pending = append(pending, serviceSessionPendingOwner{ID: r.ID, Reason: "non-deployment"})
				continue
			}
			deploymentplan.Map(desired["metadata"])["uid"] = uid
			defs = append(defs, deploymentplan.Definition{Object: desired, Phase: r.Phase})
		}
	}
	for id, o := range inventory {
		if recorded[id] || historical[id] {
			continue
		}
		// Native Pods/ReplicaSets inherit their recorded controller's session;
		// they are not independent rollout owners. Use the same UID/API-version
		// lineage resolver as normal workload acceptance, never adopt a parent.
		if o["kind"] == "Pod" || o["kind"] == "ReplicaSet" {
			owner, err := environmentPodController(o, inventory)
			if err == nil {
				if failedPod := failedOwners[owner]; failedPod != "" {
					if o["kind"] == "Pod" && deploymentplan.Map(o["metadata"])["uid"] == failedPod && deploymentplan.Map(o["status"])["phase"] == "Failed" {
						continue
					}
				} else if inheritedOwners[owner] != "" && deploymentplan.Map(inventory[owner]["metadata"])["uid"] == inheritedOwners[owner] {
					continue
				}
			}
		}
		boundary, sourceName, resourceID = "unrecorded-session-owner", "", id
		slots, err := serviceSessionSIDSlots(o, oldSID, "")
		if err != nil || slots > 0 {
			return bad()
		}
	}
	sort.Slice(pending, func(i, j int) bool { return pending[i].ID < pending[j].ID })
	if len(defs) == 0 {
		return nil, pending, nil
	}
	in.BindingsSHA256 = environmentCreateStageBindingsSHA(stage, defs)
	result, err := deploymentplan.Compile(in, defs, live)
	if err != nil {
		return nil, nil, err
	}
	for _, r := range result.Resources {
		boundary, resourceID = "compiled-update", r.ID
		if r.Action != "update" || r.UID == "" {
			return bad()
		}
	}
	return result, pending, nil
}
