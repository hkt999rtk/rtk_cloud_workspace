package main

import (
	"context"
	"path/filepath"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type createManagedTransportInterruptedForward struct {
	Previous    *createManagedTransportInterruptedForward `json:"Previous,omitempty"`
	Review      string
	SourceSHA   string
	ArtifactSHA string
	Plan        *deploymentplan.Plan
	Journal     deploymentplan.Journal
}

func managedTransportInterruptedForwardReview(j environmentCreateJournal) string {
	return managedTransportInterruptedForwardReviewDepth(j, 2)
}

func managedTransportInterruptedForwardReviewDepth(j environmentCreateJournal, remaining int) string {
	if len(j.Entries) < 3 || remaining < 1 {
		return ""
	}
	last := j.Entries[len(j.Entries)-1]
	const prefix = "kubernetes/transport-managed-hosts-forward-"
	review := strings.TrimPrefix(last.Key, prefix)
	admission := j.Entries[len(j.Entries)-2]
	base := j
	base.Entries = j.Entries[:len(j.Entries)-2]
	if !strings.HasPrefix(last.Key, prefix) || !environmentPreflightDigest.MatchString(review) || last.State != "pending" || !last.CompletedAt.IsZero() || len(last.Receipt) != 0 || !environmentPreflightDigest.MatchString(last.IntentSHA256) || admission.Key != "openbao-recovery-admission-"+review || admission.State != "completed" || admission.CompletedAt.IsZero() || len(admission.Receipt) == 0 || !environmentPreflightDigest.MatchString(admission.IntentSHA256) || !isManagedTransportBaseCheckpoint(base) && managedTransportInterruptedForwardReviewDepth(base, remaining-1) == "" {
		return ""
	}
	return review
}

func loadManagedTransportPartialBase(store secretStore, e *environmentCreateExecution) (*environmentCreateExecution, *createManagedTransportInterruptedForward, error) {
	if managedTransportPostManagedReview(e.Journal) != "" {
		base, prior, _, _, err := loadManagedTransportPostManaged(store, e)
		return base, prior, err
	}
	if managedTransportAcceptedForwardReview(e.Journal) != "" {
		base, prior, _, err := loadManagedTransportAcceptedForward(store, e)
		return base, prior, err
	}
	if isManagedTransportBaseCheckpoint(e.Journal) {
		return e, nil, nil
	}
	bad := func() (*environmentCreateExecution, *createManagedTransportInterruptedForward, error) {
		return nil, nil, createProviderRecoveryBlock("interrupted managed host forward must preserve its exact admitted source, one child lease and recorded writes")
	}
	review := managedTransportInterruptedForwardReview(e.Journal)
	if review == "" {
		return bad()
	}
	a, source, err := loadAdmittedCreateRecovery(store, e, review)
	if err != nil || a.Plan.Schema != "rtk-cloud.create-managed-transports-partial-recovery/v1" || !isManagedTransportBaseCheckpoint(source) && managedTransportInterruptedForwardReviewDepth(source, 1) == "" || len(e.Journal.Entries) != len(source.Entries)+2 || len(e.Journal.KubernetesLeases) != len(source.KubernetesLeases)+1 {
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
	base, previous, err := loadManagedTransportPartialBase(store, &historical)
	if err != nil {
		return bad()
	}
	original, _, err := loadOriginalManagedTransportPartialPlan(store, base)
	if err != nil || a.Plan.Materials["accepted-service-core-managed-partial-original-plan"] != original.SHA256() {
		return bad()
	}
	lease := e.Journal.KubernetesLeases[len(source.KubernetesLeases)]
	sha := strings.SplitN(lease.Holder, ":", 2)[0]
	if !environmentPreflightDigest.MatchString(sha) || lease.UID == "" || lease.ResourceID != "Lease/"+source.Stack+"-video-cloud/rtk-deployment-execution" {
		return bad()
	}
	var plan, preview deploymentplan.Plan
	var journal deploymentplan.Journal
	path := filepath.Join(store.Root, "deployment/rollback", sha)
	if readExecutionJSON(filepath.Join(path, "plan.json"), &plan) != nil || readExecutionJSON(filepath.Join(path, "journal.json"), &journal) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/create/openbao-recovery", review, "openbao-root-trust-plan.json"), &preview) != nil || plan.SHA256() != sha || preview.SHA256() != a.Plan.Materials["accepted-service-core-managed-partial-forward-plan"] {
		return bad()
	}
	candidate := plan
	candidate.BindingsSHA256 = preview.BindingsSHA256
	if candidate.SHA256() != preview.SHA256() {
		return bad()
	}
	intent, err := environmentCreateKubernetesStageIntent(&plan)
	hash, hashErr := environmentCreatePlanSHA256(intent)
	if err != nil || hashErr != nil || hash != e.Journal.Entries[len(e.Journal.Entries)-1].IntentSHA256 {
		return bad()
	}
	if previous == nil {
		if validateManagedTransportInterruptedForward(original, &plan, journal) != nil {
			return bad()
		}
	} else {
		if previous.Previous != nil || validateManagedTransportWrittenOpenBaoJournal(&plan, journal) != nil {
			return bad()
		}
		_, _, seedReview, err := loadManagedTransportPartialSource(store, base)
		if err != nil {
			return bad()
		}
		owners, err := loadManagedTransportAdditionalSourceOwners(store, base, seedReview)
		if err != nil {
			return bad()
		}
		partial := &createManagedTransportPartialRecovery{Original: original, PriorForward: previous, AdditionalOwners: owners}
		definitions := []deploymentplan.Definition{}
		for _, resource := range plan.Resources {
			definitions = append(definitions, deploymentplan.Definition{Phase: resource.Phase, Object: resource.Desired, Dependencies: resource.Dependencies})
		}
		if validateManagedTransportPartialForwardDefinitions(partial, definitions) != nil || validateManagedTransportPartialForwardActions(partial, &plan) != nil {
			return bad()
		}
		before := map[string]deploymentplan.Resource{}
		for _, resource := range previous.Plan.Resources {
			if strings.HasPrefix(resource.ID, "StatefulSet/") {
				resource.Desired = resource.Baseline
			}
			before[resource.ID] = resource
		}
		for id, resource := range owners {
			before[id] = resource
		}
		for _, resource := range plan.Resources {
			if !deploymentplan.Matches(before[resource.ID].Desired, resource.Baseline) {
				return bad()
			}
		}
	}
	return base, &createManagedTransportInterruptedForward{Previous: previous, Review: review, SourceSHA: deploymentplan.Digest(source), ArtifactSHA: deploymentplan.Digest(a), Plan: &plan, Journal: journal}, nil
}

func validateManagedTransportInterruptedForward(original, plan *deploymentplan.Plan, journal deploymentplan.Journal) error {
	bad := func() error {
		return createProviderRecoveryBlock("interrupted managed host forward contains an unqualified owner, recipe or write")
	}
	if original == nil || plan == nil || len(original.Resources) != 3 || len(plan.Resources) != 5 || plan.Environment != original.Environment || plan.Stack != original.Stack || journal.PlanSHA256 != plan.SHA256() || journal.Accepted || journal.AcceptanceStarted || journal.InFlight != "" || journal.Failed != "Deployment/"+plan.Stack+"-video-cloud/mqtt-pki" || len(journal.Owners) != 4 {
		return bad()
	}
	cm := "ConfigMap/" + plan.Stack + "-secrets/openbao-file"
	config := "ConfigMap/" + plan.Stack + "-video-cloud/mqtt-pki-config"
	secret := "Secret/" + plan.Stack + "-video-cloud/mqtt-pki-runtime"
	if !slices.Equal(journal.Completed, []string{cm, config, secret}) {
		return bad()
	}
	before := map[string]deploymentplan.Resource{}
	for _, resource := range original.Resources {
		before[resource.ID] = resource
	}
	seen := map[string]bool{}
	for _, resource := range plan.Resources {
		if seen[resource.ID] {
			return bad()
		}
		seen[resource.ID] = true
		if old, exists := before[resource.ID]; exists {
			expected := old.Desired
			if strings.HasPrefix(resource.ID, "StatefulSet/") {
				expected = old.Baseline
			}
			if old.UID == "" || resource.UID != old.UID || resource.Baseline == nil || !deploymentplan.Matches(expected, resource.Baseline) {
				return bad()
			}
			if strings.HasPrefix(resource.ID, "StatefulSet/") {
				if resource.Action != "update" || journal.Owners[resource.ID] != "" || deploymentplan.Digest(resource.Desired) != deploymentplan.Digest(old.Desired) {
					return bad()
				}
			} else if journal.Owners[resource.ID] != old.UID || resource.ID == cm && (resource.Action != "retain" || deploymentplan.Digest(resource.Desired) != deploymentplan.Digest(old.Desired)) || strings.HasPrefix(resource.ID, "Deployment/") && resource.Action != "update" {
				return bad()
			}
		} else if resource.ID != config && resource.ID != secret || resource.Action != "create" || resource.UID != "" || resource.Baseline != nil || !environmentBootstrapOwnerUUID.MatchString(journal.Owners[resource.ID]) {
			return bad()
		}
	}
	return nil
}

func verifyManagedTransportCurrentPartialBodies(ctx context.Context, b environmentKubernetesBackend, partial *createManagedTransportPartialRecovery) error {
	if partial.PostManaged != nil {
		for _, resource := range partial.PostManaged.Plan.Resources {
			actual, err := b.Read(ctx, resource.ID)
			if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != partial.PostManaged.Journal.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, actual) {
				return createProviderRecoveryBlock("completed managed transport source owner or recipe changed: resource=" + resource.ID)
			}
		}
		return nil
	}
	if partial.PriorForward == nil {
		return verifyManagedTransportPartialBodies(ctx, b, partial.Original, partial.Journal)
	}
	forward := partial.PriorForward
	if partial.AcceptedForward != nil {
		accepted := partial.AcceptedForward
		if err := validateManagedTransportAcceptedForwardJournal(accepted.Plan, accepted.Journal); err != nil {
			return err
		}
		for _, resource := range accepted.Plan.Resources {
			actual, err := b.Read(ctx, resource.ID)
			if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != accepted.Journal.Owners[resource.ID] || !deploymentplan.Matches(resource.Desired, actual) {
				return createProviderRecoveryBlock("accepted managed host owner or body changed: resource=" + resource.ID)
			}
		}
		return nil
	}
	if forward.Previous == nil {
		if err := validateManagedTransportInterruptedForward(partial.Original, forward.Plan, forward.Journal); err != nil {
			return err
		}
	} else if err := validateManagedTransportWrittenOpenBaoJournal(forward.Plan, forward.Journal); err != nil {
		return err
	}
	for _, resource := range forward.Plan.Resources {
		expected, owner := resource.Desired, forward.Journal.Owners[resource.ID]
		if strings.HasPrefix(resource.ID, "StatefulSet/") && forward.Previous == nil {
			expected, owner = resource.Baseline, resource.UID
		}
		actual, err := b.Read(ctx, resource.ID)
		if err != nil || actual == nil || owner == "" || deploymentplan.Map(actual["metadata"])["uid"] != owner || !deploymentplan.Matches(expected, actual) {
			return createProviderRecoveryBlock("interrupted managed host current owner or recipe changed: resource=" + resource.ID)
		}
	}
	for id, resource := range partial.AdditionalOwners {
		actual, err := b.Read(ctx, id)
		if err != nil || actual == nil || resource.UID == "" || deploymentplan.Map(actual["metadata"])["uid"] != resource.UID || !deploymentplan.Matches(resource.Desired, actual) {
			return createProviderRecoveryBlock("original ACK-policy source owner or recipe changed: resource=" + id)
		}
	}
	return nil
}

func validateManagedTransportPartialForwardDefinitions(partial *createManagedTransportPartialRecovery, definitions []deploymentplan.Definition) error {
	if partial.PostManaged != nil {
		return validateManagedTransportPostManagedDefinitions(partial, definitions)
	}
	if partial.AcceptedForward != nil {
		return validateManagedTransportAcceptedAuditDefinitions(partial, definitions)
	}
	if partial.PriorForward == nil {
		return validateManagedTransportForwardDefinitions(partial.Original, definitions)
	}
	bad := func() error {
		return createProviderRecoveryBlock("managed host ACK-policy forward must retain its seven exact original resources")
	}
	if len(definitions) != 7 || len(partial.AdditionalOwners) != 2 {
		return bad()
	}
	compiled, err := deploymentplan.Compile(partial.Original.Inputs, definitions, nil)
	if err != nil {
		return err
	}
	before := map[string]deploymentplan.Resource{}
	for _, resource := range partial.PriorForward.Plan.Resources {
		before[resource.ID] = resource
	}
	for id, resource := range partial.AdditionalOwners {
		before[id] = resource
	}
	if len(before) != 7 || len(compiled.Resources) != 7 {
		return bad()
	}
	for _, resource := range compiled.Resources {
		old, exists := before[resource.ID]
		if !exists {
			return bad()
		}
		if partial.PriorForward.Previous != nil {
			if resource.ID == "StatefulSet/"+partial.Original.Stack+"-secrets/openbao" {
				if err := validateManagedTransportOpenBaoPullSecretBody(old.Desired, resource.Desired); err != nil {
					return err
				}
			} else if deploymentplan.Digest(resource.Desired) != deploymentplan.Digest(old.Desired) {
				return bad()
			}
		} else if resource.ID == "ConfigMap/"+partial.Original.Stack+"-secrets/pki-create-service-trust" {
			candidate := deploymentplan.Clone(resource.Desired)
			data := deploymentplan.Map(candidate["data"])
			manifest := deploymentplan.String(data["outbound-crls.json"])
			delete(data, "outbound-crls.json")
			prior := deploymentplan.Clone(old.Desired)
			delete(deploymentplan.Map(prior["data"]), "outbound-crls.json")
			if manifest == "" || deploymentplan.Digest(candidate) != deploymentplan.Digest(prior) {
				return bad()
			}
		} else if resource.ID == "Deployment/"+partial.Original.Stack+"-video-cloud/pki-controller" {
			if err := validateManagedTransportControllerPolicyBody(old.Desired, resource.Desired); err != nil {
				return err
			}
		} else if resource.Desired["kind"] == "ConfigMap" || resource.Desired["kind"] == "Secret" {
			if deploymentplan.Digest(resource.Desired) != deploymentplan.Digest(old.Desired) {
				return bad()
			}
		} else if err := validateManagedTransportServiceHostPolicyBody(old.Desired, resource.Desired, partial.Original.Stack); err != nil {
			return err
		}
	}
	return nil
}

func validateManagedTransportPartialForwardActions(partial *createManagedTransportPartialRecovery, candidate *deploymentplan.Plan) error {
	if partial.PostManaged != nil {
		if candidate == nil || len(candidate.Resources) != 7 {
			return createProviderRecoveryBlock("post-managed API export must retain all seven completed transport owners")
		}
		for _, resource := range candidate.Resources {
			if resource.Action != "retain" || resource.UID == "" || resource.UID != partial.PostManaged.Journal.Owners[resource.ID] {
				return createProviderRecoveryBlock("post-managed API export cannot replay or replace a completed transport owner")
			}
		}
		return nil
	}
	if partial.AcceptedForward != nil {
		if candidate == nil || len(candidate.Resources) != 7 {
			return createProviderRecoveryBlock("accepted audit recovery needs seven original owners")
		}
		seen := map[string]bool{}
		for _, resource := range candidate.Resources {
			if seen[resource.ID] || resource.UID == "" || resource.UID != partial.AcceptedForward.Journal.Owners[resource.ID] || resource.Baseline == nil {
				return createProviderRecoveryBlock("accepted audit recovery changed an original owner")
			}
			seen[resource.ID] = true
			if resource.ID == "ConfigMap/"+partial.Original.Stack+"-secrets/openbao-file" {
				if resource.Action != "update" {
					return createProviderRecoveryBlock("original audit producer must be a qualified update")
				}
			} else if resource.Action != "retain" {
				return createProviderRecoveryBlock("accepted host replay is forbidden")
			}
		}
		return nil
	}
	if partial.PriorForward == nil {
		return validateManagedTransportForwardActions(partial.Original, candidate)
	}
	bad := func() error {
		return createProviderRecoveryBlock("managed host ACK-policy forward cannot replace or create any source resource")
	}
	owners := map[string]string{}
	for _, resource := range partial.PriorForward.Plan.Resources {
		uid := partial.PriorForward.Journal.Owners[resource.ID]
		if strings.HasPrefix(resource.ID, "StatefulSet/") {
			uid = resource.UID
		}
		owners[resource.ID] = uid
	}
	for id, resource := range partial.AdditionalOwners {
		owners[id] = resource.UID
	}
	if candidate == nil || len(candidate.Resources) != 7 || len(owners) != 7 {
		return bad()
	}
	seen := map[string]bool{}
	for _, resource := range candidate.Resources {
		if seen[resource.ID] || owners[resource.ID] == "" || resource.UID != owners[resource.ID] || resource.Action != "retain" && resource.Action != "update" || resource.Baseline == nil {
			return bad()
		}
		if partial.PriorForward.Previous != nil {
			if resource.ID == "StatefulSet/"+partial.Original.Stack+"-secrets/openbao" {
				if resource.Action != "update" {
					return bad()
				}
			} else if resource.Action != "retain" {
				return bad()
			}
		}
		seen[resource.ID] = true
	}
	return nil
}

func validateManagedTransportControllerPolicyBody(original, desired deploymentplan.Object) error {
	bad := func() error {
		return createProviderRecoveryBlock("managed transport Controller change exceeds its original Service consumer policy")
	}
	before := managedNamedField(environmentPKIPod(original)["containers"], "pki-controller")
	candidate := deploymentplan.Clone(desired)
	after := managedNamedField(environmentPKIPod(candidate)["containers"], "pki-controller")
	if before == nil || after == nil {
		return bad()
	}
	previous := mqttFoundationEnv(before, "PKI_REQUIRED_CONSUMERS_SERVICE")
	ids, err := managedTransportServiceConsumerIDs(strings.Split(previous, ","))
	if err != nil || len(ids) != 9 || mqttFoundationEnv(after, "PKI_REQUIRED_CONSUMERS_SERVICE") != strings.Join(ids, ",") {
		return bad()
	}
	after["env"] = mergeEnvironmentNamedFields(after["env"], environmentCreateEnv(map[string]string{"PKI_REQUIRED_CONSUMERS_SERVICE": previous}))
	if deploymentplan.Digest(candidate) != deploymentplan.Digest(original) {
		return bad()
	}
	return nil
}

// Successful host acceptance followed by a crash before alias closure requires
// separately qualified observation-only completion of the real accepted child.
// This failed-five-object selector must never replay an accepted host stage.
func validateManagedTransportServiceHostPolicyBody(original, desired deploymentplan.Object, stack string) error {
	bad := func() error {
		return createProviderRecoveryBlock("managed transport Service CRL change exceeds its original host identity recipe: resource=" + deploymentplan.ID(original))
	}
	if deploymentplan.Digest(original) == deploymentplan.Digest(desired) {
		return nil
	}
	candidate, before := deploymentplan.Clone(desired), deploymentplan.Clone(original)
	oldPod, pod := environmentPKIPod(before), environmentPKIPod(candidate)
	mqtt := deploymentplan.ID(original) == "Deployment/"+stack+"-video-cloud/mqtt-pki"
	if !mqtt && deploymentplan.ID(original) != "StatefulSet/"+stack+"-secrets/openbao" {
		return bad()
	}
	aliases := map[string]string{"service-state": "openbao-pki-identity"}
	if mqtt {
		aliases = map[string]string{"service-state": "mqtt-pki-trust", "mqtt-service-state": "mqtt-pki-host-identity"}
	}
	volumes := []any{}
	for _, entry := range certIssuerObjectList(pod["volumes"]) {
		volume := deploymentplan.Map(entry)
		name := deploymentplan.String(volume["name"])
		if claim, allowed := aliases[name]; allowed {
			if managedNamedField(oldPod["volumes"], name) != nil || deploymentplan.Digest(volume) != deploymentplan.Digest(environmentCreateServiceVolume(name, "persistentVolumeClaim", claim)) {
				return bad()
			}
			continue
		}
		volumes = append(volumes, entry)
	}
	pod["volumes"] = volumes
	for _, group := range []string{"containers", "initContainers"} {
		for _, entry := range certIssuerObjectList(pod[group]) {
			container := deploymentplan.Map(entry)
			name := deploymentplan.String(container["name"])
			old := managedNamedField(oldPod[group], name)
			if old == nil {
				return bad()
			}
			prefixes := []string{}
			alias := ""
			if mqtt && group == "containers" && name == "mqtt" {
				prefixes = []string{"EMQX_PKI_HOST_RENEWAL"}
				alias = "mqtt-service-state"
			}
			if mqtt && group == "containers" && name == "pkibroker" {
				prefixes = []string{"PKI_BROKER_IDENTITY", "PKI_BROKER_IDENTITY_RENEWAL"}
				alias = "service-state"
			}
			if !mqtt && (name == "openbao-pki" || name == "openbao-pki-install") {
				prefixes = []string{"OPENBAO_PKI_HOST_RENEWAL"}
				alias = "service-state"
			}
			allowed := map[string]string{}
			for _, prefix := range prefixes {
				allowed[prefix+"_SERVER_CRL_MANIFEST"] = environmentCreateServiceTrust + "/outbound-crls.json"
				allowed[prefix+"_PKI_CONTROLLER_URL"] = "https://pki-controller." + stack + "-video-cloud.svc:18446"
				allowed[prefix+"_MANAGEMENT_CA"] = environmentCreateServiceTrust + "/root.pem"
			}
			stripEnv := func(values any, check bool) ([]any, error) {
				out := []any{}
				for _, value := range certIssuerObjectList(values) {
					field := deploymentplan.Map(value)
					key := deploymentplan.String(field["name"])
					if expected, exists := allowed[key]; exists {
						if check && (field["value"] != expected || field["valueFrom"] != nil) {
							return nil, bad()
						}
						continue
					}
					out = append(out, value)
				}
				return out, nil
			}
			if len(allowed) > 0 {
				var err error
				container["env"], err = stripEnv(container["env"], true)
				if err != nil {
					return err
				}
				old["env"], err = stripEnv(old["env"], false)
				if err != nil {
					return err
				}
			}
			if alias != "" {
				mounts := []any{}
				for _, value := range certIssuerObjectList(container["volumeMounts"]) {
					mount := deploymentplan.Map(value)
					if mount["name"] == alias {
						if managedNamedField(old["volumeMounts"], alias) != nil || deploymentplan.Digest(mount) != deploymentplan.Digest(environmentCreateServiceMount(alias, environmentCreateServiceState, false)) {
							return bad()
						}
						continue
					}
					mounts = append(mounts, value)
				}
				container["volumeMounts"] = mounts
			}
			if group == "initContainers" && mqtt && (name == "prepare-pki-state" || name == "prepare-mqtt-host-state") {
				root := "/run/pki-state"
				if name == "prepare-mqtt-host-state" {
					root = "/var/lib/emqx-pki"
				}
				oldCommand, newCommand := certIssuerObjectList(old["command"]), certIssuerObjectList(container["command"])
				if len(oldCommand) != 3 || len(newCommand) != 3 || oldCommand[0] != newCommand[0] || oldCommand[1] != newCommand[1] || deploymentplan.String(newCommand[2]) != deploymentplan.String(oldCommand[2])+"; umask 077; mkdir -p "+root+"/private/crl; chmod 700 "+root+"/private "+root+"/private/crl" {
					return bad()
				}
				container["command"] = old["command"]
			}
			if group == "initContainers" && !mqtt && name == "openbao-pki-install" {
				expected := []any{"/bin/sh", "-ec", "umask 077; mkdir -p /var/lib/pki-service/private/crl; chmod 700 /var/lib/pki-service/private /var/lib/pki-service/private/crl; exec /usr/local/bin/openbaopkihost install"}
				if deploymentplan.Digest(container["command"]) != deploymentplan.Digest(expected) || deploymentplan.Digest(old["command"]) != deploymentplan.Digest([]any{"/usr/local/bin/openbaopkihost", "install"}) {
					return bad()
				}
				container["command"] = old["command"]
			}
		}
	}
	if deploymentplan.Digest(candidate) != deploymentplan.Digest(before) {
		return bad()
	}
	return nil
}

func loadManagedTransportAdditionalSourceOwners(store secretStore, e *environmentCreateExecution, seedReview string) (map[string]deploymentplan.Resource, error) {
	owners := map[string]deploymentplan.Resource{}
	for _, stage := range []struct {
		key  string
		size int
	}{{"kubernetes/service-credential-secrets-renewal-" + seedReview, 7}, {"kubernetes/transport-enrollment-issuer", 16}} {
		plan, journal, err := authenticatedManagedTransportSourceStage(store, e, stage.key, stage.size, map[string]string{})
		if err != nil {
			return nil, err
		}
		for _, resource := range plan.Resources {
			if resource.ID == "Deployment/"+e.Journal.Stack+"-video-cloud/pki-controller" || resource.ID == "ConfigMap/"+e.Journal.Stack+"-secrets/pki-create-service-trust" {
				resource.UID = journal.Owners[resource.ID]
				owners[resource.ID] = resource
			}
		}
	}
	if len(owners) != 2 {
		return nil, createProviderRecoveryBlock("exact original ACK-policy source owners are unavailable")
	}
	return owners, nil
}

func validateManagedTransportWrittenOpenBaoJournal(plan *deploymentplan.Plan, journal deploymentplan.Journal) error {
	bad := func() error {
		return createProviderRecoveryBlock("written OpenBao pull recovery requires exactly six completed original resources and its known failed seventh write")
	}
	if plan == nil || len(plan.Resources) != 7 || journal.PlanSHA256 != plan.SHA256() || journal.Accepted || journal.AcceptanceStarted || journal.InFlight != "" || journal.Failed != "StatefulSet/"+plan.Stack+"-secrets/openbao" || len(journal.Owners) != 7 || len(journal.Completed) != 6 {
		return bad()
	}
	for index, resource := range plan.Resources {
		if resource.UID == "" || journal.Owners[resource.ID] != resource.UID || resource.Action != "update" && resource.Action != "retain" || resource.Baseline == nil {
			return bad()
		}
		if index < 6 && journal.Completed[index] != resource.ID || index == 6 && resource.ID != journal.Failed {
			return bad()
		}
	}
	return nil
}

func validateManagedTransportOpenBaoPullSecretBody(original, desired deploymentplan.Object) error {
	candidate := deploymentplan.Clone(desired)
	pod := environmentPKIPod(candidate)
	if deploymentplan.Digest(pod["imagePullSecrets"]) != deploymentplan.Digest([]any{deploymentplan.Object{"name": "ghcr-pull"}}) {
		return createProviderRecoveryBlock("OpenBao recovery requires its selected existing ghcr-pull reference")
	}
	before := environmentPKIPod(original)
	if value, exists := before["imagePullSecrets"]; exists {
		pod["imagePullSecrets"] = value
	} else {
		delete(pod, "imagePullSecrets")
	}
	if deploymentplan.Digest(candidate) != deploymentplan.Digest(original) {
		return createProviderRecoveryBlock("OpenBao pull recovery changed an unrelated original workload field")
	}
	return nil
}

func managedTransportWrittenPolicySourceOverride(partial *createManagedTransportPartialRecovery, id, owner string, expected deploymentplan.Object) (deploymentplan.Object, error) {
	if partial == nil || partial.PriorForward == nil || partial.PriorForward.Previous == nil || id != "Deployment/"+partial.Original.Stack+"-video-cloud/pki-controller" && id != "ConfigMap/"+partial.Original.Stack+"-secrets/pki-create-service-trust" {
		return expected, nil
	}
	forward := partial.PriorForward
	for _, resource := range forward.Plan.Resources {
		if resource.ID == id {
			if resource.UID == owner && forward.Journal.Owners[id] == owner && slices.Contains(forward.Journal.Completed, id) && deploymentplan.Matches(expected, resource.Baseline) {
				return resource.Desired, nil
			}
			break
		}
	}
	return nil, createProviderRecoveryBlock("written Service policy supersession changed: resource=" + id)
}
