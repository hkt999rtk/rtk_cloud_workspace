package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestCRLExecutionContractActiveTransportCommandKeepsOriginalAdmission(t *testing.T) {
	for _, mode := range []string{"incomplete-foundation", "missing-review", "unreviewed-execution", "changed-operator", "changed-kubeconfig", "completed-pointer", "report-outside-store"} {
		t.Run(mode, func(t *testing.T) {
			f, workspace, log, requests, mu := openBaoCommandContractFixture(t)
			f.parent.Journal.Entries = f.parent.Journal.Entries[:len(f.parent.Journal.Entries)-1]
			if err := f.parent.save(); err != nil {
				t.Fatal(err)
			}
			// Check the positive original-owner prerequisite independently before
			// exercising the command's stricter managed-transport admission boundary.
			e, a, err := loadActiveTransportExecution(f.cfg, f.store, f.sha)
			if err != nil {
				t.Fatal("original provider/foundation source unavailable", err)
			}
			if deploymentplan.Digest(a) != deploymentplan.Digest(f.artifact) {
				t.Fatal("foreign original artifact")
			}
			e.Close()
			args := []string{"--workspace=" + workspace, "--environment=dev", "--parent-plan-sha256=" + f.sha}
			switch mode {
			case "missing-review":
				args = append(args, "--execute", "--confirm=video-cloud-dev")
			case "unreviewed-execution":
				args = append(args, "--execute", "--confirm=video-cloud-dev", "--review-sha256="+strings.Repeat("a", 64))
			case "changed-operator":
				if err := f.store.write("operator/env/LINODE_TOKEN", []byte("changed-isolated-original"), true); err != nil {
					t.Fatal(err)
				}
			case "changed-kubeconfig":
				if err := os.WriteFile(f.store.KubeconfigPath(), []byte("changed-isolated-kubeconfig"), 0600); err != nil {
					t.Fatal(err)
				}
			case "completed-pointer":
				if err := f.store.write(filepath.Join("deployment/create/material", f.sha, "active-transport-renewal.json"), []byte("retained-opaque-completion"), false); err != nil {
					t.Fatal(err)
				}
			case "report-outside-store":
				args = append(args, "--report="+filepath.Join(t.TempDir(), "report.json"))
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			var out, diagnostics bytes.Buffer
			err = runEnvironmentCreateActiveTransportRenewalContext(t.Context(), args, &out, &diagnostics)
			if err == nil {
				t.Fatal("underqualified original source admitted")
			}
			if mode == "completed-pointer" && !strings.Contains(err.Error(), "already completed") {
				t.Fatal("completed source did not block replay", err)
			}
			after := createBootstrapStateSnapshot(t, f.store.Root)
			for path, sha := range before {
				if path != "deployment" && after[path] != sha {
					t.Fatal("refused command changed original evidence", path)
				}
			}
			calls, _ := os.ReadFile(log)
			mu.Lock()
			count := len(*requests)
			mu.Unlock()
			if len(calls) != 0 || count != 0 {
				t.Fatal("underqualified source reached external adapter", string(calls), count)
			}
			lock, err := deploymentplan.LockExecutionOwner(filepath.Join(f.store.Root, "deployment/.execution-active"), false)
			if err != nil {
				t.Fatal("refused command retained process lock", err)
			}
			lock.Close()
		})
	}
}

func TestCRLExecutionContractActiveTransportMountedOriginalKey(t *testing.T) {
	for _, mode := range []string{"original", "reconciled-current", "current-without-admission", "different-key", "wrong-mounted-key", "mixed-old-new", "malformed-native-output"} {
		t.Run(mode, func(t *testing.T) {
			f := newCRLExecutorFixture(t)
			initial, err := createEnvironmentTransport(f.store, f.execution.Journal.PlanSHA256, "openbao", map[string][]string{"openbao": {"openbao.isolated.svc"}}, time.Now().Add(-4*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			old, key, err := renewInitialTransportMaterial(initial, time.Now().Add(-2*time.Hour))
			if err != nil {
				t.Fatal(err)
			}
			current, err := renewActiveTransportSameKey(old, key, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			selected := old
			if mode == "reconciled-current" || mode == "current-without-admission" {
				selected = current
			}
			fields := []string{environmentPreflightBytesSHA256([]byte(selected.Identities["openbao"].Certificate)), "/openbao/transport/tls.crt", environmentPreflightBytesSHA256([]byte(selected.CACert)), "/openbao/transport/ca.crt", environmentPreflightBytesSHA256([]byte(old.Identities["openbao"].Key)), "/openbao/transport/tls.key"}
			switch mode {
			case "different-key":
				v := current.Identities["openbao"]
				v.Key = "foreign"
				current.Identities["openbao"] = v
			case "wrong-mounted-key":
				fields[4] = deploymentplan.Digest("foreign")
			case "mixed-old-new":
				fields[2] = environmentPreflightBytesSHA256([]byte(current.CACert))
			case "malformed-native-output":
				fields = fields[:4]
			}
			state := f.state(t)
			state.MountedDigests = strings.Join(fields, " ")
			f.writeState(t, state)
			before := deploymentplan.Digest(state)
			err = verifyActiveTransportMounted(t.Context(), f.cfg, f.backend, old, current, mode == "reconciled-current")
			if mode == "original" || mode == "reconciled-current" {
				if err != nil {
					t.Fatal("same original mounted key rejected", err)
				}
			} else if err == nil {
				t.Fatal("unqualified mounted transport accepted")
			}
			if deploymentplan.Digest(f.state(t)) != before {
				t.Fatal("mounted proof changed native state")
			}
		})
	}
}

func TestCRLExecutionContractDeviceCompletedConsumerEvidence(t *testing.T) {
	// The same final Device consumer proof must be re-observed at publication;
	// a prior successful observation does not authorize stale receipt evidence.
	f, p, signed, _ := crlDeviceConsumerExecutionFixture(t)
	j := createDeviceRootCRLJournal{Signed: signed, Evidence: map[string]string{}}
	for _, target := range p.Consumers {
		proof, err := observeDeviceRootCRLConsumer(t.Context(), f.cfg, f.store, f.execution, f.backend, p, target, signed)
		if err != nil {
			t.Fatal(err)
		}
		for k, v := range proof {
			j.Evidence[k] = v
		}
	}
	if err := observeDeviceRootCRLCompletedConsumers(t.Context(), f.cfg, f.store, f.execution, f.backend, p, j); err != nil {
		t.Fatal("current completed consumers rejected", err)
	}
	j.Evidence["consumer-owner-"+p.Consumers[1].ID] = deploymentplan.Digest("foreign")
	if err := observeDeviceRootCRLCompletedConsumers(t.Context(), f.cfg, f.store, f.execution, f.backend, p, j); err == nil {
		t.Fatal("changed completed evidence accepted")
	}
	// A cached success also cannot survive a real native read failure.
	t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(t.TempDir(), "absent-kubectl"))
	if err := observeDeviceRootCRLCompletedConsumers(t.Context(), f.cfg, f.store, f.execution, f.backend, p, j); err == nil {
		t.Fatal("unavailable native observation accepted")
	}
}
