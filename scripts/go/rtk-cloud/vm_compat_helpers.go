package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/envroot"
)

func runRemoveAllVM(args []string) error {
	fs := flag.NewFlagSet("remove-all-vm", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	workspaceFlag := fs.String("workspace", "", "workspace")
	envRootFlag := fs.String("env-root", "", "environment root")
	confirm := fs.Bool("yes", false, "confirm")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *envRootFlag == "" {
		return errors.New("--env-root is required")
	}
	workspace := *workspaceFlag
	var err error
	if workspace == "" {
		workspace, err = workspaceRoot()
		if err != nil {
			return err
		}
	}
	envRoot, err := resolveEnvRoot(workspace, *envRootFlag)
	if err != nil {
		return err
	}
	stackValues, _ := readEnvFile(filepath.Join(envRoot, "env", "stack.env"))
	stackValues["CLOUD_PROVIDER"] = firstNonEmpty(os.Getenv("CLOUD_PROVIDER"), os.Getenv("RTK_CLOUD_STAGING_PROVIDER"), stackValues["CLOUD_PROVIDER"], "linode")
	if stackValues["CLOUD_PROVIDER"] == "lke" {
		envValues := envroot.Derive(stackValues)
		envValues["CLOUD_PROVIDER"] = "lke"
		return runRemoveAllLKE(envRoot, envValues, *confirm)
	}
	return errors.New("remove-all-vm is retired for VM staging; use remove-k8s for current staging")
}

type firewallRules struct {
	InboundPolicy  string         `json:"inbound_policy,omitempty"`
	OutboundPolicy string         `json:"outbound_policy,omitempty"`
	Inbound        []firewallRule `json:"inbound"`
	Outbound       []firewallRule `json:"outbound"`
	Version        any            `json:"version,omitempty"`
	Fingerprint    any            `json:"fingerprint,omitempty"`
}

type firewallRule struct {
	Label     string              `json:"label,omitempty"`
	Action    string              `json:"action,omitempty"`
	Protocol  string              `json:"protocol,omitempty"`
	Ports     string              `json:"ports,omitempty"`
	Addresses map[string][]string `json:"addresses,omitempty"`
}

type firewallTarget struct {
	Role  string
	Label string
	ID    string
}

func backupAndRemoveState(envRoot string) error {
	stateDir := filepath.Join(envRoot, "state")
	backupDir := filepath.Join(envRoot, "backups", "remove-vm-"+time.Now().UTC().Format("20060102T150405Z"), "state")
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
		fmt.Fprintf(os.Stderr, "[cloud-remove-all-vm] removed local state: %s\n", src)
	}
	if _, err := os.Stat(backupDir); err == nil {
		fmt.Fprintf(os.Stderr, "[cloud-remove-all-vm] local state backup: %s\n", filepath.Dir(backupDir))
	}
	return nil
}

func currentPublicIPv4CIDR() (string, error) {
	cmd := exec.Command("curl", "-4", "-fsS", "--connect-timeout", "5", "--max-time", "10", "https://api.ipify.org")
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("detect current public IPv4 failed: %w", err)
	}
	ip := strings.TrimSpace(string(out))
	if ok, _ := regexp.MatchString(`^([0-9]{1,3}\.){3}[0-9]{1,3}$`, ip); !ok {
		return "", fmt.Errorf("detect current public IPv4 returned invalid value: %q", ip)
	}
	return ip + "/32", nil
}

func updateLocalSSHWhitelistInputs(envRoot, cidr string, appendMode bool) {
	videoConfig := filepath.Join(envRoot, "topology", "video-cloud-staging.yaml")
	accountEnv := firstExistingPath(filepath.Join(envRoot, "services", "account-manager", "account-manager.env"), filepath.Join(envRoot, "services", "account-manager", "account-manager-public-staging.env"))
	adminEnv := firstExistingPath(filepath.Join(envRoot, "services", "cloud-admin", "admin.env"), filepath.Join(envRoot, "services", "cloud-admin", "admin-staging.env"))
	updateCSVEnv(accountEnv, "ACCOUNT_MANAGER_LINODE_ALLOWED_SSH_CIDRS", cidr, appendMode)
	updateCSVEnv(adminEnv, "ADMIN_LINODE_ALLOWED_SSH_CIDRS", cidr, appendMode)
	updateVideoCIDR(videoConfig, cidr, appendMode)
}

func firewallTargets(envRoot string) ([]firewallTarget, error) {
	targets := []firewallTarget{}
	statePath := videoCloudStatePath(envRoot)
	if data, err := os.ReadFile(statePath); err == nil {
		var parsed struct {
			Stack     string         `json:"stack"`
			Firewalls map[string]any `json:"firewalls"`
		}
		if err := json.Unmarshal(data, &parsed); err != nil {
			return nil, err
		}
		labelPrefix := firstNonEmpty(parsed.Stack, stackNameFromEnvRoot(envRoot), "video-cloud-staging")
		for _, role := range []string{"edge", "api", "infra", "mqtt", "coturn"} {
			if id, ok := parsed.Firewalls[role]; ok {
				targets = append(targets, firewallTarget{Role: role, Label: labelPrefix + "-" + role, ID: fmt.Sprintf("%.0f", asFloat(id))})
			}
		}
	}
	accountState := filepath.Join(envRoot, "state", "account-manager-staging.env")
	adminState := filepath.Join(envRoot, "state", "cloud-admin-staging.env")
	targets = append(targets, firewallTarget{
		Role:  "account-manager",
		Label: firstNonEmpty(envFileValue(accountState, "ACCOUNT_MANAGER_LINODE_FIREWALL_LABEL"), "rtk-account-manager-staging-fw"),
		ID:    envFileValue(accountState, "ACCOUNT_MANAGER_LINODE_FIREWALL_ID"),
	})
	targets = append(targets, firewallTarget{
		Role:  "cloud-admin",
		Label: firstNonEmpty(envFileValue(adminState, "ADMIN_LINODE_FIREWALL_LABEL"), "rtk-cloud-admin-staging-firewall"),
		ID:    envFileValue(adminState, "ADMIN_LINODE_FIREWALL_ID"),
	})
	return targets, nil
}

func updateFirewallRules(target firewallTarget, mode, cidr string, dryRun bool) error {
	out, err := curlLinode("GET", fmt.Sprintf("/networking/firewalls/%s/rules", target.ID), "")
	if err != nil {
		return err
	}
	var rules firewallRules
	if err := json.Unmarshal(out, &rules); err != nil {
		return err
	}
	changed := false
	for i := range rules.Inbound {
		rule := &rules.Inbound[i]
		if !isSSHRule(*rule) {
			continue
		}
		if rule.Addresses == nil {
			rule.Addresses = map[string][]string{}
		}
		if mode == "replace" {
			if len(rule.Addresses["ipv4"]) != 1 || rule.Addresses["ipv4"][0] != cidr {
				rule.Addresses["ipv4"] = []string{cidr}
				changed = true
			}
		} else if !contains(rule.Addresses["ipv4"], cidr) {
			rule.Addresses["ipv4"] = append(rule.Addresses["ipv4"], cidr)
			sort.Strings(rule.Addresses["ipv4"])
			changed = true
		}
	}
	if !changed {
		return nil
	}
	rules.Version = nil
	rules.Fingerprint = nil
	payload, _ := json.Marshal(rules)
	if dryRun {
		fmt.Fprintf(os.Stderr, "[cloud-ssh-whitelist] dry-run: mode=%s role=%s firewall=%s id=%s cidr=%s\n", mode, target.Role, target.Label, target.ID, cidr)
		return nil
	}
	_, err = curlLinode("PUT", fmt.Sprintf("/networking/firewalls/%s/rules", target.ID), string(payload))
	return err
}

func replaceLiveSSHWhitelist(envRoot, cidr string) error {
	targets, err := firewallTargets(envRoot)
	if err != nil {
		return err
	}
	for _, target := range targets {
		if target.ID == "" || target.ID == "null" {
			fmt.Fprintf(os.Stderr, "[cloud-ssh-whitelist] skip: %s firewall id missing label=%s\n", target.Role, target.Label)
			continue
		}
		if err := updateFirewallRules(target, "replace", cidr, false); err != nil {
			return err
		}
	}
	return nil
}

func isSSHRule(rule firewallRule) bool {
	return rule.Label == "ssh" || (strings.EqualFold(rule.Protocol, "TCP") && rule.Ports == "22")
}

func updateCSVEnv(path, key, cidr string, appendMode bool) {
	if path == "" || !exists(path) {
		return
	}
	lines := strings.Split(strings.TrimRight(readText(path), "\n"), "\n")
	updated := false
	for i, line := range lines {
		if strings.HasPrefix(line, key+"=") {
			updated = true
			if appendMode {
				_, value, _ := strings.Cut(line, "=")
				parts := splitCSV(value)
				if !contains(parts, cidr) {
					parts = append(parts, cidr)
				}
				lines[i] = key + "=" + strings.Join(parts, ",")
			} else {
				lines[i] = key + "=" + cidr
			}
		}
	}
	if !updated {
		lines = append(lines, key+"="+cidr)
	}
	_ = os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func updateVideoCIDR(path, cidr string, appendMode bool) {
	if path == "" || !exists(path) {
		return
	}
	lines := strings.Split(strings.TrimRight(readText(path), "\n"), "\n")
	out := []string{}
	inAllowed := false
	inserted := false
	for _, line := range lines {
		if strings.TrimSpace(line) == "allowed_source_cidrs:" {
			inAllowed = true
			out = append(out, line)
			if !appendMode {
				out = append(out, "    - "+cidr)
				inserted = true
			}
			continue
		}
		if inAllowed {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "- ") {
				if appendMode {
					out = append(out, line)
					if strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")) == cidr {
						inserted = true
					}
				}
				continue
			}
			if appendMode && !inserted {
				out = append(out, "    - "+cidr)
				inserted = true
			}
			inAllowed = false
		}
		out = append(out, line)
	}
	if inAllowed && appendMode && !inserted {
		out = append(out, "    - "+cidr)
	}
	_ = os.WriteFile(path, []byte(strings.Join(out, "\n")+"\n"), 0o644)
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
