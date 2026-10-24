package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedCoreAllVolumesDistinguishesReadFailureFromIdentityDrift(t *testing.T) {
	for _, mode := range []string{"retained", "expired", "cancelled", "claim-timeout", "volume-timeout", "claim-uid", "volume-uid", "claim-ref", "native-id", "missing-volume", "seventeen-records"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			e := &environmentCreateExecution{Store: store, Journal: environmentCreateJournal{Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: strings.Repeat("a", 64)}}
			ledger := environmentCreateResourceLedger{Schema: "rtk-cloud.resource-ledger/v1", Environment: "dev", Stack: e.Journal.Stack, PlanSHA256: e.Journal.PlanSHA256, Provider: map[string]environmentCreateProviderReceipt{}, Kubernetes: map[string]string{}}
			dir := t.TempDir()
			owners := map[string]string{}
			for i := range 18 {
				owner := environmentCreateVolumeOwner{Namespace: "video-cloud-dev-secrets", PVCName: fmt.Sprintf("claim-%02d", i), PVCUID: fmt.Sprintf("original-pvc-%02d", i), PVName: fmt.Sprintf("pv-%02d", i), PVUID: fmt.Sprintf("original-pv-%02d", i), NativeID: fmt.Sprint(100 + i)}
				id := "PersistentVolumeClaim/" + owner.Namespace + "/" + owner.PVCName
				ledger.Volumes = append(ledger.Volumes, owner)
				ledger.Kubernetes[id] = owner.PVCUID
				owners[id] = deploymentplan.Digest(owner)
				claim := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"namespace": owner.Namespace, "name": owner.PVCName, "uid": owner.PVCUID}, "spec": deploymentplan.Object{"volumeName": owner.PVName}, "status": deploymentplan.Object{"phase": "Bound"}}
				volume := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": owner.PVName, "uid": owner.PVUID}, "spec": deploymentplan.Object{"claimRef": deploymentplan.Object{"namespace": owner.Namespace, "name": owner.PVCName, "uid": owner.PVCUID}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": owner.NativeID + "-pvc-original"}}, "status": deploymentplan.Object{"phase": "Bound"}}
				if i == 0 {
					switch mode {
					case "claim-uid":
						deploymentplan.Map(claim["metadata"])["uid"] = "replacement"
					case "volume-uid":
						deploymentplan.Map(volume["metadata"])["uid"] = "replacement"
					case "claim-ref":
						deploymentplan.Map(deploymentplan.Map(volume["spec"])["claimRef"])["uid"] = "replacement"
					case "native-id":
						deploymentplan.Map(deploymentplan.Map(volume["spec"])["csi"])["volumeHandle"] = "999-pvc-replacement"
					}
				}
				for name, value := range map[string]deploymentplan.Object{"PersistentVolumeClaim-" + owner.PVCName: claim, "PersistentVolume-" + owner.PVName: volume} {
					if err := os.WriteFile(filepath.Join(dir, name+".json"), mustProviderRecoveryJSON(value), 0600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode == "seventeen-records" {
				ledger.Volumes = ledger.Volumes[:17]
			}
			ledgerRaw := mustProviderRecoveryJSON(ledger)
			active := managedTransportVolumeLedgerActive{Schema: "rtk-cloud.resource-ledger-active/v1", Environment: "dev", Stack: e.Journal.Stack, Plan: e.Journal.PlanSHA256, LedgerSHA: deploymentplan.Digest(ledger)}
			activeRaw := mustProviderRecoveryJSON(active)
			for relative, raw := range map[string][]byte{environmentCreateLedgerPath(e): ledgerRaw, "deployment/resources/active.json": activeRaw} {
				if err := writeEnvironmentCreateDerivedPrivateFile(store, relative, raw); err != nil {
					t.Fatal(err)
				}
			}
			calls := filepath.Join(dir, "calls")
			script := `#!/bin/sh
set -eu
printf 'call\n' >> "$RTK_VOLUME_READ_TEST_CALLS"
while [ "$#" -gt 0 ] && [ "$1" != get ]; do shift; done
[ "$#" -ge 3 ] || exit 73
shift
kind=$1
name=$2
case "$RTK_VOLUME_READ_TEST_MODE:$kind" in
 claim-timeout:PersistentVolumeClaim|volume-timeout:PersistentVolume)
  printf '%s\n' 'private-volume-canary: request deadline exceeded; token=must-not-escape' >&2
  exit 1 ;;
 missing-volume:PersistentVolume) exit 0 ;;
esac
cat "$RTK_VOLUME_READ_TEST_DIRECTORY/$kind-$name.json"
`
			if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_VOLUME_READ_TEST_CALLS", calls)
			t.Setenv("RTK_VOLUME_READ_TEST_MODE", mode)
			t.Setenv("RTK_VOLUME_READ_TEST_DIRECTORY", dir)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			if mode == "expired" {
				cancel()
				ctx, cancel = context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
			} else if mode == "cancelled" {
				cancel()
			}
			defer cancel()
			materials := map[string]string{"sentinel": "unchanged"}
			before := deploymentplan.Digest(e.Journal)
			err := verifyManagedCoreAllVolumes(ctx, e, environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-test-kubeconfig")}, materials)
			if mode == "retained" {
				if err != nil || materials["accepted-service-core-managed-complete-current-native-volumes"] != deploymentplan.Digest(owners) {
					t.Fatal("genuine original eighteen-volume proof refused", err)
				}
			} else {
				if err == nil || len(materials) != 1 {
					t.Fatal("failed read or changed original volume accepted")
				}
				if strings.Contains(err.Error(), "private-volume-canary") || strings.Contains(err.Error(), "must-not-escape") {
					t.Fatal("private native error escaped")
				}
				switch mode {
				case "expired", "cancelled", "claim-timeout", "volume-timeout":
					var blocker *deploymentplan.Blocker
					code, resource := "KUBE_TIMEOUT", "PersistentVolumeClaim/video-cloud-dev-secrets/claim-00"
					if mode == "cancelled" {
						code = "CHECK_CANCELLED"
					}
					if mode == "volume-timeout" {
						resource = "PersistentVolume//pv-00"
					}
					if !errors.As(err, &blocker) || blocker.ID != "deployment.access.resource" || blocker.Resource != resource || !strings.Contains(blocker.Cause, "["+code+"]") || strings.Contains(err.Error(), "native-volume-") {
						t.Fatal("read failure misreported as original volume drift", err)
					}
				case "seventeen-records":
					if !strings.Contains(err.Error(), "current-eighteen-volumes") {
						t.Fatal("original eighteen-record guard changed", err)
					}
				default:
					if !strings.Contains(err.Error(), "native-volume-PersistentVolumeClaim/video-cloud-dev-secrets/claim-00") {
						t.Fatal("real identity drift lost its original refusal", err)
					}
				}
			}
			if mode == "expired" || mode == "cancelled" || mode == "seventeen-records" {
				if _, err := os.Lstat(calls); !os.IsNotExist(err) {
					t.Fatal("unqualified read started a command")
				}
			}
			for relative, expected := range map[string][]byte{environmentCreateLedgerPath(e): ledgerRaw, "deployment/resources/active.json": activeRaw} {
				got, readErr := readBoundedEnvironmentFile(filepath.Join(store.Root, relative), true)
				if readErr != nil || string(got) != string(expected) {
					t.Fatal("original volume evidence changed", readErr)
				}
			}
			if deploymentplan.Digest(e.Journal) != before {
				t.Fatal("original parent journal changed")
			}
		})
	}
}
