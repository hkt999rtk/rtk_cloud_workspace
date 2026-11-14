package main

import (
	"context"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestOnDeleteCompletedInitUsesOriginalOwnedRevision(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, operation := range []string{"upgrade", "scoped"} {
			for _, mode := range []string{"completed", "missing-revision", "foreign-owner", "non-controller-owner", "wrong-revision", "foreign-namespace", "wrong-init-recipe", "failed-init", "wrong-init-image-id", "running-sidecar-old", "running-app-old", "rolling-update"} {
				t.Run(environment+"/"+operation+"/"+mode, func(t *testing.T) {
					plan, objects, targetDigest := targetWorkloadFixture(t, environment, "StatefulSet")
					plan.Operation = operation
					controller, pod := objects[0], objects[1]
					meta := deploymentplan.Map(controller["metadata"])
					deploymentplan.Map(controller["spec"])["updateStrategy"] = deploymentplan.Object{"type": "OnDelete"}
					status := deploymentplan.Map(controller["status"])
					status["currentRevision"], status["updateRevision"], status["updatedReplicas"] = "original-revision", "target-revision", float64(0)
					deploymentplan.Map(pod["metadata"])["labels"] = deploymentplan.Object{"controller-revision-hash": "original-revision"}
					oldDigest := "sha256:" + strings.Repeat("b", 64)
					oldImage := "ghcr.io/hkt999rtk/runtime@" + oldDigest
					targetInits := certIssuerObjectList(environmentPKIPod(controller)["initContainers"])
					deploymentplan.Map(targetInits[0])["command"] = []any{"/app/original-bootstrap"}
					actualInits := certIssuerObjectList(deploymentplan.Map(pod["spec"])["initContainers"])
					deploymentplan.Map(actualInits[0])["command"] = []any{"/app/original-bootstrap"}
					deploymentplan.Map(actualInits[0])["image"] = oldImage
					initStatus := certIssuerObjectList(deploymentplan.Map(pod["status"])["initContainerStatuses"])
					deploymentplan.Map(initStatus[0])["imageID"] = "docker-pullable://ghcr.io/hkt999rtk/runtime@" + oldDigest
					originalTemplate := deploymentplan.Clone(deploymentplan.Map(controller["spec"])["template"].(map[string]any))
					oldSpec := deploymentplan.Map(originalTemplate["spec"])
					for _, field := range []string{"containers", "initContainers"} {
						for _, raw := range certIssuerObjectList(oldSpec[field]) {
							deploymentplan.Map(raw)["image"] = oldImage
						}
					}
					revision := deploymentplan.Object{"apiVersion": "apps/v1", "kind": "ControllerRevision", "metadata": deploymentplan.Object{"name": "original-revision", "namespace": meta["namespace"], "uid": "original-revision-uid", "ownerReferences": []any{deploymentplan.Object{"controller": true, "kind": "StatefulSet", "name": meta["name"], "uid": meta["uid"]}}}, "data": deploymentplan.Object{"spec": deploymentplan.Object{"template": originalTemplate}}}
					switch mode {
					case "foreign-owner":
						deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(revision["metadata"])["ownerReferences"])[0])["uid"] = "foreign-controller"
					case "non-controller-owner":
						deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(revision["metadata"])["ownerReferences"])[0])["controller"] = false
					case "wrong-revision":
						deploymentplan.Map(pod["metadata"])["labels"] = deploymentplan.Object{"controller-revision-hash": "another-revision"}
					case "foreign-namespace":
						deploymentplan.Map(revision["metadata"])["namespace"] = "unselected-namespace"
					case "wrong-init-recipe":
						deploymentplan.Map(actualInits[0])["command"] = []any{"/app/not-original"}
					case "failed-init":
						deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(initStatus[0])["state"])["terminated"])["exitCode"] = float64(1)
					case "wrong-init-image-id":
						deploymentplan.Map(initStatus[0])["imageID"] = "containerd://" + targetDigest
					case "running-sidecar-old":
						deploymentplan.Map(actualInits[1])["image"] = oldImage
						deploymentplan.Map(initStatus[1])["imageID"] = "containerd://" + oldDigest
					case "running-app-old":
						deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["spec"])["containers"])[0])["image"] = oldImage
						deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(pod["status"])["containerStatuses"])[0])["imageID"] = "containerd://" + oldDigest
					case "rolling-update":
						deploymentplan.Map(controller["spec"])["updateStrategy"] = deploymentplan.Object{"type": "RollingUpdate"}
					}
					plan.Resources[0].Desired = deploymentplan.Projection(controller)
					beforePlan, beforeObjects := plan.SHA256(), deploymentplan.Digest(objects)
					reads := 0
					evidence, err := appendOnDeleteInitRevisions(t.Context(), plan, objects, func(_ context.Context, id string) (deploymentplan.Object, error) {
						reads++
						if mode == "missing-revision" {
							return nil, nil
						}
						return revision, nil
					})
					if err == nil && operation == "scoped" {
						evidence = environmentCreateStageInventory(plan, evidence)
					}
					if mode == "completed" && reads != 1 {
						t.Fatal("original init history must be fetched exactly once", reads)
					}
					if err == nil {
						err = verifyEnvironmentTargetWorkloads(t.Context(), plan, evidence, func(_ context.Context, image string) (map[string]bool, error) {
							if image == oldImage {
								return map[string]bool{oldDigest: true}, nil
							}
							return map[string]bool{targetDigest: true}, nil
						})
					}
					if (err == nil) != (mode == "completed") {
						t.Fatal("OnDelete completed startup and current runtime", mode, err)
					}
					if plan.SHA256() != beforePlan || deploymentplan.Digest(objects) != beforeObjects {
						t.Fatal("runtime view changed the frozen plan or actual inventory")
					}
				})
			}
		}
	}
}
