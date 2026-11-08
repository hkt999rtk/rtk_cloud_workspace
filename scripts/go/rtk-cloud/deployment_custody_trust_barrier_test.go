package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCustodyTransportBarrierVerifiesMountedSnapshotAndOriginalPod(t *testing.T) {
	for _, mode := range []string{"current", "stale", "replacement", "foreign-owner", "unavailable"} {
		t.Run(mode, func(t *testing.T) {
			generation := strings.Repeat("a", 64)
			snapshot := environmentCustodyTransportTrust{Schema: "rtk.custody.transport/v1", Environment: "dev", Generation: generation, ServerName: "openbao.video-cloud-dev-secrets.svc", ManagedRootSHA256: strings.Repeat("b", 64)}
			wanted, _ := json.Marshal(snapshot)
			prefix := "dev/" + generation + "/controller"
			container := deploymentplan.Object{"name": "controller", "env": environmentCreateEnv(map[string]string{"PKI_STATE_BACKEND": "openbao", "PKI_STATE_RECORD_PREFIX": prefix, "PKI_STATE_TRANSPORT_TRUST": environmentCustodyTrustPath + "/transport.json"}), "volumeMounts": []any{environmentCreateServiceMount("pki-custody-trust", environmentCustodyTrustPath, true)}}
			pod := deploymentplan.Object{"metadata": deploymentplan.Object{"name": "original", "namespace": "video-cloud-dev-video-cloud", "uid": "original-uid"}, "spec": deploymentplan.Object{"serviceAccountName": "controller", "containers": []any{container}, "volumes": []any{environmentCreateServiceVolume("pki-custody-trust", "configMap", environmentCustodyTrustName)}}, "status": deploymentplan.Object{"phase": "Running", "containerStatuses": []any{deploymentplan.Object{"name": "controller", "state": deploymentplan.Object{"running": deploymentplan.Object{}}}}}}
			if mode == "foreign-owner" {
				deploymentplan.Map(pod["spec"])["serviceAccountName"] = "foreign"
			}
			binding := &environmentCustodyBinding{Generation: generation, Owners: []environmentCustodyOwnerBinding{{ID: "controller", Namespace: "video-cloud-dev-video-cloud", ServiceAccount: "controller"}}}
			command := func(ctx context.Context, input []byte, args ...string) ([]byte, error) {
				if mode == "unavailable" {
					return nil, fmt.Errorf("private connection failure")
				}
				joined := strings.Join(args, " ")
				if strings.Contains(joined, "get pods") {
					return json.Marshal(deploymentplan.Object{"items": []any{pod}})
				}
				if strings.Contains(joined, "get pod ") {
					after := deploymentplan.Clone(pod)
					if mode == "replacement" {
						deploymentplan.Map(after["metadata"])["uid"] = "replaced"
					}
					return json.Marshal(after)
				}
				if strings.Contains(joined, " cat ") {
					if mode == "stale" {
						return []byte(`{"schema":"old"}`), nil
					}
					return wanted, nil
				}
				t.Fatal("unexpected barrier command", joined)
				return nil, nil
			}
			err := checkEnvironmentCustodyTransportProjection(context.Background(), binding, snapshot, command)
			if (err == nil) != (mode == "current") {
				t.Fatal("mounted trust barrier disagrees with original scope", mode, err)
			}
			if err != nil && strings.Contains(err.Error(), "private connection failure") {
				t.Fatal("private backend failure leaked")
			}
		})
	}
}
