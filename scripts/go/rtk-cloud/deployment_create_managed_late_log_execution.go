package main

// Native execution requires the separately admitted distinct-session source
// and its exact four plans before changing the retained log owner.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type createManagedLateLogObservation struct {
	Schema, Parent, Review, ProducerSHA, ClaimUID string
	Job, Pod                                      deploymentplan.Object
	Identity                                      logACKNativeIdentity
	Session                                       managedSessionState
	SessionRaw                                    json.RawMessage
	ClientsSHA, PublicLogSHA                      string
	Native                                        deploymentplan.Object
}

// The caller owns source-profile/native lease authentication. This callback is
// required at every mutation boundary, and must inspect only the actual admitted
// late lifecycle progress (not require the obsolete e0 owner bodies).
type managedLateLogRuntimeCheck func(context.Context) error

type createManagedLateLogFinalSession struct {
	Schema, Parent, Review string
	Raw                    json.RawMessage
}

// A terminal helper observation is immutable even while other genuine native
// acknowledgments arrive. Reuse its exact historical session bytes; current
// readback must only advance the installation set, preserving every identity.
func validateManagedLateLogObservation(late *createManagedLateSessionRecovery, observation createManagedLateLogObservation, helperUID string) error {
	if late == nil || late.LogProducer == nil || observation.Schema != "rtk-cloud.create-managed-late-log-observation/v1" || observation.Parent != late.ParentSHA || observation.Review != late.Review || observation.ProducerSHA != deploymentplan.Digest(late.LogProducer) || observation.ClaimUID != late.LogProducer.OriginalClaimUID || observation.ClientsSHA != late.OriginalClientsSHA || !environmentPreflightDigest.MatchString(observation.PublicLogSHA) || verifyLogACKDraftIdentity(observation.Identity, late.LogProducer.Identity) != nil {
		return logACKDraftBlock("stored terminal source/identity")
	}
	var state managedSessionState
	if decodeEnvironmentJSON(observation.SessionRaw, &state) != nil || deploymentplan.Digest(state) != deploymentplan.Digest(observation.Session) || state.Session != late.NewSessionID || state.Issuer != late.LogProducer.NewSession.Issuer || state.Root != late.LogProducer.NewSession.Root || !slices.Equal(state.Subjects, late.LogProducer.NewSession.Subjects) || !environmentPreflightDigest.MatchString(state.Fingerprint) {
		return logACKDraftBlock("stored terminal raw native session")
	}
	finished, err := time.Parse(time.RFC3339Nano, deploymentplan.String(observation.Native["native_finished_at"]))
	if err != nil {
		return logACKDraftBlock("stored terminal native completion time")
	}
	producer := *late.LogProducer
	producer.NewSession, producer.FreshReview = state, late.Review
	podUID := deploymentplan.String(deploymentplan.Map(observation.Pod["metadata"])["uid"])
	native, err := verifyLogACKDraftCompleted(&producer, helperUID, podUID, observation.Job, []deploymentplan.Object{observation.Pod}, state, observation.Identity, observation.ClientsSHA, observation.Identity.StateSHA256, finished.Add(time.Nanosecond))
	if err != nil {
		return err
	}
	rawSHA := deploymentplan.String(observation.Native["native_session_raw_sha256"])
	if !managedLateLogSessionHashesMatch(observation.SessionRaw, rawSHA) {
		return logACKDraftBlock("stored terminal raw native session hash")
	}
	native["native_session_raw_sha256"] = rawSHA
	if deploymentplan.Digest(native) != deploymentplan.Digest(observation.Native) {
		return logACKDraftBlock("stored genuine terminal native evidence")
	}
	return nil
}

func managedLateLogObservationCurrent(stored, current createManagedLateLogObservation) error {
	if stored.Parent != current.Parent || stored.Review != current.Review || stored.ProducerSHA != current.ProducerSHA || stored.ClaimUID != current.ClaimUID || stored.PublicLogSHA != current.PublicLogSHA || stored.ClientsSHA != current.ClientsSHA || deploymentplan.Digest(stored.Identity) != deploymentplan.Digest(current.Identity) || stored.Session.Session != current.Session.Session || stored.Session.Issuer != current.Session.Issuer || stored.Session.Root != current.Session.Root || stored.Session.Fingerprint != current.Session.Fingerprint || !stored.Session.Expires.Equal(current.Session.Expires) || current.Session.Status != "active" || current.Session.OtherActive != 0 || !slices.Equal(stored.Session.Subjects, current.Session.Subjects) {
		return logACKDraftBlock("current terminal helper source changed")
	}
	for _, subject := range stored.Session.Installed {
		if !slices.Contains(current.Session.Installed, subject) {
			return logACKDraftBlock("current native installation set regressed")
		}
	}
	before, after := deploymentplan.Clone(stored.Native), deploymentplan.Clone(current.Native)
	for _, key := range []string{"native_session_sha256", "native_session_raw_sha256"} {
		delete(before, key)
		delete(after, key)
	}
	if deploymentplan.Digest(before) != deploymentplan.Digest(after) {
		return logACKDraftBlock("current actual helper process or private identity changed")
	}
	return nil
}

func managedLateLogFile(store secretStore, review, name string) string {
	return filepath.Join(store.Root, "deployment/create/openbao-recovery", review, name)
}

func managedLateLogEntry(e *environmentCreateExecution, key string) (*environmentCreateJournalEntry, error) {
	var found *environmentCreateJournalEntry
	for i := range e.Journal.Entries {
		if e.Journal.Entries[i].Key != key {
			continue
		}
		if found != nil {
			return nil, logACKDraftBlock("duplicate lifecycle entry")
		}
		found = &e.Journal.Entries[i]
	}
	return found, nil
}

func managedLateLogAccepted(store secretStore, e *environmentCreateExecution, late *createManagedLateSessionRecovery, kind, helperUID string) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	var empty deploymentplan.Journal
	key := "kubernetes/managed-late-log-" + kind + "-" + late.Review
	entry, err := managedLateLogEntry(e, key)
	if err != nil || entry == nil || entry.State != "completed" {
		return nil, empty, logACKDraftBlock("lifecycle child not genuinely accepted")
	}
	actual, journal, err := managedTransportPostAcceptedStage(store, e, key)
	if err != nil || len(actual.Resources) != 1 || !journal.Accepted || journal.Failed != "" || journal.InFlight != "" || len(journal.Completed) != 1 || len(journal.Owners) != 1 {
		return nil, journal, logACKDraftBlock("one-resource accepted lifecycle child")
	}
	var wanted *deploymentplan.Plan
	switch kind {
	case "stop":
		wanted = late.LogStopCandidate
	case "existing-ack":
		wanted = late.LogHelperCandidate
	case "restore":
		wanted = late.LogRestoreCandidate
	case "retire":
		if validateManagedLateLogRetireAllocated(late, actual, helperUID) != nil {
			return nil, journal, logACKDraftBlock("retire only actual accepted helper UID")
		}
	default:
		return nil, journal, logACKDraftBlock("unknown lifecycle child")
	}
	if wanted != nil && !managedCoreSameQualifiedPlan(actual, wanted) {
		return nil, journal, logACKDraftBlock("accepted child differs from qualified candidate")
	}
	resource := actual.Resources[0]
	uid := journal.Owners[resource.ID]
	if !environmentBootstrapOwnerUUID.MatchString(uid) || resource.UID != "" && resource.UID != uid || kind != "existing-ack" && kind != "retire" && uid != late.LogProducer.Original.UID || kind == "retire" && uid != helperUID || kind == "existing-ack" && helperUID != "" && uid != helperUID {
		return nil, journal, logACKDraftBlock("accepted lifecycle owner binding")
	}
	return actual, journal, nil
}

func applyManagedLateLogExact(ctx context.Context, runtime environmentCreateBootstrapRuntime, late *createManagedLateSessionRecovery, kind string, definition deploymentplan.Definition, preview *deploymentplan.Plan, helperUID string, apply environmentCreateBootstrapApply, check managedLateLogRuntimeCheck) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	var empty deploymentplan.Journal
	e := runtime.Execution
	stage := "managed-late-log-" + kind + "-" + late.Review
	entry, err := managedLateLogEntry(e, "kubernetes/"+stage)
	if err != nil {
		return nil, empty, err
	}
	if entry != nil {
		if entry.State != "completed" {
			// A failed/pending helper cannot be recreated, and an uncertain
			// deletion cannot authorize business restoration. A new exact
			// source-profile qualification must classify that actual failure.
			return nil, empty, logACKDraftBlock("unfinished lifecycle requires source requalification")
		}
		return managedLateLogAccepted(e.Store, e, late, kind, helperUID)
	}
	if check == nil || check(ctx) != nil || e.Check(ctx) != nil {
		return nil, empty, logACKDraftBlock("fresh admitted source at mutation boundary")
	}
	live, err := runtime.Backend.Inventory(ctx, e.Journal.Stack)
	if err != nil {
		return nil, empty, logACKDraftBlock("native lifecycle inventory")
	}
	fresh, err := deploymentplan.Compile(preview.Inputs, []deploymentplan.Definition{definition}, live)
	if err != nil {
		return nil, empty, logACKDraftBlock("native candidate compilation")
	}
	if kind == "retire" {
		if validateManagedLateLogRetireAllocated(late, fresh, helperUID) != nil {
			return nil, empty, logACKDraftBlock("actual helper retirement preview")
		}
	} else if fresh.SHA256() != preview.SHA256() {
		return nil, empty, logACKDraftBlock("current exact lifecycle baseline changed")
	}
	if err := apply(ctx, stage, []deploymentplan.Definition{definition}); err != nil {
		return nil, empty, err
	}
	return managedLateLogAccepted(e.Store, e, late, kind, helperUID)
}

// Reject every unclassified holder, including terminating Pods and foreign
// Job/Deployment owners. Only the separate authenticated successful-history
// proof may remove a finished Pod from this inventory. Native LOCK_NB remains
// an independent final exclusive-owner guard.
func managedLateLogClaimHolders(objects []deploymentplan.Object, namespace, claim string, allowedPodUID string) error {
	for _, object := range objects {
		if object["kind"] != "Pod" || deploymentplan.Map(object["metadata"])["namespace"] != namespace {
			continue
		}
		for _, value := range certIssuerObjectList(deploymentplan.Map(object["spec"])["volumes"]) {
			volume := deploymentplan.Map(value)
			if deploymentplan.Map(volume["persistentVolumeClaim"])["claimName"] == claim && deploymentplan.Map(object["metadata"])["uid"] != allowedPodUID {
				return logACKDraftBlock("original private claim still has a native Pod holder")
			}
		}
	}
	return nil
}

// PVCs are namespaced. Read every Pod in that exact namespace, including
// unlabeled, terminal, foreign-owner and deleting Pods; never select by owner.
func readManagedLateLogClaimPods(ctx context.Context, b environmentKubernetesBackend, namespace string) ([]deploymentplan.Object, error) {
	if namespace == "" {
		return nil, logACKDraftBlock("claim holder Pod inventory namespace")
	}
	raw, err := b.command(ctx, nil, "get", "pods", "--namespace", namespace, "-o", "json")
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return nil, logACKDraftBlock("claim holder Pod inventory deadline")
		}
		if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
			return nil, logACKDraftBlock("claim holder Pod inventory cancelled")
		}
		return nil, logACKDraftBlock("claim holder Pod inventory command")
	}
	return decodeManagedLateLogClaimPods(raw, namespace)
}

func decodeManagedLateLogClaimPods(raw []byte, namespace string) ([]deploymentplan.Object, error) {
	var list struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		Metadata   struct {
			Continue  string `json:"continue"`
			Remaining *int64 `json:"remainingItemCount"`
			// Native List metadata varies between API and kubectl aggregation.
			ResourceVersion string `json:"resourceVersion"`
			SelfLink        string `json:"selfLink"`
		} `json:"metadata"`
		Items []deploymentplan.Object `json:"items"`
	}
	if namespace == "" || len(raw) > 32<<20 || decodeEnvironmentJSON(raw, &list) != nil || list.APIVersion != "v1" || list.Kind != "List" && list.Kind != "PodList" || list.Items == nil || list.Metadata.Continue != "" || list.Metadata.Remaining != nil && *list.Metadata.Remaining != 0 {
		return nil, logACKDraftBlock("claim holder Pod inventory envelope")
	}
	names, uids := map[string]bool{}, map[string]bool{}
	for _, pod := range list.Items {
		metadata := deploymentplan.Map(pod["metadata"])
		name, uid := deploymentplan.String(metadata["name"]), deploymentplan.String(metadata["uid"])
		spec := deploymentplan.Map(pod["spec"])
		if pod["apiVersion"] != "v1" || pod["kind"] != "Pod" || metadata["namespace"] != namespace || name == "" || !environmentBootstrapOwnerUUID.MatchString(uid) || names[name] || uids[uid] || spec == nil {
			return nil, logACKDraftBlock("claim holder Pod inventory identity")
		}
		names[name], uids[uid] = true, true
		volumes, ok := spec["volumes"].([]any)
		if spec["volumes"] != nil && !ok {
			return nil, logACKDraftBlock("claim holder Pod inventory volumes")
		}
		volumeNames := map[string]bool{}
		for _, value := range volumes {
			volume := deploymentplan.Map(value)
			name := deploymentplan.String(volume["name"])
			if name == "" || volumeNames[name] {
				return nil, logACKDraftBlock("claim holder Pod inventory volumes")
			}
			volumeNames[name] = true
			if source, present := volume["persistentVolumeClaim"]; present {
				pvc := deploymentplan.Map(source)
				if pvc == nil || deploymentplan.String(pvc["claimName"]) == "" {
					return nil, logACKDraftBlock("claim holder Pod inventory volumes")
				}
			}
		}
	}
	return list.Items, nil
}

// The parent receipt locates a candidate; the existing accepted-stage reader
// authenticates its immutable plan, exact write journal and original owner.
// No Job name, terminal status or UID observed from Kubernetes is authority.
func managedLateLogRecordedClaimJob(store secretStore, e *environmentCreateExecution, pod deploymentplan.Object, before time.Time) (deploymentplan.Resource, error) {
	var found deploymentplan.Resource
	meta := deploymentplan.Map(pod["metadata"])
	refs := certIssuerObjectList(meta["ownerReferences"])
	if e == nil || before.IsZero() || len(refs) != 1 {
		return found, nil
	}
	ref := deploymentplan.Map(refs[0])
	uid, name := deploymentplan.String(ref["uid"]), deploymentplan.String(ref["name"])
	if ref["controller"] != true || ref["kind"] != "Job" || ref["apiVersion"] != "batch/v1" || name == "" || !environmentBootstrapOwnerUUID.MatchString(uid) {
		return found, nil
	}
	id := "Job/" + deploymentplan.String(meta["namespace"]) + "/" + name
	for _, entry := range e.Journal.Entries {
		if !strings.HasPrefix(entry.Key, "kubernetes/") || entry.State != "completed" || entry.StartedAt.IsZero() || entry.CompletedAt.IsZero() || !entry.StartedAt.Before(entry.CompletedAt) || !entry.CompletedAt.Before(before) {
			continue
		}
		var receipt struct {
			Owners map[string]string `json:"owners"`
		}
		if json.Unmarshal(entry.Receipt, &receipt) != nil || receipt.Owners[id] != uid {
			continue
		}
		plan, journal, err := managedTransportPostAcceptedStage(store, e, entry.Key)
		if err != nil {
			return found, logACKDraftBlock("historical claim Job accepted authority")
		}
		for _, resource := range plan.Resources {
			if resource.ID != id {
				continue
			}
			if journal.Owners[id] != uid || resource.Desired["kind"] != "Job" || resource.Desired["apiVersion"] != "batch/v1" || deploymentplan.ID(resource.Desired) != id || found.ID != "" && (found.UID != uid || deploymentplan.Digest(found.Desired) != deploymentplan.Digest(resource.Desired)) {
				return found, logACKDraftBlock("historical claim Job owner or recipe")
			}
			resource.UID = uid
			found = resource
		}
	}
	return found, nil
}

// A successful historical Pod has no process left to own the claim. This is
// deliberately narrower than the general historical terminal-workload rule:
// failed/retried, deleting, unrecorded or incompletely observed Pods still hold.
func managedLateLogSuccessfulClaimHistory(owner deploymentplan.Resource, job, pod deploymentplan.Object, before time.Time) bool {
	jm, pm := deploymentplan.Map(job["metadata"]), deploymentplan.Map(pod["metadata"])
	js, ps := deploymentplan.Map(job["status"]), deploymentplan.Map(pod["status"])
	if before.IsZero() || owner.ID == "" || !environmentBootstrapOwnerUUID.MatchString(owner.UID) || owner.ID != deploymentplan.ID(job) || owner.Desired["kind"] != "Job" || job["apiVersion"] != "batch/v1" || job["kind"] != "Job" || jm["uid"] != owner.UID || jm["deletionTimestamp"] != nil || !deploymentplan.Matches(owner.Desired, job) || pod["apiVersion"] != "v1" || pod["kind"] != "Pod" || pm["namespace"] != jm["namespace"] || !environmentBootstrapOwnerUUID.MatchString(deploymentplan.String(pm["uid"])) || pm["deletionTimestamp"] != nil || ps["phase"] != "Succeeded" || js["active"] != nil && js["active"] != float64(0) || js["failed"] != nil && js["failed"] != float64(0) || js["succeeded"] != float64(1) {
		return false
	}
	for _, value := range certIssuerObjectList(jm["ownerReferences"]) {
		if deploymentplan.Map(value)["controller"] == true {
			return false
		}
	}
	refs := certIssuerObjectList(pm["ownerReferences"])
	if len(refs) != 1 {
		return false
	}
	ref := deploymentplan.Map(refs[0])
	if ref["controller"] != true || ref["kind"] != "Job" || ref["apiVersion"] != job["apiVersion"] || ref["name"] != jm["name"] || ref["uid"] != owner.UID {
		return false
	}
	complete, conditions := false, map[string]bool{}
	for _, value := range certIssuerObjectList(js["conditions"]) {
		condition := deploymentplan.Map(value)
		kind := deploymentplan.String(condition["type"])
		if kind == "" || conditions[kind] || kind == "Failed" && condition["status"] == "True" {
			return false
		}
		conditions[kind] = true
		complete = complete || kind == "Complete" && condition["status"] == "True"
	}
	finished, err := time.Parse(time.RFC3339Nano, deploymentplan.String(js["completionTime"]))
	if !complete || err != nil || finished.IsZero() || !finished.Before(before) {
		return false
	}
	expected, actual := environmentPKIPod(owner.Desired), deploymentplan.Map(pod["spec"])
	if actual["restartPolicy"] != "Never" || !deploymentplan.Matches(deploymentplan.Object{"kind": "Pod", "spec": expected}, deploymentplan.Object{"kind": "Pod", "spec": actual}) {
		return false
	}
	for _, source := range []struct {
		object deploymentplan.Object
		field  string
	}{{expected, "ephemeralContainers"}, {actual, "ephemeralContainers"}, {ps, "ephemeralContainerStatuses"}} {
		if source.object[source.field] != nil {
			if values, ok := source.object[source.field].([]any); !ok || len(values) != 0 {
				return false
			}
		}
	}
	for _, field := range []string{"containers", "initContainers"} {
		declared, containers := certIssuerObjectList(expected[field]), certIssuerObjectList(actual[field])
		statuses := certIssuerObjectList(ps[map[string]string{"containers": "containerStatuses", "initContainers": "initContainerStatuses"}[field]])
		if len(declared) != len(containers) || len(declared) != len(statuses) || field == "containers" && len(declared) == 0 {
			return false
		}
		seen := map[string]bool{}
		for _, value := range declared {
			container := deploymentplan.Map(value)
			name, image := deploymentplan.String(container["name"]), deploymentplan.String(container["image"])
			if name == "" || seen[name] || !immutableEnvironmentImage.MatchString(image) || container["restartPolicy"] == "Always" {
				return false
			}
			seen[name] = true
			var observed, state deploymentplan.Object
			for _, value := range containers {
				candidate := deploymentplan.Map(value)
				if candidate["name"] == name {
					if observed != nil {
						return false
					}
					observed = candidate
				}
			}
			for _, value := range statuses {
				candidate := deploymentplan.Map(value)
				if candidate["name"] == name {
					if state != nil {
						return false
					}
					state = candidate
				}
			}
			terminal := deploymentplan.Map(deploymentplan.Map(state["state"])["terminated"])
			startedAt, startErr := time.Parse(time.RFC3339Nano, deploymentplan.String(terminal["startedAt"]))
			finishedAt, finishErr := time.Parse(time.RFC3339Nano, deploymentplan.String(terminal["finishedAt"]))
			if observed["image"] != image || state == nil || state["restartCount"] != float64(0) || deploymentplan.String(state["containerID"]) == "" || deploymentplan.String(state["imageID"]) == "" || terminal["containerID"] != state["containerID"] || len(deploymentplan.Map(state["state"])) != 1 || len(deploymentplan.Map(state["lastState"])) != 0 || terminal["exitCode"] != float64(0) || terminal["signal"] != nil && terminal["signal"] != float64(0) || startErr != nil || finishErr != nil || startedAt.IsZero() || finishedAt.IsZero() || finishedAt.Before(startedAt) || !finishedAt.Before(before) || finished.Before(finishedAt) {
				return false
			}
		}
	}
	return true
}

func managedLateLogClaimActivePods(ctx context.Context, runtime environmentCreateBootstrapRuntime, late *createManagedLateSessionRecovery, objects []deploymentplan.Object, namespace, claim string) ([]deploymentplan.Object, error) {
	if managedLateLogClaimHolders(objects, namespace, claim, "") == nil {
		return objects, nil
	}
	if _, _, err := managedLateLogAccepted(runtime.Execution.Store, runtime.Execution, late, "stop", ""); err != nil {
		return nil, err
	}
	stop, err := managedLateLogEntry(runtime.Execution, "kubernetes/managed-late-log-stop-"+late.Review)
	if err != nil || stop == nil || stop.StartedAt.IsZero() {
		return nil, logACKDraftBlock("accepted Log stop time for historical claim")
	}
	active := make([]deploymentplan.Object, 0, len(objects))
	jobs := map[string]deploymentplan.Object{}
	for _, pod := range objects {
		if managedLateLogClaimHolders([]deploymentplan.Object{pod}, namespace, claim, "") == nil || deploymentplan.Map(pod["status"])["phase"] != "Succeeded" {
			active = append(active, pod)
			continue
		}
		owner, err := managedLateLogRecordedClaimJob(runtime.Execution.Store, runtime.Execution, pod, stop.StartedAt)
		if err != nil {
			return nil, err
		}
		if owner.ID == "" {
			active = append(active, pod)
			continue
		}
		if jobs[owner.ID] == nil {
			jobs[owner.ID], err = runtime.Backend.Read(ctx, owner.ID)
			if err != nil {
				return nil, logACKDraftBlock("historical claim Job live read")
			}
		}
		if !managedLateLogSuccessfulClaimHistory(owner, jobs[owner.ID], pod, stop.StartedAt) {
			active = append(active, pod)
		}
	}
	return active, nil
}

func waitManagedLateLogClaimEmpty(ctx context.Context, runtime environmentCreateBootstrapRuntime, late *createManagedLateSessionRecovery) error {
	ledger, err := readEnvironmentCreateLedger(runtime.Execution)
	if err != nil {
		return err
	}
	claim, err := managedLateLogClaim(late.LogProducer.Original.Desired, ledger)
	if err != nil || claim.PVCUID != late.LogProducer.OriginalClaimUID {
		return logACKDraftBlock("original private claim ledger")
	}
	if claim.Namespace == "" || claim.PVCName == "" {
		return logACKDraftBlock("original private claim ID")
	}
	bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	for {
		objects, readErr := readManagedLateLogClaimPods(bounded, runtime.Backend, claim.Namespace)
		if readErr != nil {
			return readErr
		}
		objects, readErr = managedLateLogClaimActivePods(bounded, runtime, late, objects, claim.Namespace, claim.PVCName)
		if readErr != nil {
			return readErr
		}
		if managedLateLogClaimHolders(objects, claim.Namespace, claim.PVCName, "") == nil {
			return runtime.Execution.Check(bounded)
		}
		timer := time.NewTimer(2 * time.Second)
		select {
		case <-bounded.Done():
			timer.Stop()
			return logACKDraftBlock("original/helper Pods not fully gone")
		case <-timer.C:
		}
	}
}

func readManagedLateLogObservation(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, late *createManagedLateSessionRecovery, producer *createLogACKHelperDraft, helperUID string) (createManagedLateLogObservation, error) {
	var result createManagedLateLogObservation
	jobID := late.LogHelperCandidate.Resources[0].ID
	job, err := runtime.Backend.Read(ctx, jobID)
	if err != nil || job == nil || deploymentplan.Map(job["metadata"])["uid"] != helperUID {
		return result, logACKDraftBlock("actual helper Job absent or replaced")
	}
	raw, err := runtime.Backend.command(ctx, nil, "--namespace", runtime.Execution.Journal.Stack+"-video-cloud", "get", "pods", "-l", "job-name="+producer.helperName(), "-o", "json")
	var list struct {
		Items []deploymentplan.Object `json:"items"`
	}
	if err != nil || json.Unmarshal(raw, &list) != nil || len(list.Items) != 1 {
		return result, logACKDraftBlock("one actual helper descendant")
	}
	pod := list.Items[0]
	meta := deploymentplan.Map(pod["metadata"])
	podUID, podName := deploymentplan.String(meta["uid"]), deploymentplan.String(meta["name"])
	if !environmentBootstrapOwnerUUID.MatchString(podUID) || podName == "" {
		return result, logACKDraftBlock("actual helper Pod metadata")
	}
	logs, err := runtime.Backend.command(ctx, nil, "--namespace", runtime.Execution.Journal.Stack+"-video-cloud", "logs", podName, "--container", "app")
	identity, markerErr := readLogACKDraftMarkers(logs)
	if err != nil || markerErr != nil {
		return result, logACKDraftBlock("helper native public owner-release markers")
	}
	state, nativeSessionRaw, err := readManagedSessionState(ctx, cfg, runtime, late.NewSessionID)
	if err != nil {
		return result, err
	}
	nativeSessionRaw, err = managedLateCompactLogSessionRaw(nativeSessionRaw)
	if err != nil {
		return result, err
	}
	clients, err := readManagedSessionClients(ctx, cfg, runtime, runtime.ServiceSessionRecovery.Ceremony, state.Subjects)
	if err != nil {
		return result, err
	}
	native, err := verifyLogACKDraftCompleted(producer, helperUID, podUID, job, list.Items, state, identity, clients, identity.StateSHA256, time.Now().UTC())
	if err != nil {
		return result, err
	}
	native["native_session_raw_sha256"] = environmentPreflightBytesSHA256(nativeSessionRaw)
	result = createManagedLateLogObservation{Schema: "rtk-cloud.create-managed-late-log-observation/v1", Parent: late.ParentSHA, Review: late.Review, ProducerSHA: deploymentplan.Digest(late.LogProducer), ClaimUID: late.LogProducer.OriginalClaimUID, Job: job, Pod: pod, Identity: identity, Session: state, SessionRaw: append(json.RawMessage(nil), nativeSessionRaw...), ClientsSHA: clients, PublicLogSHA: environmentPreflightBytesSHA256(logs), Native: native}
	return result, runtime.Execution.Check(ctx)
}

// The allocated session is assigned only to this runtime copy. The admitted
// descriptor retains qualified-not-issued fields and zero prospective receipts.
func executeManagedLateLogACK(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, late *createManagedLateSessionRecovery, apply environmentCreateBootstrapApply, check managedLateLogRuntimeCheck) (*createManagedLateLogProof, error) {
	if late == nil || runtime.Execution == nil || check == nil || !environmentPreflightDigest.MatchString(late.Review) || validateManagedLateLogCandidates(late) != nil {
		return nil, logACKDraftBlock("actual admitted four-plan producer")
	}
	e, store := runtime.Execution, runtime.Execution.Store
	if entry, err := managedLateLogEntry(e, "managed-late-log-verified-"+late.Review); err != nil {
		return nil, err
	} else if entry != nil {
		if entry.State != "completed" {
			return nil, logACKDraftBlock("native Log completion remains unfinished")
		}
		return readManagedLateLogCompletion(store, e, late, time.Now().UTC())
	}
	state, _, err := readManagedSessionState(ctx, cfg, runtime, late.NewSessionID)
	if err != nil {
		return nil, err
	}
	producer := *late.LogProducer
	producer.FreshReview, producer.NewSession = late.Review, state
	if err := validateLogACKDraftSource(&producer, late.LogProducer.NewSession.Subjects, time.Now().UTC()); err != nil {
		return nil, err
	}
	// Only before first stop: inspect the original business owner10 retained at e0,
	// and prove no new key/CSR/pending state exists immediately before quiescing.
	stopKey := "kubernetes/managed-late-log-stop-" + late.Review
	if entry, err := managedLateLogEntry(e, stopKey); err != nil {
		return nil, err
	} else if entry == nil {
		owner := producer.Original // owner10 retains this original e0 Log template
		pod, err := readManagedLateLogSourcePod(ctx, runtime.Backend, owner)
		if err != nil {
			return nil, err
		}
		identity, err := readLogACKDraftIdentity(ctx, runtime.Backend, deploymentplan.String(deploymentplan.Map(pod["metadata"])["name"]), producer.Identity.RootSHA256)
		if err != nil || verifyLogACKDraftIdentity(identity, producer.Identity) != nil {
			return nil, logACKDraftBlock("fresh original installed identity before stop")
		}
	}
	_, stopJournal, err := applyManagedLateLogExact(ctx, runtime, late, "stop", renderLogACKDraftQuiesce(&producer), late.LogStopCandidate, "", apply, check)
	if err != nil {
		return nil, err
	}
	if prior, entryErr := managedLateLogEntry(e, "kubernetes/managed-late-log-existing-ack-"+late.Review); entryErr != nil {
		return nil, entryErr
	} else if prior == nil {
		if err := waitManagedLateLogClaimEmpty(ctx, runtime, late); err != nil {
			return nil, err
		}
	}
	// Recheck the entire watchdog+grace+margin from the ACTUAL helper mutation
	// boundary. Quiescence time cannot consume the not-due/session lifetime guard.
	currentSession, _, sessionErr := readManagedSessionState(ctx, cfg, runtime, late.NewSessionID)
	if sessionErr != nil || currentSession.Fingerprint != producer.NewSession.Fingerprint || !currentSession.Expires.Equal(producer.NewSession.Expires) {
		return nil, logACKDraftBlock("actual allocated helper session changed")
	}
	producer.NewSession = currentSession
	if err := validateLogACKDraftSource(&producer, late.LogProducer.NewSession.Subjects, time.Now().UTC()); err != nil {
		return nil, err
	}
	helper, err := renderLogACKDraftHelper(&producer)
	if err != nil {
		return nil, err
	}
	_, helperJournal, err := applyManagedLateLogExact(ctx, runtime, late, "existing-ack", helper, late.LogHelperCandidate, "", apply, check)
	if err != nil {
		return nil, err
	}
	helperUID := helperJournal.Owners[late.LogHelperCandidate.Resources[0].ID]
	observationPath := managedLateLogFile(store, late.Review, "managed-late-log-observation.json")
	var observation createManagedLateLogObservation
	if retire, entryErr := managedLateLogEntry(e, "kubernetes/managed-late-log-retire-"+late.Review); entryErr != nil {
		return nil, entryErr
	} else if retire == nil {
		current, readErr := readManagedLateLogObservation(ctx, cfg, runtime, late, &producer, helperUID)
		if readErr != nil {
			return nil, readErr
		}
		if _, statErr := os.Lstat(observationPath); os.IsNotExist(statErr) {
			observation = current
			invalidateManagedLateSourceReuse(e)
			if err := writeManagedLateLogImmutable(store, observationPath, observation); err != nil {
				return nil, err
			}
		} else if statErr != nil || validateEnvironmentPreflightDirectories(store, filepath.Dir(observationPath)) != nil || readExecutionJSON(observationPath, &observation) != nil || validateManagedLateLogObservation(late, observation, helperUID) != nil || managedLateLogObservationCurrent(observation, current) != nil {
			return nil, logACKDraftBlock("stored terminal helper observation or current continuity differs")
		}
	} else if validateEnvironmentPreflightDirectories(store, filepath.Dir(observationPath)) != nil || readExecutionJSON(observationPath, &observation) != nil {
		return nil, logACKDraftBlock("retired helper lacks immutable prior observation")
	}
	if err := validateManagedLateLogObservation(late, observation, helperUID); err != nil {
		return nil, err
	}
	retire := deploymentplan.Definition{Phase: "retirement", Object: deploymentplan.Clone(helper.Object), Retire: true}
	deploymentplan.Map(retire.Object["metadata"])["uid"] = helperUID // Common Compile and delete preconditions pin only this accepted allocation.
	_, retireJournal, err := applyManagedLateLogExact(ctx, runtime, late, "retire", retire, late.LogRetireCandidate, helperUID, apply, check)
	if err != nil {
		return nil, err
	}
	// Read actual absence, including foreign or terminating claim holders, before
	// recreating any original business process. Uncertain deletion stops here.
	if body, err := runtime.Backend.Read(ctx, late.LogHelperCandidate.Resources[0].ID); err != nil || body != nil {
		return nil, logACKDraftBlock("helper Job retirement is not confirmed")
	}
	if prior, entryErr := managedLateLogEntry(e, "kubernetes/managed-late-log-restore-"+late.Review); entryErr != nil {
		return nil, entryErr
	} else if prior == nil {
		if err := waitManagedLateLogClaimEmpty(ctx, runtime, late); err != nil {
			return nil, err
		}
	}
	_, restoreJournal, err := applyManagedLateLogExact(ctx, runtime, late, "restore", renderLogACKDraftRestore(&producer), late.LogRestoreCandidate, "", apply, check)
	if err != nil {
		return nil, err
	}
	owner := producer.Original
	owner.Desired = renderLogACKDraftRestore(&producer).Object
	pod, err := readManagedLateLogSourcePod(ctx, runtime.Backend, owner)
	if err != nil {
		return nil, err
	}
	identity, err := readLogACKDraftIdentity(ctx, runtime.Backend, deploymentplan.String(deploymentplan.Map(pod["metadata"])["name"]), producer.Identity.RootSHA256)
	if err != nil || verifyLogACKDraftIdentity(identity, producer.Identity) != nil {
		return nil, logACKDraftBlock("restored original installed private identity")
	}
	state, finalSessionRaw, err := readManagedSessionState(ctx, cfg, runtime, late.NewSessionID)
	if err != nil || !slices.Equal(state.Subjects, producer.NewSession.Subjects) || !managedLateLogExactSet(state.Installed, state.Subjects) || state.Status != "active" || !state.Expires.After(time.Now().UTC()) {
		return nil, logACKDraftBlock("genuine new-session native nine ACKs")
	}
	finalSessionRaw, err = managedLateCompactLogSessionRaw(finalSessionRaw)
	if err != nil {
		return nil, err
	}
	clients, err := readManagedSessionClients(ctx, cfg, runtime, runtime.ServiceSessionRecovery.Ceremony, state.Subjects)
	if err != nil || clients != producer.OriginalClientsSHA || check(ctx) != nil || e.Check(ctx) != nil {
		return nil, logACKDraftBlock("original eleven identities before durable completion")
	}
	stopped, err := managedLateLogEntry(e, stopKey)
	if err != nil || stopped == nil {
		return nil, logACKDraftBlock("stop receipt unavailable")
	}
	retired, _ := managedLateLogEntry(e, "kubernetes/managed-late-log-retire-"+late.Review)
	restored, _ := managedLateLogEntry(e, "kubernetes/managed-late-log-restore-"+late.Review)
	started, startErr := time.Parse(time.RFC3339Nano, deploymentplan.String(observation.Native["native_started_at"]))
	finished, finishErr := time.Parse(time.RFC3339Nano, deploymentplan.String(observation.Native["native_finished_at"]))
	if startErr != nil || finishErr != nil || retired == nil || restored == nil {
		return nil, logACKDraftBlock("actual native terminal timestamps")
	}
	finalSession := createManagedLateLogFinalSession{Schema: "rtk-cloud.create-managed-late-log-final-session/v1", Parent: late.ParentSHA, Review: late.Review, Raw: append(json.RawMessage(nil), finalSessionRaw...)}
	invalidateManagedLateSourceReuse(e)
	if err := writeManagedLateLogImmutable(store, managedLateLogFile(store, late.Review, "managed-late-log-final-session.json"), finalSession); err != nil {
		return nil, err
	}
	proof := &createManagedLateLogProof{Schema: "rtk-cloud.create-managed-late-log-proof/v1", Parent: late.ParentSHA, Review: late.Review, HelperBindingSHA: late.HelperBindingSHA, ProducerSHA: deploymentplan.Digest(late.LogProducer), OriginalDeploymentUID: producer.Original.UID, HelperJobUID: helperUID, HelperPodUID: deploymentplan.String(deploymentplan.Map(observation.Pod["metadata"])["uid"]), NewSessionID: state.Session, IdentitySHA: deploymentplan.Digest(identity), NativeClientsSHA: clients, PrivateBytesSHA: identity.StateSHA256, NativeSessionSHA: environmentPreflightBytesSHA256(finalSessionRaw), StopJournalSHA: deploymentplan.Digest(stopJournal), HelperJournalSHA: deploymentplan.Digest(helperJournal), RetireJournalSHA: deploymentplan.Digest(retireJournal), RestoreJournalSHA: deploymentplan.Digest(restoreJournal), StoppedAt: stopped.CompletedAt, StartedAt: started, FinishedAt: finished, RetiredAt: retired.CompletedAt, RestoredAt: restored.CompletedAt, ObservationSHA: deploymentplan.Digest(observation), OriginalClaimUID: producer.OriginalClaimUID}
	if err := validateManagedLateLogCompletion(late, proof); err != nil {
		return nil, err
	}
	if err := writeManagedLateLogImmutable(store, managedLateLogFile(store, late.Review, "managed-late-log-proof.json"), proof); err != nil {
		return nil, err
	}
	key := "managed-late-log-verified-" + late.Review
	if err := e.BeginEntry(key, "recovery", mustProviderRecoveryJSON(proof), time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := e.RecordReceipt(key, mustProviderRecoveryJSON(proof)); err != nil {
		return nil, err
	}
	if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
		return nil, err
	}
	return readManagedLateLogCompletion(store, e, late, time.Now().UTC())
}

// Independent durable-only validation: after helper deletion and later fence
// archive, accepted child journals, the admitted producer and exact terminal
// observation remain authority. No exec, K8s reads, identity file read or writes.
func readManagedLateLogCompletion(store secretStore, e *environmentCreateExecution, late *createManagedLateSessionRecovery, completedAt time.Time) (*createManagedLateLogProof, error) {
	if late == nil || e == nil || late.ParentSHA != e.Journal.PlanSHA256 || validateManagedLateLogCandidates(late) != nil || !environmentPreflightDigest.MatchString(late.Review) {
		return nil, logACKDraftBlock("durable parent/descriptor")
	}
	base := filepath.Dir(managedLateLogFile(store, late.Review, "managed-late-log-proof.json"))
	var proof createManagedLateLogProof
	var observation createManagedLateLogObservation
	var finalSession createManagedLateLogFinalSession
	if validateEnvironmentPreflightDirectories(store, base) != nil || readExecutionJSON(filepath.Join(base, "managed-late-log-proof.json"), &proof) != nil || readExecutionJSON(filepath.Join(base, "managed-late-log-observation.json"), &observation) != nil || readExecutionJSON(filepath.Join(base, "managed-late-log-final-session.json"), &finalSession) != nil || validateManagedLateLogCompletion(late, &proof) != nil {
		return nil, logACKDraftBlock("private canonical durable Log proof")
	}
	admission, _, err := loadAdmittedCreateRecovery(store, e, late.Review)
	if err != nil || admission.Plan.Materials["accepted-service-core-late-session-log-producer"] != proof.ProducerSHA || admission.Plan.Materials["accepted-service-core-late-session-log-helper-binding"] != proof.HelperBindingSHA {
		return nil, logACKDraftBlock("immutable admitted producer")
	}
	finalState, finalErr := validateManagedLateLogFinalSession(late, &proof, observation, finalSession)
	if finalErr != nil {
		return nil, finalErr
	}
	for _, subject := range observation.Session.Installed {
		if !slices.Contains(finalState.Installed, subject) {
			return nil, logACKDraftBlock("native installation snapshot regressed")
		}
	}
	producer := *late.LogProducer
	producer.NewSession, producer.FreshReview = observation.Session, late.Review
	if observation.Schema != "rtk-cloud.create-managed-late-log-observation/v1" || observation.Parent != proof.Parent || observation.Review != proof.Review || observation.ProducerSHA != proof.ProducerSHA || observation.ClaimUID != proof.OriginalClaimUID || proof.ObservationSHA != deploymentplan.Digest(observation) || !environmentPreflightDigest.MatchString(observation.PublicLogSHA) || proof.IdentitySHA != deploymentplan.Digest(observation.Identity) || proof.PrivateBytesSHA != observation.Identity.StateSHA256 || proof.NativeClientsSHA != observation.ClientsSHA {
		return nil, logACKDraftBlock("exact immutable terminal observation")
	}
	if err := validateManagedLateLogObservation(late, observation, proof.HelperJobUID); err != nil {
		return nil, err
	}
	var decodedSession managedSessionState
	if decodeEnvironmentJSON(observation.SessionRaw, &decodedSession) != nil || deploymentplan.Digest(decodedSession) != deploymentplan.Digest(observation.Session) {
		return nil, logACKDraftBlock("native terminal raw session binding")
	}
	native, err := verifyLogACKDraftCompleted(&producer, proof.HelperJobUID, proof.HelperPodUID, observation.Job, []deploymentplan.Object{observation.Pod}, observation.Session, observation.Identity, observation.ClientsSHA, observation.Identity.StateSHA256, proof.FinishedAt.Add(time.Nanosecond))
	if err != nil {
		return nil, logACKDraftBlock("actual terminal native helper evidence")
	}
	native["native_session_raw_sha256"] = deploymentplan.String(observation.Native["native_session_raw_sha256"])
	if deploymentplan.Digest(native) != deploymentplan.Digest(observation.Native) {
		return nil, logACKDraftBlock("actual terminal native helper evidence")
	}
	nativeStarted, startErr := time.Parse(time.RFC3339Nano, deploymentplan.String(observation.Native["native_started_at"]))
	nativeFinished, finishErr := time.Parse(time.RFC3339Nano, deploymentplan.String(observation.Native["native_finished_at"]))
	if startErr != nil || finishErr != nil || !proof.StartedAt.Equal(nativeStarted) || !proof.FinishedAt.Equal(nativeFinished) {
		return nil, logACKDraftBlock("exact native process timestamps")
	}
	previous := time.Time{}
	for _, kind := range []string{"stop", "existing-ack", "retire", "restore"} {
		_, journal, err := managedLateLogAccepted(store, e, late, kind, proof.HelperJobUID)
		if err != nil {
			return nil, err
		}
		want := map[string]string{"stop": proof.StopJournalSHA, "existing-ack": proof.HelperJournalSHA, "retire": proof.RetireJournalSHA, "restore": proof.RestoreJournalSHA}[kind]
		entry, err := managedLateLogEntry(e, "kubernetes/managed-late-log-"+kind+"-"+late.Review)
		if err != nil || entry == nil || deploymentplan.Digest(journal) != want || !entry.CompletedAt.After(previous) || entry.CompletedAt.After(completedAt) {
			return nil, logACKDraftBlock("ordered exact accepted lifecycle journals")
		}
		if wantAt, ok := map[string]time.Time{"stop": proof.StoppedAt, "retire": proof.RetiredAt, "restore": proof.RestoredAt}[kind]; ok && !entry.CompletedAt.Equal(wantAt) {
			return nil, logACKDraftBlock("exact lifecycle receipt timestamps")
		}
		previous = entry.CompletedAt
	}
	entry, err := managedLateLogEntry(e, "managed-late-log-verified-"+late.Review)
	intentSHA, hashErr := environmentCreatePlanSHA256(mustProviderRecoveryJSON(proof))
	var receipt createManagedLateLogProof
	if err != nil || hashErr != nil || entry == nil || entry.Phase != "recovery" || entry.State != "completed" || entry.IntentSHA256 != intentSHA || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || deploymentplan.Digest(receipt) != deploymentplan.Digest(proof) || !entry.CompletedAt.After(proof.RestoredAt) || entry.CompletedAt.After(completedAt) {
		return nil, logACKDraftBlock("identical genuine native proof intent and receipt")
	}
	return &proof, nil
}

func managedLateLogExactSet(actual, expected []string) bool {
	if len(actual) != len(expected) || len(expected) != 9 {
		return false
	}
	a, b := slices.Clone(actual), slices.Clone(expected)
	slices.Sort(a)
	slices.Sort(b)
	for i := 1; i < len(a); i++ {
		if a[i] == a[i-1] || b[i] == b[i-1] {
			return false
		}
	}
	return slices.Equal(a, b)
}

func writeManagedLateLogImmutable(store secretStore, path string, value any) error {
	if validateEnvironmentPreflightDirectories(store, filepath.Dir(path)) != nil {
		return logACKDraftBlock("private qualified review directory")
	}
	raw, err := json.Marshal(value)
	if err != nil || len(raw) == 0 || len(raw) > environmentPreflightArtifactLimit {
		return logACKDraftBlock("bounded public lifecycle artifact")
	}
	// The bounded reader deliberately returns sanitized errors, including for
	// absence. Only Lstat may select creation; O_EXCL still refuses a race.
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return writeEnvironmentPreflightImmutableJSON(path, value)
	} else if err != nil {
		return logACKDraftBlock("immutable lifecycle artifact unavailable")
	}
	existing, err := readBoundedEnvironmentFile(path, true)
	if err == nil {
		if !bytes.Equal(existing, raw) {
			return logACKDraftBlock("immutable lifecycle artifact differs")
		}
		return nil
	}
	return logACKDraftBlock("immutable lifecycle artifact unavailable")
}
