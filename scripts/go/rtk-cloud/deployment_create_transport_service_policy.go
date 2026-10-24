package main

import (
	"context"
	"slices"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Initial CA ceremonies keep their admitted roster. Once the maintained
// managed hosts run, their already enrolled clients also consume Service CRLs.
func managedTransportServiceConsumerIDs(original []string) ([]string, error) {
	seen := map[string]bool{}
	ids := slices.Clone(original)
	for _, id := range ids {
		if !environmentCreateConsumerID.MatchString(id) || seen[id] {
			return nil, environmentCreateBootstrapBlocker("managed transport Service consumers require the original distinct native receipt IDs")
		}
		seen[id] = true
	}
	for _, id := range []string{"pkibroker", "emqx-pki", "openbao"} {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	return ids, nil
}

func configureCreateTransportServiceConsumerController(p environmentCreateBootstrapPlan, object deploymentplan.Object) (deploymentplan.Object, error) {
	if deploymentplan.ID(object) != "Deployment/"+p.Stack+"-video-cloud/pki-controller" {
		return nil, environmentCreateBootstrapBlocker("managed transport Service policy requires its original Controller owner")
	}
	ids, err := managedTransportServiceConsumerIDs(findEnvironmentCreateDomainPolicy(p, "service").Consumers)
	if err != nil {
		return nil, err
	}
	out := deploymentplan.Clone(object)
	container := managedNamedField(environmentPKIPod(out)["containers"], "pki-controller")
	if container == nil {
		return nil, environmentCreateBootstrapBlocker("managed transport Service policy lost its original Controller consumer")
	}
	container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_REQUIRED_CONSUMERS_SERVICE": strings.Join(ids, ",")}))
	return out, nil
}

// Only these two authenticated existing owners extend the host stage. Public
// CRL metadata is derived from the same registered Service lineage as the rest
// of the recipe; no native receipt, certificate or private key is manufactured.
func renderCreateTransportServicePolicyDefinitions(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, runtime environmentCreateBootstrapRuntime, service environmentCreateServiceCeremony) ([]deploymentplan.Definition, error) {
	controllerID := "Deployment/" + p.Stack + "-video-cloud/pki-controller"
	trustID := "ConfigMap/" + p.Stack + "-secrets/pki-create-service-trust"
	var controller, trust deploymentplan.Object
	if r := runtime.ServiceSessionRecovery; r != nil && r.ManagedTransportRecovery != nil && r.ManagedTransportRecovery.Partial != nil {
		owners := r.ManagedTransportRecovery.Partial.AdditionalOwners
		for _, id := range []string{controllerID, trustID} {
			if owners[id].UID == "" || deploymentplan.ID(owners[id].Desired) != id {
				return nil, environmentCreateBootstrapBlocker("transport Service policy lacks exact accepted additional owner bindings")
			}
		}
		controller, trust = owners[controllerID].Desired, owners[trustID].Desired
	} else {
		definitions, err := renderEnvironmentCreateManagedServiceDefinitions(ctx, cfg, p, runtime, service)
		if err != nil {
			return nil, err
		}
		for _, d := range definitions {
			if deploymentplan.ID(d.Object) == controllerID {
				controller = d.Object
			}
		}
		trust = environmentCreateTransportTrust(p.Stack+"-secrets", "pki-create-service-trust", service.Root, service.Intermediate).Object
	}
	updated, err := configureCreateTransportServiceConsumerController(p, controller)
	if err != nil {
		return nil, err
	}
	trust = deploymentplan.Clone(trust)
	data := deploymentplan.Map(trust["data"])
	if data["root.pem"] != service.Root.Certificate {
		return nil, environmentCreateBootstrapBlocker("transport Service CRL producer differs from the original registered Root")
	}
	data["outbound-crls.json"] = environmentCreateServiceCRLManifest(service.Root, environmentCreateServiceState+"/private/crl/outbound-root.json", service.Intermediate)
	return []deploymentplan.Definition{{Phase: "workload", Object: updated}, {Phase: "configuration", Object: trust}}, nil
}

// These aliases mount only each consumer's existing owner claim. CRL state can
// use the common genuine manifest without moving either enrolled identity.
func applyCreateTransportServiceCache(pod deploymentplan.Object, container deploymentplan.Object, volume, claim string) {
	pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume(volume, "persistentVolumeClaim", claim)})
	container["volumeMounts"] = mergeEnvironmentNamedFields(container["volumeMounts"], []any{environmentCreateServiceMount(volume, environmentCreateServiceState, false)})
}
