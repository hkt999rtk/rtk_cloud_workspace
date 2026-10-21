package main

import (
	"context"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This is the active executor's native lifecycle boundary, not its public
// admission or all-CA completion. The actual initial renewal establishes the
// signing-key custody and ten projections. The active attempt must preserve
// them through reload/readiness and stop at the absent Service CRL proof.
type activeTransportLifecycleContract struct {
	initial  *initialTransportExecutionContract
	artifact activeTransportRenewalArtifact
	current  map[string]environmentCreateTransport
	state    string
}

type activeTransportLifecycleState struct {
	Mode               string                `json:"mode"`
	Reloads            int                   `json:"reloads"`
	Rollouts           []string              `json:"rollouts"`
	OriginalProjection deploymentplan.Object `json:"original_projection"`
	ExpectedOwners     map[string]string     `json:"expected_owners"`
	CAHash             string                `json:"ca_hash"`
}

func newActiveTransportLifecycleContract(t *testing.T) *activeTransportLifecycleContract {
	t.Helper()
	f := newInitialTransportExecutionContract(t, "complete")
	f.backend.RolloutTimeout = 300 * time.Second
	initialReview := deploymentplan.Digest("actual initial renewal before active lifecycle")
	if err := renewEnvironmentInitialTransports(t.Context(), f.cfg, f.store, f.e, f.backend, f.plan, f.journal, initialReview); err != nil {
		t.Fatal("actual first renewal", err)
	}
	old, proof, err := readLegacyInitialTransportRenewal(f.store, f.e.Journal.PlanSHA256)
	if err != nil || proof == nil || !proof.Completed {
		t.Fatal("actual initial signing custody unavailable", err)
	}
	current := map[string]environmentCreateTransport{}
	material, keys := map[string]string{}, map[string]string{}
	for path, digest := range proof.Material {
		material[path] = digest
	}
	for _, role := range []string{"management", "openbao"} {
		path := filepath.Join("deployment/create/transport-renewal", initialReview, role+"-transport/ca.key")
		key, err := readBoundedEnvironmentFile(filepath.Join(f.store.Root, path), true)
		if err != nil {
			t.Fatal(err)
		}
		next, err := renewActiveTransportSameKey(old[role], key, time.Now().UTC())
		if err != nil {
			t.Fatal("actual same-key candidate", role, err)
		}
		current[role], keys[role] = next, path
	}
	var native initialTransportNativeState
	if err := readExecutionJSON(f.state, &native); err != nil {
		t.Fatal(err)
	}
	ns, baoNS := f.e.Journal.Stack+"-video-cloud", f.e.Journal.Stack+"-secrets"
	image := "docker.io/openbao/openbao@sha256:" + deploymentplan.Digest("original retained image")
	containers := []any{deploymentplan.Object{"name": "openbao", "image": image}}
	set := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "StatefulSet", "metadata": deploymentplan.Object{"name": "openbao", "namespace": baoNS, "uid": "stateful-owner", "resourceVersion": "1", "labels": deploymentplan.Object{"rtk.realtek.com/stack": f.e.Journal.Stack}}, "spec": deploymentplan.Object{"replicas": float64(1), "serviceName": "openbao", "selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app": "openbao"}}, "template": deploymentplan.Object{"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app": "openbao"}}, "spec": deploymentplan.Object{"containers": containers}}}}
	pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"name": "openbao-0", "namespace": baoNS, "uid": "pod-owner", "resourceVersion": "1", "ownerReferences": []any{deploymentplan.Object{"kind": "StatefulSet", "name": "openbao", "uid": "stateful-owner", "controller": true}}}, "spec": deploymentplan.Object{"containers": containers}}
	native.Objects[deploymentplan.ID(set)], native.Objects[deploymentplan.ID(pod)] = set, deploymentplan.Clone(pod)
	_, _, managedRoot := coreContractCertificate(t, "isolated managed consumer authority", true, nil, nil, 4*time.Hour)
	defs := []deploymentplan.Definition{{Object: deploymentplan.Projection(set), Phase: "workload", Retain: true}, {Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap", "metadata": deploymentplan.Object{"name": "pki-create-openbao-tls-consumer-trust", "namespace": ns}, "data": deploymentplan.Object{"roots.pem": managedRoot + "\n" + old["openbao"].CACert}}, Phase: "configuration"}}
	for _, name := range []string{"pki-controller", "certissuer"} {
		deployment := deploymentplan.Object{
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": deploymentplan.Object{"name": name, "namespace": ns},
			"spec": deploymentplan.Object{
				"replicas": float64(1),
				"selector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app": name}},
				"template": deploymentplan.Object{
					"metadata": deploymentplan.Object{"labels": deploymentplan.Object{"app": name}},
					"spec":     deploymentplan.Object{"containers": []any{deploymentplan.Object{"name": name, "image": "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:" + deploymentplan.Digest(name)}}},
				},
			},
		}
		defs = append(defs, deploymentplan.Definition{Phase: "workload", Object: deployment})
	}
	stage := "foundation-recovery-" + deploymentplan.Digest(defs)
	inventory := make([]deploymentplan.Object, 0, len(native.Objects))
	for _, object := range native.Objects {
		inventory = append(inventory, object)
	}
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: f.cfg.Environment, Stack: f.e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(f.cfg), ReleaseSHA256: deploymentplan.Digest("retained original release"), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs)}, defs, inventory)
	if err != nil {
		t.Fatal(err)
	}
	memory := freshStageBackend{&normalTailE2EBackend{e: f.e, objects: native.Objects, writes: map[string]int{}, leases: len(f.e.Journal.KubernetesLeases)}}
	if err := executeEnvironmentCreateStagePlan(t.Context(), f.store, f.e, stage, plan, memory, func(ctx context.Context, p *deploymentplan.Plan) error {
		return p.AdmitResumable(ctx, memory, f.store.Root)
	}, func(ctx context.Context, p *deploymentplan.Plan) error {
		return memory.CheckParentExecution(ctx, p, f.store.Root)
	}, func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, memory) }, func(ctx context.Context) error { return f.e.Check(ctx) }, io.Discard); err != nil {
		t.Fatal("actual retained owner stage", err)
	}
	native.Objects = memory.objects
	if err := os.WriteFile(f.state, mustProviderRecoveryJSON(native), 0600); err != nil {
		t.Fatal(err)
	}
	resources, err := activeTransportOwners(t.Context(), f.cfg, f.store, f.e, f.backend, old, current)
	if err != nil || len(resources) != 13 {
		t.Fatal("actual13 original owners", len(resources), err)
	}
	podUID, setUID, err := activeTransportOriginalIdentity(t.Context(), f.cfg, f.store, f.e, f.backend)
	if err != nil {
		t.Fatal("original initialized identity", err)
	}
	artifact := activeTransportRenewalArtifact{Schema: activeTransportRenewalSchema + "/artifact", Plan: activeTransportRenewalPlan{Schema: activeTransportRenewalSchema, Parent: f.e.Journal.PlanSHA256, OwnerSHA: deploymentplan.Digest(f.e.owner), JournalSHA: deploymentplan.Digest(f.e.Journal), OriginalProofSHA: deploymentplan.Digest(proof), ConfigurationSHA: deploymentplan.Digest(f.cfg), Material: material, AuthorityKeys: keys, Resources: resources, PodUID: podUID, SetUID: setUID}}
	state := filepath.Join(t.TempDir(), "active-lifecycle.json")
	owners := map[string]string{}
	for _, resource := range resources {
		owners[resource.ID] = resource.UID
	}
	value := activeTransportLifecycleState{Mode: "complete-native", Rollouts: []string{}, OriginalProjection: deploymentplan.Clone(native.Objects["Secret/"+baoNS+"/openbao-transport-tls"]), ExpectedOwners: owners, CAHash: environmentPreflightBytesSHA256([]byte(current["openbao"].CACert))}
	if err := os.WriteFile(state, mustProviderRecoveryJSON(value), 0600); err != nil {
		t.Fatal(err)
	}
	parent := os.Getenv("RTK_CLOUD_KUBECTL")
	script := `#!/usr/bin/env python3
import base64,hashlib,json,os,sys
a=sys.argv[1:]
with open(__STATE__) as source:d=json.load(source)
with open(os.environ['RTK_INITIAL_RENEWAL_NATIVE']) as source:n=json.load(source)
def save():
 with open(__STATE__,'w') as out:json.dump(d,out)
def native():
 with open(os.environ['RTK_INITIAL_RENEWAL_NATIVE'],'w') as out:json.dump(n,out)
if 'kill -HUP 1' in a[-1]:
 assert a[a.index('--namespace')+1]==__BAONS__ and a[a.index('exec')+2]=='openbao-0' and a[a.index('--container')+1]=='openbao'
 pod=n['objects']['Pod/'+__BAONS__+'/openbao-0'];assert pod['metadata']['uid']=='pod-owner'
 actual=n['objects']['Secret/'+__BAONS__+'/openbao-transport-tls'];assert actual['metadata']['uid']==d['expected_owners']['Secret/'+__BAONS__+'/openbao-transport-tls']
 projected=d['original_projection'] if d['mode']=='stale-projection' else actual
 for field,path in [('tls.crt','/openbao/transport/tls.crt'),('ca.crt','/openbao/transport/ca.crt'),('tls.key','/openbao/transport/tls.key')]:
  digest=hashlib.sha256(base64.b64decode(projected['data'][field])).hexdigest()
  if path not in a[-1] or digest not in a[-1]:sys.exit('actual projected original transport bytes not installed')
 d['reloads']+=1;save()
 if d['mode']=='foreign-original-pod':pod['metadata']['ownerReferences'][0]['uid']='foreign-statefulset';native()
 sys.exit(0)
if 'rollout' in a:
 i=a.index('rollout');assert a[i:i+2]==['rollout','status'] and a[-1]=='--timeout=300s'
 ns=a[a.index('--namespace')+1];kind,name=a[i+2].split('/');key=kind+'/'+ns+'/'+name
 assert kind=='Deployment' and name in ('pki-controller','certissuer') and ns==__NS__
 obj=n['objects'][key];assert obj['metadata']['uid']==d['expected_owners'][key] and obj['spec']['replicas']==1
 assert obj['spec']['template']['metadata']['annotations']['rtk.realtek.com/temporary-openbao-ca-sha256']==d['ca_hash']
 if d['mode']=='rollout-unready':sys.exit('original consumer has no available updated replica')
 obj['status']={'observedGeneration':obj['metadata'].get('generation',1),'updatedReplicas':1,'availableReplicas':1,'replicas':1}
 d['rollouts'].append(key);save()
 if d['mode']=='replacement-after-rollout':obj['metadata']['uid']='replacement-owner'
 native();sys.exit(0)
os.execv(__PARENT__,[__PARENT__]+a)
`
	for token, value := range map[string]string{"__STATE__": state, "__PARENT__": parent, "__BAONS__": baoNS, "__NS__": ns} {
		script = strings.ReplaceAll(script, token, strconv.Quote(value))
	}
	dir := t.TempDir()
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	return &activeTransportLifecycleContract{initial: f, artifact: artifact, current: current, state: state}
}

func TestActiveTransportLifecycleRequiresCurrentNativeProofAfterSameKeyReload(t *testing.T) {
	f := newActiveTransportLifecycleContract(t)
	initial := f.initial
	before := createBootstrapStateSnapshot(t, initial.store.Root)
	var native initialTransportNativeState
	if err := readExecutionJSON(initial.state, &native); err != nil {
		t.Fatal(err)
	}
	baselineWrites := native.Writes
	for _, mode := range []string{"stale-projection", "rollout-unready", "replacement-after-rollout", "foreign-original-pod", "complete-native"} {
		t.Run(mode, func(t *testing.T) {
			var state activeTransportLifecycleState
			if err := readExecutionJSON(f.state, &state); err != nil {
				t.Fatal(err)
			}
			state.Mode = mode
			state.Reloads = 0
			state.Rollouts = []string{}
			if err := os.WriteFile(f.state, mustProviderRecoveryJSON(state), 0600); err != nil {
				t.Fatal(err)
			}
			native = initialTransportNativeState{}
			if err := readExecutionJSON(initial.state, &native); err != nil {
				t.Fatal(err)
			}
			// Restore only modeled external drift from a prior rejected attempt;
			// immutable receipts and already-written renewed bodies stay untouched.
			for _, resource := range f.artifact.Plan.Resources {
				deploymentplan.Map(native.Objects[resource.ID]["metadata"])["uid"] = resource.UID
			}
			pod := native.Objects["Pod/"+initial.e.Journal.Stack+"-secrets/openbao-0"]
			deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["metadata"])["ownerReferences"])[0])["uid"] = "stateful-owner"
			if err := os.WriteFile(initial.state, mustProviderRecoveryJSON(native), 0600); err != nil {
				t.Fatal(err)
			}
			err := executeActiveTransportRenewal(t.Context(), initial.cfg, initial.store, initial.e, initial.backend, f.artifact, f.current, io.Discard)
			if err == nil {
				t.Fatal("missing current Service CRL proof admitted active completion")
			}
			state = activeTransportLifecycleState{}
			if readErr := readExecutionJSON(f.state, &state); readErr != nil {
				t.Fatal(readErr)
			}
			wantReloads, wantRollouts := 1, 0
			switch mode {
			case "stale-projection":
				wantReloads = 0
			case "replacement-after-rollout":
				wantRollouts = 1
			case "complete-native":
				wantRollouts = 2
			}
			if state.Reloads != wantReloads || len(state.Rollouts) != wantRollouts {
				t.Fatal("unexpected native lifecycle stop", mode, state.Reloads, len(state.Rollouts), err)
			}
			if mode == "complete-native" && !strings.Contains(err.Error(), "completed original Service Root CRL renewal") {
				t.Fatal("did not reach current Service CRL proof boundary", err)
			}
			native = initialTransportNativeState{}
			if readErr := readExecutionJSON(initial.state, &native); readErr != nil {
				t.Fatal(readErr)
			}
			if native.Writes != baselineWrites+13 || native.Leases != native.Releases {
				t.Fatal("retry rewrote accepted native owners or leaked lease", native.Writes, native.Leases, native.Releases)
			}
			for _, resource := range f.artifact.Plan.Resources {
				body := native.Objects[resource.ID]
				if resource.Desired["kind"] == "Secret" && deploymentplan.Map(body["data"])["tls.key"] != deploymentplan.Map(resource.Before["data"])["tls.key"] {
					t.Fatal("original serving key changed", resource.ID)
				}
			}
			after := createBootstrapStateSnapshot(t, initial.store.Root)
			for path, digest := range before {
				if strings.HasPrefix(path, "openbao/") || strings.Contains(path, "-transport/") {
					if after[path] != digest {
						t.Fatal("original private custody changed", path)
					}
				}
			}
			review := deploymentplan.Digest(f.artifact.Plan)
			entry := createOpenBaoACKCompletionEntry(initial.e.Journal, "active-transport-renewal-"+review)
			if entry == nil || entry.State != "pending" || len(entry.Receipt) != 0 {
				t.Fatal("failed current native proof completed parent")
			}
			if _, err := os.Stat(filepath.Join(initial.store.Root, "deployment/create/transport-renewal", review, "completed.json")); !os.IsNotExist(err) {
				t.Fatal("unproven active completion published", err)
			}
		})
	}
	// Independently check the protocol cannot accept a reload without the exact
	// key projection, even when its two public certificate hashes are correct.
	leaf := f.current["openbao"].Identities["openbao"]
	_, err := environmentCreateOpenBaoExec(t.Context(), initial.cfg, initial.backend, nil, "sha256sum /openbao/transport/tls.crt /openbao/transport/ca.crt; "+environmentPreflightBytesSHA256([]byte(leaf.Certificate))+" "+environmentPreflightBytesSHA256([]byte(f.current["openbao"].CACert))+"; kill -HUP 1")
	if err == nil {
		t.Fatal("native reload accepted omitted original serving-key projection")
	}
	if base64.StdEncoding.EncodeToString([]byte(leaf.Key)) != deploymentplan.Map(native.Objects["Secret/"+initial.e.Journal.Stack+"-secrets/openbao-transport-tls"]["data"])["tls.key"] {
		t.Fatal("renewed original key no longer installed")
	}
}
