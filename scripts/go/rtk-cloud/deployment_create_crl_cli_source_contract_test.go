package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type crlSourceContractCommand struct {
	name                                                 string
	run                                                  func(context.Context, []string, io.Writer, io.Writer) error
	extra                                                []string
	sourceError, artifactError, operatorError, kubeError string
}

func crlSourceContractCommands() []crlSourceContractCommand {
	digest := strings.Repeat("a", 64)
	service := "original Service Root CRL repair requires"
	return []crlSourceContractCommand{
		{name: "service", run: runEnvironmentCreateRootCRLRecoveryContext, sourceError: service, artifactError: service, operatorError: service, kubeError: service},
		{name: "service-installed", run: runEnvironmentCreateRootCRLInstalledContext, extra: []string{"--original-review-sha256=" + digest}, sourceError: service, artifactError: service, operatorError: service, kubeError: service},
		{name: "mqtt", run: runEnvironmentCreateMQTTRootCRLContext, sourceError: "completed-mqtt-tail", artifactError: "canonical-artifact", operatorError: "original-operator", kubeError: "canonical-kubeconfig"},
		{name: "openbao-tls", run: runEnvironmentCreateOpenBaoTLSRootCRLContext, sourceError: "completed-mqtt-tail", artifactError: "canonical-artifact", operatorError: "original-operator", kubeError: "canonical-kubeconfig"},
		{name: "device-completion", run: runEnvironmentCreateDeviceRootCRLRecoveryContext, extra: []string{"--complete-rotation-review-sha256=" + digest}, sourceError: "completion-original-source-and-inputs", artifactError: "completion-original-source-and-inputs", operatorError: "original-configuration-and-operator", kubeError: "original-selected-kubeconfig"},
		{name: "mqtt-completion", run: runEnvironmentCreateMQTTRootCRLCompletionContext, extra: []string{"--original-review-sha256=" + digest}, sourceError: "MQTT completion recovery requires its exact original four-step native checkpoint", artifactError: "MQTT completion recovery requires its exact original four-step native checkpoint"},
		{name: "app", run: runEnvironmentCreateAppRootCRLRecoveryContext, sourceError: "literal-completed-cleanup-checkpoint", artifactError: "qualified-artifact", operatorError: "original-operator-material", kubeError: "original-kubeconfig"},
		{name: "app-completion", run: runEnvironmentCreateAppRootCRLCompletionContext, extra: []string{"--original-review-sha256=" + digest}, sourceError: "App completion recovery requires its exact imported original CRL and bounded consumer checkpoint", artifactError: "App completion recovery requires its exact imported original CRL and bounded consumer checkpoint"},
	}
}

// A real accepted foundation is deliberately insufficient for these later CRL
// operations. This exercises the public coordinators without inventing a late
// history or a qualification. Both attempts must preserve all local state and
// release the original process lock before another operator can inspect it.
func TestCRLCommandsPreserveOriginalFoundationOnRefusalAndRetry(t *testing.T) {
	for _, command := range crlSourceContractCommands() {
		t.Run(command.name, func(t *testing.T) {
			// Reuse only immutable test setup; every invocation reopens the actual
			// files and locks. Restore each intentional tamper before the next case.
			f, workspace, log, requests, mu := openBaoCommandContractFixture(t)
			originalJournal := f.parent.Journal
			originalState := createBootstrapStateSnapshot(t, f.store.Root)
			modes := []string{"incomplete-source", "unadmitted-review", "foreign-parent", "pending-original-stage", "maintenance"}
			if command.operatorError != "" {
				modes = append(modes, "changed-operator", "changed-kubeconfig")
			}
			if command.name == "mqtt" || command.name == "openbao-tls" || command.name == "app" {
				modes = append(modes, "wrong-confirmation")
			}
			for _, mode := range modes {
				t.Run(mode, func(t *testing.T) {
					f.parent.Journal = originalJournal
					t.Cleanup(func() {
						f.parent.Journal = originalJournal
						if err := f.parent.save(); err != nil {
							t.Error(err)
						}
					})
					// The fixture's final audit-enable intent has not run. Retain
					// only its genuinely accepted foundation for the basic source;
					// the pending-stage case keeps that actual unfinished intent.
					if mode != "pending-original-stage" {
						f.parent.Journal.Entries = f.parent.Journal.Entries[:len(f.parent.Journal.Entries)-1]
					}
					if err := f.parent.save(); err != nil {
						t.Fatal(err)
					}
					args := []string{"--workspace=" + workspace, "--environment=dev", "--parent-plan-sha256=" + f.sha}
					args = append(args, command.extra...)
					want := command.sourceError
					switch mode {
					case "unadmitted-review":
						args = append(args, "--execute", "--confirm="+f.cfg.Values["CLOUD_STACK_NAME"], "--review-sha256="+strings.Repeat("b", 64))
						want = command.artifactError
					case "wrong-confirmation":
						args = append(args, "--execute", "--confirm=another-stack", "--review-sha256="+strings.Repeat("b", 64))
						want = "confirmation"
					case "changed-operator":
						crlSourceContractTamper(t, f.store, "operator/env/LINODE_TOKEN", []byte("isolated-changed-provider"))
						want = command.operatorError
					case "changed-kubeconfig":
						crlSourceContractTamper(t, f.store, "kube/kubeconfig.yaml", []byte("changed-isolated-kubeconfig"))
						want = command.kubeError
					case "foreign-parent":
						args[2] = "--parent-plan-sha256=" + strings.Repeat("f", 64)
						want = "original stopped dev owner differs"
						if strings.HasPrefix(command.name, "service") {
							want = "same-key temporary TLS repair requires"
						}
					case "pending-original-stage":
						if command.name != "mqtt-completion" && command.name != "app-completion" && !strings.HasPrefix(command.name, "service") {
							want = "application trust recovery permits only its exact refused enrollment checkpoint"
						}
					case "maintenance":
						crlSourceContractTamper(t, f.store, "recovery-command.lock", []byte("isolated maintenance owner"))
						want = "maintenance is active"
						if command.name == "mqtt" || command.name == "openbao-tls" || command.name == "app" {
							want = "mutation-guard"
						}
						if strings.HasPrefix(command.name, "service") {
							want = "original Service Root CRL repair requires"
						}
					}
					before := createBootstrapStateSnapshot(t, f.store.Root)
					var firstError string
					for attempt := 0; attempt < 2; attempt++ {
						var out, diagnostics bytes.Buffer
						err := command.run(context.Background(), args, &out, &diagnostics)
						if err == nil || !strings.Contains(err.Error(), want) {
							t.Fatalf("attempt %d did not reach %s: %v", attempt+1, want, err)
						}
						if attempt == 0 {
							firstError = err.Error()
						} else if err.Error() != firstError {
							t.Fatal("unchanged retry selected a different recovery boundary")
						}
						if !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, f.store.Root)) {
							t.Fatal("refusal changed original identities, history, candidate files or permissions")
						}
						if strings.Contains(out.String(), "GO environment=") {
							t.Fatal("refused source emitted executable admission")
						}
						for _, private := range []string{"isolated-original-root", "isolated-original-share", "isolated-provider-token", "isolated-original-admin-password", "synthetic-original-token"} {
							if strings.Contains(out.String()+diagnostics.String(), private) {
								t.Fatal("private input exposed by refusal")
							}
						}
						lock, err := deploymentplan.LockExecutionOwner(filepath.Join(f.store.Root, "deployment/.execution-active"), false)
						if err != nil {
							t.Fatal("refused command retained original process lock", err)
						}
						if err := lock.Close(); err != nil {
							t.Fatal(err)
						}
					}
					calls, err := os.ReadFile(log)
					if err != nil && !os.IsNotExist(err) {
						t.Fatal(err)
					}
					mu.Lock()
					count := len(*requests)
					mu.Unlock()
					if len(calls) != 0 || count != 0 {
						t.Fatal("refused source reached external signer, runtime or provider")
					}
				})
				if !reflect.DeepEqual(originalState, createBootstrapStateSnapshot(t, f.store.Root)) {
					t.Fatal("isolated test tamper was not restored before the next command attempt")
				}
			}
		})
	}
}

func crlSourceContractTamper(t *testing.T, store secretStore, relative string, changed []byte) {
	t.Helper()
	path := filepath.Join(store.Root, relative)
	original, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	existed := err == nil
	t.Cleanup(func() {
		if !existed {
			if err := os.Remove(path); err != nil {
				t.Error(err)
			}
		} else if err := store.write(relative, original, true); err != nil {
			t.Error(err)
		}
	})
	if err := store.write(relative, changed, existed); err != nil {
		t.Fatal(err)
	}
}

func TestCRLCommandArgumentBoundariesDoNotOpenSelectedEnvironment(t *testing.T) {
	for _, command := range crlSourceContractCommands() {
		t.Run(command.name, func(t *testing.T) {
			root := t.TempDir()
			t.Setenv("RTK_CLOUD_CONFIG_ROOT", root)
			base := []string{"--environment=dev", "--parent-plan-sha256=" + strings.Repeat("a", 64)}
			base = append(base, command.extra...)
			cases := [][]string{{"--environment=prod"}, {"--parent-plan-sha256=not-a-digest"}, {"--timeout=0s"}, {"--timeout=31m"}, {"--timeout=not-a-duration"}, {"--unknown-option=true"}, {"unselected-positional-input"}}
			if command.name != "service" {
				cases = append(cases, []string{"--review-sha256=" + strings.Repeat("c", 64)})
			}
			if command.name == "device-completion" {
				cases = append(cases, []string{"--execute", "--qualify"}, []string{"--complete-rotation-review-sha256=bad"})
			}
			if strings.HasSuffix(command.name, "installed") || command.name == "mqtt-completion" || command.name == "app-completion" {
				cases = append(cases, []string{"--original-review-sha256=bad"})
			}
			before := createBootstrapStateSnapshot(t, root)
			for _, suffix := range cases {
				var out, diagnostics bytes.Buffer
				args := append(append([]string(nil), base...), suffix...)
				if err := command.run(context.Background(), args, &out, &diagnostics); err == nil {
					t.Fatal("invalid command arguments admitted", suffix)
				}
				if out.Len() != 0 || !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, root)) {
					t.Fatal("invalid invocation opened or changed an environment", suffix)
				}
			}
		})
	}
}
