package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCatalogPathInventoryIsPerValidationAndPreservesSpaces(t *testing.T) {
	workspace := t.TempDir()
	git := func(args ...string) {
		t.Helper()
		command := exec.Command("git", args...)
		command.Dir = workspace
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("local Git fixture: %v %s", err, output)
		}
	}
	git("init")
	if err := os.WriteFile(filepath.Join(workspace, "tracked with spaces.txt"), []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "tracked with spaces.txt")
	first := catalogChangePathValidator(workspace)
	if err := first("tracked with spaces.txt"); err != nil {
		t.Fatal("tracked spaces were split", err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "new.txt"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	git("add", "new.txt")
	if first("new.txt") == nil {
		t.Fatal("validation changed its input snapshot halfway through")
	}
	if err := catalogChangePathValidator(workspace)("new.txt"); err != nil {
		t.Fatal("a new validation reused stale Git state", err)
	}
}
