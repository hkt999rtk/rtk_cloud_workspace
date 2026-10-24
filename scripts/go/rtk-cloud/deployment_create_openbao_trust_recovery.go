package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"time"
)

func loadAdmittedCreateRecovery(store secretStore, e *environmentCreateExecution, review string) (createFoundationRecoveryArtifact, environmentCreateJournal, error) {
	var a createFoundationRecoveryArtifact
	var before environmentCreateJournal
	bad := func() (createFoundationRecoveryArtifact, environmentCreateJournal, error) {
		return a, before, createProviderRecoveryBlock("original qualified recovery, admission and source journal must agree")
	}
	base := filepath.Join(store.Root, "deployment/create/openbao-recovery", review)
	if !environmentPreflightDigest.MatchString(review) || readExecutionJSON(filepath.Join(base, "qualification.json"), &a) != nil || deploymentplan.Digest(a.Plan) != review || a.Plan.ParentSHA != e.Journal.PlanSHA256 || a.Qualification.PlanSHA256 != review || a.Qualification.Decision != "GO" || len(a.Qualification.Gates) != 6 || readExecutionJSON(filepath.Join(base, "source-journal.json"), &before) != nil || deploymentplan.Digest(before) != a.Plan.ParentJournalSHA || before.OwnerToken != e.Journal.OwnerToken {
		return bad()
	}
	for i, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		g := a.Qualification.Gates[i]
		if g.ID != id || !g.Required || g.Status != "PASS" || g.EvidenceSHA256 != review {
			return bad()
		}
	}
	for _, v := range e.Journal.Entries {
		if v.Key == "openbao-recovery-admission-"+review && v.State == "completed" && !v.CompletedAt.IsZero() {
			var receipt map[string]string
			if json.Unmarshal(v.Receipt, &receipt) == nil && receipt["qualified_recovery_sha256"] == review && v.IntentSHA256 == deploymentplan.Digest(map[string]string{"review_sha256": review, "qualification_sha256": deploymentplan.Digest(a), "original_parent_journal_sha256": a.Plan.ParentJournalSHA}) {
				return a, before, nil
			}
		}
	}
	return bad()
}

func prepareOpenBaoTrustNameRecovery(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery) error {
	// Exactly the recorded Service and MQTT lineages plus one ready OpenBao Root
	// are permitted. The root loader independently checks the entire registry size.
	mqtt, _, err := loadOriginalReadyCreateRoot(ctx, cfg, store, e, b, operator, p, "mqtt", true)
	if err != nil {
		return err
	}
	r.MQTTRoot = &mqtt
	intermediate, err := loadOriginalActiveCreateIntermediate(ctx, cfg, store, e, b, operator, mqtt, findEnvironmentCreateDomainPolicy(p.Plan.Bootstrap, "mqtt"))
	if err != nil {
		return err
	}
	r.MQTTIntermediate = &intermediate
	root, operation, err := loadOriginalReadyCreateRoot(ctx, cfg, store, e, b, operator, p, "openbao_tls")
	if err != nil {
		return err
	}
	r.OpenBaoRoot, r.OpenBaoOperation = &root, &operation
	_, management, err := foundationRecoveryMaterials(store, e.Journal.PlanSHA256, time.Hour)
	if err != nil {
		return err
	}
	ca, err := parseFirstCertificatePEM([]byte(management.CACert))
	if err != nil {
		return err
	}
	caPath := filepath.Join(store.Root, "deployment/create/material", e.Journal.PlanSHA256, "openbao-transport/ca.crt")
	_, renewal, err := readInitialTransportRenewal(store, e.Journal.PlanSHA256)
	if err != nil {
		return err
	}
	if renewal != nil {
		caPath = filepath.Join(store.Root, "deployment/create/transport-renewal", renewal.Review, "openbao-transport/ca.crt")
	}
	runtime := environmentCreateBootstrapRuntime{ExecutionSHA256: e.Journal.PlanSHA256, Execution: e, Backend: b, ServiceSessionRecovery: r, TransportCAPEM: management.CACert, TransportRootSHA256: environmentPreflightBytesSHA256(ca.Raw), TransportTrustConfigMap: "pki-create-transport-trust", TransportSecrets: map[string]string{"account-manager": "pki-create-account-manager-transport", "pki-controller": "pki-create-pki-controller-transport", "certissuer": "pki-create-certissuer-transport"}, OpenBao: environmentCreateOpenBaoBootstrap{CACertPath: caPath}}
	listeners, _, err := loadInitialListenerCheckpoint(store, e, p, true)
	if err != nil {
		return err
	}
	binding, _, err := resolveInitialOperatorBinding(ctx, cfg, store, e, b, operator)
	if err != nil {
		return err
	}
	generationOperator := map[string]string{}
	for key, value := range operator {
		generationOperator[key] = value
	}
	generationOperator["PKI_OPERATOR_USER_ID"] = binding.UserID
	runtime.InitialListeners, err = renderEnvironmentCreateInitialPKIListeners(cfg, p.Plan.Release, binding.UserID, generationOperator)
	if err != nil {
		return err
	}
	runtime.InitialListeners, _, err = initialCertIssuerAuthDefinitions(cfg, runtime.InitialListeners)
	if err != nil {
		return err
	}
	var canonical []deploymentplan.Definition
	for _, v := range listeners.Resources {
		canonical = append(canonical, deploymentplan.Definition{Phase: v.Phase, Object: v.Desired})
	}
	canonical, _, err = initialCertIssuerAuthDefinitions(cfg, canonical)
	if err != nil {
		return err
	}
	compiled, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: "original-listener-render"}, runtime.InitialListeners, nil)
	if err != nil {
		return err
	}
	var rendered []deploymentplan.Definition
	for _, v := range compiled.Resources {
		rendered = append(rendered, deploymentplan.Definition{Phase: v.Phase, Object: v.Desired})
	}
	if !sameEnvironmentCreateDefinitions(canonical, rendered) {
		return createProviderRecoveryBlock("original initial listener renderer changed")
	}
	defs, err := renderEnvironmentCreatePreparedOpenBaoConsumers(ctx, cfg, p.Plan.Bootstrap, runtime, r.Ceremony, root)
	if err != nil {
		return err
	}
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return err
	}
	oldDefs := defs
	originalLive := live
	if r.InstallationForward {
		var observed createServiceSessionObserved
		prior := r.UsedRenewalHistory[len(r.UsedRenewalHistory)-2]
		if readExecutionJSON(filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/service/session-renewal", prior, "observed.json"), &observed) != nil {
			return createProviderRecoveryBlock("original unwritten OpenBao session custody unavailable")
		}
		oldService := r.Ceremony
		oldService.BootstrapSessionID = observed.Cleanup.SessionID
		oldService.BootstrapCredentialSecret = "service-bootstrap-" + observed.Cleanup.SessionID
		oldService.CleanupBinding, oldService.CredentialUIDs = observed.Cleanup, observed.UIDs
		oldDefs, err = renderEnvironmentCreatePreparedOpenBaoConsumers(ctx, cfg, p.Plan.Bootstrap, runtime, oldService, root)
		if err != nil {
			return err
		}
		originalLive = originalOpenBaoRequestInventory(r)
	}
	old := make([]deploymentplan.Definition, len(oldDefs))
	for i, d := range oldDefs {
		old[i] = d
		old[i].Object = deploymentplan.Clone(d.Object)
		if old[i].Object["kind"] == "ConfigMap" && deploymentplan.Map(old[i].Object["metadata"])["name"] == "pki-create-openbao-tls-consumer-trust" {
			deploymentplan.Map(old[i].Object["metadata"])["name"] = "pki-create-openbao_tls-consumer-trust"
		}
		if old[i].Object["kind"] == "Deployment" {
			for _, v := range certIssuerObjectList(environmentPKIPod(old[i].Object)["volumes"]) {
				cm := deploymentplan.Map(deploymentplan.Map(v)["configMap"])
				if cm["name"] == "pki-create-openbao-tls-consumer-trust" {
					cm["name"] = "pki-create-openbao_tls-consumer-trust"
				}
			}
		}
	}
	inputs := deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("openbao-root-trust", old)}
	original, err := deploymentplan.Compile(inputs, old, originalLive)
	if err != nil {
		return err
	}
	intent, err := environmentCreateKubernetesStageIntent(original)
	sha, hashErr := environmentCreatePlanSHA256(intent)
	var entry environmentCreateJournalEntry
	for _, v := range e.Journal.Entries {
		if v.Key == "kubernetes/openbao-root-trust" {
			entry = v
		}
	}
	if err != nil || hashErr != nil || entry.Key != "kubernetes/openbao-root-trust" || entry.State != "pending" || len(entry.Receipt) != 0 || sha != entry.IntentSHA256 || len(original.Resources) != 16 {
		if writeErr := writeEnvironmentCreateDerivedPrivateFile(store, filepath.Join("deployment/create", e.Journal.PlanSHA256, "recovery-diagnostics/openbao-unwritten-plan.json"), mustProviderRecoveryJSON(map[string]any{"plan": original, "definitions": old, "expected_intent_sha256": entry.IntentSHA256, "actual_intent_sha256": sha})); writeErr != nil {
			return writeErr
		}
		return createProviderRecoveryBlock("unwritten OpenBao trust admission intent or its original baselines changed")
	}
	inputs.BindingsSHA256 = environmentCreateStageBindingsSHA("openbao-trust-name-forward", defs)
	r.Candidate, err = deploymentplan.Compile(inputs, defs, live)
	if err != nil {
		return err
	}
	p.Materials["accepted-service-core-openbao-trust-original-plan"] = original.SHA256()
	p.Materials["accepted-service-core-openbao-trust-forward-plan"] = r.Candidate.SHA256()
	bindAcceptedTransportRootMaterials(p.Materials)
	if r.InstallationForward {
		return prepareCurrentInstallationForward(ctx, cfg, store, e, b, p, r)
	}
	if r.PostServiceRenewal {
		image, selection, err := loadServiceTrustRepairImage(ctx, cfg, store, e.Journal.PlanSHA256)
		if err != nil {
			return err
		}
		r.GrantImage = image
		p.Materials["accepted-service-core-trust-repair-selection"] = selection
		p.Materials["accepted-service-core-trust-repair-image"] = deploymentplan.Digest(image)
		r.RenewalBindingSHA = original.SHA256()
		return prepareUsedMQTTBootstrapRenewal(ctx, cfg, store, e, b, p, r)
	}
	return nil
}

func loadOriginalActiveCreateIntermediate(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, root environmentCreateRegisteredIssuer, policy environmentCreateIssuerPolicy) (environmentCreateRegisteredIssuer, error) {
	var issuer environmentCreateRegisteredIssuer
	var op environmentCreateIssuerOperation
	bad := func() (environmentCreateRegisteredIssuer, error) {
		return issuer, createProviderRecoveryBlock("completed MQTT intermediate must retain its original native operation, activation and public chain")
	}
	var doc map[string]any
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap", root.Domain, "intermediate-active.json"), &doc) != nil {
		return bad()
	}
	issuer, err := environmentCreateRegisteredIssuerFromMap(doc, root.RootFingerprint)
	if err != nil || issuer.Status != "active" || issuer.Domain != root.Domain || doc["parent_issuer_id"] != root.ID {
		return bad()
	}
	for _, v := range e.Journal.Entries {
		if v.Key == "pki-reserve-"+root.Domain+"-intermediate" && v.State == "completed" {
			if json.Unmarshal(v.Receipt, &op) != nil {
				return bad()
			}
		}
	}
	if op.IssuerID != issuer.ID || op.Digest != deploymentplan.Digest(environmentCreateIssuerRequest{Environment: cfg.Environment, Domain: root.Domain, Kind: "intermediate", ParentID: root.ID, ServerDNSNames: policy.ServerDNSNames, ServiceClientIDs: policy.ServiceClientIDs}) {
		return bad()
	}
	for _, key := range []string{"pki-authorize-" + op.ID, "pki-provision-" + op.ID, "pki-import-" + op.ID, "pki-activate-" + op.ID} {
		found := false
		for _, v := range e.Journal.Entries {
			if v.Key == key && v.State == "completed" && len(v.Receipt) > 0 {
				found = true
				if strings.HasPrefix(key, "pki-activate-") {
					var actual map[string]any
					if json.Unmarshal(v.Receipt, &actual) != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(doc) {
						return bad()
					}
				}
			}
		}
		if !found {
			return bad()
		}
	}
	binding, _, err := resolveInitialOperatorBinding(ctx, cfg, store, e, b, operator)
	if err != nil {
		return issuer, err
	}
	origin, httpClient, err := newEnvironmentCreateAccountClient(ctx, cfg, b, e)
	if err != nil {
		return issuer, err
	}
	client, err := newEnvironmentCreatePKIClient(origin, httpClient, cfg.Environment, binding.UserID)
	if err != nil {
		return issuer, err
	}
	if _, err = client.Authenticate(ctx, operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]); err != nil {
		return issuer, err
	}
	actual, err := client.ReadIssuer(ctx, issuer.ID)
	if err != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(doc) {
		return bad()
	}
	return issuer, nil
}
