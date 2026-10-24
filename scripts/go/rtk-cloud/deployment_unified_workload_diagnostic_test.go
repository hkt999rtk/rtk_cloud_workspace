package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestTargetWorkloadEvidenceDiagnosticsKeepFixedPredicateAndRedactUnknownOCI(t *testing.T) {
	for _, mode := range []string{"valid", "missing-status", "not-started", "init-failed", "wrong-runtime-digest", "plugin-inventory", "oci-metadata", "oci-platform", "oci-config", "empty-evidence", "private-error", "decorated-known-error"} {
		t.Run(mode, func(t *testing.T) {
			plan, objects, digest := targetWorkloadFixture(t, "dev", "StatefulSet")
			status := deploymentplan.Map(objects[1]["status"])
			app := deploymentplan.Map(certIssuerObjectList(status["containerStatuses"])[0])
			var resolverErr error
			want := ""
			switch mode {
			case "missing-status":
				status["containerStatuses"] = certIssuerObjectList(status["containerStatuses"])[1:]
				want = "incomplete container inventory"
			case "not-started":
				app["started"] = false
				want = "container not running and ready"
			case "init-failed":
				init := deploymentplan.Map(certIssuerObjectList(status["initContainerStatuses"])[0])
				deploymentplan.Map(deploymentplan.Map(init["state"])["terminated"])["exitCode"] = float64(1)
				want = "init not successfully completed"
			case "wrong-runtime-digest":
				app["imageID"] = "containerd://sha256:" + strings.Repeat("f", 64)
				want = "running digest differs from the approved image"
			case "plugin-inventory":
				resolverErr = errors.New("installed Docker client plugin inventory unavailable")
			case "oci-metadata":
				resolverErr = errors.New("digest-bound OCI metadata unavailable")
			case "oci-platform":
				resolverErr = errors.New("required image platform missing")
			case "oci-config":
				resolverErr = errors.New("image config digest missing")
			case "empty-evidence":
				want = "immutable runtime image evidence unavailable: empty approved image digest evidence"
			case "private-error":
				resolverErr = errors.New("private-oci-canary: token=credential-canary image=private-registry-canary")
				want = "immutable runtime image evidence unavailable: runtime image lookup failed"
			case "decorated-known-error":
				resolverErr = errors.New("digest-bound OCI metadata unavailable: credential-canary")
				want = "immutable runtime image evidence unavailable: runtime image lookup failed"
			}
			if resolverErr != nil && want == "" {
				want = "immutable runtime image evidence unavailable: " + resolverErr.Error()
			}
			beforePlan, beforeObjects := plan.SHA256(), deploymentplan.Digest(objects)
			calls := 0
			err := verifyEnvironmentTargetWorkloads(context.Background(), plan, objects, func(context.Context, string) (map[string]bool, error) {
				calls++
				if mode == "empty-evidence" {
					return nil, nil
				}
				return map[string]bool{digest: true}, resolverErr
			})
			if mode == "valid" {
				if err != nil || calls != 1 {
					t.Fatal("healthy immutable workload changed", err)
				}
			} else {
				var blocker *deploymentplan.Blocker
				if !errors.As(err, &blocker) || blocker.ID != "deployment.post-deploy.workloads" || blocker.Resource != plan.Resources[0].ID || blocker.Cause != "owned Pod startup and approved immutable runtime image evidence could not be verified: "+want {
					t.Fatal("fixed inner predicate was lost", err)
				}
				for _, private := range []string{"credential-canary", "private-oci-canary", "private-registry-canary", "token="} {
					if strings.Contains(err.Error(), private) {
						t.Fatal("unknown OCI diagnostics escaped")
					}
				}
			}
			if plan.SHA256() != beforePlan || deploymentplan.Digest(objects) != beforeObjects {
				t.Fatal("diagnostics changed planned or observed evidence")
			}
		})
	}
}

func TestWorkloadEvidenceDiagnosticAllowlistRejectsRawOrDecoratedErrors(t *testing.T) {
	for _, message := range []string{
		"credential-canary",
		"installed Docker client plugin inventory unavailable\ncredential-canary",
		"immutable runtime image evidence unavailable: credential-canary",
		"immutable runtime image evidence unavailable: digest-bound OCI metadata unavailable credential-canary",
		"container not running and ready: credential-canary",
	} {
		if strings.Contains(environmentRuntimeImageFailureCause(message), "credential-canary") || strings.Contains(environmentPodEvidenceFailureCause(errors.New(message)), "credential-canary") {
			t.Fatal("unrecognized or decorated error was not replaced with a fixed cause")
		}
	}
}
