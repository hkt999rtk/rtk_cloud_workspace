package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestCreateRegistrySessionReadHandlesOnlyTransientTransportFailures(t *testing.T) {
	const sid = "11111111-1111-4111-8111-111111111111"
	for _, tc := range []struct {
		name, prefix, code string
		calls              int
		fail               bool
	}{
		{"transient", `if [ ! -f "$RTK_CHECK_MARKER" ]; then
 : > "$RTK_CHECK_MARKER"
 printf 'Error from server: error dialing backend: proxy error: 500 Internal Server Error private-marker' >&2
 exit 1
fi
`, "", 2, false},
		{"forbidden", "printf 'Forbidden private-marker' >&2\nexit 1\n", "KUBE_FORBIDDEN", 1, true},
		{"bounded", "printf '500 Internal Server Error private-marker' >&2\nexit 1\n", "KUBE_UNAVAILABLE", deploymentReadMaxAttempts, true},
		{"sql-error", "printf 'syntax error private-marker' >&2\nexit 1\n", "", 1, true},
		{"malformed", "printf 'invalid-json'\nexit 0\n", "", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, e, _, _ := freshStageFixture(t)
			t.Setenv("RTK_CHECK_MARKER", filepath.Join(t.TempDir(), "first-read"))
			calls := deploymentRuntimeTestKubectl(t, tc.prefix+`case "$*" in
 *'PGOPTIONS=-c default_transaction_read_only=on'*'registry-read SELECT '*) printf '{"session_id":"11111111-1111-4111-8111-111111111111"}\n' ;;
 *) exit 99 ;;
esac
`)
			cfg := deploymentConfig{Environment: store.Environment, Values: map[string]string{"CLOUD_STACK_NAME": e.Journal.Stack}}
			runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated fixture")}}
			state, raw, err := readManagedSessionState(t.Context(), cfg, runtime, sid)
			if (err != nil) != tc.fail {
				t.Fatalf("registry read = %v", err)
			}
			if tc.code != "" {
				deploymentRuntimeCode(t, err, tc.code)
			}
			if err != nil && strings.Contains(err.Error(), "private-marker") {
				t.Fatal("private command diagnostic exposed")
			}
			if err == nil && (state.Session != sid || string(raw) != `{"session_id":"11111111-1111-4111-8111-111111111111"}`) {
				t.Fatal("original registry bytes changed")
			}
			if got := deploymentRuntimeCallCount(t, calls); got != tc.calls {
				t.Fatalf("native reads = %d, want %d", got, tc.calls)
			}
		})
	}
}
