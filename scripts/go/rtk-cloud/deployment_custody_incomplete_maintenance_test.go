package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

// Maintenance needs original initialized custody before final bootstrap bindings
// exist. It must not invent completed deployment evidence or accept another scope.
func TestCustodyMaintenanceReadsInitializedUnfinishedCreate(t *testing.T) {
	for _, mode := range []string{"valid", "changed-owners", "unaccepted-init", "unknown-root", "corrupt-completed-binding"} {
		t.Run(mode, func(t *testing.T) {
			cfg, bootstrap, _, service := createServiceRecipeFixture(t, "dev")
			_, _, _, other := createServiceRecipeFixture(t, "dev")
			cfg.Values["PKI_STATE_BACKEND"], cfg.Values["PKI_STATE_KV_MOUNT"], cfg.Values["PKI_STATE_LEASE_DURATION"] = "openbao", "identities", "30s"
			cfg.Values["DEPLOYMENT_PREFLIGHT_VALIDITY_SECONDS"] = "900"
			cfg, store, original, _, parent := providerExecutionContractFixture(t, providerExecutionContractInputs{cfg: cfg, bootstrap: bootstrap, operator: map[string]string{}})
			_, operator, err := readEnvironmentCreateOperator(cfg)
			if err != nil {
				t.Fatal(err)
			}
			parent.OperatorSHA256 = deploymentplan.Digest(operator)
			e, _ := providerExecutionContractFork(t, original, parent, "isolated-custody-maintenance")
			generation := e.Journal.PlanSHA256
			managed := other.Root
			managed.Domain, managed.Status = "openbao_tls", "active"
			managed.ID = "22222222-2222-4222-8222-222222222222"
			binding, err := createEnvironmentCustodyBinding(cfg, bootstrap, generation, managed.RootFingerprint)
			if err != nil {
				t.Fatal(err)
			}
			scope := deploymentplan.Object{"schema": "rtk.custody.bootstrap/v1", "environment": "dev", "generation": generation, "mount": "identities", "cas_required": true, "owners": binding.Owners}
			body, _ := json.Marshal(scope)
			scopeSHA, _ := environmentCreatePlanSHA256(body)
			ownersPath := filepath.Join("deployment/create", generation, "custody/owners.json")
			if err = store.write(ownersPath, body, false); err != nil {
				t.Fatal(err)
			}
			now := time.Now().UTC()
			if err = e.BeginEntry("workload-custody-initialize", "bootstrap", body, now); err != nil {
				t.Fatal(err)
			}
			if mode != "unaccepted-init" {
				if err = e.RecordReceipt("workload-custody-initialize", mustProviderRecoveryJSON(map[string]string{"scope_sha256": scopeSHA})); err != nil {
					t.Fatal(err)
				}
				if err = e.CompleteEntry("workload-custody-initialize", now); err != nil {
					t.Fatal(err)
				}
			}
			for _, root := range []environmentCreateRegisteredIssuer{service.Root, managed} {
				doc := deploymentplan.Clone(root.Document)
				doc["issuer_id"], doc["trust_domain"], doc["status"] = root.ID, root.Domain, "active"
				if mode == "unknown-root" && root.Domain == "openbao_tls" {
					continue
				}
				key := "pki-activate-" + root.ID
				if err = e.BeginEntry(key, "bootstrap", mustProviderRecoveryJSON(map[string]string{"issuer_id": root.ID}), now); err != nil {
					t.Fatal(err)
				}
				if err = e.RecordReceipt(key, mustProviderRecoveryJSON(doc)); err != nil {
					t.Fatal(err)
				}
				if err = e.CompleteEntry(key, now); err != nil {
					t.Fatal(err)
				}
			}
			trust, err := renderEnvironmentCustodyTrust(cfg, generation, service.Root.Certificate, &managed, false)
			if err != nil {
				t.Fatal(err)
			}
			for path, data := range map[string][]byte{
				filepath.Join("deployment/create/material", generation, "openbao-transport/ca.crt"): []byte(service.Root.Certificate),
				filepath.Join("deployment/create", generation, "custody/transport.json"):            []byte(deploymentplan.String(deploymentplan.Map(trust[0].Object["data"])["transport.json"])),
			} {
				if err = store.write(path, data, false); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "changed-owners" {
				if err = store.write(ownersPath, []byte(`{"owners":[]}`), true); err != nil {
					t.Fatal(err)
				}
			}
			completedPath := filepath.Join(store.Root, "deployment/create", generation, "bootstrap/bindings.json")
			if mode == "corrupt-completed-binding" {
				if err = store.write(filepath.Join("deployment/create", generation, "bootstrap/bindings.json"), []byte(`{}`), false); err != nil {
					t.Fatal(err)
				}
			}
			before, err := os.ReadFile(e.JournalPath)
			if err != nil {
				t.Fatal(err)
			}
			if err = e.Close(); err != nil {
				t.Fatal(err)
			}
			actual, err := readEnvironmentCustodyBootstrapBinding(cfg, store, generation)
			if mode == "valid" {
				if err != nil || actual == nil || actual.ServiceRootSHA256 != service.Root.RootFingerprint || actual.OpenBaoRootSHA256 != managed.RootFingerprint || len(actual.Owners) != len(binding.Owners) {
					t.Fatalf("initialized original custody must remain maintainable before deployment completion: %v", err)
				}
				held, _, err := loadCreateRecoveryExecutionOwner(cfg, store, generation)
				if err != nil {
					t.Fatal(err)
				}
				_, err = readEnvironmentCustodyBootstrapBinding(cfg, store, generation, held)
				if err != nil || held.Check(t.Context()) != nil {
					_ = held.Close()
					t.Fatal("maintenance discovery must share its held original owner without relocking or closing it", err)
				}
				_ = held.Close()
			} else if err == nil {
				t.Fatal("accepted unverified original custody", mode)
			}
			after, err := os.ReadFile(e.JournalPath)
			if err != nil || string(before) != string(after) {
				t.Fatal("maintenance discovery rewrote original execution", err)
			}
			if mode != "corrupt-completed-binding" {
				if _, err = os.Lstat(completedPath); !os.IsNotExist(err) {
					t.Fatal("discovery manufactured deployment completion bindings", err)
				}
			}
		})
	}
}
