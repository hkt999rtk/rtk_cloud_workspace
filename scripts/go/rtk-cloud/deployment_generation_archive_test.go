package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestArchiveRemovedGenerationPreservesAndResumesExactBytes(t *testing.T) {
	cfg, store := deploymentRemovalFixture(t, "dev", false)
	cfg.Storage.RuntimeMedia = deploymentStorageTarget{Purpose: "runtime-media", Bucket: "rtk-cloud-dev-runtime-us-sea", Region: "us-sea"}
	cfg.Storage.OTAFirmware = deploymentStorageTarget{Purpose: "ota-firmware", Bucket: "rtk-cloud-dev-ota-firmware-us-lax", Region: "us-lax"}
	kubeSHA := environmentPreflightBytesSHA256([]byte("original-kubeconfig"))
	proof, err := json.Marshal(deploymentRemovalVolumes{
		Schema: 1, Environment: "dev", Stack: cfg.Values["CLOUD_STACK_NAME"], ClusterID: "123",
		KubeSHA256: kubeSHA, VolumeIDs: []string{"456"},
	})
	if err != nil {
		t.Fatal(err)
	}
	original := map[string][]byte{
		generationRemovalProof:                 proof,
		"openbao/init.json":                    []byte("original encrypted key bytes\n"),
		"deployment/preflight/active.json":     []byte("original deployment qualification"),
		environmentCreateStorageAllocationPath: []byte("original bucket and credential IDs"),
	}
	for relative, raw := range original {
		if err := store.write(relative, raw, false); err != nil {
			t.Fatal(err)
		}
	}
	const operator = "operator/env/LINODE_TOKEN"
	if err := store.write(operator, []byte("selected-fixture-token"), false); err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(store.Root, "deployment", "archives", "lke-123")
	run := func(verify func(context.Context, deploymentConfig, deploymentGenerationArchive) error) error {
		return archiveRemovedDeploymentGeneration(context.Background(), cfg, store, "123", verify)
	}
	called := false
	if err := run(func(context.Context, deploymentConfig, deploymentGenerationArchive) error {
		called = true
		return nil
	}); err == nil || called {
		t.Fatal("volume proof alone authorized archiving without completed exact-generation removal")
	}
	writeOriginalGenerationTestLedger(t, cfg, store, "123", kubeSHA)
	targets, ledgerSHA, _, err := loadGenerationRemovalTargets(cfg, store, "123")
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	dnsSHA, err := verifyGenerationRemovalDNS(t.Context(), cfg, true)
	if err != nil {
		t.Fatal(err)
	}
	workers := []string{"90"}
	instances, buckets := exactGenerationProviderScope(targets, workers)
	original[generationRemovalIntent], err = json.Marshal(originalGenerationRemoval{Schema: 1, Environment: "dev", ClusterID: "123",
		LedgerSHA256: ledgerSHA, KubeSHA256: kubeSHA, WorkerIDs: workers, VolumeIDs: []string{"456"}, KubernetesPurged: true,
		CompletedAt: &now, DNSOwnershipSHA: dnsSHA, InstanceIDs: instances, BucketIDs: buckets})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.write(generationRemovalIntent, original[generationRemovalIntent], false); err != nil {
		t.Fatal(err)
	}
	denied := errors.New("exact volume still present")
	if err := run(func(context.Context, deploymentConfig, deploymentGenerationArchive) error { return denied }); !errors.Is(err, denied) {
		t.Fatalf("expected absence failure, got %v", err)
	}
	if _, err := os.Stat(base); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed absence check staged an archive: %v", err)
	}
	verified := func(_ context.Context, selected deploymentConfig, m deploymentGenerationArchive) error {
		if selected.Environment != "dev" || m.ClusterID != "123" || len(m.VolumeIDs) != 1 || m.VolumeIDs[0] != "456" {
			t.Fatalf("wrong selected removal authority: %s %s %v", selected.Environment, m.ClusterID, m.VolumeIDs)
		}
		if strings.Join(m.InstanceIDs, ",") != "71,72,90" || strings.Join(m.BucketIDs, ",") != "us-lax/rtk-cloud-dev-ota-firmware-us-lax,us-sea/rtk-cloud-dev-runtime-us-sea" {
			t.Fatal("archive omitted original VM or bucket identities")
		}
		return nil
	}
	if err := run(verified); err != nil {
		t.Fatal(err)
	}
	// Recreate the on-disk state left by an interruption after publishing the
	// immutable manifest and moving some, but not all, original paths.
	if err := store.write(generationArchivePending, []byte("123"), false); err != nil {
		t.Fatal(err)
	}
	if err := qualifyEnvironmentCreateTargetAbsence(context.Background(), cfg, store); err == nil || !strings.Contains(err.Error(), "archival is incomplete") {
		t.Fatal("fresh target must block while its previous archive is incomplete")
	}
	staged := filepath.Join(base, "state", "openbao")
	active := filepath.Join(store.Root, "openbao")
	if err := os.Rename(staged, active); err != nil {
		t.Fatal(err)
	}
	if err := run(verified); err != nil {
		t.Fatal(err)
	}
	if err := checkGenerationArchiveIdle(store); err != nil {
		t.Fatal(err)
	}
	for relative, expected := range original {
		if _, err := os.Lstat(filepath.Join(store.Root, relative)); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("original path remains active: %s: %v", relative, err)
		}
		path := filepath.Join(base, "state", relative)
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != string(expected) {
			t.Fatalf("archive did not preserve original bytes at %s: %v", relative, err)
		}
		if info, err := os.Lstat(path); err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("archive lost private file mode at %s: %v", relative, err)
		}
	}
	if raw, err := os.ReadFile(filepath.Join(store.Root, operator)); err != nil || string(raw) != "selected-fixture-token" {
		t.Fatal("selected operator credentials must stay active")
	}
	if err := store.write("openbao/init.json", []byte("new generation"), false); err != nil {
		t.Fatal(err)
	}
	if err := run(verified); err == nil {
		t.Fatal("retry would accept a conflicting active generation")
	}
}
