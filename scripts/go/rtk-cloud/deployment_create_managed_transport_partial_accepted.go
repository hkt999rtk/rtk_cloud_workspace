package main

import (
	"encoding/json"
	"path/filepath"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type createManagedTransportAcceptedForward struct {
	Review      string
	SourceSHA   string
	ArtifactSHA string
	Plan        *deploymentplan.Plan
	Journal     deploymentplan.Journal
	UnsealKey   string
	UnsealSHA   string
}

func managedTransportAcceptedForwardReview(j environmentCreateJournal) string {
	if len(j.Entries) < 5 {
		return ""
	}
	prefix := j
	prefix.Entries = j.Entries[:len(j.Entries)-3]
	previous := prefix
	previous.Entries = prefix.Entries[:len(prefix.Entries)-2]
	if managedTransportInterruptedForwardReviewDepth(prefix, 2) == "" || managedTransportInterruptedForwardReviewDepth(previous, 1) == "" {
		return ""
	}
	admission, forward, unseal := j.Entries[len(j.Entries)-3], j.Entries[len(j.Entries)-2], j.Entries[len(j.Entries)-1]
	const stagePrefix = "kubernetes/transport-managed-hosts-forward-"
	review := strings.TrimPrefix(forward.Key, stagePrefix)
	if !strings.HasPrefix(forward.Key, stagePrefix) || !environmentPreflightDigest.MatchString(review) || admission.Key != "openbao-recovery-admission-"+review || !strings.HasPrefix(unseal.Key, "openbao-managed-unseal-") {
		return ""
	}
	for _, entry := range []environmentCreateJournalEntry{admission, forward, unseal} {
		if entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 || !environmentPreflightDigest.MatchString(entry.IntentSHA256) {
			return ""
		}
	}
	return review
}

func validateManagedTransportAcceptedForwardJournal(plan *deploymentplan.Plan, journal deploymentplan.Journal) error {
	bad := func() error {
		return createProviderRecoveryBlock("accepted managed host continuation requires its exact seven completed owners and genuine child acceptance")
	}
	if plan == nil || len(plan.Resources) != 7 || journal.PlanSHA256 != plan.SHA256() || !journal.Accepted || !journal.AcceptanceStarted || journal.InFlight != "" || journal.Failed != "" || len(journal.Owners) != 7 || len(journal.Completed) != 7 {
		return bad()
	}
	for index, resource := range plan.Resources {
		if resource.UID == "" || journal.Owners[resource.ID] != resource.UID || journal.Completed[index] != resource.ID || resource.Baseline == nil || resource.Action != "retain" && resource.Action != "update" {
			return bad()
		}
		if resource.ID == "StatefulSet/"+plan.Stack+"-secrets/openbao" {
			if resource.Action != "update" {
				return bad()
			}
		} else if resource.Action != "retain" {
			return bad()
		}
	}
	return nil
}

func loadManagedTransportAcceptedForward(store secretStore, e *environmentCreateExecution) (*environmentCreateExecution, *createManagedTransportInterruptedForward, *createManagedTransportAcceptedForward, error) {
	bad := func() (*environmentCreateExecution, *createManagedTransportInterruptedForward, *createManagedTransportAcceptedForward, error) {
		return nil, nil, nil, createProviderRecoveryBlock("accepted managed hosts require the admitted original source, exact accepted child and genuine original-share unseal")
	}
	review := managedTransportAcceptedForwardReview(e.Journal)
	if review == "" {
		return bad()
	}
	a, source, err := loadAdmittedCreateRecovery(store, e, review)
	if err != nil || a.Plan.Schema != "rtk-cloud.create-managed-transports-partial-recovery/v1" || len(e.Journal.Entries) != len(source.Entries)+3 || len(e.Journal.KubernetesLeases) != len(source.KubernetesLeases)+1 || managedTransportInterruptedForwardReviewDepth(source, 2) == "" {
		return bad()
	}
	for index, entry := range source.Entries {
		if activeTransportJournalDigest(entry) != activeTransportJournalDigest(e.Journal.Entries[index]) {
			return bad()
		}
	}
	normalized := e.Journal
	normalized.Entries = source.Entries
	normalized.KubernetesLeases = e.Journal.KubernetesLeases[:len(source.KubernetesLeases)]
	if deploymentplan.Digest(normalized) != deploymentplan.Digest(source) {
		return bad()
	}
	historical := *e
	historical.Journal = source
	base, prior, err := loadManagedTransportPartialBase(store, &historical)
	if err != nil || prior == nil || prior.Previous == nil {
		return bad()
	}
	original, _, err := loadOriginalManagedTransportPartialPlan(store, base)
	if err != nil || a.Plan.Materials["accepted-service-core-managed-partial-original-plan"] != original.SHA256() {
		return bad()
	}
	entry := e.Journal.Entries[len(source.Entries)+1]
	var receipt struct {
		Plan   string            `json:"plan_sha256"`
		Owners map[string]string `json:"owners"`
	}
	if json.Unmarshal(entry.Receipt, &receipt) != nil || !environmentPreflightDigest.MatchString(receipt.Plan) {
		return bad()
	}
	lease := e.Journal.KubernetesLeases[len(source.KubernetesLeases)]
	if lease.UID == "" || lease.ResourceID != "Lease/"+source.Stack+"-video-cloud/rtk-deployment-execution" || strings.SplitN(lease.Holder, ":", 2)[0] != receipt.Plan {
		return bad()
	}
	var plan, preview deploymentplan.Plan
	var journal deploymentplan.Journal
	path := filepath.Join(store.Root, "deployment/rollback", receipt.Plan)
	if readExecutionJSON(filepath.Join(path, "plan.json"), &plan) != nil || readExecutionJSON(filepath.Join(path, "journal.json"), &journal) != nil || plan.SHA256() != receipt.Plan || validateManagedTransportAcceptedForwardJournal(&plan, journal) != nil || deploymentplan.Digest(journal.Owners) != deploymentplan.Digest(receipt.Owners) || readExecutionJSON(filepath.Join(store.Root, "deployment/create/openbao-recovery", review, "openbao-root-trust-plan.json"), &preview) != nil || preview.SHA256() != a.Plan.Materials["accepted-service-core-managed-partial-forward-plan"] {
		return bad()
	}
	checked := plan
	checked.BindingsSHA256 = preview.BindingsSHA256
	intent, err := environmentCreateKubernetesStageIntent(&plan)
	hash, hashErr := environmentCreatePlanSHA256(intent)
	if checked.SHA256() != preview.SHA256() || err != nil || hashErr != nil || hash != entry.IntentSHA256 {
		return bad()
	}
	owners := map[string]deploymentplan.Resource{}
	for _, resource := range prior.Plan.Resources {
		if resource.ID == "Deployment/"+plan.Stack+"-video-cloud/pki-controller" || resource.ID == "ConfigMap/"+plan.Stack+"-secrets/pki-create-service-trust" {
			owners[resource.ID] = resource
		}
	}
	partial := &createManagedTransportPartialRecovery{Original: original, PriorForward: prior, AdditionalOwners: owners}
	definitions := []deploymentplan.Definition{}
	for _, resource := range plan.Resources {
		definitions = append(definitions, deploymentplan.Definition{Phase: resource.Phase, Object: resource.Desired, Dependencies: resource.Dependencies})
		found := false
		for _, old := range prior.Plan.Resources {
			if resource.ID == old.ID && deploymentplan.Matches(old.Desired, resource.Baseline) && resource.UID == old.UID {
				found = true
			}
		}
		if !found {
			return bad()
		}
	}
	if validateManagedTransportPartialForwardDefinitions(partial, definitions) != nil || validateManagedTransportPartialForwardActions(partial, &plan) != nil {
		return bad()
	}
	unseal := e.Journal.Entries[len(source.Entries)+2]
	var observed struct {
		Pod         string `json:"pod_uid"`
		Revision    string `json:"revision"`
		Initialized bool   `json:"initialized"`
		Sealed      bool   `json:"sealed"`
	}
	if json.Unmarshal(unseal.Receipt, &observed) != nil || !environmentBootstrapOwnerUUID.MatchString(observed.Pod) || observed.Revision == "" || !observed.Initialized || observed.Sealed || unseal.Key != "openbao-managed-unseal-"+observed.Pod || unseal.Phase != "openbao-unseal" {
		return bad()
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "openbao/init.json"), true)
	var initialized struct {
		Keys []string `json:"unseal_keys_b64"`
	}
	if err != nil || json.Unmarshal(raw, &initialized) != nil || len(initialized.Keys) != 1 || initialized.Keys[0] == "" {
		return bad()
	}
	unsealIntent := deploymentplan.Object{"pod_uid": observed.Pod, "controller_uid": journal.Owners["StatefulSet/"+plan.Stack+"-secrets/openbao"], "revision": observed.Revision, "original_init_sha256": environmentPreflightBytesSHA256(raw), "unseal_sha256": environmentPreflightBytesSHA256([]byte(initialized.Keys[0]))}
	if unseal.IntentSHA256 != activeTransportJournalDigest(unsealIntent) {
		return bad()
	}
	return base, prior, &createManagedTransportAcceptedForward{Review: review, SourceSHA: deploymentplan.Digest(source), ArtifactSHA: deploymentplan.Digest(a), Plan: &plan, Journal: journal, UnsealKey: unseal.Key, UnsealSHA: activeTransportJournalDigest(unseal)}, nil
}

func validateManagedTransportAcceptedAuditDefinitions(partial *createManagedTransportPartialRecovery, definitions []deploymentplan.Definition) error {
	bad := func() error {
		return createProviderRecoveryBlock("accepted managed hosts may change only their exact original declarative OpenBao audit block")
	}
	if partial.AcceptedForward == nil || len(definitions) != 7 {
		return bad()
	}
	compiled, err := deploymentplan.Compile(partial.Original.Inputs, definitions, nil)
	if err != nil {
		return err
	}
	before := map[string]deploymentplan.Resource{}
	for _, resource := range partial.AcceptedForward.Plan.Resources {
		before[resource.ID] = resource
	}
	if len(before) != 7 || len(compiled.Resources) != 7 {
		return bad()
	}
	for _, resource := range compiled.Resources {
		old, exists := before[resource.ID]
		if !exists {
			return bad()
		}
		if resource.ID == "ConfigMap/"+partial.Original.Stack+"-secrets/openbao-file" {
			candidate := deploymentplan.Clone(resource.Desired)
			expected := deploymentplan.String(deploymentplan.Map(old.Desired["data"])["file.hcl"]) + managedTransportOriginalAuditBlock
			if deploymentplan.Map(candidate["data"])["file.hcl"] != expected {
				return bad()
			}
			deploymentplan.Map(candidate["data"])["file.hcl"] = deploymentplan.Map(old.Desired["data"])["file.hcl"]
			if deploymentplan.Digest(candidate) != deploymentplan.Digest(old.Desired) {
				return bad()
			}
		} else if deploymentplan.Digest(resource.Desired) != deploymentplan.Digest(old.Desired) {
			return bad()
		}
	}
	return nil
}

const managedTransportOriginalAuditBlock = "audit \"file\" \"deployment-audit\" {\n  options {\n    file_path = \"/openbao/audit/audit.jsonl\"\n    log_raw = \"false\"\n  }\n}\n"
