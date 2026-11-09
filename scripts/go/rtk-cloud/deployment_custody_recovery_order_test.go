package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCustodyRecoveryInstallsProviderTransportBeforeAnyRemoteCache(t *testing.T) {
	for _, online := range []bool{false, true} {
		t.Run(map[bool]string{false: "remote-root", true: "remote-intermediate"}[online], func(t *testing.T) {
			f := newOnlineRuntimeTestFixture(t)
			remote := crlMaintenanceConsumer{Namespace: "video-cloud-staging-video-cloud", Pod: "faulted-remote", PodUID: "original-remote", Container: "app", StatePath: "/identity/crl.json", Custody: true}
			if online {
				remote.IssuerID = f.item.Issuer.ID
			} else {
				root, _, _ := maintenanceFixture(t, "staging", time.Now().UTC().Add(72*time.Hour))
				root.Issuer.ID = "22222222-2222-4222-8222-222222222222"
				root.Issuer.Document["issuer_id"] = root.Issuer.ID
				root.Snapshot.Prior.IssuerID = root.Issuer.ID
				remote.IssuerID = root.Issuer.ID
				f.inventory.Issuers = append(f.inventory.Issuers, root)
				f.records[root.Issuer.ID] = root.Snapshot.Prior
			}
			// Deliberately discover the remote owner first; list order is not a
			// transport dependency and cannot select recovery order.
			f.inventory.Consumers = append([]crlMaintenanceConsumer{remote}, f.inventory.Consumers...)
			originalRead := f.runtime.readOverride
			remoteSeen := false
			f.runtime.readOverride = func(ctx context.Context, id string) (deploymentplan.Object, error) {
				if id == "Pod/"+remote.Namespace+"/"+remote.Pod {
					remoteSeen = true
					if f.installCalls == 0 {
						t.Error("KV-dependent cache attempted before native provider transport installation")
					}
					return nil, errors.New("stop after observing first remote dependency")
				}
				return originalRead(ctx, id)
			}
			if err := f.runtime.converge(t.Context(), f.inventory, f.records); err == nil {
				t.Fatal("fixture must stop at remote operation")
			}
			if !remoteSeen || f.installCalls != 1 {
				t.Fatalf("recovery never reached remote cache after original provider installation: remote=%v installs=%d", remoteSeen, f.installCalls)
			}
		})
	}
}
