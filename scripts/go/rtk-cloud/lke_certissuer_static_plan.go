package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

// Full static rendering replaces the Service and listener configuration. Its
// preflight qualifies the exact identity that will be reused, rather than
// requiring that repairable old topology already matches the desired render.
// Route-only and targeted deployments continue using the installed-policy plan.
func lkePlanCertIssuerIngressMigrationForStaticRenderWithContext(ctx context.Context, paths provisionPaths, env map[string]string) (*lkeCertIssuerIngressMigration, error) {
	command := func(_ io.Reader, args ...string) ([]byte, error) {
		return newDeploymentCheckRuntime(ctx, "").run(false, lkeKubectlArgs(args...)...)
	}
	return lkePlanCertIssuerIngressMigrationForStaticRenderWithIO(ctx, paths, env, command, newCertIssuerIngressIO(ctx))
}

func lkePlanCertIssuerIngressMigrationForStaticRenderWithIO(ctx context.Context, paths provisionPaths, env map[string]string, command certIssuerIngressCommand, access certIssuerIngressIO) (*lkeCertIssuerIngressMigration, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	config := lkeResolveCertIssuerTLSConfig(env)
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if config.PublicHost == "" {
		return lkePlanCertIssuerIngressMigrationWithIO(paths, env, access)
	}
	deployment, err := certIssuerReadPublicObject(command, "deployment", config.Namespace, "certissuer")
	if err != nil {
		return nil, fmt.Errorf("inspect CertIssuer static renderer ownership: %w", err)
	}
	if err := lkeValidateCertIssuerLegacyRenderer(deployment); err != nil {
		return nil, err
	}
	if err := certIssuerValidateStaticRenderOwner("Deployment", deployment, config, env); err != nil {
		return nil, err
	}
	service, err := certIssuerReadPublicObject(command, "service", config.Namespace, "certissuer")
	if err != nil {
		return nil, fmt.Errorf("inspect CertIssuer static Service ownership: %w", err)
	}
	if err := certIssuerValidateStaticRenderOwner("Service", service, config, env); err != nil {
		return nil, err
	}
	if service != nil && certIssuerObjectString(certIssuerObjectMap(service["spec"])["clusterIP"]) == "None" {
		return nil, errors.New("existing CertIssuer Service is headless; full static rendering cannot safely repair its immutable ClusterIP topology")
	}
	strictServing := access.serving
	access.serving = func(cfg lkeCertIssuerTLSConfig, allowAbsent, requireAvailable bool) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !requireAvailable {
			return nil
		}
		// Apply and Verify always require the complete installed serving policy,
		// certificate identity and current available workload generation.
		return strictServing(cfg, allowAbsent, true)
	}
	plan, err := lkePlanCertIssuerIngressMigrationWithIO(paths, env, access)
	if err != nil {
		return nil, err
	}
	if deployment == nil {
		// A public plan always creates the canonical route when no route exists.
		// No changes means it already exists; a Before snapshot proves another
		// existing route. Neither can be treated as permission to mint a new CA.
		existingRoute := len(plan.Changes) == 0
		for _, change := range plan.Changes {
			existingRoute = existingRoute || change.Before != nil
		}
		if service != nil || existingRoute {
			return nil, errors.New("existing CertIssuer Service or ingress has no inspectable Deployment identity source; restore the existing identity before full static rendering")
		}
		// A partially removed environment may retain the renderer-owned Secret.
		// Read metadata only: absence of workloads is not permission to replace
		// an existing CA or identity whose owner source can no longer be read.
		secretUID, err := command(nil, "-n", config.Namespace, "get", "secret", "certissuer-runtime", "--ignore-not-found=true", "-o", "jsonpath={.metadata.uid}")
		if err != nil {
			return nil, fmt.Errorf("inspect CertIssuer bootstrap Secret ownership: %w", err)
		}
		if len(bytes.TrimSpace(secretUID)) != 0 {
			return nil, errors.New("existing CertIssuer runtime Secret has no inspectable Deployment identity source; restore the existing owner before full static rendering")
		}
	}
	desiredCommand := func(input io.Reader, args ...string) ([]byte, error) {
		raw, err := command(input, args...)
		if err != nil || len(args) < 5 || args[0] != "-n" || args[1] != config.Namespace || args[2] != "get" || args[3] != "deployment" || args[4] != "certissuer" {
			return raw, err
		}
		var current certIssuerIngressObject
		if len(bytes.TrimSpace(raw)) != 0 {
			if err := json.Unmarshal(raw, &current); err != nil {
				return nil, err
			}
		}
		if (deployment == nil) != (current == nil) || (deployment != nil && certIssuerObjectMap(deployment["metadata"])["uid"] != certIssuerObjectMap(current["metadata"])["uid"]) {
			return nil, errors.New("CertIssuer Deployment identity source changed during full static planning; repeat the read-only check")
		}
		if err := certIssuerValidateStaticRenderOwner("Deployment", current, config, env); err != nil {
			return nil, err
		}
		if err := lkeValidateCertIssuerLegacyRenderer(current); err != nil {
			return nil, err
		}
		return raw, nil
	}
	if err := lkeRequireCertIssuerDesiredMaterialWithCommand(ctx, paths, env, desiredCommand); err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return plan, nil
}

func certIssuerValidateStaticRenderOwner(kind string, object certIssuerIngressObject, config lkeCertIssuerTLSConfig, env map[string]string) error {
	if object == nil {
		return nil
	}
	metadata := certIssuerObjectMap(object["metadata"])
	labels := certIssuerObjectMap(metadata["labels"])
	if certIssuerObjectString(metadata["name"]) != "certissuer" || certIssuerObjectString(metadata["namespace"]) != config.Namespace ||
		certIssuerObjectString(labels["app.kubernetes.io/name"]) != "certissuer" || certIssuerObjectString(labels["app.kubernetes.io/part-of"]) != "rtk-cloud" ||
		certIssuerObjectString(labels["rtk.realtek.com/provider"]) != "lke" || certIssuerObjectString(labels["rtk.realtek.com/stack"]) != env["CLOUD_STACK_NAME"] ||
		certIssuerObjectString(metadata["uid"]) == "" || certIssuerObjectString(metadata["resourceVersion"]) == "" {
		return fmt.Errorf("existing CertIssuer %s lacks exact stack ownership or API identity; refusing full static overwrite", kind)
	}
	if metadata["deletionTimestamp"] != nil || len(certIssuerObjectList(metadata["finalizers"])) != 0 {
		return fmt.Errorf("existing CertIssuer %s is deleting or has finalizers; review its owner before full static rendering", kind)
	}
	return nil
}
