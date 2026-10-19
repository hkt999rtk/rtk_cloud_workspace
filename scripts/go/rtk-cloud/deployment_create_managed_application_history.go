package main

import (
	"context"
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Historical derivation is local and immutable. Current Secrets, retired
// readers, workload bodies and volume allocations are verified independently;
// they are never replaced by fabricated historical native observations.
func loadManagedApplicationPartialServiceCore(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan) (*createServiceSessionRecovery, error) {
	r, err := deriveManagedApplicationPartialRecovery(cfg, store, e, p)
	if err != nil {
		return nil, err
	}
	if err := verifyManagedApplicationPartialCurrent(ctx, cfg, store, e, b, r, p); err != nil {
		return nil, err
	}
	if err := prepareManagedApplicationPartialCandidate(ctx, cfg, e, b, p, r); err != nil {
		return nil, err
	}
	return r, e.Check(ctx)
}

func deriveManagedApplicationPartialRecovery(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, p *createFoundationRecoveryPlan) (*createServiceSessionRecovery, error) {
	source, admission, application, err := loadManagedApplicationPartialSource(store, e)
	if err != nil {
		return nil, err
	}
	r, err := deriveManagedApplicationPartialHistory(cfg, store, source, p)
	if err != nil {
		return nil, err
	}
	application.Historical = r.ManagedTransportRecovery.Partial
	var preview deploymentplan.Plan
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create/openbao-recovery", application.Review, "openbao-root-trust-plan.json"), &preview) != nil || preview.SHA256() != admission.Plan.Materials["accepted-service-core-managed-partial-forward-plan"] {
		return nil, createProviderRecoveryBlock("managed application admitted transport observation plan changed")
	}
	definitions := []deploymentplan.Definition{}
	for _, resource := range preview.Resources {
		definitions = append(definitions, deploymentplan.Definition{Phase: resource.Phase, Object: resource.Desired, Dependencies: resource.Dependencies})
	}
	if validateManagedTransportPostManagedDefinitions(application.Historical, definitions) != nil || validateManagedTransportPartialForwardActions(application.Historical, &preview) != nil {
		return nil, createProviderRecoveryBlock("managed application source must retain all accepted managed transport owners")
	}
	r.ManagedApplicationPartial = application
	if err := validateManagedApplicationPartialExports(store, e, source, application, r.Ceremony.Root); err != nil {
		return nil, err
	}
	p.Materials["accepted-service-core-managed-application-source"] = application.SourceSHA
	p.Materials["accepted-service-core-managed-application-admission"] = application.ArtifactSHA
	p.Materials["accepted-service-core-managed-application-plan"] = application.Plan.SHA256()
	p.Materials["accepted-service-core-managed-application-journal"] = deploymentplan.Digest(application.Journal)
	for key, digest := range admission.Plan.Materials {
		if strings.HasPrefix(key, "accepted-service-core-managed-partial-original-volume-") {
			p.Materials[key] = digest
		}
	}
	return r, nil
}

func deriveManagedApplicationPartialHistory(cfg deploymentConfig, store secretStore, e *environmentCreateExecution, p *createFoundationRecoveryPlan) (*createServiceSessionRecovery, error) {
	bad := func(stage string) (*createServiceSessionRecovery, error) {
		return nil, createProviderRecoveryBlock("managed application original history differs: stage=" + stage)
	}
	baseExecution, prior, accepted, post, err := loadManagedTransportExportRetirementSource(store, e, true)
	if err != nil {
		return nil, err
	}
	admitted, before, review, err := loadManagedTransportPartialSource(store, baseExecution)
	if err != nil {
		return nil, err
	}
	original, journal, err := loadOriginalManagedTransportPartialPlan(store, baseExecution)
	if err != nil {
		return nil, err
	}
	partial := &createManagedTransportPartialRecovery{SourceReview: review, PriorForward: prior, AcceptedForward: accepted, PostManaged: post, Original: original, Journal: journal, PVCs: map[string]string{}, AdditionalOwners: map[string]deploymentplan.Resource{}}
	p.Plan.Bootstrap = admitted.Plan.Plan.Bootstrap
	r := &createServiceSessionRecovery{CoreAccepted: true, Renewal: true, PostServiceResume: true, PostTransportResume: true}
	historical := *e
	historical.Journal = before
	if _, _, err := loadManagedTransportRecoverySource(store, &historical, r); err != nil {
		return nil, err
	}
	d := r.ManagedTransportRecovery
	d.Partial = partial
	roots, intermediates := map[string]environmentCreateRegisteredIssuer{}, map[string]environmentCreateRegisteredIssuer{}
	for _, domain := range managedPKIDomains {
		var public struct {
			Root         map[string]any `json:"root"`
			Intermediate map[string]any `json:"intermediate,omitempty"`
		}
		if readExecutionJSON(filepath.Join(store.Root, "pki", domain, "issuer.json"), &public) != nil {
			return bad("publication-" + domain)
		}
		root, err := environmentCreateRegisteredIssuerFromMap(public.Root, "")
		if err != nil || root.Environment != cfg.Environment || root.Domain != domain || root.Status != "active" {
			return bad("active-root-" + domain)
		}
		roots[domain] = root
		if domain != "device" {
			intermediate, err := environmentCreateRegisteredIssuerFromMap(public.Intermediate, root.RootFingerprint)
			if err != nil || intermediate.Environment != cfg.Environment || intermediate.Domain != domain || intermediate.Status != "active" || intermediate.Document["parent_issuer_id"] != root.ID {
				return bad("active-intermediate-" + domain)
			}
			intermediates[domain] = intermediate
		}
	}
	if err := verifyEnvironmentCreateRegisteredIssuers(store, roots, intermediates, p.Materials); err != nil {
		return nil, err
	}
	d.Device, d.AppRoot, d.AppIntermediate = roots["device"], roots["app"], intermediates["app"]
	var ready map[string]any
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/device/root-ready.json"), &ready) != nil {
		return bad("device-ready")
	}
	d.DeviceReady, err = environmentCreateRegisteredIssuerFromMap(ready, "")
	if err != nil || d.DeviceReady.ID != d.Device.ID || d.DeviceReady.Certificate != d.Device.Certificate || d.DeviceReady.Status != "ready" {
		return bad("device-ready-pin")
	}
	d.AppIntermediateReady, err = originalReadyIntermediate(store, e, d.AppIntermediate)
	if err != nil {
		return nil, err
	}
	r.AppTrustRecovery.DeviceReady, r.AppTrustRecovery.Device, r.AppTrustRecovery.Root = d.DeviceReady, d.Device, d.AppRoot
	mqttRoot, mqttIntermediate, baoRoot, baoIntermediate := roots["mqtt"], intermediates["mqtt"], roots["openbao_tls"], intermediates["openbao_tls"]
	r.MQTTRoot, r.MQTTIntermediate, r.OpenBaoRoot, r.OpenBaoIntermediate = &mqttRoot, &mqttIntermediate, &baoRoot, &baoIntermediate
	path := filepath.Join("deployment/create", e.Journal.PlanSHA256, "bootstrap/service")
	if readExecutionJSON(filepath.Join(store.Root, path, "session-intent.json"), &r.Intent) != nil || !environmentBootstrapOwnerUUID.MatchString(r.Intent.SessionID) || r.Intent.Claim != "pki-service-bootstrap-state" || !slices.Equal(r.Intent.Namespaces, []string{p.Plan.Stack + "-video-cloud", p.Plan.Stack + "-account-manager", p.Plan.Stack + "-secrets"}) {
		return bad("original-session-intent")
	}
	var observed createServiceSessionObserved
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, path, "session-renewal", review, "observed.json"), true)
	if err != nil || json.Unmarshal(raw, &observed) != nil || validateEnvironmentBootstrapCleanupBinding(&observed.Cleanup, cfg.Environment) != nil || observed.Reader != "pki-bootstrap-custody-"+e.Journal.PlanSHA256[:12] || len(observed.UIDs) != len(r.Intent.Namespaces) {
		return bad("renewed-session-observation")
	}
	var state managedSessionState
	if json.Unmarshal(observed.Session, &state) != nil || state.Session != observed.Cleanup.SessionID || state.Issuer != intermediates["service"].ID || state.Root != roots["service"].RootFingerprint || !environmentPreflightDigest.MatchString(state.Fingerprint) || post.Session != state.Session || post.ExportRetire.RootSHA != state.Root {
		return bad("historical-renewed-session")
	}
	r.Ceremony = environmentCreateServiceCeremony{Root: roots["service"], Intermediate: intermediates["service"], BootstrapSessionID: observed.Cleanup.SessionID, BootstrapCredentialSecret: "service-bootstrap-" + observed.Cleanup.SessionID, BootstrapCredentialNamespace: r.Intent.Namespaces[0], BootstrapCredentialNamespaces: slices.Clone(r.Intent.Namespaces), BootstrapClaim: r.Intent.Claim, CustodyReader: observed.Reader, CleanupBinding: observed.Cleanup, CredentialUIDs: observed.UIDs, RootCRLSecret: "pki-create-service-root-crl"}
	crl, proof, err := readCreateRootCRLRecovery(store, e.Journal.PlanSHA256)
	if err != nil || crl == nil || proof == nil || crl.Plan.Root.ID != r.Ceremony.Root.ID {
		return bad("effective-root-crl")
	}
	r.Ceremony.RootCRLFile, r.Ceremony.RootCRLPEM = proof.File, crl.Plan.Signed.PEM
	p.Materials["accepted-service-core-managed-service-root-crl-recovery"] = deploymentplan.Digest(crl)
	p.Materials["accepted-service-core-managed-service-root-crl-proof"] = deploymentplan.Digest(proof)
	for _, stage := range []struct {
		key  string
		size int
	}{{"kubernetes/service-session-original-key-renewal-" + review, 1}, {"kubernetes/service-custody-reader-renewal-" + review, 1}, {"kubernetes/service-credential-secrets-renewal-" + review, 7}, {"kubernetes/transport-enrollment-issuer", 16}, {"kubernetes/transport-server-enrollment", 4}} {
		plan, j, err := authenticatedManagedTransportSourceStage(store, e, stage.key, stage.size, p.Materials)
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(stage.key, "kubernetes/service-session-original-key-renewal-") {
			r.Original, r.Journal = plan, j
			var preview deploymentplan.Plan
			if readExecutionJSON(filepath.Join(store.Root, "deployment/create/openbao-recovery", review, "used-bootstrap-renewal-plan.json"), &preview) != nil || preview.SHA256() != admitted.Plan.Materials["accepted-service-core-used-renewal-plan"] {
				return bad("admitted-renewal-preview")
			}
			normalized := *plan
			normalized.BindingsSHA256 = preview.BindingsSHA256
			if normalized.SHA256() != preview.SHA256() {
				return bad("accepted-renewal-plan")
			}
		}
		if strings.HasPrefix(stage.key, "kubernetes/service-custody-reader-renewal-") {
			containers := certIssuerObjectList(environmentPKIPod(plan.Resources[0].Desired)["containers"])
			if len(containers) != 1 {
				return bad("original-custody-container")
			}
			r.RenewedCertPath = mqttFoundationEnv(deploymentplan.Map(containers[0]), "PKI_BOOTSTRAP_CERT_PATH")
		}
		if stage.key == "kubernetes/transport-enrollment-issuer" {
			r.Trust, r.TrustJournal = plan, j
		}
	}
	if !strings.HasPrefix(r.RenewedCertPath, "/run/pki-bootstrap/renew-") || !strings.HasSuffix(r.RenewedCertPath, "/bootstrap.crt") {
		return bad("renewed-certificate-path")
	}
	var clientProof managedTransportPreservedClients
	if readExecutionJSON(filepath.Join(store.Root, path, "session-renewal", review, "managed-clients-preserved.json"), &clientProof) != nil {
		return bad("preserved-client-proof")
	}
	pins, err := loadManagedTransportOriginalCustodyPins(store, e, clientProof, state.Session, p.Materials)
	if err != nil {
		return nil, err
	}
	r.OriginalKeySHA, r.OriginalCSRSHA = pins["bootstrap.key"], pins["bootstrap.csr"]
	partial.ClientsSHA, partial.ObservedSHA = clientProof.Clients, environmentPreflightBytesSHA256(raw)
	r.RenewedIntent = r.Intent
	r.RenewedIntent.SessionID = state.Session
	r.PostServiceRenewal = false
	r.SourceRenewalStage = "kubernetes/service-session-original-key-renewal-" + review
	seen := map[string]bool{}
	for _, entry := range before.Entries {
		const prefix = "kubernetes/service-credential-secrets-renewal-"
		if !strings.HasPrefix(entry.Key, prefix) {
			continue
		}
		oldReview := strings.TrimPrefix(entry.Key, prefix)
		if _, _, err := loadAdmittedCreateRecovery(store, e, oldReview); err != nil {
			return bad("retained-seed-admission")
		}
		var old createServiceSessionObserved
		if readExecutionJSON(filepath.Join(store.Root, path, "session-renewal", oldReview, "observed.json"), &old) != nil || validateEnvironmentBootstrapCleanupBinding(&old.Cleanup, cfg.Environment) != nil {
			return bad("retained-seed-history")
		}
		if old.Cleanup.SessionID == state.Session || old.Cleanup.SessionID == r.Intent.SessionID || seen[old.Cleanup.SessionID] {
			continue
		}
		plan, j, err := acceptedCreateStage(store, e, entry.Key)
		if err != nil {
			return nil, err
		}
		for _, ns := range r.Intent.Namespaces {
			id := "Secret/" + ns + "/service-bootstrap-" + old.Cleanup.SessionID
			bound := false
			for _, resource := range plan.Resources {
				if resource.ID == id && j.Owners[id] == old.UIDs[ns] {
					bound = true
				}
			}
			if !bound {
				return bad("retained-seed-copy-owner")
			}
		}
		seen[old.Cleanup.SessionID] = true
		r.StaleSeeds = append(r.StaleSeeds, old)
	}
	if !slices.ContainsFunc(r.StaleSeeds, func(old createServiceSessionObserved) bool { return old.Cleanup.SessionID == clientProof.Original }) {
		return bad("source-expired-seed-history")
	}
	image, selection, err := loadManagedApplicationHistoricalGrantImage(store, e.Journal.PlanSHA256, admitted.Plan.Materials)
	if err != nil {
		return nil, err
	}
	r.GrantImage = image
	p.Materials["accepted-service-core-trust-repair-selection"], p.Materials["accepted-service-core-trust-repair-image"] = selection, deploymentplan.Digest(image)
	p.Materials["accepted-service-core-managed-partial-stale-seeds"] = deploymentplan.Digest(r.StaleSeeds)
	p.Materials["accepted-service-core-managed-partial-source"] = deploymentplan.Digest(admitted)
	p.Materials["accepted-service-core-managed-partial-original-plan"] = original.SHA256()
	p.Materials["accepted-service-core-managed-partial-original-journal"] = deploymentplan.Digest(journal)
	return r, nil
}

func loadManagedApplicationHistoricalGrantImage(store secretStore, parent string, pins map[string]string) (environmentReleaseImage, string, error) {
	bad := func() (environmentReleaseImage, string, error) {
		return environmentReleaseImage{}, "", createProviderRecoveryBlock("managed application original repair image publication differs from admitted renewal")
	}
	var selected struct {
		Schema      string `json:"schema"`
		Parent      string `json:"parent_plan_sha256"`
		Revision    string `json:"source_revision"`
		RunID       int64  `json:"run_id"`
		Attempt     int    `json:"run_attempt"`
		ManifestSHA string `json:"manifest_sha256"`
	}
	raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment/repairs/service-bootstrap/selection.json"), true)
	if err != nil || json.Unmarshal(raw, &selected) != nil || selected.Schema != "rtk-cloud.create-service-trust-repair-selection/v1" || selected.Parent != parent || !deploymentUpgradeSHA.MatchString(selected.Revision) || selected.RunID <= 0 || selected.Attempt <= 0 || !environmentPreflightDigest.MatchString(selected.ManifestSHA) || environmentPreflightBytesSHA256(raw) != pins["accepted-service-core-trust-repair-selection"] {
		return bad()
	}
	manifest, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "deployment/repairs/service-bootstrap", selected.Revision, "image-manifest.json"), true)
	if err != nil || environmentPreflightBytesSHA256(manifest) != selected.ManifestSHA {
		return bad()
	}
	image, err := selectEnvironmentPublishedImage(manifest, "hkt999rtk/rtk_video_cloud", selected.RunID, selected.Attempt, selected.Revision, "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api", false)
	if err != nil || deploymentplan.Digest(image) != pins["accepted-service-core-trust-repair-image"] {
		return bad()
	}
	return image, environmentPreflightBytesSHA256(raw), nil
}
