package main

import (
	"errors"
	"strings"
	"testing"
)

const isolatedCustodyStopEnv = "RTK_TEST_STOP_BEFORE_CUSTODY"
const isolatedCustodyStopMessage = "isolated test stop before custody"

// This overlay is compiled only by the isolated fixture and stops before any
// custody snapshot, intent, Kubernetes write or OpenBao operation.
func isolatedCustodyStopOverlay(source string) (string, error) {
	start := "func continueEnvironmentCreateAfterOpenBao("
	if strings.Count(source, start) != 1 {
		return "", errors.New("isolated custody boundary is ambiguous")
	}
	i := strings.Index(source, start)
	stop := "\n\tstop := func() {}"
	end := strings.Index(source[i:], stop)
	if end < 0 || end > 1024 {
		return "", errors.New("isolated custody boundary changed")
	}
	pos := i + end + len(stop)
	insert := "\n\tif os.Getenv(\"" + isolatedCustodyStopEnv + "\") == \"1\" { return runtime, stop, environmentCreateBootstrapBlocker(\"" + isolatedCustodyStopMessage + "\") }"
	return source[:pos] + insert + source[pos:], nil
}

func TestIsolatedCustodyStopOverlayRequiresExactBoundary(t *testing.T) {
	source := "func continueEnvironmentCreateAfterOpenBao(ctx context.Context) error {\n\tstop := func() {}\n\tcustody()\n}"
	got, err := isolatedCustodyStopOverlay(source)
	if err != nil || strings.Count(got, isolatedCustodyStopMessage) != 1 || strings.Index(got, isolatedCustodyStopMessage) > strings.Index(got, "custody()") {
		t.Fatal("exact before-custody boundary not selected", err)
	}
	for _, bad := range []string{"", source + source, strings.Replace(source, "stop := func() {}", "changed", 1)} {
		if _, err := isolatedCustodyStopOverlay(bad); err == nil {
			t.Fatal("ambiguous custody boundary accepted")
		}
	}
}
