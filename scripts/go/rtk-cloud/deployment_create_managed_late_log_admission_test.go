package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateAcceptedHelperAdmissionFixture(t *testing.T) (*createManagedLateSessionRecovery, *deploymentplan.Plan, deploymentplan.Journal, createManagedLateLogObservation) {
	t.Helper()
	_, _, late, _ := managedLateQualifiedPreviewsFixture(t)
	late.OriginalClientsSHA = late.LogProducer.OriginalClientsSHA
	// The qualified-reader fixture omits replicas because it does not launch
	// owners. This regression needs a real one-replica stop/restore transition.
	deploymentplan.Map(late.LogProducer.Original.Desired["spec"])["replicas"] = float64(1)
	deploymentplan.Map(deploymentplan.Map(late.LogProducer.Original.Desired["metadata"])["labels"])["app.kubernetes.io/managed-by"] = "rtk-cloud"
	parent := environmentCreatePlan{Environment: late.AcceptedCore.Environment, Stack: late.AcceptedCore.Stack, ConfigurationSHA256: late.LogHelperCandidate.ConfigurationSHA256}
	original := deploymentplan.Clone(late.LogProducer.Original.Desired)
	deploymentplan.Map(original["metadata"])["uid"] = late.LogProducer.Original.UID
	var err error
	late.LogStopCandidate, err = managedLateCompile(parent, "managed-late-log-stop", []deploymentplan.Definition{renderLogACKDraftQuiesce(late.LogProducer)}, []deploymentplan.Object{original})
	if err != nil {
		t.Fatal(err)
	}
	stopped := deploymentplan.Clone(original)
	deploymentplan.Map(stopped["spec"])["replicas"] = float64(0)
	late.LogRestoreCandidate, err = managedLateCompile(parent, "managed-late-log-restore", []deploymentplan.Definition{renderLogACKDraftRestore(late.LogProducer)}, []deploymentplan.Object{stopped})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateManagedLateLogCandidates(late); err != nil {
		t.Fatal("complete lifecycle fixture", err)
	}
	now := time.Now().UTC().Truncate(time.Second)
	state := late.LogProducer.NewSession
	state.Status, state.Fingerprint, state.Expires, state.Installed = "active", strings.Repeat("7", 64), now.Add(time.Hour), append([]string(nil), state.Subjects...)
	producer := *late.LogProducer
	producer.FreshReview, producer.NewSession = late.Review, state
	jobUID, podUID := "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", "bbbbbbbb-bbbb-4bbb-8bbb-bbbbbbbbbbbb"
	job := deploymentplan.Clone(late.LogHelperCandidate.Resources[0].Desired)
	meta := deploymentplan.Map(job["metadata"])
	meta["uid"], meta["resourceVersion"] = jobUID, "actual-native-version"
	job["status"] = deploymentplan.Object{"succeeded": float64(1), "conditions": []any{deploymentplan.Object{"type": "Complete", "status": "True"}}}
	pod := deploymentplan.Object{"kind": "Pod", "metadata": deploymentplan.Object{"name": "actual-terminal-helper", "namespace": meta["namespace"], "uid": podUID, "ownerReferences": []any{deploymentplan.Object{"kind": "Job", "name": meta["name"], "uid": jobUID, "controller": true}}}, "spec": deploymentplan.Clone(environmentPKIPod(job)), "status": deploymentplan.Object{"phase": "Succeeded", "containerStatuses": []any{deploymentplan.Object{"name": "app", "restartCount": float64(0), "state": deploymentplan.Object{"terminated": deploymentplan.Object{"exitCode": float64(0), "reason": "Completed", "startedAt": now.Add(-3 * time.Second).Format(time.RFC3339Nano), "finishedAt": now.Add(-time.Second).Format(time.RFC3339Nano)}}}}}}
	native, err := verifyLogACKDraftCompleted(&producer, jobUID, podUID, job, []deploymentplan.Object{pod}, state, producer.Identity, producer.OriginalClientsSHA, producer.Identity.StateSHA256, now)
	if err != nil {
		t.Fatal("genuine terminal fixture refused", err)
	}
	raw := mustProviderRecoveryJSON(state)
	native["native_session_raw_sha256"] = environmentPreflightBytesSHA256(raw)
	observation := createManagedLateLogObservation{Schema: "rtk-cloud.create-managed-late-log-observation/v1", Parent: late.ParentSHA, Review: late.Review, ProducerSHA: deploymentplan.Digest(late.LogProducer), ClaimUID: late.LogProducer.OriginalClaimUID, Job: job, Pod: pod, Identity: producer.Identity, Session: state, SessionRaw: raw, ClientsSHA: producer.OriginalClientsSHA, PublicLogSHA: strings.Repeat("8", 64), Native: native}
	plan := late.LogHelperCandidate
	journal := deploymentplan.Journal{PlanSHA256: plan.SHA256(), Accepted: true, AcceptanceStarted: true, Completed: []string{plan.Resources[0].ID}, Owners: map[string]string{plan.Resources[0].ID: jobUID}}
	return late, plan, journal, observation
}

type managedLateHelperAdmissionBackend struct {
	body          deploymentplan.Object
	reads, writes int
}

func (b *managedLateHelperAdmissionBackend) Read(context.Context, string) (deploymentplan.Object, error) {
	b.reads++
	return deploymentplan.Clone(b.body), nil
}
func (b *managedLateHelperAdmissionBackend) Write(context.Context, string, deploymentplan.Object, bool) (deploymentplan.Object, error) {
	b.writes++
	return nil, nil
}
func (b *managedLateHelperAdmissionBackend) Wait(context.Context, deploymentplan.Resource) error {
	return nil
}

func TestManagedLateLogAcceptedHelperAdmissionRetainsActualAllocationWithoutReplay(t *testing.T) {
	late, plan, journal, observation := managedLateAcceptedHelperAdmissionFixture(t)
	before := deploymentplan.Digest([]any{late, plan, journal, observation})
	backend := &managedLateHelperAdmissionBackend{body: observation.Job}
	if plan.Admit(context.Background(), backend) == nil {
		t.Fatal("fixture failed to reproduce historical absent-owner admission drift")
	}
	view, err := managedLateRetainedLogHelperPlan(late, plan, journal, observation)
	if err != nil {
		t.Fatal(err)
	}
	if view.Resources[0].Action != "retain" || view.Resources[0].UID != journal.Owners[plan.Resources[0].ID] || deploymentplan.Digest(view.Resources[0].Desired) != deploymentplan.Digest(plan.Resources[0].Desired) || view.Admit(context.Background(), backend) != nil || backend.writes != 0 {
		t.Fatal("accepted helper was recreated, modified or not reread")
	}
	if before != deploymentplan.Digest([]any{late, plan, journal, observation}) || plan.Resources[0].Action != "create" || plan.Resources[0].UID != "" || plan.Resources[0].Baseline != nil {
		t.Fatal("immutable historical create authority was rewritten")
	}
	for _, mode := range []string{"replaced-uid", "changed-image", "deleted"} {
		t.Run(mode, func(t *testing.T) {
			body := deploymentplan.Clone(observation.Job)
			switch mode {
			case "replaced-uid":
				deploymentplan.Map(body["metadata"])["uid"] = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
			case "changed-image":
				managedNamedField(environmentPKIPod(body)["containers"], "app")["image"] = "foreign@sha256:" + strings.Repeat("f", 64)
			case "deleted":
				body = nil
			}
			backend := &managedLateHelperAdmissionBackend{body: body}
			if view.Admit(context.Background(), backend) == nil || backend.writes != 0 {
				t.Fatal("current owner/body drift passed or wrote")
			}
		})
	}
}

func TestManagedLateLogAcceptedHelperAdmissionRejectsUnprovedAcceptanceAndProcess(t *testing.T) {
	for _, mode := range []string{"unaccepted", "acceptance-not-started", "inflight", "failed", "wrong-plan", "wrong-owner", "missing-owner", "deleted-job", "changed-job", "pod-running", "pod-restart", "no-native-ACK", "changed-private-state", "changed-observation-native"} {
		t.Run(mode, func(t *testing.T) {
			late, plan, journal, observation := managedLateAcceptedHelperAdmissionFixture(t)
			switch mode {
			case "unaccepted":
				journal.Accepted = false
			case "acceptance-not-started":
				journal.AcceptanceStarted = false
			case "inflight":
				journal.InFlight = plan.Resources[0].ID
			case "failed":
				journal.Failed = plan.Resources[0].ID
			case "wrong-plan":
				journal.PlanSHA256 = strings.Repeat("f", 64)
			case "wrong-owner":
				journal.Owners[plan.Resources[0].ID] = "cccccccc-cccc-4ccc-8ccc-cccccccccccc"
			case "missing-owner":
				delete(journal.Owners, plan.Resources[0].ID)
			case "deleted-job":
				deploymentplan.Map(observation.Job["metadata"])["deletionTimestamp"] = time.Now().UTC().Format(time.RFC3339Nano)
			case "changed-job":
				managedNamedField(environmentPKIPod(observation.Job)["containers"], "app")["image"] = "private-unapproved-canary"
			case "pod-running":
				deploymentplan.Map(observation.Pod["status"])["phase"] = "Running"
			case "pod-restart":
				deploymentplan.Map(certIssuerObjectList(deploymentplan.Map(observation.Pod["status"])["containerStatuses"])[0])["restartCount"] = float64(1)
			case "no-native-ACK":
				observation.Session.Installed = nil
				observation.SessionRaw = mustProviderRecoveryJSON(observation.Session)
			case "changed-private-state":
				observation.Identity.StateSHA256 = strings.Repeat("f", 64)
			case "changed-observation-native":
				observation.Native["private_canary"] = "never expose"
			}
			before := deploymentplan.Digest([]any{late, plan, journal, observation})
			if _, err := managedLateRetainedLogHelperPlan(late, plan, journal, observation); err == nil || strings.Contains(err.Error(), "canary") || before != deploymentplan.Digest([]any{late, plan, journal, observation}) {
				t.Fatal("unproved helper acceptance/process admitted, leaked or modified")
			}
		})
	}
}
