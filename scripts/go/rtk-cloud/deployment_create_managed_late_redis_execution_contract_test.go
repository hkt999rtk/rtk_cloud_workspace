package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// The subprocess is only an external Kubernetes/Redis protocol transport. All
// source qualification, native checks, child execution and receipts remain real.
func TestManagedLateRedisExternalProtocolHelper(t *testing.T) {
	endpoint := os.Getenv("RTK_TEST_REDIS_PROTOCOL")
	if endpoint == "" {
		return
	}
	i := slices.Index(os.Args, "--")
	if i < 0 {
		os.Exit(81)
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(82)
	}
	raw, _ := json.Marshal(redisExecutionRequest{Args: os.Args[i+1:], Input: input})
	resp, err := http.Post(endpoint, "application/json", bytes.NewReader(raw))
	if err != nil {
		os.Exit(83)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != http.StatusOK {
		_, _ = os.Stderr.Write(body)
		os.Exit(84)
	}
	_, _ = os.Stdout.Write(body)
	os.Exit(0)
}

type redisExecutionRequest struct {
	Args  []string
	Input []byte
}
type redisExecutionContract struct {
	t                        *testing.T
	store                    secretStore
	e                        *environmentCreateExecution
	cfg                      deploymentConfig
	r                        *createServiceSessionRecovery
	a                        createFoundationRecoveryArtifact
	child                    lateLogContractBackend
	backend                  environmentKubernetesBackend
	native                   *redisNativeFixture
	mu                       sync.Mutex
	sourceID, podID, rsID    string
	denial, drift, failStage string
	calls, stages            []string
	postApply                func(string)
}

func (f *redisExecutionContract) protocol(q redisExecutionRequest) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	a := q.Args
	if len(a) < 4 || a[0] != "--kubeconfig" || !strings.HasPrefix(a[2], "--request-timeout=") {
		return nil, errors.New("unfrozen request")
	}
	if _, err := os.Stat(a[1]); err != nil {
		return nil, err
	}
	a = a[3:]
	f.calls = append(f.calls, strings.Join(a, " "))
	if a[0] == "get" {
		if len(a) >= 6 && a[len(a)-3] == "--ignore-not-found=true" {
			ns := ""
			if len(a) > 4 && a[3] == "--namespace" {
				ns = a[4]
			}
			obj := f.child.objects[a[1]+"/"+ns+"/"+a[2]]
			if obj == nil {
				return nil, nil
			}
			return mustProviderRecoveryJSON(obj), nil
		}
		items := []any{}
		for _, obj := range f.child.objects {
			ns := deploymentplan.String(deploymentplan.Map(obj["metadata"])["namespace"])
			if a[1] == "namespace" && obj["kind"] == "Namespace" || a[1] == environmentNamespacedInventoryKinds && len(a) > 3 && ns == a[3] || a[1] == environmentClusterInventoryKinds && ns == "" && obj["kind"] != "Namespace" {
				items = append(items, obj)
			}
		}
		return mustProviderRecoveryJSON(deploymentplan.Object{"items": items}), nil
	}
	if len(a) == 6 && a[0] == "auth" && a[1] == "can-i" {
		if a[2]+"/"+a[3] == f.denial {
			return []byte("no\n"), nil
		}
		return []byte("yes\n"), nil
	}
	if a[0] == "create" || a[0] == "replace" {
		if !slices.Contains(a, "--dry-run=server") {
			return nil, errors.New("protocol must not bypass child executor")
		}
		var obj deploymentplan.Object
		if json.Unmarshal(q.Input, &obj) != nil {
			return nil, errors.New("invalid admission")
		}
		if f.drift == "admission" {
			deploymentplan.Map(obj["metadata"])["name"] = "foreign"
		}
		return mustProviderRecoveryJSON(obj), nil
	}
	if a[0] == "--namespace" && len(a) >= 5 && a[2] == "wait" {
		return []byte("ready\n"), nil
	}
	if a[0] == "exec" && len(a) >= 8 && a[1] == "--namespace" && a[4] == "-c" {
		i := slices.Index(a, "--")
		if i < 0 {
			return nil, errors.New("missing exec boundary")
		}
		endpoint := "source"
		if a[5] == "redis-migration" {
			endpoint = "target"
		}
		return f.native.run(context.Background(), endpoint, q.Input, a[i+1:]...)
	}
	return nil, fmt.Errorf("unexpected isolated command %q", a)
}

// This fixture covers the bounded Redis adapter, with a genuinely accepted
// original Redis child, fixed authority and retained-volume ledger. Unrelated
// earlier entries are selector fixtures, not complete bootstrap ancestry proof.
func newRedisExecutionContract(t *testing.T) *redisExecutionContract {
	t.Helper()
	store, e, _, b := freshStageFixture(t)
	f := &redisExecutionContract{t: t, store: store, e: e, cfg: deploymentConfig{Environment: "dev", Values: map[string]string{"REDIS_DATA_STORAGE": "5Gi", "DURABLE_DATA_STORAGE_CLASS": "selected-retain"}}, child: lateLogContractBackend{b}, native: newRedisNativeFixture()}
	ns := e.Journal.Stack + "-platform"
	defs, err := decodeEnvironmentCreateManifests(lkeRedisDeploymentManifest(map[string]string{"CLOUD_ENV_NAME": "dev", "CLOUD_STACK_NAME": e.Journal.Stack}))
	if err != nil || len(defs) != 1 {
		t.Fatal(err)
	}
	original := defs[0].Object
	deploymentplan.Map(certIssuerObjectList(environmentPKIPod(original)["containers"])[0])["image"] = "valkey/valkey@sha256:" + strings.Repeat("a", 64)
	f.sourceID = deploymentplan.ID(original)
	for _, namespace := range []string{ns, e.Journal.Stack + "-video-cloud"} {
		obj := deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": namespace}}
		f.child.objects[deploymentplan.ID(obj)] = obj
	}
	sc := deploymentplan.Object{"apiVersion": "storage.k8s.io/v1", "kind": "StorageClass", "metadata": deploymentplan.Object{"name": "selected-retain"}, "provisioner": "linodebs.csi.linode.com", "reclaimPolicy": "Retain"}
	f.child.objects[deploymentplan.ID(sc)] = sc
	if err := f.apply(t.Context(), "redis-source-bootstrap", []deploymentplan.Definition{{Object: original, Phase: "workload"}}); err != nil {
		t.Fatal(err)
	}
	accepted := *createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/redis-source-bootstrap")
	sample, _ := redisDurableProjectionFixture()
	e.Journal.Entries = sample.Entries
	e.Journal.Entries[0] = accepted
	custody := "retire-pki-bootstrap-custody-" + e.Journal.PlanSHA256[:12]
	e.Journal.Entries[429].Key = custody
	e.Journal.Entries[430].Key = "kubernetes/" + custody
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	f.installPod("source-redis", "11111111-3333-4333-8333-333333333333", original)
	ledger, err := readEnvironmentCreateLedger(e)
	if err != nil {
		t.Fatal(err)
	}
	for i := 1; i <= 18; i++ {
		v := environmentCreateVolumeOwner{NativeID: fmt.Sprint(i), PVName: fmt.Sprint("original-pv-", i), PVUID: fmt.Sprintf("%08d-4444-4444-8444-444444444444", i), PVCName: fmt.Sprint("original-claim-", i), PVCUID: fmt.Sprintf("%08d-5555-4555-8555-555555555555", i), Namespace: e.Journal.Stack + "-video-cloud"}
		ledger.Volumes = append(ledger.Volumes, v)
		f.installVolume(v, nil)
	}
	if err := saveEnvironmentCreateLedger(t.Context(), e, ledger); err != nil {
		t.Fatal(err)
	}
	n, first, started, now := normalTailAuthorityFixture(t)
	n.SourceSHA = deploymentplan.Digest(managedLateNormalTailOriginalSource(e.Journal))
	n.CurrentSHA = n.SourceSHA
	n.OwnerSHA = deploymentplan.Digest(e.owner)
	n.Approval.ParentSHA = e.Journal.PlanSHA256
	first.Plan.ParentSHA = e.Journal.PlanSHA256
	first.Plan.ParentOwnerSHA = n.OwnerSHA
	first.Plan.ParentJournalSHA = n.SourceSHA
	first.Plan.Materials[managedLateNormalTailMaterial+"source"] = managedLateNormalTailDigest(n)
	n.Review = deploymentplan.Digest(first.Plan)
	first.Qualification.PlanSHA256 = n.Review
	for i := range first.Qualification.Gates {
		first.Qualification.Gates[i].EvidenceSHA256 = n.Review
	}
	phase, err := newManagedLateNormalTailAuthority(n, first, environmentPreflightBytesSHA256(mustProviderRecoveryJSON(first)), started, now)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]any{"qualification.json": first, managedLateNormalTailFilename: n} {
		if err := store.write(filepath.Join("deployment/create/openbao-recovery", n.Review, name), mustProviderRecoveryJSON(value), false); err != nil {
			t.Fatal(err)
		}
	}
	if err := ensurePrivateDirectory(filepath.Dir(filepath.Join(store.Root, managedLateNormalTailPath(e.Journal.PlanSHA256, false)))); err != nil {
		t.Fatal(err)
	}
	if err := writeManagedLateNormalTailPair(store, e.Journal.PlanSHA256, mustProviderRecoveryJSON(phase)); err != nil {
		t.Fatal(err)
	}
	n.Authority = phase
	n.CurrentSHA = deploymentplan.Digest(e.Journal)
	f.r = &createServiceSessionRecovery{normalTail: n}
	f.a = first
	f.a.Plan.Materials = map[string]string{}
	for k, v := range first.Plan.Materials {
		f.a.Plan.Materials[k] = v
	}
	f.a.Plan.ParentJournalSHA = deploymentplan.Digest(e.Journal)
	f.a.Plan.Materials[managedLateNormalTailMaterial+"source"] = managedLateNormalTailDigest(n)
	f.a.Plan.Plan.ConfigurationSHA256 = deploymentplan.Digest(f.cfg)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var q redisExecutionRequest
		if json.NewDecoder(r.Body).Decode(&q) != nil {
			http.Error(w, "bad request", 400)
			return
		}
		raw, err := f.protocol(q)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		_, _ = w.Write(raw)
	}))
	t.Cleanup(server.Close)
	binary, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\nexec " + shellQuoteArg(binary) + " -test.run='^TestManagedLateRedisExternalProtocolHelper$' -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_TEST_REDIS_PROTOCOL", server.URL)
	t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(dir, "kubectl"))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	f.backend = environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated-redis-contract-kubeconfig"), RolloutTimeout: time.Second}
	f.stages = nil
	return f
}

func (f *redisExecutionContract) installVolume(v environmentCreateVolumeOwner, claim deploymentplan.Object) {
	if claim == nil {
		claim = deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": v.PVCName, "namespace": v.Namespace}, "spec": deploymentplan.Object{"storageClassName": "selected-retain"}}
	} else {
		claim = deploymentplan.Clone(claim)
	}
	deploymentplan.Map(claim["metadata"])["uid"] = v.PVCUID
	deploymentplan.Map(claim["spec"])["volumeName"] = v.PVName
	claim["status"] = deploymentplan.Object{"phase": "Bound"}
	f.child.objects[deploymentplan.ID(claim)] = claim
	pv := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolume", "metadata": deploymentplan.Object{"name": v.PVName, "uid": v.PVUID}, "spec": deploymentplan.Object{"storageClassName": "selected-retain", "persistentVolumeReclaimPolicy": "Retain", "claimRef": deploymentplan.Object{"name": v.PVCName, "namespace": v.Namespace, "uid": v.PVCUID}, "csi": deploymentplan.Object{"driver": "linodebs.csi.linode.com", "volumeHandle": v.NativeID + "-pvc-isolated"}}, "status": deploymentplan.Object{"phase": "Bound"}}
	f.child.objects[deploymentplan.ID(pv)] = pv
}
func (f *redisExecutionContract) installPod(name, uid string, deployment deploymentplan.Object) {
	ns := f.e.Journal.Stack + "-platform"
	owner := f.child.objects[f.sourceID]
	deploymentplan.Map(owner["metadata"])["generation"] = float64(1)
	owner["status"] = deploymentplan.Object{"observedGeneration": float64(1), "replicas": float64(1), "readyReplicas": float64(1), "updatedReplicas": float64(1), "availableReplicas": float64(1)}
	rs := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": deploymentplan.Object{"namespace": ns, "name": "redis-rs", "uid": "22222222-3333-4333-8333-333333333333", "ownerReferences": []any{deploymentplan.Object{"kind": "Deployment", "uid": deploymentplan.Map(owner["metadata"])["uid"], "controller": true}}}}
	pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": deploymentplan.Object{"namespace": ns, "name": name, "uid": uid, "ownerReferences": []any{deploymentplan.Object{"kind": "ReplicaSet", "uid": deploymentplan.Map(rs["metadata"])["uid"], "controller": true}}}, "spec": deploymentplan.Clone(environmentPKIPod(deployment)), "status": deploymentplan.Object{"phase": "Running"}}
	f.podID = deploymentplan.ID(pod)
	f.rsID = deploymentplan.ID(rs)
	f.child.objects[f.podID] = pod
	f.child.objects[f.rsID] = rs
}
func (f *redisExecutionContract) apply(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
	if stage == f.failStage {
		return errors.New("isolated stage interruption: " + stage)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	objects := []deploymentplan.Object{}
	for _, o := range f.child.objects {
		objects = append(objects, o)
	}
	inputs := deploymentplan.Inputs{Environment: "dev", Stack: f.e.Journal.Stack, Operation: "scoped", ConfigurationSHA256: deploymentplan.Digest(f.cfg), ReleaseSHA256: deploymentplan.Digest(f.a.Plan.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, defs)}
	p, err := deploymentplan.Compile(inputs, defs, objects)
	if err != nil {
		return err
	}
	qualify := func(ctx context.Context, p *deploymentplan.Plan) error {
		return p.AdmitResumable(ctx, f.child, f.store.Root)
	}
	fence := func(ctx context.Context, p *deploymentplan.Plan) error {
		return f.child.CheckParentExecution(ctx, p, f.store.Root)
	}
	accept := func(ctx context.Context, p *deploymentplan.Plan) error { return p.Verify(ctx, f.child) }
	if err := executeEnvironmentCreateStagePlan(ctx, f.store, f.e, stage, p, f.child, qualify, fence, accept, f.e.Check, io.Discard); err != nil {
		return err
	}
	f.stages = append(f.stages, stage)
	switch stage {
	case "normal-redis-durable-claim":
		claim := f.child.objects[p.Resources[0].ID]
		f.installVolume(environmentCreateVolumeOwner{NativeID: "99", PVName: "redis-retained-pv", PVUID: "99999999-4444-4444-8444-444444444444", PVCName: "redis-data", PVCUID: deploymentplan.String(deploymentplan.Map(claim["metadata"])["uid"]), Namespace: f.e.Journal.Stack + "-platform"}, claim)
	case "normal-redis-durable-stop":
		delete(f.child.objects, f.podID)
		delete(f.child.objects, f.rsID)
		actual := f.child.objects[f.sourceID]
		deploymentplan.Map(actual["metadata"])["generation"] = float64(2)
		actual["status"] = deploymentplan.Object{"observedGeneration": float64(2), "replicas": float64(0), "readyReplicas": float64(0), "updatedReplicas": float64(0), "availableReplicas": float64(0)}
	case "normal-redis-durable-switch":
		f.installPod("durable-redis", "33333333-3333-4333-8333-333333333333", defs[0].Object)
		f.native.source = f.native.target
	}
	if f.postApply != nil {
		f.postApply(stage)
	}
	return nil
}
func (f *redisExecutionContract) qualify() error {
	return qualifyManagedLateRedisDurable(f.t.Context(), f.cfg, f.store, f.e, f.backend, f.r, &f.a.Plan)
}

// The executor starts after global completed-tail admission. Its artifact,
// private bytes and checker bindings remain subject to the real runtime checks;
// this local setup does not substitute for testing global ancestry admission.
func (f *redisExecutionContract) admit() {
	f.t.Helper()
	d := f.r.redisDurable
	var err error
	f.a.Plan.CheckerSHA, err = environmentPreflightCheckerSHA256()
	if err != nil {
		f.t.Fatal(err)
	}
	f.a.QualifiedAt = time.Now().UTC().Add(-time.Second)
	f.a.ValidUntil = f.a.QualifiedAt.Add(15 * time.Minute)
	f.a.Qualification.PlanSHA256 = deploymentplan.Digest(f.a.Plan)
	for i := range f.a.Qualification.Gates {
		f.a.Qualification.Gates[i].EvidenceSHA256 = f.a.Qualification.PlanSHA256
	}
	d.QualifiedReview = f.a.Qualification.PlanSHA256
	raw := mustProviderRecoveryJSON(f.a)
	if err := f.store.write(filepath.Join("deployment/create/openbao-recovery", d.QualifiedReview, "qualification.json"), raw, false); err != nil {
		f.t.Fatal(err)
	}
	d.admitted = true
	d.artifactSHA = deploymentplan.Digest(f.a)
	d.qualifiedBytesSHA = environmentPreflightBytesSHA256(raw)
}
func (f *redisExecutionContract) execute() error {
	return executeManagedLateRedisDurable(f.t.Context(), f.cfg, f.store, environmentCreateBootstrapRuntime{Execution: f.e, Backend: f.backend, ServiceSessionRecovery: f.r}, f.apply)
}

func TestManagedLateRedisDurableExecutionContract(t *testing.T) {
	f := newRedisExecutionContract(t)
	before := deploymentplan.Digest(f.e.Journal)
	if err := f.qualify(); err != nil {
		t.Fatal("qualification", err)
	}
	if deploymentplan.Digest(f.e.Journal) != before || f.native.pauseCalls != 0 || len(f.stages) != 0 {
		t.Fatal("qualification mutated source")
	}
	if err := f.qualify(); err != nil {
		t.Fatal("repeat qualification", err)
	}
	f.admit()
	if err := f.execute(); err != nil {
		t.Fatal("migration", err)
	}
	d := f.r.redisDurable
	expected := []string{"normal-redis-durable-claim", "normal-redis-durable-target", "normal-redis-durable-retire", "normal-redis-durable-stop", "normal-redis-durable-switch"}
	if !slices.Equal(f.stages, expected) || !f.native.saved || !f.native.transferred || !f.native.stopped || f.native.unpauseCalls != 0 {
		t.Fatal("migration ordering/data preservation", f.stages)
	}
	if len(f.e.Journal.Entries) != 443 || createOpenBaoACKCompletionEntry(f.e.Journal, managedLateRedisDurableKey).State != "completed" {
		t.Fatal("migration was not durably accepted")
	}
	raw, err := readManagedLateRedisArchive(f.store, d.ParentSHA, environmentPreflightBytesSHA256(f.native.rdb))
	if err != nil || !bytes.Equal(raw, f.native.rdb) {
		t.Fatal("original archive", err)
	}
	originals, volume, err := readManagedLateRedisDurableVolumeAuthority(f.store, f.e)
	if err != nil || len(originals) != 18 || volume == nil || *volume != *d.Volume {
		t.Fatal("completed volume authority", err)
	}
	before = deploymentplan.Digest([]any{f.e.Journal, f.child.objects, f.child.writes})
	if err := f.execute(); err == nil || !strings.Contains(err.Error(), "recorded-outcome-needs-reconciliation") {
		t.Fatal("completed migration replay", err)
	}
	if before != deploymentplan.Digest([]any{f.e.Journal, f.child.objects, f.child.writes}) {
		t.Fatal("refused retry changed source")
	}
}

func TestManagedLateRedisDurableQualificationRejectsChangedPrerequisites(t *testing.T) {
	cases := []struct{ name, want string }{
		{"foreign-original-owner", "accepted-original-owner"},
		{"unrecorded-persistent-source", "unrecorded-persistent-switch"},
		{"unrecorded-target", "unrecorded-target"},
		{"permission", "required-kubernetes-permission"},
		{"admission", "target-admission"},
		{"orphan-archive", "orphan-local-state"},
		{"missing-original-volume", "original-eighteen-volumes"},
		{"source-aof", "source-prerequisites"},
		{"source-unsaved-writes", "source-prerequisites"},
		{"prior-qualification-drift", "qualified-prerequisite-drift"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRedisExecutionContract(t)
			switch tc.name {
			case "foreign-original-owner":
				deploymentplan.Map(f.child.objects[f.sourceID]["metadata"])["uid"] = "44444444-3333-4333-8333-333333333333"
			case "unrecorded-persistent-source":
				environmentPKIPod(f.child.objects[f.sourceID])["volumes"] = []any{deploymentplan.Object{"name": "data", "persistentVolumeClaim": deploymentplan.Object{"claimName": "redis-data"}}}
			case "unrecorded-target":
				claim := deploymentplan.Object{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": deploymentplan.Object{"name": "redis-data", "namespace": f.e.Journal.Stack + "-platform"}}
				f.child.objects[deploymentplan.ID(claim)] = claim
			case "permission":
				f.denial = "create/pods/exec"
			case "admission":
				f.drift = "admission"
			case "orphan-archive":
				if err := f.store.write(filepath.Join(managedLateRedisDurableBase(f.e.Journal.PlanSHA256), "source.rdb"), f.native.rdb, false); err != nil {
					t.Fatal(err)
				}
			case "missing-original-volume":
				ledger, err := readEnvironmentCreateLedger(f.e)
				if err != nil {
					t.Fatal(err)
				}
				ledger.Volumes = ledger.Volumes[:17]
				if err := saveEnvironmentCreateLedger(t.Context(), f.e, ledger); err != nil {
					t.Fatal(err)
				}
			case "source-aof":
				f.native.source.AOF = 1
			case "source-unsaved-writes":
				f.native.source.RDBChanges = 1
			case "prior-qualification-drift":
				if err := f.qualify(); err != nil {
					t.Fatal(err)
				}
				f.r.redisDurable.AcceptedPlanSHA = strings.Repeat("9", 64)
			}
			before := deploymentplan.Digest([]any{f.e.Journal, f.child.objects, f.child.writes})
			if err := f.qualify(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal("unsafe qualification", err)
			}
			if before != deploymentplan.Digest([]any{f.e.Journal, f.child.objects, f.child.writes}) || f.native.pauseCalls != 0 {
				t.Fatal("qualification refusal modified original resources")
			}
		})
	}
}

func TestManagedLateRedisDurableExecutionInterruptionPreservesData(t *testing.T) {
	cases := []struct {
		name, want                string
		stopped, archive, unpause bool
	}{
		{"snapshot-save", "snapshot-save", false, false, true},
		{"target-load", "redis-native-command-refused", false, true, true},
		{"retire", "isolated stage interruption", false, true, true},
		{"stop", "source-stop-unverified-no-pvc-start", false, true, false},
		{"switch", "isolated stage interruption", true, true, false},
		{"target-data", "native-loaded-data", true, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newRedisExecutionContract(t)
			if err := f.qualify(); err != nil {
				t.Fatal(err)
			}
			f.admit()
			originalUID := deploymentplan.Map(f.child.objects[f.sourceID]["metadata"])["uid"]
			originalVolumes := deploymentplan.Digest(f.r.redisDurable.OriginalVolumes)
			switch tc.name {
			case "snapshot-save":
				f.native.fail = func(endpoint string, args []string) ([]byte, error, bool) {
					if endpoint == "source" && len(args) > 2 && args[2] == "SAVE" {
						return nil, errors.New("isolated SAVE failure"), true
					}
					return nil, nil, false
				}
			case "target-load":
				f.native.fail = func(endpoint string, args []string) ([]byte, error, bool) {
					if endpoint == "target" && len(args) == 3 && strings.Contains(args[2], "valkey-server --bind ") {
						return nil, errors.New("isolated target launch failure"), true
					}
					return nil, nil, false
				}
			case "retire", "stop", "switch":
				f.failStage = "normal-redis-durable-" + tc.name
			case "target-data":
				f.postApply = func(stage string) {
					if stage == "normal-redis-durable-switch" {
						f.native.source.Keys++
					}
				}
			}
			err := f.execute()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatal("expected interruption", err)
			}
			if (f.native.unpauseCalls == 1) != tc.unpause {
				t.Fatal("pause release did not respect stop boundary", f.native.unpauseCalls)
			}
			if deploymentplan.Map(f.child.objects[f.sourceID]["metadata"])["uid"] != originalUID || deploymentplan.Digest(f.r.redisDurable.OriginalVolumes) != originalVolumes {
				t.Fatal("original owner/volume pins changed")
			}
			if err := managedLateRedisVerifyOriginalVolumes(t.Context(), f.backend, f.r.redisDurable.OriginalVolumes); err != nil {
				t.Fatal("original volume retention", err)
			}
			if slices.Contains(f.stages, "normal-redis-durable-stop") != tc.stopped {
				t.Fatal("unexpected stop boundary", f.stages)
			}
			archive, readErr := readManagedLateRedisArchive(f.store, f.e.Journal.PlanSHA256, environmentPreflightBytesSHA256(f.native.rdb))
			if tc.archive && (readErr != nil || !bytes.Equal(archive, f.native.rdb)) {
				t.Fatal("recoverable snapshot not retained", readErr)
			}
			if !tc.archive && readErr == nil {
				t.Fatal("failed snapshot gained archive")
			}
			if entry := createOpenBaoACKCompletionEntry(f.e.Journal, managedLateRedisDurableKey); entry == nil || entry.State != "pending" {
				t.Fatal("interrupted migration was accepted")
			}
			before := deploymentplan.Digest([]any{f.e.Journal, f.child.objects, f.child.writes})
			if err := f.execute(); err == nil {
				t.Fatal("uncertain migration replayed")
			}
			if before != deploymentplan.Digest([]any{f.e.Journal, f.child.objects, f.child.writes}) {
				t.Fatal("refused retry mutated partial result")
			}
		})
	}
}

func TestManagedLateRedisDurableExecutionRejectsTamperedCompletion(t *testing.T) {
	f := newRedisExecutionContract(t)
	if err := f.qualify(); err != nil {
		t.Fatal(err)
	}
	f.admit()
	if err := f.execute(); err != nil {
		t.Fatal(err)
	}
	parent := f.e.Journal.PlanSHA256
	cases := []struct {
		name, path string
		change     func([]byte) []byte
	}{
		{"source", filepath.Join(managedLateRedisDurableBase(parent), "source.json"), func(raw []byte) []byte {
			var v managedLateRedisDurableSaved
			_ = json.Unmarshal(raw, &v)
			v.Source.Entries[3].Receipt = []byte(`{"changed":true}`)
			return mustProviderRecoveryJSON(v)
		}},
		{"qualification", filepath.Join("deployment/create/openbao-recovery", f.r.redisDurable.QualifiedReview, "qualification.json"), func(raw []byte) []byte {
			var v createFoundationRecoveryArtifact
			_ = json.Unmarshal(raw, &v)
			v.Qualification.Decision = "BLOCKED"
			return mustProviderRecoveryJSON(v)
		}},
		{"volume", filepath.Join(managedLateRedisDurableBase(parent), "volume.json"), func(raw []byte) []byte {
			var v environmentCreateVolumeOwner
			_ = json.Unmarshal(raw, &v)
			v.PVCUID = "77777777-7777-4777-8777-777777777777"
			return mustProviderRecoveryJSON(v)
		}},
		{"completion", filepath.Join(managedLateRedisDurableBase(parent), "completed.json"), func(raw []byte) []byte {
			var v managedLateRedisDurableReceipt
			_ = json.Unmarshal(raw, &v)
			v.RDBSHA = strings.Repeat("9", 64)
			return mustProviderRecoveryJSON(v)
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(f.store.Root, tc.path)
			original, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if err := os.WriteFile(path, original, 0600); err != nil {
					t.Error(err)
				}
			})
			changed := tc.change(original)
			if err := os.WriteFile(path, changed, 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := readManagedLateRedisDurableVolumeAuthority(f.store, f.e); err == nil {
				t.Fatal("changed completed authority accepted")
			}
			after, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(after, changed) {
				t.Fatal("refusal rewrote evidence", err)
			}
		})
	}
	path := filepath.Join(f.store.Root, managedLateRedisDurableBase(parent), "source.rdb")
	if err := os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := readManagedLateRedisArchive(f.store, parent, environmentPreflightBytesSHA256(f.native.rdb)); err == nil {
		t.Fatal("public archive accepted")
	}
	if err := os.Chmod(path, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(bytes.Clone(f.native.rdb), '!'), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readManagedLateRedisArchive(f.store, parent, environmentPreflightBytesSHA256(f.native.rdb)); err == nil {
		t.Fatal("changed snapshot accepted")
	}
}
