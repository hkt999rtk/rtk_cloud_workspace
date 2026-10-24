package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCreateCRLBackendUsesSharedTimeoutForActualNativeReadiness(t *testing.T) {
	for _, test := range []struct {
		name, owner string
		ready       bool
	}{{"MQTT-controller-ready", "pki-controller", true}, {"TLS-certissuer-ready", "certissuer", true}, {"unready-controller-still-blocks", "pki-controller", false}} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := yaml.Marshal(frozenKubernetesFixture(t))
			if err != nil {
				t.Fatal(err)
			}
			values := map[string]string{deploymentRolloutTimeoutKey: "300", "DEPLOYMENT_UPGRADE_WINDOW_SECONDS": "3600"}
			backend, err := newEnvironmentCreateCRLBackend(string(raw), "dev", values)
			if err != nil {
				t.Fatal(err)
			}
			if backend.RolloutTimeout != 300*time.Second || strings.Contains(string(backend.KubeconfigContent), "must-not-execute") {
				t.Fatal("shared deadline or frozen selected identity lost")
			}
			commands := t.TempDir()
			argsFile := filepath.Join(t.TempDir(), "actual-native-args.txt")
			script := `#!/bin/sh
case " $* " in
 *" rollout status "*)
  printf '%s\n' "$@" > "$RTK_TEST_CRL_READINESS_ARGS"
  if test "$RTK_TEST_CRL_NOT_READY" = yes; then exit 73; fi
  exit 0 ;;
 *) printf '{"items":[]}\n' ;;
esac
`
			if err := os.WriteFile(filepath.Join(commands, "kubectl"), []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", commands+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_TEST_CRL_READINESS_ARGS", argsFile)
			failed := ""
			if !test.ready {
				failed = "yes"
			}
			t.Setenv("RTK_TEST_CRL_NOT_READY", failed)
			// The observer independently bounds each read to30s. The native backend
			// still requires an explicit shared deadline instead of a zero default.
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			resource := deploymentplan.Resource{ID: "Deployment/video-cloud-dev-video-cloud/" + test.owner, Phase: "workload", Action: "retain"}
			err = backend.Wait(ctx, resource)
			if (err == nil) != test.ready {
				t.Fatal("native unready response was treated as ready", err)
			}
			actual, readErr := os.ReadFile(argsFile)
			if readErr != nil {
				t.Fatal("readiness did not reach native rollout status", readErr)
			}
			args := "\n" + string(actual)
			for _, expected := range []string{"\n--request-timeout=5m0s\n", "\nrollout\nstatus\n", "\nDeployment/" + test.owner + "\n", "\n--timeout=300s\n"} {
				if !strings.Contains(args, expected) {
					t.Fatal("native wait lost shared timeout or exact owner")
				}
			}
			if strings.Contains(args, "private-frozen-token") {
				t.Fatal("frozen credential exposed in arguments")
			}
		})
	}
}

func TestCreateCRLBackendBlocksMissingOrUnboundedSharedWaitPolicy(t *testing.T) {
	for _, test := range []struct{ name, rollout, window string }{{"missing", "", "3600"}, {"zero", "0", "3600"}, {"past-PKI-window", "300", "120"}, {"missing-PKI-window", "300", ""}} {
		t.Run(test.name, func(t *testing.T) {
			raw, err := yaml.Marshal(frozenKubernetesFixture(t))
			if err != nil {
				t.Fatal(err)
			}
			backend, err := newEnvironmentCreateCRLBackend(string(raw), "dev", map[string]string{deploymentRolloutTimeoutKey: test.rollout, "DEPLOYMENT_UPGRADE_WINDOW_SECONDS": test.window})
			if err == nil || backend.RolloutTimeout != 0 {
				t.Fatal("invalid policy silently supplied readiness authority")
			}
		})
	}
}
