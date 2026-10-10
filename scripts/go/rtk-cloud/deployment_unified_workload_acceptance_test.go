package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func targetWorkloadFixture(t *testing.T, environment, kind string) (*deploymentplan.Plan, []deploymentplan.Object, string) {
	t.Helper()
	stack, namespace := "video-cloud-"+environment, "video-cloud-"+environment+"-application"
	digest := "sha256:" + strings.Repeat("a", 64)
	image := "ghcr.io/hkt999rtk/runtime@" + digest
	ref := func(kind, name, uid string) []any {
		return []any{map[string]any{"apiVersion": "apps/v1", "kind": kind, "name": name, "uid": uid, "controller": true}}
	}
	podSpec := deploymentplan.Object{"containers": []any{map[string]any{"name": "app", "image": image}, map[string]any{"name": "sidecar", "image": image}}, "initContainers": []any{map[string]any{"name": "bootstrap", "image": image}, map[string]any{"name": "renewal", "image": image, "restartPolicy": "Always"}}}
	controller := deploymentplan.Object{"apiVersion": "apps/v1", "kind": kind, "metadata": map[string]any{"namespace": namespace, "name": "application", "uid": "original-controller", "resourceVersion": "1", "generation": float64(2), "labels": map[string]any{"rtk.realtek.com/stack": stack, "app.kubernetes.io/managed-by": "rtk-cloud"}}, "spec": map[string]any{"replicas": float64(1), "template": map[string]any{"spec": podSpec}}, "status": map[string]any{"observedGeneration": float64(2), "replicas": float64(1), "readyReplicas": float64(1), "updatedReplicas": float64(1), "availableReplicas": float64(1), "unavailableReplicas": float64(0), "currentRevision": "selected-revision", "updateRevision": "selected-revision", "desiredNumberScheduled": float64(1), "numberReady": float64(1), "updatedNumberScheduled": float64(1), "numberUnavailable": float64(0)}}
	container := func(name string, completed bool) any {
		state := map[string]any{"running": map[string]any{"startedAt": "2026-10-06T00:00:00Z"}}
		if completed {
			state = map[string]any{"terminated": map[string]any{"exitCode": float64(0)}}
		}
		return map[string]any{"name": name, "ready": !completed, "started": !completed, "containerID": "containerd://original-" + name, "imageID": "docker-pullable://ghcr.io/hkt999rtk/runtime@" + digest, "state": state}
	}
	pod := deploymentplan.Object{"apiVersion": "v1", "kind": "Pod", "metadata": map[string]any{"namespace": namespace, "name": "application-0", "uid": "original-pod", "ownerReferences": ref(kind, "application", "original-controller")}, "spec": deploymentplan.Clone(podSpec), "status": map[string]any{"phase": "Running", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}, "containerStatuses": []any{container("app", false), container("sidecar", false)}, "initContainerStatuses": []any{container("bootstrap", true), container("renewal", false)}}}
	objects := []deploymentplan.Object{controller, pod}
	if kind == "Deployment" {
		rs := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ReplicaSet", "metadata": map[string]any{"namespace": namespace, "name": "application-selected", "uid": "original-replicaset", "ownerReferences": ref("Deployment", "application", "original-controller")}}
		deploymentplan.Map(pod["metadata"])["ownerReferences"] = ref("ReplicaSet", "application-selected", "original-replicaset")
		objects = append(objects, rs)
	}
	plan, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: environment, Stack: stack, Operation: "upgrade", ConfigurationSHA256: deploymentplan.Digest("fixture-config"), ReleaseSHA256: deploymentplan.Digest("fixture-release"), BindingsSHA256: deploymentplan.Digest("fixture-bindings")}, []deploymentplan.Definition{{Object: deploymentplan.Projection(controller), Phase: "workload"}}, objects)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Resources[0].Action != "retain" {
		t.Fatal("fixture must verify existing retained workloads")
	}
	return plan, objects, digest
}

func TestTargetWorkloadAcceptanceRetainsOnlyVerifiedHistoricalStandalonePods(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"succeeded", "failed", "running", "pending", "restart-always", "foreign-stack", "missing-uid", "planned-terminal"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				plan, objects, digest := targetWorkloadFixture(t, environment, "Deployment")
				namespace := deploymentplan.Map(objects[0]["metadata"])["namespace"]
				probe := deploymentplan.Object{
					"apiVersion": "v1", "kind": "Pod",
					"metadata": map[string]any{"name": "sqlite-migration-probe", "namespace": namespace, "uid": "historical-probe"},
					"spec":     map[string]any{"restartPolicy": "Never", "containers": []any{map[string]any{"name": "probe", "image": "busybox:old-probe"}}},
					"status":   map[string]any{"phase": "Succeeded"},
				}
				switch mode {
				case "failed":
					deploymentplan.Map(probe["status"])["phase"] = "Failed"
				case "running":
					deploymentplan.Map(probe["status"])["phase"] = "Running"
				case "pending":
					deploymentplan.Map(probe["status"])["phase"] = "Pending"
				case "restart-always":
					deploymentplan.Map(probe["spec"])["restartPolicy"] = "Always"
				case "foreign-stack":
					deploymentplan.Map(probe["metadata"])["labels"] = map[string]any{"rtk.realtek.com/stack": "another-stack"}
				case "missing-uid":
					delete(deploymentplan.Map(probe["metadata"]), "uid")
				case "planned-terminal":
					plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: deploymentplan.ID(probe), UID: "historical-probe", Desired: deploymentplan.Projection(probe), Action: "retain", Phase: "workload"})
				}
				objects = append(objects, probe)
				calls := 0
				err := verifyEnvironmentTargetWorkloads(context.Background(), plan, objects, func(_ context.Context, image string) (map[string]bool, error) {
					calls++
					if strings.Contains(image, "old-probe") {
						t.Fatal("historical probe must not be counted as a candidate replica or image")
					}
					return map[string]bool{digest: true}, nil
				})
				validHistory := mode == "succeeded" || mode == "failed"
				if validHistory && (err != nil || calls != 1) {
					t.Fatalf("valid retained history blocked healthy application proof: calls=%d err=%v", calls, err)
				}
				if !validHistory && err == nil {
					t.Fatal("unverified or explicitly planned terminal Pod was accepted as history")
				}
			})
		}
	}
}

func TestTargetWorkloadAcceptanceRejectsReadyButWrongOrIncompleteRuntime(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet"} {
			for _, mode := range []string{"valid", "owner-replaced", "controller-unobserved", "missing-pod", "extra-pod", "foreign-owner", "foreign-parent-uid", "wrong-image", "wrong-runtime-digest", "missing-container", "missing-status", "duplicate-status", "app-waiting", "app-terminated", "not-started", "not-ready", "pod-pending", "init-failed", "init-missing", "sidecar-init-not-ready", "zero-daemon-nodes", "image-evidence-missing"} {
				t.Run(environment+"/"+kind+"/"+mode, func(t *testing.T) {
					plan, objects, digest := targetWorkloadFixture(t, environment, kind)
					approved := plan.SHA256()
					controller, pod := objects[0], objects[1]
					status := deploymentplan.Map(pod["status"])
					app := deploymentplan.Map(certIssuerObjectList(status["containerStatuses"])[0])
					switch mode {
					case "owner-replaced":
						deploymentplan.Map(controller["metadata"])["uid"] = "replacement-owner"
					case "controller-unobserved":
						deploymentplan.Map(controller["status"])["observedGeneration"] = float64(1)
					case "missing-pod":
						objects = append(objects[:1], objects[2:]...)
					case "extra-pod":
						extra := deploymentplan.Clone(pod)
						deploymentplan.Map(extra["metadata"])["name"] = "extra-pod"
						deploymentplan.Map(extra["metadata"])["uid"] = "extra-uid"
						objects = append(objects, extra)
					case "foreign-owner":
						deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["metadata"])["ownerReferences"])[0])["name"] = "unknown-controller"
					case "foreign-parent-uid":
						deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["metadata"])["ownerReferences"])[0])["uid"] = "foreign-uid"
					case "wrong-image":
						deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["spec"])["containers"])[0])["image"] = "ghcr.io/hkt999rtk/runtime:old"
					case "wrong-runtime-digest":
						app["imageID"] = "containerd://sha256:" + strings.Repeat("f", 64)
					case "missing-container":
						deploymentplan.Map(pod["spec"])["containers"] = []any{}
					case "missing-status":
						status["containerStatuses"] = []any{}
					case "duplicate-status":
						status["containerStatuses"] = []any{app, app}
					case "app-waiting":
						app["state"] = map[string]any{"waiting": map[string]any{"reason": "CrashLoopBackOff", "message": "private-diagnostic-sentinel"}}
					case "app-terminated":
						app["state"] = map[string]any{"terminated": map[string]any{"exitCode": float64(1)}}
					case "not-started":
						app["started"] = false
					case "not-ready":
						app["ready"] = false
					case "pod-pending":
						status["phase"] = "Pending"
					case "init-failed":
						deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(certIssuerObjectList(status["initContainerStatuses"])[0])["state"])["terminated"])["exitCode"] = float64(1)
					case "init-missing":
						status["initContainerStatuses"] = []any{}
					case "sidecar-init-not-ready":
						deploymentplan.Map(certIssuerObjectList(status["initContainerStatuses"])[1])["ready"] = false
					case "zero-daemon-nodes":
						if kind != "DaemonSet" {
							return
						}
						deploymentplan.Map(controller["status"])["desiredNumberScheduled"] = float64(0)
					}
					calls := 0
					err := verifyEnvironmentTargetWorkloads(context.Background(), plan, objects, func(context.Context, string) (map[string]bool, error) {
						calls++
						if mode == "image-evidence-missing" {
							return nil, nil
						}
						return map[string]bool{digest: true}, nil
					})
					if (err == nil) != (mode == "valid") || approved != plan.SHA256() {
						t.Fatal("wrong acceptance result or changed approved plan", err)
					}
					if mode == "valid" && calls != 1 {
						t.Fatal("same immutable image was fetched repeatedly", calls)
					}
					if err != nil && strings.Contains(err.Error(), "private-diagnostic-sentinel") {
						t.Fatal("private diagnostic exposed")
					}
				})
			}
		}
	}
}

func TestTargetAcceptanceRequiresPlannedJobsToComplete(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"complete", "complete-missing-pod", "complete-wrong-owner", "complete-wrong-image", "complete-bad-exit", "failed", "unfinished"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				plan, objects, digest := targetWorkloadFixture(t, environment, "Deployment")
				job := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": map[string]any{"namespace": plan.Stack + "-application", "name": "bootstrap-cleanup", "uid": "original-job"}, "spec": map[string]any{"template": map[string]any{"spec": map[string]any{"containers": []any{map[string]any{"name": "cleanup", "image": "ghcr.io/hkt999rtk/runtime@" + digest}}}}}}
				plan.Resources = append(plan.Resources, deploymentplan.Resource{ID: deploymentplan.ID(job), UID: "original-job", Action: "retain", Desired: deploymentplan.Projection(job)})
				if mode != "unfinished" {
					condition := "Complete"
					if mode == "failed" {
						condition = "Failed"
					}
					job["status"] = map[string]any{"conditions": []any{map[string]any{"type": condition, "status": "True"}}}
				}
				objects = append(objects, job)
				if strings.HasPrefix(mode, "complete") && mode != "complete-missing-pod" {
					jobPod := deploymentplan.Object{
						"apiVersion": "v1", "kind": "Pod",
						"metadata": map[string]any{"namespace": plan.Stack + "-application", "name": "bootstrap-cleanup-pod", "uid": "original-job-pod", "ownerReferences": []any{map[string]any{"apiVersion": "batch/v1", "kind": "Job", "name": "bootstrap-cleanup", "uid": "original-job", "controller": true}}},
						"spec":     deploymentplan.Clone(deploymentplan.Map(deploymentplan.Map(job["spec"])["template"])["spec"].(map[string]any)),
						"status":   map[string]any{"phase": "Succeeded", "containerStatuses": []any{map[string]any{"name": "cleanup", "containerID": "containerd://original-job-container", "imageID": "docker-pullable://ghcr.io/hkt999rtk/runtime@" + digest, "state": map[string]any{"terminated": map[string]any{"exitCode": float64(0)}}}}},
					}
					switch mode {
					case "complete-wrong-owner":
						deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(jobPod["metadata"])["ownerReferences"])[0])["uid"] = "foreign-job"
					case "complete-wrong-image":
						deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(jobPod["spec"])["containers"])[0])["image"] = "runtime:old"
					case "complete-bad-exit":
						status := deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(jobPod["status"])["containerStatuses"])[0])
						deploymentplan.Map(deploymentplan.Map(status["state"])["terminated"])["exitCode"] = float64(1)
					}
					objects = append(objects, jobPod)
				}
				err := verifyEnvironmentTargetWorkloads(context.Background(), plan, objects, func(context.Context, string) (map[string]bool, error) { return map[string]bool{digest: true}, nil })
				if (err == nil) != (mode == "complete") {
					t.Fatal(err)
				}
			})
		}
	}
}

func TestNativeRuntimeImageProofBindsPlatformManifestAndPrivateClient(t *testing.T) {
	for _, mode := range []string{"valid", "wrong-bytes", "wrong-platform", "ambiguous-platform", "missing-config", "denied"} {
		t.Run(mode, func(t *testing.T) {
			store := makeIsolatedTestSecretStore(t, "dev")
			for key, value := range map[string]string{"GHCR_PULL_USERNAME": "native-user", "GHCR_PULL_TOKEN": "private-registry-sentinel"} {
				if err := store.write("operator/env/"+key, []byte(value), true); err != nil {
					t.Fatal(err)
				}
			}
			configDigest := "sha256:" + strings.Repeat("c", 64)
			leaf := []byte(`{"schemaVersion":2,"config":{"digest":"` + configDigest + `"},"layers":[]}`)
			if mode == "missing-config" {
				leaf = []byte(`{"schemaVersion":2,"layers":[]}`)
			}
			leafDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(leaf))
			arch := "amd64"
			if mode == "wrong-platform" {
				arch = "arm64"
			}
			child := map[string]any{"digest": leafDigest, "platform": map[string]string{"os": "linux", "architecture": arch}}
			children := []any{child}
			if mode == "ambiguous-platform" {
				children = append(children, child)
			}
			index, _ := json.Marshal(map[string]any{"schemaVersion": 2, "manifests": children})
			indexDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(index))
			directory := t.TempDir()
			writeTestFile(t, filepath.Join(directory, "index.json"), string(index))
			writeTestFile(t, filepath.Join(directory, "leaf.json"), string(leaf))
			code := `#!/bin/sh
if test "$1" = info; then printf '[{"Name":"buildx","Path":"/native/installed/docker-buildx"}]'; exit 0; fi
test "$1" = --config || exit 1
test -f "$2/config.json" || exit 1
test "$DOCKER_CONFIG" = "$2" || exit 1
test "$BUILDX_CONFIG" = "$2/buildx" || exit 1
case "$2" in "$RTK_TEST_STORE_ROOT"/deployment/tmp/image-metadata-*) ;; *) exit 1;; esac
test "$3" = buildx && test "$4" = imagetools && test "$5" = inspect && test "$6" = --raw || exit 1
case "$7" in
 *"@$RTK_TEST_INDEX_DIGEST") cat "$RTK_TEST_INDEX_FILE";;
 *"@$RTK_TEST_LEAF_DIGEST") cat "$RTK_TEST_LEAF_FILE";;
 *) exit 1;;
esac
`
			if mode == "denied" {
				code = "#!/bin/sh\nprintf 'private-registry-sentinel' >&2\nexit 1\n"
			}
			if mode == "wrong-bytes" {
				writeTestFile(t, filepath.Join(directory, "leaf.json"), "changed")
			}
			writeTestFile(t, filepath.Join(directory, "docker"), code)
			if err := os.Chmod(filepath.Join(directory, "docker"), 0755); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_TEST_STORE_ROOT", store.Root)
			t.Setenv("RTK_TEST_INDEX_DIGEST", indexDigest)
			t.Setenv("RTK_TEST_LEAF_DIGEST", leafDigest)
			t.Setenv("RTK_TEST_INDEX_FILE", filepath.Join(directory, "index.json"))
			t.Setenv("RTK_TEST_LEAF_FILE", filepath.Join(directory, "leaf.json"))
			accepted, err := environmentRuntimeImageDigests(context.Background(), store, "ghcr.io/hkt999rtk/runtime@"+indexDigest)
			if mode == "valid" {
				if err != nil || !accepted[indexDigest] || !accepted[leafDigest] || !accepted[configDigest] {
					t.Fatal("valid index/platform/config rejected", err)
				}
			} else if err == nil {
				t.Fatal("unverified OCI metadata passed")
			}
			if err != nil && strings.Contains(err.Error(), "private-registry-sentinel") {
				t.Fatal("credential leaked")
			}
			entries, _ := os.ReadDir(filepath.Join(store.Root, "deployment", "tmp"))
			if len(entries) != 0 {
				t.Fatal("temporary registry credentials retained")
			}
		})
	}
}

func TestTargetWorkloadAcceptanceUsesFrozenNativeInventory(t *testing.T) {
	kube := frozenKubernetesFixture(t)
	raw, _ := yaml.Marshal(kube)
	backend, err := newEnvironmentKubernetesBackend(string(raw), "dev")
	if err != nil {
		t.Fatal(err)
	}
	plan, objects, _ := targetWorkloadFixture(t, "dev", "Deployment")
	// Fail before OCI reads: a matching spec cannot hide a broken retained owner.
	deploymentplan.Map(objects[0]["status"])["observedGeneration"] = float64(1)
	deploymentplan.Map(objects[1]["status"])["phase"] = "Pending"
	directory := t.TempDir()
	inventory, _ := json.Marshal(map[string]any{"items": objects})
	writeTestFile(t, filepath.Join(directory, "inventory.json"), string(inventory))
	writeTestFile(t, filepath.Join(directory, "namespaces.json"), `{"items":[{"apiVersion":"v1","kind":"Namespace","metadata":{"name":"video-cloud-dev-application"}}]}`)
	writeTestFile(t, filepath.Join(directory, "kubectl"), `#!/bin/sh
test "$1" = --kubeconfig || exit 1
case "$2" in "$RTK_TEST_STORE_ROOT"/deployment/tmp/kubernetes-api-*/kubeconfig.yaml) ;; *) exit 1;; esac
case "$*" in
 *"get namespace "*) cat "$RTK_TEST_NAMESPACES";;
 *"get validatingwebhookconfiguration "*|*"get clusterrole,"*) printf '{"items":[]}' ;;
 *"get deployment,"*) cat "$RTK_TEST_INVENTORY";;
 *) exit 1;;
esac
`)
	if err := os.Chmod(filepath.Join(directory, "kubectl"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_TEST_STORE_ROOT", backend.Store.Root)
	t.Setenv("RTK_TEST_NAMESPACES", filepath.Join(directory, "namespaces.json"))
	t.Setenv("RTK_TEST_INVENTORY", filepath.Join(directory, "inventory.json"))
	if err := backend.CheckTargetWorkloads(context.Background(), plan); err == nil {
		t.Fatal("native retained workload failure was accepted")
	}
	entries, _ := os.ReadDir(filepath.Join(backend.Store.Root, "deployment", "tmp"))
	if len(entries) != 0 {
		t.Fatal("frozen Kubernetes credentials retained")
	}
}

func TestUnifiedNativeExecutionRejectsAbsentFunctionalAcceptanceBeforeMutation(t *testing.T) {
	deps := defaultUnifiedDeploymentDependencies()
	if err := deps.Execute(context.Background(), nil, "", nil, nil); err == nil {
		t.Fatal("missing full functional acceptance was accepted")
	}
}

func TestNativePublicOCIImageWithIsolatedClient(t *testing.T) {
	image := os.Getenv("RTK_CLOUD_NATIVE_OCI_IMAGE")
	if image == "" {
		t.Skip("set an explicitly selected immutable public OCI image for the native metadata smoke")
	}
	toolConfig := os.Getenv("RTK_CLOUD_NATIVE_DOCKER_CONFIG_SOURCE")
	if !filepath.IsAbs(toolConfig) {
		t.Skip("explicit installed Docker client config source required; fixture HOME remains isolated")
	}
	t.Setenv("DOCKER_CONFIG", toolConfig)
	store := makeIsolatedTestSecretStore(t, "dev")
	accepted, err := environmentRuntimeImageDigests(context.Background(), store, image)
	_, digest, _ := strings.Cut(image, "@")
	if err != nil || !accepted[digest] || len(accepted) < 2 {
		t.Fatal("native index/platform/config evidence unavailable", err)
	}
	entries, err := os.ReadDir(filepath.Join(store.Root, "deployment", "tmp"))
	if err != nil || len(entries) != 0 {
		t.Fatal("native temporary state not cleaned", err)
	}
}
