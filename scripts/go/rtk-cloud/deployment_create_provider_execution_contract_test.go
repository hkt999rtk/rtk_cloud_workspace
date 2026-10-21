package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Publish the actual original create artifact, admit its owner, and record the
// external provider receipts. The initial admission is an isolated historical
// fixture; later command qualification must still run its own complete checks.
type providerExecutionContractInputs struct {
	cfg        deploymentConfig
	operator   map[string]string
	bootstrap  environmentCreateBootstrapPlan
	kubeconfig []byte
	prepare    func(*deploymentConfig, *environmentCreatePlan) []environmentReleasePublicationProof
}

func providerExecutionContractFixture(t *testing.T, inputs ...providerExecutionContractInputs) (deploymentConfig, secretStore, *environmentCreateExecution, *environmentCreateArtifact, environmentCreatePlan) {
	t.Helper()
	cfg, store, operatorSHA, original := createStateFixture(t, "dev")
	if len(inputs) > 1 {
		t.Fatal("one exact optional provider contract input required")
	}
	if len(inputs) == 1 {
		cfg = inputs[0].cfg
		operatorSHA = deploymentplan.Digest(inputs[0].operator)
		for name, value := range inputs[0].operator {
			if err := store.write(filepath.Join("operator/env", name), []byte(value), false); err != nil {
				t.Fatal(err)
			}
		}
	}
	cfg.Values["DEPLOYMENT_PROTECTION_PROFILE"] = "standard"
	if len(inputs) == 0 {
		cfg.Storage = deploymentStoragePlan{RuntimeMedia: deploymentStorageTarget{Purpose: "runtime-media", Bucket: "isolated-runtime", Region: "us-sea", Prefix: "dev"}, OTAFirmware: deploymentStorageTarget{Purpose: "ota-firmware", Bucket: "isolated-ota", Region: "us-sea", Prefix: "dev"}, OTAMode: "dedicated"}
	}
	var plan environmentCreatePlan
	if err := json.Unmarshal(original.Plan, &plan); err != nil {
		t.Fatal(err)
	}
	journal, provider := providerRecoveryJournalFixture()
	if len(inputs) == 1 && len(inputs[0].kubeconfig) != 0 {
		// Select the isolated external provider's access response before the
		// original create artifact or its owner/journal can freeze those bytes.
		if err := store.write("kube/kubeconfig.yaml", inputs[0].kubeconfig, false); err != nil {
			t.Fatal(err)
		}
		for i := range journal.Entries {
			if journal.Entries[i].Key != "provider-kube-access" {
				continue
			}
			var access map[string]string
			if err := json.Unmarshal(journal.Entries[i].Receipt, &access); err != nil {
				t.Fatal(err)
			}
			access["kubeconfig_sha256"] = environmentPreflightBytesSHA256(inputs[0].kubeconfig)
			journal.Entries[i].Receipt = mustProviderRecoveryJSON(access)
		}
	}
	plan.Provider = provider.Provider
	if len(inputs) == 1 {
		plan.Bootstrap = inputs[0].bootstrap
		for i := range plan.Provider.Resources {
			plan.Provider.Resources[i].Path = map[string]string{"lke": "/lke/clusters", "instance": "/linode/instances", "bucket": "/object-storage/buckets"}[plan.Provider.Resources[i].Kind]
		}
	}
	proofs := original.ReleaseProofs
	if len(inputs) == 1 && inputs[0].prepare != nil {
		proofs = inputs[0].prepare(&cfg, &plan)
	}
	plan.OperatorSHA256 = operatorSHA
	plan.ConfigurationSHA256 = deploymentplan.Digest(cfg)
	raw := mustProviderRecoveryJSON(plan)
	sha, err := environmentCreatePlanSHA256(raw)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := invalidateEnvironmentPreflightProofWithToken(cfg)
	if err != nil {
		t.Fatal(err)
	}
	q := original.Qualification
	q.PlanSHA256 = sha
	artifact, err := publishEnvironmentCreateArtifact(context.Background(), cfg, generation, raw, operatorSHA, q, proofs, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	e, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operatorSHA, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	for i := range journal.Entries {
		journal.Entries[i].StartedAt = e.Journal.AdmittedAt
		if journal.Entries[i].State == "completed" {
			journal.Entries[i].CompletedAt = e.Journal.AdmittedAt
		}
	}
	e.Journal.Entries = journal.Entries
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	return cfg, store, e, artifact, plan
}

func providerExecutionContractFork(t *testing.T, e *environmentCreateExecution, plan environmentCreatePlan, profile string) (*environmentCreateExecution, environmentCreateProviderRecoveryArtifact) {
	t.Helper()
	receipts := map[string]environmentCreateProviderReceipt{}
	kubeSHA := ""
	for _, entry := range e.Journal.Entries {
		if entry.Phase != "provider" {
			continue
		}
		if entry.Key == "provider-kube-access" {
			var proof map[string]string
			if err := json.Unmarshal(entry.Receipt, &proof); err != nil {
				t.Fatal(err)
			}
			kubeSHA = proof["kubeconfig_sha256"]
		} else {
			var proof environmentCreateProviderReceipt
			if err := json.Unmarshal(entry.Receipt, &proof); err != nil {
				t.Fatal(err)
			}
			receipts[entry.Key] = proof
		}
	}
	now := time.Now().UTC()
	p := environmentCreateProviderRecoveryPlan{Schema: "rtk-cloud.provider-create-recovery-plan/v1", RecoveryProfile: profile, OriginalPlanSHA256: e.Journal.PlanSHA256, OriginalJournal: e.owner.Journal, OriginalJournalSHA256: deploymentplan.Digest(e.Journal), OriginalOwnerSHA256: deploymentplan.Digest(e.owner), CheckerSHA256: deploymentplan.Digest("isolated checker"), KubeconfigSHA256: kubeSHA, Plan: plan, Receipts: receipts}
	sha := deploymentplan.Digest(p)
	q := deploymentplan.Qualification{PlanSHA256: sha, Decision: "GO"}
	for _, id := range environmentCreateRequiredGates {
		q.Gates = append(q.Gates, deploymentplan.GateResult{ID: id, Status: "PASS", Required: true, EvidenceSHA256: deploymentplan.Digest("isolated provider gate:" + id)})
	}
	artifact := environmentCreateProviderRecoveryArtifact{Schema: "rtk-cloud.provider-create-recovery-artifact/v1", Plan: p, Qualification: q, QualifiedAt: now, ValidUntil: now.Add(time.Hour)}
	if err := e.Store.write(filepath.Join("deployment/create/recovery", sha, "qualification.json"), mustProviderRecoveryJSON(artifact), false); err != nil {
		t.Fatal(err)
	}
	next, err := forkProviderCreateRecovery(e, p, sha, deploymentplan.Digest(artifact))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = next.Close() })
	return next, artifact
}

func providerExecutionContractCanaries(t *testing.T, cfg deploymentConfig, e *environmentCreateExecution) {
	t.Helper()
	if err := e.BeginEntry("activate-storage", "storage", mustProviderRecoveryJSON(cfg.Storage), time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	for _, target := range []deploymentStorageTarget{cfg.Storage.RuntimeMedia, cfg.Storage.OTAFirmware} {
		object := strings.Trim(target.Prefix, "/") + "/__rtk_cloud_create_validation__/" + e.Journal.PlanSHA256 + "/" + target.Purpose
		proof := deploymentplan.Object{"bucket": target.Bucket, "region": target.Region, "object": object, "body_sha256": environmentPreflightBytesSHA256([]byte("rtk-cloud-create-storage-canary"))}
		key := "storage-canary-" + target.Purpose
		if err := e.BeginEntry(key, "storage", mustProviderRecoveryJSON(proof), time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
		proof["deleted_verified_http"] = 404
		if err := e.RecordReceipt(key, mustProviderRecoveryJSON(proof)); err != nil {
			t.Fatal(err)
		}
		if err := e.CompleteEntry(key, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
}

func assertRecoveryContractReadOnly(t *testing.T, store secretStore, before map[string]string) {
	t.Helper()
	if after := createBootstrapStateSnapshot(t, store.Root); !reflect.DeepEqual(before, after) {
		t.Fatal("recovery qualification rewrote the original execution or material")
	}
}

func TestProviderExecutionContractLoadsOriginalAndCanaryRecoveryWithoutReplaying(t *testing.T) {
	for _, afterCanary := range []bool{false, true} {
		name := "original"
		if afterCanary {
			name = "completed-canaries"
		}
		t.Run(name, func(t *testing.T) {
			cfg, store, e, _, plan := providerExecutionContractFixture(t)
			if afterCanary {
				e, _ = providerExecutionContractFork(t, e, plan, "")
				providerExecutionContractCanaries(t, cfg, e)
			}
			expected := deploymentplan.Digest(e.Journal)
			sha := e.Journal.PlanSHA256
			if err := e.Close(); err != nil {
				t.Fatal(err)
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			loaded, actual, err := loadStoppedProviderCreate(cfg, store, sha, afterCanary)
			if err != nil {
				t.Fatal("authentic stopped provider history rejected", err)
			}
			if deploymentplan.Digest(loaded.Journal) != expected || deploymentplan.Digest(actual) != deploymentplan.Digest(plan) {
				t.Fatal("loader changed original plan or provider receipts")
			}
			if _, _, err := loadStoppedProviderCreate(cfg, store, sha, afterCanary); err == nil {
				t.Fatal("concurrent recovery acquired an already held original owner")
			}
			if err := loaded.Close(); err != nil {
				t.Fatal(err)
			}
			assertRecoveryContractReadOnly(t, store, before)
			// A read or failed concurrent attempt must not consume the stopped owner.
			again, _, err := loadStoppedProviderCreate(cfg, store, sha, afterCanary)
			if err != nil {
				t.Fatal("read-only recovery could not reopen its owner", err)
			}
			_ = again.Close()
		})
	}
}

func TestProviderExecutionContractRejectsTamperedHistoryAndReleasesLock(t *testing.T) {
	for _, mode := range []string{"wrong-owner", "wrong-journal", "wrong-pointer", "changed-artifact", "missing-artifact", "uncertain-storage", "canary-ancestor", "canary-provider-receipt", "canary-unknown-write", "canary-qualification"} {
		t.Run(mode, func(t *testing.T) {
			cfg, store, e, artifact, plan := providerExecutionContractFixture(t)
			afterCanary := strings.HasPrefix(mode, "canary-")
			if afterCanary {
				e, _ = providerExecutionContractFork(t, e, plan, "")
				providerExecutionContractCanaries(t, cfg, e)
			}
			sha := e.Journal.PlanSHA256
			if err := e.Close(); err != nil {
				t.Fatal(err)
			}
			write := func(path string, v any) {
				t.Helper()
				if err := store.write(path, mustProviderRecoveryJSON(v), true); err != nil {
					t.Fatal(err)
				}
			}
			switch mode {
			case "wrong-owner":
				owner := e.owner
				owner.OwnerToken = "foreign"
				write("deployment/.execution-active/owner.json", owner)
			case "wrong-journal":
				e.Journal.PlanSHA256 = deploymentplan.Digest("foreign")
				if err := e.save(); err != nil {
					t.Fatal(err)
				}
			case "wrong-pointer":
				var active environmentPreflightActive
				if err := readEnvironmentPreflightJSON(filepath.Join(store.Root, "deployment/create/preflight/active.json"), &active); err != nil {
					t.Fatal(err)
				}
				active.PlanSHA256 = deploymentplan.Digest("foreign")
				write("deployment/create/preflight/active.json", active)
			case "changed-artifact":
				artifact.ValidUntil = artifact.ValidUntil.Add(time.Hour)
				path, _ := filepath.Rel(store.Root, filepath.Join(artifact.Directory, "artifact.json"))
				write(path, artifact)
			case "missing-artifact":
				if err := os.Remove(filepath.Join(artifact.Directory, "artifact.json")); err != nil {
					t.Fatal(err)
				}
			case "uncertain-storage":
				e.Journal.Entries[6].Receipt = json.RawMessage(`{"unknown_write":true}`)
				if err := e.save(); err != nil {
					t.Fatal(err)
				}
			case "canary-ancestor":
				var ancestor environmentCreateJournal
				if err := readEnvironmentPreflightJSON(filepath.Join(store.Root, e.Journal.OriginalJournal), &ancestor); err != nil {
					t.Fatal(err)
				}
				ancestor.Entries[0].Receipt = json.RawMessage(`{"native_id":"foreign"}`)
				write(e.Journal.OriginalJournal, ancestor)
			case "canary-provider-receipt":
				e.Journal.Entries[0].IntentSHA256 = deploymentplan.Digest("foreign")
				if err := e.save(); err != nil {
					t.Fatal(err)
				}
			case "canary-unknown-write":
				e.Journal.Entries[7].State = "pending"
				if err := e.save(); err != nil {
					t.Fatal(err)
				}
			case "canary-qualification":
				var a environmentCreateProviderRecoveryArtifact
				path := filepath.Join("deployment/create/recovery", sha, "qualification.json")
				if err := readEnvironmentPreflightJSON(filepath.Join(store.Root, path), &a); err != nil {
					t.Fatal(err)
				}
				a.Qualification.Decision = "NO-GO"
				write(path, a)
			}
			before := createBootstrapStateSnapshot(t, store.Root)
			if loaded, _, err := loadStoppedProviderCreate(cfg, store, sha, afterCanary); err == nil {
				_ = loaded.Close()
				t.Fatal("changed or uncertain history acquired recovery authority")
			}
			assertRecoveryContractReadOnly(t, store, before)
			file, err := deploymentplan.LockExecutionOwner(filepath.Join(store.Root, "deployment/.execution-active"), false)
			if err != nil {
				t.Fatal("failed recovery leaked process lock", err)
			}
			_ = file.Close()
		})
	}
}
