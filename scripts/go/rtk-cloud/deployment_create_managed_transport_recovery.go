package main

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// This profile resumes only after all native CA ceremonies and public issuer
// publication. It never repeats enrollment, activation or custody creation.
type createManagedTransportRecovery struct {
	Partial                                                             *createManagedTransportPartialRecovery `json:"Partial,omitempty"`
	SessionRenewal                                                      *createManagedSessionRecovery          `json:"SessionRenewal,omitempty"`
	SourceReview                                                        string
	DeviceReady, Device, AppRoot, AppIntermediate, AppIntermediateReady environmentCreateRegisteredIssuer
}

func appTrustForwardReview(key string) string {
	const prefix = "kubernetes/app-root-trust-broker-forward-"
	if strings.HasPrefix(key, prefix) && environmentPreflightDigest.MatchString(strings.TrimPrefix(key, prefix)) {
		return strings.TrimPrefix(key, prefix)
	}
	return ""
}

func isManagedTransportCheckpoint(j environmentCreateJournal) bool {
	if len(j.Entries) == 0 {
		return false
	}
	seen := map[string]bool{}
	appForward, role := 0, false
	for _, e := range j.Entries {
		if seen[e.Key] || e.State != "completed" || e.CompletedAt.IsZero() || len(e.Receipt) == 0 || !environmentPreflightDigest.MatchString(e.IntentSHA256) {
			return false
		}
		seen[e.Key] = true
		if appTrustForwardReview(e.Key) != "" {
			appForward++
		}
		if strings.HasPrefix(e.Key, "openbao-role-certissuer-"+j.Environment+"-") {
			role = true
		}
		for _, prefix := range []string{"kubernetes/transport-enrollment-issuer", "kubernetes/transport-server-enrollment", "kubernetes/transport-managed-hosts", "kubernetes/managed-application-owners", "kubernetes/managed-complete-issuer"} {
			if strings.HasPrefix(e.Key, prefix) {
				return false
			}
		}
	}
	for _, key := range []string{"device-root-bootstrap", "kubernetes/device-root-active", "kubernetes/device-root-trust", "kubernetes/app-root-trust", "kubernetes/app-intermediate-trust", "pki-reserve-app-root", "pki-reserve-app-intermediate", "kubernetes/transport-client-enrollment"} {
		if !seen[key] {
			return false
		}
	}
	return appForward == 1 && role
}

func loadManagedTransportRecoverySource(store secretStore, e *environmentCreateExecution, r *createServiceSessionRecovery) (string, createFoundationRecoveryArtifact, error) {
	var empty createFoundationRecoveryArtifact
	bad := func() (string, createFoundationRecoveryArtifact, error) {
		return "", empty, createProviderRecoveryBlock("managed transport continuation requires the admitted original App trust forward and its authenticated source checkpoint")
	}
	if !isManagedTransportCheckpoint(e.Journal) {
		return bad()
	}
	review := ""
	for _, entry := range e.Journal.Entries {
		if x := appTrustForwardReview(entry.Key); x != "" {
			review = x
		}
	}
	a, before, err := loadAdmittedCreateRecovery(store, e, review)
	if err != nil || !isAppTrustCheckpoint(before) || a.Plan.Materials["accepted-service-core-app-trust-forward-plan"] == "" {
		return bad()
	}
	historical := *e
	historical.Journal = before
	identity, source, err := loadPostServiceMQTTSource(store, &historical, r)
	if err != nil {
		return "", empty, err
	}
	if r.AppTrustRecovery == nil || r.DeviceTrustRecovery == nil {
		return bad()
	}
	// The successful forward must be the actual stage named by the original
	// failed trust entry; no alternate native application owner is admitted.
	bound := false
	for _, entry := range e.Journal.Entries {
		if entry.Key == "kubernetes/app-root-trust" {
			var receipt map[string]string
			bound = json.Unmarshal(entry.Receipt, &receipt) == nil && receipt["actual_qualified_forward_stage"] == "kubernetes/app-root-trust-broker-forward-"+review && environmentPreflightDigest.MatchString(receipt["original_failed_plan_sha256"])
		}
	}
	if !bound {
		return bad()
	}
	r.ManagedTransportRecovery = &createManagedTransportRecovery{SourceReview: review}
	return identity, source, nil
}

func originalReadyIntermediate(store secretStore, e *environmentCreateExecution, active environmentCreateRegisteredIssuer) (environmentCreateRegisteredIssuer, error) {
	var document map[string]any
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap", active.Domain, "intermediate-ready.json"), &document) != nil {
		return environmentCreateRegisteredIssuer{}, createProviderRecoveryBlock("original intermediate READY public trust material is unavailable")
	}
	ready, err := environmentCreateRegisteredIssuerFromMap(document, active.RootFingerprint)
	if err != nil || ready.Status != "ready" || ready.ID != active.ID || ready.Environment != active.Environment || ready.Domain != active.Domain || ready.Kind != active.Kind || ready.Certificate != active.Certificate || ready.ChainPEM != active.ChainPEM || ready.BundleVersion != active.BundleVersion || ready.Reference != active.Reference || document["parent_issuer_id"] != active.Document["parent_issuer_id"] {
		return ready, createProviderRecoveryBlock("original intermediate READY public trust material differs from the activated identity")
	}
	return ready, nil
}

// Existing publications are authoritative outputs of completed ceremonies.
// Compare their exact native documents; never rewrite or synthesize a receipt.
func verifyEnvironmentCreateRegisteredIssuers(store secretStore, roots, intermediates map[string]environmentCreateRegisteredIssuer, materials map[string]string) error {
	for _, domain := range managedPKIDomains {
		root := roots[domain]
		if root.Status != "active" || root.Environment != store.Environment || root.Domain != domain || root.Document == nil {
			return createProviderRecoveryBlock("published managed issuers require unchanged native ACTIVE roots")
		}
		expected := deploymentplan.Object{"root": root.Document}
		if intermediate, ok := intermediates[domain]; ok {
			if intermediate.Status != "active" || intermediate.Environment != store.Environment || intermediate.Domain != domain || intermediate.RootFingerprint != root.RootFingerprint || intermediate.Document["parent_issuer_id"] != root.ID {
				return createProviderRecoveryBlock("published managed intermediate differs from the original ACTIVE lineage")
			}
			expected["intermediate"] = intermediate.Document
		} else if domain != "device" {
			return createProviderRecoveryBlock("published managed lineage lacks its original ACTIVE intermediate")
		}
		raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "pki", domain, "issuer.json"), true)
		var actual deploymentplan.Object
		if err != nil || json.Unmarshal(raw, &actual) != nil || deploymentplan.Digest(actual) != deploymentplan.Digest(expected) {
			return createProviderRecoveryBlock("existing public issuer file differs from the original native ACTIVE lineage")
		}
		if materials != nil {
			materials["accepted-service-core-managed-published-"+domain] = environmentPreflightBytesSHA256(raw)
		}
	}
	return nil
}

func prepareManagedTransportRecovery(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery) error {
	d := r.ManagedTransportRecovery
	if d == nil || r.AppTrustRecovery == nil || r.DeviceTrustRecovery == nil {
		return createProviderRecoveryBlock("managed transport continuation requires its qualified original ACTIVE lineages and original bootstrap custody")
	}
	ready, device, err := loadActivatedDeviceForAppTrust(ctx, cfg, store, e, b, operator, p)
	if err != nil {
		return err
	}
	d.DeviceReady, d.Device = ready, device
	r.AppTrustRecovery.DeviceReady, r.AppTrustRecovery.Device = ready, device
	roots := map[string]environmentCreateRegisteredIssuer{"service": r.Ceremony.Root, "device": device}
	intermediates := map[string]environmentCreateRegisteredIssuer{"service": r.Ceremony.Intermediate}
	for _, domain := range []string{"mqtt", "openbao_tls", "app"} {
		root, op, err := loadOriginalReadyCreateRoot(ctx, cfg, store, e, b, operator, p, domain, true)
		if err != nil {
			return err
		}
		intermediate, err := loadOriginalActiveCreateIntermediate(ctx, cfg, store, e, b, operator, root, findEnvironmentCreateDomainPolicy(p.Plan.Bootstrap, domain))
		if err != nil {
			return err
		}
		roots[domain], intermediates[domain] = root, intermediate
		switch domain {
		case "mqtt":
			r.MQTTRoot, r.MQTTIntermediate = &root, &intermediate
		case "openbao_tls":
			r.OpenBaoRoot, r.OpenBaoOperation, r.OpenBaoIntermediate = &root, &op, &intermediate
		case "app":
			d.AppRoot, d.AppIntermediate = root, intermediate
			r.AppTrustRecovery.Root = root
		}
	}
	d.AppIntermediateReady, err = originalReadyIntermediate(store, e, d.AppIntermediate)
	if err != nil {
		return err
	}
	if err = verifyEnvironmentCreateRegisteredIssuers(store, roots, intermediates, p.Materials); err != nil {
		return err
	}
	if r.PostServiceRenewal {
		if err := prepareManagedSessionRecovery(ctx, cfg, store, e, b, p, r); err != nil {
			return err
		}
	}
	_, management, err := foundationRecoveryMaterials(store, e.Journal.PlanSHA256, createRecoveryTransportMinimumLife(ctx, 0, true))
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
	for _, v := range listeners.Resources {
		runtime.InitialListeners = append(runtime.InitialListeners, deploymentplan.Definition{Phase: v.Phase, Object: deploymentplan.Clone(v.Desired)})
	}
	runtime.InitialListeners, _, err = initialCertIssuerAuthDefinitions(cfg, runtime.InitialListeners)
	if err != nil {
		return err
	}
	runtime.InitialListeners, err = configureCreateBrokerServiceConsumer(p.Plan.Bootstrap, runtime.InitialListeners)
	if err != nil {
		return err
	}
	// Future identity references are deterministic; the original cleanup binding
	// remains historical evidence until native execution returns the new UID.
	prospectiveService := r.Ceremony
	if d.SessionRenewal != nil {
		prospectiveService.BootstrapSessionID = r.RenewedIntent.SessionID
		prospectiveService.BootstrapCredentialSecret = "service-bootstrap-" + r.RenewedIntent.SessionID
		p.Materials["accepted-service-core-managed-preview-source-cleanup"] = deploymentplan.Digest(r.Ceremony.CleanupBinding)
	}
	definitions, err := renderEnvironmentCreateTransportEnrollmentIssuer(ctx, cfg, p.Plan.Bootstrap, runtime, prospectiveService, roots, intermediates)
	if err != nil {
		return err
	}
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return err
	}
	r.Candidate, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("transport-enrollment-issuer", definitions)}, definitions, live)
	if err != nil {
		return err
	}
	p.Materials["accepted-service-core-managed-transport-source"] = d.SourceReview
	p.Materials["accepted-service-core-managed-transport-forward-plan"] = r.Candidate.SHA256()
	bindAcceptedTransportRootMaterials(p.Materials)
	for key, value := range p.Materials {
		if strings.HasPrefix(key, "original-app-root-") {
			p.Materials["accepted-service-core-"+key] = value
			delete(p.Materials, key)
		}
	}
	return e.Check(ctx)
}

// Used only for the native bootstrap executor after the six-gate qualification.
func managedRecoveryApplicationConsumers(cfg deploymentConfig, p environmentCreateBootstrapPlan, r *createServiceSessionRecovery, applications []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	d := r.ManagedTransportRecovery
	if d == nil {
		return nil, createProviderRecoveryBlock("managed transport continuation is not qualified")
	}
	updated, err := configureEnvironmentCreateDeviceConsumers(cfg, p, d.DeviceReady, applications)
	if err != nil {
		return nil, err
	}
	return configureEnvironmentCreateAppConsumers(p, d.AppRoot, []environmentCreateRegisteredIssuer{d.AppIntermediateReady}, updated)
}

// Renewal qualification can use the authenticated Kubernetes/SQL channel even
// when temporary TLS has expired. This independently binds every native ACTIVE
// issuer to the original completed journal and existing public publication.
func verifyManagedTransportNativeRegistry(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan, terminal ...*createServiceSessionRecovery) error {
	terminalSelected := false
	if len(terminal) > 0 {
		if len(terminal) != 1 {
			return createOpenBaoACKBlock("terminal-finalizer-native-registry-source")
		}
		if err := verifyManagedLateTerminalNativeSource(ctx, cfg, store, e, terminal[0]); err != nil {
			return err
		}
		terminalSelected = true
	}
	if !terminalSelected && !isManagedTransportCheckpoint(e.Journal) && !managedTransportPartialNativeCheckpoint(store, e) && !managedApplicationPartialNativeCheckpoint(store, e) && !managedApplicationInterruptedNativeCheckpoint(store, e) && !managedCorePartialNativeCheckpoint(store, e) && !activeTransportNativeSourceCheckpoint(store, e) && !rootCRLNativeSourceCheckpoint(store, e) && !managedLateSessionNativeCheckpoint(store, e) && !mqttRootCRLNativeSourceCheckpoint(store, e) && !openBaoTLSRootCRLNativeSourceCheckpoint(store, e) {
		return createProviderRecoveryBlock("native ACTIVE issuer verification requires the completed original CA checkpoint")
	}
	review := ""
	for _, entry := range e.Journal.Entries {
		const prefix = "kubernetes/device-root-ready-order-recovery-"
		if strings.HasPrefix(entry.Key, prefix) {
			review = strings.TrimPrefix(entry.Key, prefix)
		}
	}
	if !environmentPreflightDigest.MatchString(review) {
		return createProviderRecoveryBlock("original accepted Device READY stage is unavailable")
	}
	p.Materials["accepted-service-core-device-trust-source"] = review
	if err := verifyDeviceTrustRegistry(ctx, cfg, store, e, b, p); err != nil {
		return err
	}
	roots, intermediates := map[string]environmentCreateRegisteredIssuer{}, map[string]environmentCreateRegisteredIssuer{}
	expected := map[string]map[string]any{}
	for _, domain := range managedPKIDomains {
		var publication map[string]map[string]any
		raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, "pki", domain, "issuer.json"), true)
		if err != nil || json.Unmarshal(raw, &publication) != nil {
			return createProviderRecoveryBlock("published ACTIVE issuer material is unavailable")
		}
		root, err := environmentCreateRegisteredIssuerFromMap(publication["root"], "")
		if err != nil || root.Status != "active" || root.Domain != domain || root.Environment != cfg.Environment {
			return createProviderRecoveryBlock("published native Root identity is inconsistent")
		}
		roots[domain], expected[root.ID] = root, root.Document
		if domain != "device" {
			intermediate, err := environmentCreateRegisteredIssuerFromMap(publication["intermediate"], root.RootFingerprint)
			if err != nil || intermediate.Status != "active" || intermediate.Domain != domain || intermediate.Environment != cfg.Environment || intermediate.Document["parent_issuer_id"] != root.ID {
				return createProviderRecoveryBlock("published native intermediate identity is inconsistent")
			}
			intermediates[domain], expected[intermediate.ID] = intermediate, intermediate.Document
		}
	}
	if err := verifyEnvironmentCreateRegisteredIssuers(store, roots, intermediates, p.Materials); err != nil {
		return err
	}
	if err := verifyManagedPublicationJournal(store, e, roots, intermediates); err != nil {
		return err
	}
	raw, err := readEnvironmentCreateRegistryJSON(ctx, cfg, environmentCreateBootstrapRuntime{Backend: b, Execution: e}, "SELECT json_agg(json_build_object('id',id,'environment',environment,'domain',domain,'kind',kind,'status',status,'document',document) ORDER BY id) FROM pki_issuers")
	var observed []struct {
		ID, Environment, Domain, Kind, Status string
		Document                              map[string]any
	}
	if err != nil || json.Unmarshal(raw, &observed) != nil || len(observed) != len(expected) {
		return createProviderRecoveryBlock("native ACTIVE issuer registry is unavailable or differs from original publication")
	}
	for _, issuer := range observed {
		doc := expected[issuer.ID]
		if doc == nil || issuer.Environment != cfg.Environment || issuer.Domain != doc["trust_domain"] || issuer.Kind != doc["kind"] || issuer.Status != "active" || deploymentplan.Digest(issuer.Document) != deploymentplan.Digest(doc) {
			return createProviderRecoveryBlock("native ACTIVE issuer registry differs from original public identity")
		}
		delete(expected, issuer.ID)
	}
	if len(expected) != 0 {
		return createProviderRecoveryBlock("native ACTIVE issuer registry lacks an original published identity")
	}
	// The local Device ACTIVE document must equal the accepted native Job output.
	var saved map[string]any
	if readExecutionJSON(filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap/device/root-active.json"), &saved) != nil || deploymentplan.Digest(saved) != deploymentplan.Digest(roots["device"].Document) {
		return createProviderRecoveryBlock("original Device ACTIVE publication changed")
	}
	plan, j, err := acceptedCreateStage(store, e, "kubernetes/device-root-active")
	if err != nil {
		return err
	}
	verified := false
	for _, v := range plan.Resources {
		if v.Desired["kind"] != "Job" {
			continue
		}
		actual, err := b.Read(ctx, v.ID)
		if err != nil || actual == nil || deploymentplan.Map(actual["metadata"])["uid"] != j.Owners[v.ID] || !deploymentplan.Matches(v.Desired, actual) {
			return createProviderRecoveryBlock("original Device ACTIVE Job owner changed")
		}
		containers := certIssuerObjectList(environmentPKIPod(v.Desired)["containers"])
		if len(containers) != 1 {
			return createProviderRecoveryBlock("original Device ACTIVE Job command changed")
		}
		result, err := b.command(ctx, nil, "--namespace", cfg.Values["CLOUD_STACK_NAME"]+"-video-cloud", "logs", "job/"+deploymentplan.String(deploymentplan.Map(v.Desired["metadata"])["name"]), "--container", deploymentplan.String(deploymentplan.Map(containers[0])["name"]), "--tail=1")
		var value struct {
			Issuer map[string]any `json:"issuer"`
		}
		verified = err == nil && json.Unmarshal(result, &value) == nil && deploymentplan.Digest(value.Issuer) == deploymentplan.Digest(saved)
	}
	if !verified {
		return createProviderRecoveryBlock("original Device ACTIVE native result is unavailable")
	}
	p.Materials["accepted-service-core-managed-active-registry"] = environmentPreflightBytesSHA256(raw)
	return e.Check(ctx)
}

func verifyManagedPublicationJournal(store secretStore, e *environmentCreateExecution, roots, intermediates map[string]environmentCreateRegisteredIssuer) error {
	bad := func(domain, kind, stage string) error {
		return createProviderRecoveryBlock(fmt.Sprintf("published ACTIVE issuer lacks original evidence: domain=%s kind=%s stage=%s", domain, kind, stage))
	}
	for _, domain := range managedPKIDomains {
		if domain == "device" {
			continue
		} // Bound independently to the native Job.
		issuers := map[string]environmentCreateRegisteredIssuer{"root": roots[domain], "intermediate": intermediates[domain]}
		for _, kind := range []string{"root", "intermediate"} {
			issuer := issuers[kind]
			var operation environmentCreateIssuerOperation
			for _, entry := range e.Journal.Entries {
				if entry.Key == "pki-reserve-"+domain+"-"+kind && json.Unmarshal(entry.Receipt, &operation) != nil {
					return bad(domain, kind, "reservation-decode")
				}
			}
			if operation.IssuerID != issuer.ID || !environmentBootstrapOwnerUUID.MatchString(operation.ID) {
				return bad(domain, kind, "reservation-pin")
			}
			activated := false
			for _, entry := range e.Journal.Entries {
				if entry.Key == "pki-activate-"+operation.ID {
					var document map[string]any
					activated = json.Unmarshal(entry.Receipt, &document) == nil && deploymentplan.Digest(document) == deploymentplan.Digest(issuer.Document)
				}
			}
			if !activated {
				return bad(domain, kind, "activation-receipt")
			}
			var saved map[string]any
			if readExecutionJSON(filepath.Join(store.Root, "deployment/create", e.Journal.PlanSHA256, "bootstrap", domain, kind+"-active.json"), &saved) != nil {
				return bad(domain, kind, "active-file-decode")
			}
			if deploymentplan.Digest(saved) != deploymentplan.Digest(issuer.Document) {
				return bad(domain, kind, "active-file-pin")
			}
		}
		root := roots[domain]
		var rootOperation environmentCreateIssuerOperation
		for _, entry := range e.Journal.Entries {
			if entry.Key == "pki-reserve-"+domain+"-root" && json.Unmarshal(entry.Receipt, &rootOperation) != nil {
				return bad(domain, "root", "custody-reservation-decode")
			}
		}
		// Model the complete native rtk-root-custody/v1 schema so strict JSON
		// decoding retains all legitimate signing/operator/time metadata.
		var manifest struct {
			Schema      string            `json:"schema"`
			Environment string            `json:"environment"`
			Domain      string            `json:"trust_domain"`
			Issuer      string            `json:"root_issuer_id"`
			Fingerprint string            `json:"certificate_fingerprint_sha256"`
			Operator    string            `json:"operator_user_id"`
			Operation   string            `json:"ceremony_operation_id"`
			Reference   string            `json:"signer_reference"`
			KeyFormat   string            `json:"key_format"`
			Paths       map[string]string `json:"paths"`
			Hashes      map[string]string `json:"file_sha256"`
			CreatedAt   time.Time         `json:"created_at"`
			VerifiedAt  time.Time         `json:"verified_at"`
		}
		if readExecutionJSON(filepath.Join(store.Root, "pki/custody/roots", root.ID, "manifest.json"), &manifest) != nil {
			return bad(domain, "root", "custody-manifest-decode")
		}
		if manifest.Schema != "rtk-root-custody/v1" || manifest.Environment != store.Environment || manifest.Domain != domain || manifest.Issuer != root.ID || manifest.Fingerprint != root.RootFingerprint || manifest.Operation != rootOperation.ID || len(manifest.Paths) != 4 || len(manifest.Hashes) != 4 {
			return bad(domain, "root", "custody-manifest-pin")
		}
		paths := map[string]string{"key": filepath.ToSlash(filepath.Join("pki/custody/roots", root.ID, "ca-key.encrypted.pem")), "certificate": filepath.ToSlash(filepath.Join("pki/custody/roots", root.ID, "certificate.pem")), "csr": filepath.ToSlash(filepath.Join("pki/custody/roots", root.ID, "csr.pem")), "passphrase": "pki/custody/" + domain + "-root-v1-key-passphrase"}
		for key, path := range paths {
			raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, path), true)
			if err != nil {
				return bad(domain, "root", "custody-file-read-"+key)
			}
			if manifest.Paths[key] != path || manifest.Hashes[key] != environmentPreflightBytesSHA256(raw) || key == "certificate" && string(raw) != root.Certificate {
				return bad(domain, "root", "custody-file-pin-"+key)
			}
		}
	}
	return nil
}

// The fully published continuation binds native ACTIVE documents and the exact
// private publication bytes already frozen by its original-lineage loader.
func qualifyEnvironmentCreateManagedTransportCapabilities(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, operator map[string]string, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery) error {
	if e == nil || p == nil || r == nil || r.ManagedTransportRecovery == nil || r.MQTTRoot == nil || r.MQTTIntermediate == nil || r.OpenBaoRoot == nil || r.OpenBaoIntermediate == nil {
		return createProviderRecoveryBlock("published issuer capability allowance requires the exact all-CA-ACTIVE managed checkpoint")
	}
	if r.ManagedLateRemaining != nil && r.ManagedLateRemaining.TerminalFinalizerCompletion != nil {
		// The terminal checkpoint deliberately retains the original pending
		// finalizer. Authenticate it before considering any legacy parser.
		if err := verifyManagedLateTerminalNativeSource(ctx, cfg, store, e, r); err != nil {
			return err
		}
	} else if !isManagedTransportCheckpoint(e.Journal) && !managedTransportPartialNativeCheckpoint(store, e) && !managedApplicationPartialNativeCheckpoint(store, e) && !managedApplicationInterruptedNativeCheckpoint(store, e) && !managedCorePartialNativeCheckpoint(store, e) && !managedLateSessionNativeCheckpoint(store, e) && !mqttRootCRLNativeSourceCheckpoint(store, e) && !openBaoTLSRootCRLNativeSourceCheckpoint(store, e) {
		return createProviderRecoveryBlock("published issuer capability allowance requires the exact all-CA-ACTIVE managed checkpoint")
	}
	d := r.ManagedTransportRecovery
	admitted, source, err := loadAdmittedCreateRecovery(store, e, d.SourceReview)
	if err != nil || !isAppTrustCheckpoint(source) || !environmentPreflightDigest.MatchString(admitted.Plan.Materials["accepted-service-core-app-trust-forward-plan"]) {
		return createProviderRecoveryBlock("published issuer capability allowance requires the authenticated original App forward")
	}
	roots := map[string]environmentCreateRegisteredIssuer{"service": r.Ceremony.Root, "device": d.Device, "mqtt": *r.MQTTRoot, "openbao_tls": *r.OpenBaoRoot, "app": d.AppRoot}
	intermediates := map[string]environmentCreateRegisteredIssuer{"service": r.Ceremony.Intermediate, "mqtt": *r.MQTTIntermediate, "openbao_tls": *r.OpenBaoIntermediate, "app": d.AppIntermediate}
	if err := verifyEnvironmentCreateRegisteredIssuers(store, roots, intermediates, nil); err != nil {
		return err
	}
	if err := verifyManagedPublicationJournal(store, e, roots, intermediates); err != nil {
		return err
	}
	publications := map[string]string{}
	for _, domain := range managedPKIDomains {
		relative := "pki/" + domain + "/issuer.json"
		raw, err := readBoundedEnvironmentFile(filepath.Join(store.Root, relative), true)
		hash := environmentPreflightBytesSHA256(raw)
		if err != nil || p.Materials["accepted-service-core-managed-published-"+domain] != hash {
			return createProviderRecoveryBlock("managed published issuer bytes changed after original-lineage qualification")
		}
		publications[relative] = hash
	}
	routingSHA := ""
	if r.ManagedLateSession != nil {
		proof, err := readManagedLateRoutingCapability(ctx, cfg, store, e, b, r, p)
		if err != nil {
			return err
		}
		routingSHA = proof.FileSHA
	} else if r.ManagedCorePartial != nil {
		proof, err := readManagedCoreRoutingPublication(ctx, store, e, b, r.ManagedCorePartial)
		if err != nil || p.Materials[managedCoreRoutingFileMaterial] != proof.FileSHA || p.Materials[managedCoreRoutingProofMaterial] != deploymentplan.Digest(proof) {
			return createProviderRecoveryBlock("accepted App routing publication changed after exact stopped-core qualification")
		}
		routingSHA = proof.FileSHA
	}
	return qualifyEnvironmentCreateBootstrapCapabilitiesWithRouting(ctx, cfg, p.Plan.Bootstrap, store, operator, publications, routingSHA, roots["service"].ID, roots["mqtt"].ID, roots["openbao_tls"].ID, roots["app"].ID)
}
