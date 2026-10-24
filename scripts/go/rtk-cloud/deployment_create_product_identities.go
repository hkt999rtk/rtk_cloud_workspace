package main

import (
	"context"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// A declared native lifecycle output, never a placeholder Secret or certificate.
// Issuer/root pins are filled from the registered lineage for an existing PKI;
// a brand-new ceremony instead binds its exact qualified Service issuer policy.
type environmentCreateProductIdentity struct {
	ID           string `json:"resource_id"`
	Subject      string `json:"subject"`
	Claim        string `json:"dedicated_claim"`
	Service      string `json:"service"`
	Instance     string `json:"instance"`
	Option       string `json:"option"`
	RootID       string `json:"root_issuer_id,omitempty"`
	RootSHA256   string `json:"root_sha256,omitempty"`
	IssuerID     string `json:"issuer_id,omitempty"`
	PolicySHA256 string `json:"policy_sha256,omitempty"`
}

func environmentCreateProductIdentityIntent(cfg deploymentConfig) ([]environmentCreateProductIdentity, error) {
	if cfg.Values["CLOUD_STACK_NAME"] != "video-cloud-"+cfg.Environment {
		return nil, environmentCreateBootstrapBlocker("product identity producers require the selected exact environment stack")
	}
	var out []environmentCreateProductIdentity
	for _, target := range []struct{ flag, role, secret, service, instance, option string }{
		{"MQTT_FOUNDATION_REGISTRATION_ENABLED", "mqtt", mqttFoundationIdentitySecretName, "mqtt", "mqtt-foundation-0", "mqtt"},
		{"SHADOW_WORKER_REGISTRATION_ENABLED", "shadow", shadowWorkerIdentitySecretName, "shadow", "shadow-worker-0", "iot_shadow"},
		{"OTA_SERVICE_REGISTRATION_ENABLED", "ota", otaServiceIdentitySecretName, "ota", "ota-service-0", "ota"},
		{"VIDEO_STORAGE_SERVICE_REGISTRATION_ENABLED", "video-storage", videoStorageServiceIdentitySecretName, "video-storage", "video-storage-service-0", "video_storage"},
		{"WEBRTC_SERVICE_REGISTRATION_ENABLED", "webrtc", webrtcServiceIdentitySecretName, "webrtc", "webrtc-service-0", "video_streaming"},
		{"LOGGER_SERVICE_REGISTRATION_ENABLED", "logger", loggerServiceIdentitySecretName, "logger", "logger-service-0", "device_logging"},
	} {
		enabled := firstNonEmpty(cfg.Values[target.flag], cfg.Values["LKE_"+target.flag], "false")
		if enabled != "true" && enabled != "false" {
			return nil, environmentCreateBootstrapBlocker("product identity enablement must be explicit boolean intent: " + target.flag)
		}
		if enabled == "true" {
			out = append(out, environmentCreateProductIdentity{ID: "Secret/" + cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud/" + target.secret, Subject: "service:" + target.role, Claim: "pki-product-" + target.role + "-identity", Service: target.service, Instance: target.instance, Option: target.option})
		}
	}
	slices.SortFunc(out, func(a, b environmentCreateProductIdentity) int { return strings.Compare(a.ID, b.ID) })
	return out, nil
}

func environmentCreatePlannedServiceSubjects(cfg deploymentConfig, p environmentCreateBootstrapPlan) ([]string, error) {
	core, err := environmentCreateBootstrapSubjects(cfg)
	if err != nil {
		return nil, err
	}
	policy := findEnvironmentCreateDomainPolicy(p, "service")
	if len(p.ProductIdentities) == 0 {
		return core, nil // Historical plans retain their original nine targets.
	}
	expected, err := environmentCreateProductIdentityIntent(cfg)
	if err != nil || len(expected) != len(p.ProductIdentities) {
		return nil, environmentCreateBootstrapBlocker("product identity outputs differ from the qualified enabled targets")
	}
	for i, product := range p.ProductIdentities {
		base := product
		base.RootID, base.RootSHA256, base.IssuerID, base.PolicySHA256 = "", "", "", ""
		if deploymentplan.Digest(base) != deploymentplan.Digest(expected[i]) || product.PolicySHA256 != deploymentplan.Digest(policy) {
			return nil, environmentCreateBootstrapBlocker("product identity output or exact Service issuer policy changed")
		}
		core = append(core, product.Subject)
	}
	slices.Sort(core)
	if !slices.Equal(environmentCreateServiceEnrollmentSubjects(policy), core) || !slices.Contains(policy.ServiceClientIDs, "service:deployment-bootstrap") {
		return nil, environmentCreateBootstrapBlocker("new Service session must contain every qualified installed owner and no additional subject")
	}
	return core, nil
}

func renderEnvironmentCreateProductIdentityEnrollment(cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony) ([]deploymentplan.Definition, error) {
	if _, err := environmentCreatePlannedServiceSubjects(cfg, p); err != nil {
		return nil, err
	}
	var out []deploymentplan.Definition
	for _, product := range p.ProductIdentities {
		role := strings.TrimPrefix(product.Subject, "service:")
		job, err := environmentCreateClientEnrollmentDefinition(cfg, p, runtime, service, p.Stack+"-video-cloud", role, product.Claim)
		if err != nil {
			return nil, err
		}
		out = append(out, environmentCreateServiceClaimDefinition(cfg, p.Stack+"-video-cloud", product.Claim), job)
	}
	return out, nil
}

type environmentCreateProductIdentityLifecycle struct {
	OpenBaoTrustProfile                     string `json:",omitempty"`
	ParentSHA256, SourceSHA256, OwnerSHA256 string
	Root, Intermediate                      environmentCreateRegisteredIssuer
	Policy                                  environmentCreateIssuerPolicy
	Outputs                                 []environmentCreateProductIdentity
	RequiresSuccessor                       bool
	SourceDefinitions                       []deploymentplan.Definition
	SourceOwners                            map[string]string
	SourceTrust                             []deploymentplan.Resource
	SignerPolicies                          []string
	Bootstrap                               environmentCreateBootstrapPlan
	StorageClass, TransportCAPEM            string
	RootCustodySHA256                       string
	source                                  environmentCreateJournal
	runtime                                 environmentCreateBootstrapRuntime
	operator                                map[string]string
	registrationCRLs                        *productRegistrationCRLs
}

// Retained target definitions omit live data. Freeze the public trust documents
// and original reader bodies needed for this successor's two explicit reloads.
func bindCreateNormalTailProductSourceDefinitions(l *createNormalTailProductLifecycle, definitions []deploymentplan.Definition, live []deploymentplan.Object, bindings environmentIdentityBindings) error {
	if l == nil || len(definitions) == 0 || deploymentplan.Digest(bindings.Owners) != deploymentplan.Digest(l.SourceOwners) {
		return environmentCreateBootstrapBlocker("product source requires the complete qualified owner catalog")
	}
	index := map[string]deploymentplan.Object{}
	for _, object := range live {
		id := deploymentplan.ID(object)
		if index[id] != nil {
			return environmentCreateBootstrapBlocker("product source inventory contains duplicate owners")
		}
		index[id] = object
	}
	refs := map[string]bool{}
	for _, d := range definitions {
		for _, ref := range environmentResourceReferences(d.Object) {
			refs[ref.ID] = true
		}
	}
	var trust []deploymentplan.Resource
	trustIDs := map[string]bool{}
	for id := range refs {
		object := index[id]
		if !strings.HasPrefix(id, "ConfigMap/") || object == nil {
			continue
		}
		contains := false
		for _, value := range deploymentplan.Map(object["data"]) {
			text, ok := value.(string)
			if !ok {
				continue
			}
			_, present, _, err := productIdentityTrustManifest(l, text, nil, false)
			if err != nil {
				return err
			}
			contains = contains || present
		}
		if !contains {
			continue
		}
		uid := deploymentplan.String(deploymentplan.Map(object["metadata"])["uid"])
		if !environmentBootstrapOwnerUUID.MatchString(uid) || bindings.Owners[id] != uid || deploymentplan.Map(object["metadata"])["deletionTimestamp"] != nil {
			return environmentCreateBootstrapBlocker("Service trust source has no original recorded owner")
		}
		trust = append(trust, deploymentplan.Resource{ID: id, Baseline: deploymentplan.Clone(object)})
		trustIDs[id] = true
	}
	// The legacy registration listener reloads a PEM CRL file, independently
	// of the native JSON trust manifest. Freeze its original Secret for the
	// same signed successor CRL publication, rather than leaving it on old ICA.
	registrationID := createNormalTailAccountRegistrationCRLID(bindings.Stack)
	if l.RequiresSuccessor && refs[registrationID] {
		object := index[registrationID]
		uid := bindings.Owners[registrationID]
		if object == nil || !environmentBootstrapOwnerUUID.MatchString(uid) || deploymentplan.Map(object["metadata"])["uid"] != uid || deploymentplan.Map(object["metadata"])["deletionTimestamp"] != nil {
			return accountRegistrationCRLBlock("new lifecycle registration owner")
		}
		trust = append(trust, deploymentplan.Resource{ID: registrationID, UID: uid, Baseline: deploymentplan.Projection(object)})
		trustIDs[registrationID] = true
	}
	// These startup-only readers must reload the same native owner after its
	// ConfigMap changes. Freeze that exact current body; final target resolution
	// continues to use the separate maintained SourceDefinitions catalog.
	for _, definition := range definitions {
		id := deploymentplan.ID(definition.Object)
		object := index[id]
		if object == nil || bindings.Owners[id] == "" {
			continue
		}
		switch object["kind"] {
		case "Deployment", "StatefulSet", "DaemonSet":
		default:
			continue
		}
		for _, ref := range environmentResourceReferences(object) {
			if !trustIDs[ref.ID] {
				continue
			}
			if !environmentBootstrapOwnerUUID.MatchString(bindings.Owners[id]) || bindings.Owners[id] != deploymentplan.Map(object["metadata"])["uid"] || deploymentplan.Map(object["metadata"])["deletionTimestamp"] != nil {
				return environmentCreateBootstrapBlocker("Service trust reader lacks its original current owner")
			}
			trust = append(trust, deploymentplan.Resource{ID: id, Baseline: deploymentplan.Clone(object)})
			break
		}
	}
	slices.SortFunc(trust, func(a, b deploymentplan.Resource) int { return strings.Compare(a.ID, b.ID) })
	if l.RequiresSuccessor && len(trust) == 0 {
		return environmentCreateBootstrapBlocker("qualified product catalog lacks the original Service bundle distribution")
	}
	l.SourceDefinitions = definitions
	l.SourceTrust = trust
	transport := index["ConfigMap/"+bindings.Stack+"-video-cloud/pki-create-transport-trust"]
	if transport == nil || bindings.Owners[deploymentplan.ID(transport)] != deploymentplan.Map(transport["metadata"])["uid"] {
		return environmentCreateBootstrapBlocker("original management transport trust owner is unavailable")
	}
	l.TransportCAPEM = deploymentplan.String(deploymentplan.Map(transport["data"])["ca.crt"])
	if _, err := parseFirstCertificatePEM([]byte(l.TransportCAPEM)); err != nil {
		return environmentCreateBootstrapBlocker("original management transport authority is malformed")
	}
	if _, _, root, err := productIdentityOpenBaoTrustPins(l); err != nil {
		return err
	} else if root != "" {
		id := "ConfigMap/" + bindings.Stack + "-video-cloud/pki-create-openbao-tls-consumer-trust"
		if err := validateProductIdentityOpenBaoTrust(l, index[id], time.Now().UTC()); err != nil {
			return err
		}
		if !trustIDs[id] {
			l.SourceTrust = append(l.SourceTrust, deploymentplan.Resource{ID: id, UID: bindings.Owners[id], Baseline: deploymentplan.Clone(index[id])})
		}
		slices.SortFunc(l.SourceTrust, func(a, b deploymentplan.Resource) int { return strings.Compare(a.ID, b.ID) })
		l.OpenBaoTrustProfile = productIdentityManagedOpenBaoTrustProfile
	}
	return nil
}

type createNormalTailProductLifecycle = environmentCreateProductIdentityLifecycle

func prepareCreateNormalTailProducts(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, backend environmentKubernetesBackend) (*createNormalTailProductLifecycle, error) {
	if h == nil || h.Execution == nil || h.OriginalPlan == nil || backend.Store != h.Execution.Store || h.Execution.Check(ctx) != nil {
		return nil, environmentCreateBootstrapBlocker("product producer requires the original completed bootstrap handoff")
	}
	outputs, err := environmentCreateProductIdentityIntent(cfg)
	if err != nil || len(outputs) == 0 {
		return nil, err
	}
	store := h.Execution.Store
	operator, err := store.readOperator()
	if err != nil {
		return nil, err
	}
	origin, transport, err := newEnvironmentCreateAccountClient(ctx, cfg, backend, h.Execution)
	if err != nil {
		return nil, err
	}
	binding, recorded, err := resolveInitialOperatorBinding(ctx, cfg, store, h.Execution, backend, operator)
	if err != nil || !recorded {
		return nil, environmentCreateBootstrapBlocker("existing product PKI requires the original registered operator")
	}
	client, err := newEnvironmentCreatePKIClient(origin, transport, cfg.Environment, binding.UserID)
	if err != nil {
		return nil, err
	}
	if _, err := client.Authenticate(ctx, operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]); err != nil {
		return nil, err
	}
	client.Execution = h.Execution
	var rootDoc, intermediateDoc map[string]any
	base := filepath.Join("deployment/create", h.Execution.Journal.PlanSHA256, "bootstrap/service")
	if createOpenBaoACKCompletionRead(store, filepath.Join(base, "root-active.json"), &rootDoc) != nil || createOpenBaoACKCompletionRead(store, filepath.Join(base, "intermediate-active.json"), &intermediateDoc) != nil {
		return nil, environmentCreateBootstrapBlocker("original completed Service lineage is unavailable")
	}
	root, err := environmentCreateRegisteredIssuerFromMap(rootDoc, "")
	if err != nil {
		return nil, err
	}
	ica, err := environmentCreateRegisteredIssuerFromMap(intermediateDoc, root.RootFingerprint)
	if err != nil {
		return nil, err
	}
	runtime := environmentCreateBootstrapRuntime{Execution: h.Execution, ExecutionSHA256: h.Execution.Journal.PlanSHA256, Backend: backend, PKIClient: client, APIOrigin: origin, APIClient: transport}
	runtime.OpenBao.RootTokenPath = filepath.Join(store.Root, "openbao/root-token")
	plan, err := prepareEnvironmentCreateProductIdentityLifecycle(ctx, cfg, h.OriginalPlan.Bootstrap, runtime, environmentCreateServiceCeremony{Root: root, Intermediate: ica})
	if err != nil {
		return nil, err
	}
	plan.SourceOwners = map[string]string{}
	for id, uid := range h.Bindings.Owners {
		plan.SourceOwners[id] = uid
	}
	plan.source, plan.runtime, plan.operator = h.Execution.Journal, runtime, operator
	plan.SignerPolicies, err = readProductSignerPolicies(ctx, cfg, runtime)
	if err != nil {
		return nil, err
	}
	controllerPolicies, err := readProductRolePolicies(ctx, cfg, runtime, "pki-controller-"+cfg.Environment, "pki-controller")
	if err != nil || !slices.Equal(controllerPolicies, environmentCreateControllerPolicies(runtime)) {
		return nil, environmentCreateBootstrapBlocker("original controller role differs from its recorded cumulative native policies")
	}
	if plan.RequiresSuccessor {
		plan.RootCustodySHA256, err = qualifyProductRootCustody(ctx, store, plan.Bootstrap.Image, plan.Root, runtime.PKIClient.OperatorID)
		if err != nil {
			return nil, err
		}
	}
	for _, product := range plan.Outputs {
		for _, id := range []string{product.ID, "PersistentVolumeClaim/" + cfg.Values["CLOUD_STACK_NAME"] + "-video-cloud/" + product.Claim} {
			actual, readErr := backend.Read(ctx, id)
			if readErr != nil || actual != nil || plan.SourceOwners[id] != "" {
				return nil, environmentCreateBootstrapBlocker("new product producer cannot adopt or replace an existing identity owner: " + id)
			}
		}
	}
	return &plan, nil
}

func createNormalTailProductClaimDefinitions(cfg deploymentConfig, l *createNormalTailProductLifecycle) ([]deploymentplan.Definition, error) {
	if l == nil || l.Bootstrap.Environment != cfg.Environment || l.Bootstrap.Stack != cfg.Values["CLOUD_STACK_NAME"] || !environmentPreflightDigest.MatchString(productIdentityScope(l)) {
		return nil, environmentCreateBootstrapBlocker("product allocation catalog differs from its qualified source")
	}
	cfg.Values = appendMap(cfg.Values, map[string]string{"LKE_STORAGE_CLASS": l.StorageClass})
	claims := []deploymentplan.Definition{environmentCreateServiceClaimDefinition(cfg, l.Bootstrap.Stack+"-video-cloud", "pki-product-bootstrap-"+productIdentityScope(l)[:12])}
	for _, product := range l.Outputs {
		claims = append(claims, environmentCreateServiceClaimDefinition(cfg, l.Bootstrap.Stack+"-video-cloud", product.Claim))
	}
	return claims, nil
}

func bindCreateNormalTailProductRelease(cfg deploymentConfig, l *createNormalTailProductLifecycle, release environmentRelease) error {
	if l == nil || release.Environment != cfg.Environment || release.Stack != cfg.Values["CLOUD_STACK_NAME"] || !immutableEnvironmentImage.MatchString(release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference) || !immutableEnvironmentImage.MatchString(release.Images["LKE_ACCOUNT_MANAGER_IMAGE"].Reference) {
		return environmentCreateBootstrapBlocker("product recipes require the same exact qualified current formal release")
	}
	l.Bootstrap.Image = release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference
	l.Bootstrap.AccountManagerImage = release.Images["LKE_ACCOUNT_MANAGER_IMAGE"].Reference
	l.Bootstrap.Images = map[string]string{}
	for role, image := range release.Images {
		l.Bootstrap.Images[role] = image.Reference
	}
	templates := map[string]string{}
	for name := range l.Bootstrap.Templates {
		raw, err := readBoundedEnvironmentFile(filepath.Join(cfg.Workspace, "repos/rtk_video_cloud", filepath.FromSlash(name)), false)
		if err != nil {
			return environmentCreateBootstrapBlocker("maintained product native recipe is unavailable")
		}
		templates[name] = environmentPreflightBytesSHA256(raw)
	}
	l.Bootstrap.Templates = templates
	return nil
}

func hydrateCreateNormalTailProducts(ctx context.Context, cfg deploymentConfig, h *createNormalTailHandoff, backend environmentKubernetesBackend, l *createNormalTailProductLifecycle) error {
	if l == nil {
		return nil
	}
	if h == nil || h.Execution == nil || l.ParentSHA256 != h.Execution.Journal.PlanSHA256 || l.OwnerSHA256 != deploymentplan.Digest(h.Execution.owner) || backend.Store != h.Execution.Store || h.Execution.Check(ctx) != nil {
		return environmentCreateBootstrapBlocker("saved product program differs from the original selected execution")
	}
	operator, err := h.Execution.Store.readOperator()
	if err != nil {
		return err
	}
	origin, transport, err := newEnvironmentCreateAccountClient(ctx, cfg, backend, h.Execution)
	if err != nil {
		return err
	}
	binding, recorded, err := resolveInitialOperatorBinding(ctx, cfg, h.Execution.Store, h.Execution, backend, operator)
	if err != nil || !recorded {
		return environmentCreateBootstrapBlocker("saved product program has no original registered operator")
	}
	client, err := newEnvironmentCreatePKIClient(origin, transport, cfg.Environment, binding.UserID)
	if err != nil {
		return err
	}
	if _, err = client.Authenticate(ctx, operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_EMAIL"], operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]); err != nil {
		return err
	}
	client.Execution = h.Execution
	client.operatorCredentials = operator
	root, err := client.ReadIssuer(ctx, l.Root.ID)
	if err != nil || root["status"] != "active" || root["certificate_pem"] != l.Root.Certificate || root["certificate_fingerprint_sha256"] != l.Root.RootFingerprint {
		return environmentCreateBootstrapBlocker("saved product program cannot replace its original active Service Root")
	}
	l.runtime = environmentCreateBootstrapRuntime{Execution: h.Execution, ExecutionSHA256: l.ParentSHA256, Backend: backend, PKIClient: client, APIOrigin: origin, APIClient: transport, OpenBao: environmentCreateOpenBaoBootstrap{RootTokenPath: filepath.Join(h.Execution.Store.Root, "openbao/root-token")}}
	l.operator = operator
	return h.Execution.Check(ctx)
}

// Qualification only reads the actual registered policy. A recorded old file
// cannot grant new subjects, and a missing product Secret cannot create a Root.
func prepareEnvironmentCreateProductIdentityLifecycle(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony) (environmentCreateProductIdentityLifecycle, error) {
	var plan environmentCreateProductIdentityLifecycle
	if runtime.Execution == nil || runtime.PKIClient == nil || runtime.PKIClient.Execution != runtime.Execution {
		return plan, environmentCreateBootstrapBlocker("product identity lifecycle requires the original fenced execution and authenticated existing PKI operator")
	}
	if err := runtime.Execution.Check(ctx); err != nil {
		return plan, err
	}
	outputs, err := environmentCreateProductIdentityIntent(cfg)
	if err != nil || len(outputs) == 0 {
		return plan, err
	}
	root, err := runtime.PKIClient.ReadIssuer(ctx, service.Root.ID)
	if err != nil {
		return plan, err
	}
	ica, err := runtime.PKIClient.ReadIssuer(ctx, service.Intermediate.ID)
	if err != nil {
		return plan, err
	}
	plan.Root, err = environmentCreateRegisteredIssuerFromMap(root, "")
	if err != nil {
		return plan, err
	}
	plan.Intermediate, err = environmentCreateRegisteredIssuerFromMap(ica, plan.Root.RootFingerprint)
	if err != nil || plan.Root.Environment != cfg.Environment || plan.Root.Domain != "service" || plan.Root.Kind != "root" || plan.Root.Status != "active" || plan.Root.RootFingerprint != service.Root.RootFingerprint || plan.Root.Certificate != service.Root.Certificate || plan.Intermediate.Domain != "service" || plan.Intermediate.Kind != "intermediate" || plan.Intermediate.Status != "active" || plan.Intermediate.ID != service.Intermediate.ID || deploymentplan.String(ica["parent_issuer_id"]) != plan.Root.ID {
		return plan, environmentCreateBootstrapBlocker("product enrollment must preserve the actual original active Service Root and registered ICA lineage")
	}
	policy := findEnvironmentCreateDomainPolicy(p, "service")
	policy.ServiceClientIDs = nil
	policy.ServerDNSNames = nil
	for _, field := range []struct {
		name   string
		target *[]string
	}{{"service_client_ids", &policy.ServiceClientIDs}, {"server_dns_names", &policy.ServerDNSNames}} {
		seen := map[string]bool{}
		for _, item := range certIssuerObjectList(ica[field.name]) {
			value, ok := item.(string)
			if !ok || value == "" || strings.TrimSpace(value) != value || seen[value] {
				return plan, environmentCreateBootstrapBlocker("registered Service ICA has an invalid exact issuance policy")
			}
			seen[value] = true
			*field.target = append(*field.target, value)
		}
		if len(*field.target) == 0 {
			return plan, environmentCreateBootstrapBlocker("registered Service ICA issuance policy is unavailable")
		}
	}
	for _, output := range outputs {
		if !slices.Contains(policy.ServiceClientIDs, output.Subject) {
			policy.ServiceClientIDs = append(policy.ServiceClientIDs, output.Subject)
			plan.RequiresSuccessor = true
		}
	}
	if !slices.Contains(policy.ServiceClientIDs, "service:deployment-bootstrap") {
		policy.ServiceClientIDs = append(policy.ServiceClientIDs, "service:deployment-bootstrap")
		plan.RequiresSuccessor = true
	}
	slices.Sort(policy.ServiceClientIDs)
	slices.Sort(policy.ServerDNSNames)
	for i := range outputs {
		outputs[i].RootID, outputs[i].RootSHA256, outputs[i].PolicySHA256 = plan.Root.ID, plan.Root.RootFingerprint, deploymentplan.Digest(policy)
		if !plan.RequiresSuccessor {
			outputs[i].IssuerID = plan.Intermediate.ID
		}
	}
	plan.ParentSHA256, plan.SourceSHA256, plan.OwnerSHA256 = runtime.Execution.Journal.PlanSHA256, deploymentplan.Digest(runtime.Execution.Journal), deploymentplan.Digest(runtime.Execution.owner)
	plan.Policy, plan.Outputs = policy, outputs
	plan.Bootstrap = p
	plan.StorageClass = firstNonEmpty(cfg.Values["LKE_STORAGE_CLASS"], "linode-block-storage-retain")
	return plan, runtime.Execution.Check(ctx)
}
