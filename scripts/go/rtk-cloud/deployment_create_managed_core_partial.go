package main

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

const managedCorePartialSchema = "rtk-cloud.create-managed-complete-issuer-partial-recovery/v1"
const managedCorePartialForwardPlanFilename = "managed-complete-issuer-forward-plan.json"

type createManagedCorePartialRecovery struct {
	PriorForward                                 *createManagedCoreInterruptedForward  `json:"PriorForward,omitempty"`
	CertIssuerPrepare                            *createCertIssuerPrepareImageRecovery `json:"CertIssuerPrepare,omitempty"`
	PriorAdmission                               *createManagedCorePendingAdmission    `json:"PriorAdmission,omitempty"`
	Action                                       managedCoreObservationAction
	OriginalRepairReview, SourceSHA, ArtifactSHA string
	ForwardReview                                string `json:"ForwardReview,omitempty"`
	Plan                                         *deploymentplan.Plan
	Journal                                      deploymentplan.Journal
	Apps                                         *deploymentplan.Plan
	AppsJournal                                  deploymentplan.Journal
	Routing                                      *deploymentplan.Plan
	RoutingJournal                               deploymentplan.Journal
}

func managedCorePartialBlock(stage string) error {
	return createProviderRecoveryBlock("managed complete issuer continuation requires exact original accepted applications, native ACK host and partial core owners: stage=" + stage)
}

// This selector names the actually stopped original final-core stage. The pure
// loader separately authenticates its captured admission and complete suffix.
func managedCorePartialInitialReview(j environmentCreateJournal) string {
	if len(j.Entries) < 15 {
		return ""
	}
	tail := j.Entries[len(j.Entries)-7:]
	prefix := "openbao-recovery-admission-"
	review := strings.TrimPrefix(tail[0].Key, prefix)
	if !strings.HasPrefix(tail[0].Key, prefix) || !environmentPreflightDigest.MatchString(review) {
		return ""
	}
	keys := []string{prefix + review, "kubernetes/openbao-host-ack-forward-" + review, "", "kubernetes/managed-application-owners-forward-" + review, "kubernetes/native-app-routing-profile", "native-app-routing-profile-proof", "kubernetes/managed-complete-issuer"}
	phases := []string{"recovery", "openbao-host-ack-forward-" + review, "openbao-unseal", "managed-application-owners-forward-" + review, "native-app-routing-profile", "bootstrap", "managed-complete-issuer"}
	for index, entry := range tail {
		if entry.Phase != phases[index] {
			return ""
		}
		if index == 2 {
			uid := strings.TrimPrefix(entry.Key, "openbao-managed-unseal-")
			if entry.Key != "openbao-managed-unseal-"+uid || !environmentBootstrapOwnerUUID.MatchString(uid) || entry.Phase != "openbao-unseal" {
				return ""
			}
		} else if entry.Key != keys[index] {
			return ""
		}
		if !environmentPreflightDigest.MatchString(entry.IntentSHA256) {
			return ""
		}
		if index < 6 {
			if entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 {
				return ""
			}
		} else if entry.Phase != "managed-complete-issuer" || entry.State != "pending" || !entry.CompletedAt.IsZero() || len(entry.Receipt) != 0 {
			return ""
		}
	}
	for _, entry := range j.Entries[:len(j.Entries)-7] {
		if entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 {
			return ""
		}
	}
	return review
}

func managedCorePartialResourceIDs(stack, session string) []string {
	ns := stack + "-video-cloud"
	return []string{"ClusterRole//" + stack + "-bootstrap-cleanup", "ClusterRoleBinding//" + stack + "-bootstrap-cleanup", "ConfigMap/" + stack + "-account-manager/pki-create-service-trust", "ConfigMap/" + ns + "/pki-bootstrap-cleanup-binding-" + session[:12], "ConfigMap/" + ns + "/pki-create-openbao-tls-consumer-trust", "ConfigMap/" + ns + "/pki-create-service-trust", "Role/" + ns + "/bootstrap-cleanup", "RoleBinding/" + ns + "/bootstrap-cleanup", "Service/" + stack + "-account-manager/account-manager", "Service/" + ns + "/certissuer", "Service/" + ns + "/pki-controller", "ServiceAccount/" + ns + "/certissuer", "ServiceAccount/" + ns + "/pki-controller", "Deployment/" + ns + "/pki-controller", "Deployment/" + ns + "/certissuer", "Deployment/" + stack + "-account-manager/account-manager"}
}

func validateManagedCorePartialJournal(plan *deploymentplan.Plan, j deploymentplan.Journal) error {
	if plan == nil || plan.Version != deploymentplan.Version || plan.Environment != "dev" || plan.Stack != "video-cloud-dev" || plan.Operation != "scoped" || len(plan.Resources) != 16 || j.PlanSHA256 != plan.SHA256() || j.Accepted || j.AcceptanceStarted || j.InFlight != "" || j.Failed != "Deployment/"+plan.Stack+"-video-cloud/pki-controller" || len(j.Completed) != 13 || len(j.Owners) != 14 {
		return managedCorePartialBlock("partial-core-journal")
	}
	container := mqttFoundationPrimary(plan.Resources[13].Desired)
	session := mqttFoundationEnv(container, "PKI_BOOTSTRAP_SESSION_ID")
	if !environmentBootstrapOwnerUUID.MatchString(session) {
		return managedCorePartialBlock("original-session")
	}
	ids := managedCorePartialResourceIDs(plan.Stack, session)
	for index, resource := range plan.Resources {
		phase, action := "configuration", "retain"
		if index >= 13 {
			phase = "workload"
		}
		if index == 4 || index == 13 || index == 14 {
			action = "update"
		}
		if resource.ID != ids[index] || deploymentplan.ID(resource.Desired) != resource.ID || resource.Phase != phase || resource.Action != action || resource.Baseline == nil || !environmentBootstrapOwnerUUID.MatchString(resource.UID) {
			return managedCorePartialBlock("core-resource-" + resource.ID)
		}
		if index < 13 && (j.Completed[index] != resource.ID || j.Owners[resource.ID] != resource.UID) || index == 13 && j.Owners[resource.ID] != resource.UID || index > 13 && j.Owners[resource.ID] != "" {
			return managedCorePartialBlock("core-progress-" + resource.ID)
		}
	}
	if mqttFoundationEnv(container, "PKI_FIXED_DEVICE_ROOT_TRUST") != "false" || mqttFoundationEnv(container, "PKI_DEV_FIXED_DEVICE_ROOT_TRUST") != "" || !environmentBootstrapOwnerUUID.MatchString(mqttFoundationEnv(container, "PKI_DEVICE_ROOT_ID")) || !environmentPreflightDigest.MatchString(mqttFoundationEnv(container, "PKI_DEVICE_ROOT_SHA256")) {
		return managedCorePartialBlock("original-controller-fixed-root")
	}
	return nil
}

// Only four actual application aliases may advance within the captured
// source. Original intent, start time, owner metadata and lease prefix stay pinned.
func managedCorePartialSourceMembership(source, current environmentCreateJournal, failed map[string]string, session, review string) bool {
	if len(failed) != 4 || len(current.Entries) != len(source.Entries)+7 || len(current.KubernetesLeases) != len(source.KubernetesLeases)+4 || !environmentBootstrapOwnerUUID.MatchString(session) || !environmentPreflightDigest.MatchString(review) {
		return false
	}
	prefix := current
	prefix.Entries = slices.Clone(current.Entries[:len(source.Entries)])
	prefix.KubernetesLeases = current.KubernetesLeases[:len(source.KubernetesLeases)]
	closed := 0
	for index, old := range source.Entries {
		if old.State == "pending" {
			expected, ok := failed[old.Key]
			entry := prefix.Entries[index]
			var receipt struct {
				Stage    string `json:"actual_qualified_forward_stage"`
				Original string `json:"original_failed_plan_sha256"`
				Session  string `json:"retained_session_id"`
			}
			if !ok || entry.State != "completed" || entry.CompletedAt.IsZero() || decodeEnvironmentJSON(entry.Receipt, &receipt) != nil || receipt.Stage != "kubernetes/managed-application-owners-forward-"+review || receipt.Original != expected || receipt.Session != session {
				return false
			}
			entry.State, entry.CompletedAt, entry.Receipt = old.State, old.CompletedAt, old.Receipt
			prefix.Entries[index] = entry
			closed++
		}
	}
	return closed == 4 && deploymentplan.Digest(prefix) == deploymentplan.Digest(source)
}

func loadManagedCorePartialInitialSource(store secretStore, e *environmentCreateExecution) (*environmentCreateExecution, createFoundationRecoveryArtifact, *createManagedCorePartialRecovery, error) {
	var empty createFoundationRecoveryArtifact
	bad := func(stage string) (*environmentCreateExecution, createFoundationRecoveryArtifact, *createManagedCorePartialRecovery, error) {
		return nil, empty, nil, managedCorePartialBlock(stage)
	}
	if e == nil || store.Root != e.Store.Root || e.Journal.Environment != "dev" || e.Journal.Stack != "video-cloud-dev" {
		return bad("selected-original-owner")
	}
	review := managedCorePartialInitialReview(e.Journal)
	if review == "" {
		return bad("checkpoint")
	}
	admitted, source, err := loadAdmittedCreateRecovery(store, e, review)
	if err != nil || admitted.Schema != "rtk-cloud.create-openbao-recovery-artifact/v1" || admitted.Plan.Schema != "rtk-cloud.create-managed-application-trust-openbao-ack-recovery/v1" || len(e.Journal.Entries) != len(source.Entries)+7 || len(e.Journal.KubernetesLeases) != len(source.KubernetesLeases)+4 {
		return bad("original-ack-admission")
	}
	historical := *e
	historical.Journal = source
	source330, _, latest, err := loadManagedApplicationInterruptedSource(store, &historical)
	if err != nil || latest.Previous == nil || latest.Previous.Previous == nil || latest.Previous.Previous.Previous != nil {
		return bad("bounded-original-history")
	}
	for remaining := 2; managedApplicationPartialReview(source330.Journal) == "" && remaining > 0; remaining-- {
		source330, _, _, err = loadManagedApplicationInterruptedSourceDepth(store, source330, remaining)
		if err != nil {
			return bad("original-application-history")
		}
	}
	_, _, original, err := loadManagedApplicationPartialSource(store, source330)
	if err != nil {
		return bad("original-application-plan")
	}
	original.Interrupted = latest
	apps, _, err := readCreateOpenBaoACKCompletedStage(store, e.Journal, "managed-application-owners-forward-"+review)
	var preview deploymentplan.Plan
	base := filepath.Join(store.Root, "deployment/create/openbao-recovery", review)
	if err != nil || readExecutionJSON(filepath.Join(base, "openbao-root-trust-plan.json"), &preview) != nil || preview.SHA256() != admitted.Plan.Materials["accepted-service-core-managed-application-forward-plan"] || validateManagedApplicationPartialCandidate(original, &preview) != nil {
		return bad("accepted-application-preview")
	}
	normalized := *apps
	normalized.BindingsSHA256 = preview.BindingsSHA256
	if normalized.SHA256() != preview.SHA256() {
		return bad("accepted-application-plan")
	}
	var appsJournal deploymentplan.Journal
	if readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", apps.SHA256(), "journal.json"), &appsJournal) != nil {
		return bad("accepted-application-journal")
	}
	for index, resource := range apps.Resources {
		if appsJournal.Completed[index] != resource.ID {
			return bad("accepted-application-order")
		}
	}
	failed := map[string]string{"kubernetes/managed-application-owners": original.Plan.SHA256()}
	for old := latest; old != nil; old = old.Previous {
		failed["kubernetes/managed-application-owners-forward-"+old.Review] = old.Plan.SHA256()
	}
	session := mqttFoundationEnv(mqttFoundationPrimary(apps.Resources[22].Desired), "PKI_BOOTSTRAP_SESSION_ID")
	if !environmentBootstrapOwnerUUID.MatchString(session) {
		return bad("accepted-application-session")
	}
	if !managedCorePartialSourceMembership(source, e.Journal, failed, session, review) {
		return bad("original-source-membership")
	}
	var owner environmentCreateOwner
	if readExecutionJSON(filepath.Join(base, "source-owner.json"), &owner) != nil || owner.Schema != environmentCreateOwnerSchema || owner.Environment != e.Journal.Environment || owner.Stack != e.Journal.Stack || owner.PlanSHA256 != e.Journal.PlanSHA256 || owner.OwnerToken != e.Journal.OwnerToken || deploymentplan.Digest(owner) != admitted.Plan.ParentOwnerSHA || e.owner.Schema != "" && deploymentplan.Digest(owner) != deploymentplan.Digest(e.owner) {
		return bad("original-owner")
	}
	var ack createOpenBaoACKRecovery
	if readExecutionJSON(filepath.Join(base, "openbao-host-ack-source.json"), &ack) != nil {
		return bad("original-ack-source")
	}
	defs, err := createOpenBaoACKDefinitions(&ack)
	if err != nil || ack.Candidate == nil || len(ack.Candidate.Resources) != 1 || ack.Candidate.Resources[0].Action != "update" || ack.Candidate.Resources[0].UID != ack.Original.UID || !deploymentplan.Matches(ack.Original.Desired, ack.Candidate.Resources[0].Baseline) || deploymentplan.Digest(defs[0].Object) != deploymentplan.Digest(ack.Candidate.Resources[0].Desired) {
		return bad("original-ack-candidate")
	}
	for key, value := range map[string]string{"selection": ack.SelectionSHA, "manifest": ack.ManifestSHA, "image": deploymentplan.Digest(ack.Image), "original-plan": ack.OriginalPlanSHA, "original-journal": ack.OriginalJournalSHA, "candidate-plan": ack.Candidate.SHA256(), "source-pod": deploymentplan.Digest(ack.SourcePodUID), "source-process": deploymentplan.Digest(ack.SourceProcess), "source": deploymentplan.Digest(ack), "original-release": deploymentplan.Digest(admitted.Plan.Plan.Release)} {
		if admitted.Plan.Materials["accepted-service-core-openbao-ack-"+key] != value {
			return bad("original-ack-material-" + key)
		}
	}
	var originalACKPlan deploymentplan.Plan
	var originalACKJournal deploymentplan.Journal
	if readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", ack.OriginalPlanSHA, "plan.json"), &originalACKPlan) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", ack.OriginalPlanSHA, "journal.json"), &originalACKJournal) != nil || originalACKPlan.SHA256() != ack.OriginalPlanSHA || deploymentplan.Digest(originalACKJournal) != ack.OriginalJournalSHA || len(originalACKPlan.Resources) != 7 || !createOpenBaoACKCompletedChild(&originalACKPlan, originalACKJournal) {
		return bad("original-accepted-ack-authority")
	}
	found := 0
	for _, resource := range originalACKPlan.Resources {
		if resource.ID == ack.Original.ID && deploymentplan.Digest(resource) == deploymentplan.Digest(ack.Original) && originalACKJournal.Owners[resource.ID] == ack.Original.UID {
			found++
		}
	}
	if found != 1 {
		return bad("original-ack-owner")
	}
	ackPlan, _, err := readCreateOpenBaoACKCompletedStage(store, e.Journal, "openbao-host-ack-forward-"+review)
	if err != nil {
		return bad("accepted-ack-child")
	}
	normalized = *ackPlan
	normalized.BindingsSHA256 = ack.Candidate.BindingsSHA256
	if normalized.SHA256() != ack.Candidate.SHA256() {
		return bad("accepted-ack-plan")
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "openbao/init.json"), true)
	share, shareErr := store.read("openbao/unseal-key")
	unseal := e.Journal.Entries[len(source.Entries)+2]
	uid := strings.TrimPrefix(unseal.Key, "openbao-managed-unseal-")
	var receipt struct {
		Pod         string `json:"pod_uid"`
		Revision    string `json:"revision"`
		Initialized bool   `json:"initialized"`
		Sealed      bool   `json:"sealed"`
	}
	if err != nil || shareErr != nil || decodeEnvironmentJSON(unseal.Receipt, &receipt) != nil || receipt.Pod != uid || receipt.Revision == "" || !receipt.Initialized || receipt.Sealed || unseal.IntentSHA256 != activeTransportJournalDigest(deploymentplan.Object{"pod_uid": uid, "controller_uid": ack.Original.UID, "revision": receipt.Revision, "original_init_sha256": environmentPreflightBytesSHA256(raw), "unseal_sha256": environmentPreflightBytesSHA256([]byte(share))}) {
		return bad("original-share-unseal")
	}
	routing, _, err := readCreateOpenBaoACKCompletedStage(store, e.Journal, "native-app-routing-profile")
	var routingJournal deploymentplan.Journal
	var binding environmentAppRoutingBinding
	routeEntry := e.Journal.Entries[len(source.Entries)+5]
	if err != nil || len(routing.Resources) != 1 || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", routing.SHA256(), "journal.json"), &routingJournal) != nil || readExecutionJSON(filepath.Join(store.Root, "pki/app/routing.json"), &binding) != nil || binding.Schema != "rtk-cloud.native-app-routing/v1" || binding.Environment != e.Journal.Environment || binding.Stack != e.Journal.Stack || binding.Mode != "internal-service" || binding.CreatePlanSHA256 != e.Journal.PlanSHA256 || binding.DeclarationUID != routingJournal.Owners["ConfigMap/"+e.Journal.Stack+"-video-cloud/"+environmentAppRoutingDeclaration] || binding.ServiceUID != appsJournal.Owners["Service/"+e.Journal.Stack+"-video-cloud/video-cloud-api-app-pki"] || binding.DeploymentUID != appsJournal.Owners["Deployment/"+e.Journal.Stack+"-video-cloud/video-cloud-api-app-pki"] || routeEntry.IntentSHA256 != activeTransportJournalDigest(binding) || activeTransportJournalDigest(jsonRawObject(routeEntry.Receipt)) != activeTransportJournalDigest(binding) || !deploymentplan.Matches(environmentAppRoutingDefinition(binding).Object, routing.Resources[0].Desired) {
		return bad("original-app-routing")
	}
	lease := e.Journal.KubernetesLeases[len(source.KubernetesLeases)+3]
	sha := strings.SplitN(lease.Holder, ":", 2)[0]
	var core deploymentplan.Plan
	var coreJournal deploymentplan.Journal
	if !environmentPreflightDigest.MatchString(sha) || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "plan.json"), &core) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "journal.json"), &coreJournal) != nil || core.SHA256() != sha || validateManagedCorePartialJournal(&core, coreJournal) != nil {
		return bad("partial-core-child")
	}
	plans := []*deploymentplan.Plan{ackPlan, apps, routing, &core}
	for index, plan := range plans {
		owner := e.Journal.KubernetesLeases[len(source.KubernetesLeases)+index]
		if owner.ResourceID != "Lease/"+e.Journal.Stack+"-video-cloud/rtk-deployment-execution" || !environmentBootstrapOwnerUUID.MatchString(owner.UID) || !strings.HasPrefix(owner.Holder, plan.SHA256()+":") || strings.TrimPrefix(owner.Holder, plan.SHA256()+":") == "" {
			return bad("exact-child-lease")
		}
		if plan.Environment != e.Journal.Environment || plan.Stack != e.Journal.Stack || plan.ConfigurationSHA256 != core.ConfigurationSHA256 || plan.ReleaseSHA256 != core.ReleaseSHA256 {
			return bad("exact-child-inputs")
		}
	}
	intent, err := environmentCreateKubernetesStageIntent(&core)
	if err != nil || e.Journal.Entries[len(e.Journal.Entries)-1].IntentSHA256 != activeTransportJournalDigest(jsonRawObject(intent)) || mqttFoundationEnv(mqttFoundationPrimary(core.Resources[13].Desired), "PKI_BOOTSTRAP_SESSION_ID") != session {
		return bad("partial-core-intent")
	}
	partial := &createManagedCorePartialRecovery{Action: managedCoreRestoreOnly, OriginalRepairReview: review, SourceSHA: deploymentplan.Digest(e.Journal), ArtifactSHA: deploymentplan.Digest(admitted), Plan: &core, Journal: coreJournal, Apps: apps, AppsJournal: appsJournal, Routing: routing, RoutingJournal: routingJournal}
	return &historical, admitted, partial, nil
}

func managedCorePartialNativeCheckpoint(store secretStore, e *environmentCreateExecution) bool {
	if e == nil {
		return false
	}
	if managedCorePartialReview(e.Journal) != "" {
		_, _, _, err := loadManagedCorePartialSource(store, e)
		return err == nil
	}
	// After the separately admitted forward, native completion must bind that
	// exact accepted child and its only closed historical parent alias.
	if len(e.Journal.Entries) < 2 {
		return false
	}
	entry := e.Journal.Entries[len(e.Journal.Entries)-1]
	prefix := "kubernetes/managed-complete-issuer-forward-"
	review := strings.TrimPrefix(entry.Key, prefix)
	if !strings.HasPrefix(entry.Key, prefix) || !environmentPreflightDigest.MatchString(review) || entry.State != "completed" {
		return false
	}
	admitted, source, err := loadAdmittedCreateRecovery(store, e, review)
	if err != nil || admitted.Plan.Schema != managedCorePartialSchema || len(e.Journal.Entries) != len(source.Entries)+2 || len(e.Journal.KubernetesLeases) != len(source.KubernetesLeases)+1 {
		return false
	}
	var admittedOwner environmentCreateOwner
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create/openbao-recovery", review, "source-owner.json"), &admittedOwner) != nil || admittedOwner.Schema != environmentCreateOwnerSchema || admittedOwner.OwnerToken != source.OwnerToken || admittedOwner.PlanSHA256 != source.PlanSHA256 || deploymentplan.Digest(admittedOwner) != admitted.Plan.ParentOwnerSHA || e.owner.Schema != "" && deploymentplan.Digest(e.owner) != deploymentplan.Digest(admittedOwner) {
		return false
	}
	admission := e.Journal.Entries[len(source.Entries)]
	if admission.Key != "openbao-recovery-admission-"+review || admission.Phase != "recovery" || admission.State != "completed" || admission.CompletedAt.IsZero() {
		return false
	}
	historical := *e
	historical.Journal = source
	_, _, partial, err := loadManagedCorePartialSource(store, &historical)
	if err != nil || admitted.Plan.Materials["accepted-service-core-complete-issuer-partial-original-review"] != partial.OriginalRepairReview || admitted.Plan.ParentJournalSHA != partial.SourceSHA || admitted.Plan.Materials["accepted-service-core-complete-issuer-partial-source-journal"] != deploymentplan.Digest(partial.Journal) || admitted.Plan.Materials["accepted-service-core-complete-issuer-partial-source-plan"] != partial.Plan.SHA256() || admitted.Plan.Materials["accepted-service-core-complete-issuer-partial-source"] != deploymentplan.Digest(partial) || admitted.Plan.Materials["accepted-service-core-complete-issuer-partial-action"] != deploymentplan.Digest(managedCoreRestoreOnly) {
		return false
	}
	if partial.PriorAdmission != nil && admitted.Plan.Materials["accepted-service-core-complete-issuer-partial-prior-admission"] != deploymentplan.Digest(partial.PriorAdmission) {
		return false
	}
	if partial.PriorForward != nil && admitted.Plan.Materials["accepted-service-core-complete-issuer-partial-prior-forward"] != deploymentplan.Digest(partial.PriorForward) {
		return false
	}
	current := e.Journal
	current.Entries = slices.Clone(e.Journal.Entries[:len(source.Entries)])
	current.KubernetesLeases = e.Journal.KubernetesLeases[:len(source.KubernetesLeases)]
	plan, _, err := readCreateOpenBaoACKCompletedStage(store, e.Journal, strings.TrimPrefix(entry.Key, "kubernetes/"))
	if err != nil || !managedCorePartialAliasPrefixMatches(source, current, partial, entry.Key, plan.SHA256()) {
		return false
	}
	if partial.PriorForward != nil {
		partial.CertIssuerPrepare, err = loadManagedCoreQualifiedCertIssuerPrepare(store, review, admitted.Plan.Materials)
		if err != nil {
			return false
		}
	}
	if validateManagedCorePartialCandidate(partial, plan) != nil {
		return false
	}
	defs, err := managedCorePartialDefinitions(partial)
	if err != nil || plan.BindingsSHA256 != environmentCreateStageBindingsSHA(strings.TrimPrefix(entry.Key, "kubernetes/"), defs) {
		return false
	}
	var preview deploymentplan.Plan
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create/openbao-recovery", review, managedCorePartialForwardPlanFilename), &preview) != nil || preview.SHA256() != admitted.Plan.Materials["accepted-service-core-complete-issuer-partial-forward-plan"] || validateManagedCorePartialCandidate(partial, &preview) != nil {
		return false
	}
	normalized := *plan
	normalized.BindingsSHA256 = preview.BindingsSHA256
	lease := e.Journal.KubernetesLeases[len(source.KubernetesLeases)]
	return normalized.SHA256() == preview.SHA256() && lease.ResourceID == "Lease/"+source.Stack+"-video-cloud/rtk-deployment-execution" && environmentBootstrapOwnerUUID.MatchString(lease.UID) && strings.HasPrefix(lease.Holder, plan.SHA256()+":") && strings.TrimPrefix(lease.Holder, plan.SHA256()+":") != ""
}

func managedCoreInitialDefinitions(partial *createManagedCorePartialRecovery) ([]deploymentplan.Definition, error) {
	if partial == nil || partial.Action != managedCoreRestoreOnly || validateManagedCorePartialJournal(partial.Plan, partial.Journal) != nil {
		return nil, managedCorePartialBlock("qualified-partial-definitions")
	}
	defs := make([]deploymentplan.Definition, 0, 16)
	for index, resource := range partial.Plan.Resources {
		object := deploymentplan.Clone(resource.Desired)
		if index == 13 {
			container := mqttFoundationPrimary(object)
			fields, err := environmentPKINamedFields(container["env"])
			field := fields["PKI_FIXED_DEVICE_ROOT_TRUST"]
			if err != nil || field == nil || field["value"] != "false" || field["valueFrom"] != nil {
				return nil, managedCorePartialBlock("one-controller-fixed-root-field")
			}
			field["value"] = "true"
		}
		defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: object, Dependencies: slices.Clone(resource.Dependencies)})
	}
	return defs, nil
}

func validateManagedCoreInitialCandidate(partial *createManagedCorePartialRecovery, candidate *deploymentplan.Plan) error {
	defs, err := managedCoreInitialDefinitions(partial)
	if err != nil || candidate == nil || len(candidate.Resources) != 16 || candidate.Version != partial.Plan.Version || candidate.Environment != partial.Plan.Environment || candidate.Stack != partial.Plan.Stack || candidate.Operation != "scoped" || candidate.ConfigurationSHA256 != partial.Plan.ConfigurationSHA256 || candidate.ReleaseSHA256 != partial.Plan.ReleaseSHA256 {
		return managedCorePartialBlock("candidate-inputs")
	}
	for index, resource := range candidate.Resources {
		original := partial.Plan.Resources[index]
		expected, owner := original.Desired, partial.Journal.Owners[original.ID]
		if index > 13 {
			expected, owner = original.Baseline, original.UID
		}
		action := "retain"
		if index == 13 || index == 14 {
			action = "update"
		}
		if resource.ID != original.ID || resource.Phase != original.Phase || resource.Action != action || resource.UID != owner || resource.Baseline == nil || !deploymentplan.Matches(expected, resource.Baseline) || deploymentplan.Digest(resource.Dependencies) != deploymentplan.Digest(original.Dependencies) || deploymentplan.Digest(defs[index].Object) != deploymentplan.Digest(resource.Desired) {
			return managedCorePartialBlock("candidate-scope-" + resource.ID)
		}
	}
	return nil
}

func managedCorePartialDefinitions(partial *createManagedCorePartialRecovery) ([]deploymentplan.Definition, error) {
	if partial == nil || partial.PriorForward == nil {
		return managedCoreInitialDefinitions(partial)
	}
	current, _, err := managedCorePartialCurrentPlan(partial)
	if err != nil || validateManagedCoreCertIssuerPrepare(partial.CertIssuerPrepare) != nil || partial.Action != managedCoreRestoreOnly {
		return nil, managedCorePartialBlock("qualified-certissuer-prepare-image")
	}
	defs := make([]deploymentplan.Definition, 0, len(current.Resources))
	for index, resource := range current.Resources {
		object := deploymentplan.Clone(resource.Desired)
		if index == 14 {
			container := mqttFoundationPrimary(object)
			if container["name"] != "certissuer" || container["image"] != mqttFoundationPrimary(partial.Plan.Resources[14].Desired)["image"] || container["image"] == partial.CertIssuerPrepare.Image.Reference {
				return nil, managedCorePartialBlock("original-certissuer-only-image")
			}
			container["image"] = partial.CertIssuerPrepare.Image.Reference
		}
		defs = append(defs, deploymentplan.Definition{Phase: resource.Phase, Object: object, Dependencies: slices.Clone(resource.Dependencies)})
	}
	return defs, nil
}

func validateManagedCorePartialCandidate(partial *createManagedCorePartialRecovery, candidate *deploymentplan.Plan) error {
	if partial == nil || partial.PriorForward == nil {
		return validateManagedCoreInitialCandidate(partial, candidate)
	}
	current, journal, err := managedCorePartialCurrentPlan(partial)
	defs, defErr := managedCorePartialDefinitions(partial)
	if err != nil || defErr != nil || candidate == nil || len(candidate.Resources) != 16 || candidate.Version != current.Version || candidate.Environment != current.Environment || candidate.Stack != current.Stack || candidate.Operation != "scoped" || candidate.ConfigurationSHA256 != current.ConfigurationSHA256 || candidate.ReleaseSHA256 != current.ReleaseSHA256 {
		return managedCorePartialBlock("certissuer-prepare-candidate-inputs")
	}
	for index, resource := range candidate.Resources {
		original := current.Resources[index]
		expected, owner := original.Desired, journal.Owners[original.ID]
		if index == 15 {
			expected, owner = original.Baseline, original.UID
		}
		action := "retain"
		if index == 14 {
			action = "update"
		}
		if resource.ID != original.ID || resource.Phase != original.Phase || resource.Action != action || resource.UID != owner || resource.Baseline == nil || !deploymentplan.Matches(expected, resource.Baseline) || deploymentplan.Digest(resource.Dependencies) != deploymentplan.Digest(original.Dependencies) || deploymentplan.Digest(defs[index].Object) != deploymentplan.Digest(resource.Desired) {
			return managedCorePartialBlock("certissuer-prepare-candidate-scope-" + resource.ID)
		}
	}
	return nil
}

func loadManagedCorePartialServiceCore(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan) (*createServiceSessionRecovery, error) {
	if ctx == nil || p == nil || p.Materials == nil {
		return nil, managedCorePartialBlock("selected-recovery-context")
	}
	historical, admitted, partial, err := loadManagedCorePartialSource(store, e)
	if err != nil {
		return nil, err
	}
	source330, _, latest, err := loadManagedApplicationInterruptedSource(store, historical)
	if err != nil {
		return nil, err
	}
	for remaining := 2; managedApplicationPartialReview(source330.Journal) == "" && remaining > 0; remaining-- {
		source330, _, _, err = loadManagedApplicationInterruptedSourceDepth(store, source330, remaining)
		if err != nil {
			return nil, err
		}
	}
	r, err := deriveManagedApplicationPartialRecovery(cfg, store, source330, p)
	if err != nil {
		return nil, err
	}
	r.ManagedApplicationPartial.Interrupted = latest
	r.ManagedCorePartial = partial
	r.Review = partial.OriginalRepairReview
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create/openbao-recovery", partial.OriginalRepairReview, "openbao-host-ack-source.json"), &r.OpenBaoACK) != nil || r.OpenBaoACK == nil || r.OpenBaoACK.OriginalPlanSHA != r.ManagedApplicationPartial.Historical.PostManaged.Plan.SHA256() || r.OpenBaoACK.OriginalJournalSHA != deploymentplan.Digest(r.ManagedApplicationPartial.Historical.PostManaged.Journal) {
		return nil, managedCorePartialBlock("original-retained-ack-host")
	}
	r.CoreAccepted, r.PostServiceResume, r.PostServiceRenewal, r.ApplicationForward = true, true, false, false
	r.GrantCandidate, r.RenewalCandidate, r.InstallationCandidate, r.QuiesceCandidate = nil, nil, nil, nil
	for key, value := range admitted.Plan.Materials {
		if strings.HasPrefix(key, "accepted-service-core-managed-application-") || strings.HasPrefix(key, "accepted-service-core-openbao-ack-") {
			p.Materials[key] = value
		}
	}
	p.Materials["accepted-service-core-complete-issuer-partial-source-journal"] = deploymentplan.Digest(partial.Journal)
	p.Materials["accepted-service-core-complete-issuer-partial-original-review"] = partial.OriginalRepairReview
	p.Materials["accepted-service-core-complete-issuer-partial-source"] = deploymentplan.Digest(partial)
	p.Materials["accepted-service-core-complete-issuer-partial-action"] = deploymentplan.Digest(partial.Action)
	p.Materials["accepted-service-core-complete-issuer-partial-original-admission"] = partial.ArtifactSHA
	p.Materials["accepted-service-core-complete-issuer-partial-source-plan"] = partial.Plan.SHA256()
	p.Materials["accepted-service-core-complete-issuer-partial-child-journal"] = deploymentplan.Digest(partial.Journal)
	if partial.PriorAdmission != nil {
		p.Materials["accepted-service-core-complete-issuer-partial-prior-admission"] = deploymentplan.Digest(partial.PriorAdmission)
	}
	if partial.PriorForward != nil {
		p.Materials["accepted-service-core-complete-issuer-partial-prior-forward"] = deploymentplan.Digest(partial.PriorForward)
	}
	if partial.PriorForward != nil {
		proof, err := loadCreateCertIssuerPrepareRecovery(ctx, cfg, store, e.Journal.PlanSHA256)
		if err != nil {
			return nil, err
		}
		partial.CertIssuerPrepare = proof
		addManagedCoreCertIssuerPrepareMaterials(p.Materials, proof)
	}
	if err := verifyManagedCoreObservationWithAction(ctx, cfg, store, e, b, r, p, partial.Action); err != nil {
		return nil, err
	}
	defs, err := managedCorePartialDefinitions(partial)
	if err != nil {
		return nil, err
	}
	live, err := b.Inventory(ctx, cfg.Values["CLOUD_STACK_NAME"])
	if err != nil {
		return nil, err
	}
	r.Candidate, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("managed-complete-issuer-forward", defs)}, defs, live)
	if err != nil {
		return nil, err
	}
	if err := validateManagedCorePartialCandidate(partial, r.Candidate); err != nil {
		return nil, err
	}
	p.Materials["accepted-service-core-complete-issuer-partial-forward-plan"] = r.Candidate.SHA256()
	return r, e.Check(ctx)
}
