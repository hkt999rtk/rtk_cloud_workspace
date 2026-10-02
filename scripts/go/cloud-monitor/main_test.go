package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	m "rtk-cloud-workspace/scripts/go/internal/cloudmonitor"
	"testing"
	"time"
)

func cliFixture(t *testing.T) (string, string) {
	t.Helper()
	dir := t.TempDir()
	inv := m.Inventory{SchemaVersion: 1, Environment: "dev", Stack: "video-cloud-dev", SourceFingerprint: "fixture", Targets: []m.WorkloadTarget{{ID: "edge", Service: "edge", Kind: "external", Name: "edge", Enabled: true, Required: true}}}
	raw, _ := json.Marshal(inv)
	os.WriteFile(filepath.Join(dir, "inventory.json"), raw, 0600)
	c := m.Config{SchemaVersion: 1, Environment: "dev", Stack: "video-cloud-dev", InventoryFile: "inventory.json"}
	raw, _ = json.Marshal(c)
	path := filepath.Join(dir, "config.json")
	os.WriteFile(path, raw, 0600)
	return path, dir
}

func TestCLIRejectsInvalidInvocation(t *testing.T) {
	for _, args := range [][]string{nil, {"wrong"}, {"validate"}, {"validate", "--bad"}, {"validate", "--environment", "dev", "--config", "missing", "extra"}, {"watch", "--environment", "dev", "--config", "missing", "--duration", "-1s"}} {
		if n := run(args); n != 2 {
			t.Fatalf("args=%v exit=%d", args, n)
		}
	}
	if run([]string{"validate", "--help"}) != 0 {
		t.Fatal("help")
	}
}

func TestCLIValidateReadOnlyAndMismatch(t *testing.T) {
	p, dir := cliFixture(t)
	if run([]string{"validate", "--environment", "dev", "--config", p, "--out-dir", filepath.Join(dir, "unused")}) != 0 {
		t.Fatal("validation failed")
	}
	if _, e := os.Stat(filepath.Join(dir, "unused")); !os.IsNotExist(e) {
		t.Fatal("validate created output")
	}
	if run([]string{"validate", "--environment", "prod", "--config", p}) != 2 {
		t.Fatal("environment mismatch accepted")
	}
}

func TestCLICheckMissingEvidenceAndReportFailure(t *testing.T) {
	p, dir := cliFixture(t)
	out := filepath.Join(dir, "output")
	args := []string{"check", "--environment", "dev", "--config", p, "--config-root", dir, "--workspace", dir, "--out-dir", out, "--skip-report"}
	if run(args) != 1 {
		t.Fatal("unknown evidence should exit1")
	}
	if _, e := os.Stat(filepath.Join(out, "history", "dev", "latest.json")); e != nil {
		t.Fatal(e)
	}
	if run(args[:len(args)-1]) != 3 {
		t.Fatal("missingrenderer should exit3 aftersuccessfulcheck")
	}
}

func TestCLIReportBoundsAndWatchCancellation(t *testing.T) {
	p, dir := cliFixture(t)
	out := filepath.Join(dir, "output")
	args := []string{"report", "--environment", "dev", "--config", p, "--out-dir", out}
	for _, flags := range [][]string{{"--from", "bad"}, {"--to", "bad"}, {"--from", "2026-10-03T00:00:00Z", "--to", "2026-10-02T00:00:00Z"}, nil} {
		if run(append(append([]string{}, args...), flags...)) != 2 {
			t.Fatal("invalidbounds/missingrenderer accepted")
		}
	}
	start := time.Now()
	if run([]string{"watch", "--environment", "dev", "--config", p, "--out-dir", out, "--duration", "1ns"}) != 0 {
		t.Fatal("watch cancelled")
	}
	if time.Since(start) > time.Second {
		t.Fatal("watch failed to respectdeadline")
	}
}
