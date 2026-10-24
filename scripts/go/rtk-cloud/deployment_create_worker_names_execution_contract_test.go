package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestWorkerNamingExecutionContractPreservesAllocationAndUnknownWriteHistory(t *testing.T) {
	for _, mode := range []string{"original", "already-named", "duplicate-class", "foreign-pool", "duplicate-worker", "empty-pool", "unknown-class", "label-collision", "wrong-read-owner", "lost-write-response", "changed-readback-ip"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, operatorSHA, artifact := createStateFixture(t, "dev")
			e, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operatorSHA, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer e.Close()
			pools := []deploymentplan.Object{
				{"id": 10, "label": "video-cloud-dev-general", "labels": deploymentplan.Object{"rtk.io/node-class": "general"}, "nodes": []any{deploymentplan.Object{"instance_id": 102}, deploymentplan.Object{"instance_id": 101}}},
				{"id": 20, "label": "video-cloud-dev-broker", "labels": deploymentplan.Object{"rtk.io/node-class": "broker"}, "nodes": []any{deploymentplan.Object{"instance_id": 201}}},
			}
			instances := map[int]deploymentplan.Object{}
			for _, id := range []int{101, 102, 201} {
				instances[id] = deploymentplan.Object{"id": id, "label": fmt.Sprintf("allocated-original-%d", id), "ipv4": []any{fmt.Sprintf("192.0.2.%d", id%100+1)}}
			}
			if mode == "already-named" {
				instances[101]["label"] = "video-cloud-dev-lke-general-01"
			}
			switch mode {
			case "duplicate-class":
				pools = append(pools, pools[0])
			case "foreign-pool":
				pools[0]["label"] = "video-cloud-staging-general"
			case "duplicate-worker":
				pools[1]["nodes"] = []any{deploymentplan.Object{"instance_id": 101}}
			case "empty-pool":
				pools = nil
			case "unknown-class":
				pools[0]["label"] = "video-cloud-dev-foreign"
				pools[0]["labels"] = deploymentplan.Object{"rtk.io/node-class": "foreign"}
			case "label-collision":
				instances[999] = deploymentplan.Object{"id": 999, "label": "video-cloud-dev-lke-general-01"}
			}
			var mu sync.Mutex
			var requests []string
			writes := map[int]int{}
			var protocolError string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				requests = append(requests, r.Method+" "+r.URL.Path)
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/lke/clusters/77/pools":
					_ = json.NewEncoder(w).Encode(map[string]any{"data": pools, "page": 1, "pages": 1, "results": len(pools)})
				case "/linode/instances":
					var all []deploymentplan.Object
					for _, instance := range instances {
						all = append(all, instance)
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"data": all, "page": 1, "pages": 1, "results": len(all)})
				default:
					id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/linode/instances/"))
					object := instances[id]
					if err != nil || object == nil {
						http.NotFound(w, r)
						return
					}
					if r.Method == "PUT" {
						var body map[string]string
						if err := json.NewDecoder(r.Body).Decode(&body); err != nil || len(body) != 1 || body["label"] == "" {
							protocolError = "mutation exceeded display-label scope"
							http.Error(w, "invalid label request", http.StatusBadRequest)
							return
						}
						var binding environmentWorkerNames
						if readExecutionJSON(filepath.Join(store.Root, "deployment/adapters/lke/worker-labels.json"), &binding) != nil || len(binding.Entries) != 3 {
							protocolError = "original ID and ordinal bindings were not durable before rename"
						}
						entry := createOpenBaoACKCompletionEntry(e.Journal, fmt.Sprintf("worker-label-%d", id))
						if entry == nil || entry.State != "pending" || len(entry.Receipt) != 0 {
							protocolError = "rename was not preceded by an original pending intent"
						}
						writes[id]++
						object["label"] = body["label"]
						if mode == "lost-write-response" {
							http.Error(w, "isolated response loss after rename", http.StatusServiceUnavailable)
							return
						}
					} else if r.Method != "GET" {
						protocolError = "unexpected provider mutation"
						http.Error(w, "unexpected method", http.StatusBadRequest)
						return
					}
					copy := deploymentplan.Clone(object)
					if mode == "wrong-read-owner" {
						copy["id"] = 999
					}
					if mode == "changed-readback-ip" && writes[id] != 0 && r.Method == "GET" {
						copy["ipv4"] = []any{"198.51.100.99"}
					}
					_ = json.NewEncoder(w).Encode(copy)
				}
			}))
			defer server.Close()
			t.Setenv("RTK_CLOUD_LINODE_API_ROOT", server.URL)
			cluster := environmentCreateProviderReceipt{Kind: "lke", Name: "video-cloud-dev-lke", NativeID: "77"}
			operator := map[string]string{"LINODE_TOKEN": "isolated-naming-token"}
			err = initializeEnvironmentCreateWorkerNames(context.Background(), cfg, store, operator, cluster, e)
			success := mode == "original" || mode == "already-named"
			if (err == nil) != success {
				t.Fatal("unexpected original worker naming result", err)
			}
			mu.Lock()
			if protocolError != "" {
				t.Fatal(protocolError)
			}
			writeCount := 0
			for _, count := range writes {
				writeCount += count
				if count != 1 {
					t.Fatal("worker rename replayed")
				}
			}
			mu.Unlock()
			if success {
				expectedWrites := 3
				if mode == "already-named" {
					expectedWrites = 2
				}
				if writeCount != expectedWrites {
					t.Fatal("unchanged worker label was rewritten or a rename was omitted", writeCount)
				}
				var bindings environmentWorkerNames
				if err := readExecutionJSON(filepath.Join(store.Root, "deployment/adapters/lke/worker-labels.json"), &bindings); err != nil {
					t.Fatal(err)
				}
				if bindings.ClusterID != "77" || len(bindings.Entries) != 3 || bindings.Entries[0].InstanceID != 101 || bindings.Entries[1].InstanceID != 102 || bindings.Entries[0].Ordinal != 1 || bindings.Entries[1].Ordinal != 2 {
					t.Fatal("worker ordinals were not deterministic by original native ID")
				}
				for _, key := range []string{"provider-worker-names", "worker-label-101", "worker-label-102", "worker-label-201"} {
					entry := createOpenBaoACKCompletionEntry(e.Journal, key)
					if entry == nil || entry.State != "completed" || len(entry.Receipt) == 0 {
						t.Fatal("verified rename lacks durable completion", key)
					}
				}
			}
			bindingPath := filepath.Join(store.Root, "deployment/adapters/lke/worker-labels.json")
			if _, statErr := os.Stat(bindingPath); statErr == nil {
				before := createBootstrapStateSnapshot(t, store.Root)
				mu.Lock()
				previousRequests := len(requests)
				mu.Unlock()
				if err := initializeEnvironmentCreateWorkerNames(context.Background(), cfg, store, operator, cluster, e); err == nil {
					t.Fatal("original naming intent was silently replayed")
				}
				mu.Lock()
				unchanged := len(requests) == previousRequests
				mu.Unlock()
				if !unchanged || !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, store.Root)) {
					t.Fatal("naming retry changed original bindings or issued another provider request")
				}
			} else if !os.IsNotExist(statErr) || writeCount != 0 {
				t.Fatal("unqualified worker inventory reached a provider mutation")
			}
		})
	}
}
