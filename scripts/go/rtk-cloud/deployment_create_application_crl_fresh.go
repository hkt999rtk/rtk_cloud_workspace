package main

import (
	"context"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Initialize only the native create's originally selected owner scopes; the
// binary is the exact image from the qualified environment release.
func initializeFreshApplicationCRLs(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, roots, intermediates map[string]environmentCreateRegisteredIssuer, applications []deploymentplan.Definition, apply environmentCreateBootstrapApply) error {
	if cfg.Values["PKI_STATE_BACKEND"] != "openbao" {
		return nil
	}
	if apply == nil || p.Image != release.Images["LKE_VIDEO_CLOUD_IMAGE"].Reference || !immutableEnvironmentImage.MatchString(p.Image) {
		return environmentCreateBootstrapBlocker("application CRL initialization requires the exact selected environment image")
	}
	manifest, err := environmentCreateServiceManifest(s.Root, s.Intermediate)
	if err != nil {
		return err
	}
	serviceTrust := environmentCreateServiceTrustDefinition(p.Stack+"-video-cloud", s.Root, "", manifest)
	data := deploymentplan.Map(serviceTrust.Object["data"])
	data["listener-crls.json"] = environmentCreateServiceCRLManifest(s.Root, environmentCreateCoreCRLPath("listener"), s.Intermediate)
	data["outbound-crls.json"] = environmentCreateServiceCRLManifest(s.Root, environmentCreateCoreCRLPath("outbound"), s.Intermediate)
	definitions := append(append([]deploymentplan.Definition(nil), applications...), serviceTrust)
	issuers := []environmentCreateRegisteredIssuer{s.Root, s.Intermediate}
	for _, domain := range []string{"mqtt", "device", "app"} {
		root, ok := roots[domain]
		if !ok {
			return environmentCreateBootstrapBlocker("initial application CRLs require original selected issuer lineages")
		}
		issuers = append(issuers, root)
		if intermediate, ok := intermediates[domain]; ok {
			issuers = append(issuers, intermediate)
		}
	}
	jobs, err := renderEnvironmentCreateApplicationCRLInitialization(ctx, cfg, p, runtime, s, definitions, issuers)
	if err != nil {
		return err
	}
	if len(jobs) == 0 {
		return environmentCreateBootstrapBlocker("initial application CRL manifests produced no native owner jobs")
	}
	return apply(ctx, "application-initial-crls", jobs)
}

// Keep native initializers and workload rollout ordered for a fresh generation.
// Existing-generation replay uses its separately qualified forward executor.
func applyCreateApplicationOwners(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, release environmentRelease, runtime environmentCreateBootstrapRuntime, s environmentCreateServiceCeremony, roots, intermediates map[string]environmentCreateRegisteredIssuer, applications []deploymentplan.Definition, apply environmentCreateBootstrapApply) error {
	if apply == nil {
		return environmentCreateBootstrapBlocker("application owner executor is unavailable")
	}
	if runtime.ServiceSessionRecovery == nil {
		if err := initializeFreshApplicationCRLs(ctx, cfg, p, release, runtime, s, roots, intermediates, applications, apply); err != nil {
			return err
		}
	}
	return apply(ctx, "managed-application-owners", applications)
}
