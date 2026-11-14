package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestOpenBaoFirstRecordedObjectRequiresOriginalCompletedOwner(t *testing.T) {
	want := deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": "isolated", "labels": deploymentplan.Object{"rtk.realtek.com/stack": "isolated"}}}
	raw, _ := json.Marshal(want)
	sha, err := environmentCreatePlanSHA256(raw)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	entry := environmentCreateJournalEntry{Key: deploymentplan.ID(want), Phase: "namespace", State: "completed", IntentSHA256: sha, StartedAt: now, CompletedAt: now.Add(time.Second), Receipt: json.RawMessage(`{"resource_id":"Namespace//isolated","uid":"original-uid"}`)}
	live := deploymentplan.Object{"apiVersion": "v1", "kind": "Namespace", "metadata": deploymentplan.Object{"name": "isolated", "uid": "original-uid", "labels": deploymentplan.Object{"rtk.realtek.com/stack": "isolated"}}}
	for _, mode := range []string{"original", "missing", "pending", "foreign-uid", "foreign-intent", "changed-body", "duplicate"} {
		t.Run(mode, func(t *testing.T) {
			j := environmentCreateJournal{AdmittedAt: now, Entries: []environmentCreateJournalEntry{entry}}
			actual := deploymentplan.Object{"apiVersion": live["apiVersion"], "kind": live["kind"], "metadata": deploymentplan.Object{"name": "isolated", "uid": "original-uid", "labels": deploymentplan.Object{"rtk.realtek.com/stack": "isolated"}}}
			switch mode {
			case "missing":
				j.Entries = nil
			case "pending":
				j.Entries[0].State = "pending"
			case "foreign-uid":
				deploymentplan.Map(actual["metadata"])["uid"] = "foreign-uid"
			case "foreign-intent":
				j.Entries[0].IntentSHA256 = "foreign"
			case "changed-body":
				deploymentplan.Map(actual["metadata"])["labels"] = deploymentplan.Object{"rtk.realtek.com/stack": "foreign"}
			case "duplicate":
				j.Entries = append(j.Entries, entry)
			}
			err := verifyOpenBaoFirstRecordedObject(j, "namespace", want, actual)
			if mode == "original" && err != nil {
				t.Fatal(err)
			}
			if mode != "original" && err == nil {
				t.Fatal("accepted unproven original owner")
			}
		})
	}
}

func TestOpenBaoFirstProviderMaterialUsesOriginalWithoutRepair(t *testing.T) {
	cfg, store, _, resource, _ := createProviderRuntimeFixture(t)
	sha := strings.Repeat("a", 64)
	original, err := prepareEnvironmentCreateProviderMaterial(context.Background(), cfg, sha, resource, store)
	if err != nil {
		t.Fatal(err)
	}
	got, err := readOpenBaoFirstProviderMaterial(store, sha, resource)
	if err != nil || deploymentplan.Digest(got) != deploymentplan.Digest(original) {
		t.Fatal("original producer material differs", err)
	}
	path := filepath.Join(store.Root, "deployment", "create", "material", sha, "host-identity", resource.Key, "ssh_host_ed25519_key.pub")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if _, err := readOpenBaoFirstProviderMaterial(store, sha, resource); err == nil {
		t.Fatal("missing original public key accepted")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("qualification repaired a missing file", err)
	}
}

func TestOpenBaoFirstRecordedStorageClassRetainsExistingOwner(t *testing.T) {
	want := deploymentplan.Object{"apiVersion": "storage.k8s.io/v1", "kind": "StorageClass", "metadata": deploymentplan.Object{"name": "selected", "labels": deploymentplan.Object{"rtk.realtek.com/stack": "isolated", "app.kubernetes.io/managed-by": "rtk-cloud"}}, "provisioner": "example.csi", "reclaimPolicy": "Retain", "volumeBindingMode": "Immediate", "allowVolumeExpansion": true}
	policy := deploymentplan.Object{"provisioner": want["provisioner"], "reclaimPolicy": want["reclaimPolicy"], "volumeBindingMode": want["volumeBindingMode"], "allowVolumeExpansion": want["allowVolumeExpansion"]}
	raw, _ := json.Marshal(want)
	sha, _ := environmentCreatePlanSHA256(raw)
	receipt, _ := json.Marshal(deploymentplan.Object{"resource_id": deploymentplan.ID(want), "uid": "original-uid", "policy_sha256": deploymentplan.Digest(policy)})
	now := time.Now().UTC()
	j := environmentCreateJournal{AdmittedAt: now, Entries: []environmentCreateJournalEntry{{Key: deploymentplan.ID(want), Phase: "storage", State: "completed", IntentSHA256: sha, StartedAt: now, CompletedAt: now.Add(time.Second), Receipt: receipt}}}
	live := deploymentplan.Object{"apiVersion": want["apiVersion"], "kind": want["kind"], "metadata": deploymentplan.Object{"name": "selected", "uid": "original-uid"}, "provisioner": want["provisioner"], "reclaimPolicy": want["reclaimPolicy"], "volumeBindingMode": want["volumeBindingMode"], "allowVolumeExpansion": want["allowVolumeExpansion"]}
	if err := verifyOpenBaoFirstRecordedObject(j, "storage", want, live); err != nil {
		t.Fatal("preexisting retained class rejected", err)
	}
	live["reclaimPolicy"] = "Delete"
	if err := verifyOpenBaoFirstRecordedObject(j, "storage", want, live); err == nil {
		t.Fatal("changed durable policy accepted")
	}
}

func TestOpenBaoFirstWorkerOwnersUseOriginalNativeBindings(t *testing.T) {
	cfg, store, operatorSHA, artifact := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(t.Context(), cfg, artifact, operatorSHA, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	pool := deploymentplan.Object{"id": 10, "label": "video-cloud-dev-general", "labels": deploymentplan.Object{"rtk.io/node-class": "general"}, "nodes": []any{deploymentplan.Object{"instance_id": 101}}}
	instance := deploymentplan.Object{"id": 101, "label": "allocated-original", "status": "running", "ipv4": []any{"192.0.2.1"}}
	writes := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/lke/clusters/77/pools":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{pool}, "page": 1, "pages": 1, "results": 1})
		case "/linode/instances":
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []any{instance}, "page": 1, "pages": 1, "results": 1})
		case "/linode/instances/101":
			if r.Method == "PUT" {
				var request map[string]string
				_ = json.NewDecoder(r.Body).Decode(&request)
				instance["label"] = request["label"]
				writes++
			}
			_ = json.NewEncoder(w).Encode(instance)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("RTK_CLOUD_LINODE_API_ROOT", server.URL)
	cluster := environmentCreateProviderReceipt{Kind: "lke", Name: "video-cloud-dev-lke", NativeID: "77"}
	operator := map[string]string{"LINODE_TOKEN": "isolated-only"}
	if err := initializeEnvironmentCreateWorkerNames(t.Context(), cfg, store, operator, cluster, e); err != nil {
		t.Fatal(err)
	}
	before := writes
	if err := verifyOpenBaoFirstWorkerOwners(t.Context(), cfg, store, e.Journal, cluster, operator); err != nil {
		t.Fatal("original owners rejected", err)
	}
	instance["label"] = "foreign"
	if err := verifyOpenBaoFirstWorkerOwners(t.Context(), cfg, store, e.Journal, cluster, operator); err == nil {
		t.Fatal("foreign label accepted")
	}
	instance["label"] = "video-cloud-dev-lke-general-01"
	pool["nodes"] = []any{deploymentplan.Object{"instance_id": 102}}
	if err := verifyOpenBaoFirstWorkerOwners(t.Context(), cfg, store, e.Journal, cluster, operator); err == nil {
		t.Fatal("foreign pool member accepted")
	}
	if writes != before {
		t.Fatal("read-only qualification renamed a worker")
	}
}

// Uses the same stateful provider and original naming producer as the full
// isolated create; removing status reproduces the previously observed gap.
func TestOpenBaoFirstWorkerOwnersRealIsolatedProvider(t *testing.T) {
	cfg, operator := createProviderTestFixture(t, "dev")
	operator["LINODE_TOKEN"] = "isolated-provider-token"
	stateCfg, store, operatorSHA, artifact := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(t.Context(), stateCfg, artifact, operatorSHA, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer e.Close()
	provider := newIsolatedFreshProvider(t, cfg, operator, []byte("isolated-kubeconfig"))
	plan, err := buildEnvironmentCreateProviderPlan(t.Context(), cfg, operator)
	if err != nil {
		t.Fatal(err)
	}
	var cluster environmentCreateProviderReceipt
	for _, resource := range plan.Resources {
		if resource.Kind != "lke" {
			continue
		}
		cluster, err = executeEnvironmentCreateProviderResource(t.Context(), cfg, resource, operator, nil)
		if err != nil {
			t.Fatal(err)
		}
	}
	if cluster.NativeID == "" {
		t.Fatal("isolated original cluster missing")
	}
	if err := initializeEnvironmentCreateWorkerNames(t.Context(), cfg, store, operator, cluster, e); err != nil {
		t.Fatal("original worker naming producer failed", err)
	}
	if err := verifyOpenBaoFirstWorkerOwners(t.Context(), cfg, store, e.Journal, cluster, operator); err != nil {
		t.Fatal("original isolated provider workers rejected", err)
	}
	provider.mu.Lock()
	workers := provider.resources["/linode/instances"]
	if len(workers) == 0 {
		provider.mu.Unlock()
		t.Fatal("isolated worker inventory missing")
	}
	first := workers[0]
	status := first["status"]
	delete(first, "status")
	provider.mu.Unlock()
	if err := verifyOpenBaoFirstWorkerOwners(t.Context(), cfg, store, e.Journal, cluster, operator); err == nil {
		t.Fatal("worker with missing status accepted")
	}
	provider.mu.Lock()
	first["status"] = status
	label := first["label"]
	first["label"] = "foreign-worker-label"
	provider.mu.Unlock()
	if err := verifyOpenBaoFirstWorkerOwners(t.Context(), cfg, store, e.Journal, cluster, operator); err == nil {
		t.Fatal("worker with foreign label accepted")
	}
	provider.mu.Lock()
	first["label"] = label
	provider.mu.Unlock()
}
