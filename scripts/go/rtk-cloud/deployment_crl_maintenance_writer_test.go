package main

import (
	"context"
	"encoding/json"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
	"time"
)

func TestCRLMaintenanceWriterRepairPinsOriginalWorkloadAndRequiresRealACK(t *testing.T) {
	f := maintenanceRuntimeTestFixture(t)
	podSpec := deploymentplan.Map(f.pod["spec"])
	app := deploymentplan.Map(certIssuerObjectList(podSpec["containers"])[0])
	app["image"] = "original-image@sha256:unchanged"
	app["securityContext"] = deploymentplan.Object{"runAsUser": float64(10001), "runAsNonRoot": true}
	deploymentplan.Map(f.pod["metadata"])["ownerReferences"] = []any{deploymentplan.Object{"kind": "ReplicaSet", "name": "original-rs", "uid": "rs-uid", "controller": true}}
	owner := deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "deployment-uid", "resourceVersion": "7"}, "spec": deploymentplan.Object{"replicas": float64(1), "strategy": deploymentplan.Object{"type": "Recreate"}, "template": deploymentplan.Object{"spec": deploymentplan.Clone(podSpec)}}}
	originalOwner := deploymentplan.Clone(owner)
	rs := deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "rs-uid", "ownerReferences": []any{deploymentplan.Object{"kind": "Deployment", "name": "original-api", "uid": "deployment-uid", "controller": true}}}}
	pvc := deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "original-pvc"}}
	upstreams := map[string]deploymentplan.Object{}
	originalRead := f.r.readOverride
	f.r.readOverride = func(ctx context.Context, id string) (deploymentplan.Object, error) {
		if value := upstreams[id]; value != nil {
			return value, nil
		}
		switch {
		case strings.Contains(id, "ReplicaSet/"):
			return rs, nil
		case strings.HasSuffix(id, "/original-api"):
			return owner, nil
		case strings.Contains(id, "PersistentVolumeClaim/"):
			return pvc, nil
		}
		return originalRead(ctx, id)
	}
	originalCommand := f.r.commandOverride
	patches, ackObservations := 0, 0
	f.r.commandOverride = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "pki_crl_acknowledgments") && f.ack {
			ackObservations++
			deploymentplan.Map(owner["metadata"])["resourceVersion"] = "8"
		}
		if strings.Contains(joined, "pod/upstream-") && strings.HasSuffix(joined, "cat /state/root-crl.json") {
			return json.Marshal(map[string]any{"issuer_fingerprint": f.item.Issuer.RootFingerprint, "crl": f.latest})
		}
		if len(args) > 1 && args[len(args)-2] == "crl-writer-access" {
			if deploymentplan.Map(f.pod["metadata"])["uid"] == "new-original-pod" {
				return []byte("10001 10001 2775 0 10001 yes 10001\n"), nil
			}
			return []byte("10001 999 2775 0 10001 no 999\n"), nil
		}
		if strings.Contains(joined, "patch deployment original-api") {
			patches++
			var operations []map[string]any
			if ackObservations < 2 || json.Unmarshal([]byte(args[len(args)-1]), &operations) != nil || len(operations) != 5 || operations[0]["value"] != "deployment-uid" || operations[1]["value"] != "8" || operations[4]["path"] != "/spec/template/spec/containers/0/securityContext/runAsGroup" || operations[4]["value"] != float64(10001) {
				t.Fatal("writer correction lacked real upstream observations or original guarded single-field patch")
			}
			deploymentplan.Map(managedNamedField(environmentPKIPod(owner)["containers"], "app")["securityContext"])["runAsGroup"] = float64(10001)
			return nil, nil
		}
		if strings.Contains(joined, "rollout status deployment/original-api") {
			f.pod = deploymentplan.Clone(f.pod)
			deploymentplan.Map(f.pod["metadata"])["uid"] = "new-original-pod"
			deploymentplan.Map(managedNamedField(deploymentplan.Map(f.pod["spec"])["containers"], "app")["securityContext"])["runAsGroup"] = float64(10001)
			return nil, nil
		}
		return originalCommand(ctx, input, args...)
	}
	inventory, err := f.r.observe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, role := range []string{"pki-controller", "account-manager"} {
		namespace := "video-cloud-staging-video-cloud"
		if role == "account-manager" {
			namespace = "video-cloud-staging-account-manager"
		}
		upstream := deploymentplan.Clone(f.pod)
		meta := deploymentplan.Map(upstream["metadata"])
		meta["name"] = "upstream-" + role
		meta["namespace"] = namespace
		meta["uid"] = "upstream-" + role + "-uid"
		meta["labels"] = deploymentplan.Object{"role": role}
		meta["ownerReferences"] = []any{deploymentplan.Object{"kind": "Deployment", "name": role, "uid": role + "-owner", "controller": true}}
		definition := deploymentplan.Clone(f.controller)
		definition["metadata"] = deploymentplan.Object{"uid": role + "-owner"}
		deploymentplan.Map(definition["spec"])["selector"] = deploymentplan.Object{"matchLabels": deploymentplan.Object{"role": role}}
		upstreams["Deployment/"+namespace+"/"+role] = definition
		upstreams["Pod/"+namespace+"/upstream-"+role] = upstream
		inventory.Consumers = append(inventory.Consumers, crlMaintenanceConsumer{Namespace: namespace, Pod: "upstream-" + role, PodUID: "upstream-" + role + "-uid", Container: "app", StatePath: "/state/root-crl.json", IssuerID: f.item.Issuer.ID, ConsumerID: "native-" + role})
	}
	f.installed = createRootCRLRecord{}
	f.ack = false
	records := map[string]createRootCRLRecord{f.item.Issuer.ID: f.latest}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	if err := f.r.converge(ctx, inventory, records); err == nil || !strings.Contains(err.Error(), "ACK") {
		t.Fatalf("missing real upstream ACK did not stop recovery: %v", err)
	}
	if f.installs != 0 || patches != 0 || len(f.r.deferredWriters) != 1 {
		t.Fatal("nonwritable consumer was not safely deferred")
	}
	var repair maintenanceWriterRepair
	for _, p := range f.r.deferredWriters {
		repair = p
	}
	deploymentplan.Map(pvc["metadata"])["uid"] = "replacement-pvc"
	if err := f.r.applyWriterRepair(context.Background(), repair); err == nil || patches != 0 {
		t.Fatal("replacement PVC accepted")
	}
	deploymentplan.Map(pvc["metadata"])["uid"] = "original-pvc"
	deploymentplan.Map(owner["spec"])["replicas"] = float64(2)
	if err := f.r.applyWriterRepair(context.Background(), repair); err == nil || patches != 0 {
		t.Fatal("changed writer spec accepted")
	}
	deploymentplan.Map(owner["spec"])["replicas"] = float64(1)
	f.ack = true
	if err := f.r.converge(context.Background(), inventory, records); err != nil {
		t.Fatal(err)
	}
	if patches != 1 || f.installs != 1 || len(f.r.deferredWriters) != 0 || f.installed.Digest != f.latest.Digest || deploymentplan.Map(f.pod["metadata"])["uid"] != "new-original-pod" {
		t.Fatal("defer/ACK/repair/rediscovery/install did not converge")
	}
	deploymentplan.Map(originalOwner["metadata"])["resourceVersion"] = "8"
	deploymentplan.Map(managedNamedField(environmentPKIPod(originalOwner)["containers"], "app")["securityContext"])["runAsGroup"] = float64(10001)
	if deploymentplan.Digest(owner) != deploymentplan.Digest(originalOwner) {
		t.Fatal("repair changed image, UID, Recreate strategy or original mounts")
	}
}

func TestCRLMaintenanceWriterAccessRejectsUnrelatedPermissions(t *testing.T) {
	for _, value := range []string{"10001 999 2777 0 10001 no 999", "10001 999 2755 0 10001 no 999", "10001 999 2775 0 20000 no 999", "0 0 2775 0 0 no 0", "10001 999 2775 0 10001 no 999 10001", "bad"} {
		if _, err := parseMaintenanceWriterAccess([]byte(value)); err == nil {
			t.Fatalf("unqualified permission correction accepted: %s", value)
		}
	}
	if access, err := parseMaintenanceWriterAccess([]byte("10001 999 2775 0 10001 no 999")); err != nil || access.Group != 10001 {
		t.Fatalf("original missing group rejected: %v", err)
	}
	if _, err := parseMaintenanceWriterAccess([]byte("100 1000 700 100 1000 yes 1000")); err != nil {
		t.Fatal(err)
	}
}
