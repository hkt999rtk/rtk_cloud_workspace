package main

import (
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func environmentCustodyProductWorkload(service string) string {
	return map[string]string{"mqtt": "video-cloud-mqttfoundation", "shadow": "video-cloud-shadowworker", "ota": "video-cloud-otaservice", "video-storage": "video-cloud-videostorage", "webrtc": "video-cloud-webrtcservice", "logger": "video-cloud-logingester"}[service]
}

// Native product registration owns the same durable record initialized by its
// qualified enrollment. Add that declared state purpose before common custody
// projection; legacy PEM Secret delivery is not an identity authority.
func projectEnvironmentCustodyRegistrations(values map[string]string, environment string, binding *environmentCustodyBinding, definitions []deploymentplan.Definition) ([]deploymentplan.Definition, error) {
	cfg := deploymentConfig{Environment: environment, Values: values}
	products, err := environmentCreateProductIdentityIntent(cfg)
	if err != nil {
		return nil, err
	}
	stack := values["CLOUD_STACK_NAME"]
	byWorkload := map[string]environmentCreateProductIdentity{}
	for _, product := range products {
		byWorkload["Deployment/"+stack+"-video-cloud/"+environmentCustodyProductWorkload(strings.TrimPrefix(product.Subject, "service:"))] = product
	}
	out := make([]deploymentplan.Definition, 0, len(definitions))
	for _, definition := range definitions {
		product, selected := byWorkload[deploymentplan.ID(definition.Object)]
		if !selected || definition.Retire {
			out = append(out, definition)
			continue
		}
		if !environmentPreflightDigest.MatchString(binding.ServiceRootSHA256) {
			return nil, environmentCreateBootstrapBlocker("registration custody requires its original registered Service Root binding")
		}
		owned := false
		for _, owner := range binding.Owners {
			for _, alias := range owner.StorageAliases {
				if owner.Namespace == stack+"-video-cloud" && alias == product.Claim {
					owned = true
				}
			}
		}
		if !owned {
			return nil, environmentCreateBootstrapBlocker("registration state is not bound to its original workload custody scope")
		}
		d := definition
		d.Object = deploymentplan.Clone(definition.Object)
		pod := environmentPKIPod(d.Object)
		if pod == nil {
			return nil, environmentCreateBootstrapBlocker("registration owner has no native Pod definition")
		}
		identityVolumes := map[string]bool{}
		for _, raw := range certIssuerObjectList(pod["volumes"]) {
			volume := deploymentplan.Map(raw)
			if deploymentplan.String(deploymentplan.Map(volume["secret"])["secretName"]) == strings.Split(product.ID, "/")[2] {
				identityVolumes[deploymentplan.String(volume["name"])] = true
			}
		}
		found := false
		const stateVolume = "pki-registration-state"
		const trustVolume = "pki-registration-service-trust"
		const trustPath = "/run/pki-registration-service-trust"
		for _, raw := range certIssuerObjectList(pod["containers"]) {
			container := deploymentplan.Map(raw)
			selected := false
			for _, raw := range certIssuerObjectList(container["volumeMounts"]) {
				if identityVolumes[deploymentplan.String(deploymentplan.Map(raw)["name"])] {
					selected = true
				}
			}
			if !selected {
				continue
			}
			if found {
				return nil, environmentCreateBootstrapBlocker("registration identity has multiple declared runtime owners")
			}
			found = true
			for _, raw := range certIssuerObjectList(container["volumeMounts"]) {
				mount := deploymentplan.Map(raw)
				if mount["mountPath"] == environmentCreateServiceState && mount["name"] != stateVolume {
					return nil, environmentCreateBootstrapBlocker("registration state overlaps another declared identity purpose")
				}
			}
			controller := "pki-controller." + stack + "-video-cloud.svc"
			issuer := "certissuer." + stack + "-video-cloud.svc"
			settings := map[string]string{"PKI_REGISTRATION_IDENTITY_STATE": environmentCreateServiceState + "/private/client.json", "PKI_REGISTRATION_ROOT_SHA256": binding.ServiceRootSHA256,
				"PKI_REGISTRATION_CONTROLLER_URL": "https://" + controller + ":18446", "PKI_REGISTRATION_RENEWAL_URL": "https://" + issuer + ":18445"}
			for prefix, name := range map[string]string{"PKI_REGISTRATION_CONTROLLER": controller, "PKI_REGISTRATION_RENEWAL": issuer} {
				environmentCreateServiceHTTPSettings(settings, prefix, name, binding.ServiceRootSHA256, false, environmentCreateBootstrapPlan{Environment: environment, Stack: stack})
				settings[prefix+"_TLS_CA"] = trustPath + "/root.pem"
			}
			container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(settings))
			container["volumeMounts"] = mergeEnvironmentNamedFields(container["volumeMounts"], []any{environmentCreateServiceMount(stateVolume, environmentCreateServiceState, false), environmentCreateServiceMount(trustVolume, trustPath, true)})
		}
		if !found {
			return nil, environmentCreateBootstrapBlocker("registration workload lacks its exact native credential-purpose mount")
		}
		pod["volumes"] = mergeEnvironmentNamedFields(pod["volumes"], []any{environmentCreateServiceVolume(stateVolume, "persistentVolumeClaim", product.Claim), environmentCreateServiceVolume(trustVolume, "configMap", "pki-create-service-trust")})
		out = append(out, d)
	}
	return out, nil
}
