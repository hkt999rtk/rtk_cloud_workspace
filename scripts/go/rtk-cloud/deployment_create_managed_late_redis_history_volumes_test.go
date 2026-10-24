package main

import (
	"context"
	"slices"
	"sort"
	"testing"

	"rtk-cloud-workspace/scripts/go/rtk-cloud/internal/deploymentplan"
)

func TestManagedLateRedisPrePauseHistoricalAppVolumesKeepOriginalEighteen(t *testing.T) {
	store, actual, _, _ := redisPrePauseFixture(t)
	_, saved, err := readManagedLateRedisPrePauseSource(store, actual)
	if err != nil {
		t.Fatal(err)
	}
	_, redis, err := readManagedLateRedisDurableVolumeAuthority(store, actual)
	if err != nil {
		t.Fatal(err)
	}
	ledger, err := readEnvironmentCreateLedger(actual)
	if err != nil {
		t.Fatal(err)
	}
	ids := make([]string, 0, len(saved.Descriptor.OriginalVolumes))
	for id := range saved.Descriptor.OriginalVolumes {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	ledger.Volumes = nil
	hashes := map[string]string{}
	targets := []createRootCRLStateTarget{}
	for i, id := range ids {
		owner := saved.Descriptor.OriginalVolumes[id]
		ledger.Volumes = append(ledger.Volumes, owner)
		hashes[id] = deploymentplan.Digest(owner)
		if i < 2 {
			targets = append(targets, createRootCRLStateTarget{ClaimID: id, ClaimUID: owner.PVCUID})
		}
	}
	originalSliceSHA := deploymentplan.Digest(ledger.Volumes)
	ledger.Volumes = append(ledger.Volumes, *redis)
	if err := saveEnvironmentCreateLedger(context.Background(), actual, ledger); err != nil {
		t.Fatal(err)
	}
	// The real loader rewinds through374/370/361 while retaining explicit
	// authenticated current allocation provenance; the live journal stays437.
	historical := *actual
	historical.volumeExecution = actual
	historical.Journal = actual.Journal
	historical.Journal.Entries = slices.Clone(actual.Journal.Entries[:374])
	historical.Journal.KubernetesLeases = slices.Clone(actual.Journal.KubernetesLeases[:120])
	materials := map[string]string{"accepted-service-core-managed-complete-current-native-volumes": deploymentplan.Digest(hashes)}
	original, consumers, err := readManagedLateAppRootCRLVolumes(&historical, targets, materials)
	if err != nil || len(original) != 18 || len(consumers) != 2 || deploymentplan.Digest(original) != deploymentplan.Digest(saved.Descriptor.OriginalVolumes) {
		t.Fatal("genuine19 allocation changed historical App18", err)
	}
	independent := historical
	independent.volumeExecution = nil
	if err := bindManagedLateRedisRecordedVolumeExecution(store, &independent); err != nil {
		t.Fatal("independent canonical proof reader lost typed allocation", err)
	}
	if independent.volumeExecution == nil || independent.Check(context.Background()) == nil {
		t.Fatal("readonly volume snapshot became a held writer")
	}
	if _, _, err := readManagedLateAppRootCRLVolumes(&independent, targets, materials); err != nil {
		t.Fatal("independent App history cannot retain original18", err)
	}
	projected, err := managedLateRedisHistoricalVolumes(store, &historical, ledger)
	if err != nil || deploymentplan.Digest(projected.Volumes) != originalSliceSHA {
		t.Fatal("historical Log producer volume slice changed", err)
	}
	for _, mode := range []string{"missing-current-provenance", "changed-prefix", "foreign-allocation", "foreign-original", "wrong-material", "unknown-extra"} {
		t.Run(mode, func(t *testing.T) {
			e := historical
			e.Journal = historical.Journal
			e.Journal.Entries = slices.Clone(historical.Journal.Entries)
			l := ledger
			l.Volumes = slices.Clone(ledger.Volumes)
			m := map[string]string{}
			for k, v := range materials {
				m[k] = v
			}
			switch mode {
			case "missing-current-provenance":
				e.volumeExecution = nil
			case "changed-prefix":
				e.Journal.Entries[3].Receipt = []byte(`{"foreign":true}`)
			case "foreign-allocation":
				l.Volumes[18].PVCUID = "99999999-9999-4999-8999-999999999999"
			case "foreign-original":
				l.Volumes[0].NativeID = "999"
			case "wrong-material":
				m["accepted-service-core-managed-complete-current-native-volumes"] = deploymentplan.Digest("different18")
			case "unknown-extra":
				extra := *redis
				extra.PVCName = "unrecorded"
				extra.NativeID = "100"
				extra.PVCUID = "88888888-8888-4888-8888-888888888888"
				extra.PVUID = "77777777-7777-4777-8777-777777777777"
				l.Volumes = append(l.Volumes, extra)
			}
			if err := saveEnvironmentCreateLedger(context.Background(), actual, l); err != nil {
				t.Fatal(err)
			}
			if _, _, err := readManagedLateAppRootCRLVolumes(&e, targets, m); err == nil {
				t.Fatal("unqualified historical allocation accepted")
			}
			if err := saveEnvironmentCreateLedger(context.Background(), actual, ledger); err != nil {
				t.Fatal(err)
			}
		})
	}
	full, err := readEnvironmentCreateLedger(actual)
	if err != nil || len(full.Volumes) != 19 {
		t.Fatal("history projection rewrote full ledger", err)
	}
	if len(actual.Journal.Entries) != 437 || actual.Journal.Entries[436].State != "pending" {
		t.Fatal("pure historical reader changed pending migration")
	}
}
