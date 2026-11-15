package main

import (
	"context"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"time"
)

// A runtime owner observation is not Service mTLS or bootstrap ACK evidence.
type openBaoInPlaceOwner struct {
	Namespace, PodName, PodUID, ContainerID, StatefulSetUID, Backend string
	ReleaseImage                                                     environmentReleaseImage
	qualification                                                    openBaoInPlaceQualification
	original                                                         deploymentplan.Object
	desired                                                          deploymentplan.Object
}
type openBaoInPlaceQualification struct {
	Environment, ParentSHA, ParentOwnerSHA, SourceStage, SourcePlanSHA, SourceJournalSHA    string
	TargetImage, SelectionSHA, ManifestSHA, PodUID, ProviderContainerID, OldHostContainerID string
	TemplateStage, TemplatePlanSHA                                                          string
	ParentJournal                                                                           environmentCreateJournal
}
type openBaoInPlaceReceipt struct {
	QualificationSHA256, TemplateStage, TemplatePlanSHA, HostContainerID string
}

// Keep the reviewed qualification in its immutable private file. Embedding its
// journal RawMessages in a canonical parent receipt would change their bytes.
func readOpenBaoInPlaceQualification(store secretStore, e *environmentCreateExecution, review string) (openBaoInPlaceQualification, error) {
	var q openBaoInPlaceQualification
	if e == nil || e.Store != store || !environmentPreflightDigest.MatchString(review) {
		return q, createProviderRecoveryBlock("original OpenBao image qualification is unavailable")
	}
	path, err := store.safePath(openBaoInPlaceQualificationPath(e.Journal.PlanSHA256, review))
	if err != nil || readExecutionJSON(path, &q) != nil || deploymentplan.Digest(q) != review {
		return openBaoInPlaceQualification{}, createProviderRecoveryBlock("original OpenBao image qualification changed")
	}
	return q, nil
}

func openBaoInPlaceIntent(q openBaoInPlaceQualification) []byte {
	return mustProviderRecoveryJSON(deploymentplan.Object{"qualification_sha256": deploymentplan.Digest(q)})
}

func completedOpenBaoInPlaceReceipt(ctx context.Context, store secretStore, e *environmentCreateExecution) (openBaoInPlaceReceipt, bool, error) {
	bad := func() (openBaoInPlaceReceipt, bool, error) {
		return openBaoInPlaceReceipt{}, true, createProviderRecoveryBlock("accepted original OpenBao in-place image owner is unavailable")
	}
	if e == nil || e.Store != store || e.Check(ctx) != nil {
		return bad()
	}
	var receipt openBaoInPlaceReceipt
	for i := len(e.Journal.Entries) - 1; i >= 0; i-- {
		entry := e.Journal.Entries[i]
		if entry.Phase != "openbao-host-inplace" {
			continue
		}
		if entry.State != "completed" || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil {
			return bad()
		}
		q, err := readOpenBaoInPlaceQualification(store, e, receipt.QualificationSHA256)
		if err != nil {
			return bad()
		}
		sha, err := environmentCreatePlanSHA256(openBaoInPlaceIntent(q))
		if err != nil || entry.IntentSHA256 != sha || entry.Key != "openbao-host-inplace-"+deploymentplan.Digest(q) || entry.CompletedAt.IsZero() {
			return bad()
		}
		return receipt, true, nil
	}
	return openBaoInPlaceReceipt{}, false, nil
}

// verifyOpenBaoInPlaceReady verifies the accepted image/owner handoff only.
// The session caller must separately demand the original host's actual ACK.
func verifyOpenBaoInPlaceReady(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend) (openBaoInPlaceOwner, error) {
	receipt, found, err := completedOpenBaoInPlaceReceipt(ctx, store, e)
	if err != nil {
		return openBaoInPlaceOwner{}, err
	}
	if !found {
		return openBaoInPlaceOwner{}, createProviderRecoveryBlock("accepted original OpenBao in-place image owner is unavailable")
	}
	return verifyOpenBaoInPlaceReceipt(ctx, cfg, store, e, b, receipt)
}

type openBaoInPlaceHistory struct {
	qualification     openBaoInPlaceQualification
	original, desired deploymentplan.Object
	uid               string
	image             environmentReleaseImage
	child             *deploymentplan.Plan
	journal           deploymentplan.Journal
}

// Verify the immutable qualification and accepted native child independently
// of the Pod that originally ran the in-place update. A later Pod still needs
// its own authenticated replacement and unseal evidence.
func verifyOpenBaoInPlaceHistory(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, receipt openBaoInPlaceReceipt) (openBaoInPlaceHistory, error) {
	originalPlan, err := authenticatedCreateOperatorPlan(store, e)
	if err != nil {
		return openBaoInPlaceHistory{}, err
	}
	_, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return openBaoInPlaceHistory{}, err
	}
	_, image, err := verifyCreateEnvironmentOperator(ctx, cfg, store, e, operator, originalPlan)
	selection, manifest, ok := createOperatorVersionEvidence(e, operator)
	if err != nil {
		return openBaoInPlaceHistory{}, err
	}
	if !ok {
		return openBaoInPlaceHistory{}, createProviderRecoveryBlock("accepted original OpenBao publication is unavailable")
	}
	return verifyOpenBaoInPlaceHistoryWithImage(ctx, cfg, store, e, receipt, image, selection, manifest)
}

// Publication and historical receipts stay immutable. Runtime observation alone
// follows subsequent authenticated writes, through the supplied native target.
func currentOpenBaoInPlaceHistory(ctx context.Context, store secretStore, e *environmentCreateExecution, native environmentKubernetesBackend, receipt openBaoInPlaceReceipt, history openBaoInPlaceHistory) (openBaoInPlaceHistory, error) {
	actual, err := native.Read(ctx, deploymentplan.ID(history.desired))
	if err != nil {
		return history, err
	}
	if actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != history.uid {
		return history, createProviderRecoveryBlock("original OpenBao StatefulSet owner changed")
	}
	if deploymentplan.Matches(history.desired, actual) {
		return history, nil
	}
	pending, err := ordinaryRecordedPendingWrites(ctx, store, e, nil)
	if err != nil {
		return history, err
	}
	latest, err := completedRecordedOwnerSuccessorsAfterHistory(ctx, store, e, native, "kubernetes/"+receipt.TemplateStage, []activeTransportRenewalResource{sessionRecordedOwnerBase(deploymentplan.ID(history.desired), history.uid, history.desired)}, pending)
	if err != nil || len(latest) != 1 || !deploymentplan.Matches(latest[0].Desired, actual) {
		return history, createProviderRecoveryBlock("OpenBao template differs from its recorded same-owner workload history")
	}
	history.desired = latest[0].Desired
	return history, e.Check(ctx)
}

func verifyOpenBaoInPlaceHistoryWithImage(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, receipt openBaoInPlaceReceipt, image environmentReleaseImage, selection, manifest string) (openBaoInPlaceHistory, error) {
	bad := func() (openBaoInPlaceHistory, error) {
		return openBaoInPlaceHistory{}, createProviderRecoveryBlock("accepted original OpenBao in-place image owner is unavailable")
	}
	if e == nil || e.Store != store || cfg.Environment != store.Environment || e.Check(ctx) != nil {
		return bad()
	}
	q, err := readOpenBaoInPlaceQualification(store, e, receipt.QualificationSHA256)
	if err != nil {
		return bad()
	}
	if q.Environment != store.Environment || q.ParentSHA != e.Journal.PlanSHA256 || q.ParentOwnerSHA != deploymentplan.Digest(e.owner) || !partialSessionJournalExtends(q.ParentJournal, e.Journal) || receipt.TemplateStage != q.TemplateStage || receipt.TemplatePlanSHA != q.TemplatePlanSHA || q.PodUID == "" || q.ProviderContainerID == "" || q.OldHostContainerID == "" || receipt.HostContainerID == "" || receipt.HostContainerID == q.OldHostContainerID || !secretEnvironmentPattern.MatchString(q.SourceStage) || !secretEnvironmentPattern.MatchString(receipt.TemplateStage) {
		return bad()
	}
	if image.Reference != q.TargetImage || selection != q.SelectionSHA || manifest != q.ManifestSHA {
		return bad()
	}
	source, j, err := acceptedCreateStage(store, e, "kubernetes/"+q.SourceStage)
	if err != nil || source == nil || source.SHA256() != q.SourcePlanSHA || deploymentplan.Digest(j) != q.SourceJournalSHA {
		return bad()
	}
	id := "StatefulSet/" + e.Journal.Stack + "-secrets/openbao"
	var original deploymentplan.Object
	uid := j.Owners[id]
	for _, r := range source.Resources {
		if r.ID == id && r.Action != "retire" {
			original = deploymentplan.Clone(r.Desired)
		}
	}
	if original == nil || uid == "" {
		return bad()
	}
	deploymentplan.Map(original["metadata"])["uid"] = uid
	child, cj, err := acceptedCreateStage(store, e, "kubernetes/"+receipt.TemplateStage)
	if err != nil || child == nil || child.SHA256() != receipt.TemplatePlanSHA || len(child.Resources) != 1 || child.Resources[0].ID != id || cj.Owners[id] != uid || child.Resources[0].UID != uid {
		return bad()
	}
	expected := deploymentplan.Clone(original)
	podSpec := environmentPKIPod(expected)
	host := managedNamedField(podSpec["containers"], "openbao-pki")
	init := managedNamedField(podSpec["initContainers"], "openbao-pki-install")
	if host == nil || init == nil {
		return bad()
	}
	host["image"], init["image"] = image.Reference, image.Reference
	if !deploymentplan.Matches(expected, child.Resources[0].Desired) || !deploymentplan.Matches(child.Resources[0].Desired, expected) {
		return bad()
	}
	return openBaoInPlaceHistory{qualification: q, original: original, desired: child.Resources[0].Desired, uid: uid, image: image, child: child, journal: cj}, nil
}

// The original Pod can be replaced after the image handoff. Its successor is
// checked separately against the accepted template and its actual image ID;
// the caller must also verify the original managed replacement and unseal.
func openBaoInPlaceReplacementMatches(history openBaoInPlaceHistory, set, pod deploymentplan.Object) bool {
	if set == nil || pod == nil || history.uid == "" || deploymentplan.Map(set["metadata"])["uid"] != history.uid || !deploymentplan.Matches(history.desired, set) || validateOpenBaoPodOwner(set, pod) != nil || !staleIdentityPodReady(pod, true) || deploymentplan.Map(pod["status"])["phase"] != "Running" {
		return false
	}
	if !managedOpenBaoPodMatches(history.desired, pod) {
		return false
	}
	status := deploymentplan.Map(pod["status"])
	provider := managedNamedField(status["containerStatuses"], "openbao")
	host := managedNamedField(status["containerStatuses"], "openbao-pki")
	init := managedNamedField(status["initContainerStatuses"], "openbao-pki-install")
	digest := strings.Split(history.image.Reference, "@")
	return len(digest) == 2 && provider != nil && provider["ready"] == true && deploymentplan.String(provider["containerID"]) != "" && host != nil && host["ready"] == true && deploymentplan.String(host["containerID"]) != "" && strings.HasSuffix(deploymentplan.String(host["imageID"]), "@"+digest[1]) && init != nil && deploymentplan.Map(deploymentplan.Map(init["state"])["terminated"])["exitCode"] == float64(0)
}

// Verify the exact candidate before recording completion and on every readback.
func verifyOpenBaoInPlaceReceipt(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, receipt openBaoInPlaceReceipt) (openBaoInPlaceOwner, error) {
	bad := func() (openBaoInPlaceOwner, error) {
		return openBaoInPlaceOwner{}, createProviderRecoveryBlock("accepted original OpenBao in-place image owner is unavailable")
	}
	history, err := verifyOpenBaoInPlaceHistory(ctx, cfg, store, e, receipt)
	if err != nil {
		return bad()
	}
	history, err = currentOpenBaoInPlaceHistory(ctx, store, e, b, receipt, history)
	if err != nil {
		return bad()
	}
	q, original, image := history.qualification, history.original, history.image
	id := "StatefulSet/" + e.Journal.Stack + "-secrets/openbao"
	set, err := b.Read(ctx, id)
	if err != nil || set == nil || !deploymentplan.Matches(history.desired, set) {
		return bad()
	}
	ns := e.Journal.Stack + "-secrets"
	pod, err := b.Read(ctx, "Pod/"+ns+"/openbao-0")
	if err != nil {
		return bad()
	}
	owner, err := validateOpenBaoInPlaceObservation(q, original, set, pod, receipt.HostContainerID, history.desired)
	if err != nil || e.Check(ctx) != nil {
		return bad()
	}
	owner.ReleaseImage = image
	owner.qualification = q
	owner.original = deploymentplan.Clone(original)
	owner.desired = deploymentplan.Clone(history.desired)
	return owner, nil
}

// This checks runtime images/ownership only; it cannot prove identity lock or mTLS.
func validateOpenBaoInPlaceObservation(q openBaoInPlaceQualification, original, set, pod deploymentplan.Object, hostID string, recordedTemplates ...deploymentplan.Object) (openBaoInPlaceOwner, error) {
	bad := func() (openBaoInPlaceOwner, error) {
		return openBaoInPlaceOwner{}, createProviderRecoveryBlock("OpenBao host image or original provider observation differs")
	}
	if pod == nil || set == nil || deploymentplan.Map(pod["metadata"])["uid"] != q.PodUID || !staleIdentityPodReady(pod, true) || hostID == "" || hostID == q.OldHostContainerID {
		return bad()
	}
	if len(recordedTemplates) > 1 || !openBaoInPlaceUsesPVC(pod) {
		return bad()
	}
	if len(recordedTemplates) == 0 {
		tp, pp, err := compileOpenBaoInPlaceImagePatches(original, set, pod, q.TargetImage)
		if err != nil || len(tp) != 0 || len(pp) != 0 {
			return bad()
		}
	} else {
		// A retained OnDelete Pod still belongs to its original accepted
		// template. Check that Pod separately from a later recorded template.
		if deploymentplan.Map(set["metadata"])["uid"] != deploymentplan.Map(original["metadata"])["uid"] || !deploymentplan.Matches(recordedTemplates[0], set) || validateOpenBaoPodOwner(set, pod) != nil {
			return bad()
		}
		oldHost := managedNamedField(environmentPKIPod(original)["containers"], "openbao-pki")
		originalPod := deploymentplan.Clone(pod)
		host := managedNamedField(deploymentplan.Map(originalPod["spec"])["containers"], "openbao-pki")
		if oldHost == nil || host == nil || host["image"] != q.TargetImage {
			return bad()
		}
		host["image"] = oldHost["image"]
		if !managedOpenBaoPodMatches(original, originalPod) {
			return bad()
		}
	}
	stat := deploymentplan.Map(pod["status"])
	main := managedNamedField(stat["containerStatuses"], "openbao")
	host := managedNamedField(stat["containerStatuses"], "openbao-pki")
	init := managedNamedField(stat["initContainerStatuses"], "openbao-pki-install")
	digest := strings.Split(q.TargetImage, "@")
	if len(digest) != 2 || main == nil || host == nil || init == nil || main["ready"] != true || host["ready"] != true || main["containerID"] != q.ProviderContainerID || host["containerID"] != hostID || !strings.HasSuffix(deploymentplan.String(host["imageID"]), "@"+digest[1]) || deploymentplan.Map(deploymentplan.Map(init["state"])["terminated"])["exitCode"] != float64(0) {
		return bad()
	}
	return openBaoInPlaceOwner{Namespace: deploymentplan.String(deploymentplan.Map(pod["metadata"])["namespace"]), PodName: "openbao-0", PodUID: q.PodUID, ContainerID: hostID, StatefulSetUID: deploymentplan.String(deploymentplan.Map(set["metadata"])["uid"]), Backend: "pvc"}, nil
}
func openBaoInPlaceUsesPVC(pod deploymentplan.Object) bool {
	if pod == nil {
		return false
	}
	host := managedNamedField(deploymentplan.Map(pod["spec"])["containers"], "openbao-pki")
	if host == nil {
		return false
	}
	custody := managedNamedField(host["env"], "PKI_STATE_BACKEND")
	return custody == nil || custody["value"] == "pvc" && custody["valueFrom"] == nil
}

// No parent receipt is written until all native evidence and the execution
// fence have passed; an interrupted allocated receipt remains resumable.
func completeOpenBaoInPlaceReceipt(ctx context.Context, e *environmentCreateExecution, key string, receipt openBaoInPlaceReceipt, verify, fence func(context.Context) error, now time.Time) error {
	if e == nil || verify == nil || fence == nil || now.IsZero() {
		return createProviderRecoveryBlock("OpenBao in-place receipt adapters unavailable")
	}
	if err := verify(ctx); err != nil {
		return err
	}
	if err := fence(ctx); err != nil {
		return err
	}
	if err := e.RecordReceipt(key, mustProviderRecoveryJSON(receipt)); err != nil {
		return err
	}
	if err := fence(ctx); err != nil {
		return err
	}
	return e.CompleteEntry(key, now)
}

// An unfinished image handoff cannot be silently treated as the old release.
func optionalOpenBaoInPlaceReady(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend) (openBaoInPlaceOwner, bool, error) {
	if e == nil {
		return openBaoInPlaceOwner{}, false, nil
	}
	for i := len(e.Journal.Entries) - 1; i >= 0; i-- {
		entry := e.Journal.Entries[i]
		if entry.Phase != "openbao-host-inplace" {
			continue
		}
		if entry.State != "completed" {
			return openBaoInPlaceOwner{}, true, createProviderRecoveryBlock("original OpenBao host image handoff is incomplete")
		}
		owner, err := verifyOpenBaoInPlaceReady(ctx, cfg, store, e, b)
		return owner, true, err
	}
	return openBaoInPlaceOwner{}, false, nil
}

// A completed image handoff retains its verified publication authority across
// cold processes. Provenance binds the original controller UID and exact package
// references. Later readiness/template updates do not change that publication;
// current template history, Pod readiness, unseal and ACK are runtime gates.
func completedOpenBaoPublicationImage(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, selected createVersionImage, selectionSHA string) (environmentReleaseImage, bool, error) {
	var empty environmentReleaseImage
	bad := func() (environmentReleaseImage, bool, error) {
		return empty, true, createProviderRecoveryBlock("completed OpenBao publication or original controller ownership changed")
	}
	completed := false
	for i := len(e.Journal.Entries) - 1; i >= 0; i-- {
		if e.Journal.Entries[i].Phase == "openbao-host-inplace" {
			completed = e.Journal.Entries[i].State == "completed"
			break
		}
	}
	if !completed {
		return empty, false, nil
	}
	receipt, found, err := completedOpenBaoInPlaceReceipt(ctx, store, e)
	if err != nil || !found {
		return bad()
	}
	q, err := readOpenBaoInPlaceQualification(store, e, receipt.QualificationSHA256)
	if err != nil {
		return bad()
	}
	if selected.Publication == nil || q.TargetImage != selected.Reference || q.SelectionSHA != selectionSHA || q.ManifestSHA != selected.Publication.ManifestSHA {
		return empty, false, nil
	}
	image := environmentReleaseImage{Reference: selected.Reference, SourceRevision: selected.SourceRevision, Publication: selected.Publication}
	history, err := verifyOpenBaoInPlaceHistoryWithImage(ctx, cfg, store, e, receipt, image, selectionSHA, selected.Publication.ManifestSHA)
	if err != nil {
		return bad()
	}
	var admission environmentCreateProviderRecoveryArtifact
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create/recovery", e.Journal.PlanSHA256, "qualification.json"), &admission) != nil || admission.Schema != "rtk-cloud.provider-create-recovery-artifact/v1" || deploymentplan.Digest(admission) != e.Journal.ArtifactSHA256 || deploymentplan.Digest(admission.Plan) != e.Journal.PlanSHA256 {
		return bad()
	}
	kube, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || environmentPreflightBytesSHA256(kube) != admission.Plan.KubeconfigSHA256 {
		return bad()
	}
	native, err := newEnvironmentKubernetesBackend(string(kube), cfg.Environment)
	if err != nil {
		return bad()
	}
	set, err := native.Read(ctx, "StatefulSet/"+e.Journal.Stack+"-secrets/openbao")
	if err != nil || set == nil || deploymentplan.Map(set["metadata"])["uid"] != history.uid || e.Check(ctx) != nil {
		return bad()
	}
	host := managedNamedField(environmentPKIPod(set)["containers"], "openbao-pki")
	init := managedNamedField(environmentPKIPod(set)["initContainers"], "openbao-pki-install")
	if host == nil || init == nil || host["image"] != selected.Reference || init["image"] != selected.Reference {
		return bad()
	}
	return image, true, nil
}
