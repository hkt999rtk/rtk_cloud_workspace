package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateClaimPodFixture() deploymentplan.Object {
	return deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "unlabeled-foreign-holder", "namespace": "video-cloud-dev-video-cloud", "uid": "11111111-1111-4111-8111-111111111111"}, "spec": deploymentplan.Object{"volumes": []any{deploymentplan.Object{"name": "private-state", "persistentVolumeClaim": deploymentplan.Object{"claimName": "original-log-state"}}}}}
}

func TestManagedLateLogClaimPodListIncludesEveryPossibleHolder(t *testing.T) {
	for _, phase := range []string{"Pending", "Running", "Succeeded", "Failed", "Unknown"} {
		for _, deleting := range []bool{false, true} {
			pod := managedLateClaimPodFixture()
			pod["status"] = deploymentplan.Object{"phase": phase}
			if deleting {
				deploymentplan.Map(pod["metadata"])["deletionTimestamp"] = time.Now().UTC().Format(time.RFC3339)
			}
			for _, kind := range []string{"List", "PodList"} {
				raw := mustProviderRecoveryJSON(deploymentplan.Object{"apiVersion": "v1", "kind": kind, "metadata": deploymentplan.Object{"resourceVersion": "native-snapshot"}, "items": []any{pod}})
				objects, err := decodeManagedLateLogClaimPods(raw, "video-cloud-dev-video-cloud")
				if err != nil || len(objects) != 1 || managedLateLogClaimHolders(objects, "video-cloud-dev-video-cloud", "original-log-state", "") == nil {
					t.Fatal("a complete unfiltered Pod inventory hid a possible private claim holder", err)
				}
			}
		}
	}
	for _, items := range [][]any{{}, {deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "unrelated", "namespace": "video-cloud-dev-video-cloud", "uid": "22222222-2222-4222-8222-222222222222"}, "spec": deploymentplan.Object{"volumes": []any{deploymentplan.Object{"name": "unrelated-state", "persistentVolumeClaim": deploymentplan.Object{"claimName": "other-claim"}}}}}}} {
		objects, err := decodeManagedLateLogClaimPods(mustProviderRecoveryJSON(deploymentplan.Object{"apiVersion": "v1", "kind": "List", "items": items}), "video-cloud-dev-video-cloud")
		if err != nil || managedLateLogClaimHolders(objects, "video-cloud-dev-video-cloud", "original-log-state", "") != nil {
			t.Fatal("genuine empty or unrelated Pod inventory refused", err)
		}
	}
}

func TestManagedLateLogClaimPodListRefusesIncompleteOrMalformedEvidence(t *testing.T) {
	for _, mode := range []string{"foreign-namespace", "missing-name", "missing-uid", "wrong-pod-kind", "missing-spec", "nonarray-volumes", "missing-volume-name", "duplicate-volume", "malformed-pvc", "missing-claim-name", "duplicate-name", "duplicate-uid", "missing-items", "null-items", "wrong-list-kind", "wrong-api", "pagination", "remaining-items", "unknown-envelope", "trailing-data"} {
		t.Run(mode, func(t *testing.T) {
			pod := managedLateClaimPodFixture()
			list := deploymentplan.Object{"apiVersion": "v1", "kind": "PodList", "items": []any{pod}}
			meta, spec := deploymentplan.Map(pod["metadata"]), deploymentplan.Map(pod["spec"])
			volume := deploymentplan.Map(certIssuerObjectList(spec["volumes"])[0])
			switch mode {
			case "foreign-namespace":
				meta["namespace"] = "foreign"
			case "missing-name":
				delete(meta, "name")
			case "missing-uid":
				delete(meta, "uid")
			case "wrong-pod-kind":
				pod["kind"] = "Deployment"
			case "missing-spec":
				delete(pod, "spec")
			case "nonarray-volumes":
				spec["volumes"] = deploymentplan.Object{"hidden": "claim"}
			case "missing-volume-name":
				delete(volume, "name")
			case "duplicate-volume":
				spec["volumes"] = []any{volume, volume}
			case "malformed-pvc":
				volume["persistentVolumeClaim"] = "hidden-claim"
			case "missing-claim-name":
				volume["persistentVolumeClaim"] = deploymentplan.Object{}
			case "duplicate-name", "duplicate-uid":
				second := deploymentplan.Clone(pod)
				if mode == "duplicate-name" {
					deploymentplan.Map(second["metadata"])["uid"] = "22222222-2222-4222-8222-222222222222"
				} else {
					deploymentplan.Map(second["metadata"])["name"] = "another-name"
				}
				list["items"] = []any{pod, second}
			case "missing-items":
				delete(list, "items")
			case "null-items":
				list["items"] = nil
			case "wrong-list-kind":
				list["kind"] = "Status"
			case "wrong-api":
				list["apiVersion"] = "foreign/v1"
			case "pagination":
				list["metadata"] = deploymentplan.Object{"continue": "private-page-token-canary"}
			case "remaining-items":
				list["metadata"] = deploymentplan.Object{"remainingItemCount": 1}
			case "unknown-envelope":
				list["hidden"] = "private-value-canary"
			}
			raw := mustProviderRecoveryJSON(list)
			if mode == "trailing-data" {
				raw = append(raw, []byte("{}")...)
			}
			if _, err := decodeManagedLateLogClaimPods(raw, "video-cloud-dev-video-cloud"); err == nil || strings.Contains(err.Error(), "canary") {
				t.Fatal("incomplete inventory passed or exposed native content")
			}
		})
	}
}

func managedLateClaimInventoryBackend(t *testing.T, store secretStore, raw []byte, mode string) (environmentKubernetesBackend, string) {
	t.Helper()
	dir := t.TempDir()
	response, calls := filepath.Join(dir, "response.json"), filepath.Join(dir, "calls")
	if err := os.WriteFile(response, raw, 0600); err != nil {
		t.Fatal(err)
	}
	script := `#!/bin/sh
set -eu
test "$1" = --kubeconfig && test -f "$2" || exit 73
case "$3" in --request-timeout=*) ;; *) exit 74;; esac
shift 3
test "$#" = 6 && test "$1" = get && test "$2" = pods && test "$3" = --namespace && test "$4" = video-cloud-dev-video-cloud && test "$5" = -o && test "$6" = json || exit 75
printf 'pods-only\n' >> "$RTK_LOG_CLAIM_CALLS"
case "$RTK_LOG_CLAIM_MODE" in
 denied) printf '%s\n' 'private-command-canary token=credential-canary' >&2;exit 1 ;;
 deadline) exec /bin/sleep 5 ;;
 *) cat "$RTK_LOG_CLAIM_RESPONSE" ;;
esac
`
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_LOG_CLAIM_CALLS", calls)
	t.Setenv("RTK_LOG_CLAIM_RESPONSE", response)
	t.Setenv("RTK_LOG_CLAIM_MODE", mode)
	return environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-kubeconfig")}, calls
}

func TestManagedLateLogClaimReadDistinguishesCommandDeadlineAndParsing(t *testing.T) {
	for _, mode := range []string{"valid", "denied", "deadline", "cancelled", "malformed"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			raw := []byte(`{"apiVersion":"v1","kind":"List","metadata":{},"items":[]}`)
			if mode == "malformed" {
				raw = []byte(`{"kind":"Status","message":"private-response-canary"}`)
			}
			backend, calls := managedLateClaimInventoryBackend(t, store, raw, mode)
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			if mode == "deadline" {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), 40*time.Millisecond)
			}
			if mode == "cancelled" {
				cancel()
			}
			defer cancel()
			_, err := readManagedLateLogClaimPods(ctx, backend, "video-cloud-dev-video-cloud")
			if mode == "valid" {
				if err != nil {
					t.Fatal(err)
				}
			} else {
				want := map[string]string{"denied": "command", "deadline": "deadline", "cancelled": "cancelled", "malformed": "envelope"}[mode]
				if err == nil || !strings.Contains(err.Error(), "claim holder Pod inventory "+want) || strings.Contains(err.Error(), "canary") {
					t.Fatal("private failure did not retain its safe stage", err)
				}
			}
			if mode == "cancelled" {
				if _, err := os.Lstat(calls); !os.IsNotExist(err) {
					t.Fatal("cancelled reader started a command")
				}
			}
		})
	}
}

func TestManagedLateLogClaimWaitUsesOnlyExactNamespacePodCollection(t *testing.T) {
	cfg, store, operator, artifact := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operator, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	const namespace, claim, uid = "video-cloud-dev-video-cloud", "original-log-state", "33333333-3333-4333-8333-333333333333"
	body := deploymentplan.Object{"kind": "Deployment", "metadata": deploymentplan.Object{"name": "video-cloud-logingester", "namespace": namespace}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "app", "volumeMounts": []any{deploymentplan.Object{"name": "private-state", "mountPath": filepath.Dir(filepath.Dir(logACKStatePath))}}}}, "volumes": []any{deploymentplan.Object{"name": "private-state", "persistentVolumeClaim": deploymentplan.Object{"claimName": claim}}}}}}}
	ledger := environmentCreateResourceLedger{Schema: "rtk-cloud.resource-ledger/v1", Environment: "dev", Stack: e.Journal.Stack, PlanSHA256: e.Journal.PlanSHA256, Provider: map[string]environmentCreateProviderReceipt{}, Kubernetes: map[string]string{}, Volumes: []environmentCreateVolumeOwner{{Namespace: namespace, PVCName: claim, PVCUID: uid, PVName: "original-pv", PVUID: "44444444-4444-4444-8444-444444444444", NativeID: "123"}}}
	if err := writeEnvironmentCreateDerivedPrivateFile(store, environmentCreateLedgerPath(e), mustProviderRecoveryJSON(ledger)); err != nil {
		t.Fatal(err)
	}
	late := &createManagedLateSessionRecovery{LogProducer: &createLogACKHelperDraft{Original: deploymentplan.Resource{Desired: body}, OriginalClaimUID: uid}}
	backend, calls := managedLateClaimInventoryBackend(t, store, []byte(`{"apiVersion":"v1","kind":"PodList","metadata":{},"items":[]}`), "valid")
	before := deploymentplan.Digest([]any{e.Journal, late, ledger})
	if err := waitManagedLateLogClaimEmpty(context.Background(), environmentCreateBootstrapRuntime{Execution: e, Backend: backend}, late); err != nil {
		t.Fatal("empty original claim did not pass its bounded Pod-only read", err)
	}
	called, err := os.ReadFile(calls)
	if err != nil || string(called) != "pods-only\n" {
		t.Fatal("claim emptiness used unrelated namespace/environment inventory", err)
	}
	if before != deploymentplan.Digest([]any{e.Journal, late, ledger}) {
		t.Fatal("claim inspection changed original state")
	}
	var saved environmentCreateResourceLedger
	raw, err := os.ReadFile(filepath.Join(store.Root, environmentCreateLedgerPath(e)))
	if err != nil || json.Unmarshal(raw, &saved) != nil || deploymentplan.Digest(saved) != deploymentplan.Digest(ledger) {
		t.Fatal("claim inspection changed original volume ledger")
	}
}

func managedLateLogTerminalClaimFixture() (deploymentplan.Resource, deploymentplan.Object, deploymentplan.Object, time.Time) {
	before := time.Date(2026, 10, 17, 1, 0, 0, 0, time.UTC)
	start, finish := before.Add(-24*time.Hour), before.Add(-23*time.Hour)
	uid := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa"
	job := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": "accepted-enrollment", "namespace": "video-cloud-dev-video-cloud"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"restartPolicy": "Never", "containers": []any{deploymentplan.Object{"name": "enroll", "image": logACKNativeImage, "command": []any{"/app/serviceidentity-bootstrap", "service:video-cloud-logingester"}}}, "initContainers": []any{deploymentplan.Object{"name": "private-state", "image": logACKNativeImage}}, "volumes": []any{deploymentplan.Object{"name": "private-state", "persistentVolumeClaim": deploymentplan.Object{"claimName": "original-log-state"}}}}}}}
	owner := deploymentplan.Resource{ID: deploymentplan.ID(job), UID: uid, Desired: deploymentplan.Clone(job)}
	deploymentplan.Map(job["metadata"])["uid"] = uid
	job["status"] = deploymentplan.Object{"active": float64(0), "succeeded": float64(1), "completionTime": finish.Add(time.Minute).Format(time.RFC3339Nano), "conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
	pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "accepted-enrollment-native", "namespace": "video-cloud-dev-video-cloud", "uid": "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb", "ownerReferences": []any{deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "name": "accepted-enrollment", "uid": uid, "controller": true}}}, "spec": deploymentplan.Clone(environmentPKIPod(job))}
	status := func(name string) deploymentplan.Object {
		id := "containerd://original-" + name
		return deploymentplan.Object{"name": name, "restartCount": float64(0), "containerID": id, "imageID": "containerd://sha256:" + strings.Repeat("c", 64), "state": deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": float64(0), "startedAt": start.Format(time.RFC3339Nano), "finishedAt": finish.Format(time.RFC3339Nano), "containerID": id}}}
	}
	pod["status"] = deploymentplan.Object{"phase": "Succeeded", "containerStatuses": []any{status("enroll")}, "initContainerStatuses": []any{status("private-state")}}
	return owner, job, pod, before
}

func TestManagedLateLogClaimSuccessfulHistoryRequiresExactEndedOriginalProcess(t *testing.T) {
	owner, job, pod, before := managedLateLogTerminalClaimFixture()
	original := deploymentplan.Digest([]any{owner, job, pod})
	if !managedLateLogSuccessfulClaimHistory(owner, job, pod, before) {
		t.Fatal("exact accepted historical successful process refused")
	}
	if original != deploymentplan.Digest([]any{owner, job, pod}) {
		t.Fatal("terminal history inspection mutated native or original evidence")
	}
	for _, mode := range []string{"unrecorded", "foreign-job-uid", "changed-job-body", "job-deleting", "job-active", "job-failed", "job-retried", "job-incomplete", "job-failed-condition", "duplicate-condition", "job-late", "pod-failed", "pod-pending", "pod-running", "pod-unknown", "pod-deleting", "foreign-job-owner", "no-controller", "extra-owner", "foreign-namespace", "changed-pod-image", "ephemeral-container", "ephemeral-status", "extra-container", "extra-status", "duplicate-status", "missing-init-status", "wrong-init-image", "init-failed", "process-failed", "process-running", "process-waiting", "process-restart", "previous-process", "missing-container-id", "changed-container-id", "missing-image-id", "missing-start", "missing-finish", "finish-before-start", "finish-at-stop", "finish-after-job", "invalid-state"} {
		t.Run(mode, func(t *testing.T) {
			owner, job, pod, before := managedLateLogTerminalClaimFixture()
			jm, pm := deploymentplan.Map(job["metadata"]), deploymentplan.Map(pod["metadata"])
			js, ps := deploymentplan.Map(job["status"]), deploymentplan.Map(pod["status"])
			regular := deploymentplan.Map(certIssuerObjectList(ps["containerStatuses"])[0])
			state := deploymentplan.Map(regular["state"])
			terminal := deploymentplan.Map(state["terminated"])
			switch mode {
			case "unrecorded":
				owner = deploymentplan.Resource{}
			case "foreign-job-uid":
				jm["uid"] = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
			case "changed-job-body":
				environmentPKIPod(job)["restartPolicy"] = "OnFailure"
			case "job-deleting":
				jm["deletionTimestamp"] = before.Add(-time.Hour).Format(time.RFC3339Nano)
			case "job-active":
				js["active"] = float64(1)
			case "job-failed":
				js["failed"] = float64(1)
			case "job-retried":
				js["succeeded"] = float64(2)
			case "job-incomplete":
				js["conditions"] = []any{}
			case "job-failed-condition":
				js["conditions"] = append(certIssuerObjectList(js["conditions"]), deploymentplan.Object{"type": "Failed", "status": "True"})
			case "duplicate-condition":
				js["conditions"] = append(certIssuerObjectList(js["conditions"]), certIssuerObjectList(js["conditions"])[0])
			case "job-late":
				js["completionTime"] = before.Format(time.RFC3339Nano)
			case "pod-failed", "pod-pending", "pod-running", "pod-unknown":
				ps["phase"] = strings.TrimPrefix(mode, "pod-")
			case "pod-deleting":
				pm["deletionTimestamp"] = before.Add(-time.Hour).Format(time.RFC3339Nano)
			case "foreign-job-owner":
				deploymentplan.Map(certIssuerObjectList(pm["ownerReferences"])[0])["uid"] = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
			case "no-controller":
				deploymentplan.Map(certIssuerObjectList(pm["ownerReferences"])[0])["controller"] = false
			case "extra-owner":
				pm["ownerReferences"] = append(certIssuerObjectList(pm["ownerReferences"]), deploymentplan.Object{"name": "foreign"})
			case "foreign-namespace":
				pm["namespace"] = "foreign"
			case "changed-pod-image":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["spec"])["containers"])[0])["image"] = "foreign@sha256:" + strings.Repeat("d", 64)
			case "ephemeral-container":
				deploymentplan.Map(pod["spec"])["ephemeralContainers"] = []any{deploymentplan.Object{"name": "debug"}}
			case "ephemeral-status":
				ps["ephemeralContainerStatuses"] = []any{deploymentplan.Object{"name": "debug"}}
			case "extra-container":
				deploymentplan.Map(pod["spec"])["containers"] = append(certIssuerObjectList(deploymentplan.Map(pod["spec"])["containers"]), deploymentplan.Object{"name": "extra"})
			case "extra-status":
				ps["containerStatuses"] = append(certIssuerObjectList(ps["containerStatuses"]), deploymentplan.Object{"name": "extra"})
			case "duplicate-status":
				ps["containerStatuses"] = append(certIssuerObjectList(ps["containerStatuses"]), regular)
			case "missing-init-status":
				delete(ps, "initContainerStatuses")
			case "wrong-init-image":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["spec"])["initContainers"])[0])["image"] = "foreign"
			case "init-failed":
				deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(ps["initContainerStatuses"])[0])["state"])["terminated"])["exitCode"] = float64(1)
			case "process-failed":
				terminal["exitCode"] = float64(1)
			case "process-running":
				state["running"] = deploymentplan.Object{"startedAt": before.Add(-time.Hour).Format(time.RFC3339Nano)}
			case "process-waiting":
				state["waiting"] = deploymentplan.Object{"reason": "private-native-canary"}
			case "process-restart":
				regular["restartCount"] = float64(1)
			case "previous-process":
				regular["lastState"] = deploymentplan.Object{"terminated": terminal}
			case "missing-container-id":
				delete(regular, "containerID")
			case "changed-container-id":
				terminal["containerID"] = "foreign-container"
			case "missing-image-id":
				delete(regular, "imageID")
			case "missing-start":
				delete(terminal, "startedAt")
			case "missing-finish":
				delete(terminal, "finishedAt")
			case "finish-before-start":
				terminal["finishedAt"] = before.Add(-25 * time.Hour).Format(time.RFC3339Nano)
			case "finish-at-stop":
				terminal["finishedAt"] = before.Format(time.RFC3339Nano)
			case "finish-after-job":
				terminal["finishedAt"] = before.Add(-22 * time.Hour).Format(time.RFC3339Nano)
			case "invalid-state":
				regular["state"] = "hidden-state"
			}
			unchanged := deploymentplan.Digest([]any{owner, job, pod})
			if managedLateLogSuccessfulClaimHistory(owner, job, pod, before) || unchanged != deploymentplan.Digest([]any{owner, job, pod}) {
				t.Fatal("unproven, active, failed or changed process was exempted or modified")
			}
		})
	}
}

func managedLateLogRecordClaimJobFixture(t *testing.T) (secretStore, *environmentCreateExecution, deploymentplan.Resource, deploymentplan.Object, deploymentplan.Object, time.Time) {
	t.Helper()
	store := makeIsolatedTestSecretStore(t, "dev")
	owner, job, pod, before := managedLateLogTerminalClaimFixture()
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped", ConfigurationSHA256: strings.Repeat("1", 64), ReleaseSHA256: strings.Repeat("2", 64), BindingsSHA256: strings.Repeat("3", 64)}, []deploymentplan.Definition{{Object: owner.Desired, Phase: "bootstrap"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	owner.Desired = deploymentplan.Clone(plan.Resources[0].Desired)
	child := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Accepted: true, AcceptanceStarted: true, Completed: []string{owner.ID}, Owners: map[string]string{owner.ID: owner.UID}}
	for relative, value := range map[string]any{"deployment/rollback/" + plan.SHA256() + "/plan.json": plan, "deployment/rollback/" + plan.SHA256() + "/journal.json": child} {
		if err := writeEnvironmentCreateDerivedPrivateFile(store, relative, mustProviderRecoveryJSON(value)); err != nil {
			t.Fatal(err)
		}
	}
	intent, err := environmentCreateKubernetesStageIntent(plan)
	if err != nil {
		t.Fatal(err)
	}
	sha, err := environmentCreatePlanSHA256(intent)
	if err != nil {
		t.Fatal(err)
	}
	key := "kubernetes/exact-original-enrollment"
	e := &environmentCreateExecution{Store: store, Journal: environmentCreateJournal{Environment: "dev", Stack: "video-cloud-dev", Entries: []environmentCreateJournalEntry{{Key: key, Phase: strings.TrimPrefix(key, "kubernetes/"), State: "completed", IntentSHA256: sha, Receipt: mustProviderRecoveryJSON(map[string]any{"plan_sha256": plan.SHA256(), "owners": child.Owners}), StartedAt: before.Add(-25 * time.Hour), CompletedAt: before.Add(-22 * time.Hour)}}}}
	return store, e, owner, job, pod, before
}

func TestManagedLateLogClaimHistoryRequiresGenuineAcceptedStageMembership(t *testing.T) {
	for _, mode := range []string{"original", "unknown-pod-uid", "foreign-job-uid", "missing-receipt-owner", "pending-stage", "accepted-after-stop", "changed-intent", "changed-child", "unaccepted-child"} {
		t.Run(mode, func(t *testing.T) {
			store, e, owner, _, pod, before := managedLateLogRecordClaimJobFixture(t)
			var receipt struct {
				Plan   string            `json:"plan_sha256"`
				Owners map[string]string `json:"owners"`
			}
			if json.Unmarshal(e.Journal.Entries[0].Receipt, &receipt) != nil {
				t.Fatal("fixture receipt")
			}
			switch mode {
			case "unknown-pod-uid":
				deploymentplan.Map(pod["metadata"])["uid"] = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
			case "foreign-job-uid":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["metadata"])["ownerReferences"])[0])["uid"] = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
			case "missing-receipt-owner":
				receipt.Owners = map[string]string{}
				e.Journal.Entries[0].Receipt = mustProviderRecoveryJSON(receipt)
			case "pending-stage":
				e.Journal.Entries[0].State = "pending"
			case "accepted-after-stop":
				e.Journal.Entries[0].CompletedAt = before
			case "changed-intent":
				e.Journal.Entries[0].IntentSHA256 = strings.Repeat("f", 64)
			case "changed-child", "unaccepted-child":
				path := filepath.Join(store.Root, "deployment/rollback", receipt.Plan, "journal.json")
				var child deploymentplan.Journal
				if readExecutionJSON(path, &child) != nil {
					t.Fatal("fixture child")
				}
				if mode == "changed-child" {
					child.Owners[owner.ID] = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
				} else {
					child.Accepted = false
				}
				if err := os.WriteFile(path, mustProviderRecoveryJSON(child), 0600); err != nil {
					t.Fatal(err)
				}
			}
			unchanged := deploymentplan.Digest([]any{e.Journal, pod})
			actual, err := managedLateLogRecordedClaimJob(store, e, pod, before)
			// A listed Pod's UID is not itself an enrollment receipt: its exact
			// Job parent must still be authenticated, then the full live process
			// proof binds that same listed UID without claiming an archived UID.
			valid := mode == "original" || mode == "unknown-pod-uid"
			if valid && (err != nil || actual.UID != owner.UID || deploymentplan.Digest(actual.Desired) != deploymentplan.Digest(owner.Desired)) || !valid && err == nil && actual.ID != "" || unchanged != deploymentplan.Digest([]any{e.Journal, pod}) {
				t.Fatal("historical receipt authority changed or unrecorded Job was accepted", err)
			}
		})
	}
}
