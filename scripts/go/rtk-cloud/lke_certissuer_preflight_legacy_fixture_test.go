package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

// Historical readiness samples for regression tests, excluded from the CLI.
func deploymentCertIssuerIngressReadiness(ctx context.Context, cfg deploymentConfig, out io.Writer) error {
	return deploymentCertIssuerIngressReadinessWithClusterState(ctx, cfg, out, false)
}

func deploymentCertIssuerIngressReadinessWithClusterState(ctx context.Context, cfg deploymentConfig, out io.Writer, clusterAbsent bool) error {
	if cfg.Adapter != "lke" {
		return nil
	}
	env := appendMap(cfg.Values, cfg.AdapterResolved)
	env = appendMap(env, deploymentLegacyLKEValues(env, cfg.Environment))
	env["CLOUD_ENV_NAME"] = cfg.Environment
	env = envroot.Derive(env)
	policy := lkeResolveCertIssuerTLSConfig(env)
	if err := policy.Validate(); err != nil {
		return err
	}
	if policy.PublicHost == "" {

		return nil
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	previousSecretRoot := activeSecretEnvironmentRoot
	activeSecretEnvironmentRoot = store.Root
	defer func() { activeSecretEnvironmentRoot = previousSecretRoot }()
	paths := provisionPaths{Workspace: cfg.Workspace, EnvRoot: cfg.RuntimeRoot}
	if clusterAbsent {
		if err := lkeRequireCertIssuerInitialMaterialWithContext(ctx, paths, env); err != nil {
			return err
		}
		fmt.Fprintln(out, "PASS certissuer-route-plan     provider confirmed selected cluster absent; new deployment will create the resolved direct-mTLS route")
		return nil
	}
	if _, err := os.Stat(store.KubeconfigPath()); errors.Is(err, os.ErrNotExist) {
		return errors.New("selected cluster is not confirmed absent and its kubeconfig is missing; restore read-only Kubernetes access before qualifying CertIssuer migration")
	} else if err != nil {
		return fmt.Errorf("inspect selected environment kubeconfig: %w", err)
	}
	operator, err := store.readOperator()
	if err != nil {
		return err
	}
	operator["RTK_CLOUD_KUBECONFIG"] = store.KubeconfigPath()
	operator["RTK_CLOUD_LKE_KUBECONFIG"] = store.KubeconfigPath()
	restore := installAllCredentialEnvironment(operator)
	defer restore()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := lkePlanCertIssuerIngressMigrationForStaticRenderWithContext(ctx, paths, env); err != nil {
		return fmt.Errorf("CertIssuer route migration prerequisites: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fmt.Fprintln(out, "PASS certissuer-route-plan     resolved direct mTLS and owned legacy route migration are deployable; no live resources changed")
	return nil
}
