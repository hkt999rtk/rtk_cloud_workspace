package main

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
)

// The legacy whole-Deployment renderer owns static identity Secrets. It must
// never reconcile a separately managed installation, including an unhealthy
// one: a failed Pod is not permission to overwrite its persistent identity.
func lkeRequireCertIssuerRendererCompatibility(env map[string]string) error {
	return lkeRequireCertIssuerRendererCompatibilityContext(context.Background(), env)
}

func lkeRequireCertIssuerRendererCompatibilityContext(ctx context.Context, env map[string]string) error {
	runtime := newDeploymentCheckRuntime(ctx, "")
	read := func(_ io.Reader, args ...string) ([]byte, error) {
		return runtime.run(false, lkeKubectlArgs(args...)...)
	}
	deployment, err := certIssuerReadPublicObject(read, "deployment", lkeNamespaceName(env, "video-cloud"), "certissuer")
	if err != nil {
		return fmt.Errorf("inspect CertIssuer renderer compatibility: %w", err)
	}
	return lkeValidateCertIssuerLegacyRenderer(deployment)
}

func lkeValidateCertIssuerLegacyRenderer(deployment certIssuerIngressObject) error {
	if deployment == nil {
		return nil
	}
	pod := certIssuerObjectMap(certIssuerObjectMap(certIssuerObjectMap(deployment["spec"])["template"])["spec"])
	containers := append([]any{}, certIssuerObjectList(pod["containers"])...)
	containers = append(containers, certIssuerObjectList(pod["initContainers"])...)
	if len(containers) == 0 {
		return fmt.Errorf("existing CertIssuer Deployment has no inspectable containers; refusing legacy reconciliation")
	}
	for _, item := range containers {
		container := certIssuerObjectMap(item)
		name := certIssuerObjectString(container["name"])
		if name == "pkimanagement" || name == "pki-management" || name == "serviceidentity" {
			return lkeCertIssuerManagedRendererError("managed identity sidecar")
		}
		for _, field := range []string{"command", "args"} {
			for _, argument := range certIssuerObjectList(container[field]) {
				program := filepath.Base(certIssuerObjectString(argument))
				if program == "pkimanagement" || program == "serviceidentity-bootstrap" {
					return lkeCertIssuerManagedRendererError("managed identity container")
				}
			}
		}
		for _, item := range certIssuerObjectList(container["env"]) {
			setting := certIssuerObjectMap(item)
			key, value := certIssuerObjectString(setting["name"]), strings.TrimSpace(certIssuerObjectString(setting["value"]))
			switch key {
			case "CERT_ISSUER_HOST_IDENTITY_STATE":
				if value != "" || setting["valueFrom"] != nil {
					return lkeCertIssuerManagedRendererError(key)
				}
			case "CERT_ISSUER_SERVICE_CLIENT_PKI_ENABLED", "CERT_ISSUER_HOST_PKI_ENABLED":
				if setting["valueFrom"] != nil {
					return lkeCertIssuerManagedRendererError("indirect " + key)
				}
				if enabled, err := strconv.ParseBool(value); value != "" && (err != nil || enabled) {
					return lkeCertIssuerManagedRendererError(key)
				}
			}
		}
	}
	return nil
}

func lkeCertIssuerManagedRendererError(reason string) error {
	return fmt.Errorf("legacy whole-Deployment CertIssuer reconciliation would overwrite managed PKI (%s); for dev/staging full upgrades prepare and review ~/.config/rtk_cloud/<environment>/deployment/managed-upgrade.json with deployment prepare-upgrade; preserve identity state, CRLs and Secrets; otherwise use an explicitly reviewed provision --deploy --workloads ... rollout or route-only provision --dns, and handle identity/schema changes through a separate managed PKI migration", reason)
}
