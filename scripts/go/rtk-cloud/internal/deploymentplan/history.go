package deploymentplan

import "strings"

// HistoricalTerminalWorkload classifies finished standalone Jobs and probe
// Pods consistently during qualification and acceptance. Parent identity,
// terminal conditions and every owned Pod must agree before history is retained.
func HistoricalTerminalWorkload(id string, live map[string]Object, stack string) bool {
	object := live[id]
	if object == nil {
		return false
	}
	meta := Map(object["metadata"])
	if String(meta["uid"]) == "" || meta["deletionTimestamp"] != nil || !strings.HasPrefix(String(meta["namespace"]), stack+"-") {
		return false
	}
	if owner := String(Map(meta["labels"])["rtk.realtek.com/stack"]); owner != "" && owner != stack {
		return false
	}
	refs, _ := meta["ownerReferences"].([]any)
	if object["kind"] == "Pod" {
		if phase := Map(object["status"])["phase"]; phase != "Succeeded" && phase != "Failed" {
			return false
		}
		var owner Object
		for _, v := range refs {
			r := Map(v)
			if r["controller"] == true {
				if owner != nil {
					return false
				}
				owner = r
			}
		}
		if owner == nil {
			return len(refs) == 0 && Map(object["spec"])["restartPolicy"] == "Never"
		}
		if owner["kind"] != "Job" {
			return false
		}
		parentID := "Job/" + String(meta["namespace"]) + "/" + String(owner["name"])
		parent := live[parentID]
		if parent == nil || owner["uid"] != Map(parent["metadata"])["uid"] || owner["apiVersion"] != parent["apiVersion"] {
			return false
		}
		return HistoricalTerminalWorkload(parentID, live, stack)
	}
	if object["kind"] != "Job" {
		return false
	}
	for _, v := range refs {
		if Map(v)["controller"] == true {
			return false
		}
	}
	status := Map(object["status"])
	if status["active"] != nil && status["active"] != float64(0) {
		return false
	}
	terminal := false
	conditions, _ := status["conditions"].([]any)
	for _, v := range conditions {
		c := Map(v)
		if c["status"] == "True" && (c["type"] == "Complete" || c["type"] == "Failed") {
			terminal = true
		}
	}
	if !terminal {
		return false
	}
	for _, child := range live {
		if child["kind"] != "Pod" || Map(child["metadata"])["namespace"] != meta["namespace"] {
			continue
		}
		parents, _ := Map(child["metadata"])["ownerReferences"].([]any)
		for _, v := range parents {
			r := Map(v)
			if r["controller"] == true && r["kind"] == "Job" && r["name"] == meta["name"] {
				if r["uid"] != meta["uid"] || r["apiVersion"] != object["apiVersion"] {
					return false
				}
				if phase := Map(child["status"])["phase"]; phase != "Succeeded" && phase != "Failed" {
					return false
				}
			}
		}
	}
	return true
}
