package main

import (
	"strings"
	"testing"
)

func TestLoggerSSHArgsUseEphemeralKnownHosts(t *testing.T) {
	paths := provisionPaths{VideoState: t.TempDir() + "/state.json"}
	args := loggerSSHArgs(paths, "/tmp/key", "203.0.113.10", "true")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "UserKnownHostsFile=/dev/null") {
		t.Fatalf("logger ssh args should use ephemeral known_hosts, got %q", joined)
	}

	scpArgs := loggerSCPArgs(paths, "/tmp/key", "203.0.113.10", "/tmp/source", "root@203.0.113.10:/tmp/dest")
	joined = strings.Join(scpArgs, " ")
	if !strings.Contains(joined, "UserKnownHostsFile=/dev/null") {
		t.Fatalf("logger scp args should use ephemeral known_hosts, got %q", joined)
	}
}

func TestLoggerForwarderSSHReadinessRejectsMissingHost(t *testing.T) {
	t.Setenv("CLOUD_LOGGER_FORWARDER_SSH_READY_ATTEMPTS", "1")
	t.Setenv("CLOUD_LOGGER_FORWARDER_SSH_READY_DELAY_SEC", "0")
	paths := provisionPaths{VideoState: t.TempDir() + "/state.json"}

	err := waitForLoggerForwarderSSH(paths, "/tmp/key", loggerForwarderTarget{name: "coturn"})
	if err == nil || !strings.Contains(err.Error(), "target host missing") {
		t.Fatalf("waitForLoggerForwarderSSH error = %v, want missing host", err)
	}
}
