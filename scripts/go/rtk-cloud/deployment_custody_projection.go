package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// IDs bind workload custody; aliases name maintained file-storage purposes that
// become temporary projections. Neither field is a Pod UID or mutable KV version.
type environmentCustodyOwnerBinding struct {
	ID             string   `json:"id"`
	Namespace      string   `json:"namespace"`
	ServiceAccount string   `json:"service_account"`
	StorageAliases []string `json:"storage_aliases,omitempty"`
}

type environmentCustodyBinding struct {
	ServiceRootSHA256 string                           `json:"service_root_sha256,omitempty"`
	Generation        string                           `json:"generation"`
	OpenBaoRootSHA256 string                           `json:"openbao_root_sha256"`
	Owners            []environmentCustodyOwnerBinding `json:"owners"`
}

func environmentCustodyLeaseName(prefix, path string) string {
	key := environmentCustodyRecordKey(prefix, path)
	leaseHash := sha256.Sum256([]byte(key))
	return "pki-state-" + hex.EncodeToString(leaseHash[:16])
}

func environmentCustodyRoleName(prefix string) string {
	sum := sha256.Sum256([]byte(prefix))
	return "pki-state-" + hex.EncodeToString(sum[:16])
}

// The same projection is used by bootstrap recipes and the steady renderer.
// It changes only explicitly bound workload storage, never business data or
// OpenBao's independent cold-start identity. It does not create KV records.
func projectEnvironmentCustodyResources(values map[string]string, environment string, binding *environmentCustodyBinding, bootstrapRoot string, definitions []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	if err := validateEnvironmentCustodySettings(values); err != nil {
		return nil, err
	}
	if values["PKI_STATE_BACKEND"] != "openbao" {
		return definitions, nil
	}
	stack := values["CLOUD_STACK_NAME"]
	if stack != "video-cloud-"+environment || binding == nil || len(binding.Owners) == 0 || !environmentPreflightDigest.MatchString(binding.Generation) || (binding.OpenBaoRootSHA256 != "" && !environmentPreflightDigest.MatchString(binding.OpenBaoRootSHA256)) || (bootstrapRoot == "" && !environmentPreflightDigest.MatchString(binding.OpenBaoRootSHA256)) || (bootstrapRoot != "" && !environmentPreflightDigest.MatchString(bootstrapRoot)) {
		return nil, environmentCreateBootstrapBlocker("custody projection requires the original generation and independently recorded OpenBao transport authority")
	}
	byClaim, byPrefix := map[string]environmentCustodyOwnerBinding{}, map[string]environmentCustodyOwnerBinding{}
	namespaces := map[string]bool{}
	for _, namespace := range lkeNamespaces(values) {
		namespaces[namespace.Name] = true
	}
	for _, owner := range binding.Owners {
		if !secretEnvironmentPattern.MatchString(owner.ID) || !namespaces[owner.Namespace] || !secretEnvironmentPattern.MatchString(owner.ServiceAccount) || len(owner.ServiceAccount) > 63 || owner.ID == "openbao-pki-identity" || owner.ServiceAccount == "default" {
			return nil, environmentCreateBootstrapBlocker("custody owner must bind its exact workload namespace and dedicated service account")
		}
		prefix := environment + "/" + binding.Generation + "/" + owner.ID
		if _, exists := byPrefix[prefix]; exists {
			return nil, environmentCreateBootstrapBlocker("duplicate durable custody owner is ambiguous")
		}
		byPrefix[prefix] = owner
		aliases := owner.StorageAliases
		if len(aliases) == 0 {
			aliases = []string{owner.ID}
		}
		for _, alias := range aliases {
			if !secretEnvironmentPattern.MatchString(alias) || alias == "openbao-pki-identity" {
				return nil, environmentCreateBootstrapBlocker("custody storage alias must name its original workload state")
			}
			key := owner.Namespace + "/" + alias
			if previous, exists := byClaim[key]; exists && previous.ID != owner.ID {
				return nil, environmentCreateBootstrapBlocker("custody storage alias selects multiple workload owners")
			}
			byClaim[key] = owner
		}
	}
	active := false
	for _, d := range definitions {
		active = active || !d.Retire
	}
	if !active {
		return definitions, nil
	}
	definitions, err := projectEnvironmentCustodyRegistrations(values, environment, binding, definitions)
	if err != nil {
		return nil, err
	}
	var result []deploymentplan.Definition
	dependencies := map[string]deploymentplan.Object{}
	add := func(object deploymentplan.Object) {
		dependencies[deploymentplan.ID(object)] = object
	}
	// The maintained OpenBao policy already admits video-cloud clients. Add
	// only selected custody consumers in the other owned namespaces. Preserve
	// existing egress: a new Egress policy would accidentally isolate DB/DNS/API.
	additionalNamespaces := map[string]bool{}
	for _, owner := range binding.Owners {
		if owner.Namespace != stack+"-video-cloud" {
			additionalNamespaces[owner.Namespace] = true
		}
	}
	if len(additionalNamespaces) != 0 {
		var ordered []string
		for namespace := range additionalNamespaces {
			ordered = append(ordered, namespace)
		}
		sort.Strings(ordered)
		var peers []any
		for _, namespace := range ordered {
			peers = append(peers, deploymentplan.Object{"namespaceSelector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"kubernetes.io/metadata.name": namespace}}, "podSelector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"rtk.realtek.com/pki-state-backend": "openbao"}}})
		}
		add(deploymentplan.Object{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": deploymentplan.Object{"name": "allow-openbao-workload-custody", "namespace": stack + "-secrets"}, "spec": deploymentplan.Object{"podSelector": deploymentplan.Object{"matchLabels": deploymentplan.Object{"app.kubernetes.io/name": "openbao"}}, "policyTypes": []any{"Ingress"}, "ingress": []any{deploymentplan.Object{"from": peers, "ports": []any{deploymentplan.Object{"protocol": "TCP", "port": float64(8200)}}}}}})
	}
	for _, definition := range definitions {
		d := definition
		d.Object = deploymentplan.Clone(d.Object)
		if d.Retire {
			result = append(result, d)
			continue
		}
		metadata := deploymentplan.Map(d.Object["metadata"])
		namespace := deploymentplan.String(metadata["namespace"])
		if d.Object["kind"] == "PersistentVolumeClaim" {
			if _, owned := byClaim[namespace+"/"+deploymentplan.String(metadata["name"])]; owned {
				continue
			}
		}
		if d.Object["kind"] == "StatefulSet" && metadata["name"] == "openbao" && namespace == stack+"-secrets" {
			result = append(result, d)
			continue
		}
		pod := environmentPKIPod(d.Object)
		if pod == nil {
			result = append(result, d)
			continue
		}
		volumes := certIssuerObjectList(pod["volumes"])
		volumeOwners := map[string]environmentCustodyOwnerBinding{}
		memoryVolumes := map[string]bool{}
		for _, raw := range volumes {
			volume := deploymentplan.Map(raw)
			claim := deploymentplan.String(deploymentplan.Map(volume["persistentVolumeClaim"])["claimName"])
			if owner, ok := byClaim[namespace+"/"+claim]; ok {
				volumeOwners[deploymentplan.String(volume["name"])] = owner
				delete(volume, "persistentVolumeClaim")
				volume["emptyDir"] = deploymentplan.Object{"medium": "Memory"}
			}
			memoryVolumes[deploymentplan.String(volume["name"])] = deploymentplan.Map(volume["emptyDir"])["medium"] == "Memory"
		}
		account := ""
		podDependencies := map[string]bool{}
		for _, collection := range []string{"containers", "initContainers"} {
			for _, raw := range certIssuerObjectList(pod[collection]) {
				container := deploymentplan.Map(raw)
				var owner *environmentCustodyOwnerBinding
				var paths []string
				existingPrefix := mqttFoundationEnv(container, "PKI_STATE_RECORD_PREFIX")
				for _, field := range certIssuerObjectList(container["env"]) {
					e := deploymentplan.Map(field)
					path := deploymentplan.String(e["value"])
					if !strings.HasSuffix(deploymentplan.String(e["name"]), "_IDENTITY_STATE") || path == "" {
						continue
					}
					var selected environmentCustodyOwnerBinding
					longest := 0
					for _, mountRaw := range certIssuerObjectList(container["volumeMounts"]) {
						mount := deploymentplan.Map(mountRaw)
						root := deploymentplan.String(mount["mountPath"])
						candidate, owned := volumeOwners[deploymentplan.String(mount["name"])]
						if !owned && existingPrefix != "" && memoryVolumes[deploymentplan.String(mount["name"])] {
							candidate, owned = byPrefix[existingPrefix]
						}
						if owned && root != "" && strings.HasPrefix(path, strings.TrimSuffix(root, "/")+"/") && len(root) > longest {
							selected, longest = candidate, len(root)
						}
					}
					if longest == 0 {
						continue
					}
					if !filepath.IsAbs(path) || filepath.Clean(path) != path {
						return nil, environmentCreateBootstrapBlocker("native custody identity path must remain its original absolute logical path")
					}
					if owner != nil && !sameEnvironmentCustodyOwner(*owner, selected) {
						return nil, environmentCreateBootstrapBlocker("one native identity configuration cannot select two durable workload owners")
					}
					copy := selected
					owner = &copy
					paths = append(paths, path)
				}
				if owner == nil {
					continue
				}
				if owner.Namespace != namespace || account != "" && account != owner.ServiceAccount {
					return nil, environmentCreateBootstrapBlocker("custody containers in one Pod must share its exact namespace and service account")
				}
				account = owner.ServiceAccount
				prefix := environment + "/" + binding.Generation + "/" + owner.ID
				if existingPrefix != "" && existingPrefix != prefix {
					return nil, environmentCreateBootstrapBlocker("native custody projection cannot select another original generation or owner")
				}
				settings := map[string]string{
					"PKI_STATE_BACKEND": "openbao", "PKI_STATE_KV_MOUNT": values["PKI_STATE_KV_MOUNT"], "PKI_STATE_RECORD_PREFIX": prefix, "PKI_ENVIRONMENT": environment,
					"OPENBAO_ADDR": "https://openbao." + stack + "-secrets.svc:8200", "PKI_STATE_OPENBAO_ROLE": environmentCustodyRoleName(prefix), "PKI_STATE_OPENBAO_JWT_FILE": "/run/pki-custody-auth/openbao.jwt",
					"PKI_STATE_KUBE_API": "https://kubernetes.default.svc:443", "PKI_STATE_KUBE_CA": "/run/pki-custody-auth/ca.crt", "PKI_STATE_KUBE_TOKEN_FILE": "/run/pki-custody-auth/kube.jwt", "PKI_STATE_LEASE_DURATION": values["PKI_STATE_LEASE_DURATION"],
					"PKI_STATE_OPENBAO_SERVER_PKI_ROOT_SHA256": binding.OpenBaoRootSHA256, "PKI_STATE_OPENBAO_SERVER_PKI_NAME": "openbao." + stack + "-secrets.svc", "PKI_STATE_OPENBAO_TLS_CA": environmentCustodyTrustPath + "/managed-root.pem",
					"PKI_STATE_BOOTSTRAP_ROOT_SHA256": bootstrapRoot, "PKI_STATE_TRANSPORT_TRUST": "",
				}
				if bootstrapRoot != "" {
					settings["PKI_STATE_TRANSPORT_TRUST"] = environmentCustodyTrustPath + "/transport.json"
				}
				fields := environmentCreateEnv(settings)
				for _, field := range []struct{ key, source string }{{"PKI_STATE_NAMESPACE", "metadata.namespace"}, {"PKI_STATE_POD_UID", "metadata.uid"}} {
					fields = append(fields, deploymentplan.Object{"name": field.key, "valueFrom": deploymentplan.Object{"fieldRef": deploymentplan.Object{"fieldPath": field.source}}})
				}
				container["env"] = mergeEnvironmentNamedFields(container["env"], fields)
				container["volumeMounts"] = mergeEnvironmentNamedFields(container["volumeMounts"], []any{environmentCreateServiceMount("pki-custody-auth", "/run/pki-custody-auth", true), environmentCreateServiceMount("pki-custody-trust", environmentCustodyTrustPath, true)})
				for _, path := range paths {
					name := environmentCustodyLeaseName(prefix, path)
					meta := func() deploymentplan.Object { return deploymentplan.Object{"name": name, "namespace": namespace} }
					// No spec fields: reapplying this declaration must never clear a live holder.
					add(deploymentplan.Object{"apiVersion": "coordination.k8s.io/v1", "kind": "Lease", "metadata": meta()})
					add(deploymentplan.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "Role", "metadata": meta(), "rules": []any{deploymentplan.Object{"apiGroups": []any{"coordination.k8s.io"}, "resources": []any{"leases"}, "resourceNames": []any{name}, "verbs": []any{"get", "update"}}}})
					add(deploymentplan.Object{"apiVersion": "rbac.authorization.k8s.io/v1", "kind": "RoleBinding", "metadata": meta(), "roleRef": deploymentplan.Object{"apiGroup": "rbac.authorization.k8s.io", "kind": "Role", "name": name}, "subjects": []any{deploymentplan.Object{"kind": "ServiceAccount", "name": account, "namespace": namespace}}})
					for _, kind := range []string{"Lease", "Role", "RoleBinding"} {
						podDependencies[kind+"/"+namespace+"/"+name] = true
					}
				}
			}
		}
		if account != "" {
			podDependencies["ServiceAccount/"+namespace+"/"+account] = true
			if additionalNamespaces[namespace] {
				podDependencies["NetworkPolicy/"+stack+"-secrets/allow-openbao-workload-custody"] = true
			}
			pod["automountServiceAccountToken"], pod["serviceAccountName"] = false, account
			templateMeta := deploymentplan.Map(deploymentplan.Map(deploymentplan.Map(d.Object["spec"])["template"])["metadata"])
			if templateMeta == nil {
				templateMeta = deploymentplan.Object{}
				deploymentplan.Map(deploymentplan.Map(d.Object["spec"])["template"])["metadata"] = templateMeta
			}
			labels := deploymentplan.Map(templateMeta["labels"])
			if labels == nil {
				labels = deploymentplan.Object{}
				templateMeta["labels"] = labels
			}
			labels["rtk.realtek.com/pki-state-backend"] = "openbao"
			add(deploymentplan.Object{"apiVersion": "v1", "kind": "ServiceAccount", "metadata": deploymentplan.Object{"name": account, "namespace": namespace}, "automountServiceAccountToken": false})
			pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{
				deploymentplan.Object{"name": "pki-custody-auth", "projected": deploymentplan.Object{"defaultMode": float64(288), "sources": []any{
					deploymentplan.Object{"serviceAccountToken": deploymentplan.Object{"audience": "openbao", "expirationSeconds": float64(900), "path": "openbao.jwt"}},
					deploymentplan.Object{"serviceAccountToken": deploymentplan.Object{"expirationSeconds": float64(900), "path": "kube.jwt"}},
					deploymentplan.Object{"configMap": deploymentplan.Object{"name": "kube-root-ca.crt", "items": []any{deploymentplan.Object{"key": "ca.crt", "path": "ca.crt"}}}},
				}}}, environmentCreateServiceVolume("pki-custody-trust", "configMap", environmentCustodyTrustName),
			})
		}
		if len(podDependencies) != 0 {
			dependencies := append([]string(nil), d.Dependencies...)
			for id := range podDependencies {
				if !slices.Contains(dependencies, id) {
					dependencies = append(dependencies, id)
				}
			}
			sort.Strings(dependencies)
			d.Dependencies = dependencies
		}
		result = append(result, d)
	}
	seen := map[string]deploymentplan.Object{}
	for _, d := range result {
		seen[deploymentplan.ID(d.Object)] = d.Object
	}
	ids := make([]string, 0, len(dependencies))
	for id := range dependencies {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if existing := seen[id]; existing == nil {
			result = append(result, deploymentplan.Definition{Object: dependencies[id], Phase: "bootstrap"})
		} else if existing["kind"] != "ServiceAccount" && existing["kind"] != "Lease" && deploymentplan.Digest(existing) != deploymentplan.Digest(dependencies[id]) {
			return nil, environmentCreateBootstrapBlocker("existing native custody RBAC conflicts with the exact workload owner and identity Lease")
		} else {
			for i := range result {
				if deploymentplan.ID(result[i].Object) != id {
					continue
				}
				if result[i].Retire {
					return nil, environmentCreateBootstrapBlocker("native custody cannot depend on an access owner selected for retirement")
				}
				if result[i].Phase != "namespace" && result[i].Phase != "storage" {
					result[i].Phase = "bootstrap"
				}
			}
		}
	}
	return result, nil
}

func environmentCustodyPolicy(values map[string]string, environment string, binding environmentCustodyBinding, owner environmentCustodyOwnerBinding) (string, error) {
	if err := validateEnvironmentCustodySettings(values); err != nil {
		return "", err
	}
	if values["PKI_STATE_BACKEND"] != "openbao" || !environmentPreflightDigest.MatchString(binding.Generation) || !secretEnvironmentPattern.MatchString(environment) || !secretEnvironmentPattern.MatchString(owner.ID) {
		return "", environmentCreateBootstrapBlocker("native custody policy requires its exact environment, generation and workload owner")
	}
	bound := false
	for _, original := range binding.Owners {
		if sameEnvironmentCustodyOwner(original, owner) {
			bound = true
		}
	}
	if !bound || values["CLOUD_STACK_NAME"] != "video-cloud-"+environment || !strings.HasPrefix(owner.Namespace, values["CLOUD_STACK_NAME"]+"-") || !secretEnvironmentPattern.MatchString(owner.ServiceAccount) || owner.ServiceAccount == "default" {
		return "", environmentCreateBootstrapBlocker("native custody policy cannot authorize an owner outside its selected original generation")
	}
	return fmt.Sprintf("path %q { capabilities = [\"create\", \"read\", \"update\"] }\n", values["PKI_STATE_KV_MOUNT"]+"/data/"+environment+"/"+binding.Generation+"/"+owner.ID+"/*"), nil
}

func sameEnvironmentCustodyOwner(a, b environmentCustodyOwnerBinding) bool {
	return a.ID == b.ID && a.Namespace == b.Namespace && a.ServiceAccount == b.ServiceAccount && slices.Equal(a.StorageAliases, b.StorageAliases)
}
