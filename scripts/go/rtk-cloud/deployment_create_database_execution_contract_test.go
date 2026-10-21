package main

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type recoveryAcceptanceFailureContract struct {
	cfg     deploymentConfig
	store   secretStore
	e       *environmentCreateExecution
	p       createFoundationRecoveryPlan
	child   *deploymentplan.Plan
	journal deploymentplan.Journal
	native  *normalTailE2EBackend
}

func newRecoveryAcceptanceFailureContract(t *testing.T, stage string, definitions []deploymentplan.Definition, foundation ...[]deploymentplan.Definition) *recoveryAcceptanceFailureContract {
	t.Helper()
	cfg, store, operatorSHA, artifact := createStateFixture(t, "dev")
	e, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operatorSHA, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { e.Close() })
	p := createFoundationRecoveryPlan{ParentSHA: e.Journal.PlanSHA256, Plan: environmentCreatePlan{Environment: "dev", Stack: cfg.Values["CLOUD_STACK_NAME"], ConfigurationSHA256: deploymentplan.Digest(cfg)}, Materials: map[string]string{}}
	native := &normalTailE2EBackend{e: e, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}, routes: map[string]string{}}
	check := func(ctx context.Context, _ *deploymentplan.Plan) error { return e.Check(ctx) }
	if len(foundation) == 1 {
		initial, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest("isolated original release"), BindingsSHA256: environmentCreateStageBindingsSHA("foundation", foundation[0]), CreateParentSHA256: p.ParentSHA}, foundation[0], nil)
		if err != nil {
			t.Fatal(err)
		}
		if err := executeEnvironmentCreateStagePlan(context.Background(), store, e, "foundation", initial, native, check, check, check, e.Check, io.Discard); err != nil {
			t.Fatal(err)
		}
	}
	child, err := deploymentplan.Compile(deploymentplan.Inputs{Environment: "dev", Stack: p.Plan.Stack, Operation: "scoped", ConfigurationSHA256: p.Plan.ConfigurationSHA256, ReleaseSHA256: deploymentplan.Digest("isolated original release"), BindingsSHA256: environmentCreateStageBindingsSHA(stage, definitions), CreateParentSHA256: p.ParentSHA}, definitions, nil)
	if err != nil {
		t.Fatal(err)
	}
	intent, err := environmentCreateKubernetesStageIntent(child)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.BeginEntry("kubernetes/"+stage, "bootstrap", intent, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	reject := func(context.Context, *deploymentplan.Plan) error {
		return &deploymentplan.Blocker{ID: "deployment.post-deploy.workloads", Cause: "isolated acceptance interruption after original writes"}
	}
	if err := child.Execute(context.Background(), native, child.SHA256(), store.Root, check, check, reject); err == nil {
		t.Fatal("fixture did not stop at the intended acceptance boundary")
	}
	var journal deploymentplan.Journal
	if err := readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", child.SHA256(), "journal.json"), &journal); err != nil {
		t.Fatal(err)
	}
	if journal.Accepted || !journal.AcceptanceStarted || journal.Failed != "deployment.post-deploy.workloads" || len(journal.Owners) != len(definitions) || len(journal.Completed) != len(definitions) || native.held {
		t.Fatal("executor did not preserve the complete original write boundary")
	}
	return &recoveryAcceptanceFailureContract{cfg, store, e, p, child, journal, native}
}

func (f *recoveryAcceptanceFailureContract) saveJournal(t *testing.T) {
	t.Helper()
	if err := f.store.write(filepath.Join("deployment/rollback", f.child.SHA256(), "journal.json"), mustProviderRecoveryJSON(f.journal), true); err != nil {
		t.Fatal(err)
	}
}

func databaseSecretsExecutionContract(t *testing.T) (*recoveryAcceptanceFailureContract, map[string]string) {
	t.Helper()
	passwords := map[string]string{"controller": "isolated-controller:p@ss", "issuer": "isolated-issuer?grant", "verifier": "isolated-verifier+read"}
	var defs []deploymentplan.Definition
	for _, role := range []string{"controller", "issuer", "verifier"} {
		dsn := "postgres://" + url.UserPassword("rtk_pki_"+role+"_login", passwords[role]).String() + "@postgresql.video-cloud-dev-platform.svc:5432/video_cloud?sslmode=disable"
		defs = append(defs, deploymentplan.Definition{Phase: "configuration", Object: deploymentplan.Object{
			"apiVersion": "v1", "kind": "Secret", "metadata": deploymentplan.Object{"name": "pki-" + role + "-database", "namespace": "video-cloud-dev-video-cloud"},
			"data": deploymentplan.Object{"url": base64.StdEncoding.EncodeToString([]byte(dsn))},
		}})
	}
	f := newRecoveryAcceptanceFailureContract(t, "database-runtime-secrets", defs)
	for role, password := range passwords {
		if err := f.store.write(filepath.Join("deployment/create/material", f.p.ParentSHA, "database", role+".password"), []byte(password), false); err != nil {
			t.Fatal(err)
		}
	}
	return f, passwords
}

func TestDatabaseSecretsExecutionContractPreservesActualAcceptedWritesAndPasswords(t *testing.T) {
	f, passwords := databaseSecretsExecutionContract(t)
	before := createBootstrapStateSnapshot(t, f.store.Root)
	child, journal, actual, err := loadCreateDatabaseSecretsCheckpoint(f.store, f.e, &f.p)
	if err != nil || child.SHA256() != f.child.SHA256() || deploymentplan.Digest(journal) != deploymentplan.Digest(f.journal) || !reflect.DeepEqual(actual, passwords) {
		t.Fatal("original credential write checkpoint not recovered", err)
	}
	if f.p.CheckpointPlanSHA != child.SHA256() || f.p.CheckpointJournalSHA != deploymentplan.Digest(journal) || len(f.p.Materials) != 3 {
		t.Fatal("recovery omitted original write or password fingerprints")
	}
	if !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, f.store.Root)) {
		t.Fatal("reading original credential checkpoint changed original passwords or receipts")
	}
}

func TestDatabaseSecretsExecutionContractRejectsUnknownOutcomeAndCredentialDrift(t *testing.T) {
	for _, mode := range []string{"missing-lease", "changed-intent", "changed-plan", "accepted", "not-at-acceptance", "in-flight", "wrong-failure", "missing-owner", "missing-completion", "missing-password", "changed-password", "invalid-password"} {
		t.Run(mode, func(t *testing.T) {
			f, _ := databaseSecretsExecutionContract(t)
			if _, _, _, err := loadCreateDatabaseSecretsCheckpoint(f.store, f.e, &f.p); err != nil {
				t.Fatal("intact fixture must qualify before tampering", err)
			}
			passwordPath := filepath.Join("deployment/create/material", f.p.ParentSHA, "database/controller.password")
			switch mode {
			case "missing-lease":
				f.e.Journal.KubernetesLeases = nil
			case "changed-intent":
				f.e.Journal.Entries[len(f.e.Journal.Entries)-1].IntentSHA256 = deploymentplan.Digest("foreign request")
			case "changed-plan":
				f.journal.PlanSHA256 = deploymentplan.Digest("foreign plan")
			case "accepted":
				f.journal.Accepted = true
			case "not-at-acceptance":
				f.journal.AcceptanceStarted = false
			case "in-flight":
				f.journal.InFlight = f.child.Resources[0].ID
			case "wrong-failure":
				f.journal.Failed = f.child.Resources[0].ID
			case "missing-owner":
				delete(f.journal.Owners, f.child.Resources[0].ID)
			case "missing-completion":
				f.journal.Completed = f.journal.Completed[:2]
			case "missing-password":
				if err := os.Remove(filepath.Join(f.store.Root, passwordPath)); err != nil {
					t.Fatal(err)
				}
			case "changed-password", "invalid-password":
				body := "different-original-password"
				if mode == "invalid-password" {
					body += "\n"
				}
				if err := f.store.write(passwordPath, []byte(body), true); err != nil {
					t.Fatal(err)
				}
			}
			f.saveJournal(t)
			if err := f.e.save(); err != nil {
				t.Fatal(err)
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			if _, _, _, err := loadCreateDatabaseSecretsCheckpoint(f.store, f.e, &f.p); err == nil {
				t.Fatal("unknown or altered original credential write was accepted")
			}
			if !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, f.store.Root)) {
				t.Fatal("refusal replaced missing identity or changed original history")
			}
		})
	}
}

func TestDatabaseLoginRecoveryAuthenticatesEveryOriginalPasswordAndExactPrivileges(t *testing.T) {
	for _, repaired := range []bool{false, true} {
		for _, mode := range []string{"valid", "roles", "groups", "controller", "issuer", "verifier", "memberships", "authentication-failure", "malformed"} {
			t.Run(fmt.Sprint(repaired)+"/"+mode, func(t *testing.T) {
				store := makeIsolatedTestSecretStore(t, "dev")
				passwords := map[string]string{"controller": "controller-original", "issuer": "issuer-original", "verifier": "verifier-original"}
				result := map[string]any{"roles": 3, "groups": 3, "controller": true, "issuer": true, "verifier": repaired, "memberships": 2}
				if repaired {
					result["memberships"] = 3
				}
				switch mode {
				case "roles", "groups", "memberships":
					result[mode] = 4
				case "controller", "issuer":
					result[mode] = false
				case "verifier":
					result[mode] = !repaired
				}
				dir := t.TempDir()
				response := filepath.Join(dir, "response.json")
				body := mustProviderRecoveryJSON(result)
				if mode == "malformed" {
					body = []byte("unreadable-native-result")
				}
				if err := os.WriteFile(response, body, 0600); err != nil {
					t.Fatal(err)
				}
				callLog, stdinLog := filepath.Join(dir, "calls"), filepath.Join(dir, "stdin")
				script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> \"$RTK_DATABASE_CONTRACT_CALLS\"\ncat > \"$RTK_DATABASE_CONTRACT_STDIN\"\n"
				if mode == "authentication-failure" {
					script += "exit 1\n"
				} else {
					script += "cat \"$RTK_DATABASE_CONTRACT_RESPONSE\"\n"
				}
				if err := os.WriteFile(filepath.Join(dir, "kubectl"), []byte(script), 0700); err != nil {
					t.Fatal(err)
				}
				t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
				t.Setenv("RTK_CLOUD_KUBECTL", filepath.Join(dir, "kubectl"))
				t.Setenv("RTK_DATABASE_CONTRACT_CALLS", callLog)
				t.Setenv("RTK_DATABASE_CONTRACT_STDIN", stdinLog)
				t.Setenv("RTK_DATABASE_CONTRACT_RESPONSE", response)
				cfg := deploymentConfig{Environment: "dev", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}}
				backend := environmentKubernetesBackend{Store: store, KubeconfigContent: []byte("isolated frozen database access")}
				err := verifyCreateDatabaseLogins(context.Background(), cfg, backend, passwords, repaired)
				if (err == nil) != (mode == "valid") {
					t.Fatal("unexpected native login qualification", err)
				}
				input, err := os.ReadFile(stdinLog)
				if err != nil || string(input) != "controller-original\nissuer-original\nverifier-original\n" {
					t.Fatal("did not authenticate all three original passwords through private stdin")
				}
				calls, err := os.ReadFile(callLog)
				if err != nil {
					t.Fatal(err)
				}
				for role, password := range passwords {
					if strings.Contains(string(calls), password) || !strings.Contains(string(calls), "-U rtk_pki_"+role+"_login") {
						t.Fatal("native password leaked or authentication was omitted", role)
					}
				}
				if strings.Contains(string(calls), "ALTER ROLE") || strings.Contains(string(calls), "GRANT ") || strings.Contains(string(calls), "CREATE ROLE") {
					t.Fatal("read-only credential qualification changed database privileges")
				}
			})
		}
	}
}
