package main

import (
	"context"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
)

// Only a genuinely expired seed is replaced. Existing installed identities stay
// on their original owner PVCs; their native managers attest current material.
func prepareUsedMQTTBootstrapRenewal(ctx context.Context, cfg deploymentConfig, store secretStore, e *environmentCreateExecution, b environmentKubernetesBackend, p *createFoundationRecoveryPlan, r *createServiceSessionRecovery) error {
	if !immutableEnvironmentImage.MatchString(r.GrantImage.Reference) {
		return createProviderRecoveryBlock("expired seed renewal requires the exact formal repair image")
	}
	original, _, err := acceptedCreateStage(store, e, r.SourceRenewalStage)
	if err != nil {
		return err
	}
	if len(original.Resources) != 1 || original.Resources[0].Desired["kind"] != "Job" {
		return createProviderRecoveryBlock("original accepted used bootstrap recipe is required")
	}
	object := deploymentplan.Clone(original.Resources[0].Desired)
	c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(object)["containers"])[0])
	c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_CERT_PATH": "/run/pki-bootstrap/bootstrap.crt"}))
	binding := r.MQTTPendingPlan.SHA256()
	if r.RenewalBindingSHA != "" {
		binding = r.RenewalBindingSHA
	}
	if !environmentPreflightDigest.MatchString(binding) {
		return createProviderRecoveryBlock("original expired seed renewal binding is invalid")
	}
	r.RenewedIntent.SessionID = sessionRenewalUUID(e.Journal.PlanSHA256, binding, r.Ceremony.BootstrapSessionID)
	definition, err := originalKeyRenewalDefinition(object, e.Journal.PlanSHA256, binding, r.RenewedIntent.SessionID)
	if err != nil {
		return err
	}
	c = deploymentplan.Map(certIssuerObjectList(environmentPKIPod(definition.Object)["containers"])[0])
	c["image"] = r.GrantImage.Reference
	c["env"] = environmentCreateServiceWithoutEnv(c["env"], "PKI_BOOTSTRAP_EXPIRES_AT")
	c["env"] = mergeEnvironmentNamedFields(c["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_TTL": "8h"}))
	r.RenewedCertPath = "/run/pki-bootstrap/renew-" + binding[:12] + "/bootstrap.crt"
	live, err := b.Inventory(ctx, p.Plan.Stack)
	if err != nil {
		return err
	}
	r.RenewalCandidate, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("used-bootstrap-original-key-renewal", []deploymentplan.Definition{definition})}, []deploymentplan.Definition{definition}, live)
	if err != nil {
		return err
	}
	if len(r.RenewalCandidate.Resources) != 1 || r.RenewalCandidate.Resources[0].Action != "create" {
		return createProviderRecoveryBlock("used bootstrap renewal is already present; reconcile native progress")
	}
	p.Materials["accepted-service-core-used-renewal-plan"] = r.RenewalCandidate.SHA256()
	if r.PostTransportResume {
		if err := prepareUsedInstallationQuiesce(ctx, cfg, e, b, p, r); err != nil {
			return err
		}
		return nil
	}
	var future []deploymentplan.Definition
	for _, resource := range r.Candidate.Resources {
		object := deploymentplan.Clone(resource.Desired)
		if object["kind"] == "Deployment" {
			for _, v := range certIssuerObjectList(environmentPKIPod(object)["containers"]) {
				container := deploymentplan.Map(v)
				container["env"] = mergeEnvironmentNamedFields(container["env"], environmentCreateEnv(map[string]string{"PKI_BOOTSTRAP_SESSION_ID": r.RenewedIntent.SessionID}))
			}
		}
		future = append(future, deploymentplan.Definition{Phase: resource.Phase, Object: object})
	}
	r.Candidate, err = deploymentplan.Compile(deploymentplan.Inputs{Environment: cfg.Environment, Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest(p.Plan.Release), BindingsSHA256: environmentCreateStageBindingsSHA("mqtt-renewed-trust-forward", future)}, future, live)
	if err != nil {
		return err
	}
	p.Materials["accepted-service-core-mqtt-forward-plan"] = r.Candidate.SHA256()
	return nil
}

func executeUsedMQTTBootstrapRenewal(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, store secretStore, runtime environmentCreateBootstrapRuntime, apply environmentCreateBootstrapApply) (environmentCreateServiceCeremony, error) {
	r := runtime.ServiceSessionRecovery
	s, resumed, err := resumeAcceptedUsedBootstrapCore(ctx, cfg, p, store, runtime)
	if err != nil {
		return s, err
	}
	if !resumed {
		s = r.Ceremony
		subjects, err := environmentCreateBootstrapSubjects(cfg)
		if err != nil {
			return s, err
		}
		candidate := r.Candidate
		r.Candidate = r.RenewalCandidate
		s, err = createEnvironmentServiceSession(ctx, cfg, p, store, runtime, s, subjects, apply)
		r.Candidate = candidate
		if err != nil {
			return s, err
		}
	}
	// Use the complete original subject policy, including retained application
	// owners, when creating and distributing the renewed credential.
	defs, err := renderEnvironmentCreateManagedServiceDefinitions(ctx, cfg, p, runtime, s)
	if err != nil {
		return s, err
	}
	for _, d := range defs {
		if d.Object["kind"] == "Deployment" {
			for _, v := range certIssuerObjectList(environmentPKIPod(d.Object)["containers"]) {
				c := deploymentplan.Map(v)
				if c["name"] == "pki-controller" || c["name"] == "certissuer" {
					c["image"] = r.GrantImage.Reference
				}
			}
		}
	}
	if err := apply(ctx, "service-managed-used-renewal-"+r.Review, defs); err != nil {
		return s, err
	}
	if r.QuiesceCandidate != nil {
		var stop []deploymentplan.Definition
		for _, v := range r.QuiesceCandidate.Resources {
			stop = append(stop, deploymentplan.Definition{Phase: v.Phase, Object: deploymentplan.Clone(v.Desired)})
		}
		if err := apply(ctx, "used-installation-owner-quiesce-"+r.Review, stop); err != nil {
			return s, err
		}
		if err := waitCurrentIdentityOwnersStopped(ctx, p.Stack, runtime.Backend, r.QuiesceCandidate); err != nil {
			return s, err
		}
	}
	enroll, err := renderEnvironmentCreateApplicationEnrollment(cfg, p, runtime, s)
	if err != nil {
		return s, err
	}
	for _, d := range enroll {
		if d.Object["kind"] == "Job" {
			c := deploymentplan.Map(certIssuerObjectList(environmentPKIPod(d.Object)["containers"])[0])
			path := mqttFoundationEnv(c, "PKI_CLIENT_IDENTITY_STATE")
			command := certIssuerObjectList(c["command"])
			if path == "" || strings.ContainsAny(path, "'\n\r") || len(command) != 2 {
				return s, environmentCreateBootstrapBlocker("retained application identity command changed")
			}
			subject := deploymentplan.String(command[1])
			if !strings.HasPrefix(subject, "service:") {
				return s, environmentCreateBootstrapBlocker("retained application subject changed")
			}
			c["command"] = []any{"/bin/sh", "-ec", "test -s " + path + " && grep -q '\"current\"' " + path + " && ! grep -q '\"pending\"' " + path + " && /app/serviceidentity-bootstrap " + subject}
		}
	}
	if err := apply(ctx, "application-current-installation-renewal-"+r.Review, enroll); err != nil {
		return s, err
	}
	return s, nil
}
