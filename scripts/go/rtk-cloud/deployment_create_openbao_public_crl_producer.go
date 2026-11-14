package main

import (
	"context"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
)

const openBaoPublicCRLCapability = "initial-openbao-public-crl-custody-v1"

func renderEnvironmentCreateOpenBaoPublicCRLDefinitions(ctx context.Context, cfg deploymentConfig, p environmentCreateBootstrapPlan, s environmentCreateServiceCeremony, core []deploymentplan.Definition, tool environmentReleaseImage) ([]deploymentplan.Definition, error) {
	bad := func() ([]deploymentplan.Definition, error) {
		return nil, createProviderRecoveryBlock("qualified original OpenBao public CRL purposes and helper image required")
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if cfg.Values["PKI_STATE_BACKEND"] != "openbao" {
		return nil, nil
	}
	if p.Environment != cfg.Environment || p.Stack != cfg.Values["CLOUD_STACK_NAME"] || !immutableEnvironmentImage.MatchString(tool.Reference) || !strings.HasPrefix(tool.Reference, "ghcr.io/hkt999rtk/rtk_video_cloud/video-cloud-api@sha256:") || !deploymentUpgradeSHA.MatchString(tool.SourceRevision) {
		return bad()
	}
	entries, err := originalOpenBaoPublicCRLPurposes(cfg.Environment, p.Stack, core)
	if err != nil {
		return nil, err
	}
	command := []any{"/app/serviceidentity-bootstrap", "initialize-public-crl"}
	for _, entry := range entries {
		command = append(command, entry.StatePath, entry.Issuer.ID, deploymentplan.String(entry.Issuer.Document["certificate_fingerprint_sha256"]))
	}
	selected := p
	selected.Image = tool.Reference
	var out []deploymentplan.Definition
	for _, role := range []string{"pki-controller", "certissuer"} {
		job := environmentCreateServiceEnrollmentJob(selected, p.Stack+"-video-cloud", role+"-openbao-crls", environmentCreateServiceOwnerClaim(role), s, command, map[string]string{"PKI_ENVIRONMENT": cfg.Environment, "PKI_CLIENT_IDENTITY_STATE": environmentCreateServiceOwnerIdentityPath(role, "client.json")}, false)
		pod := environmentPKIPod(job.Object)
		pod["volumes"] = environmentCreateServiceWithoutNamed(pod["volumes"], "bootstrap", "service-trust")
		container := managedNamedField(pod["containers"], "enroll")
		container["volumeMounts"] = environmentCreateServiceWithoutNamed(container["volumeMounts"], "bootstrap", "service-trust")
		out = append(out, job)
	}
	return out, nil
}

type createOpenBaoPublicCRLPurpose struct {
	StatePath string                            `json:"state_path"`
	Issuer    environmentCreateRegisteredIssuer `json:"issuer"`
}

// Both owners consume this same separately authenticated Root/intermediate
// manifest. Paths remain native logical purposes inside each original owner.
func originalOpenBaoPublicCRLPurposes(environment, stack string, core []deploymentplan.Definition) ([]createOpenBaoPublicCRLPurpose, error) {
	bad := func() ([]createOpenBaoPublicCRLPurpose, error) {
		return nil, createProviderRecoveryBlock("original OpenBao public CRL manifest or provider owner differs")
	}
	ns, name := stack+"-video-cloud", "pki-create-openbao-tls-consumer-trust"
	var manifest string
	owners := map[string]bool{}
	for _, d := range core {
		if deploymentplan.ID(d.Object) == "ConfigMap/"+ns+"/"+name {
			if manifest != "" {
				return bad()
			}
			manifest = deploymentplan.String(deploymentplan.Map(d.Object["data"])["crls.json"])
		}
		for _, role := range []string{"pki-controller", "certissuer"} {
			if deploymentplan.ID(d.Object) != "Deployment/"+ns+"/"+role {
				continue
			}
			if owners[role] {
				return bad()
			}
			pod := environmentPKIPod(d.Object)
			container := managedNamedField(pod["containers"], role)
			volume := managedNamedField(pod["volumes"], "openbao-create-trust")
			mount := managedNamedField(container["volumeMounts"], "openbao-create-trust")
			if container == nil || mqttFoundationEnv(container, "OPENBAO_SERVER_CRL_MANIFEST") != "/run/pki-openbao-create/crls.json" || deploymentplan.Map(volume["configMap"])["name"] != name || mount["mountPath"] != "/run/pki-openbao-create" || mount["readOnly"] != true {
				return bad()
			}
			owners[role] = true
		}
	}
	var entries []struct {
		Issuer    deploymentplan.Object `json:"issuer"`
		StatePath string                `json:"state_path"`
	}
	if !owners["pki-controller"] || !owners["certissuer"] || decodeEnvironmentJSON([]byte(manifest), &entries) != nil || len(entries) != 2 {
		return bad()
	}
	root, err := environmentCreateRegisteredIssuerFromMap(entries[0].Issuer, "")
	if err != nil || root.Environment != environment || root.Domain != "openbao_tls" || root.Kind != "root" || root.Status != "active" || entries[0].StatePath != environmentCreateServiceState+"/private/crl/openbao-root.json" {
		return bad()
	}
	intermediate, err := environmentCreateRegisteredIssuerFromMap(entries[1].Issuer, root.RootFingerprint)
	if err != nil || intermediate.Environment != environment || intermediate.Domain != root.Domain || intermediate.Kind != "intermediate" || intermediate.Status != "active" || intermediate.Document["parent_issuer_id"] != root.ID || entries[1].StatePath != environmentCreateServiceState+"/private/crl/openbao-root-"+intermediate.ID+".json" {
		return bad()
	}
	rootCert, rootErr := parseFirstCertificatePEM([]byte(root.Certificate))
	intermediateCert, intermediateErr := parseFirstCertificatePEM([]byte(intermediate.Certificate))
	if rootErr != nil || intermediateErr != nil || rootCert.CheckSignatureFrom(rootCert) != nil || intermediateCert.CheckSignatureFrom(rootCert) != nil || environmentPreflightBytesSHA256(rootCert.Raw) != root.RootFingerprint || environmentPreflightBytesSHA256(intermediateCert.Raw) != deploymentplan.String(intermediate.Document["certificate_fingerprint_sha256"]) {
		return bad()
	}
	return []createOpenBaoPublicCRLPurpose{{entries[0].StatePath, root}, {entries[1].StatePath, intermediate}}, nil
}
