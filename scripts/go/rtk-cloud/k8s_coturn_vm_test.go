package main

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLKECurrentCoturnRuntimeSecretsUseLiveK8SValuesAndSyncState(t *testing.T) {
	stateDir := t.TempDir()
	installCoturnSecretKubectlStub(t, "current-turn", "current-registry", false)
	withCoturnSecretState(t, stateDir)
	writeTestFile(t, filepath.Join(stateDir, "turn-shared"), "stale-turn")
	writeTestFile(t, filepath.Join(stateDir, "turn-registry-node-auth"), "stale-registry")

	turn, registry, err := lkeCurrentCoturnRuntimeSecrets(
		provisionPaths{EnvRoot: filepath.Dir(stateDir)},
		map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if turn != "current-turn" || registry != "current-registry" {
		t.Fatalf("live coturn secrets = %q/%q", turn, registry)
	}
	for name, want := range map[string]string{"turn-shared": turn, "turn-registry-node-auth": registry} {
		raw, readErr := os.ReadFile(filepath.Join(stateDir, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.TrimSpace(string(raw)) != want {
			t.Fatalf("synced %s = %q, want live value", name, raw)
		}
		info, statErr := os.Stat(filepath.Join(stateDir, name))
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("synced %s mode = %o, want 600", name, info.Mode().Perm())
		}
	}
}

func TestLKECurrentCoturnRuntimeSecretsRejectMissingLiveValueWithoutStaleFallback(t *testing.T) {
	stateDir := t.TempDir()
	installCoturnSecretKubectlStub(t, "current-turn", "", true)
	withCoturnSecretState(t, stateDir)
	writeTestFile(t, filepath.Join(stateDir, "turn-shared"), "stale-turn")
	writeTestFile(t, filepath.Join(stateDir, "turn-registry-node-auth"), "stale-registry")

	_, _, err := lkeCurrentCoturnRuntimeSecrets(
		provisionPaths{EnvRoot: filepath.Dir(stateDir)},
		map[string]string{"CLOUD_STACK_NAME": "video-cloud-staging"},
	)
	if err == nil || !strings.Contains(err.Error(), "video-cloud-workers-runtime") {
		t.Fatalf("coturn secret sync error = %v, want live worker secret failure", err)
	}
	for name, want := range map[string]string{"turn-shared": "stale-turn", "turn-registry-node-auth": "stale-registry"} {
		raw, readErr := os.ReadFile(filepath.Join(stateDir, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if strings.TrimSpace(string(raw)) != want {
			t.Fatalf("failed sync changed %s to %q", name, raw)
		}
	}
}

func TestLKECurrentCoturnRuntimeSecretsUseExplicitOperatorOverrides(t *testing.T) {
	t.Setenv("LKE_TURN_SHARED", "operator-turn")
	t.Setenv("LKE_TURN_REGISTRY_NODE_AUTH", "operator-registry")

	turn, registry, err := lkeCurrentCoturnRuntimeSecrets(
		provisionPaths{},
		map[string]string{"CLOUD_STACK_NAME": "not-staging"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if turn != "operator-turn" || registry != "operator-registry" {
		t.Fatalf("explicit coturn secrets = %q/%q", turn, registry)
	}
}

func TestLKECurrentCoturnRuntimeSecretsRejectNonStagingLiveSync(t *testing.T) {
	_, _, err := lkeCurrentCoturnRuntimeSecrets(
		provisionPaths{},
		map[string]string{"CLOUD_STACK_NAME": "coverage-run"},
	)
	if err == nil || !strings.Contains(err.Error(), "requires the staging stack") {
		t.Fatalf("non-staging live sync error = %v", err)
	}
}

func TestLKECurrentK8SSecretValueValidation(t *testing.T) {
	tests := []struct {
		name    string
		output  string
		fail    bool
		want    string
		wantErr string
	}{
		{name: "valid", output: `{"data":{"SECRET_KEY":"dmFsdWU="}}`, want: "value"},
		{name: "kubectl failure", output: "unavailable", fail: true, wantErr: "read live K8s secret"},
		{name: "invalid json", output: `{`, wantErr: "decode live K8s secret"},
		{name: "missing key", output: `{"data":{}}`, wantErr: "missing SECRET_KEY"},
		{name: "invalid base64", output: `{"data":{"SECRET_KEY":"%%%"}}`, wantErr: "key SECRET_KEY"},
		{name: "empty decoded value", output: `{"data":{"SECRET_KEY":"ICA="}}`, wantErr: "key SECRET_KEY is empty"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			installCoturnRawKubectlStub(t, tt.output, tt.fail)
			got, err := lkeCurrentK8SSecretValue("test-namespace", "test-secret", "SECRET_KEY")
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want substring %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("secret value = %q, want %q", got, tt.want)
			}
		})
	}
}

func withCoturnSecretState(t *testing.T, stateDir string) {
	t.Helper()
	oldDir := lkeRuntimeSecretStateDir
	oldTurn := lkeRuntimeSecretCache["turn-shared"]
	oldRegistry := lkeRuntimeSecretCache["turn-registry-node-auth"]
	lkeRuntimeSecretStateDir = stateDir
	delete(lkeRuntimeSecretCache, "turn-shared")
	delete(lkeRuntimeSecretCache, "turn-registry-node-auth")
	t.Cleanup(func() {
		lkeRuntimeSecretStateDir = oldDir
		if oldTurn == "" {
			delete(lkeRuntimeSecretCache, "turn-shared")
		} else {
			lkeRuntimeSecretCache["turn-shared"] = oldTurn
		}
		if oldRegistry == "" {
			delete(lkeRuntimeSecretCache, "turn-registry-node-auth")
		} else {
			lkeRuntimeSecretCache["turn-registry-node-auth"] = oldRegistry
		}
	})
}

func installCoturnSecretKubectlStub(t *testing.T, turn, registry string, failRegistry bool) {
	t.Helper()
	secretJSON := func(key, value string) string {
		raw, err := json.Marshal(map[string]any{"data": map[string]string{key: base64.StdEncoding.EncodeToString([]byte(value))}})
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}
	stub := filepath.Join(t.TempDir(), "kubectl")
	writeTestFile(t, stub, `#!/bin/sh
case "$*" in
  *"secret video-cloud-runtime"*) printf '%s' "$RTK_TEST_TURN_SECRET" ;;
  *"secret video-cloud-workers-runtime"*)
    if [ "$RTK_TEST_FAIL_WORKER_SECRET" = 1 ]; then exit 1; fi
    printf '%s' "$RTK_TEST_REGISTRY_SECRET"
    ;;
  *) echo "unexpected kubectl arguments: $*" >&2; exit 2 ;;
esac
`)
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", stub)
	t.Setenv("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", "1")
	t.Setenv("RTK_TEST_TURN_SECRET", secretJSON("VIDEO_CLOUD_TURN_SHARED_SECRET", turn))
	t.Setenv("RTK_TEST_REGISTRY_SECRET", secretJSON("VIDEO_CLOUD_TURN_REGISTRY_NODE_AUTH_KEY", registry))
	if failRegistry {
		t.Setenv("RTK_TEST_FAIL_WORKER_SECRET", "1")
	} else {
		t.Setenv("RTK_TEST_FAIL_WORKER_SECRET", "0")
	}
}

func installCoturnRawKubectlStub(t *testing.T, output string, fail bool) {
	t.Helper()
	stub := filepath.Join(t.TempDir(), "kubectl")
	writeTestFile(t, stub, `#!/bin/sh
printf '%s' "$RTK_TEST_KUBECTL_OUTPUT"
if [ "$RTK_TEST_KUBECTL_FAIL" = 1 ]; then exit 1; fi
`)
	if err := os.Chmod(stub, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_KUBECTL", stub)
	t.Setenv("RTK_CLOUD_KUBECTL_RETRY_ATTEMPTS", "1")
	t.Setenv("RTK_TEST_KUBECTL_OUTPUT", output)
	if fail {
		t.Setenv("RTK_TEST_KUBECTL_FAIL", "1")
	} else {
		t.Setenv("RTK_TEST_KUBECTL_FAIL", "0")
	}
}
