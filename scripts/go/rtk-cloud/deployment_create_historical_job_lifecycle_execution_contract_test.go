package main

import (
	"context"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestHistoricalJobLifecycleCapturesOnlyActualFirstSuccessfulWait(t *testing.T) {
	for _, mode := range []string{"completed", "pending", "interrupted"} {
		t.Run(mode, func(t *testing.T) {
			_, e, _, fixture := freshStageFixture(t)
			b := fixture.normalTailE2EBackend
			b.captureJobLifecycle = true
			job := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"namespace": e.Journal.Stack + "-video-cloud", "name": "original-enrollment"}, "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": deploymentplan.Object{"restartPolicy": "Never", "initContainers": []any{deploymentplan.Object{"name": "original-init", "image": logACKNativeImage}}, "containers": []any{deploymentplan.Object{"name": "app", "image": logACKNativeImage}}}}}}
			beforeWrite := time.Now().UTC()
			actual, err := b.Write(t.Context(), "create", job, false)
			if err != nil {
				t.Fatal(err)
			}
			resource := deploymentplan.Resource{ID: deploymentplan.ID(job), UID: deploymentplan.String(deploymentplan.Map(actual["metadata"])["uid"]), Desired: deploymentplan.Clone(job)}
			observed := coreContractNativeInventory(b.objects)
			podID := "Pod/" + e.Journal.Stack + "-video-cloud/original-enrollment-pod"
			if deploymentplan.Map(observed[podID]["status"])["phase"] != "Pending" || deploymentplan.Map(observed[resource.ID]["status"])["completionTime"] != nil {
				t.Fatal("unwaited Job was reported complete")
			}
			if mode == "completed" {
				if err := b.Wait(t.Context(), resource); err != nil {
					t.Fatal(err)
				}
			} else if mode == "interrupted" {
				failure := coreContractReadinessFailure{normalTailE2EBackend: b, target: resource.ID}
				if failure.Wait(t.Context(), resource) == nil {
					t.Fatal("missing interruption")
				}
			}
			time.Sleep(time.Microsecond) // Ensure the independent later boundary is strictly later on coarse host clocks.
			afterWait := time.Now().UTC()
			observed = historicalNativeInventory(b.objects)
			complete := managedLateLogSuccessfulClaimHistory(resource, observed[resource.ID], observed[podID], afterWait)
			if complete != (mode == "completed") {
				t.Fatal("completed holder classification differs from actual Wait", mode)
			}
			if mode != "completed" {
				return
			}
			status := deploymentplan.Map(observed[resource.ID]["status"])
			start, startErr := time.Parse(time.RFC3339Nano, deploymentplan.String(status["startTime"]))
			finish, finishErr := time.Parse(time.RFC3339Nano, deploymentplan.String(status["completionTime"]))
			if startErr != nil || finishErr != nil || start.Before(beforeWrite) || finish.Before(start) || !finish.Before(afterWait) {
				t.Fatal("lifecycle did not use actual write/Wait interval")
			}
			original := deploymentplan.Digest(status)
			if err := b.Wait(context.Background(), resource); err != nil {
				t.Fatal(err)
			}
			if deploymentplan.Digest(deploymentplan.Map(b.objects[resource.ID]["status"])) != original {
				t.Fatal("later Wait rewrote first terminal observation")
			}
			if _, err := b.Write(t.Context(), "retain", actual, false); err != nil {
				t.Fatal(err)
			}
			if deploymentplan.Digest(deploymentplan.Map(b.objects[resource.ID]["status"])) != original {
				t.Fatal("retained original Job rewrote its lifecycle")
			}
		})
	}
}
