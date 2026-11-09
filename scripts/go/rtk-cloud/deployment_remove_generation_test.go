package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestRemoveOriginalGenerationRejectsUnboundTargetsAndResumes(t *testing.T) {
	cfg, store := deploymentRemovalFixture(t, "dev", true)
	cfg.Storage.RuntimeMedia = deploymentStorageTarget{Purpose: "runtime-media", Bucket: "rtk-cloud-dev-runtime-us-sea", Region: "us-sea"}
	cfg.Storage.OTAFirmware = deploymentStorageTarget{Purpose: "ota-firmware", Bucket: "rtk-cloud-dev-ota-firmware-us-lax", Region: "us-lax"}
	kubeLog := fakeDeploymentRemovalKubectl(t, `{"items":[]}`, "")
	stack := cfg.Values["CLOUD_STACK_NAME"]
	_, kubeSHA, err := deploymentRemovalKubeconfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	const clusterID = "123"
	if err := writeLKEProviderState("dev", stack, map[string]string{"LKE_CLUSTER_ID": clusterID}); err != nil {
		t.Fatal(err)
	}
	if err := store.write("operator/env/LINODE_TOKEN", []byte("private-provider-token"), false); err != nil {
		t.Fatal(err)
	}
	volumeProof, _ := json.Marshal(deploymentRemovalVolumes{Schema: 1, Environment: "dev", Stack: stack, ClusterID: clusterID, KubeSHA256: kubeSHA, VolumeIDs: []string{}})
	if err := store.write(generationRemovalProof, volumeProof, false); err != nil {
		t.Fatal(err)
	}
	writeOriginalGenerationTestLedger(t, cfg, store, clusterID, kubeSHA)

	foreign := true
	edgeFailure := true
	var calls []string
	targets, _, _, err := loadGenerationRemovalTargets(cfg, store, clusterID)
	if err != nil {
		t.Fatal(err)
	}
	active := map[string]map[string]any{}
	for _, target := range targets {
		item := map[string]any{"label": target.Name, "region": target.Region, "tags": []string{stack}}
		if target.Kind == "bucket" {
			item["objects"] = 0
		} else {
			item["id"] = json.Number(target.NativeID)
		}
		active[target.path] = item
	}
	active["/linode/instances/90"] = map[string]any{"id": 90, "label": stack + "-lke-general-01", "region": "us-sea"}
	api := createProviderTestAPI(t, func(w http.ResponseWriter, r *http.Request) bool {
		path := r.URL.Path
		if r.Method == http.MethodDelete {
			calls = append(calls, path)
			if path == "/linode/instances/71" && edgeFailure {
				edgeFailure = false
				w.WriteHeader(http.StatusServiceUnavailable)
				return true
			}
			if active[path] == nil || path == "/linode/instances/90" {
				t.Error("unexpected DELETE", path)
				w.WriteHeader(http.StatusForbidden)
				return true
			}
			delete(active, path)
			if path == "/lke/clusters/123" {
				delete(active, "/linode/instances/90")
			}
			w.WriteHeader(http.StatusNoContent)
			return true
		}
		if path == "/lke/clusters/123/pools" {
			fmt.Fprint(w, `{"data":[{"id":11,"nodes":[{"instance_id":90}]}],"page":1,"pages":1,"results":1}`)
			return true
		}
		if r.URL.Query().Has("page") {
			items := []map[string]any{}
			for candidate, item := range active {
				if strings.HasPrefix(candidate, path+"/") {
					items = append(items, item)
				}
			}
			if foreign && path == "/linode/instances" {
				items = append(items, map[string]any{"id": 97, "label": stack + "-edge-haproxy-02", "region": "us-sea"})
			}
			json.NewEncoder(w).Encode(map[string]any{"data": items, "page": 1, "pages": 1, "results": len(items)})
			return true
		}
		if item := active[path]; item != nil {
			json.NewEncoder(w).Encode(item)
		} else {
			w.WriteHeader(http.StatusNotFound)
		}
		return true
	})
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	run := func(plan bool) error { return removeOriginalGeneration(ctx, cfg, store, clusterID, plan) }
	if err := run(false); err == nil || !strings.Contains(err.Error(), "unbound stack resource") || *api != 0 {
		t.Fatalf("unbound inventory was not rejected before deletion: %v", err)
	}
	if _, err := os.Stat(kubeLog); !os.IsNotExist(err) {
		t.Fatal("unbound inventory reached Kubernetes", err)
	}
	foreign = false
	if err := run(true); err != nil || *api != 0 {
		t.Fatalf("read-only plan reached mutation or failed: %v", err)
	}
	planned, err := os.ReadFile(kubeLog)
	if err != nil || !strings.Contains(string(planned), "get Lease") || !strings.Contains(string(planned), "get persistentvolume") ||
		strings.Contains(string(planned), "delete") {
		t.Fatal("plan did not read original Lease and volume ownership without mutation", err)
	}
	if err := run(false); err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("expected partial failure after deleting LKE, got %v", err)
	}
	before, err := os.ReadFile(kubeLog)
	if err != nil || !strings.Contains(string(before), "delete") {
		t.Fatal("formal Kubernetes purge did not run", err)
	}
	if err := run(false); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(kubeLog)
	if err != nil || string(before) != string(after) {
		t.Fatal("retry repeated Kubernetes cleanup after the original cluster was removed", err)
	}
	if len(active) != 0 || len(calls) != len(targets)+1 || calls[0] != "/lke/clusters/123" {
		t.Fatalf("unexpected exact removal operations: %v", calls)
	}
	if _, hash, err := deploymentRemovalKubeconfig(cfg); err != nil || hash != kubeSHA {
		t.Fatal("original kubeconfig was not retained for archival", err)
	}
	active["/linode/instances/71"] = map[string]any{"id": 71, "label": "renamed-original-edge", "region": "us-sea"}
	if err := archiveRemovedDeploymentGeneration(ctx, cfg, store, clusterID, verifyArchivedGenerationProviderAbsence); err == nil ||
		!strings.Contains(err.Error(), "/linode/instances/71") {
		t.Fatalf("archive overlooked the exact original VM after rename: %v", err)
	}
	delete(active, "/linode/instances/71")
	if err := archiveRemovedDeploymentGeneration(ctx, cfg, store, clusterID, verifyArchivedGenerationProviderAbsence); err != nil {
		t.Fatal("exact removal could not proceed to the formal archive", err)
	}
	if err := checkGenerationRemovalIdle(store.Root); err != nil {
		t.Fatal("archive retained the original generation's removal owner", err)
	}
}

func writeOriginalGenerationTestLedger(t *testing.T, cfg deploymentConfig, store secretStore, clusterID, kubeSHA string) {
	t.Helper()
	stack := cfg.Values["CLOUD_STACK_NAME"]
	write := func(relative string, value any) {
		t.Helper()
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := store.write(relative, raw, false); err != nil {
			t.Fatal(err)
		}
	}
	now := time.Now().UTC()
	plan := deploymentplan.Digest("original completed plan")
	provider := map[string]environmentCreateProviderReceipt{}
	for _, r := range []environmentCreateProviderReceipt{
		{Key: "lke", Kind: "lke", NativeID: clusterID, Name: stack + "-lke", Region: "us-sea"},
		{Key: "edge", Kind: "instance", NativeID: "71", Name: stack + "-edge-haproxy-01", Region: "us-sea"},
		{Key: "turn-01", Kind: "instance", NativeID: "72", Name: stack + "-turn01", Region: "us-sea"},
	} {
		provider[r.Key] = r
	}
	for _, target := range []deploymentStorageTarget{cfg.Storage.RuntimeMedia, cfg.Storage.OTAFirmware} {
		key := "bucket-" + target.Purpose
		provider[key] = environmentCreateProviderReceipt{Key: key, Kind: "bucket", NativeID: target.Region + "/" + target.Bucket, Name: target.Bucket, Region: target.Region}
	}
	ledger := environmentCreateResourceLedger{
		Schema: "rtk-cloud.resource-ledger/v1", Environment: "dev", Stack: stack, PlanSHA256: plan,
		Provider: provider, Kubernetes: map[string]string{}, Volumes: []environmentCreateVolumeOwner{}, UpdatedAt: now,
	}
	write("deployment/resources/"+plan+"/resources.json", ledger)
	write("deployment/resources/active.json", managedTransportVolumeLedgerActive{
		Schema: "rtk-cloud.resource-ledger-active/v1", Environment: "dev", Stack: stack, Plan: plan, LedgerSHA: deploymentplan.Digest(ledger),
	})
	journal := environmentCreateJournal{
		Schema: environmentCreateJournalSchema, Environment: "dev", Stack: stack, PlanSHA256: plan, ArtifactSHA256: deploymentplan.Digest("artifact"),
		AdmittedAt: now.Add(-time.Minute), CompletedAt: now,
	}
	complete := func(key string, receipt any) {
		raw, err := json.Marshal(receipt)
		if err != nil {
			t.Fatal(err)
		}
		journal.Entries = append(journal.Entries, environmentCreateJournalEntry{Key: key, Phase: "provider", State: "completed", StartedAt: journal.AdmittedAt, CompletedAt: now, Receipt: raw})
	}
	for key, r := range provider {
		complete(key, r)
	}
	complete("provider-kube-access", map[string]string{"cluster_id": clusterID, "kubeconfig_sha256": kubeSHA})
	write("deployment/create/executions/"+plan+"/execution-fixture/journal.json", journal)
}
