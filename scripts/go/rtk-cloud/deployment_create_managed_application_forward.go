package main

import (
	"context"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"time"
)

// Preserve the authenticated final recipe; repair only the refused public
// ConfigMap/subPath paths. Private identities and source projections stay exact.
func managedApplicationCRLProjectionRepair(object deploymentplan.Object) (deploymentplan.Object, error) {
	object = deploymentplan.Clone(object)
	name := deploymentplan.String(deploymentplan.Map(object["metadata"])["name"])
	if object["kind"] != "Deployment" || name != "video-cloud-api" && name != "video-cloud-logingester" {
		return object, nil
	}
	bad := func() (deploymentplan.Object, error) {
		return nil, createProviderRecoveryBlock("managed application CRL repair requires its exact original projection roles: owner=" + name)
	}
	pod := environmentPKIPod(object)
	containers := certIssuerObjectList(pod["containers"])
	if len(containers) != 1 {
		return bad()
	}
	c := deploymentplan.Map(containers[0])
	serviceRole := "account-manager-crls"
	if name == "video-cloud-logingester" {
		serviceRole = "service-crls"
	}
	for _, role := range []struct{ name, source, old, next string }{
		{"mqtt-pki-crls", "pki-create-mqtt-consumer-trust", "/run/pki-mqtt/crls.json", "/run/pki-mqtt-crls/crls.json"},
		{serviceRole, "pki-create-service-consumer-crls", environmentCreateServiceTrust + "/outbound-crls.json", "/run/pki-service-crls/outbound-crls.json"},
	} {
		mounts, volumes := 0, 0
		for _, raw := range certIssuerObjectList(c["volumeMounts"]) {
			m := deploymentplan.Map(raw)
			if m["name"] == role.name {
				if m["mountPath"] != role.old || m["subPath"] != "crls.json" || m["readOnly"] != true {
					return bad()
				}
				m["mountPath"] = role.next
				mounts++
			}
		}
		for _, raw := range certIssuerObjectList(pod["volumes"]) {
			v := deploymentplan.Map(raw)
			if v["name"] == role.name {
				if deploymentplan.Map(v["configMap"])["name"] != role.source {
					return bad()
				}
				volumes++
			}
		}
		if mounts != 1 || volumes != 1 {
			return bad()
		}
	}
	mqtt, service := 0, 0
	for _, raw := range certIssuerObjectList(c["env"]) {
		setting := deploymentplan.Map(raw)
		key := deploymentplan.String(setting["name"])
		if key == "VIDEO_CLOUD_MQTT_SERVER_CRL_MANIFEST" {
			if setting["value"] != "/run/pki-mqtt/crls.json" {
				return bad()
			}
			setting["value"] = "/run/pki-mqtt-crls/crls.json"
			mqtt++
		}
		if strings.HasSuffix(key, "_SERVER_CRL_MANIFEST") && setting["value"] == environmentCreateServiceTrust+"/outbound-crls.json" {
			setting["value"] = "/run/pki-service-crls/outbound-crls.json"
			if key != "VIDEO_CLOUD_CONTROLLER_SERVER_CRL_MANIFEST" {
				service++
			}
		}
	}
	if mqtt != 1 || service != 2 {
		return bad()
	}
	return object, nil
}

func managedApplicationPartialDefinitions(partial *createManagedApplicationPartialRecovery) ([]deploymentplan.Definition, error) {
	return managedApplicationPartialDefinitionsWithIncoming(partial, true, true, true)
}

func managedApplicationPartialDefinitionsWithServiceCRL(partial *createManagedApplicationPartialRecovery, repairService bool) ([]deploymentplan.Definition, error) {
	return managedApplicationPartialDefinitionsWithRepairs(partial, repairService, false)
}

func managedApplicationPartialDefinitionsWithRepairs(partial *createManagedApplicationPartialRecovery, repairService, repairApp bool) ([]deploymentplan.Definition, error) {
	return managedApplicationPartialDefinitionsWithIncoming(partial, repairService, repairApp, false)
}

func managedApplicationPartialDefinitionsWithIncoming(partial *createManagedApplicationPartialRecovery, repairService, repairApp, repairIncoming bool) ([]deploymentplan.Definition, error) {
	plan, _, err := managedApplicationPartialCurrentPlan(partial)
	if err != nil {
		return nil, err
	}
	var definitions []deploymentplan.Definition
	for _, resource := range plan.Resources {
		object := deploymentplan.Clone(resource.Desired)
		if partial.Interrupted == nil {
			object, err = managedApplicationCRLProjectionRepair(object)
			if err != nil {
				return nil, err
			}
		}
		name := deploymentplan.String(deploymentplan.Map(object["metadata"])["name"])
		if repairService && object["kind"] == "Deployment" && (name == "video-cloud-api" || name == "video-cloud-api-pki" || name == "video-cloud-api-app-pki") {
			object, err = managedApplicationServiceCRLConfigurationRepair(object)
			if err != nil {
				return nil, err
			}
		}
		if repairApp && object["kind"] == "Deployment" && name == "video-cloud-api-app-pki" {
			object, err = managedApplicationAppCRLRoleRepair(object)
			if err != nil {
				return nil, err
			}
		}
		if repairIncoming && object["kind"] == "Deployment" && (name == "video-cloud-api-pki" || name == "video-cloud-api-app-pki") {
			object, err = managedApplicationIncomingManifestRepair(object)
			if err != nil {
				return nil, err
			}
		}
		definitions = append(definitions, deploymentplan.Definition{Phase: resource.Phase, Object: object, Dependencies: resource.Dependencies})
	}
	return definitions, nil
}

func validateManagedApplicationPartialCandidate(partial *createManagedApplicationPartialRecovery, candidate *deploymentplan.Plan) error {
	return validateManagedApplicationCandidateWithIncoming(partial, candidate, true, true, true)
}

// An immutable admitted predecessor is checked against the recipe it actually
// qualified. Future public configuration repairs never reinterpret that write.
func validateManagedApplicationHistoricalPartialCandidate(partial *createManagedApplicationPartialRecovery, candidate *deploymentplan.Plan) error {
	return validateManagedApplicationCandidateWithRepairs(partial, candidate, false, false)
}

func validateManagedApplicationHistoricalServiceCandidate(partial *createManagedApplicationPartialRecovery, candidate *deploymentplan.Plan) error {
	return validateManagedApplicationCandidateWithRepairs(partial, candidate, true, false)
}

func validateManagedApplicationHistoricalAppRoleCandidate(partial *createManagedApplicationPartialRecovery, candidate *deploymentplan.Plan) error {
	return validateManagedApplicationCandidateWithRepairs(partial, candidate, true, true)
}

func validateManagedApplicationCandidateWithRepairs(partial *createManagedApplicationPartialRecovery, candidate *deploymentplan.Plan, repairService, repairApp bool) error {
	return validateManagedApplicationCandidateWithIncoming(partial, candidate, repairService, repairApp, false)
}

func validateManagedApplicationCandidateWithIncoming(partial *createManagedApplicationPartialRecovery, candidate *deploymentplan.Plan, repairService, repairApp, repairIncoming bool) error {
	bad := func() error {
		return createProviderRecoveryBlock("managed application forward must retain its authenticated completed prefix and update only the remaining original workloads")
	}
	definitions, err := managedApplicationPartialDefinitionsWithIncoming(partial, repairService, repairApp, repairIncoming)
	if err != nil {
		return err
	}
	if candidate == nil || len(candidate.Resources) != 26 {
		return bad()
	}
	expected := map[string]deploymentplan.Object{}
	for _, d := range definitions {
		expected[deploymentplan.ID(d.Object)] = d.Object
	}
	plan, journal, err := managedApplicationPartialCurrentPlan(partial)
	if err != nil {
		return err
	}
	for index, resource := range candidate.Resources {
		original := plan.Resources[index]
		if resource.ID != original.ID || !deploymentplan.Matches(expected[resource.ID], resource.Desired) || deploymentplan.Digest(resource.Dependencies) != deploymentplan.Digest(original.Dependencies) {
			return bad()
		}
		owner, body := journal.Owners[resource.ID], original.Desired
		if index > len(journal.Completed) {
			owner, body = original.UID, original.Baseline
		}
		if owner == "" || resource.UID != owner || resource.Baseline == nil || !deploymentplan.Matches(body, resource.Baseline) {
			return bad()
		}
		if index < len(journal.Completed) && resource.Action != "retain" || index >= len(journal.Completed) && resource.Action != "update" {
			return bad()
		}
	}
	return nil
}

func prepareManagedApplicationPartialCandidate(ctx context.Context, cfg deploymentConfig, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery) error {
	definitions, err := managedApplicationPartialDefinitions(r.ManagedApplicationPartial)
	if err != nil {
		return err
	}
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return err
	}
	r.Candidate, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("managed-application-owners-forward", definitions)}, definitions, live)
	if err != nil {
		return err
	}
	if err := validateManagedApplicationPartialCandidate(r.ManagedApplicationPartial, r.Candidate); err != nil {
		return err
	}
	p.Materials["accepted-service-core-managed-application-forward-plan"] = r.Candidate.SHA256()
	return e.Check(ctx)
}

func executeManagedApplicationPartialForward(ctx context.Context, runtime environmentCreateBootstrapRuntime, apply environmentCreateBootstrapApply) error {
	r := runtime.ServiceSessionRecovery
	if r == nil || r.ManagedApplicationPartial == nil || validateManagedApplicationPartialCandidate(r.ManagedApplicationPartial, r.Candidate) != nil {
		return createProviderRecoveryBlock("qualified original application partial continuation unavailable")
	}
	definitions, err := managedApplicationPartialDefinitions(r.ManagedApplicationPartial)
	if err != nil {
		return err
	}
	live, err := runtime.Backend.Inventory(ctx, r.Candidate.Stack)
	if err != nil {
		return err
	}
	fresh, err := deploymentplan.Compile(r.Candidate.Inputs, definitions, live)
	if err != nil {
		return err
	}
	if fresh.SHA256() != r.Candidate.SHA256() || validateManagedApplicationPartialCandidate(r.ManagedApplicationPartial, fresh) != nil {
		return createProviderRecoveryBlock("managed application qualified owner, body or action changed before forward execution")
	}
	stage := "managed-application-owners-forward-" + r.Review
	if err := apply(ctx, stage, definitions); err != nil {
		return err
	}
	for previous := r.ManagedApplicationPartial.Interrupted; previous != nil; previous = previous.Previous {
		key := "kubernetes/managed-application-owners-forward-" + previous.Review
		if err := runtime.Execution.RecordReceipt(key, mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": previous.Plan.SHA256(), "retained_session_id": r.Ceremony.BootstrapSessionID})); err != nil {
			return err
		}
		if err := runtime.Execution.CompleteEntry(key, time.Now().UTC()); err != nil {
			return err
		}
	}
	if err := runtime.Execution.RecordReceipt("kubernetes/managed-application-owners", mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": r.ManagedApplicationPartial.Plan.SHA256(), "retained_session_id": r.Ceremony.BootstrapSessionID})); err != nil {
		return err
	}
	return runtime.Execution.CompleteEntry("kubernetes/managed-application-owners", time.Now().UTC())
}

func completeManagedApplicationPartialBootstrap(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, store secretStore, operator map[string]string, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, apply environmentCreateBootstrapApply) (environmentIdentityBindings, error) {
	r := runtime.ServiceSessionRecovery
	roots := map[string]environmentCreateRegisteredIssuer{"service": service.Root, "device": r.ManagedTransportRecovery.Device, "app": r.ManagedTransportRecovery.AppRoot, "mqtt": *r.MQTTRoot, "openbao_tls": *r.OpenBaoRoot}
	intermediates := map[string]environmentCreateRegisteredIssuer{"service": service.Intermediate, "app": r.ManagedTransportRecovery.AppIntermediate, "mqtt": *r.MQTTIntermediate, "openbao_tls": *r.OpenBaoIntermediate}
	if err := verifyEnvironmentCreateRegisteredIssuers(store, roots, intermediates, nil); err != nil {
		return environmentIdentityBindings{}, err
	}
	// The completed Device/App ceremonies already admitted this native broker
	// consumer policy. Keep it in the maintained core recipe used by the tail.
	listeners, err := configureCreateBrokerServiceConsumer(p, runtime.InitialListeners)
	if err != nil {
		return environmentIdentityBindings{}, err
	}
	runtime.InitialListeners = listeners
	parentContext := ctx
	if r.OpenBaoACK != nil {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 20*time.Minute)
		defer cancel()
		if err := verifyCreateManagedOpenBaoACKBootstrapEntry(ctx, cfg, runtime); err != nil {
			return environmentIdentityBindings{}, err
		}
	}
	if err := releaseManagedApplicationInterruptedLease(ctx, runtime); err != nil {
		return environmentIdentityBindings{}, err
	}
	if err := executeCreateManagedOpenBaoACK(ctx, cfg, runtime, apply); err != nil {
		return environmentIdentityBindings{}, err
	}
	if err := executeManagedApplicationPartialForward(ctx, runtime, apply); err != nil {
		return environmentIdentityBindings{}, err
	}
	if r.OpenBaoACK != nil {
		return completeEnvironmentCreateAfterApplicationOwners(ctx, cfg, p, release, store, operator, runtime, service, roots, intermediates, apply, parentContext)
	}
	return completeEnvironmentCreateAfterApplicationOwners(ctx, cfg, p, release, store, operator, runtime, service, roots, intermediates, apply)
}
