package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"slices"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Restore only the previously recorded controller Pod template after an
// interrupted transport stage incorrectly projected legacy PVC custody.
func runCreateCustodyControllerRecovery(args []string) error {
	fs := flag.NewFlagSet("recover-create-custody-controller", flag.ContinueOnError)
	env := fs.String("environment", "", "selected environment")
	parent := fs.String("parent-plan-sha256", "", "original create generation")
	confirm := fs.String("confirm", "", "selected stack")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || !secretEnvironmentPattern.MatchString(*env) || !environmentPreflightDigest.MatchString(*parent) || *confirm != "video-cloud-"+*env {
		return createProviderRecoveryBlock("select the exact environment, original create generation and stack")
	}
	workspace, err := workspaceRoot()
	if err != nil {
		return err
	}
	cfg, err := resolveDeploymentConfig(workspace, *env, "")
	if err != nil {
		return err
	}
	store, operator, err := readEnvironmentCreateOperator(cfg)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	e, original, err := loadActiveTransportExecution(cfg, store, *parent)
	if err != nil {
		return err
	}
	defer e.Close()
	if deploymentplan.Digest(operator) != original.Plan.Plan.OperatorSHA256 {
		return createProviderRecoveryBlock("original operator binding changed")
	}
	if _, ok, err := initialSuccessorContinuationEntry(store, e); err != nil || !ok {
		return createProviderRecoveryBlock("original temporary transport continuation is unavailable")
	}
	plan, err := readEnvironmentCreateStageInput(store, e, "transport-enrollment-issuer")
	if err != nil || plan == nil {
		return createProviderRecoveryBlock("original interrupted issuer stage is unavailable")
	}
	journal, exists, err := readEnvironmentCreateStageProgress(store, e, "transport-enrollment-issuer", plan)
	if err != nil || !exists || journal.Accepted || journal.InFlight != "" {
		return createProviderRecoveryBlock("original issuer stage is not safely stopped")
	}
	id := "Deployment/" + *confirm + "-video-cloud/pki-controller"
	var selected *deploymentplan.Resource
	for i := range plan.Resources {
		if plan.Resources[i].ID == id {
			selected = &plan.Resources[i]
		}
	}
	if selected == nil || selected.Action != "update" || selected.UID == "" || journal.Owners[id] != selected.UID || !slices.Contains(journal.Completed, id) || selected.Baseline == nil {
		return createProviderRecoveryBlock("original controller write and owner are not recorded")
	}
	oldPod, badPod := environmentPKIPod(selected.Baseline), environmentPKIPod(selected.Desired)
	if oldPod == nil || badPod == nil || !createPodHasVolume(oldPod, "pki-custody-auth") || createPodHasPVC(oldPod, "pki-controller-state") || !createPodHasPVC(badPod, "pki-controller-state") {
		return createProviderRecoveryBlock("original controller template does not prove the OpenBao custody regression")
	}
	kube, err := readBoundedEnvironmentFile(store.KubeconfigPath(), true)
	if err != nil || environmentPreflightBytesSHA256(kube) != original.Plan.KubeconfigSHA256 {
		return createProviderRecoveryBlock("original Kubernetes identity changed")
	}
	b, err := newEnvironmentKubernetesBackend(string(kube), *env)
	if err != nil {
		return err
	}
	b.RolloutTimeout, err = environmentRolloutTimeout(cfg.Values)
	if err != nil {
		return err
	}
	if len(e.Journal.KubernetesLeases) == 0 {
		return createProviderRecoveryBlock("original Kubernetes execution lease is unavailable")
	}
	lease := e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1]
	liveLease, err := b.Read(ctx, lease.ResourceID)
	if err != nil || liveLease != nil && (deploymentplan.Map(liveLease["metadata"])["uid"] != lease.UID || deploymentplan.Map(liveLease["spec"])["holderIdentity"] != lease.Holder) {
		return createProviderRecoveryBlock("original Kubernetes execution lease changed")
	}
	live, err := b.Read(ctx, id)
	if err != nil || deploymentplan.Map(live["metadata"])["uid"] != selected.UID {
		return createProviderRecoveryBlock("original controller owner changed")
	}
	intent := custodyControllerRestoreIntent(plan, *selected)
	key := "custody-controller-restore-" + deploymentplan.Digest(intent)
	state := ""
	for _, entry := range e.Journal.Entries {
		if entry.Key == key {
			if entry.IntentSHA256 != deploymentplan.Digest(intent) {
				return createProviderRecoveryBlock("controller recovery intent changed")
			}
			state = entry.State
		}
	}
	if state == "" {
		if err := e.BeginEntry(key, "recovery", mustProviderRecoveryJSON(intent), time.Now().UTC()); err != nil {
			return err
		}
		state = "pending"
	}
	if !deploymentplan.Matches(selected.Baseline, live) {
		if state != "pending" || !deploymentplan.Matches(selected.Desired, live) {
			return createProviderRecoveryBlock("controller differs from both the interrupted write and original template")
		}
		body := deploymentplan.Clone(live)
		deploymentplan.Map(body["spec"])["template"] = deploymentplan.Clone(deploymentplan.Map(deploymentplan.Map(selected.Baseline["spec"])["template"]))
		if _, err := b.Write(ctx, "update", body, true); err != nil {
			return err
		}
		if err := e.Check(ctx); err != nil {
			return err
		}
		if _, err := b.Write(ctx, "update", body, false); err != nil {
			return err
		}
	}
	if state == "pending" {
		if err := e.RecordReceipt(key, mustProviderRecoveryJSON(intent)); err != nil {
			return err
		}
		state = "allocated"
	}
	if err := b.Wait(ctx, deploymentplan.Resource{ID: id, UID: selected.UID, Action: "update", Desired: selected.Baseline}); err != nil {
		return err
	}
	if state == "allocated" {
		if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
			return err
		}
	}
	// The interrupted child retains its own Lease. Release only the exact
	// recorded holder after the original controller has recovered; a following
	// qualified child must acquire a new Lease of its own.
	releaseIntent := deploymentplan.Object{"resource": lease.ResourceID, "uid": lease.UID, "holder": lease.Holder, "interrupted_plan_sha256": plan.SHA256()}
	releaseKey := "custody-controller-lease-release-" + deploymentplan.Digest(releaseIntent)
	releaseState := ""
	for _, entry := range e.Journal.Entries {
		if entry.Key == releaseKey {
			if entry.IntentSHA256 != deploymentplan.Digest(releaseIntent) {
				return createProviderRecoveryBlock("original lease release intent changed")
			}
			releaseState = entry.State
		}
	}
	if releaseState == "" {
		if liveLease == nil {
			return createProviderRecoveryBlock("original lease is missing without a recorded release intent")
		}
		if err := e.BeginEntry(releaseKey, "recovery", mustProviderRecoveryJSON(releaseIntent), time.Now().UTC()); err != nil {
			return err
		}
		releaseState = "pending"
	}
	if liveLease != nil {
		if releaseState != "pending" {
			return createProviderRecoveryBlock("previously released lease unexpectedly exists")
		}
		ownedLease := &environmentExecutionLease{backend: b, id: lease.ResourceID, uid: lease.UID, holder: lease.Holder}
		if err := ownedLease.Release(ctx); err != nil {
			return err
		}
	}
	if releaseState == "pending" {
		if err := e.RecordReceipt(releaseKey, mustProviderRecoveryJSON(releaseIntent)); err != nil {
			return err
		}
		releaseState = "allocated"
	}
	if releaseState == "allocated" {
		if err := e.CompleteEntry(releaseKey, time.Now().UTC()); err != nil {
			return err
		}
	}
	fmt.Fprintln(os.Stdout, "PASS: original OpenBao-custody controller template restored and ready")
	return nil
}

func custodyControllerForwardStage(original *deploymentplan.Plan) string {
	return "transport-enrollment-issuer-custody-forward-" + original.SHA256()[:12]
}

func custodyControllerRestoreIntent(plan *deploymentplan.Plan, resource deploymentplan.Resource) deploymentplan.Object {
	return deploymentplan.Object{"stage_plan_sha256": plan.SHA256(), "resource": resource.ID, "uid": resource.UID, "original_template_sha256": deploymentplan.Digest(deploymentplan.Map(resource.Baseline["spec"])["template"]), "interrupted_template_sha256": deploymentplan.Digest(deploymentplan.Map(resource.Desired["spec"])["template"])}
}

// Authenticate the interrupted child and its completed repair before a new
// projected child may supersede it. The original plan is never overwritten.
func interruptedCustodyControllerStage(store secretStore, e *environmentCreateExecution) (*deploymentplan.Plan, error) {
	plan, err := readEnvironmentCreateStageInput(store, e, "transport-enrollment-issuer")
	if err != nil || plan == nil {
		return plan, err
	}
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/transport-enrollment-issuer")
	if entry == nil {
		return nil, createProviderRecoveryBlock("interrupted issuer stage parent entry is unavailable")
	}
	var journal deploymentplan.Journal
	var exists bool
	if entry.State == "pending" {
		journal, exists, err = readEnvironmentCreateStageProgress(store, e, "transport-enrollment-issuer", plan)
	} else {
		var receipt struct {
			Forward  string `json:"actual_qualified_forward_stage"`
			Original string `json:"original_failed_plan_sha256"`
		}
		if (entry.State != "allocated" && entry.State != "completed") || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt.Original != plan.SHA256() || receipt.Forward != "kubernetes/"+custodyControllerForwardStage(plan) {
			return nil, createProviderRecoveryBlock("issuer forward completion differs from its interrupted source")
		}
		if _, _, err := acceptedCreateStage(store, e, receipt.Forward); err != nil {
			return nil, err
		}
		journal, exists, err = plan.ReadExecution(store.Root)
	}
	if err != nil || !exists || journal.Accepted || journal.InFlight != "" {
		return nil, createProviderRecoveryBlock("interrupted issuer stage does not have its original stopped child journal")
	}
	id := "Deployment/" + e.Journal.Stack + "-video-cloud/pki-controller"
	for _, resource := range plan.Resources {
		if resource.ID != id {
			continue
		}
		if resource.Action != "update" || resource.UID == "" || journal.Owners[id] != resource.UID || !slices.Contains(journal.Completed, id) {
			return nil, createProviderRecoveryBlock("interrupted issuer controller owner is unavailable")
		}
		intent := custodyControllerRestoreIntent(plan, resource)
		key := "custody-controller-restore-" + deploymentplan.Digest(intent)
		for _, entry := range e.Journal.Entries {
			if entry.Key == key && entry.State == "completed" && entry.IntentSHA256 == deploymentplan.Digest(intent) && deploymentplan.Digest(jsonRawObject(entry.Receipt)) == deploymentplan.Digest(intent) {
				return plan, nil
			}
		}
		return nil, createProviderRecoveryBlock("original controller restoration has not completed")
	}
	return nil, createProviderRecoveryBlock("interrupted issuer controller is unavailable")
}

func createPodHasVolume(pod deploymentplan.Object, name string) bool {
	for _, raw := range certIssuerObjectList(pod["volumes"]) {
		if deploymentplan.Map(raw)["name"] == name {
			return true
		}
	}
	return false
}

func createPodHasPVC(pod deploymentplan.Object, claim string) bool {
	for _, raw := range certIssuerObjectList(pod["volumes"]) {
		if deploymentplan.Map(deploymentplan.Map(raw)["persistentVolumeClaim"])["claimName"] == claim {
			return true
		}
	}
	return false
}

// Replay only the exact completed native template write. Its real receipt is
// retained; no accepted child journal is manufactured for this older producer.
func completedRecordedCustodyControllerRestore(store secretStore, e *environmentCreateExecution, entry environmentCreateJournalEntry) (*activeTransportRenewalResource, error) {
	bad := func() (*activeTransportRenewalResource, error) {
		return nil, createProviderRecoveryBlock("recorded controller template restoration lacks its original native receipt")
	}
	plan, err := interruptedCustodyControllerStage(store, e)
	if err != nil || plan == nil {
		return bad()
	}
	id := "Deployment/" + e.Journal.Stack + "-video-cloud/pki-controller"
	for _, resource := range plan.Resources {
		if resource.ID != id {
			continue
		}
		intent := custodyControllerRestoreIntent(plan, resource)
		digest := deploymentplan.Digest(intent)
		if entry.Key != "custody-controller-restore-"+digest || entry.Phase != "recovery" || entry.State != "completed" || entry.CompletedAt.IsZero() || entry.IntentSHA256 != digest || deploymentplan.Digest(jsonRawObject(entry.Receipt)) != digest {
			return bad()
		}
		desired := deploymentplan.Clone(resource.Desired)
		spec, original := deploymentplan.Map(desired["spec"]), deploymentplan.Map(resource.Baseline["spec"])
		if spec == nil || deploymentplan.Map(original["template"]) == nil {
			return bad()
		}
		spec["template"] = deploymentplan.Clone(deploymentplan.Map(original["template"]))
		return &activeTransportRenewalResource{ID: id, UID: resource.UID, Before: deploymentplan.Clone(resource.Desired), Desired: desired}, nil
	}
	return bad()
}
