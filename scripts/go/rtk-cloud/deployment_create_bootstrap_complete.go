package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Complete the same admitted initial ceremony. Ready metadata is installed by
// actual native owners; only their signed registry receipts allow activation.
func completeEnvironmentCreateBootstrap(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, store secretStore, operator map[string]string, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, apply environmentCreateBootstrapApply) (environmentIdentityBindings, error) {
	var empty environmentIdentityBindings
	if runtime.Execution == nil || runtime.PKIClient == nil || runtime.ServiceCeremony == nil || apply == nil || runtime.Execution.Store.Root != store.Root {
		return empty, environmentCreateBootstrapBlocker("complete initial ceremony requires its original parent, authenticated registry and actual Service installation")
	}
	if r := runtime.ServiceSessionRecovery; r != nil && r.ManagedLateSession != nil {
		return completeManagedLateBootstrap(ctx, cfg, p, release, store, operator, runtime, service, apply)
	}
	if r := runtime.ServiceSessionRecovery; r != nil && r.ManagedCorePartial != nil {
		return completeManagedCorePartialBootstrap(ctx, cfg, p, release, store, operator, runtime, service, apply)
	}
	if r := runtime.ServiceSessionRecovery; r != nil && r.ManagedApplicationPartial != nil {
		return completeManagedApplicationPartialBootstrap(ctx, cfg, p, release, store, operator, runtime, service, apply)
	}
	enrollment, err := renderEnvironmentCreateApplicationEnrollment(cfg, p, runtime, service)
	if err != nil {
		return empty, err
	}
	stage := "application-client-enrollment"
	postService := runtime.ServiceSessionRecovery != nil && runtime.ServiceSessionRecovery.PostServiceResume
	if r := runtime.ServiceSessionRecovery; r != nil && r.ApplicationForward {
		stage = "application-client-enrollment-trust-recovery-" + r.Review
		// The refused first owner already persisted its pending request. A
		// forward executor may only consume that original state, never replace it.
		for _, d := range enrollment {
			if d.Object["kind"] == "Job" && strings.Contains(deploymentplan.String(deploymentplan.Map(d.Object["metadata"])["name"]), "factoryenroll-client-") {
				c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(d.Object)["containers"])[0])
				path := mqttFoundationEnv(c, "PKI_CLIENT_IDENTITY_STATE")
				if path != environmentCreateServiceState+"/private/client.json" {
					return empty, environmentCreateBootstrapBlocker("original Factory owner state path changed")
				}
				command := certIssuerObjectList(c["command"])
				if len(command) != 2 {
					return empty, environmentCreateBootstrapBlocker("original Factory client command changed")
				}
				c["command"] = []any{"/bin/sh", "-ec", "test -s " + path + " && grep -q '\"pending\"' " + path + " && exec /app/serviceidentity-bootstrap service:factory-enroll"}
			}
		}
	}
	if !postService {
		if err := apply(ctx, stage, enrollment); err != nil {
			return empty, err
		}
		if r := runtime.ServiceSessionRecovery; r != nil && r.ApplicationForward {
			if err := runtime.Execution.RecordReceipt("kubernetes/application-client-enrollment", mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": r.PartialCore.SHA256(), "renewed_session_id": service.BootstrapSessionID})); err != nil {
				return empty, err
			}
			if err := runtime.Execution.CompleteEntry("kubernetes/application-client-enrollment", time.Now().UTC()); err != nil {
				return empty, err
			}
		}
	}
	// Install the final Service subjects while the seed is still valid. The
	// remaining trust-domain ceremonies can finish after this short seed expires.
	clients, err := renderEnvironmentCreateTransportClientDefinitions(ctx, cfg, p, runtime, service, nil, nil)
	if err != nil {
		return empty, err
	}
	if runtime.ServiceSessionRecovery == nil || runtime.ServiceSessionRecovery.DeviceTrustRecovery == nil && (!runtime.ServiceSessionRecovery.OpenBaoIntermediateRejected || runtime.ServiceSessionRecovery.PostServiceRenewal) {
		transportStage := "transport-client-enrollment"
		if runtime.ServiceSessionRecovery != nil && runtime.ServiceSessionRecovery.OpenBaoIntermediateRejected {
			transportStage += "-renewal-" + runtime.ServiceSessionRecovery.Review
		}
		if err := apply(ctx, transportStage, clients); err != nil {
			return empty, err
		}
	}
	applications, err := renderEnvironmentCreateApplicationOwners(ctx, cfg, p, release, store, operator, service)
	if err != nil {
		return empty, err
	}
	distributeMQTT := func(root environmentCreateRegisteredIssuer, additional ...environmentCreateRegisteredIssuer) error {
		updated, err := configureEnvironmentCreateMQTTConsumers(p, service, root, additional, applications)
		if err != nil {
			return err
		}
		applications = updated
		stage := "mqtt-root-trust"
		if len(additional) != 0 {
			stage = "mqtt-intermediate-trust"
		}
		if postService && len(additional) == 0 {
			stage = "mqtt-root-trust-order-recovery-" + runtime.ServiceSessionRecovery.Review
		}
		if err := apply(ctx, stage, environmentCreateApplicationDefinitionsFor(applications, "video-cloud-api", "video-cloud-logingester")); err != nil {
			return err
		}
		if postService && len(additional) == 0 {
			r := runtime.ServiceSessionRecovery
			if err := runtime.Execution.RecordReceipt("kubernetes/mqtt-root-trust", mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": r.MQTTPendingPlan.SHA256(), "original_root_issuer_id": root.ID})); err != nil {
				return err
			}
			if r.MQTTForwardKey != "" {
				if err := runtime.Execution.RecordReceipt(r.MQTTForwardKey, mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": r.MQTTPendingPlan.SHA256()})); err != nil {
					return err
				}
				if err := runtime.Execution.CompleteEntry(r.MQTTForwardKey, time.Now().UTC()); err != nil {
					return err
				}
			}
			return runtime.Execution.CompleteEntry("kubernetes/mqtt-root-trust", time.Now().UTC())
		}
		return nil
	}
	roots, intermediates, _, err := executeEnvironmentCreateTransportLineages(ctx, cfg, p, store, operator, runtime, service, distributeMQTT, apply)
	if err != nil {
		return empty, err
	}
	roots["service"], intermediates["service"] = service.Root, service.Intermediate
	// Application enrollment has now installed the broker's managed key. Its
	// outbound Service CRL policy must be admitted before Device/App startup.
	if r := runtime.ServiceSessionRecovery; r == nil || r.DeviceTrustRecovery == nil {
		policy, err := renderEnvironmentCreatePreparedOpenBaoConsumers(ctx, cfg, p, runtime, service, roots["openbao_tls"], intermediates["openbao_tls"])
		if err != nil {
			return empty, err
		}
		policy, err = configureCreateBrokerServiceConsumer(p, policy)
		if err != nil {
			return empty, err
		}
		var controller []deploymentplan.Definition
		for _, d := range policy {
			if deploymentplan.ID(d.Object) == "Deployment/"+p.Stack+"-video-cloud/pki-controller" {
				d.Dependencies = nil
				controller = append(controller, d)
			}
		}
		if err := apply(ctx, "broker-service-trust-policy", controller); err != nil {
			return empty, err
		}
	}
	// All subsequent maintained managed-host recipes preserve that policy.
	runtime.InitialListeners, err = configureCreateBrokerServiceConsumer(p, runtime.InitialListeners)
	if err != nil {
		return empty, err
	}
	var device environmentCreateRegisteredIssuer
	if r := runtime.ServiceSessionRecovery; r != nil && r.AppTrustRecovery != nil {
		device = r.AppTrustRecovery.Device
		applications, err = configureEnvironmentCreateDeviceConsumers(cfg, p, r.AppTrustRecovery.DeviceReady, applications)
	} else {
		device, err = executeEnvironmentCreateDeviceRoot(ctx, cfg, p, store, operator, runtime, apply, func(root environmentCreateRegisteredIssuer) error {
			updated, err := configureEnvironmentCreateDeviceConsumers(cfg, p, root, applications)
			if err != nil {
				return err
			}
			applications = updated
			if r := runtime.ServiceSessionRecovery; r != nil && r.DeviceTrustRecovery != nil {
				return executeDeviceTrustForward(ctx, runtime, apply)
			}
			return apply(ctx, "device-root-trust", environmentCreateApplicationDefinitionsFor(applications, "video-cloud-api-pki", "mqtt-pki"))
		})
	}
	if err != nil {
		return empty, err
	}
	roots["device"] = device
	var appRoot, appIntermediate environmentCreateRegisteredIssuer
	if r := runtime.ServiceSessionRecovery; r != nil && r.ManagedTransportRecovery != nil {
		appRoot, appIntermediate = r.ManagedTransportRecovery.AppRoot, r.ManagedTransportRecovery.AppIntermediate
		applications, err = managedRecoveryApplicationConsumers(cfg, p, r, applications)
	} else {
		appRoot, appIntermediate, _, err = executeEnvironmentCreateIssuerLineage(ctx, cfg, p, store, operator, runtime, findEnvironmentCreateDomainPolicy(p, "app"), func(root environmentCreateRegisteredIssuer, additional ...environmentCreateRegisteredIssuer) error {
			updated, err := configureEnvironmentCreateAppConsumers(p, root, additional, applications)
			if err != nil {
				return err
			}
			applications = updated
			if r := runtime.ServiceSessionRecovery; r != nil && r.AppTrustRecovery != nil && len(additional) == 0 {
				return executeAppTrustForward(ctx, runtime, apply)
			}
			stage := "app-root-trust"
			if len(additional) != 0 {
				stage = "app-intermediate-trust"
			}
			return apply(ctx, stage, environmentCreateApplicationDefinitionsFor(applications, "video-cloud-api-app-pki", "mqtt-pki"))
		})
		if err != nil {
			return empty, err
		}
	}
	if err != nil {
		return empty, err
	}
	roots["app"], intermediates["app"] = appRoot, appIntermediate
	if r := runtime.ServiceSessionRecovery; r != nil && r.ManagedTransportRecovery != nil {
		if err := verifyEnvironmentCreateRegisteredIssuers(store, roots, intermediates, nil); err != nil {
			return empty, err
		}
	} else if err := saveEnvironmentCreateRegisteredIssuers(store, roots, intermediates); err != nil {
		return empty, err
	}
	if err := configureEnvironmentCreateCompleteSignerRole(ctx, cfg, runtime, roots, intermediates); err != nil {
		return empty, err
	}
	// The Device/App native ACL ceremonies consume the original unsealed
	// bootstrap transport. Convert that host only after those calls complete.
	if err := executeEnvironmentCreateManagedTransports(ctx, cfg, p, release, runtime, service, roots, intermediates, apply); err != nil {
		return empty, err
	}
	if err := publishEnvironmentCreateApplicationServers(ctx, cfg, p, runtime, service, apply); err != nil {
		return empty, err
	}
	applications, err = finalizeEnvironmentCreateApplicationDefinitions(ctx, cfg, p, release, runtime, service, roots, intermediates, applications)
	if err != nil {
		return empty, err
	}
	if err := apply(ctx, "managed-application-owners", applications); err != nil {
		return empty, err
	}
	return completeEnvironmentCreateAfterApplicationOwners(ctx, cfg, p, release, store, operator, runtime, service, roots, intermediates, apply)
}

func completeEnvironmentCreateAfterApplicationOwners(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, store secretStore, operator map[string]string, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony, roots, intermediates map[string]environmentCreateRegisteredIssuer, apply environmentCreateBootstrapApply, continuationContexts ...context.Context) (environmentIdentityBindings, error) {
	var empty environmentIdentityBindings
	if len(continuationContexts) > 1 || len(continuationContexts) == 1 && continuationContexts[0] == nil {
		return empty, createOpenBaoACKBlock("bounded-finalization-continuation")
	}
	var core []deploymentplan.Definition
	var err error
	if r := runtime.ServiceSessionRecovery; r != nil && r.ManagedLateSession != nil {
		if err := verifyManagedLateAcceptedOwners(ctx, cfg, runtime); err != nil {
			return empty, err
		}
		core, err = managedLateCurrentCoreDefinitions(runtime)
		if err != nil {
			return empty, err
		}
	} else {
		if r := runtime.ServiceSessionRecovery; r != nil && r.ManagedCorePartial != nil {
			if err := verifyManagedCorePartialCompletedStages(ctx, cfg, runtime); err != nil {
				return empty, err
			}
		} else {
			if err := publishEnvironmentCreateAppRouting(ctx, cfg, p, runtime, apply); err != nil {
				return empty, err
			}
		}
		core, err = renderEnvironmentCreateManagedServiceDefinitions(ctx, cfg, p, runtime, service)
		if err != nil {
			return empty, err
		}
		core, err = applyEnvironmentCreateManagedOpenBaoConsumers(p, service, roots, intermediates, core)
		if err != nil {
			return empty, err
		}
		core, err = configureEnvironmentCreateFinalIssuer(p, roots, intermediates, core)
		if err != nil {
			return empty, err
		}
		if r := runtime.ServiceSessionRecovery; r == nil || r.ManagedCorePartial == nil {
			if err := apply(ctx, "managed-complete-issuer", core); err != nil {
				return empty, err
			}
		}
	}
	for _, domain := range managedPKIDomains {
		policy := findEnvironmentCreateDomainPolicy(p, domain)
		if err := waitEnvironmentCreateIssuerAcks(ctx, cfg, runtime, roots[domain], policy.BundleConsumers); err != nil {
			return empty, err
		}
		if intermediate, exists := intermediates[domain]; exists {
			if err := waitEnvironmentCreateIssuerAcks(ctx, cfg, runtime, intermediate, policy.BundleConsumers); err != nil {
				return empty, err
			}
		}
	}
	freshProducts := len(p.ProductIdentities) > 0 && runtime.ServiceSessionRecovery == nil
	if freshProducts {
		if err := executeEnvironmentCreateFreshProductIdentities(ctx, cfg, p, release, store, runtime, service, apply); err != nil {
			return empty, err
		}
	} else if r := runtime.ServiceSessionRecovery; r != nil && r.completedTail != nil {
		ctx, err = managedLateCompletedTailContext(ctx, runtime)
		if err != nil {
			return empty, err
		}
	} else {
		finalizeCtx := ctx
		cancelFinalization := func() {}
		if r := runtime.ServiceSessionRecovery; r != nil && r.ManagedLateSession != nil {
			if err := verifyManagedLateFinalizerBudget(ctx); err != nil {
				return empty, err
			}
		}
		if r := runtime.ServiceSessionRecovery; r != nil && r.OpenBaoACK != nil {
			if managedLatePostNativeCleanupRequested(runtime) {
				if err := verifyManagedLatePostNativeCleanupRuntime(ctx, cfg, store, runtime); err != nil {
					return empty, err
				}
			} else {
				finalizeCtx, cancelFinalization = context.WithTimeout(ctx, 15*time.Minute)
			}
		}
		finalizationErr := func() error {
			defer cancelFinalization()
			// The terminal branch performs its single fresh native authority check
			// inside the finalizer immediately below, before any mutation.
			if !managedLateTerminalFinalizerRequested(runtime) {
				if err := completeCreateManagedOpenBaoACK(finalizeCtx, cfg, runtime, p, release); err != nil {
					return err
				}
			}
			return finalizeEnvironmentCreateServiceSession(finalizeCtx, runtime.PKIClient, cfg, p, store, runtime, service, operator)
		}()
		if finalizationErr != nil {
			return empty, finalizationErr
		}
		if r := runtime.ServiceSessionRecovery; r != nil && r.ManagedLateSession != nil {
			ctx, err = managedLateContextAfterFinalization(ctx, runtime)
			if err != nil {
				return empty, err
			}
		}
	}
	if len(continuationContexts) == 1 {
		ctx = continuationContexts[0]
		if err := ctx.Err(); err != nil {
			return empty, err
		}
		if r := runtime.ServiceSessionRecovery; r != nil && r.completedTail != nil {
			ctx, err = managedLateCompletedTailContext(ctx, runtime)
			if err != nil {
				return empty, err
			}
		}
	}
	if cfg.Values["ACCOUNT_MANAGER_SERVICE_REGISTRATION_ENABLED"] == "true" && (runtime.ServiceSessionRecovery == nil || runtime.ServiceSessionRecovery.normalTail == nil) {
		namespace := p.Stack + "-account-manager"
		serverName := "account-manager." + namespace + ".svc.cluster.local"
		material, err := readEnvironmentCreateOwnedServer(ctx, cfg, p, runtime, service, namespace, "account-manager", "account-manager", "account-manager-service-state", environmentCreateServiceState, environmentCreateServiceState+"/private/client.json", environmentCreateServiceState+"/private/server.json", serverName, apply)
		if err != nil {
			return empty, err
		}
		if err := refreshEnvironmentCreateAuthentication(ctx, runtime.PKIClient, operator); err != nil {
			return empty, err
		}
		var current struct {
			Issuer string `json:"issuer_id"`
			PEM    string `json:"crl_pem"`
		}
		if err := runtime.PKIClient.call(ctx, "GET", "/v1/platform/pki/issuers/"+service.Intermediate.ID+"/crl", "", nil, &current); err != nil {
			return empty, err
		}
		if current.Issuer != service.Intermediate.ID || current.PEM == "" {
			return empty, environmentCreateBootstrapBlocker("finalized original Service revocation CRL cannot be read for registration")
		}
		registration, err := renderEnvironmentCreateAccountRegistration(cfg, p, service, material.Certificate, material.Key, current.PEM, core)
		if err != nil {
			return empty, err
		}
		account, err := environmentCreateAccountRegistrationStage(p, registration)
		if err != nil {
			return empty, err
		}
		if err := apply(ctx, "account-manager-registration", account); err != nil {
			return empty, err
		}
	}
	if !freshProducts && (runtime.ServiceSessionRecovery == nil || runtime.ServiceSessionRecovery.normalTail == nil) {
		if err := retireEnvironmentCreateOwnedDeployment(ctx, cfg, runtime, service.CustodyReader, apply); err != nil {
			return empty, err
		}
	}
	if err := executeEnvironmentCreateIngressAdmission(ctx, cfg, p, release, store, runtime, apply); err != nil {
		return empty, err
	}
	live, err := runtime.Backend.Inventory(ctx, p.Stack)
	if err != nil {
		return empty, environmentCreateBootstrapBlocker("completed native bootstrap ownership inventory cannot be observed")
	}
	commands, err := managedLateNormalTailReleaseCommands(ctx, cfg, store, runtime)
	if err != nil {
		return empty, err
	}
	release, err = effectiveCreateOpenBaoACKRelease(ctx, cfg, store, runtime.ExecutionSHA256, release, commands...)
	if err != nil {
		return empty, err
	}
	bindings, err := observeEnvironmentBindings(cfg.Environment, release, live)
	if err != nil {
		return empty, err
	}
	if err := applyManagedLateBootstrapBindings(ctx, cfg, runtime, &bindings, live); err != nil {
		return empty, err
	}
	if err := applyEnvironmentFreshProductBindings(ctx, cfg, runtime.Execution, &bindings); err != nil {
		return empty, err
	}
	if err := validateEnvironmentIdentityBindings(bindings, cfg.Environment); err != nil {
		return empty, err
	}
	if runtime.Execution != nil && managedLateRedisPrePauseSelected(runtime.Execution.Journal) {
		bindings, err = readManagedLateRedisPrePauseBindings(ctx, store, runtime.Execution, bindings)
		if err != nil {
			return empty, err
		}
	}
	path := filepath.Join("deployment", "create", runtime.ExecutionSHA256, "bootstrap", "bindings.json")
	if len(commands) == 1 {
		_, err = writeManagedLateNormalTailBindings(ctx, store, runtime.Execution, bindings)
	} else {
		_, err = writeEnvironmentCreateBootstrapJSON(store, path, bindings)
	}
	if err != nil {
		return empty, err
	}
	if err := executeManagedLateRedisDurable(ctx, cfg, store, runtime, apply); err != nil {
		return empty, err
	}
	if runtime.ServiceSessionRecovery != nil && runtime.ServiceSessionRecovery.redisDurable != nil {
		live, err = runtime.Backend.Inventory(ctx, p.Stack)
		if err != nil {
			return empty, err
		}
		bindings, err = observeEnvironmentBindings(cfg.Environment, release, live)
		if err != nil {
			return empty, err
		}
		if err := applyManagedLateBootstrapBindings(ctx, cfg, runtime, &bindings, live); err != nil {
			return empty, err
		}
		if err := validateEnvironmentIdentityBindings(bindings, cfg.Environment); err != nil {
			return empty, err
		}
		if _, err := writeEnvironmentCreateBootstrapJSON(store, filepath.Join(managedLateRedisDurableBase(runtime.ExecutionSHA256), "bindings.json"), bindings); err != nil {
			return empty, err
		}
	}
	return bindings, nil
}

func configureEnvironmentCreateCompleteSignerRole(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, roots, intermediates map[string]environmentCreateRegisteredIssuer) error {
	policies := []string{"create-device-signer"}
	for _, domain := range []string{"service", "mqtt", "openbao_tls", "app"} {
		issuer := intermediates[domain]
		if issuer.Status != "active" || issuer.RootFingerprint != roots[domain].RootFingerprint || issuer.Reference == "" {
			return environmentCreateBootstrapBlocker("final native issuer requires all original active signing mounts")
		}
		policies = append(policies, "create-"+issuer.ID+"-signer")
		if domain == "service" {
			policies = append(policies, "create-"+issuer.ID+"-service-client")
		}
	}
	slices.Sort(policies)
	if managedTransportOpenBaoPullRepair(runtime.ServiceSessionRecovery) {
		return verifyManagedTransportRecordedOpenBaoRole(runtime, cfg, "certissuer-"+cfg.Environment, []string{"certissuer"}, policies)
	}
	return configureEnvironmentCreateOpenBaoRole(ctx, cfg, runtime, "certissuer-"+cfg.Environment, []string{"certissuer"}, policies)
}

func configureEnvironmentCreateFinalIssuer(p environmentCreateBootstrapPlan, roots, intermediates map[string]environmentCreateRegisteredIssuer, definitions []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	for _, domain := range managedPKIDomains {
		root := roots[domain]
		if root.Environment != p.Environment || root.Domain != domain || root.Kind != "root" || root.Status != "active" || !environmentPreflightDigest.MatchString(root.RootFingerprint) || root.ID == "" {
			return nil, environmentCreateBootstrapBlocker("final issuer/controller requires every original active registered Root")
		}
		if domain != "device" {
			issuer := intermediates[domain]
			if issuer.Environment != p.Environment || issuer.Domain != domain || issuer.Status != "active" || issuer.RootFingerprint != root.RootFingerprint || issuer.Reference == "" {
				return nil, environmentCreateBootstrapBlocker("final issuer signing mount differs from its original active lineage")
			}
		}
	}
	servicePolicy := findEnvironmentCreateDomainPolicy(p, "service")
	var gatewaySubjects []string
	for _, subject := range servicePolicy.ServiceClientIDs {
		if subject != "service:deployment-bootstrap" {
			gatewaySubjects = append(gatewaySubjects, regexp.QuoteMeta(subject))
		}
	}
	if len(gatewaySubjects) == 0 || len(servicePolicy.ServerDNSNames) == 0 {
		return nil, environmentCreateBootstrapBlocker("final issuer lacks its qualified exact native Service caller and DNS policy")
	}
	var out []deploymentplan.Definition
	seen := map[string]bool{}
	for _, definition := range definitions {
		definition.Object = deploymentplan.Clone(definition.Object)
		object := definition.Object
		name := deploymentplan.String(deploymentplan.Map(object["metadata"])["name"])
		if object["kind"] != "Deployment" || name != "certissuer" && name != "pki-controller" {
			out = append(out, definition)
			continue
		}
		if seen[name] || deploymentplan.Map(object["metadata"])["namespace"] != p.Stack+"-video-cloud" {
			return nil, environmentCreateBootstrapBlocker("final issuer/controller contains a duplicate or foreign native owner")
		}
		seen[name] = true
		container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(object)["containers"])[0])
		settings := map[string]string{"PKI_DEVICE_ROOT_ID": roots["device"].ID, "PKI_DEVICE_ROOT_SHA256": roots["device"].RootFingerprint, "PKI_FIXED_DEVICE_ROOT_TRUST": "false", "PKI_APP_CRL_WORKER_ENABLED": "true", "PKI_SERVER_CRL_DOMAIN": "service,mqtt,openbao_tls"}
		if name == "pki-controller" {
			settings["PKI_FIXED_DEVICE_ROOT_TRUST"] = "true"
		}
		if name == "certissuer" {
			pod := environmentPKIPod(object)
			session := mqttFoundationEnv(container, "PKI_BOOTSTRAP_SESSION_ID")
			if !environmentBootstrapOwnerUUID.MatchString(session) {
				return nil, environmentCreateBootstrapBlocker("final issuer cannot retire seed references without its original session")
			}
			fields, err := environmentPKINamedFields(container["env"])
			if err != nil {
				return nil, environmentCreateBootstrapBlocker("final issuer seed credential settings are ambiguous")
			}
			for field, key := range map[string]string{"CERT_ISSUER_HOST_RENEWAL_CLIENT_CERT": "tls.crt", "CERT_ISSUER_HOST_RENEWAL_CLIENT_KEY": "tls.key"} {
				setting := fields[field]
				if setting == nil || setting["value"] == "" && setting["valueFrom"] == nil {
					continue
				}
				file, err := environmentIssuerSecretFile(object, container, "service-bootstrap-"+session, key)
				if err != nil || setting["valueFrom"] != nil || setting["value"] != file {
					return nil, environmentCreateBootstrapBlocker("final issuer renewal seed credential differs from its original session Secret")
				}
			}
			container["env"] = environmentCreateServiceWithoutEnv(container["env"], "CERT_ISSUER_HOST_RENEWAL_CLIENT_CERT", "CERT_ISSUER_HOST_RENEWAL_CLIENT_KEY")
			var volumes []any
			retiredMounts := map[string]bool{}
			for _, raw := range certIssuerObjectList(pod["volumes"]) {
				volume := deploymentplan.Map(raw)
				volumeName := deploymentplan.String(volume["name"])
				secret := deploymentplan.Map(volume["secret"])["secretName"]
				if volumeName == "bootstrap" && secret != "service-bootstrap-"+session {
					return nil, environmentCreateBootstrapBlocker("final issuer seed reference differs from its original native session")
				}
				if secret == "service-bootstrap-"+session {
					retiredMounts[volumeName] = true
					continue
				}
				volumes = append(volumes, raw)
			}
			pod["volumes"] = volumes
			for _, group := range []string{"containers", "initContainers"} {
				for _, raw := range certIssuerObjectList(pod[group]) {
					consumer := deploymentplan.Map(raw)
					if len(retiredMounts) == 0 || consumer["volumeMounts"] == nil {
						continue
					}
					var mounts []any
					for _, value := range certIssuerObjectList(consumer["volumeMounts"]) {
						if !retiredMounts[deploymentplan.String(deploymentplan.Map(value)["name"])] {
							mounts = append(mounts, value)
						}
					}
					consumer["volumeMounts"] = mounts
				}
			}
			var initializers []any
			for _, value := range certIssuerObjectList(pod["initContainers"]) {
				initializer := deploymentplan.Map(value)
				if initializer["name"] != "pki-private-permissions" {
					initializers = append(initializers, value)
					continue
				}
				if deploymentplan.Digest(initializer) != deploymentplan.Digest(environmentCreateServicePrivateInit(p.Image, false)) {
					return nil, environmentCreateBootstrapBlocker("final native issuer permission initializer differs from its completed original recipe")
				}
			}
			// Enrollment already created this owner's private directories. The
			// final host uses the same claim and UID; avoid recursive fsGroup
			// permission widening when that retained volume is mounted again.
			pod["initContainers"] = initializers
			deploymentplan.Map(pod["securityContext"])["fsGroupChangePolicy"] = "OnRootMismatch"
			if len(p.ProductIdentities) == 0 {
				container["env"] = environmentCreateServiceWithoutEnv(container["env"], "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_CALLER", "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SUBJECTS", "CERT_ISSUER_SERVICE_CLIENT_BOOTSTRAP_SESSION_ID")
			}
			settings = map[string]string{"CERT_ISSUER_CREATE_SERVICE_ENROLLMENT_ONLY": "false", "CERT_ISSUER_REGISTRY_ONLY_ENABLED": "true", "CERT_ISSUER_SERVICE_CLIENT_PROVISIONER_CN_PATTERN": "^$", "CERT_ISSUER_PRODUCT_PKI_ENABLED": "true", "CERT_ISSUER_APP_PKI_ENABLED": "true", "CERT_ISSUER_SIGNER_PROVIDER": "openbao", "CERT_ISSUER_OPENBAO_PKI_MOUNT": intermediates["service"].Reference, "CERT_ISSUER_APP_SIGNER_PROVIDER": "openbao", "CERT_ISSUER_APP_OPENBAO_PKI_MOUNT": intermediates["app"].Reference, "CERT_ISSUER_APP_OPENBAO_PKI_ROLE": "app", "CERT_ISSUER_FACTORY_CLIENT_CN_PATTERN": "^service:factory-enroll$", "CERT_ISSUER_APP_CLIENT_CN_PATTERN": "^service:account-manager$", "CERT_ISSUER_SERVICE_CLIENT_ROOT_SHA256": roots["service"].RootFingerprint, "CERT_ISSUER_SERVICE_CLIENT_ISSUER_ROOT_SHA256": roots["service"].RootFingerprint, "CERT_ISSUER_SERVICE_CLIENT_VERIFY_ROOT_SHA256": roots["service"].RootFingerprint, "CERT_ISSUER_SERVER_PKI_DOMAIN": "service", "CERT_ISSUER_SERVER_PKI_ROOT_SHA256": roots["service"].RootFingerprint, "CERT_ISSUER_SERVER_PKI_ISSUER_ROOT_SHA256": roots["service"].RootFingerprint, "CERT_ISSUER_SERVER_PKI_VERIFY_ROOT_SHA256": roots["service"].RootFingerprint, "CERT_ISSUER_GATEWAY_CLIENT_CN_PATTERN": "^(?:" + strings.Join(gatewaySubjects, "|") + ")$", "CERT_ISSUER_GATEWAY_DNS_NAMES": strings.Join(servicePolicy.ServerDNSNames, ","), "CERT_ISSUER_MQTT_SERVER_PKI_ROOT_SHA256": roots["mqtt"].RootFingerprint, "CERT_ISSUER_MQTT_SERVER_CLIENT_CN_PATTERN": "^service:emqx-pki$", "CERT_ISSUER_MQTT_SERVER_DNS_NAMES": strings.Join(findEnvironmentCreateDomainPolicy(p, "mqtt").ServerDNSNames, ","), "CERT_ISSUER_OPENBAO_HOST_PKI_ROOT_SHA256": roots["openbao_tls"].RootFingerprint, "CERT_ISSUER_OPENBAO_HOST_PKI_VERIFY_ROOT_SHA256": roots["openbao_tls"].RootFingerprint, "CERT_ISSUER_OPENBAO_HOST_CLIENT_CN_PATTERN": "^service:openbao$", "CERT_ISSUER_OPENBAO_HOST_DNS_NAMES": strings.Join(findEnvironmentCreateDomainPolicy(p, "openbao_tls").ServerDNSNames, ",")}
		}
		container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(settings))
		if name == "pki-controller" {
			updated, err := configureCreateTransportServiceConsumerController(p, object)
			if err != nil {
				return nil, err
			}
			definition.Object = updated
		}
		if name == "certissuer" {
			// Product admission shares the maintained factory JWT authority;
			// this exact Secret was installed by the application stage.
			container["env"] = mergeEnvironmentNamedFields(container["env"], []any{
				deploymentplan.Object{"name": "CERT_ISSUER_PRODUCTION_JWT_SECRET", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "factoryenroll-runtime", "key": "FACTORY_ENROLL_PRODUCTION_JWT_SECRET"}}},
				deploymentplan.Object{"name": "CERT_ISSUER_PRODUCTION_JWT_AUDIENCE", "valueFrom": deploymentplan.Object{"secretKeyRef": deploymentplan.Object{"name": "factoryenroll-runtime", "key": "FACTORY_ENROLL_PRODUCTION_JWT_AUDIENCE"}}},
			})
		}
		out = append(out, definition)
	}
	if !seen["certissuer"] || !seen["pki-controller"] {
		return nil, environmentCreateBootstrapBlocker("both original native issuer and controller owners are required for final runtime")
	}
	return out, nil
}

func retireEnvironmentCreateOwnedDeployment(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, name string, apply environmentCreateBootstrapApply) error {
	return retireEnvironmentCreateOwnedDeploymentInNamespace(ctx, cfg, runtime, cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud", name, apply)
}

func retireEnvironmentCreateOwnedDeploymentInNamespace(ctx context.Context, cfg deploymentConfig, runtime environmentCreateBootstrapRuntime, namespace, name string, apply environmentCreateBootstrapApply) error {
	if !secretEnvironmentPattern.MatchString(name) || runtime.Execution == nil || apply == nil {
		return environmentCreateBootstrapBlocker("temporary bootstrap owner cannot be retired without its original execution")
	}
	if namespace != cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud" && namespace != cfg.Values["CLOUD_STACK_NAME"]+"-account-manager" {
		return environmentCreateBootstrapBlocker("temporary bootstrap owner is outside its declared original namespace")
	}
	id := "Deployment/" + namespace + "/" + name
	actual, err := runtime.Backend.Read(ctx, id)
	if err != nil || actual == nil {
		return environmentCreateBootstrapBlocker("original temporary bootstrap Deployment cannot be read for retirement")
	}
	meta := deploymentplan.Map(actual["metadata"])
	if deploymentplan.Map(meta["labels"])["rtk.realtek.com/stack"] != cfg.Values["CLOUD_STACK_NAME"] || deploymentplan.String(meta["uid"]) == "" || meta["deletionTimestamp"] != nil {
		return environmentCreateBootstrapBlocker("temporary bootstrap Deployment changed its original owner before retirement")
	}
	intent, _ := json.Marshal(deploymentplan.Object{"resource_id": id, "uid": meta["uid"]})
	key := "retire-" + name
	retained, err := verifyManagedTransportPendingExportRetirement(runtime, id, deploymentplan.String(meta["uid"]))
	if err != nil {
		return err
	}
	if !retained {
		if err := runtime.Execution.BeginEntry(key, "bootstrap", intent, time.Now().UTC()); err != nil {
			return err
		}
	}
	if err := apply(ctx, key, []deploymentplan.Definition{{Phase: "retirement", Object: actual, Retire: true}}); err != nil {
		return err
	}
	readback, err := runtime.Backend.Read(ctx, id)
	if err != nil || readback != nil {
		return environmentCreateBootstrapBlocker("temporary bootstrap Deployment retirement outcome is not verified")
	}
	proof, _ := json.Marshal(deploymentplan.Object{"resource_id": id, "uid": meta["uid"], "absent": true})
	if err := runtime.Execution.RecordReceipt(key, proof); err != nil {
		return err
	}
	return runtime.Execution.CompleteEntry(key, time.Now().UTC())
}
