package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestCreateLedgerPreservesAllocatedIDBeforeLaterFailure(t *testing.T) {
	for _, environment := range []string{"dev", "staging", "prod"} {
		t.Run(environment, func(t *testing.T) {
			cfg, store, operatorSHA, artifact := createStateFixture(t, environment)
			execution, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operatorSHA, time.Now().UTC())
			if err != nil {
				t.Fatal(err)
			}
			defer execution.Close()
			intent, _ := json.Marshal(map[string]string{"native_path": "/lke/clusters", "label": cfg.Values["CLOUD_STACK_NAME"] + "-lke"})
			if err := execution.BeginEntry("lke", "provider", intent, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
			receipt := environmentCreateProviderReceipt{Key: "lke", Kind: "lke", NativeID: "12345", Name: cfg.Values["CLOUD_STACK_NAME"] + "-lke", Region: "us-sea"}
			raw, _ := json.Marshal(receipt)
			if err := execution.RecordReceipt("lke", raw); err != nil {
				t.Fatal(err)
			}
			if err := recordEnvironmentCreateProviderOwner(context.Background(), execution, receipt); err != nil {
				t.Fatal(err)
			}
			ledger, err := readEnvironmentCreateLedger(execution)
			if err != nil || ledger.Provider["lke"].NativeID != "12345" || execution.Journal.Entries[0].State == "completed" {
				t.Fatal("uncertain native allocation lost its original ID", err)
			}
			path := filepath.Join(store.Root, environmentCreateLedgerPath(execution))
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatal("private receipt permissions", err)
			}
			for _, mutation := range []string{"id", "kind", "region", "name"} {
				t.Run(mutation, func(t *testing.T) {
					changed := receipt
					switch mutation {
					case "id":
						changed.NativeID = "99999"
					case "kind":
						changed.Kind = "instance"
					case "region":
						changed.Region = "us-lax"
					case "name":
						changed.Name = "foreign"
					}
					if recordEnvironmentCreateProviderOwner(context.Background(), execution, changed) == nil {
						t.Fatal("allocated owner was replaced")
					}
				})
			}
			receipt.Status = "ready"
			if err := recordEnvironmentCreateProviderOwner(context.Background(), execution, receipt); err != nil {
				t.Fatal("original owner's observed status did not update", err)
			}
			// The root fence survives an incomplete flow so another operation cannot
			// reinterpret allocated objects as a brand-new environment.
			execution.Close()
			if _, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operatorSHA, time.Now().UTC()); err == nil {
				t.Fatal("partial native allocation was repeated")
			}
		})
	}
}

func TestCreateLedgerRejectsCrossEnvironmentAndLostOwnership(t *testing.T) {
	cfg, store, operatorSHA, artifact := createStateFixture(t, "dev")
	execution, err := startEnvironmentCreateExecution(context.Background(), cfg, artifact, operatorSHA, time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	defer execution.Close()
	receipt := environmentCreateProviderReceipt{Key: "edge", Kind: "instance", NativeID: "12345", Name: "video-cloud-dev-edge-haproxy-01", Region: "us-sea"}
	if err := recordEnvironmentCreateProviderOwner(context.Background(), execution, receipt); err != nil {
		t.Fatal(err)
	}
	ledger, err := readEnvironmentCreateLedger(execution)
	if err != nil {
		t.Fatal(err)
	}
	ledger.Environment = "staging"
	raw, _ := json.Marshal(ledger)
	if err := os.WriteFile(filepath.Join(store.Root, environmentCreateLedgerPath(execution)), raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readEnvironmentCreateLedger(execution); err == nil {
		t.Fatal("another environment's ledger was adopted")
	}
}
