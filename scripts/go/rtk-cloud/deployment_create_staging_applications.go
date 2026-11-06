package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const stagingApplicationsStage = "managed-application-owners"

type stagingApplicationsForward struct {
	Original    string
	Definitions []deploymentplan.Definition
}

func qualifyStagingApplicationsForward(ctx context.Context, cfg deploymentConfig, store secretStore, parent *environmentCreatePlan, e *environmentCreateExecution, runtime environmentCreateBootstrapRuntime) (*stagingApplicationsForward, error) {
	bad := createProviderRecoveryBlock("original staging applications do not match the fully written media and broker startup checkpoint")
	original, err := readEnvironmentCreateStageInput(store, e, stagingApplicationsStage)
	if err != nil || original == nil || len(original.Resources) != 26 || original.ConfigurationSHA256 != parent.ConfigurationSHA256 || original.ReleaseSHA256 != deploymentplan.Digest(parent.Release) {
		return nil, bad
	}
	ns := parent.Stack + "-video-cloud"
	journal, exists, err := readEnvironmentCreateStageProgress(store, e, stagingApplicationsStage, original)
	if err != nil || !exists || journal.Accepted || journal.AcceptanceStarted || journal.InFlight != "" || journal.Failed != "Deployment/"+ns+"/video-cloud-api" || len(journal.Completed) != len(original.Resources) || len(journal.Owners) != len(original.Resources) {
		return nil, bad
	}
	media, err := readMediaStorageObservation(cfg, store)
	if err != nil || validateMediaStorageObservation(cfg, media) != nil || lkeVideoStorageCoreCutoverEnabled(environmentConfigurationRenderValues(cfg)) {
		return nil, bad
	}
	settings := map[string]string{}
	projectCoreMediaAllocation(settings, media)
	secrets, err := environmentRuntimeSecretSnapshot(store)
	if err != nil {
		return nil, err
	}
	brokerKey, brokerSecret := secrets[lkeSecretFileName("emqx-handoff-api-key")], secrets[lkeSecretFileName("emqx-handoff-api-secret")]
	brokerRuntime, err := runtime.Backend.Read(ctx, "Secret/"+ns+"/mqtt-pki-runtime")
	if err != nil || brokerKey == "" || brokerSecret == "" || deploymentplan.Map(brokerRuntime["data"])["api-keys.conf"] != base64.StdEncoding.EncodeToString([]byte(brokerKey+":"+brokerSecret+":administrator\n")) {
		return nil, bad
	}
	changed := 0
	f := &stagingApplicationsForward{Original: original.SHA256()}
	for _, resource := range original.Resources {
		actual, err := runtime.Backend.Read(ctx, resource.ID)
		uid := journal.Owners[resource.ID]
		if err != nil || !slices.Contains(journal.Completed, resource.ID) || !environmentBootstrapOwnerUUID.MatchString(uid) || deploymentplan.Map(actual["metadata"])["uid"] != uid || !deploymentplan.Matches(resource.Desired, actual) {
			return nil, bad
		}
		object := deploymentplan.Clone(resource.Desired)
		metadata := deploymentplan.Map(object["metadata"])
		metadata["uid"] = uid
		definition := deploymentplan.Definition{Object: object, Phase: resource.Phase, Dependencies: slices.Clone(resource.Dependencies), Retain: true}
		if bindStagingSingleAPIOwner(object, parent.Stack) {
			if !bindStagingMissingCoreMedia(object, settings) {
				return nil, bad
			}
			definition.Retain = false
			changed++
		}
		switch resource.ID {
		case "Secret/" + ns + "/video-cloud-workers-runtime":
			if !bindStagingMissingBrokerCredentials(object, brokerKey, brokerSecret) {
				return nil, bad
			}
			definition.Retain = false
			changed++
		case "Deployment/" + ns + "/mqtt-pki":
			template := deploymentplan.Map(deploymentplan.Map(object["spec"])["template"])
			meta := deploymentplan.Map(template["metadata"])
			annotations := deploymentplan.Map(meta["annotations"])
			if annotations == nil {
				annotations = deploymentplan.Object{}
				meta["annotations"] = annotations
			}
			annotations["rtk.realtek.com/staging-broker-credential-recovery"] = f.Original[:12]
			definition.Retain = false
			changed++
		}
		f.Definitions = append(f.Definitions, definition)
	}
	if changed != 5 {
		return nil, bad
	}
	return f, e.Check(ctx)
}

// Read the native forward receipt without expecting the old failed bodies to
// remain live after the normal workload stage has legitimately updated them.
func verifyStagingAcceptedApplications(store secretStore, e *environmentCreateExecution) error {
	bad := createProviderRecoveryBlock("original staging application startup recovery lacks its accepted child and retained owners")
	original, err := readEnvironmentCreateStageInput(store, e, stagingApplicationsStage)
	entry := createOpenBaoACKCompletionEntry(e.Journal, "kubernetes/"+stagingApplicationsStage)
	if err != nil || original == nil || entry == nil || entry.State != "completed" || len(original.Resources) != 26 {
		return bad
	}
	var receipt map[string]string
	stage := stagingApplicationsStage + "-startup-" + original.SHA256()[:12]
	if decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt["actual_qualified_forward_stage"] != "kubernetes/"+stage || receipt["original_failed_plan_sha256"] != original.SHA256() {
		return bad
	}
	before, exists, err := original.ReadExecution(store.Root)
	if err != nil || !exists || before.Accepted || before.Failed != "Deployment/"+e.Journal.Stack+"-video-cloud/video-cloud-api" || len(before.Owners) != len(original.Resources) {
		return bad
	}
	forward, after, err := acceptedCreateStage(store, e, "kubernetes/"+stage)
	if err != nil || len(forward.Resources) != len(original.Resources) || deploymentplan.Digest(before.Owners) != deploymentplan.Digest(after.Owners) {
		return bad
	}
	return nil
}

func restoreStagingBoundBootstrap(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, store secretStore, runtime environmentCreateBootstrapRuntime) (environmentIdentityBindings, bool, error) {
	var saved environmentIdentityBindings
	e := runtime.Execution
	relative := filepath.Join("deployment", "create", e.Journal.PlanSHA256, "bootstrap", "bindings.json")
	path, err := store.safePath(relative)
	if err != nil {
		return saved, false, err
	}
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return saved, false, nil
	} else if err != nil {
		return saved, false, err
	}
	bad := createProviderRecoveryBlock("original staging bootstrap bindings or accepted permanent outputs differ")
	if readExecutionJSON(path, &saved) != nil || validateEnvironmentIdentityBindings(saved, p.Environment) != nil || saved.ProductIdentities == nil {
		return saved, false, bad
	}
	ingress := createOpenBaoACKCompletionEntry(e.Journal, environmentCreateIngressAdmissionEntry)
	if ingress == nil || ingress.State != "completed" || ingress.CompletedAt.IsZero() {
		return saved, false, bad
	}
	live, err := runtime.Backend.Inventory(ctx, p.Stack)
	if err != nil {
		return saved, false, err
	}
	_, workload, err := readStagingWorkloadAllocationCheckpoint(store, e)
	if err != nil {
		return saved, false, err
	}
	for _, stage := range []string{"kubernetes/account-manager-registration", "kubernetes/ingress-controller"} {
		if stage == "kubernetes/account-manager-registration" && cfg.Values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] != "true" {
			continue
		}
		child, journal, err := acceptedCreateStage(store, e, stage)
		if err != nil {
			return saved, false, err
		}
		retained := *child
		retained.Resources = nil
		for _, resource := range child.Resources {
			// The exact forward workload child owns any recorded updates.
			// Its normal resumption rechecks each native body before writing.
			if workload.Owners[resource.ID] != journal.Owners[resource.ID] {
				retained.Resources = append(retained.Resources, resource)
			}
		}
		if err := verifyOriginalMigrationCheckpointInventory(ctx, store, &retained, journal, live); err != nil {
			return saved, false, err
		}
	}
	current, err := observeEnvironmentTargetBindings(ctx, cfg, release, "create", live)
	if err != nil || current.ProductIdentities == nil || *current.ProductIdentities != *saved.ProductIdentities {
		return saved, false, bad
	}
	for id, uid := range saved.Owners {
		if current.Owners[id] != uid {
			return saved, false, bad
		}
	}
	program, err := readFreshProductProgram(store, e)
	if err != nil {
		return saved, false, err
	}
	installed, err := readProductInstalled(store, &program.Lifecycle)
	if err != nil {
		return saved, false, err
	}
	if err := verifyProductIdentityOutputs(ctx, cfg, runtime, &program.Lifecycle, installed); err != nil {
		return saved, false, err
	}
	return current, true, e.Check(ctx)
}

func readStagingCompletedAccountServer(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, namespace, role, claim, stateRoot, clientState, serverState, serverName string) (environmentCreateOwnedServerMaterial, error) {
	var empty environmentCreateOwnedServerMaterial
	bad := createProviderRecoveryBlock("staging Account Manager export or its exact completed reader retirement differs")
	e := runtime.Execution
	entry := createOpenBaoACKCompletionEntry(e.Journal, "server-export-account-manager")
	var receipt completedAccountServerExport
	if entry == nil || entry.State != "completed" || entry.Phase != "bootstrap" || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt.Server != serverName || !environmentPreflightDigest.MatchString(receipt.Certificate) {
		return empty, bad
	}
	intent := deploymentplan.Object{"owner": "account-manager", "namespace": namespace, "claim_uid": receipt.Claim, "reader_uid": receipt.Reader, "pod_uid": receipt.Pod, "replicaset_uid": receipt.ReplicaSet, "server_name": serverName, "root_sha256": service.Root.RootFingerprint}
	if entry.IntentSHA256 != activeTransportJournalDigest(intent) {
		return empty, bad
	}
	name := "pki-export-account-manager-" + e.Journal.PlanSHA256[:12]
	id := "Deployment/" + namespace + "/" + name
	reader, readJournal, err := acceptedCreateStage(e.Store, e, "kubernetes/server-custody-account-manager")
	retired, retireJournal, retireErr := acceptedCreateStage(e.Store, e, "kubernetes/retire-"+name)
	liveReader, liveErr := runtime.Backend.Read(ctx, id)
	if err != nil || retireErr != nil || liveErr != nil || liveReader != nil || len(reader.Resources) != 1 || len(retired.Resources) != 1 || reader.Resources[0].ID != id || retired.Resources[0].ID != id || retired.Resources[0].Action != "retire" || readJournal.Owners[id] != receipt.Reader || retireJournal.Owners[id] != receipt.Reader || !managedLateCompletedExportReaderMatches(reader.Resources[0].Desired, p, service, namespace, claim, stateRoot, clientState, serverName) {
		return empty, bad
	}
	core, owners, err := acceptedCreateStage(e.Store, e, "kubernetes/managed-complete-issuer")
	if err != nil {
		return empty, err
	}
	account := "Deployment/" + namespace + "/account-manager"
	for _, resource := range core.Resources {
		if resource.ID == account && environmentBootstrapOwnerUUID.MatchString(owners.Owners[account]) && managedLateCompletedAccountExportRecipe(resource.Desired, claim, clientState, serverState, serverName, service.Root.RootFingerprint) {
			return readCompletedAccountServerFromOwner(ctx, cfg, p, runtime, service, namespace, role, claim, clientState, serverState, serverName, resource.Desired, owners.Owners[account], receipt)
		}
	}
	return empty, bad
}

func bindStagingMissingCoreMedia(object deploymentplan.Object, settings map[string]string) bool {
	containers := certIssuerObjectList(environmentPKIPod(object)["containers"])
	if len(containers) != 1 {
		return false
	}
	container := deploymentplan.Map(containers[0])
	if container["name"] != "app" || mqttFoundationEnv(container, "VIDEO_CLOUD_CLIP_DIRECT_UPLOAD_ENABLED") != "true" {
		return false
	}
	for name, value := range settings {
		if value == "" || mqttFoundationEnv(container, name) != "" {
			return false
		}
	}
	container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(settings))
	return true
}

func bindStagingMissingBrokerCredentials(object deploymentplan.Object, key, secret string) bool {
	data := deploymentplan.Map(object["data"])
	values := map[string]string{"VIDEO_CLOUD_EMQX_API_KEY": key, "VIDEO_CLOUD_EMQX_API_SECRET": secret}
	for name, value := range values {
		if value == "" || data[name] != "" {
			return false
		}
	}
	for name, value := range values {
		data[name] = base64.StdEncoding.EncodeToString([]byte(value))
	}
	return true
}

func executeStagingApplicationsForward(ctx context.Context, runtime environmentCreateBootstrapRuntime, f *stagingApplicationsForward, apply environmentCreateBootstrapApply) error {
	stage := stagingApplicationsStage + "-startup-" + f.Original[:12]
	if err := apply(ctx, stage, f.Definitions); err != nil {
		return err
	}
	key := "kubernetes/" + stagingApplicationsStage
	receipt := mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": f.Original})
	if err := runtime.Execution.RecordReceipt(key, receipt); err != nil {
		return err
	}
	if err := runtime.Execution.CompleteEntry(key, time.Now().UTC()); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "[PASS] deployment.recovery.staging.application-startup original-allocation-and-broker-credentials-retained")
	return nil
}
