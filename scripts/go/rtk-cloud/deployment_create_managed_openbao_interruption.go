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

func managedTransportOpenBaoPullRepair(r *createServiceSessionRecovery) bool {
	return r != nil && r.ManagedTransportRecovery != nil && r.ManagedTransportRecovery.Partial != nil && r.ManagedTransportRecovery.Partial.PostManaged == nil && r.ManagedTransportRecovery.Partial.AcceptedForward == nil && r.ManagedTransportRecovery.Partial.PriorForward != nil && r.ManagedTransportRecovery.Partial.PriorForward.Previous != nil
}

func managedTransportOpenBaoAuditRepair(r *createServiceSessionRecovery) bool {
	return r != nil && r.ManagedTransportRecovery != nil && r.ManagedTransportRecovery.Partial != nil && r.ManagedTransportRecovery.Partial.PostManaged == nil && r.ManagedTransportRecovery.Partial.AcceptedForward != nil
}

func managedTransportOpenBaoRetained(r *createServiceSessionRecovery) bool {
	return r != nil && r.ManagedTransportRecovery != nil && r.ManagedTransportRecovery.Partial != nil && r.ManagedTransportRecovery.Partial.PostManaged != nil
}

// Only the authenticated second interrupted forward has written OpenBao while
// its installer never started. Qualify retained prerequisites, then require real
// native audit/authentication after the checked replacement becomes Ready.
func verifyManagedTransportOpenBaoIdentity(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery, restored bool) (string, deploymentplan.Object, error) {
	bad := func() (string, deploymentplan.Object, error) {
		return "", nil, createProviderRecoveryBlock("interrupted OpenBao replacement must retain original initialization, exact written owner and PVC-backed recipe")
	}
	if !managedTransportOpenBaoPullRepair(r) && !managedTransportOpenBaoAuditRepair(r) && !managedTransportOpenBaoRetained(r) || cfg.Environment != "dev" || cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] != "standard" || store.Root != e.Store.Root || b.Store.Environment != cfg.Environment {
		return bad()
	}
	partial := r.ManagedTransportRecovery.Partial
	latest := partial.PriorForward
	if validateManagedTransportWrittenOpenBaoJournal(latest.Plan, latest.Journal) != nil {
		return bad()
	}
	plan, writtenJournal := latest.Plan, latest.Journal
	accepted := partial.AcceptedForward
	if accepted != nil {
		if validateManagedTransportAcceptedForwardJournal(accepted.Plan, accepted.Journal) != nil {
			return bad()
		}
		plan, writtenJournal = accepted.Plan, accepted.Journal
	}
	post := partial.PostManaged
	if post != nil {
		if validateManagedTransportPostManagedJournal(post.Plan, post.Journal) != nil {
			return bad()
		}
		plan, writtenJournal = post.Plan, post.Journal
	}
	writtenPlan := plan
	if restored {
		plan = r.Candidate
	}
	if plan == nil {
		return bad()
	}
	ns := cfg.Values["CLOUD_STACK_NAME"] + "-secrets"
	id := "StatefulSet/" + ns + "/openbao"
	var wanted deploymentplan.Resource
	for _, resource := range plan.Resources {
		if resource.ID == id {
			wanted = resource
		}
	}
	owner := writtenJournal.Owners[id]
	set, err := b.Read(ctx, id)
	if err != nil || set == nil || owner == "" || wanted.UID != owner || deploymentplan.Map(set["metadata"])["uid"] != owner || !deploymentplan.Matches(wanted.Desired, set) {
		return bad()
	}
	pod, err := b.Read(ctx, "Pod/"+ns+"/openbao-0")
	if err != nil || pod == nil || validateOpenBaoPodOwner(set, pod) != nil {
		return bad()
	}
	meta, status := deploymentplan.Map(pod["metadata"]), deploymentplan.Map(set["status"])
	revision := deploymentplan.String(deploymentplan.Map(meta["labels"])["controller-revision-hash"])
	if meta["namespace"] != ns || meta["name"] != "openbao-0" || revision == "" || revision != status["updateRevision"] || status["observedGeneration"] != deploymentplan.Map(set["metadata"])["generation"] || !managedOpenBaoPodMatches(wanted.Desired, pod) {
		return bad()
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "openbao/init.json"), true)
	var initialized struct {
		Root string   `json:"root_token"`
		Keys []string `json:"unseal_keys_b64"`
	}
	if err != nil || json.Unmarshal(raw, &initialized) != nil || initialized.Root == "" || len(initialized.Keys) != 1 {
		return bad()
	}
	root, err := store.read("openbao/root-token")
	if err != nil || root != initialized.Root {
		return bad()
	}
	key, err := store.read("openbao/unseal-key")
	if err != nil || key != initialized.Keys[0] {
		return bad()
	}
	var original deploymentplan.Object
	var auditSHA string
	initCount, auditCount := 0, 0
	for _, entry := range e.Journal.Entries {
		if entry.Key == "openbao-initialize" {
			initCount++
			if entry.State != "completed" || entry.CompletedAt.IsZero() || json.Unmarshal(entry.Receipt, &original) != nil {
				return bad()
			}
		}
		if entry.Key == "openbao-audit-enable" {
			auditCount++
			var receipt map[string]string
			if entry.State != "completed" || entry.CompletedAt.IsZero() || json.Unmarshal(entry.Receipt, &receipt) != nil || !environmentPreflightDigest.MatchString(receipt["audit_sha256"]) {
				return bad()
			}
			auditSHA = receipt["audit_sha256"]
		}
	}
	if initCount != 1 || auditCount != 1 || original["init_sha256"] != environmentPreflightBytesSHA256(raw) || original["statefulset_uid"] != owner || original["root_token_ref"] != "openbao/root-token" || original["unseal_ref"] != "openbao/unseal-key" || original["pod_uid"] == meta["uid"] {
		return bad()
	}
	if !restored && accepted == nil {
		if err := validateManagedOpenBaoUnstartedPod(pod); err != nil {
			return "", nil, err
		}
	} else {
		// The native replacement callback must already have proved the original
		// initialized store/share on this exact replacement, never a new init.
		count := 0
		for _, entry := range e.Journal.Entries {
			if entry.Key == "openbao-managed-unseal-"+deploymentplan.String(meta["uid"]) {
				count++
				var receipt deploymentplan.Object
				if entry.State != "completed" || entry.CompletedAt.IsZero() || json.Unmarshal(entry.Receipt, &receipt) != nil || receipt["pod_uid"] != meta["uid"] || receipt["revision"] != revision || receipt["initialized"] != true || receipt["sealed"] != false {
					return bad()
				}
			}
		}
		if count != 1 {
			return bad()
		}
	}
	proof := deploymentplan.Object{"original_init_sha256": environmentPreflightBytesSHA256(raw), "original_audit_sha256": auditSHA, "statefulset_uid": owner, "pod_uid": meta["uid"], "revision": revision, "written_plan_sha256": writtenPlan.SHA256(), "written_journal_sha256": deploymentplan.Digest(writtenJournal), "original_root_token_sha256": environmentPreflightBytesSHA256([]byte(root)), "original_unseal_sha256": environmentPreflightBytesSHA256([]byte(key))}
	if accepted != nil {
		bound := false
		for _, entry := range e.Journal.Entries {
			if entry.Key == accepted.UnsealKey && activeTransportJournalDigest(entry) == accepted.UnsealSHA && entry.Key == "openbao-managed-unseal-"+deploymentplan.String(meta["uid"]) {
				bound = true
			}
		}
		if !bound {
			return bad()
		}
		status, statusErr := readEnvironmentCreateOpenBaoStatus(ctx, cfg, b)
		if statusErr != nil || !status.Initialized || status.Sealed {
			return bad()
		}
		ready := false
		for _, value := range certIssuerObjectList(deploymentplan.Map(pod["status"])["conditions"]) {
			c := deploymentplan.Map(value)
			if c["type"] == "Ready" && c["status"] == "True" {
				ready = true
			}
		}
		if !ready {
			return bad()
		}
		if !restored && post == nil {
			audit, auditErr := environmentCreateOpenBaoExec(ctx, cfg, b, []byte(root+"\n"), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao audit list -format=json\n")
			var devices deploymentplan.Object
			if auditErr != nil || json.Unmarshal(audit, &devices) != nil || len(devices) != 0 {
				return bad()
			}
			proof["observed_missing_audit_sha256"] = deploymentplan.Digest(devices)
			proof["accepted_forward_sha256"] = accepted.Plan.SHA256()
			proof["accepted_unseal_sha256"] = accepted.UnsealSHA
			process, processErr := readManagedOpenBaoAuditProcess(ctx, cfg, b)
			if processErr != nil {
				return "", nil, processErr
			}
			proof["provider_process_sha256"] = deploymentplan.Digest(process)
		}
	}
	if post != nil {
		audit, auditErr := environmentCreateOpenBaoExec(ctx, cfg, b, []byte(root+"\n"), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao audit list -format=json\n")
		var devices deploymentplan.Object
		if auditErr != nil || json.Unmarshal(audit, &devices) != nil {
			return bad()
		}
		device := deploymentplan.Map(devices["deployment-audit/"])
		options := deploymentplan.Map(device["options"])
		if environmentPreflightBytesSHA256(audit) != auditSHA || len(devices) != 1 || device["type"] != "file" || options["file_path"] != "/openbao/audit/audit.jsonl" || options["log_raw"] != "false" {
			return bad()
		}
		proof["native_audit_sha256"] = environmentPreflightBytesSHA256(audit)
		proof["accepted_audit_reload_sha256"] = post.ReloadSHA
		proof["accepted_native_verification_sha256"] = post.VerifiedSHA
		proof["accepted_managed_plan_sha256"] = post.Plan.SHA256()
		proof["accepted_unseal_sha256"] = accepted.UnsealSHA
	}
	return root, proof, e.Check(ctx)
}

// StatefulSet claim templates become actual Pod volumes. Normalize only
// those exact original ordinal claims and documented API defaults; extra or
// remapped volumes and altered container policy still fail qualification.
func managedOpenBaoPodMatches(set, pod deploymentplan.Object) bool {
	wanted := deploymentplan.Clone(environmentPKIPod(set))
	current := deploymentplan.Clone(deploymentplan.Map(pod["spec"]))
	volumes := certIssuerObjectList(wanted["volumes"])
	names := map[string]bool{}
	for _, value := range volumes {
		name := deploymentplan.String(deploymentplan.Map(value)["name"])
		if name == "" || names[name] {
			return false
		}
		names[name] = true
	}
	for _, value := range certIssuerObjectList(deploymentplan.Map(set["spec"])["volumeClaimTemplates"]) {
		name := deploymentplan.String(deploymentplan.Map(deploymentplan.Map(value)["metadata"])["name"])
		if name == "" || names[name] {
			return false
		}
		names[name] = true
		volumes = append(volumes, deploymentplan.Object{"name": name, "persistentVolumeClaim": deploymentplan.Object{"claimName": name + "-openbao-0"}})
	}
	actual := certIssuerObjectList(current["volumes"])
	less := func(a, b any) int {
		return strings.Compare(deploymentplan.String(deploymentplan.Map(a)["name"]), deploymentplan.String(deploymentplan.Map(b)["name"]))
	}
	slices.SortFunc(volumes, less)
	slices.SortFunc(actual, less)
	wanted["volumes"] = volumes
	current["volumes"] = actual
	return deploymentplan.Matches(deploymentplan.Object{"kind": "Pod", "spec": wanted}, deploymentplan.Object{"kind": "Pod", "spec": current})
}

func validateManagedOpenBaoUnstartedPod(pod deploymentplan.Object) error {
	bad := func() error {
		return createProviderRecoveryBlock("interrupted OpenBao pull repair requires the exact never-started installer and provider containers")
	}
	status := deploymentplan.Map(pod["status"])
	if status["phase"] != "Pending" {
		return bad()
	}
	init := certIssuerObjectList(status["initContainerStatuses"])
	if len(init) != 1 {
		return bad()
	}
	installer := deploymentplan.Map(init[0])
	state := deploymentplan.Map(installer["state"])
	reason := deploymentplan.String(deploymentplan.Map(state["waiting"])["reason"])
	if installer["name"] != "openbao-pki-install" || installer["ready"] == true || installer["restartCount"] != float64(0) || installer["containerID"] != nil && installer["containerID"] != "" || state["running"] != nil || state["terminated"] != nil || len(deploymentplan.Map(installer["lastState"])) != 0 || reason != "ImagePullBackOff" && reason != "ErrImagePull" {
		return bad()
	}
	containers := certIssuerObjectList(status["containerStatuses"])
	if len(containers) != 2 {
		return bad()
	}
	names := map[string]bool{}
	for _, value := range containers {
		c := deploymentplan.Map(value)
		name := deploymentplan.String(c["name"])
		s := deploymentplan.Map(c["state"])
		if names[name] || name != "openbao" && name != "openbao-pki" || c["ready"] == true || c["restartCount"] != float64(0) || c["containerID"] != nil && c["containerID"] != "" || s["running"] != nil || s["terminated"] != nil || len(deploymentplan.Map(c["lastState"])) != 0 || deploymentplan.Map(s["waiting"])["reason"] != "PodInitializing" {
			return bad()
		}
		names[name] = true
	}
	return nil
}

func verifyManagedTransportRetainedAuthentication(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery) error {
	if managedTransportOpenBaoAuditRepair(r) || managedTransportOpenBaoRetained(r) {
		if _, _, err := verifyManagedTransportOpenBaoIdentity(ctx, cfg, store, e, b, r, false); err != nil {
			return err
		}
		return verifyServiceSessionAuthentication(ctx, cfg, store, e, b, r)
	}
	if !managedTransportOpenBaoPullRepair(r) {
		return verifyServiceSessionAuthentication(ctx, cfg, store, e, b, r)
	}
	if _, _, err := verifyManagedTransportOpenBaoIdentity(ctx, cfg, store, e, b, r, false); err != nil {
		return err
	}
	_, _, _, err := verifyServiceSessionRecordedAuthentication(ctx, cfg, store, e, b, r)
	return err
}

func verifyManagedTransportRecordedOpenBaoRole(runtime environmentCreateBootstrapRuntime, cfg deploymentConfig, name string, accounts, policies []string) error {
	if !managedTransportOpenBaoPullRepair(runtime.ServiceSessionRecovery) || runtime.Execution == nil || !secretEnvironmentPattern.MatchString(name) || len(accounts) == 0 || len(policies) == 0 {
		return createProviderRecoveryBlock("recorded OpenBao role allowance requires the authenticated exact interrupted managed replacement")
	}
	intent := deploymentplan.Object{"role": name, "namespace": cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud", "service_accounts": accounts, "policies": policies, "audience": "openbao", "ttl": "15m", "no_default_policy": true}
	body, _ := json.Marshal(intent)
	hash, err := environmentCreatePlanSHA256(body)
	if err != nil {
		return err
	}
	count := 0
	for _, entry := range runtime.Execution.Journal.Entries {
		if entry.Key == "openbao-role-"+name+"-"+deploymentplan.Digest(intent)[:12] {
			count++
			var receipt deploymentplan.Object
			if entry.State != "completed" || entry.CompletedAt.IsZero() || entry.IntentSHA256 != hash || json.Unmarshal(entry.Receipt, &receipt) != nil || deploymentplan.Digest(receipt) != deploymentplan.Digest(intent) {
				return createProviderRecoveryBlock("original retained OpenBao role intent or receipt differs; replay is prohibited")
			}
		}
	}
	if count != 1 {
		return createProviderRecoveryBlock("exact original completed OpenBao role receipt is required before pull repair")
	}
	return runtime.Execution.Check(context.Background())
}

func verifyRecoveryInitializedOpenBao(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, r *createServiceSessionRecovery) (string, []byte, error) {
	if r != nil && r.ManagedLateSession != nil {
		if _, err := verifyCreateManagedOpenBaoACKNative(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: r}); err != nil {
			return "", nil, err
		}
		root, err := store.read("openbao/root-token")
		if err != nil {
			return "", nil, err
		}
		audit, err := environmentCreateOpenBaoExec(ctx, cfg, b, []byte(root+"\n"), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao audit list -format=json\n")
		return root, audit, err
	}
	if r != nil && r.ManagedCorePartial != nil {
		if _, err := verifyCreateManagedOpenBaoACKNative(ctx, cfg, environmentCreateBootstrapRuntime{Execution: e, Backend: b, ServiceSessionRecovery: r}); err != nil {
			return "", nil, err
		}
		root, err := store.read("openbao/root-token")
		if err != nil {
			return "", nil, err
		}
		audit, err := environmentCreateOpenBaoExec(ctx, cfg, b, []byte(root+"\n"), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao audit list -format=json\n")
		return root, audit, err
	}
	if !managedTransportOpenBaoPullRepair(r) && !managedTransportOpenBaoAuditRepair(r) && !managedTransportOpenBaoRetained(r) {
		return verifyInitializedOpenBaoIdentity(ctx, cfg, store, e, b)
	}
	root, _, err := verifyManagedTransportOpenBaoIdentity(ctx, cfg, store, e, b, r, false)
	if err != nil || !managedTransportOpenBaoRetained(r) {
		return root, nil, err
	}
	audit, err := environmentCreateOpenBaoExec(ctx, cfg, b, []byte(root+"\n"), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao audit list -format=json\n")
	return root, audit, err
}

func verifyManagedTransportRestoredOpenBao(ctx context.Context, cfg deploymentConfig, store secretStore, runtime environmentCreateBootstrapRuntime) error {
	r := runtime.ServiceSessionRecovery
	if !managedTransportOpenBaoPullRepair(r) && !managedTransportOpenBaoAuditRepair(r) {
		return nil
	}
	if managedTransportOpenBaoAuditRepair(r) {
		if err := reloadManagedTransportOriginalAudit(ctx, cfg, store, runtime); err != nil {
			return err
		}
	}
	root, proof, err := verifyManagedTransportOpenBaoIdentity(ctx, cfg, store, runtime.Execution, runtime.Backend, r, true)
	if err != nil {
		return err
	}
	status, err := readEnvironmentCreateOpenBaoStatus(ctx, cfg, runtime.Backend)
	if err != nil || !status.Initialized || status.Sealed {
		return createProviderRecoveryBlock("restored original OpenBao has not proved initialized and unsealed native state")
	}
	raw, err := environmentCreateOpenBaoExec(ctx, cfg, runtime.Backend, []byte(root+"\n"), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao audit list -format=json\n")
	var devices deploymentplan.Object
	if err != nil || json.Unmarshal(raw, &devices) != nil {
		return createProviderRecoveryBlock("restored original OpenBao native audit readback is unavailable")
	}
	device := deploymentplan.Map(devices["deployment-audit/"])
	options := deploymentplan.Map(device["options"])
	if device["type"] != "file" || options["file_path"] != "/openbao/audit/audit.jsonl" || options["log_raw"] != "false" {
		return createProviderRecoveryBlock("restored original OpenBao native redacted audit device differs")
	}
	authentication := *r
	authentication.Intent = r.RenewedIntent
	if err := verifyServiceSessionAuthentication(ctx, cfg, store, runtime.Execution, runtime.Backend, &authentication); err != nil {
		return err
	}
	// Verify the exact latest union of original accepted signer-role policies.
	prefix := "openbao-role-certissuer-" + cfg.Environment + "-"
	policies := []string{"create-" + r.Ceremony.Intermediate.ID + "-signer", "create-" + r.Ceremony.Intermediate.ID + "-service-client", "create-" + r.MQTTIntermediate.ID + "-signer", "create-" + r.OpenBaoIntermediate.ID + "-signer"}
	for _, entry := range runtime.Execution.Journal.Entries {
		if strings.HasPrefix(entry.Key, prefix) && entry.State == "completed" {
			var receipt struct {
				Policies []string `json:"policies"`
			}
			if json.Unmarshal(entry.Receipt, &receipt) != nil {
				return createProviderRecoveryBlock("retained certissuer policies differ")
			}
			policies = append(policies, receipt.Policies...)
		}
	}
	slices.Sort(policies)
	policies = slices.Compact(policies)
	if err := configureEnvironmentCreateOpenBaoRole(ctx, cfg, runtime, "certissuer-"+cfg.Environment, []string{"certissuer"}, policies); err != nil {
		return err
	}
	proof["native_audit_sha256"] = environmentPreflightBytesSHA256(raw)
	key := "openbao-managed-recovery-verified-" + r.Review
	body, _ := json.Marshal(proof)
	if err := runtime.Execution.BeginEntry(key, "openbao-verification", body, time.Now().UTC()); err != nil {
		return err
	}
	if err := runtime.Execution.RecordReceipt(key, body); err != nil {
		return err
	}
	return runtime.Execution.CompleteEntry(key, time.Now().UTC())
}
