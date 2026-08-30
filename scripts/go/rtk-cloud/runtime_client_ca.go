package main

import (
	"crypto/x509"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func runRefreshRuntimeClientCA(args []string) error {
	fs := flag.NewFlagSet("refresh-runtime-client-ca", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	if err := fs.Parse(args); err != nil {
		return err
	}
	workspace := strings.TrimSpace(*workspaceFlag)
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	if strings.TrimSpace(*envRootFlag) == "" {
		return errors.New("--env-root is required")
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	path, err := refreshRuntimeDeviceClientCABundle(workspace, envRoot)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(map[string]any{"status": "PASS", "path": path})
}

func refreshRuntimeDeviceClientCABundle(workspace, envRoot string) (string, error) {
	stackEnv, err := readEnvFile(filepath.Join(envRoot, "env", "stack.env"))
	if err != nil {
		return "", fmt.Errorf("read runtime stack environment: %w", err)
	}
	if firstNonEmpty(os.Getenv("CLOUD_PROVIDER"), stackEnv["CLOUD_PROVIDER"], "lke") != "lke" {
		return "", errors.New("runtime client CA refresh requires CLOUD_PROVIDER=lke")
	}
	stack := firstNonEmpty(stackEnv["CLOUD_STACK_NAME"], "video-cloud-staging")
	if err := validateRuntimeClientCAStack(stack, stackEnv); err != nil {
		return "", err
	}
	kubeconfig, err := lkeRuntimeKubeconfig(workspace, envRoot, stack)
	if err != nil {
		return "", err
	}
	cmd := exec.Command(
		lkeKubectl(), "--kubeconfig", kubeconfig, "--request-timeout=10s",
		"-n", stack+"-video-cloud", "get", "secret", "certissuer-runtime", "-o", "json",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("read live certissuer CA material: %w: %s", err, truncateForLog(string(out), 300))
	}
	var secret struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal(out, &secret); err != nil {
		return "", fmt.Errorf("decode live certissuer secret: %w", err)
	}
	rootCA, err := decodeSecretPEM(secret.Data, "root-ca.crt")
	if err != nil {
		return "", err
	}
	deviceCA, err := decodeSecretPEM(secret.Data, "device-ca.crt")
	if err != nil {
		return "", err
	}
	appCA, err := decodeSecretPEM(secret.Data, "app-ca.crt")
	if err != nil {
		return "", err
	}
	if err := validateRuntimeClientCAChain(rootCA, deviceCA, appCA); err != nil {
		return "", err
	}
	if err := writeLKEDeviceClientCABundle(provisionPaths{EnvRoot: envRoot}, rootCA, deviceCA, appCA); err != nil {
		return "", err
	}
	return filepath.Join(envRoot, "state", "pki", "device-client-ca-bundle.pem"), nil
}

func validateRuntimeClientCAStack(stack string, stackEnv map[string]string) error {
	if stack == "video-cloud-staging" {
		return nil
	}
	runtimeStack := strings.TrimSpace(stackEnv["CLOUD_RUNTIME_COVERAGE_STACK"])
	if strings.TrimSpace(stackEnv["CLOUD_ENV_NAME"]) == "runtime-coverage" &&
		strings.HasPrefix(stack, "coverage-") &&
		runtimeStack == stack {
		return nil
	}
	return fmt.Errorf("runtime client CA refresh requires staging or the exact run-scoped coverage stack, got %s", stack)
}

func validateRuntimeClientCAChain(rootPEM, devicePEM, appPEM string) error {
	root, err := parseRuntimeClientCA(rootPEM)
	if err != nil {
		return fmt.Errorf("parse live root CA: %w", err)
	}
	device, err := parseRuntimeClientCA(devicePEM)
	if err != nil {
		return fmt.Errorf("parse live device CA: %w", err)
	}
	app, err := parseRuntimeClientCA(appPEM)
	if err != nil {
		return fmt.Errorf("parse live app CA: %w", err)
	}
	for name, cert := range map[string]*x509.Certificate{"root": root, "device": device, "app": app} {
		if !cert.IsCA || cert.KeyUsage&x509.KeyUsageCertSign == 0 {
			return fmt.Errorf("live %s certificate is not a signing CA", name)
		}
	}
	if err := root.CheckSignatureFrom(root); err != nil {
		return fmt.Errorf("live root CA is not self-signed: %w", err)
	}
	if err := device.CheckSignatureFrom(root); err != nil {
		return fmt.Errorf("live device CA is not signed by the live root: %w", err)
	}
	if err := app.CheckSignatureFrom(root); err != nil {
		return fmt.Errorf("live app CA is not signed by the live root: %w", err)
	}
	return nil
}

func parseRuntimeClientCA(raw string) (*x509.Certificate, error) {
	cert, err := parseFirstCertificatePEM([]byte(raw))
	if err != nil {
		return nil, err
	}
	if cert == nil {
		return nil, errors.New("PEM certificate is missing")
	}
	return cert, nil
}
