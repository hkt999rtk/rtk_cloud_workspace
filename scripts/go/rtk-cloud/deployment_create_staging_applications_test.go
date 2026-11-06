package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestStagingApplicationRecoveryDoesNotRequirePreviousLogs(t *testing.T) {
	for _, mode := range []string{"logs-absent", "replacement", "changed-body", "unknown-write"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, operator, artifact := createStateFixture(t, "staging")
			e, err := startEnvironmentCreateExecution(t.Context(), cfg, artifact, operator, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = e.Close() })
			var parent environmentCreatePlan
			if err := json.Unmarshal(artifact.Plan, &parent); err != nil {
				t.Fatal(err)
			}
			ns := parent.Stack + "-video-cloud"
			objects := map[string]deploymentplan.Object{}
			var definitions []deploymentplan.Definition
			add := func(o deploymentplan.Object) {
				m := deploymentplan.Map(o["metadata"])
				m["namespace"] = ns
				definitions = append(definitions, deploymentplan.Definition{Object: o, Phase: "configuration"})
			}
			for _, owner := range environmentCreateApplicationOwners() {
				if owner.Role != "video-cloud-api" {
					continue
				}
				add(deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": owner.Name}, "spec": deploymentplan.Object{"replicas": float64(2), "template": deploymentplan.Object{"spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "app", "env": []any{deploymentplan.Object{"name": "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED", "value": "true"}}}}, "volumes": []any{deploymentplan.Object{"name": "identity", "persistentVolumeClaim": deploymentplan.Object{"claimName": owner.Claim}}}}}}})
			}
			add(deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": "video-cloud-workers-runtime"}, "data": deploymentplan.Object{"VIDEO_CLOUD_EMQX_API_KEY": "", "VIDEO_CLOUD_EMQX_API_SECRET": ""}})
			add(deploymentplan.Object{"apiVersion": "apps/v1", "kind": "Deployment", "metadata": deploymentplan.Object{"name": "mqtt-pki"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"metadata": deploymentplan.Object{}, "spec": deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": "pkibroker"}}}}}})
			for len(definitions) < 26 {
				add(deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": fmt.Sprintf("retained-%d", len(definitions))}, "data": deploymentplan.Object{"value": "original"}})
			}
			p, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: parent.Stack, Operation: "scoped", ConfigurationSHA256: parent.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: deploymentplan.Digest("test")}, definitions, nil)
			if err != nil {
				t.Fatal(err)
			}
			j := deploymentplan.Journal{PlanSHA256: p.SHA256(), Owners: map[string]string{}, Failed: "Deployment/" + ns + "/video-cloud-api"}
			for i, r := range p.Resources {
				uid := fmt.Sprintf("%08d-1111-4111-8111-111111111111", i+1)
				o := deploymentplan.Clone(r.Desired)
				deploymentplan.Map(o["metadata"])["uid"] = uid
				deploymentplan.Map(o["metadata"])["resourceVersion"] = "1"
				objects[r.ID] = o
				j.Completed = append(j.Completed, r.ID)
				j.Owners[r.ID] = uid
			}
			if mode == "replacement" {
				deploymentplan.Map(objects[j.Failed]["metadata"])["uid"] = "22222222-2222-4222-8222-222222222222"
			}
			if mode == "changed-body" {
				deploymentplan.Map(objects[j.Failed]["spec"])["replicas"] = float64(7)
			}
			if mode == "unknown-write" {
				j.InFlight = j.Failed
			}
			if err := saveEnvironmentCreateStageInput(t.Context(), store, e, stagingApplicationsStage, p); err != nil {
				t.Fatal(err)
			}
			intent, err := environmentCreateKubernetesStageIntent(p)
			if err != nil {
				t.Fatal(err)
			}
			if err := e.BeginEntry("kubernetes/"+stagingApplicationsStage, stagingApplicationsStage, intent, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			if err := e.RecordKubernetesLease(deploymentplan.LeaseOwner{ResourceID: "Lease/" + ns + "/rtk-deployment-execution", UID: "11111111-1111-4111-8111-111111111111", Holder: p.SHA256() + ":test"}); err != nil {
				t.Fatal(err)
			}
			save := func(path string, value any) {
				t.Helper()
				raw, err := json.Marshal(value)
				if err != nil {
					t.Fatal(err)
				}
				if err := store.write(path, raw, false); err != nil {
					t.Fatal(err)
				}
			}
			save("deployment/rollback/"+p.SHA256()+"/plan.json", p)
			save("deployment/rollback/"+p.SHA256()+"/journal.json", j)
			cfg.Storage.RuntimeMedia.Bucket = "isolated-media"
			cfg.Storage.RuntimeMedia.Region = "us-east"
			cfg.Storage.RuntimeMedia.Prefix = "staging/"
			o := environmentOTAStorageObservation{Version: 1, Environment: "staging", Provider: "linode", Bucket: "isolated-media", Region: "us-east", Prefix: "staging/", Endpoint: "https://objects.example.test"}
			save(environmentMediaObservationPath, environmentOTAStorageRecord{Observation: o, SHA256: deploymentplan.Digest(o), ObservedAt: time.Now().UTC().Format(time.RFC3339)})
			for key, value := range map[string]string{"emqx-handoff-api-key": "fixture-key", "emqx-handoff-api-secret": "fixture-secret"} {
				if err := store.write("runtime/"+lkeSecretFileName(key), []byte(value), false); err != nil {
					t.Fatal(err)
				}
			}
			objects["Secret/"+ns+"/mqtt-pki-runtime"] = deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": "mqtt-pki-runtime", "namespace": ns}, "data": deploymentplan.Object{"api-keys.conf": base64.StdEncoding.EncodeToString([]byte("fixture-key:fixture-secret:administrator\n"))}}
			dir := t.TempDir()
			raw, _ := json.Marshal(objects)
			writeTestFile(t, filepath.Join(dir, "objects.json"), string(raw))
			python, err := exec.LookPath("python3")
			if err != nil {
				t.Fatal(err)
			}
			writeTestFile(t, filepath.Join(dir, "kubectl"), "#!"+python+"\n"+`import json,os,sys
from pathlib import Path
root=Path(os.environ['RTK_APPLICATION_RECOVERY_TEST'])
a=sys.argv[1:]
with (root/'calls').open('a') as f: f.write(' '.join(a)+'\n')
if 'get' not in a: sys.exit(1)
i=a.index('get'); ns=a[a.index('--namespace')+1] if '--namespace' in a else a[a.index('-n')+1]
obj=json.loads((root/'objects.json').read_text()).get(a[i+1]+'/'+ns+'/'+a[i+2])
if obj is None: sys.exit(1)
print(json.dumps(obj))
`)
			if err := os.Chmod(filepath.Join(dir, "kubectl"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_APPLICATION_RECOVERY_TEST", dir)
			b := environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-fixture")}
			f, err := qualifyStagingApplicationsForward(t.Context(), cfg, store, &parent, e, environmentCreateBootstrapRuntime{Execution: e, Backend: b})
			if mode != "logs-absent" {
				if err == nil {
					t.Fatal("unverified recovery was allowed")
				}
				return
			}
			if err != nil {
				calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
				t.Fatal("durable checkpoint must qualify without previous Pod logs", err, string(calls))
			}
			calls, _ := os.ReadFile(filepath.Join(dir, "calls"))
			if strings.Contains(string(calls), " logs ") {
				t.Fatal("recovery queried transient Pod logs")
			}
			changed := 0
			for _, d := range f.Definitions {
				if !d.Retain {
					changed++
				}
			}
			if changed != 5 {
				t.Fatalf("changed %d resources, want original five", changed)
			}
			originalJournalPath := filepath.Join(store.Root, "deployment/rollback", p.SHA256(), "journal.json")
			before, err := os.ReadFile(originalJournalPath)
			if err != nil {
				t.Fatal(err)
			}
			memory := freshStageBackend{&normalTailE2EBackend{e: e, objects: objects, writes: map[string]int{}}}
			apply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
				live := make([]deploymentplan.Object, 0, len(memory.objects))
				for _, object := range memory.objects {
					live = append(live, object)
				}
				child, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: parent.Stack, Operation: "scoped", ConfigurationSHA256: parent.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(parent.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs)}, defs, live, retainedCreateWorkloadAdoptions(defs))
				if err != nil {
					return err
				}
				check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
				return executeEnvironmentCreateStagePlan(ctx, store, e, stage, child, memory, check, check, check, func(context.Context) error { return nil }, io.Discard)
			}
			if err := executeStagingApplicationsForward(t.Context(), environmentCreateBootstrapRuntime{Execution: e}, f, apply); err != nil {
				t.Fatal(err)
			}
			if len(memory.writes) != 5 {
				t.Fatalf("wrote %d owners, want the five qualified repairs", len(memory.writes))
			}
			if err := verifyStagingAcceptedApplications(store, e); err != nil {
				t.Fatal("accepted forward history rejected", err)
			}
			after, err := os.ReadFile(originalJournalPath)
			if err != nil || string(before) != string(after) {
				t.Fatal("original failed journal changed", err)
			}
			// A parent receipt pointing elsewhere cannot stand in for the
			// actual accepted child, even after all original writes succeeded.
			entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stagingApplicationsStage)
			entry.Receipt = []byte(`{"actual_qualified_forward_stage":"kubernetes/foreign","original_failed_plan_sha256":"foreign"}`)
			if err := verifyStagingAcceptedApplications(store, e); err == nil {
				t.Fatal("foreign completion receipt accepted")
			}

		})
	}
}
