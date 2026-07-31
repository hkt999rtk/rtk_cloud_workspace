package main

import (
	"path/filepath"
	"testing"
)

func TestResolveUIEvidenceOutputRoot(t *testing.T) {
	workspace := t.TempDir()
	defaultRoot, err := resolveUIEvidenceOutputRoot(workspace, "run-1", "")
	if err != nil {
		t.Fatal(err)
	}
	wantDefault := filepath.Join(workspace, ".artifacts", "test-runs", "run-1", "ui")
	if defaultRoot != wantDefault {
		t.Fatalf("default output root = %q, want %q", defaultRoot, wantDefault)
	}

	customRoot, err := resolveUIEvidenceOutputRoot(workspace, "run-1", ".artifacts/test-runs/run-1/deterministic/ui")
	if err != nil {
		t.Fatal(err)
	}
	wantCustom := filepath.Join(workspace, ".artifacts", "test-runs", "run-1", "deterministic", "ui")
	if customRoot != wantCustom {
		t.Fatalf("custom output root = %q, want %q", customRoot, wantCustom)
	}

	for _, unsafe := range []string{".", ".artifacts/test-runs", "../outside"} {
		if _, err := resolveUIEvidenceOutputRoot(workspace, "run-1", unsafe); err == nil {
			t.Fatalf("unsafe output root %q was accepted", unsafe)
		}
	}
}
