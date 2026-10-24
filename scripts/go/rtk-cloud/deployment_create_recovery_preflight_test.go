package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func recoveryPreflightFixture(t *testing.T) (deploymentConfig, secretStore, createRecoveryCheckpoint, unifiedDeploymentOptions, map[string][]byte) {
	t.Helper()
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RTK_CLOUD_CONFIG_ROOT", root)
	store, err := newSecretStore("", "dev")
	if err != nil {
		t.Fatal(err)
	}
	cfg := deploymentConfig{Environment: "dev", Workspace: "/fixture/workspace", Values: map[string]string{"CLOUD_STACK_NAME": "video-cloud-dev"}}
	parent := strings.Repeat("a", 64)
	o := environmentCreateOwner{Schema: environmentCreateOwnerSchema, Environment: "dev", Stack: "video-cloud-dev", PlanSHA256: parent, OwnerToken: strings.Repeat("b", 64), Journal: "deployment/create/executions/" + parent + "/execution-fixture/journal.json"}
	j := environmentCreateJournal{Schema: environmentCreateJournalSchema, Environment: o.Environment, Stack: o.Stack, PlanSHA256: parent, OwnerToken: o.OwnerToken, Entries: []environmentCreateJournalEntry{{Key: "openbao-initialize", State: "completed", CompletedAt: time.Now().Add(-time.Hour)}, {Key: "kubernetes/service-issuer-server-enrollment", State: "pending", IntentSHA256: strings.Repeat("c", 64)}, {Key: "kubernetes/service-session-original-key-renewal-" + strings.Repeat("d", 64), State: "pending", IntentSHA256: strings.Repeat("e", 64)}}}
	files := map[string][]byte{"openbao/root-token": []byte("original-fixture-root-token"), "pki/custody/roots/original/ca-key.encrypted.pem": []byte("original-fixture-encrypted-key")}
	proofPath := "deployment/repairs/account-email-format-fixture/verified.json"
	proofRaw, _ := json.Marshal(map[string]any{"schema": "rtk-cloud.initial-email-key-format-repair/v1", "environment": "dev", "parent_plan_sha256": parent, "completed": true, "only_email_secret_field_changed": true})
	files[proofPath] = proofRaw
	receipt, _ := json.Marshal(map[string]string{"email_key_repair_proof_sha256": environmentPreflightBytesSHA256(proofRaw)})
	j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: "kubernetes/account-bootstrap", State: "completed", CompletedAt: time.Now().Add(-time.Hour), Receipt: receipt})
	files["deployment/.execution-active/owner.json"], err = json.Marshal(o)
	if err != nil {
		t.Fatal(err)
	}
	files[o.Journal], err = json.Marshal(j)
	if err != nil {
		t.Fatal(err)
	}
	for name, raw := range files {
		path := filepath.Join(store.Root, name)
		if os.MkdirAll(filepath.Dir(path), 0700) != nil || os.WriteFile(path, raw, 0600) != nil {
			t.Fatal("fixture write failed")
		}
	}
	c, err := inspectCreateRecoveryCheckpoint(cfg, store)
	if err != nil {
		t.Fatal(err)
	}
	options := unifiedDeploymentOptions{Action: "preflight", Operation: "create", Environment: "dev", Workspace: cfg.Workspace, Timeout: 45 * time.Minute, ParentPlanSHA256: parent, EmailKeyRepairProof: filepath.Join(store.Root, "deployment/repairs/account-email-format-fixture/verified.json")}
	return cfg, store, c, options, files
}

func assertRecoveryOriginalFiles(t *testing.T, store secretStore, files map[string][]byte) {
	t.Helper()
	for name, want := range files {
		actual, err := os.ReadFile(filepath.Join(store.Root, name))
		if err != nil || !bytes.Equal(actual, want) {
			t.Fatal("original fence, journal or identity changed", name)
		}
	}
}

func TestCreateRecoveryPreflightDispatchPreservesOriginalIdentityAndFullEvidence(t *testing.T) {
	for _, mode := range []string{"go", "automatic-go", "missing-gate", "duplicate-gate", "stale-go", "native-failure"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, c, o, files := recoveryPreflightFixture(t)
			recordedProof := o.EmailKeyRepairProof
			if mode == "automatic-go" {
				o.EmailKeyRepairProof = ""
				o.ParentPlanSHA256 = ""
			}
			var out bytes.Buffer
			report := unifiedDeploymentReport{Environment: "dev", Decision: "NO-GO"}
			calls := 0
			run := func(ctx context.Context, args []string, out, diagnostics io.Writer) error {
				calls++
				values := map[string]string{}
				for index := 0; index < len(args); index++ {
					name := args[index]
					if name == "--execute" || name == "--confirm" || name == "--review-sha256" {
						t.Fatal("preflight forwarded execution")
					}
					if name == "--after-bootstrap-renewal-permissions" {
						continue
					}
					index++
					if index >= len(args) {
						t.Fatal("missing argument")
					}
					values[name] = args[index]
				}
				if values["--parent-plan-sha256"] != c.Owner.PlanSHA256 || values["--email-key-repair-proof"] != recordedProof || values["--workspace"] != cfg.Workspace || !strings.Contains(strings.Join(args, " "), "--after-bootstrap-renewal-permissions") {
					t.Fatal("original checkpoint not selected")
				}
				var pending struct{ Decision string }
				if readExecutionJSON(values["--report"], &pending) == nil {
					t.Fatal("pending sentinel unexpectedly lacks its schema")
				}
				raw, err := os.ReadFile(values["--report"])
				if err != nil || !bytes.Contains(raw, []byte(`"decision":"NO-GO"`)) {
					t.Fatal("old GO not invalidated")
				}
				if mode == "native-failure" {
					return &deploymentplan.Blocker{ID: "deployment.create.recovery", Cause: "original executor is running", Next: "wait for the original owner; never remove its fence"}
				}
				checker, err := environmentPreflightCheckerSHA256()
				if err != nil {
					t.Fatal(err)
				}
				p := createFoundationRecoveryPlan{ParentSHA: c.Owner.PlanSHA256, ParentOwnerSHA: deploymentplan.Digest(c.Owner), ParentJournalSHA: deploymentplan.Digest(c.Journal), CheckerSHA: checker}
				sha := deploymentplan.Digest(p)
				q := deploymentplan.Qualification{PlanSHA256: sha, Decision: "GO"}
				for _, id := range []string{"deployment.openbao.target", "deployment.openbao.release", "deployment.openbao.storage", "deployment.openbao.bootstrap", "deployment.openbao.network", "deployment.openbao.inputs"} {
					q.Gates = append(q.Gates, deploymentplan.GateResult{ID: id, Required: true, Status: "PASS", EvidenceSHA256: sha})
				}
				if mode == "missing-gate" {
					q.Gates = q.Gates[:5]
				}
				if mode == "duplicate-gate" {
					q.Gates[5] = q.Gates[0]
				}
				now := time.Now().UTC()
				if mode == "stale-go" {
					now = now.Add(-time.Minute)
				}
				a := createFoundationRecoveryArtifact{Schema: "rtk-cloud.create-openbao-recovery-artifact/v1", Plan: p, Qualification: q, QualifiedAt: now, ValidUntil: now.Add(15 * time.Minute)}
				raw, err = json.Marshal(a)
				if err != nil || os.WriteFile(values["--report"], raw, 0600) != nil {
					t.Fatal("qualification fixture failed")
				}
				return nil
			}
			err := runCreateRecoveryPreflightWithRunner(context.Background(), cfg, o, &report, &out, &out, run)
			if calls != 1 {
				t.Fatal("native full qualifier not called once")
			}
			if mode == "go" || mode == "automatic-go" {
				if err != nil || report.Decision != "GO" || len(report.Gates) != 6 || report.Completed || !strings.Contains(out.String(), "operation=create scope=original-generation-recovery completed=false") {
					t.Fatal("complete recovery preflight not reported", err)
				}
				var a createFoundationRecoveryArtifact
				if readExecutionJSON(filepath.Join(store.Root, "deployment/reports/create-recovery-preflight.json"), &a) != nil || a.Schema != "rtk-cloud.create-openbao-recovery-artifact/v1" {
					t.Fatal("full resumable qualification overwritten by summary")
				}
			} else if err == nil || report.Decision != "NO-GO" || strings.Contains(out.String(), "\nGO environment=") {
				t.Fatal("partial or stale proof accepted", mode)
			}
			if mode == "native-failure" && !strings.Contains(out.String(), "original executor is running") {
				t.Fatal("native ownership diagnostic lost")
			}
			assertRecoveryOriginalFiles(t, store, files)
		})
	}
}

func TestCreateRecoveryPreflightRejectsUnknownOwnershipAndExecution(t *testing.T) {
	for _, mode := range []string{"foreign-parent", "missing-proof", "execute-action", "confirm", "execution-plan", "cancelled", "unknown-checkpoint", "receipt-without-completion", "duplicate-entry", "foreign-owner", "escaped-journal", "symlink-owner", "public-owner"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, c, o, files := recoveryPreflightFixture(t)
			ctx := context.Background()
			switch mode {
			case "foreign-parent":
				o.ParentPlanSHA256 = strings.Repeat("f", 64)
			case "missing-proof":
				o.EmailKeyRepairProof = ""
				if err := os.Remove(filepath.Join(store.Root, "deployment/repairs/account-email-format-fixture/verified.json")); err != nil {
					t.Fatal(err)
				}
				delete(files, "deployment/repairs/account-email-format-fixture/verified.json")
			case "execute-action":
				o.Action = "create"
			case "confirm":
				o.Confirm = "video-cloud-dev"
			case "execution-plan":
				o.PlanSHA256 = strings.Repeat("f", 64)
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			case "unknown-checkpoint":
				c.Journal.Entries[2].Key = "PRIVATE_UNKNOWN_CHECKPOINT_VALUE"
			case "receipt-without-completion":
				c.Journal.Entries[2].Receipt = json.RawMessage(`{"uncertain":true}`)
			case "duplicate-entry":
				c.Journal.Entries = append(c.Journal.Entries, c.Journal.Entries[2])
			case "foreign-owner":
				c.Owner.OwnerToken = strings.Repeat("f", 64)
			case "escaped-journal":
				c.Owner.Journal = "../../another-environment/journal.json"
			case "symlink-owner":
				path := filepath.Join(store.Root, "deployment/.execution-active/owner.json")
				if os.Rename(path, path+".original") != nil || os.Symlink(path+".original", path) != nil {
					t.Fatal("symlink fixture failed")
				}
			case "public-owner":
				if os.Chmod(filepath.Join(store.Root, "deployment/.execution-active/owner.json"), 0644) != nil {
					t.Fatal("mode fixture failed")
				}
			}
			if mode == "unknown-checkpoint" || mode == "receipt-without-completion" || mode == "duplicate-entry" {
				raw, _ := json.Marshal(c.Journal)
				path := filepath.Join(store.Root, c.Owner.Journal)
				os.WriteFile(path, raw, 0600)
				files[c.Owner.Journal] = raw
			}
			if mode == "foreign-owner" || mode == "escaped-journal" {
				raw, _ := json.Marshal(c.Owner)
				os.WriteFile(filepath.Join(store.Root, "deployment/.execution-active/owner.json"), raw, 0600)
				files["deployment/.execution-active/owner.json"] = raw
			}
			var out bytes.Buffer
			report := unifiedDeploymentReport{Environment: "dev"}
			err := runCreateRecoveryPreflightWithRunner(ctx, cfg, o, &report, &out, &out, func(context.Context, []string, io.Writer, io.Writer) error {
				t.Fatal("unsafe checkpoint reached native qualification")
				return nil
			})
			if err == nil || report.Decision != "NO-GO" || bytes.Contains(out.Bytes(), []byte("PRIVATE_UNKNOWN_CHECKPOINT_VALUE")) {
				t.Fatal("unsafe recovery or private diagnostic accepted")
			}
			assertRecoveryOriginalFiles(t, store, files)
		})
	}
}

func TestCreateRecoveryCheckpointHintUsesFormalScriptAndRetainsFence(t *testing.T) {
	cfg, store, _, _, files := recoveryPreflightFixture(t)
	err := createCheckpointPreflightHint(cfg, store)
	if !strings.Contains(err.Error(), "checkpoint=bootstrap-renewal-permissions") || !strings.Contains(err.Error(), "--operation create") || strings.Contains(err.Error(), "--execute") {
		t.Fatal("formal checkpoint parameters missing")
	}
	assertRecoveryOriginalFiles(t, store, files)
}

func TestCreateRecoveryPreflightOperationIsOnlyReachableThroughCompletePreflight(t *testing.T) {
	good := []string{"--environment", "dev", "--phase", "pre-deploy", "--operation", "create"}
	if _, err := parseDeploymentCheckOptions(good, io.Discard); err != nil {
		t.Fatal(err)
	}
	for _, bad := range [][]string{{"--execute"}, {"--execute=false"}, {"--confirm", "video-cloud-dev"}, {"--fast"}, {"--checks", "linode"}, {"--image", "foreign"}, {"--grant-object-storage-bucket-access"}, {"--read-only=false"}, {"--parent-plan-sha256", "wrong"}, {"--phase", "post-deploy"}} {
		if _, err := parseDeploymentCheckOptions(append(slices.Clone(good), bad...), io.Discard); err == nil {
			t.Fatal("unsafe preflight options accepted", bad)
		}
	}
	for _, action := range []string{"create", "upgrade", "plan"} {
		if _, err := parseUnifiedDeploymentOptions(action, []string{"--environment", "dev", "--operation", "create", "--plan-sha256", strings.Repeat("a", 64), "--parent-plan-sha256", strings.Repeat("b", 64)}, io.Discard); err == nil {
			t.Fatal("recovery preflight exposed execution", action)
		}
	}
}

func TestCreateRecoveryPreflightUnifiedEntryNeverDispatchesFreshCreateOrRollout(t *testing.T) {
	cfg, _, _, _, _ := recoveryPreflightFixture(t)
	called := false
	deps := unifiedDeploymentDependencies{Resolve: func(string, string, string) (deploymentConfig, error) { return cfg, nil }, RecoveryPreflight: func(ctx context.Context, selected deploymentConfig, opts unifiedDeploymentOptions, report *unifiedDeploymentReport, out, diagnostics io.Writer) error {
		called = true
		if selected.Environment != "dev" || opts.Action != "preflight" || opts.Operation != "create" || opts.EmailKeyRepairProof != "" || opts.ParentPlanSHA256 != "" || ctx.Err() != nil {
			t.Fatal("formal recovery entry lost its selection")
		}
		report.Decision, report.Scope = "GO", "original-generation-recovery"
		return nil
	}, FreshCreate: func(context.Context, deploymentConfig, unifiedDeploymentOptions, *unifiedDeploymentReport, io.Writer, io.Writer) error {
		t.Fatal("recovery called fresh allocation")
		return nil
	}, Compile: func(context.Context, deploymentConfig, string) (*compiledEnvironmentDeployment, error) {
		t.Fatal("recovery fell back to ordinary upgrade")
		return nil, nil
	}, Execute: func(context.Context, *compiledEnvironmentDeployment, string, func(context.Context, *deploymentplan.Plan) error, func(context.Context, *compiledEnvironmentDeployment) error) error {
		t.Fatal("preflight executed deployment")
		return nil
	}}
	if err := runUnifiedEnvironmentDeploymentWithDependencies(context.Background(), "preflight", []string{"--environment", "dev", "--operation", "create"}, io.Discard, io.Discard, deps); err != nil || !called {
		t.Fatal("formal recovery preflight not dispatched", err)
	}
}

func TestCreateRecoveryAutomaticProofRequiresUniqueOriginalReceiptBinding(t *testing.T) {
	for _, mode := range []string{"original", "foreign-parent", "foreign-environment", "changed-bytes", "public-file", "symlink-file", "symlink-directory", "duplicate-proof", "missing-receipt", "wrong-explicit", "no-latest-fallback"} {
		t.Run(mode, func(t *testing.T) {
			_, store, c, o, _ := recoveryPreflightFixture(t)
			original := o.EmailKeyRepairProof
			raw, err := os.ReadFile(original)
			if err != nil {
				t.Fatal(err)
			}
			explicit := ""
			switch mode {
			case "foreign-parent", "foreign-environment":
				var p map[string]any
				json.Unmarshal(raw, &p)
				if mode == "foreign-parent" {
					p["parent_plan_sha256"] = strings.Repeat("f", 64)
				} else {
					p["environment"] = "staging"
				}
				raw, _ = json.Marshal(p)
				os.WriteFile(original, raw, 0600)
				for i := range c.Journal.Entries {
					if c.Journal.Entries[i].Key == "kubernetes/account-bootstrap" {
						c.Journal.Entries[i].Receipt = mustProviderRecoveryJSON(map[string]string{"email_key_repair_proof_sha256": environmentPreflightBytesSHA256(raw)})
					}
				}
			case "changed-bytes":
				os.WriteFile(original, append(raw, '\n'), 0600)
			case "public-file":
				os.Chmod(original, 0644)
			case "symlink-file":
				os.Rename(original, original+".original")
				os.Symlink(original+".original", original)
			case "symlink-directory":
				dir := filepath.Dir(original)
				preserved := filepath.Join(filepath.Dir(dir), "preserved-original")
				os.Rename(dir, preserved)
				os.Symlink(preserved, dir)
			case "duplicate-proof":
				other := filepath.Join(store.Root, "deployment/repairs/account-email-format-duplicate/verified.json")
				os.MkdirAll(filepath.Dir(other), 0700)
				os.WriteFile(other, raw, 0600)
			case "missing-receipt":
				for i := range c.Journal.Entries {
					if c.Journal.Entries[i].Key == "kubernetes/account-bootstrap" {
						c.Journal.Entries[i].Receipt = json.RawMessage(`{}`)
					}
				}
			case "wrong-explicit":
				explicit = filepath.Join(store.Root, "deployment/repairs/account-email-format-foreign/verified.json")
			case "no-latest-fallback":
				os.Remove(original)
				other := filepath.Join(store.Root, "deployment/repairs/account-email-format-newest/verified.json")
				os.MkdirAll(filepath.Dir(other), 0700)
				os.WriteFile(other, append(raw, '\n'), 0600)
			}
			selected, err := resolveRecordedCreateEmailProof(store, c, explicit)
			if mode == "original" {
				if err != nil || selected != original {
					t.Fatal("recorded original proof not resolved", err)
				}
			} else if err == nil {
				t.Fatal("unsafe evidence or alternate generation selected", mode)
			}
		})
	}
}

func TestCreatePreflightCheckpointDetectionDoesNotGuessAbsence(t *testing.T) {
	for _, mode := range []string{"fence", "unknown-owner", "fence-file", "symlink-fence", "absent"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, _, _, _ := recoveryPreflightFixture(t)
			path := filepath.Join(store.Root, "deployment/.execution-active")
			switch mode {
			case "unknown-owner":
				os.WriteFile(filepath.Join(path, "owner.json"), []byte(`{"unknown":true}`), 0600)
			case "fence-file":
				os.RemoveAll(path)
				os.WriteFile(path, []byte("unknown-owner"), 0600)
			case "symlink-fence":
				os.Rename(path, path+"-original")
				os.Symlink(path+"-original", path)
			case "absent":
				os.RemoveAll(path)
			}
			exists, err := createPreflightHasCheckpoint(cfg)
			if err != nil || exists != (mode != "absent") {
				t.Fatal("uncertain fence treated as empty target", mode, err)
			}
		})
	}
}

func TestCreateRecoveryCheckpointProfilesRequireExactPendingStages(t *testing.T) {
	_, _, c, _, _ := recoveryPreflightFixture(t)
	for _, test := range []struct {
		keys    []string
		profile string
	}{{[]string{"kubernetes/service-session"}, "service-session-order"}, {[]string{"kubernetes/service-issuer-server-enrollment"}, "expired-bootstrap-enrollment"}, {[]string{"kubernetes/service-issuer-server-enrollment", "kubernetes/service-session-original-key-renewal-" + strings.Repeat("d", 64)}, "bootstrap-renewal-permissions"}, {[]string{"kubernetes/service-issuer-server-enrollment", "kubernetes/service-session-original-key-renewal-unknown"}, ""}, {[]string{"kubernetes/service-session", "kubernetes/unknown-partial"}, ""}} {
		j := c.Journal
		j.Entries = slices.Clone(j.Entries[:1])
		for _, key := range test.keys {
			j.Entries = append(j.Entries, environmentCreateJournalEntry{Key: key, State: "pending", IntentSHA256: strings.Repeat("e", 64)})
		}
		profile, _ := createRecoveryCheckpointProfile(j)
		if profile != test.profile {
			t.Fatal("checkpoint selector admitted unsupported pending state", test.keys, profile)
		}
	}
}

func TestCreateRecoveryRecognizesOnlyFullyAcceptedCoreBoundary(t *testing.T) {
	j := environmentCreateJournal{Entries: []environmentCreateJournalEntry{{Key: "kubernetes/service-core-enrollment", State: "completed", CompletedAt: time.Now(), Receipt: json.RawMessage(`{"native":true}`)}}}
	profile, flag := createRecoveryCheckpointProfile(j)
	if profile != "accepted-service-core" || flag != "--after-service-core-enrollment" {
		t.Fatal("accepted core not selected")
	}
	for _, bad := range []environmentCreateJournalEntry{{Key: "kubernetes/unknown", State: "completed", CompletedAt: time.Now()}, {Key: "kubernetes/unknown", State: "pending", IntentSHA256: strings.Repeat("e", 64)}} {
		j.Entries = append(j.Entries[:1], bad)
		if profile, _ := createRecoveryCheckpointProfile(j); profile != "" {
			t.Fatal("uncertain subsequent writes admitted")
		}
	}
}
