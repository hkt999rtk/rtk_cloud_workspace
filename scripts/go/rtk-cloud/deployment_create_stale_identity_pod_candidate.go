package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// selectStaleIdentityPod derives identity Lease names from the environment's
// existing workload settings. It never adopts an unrecorded workload owner.
func selectStaleIdentityPod(retain *deploymentplan.Plan, journal deploymentplan.Journal, resource deploymentplan.Resource, objects []deploymentplan.Object, now time.Time, allowReady ...bool) (*deploymentplan.Plan, string, string, string, string, []staleIdentityLeaseEvidence, error) {
	bad := func() (*deploymentplan.Plan, string, string, string, string, []staleIdentityLeaseEvidence, error) {
		return nil, "", "", "", "", nil, createProviderRecoveryBlock("stale identity restart requires the exact unready single-replica original owner and expired same-Pod Leases")
	}
	readyOK := len(allowReady) == 1 && allowReady[0]
	if retain == nil || len(allowReady) > 1 || readyOK && resource.ID != "Deployment/"+retain.Stack+"-video-cloud/pki-controller" {
		return bad()
	}
	recorded := false
	for _, id := range journal.Completed {
		if id == resource.ID {
			recorded = true
		}
	}
	if retain == nil || journal.PlanSHA256 != retain.SHA256() || (!recorded && journal.Failed != resource.ID) || resource.Desired["kind"] != "Deployment" || resource.Action == "retire" || resource.UID == "" || journal.Owners[resource.ID] != resource.UID || now.IsZero() {
		return bad()
	}
	live := map[string]deploymentplan.Object{}
	for _, o := range objects {
		id := deploymentplan.ID(o)
		if live[id] != nil {
			return bad()
		}
		live[id] = o
	}
	deployment := live[resource.ID]
	if deployment == nil || deploymentplan.Map(deployment["metadata"])["uid"] != resource.UID || !deploymentplan.Matches(resource.Desired, deployment) {
		return bad()
	}
	spec := deploymentplan.Map(deployment["spec"])
	if fmt.Sprint(spec["replicas"]) != "1" {
		return bad()
	}
	var pod, rs deploymentplan.Object
	for _, o := range objects {
		if o["kind"] != "Pod" || deploymentplan.Map(o["metadata"])["deletionTimestamp"] != nil {
			continue
		}
		id, err := environmentPodController(o, live)
		if err != nil || id != resource.ID {
			continue
		}
		if pod != nil {
			return bad()
		}
		pod = o
	}
	if pod == nil {
		return bad()
	}
	pm := deploymentplan.Map(pod["metadata"])
	podUID := deploymentplan.String(pm["uid"])
	if !environmentBootstrapOwnerUUID.MatchString(podUID) {
		return bad()
	}
	for _, raw := range certIssuerObjectList(pm["ownerReferences"]) {
		ref := deploymentplan.Map(raw)
		if ref["controller"] == true && ref["kind"] == "ReplicaSet" {
			if rs != nil {
				return bad()
			}
			id := "ReplicaSet/" + deploymentplan.String(pm["namespace"]) + "/" + deploymentplan.String(ref["name"])
			rs = live[id]
			if rs == nil || deploymentplan.Map(rs["metadata"])["uid"] != ref["uid"] {
				return bad()
			}
		}
	}
	if rs == nil {
		return bad()
	}
	readyKnown := false
	for _, raw := range certIssuerObjectList(deploymentplan.Map(pod["status"])["conditions"]) {
		c := deploymentplan.Map(raw)
		if c["type"] == "Ready" {
			if readyKnown || (c["status"] != "False" && !(readyOK && c["status"] == "True")) {
				return bad()
			}
			readyKnown = true
		}
	}
	if !readyKnown {
		return bad()
	}
	template := deploymentplan.Map(deploymentplan.Map(spec["template"])["spec"])
	if !deploymentplan.Matches(deploymentplan.Object{"spec": template}, pod) {
		return bad()
	}
	seen := map[string]bool{}
	var evidence []staleIdentityLeaseEvidence
	for _, raw := range certIssuerObjectList(deploymentplan.Map(pod["spec"])["containers"]) {
		c := deploymentplan.Map(raw)
		paths := []string{}
		for _, v := range certIssuerObjectList(c["env"]) {
			env := deploymentplan.Map(v)
			if strings.HasSuffix(deploymentplan.String(env["name"]), "_IDENTITY_STATE") {
				path := deploymentplan.String(env["value"])
				if path == "" || env["valueFrom"] != nil {
					return bad()
				}
				paths = append(paths, path)
			}
		}
		if len(paths) == 0 {
			continue
		}
		if mqttFoundationEnv(c, "PKI_STATE_BACKEND") != "openbao" {
			return bad()
		}
		prefix := mqttFoundationEnv(c, "PKI_STATE_RECORD_PREFIX")
		if prefix == "" {
			return bad()
		}
		for _, path := range paths {
			id := "Lease/" + deploymentplan.String(pm["namespace"]) + "/" + environmentCustodyLeaseName(prefix, path)
			if seen[id] {
				return bad()
			}
			seen[id] = true
			lease := live[id]
			if lease == nil {
				return bad()
			}
			lm, ls := deploymentplan.Map(lease["metadata"]), deploymentplan.Map(lease["spec"])
			uid, holder := deploymentplan.String(lm["uid"]), deploymentplan.String(ls["holderIdentity"])
			duration, err := strconv.ParseInt(fmt.Sprint(ls["leaseDurationSeconds"]), 10, 64)
			renewed, parseErr := time.Parse(time.RFC3339Nano, deploymentplan.String(ls["renewTime"]))
			if err != nil || parseErr != nil || duration <= 0 || !environmentBootstrapOwnerUUID.MatchString(uid) || !strings.HasPrefix(holder, podUID+":") || renewed.Add(time.Duration(duration)*time.Second).After(now) {
				return bad()
			}
			evidence = append(evidence, staleIdentityLeaseEvidence{ID: id, UID: uid, Holder: holder, RenewedAt: renewed, DurationSeconds: duration})
		}
	}
	if len(evidence) == 0 {
		return bad()
	}
	sort.Slice(evidence, func(i, j int) bool { return evidence[i].ID < evidence[j].ID })
	def := deploymentplan.Definition{Object: deploymentplan.Clone(pod), Phase: "retirement", Retire: true}
	in := retain.Inputs
	in.Operation = "scoped"
	in.BindingsSHA256 = environmentCreateStageBindingsSHA("stale-identity-pod-restart", []deploymentplan.Definition{def})
	candidate, err := deploymentplan.Compile(in, []deploymentplan.Definition{def}, objects)
	if err != nil {
		return nil, "", "", "", "", nil, err
	}
	if len(candidate.Resources) != 1 || candidate.Resources[0].Action != "retire" || candidate.Resources[0].UID != podUID {
		return bad()
	}
	return candidate, deploymentplan.ID(rs), deploymentplan.String(deploymentplan.Map(rs["metadata"])["uid"]), deploymentplan.ID(pod), podUID, evidence, nil
}
