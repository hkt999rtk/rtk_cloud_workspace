package main

import (
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestManagedOpenBaoAuditSignalRequiresCanonicalOriginalProviderProcess(t *testing.T) {
	for _, mode := range []string{"valid", "pid-one", "pid-zero", "pid-leading-zero", "shell-injection", "overflow", "zero-start", "start-leading-zero", "foreign-executable", "foreign-user", "different-marker", "malformed-exe-hash", "extra-field"} {
		t.Run(mode, func(t *testing.T) {
			proof := deploymentplan.Object{"pid": "24", "start_time": "13380439", "executable_sha256": strings.Repeat("a", 64), "executable": "/bin/bao", "uid": "100", "pid_marker_sha256": environmentPreflightBytesSHA256([]byte("$\n"))}
			switch mode {
			case "pid-one":
				proof["pid"] = "1"
			case "pid-zero":
				proof["pid"] = "0"
			case "pid-leading-zero":
				proof["pid"] = "01"
			case "shell-injection":
				proof["pid"] = "24; kill 1"
			case "overflow":
				proof["pid"] = "9999999999999999999999"
			case "zero-start":
				proof["start_time"] = "0"
			case "start-leading-zero":
				proof["start_time"] = "013380439"
			case "foreign-executable":
				proof["executable"] = "/usr/local/bin/openbaopkihost"
			case "foreign-user":
				proof["uid"] = "1000"
			case "different-marker":
				proof["pid_marker_sha256"] = strings.Repeat("b", 64)
			case "malformed-exe-hash":
				proof["executable_sha256"] = "unknown"
			case "extra-field":
				proof["unqualified"] = "process"
			}
			err := validateManagedOpenBaoAuditProcess(proof)
			if (mode == "valid") != (err == nil) {
				t.Fatal("unqualified process could receive the provider signal", mode, err)
			}
		})
	}
}
