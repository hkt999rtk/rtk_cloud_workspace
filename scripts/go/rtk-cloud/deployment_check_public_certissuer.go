package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"syscall"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

func deploymentCheckCertIssuerPolicy(cfg deploymentConfig) lkeCertIssuerTLSConfig {
	env := appendMap(cfg.Values, cfg.AdapterResolved)
	env = appendMap(env, deploymentLegacyLKEValues(env, cfg.Environment))
	env["CLOUD_ENV_NAME"] = cfg.Environment
	return lkeResolveCertIssuerTLSConfig(envroot.Derive(env))
}

func verifyDeploymentPublicCertIssuer(ctx context.Context, cfg deploymentConfig, store secretStore) error {
	policy := deploymentCheckCertIssuerPolicy(cfg)
	if policy.PublicHost == "" {
		return nil
	}
	if err := policy.Validate(); err != nil {
		return err
	}
	raw, err := secretCheckKubectl([]*deploymentCheckRuntime{store.checkRuntime}, false,
		"--kubeconfig", store.KubeconfigPath(), "get", "ingresses", "--all-namespaces", "-o", "json")
	if err != nil {
		return secretCheckFailure(err, "cannot inspect required public CertIssuer route")
	}
	var ingresses certIssuerIngressList
	if json.Unmarshal(raw, &ingresses) != nil || ingresses.Items == nil {
		return errors.New("required public CertIssuer route inventory is invalid")
	}
	var definitions []deploymentplan.Definition
	for _, kind := range []string{"deployment", "service"} {
		raw, readErr := secretCheckKubectl([]*deploymentCheckRuntime{store.checkRuntime}, false, "--kubeconfig", store.KubeconfigPath(), "-n", "video-cloud-"+cfg.Environment+"-video-cloud", "get", kind, "certissuer", "-o", "json")
		var object deploymentplan.Object
		if readErr != nil || json.Unmarshal(raw, &object) != nil {
			return errors.New("required public CertIssuer native listener graph is unavailable")
		}
		definitions = append(definitions, deploymentplan.Definition{Object: object})
	}
	port, err := environmentCertIssuerPublicPort(definitions, "video-cloud-"+cfg.Environment)
	if err != nil {
		return err
	}
	if err := validateRequiredCertIssuerPublicIngress("video-cloud-"+cfg.Environment, policy.PublicHost, port, ingresses); err != nil {
		return err
	}
	kubeconfig := store.KubeconfigPath()
	if store.checkRuntime != nil && store.checkRuntime.frozenBackend != nil {
		path, cleanup, err := store.checkRuntime.frozenBackend.privateKubeconfig()
		if err != nil {
			return errors.New("qualified frozen Kubernetes identity cannot be prepared for the public CertIssuer probe")
		}
		defer cleanup()
		kubeconfig = path
	}
	return runDeploymentCertIssuerPublicProbe(ctx, cfg.Workspace, cfg.Environment, store.ConfigRoot, kubeconfig, policy.PublicHost)
}

// Existing installations may still use the original nginx class before the
// reviewed update. The shared renderer targets only this stack's scoped class.
// Both retain every route and actual authenticated/anonymous TLS check below.
func deploymentSupportedIngressClass(stack, class string) bool {
	return class == "nginx" || class == environmentIngressClass(map[string]string{"CLOUD_STACK_NAME": stack})
}

func validateRequiredCertIssuerPublicIngress(stack, host string, port int, ingresses certIssuerIngressList) error {
	matches := 0
	for _, ingress := range ingresses.Items {
		for _, rule := range ingress.Spec.Rules {
			if rule.Host != host {
				continue
			}
			for _, path := range rule.HTTP.Paths {
				if !deploymentSupportedIngressClass(stack, ingress.Spec.IngressClassName) || ingress.Metadata.Namespace != stack+"-video-cloud" ||
					ingress.Metadata.Annotations["nginx.ingress.kubernetes.io/ssl-passthrough"] != "true" ||
					ingress.Metadata.Annotations["nginx.ingress.kubernetes.io/backend-protocol"] != "HTTPS" ||
					path.Backend.Service.Name != "certissuer" || path.Backend.Service.Port.Number != port || path.Path != "/" || path.PathType != "Prefix" {
					return errors.New("required public CertIssuer route has conflicting ownership, path, port or direct-mTLS policy")
				}
				matches++
			}
		}
	}
	if matches != 1 {
		return errors.New("required public CertIssuer route is missing or duplicated")
	}
	return nil
}

var deploymentCertIssuerProbeCode = regexp.MustCompile(`FAIL \[([A-Z_]+)\]`)

// The probe uses the AM Pod's original identity, including the managed owner
// when the app uses a private socket. It connects to the explicit public origin;
// keys stay in the Pod and both calls are incomplete, non-issuing requests.
func runDeploymentCertIssuerPublicProbe(ctx context.Context, workspace, environment, configRoot, kubeconfig, host string) error {
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", filepath.Join(workspace, "scripts", "check-certissuer-app-mtls.sh"), environment, "--public-host", host)
	cmd.Env = append(os.Environ(), "RTK_CLOUD_CONFIG_ROOT="+configRoot, "RTK_CLOUD_KUBECONFIG="+kubeconfig, "RTK_CERTISSUER_CHECK_TIMEOUT_SECONDS=20")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM) }
	cmd.WaitDelay = time.Second
	raw, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() != nil {
			return &deploymentRuntimeError{"CHECK_TIMEOUT", "public CertIssuer probe exceeded its deadline or was cancelled"}
		}
		code := "CERTISSUER_PUBLIC_PROBE_FAILED"
		if match := deploymentCertIssuerProbeCode.FindSubmatch(raw); len(match) == 2 {
			code = string(match[1])
		}
		return &deploymentRuntimeError{code, "public CertIssuer authenticated mTLS and anonymous-denial qualification failed"}
	}
	if !regexp.MustCompile(`(?m)^PASS: public CertIssuer mTLS validation and anonymous TLS denial verified$`).Match(raw) {
		return errors.New("public CertIssuer probe returned no complete qualification result")
	}
	return nil
}
