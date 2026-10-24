package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateBackendWaitCapturesDurableOwnersAfterReadiness(t *testing.T) {
	for _, mode := range []string{"claim-ready", "stateful-ready", "readiness-failed", "capture-failed"} {
		t.Run(mode, func(t *testing.T) {
			ctx := context.Background()
			cfg, store, operatorSHA, artifact := createStateFixture(t, "dev")
			execution, err := startEnvironmentCreateExecution(ctx, cfg, artifact, operatorSHA, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { execution.Close() })
			kubeconfig := []byte("private-frozen-test-kubeconfig")
			if err := store.write("kube/kubeconfig.yaml", kubeconfig, false); err != nil {
				t.Fatal(err)
			}
			if err := recordEnvironmentCreateProviderOwner(ctx, execution, environmentCreateProviderReceipt{Key: "lke", Kind: "lke", NativeID: "201"}); err != nil {
				t.Fatal(err)
			}
			directory := t.TempDir()
			log := filepath.Join(directory, "calls")
			kubectl := filepath.Join(directory, "kubectl")
			writeTestFile(t, kubectl, `#!/bin/sh
set -eu
printf '%s\n' "$*" >>"$RTK_CREATE_WAIT_LOG"
case "$*" in
  *' wait '*|*' rollout status '*)
    if [ "$RTK_CREATE_WAIT_MODE" = readiness-failed ]; then exit 1; fi ;;
  *'get persistentvolumes -o json'*)
    if [ "$RTK_CREATE_WAIT_MODE" = capture-failed ]; then exit 1; fi
    printf '{"items":[{"metadata":{"name":"original-pv","uid":"pv-owner"},"spec":{"claimRef":{"namespace":"video-cloud-dev-platform","name":"data","uid":"claim-owner"},"csi":{"driver":"linodebs.csi.linode.com","volumeHandle":"101-pvc-original"}}}]}' ;;
  *) exit 2 ;;
esac
`)
			if err := os.Chmod(kubectl, 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_CREATE_WAIT_LOG", log)
			t.Setenv("RTK_CREATE_WAIT_MODE", mode)
			kind := "PersistentVolumeClaim"
			if mode == "stateful-ready" {
				kind = "StatefulSet"
			}
			resource := deploymentplan.Resource{ID: kind + "/video-cloud-dev-platform/data", Action: "create", Desired: deploymentplan.Object{"kind": kind}}
			var backend deploymentplan.Backend = environmentCreateKubernetesBackend{
				environmentKubernetesBackend: environmentKubernetesBackend{Store: store, KubeconfigContent: kubeconfig, RolloutTimeout: time.Minute},
				execution:                    execution, cfg: cfg, store: store,
			}
			err = backend.Wait(ctx, resource)
			calls := readTestFile(t, log)
			if !strings.Contains(calls, kind+"/data --timeout=60s") {
				t.Fatal("create wait skipped native readiness", calls)
			}
			ledger, ledgerErr := readEnvironmentCreateLedger(execution)
			proof, known, proofErr := readDeploymentRemovalVolumes(cfg)
			if ledgerErr != nil || proofErr != nil {
				t.Fatal("ownership evidence cannot be read", ledgerErr, proofErr)
			}
			if mode == "readiness-failed" || mode == "capture-failed" {
				var blocker *deploymentplan.Blocker
				wantID := "deployment.create.resources"
				if mode == "readiness-failed" {
					wantID = "deployment.rollout"
				}
				if !errors.As(err, &blocker) || blocker.ID != wantID || len(ledger.Volumes) != 0 || known {
					t.Fatal("failed readiness or capture created successful ownership evidence", err, ledger.Volumes, known)
				}
				if mode == "readiness-failed" && strings.Contains(calls, "get persistentvolumes") {
					t.Fatal("volume ownership was captured before readiness passed", calls)
				}
				return
			}
			want := environmentCreateVolumeOwner{NativeID: "101", PVName: "original-pv", PVUID: "pv-owner", PVCName: "data", PVCUID: "claim-owner", Namespace: "video-cloud-dev-platform"}
			if err != nil || len(ledger.Volumes) != 1 || ledger.Volumes[0] != want || !known || proof.ClusterID != "201" || proof.KubeSHA256 != environmentPreflightBytesSHA256(kubeconfig) || len(proof.VolumeIDs) != 1 || proof.VolumeIDs[0] != "101" {
				t.Fatal("create wait did not persist original durable ownership and removal proof", err, ledger.Volumes, proof)
			}
		})
	}
}
