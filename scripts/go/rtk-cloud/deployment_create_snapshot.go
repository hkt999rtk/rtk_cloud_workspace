package main

import "rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"

// The snapshot is fetched anew at each verification boundary. This checks every
// historical owner/body, including failed Jobs excluded from readiness checks.
func verifyAcceptedCreateSnapshot(latest map[string]deploymentplan.Resource, owners map[string]string, live []deploymentplan.Object) error {
	bad := func() error {
		return createProviderRecoveryBlock("accepted Service core owner, desired body or bound PVC changed")
	}
	byID := map[string]deploymentplan.Object{}
	for _, v := range live {
		id := deploymentplan.ID(v)
		if byID[id] != nil {
			return bad()
		}
		byID[id] = v
	}
	for id, r := range latest {
		a := byID[id]
		if owners[id] == "" || a == nil || deploymentplan.Map(a["metadata"])["uid"] != owners[id] || !deploymentplan.Matches(r.Desired, a) {
			return bad()
		}
		if a["kind"] == "PersistentVolumeClaim" && deploymentplan.Map(a["status"])["phase"] != "Bound" {
			return bad()
		}
	}
	return nil
}
