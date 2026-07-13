package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func uniqueNonEmpty(values ...string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, value := range values {
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}

func prometheusTargetHost(configPath, job string) string {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return ""
	}
	inJob := false
	for _, line := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "- job:") {
			inJob = strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "- job:")), `"'`) == job
			continue
		}
		if !inJob || !strings.HasPrefix(trimmed, "address:") {
			continue
		}
		address := strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "address:")), `"'`)
		host, _, ok := strings.Cut(address, ":")
		if !ok {
			return strings.TrimSpace(address)
		}
		return strings.TrimSpace(host)
	}
	return ""
}

func digShort(server, domain string) string {
	out, _ := exec.Command("dig", "+short", "@"+server, domain).Output()
	return strings.TrimSpace(strings.Split(string(out), "\n")[0])
}

func defaultStagingSSHKey() string {
	return filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519_rtkcloud")
}

func loggerSSHArgs(paths provisionPaths, sshKey, host string, remoteArgs ...string) []string {
	if sshKey == "" {
		sshKey = defaultStagingSSHKey()
	}
	args := []string{
		"-i", sshKey,
		"-o", "IdentitiesOnly=yes",
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=15",
		"-o", "ServerAliveInterval=10",
		"-o", "ServerAliveCountMax=3",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "LogLevel=ERROR",
	}
	if proxy := loggerProxyCommand(paths, sshKey, host); proxy != "" {
		args = append(args, "-o", "ProxyCommand="+proxy)
	}
	args = append(args, "root@"+host)
	args = append(args, remoteArgs...)
	return args
}

func loggerProxyCommand(paths provisionPaths, sshKey, host string) string {
	if !isPrivateIPv4(host) {
		return ""
	}
	edge := videoStatePublicHost(paths.VideoState, "edge")
	if edge == "" || edge == host {
		return ""
	}
	return strings.Join([]string{
		"ssh",
		"-i", shellQuote(sshKey),
		"-o", "IdentitiesOnly=yes",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=/dev/null",
		"-o", "ConnectTimeout=15",
		"-o", "LogLevel=ERROR",
		"-W", "%h:%p",
		"root@" + edge,
	}, " ")
}

func isPrivateIPv4(host string) bool {
	parts := strings.Split(host, ".")
	if len(parts) != 4 {
		return false
	}
	a := atoiOrZero(parts[0])
	b := atoiOrZero(parts[1])
	switch {
	case a == 10:
		return true
	case a == 172 && b >= 16 && b <= 31:
		return true
	case a == 192 && b == 168:
		return true
	default:
		return false
	}
}

func runCmdWithInput(dir, input, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s: %w", name, strings.Join(redactCommandArgs(args), " "), err)
	}
	return nil
}

func redactCommandArgs(args []string) []string {
	redacted := append([]string(nil), args...)
	for i, arg := range redacted {
		key, _, found := strings.Cut(arg, "=")
		upper := strings.ToUpper(key)
		if found && (strings.Contains(upper, "SECRET") || strings.Contains(upper, "TOKEN") || strings.HasSuffix(upper, "_KEY") || strings.Contains(upper, "PASSWORD")) {
			redacted[i] = key + "=<redacted>"
		}
	}
	return redacted
}

func runCmdQuiet(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	return cmd.Run()
}

func videoStatePublicHost(path, role string) string {
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var state struct {
		Instances map[string]struct {
			PublicIPv4 string `json:"public_ipv4"`
		} `json:"instances"`
	}
	if err := json.Unmarshal(data, &state); err != nil {
		return ""
	}
	return state.Instances[role].PublicIPv4
}
