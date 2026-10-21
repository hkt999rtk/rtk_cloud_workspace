package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Provider storage activation is an explicit historical fixture boundary.
// Provider metadata and Kubernetes allocation/readiness are modeled external
// APIs; runtime initialization, temporary transport/signing material, maintained
// rendering and the original scoped executor run against an isolated store.
// This proves the bootstrap producer, not provider cutover or public preflight.
func TestFreshFoundationProducerPreservesOriginalRuntimeAndOwnedNamespaces(t *testing.T) {
	f := newProviderStorageExecutionContract(t, "complete", func(_ *deploymentConfig, plan *environmentCreatePlan) []environmentReleasePublicationProof {
		// Select the synthetic immutable provider role before the original
		// artifact and its publication proof are frozen; never retrofit it.
		plan.Release.Images["LKE_OPENBAO_IMAGE"] = environmentReleaseImage{Reference: "docker.io/openbao/openbao@sha256:" + strings.Repeat("1", 64)}
		return coreContractPublicationProofs(t, &plan.Release)
	})
	// Select the external metadata before any observation is captured. These
	// endpoints are returned data only: this contract performs no S3 requests,
	// key allocation, bucket mutation or storage activation operation.
	var metadataReads atomic.Int64
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/object-storage/buckets" || r.Header.Get("Authorization") != "Bearer "+f.operator["LINODE_TOKEN"] {
			t.Error("fresh foundation attempted an unselected provider operation", r.Method, r.URL.Path)
			http.Error(w, "readonly original bucket metadata required", http.StatusForbidden)
			return
		}
		metadataReads.Add(1)
		buckets := []linodeStorageBucket{}
		for _, target := range []deploymentStorageTarget{f.cfg.Storage.RuntimeMedia, f.cfg.Storage.OTAFirmware} {
			buckets = append(buckets, linodeStorageBucket{Label: target.Bucket, Region: target.Region, S3Endpoint: "https://isolated-storage.example.invalid", EndpointType: "E3"})
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": buckets, "page": 1, "pages": 1, "results": len(buckets)})
	}))
	t.Cleanup(provider.Close)
	t.Setenv("RTK_CLOUD_LINODE_API_ROOT", provider.URL)
	checker := defaultDeploymentCredentialChecker()
	if _, err := observeOTAStorageForRender(t.Context(), f.cfg, f.store, f.operator, checker); err != nil {
		t.Fatal("actual selected original OTA allocation metadata", err)
	}
	if _, err := observeMediaStorageForRender(t.Context(), f.cfg, f.store, f.operator, checker); err != nil {
		t.Fatal("actual selected original media allocation metadata", err)
	}
	// Complete only the explicit external provider fixture boundary. Actual
	// storage activation/key lifecycle is covered separately; this receipt must
	// not pretend that this bootstrap contract executed either operation.
	if err := f.e.RecordReceipt("activate-storage", mustProviderRecoveryJSON(map[string]string{"fixture_boundary": "external-provider-storage-activation", "configuration_sha256": f.plan.ConfigurationSHA256})); err != nil {
		t.Fatal(err)
	}
	if err := f.e.CompleteEntry("activate-storage", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	state := filepath.Join(t.TempDir(), "native.json")
	if err := os.WriteFile(state, []byte(`{"objects":{},"writes":0}`), 0600); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	command := `#!/usr/bin/env python3
import json,os,sys,uuid
a=sys.argv[1:]
p=os.environ['RTK_FRESH_FOUNDATION_NATIVE']
with open(p) as f:d=json.load(f)
if 'get' in a:
 i=a.index('get');kind,name=a[i+1:i+3]
 assert kind in ('Namespace','StorageClass')
 v=d['objects'].get(kind+'//'+name)
 if v is not None:print(json.dumps(v))
 sys.exit()
assert 'create' in a and '-f' in a and a[a.index('-f')+1]=='-'
v=json.load(sys.stdin);kind=v['kind'];m=v['metadata'];key=kind+'//'+m['name']
assert kind in ('Namespace','StorageClass') and not m.get('namespace') and key not in d['objects']
if '--dry-run=server' not in a:
 m['uid']=str(uuid.uuid4());m['resourceVersion']='1';d['objects'][key]=v;d['writes']+=1
 with open(p,'w') as f:json.dump(d,f)
print(json.dumps(v))
`
	path := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(path, []byte(command), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", path)
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_FRESH_FOUNDATION_NATIVE", state)
	native := &normalTailE2EBackend{e: f.e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}, routes: map[string]string{}}
	producer := &acceptedCoreExecutionContract{cfg: f.cfg, store: f.store, e: f.e, p: createFoundationRecoveryPlan{Plan: f.plan}, native: native, protocol: map[string]any{}, protocolPath: filepath.Join(t.TempDir(), "observations.json")}
	stop := errors.New("isolated accepted original foundation boundary before OpenBao initialization")
	calls := 0
	apply := func(ctx context.Context, stage string, defs []deploymentplan.Definition) error {
		if ctx.Err() != nil || stage != "foundation" {
			t.Fatal("fresh foundation escaped its original stage", stage, ctx.Err())
		}
		calls++
		var observed struct {
			Objects map[string]deploymentplan.Object `json:"objects"`
			Writes  int                              `json:"writes"`
		}
		if err := readExecutionJSON(state, &observed); err != nil || observed.Writes != len(lkeNamespaces(environmentCreateProviderValues(f.cfg)))+1 {
			t.Fatal("original namespaces and Retain CSI class were not actually created", err)
		}
		for id, object := range observed.Objects {
			native.objects[id] = deploymentplan.Clone(object)
		}
		producer.acceptedHistoricalStage(t, "foundation", defs)
		return stop
	}
	runtime, cleanup, err := prepareEnvironmentCreateBootstrap(t.Context(), f.cfg, &f.plan, f.store, f.operator, f.e, apply)
	defer cleanup()
	if !errors.Is(err, stop) || calls != 1 || runtime.Execution != f.e || len(runtime.Backend.KubeconfigContent) == 0 {
		t.Fatal("fresh producer did not reach its actual accepted foundation boundary", err, calls)
	}
	plan, journal, err := acceptedCreateStage(f.store, f.e, "kubernetes/foundation")
	if err != nil || !journal.Accepted || len(plan.Resources) == 0 || len(journal.Owners) != len(plan.Resources) {
		t.Fatal("fresh original foundation lost its actual native child owners", err)
	}
	for _, id := range []string{"StatefulSet/" + f.plan.Stack + "-secrets/openbao", "StatefulSet/" + f.plan.Stack + "-platform/postgresql", "Secret/" + f.plan.Stack + "-secrets/openbao-transport-tls", "Secret/" + f.plan.Stack + "-account-manager/account-manager-jwt"} {
		if journal.Owners[id] == "" || native.objects[id] == nil {
			t.Fatal("maintained fresh foundation omitted an actual original owner", id)
		}
	}
	if password, err := f.store.readRuntime("platform-admin"); err != nil || password != f.operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"] {
		t.Fatal("runtime catalog lost the original explicit operator credential", err)
	}
	if metadataReads.Load() != 2 {
		t.Fatal("fresh storage observations did not independently read both selected allocations", metadataReads.Load())
	}
	f.mu.Lock()
	providerWrites, providerCalls := f.posts+f.puts+f.deletes, len(f.calls)
	f.mu.Unlock()
	if providerWrites != 0 || providerCalls != 0 {
		t.Fatal("bootstrap-only fixture invoked a storage credential or object lifecycle")
	}
	for name, original := range f.originals {
		raw, err := os.ReadFile(filepath.Join(f.store.Root, "operator/env", name))
		if err != nil || string(raw) != string(original) || f.operator[name] != string(original) {
			t.Fatal("fresh bootstrap changed original storage credentials", name, err)
		}
	}
	before := createBootstrapStateSnapshot(t, f.store.Root)
	writes := readTestFile(t, state)
	_, retryCleanup, retryErr := prepareEnvironmentCreateBootstrap(t.Context(), f.cfg, &f.plan, f.store, f.operator, f.e, apply)
	defer retryCleanup()
	if retryErr == nil || !strings.Contains(retryErr.Error(), "new namespace is occupied") || calls != 1 || readTestFile(t, state) != writes {
		t.Fatal("fresh retry replayed occupied namespace/foundation or lost its recovery refusal", retryErr)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
	if err := qualifyEnvironmentCreateBootstrapLineage(f.store, nil, nil, nil); err != nil {
		t.Fatal("foundation preparation created permanent managed PKI authority before issuer bootstrap", err)
	}
	// A class with an already recorded intent cannot be automatically replayed,
	// even when its current native policy is unchanged. Preserve its UID, native
	// write count and original receipt timestamps through that refusal.
	classErr := createEnvironmentRetainStorageClass(t.Context(), f.cfg, runtime.Backend, f.e)
	if classErr == nil || !strings.Contains(classErr.Error(), "automatic replay is prohibited") || readTestFile(t, state) != writes {
		t.Fatal("original Retain class was replayed after its accepted receipt", classErr)
	}
	assertRecoveryContractReadOnly(t, f.store, before)
}
