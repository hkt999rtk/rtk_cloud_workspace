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

const stagingCoreEnrollmentStage = "service-core-enrollment"

type stagingServiceCoreForward struct {
	OriginalSHA string
	JournalSHA  string
	FailedJob   string
	FailedUID   string
	Definitions []deploymentplan.Definition
}

func qualifyStagingServiceCoreForward(ctx context.Context, cfg deploymentConfig, store secretStore, plan *environmentCreatePlan, e *environmentCreateExecution, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, entry environmentCreateJournalEntry) (*stagingServiceCoreForward, error) {
	bad := func(cause string) (*stagingServiceCoreForward, error) {
		return nil, createProviderRecoveryBlock("staging enrollment volume recovery " + cause)
	}
	original, journal, err := originalCreateChildAtIntent(store, e, entry)
	if err != nil {
		return nil, err
	}
	ns := plan.Stack + "-account-manager"
	videoNS := plan.Stack + "-video-cloud"
	session := service.BootstrapSessionID
	suffix := strings.ReplaceAll(session, "-", "")[:12]
	failedName := "pki-enroll-account-manager-client-" + suffix
	failedID := "Job/" + ns + "/" + failedName
	failedUID := journal.Owners[failedID]
	if original.Environment != cfg.Environment || original.Stack != plan.Stack || original.ConfigurationSHA256 != plan.ConfigurationSHA256 || original.ReleaseSHA256 != deploymentplan.Digest(plan.Release) || journal.Accepted || journal.AcceptanceStarted || journal.InFlight != "" || journal.Failed != failedID || !environmentBootstrapOwnerUUID.MatchString(failedUID) || len(original.Resources) != 8 || len(journal.Completed) != 3 || len(journal.Owners) != 4 {
		return bad("requires the original failed first Job and three recorded PVC owners")
	}
	jobs := map[string]string{
		failedID: "account-manager", "Job/" + ns + "/pki-enroll-account-manager-server-" + suffix: "account-manager",
		"Job/" + videoNS + "/pki-enroll-certissuer-client-" + suffix:     "certissuer",
		"Job/" + videoNS + "/pki-enroll-pki-controller-client-" + suffix: "pki-controller",
		"Job/" + videoNS + "/pki-enroll-pki-controller-server-" + suffix: "pki-controller",
	}
	claims := keySet("PersistentVolumeClaim/"+ns+"/account-manager-service-state", "PersistentVolumeClaim/"+videoNS+"/certissuer-service-state", "PersistentVolumeClaim/"+videoNS+"/pki-controller-state")
	var definitions []deploymentplan.Definition
	var volume string
	for _, resource := range original.Resources {
		id := resource.ID
		definition := deploymentplan.Definition{Object: deploymentplan.Clone(resource.Desired), Phase: resource.Phase, Dependencies: slices.Clone(resource.Dependencies)}
		actual, err := runtime.Backend.Read(ctx, id)
		if err != nil {
			return nil, err
		}
		uid := deploymentplan.Map(actual["metadata"])["uid"]
		switch {
		case claims[id] && resource.Phase == "storage":
			if !slices.Contains(journal.Completed, id) || !environmentBootstrapOwnerUUID.MatchString(journal.Owners[id]) || uid != journal.Owners[id] || !deploymentplan.Matches(resource.Desired, actual) {
				return bad("an original PVC owner or claim changed")
			}
			deploymentplan.Map(definition.Object["metadata"])["uid"] = uid
			definition.Retain = true
			if id == "PersistentVolumeClaim/"+ns+"/account-manager-service-state" {
				volume = deploymentplan.String(deploymentplan.Map(actual["spec"])["volumeName"])
			}
		case jobs[id] != "" && resource.Phase == "bootstrap":
			metadata := deploymentplan.Map(definition.Object["metadata"])
			pod := environmentPKIPod(definition.Object)
			volumes, err := environmentPKINamedFields(pod["volumes"])
			if err != nil || deploymentplan.Map(volumes["service-state"]["persistentVolumeClaim"])["claimName"] != environmentCreateServiceOwnerClaim(jobs[id]) || pod["affinity"] != nil || len(resource.Dependencies) != 0 {
				return bad("the original Job claim, placement or dependency changed")
			}
			if id == failedID {
				if uid != failedUID || !deploymentplan.Matches(resource.Desired, actual) || !stagingEnrollmentJobDeadlineExceeded(actual) {
					return bad("the first Job has no exact terminal deadline outcome")
				}
				metadata["name"] = failedName + "-rwo"
			} else if actual != nil || journal.Owners[id] != "" {
				return bad("a later enrollment Job already has native state")
			}
			bindServiceEnrollmentToClaimOwner(definition.Object, deploymentplan.String(metadata["namespace"]), jobs[id])
		default:
			return bad("an original stage resource is outside the enrollment recipe")
		}
		definitions = append(definitions, definition)
	}
	if volume == "" {
		return bad("the original Account Manager volume is not bound")
	}
	for _, owner := range []struct{ namespace, name, stage string }{
		{ns, "account-manager", "service-active-listeners"},
		{plan.Stack + "-video-cloud", "certissuer", "service-managed-enrollment-issuer"},
	} {
		checkpoint := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+owner.stage)
		if checkpoint == nil || checkpoint.State != "completed" {
			return bad("the resident claim owner has no accepted stage")
		}
		child, ownerJournal, err := originalCreateChildAtIntent(store, e, *checkpoint)
		if err != nil || !createOpenBaoACKCompletedChild(child, ownerJournal) {
			return bad("the resident claim owner receipt is unavailable")
		}
		id := "Deployment/" + owner.namespace + "/" + owner.name
		actual, err := runtime.Backend.Read(ctx, id)
		if err != nil || !stagingEnrollmentClaimOwner(actual, ownerJournal.Owners[id], owner.name) {
			return bad("the resident claim owner or scheduler labels changed")
		}
	}
	if err := verifyStagingEnrollmentAttachmentFailure(ctx, runtime.Backend, ns, failedName, failedUID, volume, "account-manager"); err != nil {
		return nil, err
	}
	query := "SELECT json_build_object('clients',(SELECT count(*) FROM pki_service_client_issuances WHERE environment='staging' AND issuer_id='" + service.Intermediate.ID + "' AND subject='service:account-manager'),'servers',(SELECT count(*) FROM pki_server_issuances WHERE environment='staging' AND request_id='" + session + ":account-manager:server'))"
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, runtime, query)
	var counts map[string]*int
	if err != nil || decodeEnvironmentJSON(raw, &counts) != nil || len(counts) != 2 || counts["clients"] == nil || counts["servers"] == nil || *counts["clients"] != 0 || *counts["servers"] != 0 {
		return bad("the blocked Job has an unknown or existing native issuance outcome")
	}
	return &stagingServiceCoreForward{OriginalSHA: original.SHA256(), JournalSHA: deploymentplan.Digest(journal), FailedJob: failedID, FailedUID: failedUID, Definitions: definitions}, nil
}

func stagingEnrollmentJobDeadlineExceeded(job deploymentplan.Object) bool {
	status := deploymentplan.Map(job["status"])
	if status["active"] != nil && status["active"] != float64(0) || status["succeeded"] != nil && status["succeeded"] != float64(0) || status["failed"] != float64(1) {
		return false
	}
	for _, item := range certIssuerObjectList(status["conditions"]) {
		condition := deploymentplan.Map(item)
		if condition["type"] == "Failed" && condition["status"] == "True" && condition["reason"] == "DeadlineExceeded" {
			return true
		}
	}
	return false
}

func stagingEnrollmentClaimOwner(deployment deploymentplan.Object, uid, name string) bool {
	spec := deploymentplan.Map(deployment["spec"])
	labels := deploymentplan.Map(deploymentplan.Map(spec["selector"])["matchLabels"])
	if !environmentBootstrapOwnerUUID.MatchString(uid) || deploymentplan.Map(deployment["metadata"])["uid"] != uid || labels["app.kubernetes.io/name"] != name || spec["replicas"] != float64(1) {
		return false
	}
	for _, item := range certIssuerObjectList(environmentPKIPod(deployment)["volumes"]) {
		if deploymentplan.Map(deploymentplan.Map(item)["persistentVolumeClaim"])["claimName"] == environmentCreateServiceOwnerClaim(name) {
			return true
		}
	}
	return false
}

func verifyStagingEnrollmentAttachmentFailure(ctx context.Context, backend environmentKubernetesBackend, namespace, job, uid, volume, owner string) error {
	bad := createProviderRecoveryBlock("original failed enrollment Job lacks an exact RWO attach conflict and terminated Pod without execution")
	pods, err := backend.command(ctx, nil, "--namespace", namespace, "get", "pods", "-l", "job-name="+job, "-o", "json")
	if err != nil {
		return bad
	}
	events, err := backend.command(ctx, nil, "--namespace", namespace, "get", "events", "-o", "json")
	if err != nil || !stagingEnrollmentAttachmentEvidence(pods, events, job, uid, volume, owner) {
		return bad
	}
	return nil
}

func stagingEnrollmentAttachmentEvidence(podList, eventList []byte, job, uid, volume, owner string) bool {
	var pods struct{ Items []deploymentplan.Object }
	if json.Unmarshal(podList, &pods) != nil || pods.Items == nil || len(pods.Items) != 0 {
		return false
	}
	var events struct{ Items []deploymentplan.Object }
	if json.Unmarshal(eventList, &events) != nil {
		return false
	}
	created, deleted := "", ""
	for _, event := range events.Items {
		object := deploymentplan.Map(event["involvedObject"])
		if object["kind"] != "Job" || object["name"] != job || object["uid"] != uid {
			continue
		}
		message := deploymentplan.String(event["message"])
		switch event["reason"] {
		case "SuccessfulCreate":
			created = strings.TrimPrefix(message, "Created pod: ")
		case "SuccessfulDelete":
			deleted = strings.TrimPrefix(message, "Deleted pod: ")
		}
	}
	if created == "" || created != deleted || !strings.HasPrefix(created, job+"-") {
		return false
	}
	blocked := false
	for _, event := range events.Items {
		object := deploymentplan.Map(event["involvedObject"])
		if object["kind"] != "Pod" || object["name"] != created || !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(object["uid"])) {
			continue
		}
		switch event["reason"] {
		case "Started", "Created", "Pulled", "Pulling":
			return false
		case "FailedAttachVolume":
			message := deploymentplan.String(event["message"])
			blocked = blocked || strings.Contains(message, `"`+volume+`"`) && strings.Contains(message, owner+"-")
		}
	}
	return blocked
}

func executeStagingServiceCoreForward(ctx context.Context, cfg deploymentConfig, store secretStore, plan *environmentCreatePlan, e *environmentCreateExecution, forward *stagingServiceCoreForward) error {
	stage := stagingCoreEnrollmentStage + "-rwo-" + forward.OriginalSHA[:12]
	if err := applyEnvironmentCreateStage(ctx, cfg, store, plan, e, stage, forward.Definitions, os.Stdout); err != nil {
		return err
	}
	receipt := mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": forward.OriginalSHA, "original_failed_journal_sha256": forward.JournalSHA, "original_failed_job": forward.FailedJob, "original_failed_job_uid": forward.FailedUID})
	key := "kubernetes/" + stagingCoreEnrollmentStage
	if err := e.RecordReceipt(key, receipt); err != nil {
		return err
	}
	return e.CompleteEntry(key, time.Now().UTC())
}

func acceptedStagingCoreForward(store secretStore, e *environmentCreateExecution, entry environmentCreateJournalEntry, original *deploymentplan.Plan, journal deploymentplan.Journal) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	bad := createProviderRecoveryBlock("original core enrollment has no exact accepted volume forward receipt")
	var receipt map[string]string
	stage := "kubernetes/" + stagingCoreEnrollmentStage + "-rwo-" + original.SHA256()[:12]
	if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt["actual_qualified_forward_stage"] != stage || receipt["original_failed_plan_sha256"] != original.SHA256() || receipt["original_failed_journal_sha256"] != deploymentplan.Digest(journal) || journal.Failed == "" || receipt["original_failed_job"] != journal.Failed || receipt["original_failed_job_uid"] != journal.Owners[journal.Failed] {
		return nil, deploymentplan.Journal{}, bad
	}
	forward := createOpenBaoACKCompletionEntry(e.Journal, stage)
	if forward == nil || forward.State != "completed" {
		return nil, deploymentplan.Journal{}, bad
	}
	child, completed, err := originalCreateChildAtIntent(store, e, *forward)
	if err != nil || !createOpenBaoACKCompletedChild(child, completed) {
		return nil, deploymentplan.Journal{}, bad
	}
	return child, completed, nil
}
