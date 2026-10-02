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
		filepath.Join(envRoot, "env", "stack.env"):                        "CLOUD_STACK_NAME=video-cloud-staging\n",
	} {
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	bin := t.TempDir()
	for name, script := range map[string]string{
		"go": `#!/bin/sh
case "$*" in
  *"deployment credentials-check"*"--require-loki-retention-ready"*)
    case "$*" in *"--require-logger-period-source-ready"*) ;; *) exit 1 ;; esac
    case "${BILLABLE_LOKI_GUARD:-}" in
      storage) echo 'Loki retention Deployment still uses nonpersistent storage'; exit 1 ;;
      retention) echo 'Loki retention ConfigMap lacks retention_enabled: true'; exit 1 ;;
      checksum) echo 'Loki retention Deployment config checksum differs from the current retention configuration'; exit 1 ;;
      *) echo 'Loki canonical configuration checksum verified; receipt/Billing reconciliation remains required' ;;
    esac
    ;;
esac
`,
		"kubectl": `#!/bin/sh
case "$*" in
  *"get secret account-manager-runtime"*)
    if [ "${BILLABLE_GATE:-}" = off ]; then printf 'ZmFsc2U='; else printf 'dHJ1ZQ=='; fi
    ;;
  *"get pods -l app.kubernetes.io/name=account-manager-outbox-worker "*) printf account-manager-outbox-worker-1 ;;
  *"get pods -l app.kubernetes.io/name=account-manager "*) printf account-manager-1 ;;
  *"exec pod/account-manager-1 -- printenv ACCOUNT_MANAGER_PLATFORM_SERVICE_PRODUCT_WRITES"*)
    if [ "${BILLABLE_API_POD_GATE:-}" = off ]; then printf false; else printf true; fi
    ;;
  *"exec pod/account-manager-outbox-worker-1 -- printenv ACCOUNT_MANAGER_PLATFORM_SERVICE_PRODUCT_WRITES"*) printf true ;;
  *"exec statefulset/postgresql"*) printf '0|active\n' ;;
  *"get deployment video-cloud-api "*)
    case "$*" in
      *VIDEO_CLOUD_LOGGER_MQTT_CUTOVER_ENABLED*)
        if [ "${BILLABLE_MQTT_CUTOVER:-}" = off ]; then printf false; else printf true; fi ;;
      *) printf true ;;
    esac
    ;;
  *"get deployment video-cloud-logingester "*) printf true ;;
  *"get deployments"*)
    for name in video-cloud-api video-cloud-cleaner video-cloud-clipverifier video-cloud-statistics video-cloud-metricsexporter video-cloud-turnregistry video-cloud-logingester video-cloud-mqttusage; do
      image='registry.example.test/video-cloud:reviewed'
      if [ "$name" = video-cloud-cleaner ] && [ "${DRIFT:-}" = 1 ]; then image='registry.example.test/video-cloud:old'; fi
      printf '%s\t%s\n' "$name" "$image"
    done
    case "${FACTORY_FIXTURE:-}" in
      matching) printf 'factoryenroll\tregistry.example.test/video-cloud:reviewed\n' ;;
      drift) printf 'factoryenroll\tregistry.example.test/video-cloud:old\n' ;;
    esac
    ;;
  *"rollout status deployment/factoryenroll"*) test "${FACTORY_FIXTURE:-}" != unready ;;
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
	for _, tc := range []struct{ name, flag, state, want string }{
		{"public Factory matches", "true", "matching", ""},
		{"public Factory old image", "true", "drift", "image drift: video-cloud-staging-video-cloud/factoryenroll"},
		{"public Factory missing", "true", "missing", "missing required Video Cloud deployment: video-cloud-staging-video-cloud/factoryenroll"},
		{"public Factory unready", "true", "unready", ""},
		{"private Factory outside selected scope", "false", "drift", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := os.WriteFile(filepath.Join(envRoot, "env", "stack.env"), []byte("CLOUD_STACK_NAME=video-cloud-staging\nFACTORY_ENROLL_PUBLIC_ENABLED="+tc.flag+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			t.Setenv("FACTORY_FIXTURE", tc.state)
			t.Setenv("FACTORY_ENROLL_PUBLIC_ENABLED", "false") // selected runtime wins over caller
			output, err := exec.Command("bash", args...).CombinedOutput()
			wantFailure := tc.want != "" || tc.state == "unready"
			if (err != nil) != wantFailure || (tc.want != "" && !strings.Contains(string(output), tc.want)) {
				t.Fatalf("unexpected Factory image gate: %v %s", err, output)
			}
		})
	}
	for _, tc := range []struct {
		name, gate, apiPodGate, mqttCutover, lokiGuard, want string
		fail                                                 bool
	}{
		{name: "billable logging ready forwards Go guard", want: "Loki canonical configuration checksum verified; receipt/Billing reconciliation remains required"},
		{name: "billable logging disabled", gate: "off", want: "Product service writes are disabled", fail: true},
		{name: "API Pod has stale gate", apiPodGate: "off", want: "account-manager Pod account-manager-1 Product service writes are false", fail: true},
		{name: "MQTT cutover missing", mqttCutover: "off", want: "video-cloud-api VIDEO_CLOUD_LOGGER_MQTT_CUTOVER_ENABLED is false", fail: true},
		{name: "Loki PVC missing", lokiGuard: "storage", want: "Loki retention Deployment still uses nonpersistent storage", fail: true},
		{name: "Loki retention missing", lokiGuard: "retention", want: "Loki retention ConfigMap lacks retention_enabled: true", fail: true},
		{name: "old Loki revision with new ConfigMap", lokiGuard: "checksum", want: "Loki retention Deployment config checksum differs", fail: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("BILLABLE_GATE", tc.gate)
			t.Setenv("BILLABLE_API_POD_GATE", tc.apiPodGate)
			t.Setenv("BILLABLE_MQTT_CUTOVER", tc.mqttCutover)
			t.Setenv("BILLABLE_LOKI_GUARD", tc.lokiGuard)
			billableArgs := []string{filepath.Join(workspace, "scripts", "check-deployment-credentials.sh"), "--environment", "staging", "--read-only", "--require-billable-logging-ready"}
			output, err := exec.Command("bash", billableArgs...).CombinedOutput()
			if (err != nil) != tc.fail || !strings.Contains(string(output), tc.want) {
				t.Fatalf("unexpected result: %v %s", err, output)
			}
		})
	}
}
