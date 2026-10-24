package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateNormalTailWorkloadOpenBaoUsesOriginalShareAfterTargetReload(t *testing.T) {
	for _, mode := range []string{"replacement", "already-ready", "foreign-controller", "foreign-pod", "wrong-revision", "changed-init", "changed-share", "canceled"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, operator, artifact := createStateFixture(t, "dev")
			e, err := startEnvironmentCreateExecution(t.Context(), cfg, artifact, operator, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = e.Close() })
			controllerUID, podUID := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
			share := base64.StdEncoding.EncodeToString([]byte(strings.Repeat("a", 32)))
			initialized := mustProviderRecoveryJSON(deploymentplan.Object{"unseal_keys_b64": []string{share}, "root_token": "isolated-original-token"})
			for rel, raw := range map[string][]byte{"openbao/init.json": initialized, "openbao/root-token": []byte("isolated-original-token\n"), "openbao/unseal-key": []byte(share + "\n")} {
				if err := store.write(rel, raw, false); err != nil {
					t.Fatal(err)
				}
			}
			original := deploymentplan.Object{"init_sha256": environmentPreflightBytesSHA256(initialized), "statefulset_uid": controllerUID, "root_token_ref": "openbao/root-token", "unseal_ref": "openbao/unseal-key"}
			if mode == "changed-init" {
				original["init_sha256"] = strings.Repeat("f", 64)
			}
			if mode == "changed-share" {
				if err := store.write("openbao/unseal-key", []byte(base64.StdEncoding.EncodeToString([]byte(strings.Repeat("b", 32)))), true); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.BeginEntry("openbao-initialize", "bootstrap", []byte(`{"isolated":true}`), time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if err := e.RecordReceipt("openbao-initialize", mustProviderRecoveryJSON(original)); err != nil {
				t.Fatal(err)
			}
			if err := e.CompleteEntry("openbao-initialize", time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			image := "ghcr.io/example/openbao@sha256:" + strings.Repeat("a", 64)
			want := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"name": "openbao", "namespace": e.Journal.Stack + "-secrets"}, "spec": deploymentplan.Object{"replicas": float64(1), "updateStrategy": deploymentplan.Object{"type": "OnDelete"}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"annotations": deploymentplan.Object{"rtk.cloud/service-bundles": "selected-maintained-target"}}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "openbao", "image": image}}}}}}
			controller := deploymentplan.Clone(want)
			meta := deploymentplan.Map(controller["metadata"])
			meta["uid"], meta["resourceVersion"], meta["generation"] = controllerUID, "2", float64(2)
			controller["status"] = deploymentplan.Object{"observedGeneration": float64(2), "updateRevision": "selected-target-revision"}
			pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "openbao-0", "namespace": meta["namespace"], "uid": podUID, "resourceVersion": "3", "labels": deploymentplan.Object{"controller-revision-hash": "selected-target-revision"}, "ownerReferences": []any{deploymentplan.Object{"controller": true, "apiVersion": "apps/v1", "kind": "StatefulSet", "name": "openbao", "uid": controllerUID}}}, "spec": deploymentplan.Clone(environmentPKIPod(want)), "status": deploymentplan.Object{"phase": "Running", "conditions": []any{deploymentplan.Object{"type": "Ready", "status": "False"}}}}
			switch mode {
			case "foreign-controller":
				meta["uid"] = "33333333-3333-4333-8333-333333333333"
			case "foreign-pod":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["metadata"])["ownerReferences"])[0])["uid"] = "33333333-3333-4333-8333-333333333333"
			}
			directory := t.TempDir()
			for name, object := range map[string]deploymentplan.Object{"controller.json": controller, "pod.json": pod} {
				writeTestFile(t, filepath.Join(directory, name), string(mustProviderRecoveryJSON(object)))
			}
			if mode == "already-ready" {
				writeTestFile(t, filepath.Join(directory, "unsealed"), "same-native-state")
			}
			python, err := exec.LookPath("python3")
			if err != nil {
				t.Fatal(err)
			}
			script := "#!" + python + "\n" + `import sys,json,os
from pathlib import Path
root=Path(os.environ['RTK_WORKLOAD_OPENBAO_TEST'])
args=sys.argv[4:]
with (root/'calls').open('a') as log: log.write(json.dumps(args)+'\n')
if args[0]=='get':
 if args[1]=='StatefulSet': print((root/'controller.json').read_text())
 elif args[1]=='Pod':
  pod=json.loads((root/'pod.json').read_text())
  if (root/'unsealed').exists(): pod['status']['conditions'][0]['status']='True'
  print(json.dumps(pod))
 else: sys.exit(2)
elif 'exec' in args:
 script=args[-1]
 if 'bao status -format=json' in script:
  print(json.dumps({'initialized':True,'sealed':not (root/'unsealed').exists()}))
 elif script=='bao write -format=json sys/unseal -':
  value=json.load(sys.stdin)
  assert value=={'key':os.environ['RTK_WORKLOAD_OPENBAO_SHARE']}
  (root/'unsealed').write_text('original-share-only')
  print('{"initialized":true,"sealed":false}')
 else: sys.exit(2)
else: sys.exit(2)
`
			writeTestFile(t, filepath.Join(directory, "kubectl"), script)
			if err := os.Chmod(filepath.Join(directory, "kubectl"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_WORKLOAD_OPENBAO_TEST", directory)
			t.Setenv("RTK_WORKLOAD_OPENBAO_SHARE", share)
			p := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: "dev", Stack: e.Journal.Stack, Operation: "create"}, Resources: []deploymentplan.Resource{{ID: deploymentplan.ID(want), UID: controllerUID, Action: "update", Desired: want}}}
			native := environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-selected-config"), RolloutTimeout: 3 * time.Second}
			compiled := &compiledEnvironmentDeployment{Plan: p, Store: store, Backend: native}
			backend := createNormalTailWorkloadBackend(cfg, store, &createNormalTailHandoff{Execution: e}, compiled)
			if backend.OpenBaoReplacementReady == nil || deploymentplan.Digest(compiled.Plan.Resources[0].Desired) != deploymentplan.Digest(want) || backend.RolloutTimeout != native.RolloutTimeout || backend.stagePlanSHA256 != p.SHA256() {
				t.Fatal("full workload lost its original target, runtime or controlled replacement callback")
			}
			ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
			defer cancel()
			if mode == "canceled" {
				cancel()
			}
			if mode == "wrong-revision" {
				changed := deploymentplan.Clone(pod)
				deploymentplan.Map(deploymentplan.Map(changed["metadata"])["labels"])["controller-revision-hash"] = "unreviewed-revision"
				err = backend.OpenBaoReplacementReady(ctx, controller, changed)
			} else {
				// Exercise the same native OnDelete wait used by the full workload;
				// volume capture is independent and is deliberately outside this test.
				err = backend.environmentKubernetesBackend.Wait(ctx, p.Resources[0])
			}
			if (err == nil) != (mode == "replacement" || mode == "already-ready") {
				t.Fatalf("full workload controlled replacement result: %v", err)
			}
			entry := createOpenBaoACKCompletionEntry(e.Journal, "openbao-managed-unseal-"+podUID)
			if mode == "replacement" {
				if entry == nil || entry.State != "completed" || entry.Phase != "openbao-unseal" {
					t.Fatal("same original parent lost its genuine native unseal receipt")
				}
			} else if entry != nil {
				t.Fatal("unchanged or rejected replacement published an unseal receipt")
			}
			raw, err := os.ReadFile(filepath.Join(directory, "calls"))
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			var calls [][]string
			for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
				if line == "" {
					continue
				}
				var call []string
				if json.Unmarshal([]byte(line), &call) != nil {
					t.Fatal("invalid isolated command log")
				}
				calls = append(calls, call)
			}
			unseals := 0
			for _, call := range calls {
				if strings.Contains(strings.Join(call, " "), "bao write -format=json sys/unseal -") {
					unseals++
				} else if len(call) > 0 && call[0] != "get" && !strings.Contains(strings.Join(call, " "), "bao status -format=json") {
					t.Fatal("full workload invoked an undeclared mutation")
				}
			}
			if unseals != map[bool]int{true: 1, false: 0}[mode == "replacement"] {
				t.Fatal("original share was replayed or used after rejected provenance", unseals)
			}
		})
	}
}
