package main

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCRLDiscoveryReadsOriginalPVCWhenConsumerCannotStart(t *testing.T) {
	f := maintenanceRuntimeTestFixture(t)
	r := f.r
	r.lease = nil
	r.image = "ghcr.io/fixture/native@sha256:" + strings.Repeat("d", 64)
	r.cfg.Values["DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS"] = "60"
	r.cfg.Values["DEPLOYMENT_UPGRADE_WINDOW_SECONDS"] = "300"
	meta, spec := deploymentplan.Map(f.pod["metadata"]), deploymentplan.Map(f.pod["spec"])
	meta["ownerReferences"] = []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "name": "original", "uid": "original-controller", "controller": true}}
	spec["nodeName"] = "original-node"
	spec["securityContext"] = deploymentplan.Object{"runAsUser": float64(10001), "runAsGroup": float64(10001)}
	deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(f.pod["status"])["containerStatuses"])[0])["ready"] = false
	parent := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"name": "original", "namespace": meta["namespace"], "uid": "original-controller"}}
	claim := deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "original-claim"}, "status": deploymentplan.Object{"phase": "Bound"}}
	var helper deploymentplan.Object
	originalRead := r.readOverride
	r.readOverride = func(ctx context.Context, id string) (deploymentplan.Object, error) {
		switch {
		case strings.Contains(id, "/crl-pvc-recovery-"):
			return helper, nil
		case strings.HasPrefix(id, "StatefulSet/"):
			return parent, nil
		case strings.HasPrefix(id, "PersistentVolumeClaim/"):
			return claim, nil
		default:
			return originalRead(ctx, id)
		}
	}
	originalCommand := r.commandOverride
	r.commandOverride = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
		joined := strings.Join(args, " ")
		switch {
		case strings.Contains(joined, "create -f -"):
			if json.Unmarshal(input, &helper) != nil {
				t.Fatal("invalid helper")
			}
			hm, hs := deploymentplan.Map(helper["metadata"]), deploymentplan.Map(helper["spec"])
			if lifetime, _ := hs["activeDeadlineSeconds"].(float64); lifetime <= 60 || lifetime > 120 {
				t.Fatal("helper must live through its explicit maintenance caller, not only the shorter workload rollout window", lifetime)
			}
			if hs["nodeName"] != "original-node" || hs["initContainers"] != nil || hs["automountServiceAccountToken"] != false {
				t.Fatal("helper must remain on original PVC node without bootstrap or ambient token")
			}
			for _, v := range certIssuerObjectList(hs["volumes"]) {
				pvc := deploymentplan.Map(deploymentplan.Map(v)["persistentVolumeClaim"])
				if pvc != nil && (pvc["claimName"] != "original-state" || pvc["readOnly"] != true) {
					t.Fatal("read-only discovery mounted another or writable PVC")
				}
			}
			hm["name"], hm["uid"] = "crl-pvc-recovery-fixture", "helper-owned"
			return json.Marshal(helper)
		case strings.Contains(joined, "wait --for=condition=Ready"):
			return nil, nil
		case strings.Contains(joined, "get pods"):
			return json.Marshal(deploymentplan.Object{"items": []any{f.pod, helper}})
		case strings.Contains(joined, "exec pod/crl-pvc-recovery-fixture") && strings.HasSuffix(joined, "cat /state/client.json"):
			return f.identity, nil
		case strings.Contains(joined, "exec pod/"+deploymentplan.String(meta["name"])):
			return nil, errors.New("original container cannot start")
		default:
			return originalCommand(ctx, input, args...)
		}
	}
	source := managedNamedField(spec["containers"], "app")
	before := deploymentplan.Digest([]any{f.pod, parent, claim})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	id, err := r.consumerID(ctx, deploymentplan.String(meta["namespace"]), deploymentplan.String(meta["name"]), "app", source)
	if err != nil || id != "actual-native-consumer" {
		t.Fatalf("original signed identity must remain observable without consumer startup: %q %v", id, err)
	}
	if helper == nil || deploymentplan.Digest([]any{f.pod, parent, claim}) != before {
		t.Fatal("discovery skipped isolated PVC inspection or changed original resources")
	}
	inventory, err := r.observe(t.Context())
	if err != nil || len(inventory.Consumers) != 1 || inventory.Consumers[0].PodUID != "pod-owned" {
		t.Fatalf("operation-owned helper must not become a second runtime CRL consumer: %d %v", len(inventory.Consumers), err)
	}
	deploymentplan.Map(claim["metadata"])["uid"] = "foreign-claim"
	if _, err := r.consumerID(t.Context(), deploymentplan.String(meta["namespace"]), deploymentplan.String(meta["name"]), "app", source); err == nil {
		t.Fatal("a replacement claim was accepted by the retained helper")
	}
}

func TestCRLRecoveryRefreshesOpenBaoStartupBeforeRemoteCaches(t *testing.T) {
	f := maintenanceRuntimeTestFixture(t)
	f.r.cfg.Values["DEPLOYMENT_ROLLOUT_TIMEOUT_SECONDS"], f.r.cfg.Values["DEPLOYMENT_UPGRADE_WINDOW_SECONDS"] = "1", "300"
	meta, spec := deploymentplan.Map(f.pod["metadata"]), deploymentplan.Map(f.pod["spec"])
	meta["namespace"], meta["name"] = "video-cloud-staging-secrets", "openbao-0"
	meta["ownerReferences"] = []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "name": "openbao", "uid": "original-controller", "controller": true}}
	c := managedNamedField(spec["containers"], "app")
	c["name"] = "openbao-pki"
	c["command"] = []any{"/usr/local/bin/openbaopkihost"}
	c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"OPENBAO_PKI_STARTUP_TRUST": "/state/startup-trust.json"}))
	deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(f.pod["status"])["containerStatuses"])[0])["name"] = "openbao-pki"
	spec["containers"] = append(certIssuerObjectList(spec["containers"]), deploymentplan.Object{"name": "openbao"})
	deploymentplan.Map(f.pod["status"])["containerStatuses"] = append(certIssuerObjectList(deploymentplan.Map(f.pod["status"])["containerStatuses"]), deploymentplan.Object{"name": "openbao", "ready": true, "containerID": "original-provider"})
	startup, remoteSeen := false, false
	originalCommand := f.r.commandOverride
	f.r.commandOverride = func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
		if strings.HasSuffix(strings.Join(args, " "), "/usr/local/bin/openbaopkihost prepare-startup") {
			if f.installs != 1 {
				t.Fatal("startup trust refreshed before original local Root installation")
			}
			startup = true
			return nil, nil
		}
		return originalCommand(ctx, input, args...)
	}
	originalRead := f.r.readOverride
	pendingReads := 0
	f.r.readOverride = func(ctx context.Context, id string) (deploymentplan.Object, error) {
		if id == "StatefulSet/video-cloud-staging-secrets/openbao" {
			return deploymentplan.Object{"metadata": deploymentplan.Object{"uid": "original-controller"}}, nil
		}
		if id == "Pod/video-cloud-staging-secrets/openbao-0" && startup && pendingReads < 2 {
			pendingReads++
			current := deploymentplan.Clone(f.pod)
			for _, raw := range certIssuerObjectList(deploymentplan.Map(current["status"])["containerStatuses"]) {
				deploymentplan.Map(raw)["ready"] = false
			}
			return current, nil
		}
		if id == "Pod/video-cloud-staging-video-cloud/remote" {
			remoteSeen = true
			if !startup {
				t.Error("KV-dependent cache was attempted before restoring original OpenBao startup trust")
			}
			return nil, errors.New("stop at verified remote dependency")
		}
		return originalRead(ctx, id)
	}
	f.installed = createRootCRLRecord{}
	local := crlMaintenanceConsumer{Namespace: "video-cloud-staging-secrets", Pod: "openbao-0", PodUID: "pod-owned", Container: "openbao-pki", StatePath: "/state/root-crl.json", IssuerID: f.item.Issuer.ID, ConsumerID: "actual-native-consumer"}
	remote := crlMaintenanceConsumer{Namespace: "video-cloud-staging-video-cloud", Pod: "remote", PodUID: "original-remote", Container: "app", StatePath: "/state/root-crl.json", IssuerID: f.item.Issuer.ID, Custody: true}
	before := deploymentplan.Digest(f.pod)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := f.r.converge(ctx, crlMaintenanceInventory{Issuers: []crlMaintenanceIssuer{f.item}, Consumers: []crlMaintenanceConsumer{remote, local}}, map[string]createRootCRLRecord{f.item.Issuer.ID: f.latest})
	if err == nil || !startup || !remoteSeen || f.installs != 1 || deploymentplan.Digest(f.pod) != before {
		t.Fatalf("provider recovery must precede remote consumers without changing its original owner: startup=%v remote=%v installs=%d err=%v", startup, remoteSeen, f.installs, err)
	}
}
