package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func staleIdentityPodFixture(t *testing.T) (*deploymentplan.Plan, deploymentplan.Journal, deploymentplan.Resource, []deploymentplan.Object, time.Time) {
	t.Helper()
	now := time.Now().UTC()
	stack := "video-cloud-dev"
	ns := stack + "-video-cloud"
	depUID := "11111111-1111-4111-8111-111111111111"
	rsUID := "22222222-2222-4222-8222-222222222222"
	podUID := "33333333-3333-4333-8333-333333333333"
	leaseUID := "44444444-4444-4444-8444-444444444444"
	prefix := "dev/" + strings.Repeat("a", 64) + "/mqtt-pki"
	path := "/run/state/client.json"
	container := deploymentplan.Object{"name": "app", "image": "fixture", "env": []any{deploymentplan.Object{"name": "PKI_STATE_BACKEND", "value": "openbao"}, deploymentplan.Object{"name": "PKI_STATE_RECORD_PREFIX", "value": prefix}, deploymentplan.Object{"name": "SERVICE_IDENTITY_STATE", "value": path}, deploymentplan.Object{"name": "OTHER_IDENTITY_STATE", "value": "/run/state/other.json"}}}
	broker := deploymentplan.Object{"name": "broker", "image": "fixture", "env": []any{deploymentplan.Object{"name": "PKI_STATE_BACKEND", "value": "openbao"}, deploymentplan.Object{"name": "PKI_STATE_RECORD_PREFIX", "value": prefix}, deploymentplan.Object{"name": "BROKER_IDENTITY_STATE", "value": "/run/state/broker.json"}}}
	podSpec := deploymentplan.Object{"containers": []any{container, broker}}
	labels := deploymentplan.Object{"rtk.realtek.com/stack": stack}
	dep := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "mqtt-pki", "namespace": ns, "uid": depUID, "resourceVersion": "1", "labels": deploymentplan.Clone(labels)}, "spec": deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": podSpec}}}
	ref := func(kind, name, uid string) []any {
		return []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": kind, "name": name, "uid": uid, "controller": true}}
	}
	rs := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": deploymentplan.Object{"name": "mqtt-rs", "namespace": ns, "uid": rsUID, "ownerReferences": ref("Deployment", "mqtt-pki", depUID)}}
	pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "mqtt-pod", "namespace": ns, "uid": podUID, "resourceVersion": "2", "labels": deploymentplan.Clone(labels), "ownerReferences": ref("ReplicaSet", "mqtt-rs", rsUID)}, "spec": deploymentplan.Clone(podSpec), "status": deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "Ready", "status": "False"}}}}
	lease := deploymentplan.Object{"apiVersion": "coordination.k8s.io/v1", "kind": "Lease", "metadata": deploymentplan.Object{"name": environmentCustodyLeaseName(prefix, path), "namespace": ns, "uid": leaseUID}, "spec": deploymentplan.Object{"holderIdentity": podUID + ":instance", "leaseDurationSeconds": float64(30), "renewTime": now.Add(-time.Minute).Format(time.RFC3339Nano)}}
	otherLease := deploymentplan.Clone(lease)
	deploymentplan.Map(otherLease["metadata"])["name"] = environmentCustodyLeaseName(prefix, "/run/state/other.json")
	deploymentplan.Map(otherLease["metadata"])["uid"] = "77777777-7777-4777-8777-777777777777"
	brokerLease := deploymentplan.Clone(lease)
	deploymentplan.Map(brokerLease["metadata"])["name"] = environmentCustodyLeaseName(prefix, "/run/state/broker.json")
	deploymentplan.Map(brokerLease["metadata"])["uid"] = "88888888-8888-4888-8888-888888888888"
	in := deploymentplan.Inputs{Environment: "dev", Stack: stack, Operation: "scoped", ConfigurationSHA256: strings.Repeat("a", 64), ReleaseSHA256: strings.Repeat("b", 64), BindingsSHA256: strings.Repeat("c", 64)}
	plan, err := deploymentplan.Compile(in, []deploymentplan.Definition{{Object: deploymentplan.Clone(dep), Phase: "workload", Retain: true}}, []deploymentplan.Object{dep})
	if err != nil {
		t.Fatal(err)
	}
	resource := plan.Resources[0]
	journal := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Failed: resource.ID, Completed: []string{resource.ID}, Owners: map[string]string{resource.ID: depUID}}
	return plan, journal, resource, []deploymentplan.Object{dep, rs, pod, lease, otherLease, brokerLease}, now
}
func TestStaleIdentityPodOnlyRestartsExactExpiredOwner(t *testing.T) {
	plan, journal, resource, objects, now := staleIdentityPodFixture(t)
	candidate, rsID, rsUID, podID, podUID, leases, err := selectStaleIdentityPod(plan, journal, resource, objects, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(leases) != 3 || candidate.Resources[0].UID != podUID || candidate.Resources[0].Action != "retire" {
		t.Fatal("wrong qualified Pod")
	}
	q := staleIdentityPodProof{DeploymentID: resource.ID, DeploymentUID: resource.UID, ReplicaSetID: rsID, ReplicaSetUID: rsUID, PodID: podID, PodUID: podUID, Leases: leases, Candidate: candidate}
	if err = verifyStaleIdentityPodSnapshot(q, resource, objects, now); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"lease-active", "foreign-holder", "changed-pod", "ready", "replicas", "foreign-owner", "extra-replica"} {
		t.Run(mode, func(t *testing.T) {
			items := make([]deploymentplan.Object, len(objects))
			for i := range objects {
				items[i] = deploymentplan.Clone(objects[i])
			}
			switch mode {
			case "lease-active":
				deploymentplan.Map(items[3]["spec"])["renewTime"] = now.Format(time.RFC3339Nano)
			case "foreign-holder":
				deploymentplan.Map(items[3]["spec"])["holderIdentity"] = "99999999-9999-4999-8999-999999999999:other"
			case "changed-pod":
				deploymentplan.Map(items[2]["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "ready":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(items[2]["status"])["conditions"])[0])["status"] = "True"
			case "replicas":
				deploymentplan.Map(items[0]["spec"])["replicas"] = float64(2)
			case "foreign-owner":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(items[1]["metadata"])["ownerReferences"])[0])["uid"] = "99999999-9999-4999-8999-999999999999"
			case "extra-replica":
				extra := deploymentplan.Clone(items[2])
				deploymentplan.Map(extra["metadata"])["name"] = "another"
				deploymentplan.Map(extra["metadata"])["uid"] = "55555555-5555-4555-8555-555555555555"
				items = append(items, extra)
			}
			if _, _, _, _, _, _, err := selectStaleIdentityPod(plan, journal, resource, items, now); err == nil {
				t.Fatal("accepted", mode)
			}
		})
	}
	replacement := make([]deploymentplan.Object, len(objects))
	for i := range objects {
		replacement[i] = deploymentplan.Clone(objects[i])
	}
	uid := "66666666-6666-4666-8666-666666666666"
	deploymentplan.Map(replacement[2]["metadata"])["uid"] = uid
	deploymentplan.Map(replacement[2]["metadata"])["name"] = "mqtt-replacement"
	deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(replacement[2]["status"])["conditions"])[0])["status"] = "True"
	for _, lease := range replacement[3:] {
		deploymentplan.Map(lease["spec"])["holderIdentity"] = fmt.Sprint(uid, ":new-instance")
		deploymentplan.Map(lease["spec"])["renewTime"] = now.Format(time.RFC3339Nano)
	}
	if got, err := checkStaleIdentityReplacement(q, resource, replacement, now); err != nil || got != uid {
		t.Fatal("valid same-owner replacement rejected", err)
	}
	deploymentplan.Map(replacement[3]["spec"])["holderIdentity"] = podUID + ":old-instance"
	if _, err := checkStaleIdentityReplacement(q, resource, replacement, now); err == nil {
		t.Fatal("accepted original expired holder")
	}
}

func TestStaleControllerPodCanBeReadyOnlyWithRecordedExpiredLeases(t *testing.T) {
	old, j, _, objects, now := staleIdentityPodFixture(t)
	dep, rs, pod := objects[0], objects[1], objects[2]
	deploymentplan.Map(dep["metadata"])["name"] = "pki-controller"
	deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(rs["metadata"])["ownerReferences"])[0])["name"] = "pki-controller"
	deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["status"])["conditions"])[0])["status"] = "True"
	desired := deploymentplan.Clone(dep)
	uid := deploymentplan.String(deploymentplan.Map(dep["metadata"])["uid"])
	plan, err := deploymentplan.Compile(old.Inputs, []deploymentplan.Definition{{Object: desired, Phase: "workload", Retain: true}}, []deploymentplan.Object{dep})
	if err != nil {
		t.Fatal(err)
	}
	target := plan.Resources[0]
	j.PlanSHA256 = plan.SHA256()
	j.Failed = ""
	j.Accepted = true
	j.Completed = []string{target.ID}
	j.Owners = map[string]string{target.ID: uid}
	if _, _, _, _, _, _, err := selectStaleIdentityPod(plan, j, target, objects, now); err == nil {
		t.Fatal("ordinary recovery accepted ready Pod")
	}
	candidate, rsID, rsUID, podID, podUID, leases, err := selectStaleIdentityPod(plan, j, target, objects, now, true)
	if err != nil {
		t.Fatal(err)
	}
	q := staleIdentityPodProof{Controller: true, DeploymentID: target.ID, DeploymentUID: uid, ReplicaSetID: rsID, ReplicaSetUID: rsUID, PodID: podID, PodUID: podUID, Leases: leases, Candidate: candidate}
	if err := verifyStaleIdentityPodSnapshot(q, target, objects, now); err != nil {
		t.Fatal(err)
	}
	// A previously expired lease can be acquired between qualification and delete.
	deploymentplan.Map(objects[3]["spec"])["renewTime"] = now.Format(time.RFC3339Nano)
	if err := verifyStaleIdentityPodSnapshot(q, target, objects, now); err == nil {
		t.Fatal("renewed lease accepted")
	}
}
