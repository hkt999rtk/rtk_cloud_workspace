package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCRLExecutionContractDeviceCommandPreservesIncompleteSource(t *testing.T) {
	for _, mode := range []string{"qualification", "execution-needs-review", "wrong-confirmation", "changed-operator", "changed-kubeconfig"} {
		t.Run(mode, func(t *testing.T) {
			f, workspace, log, requests, mu := openBaoCommandContractFixture(t)
			// Leave the genuine accepted foundation intact. Device recovery may not
			// treat it as the much later Core/Apps/expired-session source generation.
			f.parent.Journal.Entries = f.parent.Journal.Entries[:len(f.parent.Journal.Entries)-1]
			if err := f.parent.save(); err != nil {
				t.Fatal(err)
			}
			args := []string{"--workspace=" + workspace, "--environment=dev", "--parent-plan-sha256=" + f.sha}
			switch mode {
			case "execution-needs-review":
				args = append(args, "--execute", "--confirm=video-cloud-dev", "--review-sha256="+strings.Repeat("a", 64))
			case "wrong-confirmation":
				args = append(args, "--execute", "--confirm=another-stack", "--review-sha256="+strings.Repeat("a", 64))
			case "changed-operator":
				if err := os.WriteFile(filepath.Join(f.store.Root, "operator/env/LINODE_TOKEN"), []byte("changed-isolated-token"), 0600); err != nil {
					t.Fatal(err)
				}
			case "changed-kubeconfig":
				if err := os.WriteFile(f.store.KubeconfigPath(), []byte("changed"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			var out, diagnostics bytes.Buffer
			err := runEnvironmentCreateDeviceRootCRLRecoveryContext(context.Background(), args, &out, &diagnostics)
			if err == nil {
				t.Fatal("incomplete original source admitted")
			}
			expected := ""
			switch mode {
			case "qualification":
				expected = "source"
			case "execution-needs-review":
				expected = "immutable-qualified-source"
			case "wrong-confirmation":
				expected = "confirmation"
			case "changed-operator":
				expected = "original-configuration-and-operator"
			case "changed-kubeconfig":
				expected = "original-selected-kubeconfig"
			}
			if !strings.Contains(err.Error(), expected) {
				t.Fatalf("did not reach intended real command boundary %s: %v", expected, err)
			}
			after := createBootstrapStateSnapshot(t, f.store.Root)
			for path, want := range before {
				if path != "deployment" && after[path] != want {
					t.Fatal("refused command changed original evidence", path)
				}
			}
			calls, _ := os.ReadFile(log)
			mu.Lock()
			count := len(*requests)
			mu.Unlock()
			if len(calls) != 0 || count != 0 {
				t.Fatal("incomplete local source reached external mutation boundary", string(calls), count)
			}
			if strings.Contains(out.String()+diagnostics.String(), "isolated-original-root") {
				t.Fatal("private original authentication exposed")
			}
			lock, err := deploymentplan.LockExecutionOwner(filepath.Join(f.store.Root, "deployment/.execution-active"), false)
			if err != nil {
				t.Fatal("refused command retained owner lock", err)
			}
			lock.Close()
		})
	}
}
