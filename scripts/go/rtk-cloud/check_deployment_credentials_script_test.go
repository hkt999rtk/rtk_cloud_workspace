package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDeploymentCredentialScriptForwardsOptionalPKIFlagsOnBash(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	calls := filepath.Join(bin, "calls")
	if err := os.WriteFile(filepath.Join(bin, "go"), []byte("#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$CALLS\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CALLS", calls)
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"without optional flags", nil, "secrets verify --environment staging"},
		{"with Product PKI flag", []string{"--require-product-pki"}, "secrets verify --environment staging --require-product-pki"},
		{"with retained Cloud", []string{"--require-product-pki", "--product-pki-cloud-id", testProductPKICloudID}, "secrets verify --environment staging --require-product-pki --product-pki-cloud-id " + testProductPKICloudID},
		{"with deployment identity", []string{"--require-deployment-identity"}, "secrets verify --environment staging --require-deployment-identity"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(calls, nil, 0o600); err != nil {
				t.Fatal(err)
			}
			args := append([]string{filepath.Join(workspace, "scripts", "check-deployment-credentials.sh"), "--environment", "staging", "--read-only", "--checks", "linode"}, tc.args...)
			output, err := exec.Command("bash", args...).CombinedOutput()
			if err != nil {
				t.Fatalf("script failed: %v %s", err, output)
			}
			payload, err := os.ReadFile(calls)
			if err != nil {
				t.Fatal(err)
			}
			lines := strings.Split(strings.TrimSpace(string(payload)), "\n")
			if len(lines) != 2 || !strings.Contains(lines[0], tc.want) || !strings.Contains(lines[1], "deployment credentials-check") || strings.Contains(lines[1], "--require-product-pki") || strings.Contains(lines[1], "--require-deployment-identity") || strings.Contains(lines[1], "--product-pki-cloud-id") {
				t.Fatalf("unexpected credential script forwarding: %q", lines)
			}
		})
	}
}

func TestDeploymentCredentialScriptDetectsScopedImageDrift(t *testing.T) {
	workspace, err := workspaceRoot()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	config := filepath.Join(root, "staging")
	envRoot := filepath.Join(root, "runtime")
	for _, dir := range []string{filepath.Join(config, "kube"), filepath.Join(config, "operator", "env")} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.MkdirAll(filepath.Join(envRoot, "env"), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(config, "kube", "kubeconfig.yaml"):                  "fixture",
		filepath.Join(config, "operator", "env", "LKE_VIDEO_CLOUD_IMAGE"): "registry.example.test/video-cloud:reviewed\n",
		filepath.Join(envRoot, "env", "stack.env"):                         "CLOUD_STACK_NAME=video-cloud-staging\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir()
	for name, script := range map[string]string{
		"go": "#!/bin/sh\nexit 0\n",
		"kubectl": `#!/bin/sh
case "$*" in
  *"get secret account-manager-runtime"*)
    if [ "${BILLABLE_GATE:-}" = off ]; then printf 'ZmFsc2U='; else printf 'dHJ1ZQ=='; fi
    ;;
  *"exec statefulset/postgresql"*) printf '0|active\n' ;;
  *"get deployments"*)
    for name in video-cloud-api video-cloud-cleaner video-cloud-clipverifier video-cloud-statistics video-cloud-metricsexporter video-cloud-turnregistry video-cloud-logingester video-cloud-mqttusage; do
      image='registry.example.test/video-cloud:reviewed'
      if [ "$name" = video-cloud-cleaner ] && [ "${DRIFT:-}" = 1 ]; then image='registry.example.test/video-cloud:old'; fi
      printf '%s\t%s\n' "$name" "$image"
    done
    ;;
esac
`,
	} {
		if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(bin, "kubectl"))
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", root)
	t.Setenv("RTK_CLOUD_ENV_ROOT", envRoot)
	args := []string{filepath.Join(workspace, "scripts", "check-deployment-credentials.sh"), "--environment", "staging", "--read-only", "--require-video-cloud-image"}
	for _, tc := range []struct {
		name  string
		drift string
		fail  bool
	}{
		{name: "matching image"},
		{name: "old worker image", drift: "1", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DRIFT", tc.drift)
			output, err := exec.Command("bash", args...).CombinedOutput()
			if (err != nil) != tc.fail {
				t.Fatalf("unexpected result: %v %s", err, output)
			}
			if tc.fail && !strings.Contains(string(output), "image drift: video-cloud-staging-video-cloud/video-cloud-cleaner") {
				t.Fatalf("missing drift diagnosis: %s", output)
			}
		})
	}
	for _, tc := range []struct {
		name, gate, want string
		fail             bool
	}{
		{name: "billable logging ready", want: "Billable logging Product grants and Logger catalog are ready"},
		{name: "billable logging disabled", gate: "off", want: "Product service writes are disabled", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BILLABLE_GATE", tc.gate)
			billableArgs := []string{filepath.Join(workspace, "scripts", "check-deployment-credentials.sh"), "--environment", "staging", "--read-only", "--require-billable-logging-ready"}
			output, err := exec.Command("bash", billableArgs...).CombinedOutput()
			if (err != nil) != tc.fail || !strings.Contains(string(output), tc.want) {
				t.Fatalf("unexpected result: %v %s", err, output)
			}
		})
	}
}
