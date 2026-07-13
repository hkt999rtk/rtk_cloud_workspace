package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

func kubectlApply(manifest string) error {
	out, err := kubectlCombinedOutput(strings.NewReader(manifest), "apply", "-f", "-")
	if len(out) > 0 {
		fmt.Fprint(os.Stdout, string(out))
	}
	if err != nil {
		return fmt.Errorf("kubectl apply failed: %w: %s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

func kubectlCombinedOutput(stdin io.Reader, args ...string) ([]byte, error) {
	attempts := envIntDefault("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", 30)
	delay := envDurationDefault("RTK_CLOUD_KUBECTL_RETRY_DELAY", 5*time.Second)
	commandTimeout := envDurationDefault("RTK_CLOUD_KUBECTL_COMMAND_TIMEOUT", 20*time.Second)
	if attempts < 1 {
		attempts = 1
	}
	var lastOut []byte
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		ctx := context.Background()
		var cancel context.CancelFunc
		if commandTimeout > 0 {
			ctx, cancel = context.WithTimeout(ctx, commandTimeout)
		}
		cmd := exec.CommandContext(ctx, lkeKubectl(), lkeKubectlArgs(args...)...)
		if stdin != nil {
			if seeker, ok := stdin.(io.Seeker); ok {
				_, _ = seeker.Seek(0, io.SeekStart)
			}
			cmd.Stdin = stdin
		}
		out, err := cmd.CombinedOutput()
		if cancel != nil {
			cancel()
		}
		if commandTimeout > 0 && ctx.Err() == context.DeadlineExceeded {
			err = fmt.Errorf("kubectl command timed out after %s: %w", commandTimeout, ctx.Err())
		}
		lastOut, lastErr = out, err
		if err == nil {
			return out, nil
		}
		if !isTransientKubectlError(out, err) || attempt == attempts {
			break
		}
		fmt.Fprintf(os.Stderr, "[lke] kubectl transient failure, retrying attempt %d/%d: %s\n", attempt+1, attempts, kubectlErrorSummary(out, err))
		time.Sleep(delay)
	}
	return lastOut, lastErr
}

func waitForKubernetesAPIReady() error {
	timeout := envDurationDefault("RTK_CLOUD_KUBE_API_READY_TIMEOUT", 20*time.Minute)
	delay := envDurationDefault("RTK_CLOUD_KUBE_API_READY_POLL", 15*time.Second)
	stableChecks := envIntDefault("RTK_CLOUD_KUBE_API_READY_STABLE_CHECKS", 3)
	deadline := time.Now().Add(timeout)
	attempt := 0
	consecutiveReady := 0
	for {
		attempt++
		readyz, readyErr := kubectlCombinedOutput(nil, "--request-timeout=10s", "get", "--raw=/readyz")
		nodes, nodesErr := kubectlCombinedOutput(nil, "--request-timeout=10s", "get", "nodes", "-o", "name")
		if readyErr == nil && strings.TrimSpace(string(readyz)) == "ok" && nodesErr == nil && strings.TrimSpace(string(nodes)) != "" {
			consecutiveReady++
			if consecutiveReady >= stableChecks {
				fmt.Fprintf(os.Stderr, "[lke] Kubernetes API ready after %d attempt(s), stable_checks=%d\n", attempt, consecutiveReady)
				return nil
			}
			fmt.Fprintf(os.Stderr, "[lke] Kubernetes API readiness check %d/%d succeeded\n", consecutiveReady, stableChecks)
		} else {
			consecutiveReady = 0
			fmt.Fprintf(os.Stderr, "[lke] waiting for Kubernetes API readiness attempt=%d readyz=%s nodes=%s\n", attempt, kubectlErrorSummary(readyz, readyErr), kubectlErrorSummary(nodes, nodesErr))
		}
		if timeout == 0 || time.Now().Add(delay).After(deadline) {
			return fmt.Errorf("Kubernetes API did not become ready within %s: readyz=%s nodes=%s", timeout, kubectlErrorSummary(readyz, readyErr), kubectlErrorSummary(nodes, nodesErr))
		}
		time.Sleep(delay)
	}
}

func kubectlErrorSummary(out []byte, err error) string {
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			text = err.Error()
		} else {
			text = err.Error() + ": " + text
		}
	}
	text = strings.Join(strings.Fields(text), " ")
	if len(text) > 240 {
		text = text[:240] + "..."
	}
	if text == "" {
		return "ok"
	}
	return text
}

func isTransientKubectlError(out []byte, err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error() + "\n" + string(out))
	for _, token := range []string{
		"i/o timeout",
		"unable to connect to the server",
		"connection refused",
		"tls handshake timeout",
		"net/http: tls handshake timeout",
		"command timed out",
		"context deadline exceeded",
		"server has asked for the client to provide credentials",
	} {
		if strings.Contains(message, token) {
			return true
		}
	}
	return false
}

func kubectlDeleteSecret(namespace, name string) error {
	return runKubectl("-n", namespace, "delete", "secret/"+name, "--ignore-not-found=true")
}

func runKubectl(args ...string) error {
	out, err := kubectlCombinedOutput(os.Stdin, args...)
	if len(out) > 0 {
		_, _ = os.Stdout.Write(out)
	}
	return err
}

func runKubectlOutput(args ...string) (string, error) {
	out, err := kubectlCombinedOutput(os.Stdin, args...)
	if len(out) > 0 {
		_, _ = os.Stdout.Write(out)
	}
	return string(out), err
}

func lkeKubectl() string {
	return firstNonEmpty(os.Getenv("RTK_CLOUD_KUBECTL"), "kubectl")
}

func lkeKubectlArgs(args ...string) []string {
	prefix := []string{}
	if kubeconfig := firstNonEmpty(os.Getenv("RTK_CLOUD_KUBECONFIG"), os.Getenv("KUBECONFIG"), os.Getenv("RTK_CLOUD_LKE_KUBECONFIG")); kubeconfig != "" {
		prefix = append(prefix, "--kubeconfig", kubeconfig)
	}
	if !kubectlArgsHaveRequestTimeout(args) {
		prefix = append(prefix, "--request-timeout="+firstNonEmpty(os.Getenv("RTK_CLOUD_KUBECTL_REQUEST_TIMEOUT"), "10s"))
	}
	return append(prefix, args...)
}

func kubectlArgsHaveRequestTimeout(args []string) bool {
	for _, arg := range args {
		if arg == "--request-timeout" || strings.HasPrefix(arg, "--request-timeout=") {
			return true
		}
	}
	return false
}
