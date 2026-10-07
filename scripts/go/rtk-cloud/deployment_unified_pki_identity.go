package main

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentServerIdentityPolicy struct{ State, Domain, Subject, Root, Names, Database string }

// Reuse the shipped read-only identity inspector. It checks the original key
// pair, pinned chain, exact server DNS/purpose and current validity without
// exporting a private key or creating identity/lock files. Registry admission,
// CRLs, client identities and candidate startup remain separate qualifications.
func (b environmentKubernetesBackend) CheckManagedServerIdentities(ctx context.Context, plan *deploymentplan.Plan) error {
	return b.checkManagedIdentityStates(ctx, plan, false, false, 0)
}
func (b environmentKubernetesBackend) CheckManagedClientIdentities(ctx context.Context, plan *deploymentplan.Plan) error {
	return b.checkManagedIdentityStates(ctx, plan, true, false, 0)
}

func (b environmentKubernetesBackend) CheckManagedServerRegistry(ctx context.Context, plan *deploymentplan.Plan, window int64) error {
	return b.checkManagedIdentityStates(ctx, plan, false, true, window)
}

func (b environmentKubernetesBackend) CheckManagedClientRegistry(ctx context.Context, plan *deploymentplan.Plan, window int64) error {
	return b.checkManagedIdentityStates(ctx, plan, true, true, window)
}

func (b environmentKubernetesBackend) checkManagedIdentityStates(ctx context.Context, plan *deploymentplan.Plan, clients, registry bool, window int64) error {
	if registry && (window <= 0 || window > int64((1<<63-1)/time.Second)) {
		return validityWindowBlocker()
	}
	block := serverIdentityBlocker
	if clients {
		block = clientIdentityBlocker
	}
	if registry {
		block = func(resource, cause string) error {
			id := "deployment.pki.server-registry"
			if clients {
				id = "deployment.pki.client-registry"
			}
			return &deploymentplan.Blocker{ID: id, Resource: resource, Cause: cause, Next: "verify the selected environment's original registry connection and read permissions, recorded issuance receipt and admitted issuer policy; use a qualified image with the shipped read-only registry inspector on the original owned state, then repeat complete preflight; never enroll a replacement identity or fall back to basic inspection"}
		}
	}

	if plan == nil || plan.Environment != b.Store.Environment || plan.Stack != "video-cloud-"+b.Store.Environment || plan.Operation != "upgrade" {
		return block("", "original managed server state requires a complete selected upgrade plan; fresh bootstrap must qualify its own original session")
	}
	if err := b.CheckStorageBindings(ctx, plan); err != nil {
		return err
	}
	objects, err := b.Inventory(ctx, plan.Stack)
	if err != nil {
		return block("", "selected original workload inventory is unavailable")
	}
	if err := plan.CheckWorkloadInventory(objects); err != nil {
		return err
	}
	live := map[string]deploymentplan.Object{}
	for _, object := range objects {
		id := deploymentplan.ID(object)
		if live[id] != nil {
			return block("", "selected workload inventory contains duplicate resource identities")
		}
		live[id] = object
	}
	checked := 0
	for _, resource := range plan.Resources {
		if resource.Action == "retire" || !keySet("Deployment", "StatefulSet", "DaemonSet")[deploymentplan.String(resource.Desired["kind"])] {
			continue
		}
		pod := environmentPKIPod(resource.Desired)
		for _, value := range certIssuerObjectList(pod["containers"]) {
			container := deploymentplan.Map(value)
			resolved, recheckSettings, err := b.resolveIdentitySettings(ctx, plan, resource.Desired, container)
			if err != nil {
				var detail *deploymentplan.Blocker
				if errors.As(err, &detail) {
					return detail
				}
				return block(resource.ID, "public identity setting references are unavailable, changed or lack a planned original owner")
			}
			policies, err := environmentServerIdentityPolicies(resolved)
			if clients {
				policies, err = environmentClientIdentityPolicies(resource.Desired, resolved)
			}
			if err != nil {
				return block(resource.ID, "target managed host policy is unresolved, incomplete or ambiguous")
			}
			if len(policies) == 0 {
				continue
			}
			current := live[resource.ID]
			if resource.Action == "create" || current == nil || resource.UID == "" {
				return block(resource.ID, "original server identity owner is absent; bootstrap or candidate original-state qualification is required")
			}
			if _, err := resource.Body(current); err != nil {
				return block(resource.ID, "original workload ownership or configuration drifted after planning")
			}
			for _, policy := range policies {
				if !environmentServerStateOnPVC(resource.Desired, container, policy.State) {
					return block(resource.ID, "managed server identity state is not backed by an explicit durable claim")
				}
			}
			found := 0
			for _, object := range objects {
				meta := deploymentplan.Map(object["metadata"])
				if object["kind"] != "Pod" || meta["deletionTimestamp"] != nil {
					continue
				}
				owner, err := environmentPodController(object, live)
				if err != nil || owner != resource.ID {
					continue
				}
				name, namespace, uid := deploymentplan.String(meta["name"]), deploymentplan.String(meta["namespace"]), deploymentplan.String(meta["uid"])
				if name == "" || uid == "" || deploymentplan.Map(object["status"])["phase"] != "Running" {
					return block(resource.ID, "original owned Pod cannot inspect its server state; qualify the repaired candidate before proceeding")
				}
				if err := b.checkEnvironmentAccess(ctx, environmentAccessRequirement{Resource: "pods", Namespace: namespace, Name: name, Verb: "get"}, ""); err != nil {
					return err
				}
				if err := b.checkEnvironmentAccess(ctx, environmentAccessRequirement{Resource: "pods", Namespace: namespace, Name: name, Verb: "create"}, "exec"); err != nil {
					return err
				}
				for _, policy := range policies {
					if !environmentServerPodStateClaim(plan, resource.Desired, container, object, policy.State) {
						return block(resource.ID, "owned Pod does not mount the original planned state claim")
					}
					args := []string{"exec", "--namespace", namespace, name, "--container", deploymentplan.String(container["name"]), "--", "/app/serviceidentity-bootstrap"}
					if registry && clients {
						args = append(args, "inspect-registry", plan.Environment, policy.State, policy.Subject, policy.Root)
					} else if registry {
						args = append(args, "inspect-server-registry", plan.Environment, policy.State, policy.Domain, policy.Subject, policy.Root, policy.Names)
					} else if clients {
						args = append(args, "inspect", policy.State, policy.Subject, policy.Root)
					} else {
						args = append(args, "inspect-server", policy.State, policy.Domain, policy.Subject, policy.Root, policy.Names)
					}
					if registry {
						args = append(args, strconv.FormatInt(window, 10), policy.Database)
					}
					raw, err := b.command(ctx, nil, args...)
					if err != nil || validateEnvironmentIdentitySummary(raw, policy, registry, window) != nil {
						return block(resource.ID, "original server identity inspection failed or reports incomplete/currently pending state")
					}
					checked++
				}
				if err := recheckSettings(ctx); err != nil {
					return err
				}
				fresh, err := b.Read(ctx, deploymentplan.ID(object))
				if err != nil || fresh == nil || deploymentplan.Map(fresh["metadata"])["uid"] != uid || deploymentplan.Map(fresh["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(deploymentplan.Projection(object), fresh) {
					return block(resource.ID, "owned Pod changed during identity inspection")
				}
				found++
			}
			expected := float64(1)
			if value, exists := deploymentplan.Map(current["spec"])["replicas"]; exists {
				var ok bool
				expected, ok = value.(float64)
				if !ok {
					return block(resource.ID, "original replica count is invalid")
				}
			}
			if current["kind"] == "DaemonSet" {
				var ok bool
				expected, ok = deploymentplan.Map(current["status"])["desiredNumberScheduled"].(float64)
				if !ok {
					return block(resource.ID, "original daemon placement count is unavailable")
				}
			}
			if expected <= 0 || expected > 10000 || expected != float64(int(expected)) || found != int(expected) {
				return block(resource.ID, "original owned Pod count does not cover every server identity replica")
			}
			fresh, err := b.Read(ctx, resource.ID)
			if err != nil {
				return block(resource.ID, "original controller cannot be rechecked after inspection")
			}
			if _, err := resource.Body(fresh); err != nil {
				return block(resource.ID, "original controller changed during identity inspection")
			}
		}
	}
	if checked == 0 {
		return block("", "no complete managed server identity policy was inspected")
	}
	return nil
}

func serverIdentityBlocker(resource, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.pki.server-identity", Resource: resource, Cause: cause, Next: "restore explicit maintained host state/name/DNS/root bindings and original durable claims; reconcile pending lifecycle requests or inspect the original state using a qualified candidate; verify the shipped read-only inspector and original Pod/controller ownership, then repeat complete preflight; never initialize replacement state to pass"}
}

func environmentServerIdentityPolicies(container deploymentplan.Object) ([]environmentServerIdentityPolicy, error) {
	settings, err := environmentPKINamedFields(container["env"])
	if err != nil {
		return nil, err
	}
	var policies []environmentServerIdentityPolicy
	for name, setting := range settings {
		if !strings.HasSuffix(name, "_HOST_IDENTITY_STATE") {
			continue
		}
		prefix := strings.TrimSuffix(name, "_HOST_IDENTITY_STATE")
		domain := map[string]string{"CERT_ISSUER": "service", "PKI": "service", "EMQX_PKI": "mqtt", "OPENBAO_PKI": "openbao_tls"}[prefix]
		get := func(suffix string) string {
			item := settings[prefix+suffix]
			if item["valueFrom"] != nil {
				return ""
			}
			return deploymentplan.String(item["value"])
		}
		policy := environmentServerIdentityPolicy{State: deploymentplan.String(setting["value"]), Domain: domain, Subject: get("_HOST_NAME"), Root: get("_HOST_ROOT_SHA256"), Names: get("_HOST_DNS_NAMES"), Database: map[string]string{"CERT_ISSUER": "CERT_ISSUER_DB_DSN", "PKI": "PKI_DATABASE_URL", "EMQX_PKI": "PKI_DATABASE_URL", "OPENBAO_PKI": "PKI_DATABASE_URL"}[prefix]}
		if setting["valueFrom"] != nil || domain == "" || !filepath.IsAbs(policy.State) || filepath.Clean(policy.State) != policy.State || policy.Subject == "" || !deploymentImageDigestPattern.MatchString("sha256:"+policy.Root) || policy.Names == "" {
			return nil, errors.New("explicit canonical host policy required")
		}
		policies = append(policies, policy)
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].State < policies[j].State })
	return policies, nil
}

func environmentServerStateOnPVC(object, container deploymentplan.Object, path string) bool {
	return environmentServerIdentityClaim(object, container, path) != ""
}

func environmentServerIdentityClaim(object, container deploymentplan.Object, path string) string {
	volumes, err := environmentPKIVolumeSources(object)
	if err != nil {
		return ""
	}
	best := ""
	var source deploymentplan.Object
	for _, value := range certIssuerObjectList(container["volumeMounts"]) {
		mount := deploymentplan.Map(value)
		root := deploymentplan.String(mount["mountPath"])
		if root == "" || !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return ""
		}
		if (path == root || strings.HasPrefix(path, strings.TrimRight(root, "/")+"/")) && len(root) > len(best) {
			best, source = root, volumes[deploymentplan.String(mount["name"])]
		}
	}
	if best == "" {
		return ""
	}
	if name := deploymentplan.String(source["persistentVolumeClaimTemplate"]); name != "" {
		return "template:" + name
	}
	return deploymentplan.String(deploymentplan.Map(source["persistentVolumeClaim"])["claimName"])
}

func environmentServerPodStateClaim(plan *deploymentplan.Plan, target, container, pod deploymentplan.Object, path string) bool {
	claim := environmentServerIdentityClaim(target, container, path)
	if strings.HasPrefix(claim, "template:") {
		claim = strings.TrimPrefix(claim, "template:") + "-" + deploymentplan.String(deploymentplan.Map(pod["metadata"])["name"])
	}
	actualPod := deploymentplan.Map(pod["spec"])
	containers, err := environmentPKINamedFields(actualPod["containers"])
	if err != nil || containers[deploymentplan.String(container["name"])] == nil {
		return false
	}
	actual := deploymentplan.Object{"kind": "Deployment", "spec": deploymentplan.Object{"template": deploymentplan.Object{"spec": actualPod}}}
	if claim == "" || environmentServerIdentityClaim(actual, containers[deploymentplan.String(container["name"])], path) != claim {
		return false
	}
	id := "PersistentVolumeClaim/" + deploymentplan.String(deploymentplan.Map(pod["metadata"])["namespace"]) + "/" + claim
	for _, resource := range plan.Resources {
		if resource.ID == id && resource.UID != "" && resource.Action != "create" && resource.Action != "retire" {
			return true
		}
	}
	return false
}

func clientIdentityBlocker(resource, cause string) error {
	return &deploymentplan.Blocker{ID: "deployment.pki.client-identity", Resource: resource, Cause: strings.ReplaceAll(cause, "server", "client"), Next: "restore the exact maintained Service client state/subject/root bindings and original durable claims; reconcile pending lifecycle requests or qualify the original state using a candidate; repeat full preflight without replacing a private key or bootstrap session"}
}

// Subject mappings are the maintained consumer contracts, not a subject supplied
// by the inspection response or inferred from an arbitrary workload name.
func environmentClientIdentityPolicies(object, container deploymentplan.Object) ([]environmentServerIdentityPolicy, error) {
	settings, err := environmentPKINamedFields(container["env"])
	if err != nil {
		return nil, err
	}
	name := deploymentplan.String(deploymentplan.Map(object["metadata"])["name"])
	var policies []environmentServerIdentityPolicy
	for settingName, setting := range settings {
		if !strings.HasSuffix(settingName, "_IDENTITY_STATE") || strings.HasSuffix(settingName, "_HOST_IDENTITY_STATE") {
			continue
		}
		prefix := strings.TrimSuffix(settingName, "_IDENTITY_STATE")
		subject := ""
		switch prefix {
		case "CERT_ISSUER_SERVICE_CLIENT":
			if name == "certissuer" || name == "certissuer-service-pki" {
				subject = "service:certissuer"
			}
		case "PKI_SERVICE_CLIENT":
			if name == "pki-controller" {
				subject = "service:pki-controller"
			}
		case "EMQX_PKI_SERVICE_CLIENT":
			if name == "mqtt-pki" {
				subject = "service:emqx-pki"
			}
		case "OPENBAO_PKI_SERVICE_CLIENT":
			if name == "openbao" {
				subject = "service:openbao"
			}
		case "PKI_BROKER":
			if name == "mqtt-pki" {
				subject = "service:pkibroker"
			}
		case "VIDEO_CLOUD_CONTROLLER", "VIDEO_CLOUD_ACCOUNT_MANAGER":
			if name == "video-cloud-api-pki" {
				subject = "service:video-cloud-api"
			}
		case "VIDEO_CLOUD_LOG_INGESTER_MQTT":
			if name == "video-cloud-logingester" {
				subject = "service:video-cloud-logingester"
			}
		}
		rootSetting := settings[prefix+"_ROOT_SHA256"]
		if prefix == "VIDEO_CLOUD_CONTROLLER" || prefix == "VIDEO_CLOUD_ACCOUNT_MANAGER" || prefix == "VIDEO_CLOUD_LOG_INGESTER_MQTT" {
			rootSetting = settings[prefix+"_IDENTITY_ROOT_SHA256"]
		}
		policy := environmentServerIdentityPolicy{State: deploymentplan.String(setting["value"]), Subject: subject, Root: deploymentplan.String(rootSetting["value"]), Database: "PKI_DATABASE_URL"}
		if prefix == "CERT_ISSUER_SERVICE_CLIENT" {
			policy.Database = "CERT_ISSUER_DB_DSN"
		}
		if strings.HasPrefix(prefix, "VIDEO_CLOUD_") {
			policy.Database = "VIDEO_CLOUD_DB_DSN"
		}
		if setting["valueFrom"] != nil || rootSetting["valueFrom"] != nil || subject == "" || !filepath.IsAbs(policy.State) || filepath.Clean(policy.State) != policy.State || !deploymentImageDigestPattern.MatchString("sha256:"+policy.Root) {
			return nil, errors.New("explicit maintained Service client policy required")
		}
		policies = append(policies, policy)
	}
	sort.Slice(policies, func(i, j int) bool { return policies[i].State < policies[j].State })
	return policies, nil
}

func validityWindowBlocker() error {
	return &deploymentplan.Blocker{ID: "deployment.pki.validity-window", Cause: "an explicit positive bounded upgrade validity window is required", Next: "maintain DEPLOYMENT_UPGRADE_WINDOW_SECONDS in the shared architecture or environment architecture override; select a window that covers the complete update and acceptance, then repeat complete preflight"}
}

func environmentPKIValidityWindow(values map[string]string) (int64, error) {
	raw := values["DEPLOYMENT_UPGRADE_WINDOW_SECONDS"]
	window, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || raw != strconv.FormatInt(window, 10) || window <= 0 || window > int64((1<<63-1)/time.Second) {
		return 0, validityWindowBlocker()
	}
	return window, nil
}

func validateEnvironmentIdentitySummary(raw []byte, policy environmentServerIdentityPolicy, registry bool, window int64) error {
	var state struct {
		Subject     string `json:"subject"`
		Fingerprint string `json:"fingerprint"`
		PublicKey   string `json:"public_key_sha256"`
		Root        string `json:"root_sha256"`
		State       string `json:"state_sha256"`
		Pending     *bool  `json:"pending"`
		Registry    *bool  `json:"registry_verified,omitempty"`
		Window      *int64 `json:"minimum_validity_seconds,omitempty"`
	}
	if decodeEnvironmentJSON(raw, &state) != nil || state.Subject != policy.Subject || state.Root != policy.Root || !deploymentImageDigestPattern.MatchString("sha256:"+state.Fingerprint) || !deploymentImageDigestPattern.MatchString("sha256:"+state.PublicKey) || !deploymentImageDigestPattern.MatchString("sha256:"+state.State) || state.Pending == nil || *state.Pending || (registry && (window <= 0 || state.Registry == nil || !*state.Registry || state.Window == nil || *state.Window != window)) {
		return errors.New("original managed identity summary is incomplete or differs from reviewed policy")
	}
	return nil
}
