package main

import (
	"context"
	"errors"
	"path/filepath"
	"sort"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type environmentServerIdentityPolicy struct{ State, Domain, Subject, Root, Names string }

// Reuse the shipped read-only identity inspector. It checks the original key
// pair, pinned chain, exact server DNS/purpose and current validity without
// exporting a private key or creating identity/lock files. Registry admission,
// CRLs, client identities and candidate startup remain separate qualifications.
func (b environmentKubernetesBackend) CheckManagedServerIdentities(ctx context.Context, plan *deploymentplan.Plan) error {
	if plan == nil || plan.Environment != b.Store.Environment || plan.Stack != "video-cloud-"+b.Store.Environment || plan.Operation != "upgrade" {
		return serverIdentityBlocker("", "original managed server state requires a complete selected upgrade plan; fresh bootstrap must qualify its own original session")
	}
	if err := b.CheckStorageBindings(ctx, plan); err != nil {
		return err
	}
	objects, err := b.Inventory(ctx, plan.Stack)
	if err != nil {
		return serverIdentityBlocker("", "selected original workload inventory is unavailable")
	}
	if err := plan.CheckWorkloadInventory(objects); err != nil {
		return err
	}
	live := map[string]deploymentplan.Object{}
	for _, object := range objects {
		id := deploymentplan.ID(object)
		if live[id] != nil {
			return serverIdentityBlocker("", "selected workload inventory contains duplicate resource identities")
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
			policies, err := environmentServerIdentityPolicies(container)
			if err != nil {
				return serverIdentityBlocker(resource.ID, "target managed host policy is unresolved, incomplete or ambiguous")
			}
			if len(policies) == 0 {
				continue
			}
			current := live[resource.ID]
			if resource.Action == "create" || current == nil || resource.UID == "" {
				return serverIdentityBlocker(resource.ID, "original server identity owner is absent; bootstrap or candidate original-state qualification is required")
			}
			if _, err := resource.Body(current); err != nil {
				return serverIdentityBlocker(resource.ID, "original workload ownership or configuration drifted after planning")
			}
			for _, policy := range policies {
				if !environmentServerStateOnPVC(resource.Desired, container, policy.State) {
					return serverIdentityBlocker(resource.ID, "managed server identity state is not backed by an explicit durable claim")
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
					return serverIdentityBlocker(resource.ID, "original owned Pod cannot inspect its server state; qualify the repaired candidate before proceeding")
				}
				if err := b.checkEnvironmentAccess(ctx, environmentAccessRequirement{Resource: "pods", Namespace: namespace, Name: name, Verb: "get"}, ""); err != nil {
					return err
				}
				if err := b.checkEnvironmentAccess(ctx, environmentAccessRequirement{Resource: "pods", Namespace: namespace, Name: name, Verb: "create"}, "exec"); err != nil {
					return err
				}
				for _, policy := range policies {
					if !environmentServerPodStateClaim(plan, resource.Desired, container, object, policy.State) {
						return serverIdentityBlocker(resource.ID, "owned Pod does not mount the original planned state claim")
					}
					raw, err := b.command(ctx, nil, "exec", "--namespace", namespace, name, "--container", deploymentplan.String(container["name"]), "--", "/app/serviceidentity-bootstrap", "inspect-server", policy.State, policy.Domain, policy.Subject, policy.Root, policy.Names)
					var state struct {
						Subject     string `json:"subject"`
						Fingerprint string `json:"fingerprint"`
						PublicKey   string `json:"public_key_sha256"`
						Root        string `json:"root_sha256"`
						State       string `json:"state_sha256"`
						Pending     *bool  `json:"pending"`
					}
					if err != nil || decodeEnvironmentJSON(raw, &state) != nil || state.Subject != policy.Subject || state.Root != policy.Root || !deploymentImageDigestPattern.MatchString("sha256:"+state.Fingerprint) || !deploymentImageDigestPattern.MatchString("sha256:"+state.PublicKey) || !deploymentImageDigestPattern.MatchString("sha256:"+state.State) || state.Pending == nil || *state.Pending {
						return serverIdentityBlocker(resource.ID, "original server identity inspection failed or reports incomplete/currently pending state")
					}
					checked++
				}
				fresh, err := b.Read(ctx, deploymentplan.ID(object))
				if err != nil || fresh == nil || deploymentplan.Map(fresh["metadata"])["uid"] != uid || deploymentplan.Map(fresh["metadata"])["deletionTimestamp"] != nil || !deploymentplan.Matches(deploymentplan.Projection(object), fresh) {
					return serverIdentityBlocker(resource.ID, "owned Pod changed during identity inspection")
				}
				found++
			}
			expected := float64(1)
			if value, exists := deploymentplan.Map(current["spec"])["replicas"]; exists {
				var ok bool
				expected, ok = value.(float64)
				if !ok {
					return serverIdentityBlocker(resource.ID, "original replica count is invalid")
				}
			}
			if current["kind"] == "DaemonSet" {
				var ok bool
				expected, ok = deploymentplan.Map(current["status"])["desiredNumberScheduled"].(float64)
				if !ok {
					return serverIdentityBlocker(resource.ID, "original daemon placement count is unavailable")
				}
			}
			if expected <= 0 || expected > 10000 || expected != float64(int(expected)) || found != int(expected) {
				return serverIdentityBlocker(resource.ID, "original owned Pod count does not cover every server identity replica")
			}
			fresh, err := b.Read(ctx, resource.ID)
			if err != nil {
				return serverIdentityBlocker(resource.ID, "original controller cannot be rechecked after inspection")
			}
			if _, err := resource.Body(fresh); err != nil {
				return serverIdentityBlocker(resource.ID, "original controller changed during identity inspection")
			}
		}
	}
	if checked == 0 {
		return serverIdentityBlocker("", "no complete managed server identity policy was inspected")
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
		policy := environmentServerIdentityPolicy{State: deploymentplan.String(setting["value"]), Domain: domain, Subject: get("_HOST_NAME"), Root: get("_HOST_ROOT_SHA256"), Names: get("_HOST_DNS_NAMES")}
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
