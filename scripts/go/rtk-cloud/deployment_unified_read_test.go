package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func newResourceReadFixture(t *testing.T, environment string) storageReadFixture {
	t.Helper()
	f := newStorageReadFixture(t, environment)
	f.claim = deploymentplan.Object{"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": "ghcr-pull", "namespace": "video-cloud-" + environment + "-billing", "uid": "original-pull-secret", "resourceVersion": "1"}, "type": "kubernetes.io/dockerconfigjson", "data": deploymentplan.Object{".dockerconfigjson": "private-storage-marker"}}
	f.claimID = deploymentplan.ID(f.claim)
	f.plan = &deploymentplan.Plan{Inputs: deploymentplan.Inputs{Environment: environment, Stack: "video-cloud-" + environment}, Resources: []deploymentplan.Resource{{ID: f.claimID, UID: "original-pull-secret", Action: "retain", Desired: deploymentplan.Projection(f.claim), Baseline: deploymentplan.Projection(f.claim)}}}
	f.publish(t)
	t.Setenv("RTK_STORAGE_READ_TARGET", "claim")
	return f
}

func TestNativeResourceReadClassifiesAndRetriesOnlyTransientFailures(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, test := range []struct {
			mode, code string
			calls      int
		}{
			{"valid", "", 1}, {"unavailable-once", "", 2}, {"rate-limit-once", "", 2}, {"timeout-once", "", 2},
			{"unavailable", "KUBE_UNAVAILABLE", 2}, {"rate-limit", "KUBE_RATE_LIMITED", 2}, {"timeout", "KUBE_TIMEOUT", 2},
			{"forbidden", "KUBE_FORBIDDEN", 1}, {"unauthorized", "KUBE_UNAUTHORIZED", 1}, {"not-found", "KUBE_NOT_FOUND", 1},
			{"command-failed", "KUBE_COMMAND_FAILED", 1}, {"malformed", "KUBE_INVALID_RESPONSE", 1},
			{"oversized-stdout", "KUBE_INVALID_RESPONSE", 1}, {"oversized-stderr", "KUBE_INVALID_RESPONSE", 1}, {"missing", "", 1},
		} {
			t.Run(environment+"/"+test.mode, func(t *testing.T) {
				f := newResourceReadFixture(t, environment)
				t.Setenv("RTK_STORAGE_READ_MODE", test.mode)
				before := f.plan.SHA256()
				object, err := f.backend.Read(context.Background(), f.claimID)
				if test.code != "" {
					var blocker *deploymentplan.Blocker
					if !errors.As(err, &blocker) || blocker.ID != "deployment.access.resource" || blocker.Resource != f.claimID || !strings.Contains(blocker.Cause, "["+test.code+"]") || object != nil {
						t.Fatal("native read lost its safe resource/category", err)
					}
				} else if err != nil || (test.mode == "missing") != (object == nil) || object != nil && deploymentplan.Digest(object) != deploymentplan.Digest(f.claim) {
					t.Fatal("successful read changed or fabricated original resource", err)
				}
				if f.count(t, "claim") != test.calls || f.count(t, "volume") != 0 || f.plan.SHA256() != before {
					t.Fatal("read retries changed the plan or fetched another resource")
				}
				f.assertClean(t, err)
				if strings.HasPrefix(test.mode, "oversized-") {
					f.assertReaped(t)
				}
			})
		}
	}
}

func TestNativeResourceReadPreservesAdmissionOwnerAndInputFences(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"valid-retry", "missing", "replaced", "terminating", "config-changed", "metadata-type", "uid-type", "json-type", "forbidden"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				f := newResourceReadFixture(t, environment)
				t.Setenv("RTK_STORAGE_READ_MODE", "unavailable-once")
				wantID, calls := "deployment.drift", 2
				switch mode {
				case "missing":
					t.Setenv("RTK_STORAGE_READ_MODE", "missing")
					calls = 1
				case "replaced":
					deploymentplan.Map(f.claim["metadata"])["uid"] = "private-replacement"
				case "terminating":
					deploymentplan.Map(f.claim["metadata"])["deletionTimestamp"] = "now"
				case "config-changed":
					deploymentplan.Map(f.claim["data"])[".dockerconfigjson"] = "private-changed"
				case "uid-type":
					deploymentplan.Map(f.claim["metadata"])["uid"] = float64(1)
				case "metadata-type":
					f.claim["metadata"] = []any{"private-storage-marker"}
					wantID = "deployment.access.resource"
				case "json-type":
					t.Setenv("RTK_STORAGE_READ_MODE", "valid")
					wantID, calls = "deployment.access.resource", 1
				case "forbidden":
					t.Setenv("RTK_STORAGE_READ_MODE", "forbidden")
					wantID, calls = "deployment.access.resource", 1
				}
				f.publish(t)
				if mode == "json-type" {
					if err := os.WriteFile(f.claimPath, []byte(`[{"credential":"private-storage-marker"}]`), 0600); err != nil {
						t.Fatal(err)
					}
				}
				before := f.plan.SHA256()
				err := f.plan.Admit(context.Background(), f.backend)
				if mode == "valid-retry" {
					if err != nil {
						t.Fatal("transient selected-owner read was not recovered", err)
					}
				} else {
					var blocker *deploymentplan.Blocker
					if !errors.As(err, &blocker) || blocker.ID != wantID || blocker.Resource != f.claimID {
						t.Fatal("read retry hid an invalid response or owner/config drift", err)
					}
					if mode == "forbidden" && !strings.Contains(blocker.Cause, "[KUBE_FORBIDDEN]") {
						t.Fatal("admission replaced the native error classification", err)
					}
				}
				if f.count(t, "claim") != calls || f.plan.SHA256() != before {
					t.Fatal("successful but incompatible response was retried or plan changed")
				}
				f.assertClean(t, err)
			})
		}
	}
}

func TestNativeResourceReadRespectsCancellationAndFrozenIdentity(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		for _, mode := range []string{"pre-cancelled", "deadline", "missing-tool", "missing-config"} {
			t.Run(environment+"/"+mode, func(t *testing.T) {
				f := newResourceReadFixture(t, environment)
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				code, calls := "CHECK_CANCELLED", 0
				switch mode {
				case "pre-cancelled":
					cancel()
				case "deadline":
					ctx, cancel = context.WithTimeout(context.Background(), 300*time.Millisecond)
					defer cancel()
					t.Setenv("RTK_STORAGE_READ_MODE", "hang")
					code, calls = "KUBE_TIMEOUT", 1
				case "missing-tool":
					t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(f.directory, "missing"))
					code = "KUBE_TOOL_MISSING"
				case "missing-config":
					f.backend.KubeconfigContent = nil
					code = "KUBE_CONFIG_UNAVAILABLE"
				}
				_, err := f.backend.Read(ctx, f.claimID)
				var blocker *deploymentplan.Blocker
				observedCalls := f.count(t, "claim")
				// A parent deadline can expire during private setup or before
				// subprocess startup; neither path may start a retry.
				correctCalls := observedCalls == calls || mode == "deadline" && observedCalls == 0
				if !errors.As(err, &blocker) || blocker.ID != "deployment.access.resource" || blocker.Resource != f.claimID || !strings.Contains(blocker.Cause, "["+code+"]") || !correctCalls {
					t.Fatalf("native read lost cancellation or frozen identity fence: calls=%d err=%v", observedCalls, err)
				}
				f.assertClean(t, err)
				if observedCalls == 1 {
					f.assertReaped(t)
				}
			})
		}
	}
}
