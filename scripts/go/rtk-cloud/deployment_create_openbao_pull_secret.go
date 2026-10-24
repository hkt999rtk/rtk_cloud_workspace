package main

import "rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"

func environmentOpenBaoImagePullSecretName(values map[string]string) string {
	return firstNonEmpty(values["LKE_IMAGE_PULL_SECRET_NAME"], "ghcr-pull")
}

// The original-foundation target gate independently checks live Secret UID and
// body. This candidate check binds the declared registry reference to that exact
// accepted owner/type; it never parses credentials or probes a registry.
func validateManagedOpenBaoImagePullSecret(cfg deploymentConfig, candidate, foundation *deploymentplan.Plan, accepted deploymentplan.Journal) error {
	bad := func() error {
		return createProviderRecoveryBlock("managed OpenBao requires its exact selected original registry pull Secret reference, recorded owner and dockerconfigjson type")
	}
	if candidate == nil || foundation == nil || !accepted.Accepted || accepted.InFlight != "" || accepted.Failed != "" || accepted.PlanSHA256 != foundation.SHA256() || cfg.Environment != candidate.Environment || cfg.Environment != foundation.Environment || candidate.Stack != cfg.Values["CLOUD_STACK_NAME"] || foundation.Stack != candidate.Stack {
		return bad()
	}
	name := environmentOpenBaoImagePullSecretName(environmentCreateProviderValues(cfg))
	id := "Secret/" + candidate.Stack + "-secrets/" + name
	openbaoID := "StatefulSet/" + candidate.Stack + "-secrets/openbao"
	foundHost, foundSecret := false, false
	for _, resource := range candidate.Resources {
		if resource.ID != openbaoID {
			continue
		}
		if foundHost {
			return bad()
		}
		refs := certIssuerObjectList(environmentPKIPod(resource.Desired)["imagePullSecrets"])
		if len(refs) != 1 || len(deploymentplan.Map(refs[0])) != 1 || deploymentplan.Map(refs[0])["name"] != name {
			return bad()
		}
		foundHost = true
	}
	for _, resource := range foundation.Resources {
		if resource.ID != id {
			continue
		}
		if foundSecret || accepted.Owners[id] == "" || deploymentplan.ID(resource.Desired) != id || resource.Desired["type"] != "kubernetes.io/dockerconfigjson" {
			return bad()
		}
		foundSecret = true
	}
	if !foundHost || !foundSecret {
		return bad()
	}
	return nil
}
