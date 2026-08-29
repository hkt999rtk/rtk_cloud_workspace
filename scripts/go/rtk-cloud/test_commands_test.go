package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
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

func TestInstallPlaywrightRetriesLinuxLockContention(t *testing.T) {
	originalCommand := playwrightInstallCommand
	originalSleep := playwrightInstallSleep
	t.Cleanup(func() {
		playwrightInstallCommand = originalCommand
		playwrightInstallSleep = originalSleep
	})

	attempts := 0
	sleeps := 0
	playwrightInstallCommand = func(dir, name string, args ...string) error {
		attempts++
		if dir != "/web" || name != "npx" || strings.Join(args, " ") != "playwright install --with-deps chromium" {
			t.Fatalf("unexpected install command: dir=%q name=%q args=%v", dir, name, args)
		}
		if attempts == 1 {
			return errors.New("dpkg lock busy")
		}
		return nil
	}
	playwrightInstallSleep = func(delay time.Duration) {
		sleeps++
		if delay != 15*time.Second {
			t.Fatalf("retry delay = %s", delay)
		}
	}
	if err := installPlaywright("/web", "linux"); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || sleeps != 1 {
		t.Fatalf("attempts=%d sleeps=%d, want 2 and 1", attempts, sleeps)
	}

	playwrightInstallCommand = func(string, string, ...string) error {
		return errors.New("browser download failed")
	}
	if err := installPlaywright("/web", "darwin"); err == nil || !strings.Contains(err.Error(), "after 1 attempt") {
		t.Fatalf("non-Linux install error = %v", err)
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
