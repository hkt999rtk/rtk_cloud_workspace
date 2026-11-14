package main

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func TestCreateFailureReportsOnlySafeResource(t *testing.T) {
	for _, tc := range []struct{ name, resource, want string }{{"kubernetes", "Job/video-cloud-dev-video-cloud/pki-enroll-example", "Job/video-cloud-dev-video-cloud/pki-enroll-example"}, {"cluster", "ClusterRole//example", "ClusterRole//example"}, {"private", "operator/env/SECRET_PRIVATE", ""}, {"escape", "Job/ns/name\nSECRET_PRIVATE", ""}} {
		t.Run(tc.name, func(t *testing.T) {
			var out, diag bytes.Buffer
			report := unifiedDeploymentReport{Decision: "NO-GO", Environment: "dev", Operation: "create"}
			_ = emitEnvironmentCreateFailure(&out, &diag, unifiedDeploymentOptions{}, &report, &deploymentplan.Blocker{ID: "deployment.admission", Resource: tc.resource, Cause: "safe", Next: "safe"})
			if len(report.Gates) != 1 || report.Gates[0].Resource != tc.want {
				t.Fatal("resource was not sanitized", report.Gates)
			}
			if tc.want != "" && !strings.Contains(out.String(), "resource: "+tc.want) {
				t.Fatal("resource missing", out.String())
			}
			if strings.Contains(out.String(), "SECRET_PRIVATE") {
				t.Fatal("private value leaked")
			}
		})
	}
}

func TestAdmissionErrorClassificationIsFixedAndRedacted(t *testing.T) {
	for _, tc := range []struct{ input, code string }{{"field is immutable PRIVATE_TOKEN", "KUBE_IMMUTABLE"}, {"exceeded quota PRIVATE_TOKEN", "KUBE_QUOTA"}, {"admission webhook denied PRIVATE_TOKEN", "KUBE_ADMISSION_DENIED"}, {"The Job is invalid: PRIVATE_TOKEN", "KUBE_INVALID"}, {"Forbidden PRIVATE_TOKEN", "KUBE_FORBIDDEN"}, {"other PRIVATE_TOKEN", "KUBE_COMMAND_FAILED"}} {
		e := classifyDeploymentAdmissionError(context.Background(), []byte(tc.input), errors.New("private"))
		if e.CheckCode() != tc.code || strings.Contains(e.Error(), "PRIVATE_TOKEN") {
			t.Fatalf("unsafe classification: %v", e)
		}
	}
}

func TestNativeDryRunReportsSafeCategoryWithoutRawServerOutput(t *testing.T) {
	makeIsolatedTestSecretStore(t, "dev")
	frozen, err := yaml.Marshal(frozenKubernetesFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	backend, err := newEnvironmentKubernetesBackend(string(frozen), "dev")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	tool := filepath.Join(bin, "kubectl")
	script := "#!/bin/sh\nprintf '%s\\n' 'The Job is invalid: PRIVATE_TOKEN' >&2\nexit 1\n"
	if err := os.WriteFile(tool, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	body := deploymentplan.Object{"apiVersion": "batch/v1", "kind": "Job", "metadata": deploymentplan.Object{"name": "example", "namespace": "video-cloud-dev-video-cloud"}}
	_, err = backend.Write(t.Context(), "create", body, true)
	var code interface{ CheckCode() string }
	if !errors.As(err, &code) || code.CheckCode() != "KUBE_INVALID" || strings.Contains(err.Error(), "PRIVATE_TOKEN") {
		t.Fatal("native server error not safely classified", err)
	}
}
