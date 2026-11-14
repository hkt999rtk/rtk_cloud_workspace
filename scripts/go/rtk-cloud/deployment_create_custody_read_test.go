package main

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestOriginalBootstrapCustodyReadsRetryOnlyTransientFailures(t *testing.T) {
	for _, tc := range []struct {
		name, prefix, code string
		calls              int
		fail               bool
	}{
		{"transient", `if [ ! -f "$RTK_CHECK_MARKER" ]; then
 : > "$RTK_CHECK_MARKER"
 printf 'Service Unavailable private-marker' >&2
 exit 1
fi
`, "", 4, false},
		{"forbidden", "printf 'Forbidden private-marker' >&2\nexit 1\n", "KUBE_FORBIDDEN", 1, true},
		{"bounded", "printf 'Service Unavailable private-marker' >&2\nexit 1\n", "KUBE_UNAVAILABLE", deploymentReadMaxAttempts, true},
		{"changed-material", "printf 'foreign-material'\nexit 0\n", "", 1, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store, e, _, _ := freshStageFixture(t)
			t.Setenv("RTK_CHECK_MARKER", filepath.Join(t.TempDir(), "first-read"))
			calls := deploymentRuntimeTestKubectl(t, tc.prefix+`case "$*" in
 *'/run/pki-bootstrap/bootstrap.key') printf 'original-key' ;;
 *'/run/pki-bootstrap/bootstrap.csr') printf 'original-csr' ;;
 *'/run/pki-bootstrap/bootstrap.crt') printf 'original-certificate' ;;
 *) exit 99 ;;
esac
`)
			r := &createServiceSessionRecovery{OriginalKeySHA: environmentPreflightBytesSHA256([]byte("original-key")), OriginalCSRSHA: environmentPreflightBytesSHA256([]byte("original-csr")), Ceremony: environmentCreateServiceCeremony{BootstrapCredentialNamespace: e.Journal.Stack + "-video-cloud", CustodyReader: "pki-bootstrap-custody-" + e.Journal.PlanSHA256[:12]}}
			proof := &createManagedSessionRecovery{OriginalCertificatePath: "/run/pki-bootstrap/bootstrap.crt", OriginalCertificateSHA256: environmentPreflightBytesSHA256([]byte("original-certificate"))}
			runtime := environmentCreateBootstrapRuntime{Execution: e, Backend: environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated fixture")}}
			err := verifyManagedSeedOriginalCustody(t.Context(), runtime, r, proof)
			if (err != nil) != tc.fail {
				t.Fatalf("custody verification = %v", err)
			}
			if tc.code != "" {
				deploymentRuntimeCode(t, err, tc.code)
			}
			if err != nil && strings.Contains(err.Error(), "private-marker") {
				t.Fatal("private command diagnostic exposed")
			}
			if got := deploymentRuntimeCallCount(t, calls); got != tc.calls {
				t.Fatalf("native reads = %d, want %d", got, tc.calls)
			}
		})
	}
}
