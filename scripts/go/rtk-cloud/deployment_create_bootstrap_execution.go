package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Bootstrap always continues the allocated cluster's original parent execution.
// In particular, a missing runtime cannot cause a second login, a replacement
// kubeconfig or an independently generated success/identity record.
func executeEnvironmentCreateBootstrap(ctx context.Context, cfg deploymentConfig, plan environmentCreateBootstrapPlan, release environmentRelease, store secretStore, operator map[string]string, apply environmentCreateBootstrapApply) (environmentIdentityBindings, error) {
	var empty environmentIdentityBindings
	runtime, ok := ctx.Value(environmentCreateBootstrapRuntimeKey{}).(environmentCreateBootstrapRuntime)
	if !ok || runtime.Execution == nil || runtime.PKIClient == nil || apply == nil || runtime.ExecutionSHA256 != runtime.Execution.Journal.PlanSHA256 || !environmentPreflightDigest.MatchString(runtime.ExecutionSHA256) || runtime.Execution.Store.Root != store.Root || runtime.Backend.Store.Root != store.Root || store.Environment != cfg.Environment || runtime.Execution.Journal.Environment != cfg.Environment || runtime.Execution.Journal.Stack != plan.Stack || runtime.PKIClient.Environment != cfg.Environment || runtime.PKIClient.Origin == nil || runtime.PKIClient.Origin.String() != runtime.APIOrigin || runtime.PKIClient.token == "" || !environmentBootstrapOwnerUUID.MatchString(runtime.PKIClient.OperatorID) {
		return empty, environmentCreateBootstrapBlocker("complete bootstrap requires the original selected parent execution, cluster backend and authenticated native Account Manager tunnel")
	}
	if err := runtime.Execution.Check(ctx); err != nil {
		return empty, err
	}
	if plan.AppRoutingMode != "internal-service" || plan.IngressAdmissionProfile != environmentCreateIngressAdmissionProfile || plan.Environment != cfg.Environment || plan.Stack != cfg.Values["CLOUD_STACK_NAME"] || release.Environment != cfg.Environment || release.Stack != plan.Stack || !immutableEnvironmentImage.MatchString(plan.Image) || !immutableEnvironmentImage.MatchString(plan.AccountManagerImage) || plan.Image != release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference || plan.AccountManagerImage != release.Images["LKE_ACCOUNT_MANAGER_IMAGE"].Reference || len(plan.Domains) != len(managedPKIDomains) || len(plan.Templates) != 8 || len(plan.Claims) == 0 {
		return empty, environmentCreateBootstrapBlocker("bootstrap recipe and exact formal release differ from the selected allocated environment")
	}
	for role, image := range release.Images {
		if !immutableEnvironmentImage.MatchString(image.Reference) || plan.Images[role] != image.Reference {
			return empty, environmentCreateBootstrapBlocker("bootstrap image roles changed after original create qualification")
		}
	}
	seen := map[string]bool{}
	for _, policy := range plan.Domains {
		if !slices.Contains(managedPKIDomains, policy.Domain) || seen[policy.Domain] || len(policy.Consumers) == 0 || len(policy.BundleConsumers) == 0 {
			return empty, environmentCreateBootstrapBlocker("complete bootstrap requires one exact concrete consumer policy for every managed trust domain")
		}
		seen[policy.Domain] = true
	}
	for name, hash := range plan.Templates {
		raw, err := readBoundedEnvironmentFile(filepath.Join(cfg.Workspace, "repos", "rtk_video_cloud", filepath.FromSlash(name)), false)
		if err != nil || environmentPreflightBytesSHA256(raw) != hash {
			return empty, environmentCreateBootstrapBlocker("qualified native bootstrap template changed before execution")
		}
	}
	// The foundation owns this very client; journal every authenticated registry
	// mutation with that parent, including calls made by the ceremony helpers.
	runtime.PKIClient.Execution = runtime.Execution
	if bindings, restored, err := restoreEnvironmentFreshProductBootstrap(ctx, cfg, plan, release, store, runtime, apply); err != nil || restored {
		return bindings, err
	}
	service, err := executeEnvironmentCreateServiceBootstrap(ctx, cfg, plan, store, operator, runtime, apply)
	if err != nil {
		return empty, err
	}
	runtime.ServiceCeremony = &service
	return completeEnvironmentCreateBootstrap(ctx, cfg, plan, release, store, operator, runtime, service, apply)
}

// Distribution runs the actual native consumers before either activation. The
// callback installs public ready lineage; only native registry receipts and the
// original registered operation can make that lineage active.
func executeEnvironmentCreateIssuerLineage(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, operator map[string]string, runtime environmentCreateBootstrapRuntime, policy environmentCreateIssuerPolicy, distribute func(environmentCreateRegisteredIssuer, ...environmentCreateRegisteredIssuer) error) (environmentCreateRegisteredIssuer, environmentCreateRegisteredIssuer, string, error) {
	var root, intermediate environmentCreateRegisteredIssuer
	if runtime.Execution == nil || runtime.PKIClient == nil || runtime.PKIClient.Execution != runtime.Execution || distribute == nil || policy.Domain == "device" || !slices.Contains(managedPKIDomains, policy.Domain) {
		return root, intermediate, "", environmentCreateBootstrapBlocker("original native offline-root execution and actual lineage distribution are required")
	}
	client := runtime.PKIClient
	if err := refreshEnvironmentCreateAuthentication(ctx, client, operator); err != nil {
		return root, intermediate, "", err
	}
	var rootOperation environmentCreateIssuerOperation
	var rootDocument map[string]any
	var err error
	if r := runtime.ServiceSessionRecovery; r != nil && r.AppTrustRecovery != nil && policy.Domain == "app" {
		rootOperation = r.AppTrustRecovery.Operation
		rootDocument, err = client.ReadIssuer(ctx, rootOperation.IssuerID)
		if err == nil && deploymentplan.Digest(rootDocument) != deploymentplan.Digest(r.AppTrustRecovery.Root.Document) {
			err = environmentCreateBootstrapBlocker("original READY App Root changed before trust continuation")
		}
	} else if r := runtime.ServiceSessionRecovery; r != nil && r.PostServiceResume && policy.Domain == "mqtt" && r.MQTTRoot != nil && r.MQTTOperation != nil {
		rootOperation = *r.MQTTOperation
		rootDocument, err = client.ReadIssuer(ctx, rootOperation.IssuerID)
		if err == nil && deploymentplan.Digest(rootDocument) != deploymentplan.Digest(r.MQTTRoot.Document) {
			err = environmentCreateBootstrapBlocker("original ready MQTT Root changed before trust recovery")
		}
	} else if r := runtime.ServiceSessionRecovery; r != nil && r.PostTransportResume && policy.Domain == "openbao_tls" && r.OpenBaoRoot != nil && r.OpenBaoOperation != nil {
		rootOperation = *r.OpenBaoOperation
		rootDocument, err = client.ReadIssuer(ctx, rootOperation.IssuerID)
		if err == nil && deploymentplan.Digest(rootDocument) != deploymentplan.Digest(r.OpenBaoRoot.Document) {
			err = environmentCreateBootstrapBlocker("original ready OpenBao TLS Root changed before recovery")
		}
	} else if policy.Domain == "service" && runtime.ServiceRootReady != nil && runtime.ServiceRootOperation != nil {
		rootOperation = *runtime.ServiceRootOperation
		rootDocument, err = client.ReadIssuer(ctx, rootOperation.IssuerID)
		if err == nil && deploymentplan.Digest(rootDocument) != deploymentplan.Digest(runtime.ServiceRootReady.Document) {
			err = environmentCreateBootstrapBlocker("original ready Service Root changed before trust recovery")
		}
	} else {
		rootOperation, rootDocument, err = executeEnvironmentCreateRootCeremony(ctx, client, cfg, p, store, runtime.ExecutionSHA256, policy.Domain)
	}
	if err != nil {
		return root, intermediate, "", err
	}
	root, err = environmentCreateRegisteredIssuerFromMap(rootDocument, "")
	if err != nil {
		return root, intermediate, "", err
	}
	if r := runtime.ServiceSessionRecovery; r != nil && r.OpenBaoIntermediateRejected && policy.Domain == "openbao_tls" {
		if root.Status != "active" {
			return root, intermediate, "", environmentCreateBootstrapBlocker("original OpenBao Root is no longer active")
		}
		crl, err := originalOpenBaoRootCRL(store, runtime.Execution, root)
		if err != nil {
			return root, intermediate, "", err
		}
		old := environmentCreateIssuerRequest{Environment: cfg.Environment, Domain: policy.Domain, Kind: "intermediate", ParentID: root.ID, ServerDNSNames: append(slices.Clone(policy.ServerDNSNames), "openbao"), ServiceClientIDs: policy.ServiceClientIDs}
		slices.Sort(old.ServerDNSNames)
		client.ReservationRecoveryReview = r.Review
		client.RejectedOpenBaoIntermediateRequest = &old
		op, doc, err := executeEnvironmentCreateIntermediateCeremony(ctx, client, cfg, p, store, runtime, rootDocument, policy)
		if err != nil {
			return root, intermediate, "", err
		}
		intermediate, err = environmentCreateRegisteredIssuerFromMap(doc, root.RootFingerprint)
		if err != nil {
			return root, intermediate, "", err
		}
		if err = distribute(root, intermediate); err != nil {
			return root, intermediate, "", err
		}
		if err = refreshEnvironmentCreateAuthentication(ctx, client, operator); err != nil {
			return root, intermediate, "", err
		}
		doc, err = activateEnvironmentCreateIssuer(ctx, client, cfg, store, runtime, op, doc, policy)
		if err != nil {
			return root, intermediate, "", err
		}
		intermediate, err = environmentCreateRegisteredIssuerFromMap(doc, root.RootFingerprint)
		return root, intermediate, crl, err
	}
	if err := distribute(root); err != nil {
		return root, intermediate, "", err
	}
	if err := refreshEnvironmentCreateAuthentication(ctx, client, operator); err != nil {
		return root, intermediate, "", err
	}
	rootDocument, err = activateEnvironmentCreateIssuer(ctx, client, cfg, store, runtime, rootOperation, rootDocument, policy)
	if err != nil {
		return root, intermediate, "", err
	}
	root, err = environmentCreateRegisteredIssuerFromMap(rootDocument, "")
	if err != nil {
		return root, intermediate, "", err
	}
	crlKey := "pki-initial-root-crl-" + policy.Domain
	intent, _ := json.Marshal(deploymentplan.Object{"issuer_id": root.ID, "root_sha256": root.RootFingerprint, "crl_number": "1"})
	if err := runtime.Execution.BeginEntry(crlKey, "bootstrap", intent, time.Now().UTC()); err != nil {
		return root, intermediate, "", err
	}
	rootCRLFile, err := executeEnvironmentCreateRootCRL(ctx, client, cfg, p, store, runtime, rootDocument)
	if err != nil {
		return root, intermediate, "", err
	}
	crlPath, err := store.safePath(rootCRLFile)
	if err != nil {
		return root, intermediate, "", err
	}
	crlPEM, err := readBoundedEnvironmentFile(crlPath, true)
	if err != nil {
		return root, intermediate, "", environmentCreateBootstrapBlocker("original native initial Root CRL cannot be observed after publication")
	}
	proof, _ := json.Marshal(deploymentplan.Object{"issuer_id": root.ID, "crl_file": rootCRLFile, "crl_pem_sha256": environmentPreflightBytesSHA256(crlPEM)})
	if err := runtime.Execution.RecordReceipt(crlKey, proof); err != nil {
		return root, intermediate, "", err
	}
	if err := runtime.Execution.CompleteEntry(crlKey, time.Now().UTC()); err != nil {
		return root, intermediate, "", err
	}
	if err := refreshEnvironmentCreateAuthentication(ctx, client, operator); err != nil {
		return root, intermediate, "", err
	}
	operation, document, err := executeEnvironmentCreateIntermediateCeremony(ctx, client, cfg, p, store, runtime, rootDocument, policy)
	if err != nil {
		return root, intermediate, "", err
	}
	intermediate, err = environmentCreateRegisteredIssuerFromMap(document, root.RootFingerprint)
	if err != nil {
		return root, intermediate, "", err
	}
	if err := distribute(root, intermediate); err != nil {
		return root, intermediate, "", err
	}
	if err := refreshEnvironmentCreateAuthentication(ctx, client, operator); err != nil {
		return root, intermediate, "", err
	}
	document, err = activateEnvironmentCreateIssuer(ctx, client, cfg, store, runtime, operation, document, policy)
	if err != nil {
		return root, intermediate, "", err
	}
	intermediate, err = environmentCreateRegisteredIssuerFromMap(document, root.RootFingerprint)
	return root, intermediate, rootCRLFile, err
}

// Core Service owners become managed before any other domain signer is exposed.
// The session remains active here: its other declared workload subjects still
// have to install their own native identities before authenticated finalization.
func executeEnvironmentCreateServiceBootstrap(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, operator map[string]string, runtime environmentCreateBootstrapRuntime, apply environmentCreateBootstrapApply) (environmentCreateServiceCeremony, error) {
	var s environmentCreateServiceCeremony
	var err error
	if r := runtime.ServiceSessionRecovery; r != nil && r.ManagedLateSession != nil {
		return executeManagedLateBootstrapSeed(ctx, cfg, p, store, runtime, apply)
	}
	if r := runtime.ServiceSessionRecovery; r != nil && r.CoreAccepted {
		if r.PostServiceResume {
			if r.ManagedTransportRecovery != nil {
				if r.PostServiceRenewal {
					return executeManagedSessionRecovery(ctx, cfg, p, store, runtime, apply)
				}
				return r.Ceremony, nil
			}
			if r.OpenBaoIntermediateRejected {
				if r.PostServiceRenewal {
					return executeUsedMQTTBootstrapRenewal(ctx, cfg, p, store, runtime, apply)
				}
				return r.Ceremony, nil
			}
			if r.InstallationForward {
				if err := executeCurrentInstallationForward(ctx, cfg, p, store, runtime, apply); err != nil {
					return r.Ceremony, err
				}
				return r.Ceremony, nil
			}
			if r.PostServiceRenewal {
				return executeUsedMQTTBootstrapRenewal(ctx, cfg, p, store, runtime, apply)
			}
			return r.Ceremony, nil
		}
		ceremony := r.Ceremony
		stage := "service-managed-core"
		if r.ApplicationForward && !r.IdentityLockForward {
			if err := apply(ctx, "service-trust-grants-recovery-"+r.Review, r.GrantDefinitions); err != nil {
				return s, err
			}
		}
		if r.OrderForward || r.ApplicationForward && !r.IdentityLockForward {
			subjects, err := environmentCreateBootstrapSubjects(cfg)
			if err != nil {
				return s, err
			}
			ceremony, err = createEnvironmentServiceSession(ctx, cfg, p, store, runtime, ceremony, subjects, apply)
			if err != nil {
				return s, err
			}
			stage = "service-managed-core-order-recovery-" + r.Review
			if r.ApplicationForward {
				stage = "service-managed-core-trust-recovery-" + r.Review
			}
		}
		if r.IdentityLockForward {
			stage = "service-managed-core-identity-recovery-" + r.Review
		}
		definitions, err := renderEnvironmentCreateManagedServiceDefinitions(ctx, cfg, p, runtime, ceremony)
		if err != nil {
			return s, err
		}
		if r.ApplicationForward {
			for _, d := range definitions {
				if d.Object["kind"] == "Deployment" {
					for _, v := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
						c := deploymentplan.Map(v)
						if c["name"] == "pki-controller" || c["name"] == "certissuer" {
							c["image"] = r.GrantImage.Reference
						}
					}
				}
			}
		}
		if err := apply(ctx, stage, definitions); err != nil {
			return s, err
		}
		if r.IdentityLockForward {
			if err := runtime.Execution.RecordReceipt(r.PendingManagedKey, mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": r.PendingManagedPlan.SHA256(), "renewed_session_id": ceremony.BootstrapSessionID})); err != nil {
				return s, err
			}
			if err := runtime.Execution.CompleteEntry(r.PendingManagedKey, time.Now().UTC()); err != nil {
				return s, err
			}
		}
		if r.OrderForward {
			if err := runtime.Execution.RecordReceipt("kubernetes/service-managed-core", mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_failed_plan_sha256": r.PartialCore.SHA256(), "renewed_session_id": ceremony.BootstrapSessionID})); err != nil {
				return s, err
			}
			if err := runtime.Execution.CompleteEntry("kubernetes/service-managed-core", time.Now().UTC()); err != nil {
				return s, err
			}
		}
		return ceremony, nil
	}
	if !runtime.InitialAuthSeparated {
		runtime, err = separateInitialCertIssuerAuthentication(ctx, cfg, runtime, apply)
		if err != nil {
			return s, err
		}
	}
	policy := findEnvironmentCreateDomainPolicy(p, "service")
	distribute := func(root environmentCreateRegisteredIssuer, additional ...environmentCreateRegisteredIssuer) error {
		definitions, err := renderEnvironmentCreateFirstTrustDefinitions(ctx, cfg, p, runtime, root, additional...)
		if err != nil {
			return err
		}
		stage := "service-root-trust"
		if len(additional) == 0 && runtime.ServiceRootTrustStage != "" {
			stage = runtime.ServiceRootTrustStage
		}
		if len(additional) > 0 {
			stage = "service-intermediate-trust"
		}
		if err := apply(ctx, stage, definitions); err != nil {
			return err
		}
		if len(additional) == 0 && runtime.ServiceRootTrustStage != "" {
			if runtime.ServiceRootTrustAdditionalParentKey != "" {
				if err := runtime.Execution.RecordReceipt(runtime.ServiceRootTrustAdditionalParentKey, mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + stage, "original_root_issuer_id": root.ID, "root_sha256": root.RootFingerprint})); err != nil {
					return err
				}
				if err := runtime.Execution.CompleteEntry(runtime.ServiceRootTrustAdditionalParentKey, time.Now().UTC()); err != nil {
					return err
				}
			}
			if err := runtime.Execution.RecordReceipt("kubernetes/service-root-trust", mustProviderRecoveryJSON(map[string]string{"actual_qualified_trust_stage": "kubernetes/" + stage, "original_root_issuer_id": root.ID, "root_sha256": root.RootFingerprint})); err != nil {
				return err
			}
			return runtime.Execution.CompleteEntry("kubernetes/service-root-trust", time.Now().UTC())
		}
		return nil
	}
	if runtime.ServiceSessionRecovery != nil {
		s = runtime.ServiceSessionRecovery.Ceremony
	} else {
		s.Root, s.Intermediate, s.RootCRLFile, err = executeEnvironmentCreateIssuerLineage(ctx, cfg, p, store, operator, runtime, policy, distribute)
	}
	if err != nil {
		return s, err
	}
	crlPath, err := store.safePath(s.RootCRLFile)
	if err != nil {
		return s, err
	}
	crlPEM, err := readBoundedEnvironmentFile(crlPath, true)
	if err != nil {
		return s, environmentCreateBootstrapBlocker("original signed Service Root CRL is unavailable")
	}
	s.RootCRLPEM, s.RootCRLSecret = string(crlPEM), "pki-create-service-root-crl"
	if runtime.ServiceSessionRecovery == nil {
		if err := apply(ctx, "service-active-trust", []deploymentplan.Definition{environmentCreateSecret(p.Stack+"-video-cloud", s.RootCRLSecret, map[string]string{"revocations.pem": s.RootCRLPEM})}); err != nil {
			return s, err
		}
		activeTrust, err := renderEnvironmentCreateFirstTrustDefinitions(ctx, cfg, p, runtime, s.Root, s.Intermediate)
		if err != nil {
			return s, err
		}
		if err := apply(ctx, "service-active-listeners", activeTrust); err != nil {
			return s, err
		}
	}
	subjects, err := environmentCreatePlannedServiceSubjects(cfg, p)
	if err != nil {
		return s, err
	}
	s, err = createEnvironmentServiceSession(ctx, cfg, p, store, runtime, s, subjects, apply)
	if err != nil {
		return s, err
	}
	policies := []string{"create-" + s.Intermediate.ID + "-signer", "create-" + s.Intermediate.ID + "-service-client"}
	if runtime.ServiceSessionRecovery == nil || !runtime.ServiceSessionRecovery.Renewal {
		if err := configureEnvironmentCreateOpenBaoRole(ctx, cfg, runtime, "certissuer-"+cfg.Environment, []string{"certissuer"}, policies); err != nil {
			return s, err
		}
	}
	for _, stage := range []struct {
		name   string
		render func(context.Context, deploymentConfig, environmentCreateBootstrapPlan, environmentCreateBootstrapRuntime, environmentCreateServiceCeremony) ([]deploymentplan.Definition, error)
	}{
		{"service-enrollment-issuer", renderEnvironmentCreateServiceIssuerDefinitions},
		{"service-issuer-server-enrollment", renderEnvironmentCreateServiceIssuerServerEnrollmentDefinitions},
		{"service-managed-enrollment-issuer", renderEnvironmentCreateManagedServiceIssuerDefinitions},
		{"service-core-enrollment", renderEnvironmentCreateServiceEnrollmentDefinitions},
		{"service-managed-core", renderEnvironmentCreateManagedServiceDefinitions},
	} {
		if err := runtime.Execution.Check(ctx); err != nil {
			return s, err
		}
		definitions, err := stage.render(ctx, cfg, p, runtime, s)
		if err != nil {
			return s, err
		}
		name := stage.name
		if runtime.ServiceSessionRecovery != nil && runtime.ServiceSessionRecovery.Renewal && (name == "service-enrollment-issuer" || name == "service-issuer-server-enrollment") {
			name = renewalStage(runtime, name)
		}
		if err := apply(ctx, name, definitions); err != nil {
			return s, err
		}
		if stage.name == "service-issuer-server-enrollment" && runtime.ServiceSessionRecovery != nil && runtime.ServiceSessionRecovery.Renewal {
			if err := runtime.Execution.RecordReceipt("kubernetes/service-issuer-server-enrollment", mustProviderRecoveryJSON(map[string]string{"actual_qualified_forward_stage": "kubernetes/" + name, "original_failed_plan_sha256": runtime.ServiceSessionRecovery.Original.SHA256(), "renewed_session_id": s.BootstrapSessionID})); err != nil {
				return s, err
			}
			if err := runtime.Execution.CompleteEntry("kubernetes/service-issuer-server-enrollment", time.Now().UTC()); err != nil {
				return s, err
			}
		}
	}
	return s, nil
}
