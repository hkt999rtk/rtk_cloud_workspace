package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func staleIdentityLiveIndex(objects []deploymentplan.Object) (map[string]deploymentplan.Object, bool) {
	out := map[string]deploymentplan.Object{}
	for _, o := range objects {
		id := deploymentplan.ID(o)
		if out[id] != nil {
			return nil, false
		}
		out[id] = o
	}
	return out, true
}
func staleIdentityPodReady(p deploymentplan.Object, want bool) bool {
	found := false
	for _, v := range certIssuerObjectList(deploymentplan.Map(p["status"])["conditions"]) {
		c := deploymentplan.Map(v)
		if c["type"] == "Ready" {
			if found {
				return false
			}
			found = true
			if (c["status"] == "True") != want || (c["status"] != "True" && c["status"] != "False") {
				return false
			}
		}
	}
	return found
}
func staleIdentityLeaseMatches(lease deploymentplan.Object, e staleIdentityLeaseEvidence, holderPrefix string, now time.Time, expired bool) bool {
	if lease == nil || deploymentplan.Map(lease["metadata"])["uid"] != e.UID {
		return false
	}
	s := deploymentplan.Map(lease["spec"])
	holder := deploymentplan.String(s["holderIdentity"])
	if !strings.HasPrefix(holder, holderPrefix+":") {
		return false
	}
	seconds, err := strconv.ParseInt(fmt.Sprint(s["leaseDurationSeconds"]), 10, 64)
	renewed, parseErr := time.Parse(time.RFC3339Nano, deploymentplan.String(s["renewTime"]))
	if err != nil || parseErr != nil || seconds <= 0 {
		return false
	}
	if expired {
		return holder == e.Holder && seconds == e.DurationSeconds && renewed.Equal(e.RenewedAt) && !renewed.Add(time.Duration(seconds)*time.Second).After(now)
	}
	return renewed.Add(time.Duration(seconds) * time.Second).After(now)
}
func verifyStaleIdentityPodSnapshot(q staleIdentityPodProof, target deploymentplan.Resource, objects []deploymentplan.Object, now time.Time) error {
	bad := func() error {
		return createProviderRecoveryBlock("stale identity original Pod or Lease evidence changed")
	}
	live, ok := staleIdentityLiveIndex(objects)
	if !ok {
		return bad()
	}
	dep := live[q.DeploymentID]
	pod := live[q.PodID]
	rs := live[q.ReplicaSetID]
	if dep == nil || deploymentplan.Map(dep["metadata"])["uid"] != q.DeploymentUID || !deploymentplan.Matches(target.Desired, dep) || fmt.Sprint(deploymentplan.Map(dep["spec"])["replicas"]) != "1" || rs == nil || deploymentplan.Map(rs["metadata"])["uid"] != q.ReplicaSetUID || pod == nil || deploymentplan.Map(pod["metadata"])["uid"] != q.PodUID || (!staleIdentityPodReady(pod, false) && !(q.Controller && staleIdentityPodReady(pod, true))) {
		return bad()
	}
	controller, err := environmentPodController(pod, live)
	if err != nil || controller != q.DeploymentID {
		return bad()
	}
	if _, err = q.Candidate.Resources[0].Body(pod); err != nil {
		return bad()
	}
	count := 0
	for _, o := range objects {
		if o["kind"] != "Pod" || deploymentplan.Map(o["metadata"])["deletionTimestamp"] != nil {
			continue
		}
		if id, e := environmentPodController(o, live); e == nil && id == q.DeploymentID {
			count++
		}
	}
	if count != 1 {
		return bad()
	}
	for _, l := range q.Leases {
		if !staleIdentityLeaseMatches(live[l.ID], l, q.PodUID, now, true) {
			return bad()
		}
	}
	return nil
}
func checkStaleIdentityReplacement(q staleIdentityPodProof, target deploymentplan.Resource, objects []deploymentplan.Object, now time.Time) (string, error) {
	bad := func() (string, error) {
		return "", createProviderRecoveryBlock("replacement Pod or original identity Lease is not ready under the same Deployment")
	}
	live, ok := staleIdentityLiveIndex(objects)
	if !ok {
		return bad()
	}
	dep := live[q.DeploymentID]
	if dep == nil || deploymentplan.Map(dep["metadata"])["uid"] != q.DeploymentUID || !deploymentplan.Matches(target.Desired, dep) || fmt.Sprint(deploymentplan.Map(dep["spec"])["replicas"]) != "1" {
		return bad()
	}
	var replacement deploymentplan.Object
	for _, o := range objects {
		if o["kind"] != "Pod" || deploymentplan.Map(o["metadata"])["deletionTimestamp"] != nil {
			continue
		}
		if id, e := environmentPodController(o, live); e == nil && id == q.DeploymentID {
			if replacement != nil {
				return bad()
			}
			replacement = o
		}
	}
	if replacement == nil || !staleIdentityPodReady(replacement, true) {
		return bad()
	}
	uid := deploymentplan.String(deploymentplan.Map(replacement["metadata"])["uid"])
	if !environmentBootstrapOwnerUUID.MatchString(uid) || uid == q.PodUID {
		return bad()
	}
	expected := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(dep["spec"])["template"])["spec"])
	if !deploymentplan.Matches(deploymentplan.Object{"spec": expected}, replacement) {
		return bad()
	}
	for _, l := range q.Leases {
		if !staleIdentityLeaseMatches(live[l.ID], l, uid, now, false) {
			return bad()
		}
	}
	return uid, nil
}
