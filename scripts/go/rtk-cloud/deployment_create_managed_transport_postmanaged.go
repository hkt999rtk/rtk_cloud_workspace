package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This exact boundary follows accepted managed hosts and the first API server
// enrollment/custody stages. Its continuation observes those owners and exports
// their retained state; it never replays their Jobs, host cutover or audit reload.
type createManagedTransportPostManaged struct {
	Review, SourceSHA, ArtifactSHA string
	Session                        string
	Plan                           *deploymentplan.Plan
	Journal                        deploymentplan.Journal
	ReloadSHA, VerifiedSHA         string
	ExportPlans                    map[string]*deploymentplan.Plan
	ExportJournals                 map[string]deploymentplan.Journal
	APIVolume                      environmentCreateVolumeOwner
	ExportRetire                   *createManagedTransportExportRetire `json:"ExportRetire,omitempty"`
}

func managedTransportPostManagedReview(j environmentCreateJournal) string {
	if review := managedTransportExportRetirementReview(j); review != "" {
		return review
	}
	if len(j.Entries) < 6 {
		return ""
	}
	tail := j.Entries[len(j.Entries)-6:]
	const prefix = "kubernetes/transport-managed-hosts-forward-"
	review := strings.TrimPrefix(tail[1].Key, prefix)
	if !strings.HasPrefix(tail[1].Key, prefix) || !environmentPreflightDigest.MatchString(review) {
		return ""
	}
	keys := []string{"openbao-recovery-admission-" + review, prefix + review, "openbao-managed-audit-reload-" + review, "openbao-managed-recovery-verified-" + review, "kubernetes/server-enrollment-video-cloud-api-pki", "kubernetes/server-custody-video-cloud-api-pki"}
	seen := map[string]bool{}
	for index, entry := range j.Entries {
		if seen[entry.Key] || entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 || !environmentPreflightDigest.MatchString(entry.IntentSHA256) {
			return ""
		}
		seen[entry.Key] = true
		if index >= len(j.Entries)-6 && entry.Key != keys[index-len(j.Entries)+6] {
			return ""
		}
	}
	before, count := managedTransportPostManagedOriginalPrefix(j, review)
	if count != 3 || managedTransportAcceptedForwardReview(before) == "" {
		return ""
	}
	return review
}

func managedTransportPostManagedOriginalPrefix(j environmentCreateJournal, review string) (environmentCreateJournal, int) {
	before := j
	before.Entries = slices.Clone(j.Entries[:len(j.Entries)-6])
	count := 0
	for index, entry := range before.Entries {
		if entry.Key != "kubernetes/transport-managed-hosts" && !strings.HasPrefix(entry.Key, "kubernetes/transport-managed-hosts-forward-") {
			continue
		}
		var receipt map[string]string
		if json.Unmarshal(entry.Receipt, &receipt) != nil || receipt["actual_qualified_forward_stage"] != "kubernetes/transport-managed-hosts-forward-"+review || !environmentPreflightDigest.MatchString(receipt["original_failed_plan_sha256"]) || !environmentBootstrapOwnerUUID.MatchString(receipt["retained_session_id"]) {
			continue
		}
		before.Entries[index].State = "pending"
		before.Entries[index].CompletedAt = time.Time{}
		before.Entries[index].Receipt = nil
		count++
	}
	return before, count
}

func loadManagedTransportPostManaged(store secretStore, e *environmentCreateExecution) (*environmentCreateExecution, *createManagedTransportInterruptedForward, *createManagedTransportAcceptedForward, *createManagedTransportPostManaged, error) {
	if managedTransportExportRetirementReview(e.Journal) != "" {
		return loadManagedTransportExportRetirement(store, e)
	}
	bad := func() (*environmentCreateExecution, *createManagedTransportInterruptedForward, *createManagedTransportAcceptedForward, *createManagedTransportPostManaged, error) {
		return nil, nil, nil, nil, createProviderRecoveryBlock("post-managed API export requires the exact admitted audit repair, completed original aliases and accepted first server owners")
	}
	review := managedTransportPostManagedReview(e.Journal)
	if review == "" {
		return bad()
	}
	a, source, err := loadAdmittedCreateRecovery(store, e, review)
	if err != nil || a.Plan.Schema != "rtk-cloud.create-managed-transports-partial-recovery/v1" || managedTransportAcceptedForwardReview(source) == "" || len(e.Journal.Entries) != len(source.Entries)+6 || len(e.Journal.KubernetesLeases) != len(source.KubernetesLeases)+3 {
		return bad()
	}
	historical := *e
	historical.Journal = source
	base, prior, accepted, err := loadManagedTransportAcceptedForward(store, &historical)
	if err != nil || prior == nil || prior.Previous == nil || accepted == nil {
		return bad()
	}
	original, _, err := loadOriginalManagedTransportPartialPlan(store, base)
	if err != nil || a.Plan.Materials["accepted-service-core-managed-partial-original-plan"] != original.SHA256() {
		return bad()
	}
	aliases := map[string]string{"kubernetes/transport-managed-hosts": original.SHA256(), "kubernetes/transport-managed-hosts-forward-" + prior.Review: prior.Plan.SHA256(), "kubernetes/transport-managed-hosts-forward-" + prior.Previous.Review: prior.Previous.Plan.SHA256()}
	before := e.Journal
	before.Entries = slices.Clone(e.Journal.Entries[:len(source.Entries)])
	count, session := 0, ""
	for index, entry := range before.Entries {
		failed, alias := aliases[entry.Key]
		if !alias {
			if activeTransportJournalDigest(entry) != activeTransportJournalDigest(source.Entries[index]) {
				return bad()
			}
			continue
		}
		var receipt map[string]string
		old := source.Entries[index]
		if old.Key != entry.Key || old.State != "pending" || !old.CompletedAt.IsZero() || len(old.Receipt) != 0 || json.Unmarshal(entry.Receipt, &receipt) != nil || len(receipt) != 3 || receipt["actual_qualified_forward_stage"] != "kubernetes/transport-managed-hosts-forward-"+review || receipt["original_failed_plan_sha256"] != failed || !environmentBootstrapOwnerUUID.MatchString(receipt["retained_session_id"]) || session != "" && session != receipt["retained_session_id"] {
			return bad()
		}
		session = receipt["retained_session_id"]
		checked := entry
		checked.State, checked.CompletedAt, checked.Receipt = old.State, old.CompletedAt, old.Receipt
		if activeTransportJournalDigest(checked) != activeTransportJournalDigest(old) {
			return bad()
		}
		before.Entries[index] = old
		count++
	}
	before.KubernetesLeases = e.Journal.KubernetesLeases[:len(source.KubernetesLeases)]
	if count != 3 || deploymentplan.Digest(before) != deploymentplan.Digest(source) {
		return bad()
	}
	plan, journal, err := managedTransportPostAcceptedStage(store, e, "kubernetes/transport-managed-hosts-forward-"+review)
	var preview deploymentplan.Plan
	if err != nil || validateManagedTransportPostManagedJournal(plan, journal) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/create/openbao-recovery", review, "openbao-root-trust-plan.json"), &preview) != nil || preview.SHA256() != a.Plan.Materials["accepted-service-core-managed-partial-forward-plan"] {
		return bad()
	}
	checked := *plan
	checked.BindingsSHA256 = preview.BindingsSHA256
	if checked.SHA256() != preview.SHA256() {
		return bad()
	}
	partial := &createManagedTransportPartialRecovery{Original: original, PriorForward: prior, AcceptedForward: accepted}
	defs := []deploymentplan.Definition{}
	for _, resource := range plan.Resources {
		defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: resource.Desired, Dependencies: resource.Dependencies})
		found := false
		for _, old := range accepted.Plan.Resources {
			if resource.ID == old.ID && resource.UID == old.UID && journal.Owners[resource.ID] == old.UID && deploymentplan.Matches(old.Desired, resource.Baseline) {
				found = true
			}
		}
		if !found {
			return bad()
		}
	}
	if validateManagedTransportPartialForwardDefinitions(partial, defs) != nil || validateManagedTransportPartialForwardActions(partial, plan) != nil {
		return bad()
	}
	post := &createManagedTransportPostManaged{Review: review, SourceSHA: deploymentplan.Digest(source), ArtifactSHA: deploymentplan.Digest(a), Session: session, Plan: plan, Journal: journal, ExportPlans: map[string]*deploymentplan.Plan{}, ExportJournals: map[string]deploymentplan.Journal{}}
	for index, key := range []string{"kubernetes/transport-managed-hosts-forward-" + review, "kubernetes/server-enrollment-video-cloud-api-pki", "kubernetes/server-custody-video-cloud-api-pki"} {
		var current *deploymentplan.Plan
		var child deploymentplan.Journal
		if index == 0 {
			current, child = plan, journal
		} else {
			current, child, err = managedTransportPostAcceptedStage(store, e, key)
			if err != nil || len(current.Resources) != 1 || len(child.Completed) != 1 || len(child.Owners) != 1 {
				return bad()
			}
			kind, name := "Job", "pki-enroll-video-cloud-api-pki-server-"+strings.ReplaceAll(session, "-", "")[:12]
			if index == 2 {
				kind, name = "Deployment", "pki-export-video-cloud-api-pki-"+source.PlanSHA256[:12]
			}
			if current.Resources[0].ID != kind+"/"+source.Stack+"-video-cloud/"+name {
				return bad()
			}
			post.ExportPlans[key], post.ExportJournals[key] = current, child
		}
		lease := e.Journal.KubernetesLeases[len(source.KubernetesLeases)+index]
		if lease.UID == "" || lease.ResourceID != "Lease/"+source.Stack+"-video-cloud/rtk-deployment-execution" || strings.SplitN(lease.Holder, ":", 2)[0] != current.SHA256() {
			return bad()
		}
	}
	if validateManagedTransportPostManagedProofs(store, e, accepted, post, len(source.Entries)) != nil {
		return bad()
	}
	return base, prior, accepted, post, nil
}

func managedTransportPostAcceptedStage(store secretStore, e *environmentCreateExecution, key string) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	plan, journal, err := acceptedCreateStage(store, e, key)
	bad := func() (*deploymentplan.Plan, deploymentplan.Journal, error) {
		return nil, journal, createProviderRecoveryBlock("post-managed accepted stage intent or receipt differs: stage=" + key)
	}
	if err != nil || !journal.AcceptanceStarted || plan.Environment != e.Journal.Environment || plan.Stack != e.Journal.Stack {
		return bad()
	}
	intent, intentErr := environmentCreateKubernetesStageIntent(plan)
	hash, hashErr := environmentCreatePlanSHA256(intent)
	if intentErr != nil || hashErr != nil {
		return bad()
	}
	for _, entry := range e.Journal.Entries {
		if entry.Key != key {
			continue
		}
		var receipt struct {
			Plan   string            `json:"plan_sha256"`
			Owners map[string]string `json:"owners"`
		}
		if entry.State != "completed" || entry.CompletedAt.IsZero() || entry.IntentSHA256 != hash || entry.Phase != strings.TrimPrefix(key, "kubernetes/") || json.Unmarshal(entry.Receipt, &receipt) != nil || receipt.Plan != plan.SHA256() || deploymentplan.Digest(receipt.Owners) != deploymentplan.Digest(journal.Owners) {
			return bad()
		}
		for index, resource := range plan.Resources {
			if journal.Completed[index] != resource.ID || journal.Owners[resource.ID] == "" || resource.UID != "" && resource.UID != journal.Owners[resource.ID] {
				return bad()
			}
		}
		return plan, journal, nil
	}
	return bad()
}

func validateManagedTransportPostManagedJournal(plan *deploymentplan.Plan, journal deploymentplan.Journal) error {
	bad := func() error {
		return createProviderRecoveryBlock("post-managed hosts require exact seven-owner audit-only acceptance")
	}
	if plan == nil || len(plan.Resources) != 7 || journal.PlanSHA256 != plan.SHA256() || !journal.Accepted || !journal.AcceptanceStarted || journal.InFlight != "" || journal.Failed != "" || len(journal.Completed) != 7 || len(journal.Owners) != 7 {
		return bad()
	}
	for index, resource := range plan.Resources {
		if resource.UID == "" || journal.Owners[resource.ID] != resource.UID || journal.Completed[index] != resource.ID || resource.Baseline == nil {
			return bad()
		}
		if resource.ID == "ConfigMap/"+plan.Stack+"-secrets/openbao-file" {
			if resource.Action != "update" {
				return bad()
			}
		} else if resource.Action != "retain" || !deploymentplan.Matches(resource.Desired, resource.Baseline) {
			return bad()
		}
	}
	return nil
}

func validateManagedTransportPostManagedProofs(store secretStore, e *environmentCreateExecution, accepted *createManagedTransportAcceptedForward, post *createManagedTransportPostManaged, sourceLength int) error {
	bad := func() error {
		return createProviderRecoveryBlock("completed original audit signal and native verification proof differ")
	}
	reload, verified := e.Journal.Entries[sourceLength+2], e.Journal.Entries[sourceLength+3]
	var signal, proof deploymentplan.Object
	if reload.Phase != "openbao-audit-reload" || verified.Phase != "openbao-verification" || json.Unmarshal(reload.Receipt, &signal) != nil || json.Unmarshal(verified.Receipt, &proof) != nil || verified.IntentSHA256 != activeTransportJournalDigest(proof) {
		return bad()
	}
	intent := deploymentplan.Clone(signal)
	delete(intent, "native_audit_sha256")
	if reload.IntentSHA256 != activeTransportJournalDigest(intent) || signal["signal"] != "SIGHUP" || signal["audit_path"] != "/openbao/audit/audit.jsonl" || signal["log_raw"] != false || !environmentPreflightDigest.MatchString(deploymentplan.String(signal["provider_process_sha256"])) {
		return bad()
	}
	setID, configID := "StatefulSet/"+post.Plan.Stack+"-secrets/openbao", "ConfigMap/"+post.Plan.Stack+"-secrets/openbao-file"
	var hcl string
	for _, resource := range post.Plan.Resources {
		if resource.ID == configID {
			hcl = deploymentplan.String(deploymentplan.Map(resource.Desired["data"])["file.hcl"])
		}
	}
	if hcl == "" || signal["hcl_sha256"] != environmentPreflightBytesSHA256([]byte(hcl)) || signal["statefulset_uid"] != post.Journal.Owners[setID] || signal["configmap_uid"] != post.Journal.Owners[configID] || proof["statefulset_uid"] != post.Journal.Owners[setID] || signal["pod_uid"] != proof["pod_uid"] || proof["written_plan_sha256"] != accepted.Plan.SHA256() || proof["written_journal_sha256"] != deploymentplan.Digest(accepted.Journal) || !environmentPreflightDigest.MatchString(deploymentplan.String(proof["original_audit_sha256"])) || signal["native_audit_sha256"] != proof["original_audit_sha256"] || proof["native_audit_sha256"] != proof["original_audit_sha256"] {
		return bad()
	}
	var unseal deploymentplan.Object
	for _, entry := range e.Journal.Entries {
		if entry.Key == accepted.UnsealKey {
			if activeTransportJournalDigest(entry) != accepted.UnsealSHA || json.Unmarshal(entry.Receipt, &unseal) != nil || unseal["pod_uid"] != proof["pod_uid"] || unseal["revision"] != proof["revision"] {
				return bad()
			}
		}
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "openbao/init.json"), true)
	root, rootErr := store.read("openbao/root-token")
	share, shareErr := store.read("openbao/unseal-key")
	if err != nil || rootErr != nil || shareErr != nil || proof["original_init_sha256"] != environmentPreflightBytesSHA256(raw) || proof["original_root_token_sha256"] != environmentPreflightBytesSHA256([]byte(root)) || proof["original_unseal_sha256"] != environmentPreflightBytesSHA256([]byte(share)) {
		return bad()
	}
	post.ReloadSHA, post.VerifiedSHA = activeTransportJournalDigest(reload), activeTransportJournalDigest(verified)
	return nil
}

func verifyManagedTransportAcceptedExportStage(ctx context.Context, runtime environmentCreateBootstrapRuntime, stage string, definition deploymentplan.Definition) (bool, error) {
	r := runtime.ServiceSessionRecovery
	if r == nil || r.ManagedTransportRecovery == nil || r.ManagedTransportRecovery.Partial == nil || r.ManagedTransportRecovery.Partial.PostManaged == nil {
		return false, nil
	}
	post := r.ManagedTransportRecovery.Partial.PostManaged
	key := "kubernetes/" + stage
	plan, exists := post.ExportPlans[key]
	if !exists {
		return false, nil
	}
	bad := func() (bool, error) {
		return false, createProviderRecoveryBlock("accepted API server enrollment/custody continuation differs: stage=" + key)
	}
	journal := post.ExportJournals[key]
	if plan == nil || len(plan.Resources) != 1 || journal.PlanSHA256 != plan.SHA256() || !journal.Accepted || !journal.AcceptanceStarted || journal.InFlight != "" || journal.Failed != "" || len(journal.Completed) != 1 || len(journal.Owners) != 1 {
		return bad()
	}
	resource := plan.Resources[0]
	if journal.Completed[0] != resource.ID || journal.Owners[resource.ID] == "" || deploymentplan.Digest(resource.Desired) != deploymentplan.Digest(definition.Object) {
		return bad()
	}
	actual, err := runtime.Backend.Read(ctx, resource.ID)
	if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != journal.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, actual) {
		return bad()
	}
	retained := resource
	retained.UID, retained.Action = journal.Owners[resource.ID], "retain"
	if runtime.Backend.Wait(ctx, retained) != nil {
		return bad()
	}
	return true, runtime.Execution.Check(ctx)
}

func verifyManagedTransportPostManagedExportOwners(ctx context.Context, cfg deploymentConfig, e *environmentCreateExecution, b environmentKubernetesBackend, post *createManagedTransportPostManaged) error {
	bad := func(stage string) error {
		return createProviderRecoveryBlock("post-managed original API server owner differs: stage=" + stage)
	}
	ledger, err := readEnvironmentCreateLedger(e)
	var active managedTransportVolumeLedgerActive
	if err != nil || readExecutionJSON(filepath.Join(e.Store.Root, "deployment/resources/active.json"), &active) != nil || validateManagedTransportVolumeLedger(ledger, active) != nil {
		return bad("original-api-volume-ledger")
	}
	claimID := "PersistentVolumeClaim/" + cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud/video-cloud-api-pki-controller-identity"
	owner, err := managedTransportRecordedHostVolume(ledger, claimID)
	if err != nil {
		return bad("original-api-private-volume")
	}
	claim, claimErr := b.Read(ctx, claimID)
	volume, volumeErr := b.Read(ctx, "PersistentVolume//"+owner.PVName)
	if claimErr != nil || volumeErr != nil || validateManagedTransportHostVolume(owner, claim, volume) != nil {
		return bad("original-api-private-volume")
	}
	post.APIVolume = owner
	if post.ExportRetire != nil && post.ExportRetire.ClaimUID != owner.PVCUID {
		return bad("retained-api-private-volume")
	}
	for _, key := range []string{"kubernetes/server-enrollment-video-cloud-api-pki", "kubernetes/server-custody-video-cloud-api-pki"} {
		plan, journal := post.ExportPlans[key], post.ExportJournals[key]
		if plan == nil || len(plan.Resources) != 1 {
			return bad(key)
		}
		resource := plan.Resources[0]
		actual, err := b.Read(ctx, resource.ID)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != journal.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, actual) {
			return bad(key)
		}
		if strings.Contains(key, "server-enrollment-") {
			status := deploymentplan.Map(actual["status"])
			if actual["kind"] != "Job" || status["succeeded"] != float64(1) || status["failed"] != nil && status["failed"] != float64(0) || status["active"] != nil && status["active"] != float64(0) {
				return bad(key)
			}
		} else {
			if actual["kind"] != "Deployment" {
				return bad(key)
			}
			owned, err := selectEnvironmentCreateExportPod(ctx, b, resource.Desired, journal.Owners[resource.ID])
			if err != nil || owned.Verify(ctx, b, resource.Desired, journal.Owners[resource.ID]) != nil {
				return bad(key)
			}
			if post.ExportRetire != nil && (owned.UID != post.ExportRetire.PodUID || owned.ReplicaSetUID != post.ExportRetire.ReplicaSetUID || journal.Owners[resource.ID] != post.ExportRetire.ReaderUID) {
				return bad("retained-export-reader-pod")
			}
			if post.ExportRetire != nil {
				if err := verifyManagedTransportExportRetirementMaterial(ctx, cfg, e, b, post, owned, resource); err != nil {
					return err
				}
			}
		}
	}
	id := "Secret/" + cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud/video-cloud-api-pki-tls"
	secret, err := b.Read(ctx, id)
	if err != nil || secret != nil {
		return bad("original-api-serving-secret-absent")
	}
	return e.Check(ctx)
}

func validateManagedTransportPostManagedDefinitions(partial *createManagedTransportPartialRecovery, definitions []deploymentplan.Definition) error {
	bad := func() error {
		return createProviderRecoveryBlock("post-managed API export permits only observation of its seven completed transport resources")
	}
	if partial.PostManaged == nil || partial.PostManaged.Plan == nil || len(definitions) != 7 {
		return bad()
	}
	plan, err := deploymentplan.Compile(partial.Original.Inputs, definitions, nil)
	if err != nil || len(plan.Resources) != 7 {
		return bad()
	}
	before := map[string]deploymentplan.Object{}
	for _, resource := range partial.PostManaged.Plan.Resources {
		before[resource.ID] = resource.Desired
	}
	for _, resource := range plan.Resources {
		old, found := before[resource.ID]
		if !found || deploymentplan.Digest(old) != deploymentplan.Digest(resource.Desired) {
			return bad()
		}
	}
	return nil
}

// A completed API-export continuation observes its accepted transport owners.
// The historical PID producer is corrected later by the separately admitted ACK
// repair; observing it must not silently request a new host rollout. Only that
// exact old marker may differ from the maintained recipe, and the unchanged
// seven-resource comparison still binds every resulting desired body.
func preserveManagedTransportPostManagedDefinitions(p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, definitions []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	r := runtime.ServiceSessionRecovery
	if r == nil || r.ManagedTransportRecovery == nil || r.ManagedTransportRecovery.Partial == nil || r.ManagedTransportRecovery.Partial.PostManaged == nil {
		return definitions, nil
	}
	partial := r.ManagedTransportRecovery.Partial
	post := partial.PostManaged
	bad := func() ([]deploymentplan.Definition, error) {
		return nil, createProviderRecoveryBlock("post-managed transport observation requires its exact accepted recipe and known original PID producer")
	}
	if partial.Original == nil || post.Plan == nil || post.Plan.Environment != p.Environment || post.Plan.Stack != p.Stack || partial.Original.Environment != p.Environment || partial.Original.Stack != p.Stack || post.Session != service.BootstrapSessionID || validateManagedTransportPostManagedJournal(post.Plan, post.Journal) != nil {
		return bad()
	}
	if validateManagedTransportPostManagedDefinitions(partial, definitions) == nil {
		return definitions, nil
	}
	id := "StatefulSet/" + p.Stack + "-secrets/openbao"
	var original deploymentplan.Object
	for _, resource := range post.Plan.Resources {
		if resource.ID == id {
			if original != nil {
				return bad()
			}
			original = resource.Desired
		}
	}
	if original == nil {
		return bad()
	}
	oldContainers, err := environmentPKINamedFields(environmentPKIPod(original)["containers"])
	if err != nil || oldContainers["openbao"] == nil {
		return bad()
	}
	oldCommand := certIssuerObjectList(oldContainers["openbao"]["command"])
	const oldMarker = `printf '%s\n' "$$" > /run/openbao-pki/private/openbao.pid`
	const currentMarker = `printf '%s\n' "$$$$" > /run/openbao-pki/private/openbao.pid`
	if len(oldCommand) != 3 || oldCommand[0] != "/bin/sh" || oldCommand[1] != "-ec" || strings.Count(deploymentplan.String(oldCommand[2]), oldMarker) != 1 {
		return bad()
	}
	retained := slices.Clone(definitions)
	for i, definition := range definitions {
		if deploymentplan.ID(definition.Object) != id {
			continue
		}
		retained[i].Object = deploymentplan.Clone(definition.Object)
		containers, err := environmentPKINamedFields(environmentPKIPod(retained[i].Object)["containers"])
		if err != nil || containers["openbao"] == nil {
			return bad()
		}
		command := certIssuerObjectList(containers["openbao"]["command"])
		if len(command) != 3 || command[0] != oldCommand[0] || command[1] != oldCommand[1] || strings.Count(deploymentplan.String(command[2]), currentMarker) != 1 || strings.Replace(deploymentplan.String(command[2]), currentMarker, oldMarker, 1) != oldCommand[2] {
			return bad()
		}
		containers["openbao"]["command"] = slices.Clone(oldCommand)
	}
	if validateManagedTransportPostManagedDefinitions(partial, retained) != nil {
		return bad()
	}
	return retained, nil
}
