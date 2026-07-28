package main

import (
	"reflect"
	"strings"
	"testing"
)

func TestTestLayerCommandsAreRegistered(t *testing.T) {
	for _, name := range []string{"test-catalog", "test-coverage", "test-matrix", "test-services", "test-e2e", "test-ui", "test-live", "test-spec-inventory", "test-spec-impact"} {
		if _, ok := commands[name]; !ok {
			t.Fatalf("command %q is not registered", name)
		}
	}
}

func TestPlaywrightInstallArgumentsIncludeLinuxSystemDependencies(t *testing.T) {
	if got, want := strings.Join(playwrightInstallArguments("linux"), " "), "playwright install --with-deps chromium"; got != want {
		t.Fatalf("linux install arguments = %q, want %q", got, want)
	}
	if got, want := strings.Join(playwrightInstallArguments("darwin"), " "), "playwright install chromium"; got != want {
		t.Fatalf("darwin install arguments = %q, want %q", got, want)
	}
}

func TestLiveCommandFlagHelpers(t *testing.T) {
	args := []string{"--run", "--run-id=run-1", "--workspace", "/workspace", "--out-dir", "/evidence"}
	if got := commandFlagValue(args, "--run-id"); got != "run-1" {
		t.Fatalf("run ID = %q", got)
	}
	if got := commandFlagValue(args, "--workspace"); got != "/workspace" {
		t.Fatalf("workspace = %q", got)
	}
	want := []string{"--run", "--workspace", "/workspace", "--out-dir", "/evidence"}
	if got := removeFlagValue(args, "--run-id"); !reflect.DeepEqual(got, want) {
		t.Fatalf("args without run ID = %v, want %v", got, want)
	}
}

func TestTestLiveDefaultsToPlan(t *testing.T) {
	args := []string{"--env-root", "/tmp/test-runtime"}
	planned := ensureTestLiveMode(args)
	if !hasFlag(planned, "--plan") {
		t.Fatal("test-live should add --plan when no execution mode is provided")
	}
	if hasFlag(args, "--plan") {
		t.Fatal("test-live mutated its input arguments")
	}
}
