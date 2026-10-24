package main

import (
	"context"
	"errors"
	"fmt"
	"io"
)

// Readiness is about the desired change. Current mirrors and PKI health are
// deliberately checked by post-deploy/acceptance, rather than this preflight.
func defaultDeploymentCheckPreflight(ctx context.Context, cfg deploymentConfig, out io.Writer) error {
	return runDeploymentPreflightWithChecksContext(ctx, cfg, "provision", defaultDeploymentPreflightChecksContext(ctx), out)
}

func lkeCertIssuerRoutingSelected(ctx provisionContext) bool {
	return lkeResolveCertIssuerTLSConfig(ctx.Env).PublicHost != "" &&
		(ctx.Opts.mode.dns || (ctx.Opts.mode.deploy && lkeWorkloadSelected(ctx.Env, ctx.Opts, "video-cloud")))
}

func lkeCertIssuerIngressPreflight(ctx provisionContext) error {
	if ctx.Opts.mode.deploy && lkeWorkloadSelected(ctx.Env, ctx.Opts, "video-cloud") {
		return errors.New("legacy static PKI templates are retired; use deployment upgrade with the exact --plan-sha256")
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
