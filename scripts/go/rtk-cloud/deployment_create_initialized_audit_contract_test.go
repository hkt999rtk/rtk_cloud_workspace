package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

type initializedAuditContractFixture struct {
	cfg      deploymentConfig
	store    secretStore
	sha      string
	artifact environmentCreateProviderRecoveryArtifact
	child    *deploymentplan.Plan
	journal  deploymentplan.Journal
	parent   *environmentCreateExecution
	native   *normalTailE2EBackend
}

type initializedAuditContractInputs struct {
	cfg        deploymentConfig
	operator   map[string]string
	kubeconfig []byte
}

// The fixture records an isolated provider handoff through the real owner and
// journal writers. Its compiled child inventory models accepted foundation
// provenance; it never contacts a provider or reads a real environment store.
func newInitializedAuditContractFixture(t *testing.T, inputs ...initializedAuditContractInputs) *initializedAuditContractFixture {
	t.Helper()
	cfg, store, operatorSHA, initial := createStateFixture(t, "dev")
	provider, providerPlan := providerRecoveryJournalFixture()
	if len(inputs) == 1 {
		cfg = inputs[0].cfg
		operatorSHA = deploymentplan.Digest(inputs[0].operator)
		for key, value := range inputs[0].operator {
			if err := store.write(filepath.Join("operator/env", key), []byte(value), false); err != nil {
				t.Fatal(err)
			}
		}
		if err := store.write("kube/kubeconfig.yaml", inputs[0].kubeconfig, false); err != nil {
			t.Fatal(err)
		}
		var proof map[string]string
		if err := json.Unmarshal(provider.Entries[5].Receipt, &proof); err != nil {
			t.Fatal(err)
		}
		proof["kubeconfig_sha256"] = environmentPreflightBytesSHA256(inputs[0].kubeconfig)
		provider.Entries[5].Receipt = mustProviderRecoveryJSON(proof)
	}
	cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] = "standard"
	var plan environmentCreatePlan
	if err := json.Unmarshal(initial.Plan, &plan); err != nil {
		t.Fatal(err)
	}
	plan.Provider = providerPlan.Provider
	plan.ConfigurationSHA256, plan.OperatorSHA256 = deploymentplan.Digest(cfg), operatorSHA
	raw := mustProviderRecoveryJSON(plan)
	generation, err := invalidateEnvironmentPreflightProofWithToken(cfg)
	if err != nil {
		t.Fatal(err)
	}
	qualification := initial.Qualification
	qualification.PlanSHA256, err = environmentCreatePlanSHA256(raw)
	if err != nil {
		t.Fatal(err)
	}
	initial, err = publishEnvironmentCreateArtifact(context.Background(), cfg, generation, raw, operatorSHA, qualification, initial.ReleaseProofs, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	old, err := startEnvironmentCreateExecution(context.Background(), cfg, initial, operatorSHA, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { old.Close() })
	provider.PlanSHA256, provider.OwnerToken, provider.ArtifactSHA256 = old.Journal.PlanSHA256, old.Journal.OwnerToken, old.Journal.ArtifactSHA256
	old.Journal = provider
	if err := old.save(); err != nil {
		t.Fatal(err)
	}
	receipts, kubeSHA, err := validateProviderOnlyCreateJournal(provider, plan)
	if err != nil {
		t.Fatal(err)
	}
	recovery := environmentCreateProviderRecoveryPlan{
		Schema: "rtk-cloud.provider-create-recovery-plan/v1", OriginalPlanSHA256: initial.PlanSHA256,
		OriginalJournal: old.owner.Journal, OriginalJournalSHA256: deploymentplan.Digest(provider),
		OriginalOwnerSHA256: deploymentplan.Digest(old.owner), Plan: plan, Receipts: receipts, KubeconfigSHA256: kubeSHA,
	}
	sha := deploymentplan.Digest(recovery)
	artifact := environmentCreateProviderRecoveryArtifact{
		Schema: "rtk-cloud.provider-create-recovery-artifact/v1", Plan: recovery,
		Qualification: deploymentplan.Qualification{Decision: "GO", PlanSHA256: sha},
		QualifiedAt:   time.Now().UTC().Add(-time.Minute), ValidUntil: time.Now().UTC().Add(time.Hour),
	}
	parent, err := forkProviderCreateRecovery(old, recovery, sha, deploymentplan.Digest(artifact))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { parent.Close() })
	f := &initializedAuditContractFixture{cfg: cfg, store: store, sha: sha, artifact: artifact, parent: parent}
	f.write(t, filepath.Join("deployment/create/recovery", sha, "qualification.json"), artifact)
	var definitions []deploymentplan.Definition
	for i := 0; i < 33; i++ {
		definitions = append(definitions, deploymentplan.Definition{
			Phase: "configuration",
			Object: deploymentplan.Object{"apiVersion": "v1", "kind": "ConfigMap",
				"metadata": deploymentplan.Object{"name": fmt.Sprintf("foundation-proof-%02d", i), "namespace": "video-cloud-dev-secrets",
					"labels": deploymentplan.Object{"rtk.realtek.com/stack": "video-cloud-dev"}},
				"data": deploymentplan.Object{"fixture": "original-accepted"}},
		})
	}
	if len(inputs) == 1 {
		// Use real original-key material and the actual stage executor for
		// command-level tests. The generic loader cases above intentionally
		// exercise only the historical inventory/provenance format.
		definitions = definitions[:26]
		for _, role := range []string{"management", "openbao"} {
			names := map[string][]string{}
			if role == "openbao" {
				names["openbao"] = []string{"openbao.video-cloud-dev-secrets.svc"}
			} else {
				for _, name := range []string{"account-manager", "pki-controller", "certissuer", "video-cloud-api", "video-cloud-api-app-pki"} {
					names[name] = []string{name + ".video-cloud-dev-platform.svc"}
				}
			}
			transport, err := createEnvironmentTransport(store, sha, role, names, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			for name, identity := range transport.Identities {
				secret := "pki-create-" + name + "-transport"
				if role == "openbao" {
					secret = "openbao-transport-tls"
				}
				definitions = append(definitions, environmentCreateSecret("video-cloud-dev-secrets", secret, map[string]string{"ca.crt": transport.CACert, "tls.crt": identity.Certificate, "tls.key": identity.Key}))
			}
		}
		signer, err := ensureEnvironmentCreateAccountSigner(store)
		if err != nil {
			t.Fatal(err)
		}
		definitions = append(definitions, environmentCreateSecret("video-cloud-dev-secrets", "account-manager-jwt", signer))
		// Create the fresh fixture's actual catalog credentials before its
		// first foundation write. Preserve that complete inventory throughout
		// later qualification; an empty catalog must not hide missing inputs.
		password := inputs[0].operator["ACCOUNT_MANAGER_BOOTSTRAP_PLATFORM_ADMIN_PASSWORD"]
		if password == "" {
			t.Fatal("original initial operator password must be selected before publication")
		}
		if err := store.write("runtime/platform-admin", []byte(password+"\n"), false); err != nil {
			t.Fatal(err)
		}
		if err := ensureMissingRuntimeSecrets(io.Discard, store); err != nil {
			t.Fatal(err)
		}
		f.write(t, "deployment/storage/create-allocation.json", map[string]string{"source": "isolated original allocation"})
	}
	f.child, err = deploymentplan.Compile(deploymentplan.Inputs{
		Environment: "dev", Stack: "video-cloud-dev", Operation: "scoped",
		ConfigurationSHA256: deploymentplan.Digest(cfg), ReleaseSHA256: deploymentplan.Digest("isolated-release"),
		BindingsSHA256: deploymentplan.Digest("isolated-foundation"), CreateParentSHA256: sha,
	}, definitions, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(inputs) == 1 {
		backend := &normalTailE2EBackend{e: parent, objects: map[string]deploymentplan.Object{}, writes: map[string]int{}, routes: map[string]string{}}
		f.native = backend
		check := func(ctx context.Context, _ *deploymentplan.Plan) error { return parent.Check(ctx) }
		if err := executeEnvironmentCreateStagePlan(context.Background(), store, parent, "foundation-recovery-"+f.child.SHA256(), f.child, backend, check, check, check, parent.Check, io.Discard); err != nil {
			t.Fatal("record original accepted foundation", err)
		}
		if err := readExecutionJSON(filepath.Join(store.Root, "deployment/rollback", f.child.SHA256(), "journal.json"), &f.journal); err != nil {
			t.Fatal(err)
		}
		if !f.journal.Accepted || len(f.journal.Owners) != 33 || len(f.journal.Completed) != 33 {
			t.Fatal("original foundation did not finish")
		}
		if err := parent.Close(); err != nil {
			t.Fatal(err)
		}
		return f
	}
	f.journal = deploymentplan.Journal{PlanSHA256: f.child.SHA256(), Accepted: true, AcceptanceStarted: true, Owners: map[string]string{}}
	for i, resource := range f.child.Resources {
		f.journal.Owners[resource.ID] = fmt.Sprintf("original-foundation-owner-%02d", i)
		f.journal.Completed = append(f.journal.Completed, resource.ID)
	}
	f.write(t, filepath.Join("deployment/rollback", f.child.SHA256(), "plan.json"), f.child)
	f.write(t, filepath.Join("deployment/rollback", f.child.SHA256(), "journal.json"), f.journal)
	key := "kubernetes/foundation-recovery-" + f.child.SHA256()
	if err := parent.BeginEntry(key, "bootstrap", mustProviderRecoveryJSON(map[string]string{"plan_sha256": f.child.SHA256()}), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := parent.RecordReceipt(key, mustProviderRecoveryJSON(map[string]any{"plan_sha256": f.child.SHA256(), "owners": f.journal.Owners})); err != nil {
		t.Fatal(err)
	}
	if err := parent.CompleteEntry(key, time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *initializedAuditContractFixture) write(t *testing.T, relative string, value any) {
	t.Helper()
	if err := f.store.write(relative, mustProviderRecoveryJSON(value), true); err != nil {
		t.Fatal(err)
	}
}

func (f *initializedAuditContractFixture) pending(t *testing.T, keys ...string) {
	t.Helper()
	for _, key := range keys {
		f.parent.Journal.Entries = append(f.parent.Journal.Entries, environmentCreateJournalEntry{
			Key: key, Phase: "bootstrap", State: "pending", StartedAt: time.Now().UTC(),
			IntentSHA256: deploymentplan.Digest(key),
		})
	}
	if err := f.parent.save(); err != nil {
		t.Fatal(err)
	}
}

func TestInitializedCreateAuditResumesOnlyRecordedCheckpointAndKeepsHistory(t *testing.T) {
	for _, tc := range []struct {
		name    string
		flags   [5]bool
		profile []bool
		pending []string
	}{
		{"audit", [5]bool{}, nil, []string{"openbao-audit-enable"}},
		{"migration", [5]bool{true}, nil, []string{"kubernetes/pki-migrate"}},
		{"database-roles", [5]bool{false, true}, nil, []string{"kubernetes/database-runtime-roles"}},
		{"database-secrets", [5]bool{false, false, true}, nil, []string{"kubernetes/database-runtime-secrets"}},
		{"account-admission", [5]bool{false, false, false, true}, nil, []string{"kubernetes/account-bootstrap"}},
		{"account-write-accepted", [5]bool{false, false, false, false, true}, nil, []string{"kubernetes/account-bootstrap", "kubernetes/account-admission-recovery-" + strings.Repeat("a", 64)}},
		{"account-complete", [5]bool{}, []bool{true}, nil},
		{"listener-pending", [5]bool{}, []bool{true, true}, []string{"kubernetes/pki-initial-listeners"}},
		{"listener-complete", [5]bool{}, []bool{true, false}, nil},
		{"root-reservation", [5]bool{}, []bool{true, false, true}, []string{"pki-reserve-service-root"}},
		{"root-admission", [5]bool{}, []bool{true, false, false, true}, []string{"kubernetes/service-root-trust"}},
		{"root-partial", [5]bool{}, []bool{true, false, false, true, true}, []string{"kubernetes/service-root-trust", "kubernetes/service-root-trust-admission-recovery-" + strings.Repeat("b", 64)}},
		{"service-session", [5]bool{}, []bool{true, false, false, false, false, true}, []string{"kubernetes/service-session"}},
		{"expired-session", [5]bool{}, []bool{true, false, false, false, false, false, true}, []string{"kubernetes/service-issuer-server-enrollment"}},
		{"renewal-permissions", [5]bool{}, []bool{true, false, false, false, false, false, false, true}, []string{"kubernetes/service-issuer-server-enrollment", "kubernetes/service-session-original-key-renewal-" + strings.Repeat("c", 64)}},
		{"core-accepted", [5]bool{}, []bool{true, false, false, false, false, false, false, false, true}, nil},
		{"core-rollout-pending", [5]bool{}, []bool{true, false, false, false, false, false, false, false, false, true}, []string{"kubernetes/service-managed-core"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newInitializedAuditContractFixture(t)
			f.pending(t, tc.pending...)
			before := createBootstrapStateSnapshot(t, f.store.Root)
			e, artifact, child, journal, err := loadInitializedCreateAudit(f.cfg, f.store, f.sha, tc.flags[0], tc.flags[1], tc.flags[2], tc.flags[3], tc.flags[4], tc.profile...)
			if err != nil {
				t.Fatal("recorded original checkpoint rejected", err)
			}
			defer e.Close()
			if deploymentplan.Digest(artifact) != deploymentplan.Digest(f.artifact) || child.SHA256() != f.child.SHA256() || deploymentplan.Digest(journal) != deploymentplan.Digest(f.journal) {
				t.Fatal("recovery changed admitted provenance")
			}
			if err := e.Check(context.Background()); err != nil {
				t.Fatal("recovery did not retain original execution owner", err)
			}
			if another, _, _, _, err := loadInitializedCreateAudit(f.cfg, f.store, f.sha, tc.flags[0], tc.flags[1], tc.flags[2], tc.flags[3], tc.flags[4], tc.profile...); err == nil {
				another.Close()
				t.Fatal("simultaneous recovery acquired the same owner")
			}
			if !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, f.store.Root)) {
				t.Fatal("loading accepted history mutated original evidence")
			}
		})
	}
}

func TestInitializedCreateAuditRefusesAlteredHistoryAndReleasesReadLock(t *testing.T) {
	for _, mode := range []string{
		"wrong-environment", "wrong-profile", "invalid-parent", "owner-token", "foreign-owner",
		"journal-token", "journal-completed", "journal-empty", "preceding-pending", "wrong-final-step", "unexpected-final-receipt",
		"artifact-schema", "artifact-decision", "artifact-plan", "missing-artifact", "missing-foundation-proof",
		"foundation-plan", "foundation-in-flight", "foundation-failed", "foundation-unaccepted", "foundation-owner-count", "foundation-completed-count",
	} {
		t.Run(mode, func(t *testing.T) {
			f := newInitializedAuditContractFixture(t)
			f.pending(t, "openbao-audit-enable")
			switch mode {
			case "wrong-environment":
				f.cfg.Environment = "staging"
			case "wrong-profile":
				f.cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] = "protected"
			case "invalid-parent":
				f.sha = "not-a-digest"
			case "owner-token":
				f.parent.owner.OwnerToken = "other-owner"
				f.write(t, "deployment/.execution-active/owner.json", f.parent.owner)
			case "foreign-owner":
				f.parent.owner.Stack = "video-cloud-staging"
				f.write(t, "deployment/.execution-active/owner.json", f.parent.owner)
			case "journal-token":
				f.parent.Journal.OwnerToken = "other-owner"
			case "journal-completed":
				f.parent.Journal.CompletedAt = time.Now().UTC()
			case "journal-empty":
				f.parent.Journal.Entries = nil
			case "preceding-pending":
				f.parent.Journal.Entries[0].State = "pending"
			case "wrong-final-step":
				f.parent.Journal.Entries[len(f.parent.Journal.Entries)-1].Key = "openbao-initialize"
			case "unexpected-final-receipt":
				f.parent.Journal.Entries[len(f.parent.Journal.Entries)-1].Receipt = mustProviderRecoveryJSON(map[string]bool{"uncertain-write": true})
			case "artifact-schema":
				f.artifact.Schema = "unknown"
			case "artifact-decision":
				f.artifact.Qualification.Decision = "NO-GO"
			case "artifact-plan":
				f.artifact.Plan.KubeconfigSHA256 = strings.Repeat("f", 64)
			case "missing-artifact":
				if err := os.Remove(filepath.Join(f.store.Root, "deployment/create/recovery", f.sha, "qualification.json")); err != nil {
					t.Fatal(err)
				}
			case "missing-foundation-proof":
				f.parent.Journal.Entries[len(f.parent.Journal.Entries)-2].Receipt = mustProviderRecoveryJSON(map[string]string{"plan_sha256": "untrusted"})
			case "foundation-plan":
				f.child.Resources[0].Desired["data"] = deploymentplan.Object{"fixture": "changed"}
				f.write(t, filepath.Join("deployment/rollback", f.journal.PlanSHA256, "plan.json"), f.child)
			case "foundation-in-flight":
				f.journal.InFlight = f.child.Resources[0].ID
			case "foundation-failed":
				f.journal.Failed = f.child.Resources[0].ID
			case "foundation-unaccepted":
				f.journal.Accepted = false
			case "foundation-owner-count":
				delete(f.journal.Owners, f.child.Resources[0].ID)
			case "foundation-completed-count":
				f.journal.Completed = f.journal.Completed[1:]
			}
			if strings.HasPrefix(mode, "artifact-") {
				f.write(t, filepath.Join("deployment/create/recovery", f.sha, "qualification.json"), f.artifact)
			}
			if strings.HasPrefix(mode, "foundation-") && mode != "foundation-plan" {
				f.write(t, filepath.Join("deployment/rollback", f.child.SHA256(), "journal.json"), f.journal)
			}
			if err := f.parent.save(); err != nil {
				t.Fatal(err)
			}
			before := createBootstrapStateSnapshot(t, f.store.Root)
			e, _, _, _, err := loadInitializedCreateAudit(f.cfg, f.store, f.sha, false, false, false, false, false)
			if err == nil {
				e.Close()
				t.Fatal("altered or uncertain history admitted")
			}
			if !reflect.DeepEqual(before, createBootstrapStateSnapshot(t, f.store.Root)) {
				t.Fatal("refused recovery changed original evidence")
			}
			lock, err := deploymentplan.LockExecutionOwner(filepath.Join(f.store.Root, "deployment/.execution-active"), false)
			if err != nil {
				t.Fatal("failed reader leaked execution lock", err)
			}
			lock.Close()
		})
	}
}
