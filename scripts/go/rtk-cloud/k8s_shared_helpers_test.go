package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestRedactCommandArgs(t *testing.T) {
	got := redactCommandArgs([]string{
		"env",
		"VIDEO_CLOUD_COTURN_SHARED_SECRET=secret-value",
		"VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY=key-value",
		"LINODE_TOKEN=token-value",
		"SAFE=value",
	})
	want := []string{
		"env",
		"VIDEO_CLOUD_COTURN_SHARED_SECRET=<redacted>",
		"VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY=<redacted>",
		"LINODE_TOKEN=<redacted>",
		"SAFE=value",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("redactCommandArgs() = %#v, want %#v", got, want)
	}
}

func TestSharedKubernetesAddressAndSSHHelpers(t *testing.T) {
	if got := uniqueNonEmpty("", "one", "two", "one"); !reflect.DeepEqual(got, []string{"one", "two"}) {
		t.Fatalf("unique values = %#v", got)
	}
	configPath := filepath.Join(t.TempDir(), "video.yaml")
	if err := os.WriteFile(configPath, []byte(`
prometheus:
  targets:
    - job: cloud_logger_node
      address: "10.42.1.90:9100"
    - job: other
      address: other.example.test
`), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := prometheusTargetHost(configPath, "cloud_logger_node"); got != "10.42.1.90" {
		t.Fatalf("prometheus host = %q", got)
	}
	if got := prometheusTargetHost(configPath, "other"); got != "other.example.test" {
		t.Fatalf("prometheus host without port = %q", got)
	}
	if got := prometheusTargetHost(filepath.Join(t.TempDir(), "missing"), "other"); got != "" {
		t.Fatalf("missing prometheus config host = %q", got)
	}

	for host, want := range map[string]bool{
		"10.1.2.3": true, "172.16.0.1": true, "172.31.0.1": true, "172.32.0.1": false,
		"192.168.1.1": true, "192.0.2.1": false, "not-an-ip": false,
	} {
		if got := isPrivateIPv4(host); got != want {
			t.Fatalf("isPrivateIPv4(%q) = %t, want %t", host, got, want)
		}
	}

	statePath := filepath.Join(t.TempDir(), "video-state.json")
	if err := os.WriteFile(statePath, []byte(`{"instances":{"edge":{"public_ipv4":"203.0.113.10"}}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	paths := provisionPaths{VideoState: statePath}
	if got := videoStatePublicHost(statePath, "edge"); got != "203.0.113.10" {
		t.Fatalf("public host = %q", got)
	}
	if got := loggerProxyCommand(paths, "/id", "10.42.1.90"); !strings.Contains(got, "root@203.0.113.10") || !strings.Contains(got, "-W %h:%p") {
		t.Fatalf("proxy command = %q", got)
	}
	if got := loggerProxyCommand(paths, "/id", "203.0.113.20"); got != "" {
		t.Fatalf("public host proxy = %q", got)
	}
	args := loggerSSHArgs(paths, "/id", "10.42.1.90", "echo", "ready")
	joined := strings.Join(args, " ")
	if !strings.Contains(joined, "ProxyCommand=") || !strings.Contains(joined, "root@10.42.1.90 echo ready") {
		t.Fatalf("SSH args = %#v", args)
	}
}
