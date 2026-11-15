package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCustodyTokenAccessUsesActualImageGroupAndResumesSameSession(t *testing.T) {
	for _, gid := range []float64{999, 54321} {
		t.Run(strings.ReplaceAll(fmt.Sprint(gid), ".", "-"), func(t *testing.T) {
			store, e, template, b := freshStageFixture(t)
			image := "ghcr.io/example/api@sha256:" + strings.Repeat("a", 64)
			container := deploymentplan.Object{
				"name": "app", "image": image,
				"env":          environmentCreateEnv(map[string]string{"PKI_STATE_BACKEND": "openbao", "PKI_BOOTSTRAP_SESSION_ID": "11111111-1111-4111-8111-111111111111"}),
				"volumeMounts": []any{environmentCreateServiceMount("pki-custody-auth", "/run/pki-custody-auth", true)},
			}
			token := deploymentplan.Object{"serviceAccountToken": deploymentplan.Object{"audience": "openbao", "path": "openbao.jwt", "expirationSeconds": float64(900)}}
			volume := deploymentplan.Object{"name": "pki-custody-auth", "projected": deploymentplan.Object{"defaultMode": float64(0440), "sources": []any{token}}}
			podSpec := deploymentplan.Object{"serviceAccountName": "factoryenroll", "containers": []any{container}, "volumes": []any{volume}}
			body := deploymentplan.Object{
				"apiVersion": "apps/v1", "kind": "Deployment",
				"metadata": deploymentplan.Object{"name": "factoryenroll", "namespace": e.Journal.Stack + "-video-cloud"},
				"spec":     deploymentplan.Object{"replicas": float64(1), "template": deploymentplan.Object{"spec": podSpec}},
			}
			defs := []deploymentplan.Definition{{Object: body, Phase: "workload"}}
			in := template.Inputs
			in.BindingsSHA256 = environmentCreateStageBindingsSHA("same-session", defs)
			p, err := deploymentplan.Compile(in, defs, nil)
			if err != nil {
				t.Fatal(err)
			}
			check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
			run := func(stage string, p *deploymentplan.Plan) error {
				return executeEnvironmentCreateStagePlan(t.Context(), store, e, stage, p, b, check, check, check, func(context.Context) error { return nil }, io.Discard)
			}
			b.failure = "after-write-workload"
			if run("same-session", p) == nil {
				t.Fatal("source readiness interruption missing")
			}
			j, ok, err := p.ReadExecution(store.Root)
			if err != nil || !ok || j.Accepted {
				t.Fatal(err)
			}
			id := p.Resources[0].ID
			uid := j.Owners[id]
			ns := e.Journal.Stack + "-video-cloud"
			rs := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": deploymentplan.Object{"name": "factory-rs", "namespace": ns, "uid": "22222222-2222-4222-8222-222222222222", "ownerReferences": []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "name": "factoryenroll", "uid": uid, "controller": true}}}}
			pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "factory-pod", "namespace": ns, "uid": "33333333-3333-4333-8333-333333333333", "ownerReferences": []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "name": "factory-rs", "uid": deploymentplan.Map(rs["metadata"])["uid"], "controller": true}}}, "spec": deploymentplan.Clone(environmentPKIPod(body)), "status": deploymentplan.Object{"containerStatuses": []any{deploymentplan.Object{"name": "app", "imageID": "containerd://sha256:" + strings.Repeat("a", 64), "user": deploymentplan.Object{"linux": deploymentplan.Object{"uid": float64(10001), "gid": gid, "supplementalGroups": []any{gid}}}}}}}
			sources := []serviceSessionRecordedPlan{{Stage: "same-session", Plan: p, Journal: j, SelectedIDs: map[string]bool{id: true}}}
			live := []deploymentplan.Object{b.objects[id], rs, pod}
			next, facts, err := compileCustodyTokenAccess(sources, live)
			if err != nil || next == nil || len(next.Resources) != 1 || len(facts) != 1 || facts[0].GID != int64(gid) {
				t.Fatal("actual runtime group not selected", err)
			}
			r := next.Resources[0]
			proof := custodyTokenAccessProof{Plan: next, Facts: facts}
			if err := validateCustodyTokenAccessDelta(proof); err != nil {
				t.Fatal("legitimate native group delta", err)
			}
			corrupt := *next
			corrupt.Resources = append([]deploymentplan.Resource(nil), next.Resources...)
			corrupt.Resources[0].Desired = deploymentplan.Clone(r.Desired)
			deploymentplan.Map(corrupt.Resources[0].Desired["spec"])["replicas"] = float64(2)
			if err := validateCustodyTokenAccessDelta(custodyTokenAccessProof{Plan: &corrupt, Facts: facts}); err == nil {
				t.Fatal("unrelated workload mutation accepted")
			}
			if r.UID != uid || deploymentplan.Map(environmentPKIPod(r.Desired)["securityContext"])["fsGroup"] != gid || !deploymentplan.Matches(p.Resources[0].Desired, r.Desired) {
				t.Fatal("repair changed identity, session or a frozen field")
			}
			b.failure, b.failed = "", false
			if err := run(custodyTokenAccessStage, next); err != nil {
				t.Fatal(err)
			}
			if err := p.AdmitResumable(t.Context(), b, store.Root); err != nil {
				t.Fatal("old admitted session cannot reuse exact known writes", err)
			}
			if err := run("same-session", p); err != nil {
				t.Fatal("same-session retry failed", err)
			}
			accepted, ok, err := p.ReadExecution(store.Root)
			if err != nil || !ok || !accepted.Accepted || accepted.Owners[id] != uid || b.writes[id] != 2 {
				t.Fatal("retry replayed old writes or fabricated completion", err)
			}
			for _, mode := range []string{"foreign-uid", "missing-runtime-user", "wrong-image"} {
				t.Run(mode, func(t *testing.T) {
					d := deploymentplan.Clone(live[0])
					rp := deploymentplan.Clone(pod)
					switch mode {
					case "foreign-uid":
						deploymentplan.Map(d["metadata"])["uid"] = "foreign"
					case "missing-runtime-user":
						managedNamedField(deploymentplan.Map(rp["status"])["containerStatuses"], "app")["user"] = nil
					case "wrong-image":
						managedNamedField(deploymentplan.Map(rp["spec"])["containers"], "app")["image"] = "ghcr.io/example/other@sha256:" + strings.Repeat("b", 64)
					}
					if _, _, err := compileCustodyTokenAccess(sources, []deploymentplan.Object{d, rs, rp}); err == nil {
						t.Fatal("unknown runtime facts accepted")
					}
				})
			}
		})
	}
}
