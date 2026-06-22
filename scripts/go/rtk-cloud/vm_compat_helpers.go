package main

import (
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

func runRemoveAllVM(args []string) error {
	fs := flag.NewFlagSet("remove-all-vm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	_ = fs.String("workspace", "", "workspace")
	_ = fs.String("env-root", "", "environment root")
	_ = fs.Bool("yes", false, "confirm")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return errors.New("remove-all-vm is retired for VM staging; use remove-k8s for current staging")
}

func backupAndRemoveLKEState(envRoot string) error {
	stateDir := filepath.Join(envRoot, "state")
	backupDir := filepath.Join(envRoot, "backups", "remove-k8s-"+time.Now().UTC().Format("20060102T150405Z"), "state")
	files := []string{"video-cloud-staging.state.json", "account-manager-staging.env", "cloud-admin-staging.env", "cloud-logger.env"}
	if stackName := envFileValue(filepath.Join(envRoot, "env", "stack.env"), "CLOUD_STACK_NAME"); stackName != "" {
		files = append(files, stackName+".state.json")
	}
	for _, name := range files {
		src := filepath.Join(stateDir, name)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		if err := os.MkdirAll(backupDir, 0o755); err != nil {
			return err
		}
		dst := filepath.Join(backupDir, name)
		if err := copyFile(src, dst); err != nil {
			return err
		}
		if err := os.Remove(src); err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "[cloud-remove-k8s] removed local state: %s\n", src)
	}
	if _, err := os.Stat(backupDir); err == nil {
		fmt.Fprintf(os.Stderr, "[cloud-remove-k8s] local state backup: %s\n", filepath.Dir(backupDir))
	}
	return nil
}

func lkeAccountManagerPortForward(envRoot string, env map[string]string) (string, func(), error) {
	return lkeServicePortForward(envRoot, env, "account-manager", "account-manager", 80, "account-manager")
}

func lkeFactoryEnrollPortForward(envRoot string, env map[string]string) (string, func(), error) {
	return lkeServicePortForward(envRoot, env, "video-cloud", "factoryenroll", 80, "factoryenroll")
}

func lkeVideoCloudAPIPortForward(envRoot string, env map[string]string) (string, func(), error) {
	return lkeServicePortForward(envRoot, env, "video-cloud", "video-cloud-api", 80, "video-cloud-api")
}

func lkeServicePortForward(envRoot string, env map[string]string, namespaceKey, service string, remotePort int, label string) (string, func(), error) {
	port, cleanup, err := lkeTCPServicePortForward(envRoot, env, namespaceKey, service, remotePort, label)
	if err != nil {
		return "", cleanup, err
	}
	return fmt.Sprintf("http://127.0.0.1:%d", port), cleanup, nil
}

func lkeTCPServicePortForward(envRoot string, env map[string]string, namespaceKey, service string, remotePort int, label string) (int, func(), error) {
	if err := ensureLKEKubeAccess(provisionPaths{EnvRoot: envRoot}, env, false); err != nil {
		return 0, func() {}, err
	}
	port, err := freeLocalPort()
	if err != nil {
		return 0, func() {}, err
	}
	args := lkeKubectlArgs("-n", lkeNamespaceName(env, namespaceKey), "port-forward", "svc/"+service, fmt.Sprintf("%d:%d", port, remotePort))
	cmd := exec.Command(lkeKubectl(), args...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return 0, func() {}, err
	}
	cleanup := func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_, _ = cmd.Process.Wait()
		}
	}
	waitTimeout := envDurationDefault("RTK_CLOUD_LKE_PORT_FORWARD_WAIT", 10*time.Second)
	if waitTimeout > 0 {
		if err := waitForLocalTCPPort(port, waitTimeout); err != nil {
			cleanup()
			errText := strings.TrimSpace(stderr.String())
			if errText != "" {
				return 0, func() {}, fmt.Errorf("%s port-forward failed: %w: %s", label, err, errText)
			}
			return 0, func() {}, err
		}
	}
	return port, cleanup, nil
}

func freeLocalPort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer ln.Close()
	return ln.Addr().(*net.TCPAddr).Port, nil
}

func waitForLocalTCPPort(port int, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	var lastErr error
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()
			return nil
		}
		lastErr = err
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for local port-forward %s: %w", addr, lastErr)
}
