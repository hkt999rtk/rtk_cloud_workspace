package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

// Readiness is about the desired change. Current mirrors and PKI health are
// deliberately checked by post-deploy/acceptance, rather than this preflight.
func defaultDeploymentCheckPreflight(ctx context.Context, cfg deploymentConfig, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	clusterAbsent := false
	checks := defaultDeploymentPreflightChecksContext(ctx)
	checks.validateLKEState = func(cfg deploymentConfig) error {
		return validateLKEEnvironmentStateBeforeMutationWithDiscovery(cfg, func(token string, paths provisionPaths, env map[string]string, allowCreate bool) (lkeCluster, error) {
			cluster, err := discoverDeploymentPreflightLKECluster(ctx, token, paths, env, allowCreate)
			clusterAbsent = errors.Is(err, errLKEMissingCluster)
			return cluster, err
		})
	}
	if err := runDeploymentPreflightWithChecksContext(ctx, cfg, "provision", checks, out); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return deploymentCertIssuerIngressReadinessWithClusterState(ctx, cfg, out, clusterAbsent)
}

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
		store, plan, err := managedUpgradeStore(env)
		if err != nil {
			return err
		}
		if plan != nil {
			return qualifyManagedUpgrade(ctx, provisionPaths{Workspace: cfg.Workspace, EnvRoot: cfg.RuntimeRoot}, env, store, plan)
		}
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
	plan, err := loadManagedUpgradePlan(store, env["CLOUD_STACK_NAME"])
	if err != nil {
		return err
	}
	if plan != nil {
		if err := qualifyManagedUpgrade(ctx, paths, env, store, plan); err != nil {
			return fmt.Errorf("managed full upgrade prerequisites: %w", err)
		}
		fmt.Fprintf(out, "PASS managed-full-upgrade     complete desired workload overwrite and retained dependencies qualified; plan_sha256=%s; no cloud resources changed\n", plan.digest)
		return nil
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

func lkeCertIssuerRoutingSelected(ctx provisionContext) bool {
	return lkeResolveCertIssuerTLSConfig(ctx.Env).PublicHost != "" &&
		(ctx.Opts.mode.dns || (ctx.Opts.mode.deploy && lkeWorkloadSelected(ctx.Env, ctx.Opts, "video-cloud")))
}

func lkeCertIssuerIngressPreflight(ctx provisionContext) error {
	if ctx.Opts.mode.deploy && len(ctx.Opts.workloads) == 0 {
		store, plan, err := managedUpgradeStore(ctx.Env)
		if err != nil {
			return err
		}
		if plan != nil && managedUpgradeSelected(ctx.Opts) {
			if err := qualifyManagedUpgrade(context.Background(), ctx.Paths, ctx.Env, store, plan); err != nil {
				return err
			}
			ctx.Env["RTK_MANAGED_UPGRADE_PLAN_SHA256"] = plan.digest
			return nil
		}
		_, err = lkePlanCertIssuerIngressMigrationForStaticRenderWithContext(context.Background(), ctx.Paths, ctx.Env)
		return err
	}
	_, err := lkePlanCertIssuerIngressMigration(ctx.Paths, ctx.Env)
	return err
}

// A deploy-only update must also converge the CertIssuer route. DNS changes
// and unrelated public routes remain in the full public-https step.
func lkeDeployCertIssuerPublicIngress(ctx provisionContext) error {
	plan, err := lkePlanCertIssuerIngressMigration(ctx.Paths, ctx.Env)
	if err != nil {
		return err
	}
	if ctx.Env["RTK_MANAGED_UPGRADE_PLAN_SHA256"] != "" {
		if len(plan.Changes) != 0 {
			return errors.New("managed workload plan retains public routes; qualify route migration separately before the full workload upgrade")
		}
		return plan.Verify()
	}
	if err := lkeInstallIngressNginx(ctx.Env); err != nil {
		return err
	}
	if err := kubectlApply(lkeCertIssuerPublicNetworkPolicyManifest(ctx.Env)); err != nil {
		return err
	}
	return plan.Apply()
}

func lkeCertIssuerPublicNetworkPolicyManifest(env map[string]string) string {
	config := lkeResolveCertIssuerTLSConfig(env)
	return fmt.Sprintf(`apiVersion: networking.k8s.io/v1
kind: NetworkPolicy
metadata:
  name: allow-certissuer-public-mtls
  namespace: %s
  labels:
    app.kubernetes.io/part-of: rtk-cloud
    rtk.realtek.com/provider: lke
    rtk.realtek.com/stack: %s
spec:
  podSelector:
    matchLabels:
      app.kubernetes.io/name: certissuer
  policyTypes:
    - Ingress
  ingress:
    - from:
        - namespaceSelector:
            matchLabels:
              kubernetes.io/metadata.name: %s
          podSelector:
            matchLabels:
              app.kubernetes.io/name: ingress-nginx
      ports:
        - protocol: TCP
          port: %d
`, config.Namespace, env["CLOUD_STACK_NAME"], lkeIngressNamespace(env), config.HTTPSPort)
}
