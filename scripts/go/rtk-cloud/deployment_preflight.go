package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
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

	if operation == "acceptance" {
		validateAcceptanceRuntime(cfg, reporter)
		if err := checks.validateKube(cfg); err != nil {
			reporter.fail("kubernetes-access", err)
		} else {
			reporter.pass("kubernetes-access", "API readyz is reachable with the environment kubeconfig")
		}
		return reporter.result()
	}

	if resolveLinodeToken(cfg.RuntimeRoot) == "" {
		reporter.fail("credential:linode", errors.New("LINODE_TOKEN is not configured in process env, runtime operator.env, or ~/.env"))
	} else {
		reporter.pass("credential:linode", "configured (value redacted)")
	}
	credentialEnv := make(map[string]string, len(cfg.Values))
	for key, value := range cfg.Values {
		credentialEnv[key] = value
	}
	if operator, err := readEnvFile(filepath.Join(cfg.RuntimeRoot, "env", "operator.env")); err == nil {
		for key, value := range operator {
			if credentialEnv[key] == "" {
				credentialEnv[key] = value
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		reporter.fail("credential:ghcr", errors.New("runtime operator.env could not be read"))
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
	} else if _, err := positiveIntValue("LKE_ACTIVE_SERVICE_LIMIT", account["LKE_ACTIVE_SERVICE_LIMIT"]); err != nil {
		reporter.fail("active-service-limit", err)
	} else {
		reporter.pass("active-service-limit", "confirmed operator state is available")
	}

	if err := checks.validateLKEState(cfg); err != nil {
		reporter.fail("environment-safety", err)
	} else {
		reporter.pass("environment-safety", "provider state and existing-cluster runtime state are coherent")
	}
	if operation == "ephemeral-test" {
		if err := checks.validateEphemeral(cfg); err != nil {
			reporter.fail("ephemeral-ownership", err)
		} else {
			reporter.pass("ephemeral-ownership", "stack has no pre-existing owned resources or DNS ownership state")
		}
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
		filepath.Join(cfg.RuntimeRoot, "state", "kubeconfig.yaml"),
		filepath.Join(cfg.RuntimeRoot, "env", "stack.env"),
		filepath.Join(cfg.RuntimeRoot, "state", "openbao", "unseal-key"),
		filepath.Join(cfg.RuntimeRoot, "state", "openbao", "root-token"),
		filepath.Join(cfg.RuntimeRoot, "state", "secrets", "postgres"),
	}
	for _, path := range required {
		if info, err := os.Stat(path); err != nil || info.Size() == 0 {
			reporter.fail("runtime-state", fmt.Errorf("required matching runtime file is missing or empty: %s", path))
			continue
		}
		reporter.pass("runtime-state", strings.TrimPrefix(path, cfg.RuntimeRoot+string(os.PathSeparator))+" is available")
	}
	stackPath := filepath.Join(cfg.RuntimeRoot, "env", "stack.env")
	if got := envFileValue(stackPath, "CLOUD_STACK_NAME"); got != "" && got != cfg.Values["CLOUD_STACK_NAME"] {
		reporter.fail("runtime-identity", fmt.Errorf("runtime CLOUD_STACK_NAME does not match tracked environment identity"))
	} else if got != "" {
		reporter.pass("runtime-identity", "runtime stack matches tracked environment identity")
	}
}

func validateDeploymentKubeAccess(cfg deploymentConfig) error {
	kubeconfig := filepath.Join(cfg.RuntimeRoot, "state", "kubeconfig.yaml")
	cmd := exec.Command(lkeKubectl(), "--kubeconfig", kubeconfig, "--request-timeout=10s", "get", "--raw=/readyz")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("Kubernetes API readyz check failed: %s", strings.TrimSpace(string(out)))
	}
	if strings.TrimSpace(string(out)) != "ok" {
		return fmt.Errorf("Kubernetes API readyz returned an unexpected response")
	}
	return nil
}
