package main

import (
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

// runCertIssuerOpenBaoSync repairs only the certissuer trust link after an
// existing OpenBao TLS CA rotation. AppRole IDs are preserved and no other
// workload or secret is reconciled.
func runCertIssuerOpenBaoSync(args []string) error {
	fs := flag.NewFlagSet("certissuer-openbao-sync", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace root")
	envRootFlag := fs.String("env-root", "", "normalized environment root")
	confirm := fs.String("confirm", "", "exact stack name confirmation")
	kubeconfig := fs.String("kubeconfig", "", "kubeconfig for the existing staging cluster")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if strings.TrimSpace(*envRootFlag) == "" {
		return errors.New("--env-root is required")
	}
	workspace := strings.TrimSpace(*workspaceFlag)
	if workspace == "" {
		var err error
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	root, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	loaded, err := envroot.Load(root, "")
	if err != nil {
		return err
	}
	env := loaded.Values
	stack := strings.TrimSpace(env["CLOUD_STACK_NAME"])
	if stack == "" || strings.TrimSpace(*confirm) != stack {
		return fmt.Errorf("--confirm must equal CLOUD_STACK_NAME %q", stack)
	}
	if strings.TrimSpace(*kubeconfig) != "" {
		if err := os.Setenv("RTK_CLOUD_KUBECONFIG", strings.TrimSpace(*kubeconfig)); err != nil {
			return err
		}
	}
	if err := waitForKubernetesAPIReady(); err != nil {
		return err
	}

	openBaoTLS, err := kubectlResourceJSON(lkeNamespaceName(env, "secrets"), "secret", "openbao-tls")
	if err != nil {
		return err
	}
	videoNamespace := lkeNamespaceName(env, "video-cloud")
	auth, err := kubectlResourceJSON(videoNamespace, "secret", "certissuer-openbao-auth")
	if err != nil {
		return err
	}
	deployment, err := kubectlResourceJSON(videoNamespace, "deployment", "certissuer")
	if err != nil {
		return err
	}
	changed, err := reconcileCertIssuerOpenBaoCA(openBaoTLS, auth, deployment)
	if err != nil {
		return err
	}
	if !changed {
		fmt.Fprintln(os.Stdout, "certissuer_openbao_ca=unchanged")
		return nil
	}
	if err := kubectlReplaceJSON(auth); err != nil {
		return fmt.Errorf("replace certissuer OpenBao auth Secret: %w", err)
	}
	if err := kubectlReplaceJSON(deployment); err != nil {
		return fmt.Errorf("replace certissuer Deployment: %w", err)
	}
	if err := runKubectl(
		"-n", videoNamespace, "rollout", "status", "deployment/certissuer",
		"--timeout", firstNonEmpty(os.Getenv("LKE_CERTISSUER_ROLLOUT_TIMEOUT"), "5m"),
	); err != nil {
		return err
	}
	fmt.Fprintln(os.Stdout, "certissuer_openbao_ca=synchronized")
	return nil
}

func reconcileCertIssuerOpenBaoCA(openBaoTLS, auth, deployment map[string]any) (bool, error) {
	openBaoData, ok := openBaoTLS["data"].(map[string]any)
	if !ok {
		return false, errors.New("openbao-tls Secret has no data")
	}
	authData, ok := auth["data"].(map[string]any)
	if !ok {
		return false, errors.New("certissuer-openbao-auth Secret has no data")
	}
	encodedCA, _ := openBaoData["ca.crt"].(string)
	caPEM, err := base64.StdEncoding.DecodeString(encodedCA)
	if err != nil || len(caPEM) == 0 {
		return false, errors.New("openbao-tls ca.crt is invalid")
	}
	block, rest := pem.Decode(caPEM)
	if block == nil || block.Type != "CERTIFICATE" || strings.TrimSpace(string(rest)) != "" {
		return false, errors.New("openbao-tls ca.crt must contain one certificate")
	}
	if _, err := x509.ParseCertificate(block.Bytes); err != nil {
		return false, errors.New("openbao-tls ca.crt cannot be parsed")
	}
	for _, key := range []string{"role_id", "secret_id"} {
		value, _ := authData[key].(string)
		if strings.TrimSpace(value) == "" {
			return false, fmt.Errorf("certissuer-openbao-auth is missing %s", key)
		}
	}
	if current, _ := authData["ca.crt"].(string); current == encodedCA {
		return false, nil
	}
	authData["ca.crt"] = encodedCA
	sum := sha256.Sum256(caPEM)
	if err := setDeploymentTemplateAnnotation(
		deployment,
		"rtk.realtek.com/openbao-ca-checksum",
		hex.EncodeToString(sum[:]),
	); err != nil {
		return false, err
	}
	return true, nil
}

func setDeploymentTemplateAnnotation(deployment map[string]any, key, value string) error {
	spec, ok := deployment["spec"].(map[string]any)
	if !ok {
		return errors.New("deployment has no spec")
	}
	template, ok := spec["template"].(map[string]any)
	if !ok {
		return errors.New("deployment has no pod template")
	}
	metadata, _ := template["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
		template["metadata"] = metadata
	}
	annotations, _ := metadata["annotations"].(map[string]any)
	if annotations == nil {
		annotations = map[string]any{}
		metadata["annotations"] = annotations
	}
	annotations[key] = value
	return nil
}
