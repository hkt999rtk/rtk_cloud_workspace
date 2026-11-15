package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestCIRolloutDetectsMigrationChangesWithoutDatabaseCredentials(t *testing.T) {
	repo := "hkt999rtk/rtk_billing"
	old, new := strings.Repeat("a", 40), strings.Repeat("b", 40)
	read := func(_ context.Context, _ string, revision string) (string, error) {
		if revision == old || revision == new {
			return "same-tree", nil
		}
		return "", errors.New("unknown revision")
	}
	if err := checkCIRolloutMigrationChange(context.Background(), repo, old, new, read); err != nil {
		t.Fatal(err)
	}
	changed := func(_ context.Context, _ string, revision string) (string, error) { return revision, nil }
	if err := checkCIRolloutMigrationChange(context.Background(), repo, old, new, changed); !errors.Is(err, errCIRolloutMigrationChanged) {
		t.Fatalf("migration change not routed to full deployment: %v", err)
	}
	if err := checkCIRolloutMigrationChange(context.Background(), "hkt999rtk/rtk_cloud_frontend", "", "", nil); err != nil {
		t.Fatal("frontend should not need database checks", err)
	}
}
