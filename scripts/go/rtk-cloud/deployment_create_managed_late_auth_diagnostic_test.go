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

func TestManagedLateRecordedAuthenticationReportsSafeRefusalStage(t *testing.T) {
	for _, mode := range []string{"profile", "execution-fence", "cancelled-fence", "current-source", "runtime-progress", "repair-source"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, operator, artifact := createStateFixture(t, "dev")
			e, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operator, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { e.Close() })
			late := &createManagedLateSessionRecovery{Mode: managedLateSessionACKReady, SourceSHA: deploymentplan.Digest(e.Journal), SourceArtifactSHA: "private-artifact-canary", OriginalSession: "private-session-canary"}
			r := &createServiceSessionRecovery{ManagedLateSession: late}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			want := mode
			switch mode {
			case "profile":
				r = nil
			case "execution-fence":
				e.owner.OwnerToken = "private-owner-canary"
			case "cancelled-fence":
				cancel()
				want = "execution-fence"
			case "runtime-progress":
				late.SourceSHA = strings.Repeat("f", 64)
			case "repair-source":
				late.Mode = managedLateSessionMQTTRepair
			}
			before := deploymentplan.Digest([]any{e.Journal, late})
			calls := filepath.Join(t.TempDir(), "native-calls")
			bin := t.TempDir()
			if err := os.WriteFile(filepath.Join(bin, "kubectl"), []byte("#!/bin/sh\nprintf called > \"$RTK_AUTH_DIAGNOSTIC_TEST_CALLS\"\nexit 91\n"), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("RTK_AUTH_DIAGNOSTIC_TEST_CALLS", calls)
			role, policy, signer, err := managedLateRecordedAuthentication(ctx, cfg, store, e, environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("private-kubeconfig-canary")}, r)
			var blocker *deploymentplan.Blocker
			if !errors.As(err, &blocker) || blocker.ID != "deployment.create.recovery" || blocker.Cause != "original stopped final-core observation differs: stage=late-qualified-native-auth-"+want {
				t.Fatal("refusal lost its safe constant substage", err)
			}
			if role != "" || policy != "" || signer != "" || strings.Contains(err.Error(), "canary") {
				t.Fatal("refusal exposed private authentication material")
			}
			if _, err := os.Lstat(calls); !os.IsNotExist(err) {
				t.Fatal("rejected local authority reached a native command")
			}
			if before != deploymentplan.Digest([]any{e.Journal, late}) {
				t.Fatal("diagnostic refusal mutated original authority")
			}
		})
	}
}
