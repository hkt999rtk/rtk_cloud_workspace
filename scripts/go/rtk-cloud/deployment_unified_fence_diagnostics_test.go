package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
	"strings"
	"testing"
)

func fenceDiagnosticFixture(t *testing.T) *compiledEnvironmentDeployment {
	t.Helper()
	cfg, store, release, _ := maintainedDeploymentSnapshotFixture(t, "dev")
	snapshot, err := readEnvironmentDeploymentSnapshot(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	backend, err := newEnvironmentKubernetesBackend(snapshot.Kubeconfig, "dev")
	if err != nil {
		t.Fatal(err)
	}
	bindings := environmentIdentityBindings{Version: 1, Environment: "dev", Stack: cfg.Values["CLOUD_STACK_NAME"], Owners: map[string]string{}}
	plan := &deploymentplan.Plan{Version: deploymentplan.Version, Inputs: deploymentplan.Inputs{Environment: "dev", Stack: bindings.Stack, Operation: "upgrade", ConfigurationSHA256: snapshot.Digest(cfg), ReleaseSHA256: deploymentplan.Digest(release), BindingsSHA256: deploymentplan.Digest(bindings)}}
	storage, err := backend.storageBindings(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	plan.StorageSHA256 = deploymentplan.Digest(storage)
	dir := t.TempDir()
	script := filepath.Join(dir, "kubectl")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nif [ \"$RTK_FENCE_TEST_TRANSPORT\" = 1 ]; then printf '%s' 'Unable to connect to the server: private-fence-canary' >&2; exit 1; fi\nprintf '%s' '{\"items\":[]}'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return &compiledEnvironmentDeployment{Plan: plan, Configuration: cfg, Store: store, Release: release, Bindings: bindings, Backend: backend}
}

func TestEnvironmentFenceDiagnosticsIdentifySafeCurrentFailureCategory(t *testing.T) {
	for _, mode := range []string{"valid", "selected-plan", "configuration-resolution", "snapshot-read", "configuration-drift", "release-drift", "bindings-drift", "runtime-transport", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			c := fenceDiagnosticFixture(t)
			ctx := t.Context()
			want := ""
			plan := c.Plan
			switch mode {
			case "selected-plan":
				plan = &deploymentplan.Plan{}
				want = "deployment.inputs.plan"
			case "configuration-resolution":
				c.Configuration.Workspace = filepath.Join(t.TempDir(), "private-fence-canary")
				want = "deployment.inputs.configuration-resolution"
			case "snapshot-read":
				if err := os.Remove(filepath.Join(c.Store.RuntimeDir(), "postgres")); err != nil {
					t.Fatal(err)
				}
				want = "deployment.inputs.snapshot"
			case "configuration-drift":
				if err := c.Store.write("runtime/postgres", []byte("private-fence-canary"), true); err != nil {
					t.Fatal(err)
				}
				want = "deployment.inputs.configuration"
			case "release-drift":
				c.Release.WorkspaceRevision = strings.Repeat("f", 40)
				want = "deployment.inputs.release"
			case "bindings-drift":
				c.Bindings.Owners = map[string]string{"private-fence-canary": "foreign"}
				want = "deployment.inputs.bindings"
			case "runtime-transport":
				t.Setenv("RTK_FENCE_TEST_TRANSPORT", "1")
				want = "deployment.inventory"
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx = cancelled
			}
			before := c.Plan.SHA256()
			_, err := c.FenceInventory(ctx, plan)
			if mode == "valid" {
				if err != nil {
					t.Fatal("valid original fence changed", err)
				}
				return
			}
			if mode == "cancelled" {
				if !errors.Is(err, context.Canceled) {
					t.Fatal("context cancellation lost")
				}
				return
			}
			var blocker *deploymentplan.Blocker
			if !errors.As(err, &blocker) || blocker.ID != want {
				t.Fatalf("failure category=%v want %s", err, want)
			}
			if strings.Contains(err.Error(), "private-fence-canary") || strings.Contains(err.Error(), "Unable to connect") {
				t.Fatal("private tool/input detail leaked")
			}
			if c.Plan.SHA256() != before {
				t.Fatal("diagnosis changed frozen plan")
			}
		})
	}
}
func TestEnvironmentFenceDiagnosticsPreserveTypedErrorsAndContext(t *testing.T) {
	expected := &deploymentplan.Blocker{ID: "deployment.access.resource", Resource: "Secret/video-cloud-dev-billing/billing-runtime", Cause: "cannot read selected owner [KUBE_FORBIDDEN]", Next: "restore exact selected access"}
	if got := environmentFenceFailure(expected, "deployment.inventory", "fallback"); got != expected {
		t.Fatal("typed public blocker changed")
	}
	for _, sentinel := range []error{context.Canceled, context.DeadlineExceeded} {
		got := environmentFenceFailure(fmt.Errorf("private-fence-canary: %w", sentinel), "deployment.inventory", "fallback")
		if !errors.Is(got, sentinel) || strings.Contains(got.Error(), "private-fence-canary") {
			t.Fatal("context lost or private context prefix leaked")
		}
	}
	unknown := environmentFenceFailure(errors.New("private-fence-canary"), "deployment.inventory", "current Kubernetes inventory could not be read or decoded")
	row := safeQualifiedExecutionFailure(unknown)
	if row.ID != "deployment.inventory" || row.Cause != "current Kubernetes inventory could not be read or decoded" || strings.Contains(row.Cause, "private-fence-canary") {
		t.Fatal("public report did not retain precise safe boundary")
	}
}
