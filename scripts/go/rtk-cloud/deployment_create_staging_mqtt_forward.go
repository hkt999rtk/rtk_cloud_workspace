package main

import (
	"context"
	"encoding/json"
	"os"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const stagingMQTTRootStage = "mqtt-root-trust"

type stagingMQTTRootForward struct {
	Root         environmentCreateRegisteredIssuer
	Operation    environmentCreateIssuerOperation
	OriginalSHA  string
	JournalSHA   string
	PreviousKeys []string
	SingleOwner  bool
	Definitions  []deploymentplan.Definition
}

func validateStagingMQTTRootTail(entries []environmentCreateJournalEntry) (int, error) {
	bad := createProviderRecoveryBlock("only the original ready MQTT Root and its pending trust stage can use placement recovery")
	end := len(entries)
	for end > 0 && strings.HasPrefix(entries[end-1].Key, "kubernetes/"+stagingMQTTRootStage+"-rwo-") {
		entry := entries[end-1]
		if entry.State != "pending" || !entry.CompletedAt.IsZero() || len(entry.Receipt) != 0 {
			return 0, bad
		}
		end--
	}
	if end < 6 || entries[end-6].Key != "kubernetes/transport-client-enrollment" || entries[end-6].State != "completed" {
		return 0, bad
	}
	tail := entries[end-5 : end]
	var operation environmentCreateIssuerOperation
	if decodeEnvironmentJSON(tail[0].Receipt, &operation) != nil || !environmentBootstrapOwnerUUID.MatchString(operation.ID) || !environmentBootstrapOwnerUUID.MatchString(operation.IssuerID) || operation.Status != "requested" {
		return 0, bad
	}
	for i, key := range []string{"pki-reserve-mqtt-root", "pki-authorize-" + operation.ID, "pki-provision-" + operation.ID, "pki-import-" + operation.ID} {
		if entry := tail[i]; entry.Key != key || entry.Phase != "bootstrap" || entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 {
			return 0, bad
		}
	}
	if entry := tail[4]; entry.Key != "kubernetes/"+stagingMQTTRootStage || entry.Phase != stagingMQTTRootStage || entry.State != "pending" || !entry.CompletedAt.IsZero() || len(entry.Receipt) != 0 {
		return 0, bad
	}
	return end - 5, nil
}

func qualifyStagingMQTTRootForward(ctx context.Context, cfg deploymentConfig, store secretStore, plan *environmentCreatePlan, e *environmentCreateExecution, runtime environmentCreateBootstrapRuntime, operator map[string]string, singleOwner bool) (*stagingMQTTRootForward, error) {
	bad := func(cause string) (*stagingMQTTRootForward, error) {
		return nil, createProviderRecoveryBlock("staging MQTT Root placement recovery " + cause)
	}
	entries := stagingMQTTSourceEntries(e.Journal.Entries)
	boundary, err := validateStagingMQTTRootTail(entries)
	if err != nil {
		return nil, err
	}
	entry := e.Journal.Entries[boundary+4]
	original, journal, err := originalCreateChildAtIntent(store, e, entry)
	if err != nil {
		return nil, err
	}
	ns := plan.Stack + "-video-cloud"
	apiID := "Deployment/" + ns + "/video-cloud-api"
	validFailedStage := func(child *deploymentplan.Plan, childJournal deploymentplan.Journal) bool {
		return child.Environment == cfg.Environment && child.Stack == plan.Stack && child.ConfigurationSHA256 == plan.ConfigurationSHA256 && child.ReleaseSHA256 == deploymentplan.Digest(plan.Release) && !childJournal.Accepted && !childJournal.AcceptanceStarted && childJournal.InFlight == "" && childJournal.Failed == apiID && len(child.Resources) == 6 && len(childJournal.Completed) == 6 && len(childJournal.Owners) == 6
	}
	if !validFailedStage(original, journal) {
		return bad("requires the original fully written stage with only the API readiness failure")
	}
	originalSHA := original.SHA256()
	current, currentJournal := original, journal
	previous := entries[boundary+5:]
	suffixes := []string{"-rwo-", "-rwo-spread-"}
	if len(previous) > len(suffixes) || singleOwner != (len(previous) == len(suffixes)) {
		return bad("single replica recovery requires both original failed placement stages")
	}
	var previousKeys []string
	for i, prior := range previous {
		key := "kubernetes/" + stagingMQTTRootStage + suffixes[i] + originalSHA[:12]
		if prior.Key != key || prior.Phase != strings.TrimPrefix(key, "kubernetes/") {
			return bad("the prior placement stage is not bound to the original plan")
		}
		current, currentJournal, err = originalCreateChildAtIntent(store, e, prior)
		if err != nil || !validFailedStage(current, currentJournal) || deploymentplan.Digest(currentJournal.Owners) != deploymentplan.Digest(journal.Owners) {
			return bad("the prior placement stage changed its resource owners")
		}
		previousKeys = append(previousKeys, key)
	}
	root, operation, err := loadOriginalReadyCreateRoot(ctx, cfg, store, e, runtime.Backend, operator, &createFoundationRecoveryPlan{Materials: map[string]string{}}, "mqtt")
	if err != nil {
		return nil, err
	}
	app := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/application-client-enrollment")
	if app == nil || app.State != "completed" {
		return bad("requires the accepted application enrollment owners")
	}
	appPlan, appJournal, err := originalCreateChildAtIntent(store, e, *app)
	if err != nil || !createOpenBaoACKCompletedChild(appPlan, appJournal) {
		return bad("cannot verify the original application claim receipt")
	}
	claimID := "PersistentVolumeClaim/" + ns + "/video-cloud-api-service-state"
	claim, err := runtime.Backend.Read(ctx, claimID)
	claimUID := appJournal.Owners[claimID]
	claimSpec := deploymentplan.Map(claim["spec"])
	volume := deploymentplan.String(claimSpec["volumeName"])
	if err != nil || volume == "" || !environmentBootstrapOwnerUUID.MatchString(claimUID) || deploymentplan.Map(claim["metadata"])["uid"] != claimUID || deploymentplan.Digest(claimSpec["accessModes"]) != deploymentplan.Digest([]string{"ReadWriteOnce"}) {
		return bad("requires the exact bound original ReadWriteOnce claim")
	}
	var definitions []deploymentplan.Definition
	for _, resource := range current.Resources {
		actual, err := runtime.Backend.Read(ctx, resource.ID)
		uid := currentJournal.Owners[resource.ID]
		if err != nil || !slices.Contains(currentJournal.Completed, resource.ID) || !environmentBootstrapOwnerUUID.MatchString(uid) || deploymentplan.Map(actual["metadata"])["uid"] != uid || !deploymentplan.Matches(resource.Desired, actual) {
			return bad("found drift outside the original stage owners")
		}
		object := deploymentplan.Clone(resource.Desired)
		deploymentplan.Map(object["metadata"])["uid"] = uid
		if resource.ID == apiID && singleOwner {
			if deploymentplan.Map(actual["spec"])["replicas"] != float64(2) ||
				deploymentplan.Map(actual["status"])["readyReplicas"] != float64(1) ||
				!bindStagingSingleAPIOwner(object, plan.Stack) {
				return bad("original API is not the verified one-ready, two-replica identity owner")
			}
		} else if resource.ID == apiID {
			before := deploymentplan.Digest(environmentPKIPod(object))
			if (environmentPKIPod(object)["affinity"] != nil) != (len(previous) != 0) || !bindCreateApplicationSharedClaimPlacement(object, plan.Stack) || deploymentplan.Digest(environmentPKIPod(object)) == before {
				return bad("cannot derive the single placement change from the original API recipe")
			}
			if err := verifyStagingMQTTAPIPlacement(ctx, runtime.Backend, plan.Stack, apiID, volume, len(previous) != 0); err != nil {
				return nil, err
			}
		}
		definitions = append(definitions, deploymentplan.Definition{Object: object, Phase: resource.Phase, Dependencies: slices.Clone(resource.Dependencies), Retain: resource.ID != apiID})
	}
	return &stagingMQTTRootForward{Root: root, Operation: operation, OriginalSHA: originalSHA, JournalSHA: deploymentplan.Digest(journal), PreviousKeys: previousKeys, SingleOwner: singleOwner, Definitions: definitions}, nil
}

func verifyStagingMQTTAPIPlacement(ctx context.Context, backend environmentKubernetesBackend, stack, apiID, volume string, spreadConflict bool) error {
	bad := createProviderRecoveryBlock("original API replicas lack the expected native shared-volume placement conflict")
	objects, err := backend.Inventory(ctx, stack)
	if err != nil {
		return err
	}
	live := map[string]deploymentplan.Object{}
	for _, object := range objects {
		live[deploymentplan.ID(object)] = object
	}
	var pending, running deploymentplan.Object
	for _, object := range objects {
		if object["kind"] != "Pod" || deploymentplan.Map(object["metadata"])["deletionTimestamp"] != nil {
			continue
		}
		owner, err := environmentPodController(object, live)
		if err != nil || owner != apiID {
			continue
		}
		switch deploymentplan.Map(object["status"])["phase"] {
		case "Pending":
			if pending != nil {
				return bad
			}
			pending = object
		case "Running":
			if running != nil {
				return bad
			}
			running = object
		}
	}
	pendingMeta, runningMeta := deploymentplan.Map(pending["metadata"]), deploymentplan.Map(running["metadata"])
	pendingNode, runningNode := deploymentplan.Map(pending["spec"])["nodeName"], deploymentplan.Map(running["spec"])["nodeName"]
	if (pendingNode == nil) != spreadConflict || runningNode == nil || pendingNode == runningNode || pendingMeta["uid"] == nil || runningMeta["name"] == nil {
		return bad
	}
	raw, err := backend.command(ctx, nil, "--namespace", stack+"-video-cloud", "get", "events", "--field-selector", "involvedObject.name="+deploymentplan.String(pendingMeta["name"]), "-o", "json")
	var events struct{ Items []deploymentplan.Object }
	if err != nil || json.Unmarshal(raw, &events) != nil {
		return bad
	}
	for _, event := range events.Items {
		object := deploymentplan.Map(event["involvedObject"])
		message := deploymentplan.String(event["message"])
		if object["uid"] != pendingMeta["uid"] {
			continue
		}
		if spreadConflict && event["reason"] == "FailedScheduling" && strings.Contains(message, "didn't match pod topology spread constraints") {
			return nil
		}
		if !spreadConflict && event["reason"] == "FailedAttachVolume" && strings.Contains(message, `"`+volume+`"`) && strings.Contains(message, deploymentplan.String(runningMeta["name"])) {
			return nil
		}
	}
	return bad
}

func executeStagingMQTTRootForward(ctx context.Context, cfg deploymentConfig, store secretStore, plan *environmentCreatePlan, e *environmentCreateExecution, forward *stagingMQTTRootForward) error {
	stage := stagingMQTTRootStage + "-rwo-" + forward.OriginalSHA[:12]
	if forward.SingleOwner {
		stage = stagingMQTTRootStage + "-single-api-" + forward.OriginalSHA[:12]
	} else if len(forward.PreviousKeys) != 0 {
		stage = stagingMQTTRootStage + "-rwo-spread-" + forward.OriginalSHA[:12]
	}
	if err := applyEnvironmentCreateStage(ctx, cfg, store, plan, e, stage, forward.Definitions, os.Stdout); err != nil {
		return err
	}
	receipt := mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": forward.OriginalSHA, "original_failed_journal_sha256": forward.JournalSHA, "original_root_issuer_id": forward.Root.ID})
	for _, previous := range forward.PreviousKeys {
		if err := e.RecordReceipt(previous, receipt); err != nil {
			return err
		}
		if err := e.CompleteEntry(previous, time.Now().UTC()); err != nil {
			return err
		}
	}
	key := "kubernetes/" + stagingMQTTRootStage
	if err := e.RecordReceipt(key, receipt); err != nil {
		return err
	}
	return e.CompleteEntry(key, time.Now().UTC())
}

// The explicitly selected single owner uses each API variant's own existing PVC.
func bindStagingSingleAPIOwner(object deploymentplan.Object, stack string) bool {
	meta := deploymentplan.Map(object["metadata"])
	if object["kind"] != "Deployment" || meta["namespace"] != stack+"-video-cloud" {
		return false
	}
	for _, owner := range environmentCreateApplicationOwners() {
		if owner.Role != "video-cloud-api" || meta["name"] != owner.Name {
			continue
		}
		for _, item := range certIssuerObjectList(environmentPKIPod(object)["volumes"]) {
			if deploymentplan.Map(deploymentplan.Map(item)["persistentVolumeClaim"])["claimName"] == owner.Claim {
				deploymentplan.Map(object["spec"])["replicas"] = float64(1)
				return true
			}
		}
	}
	return false
}
