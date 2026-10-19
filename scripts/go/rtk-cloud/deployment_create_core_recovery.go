package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"gopkg.in/yaml.v3"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"slices"
	"strings"
	"time"
)

// This checkpoint stopped before rendering managed core, after all enrollment
// stages were actually accepted. It must never replay renewal or enrollment.
func loadAcceptedServiceCore(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, p *createFoundationRecoveryPlan, order ...bool) (*createServiceSessionRecovery, error) {
	if len(order) == 2 && order[1] && managedApplicationInterruptedReview(e.Journal) != "" {
		return loadManagedApplicationInterruptedServiceCore(ctx, cfg, store, e, b, p)
	}
	if len(order) == 2 && order[1] && managedApplicationPartialReview(e.Journal) != "" {
		return loadManagedApplicationPartialServiceCore(ctx, cfg, store, e, b, p)
	}
	if len(order) == 2 && order[1] && isManagedTransportPartialCheckpoint(e.Journal) {
		return loadManagedTransportPartialServiceCore(ctx, cfg, store, e, b, operator, p)
	}
	partial := len(order) == 1 && order[0]
	application := len(order) == 2 && order[1]
	postService := application && len(e.Journal.Entries) > 0 && (isManagedTransportCheckpoint(e.Journal) || isAppTrustCheckpoint(e.Journal) || isDeviceTrustCheckpoint(e.Journal) || e.Journal.Entries[len(e.Journal.Entries)-1].Key == "kubernetes/device-root-ready" || e.Journal.Entries[len(e.Journal.Entries)-1].Key == "pki-reserve-openbao_tls-intermediate" || currentQuiesceReview(e.Journal.Entries[len(e.Journal.Entries)-1].Key) != "" || currentInstallationReview(e.Journal.Entries[len(e.Journal.Entries)-1].Key) != "" || e.Journal.Entries[len(e.Journal.Entries)-1].Key == "kubernetes/openbao-root-trust" || e.Journal.Entries[len(e.Journal.Entries)-1].Key == "kubernetes/mqtt-root-trust" || mqttTrustForwardReview(e.Journal.Entries[len(e.Journal.Entries)-1].Key) != "")
	identityReview := ""
	if application && len(e.Journal.Entries) > 0 {
		identityReview = applicationIdentityLockReview(e.Journal.Entries[len(e.Journal.Entries)-1].Key)
	}
	bad := func() (*createServiceSessionRecovery, error) {
		return nil, createProviderRecoveryBlock("accepted Service core requires original complete stage owners, active native lineage and exact renewed session custody")
	}
	boundary := "kubernetes/service-core-enrollment"
	if partial {
		boundary = "kubernetes/service-managed-core"
	}
	if application {
		boundary = "kubernetes/application-client-enrollment"
	}
	if identityReview != "" {
		boundary = e.Journal.Entries[len(e.Journal.Entries)-1].Key
	}
	if postService {
		boundary = e.Journal.Entries[len(e.Journal.Entries)-1].Key
	}
	if len(e.Journal.Entries) == 0 || e.Journal.Entries[len(e.Journal.Entries)-1].Key != boundary {
		return bad()
	}
	for _, entry := range e.Journal.Entries {
		if postService && isDeviceTrustCheckpoint(e.Journal) && (entry.Key == "device-root-bootstrap" || entry.Key == "kubernetes/device-root-trust" || deviceTrustForwardReview(entry.Key) != "") {
			continue
		}
		if (partial || application) && (entry.Key == boundary || postService && (entry.Key == "device-root-bootstrap" || entry.Key == "kubernetes/mqtt-root-trust" || entry.Key == "kubernetes/openbao-root-trust" || currentInstallationReview(entry.Key) != "" || currentQuiesceReview(entry.Key) != "") || identityReview != "" && entry.Key == "kubernetes/application-client-enrollment") && entry.State == "pending" && len(entry.Receipt) == 0 {
			continue
		}
		if entry.State != "completed" || entry.CompletedAt.IsZero() || len(entry.Receipt) == 0 {
			return bad()
		}
	}
	r := &createServiceSessionRecovery{CoreAccepted: true, Renewal: true, OrderForward: partial, ApplicationForward: application}
	var identitySource createFoundationRecoveryArtifact
	if postService {
		var err error
		identityReview, identitySource, err = loadPostServiceMQTTSource(store, e, r)
		if err != nil {
			return nil, err
		}
		p.Materials["accepted-service-core-post-service-source"] = deploymentplan.Digest(identitySource)
		if r.CompletedIssuerReview != "" {
			a, _, err := loadAdmittedCreateRecovery(store, e, r.CompletedIssuerReview)
			if err != nil {
				return nil, err
			}
			p.Materials["accepted-service-core-device-order-source"] = deploymentplan.Digest(a)
			p.Plan.Bootstrap = a.Plan.Plan.Bootstrap
		}
		if r.PostTransportResume {
			for _, entry := range e.Journal.Entries {
				if entry.Key == "kubernetes/openbao-root-trust" {
					p.Materials["accepted-service-core-openbao-trust-checkpoint"] = entry.IntentSHA256
				}
			}
		}
	}
	if identityReview != "" && !postService {
		var err error
		identitySource, err = loadApplicationIdentityLockSource(store, e, r)
		if err != nil {
			return nil, err
		}
		p.Materials["accepted-service-core-identity-source"] = deploymentplan.Digest(identitySource)
		p.Materials["accepted-service-core-identity-partial-plan"] = r.PendingManagedPlan.SHA256()
		p.Materials["accepted-service-core-identity-partial-journal"] = deploymentplan.Digest(r.PendingManagedJournal)
	}
	var forward struct {
		Stage    string `json:"actual_qualified_forward_stage"`
		Session  string `json:"renewed_session_id"`
		Original string `json:"original_failed_plan_sha256"`
	}
	for _, entry := range e.Journal.Entries {
		if entry.Key == "kubernetes/service-issuer-server-enrollment" {
			if json.Unmarshal(entry.Receipt, &forward) != nil {
				return bad()
			}
		}
	}
	prefix := "kubernetes/service-issuer-server-enrollment-renewal-"
	review := strings.TrimPrefix(forward.Stage, prefix)
	if !strings.HasPrefix(forward.Stage, prefix) || !environmentPreflightDigest.MatchString(review) || !environmentPreflightDigest.MatchString(forward.Original) || !environmentBootstrapOwnerUUID.MatchString(forward.Session) {
		return bad()
	}
	var source createFoundationRecoveryArtifact
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment/create/openbao-recovery", review, "qualification.json"), true)
	if err != nil || json.Unmarshal(raw, &source) != nil || source.Schema != "rtk-cloud.create-openbao-recovery-artifact/v1" || source.Plan.ParentSHA != e.Journal.PlanSHA256 || deploymentplan.Digest(source.Plan) != review || source.Qualification.PlanSHA256 != review || source.Qualification.Decision != "GO" || len(source.Qualification.Gates) != 6 {
		return bad()
	}
	p.Materials["accepted-service-core-source-qualification"] = environmentPreflightBytesSHA256(raw)
	for i, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
		g := source.Qualification.Gates[i]
		if !g.Required || g.ID != id || g.Status != "PASS" || g.EvidenceSHA256 != review {
			return bad()
		}
	}
	latest := map[string]deploymentplan.Resource{}
	owners := map[string]string{}
	stages := []struct {
		key  string
		size int
	}{
		{"kubernetes/service-session-original-key-renewal-" + review, 1},
		{"kubernetes/service-custody-reader-renewal-" + review, 1},
		{"kubernetes/service-credential-secrets-renewal-" + review, 7},
		{"kubernetes/service-enrollment-issuer-renewal-" + review, 1},
		{forward.Stage, 2},
		{"kubernetes/service-managed-enrollment-issuer", 1},
		{"kubernetes/service-core-enrollment", 8},
	}
	currentReview, currentSession, currentOriginal := review, forward.Session, forward.Original
	previousReview := ""
	previousSession := ""
	if application {
		var coreForward struct {
			Stage    string `json:"actual_qualified_forward_stage"`
			Session  string `json:"renewed_session_id"`
			Original string `json:"original_failed_plan_sha256"`
		}
		for _, entry := range e.Journal.Entries {
			if entry.Key == "kubernetes/service-managed-core" {
				if json.Unmarshal(entry.Receipt, &coreForward) != nil {
					return bad()
				}
			}
		}
		prefix := "kubernetes/service-managed-core-order-recovery-"
		currentReview = strings.TrimPrefix(coreForward.Stage, prefix)
		currentSession, currentOriginal = coreForward.Session, coreForward.Original
		if !strings.HasPrefix(coreForward.Stage, prefix) || !environmentPreflightDigest.MatchString(currentReview) || !environmentPreflightDigest.MatchString(currentOriginal) || !environmentBootstrapOwnerUUID.MatchString(currentSession) {
			return bad()
		}
		stages = append(stages, struct {
			key  string
			size int
		}{"kubernetes/service-session-original-key-renewal-" + currentReview, 1}, struct {
			key  string
			size int
		}{"kubernetes/service-custody-reader-renewal-" + currentReview, 1}, struct {
			key  string
			size int
		}{"kubernetes/service-credential-secrets-renewal-" + currentReview, 7}, struct {
			key  string
			size int
		}{coreForward.Stage, 15})
	}
	if identityReview != "" {
		previousReview, previousSession = currentReview, currentSession
		currentReview = identityReview
		currentOriginal = identitySource.Plan.Materials["accepted-service-core-application-plan"]
		currentSession = sessionRenewalUUID(e.Journal.PlanSHA256, currentOriginal, previousSession)
		for _, stage := range []struct {
			key  string
			size int
		}{
			{"kubernetes/service-trust-grants-recovery-" + identityReview, 1},
			{"kubernetes/service-session-original-key-renewal-" + identityReview, 1},
			{"kubernetes/service-custody-reader-renewal-" + identityReview, 1},
			{"kubernetes/service-credential-secrets-renewal-" + identityReview, 7},
		} {
			stages = append(stages, stage)
		}
		if postService {
			stages = append(stages, struct {
				key  string
				size int
			}{r.CompletedManagedStage, 15}, struct {
				key  string
				size int
			}{r.ApplicationStage, 12})
		}
	}
	for _, usedReview := range r.UsedRenewalHistory {
		for _, stage := range []struct {
			key  string
			size int
		}{{"kubernetes/service-session-original-key-renewal-" + usedReview, 1}, {"kubernetes/service-custody-reader-renewal-" + usedReview, 1}, {"kubernetes/service-credential-secrets-renewal-" + usedReview, 7}, {"kubernetes/service-managed-used-renewal-" + usedReview, 15}} {
			stages = append(stages, stage)
		}
		if !(r.InstallationForward && usedReview == r.UsedRenewalReview) {
			stages = append(stages, struct {
				key  string
				size int
			}{"kubernetes/application-current-installation-renewal-" + usedReview, 12})
		}
	}
	if r.PostTransportResume {
		stages = append(stages, struct {
			key  string
			size int
		}{r.MQTTAcceptedStage, 5}, struct {
			key  string
			size int
		}{"kubernetes/mqtt-intermediate-trust", 5})
	}
	if r.OpenBaoIntermediateRejected {
		for _, stage := range []struct {
			key  string
			size int
		}{{"kubernetes/current-owner-quiesce-" + r.CompletedTransportReview, 2}, {"kubernetes/current-installation-lock-forward-" + r.CompletedTransportReview, 3}, {"kubernetes/transport-client-enrollment", 7}, {"kubernetes/openbao-root-trust-name-recovery-" + r.CompletedTransportReview, 16}} {
			stages = append(stages, stage)
		}
	}
	if r.CompletedIssuerReview != "" {
		stages = append(stages, struct {
			key  string
			size int
		}{"kubernetes/used-installation-owner-quiesce-" + r.CompletedIssuerReview, 2}, struct {
			key  string
			size int
		}{"kubernetes/transport-client-enrollment-renewal-" + r.CompletedIssuerReview, 7}, struct {
			key  string
			size int
		}{"kubernetes/openbao-intermediate-trust", 16})
	}
	if r.DeviceTrustRecovery != nil {
		stages = append(stages, struct {
			key  string
			size int
		}{"kubernetes/device-root-ready-order-recovery-" + r.DeviceTrustRecovery.SourceReview, 2})
		p.Materials["accepted-service-core-device-trust-source"] = r.DeviceTrustRecovery.SourceReview
		if r.AppTrustRecovery != nil {
			stages = append(stages, struct {
				key  string
				size int
			}{"kubernetes/device-root-trust-policy-forward-" + r.AppTrustRecovery.SourceReview, 7}, struct {
				key  string
				size int
			}{"kubernetes/device-root-active", 2})
		}
	}
	if r.ManagedTransportRecovery != nil {
		stages = append(stages, struct {
			key  string
			size int
		}{"kubernetes/app-root-trust-broker-forward-" + r.ManagedTransportRecovery.SourceReview, 7}, struct {
			key  string
			size int
		}{"kubernetes/app-intermediate-trust", 7})
	}
	for _, stage := range stages {
		plan, journal, err := acceptedCreateStage(store, e, stage.key)
		if err != nil || len(plan.Resources) != stage.size || plan.Environment != cfg.Environment || plan.Stack != p.Plan.Stack || plan.ConfigurationSHA256 != p.Plan.ConfigurationSHA256 || plan.ReleaseSHA256 != deploymentplan.Digest(p.Plan.Release) {
			return bad()
		}
		intent, err := environmentCreateKubernetesStageIntent(plan)
		digest, digestErr := environmentCreatePlanSHA256(intent)
		bound := false
		for _, entry := range e.Journal.Entries {
			if entry.Key == stage.key && entry.IntentSHA256 == digest && err == nil && digestErr == nil {
				bound = true
			}
		}
		if !bound {
			return bad()
		}
		p.Materials["accepted-service-core-plan-"+stage.key] = plan.SHA256()
		p.Materials["accepted-service-core-journal-"+stage.key] = deploymentplan.Digest(journal)
		for _, resource := range plan.Resources {
			if owners[resource.ID] != "" && owners[resource.ID] != journal.Owners[resource.ID] {
				return bad()
			}
			latest[resource.ID], owners[resource.ID] = resource, journal.Owners[resource.ID]
		}
	}
	if partial {
		r.PartialCore, r.PartialCoreJournal, err = originalPendingManagedCorePlan(store, e, p.Plan.Stack)
		if err != nil {
			return nil, err
		}
		if !knownManagedCoreOrderFailure(r.PartialCore, r.PartialCoreJournal, p.Plan.Stack) || r.PartialCore.Environment != cfg.Environment || r.PartialCore.Stack != p.Plan.Stack || r.PartialCore.ConfigurationSHA256 != p.Plan.ConfigurationSHA256 || r.PartialCore.ReleaseSHA256 != deploymentplan.Digest(p.Plan.Release) {
			return bad()
		}
		p.Materials["accepted-service-core-partial-plan"] = r.PartialCore.SHA256()
		p.Materials["accepted-service-core-partial-journal"] = deploymentplan.Digest(r.PartialCoreJournal)
		for _, resource := range r.PartialCore.Resources {
			if uid := r.PartialCoreJournal.Owners[resource.ID]; uid != "" {
				if owners[resource.ID] != "" && owners[resource.ID] != uid {
					return bad()
				}
				latest[resource.ID], owners[resource.ID] = resource, uid
			}
		}
	}
	if application {
		if identityReview != "" {
			r.PartialCore, r.PartialCoreJournal, err = loadOriginalApplicationPlanAt(store, e, p.Plan.Stack, currentOriginal, postService)
		} else {
			r.PartialCore, r.PartialCoreJournal, err = originalPendingApplicationPlan(store, e, p.Plan.Stack)
		}
		if err != nil {
			return nil, err
		}
		p.Materials["accepted-service-core-application-plan"] = r.PartialCore.SHA256()
		p.Materials["accepted-service-core-application-journal"] = deploymentplan.Digest(r.PartialCoreJournal)
		for _, resource := range r.PartialCore.Resources {
			if uid := r.PartialCoreJournal.Owners[resource.ID]; uid != "" {
				latest[resource.ID], owners[resource.ID] = resource, uid
			}
		}
	}
	if identityReview != "" && !postService {
		for _, resource := range r.PendingManagedPlan.Resources {
			uid := r.PendingManagedJournal.Owners[resource.ID]
			if owners[resource.ID] != "" && owners[resource.ID] != uid {
				return bad()
			}
			latest[resource.ID], owners[resource.ID] = resource, uid
		}
	}
	if r.InstallationForward {
		p.Materials["accepted-service-core-current-installation-plan"] = r.InstallationPlan.SHA256()
		p.Materials["accepted-service-core-current-installation-journal"] = deploymentplan.Digest(r.InstallationJournal)
		for _, resource := range r.InstallationPlan.Resources {
			if uid := r.InstallationJournal.Owners[resource.ID]; uid != "" {
				latest[resource.ID], owners[resource.ID] = resource, uid
			}
		}
	}
	for _, pause := range r.QuiesceHistory {
		p.Materials["accepted-service-core-quiesce-plan-"+pause.Key] = pause.Plan.SHA256()
		p.Materials["accepted-service-core-quiesce-journal-"+pause.Key] = deploymentplan.Digest(pause.Journal)
	}
	if r.QuiescePendingPlan != nil {
		p.Materials["accepted-service-core-current-quiesce-plan"] = r.QuiescePendingPlan.SHA256()
		p.Materials["accepted-service-core-current-quiesce-journal"] = deploymentplan.Digest(r.QuiescePendingJournal)
		for _, v := range r.QuiescePendingPlan.Resources {
			latest[v.ID], owners[v.ID] = v, r.QuiescePendingJournal.Owners[v.ID]
		}
	}
	if r.CompletedIssuerReview != "" {
		for _, stage := range stages {
			plan, journal, err := acceptedCreateStage(store, e, stage.key)
			if err != nil {
				return nil, err
			}
			for _, v := range plan.Resources {
				latest[v.ID], owners[v.ID] = v, journal.Owners[v.ID]
			}
		}
	}

	if r.AppTrustRecovery != nil && r.ManagedTransportRecovery == nil {
		plan, j, err := loadOriginalAppTrustPlan(store, e)
		if err != nil {
			return nil, err
		}
		r.AppTrustRecovery.Original, r.AppTrustRecovery.Journal = plan, j
		for _, v := range plan.Resources {
			if uid := j.Owners[v.ID]; uid != "" {
				if owners[v.ID] != "" && owners[v.ID] != uid {
					return bad()
				}
				latest[v.ID], owners[v.ID] = v, uid
			}
		}
		p.Materials["accepted-service-core-app-trust-original-plan"] = plan.SHA256()
		p.Materials["accepted-service-core-app-trust-original-journal"] = deploymentplan.Digest(j)
	}
	if r.DeviceTrustRecovery != nil && r.AppTrustRecovery == nil {
		if err := loadDeviceTrustPartialForwards(store, e, r.DeviceTrustRecovery); err != nil {
			return nil, err
		}
		if plan := r.DeviceTrustRecovery.ForwardPlan; plan != nil {
			for _, v := range plan.Resources {
				if uid := r.DeviceTrustRecovery.ForwardJournal.Owners[v.ID]; uid != "" {
					if owners[v.ID] != "" && owners[v.ID] != uid {
						return bad()
					}
					latest[v.ID], owners[v.ID] = v, uid
				}
			}
			p.Materials["accepted-service-core-device-trust-partial-forward-plan"] = plan.SHA256()
			p.Materials["accepted-service-core-device-trust-partial-forward-journal"] = deploymentplan.Digest(r.DeviceTrustRecovery.ForwardJournal)
		}
	}
	if r.ManagedTransportRecovery != nil {
		transportOverrides, err := activeTransportRenewalOverrides(store, e.Journal.PlanSHA256)
		if err != nil {
			return nil, err
		}
		for id, resource := range transportOverrides {
			if _, exists := latest[id]; !exists {
				continue
			}
			if resource.UID == "" || owners[id] != resource.UID || deploymentplan.ID(resource.Desired) != id {
				return bad()
			}
			latest[id] = resource
			p.Materials["accepted-service-core-managed-temporary-transport-"+id] = deploymentplan.Digest(resource.Desired)
		}
	}
	combined := &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack}}
	ids := make([]string, 0, len(latest))
	for id := range latest {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	// Obtain one fresh native snapshot at this verification boundary. Historical
	// accepted resources must already be ready; readiness and Pod image evidence
	// are checked below without serial GET + retained rollout waits per resource.
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return nil, err
	}
	if err := verifyAcceptedCreateSnapshot(latest, owners, live); err != nil {
		return nil, err
	}
	for _, id := range ids {
		resource := latest[id]
		resource.Action, resource.UID = "retain", owners[id]
		if r.AppTrustRecovery != nil && id == r.AppTrustRecovery.Journal.Failed {
			continue
		}
		if r.DeviceTrustRecovery != nil && id == r.DeviceTrustRecovery.ForwardJournal.Failed {
			continue
		}
		if r.InstallationForward && id == r.InstallationJournal.Failed {
			continue
		}
		if (partial || application) && id == r.PartialCoreJournal.Failed || identityReview != "" && !postService && id == r.PendingManagedJournal.Failed {
			continue
		}
		combined.Resources = append(combined.Resources, resource)
	}
	if err := verifyOriginalMigrationCheckpointInventory(ctx, store, combined, deploymentplan.Journal{Owners: owners}, live); err != nil {
		return nil, err
	}
	r.Review = review
	base := filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/service")
	if readExecutionJSON(filepath.Join(base, "session-intent.json"), &r.Intent) != nil || !environmentBootstrapOwnerUUID.MatchString(r.Intent.SessionID) || r.Intent.Claim != "pki-service-bootstrap-state" || !slices.Equal(r.Intent.Namespaces, []string{p.Plan.Stack + "-video-cloud", p.Plan.Stack + "-account-manager", p.Plan.Stack + "-secrets"}) || sessionRenewalUUID(e.Journal.PlanSHA256, forward.Original, r.Intent.SessionID) != forward.Session {
		return bad()
	}
	r.RenewedIntent = r.Intent
	r.RenewedIntent.SessionID = forward.Session
	r.RenewedCertPath = "/run/pki-bootstrap/renew-" + forward.Original[:12] + "/bootstrap.crt"
	if application {
		previous := forward.Session
		if identityReview != "" {
			previous = previousSession
		}
		if sessionRenewalUUID(e.Journal.PlanSHA256, currentOriginal, previous) != currentSession {
			return bad()
		}
		r.RenewedIntent.SessionID = currentSession
		r.RenewedCertPath = "/run/pki-bootstrap/renew-" + currentOriginal[:12] + "/bootstrap.crt"
	}
	for _, usedReview := range r.UsedRenewalHistory {
		binding := r.OriginalMQTTPlan.SHA256()
		if usedReview == r.UsedRenewalReview && r.InstallationForward {
			binding = r.RenewalBindingSHA
		}
		if usedReview == r.CompletedIssuerReview {
			a, _, err := loadAdmittedCreateRecovery(store, e, usedReview)
			if err != nil {
				return nil, err
			}
			binding = a.Plan.Materials["accepted-service-core-openbao-dns-original-request"]
			if !environmentPreflightDigest.MatchString(binding) {
				return bad()
			}
		}
		currentSession = sessionRenewalUUID(e.Journal.PlanSHA256, binding, currentSession)
		currentReview = usedReview
		r.RenewedIntent.SessionID = currentSession
		r.RenewedCertPath = "/run/pki-bootstrap/renew-" + binding[:12] + "/bootstrap.crt"
	}
	root, _, err := loadOriginalReadyServiceRoot(ctx, cfg, store, e, b, operator, p, true, postService)
	if err != nil {
		return nil, err
	}
	var document map[string]any
	if readExecutionJSON(filepath.Join(base, "intermediate-active.json"), &document) != nil {
		return bad()
	}
	intermediate, err := environmentCreateRegisteredIssuerFromMap(document, root.RootFingerprint)
	if err != nil || intermediate.ID != r.Intent.IssuerID || intermediate.Status != "active" || document["parent_issuer_id"] != root.ID || intermediate.Domain != "service" || intermediate.Environment != cfg.Environment {
		return bad()
	}
	bound := false
	for _, entry := range e.Journal.Entries {
		var v map[string]any
		if strings.HasPrefix(entry.Key, "pki-activate-") && json.Unmarshal(entry.Receipt, &v) == nil && deploymentplan.Digest(v) == deploymentplan.Digest(document) {
			bound = true
		}
	}
	if !bound {
		return bad()
	}
	origin, httpClient, err := newEnvironmentCreateAccountClient(ctx, cfg, b, e)
	if err != nil {
		return nil, err
	}
	binding, _, err := resolveInitialOperatorBinding(ctx, cfg, store, e, b, operator)
	if err != nil {
		return nil, err
	}
	client, err := newEnvironmentCreatePKIClient(origin, httpClient, cfg.Environment, binding.UserID)
	if err != nil {
		return nil, err
	}
	if _, err = client.Authenticate(ctx, operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]); err != nil {
		return nil, err
	}
	actualIntermediate, err := client.ReadIssuer(ctx, intermediate.ID)
	if err != nil || deploymentplan.Digest(actualIntermediate) != deploymentplan.Digest(document) {
		return bad()
	}
	var observed createServiceSessionObserved
	raw, err = readBoundedEnvironmentFile(filepath.Join(base, "session-renewal", currentReview, "observed.json"), true)
	if err != nil || json.Unmarshal(raw, &observed) != nil || validateEnvironmentBootstrapCleanupBinding(&observed.Cleanup, cfg.Environment) != nil || observed.Cleanup.SessionID != currentSession || observed.Reader != "pki-bootstrap-custody-"+e.Journal.PlanSHA256[:12] || len(observed.UIDs) != len(r.Intent.Namespaces) {
		return bad()
	}
	p.Materials["accepted-service-core-observed"] = environmentPreflightBytesSHA256(raw)
	s := environmentCreateServiceCeremony{Root: root, Intermediate: intermediate, BootstrapSessionID: currentSession, BootstrapClaim: r.Intent.Claim, BootstrapCredentialNamespace: r.Intent.Namespaces[0], BootstrapCredentialNamespaces: slices.Clone(r.Intent.Namespaces), BootstrapCredentialSecret: "service-bootstrap-" + currentSession, CustodyReader: observed.Reader, CleanupBinding: observed.Cleanup, CredentialUIDs: observed.UIDs, RootCRLSecret: "pki-create-service-root-crl"}
	for _, entry := range e.Journal.Entries {
		if entry.Key == "pki-initial-root-crl-service" {
			var v struct {
				File string `json:"crl_file"`
				SHA  string `json:"crl_pem_sha256"`
			}
			if json.Unmarshal(entry.Receipt, &v) != nil {
				return bad()
			}
			raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, v.File), true)
			if err != nil || environmentPreflightBytesSHA256(raw) != v.SHA {
				return bad()
			}
			s.RootCRLFile, s.RootCRLPEM = v.File, string(raw)
		}
	}
	if r.ManagedTransportRecovery != nil {
		crlRecovery, crlProof, err := readCreateRootCRLRecovery(store, e.Journal.PlanSHA256)
		if err != nil {
			return nil, err
		}
		if crlRecovery != nil {
			if crlRecovery.Plan.Root.ID != root.ID || crlRecovery.Plan.Root.RootFingerprint != root.RootFingerprint {
				return bad()
			}
			s.RootCRLFile, s.RootCRLPEM = crlProof.File, crlRecovery.Plan.Signed.PEM
			p.Materials["accepted-service-core-managed-service-root-crl-recovery"] = deploymentplan.Digest(crlRecovery)
			p.Materials["accepted-service-core-managed-service-root-crl-proof"] = deploymentplan.Digest(crlProof)
		}
	}
	var kube struct {
		Clusters []struct {
			Cluster map[string]any `yaml:"cluster"`
		} `yaml:"clusters"`
	}
	if yaml.Unmarshal(b.KubeconfigContent, &kube) != nil || len(kube.Clusters) != 1 || kube.Clusters[0].Cluster["certificate-authority-data"] != observed.Cleanup.KubernetesCA {
		return bad()
	}
	ns, err := b.Read(ctx, "Namespace//"+s.BootstrapCredentialNamespace)
	if err != nil || ns == nil || deploymentplan.Map(ns["metadata"])["uid"] != s.CleanupBinding.NamespaceUID {
		return bad()
	}
	material := map[string]string{}
	for _, namespace := range s.BootstrapCredentialNamespaces {
		secret, err := b.Read(ctx, "Secret/"+namespace+"/"+s.BootstrapCredentialSecret)
		if err != nil || secret == nil || deploymentplan.Map(secret["metadata"])["uid"] != observed.UIDs[namespace] {
			return bad()
		}
		values := map[string]string{}
		for _, key := range []string{"tls.crt", "tls.key", "ca.crt"} {
			v, err := base64.StdEncoding.Strict().DecodeString(deploymentplan.String(deploymentplan.Map(secret["data"])[key]))
			if err != nil {
				return bad()
			}
			values[key] = string(v)
		}
		if len(material) > 0 && deploymentplan.Digest(material) != deploymentplan.Digest(values) {
			return bad()
		}
		material = values
	}
	if observed.Cleanup.SecretUID != observed.UIDs[s.BootstrapCredentialNamespace] || material["ca.crt"] != root.Certificate {
		return bad()
	}
	pair, err := tls.X509KeyPair([]byte(material["tls.crt"]), []byte(material["tls.key"]))
	if err != nil || len(pair.Certificate) < 2 {
		return bad()
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	minimum := time.Hour
	if postService {
		minimum = 30 * time.Minute
		if r.PostTransportResume {
			minimum = 10 * time.Minute
		}
	}
	if postService && err == nil && !leaf.NotAfter.After(time.Now()) {
		r.PostServiceRenewal = true
	}
	if err != nil || leaf.Subject.CommonName != "service:deployment-bootstrap" || (!partial && !application && !leaf.NotAfter.After(time.Now())) || ((partial || application) && identityReview == "" && leaf.NotAfter.After(time.Now())) || (identityReview != "" && !r.PostServiceRenewal && !r.OpenBaoIntermediateRejected && !leaf.NotAfter.After(time.Now().Add(minimum))) || environmentPreflightBytesSHA256(pair.Certificate[len(pair.Certificate)-1]) != root.RootFingerprint {
		return bad()
	}
	for _, file := range []string{"bootstrap.key", "bootstrap.csr", r.RenewedCertPath} {
		path := "/run/pki-bootstrap/" + file
		if strings.HasPrefix(file, "/") {
			path = file
		}
		raw, err := b.command(ctx, nil, "--namespace", s.BootstrapCredentialNamespace, "exec", "deployment/"+s.CustodyReader, "--container", "custody", "--", "cat", path)
		if err != nil {
			return bad()
		}
		digest := environmentPreflightBytesSHA256(raw)
		if file == "bootstrap.key" {
			r.OriginalKeySHA = digest
			if string(raw) != material["tls.key"] {
				return bad()
			}
		}
		if file == "bootstrap.csr" {
			r.OriginalCSRSHA = digest
		}
		if file == r.RenewedCertPath && string(raw) != material["tls.crt"] {
			return bad()
		}
		if file == "bootstrap.key" || file == "bootstrap.csr" {
			if source.Plan.Materials["original-service-session-custody-"+file] != digest {
				return bad()
			}
		}
	}
	query := "SELECT json_build_object('session_id',session_id,'issuer_id',issuer_id,'root',root_sha256,'fingerprint',bootstrap_fingerprint,'status',status,'subjects',subjects,'installed',installed_subjects,'expires',expires_at,'other_active',(SELECT count(*) FROM pki_deployment_bootstrap_sessions WHERE status='active' AND expires_at>now() AND session_id<>'" + currentSession + "')) FROM pki_deployment_bootstrap_sessions WHERE environment='" + cfg.Environment + "' AND session_id='" + currentSession + "'"
	raw, err = readEnvironmentCreateRegistryJSON(ctx, cfg, environmentCreateBootstrapRuntime{Backend: b, Execution: e}, query)
	var session struct {
		ID          string    `json:"session_id"`
		Issuer      string    `json:"issuer_id"`
		Root        string    `json:"root"`
		Fingerprint string    `json:"fingerprint"`
		Status      string    `json:"status"`
		Subjects    []string  `json:"subjects"`
		Expires     time.Time `json:"expires"`
		Other       int       `json:"other_active"`
		Installed   []string  `json:"installed"`
	}
	subjects, subjectsErr := environmentCreateBootstrapSubjects(cfg)
	if err != nil || subjectsErr != nil || json.Unmarshal(raw, &session) != nil || session.ID != currentSession || session.Issuer != intermediate.ID || session.Root != root.RootFingerprint || session.Fingerprint != environmentPreflightBytesSHA256(leaf.Raw) || (session.Status != "active" && !((partial || application) && session.Status == "expired")) || session.Other != 0 || (!partial && !application && !session.Expires.After(time.Now())) || (partial && (session.Expires.After(time.Now()) || len(session.Installed) != 0)) || !slices.Equal(session.Subjects, subjects) || !slices.Equal(r.Intent.Subjects, subjects) {
		return bad()
	}
	if application {
		for _, subject := range session.Installed {
			allowed := []string{"service:account-manager", "service:certissuer", "service:pki-controller"}
			if postService {
				allowed = subjects
			}
			if !slices.Contains(allowed, subject) {
				return bad()
			}
		}
	}
	r.Ceremony = s
	authentication := *r
	authentication.Intent = r.RenewedIntent
	if err := verifyServiceSessionAuthentication(ctx, cfg, store, e, b, &authentication); err != nil {
		return nil, err
	}
	if partial {
		r.StaleRenewal = &observed
		if err := prepareManagedCoreRenewal(ctx, cfg, store, e, b, p, r, latest); err != nil {
			return nil, err
		}
	}
	if application {
		r.StaleRenewal = &observed
		r.SourceRenewalStage = "kubernetes/service-session-original-key-renewal-" + currentReview
		var previous createServiceSessionObserved
		if readExecutionJSON(filepath.Join(base, "session-renewal", review, "observed.json"), &previous) != nil {
			return bad()
		}
		r.StaleSeeds = []createServiceSessionObserved{previous, observed}
		if identityReview != "" {
			var prior createServiceSessionObserved
			if readExecutionJSON(filepath.Join(base, "session-renewal", previousReview, "observed.json"), &prior) != nil {
				return bad()
			}
			r.StaleSeeds = []createServiceSessionObserved{previous, prior}
			if r.UsedRenewalReview != "" {
				var old createServiceSessionObserved
				if readExecutionJSON(filepath.Join(base, "session-renewal", identityReview, "observed.json"), &old) != nil {
					return bad()
				}
				r.StaleSeeds = append(r.StaleSeeds, old)
			}
			r.RenewedIntent.SessionID = currentSession
			if postService {
				if r.PostServiceRenewal {
					r.StaleSeeds = append(r.StaleSeeds, observed)
				}
				var prepareErr error
				if r.ManagedTransportRecovery != nil {
					prepareErr = prepareManagedTransportRecovery(ctx, cfg, store, e, b, operator, p, r)
				} else if r.AppTrustRecovery != nil {
					prepareErr = prepareAppTrustRecovery(ctx, cfg, store, e, b, operator, p, r)
				} else if r.DeviceTrustRecovery != nil {
					prepareErr = prepareDeviceTrustRecovery(ctx, cfg, store, e, b, operator, p, r)
				} else if r.DeviceBootstrapRecovery != nil {
					prepareErr = prepareDeviceBootstrapOrderRecovery(ctx, cfg, store, e, b, operator, p, r)
				} else if r.PostTransportResume {
					if r.OpenBaoIntermediateRejected {
						prepareErr = prepareOpenBaoIntermediateDNSRecovery(ctx, cfg, store, e, b, operator, p, r)
					} else {
						prepareErr = prepareOpenBaoTrustNameRecovery(ctx, cfg, store, e, b, operator, p, r)
					}
				} else {
					prepareErr = prepareMQTTTrustOrderRecovery(ctx, cfg, store, e, b, operator, p, r)
				}
				if err := prepareErr; err != nil {
					return nil, err
				}
			} else if err := prepareApplicationIdentityLockRecovery(ctx, cfg, store, e, b, p, r); err != nil {
				return nil, err
			}
		} else if err := prepareApplicationTrustRecovery(ctx, cfg, store, e, b, p, r); err != nil {
			return nil, err
		}
	}
	p.CheckpointPlanSHA, p.CheckpointJournalSHA = p.Materials["accepted-service-core-plan-kubernetes/service-core-enrollment"], p.Materials["accepted-service-core-journal-kubernetes/service-core-enrollment"]
	return r, e.Check(ctx)
}

func knownManagedCoreOrderFailure(plan *deploymentplan.Plan, j deploymentplan.Journal, stack string) bool {
	if plan == nil || len(plan.Resources) != 15 || j.PlanSHA256 != plan.SHA256() || j.Accepted || j.AcceptanceStarted || j.InFlight != "" || j.Failed != "Deployment/"+stack+"-account-manager/account-manager" || len(j.Completed) != 12 || len(j.Owners) != 13 {
		return false
	}
	seen := map[string]bool{}
	for _, id := range j.Completed {
		if seen[id] || j.Owners[id] == "" {
			return false
		}
		seen[id] = true
	}
	for _, r := range plan.Resources {
		if r.ID == "Deployment/"+stack+"-video-cloud/pki-controller" || r.ID == "Deployment/"+stack+"-video-cloud/certissuer" {
			if j.Owners[r.ID] != "" || seen[r.ID] {
				return false
			}
			continue
		}
		if j.Owners[r.ID] == "" || (r.ID != j.Failed && !seen[r.ID]) || (r.ID == j.Failed && seen[r.ID]) {
			return false
		}
	}
	return true
}

func originalPendingManagedCorePlan(store secretStore, e *environmentCreateExecution, stack string) (*deploymentplan.Plan, deploymentplan.Journal, error) {
	var plan deploymentplan.Plan
	var j deploymentplan.Journal
	bad := func() (*deploymentplan.Plan, deploymentplan.Journal, error) {
		return nil, j, createProviderRecoveryBlock("exact known partial core and its most recent original child lease required")
	}
	if len(e.Journal.KubernetesLeases) == 0 || len(e.Journal.Entries) == 0 {
		return bad()
	}
	entry := e.Journal.Entries[len(e.Journal.Entries)-1]
	if entry.Key != "kubernetes/service-managed-core" || entry.State != "pending" || len(entry.Receipt) != 0 {
		return bad()
	}
	sha := strings.SplitN(e.Journal.KubernetesLeases[len(e.Journal.KubernetesLeases)-1].Holder, ":", 2)[0]
	if !environmentPreflightDigest.MatchString(sha) || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "plan.json"), &plan) != nil || readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", sha, "journal.json"), &j) != nil || plan.SHA256() != sha || !knownManagedCoreOrderFailure(&plan, j, stack) {
		return bad()
	}
	intent, err := environmentCreateKubernetesStageIntent(&plan)
	digest, digestErr := environmentCreatePlanSHA256(intent)
	if err != nil || digestErr != nil || digest != entry.IntentSHA256 {
		return bad()
	}
	return &plan, j, nil
}

func prepareManagedCoreRenewal(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery, latest map[string]deploymentplan.Resource) error {
	var old deploymentplan.Object
	for id, resource := range latest {
		if strings.HasPrefix(id, "Job/"+p.Plan.Stack+"-video-cloud/pki-bootstrap-renew-") {
			if old != nil {
				return createProviderRecoveryBlock("one original accepted renewal required")
			}
			old = deploymentplan.Clone(resource.Desired)
		}
	}
	if old == nil {
		return createProviderRecoveryBlock("original accepted native renewal recipe missing")
	}
	container := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(old)["containers"])[0])
	container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/bootstrap.crt"}))
	r.RenewedIntent.SessionID = sessionRenewalUUID(e.Journal.PlanSHA256, r.PartialCore.SHA256(), r.Ceremony.BootstrapSessionID)
	definition, err := originalKeyRenewalDefinition(old, e.Journal.PlanSHA256, r.PartialCore.SHA256(), r.RenewedIntent.SessionID)
	if err != nil {
		return err
	}
	r.RenewedCertPath = "/run/pki-bootstrap/renew-" + r.PartialCore.SHA256()[:12] + "/bootstrap.crt"
	// The leaf remains a 30-minute provisioner. The separately bounded session
	// permits original managed workloads to acknowledge after that leaf expires.
	var expires time.Time
	for _, entry := range e.Journal.Entries {
		if entry.Key == "kubernetes/service-managed-core" {
			expires = entry.StartedAt.Add(3 * time.Hour).UTC().Truncate(time.Second)
		}
	}
	if expires.IsZero() || !expires.After(time.Now().Add(90*time.Minute)) {
		return createProviderRecoveryBlock("original partial-core recovery acknowledgement window is exhausted")
	}
	container = deploymentplan.Map(certIssuerObjectList(environmentPKIPod(definition.Object)["containers"])[0])
	container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_EXPIRES_AT": expires.Format(time.RFC3339)}))
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return err
	}
	r.Candidate, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("managed-core-original-key-renewal", []deploymentplan.Definition{definition})}, []deploymentplan.Definition{definition}, live)
	if err != nil {
		return err
	}
	if len(r.Candidate.Resources) != 1 || r.Candidate.Resources[0].Action != "create" {
		return createProviderRecoveryBlock("core forward renewal already exists; inspect its actual native outcome")
	}
	p.Materials["accepted-service-core-order-renewal-plan"] = r.Candidate.SHA256()
	return nil
}
