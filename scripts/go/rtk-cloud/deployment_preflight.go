package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

type deploymentPreflightChecks struct {
	lookPath          func(string) (string, error)
	validateDNS       func(deploymentConfig) error
	validateLKEState  func(deploymentConfig) error
	validateEphemeral func(deploymentConfig) error
	validateKube      func(deploymentConfig) error
}

func defaultDeploymentPreflightChecks() deploymentPreflightChecks {
	return deploymentPreflightChecks{
		lookPath:          exec.LookPath,
		validateDNS:       validateDNSBeforeMutation,
		validateLKEState:  validateLKEEnvironmentStateBeforeMutation,
		validateEphemeral: validateEphemeralDeploymentEnvironmentAbsent,
		validateKube:      validateDeploymentKubeAccess,
	}
}

// These context-aware probes are used by deployment check's provision phase.
// Existing injected callbacks and lifecycle callers retain their API.
func defaultDeploymentPreflightChecksContext(ctx context.Context) deploymentPreflightChecks {
	checks := defaultDeploymentPreflightChecks()
	checks.validateDNS = func(cfg deploymentConfig) error {
		return validateDeploymentPreflightDNS(ctx, cfg)
	}
	checks.validateLKEState = func(cfg deploymentConfig) error {
		return validateLKEEnvironmentStateBeforeMutationWithDiscovery(cfg, func(token string, paths provisionPaths, env map[string]string, allowCreate bool) (lkeCluster, error) {
			return discoverDeploymentPreflightLKECluster(ctx, token, paths, env, allowCreate)
		})
	}
	checks.validateKube = func(cfg deploymentConfig) error {
		return validateDeploymentKubeAccessContext(ctx, cfg)
	}
	return checks
}

func validateDeploymentPreflightDNS(ctx context.Context, cfg deploymentConfig) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	env := appendMap(cfg.Values, cfg.DNSValues)
	adapter, err := newDNSAdapter(firstNonEmpty(env["DNS_ADAPTER"], "godaddy"))
	if err != nil {
		return err
	}
	adapterCtx := dnsContext(newProvisionPaths(cfg.Workspace, cfg.RuntimeRoot, provisionOptions{}), env)
	if err := adapter.Validate(ctx, adapterCtx); err != nil {
		return err
	}
	_, err = adapter.DiscoverZone(ctx, adapterCtx)
	return err
}

// Preflight only discovers the existing cluster. It never creates resources,
// writes kubeconfig or changes the operator's confirmed provider state.
func discoverDeploymentPreflightLKECluster(ctx context.Context, token string, _ provisionPaths, env map[string]string, allowCreate bool) (lkeCluster, error) {
	if allowCreate {
		return lkeCluster{}, errors.New("deployment preflight cannot create an LKE cluster")
	}
	label := lkeClusterLabel(env)
	if label == "" {
		return lkeCluster{}, errors.New("LKE_CLUSTER_LABEL or CLOUD_STACK_NAME is required to discover an LKE cluster")
	}
	cmd := exec.CommandContext(ctx, "curl", "--fail-with-body", "-sS", "-X", "GET", "https://api.linode.com/v4/lke/clusters?page_size=500", "-H", "Authorization: Bearer "+token, "-H", "Content-Type: application/json")
	cmd.WaitDelay = time.Second
	raw, err := cmd.Output()
	if ctx.Err() != nil {
		return lkeCluster{}, ctx.Err()
	}
	if err != nil {
		return lkeCluster{}, fmt.Errorf("LKE cluster inventory request failed: %w", err)
	}
	var inventory struct {
		Data []lkeCluster `json:"data"`
	}
	if err := json.Unmarshal(raw, &inventory); err != nil {
		return lkeCluster{}, errors.New("LKE cluster inventory response is invalid")
	}
	for _, cluster := range inventory.Data {
		if cluster.Label == label {
			return cluster, nil
		}
	}
	return lkeCluster{}, fmt.Errorf("%w: %s", errLKEMissingCluster, label)
}

type deploymentPreflightReporter struct {
	out    io.Writer
	failed []string
}

func (r *deploymentPreflightReporter) pass(name, detail string) {
	fmt.Fprintf(r.out, "PASS %-24s %s\n", name, detail)
}

func (r *deploymentPreflightReporter) warn(name, detail string) {
	fmt.Fprintf(r.out, "WARN %-24s %s\n", name, detail)
}

func (r *deploymentPreflightReporter) fail(name string, err error) {
	detail := strings.TrimSpace(err.Error())
	fmt.Fprintf(r.out, "FAIL %-24s %s\n", name, detail)
	r.failed = append(r.failed, name)
}

func runDeploymentPreflight(cfg deploymentConfig, operation string) error {
	return runDeploymentPreflightWithChecks(cfg, operation, defaultDeploymentPreflightChecks(), os.Stdout)
}

func runDeploymentPreflightWithChecks(cfg deploymentConfig, operation string, checks deploymentPreflightChecks, out io.Writer) error {
	return runDeploymentPreflightWithChecksContext(context.Background(), cfg, operation, checks, out)
}

func runDeploymentPreflightWithChecksContext(ctx context.Context, cfg deploymentConfig, operation string, checks deploymentPreflightChecks, out io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	allowed := map[string]bool{"plan": true, "provision": true, "acceptance": true, "ephemeral-test": true}
	if !allowed[operation] {
		return errors.New("--operation must be plan, provision, acceptance, or ephemeral-test")
	}

	reporter := &deploymentPreflightReporter{out: out}
	fmt.Fprintf(out, "Deployment preflight: environment=%s operation=%s runtime=%s\n", cfg.Environment, operation, cfg.RuntimeRoot)
	reporter.pass("environment-config", "tracked environment, architecture, adapter, and DNS config are valid")

	tools := []string{"git"}
	switch operation {
	case "provision", "ephemeral-test":
		tools = append(tools, lkeKubectl(), lkeHelm(), firstNonEmpty(os.Getenv("RTK_CLOUD_CERTBOT"), "certbot"), "curl", "ssh", "openssl")
	case "acceptance":
		tools = append(tools, lkeKubectl())
	}
	for _, tool := range uniqueNonEmpty(tools...) {
		if err := ctx.Err(); err != nil {
			return err
		}
		if _, err := checks.lookPath(tool); err != nil {
			reporter.fail("tool:"+tool, fmt.Errorf("required command is not available"))
			continue
		}
		reporter.pass("tool:"+filepath.Base(tool), "available")
	}

	if operation == "plan" {
		reporter.warn("credentials", "not required for a configuration-only preflight")
		return reporter.result()
	}
	if cfg.Adapter != "lke" {
		reporter.fail("deployment-adapter", fmt.Errorf("adapter %s does not support live mutation", cfg.Adapter))
		return reporter.result()
	}
	if !rtkCloudTestMode() {
		store, err := newSecretStore("", cfg.Environment)
		store.checkRuntime = newDeploymentCheckRuntime(ctx, cfg.Environment)
		if err != nil {
			reporter.fail("secret-store", err)
		} else if err := verifySecretStoreContents(store); err != nil {
			reporter.fail("secret-store", err)
		} else {
			reporter.pass("secret-store", "required canonical secret IDs are configured")
			if operation == "acceptance" {
				var liveFailures []string
				if err := verifySecretStoreK8SBindings(store); err != nil {
					liveFailures = append(liveFailures, err.Error())
				}
				if err := verifySecretStoreK8SRuntime(store, time.Now()); err != nil {
					liveFailures = append(liveFailures, err.Error())
				}
				if len(liveFailures) > 0 {
					reporter.fail("live-secrets", errors.New(strings.Join(liveFailures, "; ")))
				} else {
					reporter.pass("live-secrets", "canonical mirrors, certificate key pairs, validity, and PKI-backed workload identities are healthy")
				}
			}
		}
	}

	if operation == "acceptance" {
		validateAcceptanceRuntime(cfg, reporter)
		if err := checks.validateKube(cfg); err != nil {
			reporter.fail("kubernetes-access", err)
		} else {
			reporter.pass("kubernetes-access", "API readyz is reachable with the environment kubeconfig")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		return reporter.result()
	}

	if resolveLinodeToken(cfg.RuntimeRoot) == "" {
		reporter.fail("credential:linode", errors.New("LINODE_TOKEN is not configured in ~/.config/rtk_cloud/<environment>/operator/env"))
	} else {
		reporter.pass("credential:linode", "configured (value redacted)")
	}
	credentialEnv := make(map[string]string, len(cfg.Values))
	for key, value := range cfg.Values {
		credentialEnv[key] = value
	}
	if operator, check := deploymentCredentialValues(defaultDeploymentEnvironmentCredentialFile(cfg.Environment)); check.Passed {
		for key, value := range operator {
			credentialEnv[key] = value
		}
	} else {
		reporter.fail("credential:ghcr", errors.New("canonical environment credential directory could not be read"))
	}
	username, token := lkeGHCRPullCredentials(credentialEnv)
	if username == "" || token == "" {
		reporter.fail("credential:ghcr", errors.New("GHCR_PULL_USERNAME and GHCR_PULL_TOKEN are required"))
	} else {
		reporter.pass("credential:ghcr", "configured (values redacted)")
	}
	if err := checks.validateDNS(cfg); err != nil {
		reporter.fail("credential:dns", err)
	} else {
		reporter.pass("credential:dns", cfg.DNSAdapter+" credentials are configured (values redacted)")
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	privateKey := defaultStagingSSHKey()
	for _, path := range []string{privateKey, privateKey + ".pub"} {
		if info, err := os.Stat(path); err != nil || info.Size() == 0 {
			reporter.fail("ssh-key", fmt.Errorf("required key file is missing or empty: %s", path))
		} else {
			reporter.pass("ssh-key", filepath.Base(path)+" is available")
		}
	}

	account, err := readLKEAccountState(cfg.RuntimeRoot, true)
	if err != nil {
		reporter.fail("active-service-limit", err)
	} else if err := validateActiveServiceLimit(account["LKE_ACTIVE_SERVICE_LIMIT"]); err != nil {
		reporter.fail("active-service-limit", err)
	} else {
		reporter.pass("active-service-limit", "confirmed operator state is available")
	}

	if err := checks.validateLKEState(cfg); err != nil {
		reporter.fail("environment-safety", err)
	} else {
		reporter.pass("environment-safety", "provider state and existing-cluster runtime state are coherent")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if operation == "ephemeral-test" {
		if err := checks.validateEphemeral(cfg); err != nil {
			reporter.fail("ephemeral-ownership", err)
		} else {
			reporter.pass("ephemeral-ownership", "stack has no pre-existing owned resources or DNS ownership state")
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return reporter.result()
}

func (r *deploymentPreflightReporter) result() error {
	if len(r.failed) == 0 {
		fmt.Fprintln(r.out, "Preflight result: PASS")
		return nil
	}
	fmt.Fprintf(r.out, "Preflight result: FAIL (%s)\n", strings.Join(r.failed, ", "))
	return fmt.Errorf("deployment preflight failed: %s", strings.Join(r.failed, ", "))
}

func validateAcceptanceRuntime(cfg deploymentConfig, reporter *deploymentPreflightReporter) {
	required := []string{
		filepath.Join(cfg.RuntimeRoot, "state", "provider-preflight.env"),
		filepath.Join(cfg.RuntimeRoot, "env", "stack.env"),
	}
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		reporter.fail("secret-store", err)
		return
	}
	required = append(required,
		store.KubeconfigPath(),
		filepath.Join(store.Root, "openbao", "unseal-key"),
		filepath.Join(store.Root, "openbao", "root-token"),
		filepath.Join(store.Root, "runtime", "postgres"),
	)
	for _, path := range required {
		if info, err := os.Stat(path); err != nil || info.Size() == 0 {
			reporter.fail("runtime-state", fmt.Errorf("required matching runtime file is missing or empty: %s", path))
			continue
		}
		display := strings.TrimPrefix(path, cfg.RuntimeRoot+string(os.PathSeparator))
		if strings.HasPrefix(path, store.Root+string(os.PathSeparator)) {
			display = "SecretStore/" + strings.TrimPrefix(path, store.Root+string(os.PathSeparator))
		}
		reporter.pass("runtime-state", display+" is available")
	}
	stackPath := filepath.Join(cfg.RuntimeRoot, "env", "stack.env")
	if got := envFileValue(stackPath, "CLOUD_STACK_NAME"); got != "" && got != cfg.Values["CLOUD_STACK_NAME"] {
		reporter.fail("runtime-identity", fmt.Errorf("runtime CLOUD_STACK_NAME does not match tracked environment identity"))
	} else if got != "" {
		reporter.pass("runtime-identity", "runtime stack matches tracked environment identity")
	}
}

func validateDeploymentKubeAccess(cfg deploymentConfig) error {
	return validateDeploymentKubeAccessContext(context.Background(), cfg)
}

func validateDeploymentKubeAccessContext(ctx context.Context, cfg deploymentConfig) error {
	store, err := newSecretStore("", cfg.Environment)
	if err != nil {
		return err
	}
	kubeconfig := store.KubeconfigPath()
	cmd := exec.CommandContext(ctx, lkeKubectl(), "--kubeconfig", kubeconfig, "--request-timeout=10s", "get", "--raw=/readyz")
	cmd.WaitDelay = time.Second
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err != nil {
		return fmt.Errorf("Kubernetes API readyz check failed: %s", strings.TrimSpace(string(out)))
	}
	if strings.TrimSpace(string(out)) != "ok" {
		return fmt.Errorf("Kubernetes API readyz returned an unexpected response")
	}
	return nil
}
