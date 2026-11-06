package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestStagingMQTTRootPlacementTail(t *testing.T) {
	for _, mode := range []string{"original", "two-retries", "foreign-stage", "completed-retry", "retry-receipt", "missing-enrollment", "wrong-operation", "empty-receipt", "completed-root"} {
		t.Run(mode, func(t *testing.T) {
			op := environmentCreateIssuerOperation{ID: "11111111-1111-4111-8111-111111111111", IssuerID: "22222222-2222-4222-8222-222222222222", Status: "requested"}
			receipt, _ := json.Marshal(op)
			entries := []environmentCreateJournalEntry{{Key: "kubernetes/transport-client-enrollment", State: "completed"}}
			for _, key := range []string{"pki-reserve-mqtt-root", "pki-authorize-" + op.ID, "pki-provision-" + op.ID, "pki-import-" + op.ID} {
				entries = append(entries, environmentCreateJournalEntry{Key: key, Phase: "bootstrap", State: "completed", CompletedAt: time.Now(), Receipt: receipt})
			}
			entries = append(entries, environmentCreateJournalEntry{Key: "kubernetes/mqtt-root-trust", Phase: "mqtt-root-trust", State: "pending"})
			switch mode {
			case "two-retries", "completed-retry", "retry-receipt":
				entries = append(entries, environmentCreateJournalEntry{Key: "kubernetes/mqtt-root-trust-rwo-first", State: "pending"}, environmentCreateJournalEntry{Key: "kubernetes/mqtt-root-trust-rwo-spread-first", State: "pending"})
				if mode == "completed-retry" {
					entries[7].State = "completed"
					entries[7].CompletedAt = time.Now()
				}
				if mode == "retry-receipt" {
					entries[7].Receipt = []byte(`{}`)
				}
			case "foreign-stage":
				entries[5].Key = "kubernetes/foreign"
			case "missing-enrollment":
				entries[0].State = "pending"
			case "wrong-operation":
				entries[2].Key = "pki-authorize-foreign"
			case "empty-receipt":
				entries[1].Receipt = nil
			case "completed-root":
				entries[5].CompletedAt = time.Now()
			}
			boundary, err := validateStagingMQTTRootTail(entries)
			valid := mode == "original" || mode == "two-retries"
			if valid && (err != nil || boundary != 1) {
				t.Fatalf("valid interrupted tail: boundary=%d err=%v", boundary, err)
			}
			if !valid && err == nil {
				t.Fatal("unbound history accepted")
			}
		})
	}
}

func TestStagingMQTTPlacementUsesCurrentPodOwnerAndEvent(t *testing.T) {
	for _, mode := range []string{"attach", "spread", "foreign-event", "wrong-volume", "same-node", "duplicate-pending", "replaced-controller", "missing-running", "wrong-reason", "deleted-pending", "command-fails", "malformed-events"} {
		t.Run(mode, func(t *testing.T) {
			_, store, _, _ := createStateFixture(t, "staging")
			stack := "video-cloud-staging"
			ns := stack + "-video-cloud"
			controller := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "video-cloud-api", "namespace": ns, "uid": "controller-original"}}
			pod := func(name, uid, node, phase string) deploymentplan.Object {
				return deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": name, "namespace": ns, "uid": uid, "ownerReferences": []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "name": "video-cloud-api", "uid": "controller-original", "controller": true}}}, "spec": deploymentplan.Object{"nodeName": node}, "status": deploymentplan.Object{"phase": phase}}
			}
			pending := pod("pending", "pending-original", "node-two", "Pending")
			running := pod("running", "running-original", "node-one", "Running")
			event := deploymentplan.Object{"involvedObject": deploymentplan.Object{"uid": "pending-original"}, "reason": "FailedAttachVolume", "message": `volume "original-volume" is used by running`}
			items := []deploymentplan.Object{controller, pending, running}
			spread := mode == "spread"
			switch mode {
			case "spread":
				delete(deploymentplan.Map(pending["spec"]), "nodeName")
				event["reason"] = "FailedScheduling"
				event["message"] = "didn't match pod topology spread constraints"
			case "foreign-event":
				deploymentplan.Map(event["involvedObject"])["uid"] = "previous-pod"
			case "wrong-volume":
				event["message"] = `volume "another-volume" is used by running`
			case "same-node":
				deploymentplan.Map(pending["spec"])["nodeName"] = "node-one"
			case "duplicate-pending":
				items = append(items, pod("other", "other", "node-three", "Pending"))
			case "replaced-controller":
				deploymentplan.Map(controller["metadata"])["uid"] = "replacement"
			case "missing-running":
				items = items[:2]
			case "wrong-reason":
				event["reason"] = "Unrelated"
			case "deleted-pending":
				deploymentplan.Map(pending["metadata"])["deletionTimestamp"] = "2026-01-01T00:00:00Z"
			}
			dir := t.TempDir()
			python, err := exec.LookPath("python3")
			if err != nil {
				t.Fatal(err)
			}
			data, _ := json.Marshal(map[string]any{"namespace": map[string]any{"items": []any{deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": ns}}}}, "objects": map[string]any{"items": items}, "events": map[string]any{"items": []any{event}}})
			writeTestFile(t, filepath.Join(dir, "data.json"), string(data))
			writeTestFile(t, filepath.Join(dir, "kubectl"), "#!"+python+"\n"+`import json,os,sys
from pathlib import Path
a=sys.argv[1:]; root=Path(os.environ['RTK_MQTT_PLACEMENT_TEST']); d=json.loads((root/'data.json').read_text())
if 'get' not in a: sys.exit(1)
kind=a[a.index('get')+1]
if kind=='events':
 if os.environ['RTK_MQTT_PLACEMENT_MODE']=='command-fails': sys.exit(1)
 if os.environ['RTK_MQTT_PLACEMENT_MODE']=='malformed-events': print('{'); sys.exit(0)
 print(json.dumps(d['events']))
elif kind=='namespace': print(json.dumps(d['namespace']))
elif '--namespace' in a: print(json.dumps(d['objects']))
else: print('{"items":[]}')
`)
			if err := os.Chmod(filepath.Join(dir, "kubectl"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_MQTT_PLACEMENT_TEST", dir)
			t.Setenv("RTK_MQTT_PLACEMENT_MODE", mode)
			b := environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-fixture")}
			err = verifyStagingMQTTAPIPlacement(t.Context(), b, stack, "Deployment/"+ns+"/video-cloud-api", "original-volume", spread)
			if mode == "attach" || mode == "spread" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("unverified placement conflict accepted")
			}
		})
	}
}

func TestStagingSingleAPIOwnerPreservesEachClaim(t *testing.T) {
	for _, owner := range environmentCreateApplicationOwners() {
		if owner.Role != "video-cloud-api" {
			continue
		}
		for _, mode := range []string{"original", "foreign-claim", "foreign-namespace", "foreign-kind"} {
			t.Run(owner.Name+"/"+mode, func(t *testing.T) {
				object := deploymentplan.Object{"kind": "Deployment", "metadata": deploymentplan.Object{"name": owner.Name, "namespace": "stack-video-cloud"}, "spec": deploymentplan.Object{"replicas": float64(2), "template": deploymentplan.Object{"spec": deploymentplan.Object{"volumes": []any{deploymentplan.Object{"name": "identity", "persistentVolumeClaim": deploymentplan.Object{"claimName": owner.Claim}}}}}}}
				switch mode {
				case "foreign-claim":
					deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(environmentPKIPod(object)["volumes"])[0])["persistentVolumeClaim"])["claimName"] = "foreign"
				case "foreign-namespace":
					deploymentplan.Map(object["metadata"])["namespace"] = "other"
				case "foreign-kind":
					object["kind"] = "StatefulSet"
				}
				before := deploymentplan.Digest(object)
				podBefore := deploymentplan.Digest(environmentPKIPod(object))
				got := bindStagingSingleAPIOwner(object, "stack")
				if mode == "original" {
					if !got || deploymentplan.Map(object["spec"])["replicas"] != float64(1) || deploymentplan.Digest(environmentPKIPod(object)) != podBefore {
						t.Fatal("single owner changed its identity or template")
					}
				} else if got || deploymentplan.Digest(object) != before {
					t.Fatal("unqualified owner changed")
				}
			})
		}
	}
}

func TestStagingMQTTRootRecoveryRequiresFrozenAttemptsAndRootEvidence(t *testing.T) {
	for _, mode := range []string{"missing-ready-root", "unknown-write", "accepted-original", "single-owner-too-early", "foreign-prior-key", "changed-prior-owner", "verified-prior-without-root"} {
		t.Run(mode, func(t *testing.T) {
			f := newStagingQualificationFixture(t)
			f.stage(t, "transport-client-enrollment", []deploymentplan.Definition{{Object: stagingTransportQualificationObject("ConfigMap", f.parent.Stack+"-video-cloud", "transport", nil), Phase: "configuration"}}, true)
			op := environmentCreateIssuerOperation{ID: "11111111-1111-4111-8111-111111111111", IssuerID: "22222222-2222-4222-8222-222222222222", Status: "requested", Digest: deploymentplan.Digest(environmentCreateIssuerRequest{Environment: "staging", Domain: "mqtt", Kind: "root"})}
			for _, key := range []string{"pki-reserve-mqtt-root", "pki-authorize-" + op.ID, "pki-provision-" + op.ID, "pki-import-" + op.ID} {
				if err := f.e.BeginEntry(key, "bootstrap", mustProviderRecoveryJSON(op), time.Now()); err != nil {
					t.Fatal(err)
				}
				if err := f.e.RecordReceipt(key, mustProviderRecoveryJSON(op)); err != nil {
					t.Fatal(err)
				}
				if err := f.e.CompleteEntry(key, time.Now()); err != nil {
					t.Fatal(err)
				}
			}
			ns := f.parent.Stack + "-video-cloud"
			apiID := "Deployment/" + ns + "/video-cloud-api"
			defs := []deploymentplan.Definition{{Object: stagingTransportQualificationObject("Deployment", ns, "video-cloud-api", deploymentplan.Object{"replicas": float64(2)}), Phase: "workload"}}
			for len(defs) < 6 {
				defs = append(defs, deploymentplan.Definition{Object: stagingTransportQualificationObject("ConfigMap", ns, fmt.Sprintf("trust-%d", len(defs)), nil), Phase: "configuration"})
			}
			original, j := f.stage(t, stagingMQTTRootStage, defs, false)
			j.Failed = apiID
			for i, r := range original.Resources {
				j.Completed = append(j.Completed, r.ID)
				j.Owners[r.ID] = fmt.Sprintf("%08d-1111-4111-8111-111111111111", i+1)
			}
			switch mode {
			case "unknown-write":
				j.InFlight = apiID
			case "accepted-original":
				j.Accepted = true
			}
			f.persistStage(t, original, j)
			if mode == "foreign-prior-key" || mode == "changed-prior-owner" || mode == "verified-prior-without-root" {
				stage := stagingMQTTRootStage + "-rwo-" + original.SHA256()[:12]
				if mode == "foreign-prior-key" {
					stage = stagingMQTTRootStage + "-rwo-foreign"
				}
				prior, pj := f.stage(t, stage, defs, false)
				pj.Failed = apiID
				pj.Completed = append([]string(nil), j.Completed...)
				for id, uid := range j.Owners {
					pj.Owners[id] = uid
				}
				if mode == "changed-prior-owner" {
					pj.Owners[apiID] = "99999999-9999-4999-8999-999999999999"
				}
				f.persistStage(t, prior, pj)
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			_, err := qualifyStagingMQTTRootForward(t.Context(), f.cfg, f.store, &f.parent, f.e, environmentCreateBootstrapRuntime{Execution: f.e, Backend: f.b}, nil, mode == "single-owner-too-early")
			if err == nil {
				t.Fatal("unqualified original MQTT recovery admitted")
			}
			if (mode == "missing-ready-root" || mode == "verified-prior-without-root") && !strings.Contains(err.Error(), "original ready Service Root") {
				t.Fatal("valid checkpoint did not reach native Root evidence check", err)
			}
			assertRecoveryContractReadOnly(t, f.store, before)
		})
	}
}
