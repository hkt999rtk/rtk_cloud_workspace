package main

import (
	"context"
	"fmt"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const mqttCustodyCAStage = "mqtt-root-custody-ca-forward"
const mqttLogTrustStage = "mqtt-root-log-public-trust-forward"
const mqttLogTrustRepair = "logingester-bootstrap-trust"
const mqttLogTrustRepairSchema = "rtk-cloud.create-logingester-bootstrap-trust-selection/v1"

func completeMQTTCustodyForwardEntry(e *environmentCreateExecution, source, accepted, failedPlan, root string) error {
	key := "kubernetes/" + source
	if err := e.RecordReceipt(key, mustProviderRecoveryJSON(deploymentplan.Object{"actual_qualified_forward_stage": "kubernetes/" + accepted, "original_failed_plan_sha256": failedPlan, "original_root_issuer_id": root})); err != nil {
		return err
	}
	return e.CompleteEntry(key, time.Now().UTC())
}

// This stage only supersedes the known rollout failure after all original
// writes. The original plan, journal and workload UIDs stay bound to recovery.
func qualifyMQTTCustodyStage(ctx context.Context, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, previousRelease, release, image string) (*deploymentplan.Plan, string, error) {
	source := "mqtt-root-trust"
	if image != "" {
		source = mqttCustodyCAStage
	}
	old, err := readEnvironmentCreateStageInput(store, e, source)
	if err != nil || old == nil || old.ReleaseSHA256 != previousRelease {
		return nil, "", createProviderRecoveryBlock("original MQTT custody rollout input is unavailable")
	}
	j, exists, err := readEnvironmentCreateStageProgress(store, e, source, old)
	if err != nil || !exists {
		return nil, "", createProviderRecoveryBlock("original MQTT custody rollout journal is unavailable")
	}
	live, err := b.Inventory(ctx, old.Stack)
	if err != nil {
		return nil, "", err
	}
	plan, err := compileMQTTCustodyStage(old, j, live, release, image)
	if err != nil {
		return nil, "", err
	}
	leaseID := "Lease/" + old.Stack + "-video-cloud/rtk-deployment-execution"
	if lease, err := b.Read(ctx, leaseID); err != nil || lease != nil {
		return nil, "", createProviderRecoveryBlock("original interrupted MQTT stage must have released its Kubernetes execution Lease")
	}
	return plan, old.SHA256(), plan.Admit(ctx, b)
}

func compileMQTTCustodyStage(old *deploymentplan.Plan, j deploymentplan.Journal, live []deploymentplan.Object, release, image string) (*deploymentplan.Plan, error) {
	api := "Deployment/" + old.Stack + "-video-cloud/video-cloud-api"
	log := "Deployment/" + old.Stack + "-video-cloud/video-cloud-logingester"
	stage, failed, changeCount := mqttCustodyCAStage, api, 2
	if image != "" {
		stage, failed, changeCount = mqttLogTrustStage, log, 1
	}
	if j.Accepted || j.InFlight != "" || j.Failed != failed || len(j.Completed) != len(old.Resources) || len(j.Owners) != len(old.Resources) {
		return nil, createProviderRecoveryBlock("MQTT custody recovery requires all original writes and the exact interrupted rollout: " + failed)
	}
	objects := map[string]deploymentplan.Object{}
	for _, o := range live {
		objects[deploymentplan.ID(o)] = o
	}
	var defs []deploymentplan.Definition
	changes := map[string]bool{}
	for i, r := range old.Resources {
		actual := objects[r.ID]
		uid := j.Owners[r.ID]
		if j.Completed[i] != r.ID || uid == "" || deploymentplan.Map(actual["metadata"])["uid"] != uid || !deploymentplan.Matches(r.Desired, actual) {
			return nil, createProviderRecoveryBlock("original MQTT stage resource body or UID changed: " + r.ID)
		}
		object := deploymentplan.Clone(r.Desired)
		deploymentplan.Map(object["metadata"])["uid"] = uid
		if image != "" && r.ID == log {
			containers := certIssuerObjectList(environmentPKIPod(object)["containers"])
			if len(containers) != 1 {
				return nil, createProviderRecoveryBlock("original Log Ingester bootstrap workload has an unexpected container layout")
			}
			container := deploymentplan.Map(containers[0])
			const repository = "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@"
			prior := deploymentplan.String(container["image"])
			if !strings.HasPrefix(image, repository) || !immutableEnvironmentImage.MatchString(image) || !strings.HasPrefix(prior, repository) || image == prior || deploymentplan.Digest(container["command"]) != deploymentplan.Digest([]string{"/app/logingester", "--deployment-bootstrap-trust"}) {
				return nil, createProviderRecoveryBlock("the exact formal Log Ingester bootstrap image is required")
			}
			container["image"] = image
			changes[r.ID] = true
		} else if image == "" && (r.ID == api || r.ID == log) {
			if !allowCustodyPublicCARead(environmentPKIPod(object)) {
				return nil, createProviderRecoveryBlock("original workload does not have the expected custody CA projection: " + r.ID)
			}
			changes[r.ID] = true
		}
		defs = append(defs, deploymentplan.Definition{Object: object, Phase: r.Phase, Dependencies: r.Dependencies, Retain: !changes[r.ID]})
	}
	if len(changes) != changeCount {
		return nil, createProviderRecoveryBlock("required original MQTT workload owners are missing from the stage")
	}
	inputs := old.Inputs
	inputs.ReleaseSHA256 = release
	inputs.BindingsSHA256 = deploymentplan.Digest([]string{stage, old.SHA256(), deploymentplan.Digest(j), environmentCreateStageBindingsSHA(stage, defs)})
	plan, err := deploymentplan.Compile(inputs, defs, live)
	if err != nil {
		return nil, err
	}
	for _, r := range plan.Resources {
		want := "retain"
		if changes[r.ID] {
			want = "update"
		}
		if r.Action != want || r.UID != j.Owners[r.ID] {
			return nil, createProviderRecoveryBlock(fmt.Sprintf("MQTT custody recovery expected %s for original owner %s", want, r.ID))
		}
	}
	return plan, nil
}
