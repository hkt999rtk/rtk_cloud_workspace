package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"sync"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestStagingGeneralCapacityQualificationPreservesAllocationAcrossRecovery(t *testing.T) {
	for _, mode := range []string{"original", "lost-response-applied", "lost-response-unapplied", "changed-member", "unrecorded-expansion", "changed-workload-owner", "unbound-claim", "quota-exhausted"} {
		t.Run(mode, func(t *testing.T) {
			ns := "video-cloud-staging-platform"
			uid := "11111111-1111-4111-8111-111111111111"
			controller := stagingQualificationObject("StatefulSet", ns, "fleet-valkey", uid, deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"nodeSelector": deploymentplan.Object{"rtk.io/node-class": "general"}}}})
			id := deploymentplan.ID(controller)
			f := newStagingWorkloadQualificationFixture(t, []deploymentplan.Resource{{ID: id, UID: uid, Phase: "workload", Action: "update", Baseline: controller, Desired: controller}})
			f.journal.Completed = []string{id}
			f.journal.Owners[id] = uid
			f.saveProgress(t, f.child, f.journal)
			f.parent.Provider.Resources = []environmentCreateProviderResource{{Key: "lke", Body: deploymentplan.Object{"node_pools": []any{deploymentplan.Object{"label": f.parent.Stack + "-general", "count": float64(2), "type": "g6-standard-4"}}}}}
			workers := environmentWorkerNames{Schema: "rtk-cloud.lke-worker-labels/v1", Environment: "staging", Stack: f.parent.Stack, ClusterID: "77", Entries: []environmentWorkerName{{PoolID: 23, InstanceID: 101, NodeClass: "general"}, {PoolID: 23, InstanceID: 102, NodeClass: "general"}, {PoolID: 24, InstanceID: 201, NodeClass: "broker"}}}
			workerRaw, _ := json.Marshal(workers)
			if err := f.store.write("deployment/adapters/lke/worker-labels.json", workerRaw, false); err != nil {
				t.Fatal(err)
			}
			path, err := deploymentLKEStatePath("staging", "account.env")
			if err != nil {
				t.Fatal(err)
			}
			rel, err := filepath.Rel(f.store.Root, path)
			if err != nil {
				t.Fatal(err)
			}
			quota := "4"
			if mode == "quota-exhausted" {
				quota = "3"
			}
			if err := f.store.write(rel, []byte("LKE_ACTIVE_SERVICE_LIMIT="+quota+"\n"), false); err != nil {
				t.Fatal(err)
			}
			pod := stagingQualificationObject("Pod", ns, "fleet-valkey-0", "22222222-2222-4222-8222-222222222222", deploymentplan.Object{})
			deploymentplan.Map(pod["metadata"])["ownerReferences"] = []any{deploymentplan.Object{"kind": "StatefulSet", "name": "fleet-valkey", "controller": true, "uid": uid}}
			pod["status"] = deploymentplan.Object{"conditions": []any{deploymentplan.Object{"type": "PodScheduled", "status": "False", "reason": "Unschedulable", "message": "0/3 nodes are available: 2 node(s) exceed max volume count"}}}
			claim := stagingQualificationObject("PersistentVolumeClaim", ns, "data-fleet-valkey-0", "33333333-3333-4333-8333-333333333333", deploymentplan.Object{"volumeName": "original-fleet-volume"})
			claim["status"] = deploymentplan.Object{"phase": "Bound"}
			if mode == "changed-workload-owner" {
				deploymentplan.Map(controller["metadata"])["uid"] = "44444444-4444-4444-8444-444444444444"
			}
			if mode == "unbound-claim" {
				claim["status"] = deploymentplan.Object{"phase": "Pending"}
			}
			backend := stagingQualificationBackend(t, f.store, map[string]deploymentplan.Object{id: controller, deploymentplan.ID(pod): pod, deploymentplan.ID(claim): claim})
			pool := stagingGeneralCapacityPool{lkeNodePool: lkeNodePool{ID: 23, Type: "g6-standard-4", Count: 2, Label: f.parent.Stack + "-general", Labels: map[string]string{"rtk.io/node-class": "general"}}, Nodes: []stagingGeneralCapacityPoolNode{{ID: 101}, {ID: 102}}}
			if mode == "changed-member" {
				pool.Nodes[0].ID = 901
			}
			if mode == "unrecorded-expansion" {
				pool.Count = 3
				pool.Nodes = append(pool.Nodes, stagingGeneralCapacityPoolNode{ID: 103})
			}
			var mu sync.Mutex
			puts := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/linode/instances", "/volumes", "/nodebalancers":
					if r.Method != "GET" {
						t.Errorf("unexpected quota mutation: %s", r.Method)
					}
					_ = json.NewEncoder(w).Encode(deploymentplan.Object{"data": []any{deploymentplan.Object{"id": 1, "label": "original-quota-resource"}}, "page": 1, "pages": 1, "results": 1})
				case "/lke/clusters/77/pools/23":
					if r.Method == "PUT" {
						puts++
						var body map[string]int
						if json.NewDecoder(r.Body).Decode(&body) != nil || !reflect.DeepEqual(body, map[string]int{"count": 3}) {
							t.Error("provider mutation exceeded one absolute capacity target")
						}
						entry := createOpenBaoACKCompletionEntry(f.e.Journal, stagingGeneralCapacityEntry)
						if entry == nil || entry.State != "pending" || len(entry.Receipt) != 0 {
							t.Error("provider mutation preceded durable original intent")
						}
						if mode != "lost-response-unapplied" {
							pool.Count = 3
							pool.Nodes = append(pool.Nodes, stagingGeneralCapacityPoolNode{ID: 103})
						}
						if mode == "lost-response-applied" || mode == "lost-response-unapplied" {
							http.Error(w, "isolated lost response", http.StatusBadRequest)
							return
						}
					} else if r.Method != "GET" {
						t.Errorf("unexpected provider mutation: %s", r.Method)
					}
					_ = json.NewEncoder(w).Encode(pool)
				default:
					t.Errorf("unexpected provider path %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			t.Setenv("RTK_CLOUD_LINODE_API_ROOT", server.URL)
			run := func() error {
				return resumeStagingGeneralVolumeCapacity(t.Context(), f.cfg, f.store, &f.parent, map[string]environmentCreateProviderReceipt{"lke": {NativeID: "77", Name: f.parent.Stack + "-lke"}}, map[string]string{"LINODE_TOKEN": "isolated-token"}, f.e, backend)
			}
			err = run()
			if mode == "original" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil {
				t.Fatal("unsafe or interrupted capacity change reported success")
			}
			if mode == "lost-response-applied" || mode == "lost-response-unapplied" {
				entry := createOpenBaoACKCompletionEntry(f.e.Journal, stagingGeneralCapacityEntry)
				if entry == nil || entry.State != "pending" {
					t.Fatal("uncertain provider write lost its pending intent")
				}
				before := deploymentplan.Digest(*entry)
				err = run()
				if mode == "lost-response-applied" {
					if err != nil {
						t.Fatal("native readback could not finish original request", err)
					}
				} else {
					if err == nil || deploymentplan.Digest(*createOpenBaoACKCompletionEntry(f.e.Journal, stagingGeneralCapacityEntry)) != before {
						t.Fatal("unknown provider outcome was replayed or rewritten")
					}
				}
			}
			if mode == "original" || mode == "lost-response-applied" {
				entry := createOpenBaoACKCompletionEntry(f.e.Journal, stagingGeneralCapacityEntry)
				if entry == nil || entry.State != "completed" {
					t.Fatal("verified capacity did not finish original request")
				}
				var receipt stagingGeneralCapacityIntent
				if json.Unmarshal(entry.Receipt, &receipt) != nil || receipt.Count != 3 || receipt.ClusterID != "77" || receipt.PoolID != 23 || !reflect.DeepEqual(receipt.Members, []int{101, 102}) {
					t.Fatal("capacity receipt lost original allocation")
				}
				before := deploymentplan.Digest(*entry)
				if err := run(); err != nil {
					t.Fatal("completed capacity checkpoint did not verify", err)
				}
				if deploymentplan.Digest(*createOpenBaoACKCompletionEntry(f.e.Journal, stagingGeneralCapacityEntry)) != before {
					t.Fatal("completed checkpoint was rewritten")
				}
			}
			mu.Lock()
			writes := puts
			mu.Unlock()
			wantWrites := 0
			if mode == "original" || mode == "lost-response-applied" || mode == "lost-response-unapplied" {
				wantWrites = 1
			}
			if writes != wantWrites {
				t.Fatalf("provider PUTs = %d, want %d", writes, wantWrites)
			}
			var after environmentWorkerNames
			if readExecutionJSON(filepath.Join(f.store.Root, "deployment/adapters/lke/worker-labels.json"), &after) != nil || !reflect.DeepEqual(after, workers) {
				t.Fatal("original worker allocation was overwritten")
			}
		})
	}
}
