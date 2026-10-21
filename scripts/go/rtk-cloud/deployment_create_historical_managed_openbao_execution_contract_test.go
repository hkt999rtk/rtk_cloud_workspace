package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Simulate the external StatefulSet controller only: retained controller/claims,
// actual template revision, and a new native Pod UID after the host template changes.
func historicalManagedOpenBaoPod(t *testing.T, f *acceptedCoreExecutionContract, ready bool) deploymentplan.Object {
	t.Helper()
	ns := f.p.Plan.Stack + "-secrets"
	set := f.native.objects["StatefulSet/"+ns+"/openbao"]
	meta := deploymentplan.Map(set["metadata"])
	template := deploymentplan.Map(deploymentplan.Map(set["spec"])["template"])
	revision := "openbao-" + deploymentplan.Digest(template)[:12]
	meta["generation"] = float64(f.native.writes[deploymentplan.ID(set)])
	set["status"] = deploymentplan.Object{"observedGeneration": meta["generation"], "updateRevision": revision, "currentRevision": revision, "replicas": float64(1), "currentReplicas": float64(1), "updatedReplicas": float64(1), "readyReplicas": float64(0)}
	pod := deploymentplan.Clone(template)
	pod["apiVersion"], pod["kind"] = "v1", "Pod"
	pm := deploymentplan.Map(pod["metadata"])
	pm["name"], pm["namespace"], pm["uid"] = "openbao-0", ns, coreContractObserverUUID(deploymentplan.String(meta["uid"]), "Pod", template, 0)
	pm["resourceVersion"] = "1"
	pm["ownerReferences"] = []any{deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "name": "openbao", "uid": meta["uid"], "controller": true}}
	labels := deploymentplan.Map(pm["labels"])
	if labels == nil {
		labels = deploymentplan.Object{}
		pm["labels"] = labels
	}
	labels["controller-revision-hash"] = revision
	spec := deploymentplan.Map(pod["spec"])
	volumes := certIssuerObjectList(spec["volumes"])
	for _, value := range certIssuerObjectList(deploymentplan.Map(set["spec"])["volumeClaimTemplates"]) {
		name := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(value)["metadata"])["name"])
		volumes = append(volumes, environmentCreateServiceVolume(name, "persistentVolumeClaim", name+"-openbao-0"))
	}
	spec["volumes"] = volumes
	pod["status"] = unstartedManagedOpenBaoPodFixture()["status"]
	if ready {
		pod["status"] = deploymentplan.Object{"phase": "Running", "conditions": []any{deploymentplan.Object{"type": "Ready", "status": "True"}}}
		deploymentplan.Map(set["status"])["readyReplicas"] = float64(1)
	}
	if !managedOpenBaoPodMatches(set, pod) || validateOpenBaoPodOwner(set, pod) != nil {
		t.Fatal("native replacement does not preserve actual StatefulSet template and claims")
	}
	f.native.objects[deploymentplan.ID(pod)] = pod
	f.protocol["objects"] = historicalNativeInventory(f.native.objects)
	f.publish(t)
	return pod
}

func installHistoricalManagedOpenBaoProtocol(t *testing.T, f *acceptedCoreExecutionContract) {
	t.Helper()
	f.protocol["historical_bao_process"] = deploymentplan.Object{"pid": "54", "start_time": "123456", "executable_sha256": deploymentplan.Digest("isolated-original-bao-executable"), "executable": "/bin/bao", "uid": "100", "pid_marker_sha256": environmentPreflightBytesSHA256([]byte("$\n"))}
	f.publish(t)
	var native map[string]any
	statePath := os.Getenv("RTK_HISTORICAL_FOUNDATION_STATE")
	if err := readExecutionJSON(statePath, &native); err != nil {
		t.Fatal(err)
	}
	native["sealed"], native["audit"], native["signals"] = true, false, float64(0)
	if err := os.WriteFile(statePath, mustProviderRecoveryJSON(native), 0600); err != nil {
		t.Fatal(err)
	}
	parent := os.Getenv("RTK_CLOUD_KUBECTL")
	dir := t.TempDir()
	script := `#!/usr/bin/env python3
import json,os,sys
with open(os.environ['RTK_CORE_CONTRACT_NATIVE']) as source:d=json.load(source)
a=sys.argv[1:];joined=' '.join(a)
path=os.environ['RTK_HISTORICAL_FOUNDATION_STATE']
with open(path) as source:s=json.load(source)
def save():
 with open(path,'w') as target:json.dump(s,target)
if 'bao status -format=json' in joined or 'bao write -format=json sys/unseal -' in joined:
 os.execv(os.environ['RTK_HISTORICAL_FOUNDATION_NATIVE'],[os.environ['RTK_HISTORICAL_FOUNDATION_NATIVE']]+a)
elif 'bao audit list -format=json' in joined:
 if s['sealed'] or sys.stdin.readline().strip()!=s['token']:sys.exit('wrong retained OpenBao authentication')
 print(json.dumps({'deployment-audit/':{'type':'file','options':{'file_path':'/openbao/audit/audit.jsonl','log_raw':'false'}}} if s['audit'] else {}))
elif 'for item in /proc/[0-9]*/comm' in joined:
 if s['sealed']:sys.exit('native provider not running')
 print(json.dumps(d['historical_bao_process']))
elif 'kill -HUP ' in joined:
 p=d['historical_bao_process']
 if s['sealed'] or 'kill -HUP '+p['pid'] not in joined or p['start_time'] not in joined or p['executable_sha256'] not in joined:sys.exit('native provider changed before signal')
 s['signals']+=1;s['audit']=True;save()
elif len(a)>=2 and a[-2:]==['cat','/openbao/config/file.hcl']:
 ns=a[a.index('--namespace')+1]
 sys.stdout.write(d['objects']['ConfigMap/'+ns+'/openbao-file']['data']['file.hcl'])
else:os.execv(os.environ['RTK_HISTORICAL_MANAGED_BAO_PARENT'],[os.environ['RTK_HISTORICAL_MANAGED_BAO_PARENT']]+a)
`
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_HISTORICAL_MANAGED_BAO_PARENT", parent)
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func advanceHistoricalManagedOpenBaoPull(t *testing.T, f *acceptedCoreExecutionContract, r *createServiceSessionRecovery) {
	t.Helper()
	partial := r.ManagedTransportRecovery.Partial
	historicalManagedOpenBaoPod(t, f, false)
	installHistoricalManagedOpenBaoProtocol(t, f)
	_, retained, err := verifyManagedTransportOpenBaoIdentity(t.Context(), f.cfg, f.store, f.e, f.backend, r, false)
	if err != nil {
		t.Fatal("original native image-pull interruption", err)
	}
	var defs []deploymentplan.Definition
	for _, resource := range partial.PriorForward.Plan.Resources {
		body := deploymentplan.Clone(resource.Desired)
		if body["kind"] == "StatefulSet" {
			environmentPKIPod(body)["imagePullSecrets"] = []any{deploymentplan.Object{"name": lkeImagePullSecretName(f.cfg.Values)}}
		}
		defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: body, Dependencies: resource.Dependencies})
	}
	candidate := historicalTransportHostPreview(t, f, defs)
	if err := validateManagedTransportPartialForwardDefinitions(partial, defs); err != nil {
		t.Fatal(err)
	}
	materials := map[string]string{"accepted-service-core-managed-partial-original-plan": partial.Original.SHA256(), "accepted-service-core-managed-partial-forward-plan": candidate.SHA256(), "accepted-service-core-managed-partial-openbao-retained-prerequisites": deploymentplan.Digest(retained)}
	review, _ := f.admitRecovery(t, "rtk-cloud.create-managed-transports-partial-recovery/v1", materials)
	f.write(t, filepath.Join("deployment/create/openbao-recovery", review, "openbao-root-trust-plan.json"), candidate)
	r.Review, r.Candidate = review, candidate
	runtime := f.runtime
	runtime.ServiceSessionRecovery = r
	err = executeManagedTransportPartialForward(t.Context(), f.cfg, runtime, defs, func(ctx context.Context, stage string, actual []deploymentplan.Definition) error {
		f.acceptedHistoricalStage(t, stage, actual)
		pod := historicalManagedOpenBaoPod(t, f, true)
		return unsealEnvironmentCreateOpenBaoRestart(ctx, f.cfg, f.store, runtime, pod)
	})
	if err == nil || !strings.Contains(err.Error(), "native redacted audit device differs") {
		t.Fatal("pull repair must preserve accepted child and original-share unseal at missing native audit", err)
	}
	_, _, accepted, err := loadManagedTransportAcceptedForward(f.store, f.e)
	if err != nil || accepted == nil {
		t.Fatal("actual accepted pull/unseal source", err)
	}
	partial.AcceptedForward = accepted
	f.runtime = runtime
}

func advanceHistoricalManagedOpenBaoAudit(t *testing.T, f *acceptedCoreExecutionContract, r *createServiceSessionRecovery) {
	t.Helper()
	partial := r.ManagedTransportRecovery.Partial
	_, retained, err := verifyManagedTransportOpenBaoIdentity(t.Context(), f.cfg, f.store, f.e, f.backend, r, false)
	if err != nil {
		t.Fatal("actual accepted missing audit source", err)
	}
	var defs []deploymentplan.Definition
	for _, resource := range partial.AcceptedForward.Plan.Resources {
		body := deploymentplan.Clone(resource.Desired)
		if body["kind"] == "ConfigMap" && deploymentplan.Map(body["metadata"])["name"] == "openbao-file" {
			data := deploymentplan.Map(body["data"])
			data["file.hcl"] = deploymentplan.String(data["file.hcl"]) + managedTransportOriginalAuditBlock
		}
		defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: body, Dependencies: resource.Dependencies})
	}
	candidate := historicalTransportHostPreview(t, f, defs)
	if err := validateManagedTransportAcceptedAuditDefinitions(partial, defs); err != nil {
		t.Fatal(err)
	}
	materials := map[string]string{"accepted-service-core-managed-partial-original-plan": partial.Original.SHA256(), "accepted-service-core-managed-partial-forward-plan": candidate.SHA256(), "accepted-service-core-managed-partial-openbao-retained-prerequisites": deploymentplan.Digest(retained)}
	review, _ := f.admitRecovery(t, "rtk-cloud.create-managed-transports-partial-recovery/v1", materials)
	f.write(t, filepath.Join("deployment/create/openbao-recovery", review, "openbao-root-trust-plan.json"), candidate)
	r.Review, r.Candidate = review, candidate
	runtime := f.runtime
	runtime.ServiceSessionRecovery = r
	if err := executeManagedTransportPartialForward(t.Context(), f.cfg, runtime, defs, func(_ context.Context, stage string, actual []deploymentplan.Definition) error {
		f.acceptedHistoricalStage(t, stage, actual)
		return nil
	}); err != nil {
		t.Fatal("actual audited native host continuation", err)
	}
	f.runtime = runtime
}

func TestHistoricalManagedOpenBaoRestartUsesOnlyOriginalShareAndNativePod(t *testing.T) {
	f, roots, intermediates, _ := newHistoricalTransportCoordinatorExecutionContract(t)
	defs, err := renderEnvironmentCreateTransportDefinitions(t.Context(), f.cfg, f.p.Plan.Bootstrap, f.runtime, f.service, roots, intermediates, f.p.Plan.Release)
	if err != nil {
		t.Fatal(err)
	}
	f.acceptedHistoricalStage(t, "isolated-native-managed-host-installation", historicalTransportHostDefinitions(defs, 2))
	pod := historicalManagedOpenBaoPod(t, f, true)
	installHistoricalManagedOpenBaoProtocol(t, f)
	protected := map[string][]byte{}
	for _, name := range []string{"openbao/init.json", "openbao/root-token", "openbao/unseal-key"} {
		raw, err := os.ReadFile(filepath.Join(f.store.Root, name))
		if err != nil {
			t.Fatal(err)
		}
		protected[name] = raw
	}
	if err := unsealEnvironmentCreateOpenBaoRestart(t.Context(), f.cfg, f.store, f.runtime, pod); err != nil {
		t.Fatal("original-share native restart", err)
	}
	key := "openbao-managed-unseal-" + deploymentplan.String(deploymentplan.Map(pod["metadata"])["uid"])
	entry := createOpenBaoACKCompletionEntry(f.e.Journal, key)
	if entry == nil || entry.State != "completed" || entry.Phase != "openbao-unseal" {
		t.Fatal("actual original-share unseal receipt missing")
	}
	before := deploymentplan.Digest(f.e.Journal)
	if err := unsealEnvironmentCreateOpenBaoRestart(t.Context(), f.cfg, f.store, f.runtime, pod); err != nil || deploymentplan.Digest(f.e.Journal) != before {
		t.Fatal("already-unsealed retry rewrote native history", err)
	}
	for name, raw := range protected {
		actual, err := os.ReadFile(filepath.Join(f.store.Root, name))
		if err != nil || string(actual) != string(raw) {
			t.Fatal("restart replaced original private initialization", name, err)
		}
	}
	for _, name := range []string{"openbao/root-token", "openbao/unseal-key"} {
		path := filepath.Join(f.store.Root, name)
		if err := os.WriteFile(path, []byte("foreign-retained-material"), 0600); err != nil {
			t.Fatal(err)
		}
		err := unsealEnvironmentCreateOpenBaoRestart(t.Context(), f.cfg, f.store, f.runtime, pod)
		if writeErr := os.WriteFile(path, protected[name], 0600); writeErr != nil {
			t.Fatal(writeErr)
		}
		if err == nil || deploymentplan.Digest(f.e.Journal) != before {
			t.Fatal("foreign original custody authorized another unseal", name, err)
		}
	}
	changed := deploymentplan.Clone(pod)
	deploymentplan.Map(changed["metadata"])["uid"] = "99999999-9999-4999-8999-999999999999"
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if err := unsealEnvironmentCreateOpenBaoRestart(ctx, f.cfg, f.store, f.runtime, changed); err == nil {
		t.Fatal("foreign native Pod authorized original-share use")
	}
}
