package main

// PRIVATE DRAFT. Source-only authority capture and exact future lifecycle plans.

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func managedLateCompile(p environmentCreatePlan, stage string, definitions []deploymentplan.Definition, live []deploymentplan.Object) (*deploymentplan.Plan, error) {
	return deploymentplan.Compile(deploymentplan.Inputs{Environment: p.Environment, Stack: p.Stack, Operation: "scoped", ConfigurationSHA256: p.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Release), BindingsSHA256: environmentCreateStageBindingsSHA(stage, definitions)}, definitions, live)
}

func managedLateRecordedOwners(plan *deploymentplan.Plan, journal deploymentplan.Journal, into map[string]deploymentplan.Resource) error {
	if !createOpenBaoACKCompletedChild(plan, journal) || !journal.AcceptanceStarted {
		return environmentCreateBootstrapBlocker("late owner capture requires a genuine accepted original child")
	}
	for index, resource := range plan.Resources {
		uid := journal.Owners[resource.ID]
		if journal.Completed[index] != resource.ID || !environmentBootstrapOwnerUUID.MatchString(uid) || resource.UID != "" && resource.UID != uid {
			return environmentCreateBootstrapBlocker("late accepted owner receipt changed: " + resource.ID)
		}
		if previous, ok := into[resource.ID]; ok && (previous.UID != uid || !deploymentplan.Matches(previous.Desired, resource.Baseline)) {
			return environmentCreateBootstrapBlocker("late exact accepted owner chain changed: " + resource.ID)
		}
		resource.UID = uid
		into[resource.ID] = resource
	}
	return nil
}

// Historical partial owners are genuine intent/UID write receipts, never
// accepted stages. Only the separately validated two bounded Core children may
// supersede their own baseline before the final accepted child is captured.
func managedLateRecordedPartialOwners(plan *deploymentplan.Plan, journal deploymentplan.Journal, into map[string]deploymentplan.Resource) error {
	return managedLateRecordedWrittenOwners(plan, journal, into, false)
}

func managedLateRecordedWrittenOwners(plan *deploymentplan.Plan, journal deploymentplan.Journal, into map[string]deploymentplan.Resource, createdAllocations bool) error {
	if plan == nil || journal.PlanSHA256 != plan.SHA256() {
		return environmentCreateBootstrapBlocker("late partial owner plan differs")
	}
	for _, resource := range plan.Resources {
		uid, ok := journal.Owners[resource.ID]
		if !ok {
			continue
		}
		created := createdAllocations && resource.Action == "create" && resource.UID == "" && resource.Baseline == nil
		if !environmentBootstrapOwnerUUID.MatchString(uid) || resource.UID != uid && !created {
			return environmentCreateBootstrapBlocker("late partial owner UID differs: " + resource.ID)
		}
		if previous, ok := into[resource.ID]; ok && (previous.UID != uid || !deploymentplan.Matches(previous.Desired, resource.Baseline)) {
			return environmentCreateBootstrapBlocker("late partial owner chain differs: " + resource.ID)
		}
		resource.UID = uid
		into[resource.ID] = resource
	}
	return nil
}

// The typed source constructor has authenticated these exact original
// transport generations. Capture their real writes before later retain actions:
// the six completed members of the seven-owner failure installed native policy.
func managedLateRecordedTransportHistory(partial *createManagedTransportPartialRecovery, into map[string]deploymentplan.Resource) error {
	bad := func() error {
		return environmentCreateBootstrapBlocker("late transport history requires its exact original three/five/seven-owner lineage")
	}
	if partial == nil || partial.Original == nil || len(partial.Original.Resources) != 3 || partial.Journal.PlanSHA256 != partial.Original.SHA256() || partial.Journal.Accepted || partial.Journal.AcceptanceStarted || partial.Journal.InFlight != "" || len(partial.Journal.Completed) != 1 || len(partial.Journal.Owners) != 2 || partial.PriorForward == nil || partial.PriorForward.Previous == nil || partial.PriorForward.Previous.Previous != nil || partial.AcceptedForward == nil || partial.PostManaged == nil {
		return bad()
	}
	stack := partial.Original.Stack
	cm, mqtt, bao := "ConfigMap/"+stack+"-secrets/openbao-file", "Deployment/"+stack+"-video-cloud/mqtt-pki", "StatefulSet/"+stack+"-secrets/openbao"
	if partial.Journal.Failed != mqtt || partial.Journal.Completed[0] != cm {
		return bad()
	}
	seen := map[string]bool{}
	for _, resource := range partial.Original.Resources {
		if seen[resource.ID] || resource.ID != cm && resource.ID != mqtt && resource.ID != bao || resource.Action != "update" || resource.Baseline == nil || !environmentBootstrapOwnerUUID.MatchString(resource.UID) || resource.ID == bao && partial.Journal.Owners[resource.ID] != "" || resource.ID != bao && partial.Journal.Owners[resource.ID] != resource.UID {
			return bad()
		}
		seen[resource.ID] = true
	}
	if err := managedLateRecordedPartialOwners(partial.Original, partial.Journal, into); err != nil {
		return err
	}
	first, second := partial.PriorForward.Previous, partial.PriorForward
	if validateManagedTransportInterruptedForward(partial.Original, first.Plan, first.Journal) != nil || validateManagedTransportWrittenOpenBaoJournal(second.Plan, second.Journal) != nil {
		return bad()
	}
	for _, forward := range []*createManagedTransportInterruptedForward{first, second} {
		if !environmentPreflightDigest.MatchString(forward.Review) || !environmentPreflightDigest.MatchString(forward.SourceSHA) || !environmentPreflightDigest.MatchString(forward.ArtifactSHA) {
			return bad()
		}
		if err := managedLateRecordedWrittenOwners(forward.Plan, forward.Journal, into, true); err != nil {
			return err
		}
	}
	accepted, post := partial.AcceptedForward, partial.PostManaged
	for _, pins := range [][]string{{accepted.Review, accepted.SourceSHA, accepted.ArtifactSHA}, {post.Review, post.SourceSHA, post.ArtifactSHA}} {
		for _, pin := range pins {
			if !environmentPreflightDigest.MatchString(pin) {
				return bad()
			}
		}
	}
	if validateManagedTransportAcceptedForwardJournal(accepted.Plan, accepted.Journal) != nil || validateManagedTransportPostManagedJournal(post.Plan, post.Journal) != nil {
		return bad()
	}
	if err := managedLateRecordedOwners(accepted.Plan, accepted.Journal, into); err != nil {
		return err
	}
	return managedLateRecordedOwners(post.Plan, post.Journal, into)
}

// Preserve the genuine written application generations before the accepted
// final child. The original MQTT startup-to-runtime change belongs to its
// completed prefix; later retain actions cannot replace that missing authority.
func managedLateRecordedApplicationHistory(partial *createManagedApplicationPartialRecovery, into map[string]deploymentplan.Resource) error {
	if _, _, err := managedApplicationPartialCurrentPlan(partial); err != nil {
		return err
	}
	if err := managedLateRecordedWrittenOwners(partial.Plan, partial.Journal, into, true); err != nil {
		return err
	}
	var forwards []*createManagedApplicationInterruptedForward
	for forward := partial.Interrupted; forward != nil; forward = forward.Previous {
		if len(forwards) == 3 {
			return environmentCreateBootstrapBlocker("late application history exceeds its three original forward generations")
		}
		forwards = append(forwards, forward)
	}
	for index := len(forwards) - 1; index >= 0; index-- {
		forward := forwards[index]
		if err := managedLateRecordedWrittenOwners(forward.Plan, forward.Journal, into, true); err != nil {
			return err
		}
	}
	return nil
}

// The ordered authorities are literal original stages. Aliases are resolved by
// their exact original receipt, never by searching another generation.
// Load the exact ordered historical owner authorities without observing or
// recapturing a native baseline. Both qualification and pre-admission reuse
// authenticate descriptor owners against this same immutable chain.
func managedLateSessionRecordedSourceOwners(store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery, late *createManagedLateSessionRecovery) (map[string]deploymentplan.Resource, error) {
	bad := func(stage string) error {
		return environmentCreateBootstrapBlocker("late original recorded owner authority required: " + stage)
	}
	if e == nil || r == nil || late == nil || r.ManagedApplicationPartial == nil || r.ManagedApplicationPartial.Historical == nil || r.ManagedTransportRecovery == nil || r.ManagedTransportRecovery.Partial == nil || r.ManagedCorePartial == nil || r.OpenBaoACK == nil {
		return nil, bad("source")
	}
	all := map[string]deploymentplan.Resource{}
	base := r.ManagedTransportRecovery.Partial
	var databaseStage string
	for _, entry := range e.Journal.Entries {
		if entry.Key != "kubernetes/database-runtime-secrets" {
			continue
		}
		var receipt map[string]string
		if entry.State != "completed" || json.Unmarshal(entry.Receipt, &receipt) != nil || receipt["actual_retained_verification_stage"] == "" {
			return nil, bad("database-alias")
		}
		databaseStage = receipt["actual_retained_verification_stage"]
	}
	if databaseStage == "" {
		return nil, bad("database-owner")
	}
	for _, key := range []string{databaseStage, "kubernetes/service-credential-secrets-renewal-" + base.SourceReview, "kubernetes/service-custody-reader-renewal-" + base.SourceReview, "kubernetes/transport-enrollment-issuer"} {
		plan, journal, err := managedTransportPostAcceptedStage(store, e, key)
		if err != nil || managedLateRecordedOwners(plan, journal, all) != nil {
			return nil, bad(key)
		}
	}
	if err := managedLateRecordedTransportHistory(r.ManagedApplicationPartial.Historical, all); err != nil {
		return nil, err
	}
	ack, ackJournal, err := managedTransportPostAcceptedStage(store, e, "kubernetes/openbao-host-ack-forward-"+late.OriginalRepairReview)
	if err != nil || ack == nil || r.OpenBaoACK.Candidate == nil || !managedCoreSameQualifiedPlan(ack, r.OpenBaoACK.Candidate) || managedLateRecordedOwners(ack, ackJournal, all) != nil {
		return nil, bad("original-formal-ack-host")
	}
	if err := managedLateRecordedApplicationHistory(r.ManagedApplicationPartial, all); err != nil {
		return nil, err
	}
	if err := managedLateRecordedOwners(r.ManagedCorePartial.Apps, r.ManagedCorePartial.AppsJournal, all); err != nil {
		return nil, err
	}
	if err := validateManagedCorePartialJournal(r.ManagedCorePartial.Plan, r.ManagedCorePartial.Journal); err != nil {
		return nil, err
	}
	if err := managedLateRecordedPartialOwners(r.ManagedCorePartial.Plan, r.ManagedCorePartial.Journal, all); err != nil {
		return nil, err
	}
	if prior := r.ManagedCorePartial.PriorForward; prior != nil {
		if validateManagedCoreInterruptedJournal(r.ManagedCorePartial) != nil || managedLateRecordedPartialOwners(prior.Plan, prior.Journal, all) != nil {
			return nil, bad("exact-interrupted-written-core")
		}
	}
	if managedLateRecordedOwners(late.AcceptedCore, late.AcceptedCoreJournal, all) != nil {
		return nil, bad("accepted-current-core")
	}
	if f := late.ExpiredRenewal; f != nil {
		for id, owner := range f.PriorOwners {
			if previous, ok := all[id]; ok && previous.UID != owner.UID {
				return nil, bad("prior-owner-uid")
			}
			all[id] = owner
		}
	}
	return all, nil
}

func prepareManagedLateSessionOwners(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery, late *createManagedLateSessionRecovery) error {
	bad := func(stage string) error {
		return environmentCreateBootstrapBlocker("late owner qualification requires exact original accepted recipe: " + stage)
	}
	if r == nil || late == nil || r.ManagedApplicationPartial == nil || r.ManagedApplicationPartial.Historical == nil || r.ManagedTransportRecovery == nil || r.ManagedTransportRecovery.Partial == nil || r.ManagedCorePartial == nil || r.OpenBaoACK == nil || late.NewSessionID == "" {
		return bad("source")
	}
	all, err := managedLateSessionRecordedSourceOwners(store, e, r, late)
	if err != nil {
		return err
	}
	late.TrustOwners, late.VerifierOwners, late.Owners = map[string]deploymentplan.Resource{}, map[string]deploymentplan.Resource{}, map[string]deploymentplan.Resource{}
	for _, namespace := range late.SourceCeremony.BootstrapCredentialNamespaces {
		for name, destination := range map[string]map[string]deploymentplan.Resource{"pki-create-service-trust": late.TrustOwners, "pki-verifier-database": late.VerifierOwners} {
			kind := "ConfigMap"
			if name == "pki-verifier-database" {
				kind = "Secret"
			}
			id := kind + "/" + namespace + "/" + name
			owner, ok := all[id]
			if !ok {
				return bad(id)
			}
			destination[id] = owner
		}
	}
	late.Custody = all["Deployment/"+late.SourceCeremony.BootstrapCredentialNamespace+"/"+late.SourceCeremony.CustodyReader]
	if late.Custody.ID == "" {
		return bad("original-custody")
	}
	for _, id := range managedLateNativeOwnerIDs(e.Journal.Stack) {
		owner, ok := all[id]
		if !ok {
			return bad(id)
		}
		actual, err := b.Read(ctx, id)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != owner.UID || !deploymentplan.Matches(owner.Desired, actual) {
			return bad(id)
		}
		late.Owners[id] = owner
	}
	if err := verifyManagedLateSessionRetainedDependencies(ctx, b, late); err != nil {
		return err
	}
	service := late.SourceCeremony
	service.BootstrapSessionID = late.NewSessionID
	definitions, err := managedLateSessionOwnerDefinitions(late, service)
	if err != nil {
		return err
	}
	if late.ExpiredRenewal != nil {
		for id, source := range all {
			if strings.HasPrefix(id, "Role/") {
				all[id] = managedLateExpiredOwnerDependency(late, source)
			}
		}
	}
	// Preserve genuine DAG edges. Explicitly retained configuration/storage
	// ancestors belong to the reviewed closure; no dependency is silently cut.
	definitions, err = managedLateDefinitionClosure(definitions, all)
	if err != nil {
		return err
	}
	late.DependencyOwners = map[string]deploymentplan.Resource{}
	for _, definition := range definitions {
		id := deploymentplan.ID(definition.Object)
		if _, native := late.Owners[id]; !native {
			late.DependencyOwners[id] = all[id]
		}
	}
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return err
	}
	ownerLive := live
	if late.ExpiredRenewal != nil {
		ownerLive = append([]deploymentplan.Object(nil), live...)
		for index, object := range ownerLive {
			id := deploymentplan.ID(object)
			if source, ok := late.DependencyOwners[id]; ok && id == "Role/"+p.Plan.Stack+"-video-cloud/bootstrap-cleanup" {
				clone := deploymentplan.Clone(object)
				clone["rules"] = deploymentplan.Clone(source.Desired)["rules"]
				ownerLive[index] = clone
			}
		}
	}
	late.OwnerCandidate, err = managedLateCompile(p.Plan, "managed-late-session-owners", definitions, ownerLive)
	if err != nil || validateManagedLateSessionOwnerCandidate(late, late.OwnerCandidate) != nil {
		return bad("sid-only-owner-candidate")
	}
	reader := deploymentplan.Clone(late.Custody.Desired)
	container := managedNamedField(environmentPKIPod(reader)["containers"], "custody")
	if container == nil {
		return bad("custody-container")
	}
	container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": late.NewSessionID, "PKI_BOOTSTRAP_CERT_PATH": late.NewCertificatePath}))
	late.CustodyCandidate, err = managedLateCompile(p.Plan, "service-custody-reader-renewal", []deploymentplan.Definition{{Phase: "bootstrap", Object: reader}}, live)
	if err != nil || len(late.CustodyCandidate.Resources) != 1 || late.CustodyCandidate.Resources[0].UID != late.Custody.UID || late.CustodyCandidate.Resources[0].Action != "update" {
		return bad("original-custody-candidate")
	}
	p.Materials["accepted-service-core-late-session-owners-plan"] = late.OwnerCandidate.SHA256()
	p.Materials["accepted-service-core-late-session-custody-plan"] = late.CustodyCandidate.SHA256()
	p.Materials["accepted-service-core-late-session-retained-trust"] = deploymentplan.Digest(late.TrustOwners)
	p.Materials["accepted-service-core-late-session-retained-verifiers"] = deploymentplan.Digest(late.VerifierOwners)
	return e.Check(ctx)
}

func managedLateNativeOwnerIDs(stack string) []string {
	ns := stack + "-video-cloud"
	return []string{"Deployment/" + stack + "-account-manager/account-manager", "Deployment/" + ns + "/pki-controller", "Deployment/" + ns + "/certissuer", "Deployment/" + ns + "/factoryenroll", "Deployment/" + ns + "/video-cloud-api", "Deployment/" + ns + "/video-cloud-api-pki", "Deployment/" + ns + "/video-cloud-api-app-pki", "Deployment/" + ns + "/video-cloud-logingester", "Deployment/" + ns + "/mqtt-pki", "StatefulSet/" + stack + "-secrets/openbao"}
}

func managedLateDefinitionClosure(definitions []deploymentplan.Definition, originals map[string]deploymentplan.Resource) ([]deploymentplan.Definition, error) {
	seen := map[string]bool{}
	for _, definition := range definitions {
		seen[deploymentplan.ID(definition.Object)] = true
	}
	for cursor := 0; cursor < len(definitions); cursor++ {
		for _, id := range definitions[cursor].Dependencies {
			if seen[id] {
				continue
			}
			owner, ok := originals[id]
			if !ok || !environmentBootstrapOwnerUUID.MatchString(owner.UID) {
				return nil, environmentCreateBootstrapBlocker("late dependency lacks exact accepted original owner: " + id)
			}
			seen[id] = true
			definitions = append(definitions, deploymentplan.Definition{Phase: owner.Phase, Object: deploymentplan.Clone(owner.Desired), Dependencies: slices.Clone(owner.Dependencies), Retain: true})
		}
	}
	return definitions, nil
}

func validateManagedLateSessionOwnerCandidate(late *createManagedLateSessionRecovery, candidate *deploymentplan.Plan) error {
	bad := func(id string) error {
		return environmentCreateBootstrapBlocker("late owner changed beyond exact SID/cleanup/DAG: " + id)
	}
	if late == nil || candidate == nil || late.AcceptedCore == nil || len(late.Owners) != 10 || candidate.Stack != late.AcceptedCore.Stack {
		return bad("ten-native-owner-scope")
	}
	service := late.SourceCeremony
	service.BootstrapSessionID = late.NewSessionID
	defs, err := managedLateSessionOwnerDefinitions(late, service)
	if err != nil {
		return err
	}
	defs, err = managedLateDefinitionClosure(defs, late.DependencyOwners)
	if err != nil {
		return err
	}
	wanted := map[string]deploymentplan.Definition{}
	for _, d := range defs {
		wanted[deploymentplan.ID(d.Object)] = d
	}
	if len(candidate.Resources) != len(wanted) {
		return bad("extra-or-missing-resource")
	}
	seen := map[string]bool{}
	for _, resource := range candidate.Resources {
		d, ok := wanted[resource.ID]
		source, native := late.Owners[resource.ID]
		if !native {
			source = late.DependencyOwners[resource.ID]
		}
		action := "update"
		if d.Retain {
			action = "retain"
		}
		if !ok || seen[resource.ID] || source.ID != resource.ID || resource.Phase != d.Phase || resource.Action != action || resource.UID != source.UID || !environmentBootstrapOwnerUUID.MatchString(resource.UID) || !deploymentplan.Matches(source.Desired, resource.Baseline) || deploymentplan.Digest(resource.Desired) != deploymentplan.Digest(d.Object) || !slices.Equal(resource.Dependencies, d.Dependencies) {
			return bad(resource.ID)
		}
		seen[resource.ID] = true
	}
	return nil
}

func managedLateRoleIntent(cfg deploymentConfig, name string, accounts, policies []string) deploymentplan.Object {
	return deploymentplan.Object{"role": name, "namespace": cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud", "service_accounts": accounts, "policies": policies, "audience": "openbao", "ttl": "15m", "no_default_policy": true}
}

func managedLateSessionNativeRecipes(cfg deploymentConfig, late *createManagedLateSessionRecovery) (deploymentplan.Object, deploymentplan.Object, error) {
	if late == nil || !environmentPreflightDigest.MatchString(late.ParentSHA) || !environmentPreflightDigest.MatchString(late.SourceSHA) || !environmentBootstrapOwnerUUID.MatchString(late.NewSessionID) || !environmentPreflightDigest.MatchString(late.SignerPolicySHA) || deploymentplan.Digest(late.OriginalSignerRole) != late.OriginalSignerRoleSHA || deploymentplan.Digest(late.OriginalControllerRole) != late.OriginalControllerRoleSHA || len(late.ControllerPolicies) == 0 {
		return nil, nil, environmentCreateBootstrapBlocker("late native authorization recipes require original role snapshots")
	}
	name := "rtk-" + cfg.Environment + "-service-bootstrap-" + late.NewSessionID
	policy := deploymentplan.Object{"environment": cfg.Environment, "policy": name, "policy_sha256": late.SignerPolicySHA}
	role := managedLateRoleIntent(cfg, "pki-service-bootstrap-"+cfg.Environment, []string{"pki-service-bootstrap"}, []string{name})
	seed := deploymentplan.Object{"schema": managedLateSessionSchema, "parent": late.ParentSHA, "source": late.SourceSHA, "new_session": late.NewSessionID, "original_session": late.OriginalSession, "role_before_sha256": late.OriginalSignerRoleSHA, "policy_intent": policy, "role_intent": role}
	cleanupName := "create-" + late.NewSessionID + "-cleanup-controller"
	cleanupText := fmt.Sprintf("path %q {\n  capabilities = [\"read\", \"delete\"]\n}\n", "sys/policies/acl/"+name)
	policies := append(slices.Clone(late.ControllerPolicies), cleanupName)
	slices.Sort(policies)
	policies = slices.Compact(policies)
	cleanup := deploymentplan.Object{"schema": managedLateSessionSchema, "parent": late.ParentSHA, "source": late.SourceSHA, "new_session": late.NewSessionID, "role_before_sha256": late.OriginalControllerRoleSHA, "policy_intent": deploymentplan.Object{"environment": cfg.Environment, "policy": cleanupName, "policy_sha256": environmentPreflightBytesSHA256([]byte(cleanupText))}, "role_intent": managedLateRoleIntent(cfg, "pki-controller-"+cfg.Environment, []string{"pki-controller"}, policies)}
	return seed, cleanup, nil
}

func readManagedLateOpenBaoRole(ctx context.Context, cfg deploymentConfig, store secretStore, b environmentKubernetesBackend, name string) (deploymentplan.Object, error) {
	if !secretEnvironmentPattern.MatchString(name) {
		return nil, environmentCreateBootstrapBlocker("late role name malformed")
	}
	token, err := readBoundedEnvironmentFile(store.Root+"/openbao/root-token", true)
	if err != nil {
		return nil, environmentCreateBootstrapBlocker("late original OpenBao custody unavailable")
	}
	raw, err := environmentCreateOpenBaoExec(ctx, cfg, b, append(bytes.TrimSpace(token), '\n'), "IFS= read -r BAO_TOKEN\nexport BAO_TOKEN\nbao read -format=json auth/kubernetes/role/"+name+"\n")
	var envelope struct {
		Data deploymentplan.Object `json:"data"`
	}
	if err != nil || json.Unmarshal(raw, &envelope) != nil || envelope.Data == nil {
		return nil, environmentCreateBootstrapBlocker("late original OpenBao role unavailable")
	}
	return envelope.Data, nil
}

func managedLateRoleMatches(data deploymentplan.Object, cfg deploymentConfig, accounts, policies []string) bool {
	decodeList := func(value any) []string {
		var result []string
		for _, raw := range certIssuerObjectList(value) {
			text, ok := raw.(string)
			if !ok {
				return nil
			}
			result = append(result, text)
		}
		slices.Sort(result)
		return result
	}
	accounts = slices.Clone(accounts)
	policies = slices.Clone(policies)
	slices.Sort(accounts)
	slices.Sort(policies)
	return slices.Equal(decodeList(data["bound_service_account_names"]), accounts) && slices.Equal(decodeList(data["bound_service_account_namespaces"]), []string{cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud"}) && slices.Equal(decodeList(data["token_policies"]), policies) && data["audience"] == "openbao" && data["token_ttl"] == float64(900) && data["token_max_ttl"] == float64(900) && data["token_no_default_policy"] == true
}

func prepareManagedLateSessionAuthorizations(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery, late *createManagedLateSessionRecovery) error {
	old := *r
	old.Review = late.OriginalRepairReview
	old.Ceremony = late.SourceCeremony
	old.Intent, old.RenewedIntent = late.SourceRenewedIntent, late.SourceRenewedIntent
	name, role, text, err := verifyServiceSessionRecordedAuthentication(ctx, cfg, store, e, b, &old)
	if err != nil {
		return err
	}
	if name != "rtk-"+cfg.Environment+"-service-bootstrap-"+late.OriginalSession || role != "pki-service-bootstrap-"+cfg.Environment {
		return environmentCreateBootstrapBlocker("late source signer does not select original expired session")
	}
	if err := verifyServiceSessionAuthentication(ctx, cfg, store, e, b, &old); err != nil {
		return err
	}
	late.SignerPolicySHA = environmentPreflightBytesSHA256([]byte(text))
	late.OriginalSignerRole, err = readManagedLateOpenBaoRole(ctx, cfg, store, b, role)
	if err != nil {
		return err
	}
	late.OriginalSignerRoleSHA = deploymentplan.Digest(late.OriginalSignerRole)
	late.ControllerPolicies = environmentCreateControllerPolicies(environmentCreateBootstrapRuntime{Execution: e})
	late.OriginalControllerRole, err = readManagedLateOpenBaoRole(ctx, cfg, store, b, "pki-controller-"+cfg.Environment)
	if err != nil {
		return err
	}
	if !managedLateRoleMatches(late.OriginalSignerRole, cfg, []string{"pki-service-bootstrap"}, []string{name}) || !managedLateRoleMatches(late.OriginalControllerRole, cfg, []string{"pki-controller"}, late.ControllerPolicies) {
		return environmentCreateBootstrapBlocker("late original exclusive auth roles changed")
	}
	late.OriginalControllerRoleSHA = deploymentplan.Digest(late.OriginalControllerRole)
	late.SeedAuthorization, late.CleanupAuthorization, err = managedLateSessionNativeRecipes(cfg, late)
	if err != nil {
		return err
	}
	p.Materials["accepted-service-core-late-session-native-late-seed-authorization"] = deploymentplan.Digest(late.SeedAuthorization)
	p.Materials["accepted-service-core-late-session-native-late-cleanup-authorization"] = deploymentplan.Digest(late.CleanupAuthorization)
	p.Materials["accepted-service-core-late-session-old-auth-roles"] = deploymentplan.Digest(map[string]string{"signer": late.OriginalSignerRoleSHA, "controller": late.OriginalControllerRoleSHA})
	return e.Check(ctx)
}

func managedLateSessionSourceDigest(late *createManagedLateSessionRecovery) string {
	if late == nil {
		return ""
	}
	copy := *late
	copy.Review = ""
	if copy.ExpiredRenewal != nil {
		f := *copy.ExpiredRenewal
		f.Review = ""
		copy.ExpiredRenewal = &f
	}
	return deploymentplan.Digest(copy)
}

func managedLateAuthorizationEntryIntent(recipe deploymentplan.Object, which string) (string, deploymentplan.Object, error) {
	intent := deploymentplan.Map(recipe[which])
	if intent == nil {
		return "", nil, environmentCreateBootstrapBlocker("late authorization recipe lacks exact native intent")
	}
	if which == "policy_intent" {
		return "openbao-policy-" + deploymentplan.String(intent["policy"]), intent, nil
	}
	if which == "role_intent" {
		return "openbao-role-" + deploymentplan.String(intent["role"]) + "-" + deploymentplan.Digest(intent)[:12], intent, nil
	}
	return "", nil, environmentCreateBootstrapBlocker("late authorization intent type is foreign")
}

// Used only for normalization of planner input binding. No resource or UID can
// be changed by this comparison.
func managedLateSamePlan(actual, want *deploymentplan.Plan) bool {
	if actual == nil || want == nil {
		return false
	}
	copy := *actual
	copy.BindingsSHA256 = want.BindingsSHA256
	return copy.SHA256() == want.SHA256()
}

var _ = strings.TrimSpace
