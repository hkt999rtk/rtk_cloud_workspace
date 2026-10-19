package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateTargetRequiresVerifiedFreshOrCompletelyRemovedGeneration(t *testing.T) {
	const first = "/lke/clusters?page_size=500&page=1"
	const second = "/lke/clusters?page_size=500&page=2"
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"fresh", "fresh-paginated", "removed", "completed-removed", "original-cluster-remains", "original-cluster-denied", "original-volume-remains", "original-volume-denied", "missing-original-cluster-binding", "foreign-volume-proof", "missing-token", "token-permissions", "canonical-kubeconfig", "active-provider-owner", "missing-active-provider-owner", "renamed-stack-owner", "provider-denied", "incomplete-inventory", "interrupted-owner", "lost-fence-incomplete-journal", "completed-history-without-removal", "foreign-journal"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				store := makeIsolatedTestSecretStore(t, environment)
				cfg := deploymentConfig{Environment: environment, Adapter: "lke", RuntimeRoot: t.TempDir(), Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-" + environment}}
				if err := store.write("operator/env/LINODE_TOKEN", []byte("private-selected-token"), true); err != nil {
					t.Fatal(err)
				}
				responses := map[string]string{first: `{"data":[],"page":1,"pages":0,"results":0}`}
				wantDiscovery := "GET " + first + "\n"
				wantNative := []string{}
				wantID := ""
				proof := deploymentRemovalVolumes{Schema: 1, Environment: environment, Stack: cfg.Values["CLOUD_STACK_NAME"], ClusterID: "123", KubeSHA256: deploymentplan.Digest("original-native-kubernetes-identity"), VolumeIDs: []string{"101", "102"}}
				removed := strings.Contains(mode, "removed") || strings.HasPrefix(mode, "original-") || strings.Contains(mode, "volume-proof") || mode == "missing-original-cluster-binding"
				if mode == "completed-history-without-removal" {
					removed = false
				}
				switch mode {
				case "fresh-paginated":
					responses[first] = `{"data":[{"id":456,"label":"foreign-one","tags":[]}],"page":1,"pages":2,"results":2}`
					responses[second] = `{"data":[{"id":789,"label":"foreign-two","tags":[]}],"page":2,"pages":2,"results":2}`
					wantDiscovery += "GET " + second + "\n"
				case "original-cluster-remains", "original-cluster-denied":
					wantID = "deployment.create.target"
				case "original-volume-remains", "original-volume-denied":
					wantID = "deployment.create.resources"
				case "missing-original-cluster-binding":
					proof.ClusterID = ""
					wantID, wantDiscovery = "deployment.create.target", ""
				case "foreign-volume-proof":
					proof.Environment = "foreign"
					wantID, wantDiscovery = "deployment.create.resources", ""
				case "missing-token":
					if err := os.Remove(filepath.Join(store.Root, "operator", "env", "LINODE_TOKEN")); err != nil {
						t.Fatal(err)
					}
					wantID, wantDiscovery = "deployment.create.target", ""
				case "token-permissions":
					if err := os.Chmod(filepath.Join(store.Root, "operator", "env", "LINODE_TOKEN"), 0644); err != nil {
						t.Fatal(err)
					}
					wantID, wantDiscovery = "deployment.create.target", ""
				case "canonical-kubeconfig":
					if err := store.write("kube/kubeconfig.yaml", []byte("private-retained-kubernetes-identity"), true); err != nil {
						t.Fatal(err)
					}
					wantID, wantDiscovery = "deployment.create.target", ""
				case "active-provider-owner", "missing-active-provider-owner":
					writeTestLKEState(t, environment, "LKE_CLUSTER_ID=123\n")
					responses["/lke/clusters/123"] = fmt.Sprintf(`{"id":123,"label":"%s-lke"}`, cfg.Values["CLOUD_STACK_NAME"])
					if mode == "missing-active-provider-owner" {
						responses["/lke/clusters/123"] = "__ERROR_404__"
					}
					wantID, wantDiscovery = "deployment.create.target", "GET /lke/clusters/123\n"
				case "renamed-stack-owner":
					responses[first] = fmt.Sprintf(`{"data":[{"id":123,"label":"renamed-owner","tags":["%s"]}],"page":1,"pages":1,"results":1}`, cfg.Values["CLOUD_STACK_NAME"])
					wantID = "deployment.create.target"
				case "provider-denied":
					responses[first] = "__ERROR_404__"
					wantID = "deployment.create.target"
				case "incomplete-inventory":
					responses[first] = `{"data":[],"page":1,"results":0}`
					wantID = "deployment.create.target"
				case "interrupted-owner":
					if err := os.MkdirAll(filepath.Join(store.Root, "deployment", ".execution-active"), 0700); err != nil {
						t.Fatal(err)
					}
					wantID, wantDiscovery = "deployment.create.checkpoint", ""
				case "lost-fence-incomplete-journal", "foreign-journal":
					wantID, wantDiscovery = "deployment.create.execution", ""
				case "completed-history-without-removal":
					wantID, wantDiscovery = "deployment.create.resources", ""
				}
				if removed {
					raw, _ := json.Marshal(proof)
					if err := store.write("deployment/remove-volume-ownership.json", raw, true); err != nil {
						t.Fatal(err)
					}
					if wantDiscovery != "" {
						wantNative = append(wantNative, "GET /lke/clusters/123")
						if !strings.HasPrefix(mode, "original-cluster-") {
							wantNative = append(wantNative, "GET /volumes/101")
							if !strings.HasPrefix(mode, "original-volume-") {
								wantNative = append(wantNative, "GET /volumes/102")
							}
						}
					}
				}
				if mode == "completed-removed" || mode == "completed-history-without-removal" || mode == "lost-fence-incomplete-journal" || mode == "foreign-journal" {
					sha := deploymentplan.Digest("original-create-stage-plan")
					now := time.Now().UTC()
					journal := environmentCreateJournal{Schema: environmentCreateJournalSchema, Environment: environment, Stack: cfg.Values["CLOUD_STACK_NAME"], PlanSHA256: sha, ArtifactSHA256: deploymentplan.Digest("original-stage-proof"), OwnerToken: deploymentplan.Digest("original-owner"), AdmittedAt: now.Add(-time.Hour), CompletedAt: now.Add(-time.Minute), Entries: []environmentCreateJournalEntry{}}
					if mode == "lost-fence-incomplete-journal" {
						journal.CompletedAt = time.Time{}
					}
					if mode == "foreign-journal" {
						journal.Environment = "foreign"
					}
					raw, _ := json.Marshal(journal)
					if err := store.write(filepath.Join("deployment", "create", "executions", sha, "execution-original", "journal.json"), raw, true); err != nil {
						t.Fatal(err)
					}
				}
				var nativeCalls []string
				native := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					nativeCalls = append(nativeCalls, r.Method+" "+r.URL.Path)
					if r.Header.Get("Authorization") != "Bearer private-selected-token" || r.Method != http.MethodGet {
						t.Error("native discovery used a foreign credential or mutation")
					}
					status := http.StatusNotFound
					if mode == "original-cluster-remains" && r.URL.Path == "/lke/clusters/123" || mode == "original-volume-remains" && r.URL.Path == "/volumes/101" {
						status = http.StatusOK
					}
					if mode == "original-cluster-denied" && r.URL.Path == "/lke/clusters/123" || mode == "original-volume-denied" && r.URL.Path == "/volumes/101" {
						status = http.StatusForbidden
					}
					w.WriteHeader(status)
					fmt.Fprint(w, `{"private-provider-payload":"must-not-escape"}`)
				}))
				defer native.Close()
				t.Setenv("RTK_CLOUD_LINODE_API_ROOT", native.URL)
				discoveryLog := fakeDeploymentPreflightLKERead(t, responses)
				t.Setenv("LKE_CLUSTER_ID", "999")
				t.Setenv("LKE_CLUSTER_LABEL", "foreign-ambient-target")
				t.Setenv("LINODE_TOKEN", "private-ambient-token")
				before := snapshotCreateTargetStore(t, store.Root)
				err := qualifyEnvironmentCreateTargetAbsence(context.Background(), cfg, store)
				if wantID == "" && err != nil {
					t.Fatal("verified fresh/removed generation remained blocked", err)
				}
				var blocker *deploymentplan.Blocker
				if wantID != "" && (!errors.As(err, &blocker) || blocker.ID != wantID) {
					t.Fatal("unsafe or uncertain target was not precisely blocked", err, wantID)
				}
				calls, _ := os.ReadFile(discoveryLog)
				if string(calls) != wantDiscovery || !reflect.DeepEqual(nativeCalls, wantNative) && !(len(nativeCalls) == 0 && len(wantNative) == 0) {
					t.Fatal("fresh target used a fallback, incomplete read or provider mutation", string(calls), wantDiscovery, nativeCalls, wantNative)
				}
				if !reflect.DeepEqual(before, snapshotCreateTargetStore(t, store.Root)) {
					t.Fatal("fresh discovery changed original identity, receipts or runtime state")
				}
				if err != nil && strings.Contains(err.Error(), "private-") || os.Getenv("LKE_CLUSTER_ID") != "999" || os.Getenv("LKE_CLUSTER_LABEL") != "foreign-ambient-target" || os.Getenv("LINODE_TOKEN") != "private-ambient-token" {
					t.Fatal("fresh target leaked private evidence or changed caller selection", err)
				}
			})
		}
	}
}

func snapshotCreateTargetStore(t *testing.T, root string) map[string]string {
	t.Helper()
	files := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if !entry.IsDir() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += ":" + string(raw)
		}
		files[path] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files
}
