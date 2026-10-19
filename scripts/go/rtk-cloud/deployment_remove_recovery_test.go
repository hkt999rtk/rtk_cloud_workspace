package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io/fs"
	"math/big"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type removalRecoveryTestFixture struct {
	config      deploymentConfig
	store       secretStore
	artifact    string
	kube        string
	qualifiedAt time.Time
}

type removalRecoveryTestInventory struct {
	volumes       []map[string]any
	clusters      []map[string]any
	nodes         []map[string]any
	events        map[string][]map[string]any
	calls         []string
	incomplete    bool
	detailErr     error
	detailVolumes map[string]map[string]any
}

func recoveryRemovalInventory(fixture removalRecoveryTestFixture) *removalRecoveryTestInventory {
	entity := func(kind, id, label string) map[string]any {
		path := "/v4/volumes/" + id
		if kind == "linode" {
			path = "/v4/linode/instances/" + id
		}
		return map[string]any{"type": kind, "id": id, "label": label, "url": path}
	}
	result := &removalRecoveryTestInventory{events: map[string][]map[string]any{}}
	node := entity("linode", "42", "lke123-general-original")
	for i, binding := range []struct{ id, label string }{{"101", "pvc-1111111122223333"}, {"202", "pvc-aaaaaaaabbbbcccc"}} {
		result.volumes = append(result.volumes, map[string]any{"id": 101 + i*101, "label": binding.label, "region": "us-sea", "status": "active", "linode_id": nil, "created": fixture.qualifiedAt.Add(-2 * time.Hour).Format(time.RFC3339)})
		result.events["volume/"+binding.id] = []map[string]any{{"id": i + 1, "created": fixture.qualifiedAt.Add(-time.Hour).Format(time.RFC3339), "action": "volume_attach", "status": "finished", "entity": entity("volume", binding.id, binding.label), "secondary_entity": node}}
	}
	result.volumes = append(result.volumes, map[string]any{"id": 999, "label": "pvc-unrelated-volume", "region": "us-sea", "status": "active", "linode_id": nil, "created": fixture.qualifiedAt.Add(-2 * time.Hour).Format(time.RFC3339)})
	result.events["linode/42"] = []map[string]any{{"id": 3, "created": fixture.qualifiedAt.Add(time.Hour).Format(time.RFC3339), "action": "linode_delete", "status": "finished", "entity": node}}
	return result
}

func (inventory *removalRecoveryTestInventory) reader(t *testing.T) removalRecoveryReader {
	t.Helper()
	return func(ctx context.Context, token, path string, filter map[string]any) ([]byte, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if token != "isolated-recovery-token" {
			return nil, fmt.Errorf("unexpected token: credentials must come from selected store")
		}
		inventory.calls = append(inventory.calls, path)
		endpoint, _, _ := strings.Cut(path, "?")
		data := []map[string]any{}
		switch endpoint {
		case "/volumes":
			data = inventory.volumes
		case "/lke/clusters":
			data = inventory.clusters
		case "/linode/instances":
			data = inventory.nodes
		case "/account/events":
			if filter == nil {
				return nil, fmt.Errorf("events must have an exact entity filter")
			}
			data = inventory.events[fmt.Sprint(filter["entity.type"])+"/"+fmt.Sprint(filter["entity.id"])]
		default:
			if id, ok := strings.CutPrefix(endpoint, "/volumes/"); ok {
				if inventory.detailErr != nil {
					return nil, inventory.detailErr
				}
				if detail := inventory.detailVolumes[id]; detail != nil {
					return json.Marshal(detail)
				}
				for _, volume := range inventory.volumes {
					if fmt.Sprint(volume["id"]) == id {
						return json.Marshal(volume)
					}
				}
				return nil, errRemovalRecoveryNativeNotFound
			}
			return nil, fmt.Errorf("unexpected recovery API endpoint %q", endpoint)
		}
		if data == nil {
			data = []map[string]any{}
		}
		results := len(data)
		if inventory.incomplete {
			results++
		}
		return json.Marshal(map[string]any{"page": 1, "pages": 1, "results": results, "data": data})
	}
}

func (fixture removalRecoveryTestFixture) request() removalRecoveryRequest {
	return removalRecoveryRequest{SourcePreflight: fixture.artifact, SourceKubeconfig: fixture.kube, ClusterID: "123", NodeIDs: []string{"42"}}
}

func approveRemovalRecoveryTestReview(t *testing.T, fixture removalRecoveryTestFixture, request removalRecoveryRequest, read removalRecoveryReader) removalRecoveryRequest {
	t.Helper()
	var output bytes.Buffer
	if err := reconcileDeploymentRemovalVolumes(context.Background(), fixture.config, request, read, &output); err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(output.String(), "\n") {
		if value, ok := strings.CutPrefix(strings.TrimSpace(line), "review-sha256:"); ok {
			request.ReviewSHA256 = strings.TrimSpace(value)
		}
	}
	if !environmentPreflightDigest.MatchString(request.ReviewSHA256) {
		t.Fatalf("review omitted its exact candidate SHA-256: %s", output.String())
	}
	request.Confirm = fixture.config.Values["CLOUD_STACK_NAME"]
	return request
}

func TestDeploymentRemovalRecoveryReviewUsesOriginalEvidenceWithoutWrites(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			fixture := deploymentRemovalRecoveryFixture(t, environment)
			inventory := recoveryRemovalInventory(fixture)
			before := snapshotRemovalRecoveryStore(t, fixture.store.Root)
			var output bytes.Buffer
			if err := reconcileDeploymentRemovalVolumes(context.Background(), fixture.config, fixture.request(), inventory.reader(t), &output); err != nil {
				t.Fatal(err)
			}
			if after := snapshotRemovalRecoveryStore(t, fixture.store.Root); !reflect.DeepEqual(before, after) {
				t.Fatal("review changed the canonical private store")
			}
			if len(inventory.calls) == 0 {
				t.Fatal("review did not verify live inventory and event evidence")
			}
			if !strings.Contains(output.String(), "101") || !strings.Contains(output.String(), "202") {
				t.Fatalf("review omitted exact original volume mappings: %s", output.String())
			}
			if _, known, err := readDeploymentRemovalVolumes(fixture.config); err != nil || known {
				t.Fatalf("review published ownership: %v %v", known, err)
			}
		})
	}
}

func TestDeploymentRemovalRecoveryPublishesOnlyReviewedOriginalIDs(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			fixture := deploymentRemovalRecoveryFixture(t, environment)
			inventory := recoveryRemovalInventory(fixture)
			request := approveRemovalRecoveryTestReview(t, fixture, fixture.request(), inventory.reader(t))
			var output bytes.Buffer
			if err := reconcileDeploymentRemovalVolumes(context.Background(), fixture.config, request, inventory.reader(t), &output); err != nil {
				t.Fatal(err)
			}
			proof, known, err := readDeploymentRemovalVolumes(fixture.config)
			if err != nil || !known || !reflect.DeepEqual(proof.VolumeIDs, []string{"101", "202"}) {
				t.Fatalf("recovery did not publish exact reviewed IDs: %v %v %v", proof.VolumeIDs, known, err)
			}
			path := filepath.Join(fixture.store.Root, "deployment", "remove-volume-ownership.json")
			if info, err := os.Lstat(path); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
				t.Fatalf("recovery proof was not a private regular file: %v %v", info, err)
			}
			if proof.Environment != environment || proof.Stack != fixture.config.Values["CLOUD_STACK_NAME"] || proof.ClusterID != "123" {
				t.Fatalf("recovery lost selected environment and original cluster: %+v", proof)
			}
		})
	}
}

func TestDeploymentRemovalRecoveryRequiresReviewedNodeSetWhenArchiveStateIsAbsent(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			fixture := deploymentRemovalRecoveryFixture(t, environment)
			path := filepath.Join(filepath.Dir(filepath.Dir(fixture.kube)), "deployment", "adapters", "lke", "state.env")
			if err := os.Remove(path); err != nil {
				t.Fatal(err)
			}
			inventory := recoveryRemovalInventory(fixture)
			request := approveRemovalRecoveryTestReview(t, fixture, fixture.request(), inventory.reader(t))
			var output bytes.Buffer
			if err := reconcileDeploymentRemovalVolumes(context.Background(), fixture.config, request, inventory.reader(t), &output); err != nil {
				t.Fatal(err)
			}
			proof, known, err := readDeploymentRemovalVolumes(fixture.config)
			if err != nil || !known || proof.Schema != 2 || proof.Recovery == nil || proof.Recovery.ClusterBinding != "explicit-reviewed-node-set" || !reflect.DeepEqual(proof.VolumeIDs, []string{"101", "202"}) {
				t.Fatalf("explicit reviewed recovery was lost or claimed a stronger source: %+v %v %v", proof, known, err)
			}
			if _, err := os.Stat(path); !os.IsNotExist(err) {
				t.Fatalf("recovery invented missing archived provider state: %v", err)
			}
		})
	}
}

func TestDeploymentRemovalRecoveryRejectsChangedCandidateAfterReview(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			fixture := deploymentRemovalRecoveryFixture(t, environment)
			inventory := recoveryRemovalInventory(fixture)
			request := approveRemovalRecoveryTestReview(t, fixture, fixture.request(), inventory.reader(t))
			inventory.events["volume/101"][0]["id"] = 30
			before := snapshotRemovalRecoveryStore(t, fixture.store.Root)
			var output bytes.Buffer
			if err := reconcileDeploymentRemovalVolumes(context.Background(), fixture.config, request, inventory.reader(t), &output); err == nil {
				t.Fatal("a different original attachment event consumed an earlier review approval")
			}
			if after := snapshotRemovalRecoveryStore(t, fixture.store.Root); !reflect.DeepEqual(before, after) {
				t.Fatal("candidate drift published recovery evidence")
			}
		})
	}
}

func TestDeploymentRemovalRecoveryApprovalKeepsUnrelatedVolumesOutsideScope(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			fixture := deploymentRemovalRecoveryFixture(t, environment)
			inventory := recoveryRemovalInventory(fixture)
			request := approveRemovalRecoveryTestReview(t, fixture, fixture.request(), inventory.reader(t))
			inventory.volumes = append(inventory.volumes, map[string]any{"id": 888, "label": "other-environment-volume", "region": "us-sea", "status": "active", "linode_id": nil, "created": fixture.qualifiedAt.Add(-time.Hour).Format(time.RFC3339)})
			var output bytes.Buffer
			if err := reconcileDeploymentRemovalVolumes(context.Background(), fixture.config, request, inventory.reader(t), &output); err != nil {
				t.Fatal(err)
			}
			proof, known, err := readDeploymentRemovalVolumes(fixture.config)
			if err != nil || !known || !reflect.DeepEqual(proof.VolumeIDs, []string{"101", "202"}) {
				t.Fatalf("unrelated inventory widened or invalidated the reviewed recovery scope: %v %v %v", proof.VolumeIDs, known, err)
			}
		})
	}
}

func TestDeploymentRemovalRecoveryConsumerRejectsChangedSourcesOrScope(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"source-plan", "source-proof", "source-bindings", "source-release", "source-kube", "source-state", "source-sha-field", "append-volume", "mapping-volume", "review-sha", "recovery-method", "scope", "nil-recovery", "cluster-binding", "node-set", "future-node-delete"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				fixture := deploymentRemovalRecoveryFixture(t, environment)
				inventory := recoveryRemovalInventory(fixture)
				request := approveRemovalRecoveryTestReview(t, fixture, fixture.request(), inventory.reader(t))
				var output bytes.Buffer
				if err := reconcileDeploymentRemovalVolumes(context.Background(), fixture.config, request, inventory.reader(t), &output); err != nil {
					t.Fatal(err)
				}
				proof, known, err := readDeploymentRemovalVolumes(fixture.config)
				if err != nil || !known {
					t.Fatalf("unchanged recovery is not consumable: %v %v", known, err)
				}
				if strings.HasPrefix(mode, "source-") && mode != "source-sha-field" {
					file := strings.TrimPrefix(mode, "source-") + ".json"
					path := filepath.Join(fixture.artifact, file)
					if mode == "source-kube" {
						path = fixture.kube
					}
					if mode == "source-state" {
						path = filepath.Join(filepath.Dir(filepath.Dir(fixture.kube)), "deployment", "adapters", "lke", "state.env")
					}
					if err := os.WriteFile(path, append([]byte(readTestFile(t, path)), '\n'), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					switch mode {
					case "source-sha-field":
						for key := range proof.Recovery.SourceSHA256 {
							proof.Recovery.SourceSHA256[key] = strings.Repeat("f", 64)
							break
						}
					case "append-volume":
						proof.VolumeIDs = append(proof.VolumeIDs, "999")
					case "mapping-volume":
						proof.Recovery.Mappings[0].VolumeID = "999"
					case "review-sha":
						proof.Recovery.ReviewSHA256 = strings.Repeat("f", 64)
					case "recovery-method":
						proof.Recovery.Method = "csi-capture"
					case "scope":
						proof.Recovery.Scope = "all-provider-resources"
					case "nil-recovery":
						proof.Recovery = nil
					case "cluster-binding":
						proof.Recovery.ClusterBinding = ""
					case "node-set":
						proof.Recovery.Nodes[0].ID = "999"
					case "future-node-delete":
						proof.Recovery.Nodes[0].DeletedAt = time.Now().UTC().Add(time.Hour)
						proof.Recovery.ReviewSHA256 = removalRecoveryReviewSHA(*proof.Recovery)
						if removalRecoveryReviewSHA(*proof.Recovery) != proof.Recovery.ReviewSHA256 {
							t.Fatal("future deletion fixture does not have a valid payload digest")
						}
					}
					raw, err := json.Marshal(proof)
					if err != nil {
						t.Fatal(err)
					}
					if err := fixture.store.write("deployment/remove-volume-ownership.json", raw, true); err != nil {
						t.Fatal(err)
					}
				}
				before := snapshotRemovalRecoveryStore(t, fixture.store.Root)
				if _, known, err := readDeploymentRemovalVolumes(fixture.config); err == nil || known {
					t.Fatalf("%s source or proof change remained consumable: %v %v", mode, known, err)
				}
				if after := snapshotRemovalRecoveryStore(t, fixture.store.Root); !reflect.DeepEqual(before, after) {
					t.Fatal("rejected consumer validation repaired or replaced recovery metadata")
				}
			})
		}
	}
}

func TestDeploymentRemovalRecoveryOptionalReportIsPrivateAndScoped(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			fixture := deploymentRemovalRecoveryFixture(t, environment)
			inventory := recoveryRemovalInventory(fixture)
			request := fixture.request()
			request.Report = filepath.Join(fixture.store.Root, "deployment", "reports", "removal-recovery-review.json")
			var output bytes.Buffer
			if err := reconcileDeploymentRemovalVolumes(context.Background(), fixture.config, request, inventory.reader(t), &output); err != nil {
				t.Fatal(err)
			}
			if info, err := os.Lstat(request.Report); err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
				t.Fatalf("review report is not a private regular file: %v %v", info, err)
			}
			if info, err := os.Stat(filepath.Dir(request.Report)); err != nil || info.Mode().Perm() != 0700 {
				t.Fatalf("review report directory is not private: %v %v", info, err)
			}
			body := readTestFile(t, request.Report)
			var report struct {
				Environment  string                    `json:"environment"`
				ReviewSHA256 string                    `json:"review_sha256"`
				Recovery     deploymentRemovalRecovery `json:"recovery"`
			}
			if err := json.Unmarshal([]byte(body), &report); err != nil || report.Environment != environment || !environmentPreflightDigest.MatchString(report.ReviewSHA256) || report.Recovery.Scope != "qualified-original-PVC-inventory" || len(report.Recovery.Mappings) != 2 {
				t.Fatalf("report omits the exact scoped reviewed candidate: %v %s", err, body)
			}
			for _, secret := range []string{"isolated-recovery-token", "foreign-process-token-must-not-be-used", "isolated-recovery-kubernetes-token", "BEGIN CERTIFICATE"} {
				if strings.Contains(body, secret) {
					t.Fatalf("report contains private credential material %q", secret)
				}
			}
			if _, known, err := readDeploymentRemovalVolumes(fixture.config); err != nil || known {
				t.Fatalf("review report published removal ownership: %v %v", known, err)
			}
		})
	}
}

func TestDeploymentRemovalRecoveryRejectsReportOutsideSelectedStore(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			fixture := deploymentRemovalRecoveryFixture(t, environment)
			inventory := recoveryRemovalInventory(fixture)
			request := fixture.request()
			request.Report = filepath.Join(t.TempDir(), "outside-review.json")
			before := snapshotRemovalRecoveryStore(t, fixture.store.Root)
			var output bytes.Buffer
			if err := reconcileDeploymentRemovalVolumes(context.Background(), fixture.config, request, inventory.reader(t), &output); err == nil {
				t.Fatal("review report outside selected canonical store was accepted")
			}
			if _, err := os.Stat(request.Report); !os.IsNotExist(err) {
				t.Fatalf("foreign report was written: %v", err)
			}
			if after := snapshotRemovalRecoveryStore(t, fixture.store.Root); !reflect.DeepEqual(before, after) {
				t.Fatal("invalid report path changed private evidence")
			}
		})
	}
}

func TestDeploymentRemovalRecoveryNativeConsumerUsesExactIDsAndConfirmedAbsence(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"unchanged", "already-absent", "permission-not-absence", "lookup-present-but-list-absent", "renamed", "wrong-region", "created-drift", "attached", "status", "new-cluster", "original-node", "later-foreign-attachment"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				fixture := deploymentRemovalRecoveryFixture(t, environment)
				inventory := recoveryRemovalInventory(fixture)
				request := approveRemovalRecoveryTestReview(t, fixture, fixture.request(), inventory.reader(t))
				var output bytes.Buffer
				if err := reconcileDeploymentRemovalVolumes(context.Background(), fixture.config, request, inventory.reader(t), &output); err != nil {
					t.Fatal(err)
				}
				proof, known, err := readDeploymentRemovalVolumes(fixture.config)
				if err != nil || !known {
					t.Fatalf("unchanged recovery is not consumable: %v %v", known, err)
				}
				switch mode {
				case "already-absent", "permission-not-absence", "lookup-present-but-list-absent":
					original := inventory.volumes[0]
					inventory.volumes = inventory.volumes[2:]
					if mode == "permission-not-absence" {
						inventory.detailErr = errors.New("403 selected credentials no longer authorized")
					}
					if mode == "lookup-present-but-list-absent" {
						inventory.detailVolumes = map[string]map[string]any{"101": original}
					}
				case "renamed":
					inventory.volumes[0]["label"] = "pvc-new-name"
				case "wrong-region":
					inventory.volumes[0]["region"] = "us-east"
				case "created-drift":
					inventory.volumes[0]["created"] = fixture.qualifiedAt.Add(-3 * time.Hour).Format(time.RFC3339)
				case "attached":
					inventory.volumes[0]["linode_id"] = 999
				case "status":
					inventory.volumes[0]["status"] = "creating"
				case "new-cluster":
					inventory.clusters = []map[string]any{{"id": 456, "label": fixture.config.Values["CLOUD_STACK_NAME"] + "-lke", "tags": []string{}}}
				case "original-node":
					inventory.nodes = []map[string]any{{"id": 42, "label": "lke123-general-original", "tags": []string{}}}
				case "later-foreign-attachment":
					inventory.events["volume/101"] = append(inventory.events["volume/101"], map[string]any{"id": 4, "created": fixture.qualifiedAt.Add(2 * time.Hour).Format(time.RFC3339), "action": "volume_attach", "status": "finished", "entity": inventory.events["volume/101"][0]["entity"], "secondary_entity": map[string]any{"type": "linode", "id": "999", "label": "lke456-foreign", "url": "/v4/linode/instances/999"}})
				}
				before := snapshotRemovalRecoveryStore(t, fixture.store.Root)
				err = validateRecoveredRemovalTarget(context.Background(), fixture.config, proof, inventory.reader(t))
				if mode == "unchanged" || mode == "already-absent" {
					if err != nil {
						t.Fatalf("exact unchanged/confirmed-absent IDs were rejected: %v", err)
					}
				} else if err == nil {
					t.Fatalf("%s drift was allowed to consume original removal ownership", mode)
				}
				if mode == "already-absent" {
					calls := strings.Join(inventory.calls, "\n")
					if !strings.Contains(calls, "/volumes/101") || !strings.Contains(calls, "/volumes/202") {
						t.Fatalf("absent IDs were not independently confirmed by exact-ID lookups: %s", calls)
					}
				}
				if after := snapshotRemovalRecoveryStore(t, fixture.store.Root); !reflect.DeepEqual(before, after) {
					t.Fatal("native consumer validation changed local ownership or source evidence")
				}
			})
		}
	}
}

func TestDeploymentRemovalRecoveryRejectsMissingAmbiguousOrDriftedEvidence(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"missing-proof", "foreign-environment", "changed-plan", "changed-bindings", "changed-kubeconfig", "missing-checker", "invalid-checker-version", "archive-cluster", "archive-environment", "current-kube-generation", "current-state-generation", "duplicate-nodes", "foreign-confirm", "missing-review-sha", "wrong-review-sha", "missing-token", "incomplete-pagination", "duplicate-volume-id", "duplicate-volume-label", "wrong-region", "attached-volume", "current-cluster", "current-node", "missing-attachment", "missing-delete", "future-node-delete", "unfinished-attachment", "wrong-attachment-label", "wrong-attachment-url", "wrong-original-node", "later-foreign-attachment", "duplicate-event"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				fixture := deploymentRemovalRecoveryFixture(t, environment)
				inventory := recoveryRemovalInventory(fixture)
				request := fixture.request()
				switch mode {
				case "missing-proof":
					if err := os.Remove(filepath.Join(fixture.artifact, "proof.json")); err != nil {
						t.Fatal(err)
					}
				case "foreign-environment":
					request.SourcePreflight = strings.Replace(fixture.artifact, string(filepath.Separator)+environment+string(filepath.Separator), string(filepath.Separator)+"foreign"+string(filepath.Separator), 1)
				case "changed-plan", "changed-bindings":
					name := strings.TrimPrefix(mode, "changed-") + ".json"
					if err := os.WriteFile(filepath.Join(fixture.artifact, name), []byte("{}"), 0600); err != nil {
						t.Fatal(err)
					}
				case "changed-kubeconfig":
					body := readTestFile(t, fixture.kube)
					if err := os.WriteFile(fixture.kube, []byte(strings.ReplaceAll(body, "isolated-recovery-kubernetes-token", "different-identity-token")), 0600); err != nil {
						t.Fatal(err)
					}
				case "missing-checker", "invalid-checker-version":
					path := filepath.Join(fixture.artifact, "proof.json")
					var proof environmentPreflightProof
					if err := readEnvironmentPreflightJSON(path, &proof); err != nil {
						t.Fatal(err)
					}
					if mode == "missing-checker" {
						proof.CheckerSHA256 = ""
					} else {
						proof.CheckerVersion = "unqualified-checker"
					}
					writeSyntheticPreflightJSON(t, path, proof)
				case "archive-cluster", "archive-environment":
					path := filepath.Join(filepath.Dir(filepath.Dir(fixture.kube)), "deployment", "adapters", "lke", "state.env")
					body := readTestFile(t, path)
					if mode == "archive-cluster" {
						body = strings.ReplaceAll(body, "LKE_CLUSTER_ID=123", "LKE_CLUSTER_ID=456")
					} else {
						body = strings.ReplaceAll(body, "CLOUD_ENV_NAME="+environment, "CLOUD_ENV_NAME=foreign")
					}
					if err := os.WriteFile(path, []byte(body), 0600); err != nil {
						t.Fatal(err)
					}
				case "current-kube-generation":
					body := strings.ReplaceAll(readTestFile(t, fixture.kube), "isolated-recovery-kubernetes-token", "new-generation-kubernetes-token")
					if err := fixture.store.write("kube/kubeconfig.yaml", []byte(body), true); err != nil {
						t.Fatal(err)
					}
				case "current-state-generation":
					writeTestLKEState(t, environment, "LKE_CLUSTER_ID=456\nLKE_CLUSTER_REGION=us-sea\n")
				case "duplicate-nodes":
					request.NodeIDs = []string{"42", "42"}
				case "foreign-confirm":
					request.Confirm = "video-cloud-foreign"
				case "missing-review-sha":
					request.Confirm = fixture.config.Values["CLOUD_STACK_NAME"]
				case "wrong-review-sha":
					request.Confirm = fixture.config.Values["CLOUD_STACK_NAME"]
					request.ReviewSHA256 = strings.Repeat("0", 64)
				case "missing-token":
					if err := os.Remove(filepath.Join(fixture.store.Root, "operator", "env", "LINODE_TOKEN")); err != nil {
						t.Fatal(err)
					}
				case "incomplete-pagination":
					inventory.incomplete = true
				case "duplicate-volume-id":
					inventory.volumes = append(inventory.volumes, map[string]any{"id": 101, "label": "pvc-other", "region": "us-sea", "status": "active", "linode_id": nil, "created": fixture.qualifiedAt.Add(-2 * time.Hour).Format(time.RFC3339)})
				case "duplicate-volume-label":
					inventory.volumes = append(inventory.volumes, map[string]any{"id": 303, "label": inventory.volumes[0]["label"], "region": "us-sea", "status": "active", "linode_id": nil, "created": fixture.qualifiedAt.Add(-2 * time.Hour).Format(time.RFC3339)})
				case "wrong-region":
					inventory.volumes[0]["region"] = "us-east"
				case "attached-volume":
					inventory.volumes[0]["linode_id"] = 42
				case "current-cluster":
					inventory.clusters = []map[string]any{{"id": 123, "label": "original-cluster", "tags": []string{}}}
				case "current-node":
					inventory.nodes = []map[string]any{{"id": 42, "label": "lke123-general-original", "tags": []string{}}}
				case "missing-attachment":
					inventory.events["volume/101"] = nil
				case "missing-delete":
					inventory.events["linode/42"] = nil
				case "future-node-delete":
					inventory.events["linode/42"][0]["created"] = time.Now().UTC().Add(time.Hour).Format(time.RFC3339Nano)
				case "unfinished-attachment":
					inventory.events["volume/101"][0]["status"] = "started"
				case "wrong-attachment-label":
					inventory.events["volume/101"][0]["entity"].(map[string]any)["label"] = "pvc-foreign"
				case "wrong-attachment-url":
					inventory.events["volume/101"][0]["entity"].(map[string]any)["url"] = "/v4/volumes/999"
				case "wrong-original-node":
					inventory.events["volume/101"][0]["secondary_entity"] = map[string]any{"type": "linode", "id": "999", "label": "lke456-foreign", "url": "/v4/linode/instances/999"}
				case "later-foreign-attachment":
					original := inventory.events["volume/101"][0]
					inventory.events["volume/101"] = append(inventory.events["volume/101"], map[string]any{"id": 4, "created": fixture.qualifiedAt.Add(2 * time.Hour).Format(time.RFC3339), "action": "volume_attach", "status": "finished", "entity": original["entity"], "secondary_entity": map[string]any{"type": "linode", "id": "999", "label": "lke456-foreign", "url": "/v4/linode/instances/999"}})
				case "duplicate-event":
					inventory.events["volume/101"] = append(inventory.events["volume/101"], inventory.events["volume/101"][0])
				}
				before := snapshotRemovalRecoveryStore(t, fixture.store.Root)
				var output bytes.Buffer
				if err := reconcileDeploymentRemovalVolumes(context.Background(), fixture.config, request, inventory.reader(t), &output); err == nil {
					t.Fatalf("%s evidence was accepted", mode)
				}
				if after := snapshotRemovalRecoveryStore(t, fixture.store.Root); !reflect.DeepEqual(before, after) {
					t.Fatal("rejected recovery changed the canonical private store")
				}
			})
		}
	}
}

func TestDeploymentRemovalRecoveryNeverOverwritesExistingOwnership(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			fixture := deploymentRemovalRecoveryFixture(t, environment)
			inventory := recoveryRemovalInventory(fixture)
			request := approveRemovalRecoveryTestReview(t, fixture, fixture.request(), inventory.reader(t))
			original := []byte("existing-removal-ownership-must-not-be-replaced\n")
			if err := fixture.store.write("deployment/remove-volume-ownership.json", original, true); err != nil {
				t.Fatal(err)
			}
			before := snapshotRemovalRecoveryStore(t, fixture.store.Root)
			var output bytes.Buffer
			if err := reconcileDeploymentRemovalVolumes(context.Background(), fixture.config, request, inventory.reader(t), &output); err == nil {
				t.Fatal("recovery overwrote an existing removal proof")
			}
			if after := snapshotRemovalRecoveryStore(t, fixture.store.Root); !reflect.DeepEqual(before, after) {
				t.Fatal("existing ownership or other private evidence was overwritten")
			}
		})
	}
}

func TestDeploymentRemovalRecoveryPublishDoesNotReplaceConcurrentOwnership(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			fixture := deploymentRemovalRecoveryFixture(t, environment)
			inventory := recoveryRemovalInventory(fixture)
			request := approveRemovalRecoveryTestReview(t, fixture, fixture.request(), inventory.reader(t))
			original := []byte("another-process-published-original-ownership\n")
			baseRead := inventory.reader(t)
			published := false
			read := func(ctx context.Context, token, path string, filter map[string]any) ([]byte, error) {
				body, err := baseRead(ctx, token, path, filter)
				if err == nil && strings.HasPrefix(path, "/account/events?") && !published {
					published = true
					if err = fixture.store.write("deployment/remove-volume-ownership.json", original, true); err != nil {
						t.Fatal(err)
					}
				}
				return body, err
			}
			var output bytes.Buffer
			if err := reconcileDeploymentRemovalVolumes(context.Background(), fixture.config, request, read, &output); err == nil {
				t.Fatal("concurrent ownership publication was accepted")
			}
			if !published {
				t.Fatal("race fixture did not reach event verification")
			}
			path := filepath.Join(fixture.store.Root, "deployment", "remove-volume-ownership.json")
			if !bytes.Equal([]byte(readTestFile(t, path)), original) {
				t.Fatal("recovery replaced concurrently published ownership")
			}
		})
	}
}

func deploymentRemovalRecoveryFixture(t *testing.T, environment string) removalRecoveryTestFixture {
	t.Helper()
	c, qualification, _ := completePreflightProofFixture(t, environment)
	c.Configuration.Adapter = "lke"
	c.Configuration.Workspace = t.TempDir()
	c.Configuration.RuntimeRoot = filepath.Join(c.Configuration.Workspace, "cloud_env", environment, "runtime")
	c.Configuration.Values["LINODE_DEFAULT_REGION"] = "us-sea"
	c.Configuration.AdapterResolved = map[string]string{"LKE_REGION": "us-sea"}
	stack := c.Configuration.Values["CLOUD_STACK_NAME"]
	raw, err := json.Marshal(map[string]any{
		"apiVersion": "v1", "kind": "Config", "current-context": "selected",
		"contexts": []any{map[string]any{"name": "selected", "context": map[string]any{"cluster": "selected", "user": "operator"}}},
		"clusters": []any{map[string]any{"name": "selected", "cluster": map[string]any{"server": "https://123.us-sea.linodelke.net:443", "certificate-authority-data": base64.StdEncoding.EncodeToString(removalRecoveryTestCA(t))}}},
		"users":    []any{map[string]any{"name": "operator", "user": map[string]any{"token": "isolated-recovery-kubernetes-token"}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	c.Backend, err = newEnvironmentKubernetesBackend(string(raw), environment)
	if err != nil {
		t.Fatal(err)
	}
	objects := []deploymentplan.Object{}
	definitions := []deploymentplan.Definition{}
	c.Bindings.Owners = map[string]string{}
	for _, binding := range []struct{ name, uid, volume string }{
		{"database", "11111111-2222-3333-4444-555555555555", "pvc-1111111122223333"},
		{"identity", "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee", "pvc-aaaaaaaabbbbcccc"},
	} {
		object := deploymentplan.Object{
			"apiVersion": "v1", "kind": "PersistentVolumeClaim",
			"metadata": deploymentplan.Object{"name": binding.name, "namespace": stack + "-platform", "uid": binding.uid, "resourceVersion": "4", "labels": deploymentplan.Object{"rtk.realtek.com/stack": stack, "app.kubernetes.io/managed-by": "rtk-cloud"}},
			"spec":     deploymentplan.Object{"volumeName": binding.volume, "storageClassName": "linode-block-storage-retain", "accessModes": []any{"ReadWriteOnce"}, "resources": deploymentplan.Object{"requests": deploymentplan.Object{"storage": "10Gi"}}},
		}
		objects = append(objects, object)
		definitions = append(definitions, deploymentplan.Definition{Object: deploymentplan.Clone(object), Phase: "storage", Retain: true})
		c.Bindings.Owners[deploymentplan.ID(object)] = binding.uid
	}
	inputs := c.Plan.Inputs
	inputs.BindingsSHA256 = deploymentplan.Digest(c.Bindings)
	c.Plan, err = deploymentplan.Compile(inputs, definitions, objects)
	if err != nil {
		t.Fatal(err)
	}
	qualification.PlanSHA256 = c.Plan.SHA256()
	record := environmentPreflightBindingsRecord{Schema: "rtk-cloud.preflight-bindings/v1", Environment: environment, Operation: "upgrade", Decision: "NO-GO", PlanSHA256: c.Plan.SHA256(), BindingsSHA256: inputs.BindingsSHA256, Observed: &c.Bindings}
	rawRecord, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	if err = c.Store.write("deployment/identity-bindings.json", rawRecord, true); err != nil {
		t.Fatal(err)
	}
	publishedAt := time.Now().UTC()
	c.PKICheckedAt = publishedAt.Add(-time.Second)
	c.PKIVerifiedUntil = publishedAt.Add(599 * time.Second)
	for i := range c.ReleaseProofs {
		c.ReleaseProofs[i].ObservedAt = publishedAt.Add(-2 * time.Second)
	}
	proof, err := saveEnvironmentPreflightProof(c, qualification, publishedAt)
	if err != nil {
		t.Fatal(err)
	}
	active, err := readEnvironmentPreflightActive(c.Store)
	if err != nil {
		t.Fatal(err)
	}
	artifact := filepath.Join(c.Store.Root, "deployment", "preflight", filepath.FromSlash(active.Artifact))
	// Publish through the complete production validator, then age only this
	// isolated historical fixture; recovery must not renew deployment admission.
	qualifiedAt := publishedAt.Add(-24 * time.Hour).Truncate(time.Second)
	proof.QualifiedAt = qualifiedAt
	proof.ValidUntil = qualifiedAt.Add(600 * time.Second)
	proof.PKICheckedAt = qualifiedAt.Add(-time.Second)
	proof.PKIVerifiedUntil = qualifiedAt.Add(599 * time.Second)
	for i := range proof.ReleaseProofs {
		proof.ReleaseProofs[i].ObservedAt = qualifiedAt.Add(-2 * time.Second)
	}
	writeSyntheticPreflightJSON(t, filepath.Join(artifact, "proof.json"), proof)
	active.ProofSHA256 = proof.SHA256()
	if err := writeEnvironmentPreflightActive(c.Store, active); err != nil {
		t.Fatal(err)
	}
	kube := filepath.Join(c.Store.Root, "migration-backup", "destroy-lke-123", "kube", "kubeconfig.yaml")
	if err = c.Store.write(filepath.ToSlash(strings.TrimPrefix(kube, c.Store.Root+string(filepath.Separator))), raw, true); err != nil {
		t.Fatal(err)
	}
	state := []byte("CLOUD_ENV_NAME=" + environment + "\nCLOUD_STACK_NAME=" + stack + "\nLKE_CLUSTER_ID=123\nLKE_CLUSTER_REGION=us-sea\n")
	if err = c.Store.write("migration-backup/destroy-lke-123/deployment/adapters/lke/state.env", state, true); err != nil {
		t.Fatal(err)
	}
	if err = c.Store.write("operator/env/LINODE_TOKEN", []byte("isolated-recovery-token"), true); err != nil {
		t.Fatal(err)
	}
	t.Setenv("LINODE_TOKEN", "foreign-process-token-must-not-be-used")
	return removalRecoveryTestFixture{config: c.Configuration, store: c.Store, artifact: artifact, kube: kube, qualifiedAt: qualifiedAt}
}

func removalRecoveryTestCA(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "isolated recovery CA"}, NotBefore: now.Add(-48 * time.Hour), NotAfter: now.Add(48 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func snapshotRemovalRecoveryStore(t *testing.T, root string) map[string]string {
	t.Helper()
	result := map[string]string{}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		value := info.Mode().String()
		if entry.Type().IsRegular() {
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			value += ":" + string(raw)
		}
		result[relative] = value
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}
