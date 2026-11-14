package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCRLMaintenanceRetainsOriginalUnfinishedCreateFence(t *testing.T) {
	for _, mode := range []string{"valid", "running-original", "foreign-kube"} {
		t.Run(mode, func(t *testing.T) {
			cfg, _, first, _, parent := providerExecutionContractFixture(t)
			_ = first.Close()
			kube := []byte("isolated-native-kubeconfig\n")
			cfg, store, original, _, parent := providerExecutionContractFixture(t, providerExecutionContractInputs{cfg: cfg, bootstrap: parent.Bootstrap, kubeconfig: kube, operator: map[string]string{}})
			e, _ := providerExecutionContractFork(t, original, parent, "isolated-original-maintenance")
			ownerPath := filepath.Join(store.Root, "deployment/.execution-active/owner.json")
			beforeOwner, err := os.ReadFile(ownerPath)
			if err != nil {
				t.Fatal(err)
			}
			beforeEntries := deploymentplan.Digest(e.Journal.Entries)
			if mode != "running-original" {
				_ = e.Close()
			}
			native, _, _ := unifiedSessionNativeBackend(t, store, map[string]deploymentplan.Object{}, nil)
			native.KubeconfigContent = bytes.TrimSpace(kube)
			sourceSHA := environmentPreflightBytesSHA256(kube)
			if mode == "foreign-kube" {
				native.KubeconfigContent = []byte("another-environment-kube")
				sourceSHA = environmentPreflightBytesSHA256(native.KubeconfigContent)
			}
			r := &maintenanceRuntime{cfg: cfg, store: store, backend: native, sourceKubeconfigSHA: sourceSHA}
			p := rootCRLLeasePlan(cfg, environmentPreflightBytesSHA256(native.KubeconfigContent))
			err = r.acquireMaintenanceExecution(t.Context(), p)
			if mode == "valid" {
				if err != nil || r.lease == nil || r.createExecution == nil || r.createExecution.Journal.PlanSHA256 != e.Journal.PlanSHA256 || r.lease.Check(t.Context()) != nil {
					t.Fatalf("maintenance must use the verified original create owner instead of rejecting its own fence: %v", err)
				}
				if deploymentplan.Digest(r.createExecution.Journal.Entries) != beforeEntries || !r.createExecution.Journal.CompletedAt.IsZero() || len(r.createExecution.Journal.KubernetesLeases) != len(e.Journal.KubernetesLeases)+1 {
					t.Fatal("maintenance changed original deployment intent or invented completion")
				}
				if other, _, err := loadCreateRecoveryExecutionOwner(cfg, store, e.Journal.PlanSHA256); err == nil {
					_ = other.Close()
					t.Fatal("original owner was not held during maintenance")
				}
				if err = r.lease.Release(t.Context()); err != nil {
					t.Fatal(err)
				}
				_ = r.createExecution.Close()
			} else if err == nil || r.lease != nil || r.createExecution != nil {
				t.Fatal("maintenance adopted an active executor or foreign Kubernetes binding", mode, err)
			}
			afterOwner, err := os.ReadFile(ownerPath)
			if err != nil || string(afterOwner) != string(beforeOwner) {
				t.Fatal("original execution fence was removed or replaced", err)
			}
		})
	}
}
