package main

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

func kubectlApply(manifest string) error {
	cmd := exec.Command(lkeKubectl(), lkeKubectlArgs("apply", "-f", "-")...)
	cmd.Stdin = strings.NewReader(manifest)
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		fmt.Fprint(os.Stdout, string(out))
	}
	if err != nil {
		return fmt.Errorf("kubectl apply failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func kubectlDeleteSecret(namespace, name string) error {
	return runKubectl("-n", namespace, "delete", "secret/"+name, "--ignore-not-found=true")
}

func runKubectl(args ...string) error {
	cmd := exec.Command(lkeKubectl(), lkeKubectlArgs(args...)...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Stdin = os.Stdin
	return cmd.Run()
}

func runKubectlOutput(args ...string) (string, error) {
	cmd := exec.Command(lkeKubectl(), lkeKubectlArgs(args...)...)
	cmd.Stdin = os.Stdin
	out, err := cmd.CombinedOutput()
	if len(out) > 0 {
		_, _ = os.Stdout.Write(out)
	}
	return string(out), err
}

func lkeKubectl() string {
	return firstNonEmpty(os.Getenv("RTK_CLOUD_KUBECTL"), "kubectl")
}

func lkeKubectlArgs(args ...string) []string {
	if kubeconfig := os.Getenv("RTK_CLOUD_LKE_KUBECONFIG"); kubeconfig != "" {
		return append([]string{"--kubeconfig", kubeconfig}, args...)
	}
	return args
}
